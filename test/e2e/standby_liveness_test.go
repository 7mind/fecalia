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
// whether an "up" path's liveness is genuinely BIDIRECTIONAL, or whether it can be
// satisfied by receive-only traffic. The motivating production observation was
// wanbond_path_up{path="5g"}=1 with wanbond_path_tx_bytes_total{path="5g"}=0 — a
// path reported healthy while its tx counter never moved. That gap was defect D48:
// emitProbes and dispatchInbound's echo reflection wrote real PROBE/echo frames to
// the wire but never counted them into ps.txBytes (internal/bind/probe.go,
// internal/bind/multipath.go). D48's fix adopted a true-wire-volume contract — every
// successful egress write on a path counts — so txBytes advances on ANY wire
// activity. Two independent checks:
//
//   - standby-transmits-when-idle: an UP second path must be observed TRANSMITTING
//     (its tx counter grows) while a live flow runs over the bond.
//   - standby-egress-blocked-goes-down: with the second path's EGRESS direction (only)
//     blocked one-way at the edge veth (BlockEgress, a tc clsact/matchall/drop
//     filter — see netns.go), it must transition DOWN and must NOT restore
//     connectivity when the first path is then killed. This is the affirmative half: it
//     proves the liveness verdict requires THIS path's own send-probe/receive-echo
//     round trip, not merely "traffic arrived on this interface" (the peer's own
//     probes keep ARRIVING at the blocked path the whole time — see
//     internal/bind/multipath.go dispatchInbound: an unechoed inbound PROBE only
//     learns the remote and is reflected; it never marks OUR liveness up — only
//     HandleEcho, driven by OUR OWN probe's authenticated echo, can).
//
// This harness cannot execute the `-tags e2e` netns tier itself (it requires
// CAP_NET_ADMIN/root and runs via the dedicated privileged target — see AGENTS.md);
// it is written to COMPILE and, once run there, is expected to PASS both subtests.
const (
	t104MetricsListen = "127.0.0.1:9100"
	t104MetricsURL    = "http://" + t104MetricsListen + "/metrics"

	// t104ProbeWindow is the observation window for the tx-growth
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

// testStandbyIdleTransmits is the FIRST T104 check: with a live flow over the bond,
// the second path's (cellular) wanbond_path_tx_bytes_total must grow over a
// multi-probe-interval window while it reports up.
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

	// Drive a live flow for (at least) the whole observation window.
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
	t.Logf("standby %q: wanbond_path_up=1 throughout a %s window with a live flow over the bond, tx delta = %.0f bytes",
		standby.name, t104ProbeWindow, delta)
	if delta <= 0 {
		t.Errorf("standby path %q stayed wanbond_path_up=1 for the whole %s window with a live flow over the "+
			"bond, but its %s did not grow (delta=%.0f bytes) — this would reproduce the pre-D48 production "+
			"observation path_up{%s}=1 with tx{%s}=0. Every successful egress write on a path counts into "+
			"ps.txBytes (internal/bind/probe.go, internal/bind/multipath.go, internal/bind/adaptive.go). A "+
			"failure here now indicates a fresh regression in that accounting, not the original D48 gap — "+
			"refile as a new defect linked to goals:G6 (Q39) and keep this test as the reproduction.",
			standby.name, t104ProbeWindow, metrics.MetricTxBytes, delta, standby.name, standby.name)
	}
}

// testStandbyEgressBlockedGoesDown is the SECOND T104 check: with the standby's
// egress (only) blocked one-way, it must transition DOWN — proving liveness needs
// this path's own probe/echo round trip, not merely inbound traffic arriving on it
// — and, once the primary is then killed, connectivity must NOT recover over it.
func testStandbyEgressBlockedGoesDown(t *testing.T, bin string) {
	t.Helper()
	top := Setup(t)
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

	// Kill the still-healthy primary. If the standby's DOWN verdict is genuine, no
	// healthy path is left, so connectivity must NOT recover.
	top.Blackhole(primary.name)
	t.Cleanup(func() { top.Restore(primary.name) })

	noFailoverWindow := PLivenessFailoverBudget + 3*time.Second
	if top.pingUntil(concInner, noFailoverWindow) {
		t.Errorf("connectivity recovered within %s after the primary died with the standby's egress still blocked "+
			"— the falsely-live standby carried traffic\n--- edge ---\n%s", noFailoverWindow, edge.log())
	} else {
		t.Logf("connectivity correctly did NOT recover within %s — the down standby carried nothing", noFailoverWindow)
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

// setupT104Tunnel brings the multipath tunnel up over paths with the /metrics
// endpoint enabled on the edge —
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
