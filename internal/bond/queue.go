package bond

import (
	"math"
	"time"
)

const (
	codelMinTarget   = 10 * time.Millisecond
	codelMaxTarget   = 100 * time.Millisecond
	codelMinInterval = 100 * time.Millisecond
)

// codel is the RFC 8289 controlled-delay drop schedule for the bulk queue. It
// drops at dequeue only after sojourn stays above target for an interval, then
// spaces drops by interval/sqrt(count), so a burst or rate cut costs each TCP
// flow one congestion signal instead of a contiguous loss run.
//
// The target is the slowest lane's round trip rather than RFC 8289's 5 ms: a
// loss-based sender cuts its window by 30%, and only a standing queue of at
// least 0.43 of the sender's round trip, which includes resequencing and
// acknowledgement delay, keeps the lanes busy afterwards (VM trace
// 20260929-134042-fast-up-d1). Small datagrams bypass this queue, so the
// deeper target does not delay them.
type codel struct {
	target     time.Duration
	interval   time.Duration
	firstAbove time.Time
	dropNext   time.Time
	count      int
	lastCount  int
	dropping   bool
}

func (c *codel) okToDrop(now time.Time, sojourn time.Duration, remaining int) bool {
	if sojourn < c.target || remaining == 0 {
		c.firstAbove = time.Time{}
		return false
	}
	if c.firstAbove.IsZero() {
		c.firstAbove = now.Add(c.interval)
		return false
	}
	return !now.Before(c.firstAbove)
}

// drop reports whether the dequeued packet must be discarded instead of sent.
func (c *codel) drop(now time.Time, sojourn time.Duration, remaining int, roundTrip time.Duration) bool {
	c.target = min(codelMaxTarget, max(codelMinTarget, roundTrip))
	c.interval = max(codelMinInterval, 2*roundTrip)
	ok := c.okToDrop(now, sojourn, remaining)
	if c.dropping {
		if !ok {
			c.dropping = false
			return false
		}
		if now.Before(c.dropNext) {
			return false
		}
		c.count++
		c.dropNext = c.controlLaw(c.dropNext)
		return true
	}
	if !ok {
		return false
	}
	c.dropping = true
	if delta := c.count - c.lastCount; delta > 1 && now.Sub(c.dropNext) < 16*c.interval {
		c.count = delta
	} else {
		c.count = 1
	}
	c.lastCount = c.count
	c.dropNext = c.controlLaw(now)
	return true
}

func (c *codel) controlLaw(t time.Time) time.Time {
	return t.Add(time.Duration(float64(c.interval) / math.Sqrt(float64(c.count))))
}

type packetQueue interface {
	peek() *packet
	pop()
}

type packetFIFO []*packet

func (q *packetFIFO) peek() *packet {
	if len(*q) == 0 {
		return nil
	}
	return (*q)[0]
}

func (q *packetFIFO) pop() {
	(*q)[0] = nil
	*q = (*q)[1:]
}

type packetFlow struct {
	id       FlowID
	packets  packetFIFO
	next     *packetFlow
	previous *packetFlow
}

// The active-flow ring preserves FIFO within each flow and rotates after every
// small datagram. A burst in one flow cannot occupy another flow's turn.
type fairPacketQueue struct {
	flows map[FlowID]*packetFlow
	head  *packetFlow
	count int
}

func (q *fairPacketQueue) push(p *packet) (coalesced bool) {
	if q.flows == nil {
		q.flows = make(map[FlowID]*packetFlow)
	}
	flow := q.flows[p.flow]
	if flow == nil {
		flow = &packetFlow{id: p.flow}
		q.flows[p.flow] = flow
		if q.head == nil {
			flow.next, flow.previous = flow, flow
			q.head = flow
		} else {
			flow.next, flow.previous = q.head, q.head.previous
			q.head.previous.next = flow
			q.head.previous = flow
		}
	}
	if n := len(flow.packets); n >= 2 && p.flow != (FlowID{}) {
		previous, last := flow.packets[n-2].ack, flow.packets[n-1].ack
		// Keep an ACK clock and all duplicate/control ACKs. Only replace the
		// middle of three strictly advancing cumulative ACKs. The newest ACK
		// also supersedes the advertised window; window-only updates stay.
		if last.supersedes(previous) && p.ack.supersedes(last) {
			flow.packets[n-1] = p
			return true
		}
	}
	flow.packets = append(flow.packets, p)
	q.count++
	return false
}

func (ack TCPACK) supersedes(old TCPACK) bool {
	return ack.Eligible && old.Eligible && ack.Sequence == old.Sequence &&
		ack.Window != 0 && old.Window != 0 && ack.TrafficClass == old.TrafficClass &&
		int32(ack.Acknowledgement-old.Acknowledgement) > 0 &&
		ack.Timestamp == old.Timestamp && (!ack.Timestamp ||
		(int32(ack.TSVal-old.TSVal) >= 0 && int32(ack.TSEcr-old.TSEcr) >= 0))
}

func (q *fairPacketQueue) peek() *packet {
	if q.head == nil {
		return nil
	}
	return q.head.packets.peek()
}

func (q *fairPacketQueue) pop() {
	flow := q.head
	flow.packets.pop()
	q.count--
	q.head = flow.next
	if len(flow.packets) == 0 {
		delete(q.flows, flow.id)
		if flow.next == flow {
			q.head = nil
		} else {
			flow.previous.next = flow.next
			flow.next.previous = flow.previous
		}
	}
}

const (
	waitWindowLength = 100 * time.Millisecond
	standingWait     = time.Millisecond
)

// waitWindow reports whether bulk datagrams consistently waited for a lane.
// A standing wait means the lanes, not the sender, limit what is carried.
type waitWindow struct {
	start             time.Time
	current, previous time.Duration
	filled, primed    bool
}

func (w *waitWindow) rotate(now time.Time) {
	for w.start.IsZero() || now.Sub(w.start) >= waitWindowLength {
		if w.start.IsZero() || now.Sub(w.start) >= 2*waitWindowLength {
			w.start, w.primed, w.filled = now, false, false
			return
		}
		w.start = w.start.Add(waitWindowLength)
		w.previous, w.primed = w.current, w.filled
		w.filled = false
	}
}

func (w *waitWindow) observe(now time.Time, wait time.Duration) {
	w.rotate(now)
	if !w.filled || wait < w.current {
		w.current, w.filled = wait, true
	}
}

func (w *waitWindow) standing(now time.Time) bool {
	w.rotate(now)
	return w.primed && w.previous >= standingWait && (!w.filled || w.current >= standingWait)
}
