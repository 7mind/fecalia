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
	m, _ = newProbingMultipath(t, loopbackPaths(1), pskA, clk) // one shared path "a"
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

		// A transport data frame from this source BEFORE any PROBE has bound it must be
		// dropped — a binding is established ONLY by an authenticated PROBE (D9/D11), never
		// by a data frame, even one peer B's transport would accept on that lane.
		remote := newRemoteTransport(t, second, 987)
		remote.join(secondView, peerAP)
		unbound := remote.clone()
		m.demuxInbound(m.paths[0], unbound.wire(unbound.bulk(secondView, []byte("early"))), peerAP)
		if it, ok := second.resequencer.Load().Pop(); ok {
			t.Fatalf("data from an UNBOUND source was delivered up peer B (%q); only a PROBE may bind first", it.Payload)
		}
		if _, ok := m.lookupPeerBySource(peerAP); ok {
			t.Fatal("a transport data frame established a source->peer binding (D9/D11 violated)")
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
		bound, ok := m.lookupPeerBySource(peerAP)
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

		// With the source now bound, a subsequent data frame from it is demuxed to peer B's
		// resequencer (the binding's purpose) — and NOT the primary's.
		m.demuxInbound(m.paths[0], remote.wire(remote.bulk(secondView, []byte("late"))), peerAP)
		if it, ok := second.resequencer.Load().Pop(); !ok || !bytes.Equal(it.Payload, []byte("late")) {
			t.Fatalf("data from the BOUND source was not delivered to peer B: ok=%v payload=%q", ok, it.Payload)
		}
		if it, ok := primary.resequencer.Load().Pop(); ok {
			t.Fatalf("bound peer B's data leaked into the PRIMARY resequencer (%q)", it.Payload)
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

		if _, ok := m.lookupPeerBySource(peerAP); ok {
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

	t.Run("trial-decode STOPS at the first matching psk (discriminating: both peers share a psk)", func(t *testing.T) {
		// Discriminating design: bind BOTH peers under the SAME psk. A single PROBE then
		// authenticates under BOTH views' codecs, so the two loop policies diverge observably:
		//   - stop-at-first-match (correct): only view[0] (the primary) dispatches — exactly ONE
		//     echo is reflected, the source binds to the primary (the first view), and peer B
		//     (view[1]) is never consulted, so it learns no remote.
		//   - a non-stopping loop (the defect this guards against): BOTH views dispatch — TWO
		//     echoes are reflected and peer B ALSO learns the source as a remote.
		// Under distinct psks a single PROBE matches only one view, so the two policies would be
		// indistinguishable; the shared psk is what makes the assertion discriminating.
		const sharedPsk = 0x33
		psk := testKey(t, sharedPsk)
		m, primary, second := twoPeerConcentrator(t, psk, psk)
		secondView := peerPathByName(second, "a")
		peer, peerAP := rawPeer(t)

		const seq = 3
		ts := newFakeClock().Now().UnixNano()
		probeRaw, err := frame.Encode(psk, frame.Probe{PathID: m.paths[0].id, ProbeSeq: seq, TimestampNanos: ts, IsEcho: false})
		if err != nil {
			t.Fatalf("encode probe under the shared psk: %v", err)
		}
		m.demuxInbound(m.paths[0], probeRaw, peerAP)

		// The source bound to the primary — the FIRST view whose codec yielded a PROBE.
		bound, ok := m.lookupPeerBySource(peerAP)
		if !ok || bound != primary {
			t.Fatalf("source bound to %v (ok=%v), want the PRIMARY (the first matching psk)", bound, ok)
		}
		// Peer B (tried AFTER the primary) was never consulted: no remote learned on its view.
		// Under a non-stopping loop this getRemote would succeed (B dispatched a second time).
		if _, ok := secondView.getRemote(); ok {
			t.Fatal("peer B processed a probe already matched by the primary's psk — the trial-decode did not STOP at the first match")
		}
		// EXACTLY ONE echo was reflected (by the primary). Read the first — it must decode under
		// the shared psk with the verbatim ProbeSeq — then assert a SECOND read finds nothing: a
		// non-stopping loop would have reflected a second echo from peer B.
		echoCodec, _ := frame.NewCodec(psk)
		echo := readProbe(t, peer, echoCodec)
		if !echo.IsEcho || echo.ProbeSeq != seq {
			t.Fatalf("reflected echo = %+v, want IsEcho=true seq=%d", echo, seq)
		}
		_ = peer.SetReadDeadline(newFakeClock().Now())
		buf := make([]byte, maxDatagram)
		if _, _, rerr := peer.ReadFromUDPAddrPort(buf); rerr == nil {
			t.Fatal("a SECOND echo was reflected — the trial-decode did not STOP after the primary matched (both views dispatched)")
		}
	})
}
