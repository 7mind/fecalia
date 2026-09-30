package bond

import (
	"fmt"
	"testing"
	"time"
)

func TestLateReceiptsAreAcknowledgedBeyondLatestBitmap(t *testing.T) {
	for _, missing := range []int{0, 1, 64, 127, 256, 399} {
		t.Run(fmt.Sprintf("missing_%d", missing), func(t *testing.T) {
			testLateReceipts(t, missing)
		})
	}
}

func testLateReceipts(t *testing.T, missing int) {
	t.Helper()
	start := time.Unix(100, 0)
	a, b := New(Epoch{Boot: 1, Generation: 1}), New(Epoch{Boot: 2, Generation: 1})
	a.SetRemote(b.Epoch(), true)
	b.SetRemote(a.Epoch(), true)
	a.Path(0, 0, 80*time.Millisecond, start)
	b.Path(0, 0, 80*time.Millisecond, start)
	// Start at a converged 100 Mbit/s rate to isolate ACK coverage from discovery.
	a.paths[0].rate = 12500000
	a.paths[0].confirmedWireBytes = maxPackets * maxDatagram
	var delayed []Transmission
	delivered := 0
	for i := 0; i < 400; i++ {
		now := start.Add(time.Duration(i) * 120 * time.Microsecond)
		if err := a.Enqueue(make([]byte, 1200), PacketMetadata{}, now); err != nil {
			t.Fatal(err)
		}
		for _, tx := range a.Poll(now) {
			if i+1 == missing {
				continue
			}
			if i < 100 {
				delayed = append(delayed, tx)
				continue
			}
			ds, err := b.Receive(tx.Path, tx.Frame, now)
			if err != nil {
				t.Fatal(err)
			}
			delivered += len(ds)
		}
		for _, tx := range b.Poll(now) {
			if _, err := a.Receive(tx.Path, tx.Frame, now); err != nil {
				t.Fatal(err)
			}
		}
	}
	now := start.Add(50 * time.Millisecond)
	for _, tx := range delayed {
		ds, err := b.Receive(tx.Path, tx.Frame, now)
		if err != nil {
			t.Fatal(err)
		}
		delivered += len(ds)
	}
	wantPending := 0
	if missing != 0 {
		wantPending = 1
	}
	if delivered != 400-wantPending {
		t.Fatalf("reproduction requires %d packets delivered, got %d", 400-wantPending, delivered)
	}
	for tick := 50; tick <= 150; tick++ {
		now = start.Add(time.Duration(tick) * time.Millisecond)
		for _, tx := range b.Poll(now) {
			if _, err := a.Receive(tx.Path, tx.Frame, now); err != nil {
				t.Fatal(err)
			}
		}
	}
	if len(a.pending) != wantPending {
		t.Fatalf("delivered %d packets, but %d receipts remain unacknowledged; want %d", delivered, len(a.pending), wantPending)
	}
	if missing != 0 && a.pending[uint64(missing)] == nil {
		t.Fatalf("packet %d was acknowledged without being received", missing)
	}
}

func TestCrossLaneReceiptReleasesOriginalCongestionWindow(t *testing.T) {
	now := time.Unix(100, 0)
	a, b := New(Epoch{Boot: 1, Generation: 1}), New(Epoch{Boot: 2, Generation: 1})
	a.SetRemote(b.Epoch(), true)
	b.SetRemote(a.Epoch(), true)
	for path := PathID(0); path < 2; path++ {
		a.Path(path, path, 60*time.Millisecond, now)
		b.Path(path, path, 60*time.Millisecond, now)
	}
	if err := a.Enqueue(make([]byte, 1200), PacketMetadata{}, now); err != nil {
		t.Fatal(err)
	}
	for _, tx := range a.Poll(now) {
		if tx.Path != 0 {
			t.Fatalf("expected original data on lane 0, got %d", tx.Path)
		}
		if _, err := b.Receive(tx.Path, tx.Frame, now); err != nil {
			t.Fatal(err)
		}
	}
	// Report the authenticated global receipt through lane 1. Its physical ACK
	// bitmap says nothing about lane 0, so that lane's byte counter stays zero.
	ack := acknowledgement{observed: a.Epoch(), receivedHigh: b.received.high, receivedMask: b.received.bitmap(b.received.high)}
	if _, err := a.Receive(1, ackFrame(b.Epoch(), 1, 1, ack), now.Add(30*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if got := a.Snapshot(now).Paths[0].InFlight; got != 0 {
		t.Fatalf("delivery is confirmed through the other lane, but %d bytes still occupy the original window", got)
	}
	if got := a.Snapshot(now).Paths[0].ACKed; got != 0 {
		t.Fatalf("global delivery does not prove physical delivery on lane 0: %d bytes", got)
	}
	for _, tx := range b.Poll(now.Add(40 * time.Millisecond)) {
		if _, err := a.Receive(tx.Path, tx.Frame, now.Add(70*time.Millisecond)); err != nil {
			t.Fatal(err)
		}
	}
	if got := a.Snapshot(now).Paths[0].InFlight; got != 0 {
		t.Fatalf("subsequent physical acknowledgement released bytes twice: %d", got)
	}
	if got := a.Snapshot(now).Paths[0].ACKed; got != 1200+wireOverhead {
		t.Fatalf("later physical receipt was not accounted: %d bytes", got)
	}
	if got := a.paths[0].confirmedWireBytes; got != 1200+wireOverhead {
		t.Fatalf("global confirmation hid later physical delivery from startup discovery: %d bytes", got)
	}
}

func TestReorderedACKMergesReceiptsWithoutRegressingFeedback(t *testing.T) {
	now := time.Unix(100, 0)
	a := New(Epoch{Boot: 1, Generation: 1})
	remote := Epoch{Boot: 2, Generation: 1}
	a.SetRemote(remote, true)
	a.Path(0, 0, 80*time.Millisecond, now)
	a.paths[0].rate = 12500000
	a.paths[0].confirmedWireBytes = maxPackets * maxDatagram
	for i := 0; i < 400; i++ {
		at := now.Add(time.Duration(i) * 120 * time.Microsecond)
		if err := a.Enqueue(make([]byte, 1200), PacketMetadata{}, at); err != nil {
			t.Fatal(err)
		}
		a.Poll(at)
	}
	full := [ackReceiptWords]uint64{^uint64(0), ^uint64(0), ^uint64(0), ^uint64(0)}
	old := acknowledgement{observed: a.Epoch(), high: 256, mask: ^uint64(0), bytes: 256 * (1200 + wireOverhead), elapsed: uint64(30 * time.Millisecond), receivedHigh: 256, receivedMask: full}
	newer := acknowledgement{observed: a.Epoch(), high: 400, mask: ^uint64(0), bytes: 400 * (1200 + wireOverhead), elapsed: uint64(48 * time.Millisecond), receivedHigh: 400, receivedMask: full}
	if _, err := a.Receive(0, ackFrame(remote, 0, 2, newer), now.Add(60*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	path := a.paths[0]
	previousRate, previousRTT, previousACK := path.rate, path.rtt, path.lastACK
	if len(a.pending) != 144 {
		t.Fatalf("reproduction requires the older receipt window to be missing, pending=%d", len(a.pending))
	}
	for _, invalid := range []acknowledgement{
		{observed: a.Epoch(), bytes: newer.bytes + 1, elapsed: old.elapsed},
		{observed: a.Epoch(), bytes: old.bytes, elapsed: newer.elapsed + 1},
	} {
		if _, err := a.Receive(0, ackFrame(remote, 0, 1, invalid), now.Add(61*time.Millisecond)); err == nil {
			t.Fatal("older ACK advanced cumulative feedback")
		}
	}
	frame := ackFrame(remote, 0, 1, old)
	if _, err := a.Receive(0, frame, now.Add(61*time.Millisecond)); err != nil {
		t.Fatalf("first arrival of a reordered receipt must be useful: %v", err)
	}
	if len(a.pending) != 0 || path.inflight != 0 {
		t.Fatalf("reordered receipt left pending=%d in_flight=%d", len(a.pending), path.inflight)
	}
	if path.ackRevision != 2 || path.ackedBytes != newer.bytes || path.ackedElapsed != newer.elapsed || path.rate != previousRate || path.rtt != previousRTT || path.lastACK != previousACK {
		t.Fatal("old receipt regressed current feedback or freshness")
	}
	if _, err := a.Receive(0, frame, now.Add(62*time.Millisecond)); err == nil {
		t.Fatal("replayed ACK accepted twice")
	}
	if _, err := a.Receive(0, ackFrame(remote, 0, maxPackets+3, newer), now.Add(63*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Receive(0, ackFrame(remote, 0, 3, old), now.Add(64*time.Millisecond)); err == nil {
		t.Fatal("unseen ACK outside the replay window accepted")
	}
}
