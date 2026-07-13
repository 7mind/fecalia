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

	"github.com/7mind/wanbond/internal/telemetry"
)

// T62 is the privileged netns e2e for the Q18 concentrator hub-failover feature
// (implemented in T57, internal/device/failover.go). It validates the edge-side
// active-standby switch END TO END: when EVERY path's liveness to the active
// concentrator (hub#1) goes DOWN simultaneously (HUB LOSS — the concentrator itself
// is gone, distinct from a single uplink dying), the edge advances to the next
// ordered endpoint (hub#2, a SEPARATE concentrator sharing the peer's SAME WireGuard
// static key), repoints the bond's remote, re-handshakes a FRESH session, and
// resumes carrying traffic through the standby — and the single-endpoint GUARD takes
// NO such action.
//
// TOPOLOGY (why a bridge). The two hubs must be (a) INDEPENDENTLY killable and (b)
// BOTH reachable from the edge's uplink, because the hub-failover switch repoints the
// WHOLE bond UNIFORMLY at ONE standby address (bind.Multipath.SetPeerRemote — invariant
// A1: the per-path fan-out beneath the single virtual endpoint is retargeted, the
// engine still sees one peer). A single edge uplink that can reach two distinct hub
// IPs models the REAL scenario — one WAN, the active concentrator dies, switch to a
// standby concentrator at a different public IP. In netns that shared medium is an L2
// bridge in the edge namespace with the edge and both hubs on one /24:
//
//	edge netns:            bridge wbHfbr = 10.100.0.1/24  (the edge uplink source_addr)
//	                        │
//	          ┌─────────────┴─────────────┐   (two bridge ports, two veth pairs)
//	   wbHfe1 │                     wbHfe2 │
//	   hub#1 netns:            hub#2 netns:
//	   wbHfc1 = 10.100.0.2/24   wbHfc2 = 10.100.0.3/24
//	   concentrator (hubKey)    concentrator (hubKey)   ← SAME WireGuard static key
//
// Both hubs run a concentrator daemon with the IDENTICAL WireGuard private key, so the
// edge's single peer (one public key) re-handshakes to EITHER — the standby is a fresh
// WG session to the same peer identity, NO hub-to-hub state handoff. Each hub is its
// own network namespace (a separate holder), so each owns its own `wanbond0` TUN
// (device.defaultTUNName is fixed, so two daemons cannot share one namespace) and is
// killed independently by bringing its bridge port down.
//
// ALL-PATHS-DOWN is driven by bringing hub#1's bridge port down (a REAL hub outage):
// the edge's probes to 10.100.0.2 are dropped at the bridge, so its prober(s) go
// StateDown together — exactly the liveness plane the controller keys on — while
// 10.100.0.3 (hub#2) stays reachable and the edge's own source (10.100.0.1 on the
// bridge) is untouched, so the re-handshake toward hub#2 can egress.
//
// The topology uses a SINGLE edge path deliberately: the controller's decision
// ("EVERY prober StateDown") and action (advance + SetPeerRemote + re-handshake) are
// identical for any path count, and after a uniform repoint the whole bond targets one
// hub socket regardless. The multi-path "ALL down, not just one" discrimination (a
// PARTIAL-down set takes no action) is already covered by T57's device-package unit
// test; this e2e proves the end-to-end switch on the real wire.

const (
	// Bridge + addresses for the two-hub failover fabric. All on one /24 so every
	// node is L2-adjacent (no cross-subnet routing, no return routes). Veth names are
	// <=15 chars (kernel IFNAMSIZ) and distinct from every other e2e file's veths.
	hfBridge   = "wbHfbr"
	hfEdgeIP   = "10.100.0.1"
	hfHub1IP   = "10.100.0.2"
	hfHub2IP   = "10.100.0.3"
	hfHub1EP   = hfHub1IP + ":51820" // active concentrator endpoint (Endpoints[0])
	hfHub2EP   = hfHub2IP + ":51820" // standby concentrator endpoint (Endpoints[1])
	hfHub1Port = "wbHfe1"            // edge/bridge-side veth to hub#1 (brought DOWN to kill hub#1)
	hfHub1Ceth = "wbHfc1"            // hub#1-side veth
	hfHub2Port = "wbHfe2"            // edge/bridge-side veth to hub#2
	hfHub2Ceth = "wbHfc2"            // hub#2-side veth
	hfPathName = "uplink"            // the single edge path's name (the metrics series key)

	// hfMetricsListen is the edge /metrics endpoint for this file, on a port none of
	// the other e2e files use (p2/p3/p4/pacing/tolerant use 9095-9098).
	hfMetricsListen = "127.0.0.1:9099"
	hfMetricsURL    = "http://" + hfMetricsListen + "/metrics"
)

// hubFailoverSettleUpperBound mirrors the unexported device.hubFailoverSettle (3 s,
// docs/design.md §"Concentrator hub failover" → "Settle dwell"). It is the dwell a
// freshly-selected endpoint gets before another advance is allowed. It does NOT gate
// the FIRST switch in this test — the controller initialises lastSwitch to boot, and
// the tunnel bring-up + baseline-traffic phase before the kill already exceeds 3 s, so
// the boot dwell has elapsed by the time hub#1 is killed — but it is folded into the
// assertion window below as deterministic slack in case bring-up is unusually fast.
const hubFailoverSettleUpperBound = 3 * time.Second

// hubFailoverWindow bounds how long the edge may take, after hub#1 is killed, to
// detect hub loss, switch to hub#2, re-handshake, and resume carrying traffic. It is
// an ANALYTICAL bound composed from the daemon's own timing constants (the D16
// single-source-of-truth style of thresholds.go), NOT a guessed sleep:
//
//	PLivenessFailoverBudget                     = DownAfter + 2*interval  = 1.6 s  (all-paths-DOWN detect + one interval headroom)
//	+ hubFailoverSettleUpperBound               =                          3.0 s  (settle dwell upper bound; see above — non-gating here)
//	+ DefaultUpSuccesses * DefaultProbeInterval  =                         0.6 s  (standby path UP-recovery: 3 echoes once probes reach hub#2)
//	+ 5 s                                        =                          5.0 s  (WG re-handshake RTT + CPU/PPS jitter on the shared netns fixture)
//	                                             ≈ 10.2 s
//
// The load generators here are ICMP/one-shot iperf3 (not a saturating flow), so the
// jitter term is generous rather than tight. The same window bounds the control test's
// NON-recovery poll, so the guard is given the FULL opportunity the positive path gets.
const hubFailoverWindow = PLivenessFailoverBudget +
	hubFailoverSettleUpperBound +
	time.Duration(telemetry.DefaultUpSuccesses)*telemetry.DefaultProbeInterval +
	5*time.Second

// TestHubFailoverStandbySwitch is the T62 positive acceptance: with an ORDERED
// two-endpoint list [hub#1, hub#2], the bond comes up on hub#1; killing hub#1 (a full
// hub outage, every path's liveness DOWN) makes the edge switch to hub#2, complete a
// FRESH re-handshake, and RESUME carrying traffic through hub#2 — within the analytical
// hub-failover window. The load-bearing proof is traffic-resumes-VIA-hub#2: with hub#1
// blackholed, any tunnel traffic can only traverse hub#2, and the post-switch iperf3
// server runs INSIDE hub#2's namespace, so a positive transfer is unambiguous. The
// edge's "hub failover:" WARN line (asserting the switch TARGET is hub#2's endpoint)
// corroborates the switch + re-handshake.
func TestHubFailoverStandbySwitch(t *testing.T) {
	bin := buildWanbond(t)
	// Ordered endpoint list: index 0 = hub#1 active, index 1 = hub#2 standby.
	endpoints := fmt.Sprintf("[%q, %q]", hfHub1EP, hfHub2EP)
	base, edge, h1, h2 := setupHubFailover(t, bin, endpoints)

	// (1) Bond up on hub#1: handshake + ping + a positive transfer through hub#1's
	// namespace. waitPathUp confirms the single uplink's liveness is UP (=1).
	if !base.pingUntil(concInner, 15*time.Second) {
		t.Fatalf("bond never came up on active hub#1\n--- edge ---\n%s", edge.log())
	}
	waitPathUp(t, hfMetricsURL, hfPathName, 1, 15*time.Second)
	if mbps := h1.iperf3Mbps(t, concInner, 3); mbps <= 0 {
		t.Fatalf("active hub#1 carried non-positive throughput %.2f Mbit/s", mbps)
	}

	// (2) Kill hub#1 entirely (bring its bridge port down): every path's liveness to
	// the active concentrator goes DOWN together — HUB LOSS — while hub#2 stays
	// reachable and the edge's own uplink source is untouched.
	killAt := time.Now()
	base.run("ip", "link", "set", hfHub1Port, "down")
	t.Logf("killed active hub#1 (bridge port %s down) at T0", hfHub1Port)

	// (3) The edge must switch to hub#2 and RESUME traffic within the analytical
	// window. With hub#1 dead, a successful ping to concInner can ONLY be carried by
	// hub#2, so this alone proves the switch resumed traffic on the standby.
	if !base.pingUntil(concInner, hubFailoverWindow) {
		t.Fatalf("tunnel did not resume via standby hub#2 within %s of the hub#1 outage\n--- edge ---\n%s",
			hubFailoverWindow, edge.log())
	}
	resumeGap := time.Since(killAt)

	// (4) The standby path's liveness recovered to UP (the bond re-established on
	// hub#2), corroborating the ping.
	waitPathUp(t, hfMetricsURL, hfPathName, 1, hubFailoverWindow)

	// (5) Traffic-resumes-VIA-hub#2, unambiguously: the iperf3 server runs INSIDE
	// hub#2's namespace, so a positive transfer can only mean the WG session now
	// terminates on hub#2 (hub#1 is still blackholed).
	if mbps := h2.iperf3Mbps(t, concInner, 3); mbps <= 0 {
		t.Fatalf("standby hub#2 carried non-positive throughput %.2f Mbit/s after failover", mbps)
	}

	// (6) The switch + re-handshake is recorded in the edge journal, and it advanced
	// to hub#2's endpoint specifically (the controller logs to_endpoint=next).
	if !edgeLoggedHubFailover(edge.log(), hfHub2EP) {
		t.Fatalf("edge journal has no 'hub failover:' switch to hub#2 endpoint %q; the controller must log the advance\n--- edge ---\n%s",
			hfHub2EP, edge.log())
	}

	t.Logf("HUB_FAILOVER_RESUME_MS=%d window_ms=%d: bond up on hub#1, hub#1 killed, edge switched to hub#2 (%s), re-handshaked, and resumed traffic via hub#2",
		resumeGap.Milliseconds(), hubFailoverWindow.Milliseconds(), hfHub2EP)
}

// TestHubFailoverSingleEndpointGuard is the T62 control / GUARD: with a ONE-element
// endpoint list [hub#1] the edge takes NO failover action when hub#1 dies — no switch,
// no re-handshake toward a standby, traffic does NOT resume — even though a healthy
// hub#2 is present and reachable in the fabric. This proves the switch is gated by the
// single-endpoint GUARD (len(endpoints) < 2), not merely by the absence of a standby:
// the standby EXISTS on the wire, it is simply not in the edge's endpoint list.
func TestHubFailoverSingleEndpointGuard(t *testing.T) {
	bin := buildWanbond(t)
	// ONE-element ordered list: only hub#1. hub#2 is still stood up and reachable, so
	// non-recovery is attributable to the guard, not a missing standby.
	endpoints := fmt.Sprintf("[%q]", hfHub1EP)
	base, edge, h1, _ := setupHubFailover(t, bin, endpoints)

	if !base.pingUntil(concInner, 15*time.Second) {
		t.Fatalf("bond never came up on hub#1\n--- edge ---\n%s", edge.log())
	}
	waitPathUp(t, hfMetricsURL, hfPathName, 1, 15*time.Second)
	if mbps := h1.iperf3Mbps(t, concInner, 3); mbps <= 0 {
		t.Fatalf("hub#1 carried non-positive throughput %.2f Mbit/s", mbps)
	}

	// Kill hub#1. A single-endpoint deployment has no standby to advance to.
	base.run("ip", "link", "set", hfHub1Port, "down")
	t.Logf("killed hub#1 (bridge port %s down); single-endpoint list must NOT fail over", hfHub1Port)

	// The tunnel must STAY down for the full window the positive test uses to switch:
	// a resumed ping here would mean a spurious failover to the (present, reachable)
	// standby, violating the guard.
	if base.pingUntil(concInner, hubFailoverWindow) {
		t.Fatalf("tunnel resumed within %s under a SINGLE-endpoint list — a spurious hub failover; the guard (endpoints<2) must take no action\n--- edge ---\n%s",
			hubFailoverWindow, edge.log())
	}

	// And the controller must have logged NO switch.
	if edgeLoggedHubFailover(edge.log(), "") {
		t.Fatalf("edge journal contains a 'hub failover:' line under a single-endpoint list; the guard must emit none\n--- edge ---\n%s", edge.log())
	}

	t.Logf("single-endpoint GUARD: hub#1 killed, no switch, tunnel stayed down for %s (a reachable hub#2 was NOT adopted) as required", hubFailoverWindow)
}

// edgeLoggedHubFailover reports whether the edge journal contains a controller
// "hub failover:" switch line. When wantEndpoint is non-empty it additionally requires
// that line to name that endpoint (the controller logs to_endpoint=<next>), so the
// positive test asserts the switch TARGET; the control test passes "" to assert the
// mere ABSENCE of any such line.
func edgeLoggedHubFailover(logText, wantEndpoint string) bool {
	for _, line := range strings.Split(logText, "\n") {
		if !strings.Contains(line, "hub failover:") {
			continue
		}
		if wantEndpoint == "" || strings.Contains(line, wantEndpoint) {
			return true
		}
	}
	return false
}

// setupHubFailover builds the two-hub bridge fabric, starts both concentrator daemons
// (sharing ONE WireGuard static key) and the edge daemon (single path, the supplied
// ordered endpointsTOML), addresses all three TUNs, and returns the edge-namespace
// Topology handle (for ping/blackhole/run), the edge proc, and the two hub handles
// (for per-hub iperf3 into the correct namespace). endpointsTOML is the verbatim TOML
// array value for the edge peer's `endpoints` key, e.g. `["a:51820", "b:51820"]`.
func setupHubFailover(t *testing.T, bin, endpointsTOML string) (base *Topology, edge *proc, h1, h2 *Topology) {
	t.Helper()

	base = &Topology{t: t}
	base.run("ip", "link", "set", "lo", "up")

	// Idempotent pre-delete: the bridge and edge-side veths have FIXED names in the
	// shared edge namespace (the TestMain re-exec's netns is reused across this file's
	// tests), so a prior test's teardown racing the kernel reap can leave them behind.
	// Deleting first (ignore-if-absent) makes setup robust to sequential reuse.
	_ = base.tryRun("ip", "link", "del", hfHub1Port)
	_ = base.tryRun("ip", "link", "del", hfHub2Port)
	_ = base.tryRun("ip", "link", "del", hfBridge)

	// The L2 fabric: an STP-off, zero-forward-delay bridge so ports forward the instant
	// they come up (no listening/learning delay), carrying the edge uplink and both hubs.
	base.run("ip", "link", "add", hfBridge, "type", "bridge", "stp_state", "0", "forward_delay", "0")
	base.run("ip", "addr", "add", hfEdgeIP+"/24", "dev", hfBridge)
	base.run("ip", "link", "set", hfBridge, "up")
	t.Cleanup(func() {
		_ = base.tryRun("ip", "link", "del", hfHub1Port)
		_ = base.tryRun("ip", "link", "del", hfHub2Port)
		_ = base.tryRun("ip", "link", "del", hfBridge)
	})

	h1 = startHubHolder(t, base, hfHub1Port, hfHub1Ceth, hfHub1IP)
	h2 = startHubHolder(t, base, hfHub2Port, hfHub2Ceth, hfHub2IP)

	// Shared identity: one WireGuard static key for BOTH hubs (the standby presents the
	// SAME peer identity), one PSK for the outer control/probe plane.
	edgePriv, edgePub := genKey(t)
	hubPriv, hubPub := genKey(t)
	psk := randKey(t)

	dir := t.TempDir()
	// Edge: single uplink path (source_addr on the bridge), a metrics endpoint, and the
	// ordered peer endpoint list. Level "info" so the controller's WARN "hub failover:"
	// line is captured (it is suppressed at "error"). No per-path dest_addr: the peer's
	// Endpoints[0] seeds the path remote (device.go:719), and the hub switch repoints it.
	edgeCfg := writeConfig(t, filepath.Join(dir, "edge.toml"), fmt.Sprintf(`role = "edge"
psk = "%s"

[[paths]]
name = "%s"
source_addr = "%s"

[metrics]
listen = "%s"

[wireguard]
private_key = "%s"

[[wireguard.peers]]
public_key = "%s"
endpoints = %s
allowed_ips = ["%s/32"]

[log]
level = "info"
`, psk, hfPathName, hfEdgeIP, hfMetricsListen, edgePriv, hubPub, endpointsTOML, concInner))

	hub1Cfg := writeConfig(t, filepath.Join(dir, "hub1.toml"), hubConfig(psk, hfHub1IP, hubPriv, edgePub))
	hub2Cfg := writeConfig(t, filepath.Join(dir, "hub2.toml"), hubConfig(psk, hfHub2IP, hubPriv, edgePub))

	// Concentrators first (they must be listening before the edge initiates), then edge.
	h1.startProc(t, "hub1", "nsenter", "-t", strconv.Itoa(h1.pid), "-n", bin, "--config", hub1Cfg)
	h2.startProc(t, "hub2", "nsenter", "-t", strconv.Itoa(h2.pid), "-n", bin, "--config", hub2Cfg)
	edge = base.startProc(t, "edge", bin, "--config", edgeCfg)

	if !base.waitLink(tunDev, false, 5*time.Second) {
		t.Fatalf("edge %s never appeared\n%s", tunDev, edge.log())
	}
	if !h1.waitLink(tunDev, true, 5*time.Second) {
		t.Fatalf("hub#1 %s never appeared", tunDev)
	}
	if !h2.waitLink(tunDev, true, 5*time.Second) {
		t.Fatalf("hub#2 %s never appeared", tunDev)
	}

	// Address the edge TUN and BOTH hub TUNs. Both hubs carry the SAME inner address
	// (concInner) in their OWN namespaces, so a ping to concInner is answered by
	// whichever hub the WG session currently terminates on.
	base.run("ip", "addr", "add", edgeInner+"/24", "dev", tunDev)
	base.run("ip", "link", "set", tunDev, "up")
	for _, h := range []*Topology{h1, h2} {
		h.nsenter("ip", "addr", "add", concInner+"/24", "dev", tunDev)
		h.nsenter("ip", "link", "set", tunDev, "up")
	}
	return base, edge, h1, h2
}

// hubConfig renders a concentrator config on source_addr srcIP with the shared private
// key privB64 and the edge peer (pubB64). Log level "error": the hub's role in the
// assertions is to CARRY traffic (proven by iperf3 into its namespace), not to be
// log-scraped, so it stays quiet.
func hubConfig(psk, srcIP, privB64, edgePub string) string {
	return fmt.Sprintf(`role = "concentrator"
psk = "%s"

[[paths]]
name = "%s"
source_addr = "%s"

[wireguard]
private_key = "%s"
listen_port = %d

[[wireguard.peers]]
public_key = "%s"
allowed_ips = ["%s/32"]

[log]
level = "error"
`, psk, hfPathName, srcIP, privB64, listenPort, edgePub, edgeInner)
}

// startHubHolder opens a fresh network namespace (a sleeping child, PID-addressed like
// the base fixture's holder), wires a veth pair from the edge bridge into it, assigns
// hubIP, and brings lo + both ends up. It returns a Topology bound to the holder PID so
// the caller runs the daemon and iperf3 inside it. edgePortVeth is the bridge-side end
// (brought DOWN to kill the hub); hubCeth is the in-namespace end.
func startHubHolder(t *testing.T, base *Topology, edgePortVeth, hubCeth, hubIP string) *Topology {
	t.Helper()
	holder := exec.Command("unshare", "-n", "sleep", "600")
	if err := holder.Start(); err != nil {
		t.Fatalf("start hub netns holder: %v", err)
	}
	pid := holder.Process.Pid
	t.Cleanup(func() {
		_ = holder.Process.Kill()
		_, _ = holder.Process.Wait()
	})

	h := &Topology{t: t, pid: pid}
	h.waitForNetns()

	// veth pair: bridge-side end onto the bridge, other end into the hub namespace.
	base.run("ip", "link", "add", edgePortVeth, "type", "veth", "peer", "name", hubCeth)
	base.run("ip", "link", "set", hubCeth, "netns", strconv.Itoa(pid))
	base.run("ip", "link", "set", edgePortVeth, "master", hfBridge)
	base.run("ip", "link", "set", edgePortVeth, "up")

	h.nsenter("ip", "link", "set", "lo", "up")
	h.nsenter("ip", "addr", "add", hubIP+"/24", "dev", hubCeth)
	h.nsenter("ip", "link", "set", hubCeth, "up")
	return h
}
