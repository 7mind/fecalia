package bind

import (
	"net"
	"net/netip"
	"syscall"
	"testing"

	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/frame"
)

// TestRemoveDeferredPathMisalignedPeerProbersFailsFast is the D42 residual regression for
// removeDurableLocked's fail-fast alignment guard: on a 2-peer concentrator, if ANY bound
// peer's prober set ever falls out of index-alignment with the durable membership (m.defs) —
// the class of wiring defect a partial fan-out (a promotion or admission reaching only SOME
// peers) produces — removing a path at the misaligned index must return a wiring-defect
// error, NOT panic with an index-out-of-range slice operation. It directly simulates the
// misalignment (white-box: same package) rather than relying on a currently-reachable code
// path, since the OTHER two fixes in this task (promoteDeferredLocked's fan-out, AddPath's
// already-shipped fan-out) keep every legitimate admission path aligned — this guards the
// invariant defensively, exactly as Open's `pp.prober = p.probers[i]` guard already does for
// the read side. Before the removeDurableLocked guard this test PANICS (slice bounds out of
// range slicing p.probers[defIdx+1:] past a peer's short probers slice); after it, RemovePath
// returns a descriptive error instead.
func TestRemoveDeferredPathMisalignedPeerProbersFailsFast(t *testing.T) {
	pskA := testKey(t, 0x91)
	pskB := testKey(t, 0x92)
	clk := newFakeClock()
	paths := loopbackPaths(1) // boot path "a"
	m, _, _ := newProbingMultipath(t, paths, pskA, clk)

	betaSched, betaProbers, betaFactory := concPeerWiring(t, paths, pskB, 0x0BAD1, clk)
	if err := m.AddConcentratorPeer("beta", pskB, betaSched, betaProbers, betaFactory); err != nil {
		t.Fatalf("AddConcentratorPeer: %v", err)
	}
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	// A runtime AddPath that DEFERS (EADDRNOTAVAIL): the already-shipped AddPath fan-out mints
	// a Down prober for EVERY peer, keeping m.defs and every peer's probers aligned at length 2.
	m.addPathListen = func(_ netip.Addr, _ uint16, _ string) (*net.UDPConn, error) {
		return nil, syscall.EADDRNOTAVAIL
	}
	if err := m.AddPath(config.Path{Name: "deferred-c", SourceAddr: netip.MustParseAddr("127.0.0.1")}); err != nil {
		t.Fatalf("AddPath (deferred): %v", err)
	}

	beta := m.peersByName["beta"]
	if len(m.defs) != 2 || len(beta.probers) != 2 {
		t.Fatalf("precondition: m.defs=%d beta.probers=%d, want 2 and 2", len(m.defs), len(beta.probers))
	}

	// Simulate the wiring defect the guard defends against: beta's prober fan-out fell short
	// of m.defs (exactly the shape a partial per-peer fan-out — reaching only SOME peers —
	// would leave behind). Mutated directly (white-box) under m.mu, mirroring how the real
	// fan-out sites hold the lock while mutating p.probers.
	m.mu.Lock()
	beta.probers = beta.probers[:1]
	m.mu.Unlock()

	// RemovePath on the misaligned index ("deferred-c", defIdx=1) must fail fast with a
	// descriptive wiring-defect error rather than panicking on beta.probers[2:] (out of range
	// on a length-1 slice).
	err := m.RemovePath("deferred-c")
	if err == nil {
		t.Fatal("RemovePath with a misaligned peer prober set succeeded, want a wiring-defect error")
	}

	// Neither m.defs nor the (still-misaligned) beta.probers were mutated by the failed
	// removal: the guard checks BEFORE mutating, so a caller could in principle repair the
	// wiring and retry rather than being left with a half-spliced membership.
	if len(m.defs) != 2 {
		t.Fatalf("m.defs mutated by a failed removal: len=%d, want 2 (untouched)", len(m.defs))
	}
	if len(beta.probers) != 1 {
		t.Fatalf("beta.probers mutated by a failed removal: len=%d, want 1 (untouched)", len(beta.probers))
	}
}

// TestRemoveDeferredPathInRangeMisalignedPeerProbersFailsFast is the ROUND-2 strengthening of
// removeDurableLocked's fail-fast alignment guard (D42): an INDEX-ONLY bound check
// (defIdx >= len(p.probers)) catches out-of-range divergence, but NOT an IN-RANGE one — a peer
// whose prober set has grown LONGER than m.defs (or fallen short at the tail while defIdx still
// happens to be in bounds) passes an index-only check silently and lets removeDurableLocked
// splice an entry that is no longer p.probers[defIdx]'s TRUE counterpart in m.defs, corrupting
// the durable per-peer alignment the whole fan-out (AddPath/promoteDeferredLocked) depends on —
// exactly the "silently splices the wrong entry" failure the guard's own doc comment claims to
// prevent. Before the length-based guard this RemovePath call would SUCCEED (silently
// mis-splicing, since defIdx=1 is well within bounds of a length-3 probers slice); after it,
// RemovePath returns a descriptive wiring-defect error instead, leaving m.defs and the
// (still-misaligned) beta.probers untouched.
func TestRemoveDeferredPathInRangeMisalignedPeerProbersFailsFast(t *testing.T) {
	pskA := testKey(t, 0x95)
	pskB := testKey(t, 0x96)
	clk := newFakeClock()
	paths := loopbackPaths(1) // boot path "a"
	m, _, _ := newProbingMultipath(t, paths, pskA, clk)

	betaSched, betaProbers, betaFactory := concPeerWiring(t, paths, pskB, 0x0BAD3, clk)
	if err := m.AddConcentratorPeer("beta", pskB, betaSched, betaProbers, betaFactory); err != nil {
		t.Fatalf("AddConcentratorPeer: %v", err)
	}
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	// A runtime AddPath that DEFERS (EADDRNOTAVAIL): the already-shipped AddPath fan-out mints
	// a Down prober for EVERY peer, keeping m.defs and every peer's probers aligned at length 2.
	m.addPathListen = func(_ netip.Addr, _ uint16, _ string) (*net.UDPConn, error) {
		return nil, syscall.EADDRNOTAVAIL
	}
	if err := m.AddPath(config.Path{Name: "deferred-c", SourceAddr: netip.MustParseAddr("127.0.0.1")}); err != nil {
		t.Fatalf("AddPath (deferred): %v", err)
	}

	beta := m.peersByName["beta"]
	if len(m.defs) != 2 || len(beta.probers) != 2 {
		t.Fatalf("precondition: m.defs=%d beta.probers=%d, want 2 and 2", len(m.defs), len(beta.probers))
	}

	// Simulate an IN-RANGE wiring defect: beta's prober fan-out ran TWICE for one entry (or
	// some other partial-fan-out artifact), leaving beta.probers LONGER than m.defs while
	// defIdx=1 ("deferred-c") remains well within its bounds — the class of divergence an
	// index-only bound check cannot see. Mutated directly (white-box) under m.mu, mirroring how
	// the real fan-out sites hold the lock while mutating p.probers.
	m.mu.Lock()
	beta.probers = append(beta.probers, beta.probers[0])
	m.mu.Unlock()

	// RemovePath on the in-range but length-divergent index ("deferred-c", defIdx=1) must fail
	// fast with a wiring-defect error rather than silently splicing beta.probers[1] out from
	// under the wrong path.
	err := m.RemovePath("deferred-c")
	if err == nil {
		t.Fatal("RemovePath with an in-range (length-divergent) peer prober set succeeded, want a wiring-defect error")
	}

	// Neither m.defs nor the (still-misaligned) beta.probers were mutated by the failed
	// removal: the guard checks BEFORE mutating.
	if len(m.defs) != 2 {
		t.Fatalf("m.defs mutated by a failed removal: len=%d, want 2 (untouched)", len(m.defs))
	}
	if len(beta.probers) != 3 {
		t.Fatalf("beta.probers mutated by a failed removal: len=%d, want 3 (untouched)", len(beta.probers))
	}
}

// TestReconcilePromotionFansViewAndSchedulerToEveryPeer is the T124/D42 core acceptance: on a
// 2-peer concentrator, a deferred boot path's PROMOTION (StartReconcileLoop's background
// reconcile, once the source_addr becomes assignable) must give EVERY bound peer — not just
// the primary — a receive-demux VIEW of the freshly-bound socket and a scheduler entry, and
// each peer's DATA must actually flow (demux + resequencer delivery) over the promoted path.
// Pre-fix, promoteDeferredLocked attached only the primary's view, leaving beta view-less: its
// frames on the promoted socket would never demux to it (this test's demuxInbound/DATA
// assertions fail pre-fix — beta's view is nil, so peerPathByName(beta, "deferred") is nil and
// the test fails before it can even attempt the DATA exchange).
func TestReconcilePromotionFansViewAndSchedulerToEveryPeer(t *testing.T) {
	pskA := testKey(t, 0x81)
	pskB := testKey(t, 0x82)
	clk := newFakeClock()
	paths := []config.Path{
		{Name: "bindable", SourceAddr: netip.MustParseAddr("127.0.0.1")},
		{Name: "deferred", SourceAddr: netip.MustParseAddr(unassignableSource)},
	}
	m, _, _ := newProbingMultipath(t, paths, pskA, clk)

	betaSched, betaProbers, betaFactory := concPeerWiring(t, paths, pskB, 0x0B00B00, clk)
	if err := m.AddConcentratorPeer("beta", pskB, betaSched, betaProbers, betaFactory); err != nil {
		t.Fatalf("AddConcentratorPeer: %v", err)
	}
	binder := &fakeDeferredBinder{}
	m.deferredListen = binder.listen

	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	primary := m.peerState
	beta := m.peersByName["beta"]

	if len(m.deferred) != 1 {
		t.Fatalf("after Open: deferred=%d, want 1", len(m.deferred))
	}
	if peerPathByName(primary, "deferred") != nil || peerPathByName(beta, "deferred") != nil {
		t.Fatal("the deferred path already has a view before promotion")
	}

	// The address becomes assignable; the background reconcile promotes it.
	binder.arm()
	m.reconcileDeferred()
	if len(m.deferred) != 0 {
		t.Fatalf("deferred after reconcile = %d, want 0 (promoted)", len(m.deferred))
	}

	// Every bound peer got a VIEW of the promoted path.
	primaryView := peerPathByName(primary, "deferred")
	betaView := peerPathByName(beta, "deferred")
	if primaryView == nil || betaView == nil {
		t.Fatalf("promoted path missing a view: primary=%v beta=%v", pathNamesOfPeer(primary), pathNamesOfPeer(beta))
	}
	if primaryView.sharedPathState != betaView.sharedPathState {
		t.Fatal("primary and beta's promoted views do not share the same socket")
	}
	if primaryView.prober == betaView.prober {
		t.Fatal("primary and beta share ONE prober for the promoted path — the fan-out did not mint/reuse per-peer state")
	}
	assertProberKeyedOn(t, "primary promoted prober", primaryView.prober, pskA, pskB)
	assertProberKeyedOn(t, "beta promoted prober", betaView.prober, pskB, pskA)

	// Both peers got a SCHEDULER entry too: exercised behaviourally below by proving each
	// peer's DATA actually demuxes and delivers over the promoted (now-shared) socket — the
	// "frames never demux to it" residual this task fixes. (A missing scheduler entry would
	// leave the peer's view built but the path unschedulable for egress; the receive-side
	// behavioural proof below is this test's DATA-flow acceptance.)
	dataCodecA, err := frame.NewCodec(pskA)
	if err != nil {
		t.Fatalf("build peer A data codec: %v", err)
	}
	dataCodecB, err := frame.NewCodec(pskB)
	if err != nil {
		t.Fatalf("build peer B data codec: %v", err)
	}

	srcA := netip.MustParseAddrPort("203.0.113.10:51820")
	srcB := netip.MustParseAddrPort("198.51.100.10:51820")

	// Bind each source to its own peer through an authenticated PROBE over the PROMOTED
	// socket (demuxInbound's production source-demux, entered via the primary's view — the
	// same view the promoted path's single Bind-owned reader holds).
	m.demuxInbound(primaryView, authProbe(t, pskA, primaryView.id, 1, clk), srcA)
	if bound, ok := m.lookupPeerBySource(srcA); !ok || bound != primary {
		t.Fatalf("srcA did not bind to the primary over the promoted path: bound=%v ok=%v", bound, ok)
	}
	m.demuxInbound(primaryView, authProbe(t, pskB, betaView.id, 1, clk), srcB)
	if bound, ok := m.lookupPeerBySource(srcB); !ok || bound != beta {
		t.Fatalf("srcB did not bind to beta over the promoted path: bound=%v ok=%v", bound, ok)
	}

	// A DATA frame from each bound source demuxes to that peer's OWN resequencer over the
	// promoted path — the acceptance's "both peers' DATA flows on the promoted path".
	m.demuxInbound(primaryView, mustEncodeData(t, dataCodecA, 0, primaryView.id, "A-data"), srcA)
	m.demuxInbound(primaryView, mustEncodeData(t, dataCodecB, 0, betaView.id, "B-data"), srcB)

	itA, ok := primary.resequencer.Load().Pop()
	if !ok || string(itA.Payload) != "A-data" {
		t.Fatalf("primary did not receive DATA over the promoted path (ok=%v)", ok)
	}
	itB, ok := beta.resequencer.Load().Pop()
	if !ok || string(itB.Payload) != "B-data" {
		t.Fatalf("beta did not receive DATA over the promoted path (ok=%v) — the promotion left beta's frames un-demuxed", ok)
	}
}

// TestConcentratorCloseOpenAfterDeferredPromoteRebuildsBothPeers is the T124 acceptance (c): a
// deferred path that is PROMOTED (via the background reconcile, exercising promoteDeferredLocked's
// fan-out) and THEN taken through a Close→Open cycle (an interface Down/Up, or the amneziawg
// engine's route-change cycle) must rebuild EVERY bound peer's view of it, from EVERY peer's
// still-aligned p.probers, without an out-of-range panic indexing p.probers[i] at Open (the D42
// scenario: a peer whose prober fan-out fell short of m.defs). This is a genuinely different
// path from TestConcentratorDeferredAddThenReopenFansPerPeerProbers (which reopens a path that
// was NEVER promoted): here the path IS promoted first, proving promoteDeferredLocked's own
// fan-out leaves durable state (m.defs / every peer's probers) exactly as aligned as it found it,
// so a subsequent reopen is unaffected by having gone through a promotion.
func TestConcentratorCloseOpenAfterDeferredPromoteRebuildsBothPeers(t *testing.T) {
	pskA := testKey(t, 0x83)
	pskB := testKey(t, 0x84)
	clk := newFakeClock()
	paths := loopbackPaths(1) // boot path "a"
	m, _, _ := newProbingMultipath(t, paths, pskA, clk)

	betaSched, betaProbers, betaFactory := concPeerWiring(t, paths, pskB, 0x0C105E, clk)
	if err := m.AddConcentratorPeer("beta", pskB, betaSched, betaProbers, betaFactory); err != nil {
		t.Fatalf("AddConcentratorPeer: %v", err)
	}
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	// A runtime AddPath that DEFERS (EADDRNOTAVAIL), fanned to both peers by the already-shipped
	// AddPath fix.
	m.addPathListen = func(_ netip.Addr, _ uint16, _ string) (*net.UDPConn, error) {
		return nil, syscall.EADDRNOTAVAIL
	}
	if err := m.AddPath(config.Path{Name: "runtime-deferred", SourceAddr: netip.MustParseAddr("127.0.0.1")}); err != nil {
		t.Fatalf("AddPath (deferred): %v", err)
	}
	if len(m.defs) != 2 || len(m.deferred) != 1 {
		t.Fatalf("after deferred AddPath: m.defs=%d m.deferred=%d, want 2 and 1", len(m.defs), len(m.deferred))
	}

	// The address becomes assignable; the background reconcile PROMOTES it — exercising this
	// task's fan-out fix — BEFORE the reopen below.
	m.reconcileDeferred()
	if len(m.deferred) != 0 {
		t.Fatalf("deferred after reconcile = %d, want 0 (promoted)", len(m.deferred))
	}
	primary := m.peerState
	beta := m.peersByName["beta"]
	if peerPathByName(primary, "runtime-deferred") == nil || peerPathByName(beta, "runtime-deferred") == nil {
		t.Fatalf("promotion did not fan a view to both peers before reopen: primary=%v beta=%v", pathNamesOfPeer(primary), pathNamesOfPeer(beta))
	}

	// Close -> Open (an interface Down/Up, or a route-change cycle): must rebuild BOTH peers'
	// views for EVERY durable path, including the just-promoted one, without panicking on
	// p.probers[i] out of range.
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("reopen after a deferred-then-promoted path: %v", err)
	}

	primary = m.peerState
	beta = m.peersByName["beta"]
	pa := peerPathByName(primary, "runtime-deferred")
	pb := peerPathByName(beta, "runtime-deferred")
	if pa == nil || pb == nil {
		t.Fatalf("promoted-then-reopened path missing a view: primary=%v beta=%v", pathNamesOfPeer(primary), pathNamesOfPeer(beta))
	}
	if pa.prober == pb.prober {
		t.Fatal("both peers share ONE prober after reopen — the durable membership desynced across the promote+reopen cycle")
	}
	assertProberKeyedOn(t, "primary reopened prober", pa.prober, pskA, pskB)
	assertProberKeyedOn(t, "beta reopened prober", pb.prober, pskB, pskA)
}
