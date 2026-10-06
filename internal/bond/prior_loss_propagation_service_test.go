package bond_test

import (
	"testing"
	"time"
)

// Performance-Blackbox-Group: prior physical loss cannot freeze later delay adaptation.
func TestSettledPropagationServiceAfterEarlierPhysicalLoss(t *testing.T) {
	for _, loss := range []float64{0, .004} {
		name := "loss-free"
		if loss > 0 {
			name = "prior-loss"
		}
		t.Run(name, func(t *testing.T) {
			lane := modelLane{rate: 1.25e6, delay: 20 * time.Millisecond}
			lane.condition = func(side int, at time.Duration) modelCondition {
				delay := 20 * time.Millisecond
				if at >= 20*time.Second {
					delay = 35 * time.Millisecond
				}
				earlierLoss := 0.0
				if side == 0 && at >= 5*time.Second && at < 6*time.Second {
					earlierLoss = loss
				}
				return modelCondition{rate: 1.25e6, delay: delay, loss: earlierLoss, buffer: 200 * time.Millisecond}
			}
			m := policyRun{lanes: []modelLane{lane}, seconds: 32, trafficAt: 1, bulk: true, bulkDirection: policyBulkDownlink}
			o := m.run(t)
			var dropped, before float64
			for _, bytes := range o.laneDropped[0][0][5:6] {
				dropped += bytes
			}
			if loss > 0 && dropped <= 0 {
				t.Fatal("fixture did not establish earlier physical loss")
			}
			for _, bytes := range o.bulk[0][17:20] {
				before += bytes / 3
			}
			if ref := m.reference(19 * time.Second)[0]; before < .75*ref {
				t.Fatalf("fixture did not establish settled pre-change service: %.0f B/s, reference %.0f", before, ref)
			}
			t.Logf("prior drop %.0f bytes, pre-change %.0f B/s, post-change %v B/s", dropped, before, o.bulk[0][22:])
			for second := 22; second < len(o.bulk[0]); second++ {
				if o.bulk[0][second] < .75*before {
					t.Errorf("second %d delivered %.0f B/s, below 75%% of %.0f before the propagation change", second, o.bulk[0][second], before)
				}
			}
		})
	}
}
