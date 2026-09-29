package bond_test

import (
	"container/heap"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/bond"
)

// A cold lane paces at its initial rate while four TCP flows each send an
// initial window. Dropping the whole aged backlog at once costs every flow a
// loss run and ends slow start; the queue must signal congestion with spaced
// drops instead (VM trace 20260929-121945-ramp-radio-down-main: 48 drops in
// the first second of a four-flow download).
func TestColdStartBurstIsNotDroppedAsARun(t *testing.T) {
	const (
		wireCapacity = 12500000.0
		flows        = 4
		initialBurst = 10
		datagram     = 1300
	)
	start := time.Unix(100, 0)
	peers := [2]*bond.Transport{bond.New(bond.Epoch{Boot: 1, Generation: 1}), bond.New(bond.Epoch{Boot: 2, Generation: 1})}
	for side, p := range peers {
		p.SetRemote(peers[1-side].Epoch(), true)
	}
	queue := &events{}
	heap.Init(queue)
	var available [2]time.Time
	for tick := 0; tick < 1000; tick++ {
		now := start.Add(time.Duration(tick) * time.Millisecond)
		for _, p := range peers {
			p.Path(0, 0, 80*time.Millisecond, now)
		}
		if tick == 0 {
			for flow := range flows {
				var id bond.FlowID
				id[0] = byte(flow + 1)
				for range initialBurst {
					if err := peers[0].Enqueue(make([]byte, datagram), bond.PacketMetadata{Flow: id}, now); err != nil {
						t.Fatal(err)
					}
				}
			}
		}
		for side, p := range peers {
			for _, tx := range p.Poll(now) {
				begin := maxTimeTest(now, available[side])
				available[side] = begin.Add(time.Duration(float64(len(tx.Frame.Payload)+78) / wireCapacity * float64(time.Second)))
				heap.Push(queue, event{available[side].Add(40 * time.Millisecond), 1 - side, tx.Path, tx.Frame})
			}
		}
		for queue.Len() > 0 && !(*queue)[0].at.After(now) {
			e := heap.Pop(queue).(event)
			if _, err := peers[e.to].Receive(e.path, e.frame, now); err != nil {
				t.Fatal(err)
			}
		}
	}
	// One congestion signal per flow is the most a single burst should cost.
	if drops := peers[0].Snapshot(start.Add(time.Second)).QueueDrops; drops > flows {
		t.Fatalf("initial %d-packet burst lost %d packets in its first second", flows*initialBurst, drops)
	}
}

func maxTimeTest(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
