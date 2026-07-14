//go:build e2e

package e2e

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/metrics"
)

// T104 (Q39, goals:G6) is an IN-GOAL VERIFICATION task, not a refactor: it asks
// whether an idle-but-"up" standby path's liveness is genuinely BIDIRECTIONAL, or
// whether it can be satisfied by receive-only traffic. The motivating production
// observation was wanbond_path_up{path="5g"}=1 with wanbond_path_tx_bytes_total{
// path="5g"}=0 — a standby path reported healthy while its tx counter never moved.
// Two independent checks:
//
//   - standby-transmits-when-idle: an UP standby that carries no DATA (active-backup
//     collapses all DATA onto the primary) must still be observed TRANSMITTING —
//     its own periodic liveness probes are real wire writes (bind.emitProbes ->
//     conn.WriteToUDPAddrPort) — while the primary carries a live flow.
//   - standby-egress-blocked-goes-down: with the standby's EGRESS direction (only)
//     blocked one-way at the edge veth (BlockEgress, a tc clsact/matchall/drop
//     filter — see netns.go), the standby must transition DOWN and must NOT be
//     selected when the primary is then killed. This is the affirmative half: it
//     proves the liveness verdict requires THIS path's own send-probe/receive-echo
//     round trip, not merely "traffic arrived on this interface" (the peer's own
//     probes keep ARRIVING at the blocked path the whole time — see
//     internal/bind/multipath.go dispatchInbound: an unechoed inbound PROBE only
//     learns the remote and is reflected; it never marks OUR liveness up — only
//     HandleEcho, driven by OUR OWN probe's authenticated echo, can).
//
// This harness cannot execute the `-tags e2e` netns tier itself (it requires
// CAP_NET_ADMIN/root and runs via the dedicated privileged target — see AGENTS.md);
// it is written to COMPILE and to make BOTH outcomes (pass, or a documented failure
// kept as a defect repro per Q39) observable once run there. A static read of the
// send path (internal/bind/probe.go's emitProbes never calls ps.txBytes.Add — only
// Send() and fecFlushDeadline() in internal/bind/multipath.go do) predicts the first
// subtest fails against the current implementation, reproducing the production
// observation; that is an inference from source, not a measured result — the
// authoritative answer is the hardware run's PASS/FAIL, at which point a genuine
// failure should be refiled as a defect linked to goals:G6 with this test kept as
// the repro, per the Q39 either-outcome acceptance.
const (
	t104MetricsListen = "127.0.0.1:9100"
	t104MetricsURL    = "http://" + t104MetricsListen + "/metrics"

	// t104ProbeWindow is the idle-standby observation window for the tx-growth
	// check: long enough (15 probe intervals at the default 200ms cadence) that
	// even ONE counted probe would show up as a nonzero delta, well clear of
	// scrape/scheduling jitter.
	t104ProbeWindow = 15 * PLivenessProbeInterval
)

// TestStandbyLivenessBidirectional runs both T104 checks. Each brings up its own
// topology (sequentially — the fixture's fixed veth names forbid two live
// topologies), mirroring the subtest structure of TestP2Aggregation.
func TestStandbyLivenessBidirectional(t *testing.T) {
	bin := buildWanbond(t)

	t.Run("standby-transmits-when-idle", func(t *testing.T) {
		testStandbyIdleTransmits(t, bin)
	})
	t.Run("standby-egress-blocked-goes-down", func(t *testing.T) {
		testStandbyEgressBlockedGoesDown(t, bin)
	})
}

// testStandbyIdleTransmits is the FIRST T104 check: with the primary (starlink)
// carrying a live flow and the standby (cellular) carrying no DATA (active-backup
// default), the standby's wanbond_path_tx_bytes_total must still grow over a
// multi-probe-interval window — its own periodic liveness probes are genuine wire
// writes even though no DATA rides them.
func testStandbyIdleTransmits(t *testing.T, bin string) {
	t.Helper()
	top := Setup(t)
	edge, conc := setupT104Tunnel(t, top, bin, DefaultPaths, "error")

	if !top.pingUntil(concInner, 15*time.Second) {
		t.Fatalf("bond never came up\n--- edge ---\n%s\n--- conc ---\n%s", edge.log(), conc.log())
	}

	primary := DefaultPaths[primaryPathIdx] // starlink
	standby := DefaultPaths[backupPathIdx]  // cellular

	waitPathUp(t, t104MetricsURL, standby.name, 1, 10*time.Second)

	// Drive a live flow on the primary for (at least) the whole observation window;
	// active-backup keeps ALL of it off the standby, so the standby's own tx (if
	// any is counted) can only come from its periodic probes.
	loadSecs := int(t104ProbeWindow.Seconds()) + 10
	top.startProc(t, "iperf3-server", "nsenter", "-t", strconv.Itoa(top.pid), "-n", "iperf3", "-s", "-1", "-B", concInner)
	time.Sleep(400 * time.Millisecond)
	top.startProc(t, "iperf3-load", "iperf3", "-c", concInner, "-t", strconv.Itoa(loadSecs))
	time.Sleep(500 * time.Millisecond) // let the flow ramp before sampling the window

	before := scrapeMetrics(t, t104MetricsURL)
	txBefore, ok := before.PathValue(metrics.MetricTxBytes, standby.name)
	if !ok {
		t.Fatalf("edge /metrics missing %s{path=%q}", metrics.MetricTxBytes, standby.name)
	}
	if up, ok := before.PathValue(metrics.MetricUp, standby.name); !ok || up != 1 {
		t.Fatalf("standby %q not up at window start (up=%v ok=%v)\n%s", standby.name, up, ok, edge.log())
	}
	if up, ok := before.PathValue(metrics.MetricUp, primary.name); !ok || up != 1 {
		t.Fatalf("primary %q not up at window start (up=%v ok=%v)\n%s", primary.name, up, ok, edge.log())
	}

	time.Sleep(t104ProbeWindow)

	after := scrapeMetrics(t, t104MetricsURL)
	txAfter, ok := after.PathValue(metrics.MetricTxBytes, standby.name)
	if !ok {
		t.Fatalf("edge /metrics missing %s{path=%q} on the second scrape", metrics.MetricTxBytes, standby.name)
	}
	if up, ok := after.PathValue(metrics.MetricUp, standby.name); !ok || up != 1 {
		t.Fatalf("standby %q dropped from up during the idle window (up=%v ok=%v) — cannot judge idle-transmit liveness while down\n%s",
			standby.name, up, ok, edge.log())
	}

	delta := txAfter - txBefore
	t.Logf("standby %q: wanbond_path_up=1 throughout a %s window with the primary carrying a live flow, tx delta = %.0f bytes",
		standby.name, t104ProbeWindow, delta)
	if delta <= 0 {
		t.Errorf("standby path %q stayed wanbond_path_up=1 for the whole %s window while the primary carried a live "+
			"flow, but its %s did not grow (delta=%.0f bytes) — this reproduces the production observation "+
			"path_up{%s}=1 with tx{%s}=0. The standby's own periodic liveness probes ARE written to the wire "+
			"(internal/bind/probe.go emitProbes -> conn.WriteToUDPAddrPort), but that write never increments "+
			"ps.txBytes — only Send()/fecFlushDeadline() DATA/PARITY writes do (internal/bind/multipath.go). The "+
			"path genuinely transmits; the operator-facing tx_bytes series just does not reflect it. Refile as a "+
			"defect linked to goals:G6 (Q39) and keep this test as the reproduction.",
			standby.name, t104ProbeWindow, metrics.MetricTxBytes, delta, standby.name, standby.name)
	}
}

// testStandbyEgressBlockedGoesDown is the SECOND T104 check: with the standby's
// egress (only) blocked one-way, it must transition DOWN — proving liveness needs
// this path's own probe/echo round trip, not merely inbound traffic arriving on it
// — and, once the primary is then killed, it must NOT be selected for failover.
func testStandbyEgressBlockedGoesDown(t *testing.T, bin string) {
	t.Helper()
	top := Setup(t)
	// "info" so the scheduler's "active path change" transitions are readable from
	// the edge log, the same idiom TestP1Failover uses.
	edge, conc := setupT104Tunnel(t, top, bin, DefaultPaths, "info")

	if !top.pingUntil(concInner, 15*time.Second) {
		t.Fatalf("bond never came up\n--- edge ---\n%s\n--- conc ---\n%s", edge.log(), conc.log())
	}

	primary := DefaultPaths[primaryPathIdx] // starlink
	standby := DefaultPaths[backupPathIdx]  // cellular

	waitPathUp(t, t104MetricsURL, standby.name, 1, 10*time.Second)

	// Block ONLY the standby's egress at the edge: outbound frames on its veth are
	// dropped while the veth stays up and inbound (the concentrator's own probes)
	// keeps arriving. A receive-only liveness check would stay wrongly up here.
	top.BlockEgress(standby.name)
	t.Cleanup(func() { top.UnblockEgress(standby.name) })

	waitPathUp(t, t104MetricsURL, standby.name, 0, PLivenessDetectBudget)
	t.Logf("standby %q went DOWN within %s of its egress being blocked one-way (its own probe/echo round trip broke; inbound traffic alone did not keep it up)",
		standby.name, PLivenessDetectBudget)

	// Force a failover decision: kill the still-healthy primary. If the standby's
	// DOWN verdict is genuine, active-backup has no healthy path left, so egress
	// must NOT reroute onto the standby and connectivity must NOT recover.
	top.Blackhole(primary.name)
	t.Cleanup(func() { top.Restore(primary.name) })

	noFailoverWindow := PLivenessFailoverBudget + 3*time.Second
	if top.pingUntil(concInner, noFailoverWindow) {
		t.Errorf("connectivity recovered within %s after the primary died with the standby's egress still blocked "+
			"— the falsely-live standby was selected for failover\n--- edge ---\n%s", noFailoverWindow, edge.log())
	} else {
		t.Logf("connectivity correctly did NOT recover within %s — the down standby was not selected", noFailoverWindow)
	}
	if idx := currentActivePathIdx(edge.log()); idx == backupPathIdx {
		t.Errorf("edge scheduler switched its active path to the standby (index %d, %q) despite its egress being "+
			"blocked and its liveness DOWN\n%s", backupPathIdx, standby.name, edge.log())
	}

	// Sanity: the fixture recovers once both faults are cleared — a genuine
	// liveness-driven exclusion, not a wedged/broken harness.
	top.UnblockEgress(standby.name)
	top.Restore(primary.name)
	if !top.pingUntil(concInner, 15*time.Second) {
		t.Fatalf("bond did not recover after clearing the egress block and restoring the primary\n--- edge ---\n%s\n--- conc ---\n%s",
			edge.log(), conc.log())
	}
}

// setupT104Tunnel brings the multipath tunnel up over paths with the (default,
// omitted) active-backup scheduler and the /metrics endpoint enabled on the edge —
// only the edge's tx/up series are asserted by either T104 check, so only the edge
// carries the [metrics] block. It otherwise mirrors setupMultipathTunnelLevel.
func setupT104Tunnel(t *testing.T, top *Topology, bin string, paths []pathSpec, level string) (edge, conc *proc) {
	t.Helper()

	edgePriv, edgePub := genKey(t)
	concPriv, concPub := genKey(t)
	psk := randKey(t)

	var edgePaths, concPaths strings.Builder
	for _, p := range paths {
		fmt.Fprintf(&edgePaths, "[[paths]]\nname = %q\nsource_addr = %q\ndest_addr = \"%s:%d\"\n\n", p.name, p.edgeIP, p.concIP, listenPort)
		fmt.Fprintf(&concPaths, "[[paths]]\nname = %q\nsource_addr = %q\n\n", p.name, p.concIP)
	}
	primary := paths[0]
	metricsBlock := fmt.Sprintf("[metrics]\nlisten = %q\n\n", t104MetricsListen)

	dir := t.TempDir()
	edgeCfg := writeConfig(t, filepath.Join(dir, "edge.toml"), fmt.Sprintf(`role = "edge"
psk = "%s"

%s%s[wireguard]
private_key = "%s"

[[wireguard.peers]]
public_key = "%s"
endpoint = "%s:%d"
allowed_ips = ["%s/32"]

[log]
level = %q
`, psk, edgePaths.String(), metricsBlock, edgePriv, concPub, primary.concIP, listenPort, concInner, level))

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
