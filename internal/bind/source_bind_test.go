package bind

import (
	"bytes"
	"crypto/rand"
	"testing"

	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/frame"
)

// twoPeerConcentrator stands up a Multipath with ONE shared socket ("a") and TWO bound
// peers keyed by DISTINCT psks: the primary (pskA) and a second peer (pskB) wired in via
// the bindSecondPeer helper (standing in for the concentrator's per-peer wiring). It
// returns the bind plus each peer, whose per-(peer,path) view of the shared socket is what
// the T88 source->peer demux routes between. m.paths[0] is the primary's view — the value
// the Bind-owned readLoop hands demuxInbound for this socket.
func twoPeerConcentrator(t *testing.T, pskA, pskB config.Key) (m *Multipath, primary, second *peerState) {
	t.Helper()
	clk := newFakeClock()
	m, _, _ = newProbingMultipath(t, loopbackPaths(1), pskA, clk) // one shared path "a"
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	primary = m.peerState
	second = bindSecondPeer(t, m, "peer-b", pskB, clk)
	if peerPathByName(second, "a") == nil {
		t.Fatalf("second peer has no view of shared path 'a': %v", pathNamesOfPeer(second))
	}
	return m, primary, second
}

// TestConcentratorBindsSourceToPeerViaAuthenticatedProbe is the T88 acceptance: with two
// peers bound to one shared socket under distinct psks, an inbound PROBE from a fresh
// source is trial-decoded against each peer's psk-derived codec and, on the first MAC
// that verifies, the source is bound to THAT peer (source->peer) and its probe reflected.
// Only an authenticated PROBE binds; a forged frame verifies under no psk and binds
// nothing; the trial-decode stops at the first matching psk.
func TestConcentratorBindsSourceToPeerViaAuthenticatedProbe(t *testing.T) {
	pskA := testKey(t, 0x11) // primary
	pskB := testKey(t, 0x22) // second peer

	t.Run("authenticated PROBE under peer B's psk binds the source to B and reflects an echo", func(t *testing.T) {
		m, primary, second := twoPeerConcentrator(t, pskA, pskB)
		secondView := peerPathByName(second, "a")

		// A raw loopback socket stands in for peer B's remote uplink: it both is the probe
		// SOURCE and receives the reflected echo. Its address is a fresh, unbound source.
		peer, peerAP := rawPeer(t)

		// A DATA frame from this source BEFORE any PROBE has bound it must be dropped — a
		// binding is established ONLY by an authenticated PROBE (D9/D11), never by DATA.
		dataCodecB, _ := frame.NewCodec(pskB)
		preData, err := dataCodecB.Encode(nil, frame.Data{OuterSeq: 5, PathID: secondView.id, Payload: []byte("early")})
		if err != nil {
			t.Fatalf("encode pre-bind DATA: %v", err)
		}
		m.demuxInbound(m.paths[0], preData, peerAP)
		if it, ok := second.resequencer.Load().Pop(); ok {
			t.Fatalf("DATA from an UNBOUND source was delivered up peer B (%q); only a PROBE may bind first", it.Payload)
		}
		if _, ok := m.lookupPeerBySource(peerAP.Addr()); ok {
			t.Fatal("an unauthenticated DATA frame established a source->peer binding (D9/D11 violated)")
		}

		// The authenticated PROBE, encoded under peer B's psk, from the same source.
		const seq = 7
		ts := newFakeClock().Now().UnixNano()
		probeRaw, err := frame.Encode(pskB, frame.Probe{PathID: secondView.id, ProbeSeq: seq, TimestampNanos: ts, IsEcho: false})
		if err != nil {
			t.Fatalf("encode probe under psk B: %v", err)
		}
		m.demuxInbound(m.paths[0], probeRaw, peerAP)

		// The source is now bound to peer B (never the primary).
		bound, ok := m.lookupPeerBySource(peerAP.Addr())
		if !ok {
			t.Fatal("authenticated PROBE did not establish a source->peer binding")
		}
		if bound != second {
			t.Fatalf("source bound to the wrong peer: got %q, want peer B", bound.name)
		}

		// Peer B learned the probe source as its return remote (authenticated learning, D11);
		// the primary's view of the socket learned nothing.
		if remote, ok := secondView.getRemote(); !ok || remote != peerAP {
			t.Fatalf("peer B view remote = %v (ok=%v), want %v learned from the probe", remote, ok, peerAP)
		}
		if _, ok := m.paths[0].getRemote(); ok {
			t.Fatal("the PRIMARY view learned a remote from a probe that authenticated under peer B's psk")
		}

		// The echo landed at the source, encoded under peer B's psk, verbatim ProbeSeq with
		// IsEcho flipped true — the reflection came from peer B's reflector.
		echoCodec, _ := frame.NewCodec(pskB)
		echo := readProbe(t, peer, echoCodec)
		if !echo.IsEcho || echo.ProbeSeq != seq || echo.PathID != secondView.id {
			t.Fatalf("reflected echo = %+v, want IsEcho=true seq=%d path=%d under psk B", echo, seq, secondView.id)
		}

		// With the source now bound, a subsequent DATA from it is demuxed to peer B's
		// resequencer (the binding's purpose) — and NOT the primary's.
		postData, err := dataCodecB.Encode(nil, frame.Data{OuterSeq: 9, PathID: secondView.id, Payload: []byte("late")})
		if err != nil {
			t.Fatalf("encode post-bind DATA: %v", err)
		}
		m.demuxInbound(m.paths[0], postData, peerAP)
		if it, ok := second.resequencer.Load().Pop(); !ok || !bytes.Equal(it.Payload, []byte("late")) {
			t.Fatalf("DATA from the BOUND source was not delivered to peer B: ok=%v payload=%q", ok, it.Payload)
		}
		if it, ok := primary.resequencer.Load().Pop(); ok {
			t.Fatalf("bound peer B's DATA leaked into the PRIMARY resequencer (%q)", it.Payload)
		}
	})

	t.Run("a forged/garbage frame verifies under no peer psk and establishes no binding", func(t *testing.T) {
		m, primary, second := twoPeerConcentrator(t, pskA, pskB)
		secondView := peerPathByName(second, "a")
		peer, peerAP := rawPeer(t)

		// Random bytes: neither psk's MAC can verify them, so no frame kind is ever recovered.
		garbage := make([]byte, 128)
		if _, err := rand.Read(garbage); err != nil {
			t.Fatalf("draw garbage: %v", err)
		}
		m.demuxInbound(m.paths[0], garbage, peerAP)

		if _, ok := m.lookupPeerBySource(peerAP.Addr()); ok {
			t.Fatal("a forged/garbage frame established a source->peer binding")
		}
		if _, ok := secondView.getRemote(); ok {
			t.Fatal("a forged frame taught peer B a remote")
		}
		if _, ok := m.paths[0].getRemote(); ok {
			t.Fatal("a forged frame taught the primary a remote")
		}
		if it, ok := second.resequencer.Load().Pop(); ok {
			t.Fatalf("a forged frame was delivered up peer B (%q)", it.Payload)
		}
		if it, ok := primary.resequencer.Load().Pop(); ok {
			t.Fatalf("a forged frame was delivered up the primary (%q)", it.Payload)
		}
		// No echo was reflected: a Read with an already-expired deadline returns a timeout
		// error rather than a datagram.
		_ = peer.SetReadDeadline(newFakeClock().Now())
		buf := make([]byte, maxDatagram)
		if _, _, rerr := peer.ReadFromUDPAddrPort(buf); rerr == nil {
			t.Fatal("a forged frame was reflected as an echo")
		}
	})

	t.Run("trial-decode stops at the first matching psk", func(t *testing.T) {
		// The primary is index 0 in the per-socket view set, so a PROBE that verifies under the
		// PRIMARY's psk must bind to the primary and the loop must STOP there — peer B (index 1)
		// is never consulted, so it neither reflects nor learns a remote.
		m, primary, second := twoPeerConcentrator(t, pskA, pskB)
		secondView := peerPathByName(second, "a")
		peer, peerAP := rawPeer(t)

		const seq = 3
		ts := newFakeClock().Now().UnixNano()
		probeRaw, err := frame.Encode(pskA, frame.Probe{PathID: m.paths[0].id, ProbeSeq: seq, TimestampNanos: ts, IsEcho: false})
		if err != nil {
			t.Fatalf("encode probe under psk A: %v", err)
		}
		m.demuxInbound(m.paths[0], probeRaw, peerAP)

		bound, ok := m.lookupPeerBySource(peerAP.Addr())
		if !ok || bound != primary {
			t.Fatalf("source bound to %v (ok=%v), want the PRIMARY (the first matching psk)", bound, ok)
		}
		// The primary reflected; the echo decodes under psk A.
		echoCodec, _ := frame.NewCodec(pskA)
		echo := readProbe(t, peer, echoCodec)
		if !echo.IsEcho || echo.ProbeSeq != seq {
			t.Fatalf("reflected echo = %+v, want IsEcho=true seq=%d under psk A", echo, seq)
		}
		// Peer B (tried AFTER the primary) was never consulted: no remote learned on its view.
		if _, ok := secondView.getRemote(); ok {
			t.Fatal("peer B processed a probe already matched by the primary's psk — the trial-decode did not stop at the first match")
		}
	})
}
