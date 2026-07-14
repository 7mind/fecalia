//go:build e2e

package e2e

import (
	"fmt"
	"path/filepath"
	"strconv"
	"time"

	"net"
	"testing"
)

// TestAggregationGateLog is the T143 e2e acceptance (Item 1 + Q54, R155): a
// weighted-policy daemon's "scheduler aggregation change" log record — the
// CANONICAL message updateGateLocked already emits on every engage/disengage flip
// (internal/sched/weighted.go) — carries the T143-added structured fields (from,
// engage_threshold_fps, disengage_threshold_fps) alongside its pre-existing
// to/load_fps/reason fields, and still fires EXACTLY ONCE per flip (no
// double-log regression) end-to-end through a real daemon, using the T141
// overload driver (DriveUDPLoad) and structured-log capturer
// (ParseLogLines/AwaitLogLine).
//
// Every wait below is DERIVED from the scheduler's own configured knobs
// (aggLogCollapseDwell, aggLogLoadTau) plus a fixed safety multiplier/margin — no
// magic sleeps.
const (
	// aggLogPerPathCapacityFPS is the acceptance-mandated per_path_capacity_fps.
	aggLogPerPathCapacityFPS = 250.0
	// aggLogEngageFraction/aggLogDisengageFraction are set explicitly (matching the
	// scheduler package defaults) so the test's expected threshold values do not
	// depend on internal/config's default constants staying in sync.
	aggLogEngageFraction    = 0.9
	aggLogDisengageFraction = 0.5
	// aggLogEngageThresholdFPS/aggLogDisengageThresholdFPS are the resulting gate
	// thresholds asserted on the extended log fields.
	aggLogEngageThresholdFPS    = aggLogEngageFraction * aggLogPerPathCapacityFPS
	aggLogDisengageThresholdFPS = aggLogDisengageFraction * aggLogPerPathCapacityFPS

	// aggLogCollapseDwell/aggLogLoadTau are configured explicitly (smaller than
	// internal/config's defaults) so the e2e run stays fast while remaining a real
	// hysteresis measurement, not an instant flip. aggLogCollapseDwell is set well
	// ABOVE the harness's own inter-phase wall-clock jitter (AwaitLogLine + log
	// parse + spawning the low-phase driver's nsenter'd UDP sink between the
	// engage drive's last frame and the low drive's first frame): if that jitter
	// span reached aggLogCollapseDwell, the low phase's first Pick would collapse
	// via the idle-gap branch (updateGateLocked's gap>=CollapseDwell check)
	// instead of the sustained-low-load dwell this test measures, flipping the
	// asserted "reason" nondeterministically. 2s gives that margin comfortably.
	aggLogCollapseDwell = 2 * time.Second
	aggLogLoadTau       = 100 * time.Millisecond

	// aggLogEngageOfferedFPS is comfortably above aggLogEngageThresholdFPS (225).
	aggLogEngageOfferedFPS = 400.0
	// aggLogEngageDuration is how long the above-engage offered load runs: several
	// aggLogLoadTau for the EWMA to converge and cross the threshold, plus a fixed
	// margin so the flip is observed well before the driver returns.
	aggLogEngageDuration = 10*aggLogLoadTau + 1*time.Second

	// aggLogLowOfferedFPS is "stopping the load": comfortably below
	// aggLogDisengageThresholdFPS (125), so the sustained-low-load branch collapses
	// the gate (not the abrupt-idle-gap branch — this test measures the dwell path).
	aggLogLowOfferedFPS = 20.0
	// aggLogCollapseWaitBudget is the collapse-dwell + EWMA-tau budget the
	// acceptance names: the dwell itself, plus several tau for the EWMA to decay
	// below the disengage threshold before the dwell even starts counting, plus a
	// fixed safety margin for scheduling/log-flush jitter. The low-rate driver runs
	// for this long, so by the time it returns the collapse record is already
	// captured — no polling sleep needed on top.
	aggLogCollapseWaitBudget = aggLogCollapseDwell + 10*aggLogLoadTau + 1*time.Second

	aggLogPayloadBytes = 512
	aggLogSinkPort     = 6002
)

// aggLogPath is the single emulated uplink this test's tunnel runs over. An
// uncapped rate (rateMbit 0) so netem shaping never throttles the SEND side the
// scheduler's Pick observes (DriveUDPLoad's own send accounting is what drives
// the offered-load meter, independent of what the link later delivers).
var aggLogPath = pathSpec{
	name:     "gate",
	edgeIP:   "10.100.3.1",
	concIP:   "10.100.3.2",
	edgeVeth: "wbGe",
	concVeth: "wbGc",
	delayMs:  5,
	jitterMs: 0,
}

func TestAggregationGateLog(t *testing.T) {
	bin := buildWanbond(t)
	top := SetupWithPaths(t, []pathSpec{aggLogPath})

	edge, conc := setupAggLogTunnel(t, top, bin)
	if !top.pingUntil(concInner, 15*time.Second) {
		t.Fatalf("aggregation-gate-log: tunnel never came up\n--- edge ---\n%s\n--- conc ---\n%s", edge.log(), conc.log())
	}

	sinkAddr := net.JoinHostPort(concInner, strconv.Itoa(aggLogSinkPort))

	// Engage: push offered load above the engage threshold.
	top.DriveUDPLoad(t, edgeInner, sinkAddr, UDPLoadSpec{
		TargetFPS:    aggLogEngageOfferedFPS,
		PayloadBytes: aggLogPayloadBytes,
		Duration:     aggLogEngageDuration,
	})

	engageLine, ok := AwaitLogLine(t, edge, "scheduler aggregation change", 5*time.Second)
	if !ok {
		t.Fatalf("no %q record within 5s of driving %.0f fps (engage threshold %.0f fps)\n%s",
			"scheduler aggregation change", aggLogEngageOfferedFPS, aggLogEngageThresholdFPS, edge.log())
	}
	if to, _ := engageLine.FieldString("to"); to != "aggregating" {
		t.Fatalf("first 'scheduler aggregation change' record has to=%q, want %q\n%s", to, "aggregating", edge.log())
	}
	assertAggLogFields(t, edge, engageLine, "collapsed", "")

	engageLines := filterAggregationChangeLines(edge, "aggregating")
	if len(engageLines) != 1 {
		t.Fatalf("got %d 'scheduler aggregation change' to=aggregating records, want exactly 1 (no double-log)\n%s",
			len(engageLines), edge.log())
	}

	// "Stopping the load": drop to an offered rate well below the disengage
	// threshold, sustained for the collapse-dwell + EWMA-tau budget.
	top.DriveUDPLoad(t, edgeInner, sinkAddr, UDPLoadSpec{
		TargetFPS:    aggLogLowOfferedFPS,
		PayloadBytes: aggLogPayloadBytes,
		Duration:     aggLogCollapseWaitBudget,
	})

	collapseLines := filterAggregationChangeLines(edge, "collapsed")
	if len(collapseLines) != 1 {
		t.Fatalf("got %d 'scheduler aggregation change' to=collapsed records within the %s collapse-dwell+tau budget, want exactly 1\n%s",
			len(collapseLines), aggLogCollapseWaitBudget, edge.log())
	}
	// aggLogCollapseDwell (2s) comfortably exceeds the harness's own inter-phase
	// wall-clock jitter, so the collapse deterministically takes the
	// sustained-low-load branch, not the idle-gap branch: assert reason
	// explicitly, not just the shared uniform fields.
	assertAggLogFields(t, edge, collapseLines[0], "aggregating", "sustained low load")

	// Exactly one record per flip, end to end: one engage + one collapse, no more.
	if all := filterAggregationChangeLines(edge, ""); len(all) != 2 {
		t.Fatalf("got %d total 'scheduler aggregation change' records, want exactly 2 (1 engage + 1 collapse)\n%s", len(all), edge.log())
	}
}

// filterAggregationChangeLines returns every captured "scheduler aggregation
// change" record from edge's log, optionally filtered to a "to" value (empty
// string = no filter).
func filterAggregationChangeLines(edge *proc, to string) []LogLine {
	var out []LogLine
	for _, l := range ParseLogLines(edge.log()) {
		if l.Msg != "scheduler aggregation change" {
			continue
		}
		if to != "" {
			if got, _ := l.FieldString("to"); got != to {
				continue
			}
		}
		out = append(out, l)
	}
	return out
}

// assertAggLogFields checks the T143-added structured fields on a "scheduler
// aggregation change" record: "from" equals wantFrom, the "load_fps" field is
// present (uniform across every record — including an idle-gap collapse, per
// R179 fix 1), the new engage/disengage threshold fields match the configured
// gate, and — when wantReason is non-empty — "reason" equals wantReason
// exactly (pass "" to skip the reason check, e.g. for the engage record,
// which carries no reason).
func assertAggLogFields(t *testing.T, edge *proc, l LogLine, wantFrom, wantReason string) {
	t.Helper()
	if from, ok := l.FieldString("from"); !ok || from != wantFrom {
		t.Fatalf("'scheduler aggregation change' record: from = %q (ok=%v), want %q\n%s", from, ok, wantFrom, edge.log())
	}
	if _, ok := l.FieldFloat("load_fps"); !ok {
		t.Fatalf("'scheduler aggregation change' record missing the load_fps field (must be uniform across every record)\n%s", edge.log())
	}
	if got, ok := l.FieldFloat("engage_threshold_fps"); !ok || got != aggLogEngageThresholdFPS {
		t.Fatalf("'scheduler aggregation change' record: engage_threshold_fps = %v (ok=%v), want %g\n%s",
			got, ok, aggLogEngageThresholdFPS, edge.log())
	}
	if got, ok := l.FieldFloat("disengage_threshold_fps"); !ok || got != aggLogDisengageThresholdFPS {
		t.Fatalf("'scheduler aggregation change' record: disengage_threshold_fps = %v (ok=%v), want %g\n%s",
			got, ok, aggLogDisengageThresholdFPS, edge.log())
	}
	if wantReason != "" {
		if reason, ok := l.FieldString("reason"); !ok || reason != wantReason {
			t.Fatalf("'scheduler aggregation change' record: reason = %q (ok=%v), want %q\n%s", reason, ok, wantReason, edge.log())
		}
	}
}

// setupAggLogTunnel brings up the edge+concentrator tunnel over aggLogPath with
// the weighted scheduler (per_path_capacity_fps=250, explicit engage/disengage
// fractions and collapse_dwell/load_tau) and info-level structured logging (the
// level "scheduler aggregation change" is emitted at), mirroring
// setupLoadSelfTestTunnel's shape.
func setupAggLogTunnel(t *testing.T, top *Topology, bin string) (edge, conc *proc) {
	t.Helper()

	edgePriv, edgePub := genKey(t)
	concPriv, concPub := genKey(t)
	psk := randKey(t)
	p := aggLogPath

	// No [metrics] block: this test never queries /metrics (T146 defers that
	// wiring), and Metrics.Listen is optional — an unset listen leaves the
	// endpoint disabled (internal/device.applyMetricsLocked), so the daemon
	// starts fine without it.
	schedBlock := fmt.Sprintf(
		"[scheduler]\npolicy = \"weighted\"\nper_path_capacity_fps = %.1f\nengage_fraction = %g\ndisengage_fraction = %g\ncollapse_dwell = %q\nload_tau = %q\n\n",
		aggLogPerPathCapacityFPS, aggLogEngageFraction, aggLogDisengageFraction, aggLogCollapseDwell.String(), aggLogLoadTau.String())

	dir := t.TempDir()
	edgeCfg := writeConfig(t, filepath.Join(dir, "edge.toml"), fmt.Sprintf(`role = "edge"
psk = "%s"

[[paths]]
name = %q
source_addr = "%s"
dest_addr = "%s:%d"

%s[wireguard]
private_key = "%s"

[[wireguard.peers]]
public_key = "%s"
endpoint = "%s:%d"
allowed_ips = ["%s/32"]

[log]
level = "info"
`, psk, p.name, p.edgeIP, p.concIP, listenPort, schedBlock, edgePriv, concPub, p.concIP, listenPort, concInner))

	concCfg := writeConfig(t, filepath.Join(dir, "conc.toml"), fmt.Sprintf(`role = "concentrator"
psk = "%s"

[[paths]]
name = %q
source_addr = "%s"

%s[wireguard]
private_key = "%s"
listen_port = %d

[[wireguard.peers]]
public_key = "%s"
allowed_ips = ["%s/32"]

[log]
level = "info"
`, psk, p.name, p.concIP, schedBlock, concPriv, listenPort, edgePub, edgeInner))

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
