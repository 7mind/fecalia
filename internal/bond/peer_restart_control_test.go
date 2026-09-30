package bond_test

import (
	"container/heap"
	"math"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/bond"
)

// When the peer restarts, the lanes forget their estimates but kept their
// targets and stayed out of discovery: with no estimate, the target grew 6%
// every control interval, with nothing but a congestion signal to stop it
// and none of discovery's bounds. On a path that drops rather than queues
// there is no delay signal, and the loss went uncounted (production,
// 2026-09-30 22:40, after the edge was redeployed: the concentrator raised
// its satellite lane's target from 80 kB/s to 7.4 MB/s in fifteen seconds
// while the lane delivered 65, and 97% of what it sent was lost). The peer
// restarted, not the path: the estimate stands.
func TestPeerRestartKeepsTheCapacityEstimate(t *testing.T) {
	rates := []float64{62.5e3, 12.5e6}
	const burst = 2 * 1378.0
	start := time.Unix(100, 0)
	peers := [2]*bond.Transport{bond.New(bond.Epoch{Boot: 1, Generation: 1}), bond.New(bond.Epoch{Boot: 2, Generation: 1})}
	for side, p := range peers {
		p.SetRemote(peers[1-side].Epoch(), true)
	}
	queue := &events{}
	heap.Init(queue)
	var available [2]time.Time
	tokens := [2]float64{burst, burst}
	refilled := [2]time.Time{start, start}
	var offered, dropped float64
	var peak float64
	const restartAt = 10000
	for tick := 0; tick < restartAt+5000; tick++ {
		now := start.Add(time.Duration(tick) * time.Millisecond)
		if tick == restartAt {
			// The far end comes back with a new boot epoch; this end adopts it.
			peers[1] = bond.New(bond.Epoch{Boot: 3, Generation: 1})
			peers[1].SetRemote(peers[0].Epoch(), true)
			peers[0].SetRemote(peers[1].Epoch(), true)
			queue = &events{}
			heap.Init(queue)
			for lane, path := range peers[0].Snapshot(now).Paths {
				if path.Capacity == 0 {
					t.Errorf("lane %d forgot its capacity when the peer restarted", lane)
				}
			}
		}
		for _, p := range peers {
			for lane := range rates {
				p.Path(bond.PathID(lane), bond.PathID(lane), 40*time.Millisecond, now)
			}
		}
		for side, p := range peers {
			if side == 0 && tick >= 1000 {
				for range 40 {
					if err := p.Enqueue(make([]byte, 1300), bond.PacketMetadata{Flow: bond.FlowID{4, 6, 1}}, now); err != nil {
						break
					}
				}
			}
			for _, tx := range p.Poll(now) {
				lane := int(tx.Path)
				wire := float64(len(tx.Frame.Payload) + 78)
				if lane == 0 {
					// A policed path: forwarded within its rate, dropped beyond it.
					if side == 0 && tick >= restartAt {
						offered += wire
					}
					tokens[side] = math.Min(burst, tokens[side]+now.Sub(refilled[side]).Seconds()*rates[0])
					refilled[side] = now
					if tokens[side] < wire {
						if side == 0 && tick >= restartAt {
							dropped += wire
						}
						continue
					}
					tokens[side] -= wire
					heap.Push(queue, event{now.Add(20*time.Millisecond + time.Duration(wire/rates[0]*float64(time.Second))), 1 - side, tx.Path, tx.Frame})
					continue
				}
				begin := maxTimeTest(now, available[side])
				if begin.Sub(now) > 100*time.Millisecond {
					continue
				}
				available[side] = begin.Add(time.Duration(wire / rates[1] * float64(time.Second)))
				heap.Push(queue, event{available[side].Add(20 * time.Millisecond), 1 - side, tx.Path, tx.Frame})
			}
		}
		for queue.Len() > 0 && !(*queue)[0].at.After(now) {
			e := heap.Pop(queue).(event)
			_, _ = peers[e.to].Receive(e.path, e.frame, now)
		}
		if tick >= restartAt && tick%100 == 0 {
			peak = math.Max(peak, peers[0].Snapshot(now).Paths[0].Rate)
		}
	}
	t.Logf("after the restart the slow lane's target peaked at %.2f of its capacity; %.0f%% of what was offered to the path was dropped", peak/rates[0], 100*dropped/offered)
	if peak > 1.5*rates[0] || dropped/offered > 0.15 {
		t.Fatalf("slow lane overdriven after a peer restart: target peaked at %.1f of capacity, %.0f%% dropped", peak/rates[0], 100*dropped/offered)
	}
}
