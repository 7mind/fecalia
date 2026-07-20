package telemetry

import (
	"errors"
	"testing"
	"time"
)

// fakePMTUProbe is a synchronous PMTUProbe standing in for the padded-probe +
// echo-await transport: it echoes exactly when the candidate on-wire size is within
// threshold (the path's true PMTU), records every probed size, and can inject a
// transport error. It needs no clock and no real network, so the discovery search is
// deterministic and instant.
type fakePMTUProbe struct {
	threshold int
	calls     int
	sizes     []int
	err       error
}

func (f *fakePMTUProbe) ProbePMTU(onWire int) (bool, error) {
	f.calls++
	f.sizes = append(f.sizes, onWire)
	if f.err != nil {
		return false, f.err
	}
	return onWire <= f.threshold, nil
}

// TestPMTUSearchConverges is the primary reproduce-first: with a fake transport that
// drops every probe larger than 1400 (echoes carry the same on-wire size, T202, and
// oversize datagrams fail under DF, T201), the binary search between the 1280 floor
// and the 1500 default ceiling must CONVERGE to exactly 1400 — the largest size that
// still echoes — driven only by a fake clock and the fake transport (no sleeps).
func TestPMTUSearchConverges(t *testing.T) {
	clk := newFakeClock()
	probe := &fakePMTUProbe{threshold: 1400}
	d := NewPMTUDiscovery("starlink", PMTUConfig{DefaultMTU: 1500}, probe, clk, discardLogger(t))

	if err := d.Tick(StateUp); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	if got := d.PathMTU(); got != 1400 {
		t.Fatalf("PathMTU after search = %d, want 1400 (largest echoing size)", got)
	}
	if probe.calls == 0 {
		t.Fatal("expected the search to probe the transport, got zero probes")
	}
	// Every probed size must have stayed inside the [floor, ceiling] search window.
	for _, s := range probe.sizes {
		if s < PMTUFloor || s > 1500 {
			t.Errorf("probed size %d outside [%d,1500]", s, PMTUFloor)
		}
	}
}

// TestPMTUReprobeOnUp asserts a path DOWN->UP transition RE-triggers discovery: after
// converging to 1400, the true PMTU widens to 1500; a bounce (Tick(Down) then
// Tick(Up)) must re-run the search and adopt the new 1400->1500 ceiling, proving the
// converged machine is not frozen and re-probes on recovery.
func TestPMTUReprobeOnUp(t *testing.T) {
	clk := newFakeClock()
	probe := &fakePMTUProbe{threshold: 1400}
	d := NewPMTUDiscovery("starlink", PMTUConfig{DefaultMTU: 1500}, probe, clk, discardLogger(t))

	if err := d.Tick(StateUp); err != nil {
		t.Fatalf("initial Tick: %v", err)
	}
	if got := d.PathMTU(); got != 1400 {
		t.Fatalf("PathMTU after first search = %d, want 1400", got)
	}
	afterFirst := probe.calls

	// The path bounces; while it recovers the underlay PMTU has widened to the full
	// 1500 ceiling.
	probe.threshold = 1500
	if err := d.Tick(StateDown); err != nil {
		t.Fatalf("Tick(Down): %v", err)
	}
	if probe.calls != afterFirst {
		t.Fatalf("a DOWN path must not probe: calls went %d -> %d", afterFirst, probe.calls)
	}
	if err := d.Tick(StateUp); err != nil {
		t.Fatalf("Tick(Up) after bounce: %v", err)
	}

	if probe.calls <= afterFirst {
		t.Fatalf("DOWN->UP did not re-probe: calls stayed at %d", probe.calls)
	}
	if got := d.PathMTU(); got != 1500 {
		t.Errorf("PathMTU after re-probe = %d, want 1500 (widened ceiling adopted)", got)
	}
}

// TestPMTUPinnedPathSkipsDiscovery asserts a path with an EXPLICIT configured MTU is
// PINNED: discovery NEVER probes (the operator override is authoritative — how the
// T200 knob and auto-discovery compose), and PathMTU reports the configured value
// verbatim regardless of how many liveness ticks (or roam notifications) elapse.
func TestPMTUPinnedPathSkipsDiscovery(t *testing.T) {
	clk := newFakeClock()
	probe := &fakePMTUProbe{threshold: 1400}
	d := NewPMTUDiscovery("starlink", PMTUConfig{ConfiguredMTU: 1400, DefaultMTU: 1500}, probe, clk, discardLogger(t))

	for i := 0; i < 5; i++ {
		if err := d.Tick(StateUp); err != nil {
			t.Fatalf("Tick %d: %v", i, err)
		}
	}
	d.NotifyRoam()
	if err := d.Tick(StateUp); err != nil {
		t.Fatalf("Tick after roam: %v", err)
	}

	if probe.calls != 0 {
		t.Errorf("pinned path probed %d times, want 0 (discovery must never run)", probe.calls)
	}
	if got := d.PathMTU(); got != 1400 {
		t.Errorf("pinned PathMTU = %d, want 1400 (configured value verbatim)", got)
	}
}

// TestPMTUPeriodicRefresh asserts the slow periodic refresh re-probes once the
// interval elapses on the injected clock (no real sleep), and not before.
func TestPMTUPeriodicRefresh(t *testing.T) {
	clk := newFakeClock()
	probe := &fakePMTUProbe{threshold: 1400}
	d := NewPMTUDiscovery("starlink", PMTUConfig{DefaultMTU: 1500, RefreshInterval: time.Minute}, probe, clk, discardLogger(t))

	if err := d.Tick(StateUp); err != nil {
		t.Fatalf("initial Tick: %v", err)
	}
	afterFirst := probe.calls

	// Before the interval elapses, a tick must not re-probe.
	clk.advance(30 * time.Second)
	if err := d.Tick(StateUp); err != nil {
		t.Fatalf("Tick before interval: %v", err)
	}
	if probe.calls != afterFirst {
		t.Fatalf("re-probed before refresh interval: calls %d -> %d", afterFirst, probe.calls)
	}

	// Past the interval, it re-probes.
	clk.advance(31 * time.Second)
	if err := d.Tick(StateUp); err != nil {
		t.Fatalf("Tick after interval: %v", err)
	}
	if probe.calls <= afterFirst {
		t.Errorf("periodic refresh did not re-probe after the interval elapsed")
	}
}

// TestPMTUProbeErrorRetries asserts a transient transport error leaves the machine
// unconverged so the next tick retries, rather than latching a bogus PMTU.
func TestPMTUProbeErrorRetries(t *testing.T) {
	clk := newFakeClock()
	probe := &fakePMTUProbe{threshold: 1400, err: errors.New("transport down")}
	d := NewPMTUDiscovery("starlink", PMTUConfig{DefaultMTU: 1500}, probe, clk, discardLogger(t))

	if err := d.Tick(StateUp); err == nil {
		t.Fatal("Tick with a failing transport returned nil, want the transport error surfaced")
	}
	if got := d.PathMTU(); got != PMTUFloor {
		t.Errorf("PathMTU after failed search = %d, want the conservative floor %d", got, PMTUFloor)
	}

	// Transport recovers; the next tick must retry and converge.
	probe.err = nil
	if err := d.Tick(StateUp); err != nil {
		t.Fatalf("Tick after recovery: %v", err)
	}
	if got := d.PathMTU(); got != 1400 {
		t.Errorf("PathMTU after retry = %d, want 1400", got)
	}
}
