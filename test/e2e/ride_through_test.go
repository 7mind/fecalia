//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/metrics"
	"github.com/7mind/wanbond/internal/telemetry"
)

// TestE2ERideThroughMicroOutage is the D86 resolution gate (decision 5, task T213):
// the on-wire proof that a per-path ride_through dwell absorbs a sub-threshold
// micro-outage of the primary WAN WITHOUT thrashing egress onto the metered
// (5G/cellular) backup, while a genuinely long outage still fails over — and that
// the long-outage recovery respects the CONFIGURED failover budget, not the default.
//
// HARDWARE TIER — NOT RUN IN THE SANDBOX. Like every `//go:build e2e` test here, this
// exercises the privileged netns/veth harness (netns.go): it needs root, CAP_NET_ADMIN,
// and /dev/net/tun, provided by the dedicated privileged runner (see AGENTS.md), NOT by
// the CI sandbox that builds it. This file is written to COMPILE and `go vet -tags e2e`
// / `just lint` GREEN locally; the privileged run is deferred to the hardware tier. Do
// NOT attempt to run it locally.
//
// REPRODUCE-FIRST (documented negative control). The subtests are ordered so the DEFECT
// is reproduced BEFORE the fix is asserted — the phase structure IS the reproduction
// discipline for a hardware-tier test that cannot be a fast unit repro:
//
//   - Phase 1 (negative control, DEFAULT config — no ride_through): a ~1.3s blackhole of
//     the primary DOES fail the bond over — the scheduler switches egress to the backup
//     and real DATA bytes appear on the metered path, its share crossing the P2 data-thrift
//     bound. This is TODAY's behaviour and proves the fixture genuinely provokes the D86
//     field thrash. down_after defaults to 1200ms, so a 1.3s silence trips the up->down
//     transition (the strict-'>' Tick observes silence climbing past 1200ms — it keeps
//     climbing until the first post-restore echo round-trips, ~one path-RTT after Restore,
//     so detection is robust to the exact blackhole length).
//
//   - Phase 2 (fix, primary ride_through=2s, standby strict): the SAME 1.3s blackhole
//     causes NO failover. down_after+ride_through = 3200ms, and peak silence (~1.55s) never
//     reaches it, so the primary NEVER transitions DOWN (log + wanbond_path_up assert), the
//     scheduler never switches to the backup, the flow survives on the primary, and the
//     metered path's byte-share stays WITHIN the P2 data-thrift bound (P2MeteredMaxByteFraction).
//
//   - Phase 3 (fix, primary ride_through=2s): a LONG outage (> down_after+ride_through)
//     STILL fails over, and the measured per-direction recovery — read the same sound,
//     un-confounded way as TestP1Failover (each daemon's "scheduler active path change"
//     transition timestamp) — respects telemetry.FailoverBudget(down_after, ride_through=2s,
//     probe_interval), i.e. the budget for the CONFIGURED timing, closing the D86 decision-4
//     thresholds.go loop against CONFIGURED, not default, values. (That budget, 3.6s, is by
//     design LARGER than the 1.6s default PLivenessFailoverBudget — ride_through trades a
//     slower worst-case failover for micro-outage survival; the daemon logs a WARN-and-allow
//     over-budget verdict at start, which does not block the tunnel.)
//
// TestP1Failover at DEFAULTS is a separate test and stays untouched and green: it never
// configures ride_through, so its DefaultPaths/active-backup behaviour is unaffected.
//
// What is asserted are PATH-SELECTION / COUNTER-RATIO outcomes (metered byte fraction,
// scheduler transitions, liveness transitions, measured switch latency) — NOT throughput —
// so the assertions are robust to the CPU/PPS-bound single-host fixture.
const (
	rtMetricsListen = "127.0.0.1:9111"
	rtMetricsURL    = "http://" + rtMetricsListen + "/metrics"

	// rtPrimaryRideThrough is the primary path's configured down-side dwell for phases 2
	// and 3 (Starlink-primary-rides-through). At the default 1200ms down_after and 200ms
	// probe interval the derived per-direction failover budget is 1200+2000+2*200 = 3600ms.
	rtPrimaryRideThrough = 2 * time.Second

	// rtMicroOutage is the sub-threshold primary blackhole. It is chosen to sit ABOVE the
	// default down_after (1200ms) — so the DEFAULT config (phase 1) trips a failover — yet
	// far BELOW down_after+ride_through (3200ms) — so the ride_through config (phase 2)
	// absorbs it with margin.
	rtMicroOutage = 1300 * time.Millisecond

	// rtLongOutage is the long primary blackhole for phase 3: comfortably past
	// down_after+ride_through (3200ms) so the ride-through path still transitions DOWN and
	// fails over. Held long enough that the switch is observed while the link is still down.
	rtLongOutage = 6 * time.Second

	// rtFlowBitrate is the modest, bandwidth-limited offered load spanning each phase's
	// outage: high enough that the primary's DATA bytes dwarf the standby's probe noise (so
	// the phase-2 thrift fraction is well-defined and tiny), low enough that the phase-3
	// recovery measurement is not delayed by the D15 saturating-load probe-loop starvation
	// (this is a path-selection test, not a throughput/tail test). Mirrors p2ThriftBitrate.
	rtFlowBitrate = "12M"

	// rtRampBefore lets the flow reach steady state on the primary before the blackhole;
	// rtPostOutageObserve is the post-restore window over which the metered-path byte share
	// is sampled (shorter than the 5s scheduler FailbackAfter dwell, so in phase 1 the flow
	// is still carried on the backup throughout it — the standby carriage is observable).
	rtRampBefore        = 1500 * time.Millisecond
	rtPostOutageObserve = 2500 * time.Millisecond
)

// TestE2ERideThroughMicroOutage runs the three D86 phases as sequential subtests. They run
// sequentially (no t.Parallel) because the fixture's FIXED veth names forbid two live
// topologies — each subtest builds and tears down its own Setup, mirroring TestP2Aggregation.
func TestE2ERideThroughMicroOutage(t *testing.T) {
	bin := buildWanbond(t)

	// Phase 1: DEFAULT config (no ride_through) — the 1.3s micro-outage DOES fail over.
	t.Run("negative-control-default-fails-over", func(t *testing.T) {
		top := Setup(t)
		edge, conc := setupRideThroughTunnel(t, top, bin, DefaultPaths, 0)
		if !top.pingUntil(concInner, 15*time.Second) {
			t.Fatalf("bond never came up\n--- edge ---\n%s\n--- conc ---\n%s", edge.log(), conc.log())
		}
		primary := DefaultPaths[primaryPathIdx] // starlink
		standby := DefaultPaths[backupPathIdx]  // cellular

		before, after, killAt := rtRunMicroOutage(t, top, edge, primary, standby)

		// The metered path carried real DATA: its byte-share crosses the thrift bound. In the
		// DEFAULT config the 1.3s silence trips a failover, so the backup carries the flow.
		starTx := deltaPathValue(t, before, after, metrics.MetricTxBytes, primary.name)
		cellTx := deltaPathValue(t, before, after, metrics.MetricTxBytes, standby.name)
		frac := rtMeteredFraction(starTx, cellTx)
		t.Logf("negative-control: primary tx = %.0f B, metered tx = %.0f B, metered fraction = %.4f (defect if >= %.4f)",
			starTx, cellTx, frac, P2MeteredMaxByteFraction)
		if frac < P2MeteredMaxByteFraction {
			t.Fatalf("DEFAULT config held the metered path below %.4f (%.4f) across a %s primary micro-outage — the fixture did NOT provoke the D86 thrash it must reproduce",
				P2MeteredMaxByteFraction, frac, rtMicroOutage)
		}

		// Direct failover proof: the edge scheduler switched egress to the backup after the kill.
		if lat := switchToLatency(edge.log(), killAt, backupPathIdx); lat < 0 {
			t.Fatalf("DEFAULT config logged no scheduler switch to the backup after the %s micro-outage — expected today's failover\n--- edge ---\n%s",
				rtMicroOutage, edge.log())
		} else {
			t.Logf("negative-control: edge scheduler switched to the backup %dms after the kill (today's thrash)", lat.Milliseconds())
		}
	})

	// Phase 2: primary ride_through=2s — the SAME 1.3s micro-outage causes NO failover.
	t.Run("ride-through-absorbs-micro-outage", func(t *testing.T) {
		top := Setup(t)
		edge, conc := setupRideThroughTunnel(t, top, bin, DefaultPaths, rtPrimaryRideThrough)
		if !top.pingUntil(concInner, 15*time.Second) {
			t.Fatalf("bond never came up\n--- edge ---\n%s\n--- conc ---\n%s", edge.log(), conc.log())
		}
		primary := DefaultPaths[primaryPathIdx] // starlink (ride_through=2s)
		standby := DefaultPaths[backupPathIdx]  // cellular (strict)

		before, after, killAt := rtRunMicroOutage(t, top, edge, primary, standby)

		// Metered path stayed within the data-thrift bound — egress never left the primary.
		starTx := deltaPathValue(t, before, after, metrics.MetricTxBytes, primary.name)
		cellTx := deltaPathValue(t, before, after, metrics.MetricTxBytes, standby.name)
		frac := rtMeteredFraction(starTx, cellTx)
		t.Logf("ride-through: primary tx = %.0f B, metered tx = %.0f B, metered fraction = %.4f (want < %.4f)",
			starTx, cellTx, frac, P2MeteredMaxByteFraction)
		if frac >= P2MeteredMaxByteFraction {
			t.Fatalf("ride_through=%s did NOT absorb the %s micro-outage: metered path carried %.2f%% of DATA bytes (want < %.2f%%) — egress thrashed onto the backup\n--- edge ---\n%s",
				rtPrimaryRideThrough, rtMicroOutage, frac*100, P2MeteredMaxByteFraction*100, edge.log())
		}

		// The primary NEVER transitioned DOWN and the scheduler NEVER switched to the backup.
		if rtPrimaryWentDown(edge.log(), primary.name, killAt) {
			t.Errorf("primary %q logged a liveness up->down transition during a %s micro-outage under ride_through=%s — the dwell did not hold\n--- edge ---\n%s",
				primary.name, rtMicroOutage, rtPrimaryRideThrough, edge.log())
		}
		if lat := switchToLatency(edge.log(), killAt, backupPathIdx); lat >= 0 {
			t.Errorf("edge scheduler switched to the backup %dms after a %s micro-outage under ride_through=%s — expected no failover\n--- edge ---\n%s",
				lat.Milliseconds(), rtMicroOutage, rtPrimaryRideThrough, edge.log())
		}
		// And the primary is still reported healthy at the end of the window.
		if up, ok := after.PathValue(metrics.MetricUp, primary.name); !ok || up != 1 {
			t.Errorf("primary %q not wanbond_path_up=1 after the micro-outage (up=%v ok=%v) — ride_through did not keep it up\n--- edge ---\n%s",
				primary.name, up, ok, edge.log())
		}
	})

	// Phase 3: primary ride_through=2s — a LONG outage still fails over, within the CONFIGURED budget.
	t.Run("long-outage-fails-over-within-budget", func(t *testing.T) {
		top := Setup(t)
		edge, conc := setupRideThroughTunnel(t, top, bin, DefaultPaths, rtPrimaryRideThrough)
		if !top.pingUntil(concInner, 15*time.Second) {
			t.Fatalf("bond never came up\n--- edge ---\n%s\n--- conc ---\n%s", edge.log(), conc.log())
		}
		primary := DefaultPaths[primaryPathIdx] // starlink (ride_through=2s)
		standby := DefaultPaths[backupPathIdx]  // cellular

		// The budget for the CONFIGURED timing (down_after default + ride_through=2s), the single
		// source of truth telemetry.FailoverBudget — NOT the default-config PLivenessFailoverBudget.
		budget := telemetry.FailoverBudget(PLivenessDownAfter, rtPrimaryRideThrough, PLivenessProbeInterval)

		// A modest flow so the failover carries observable DATA to the backup.
		flowSecs := int((rtRampBefore + rtLongOutage + 3*time.Second).Seconds()) + 1
		top.startProc(t, "iperf3-server", "nsenter", "-t", strconv.Itoa(top.pid), "-n", "iperf3", "-s", "-1", "-B", concInner)
		time.Sleep(400 * time.Millisecond)
		top.startProc(t, "iperf3-flow", "iperf3", "-c", concInner, "-t", strconv.Itoa(flowSecs), "-b", rtFlowBitrate)
		time.Sleep(rtRampBefore)

		before := scrapeMetrics(t, rtMetricsURL)
		if up, ok := before.PathValue(metrics.MetricUp, primary.name); !ok || up != 1 {
			t.Fatalf("primary %q not up before the long outage (up=%v ok=%v)\n%s", primary.name, up, ok, edge.log())
		}

		killAt := time.Now()
		top.Blackhole(primary.name)

		// Measure the per-direction failover, both ends, the same sound way as TestP1Failover:
		// the "scheduler active path change" to the backup logged after the kill. Poll well past
		// the budget so a late switch is MEASURED (magnitude-bearing) rather than lost to a
		// non-observation timeout.
		edgeSwitch, concSwitch, ok := waitBothSwitchTo(edge, conc, killAt, backupPathIdx, budget+3*time.Second)
		top.Restore(primary.name)
		if !ok {
			t.Fatalf("long outage did NOT fail over on both ends within %s (edge=%s conc=%s) — ride_through must still fail over a > down_after+ride_through outage\n--- edge ---\n%s\n--- conc ---\n%s",
				budget+3*time.Second, latencyStr(edgeSwitch), latencyStr(concSwitch), edge.log(), conc.log())
		}
		recovery := edgeSwitch
		if concSwitch > recovery {
			recovery = concSwitch
		}
		t.Logf("long-outage: RECOVERY_MS=%d configured_budget_ms=%d edge_switch_ms=%d conc_switch_ms=%d (default budget %dms)",
			recovery.Milliseconds(), budget.Milliseconds(), edgeSwitch.Milliseconds(), concSwitch.Milliseconds(),
			PLivenessFailoverBudget.Milliseconds())
		if recovery >= budget {
			t.Errorf("long-outage bidirectional recovery %v exceeded the CONFIGURED failover budget %v (down_after=%v + ride_through=%v + 2*%v) — edge=%v conc=%v\n--- edge ---\n%s\n--- conc ---\n%s",
				recovery, budget, PLivenessDownAfter, rtPrimaryRideThrough, PLivenessProbeInterval, edgeSwitch, concSwitch, edge.log(), conc.log())
		}

		// Corroboration: the metered path actually carried the recovered flow.
		after := scrapeMetrics(t, rtMetricsURL)
		cellTx := deltaPathValue(t, before, after, metrics.MetricTxBytes, standby.name)
		if cellTx <= 0 {
			t.Errorf("metered path %q carried no DATA (tx delta %.0f) after the long-outage failover\n--- edge ---\n%s", standby.name, cellTx, edge.log())
		}
	})
}

// rtRunMicroOutage drives the shared phase-1/phase-2 micro-outage sequence: it starts a
// modest bandwidth-limited flow on the primary, samples the edge /metrics just before the
// blackhole, blackholes the primary for rtMicroOutage, restores it, then samples /metrics
// after a post-outage observation window. It returns the two /metrics snapshots (for the
// per-path byte-fraction delta the callers assert) and the kill instant (for the scheduler /
// liveness log assertions). What DIFFERS between the phases is only the config (ride_through)
// and hence the OUTCOME — the stimulus is identical, which is what makes phase 1 a faithful
// negative control for phase 2.
func rtRunMicroOutage(t *testing.T, top *Topology, edge *proc, primary, standby pathSpec) (before, after metrics.Exposition, killAt time.Time) {
	t.Helper()

	flowSecs := int((rtRampBefore + rtMicroOutage + rtPostOutageObserve + 3*time.Second).Seconds()) + 1
	top.startProc(t, "iperf3-server", "nsenter", "-t", strconv.Itoa(top.pid), "-n", "iperf3", "-s", "-1", "-B", concInner)
	time.Sleep(400 * time.Millisecond)
	top.startProc(t, "iperf3-flow", "iperf3", "-c", concInner, "-t", strconv.Itoa(flowSecs), "-b", rtFlowBitrate)
	time.Sleep(rtRampBefore)

	before = scrapeMetrics(t, rtMetricsURL)
	if up, ok := before.PathValue(metrics.MetricUp, primary.name); !ok || up != 1 {
		t.Fatalf("primary %q not up before the micro-outage (up=%v ok=%v)\n%s", primary.name, up, ok, edge.log())
	}
	if up, ok := before.PathValue(metrics.MetricUp, standby.name); !ok || up != 1 {
		t.Fatalf("standby %q not up before the micro-outage (up=%v ok=%v)\n%s", standby.name, up, ok, edge.log())
	}

	killAt = time.Now()
	top.Blackhole(primary.name)
	time.Sleep(rtMicroOutage)
	top.Restore(primary.name)
	time.Sleep(rtPostOutageObserve)

	after = scrapeMetrics(t, rtMetricsURL)
	return before, after, killAt
}

// rtMeteredFraction returns the metered (standby) path's share of the total DATA tx bytes
// over a window, or 0 when no DATA moved (an empty window is not a thrash). It mirrors the
// P2 data-thrift ratio (runDataThrift): cellTx / (starTx + cellTx).
func rtMeteredFraction(primaryTx, meteredTx float64) float64 {
	total := primaryTx + meteredTx
	if total <= 0 {
		return 0
	}
	return meteredTx / total
}

// rtPrimaryWentDown reports whether the primary path logged an up->down liveness transition
// (telemetry.Liveness.transition — "path liveness transition", to="down") strictly after the
// kill instant. It is the log half of the "path never transitions DOWN" assertion: the daemon
// runs at INFO so the transition record is captured. The record carries the per-path field
// (log.FieldPath) so a transition on the standby cannot be mistaken for one on the primary.
func rtPrimaryWentDown(logText, pathName string, after time.Time) bool {
	for _, line := range strings.Split(logText, "\n") {
		if !strings.Contains(line, "path liveness transition") {
			continue
		}
		var rec struct {
			Time time.Time `json:"time"`
			Msg  string    `json:"msg"`
			Path string    `json:"path"`
			To   string    `json:"to"`
		}
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		if rec.Msg == "path liveness transition" && rec.Path == pathName && rec.To == "down" && rec.Time.After(after) {
			return true
		}
	}
	return false
}

// setupRideThroughTunnel brings the two-path tunnel up over the (default, omitted)
// active-backup scheduler with the /metrics endpoint on the edge — only the edge's per-path
// tx/up series are asserted, so only the edge carries the [metrics] block (as in
// setupT104Tunnel). When primaryRideThrough > 0 it emits `ride_through = "<dur>"` on the
// PRIMARY path (paths[0]) in BOTH the edge and concentrator configs — the concentrator must
// ride through the same micro-outage or it would reroute its REPLY egress onto the backup —
// leaving the standby strict (ride_through omitted = 0). Both daemons run at INFO so the
// scheduler "active path change" and liveness "path liveness transition" records are readable
// (the same idiom TestP1Failover / T104 use). It otherwise mirrors setupMultipathTunnelLevel.
func setupRideThroughTunnel(t *testing.T, top *Topology, bin string, paths []pathSpec, primaryRideThrough time.Duration) (edge, conc *proc) {
	t.Helper()

	edgePriv, edgePub := genKey(t)
	concPriv, concPub := genKey(t)
	psk := randKey(t)

	primaryName := paths[0].name
	rideThroughLine := func(name string) string {
		if primaryRideThrough > 0 && name == primaryName {
			return fmt.Sprintf("ride_through = %q\n", primaryRideThrough.String())
		}
		return ""
	}

	var edgePaths, concPaths strings.Builder
	for _, p := range paths {
		fmt.Fprintf(&edgePaths, "[[paths]]\nname = %q\nsource_addr = %q\ndest_addr = \"%s:%d\"\n%s\n",
			p.name, p.edgeIP, p.concIP, listenPort, rideThroughLine(p.name))
		fmt.Fprintf(&concPaths, "[[paths]]\nname = %q\nsource_addr = %q\n%s\n",
			p.name, p.concIP, rideThroughLine(p.name))
	}
	primary := paths[0]
	metricsBlock := fmt.Sprintf("[metrics]\nlisten = %q\n\n", rtMetricsListen)

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
level = "info"
`, psk, edgePaths.String(), metricsBlock, edgePriv, concPub, primary.concIP, listenPort, concInner))

	concCfg := writeConfig(t, filepath.Join(dir, "conc.toml"), fmt.Sprintf(`role = "concentrator"
psk = "%s"

%s[wireguard]
private_key = "%s"
listen_port = %d

[[wireguard.peers]]
public_key = "%s"
allowed_ips = ["%s/32"]

[log]
level = "info"
`, psk, concPaths.String(), concPriv, listenPort, edgePub, edgeInner))

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
