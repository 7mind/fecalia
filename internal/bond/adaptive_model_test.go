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

type policyRun struct {
	lanes              []modelLane
	seconds, trafficAt int
	bulk, voice        bool
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
	acknowledged := binary.BigEndian.Uint64(payload)
	for block := 0; block < tcpSACKBlocks; block++ {
		from := binary.BigEndian.Uint64(payload[8+16*block:])
		to := binary.BigEndian.Uint64(payload[16+16*block:])
		for seq := max(from, s.unacked); seq < to && seq < s.next; seq++ {
			s.sacked[seq] = true
			s.highestSacked = max(s.highestSacked, seq)
		}
	}
	if acknowledged > s.unacked {
		if at, ok := s.sentAt[acknowledged-1]; ok && !s.retransmitted[acknowledged-1] {
			s.sample(now.Sub(at))
		}
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
	for seq := s.unacked; seq+tcpDuplicateACK <= s.highestSacked && seq < s.next; seq++ {
		if s.sacked[seq] || s.retransmitted[seq] {
			continue
		}
		if !s.recovering {
			s.reduce(now)
			s.cwnd, s.recovering, s.recover = s.ssthresh, true, s.next
		}
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

// policyRun exercises both transports, two echo streams and one TCP flow in
// each direction. Measurements are payload delivery, never pacing targets.
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
			for lane, l := range m.lanes {
				// Authenticated hellos keep their lease even across a one-way
				// outage: only progress feedback can establish that direction.
				c := l.at(side, now.Sub(start))
				p.Path(bond.PathID(lane), bond.PathID(lane), c.delay+l.at(1-side, now.Sub(start)).delay, now)
			}
			if tick >= m.trafficAt*1000 && tick < m.seconds*1000 {
				if m.bulk {
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
				meta := bond.PacketMetadata{Flow: bond.FlowID{4, 6, byte(3 + side)}, ACK: bond.TCPACK{Eligible: true, Sequence: 1, Acknowledgement: uint32(tcp[1-side].next), Window: 4096}}
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
// budgets leaves room for both flows even on an asymmetric pair.
func (m policyRun) reference(at time.Duration) [2]float64 {
	var available [2]float64
	for side := range available {
		for _, l := range m.lanes {
			c := l.at(side, at)
			if !c.dark {
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
	gain := 1.0
	for side := range available {
		demand := desired[side]*dataWire + desired[1-side]*ackWire
		if demand > 0 {
			gain = min(gain, available[side]/demand)
		}
	}
	return [2]float64{desired[0] * gain * policyTCPPayload, desired[1] * gain * policyTCPPayload}
}
