//go:build progression

package bond_test

import "testing"

// A lane loaded from its first packet never observes idle keepalives, so its
// jitter allowance stays zero. Radio jitter of +-30 ms then reads as queueing
// against the 10 ms target and holds some seeds near 0.2 Mbit/s. Known
// limitation, identical before and after the discovery phase was added.
func TestColdDiscoveryUnderRadioJitterWithoutIdleHistory(t *testing.T) {
	coldDiscoveryUnderRadioJitter(t, 0)
}
