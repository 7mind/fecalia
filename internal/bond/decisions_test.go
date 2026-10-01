package bond_test

import (
	"testing"

	"github.com/7mind/wanbond/internal/bond"
)

// A saturating transfer on a steady lane ends discovery by a congestion
// signal and then probes its estimate; the counts say so.
func TestControlDecisionsAreCounted(t *testing.T) {
	m := mixedLoad{lanes: []varyingLane{steadyLane}, offered: 8e6, seconds: 20, failed: -1}
	var last bond.Snapshot
	m.observe = func(second int, s bond.Snapshot) { last = s }
	m.run()
	lane := last.Paths[0]
	d := lane.Decisions
	t.Logf("capacity %.0f, threshold %s, decisions %+v", lane.Capacity, lane.Threshold, d)
	if d.DiscoveryCongested+d.DiscoveryPlateau == 0 {
		t.Error("discovery ended without being counted")
	}
	if d.Pulses == 0 || d.PulseWins+d.PulseLosses == 0 {
		t.Errorf("a held lane under load counted %d pulses, %d won and %d lost", d.Pulses, d.PulseWins, d.PulseLosses)
	}
	if d.DelaySignals+d.LossSignals < d.DiscoveryCongested+d.PulseLosses+d.CapacityRemeasured+d.CapacityDecays {
		t.Errorf("more consequences than congestion signals: %+v", d)
	}
	if lane.Capacity == 0 || lane.Threshold <= 0 {
		t.Errorf("capacity %.0f and threshold %s after discovery", lane.Capacity, lane.Threshold)
	}
}
