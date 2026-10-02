package bond_test

import (
	"container/heap"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/bond"
)

// A constant-rate sender offers what the lanes could carry and cannot slow
// down. The lanes must stay in use: 80% UDP goodput needs 92.4% of the wire
// as full datagrams (VM run 20260929-153950-udp: a 1.65 Mbit/s radio uplink
// carried 60%).
func TestConstantRateDatagramsFillRadioUplink(t *testing.T) {
	rates := []float64{50000, 156250}
	delays := []time.Duration{20 * time.Millisecond, 40 * time.Millisecond}
	jitters := []time.Duration{10 * time.Millisecond, 30 * time.Millisecond}
	losses := []float64{0.004, 0}
	const (
		datagram = 1339 + 32 // tunnel MTU inside WireGuard
		duration = 30000
		measured = 10000
	)
	start := time.Unix(100, 0)
	random := rand.New(rand.NewPCG(1, 0))
	peers := [2]*bond.Transport{bond.New(bond.Epoch{Boot: 1, Generation: 1}), bond.New(bond.Epoch{Boot: 2, Generation: 1})}
	for side, p := range peers {
		p.SetRemote(peers[1-side].Epoch(), true)
	}
	queue := &events{}
	heap.Init(queue)
	available := make([][2]time.Time, len(rates))
	capacity := rates[0] + rates[1]
	var delivered int
	var credit float64
	for tick := 0; tick < duration; tick++ {
		now := start.Add(time.Duration(tick) * time.Millisecond)
		for _, p := range peers {
			for lane := range rates {
				p.Path(bond.PathID(lane), bond.PathID(lane), 2*delays[lane], now)
			}
		}
		if tick >= 5000 {
			credit += capacity / 1000
			for ; credit >= datagram+129; credit -= datagram + 129 {
				if err := peers[0].Enqueue(make([]byte, datagram), bond.PacketMetadata{Flow: bond.FlowID{4, 17, 1}}, now); err != nil {
					t.Fatal(err)
				}
			}
		}
		for side, p := range peers {
			for _, tx := range poll(p, now) {
				lane := int(tx.Path)
				begin := maxTimeTest(now, available[lane][side])
				if begin.Sub(now) > 100*time.Millisecond || random.Float64() < losses[lane] {
					continue
				}
				available[lane][side] = begin.Add(time.Duration(float64(len(tx.Frame.Payload)+78) / rates[lane] * float64(time.Second)))
				delay := delays[lane] + time.Duration(random.Int64N(int64(2*jitters[lane]))) - jitters[lane]
				heap.Push(queue, event{available[lane][side].Add(delay), 1 - side, tx.Path, tx.Frame})
			}
		}
		for queue.Len() > 0 && !(*queue)[0].at.After(now) {
			e := heap.Pop(queue).(event)
			got, err := peers[e.to].Receive(e.path, e.frame, now)
			if err != nil {
				t.Fatal(err)
			}
			if e.to == 1 && tick >= measured {
				for _, d := range got {
					delivered += len(d.Payload) + 129
				}
			}
		}
	}
	snapshot := peers[0].Snapshot(start.Add(duration * time.Millisecond))
	utilization := float64(delivered) / (capacity * (duration - measured) / 1000)
	t.Logf("wire utilization %.3f; drops %d (aqm %d) expired %d", utilization, snapshot.QueueDrops, snapshot.AQMDrops, snapshot.Expired)
	for _, lane := range snapshot.Paths {
		t.Logf("lane %d: target %.1f kB/s send %.1f delivery %.1f repairs %d", lane.Path, lane.Rate/1e3, lane.SendRate/1e3, lane.DeliveryRate/1e3, lane.Retransmits)
	}
	if utilization < 0.925 {
		t.Fatalf("constant-rate datagrams used %.1f%% of the wire", 100*utilization)
	}
}
