//go:build adaptivepolicy

package bond_test

import (
	"testing"
	"time"
)

// Performance-Progression-Blackbox-Group: the legacy mechanism checks pass
// without proving that sparse or resumed TCP traffic uses a policed path.
func TestAdaptivePolicedSparseAndResumedSenders(t *testing.T) {
	checkSparseAndResumedSenders(t, true)
}

// Performance-Progression-Blackbox-Group: counted wins on a formerly slow
// lane do not prove that its TCP service follows the new rate.
func TestAdaptiveSlowLaneRateIncreaseMakesBulkProgress(t *testing.T) {
	checkSlowLaneRateIncrease(t)
}

// Performance-Blackbox-Group: a formerly slow lane must carry the extra
// demand after its service rate rises, rather than merely count probe wins.
func checkSlowLaneRateIncrease(t *testing.T) {
	t.Helper()
	m := policyRun{lanes: []modelLane{{condition: func(side int, at time.Duration) modelCondition {
		rate := 90e3
		if at >= 10*time.Second {
			rate = 625e3
		}
		return modelCondition{rate: rate, delay: 30 * time.Millisecond, buffer: 100 * time.Millisecond}
	}}}, seconds: 30, trafficAt: 2, voice: true, bulk: true}
	checkCapacityService(t, m, 20, 30)
}
