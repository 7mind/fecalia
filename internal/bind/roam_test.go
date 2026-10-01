package bind

import (
	"bytes"
	"net/netip"
	"testing"

	"github.com/7mind/wanbond/internal/frame"
)

// TestMultipathReRoamRelearnsRemoteVirtStable is the T16 unit acceptance for the
// receive-side re-roam glue: when the edge's public IP changes on a path, its next
// AUTHENTICATED probe arrives from a NEW source, and the concentrator's Bind must
//
//   - re-learn that path's return remote from the new source (so replies follow
//     the edge to its new address), while
//   - keeping the engine's single virtual endpoint pinned to the ORIGINAL source
//     (the WG session must not observe endpoint churn), and
//   - NOT tripping the receive resequencer's discontinuity guard — a re-roam is
//     the SAME session, so its sequences keep climbing monotonically across it and
//     only the source address changes.
//
// It drives handleInbound directly (no goroutines), mirroring probe_test.go.
func TestMultipathReRoamRelearnsRemoteVirtStable(t *testing.T) {
	psk := testKey(t, 0x16)
	clk := newFakeClock()
	m, _ := newProbingMultipath(t, loopbackPaths(1), psk, clk)
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	ps := m.paths[0]

	srcA := netip.MustParseAddrPort("198.51.100.10:5000") // the edge's original public addr
	srcB := netip.MustParseAddrPort("203.0.113.20:5000")  // its addr AFTER the re-roam

	// The edge's transport: one process, so its sequences keep climbing across the roam.
	edge := newRemoteTransport(t, m.peerState, 0x1122334455667788)
	data := func(payload string) []byte {
		return edge.wire(edge.bulk(ps, []byte(payload)))
	}
	// probe builds an authenticated originating PROBE (IsEcho=false) as the edge
	// would emit it: same session, strictly increasing ProbeSeq.
	probe := func(seq uint64) []byte {
		raw, err := frame.Encode(psk, frame.Probe{PathID: 0, ProbeSeq: seq, TimestampNanos: clk.Now().UnixNano(), SessionID: 0x1122334455667788})
		if err != nil {
			t.Fatalf("encode probe: %v", err)
		}
		return raw
	}
	popPayload := func(want string) netip.AddrPort {
		it, ok := m.resequencer.Load().Pop()
		if !ok {
			t.Fatalf("resequencer had nothing to Pop, want %q", want)
		}
		if !bytes.Equal(it.Payload, []byte(want)) {
			t.Fatalf("popped payload %q, want %q", it.Payload, want)
		}
		// The receiver pins/reads the virtual endpoint from each delivered frame's src.
		_ = m.virtualEndpoint(m.peerState, it.Src)
		return it.Src
	}

	// --- before the re-roam: learn srcA, pin the virtual endpoint to it ---
	m.handleInbound(ps, probe(0), srcA)
	if got, ok := ps.getRemote(); !ok || got != srcA {
		t.Fatalf("remote before re-roam = %v (ok=%v), want %v", got, ok, srcA)
	}
	edge.join(ps, srcA)
	m.handleInbound(ps, data("a"), srcA)
	if src := popPayload("a"); src != srcA {
		t.Fatalf("frame 1 carried src %v, want %v", src, srcA)
	}
	if !m.virt.dstValid() || m.virt.dstAddrPort() != srcA {
		t.Fatalf("virtual endpoint pinned to %v, want %v", m.virt.dstAddrPort(), srcA)
	}

	// --- the re-roam: the edge's next authenticated probe arrives from srcB ---
	m.handleInbound(ps, probe(1), srcB)
	if got, ok := ps.getRemote(); !ok || got != srcB {
		t.Fatalf("remote NOT re-learned after re-roam = %v (ok=%v), want %v", got, ok, srcB)
	}

	// Datagrams keep flowing from srcB with CONTINUOUS sequences (same session, whose
	// hello now arrives from srcB): every one is delivered in order, and the virtual
	// endpoint stays pinned to srcA.
	rebaselines := m.resequencer.Load().Stats().Rebaselines
	edge.join(ps, srcB)
	m.handleInbound(ps, data("b"), srcB)
	m.handleInbound(ps, data("c"), srcB)
	if src := popPayload("b"); src != srcB {
		t.Fatalf("frame 2 carried src %v, want %v", src, srcB)
	}
	if src := popPayload("c"); src != srcB {
		t.Fatalf("frame 3 carried src %v, want %v", src, srcB)
	}
	if m.virt.dstAddrPort() != srcA {
		t.Fatalf("virtual endpoint moved to %v after re-roam, want it PINNED to %v", m.virt.dstAddrPort(), srcA)
	}

	// The discontinuity guard must NOT have fired: the source changed but the stream
	// continued, so there was no re-baseline, no resync and no frame dropped as suspect.
	st := m.resequencer.Load().Stats()
	if st.Rebaselines != rebaselines {
		t.Fatalf("resequencer re-baselined %d time(s) across a re-roam; a same-session source change must not restart the stream", st.Rebaselines-rebaselines)
	}
	if st.Resyncs != 0 {
		t.Fatalf("resequencer resynced %d time(s) across a re-roam; a same-session source change must not look like a discontinuity", st.Resyncs)
	}
	if st.DroppedSuspect != 0 {
		t.Fatalf("resequencer dropped %d frame(s) as suspect across a re-roam, want 0", st.DroppedSuspect)
	}
}
