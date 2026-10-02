package bond_test

import (
	"bytes"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/bond"
)

func TestACKCoalescingPreservesTCPInformation(t *testing.T) {
	for name, mutate := range map[string]func(*bond.PacketMetadata){
		"duplicate-ack":      func(m *bond.PacketMetadata) { m.ACK.Acknowledgement = 200 },
		"backwards-ack":      func(m *bond.PacketMetadata) { m.ACK.Acknowledgement = 100 },
		"window-only-update": func(m *bond.PacketMetadata) { m.ACK.Window++; m.ACK.Acknowledgement = 200 },
		"zero-window":        func(m *bond.PacketMetadata) { m.ACK.Window = 0 },
		"sequence-change":    func(m *bond.PacketMetadata) { m.ACK.Sequence++ },
		"traffic-class":      func(m *bond.PacketMetadata) { m.ACK.TrafficClass++ },
		"control-or-sack":    func(m *bond.PacketMetadata) { m.ACK.Eligible = false },
		"timestamp-value":    func(m *bond.PacketMetadata) { m.ACK.TSVal = 19 },
		"timestamp-echo":     func(m *bond.PacketMetadata) { m.ACK.TSEcr = 19 },
		"timestamp-option":   func(m *bond.PacketMetadata) { m.ACK.Timestamp = false },
		"different-flow":     func(m *bond.PacketMetadata) { m.Flow[0]++ },
	} {
		t.Run(name, func(t *testing.T) { checkACKDelivery(t, mutate, []byte{1, 2, 3}, 0, 0) })
	}
	t.Run("advancing", func(t *testing.T) { checkACKDelivery(t, func(*bond.PacketMetadata) {}, []byte{1, 3}, 1, 0) })
	t.Run("advancing-growing-window", func(t *testing.T) {
		checkACKDelivery(t, func(m *bond.PacketMetadata) { m.ACK.Window++ }, []byte{1, 3}, 1, 0)
	})
	t.Run("advancing-shrinking-window", func(t *testing.T) {
		checkACKDelivery(t, func(m *bond.PacketMetadata) { m.ACK.Window-- }, []byte{1, 3}, 1, 0)
	})
	t.Run("serial-wrap", func(t *testing.T) {
		checkACKDelivery(t, func(*bond.PacketMetadata) {}, []byte{1, 3}, 1, ^uint32(0)-150)
	})
	t.Run("window-closure-and-reopening", func(t *testing.T) {
		checkACKSequence(t, func(i byte, m *bond.PacketMetadata) {
			if i == 2 {
				m.ACK.Window = 0
			}
		}, []byte{1, 2, 3}, 0, 0)
	})
}

func checkACKDelivery(t *testing.T, mutate func(*bond.PacketMetadata), want []byte, coalesced uint64, baseACK uint32) {
	t.Helper()
	checkACKSequence(t, func(i byte, m *bond.PacketMetadata) {
		if i == 3 {
			mutate(m)
		}
	}, want, coalesced, baseACK)
}

func checkACKSequence(t *testing.T, mutate func(byte, *bond.PacketMetadata), want []byte, coalesced uint64, baseACK uint32) {
	t.Helper()
	now := time.Unix(100, 0)
	a, b := bond.New(bond.Epoch{Boot: 1, Generation: 1}), bond.New(bond.Epoch{Boot: 2, Generation: 1})
	a.SetRemote(b.Epoch(), true)
	b.SetRemote(a.Epoch(), true)
	a.Path(0, 0, time.Millisecond, now)
	b.Path(0, 0, time.Millisecond, now)
	for i := byte(1); i <= 3; i++ {
		meta := bond.PacketMetadata{Flow: bond.FlowID{4}, ACK: bond.TCPACK{Eligible: true, Sequence: 7, Acknowledgement: baseACK + uint32(i)*100, Window: 4096, Timestamp: true, TSVal: 20, TSEcr: 20}}
		mutate(i, &meta)
		if err := a.Enqueue(bytes.Repeat([]byte{i}, 80), meta, now); err != nil {
			t.Fatal(err)
		}
	}
	seen := make(map[byte]bool)
	for range 40 {
		now = now.Add(time.Millisecond)
		for _, tx := range poll(a, now) {
			got, err := b.Receive(tx.Path, tx.Frame, now)
			if err != nil {
				t.Fatal(err)
			}
			for _, delivery := range got {
				if !bytes.Equal(delivery.Payload, bytes.Repeat(delivery.Payload[:1], 80)) || seen[delivery.Payload[0]] {
					t.Fatal("payload corrupted or delivered twice")
				}
				seen[delivery.Payload[0]] = true
			}
		}
	}
	if len(seen) != len(want) {
		t.Fatalf("delivered %v, want %v", seen, want)
	}
	for _, value := range want {
		if !seen[value] {
			t.Fatalf("required packet %d was coalesced", value)
		}
	}
	if a.Snapshot(now).CoalescedACKs != coalesced {
		t.Fatal("ACK coalescing counter disagrees with delivery")
	}
}
