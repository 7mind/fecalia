package bond

import (
	"bytes"
	"testing"
	"time"
)

func TestBulkRepairLifetimeStartsWithFirstTransmission(t *testing.T) {
	for _, scenario := range []struct {
		name string
		size int
	}{{"bulk", 1200}, {"interactive", 224}} {
		t.Run(scenario.name, func(t *testing.T) {
			size := scenario.size
			start := time.Unix(100, 0)
			a := New(Epoch{Boot: 1, Generation: 1})
			a.SetRemote(Epoch{Boot: 2, Generation: 1}, true)
			a.Path(0, 0, 40*time.Millisecond, start)
			a.Path(1, 1, 80*time.Millisecond, start)
			a.paths[0].feedbackRTT = 150 * time.Millisecond
			a.paths[0].feedbackRTTVariation = 10 * time.Millisecond
			payload := bytes.Repeat([]byte{7}, size)
			if err := a.Enqueue(payload, PacketMetadata{}, start); err != nil {
				t.Fatal(err)
			}
			first := poll(a, start.Add(90*time.Millisecond))
			if len(first) != 1 || first[0].Path != 0 {
				t.Fatal("expected one queued datagram to start on the first lane")
			}
			for _, tx := range poll(a, start.Add(285*time.Millisecond)) {
				if tx.Frame.ControlType == DataType && bytes.Equal(tx.Frame.Payload[headerBytes+32:], payload) {
					if size <= smallPacket {
						t.Fatal("interactive datagram survived its enqueue-relative deadline")
					}
					if tx.Path != 1 {
						t.Fatal("bulk repair did not use the healthy alternate lane")
					}
					for _, later := range poll(a, start.Add(400*time.Millisecond)) {
						if later.Frame.ControlType == DataType && bytes.Equal(later.Frame.Payload[headerBytes+32:], payload) {
							t.Fatal("retransmission extended the original repair deadline")
						}
					}
					if a.Snapshot(start.Add(400*time.Millisecond)).Expired != 1 {
						t.Fatal("bulk did not expire at its fixed deadline")
					}
					return
				}
			}
			if size > smallPacket {
				t.Fatal("bulk expired after 195ms in flight because queue residence consumed its 250ms repair lifetime")
			}
		})
	}
}
