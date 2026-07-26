package reseq_test

import (
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/reseq"
)

func TestLossObserverReportsExactFinalizedSequenceRange(t *testing.T) {
	const timeout = 10 * time.Millisecond
	clock := newFakeClock()
	r := reseq.New(64, timeout, clock)
	type lossRange struct {
		first uint64
		count uint64
	}
	var observed []lossRange
	r.SetLossObserver(func(first, count uint64) {
		observed = append(observed, lossRange{first: first, count: count})
	})

	r.Observe(10, payloadOf(10), testSrc)
	r.Observe(14, payloadOf(14), testSrc)
	clock.advance(timeout)
	_, _ = r.Pop()

	want := []lossRange{{first: 11, count: 3}}
	if len(observed) != len(want) || observed[0] != want[0] {
		t.Fatalf("finalized loss ranges = %+v, want %+v", observed, want)
	}
	if r.ObserveRecovered(12, payloadOf(12), testSrc) {
		t.Fatal("late recovered DATA was admitted after its sequence finalized")
	}
	if len(observed) != len(want) {
		t.Fatalf("late recovery changed finalized loss ranges: %+v", observed)
	}
}
