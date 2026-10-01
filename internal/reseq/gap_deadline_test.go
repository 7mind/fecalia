package reseq_test

import (
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/reseq"
)

const (
	gapTimeout = 250 * time.Millisecond
	gapHold    = 60 * time.Millisecond
)

// newBoundedHoldResequencer builds the receiver the adaptive transport runs: a
// hold bound shorter than the construction timeout.
func newBoundedHoldResequencer(clk *fakeClock, window uint64) *reseq.Resequencer {
	r := reseq.New(window, gapTimeout, clk)
	r.SetHoldBound(gapHold)
	return r
}

// newHeldGapResequencer returns a bounded-hold resequencer with seq 0 delivered
// and seq 2 buffered behind the missing seq 1, the gap armed at the current
// clock instant.
func newHeldGapResequencer(clk *fakeClock) *reseq.Resequencer {
	r := newBoundedHoldResequencer(clk, 64)
	r.Observe(0, []byte("zero"), testSrc)
	drainHold(r)
	r.Observe(2, []byte("two"), testSrc)
	return r
}

// changeNotifier installs a coalescing notifier, as the Bind's receive drainer
// does, and returns its channel.
func changeNotifier(r *reseq.Resequencer) chan struct{} {
	changed := make(chan struct{}, 1)
	r.SetNotifier(func() {
		select {
		case changed <- struct{}{}:
		default:
		}
	})
	return changed
}

func notified(changed chan struct{}) bool {
	select {
	case <-changed:
		return true
	default:
		return false
	}
}

// BG regression: filling one gap must not lend its older deadline to the next.
func TestFilledGapDoesNotExpireYoungerGap(t *testing.T) {
	clk := newFakeClock()
	r := reseq.New(64, 150*time.Millisecond, clk)
	r.Observe(0, payloadOf(0), testSrc)
	drain(r)
	r.Observe(2, payloadOf(2), testSrc)
	clk.advance(100 * time.Millisecond)
	r.Observe(4, payloadOf(4), testSrc)
	secondObserved := clk.Now()
	clk.advance(20 * time.Millisecond)
	r.Observe(1, payloadOf(1), testSrc)
	if got := drain(r); !equalSeqs(got, []uint64{1, 2}) {
		t.Fatalf("first gap fill: %v", got)
	}
	deadline, armed := r.ArmedDeadline()
	if !armed || deadline != secondObserved.Add(150*time.Millisecond) {
		t.Fatalf("younger gap inherited old deadline: %v, want %v", deadline, secondObserved.Add(150*time.Millisecond))
	}
	clk.advance(40 * time.Millisecond)
	if !r.Observe(3, payloadOf(3), testSrc) {
		t.Fatal("in-window straggler discarded")
	}
	if got := drain(r); !equalSeqs(got, []uint64{3, 4}) {
		t.Fatalf("younger gap fill: %v", got)
	}
}

func TestBufferedGapsExpireFromSuccessorObservation(t *testing.T) {
	clk := newFakeClock()
	r := newBoundedHoldResequencer(clk, 64)

	r.Observe(0, []byte("zero"), testSrc)
	if got := drainHold(r); len(got) != 1 || got[0] != "zero" {
		t.Fatalf("seed delivery = %v, want [zero]", got)
	}
	r.Observe(2, []byte("two"), testSrc)
	r.Observe(3, []byte("three"), testSrc)
	r.Observe(5, []byte("five"), testSrc)

	clk.advance(gapHold - time.Nanosecond)
	if got := drainHold(r); len(got) != 0 {
		t.Fatalf("buffered gaps released before their hold elapsed: %v", got)
	}
	clk.advance(time.Nanosecond)
	got := drainHold(r)
	want := []string{"two", "three", "five"}
	if len(got) != len(want) {
		deadline, armed := r.ArmedDeadline()
		t.Fatalf(
			"delivery at the shared observation horizon = %v, want %v; next deadline=%v armed=%v (a fresh full hold cascades latency)",
			got,
			want,
			deadline,
			armed,
		)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("delivery at the shared observation horizon = %v, want %v", got, want)
		}
	}
}

func TestStaggeredBufferedGapRetainsItsRemainingHold(t *testing.T) {
	const stagger = 20 * time.Millisecond
	clk := newFakeClock()
	r := newBoundedHoldResequencer(clk, 64)

	r.Observe(0, []byte("zero"), testSrc)
	drainHold(r)
	r.Observe(2, []byte("two"), testSrc)
	r.Observe(3, []byte("three"), testSrc)
	clk.advance(stagger)
	secondObserved := clk.Now()
	r.Observe(5, []byte("five"), testSrc)

	clk.advance(gapHold - stagger)
	if got := drainHold(r); len(got) != 2 || got[0] != "two" || got[1] != "three" {
		t.Fatalf("first gap deadline delivery = %v, want [two three]", got)
	}
	wantDeadline := secondObserved.Add(gapHold)
	if deadline, armed := r.ArmedDeadline(); !armed || deadline != wantDeadline {
		t.Fatalf("staggered gap deadline = %v armed=%v, want first successor observation + hold = %v",
			deadline, armed, wantDeadline)
	}

	clk.advance(stagger - time.Nanosecond)
	if !r.Observe(4, []byte("four"), testSrc) {
		t.Fatal("fill at staggered gap hold-1ns was rejected")
	}
	if got := drainHold(r); len(got) != 2 || got[0] != "four" || got[1] != "five" {
		t.Fatalf("staggered hold-1ns fill delivery = %v, want [four five]", got)
	}
}

// TestHoldDeadlineBoundaryAndFill pins the half-open hold [armed, armed+hold): a
// straggler one nanosecond before the deadline fills the gap exactly once and in
// order; at the deadline the gap is skipped and the straggler is too late.
func TestHoldDeadlineBoundaryAndFill(t *testing.T) {
	t.Run("hold-1ns straggler fills exactly once in order", func(t *testing.T) {
		clk := newFakeClock()
		r := newHeldGapResequencer(clk)
		clk.advance(gapHold - time.Nanosecond)
		if !r.Observe(1, []byte("one"), testSrc) {
			t.Fatal("straggler immediately before the deadline was rejected")
		}
		if got := drainHold(r); len(got) != 2 || got[0] != "one" || got[1] != "two" {
			t.Fatalf("delivery after hold-1ns fill = %v, want [one two]", got)
		}
		if r.Observe(1, []byte("duplicate"), testSrc) {
			t.Fatal("duplicate straggler was accepted")
		}
		if got := drainHold(r); len(got) != 0 {
			t.Fatalf("duplicate straggler delivered %v", got)
		}
		if stats := r.Stats(); stats.Holds != 1 ||
			stats.GapFills != 1 ||
			stats.DeadlineWakeups != 0 ||
			stats.Skipped != 0 ||
			!stats.ArmedDeadline.IsZero() ||
			stats.ArmedWindow != 0 {
			t.Fatalf("fill observability = %+v", stats)
		}
	})

	t.Run("deadline expires the gap and the straggler is rejected", func(t *testing.T) {
		clk := newFakeClock()
		r := newHeldGapResequencer(clk)
		clk.advance(gapHold)
		if r.Observe(1, []byte("late"), testSrc) {
			t.Fatal("straggler at the half-open deadline was accepted")
		}
		if got := drainHold(r); len(got) != 1 || got[0] != "two" {
			t.Fatalf("delivery at the deadline = %v, want [two]", got)
		}
		if stats := r.Stats(); stats.Holds != 1 ||
			stats.GapFills != 0 ||
			stats.DeadlineWakeups != 1 ||
			stats.Skipped != 1 ||
			stats.DroppedOld != 1 ||
			!stats.ArmedDeadline.IsZero() ||
			stats.ArmedWindow != 0 {
			t.Fatalf("deadline-wake observability = %+v", stats)
		}
	})
}

// TestGapArmNotifiesAndPublishesDeadline: arming a gap wakes the drainer and
// publishes the exact deadline and window it must sleep for.
func TestGapArmNotifiesAndPublishesDeadline(t *testing.T) {
	clk := newFakeClock()
	r := newBoundedHoldResequencer(clk, 64)
	changed := changeNotifier(r)

	r.Observe(0, []byte("zero"), testSrc)
	drainHold(r)
	if deadline, armed := r.ArmedDeadline(); armed || !deadline.IsZero() {
		t.Fatalf("in-order delivery armed a deadline: %v,%v", deadline, armed)
	}
	if notified(changed) {
		t.Fatal("in-order delivery published a deadline change")
	}

	armedAt := clk.Now()
	r.Observe(2, []byte("two"), testSrc)
	if !notified(changed) {
		t.Fatal("gap arm did not publish a change notification")
	}
	if deadline, armed := r.ArmedDeadline(); !armed || deadline != armedAt.Add(gapHold) {
		t.Fatalf("armed deadline = %v,%v, want %v,true", deadline, armed, armedAt.Add(gapHold))
	}
	if stats := r.Stats(); stats.ArmedDeadline != armedAt.Add(gapHold) || stats.ArmedWindow != gapHold {
		t.Fatalf("armed-gap observability = %+v", stats)
	}

	// A later successor behind the same gap changes neither the deadline nor
	// wakes the drainer.
	clk.advance(gapHold / 2)
	r.Observe(3, []byte("three"), testSrc)
	if notified(changed) {
		t.Fatal("a successor behind an armed gap published a change notification")
	}
	if deadline, armed := r.ArmedDeadline(); !armed || deadline != armedAt.Add(gapHold) {
		t.Fatalf("deadline after a later successor = %v,%v, want %v,true", deadline, armed, armedAt.Add(gapHold))
	}

	clk.advance(gapHold / 2)
	if got := drainHold(r); len(got) != 2 || got[0] != "two" || got[1] != "three" {
		t.Fatalf("delivery at the deadline = %v, want [two three]", got)
	}
	if deadline, armed := r.ArmedDeadline(); armed || !deadline.IsZero() {
		t.Fatalf("expired gap left a deadline armed: %v,%v", deadline, armed)
	}
}

func TestGapFillExpiryAndCloseNotify(t *testing.T) {
	t.Run("fill", func(t *testing.T) {
		clk := newFakeClock()
		r := newHeldGapResequencer(clk)
		changed := changeNotifier(r)
		clk.advance(gapHold - time.Nanosecond)
		if !r.Observe(1, []byte("one"), testSrc) {
			t.Fatal("pre-expiry fill rejected")
		}
		if !notified(changed) {
			t.Fatal("gap fill did not publish a change notification")
		}
	})

	t.Run("expiry", func(t *testing.T) {
		clk := newFakeClock()
		r := newHeldGapResequencer(clk)
		changed := changeNotifier(r)
		clk.advance(gapHold)
		drainHold(r)
		if !notified(changed) {
			t.Fatal("expiry did not publish a change notification")
		}
	})

	t.Run("close", func(t *testing.T) {
		clk := newFakeClock()
		r := newHeldGapResequencer(clk)
		changed := changeNotifier(r)
		r.Close()
		if !notified(changed) {
			t.Fatal("close did not publish a change notification")
		}
		if _, armed := r.ArmedDeadline(); armed {
			t.Fatal("close left the deadline armed")
		}
	})
}

func TestRebaselineAtDisarmsGapAndNotifies(t *testing.T) {
	clk := newFakeClock()
	r := newHeldGapResequencer(clk)
	changed := changeNotifier(r)
	r.RebaselineAt(1)
	if !notified(changed) {
		t.Fatal("rebaseline did not notify the receive drainer")
	}
	if deadline, armed := r.ArmedDeadline(); armed || !deadline.IsZero() {
		t.Fatalf("rebaseline left deadline armed: %v,%v", deadline, armed)
	}
	if got := drainHold(r); len(got) != 0 {
		t.Fatalf("rebaseline delivered the buffered pre-switch frame: %v", got)
	}
}

func TestBoundedAdvanceStartsFreshGap(t *testing.T) {
	const window = uint64(64)
	clk := newFakeClock()
	r := newBoundedHoldResequencer(clk, window)
	r.Observe(0, []byte("zero"), testSrc)
	drainHold(r)
	r.Observe(2, []byte("two"), testSrc)
	clk.advance(gapHold - time.Nanosecond)

	// seq65 sits exactly one window ahead of the current gap at seq1. Admitting
	// it advances the bounded window, releases seq2, and exposes a distinct gap
	// [3,65) at the current injected time.
	r.Observe(65, []byte("sixty-five"), testSrc)
	if got := drainHold(r); len(got) != 1 || got[0] != "two" {
		t.Fatalf("bounded-advance delivery = %v, want [two]", got)
	}
	wantFresh := clk.Now().Add(gapHold)
	if deadline, armed := r.ArmedDeadline(); !armed || deadline != wantFresh {
		t.Fatalf("new gap deadline = %v,%v, want fresh %v,true", deadline, armed, wantFresh)
	}

	clk.advance(time.Nanosecond)
	if got := drainHold(r); len(got) != 0 {
		t.Fatalf("new gap inherited the prior hold and released early: %v", got)
	}
	clk.advance(gapHold - time.Nanosecond)
	if got := drainHold(r); len(got) != 1 || got[0] != "sixty-five" {
		t.Fatalf("fresh-gap expiry delivery = %v, want [sixty-five]", got)
	}
	if stats := r.Stats(); stats.Skipped != 63 || stats.Released != 3 {
		t.Fatalf("bounded advance stats = skipped %d released %d, want 63/3", stats.Skipped, stats.Released)
	}
	if got := drainHold(r); len(got) != 0 {
		t.Fatalf("bounded advance delivered a frame twice: %v", got)
	}
}

// TestGapSuccessorsHeldUntilTimeout: successors buffered behind a missing head
// are never released early, whatever path delivered them — they wait the full
// hold and are then released in order by the timeout skip.
func TestGapSuccessorsHeldUntilTimeout(t *testing.T) {
	clk := newFakeClock()
	r := reseq.New(64, gapTimeout, clk)

	r.Observe(0, payloadOf(0), testSrc)
	_ = drain(r) // next == 1
	r.Observe(2, payloadOf(2), testSrc)
	r.Observe(3, payloadOf(3), testSrc)
	if got := drain(r); len(got) != 0 {
		t.Fatalf("gap released %v without the full hold — a reordered head would be dropped", got)
	}
	clk.advance(gapTimeout + time.Millisecond)
	if got := drain(r); !equalSeqs(got, []uint64{2, 3}) {
		t.Fatalf("delivery after the full hold = %v, want [2 3]", got)
	}
}

// TestHeldGapStragglerReordersIn: a gap is held, so the straggler for the missing
// head is reordered in rather than dropped.
func TestHeldGapStragglerReordersIn(t *testing.T) {
	clk := newFakeClock()
	r := reseq.New(64, gapTimeout, clk)

	r.Observe(0, payloadOf(0), testSrc)
	r.Observe(1, payloadOf(1), testSrc)
	_ = drain(r) // next == 2

	r.Observe(3, payloadOf(3), testSrc)
	r.Observe(4, payloadOf(4), testSrc)
	if got := drain(r); len(got) != 0 {
		t.Fatalf("gap released %v before its hold elapsed — stragglers would be dropped", got)
	}

	r.Observe(2, payloadOf(2), testSrc)
	if got := drain(r); !equalSeqs(got, []uint64{2, 3, 4}) {
		t.Fatalf("delivery = %v, want [2 3 4] (straggler reordered in, not dropped)", got)
	}
}

// TestGapStaysHeldWhileSuccessorsKeepArriving: successors that keep arriving
// behind a permanent gap neither release it early nor extend its hold; it is
// released by the ordinary timeout skip.
func TestGapStaysHeldWhileSuccessorsKeepArriving(t *testing.T) {
	clk := newFakeClock()
	r := reseq.New(64, gapTimeout, clk)

	r.Observe(0, payloadOf(0), testSrc)
	_ = drain(r) // next == 1 — a permanent gap at 1 from here on

	// 4 steps of 50 ms = 200 ms < the 250 ms timeout.
	seq := uint64(2)
	for i := 0; i < 4; i++ {
		r.Observe(seq, payloadOf(seq), testSrc)
		seq++
		clk.advance(50 * time.Millisecond)
		if got := drain(r); len(got) != 0 {
			t.Fatalf("step %d released %v before the full hold", i, got)
		}
	}

	clk.advance(50 * time.Millisecond)
	if got := drain(r); !equalSeqs(got, []uint64{2, 3, 4, 5}) {
		t.Fatalf("delivery at the first successor's deadline = %v, want [2 3 4 5]", got)
	}
}
