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

// P3 fixed-ratio FEC recovery tuning.
//
// PARITY RATIO (p3DataShards K / p3ParityShards M). A group of K+M shards recovers any M
// erasures, so its PER-GROUP erasure tolerance is M/(K+M). But netem loss is UNIFORM
// (independent per shard = binomial), so a given group occasionally loses MORE than the
// mean rate; the quantity P3 asserts is the recovered FRACTION across MANY groups, which
// needs tolerance well ABOVE the mean loss, not merely above it. Closed-form: with data
// losses D~Bin(K,p) and parity losses Q~Bin(M,p), a group is fully recoverable iff
// D+Q <= M, and the recovered fraction is E[D·1(D+Q<=M)]/E[D]. For K=10,M=6 (tolerance
// 6/16 = 37.5%) that fraction is ~0.9999 at p=0.05 and ~0.983 at p=0.15 — both clear
// P3MinRecoveredFraction (0.95) with margin. (The intuitive K=10,M=4 — 28.6% tolerance —
// only reaches ~0.882 at 15% and FAILS 0.95: mean-tolerance headroom is not enough because
// of the per-group binomial variance.)
const (
	p3DataShards   = 10 // K
	p3ParityShards = 6  // M

	// p3DeadlineNanos is the FEC group-close deadline (TOML time.Duration is integer
	// nanoseconds). 100ms sits just under maxFECDeadline (125ms = resequencerTimeout/2), so a
	// deadline-flushed group's parity still reaches the receive resequencer BEFORE it skips
	// the gap (recovery stays "in time", counted as delivered) — yet it is 20x the 5ms
	// default so groups FILL toward K at the fixture's low packet rate. That coupling matters
	// here: the single-host netns fixture is CPU/PPS-bound at a few Mbit/s (T23 finding), and
	// at that rate the 5ms default would flush groups after only ~1-3 DATA frames, emitting M
	// parity over a near-empty group and driving ParityFrames/DataFrames FAR above M/K — a
	// spurious overhead failure. A 100ms deadline lets ~full K=10 groups form at any rate
	// above ~100 fps, keeping measured overhead near M/K = 0.6 with headroom under the 1.2 cap.
	p3DeadlineNanos = 100 * 1000 * 1000

	p3MetricsListen = "127.0.0.1:9096"
	p3MetricsURL    = "http://" + p3MetricsListen + "/metrics"

	// p3LoadSecs is the saturating single-flow transfer duration. FEC counter deltas are
	// differenced across the whole transfer (the tunnel is already warm and loss is steady,
	// so bring-up traffic — scraped before the transfer starts — is excluded). Long enough to
	// accumulate thousands of DATA frames / hundreds of FEC groups so the recovered fraction
	// is statistically meaningful even at the fixture's low frame rate.
	p3LoadSecs = 30

	// p3MinDataFrames is the minimum DATA-frame sample the window must carry for the ratios
	// to mean anything. Below it the fixture delivered too few frames (raise throughput or
	// lengthen p3LoadSecs) and a 0.95 recovered fraction would be noise, so the test FAILS
	// loudly rather than passing vacuously.
	p3MinDataFrames = 2000

	// p3MinLostSample is the minimum FEC-accounted lost DATA frames (recovered+unrecoverable)
	// the window must carry, so the recovered-fraction DENOMINATOR is a real population.
	p3MinLostSample = 100

	// p3LostAccountingFloor gives the recovered fraction TEETH: the FEC-accounted lost frames
	// (recovered+unrecoverable) must be at least this fraction of the loss the injected rate
	// predicts (lossRate * DataFrames emitted). It confirms the injected loss actually reached
	// the decoder as accounted erasures — so a high recovered fraction cannot be an artefact
	// of the decoder not observing the loss (or, before the trailing drain below, of the
	// unrecoverable term being structurally under-reported).
	//
	// 0.8: with the trailing lossless drain forcing eviction+accounting of every loss-window
	// group (see p3Drain), the accounted lost frames capture essentially all of the predicted
	// loss. The residual gap that keeps this below 1.0 is small and bounded: (a) whole-group
	// losses — every one of a group's M parity shards ALSO dropped, so M is never learned and
	// the loss is accounted downstream by the resequencer gap timeout, not the FEC counters —
	// which is P(>=K+M-K+1 ... ) dominated by P(all M parity lost) = loss^M <= 0.15^6 ~ 1e-5;
	// (b) late-reconstructed frames (rebuilt but delivered after the resequencer released the
	// gap) — counted in NEITHER term — which the 100ms deadline (<< 250ms gap timeout) keeps
	// near zero; and (c) binomial sampling variance of the finite window. 0.8 leaves margin
	// for (c) while still failing if a structural chunk of the loss goes unaccounted (the
	// eviction-lag bug this drain fixes under-reported ~64% of tail failures — far outside an
	// 0.8 floor).
	p3LostAccountingFloor = 0.8

	// p3RetainGroups MIRRORS internal/bind.fecRetainGroups (the decoder's retained-group
	// window, in GROUPS). A group's repair failure is accounted `unrecoverable` ONLY when the
	// group is evicted, i.e. only once the decoder high-water advances MORE than this many
	// groups past it — and the high-water advances ONLY on newly-offered groups. Kept in sync
	// with the bind constant (a divergence would over- or under-size the drain).
	p3RetainGroups = 512

	// p3DrainMarginGroups is the safety margin (in groups) the trailing drain advances the
	// high-water BEYOND p3RetainGroups, covering any straggler/reorder at the drain's own tail.
	p3DrainMarginGroups = 128

	// p3DrainMinDataFrames is the DATA-frame count the trailing lossless drain must emit so the
	// decoder high-water advances > p3RetainGroups groups past the loss window's last group,
	// evicting+accounting EVERY loss-window tail group before the after-scrape. Arithmetic: the
	// encoder opens one new group per at most p3DataShards (K) DATA frames, so N drain data
	// frames advance the high-water by AT LEAST N/K groups (partial/deadline-flushed groups
	// only advance it FASTER). (p3RetainGroups+p3DrainMarginGroups)*K = (512+128)*10 = 6400
	// data frames therefore guarantees >= 640 group-id advances >= 512+128, past the whole
	// retained window with margin.
	p3DrainMinDataFrames = (p3RetainGroups + p3DrainMarginGroups) * p3DataShards

	// p3DrainChunkSecs / p3DrainMaxWall bound the drain loop: it drives lossless upload chunks
	// until p3DrainMinDataFrames have flowed, giving up (fail-loud) after the wall-clock cap so
	// a pathologically slow fixture surfaces as a clear "drain too slow" failure rather than a
	// silent under-drain.
	p3DrainChunkSecs = 12
	p3DrainMaxWall   = 120 * time.Second
)

// p3Path is the single emulated uplink for the P3 FEC test: a fixed 20ms one-way delay
// (RTT ~40ms, well under the 250ms resequencer timeout), NO bandwidth cap (the fixture is
// already CPU/PPS-bound, and leaving the path uncapped maximises the achievable frame rate
// so groups fill and the sample is large), and NO jitter (jitter would inject reordering
// unrelated to the loss under test). Loss is injected at runtime per subtest via InjectLoss,
// which preserves this delay profile. It reuses DefaultPaths' veth names/IPs; safe because
// the test owns its own topology and tears it down between subtests.
var p3Path = pathSpec{name: "wan", edgeIP: "10.100.1.1", concIP: "10.100.1.2", edgeVeth: "wbAe", concVeth: "wbAc", delayMs: 20}

// TestP3FixedFEC is the P3 acceptance: with the fixed-ratio Reed-Solomon FEC plane enabled,
// at BOTH 5% and 15% uniform injected loss (P3InjectedLossRates) the tunnel (a) recovers
// >= P3MinRecoveredFraction of the lost DATA frames without retransmit, and (b) spends FEC
// overhead <= P3MaxOverheadFactor x the configured parity ratio (M/K) — both read from the
// daemons' /metrics endpoints. A saturating single-flow TCP upload spans each subtest; its
// completion with positive receiver goodput is the "without retransmit / data-plane
// survived" corroboration (FEC masked the loss so the flow never reset).
//
// WHICH END EXPOSES WHICH COUNTER. The upload flows edge->concentrator, so its DATA frames
// are FEC-ENCODED by the EDGE and FEC-DECODED by the CONCENTRATOR, and netem loss is
// injected on the edge egress (the edge->conc direction). Therefore the OVERHEAD numerator/
// denominator (parity/data frames EMITTED) are read from the EDGE /metrics, and the RECOVERY
// counts (recovered/unrecoverable data frames) are read from the CONCENTRATOR /metrics. Each
// counter is a DELTA across the transfer window.
//
// DENOMINATOR COMPLETENESS. The `unrecoverable` term is accounted lazily — only when a
// failed group is evicted from the decoder's retained-group window, which lags the traffic
// by p3RetainGroups (512) groups and only advances on newly-offered groups. So after the
// measured (lossy) transfer, this test CLEARS the loss and drives a trailing LOSSLESS drain
// (see runP3FixedFEC / p3Drain) that advances the high-water past every loss-window group,
// forcing their failures into `unrecoverable` before the recovery counts are scraped. Without
// this the recovered fraction would be structurally biased high (the last ~512 groups'
// failures never counted). The under-reporting is also a real production defect at quiescence
// (filed D24, pre-existing T24); the fix here is test-side only.
//
// Subtests run sequentially (no t.Parallel): the fixed veth names forbid two live
// topologies, so each subtest stands up and tears down its own before the next.
func TestP3FixedFEC(t *testing.T) {
	bin := buildWanbond(t)
	for _, loss := range P3InjectedLossRates {
		loss := loss
		t.Run(fmt.Sprintf("loss-%02.0fpct", loss*100), func(t *testing.T) {
			runP3FixedFEC(t, bin, loss)
		})
	}
	appendP3Checklist(t)
}

// runP3FixedFEC brings up an FEC-enabled tunnel over the single p3Path, injects uniform
// egress loss at lossFrac, drives a saturating upload, and asserts the recovered-fraction
// and overhead bounds from the two ends' /metrics counters.
func runP3FixedFEC(t *testing.T, bin string, lossFrac float64) {
	top := SetupWithPaths(t, []pathSpec{p3Path})
	edge, conc := setupP3Tunnel(t, top, bin)
	if !top.pingUntil(concInner, 15*time.Second) {
		t.Fatalf("p3 loss=%.0f%%: tunnel never came up\n--- edge ---\n%s\n--- conc ---\n%s",
			lossFrac*100, edge.log(), conc.log())
	}

	// Inject uniform egress loss on the edge->conc direction (where the upload DATA frames
	// flow) and let the qdisc change settle before sampling.
	top.InjectLoss("wan", lossFrac*100)
	time.Sleep(1 * time.Second)

	// Window start: scrape both ends. The concentrator endpoint is loopback-bound inside its
	// own netns, so it is scraped from within that netns.
	ctxB, cancelB := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelB()
	edgeBefore := fetchMetrics(t, ctxB, p3MetricsURL)
	concBefore := fetchMetricsInNetns(t, top.pid, p3MetricsURL)

	// Saturating single-flow TCP upload edge->conc THROUGH the tunnel: its DATA frames are
	// FEC-encoded by the edge, dropped uniformly by netem, and recovered by the concentrator.
	// A positive receiver goodput (sum_received) with no error is the data-plane-survival
	// corroboration: FEC masked the loss so the flow completed without a reset.
	goodput := top.fecIperf3RecvMbps(t, concInner, p3LoadSecs)

	// Loss-window end. The SEND-side counts (overhead numerator/denominator) are complete NOW
	// — they are charged as frames reach the socket, so the loss-window delta is exactly the
	// parity/data the edge emitted while loss was injected.
	ctxM, cancelM := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelM()
	edgeMid := fetchMetrics(t, ctxM, p3MetricsURL)
	dataFrames := deltaValue(t, edgeBefore, edgeMid, metrics.MetricFECData)
	parityFrames := deltaValue(t, edgeBefore, edgeMid, metrics.MetricFECRepair)
	edgeMidData, _ := edgeMid.Value(metrics.MetricFECData)

	// COMPLETE THE RECEIVE-SIDE DENOMINATOR (the critical measurement-validity step). The
	// `unrecoverable` term is accounted ONLY when a group is EVICTED from the decoder's
	// retained window — which happens only once the high-water advances > p3RetainGroups (512)
	// groups past it, and the high-water advances ONLY on newly-offered groups. With no
	// trailing traffic, the loss window's LAST ~512 groups' repair failures would NEVER be
	// counted, so recovered/(recovered+unrecoverable) would be biased high (a masking of only
	// ~70% could read as 1.0). So: CLEAR the loss and drive a trailing LOSSLESS drain of
	// > p3RetainGroups groups. The drain adds NO failures of its own (lossless => its groups
	// complete from data alone => recovered/unrecoverable += 0); it only advances the
	// high-water past every loss-window tail group, forcing their failures to be evicted and
	// folded into `unrecoverable` BEFORE the after-scrape. Only then is the denominator
	// complete over the loss window.
	top.ClearLoss("wan")
	time.Sleep(500 * time.Millisecond) // let the qdisc change apply and in-flight lossy frames flush
	edgeAfterData := top.p3Drain(t, edgeMidData)
	drainDataFrames := edgeAfterData - edgeMidData
	if drainDataFrames < p3DrainMinDataFrames {
		t.Fatalf("p3 loss=%.0f%%: trailing drain emitted only %.0f DATA frames (< %d needed to advance the decoder high-water past the %d-group retain window and account the loss window's tail failures) — denominator incomplete",
			lossFrac*100, drainDataFrames, p3DrainMinDataFrames, p3RetainGroups)
	}

	// Window end (after the drain): the receive-side recovery outcome, now with a COMPLETE
	// denominator for the loss window.
	concAfter := fetchMetricsInNetns(t, top.pid, p3MetricsURL)
	recovered := deltaValue(t, concBefore, concAfter, metrics.MetricFECRecovered)
	unrecoverable := deltaValue(t, concBefore, concAfter, metrics.MetricFECUnrecoverable)

	t.Logf("p3 loss=%.0f%%: goodput=%.2f Mbit/s | edge loss-window data=%.0f parity=%.0f | drain data=%.0f | conc recovered=%.0f unrecoverable=%.0f",
		lossFrac*100, goodput, dataFrames, parityFrames, drainDataFrames, recovered, unrecoverable)

	// Data-plane survival (no reset / without retransmit): FEC kept the flow healthy despite
	// the loss.
	if goodput <= 0 {
		t.Fatalf("p3 loss=%.0f%%: upload goodput non-positive (%.2f Mbit/s) — the flow did not survive the injected loss\n--- edge ---\n%s\n--- conc ---\n%s",
			lossFrac*100, goodput, edge.log(), conc.log())
	}

	// Sample-size guard 1: enough DATA frames flowed for the ratios to be meaningful.
	if dataFrames < p3MinDataFrames {
		t.Fatalf("p3 loss=%.0f%%: only %.0f DATA frames over the window (< %d) — sample too small for a meaningful recovered fraction; raise throughput or p3LoadSecs",
			lossFrac*100, dataFrames, p3MinDataFrames)
	}

	// "Lost DATA frames" for the recovered fraction = those the decoder had cardinality for
	// (a parity shard of the group survived, so M was learned) and either RECONSTRUCTED and
	// delivered in time (recovered) or evicted still-incomplete (unrecoverable). The trailing
	// drain above evicted+accounted every loss-window group, so this sum captures the loss
	// window's repair failures COMPLETELY — it is NOT the eviction-lagged partial count that a
	// no-trailing-traffic scrape would yield. The only frames still excluded from BOTH terms
	// are the small, bounded residuals the p3LostAccountingFloor guard budgets for: whole-group
	// losses (all M parity also dropped, ~loss^M) and any late-reconstructed frame (rebuilt
	// after the resequencer released its gap — kept near zero by the 100ms deadline).
	accountedLost := recovered + unrecoverable

	// Sample-size guard 2: a real lost-frame population under the denominator.
	if accountedLost < p3MinLostSample {
		t.Fatalf("p3 loss=%.0f%%: only %.0f FEC-accounted lost DATA frames (recovered %.0f + unrecoverable %.0f, < %d) — denominator too small to assert a 0.95 fraction",
			lossFrac*100, accountedLost, recovered, unrecoverable, p3MinLostSample)
	}

	// Teeth: the accounted loss must be a real fraction of what the injected rate predicts, so
	// a high recovered fraction cannot be an artefact of the decoder not seeing the loss.
	expectedLost := lossFrac * dataFrames
	if accountedLost < p3LostAccountingFloor*expectedLost {
		t.Fatalf("p3 loss=%.0f%%: FEC accounted only %.0f lost DATA frames, < %.2f * %.0f expected (= lossRate * dataFrames) — the injected loss did not reach the decoder as accounted erasures; the recovered fraction would be vacuous",
			lossFrac*100, accountedLost, p3LostAccountingFloor, expectedLost)
	}

	// Assertion 1 — recovered fraction of lost DATA frames >= P3MinRecoveredFraction.
	recoveredFrac := recovered / accountedLost
	t.Logf("p3 loss=%.0f%%: recovered fraction = %.4f (= %.0f recovered / (%.0f recovered + %.0f unrecoverable)); want >= %.2f",
		lossFrac*100, recoveredFrac, recovered, recovered, unrecoverable, P3MinRecoveredFraction)
	if recoveredFrac < P3MinRecoveredFraction {
		t.Errorf("p3 loss=%.0f%%: FEC recovered only %.2f%% of lost DATA frames, want >= %.2f%% (K=%d,M=%d)",
			lossFrac*100, recoveredFrac*100, P3MinRecoveredFraction*100, p3DataShards, p3ParityShards)
	}

	// Assertion 2 — overhead <= P3MaxOverheadFactor x configured ratio (M/K). UNIT: FRAMES.
	// Both MetricFECRepair and MetricFECData increment once per frame that reached the socket,
	// so their ratio is dimensionless (parity frames per data frame) and tends to M/K once
	// groups fill. The 2x factor absorbs deadline-flushed partial groups (fewer than K data
	// frames but still M parity). (A byte ratio would NOT equal M/K: parity shards are
	// max-shard-sized while DATA frames vary, so frames are the correct unit for the M/K
	// comparison.)
	configuredRatio := float64(p3ParityShards) / float64(p3DataShards)
	overhead := parityFrames / dataFrames
	maxOverhead := P3MaxOverheadFactor * configuredRatio
	t.Logf("p3 loss=%.0f%%: FEC overhead = %.4f (= %.0f parity / %.0f data frames); configured ratio M/K = %.3f; want <= %.3f (%.1fx)",
		lossFrac*100, overhead, parityFrames, dataFrames, configuredRatio, maxOverhead, P3MaxOverheadFactor)
	if overhead > maxOverhead {
		t.Errorf("p3 loss=%.0f%%: FEC frame overhead %.3f > %.3f (= %.1f * M/K) — parity spend exceeds the bounded budget (groups likely flushing partially at the fixture's low frame rate; raise the deadline or the throughput)",
			lossFrac*100, overhead, maxOverhead, P3MaxOverheadFactor)
	}
}

// deltaValue returns after-before for an UNLABELED (connection-scoped) series — the shape of
// the FEC counters — failing if either scrape lacked it (a missing series is a wiring defect,
// not a zero).
func deltaValue(t *testing.T, before, after metrics.Exposition, name string) float64 {
	t.Helper()
	b, ok := before.Value(name)
	if !ok {
		t.Fatalf("first scrape missing unlabeled series %s", name)
	}
	a, ok := after.Value(name)
	if !ok {
		t.Fatalf("second scrape missing unlabeled series %s", name)
	}
	return a - b
}

// p3Drain drives a trailing LOSSLESS upload until the edge FEC DATA-frame counter has
// advanced by at least p3DrainMinDataFrames beyond edgeStartData, returning the final
// counter value. That advance forces the decoder high-water past the whole p3RetainGroups
// retain window, so every loss-window tail group is evicted and its repair failure folded
// into `unrecoverable` before the caller's after-scrape. The drain is lossless (loss was
// cleared by the caller), so it contributes zero recovered/unrecoverable of its own. It
// loops in chunks and fails loud if a slow fixture cannot produce the frames within
// p3DrainMaxWall, rather than under-draining silently.
func (top *Topology) p3Drain(t *testing.T, edgeStartData float64) float64 {
	t.Helper()
	deadline := time.Now().Add(p3DrainMaxWall)
	for {
		cur := top.p3EdgeDataFrames(t)
		if cur-edgeStartData >= p3DrainMinDataFrames {
			return cur
		}
		if time.Now().After(deadline) {
			t.Fatalf("p3 drain: only %.0f lossless DATA frames after %s (need %d to advance the decoder high-water past the %d-group retain window) — fixture too slow; raise throughput or p3DrainMaxWall",
				cur-edgeStartData, p3DrainMaxWall, p3DrainMinDataFrames, p3RetainGroups)
		}
		top.fecIperf3RecvMbps(t, concInner, p3DrainChunkSecs)
	}
}

// p3EdgeDataFrames scrapes the edge /metrics and returns the cumulative FEC DATA-frame
// counter (wanbond_fec_data_packets_total), used to size the trailing drain.
func (top *Topology) p3EdgeDataFrames(t *testing.T) float64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	exp := fetchMetrics(t, ctx, p3MetricsURL)
	v, ok := exp.Value(metrics.MetricFECData)
	if !ok {
		t.Fatalf("edge scrape missing %s", metrics.MetricFECData)
	}
	return v
}

// setupP3Tunnel brings up the edge+concentrator tunnel over the single p3Path with the FEC
// plane ENABLED (K=p3DataShards, M=p3ParityShards, the p3DeadlineNanos group-close deadline)
// and the /metrics endpoint on both ends. It mirrors fecBringUpTunnel's addressing/bring-up
// but adds the [fec] and [metrics] blocks P3 needs on BOTH roles (the edge encodes the
// upload; the concentrator decodes it — both need the matching [fec] block).
func setupP3Tunnel(t *testing.T, top *Topology, bin string) (edge, conc *proc) {
	t.Helper()

	edgePriv, edgePub := genKey(t)
	concPriv, concPub := genKey(t)
	psk := randKey(t)

	fecBlock := fmt.Sprintf("[fec]\nenabled = true\ndata_shards = %d\nparity_shards = %d\ndeadline = %d\n\n",
		p3DataShards, p3ParityShards, p3DeadlineNanos)
	metricsBlock := fmt.Sprintf("[metrics]\nlisten = %q\n\n", p3MetricsListen)

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
`, psk, p3Path.name, p3Path.edgeIP, metricsBlock, fecBlock, edgePriv, concPub, p3Path.concIP, listenPort, concInner))

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
`, psk, p3Path.name, p3Path.concIP, metricsBlock, fecBlock, concPriv, listenPort, edgePub, edgeInner))

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

// p3ChecklistMarker is the idempotency sentinel: appendP3Checklist appends its section only
// if this heading is not already present in docs/manual-checklist.md.
const p3ChecklistMarker = "## P3 — scripted real-setup run"

// appendP3Checklist appends the P3 scripted manual-verification section to
// docs/manual-checklist.md, idempotently (a second run is a no-op once the marker is
// present). It mirrors the fec-baseline doc-writing pattern: the privileged e2e test owns
// the doc mutation, run on hardware.
func appendP3Checklist(t *testing.T) {
	t.Helper()
	path := p3ChecklistPath(t)
	existing, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read manual checklist %s: %v", path, err)
	}
	if strings.Contains(string(existing), p3ChecklistMarker) {
		return // already appended
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open manual checklist %s: %v", path, err)
	}
	defer f.Close()
	if _, err := f.WriteString(p3ChecklistSection()); err != nil {
		t.Fatalf("append P3 checklist to %s: %v", path, err)
	}
	t.Logf("appended P3 scripted checklist to %s", path)
}

// p3ChecklistPath resolves docs/manual-checklist.md relative to the module root, found by
// walking up from the test's working directory to the go.mod.
func p3ChecklistPath(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, "docs", "manual-checklist.md")
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("go.mod not found above working directory %s", dir)
		}
		dir = parent
	}
}

// p3ChecklistSection renders the P3 scripted real-setup section. The parity ratio, deadline,
// and thresholds are interpolated from the same constants the automated test asserts, so the
// manual and automated criteria never drift.
func p3ChecklistSection() string {
	var b strings.Builder
	ratio := float64(p3ParityShards) / float64(p3DataShards)
	b.WriteString("\n## P3 — scripted real-setup run (single-path induced loss, FEC)\n\n")
	b.WriteString("Scripted counterpart of the P3 phase for the real deployment. Enable the FEC plane\n")
	b.WriteString("on BOTH ends' configs (edge encodes, concentrator decodes):\n\n")
	b.WriteString("```toml\n")
	fmt.Fprintf(&b, "[fec]\nenabled = true\ndata_shards = %d   # K\nparity_shards = %d  # M\ndeadline = \"%dms\"\n", p3DataShards, p3ParityShards, p3DeadlineNanos/1_000_000)
	b.WriteString("```\n\n")
	fmt.Fprintf(&b, "K=%d,M=%d is a 37.5%% per-group erasure tolerance; because uniform loss is binomial the\n", p3DataShards, p3ParityShards)
	fmt.Fprintf(&b, "recovered FRACTION across many groups is ~0.98 at 15%% and ~1.00 at 5%% loss — clearing\n")
	fmt.Fprintf(&b, "P3MinRecoveredFraction=%.2f with margin. Configured parity ratio M/K = %.2f.\n\n", P3MinRecoveredFraction, ratio)
	b.WriteString("Inner addresses assume concentrator `10.77.0.1`, edge `10.77.0.2`; adjust to your\n")
	b.WriteString("`allowed_ips`. `FEC()` below is `curl -s http://127.0.0.1:9090/metrics | grep wanbond_fec`.\n")
	b.WriteString("Record date, `wanbond version`, and observed numbers next to each item.\n\n")

	b.WriteString("### Setup + induced loss\n")
	b.WriteString("- [ ] Bring the FEC-enabled tunnel up (both ends); confirm ping + a short transfer pass.\n")
	b.WriteString("- [ ] On the edge uplink, induce uniform loss (lab: `tc qdisc ... netem loss 5%`, then\n")
	b.WriteString("      repeat at `15%`; real link: a lossy uplink or an inline impairment). Run each rate\n")
	b.WriteString("      as a separate measurement.\n\n")

	b.WriteString("### Per loss rate (5% AND 15%)\n")
	b.WriteString("- [ ] Read the EDGE `/metrics` `wanbond_fec_data_packets_total` and\n")
	b.WriteString("      `wanbond_fec_repair_packets_total` at the START and END of a saturating upload\n")
	b.WriteString("      (`iperf3 -c 10.77.0.1 -t 30`); take DELTAS `dData`, `dParity`.\n")
	b.WriteString("- [ ] Read the CONCENTRATOR `/metrics` `wanbond_fec_recovered_packets_total` and\n")
	b.WriteString("      `wanbond_fec_unrecoverable_packets_total` over the SAME window; deltas\n")
	b.WriteString("      `dRecovered`, `dUnrecoverable`. IMPORTANT: `unrecoverable` is accounted only when a\n")
	fmt.Fprintf(&b, "      failed group is EVICTED (>%d groups behind the decoder high-water), so BEFORE reading\n", p3RetainGroups)
	fmt.Fprintf(&b, "      `dUnrecoverable` clear the induced loss and drive a trailing LOSSLESS transfer of\n")
	fmt.Fprintf(&b, "      > %d DATA frames (> %d groups) so the loss window's tail failures are counted;\n", p3DrainMinDataFrames, p3RetainGroups+p3DrainMarginGroups)
	b.WriteString("      otherwise the recovered fraction reads structurally high.\n")
	fmt.Fprintf(&b, "- [ ] Recovered fraction `dRecovered / (dRecovered + dUnrecoverable) >= %.2f`\n", P3MinRecoveredFraction)
	b.WriteString("      (`P3MinRecoveredFraction`). Confirm the sample is large (`dData` in the thousands,\n")
	b.WriteString("      the lost count in the hundreds) so the fraction is not noise.\n")
	fmt.Fprintf(&b, "- [ ] Overhead `dParity / dData <= %.2f` (= `P3MaxOverheadFactor` x M/K = %.1f x %.2f).\n", P3MaxOverheadFactor*ratio, P3MaxOverheadFactor, ratio)
	b.WriteString("- [ ] The upload completed with a positive receiver goodput and NO connection reset —\n")
	b.WriteString("      FEC masked the loss without retransmit (compare against the pre-FEC collapse in\n")
	b.WriteString("      `docs/fec-baseline.md` at the same loss).\n")
	return b.String()
}
