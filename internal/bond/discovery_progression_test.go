//go:build progression

package bond_test

import (
	"container/heap"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/bond"
)

func TestCapacityDiscoveryAtCellularRTT(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		rampAt int
	}{{"cold", 0}, {"capacity-increase", 5000}} {
		t.Run(scenario.name, func(t *testing.T) {
			const wireCapacity = 12500000.0
			start := time.Unix(100, 0)
			peers := [2]*bond.Transport{bond.New(bond.Epoch{Boot: 1, Generation: 1}), bond.New(bond.Epoch{Boot: 2, Generation: 1})}
			for side, p := range peers {
				p.SetRemote(peers[1-side].Epoch(), true)
			}
			queue := &events{}
			heap.Init(queue)
			var available [2]time.Time
			var delivered int
			for tick := 0; tick < scenario.rampAt+5000; tick++ {
				now := start.Add(time.Duration(tick) * time.Millisecond)
				capacity := wireCapacity
				if tick < scenario.rampAt {
					capacity = 250000
				}
				for range 20 {
					if err := peers[0].Enqueue(make([]byte, 1200), bond.PacketMetadata{}, now); err != nil {
						t.Fatal(err)
					}
				}
				for side, p := range peers {
					p.Path(0, 0, 80*time.Millisecond, now)
					for _, tx := range p.Poll(now) {
						begin := now
						if available[side].After(begin) {
							begin = available[side]
						}
						available[side] = begin.Add(time.Duration(float64(len(tx.Frame.Payload)+78) / capacity * float64(time.Second)))
						heap.Push(queue, event{available[side].Add(40 * time.Millisecond), 1 - side, tx.Path, tx.Frame})
					}
				}
				for queue.Len() > 0 && !(*queue)[0].at.After(now) {
					e := heap.Pop(queue).(event)
					got, err := peers[e.to].Receive(e.path, e.frame, now)
					if err != nil {
						t.Fatal(err)
					}
					if e.to == 1 && tick >= scenario.rampAt+4000 {
						for _, d := range got {
							delivered += len(d.Payload)
						}
					}
				}
			}
			wireRate := float64(delivered) * 1329 / 1200
			t.Logf("final second %.2f Mbit/s", wireRate*8/1e6)
			if wireRate < 0.75*wireCapacity {
				t.Fatalf("capacity discovery still limited a healthy 100 Mbit/s lane after five seconds: %.2f Mbit/s", wireRate*8/1e6)
			}
		})
	}
}
