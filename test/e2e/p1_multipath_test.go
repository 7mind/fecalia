//go:build e2e

package e2e

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/bind"
)

// TestMultipathPerPath is the T12 per-path acceptance: the multipath Bind carries
// tunnel traffic over EACH configured path individually. Each subtest brings the
// bond up with exactly ONE path configured (the other "disabled" by omission) and
// verifies the WireGuard handshake completes and both ICMP and a TCP bulk transfer
// flow over that single path's source-bound socket + outer DATA framing.
func TestMultipathPerPath(t *testing.T) {
	bin := buildWanbond(t)
	for _, p := range DefaultPaths {
		p := p
		t.Run(p.name, func(t *testing.T) {
			top := Setup(t)
			edge, conc := setupMultipathTunnel(t, top, bin, []pathSpec{p})

			if !top.pingUntil(concInner, 15*time.Second) {
				t.Fatalf("path %q: tunnel never came up\n--- edge ---\n%s\n--- conc ---\n%s",
					p.name, edge.log(), conc.log())
			}
			mbps := top.iperf3Mbps(t, concInner, 3)
			if mbps <= 0 {
				t.Fatalf("path %q: iperf3 non-positive throughput %.2f Mbit/s", p.name, mbps)
			}
			t.Logf("multipath single-path [%s]: handshake + ping OK, iperf3 = %.1f Mbit/s", p.name, mbps)
		})
	}
}

// TestMultipathBothPathsAndFailover brings the bond up with BOTH paths configured,
// confirms the tunnel is up, then blackholes the secondary path and confirms the
// tunnel still carries traffic over the primary — demonstrating each path is an
// independent socket beneath the one virtual endpoint (the engine sees no churn).
// (Runtime scheduling/failover across paths is T15; T12's policy is "first healthy
// path", so the primary continues to carry traffic while the secondary is down.)
func TestMultipathBothPathsAndFailover(t *testing.T) {
	bin := buildWanbond(t)
	top := Setup(t)
	edge, conc := setupMultipathTunnel(t, top, bin, DefaultPaths)

	if !top.pingUntil(concInner, 15*time.Second) {
		t.Fatalf("bond never came up\n--- edge ---\n%s\n--- conc ---\n%s", edge.log(), conc.log())
	}

	// Blackhole the secondary path; the primary (first configured) still carries
	// traffic.
	top.Blackhole(DefaultPaths[1].name)
	if !top.pingUntil(concInner, 10*time.Second) {
		t.Fatalf("bond lost connectivity after blackholing secondary path %q\n--- edge ---\n%s",
			DefaultPaths[1].name, edge.log())
	}
	top.Restore(DefaultPaths[1].name)
	t.Logf("multipath both-paths: up over %d paths; survived blackhole of %q", len(DefaultPaths), DefaultPaths[1].name)
}

// TestMultipathInnerMTU asserts the TUN MTU the daemon set equals the computed
// inner MTU = path MTU − (outer IP/UDP + outer DATA frame + WG overhead), the
// fixture value pinned by the bind package's unit test.
func TestMultipathInnerMTU(t *testing.T) {
	bin := buildWanbond(t)
	top := Setup(t)
	setupMultipathTunnel(t, top, bin, DefaultPaths)

	// This fixture runs the daemon with FEC OFF (DefaultPaths carries no [fec] block),
	// so the TUN MTU is the FEC-off inner budget.
	want := bind.InnerMTU(bind.DefaultPathMTU, false)
	got := top.linkMTU(t, tunDev, false)
	if got != want {
		t.Fatalf("edge %s MTU = %d, want computed inner MTU %d", tunDev, got, want)
	}
	t.Logf("multipath inner MTU: %s MTU = %d (= %d path MTU − %d IP/UDP − %d DATA frame − %d WG)",
		tunDev, got, bind.DefaultPathMTU, bind.IPv4UDPOverhead, 40, bind.WGTransportOverhead)
}

// TestMultipathNoFragmentation sends a max-inner-MTU payload with the DF bit set
// through the tunnel and asserts, from a capture on the edge egress veth, that no
// outer datagram is IP-fragmented — the MTU accounting keeps a full inner packet
// inside the path MTU (no fragmentation, no PMTUD black hole).
func TestMultipathNoFragmentation(t *testing.T) {
	bin := buildWanbond(t)
	top := Setup(t)
	p := top.path("starlink")
	edge, _ := setupMultipathTunnel(t, top, bin, []pathSpec{p})

	if !top.pingUntil(concInner, 15*time.Second) {
		t.Fatalf("tunnel never came up\n%s", edge.log())
	}

	inner := bind.InnerMTU(bind.DefaultPathMTU, false)
	// ICMP echo payload that exactly fills the inner MTU: inner − 20 (IP) − 8 (ICMP).
	payload := inner - 28

	// Capture fragments on the edge egress veth while a DF-set, max-size ping runs.
	frags := top.captureFragments(t, p.edgeVeth, 6*time.Second, func() {
		out, err := exec.Command("ping", "-c", "5", "-s", strconv.Itoa(payload), "-M", "do", "-W", "2", concInner).CombinedOutput()
		if err != nil {
			t.Fatalf("DF max-MTU ping (payload %d) failed — inner packet does not fit the budget:\n%s", payload, out)
		}
	})
	if frags != 0 {
		t.Fatalf("observed %d IP-fragmented outer datagrams on %s during a max-inner-MTU transfer; MTU accounting is wrong", frags, p.edgeVeth)
	}
	t.Logf("multipath no-fragmentation: %d-byte inner packets (payload %d) produced no IP fragments on %s", inner, payload, p.edgeVeth)
}

// setupMultipathTunnel brings the multipath tunnel up over the given paths: the
// edge binds one source-addr'd socket per path and targets each path's
// concentrator address via dest_addr; the concentrator binds one socket per path
// on its own address. It addresses both TUNs and returns the running processes.
func setupMultipathTunnel(t *testing.T, top *Topology, bin string, paths []pathSpec) (edge, conc *proc) {
	t.Helper()
	return setupMultipathTunnelLevel(t, top, bin, paths, "error")
}

// setupMultipathTunnelLevel is setupMultipathTunnel with an explicit daemon log
// level. The P1 failover measurement (TestP1Failover) runs both daemons at "info"
// so the scheduler's per-direction "active path change" transitions are captured in
// each process's log for the root-cause evidence; every other caller uses "error".
func setupMultipathTunnelLevel(t *testing.T, top *Topology, bin string, paths []pathSpec, level string) (edge, conc *proc) {
	t.Helper()

	edgePriv, edgePub := genKey(t)
	concPriv, concPub := genKey(t)
	psk := randKey(t)

	var edgePaths, concPaths strings.Builder
	for _, p := range paths {
		fmt.Fprintf(&edgePaths, "[[paths]]\nname = %q\nsource_addr = %q\ndest_addr = \"%s:%d\"\n\n", p.name, p.edgeIP, p.concIP, listenPort)
		fmt.Fprintf(&concPaths, "[[paths]]\nname = %q\nsource_addr = %q\n\n", p.name, p.concIP)
	}
	// The wireguard peer endpoint (edge -> concentrator) seeds the virtual
	// endpoint; the primary path's concentrator address serves.
	primary := paths[0]

	dir := t.TempDir()
	edgeCfg := writeConfig(t, filepath.Join(dir, "edge.toml"), fmt.Sprintf(`role = "edge"
psk = "%s"

%s[wireguard]
private_key = "%s"

[[wireguard.peers]]
public_key = "%s"
endpoint = "%s:%d"
allowed_ips = ["%s/32"]

[log]
level = %q
`, psk, edgePaths.String(), edgePriv, concPub, primary.concIP, listenPort, concInner, level))

	concCfg := writeConfig(t, filepath.Join(dir, "conc.toml"), fmt.Sprintf(`role = "concentrator"
psk = "%s"

%s[wireguard]
private_key = "%s"
listen_port = %d

[[wireguard.peers]]
public_key = "%s"
allowed_ips = ["%s/32"]

[log]
level = %q
`, psk, concPaths.String(), concPriv, listenPort, edgePub, edgeInner, level))

	conc = top.startProc(t, "concentrator", "nsenter", "-t", strconv.Itoa(top.pid), "-n", bin, "--config", concCfg)
	edge = top.startProc(t, "edge", bin, "--config", edgeCfg)

	if !top.waitLink(tunDev, false, 5*time.Second) {
		t.Fatalf("edge %s never appeared\n%s", tunDev, edge.log())
	}
	if !top.waitLink(tunDev, true, 5*time.Second) {
		t.Fatalf("concentrator %s never appeared\n%s", tunDev, conc.log())
	}
	top.run("ip", "addr", "add", edgeInner+"/24", "dev", tunDev)
	top.run("ip", "link", "set", tunDev, "up")
	top.nsenter("ip", "addr", "add", concInner+"/24", "dev", tunDev)
	top.nsenter("ip", "link", "set", tunDev, "up")
	return edge, conc
}

// linkMTU returns the MTU of dev (in the peer netns when ns is true).
func (top *Topology) linkMTU(t *testing.T, dev string, ns bool) int {
	t.Helper()
	var out string
	if ns {
		out = top.runOut("nsenter", "-t", strconv.Itoa(top.pid), "-n", "ip", "-o", "link", "show", dev)
	} else {
		out = top.runOut("ip", "-o", "link", "show", dev)
	}
	// ... mtu 1401 ...
	fields := strings.Fields(out)
	for i, f := range fields {
		if f == "mtu" && i+1 < len(fields) {
			mtu, err := strconv.Atoi(fields[i+1])
			if err != nil {
				t.Fatalf("parse mtu %q: %v", fields[i+1], err)
			}
			return mtu
		}
	}
	t.Fatalf("no mtu field in %q", out)
	return 0
}

// captureFragments runs tcpdump on veth for the duration of during(), counting
// IP-fragmented datagrams (tcpdump annotates them "(frag ...)").
func (top *Topology) captureFragments(t *testing.T, veth string, d time.Duration, during func()) int {
	t.Helper()
	secs := int(d.Seconds())
	if secs < 1 {
		secs = 1
	}
	cap := exec.Command("timeout", strconv.Itoa(secs), "tcpdump", "-n", "-l", "-i", veth)
	out := &lockedBuffer{}
	cap.Stdout, cap.Stderr = out, out
	if err := cap.Start(); err != nil {
		t.Fatalf("start tcpdump on %s: %v", veth, err)
	}
	time.Sleep(700 * time.Millisecond) // let tcpdump attach before generating traffic
	during()
	time.Sleep(300 * time.Millisecond) // let trailing packets flush to the capture
	_ = cap.Wait()                     // timeout terminates it

	count := 0
	for _, line := range strings.Split(out.String(), "\n") {
		if strings.Contains(line, "frag ") {
			count++
		}
	}
	return count
}
