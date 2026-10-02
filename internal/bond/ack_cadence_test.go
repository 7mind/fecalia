package bond_test

import (
	"container/heap"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/bond"
)

// Acknowledgements bypass pacing, so on a slow lane a fixed cadence takes a
// fixed slice of the reverse direction: every 25 ms it is 15% of a 0.4 Mbit/s
// lane, which voice and TCP then cannot use (VM accounting of run
// 20260929-125805-adaptive: 7% of a 1.65 Mbit/s uplink).
func TestSlowLaneAcknowledgementShareIsBounded(t *testing.T) {
	for _, scenario := range []struct {
		name     string
		capacity float64
		share    float64
	}{{"0.4Mbit", 50000, 0.09}, {"100Mbit", 12500000, 0.02}} {
		t.Run(scenario.name, func(t *testing.T) {
			start := time.Unix(100, 0)
			peers := [2]*bond.Transport{bond.New(bond.Epoch{Boot: 1, Generation: 1}), bond.New(bond.Epoch{Boot: 2, Generation: 1})}
			for side, p := range peers {
				p.SetRemote(peers[1-side].Epoch(), true)
			}
			queue := &events{}
			heap.Init(queue)
			var available [2]time.Time
			var acknowledgementBytes, delivered int
			offered := int(2*scenario.capacity/1000/1200) + 1
			for tick := 0; tick < 10000; tick++ {
				now := start.Add(time.Duration(tick) * time.Millisecond)
				for _, p := range peers {
					p.Path(0, 0, 40*time.Millisecond, now)
				}
				for range offered {
					if err := peers[0].Enqueue(make([]byte, 1200), bond.PacketMetadata{}, now); err != nil {
						t.Fatal(err)
					}
				}
				for side, p := range peers {
					for _, tx := range poll(p, now) {
						wire := len(tx.Frame.Payload) + 78
						if side == 1 && tx.Frame.ControlType == bond.ACKType && tick >= 5000 {
							acknowledgementBytes += wire
						}
						begin := maxTimeTest(now, available[side])
						available[side] = begin.Add(time.Duration(float64(wire) / scenario.capacity * float64(time.Second)))
						heap.Push(queue, event{available[side].Add(20 * time.Millisecond), 1 - side, tx.Path, tx.Frame})
					}
				}
				for queue.Len() > 0 && !(*queue)[0].at.After(now) {
					e := heap.Pop(queue).(event)
					got, err := peers[e.to].Receive(e.path, e.frame, now)
					if err != nil {
						t.Fatal(err)
					}
					if e.to == 1 && tick >= 5000 {
						for _, d := range got {
							delivered += len(d.Payload) + 129
						}
					}
				}
			}
			share := float64(acknowledgementBytes) / (scenario.capacity * 5)
			utilization := float64(delivered) / (scenario.capacity * 5)
			t.Logf("acknowledgements use %.1f%% of the reverse lane; forward utilization %.1f%%", 100*share, 100*utilization)
			if share > scenario.share {
				t.Errorf("acknowledgements use %.1f%% of the reverse lane", 100*share)
			}
			if utilization < 0.9 {
				t.Errorf("forward lane delivered %.1f%% of its capacity", 100*utilization)
			}
		})
	}
}
