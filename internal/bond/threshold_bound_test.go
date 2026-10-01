package bond

import (
	"testing"
	"time"
)

// The delay threshold stands above the variation of an idle lane's transit
// time. That variation is the path's own only while nothing else loads the
// path: a transfer on the same link outside the tunnel queues the lane's
// keepalives behind it, and their variation is then that queue. Production,
// 2026-10-01: a Speedtest on the mobile link itself, with 552 ms of loaded
// latency, raised the idle variation of the tunnel's uplink lane to 273 and
// 361 ms and its threshold to 546 and 722 ms. The Speedtest through the bond
// that followed ran without a delay signal and measured 764 ms of loaded
// latency, 2.5 s at the highest.
func TestThresholdIsBounded(t *testing.T) {
	for _, c := range []struct {
		variation, want time.Duration
	}{
		{4 * time.Millisecond, targetQueue},
		{20 * time.Millisecond, 40 * time.Millisecond},
		{40 * time.Millisecond, 80 * time.Millisecond},
		{361 * time.Millisecond, maxThreshold},
	} {
		p := &lane{idleForwardVariation: c.variation}
		if got := p.congestionThreshold(); got != c.want {
			t.Errorf("idle variation %s: threshold %s, want %s", c.variation, got, c.want)
		}
	}
	wandering := &lane{wanderKnown: true, wander: 300 * time.Millisecond, wanderVariation: 100 * time.Millisecond}
	if got := wandering.congestionThreshold(); got != maxThreshold {
		t.Errorf("wander of 300 ms: threshold %s, want %s", got, maxThreshold)
	}
}
