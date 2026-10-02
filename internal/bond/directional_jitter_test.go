package bond

import (
	"testing"
	"time"
)

func TestReverseJitterDoesNotMaskForwardCongestion(t *testing.T) {
	start := time.Unix(100, 0)
	a, b := New(Epoch{Boot: 1, Generation: 1}), New(Epoch{Boot: 2, Generation: 1})
	a.SetRemote(b.Epoch(), true)
	b.SetRemote(a.Epoch(), true)
	for tick := 0; tick <= 12; tick++ {
		now := start.Add(time.Duration(tick+1) * 500 * time.Millisecond)
		a.Path(0, 0, 40*time.Millisecond, now)
		b.Path(0, 0, 40*time.Millisecond, now)
		forward, reverse := 20*time.Millisecond, time.Duration(20+80*(tick%2))*time.Millisecond
		packets := poll(a, now)
		before := a.Snapshot(now).Paths[0].Rate
		if tick == 12 {
			forward += 30 * time.Millisecond
			for range 20 {
				if err := a.Enqueue(make([]byte, 1200), PacketMetadata{}, now); err != nil {
					t.Fatal(err)
				}
			}
			poll(a, now.Add(time.Millisecond))
		}
		for _, tx := range packets {
			if _, err := b.Receive(0, tx.Frame, now.Add(forward)); err != nil {
				t.Fatal(err)
			}
		}
		for _, tx := range poll(b, now.Add(forward+30*time.Millisecond)) {
			if _, err := a.Receive(0, tx.Frame, now.Add(forward+reverse+30*time.Millisecond)); err != nil {
				t.Fatal(err)
			}
		}
		if tick == 12 {
			state := a.Snapshot(now).Paths[0]
			if state.QueueDelay != 30*time.Millisecond {
				t.Fatalf("forward queue reproduction measured %s, want 30ms", state.QueueDelay)
			}
			if state.Rate >= before {
				t.Fatalf("reverse-only jitter masked a real forward queue: rate %.0f -> %.0f, idle RTT variation=%s", before, state.Rate, state.IdleRTTVariation)
			}
		}
	}
}
