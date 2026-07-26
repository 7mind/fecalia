package reseq_test

import (
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/reseq"
)

func TestBufferedRecoveryGapsExpireFromSuccessorObservation(t *testing.T) {
	const hold = 60 * time.Millisecond
	clk := newFakeClock()
	r := reseq.New(64, 250*time.Millisecond, clk)
	r.SetFECActive(true)
	r.SetRecoveryWindow(reseq.RecoveryWindow{
		Enabled:    true,
		Revision:   1,
		PathKey:    7,
		Source:     testSrc,
		Hold:       hold,
		ValidUntil: clk.Now().Add(time.Second),
	})

	r.ObserveFromPath(0, []byte("zero"), testSrc, 7)
	if got := drainHold(r); len(got) != 1 || got[0] != "zero" {
		t.Fatalf("seed delivery = %v, want [zero]", got)
	}
	r.ObserveFromPath(2, []byte("two"), testSrc, 7)
	r.ObserveFromPath(3, []byte("three"), testSrc, 7)
	r.ObserveFromPath(5, []byte("five"), testSrc, 7)

	clk.advance(hold - time.Nanosecond)
	if got := drainHold(r); len(got) != 0 {
		t.Fatalf("buffered gaps released before their recovery horizon: %v", got)
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

func TestStaggeredBufferedGapRetainsItsRemainingRecoveryTime(t *testing.T) {
	const (
		hold    = 60 * time.Millisecond
		stagger = 20 * time.Millisecond
	)
	clk := newFakeClock()
	r := reseq.New(64, 250*time.Millisecond, clk)
	r.SetFECActive(true)
	r.SetRecoveryWindow(reseq.RecoveryWindow{
		Enabled:    true,
		Revision:   1,
		PathKey:    7,
		Source:     testSrc,
		Hold:       hold,
		ValidUntil: clk.Now().Add(time.Second),
	})

	r.ObserveFromPath(0, []byte("zero"), testSrc, 7)
	drainHold(r)
	r.ObserveFromPath(2, []byte("two"), testSrc, 7)
	r.ObserveFromPath(3, []byte("three"), testSrc, 7)
	clk.advance(stagger)
	secondObserved := clk.Now()
	r.ObserveFromPath(5, []byte("five"), testSrc, 7)

	clk.advance(hold - stagger)
	if got := drainHold(r); len(got) != 2 || got[0] != "two" || got[1] != "three" {
		t.Fatalf("first gap deadline delivery = %v, want [two three]", got)
	}
	wantDeadline := secondObserved.Add(hold)
	if deadline, armed := r.ArmedDeadline(); !armed || deadline != wantDeadline {
		t.Fatalf("staggered gap deadline = %v armed=%v, want first successor observation + W = %v",
			deadline, armed, wantDeadline)
	}

	clk.advance(stagger - time.Nanosecond)
	if !r.ObserveRecovered(4, []byte("four"), testSrc) {
		t.Fatal("repair at staggered gap W-1ns was rejected")
	}
	if got := drainHold(r); len(got) != 2 || got[0] != "four" || got[1] != "five" {
		t.Fatalf("staggered W-1ns repair delivery = %v, want [four five]", got)
	}
}

func TestBufferedGapObservationCannotPredateConservativeRearm(t *testing.T) {
	const (
		fastHold = 60 * time.Millisecond
		advance  = 20 * time.Millisecond
	)
	clk := newFakeClock()
	r := reseq.New(64, 250*time.Millisecond, clk)
	r.SetFECActive(true)
	r.SetRecoveryWindow(reseq.RecoveryWindow{
		Enabled:    true,
		Revision:   1,
		PathKey:    7,
		Source:     testSrc,
		Hold:       fastHold,
		ValidUntil: clk.Now().Add(time.Second),
	})

	r.ObserveFromPath(0, []byte("zero"), testSrc, 7)
	drainHold(r)
	r.ObserveFromPath(2, []byte("two"), testSrc, 7)
	r.ObserveFromPath(3, []byte("three"), testSrc, 7)
	r.ObserveFromPath(5, []byte("five"), testSrc, 7)

	clk.advance(advance)
	r.ObserveFromPath(5, nil, testSrc, 8)
	rearmedAt := clk.Now()
	if !r.ObserveRecovered(1, []byte("one"), testSrc) {
		t.Fatal("repair after conservative rearm was rejected")
	}
	if got := drainHold(r); len(got) != 3 ||
		got[0] != "one" || got[1] != "two" || got[2] != "three" {
		t.Fatalf("rearmed first-gap fill delivery = %v, want [one two three]", got)
	}
	wantDeadline := rearmedAt.Add(250 * time.Millisecond)
	if deadline, armed := r.ArmedDeadline(); !armed || deadline != wantDeadline {
		t.Fatalf("post-rearm buffered gap deadline = %v armed=%v, want %v",
			deadline, armed, wantDeadline)
	}
}
