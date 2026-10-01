package bind

import (
	"bytes"
	"testing"

	"github.com/7mind/wanbond/internal/frame"
)

// TestSharedSocketGatesUnboundData is the T89 acceptance: on a SHARED (multi-view)
// concentrator socket, transport data arriving from a source with no established
// source->peer binding must reach NO peer — dropped outright — EVEN when the frame
// authenticates under some peer's codec and that peer's transport holds a lane to the
// source. A data frame carries no binding authority (D9/D11: only an authenticated PROBE
// binds a source), so a decode under any codec must never attribute the frame to that
// peer. After an authenticated PROBE binds the source to peer B, subsequent data from it
// routes to B's plane only.
//
// This hardens the T88 multi-peer gate: T88 already `continue`s past a non-PROBE trial-decode
// so unbound data falls through to the loop-end drop; this test pins that property down for
// BOTH datagram classes and asserts NEITHER peer is touched (not merely peer B).
func TestSharedSocketGatesUnboundData(t *testing.T) {
	pskA := testKey(t, 0x11) // primary
	pskB := testKey(t, 0x22) // second peer

	t.Run("ordered data that authenticates under peer B's psk from an unbound source reaches no resequencer", func(t *testing.T) {
		m, primary, second := twoPeerConcentrator(t, pskA, pskB)
		secondView := peerPathByName(second, "a")
		_, peerAP := rawPeer(t) // a fresh, unbound source address

		// Peer B's transport holds a lane to the source, so only the demux gate stands
		// between this frame and peer B's resequencer.
		remote := newRemoteTransport(t, second, 987)
		remote.join(secondView, peerAP)
		m.demuxInbound(m.paths[0], remote.wire(remote.bulk(secondView, []byte("unbound-data"))), peerAP)

		// Dropped: neither peer's resequencer received it, and no binding was minted.
		if it, ok := second.resequencer.Load().Pop(); ok {
			t.Fatalf("data from an UNBOUND source landed in peer B's resequencer (%q); a valid decode must not attribute it", it.Payload)
		}
		if it, ok := primary.resequencer.Load().Pop(); ok {
			t.Fatalf("data from an UNBOUND source landed in the PRIMARY resequencer (%q)", it.Payload)
		}
		if _, ok := m.lookupPeerBySource(peerAP); ok {
			t.Fatal("an unbound data frame established a source->peer binding (D9/D11 violated)")
		}
	})

	t.Run("a small datagram that authenticates under peer B's psk from an unbound source is delivered to no peer", func(t *testing.T) {
		m, primary, second := twoPeerConcentrator(t, pskA, pskB)
		secondView := peerPathByName(second, "a")
		_, peerAP := rawPeer(t)

		// A small datagram bypasses the resequencer: dispatched into either peer's
		// transport it would be handed to the engine at once, which is what makes a leak
		// past the gate observable here.
		remote := newRemoteTransport(t, second, 987)
		remote.join(secondView, peerAP)
		m.demuxInbound(m.paths[0], remote.wire(remote.small(secondView, []byte("unbound-small"))), peerAP)

		for _, pc := range []struct {
			who string
			p   *peerState
		}{{"peer B", second}, {"the PRIMARY", primary}} {
			if it, ok := pc.p.adaptive.Load().popInteractive(); ok {
				t.Fatalf("a small datagram from an UNBOUND source was delivered up %s (%q); a valid decode carries no binding authority and must not be dispatched", pc.who, it.Payload)
			}
			if it, ok := pc.p.resequencer.Load().Pop(); ok {
				t.Fatalf("a small datagram from an UNBOUND source landed in %s's resequencer (%q)", pc.who, it.Payload)
			}
		}
		if _, ok := m.lookupPeerBySource(peerAP); ok {
			t.Fatal("an unbound small datagram established a source->peer binding (D9/D11 violated)")
		}
	})

	t.Run("after an authenticated PROBE binds the source to B, subsequent data lands in B's resequencer only", func(t *testing.T) {
		m, primary, second := twoPeerConcentrator(t, pskA, pskB)
		secondView := peerPathByName(second, "a")
		peer, peerAP := rawPeer(t)
		remote := newRemoteTransport(t, second, 987)
		remote.join(secondView, peerAP)

		// Pre-binding data under B's psk from this source is dropped (the gate above).
		unbound := remote.clone()
		m.demuxInbound(m.paths[0], unbound.wire(unbound.bulk(secondView, []byte("early"))), peerAP)
		if _, ok := m.lookupPeerBySource(peerAP); ok {
			t.Fatal("pre-bind data established a binding")
		}

		// An authenticated PROBE under peer B's psk binds the source to peer B.
		const seq = 7
		ts := newFakeClock().Now().UnixNano()
		probeRaw, err := frame.Encode(pskB, frame.Probe{PathID: secondView.id, ProbeSeq: seq, TimestampNanos: ts, IsEcho: false})
		if err != nil {
			t.Fatalf("encode probe under psk B: %v", err)
		}
		m.demuxInbound(m.paths[0], probeRaw, peerAP)

		bound, ok := m.lookupPeerBySource(peerAP)
		if !ok || bound != second {
			t.Fatalf("PROBE did not bind the source to peer B: bound=%v ok=%v", bound, ok)
		}
		// Drain peer B's reflected echo so a later timeout-read in the suite is unambiguous;
		// its content is asserted by the T88 acceptance, so we only consume it here.
		echoCodec, _ := frame.NewCodec(pskB)
		_ = readProbe(t, peer, echoCodec)

		// Subsequent data from the now-bound source lands in peer B's resequencer, and ONLY B's.
		m.demuxInbound(m.paths[0], remote.wire(remote.bulk(secondView, []byte("late"))), peerAP)
		if it, ok := second.resequencer.Load().Pop(); !ok || !bytes.Equal(it.Payload, []byte("late")) {
			t.Fatalf("post-bind data did not reach peer B's resequencer: ok=%v payload=%q", ok, it.Payload)
		}
		if it, ok := primary.resequencer.Load().Pop(); ok {
			t.Fatalf("post-bind data for peer B leaked into the PRIMARY resequencer (%q)", it.Payload)
		}
		// The pre-bind "early" frame was never buffered anywhere: peer B's resequencer held
		// only "late" (already popped) and the primary's held nothing.
		if it, ok := second.resequencer.Load().Pop(); ok {
			t.Fatalf("peer B's resequencer held a second frame (%q); the pre-bind data was not dropped", it.Payload)
		}
	})
}
