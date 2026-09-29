package bond_test

import (
	"container/heap"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/bond"
)

// Radio jitter reorders arrivals and makes early delivery samples uneven. A
// lane that learned its idle jitter must not mistake jitter for a capacity
// plateau and leave discovery near the initial rate (VM trace
// 20260929-123738-ramp-radio-down-c3 exited at 0.2 Mbit/s).
func TestColdDiscoveryUnderRadioJitter(t *testing.T) {
	coldDiscoveryUnderRadioJitter(t, 5*time.Second)
}

func coldDiscoveryUnderRadioJitter(t *testing.T, idle time.Duration) {
	const (
		wireCapacity = 12500000.0
		delay        = 40 * time.Millisecond
		jitter       = 30 * time.Millisecond
	)
	for seed := uint64(1); seed <= 5; seed++ {
		random := rand.New(rand.NewPCG(seed, 0))
		start := time.Unix(100, 0)
		peers := [2]*bond.Transport{bond.New(bond.Epoch{Boot: 1, Generation: 1}), bond.New(bond.Epoch{Boot: 2, Generation: 1})}
		for side, p := range peers {
			p.SetRemote(peers[1-side].Epoch(), true)
		}
		queue := &events{}
		heap.Init(queue)
		var available [2]time.Time
		var delivered int
		loadStart := int(idle.Milliseconds())
		for tick := 0; tick < loadStart+5000; tick++ {
			now := start.Add(time.Duration(tick) * time.Millisecond)
			for _, p := range peers {
				p.Path(0, 0, 2*delay, now)
			}
			for range 20 * min(1, max(0, tick-loadStart+1)) {
				if err := peers[0].Enqueue(make([]byte, 1200), bond.PacketMetadata{}, now); err != nil {
					t.Fatal(err)
				}
			}
			for side, p := range peers {
				for _, tx := range p.Poll(now) {
					begin := maxTimeTest(now, available[side])
					available[side] = begin.Add(time.Duration(float64(len(tx.Frame.Payload)+78) / wireCapacity * float64(time.Second)))
					variation := time.Duration(random.Int64N(int64(2*jitter))) - jitter
					heap.Push(queue, event{available[side].Add(delay + variation), 1 - side, tx.Path, tx.Frame})
				}
			}
			for queue.Len() > 0 && !(*queue)[0].at.After(now) {
				e := heap.Pop(queue).(event)
				got, err := peers[e.to].Receive(e.path, e.frame, now)
				if err != nil {
					t.Fatal(err)
				}
				if e.to == 1 && tick >= loadStart+4000 {
					for _, d := range got {
						delivered += len(d.Payload)
					}
				}
			}
		}
		wireRate := float64(delivered) * 1329 / 1200
		if wireRate < 0.75*wireCapacity {
			t.Errorf("seed %d: jittered cold lane reached only %.2f Mbit/s after five seconds", seed, wireRate*8/1e6)
		}
	}
}
