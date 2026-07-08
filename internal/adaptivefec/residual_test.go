package adaptivefec

import (
	"math"
	"testing"
)

// p4ResidualLossMax mirrors the P4 adaptive-FEC e2e acceptance bound (0.5%
// post-recovery residual loss). The residual-SLA sizing mode (D26/T46) lets an
// operator name this bound directly as target_residual instead of hand-deriving a
// SafetyFactor that happens to clear it.
const p4ResidualLossMax = 0.005

// newResidualController builds a residual-SLA-mode Controller (DefaultConfig with the
// SafetyFactor path disabled and TargetResidual governing) on a fresh fake clock.
func newResidualController(t *testing.T, target float64) (*Controller, *fakeClock) {
	t.Helper()
	clk := newFakeClock()
	cfg := DefaultConfig()
	cfg.SafetyFactor = 0 // mutually exclusive with TargetResidual
	cfg.TargetResidual = target
	ctrl, err := NewController(cfg, clk)
	if err != nil {
		t.Fatalf("NewController(residual %.4f): %v", target, err)
	}
	return ctrl, clk
}

// steadyParity drives ctrl with a constant loss until it settles and returns the
// tail (steady-state) parity target.
func steadyParity(ctrl *Controller, clk *fakeClock, loss float64) int {
	ms := runTrace(ctrl, clk, constTrace(150, loss))
	return ms[len(ms)-1]
}

// TestBinomialResidualModel locks the residual model residual.go inverts: the
// expected unrecovered fraction E[max(0,D-m)]/k with D~Bin(k,p). It pins the
// boundary behaviours (no loss -> 0, total loss -> (k-m)/k) and the two
// monotonicities the derivation relies on (falling in m, rising in p).
func TestBinomialResidualModel(t *testing.T) {
	const k = 10
	// No loss: residual is 0 for any parity.
	for m := 0; m <= k; m++ {
		if r := binomialResidual(k, 0, m); r != 0 {
			t.Fatalf("binomialResidual(%d,0,%d)=%g, want 0", k, m, r)
		}
	}
	// Total loss: D=k deterministically, so residual is exactly (k-m)/k.
	for m := 0; m <= k; m++ {
		want := float64(k-m) / float64(k)
		if r := binomialResidual(k, 1, m); math.Abs(r-want) > 1e-12 {
			t.Fatalf("binomialResidual(%d,1,%d)=%g, want %g", k, m, r, want)
		}
	}
	// Falls (non-increasing) as parity m grows at a fixed loss; rises (non-decreasing)
	// as loss p grows at a fixed m.
	for _, p := range []float64{0.01, 0.05, 0.1, 0.2, 0.5} {
		prev := math.Inf(1)
		for m := 0; m <= k; m++ {
			r := binomialResidual(k, p, m)
			if r > prev+1e-12 {
				t.Fatalf("residual rose with parity at p=%g: m=%d gave %g > %g", p, m, r, prev)
			}
			prev = r
		}
	}
	for m := 0; m <= 5; m++ {
		prev := -1.0
		for _, p := range []float64{0.0, 0.02, 0.05, 0.1, 0.2, 0.4} {
			r := binomialResidual(k, p, m)
			if r < prev-1e-12 {
				t.Fatalf("residual fell with loss at m=%d: p=%g gave %g < %g", m, p, r, prev)
			}
			prev = r
		}
	}
}

// TestDefaultSafetyFactorMissesSubOneResidual REPRODUCES defect D26: the DEFAULT
// SafetyFactor (1.5) sizing mode, at 5% loss with K=10, settles at M=1 whose MODELED
// residual is ~1% — 2x the P4 0.5% bound — so the legacy default cannot meet a
// sub-1% SLA. This is the failing-first observation the target_residual fix addresses.
func TestDefaultSafetyFactorMissesSubOneResidual(t *testing.T) {
	ctrl, clk := newDefaultController(t)
	m := steadyParity(ctrl, clk, 0.05)
	if m != 1 {
		t.Fatalf("default (safety=1.5) steady M at 5%% loss = %d, want 1 (the defect)", m)
	}
	residual := binomialResidual(DefaultDataShards, 0.05, m)
	if residual <= p4ResidualLossMax {
		t.Fatalf("default residual at 5%% loss = %g, expected it to EXCEED the %g bound (defect D26)", residual, p4ResidualLossMax)
	}
	if residual < 0.008 || residual > 0.012 {
		t.Fatalf("default residual at 5%% loss = %g, want ~1%% (D26's E[max(0,D-1)]/K)", residual)
	}
}

// TestTargetResidualMeetsSubOneResidual is the FIX for D26: with target_residual set to
// the P4 bound, the controller at the same 5% loss / K=10 derives a LARGER M whose
// modeled residual is at/below 0.5%, and M does not exceed the parity ceiling. Contrast
// TestDefaultSafetyFactorMissesSubOneResidual, which shows the legacy default missing it.
func TestTargetResidualMeetsSubOneResidual(t *testing.T) {
	ctrl, clk := newResidualController(t, p4ResidualLossMax)
	m := steadyParity(ctrl, clk, 0.05)
	if m <= 0 || m > DefaultMaxParity {
		t.Fatalf("residual-mode steady M at 5%% loss = %d, want in (0,%d]", m, DefaultMaxParity)
	}
	residual := binomialResidual(DefaultDataShards, 0.05, m)
	if residual > p4ResidualLossMax {
		t.Fatalf("residual-mode residual at 5%% loss = %g (M=%d), want <= %g", residual, m, p4ResidualLossMax)
	}
	// The fix spends strictly more parity than the legacy default (which missed the bound).
	if m <= 1 {
		t.Fatalf("residual-mode M=%d did not exceed the safety-factor default's M=1; SLA cannot be met", m)
	}
}

// TestResidualDerivationMeetsSLAAcrossLossSweep is the acceptance property: for a
// representative target_residual, K, and a sweep of smoothed loss, the derived M yields
// a MODELED residual <= target_residual whenever the target is attainable within the
// ceiling, and M never exceeds the ceiling; the map is also zero-at-zero and monotone
// (the shape the hysteresis/slew loop requires).
func TestResidualDerivationMeetsSLAAcrossLossSweep(t *testing.T) {
	ctrl, _ := newResidualController(t, p4ResidualLossMax)
	if m := ctrl.residualTargetParity(0); m != 0 {
		t.Fatalf("residualTargetParity(0)=%d, want 0 (zero overhead when clean)", m)
	}
	prev := -1
	for loss := 0.0; loss <= 0.40+1e-9; loss += 0.005 {
		m := ctrl.residualTargetParity(loss)
		if m < 0 || m > DefaultMaxParity {
			t.Fatalf("residualTargetParity(%.3f)=%d out of [0,%d]", loss, m, DefaultMaxParity)
		}
		if m < prev {
			t.Fatalf("residualTargetParity not monotone: dropped to %d at loss=%.3f", m, loss)
		}
		prev = m
		// When the derivation did not saturate at the ceiling it MUST meet the SLA.
		if m < DefaultMaxParity {
			if r := binomialResidual(DefaultDataShards, loss, m); r > p4ResidualLossMax+1e-12 {
				t.Fatalf("residualTargetParity(%.3f)=%d gives residual %g > target %g", loss, m, r, p4ResidualLossMax)
			}
			// Minimality: one fewer parity would MISS the target (except M=0 at zero loss).
			if m > 0 {
				if r := binomialResidual(DefaultDataShards, loss, m-1); r <= p4ResidualLossMax {
					t.Fatalf("residualTargetParity(%.3f)=%d not minimal: M-1 already meets target (residual %g)", loss, m, r)
				}
			}
		}
	}
}

// TestResidualModeConvergesAndHolds: under sustained loss the residual-mode loop RISES
// to the derived target and HOLDS (converges, no overshoot, monotone climb) — the same
// stability the SafetyFactor mode has, confirming the new map plugs into the control law
// without introducing oscillation.
func TestResidualModeConvergesAndHolds(t *testing.T) {
	ctrl, clk := newResidualController(t, p4ResidualLossMax)
	want := ctrl.residualTargetParity(0.15)
	if want <= 0 {
		t.Fatalf("test setup: residualTargetParity(0.15)=%d, want > 0", want)
	}
	ms := runTrace(ctrl, clk, constTrace(120, 0.15))
	for i := 1; i < len(ms); i++ {
		if ms[i] < ms[i-1] {
			t.Fatalf("residual mode: M decreased %d->%d at sample %d during a rising step", ms[i-1], ms[i], i)
		}
	}
	if peak := maxOf(ms); peak != want {
		t.Fatalf("residual mode: peak M=%d overshoots the derived target %d", peak, want)
	}
	for i := len(ms) - 20; i < len(ms); i++ {
		if ms[i] != want {
			t.Fatalf("residual mode: not steady in tail, M=%d at sample %d (want %d)", ms[i], i, want)
		}
	}
}

// TestResidualModeDoesNotFlap: under a loss signal oscillating around a decision
// boundary the residual-mode loop's change rate stays bounded by the hysteresis — no
// flap — exactly as the SafetyFactor mode (TestOscillatingLossDoesNotFlap). The
// derivation only changes the loss->M map; the deadband/slew anti-thrash machinery is
// shared, so a scripted oscillation must not make M toggle every half-cycle.
func TestResidualModeDoesNotFlap(t *testing.T) {
	ctrl, clk := newResidualController(t, p4ResidualLossMax)
	const samples = 200
	ms := runTrace(ctrl, clk, squareTrace(samples, 0.16, 0.06))

	changes := countChanges(ms)
	const maxChanges = 6
	if changes > maxChanges {
		t.Fatalf("residual mode: %d M changes over %d samples, want <= %d (flap)", changes, samples, maxChanges)
	}
	if maxOf(ms) == 0 {
		t.Fatalf("residual mode: M never rose; the no-flap bound is vacuous")
	}
}
