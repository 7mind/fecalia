package bond

import (
	"math"
	"time"
)

const (
	capacityHold     = 0.95 // held target, as a fraction of demonstrated capacity
	capacityDrain    = 0.85 // target after a probe found the limit, to drain its queue
	capacityDecay    = 0.97 // estimate reduction per congestion cut while holding
	capacityDrop     = 0.75 // held target this far below the estimate: capacity fell
	rediscoveryGain  = 1.5  // discovery gain on a lane that already carried traffic
	pulseExcess      = 0.1  // pulse target above the estimate
	pulseGain        = 1.05 // estimate increase after a first pulse without congestion; doubles per further one
	pulseQueueFactor = 1.5  // pulse queue relative to the detection threshold
	pulseWinsToLeave = 3    // consecutive clean pulses before rediscovery
	pulseInterval    = time.Second
	pulseStagger     = 100 * time.Millisecond
	minPulse         = 150 * time.Millisecond
	maxPulse         = 500 * time.Millisecond
	verdictExpiry    = time.Second
	maxFlush         = 500 * time.Millisecond
)

// control holds a lane's target between discovery runs.
//
// Probing continuously keeps a standing queue in the path's buffer, where small
// datagrams have no priority over bulk. After discovery the target therefore
// holds just below the capacity the path demonstrated. A short pulse above the
// estimate tests for more: its queue is bounded by its length, not by feedback
// lag. A pulse that draws no congestion signal raises the estimate, and
// consecutive ones return the lane to discovery.
type control struct {
	capacity   float64
	draining   bool
	flushUntil time.Time
	holdSignal bool
	nextPulse  time.Time
	pulseEnd   time.Time
	verdictAt  time.Time
	pulseWins  int
}

func (c *control) awaitingVerdict() bool { return !c.verdictAt.IsZero() }

func (p *lane) schedulePulse(now time.Time) {
	p.control.verdictAt, p.control.pulseEnd = time.Time{}, time.Time{}
	p.control.nextPulse = now.Add(pulseInterval + time.Duration((uint16(p.id)^uint16(p.id)>>8)&7)*pulseStagger)
}

func (p *lane) measuredDelivery() float64 {
	return math.Max(p.deliveryRate, (p.deliverySample+p.previousDelivery)/2)
}

func (p *lane) congestionThreshold() time.Duration {
	return max(targetQueue, jitterAllowanceFactor*p.idleForwardVariation)
}

// adjust runs once per control interval of a lane with demand. laneLimited
// reports that bulk datagrams consistently waited for a lane, so the lanes,
// not the sender, limit what is carried.
//
// realtime reports that real-time datagrams are being carried. Discovery then
// uses the gentler gain: its overshoot queues in the path's buffer ahead of
// them (VM run 20260929-152215-continuity: 370 ms voice round trips in the
// first five seconds of a cold start).
func (p *lane) adjust(now time.Time, lost, delayed, sampled, laneLimited, realtime bool) {
	if p.startup && lost && !p.startupLossy {
		// Isolated timeouts under jitter are not the material loss that ends
		// discovery (BBRv2 exits startup on a round's loss rate); hold instead.
		return
	}
	lost = lost && p.deliveryRate > 0
	switch {
	case lost || delayed:
		p.congested(now, lost, laneLimited)
	case sampled:
		p.control.holdSignal, p.control.draining, p.control.flushUntil = false, false, time.Time{}
		switch {
		case p.startup:
			p.discover(now, realtime)
		case p.control.capacity == 0:
			p.rate = math.Min(maximumRate, p.rate*1.06+1500)
		default:
			p.holdOrPulse(now, laneLimited)
		}
	}
}

func (p *lane) cut(lost bool) float64 {
	if lost {
		return math.Min(lossRateReduction*p.rate, lossPacingHeadroom*p.deliveryRate)
	}
	rate := p.rate * 0.9
	if p.deliveryRate > 0 && p.sendRate > queueSendExcessRatio*p.deliveryRate {
		rate = math.Min(rate, lossPacingHeadroom*p.deliveryRate)
	}
	return rate
}

func (p *lane) congested(now time.Time, lost, laneLimited bool) {
	c := &p.control
	switch {
	case p.startup || c.capacity == 0:
		// Discovery saturated the path, so recent delivery measures capacity,
		// unless the sender was the limit: then it measures the sender, and
		// pulses must find the rest.
		// Discovery leaves a queue of up to a window in the path. Lower
		// classes wait, for a bounded time, until a clear interval shows it
		// has gone: real-time traffic alone may use most of a slow lane, and
		// a reduced target would then drain nothing.
		p.startup = false
		c.capacity, c.holdSignal, c.pulseWins, c.draining, c.flushUntil = p.measuredDelivery(), false, 0, true, now.Add(maxFlush)
		p.schedulePulse(now)
		if !laneLimited {
			p.rate = math.Max(minimumRate, math.Max(p.cut(lost), c.capacity))
			c.nextPulse = now
			return
		}
		p.rate = math.Max(minimumRate, math.Max(p.cut(lost), capacityDrain*c.capacity))
	case c.awaitingVerdict():
		// The pulse found the limit: the estimate stands and its queue drains.
		c.pulseWins, c.draining = 0, true
		p.rate = math.Max(minimumRate, capacityDrain*c.capacity)
		p.schedulePulse(now)
	case c.draining:
		// The queue a probe built is still draining; it says nothing new
		// about capacity.
		p.rate = math.Max(minimumRate, math.Max(p.cut(lost), capacityDrop*c.capacity))
	case !c.holdSignal:
		// One signal below demonstrated capacity may be jitter.
		c.holdSignal = true
	case p.rate < capacityDrop*c.capacity:
		// Repeated cuts took the target well below the estimate: capacity
		// fell. This measurement was taken below the new capacity, so test
		// for more at once.
		c.capacity, c.pulseWins = p.measuredDelivery(), 0
		p.rate = math.Max(minimumRate, p.cut(lost))
		p.schedulePulse(now)
		c.nextPulse = now
	default:
		c.capacity *= capacityDecay
		p.rate = math.Max(minimumRate, p.cut(lost))
	}
}

func (p *lane) holdOrPulse(now time.Time, laneLimited bool) {
	c := &p.control
	limit := capacityHold * c.capacity
	switch {
	case c.awaitingVerdict() && now.Before(c.pulseEnd):
	case c.awaitingVerdict() && now.Before(c.verdictAt):
		p.rate = limit
	case c.awaitingVerdict() && now.Sub(c.verdictAt) < verdictExpiry:
		// The pulse's feedback arrived without a congestion signal.
		c.capacity *= 1 + (pulseGain-1)*float64(int(1)<<c.pulseWins)
		if c.pulseWins++; c.pulseWins >= pulseWinsToLeave {
			c.pulseWins = 0
			p.schedulePulse(now)
			p.startup, p.startupBest, p.startupFlatRounds, p.discoveryGain = true, 0, 0, rediscoveryGain
			return
		}
		p.pulse(now)
	case c.awaitingVerdict():
		p.schedulePulse(now)
	case !now.Before(c.nextPulse) && p.rate >= limit && laneLimited:
		p.pulse(now)
	case p.rate < limit:
		p.rate = math.Min(limit, p.rate*1.06+1500)
	}
}

// pulse raises the target above the estimate for long enough to build a queue
// the delay signal can detect if the estimate is the path's capacity.
func (p *lane) pulse(now time.Time) {
	c := &p.control
	length := time.Duration(pulseQueueFactor / pulseExcess * float64(p.congestionThreshold()))
	length = min(maxPulse, max(minPulse, length))
	c.pulseEnd = now.Add(length)
	c.verdictAt = c.pulseEnd.Add(p.rtt + p.peerACKInterval())
	p.rate = math.Min(maximumRate, (1+pulseExcess)*c.capacity)
}

// discover follows measured delivery at the pace of a sender's slow start
// until the first congestion signal or a delivery plateau.
func (p *lane) discover(now time.Time, realtime bool) {
	gain := p.discoveryGain
	if realtime {
		gain = math.Min(gain, rediscoveryGain)
	}
	p.rate = math.Min(maximumRate, p.rate*1.06+1500)
	// Jitter can mask queue delay, so a lane that sends at its pacing rate
	// while delivery stops growing has found its capacity (BBR's full-pipe
	// rule). Flat delivery below the pacing rate is not a plateau.
	delivered := math.Max(p.deliveryRate, p.deliverySample)
	if p.roundDone {
		p.roundDone = false
		if delivered >= startupPlateauGrowth*p.startupBest {
			p.startupBest, p.startupFlatRounds = delivered, 0
		} else if p.sendRate < startupPlateauSending*p.rate {
			p.startupFlatRounds = 0
		} else if p.startupFlatRounds++; p.startupFlatRounds >= startupPlateauRounds {
			p.startup = false
			p.control = control{capacity: p.startupBest}
			p.schedulePulse(now)
			p.rate = math.Max(minimumRate, capacityHold*p.startupBest)
			return
		}
	}
	// The target may lead what the lane carries by the discovery gain only: a
	// target inflated while the sender was the limit leaves the path
	// unprotected when the sender catches up.
	p.rate = math.Max(p.rate, gain*delivered)
	if carried := math.Max(delivered, p.sendRate); carried > 0 {
		// Once delivery is measured the initial assumption no longer
		// applies: on a lane slower than it, the window sized from it admits
		// a queue of hundreds of milliseconds.
		p.rate = math.Max(minimumRate, math.Min(p.rate, gain*carried))
	}
}
