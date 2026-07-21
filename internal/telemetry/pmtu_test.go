package telemetry

import (
	"errors"
	"math"
	"testing"
	"time"
)

// intermittentPMTUProbe models a partially-lossy carrier (defect D91): a candidate at or
// below `reliable` ALWAYS echoes, while a candidate ABOVE `reliable` echoes only on a
// period-2 pattern per size — the first probe of the size echoes, the next drops, and so
// on. That is exactly the pathology single-echo acceptance mishandles: an oversize size
// echoes on its FIRST probe (so pre-fix it is accepted and the search converges ABOVE
// reliable), yet it can never accumulate two consecutive echoes (so N>=2 consecutive
// rejects it). It needs no clock and no real network.
type intermittentPMTUProbe struct {
	reliable int
	calls    int
	sizes    []int
	perSize  map[int]int
}

func (f *intermittentPMTUProbe) ProbePMTU(onWire int) (bool, error) {
	f.calls++
	f.sizes = append(f.sizes, onWire)
	if onWire <= f.reliable {
		return true, nil
	}
	if f.perSize == nil {
		f.perSize = make(map[int]int)
	}
	n := f.perSize[onWire]
	f.perSize[onWire]++
	// Echo on even attempts (0,2,4…), drop on odd (1,3…): the first probe of a fresh
	// oversize candidate echoes, but two consecutive echoes never occur.
	return n%2 == 0, nil
}

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

// TestPMTUReliabilityRejectsIntermittentOversize is the defect-D91 reproduce-first and
// its fix (acceptance a): against a partially-lossy carrier where sizes above a reliable
// threshold echo only intermittently, the PRE-FIX single-echo search (Confirmations:1)
// converges ABOVE the reliable threshold (the black-holing defect), while the
// N-consecutive default converges AT/BELOW it. Both cases run on the SAME deterministic
// fake with no real network or sleep.
func TestPMTUReliabilityRejectsIntermittentOversize(t *testing.T) {
	const reliable = 1300

	// PRE-FIX (Confirmations:1 == single-echo): an oversize size echoes on its first probe
	// and is accepted, so the search climbs ABOVE the reliably-carried threshold — the
	// D91 field symptom (converged too high, full-MTU DATA black-holes).
	old := &intermittentPMTUProbe{reliable: reliable}
	dold := NewPMTUDiscovery("cellular", PMTUConfig{DefaultMTU: 1500, Confirmations: 1}, old, newFakeClock(), discardLogger(t))
	if err := dold.Tick(StateUp); err != nil {
		t.Fatalf("single-echo Tick: %v", err)
	}
	if got := dold.PathMTU(); got <= reliable {
		t.Fatalf("single-echo converged to %d, expected ABOVE reliable %d (the D91 defect being reproduced)", got, reliable)
	}

	// POST-FIX (default N-consecutive): the intermittently-echoing oversize sizes never
	// accumulate N consecutive echoes, so the search settles at/below the reliable size.
	f := &intermittentPMTUProbe{reliable: reliable}
	d := NewPMTUDiscovery("cellular", PMTUConfig{DefaultMTU: 1500}, f, newFakeClock(), discardLogger(t))
	if err := d.Tick(StateUp); err != nil {
		t.Fatalf("N-consecutive Tick: %v", err)
	}
	if got := d.PathMTU(); got > reliable {
		t.Fatalf("N-consecutive converged to %d, want <= reliable %d (marginal oversize rejected)", got, reliable)
	}
}

// TestPMTUSafetyMargin covers acceptance (b) and (d): with the default margin 0 the
// reported PathMTU is byte-identical to the raw discovered value; with a positive
// SafetyMargin the RAW discovered value is unchanged while the REPORTED value is
// discovered-margin (clamped to PMTUFloor). The raw and reported values are asserted
// SEPARATELY.
func TestPMTUSafetyMargin(t *testing.T) {
	const threshold = 1400

	// (b) Margin 0 (default): reported == raw discovered, byte-identical to today.
	base := &fakePMTUProbe{threshold: threshold}
	d0 := NewPMTUDiscovery("cellular", PMTUConfig{DefaultMTU: 1500}, base, newFakeClock(), discardLogger(t))
	if err := d0.Tick(StateUp); err != nil {
		t.Fatalf("margin-0 Tick: %v", err)
	}
	if d0.discovered != threshold {
		t.Fatalf("raw discovered = %d, want %d", d0.discovered, threshold)
	}
	if got := d0.PathMTU(); got != d0.discovered {
		t.Fatalf("margin 0: reported PathMTU %d != raw discovered %d (must be byte-identical)", got, d0.discovered)
	}
	if got := d0.PathMTUOrZero(); got != threshold {
		t.Fatalf("margin 0: reported PathMTUOrZero %d, want %d", got, threshold)
	}

	// (d) Margin M>0: raw discovered UNCHANGED; reported == discovered - M.
	const margin = 40
	dm := NewPMTUDiscovery("cellular", PMTUConfig{DefaultMTU: 1500, SafetyMargin: margin}, &fakePMTUProbe{threshold: threshold}, newFakeClock(), discardLogger(t))
	if err := dm.Tick(StateUp); err != nil {
		t.Fatalf("margin Tick: %v", err)
	}
	if dm.discovered != threshold {
		t.Fatalf("SafetyMargin must NOT change the raw discovered value: got %d, want %d", dm.discovered, threshold)
	}
	if got := dm.PathMTU(); got != threshold-margin {
		t.Fatalf("reported PathMTU = %d, want %d (discovered - margin)", got, threshold-margin)
	}
	if got := dm.PathMTUOrZero(); got != threshold-margin {
		t.Fatalf("reported PathMTUOrZero = %d, want %d (discovered - margin)", got, threshold-margin)
	}

	// A margin larger than (discovered - floor) clamps the reported value to PMTUFloor,
	// never below; the raw value still stands.
	dbig := NewPMTUDiscovery("cellular", PMTUConfig{DefaultMTU: 1500, SafetyMargin: 10000}, &fakePMTUProbe{threshold: threshold}, newFakeClock(), discardLogger(t))
	if err := dbig.Tick(StateUp); err != nil {
		t.Fatalf("over-large-margin Tick: %v", err)
	}
	if dbig.discovered != threshold {
		t.Fatalf("over-large margin changed raw discovered: got %d, want %d", dbig.discovered, threshold)
	}
	if got := dbig.PathMTU(); got != PMTUFloor {
		t.Fatalf("over-large margin: reported PathMTU %d, want floor %d (clamped)", got, PMTUFloor)
	}
}

// TestPMTUProbeCountBounded covers acceptance (c): the worst-case probe count stays within
// N x ceil(log2(window)). A lossless path (every candidate echoes) is the worst case — each
// binary-search step spends the full N consecutive probes. Because only the failing
// candidates (at most ceil(log2(window)) of them) ever wait a probe deadline and each does
// so at most once (short-circuit), the wall-clock cost fits comfortably inside the e2e 20s
// window (DefaultPMTUProbeDeadline=1s).
func TestPMTUProbeCountBounded(t *testing.T) {
	probe := &fakePMTUProbe{threshold: 1500} // lossless up to the ceiling
	d := NewPMTUDiscovery("cellular", PMTUConfig{DefaultMTU: 1500}, probe, newFakeClock(), discardLogger(t))
	if err := d.Tick(StateUp); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	iters := int(math.Ceil(math.Log2(float64(d.ceiling - d.floor + 1))))
	bound := d.confirmations * iters
	if probe.calls > bound {
		t.Fatalf("probe count %d exceeds worst-case bound %d (= N=%d x ceil(log2(window=%d))=%d)",
			probe.calls, bound, d.confirmations, d.ceiling-d.floor, iters)
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

// TestPMTUDiscoveryReservesJunkPrefix pins D85 fix-direction 4 (T225): the padded MTU
// probes measure PROBE-plane datagrams that do NOT carry the AmneziaWG junk prefix real WG
// DATA carries, so the raw echoing size over-estimates the usable outer envelope on an
// obfuscated path. With JunkHeadroom=L the converged USABLE PMTU is L bytes smaller than
// the raw discovered PMTU, so downstream inner-MTU sizing cannot settle on a size that
// still EMSGSIZE/black-holes a full-size obfuscated DATA datagram. With JunkHeadroom=0
// (plain WireGuard) usable == raw, byte-identical. Covers the searched, pinned, and
// plain-WG cases.
func TestPMTUDiscoveryReservesJunkPrefix(t *testing.T) {
	const junk = 92

	// Searched path: the raw gauge stays honest; the usable envelope reserves the junk.
	d := NewPMTUDiscovery("starlink", PMTUConfig{DefaultMTU: 1500, JunkHeadroom: junk}, &fakePMTUProbe{threshold: 1400}, newFakeClock(), discardLogger(t))
	if err := d.Tick(StateUp); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if got := d.PathMTU(); got != 1400 {
		t.Fatalf("PathMTU (raw gauge) = %d, want 1400", got)
	}
	if got := d.UsablePathMTU(); got != 1400-junk {
		t.Fatalf("UsablePathMTU = %d, want %d (raw 1400 minus junk %d)", got, 1400-junk, junk)
	}

	// Plain WireGuard (JunkHeadroom=0): usable and raw are byte-identical.
	plain := NewPMTUDiscovery("starlink", PMTUConfig{DefaultMTU: 1500}, &fakePMTUProbe{threshold: 1400}, newFakeClock(), discardLogger(t))
	if err := plain.Tick(StateUp); err != nil {
		t.Fatalf("plain Tick: %v", err)
	}
	if plain.UsablePathMTU() != plain.PathMTU() {
		t.Fatalf("plain: UsablePathMTU %d != PathMTU %d (junk=0 must be byte-identical)", plain.UsablePathMTU(), plain.PathMTU())
	}

	// Pinned obfuscated path: junk is reserved against the operator-declared MTU too.
	pinned := NewPMTUDiscovery("starlink", PMTUConfig{ConfiguredMTU: 1400, DefaultMTU: 1500, JunkHeadroom: junk}, &fakePMTUProbe{}, newFakeClock(), discardLogger(t))
	if got := pinned.PathMTU(); got != 1400 {
		t.Fatalf("pinned PathMTU (raw) = %d, want 1400", got)
	}
	if got := pinned.UsablePathMTU(); got != 1400-junk {
		t.Fatalf("pinned UsablePathMTU = %d, want %d", got, 1400-junk)
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
