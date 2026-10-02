package bond_test

import (
	"container/heap"
	"strings"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/bond"
)

func TestLossFreeRadioJitterDoesNotCauseRepeatedRepairs(t *testing.T) {
	start := time.Unix(100, 0)
	peers := [2]*bond.Transport{bond.New(bond.Epoch{Boot: 1, Generation: 1}), bond.New(bond.Epoch{Boot: 2, Generation: 1})}
	for side, peer := range peers {
		peer.SetRemote(peers[1-side].Epoch(), true)
	}
	queue := &events{}
	heap.Init(queue)
	var transmissions [2]int
	var available [2]time.Time
	var before uint64
	var delivered int
	for tick := 0; tick < 15000; tick++ {
		now := start.Add(time.Duration(tick) * time.Millisecond)
		if tick%2 == 0 {
			if err := peers[0].Enqueue(make([]byte, 800), bond.PacketMetadata{}, now); err != nil {
				t.Fatal(err)
			}
		}
		for side, peer := range peers {
			peer.Path(0, 0, 80*time.Millisecond, now)
			for _, tx := range poll(peer, now) {
				transmissions[side]++
				delay := time.Duration(10+transmissions[side]*17%61) * time.Millisecond
				begin := now
				if available[side].After(begin) {
					begin = available[side]
				}
				// A 100 Mbit/s link with 3.2 Mbit/s offered application traffic.
				available[side] = begin.Add(time.Duration(float64(len(tx.Frame.Payload)+78) / 12500000 * float64(time.Second)))
				heap.Push(queue, event{available[side].Add(delay), 1 - side, tx.Path, tx.Frame})
			}
		}
		for queue.Len() > 0 && !(*queue)[0].at.After(now) {
			e := heap.Pop(queue).(event)
			ds, err := peers[e.to].Receive(e.path, e.frame, now)
			if err != nil {
				if e.frame.ControlType == bond.ACKType && strings.Contains(err.Error(), "stale or impossible ACK") {
					continue
				}
				t.Fatal(err)
			}
			if e.to == 1 && tick >= 10000 {
				delivered += len(ds)
			}
		}
		if tick == 9999 {
			before = peers[0].Snapshot(now).Paths[0].Retransmits
		}
	}
	state := peers[0].Snapshot(start.Add(15 * time.Second))
	t.Logf("final 5s: delivered=%d, repairs=%d, feedback RTT=%s, physical RTT=%s", delivered, state.Paths[0].Retransmits-before, state.Paths[0].FeedbackRTT, state.Paths[0].RTT)
	const offeredPackets = 2500
	if delivered < offeredPackets*95/100 {
		t.Errorf("loss-free underloaded path delivered only %d of approximately %d packets", delivered, offeredPackets)
	}
	if repairs := state.Paths[0].Retransmits - before; repairs > offeredPackets/100 {
		t.Errorf("loss-free underloaded path retransmitted %d packets in 5s: %+v", repairs, state.Paths[0])
	}
}
