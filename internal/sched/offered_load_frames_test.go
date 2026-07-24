package sched

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/telemetry"
)

// --- T290 step 4: the frame-accurate offered-load seam (defect D95) ------------------
//
// These tests pin Scheduler.Pick(class, frames): ONE selection decision covering
// `frames` offered WIRE frames. They cover the estimator's identity (a), the batch fold
// (b), the caller contract (c), the D95 gate case (d) and the thrift guard together with
// its FEC-expansion boundary (e1, e2).

// foldLoadRate replays observeLoadLocked's recurrence analytically over a sequence of
// per-Pick offered frame counts spaced dt apart. The FIRST sample seeds loadRate = 0 and
// then adds frames/tau (no decay — there is no previous sample); every later sample
// decays by exp(-dt/tau) first. It is the independent reference every expectation in
// this file is derived from, so no assertion carries a magic literal.
func foldLoadRate(frames []int, dt time.Duration, tau time.Duration) float64 {
	tauSec := tau.Seconds()
	rate := 0.0
	for i, n := range frames {
		if i > 0 {
			rate *= math.Exp(-dt.Seconds() / tauSec)
		}
		rate += float64(n) / tauSec
	}
	return rate
}

func relClose(got, want, relTol float64) bool {
	if want == 0 {
		return math.Abs(got) <= relTol
	}
	return math.Abs(got-want)/math.Abs(want) <= relTol
}

// TestPickSingleFramePreservesTheLoadTrajectory is step 4(a), the EQUIVALENCE guard: a
// sequence of Pick(class, 1) calls must reproduce the pre-change loadRate trajectory
// EXACTLY. The pre-change estimator added 1/tau per Pick after decaying by exp(-dt/tau);
// foldLoadRate over an all-ones sequence is that trajectory, computed independently of
// the implementation. It catches an accidental re-scaling of the addend (e.g. frames/
// (frames*tau), or a per-call normalisation) that would silently move every threshold.
func TestPickSingleFramePreservesTheLoadTrajectory(t *testing.T) {
	cfg := weightedCfg()
	clock := newFakeClock()
	primary := &fakeQuality{state: telemetry.StateUp, est: telemetry.Estimate{RTT: 10 * time.Millisecond}}
	s := newWeighted(t, clock, cfg, primary)

	const (
		picks = 500
		dt    = 2 * time.Millisecond
	)
	ones := make([]int, picks)
	for i := range ones {
		ones[i] = 1
	}
	for i := 0; i < picks; i++ {
		if i > 0 {
			clock.advance(dt)
		}
		s.Pick(ClassData, 1)
	}

	want := foldLoadRate(ones, dt, cfg.LoadTau)
	if !relClose(s.loadRate, want, 1e-12) {
		t.Fatalf("loadRate after %d single-frame Picks = %.12g, want %.12g (the single-frame trajectory must be bit-for-bit the pre-D95 one)",
			picks, s.loadRate, want)
	}
}

// TestPickBatchEqualsNSingleFramePicksAtOneInstant is step 4(b), the BATCH-EQUIVALENCE
// guard: ONE Pick(class, N) at clock instant t must leave loadRate identical to N
// sequential Pick(class, 1) calls at that SAME instant. That identity is the whole
// justification for folding N events in one decay step (decisions:K35 §3b): with dt == 0
// between the N single-frame updates, exp(0) == 1 contributes no decay and the N addends
// simply sum to N/tau.
//
// TOLERANCE: the two are equal in exact arithmetic but not bit-identical in floating
// point (N additions of 1/tau versus one addition of N/tau), so they are compared at a
// 1e-12 RELATIVE tolerance — far tighter than any behavioural threshold and far looser
// than the ~N*eps rounding difference.
func TestPickBatchEqualsNSingleFramePicksAtOneInstant(t *testing.T) {
	cfg := weightedCfg()
	const (
		warmup = 200
		dt     = 2 * time.Millisecond
		batchN = 37 // not a power of two, so a lucky binary-exact sum cannot mask an error
	)

	clockBatch, clockSingle := newFakeClock(), newFakeClock()
	batchPrimary := &fakeQuality{state: telemetry.StateUp, est: telemetry.Estimate{RTT: 10 * time.Millisecond}}
	singlePrimary := &fakeQuality{state: telemetry.StateUp, est: telemetry.Estimate{RTT: 10 * time.Millisecond}}
	sBatch := newWeighted(t, clockBatch, cfg, batchPrimary)
	sSingle := newWeighted(t, clockSingle, cfg, singlePrimary)

	// Identical warm-up so both start the divergent step from the same state.
	for i := 0; i < warmup; i++ {
		if i > 0 {
			clockBatch.advance(dt)
			clockSingle.advance(dt)
		}
		sBatch.Pick(ClassData, 1)
		sSingle.Pick(ClassData, 1)
	}
	if !relClose(sBatch.loadRate, sSingle.loadRate, 1e-15) {
		t.Fatalf("fixture error: warm-up left the two schedulers at %.12g vs %.12g", sBatch.loadRate, sSingle.loadRate)
	}

	// The divergent step, at ONE clock instant on both sides: one Pick of N frames
	// against N Picks of one frame. Neither clock advances between them.
	clockBatch.advance(dt)
	clockSingle.advance(dt)
	sBatch.Pick(ClassData, batchN)
	for i := 0; i < batchN; i++ {
		sSingle.Pick(ClassData, 1)
	}

	if !relClose(sBatch.loadRate, sSingle.loadRate, 1e-12) {
		t.Fatalf("Pick(class,%d) left loadRate %.12g, want %.12g (== %d sequential Pick(class,1) at the same instant); "+
			"folding N events in one decay step must be exactly N single-frame Picks at dt=0",
			batchN, sBatch.loadRate, sSingle.loadRate, batchN)
	}
}

// TestPickPanicsOnNonPositiveFrames is step 4(c): frames < 1 is a caller-contract
// violation — a Pick standing for no offered frame — and every Scheduler implementation
// fails fast on it rather than silently metering nothing (which is how D95 under-counted
// in the first place). Both implementations are covered: the contract must hold whichever
// scheduler the composition root wires.
func TestPickPanicsOnNonPositiveFrames(t *testing.T) {
	clock := newFakeClock()
	primary := &fakeQuality{state: telemetry.StateUp, est: telemetry.Estimate{RTT: 10 * time.Millisecond}}
	weighted := newWeighted(t, clock, weightedCfg(), primary)

	ab, err := NewActiveBackup([]PathHealth{AlwaysUp{}}, Config{FailbackAfter: time.Second}, clock, discardLogger(t))
	if err != nil {
		t.Fatalf("NewActiveBackup: %v", err)
	}

	cases := []struct {
		name   string
		frames int
	}{
		{"zero", 0},
		{"negative", -1},
	}
	for _, tc := range cases {
		for _, impl := range []struct {
			name string
			pick func(int) int
		}{
			{"weighted", func(n int) int { return weighted.Pick(ClassData, n) }},
			{"active-backup", func(n int) int { return ab.Pick(ClassData, n) }},
		} {
			t.Run(impl.name+"/"+tc.name, func(t *testing.T) {
				defer func() {
					r := recover()
					if r == nil {
						t.Fatalf("Pick(ClassData, %d) did not panic, want a caller-contract panic", tc.frames)
					}
					msg, ok := r.(string)
					if !ok || !strings.Contains(msg, "frames >= 1") {
						t.Fatalf("panic value %v does not name the caller contract (want a message mentioning \"frames >= 1\")", r)
					}
				}()
				impl.pick(tc.frames)
			})
		}
	}
}

// TestBatchedOfferEngagesOnTheWireRateNotTheBatchRate is step 4(d): the D95 case at
// scheduler level. Offers arriving in BATCHES whose wire-frame rate exceeds
// EngageFraction*PerPathCapacity while the batch rate stays below
// DisengageFraction*PerPathCapacity must ENGAGE the gate — the path is genuinely
// saturated, and the gate's denominator is a wire-frame capacity.
func TestBatchedOfferEngagesOnTheWireRateNotTheBatchRate(t *testing.T) {
	cfg := weightedCfg()
	cfg.PerPathCapacity = e2eFixtureCapacityFPS
	engage := cfg.EngageFraction * cfg.PerPathCapacity
	disengage := cfg.DisengageFraction * cfg.PerPathCapacity

	const (
		batchFrames = 8
		wireFPS     = 3200.0
	)
	batchFPS := wireFPS / batchFrames
	if wireFPS <= engage {
		t.Fatalf("fixture error: offered wire rate %g must exceed the engage threshold %g", wireFPS, engage)
	}
	if batchFPS >= disengage {
		t.Fatalf("fixture error: offered batch rate %g must stay below the disengage threshold %g", batchFPS, disengage)
	}

	clock := newFakeClock()
	primary := &fakeQuality{state: telemetry.StateUp, est: telemetry.Estimate{RTT: 10 * time.Millisecond}}
	backup := &fakeQuality{state: telemetry.StateUp, est: telemetry.Estimate{RTT: 20 * time.Millisecond}}
	s := newWeighted(t, clock, cfg, primary, backup)

	dt := time.Duration(float64(time.Second) * batchFrames / wireFPS)
	driveSpan := offeredWarmupTaus * cfg.LoadTau
	for elapsed := time.Duration(0); elapsed < driveSpan; elapsed += dt {
		s.Pick(ClassData, batchFrames)
		clock.advance(dt)
	}

	snap := s.AggregationSnapshot()
	if !snap.Aggregating {
		t.Fatalf("gate collapsed at %g offered WIRE fps (batches of %d at %g batches/s), want engaged: "+
			"smoothed offered load %.1f fps against engage %g (a batch-counting estimator would read ~%g fps)",
			wireFPS, batchFrames, batchFPS, snap.OfferedLoadFPS, engage, batchFPS)
	}
	if snap.OfferedLoadFPS < engage {
		t.Fatalf("gate engaged but the smoothed offered load reads %.1f fps, want >= engage %g (wrong unit)", snap.OfferedLoadFPS, engage)
	}
}

// --- Thrift guard and the FEC-expansion boundary (step 4e) --------------------------

const (
	// offeredWarmupTaus (k) is the EWMA warm-up window in units of LoadTau: at k=5 the
	// estimator is within ~1% of the offered rate.
	offeredWarmupTaus = 5

	// thriftDataFPS is the DATA-frame rate the tasks:T284 hardware measurement observed
	// for the 12 Mbit/s "5G-idle" thrift load on the o3 40 Mbit/s paths: 1042-1061 wire
	// fps with FEC OFF. The upper end is used so every margin below is the CONSERVATIVE
	// one (a higher demand rate is closer to the disengage threshold).
	thriftDataFPS = 1061.0

	// e2eFixtureCapacityFPS is test/e2e/p2_aggregation_test.go's p2PerPathCapacityFPS: a
	// deliberately conservative rounding of the 40 Mbit/s path's true wire frame rate.
	e2eFixtureCapacityFPS = 3000.0

	// honestWireCapacityFPS is that path's HONEST wire frame rate, the sizing rule
	// decisions:K35 §3h makes normative: bandwidth/(8 * on-wire frame bytes) =
	// 40e6/(8*1470) ~= 3401 fps at a ~1400-byte inner MTU plus outer framing. A link's
	// wire capacity is FEC-INDEPENDENT — the path carries the same frames/s however many
	// of them are parity — which is why sizing from it is the one sizing that makes both
	// the engage and the disengage direction correct at once.
	honestWireCapacityFPS = 3400.0

	// thriftCollapseDwell is the sustained-low-load dwell these tests configure. It is
	// short so the drive spans stay cheap; the gate LOGIC it exercises is unchanged.
	thriftCollapseDwell = 500 * time.Millisecond
)

// gateCfg is the weighted config for the thrift/expansion gate tests: capacity is the
// parameter under test, the hysteresis band and LoadTau are the shipped shape, pacing off.
func gateCfg(capacityFPS float64) WeightedConfig {
	cfg := weightedCfg()
	cfg.PerPathCapacity = capacityFPS
	cfg.CollapseDwell = thriftCollapseDwell
	return cfg
}

// driveOfferedRate pumps Pick(ClassData, framesPerPick) at a steady offered WIRE rate of
// wireFPS for span of fake time, and returns the final gate state. It is the shared
// generator for the thrift tests: one Pick per framesPerPick wire frames, spaced so the
// realised wire rate is exactly wireFPS.
func driveOfferedRate(s *WeightedScheduler, clock *fakeClock, wireFPS float64, framesPerPick int, span time.Duration) {
	dt := time.Duration(float64(time.Second) * float64(framesPerPick) / wireFPS)
	for elapsed := time.Duration(0); elapsed < span; elapsed += dt {
		s.Pick(ClassData, framesPerPick)
		clock.advance(dt)
	}
}

// engageGate drives a saturating offer until the gate engages, so a thrift test measures
// the COLLAPSE direction (which is where the metered-cost guarantee lives) rather than
// merely observing a gate that never opened.
func engageGate(t testing.TB, s *WeightedScheduler, clock *fakeClock, cfg WeightedConfig) {
	t.Helper()
	driveOfferedRate(s, clock, 2*cfg.PerPathCapacity, 1, offeredWarmupTaus*cfg.LoadTau)
	if !s.AggregationSnapshot().Aggregating {
		t.Fatalf("fixture error: gate did not engage under a %g fps offer against engage %g",
			2*cfg.PerPathCapacity, cfg.EngageFraction*cfg.PerPathCapacity)
	}
}

// TestWeightedThriftLoadCollapsesTheGateWithFECOff is step 4(e1): the FEC-OFF THRIFT
// GUARD. It is the assertion that now carries the 5G-idle data-thrift guarantee, because
// keeping updateGateLocked textually unchanged preserves the gate's LOGIC but NOT its
// guarantee: the estimator's output is rescaled into wire frames, so both thresholds fire
// at a different real offered load than before (decisions:K35 §3d).
//
// SCOPE — FEC OFF, AND THAT QUALIFICATION IS LOAD-BEARING. The margin proved here is
// measured with FEC DISABLED (the shipped default and the e2e aggregation fixture). With
// FEC enabled the same user demand meters (K+M)/K times as many wire frames and the
// margin is divided by that expansion factor; the boundary that results is pinned
// separately by TestFECExpansionThriftCollapseBoundary. Nothing here asserts an
// unqualified "thrift always collapses" claim.
//
// NUMBERS: tasks:T284 measured the 12 Mbit/s thrift load at 1042-1061 wire fps against
// the fixture's declared 3000 fps capacity, i.e. a disengage threshold of 1500 fps. At
// the conservative (higher) 1061 the demand may rise by a factor 1500/1061 = 1.414 — a
// (1500-1061)/1500 = 29.3% headroom below the threshold — before the gate would hold.
func TestWeightedThriftLoadCollapsesTheGateWithFECOff(t *testing.T) {
	cfg := gateCfg(e2eFixtureCapacityFPS)
	disengage := cfg.DisengageFraction * cfg.PerPathCapacity
	if thriftDataFPS >= disengage {
		t.Fatalf("fixture error: thrift rate %g must sit below the disengage threshold %g", thriftDataFPS, disengage)
	}

	clock := newFakeClock()
	primary := &fakeQuality{state: telemetry.StateUp, est: telemetry.Estimate{RTT: 10 * time.Millisecond}}
	backup := &fakeQuality{state: telemetry.StateUp, est: telemetry.Estimate{RTT: 20 * time.Millisecond}}
	s := newWeighted(t, clock, cfg, primary, backup)

	engageGate(t, s, clock, cfg)

	// FEC OFF: the offered WIRE rate IS the data rate (expansion factor 1.0).
	driveOfferedRate(s, clock, thriftDataFPS, 1, 6*cfg.CollapseDwell)

	snap := s.AggregationSnapshot()
	if snap.Aggregating {
		t.Fatalf("gate stayed ENGAGED at a %g fps FEC-OFF thrift load after %s (smoothed %.1f fps against disengage %g), "+
			"want COLLAPSED — the metered path must go idle at the 5G-idle load",
			thriftDataFPS, 6*cfg.CollapseDwell, snap.OfferedLoadFPS, disengage)
	}
	t.Logf("FEC-OFF thrift: offered %g wire fps, smoothed %.1f fps, disengage %g; margin %.1f fps (%.1f%% of the threshold, ratio %.3f)",
		thriftDataFPS, snap.OfferedLoadFPS, disengage, disengage-thriftDataFPS,
		100*(disengage-thriftDataFPS)/disengage, disengage/thriftDataFPS)
}

// TestFECExpansionThriftCollapseBoundary is step 4(e2): the FEC-EXPANSION BOUNDARY, the
// disengage-direction analogue of the §3c engage arithmetic (decisions:K35 §3h).
//
// THIS PINS DOCUMENTED BEHAVIOUR, NOT A DEFECT. Counting FEC parity as offered load
// (K35 §3c) is what puts the gate's numerator in the same WIRE-frame unit as its
// denominator — without it the gate cannot engage on a saturated FEC-enabled path at all.
// The price is paid in the collapse direction: the SAME user demand meters
// f = (K+M)/K times as many wire frames, so a thrift flow collapses the gate only while
//
//	f * thriftDataFPS < DisengageFraction * PerPathCapacity   <=>   f < fMax
//
// with fMax = DisengageFraction*PerPathCapacity/thriftDataFPS, computed ANALYTICALLY
// below from those constants and never written as a literal. ABOVE fMax THE GATE IS NOT
// LYING: a thrift flow at that expansion genuinely occupies more than DisengageFraction
// of the path's wire capacity, which is exactly the question DisengageFraction asks. The
// remedy is the K35 §3h capacity-SIZING rule (size per_path_capacity_fps from the path's
// WIRE frame rate) plus bounding the expansion factor by capping parity_shards relative
// to data_shards — parity_shards is a HARD CEILING even under adaptive FEC
// (internal/config/config.go:454-458), so fCeil = (data+parity)/data is statically
// checkable against fMax from an operator's own TOML. NOBODY SHOULD "FIX" A FAILING
// ASSERTION HERE BY CHANGING THE GATE: this test is the executable statement of §3h and
// must fail loudly if the thresholds, the units or the parity-counting rule ever change.
//
// The LAST case is the one that pins §3h's finding: at the e2e fixture's DECLARED 3000
// (a conservative rounding of the path's honest ~3400 wire fps) a static 4+2 group does
// NOT collapse, whereas against the honest capacity it does. It is the UNDER-DECLARED
// CAPACITY, not the parity-counting rule, that spends the thrift margin.
func TestFECExpansionThriftCollapseBoundary(t *testing.T) {
	cases := []struct {
		name       string
		capacity   float64
		expansion  float64 // f = (K+M)/K
		geometry   string
		wantReason string
	}{
		{"fec-off/honest-capacity", honestWireCapacityFPS, 1.0, "FEC off (K+M)/K = 1.0", "no parity: the thrift demand IS the wire rate"},
		{"4+2/honest-capacity", honestWireCapacityFPS, 1.5, "K=4 M=2", "wire-rate-sized capacity leaves room for a 1.5x expansion"},
		{"4+4/honest-capacity", honestWireCapacityFPS, 2.0, "K=4 M=4", "2.0x expansion exceeds fMax: a thrift flow genuinely occupies > DisengageFraction of the wire"},
		{"4+2/fixture-declared-capacity", e2eFixtureCapacityFPS, 1.5, "K=4 M=2", "the under-declared 3000 (vs the honest ~3400) is what spends the margin"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := gateCfg(tc.capacity)
			disengage := cfg.DisengageFraction * cfg.PerPathCapacity
			// fMax derived from the constants, never a literal.
			fMax := disengage / thriftDataFPS
			wantCollapsed := tc.expansion < fMax
			wireFPS := tc.expansion * thriftDataFPS

			clock := newFakeClock()
			primary := &fakeQuality{state: telemetry.StateUp, est: telemetry.Estimate{RTT: 10 * time.Millisecond}}
			backup := &fakeQuality{state: telemetry.StateUp, est: telemetry.Estimate{RTT: 20 * time.Millisecond}}
			s := newWeighted(t, clock, cfg, primary, backup)

			engageGate(t, s, clock, cfg)
			driveOfferedRate(s, clock, wireFPS, 1, 6*cfg.CollapseDwell)

			snap := s.AggregationSnapshot()
			gotCollapsed := !snap.Aggregating
			if gotCollapsed != wantCollapsed {
				t.Fatalf("%s at capacity %g: collapsed = %v, want %v (f = %.3f, fMax = DisengageFraction*capacity/thriftDataFPS = %.3f/%g = %.4f; "+
					"offered %.1f wire fps vs disengage %g, smoothed %.1f). %s. "+
					"This is DOCUMENTED behaviour (K35 §3h) — do not repair it by editing the gate",
					tc.geometry, tc.capacity, gotCollapsed, wantCollapsed, tc.expansion, disengage, thriftDataFPS, fMax,
					wireFPS, disengage, snap.OfferedLoadFPS, tc.wantReason)
			}
			t.Logf("%s at capacity %g: f=%.2f, fMax=%.4f, offered %.1f wire fps, smoothed %.1f, disengage %g -> collapsed=%v (%s)",
				tc.geometry, tc.capacity, tc.expansion, fMax, wireFPS, snap.OfferedLoadFPS, disengage, gotCollapsed, tc.wantReason)
		})
	}
}
