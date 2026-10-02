package bond_test

import (
	"container/heap"
	"fmt"
	"math"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/bond"
)

// policedSlowLane offers saturating bulk over a 0.5 Mbit/s lane whose path
// polices rather than queues: a token bucket of two datagrams refilled at the
// lane's rate forwards what it can and drops the rest without delay. The
// other lane is 100 Mbit/s with a 100 ms buffer. The slow lane's latency
// wanders as the production satellite link's does. It
// returns, per second, the wire bytes offered to the slow link as a share of
// its capacity, the share of them dropped, and the lane's target as a share
// of capacity.
func policedSlowLane(t *testing.T, seconds int) (offered, dropped, target []float64) {
	t.Helper()
	rates := []float64{62.5e3, 12.5e6}
	start := time.Unix(100, 0)
	const burst = 2 * 1378.0
	tokens := [2]float64{burst, burst}
	refilled := [2]time.Time{start, start}
	random := rand.New(rand.NewPCG(3, 0))
	peers := [2]*bond.Transport{bond.New(bond.Epoch{Boot: 1, Generation: 1}), bond.New(bond.Epoch{Boot: 2, Generation: 1})}
	for side, p := range peers {
		p.SetRemote(peers[1-side].Epoch(), true)
	}
	queue := &events{}
	heap.Init(queue)
	available := make([][2]time.Time, 2)
	var wander [2]float64
	offered, dropped, target = make([]float64, seconds), make([]float64, seconds), make([]float64, seconds)
	for tick := 0; tick < seconds*1000; tick++ {
		now := start.Add(time.Duration(tick) * time.Millisecond)
		for _, p := range peers {
			for lane := range rates {
				p.Path(bond.PathID(lane), bond.PathID(lane), 40*time.Millisecond, now)
			}
		}
		// An Ornstein-Uhlenbeck walk: 4 ms standard deviation, 50 ms memory.
		const step = 1.0 / 50
		for side := range wander {
			x := wander[side]
			x += -x*step + 4e6*math.Sqrt(2*step)*random.NormFloat64()
			wander[side] = math.Max(-8e6, math.Min(8e6, x))
		}
		for side, p := range peers {
			if side == 0 && tick >= 1000 {
				for range 40 {
					if err := p.Enqueue(make([]byte, 1300), bond.PacketMetadata{Flow: bond.FlowID{4, 6, 1}}, now); err != nil {
						break
					}
				}
			}
			for _, tx := range poll(p, now) {
				lane := int(tx.Path)
				wire := float64(len(tx.Frame.Payload) + 78)
				transit := 20 * time.Millisecond
				if lane == 0 {
					if side == 0 {
						offered[tick/1000] += wire / rates[0]
					}
					tokens[side] = math.Min(burst, tokens[side]+now.Sub(refilled[side]).Seconds()*rates[0])
					refilled[side] = now
					if tokens[side] < wire {
						if side == 0 {
							dropped[tick/1000] += wire / rates[0]
						}
						continue
					}
					tokens[side] -= wire
					transit += time.Duration(wire/rates[0]*float64(time.Second)) + time.Duration(wander[side])
					heap.Push(queue, event{now.Add(transit), 1 - side, tx.Path, tx.Frame})
					continue
				}
				begin := maxTimeTest(now, available[lane][side])
				if begin.Sub(now) > 100*time.Millisecond {
					continue
				}
				available[lane][side] = begin.Add(time.Duration(wire / rates[lane] * float64(time.Second)))
				heap.Push(queue, event{available[lane][side].Add(transit), 1 - side, tx.Path, tx.Frame})
			}
		}
		for queue.Len() > 0 && !(*queue)[0].at.After(now) {
			e := heap.Pop(queue).(event)
			_, _ = peers[e.to].Receive(e.path, e.frame, now)
		}
		if tick%1000 == 500 {
			target[tick/1000] = peers[0].Snapshot(now).Paths[0].Rate / rates[0]
		}
	}
	for i := range dropped {
		if offered[i] > 0 {
			dropped[i] /= offered[i]
		}
	}
	return offered, dropped, target
}

// A policed path drops what it cannot forward instead of queueing it:
// overload shows as loss, not as delay. The production satellite
// link behaves so (2026-09-30, 20:21: the concentrator sent 107 kB/s into a
// lane that delivered 65 for fifteen seconds, with queue delay under 7 ms; the
// loss travelled the other lane as repairs). Loss at the hold target says the
// estimate is too high, and the loss cut must take the target below what
// the path delivered, since a saturated path delivers its capacity while it
// drops the rest.
func TestPolicedLaneIsNotOverdriven(t *testing.T) {
	const seconds = 40
	offered, dropped, target := policedSlowLane(t, seconds)
	report := make([]string, 0, seconds)
	var lost, sent, worst float64
	for second := 10; second < seconds; second++ {
		report = append(report, fmt.Sprintf("%.2f/%.0f%%/%.2f", offered[second], 100*dropped[second], target[second]))
		lost += dropped[second] * offered[second]
		sent += offered[second]
		worst = math.Max(worst, dropped[second])
	}
	t.Logf("slow lane from 10 s, per second offered/capacity, dropped, target/capacity: %v", report)
	t.Logf("dropped %.1f%% of what was offered from 10 s; worst second %.0f%%", 100*lost/sent, 100*worst)
	if lost/sent > 0.02 || worst > 0.1 {
		t.Fatalf("the slow lane lost %.1f%% of its datagrams to a shallow buffer (worst second %.0f%%)", 100*lost/sent, 100*worst)
	}
}
