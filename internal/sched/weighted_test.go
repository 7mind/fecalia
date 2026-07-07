package sched

import (
	"math"
	"testing"
	"time"

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
		s.Pick()
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
		idx := s.Pick()
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
		idx := s.Pick()
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

// TestWeightedHysteresisBand exercises the engage/disengage hysteresis: once
// aggregating, a drop below the disengage threshold does NOT immediately collapse —
// it must persist for CollapseDwell. This is what stops the metered path from
// dribbling on/off around a single threshold.
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

	// Let load decay below the disengage threshold by idling the clock, then pump a
	// LOW offered rate. It must remain aggregating until CollapseDwell of sustained
	// low load elapses.
	clock.advance(2 * time.Second) // load decays to ~0 (< disengage)
	// First low-load Pick starts the collapse dwell but does not collapse yet.
	s.Pick()
	if !s.aggregating {
		t.Fatal("collapsed on the first low-load sample, want the dwell to hold aggregation")
	}
	const lowDt = 20 * time.Millisecond // 50 fps, well below disengage (500 fps)
	collapsedAt := -1
	for i := 0; i < 200; i++ {
		clock.advance(lowDt)
		s.Pick()
		if !s.aggregating {
			collapsedAt = i
			break
		}
	}
	if collapsedAt < 0 {
		t.Fatal("never collapsed under sustained low load, want collapse after the dwell")
	}
	// It must have taken at least CollapseDwell of low load to collapse (hysteresis),
	// not collapsed on the first sample.
	if elapsed := time.Duration(collapsedAt+1) * lowDt; elapsed < cfg.CollapseDwell {
		t.Fatalf("collapsed after %s of low load, want >= CollapseDwell %s", elapsed, cfg.CollapseDwell)
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
	if got := s.Pick(); got != 0 {
		t.Fatalf("initial Pick = %d, want 0 (primary, collapsed)", got)
	}
	primary.down()
	clock.advance(time.Millisecond)
	if got := s.Pick(); got != 1 {
		t.Fatalf("Pick after primary DOWN = %d, want 1 (failover to backup)", got)
	}
	// With every path down, no eligible path.
	backup.down()
	if got := s.Pick(); got >= 0 {
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
		if got := s.Pick(); got != 1 {
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
		idx := s.Pick()
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
	if err := s.SetPaths([]PathHealth{only}); err != nil {
		t.Fatalf("SetPaths: %v", err)
	}
	if s.aggregating {
		t.Fatal("SetPaths did not collapse aggregation")
	}
	if got := s.Pick(); got != 0 {
		t.Fatalf("Pick after SetPaths = %d, want 0 (sole path)", got)
	}
	if err := s.SetPaths(nil); err == nil {
		t.Fatal("SetPaths(nil) accepted, want error")
	}
	if _, err := s.AddPath(nil); err == nil {
		t.Fatal("AddPath(nil) accepted, want error")
	}
}
