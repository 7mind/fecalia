package bond

import (
	"math"
	"time"
)

const (
	capacityHold     = 0.95 // held target, as a fraction of demonstrated capacity
	wanderLoad       = 0.5  // a lane sending below this share of its target queues nothing
	capacityDrain    = 0.85 // target after a probe found the limit, to drain its queue
	capacityDecay    = 0.97 // estimate reduction per congestion cut while holding
	capacityDrop     = 0.75 // held target this far below the estimate: capacity fell
	rediscoveryGain  = 1.5  // discovery gain on a lane that already carried traffic
	pulseExcess      = 0.1  // pulse target above the estimate
	pulseGain        = 1.05 // estimate increase after a first pulse without congestion; doubles per further one
	pulseQueueFactor = 1.5  // pulse queue relative to the detection threshold
	pulseWinsToLeave = 3    // consecutive clean pulses before rediscovery
	// A pulse is won once this many datagrams followed it without loss: on a
	// path that drops a tenth of them, a run this long without one is rare.
	pulseConfirmDatagrams = 30
	pulseInterval         = time.Second
	// A probe that ends in loss found a path that drops rather than queues;
	// the next waits twice as long, up to this, until one ends without loss.
	maxPulseInterval = 16 * time.Second
	policingMemory   = time.Minute // how long a path that showed material loss counts as one that polices
	// The least time between two tests of a lane's transit floor doubles with
	// every test that finds a queue, up to the longest, and starts again when
	// one finds the floor moved: a lane whose delay keeps turning out to be a
	// queue pays a pause for nothing (a 0.4 Mbit/s lane tested every two
	// seconds delivered 87% of its capacity instead of 93%,
	// `TestSlowLaneAcknowledgementShareIsBounded`).
	floorTestInterval    = 2 * time.Second
	maxFloorTestInterval = 16 * time.Second
	pulseStagger         = 100 * time.Millisecond
	minPulse             = 150 * time.Millisecond
	maxPulse             = 500 * time.Millisecond
	verdictExpiry        = time.Second
	maxFlush             = 500 * time.Millisecond
	flushQueues          = 2 // flush for this many times the queue delay measured
	// A queue is allowed this many times the delay measured to drain, and a
	// test of the transit floor pauses bulk for as long: the delay is
	// measured a round trip late, and what built the queue ran on.
	drainQueues = 2
	// maxStallClear bounds how long delay is put down to a stall.
	maxStallClear = time.Second
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
	capacity float64
	draining bool
	// drainBy is when the queue that explains the lane's delay will have
	// left the path: a probe's, or the one a cut is draining.
	drainBy    time.Time
	flushUntil time.Time
	holdSignal bool
	nextPulse  time.Time
	pulseEvery time.Duration
	pulseEnd   time.Time
	verdictAt  time.Time
	pulseWins  int
	// A pulse without a signal raises the estimate at once but counts as won
	// only once the loss it may have caused would have shown: loss is known
	// a settling period after it happens. wonFrom is the estimate to return
	// to if it does.
	pending bool
	wonAt   time.Time
	wonSeq  uint64
	wonFrom float64
}

func (c *control) awaitingVerdict() bool { return !c.verdictAt.IsZero() }

func (p *lane) schedulePulse(now time.Time) {
	p.control.verdictAt, p.control.pulseEnd = time.Time{}, time.Time{}
	p.control.nextPulse = now.Add(max(pulseInterval, p.control.pulseEvery) + time.Duration((uint16(p.id)^uint16(p.id)>>8)&7)*pulseStagger)
}

func (p *lane) measuredDelivery() float64 {
	return math.Max(p.deliveryRate, (p.deliverySample+p.previousDelivery)/2)
}

// demonstrated is the capacity the lane has shown: what it delivers now, or
// what it kept up shortly before if that was more.
func (p *lane) demonstrated(now time.Time) float64 {
	return math.Max(p.measuredDelivery(), p.sustained.value(now))
}

// congestionThreshold is the queue delay above which the path is taken to be
// queueing this lane's datagrams.
//
// Queue delay is measured from the lowest transit time seen. A path whose
// latency wanders over tens of milliseconds sits above that floor most of the
// time, by more than the unloaded variation suggests when the wander is slow:
// the samples of a control interval then move together, and their minimum is
// no nearer the floor than any of them. The threshold therefore stands above
// the delay seen while the lane sent too little to queue anything (latency
// measured on the production links on 2026-09-30 wanders with a standard
// deviation of 3-5 ms one way; a model lane with that wander and a 10 ms
// threshold was held at its minimum rate, `TestWanderingLatencyIsNotAQueue`).
func (p *lane) congestionThreshold() time.Duration {
	threshold := max(targetQueue, jitterAllowanceFactor*p.idleForwardVariation)
	if p.wanderKnown {
		threshold = max(threshold, p.wander+jitterAllowanceFactor*p.wanderVariation+targetQueue)
	}
	// What an idle lane sees is the path's own only while nothing else loads
	// the path: a transfer on the same link outside the tunnel queues the
	// lane's keepalives behind it, and with that taken for jitter the lane
	// then queued without a signal (`TestThresholdIsBounded`).
	return min(threshold, maxThreshold)
}

// lightlyLoaded reports that the lane sends well below what it has shown it
// can carry and that delivery keeps up: whatever delay it sees, it did not
// queue. Before any capacity is known the target stands in for it; a target
// inflated by discovery is then no excuse, since the lane sends half of it
// only while the sender, not the lane, is the limit.
func (p *lane) lightlyLoaded() bool {
	reference := p.control.capacity
	if p.startup || reference == 0 {
		reference = math.Min(p.rate, math.Max(initialRate, p.recentDelivery.value(p.lastACK)))
	}
	return p.sendRate < wanderLoad*reference && p.deliveryRate > 0 && p.sendRate <= queueSendExcessRatio*p.deliveryRate
}

// observeWander folds the queue delay of a lightly loaded control interval
// into the estimate of the path's own wander above its floor.
func (p *lane) observeWander(delay time.Duration) {
	if !p.wanderKnown {
		p.wander, p.wanderVariation, p.wanderKnown = delay, delay/2, true
		return
	}
	difference := delay - p.wander
	if difference < 0 {
		difference = -difference
	}
	p.wanderVariation = (3*p.wanderVariation + difference) / 4
	p.wander = (7*p.wander + delay) / 8
}

// stall is the last time the lane delivered nothing for a while and then what
// it held: a stall of the path, not a queue this lane built. What was sent
// meanwhile waits behind it. That backlog is as long as the stall when it
// ends and leaves at the draining target in several times the stall's
// length; delay within what is left of it says nothing about capacity.
type stall struct {
	length           time.Duration
	ended, clearedBy time.Time
}

func (p *lane) stallEnded(now time.Time, silence time.Duration) {
	clear := min(maxStallClear, time.Duration(float64(silence)/(1-capacityDrop))+2*p.rtt)
	p.stall = stall{length: silence, ended: now, clearedBy: now.Add(clear)}
}

// explains reports whether the stall's backlog accounts for the delay: a
// queue this lane builds grows or stands, the backlog only shrinks.
func (s *stall) explains(now time.Time, delay, threshold time.Duration) bool {
	if !now.Before(s.clearedBy) {
		return false
	}
	left := time.Duration(float64(s.length) * float64(s.clearedBy.Sub(now)) / float64(s.clearedBy.Sub(s.ended)))
	return delay <= left+threshold
}

// stalledSignal answers a delay signal that follows a stall. The estimate
// stands and discovery goes on: a lane that carries nothing for a while
// delays whatever is sent, at any rate (`TestStallDoesNotEndDiscovery`,
// `TestStalledLaneKeepsItsEstimate`). The target gives way as it does while a
// probe's queue drains, so that the backlog leaves.
func (p *lane) stalledSignal(now time.Time) {
	c := &p.control
	p.decisions.StallSignals++
	switch {
	case p.startup || c.capacity == 0:
	case c.awaitingVerdict():
		// The pulse's verdict is lost with the stall; neither won nor lost.
		p.schedulePulse(now)
		p.rate = math.Max(minimumRate, capacityDrain*c.capacity)
	default:
		p.rate = math.Max(minimumRate, math.Max(p.cut(false), capacityDrop*c.capacity))
	}
}

// adjust runs once per control interval of a lane with demand. laneLimited
// reports that bulk datagrams consistently waited for a lane, so the lanes,
// not the sender, limit what is carried.
//
// realtime reports that real-time datagrams are being carried. Discovery then
// uses the gentler gain: its overshoot queues in the path's buffer ahead of
// them (VM run 20260929-152215-continuity: 370 ms voice round trips in the
// first five seconds of a cold start). stream reports that they arrive as a
// steady stream.
func (p *lane) adjust(now time.Time, lost, delayed, sampled, laneLimited, realtime, stream bool) {
	if p.startup && lost && !p.roundLossy {
		// Isolated timeouts under jitter are not the material loss that ends
		// discovery (BBRv2 exits startup on a round's loss rate); hold instead.
		return
	}
	// The same rule holds the target: a path that loses a fraction of a
	// percent at random loses something in every control interval at a high
	// rate, and cutting on each of them drove a 300 Mbit/s model lane to
	// 3 MB/s (`TestRandomLossDoesNotCollapseTheTarget`). Repair covers such
	// loss; only a round's material loss is congestion.
	lost = lost && p.roundLossy && p.deliveryRate > 0
	switch {
	case delayed && !lost && p.stall.explains(now, p.signalDelay, p.congestionThreshold()):
		p.stalledSignal(now)
	case lost || delayed:
		if delayed {
			p.decisions.DelaySignals++
		}
		if lost {
			p.decisions.LossSignals++
			p.droppedAt = now
		}
		p.congested(now, lost, laneLimited)
	case sampled:
		p.control.holdSignal, p.control.draining, p.control.flushUntil = false, false, time.Time{}
		switch {
		case p.startup:
			p.discover(now, realtime, stream, laneLimited)
		case p.control.capacity == 0:
			p.rate = math.Min(maximumRate, p.rate*1.06+1500)
		default:
			p.holdOrPulse(now, laneLimited)
		}
	}
}

func (p *lane) cut(lost bool) float64 {
	if lost {
		// A saturated path delivers its capacity while it drops the rest, so
		// the target must go below what was delivered, not above it. A lane
		// sending well below its target lost to its bursts, and what it
		// delivered says nothing about the path (a lane that took over voice
		// from a failed one sent 30 kB/s of a 42 kB/s target into a policed
		// 50 kB/s path and was cut to 25 kB/s: 96 voice datagrams dropped in
		// its own queue).
		if !p.saturating() {
			return lossRateReduction * p.rate
		}
		return math.Min(lossRateReduction*p.rate, capacityHold*p.deliveryRate)
	}
	rate := p.rate * 0.9
	if p.deliveryRate > 0 && p.sendRate > queueSendExcessRatio*p.deliveryRate {
		rate = math.Min(rate, lossPacingHeadroom*p.deliveryRate)
	}
	return rate
}

func (p *lane) congested(now time.Time, lost, laneLimited bool) {
	c := &p.control
	queued := p.queueDelay
	if c.pending && lost {
		// The loss the last pulse caused has shown: it was not won. The path
		// dropped the pulse's excess instead of queueing it; probing such a
		// path costs datagrams, so the next probe waits longer
		// (`TestPolicedLaneIsNotOverdriven`).
		p.decisions.PulseLosses++
		c.capacity = math.Min(c.capacity, c.wonFrom)
		c.pending, c.pulseWins = false, 0
		c.pulseEvery = min(maxPulseInterval, 2*max(pulseInterval, c.pulseEvery))
	} else if c.pending && p.pulseSettled(now) {
		p.decisions.PulseWins++
		c.pending, c.pulseEvery = false, 0
	}
	probed := c.drainBy
	if lost {
		// What a probe lost is known a settling period after it was lost.
		probed = probed.Add(lossSettle)
	}
	if c.draining && !now.Before(probed) {
		// The probe's queue has had the time to leave, and delay was seen
		// throughout: it is not the probe's. Taken for it, the delay of a lane
		// that lost half its capacity while a probe drained held the target at
		// three quarters of the old capacity, with a full buffer in the path
		// (`TestCapacityDropIsNotALevelShift`).
		c.draining, c.holdSignal = false, true
	}
	switch {
	case p.startup && c.capacity == 0 && !laneLimited && !lost:
		// The sender, not the lane, is the limit, and the lane has shown
		// nothing yet: what it delivers measures the sender. Taken for its
		// capacity, that held the target at the sender's first datagrams,
		// and probes then raised it by a twentieth at a time. The target
		// gives way to the delay by a tenth and discovery goes on
		// (`TestSenderLimitedDiscoveryKeepsTheEstimate`). Cut to what the
		// lane delivers, the sender's first datagrams, it fell to the floor;
		// the sender then waited for the lane, and the next signal ended
		// discovery there.
		p.rate = math.Max(minimumRate, 0.9*p.rate)
	case p.startup || c.capacity == 0:
		// Discovery saturated the path, so recent delivery measures capacity,
		// unless the sender was the limit: then it measures the sender, and
		// what the lane was known to carry before this return to discovery
		// still stands. Material loss is the path's own answer whoever was
		// the limit.
		// Discovery leaves a queue in the path. Lower classes wait for
		// about as long as it takes to drain, or until a clear interval
		// shows it has gone: real-time traffic alone may use most of a slow
		// lane, and a reduced target would then drain nothing. A longer
		// pause on a fast lane fills the tunnel queue beyond its bound (VM
		// trace 20260929-171122-radio-down-k1: 995 datagrams expired at once).
		p.decisions.DiscoveryCongested++
		p.startup = false
		shown := p.demonstrated(now)
		if !laneLimited && !lost {
			shown = math.Max(shown, c.capacity)
		}
		c.capacity, c.holdSignal, c.pulseWins, c.draining = shown, false, 0, true
		c.drainBy = now.Add(p.drainTime(queued))
		c.flushUntil = now.Add(min(maxFlush, flushQueues*queued))
		p.schedulePulse(now)
		if !laneLimited {
			p.rate = math.Max(minimumRate, math.Max(p.cut(lost), c.capacity))
			c.nextPulse = now
			return
		}
		p.rate = math.Max(minimumRate, math.Max(p.cut(lost), capacityDrain*c.capacity))
	case c.awaitingVerdict():
		// The pulse found the limit: the estimate stands and its queue drains.
		// A path that answered with loss instead of delay drops what it
		// cannot forward: probing it costs datagrams, so the next probe waits
		// longer (`TestPolicedLaneIsNotOverdriven`).
		p.decisions.PulseLosses++
		c.pulseWins, c.draining = 0, true
		if lost {
			c.pulseEvery = min(maxPulseInterval, 2*max(pulseInterval, c.pulseEvery))
		}
		c.drainBy = now.Add(p.drainTime(queued))
		p.rate = math.Max(minimumRate, capacityDrain*c.capacity)
		p.schedulePulse(now)
	case c.draining:
		// The queue a probe built is still draining; it says nothing new
		// about capacity.
		p.rate = math.Max(minimumRate, math.Max(p.cut(lost), capacityDrop*c.capacity))
	case !c.holdSignal && !lost:
		// One delay signal below demonstrated capacity may be jitter.
		// Material loss is judged over enough datagrams already, and on a
		// slow lane it shows in one control interval of several.
		c.holdSignal = true
	case !lost && p.floorTestable(now, p.signalDelay):
		// Delay that persists on a lane holding below its capacity is either
		// a queue or a path whose latency moved to a higher level: measured
		// from the floor of the lower one, the higher level reads as a queue
		// for as long as it lasts, and no reduction of the target removes it.
		// The two are told apart before the target is cut.
		p.testFloor(now, p.signalDelay)
	case p.rate < capacityDrop*c.capacity && (lost || p.sustained.value(now) < capacityDecay*c.capacity):
		// Repeated cuts took the target well below the estimate: capacity
		// fell. This measurement was taken below the new capacity, so test
		// for more at once.
		p.decisions.CapacityRemeasured++
		c.capacity, c.pulseWins = p.measuredDelivery(), 0
		if !lost {
			c.capacity = p.demonstrated(now)
		}
		p.rate = math.Max(minimumRate, p.cut(lost))
		c.drainBy = now.Add(p.drainTime(queued))
		p.schedulePulse(now)
		c.nextPulse = now
	default:
		p.decisions.CapacityDecays++
		decayed := capacityDecay * c.capacity
		if !lost {
			// Delay does not lower the estimate below what the lane kept
			// up shortly before.
			decayed = math.Max(decayed, math.Min(c.capacity, p.sustained.value(now)))
		}
		c.capacity = decayed
		if lost && p.saturating() {
			// Material loss while sending the target: the path delivers no
			// more than it did.
			c.capacity = math.Min(c.capacity, p.deliveryRate)
		}
		p.rate = math.Max(minimumRate, p.cut(lost))
		c.drainBy = now.Add(p.drainTime(queued))
	}
}

// saturating reports that the lane sends about its target, so what the path
// delivers measures the path and not the sender.
func (p *lane) saturating() bool {
	return p.sendRate >= capacityDrain*p.rate
}

// drainTime is how long a queue may take to leave a path sent to at the
// draining target, with the round trip its feedback needs and a control
// interval. The delay is measured a round trip late and what built the queue
// ran on meanwhile, so twice the queue measured is allowed for.
func (p *lane) drainTime(queued time.Duration) time.Duration {
	return time.Duration(drainQueues*float64(queued)/(1-capacityDrain)) + 2*p.rtt
}

// testFloor asks whether the lane's delay is a queue. Bulk pauses for long
// enough to drain the queue the delay would be; what is sent afterwards finds
// an empty path. If it is still as late, the path's latency moved: the floor
// is measured anew, and the target stands (floorMoved). If it is not, the
// delay was a queue, and the estimate that let it form is lowered
// (queueFound).
//
// A test is no answer to a lane that keeps queueing: between tests, delay is
// taken for a queue.
func (p *lane) testFloor(now time.Time, queued time.Duration) {
	pause, _ := p.floorTestPause(queued)
	p.drainUntil = now.Add(pause)
	p.baselinePending, p.floorTesting = true, true
	p.floorTested = now
	p.control.holdSignal = false
	// A probe now would queue on top of whatever the test is about.
	p.schedulePulse(now)
}

// queueFound concludes a floor test that found the floor where it was. The
// pause has drained the queue; the estimate is lowered as a repeated signal
// lowers it, or the queue forms again at once. Swallowed by the test instead,
// the signals of two model lanes with an estimate a quarter too high left
// 40 ms standing in the path.
func (p *lane) queueFound() {
	c := &p.control
	c.capacity *= capacityDecay
	p.rate = math.Max(minimumRate, math.Min(p.rate, capacityHold*c.capacity))
	p.floorTestEvery = min(maxFloorTestInterval, 2*max(floorTestInterval, p.floorTestEvery))
}

// floorMoved concludes a floor test that found the path's latency at a new
// level: the floor is measured from here.
func (p *lane) floorMoved(sample time.Duration) {
	p.rebaseline(sample)
	clear(p.transitBases[:])
	p.floorTestEvery = floorTestInterval
}

// floorTestPause is how long bulk must pause for a queue of the given delay to
// drain, if a pause short enough does that. The higher classes keep sending,
// so the queue drains at the share of the lane that bulk gives up.
func (p *lane) floorTestPause(queued time.Duration) (pause time.Duration, short bool) {
	bulk := p.allowed(classBulk)
	if bulk <= 0 {
		return 0, false
	}
	needed := drainQueues * float64(queued) * p.rate / bulk
	if needed > float64(baselineDrain) {
		return 0, false
	}
	return time.Duration(needed), true
}

// floorTestable reports that the delay needs explaining and that pausing bulk
// would explain it. A target above the estimate, or a probe's queue that may
// still be in the path, explains it already. The last test must not be
// recent, and the pause must be short: on a lane that mostly carries the
// higher classes a pause drains little (a 0.4 Mbit/s lane with voice on 70%
// of it kept its queue through the pause, and the queue became the floor:
// `TestVoiceSurvivesOnSingleSlowLane`).
func (p *lane) floorTestable(now time.Time, queued time.Duration) bool {
	c := &p.control
	_, short := p.floorTestPause(queued)
	return short && p.rate <= c.capacity && !now.Before(c.drainBy) &&
		now.Sub(p.floorTested) >= max(floorTestInterval, p.floorTestEvery)
}

// polices reports that the path answered with material loss lately: it
// drops what it cannot forward instead of queueing it.
func (p *lane) polices(now time.Time) bool {
	return !p.droppedAt.IsZero() && now.Sub(p.droppedAt) < policingMemory
}

// pulseSettled reports that loss the last pulse may have caused would have
// shown by now. On a path that polices that takes thirty datagrams more. The
// next pulse does not wait for them, and below about 170 kB/s every pulse
// replaced the last before it counted: a lane left with a low estimate rose
// by a twentieth per pulse and never returned to discovery
// (`TestProbeWinsCountOnASlowLane`). A path that has shown no loss answers
// with delay, which the pulse's verdict already waited for.
func (p *lane) pulseSettled(now time.Time) bool {
	return now.Sub(p.control.wonAt) >= lossSettle+p.rtt && (p.seq-p.control.wonSeq >= pulseConfirmDatagrams || !p.polices(now))
}

func (p *lane) holdOrPulse(now time.Time, laneLimited bool) {
	c := &p.control
	limit := capacityHold * c.capacity
	switch {
	case c.awaitingVerdict() && now.Before(c.pulseEnd):
	case c.awaitingVerdict() && now.Before(c.verdictAt):
		p.rate = limit
	case c.awaitingVerdict() && now.Sub(c.verdictAt) < verdictExpiry:
		// The pulse's feedback arrived without a congestion signal. The
		// estimate rises now; the win counts, and the next pulse follows,
		// once loss the pulse may have caused would have shown.
		c.pending, c.wonAt, c.wonSeq, c.wonFrom = true, now, p.seq, c.capacity
		c.capacity *= 1 + (pulseGain-1)*float64(int(1)<<c.pulseWins)
		p.schedulePulse(now)
		c.nextPulse = now.Add(lossSettle + p.rtt)
		p.rate = capacityHold * c.capacity
	case c.awaitingVerdict():
		p.schedulePulse(now)
	case c.pending && p.pulseSettled(now):
		// No loss followed the pulse: it is won.
		p.decisions.PulseWins++
		c.pending, c.pulseEvery = false, 0
		if c.pulseWins++; c.pulseWins >= pulseWinsToLeave {
			p.decisions.Rediscoveries++
			c.pulseWins = 0
			p.schedulePulse(now)
			p.startup, p.startupBest, p.startupFlatRounds, p.discoveryGain = true, 0, 0, rediscoveryGain
			return
		}
		if p.rate >= limit && laneLimited && p.sendRate >= startupPlateauSending*limit {
			p.pulse(now)
		}
	case !now.Before(c.nextPulse) && p.rate >= limit && laneLimited && p.sendRate >= startupPlateauSending*limit:
		// A pulse on a lane that sends less than its target tests nothing,
		// and its silence would raise the estimate without evidence.
		p.pulse(now)
	case p.rate < limit:
		p.rate = math.Min(limit, p.rate*1.06+1500)
	}
}

// pulse raises the target above the estimate for long enough to build a queue
// the delay signal can detect if the estimate is the path's capacity.
func (p *lane) pulse(now time.Time) {
	p.decisions.Pulses++
	c := &p.control
	length := time.Duration(pulseQueueFactor / pulseExcess * float64(p.congestionThreshold()))
	length = min(maxPulse, max(minPulse, length))
	c.pulseEnd = now.Add(length)
	c.verdictAt = c.pulseEnd.Add(p.rtt + p.peerACKInterval())
	p.rate = math.Min(maximumRate, (1+pulseExcess)*c.capacity)
}

// discover follows measured delivery at the pace of a sender's slow start
// until the first congestion signal or a delivery plateau.
func (p *lane) discover(now time.Time, realtime, stream, laneLimited bool) {
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
			p.decisions.DiscoveryPlateau++
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
	if p.boundByDelivery(now, stream) {
		return
	}
	if carried := math.Max(delivered, p.sendRate); carried > 0 {
		// While datagrams wait for the lane, measured delivery replaces the
		// initial assumption: on a lane slower than it, the window sized from
		// it admits a queue of hundreds of milliseconds. A sparse sender's
		// delivery says nothing about the lane.
		floor := initialRate
		if laneLimited {
			floor = minimumRate
		}
		p.rate = math.Min(p.rate, math.Max(floor, gain*carried))
	}
}

// lossLedger measures a lane's loss from byte counts: the bytes the sender put
// on the lane up to an acknowledged sequence, against the bytes the receiver
// reports having received on it. A timeout cannot tell a lost datagram from a
// late one; with latency that wanders, datagrams that arrived were timed out,
// repaired and counted as lost (VM run of 2026-09-30: 284 loss cuts in one
// transfer over links that dropped nothing).
//
// The deficit of received against sent bytes is the loss so far plus the
// datagrams below the acknowledged sequence that have not arrived yet. That
// backlog is never negative and empties as they arrive, so the lowest deficit
// over a settling period is the loss, and its growth from one period to a
// later one is the loss in between.
type lossLedger struct {
	entries []lossEntry
}

type lossEntry struct {
	at      time.Time
	seq     uint64
	sent    uint64
	deficit float64
}

const (
	lossHorizon = time.Second
	lossSettle  = 200 * time.Millisecond
	// Three lost datagrams in a hundred and fifty are the material loss that
	// counts; a lane too slow to send that many in a second is judged over
	// longer, up to this.
	maxLossHorizon = 4 * time.Second
)

// record adds the counts confirmed with the acknowledgement of seq; late is
// what the sender believes is still on its way below seq.
func (l *lossLedger) record(now time.Time, seq, sent, received, late uint64) {
	for len(l.entries) > 0 && now.Sub(l.entries[0].at) >= maxLossHorizon+2*lossSettle {
		l.entries = l.entries[1:]
	}
	if n := len(l.entries); n > 0 && seq <= l.entries[n-1].seq {
		return
	}
	l.entries = append(l.entries, lossEntry{now, seq, sent, float64(sent) - float64(received) - float64(late)})
}

// settled is the loss so far, in bytes: the lowest deficit of the current
// settling period. It is meaningful only as a difference.
func (l *lossLedger) settled(now time.Time) (deficit float64, known bool) {
	for i := range l.entries {
		if e := l.entries[i]; now.Sub(e.at) < lossSettle && (!known || e.deficit < deficit) {
			deficit, known = e.deficit, true
		}
	}
	return deficit, known
}

// lost is the loss, in bytes, between the settling period a horizon ago and
// the current one, with the bytes and datagrams sent in between. The horizon
// is a second, or as much longer as it takes the lane to send enough
// datagrams for three lost ones to mean two percent (a 45-datagram-a-second
// lane lost 5% for good with a one-second horizon: three losses a second was
// 6.7% of it).
func (l *lossLedger) lost(now time.Time) (bytes, sent, datagrams float64) {
	var recent *lossEntry
	for i := range l.entries {
		if e := &l.entries[i]; now.Sub(e.at) < lossSettle && (recent == nil || e.deficit < recent.deficit) {
			recent = e
		}
	}
	if recent == nil {
		return 0, 0, 0
	}
	const judged = startupLossEvents / startupLossRatio
	for horizon := lossHorizon; horizon <= maxLossHorizon; horizon += lossSettle {
		var old *lossEntry
		for i := range l.entries {
			e := &l.entries[i]
			if age := now.Sub(e.at); age >= horizon && age < horizon+lossSettle && (old == nil || e.deficit < old.deficit) {
				old = e
			}
		}
		if old == nil || recent.seq <= old.seq {
			continue
		}
		if datagrams = float64(recent.seq - old.seq); datagrams >= judged || horizon+lossSettle > maxLossHorizon {
			return math.Max(0, recent.deficit-old.deficit), float64(recent.sent - old.sent), datagrams
		}
	}
	return 0, 0, 0
}

// material reports loss of at least three datagrams and 2% of what was sent
// over the horizon (BBRv2's threshold). A path that loses a fraction of a
// percent at random loses something in every control interval at a high rate;
// repair covers that, and only material loss is congestion.
func (l *lossLedger) material(now time.Time) bool {
	lost, sent, datagrams := l.lost(now)
	return datagrams > 0 && lost >= startupLossEvents*sent/datagrams && lost >= startupLossRatio*sent
}

const (
	sustainedSpan   = 500 * time.Millisecond
	sustainedMemory = 500 * time.Millisecond // one of the peak's five buckets
)

// sustainedDelivery remembers the highest delivery a lane kept up over
// sustainedSpan in the last few seconds, from the receiver's byte counts.
//
// Delivery measured over a control interval follows the path down and back
// when it slows for a few hundred milliseconds. An estimate of capacity taken
// from it at that moment is low by as much, and the target then holds below
// it (`TestSlowdownDoesNotLowerTheEstimate`). What the lane kept up over a
// longer span shortly before is still true of it; if its capacity did fall,
// that memory lapses within three seconds.
type sustainedDelivery struct {
	marks []deliveryMark
	best  peak
}

// deliveryMark is the receiver's count of bytes at its elapsed time.
type deliveryMark struct {
	bytes, elapsed uint64
}

func (d *sustainedDelivery) record(now time.Time, bytes, elapsed uint64) {
	d.marks = append(d.marks, deliveryMark{bytes, elapsed})
	// Keep one mark at least a span old to measure from.
	for len(d.marks) > 1 && elapsed-d.marks[1].elapsed >= uint64(sustainedSpan) {
		d.marks = d.marks[1:]
	}
	if oldest := d.marks[0]; elapsed-oldest.elapsed >= uint64(sustainedSpan) {
		d.best.record(now, float64(bytes-oldest.bytes)/time.Duration(elapsed-oldest.elapsed).Seconds())
	}
}

func (d *sustainedDelivery) value(now time.Time) float64 { return d.best.value(now) }

const (
	peakBucket  = 2 * time.Second
	peakBuckets = 5
)

// peak reports the highest of the values recorded in the last five buckets
// of two seconds, or of the bucket length set.
type peak struct {
	bucket  time.Duration
	start   time.Time
	buckets [peakBuckets]float64
	index   int
}

func (m *peak) length() time.Duration {
	if m.bucket == 0 {
		return peakBucket
	}
	return m.bucket
}

func (m *peak) rotate(now time.Time) {
	bucket := m.length()
	if m.start.IsZero() || now.Sub(m.start) >= peakBuckets*bucket {
		*m = peak{bucket: m.bucket, start: now}
		return
	}
	for now.Sub(m.start) >= bucket {
		m.start = m.start.Add(bucket)
		m.index = (m.index + 1) % peakBuckets
		m.buckets[m.index] = 0
	}
}

func (m *peak) record(now time.Time, value float64) {
	m.rotate(now)
	m.buckets[m.index] = max(m.buckets[m.index], value)
}

func (m *peak) value(now time.Time) float64 {
	m.rotate(now)
	var highest float64
	for _, bucket := range m.buckets {
		highest = max(highest, bucket)
	}
	return highest
}

// boundByDelivery holds the target of a discovering lane that carries a
// real-time stream within the discovery gain of the most it delivered
// recently, and reports whether it applied. What the lane was offered is no
// evidence of what it can carry, nor is the initial assumption: a target
// inflated while the lane was lightly loaded overfills the path when another
// lane fails and its traffic arrives here (VM runs of 2026-09-29: targets of
// 81-97 kB/s on a 62.5 kB/s lane, 300-390 ms voice round trips after the
// failure). A burst delivers too little to measure anything.
//
// Other lanes are not bound: their overshoot delays no real-time datagram,
// and a bound that followed a paused sender's delivery down left bulk to
// start again from nothing (VM run 20260929-234339-continuity: no TCP
// delivery in 65 seconds).
//
// It applies to every acknowledgement: a lightly loaded lane is never
// backlogged, and the control interval does not run for it.
func (p *lane) boundByDelivery(now time.Time, stream bool) bool {
	if !p.startup || !stream || p.reserved[classRealtime] == 0 {
		return false
	}
	delivered := p.recentDelivery.value(now)
	if delivered == 0 {
		return false
	}
	p.rate = math.Min(p.rate, math.Max(minimumRate, rediscoveryGain*delivered))
	return true
}
