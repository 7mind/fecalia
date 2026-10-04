package bond_test

import (
	"testing"

	"github.com/7mind/wanbond/internal/bond"
)

// Performance-Blackbox-Group: useful service must have corresponding physical
// sent and acknowledged counters, independent of controller phases.
func TestControlTelemetryAccountsForDeliveredTraffic(t *testing.T) {
	m := mixedLoad{lanes: []varyingLane{steadyLane}, offered: 8e6, seconds: 20, failed: -1}
	var last bond.Snapshot
	m.observe = func(second int, s bond.Snapshot) { last = s }
	o := m.run()
	lane := last.Paths[0]
	d := lane.Decisions
	t.Logf("capacity %.0f, threshold %s, decisions %+v", lane.Capacity, lane.Threshold, d)
	if o.bulk < .8*steadyLane.rate || lane.ACKed == 0 || lane.Sent < lane.ACKed || lane.BulkOriginals == 0 || lane.RealtimeOriginals == 0 {
		t.Errorf("bulk %.0f B/s lacks productive service or matching transport counters: %+v", o.bulk, lane)
	}
}
