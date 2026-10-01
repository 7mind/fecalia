//go:build e2e

package e2e

import "testing"

// TestThresholds is a pure-value sanity check on the Q1 acceptance table. It
// needs no privileges, so it also demonstrates that the e2e harness imports the
// constants (no magic literals in the phase tests). The privileged phase tests
// (tunnel bring-up) live alongside it under the same e2e build tag and
// require root + /dev/net/tun.
func TestThresholds(t *testing.T) {
	if P1RecoverySeconds != 3 {
		t.Errorf("P1RecoverySeconds = %d, want 3", P1RecoverySeconds)
	}
}
