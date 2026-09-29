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
// Each lower class keeps a minimum share so a flood above it cannot take every
// transmission slot. One lane guarantees it, the lane with the most capacity
// left for the class: a bulk datagram occupies a slow lane for tens of
// milliseconds, so the guarantee must not sit on the lane voice prefers
// while another lane has room.
func (t *Transport) reserve(now time.Time) {
	lanes := make([]*lane, 0, len(t.paths))
	for _, p := range t.paths {
		p.reserved, p.guaranteed = [classBulk]float64{}, [classes]bool{}
		if p.up(now) && !p.stalled {
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
		guarantor := 0
		for i := range lanes {
			if free[i]-t.assignable(now, c, free, i) > free[guarantor]-t.assignable(now, c, free, guarantor) {
				guarantor = i
			}
		}
		need := reservationHeadroom * t.demand[c].rate(now)
		for i, p := range lanes {
			room := free[i]
			if i == guarantor {
				room -= float64(classBulk-c) * minimumClassShare * p.rate
			}
			p.reserved[c] = max(0, min(need, room))
			need -= p.reserved[c]
			free[i] -= p.reserved[c]
		}
		lanes[guarantor].guaranteed[c+1] = true
	}
}

// assignable estimates what the class would reserve on a lane if demand were
// assigned in round-trip order without a guarantee.
func (t *Transport) assignable(now time.Time, c class, free []float64, lane int) float64 {
	need := reservationHeadroom * t.demand[c].rate(now)
	for i := 0; i < lane; i++ {
		need -= min(need, free[i])
	}
	return min(need, free[lane])
}

// latency ranks lanes for small datagrams by unloaded round trip and jitter.
// The loaded round trip would rank a lane lower for carrying the traffic
// that prefers it.
func (p *lane) latency() time.Duration {
	return p.idleRTT + jitterAllowanceFactor*p.idleRTTVariation
}

// allowed is the rate a class may use on the lane: what higher classes have
// not reserved, and at least the minimum share on the lane that guarantees it.
func (p *lane) allowed(c class) float64 {
	rate := p.rate
	for higher := classRealtime; higher < c; higher++ {
		rate -= p.reserved[higher]
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
	for _, p := range t.paths {
		if !p.up(now) || p.stalled || now.Before(p.drainUntil) && c == classBulk || avoid && p.id == exclude {
			continue
		}
		window := p.window()
		// Small datagrams may lead the pacing clock. Bulk competing with them
		// for the same slots needs the same lead to receive its share.
		lead := priorityLead
		if c == classBulk && !contended {
			lead = 0
		}
		if c == classRealtime {
			// A small datagram may borrow one datagram beyond a full window.
			if p.inflight > window {
				continue
			}
		} else if allowed := p.allowed(c); allowed == 0 || p.inflight+size > max(size, int(float64(window)*allowed/p.rate)) {
			continue
		}
		if p.classNext[c].After(now.Add(lead)) || p.nextSend.After(now.Add(lead)) {
			continue
		}
		arrival := max(0, p.nextSend.Sub(now)) + p.latency()/2
		if best == nil || arrival < bestArrival {
			best, bestArrival = p, arrival
		}
	}
	return best
}

func (t *Transport) send(now time.Time, out []Transmission) []Transmission {
	t.reserve(now)
	for c := classRealtime; c < classBulk; c++ {
		queue := &t.small[c]
		for p := queue.peek(); p != nil; p = queue.peek() {
			if now.After(p.queueDeadline) {
				queue.pop()
				t.drops++
				t.interactiveDrops++
				continue
			}
			size := len(p.payload) + wireOverhead
			path := t.chooseLane(now, c, size, 0, false)
			if path == nil {
				break
			}
			queue.pop()
			out = append(out, t.transmit(p, path, now))
			if c == classRealtime && t.redundancyTokens >= float64(size) {
				if second := t.chooseLane(now, c, size, path.id, true); second != nil {
					t.redundancyTokens -= float64(size)
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
