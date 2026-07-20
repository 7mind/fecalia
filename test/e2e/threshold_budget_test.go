//go:build e2e

package e2e

import (
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/telemetry"
)

// TestRecoveryBudgetMatchesP1RecoverySeconds is the D16 anti-drift identity: the
// hoisted telemetry.RecoveryBudget (a Duration) and the e2e table's P1RecoverySeconds
// (an int SECONDS-count consumed at its int call sites) are two representations of the
// SAME 3s P1 deadline. Asserting their equality here — the one package that imports
// both — guarantees a change to either representation without the other is caught at
// test time, not in a silently-retuned production budget.
func TestRecoveryBudgetMatchesP1RecoverySeconds(t *testing.T) {
	if telemetry.RecoveryBudget != time.Duration(P1RecoverySeconds)*time.Second {
		t.Errorf("telemetry.RecoveryBudget = %s, want time.Duration(P1RecoverySeconds=%d)*time.Second = %s",
			telemetry.RecoveryBudget, P1RecoverySeconds, time.Duration(P1RecoverySeconds)*time.Second)
	}
}

// TestFailoverBudgetUnchangedAtDefaults pins the numerically-unchanged e2e threshold
// (T211 acceptance): rederiving PLivenessFailoverBudget through telemetry.FailoverBudget
// must still produce the byte-identical 1.6s the pre-T211 const expression produced
// (DownAfter 1200ms + 2*200ms interval), so no phase test's timing assertion shifts.
func TestFailoverBudgetUnchangedAtDefaults(t *testing.T) {
	if PLivenessFailoverBudget != 1600*time.Millisecond {
		t.Errorf("PLivenessFailoverBudget = %s, want 1.6s (unchanged at defaults)", PLivenessFailoverBudget)
	}
}
