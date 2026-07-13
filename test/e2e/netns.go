//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// pathSpec describes one emulated WAN uplink between the edge and concentrator
// namespaces: a veth pair carrying a /24, with netem delay+jitter on the edge
// egress. The bandwidth cap (rateMbit) and controlled loss (lossPct) are
// OPTIONAL config-time impairments: both default to zero, so the DefaultPaths
// topology stays uncapped and lossless and every existing P0/P1 e2e test runs
// unchanged. A non-zero rateMbit makes the LINK — not the single-core userspace
// WG crypto — the bottleneck ONLY when rateMbit sits below the EXECUTING host's
// measured in-fixture tunnel ceiling. That ceiling is CPU/PPS-bound (both daemons
// plus the load generator share the host's cores), a lower bound that scales with
// core count, NOT a link-throughput spec: ~12–46 Mbit/s single-flow on a 1-vCPU
// aarch64 host (docs/p0-findings.md:216-225), ~13 Mbit/s single-path (up to
// ~47–87 Mbit/s FEC single-flow) on a 4-vCPU amd64 host. Sizing rule: cap < ceiling
// for single-path, 2×cap < ceiling for aggregation. Below the ceiling
// a standing queue can form for bufferbloat/pacing (T21/T23) work; a non-zero
// lossPct injects uniform egress loss at Setup time for FEC-recovery (T25/T29)
// work.
type pathSpec struct {
	name     string
	edgeIP   string
	concIP   string
	edgeVeth string // <=15 chars
	concVeth string
	delayMs  int
	jitterMs int
	rateMbit int     // optional per-path bandwidth cap (netem rate); 0 = uncapped
	lossPct  float64 // optional config-time uniform egress loss (netem loss); 0 = lossless

	// deferEdgeAddr, when true, makes Setup create and bring up the edge veth WITHOUT
	// assigning edgeIP to it: the interface exists (so tc/netem still applies and the
	// link is up) but the configured source_addr is not yet owned by any interface —
	// the well-formed-but-not-yet-assignable condition T51's tolerant Open() defers and
	// T55's background reconcile later promotes (T60). AddEdgeAddr adds the withheld
	// address later. Zero value (false) is the default for every existing path, so
	// Setup's behaviour for DefaultPaths and every other caller is unchanged.
	deferEdgeAddr bool
}

// DefaultPaths are the two emulated links: Starlink-like (low latency, jittery)
// and 5G-like (higher latency, stable).
var DefaultPaths = []pathSpec{
	{name: "starlink", edgeIP: "10.100.1.1", concIP: "10.100.1.2", edgeVeth: "wbAe", concVeth: "wbAc", delayMs: 45, jitterMs: 10},
	{name: "cellular", edgeIP: "10.100.2.1", concIP: "10.100.2.2", edgeVeth: "wbBe", concVeth: "wbBc", delayMs: 64, jitterMs: 0},
}

// Topology is a two-namespace netns/netem fixture. The edge side is the current
// (re-exec'd) network namespace; the concentrator side is a child process's
// network namespace, addressed by PID so no writable /run/netns is required.
type Topology struct {
	t      *testing.T
	holder *exec.Cmd
	pid    int
	paths  []pathSpec
}

// Setup builds the two-path topology from DefaultPaths (uncapped, lossless). It
// requires CAP_NET_ADMIN, which the e2e TestMain provides via an unprivileged
// user+net namespace (`unshare -Urmn`).
func Setup(t *testing.T) *Topology {
	return SetupWithPaths(t, DefaultPaths)
}

// SetupWithPaths builds the topology from an explicit path set, allowing a test
// to opt into the optional per-path bandwidth cap (rateMbit) and/or config-time
// loss (lossPct). DefaultPaths leaves both zero, so Setup is unchanged.
func SetupWithPaths(t *testing.T, paths []pathSpec) *Topology {
	t.Helper()
	top := &Topology{t: t, paths: paths}

	// Hold the concentrator network namespace open with a sleeping child.
	top.holder = exec.Command("unshare", "-n", "sleep", "600")
	if err := top.holder.Start(); err != nil {
		t.Fatalf("start concentrator netns holder: %v", err)
	}
	top.pid = top.holder.Process.Pid
	top.waitForNetns()

	pid := strconv.Itoa(top.pid)
	top.run("ip", "link", "set", "lo", "up")
	top.nsenter("ip", "link", "set", "lo", "up")
	for _, p := range top.paths {
		// Idempotent pre-delete: the veth names are FIXED per path, so a prior subtest's
		// teardown racing the kernel's async netns/veth reap can leave the edge veth behind,
		// making `ip link add` fail with EEXIST ("File exists"). Deleting the pair first
		// (ignore-if-absent) makes Setup robust to reused fixed names across sequential
		// subtests. Deleting the edge end removes its peer too.
		_ = top.tryRun("ip", "link", "del", p.edgeVeth)
		top.run("ip", "link", "add", p.edgeVeth, "type", "veth", "peer", "name", p.concVeth)
		top.run("ip", "link", "set", p.concVeth, "netns", pid)
		if !p.deferEdgeAddr {
			top.run("ip", "addr", "add", p.edgeIP+"/24", "dev", p.edgeVeth)
		}
		top.run("ip", "link", "set", p.edgeVeth, "up")
		top.nsenter("ip", "addr", "add", p.concIP+"/24", "dev", p.concVeth)
		top.nsenter("ip", "link", "set", p.concVeth, "up")
		qargs := append([]string{"qdisc", "add", "dev", p.edgeVeth, "root", "netem"}, top.netemArgs(p)...)
		top.run("tc", qargs...)
	}
	t.Cleanup(top.Teardown)
	return top
}

// waitForNetns blocks until the holder's network namespace is observable.
func (top *Topology) waitForNetns() {
	path := fmt.Sprintf("/proc/%d/ns/net", top.pid)
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	top.t.Fatalf("concentrator netns %s never appeared", path)
}

// netemArgs builds the netem parameter list for a path's baseline impairment
// profile: delay/jitter, then its configured loss (lossPct) and bandwidth cap
// (rateMbit) if set. For a DefaultPaths entry (both zero) this reduces to the
// prior delay/jitter-only output, so existing qdisc setup is byte-identical.
func (top *Topology) netemArgs(p pathSpec) []string {
	return top.netemArgsWithLoss(p, p.lossPct)
}

// netemArgsWithLoss is netemArgs with the loss percentage overridden — used by
// InjectLoss to set runtime loss while preserving delay/jitter and any rate cap.
// netem accepts the options in delay/loss/rate order.
func (top *Topology) netemArgsWithLoss(p pathSpec, lossPct float64) []string {
	args := []string{"delay", fmt.Sprintf("%dms", p.delayMs)}
	if p.jitterMs > 0 {
		args = append(args, fmt.Sprintf("%dms", p.jitterMs))
	}
	if lossPct > 0 {
		args = append(args, "loss", fmt.Sprintf("%g%%", lossPct))
	}
	if p.rateMbit > 0 {
		args = append(args, "rate", fmt.Sprintf("%dmbit", p.rateMbit))
	}
	return args
}

// path looks up a path by name, failing the test if absent.
func (top *Topology) path(name string) pathSpec {
	for _, p := range top.paths {
		if p.name == name {
			return p
		}
	}
	top.t.Fatalf("unknown path %q", name)
	return pathSpec{}
}

// RTT pings the concentrator end of the named path from the edge and returns the
// average round-trip time in milliseconds.
func (top *Topology) RTT(name string, count int) float64 {
	p := top.path(name)
	out := top.runOut("ping", "-c", strconv.Itoa(count), "-i", "0.2", "-W", "1", p.concIP)
	// rtt min/avg/max/mdev = 37.264/48.520/82.043/19.354 ms
	idx := strings.Index(out, "min/avg/max")
	if idx < 0 {
		top.t.Fatalf("path %q: no rtt line in ping output:\n%s", name, out)
	}
	eq := strings.Index(out[idx:], "=")
	fields := strings.Fields(out[idx+eq+1:])
	nums := strings.Split(fields[0], "/")
	avg, err := strconv.ParseFloat(nums[1], 64)
	if err != nil {
		top.t.Fatalf("path %q: parse avg rtt %q: %v", name, nums[1], err)
	}
	return avg
}

// Reachable reports whether the concentrator end of the named path answers pings.
func (top *Topology) Reachable(name string, count int) bool {
	p := top.path(name)
	return top.tryRun("ping", "-c", strconv.Itoa(count), "-i", "0.2", "-W", "1", p.concIP) == nil
}

// InjectLoss sets uniform egress loss (percent) on the named path at runtime,
// preserving its delay/jitter profile and any configured bandwidth cap.
func (top *Topology) InjectLoss(name string, pct float64) {
	p := top.path(name)
	args := append([]string{"qdisc", "change", "dev", p.edgeVeth, "root", "netem"}, top.netemArgsWithLoss(p, pct)...)
	top.run("tc", args...)
}

// ClearLoss restores the named path to its configured baseline impairment
// (delay/jitter, plus any config-time loss and bandwidth cap). For a DefaultPaths
// entry that is delay/jitter only.
func (top *Topology) ClearLoss(name string) {
	p := top.path(name)
	args := append([]string{"qdisc", "change", "dev", p.edgeVeth, "root", "netem"}, top.netemArgs(p)...)
	top.run("tc", args...)
}

// Blackhole brings the named path's edge link down, simulating a WAN death (used
// by the P1 failover test).
func (top *Topology) Blackhole(name string) {
	p := top.path(name)
	top.run("ip", "link", "set", p.edgeVeth, "down")
}

// Restore brings the named path's edge link back up.
func (top *Topology) Restore(name string) {
	p := top.path(name)
	top.run("ip", "link", "set", p.edgeVeth, "up")
}

// Readdress replaces the edge-side address of the named path, simulating an edge
// public-IP change (used by the P1 roaming test). newEdgeIP must be a bare IPv4
// address in the path's /24.
func (top *Topology) Readdress(name, newEdgeIP string) {
	p := top.path(name)
	top.run("ip", "addr", "flush", "dev", p.edgeVeth)
	top.run("ip", "addr", "add", newEdgeIP+"/24", "dev", p.edgeVeth)
	top.run("ip", "link", "set", p.edgeVeth, "up")
}

// AddEdgeAddr adds the named path's configured edge-side address to its (already up)
// edge veth, WITHOUT flushing any existing address first (T60). It is the companion
// to a path built with deferEdgeAddr: that path's interface starts addressless, so
// this simulates the address becoming assignable after boot (e.g. a 5G modem's DHCP
// lease completing) for the T55 background reconciler to observe. Unlike Readdress —
// which flushes THEN re-adds, for the T16 re-roam case where an address is replaced —
// this only ADDS, because here there is nothing to flush.
func (top *Topology) AddEdgeAddr(name string) {
	p := top.path(name)
	top.run("ip", "addr", "add", p.edgeIP+"/24", "dev", p.edgeVeth)
}

// QdiscShow returns `tc qdisc show` for the named path (for assertions/debug).
func (top *Topology) QdiscShow(name string) string {
	p := top.path(name)
	return top.runOut("tc", "qdisc", "show", "dev", p.edgeVeth)
}

// LinkExists reports whether the edge-side veth of the named path still exists.
func (top *Topology) LinkExists(name string) bool {
	p := top.path(name)
	return top.tryRun("ip", "link", "show", p.edgeVeth) == nil
}

// Teardown removes the topology. Killing the holder drops the concentrator netns
// (and its veth ends); the edge-side veths are deleted explicitly. Idempotent.
func (top *Topology) Teardown() {
	for _, p := range top.paths {
		_ = top.tryRun("ip", "link", "del", p.edgeVeth)
	}
	if top.holder != nil && top.holder.Process != nil {
		_ = top.holder.Process.Kill()
		_, _ = top.holder.Process.Wait()
		top.holder = nil
	}
}

func (top *Topology) run(name string, args ...string) {
	top.t.Helper()
	if out, err := exec.Command(name, args...).CombinedOutput(); err != nil {
		top.t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
}

func (top *Topology) tryRun(name string, args ...string) error {
	return exec.Command(name, args...).Run()
}

func (top *Topology) runOut(name string, args ...string) string {
	top.t.Helper()
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		top.t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return string(out)
}

// iperfListenTimeout bounds waitIperfListen: how long to wait for the one-shot
// iperf3 server to reach the LISTEN state before failing the test.
const iperfListenTimeout = 5 * time.Second

// waitIperfListen polls, inside the concentrator network namespace, for a TCP
// socket in the LISTEN state on the given port, and returns once it appears (or
// fails the test at iperfListenTimeout). It reads kernel socket state via
// `ss -ltn` and NEVER connects: the iperf3 servers here run one-shot (`-s -1`),
// so a probe-connect would consume the server's single accept and break the real
// client. It replaces the prior fixed sleeps at the iperf3Mbps and rttUnderLoad
// call sites, which raced a slow bind under load into 'connection refused' (D3).
func (top *Topology) waitIperfListen(t *testing.T, port int) {
	t.Helper()
	suffix := fmt.Sprintf(":%d", port)
	deadline := time.Now().Add(iperfListenTimeout)
	for time.Now().Before(deadline) {
		// ss -ltn columns: State Recv-Q Send-Q Local-Address:Port Peer-Address:Port.
		out, err := exec.Command("nsenter", "-t", strconv.Itoa(top.pid), "-n", "ss", "-ltn").CombinedOutput()
		if err == nil {
			for _, line := range strings.Split(string(out), "\n") {
				fields := strings.Fields(line)
				if len(fields) >= 4 && strings.HasSuffix(fields[3], suffix) {
					return
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("iperf3 server never reached LISTEN on port %d within %s", port, iperfListenTimeout)
}

// nsenter runs a command inside the concentrator network namespace.
func (top *Topology) nsenter(args ...string) {
	top.t.Helper()
	full := append([]string{"-t", strconv.Itoa(top.pid), "-n"}, args...)
	top.run("nsenter", full...)
}
