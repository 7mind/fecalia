package bond_test

import (
	"container/heap"
	"encoding/binary"
	"strings"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/bond"
)

func TestReorderedDataBeyondACKBitmapIsDelivered(t *testing.T) {
	now := time.Unix(100, 0)
	a := bond.New(bond.Epoch{Boot: 1, Generation: 1})
	b := bond.New(bond.Epoch{Boot: 2, Generation: 1})
	a.SetRemote(b.Epoch(), true)
	b.SetRemote(a.Epoch(), true)
	a.Path(0, 0, 60*time.Millisecond, now)
	b.Path(0, 0, 60*time.Millisecond, now)
	if err := a.Enqueue(make([]byte, 1200), bond.PacketMetadata{}, now); err != nil {
		t.Fatal(err)
	}
	first := a.Poll(now)[0].Frame
	for seq := uint64(2); seq <= 100; seq++ {
		packet := first
		packet.Seq = seq
		packet.Payload = append([]byte(nil), first.Payload...)
		binary.BigEndian.PutUint64(packet.Payload[35:], seq)
		binary.BigEndian.PutUint64(packet.Payload[43:], seq)
		if _, err := b.Receive(0, packet, now); err != nil {
			t.Fatal(err)
		}
	}
	deliveries, err := b.Receive(0, first, now.Add(20*time.Millisecond))
	if err != nil || len(deliveries) != 1 || deliveries[0].Sequence != 1 {
		t.Fatalf("valid reordered packet discarded outside ACK bitmap: deliveries=%v err=%v", deliveries, err)
	}
	if duplicate, err := b.Receive(0, first, now.Add(21*time.Millisecond)); err != nil || len(duplicate) != 0 {
		t.Fatalf("replayed attempt delivered: %v, %v", duplicate, err)
	}
}

func TestDeliveredPacketsOutsideLaneACKBitmapReleaseWindow(t *testing.T) {
	now := time.Unix(100, 0)
	a := bond.New(bond.Epoch{Boot: 1, Generation: 1})
	b := bond.New(bond.Epoch{Boot: 2, Generation: 1})
	a.SetRemote(b.Epoch(), true)
	b.SetRemote(a.Epoch(), true)
	for i := 0; i < 80; i++ {
		now = now.Add(11 * time.Millisecond)
		a.Path(0, 0, time.Second, now)
		b.Path(0, 0, time.Second, now)
		if err := a.Enqueue(make([]byte, 1200), bond.PacketMetadata{}, now); err != nil {
			t.Fatal(err)
		}
		for _, tx := range a.Poll(now) {
			if _, err := b.Receive(tx.Path, tx.Frame, now); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, tx := range b.Poll(now.Add(10 * time.Millisecond)) {
		if _, err := a.Receive(tx.Path, tx.Frame, now.Add(20*time.Millisecond)); err != nil {
			t.Fatal(err)
		}
	}
	if got := a.Snapshot(now).Paths[0].InFlight; got != 0 {
		t.Fatalf("receiver confirmed all 80 packets, but %d wire bytes still occupy the congestion window", got)
	}
}

func TestBulkACKCadenceBoundsReverseBandwidth(t *testing.T) {
	now := time.Unix(100, 0)
	a := bond.New(bond.Epoch{Boot: 1, Generation: 1})
	b := bond.New(bond.Epoch{Boot: 2, Generation: 1})
	a.SetRemote(b.Epoch(), true)
	b.SetRemote(a.Epoch(), true)
	a.Path(0, 0, 60*time.Millisecond, now)
	b.Path(0, 0, 60*time.Millisecond, now)
	if err := a.Enqueue(make([]byte, 1200), bond.PacketMetadata{}, now); err != nil {
		t.Fatal(err)
	}
	first := a.Poll(now)[0].Frame
	acks := 0
	for seq := uint64(1); seq <= 1000; seq++ {
		packet := first
		packet.Seq = seq
		packet.Payload = append([]byte(nil), first.Payload...)
		binary.BigEndian.PutUint64(packet.Payload[35:], seq)
		binary.BigEndian.PutUint64(packet.Payload[43:], seq)
		at := now.Add(time.Duration(seq) * 100 * time.Microsecond)
		if _, err := b.Receive(0, packet, at); err != nil {
			t.Fatal(err)
		}
		for _, tx := range b.Poll(at) {
			if tx.Frame.ControlType == bond.ACKType {
				acks++
			}
		}
	}
	const maxACKWireBytes = 7200 // 0.6% of 1.2 MB delivered data.
	const ackWireBytes = 193
	if acks*ackWireBytes > maxACKWireBytes || acks == 0 {
		t.Fatalf("100 ms of bulk traffic produced %d ACKs; feedback exceeds the reverse bandwidth budget", acks)
	}
}

func TestIdleJitterDoesNotCollapsePacingRate(t *testing.T) {
	runJitterCapacity(t, false, 15, 30, 1250000, 0)
}

func TestIdleKeepaliveTimeoutPreservesPacingTarget(t *testing.T) {
	start := time.Unix(100, 0)
	a := bond.New(bond.Epoch{Boot: 1, Generation: 1})
	a.SetRemote(bond.Epoch{Boot: 2, Generation: 1}, true)
	a.Path(0, 0, 80*time.Millisecond, start)
	initial := a.Snapshot(start).Paths[0].Rate
	a.Poll(start.Add(200 * time.Millisecond)) // Drop the idle keepalive.
	a.Poll(start.Add(500 * time.Millisecond))
	state := a.Snapshot(start.Add(500 * time.Millisecond)).Paths[0]
	if state.Up {
		t.Fatal("missing keepalive ACK must still stall the path")
	}
	if state.Rate != initial {
		t.Fatalf("no application traffic was offered, but a keepalive timeout changed pacing: %.0f -> %.0f", initial, state.Rate)
	}
}

func TestReplicationBudgetTracksPacingCapacity(t *testing.T) {
	start := time.Unix(100, 0)
	peers := [2]*bond.Transport{bond.New(bond.Epoch{Boot: 1, Generation: 1}), bond.New(bond.Epoch{Boot: 2, Generation: 1})}
	for side, peer := range peers {
		peer.SetRemote(peers[1-side].Epoch(), true)
	}
	queue := &events{}
	heap.Init(queue)
	for tick := 0; tick < 2000; tick++ {
		now := start.Add(time.Duration(tick) * time.Millisecond)
		if tick%5 == 0 {
			if err := peers[0].Enqueue(make([]byte, 100), bond.PacketMetadata{}, now); err != nil {
				t.Fatal(err)
			}
		}
		for side, peer := range peers {
			for path := bond.PathID(0); path < 2; path++ {
				peer.Path(path, path, 40*time.Millisecond, now)
			}
			for _, tx := range peer.Poll(now) {
				heap.Push(queue, event{now.Add(20 * time.Millisecond), 1 - side, tx.Path, tx.Frame})
			}
		}
		for queue.Len() > 0 && !(*queue)[0].at.After(now) {
			e := heap.Pop(queue).(event)
			if _, err := peers[e.to].Receive(e.path, e.frame, now); err != nil {
				t.Fatal(err)
			}
		}
	}
	var copiedBytes uint64
	for _, path := range peers[0].Snapshot(start.Add(2 * time.Second)).Paths {
		copiedBytes += path.Retransmits * (100 + 129)
	}
	const capacityShareWithBurst = 55000 // 10% of two 125 kB/s lanes for 2 s, plus burst allowance.
	if copiedBytes > capacityShareWithBurst {
		t.Fatalf("small-packet copies consumed %d wire bytes, exceeding learned-capacity budget %d", copiedBytes, capacityShareWithBurst)
	}
}

func TestWindowDeliveryDrivesPacingWithoutQueuedData(t *testing.T) {
	start := time.Unix(100, 0)
	a, b := bond.New(bond.Epoch{Boot: 1, Generation: 1}), bond.New(bond.Epoch{Boot: 2, Generation: 1})
	a.SetRemote(b.Epoch(), true)
	b.SetRemote(a.Epoch(), true)
	a.Path(0, 0, 100*time.Millisecond, start)
	b.Path(0, 0, 100*time.Millisecond, start)
	initial := a.Snapshot(start).Paths[0].Rate
	for i := 0; i < 8; i++ {
		now := start.Add(time.Duration(i) * 11 * time.Millisecond)
		if err := a.Enqueue(make([]byte, 1200), bond.PacketMetadata{}, now); err != nil {
			t.Fatal(err)
		}
		for _, tx := range a.Poll(now) {
			if _, err := b.Receive(tx.Path, tx.Frame, now.Add(50*time.Millisecond)); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, tx := range b.Poll(start.Add(160 * time.Millisecond)) {
		if _, err := a.Receive(tx.Path, tx.Frame, start.Add(210*time.Millisecond)); err != nil {
			t.Fatal(err)
		}
	}
	if got := a.Snapshot(start).Paths[0].Rate; got <= initial {
		t.Fatalf("delivery of a busy window did not increase pacing: %.0f -> %.0f", initial, got)
	}
}

func TestJitteredPathRepairsBeforePacketExpires(t *testing.T) {
	start := time.Unix(100, 0)
	a, b := bond.New(bond.Epoch{Boot: 1, Generation: 1}), bond.New(bond.Epoch{Boot: 2, Generation: 1})
	a.SetRemote(b.Epoch(), true)
	b.SetRemote(a.Epoch(), true)
	for i := 0; i < 30; i++ {
		now := start.Add(time.Duration(i) * 250 * time.Millisecond)
		a.Path(0, 0, 80*time.Millisecond, now)
		b.Path(0, 0, 80*time.Millisecond, now)
		if err := a.Enqueue(make([]byte, 1200), bond.PacketMetadata{}, now); err != nil {
			t.Fatal(err)
		}
		rtt := time.Duration(60+40*(i%2)) * time.Millisecond
		for _, tx := range a.Poll(now) {
			if _, err := b.Receive(tx.Path, tx.Frame, now.Add(rtt/2)); err != nil {
				t.Fatal(err)
			}
		}
		for _, tx := range b.Poll(now.Add(rtt/2 + 30*time.Millisecond)) {
			if _, err := a.Receive(tx.Path, tx.Frame, now.Add(rtt+30*time.Millisecond)); err != nil {
				t.Fatal(err)
			}
		}
	}
	now := start.Add(30 * 250 * time.Millisecond)
	a.Path(0, 0, 80*time.Millisecond, now)
	a.Path(1, 1, 150*time.Millisecond, now)
	if err := a.Enqueue(make([]byte, 1200), bond.PacketMetadata{}, now); err != nil {
		t.Fatal(err)
	}
	a.Poll(now) // Drop the first attempt.
	for _, tx := range a.Poll(now.Add(225 * time.Millisecond)) {
		if tx.Path == 1 && tx.Frame.ControlType == bond.DataType && len(tx.Frame.Payload) > 1000 {
			return
		}
	}
	t.Fatalf("lost packet received no repair before its 250 ms lifetime: %+v", a.Snapshot(now).Paths)
}

func TestBusyJitterDoesNotCollapsePacingRate(t *testing.T) {
	runJitterCapacity(t, true, 15, 30, 1250000, 0)
}

func TestBusyRadioJitterDoesNotCollapsePacingRate(t *testing.T) {
	runJitterCapacity(t, true, 10, 60, 1250000, 0)
}

func TestSlowRadioJitterDoesNotCollapsePacingRate(t *testing.T) {
	runJitterCapacity(t, true, 10, 60, 156250, 5000)
}

func TestStandbySerializationIsNotMistakenForQueueDelay(t *testing.T) {
	runJitterCapacity(t, true, 20, 0, 50000, 5000)
}

func runJitterCapacity(t *testing.T, busy bool, minimumDelayMS, delaySpreadMS, capacityBytesPerSecond, idleMS int) {
	t.Helper()
	start := time.Unix(100, 0)
	peers := [2]*bond.Transport{
		bond.New(bond.Epoch{Boot: 1, Generation: 1}),
		bond.New(bond.Epoch{Boot: 2, Generation: 1}),
	}
	for side, peer := range peers {
		peer.SetRemote(peers[1-side].Epoch(), true)
		peer.Path(0, 0, 60*time.Millisecond, start)
	}
	initial := peers[0].Snapshot(start).Paths[0].Rate
	queue := &events{}
	heap.Init(queue)
	var transmissions [2]int
	var available [2]time.Time
	var delivered int
	for tick := 0; tick < 30000; tick++ {
		now := start.Add(time.Duration(tick) * time.Millisecond)
		if busy && tick >= idleMS {
			if err := peers[0].Enqueue(make([]byte, 1200), bond.PacketMetadata{}, now); err != nil {
				t.Fatal(err)
			}
		}
		for side, peer := range peers {
			peer.Path(0, 0, 60*time.Millisecond, now)
			for _, tx := range peer.Poll(now) {
				transmissions[side]++
				delay := time.Duration(minimumDelayMS+transmissions[side]*17%(delaySpreadMS+1)) * time.Millisecond
				begin := now
				if available[side].After(begin) {
					begin = available[side]
				}
				if begin.Sub(now) > 100*time.Millisecond {
					continue
				}
				available[side] = begin.Add(time.Duration(float64(len(tx.Frame.Payload)+78) / float64(capacityBytesPerSecond) * float64(time.Second)))
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
			if e.to == 1 && tick >= 25000 {
				for _, d := range ds {
					delivered += len(d.Payload)
				}
			}
		}
	}
	for side, peer := range peers {
		state := peer.Snapshot(start.Add(30 * time.Second))
		if rate := state.Paths[0].Rate; !busy && rate < initial {
			t.Errorf("idle peer %d reduced pacing from %.0f to %.0f bytes/s without offered data or congestion: %+v", side, initial, rate, state)
		}
	}
	const measuredSeconds = 5
	const minimumUtilizationPercent = 64
	if busy && delivered < measuredSeconds*capacityBytesPerSecond*minimumUtilizationPercent/100 {
		t.Errorf("%d bytes/s with propagation jitter delivered only %d bytes in final 5s: %+v", capacityBytesPerSecond, delivered, peers[0].Snapshot(start.Add(30*time.Second)))
	}
}
