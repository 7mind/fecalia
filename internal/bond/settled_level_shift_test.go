package bond_test

import (
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/bond"
)

// Performance-Blackbox-Group: propagation changes preserve measured service
// after capacity discovery has settled.
func TestSettledBulkServiceSurvivesAPropagationLevelShift(t *testing.T) {
	rate := func(time.Duration) float64 { return fastLaneRate }
	delay := func(elapsed time.Duration) time.Duration {
		if elapsed >= 20*time.Second {
			return 35 * time.Millisecond
		}
		return 20 * time.Millisecond
	}
	delivered, backlog := changingLane(t, rate, delay, 32)
	var before float64
	for _, bytes := range delivered[17:20] {
		before += bytes / 3
	}
	if before < .75*fastLaneRate*1300/(1300+bond.Overhead) {
		t.Fatalf("fixture did not establish steady bulk service before the change: %.0f B/s", before)
	}
	t.Logf("delivered %v MB/s, link queue %v", megabytes(delivered), backlog)
	for second := 22; second < len(delivered); second++ {
		if delivered[second] < .75*before {
			t.Fatalf("second %d delivered %.0f B/s, below 75%% of %.0f B/s before a propagation-only change", second, delivered[second], before)
		}
	}
}
