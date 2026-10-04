//go:build adaptivepolicy

package bond_test

import "testing"

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
