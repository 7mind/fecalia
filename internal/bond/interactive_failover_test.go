package bond

import (
	"bytes"
	"testing"
	"time"
)

func TestInteractiveRepairPrecedesFeedbackTimeout(t *testing.T) {
	for _, scenario := range []struct {
		name      string
		size      int
		rate      float64
		alternate bool
		repaired  bool
		feedback  bool
	}{
		{"small", 224, initialRate, true, true, false},
		{"bulk", 1200, initialRate, true, false, false},
		{"budget-exhausted", 224, minimumRate, true, false, false},
		{"no-alternate", 224, initialRate, false, false, false},
		{"fresh-feedback", 224, initialRate, true, false, true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			now := time.Unix(100, 0)
			a := New(Epoch{Boot: 1, Generation: 1})
			a.SetRemote(Epoch{Boot: 2, Generation: 1}, true)
			a.Path(0, 0, 40*time.Millisecond, now)
			a.Path(1, 1, 80*time.Millisecond, now)
			a.paths[0].rate, a.paths[1].rate = scenario.rate, scenario.rate
			if !scenario.alternate {
				a.Disable(1)
			}
			// A radio lane can have a long delivery-confirmation tail while its
			// propagation minimum is short. The other path still accepts traffic.
			a.paths[0].feedbackRTT = 150 * time.Millisecond
			a.paths[0].feedbackRTTVariation = 50 * time.Millisecond
			if err := a.Enqueue(make([]byte, scenario.size), PacketMetadata{Flow: FlowID{1}}, now); err != nil {
				t.Fatal(err)
			}
			first := a.Poll(now)
			if len(first) != 1 || first[0].Path != 0 {
				t.Fatal("expected an initially unreplicated datagram on the faster lane")
			}
			for tick := 1; tick <= 100; tick++ {
				if scenario.feedback {
					a.paths[0].lastACK = now.Add(time.Duration(tick) * time.Millisecond)
				}
				for _, tx := range a.Poll(now.Add(time.Duration(tick) * time.Millisecond)) {
					if tx.Frame.ControlType == DataType && tx.Path == 1 && len(tx.Frame.Payload) == len(first[0].Frame.Payload) {
						if !scenario.repaired {
							t.Fatal("early copy violated its size, budget or path precondition")
						}
						if !bytes.Equal(tx.Frame.Payload[headerBytes+16:], first[0].Frame.Payload[headerBytes+16:]) {
							t.Fatal("repair changed delivery identity or encrypted bytes")
						}
						return
					}
				}
			}
			if scenario.repaired {
				t.Fatal("small datagram waited past the interactive deadline for a feedback timeout longer than its repair lifetime")
			}
		})
	}
}
