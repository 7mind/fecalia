package adaptivefec

import (
	"math/rand"
	"testing"
)

// The simulation harness drives the pure controller with synthetic loss traces
// over a virtual clock and asserts the four acceptance properties. Each test
// documents the mutation that makes it fail (mutation-verification is recorded
// in the task report), so no assertion is vacuous.
//
// Redundancy-map reference values under DefaultConfig (K=10, safety=1.5),
// M = ceil(K*e/(1-e)), e = safety*loss:
//
//	loss 0.00 -> M=0   loss 0.10 -> M=2   loss 0.15 -> M=3
//	loss 0.02 -> M=1   loss 0.30 -> M=9   loss 0.284 -> M=8 (spike, smoothed)

// Acceptance: at 0% loss the steady-state parity overhead is ~0 — no bandwidth
// wasted on redundancy when the path is clean.
func TestCleanTraceKeepsZeroParity(t *testing.T) {
	ctrl, clk := newDefaultController(t)
	ms := runTrace(ctrl, clk, constTrace(100, 0.0))

	for i, m := range ms {
		if m != 0 {
			t.Fatalf("clean trace: M=%d at sample %d, want 0 (no standing redundancy when clean)", m, i)
		}
	}
	if ov := ctrl.Overhead(); ov != 0 {
		t.Fatalf("clean trace: overhead=%g, want 0", ov)
	}
	// Non-vacuous: if the redundancy map yielded a floor above 0, or any standing
	// redundancy existed, M would be > 0 here.
}

// Acceptance: the parity ratio RISES with sustained loss and CONVERGES to a
// steady value. A 30% loss step must drive M up (rate-limited) to map(0.30)=9
// and then hold.
func TestSustainedLossRisesAndConverges(t *testing.T) {
	ctrl, clk := newDefaultController(t)
	const wantSteady = 9 // map(0.30)
	ms := runTrace(ctrl, clk, constTrace(80, 0.30))

	if ms[0] <= 0 {
		t.Fatalf("sustained loss: M did not rise on first sample, got %d", ms[0])
	}
	// Monotonic non-decreasing during the climb (rate-limited slew, no dip).
	for i := 1; i < len(ms); i++ {
		if ms[i] < ms[i-1] {
			t.Fatalf("sustained loss: M decreased %d->%d at sample %d during a rising step", ms[i-1], ms[i], i)
		}
	}
	// Converged: steady value reached, no overshoot past it, and stable at the end.
	if got := ms[len(ms)-1]; got != wantSteady {
		t.Fatalf("sustained loss: converged M=%d, want %d", got, wantSteady)
	}
	if peak := maxOf(ms); peak != wantSteady {
		t.Fatalf("sustained loss: peak M=%d overshoots steady value %d", peak, wantSteady)
	}
	for i := len(ms) - 20; i < len(ms); i++ {
		if ms[i] != wantSteady {
			t.Fatalf("sustained loss: not steady in tail, M=%d at sample %d", ms[i], i)
		}
	}
	// Non-vacuous: remove the rate limit and the climb is a single jump (still
	// converges); remove the redundancy map's monotone growth and it never reaches 9.
}

// Acceptance: the parity ratio FALLS when loss clears — but only after the
// dwell, so a brief lull does not prematurely strip protection. A loss step
// followed by a long clean tail must HOLD M through the dwell window, then decay
// back to 0.
func TestLossClearsFallsAfterDwell(t *testing.T) {
	ctrl, clk := newDefaultController(t)
	const lossSamples = 40 // 4.0s at 0.15 -> M rises to 3
	const cleanSamples = 140
	trace := append(constTrace(lossSamples, 0.15), constTrace(cleanSamples, 0.0)...)
	ms := runTrace(ctrl, clk, trace)

	peak := maxOf(ms)
	if peak != 3 { // map(0.15)
		t.Fatalf("loss-clears: peak M=%d during loss, want 3", peak)
	}

	// DWELL HOLD: for the first 3.0s (30 samples at 100ms) after loss drops, M must
	// stay at its peak. The smoothed loss takes ~0.6s to decay below LowerThreshold,
	// then Dwell=3s must elapse before the FIRST decrease (~3.5s total), so a 3.0s
	// hold window is comfortably inside that and outside the no-dwell behavior.
	const holdSamples = 30
	for i := lossSamples; i < lossSamples+holdSamples; i++ {
		if ms[i] != peak {
			t.Fatalf("loss-clears: M fell to %d at sample %d (%.1fs after drop) — inside the dwell hold window", ms[i], i, float64(i-lossSamples)*0.1)
		}
	}

	// EVENTUAL FALL: after the dwell + slew, M returns to 0 (overhead ~0 once clean).
	if got := ms[len(ms)-1]; got != 0 {
		t.Fatalf("loss-clears: M=%d at end of clean tail, want 0", got)
	}
	// Non-vacuous: remove the dwell and M starts falling ~0.6s after the drop, well
	// inside the 3.0s hold window, failing the DWELL HOLD assertion.
}

// Acceptance: under a loss signal OSCILLATING around a decision threshold the
// change rate is bounded by the hysteresis — NO FLAP. A square wave straddling
// the map's 2<->3 boundary (raw 0.06/0.16, smoothed ~0.10..0.12) must produce
// only a handful of M changes, not one per half-cycle.
func TestOscillatingLossDoesNotFlap(t *testing.T) {
	ctrl, clk := newDefaultController(t)
	const samples = 200 // 20s of oscillation
	ms := runTrace(ctrl, clk, squareTrace(samples, 0.16, 0.06))

	changes := countChanges(ms)
	const maxChanges = 6
	if changes > maxChanges {
		t.Fatalf("oscillating loss: %d M changes over %d samples, want <= %d (flap)", changes, samples, maxChanges)
	}
	// The bound must not be met by M simply pinning at 0: it should have risen.
	if maxOf(ms) == 0 {
		t.Fatalf("oscillating loss: M never rose; the no-flap bound is vacuous")
	}
	// Non-vacuous: replacing the hysteresis bands with an unconditional map(smoothed)
	// makes M toggle at every rate-limit boundary (~40 changes), failing this bound.
}

// Hysteresis deadband: a loss signal wandering WITHIN the deadband must not move
// an already-established M. Establish M at 3 under 0.15 loss, then oscillate the
// raw loss inside the band (smoothed ~0.03..0.04) and assert M is pinned.
func TestDeadbandHoldsEstablishedParity(t *testing.T) {
	ctrl, clk := newDefaultController(t)
	const establish = 30
	const oscillate = 140
	trace := append(constTrace(establish, 0.15), squareTrace(oscillate, 0.06, 0.01)...)
	ms := runTrace(ctrl, clk, trace)

	established := ms[establish-1]
	if established != 3 {
		t.Fatalf("deadband: M=%d after establish phase, want 3", established)
	}
	for i := establish; i < len(ms); i++ {
		if ms[i] != established {
			t.Fatalf("deadband: M moved to %d at sample %d while smoothed loss stayed in the deadband", ms[i], i)
		}
	}
	// Non-vacuous: making the deadband branch return map(smoothed) instead of holding
	// drives M down toward 1 during the oscillation, failing the pin assertion.
}

// Acceptance foundation (anti-thrash): under NOISY telemetry around a stable
// mean, M tracks the mean rather than the noise — few changes and no chase to
// the noise peaks.
func TestNoisyTelemetryTracksMeanNotNoise(t *testing.T) {
	ctrl, clk := newDefaultController(t)
	const samples = 200
	// Deterministic pseudo-random noise: mean 0.10, uniform +/-0.08 -> [0.02,0.18].
	rng := rand.New(rand.NewSource(42))
	noisy := make([]float64, samples)
	for i := range noisy {
		noisy[i] = 0.10 + (rng.Float64()*2-1)*0.08
	}
	ms := runTrace(ctrl, clk, noisy)

	// Tracks the MEAN: map(0.10)=2, allow the raise-region ratchet up to 3; must not
	// chase the noise peak (raw 0.18 -> map 4).
	if peak := maxOf(ms); peak > 3 {
		t.Fatalf("noisy: peak M=%d chased the noise (map of the noise peak is 4)", peak)
	}
	// Few changes: the EWMA damps the per-sample jitter so M settles.
	changes := countChanges(ms)
	const maxChanges = 6
	if changes > maxChanges {
		t.Fatalf("noisy: %d M changes, want <= %d (M chasing noise)", changes, maxChanges)
	}
	if maxOf(ms) < 2 {
		t.Fatalf("noisy: M=%d never reached the mean's mapped parity (2); assertion vacuous", maxOf(ms))
	}
	// Non-vacuous: remove the EWMA (feed raw loss) and M is driven by each sample —
	// it reaches the noise-peak parity (4) and changes far more than 6.
}

// Rate limit: a single loss SPIKE must not swing redundancy wildly. A one-sample
// spike to 90% loss over a quiet baseline must move M by at most a bounded amount
// (the slew cap), not jump to the spike's mapped parity.
func TestSpikeIsRateLimited(t *testing.T) {
	ctrl, clk := newDefaultController(t)
	trace := append(constTrace(10, 0.02), 0.9) // quiet, then one spike sample
	trace = append(trace, constTrace(40, 0.02)...)
	ms := runTrace(ctrl, clk, trace)

	// The smoothed loss after the spike is ~0.284 (map 8); without the slew cap M
	// would jump there. The rate limit bounds the move to MaxStep (2) per interval,
	// and smoothing decays the spike before the next interval, so M peaks low.
	if peak := maxOf(ms); peak > 3 {
		t.Fatalf("spike: peak M=%d, want <= 3 (a single spike overshot the redundancy)", peak)
	}
	// Non-vacuous: remove the rate limit and M jumps to the smoothed spike's parity
	// (~8) on the spike sample, failing this bound.
}

// Determinism: identical traces produce identical trajectories (a precondition
// for the -race-clean, reproducible simulation harness).
func TestDeterministicTrajectories(t *testing.T) {
	trace := append(constTrace(20, 0.12), constTrace(20, 0.0)...)

	c1, k1 := newDefaultController(t)
	c2, k2 := newDefaultController(t)
	a := runTrace(c1, k1, trace)
	b := runTrace(c2, k2, trace)

	if len(a) != len(b) {
		t.Fatalf("determinism: length mismatch %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("determinism: divergence at sample %d: %d vs %d", i, a[i], b[i])
		}
	}
}
