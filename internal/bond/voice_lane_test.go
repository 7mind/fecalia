package bond_test

import (
	"container/heap"
	"encoding/binary"
	"math/rand/v2"
	"sort"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/bond"
)

const (
	voiceWireGuardBytes = 224 // 160-byte RTP-sized UDP payload inside WireGuard
	ackWireGuardBytes   = 96
)

func voiceFlow(stream byte) bond.FlowID {
	flow := bond.FlowID{4, 17}
	flow[34] = stream
	return flow
}

func tcpFlow() bond.FlowID { return bond.FlowID{4, 6} }

// voiceUnderLoad offers, in both directions, saturating bulk, a pure TCP ACK
// stream and two 50 Hz voice streams over the given lanes. It returns the
// voice datagrams delivered of those sent in the final five seconds, and
// their one-way 99th percentile delay.
func voiceUnderLoad(t *testing.T, rates []float64, delays, jitters []time.Duration, acksPerSecond int) (delivered, sent int, p99 time.Duration) {
	t.Helper()
	const (
		duration = 15000
		measured = 10000
	)
	start := time.Unix(100, 0)
	random := rand.New(rand.NewPCG(1, 0))
	peers := [2]*bond.Transport{bond.New(bond.Epoch{Boot: 1, Generation: 1}), bond.New(bond.Epoch{Boot: 2, Generation: 1})}
	for side, p := range peers {
		p.SetRemote(peers[1-side].Epoch(), true)
	}
	queue := &events{}
	heap.Init(queue)
	available := make([][2]time.Time, len(rates))
	var waits []time.Duration
	acknowledgement := uint32(1)
	for tick := 0; tick < duration+500; tick++ {
		now := start.Add(time.Duration(tick) * time.Millisecond)
		for _, p := range peers {
			for lane := range rates {
				p.Path(bond.PathID(lane), bond.PathID(lane), 2*delays[lane], now)
			}
		}
		for side, p := range peers {
			if tick >= 3000 && tick < duration {
				for range 2 {
					if err := p.Enqueue(make([]byte, 1200), bond.PacketMetadata{Flow: bond.FlowID{4, 6, 1}}, now); err != nil {
						t.Fatal(err)
					}
				}
				if tick%10 == 0 {
					voice := make([]byte, voiceWireGuardBytes)
					binary.BigEndian.PutUint64(voice, uint64(tick))
					if err := p.Enqueue(voice, bond.PacketMetadata{Flow: voiceFlow(byte(1 + tick/10%2))}, now); err != nil {
						t.Fatal(err)
					}
					if side == 0 && tick >= measured {
						sent++
					}
				}
				for range (tick+1)*acksPerSecond/1000 - tick*acksPerSecond/1000 {
					acknowledgement += 1448
					ack := bond.PacketMetadata{Flow: tcpFlow(), ACK: bond.TCPACK{Eligible: true, Sequence: 1, Acknowledgement: acknowledgement, Window: 4096}}
					if err := p.Enqueue(make([]byte, ackWireGuardBytes), ack, now); err != nil {
						t.Fatal(err)
					}
				}
			}
			for _, tx := range p.Poll(now) {
				lane := int(tx.Path)
				begin := maxTimeTest(now, available[lane][side])
				if begin.Sub(now) > 100*time.Millisecond {
					continue
				}
				available[lane][side] = begin.Add(time.Duration(float64(len(tx.Frame.Payload)+78) / rates[lane] * float64(time.Second)))
				delay := delays[lane]
				if jitters[lane] > 0 {
					delay += time.Duration(random.Int64N(int64(2*jitters[lane]))) - jitters[lane]
				}
				heap.Push(queue, event{available[lane][side].Add(delay), 1 - side, tx.Path, tx.Frame})
			}
		}
		for queue.Len() > 0 && !(*queue)[0].at.After(now) {
			e := heap.Pop(queue).(event)
			got, err := peers[e.to].Receive(e.path, e.frame, now)
			if err != nil {
				t.Fatal(err)
			}
			for _, d := range got {
				if e.to == 1 && len(d.Payload) == voiceWireGuardBytes {
					if at := int(binary.BigEndian.Uint64(d.Payload)); at >= measured {
						waits = append(waits, time.Duration(tick-at)*time.Millisecond)
					}
				}
			}
		}
	}
	if len(waits) == 0 {
		return 0, sent, 0
	}
	sort.Slice(waits, func(i, j int) bool { return waits[i] < waits[j] })
	return len(waits), sent, waits[len(waits)*99/100]
}

// Voice must get the low-latency lane while bulk and the TCP ACK stream of a
// fast download load both lanes (VM trace 20260929-143023-continuity: the
// 0.4 Mbit/s low-latency uplink carried equal turns of two voice streams and
// TCP ACKs, and most voice travelled on the slower lane).
func TestVoiceKeepsLowLatencyLaneUnderLoad(t *testing.T) {
	delivered, sent, p99 := voiceUnderLoad(t, []float64{50000, 156250},
		[]time.Duration{20 * time.Millisecond, 40 * time.Millisecond},
		[]time.Duration{0, 30 * time.Millisecond}, 400)
	t.Logf("delivered %d/%d voice datagrams, one-way p99 %s", delivered, sent, p99)
	// The low-latency lane costs 20 ms, one 28 ms bulk datagram ahead and 7 ms
	// of voice serialization.
	if delivered < sent*99/100 || p99 > 65*time.Millisecond {
		t.Fatalf("voice lost its lane: %d/%d delivered, one-way p99 %s", delivered, sent, p99)
	}
}

// With one 0.4 Mbit/s lane left, voice needs 70% of it; TCP ACKs and bulk must
// share the rest instead of taking equal turns.
func TestVoiceSurvivesOnSingleSlowLane(t *testing.T) {
	delivered, sent, p99 := voiceUnderLoad(t, []float64{50000}, []time.Duration{20 * time.Millisecond}, []time.Duration{0}, 100)
	t.Logf("delivered %d/%d voice datagrams, one-way p99 %s", delivered, sent, p99)
	if delivered < sent*99/100 || p99 > 75*time.Millisecond {
		t.Fatalf("voice displaced on a slow lane: %d/%d delivered, one-way p99 %s", delivered, sent, p99)
	}
}
