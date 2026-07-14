package bind

import (
	"bytes"
	"crypto/rand"
	"net/netip"
	"testing"

	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/fec"
	"github.com/7mind/wanbond/internal/frame"
	"github.com/7mind/wanbond/internal/telemetry"
)

// This file codifies Q27 — the cross-peer isolation threat model — as concrete unit-level
// adversary cases against the T83-T91 multi-peer demux machinery. The claim it makes concrete:
//
//   - A party knowing ONLY peer B's psk can bind/disturb ONLY peer B. It can NEVER move peer A's
//     source->peer binding, inject into A's resequencer, or evict A. The isolation rests on two
//     production guards plus the frame codec:
//       (1) demuxInbound routes an ALREADY-BOUND source to its owner peer's view ONLY (the
//           lookupPeerBySource early-return) — a bound source is never re-trial-decoded, so a
//           foreign-psk PROBE from it can never re-point the binding.
//       (2) The trial-decode loop binds a NEW source ONLY on an authenticated PROBE (the
//           `if !isProbe continue` D9/D11 gate) — forged DATA/PARITY floods bind nothing.
//     The codec supplies the third leg: PROBE/CONTROL carry an HMAC tag (forging A's tag without
//     A's psk is infeasible), and DATA/PARITY are psk-keyed-obfuscated, so a frame built under B's
//     psk decodes under A's psk only as UNCONTROLLED garbage (~2/256) — never attacker-CHOSEN
//     content. Every assertion below is therefore either fully deterministic or immune to that
//     garble by keying on a chosen sentinel payload the wrong-key decode can never reproduce.
//
//   - A party with NO valid psk is limited to bounded bootstrap degradation: its floods bind
//     nothing (no PROBE authenticates), corrupt no peer's stream, and evict no live peer.

// victimPeerFEC stands up a FEC-configured concentrator whose PRIMARY (peer A, pskA) is the
// isolation VICTIM: a source is bound to it via an authenticated PROBE and its path driven Up
// (a LIVE peer that must never be disturbed or evicted). Peer B (pskB) is bound LAZILY (no heavy
// state) and stands in for the peer whose psk the adversary holds. FEC is on so the victim carries
// an armed decoder whose non-disturbance the FEC-garbage cases can assert. It returns the bind,
// both peers, the fake clock, and the victim's bound source address.
func victimPeerFEC(t *testing.T, pskA, pskB config.Key) (m *Multipath, victim, peerB *peerState, clk *fakeClock, victimSrc netip.AddrPort) {
	t.Helper()
	fecCfg := &fec.Config{DataShards: 4, ParityShards: 1, Deadline: testFECDeadline}
	m, victim, peerB, clk = lazyConcentratorFEC(t, pskA, pskB, fecCfg)

	src := synthSource(0)
	// Bind the victim source to peer A through an authenticated PROBE, then drive its path Up so
	// A is a genuinely LIVE, bound peer for the duration of every attack below.
	m.demuxInbound(m.paths[0], authProbe(t, pskA, m.paths[0].id, 1, clk), src)
	bound, ok := m.lookupPeerBySource(src)
	if !ok || bound != victim {
		t.Fatalf("victim source did not bind to peer A: bound=%v ok=%v", bound, ok)
	}
	driveConcentratorPathUp(t, m.paths[0], pskA, clk)
	if m.paths[0].prober.State() != telemetry.StateUp {
		t.Fatalf("victim path not Up after drive: %v", m.paths[0].prober.State())
	}
	return m, victim, peerB, clk, src
}

// TestForeignProbeCannotMoveVictimBinding is the centerpiece: a source bound to peer A cannot be
// re-pointed, unbound, or otherwise disturbed by frames an adversary crafts under a DIFFERENT psk
// (peer B's, or a psk no peer holds). It mutation-discriminates the demuxInbound bound-source
// early-return: with that guard removed, the foreign-psk PROBE subtest re-trial-decodes the bound
// source and re-points it to peer B, turning the "still bound to A" assertion red.
func TestForeignProbeCannotMoveVictimBinding(t *testing.T) {
	pskA := testKey(t, 0x11) // victim (primary)
	pskB := testKey(t, 0x22) // the psk the adversary holds
	pskC := testKey(t, 0x33) // a psk NO peer holds (the no-valid-psk adversary)

	t.Run("an authenticated PROBE under peer B's psk from A's bound source does NOT re-point the binding", func(t *testing.T) {
		m, victim, peerB, clk, src := victimPeerFEC(t, pskA, pskB)
		secondView := peerPathByName(peerB, "a")

		// The adversary holds pskB and forges a valid, authenticated PROBE from the victim's own
		// bound source address. It authenticates under B's psk — but the source is already bound to
		// A, so demuxInbound routes it to A's view, where A's codec cannot decode a B-authenticated
		// frame. It must NOT reach the trial-decode loop that would re-bind it to B.
		m.demuxInbound(m.paths[0], authProbe(t, pskB, secondView.id, 2, clk), src)

		// The binding is unmoved: still A, never re-pointed to B (the T90 roam path must never fire
		// for a foreign-psk frame). With the early-return removed this reads peer B.
		bound, ok := m.lookupPeerBySource(src)
		if !ok || bound != victim {
			t.Fatalf("foreign-psk PROBE re-pointed A's bound source: bound=%v ok=%v (want peer A)", bound, ok)
		}
		// Peer B was never consulted: it learned no remote on its view and its lazy heavy state was
		// never instantiated (a re-point would have called ensurePeerReceiveInstantiated on B).
		if _, ok := secondView.getRemote(); ok {
			t.Fatal("peer B learned a remote from a PROBE aimed at A's bound source")
		}
		if peerB.resequencer.Load() != nil {
			t.Fatal("peer B's heavy receive state was instantiated by a foreign PROBE (it was re-bound)")
		}
		if peerB.fecRecv.Load() != nil {
			t.Fatal("peer B's FEC decoder was instantiated by a foreign PROBE")
		}
		// The victim was untouched: still Up (not evicted).
		if m.paths[0].prober.State() != telemetry.StateUp {
			t.Fatalf("victim liveness disturbed by a foreign PROBE: %v", m.paths[0].prober.State())
		}
	})

	t.Run("a PROBE under a psk NO peer holds neither re-binds nor unbinds A's bound source", func(t *testing.T) {
		m, victim, _, clk, src := victimPeerFEC(t, pskA, pskB)

		// The no-valid-psk adversary forges a PROBE under pskC. Routed to A's view, it fails A's MAC;
		// it must be a per-frame drop that leaves the binding EXACTLY as it was — neither re-pointed
		// (no peer holds pskC) NOR unbound (a decode failure must not evict a live binding).
		m.demuxInbound(m.paths[0], authProbe(t, pskC, m.paths[0].id, 2, clk), src)

		bound, ok := m.lookupPeerBySource(src)
		if !ok {
			t.Fatal("a wrong-psk PROBE UNBOUND A's live source (a decode failure must never evict a binding)")
		}
		if bound != victim {
			t.Fatalf("a wrong-psk PROBE re-pointed A's source to %v", bound)
		}
		if m.paths[0].prober.State() != telemetry.StateUp {
			t.Fatalf("victim liveness disturbed by a wrong-psk PROBE: %v", m.paths[0].prober.State())
		}
	})

	t.Run("replayed and byte-mutated genuine PROBEs from the bound source do not disturb the binding", func(t *testing.T) {
		m, victim, _, clk, src := victimPeerFEC(t, pskA, pskB)

		// A REPLAY of a genuine, authenticated pskA PROBE from the bound source: it decodes cleanly
		// under A's psk and is reflected, but carries no fresh binding authority — the source is
		// already bound to A, so the binding is a no-op and stays put.
		genuine := authProbe(t, pskA, m.paths[0].id, 2, clk)
		m.demuxInbound(m.paths[0], genuine, src)
		if bound, ok := m.lookupPeerBySource(src); !ok || bound != victim {
			t.Fatalf("a replayed genuine PROBE disturbed A's binding: bound=%v ok=%v", bound, ok)
		}

		// A byte-MUTATED genuine PROBE: flipping a body byte breaks A's MAC (or garbles the kind),
		// so A's codec rejects it — a per-frame drop that leaves the binding untouched.
		mutated := append([]byte(nil), genuine...)
		mutated[len(mutated)-1] ^= 0xFF // corrupt the authentication tag
		m.demuxInbound(m.paths[0], mutated, src)
		if bound, ok := m.lookupPeerBySource(src); !ok || bound != victim {
			t.Fatalf("a mutated PROBE disturbed A's binding: bound=%v ok=%v", bound, ok)
		}
		if m.paths[0].prober.State() != telemetry.StateUp {
			t.Fatalf("victim liveness disturbed by replay/mutation: %v", m.paths[0].prober.State())
		}
	})

	t.Run("a forged DATA storm under B's psk from A's bound source injects no chosen content and moves no release point", func(t *testing.T) {
		m, victim, peerB, _, src := victimPeerFEC(t, pskA, pskB)
		secondView := peerPathByName(peerB, "a")
		codecA, err := frame.NewCodec(pskA)
		if err != nil {
			t.Fatalf("build peer A codec: %v", err)
		}
		codecB, err := frame.NewCodec(pskB)
		if err != nil {
			t.Fatalf("build peer B codec: %v", err)
		}

		// Establish A's release point at 100 (deliver one legit frame, advancing `next` to 101).
		m.demuxInbound(m.paths[0], mustEncodeData(t, codecA, 100, m.paths[0].id, "v100"), src)
		if it, ok := victim.resequencer.Load().Pop(); !ok || string(it.Payload) != "v100" {
			t.Fatalf("victim did not deliver its legit frame: ok=%v payload=%q", ok, it.Payload)
		}

		// The adversary (holding pskB) floods DATA authenticated under B's psk from the victim's own
		// bound source, carrying a CHOSEN sentinel payload and wildly discontinuous outer-seqs (the
		// outer-seq-discontinuity storm). Routed to A's view, each frame fails A's psk-keyed decode
		// and is dropped; the ~2/256 that garble-decode carry UNCONTROLLED bytes, never the sentinel.
		const sentinel = "FORGED-BY-B"
		wildSeqs := []uint64{1, 1 << 20, 1 << 40, 1 << 62, 0xFFFFFFFFFFFFFFFF, 202, 5000}
		const floodPerSeq = 40
		for _, seq := range wildSeqs {
			for i := 0; i < floodPerSeq; i++ {
				m.demuxInbound(m.paths[0], mustEncodeData(t, codecB, seq, secondView.id, sentinel), src)
			}
		}

		// The binding and liveness are intact (no eviction), and peer B — whose psk the adversary
		// holds — got NO binding to A's source (the storm cannot bleed A's source into B).
		if bound, ok := m.lookupPeerBySource(src); !ok || bound != victim {
			t.Fatalf("the forged storm disturbed A's binding: bound=%v ok=%v", bound, ok)
		}
		if m.paths[0].prober.State() != telemetry.StateUp {
			t.Fatalf("the forged storm evicted/disturbed the live victim: %v", m.paths[0].prober.State())
		}

		// The stream still flows AND its release point never moved: a legit pskA frame at the very
		// NEXT outer-seq (101) delivers immediately. A storm that had dragged A's release point
		// forward (to one of the wild high seqs) would reject 101 as late and deliver nothing here.
		m.demuxInbound(m.paths[0], mustEncodeData(t, codecA, 101, m.paths[0].id, "v101"), src)
		var delivered []string
		for {
			it, ok := victim.resequencer.Load().Pop()
			if !ok {
				break
			}
			delivered = append(delivered, string(it.Payload))
		}
		sawNext := false
		for _, p := range delivered {
			if p == sentinel {
				t.Fatalf("the adversary's CHOSEN payload %q was delivered up the victim's stream", sentinel)
			}
			if p == "v101" {
				sawNext = true
			}
		}
		if !sawNext {
			t.Fatalf("the victim's release point was moved by the storm: legit seq 101 was not delivered (got %v)", delivered)
		}
	})
}

// TestUnauthenticatedFloodBindsNothingAndInjectsNothing is the no-valid-psk / wrong-psk flood
// case: a flood of DATA/PARITY (forged under peer B's psk) and pure garbage from MANY distinct,
// unbound spoofed sources binds nothing (no PROBE authenticates), grows no demux state, injects
// into NO peer's resequencer or FEC decoder, and never disturbs the live victim binding. It
// mutation-discriminates the trial-decode `if !isProbe continue` D9/D11 gate: with that gate
// removed, a DATA/PARITY that decodes under peer B's view binds its source to B and dispatches
// into B's planes, turning the "no binding grew / decoder untouched" assertions red.
func TestUnauthenticatedFloodBindsNothingAndInjectsNothing(t *testing.T) {
	pskA := testKey(t, 0x11)
	pskB := testKey(t, 0x22)
	m, victim, peerB, _, victimSrc := victimPeerFEC(t, pskA, pskB)
	secondView := peerPathByName(peerB, "a")
	fecCfg := fec.Config{DataShards: 4, ParityShards: 1, Deadline: testFECDeadline}
	codecB, err := frame.NewCodec(pskB)
	if err != nil {
		t.Fatalf("build peer B codec: %v", err)
	}

	// Buffer a known frame in the victim's ring (not yet popped): the flood must leave it intact.
	codecA, err := frame.NewCodec(pskA)
	if err != nil {
		t.Fatalf("build peer A codec: %v", err)
	}
	m.demuxInbound(m.paths[0], mustEncodeData(t, codecA, 500, m.paths[0].id, "victim-buffered"), victimSrc)

	before := m.peerBySourceLenForTest()
	victimDecoderRecoveredBefore := victim.fecRecv.Load().stats().Recovered

	// The flood: from 300 distinct spoofed unbound sources, cycle three adversary shapes —
	// DATA forged under B's psk, a single-frame reconstructing PARITY forged under B's psk (FEC
	// garbage that WOULD reconstruct a data frame if it ever reached a decoder), and pure random
	// garbage (the no-psk adversary). None is an authenticated PROBE, so none may bind.
	const flood = 300
	for i := 1; i <= flood; i++ {
		src := synthSource(i)
		switch i % 3 {
		case 0:
			m.demuxInbound(m.paths[0], mustEncodeData(t, codecB, uint64(i), secondView.id, "flood-data"), src)
		case 1:
			group, index, dataCount, parityPayload := singleFrameParity(t, fecCfg, uint64(1000+i), []byte("flood-parity"))
			parity, perr := codecB.Encode(nil, frame.Parity{FECGroup: group, ParityIndex: index, DataCount: dataCount, PathID: secondView.id, Payload: parityPayload})
			if perr != nil {
				t.Fatalf("encode flood parity: %v", perr)
			}
			m.demuxInbound(m.paths[0], parity, src)
		default:
			garbage := make([]byte, 96)
			if _, rerr := rand.Read(garbage); rerr != nil {
				t.Fatalf("draw garbage: %v", rerr)
			}
			m.demuxInbound(m.paths[0], garbage, src)
		}
	}

	// No source bound: the demux map did not grow by a single entry (bootstrap flood is bounded).
	if after := m.peerBySourceLenForTest(); after != before {
		t.Fatalf("the unauthenticated flood grew the demux map from %d to %d (a non-PROBE bound a source)", before, after)
	}
	// Peer B — whose psk half the flood used — was never bound or instantiated: its lazy heavy
	// state is still absent (a DATA/PARITY that decoded under B's view must never bind B).
	if peerB.resequencer.Load() != nil {
		t.Fatal("peer B's heavy state was instantiated by a non-PROBE flood (a DATA/PARITY bound it)")
	}
	if _, ok := secondView.getRemote(); ok {
		t.Fatal("peer B learned a remote from a non-PROBE flood")
	}
	// The victim's armed FEC decoder never advanced: no flood PARITY reached it (a leaked PARITY
	// with DataCount=1 would have reconstructed a frame and moved Recovered).
	if got := victim.fecRecv.Load().stats().Recovered; got != victimDecoderRecoveredBefore {
		t.Fatalf("the flood's FEC garbage reached the victim's decoder: Recovered %d -> %d", victimDecoderRecoveredBefore, got)
	}
	// The live victim binding, liveness, and buffered frame all survived untouched.
	if bound, ok := m.lookupPeerBySource(victimSrc); !ok || bound != victim {
		t.Fatal("the flood disturbed the live victim binding")
	}
	if m.paths[0].prober.State() != telemetry.StateUp {
		t.Fatalf("the flood evicted/disturbed the live victim: %v", m.paths[0].prober.State())
	}
	if it, ok := victim.resequencer.Load().Pop(); !ok || !bytes.Equal(it.Payload, []byte("victim-buffered")) {
		t.Fatalf("the flood corrupted the victim's buffered frame: ok=%v payload=%q", ok, it.Payload)
	}
}
