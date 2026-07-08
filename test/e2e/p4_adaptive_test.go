//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/metrics"
)

// P4 adaptive-FEC tuning.
//
// The P4 acceptance is a COMPARISON at EQUAL MASKING: at steady P4SteadyLossRate (5%) path
// loss, adaptive FEC must (a) keep the post-recovery residual loss <= P4ResidualLossMax
// (0.5%) — the same masking bound the fixed baseline meets — while (b) spending <= the
// fixed baseline's overhead BYTES. The fixed baseline is the P3 ratio (K=10/M=6 = 60%
// frame overhead), which over-provisions for 5% loss; adaptive sizes parity to the measured
// loss and so should spend far less for the same masking.
//
// PARITY GEOMETRY. Both phases share K=p4DataShards data shards and a p4ParityCeiling parity
// ceiling. Fixed runs at the ceiling (M=6, the P3 baseline). Adaptive runs the controller
// with the ceiling as MaxParity, sizing M in [0,6] from the measured loss.
//
// SAFETY FACTOR. The controller sizes M so M/(K+M) >= SafetyFactor*loss. The simulation
// default (1.5) sizes M=1 at 5% with K=10 — which leaves ~1% residual (E[max(0,D-1)]/K for
// D~Bin(10,0.05)), ABOVE the 0.5% bound. Meeting a 0.5% residual SLA under binomial per-
// group variance needs more headroom, so P4 configures p4SafetyFactor to lift M to ~3 (e =
// 4.0*0.05 = 0.20 -> M = ceil(10*0.2/0.8) = 3), whose residual is far under 0.5% AND whose
// overhead (~3 parity/group) is still well under the fixed M=6 baseline. The knob is the
// residual-tuning lever the deployment sets for its SLA; the controller itself is unchanged.
const (
	p4DataShards    = 10  // K
	p4ParityCeiling = 6   // M ceiling (== the fixed P3 baseline M)
	p4SafetyFactor  = 4.0 // adaptive redundancy headroom -> M~3 at 5% loss (see above)

	// p4DeadlineNanos mirrors p3DeadlineNanos: 100ms sits under maxFECDeadline yet is large
	// enough that groups FILL toward K at the fixture's low frame rate (see p3_fec_test.go).
	p4DeadlineNanos = 100 * 1000 * 1000

	p4MetricsListen = "127.0.0.1:9097"
	p4MetricsURL    = "http://" + p4MetricsListen + "/metrics"

	// p4WarmupSecs is the settle time after injecting loss BEFORE the measurement window:
	// long enough for the per-path probe-loss estimate to fill with lossy samples and the
	// controller to slew to its steady M (RateInterval 500ms, EWMA over a few 200ms probe
	// intervals), so the window measures the controller's STEADY overhead, not its ramp.
	p4WarmupSecs = 8

	// p4LoadSecs is the saturating single-flow measurement window: long enough to accumulate
	// thousands of DATA frames / hundreds of groups so the byte ratios and the residual are
	// statistically meaningful at the fixture's low frame rate (mirrors p3LoadSecs).
	p4LoadSecs = 30

	// p4SettleSecs lets in-flight recovery land before the residual gauge is scraped: with
	// loss still injected but the flow stopped, no new outer-seqs advance the residual window,
	// so any late reconstruction fills its gap and the gauge settles to the true residual.
	p4SettleSecs = 2

	// p4MinDataFrames is the minimum DATA-frame sample the window must carry for the byte
	// ratios and residual to mean anything (mirrors p3MinDataFrames); below it the fixture
	// delivered too few frames and the test FAILS loudly rather than passing vacuously.
	p4MinDataFrames = 2000
)

// p4Path is the single emulated uplink for P4: same profile as p3Path (20ms one-way delay,
// uncapped, no jitter). Loss is injected at runtime via InjectLoss. It reuses the shared
// veth names; safe because each phase owns and tears down its own topology.
var p4Path = pathSpec{name: "wan", edgeIP: "10.100.1.1", concIP: "10.100.1.2", edgeVeth: "wbAe", concVeth: "wbAc", delayMs: 20}

// p4Result carries one phase's measured window: the post-recovery residual (from the
// decoder/concentrator), the byte-denominated overhead (from the encoder/edge), and the
// raw counters for the log.
type p4Result struct {
	adaptive      bool
	residual      float64 // post-FEC-recovery connection loss fraction (conc /metrics gauge)
	overheadBytes float64 // repair_bytes / data_bytes (edge deltas)
	overheadFrame float64 // parity_frames / data_frames (edge deltas, for the log)
	dataFrames    float64
	dataBytes     float64
	repairBytes   float64
	goodput       float64
}

// TestP4AdaptiveFEC is the P4 acceptance. It runs the adaptive plane and the fixed-FEC
// baseline at the SAME steady 5% loss, establishes EQUAL MASKING (both keep residual loss
// <= P4ResidualLossMax), and then asserts the adaptive overhead BYTES are <= the fixed
// baseline's — the "same masking for less overhead" claim. Both phases read from /metrics.
//
// The two phases run sequentially (the shared veth names forbid two live topologies), each
// standing up and tearing down its own fixture. The overhead comparison is gated on masking-
// equality: it is only meaningful once BOTH phases meet the residual bound, so a phase that
// fails to mask fails the test at that phase rather than yielding a vacuous comparison.
func TestP4AdaptiveFEC(t *testing.T) {
	bin := buildWanbond(t)

	var adaptive, fixed p4Result
	okA := t.Run("adaptive", func(t *testing.T) { adaptive = runP4Phase(t, bin, true) })
	okF := t.Run("fixed-baseline", func(t *testing.T) { fixed = runP4Phase(t, bin, false) })
	if !okA || !okF {
		return // a phase failed its own sample/masking guards; those errors stand
	}

	t.Logf("P4 summary @ %.0f%% loss:\n  adaptive: residual=%.4f overheadBytes=%.4f (frameOverhead=%.3f) data=%.0f goodput=%.2fMbit/s\n  fixed:    residual=%.4f overheadBytes=%.4f (frameOverhead=%.3f) data=%.0f goodput=%.2fMbit/s",
		P4SteadyLossRate*100,
		adaptive.residual, adaptive.overheadBytes, adaptive.overheadFrame, adaptive.dataFrames, adaptive.goodput,
		fixed.residual, fixed.overheadBytes, fixed.overheadFrame, fixed.dataFrames, fixed.goodput)

	// Masking-equality gate (both must mask to <= P4ResidualLossMax). The fixed baseline
	// over-provisions, so its residual failing here signals a fixture/measurement problem,
	// not an adaptive regression — surface it distinctly.
	if fixed.residual > P4ResidualLossMax {
		t.Fatalf("fixed baseline residual %.4f > %.4f — baseline did not mask 5%% loss (fixture/measurement issue); the equal-masking comparison is invalid",
			fixed.residual, P4ResidualLossMax)
	}
	if adaptive.residual > P4ResidualLossMax {
		t.Errorf("adaptive residual %.4f > %.4f (P4ResidualLossMax) at %.0f%% loss — adaptive did not mask to the required bound; raise fec.safety_factor (currently %.1f) to lift M",
			adaptive.residual, P4ResidualLossMax, P4SteadyLossRate*100, p4SafetyFactor)
	}

	// The overhead-BYTES comparison: at equal masking, adaptive spends <= the fixed baseline.
	if adaptive.overheadBytes > fixed.overheadBytes {
		t.Errorf("adaptive overhead bytes %.4f > fixed baseline %.4f — adaptive did not save overhead at equal masking",
			adaptive.overheadBytes, fixed.overheadBytes)
	} else {
		t.Logf("adaptive overhead bytes %.4f <= fixed baseline %.4f (%.0f%% of baseline) at residual %.4f <= %.4f — PASS",
			adaptive.overheadBytes, fixed.overheadBytes, 100*adaptive.overheadBytes/fixed.overheadBytes, adaptive.residual, P4ResidualLossMax)
	}

	appendP4Checklist(t)
}

// runP4Phase brings up one FEC-enabled tunnel (adaptive or fixed) over p4Path, injects
// steady P4SteadyLossRate loss, warms up, drives a saturating upload, and returns the
// window's residual (from the concentrator/decoder) and overhead bytes (from the edge/
// encoder). It fails the subtest on a bring-up failure or an insufficient sample.
func runP4Phase(t *testing.T, bin string, adaptive bool) p4Result {
	label := "fixed"
	if adaptive {
		label = "adaptive"
	}
	top := SetupWithPaths(t, []pathSpec{p4Path})
	edge, conc := setupP4Tunnel(t, top, bin, adaptive)
	if !top.pingUntil(concInner, 15*time.Second) {
		t.Fatalf("p4 %s: tunnel never came up\n--- edge ---\n%s\n--- conc ---\n%s", label, edge.log(), conc.log())
	}

	// Steady uniform egress loss on the edge->conc direction (where the upload DATA frames
	// flow), then warm up so the probe-loss estimate and the adaptive controller settle.
	top.InjectLoss("wan", P4SteadyLossRate*100)
	time.Sleep(time.Duration(p4WarmupSecs) * time.Second)

	// Window start: the SEND-side byte counters (overhead numerator/denominator) are charged
	// as frames reach the socket, so a delta across the transfer is exactly the parity/data
	// bytes the edge emitted while loss was injected and the controller was at steady M.
	ctxB, cancelB := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelB()
	edgeBefore := fetchMetrics(t, ctxB, p4MetricsURL)

	goodput := top.fecIperf3RecvMbps(t, concInner, p4LoadSecs)

	ctxM, cancelM := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelM()
	edgeAfter := fetchMetrics(t, ctxM, p4MetricsURL)
	dataFrames := deltaValue(t, edgeBefore, edgeAfter, metrics.MetricFECData)
	parityFrames := deltaValue(t, edgeBefore, edgeAfter, metrics.MetricFECRepair)
	dataBytes := deltaValue(t, edgeBefore, edgeAfter, metrics.MetricFECDataBytes)
	repairBytes := deltaValue(t, edgeBefore, edgeAfter, metrics.MetricFECRepairBytes)

	// Let in-flight recovery land, loss still on, before scraping the residual gauge.
	time.Sleep(time.Duration(p4SettleSecs) * time.Second)

	// The residual is a connection-scoped GAUGE read from the CONCENTRATOR (the decoder side,
	// where the upload's DATA frames are received and reconstructed). It reflects the trailing
	// window of post-recovery loss — outer-seqs neither received nor rebuilt from parity.
	concAfter := fetchMetricsInNetns(t, top.pid, p4MetricsURL)
	residual, ok := concAfter.Value(metrics.MetricFECResidualLoss)
	if !ok {
		t.Fatalf("p4 %s: concentrator /metrics missing %s", label, metrics.MetricFECResidualLoss)
	}

	// Data-plane survival (no reset / without retransmit).
	if goodput <= 0 {
		t.Fatalf("p4 %s: upload goodput non-positive (%.2f Mbit/s) — the flow did not survive the injected loss\n--- edge ---\n%s\n--- conc ---\n%s",
			label, goodput, edge.log(), conc.log())
	}
	// Sample-size guard: enough DATA frames for the byte ratios and residual to be meaningful.
	if dataFrames < p4MinDataFrames {
		t.Fatalf("p4 %s: only %.0f DATA frames over the window (< %d) — sample too small; raise throughput or p4LoadSecs",
			label, dataFrames, p4MinDataFrames)
	}
	if dataBytes <= 0 {
		t.Fatalf("p4 %s: DATA byte counter did not advance (%.0f) — overhead-bytes denominator empty", label, dataBytes)
	}

	overheadBytes := repairBytes / dataBytes
	overheadFrame := parityFrames / dataFrames
	t.Logf("p4 %s: residual=%.4f | edge data=%.0f parity=%.0f dataBytes=%.0f repairBytes=%.0f | overheadBytes=%.4f frameOverhead=%.3f | goodput=%.2fMbit/s",
		label, residual, dataFrames, parityFrames, dataBytes, repairBytes, overheadBytes, overheadFrame, goodput)

	return p4Result{
		adaptive:      adaptive,
		residual:      residual,
		overheadBytes: overheadBytes,
		overheadFrame: overheadFrame,
		dataFrames:    dataFrames,
		dataBytes:     dataBytes,
		repairBytes:   repairBytes,
		goodput:       goodput,
	}
}

// setupP4Tunnel brings up the edge+concentrator tunnel over the single p4Path with the FEC
// plane enabled and the /metrics endpoint on both ends. When adaptive is true the edge's
// [fec] block adds `adaptive = true` + the p4SafetyFactor; the concentrator (decoder) is
// unchanged either way — a group coded with fewer parity shards decodes against the
// parity_shards-ceiling codec unchanged — but it too gets the [fec] block so it builds the
// decoder at the ceiling. It mirrors setupP3Tunnel's addressing/bring-up.
func setupP4Tunnel(t *testing.T, top *Topology, bin string, adaptive bool) (edge, conc *proc) {
	t.Helper()

	edgePriv, edgePub := genKey(t)
	concPriv, concPub := genKey(t)
	psk := randKey(t)

	// The edge encodes: it carries the adaptive flag + safety factor when adaptive. The
	// concentrator decodes at the fixed ceiling; adaptive is a no-op there, so its block is
	// the plain fixed ratio (data_shards/parity_shards/deadline).
	edgeFEC := fmt.Sprintf("[fec]\nenabled = true\ndata_shards = %d\nparity_shards = %d\ndeadline = %d\n",
		p4DataShards, p4ParityCeiling, p4DeadlineNanos)
	if adaptive {
		edgeFEC += fmt.Sprintf("adaptive = true\nsafety_factor = %g\n", p4SafetyFactor)
	}
	edgeFEC += "\n"
	concFEC := fmt.Sprintf("[fec]\nenabled = true\ndata_shards = %d\nparity_shards = %d\ndeadline = %d\n\n",
		p4DataShards, p4ParityCeiling, p4DeadlineNanos)
	metricsBlock := fmt.Sprintf("[metrics]\nlisten = %q\n\n", p4MetricsListen)

	dir := t.TempDir()
	edgeCfg := writeConfig(t, filepath.Join(dir, "edge.toml"), fmt.Sprintf(`role = "edge"
psk = "%s"

[[paths]]
name = "%s"
source_addr = "%s"

%s%s[wireguard]
private_key = "%s"

[[wireguard.peers]]
public_key = "%s"
endpoint = "%s:%d"
allowed_ips = ["%s/32"]

[log]
level = "error"
`, psk, p4Path.name, p4Path.edgeIP, metricsBlock, edgeFEC, edgePriv, concPub, p4Path.concIP, listenPort, concInner))

	concCfg := writeConfig(t, filepath.Join(dir, "conc.toml"), fmt.Sprintf(`role = "concentrator"
psk = "%s"

[[paths]]
name = "%s"
source_addr = "%s"

%s%s[wireguard]
private_key = "%s"
listen_port = %d

[[wireguard.peers]]
public_key = "%s"
allowed_ips = ["%s/32"]

[log]
level = "error"
`, psk, p4Path.name, p4Path.concIP, metricsBlock, concFEC, concPriv, listenPort, edgePub, edgeInner))

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

// p4ChecklistMarker is the idempotency sentinel for appendP4Checklist.
const p4ChecklistMarker = "## P4 — scripted adaptive-vs-fixed run"

// appendP4Checklist appends the P4 scripted manual-verification section to
// docs/manual-checklist.md, idempotently (a second run is a no-op once the marker is
// present). It mirrors appendP3Checklist: the privileged e2e test owns the doc mutation.
func appendP4Checklist(t *testing.T) {
	t.Helper()
	path := p3ChecklistPath(t) // module-root docs/manual-checklist.md (shared resolver)
	existing, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read manual checklist %s: %v", path, err)
	}
	if strings.Contains(string(existing), p4ChecklistMarker) {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open manual checklist %s: %v", path, err)
	}
	defer f.Close()
	if _, err := f.WriteString(p4ChecklistSection()); err != nil {
		t.Fatalf("append P4 checklist to %s: %v", path, err)
	}
	t.Logf("appended P4 scripted checklist to %s", path)
}

// p4ChecklistSection renders the P4 scripted real-setup section. The geometry, safety
// factor, and thresholds are interpolated from the same constants the automated test
// asserts, so the manual and automated criteria never drift.
func p4ChecklistSection() string {
	var b strings.Builder
	fixedRatio := float64(p4ParityCeiling) / float64(p4DataShards)
	b.WriteString("\n## P4 — scripted adaptive-vs-fixed run (steady loss, adaptive FEC)\n\n")
	b.WriteString("Scripted counterpart of the P4 phase. Run the SAME single-path tunnel at a steady\n")
	fmt.Fprintf(&b, "%.0f%% uplink loss TWICE: once fixed-ratio (the P3 baseline), once adaptive.\n\n", P4SteadyLossRate*100)
	b.WriteString("Fixed (baseline) edge+conc [fec]:\n```toml\n")
	fmt.Fprintf(&b, "[fec]\nenabled = true\ndata_shards = %d   # K\nparity_shards = %d  # M ceiling\ndeadline = \"%dms\"\n", p4DataShards, p4ParityCeiling, p4DeadlineNanos/1_000_000)
	b.WriteString("```\n\nAdaptive edge [fec] (conc keeps the fixed block above — it decodes at the ceiling):\n```toml\n")
	fmt.Fprintf(&b, "[fec]\nenabled = true\ndata_shards = %d\nparity_shards = %d  # ceiling\ndeadline = \"%dms\"\nadaptive = true\nsafety_factor = %g\n", p4DataShards, p4ParityCeiling, p4DeadlineNanos/1_000_000, p4SafetyFactor)
	b.WriteString("```\n\n")
	fmt.Fprintf(&b, "The fixed ratio M/K = %.2f over-provisions for %.0f%% loss; adaptive sizes M to the\n", fixedRatio, P4SteadyLossRate*100)
	fmt.Fprintf(&b, "measured loss (safety_factor %.1f -> M~3 at %.0f%%), masking to the same residual for less\n", p4SafetyFactor, P4SteadyLossRate*100)
	b.WriteString("overhead. `FEC()` below is `curl -s http://127.0.0.1:9090/metrics | grep wanbond_fec`.\n")
	b.WriteString("Record date, `wanbond version`, and observed numbers next to each item.\n\n")

	b.WriteString("### Per phase (fixed, then adaptive)\n")
	fmt.Fprintf(&b, "- [ ] Bring the tunnel up; induce steady uniform loss on the edge uplink (`tc ... netem loss %.0f%%`).\n", P4SteadyLossRate*100)
	b.WriteString("- [ ] Warm up (~8s) so the adaptive controller reaches steady M, then take a saturating\n")
	b.WriteString("      upload (`iperf3 -c 10.77.0.1 -t 30`).\n")
	b.WriteString("- [ ] EDGE `/metrics`: DELTAS of `wanbond_fec_data_bytes_total` and\n")
	b.WriteString("      `wanbond_fec_repair_bytes_total` over the window; overhead bytes = `dRepair/dData`.\n")
	b.WriteString("- [ ] CONCENTRATOR `/metrics`: `wanbond_fec_residual_loss_ratio` (post-recovery residual).\n\n")

	b.WriteString("### Acceptance\n")
	fmt.Fprintf(&b, "- [ ] BOTH phases: residual <= %.3f (`P4ResidualLossMax`) — equal masking.\n", P4ResidualLossMax)
	b.WriteString("- [ ] Adaptive overhead bytes <= fixed baseline overhead bytes — same masking, less overhead.\n")
	b.WriteString("- [ ] Both uploads completed with positive receiver goodput and no reset.\n")
	return b.String()
}
