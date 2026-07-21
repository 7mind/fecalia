package reseq_test

import (
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/reseq"
)

// T242 (D93 observability leg): the resequencer exports cumulative hold accounting
// measured via the injected Clock — Holds (gaps that armed a hold), HoldNanos
// (cumulative held time before skip/immediate-release/fill), and ImmediateReleases
// (gaps released via the D93 single-delivering-path fast path, counted DISTINCTLY
// from timeout skips). These fake-clock tests pin the three acceptance cases and the
// Skipped-vs-immediate semantics (an immediate release increments Skipped AND
// ImmediateReleases; the distinction is recoverable from the two counters).

// TestHoldAccountingTimeoutSkip is acceptance case (a): a held-then-skipped gap
// increments Holds by one and adds ~the full held duration to HoldNanos, and is NOT
// an immediate release. Two DISTINCT delivering keys force the full hold.
func TestHoldAccountingTimeoutSkip(t *testing.T) {
	clk := newFakeClock()
	r := reseq.New(64, d93Timeout, clk)
	const kA, kB = uint32(1), uint32(2)

	r.ObserveFromPath(0, payloadOf(0), testSrc, kA)
	_ = drain(r) // next == 1

	// A gap at 1 arms a hold at t0; a second distinct key keeps it held (no fast path).
	r.ObserveFromPath(2, payloadOf(2), testSrc, kA)
	r.ObserveFromPath(3, payloadOf(3), testSrc, kB)
	if got := drain(r); len(got) != 0 {
		t.Fatalf("gap released %v before the hold elapsed", got)
	}

	// Past the deadline the gap is skipped by the ordinary timeout path.
	clk.advance(d93Timeout + time.Millisecond)
	if got := drain(r); !equalSeqs(got, []uint64{2, 3}) {
		t.Fatalf("timeout delivery = %v, want [2 3]", got)
	}

	s := r.Stats()
	if s.Holds != 1 {
		t.Errorf("Holds = %d, want 1", s.Holds)
	}
	if s.ImmediateReleases != 0 {
		t.Errorf("ImmediateReleases = %d, want 0 (a timeout skip is not a fast-path release)", s.ImmediateReleases)
	}
	wantNanos := uint64((d93Timeout + time.Millisecond).Nanoseconds())
	if s.HoldNanos != wantNanos {
		t.Errorf("HoldNanos = %d, want %d (the full held duration arm→skip)", s.HoldNanos, wantNanos)
	}
	if s.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1 (the lost head seq 1)", s.Skipped)
	}
}

// TestHoldAccountingImmediateRelease is acceptance case (b): a single-delivering-path
// immediate release increments ImmediateReleases and its hold contribution is ~0. It
// also pins the semantics decision — an immediate release STILL increments Skipped by
// the lost gap's seq count (Skipped's meaning is unchanged), while ImmediateReleases
// separately counts the fast-path EVENT so the two are distinguishable.
func TestHoldAccountingImmediateRelease(t *testing.T) {
	clk := newFakeClock()
	r := reseq.New(64, d93Timeout, clk)
	const key = uint32(0x55)

	r.ObserveFromPath(0, payloadOf(0), testSrc, key)
	_ = drain(r) // next == 1

	// A gap at 1 arms a hold; the single delivering path releases its successors with
	// ~0 hold — WITHOUT advancing the clock.
	r.ObserveFromPath(2, payloadOf(2), testSrc, key)
	r.ObserveFromPath(3, payloadOf(3), testSrc, key)
	if got := drain(r); !equalSeqs(got, []uint64{2, 3}) {
		t.Fatalf("single-path delivery = %v, want [2 3] released immediately", got)
	}

	s := r.Stats()
	if s.Holds != 1 {
		t.Errorf("Holds = %d, want 1", s.Holds)
	}
	if s.ImmediateReleases != 1 {
		t.Errorf("ImmediateReleases = %d, want 1 (the D93 fast path fired)", s.ImmediateReleases)
	}
	if s.HoldNanos != 0 {
		t.Errorf("HoldNanos = %d, want 0 (~zero hold on the fast path)", s.HoldNanos)
	}
	if s.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1 (an immediate release still counts the lost seq — semantics unchanged)", s.Skipped)
	}
}

// TestHoldAccountingPartialFill is acceptance case (c): a gap FILLED before its
// deadline contributes its partial hold time to HoldNanos, skips nothing, and is not
// an immediate release. Two distinct keys keep the gap held so the missing head can
// reorder in.
func TestHoldAccountingPartialFill(t *testing.T) {
	clk := newFakeClock()
	r := reseq.New(64, d93Timeout, clk)
	const kA, kB = uint32(3), uint32(4)

	r.ObserveFromPath(0, payloadOf(0), testSrc, kA)
	_ = drain(r) // next == 1

	// A gap at 1 arms a hold at t0; a second key keeps it held rather than fast-released.
	r.ObserveFromPath(2, payloadOf(2), testSrc, kA)
	r.ObserveFromPath(3, payloadOf(3), testSrc, kB)
	if got := drain(r); len(got) != 0 {
		t.Fatalf("gap released %v before the fill", got)
	}

	const partial = 80 * time.Millisecond
	clk.advance(partial)

	// The missing head arrives within the hold and fills the gap.
	r.ObserveFromPath(1, payloadOf(1), testSrc, kB)
	if got := drain(r); !equalSeqs(got, []uint64{1, 2, 3}) {
		t.Fatalf("post-fill delivery = %v, want [1 2 3] (head reordered in)", got)
	}

	s := r.Stats()
	if s.Holds != 1 {
		t.Errorf("Holds = %d, want 1", s.Holds)
	}
	if s.ImmediateReleases != 0 {
		t.Errorf("ImmediateReleases = %d, want 0 (the gap was filled, not fast-released)", s.ImmediateReleases)
	}
	if s.Skipped != 0 {
		t.Errorf("Skipped = %d, want 0 (nothing lost — the gap was filled)", s.Skipped)
	}
	if s.HoldNanos != uint64(partial.Nanoseconds()) {
		t.Errorf("HoldNanos = %d, want %d (partial held time arm→fill)", s.HoldNanos, uint64(partial.Nanoseconds()))
	}
}
