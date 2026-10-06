package bond_test

import (
	"container/heap"
	"encoding/binary"
	"math"
	"math/rand/v2"
	"net/netip"
	"slices"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/bond"
	"github.com/7mind/wanbond/internal/reseq"
)

// modelClock is the resequencer's clock in the transfer model.
type modelClock struct{ now time.Time }

func (c *modelClock) Now() time.Time { return c.now }

// modelLane is one emulated link: rate in bytes per second, one-way delay,
// and random loss. The delay drifts within ±jitter over about drift, as a
// radio link's latency does: datagrams sent together share it, so the lane
// does not reorder, but a round trip can stay high for many rounds. (netem's
// delay correlation is per datagram; at 95% it leaves a spread of about a
// sixth of the configured jitter.)
type modelLane struct {
	rate          float64
	delay, jitter time.Duration
	drift         time.Duration
	loss          float64
	// The base delay also moves to a new level within ±shift every
	// shiftEvery, as a satellite link's does when its path is reassigned.
	shift      time.Duration
	shiftEvery time.Duration
	// A policed link forwards at its rate what a bucket of two datagrams
	// admits and drops the rest without delaying it.
	policed bool
	buffer  time.Duration
	// condition is evaluated independently for each direction at send time.
	condition func(side int, elapsed time.Duration) modelCondition
}

type modelCondition struct {
	rate          float64
	delay         time.Duration
	loss          float64
	buffer        time.Duration
	policed, dark bool
}

func (l modelLane) at(side int, elapsed time.Duration) modelCondition {
	if l.condition != nil {
		return l.condition(side, elapsed)
	}
	buffer := l.buffer
	if buffer == 0 {
		buffer = modelRouterBuffer
	}
	return modelCondition{rate: l.rate, delay: l.delay, loss: l.loss, buffer: buffer, policed: l.policed}
}

// tcpTransfer is one CUBIC-like sender with selective acknowledgements pushing
// segments through two bonded endpoints and the receive resequencer, as one
// TCP flow does through the tunnel. A segment is lost once three later ones
// are selectively acknowledged; each loss episode reduces the window once.
type tcpTransfer struct {
	lanes []modelLane
	seed  uint64
	// seconds of transfer after a second of idle probing.
	seconds int
}

func tcpModelACKMetadata(acknowledgement uint64, ranges [][2]uint64) bond.PacketMetadata {
	return bond.PacketMetadata{Flow: bond.FlowID{4, 6, 2}, ACK: bond.TCPACK{Eligible: len(ranges) == 0, Sequence: 1, Acknowledgement: uint32(acknowledgement), Window: 4096}}
}

func TestTCPModelPreservesSACKReports(t *testing.T) {
	checkACKSequence(t, func(i byte, metadata *bond.PacketMetadata) {
		var ranges [][2]uint64
		if i == 3 {
			ranges = [][2]uint64{{1000, 2000}}
		}
		*metadata = tcpModelACKMetadata(uint64(i)*100, ranges)
	}, []byte{1, 2, 3}, 0, 0)
}

type tcpOutcome struct {
	// delivered is the in-order payload reaching the receiver's TCP in each
	// second, in bytes; windowMB the congestion window at its end.
	delivered []float64
	windowMB  []float64
	// retransmits counts segments sent again after selective
	// acknowledgements, timeouts the retransmission timer's expiries, and
	// abandoned the sequences the resequencer gave up on.
	retransmits, timeouts int
	abandoned             uint64
	// targetMB is each lane's mean pacing target at the sender over the last
	// half of the transfer, in MB/s; queueDrops and aqmDrops are the sender's
	// tunnel queue totals.
	targetMB             []float64
	queueDrops, aqmDrops uint64
	// linkQueue is, per lane, the wait for the link that 95% of the sender's
	// datagrams stayed below over the last half of the transfer: the queue in
	// the path's own buffer, where nothing has priority.
	linkQueue []time.Duration
}

const (
	tcpSegment      = 1300
	tcpACKBytes     = 96
	tcpInitialCwnd  = 10
	tcpCubicC       = 0.4
	tcpCubicBeta    = 0.7
	tcpRenoAlpha    = 3 * (1 - tcpCubicBeta) / (1 + tcpCubicBeta)
	tcpInitialRTO   = time.Second
	tcpMinimumRTO   = 200 * time.Millisecond
	tcpDuplicateACK = 3
	tcpSACKBlocks   = 4
	// The receiver's window: 64 MB, as the lab guests allow.
	tcpReceiveWindowSegments = 64 << 20 / tcpSegment
	modelResequencerWindow   = 32768
	modelResequencerTimeout  = 250 * time.Millisecond
	modelResequencerHold     = 300 * time.Millisecond
	modelRouterBuffer        = 100 * time.Millisecond
)

// tcpSender is the sender's congestion state, in segments.
type tcpSender struct {
	cwnd, ssthresh float64
	next, unacked  uint64 // next new segment; oldest unacknowledged
	recovering     bool
	recover        uint64
	// afterTimeout: the recovery began with a retransmission timeout, and
	// the window grows by slow start within it, as Linux's loss state does.
	afterTimeout bool
	// HyStart: slow start ends when a round's lowest round trip exceeds the
	// previous round's by an eighth, a queue forming.
	roundEnd              uint64
	roundMin, previousMin time.Duration
	wMax, k               float64
	renoWindow            float64
	epoch                 time.Time
	srtt, rttVar          time.Duration
	lastProgress          time.Time
	backoff               int
	// sentAt keeps RTT-eligible originals; retransmission removes the sample.
	sentAt        map[uint64]time.Time
	retransmitted map[uint64]bool
	sacked        map[uint64]bool
	highestSacked [tcpDuplicateACK]uint64
}

func (s *tcpSender) reduce(now time.Time) {
	s.wMax = s.cwnd
	s.ssthresh = math.Max(2, s.cwnd*tcpCubicBeta)
	s.renoWindow = s.ssthresh
	s.k = math.Cbrt(s.wMax * (1 - tcpCubicBeta) / tcpCubicC)
	s.epoch = now
}

func (s *tcpSender) grow(now time.Time, newly float64) {
	if s.cwnd < s.ssthresh {
		s.cwnd += newly
		return
	}
	if s.renoWindow == 0 {
		s.renoWindow = s.cwnd
	}
	s.renoWindow += tcpRenoAlpha * newly / s.cwnd
	target := tcpCubicC*math.Pow(now.Sub(s.epoch).Seconds()-s.k, 3) + s.wMax
	if target > s.cwnd {
		s.cwnd += (target - s.cwnd) / s.cwnd * newly
	} else {
		s.cwnd += newly / (100 * s.cwnd)
	}
	s.cwnd = math.Max(s.cwnd, s.renoWindow)
}

func (s *tcpSender) sample(rtt time.Duration) {
	if s.roundMin == 0 || rtt < s.roundMin {
		s.roundMin = rtt
	}
	if s.srtt == 0 {
		s.srtt, s.rttVar = rtt, rtt/2
		return
	}
	difference := rtt - s.srtt
	if difference < 0 {
		difference = -difference
	}
	s.rttVar = (3*s.rttVar + difference) / 4
	s.srtt = (7*s.srtt + rtt) / 8
}

func (s *tcpSender) rto() time.Duration {
	if s.srtt == 0 {
		return tcpInitialRTO
	}
	return max(tcpMinimumRTO, s.srtt+4*s.rttVar)
}

func (s *tcpSender) observeACK(payload []byte, now time.Time) uint64 {
	acknowledged := binary.BigEndian.Uint64(payload)
	var cumulative, selective time.Time
	ambiguous := false
	for seq := s.unacked; seq < acknowledged; seq++ {
		at, eligible := s.sentAt[seq]
		ambiguous = ambiguous || !eligible
		if eligible && !s.sacked[seq] && (cumulative.IsZero() || at.Before(cumulative)) {
			cumulative = at
		}
	}
	for block := 0; block < tcpSACKBlocks; block++ {
		from := binary.BigEndian.Uint64(payload[8+16*block:])
		to := binary.BigEndian.Uint64(payload[16+16*block:])
		for seq := max(from, s.unacked); seq < to && seq < s.next; seq++ {
			if at, eligible := s.sentAt[seq]; eligible && !s.sacked[seq] && (selective.IsZero() || at.Before(selective)) {
				selective = at
			}
			if !s.sacked[seq] {
				for i, high := range s.highestSacked {
					if seq > high {
						copy(s.highestSacked[i+1:], s.highestSacked[i:])
						s.highestSacked[i] = seq
						break
					}
				}
			}
			s.sacked[seq] = true
		}
	}
	// A cumulative ACK covering a retransmission cannot identify its RTT;
	// already SACKed segments report no new receipt timing either.
	if ambiguous || cumulative.IsZero() {
		cumulative = selective
	}
	if !cumulative.IsZero() {
		s.sample(now.Sub(cumulative))
	}
	return acknowledged
}

// insertRange adds seq to ascending half-open ranges, merging neighbours.
func insertRange(ranges [][2]uint64, seq uint64) [][2]uint64 {
	i := len(ranges)
	for i > 0 && ranges[i-1][0] > seq {
		i--
	}
	switch {
	case i > 0 && ranges[i-1][1] == seq:
		ranges[i-1][1]++
		if i < len(ranges) && ranges[i][0] == ranges[i-1][1] {
			ranges[i-1][1] = ranges[i][1]
			ranges = append(ranges[:i], ranges[i+1:]...)
		}
	case i < len(ranges) && ranges[i][0] == seq+1:
		ranges[i][0] = seq
	default:
		ranges = append(ranges, [2]uint64{})
		copy(ranges[i+1:], ranges[i:])
		ranges[i] = [2]uint64{seq, seq + 1}
	}
	return ranges
}

func (m tcpTransfer) run(t *testing.T) tcpOutcome {
	t.Helper()
	start := time.Unix(100, 0)
	clock := &modelClock{now: start}
	random := rand.New(rand.NewPCG(m.seed, 0))
	// Loss draws one number per datagram; from the stream that moves the
	// latency, it would give each version of the transport a different path.
	drops := rand.New(rand.NewPCG(m.seed, 1))
	peers := [2]*bond.Transport{bond.New(bond.Epoch{Boot: 1, Generation: 1}), bond.New(bond.Epoch{Boot: 2, Generation: 1})}
	for side, p := range peers {
		p.SetRemote(peers[1-side].Epoch(), true)
	}
	resequencer := reseq.New(modelResequencerWindow, modelResequencerTimeout, clock)
	resequencer.SetHoldBound(modelResequencerHold)
	source := netip.MustParseAddrPort("192.0.2.1:51820")
	wire := &events{}
	heap.Init(wire)
	available := make([][2]time.Time, len(m.lanes))
	const policerBurst = 2 * (tcpSegment + 129.0)
	tokens := make([][2]float64, len(m.lanes))
	refilled := make([][2]time.Time, len(m.lanes))
	for lane := range tokens {
		tokens[lane], refilled[lane] = [2]float64{policerBurst, policerBurst}, [2]time.Time{start, start}
	}
	wander := make([][2]float64, len(m.lanes)) // current offset from the delay, in nanoseconds
	level := make([][2]time.Duration, len(m.lanes))
	sender := tcpSender{cwnd: tcpInitialCwnd, ssthresh: math.Inf(1), next: 1, unacked: 1, backoff: 1,
		sentAt: map[uint64]time.Time{}, retransmitted: map[uint64]bool{}, sacked: map[uint64]bool{}}
	receiverNext := uint64(1)
	receiverBuffered := map[uint64]bool{}
	var receiverRanges [][2]uint64 // received beyond receiverNext, ascending, half-open
	outcome := tcpOutcome{delivered: make([]float64, m.seconds), windowMB: make([]float64, m.seconds)}
	targets := make([]float64, len(m.lanes))
	waits := make([][]time.Duration, len(m.lanes))
	targetSamples := 0
	send := func(now time.Time, seq uint64) {
		segment := make([]byte, tcpSegment)
		binary.BigEndian.PutUint64(segment, seq)
		// A refused datagram is a drop at the tunnel's entrance, as a full
		// interface queue is to TCP.
		_ = peers[0].Enqueue(segment, bond.PacketMetadata{Flow: bond.FlowID{4, 6, 1}}, now)
	}
	for tick := 0; tick < (m.seconds+1)*1000; tick++ {
		now := start.Add(time.Duration(tick) * time.Millisecond)
		clock.now = now
		for _, p := range peers {
			for lane, l := range m.lanes {
				p.Path(bond.PathID(lane), bond.PathID(lane), 2*l.delay, now)
			}
		}
		for lane, l := range m.lanes {
			if l.shiftEvery > 0 && tick%int(l.shiftEvery/time.Millisecond) == 0 {
				for side := range level[lane] {
					level[lane][side] = time.Duration(random.Int64N(int64(2*l.shift))) - l.shift
				}
			}
			if l.jitter == 0 {
				continue
			}
			// An Ornstein-Uhlenbeck walk with a standard deviation of half the
			// jitter, held within it.
			step := float64(time.Millisecond) / float64(l.drift)
			for side := range wander[lane] {
				x := wander[lane][side]
				x += -x*step + float64(l.jitter)/2*math.Sqrt(2*step)*random.NormFloat64()
				wander[lane][side] = math.Max(-float64(l.jitter), math.Min(float64(l.jitter), x))
			}
		}
		if tick >= 1000 {
			s := &sender
			if s.next > s.unacked && now.Sub(s.lastProgress) >= time.Duration(s.backoff)*s.rto() {
				s.reduce(now)
				s.cwnd, s.recovering, s.recover, s.afterTimeout = 1, true, s.next, true
				s.backoff = min(s.backoff*2, 64)
				s.lastProgress = now
				clear(s.retransmitted)
				delete(s.sentAt, s.unacked)
				s.retransmitted[s.unacked] = true
				send(now, s.unacked)
				outcome.timeouts++
			}
			for float64(s.next-s.unacked)-float64(len(s.sacked)) < math.Min(s.cwnd, tcpReceiveWindowSegments) {
				if s.next == s.unacked {
					// The timer runs from the oldest outstanding segment.
					s.lastProgress = now
				}
				s.sentAt[s.next] = now
				send(now, s.next)
				s.next++
			}
		}
		for side, p := range peers {
			for _, tx := range poll(p, now) {
				lane := int(tx.Path)
				l := m.lanes[lane]
				condition := l.at(side, now.Sub(start))
				if condition.dark {
					continue
				}
				if condition.policed {
					bytes := float64(len(tx.Frame.Payload) + 78)
					tokens[lane][side] = math.Min(policerBurst, tokens[lane][side]+now.Sub(refilled[lane][side]).Seconds()*condition.rate)
					refilled[lane][side] = now
					if tokens[lane][side] < bytes {
						continue
					}
					tokens[lane][side] -= bytes
					transit := condition.delay + level[lane][side] + time.Duration(wander[lane][side]) + time.Duration(bytes/condition.rate*float64(time.Second))
					heap.Push(wire, event{now.Add(transit), 1 - side, tx.Path, tx.Frame})
					continue
				}
				begin := maxTimeTest(now, available[lane][side])
				if begin.Sub(now) > condition.buffer {
					continue
				}
				if side == 0 && tick >= (m.seconds/2+1)*1000 {
					waits[lane] = append(waits[lane], begin.Sub(now))
				}
				available[lane][side] = begin.Add(time.Duration(float64(len(tx.Frame.Payload)+78) / condition.rate * float64(time.Second)))
				if drops.Float64() < condition.loss {
					continue
				}
				transit := condition.delay + level[lane][side] + time.Duration(wander[lane][side])
				heap.Push(wire, event{available[lane][side].Add(transit), 1 - side, tx.Path, tx.Frame})
			}
		}
		for wire.Len() > 0 && !(*wire)[0].at.After(now) {
			e := heap.Pop(wire).(event)
			got, err := peers[e.to].Receive(e.path, e.frame, now)
			if err != nil {
				continue
			}
			for _, d := range got {
				if e.to == 1 {
					// Bulk is released to TCP in order, as before WireGuard.
					resequencer.Observe(d.Sequence, d.Payload, source)
					continue
				}
				// An acknowledgement reached the sender: the cumulative point
				// and up to four ranges received beyond it.
				s := &sender
				acknowledged := s.observeACK(d.Payload, now)
				if acknowledged > s.unacked {
					newly := float64(acknowledged - s.unacked)
					for seq := s.unacked; seq < acknowledged; seq++ {
						delete(s.sentAt, seq)
						delete(s.retransmitted, seq)
						delete(s.sacked, seq)
					}
					s.unacked, s.backoff, s.lastProgress = acknowledged, 1, now
					if s.recovering && acknowledged >= s.recover {
						s.recovering, s.afterTimeout = false, false
					}
					if !s.recovering || s.afterTimeout {
						s.grow(now, newly)
					}
					if acknowledged >= s.roundEnd {
						if s.cwnd < s.ssthresh && s.previousMin > 0 && s.roundMin >= s.previousMin+max(4*time.Millisecond, s.previousMin/8) {
							s.ssthresh = s.cwnd
							s.renoWindow = s.cwnd
							s.wMax, s.k, s.epoch = s.cwnd, 0, now
						}
						s.previousMin, s.roundMin, s.roundEnd = s.roundMin, 0, s.next
					}
				}
				// Three selectively received full segments above a hole show loss.
				for seq := s.unacked; seq < s.highestSacked[tcpDuplicateACK-1] && seq < s.next; seq++ {
					if s.sacked[seq] || s.retransmitted[seq] {
						continue
					}
					if !s.recovering {
						s.reduce(now)
						s.cwnd = s.ssthresh
						s.recovering, s.recover = true, s.next
					}
					delete(s.sentAt, seq)
					s.retransmitted[seq] = true
					send(now, seq)
					outcome.retransmits++
				}
			}
		}
		// The receiver's TCP: cumulative acknowledgement of what arrived in
		// order, and the ranges beyond it.
		for {
			item, ok := resequencer.Pop()
			if !ok {
				break
			}
			seq := binary.BigEndian.Uint64(item.Payload)
			if seq >= receiverNext && !receiverBuffered[seq] {
				receiverBuffered[seq] = true
				receiverRanges = insertRange(receiverRanges, seq)
			}
			for receiverBuffered[receiverNext] {
				delete(receiverBuffered, receiverNext)
				receiverNext++
				if second := tick/1000 - 1; second >= 0 && second < m.seconds {
					outcome.delivered[second] += tcpSegment
				}
			}
			for len(receiverRanges) > 0 && receiverRanges[0][1] <= receiverNext {
				receiverRanges = receiverRanges[1:]
			}
			if len(receiverRanges) > 0 && receiverRanges[0][0] < receiverNext {
				receiverRanges[0][0] = receiverNext
			}
			ack := make([]byte, tcpACKBytes)
			binary.BigEndian.PutUint64(ack, receiverNext)
			// The highest ranges are the newest information.
			for block := 0; block < tcpSACKBlocks && block < len(receiverRanges); block++ {
				r := receiverRanges[len(receiverRanges)-1-block]
				binary.BigEndian.PutUint64(ack[8+16*block:], r[0])
				binary.BigEndian.PutUint64(ack[16+16*block:], r[1])
			}
			metadata := tcpModelACKMetadata(receiverNext, receiverRanges)
			_ = peers[1].Enqueue(ack, metadata, now)
		}
		if second := tick/1000 - 1; second >= 0 && tick%1000 == 999 {
			outcome.windowMB[second] = sender.cwnd * tcpSegment / 1e6
		}
		if tick >= (m.seconds/2+1)*1000 && tick%100 == 0 {
			for lane, path := range peers[0].Snapshot(now).Paths {
				targets[lane] += path.Rate
			}
			targetSamples++
		}
	}
	for _, sum := range targets {
		outcome.targetMB = append(outcome.targetMB, sum/float64(targetSamples)/1e6)
	}
	for _, lane := range waits {
		slices.Sort(lane)
		var wait time.Duration
		if len(lane) > 0 {
			wait = lane[len(lane)*95/100]
		}
		outcome.linkQueue = append(outcome.linkQueue, wait.Round(time.Millisecond))
	}
	final := peers[0].Snapshot(start.Add(time.Duration(m.seconds+1) * time.Second))
	outcome.queueDrops, outcome.aqmDrops = final.QueueDrops, final.AQMDrops
	outcome.abandoned = resequencer.Stats().Skipped
	return outcome
}

func megabits(bytesPerSecond []float64) []int {
	out := make([]int, len(bytesPerSecond))
	for i, b := range bytesPerSecond {
		out[i] = int(b * 8 / 1e6)
	}
	return out
}

// Latency as measured on the production mobile link on 2026-09-30 (600 pings,
// idle): it scatters with a round-trip standard deviation of 6.6 ms and no
// memory beyond a fraction of a second.
var measuredMobile = modelLane{delay: 12 * time.Millisecond, jitter: 9 * time.Millisecond, drift: 50 * time.Millisecond}

func withRate(l modelLane, rate, loss float64) modelLane {
	l.rate, l.loss = rate, loss
	return l
}

// A lane whose latency wanders as the measured mobile link's does, carrying
// one TCP transfer on its own. Queue delay is measured from the lowest
// transit time seen, and a wandering path sits above that floor most of the
// time; with a fixed 10 ms threshold the lane was held at its minimum rate
// and the transfer delivered nothing.
func TestWanderingLatencyIsNotAQueue(t *testing.T) {
	const capacity = 37.5e6
	outcome := tcpTransfer{lanes: []modelLane{withRate(measuredMobile, capacity, 0)}, seed: 1, seconds: 20}.run(t)
	var last float64
	for _, bytes := range outcome.delivered[10:] {
		last += bytes
	}
	t.Logf("last 10 s %.0f Mbit/s of %.0f on the wire; every 4th second %v; lane target %.1f MB/s; tcp retransmits %d",
		last*8/10e6, capacity*8/1e6, everyFourth(megabits(outcome.delivered)), outcome.targetMB, outcome.retransmits)
	if last/10 < 0.6*capacity {
		t.Fatalf("transfer delivered %.0f Mbit/s over a wandering %.0f Mbit/s lane", last*8/10e6, capacity*8/1e6)
	}
}

func everyFourth(values []int) []int {
	var out []int
	for i := 3; i < len(values); i += 4 {
		out = append(out, values[i])
	}
	return out
}
