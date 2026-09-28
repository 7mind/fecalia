// Package bond implements a bounded, delivery-clocked multipath datagram transport.
// The owner supplies authenticated frames, monotonic time, and validated path changes.
package bond

import (
	"encoding/binary"
	"errors"
	"math"
	"time"

	"github.com/7mind/wanbond/internal/frame"
)

const (
	initialRate      = 125000.0
	minimumRate      = 16000.0
	maximumRate      = 1250000000.0
	targetQueue      = 10 * time.Millisecond
	maxQueueAge      = 100 * time.Millisecond
	maxPacketAge     = 250 * time.Millisecond
	ackInterval      = 25 * time.Millisecond
	ackBatchPackets  = 64
	priorityLead     = 5 * time.Millisecond
	pathLease        = time.Second
	minimumRTO       = 60 * time.Millisecond
	feedbackHorizon  = 2 * time.Second
	deliveryInterval = 50 * time.Millisecond
	baselineInterval = 10 * time.Second
	baselineDrain    = 200 * time.Millisecond
	baselineStagger  = 500 * time.Millisecond
	maxPackets       = 8192
	smallPacket      = 384
	redundancyRate   = 64000.0
	redundancyShare  = 0.1
	maxDatagram      = 9000
	wireOverhead     = 129 // outer CONTROL, adaptive header, sequences, IP and UDP
)

type Transmission struct {
	Path  PathID
	Frame frame.Control
}

type Delivery struct {
	Sequence    uint64
	Interactive bool
	Payload     []byte
}

type PathStats struct {
	Path         PathID
	Rate         float64
	DeliveryRate float64
	RTT          time.Duration
	BaseRTT      time.Duration
	QueueDelay   time.Duration
	InFlight     int
	Sent         uint64
	ACKed        uint64
	Retransmits  uint64
	Up           bool
}

type Snapshot struct {
	Paths      []PathStats
	QueueDrops uint64
	Expired    uint64
	Duplicates uint64
}

type packet struct {
	seq         uint64
	order       uint64
	interactive bool
	payload     []byte
	created     time.Time
	lastSent    time.Time
	lastPath    PathID
	attempts    int
	acked       bool
}

type attempt struct {
	packet   *packet
	sent     time.Time
	bytes    int
	timedOut bool
}

type lane struct {
	id              PathID
	remoteID        PathID
	lease           time.Time
	rate            float64
	deliveryRate    float64
	peakDelivery    float64
	rtt             time.Duration
	rttVariation    time.Duration
	baseRTT         time.Duration
	baseAt          time.Time
	nextSend        time.Time
	lastACK         time.Time
	lastAdjust      time.Time
	lostSinceAdjust bool
	inflight        int
	seq             uint64
	ackRevision     uint64
	ackedBytes      uint64
	ackedElapsed    uint64
	feedbackAt      time.Time
	rateBytes       uint64
	rateElapsed     uint64
	firstSent       time.Time
	transitBase     time.Duration
	haveTransit     bool
	intervalTransit time.Duration
	haveInterval    bool
	queueDelay      time.Duration
	nextBaseline    time.Time
	drainUntil      time.Time
	baselinePending bool
	stalled         bool
	lastTransmit    time.Time
	attempts        map[uint64]attempt
	sent            uint64
	acked           uint64
	retries         uint64
}

type receiver struct {
	path       PathID
	remoteLane PathID
	receipts   receiptWindow
	bytes      uint64
	start      time.Time
	last       time.Time
	highAt     time.Time
	pending    int
	ackAt      time.Time
	revision   uint64
}

// Transport has no goroutines or I/O. Its owner serializes calls.
type Transport struct {
	epoch            Epoch
	remote           Epoch
	paths            []*lane
	receivers        map[PathID]*receiver
	queue            []*packet
	priority         []*packet
	pending          map[uint64]*packet
	pendingOrder     []*packet
	seq              uint64
	bulkSeq          uint64
	interactiveSeq   uint64
	redundancyTokens float64
	lastPoll         time.Time
	drops            uint64
	expired          uint64
	duplicates       uint64
	received         receiptWindow
}

type receiptWindow struct {
	high uint64
	mask [maxPackets / 64]uint64
}

func New(epoch Epoch) *Transport {
	if epoch.Boot == 0 || epoch.Generation == 0 {
		panic("bond: zero local epoch")
	}
	return &Transport{epoch: epoch, receivers: make(map[PathID]*receiver), pending: make(map[uint64]*packet)}
}

func (t *Transport) Epoch() Epoch { return t.epoch }

// SetRemote accepts only a fresh probe's epoch. A boot change requires the
// reflector's challenge proof; generations within that boot never go backwards.
func (t *Transport) SetRemote(epoch Epoch, adopted bool) bool {
	if epoch == t.remote {
		return false
	}
	if epoch.Boot == 0 || epoch.Generation == 0 {
		return false
	}
	if t.remote.Boot != 0 {
		if epoch.Boot == t.remote.Boot && epoch.Generation <= t.remote.Generation {
			return false
		}
		if epoch.Boot != t.remote.Boot && !adopted {
			return false
		}
	}
	t.remote = epoch
	// Sequence numbers belong to the pair of authenticated endpoint generations.
	t.expired += uint64(len(t.pending))
	t.pending = make(map[uint64]*packet)
	t.pendingOrder = nil
	t.seq, t.bulkSeq, t.interactiveSeq = 0, 0, 0
	t.receivers = make(map[PathID]*receiver)
	t.received = receiptWindow{}
	for _, p := range t.paths {
		p.lease = time.Time{}
		p.attempts = make(map[uint64]attempt)
		p.inflight = 0
		p.seq = 0
		p.lastACK = time.Time{}
		p.nextSend = time.Time{}
		p.stalled = false
		p.lostSinceAdjust = false
		p.ackRevision = 0
		p.ackedBytes, p.ackedElapsed = 0, 0
		p.haveTransit = false
		p.haveInterval = false
		p.firstSent = time.Time{}
		p.feedbackAt = time.Time{}
	}
	return true
}

func (t *Transport) Remote() Epoch { return t.remote }

func (t *Transport) Path(id, remoteID PathID, rtt time.Duration, now time.Time) {
	p := t.find(id)
	if p == nil {
		if rtt <= 0 {
			rtt = 50 * time.Millisecond
		}
		p = &lane{id: id, remoteID: remoteID, rate: initialRate, rtt: rtt, baseRTT: rtt, baseAt: now, attempts: make(map[uint64]attempt)}
		p.lastTransmit = now
		p.nextBaseline = now.Add(baselineInterval + time.Duration((uint16(id)^uint16(id)>>8)&255)*baselineStagger)
		t.paths = append(t.paths, p)
	}
	p.lease = now
}

func (t *Transport) Disable(id PathID) {
	if p := t.find(id); p != nil {
		p.lease = time.Time{}
	}
}

func (t *Transport) find(id PathID) *lane {
	for _, p := range t.paths {
		if p.id == id {
			return p
		}
	}
	return nil
}

func (t *Transport) Enqueue(payload []byte, now time.Time) error {
	if len(payload) == 0 || len(payload) > maxDatagram {
		return errors.New("bond: invalid datagram length")
	}
	if len(t.queue)+len(t.priority)+len(t.pending) >= maxPackets {
		t.drops++
		return nil
	}
	p := &packet{payload: append([]byte(nil), payload...), created: now}
	if len(payload) <= smallPacket {
		p.interactive = true
		t.priority = append(t.priority, p)
	} else {
		t.queue = append(t.queue, p)
	}
	return nil
}

func (p *lane) up(now time.Time) bool { return !p.lease.IsZero() && now.Sub(p.lease) < pathLease }

func (p *lane) rto() time.Duration {
	return max(minimumRTO, p.rtt+4*p.rttVariation+ackInterval)
}

func (p *lane) window() int {
	return max(4*1500, int(p.rate*(p.baseRTT+2*targetQueue+ackInterval).Seconds()))
}

func (t *Transport) PacingRate(now time.Time) float64 {
	var rate float64
	for _, p := range t.paths {
		if p.up(now) && !p.stalled {
			rate += p.rate
		}
	}
	return rate
}

func (t *Transport) choose(now time.Time, size int, exclude PathID, retry bool) *lane {
	var best *lane
	for _, p := range t.paths {
		if !p.up(now) || p.stalled || p.nextSend.After(now) || now.Before(p.drainUntil) || p.inflight+size > p.window() {
			continue
		}
		if retry && p.id == exclude {
			continue
		}
		if best == nil || p.rtt < best.rtt {
			best = p
		}
	}
	return best
}

func (t *Transport) transmit(p *packet, path *lane, now time.Time) Transmission {
	if p.seq == 0 {
		t.seq++
		p.seq = t.seq
		if p.interactive {
			t.interactiveSeq++
			p.order = t.interactiveSeq | interactiveBit
		} else {
			t.bulkSeq++
			p.order = t.bulkSeq
		}
		t.pendingOrder = append(t.pendingOrder, p)
	}
	path.seq++
	if path.firstSent.IsZero() {
		path.firstSent = now
	}
	size := len(p.payload) + wireOverhead
	path.attempts[path.seq] = attempt{packet: p, sent: now, bytes: size}
	path.inflight += size
	path.sent += uint64(size)
	path.nextSend = maxTime(path.nextSend, now.Add(-2*time.Millisecond)).Add(time.Duration(float64(size) / path.rate * float64(time.Second)))
	if p.attempts > 0 {
		path.retries++
	}
	p.attempts++
	p.lastSent, p.lastPath = now, path.id
	path.lastTransmit = now
	t.pending[p.seq] = p
	return Transmission{path.id, dataFrame(t.epoch, t.remote, path.id, path.seq, p.seq, p.order, p.payload)}
}

func (t *Transport) Poll(now time.Time) []Transmission {
	if !t.lastPoll.IsZero() {
		budget := math.Min(redundancyRate, redundancyShare*t.PacingRate(now))
		t.redundancyTokens = math.Min(budget/10, t.redundancyTokens+now.Sub(t.lastPoll).Seconds()*budget)
	}
	t.lastPoll = now
	out := make([]Transmission, 0, 8)
	for _, r := range t.receivers {
		if r.pending > 0 && (r.pending >= ackBatchPackets || now.Sub(r.ackAt) >= ackInterval) {
			r.revision++
			a := acknowledgement{observed: t.remote, high: r.receipts.high, mask: r.receipts.mask[0], bytes: r.bytes, elapsed: uint64(r.highAt.Sub(r.start)), delay: uint64(now.Sub(r.highAt)), receivedHigh: t.received.high, receivedMask: [4]uint64(t.received.mask[:4])}
			out = append(out, Transmission{r.path, ackFrame(t.epoch, r.remoteLane, r.revision, a)})
			r.pending = 0
			r.ackAt = now
		}
	}
	for _, path := range t.paths {
		if !now.Before(path.nextBaseline) && path.queueDelay > targetQueue && path.rate <= math.Max(1.1*minimumRate, path.peakDelivery/2) {
			path.drainUntil = now.Add(max(baselineDrain, 2*path.rtt))
			path.nextBaseline = now.Add(baselineInterval)
			path.baselinePending = true
		}
		for seq, a := range path.attempts {
			if !a.timedOut && now.Sub(a.sent) >= path.rto() {
				path.lostSinceAdjust = true
				if path.lastACK.Before(a.sent) {
					path.stalled = true
					if now.Sub(path.lastAdjust) >= 50*time.Millisecond {
						path.rate = math.Max(minimumRate, path.rate*0.7)
						path.lastAdjust = now
					}
				}
				path.inflight -= a.bytes
				a.timedOut = true
				path.attempts[seq] = a
			}
			if now.Sub(a.sent) >= feedbackHorizon {
				delete(path.attempts, seq)
			}
		}
		pingInterval := 200 * time.Millisecond
		if path.stalled {
			pingInterval = 50 * time.Millisecond
		}
		if path.up(now) && now.Sub(path.lastTransmit) >= pingInterval {
			path.seq++
			path.lastTransmit = now
			if path.firstSent.IsZero() {
				path.firstSent = now
			}
			path.attempts[path.seq] = attempt{sent: now, bytes: wireOverhead}
			path.inflight += wireOverhead
			path.sent += wireOverhead
			out = append(out, Transmission{path.id, dataFrame(t.epoch, t.remote, path.id, path.seq, 0, 0, nil)})
		}
	}
	retained := t.pendingOrder[:0]
	for _, p := range t.pendingOrder {
		if p.acked {
			delete(t.pending, p.seq)
			continue
		}
		if now.Sub(p.created) > maxPacketAge {
			delete(t.pending, p.seq)
			t.expired++
			continue
		}
		retained = append(retained, p)
		previous := t.find(p.lastPath)
		rto := minimumRTO
		if previous != nil {
			rto = previous.rto()
		}
		if now.Sub(p.lastSent) < rto || p.attempts >= 4 {
			continue
		}
		path := t.choose(now, len(p.payload)+wireOverhead, p.lastPath, true)
		if path == nil {
			path = t.choose(now, len(p.payload)+wireOverhead, 0, false)
		}
		if path != nil {
			out = append(out, t.transmit(p, path, now))
		}
	}
	clear(t.pendingOrder[len(retained):])
	t.pendingOrder = retained
	for _, queue := range []*[]*packet{&t.priority, &t.queue} {
		for len(*queue) > 0 {
			p := (*queue)[0]
			if now.Sub(p.created) > maxQueueAge {
				*queue = (*queue)[1:]
				t.drops++
				continue
			}
			path := t.choose(now, len(p.payload)+wireOverhead, 0, false)
			if queue == &t.priority {
				path = t.choosePriority(now, len(p.payload)+wireOverhead, 0, false)
			}
			if path == nil {
				break
			}
			*queue = (*queue)[1:]
			out = append(out, t.transmit(p, path, now))
			if len(p.payload) <= smallPacket && t.redundancyTokens >= float64(len(p.payload)+wireOverhead) {
				second := t.choosePriority(now, len(p.payload)+wireOverhead, path.id, true)
				if second != nil {
					t.redundancyTokens -= float64(len(p.payload) + wireOverhead)
					out = append(out, t.transmit(p, second, now))
				}
			}
		}
	}
	return out
}

func (t *Transport) choosePriority(now time.Time, size int, exclude PathID, duplicate bool) *lane {
	var best *lane
	for _, p := range t.paths {
		if !p.up(now) || p.stalled || (duplicate && p.id == exclude) || p.inflight+size > p.window()+4096 || p.nextSend.After(now.Add(priorityLead)) {
			continue
		}
		if best == nil || p.rtt < best.rtt {
			best = p
		}
	}
	return best
}

func (t *Transport) Receive(path PathID, f frame.Control, now time.Time) ([]Delivery, error) {
	h, err := parseHeader(f.Payload)
	if err != nil {
		return nil, err
	}
	if h.epoch != t.remote || f.Seq == 0 {
		return nil, errors.New("bond: stale epoch or zero sequence")
	}
	local := t.find(path)
	if local == nil || !local.up(now) {
		return nil, errors.New("bond: unvalidated path")
	}
	switch f.ControlType {
	case DataType:
		if h.lane != local.remoteID {
			return nil, errors.New("bond: DATA lane disagrees with authenticated probe")
		}
		if len(f.Payload) < headerBytes+32 || len(f.Payload) > headerBytes+32+maxDatagram {
			return nil, errors.New("bond: invalid DATA size")
		}
		destination := Epoch{binary.BigEndian.Uint64(f.Payload[headerBytes:]), binary.BigEndian.Uint64(f.Payload[headerBytes+8:])}
		if destination != t.epoch {
			return nil, errors.New("bond: DATA for stale local epoch")
		}
		seq := binary.BigEndian.Uint64(f.Payload[headerBytes+16:])
		order := binary.BigEndian.Uint64(f.Payload[headerBytes+24:])
		if seq == 0 && (order != 0 || len(f.Payload) != headerBytes+32) {
			return nil, errors.New("bond: malformed keepalive")
		}
		if seq != 0 && (order & ^interactiveBit == 0 || len(f.Payload) == headerBytes+32) {
			return nil, errors.New("bond: invalid delivery sequence or empty datagram")
		}
		r := t.receivers[h.lane]
		if r == nil {
			r = &receiver{path: path, remoteLane: h.lane, start: now, ackAt: now}
			t.receivers[h.lane] = r
		}
		if r.path != path {
			return nil, errors.New("bond: receiver lane changed without probe")
		}
		previousHigh := r.receipts.high
		if !r.receipts.mark(f.Seq) {
			t.duplicates++
			return nil, nil
		}
		if f.Seq > previousHigh {
			r.highAt = now
		}
		r.bytes += uint64(len(f.Payload) - headerBytes - 32 + wireOverhead)
		r.last = now
		r.pending++
		if seq == 0 {
			return nil, nil
		}
		if !t.received.mark(seq) {
			t.duplicates++
			return nil, nil
		}
		return []Delivery{{Sequence: order & ^interactiveBit, Interactive: order&interactiveBit != 0, Payload: f.Payload[headerBytes+32:]}}, nil
	case ACKType:
		if h.lane != path {
			return nil, errors.New("bond: ACK arrived on wrong path")
		}
		a, err := parseACK(f.Payload)
		if err != nil {
			return nil, err
		}
		if a.observed != t.epoch {
			return nil, errors.New("bond: ACK for stale local epoch")
		}
		if f.Seq <= local.ackRevision || a.high > local.seq || a.receivedHigh > t.seq || a.bytes < local.ackedBytes || a.elapsed < local.ackedElapsed {
			return nil, errors.New("bond: stale or impossible ACK")
		}
		local.ackRevision = f.Seq
		t.ack(local, a, now)
		return nil, nil
	default:
		return nil, errors.New("bond: unknown frame type")
	}
}

func (t *Transport) ack(p *lane, a acknowledgement, now time.Time) {
	backlogged := len(t.queue)+len(t.priority) > 0 || p.inflight >= p.window()/2
	var sample time.Duration
	for seq, pending := range t.pending {
		if a.received(seq) {
			pending.acked = true
			delete(t.pending, seq)
		}
	}
	for seq, sent := range p.attempts {
		if seq <= a.high && a.high-seq < 64 && a.mask&(uint64(1)<<(a.high-seq)) != 0 {
			if !sent.timedOut {
				p.inflight -= sent.bytes
			}
			p.acked += uint64(sent.bytes)
			if sent.packet != nil {
				sent.packet.acked = true
				delete(t.pending, sent.packet.seq)
			}
			delete(p.attempts, seq)
			if seq == a.high && a.delay <= uint64(now.Sub(sent.sent)) {
				p.stalled = false
				sample = now.Sub(sent.sent) - time.Duration(a.delay)
				transit := time.Duration(a.elapsed) - sent.sent.Sub(p.firstSent)
				if p.baselinePending && !sent.sent.Before(p.drainUntil) {
					p.haveTransit = false
					p.haveInterval = false
					p.baselinePending = false
				}
				if !p.haveTransit || transit < p.transitBase {
					p.transitBase, p.haveTransit = transit, true
				}
				p.queueDelay = transit - p.transitBase
				if !p.haveInterval || transit < p.intervalTransit {
					p.intervalTransit, p.haveInterval = transit, true
				}
			}
		} else if sent.packet != nil && a.received(sent.packet.seq) && seq < a.high && a.high-seq >= 64 {
			// The global receipt confirms delivery after the lane bitmap moved on.
			// Release ownership without claiming which physical attempt arrived.
			if !sent.timedOut {
				p.inflight -= sent.bytes
			}
			delete(p.attempts, seq)
		}
	}
	if sample > 0 {
		difference := sample - p.rtt
		if difference < 0 {
			difference = -difference
		}
		p.rttVariation = (3*p.rttVariation + difference) / 4
		p.rtt = (7*p.rtt + sample) / 8
		if p.lastACK.IsZero() || sample < p.baseRTT || now.Sub(p.baseAt) > 30*time.Second {
			p.baseRTT, p.baseAt = sample, now
		}
	}
	if p.feedbackAt.IsZero() {
		p.rateBytes, p.rateElapsed, p.feedbackAt = a.bytes, a.elapsed, now
	} else if a.elapsed-p.rateElapsed >= uint64(deliveryInterval) {
		rate := float64(a.bytes-p.rateBytes) / time.Duration(a.elapsed-p.rateElapsed).Seconds()
		if p.deliveryRate == 0 {
			p.deliveryRate = rate
		} else {
			p.deliveryRate = 0.8*p.deliveryRate + 0.2*rate
		}
		p.rateBytes, p.rateElapsed, p.feedbackAt = a.bytes, a.elapsed, now
	}
	p.peakDelivery = math.Max(p.peakDelivery, p.deliveryRate)
	p.ackedBytes, p.ackedElapsed = a.bytes, a.elapsed
	p.lastACK = now
	if now.Before(p.drainUntil) || now.Sub(p.lastAdjust) < max(min(p.rtt, 100*time.Millisecond), 50*time.Millisecond) {
		return
	}
	p.lastAdjust = now
	queueDelay := p.queueDelay
	if p.haveInterval {
		queueDelay = max(0, p.intervalTransit-p.transitBase)
		p.haveInterval = false
	}
	lost := p.lostSinceAdjust
	p.lostSinceAdjust = false
	if !backlogged {
		return
	}
	if lost && p.deliveryRate > 0 && p.rate > 1.25*p.deliveryRate {
		p.rate = math.Max(minimumRate, 1.05*p.deliveryRate)
	} else if sample > 0 && queueDelay > targetQueue {
		p.rate = math.Max(minimumRate, p.rate*0.9)
	} else if sample > 0 {
		p.rate = math.Min(maximumRate, p.rate*1.06+1500)
	}
}

func (w *receiptWindow) mark(seq uint64) bool {
	if seq > w.high {
		delta := seq - w.high
		if delta >= maxPackets {
			w.mask = [maxPackets / 64]uint64{}
		} else {
			words, shift := int(delta/64), uint(delta%64)
			for i := len(w.mask) - 1; i >= 0; i-- {
				var value uint64
				if i >= words {
					value = w.mask[i-words] << shift
				}
				if shift > 0 && i > words {
					value |= w.mask[i-words-1] >> (64 - shift)
				}
				w.mask[i] = value
			}
		}
		w.high = seq
	}
	delta := w.high - seq
	if delta >= maxPackets {
		return false
	}
	bit := uint64(1) << (delta % 64)
	if w.mask[delta/64]&bit != 0 {
		return false
	}
	w.mask[delta/64] |= bit
	return true
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func (t *Transport) Snapshot(now time.Time) Snapshot {
	s := Snapshot{QueueDrops: t.drops, Expired: t.expired, Duplicates: t.duplicates}
	for _, p := range t.paths {
		s.Paths = append(s.Paths, PathStats{p.id, p.rate, p.deliveryRate, p.rtt, p.baseRTT, p.queueDelay, p.inflight, p.sent, p.acked, p.retries, p.up(now) && !p.stalled})
	}
	return s
}
