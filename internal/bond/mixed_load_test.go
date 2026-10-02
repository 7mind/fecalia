package bond_test

import (
	"container/heap"
	"encoding/binary"
	"math/rand/v2"
	"sort"
	"time"

	"github.com/7mind/wanbond/internal/bond"
)

// varyingLane is an emulated link whose service rate is redrawn every
// rateEvery, uniformly within rate*(1±swing), as a cellular scheduler's grant
// is: a queue forms in the path whenever the grant falls below the sending
// rate, well before the mean rate is reached. Its buffer holds buffer of
// traffic at the mean rate.
type varyingLane struct {
	rate      float64 // mean, bytes per second
	swing     float64
	rateEvery time.Duration
	delay     time.Duration
	buffer    time.Duration
	// stall, when set, is the mean time the lane serves nothing in the bulk
	// direction, once in stallEvery on average; each stall and each interval
	// between two is drawn uniformly within half to one and a half times its
	// mean. What waited is then delivered in order. With stallRate set, the
	// lane serves at that fraction of its rate for the time instead.
	stall, stallEvery time.Duration
	stallRate         float64
	// batch, when set, delivers the bulk direction's datagrams together at
	// every multiple of it, as a radio link's scheduler does; ackLoss is the
	// share of the acknowledgements for that direction that the lane loses.
	batch   time.Duration
	ackLoss float64
}

// stalls is a lane's schedule of stalls in the bulk direction.
type stalls struct {
	from, until time.Time
}

// at reports whether the given time falls into a stall, and when that ends.
func (s *stalls) at(l varyingLane, random *rand.Rand, begin time.Time) (bool, time.Time) {
	around := func(mean time.Duration) time.Duration {
		return time.Duration((0.5 + random.Float64()) * float64(mean))
	}
	if s.from.IsZero() {
		s.from = begin.Add(around(l.stallEvery))
		s.until = s.from.Add(around(l.stall))
	}
	for !begin.Before(s.until) {
		s.from = s.until.Add(around(l.stallEvery - l.stall))
		s.until = s.from.Add(around(l.stall))
	}
	return !begin.Before(s.from), s.until
}

// mixedLoad offers one direction saturating bulk and both directions a 50 Hz
// voice stream over the lanes, and measures the final half of the run.
type mixedLoad struct {
	lanes []varyingLane
	// offered is the bulk load in bytes per second.
	offered float64
	seconds int
	// failed, when not negative, loses every datagram in both directions
	// from failAt, in milliseconds, to the end.
	failed, failAt int
	// acksPerSecond adds a pure TCP acknowledgement stream in both
	// directions, as a download in the other direction would.
	acksPerSecond int
	// observe, when set, receives the bulk sender's state once a second.
	observe func(second int, state bond.Snapshot)
	// seed varies what the lanes draw at random.
	seed uint64
}

type mixedLoadOutcome struct {
	// bulk is the bulk payload delivered per second over the measured
	// period, in bytes per second.
	bulk float64
	// voiceDelivered of voiceSent datagrams arrived, with this one-way 99th
	// percentile delay and longest interval between arrivals.
	voiceDelivered, voiceSent int
	voiceP99, voiceGap        time.Duration
	// sender and receiver are the bulk sender's and its peer's state at the
	// end of the run.
	sender, receiver bond.Snapshot
}

func (m mixedLoad) run() mixedLoadOutcome {
	duration := m.seconds * 1000
	measured := duration / 2
	start := time.Unix(100, 0)
	random := rand.New(rand.NewPCG(7, m.seed))
	peers := [2]*bond.Transport{bond.New(bond.Epoch{Boot: 1, Generation: 1}), bond.New(bond.Epoch{Boot: 2, Generation: 1})}
	for side, p := range peers {
		p.SetRemote(peers[1-side].Epoch(), true)
	}
	queue := &events{}
	heap.Init(queue)
	available := make([][2]time.Time, len(m.lanes))
	current := make([][2]float64, len(m.lanes))
	stalled := make([]stalls, len(m.lanes))
	var outcome mixedLoadOutcome
	var waits []time.Duration
	var bulkBytes int
	var owed float64
	lastArrival := -1
	acknowledgement := uint32(1)
	for tick := 0; tick < duration+500; tick++ {
		now := start.Add(time.Duration(tick) * time.Millisecond)
		down := func(lane int) bool { return lane == m.failed && tick >= m.failAt }
		for lane, l := range m.lanes {
			if l.rateEvery == 0 || tick%int(l.rateEvery/time.Millisecond) == 0 {
				for side := range current[lane] {
					current[lane][side] = l.rate * (1 + l.swing*(2*random.Float64()-1))
				}
			}
		}
		for _, p := range peers {
			for lane, l := range m.lanes {
				// Probes stop confirming a failed path; its lease then expires.
				if !down(lane) {
					p.Path(bond.PathID(lane), bond.PathID(lane), 2*l.delay, now)
				}
			}
		}
		if tick >= 2000 && tick < duration {
			for owed += m.offered / 1000; owed >= 1200; owed -= 1200 {
				// A refused datagram is a drop at the tunnel's entrance.
				_ = peers[0].Enqueue(make([]byte, 1200), bond.PacketMetadata{Flow: bond.FlowID{4, 6, 1}}, now)
			}
			for range (tick+1)*m.acksPerSecond/1000 - tick*m.acksPerSecond/1000 {
				acknowledgement += 1448
				ack := bond.PacketMetadata{Flow: tcpFlow(), ACK: bond.TCPACK{Eligible: true, Sequence: 1, Acknowledgement: acknowledgement, Window: 4096}}
				for _, p := range peers {
					_ = p.Enqueue(make([]byte, ackWireGuardBytes), ack, now)
				}
			}
			if tick%20 == 0 {
				for side, p := range peers {
					datagram := make([]byte, voiceWireGuardBytes)
					binary.BigEndian.PutUint64(datagram, uint64(tick))
					_ = p.Enqueue(datagram, bond.PacketMetadata{Flow: voiceFlow(1)}, now)
					if side == 0 && tick >= measured {
						outcome.voiceSent++
					}
				}
			}
		}
		if m.observe != nil && tick%1000 == 999 {
			m.observe(tick/1000, peers[0].Snapshot(now))
		}
		if tick == duration-1 {
			outcome.sender, outcome.receiver = peers[0].Snapshot(now), peers[1].Snapshot(now)
		}
		for side, p := range peers {
			for _, tx := range p.Poll(now) {
				lane := int(tx.Path)
				if down(lane) {
					continue
				}
				l := m.lanes[lane]
				if side == 1 && tx.Frame.ControlType == bond.ACKType && l.ackLoss > 0 && random.Float64() < l.ackLoss {
					continue
				}
				begin := maxTimeTest(now, available[lane][side])
				rate := current[lane][side]
				if side == 0 && l.stallEvery > 0 {
					if stalled, until := stalled[lane].at(l, random, begin); stalled && l.stallRate == 0 {
						begin = until
					} else if stalled {
						rate *= l.stallRate
					}
				}
				if begin.Sub(now) > l.buffer {
					continue
				}
				available[lane][side] = begin.Add(time.Duration(float64(len(tx.Frame.Payload)+78) / rate * float64(time.Second)))
				arrival := available[lane][side].Add(l.delay)
				if side == 0 && l.batch > 0 {
					arrival = start.Add(arrival.Sub(start).Truncate(l.batch) + l.batch)
				}
				heap.Push(queue, event{arrival, 1 - side, tx.Path, tx.Frame})
			}
		}
		for queue.Len() > 0 && !(*queue)[0].at.After(now) {
			e := heap.Pop(queue).(event)
			if down(int(e.path)) {
				continue
			}
			got, err := peers[e.to].Receive(e.path, e.frame, now)
			if err != nil {
				// A frame in flight when the path's lease expired.
				continue
			}
			for _, d := range got {
				if e.to != 1 {
					continue
				}
				switch len(d.Payload) {
				case 1200:
					if tick >= measured && tick < duration {
						bulkBytes += len(d.Payload)
					}
				case voiceWireGuardBytes:
					if at := int(binary.BigEndian.Uint64(d.Payload)); at >= measured {
						waits = append(waits, time.Duration(tick-at)*time.Millisecond)
						if lastArrival >= 0 {
							outcome.voiceGap = max(outcome.voiceGap, time.Duration(tick-lastArrival)*time.Millisecond)
						}
						lastArrival = tick
					}
				}
			}
		}
	}
	outcome.bulk = float64(bulkBytes) / float64(duration-measured) * 1000
	outcome.voiceDelivered = len(waits)
	if len(waits) > 0 {
		sort.Slice(waits, func(i, j int) bool { return waits[i] < waits[j] })
		outcome.voiceP99 = waits[len(waits)*99/100]
	}
	return outcome
}

var (
	// lowLatencyLane is the lane a call prefers: slow, steady and short.
	lowLatencyLane = varyingLane{rate: 62500, delay: 14 * time.Millisecond, buffer: 100 * time.Millisecond}
	// steadyLane carries 50 Mbit/s.
	steadyLane = varyingLane{rate: 6.25e6, delay: 25 * time.Millisecond, buffer: 100 * time.Millisecond}
)
