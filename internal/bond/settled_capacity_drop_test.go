package bond_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/bond"
)

// Performance-Blackbox-Group: capacity falls only after measured service settles.
func TestSettledCapacityDropDrainsWithoutLosingService(t *testing.T) {
	for _, remaining := range []float64{0.8, 0.5, 0.25} {
		t.Run(fmt.Sprintf("%.0f-percent", remaining*100), func(t *testing.T) {
			t.Parallel()
			rate := func(at time.Duration) float64 {
				if at >= 20*time.Second {
					return remaining * fastLaneRate
				}
				return fastLaneRate
			}
			delay := func(time.Duration) time.Duration { return 20 * time.Millisecond }
			delivered, backlog := changingLane(t, rate, delay, 28)
			var before float64
			for _, bytes := range delivered[17:20] {
				before += bytes / 3
			}
			if before < .75*fastLaneRate*1300/(1300+bond.Overhead) {
				t.Fatalf("fixture did not establish steady bulk service before the capacity drop: %.0f B/s", before)
			}
			t.Logf("delivered %v MB/s, link queue %v", megabytes(delivered), backlog)
			for second := 24; second < len(delivered); second++ {
				if backlog[second] > 60*time.Millisecond {
					t.Errorf("second %d still queued for %s after capacity fell", second, backlog[second])
				}
				if delivered[second] < .75*remaining*before {
					t.Errorf("second %d delivered %.0f B/s, below 75%% of new capacity relative to %.0f B/s measured before", second, delivered[second], before)
				}
			}
		})
	}
}
