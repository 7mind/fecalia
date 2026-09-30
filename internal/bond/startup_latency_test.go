package bond_test

import (
	"container/heap"
	"encoding/binary"
	"sort"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/bond"
)

func TestStandbyLinkStartupDoesNotFloodVoice(t *testing.T) {
	start := time.Unix(100, 0)
	peers := [2]*bond.Transport{bond.New(bond.Epoch{Boot: 1, Generation: 1}), bond.New(bond.Epoch{Boot: 2, Generation: 1})}
	for side, p := range peers {
		p.SetRemote(peers[1-side].Epoch(), true)
	}
	queue := &events{}
	heap.Init(queue)
	var available [2]time.Time
	var delays []time.Duration
	for tick := 0; tick < 2000; tick++ {
		now := start.Add(time.Duration(tick) * time.Millisecond)
		if tick < 1000 {
			if err := peers[0].Enqueue(make([]byte, 1200), bond.PacketMetadata{}, now); err != nil {
				t.Fatal(err)
			}
			if tick%10 == 0 {
				voice := make([]byte, 224)
				binary.BigEndian.PutUint64(voice, uint64(tick))
				if err := peers[0].Enqueue(voice, bond.PacketMetadata{Flow: bond.FlowID{1}}, now); err != nil {
					t.Fatal(err)
				}
			}
		}
		for side, p := range peers {
			p.Path(0, 0, 40*time.Millisecond, now)
			for _, tx := range p.Poll(now) {
				begin := now
				if available[side].After(begin) {
					begin = available[side]
				}
				available[side] = begin.Add(time.Duration(float64(len(tx.Frame.Payload)+78) / 50000 * float64(time.Second)))
				heap.Push(queue, event{available[side].Add(20 * time.Millisecond), 1 - side, tx.Path, tx.Frame})
			}
		}
		for queue.Len() > 0 && !(*queue)[0].at.After(now) {
			e := heap.Pop(queue).(event)
			delivered, err := peers[e.to].Receive(e.path, e.frame, now)
			if err != nil {
				t.Fatal(err)
			}
			for _, d := range delivered {
				if len(d.Payload) == 224 {
					sent := int(binary.BigEndian.Uint64(d.Payload))
					delays = append(delays, time.Duration(tick-sent)*time.Millisecond)
				}
			}
		}
	}
	// Voice that waited behind the first bulk datagrams gives way to the
	// datagrams after it rather than delaying them.
	if len(delays) < 97 {
		t.Fatalf("delivered %d/100 voice packets", len(delays))
	}
	sort.Slice(delays, func(i, j int) bool { return delays[i] < delays[j] })
	p99 := delays[len(delays)*99/100]
	t.Logf("startup voice p99 one-way delay %s", p99)
	if p99 > 100*time.Millisecond {
		t.Fatalf("initial pacing flooded the 0.4 Mbit/s link: p99=%s", p99)
	}
}
