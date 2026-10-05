package bond

import (
	"sort"
	"time"
)

// class orders traffic for transmission. Small datagrams that are not TCP are
// treated as real-time (voice, DNS, WireGuard keepalives): they need more than
// an equal share of a slow lane, so equal turns with a TCP ACK stream lose
// them. Small TCP datagrams, mostly ACKs, come next and bulk last. This is a
// size and protocol heuristic, not an application classifier.
type class uint8

const (
	classRealtime class = iota
	classSmall
	classBulk
	classes
)

func classify(length int, metadata PacketMetadata) class {
	switch {
	case length > smallPacket:
		return classBulk
	case metadata.Flow[1] == ipProtocolTCP || metadata.ACK.Eligible:
		return classSmall
	default:
		return classRealtime
	}
}

const (
	meterBucket  = 200 * time.Millisecond
	meterBuckets = 5
)

// rateMeter reports the highest rate of any recent bucket, so a reservation
// outlasts the gaps between a sparse flow's datagrams.
type rateMeter struct {
	start   time.Time
	buckets [meterBuckets]float64
	index   int
}

func (m *rateMeter) rotate(now time.Time) {
	if m.start.IsZero() || now.Sub(m.start) >= meterBuckets*meterBucket {
		*m = rateMeter{start: now}
		return
	}
	for now.Sub(m.start) >= meterBucket {
		m.start = m.start.Add(meterBucket)
		m.index = (m.index + 1) % meterBuckets
		m.buckets[m.index] = 0
	}
}

func (m *rateMeter) add(now time.Time, bytes float64) {
	m.rotate(now)
	m.buckets[m.index] += bytes
}

func (m *rateMeter) rate(now time.Time) float64 {
	m.rotate(now)
	var highest float64
	for _, bucket := range m.buckets {
		highest = max(highest, bucket)
	}
	return highest / meterBucket.Seconds()
}

// steady reports that every recent bucket carried traffic: a stream, not a
// burst.
func (m *rateMeter) steady(now time.Time) bool {
	m.rotate(now)
	for i, bucket := range m.buckets {
		if bucket == 0 && i != m.index {
			return false
		}
	}
	return true
}

const standingWait = 5 * time.Millisecond

// laneLimited reports whether a queued datagram has waited for a lane. A
// sender that offers less than the lanes carry has its datagrams taken within
// a poll; a standing wait means the lanes are the limit.
func (t *Transport) laneLimited(now time.Time) bool {
	for _, head := range []*packet{t.small[classRealtime].peek(), t.small[classSmall].peek(), t.queue.peek()} {
		if head != nil && now.Sub(head.created) >= standingWait {
			return true
		}
	}
	return false
}

func (t *Transport) queued() int {
	return len(t.queue) + t.small[classRealtime].count + t.small[classSmall].count
}

// reserve assigns the measured demand of each small class to the lanes in
// order of round trip. Lower classes are paced to leave that capacity free:
// a lane filled by bulk otherwise sends voice to a slower lane.
//
// Small TCP datagrams are mostly the ACK stream of a transfer in the other
// direction, which can be coalesced; bulk cannot. While bulk waits, that class
// is held to half of what real-time traffic leaves, and bulk gets the rest.
// A lane on which real-time traffic has reserved more than half the capacity
// is the exception: each bulk datagram occupies a slow lane for tens of
// milliseconds, so bulk keeps only its guaranteed minimum there.
//
// Each lower class keeps a minimum share so a flood above it cannot take every
// transmission slot. One lane guarantees it, the lane with the most capacity
// left for the class: a bulk datagram occupies a slow lane for tens of
// milliseconds, so the guarantee must not sit on the lane voice prefers
// while another lane has room.
func (t *Transport) reserve(now time.Time) {
	lanes := make([]*lane, 0, len(t.paths))
	bulkWaiting := len(t.queue) > 0
	for _, p := range t.paths {
		p.reserved, p.guaranteed, p.shared, p.copies, p.bulkElsewhere = [classBulk]float64{}, [classes]bool{}, bulkWaiting, 0, false
		if p.eligible(classRealtime, now) {
			lanes = append(lanes, p)
		}
	}
	if len(lanes) == 0 {
		return
	}
	sort.SliceStable(lanes, func(i, j int) bool { return lanes[i].latency() < lanes[j].latency() })
	free := make([]float64, len(lanes))
	for i, p := range lanes {
		free[i] = p.rate
	}
	for c := classRealtime; c < classBulk; c++ {
		// The lane with the most room left guarantees the next class.
		guarantor := -1
		for i, p := range lanes {
			if !p.eligible(c+1, now) {
				continue
			}
			if guarantor < 0 || free[i]-t.assignable(now, c, lanes, free, i) > free[guarantor]-t.assignable(now, c, lanes, free, guarantor) {
				guarantor = i
			}
		}
		need := reservationHeadroom * t.demand[c].rate(now)
		for i, p := range lanes {
			if !p.eligible(c, now) {
				continue
			}
			room := free[i]
			if i == guarantor {
				room -= float64(classBulk-c) * minimumClassShare * p.rate
			}
			if c == classSmall && bulkWaiting && !p.realtimeLane() {
				room = min(room, smallClassShare*free[i])
			}
			p.reserved[c] = max(0, min(need, room))
			need -= p.reserved[c]
			free[i] -= p.reserved[c]
		}
		if guarantor >= 0 {
			lanes[guarantor].guaranteed[c+1] = true
		}
		if c == classRealtime {
			t.reserveCopies(now, lanes, free)
			quiet := 0
			for _, p := range lanes {
				if p.eligible(classBulk, now) && p.reserved[classRealtime] == 0 {
					quiet++
				}
			}
			for _, p := range lanes {
				p.bulkElsewhere = quiet > 0 && p.reserved[classRealtime] > 0
			}
		}
	}
}

// reserveCopies sets capacity aside for the copies of real-time datagrams. A
// copy travels on another lane than its original, so each lane reserves for
// the originals the other lanes carry. Without it lower classes fill that
// lane's window and pacing slots, and the copy, which is sent with its
// original or not at all, finds no room.
func (t *Transport) reserveCopies(now time.Time, lanes []*lane, free []float64) {
	var originals, suspect float64
	for _, p := range lanes {
		originals += p.reserved[classRealtime]
		if p.liveness(now) == LaneSuspect {
			suspect += p.reserved[classRealtime]
		}
	}
	if originals == 0 || len(lanes) < 2 {
		return
	}
	need := min(t.copyBudget(now), originals)
	for i, p := range lanes {
		forced := suspect
		if p.liveness(now) == LaneSuspect {
			forced -= p.reserved[classRealtime]
		}
		p.copies = min(free[i], max(forced, need*(1-p.reserved[classRealtime]/originals)))
		free[i] -= p.copies
	}
}

// assignable estimates what the class would reserve on a lane if demand were
// assigned in round-trip order without a guarantee.
func (t *Transport) assignable(now time.Time, c class, lanes []*lane, free []float64, lane int) float64 {
	need := reservationHeadroom * t.demand[c].rate(now)
	for i := 0; i < lane; i++ {
		if lanes[i].eligible(c, now) {
			need -= min(need, free[i])
		}
	}
	return min(need, free[lane])
}

// latency ranks lanes for small datagrams by unloaded round trip and jitter.
// The loaded round trip would rank a lane lower for carrying the traffic
// that prefers it.
func (p *lane) latency() time.Duration {
	return p.idleRTT + jitterAllowanceFactor*p.idleRTTVariation
}

// realtimeLane reports that bulk keeps off the lane, to its minimum share at
// most, because real-time datagrams travel on it.
//
// A lane whose capacity was never found is not one: its target is not what it
// carries. Beside a real-time stream that target is held near recent
// delivery, and judged by it the stream alone took most of the lane, bulk
// kept its minimum share, delivery could not grow and neither could the
// target (`TestBulkBesideVoiceDiscoversTheOnlyLane`). Bulk discovers the lane
// first, at the pace that bound allows.
//
// While another lane can take the bulk, it then holds for a lane on which one
// full datagram ahead of a real-time one delays it by more than the queue the
// lane is allowed: at 0.5 Mbit/s a bulk datagram is 24 ms of serialization,
// for a third of a megabit of throughput.
func (p *lane) realtimeLane() bool {
	if p.startup && p.control.capacity == 0 {
		return false
	}
	return p.bulkElsewhere && fullDatagramWireBytes/p.rate*float64(time.Second) > float64(targetQueue)
}

// allowed is the rate a class may use on the lane: what higher classes have
// not reserved, and at least the minimum share on the lane that guarantees it.

func (p *lane) allowed(c class) float64 {
	if c == classBulk && p.realtimeLane() {
		if p.guaranteed[c] {
			return minimumClassShare * p.rate
		}
		return 0
	}
	if c == classSmall && p.shared && !p.realtimeLane() {
		rate := p.reserved[classSmall]
		if p.guaranteed[c] {
			rate = max(rate, minimumClassShare*p.rate)
		}
		return rate
	}
	rate := p.rate
	for higher := classRealtime; higher < c; higher++ {
		rate -= p.reserved[higher]
	}
	if c > classRealtime {
		rate -= p.copies
	}
	for lower := c + 1; lower < classes; lower++ {
		if p.guaranteed[lower] {
			rate -= minimumClassShare * p.rate
		}
	}
	if p.guaranteed[c] {
		rate = max(rate, minimumClassShare*p.rate)
	}
	return max(0, rate)
}

// chooseLane returns the lane on which a datagram of the class would arrive
// first, or nil while no lane may send it. exclude names the lane of an
// earlier attempt: a copy must avoid it, and a retry prefers to.
func (t *Transport) chooseLane(now time.Time, c class, size int, exclude PathID, avoid bool) *lane {
	var best *lane
	var bestArrival time.Duration
	contended := t.small[classRealtime].count+t.small[classSmall].count > 0
	leased := 0
	if c == classRealtime {
		for _, p := range t.paths {
			if p.up(now) {
				leased++
			}
		}
	}
	for _, p := range t.paths {
		if !p.eligible(c, now) || now.Before(p.drainUntil) && c == classBulk || avoid && p.id == exclude {
			continue
		}
		if now.Before(p.control.flushUntil) && c != classRealtime {
			continue
		}
		window := p.window()
		// Small datagrams may lead the pacing clock. Bulk competing with them
		// for the same slots needs the same lead to receive its share.
		lead := priorityLead
		if c == classBulk && !contended {
			lead = 0
		}
		classLead := lead
		if c == classRealtime {
			// A small datagram may borrow one datagram beyond a full window.
			// Within the share of the window reserved for them, real-time
			// datagrams are not blocked by the bytes of lower classes: on a
			// slow lane one bulk datagram in flight is a third of the window.
			if p.inflight > window && float64(p.classInflight[c]) > float64(window)*(p.reserved[c]+p.copies)/p.rate {
				continue
			}
			// Voice may borrow one slot; a lower class in flight can occupy
			// a full datagram's slot. Its bytes still advance the shared clock.
			borrow := size
			if leased == 1 || p.classInflight[classBulk] > 0 || p.classInflight[classSmall] > 0 || avoid && p.copies > 0 {
				borrow = fullDatagramWireBytes
			}
			lead += time.Duration(float64(borrow) / p.rate * float64(time.Second))
		} else if allowed := p.allowed(c); allowed == 0 || p.inflight+size > max(size, window) ||
			p.classInflight[c]+size > p.classWindow(c, size, window, allowed) {
			// A class is held to its own share of the window: the bytes of
			// higher classes in flight must not shut it out.
			continue
		}
		if p.classNext[c].After(now.Add(classLead)) || p.nextSend.After(now.Add(lead)) {
			continue
		}
		arrival := max(0, p.nextSend.Sub(now)) + p.latency()/2
		if best == nil || arrival < bestArrival {
			best, bestArrival = p, arrival
		}
	}
	return best
}

func (p *lane) classWindow(c class, size, window int, allowed float64) int {
	share := max(size, int(float64(window)*allowed/p.rate))
	if c == classBulk && p.reserved[classRealtime] > 0 &&
		(p.control.capacity > 0 || p.reserved[classRealtime]+p.copies > allowed) &&
		float64(size)/p.rate*float64(time.Second) > float64(targetQueue) {
		return size
	}
	return share
}

// stale reports that a waiting real-time datagram should give way to the ones
// behind it. A backlog formed when a lane failed drains only as fast as the
// remaining capacity exceeds the flow's rate, which on a slow lane takes
// seconds, and every datagram meanwhile arrives late by the backlog. Once
// datagrams have waited longer than the target for a whole interval, the
// oldest are dropped until the queue is current again.
func (t *Transport) stale(now time.Time, head *packet) bool {
	if now.Sub(head.created) <= realtimeQueueTarget {
		t.realtimeLate, t.realtimeSkipping = time.Time{}, false
		return false
	}
	if t.realtimeSkipping {
		return true
	}
	if t.realtimeLate.IsZero() {
		t.realtimeLate = now
	}
	t.realtimeSkipping = now.Sub(t.realtimeLate) >= realtimeQueueInterval
	return t.realtimeSkipping
}

func (t *Transport) send(now time.Time, out []Transmission) []Transmission {
	for c := classRealtime; c < classBulk; c++ {
		queue := &t.small[c]
		if c == classRealtime && queue.peek() == nil {
			// An empty queue ends the backlog, however late its last
			// datagram left.
			t.realtimeLate, t.realtimeSkipping = time.Time{}, false
		}
		for p := queue.peek(); p != nil; p = queue.peek() {
			expired := now.After(p.queueDeadline)
			if expired || c == classRealtime && t.stale(now, p) {
				queue.pop()
				t.drops++
				t.interactiveDrops++
				if expired {
					t.smallQueueDrops[c].Deadline++
				} else {
					t.smallQueueDrops[c].Stale++
				}
				continue
			}
			size := len(p.payload) + wireOverhead
			path := t.chooseLane(now, c, size, 0, false)
			if path == nil {
				break
			}
			queue.pop()
			out = append(out, t.transmit(p, path, now))
			forced := path.liveness(now) == LaneSuspect
			if c == classRealtime && (forced || t.redundancyTokens >= float64(size)) {
				if second := t.chooseLane(now, c, size, path.id, true); second != nil {
					if !forced {
						t.redundancyTokens -= float64(size)
					}
					out = append(out, t.transmit(p, second, now))
				}
			}
		}
	}
	for p := t.queue.peek(); p != nil; p = t.queue.peek() {
		if now.After(p.queueDeadline) {
			t.queue.pop()
			t.drops++
			continue
		}
		path := t.chooseLane(now, classBulk, len(p.payload)+wireOverhead, 0, false)
		if path == nil {
			break
		}
		t.queue.pop()
		if !t.discovering(now) && t.bulkAQM.drop(now, now.Sub(p.created), len(t.queue), t.slowestRoundTrip(now)) {
			t.drops++
			t.aqmDrops++
			continue
		}
		out = append(out, t.transmit(p, path, now))
	}
	return out
}
