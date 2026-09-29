package bond

import (
	"math"
	"time"
)

const (
	codelTarget   = 5 * time.Millisecond
	codelInterval = 100 * time.Millisecond
)

// codel is the RFC 8289 controlled-delay drop schedule for the bulk queue. It
// drops at dequeue only after sojourn stays above target for an interval, then
// spaces drops by interval/sqrt(count), so a burst or rate cut costs each TCP
// flow one congestion signal instead of a contiguous loss run.
type codel struct {
	firstAbove time.Time
	dropNext   time.Time
	count      int
	lastCount  int
	dropping   bool
}

func (c *codel) okToDrop(now time.Time, sojourn time.Duration, remaining int) bool {
	if sojourn < codelTarget || remaining == 0 {
		c.firstAbove = time.Time{}
		return false
	}
	if c.firstAbove.IsZero() {
		c.firstAbove = now.Add(codelInterval)
		return false
	}
	return !now.Before(c.firstAbove)
}

// drop reports whether the dequeued packet must be discarded instead of sent.
func (c *codel) drop(now time.Time, sojourn time.Duration, remaining int) bool {
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
		c.dropNext = codelControlLaw(c.dropNext, c.count)
		return true
	}
	if !ok {
		return false
	}
	c.dropping = true
	if delta := c.count - c.lastCount; delta > 1 && now.Sub(c.dropNext) < 16*codelInterval {
		c.count = delta
	} else {
		c.count = 1
	}
	c.lastCount = c.count
	c.dropNext = codelControlLaw(now, c.count)
	return true
}

func codelControlLaw(t time.Time, count int) time.Time {
	return t.Add(time.Duration(float64(codelInterval) / math.Sqrt(float64(count))))
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
