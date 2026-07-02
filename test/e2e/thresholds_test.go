//go:build e2e

package e2e

import "testing"

// TestThresholds is a pure-value sanity check on the Q1 acceptance table. It
// needs no privileges, so it also demonstrates that the e2e harness imports the
// constants (no magic literals in the phase tests). The privileged phase tests
// (P0-P5 tunnel bring-up) live alongside it under the same e2e build tag and
// require root + /dev/net/tun.
func TestThresholds(t *testing.T) {
	if P1RecoverySeconds != 3 {
		t.Errorf("P1RecoverySeconds = %d, want 3", P1RecoverySeconds)
	}
	if P2BondedMinFraction != 0.85 || P2MeteredMaxByteFraction != 0.01 {
		t.Errorf("P2 thresholds = %v/%v, want 0.85/0.01", P2BondedMinFraction, P2MeteredMaxByteFraction)
	}
	if len(P3InjectedLossRates) != 2 || P3InjectedLossRates[0] != 0.05 || P3InjectedLossRates[1] != 0.15 {
		t.Errorf("P3InjectedLossRates = %v, want [0.05 0.15]", P3InjectedLossRates)
	}
	if P3MinRecoveredFraction != 0.95 || P3MaxOverheadFactor != 2.0 {
		t.Errorf("P3 thresholds = %v/%v, want 0.95/2.0", P3MinRecoveredFraction, P3MaxOverheadFactor)
	}
	if P4ResidualLossMax != 0.005 || P4SteadyLossRate != 0.05 {
		t.Errorf("P4 thresholds = %v/%v, want 0.005/0.05", P4ResidualLossMax, P4SteadyLossRate)
	}
}
