package bind

import (
	"bytes"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/fec"
	"github.com/7mind/wanbond/internal/frame"
	"github.com/7mind/wanbond/internal/telemetry"
)

// fecReceiveConfig is the fixed-ratio group the strengthened PARITY subtest arms both
// peers' receivers with. K=1 parity is all that is needed: a DataCount=1 group (one data
// shard) reconstructs that sole data frame from the single parity shard ALONE, so a PARITY
// that leaks past the demux gate into dispatchInbound recovers a data frame straight into
// the dispatching peer's resequencer — which is what makes the leak observable.
var fecReceiveConfig = fec.Config{DataShards: 4, ParityShards: 1, Deadline: time.Millisecond}

// enableFECReceive arms a peer's receive-side FEC through the SAME seam Open uses
// (openPeerDatapathLocked, multipath.go:851) to Store a fecReceiver. Without it the Parity
// case of dispatchInbound is a no-op (fecRecv nil), so a leaked PARITY would touch no
// resequencer and the drop property this subtest names would be unobservable.
func enableFECReceive(t testing.TB, p *peerState, cfg fec.Config) {
	t.Helper()
	dec, err := fec.NewDecoder(cfg)
	if err != nil {
		t.Fatalf("build FEC decoder: %v", err)
	}
	p.fecRecv.Store(&fecReceiver{dec: dec, connLoss: telemetry.NewConnLoss(fecResidualLossWindow)})
}

// singleFrameParity builds a GENUINE parity shard for a one-data-frame group whose sole
// data shard's coded bytes are seq||inner (fecShardPayload) — the exact shape the datapath
// codes parity over. Offered to a decoder built at the same cfg, this parity ALONE
// reconstructs that data frame, so a PARITY carrying it would deliver seq||inner up a
// resequencer at outer-seq `seq`. It returns the wire fields for a frame.Parity.
func singleFrameParity(t testing.TB, cfg fec.Config, seq uint64, inner []byte) (group uint32, index uint16, dataCount uint8, payload []byte) {
	t.Helper()
	enc, err := fec.NewEncoder(cfg, fec.SystemClock{})
	if err != nil {
		t.Fatalf("build FEC encoder: %v", err)
	}
	if _, _, aerr := enc.Admit(fecShardPayload(seq, inner)); aerr != nil {
		t.Fatalf("admit single data shard: %v", aerr)
	}
	parity, err := enc.Flush() // close the 1-data group, coding K parity over its sole shard
	if err != nil {
		t.Fatalf("flush single-frame group: %v", err)
	}
	if len(parity) != cfg.ParityShards {
		t.Fatalf("single-frame group emitted %d parity, want %d", len(parity), cfg.ParityShards)
	}
	pshard := parity[0]
	if pshard.DataCount != 1 {
		t.Fatalf("single-frame parity DataCount = %d, want 1", pshard.DataCount)
	}
	return uint32(pshard.Group), uint16(pshard.Index), uint8(pshard.DataCount), pshard.Payload
}

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

		// Arm BOTH peers' receive-side FEC exactly as Open does (multipath.go:851). This is
		// what makes the drop OBSERVABLE: with FEC receive off (the prior fixture), a PARITY
		// leaked past the gate falls into dispatchInbound's fecRecv-nil no-op and touches no
		// resequencer, so the subtest could not distinguish a gate that drops from one that
		// dispatches. With FEC receive on, the DataCount=1 parity below reconstructs its sole
		// data frame straight into the DISPATCHING peer's resequencer, so a leak is caught by
		// both the decoder-recovery and the resequencer assertions (mutation-verified).
		enableFECReceive(t, primary, fecReceiveConfig)
		enableFECReceive(t, second, fecReceiveConfig)

		// A GENUINE single-frame-group parity: its one data shard's coded bytes are
		// leakSeq||leakInner, so if this PARITY were dispatched into either peer's decoder it
		// alone would recover that data frame and resequence leakInner at outer-seq leakSeq.
		const leakSeq = 42
		leakInner := []byte("parity-leak-recovers-this")
		group, index, dataCount, parityPayload := singleFrameParity(t, fecReceiveConfig, leakSeq, leakInner)

		parity, err := codecB.Encode(nil, frame.Parity{FECGroup: group, ParityIndex: index, DataCount: dataCount, PathID: secondView.id, Payload: parityPayload})
		if err != nil {
			t.Fatalf("encode PARITY under psk B: %v", err)
		}
		m.demuxInbound(m.paths[0], parity, peerAP)

		// The gate dropped the unbound PARITY BEFORE dispatch: it reached NEITHER peer's FEC
		// decoder (no reconstruction) NOR any resequencer. A dispatch-without-bind leak would
		// trip whichever peer the leaked frame was dispatched to, so both are asserted.
		for _, pc := range []struct {
			who string
			p   *peerState
		}{{"peer B", second}, {"the PRIMARY", primary}} {
			fr := pc.p.fecRecv.Load()
			if got := fr.stats().Recovered; got != 0 {
				t.Fatalf("unbound PARITY reached %s's FEC decoder and reconstructed %d frame(s); a valid decode carries no binding authority and must not be dispatched", pc.who, got)
			}
			if got := fr.deliveredRecovered.Load(); got != 0 {
				t.Fatalf("unbound PARITY delivered %d reconstructed frame(s) up %s's resequencer", got, pc.who)
			}
			if it, ok := pc.p.resequencer.Load().Pop(); ok {
				t.Fatalf("PARITY from an UNBOUND source landed in %s's resequencer (%q)", pc.who, it.Payload)
			}
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
