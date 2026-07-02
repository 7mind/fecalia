//go:build e2e

package e2e

import (
	"strings"
	"testing"
)

// TestFixture verifies the netns/netem two-path fixture: it brings the topology
// up, checks each path's emulated RTT and qdisc, injects and clears loss on the
// Starlink-like path while keeping it reachable, and confirms teardown leaves no
// residual edge veth.
func TestFixture(t *testing.T) {
	top := Setup(t)

	// Starlink-like path: ~45ms with jitter. RTT ~= one-way delay (netem is on
	// the edge egress only).
	if rtt := top.RTT("starlink", 5); rtt < 30 || rtt > 95 {
		t.Errorf("starlink RTT = %.1fms, want ~45ms (30-95 tolerance)", rtt)
	}
	// 5G-like path: ~64ms, stable.
	if rtt := top.RTT("cellular", 5); rtt < 48 || rtt > 110 {
		t.Errorf("cellular RTT = %.1fms, want ~64ms (48-110 tolerance)", rtt)
	}

	if q := top.QdiscShow("starlink"); !strings.Contains(q, "netem") || !strings.Contains(q, "45ms") {
		t.Errorf("starlink qdisc unexpected: %q", strings.TrimSpace(q))
	}

	top.InjectLoss("starlink", 5)
	if q := top.QdiscShow("starlink"); !strings.Contains(q, "loss") {
		t.Errorf("5%% loss not reflected in qdisc: %q", strings.TrimSpace(q))
	}
	if !top.Reachable("starlink", 12) {
		t.Error("starlink unreachable after injecting 5% loss (should still pass most pings)")
	}
	top.ClearLoss("starlink")
	if q := top.QdiscShow("starlink"); strings.Contains(q, "loss") {
		t.Errorf("loss not cleared: %q", strings.TrimSpace(q))
	}

	// Blackhole/restore knob (used by the P1 failover test).
	top.Blackhole("cellular")
	if top.Reachable("cellular", 3) {
		t.Error("cellular still reachable after blackhole")
	}
	top.Restore("cellular")
	if !top.Reachable("cellular", 5) {
		t.Error("cellular unreachable after restore")
	}

	top.Teardown()
	if top.LinkExists("starlink") || top.LinkExists("cellular") {
		t.Error("edge veth survived teardown")
	}
}
