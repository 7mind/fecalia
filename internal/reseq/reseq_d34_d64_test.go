package reseq_test

import (
	"net/netip"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/reseq"
)

// TestRebaselineSourceIdentityGateD34 is the D34 regression: after a plain (hub-failover)
// Rebaseline armed with the expected standby endpoint, the release point re-anchors ONLY on a
// frame from THAT endpoint. A stale straggler still draining from the OLD hub (a different
// source) is SUSPECT-dropped and does NOT re-pin `next`, while the genuine standby frame
// re-anchors and delivers. Before the fix Rebaseline took no source and re-anchored on whatever
// arrived first, so an old-hub straggler could re-pin the release point to a wrong value.
func TestRebaselineSourceIdentityGateD34(t *testing.T) {
	clk := newFakeClock()
	const window = 64
	r := reseq.New(window, time.Hour, clk)
	oldHub := netip.MustParseAddrPort("192.0.2.1:51820")
	standby := netip.MustParseAddrPort("192.0.2.9:51820")

	// Prior hub advances the release point well past one window.
	for s := uint64(0); s < 200; s++ {
		r.Observe(s, payloadOf(s), oldHub)
	}
	_ = drain(r)

	// Hub failover: Rebaseline armed on the standby endpoint the edge failed over to.
	r.Rebaseline(standby)

	// A stale straggler from the OLD hub (low seq) must NOT re-anchor — dropped as suspect.
	beforeSuspect := r.Stats().DroppedSuspect
	r.Observe(1, payloadOf(1), oldHub)
	if got := drain(r); len(got) != 0 {
		t.Fatalf("old-hub straggler re-anchored/delivered %v — the D34 source gate must drop it", got)
	}
	if r.Stats().DroppedSuspect <= beforeSuspect {
		t.Fatal("old-hub straggler was not SUSPECT-dropped (DroppedSuspect did not increase) — D34 gate not armed")
	}

	// The genuine standby frame (from the failover endpoint) re-anchors and delivers in order.
	r.Observe(1, payloadOf(1), standby)
	r.Observe(2, payloadOf(2), standby)
	if got := drain(r); !equalSeqs(got, []uint64{1, 2}) {
		t.Fatalf("after the D34 gate re-anchored on the standby source, delivered %v, want [1 2]", got)
	}
}

// TestRebaselineSourceGateSelfHealsD34 verifies the D34 source gate is BOUNDED (like the
// pendingLow gate): if the expected standby endpoint never sends (a mis-config or a further
// failover), after O(window) wrong-source frames the gate falls back to re-anchoring on the next
// frame, so it can NEVER permanently blackhole the stream.
func TestRebaselineSourceGateSelfHealsD34(t *testing.T) {
	clk := newFakeClock()
	const window = 8
	r := reseq.New(window, time.Hour, clk)
	standby := netip.MustParseAddrPort("192.0.2.9:51820")
	other := netip.MustParseAddrPort("192.0.2.5:51820")

	for s := uint64(0); s < 100; s++ {
		r.Observe(s, payloadOf(s), standby)
	}
	_ = drain(r)
	r.Rebaseline(standby)

	// Feed window+1 frames from a DIFFERENT source. The first `window` are SUSPECT-dropped; the
	// (window+1)th trips the bounded self-heal and re-anchors on itself.
	base := uint64(1000)
	var delivered []uint64
	for i := uint64(0); i <= window; i++ {
		r.Observe(base+i, payloadOf(base+i), other)
		delivered = append(delivered, drain(r)...)
	}
	if len(delivered) == 0 {
		t.Fatal("D34 source gate never self-healed: window+1 wrong-source frames delivered nothing (blackhole)")
	}
	if delivered[len(delivered)-1] != base+window {
		t.Fatalf("self-heal re-anchored on seq %d, want the (window+1)th wrong-source frame %d", delivered[len(delivered)-1], base+window)
	}
}

// TestObserveRecoveredNeverRepinsD64 is the D64 regression: a parity-RECOVERED frame must NEVER
// establish or re-pin the release point (ObserveRecovered's documented contract). Before the
// first live Observe (a fresh ring, or after a plain Rebaseline that cleared `started`), a
// recovered frame must be dropped, NOT seat `next` to its repaired past seq. Before the fix the
// !started branch set next = the recovered frame's seq, dumping the (future) live buffer.
func TestObserveRecoveredNeverRepinsD64(t *testing.T) {
	clk := newFakeClock()
	r := reseq.New(64, time.Hour, clk)

	// A recovered frame on a FRESH (unstarted) ring must not establish the release point.
	before := r.Stats().DroppedSuspect
	if placed := r.ObserveRecovered(500, payloadOf(500), testSrc); placed {
		t.Fatal("ObserveRecovered on a fresh ring PLACED a frame — it must not establish the release point (D64)")
	}
	if r.Stats().DroppedSuspect <= before {
		t.Fatal("early ObserveRecovered was not dropped (DroppedSuspect did not increase)")
	}
	if got := drain(r); len(got) != 0 {
		t.Fatalf("recovered frame was delivered from an unstarted ring: %v", got)
	}

	// A live Observe now pins `next` normally — proving the recovered frame did NOT pin it to 500
	// (which would have made this live seq 10 look like a far-past suspect and drop it).
	r.Observe(10, payloadOf(10), testSrc)
	if got := drain(r); !equalSeqs(got, []uint64{10}) {
		t.Fatalf("first live Observe delivered %v, want [10] (release point must anchor on the live frame, not the earlier recovered 500)", got)
	}
}
