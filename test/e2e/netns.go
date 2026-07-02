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
// egress.
type pathSpec struct {
	name     string
	edgeIP   string
	concIP   string
	edgeVeth string // <=15 chars
	concVeth string
	delayMs  int
	jitterMs int
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

// Setup builds the two-path topology. It requires CAP_NET_ADMIN, which the e2e
// TestMain provides via an unprivileged user+net namespace (`unshare -Urmn`).
func Setup(t *testing.T) *Topology {
	t.Helper()
	top := &Topology{t: t, paths: DefaultPaths}

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
		top.run("ip", "link", "add", p.edgeVeth, "type", "veth", "peer", "name", p.concVeth)
		top.run("ip", "link", "set", p.concVeth, "netns", pid)
		top.run("ip", "addr", "add", p.edgeIP+"/24", "dev", p.edgeVeth)
		top.run("ip", "link", "set", p.edgeVeth, "up")
		top.nsenter("ip", "addr", "add", p.concIP+"/24", "dev", p.concVeth)
		top.nsenter("ip", "link", "set", p.concVeth, "up")
		qargs := append([]string{"qdisc", "add", "dev", p.edgeVeth, "root", "netem"}, top.delayArgs(p)...)
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

func (top *Topology) delayArgs(p pathSpec) []string {
	args := []string{"delay", fmt.Sprintf("%dms", p.delayMs)}
	if p.jitterMs > 0 {
		args = append(args, fmt.Sprintf("%dms", p.jitterMs))
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

// InjectLoss sets uniform egress loss (percent) on the named path, preserving its
// delay/jitter profile.
func (top *Topology) InjectLoss(name string, pct float64) {
	p := top.path(name)
	args := append([]string{"qdisc", "change", "dev", p.edgeVeth, "root", "netem"}, top.delayArgs(p)...)
	args = append(args, "loss", fmt.Sprintf("%g%%", pct))
	top.run("tc", args...)
}

// ClearLoss restores the named path to delay/jitter only.
func (top *Topology) ClearLoss(name string) {
	p := top.path(name)
	args := append([]string{"qdisc", "change", "dev", p.edgeVeth, "root", "netem"}, top.delayArgs(p)...)
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

// nsenter runs a command inside the concentrator network namespace.
func (top *Topology) nsenter(args ...string) {
	top.t.Helper()
	full := append([]string{"-t", strconv.Itoa(top.pid), "-n"}, args...)
	top.run("nsenter", full...)
}
