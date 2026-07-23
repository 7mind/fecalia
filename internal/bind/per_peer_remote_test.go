package bind

import (
	"net/netip"
	"testing"

	"github.com/7mind/wanbond/internal/config"
)

// buildTwoPeerEdge constructs and Opens a two-peer multi-exit edge over two shared sockets
// ("a","b"), each peer seeded at its OWN concentrator remote (unless a passed remote is the
// zero AddrPort, which SeedEdgePeerRemotes skips — the tolerant endpoint-less boot). It returns
// the Multipath plus the primary and beta peerStates. The caller drives ParseEndpoint itself so
// a test can assert install/misresolution around a specific call order.
func buildTwoPeerEdge(t *testing.T, pskA, pskB config.Key, remoteA, remoteB netip.AddrPort) (*Multipath, *peerState, *peerState) {
	t.Helper()
	clk := newFakeClock()
	paths := loopbackPaths(2)
	m, _, _ := newProbingMultipath(t, paths, pskA, clk)

	betaSched, betaProbers, betaFactory := concPeerWiring(t, paths, pskB, 0x0BEECAFE, clk)
	if err := m.AddConcentratorPeer("beta", pskB, betaSched, betaProbers, betaFactory); err != nil {
		t.Fatalf("AddConcentratorPeer: %v", err)
	}
	if err := m.SeedEdgePeerRemotes([]netip.AddrPort{remoteA, remoteB}); err != nil {
		t.Fatalf("SeedEdgePeerRemotes: %v", err)
	}
	if remoteA.IsValid() {
		if _, err := m.ParseEndpoint(remoteA.String()); err != nil {
			t.Fatalf("ParseEndpoint(remoteA): %v", err)
		}
	}
	if remoteB.IsValid() {
		if _, err := m.ParseEndpoint(remoteB.String()); err != nil {
			t.Fatalf("ParseEndpoint(remoteB): %v", err)
		}
	}
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m, m.peerState, m.peersByName["beta"]
}

// remotesOfPeer snapshots each of a peer's per-path views' selected remote, keyed by path name,
// for a byte-identical before/after comparison.
func remotesOfPeer(t *testing.T, p *peerState) map[string]netip.AddrPort {
	t.Helper()
	out := make(map[string]netip.AddrPort, len(p.paths))
	for _, pp := range p.paths {
		ap, ok := pp.getRemote()
		if !ok {
			t.Fatalf("peer %q path %q has no remote", p.name, pp.name)
		}
		out[pp.name] = ap
	}
	return out
}

// TestSetPeerRemoteForRepointsOnlyTargetPeer is the T252/G28 acceptance for the per-peer
// hub-failover seam: with two peers bound over the SAME shared sockets, SetPeerRemoteFor on peer
// A must repoint EVERY peer-A path at the new remote AND re-baseline A's resequencer, while every
// peer-B path's remote and B's whole resequencer state stay BYTE-IDENTICAL — the per-peer D32
// boundary that the removed bind-global defaultRemote coupling would have violated.
func TestSetPeerRemoteForRepointsOnlyTargetPeer(t *testing.T) {
	pskA := testKey(t, 0x11)
	pskB := testKey(t, 0x22)
	remoteA := netip.MustParseAddrPort("203.0.113.10:51820")  // primary's (witness B) concentrator
	remoteB := netip.MustParseAddrPort("198.51.100.20:51820") // beta's (repoint target A) concentrator
	newRemoteB := netip.MustParseAddrPort("198.51.100.99:51820")

	m, primary, beta := buildTwoPeerEdge(t, pskA, pskB, remoteA, remoteB)

	// Capture witness (primary/B) state BEFORE the repoint.
	witnessRemotesBefore := remotesOfPeer(t, primary)
	witnessRQ := primary.resequencer.Load()
	betaRQ := beta.resequencer.Load()
	if witnessRQ == nil || betaRQ == nil {
		t.Fatalf("resequencer not instantiated (primary=%v beta=%v)", witnessRQ != nil, betaRQ != nil)
	}
	witnessStatsBefore := witnessRQ.Stats()
	betaRebaselinesBefore := betaRQ.Stats().Rebaselines

	// Repoint ONLY peer A (beta).
	if err := m.SetPeerRemoteFor("beta", newRemoteB); err != nil {
		t.Fatalf("SetPeerRemoteFor(beta): %v", err)
	}

	// Every peer-A path observes the new remote.
	for name, ap := range remotesOfPeer(t, beta) {
		if ap != newRemoteB {
			t.Errorf("beta path %q remote = %v, want %v (the repoint target)", name, ap, newRemoteB)
		}
	}
	// Peer A's resequencer was re-baselined exactly once (the D32 hub-switch resync).
	if got := betaRQ.Stats().Rebaselines; got != betaRebaselinesBefore+1 {
		t.Errorf("beta resequencer Rebaselines = %d, want %d (SetPeerRemoteFor must resync the repointed peer)", got, betaRebaselinesBefore+1)
	}

	// Peer B's per-path remotes are byte-identical.
	witnessRemotesAfter := remotesOfPeer(t, primary)
	if len(witnessRemotesAfter) != len(witnessRemotesBefore) {
		t.Fatalf("witness path count changed: %d → %d", len(witnessRemotesBefore), len(witnessRemotesAfter))
	}
	for name, before := range witnessRemotesBefore {
		if after := witnessRemotesAfter[name]; after != before {
			t.Errorf("witness path %q remote disturbed by beta's repoint: %v → %v", name, before, after)
		}
	}
	// Peer B's resequencer is the SAME instance with byte-identical stats (untouched).
	if primary.resequencer.Load() != witnessRQ {
		t.Error("witness resequencer instance replaced by beta's repoint")
	}
	if got := witnessRQ.Stats(); got != witnessStatsBefore {
		t.Errorf("witness resequencer state disturbed by beta's repoint: %+v → %+v", witnessStatsBefore, got)
	}
}

// TestSetPeerRemoteForReseedsAtNewHubAfterCloseOpen is the D101 acceptance: a per-peer repoint
// must ALSO update the DURABLE seeds (p.configuredRemote + the edgePeerByRemote keying) so a
// subsequent engine Close/Open re-seeds the repointed peer's fresh paths at its CURRENT hub — not
// its original (stale) boot hub — and ParseEndpoint resolves the new remote to the right virt.
func TestSetPeerRemoteForReseedsAtNewHubAfterCloseOpen(t *testing.T) {
	pskA := testKey(t, 0x33)
	pskB := testKey(t, 0x44)
	remoteA := netip.MustParseAddrPort("203.0.113.10:51820")
	remoteB := netip.MustParseAddrPort("198.51.100.20:51820")
	newRemoteB := netip.MustParseAddrPort("198.51.100.99:51820")

	m, _, beta := buildTwoPeerEdge(t, pskA, pskB, remoteA, remoteB)

	if err := m.SetPeerRemoteFor("beta", newRemoteB); err != nil {
		t.Fatalf("SetPeerRemoteFor(beta): %v", err)
	}

	// Durable seeds re-keyed: configuredRemote advanced, old key out, new key → beta.
	if !beta.hasConfiguredRemote || beta.configuredRemote != newRemoteB {
		t.Fatalf("beta.configuredRemote = %v (has=%v), want %v — Close/Open would re-seed at the stale hub (D101)",
			beta.configuredRemote, beta.hasConfiguredRemote, newRemoteB)
	}
	if m.edgePeerByRemote[newRemoteB] != beta {
		t.Errorf("edgePeerByRemote[newRemoteB] != beta — ParseEndpoint(newRemoteB) would misresolve")
	}
	if _, stillKeyed := m.edgePeerByRemote[remoteB]; stillKeyed {
		t.Errorf("edgePeerByRemote still keys the OLD remote %v — ParseEndpoint(oldAP) would still map to beta", remoteB)
	}

	// Simulate the engine's Close/Open re-seed cycle (device.upLocked → Close() → Open()).
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("re-Open: %v", err)
	}

	// Beta's fresh paths seed at the NEW hub; primary's at its (unchanged) own hub.
	betaAfter := m.peersByName["beta"]
	for name, ap := range remotesOfPeer(t, betaAfter) {
		if ap != newRemoteB {
			t.Errorf("after Close/Open, beta path %q re-seeded at %v, want %v (its CURRENT hub)", name, ap, newRemoteB)
		}
	}
	for name, ap := range remotesOfPeer(t, m.peerState) {
		if ap != remoteA {
			t.Errorf("after Close/Open, primary path %q re-seeded at %v, want %v", name, ap, remoteA)
		}
	}

	// ParseEndpoint resolves the NEW remote to beta's virt; the OLD remote no longer maps to beta.
	epNew, err := m.ParseEndpoint(newRemoteB.String())
	if err != nil {
		t.Fatalf("ParseEndpoint(newRemoteB): %v", err)
	}
	if epNew.(*udpEndpoint) != betaAfter.virt {
		t.Error("ParseEndpoint(newRemoteB) did not resolve to beta's virt")
	}
	epOld, err := m.ParseEndpoint(remoteB.String())
	if err != nil {
		t.Fatalf("ParseEndpoint(oldRemoteB): %v", err)
	}
	if epOld.(*udpEndpoint) == betaAfter.virt {
		t.Error("ParseEndpoint(oldRemoteB) still resolves to beta's virt — the stale endpoint→peer key was not removed (D101)")
	}
	if epOld.(*udpEndpoint) != m.virt {
		t.Error("ParseEndpoint(oldRemoteB) did not fall back to the primary's virt after the key removal")
	}
}

// TestSetPeerRemoteForInstallsRemoteForUnseededPeer is the D100 install leg: a peer that booted
// endpoint-less (its hostname had no address at boot, so SeedEdgePeerRemotes skipped it — no
// configuredRemote, no edgePeerByRemote key) is left to misresolve on ParseEndpoint (its future
// endpoint would fall back to the primary's virt). SetPeerRemoteFor must be able to INSTALL the
// remote — seeding both durable maps — so the previously-unseeded peer owns its endpoint.
func TestSetPeerRemoteForInstallsRemoteForUnseededPeer(t *testing.T) {
	pskA := testKey(t, 0x55)
	pskB := testKey(t, 0x66)
	remoteA := netip.MustParseAddrPort("203.0.113.10:51820")
	installB := netip.MustParseAddrPort("198.51.100.20:51820")

	// beta boots endpoint-less: the zero AddrPort makes SeedEdgePeerRemotes skip it.
	m, _, beta := buildTwoPeerEdge(t, pskA, pskB, remoteA, netip.AddrPort{})

	// Pre-install: beta has no configured remote, no endpoint→peer key, and its boot paths stay
	// remoteless (the tolerant endpoint-less boot), so ParseEndpoint(installB) would misresolve.
	if beta.hasConfiguredRemote {
		t.Fatalf("beta unexpectedly has a configured remote at boot")
	}
	if _, keyed := m.edgePeerByRemote[installB]; keyed {
		t.Fatalf("edgePeerByRemote already keys beta's install remote before install")
	}
	for _, pp := range beta.paths {
		if _, ok := pp.getRemote(); ok {
			t.Fatalf("endpoint-less beta path %q already has a remote — tolerant boot violated", pp.name)
		}
	}

	// INSTALL beta's remote through the per-peer seam.
	if err := m.SetPeerRemoteFor("beta", installB); err != nil {
		t.Fatalf("SetPeerRemoteFor(beta) install: %v", err)
	}

	if !beta.hasConfiguredRemote || beta.configuredRemote != installB {
		t.Errorf("install did not seed beta.configuredRemote: got %v (has=%v), want %v", beta.configuredRemote, beta.hasConfiguredRemote, installB)
	}
	if m.edgePeerByRemote[installB] != beta {
		t.Errorf("install did not key edgePeerByRemote[installB] → beta")
	}
	for _, pp := range beta.paths {
		if ap, ok := pp.getRemote(); !ok || ap != installB {
			t.Errorf("beta path %q remote after install = %v (ok=%v), want %v", pp.name, ap, ok, installB)
		}
	}
	// ParseEndpoint now resolves the installed endpoint to beta's OWN virt, not the primary's.
	ep, err := m.ParseEndpoint(installB.String())
	if err != nil {
		t.Fatalf("ParseEndpoint(installB): %v", err)
	}
	if ep.(*udpEndpoint) != beta.virt {
		t.Error("ParseEndpoint(installB) resolved to the primary's virt — the D100 install misresolution was not closed")
	}
}

// TestSetPeerRemoteForUnknownPeer pins the fail-fast contract: repointing a name no peer holds is
// a wiring defect, returned as an error rather than silently repointing nothing.
func TestSetPeerRemoteForUnknownPeer(t *testing.T) {
	pskA := testKey(t, 0x77)
	pskB := testKey(t, 0x88)
	remoteA := netip.MustParseAddrPort("203.0.113.10:51820")
	remoteB := netip.MustParseAddrPort("198.51.100.20:51820")
	m, _, _ := buildTwoPeerEdge(t, pskA, pskB, remoteA, remoteB)

	if err := m.SetPeerRemoteFor("gamma", netip.MustParseAddrPort("192.0.2.1:51820")); err == nil {
		t.Fatal("SetPeerRemoteFor accepted an unknown peer name, want a fail-fast error")
	}
}
