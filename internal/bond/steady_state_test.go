package bond_test

import (
	"container/heap"
	"sort"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/bond"
)

// A backlogged lane on a steady path must use the path without keeping a
// standing queue in the path's own buffer, where small datagrams have no
// priority over bulk (VM trace 20260929-133204-fast-up-c8: 15-30 ms forward
// queue on a jitter-free 32 Mbit/s link).
func TestSteadyPathIsUsedWithoutStandingLinkQueue(t *testing.T) {
	for _, scenario := range []struct {
		name     string
		capacity float64
		delay    time.Duration
	}{{"100Mbit", 12500000, 25 * time.Millisecond}, {"1.25Mbit", 156250, 40 * time.Millisecond}} {
		t.Run(scenario.name, func(t *testing.T) {
			utilization, queueP90 := steadyPath(t, scenario.capacity, scenario.delay)
			t.Logf("utilization %.3f, link queue p90 %s", utilization, queueP90)
			if utilization < 0.93 {
				t.Errorf("steady lane used %.1f%% of wire capacity", 100*utilization)
			}
			if queueP90 > 15*time.Millisecond {
				t.Errorf("steady lane kept a %s p90 queue in the link", queueP90)
			}
		})
	}
}

func steadyPath(t *testing.T, capacity float64, delay time.Duration) (float64, time.Duration) {
	t.Helper()
	const (
		warmup   = 10000
		measured = 10000
		datagram = 1200
	)
	start := time.Unix(100, 0)
	peers := [2]*bond.Transport{bond.New(bond.Epoch{Boot: 1, Generation: 1}), bond.New(bond.Epoch{Boot: 2, Generation: 1})}
	for side, p := range peers {
		p.SetRemote(peers[1-side].Epoch(), true)
	}
	queue := &events{}
	heap.Init(queue)
	var available [2]time.Time
	var delivered int
	var linkQueue []time.Duration
	offered := int(2*capacity/1000/datagram) + 1
	for tick := 0; tick < 3000+warmup+measured; tick++ {
		now := start.Add(time.Duration(tick) * time.Millisecond)
		for _, p := range peers {
			p.Path(0, 0, 2*delay, now)
		}
		if tick >= 3000 {
			for range offered {
				if err := peers[0].Enqueue(make([]byte, datagram), bond.PacketMetadata{}, now); err != nil {
					t.Fatal(err)
				}
			}
		}
		for side, p := range peers {
			for _, tx := range p.Poll(now) {
				begin := maxTimeTest(now, available[side])
				if side == 0 && tick >= 3000+warmup {
					linkQueue = append(linkQueue, begin.Sub(now))
				}
				available[side] = begin.Add(time.Duration(float64(len(tx.Frame.Payload)+78) / capacity * float64(time.Second)))
				heap.Push(queue, event{available[side].Add(delay), 1 - side, tx.Path, tx.Frame})
			}
		}
		for queue.Len() > 0 && !(*queue)[0].at.After(now) {
			e := heap.Pop(queue).(event)
			got, err := peers[e.to].Receive(e.path, e.frame, now)
			if err != nil {
				t.Fatal(err)
			}
			if e.to == 1 && tick >= 3000+warmup {
				for _, d := range got {
					delivered += len(d.Payload) + 129
				}
			}
		}
	}
	sort.Slice(linkQueue, func(i, j int) bool { return linkQueue[i] < linkQueue[j] })
	return float64(delivered) / (capacity * measured / 1000), linkQueue[len(linkQueue)*9/10]
}
