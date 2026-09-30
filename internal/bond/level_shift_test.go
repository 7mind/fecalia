package bond_test

import (
	"fmt"
	"testing"
	"time"
)

// A path's latency moves to a level 15 ms higher, as a satellite link's does
// when its path is reassigned. Measured from the transit floor of the lower
// level, the higher one is a 15 ms queue that no reduction of the target
// drains. The target was cut interval after interval until it had halved and
// the floor was measured again; a change that fell on a probe left the target
// at three quarters of the capacity for as long as the level lasted.
func TestLatencyLevelShiftDoesNotCutTheTarget(t *testing.T) {
	rate := func(time.Duration) float64 { return fastLaneRate }
	for _, shiftAt := range changeTimes {
		t.Run(fmt.Sprintf("at %s", shiftAt), func(t *testing.T) {
			t.Parallel()
			delay := func(elapsed time.Duration) time.Duration {
				if elapsed >= shiftAt {
					return 35 * time.Millisecond
				}
				return 20 * time.Millisecond
			}
			delivered, _ := changingLane(t, rate, delay, changeRunSeconds)
			t.Logf("delivered %v MB/s", megabytes(delivered))
			for second := beforeChange + 1; second < changeRunSeconds; second++ {
				if delivered[second] < 0.8*delivered[beforeChange] {
					t.Fatalf("second %d delivered %.1f MB/s after %.1f MB/s before the latency changed level", second, delivered[second]/1e6, delivered[beforeChange]/1e6)
				}
			}
		})
	}
}
