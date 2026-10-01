package reseq_test

import (
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/reseq"
)

// T242: the resequencer exports cumulative hold accounting measured via the
// injected Clock — Holds (gaps that armed a hold) and HoldNanos (cumulative held
// time before skip/fill). These fake-clock tests pin the acceptance cases.

// TestHoldAccountingTimeoutSkip is acceptance case (a): a held-then-skipped gap
// increments Holds by one and adds ~the full held duration to HoldNanos.
func TestHoldAccountingTimeoutSkip(t *testing.T) {
	clk := newFakeClock()
	r := reseq.New(64, gapTimeout, clk)
	r.Observe(0, payloadOf(0), testSrc)
	_ = drain(r) // next == 1

	// A gap at 1 arms a hold at t0.
	r.Observe(2, payloadOf(2), testSrc)
	r.Observe(3, payloadOf(3), testSrc)
	if got := drain(r); len(got) != 0 {
		t.Fatalf("gap released %v before the hold elapsed", got)
	}

	// Past the deadline the gap is skipped by the ordinary timeout path.
	clk.advance(gapTimeout + time.Millisecond)
	if got := drain(r); !equalSeqs(got, []uint64{2, 3}) {
		t.Fatalf("timeout delivery = %v, want [2 3]", got)
	}

	s := r.Stats()
	if s.Holds != 1 {
		t.Errorf("Holds = %d, want 1", s.Holds)
	}
	wantNanos := uint64((gapTimeout + time.Millisecond).Nanoseconds())
	if s.HoldNanos != wantNanos {
		t.Errorf("HoldNanos = %d, want %d (the full held duration arm→skip)", s.HoldNanos, wantNanos)
	}
	if s.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1 (the lost head seq 1)", s.Skipped)
	}
}

// TestHoldAccountingPartialFill is acceptance case (b): a gap FILLED before its
// deadline contributes its partial hold time to HoldNanos and skips nothing.
func TestHoldAccountingPartialFill(t *testing.T) {
	clk := newFakeClock()
	r := reseq.New(64, gapTimeout, clk)
	r.Observe(0, payloadOf(0), testSrc)
	_ = drain(r) // next == 1

	// A gap at 1 arms a hold at t0.
	r.Observe(2, payloadOf(2), testSrc)
	r.Observe(3, payloadOf(3), testSrc)
	if got := drain(r); len(got) != 0 {
		t.Fatalf("gap released %v before the fill", got)
	}

	const partial = 80 * time.Millisecond
	clk.advance(partial)

	// The missing head arrives within the hold and fills the gap.
	r.Observe(1, payloadOf(1), testSrc)
	if got := drain(r); !equalSeqs(got, []uint64{1, 2, 3}) {
		t.Fatalf("post-fill delivery = %v, want [1 2 3] (head reordered in)", got)
	}

	s := r.Stats()
	if s.Holds != 1 {
		t.Errorf("Holds = %d, want 1", s.Holds)
	}
	if s.Skipped != 0 {
		t.Errorf("Skipped = %d, want 0 (nothing lost — the gap was filled)", s.Skipped)
	}
	if s.HoldNanos != uint64(partial.Nanoseconds()) {
		t.Errorf("HoldNanos = %d, want %d (partial held time arm→fill)", s.HoldNanos, uint64(partial.Nanoseconds()))
	}
}
