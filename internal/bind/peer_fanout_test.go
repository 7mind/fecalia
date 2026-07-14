package bind

import (
	"io"
	"net/netip"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/log"
	"github.com/7mind/wanbond/internal/sched"
	"github.com/7mind/wanbond/internal/telemetry"
)

// bindSecondPeer constructs a SECOND peerState over the SAME shared sockets an already-Open
// Multipath holds, and binds it into m.peers/m.peersByName. It stands in for the concentrator's
// per-peer wiring (a later G4 task) so this test can exercise the runtime shared-path fan-out
// while >=2 peerStates are bound: it gives the peer its own DynamicScheduler, its own probe
// initiator factory + boot probers (aligned with the durable membership m.defs), and a
// peerPathState VIEW over each currently-bound shared socket. It must run after m.Open.
func bindSecondPeer(t testing.TB, m *Multipath, name string, psk config.Key, clk telemetry.Clock) *peerState {
	t.Helper()
	lg, err := log.New("error", io.Discard)
	if err != nil {
		t.Fatalf("build logger: %v", err)
	}
	cfg := telemetry.ProberConfig{
		LossWindow: 0,
		Liveness:   telemetry.LivenessConfig{DownAfter: testProbeDownAfter, UpAfterSuccesses: testProbeUpSucc},
	}
	// A DISTINCT probe session id from the primary's: a concentrator peer runs its own probe
	// session, and it proves the fan-out mints genuinely per-peer probers (not shared handles).
	const secondPeerSessionID uint64 = 0x0123456789ABCDEF
	newProber := func(pname string, id uint8) *telemetry.Prober {
		return telemetry.NewProber(pname, id, secondPeerSessionID, psk, cfg, clk, lg)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Boot probers aligned with the durable membership (m.defs), stamped with each shared
	// socket's path-id so DATA and PROBE agree on the wire, exactly as Open does for the primary.
	probers := make([]*telemetry.Prober, len(m.defs))
	health := make([]sched.PathHealth, len(m.defs))
	byName := make(map[string]*telemetry.Prober, len(m.defs))
	for i := range m.defs {
		var id uint8
		for _, sp := range m.shared {
			if sp.name == m.defs[i].Name {
				id = sp.id
			}
		}
		probers[i] = newProber(m.defs[i].Name, id)
		health[i] = probers[i]
		byName[m.defs[i].Name] = probers[i]
	}
	scheduler, err := sched.NewActiveBackup(health, sched.Config{FailbackAfter: time.Hour}, clk, lg)
	if err != nil {
		t.Fatalf("build second-peer scheduler: %v", err)
	}
	// Build the peer through the SAME seam production uses (newPeerState), so its Reflector
	// and every codec derive from THIS peer's psk (T84) rather than being hand-wired.
	p := newPeerState(name, psk, scheduler, newProber, probers)
	sendCodec, err := p.newCodec()
	if err != nil {
		t.Fatalf("build second-peer send codec: %v", err)
	}
	p.sendCodec = sendCodec
	// A peerPathState VIEW over each bound shared socket, index-aligned with m.shared (which,
	// with no deferred paths, is also aligned with m.defs) so the peer's scheduler and paths
	// stay index-aligned exactly as the primary's do.
	for _, sp := range m.shared {
		codec, cerr := p.newCodec()
		if cerr != nil {
			t.Fatalf("build second-peer path codec: %v", cerr)
		}
		p.paths = append(p.paths, &peerPathState{sharedPathState: sp, peer: p, codec: codec, prober: byName[sp.name]})
	}
	m.peers = append(m.peers, p)
	m.peersByName[name] = p
	// Register the peer's virt so an outbound Send to it routes to THIS peerState (the
	// concentrator's per-peer wiring does the same when it binds a peer).
	m.peerByVirt[p.virt] = p
	return p
}

// pathNamesOfPeer snapshots a peer's per-(peer,path) view names, for assertions.
func pathNamesOfPeer(p *peerState) []string {
	out := make([]string, len(p.paths))
	for i, pp := range p.paths {
		out[i] = pp.name
	}
	return out
}

// peerPathByName returns the peer's per-(peer,path) view of the named shared path, or nil.
func peerPathByName(p *peerState, name string) *peerPathState {
	for _, pp := range p.paths {
		if pp.name == name {
			return pp
		}
	}
	return nil
}

// TestMultipathSharedPathFanOutAcrossPeers is the G4 fan-out acceptance for the peerState /
// pathState split: with TWO peerStates bound over the same shared sockets, a runtime AddPath of
// a SHARED path must instantiate per-(peer,path) state (codec, remote, prober, tx/rx) for BOTH
// peers, and a runtime RemovePath must tear that per-(peer,path) state down for BOTH peers,
// leaving each peer's remaining paths untouched. It proves the fan-out has a single owner
// (attachSharedPathLocked / RemovePath) exercised while >=2 peerStates exist.
func TestMultipathSharedPathFanOutAcrossPeers(t *testing.T) {
	psk := testKey(t, 0x41)
	clk := newFakeClock()
	m, _, _ := newProbingMultipath(t, loopbackPaths(1), psk, clk) // one shared path, "a"
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	primary := m.peerState
	second := bindSecondPeer(t, m, "peer-2", psk, clk)

	// Preconditions: two bound peers, each with exactly the one boot path "a" over the one
	// shared socket. Capture each peer's original path object to prove the remove leaves it
	// untouched.
	if len(m.peers) != 2 {
		t.Fatalf("bound peers = %d, want 2", len(m.peers))
	}
	if len(m.shared) != 1 {
		t.Fatalf("shared sockets = %d, want 1", len(m.shared))
	}
	primaryOrigA := peerPathByName(primary, "a")
	secondOrigA := peerPathByName(second, "a")
	if primaryOrigA == nil || secondOrigA == nil {
		t.Fatalf("boot path 'a' missing: primary=%v second=%v", pathNamesOfPeer(primary), pathNamesOfPeer(second))
	}
	if primaryOrigA.sharedPathState != secondOrigA.sharedPathState {
		t.Fatal("the two peers' boot path 'a' must VIEW the same shared socket")
	}

	// --- AddPath a SHARED path at runtime: the fan-out must reach BOTH peers. ---
	if err := m.AddPath(config.Path{Name: "shared-b", SourceAddr: netip.MustParseAddr("127.0.0.1")}); err != nil {
		t.Fatalf("AddPath: %v", err)
	}
	if len(m.shared) != 2 {
		t.Fatalf("shared sockets after add = %d, want 2", len(m.shared))
	}
	sharedB := m.shared[1]
	if sharedB.name != "shared-b" {
		t.Fatalf("new shared socket name = %q, want %q", sharedB.name, "shared-b")
	}

	for _, tc := range []struct {
		who string
		p   *peerState
	}{{"primary", primary}, {"second", second}} {
		pp := peerPathByName(tc.p, "shared-b")
		if pp == nil {
			t.Fatalf("%s peer has no per-(peer,path) state for 'shared-b' after add (fan-out missed it): %v", tc.who, pathNamesOfPeer(tc.p))
		}
		// Per-(peer,path) state was instantiated: its OWN codec + prober, VIEWING the ONE shared
		// socket (not a duplicate socket per peer).
		if pp.sharedPathState != sharedB {
			t.Fatalf("%s peer's 'shared-b' does not view the shared socket (per-peer socket duplicated)", tc.who)
		}
		if pp.codec == nil {
			t.Fatalf("%s peer's 'shared-b' has no per-(peer,path) codec", tc.who)
		}
		if pp.prober == nil {
			t.Fatalf("%s peer's 'shared-b' has no per-(peer,path) prober", tc.who)
		}
		if pp.peer != tc.p {
			t.Fatalf("%s peer's 'shared-b' back-references the wrong peerState", tc.who)
		}
	}
	// The two peers minted DISTINCT probers for the shared path (genuine per-(peer,path) state,
	// not one shared handle).
	if peerPathByName(primary, "shared-b").prober == peerPathByName(second, "shared-b").prober {
		t.Fatal("both peers share ONE prober for 'shared-b' — the fan-out did not mint per-peer state")
	}

	// --- RemovePath the shared path: the teardown must reach BOTH peers. ---
	if err := m.RemovePath("shared-b"); err != nil {
		t.Fatalf("RemovePath: %v", err)
	}
	if len(m.shared) != 1 {
		t.Fatalf("shared sockets after remove = %d, want 1", len(m.shared))
	}
	for _, tc := range []struct {
		who  string
		p    *peerState
		orig *peerPathState
	}{{"primary", primary, primaryOrigA}, {"second", second, secondOrigA}} {
		if peerPathByName(tc.p, "shared-b") != nil {
			t.Fatalf("%s peer still holds per-(peer,path) state for 'shared-b' after remove: %v", tc.who, pathNamesOfPeer(tc.p))
		}
		if len(tc.p.paths) != 1 {
			t.Fatalf("%s peer path count after remove = %d, want 1 (remaining paths untouched)", tc.who, len(tc.p.paths))
		}
		remaining := peerPathByName(tc.p, "a")
		if remaining == nil {
			t.Fatalf("%s peer lost its remaining path 'a' on the remove: %v", tc.who, pathNamesOfPeer(tc.p))
		}
		if remaining != tc.orig {
			t.Fatalf("%s peer's remaining path 'a' object changed on the remove (remaining paths must be untouched)", tc.who)
		}
	}
}
