package bond_test

import (
	"container/heap"
	"encoding/binary"
	"fmt"
	"math"
	"math/rand/v2"
	"net/netip"
	"slices"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/bond"
	"github.com/7mind/wanbond/internal/frame"
	"github.com/7mind/wanbond/internal/reseq"
)

const (
	policyTCPPayload      = 1200
	policyTCPEncrypted    = 1280 // IPv4/TCP, WireGuard padding, header and tag
	policyUDPIPOverhead   = 28
	policyEthernetHeader  = 14
	policyLinkOverhead    = policyUDPIPOverhead + policyEthernetHeader
	policyControlEnvelope = 50
	policyACKWireBytes    = 193 + policyEthernetHeader
	policyACKCadence      = 25 * time.Millisecond
)

type policyBulkDirection uint8

const (
	policyBulkBoth policyBulkDirection = iota
	policyBulkDownlink
	policyBulkUplink
)

func (d policyBulkDirection) includes(side int) bool {
	switch d {
	case policyBulkBoth:
		return true
	case policyBulkDownlink:
		return side == 0
	case policyBulkUplink:
		return side == 1
	default:
		panic("invalid model bulk direction")
	}
}

type policyRun struct {
	lanes              []modelLane
	seconds, trafficAt int
	bulk, voice        bool
	bulkDirection      policyBulkDirection
}

type policyVoice struct {
	sent, arrived int
	rtt           time.Duration
}
type policyRoute struct {
	at, lane int
}
type policyOutcome struct {
	bulk                  [2][]float64
	voice                 [2][]policyVoice
	laneBulk              [2][][]float64
	laneSent, laneDropped [2][][]float64
	states                [2][]bond.Snapshot
	rejected              [2]int
	voiceRoutes           [2][]policyRoute
}

type policyTCP struct {
	sender   tcpSender
	next     uint64
	buffered map[uint64]bool
	ranges   [][2]uint64
}

func newPolicyTCP() policyTCP {
	return policyTCP{sender: tcpSender{cwnd: tcpInitialCwnd, ssthresh: math.Inf(1), next: 1, unacked: 1, backoff: 1,
		sentAt: map[uint64]time.Time{}, retransmitted: map[uint64]bool{}, sacked: map[uint64]bool{}}, next: 1, buffered: map[uint64]bool{}}
}

func (p *policyTCP) offer(now time.Time, send func(uint64)) {
	s := &p.sender
	if s.next > s.unacked && now.Sub(s.lastProgress) >= time.Duration(s.backoff)*s.rto() {
		s.reduce(now)
		s.cwnd, s.recovering, s.recover, s.afterTimeout = 1, true, s.next, true
		s.backoff = min(s.backoff*2, 64)
		s.lastProgress = now
		clear(s.retransmitted)
		delete(s.sentAt, s.unacked)
		s.retransmitted[s.unacked] = true
		send(s.unacked)
	}
	for float64(s.next-s.unacked)-float64(len(s.sacked)) < math.Min(s.cwnd, tcpReceiveWindowSegments) {
		if s.next == s.unacked {
			s.lastProgress = now
		}
		s.sentAt[s.next] = now
		send(s.next)
		s.next++
	}
}

func (p *policyTCP) acknowledge(payload []byte, now time.Time, send func(uint64)) {
	s := &p.sender
	acknowledged := s.observeACK(payload, now)
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
				s.ssthresh, s.wMax, s.k, s.epoch = s.cwnd, s.cwnd, 0, now
			}
			s.previousMin, s.roundMin, s.roundEnd = s.roundMin, 0, s.next
		}
	}
	for seq := s.unacked; seq < s.highestSacked[tcpDuplicateACK-1] && seq < s.next; seq++ {
		if s.sacked[seq] || s.retransmitted[seq] {
			continue
		}
		if !s.recovering {
			s.reduce(now)
			s.cwnd, s.recovering, s.recover = s.ssthresh, true, s.next
		}
		delete(s.sentAt, seq)
		s.retransmitted[seq] = true
		send(seq)
	}
}

func (p *policyTCP) receive(seq uint64) (int, []byte) {
	if seq >= p.next && !p.buffered[seq] {
		p.buffered[seq] = true
		p.ranges = insertRange(p.ranges, seq)
	}
	delivered := 0
	for p.buffered[p.next] {
		delete(p.buffered, p.next)
		p.next++
		delivered += policyTCPPayload
	}
	for len(p.ranges) > 0 && p.ranges[0][1] <= p.next {
		p.ranges = p.ranges[1:]
	}
	if len(p.ranges) > 0 && p.ranges[0][0] < p.next {
		p.ranges[0][0] = p.next
	}
	ack := make([]byte, tcpACKBytes)
	binary.BigEndian.PutUint64(ack, p.next)
	for block := 0; block < tcpSACKBlocks && block < len(p.ranges); block++ {
		r := p.ranges[len(p.ranges)-1-block]
		binary.BigEndian.PutUint64(ack[8+16*block:], r[0])
		binary.BigEndian.PutUint64(ack[16+16*block:], r[1])
	}
	return delivered, ack
}

func (p *policyTCP) metadata(side int) bond.PacketMetadata {
	return bond.PacketMetadata{Flow: bond.FlowID{4, 6, byte(3 + side)}, ACK: bond.TCPACK{Eligible: len(p.ranges) == 0, Sequence: 1, Acknowledgement: uint32(p.next), Window: 4096}}
}

func TestAdaptiveModelLossRequiresThreeSelectiveReceipts(t *testing.T) {
	start := time.Unix(100, 0)
	sender, receiver := newPolicyTCP(), newPolicyTCP()
	var sent []uint64
	transmit := func(seq uint64) { sent = append(sent, seq) }
	sender.offer(start, transmit)
	if len(sent) != tcpInitialCwnd {
		t.Fatalf("initial flight has %d segments", len(sent))
	}
	sent = nil
	for step, seq := range []uint64{10, 10, 9} {
		_, ack := receiver.receive(seq)
		sender.acknowledge(ack, start.Add(time.Duration(100+step*10)*time.Millisecond), transmit)
		if len(sent) != 0 {
			t.Fatalf("only %d distinct later segments arrived, but retransmitted %v", len(receiver.buffered), sent)
		}
	}
	_, ack := receiver.receive(8)
	sender.acknowledge(ack, start.Add(130*time.Millisecond), transmit)
	if !slices.Equal(sent, []uint64{1, 2, 3, 4, 5, 6, 7}) {
		t.Fatalf("three later full segments must expose the preceding holes: retransmitted %v", sent)
	}
}

func TestAdaptiveModelRTTDoesNotForgetRetransmittedSegments(t *testing.T) {
	start := time.Unix(100, 0)
	sender, receiver := newPolicyTCP(), newPolicyTCP()
	var sent []uint64
	transmit := func(seq uint64) { sent = append(sent, seq) }
	sender.offer(start, transmit)
	initiallySent := len(sent)
	acknowledge := func(seq uint64, elapsed time.Duration) {
		_, ack := receiver.receive(seq)
		sender.acknowledge(ack, start.Add(elapsed), transmit)
	}
	acknowledge(1, 100*time.Millisecond)
	for _, seq := range []uint64{5, 6, 7} {
		acknowledge(seq, 100*time.Millisecond)
	}
	if !slices.Contains(sent[initiallySent:], uint64(3)) {
		t.Fatal("fixture did not retransmit segment 3 before its timeout")
	}
	sender.offer(start.Add(800*time.Millisecond), transmit)
	acknowledge(3, 900*time.Millisecond)
	acknowledge(2, 950*time.Millisecond)
	sent = nil
	sender.offer(start.Add(1600*time.Millisecond), transmit)
	if !slices.Contains(sent, uint64(4)) {
		t.Fatalf("segment 4 was not retried: ambiguous ACK inflated RTO to %s; transmissions %v", sender.sender.rto(), sent)
	}
}

func TestAdaptiveModelRTTDoesNotResampleSelectiveAcknowledgements(t *testing.T) {
	start := time.Unix(100, 0)
	sender, receiver := newPolicyTCP(), newPolicyTCP()
	var sent []uint64
	transmit := func(seq uint64) { sent = append(sent, seq) }
	sender.offer(start, transmit)
	initiallySent := len(sent)
	acknowledge := func(seq uint64, elapsed time.Duration) {
		_, ack := receiver.receive(seq)
		sender.acknowledge(ack, start.Add(elapsed), transmit)
	}
	acknowledge(1, 100*time.Millisecond)
	for seq := uint64(7); seq <= 10; seq++ {
		acknowledge(seq, 100*time.Millisecond)
	}
	for seq := uint64(2); seq <= 6; seq++ {
		if !slices.Contains(sent[initiallySent:], seq) {
			t.Fatalf("fixture did not retransmit segment %d", seq)
		}
		acknowledge(seq, 900*time.Millisecond)
	}
	sent = nil
	sender.offer(start.Add(900*time.Millisecond), transmit)
	if !slices.Contains(sent, uint64(11)) {
		t.Fatal("fixture did not start the next flight")
	}
	sent = nil
	sender.offer(start.Add(1400*time.Millisecond), transmit)
	if !slices.Contains(sent, uint64(11)) {
		t.Fatalf("segment 11 was not retried: repeated SACK delivery inflated RTO to %s; transmissions %v", sender.sender.rto(), sent)
	}
}

func TestAdaptiveModelRTTUsesFreshSelectiveAcknowledgements(t *testing.T) {
	start := time.Unix(100, 0)
	sender, receiver := newPolicyTCP(), newPolicyTCP()
	var sent []uint64
	transmit := func(seq uint64) { sent = append(sent, seq) }
	sender.offer(start, transmit)
	_, ack := receiver.receive(10)
	sender.acknowledge(ack, start.Add(100*time.Millisecond), transmit)
	sent = nil
	sender.acknowledge(ack, start.Add(800*time.Millisecond), transmit)
	sender.offer(start.Add(900*time.Millisecond), transmit)
	if !slices.Contains(sent, uint64(1)) {
		t.Fatalf("segment 1 was not retried using fresh SACK timing: RTO %s; transmissions %v", sender.sender.rto(), sent)
	}
}

func TestAdaptiveModelPreservesSACKReports(t *testing.T) {
	now := time.Unix(100, 0)
	transport := bond.New(bond.Epoch{Boot: 1, Generation: 1})
	transport.SetRemote(bond.Epoch{Boot: 2, Generation: 1}, true)
	transport.Path(0, 0, 20*time.Millisecond, now)
	receiver := newPolicyTCP()
	for _, seq := range []uint64{5, 1, 2} {
		_, ack := receiver.receive(seq)
		if err := transport.Enqueue(ack, receiver.metadata(0), now); err != nil {
			t.Fatal(err)
		}
	}
	reports := 0
	for _, tx := range poll(transport, now) {
		if tx.Frame.ControlType == bond.DataType && len(tx.Frame.Payload) == bond.Overhead-frame.ControlOverhead+tcpACKBytes {
			reports++
		}
	}
	if reports != 3 {
		t.Fatalf("only %d of three SACK reports retained; model metadata let coalescing remove TCP information", reports)
	}
}

func (m policyRun) renewPaths(t *testing.T, peer *bond.Transport, side int, elapsed time.Duration, now time.Time) {
	t.Helper()
	for lane, link := range m.lanes {
		if link.at(1-side, elapsed).dark {
			continue
		}
		if err := peer.Path(bond.PathID(lane), bond.PathID(lane), link.at(side, elapsed).delay+link.at(1-side, elapsed).delay, now); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAdaptiveModelDoesNotRenewHelloLeasesThroughADarkDirection(t *testing.T) {
	for _, direction := range []int{-1, 0, 1} {
		t.Run(fmt.Sprint(direction), func(t *testing.T) {
			m := policyRun{lanes: []modelLane{{condition: func(side int, at time.Duration) modelCondition {
				return modelCondition{rate: 62500, delay: 20 * time.Millisecond, dark: at >= 20*time.Second && (direction == -1 || side == direction)}
			}}, {rate: 1250000, delay: 40 * time.Millisecond}}}
			start := time.Unix(100, 0)
			for side := range 2 {
				peer := bond.New(bond.Epoch{Boot: uint64(side + 1), Generation: 1})
				peer.SetRemote(bond.Epoch{Boot: uint64(2 - side), Generation: 1}, true)
				m.renewPaths(t, peer, side, 19*time.Second, start.Add(19*time.Second))
				if !peer.Snapshot(start.Add(19 * time.Second)).Paths[0].Up {
					t.Fatal("reproduction requires a healthy lane before the blackout")
				}
				m.renewPaths(t, peer, side, 22*time.Second, start.Add(22*time.Second))
				paths := peer.Snapshot(start.Add(22 * time.Second)).Paths
				want := direction != -1 && direction != 1-side
				if paths[0].Up != want || !paths[1].Up {
					t.Errorf("side %d hello lease alive=%v, want %v; unfailed lane alive=%v", side, paths[0].Up, want, paths[1].Up)
				}
			}
		})
	}
}

// policyRun exercises both transports, optional echo streams and the selected
// TCP directions. Measurements are payload delivery, never pacing targets.
func (m policyRun) run(t *testing.T) policyOutcome {
	t.Helper()
	start := time.Unix(100, 0)
	clock := &modelClock{now: start}
	peers := [2]*bond.Transport{bond.New(bond.Epoch{Boot: 1, Generation: 1}), bond.New(bond.Epoch{Boot: 2, Generation: 1})}
	tcp := [2]policyTCP{newPolicyTCP(), newPolicyTCP()}
	var resequencers [2]*reseq.Resequencer
	var outcome policyOutcome
	for side, p := range peers {
		p.SetRemote(peers[1-side].Epoch(), true)
		resequencers[side] = reseq.New(modelResequencerWindow, modelResequencerTimeout, clock)
		resequencers[side].SetHoldBound(modelResequencerHold)
		outcome.bulk[side] = make([]float64, m.seconds)
		outcome.states[side] = make([]bond.Snapshot, m.seconds*10)
		outcome.voice[side] = make([]policyVoice, 0, m.seconds*50)
		for range m.lanes {
			outcome.laneBulk[side] = append(outcome.laneBulk[side], make([]float64, m.seconds))
			outcome.laneSent[side] = append(outcome.laneSent[side], make([]float64, m.seconds))
			outcome.laneDropped[side] = append(outcome.laneDropped[side], make([]float64, m.seconds))
		}
	}
	wire := &events{}
	heap.Init(wire)
	available := make([][2]time.Time, len(m.lanes))
	tokens, refilled := make([][2]float64, len(m.lanes)), make([][2]time.Time, len(m.lanes))
	const policerBurst = 2 * (policyTCPEncrypted + bond.Overhead + policyLinkOverhead)
	for lane := range tokens {
		tokens[lane] = [2]float64{policerBurst, policerBurst}
		refilled[lane] = [2]time.Time{start, start}
	}
	draw := rand.New(rand.NewPCG(19, 0))
	firstVoice := [2]map[uint64]bool{{}, {}}
	source := netip.MustParseAddrPort("192.0.2.1:51820")
	send := func(side int, now time.Time, seq uint64) {
		payload := make([]byte, policyTCPEncrypted)
		binary.BigEndian.PutUint64(payload, seq)
		if err := peers[side].Enqueue(payload, bond.PacketMetadata{Flow: bond.FlowID{4, 6, byte(side + 1)}}, now); err != nil {
			t.Fatal(err)
		}
	}
	for tick := 0; tick < m.seconds*1000+1000; tick++ {
		now := start.Add(time.Duration(tick) * time.Millisecond)
		clock.now = now
		for side, p := range peers {
			m.renewPaths(t, p, side, now.Sub(start), now)
			if tick >= m.trafficAt*1000 && tick < m.seconds*1000 {
				if m.bulk && m.bulkDirection.includes(side) {
					tcp[side].offer(now, func(seq uint64) { send(side, now, seq) })
				}
				if m.voice && tick%20 == 0 {
					payload := make([]byte, voiceWireGuardBytes)
					binary.BigEndian.PutUint64(payload, uint64(tick))
					if err := p.Enqueue(payload, bond.PacketMetadata{Flow: voiceFlow(byte(side + 1))}, now); err != nil {
						t.Fatal(err)
					}
					outcome.voice[side] = append(outcome.voice[side], policyVoice{sent: tick, arrived: -1})
				}
			}
			for _, tx := range poll(p, now) {
				lane := int(tx.Path)
				c := m.lanes[lane].at(side, now.Sub(start))
				bytes := float64(len(tx.Frame.Payload) + policyControlEnvelope + policyLinkOverhead)
				const dataFields = 19 + 32
				if tx.Frame.ControlType == bond.DataType && len(tx.Frame.Payload) == dataFields+voiceWireGuardBytes && tx.Frame.Payload[dataFields+8] == 0 {
					seq := binary.BigEndian.Uint64(tx.Frame.Payload[dataFields:])
					if !firstVoice[side][seq] {
						firstVoice[side][seq] = true
						outcome.voiceRoutes[side] = append(outcome.voiceRoutes[side], policyRoute{tick, lane})
					}
				}
				if tick/1000 < m.seconds {
					outcome.laneSent[side][lane][tick/1000] += bytes
				}
				drop := c.dark || draw.Float64() < c.loss
				begin := maxTimeTest(now, available[lane][side])
				if c.policed {
					tokens[lane][side] = min(policerBurst, tokens[lane][side]+now.Sub(refilled[lane][side]).Seconds()*c.rate)
					refilled[lane][side] = now
					if tokens[lane][side] < bytes {
						drop = true
					} else {
						tokens[lane][side] -= bytes
					}
				} else {
					if begin.Sub(now) > c.buffer {
						drop = true
					}
				}
				if drop {
					if tick/1000 < m.seconds {
						outcome.laneDropped[side][lane][tick/1000] += bytes
					}
					continue
				}
				finish := begin.Add(time.Duration(bytes / c.rate * float64(time.Second)))
				if !c.policed {
					available[lane][side] = finish
				}
				heap.Push(wire, event{finish.Add(c.delay), 1 - side, tx.Path, tx.Frame})
			}
		}
		for wire.Len() > 0 && !(*wire)[0].at.After(now) {
			e := heap.Pop(wire).(event)
			if m.lanes[int(e.path)].at(1-e.to, now.Sub(start)).dark {
				continue
			}
			got, err := peers[e.to].Receive(e.path, e.frame, now)
			if err != nil {
				outcome.rejected[e.to]++
				continue
			}
			for _, d := range got {
				switch len(d.Payload) {
				case voiceWireGuardBytes:
					at := int(binary.BigEndian.Uint64(d.Payload))
					if d.Payload[8] == 0 {
						echo := slices.Clone(d.Payload)
						echo[8] = 1
						if err := peers[e.to].Enqueue(echo, bond.PacketMetadata{Flow: voiceFlow(byte(2 - e.to))}, now); err != nil {
							t.Fatal(err)
						}
					} else {
						index := (at - m.trafficAt*1000) / 20
						v := &outcome.voice[e.to][index]
						v.arrived, v.rtt = tick, time.Duration(tick-at)*time.Millisecond
					}
				case policyTCPEncrypted:
					resequencers[e.to].Observe(d.Sequence, d.Payload, source)
					if tick/1000 < m.seconds {
						outcome.laneBulk[1-e.to][int(e.path)][tick/1000] += policyTCPPayload
					}
				case tcpACKBytes:
					tcp[e.to].acknowledge(d.Payload, now, func(seq uint64) { send(e.to, now, seq) })
				default:
					t.Fatalf("unexpected model datagram size %d", len(d.Payload))
				}
			}
		}
		for side, rq := range resequencers {
			for {
				item, ok := rq.Pop()
				if !ok {
					break
				}
				delivered, ack := tcp[1-side].receive(binary.BigEndian.Uint64(item.Payload))
				if tick/1000 < m.seconds {
					outcome.bulk[1-side][tick/1000] += float64(delivered)
				}
				meta := tcp[1-side].metadata(side)
				if err := peers[side].Enqueue(ack, meta, now); err != nil {
					t.Fatal(err)
				}
			}
			if tick%100 == 99 && tick/100 < len(outcome.states[side]) {
				outcome.states[side][tick/100] = peers[side].Snapshot(now)
			}
		}
	}
	return outcome
}

// Reference service is independent of the transport: two 50 Hz voice streams,
// 25 ms lane feedback, 200 ms keepalives and one reverse TCP ACK per segment.
// Bulk and ACK encapsulation are included. Repairs and extra copies never
// lower the reference. Equal utilization of the available directional wire
// budgets leaves room for the selected flows even on an asymmetric pair. An outage
// uses only the bidirectional survivors, as required by scenarios 1a/1c.
func (m policyRun) reference(at time.Duration) [2]float64 {
	var available [2]float64
	for side := range available {
		for _, l := range m.lanes {
			c := l.at(side, at)
			if !c.dark && !l.at(1-side, at).dark {
				available[side] += c.rate - policyACKWireBytes/policyACKCadence.Seconds() - (bond.Overhead+policyLinkOverhead)/0.2
			}
		}
		if m.voice {
			available[side] -= 100 * (voiceWireGuardBytes + bond.Overhead + policyLinkOverhead)
		}
		available[side] = max(0, available[side])
	}
	dataWire := float64(policyTCPEncrypted + bond.Overhead + policyLinkOverhead)
	ackWire := float64(tcpACKBytes+bond.Overhead+policyLinkOverhead) + policyACKWireBytes/64.0
	desired := [2]float64{available[0] / dataWire, available[1] / dataWire}
	for side := range desired {
		if !m.bulkDirection.includes(side) {
			desired[side] = 0
		}
	}
	gain := 1.0
	for side := range available {
		demand := desired[side]*dataWire + desired[1-side]*ackWire
		if demand > 0 {
			gain = min(gain, available[side]/demand)
		}
	}
	return [2]float64{desired[0] * gain * policyTCPPayload, desired[1] * gain * policyTCPPayload}
}
