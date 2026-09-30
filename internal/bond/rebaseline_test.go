package bond

import (
	"testing"
	"time"
)

// State from the deterministic model at 35 s (lane 0: 20±10 ms each way, lane
// 1: 40±30 ms): one round trip of 23 ms on the drained jittery lane must not
// rank it ahead of the steadier lane.
func TestOneUnloadedSampleDoesNotReorderLanes(t *testing.T) {
	steady := &lane{idleRTT: 45921 * time.Microsecond, idleRTTVariation: 5584 * time.Microsecond}
	jittery := &lane{idleRTT: 80 * time.Millisecond, idleRTTVariation: 15925 * time.Microsecond}
	jittery.rebaseline(23 * time.Millisecond)
	if jittery.latency() <= steady.latency() {
		t.Fatalf("jittery lane ranked at %s ahead of the steady lane at %s", jittery.latency(), steady.latency())
	}
	// A changed propagation delay is still followed within a few samples.
	moved := &lane{idleRTT: 80 * time.Millisecond}
	for range 4 {
		moved.rebaseline(30 * time.Millisecond)
	}
	if moved.idleRTT > 35*time.Millisecond {
		t.Fatalf("unloaded round trip %s after four samples of 30 ms", moved.idleRTT)
	}
}
