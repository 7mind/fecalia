package bind

import (
	"bytes"
	"testing"

	"github.com/7mind/wanbond/internal/frame"
)

// TestSharedSocketGatesUnboundDataParity is the T89 acceptance: on a SHARED (multi-view)
// concentrator socket, DATA and PARITY arriving from a source with no established
// source->peer binding must reach NO peer's resequencer — dropped outright — EVEN when the
// frame validly decodes under some peer's codec (a real peer's psk, so the MAC-less
// DATA/PARITY body deciphers cleanly under that psk). A DATA/PARITY frame carries no binding
// authority (D9/D11: only an authenticated PROBE binds a source), so a garble-decode under
// any codec must never attribute the frame to that peer. After an authenticated PROBE binds
// the source to peer B, subsequent DATA/PARITY from it route to B's plane only.
//
// This hardens the T88 multi-peer gate: T88 already `continue`s past a non-PROBE trial-decode
// so an unbound DATA/PARITY falls through to the loop-end drop; this test pins that property
// down for BOTH kinds and asserts NEITHER peer's resequencer is touched (not merely peer B's).
func TestSharedSocketGatesUnboundDataParity(t *testing.T) {
	pskA := testKey(t, 0x11) // primary
	pskB := testKey(t, 0x22) // second peer

	// codecB deciphers exactly as peer B's per-path view codec does (same psk), so a frame it
	// encodes is guaranteed to VALIDLY decode under peer B's view during the demux trial — the
	// discriminating case the acceptance calls out ("encode it under a real peer's psk").
	codecB, err := frame.NewCodec(pskB)
	if err != nil {
		t.Fatalf("build peer B codec: %v", err)
	}

	t.Run("DATA that validly decodes under peer B's psk from an unbound source reaches no resequencer", func(t *testing.T) {
		m, primary, second := twoPeerConcentrator(t, pskA, pskB)
		secondView := peerPathByName(second, "a")
		_, peerAP := rawPeer(t) // a fresh, unbound source address

		data, err := codecB.Encode(nil, frame.Data{OuterSeq: 5, PathID: secondView.id, Payload: []byte("unbound-data")})
		if err != nil {
			t.Fatalf("encode DATA under psk B: %v", err)
		}
		m.demuxInbound(m.paths[0], data, peerAP)

		// Dropped: neither peer's resequencer received it, and no binding was minted.
		if it, ok := second.resequencer.Load().Pop(); ok {
			t.Fatalf("DATA from an UNBOUND source landed in peer B's resequencer (%q); a valid decode must not attribute it", it.Payload)
		}
		if it, ok := primary.resequencer.Load().Pop(); ok {
			t.Fatalf("DATA from an UNBOUND source landed in the PRIMARY resequencer (%q)", it.Payload)
		}
		if _, ok := m.lookupPeerBySource(peerAP.Addr()); ok {
			t.Fatal("an unbound DATA frame established a source->peer binding (D9/D11 violated)")
		}
	})

	t.Run("PARITY that validly decodes under peer B's psk from an unbound source reaches no resequencer", func(t *testing.T) {
		m, primary, second := twoPeerConcentrator(t, pskA, pskB)
		secondView := peerPathByName(second, "a")
		_, peerAP := rawPeer(t)

		parity, err := codecB.Encode(nil, frame.Parity{FECGroup: 1, ParityIndex: 0, DataCount: 1, PathID: secondView.id, Payload: []byte("unbound-parity")})
		if err != nil {
			t.Fatalf("encode PARITY under psk B: %v", err)
		}
		m.demuxInbound(m.paths[0], parity, peerAP)

		if it, ok := second.resequencer.Load().Pop(); ok {
			t.Fatalf("PARITY from an UNBOUND source landed in peer B's resequencer (%q)", it.Payload)
		}
		if it, ok := primary.resequencer.Load().Pop(); ok {
			t.Fatalf("PARITY from an UNBOUND source landed in the PRIMARY resequencer (%q)", it.Payload)
		}
		if _, ok := m.lookupPeerBySource(peerAP.Addr()); ok {
			t.Fatal("an unbound PARITY frame established a source->peer binding (D9/D11 violated)")
		}
	})

	t.Run("after an authenticated PROBE binds the source to B, subsequent DATA lands in B's resequencer only", func(t *testing.T) {
		m, primary, second := twoPeerConcentrator(t, pskA, pskB)
		secondView := peerPathByName(second, "a")
		peer, peerAP := rawPeer(t)

		// Pre-binding DATA under B's psk from this source is dropped (the gate above).
		early, err := codecB.Encode(nil, frame.Data{OuterSeq: 4, PathID: secondView.id, Payload: []byte("early")})
		if err != nil {
			t.Fatalf("encode pre-bind DATA: %v", err)
		}
		m.demuxInbound(m.paths[0], early, peerAP)
		if _, ok := m.lookupPeerBySource(peerAP.Addr()); ok {
			t.Fatal("pre-bind DATA established a binding")
		}

		// An authenticated PROBE under peer B's psk binds the source to peer B.
		const seq = 7
		ts := newFakeClock().Now().UnixNano()
		probeRaw, err := frame.Encode(pskB, frame.Probe{PathID: secondView.id, ProbeSeq: seq, TimestampNanos: ts, IsEcho: false})
		if err != nil {
			t.Fatalf("encode probe under psk B: %v", err)
		}
		m.demuxInbound(m.paths[0], probeRaw, peerAP)

		bound, ok := m.lookupPeerBySource(peerAP.Addr())
		if !ok || bound != second {
			t.Fatalf("PROBE did not bind the source to peer B: bound=%v ok=%v", bound, ok)
		}
		// Drain peer B's reflected echo so a later timeout-read in the suite is unambiguous;
		// its content is asserted by the T88 acceptance, so we only consume it here.
		echoCodec, _ := frame.NewCodec(pskB)
		_ = readProbe(t, peer, echoCodec)

		// Subsequent DATA from the now-bound source lands in peer B's resequencer, and ONLY B's.
		late, err := codecB.Encode(nil, frame.Data{OuterSeq: 9, PathID: secondView.id, Payload: []byte("late")})
		if err != nil {
			t.Fatalf("encode post-bind DATA: %v", err)
		}
		m.demuxInbound(m.paths[0], late, peerAP)
		if it, ok := second.resequencer.Load().Pop(); !ok || !bytes.Equal(it.Payload, []byte("late")) {
			t.Fatalf("post-bind DATA did not reach peer B's resequencer: ok=%v payload=%q", ok, it.Payload)
		}
		if it, ok := primary.resequencer.Load().Pop(); ok {
			t.Fatalf("post-bind DATA for peer B leaked into the PRIMARY resequencer (%q)", it.Payload)
		}
		// The pre-bind "early" frame was never buffered anywhere: peer B's resequencer held
		// only "late" (already popped) and the primary's held nothing.
		if it, ok := second.resequencer.Load().Pop(); ok {
			t.Fatalf("peer B's resequencer held a second frame (%q); the pre-bind DATA was not dropped", it.Payload)
		}
	})
}
