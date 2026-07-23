package bind

import (
	"net/netip"
	"testing"
)

// TestSeedEdgePeerRemotesRoutesEachPeerToOwnHub is the T251/Q68b bind-level acceptance for the
// multi-exit edge send-routing seam: with two configured edge peers each carrying its OWN
// concentrator endpoint, SeedEdgePeerRemotes + ParseEndpoint + Open must give EACH peer its own
// distinct virtual endpoint AND seed EACH peer's paths at ITS OWN remote — never collapse both
// onto the primary's virt or onto the single last-parsed bind-global default. It is the direct
// guard for the boot-Open per-peer remote precedence (the defect: both peers inherited
// m.defaultRemote, so one peer's whole bond egressed to the wrong concentrator).
func TestSeedEdgePeerRemotesRoutesEachPeerToOwnHub(t *testing.T) {
	pskA := testKey(t, 0x11)
	pskB := testKey(t, 0x22)
	clk := newFakeClock()
	paths := loopbackPaths(2) // two shared sockets ("a","b"), fanned out to both peers
	m, _, _ := newProbingMultipath(t, paths, pskA, clk)

	betaSched, betaProbers, betaFactory := concPeerWiring(t, paths, pskB, 0x0BEECAFE, clk)
	if err := m.AddConcentratorPeer("beta", pskB, betaSched, betaProbers, betaFactory); err != nil {
		t.Fatalf("AddConcentratorPeer: %v", err)
	}

	remoteA := netip.MustParseAddrPort("203.0.113.10:51820")  // the primary's concentrator
	remoteB := netip.MustParseAddrPort("198.51.100.20:51820") // beta's concentrator

	// Seed BEFORE the (simulated) UAPI parse + Open, exactly as device.Up does.
	if err := m.SeedEdgePeerRemotes([]netip.AddrPort{remoteA, remoteB}); err != nil {
		t.Fatalf("SeedEdgePeerRemotes: %v", err)
	}

	// ParseEndpoint resolves each configured endpoint to its OWNING peer's DISTINCT virt (the
	// engine stores that virt as the peer's send target; Send routes on it via peerByVirt).
	epA, err := m.ParseEndpoint(remoteA.String())
	if err != nil {
		t.Fatalf("ParseEndpoint(remoteA): %v", err)
	}
	epB, err := m.ParseEndpoint(remoteB.String())
	if err != nil {
		t.Fatalf("ParseEndpoint(remoteB): %v", err)
	}
	primary := m.peerState
	beta := m.peersByName["beta"]
	if epA.(*udpEndpoint) != primary.virt {
		t.Fatal("ParseEndpoint(remoteA) did not return the PRIMARY peer's virt")
	}
	if epB.(*udpEndpoint) != beta.virt {
		t.Fatal("ParseEndpoint(remoteB) returned the primary's virt, not beta's — beta's WG traffic would egress to the wrong concentrator")
	}
	if epA.(*udpEndpoint) == epB.(*udpEndpoint) {
		t.Fatal("both endpoints resolved to ONE virt (A1 per-peer virt violated for the multi-exit edge)")
	}
	// The send-side demux must map each virt back to its own peer.
	if m.peerByVirt[epA.(*udpEndpoint)] != primary || m.peerByVirt[epB.(*udpEndpoint)] != beta {
		t.Fatal("peerByVirt does not route each configured endpoint's virt to its owning peer")
	}

	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	// Open must seed EACH peer's paths at ITS OWN concentrator remote, on BOTH uplinks — not the
	// last-parsed bind-global default (remoteB) on both peers.
	for _, name := range []string{"a", "b"} {
		pa := peerPathByName(primary, name)
		pb := peerPathByName(beta, name)
		if pa == nil || pb == nil {
			t.Fatalf("boot path %q missing on a peer (primary=%v beta=%v)", name, pa, pb)
		}
		if got, ok := pa.getRemote(); !ok || got != remoteA {
			t.Errorf("primary path %q remote = %v (ok=%v), want %v (its OWN concentrator)", name, got, ok, remoteA)
		}
		if got, ok := pb.getRemote(); !ok || got != remoteB {
			t.Errorf("beta path %q remote = %v (ok=%v), want %v — a peer must not inherit another's hub", name, got, ok, remoteB)
		}
	}
}

// TestSeedEdgePeerRemotesLengthMismatch pins the fail-fast guard: a remotes slice whose length
// does not match the bound-peer count is a wiring defect, returned as an error rather than
// silently mis-indexing a peer's endpoint.
func TestSeedEdgePeerRemotesLengthMismatch(t *testing.T) {
	pskA := testKey(t, 0x33)
	clk := newFakeClock()
	m, _, _ := newProbingMultipath(t, loopbackPaths(1), pskA, clk) // one (primary) peer

	err := m.SeedEdgePeerRemotes([]netip.AddrPort{
		netip.MustParseAddrPort("203.0.113.1:51820"),
		netip.MustParseAddrPort("203.0.113.2:51820"),
	})
	if err == nil {
		t.Fatal("SeedEdgePeerRemotes accepted 2 remotes for 1 bound peer, want a length-mismatch error")
	}
}
