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
// restarted, not the path: traffic must stay within the physical budget.
func TestPeerRestartKeepsTrafficWithinThePathBudget(t *testing.T) {
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
	var slowDelivered float64
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
			for _, tx := range poll(p, now) {
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
			got, _ := peers[e.to].Receive(e.path, e.frame, now)
			if e.to == 1 && e.path == 0 && tick >= restartAt {
				for _, d := range got {
					slowDelivered += float64(len(d.Payload))
				}
			}
		}
	}
	const dataBytes = 1300.0
	const dataWireBytes = dataBytes + bond.Overhead + 28
	reference := rates[0] * dataBytes / dataWireBytes
	t.Logf("after restart: slow lane delivered %.0f B/s against %.0f B/s payload budget; %.0f%% of offered bytes dropped", slowDelivered/5, reference, 100*dropped/offered)
	if offered == 0 || dropped/offered > 0.15 {
		t.Fatalf("slow lane overdriven after a peer restart: offered %.0f bytes, %.0f%% dropped", offered, 100*dropped/offered)
	}
	if slowDelivered/5 < .75*reference {
		t.Errorf("slow lane carried %.0f B/s after restart, below 75%% of %.0f B/s payload budget", slowDelivered/5, reference)
	}
}
