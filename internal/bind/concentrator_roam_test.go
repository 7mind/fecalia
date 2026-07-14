package bind

import (
	"bytes"
	"net/netip"
	"testing"

	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/frame"
)

// TestConcentratorRoamRebindsPeerOnAuthenticatedProbe is the T90 acceptance for per-peer
// NAT/roaming on a shared concentrator socket. It mirrors the single-peer re-roam discipline
// (D11/T16, TestMultipathReRoamRelearnsRemoteVirtStable) at the per-peer demux layer:
//
//   - Peer B is bound to its ORIGINAL source by an authenticated PROBE; peer A (the primary)
//     is bound to its own source likewise.
//   - Peer B roams: its traffic now arrives from a NEW, not-yet-bound source. B's DATA from
//     that new source is DROPPED — a binding, like a return remote, is learned ONLY from an
//     authenticated PROBE (D9/D11), never from forgeable DATA. Critically the dropped DATA is
//     never MISROUTED into peer A's resequencer.
//   - A fresh authenticated PROBE under B's psk from the new source RE-BINDS the new source to
//     the SAME peer B; thereafter DATA from the new source routes to B's resequencer.
//   - Throughout, peer A's resequencer observes ONLY peer A's own frames — none of B's, before,
//     during, or after the roam.
//
// It drives demuxInbound directly (no goroutines), like source_bind_test.go / roam_test.go.
func TestConcentratorRoamRebindsPeerOnAuthenticatedProbe(t *testing.T) {
	pskA := testKey(t, 0x11) // primary == peer A
	pskB := testKey(t, 0x22) // peer B
	clk := newFakeClock()

	m, primary, second := twoPeerConcentrator(t, pskA, pskB)
	aView := m.paths[0] // the primary's (peer A's) view of shared socket "a" — what the reader hands demuxInbound
	bView := peerPathByName(second, "a")
	if bView == nil {
		t.Fatal("peer B has no view of shared path 'a'")
	}

	dataCodecA, _ := frame.NewCodec(pskA)
	dataCodecB, _ := frame.NewCodec(pskB)
	dataA := func(seq uint64, payload string) []byte {
		raw, err := dataCodecA.Encode(nil, frame.Data{OuterSeq: seq, PathID: aView.id, Payload: []byte(payload)})
		if err != nil {
			t.Fatalf("encode peer-A data: %v", err)
		}
		return raw
	}
	dataB := func(seq uint64, payload string) []byte {
		raw, err := dataCodecB.Encode(nil, frame.Data{OuterSeq: seq, PathID: bView.id, Payload: []byte(payload)})
		if err != nil {
			t.Fatalf("encode peer-B data: %v", err)
		}
		return raw
	}
	probeUnder := func(psk config.Key, pathID uint8, seq uint64) []byte {
		raw, err := frame.Encode(psk, frame.Probe{PathID: pathID, ProbeSeq: seq, TimestampNanos: clk.Now().UnixNano(), IsEcho: false})
		if err != nil {
			t.Fatalf("encode probe: %v", err)
		}
		return raw
	}

	srcA := netip.MustParseAddrPort("198.51.100.10:5000") // peer A's source
	srcB0 := netip.MustParseAddrPort("203.0.113.20:6000") // peer B's ORIGINAL source
	srcB1 := netip.MustParseAddrPort("192.0.2.30:7000")   // peer B's source AFTER the roam (a NEW, unbound addr)

	// --- bind both peers via authenticated PROBEs on the shared socket ---
	m.demuxInbound(aView, probeUnder(pskA, aView.id, 1), srcA)
	if bound, ok := m.lookupPeerBySource(srcA); !ok || bound != primary {
		t.Fatalf("srcA bound to %v (ok=%v), want peer A (primary)", bound, ok)
	}
	m.demuxInbound(aView, probeUnder(pskB, bView.id, 1), srcB0)
	if bound, ok := m.lookupPeerBySource(srcB0); !ok || bound != second {
		t.Fatalf("srcB0 bound to %v (ok=%v), want peer B", bound, ok)
	}

	// Pre-roam: peer A's own DATA routes to peer A. (First frame the primary's resequencer sees.)
	m.demuxInbound(aView, dataA(10, "a-pre"), srcA)

	// --- the roam: peer B's traffic now appears from srcB1 (a NEW, unbound source) ---
	// B's DATA from the new source must be DROPPED until an authenticated PROBE re-binds it, and
	// must NOT be misrouted into peer A's resequencer.
	m.demuxInbound(aView, dataB(20, "b-roam-early"), srcB1)
	if _, ok := m.lookupPeerBySource(srcB1); ok {
		t.Fatal("DATA from the roamed (unbound) source established a source->peer binding — only a PROBE may (D9/D11)")
	}
	if it, ok := second.resequencer.Load().Pop(); ok {
		t.Fatalf("peer B's DATA from an unbound roamed source was delivered before any re-binding PROBE (%q)", it.Payload)
	}

	// A fresh authenticated PROBE under peer B's psk from the NEW source re-binds it to the SAME peer B.
	m.demuxInbound(aView, probeUnder(pskB, bView.id, 2), srcB1)
	bound, ok := m.lookupPeerBySource(srcB1)
	if !ok {
		t.Fatal("an authenticated PROBE from the roamed source did not re-bind it")
	}
	if bound != second {
		t.Fatalf("roamed source re-bound to the wrong peer: got %v, want peer B", bound)
	}
	// Peer B's view learned the new source as its return remote (authenticated re-learn, D11).
	if remote, ok := bView.getRemote(); !ok || remote != srcB1 {
		t.Fatalf("peer B view remote after roam = %v (ok=%v), want %v", remote, ok, srcB1)
	}

	// Post-roam: DATA from the new source now routes to peer B's resequencer.
	m.demuxInbound(aView, dataB(21, "b-roam-late"), srcB1)
	if it, ok := second.resequencer.Load().Pop(); !ok || !bytes.Equal(it.Payload, []byte("b-roam-late")) {
		t.Fatalf("DATA from the re-bound roamed source was not delivered to peer B: ok=%v payload=%q", ok, it.Payload)
	} else if it.Src != srcB1 {
		t.Fatalf("peer B's post-roam frame carried src %v, want %v", it.Src, srcB1)
	}

	// Peer A's own DATA still routes to peer A after the roam churn.
	m.demuxInbound(aView, dataA(11, "a-post"), srcA)

	// --- invariant: peer A's resequencer observed ONLY peer A's own frames, never any of B's. ---
	rqA := primary.resequencer.Load()
	var gotA [][]byte
	for {
		it, ok := rqA.Pop()
		if !ok {
			break
		}
		gotA = append(gotA, append([]byte(nil), it.Payload...))
	}
	wantA := [][]byte{[]byte("a-pre"), []byte("a-post")}
	if len(gotA) != len(wantA) {
		t.Fatalf("peer A resequencer delivered %d frame(s) %q, want exactly %q", len(gotA), gotA, wantA)
	}
	for i := range wantA {
		if !bytes.Equal(gotA[i], wantA[i]) {
			t.Fatalf("peer A frame %d = %q, want %q", i, gotA[i], wantA[i])
		}
	}
	for _, p := range gotA {
		if bytes.Equal(p, []byte("b-roam-early")) || bytes.Equal(p, []byte("b-roam-late")) {
			t.Fatalf("peer B's frame %q leaked into peer A's resequencer", p)
		}
	}
}
