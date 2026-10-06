package bond

import (
	"strconv"
	"testing"
	"time"
)

// Correctness-Subcutaneous-Group: a full byte prefix must retain its receipt
// meaning after its highest physical attempt has left the sender ledger.
func TestLateCompletePrefixDoesNotRepeatReceivedDatagrams(t *testing.T) {
	for _, bytes := range []int{32, 160, 384, 1200} {
		t.Run(strconv.Itoa(bytes), func(t *testing.T) { lateCompletePrefixDoesNotRepeatReceivedDatagrams(t, bytes) })
	}
}

func lateCompletePrefixDoesNotRepeatReceivedDatagrams(t *testing.T, bytes int) {
	t.Helper()
	start := time.Unix(100, 0)
	a, b := New(Epoch{Boot: 1, Generation: 1}), New(Epoch{Boot: 2, Generation: 1})
	a.SetRemote(b.Epoch(), true)
	b.SetRemote(a.Epoch(), true)
	for _, peer := range []*Transport{a, b} {
		if err := peer.Path(0, 0, 20*time.Millisecond, start); err != nil {
			t.Fatal(err)
		}
	}
	path := a.paths[0]
	path.rate, path.confirmedWireBytes = 12500000, maxPackets*maxDatagram
	keepalives := poll(a, start.Add(200*time.Millisecond))
	if len(keepalives) != 1 {
		t.Fatalf("expected one delayed keepalive, got %d", len(keepalives))
	}
	received := make(map[uint64]bool)
	deliver := func(tx Transmission, now time.Time) {
		got, err := b.Receive(tx.Path, tx.Frame, now)
		if err != nil {
			t.Fatal(err)
		}
		for _, delivery := range got {
			received[delivery.Sequence] = true
		}
	}
	const count = 300
	for number := 0; number < count; number++ {
		payload := make([]byte, bytes)
		deliver(a.transmit(&packet{payload: payload, created: start.Add(201 * time.Millisecond), class: classify(bytes, PacketMetadata{}), interactive: classify(bytes, PacketMetadata{}) != classBulk}, path, start.Add(201*time.Millisecond)), start.Add(211*time.Millisecond))
	}
	// Lose the ACK reporting the older receipt range.
	acks := func(now time.Time) []Transmission {
		var frames []Transmission
		for _, tx := range poll(b, now) {
			if tx.Frame.ControlType == ACKType {
				frames = append(frames, tx)
			}
		}
		return frames
	}
	lost := acks(start.Add(212 * time.Millisecond))
	if len(lost) != 1 {
		t.Fatalf("expected one lost ACK, got %d", len(lost))
	}
	acknowledge := func(now time.Time) {
		frames := acks(now)
		if len(frames) != 1 {
			t.Fatalf("expected one ACK, got %d", len(frames))
		}
		for _, tx := range frames {
			if _, err := a.Receive(tx.Path, tx.Frame, now); err != nil {
				t.Fatal(err)
			}
		}
	}
	acknowledge(start.Add(240 * time.Millisecond))
	if len(a.pending) == 0 {
		t.Fatal("fixture did not retain receipts outside the latest bitmaps")
	}
	if len(received) != count {
		t.Fatalf("received %d originals, want %d", len(received), count)
	}
	deliver(keepalives[0], start.Add(241*time.Millisecond))
	acknowledge(start.Add(280 * time.Millisecond))
	t.Logf("complete byte prefix, %d already received datagrams remain pending", len(a.pending))
	path.rate = 12500000
	for tick := 281; tick <= 450; tick++ {
		for _, tx := range poll(a, start.Add(time.Duration(tick)*time.Millisecond)) {
			if len(tx.Frame.Payload) > headerBytes+dataFieldBytes {
				t.Fatal("repaired a received datagram after a full prefix was reported")
			}
		}
	}
}
