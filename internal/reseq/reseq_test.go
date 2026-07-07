package reseq_test

import (
	"encoding/binary"
	"math/rand"
	"net/netip"
	"sort"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/reseq"
)

// fakeClock is a hand-advanced reseq.Clock. The resequencer tests drive it on a
// single goroutine, so no internal synchronization is needed and every timeout
// transition is deterministic (no wall-clock, no real sleeps).
type fakeClock struct{ now time.Time }

func newFakeClock() *fakeClock { return &fakeClock{now: time.Unix(1_700_000_000, 0)} }

func (c *fakeClock) Now() time.Time          { return c.now }
func (c *fakeClock) advance(d time.Duration) { c.now = c.now.Add(d) }

// testSrc is an arbitrary but stable outer source address; the ordering tests do
// not depend on its value (they assert on the seq-tagged payload).
var testSrc = netip.MustParseAddrPort("192.0.2.1:51820")

// payloadOf encodes a seq into an 8-byte payload so a popped item is traceable
// back to the frame that produced it.
func payloadOf(seq uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, seq)
	return b
}

// seqOf decodes the seq a payload was built from.
func seqOf(p []byte) uint64 { return binary.BigEndian.Uint64(p) }

// drain pops every currently-ready item, returning the delivered seqs in order.
func drain(r *reseq.Resequencer) []uint64 {
	var out []uint64
	for {
		it, ok := r.Pop()
		if !ok {
			return out
		}
		out = append(out, seqOf(it.Payload))
	}
}

func equalSeqs(a, b []uint64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestInOrderDelivery: a contiguous in-order stream is delivered unchanged, each
// frame released immediately.
func TestInOrderDelivery(t *testing.T) {
	clk := newFakeClock()
	r := reseq.New(64, time.Second, clk)

	var got []uint64
	for seq := uint64(0); seq < 10; seq++ {
		r.Observe(seq, payloadOf(seq), testSrc)
		got = append(got, drain(r)...)
	}
	want := []uint64{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}
	if !equalSeqs(got, want) {
		t.Fatalf("in-order delivery = %v, want %v", got, want)
	}
	if b := r.Buffered(); b != 0 {
		t.Fatalf("buffered after full drain = %d, want 0", b)
	}
}

// TestReorderWithinWindow: frames arriving out of order but within the window are
// released in ascending outer-seq order once the gap fills.
func TestReorderWithinWindow(t *testing.T) {
	clk := newFakeClock()
	r := reseq.New(64, time.Second, clk)

	// Arrival order 0, 3, 2, 1, 4 — a classic multipath stripe reorder.
	arrival := []uint64{0, 3, 2, 1, 4}
	var got []uint64
	for _, seq := range arrival {
		r.Observe(seq, payloadOf(seq), testSrc)
		got = append(got, drain(r)...)
	}
	want := []uint64{0, 1, 2, 3, 4}
	if !equalSeqs(got, want) {
		t.Fatalf("reordered delivery = %v, want %v (ascending)", got, want)
	}
	// While 1..3 waited on the missing head 1, they were buffered — memory bounded.
	if hw := r.HighWater(); hw == 0 || hw > 64 {
		t.Fatalf("high-water buffered = %d, want in (0,64]", hw)
	}
}

// TestDuplicatesDroppedOnce: a seq observed multiple times (before AND after its
// release) is delivered exactly once.
func TestDuplicatesDroppedOnce(t *testing.T) {
	clk := newFakeClock()
	r := reseq.New(64, time.Second, clk)

	// Duplicate a buffered frame, then the head, then a duplicate after release.
	r.Observe(0, payloadOf(0), testSrc)
	r.Observe(2, payloadOf(2), testSrc)
	r.Observe(2, payloadOf(2), testSrc) // dup of a buffered frame
	r.Observe(1, payloadOf(1), testSrc)
	r.Observe(1, payloadOf(1), testSrc) // dup after 1 was released
	r.Observe(0, payloadOf(0), testSrc) // dup of an already-released head

	got := drain(r)
	want := []uint64{0, 1, 2}
	if !equalSeqs(got, want) {
		t.Fatalf("delivery under duplication = %v, want %v", got, want)
	}
	if s := r.Stats(); s.DroppedDup+s.DroppedOld < 3 {
		t.Fatalf("dropped dup/old = %d+%d, want >= 3 duplicates suppressed", s.DroppedDup, s.DroppedOld)
	}
}

// TestBeyondWindowReleasesAndBoundsMemory: a frame at or past next+window forces
// the window to advance, releasing the buffered run and skipping the lost head,
// and the buffer never exceeds the window.
func TestBeyondWindowReleasesAndBoundsMemory(t *testing.T) {
	clk := newFakeClock()
	const window = 4
	r := reseq.New(window, time.Hour, clk) // huge timeout: only the window forces progress

	r.Observe(0, payloadOf(0), testSrc) // delivered, next=1
	_ = drain(r)

	// Head 1 is lost; 2,3,4 buffer behind it.
	for _, seq := range []uint64{2, 3, 4} {
		r.Observe(seq, payloadOf(seq), testSrc)
		if b := r.Buffered(); uint64(b) > window {
			t.Fatalf("buffered = %d exceeds window %d", b, window)
		}
	}
	// Nothing deliverable yet: head 1 still missing, timeout not reached.
	if got := drain(r); len(got) != 0 {
		t.Fatalf("premature delivery %v while head-of-line 1 missing", got)
	}

	// A frame at next+window (== 1+4 == 5) forces the window forward past the lost 1.
	r.Observe(5, payloadOf(5), testSrc)
	got := drain(r)
	want := []uint64{2, 3, 4, 5}
	if !equalSeqs(got, want) {
		t.Fatalf("post-advance delivery = %v, want %v (1 skipped as lost)", got, want)
	}
	if hw := r.HighWater(); uint64(hw) > window {
		t.Fatalf("high-water %d exceeds window %d: memory not bounded", hw, window)
	}
	if s := r.Stats(); s.Skipped < 1 {
		t.Fatalf("skipped = %d, want >= 1 (the lost head 1)", s.Skipped)
	}
}

// TestTimeoutReleasesHeadOfLine: when the head seq never arrives, buffered frames
// ahead of it are released after the timeout rather than held forever, and a
// later arrival of the skipped seq is dropped (never delivered out of order).
func TestTimeoutReleasesHeadOfLine(t *testing.T) {
	clk := newFakeClock()
	const timeout = 100 * time.Millisecond
	r := reseq.New(64, timeout, clk)

	r.Observe(0, payloadOf(0), testSrc) // delivered, next=1
	if got := drain(r); !equalSeqs(got, []uint64{0}) {
		t.Fatalf("head delivery = %v, want [0]", got)
	}

	// Head 1 is missing; 2 and 3 buffer behind it, arming the hold clock.
	r.Observe(2, payloadOf(2), testSrc)
	r.Observe(3, payloadOf(3), testSrc)

	clk.advance(timeout / 2)
	if got := drain(r); len(got) != 0 {
		t.Fatalf("released %v before timeout elapsed", got)
	}

	clk.advance(timeout) // now past the deadline
	got := drain(r)
	want := []uint64{2, 3}
	if !equalSeqs(got, want) {
		t.Fatalf("post-timeout delivery = %v, want %v (1 skipped)", got, want)
	}

	// The late head arrives after the skip: it must be dropped, not delivered out
	// of order (delivery stays monotonic — WG's anti-replay never regresses).
	r.Observe(1, payloadOf(1), testSrc)
	if got := drain(r); len(got) != 0 {
		t.Fatalf("late skipped head delivered %v, want dropped", got)
	}
	if s := r.Stats(); s.Skipped < 1 {
		t.Fatalf("skipped = %d, want >= 1", s.Skipped)
	}
}

// TestNonZeroStartSeq: the first observed seq pins the release point, so a stream
// that starts mid-space (as the Bind's outer-seq does — it starts at 1) is not
// stalled waiting for phantom seq 0.
func TestNonZeroStartSeq(t *testing.T) {
	clk := newFakeClock()
	r := reseq.New(64, time.Second, clk)

	var got []uint64
	for _, seq := range []uint64{100, 101, 103, 102} {
		r.Observe(seq, payloadOf(seq), testSrc)
		got = append(got, drain(r)...)
	}
	want := []uint64{100, 101, 102, 103}
	if !equalSeqs(got, want) {
		t.Fatalf("mid-space delivery = %v, want %v", got, want)
	}
}

// TestPropertyReorderDupLoss is the randomized-trace property: over many seeds,
// under adversarial reorder + duplication + loss, the resequencer delivers in
// strictly ascending order, exactly once, never fabricates a seq, and never
// buffers more than the window (bounded memory). A benign sub-run (no loss, no
// window-overflow, no timeout skip) additionally asserts EVERYTHING deliverable
// is delivered.
func TestPropertyReorderDupLoss(t *testing.T) {
	const window = 32
	for seed := int64(0); seed < 200; seed++ {
		rng := rand.New(rand.NewSource(seed))
		clk := newFakeClock()
		timeout := 50 * time.Millisecond
		r := reseq.New(window, timeout, clk)

		const n = 500
		// Build the network trace: each seq may be lost, and delivered copies are
		// reordered within a bounded look-ahead so the reorder distance stays a
		// realistic multiple of typical jitter (not unbounded).
		type ev struct {
			seq uint64
			key float64
		}
		var events []ev
		observedAtLeastOnce := make(map[uint64]bool)
		// Bounded reorder: arrival order = seqs sorted by (seq + jitter) with jitter
		// in [0, window/2), which bounds the inversion distance to < window/2 — the
		// same "cross-path skew is bounded" property real links have.
		reorder := float64(window / 2)
		for seq := uint64(0); seq < n; seq++ {
			if rng.Intn(100) < 10 { // 10% loss
				continue
			}
			copies := 1
			if rng.Intn(100) < 15 { // 15% duplicated
				copies = 2
			}
			for c := 0; c < copies; c++ {
				events = append(events, ev{seq: seq, key: float64(seq) + rng.Float64()*reorder})
			}
			observedAtLeastOnce[seq] = true
		}
		sort.SliceStable(events, func(i, j int) bool { return events[i].key < events[j].key })

		delivered := make([]uint64, 0, n)
		seen := make(map[uint64]bool)
		step := 0
		feed := func(it reseq.Item) {
			seq := seqOf(it.Payload)
			if !observedAtLeastOnce[seq] {
				t.Fatalf("seed %d: delivered seq %d that was never observed (fabricated)", seed, seq)
			}
			if seen[seq] {
				t.Fatalf("seed %d: seq %d delivered twice (not exactly-once)", seed, seq)
			}
			seen[seq] = true
			if len(delivered) > 0 && seq <= delivered[len(delivered)-1] {
				t.Fatalf("seed %d: out-of-order delivery %d after %d", seed, seq, delivered[len(delivered)-1])
			}
			delivered = append(delivered, seq)
		}

		for _, e := range events {
			r.Observe(e.seq, payloadOf(e.seq), testSrc)
			if b := r.Buffered(); uint64(b) > window {
				t.Fatalf("seed %d: buffered %d exceeds window %d (unbounded memory)", seed, b, window)
			}
			// Interleave draining and occasional clock advances so timeouts fire.
			if step%3 == 0 {
				clk.advance(20 * time.Millisecond)
			}
			for {
				it, ok := r.Pop()
				if !ok {
					break
				}
				feed(it)
			}
			step++
		}
		// Advance well past any pending timeout and flush the tail.
		clk.advance(timeout + time.Second)
		for {
			it, ok := r.Pop()
			if !ok {
				break
			}
			feed(it)
		}

		if b := r.Buffered(); b != 0 {
			t.Fatalf("seed %d: %d frames still buffered after full flush (held forever)", seed, b)
		}
		if hw := r.HighWater(); uint64(hw) > window {
			t.Fatalf("seed %d: high-water %d exceeds window %d", seed, hw, window)
		}
	}
}

// TestPropertyNoLossCompleteness is the completeness half of the property: with
// NO loss and a window that comfortably exceeds the bounded reorder distance, and
// no permanent gaps, EVERY frame is delivered exactly once in order — nothing
// deliverable is dropped.
func TestPropertyNoLossCompleteness(t *testing.T) {
	const window = 64
	for seed := int64(0); seed < 100; seed++ {
		rng := rand.New(rand.NewSource(seed + 1000))
		clk := newFakeClock()
		// Huge timeout so no gap is ever skipped by the clock; with no loss there
		// are no permanent gaps, so the timeout never fires anyway.
		r := reseq.New(window, time.Hour, clk)

		const n = 400
		type ev struct {
			seq uint64
			key float64
		}
		events := make([]ev, 0, n)
		// Bounded reorder strictly < window (jitter in [0, window/4)) guarantees no
		// frame is ever beyond next+window, so no window-overflow skip occurs; with
		// no loss there are no permanent gaps either — every frame is deliverable.
		reorder := float64(window / 4)
		for seq := uint64(0); seq < n; seq++ {
			key := float64(seq) + rng.Float64()*reorder
			if seq == 0 {
				// The resequencer pins its release point to the FIRST observed seq
				// (it joins an unbounded stream and cannot know a lower one is coming).
				// Force the minimum seq to arrive first so "everything is deliverable"
				// holds; a stream that starts mid-reorder legitimately drops the few
				// startup frames that precede the pin (asserted implicitly elsewhere).
				key = -1
			}
			events = append(events, ev{seq: seq, key: key})
		}
		sort.SliceStable(events, func(i, j int) bool { return events[i].key < events[j].key })

		var delivered []uint64
		for _, e := range events {
			r.Observe(e.seq, payloadOf(e.seq), testSrc)
			delivered = append(delivered, drain(r)...)
		}
		delivered = append(delivered, drain(r)...)

		if uint64(len(delivered)) != n {
			t.Fatalf("seed %d: delivered %d frames, want all %d (completeness)", seed, len(delivered), n)
		}
		for i, seq := range delivered {
			if seq != uint64(i) {
				t.Fatalf("seed %d: delivered[%d] = %d, want %d (in-order, exactly-once)", seed, i, seq, i)
			}
		}
	}
}
