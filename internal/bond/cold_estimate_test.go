package bond

import (
	"testing"
	"time"
)

// Discovery ends at the first congestion signal with what the lane delivers
// as its capacity. While the sender, not the lane, is the limit, delivery
// measures the sender. Production, 2026-10-01: the mobile uplink lane had
// returned to discovery with 1.16 MB/s demonstrated when a transfer ended;
// 0.4 s into the next one a single delay sample of 80 ms ended that discovery
// while the lane had delivered 6 kB/s, and the estimate became the 16 kB/s
// floor. That upload and the next ran at 0.4 Mbit/s.
func TestSenderLimitedDiscoveryKeepsTheEstimate(t *testing.T) {
	now := time.Unix(100, 0)
	starting := func(estimate float64) *lane {
		p := &lane{startup: true, rate: 1.5e6, deliveryRate: 6e3, deliverySample: 6e3, previousDelivery: 1e3, sendRate: 29e3,
			rtt: 60 * time.Millisecond, queueDelay: 80 * time.Millisecond}
		p.control.capacity = estimate
		return p
	}

	returned := starting(1.16e6)
	returned.congested(now, false, false)
	if got := returned.control.capacity; got < 1.16e6 {
		t.Errorf("a return to discovery ended by a sender-limited signal left an estimate of %.0f B/s, had 1160000", got)
	}

	// A first discovery has no estimate to keep: the target gives way and
	// discovery goes on.
	first := starting(0)
	first.congested(now, false, false)
	if !first.startup || first.control.capacity != 0 || first.rate >= 1.5e6 {
		t.Errorf("a first discovery met by a sender-limited signal: discovering %v, estimate %.0f, target %.0f B/s", first.startup, first.control.capacity, first.rate)
	}

	// A lane that limits the sender has shown its capacity.
	limiting := starting(1.16e6)
	limiting.deliveryRate, limiting.deliverySample, limiting.previousDelivery, limiting.sendRate = 400e3, 400e3, 400e3, 420e3
	limiting.congested(now, false, true)
	if got := limiting.control.capacity; got != 400e3 {
		t.Errorf("a discovery that saturated the lane left an estimate of %.0f B/s, delivered 400000", got)
	}

	// A path that drops what it cannot forward delivers its capacity.
	policed := starting(1.16e6)
	policed.deliveryRate, policed.deliverySample, policed.previousDelivery, policed.sendRate = 60e3, 60e3, 60e3, 70e3
	policed.congested(now, true, false)
	if got := policed.control.capacity; got != 60e3 {
		t.Errorf("material loss ended discovery with an estimate of %.0f B/s, delivered 60000", got)
	}
}
