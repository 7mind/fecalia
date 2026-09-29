//go:build progression

package bond_test

import (
	"container/heap"
	"encoding/binary"
	"sort"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/bond"
)

func TestSparseSmallFlowsSurviveSustainedACKBacklog(t *testing.T) {
	start := time.Unix(100, 0)
	peers := [2]*bond.Transport{bond.New(bond.Epoch{Boot: 1, Generation: 1}), bond.New(bond.Epoch{Boot: 2, Generation: 1})}
	for side, p := range peers {
		p.SetRemote(peers[1-side].Epoch(), true)
	}
	queue := &events{}
	heap.Init(queue)
	var available [2]time.Time
	var delays []time.Duration
	for tick := 0; tick < 5500; tick++ {
		now := start.Add(time.Duration(tick) * time.Millisecond)
		for side, p := range peers {
			if tick < 5000 {
				if err := p.Enqueue(make([]byte, 1200), bond.PacketMetadata{}, now); err != nil {
					t.Fatal(err)
				}
				if tick%10 == 0 {
					voice := make([]byte, 224)
					binary.BigEndian.PutUint64(voice, uint64(tick))
					if err := p.Enqueue(voice, bond.PacketMetadata{Flow: bond.FlowID{byte(1 + tick/10%2)}}, now); err != nil {
						t.Fatal(err)
					}
					ack := bond.PacketMetadata{Flow: bond.FlowID{3}, ACK: bond.TCPACK{Eligible: true, Sequence: 1, Acknowledgement: uint32(tick + 1), Window: 4096}}
					if err := p.Enqueue(make([]byte, 96), ack, now); err != nil {
						t.Fatal(err)
					}
				}
			}
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
			got, err := peers[e.to].Receive(e.path, e.frame, now)
			if err != nil {
				t.Fatal(err)
			}
			for _, d := range got {
				if len(d.Payload) == 224 {
					sent := int(binary.BigEndian.Uint64(d.Payload))
					if sent >= 4000 {
						delays = append(delays, time.Duration(tick-sent)*time.Millisecond)
					}
				}
			}
		}
	}
	sort.Slice(delays, func(i, j int) bool { return delays[i] < delays[j] })
	if len(delays) == 0 {
		t.Fatal("no voice delivered")
	}
	p99 := delays[len(delays)*99/100]
	t.Logf("delivered %d/200 voice packets, one-way p99 %s", len(delays), p99)
	if len(delays) != 200 || p99 >= 75*time.Millisecond {
		t.Fatal("sustained ACK backlog displaced sparse small flows on a 0.4 Mbit/s link")
	}
}
