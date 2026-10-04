//go:build adaptivepolicy

package bond_test

import (
	"testing"
	"time"
)

// Progression-Blackbox-Group: the outcome restatement exposes collapsed bulk
// under simultaneous delay variation despite healthy one-direction cases.
func TestAdaptiveBidirectionalDelayNoiseService(t *testing.T) {
	checkDelayNoiseService(t, "both")
}

// Progression-Blackbox-Group: fresh ACK traffic must replace aged floor evidence.
func TestAdaptiveFreshTransitFloorEvidence(t *testing.T) {
	o := (policyRun{lanes: []modelLane{{rate: 1e6, delay: 20 * time.Millisecond}}, seconds: 22, trafficAt: 2, voice: true}).run(t)
	for side, states := range o.states {
		state := states[len(states)-1].Paths[0]
		if !state.TransitFloorKnown || state.TransitFloorAge > 10*time.Second {
			t.Errorf("direction %d fresh ACK stream has floor known %v age %s, want evidence within 10s", side, state.TransitFloorKnown, state.TransitFloorAge)
		}
	}
}
