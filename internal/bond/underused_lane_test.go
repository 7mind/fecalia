package bond_test

import (
	"container/heap"
	"encoding/binary"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/bond"
)

// A sender offers a third of what a 300 Mbit/s lane carries, in bursts as a
// TCP transfer does; the lane loses 0.4% at random. A round of a hundred
// datagrams then holds three losses once in fifty rounds, and each passed for
// material loss: the capacity estimate became the sender's rate, the target
// was held at 95% of it, the tunnel's queue built and its drop schedule
// discarded the transfer's datagrams (VM runs of 2026-09-30: lane targets of
// 6-30 of 37.5 MB/s during a TCP transfer, 27 loss cuts in 30 s).
func TestUnderusedLossyLaneDeliversBurstyBulk(t *testing.T) {
	const (
		rate    = 37.5e6
		delay   = 20 * time.Millisecond
		jitter  = 10 * time.Millisecond
		loss    = 0.004
		settled = 10000
	)
	start := time.Unix(100, 0)
	random := rand.New(rand.NewPCG(1, 0))
	peers := [2]*bond.Transport{bond.New(bond.Epoch{Boot: 1, Generation: 1}), bond.New(bond.Epoch{Boot: 2, Generation: 1})}
	for side, p := range peers {
		p.SetRemote(peers[1-side].Epoch(), true)
	}
	queue := &events{}
	heap.Init(queue)
	var available [2]time.Time
	var drift [2]time.Duration
	var offered, delivered uint64
	seen := make(map[uint64]bool)
	var dropsBefore uint64
	for tick := 0; tick < 30500; tick++ {
		now := start.Add(time.Duration(tick) * time.Millisecond)
		for _, p := range peers {
			p.Path(0, 0, 2*delay, now)
		}
		for side, p := range peers {
			if side == 0 && tick >= 1000 && tick < 30000 && tick%10 == 0 {
				for ordinal := range 92 {
					payload := make([]byte, 1300)
					binary.BigEndian.PutUint64(payload, uint64(tick*92+ordinal))
					if err := p.Enqueue(payload, bond.PacketMetadata{Flow: bond.FlowID{4, 6, 1}}, now); err != nil {
						break
					}
					if tick >= settled {
						offered += uint64(len(payload))
					}
				}
			}
			if side == 0 && tick == settled {
				dropsBefore = p.Snapshot(now).AQMDrops
			}
			for _, tx := range poll(p, now) {
				begin := maxTimeTest(now, available[side])
				if begin.Sub(now) > 100*time.Millisecond {
					continue
				}
				available[side] = begin.Add(time.Duration(float64(len(tx.Frame.Payload)+78) / rate * float64(time.Second)))
				if side == 0 && random.Float64() < loss {
					continue
				}
				fresh := time.Duration(random.Int64N(int64(2*jitter))) - jitter
				drift[side] = time.Duration(0.95*float64(drift[side]) + 0.05*float64(fresh))
				heap.Push(queue, event{available[side].Add(delay + drift[side]), 1 - side, tx.Path, tx.Frame})
			}
		}
		for queue.Len() > 0 && !(*queue)[0].at.After(now) {
			e := heap.Pop(queue).(event)
			got, err := peers[e.to].Receive(e.path, e.frame, now)
			if err != nil {
				continue
			}
			for _, datagram := range got {
				if e.to == 1 && len(datagram.Payload) == 1300 {
					id := binary.BigEndian.Uint64(datagram.Payload)
					if id/92 >= settled {
						if seen[id] {
							t.Fatalf("duplicate bulk delivery %d", id)
						}
						seen[id] = true
						delivered += uint64(len(datagram.Payload))
					}
				}
			}
		}
	}
	dropped := peers[0].Snapshot(start.Add(30500*time.Millisecond)).AQMDrops - dropsBefore
	const expected = 2000 * 92 * 1300
	if offered != expected {
		t.Fatalf("fixture admitted %d of %d bulk bytes", offered, expected)
	}
	t.Logf("delivered %.2f%% of %.1f MB/s offered; %d datagrams dropped by the queue's schedule", 100*float64(delivered)/float64(offered), float64(offered)/20e6, dropped)
	if delivered < offered*99/100 || dropped > 0 {
		t.Fatalf("delivered %d of %d bulk bytes; %d datagrams dropped", delivered, offered, dropped)
	}
}
