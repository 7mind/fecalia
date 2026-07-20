package bind

import (
	"net/netip"
	"sync/atomic"
	"testing"

	"github.com/7mind/wanbond/internal/telemetry"
)

// TestPMTUProbeAccessor verifies device.Up's per-path PMTU handle (T227, defect D88): a
// probing bind builds one telemetry.EchoAwaitProbe per path, the PRIMARY-peer accessor
// returns exactly that path's probe, and an unknown path returns nil.
func TestPMTUProbeAccessor(t *testing.T) {
	psk := testKey(t, 0x24)
	clk := newFakeClock()
	m, _, _ := newProbingMultipath(t, loopbackPaths(2), psk, clk)
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	if got := m.PMTUProbe("nonexistent"); got != nil {
		t.Fatalf("PMTUProbe(unknown) = %v, want nil", got)
	}
	for i, name := range []string{"a", "b"} {
		got := m.PMTUProbe(name)
		if got == nil {
			t.Fatalf("PMTUProbe(%q) = nil, want the path's own echo-await probe", name)
		}
		if got != m.paths[i].pmtuProbe {
			t.Fatalf("PMTUProbe(%q) returned a different probe than path %d holds", name, i)
		}
		var _ telemetry.PMTUProbe = got // it satisfies the discovery seam
	}
}

// TestPMTURoamCallback verifies the concentrator roam re-probe trigger (R245): the
// per-path onRoam callback fires ONCE on an actual learned-remote CHANGE, and never on
// the first set nor a same-address re-learn (setRemote runs on every inbound probe).
func TestPMTURoamCallback(t *testing.T) {
	psk := testKey(t, 0x24)
	clk := newFakeClock()
	m, _, _ := newProbingMultipath(t, loopbackPaths(1), psk, clk)
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	ap1 := netip.MustParseAddrPort("192.0.2.1:51820")
	ap2 := netip.MustParseAddrPort("192.0.2.2:51820")

	// Establish a current remote BEFORE registering, so the count reflects only changes
	// observed after registration.
	m.paths[0].setRemote(ap1)

	var roams atomic.Int32
	m.OnPathRoam("a", func() { roams.Add(1) })

	m.paths[0].setRemote(ap1) // same address: no roam
	if got := roams.Load(); got != 0 {
		t.Fatalf("roam fired %d times on a same-address re-learn, want 0", got)
	}
	m.paths[0].setRemote(ap2) // actual change: exactly one roam
	if got := roams.Load(); got != 1 {
		t.Fatalf("roam fired %d times on an actual remote change, want 1", got)
	}

	// An unknown-path registration is a no-op (must not panic).
	m.OnPathRoam("nonexistent", func() {})
}
