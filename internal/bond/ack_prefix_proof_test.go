package bond

import (
	"strconv"
	"testing"
	"time"
)

// Correctness-Subcutaneous-Group: submit wire-valid frames directly to isolate
// receipt accounting from capacity discovery, then observe actual repair delivery.
func TestOldArrivalCannotAcknowledgeANewLostDatagram(t *testing.T) {
	for _, bytes := range []int{32, 160, 384, 1200} {
		t.Run(strconv.Itoa(bytes), func(t *testing.T) {
			oldArrivalCannotAcknowledgeNewLoss(t, bytes)
		})
	}
}

func oldArrivalCannotAcknowledgeNewLoss(t *testing.T, bytes int) {
	t.Helper()
	start := time.Unix(100, 0)
	a, b := New(Epoch{Boot: 1, Generation: 1}), New(Epoch{Boot: 2, Generation: 1})
	a.SetRemote(b.Epoch(), true)
	b.SetRemote(a.Epoch(), true)
	if err := a.Path(0, 0, 20*time.Millisecond, start); err != nil {
		t.Fatal(err)
	}
	if err := b.Path(0, 0, 20*time.Millisecond, start); err != nil {
		t.Fatal(err)
	}
	path := a.paths[0]
	path.rate, path.confirmedWireBytes = 12500000, maxPackets*maxDatagram
	var received [86]int
	submit := func(value byte, now time.Time) Transmission {
		payload := make([]byte, bytes)
		payload[0] = value
		c := classify(bytes, PacketMetadata{})
		return a.transmit(&packet{payload: payload, created: now, class: c, interactive: c != classBulk}, path, now)
	}
	deliver := func(tx Transmission, now time.Time) []Delivery {
		got, err := b.Receive(tx.Path, tx.Frame, now)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range got {
			if len(d.Payload) != bytes || int(d.Payload[0]) >= len(received) {
				t.Fatalf("unexpected delivery: %v", d)
			}
			received[d.Payload[0]]++
		}
		return got
	}
	acknowledge := func(now time.Time) {
		for _, tx := range poll(b, now) {
			if _, err := a.Receive(tx.Path, tx.Frame, now); err != nil {
				t.Fatal(err)
			}
		}
	}
	old := submit(1, start)
	for value := byte(2); value <= 3; value++ {
		deliver(submit(value, start), start.Add(10*time.Millisecond))
	}
	acknowledge(start.Add(40 * time.Millisecond))
	initialDeficit := path.sent - path.ackedBytes
	path.rate = 12500000
	const missing = byte(4)
	for value := missing; value <= 85; value++ {
		tx := submit(value, start.Add(41*time.Millisecond))
		if value != missing {
			deliver(tx, start.Add(42*time.Millisecond))
		}
	}
	if got := deliver(old, start.Add(43*time.Millisecond)); len(got) != 1 || got[0].Payload[0] != 1 {
		t.Fatalf("delayed original was not delivered: %v", got)
	}
	acknowledge(start.Add(80 * time.Millisecond))
	if received[missing] != 0 {
		t.Fatal("fixture delivered the deliberately lost datagram")
	}
	t.Logf("byte deficit %d -> %d; new datagram pending %t, received %d", initialDeficit, path.sent-path.ackedBytes, a.pending[uint64(missing)] != nil, received[missing])
	for tick := 81; tick <= 250; tick++ {
		now := start.Add(time.Duration(tick) * time.Millisecond)
		for _, tx := range poll(a, now) {
			for _, got := range deliver(tx, now) {
				if len(got.Payload) == bytes && got.Payload[0] == missing {
					t.Logf("repaired after %s", now.Sub(start.Add(41*time.Millisecond)))
					return
				}
			}
		}
		acknowledge(now)
	}
	t.Fatal("new lost datagram was never repaired after an old arrival preserved the byte deficit")
}
