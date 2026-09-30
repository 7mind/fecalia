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

// fastLane offers saturating bulk over one lane and reports the payload
// delivered in each second. correlation is that of successive transit delays,
// as netem defines it: 0 scatters them independently, close to 1 lets the
// latency drift slowly across its range, as a radio link's does.
func fastLane(t *testing.T, rate float64, delay, jitter time.Duration, loss float64, correlation float64, seconds int) []float64 {
	t.Helper()
	delivered, _, _ := fastLaneTransit(t, rate, delay, jitter, loss, correlation, seconds)
	return delivered
}

// fastLaneTransit also returns, for the datagrams first transmitted after the
// third second, the time from first transmission to delivery, sorted: for all
// of them, and for those that needed more than one transmission.
func fastLaneTransit(t *testing.T, rate float64, delay, jitter time.Duration, loss float64, correlation float64, seconds int) ([]float64, []time.Duration, []time.Duration) {
	t.Helper()
	start := time.Unix(100, 0)
	random := rand.New(rand.NewPCG(1, 0))
	peers := [2]*bond.Transport{bond.New(bond.Epoch{Boot: 1, Generation: 1}), bond.New(bond.Epoch{Boot: 2, Generation: 1})}
	for side, p := range peers {
		p.SetRemote(peers[1-side].Epoch(), true)
	}
	queue := &events{}
	heap.Init(queue)
	var available [2]time.Time
	var previous [2]time.Duration
	delivered := make([]float64, seconds)
	var transit, repaired []time.Duration
	firstSent := map[int]int{}
	transmissions := map[int]int{}
	next := 0
	// The path buffers 100 ms, as a router before a fast link does.
	const routerBuffer = 100 * time.Millisecond
	for tick := 0; tick < seconds*1000; tick++ {
		now := start.Add(time.Duration(tick) * time.Millisecond)
		for _, p := range peers {
			p.Path(0, 0, 2*delay, now)
		}
		for side, p := range peers {
			if side == 0 && tick >= 1000 {
				for range 40 {
					datagram := make([]byte, 1300)
					next++
					binary.BigEndian.PutUint64(datagram, uint64(next))
					if err := p.Enqueue(datagram, bond.PacketMetadata{Flow: bond.FlowID{4, 6, 1}}, now); err != nil {
						break
					}
				}
			}
			for _, tx := range p.Poll(now) {
				if payload := tx.Frame.Payload; side == 0 && len(payload) > 1300 && len(payload) < 1300+64 {
					id := int(binary.BigEndian.Uint64(payload[len(payload)-1300:]))
					transmissions[id]++
					if firstSent[id] == 0 {
						firstSent[id] = tick
					}
				}
				begin := maxTimeTest(now, available[side])
				if begin.Sub(now) > routerBuffer {
					continue
				}
				available[side] = begin.Add(time.Duration(float64(len(tx.Frame.Payload)+78) / rate * float64(time.Second)))
				if side == 0 && random.Float64() < loss {
					continue
				}
				transit := delay
				if jitter > 0 {
					fresh := time.Duration(random.Int64N(int64(2*jitter))) - jitter
					previous[side] = time.Duration(correlation*float64(previous[side]) + (1-correlation)*float64(fresh))
					transit += previous[side]
				}
				heap.Push(queue, event{available[side].Add(transit), 1 - side, tx.Path, tx.Frame})
			}
		}
		for queue.Len() > 0 && !(*queue)[0].at.After(now) {
			e := heap.Pop(queue).(event)
			got, err := peers[e.to].Receive(e.path, e.frame, now)
			if err != nil {
				continue
			}
			if e.to == 1 {
				for _, d := range got {
					delivered[tick/1000] += float64(len(d.Payload))
					if id := int(binary.BigEndian.Uint64(d.Payload)); firstSent[id] >= 3000 {
						transit = append(transit, time.Duration(tick-firstSent[id])*time.Millisecond)
						if transmissions[id] > 1 {
							repaired = append(repaired, time.Duration(tick-firstSent[id])*time.Millisecond)
						}
					}
				}
			}
		}
	}
	sort.Slice(transit, func(i, j int) bool { return transit[i] < transit[j] })
	sort.Slice(repaired, func(i, j int) bool { return repaired[i] < repaired[j] })
	return delivered, transit, repaired
}

// A 300 Mbit/s lane, 37.5 MB/s of wire. Bulk payload of 1300 bytes in
// 1378-byte wire frames delivers at most 35.4 MB/s.
const fastLaneRate = 37.5e6

// A path that loses a fraction of a percent at random loses something in
// every control interval at this rate. Cutting on each of them drove the
// target to 3 MB/s within twelve seconds; repair covers such loss, and only a
// round's material loss is congestion.
func TestRandomLossDoesNotCollapseTheTarget(t *testing.T) {
	delivered := fastLane(t, fastLaneRate, 20*time.Millisecond, 10*time.Millisecond, 0.004, 0, 20)
	t.Logf("delivered MB/s by second: %v", mbPerSecond(delivered))
	for second := 10; second < 20; second++ {
		if delivered[second] < 0.7*fastLaneRate {
			t.Fatalf("second %d delivered %.1f MB/s on a lossy %.1f MB/s lane", second, delivered[second]/1e6, fastLaneRate/1e6)
		}
	}
}

// A lane with 30 ms of jitter each way: one delayed sample ended discovery in
// its first 100 ms, at a capacity measured from a handful of datagrams, and
// the target then took six seconds to climb back from 16 kB/s.
func TestJitteryLaneStartsUp(t *testing.T) {
	delivered := fastLane(t, fastLaneRate, 40*time.Millisecond, 30*time.Millisecond, 0, 0, 10)
	t.Logf("delivered MB/s by second: %v", mbPerSecond(delivered))
	for second := 6; second < 10; second++ {
		if delivered[second] < 0.6*fastLaneRate {
			t.Fatalf("second %d delivered %.1f MB/s on a jittery %.1f MB/s lane", second, delivered[second]/1e6, fastLaneRate/1e6)
		}
	}
}

func mbPerSecond(bytes []float64) []float64 {
	out := make([]float64, len(bytes))
	for i, b := range bytes {
		out[i] = float64(int(b/1e5)) / 10
	}
	return out
}

// A radio link's latency drifts: successive delays are close, and the round
// trip stays high for whole rounds. A timeout that follows the smoothed round
// trip then times out entire rounds on a lossless lane (VM run of 2026-09-30:
// 547 of 552 datagrams of a round, 33 cuts, the lane held at 12 of 37.5 MB/s).
func TestDriftingLatencyIsNotLoss(t *testing.T) {
	delivered := fastLane(t, fastLaneRate, 40*time.Millisecond, 30*time.Millisecond, 0, 0.95, 15)
	t.Logf("delivered MB/s by second: %v", mbPerSecond(delivered))
	for second := 8; second < 15; second++ {
		if delivered[second] < 0.6*fastLaneRate {
			t.Fatalf("second %d delivered %.1f MB/s on a drifting %.1f MB/s lane", second, delivered[second]/1e6, fastLaneRate/1e6)
		}
	}
}

// A datagram lost on a lossy lane is repaired only after the lane's timeout,
// 100-200 ms at these rates, and the receiver holds everything behind it
// meanwhile (VM run of 2026-09-30: 5378 holds totalling 33.6 s in 35 s; TCP
// at 52-126 of 450 Mbit/s). Acknowledgements of later datagrams show the loss
// within a round trip.
func TestLossIsRepairedWithinARoundTrip(t *testing.T) {
	_, transit, repaired := fastLaneTransit(t, fastLaneRate, 20*time.Millisecond, 10*time.Millisecond, 0.004, 0, 12)
	median := repaired[len(repaired)/2]
	t.Logf("%d datagrams, transit p50 %s p99.9 %s; %d repaired, transit p50 %s p90 %s max %s",
		len(transit), transit[len(transit)/2], transit[len(transit)*999/1000], len(repaired), median, repaired[len(repaired)*9/10], repaired[len(repaired)-1])
	// A repaired datagram travels the path twice and its loss is learnt from
	// the acknowledgement of the datagrams behind it: about three one-way
	// delays plus the acknowledgement cadence, on top of the queue the first
	// transmission waited in.
	if median > transit[len(transit)/2]+100*time.Millisecond {
		t.Fatalf("repaired datagrams take %s at the median against %s unrepaired: repairs wait for the timeout", median, transit[len(transit)/2])
	}
}
