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
		if _, ok := m.lookupPeerBySource(peerAP); ok {
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

	t.Run("a non-PROBE decode under one psk does not abort the trial of the remaining psks", func(t *testing.T) {
		// Reviewer criticism (R2 #1): the trial-decode must key its stop on the first PROBE that
		// AUTHENTICATES, not the first successful DECODE. KindData/KindParity carry no MAC and are
		// forgeable by design, so a genuine peer-B PROBE can cross-psk-garble into a DATA/PARITY
		// kind under the PRIMARY's codec (~0.4%). If the loop aborted on that non-PROBE decode
		// (the pre-fix `return`) it would drop peer B's genuine, MAC-verifying PROBE without ever
		// trying peer B's psk. The loop must instead `continue` past a non-PROBE decode.
		//
		// Construct exactly that collision deterministically: encode a genuine PROBE under peer
		// B's psk, redrawing its random nonce until the SAME bytes decode under the PRIMARY's psk
		// as a valid non-PROBE kind (DATA/PARITY). Feeding it to demuxInbound must still bind and
		// dispatch to peer B — proving view[0]'s non-PROBE decode did not abort the trial.
		m, primary, second := twoPeerConcentrator(t, pskA, pskB)
		secondView := peerPathByName(second, "a")
		peer, peerAP := rawPeer(t)

		const seq = 13
		ts := newFakeClock().Now().UnixNano()
		primaryCodec, err := frame.NewCodec(pskA) // decodes identically to the primary's view codec (same psk)
		if err != nil {
			t.Fatalf("build primary codec: %v", err)
		}
		var probeRaw []byte
		// The primary decodes a foreign PROBE as a non-PROBE with probability ~2/256 (kind lands
		// on DATA or PARITY); this budget is ~800x the mean and effectively never exhausts.
		const searchBudget = 100_000
		for i := 0; i < searchBudget; i++ {
			raw, encErr := frame.Encode(pskB, frame.Probe{PathID: secondView.id, ProbeSeq: seq, TimestampNanos: ts, IsEcho: false})
			if encErr != nil {
				t.Fatalf("encode probe under psk B: %v", encErr)
			}
			fr, decErr := primaryCodec.Decode(raw)
			if decErr != nil {
				continue // failed under the primary's psk (ErrAuth/ErrMalformed): not the collision we need
			}
			if _, isProbe := fr.(frame.Probe); isProbe {
				continue // a foreign PROBE can never MAC-verify as a PROBE under psk A; guard anyway
			}
			probeRaw = raw // decodes as DATA/PARITY under psk A, yet is a genuine PROBE under psk B
			break
		}
		if probeRaw == nil {
			t.Fatalf("could not construct a cross-garbling PROBE within %d attempts", searchBudget)
		}

		m.demuxInbound(m.paths[0], probeRaw, peerAP)

		// Peer B's PROBE authenticated on the SECOND trial (after the primary's non-PROBE decode
		// did NOT abort the loop): the source bound to peer B.
		bound, ok := m.lookupPeerBySource(peerAP)
		if !ok {
			t.Fatal("a genuine peer-B PROBE that decoded as a non-PROBE under the primary's psk was dropped — the trial-decode aborted at the first successful DECODE instead of the first MAC")
		}
		if bound != second {
			t.Fatalf("source bound to the wrong peer: got %q, want peer B", bound.name)
		}
		// Peer B learned the source as its remote and reflected the echo under psk B.
		if remote, ok := secondView.getRemote(); !ok || remote != peerAP {
			t.Fatalf("peer B view remote = %v (ok=%v), want %v learned from the probe", remote, ok, peerAP)
		}
		echoCodec, _ := frame.NewCodec(pskB)
		echo := readProbe(t, peer, echoCodec)
		if !echo.IsEcho || echo.ProbeSeq != seq || echo.PathID != secondView.id {
			t.Fatalf("reflected echo = %+v, want IsEcho=true seq=%d path=%d under psk B", echo, seq, secondView.id)
		}
		// The primary's non-PROBE decode was NOT dispatched: its resequencer stays empty and it
		// learned no remote (a non-PROBE carries no binding authority and is dropped).
		if _, ok := m.paths[0].getRemote(); ok {
			t.Fatal("the primary learned a remote from a frame that only cross-garbled into a non-PROBE kind under its psk")
		}
		if it, ok := primary.resequencer.Load().Pop(); ok {
			t.Fatalf("a non-PROBE cross-garble was delivered up the primary (%q)", it.Payload)
		}
	})
}
