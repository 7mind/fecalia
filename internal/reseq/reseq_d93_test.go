package reseq_test

import (
	"net/netip"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/reseq"
)

// d93Timeout is the production per-gap hold cap (internal/bind/multipath.go's
// resequencerTimeout). The D93 tests assert the single-delivering-path fast path
// releases WITHOUT ever advancing the fake clock this far.
const d93Timeout = 250 * time.Millisecond

// TestSinglePathKeyGapReleasesImmediately is the primary D93 reproduction (case a).
// Over ONE delivering path a head-of-line gap is genuine loss — a single path
// preserves order, so no straggler can be in flight — so its successors must
// release with ~0 hold, NOT after the full 250 ms timeout. RED before the policy:
// the successors sit buffered until the clock is advanced past the timeout.
func TestSinglePathKeyGapReleasesImmediately(t *testing.T) {
	clk := newFakeClock()
	r := reseq.New(64, d93Timeout, clk)
	const key = uint32(0x1234)

	r.ObserveFromPath(0, payloadOf(0), testSrc, key)
	if got := drain(r); !equalSeqs(got, []uint64{0}) {
		t.Fatalf("head delivery = %v, want [0]", got)
	}

	// Head 1 is genuinely lost; single-path successors 2, 3 arrive behind the gap.
	r.ObserveFromPath(2, payloadOf(2), testSrc, key)
	r.ObserveFromPath(3, payloadOf(3), testSrc, key)

	// No clock advance: the single delivering path makes the gap genuine loss, so
	// 2 and 3 release immediately (effective per-gap hold ~0).
	got := drain(r)
	if !equalSeqs(got, []uint64{2, 3}) {
		t.Fatalf("single-path gap delivery = %v, want [2 3] released immediately (no 250ms wait)", got)
	}
	// The skipped head arriving late is still dropped, never delivered out of order.
	r.ObserveFromPath(1, payloadOf(1), testSrc, key)
	if got := drain(r); len(got) != 0 {
		t.Fatalf("late skipped head delivered %v, want dropped (monotonic delivery)", got)
	}
}

// TestPathKeyOpaqueZeroIsKnownPath pins R251's orthogonality nuance: pathKey 0 is a
// REAL, KNOWN delivering path (a path can legitimately compose to key 0), distinct
// from Observe's UNKNOWN identity. A single key-0 stream therefore gets immediate
// release, unlike the legacy Observe path. RED before the policy.
func TestPathKeyOpaqueZeroIsKnownPath(t *testing.T) {
	clk := newFakeClock()
	r := reseq.New(64, d93Timeout, clk)

	r.ObserveFromPath(0, payloadOf(0), testSrc, 0)
	_ = drain(r) // next == 1
	r.ObserveFromPath(2, payloadOf(2), testSrc, 0)
	r.ObserveFromPath(3, payloadOf(3), testSrc, 0)
	if got := drain(r); !equalSeqs(got, []uint64{2, 3}) {
		t.Fatalf("single key-0 gap = %v, want [2 3] released immediately (key 0 is a known path)", got)
	}
}

// TestTwoPathKeysSameSrcHeldFullHold is the R249/R250 regression (case b): two
// distinct delivering paths under the SAME src AddrPort (the concentrator
// single-socket / edge same-source topology). Cross-path reorder is possible, so a
// gap must be held the FULL timeout, not fast-released. pathKey is treated opaquely
// — arbitrary distinct values (0 and 0xFFFFFFFF) are simply two distinct paths.
func TestTwoPathKeysSameSrcHeldFullHold(t *testing.T) {
	clk := newFakeClock()
	r := reseq.New(64, d93Timeout, clk)
	const kA, kB = uint32(0), uint32(0xFFFFFFFF)

	r.ObserveFromPath(0, payloadOf(0), testSrc, kA)
	_ = drain(r) // next == 1

	// A gap at 1 forms; a successor from a SECOND distinct key re-arms the full hold.
	r.ObserveFromPath(2, payloadOf(2), testSrc, kA)
	r.ObserveFromPath(3, payloadOf(3), testSrc, kB)
	if got := drain(r); len(got) != 0 {
		t.Fatalf("two-path gap released %v without the full hold — same-src reorder would be dropped", got)
	}

	clk.advance(d93Timeout + time.Millisecond)
	got := drain(r)
	if !equalSeqs(got, []uint64{2, 3}) {
		t.Fatalf("two-path delivery after full hold = %v, want [2 3]", got)
	}
}

// TestTransitionStragglersHeldWhenSecondKeyAppears is case c: a single-key steady
// state (immediate release engaged) transitions to two paths while stragglers are
// in flight. The second key appears BEFORE the gap is fast-released, so — because
// the trailing window (500 ms) exceeds the straggler horizon — immediate release
// disengages and the gap is held, letting the straggler for the missing head
// reorder in rather than being dropped.
func TestTransitionStragglersHeldWhenSecondKeyAppears(t *testing.T) {
	clk := newFakeClock()
	r := reseq.New(64, d93Timeout, clk)
	const kA, kB = uint32(0xAAAA), uint32(0xBBBB)

	// Single-key steady state establishes immediate release.
	r.ObserveFromPath(0, payloadOf(0), testSrc, kA)
	r.ObserveFromPath(1, payloadOf(1), testSrc, kA)
	_ = drain(r) // next == 2

	// A gap forms at 2 with a successor (3) buffered behind it; before any Pop
	// fast-releases it, a SECOND path delivers 4 — two distinct keys are now in the
	// trailing window, so immediate release disengages and the gap is held.
	r.ObserveFromPath(3, payloadOf(3), testSrc, kA)
	r.ObserveFromPath(4, payloadOf(4), testSrc, kB)
	if got := drain(r); len(got) != 0 {
		t.Fatalf("transition gap fast-released %v — stragglers would be dropped", got)
	}

	// The straggler for the missing head arrives within the trailing window and is
	// reordered in, not dropped.
	r.ObserveFromPath(2, payloadOf(2), testSrc, kB)
	got := drain(r)
	if !equalSeqs(got, []uint64{2, 3, 4}) {
		t.Fatalf("transition delivery = %v, want [2 3 4] (straggler reordered in, not dropped)", got)
	}
}

// TestSustainedInterleaveNeverImmediate is case d (flap): a sustained two-key
// interleave must NEVER toggle to immediate release. Each alternating key re-arms
// the full hold, so a permanent gap stays held for the full timeout and is only
// released by the ordinary timeout skip — never early.
func TestSustainedInterleaveNeverImmediate(t *testing.T) {
	clk := newFakeClock()
	r := reseq.New(64, d93Timeout, clk)
	const kA, kB = uint32(1), uint32(2)
	// Alternate starting with the OTHER key so the interleave is genuinely two-key
	// from the first loop step (step 0 introduces the second distinct key).
	keys := []uint32{kB, kA}

	r.ObserveFromPath(0, payloadOf(0), testSrc, kA)
	_ = drain(r) // next == 1 — a permanent gap at 1 from here on

	// 4 steps of 50 ms = 200 ms < the 250 ms timeout: under sustained interleave the
	// gap must stay held (never fast-released).
	seq := uint64(2)
	for i := 0; i < 4; i++ {
		r.ObserveFromPath(seq, payloadOf(seq), testSrc, keys[i%2])
		seq++
		clk.advance(50 * time.Millisecond)
		if got := drain(r); len(got) != 0 {
			t.Fatalf("interleave step %d released %v before the full hold — flapped to immediate release", i, got)
		}
	}

	// Past the full hold the gap is released by the ordinary timeout skip (not
	// immediate), proving the successors were held, not fast-released.
	clk.advance(d93Timeout)
	if got := drain(r); len(got) == 0 {
		t.Fatalf("interleave never released even after the full hold elapsed")
	}
}

// TestFECActiveSuppressesImmediateRelease is case e: with a single delivering path
// but FEC active, immediate release is suppressed entirely (a parity repair may
// still fill the gap), so successors are held the full hold. Clearing FEC re-enables
// immediate release for a fresh single-key gap.
func TestFECActiveSuppressesImmediateRelease(t *testing.T) {
	clk := newFakeClock()
	r := reseq.New(64, d93Timeout, clk)
	const key = uint32(42)

	r.SetFECActive(true)
	r.ObserveFromPath(0, payloadOf(0), testSrc, key)
	_ = drain(r) // next == 1

	r.ObserveFromPath(2, payloadOf(2), testSrc, key)
	r.ObserveFromPath(3, payloadOf(3), testSrc, key)
	if got := drain(r); len(got) != 0 {
		t.Fatalf("FEC-active single-key gap fast-released %v — must be held for parity repair", got)
	}
	clk.advance(d93Timeout + time.Millisecond)
	if got := drain(r); !equalSeqs(got, []uint64{2, 3}) {
		t.Fatalf("FEC-active delivery after full hold = %v, want [2 3]", got)
	}

	// FEC cleared: a fresh single-key gap now releases immediately.
	r.SetFECActive(false)
	r.ObserveFromPath(5, payloadOf(5), testSrc, key) // gap at 4
	if got := drain(r); !equalSeqs(got, []uint64{5}) {
		t.Fatalf("after SetFECActive(false) single-key gap = %v, want [5] released immediately", got)
	}
}

// TestLegacyObserveUnknownKeepsFullHold is the orthogonality guard: the legacy
// Observe ingest carries UNKNOWN path identity, which must NEVER enable immediate
// release even though the stream is effectively single-source. Its behaviour is
// identical to pre-D93 — a gap waits the full timeout. This is the invariant that
// keeps every existing internal/reseq test green unchanged.
func TestLegacyObserveUnknownKeepsFullHold(t *testing.T) {
	clk := newFakeClock()
	r := reseq.New(64, d93Timeout, clk)

	r.Observe(0, payloadOf(0), testSrc)
	_ = drain(r) // next == 1
	r.Observe(2, payloadOf(2), testSrc)
	r.Observe(3, payloadOf(3), testSrc)
	if got := drain(r); len(got) != 0 {
		t.Fatalf("legacy Observe fast-released %v — unknown path must hold the full timeout", got)
	}
	clk.advance(d93Timeout + time.Millisecond)
	if got := drain(r); !equalSeqs(got, []uint64{2, 3}) {
		t.Fatalf("legacy Observe delivery after full hold = %v, want [2 3]", got)
	}
}

// TestRebaselineResetsTrailingKeySet pins that a rebaseline (hub failover / peer
// restart) RESETS the trailing key set, so immediate release stays suppressed for
// a full trailing window afterwards even under a single key — the stream must
// re-establish single-path confidence before the fast path re-engages. Without the
// reset a stale single-key candidate would fast-release the post-failover gap,
// dropping the reorder that a failover typically produces.
func TestRebaselineResetsTrailingKeySet(t *testing.T) {
	clk := newFakeClock()
	r := reseq.New(64, d93Timeout, clk)
	const key = uint32(7)

	// Establish single-key immediate release on the prior stream.
	r.ObserveFromPath(0, payloadOf(0), testSrc, key)
	r.ObserveFromPath(1, payloadOf(1), testSrc, key)
	_ = drain(r)

	// A trusted control event rebaselines the release point and resets the key set.
	r.Rebaseline(netip.AddrPort{})

	// The post-rebaseline stream re-anchors; a gap within the cold window must be
	// held conservatively, not fast-released.
	r.ObserveFromPath(100, payloadOf(100), testSrc, key) // re-anchors next == 100
	_ = drain(r)
	r.ObserveFromPath(102, payloadOf(102), testSrc, key) // gap at 101
	if got := drain(r); len(got) != 0 {
		t.Fatalf("post-rebaseline gap fast-released %v within the cold window — key set not reset to conservative", got)
	}
	// It still releases once the full hold elapses (ordinary timeout skip).
	clk.advance(d93Timeout + time.Millisecond)
	if got := drain(r); !equalSeqs(got, []uint64{102}) {
		t.Fatalf("post-rebaseline delivery after full hold = %v, want [102]", got)
	}
}

// TestRebaselineToLowResetsTrailingKeySet mirrors the reset requirement for the
// peer-restart low-anchor rebaseline: it too resets the trailing key set so
// immediate release does not carry over the pre-restart single-path confidence.
func TestRebaselineToLowResetsTrailingKeySet(t *testing.T) {
	clk := newFakeClock()
	const window = 64
	r := reseq.New(window, d93Timeout, clk)
	const key = uint32(9)

	// Busy prior boot: next advances well past one window, single-key immediate on.
	const priorHi = 200
	for s := uint64(0); s < priorHi; s++ {
		r.ObserveFromPath(s, payloadOf(s), testSrc, key)
	}
	_ = drain(r)

	r.RebaselineToLow() // peer restart; resets the trailing key set

	// The restarted low-seq stream re-anchors; a gap within the cold window is held.
	r.ObserveFromPath(1, payloadOf(1), testSrc, key)
	_ = drain(r)                                     // re-anchors next == 2
	r.ObserveFromPath(3, payloadOf(3), testSrc, key) // gap at 2
	if got := drain(r); len(got) != 0 {
		t.Fatalf("post-RebaselineToLow gap fast-released %v within the cold window — key set not reset", got)
	}
}
