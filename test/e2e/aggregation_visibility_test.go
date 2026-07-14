//go:build e2e

package e2e

import (
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/metrics"
)

// T147 (G13): the T143 log lines and T146 gauges are each individually tested
// (aggregation_gate_log_test.go, aggregation_metrics_test.go) but never TOGETHER,
// end to end, across an actual engage/disengage flip driven by the T141 sustained-
// load harness. This file closes that gap: it proves the operator blind spot the
// two prior tasks addressed piecemeal is now OBSERVABLE as a single coherent story
// — the metric flips and the log records agree with each other and with the
// configured knobs, and a configured-but-never-engaging deployment now reports its
// own inertness on /metrics instead of looking identical to true single-path
// operation.
//
// Two scenarios:
//
//	(A) engage/disengage flip: a weighted policy with a small per_path_capacity_fps
//	    (250) is driven above its engage threshold, observed to engage on both the
//	    wanbond_aggregation_engaged gauge AND the canonical T143 "scheduler
//	    aggregation change" log record, then driven at a low sustained rate (the
//	    T143 "stop the load" idiom — see aggregation_gate_log_test.go's header: the
//	    gate only advances on a Pick, so a truly silent tunnel never re-evaluates
//	    the gate at all) and observed to collapse back within the configured
//	    collapse-dwell + load-tau budget, again on both the gauge and the log.
//	(B) configured-but-inert: the DEFAULT per_path_capacity_fps (10000) under a
//	    modest sustained load never engages, while /metrics now makes that
//	    inertness measurable (a clearly nonzero offered-load gauge sitting well
//	    below the engage-threshold gauge) instead of the operator blind spot this
//	    goal (G13) exists to close.
//
// Every wait is DERIVED from the scheduler's configured knobs (visCollapseDwell,
// visLoadTau) plus a fixed safety margin — no magic sleeps (matching the T143
// precedent this file extends).
const (
	// visFlipPerPathCapacityFPS is scenario A's small per_path_capacity_fps, sized
	// so the engage/disengage flip is reachable with a modest offered load.
	visFlipPerPathCapacityFPS = 250.0
	// visEngageFraction/visDisengageFraction are set explicitly (matching
	// internal/config's defaults) so the expected gauge/log threshold values do not
	// depend on those defaults staying in sync (mirrors aggLogEngageFraction's
	// rationale in aggregation_gate_log_test.go).
	visEngageFraction    = 0.9
	visDisengageFraction = 0.5
	// visEngageThresholdFPS/visDisengageThresholdFPS are the resulting gate
	// thresholds, asserted both on the gauge values and the log's structured fields.
	visEngageThresholdFPS    = visEngageFraction * visFlipPerPathCapacityFPS
	visDisengageThresholdFPS = visDisengageFraction * visFlipPerPathCapacityFPS

	// visCollapseDwell/visLoadTau are configured explicitly (smaller than
	// internal/config's defaults) so the flip is fast but still a real hysteresis
	// measurement, not an instant transition. visCollapseDwell sits well above the
	// harness's own inter-phase wall-clock jitter (spawning the low-phase driver's
	// UDP sink between the engage drive's last frame and the low drive's first
	// frame) so the collapse deterministically takes the sustained-low-load branch
	// (updateGateLocked), not the idle-gap branch — mirroring
	// aggLogCollapseDwell's rationale.
	visCollapseDwell = 2 * time.Second
	visLoadTau       = 100 * time.Millisecond

	// visEngageOfferedFPS is comfortably above visEngageThresholdFPS (225).
	visEngageOfferedFPS = 400.0
	// visEngageDuration lets the offered-load EWMA converge (several visLoadTau)
	// and cross the engage threshold, plus a fixed margin, plus enough further
	// runtime for the sampler to retain a post-engage sample.
	visEngageDuration = 15*visLoadTau + 2*time.Second

	// visLowOfferedFPS is "stopping the load": comfortably below
	// visDisengageThresholdFPS (125), so the sustained-low-load branch collapses
	// the gate (not the abrupt-idle-gap branch).
	visLowOfferedFPS = 20.0
	// visCollapseWaitBudget is the collapse-dwell + EWMA-tau budget the acceptance
	// names: the dwell itself, plus several tau for the EWMA to decay below the
	// disengage threshold before the dwell even starts counting, plus a fixed
	// safety margin for scheduling/log-flush/sampler jitter.
	visCollapseWaitBudget = visCollapseDwell + 15*visLoadTau + 2*time.Second

	visSampleInterval = 50 * time.Millisecond
	visPayloadBytes   = 512
	visFlipSinkPort   = 6003

	// visMetricsListen is this file's single /metrics port (see the metrics-port
	// registry in netns.go — 9110 is the next unused port). Both scenarios use it:
	// each runs in its own netns (separate Topology), sequentially, so the same
	// port number never collides (mirrors every other e2e file's single-port
	// convention, e.g. probe_headroom_test.go's t145MetricsListen).
	visMetricsListen = "127.0.0.1:9110"
	visMetricsURL    = "http://" + visMetricsListen + "/metrics"
	visMetricsWait   = 15 * time.Second

	// visInertSinkPort is scenario B's UDP load sink port on the concentrator inner
	// (tunnel) address — distinct from visFlipSinkPort, though each scenario runs
	// in its own netns so this is a readability convention, not a hard requirement.
	visInertSinkPort = 6004

	// visInertOfferedFPS is scenario B's "modest sustained load": clearly nonzero,
	// but far below the DEFAULT per_path_capacity_fps (10000) engage threshold
	// (0.9*10000 = 9000).
	visInertOfferedFPS = 400.0
	// visInertDuration is >= 5s (the acceptance floor) plus margin.
	visInertDuration = 6 * time.Second
	// visInertMinSamples is the acceptance's ">=5s of sustained load" restated as a
	// sample-count floor at visSampleInterval, so the assertion is on RETAINED
	// samples spanning that window, not merely on the driver's wall-clock duration.
	visInertMinSamples = int(5 * time.Second / visSampleInterval)
)

// visFlipPath is scenario A's single emulated uplink. Uncapped (rateMbit 0) so
// netem shaping never throttles the SEND side the scheduler's Pick observes —
// DriveUDPLoad's own send accounting is what drives the offered-load meter,
// independent of what the link later delivers (mirrors aggLogPath).
var visFlipPath = pathSpec{
	name:     "visflip",
	edgeIP:   "10.100.9.1",
	concIP:   "10.100.9.2",
	edgeVeth: "wbIe",
	concVeth: "wbIc",
	delayMs:  5,
	jitterMs: 0,
}

// visInertPath is scenario B's single emulated uplink.
var visInertPath = pathSpec{
	name:     "visinert",
	edgeIP:   "10.100.10.1",
	concIP:   "10.100.10.2",
	edgeVeth: "wbJe",
	concVeth: "wbJc",
	delayMs:  5,
	jitterMs: 0,
}

// TestAggregationVisibilityEngageDisengageFlip is scenario A: a weighted-policy
// daemon driven through a full engage->disengage cycle, asserting the
// wanbond_aggregation_engaged/offered_load_fps gauges and the "scheduler
// aggregation change" log records agree at each flip.
func TestAggregationVisibilityEngageDisengageFlip(t *testing.T) {
	bin := buildWanbond(t)
	top := SetupWithPaths(t, []pathSpec{visFlipPath})

	edge, conc := setupVisFlipTunnel(t, top, bin)
	if !top.pingUntil(concInner, 15*time.Second) {
		t.Fatalf("aggregation-visibility flip: tunnel never came up\n--- edge ---\n%s\n--- conc ---\n%s", edge.log(), conc.log())
	}

	// Baseline: idle, gate collapsed, gauge reads 0.
	idle := waitT144MetricsReady(t, visMetricsURL, visMetricsWait)
	if v, ok := idle.Value(metrics.MetricAggregationEngaged); !ok || v != 0 {
		t.Fatalf("aggregation-visibility flip: %s = %v (present=%v) at idle, want 0\n%s", metrics.MetricAggregationEngaged, v, ok, edge.log())
	}

	sinkAddr := net.JoinHostPort(concInner, strconv.Itoa(visFlipSinkPort))

	// --- Engage: push offered load above the engage threshold. ---
	engageSampler := StartMetricsSampler(t, visMetricsURL, visSampleInterval)
	top.DriveUDPLoad(t, edgeInner, sinkAddr, UDPLoadSpec{
		TargetFPS:    visEngageOfferedFPS,
		PayloadBytes: visPayloadBytes,
		Duration:     visEngageDuration,
	})
	engageSampler.Stop()

	engageSample, engageIdx := firstSampleWhere(engageSampler.Samples(), func(exp metrics.Exposition) bool {
		engaged, ok1 := exp.Value(metrics.MetricAggregationEngaged)
		offered, ok2 := exp.Value(metrics.MetricOfferedLoadFPS)
		threshold, ok3 := exp.Value(metrics.MetricAggregationEngageThreshold)
		return ok1 && ok2 && ok3 && engaged == 1 && offered > threshold
	})
	if engageIdx < 0 {
		t.Fatalf("aggregation-visibility flip: no sampled scrape across %s of %.0f fps offered load ever showed %s==1 with %s exceeding %s\n%s",
			visEngageDuration, visEngageOfferedFPS, metrics.MetricAggregationEngaged, metrics.MetricOfferedLoadFPS, metrics.MetricAggregationEngageThreshold, edge.log())
	}
	engageOffered, _ := engageSample.Value(metrics.MetricOfferedLoadFPS)
	engageThresholdGauge, _ := engageSample.Value(metrics.MetricAggregationEngageThreshold)
	t.Logf("aggregation-visibility flip: engaged at sample %d/%d (offered_load_fps=%.1f > engage_threshold_fps=%.1f)",
		engageIdx, len(engageSampler.Samples()), engageOffered, engageThresholdGauge)

	engageLine, ok := AwaitLogLine(t, edge, "scheduler aggregation change", 5*time.Second)
	if !ok {
		t.Fatalf("aggregation-visibility flip: no %q record within 5s of the metric flip\n%s", "scheduler aggregation change", edge.log())
	}
	if to, _ := engageLine.FieldString("to"); to != "aggregating" {
		t.Fatalf("aggregation-visibility flip: first 'scheduler aggregation change' record has to=%q, want %q\n%s", to, "aggregating", edge.log())
	}
	assertVisLogThresholds(t, edge, engageLine)

	engageLines := filterAggChangeLines(edge, "aggregating")
	if len(engageLines) != 1 {
		t.Fatalf("aggregation-visibility flip: got %d 'scheduler aggregation change' to=aggregating records, want exactly 1\n%s", len(engageLines), edge.log())
	}

	// --- "Stopping the load": drop to a sustained low rate (below the disengage
	// threshold) for the collapse-dwell + load-tau budget. A truly silent tunnel
	// never re-evaluates the gate at all (it only advances on a Pick — see
	// updateGateLocked's doc comment), so a low sustained rate is what makes the
	// collapse deterministically observable within a bounded budget. ---
	collapseSampler := StartMetricsSampler(t, visMetricsURL, visSampleInterval)
	top.DriveUDPLoad(t, edgeInner, sinkAddr, UDPLoadSpec{
		TargetFPS:    visLowOfferedFPS,
		PayloadBytes: visPayloadBytes,
		Duration:     visCollapseWaitBudget,
	})
	collapseSampler.Stop()

	collapseSample, collapseIdx := firstSampleWhere(collapseSampler.Samples(), func(exp metrics.Exposition) bool {
		engaged, ok1 := exp.Value(metrics.MetricAggregationEngaged)
		offered, ok2 := exp.Value(metrics.MetricOfferedLoadFPS)
		threshold, ok3 := exp.Value(metrics.MetricAggregationDisengageThreshold)
		return ok1 && ok2 && ok3 && engaged == 0 && offered < threshold
	})
	if collapseIdx < 0 {
		t.Fatalf("aggregation-visibility flip: no sampled scrape across the %s collapse-dwell+tau budget ever showed %s==0 with %s below %s\n%s",
			visCollapseWaitBudget, metrics.MetricAggregationEngaged, metrics.MetricOfferedLoadFPS, metrics.MetricAggregationDisengageThreshold, edge.log())
	}
	collapseOffered, _ := collapseSample.Value(metrics.MetricOfferedLoadFPS)
	collapseThresholdGauge, _ := collapseSample.Value(metrics.MetricAggregationDisengageThreshold)
	t.Logf("aggregation-visibility flip: disengaged at sample %d/%d (offered_load_fps=%.1f < disengage_threshold_fps=%.1f)",
		collapseIdx, len(collapseSampler.Samples()), collapseOffered, collapseThresholdGauge)

	collapseLines := filterAggChangeLines(edge, "collapsed")
	if len(collapseLines) != 1 {
		t.Fatalf("aggregation-visibility flip: got %d 'scheduler aggregation change' to=collapsed records within the %s budget, want exactly 1\n%s",
			len(collapseLines), visCollapseWaitBudget, edge.log())
	}
	collapseLine := collapseLines[0]
	if from, _ := collapseLine.FieldString("from"); from != "aggregating" {
		t.Fatalf("aggregation-visibility flip: collapse record has from=%q, want %q\n%s", from, "aggregating", edge.log())
	}
	assertVisLogThresholds(t, edge, collapseLine)

	// Exactly one record per flip, end to end: one engage + one collapse, no more.
	if all := filterAggChangeLines(edge, ""); len(all) != 2 {
		t.Fatalf("aggregation-visibility flip: got %d total 'scheduler aggregation change' records, want exactly 2 (1 engage + 1 collapse)\n%s", len(all), edge.log())
	}
	t.Logf("aggregation-visibility flip: 0->1->0 confirmed on both the gauges and the canonical T143 log records")
}

// TestAggregationVisibilityConfiguredButInert is scenario B: DEFAULT
// per_path_capacity_fps (10000, config's defaultPerPathCapacityFPS) under a
// modest sustained load never engages aggregation, while wanbond_offered_load_fps
// now makes that inertness measurable — a clearly nonzero gauge sitting well below
// the engage-threshold gauge, rather than looking identical to true single-path
// operation.
func TestAggregationVisibilityConfiguredButInert(t *testing.T) {
	bin := buildWanbond(t)
	top := SetupWithPaths(t, []pathSpec{visInertPath})

	edge, conc := setupVisInertTunnel(t, top, bin)
	if !top.pingUntil(concInner, 15*time.Second) {
		t.Fatalf("aggregation-visibility inert: tunnel never came up\n--- edge ---\n%s\n--- conc ---\n%s", edge.log(), conc.log())
	}

	// The configured-but-inert scenario is policy=weighted with NO per_path_capacity_fps
	// set: the daemon falls back to the synthetic defaultPerPathCapacityFPS (10000), so
	// the engage threshold (0.9*10000=9000) sits far above visInertOfferedFPS (400).
	idle := waitT144MetricsReady(t, visMetricsURL, visMetricsWait)
	if v, ok := idle.Value(metrics.MetricAggregationEngaged); !ok || v != 0 {
		t.Fatalf("aggregation-visibility inert: %s = %v (present=%v) at idle, want 0\n%s", metrics.MetricAggregationEngaged, v, ok, edge.log())
	}
	engageThresholdGauge, ok := idle.Value(metrics.MetricAggregationEngageThreshold)
	if !ok {
		t.Fatalf("aggregation-visibility inert: %s absent, want present (a weighted-policy edge always exposes it)\n%s", metrics.MetricAggregationEngageThreshold, edge.log())
	}
	if engageThresholdGauge <= visInertOfferedFPS {
		t.Fatalf("aggregation-visibility inert: engage_threshold_fps=%.1f is not comfortably above the %.1f-fps offered load this scenario drives; fixture misconfigured\n%s",
			engageThresholdGauge, visInertOfferedFPS, edge.log())
	}

	sinkAddr := net.JoinHostPort(concInner, strconv.Itoa(visInertSinkPort))
	sampler := StartMetricsSampler(t, visMetricsURL, visSampleInterval)
	result := top.DriveUDPLoad(t, edgeInner, sinkAddr, UDPLoadSpec{
		TargetFPS:    visInertOfferedFPS,
		PayloadBytes: visPayloadBytes,
		Duration:     visInertDuration,
	})
	sampler.Stop()
	if result.Elapsed < 5*time.Second {
		t.Fatalf("aggregation-visibility inert: driver ran %s, want >= 5s (the acceptance floor)", result.Elapsed)
	}

	samples := sampler.Samples()
	if len(samples) < visInertMinSamples {
		t.Fatalf("aggregation-visibility inert: metrics sampler retained %d sample(s) across the %s load window, want >= %d (a >= 5s span at %s intervals)",
			len(samples), visInertDuration, visInertMinSamples, visSampleInterval)
	}

	sawNonzeroOffered := false
	for i, s := range samples {
		engaged, ok := s.Exp.Value(metrics.MetricAggregationEngaged)
		if !ok {
			t.Fatalf("aggregation-visibility inert: sample %d missing %s\n%s", i, metrics.MetricAggregationEngaged, edge.log())
		}
		if engaged != 0 {
			t.Fatalf("aggregation-visibility inert: sample %d has %s=%v, want 0 for the ENTIRE %s window (configured-but-inert: policy=weighted, per_path_capacity_fps at the default 10000, offered load only %.1f fps)\n%s",
				i, metrics.MetricAggregationEngaged, engaged, visInertDuration, visInertOfferedFPS, edge.log())
		}
		offered, ok := s.Exp.Value(metrics.MetricOfferedLoadFPS)
		if !ok {
			t.Fatalf("aggregation-visibility inert: sample %d missing %s\n%s", i, metrics.MetricOfferedLoadFPS, edge.log())
		}
		if offered < 0 || offered >= engageThresholdGauge {
			t.Fatalf("aggregation-visibility inert: sample %d has %s=%.1f, want within [0, %.1f) (below the engage threshold)\n%s",
				i, metrics.MetricOfferedLoadFPS, offered, engageThresholdGauge, edge.log())
		}
		if offered > 0 {
			sawNonzeroOffered = true
		}
	}
	if !sawNonzeroOffered {
		t.Fatalf("aggregation-visibility inert: every sampled %s was 0 across the whole window, want a clearly nonzero reading (the offered load was never measured)\n%s",
			metrics.MetricOfferedLoadFPS, edge.log())
	}

	// No aggregation-change log record fired at all: the gate never engaged, so it
	// never had anything to disengage from either.
	if lines := filterAggChangeLines(edge, ""); len(lines) != 0 {
		t.Fatalf("aggregation-visibility inert: got %d 'scheduler aggregation change' record(s), want 0 (the gate never engaged)\n%s", len(lines), edge.log())
	}
	t.Logf("aggregation-visibility inert: %s stayed 0 across %d samples while %s reported nonzero (< %.1f-fps engage threshold) for %s — the operator blind spot is now measurable",
		metrics.MetricAggregationEngaged, len(samples), metrics.MetricOfferedLoadFPS, engageThresholdGauge, visInertDuration)
	_ = conc // conc's log is not asserted on directly; kept for parity with the other tunnel-setup tests and possible future diagnostics.
}

// firstSampleWhere returns the first sample (and its index) in samples for which
// pred(sample.Exp) holds, or a zero MetricsSample and -1 if none does.
func firstSampleWhere(samples []MetricsSample, pred func(metrics.Exposition) bool) (metrics.Exposition, int) {
	for i, s := range samples {
		if pred(s.Exp) {
			return s.Exp, i
		}
	}
	return metrics.Exposition{}, -1
}

// filterAggChangeLines returns every captured "scheduler aggregation change"
// record from edge's log, optionally filtered to a "to" value (empty string = no
// filter). Mirrors aggregation_gate_log_test.go's filterAggregationChangeLines.
func filterAggChangeLines(edge *proc, to string) []LogLine {
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

// assertVisLogThresholds checks that a "scheduler aggregation change" record's
// T143 threshold fields agree with this file's configured engage/disengage
// fractions*capacity — the log/metrics parity this scenario exists to prove.
func assertVisLogThresholds(t *testing.T, edge *proc, l LogLine) {
	t.Helper()
	if got, ok := l.FieldFloat("engage_threshold_fps"); !ok || got != visEngageThresholdFPS {
		t.Fatalf("'scheduler aggregation change' record: engage_threshold_fps = %v (ok=%v), want %g\n%s",
			got, ok, visEngageThresholdFPS, edge.log())
	}
	if got, ok := l.FieldFloat("disengage_threshold_fps"); !ok || got != visDisengageThresholdFPS {
		t.Fatalf("'scheduler aggregation change' record: disengage_threshold_fps = %v (ok=%v), want %g\n%s",
			got, ok, visDisengageThresholdFPS, edge.log())
	}
}

// setupVisFlipTunnel brings up the edge+concentrator tunnel over visFlipPath with
// the weighted scheduler (per_path_capacity_fps=250, explicit engage/disengage
// fractions and collapse_dwell/load_tau), /metrics, and info-level structured
// logging on both ends (mirrors setupAggLogTunnel + setupLoadSelfTestTunnel's
// combined shape: this file needs both the log AND the gauges).
func setupVisFlipTunnel(t *testing.T, top *Topology, bin string) (edge, conc *proc) {
	t.Helper()

	edgePriv, edgePub := genKey(t)
	concPriv, concPub := genKey(t)
	psk := randKey(t)
	p := visFlipPath

	schedBlock := fmt.Sprintf(
		"[scheduler]\npolicy = \"weighted\"\nper_path_capacity_fps = %.1f\nengage_fraction = %g\ndisengage_fraction = %g\ncollapse_dwell = %q\nload_tau = %q\n\n",
		visFlipPerPathCapacityFPS, visEngageFraction, visDisengageFraction, visCollapseDwell.String(), visLoadTau.String())
	metricsBlock := fmt.Sprintf("[metrics]\nlisten = %q\n\n", visMetricsListen)

	return startVisTunnel(t, top, bin, p, psk, edgePriv, edgePub, concPriv, concPub, schedBlock, metricsBlock)
}

// setupVisInertTunnel brings up the edge+concentrator tunnel over visInertPath
// with the weighted scheduler at the DEFAULT per_path_capacity_fps (no
// per_path_capacity_fps key at all — the daemon applies config's synthetic
// defaultPerPathCapacityFPS=10000), /metrics, and info-level structured logging.
func setupVisInertTunnel(t *testing.T, top *Topology, bin string) (edge, conc *proc) {
	t.Helper()

	edgePriv, edgePub := genKey(t)
	concPriv, concPub := genKey(t)
	psk := randKey(t)
	p := visInertPath

	schedBlock := "[scheduler]\npolicy = \"weighted\"\n\n"
	metricsBlock := fmt.Sprintf("[metrics]\nlisten = %q\n\n", visMetricsListen)

	return startVisTunnel(t, top, bin, p, psk, edgePriv, edgePub, concPriv, concPub, schedBlock, metricsBlock)
}

// startVisTunnel is the shared bring-up shape both setupVis*Tunnel helpers use:
// write edge+concentrator configs (path p, the given [scheduler]/[metrics]
// blocks), start both daemons, and wait for the TUN devices on both ends.
func startVisTunnel(t *testing.T, top *Topology, bin string, p pathSpec, psk, edgePriv, edgePub, concPriv, concPub, schedBlock, metricsBlock string) (edge, conc *proc) {
	t.Helper()

	dir := t.TempDir()
	edgeCfg := writeConfig(t, filepath.Join(dir, "edge.toml"), fmt.Sprintf(`role = "edge"
psk = "%s"

[[paths]]
name = %q
source_addr = "%s"
dest_addr = "%s:%d"

%s%s[wireguard]
private_key = "%s"

[[wireguard.peers]]
public_key = "%s"
endpoint = "%s:%d"
allowed_ips = ["%s/32"]

[log]
level = "info"
`, psk, p.name, p.edgeIP, p.concIP, listenPort, schedBlock, metricsBlock, edgePriv, concPub, p.concIP, listenPort, concInner))

	concCfg := writeConfig(t, filepath.Join(dir, "conc.toml"), fmt.Sprintf(`role = "concentrator"
psk = "%s"

[[paths]]
name = %q
source_addr = "%s"

%s%s[wireguard]
private_key = "%s"
listen_port = %d

[[wireguard.peers]]
public_key = "%s"
allowed_ips = ["%s/32"]

[log]
level = "info"
`, psk, p.name, p.concIP, schedBlock, metricsBlock, concPriv, listenPort, edgePub, edgeInner))

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
