package adaptivefec

import "testing"

// residualCfg builds a residual-SLA Config with an explicit group geometry and target —
// the D96 field shape (K=8, target_residual=0.001) the derived raise gate is designed
// around. RaiseThreshold/LowerThreshold keep the DefaultConfig values; in residual mode the
// controller supersedes them with the derived, quantization-aware gates.
func residualCfg(k, ceiling int, target float64) Config {
	cfg := DefaultConfig()
	cfg.DataShards = k
	cfg.MaxParity = ceiling
	cfg.SafetyFactor = 0 // mutually exclusive with TargetResidual
	cfg.TargetResidual = target
	return cfg
}

// newResidualControllerCfg constructs a Controller from residualCfg on a fresh fake clock.
func newResidualControllerCfg(t *testing.T, k, ceiling int, target float64) (*Controller, *fakeClock) {
	t.Helper()
	clk := newFakeClock()
	ctrl, err := NewController(residualCfg(k, ceiling, target), clk)
	if err != nil {
		t.Fatalf("NewController(residual K=%d ceiling=%d target=%.4f): %v", k, ceiling, target, err)
	}
	return ctrl, clk
}

// TestResidualRaiseGateDerivedFromTarget is the D96 mechanism-1 acceptance (fix b) / E2
// oracle: in residual-SLA mode the raise gate must be DERIVED from
// TargetResidual, not pinned at the fixed 5% RaiseThreshold. With target_residual=0.001,
// K=8, MaxParity>=3 a sustained smoothed loss in [0.03,0.05] — BELOW the legacy 5% gate —
// must raise Parity() to residualTargetParity (>=1), while a clean trace keeps M=0 (zero
// overhead when clean).
//
// On the PRE-CHANGE tree the fixed RaiseThreshold=0.05 gate holds every sub-5% sustained
// loss in the deadband, so M stays pinned at 0 and this test fails — the intended oracle.
func TestResidualRaiseGateDerivedFromTarget(t *testing.T) {
	const (
		k       = 8
		ceiling = 6
		target  = 0.001
	)
	for _, loss := range []float64{0.03, 0.035, 0.04, 0.045, 0.05} {
		ctrl, clk := newResidualControllerCfg(t, k, ceiling, target)
		want := ctrl.residualTargetParity(loss)
		if want < 1 {
			t.Fatalf("setup: residualTargetParity(%.3f)=%d, want >= 1", loss, want)
		}
		m := steadyParity(ctrl, clk, loss)
		if m < 1 {
			t.Fatalf("sustained %.1f%% loss: steady M=%d, want >= 1 (raise gate must derive from target_residual, not the 5%% RaiseThreshold)", loss*100, m)
		}
		if m != want {
			t.Fatalf("sustained %.3f loss: steady M=%d, want residualTargetParity=%d", loss, m, want)
		}
	}

	// Clean trace: zero overhead when the path is clean.
	ctrl, clk := newResidualControllerCfg(t, k, ceiling, target)
	ms := runTrace(ctrl, clk, constTrace(120, 0.0))
	if peak := maxOf(ms); peak != 0 {
		t.Fatalf("clean trace: peak M=%d, want 0 (zero overhead when clean)", peak)
	}
}

// TestResidualNearGateNoFlapAndRaise is the D96 mechanism-1 / E2 near-gate sweep: it
// exercises losses AROUND the derived thresholds — the
// regime where the new quantization-aware gate/deadband actually live (D96). The derived
// raise gate is floored at two loss quanta (2/512) so estimator quantization cannot flap
// parity, and the lower gate sits below it with a non-empty deadband.
//
//   - (a) A single one-quantum loss blip (1/512) in an otherwise clean saturated window
//     leaves M=0 STEADY: the isolated lost probe smooths to well under the gate.
//   - (b) A SUSTAINED one-quantum loss (1/512) still leaves M=0: it sits below the
//     two-quantum raise floor, which is exactly what prevents the 0<->1 parity flap at the
//     dwell/slew cadence.
//   - (c) A sustained loss two quanta ABOVE the derived gate drives M to residualTargetParity.
func TestResidualNearGateNoFlapAndRaise(t *testing.T) {
	const (
		k       = 8
		ceiling = 6
		target  = 0.001
	)
	const quantum = 1.0 / 512

	// (a) one isolated lost probe (a single 1/512 sample) in a clean window.
	ctrl, clk := newResidualControllerCfg(t, k, ceiling, target)
	trace := constTrace(20, 0.0)
	trace = append(trace, quantum)
	trace = append(trace, constTrace(80, 0.0)...)
	ms := runTrace(ctrl, clk, trace)
	if peak := maxOf(ms); peak != 0 {
		t.Fatalf("one-quantum blip: peak M=%d, want 0 STEADY (the two-quantum floor must not flap parity)", peak)
	}

	// (b) a SUSTAINED single quantum of loss must not cross the two-quantum raise floor.
	ctrl2, clk2 := newResidualControllerCfg(t, k, ceiling, target)
	msSust := runTrace(ctrl2, clk2, constTrace(120, quantum))
	if peak := maxOf(msSust); peak != 0 {
		t.Fatalf("sustained one-quantum loss: peak M=%d, want 0 (below the two-quantum raise floor)", peak)
	}

	// (c) a sustained loss two quanta above the derived gate raises M to residualTargetParity.
	raise, lower := residualCfg(k, ceiling, target).effectiveThresholds()
	if !(lower < raise) {
		t.Fatalf("derived band inverted: lower=%.6f raise=%.6f", lower, raise)
	}
	aboveLoss := raise + 2*quantum
	ctrl3, clk3 := newResidualControllerCfg(t, k, ceiling, target)
	want := ctrl3.residualTargetParity(aboveLoss)
	if want < 1 {
		t.Fatalf("setup: residualTargetParity(%.5f)=%d, want >= 1", aboveLoss, want)
	}
	m := steadyParity(ctrl3, clk3, aboveLoss)
	if m != want {
		t.Fatalf("sustained loss %.5f (two quanta above derived gate %.5f): steady M=%d, want residualTargetParity=%d", aboveLoss, raise, m, want)
	}
}

// TestEffectiveThresholdsModes locks the two-mode derivation of the band gates: legacy
// SafetyFactor mode returns the configured RaiseThreshold/LowerThreshold VERBATIM (byte-for-
// byte unchanged), while residual-SLA mode derives a quantization-aware raise gate
// (crossover TargetResidual floored to two quanta) and a lower gate that preserves the
// configured deadband shape, keeping a deadband of at least one quantum.
func TestEffectiveThresholdsModes(t *testing.T) {
	const quantum = 1.0 / 512

	// Legacy mode: verbatim passthrough.
	legacy := DefaultConfig()
	if r, l := legacy.effectiveThresholds(); r != legacy.RaiseThreshold || l != legacy.LowerThreshold {
		t.Fatalf("legacy effectiveThresholds()=(%.6f,%.6f), want configured (%.6f,%.6f)", r, l, legacy.RaiseThreshold, legacy.LowerThreshold)
	}

	// Residual mode, sub-quantum target: raise gate is floored to two quanta.
	subQuantum := residualCfg(8, 6, 0.001)
	r, l := subQuantum.effectiveThresholds()
	if r != 2*quantum {
		t.Fatalf("residual raise gate at target=0.001 = %.6f, want floored to 2 quanta %.6f", r, 2*quantum)
	}
	if r-l < quantum {
		t.Fatalf("residual deadband %.6f narrower than one quantum %.6f", r-l, quantum)
	}
	if l < 0 || l >= r {
		t.Fatalf("residual lower gate %.6f out of [0,raise=%.6f)", l, r)
	}

	// Residual mode, target ABOVE the floor: raise gate is the crossover (== target).
	aboveFloor := residualCfg(8, 6, 0.02)
	r2, l2 := aboveFloor.effectiveThresholds()
	if r2 != 0.02 {
		t.Fatalf("residual raise gate at target=0.02 = %.6f, want crossover 0.02", r2)
	}
	if r2-l2 < quantum {
		t.Fatalf("residual deadband %.6f narrower than one quantum %.6f", r2-l2, quantum)
	}
}

// TestValidateRejectsSubQuantumDerivedDeadband: Config.Validate fails fast when the derived
// residual-mode band collapses below one loss quantum. A configured deadband shape so narrow
// that, scaled onto the small derived raise gate, leaves the derived Raise and Lower within
// one quantum of each other is rejected at construction rather than silently defeating the
// hysteresis. A healthy configured deadband on the same target validates.
func TestValidateRejectsSubQuantumDerivedDeadband(t *testing.T) {
	clk := newFakeClock()

	narrow := residualCfg(8, 6, 0.001)
	narrow.RaiseThreshold = 0.05
	narrow.LowerThreshold = 0.0499 // ratio ~0.998 -> derived deadband << one quantum
	if _, err := NewController(narrow, clk); err == nil {
		t.Fatalf("expected rejection of a sub-quantum derived deadband, got nil")
	}

	healthy := residualCfg(8, 6, 0.001) // DefaultConfig deadband shape (0.02/0.05)
	if _, err := NewController(healthy, clk); err != nil {
		t.Fatalf("healthy residual config rejected: %v", err)
	}
}
