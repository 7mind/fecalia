package bond_test

import (
	"bytes"
	"container/heap"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/bond"
)

func TestInteractiveFailoverDelivery(t *testing.T) {
	const voiceDeadline = 150 * time.Millisecond
	const datagramLifetime = 250 * time.Millisecond
	for _, scenario := range []struct {
		name      string
		size      int
		alternate bool
		capacity  float64
		failed    bool
		deadline  time.Duration
	}{
		{"small", 224, true, 125000, true, voiceDeadline},
		{"bulk", 1200, true, 125000, true, datagramLifetime},
		{"slow-alternate", 224, true, 8000, true, datagramLifetime},
		{"no-alternate", 224, false, 125000, true, voiceDeadline},
		{"healthy", 224, true, 125000, false, voiceDeadline},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			start := time.Unix(100, 0)
			peers := [2]*bond.Transport{bond.New(bond.Epoch{Boot: 1, Generation: 1}), bond.New(bond.Epoch{Boot: 2, Generation: 1})}
			for side, peer := range peers {
				peer.SetRemote(peers[1-side].Epoch(), true)
				if err := peer.Path(0, 0, 40*time.Millisecond, start); err != nil {
					t.Fatal(err)
				}
				if scenario.alternate {
					if err := peer.Path(1, 1, 80*time.Millisecond, start); err != nil {
						t.Fatal(err)
					}
				}
			}
			payload := bytes.Repeat([]byte{0x71}, scenario.size)
			if err := peers[0].Enqueue(payload, bond.PacketMetadata{Flow: bond.FlowID{1}}, start); err != nil {
				t.Fatal(err)
			}
			queue := &events{}
			heap.Init(queue)
			var available [2][2]time.Time
			var delivered int
			var arrived time.Duration
			for tick := 0; tick <= 500; tick++ {
				now := start.Add(time.Duration(tick) * time.Millisecond)
				for side, peer := range peers {
					for _, tx := range poll(peer, now) {
						if scenario.failed && side == 0 && tx.Path == 0 {
							continue
						}
						path := int(tx.Path)
						begin := now
						if available[side][path].After(begin) {
							begin = available[side][path]
						}
						wireBytes := len(tx.Frame.Payload) + 78
						available[side][path] = begin.Add(time.Duration(float64(wireBytes) / scenario.capacity * float64(time.Second)))
						delay := time.Duration(20+20*path) * time.Millisecond
						heap.Push(queue, event{available[side][path].Add(delay), 1 - side, tx.Path, tx.Frame})
					}
				}
				for queue.Len() > 0 && !(*queue)[0].at.After(now) {
					e := heap.Pop(queue).(event)
					items, err := peers[e.to].Receive(e.path, e.frame, now)
					if err != nil {
						t.Fatal(err)
					}
					for _, item := range items {
						if e.to != 1 || !bytes.Equal(item.Payload, payload) {
							t.Fatal("delivery changed the destination or encrypted datagram")
						}
						delivered++
						arrived = now.Sub(start)
					}
				}
			}
			want := 1
			if scenario.failed && !scenario.alternate {
				want = 0
			}
			if delivered != want {
				t.Fatalf("delivered %d copies of one datagram; want %d", delivered, want)
			}
			if delivered > 0 && arrived >= scenario.deadline {
				t.Fatalf("delivery took %s, requires less than %s", arrived, scenario.deadline)
			}
			if want == 0 && peers[0].Snapshot(start.Add(500*time.Millisecond)).Expired != 1 {
				t.Fatal("unreachable datagram did not expire within its bounded lifetime")
			}
		})
	}
}
