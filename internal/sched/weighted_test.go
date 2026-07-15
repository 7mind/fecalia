package sched

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/log"
	"github.com/7mind/wanbond/internal/telemetry"
)

// fakeQuality is a settable PathHealth+PathQuality: it drives BOTH the liveness
// verdict and the RTT/loss Estimate the weighted scheduler reads, so a single
// synthetic source feeds a path's up/down state and its weight (as *telemetry.Prober
// does in production).
type fakeQuality struct {
	state telemetry.PathState
	est   telemetry.Estimate
}

func (f *fakeQuality) State() telemetry.PathState   { return f.state }
func (f *fakeQuality) Estimate() telemetry.Estimate { return f.est }
func (f *fakeQuality) up()                          { f.state = telemetry.StateUp }
func (f *fakeQuality) down()                        { f.state = telemetry.StateDown }

// weightedCfg is a deterministic base config for the weighted-scheduler tests:
// per-path capacity 1000 frames/s, a 0.5..0.9 hysteresis band, pacing OFF (tests
// that need it enable it explicitly).
func weightedCfg() WeightedConfig {
	return WeightedConfig{
		PerPathCapacity:   1000,
		EngageFraction:    0.9,
		DisengageFraction: 0.5,
		CollapseDwell:     500 * time.Millisecond,
		LoadTau:           200 * time.Millisecond,
		Pacing:            false,
		PacingBurst:       8,
		WeightRTTFloor:    time.Millisecond,
		WeightLossFloor:   1e-3,
	}
}

func newWeighted(t testing.TB, clock telemetry.Clock, cfg WeightedConfig, sources ...*fakeQuality) *WeightedScheduler {
	t.Helper()
	health := make([]PathHealth, len(sources))
	quality := make([]PathQuality, len(sources))
	for i, s := range sources {
		health[i] = s
		quality[i] = s
	}
	ws, err := NewWeighted(health, quality, cfg, clock, discardLogger(t))
	if err != nil {
		t.Fatalf("NewWeighted: %v", err)
	}
	return ws
}

// driveUntilAggregating pumps Pick at a fixed offered rate (one Pick per dt of fake
// time) until the load-gate engages aggregation, so a test can measure the aggregated
// distribution without the ramp-up bias.
func driveUntilAggregating(t testing.TB, s *WeightedScheduler, clock *fakeClock, dt time.Duration) {
	t.Helper()
	for i := 0; i < 200000; i++ {
		s.Pick(ClassData)
		clock.advance(dt)
		if s.aggregating {
			return
		}
	}
	t.Fatal("scheduler never engaged aggregation under sustained high offered load")
}

// TestWeightedDistributesProportionalToWeights is acceptance (a): under offered load
// exceeding one path's capacity, frames are distributed across BOTH paths in
// proportion to their RTT/loss-derived weights. Primary RTT 10ms, backup RTT 20ms,
// both zero loss -> weight ratio 2:1 -> ~2/3 primary, ~1/3 backup. Pacing is off so
// the split is the pure weighted-round-robin assignment.
func TestWeightedDistributesProportionalToWeights(t *testing.T) {
	clock := newFakeClock()
	primary := &fakeQuality{state: telemetry.StateUp, est: telemetry.Estimate{RTT: 10 * time.Millisecond}}
	backup := &fakeQuality{state: telemetry.StateUp, est: telemetry.Estimate{RTT: 20 * time.Millisecond}}
	s := newWeighted(t, clock, weightedCfg(), primary, backup)

	const dt = 100 * time.Microsecond // 10 000 fps offered >> 1000 fps capacity
	driveUntilAggregating(t, s, clock, dt)

	var count [2]int
	const samples = 60000
	for i := 0; i < samples; i++ {
		idx := s.Pick(ClassData)
		if idx >= 0 {
			count[idx]++
		}
		clock.advance(dt)
	}
	total := count[0] + count[1]
	if total == 0 {
		t.Fatal("no frames scheduled")
	}
	if count[1] == 0 {
		t.Fatal("backup carried NO traffic under aggregation, want proportional share")
	}
	primaryFrac := float64(count[0]) / float64(total)
	// Target 0.667; the smooth weighted round robin holds this tightly.
	if primaryFrac < 0.64 || primaryFrac > 0.69 {
		t.Fatalf("primary share = %.3f (%d/%d), want ~0.667 (weight ratio 2:1)", primaryFrac, count[0], total)
	}
}

// TestWeightedCollapsesToPrimaryAtLowLoad is acceptance (b): with offered load below
// one path's capacity, the distribution collapses to the primary and the metered
// backup stays idle (data-thrift, requirement 2).
func TestWeightedCollapsesToPrimaryAtLowLoad(t *testing.T) {
	clock := newFakeClock()
	primary := &fakeQuality{state: telemetry.StateUp, est: telemetry.Estimate{RTT: 10 * time.Millisecond}}
	backup := &fakeQuality{state: telemetry.StateUp, est: telemetry.Estimate{RTT: 10 * time.Millisecond}}
	s := newWeighted(t, clock, weightedCfg(), primary, backup)

	const dt = 10 * time.Millisecond // 100 fps offered << 900 fps engage threshold
	var count [2]int
	for i := 0; i < 5000; i++ {
		idx := s.Pick(ClassData)
		if idx >= 0 {
			count[idx]++
		}
		clock.advance(dt)
	}
	if s.aggregating {
		t.Fatal("scheduler engaged aggregation at low load, want collapsed to primary")
	}
	if count[1] != 0 {
		t.Fatalf("backup carried %d frames at low load, want 0 (5G ~idle)", count[1])
	}
	if count[0] == 0 {
		t.Fatal("primary carried no frames, want all of them")
	}
}

// TestWeightedHysteresisBand exercises the engage/disengage hysteresis under
// CONTINUOUS pumping (no large idle gaps, which are covered separately by the
// abrupt-stop test): (1) an offered rate BETWEEN the disengage and engage thresholds
// keeps aggregation engaged indefinitely — the two-threshold band holds, no dribble;
// (2) a sustained drop BELOW the disengage threshold collapses only after CollapseDwell
// of low load, not on the first low sample.
func TestWeightedHysteresisBand(t *testing.T) {
	clock := newFakeClock()
	primary := &fakeQuality{state: telemetry.StateUp, est: telemetry.Estimate{RTT: 10 * time.Millisecond}}
	backup := &fakeQuality{state: telemetry.StateUp, est: telemetry.Estimate{RTT: 10 * time.Millisecond}}
	cfg := weightedCfg()
	s := newWeighted(t, clock, cfg, primary, backup)

	driveUntilAggregating(t, s, clock, 100*time.Microsecond)
	if !s.aggregating {
		t.Fatal("setup: expected aggregating")
	}

	// Phase 1 — in-band pumping (~700 fps, between disengage 500 and engage 900). Small
	// per-Pick gaps, so no idle-gap shortcut; the band must HOLD aggregation throughout
	// and never seed the collapse dwell.
	const bandDt = 1428 * time.Microsecond // ~700 fps
	for i := 0; i < 1500; i++ {            // ~2.1s of sim time, >> CollapseDwell
		s.Pick(ClassData)
		clock.advance(bandDt)
		if !s.aggregating {
			t.Fatalf("collapsed at in-band load (~700 fps, step %d) — the hysteresis band must hold aggregation", i)
		}
	}
	if !s.belowSince.IsZero() {
		t.Fatal("collapse dwell seeded while load was in-band (above disengage), want it clear")
	}

	// Phase 2 — sustained below-band pumping (~50 fps, below disengage), small gaps. It
	// must collapse, but only after >= CollapseDwell of sustained low load, and NOT on
	// the first below-band sample.
	const lowDt = 20 * time.Millisecond // 50 fps << 500 fps disengage; 20ms << 500ms dwell
	startLow := clock.now
	// belowSince is backdated to when load actually dropped below disengage, so the
	// dwell is measured from THAT instant (captured at seeding), not from the clock time
	// of the sample that noticed it.
	belowSinceSeed := time.Time{}
	collapsedAt := time.Time{}
	for i := 0; i < 400; i++ {
		s.Pick(ClassData)
		if belowSinceSeed.IsZero() && !s.belowSince.IsZero() {
			belowSinceSeed = s.belowSince
		}
		if !s.aggregating {
			collapsedAt = clock.now
			break
		}
		clock.advance(lowDt)
	}
	if collapsedAt.IsZero() {
		t.Fatal("never collapsed under sustained low load, want collapse after the dwell")
	}
	if belowSinceSeed.IsZero() {
		t.Fatal("collapse dwell never seeded, cannot have honored the dwell")
	}
	if collapsedAt.Equal(startLow) {
		t.Fatal("collapsed on the very first below-band sample, want the dwell to hold aggregation")
	}
	// The dwell must have been honored: at least CollapseDwell elapsed from when load
	// dropped below disengage (belowSince) to the collapse.
	if held := collapsedAt.Sub(belowSinceSeed); held < cfg.CollapseDwell {
		t.Fatalf("collapsed %s after load dropped below disengage, want >= CollapseDwell %s", held, cfg.CollapseDwell)
	}
}

// TestWeightedCollapsesAfterOverloadIdle is the criticism-#1 regression: an overload
// that stops ABRUPTLY (load still above disengage on the final frame, so the collapse
// dwell was never seeded) followed by a long idle span must NOT keep the gate engaged.
// Idle time is the strongest evidence of low load, so the FIRST frame of the next
// low-rate burst must already be collapsed to primary-only — the metered backup must
// carry ZERO frames. Without the idle-aware fix the first ~CollapseDwell of the burst
// dribbles onto the backup (data-thrift leak, requirement 2).
func TestWeightedCollapsesAfterOverloadIdle(t *testing.T) {
	clock := newFakeClock()
	// 2:1 weights so any striping is unmistakable on the backup.
	primary := &fakeQuality{state: telemetry.StateUp, est: telemetry.Estimate{RTT: 10 * time.Millisecond}}
	backup := &fakeQuality{state: telemetry.StateUp, est: telemetry.Estimate{RTT: 20 * time.Millisecond}}
	s := newWeighted(t, clock, weightedCfg(), primary, backup)

	// Overload -> aggregating, with the load left solidly above disengage and the
	// collapse dwell UNSEEDED (the abrupt-stop precondition).
	const hiDt = 100 * time.Microsecond // 10 000 fps
	driveUntilAggregating(t, s, clock, hiDt)
	for i := 0; i < 2000; i++ { // solidify: load well above disengage
		s.Pick(ClassData)
		clock.advance(hiDt)
	}
	if !s.aggregating || !s.belowSince.IsZero() {
		t.Fatalf("setup: want aggregating with unseeded dwell, got aggregating=%v belowSince-zero=%v", s.aggregating, s.belowSince.IsZero())
	}

	// Abrupt stop: a long idle span.
	clock.advance(60 * time.Second)

	// Next low-rate burst: 40 frames at 100 fps (below engage, will not re-aggregate).
	const burstDt = 10 * time.Millisecond
	var count [2]int
	for i := 0; i < 40; i++ {
		idx := s.Pick(ClassData)
		if idx >= 0 {
			count[idx]++
		}
		clock.advance(burstDt)
	}
	if count[1] != 0 {
		t.Fatalf("backup carried %d of 40 frames in the post-idle low burst, want 0 (idle must force collapse; data-thrift leak otherwise)", count[1])
	}
	if s.aggregating {
		t.Fatal("still aggregating after the post-idle low burst, want collapsed to primary-only")
	}
	if count[0] == 0 {
		t.Fatal("primary carried no frames, want all 40")
	}
}

// TestWeightedFailoverOnPathDown is acceptance (c): a path-down event still fails
// over correctly (P1 preserved), both when collapsed (low load) and when aggregating.
func TestWeightedFailoverOnPathDown(t *testing.T) {
	clock := newFakeClock()
	primary := &fakeQuality{state: telemetry.StateUp, est: telemetry.Estimate{RTT: 10 * time.Millisecond}}
	backup := &fakeQuality{state: telemetry.StateUp, est: telemetry.Estimate{RTT: 10 * time.Millisecond}}
	s := newWeighted(t, clock, weightedCfg(), primary, backup)

	// Collapsed (low load): all traffic on the primary, then the primary dies ->
	// egress fails over to the surviving backup.
	if got := s.Pick(ClassData); got != 0 {
		t.Fatalf("initial Pick = %d, want 0 (primary, collapsed)", got)
	}
	primary.down()
	clock.advance(time.Millisecond)
	if got := s.Pick(ClassData); got != 1 {
		t.Fatalf("Pick after primary DOWN = %d, want 1 (failover to backup)", got)
	}
	// With every path down, no eligible path.
	backup.down()
	if got := s.Pick(ClassData); got >= 0 {
		t.Fatalf("Pick with all paths down = %d, want negative", got)
	}

	// Recover both and drive to aggregation, then kill the primary: every subsequent
	// frame must ride the surviving backup.
	primary.up()
	backup.up()
	const dt = 100 * time.Microsecond
	driveUntilAggregating(t, s, clock, dt)
	primary.down()
	for i := 0; i < 2000; i++ {
		if got := s.Pick(ClassData); got != 1 {
			t.Fatalf("Pick #%d after primary DOWN mid-aggregation = %d, want 1", i, got)
		}
		clock.advance(dt)
	}
}

// TestWeightedPacingBoundsEgressAndBacklog is acceptance (d): with pacing enabled,
// per-path egress does not exceed the configured pace and no unbounded send backlog
// accumulates under sustained overload (the overflow is DROPPED, not queued).
func TestWeightedPacingBoundsEgressAndBacklog(t *testing.T) {
	clock := newFakeClock()
	primary := &fakeQuality{state: telemetry.StateUp, est: telemetry.Estimate{RTT: 10 * time.Millisecond}}
	backup := &fakeQuality{state: telemetry.StateUp, est: telemetry.Estimate{RTT: 10 * time.Millisecond}}
	cfg := weightedCfg()
	cfg.Pacing = true
	cfg.PacingBurst = 8
	s := newWeighted(t, clock, cfg, primary, backup)

	const dt = 200 * time.Microsecond // 5000 fps offered >> 1000 fps per-path pace
	driveUntilAggregating(t, s, clock, dt)

	// Measurement window of exactly T seconds at the overload offered rate.
	const steps = 5000
	T := float64(steps) * dt.Seconds() // 1.0 s
	var admit [2]int
	drops := 0
	for i := 0; i < steps; i++ {
		idx := s.Pick(ClassData)
		if idx < 0 {
			drops++
		} else {
			admit[idx]++
		}
		clock.advance(dt)
	}

	// Per-path egress rate bound: admitted <= burst + capacity*T (bucket starts at most
	// burst-full and refills at capacity).
	maxPerPath := cfg.PerPathCapacity*T + cfg.PacingBurst
	for p := 0; p < 2; p++ {
		if float64(admit[p]) > maxPerPath+1 {
			t.Fatalf("path %d admitted %d frames in %.2fs, exceeds pace bound %.0f (rate %.0f fps > cap %.0f)",
				p, admit[p], T, maxPerPath, float64(admit[p])/T, cfg.PerPathCapacity)
		}
	}
	// Non-vacuous: pacing must actually be BINDING (near capacity), else the bound is
	// trivially met by an idle scheduler.
	if float64(admit[0]) < cfg.PerPathCapacity*T*0.5 {
		t.Fatalf("path 0 admitted only %d frames, expected pacing to admit near capacity (~%.0f)", admit[0], cfg.PerPathCapacity*T)
	}
	// Backlog is bounded because the overflow is dropped, not queued: under a 5x
	// overload the scheduler must be shedding frames (returning -1).
	if drops == 0 {
		t.Fatal("no frames dropped under 5x sustained overload, want the pacer to shed the overflow (bounded backlog)")
	}
}

// TestWeightedNudgeRecomputeDoesNotPerturbDistribution is the T40 reconciliation:
// Recompute (the eager-failover nudge's call) refreshes the liveness-derived active
// set WITHOUT consuming a distribution slot — it advances no round-robin credit, no
// load meter, no pacing token — yet it STILL drives eager failover. A nudge that
// called Pick instead would steal weighted-distribution slots and skew the split.
func TestWeightedNudgeRecomputeDoesNotPerturbDistribution(t *testing.T) {
	clock := newFakeClock()
	primary := &fakeQuality{state: telemetry.StateUp, est: telemetry.Estimate{RTT: 10 * time.Millisecond}}
	backup := &fakeQuality{state: telemetry.StateUp, est: telemetry.Estimate{RTT: 20 * time.Millisecond}}
	s := newWeighted(t, clock, weightedCfg(), primary, backup)

	const dt = 100 * time.Microsecond
	driveUntilAggregating(t, s, clock, dt)

	// Snapshot the per-frame distribution state, then hammer Recompute: none of it may
	// move (Recompute is strictly the non-consuming liveness refresh).
	s.mu.Lock()
	beforeCurrent := append([]float64(nil), s.current...)
	beforeTokens := append([]float64(nil), s.tokens...)
	beforeLoad := s.loadRate
	beforeAgg := s.aggregating
	s.mu.Unlock()

	for i := 0; i < 5000; i++ {
		s.Recompute()
		clock.advance(dt)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range beforeCurrent {
		if s.current[i] != beforeCurrent[i] {
			t.Fatalf("Recompute perturbed round-robin credit[%d]: %v -> %v (nudge stole a distribution slot)", i, beforeCurrent[i], s.current[i])
		}
		if s.tokens[i] != beforeTokens[i] {
			t.Fatalf("Recompute perturbed pacing token[%d]: %v -> %v", i, beforeTokens[i], s.tokens[i])
		}
	}
	if s.loadRate != beforeLoad {
		t.Fatalf("Recompute perturbed the offered-load meter: %v -> %v (nudge counted as offered load)", beforeLoad, s.loadRate)
	}
	if s.aggregating != beforeAgg {
		t.Fatalf("Recompute flipped the aggregation gate: %v -> %v", beforeAgg, s.aggregating)
	}
}

// TestWeightedRecomputeDrivesEagerFailover proves the OTHER half of the T40
// reconciliation: Recompute still refreshes the active-primary from liveness (the
// eager-failover log the D18 nudge exists to emit), so a path dying during an egress
// lull is reflected without a Send/Pick.
func TestWeightedRecomputeDrivesEagerFailover(t *testing.T) {
	clock := newFakeClock()
	primary := &fakeQuality{state: telemetry.StateUp, est: telemetry.Estimate{RTT: 10 * time.Millisecond}}
	backup := &fakeQuality{state: telemetry.StateUp, est: telemetry.Estimate{RTT: 10 * time.Millisecond}}
	s := newWeighted(t, clock, weightedCfg(), primary, backup)

	s.Recompute()
	s.mu.Lock()
	got := s.active
	s.mu.Unlock()
	if got != 0 {
		t.Fatalf("active after Recompute = %d, want 0 (primary)", got)
	}

	// Primary dies during an egress lull; only Recompute runs (no Pick/Send).
	primary.down()
	s.Recompute()
	s.mu.Lock()
	got = s.active
	s.mu.Unlock()
	if got != 1 {
		t.Fatalf("active after primary DOWN + Recompute = %d, want 1 (eager failover, no Send)", got)
	}
}

// TestWeightedWeightFormula pins the Mathis-style formula: weight ∝ 1/(RTT·√loss).
// Higher RTT lowers weight (inverse-RTT); higher loss lowers weight (1/√loss). The
// normalized weights must reflect both, within the floors.
func TestWeightedWeightFormula(t *testing.T) {
	clock := newFakeClock()
	// Path A: RTT 10ms, no loss. Path B: RTT 10ms, 4% loss. Same RTT, so the ratio is
	// governed by the loss term: wA/wB = sqrt(lossB+floor)/sqrt(lossA+floor).
	a := &fakeQuality{state: telemetry.StateUp, est: telemetry.Estimate{RTT: 10 * time.Millisecond, Loss: 0}}
	b := &fakeQuality{state: telemetry.StateUp, est: telemetry.Estimate{RTT: 10 * time.Millisecond, Loss: 0.04}}
	cfg := weightedCfg()
	s := newWeighted(t, clock, cfg, a, b)

	s.mu.Lock()
	w := s.weightsLocked([]int{0, 1})
	s.mu.Unlock()
	if w[0] <= w[1] {
		t.Fatalf("weights = %v, want the lossless path (0) to outweigh the lossy path (1)", w)
	}
	// Expected ratio from the closed form.
	wantRatio := math.Sqrt(0.04+cfg.WeightLossFloor) / math.Sqrt(0+cfg.WeightLossFloor)
	gotRatio := w[0] / w[1]
	if math.Abs(gotRatio-wantRatio)/wantRatio > 0.01 {
		t.Fatalf("weight ratio = %.4f, want %.4f (1/sqrt(loss) proxy)", gotRatio, wantRatio)
	}
	if sum := w[0] + w[1]; math.Abs(sum-1) > 1e-9 {
		t.Fatalf("weights not normalized: sum = %.6f, want 1", sum)
	}
}

// TestWeightedUnwiredPathIsNeutral is the criticism-#2 regression: a health-only path
// (admitted via AddPath with no PathQuality, so an all-zero Estimate) must get the
// NEUTRAL weight, not the floored MAXIMUM. Before the fix the floors (RTT->1ms,
// loss->0) handed such a path ~20x the weight of a real 20ms-RTT path, letting an
// unwired path siphon the dominant share. It must instead split evenly.
func TestWeightedUnwiredPathIsNeutral(t *testing.T) {
	clock := newFakeClock()
	measured := &fakeQuality{state: telemetry.StateUp, est: telemetry.Estimate{RTT: 20 * time.Millisecond}}
	s := newWeighted(t, clock, weightedCfg(), measured)

	// Admit a HEALTH-ONLY path (fakeHealth implements State but NOT Estimate), so its
	// quality is nil — exactly the AddPath seam the criticism flags.
	if _, err := s.AddPath(admitH(&fakeHealth{s: telemetry.StateUp})); err != nil {
		t.Fatalf("AddPath: %v", err)
	}

	s.mu.Lock()
	w := s.weightsLocked([]int{0, 1})
	s.mu.Unlock()
	// Neutral => the unwired path (1) equals the single measured path (0): ~0.5 each.
	if w[1] > w[0]+1e-9 {
		t.Fatalf("unwired path weight %.4f exceeds measured path weight %.4f — it is siphoning, not neutral", w[1], w[0])
	}
	if diff := w[0] - w[1]; diff < -1e-9 || diff > 1e-9 {
		t.Fatalf("weights = %v, want the unwired path neutral (~equal to the measured path)", w)
	}
}

// TestWeightedPickSentinelsDistinct verifies Pick's two negative returns are distinct
// (criticism #3 at the scheduler seam): a healthy-but-paced-out frame yields PickPaced,
// a genuine outage yields PickNone.
func TestWeightedPickSentinelsDistinct(t *testing.T) {
	clock := newFakeClock()
	p0 := &fakeQuality{state: telemetry.StateUp, est: telemetry.Estimate{RTT: 10 * time.Millisecond}}
	p1 := &fakeQuality{state: telemetry.StateUp, est: telemetry.Estimate{RTT: 10 * time.Millisecond}}
	cfg := weightedCfg()
	cfg.Pacing = true
	cfg.PacingBurst = 4
	s := newWeighted(t, clock, cfg, p0, p1)

	const dt = 200 * time.Microsecond // 5000 fps >> pace, so buckets drain and shed
	driveUntilAggregating(t, s, clock, dt)
	sawPaced := false
	for i := 0; i < 20000; i++ {
		if s.Pick(ClassData) == PickPaced {
			sawPaced = true
			break
		}
		clock.advance(dt)
	}
	if !sawPaced {
		t.Fatal("never observed PickPaced under sustained pacing overload, want the shed sentinel")
	}

	// A genuine outage returns PickNone, NOT PickPaced.
	p0.down()
	p1.down()
	clock.advance(time.Second)
	if got := s.Pick(ClassData); got != PickNone {
		t.Fatalf("Pick with all paths down = %d, want PickNone (%d), distinct from PickPaced (%d)", got, PickNone, PickPaced)
	}
}

// TestWeightedControlFrameExemptFromPacing reproduces defect D22: with pacing enabled
// under sustained ~5x overload, a frame-type-blind pacer sheds WireGuard control frames
// (handshake/keepalive) at the same probability as bulk data (~80% at 5x), delaying
// rekey. The fix classes control frames ClassControl and EXEMPTS them from the per-path
// token buckets: under the SAME overload that sheds bulk data heavily, a control frame
// is NEVER shed (it always resolves to a healthy path index, never PickPaced).
func TestWeightedControlFrameExemptFromPacing(t *testing.T) {
	clock := newFakeClock()
	p0 := &fakeQuality{state: telemetry.StateUp, est: telemetry.Estimate{RTT: 10 * time.Millisecond}}
	p1 := &fakeQuality{state: telemetry.StateUp, est: telemetry.Estimate{RTT: 10 * time.Millisecond}}
	cfg := weightedCfg()
	cfg.Pacing = true
	cfg.PacingBurst = 4
	s := newWeighted(t, clock, cfg, p0, p1)

	// capacity=1000 fps; offering a bulk frame every dt=200us is 5000 fps == 5x overload,
	// so the per-path token buckets stay drained and bulk data is shed most of the time.
	const dt = 200 * time.Microsecond
	driveUntilAggregating(t, s, clock, dt)

	dataShed := 0
	const iterations = 20000
	for i := 0; i < iterations; i++ {
		// A control frame offered under the same overload must ALWAYS be admitted onto a
		// healthy path — never shed (PickPaced) and never a false outage (PickNone).
		if got := s.Pick(ClassControl); got == PickPaced || got == PickNone {
			t.Fatalf("control frame shed under 5x overload: Pick(ClassControl) = %d, want a healthy path index (defect D22: pacer must exempt WG control frames)", got)
		}
		if s.Pick(ClassData) == PickPaced {
			dataShed++
		}
		clock.advance(dt)
	}
	// The overload must be real: bulk data has to be shedding, otherwise the control-frame
	// assertion above passes vacuously (no pacing pressure to be exempt from).
	if dataShed == 0 {
		t.Fatalf("no bulk-data frame shed in %d picks — the fixture is not overloaded, so the control-exemption check is vacuous", iterations)
	}
}

// TestWeightedControlFrameExemptWhenCollapsed checks the exemption also holds on the
// low-load / primary-only (non-aggregating) serve path, not just under aggregation:
// with the primary's bucket forced empty, a bulk frame sheds but a control frame is
// still admitted onto the primary.
func TestWeightedControlFrameExemptWhenCollapsed(t *testing.T) {
	clock := newFakeClock()
	p0 := &fakeQuality{state: telemetry.StateUp, est: telemetry.Estimate{RTT: 10 * time.Millisecond}}
	cfg := weightedCfg()
	cfg.Pacing = true
	cfg.PacingBurst = 2
	s := newWeighted(t, clock, cfg, p0) // single path => never aggregates (serveLocked path)

	// Drain the single path's bucket with bulk frames at one instant (no refill), then a
	// bulk frame sheds while a control frame is still admitted onto the primary (index 0).
	for i := 0; i < 10; i++ {
		s.Pick(ClassData)
	}
	if got := s.Pick(ClassData); got != PickPaced {
		t.Fatalf("bulk frame after draining the bucket = %d, want PickPaced (%d) — fixture not drained", got, PickPaced)
	}
	if got := s.Pick(ClassControl); got != 0 {
		t.Fatalf("control frame on the collapsed/primary path = %d, want 0 (exempt from pacing, defect D22)", got)
	}
}

// TestWeightedConstructorValidation covers the fail-fast guards on NewWeighted.
func TestWeightedConstructorValidation(t *testing.T) {
	clock := newFakeClock()
	lg := discardLogger(t)
	up := &fakeQuality{state: telemetry.StateUp}
	good := weightedCfg()

	if _, err := NewWeighted(nil, nil, good, clock, lg); err == nil {
		t.Fatal("empty health accepted, want error")
	}
	if _, err := NewWeighted([]PathHealth{up}, nil, good, clock, lg); err == nil {
		t.Fatal("mismatched quality length accepted, want error")
	}
	if _, err := NewWeighted([]PathHealth{nil}, []PathQuality{up}, good, clock, lg); err == nil {
		t.Fatal("nil health element accepted, want error")
	}
	if _, err := NewWeighted([]PathHealth{up}, []PathQuality{up}, good, nil, lg); err == nil {
		t.Fatal("nil clock accepted, want error")
	}
	if _, err := NewWeighted([]PathHealth{up}, []PathQuality{up}, good, clock, nil); err == nil {
		t.Fatal("nil logger accepted, want error")
	}
	bad := good
	bad.DisengageFraction = good.EngageFraction // not a band
	if _, err := NewWeighted([]PathHealth{up}, []PathQuality{up}, bad, clock, lg); err == nil {
		t.Fatal("disengage>=engage accepted, want error (no hysteresis band)")
	}
	bad = good
	bad.PerPathCapacity = 0
	if _, err := NewWeighted([]PathHealth{up}, []PathQuality{up}, bad, clock, lg); err == nil {
		t.Fatal("zero capacity accepted, want error")
	}
}

// stubFEC is a test FECPolicy: it echoes a fixed redundant path set.
type stubFEC struct{ extra []int }

func (f stubFEC) RedundantPaths(_ int, _ []int) []int { return f.extra }

// TestWeightedFECHook exercises the P3+ redundancy seam: no policy -> no redundant
// paths; an installed policy is consulted. This is a documented extension point, not
// FEC itself (the single-path Pick does not fan out).
func TestWeightedFECHook(t *testing.T) {
	clock := newFakeClock()
	up := &fakeQuality{state: telemetry.StateUp}
	s := newWeighted(t, clock, weightedCfg(), up, up)

	if got := s.RedundantPaths(0); got != nil {
		t.Fatalf("RedundantPaths with no policy = %v, want nil (no FEC)", got)
	}
	s.SetFEC(stubFEC{extra: []int{1}})
	if got := s.RedundantPaths(0); len(got) != 1 || got[0] != 1 {
		t.Fatalf("RedundantPaths with policy = %v, want [1]", got)
	}
	s.SetFEC(nil)
	if got := s.RedundantPaths(0); got != nil {
		t.Fatalf("RedundantPaths after clearing policy = %v, want nil", got)
	}
}

// TestWeightedSetPathsRealignsMembership checks the DynamicScheduler contract used by
// the Bind on Open: SetPaths replaces the path/quality set, collapses aggregation, and
// the next Pick re-derives selection from the new liveness.
func TestWeightedSetPathsRealignsMembership(t *testing.T) {
	clock := newFakeClock()
	p0 := &fakeQuality{state: telemetry.StateUp, est: telemetry.Estimate{RTT: 10 * time.Millisecond}}
	p1 := &fakeQuality{state: telemetry.StateUp, est: telemetry.Estimate{RTT: 10 * time.Millisecond}}
	s := newWeighted(t, clock, weightedCfg(), p0, p1)
	driveUntilAggregating(t, s, clock, 100*time.Microsecond)

	// Replace with a single up path: aggregation collapses, quality realigns, and Pick
	// selects the new sole path.
	only := &fakeQuality{state: telemetry.StateUp, est: telemetry.Estimate{RTT: 10 * time.Millisecond}}
	if err := s.SetPaths([]PathAdmission{admitH(only)}); err != nil {
		t.Fatalf("SetPaths: %v", err)
	}
	if s.aggregating {
		t.Fatal("SetPaths did not collapse aggregation")
	}
	if got := s.Pick(ClassData); got != 0 {
		t.Fatalf("Pick after SetPaths = %d, want 0 (sole path)", got)
	}
	if err := s.SetPaths(nil); err == nil {
		t.Fatal("SetPaths(nil) accepted, want error")
	}
	if _, err := s.AddPath(admitH(nil)); err == nil {
		t.Fatal("AddPath(nil) accepted, want error")
	}
}

// capturedLogLine is one parsed structured (JSON) log record, mirroring the shape
// test/e2e/load.go's LogLine gives an e2e test — but built locally here since sched
// cannot import the e2e-tagged test package.
type capturedLogLine struct {
	Msg    string
	Fields map[string]any
}

// newCapturingLogger builds a Logger writing to an in-memory buffer, so a test can
// assert on the structured fields of a record (not just that Info was called) the
// way the e2e log capturer does for a live daemon.
func newCapturingLogger(t testing.TB) (log.Logger, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	l, err := log.New("debug", &buf)
	if err != nil {
		t.Fatalf("build logger: %v", err)
	}
	return l, &buf
}

// parseCapturedLogLines parses every JSON line buf has accumulated so far.
func parseCapturedLogLines(t testing.TB, buf *bytes.Buffer) []capturedLogLine {
	t.Helper()
	var out []capturedLogLine
	for _, line := range strings.Split(buf.String(), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var raw map[string]any
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			t.Fatalf("non-JSON captured log line %q: %v", line, err)
		}
		msg, _ := raw["msg"].(string)
		out = append(out, capturedLogLine{Msg: msg, Fields: raw})
	}
	return out
}

// aggregationChangeLines filters lines to the canonical "scheduler aggregation
// change" record, optionally further filtered by "to" when want is non-empty.
func aggregationChangeLines(lines []capturedLogLine, want string) []capturedLogLine {
	var out []capturedLogLine
	for _, l := range lines {
		if l.Msg != "scheduler aggregation change" {
			continue
		}
		if want != "" {
			if to, _ := l.Fields["to"].(string); to != want {
				continue
			}
		}
		out = append(out, l)
	}
	return out
}

// TestWeightedAggregationSnapshot is the T143 accessor half: AggregationSnapshot
// reflects the gate's current engaged/collapsed state, the smoothed offered-load
// estimate, and the two configured thresholds, without perturbing any per-frame
// distribution state (it takes s.mu but calls neither Pick nor Recompute logic).
func TestWeightedAggregationSnapshot(t *testing.T) {
	clock := newFakeClock()
	primary := &fakeQuality{state: telemetry.StateUp, est: telemetry.Estimate{RTT: 10 * time.Millisecond}}
	backup := &fakeQuality{state: telemetry.StateUp, est: telemetry.Estimate{RTT: 10 * time.Millisecond}}
	cfg := weightedCfg()
	s := newWeighted(t, clock, cfg, primary, backup)

	wantEngage := cfg.EngageFraction * cfg.PerPathCapacity
	wantDisengage := cfg.DisengageFraction * cfg.PerPathCapacity

	snap := s.AggregationSnapshot()
	if snap.Aggregating {
		t.Fatal("initial snapshot reports aggregating, want collapsed (no load offered yet)")
	}
	if snap.OfferedLoadFPS != 0 {
		t.Fatalf("initial OfferedLoadFPS = %g, want 0", snap.OfferedLoadFPS)
	}
	if snap.EngageThresholdFPS != wantEngage {
		t.Fatalf("EngageThresholdFPS = %g, want %g", snap.EngageThresholdFPS, wantEngage)
	}
	if snap.DisengageThresholdFPS != wantDisengage {
		t.Fatalf("DisengageThresholdFPS = %g, want %g", snap.DisengageThresholdFPS, wantDisengage)
	}

	const dt = 100 * time.Microsecond // >> engage threshold
	driveUntilAggregating(t, s, clock, dt)

	snap = s.AggregationSnapshot()
	if !snap.Aggregating {
		t.Fatal("snapshot reports collapsed after the gate engaged, want aggregating")
	}
	if snap.OfferedLoadFPS <= wantEngage {
		t.Fatalf("OfferedLoadFPS = %g, want > engage threshold %g once aggregating", snap.OfferedLoadFPS, wantEngage)
	}
	// Thresholds are static config projections: unchanged by the gate flip.
	if snap.EngageThresholdFPS != wantEngage || snap.DisengageThresholdFPS != wantDisengage {
		t.Fatalf("thresholds changed after engage: engage=%g disengage=%g, want %g/%g",
			snap.EngageThresholdFPS, snap.DisengageThresholdFPS, wantEngage, wantDisengage)
	}
}

// TestWeightedAggregationChangeLogFieldsAndNoDoubleLog is the T143/R155 log-extension
// half: the CANONICAL "scheduler aggregation change" record (not a new message
// string) gains "from"/"engage_threshold_fps"/"disengage_threshold_fps" alongside its
// existing "to"/"load_fps"/"reason" fields, and still fires EXACTLY ONCE per gate
// flip — sustained high load after engaging must not re-log (parity with
// setActiveLocked's one-shot-on-change semantics; a saturated Pick path must not
// log per-frame).
func TestWeightedAggregationChangeLogFieldsAndNoDoubleLog(t *testing.T) {
	clock := newFakeClock()
	lg, buf := newCapturingLogger(t)
	primary := &fakeQuality{state: telemetry.StateUp, est: telemetry.Estimate{RTT: 10 * time.Millisecond}}
	backup := &fakeQuality{state: telemetry.StateUp, est: telemetry.Estimate{RTT: 10 * time.Millisecond}}
	cfg := weightedCfg()
	health := []PathHealth{primary, backup}
	quality := []PathQuality{primary, backup}
	s, err := NewWeighted(health, quality, cfg, clock, lg)
	if err != nil {
		t.Fatalf("NewWeighted: %v", err)
	}

	wantEngage := cfg.EngageFraction * cfg.PerPathCapacity
	wantDisengage := cfg.DisengageFraction * cfg.PerPathCapacity

	// Engage: sustained high offered load, then keep pumping well past the flip to
	// prove the record is one-shot, not per-frame.
	const hiDt = 100 * time.Microsecond
	driveUntilAggregating(t, s, clock, hiDt)
	for i := 0; i < 2000; i++ {
		s.Pick(ClassData)
		clock.advance(hiDt)
	}

	engageLines := aggregationChangeLines(parseCapturedLogLines(t, buf), "aggregating")
	if len(engageLines) != 1 {
		t.Fatalf("got %d 'scheduler aggregation change' to=aggregating records after 2000+ Picks under sustained overload, want exactly 1 (no double-log)", len(engageLines))
	}
	rec := engageLines[0]
	if from, _ := rec.Fields["from"].(string); from != "collapsed" {
		t.Fatalf("from = %q, want %q", from, "collapsed")
	}
	if _, ok := rec.Fields["load_fps"]; !ok {
		t.Fatal("missing existing load_fps field — must be preserved")
	}
	if got, ok := rec.Fields["engage_threshold_fps"].(float64); !ok || got != wantEngage {
		t.Fatalf("engage_threshold_fps = %v (ok=%v), want %g", rec.Fields["engage_threshold_fps"], ok, wantEngage)
	}
	if got, ok := rec.Fields["disengage_threshold_fps"].(float64); !ok || got != wantDisengage {
		t.Fatalf("disengage_threshold_fps = %v (ok=%v), want %g", rec.Fields["disengage_threshold_fps"], ok, wantDisengage)
	}

	// Disengage: sustained low offered load past CollapseDwell.
	const lowDt = 20 * time.Millisecond
	for i := 0; i < 400; i++ {
		s.Pick(ClassData)
		if !s.aggregating {
			break
		}
		clock.advance(lowDt)
	}
	if s.aggregating {
		t.Fatal("setup: never collapsed under sustained low load")
	}

	collapseLines := aggregationChangeLines(parseCapturedLogLines(t, buf), "collapsed")
	if len(collapseLines) != 1 {
		t.Fatalf("got %d 'scheduler aggregation change' to=collapsed records, want exactly 1 (no double-log)", len(collapseLines))
	}
	rec = collapseLines[0]
	if from, _ := rec.Fields["from"].(string); from != "aggregating" {
		t.Fatalf("from = %q, want %q", from, "aggregating")
	}
	if got, ok := rec.Fields["engage_threshold_fps"].(float64); !ok || got != wantEngage {
		t.Fatalf("engage_threshold_fps = %v (ok=%v), want %g", rec.Fields["engage_threshold_fps"], ok, wantEngage)
	}
	if got, ok := rec.Fields["disengage_threshold_fps"].(float64); !ok || got != wantDisengage {
		t.Fatalf("disengage_threshold_fps = %v (ok=%v), want %g", rec.Fields["disengage_threshold_fps"], ok, wantDisengage)
	}

	// Exactly one "scheduler aggregation change" record total (one engage + one
	// collapse) across the whole run — the strongest form of the no-double-log
	// assertion.
	if all := aggregationChangeLines(parseCapturedLogLines(t, buf), ""); len(all) != 2 {
		t.Fatalf("got %d total 'scheduler aggregation change' records, want exactly 2 (1 engage + 1 collapse)", len(all))
	}

	// Idle-gap collapse (T194): the idle-gap arm of updateGateLocked (reason="idle gap") was
	// previously unasserted. A single idle span >= CollapseDwell collapses the gate on the
	// next Pick, carrying the gap plus the same from/load_fps/threshold fields the other
	// aggregation-change records carry. A FRESH scheduler+logger keeps this independent of the
	// sustained-low-load flow above.
	t.Run("idle gap collapse", func(t *testing.T) {
		clock := newFakeClock()
		lg, buf := newCapturingLogger(t)
		primary := &fakeQuality{state: telemetry.StateUp, est: telemetry.Estimate{RTT: 10 * time.Millisecond}}
		backup := &fakeQuality{state: telemetry.StateUp, est: telemetry.Estimate{RTT: 10 * time.Millisecond}}
		cfg := weightedCfg()
		health := []PathHealth{primary, backup}
		quality := []PathQuality{primary, backup}
		s, err := NewWeighted(health, quality, cfg, clock, lg)
		if err != nil {
			t.Fatalf("NewWeighted: %v", err)
		}
		wantEngage := cfg.EngageFraction * cfg.PerPathCapacity
		wantDisengage := cfg.DisengageFraction * cfg.PerPathCapacity

		driveUntilAggregating(t, s, clock, 100*time.Microsecond)
		if !s.aggregating {
			t.Fatal("setup: never engaged")
		}

		// One idle span strictly longer than the collapse dwell, then a single Pick: the gate
		// collapses immediately on the idle gap alone (provision (a) of updateGateLocked).
		clock.advance(cfg.CollapseDwell + 100*time.Millisecond)
		s.Pick(ClassData)
		if s.aggregating {
			t.Fatal("gate still engaged after an idle span >= CollapseDwell, want idle-gap collapse")
		}

		collapse := aggregationChangeLines(parseCapturedLogLines(t, buf), "collapsed")
		if len(collapse) != 1 {
			t.Fatalf("got %d to=collapsed records, want exactly 1", len(collapse))
		}
		rec := collapse[0]
		if reason, _ := rec.Fields["reason"].(string); reason != "idle gap" {
			t.Fatalf("reason = %q, want %q", reason, "idle gap")
		}
		if from, _ := rec.Fields["from"].(string); from != "aggregating" {
			t.Fatalf("from = %q, want %q", from, "aggregating")
		}
		if gap, ok := rec.Fields["gap"].(string); !ok || gap == "" {
			t.Fatalf("gap = %v (ok=%v), want a non-empty duration string", rec.Fields["gap"], ok)
		}
		if _, ok := rec.Fields["load_fps"]; !ok {
			t.Fatal("missing load_fps field")
		}
		if got, ok := rec.Fields["engage_threshold_fps"].(float64); !ok || got != wantEngage {
			t.Fatalf("engage_threshold_fps = %v (ok=%v), want %g", rec.Fields["engage_threshold_fps"], ok, wantEngage)
		}
		if got, ok := rec.Fields["disengage_threshold_fps"].(float64); !ok || got != wantDisengage {
			t.Fatalf("disengage_threshold_fps = %v (ok=%v), want %g", rec.Fields["disengage_threshold_fps"], ok, wantDisengage)
		}
	})
}

// TestWeightedSetPathsLogsAggregationCollapse is the T190 reproduction/acceptance: a
// SetPaths that replaces the path set while the gate is ENGAGED must emit the canonical
// "scheduler aggregation change" record (to=collapsed, from=aggregating, reason="paths
// replaced", plus load_fps and the two threshold fields), not flip to collapsed silently.
// On the unfixed code SetPaths set aggregating=false with no record, so the assertion for
// a to=collapsed line finds none.
func TestWeightedSetPathsLogsAggregationCollapse(t *testing.T) {
	clock := newFakeClock()
	lg, buf := newCapturingLogger(t)
	primary := &fakeQuality{state: telemetry.StateUp, est: telemetry.Estimate{RTT: 10 * time.Millisecond}}
	backup := &fakeQuality{state: telemetry.StateUp, est: telemetry.Estimate{RTT: 10 * time.Millisecond}}
	cfg := weightedCfg()
	health := []PathHealth{primary, backup}
	quality := []PathQuality{primary, backup}
	s, err := NewWeighted(health, quality, cfg, clock, lg)
	if err != nil {
		t.Fatalf("NewWeighted: %v", err)
	}
	wantEngage := cfg.EngageFraction * cfg.PerPathCapacity
	wantDisengage := cfg.DisengageFraction * cfg.PerPathCapacity

	driveUntilAggregating(t, s, clock, 100*time.Microsecond)
	if !s.aggregating {
		t.Fatal("setup: never engaged")
	}

	only := &fakeQuality{state: telemetry.StateUp, est: telemetry.Estimate{RTT: 10 * time.Millisecond}}
	if err := s.SetPaths([]PathAdmission{admitH(only)}); err != nil {
		t.Fatalf("SetPaths: %v", err)
	}
	if s.aggregating {
		t.Fatal("SetPaths did not collapse aggregation")
	}

	collapse := aggregationChangeLines(parseCapturedLogLines(t, buf), "collapsed")
	if len(collapse) != 1 {
		t.Fatalf("got %d to=collapsed records after an engaged-gate SetPaths, want exactly 1 (T190: replace must not collapse silently)", len(collapse))
	}
	rec := collapse[0]
	if from, _ := rec.Fields["from"].(string); from != "aggregating" {
		t.Fatalf("from = %q, want %q", from, "aggregating")
	}
	if reason, _ := rec.Fields["reason"].(string); reason != "paths replaced" {
		t.Fatalf("reason = %q, want %q", reason, "paths replaced")
	}
	if _, ok := rec.Fields["load_fps"]; !ok {
		t.Fatal("missing load_fps field")
	}
	if got, ok := rec.Fields["engage_threshold_fps"].(float64); !ok || got != wantEngage {
		t.Fatalf("engage_threshold_fps = %v (ok=%v), want %g", rec.Fields["engage_threshold_fps"], ok, wantEngage)
	}
	if got, ok := rec.Fields["disengage_threshold_fps"].(float64); !ok || got != wantDisengage {
		t.Fatalf("disengage_threshold_fps = %v (ok=%v), want %g", rec.Fields["disengage_threshold_fps"], ok, wantDisengage)
	}

	// A SetPaths while ALREADY collapsed is a no-op transition — it must not add a record.
	if err := s.SetPaths([]PathAdmission{admitH(only)}); err != nil {
		t.Fatalf("second SetPaths: %v", err)
	}
	if all := aggregationChangeLines(parseCapturedLogLines(t, buf), "collapsed"); len(all) != 1 {
		t.Fatalf("got %d to=collapsed records after a collapsed-gate SetPaths, want still exactly 1 (no record on a no-op transition)", len(all))
	}
}

// TestWeightedAccountProbeDeductsOneTokenWithoutShedding is the T145 unit-level contract
// for exempt-but-charged probe accounting: AccountProbe deducts EXACTLY one token per call
// from the named path's bucket, never sheds or delays (it returns nothing — the probe is
// already on the wire), and the bucket MAY go NEGATIVE (strict priority: the probe egress
// is charged even past the burst, pre-draining the bucket). It is the sched-level proof of
// the mechanism the -tags e2e TestProbeHeadroomUnderOverload exercises end-to-end.
func TestWeightedAccountProbeDeductsOneTokenWithoutShedding(t *testing.T) {
	clock := newFakeClock()
	primary := &fakeQuality{state: telemetry.StateUp, est: telemetry.Estimate{RTT: 10 * time.Millisecond}}
	cfg := weightedCfg()
	cfg.Pacing = true
	cfg.PacingBurst = 4
	s := newWeighted(t, clock, cfg, primary)

	// Fresh buckets are seeded full (burst).
	if got := s.tokens[0]; got != cfg.PacingBurst {
		t.Fatalf("fresh bucket = %g, want burst %g", got, cfg.PacingBurst)
	}

	// Charge past the burst so the bucket goes NEGATIVE: strict priority means the probe is
	// always emitted, so its token is always spent even when no headroom remains.
	const probes = 6 // > burst (4)
	for i := 0; i < probes; i++ {
		s.AccountProbe(0)
	}
	want := cfg.PacingBurst - float64(probes) // 4 - 6 = -2
	if got := s.tokens[0]; got != want {
		t.Fatalf("after %d probes bucket = %g, want %g (exactly one token per probe, may go negative)", probes, got, want)
	}
	if want >= 0 {
		t.Fatal("test misconfigured: expected the charge to drive the bucket negative (strict priority)")
	}
}

// TestWeightedAccountProbeReservesClassDataHeadroom is the behavioural T145 assertion: a
// probe charged via AccountProbe removes EXACTLY one ClassData admission slot, i.e. the
// out-of-band probe stream reserves data headroom rather than being invisible to the pacer
// (the pre-T145 gap that let DATA + probes oversubscribe a ~link-rate pace and starve
// probes into a spurious DOWN). Measured by counting how many ClassData frames a path
// admits before shedding, with and without a fixed probe charge, at a pinned clock (dt=0 so
// refill is inert and the burst is the only admission budget) — the difference must equal
// the charge.
func TestWeightedAccountProbeReservesClassDataHeadroom(t *testing.T) {
	admitsAfterCharge := func(charge int) int {
		clock := newFakeClock()
		primary := &fakeQuality{state: telemetry.StateUp, est: telemetry.Estimate{RTT: 10 * time.Millisecond}}
		cfg := weightedCfg()
		cfg.Pacing = true
		cfg.PacingBurst = 8
		s := newWeighted(t, clock, cfg, primary)

		// One Pick seeds the buckets full (haveFill) and consumes one token — identical in
		// both arms, so it cancels out of the difference. The clock is never advanced, so
		// every later refill adds 0 and the remaining burst is the whole admission budget.
		s.Pick(ClassData)
		for i := 0; i < charge; i++ {
			s.AccountProbe(0)
		}
		admits := 0
		for i := 0; i < 100; i++ {
			if s.Pick(ClassData) >= 0 {
				admits++
			}
		}
		return admits
	}

	const charge = 3
	base := admitsAfterCharge(0)
	charged := admitsAfterCharge(charge)
	if base <= charge {
		t.Fatalf("test misconfigured: baseline admitted only %d ClassData frames, need > charge=%d for a non-vacuous difference", base, charge)
	}
	if base-charged != charge {
		t.Fatalf("charging %d probes freed %d ClassData admission slots, want exactly %d (each probe reserves one DATA token, T145): base=%d charged=%d",
			charge, base-charged, charge, base, charged)
	}
}

// TestWeightedAccountProbeNoopWhenInertOrOutOfRange guards the two no-op paths: with pacing
// OFF the buckets are inert accountants (AccountProbe must not touch them), and an
// out-of-range index (a stale index from a concurrent membership change — probe accounting
// is best-effort headroom) must be silently ignored rather than panic.
func TestWeightedAccountProbeNoopWhenInertOrOutOfRange(t *testing.T) {
	clock := newFakeClock()
	primary := &fakeQuality{state: telemetry.StateUp, est: telemetry.Estimate{RTT: 10 * time.Millisecond}}

	// Pacing off: inert buckets, AccountProbe is a no-op.
	off := weightedCfg() // Pacing == false
	so := newWeighted(t, clock, off, primary)
	before := so.tokens[0]
	so.AccountProbe(0)
	if so.tokens[0] != before {
		t.Fatalf("AccountProbe mutated an inert (pacing-off) bucket: %g -> %g", before, so.tokens[0])
	}

	// Pacing on: an out-of-range index must not panic and must not touch any bucket.
	on := weightedCfg()
	on.Pacing = true
	on.PacingBurst = 4
	son := newWeighted(t, clock, on, primary)
	snap := son.tokens[0]
	son.AccountProbe(-1)
	son.AccountProbe(1)  // len(tokens) == 1, so index 1 is out of range
	son.AccountProbe(99) // far out of range
	if son.tokens[0] != snap {
		t.Fatalf("an out-of-range AccountProbe mutated the in-range bucket: %g -> %g", snap, son.tokens[0])
	}
}
