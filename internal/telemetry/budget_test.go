package telemetry

import (
	"testing"
	"time"
)

// TestFailoverBudgetDerivation pins the pure budget arithmetic (D86 decision 4):
// FailoverBudget(downAfter, rideThrough, probeInterval) = downAfter + rideThrough
// + 2*probeInterval. The default row (1200ms / 0 / 200ms = 1600ms) is the value
// test/e2e/thresholds.go's PLivenessFailoverBudget must keep reading, so this
// table is the guard against an accidental change to the composition.
func TestFailoverBudgetDerivation(t *testing.T) {
	cases := []struct {
		name          string
		downAfter     time.Duration
		rideThrough   time.Duration
		probeInterval time.Duration
		want          time.Duration
	}{
		{
			name:          "defaults",
			downAfter:     DefaultDownAfter,
			rideThrough:   0,
			probeInterval: DefaultProbeInterval,
			want:          1600 * time.Millisecond,
		},
		{
			name:          "ride_through_adds_dwell",
			downAfter:     DefaultDownAfter,
			rideThrough:   500 * time.Millisecond,
			probeInterval: DefaultProbeInterval,
			want:          2100 * time.Millisecond,
		},
		{
			name:          "larger_down_after",
			downAfter:     5 * time.Second,
			rideThrough:   0,
			probeInterval: DefaultProbeInterval,
			want:          5400 * time.Millisecond,
		},
		{
			name:          "all_zero",
			downAfter:     0,
			rideThrough:   0,
			probeInterval: 0,
			want:          0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := FailoverBudget(tc.downAfter, tc.rideThrough, tc.probeInterval); got != tc.want {
				t.Errorf("FailoverBudget(%s, %s, %s) = %s, want %s",
					tc.downAfter, tc.rideThrough, tc.probeInterval, got, tc.want)
			}
		})
	}
}

// TestRecoveryBudgetValue pins the hoisted P1 recovery deadline to 3s — the value
// the e2e table's P1RecoverySeconds (a seconds-count) must equal once converted to
// a Duration. The cross-representation identity assertion itself lives in test/e2e
// (TestRecoveryBudgetMatchesP1RecoverySeconds), which can import both symbols.
func TestRecoveryBudgetValue(t *testing.T) {
	if RecoveryBudget != 3*time.Second {
		t.Errorf("RecoveryBudget = %s, want 3s", RecoveryBudget)
	}
}
