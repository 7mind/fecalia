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
	// It gives way by a tenth. Cut to what the lane delivers, it fell to the
	// floor at the sender's first datagrams, the sender then waited for the
	// lane, and the next signal ended discovery there.
	if first.rate < 0.89*1.5e6 {
		t.Errorf("a sender-limited signal cut a discovering lane's target from 1500000 to %.0f B/s", first.rate)
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

// A probe that draws no congestion signal raises the estimate. On a path
// that drops what it cannot forward, the loss a probe caused shows later, so
// there a win counts only after thirty more datagrams; the next probe did not
// wait for that, and below about 170 kB/s every probe replaced the last
// before it was counted. No win was, the gain stayed at a twentieth and the
// lane never returned to discovery. Production, 2026-10-01: the mobile
// downlink lane, cold, ended its first discovery at about 90 kB/s and held a
// 15 s Speedtest download at 1.2 Mbit/s (50 Mbit/s lane); the uplink lane at
// 16-25 kB/s ran ten probes in eight seconds, none counted won. A lane that
// has shown no material loss lately does not wait for the thirty datagrams.
func TestProbeWinsCountOnASlowLane(t *testing.T) {
	start := time.Unix(100, 0)
	probing := func(policed bool) (*lane, time.Duration) {
		now := start
		p := &lane{rate: capacityHold * 90e3, deliveryRate: 85e3, sendRate: 85e3, rtt: 60 * time.Millisecond}
		p.control.capacity = 90e3
		if policed {
			p.droppedAt = now
		}
		p.schedulePulse(now)
		p.control.nextPulse = now
		var sent float64
		for step := 0; step < 200 && !p.startup; step++ {
			now = now.Add(50 * time.Millisecond)
			for sent += 65 * 0.05; sent >= 1; sent-- {
				p.seq++
			}
			p.sendRate = p.rate
			p.holdOrPulse(now, true)
		}
		return p, now.Sub(start)
	}

	p, elapsed := probing(false)
	t.Logf("no loss seen: %d probes, %d won, estimate %.0f B/s, discovering %v after %s", p.decisions.Pulses, p.decisions.PulseWins, p.control.capacity, p.startup, elapsed)
	if p.decisions.PulseWins < pulseWinsToLeave || !p.startup || elapsed > 4*time.Second {
		t.Errorf("%d probes without a congestion signal in %s: %d counted won, back in discovery: %v", p.decisions.Pulses, elapsed, p.decisions.PulseWins, p.startup)
	}

	// A path that polices keeps the wait: probing it costs datagrams
	// (`TestPolicedLaneIsNotOverdriven`).
	q, _ := probing(true)
	t.Logf("answered with loss lately: %d probes, %d won, estimate %.0f B/s, discovering %v", q.decisions.Pulses, q.decisions.PulseWins, q.control.capacity, q.startup)
	if q.startup {
		t.Errorf("a lane that answered a probe with loss returned to discovery after %d probes", q.decisions.Pulses)
	}
}
