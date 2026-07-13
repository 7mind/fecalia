package bind

import (
	"net/netip"
	"testing"

	"github.com/7mind/wanbond/internal/config"
)

// pathNamesSet reads PathNames() into a set for order-independent membership asserts.
func pathNamesSet(m *Multipath) map[string]bool {
	out := map[string]bool{}
	for _, n := range m.PathNames() {
		out[n] = true
	}
	return out
}

// deferredNames reads the deferred set's names (white-box) for assertions.
func deferredNames(m *Multipath) map[string]bool {
	out := map[string]bool{}
	m.mu.Lock()
	for _, dp := range m.deferred {
		out[dp.def.Name] = true
	}
	m.mu.Unlock()
	return out
}

// TestPathNamesIncludesDeferred is the T51/C1 regression guard: after a tolerant Open,
// a deferred (not-yet-assignable) path MUST still appear in PathNames(), which is the
// durable membership the device reload diffs against. If it is omitted, the reload
// driver classifies the still-configured deferred path as an ADD and AddPath rejects
// its EADDRNOTAVAIL bind — so even a no-op SIGHUP reload fails for the whole deferred
// window (the SIGHUP-no-op invariant regresses). FAILS on the pre-fix PathNames, which
// iterates only m.paths.
func TestPathNamesIncludesDeferred(t *testing.T) {
	psk := testKey(t, 0x54)
	clk := newFakeClock()
	paths := []config.Path{
		{Name: "bindable", SourceAddr: netip.MustParseAddr("127.0.0.1")},
		{Name: "deferred", SourceAddr: netip.MustParseAddr(unassignableSource)},
	}
	m, _, _ := newProbingMultipath(t, paths, psk, clk)
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	names := pathNamesSet(m)
	if !names["bindable"] || !names["deferred"] {
		t.Fatalf("PathNames() = %v, want both bindable and deferred (durable membership); a "+
			"missing deferred path makes a no-op reload classify it as an add-and-reject", m.PathNames())
	}
	// A no-op reload against the SAME config must produce no add/remove. diffPaths lives
	// in the device package (order-independent set difference); replicate its verdict.
	add, remove := diffNames(m.PathNames(), paths)
	if len(add) != 0 || len(remove) != 0 {
		t.Fatalf("no-op reload diff: add=%v remove=%v, want empty (reload of the same config is a no-op)", add, remove)
	}
}

// diffNames mirrors internal/device.diffPaths (kept package-local to avoid an import
// cycle): the set difference the reload driver runs over PathNames() vs the desired
// config, so a deferred path still present in both is neither added nor removed.
func diffNames(live []string, desired []config.Path) (add []string, remove []string) {
	liveSet := map[string]bool{}
	for _, n := range live {
		liveSet[n] = true
	}
	desiredSet := map[string]bool{}
	for _, p := range desired {
		desiredSet[p.Name] = true
		if !liveSet[p.Name] {
			add = append(add, p.Name)
		}
	}
	for _, n := range live {
		if !desiredSet[n] {
			remove = append(remove, n)
		}
	}
	return add, remove
}

// TestAddPathDefersUnassignable is the T51/C1 symmetry guard: AddPath (the runtime /
// reload add path) must tolerate a well-formed-but-not-yet-assignable source_addr the
// same way Open does — defer it (record it Down) and return success — rather than
// failing the whole reload. FAILS on the pre-fix AddPath, which returns EADDRNOTAVAIL.
func TestAddPathDefersUnassignable(t *testing.T) {
	psk := testKey(t, 0x55)
	clk := newFakeClock()
	m, _, _ := newProbingMultipath(t, loopbackPaths(1), psk, clk)
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	if err := m.AddPath(config.Path{Name: "deferred", SourceAddr: netip.MustParseAddr(unassignableSource)}); err != nil {
		t.Fatalf("AddPath of a not-yet-assignable path returned an error, want a deferred no-op success: %v", err)
	}
	// It did NOT join the live/scheduler set...
	if len(m.paths) != 1 {
		t.Fatalf("live paths = %d, want 1 (the deferred add must not become a live path)", len(m.paths))
	}
	// ...but it IS present in the durable membership + deferred set, marked Down.
	if !pathNamesSet(m)["deferred"] {
		t.Fatal("deferred add missing from PathNames() (durable membership)")
	}
	if !deferredNames(m)["deferred"] {
		t.Fatal("deferred add missing from the deferred set")
	}
}

// TestRemovePathAfterDeferredPreservesMembership is the T51/C2 regression guard: with a
// deferred path PRECEDING a bound one in config order, m.defs/m.probers (durable, full
// length) diverge from m.paths (compacted, bound only). RemovePath must address the
// durable slices by IDENTITY, not by the m.paths index, or it splices the wrong entry —
// dropping the deferred path and retaining the removed one, which then resurrects on the
// next Close→Open. FAILS on the pre-fix RemovePath (index-keyed durable splice).
func TestRemovePathAfterDeferredPreservesMembership(t *testing.T) {
	psk := testKey(t, 0x56)
	clk := newFakeClock()
	paths := []config.Path{
		{Name: "first", SourceAddr: netip.MustParseAddr("127.0.0.1")},
		{Name: "mid", SourceAddr: netip.MustParseAddr(unassignableSource)}, // deferred
		{Name: "third", SourceAddr: netip.MustParseAddr("127.0.0.1")},
	}
	m, _, _ := newProbingMultipath(t, paths, psk, clk)
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	// Sanity: mid deferred, first+third bound.
	if len(m.paths) != 2 {
		t.Fatalf("live paths = %d, want 2 (first, third)", len(m.paths))
	}
	if !deferredNames(m)["mid"] {
		t.Fatalf("mid not deferred: %v", deferredNames(m))
	}

	if err := m.RemovePath("third"); err != nil {
		t.Fatalf("RemovePath(third): %v", err)
	}

	// third is gone; mid (deferred) is preserved; first (bound) is preserved.
	names := pathNamesSet(m)
	if names["third"] {
		t.Fatalf("PathNames() still lists the removed path third: %v", m.PathNames())
	}
	if !names["mid"] {
		t.Fatalf("PathNames() dropped the deferred path mid after removing third: %v", m.PathNames())
	}
	if !names["first"] {
		t.Fatalf("PathNames() dropped the surviving bound path first: %v", m.PathNames())
	}
	if !deferredNames(m)["mid"] {
		t.Fatalf("deferred set lost mid after removing third: %v", deferredNames(m))
	}

	// The durable membership must survive a Close→Open: first rebinds, mid stays
	// deferred, third does NOT resurrect.
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if len(m.paths) != 1 || m.paths[0].name != "first" {
		var got []string
		for _, ps := range m.paths {
			got = append(got, ps.name)
		}
		t.Fatalf("after reopen live paths = %v, want [first] (third must not resurrect)", got)
	}
	if !deferredNames(m)["mid"] || len(deferredNames(m)) != 1 {
		t.Fatalf("after reopen deferred set = %v, want {mid}", deferredNames(m))
	}
}
