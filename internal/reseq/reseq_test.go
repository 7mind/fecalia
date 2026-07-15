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
		// Flush the tail. Each remaining head-of-line gap now gets its OWN full
		// timeout (the per-gap-deadline fix — criticism 2), so a single frozen-clock
		// advance no longer collapses every gap at once (that only worked when a
		// buggy expire reused one already-elapsed deadline for all subsequent gaps).
		// Advance the clock once per gap — modelling real wall-clock progress — until
		// the buffer is fully drained. expire always advances past at least one gap
		// when the deadline has elapsed, so this terminates in <= window iterations.
		for r.Buffered() > 0 {
			clk.advance(timeout + time.Second)
			for {
				it, ok := r.Pop()
				if !ok {
					break
				}
				feed(it)
			}
		}
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

// TestWildSeqNoHangNoBlackhole reproduces criticism 1 (advanceTo O(jump) hard
// lock) AND criticism 3a (a single wild-high seq blackholing subsequent legit
// traffic). A DATA frame is unauthenticated (frame.go: "DATA/PARITY forgeable by
// design"), so a garbage datagram decoding as KindData yields a uniformly-random
// uint64 OuterSeq. Observing seq 1 then seq 1<<62:
//   - must NOT hang (the old advanceTo incremented next one seq at a time under
//     the mutex, spinning ~2^62 iterations — a permanent hard lock);
//   - must NOT advance the release point (a single unauthenticated frame cannot
//     move next to a random point in 2^64, else all legitimate lower-seq traffic
//     is dropped as late until the next Open — a one-frame blackhole).
func TestWildSeqNoHangNoBlackhole(t *testing.T) {
	clk := newFakeClock()
	r := reseq.New(8, time.Hour, clk)

	r.Observe(1, payloadOf(1), testSrc)
	if got := drain(r); !equalSeqs(got, []uint64{1}) {
		t.Fatalf("head delivery = %v, want [1]", got)
	}

	// The wild seq must return promptly (O(window), not O(jump)). A watchdog turns
	// the hard-lock regression into a fast test failure instead of a suite timeout.
	done := make(chan struct{})
	go func() {
		r.Observe(uint64(1)<<62, payloadOf(1<<62), testSrc)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("Observe(1<<62) did not return within 2s: advanceTo is O(jump) — hard lock")
	}
	if got := drain(r); len(got) != 0 {
		t.Fatalf("wild seq delivered %v, want nothing", got)
	}

	// Legit traffic below the wild seq must still be delivered — the wild frame
	// must not have advanced the release point.
	var got []uint64
	for _, seq := range []uint64{2, 3, 4} {
		r.Observe(seq, payloadOf(seq), testSrc)
		got = append(got, drain(r)...)
	}
	if !equalSeqs(got, []uint64{2, 3, 4}) {
		t.Fatalf("post-wild-seq delivery = %v, want [2 3 4] (no blackhole)", got)
	}
	if s := r.Stats(); s.Resyncs != 0 {
		t.Fatalf("resyncs = %d, want 0 (a lone wild seq must not resync)", s.Resyncs)
	}
}

// TestPerGapTimeoutNotInherited reproduces criticism 2: after a head-of-line
// timeout fires, a SECOND distinct gap must get its OWN full timeout, not inherit
// the first gap's already-elapsed deadline. With the defect, expire() left
// r.waiting true and re-armed with the stale deadline, so the second gap was
// skipped with ~zero hold — dropping in-window reordered frames that arrived after
// the earlier gap timed out.
func TestPerGapTimeoutNotInherited(t *testing.T) {
	clk := newFakeClock()
	const timeout = 100 * time.Millisecond
	r := reseq.New(64, timeout, clk)

	var delivered []uint64

	r.Observe(0, payloadOf(0), testSrc) // delivered, next=1
	delivered = append(delivered, drain(r)...)

	// First gap: head 1 missing, 2 buffers behind it, arming the hold clock.
	r.Observe(2, payloadOf(2), testSrc)

	clk.advance(150 * time.Millisecond) // past the FIRST gap's deadline
	delivered = append(delivered, drain(r)...)

	// Second gap: head 3 missing, 6 buffers behind it. With the defect the stale
	// (already-elapsed) deadline makes the next drain skip 3,4,5 and advance next
	// to 7 with ~zero hold, after which the reordered 4 and 5 are dropped as late.
	r.Observe(6, payloadOf(6), testSrc)
	delivered = append(delivered, drain(r)...) // fix: held (fresh deadline); defect: advances next to 7

	// These in-window reordered frames must NOT be dropped: under the fix next is
	// still 3 (the second gap is being held on its OWN deadline), so 4 and 5 land
	// in-window.
	r.Observe(4, payloadOf(4), testSrc)
	r.Observe(5, payloadOf(5), testSrc)

	clk.advance(150 * time.Millisecond) // past the SECOND gap's own deadline
	delivered = append(delivered, drain(r)...)

	want := []uint64{0, 2, 4, 5, 6}
	if !equalSeqs(delivered, want) {
		t.Fatalf("multi-gap delivery = %v, want %v (4,5 must not be dropped by an inherited deadline)", delivered, want)
	}
}

// TestPeerRestartResync reproduces criticism 3b: a peer PROCESS RESTART resets its
// outerSeq counter to 1 while the long-lived resequencer keeps next at the old
// high-water. Without a resync guard every frame from the restarted peer is
// dropped as late FOREVER (a regression vs pre-T18). A run of consecutive low-seq
// frames must corroborate and re-pin the release point within a bounded number of
// frames.
func TestPeerRestartResync(t *testing.T) {
	clk := newFakeClock()
	r := reseq.New(8, 100*time.Millisecond, clk)

	// Establish a high release point.
	for seq := uint64(100); seq < 110; seq++ {
		r.Observe(seq, payloadOf(seq), testSrc)
		_ = drain(r)
	}

	// Peer restarts: outerSeq resets to 1,2,3,... — each is far below next=110.
	var delivered []uint64
	for seq := uint64(1); seq <= 6; seq++ {
		r.Observe(seq, payloadOf(seq), testSrc)
		delivered = append(delivered, drain(r)...)
	}

	// Delivery must resume within a bounded number of frames (the corroboration
	// count), losing at most resyncCorroborate-1 frames.
	if len(delivered) == 0 {
		t.Fatalf("peer restart permanently blackholed: delivered nothing after restart")
	}
	if last := delivered[len(delivered)-1]; last != 6 {
		t.Fatalf("post-restart delivery tail = %d, want 6 (delivery resumed)", last)
	}
	if len(delivered) < 3 {
		t.Fatalf("post-restart delivered only %d frames %v, want resync within a bounded few", len(delivered), delivered)
	}
	if s := r.Stats(); s.Resyncs != 1 {
		t.Fatalf("resyncs = %d, want 1 (one corroborated restart)", s.Resyncs)
	}
}

// TestLongOutageForwardResync reproduces criticism 3c: a legitimate long outage
// during which the peer keeps incrementing its outerSeq, so when it returns the
// seqs jump far forward CONSISTENTLY. This must also resync (unlike a lone wild
// seq) because consecutive forward frames corroborate.
func TestLongOutageForwardResync(t *testing.T) {
	clk := newFakeClock()
	r := reseq.New(8, 100*time.Millisecond, clk)

	for seq := uint64(1); seq <= 5; seq++ {
		r.Observe(seq, payloadOf(seq), testSrc)
		_ = drain(r)
	}

	const base = uint64(100000) // far beyond next+resyncFactor*window
	var delivered []uint64
	for i := uint64(0); i < 6; i++ {
		r.Observe(base+i, payloadOf(base+i), testSrc)
		delivered = append(delivered, drain(r)...)
	}
	if len(delivered) == 0 {
		t.Fatalf("long-outage forward jump blackholed: delivered nothing")
	}
	if last := delivered[len(delivered)-1]; last != base+5 {
		t.Fatalf("post-outage delivery tail = %d, want %d (resumed)", last, base+5)
	}
	if s := r.Stats(); s.Resyncs != 1 {
		t.Fatalf("resyncs = %d, want 1", s.Resyncs)
	}
}

// TestJunkSeqsDoNotCorroborate is the negative half of the discontinuity guard:
// uniformly-random junk seqs interleaved with a legit ascending stream must NEVER
// resync (each junk seq is independent in 2^64, so they do not mutually fall
// within one window span) and must never blackhole the legit stream.
func TestJunkSeqsDoNotCorroborate(t *testing.T) {
	clk := newFakeClock()
	const window = 64
	r := reseq.New(window, time.Hour, clk)

	rng := rand.New(rand.NewSource(12345))
	var delivered []uint64
	const startLegit = uint64(1000)
	for i := uint64(0); i < 300; i++ {
		legit := startLegit + i
		r.Observe(legit, payloadOf(legit), testSrc)
		// A junk seq far above the current release point (suspect band), a fresh
		// random value each time so no two corroborate.
		junk := legit + (uint64(1) << 40) + rng.Uint64()%(uint64(1)<<50)
		r.Observe(junk, payloadOf(junk), testSrc)
		delivered = append(delivered, drain(r)...)
	}
	delivered = append(delivered, drain(r)...)

	// Every legit frame delivered exactly once, in order — no junk-induced blackhole.
	if uint64(len(delivered)) != 300 {
		t.Fatalf("delivered %d legit frames, want 300 (junk must not blackhole)", len(delivered))
	}
	for i, seq := range delivered {
		if seq != startLegit+uint64(i) {
			t.Fatalf("delivered[%d] = %d, want %d", i, seq, startLegit+uint64(i))
		}
	}
	if s := r.Stats(); s.Resyncs != 0 {
		t.Fatalf("resyncs = %d, want 0 (independent junk seqs must not corroborate)", s.Resyncs)
	}
}

// TestTriplicatedJunkSeqDoesNotCorroborate reproduces fable r2: three deliveries
// of a SINGLE out-of-band seq (a network-duplicated or forger-replayed junk
// datagram — identical seq, span 0 < window) must NOT corroborate a
// discontinuity. The corroboration run requires resyncCorroborate DISTINCT seqs;
// a repeated identical seq does not advance it. Before the fix the run counted
// consecutive frames without dedup, so this yielded Resyncs==1.
func TestTriplicatedJunkSeqDoesNotCorroborate(t *testing.T) {
	clk := newFakeClock()
	r := reseq.New(8, time.Hour, clk)

	// Establish a low release point with a legit run: next becomes 6.
	for seq := uint64(1); seq <= 5; seq++ {
		r.Observe(seq, payloadOf(seq), testSrc)
		_ = drain(r)
	}

	// One junk seq far above the release point (suspect band), delivered 3 times.
	const junk = uint64(1) << 61
	for i := 0; i < 3; i++ {
		r.Observe(junk, payloadOf(junk), testSrc)
		_ = drain(r)
	}
	if s := r.Stats(); s.Resyncs != 0 {
		t.Fatalf("resyncs = %d, want 0 (one repeated junk seq must not corroborate)", s.Resyncs)
	}

	// The legit stream is not blackholed: the next in-order frame still delivers.
	r.Observe(6, payloadOf(6), testSrc)
	if got := drain(r); !equalSeqs(got, []uint64{6}) {
		t.Fatalf("post-junk delivery = %v, want [6] (no blackhole)", got)
	}
}

// TestFewerThanCDistinctSuspectSeqsDoNotResync is the boundary case: a set of
// FEWER than resyncCorroborate DISTINCT suspect seqs, even repeated and even
// mutually within one window, must not reach the corroboration threshold. Before
// the fix the repeats padded the consecutive count to C and forced a resync.
func TestFewerThanCDistinctSuspectSeqsDoNotResync(t *testing.T) {
	clk := newFakeClock()
	const window = 8
	r := reseq.New(window, time.Hour, clk)

	for seq := uint64(1); seq <= 5; seq++ {
		r.Observe(seq, payloadOf(seq), testSrc)
		_ = drain(r)
	}

	// Two DISTINCT suspect seqs (fewer than C=3), mutually within one window
	// (span 3 < 8), each delivered twice — 2 distinct seqs never reach C=3.
	const a = uint64(1) << 61
	const b = a + 3
	for _, s := range []uint64{a, b, a, b} {
		r.Observe(s, payloadOf(s), testSrc)
		_ = drain(r)
	}
	if st := r.Stats(); st.Resyncs != 0 {
		t.Fatalf("resyncs = %d, want 0 (only 2 distinct suspect seqs, C=3)", st.Resyncs)
	}
	r.Observe(6, payloadOf(6), testSrc)
	if got := drain(r); !equalSeqs(got, []uint64{6}) {
		t.Fatalf("post-suspect delivery = %v, want [6] (no blackhole)", got)
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

// TestObserveRecoveredNeverResyncsOrDumps is the fix witness for the "late-recovered
// frames dump the buffer" defect (T24 #2): a batch of FEC-reconstructed seqs that have
// fallen far below the release point — the exact pattern that, offered via Observe,
// mutually corroborates a BACKWARD resync and discards the whole live buffer (see
// TestPeerRestartResync) — must, via ObserveRecovered, be dropped as too-late with NO
// resync and NO buffer dump. Recovery must never be able to CAUSE the burst loss it
// exists to prevent.
func TestObserveRecoveredNeverResyncsOrDumps(t *testing.T) {
	clk := newFakeClock()
	const window = 8
	r := reseq.New(window, time.Hour, clk)

	// Establish a high release point, then buffer a live run ahead of a head gap.
	for seq := uint64(100); seq < 108; seq++ {
		r.Observe(seq, payloadOf(seq), testSrc)
	}
	_ = drain(r) // next == 108
	for _, seq := range []uint64{109, 110, 111} {
		r.Observe(seq, payloadOf(seq), testSrc)
	}
	if b := r.Buffered(); b != 3 {
		t.Fatalf("setup: buffered = %d, want 3", b)
	}

	// Feed several DISTINCT recovered seqs far below the release point — mutually within
	// one window, so via Observe they would corroborate a backward resync and dump the
	// buffer. Via ObserveRecovered each must be dropped (placed == false).
	for _, seq := range []uint64{1, 2, 3, 4} {
		if placed := r.ObserveRecovered(seq, payloadOf(seq), testSrc); placed {
			t.Fatalf("recovered seq %d far below the release point was placed, want dropped", seq)
		}
	}
	if s := r.Stats(); s.Resyncs != 0 {
		t.Fatalf("resyncs = %d, want 0 (recovered frames must never move the release point)", s.Resyncs)
	}
	if b := r.Buffered(); b != 3 {
		t.Fatalf("buffered = %d after late recovered frames, want 3 (the live buffer was dumped)", b)
	}

	// The live run still delivers once its own gap times out — recovery did not cost it.
	clk.advance(2 * time.Hour)
	got := drain(r)
	if len(got) != 3 || got[0] != 109 || got[2] != 111 {
		t.Fatalf("live run after late recoveries = %v, want [109 110 111]", got)
	}
}

// TestObserveRecoveredFillsGapInOrder is the positive half: a recovered frame that
// lands AT or above the release point fills its gap and is placed (returns true), so a
// TIMELY reconstruction resequences exactly like a natively-received frame.
func TestObserveRecoveredFillsGapInOrder(t *testing.T) {
	clk := newFakeClock()
	r := reseq.New(8, time.Hour, clk)

	r.Observe(1, payloadOf(1), testSrc)
	_ = drain(r)                        // next == 2
	r.Observe(3, payloadOf(3), testSrc) // buffered; head gap at 2

	if placed := r.ObserveRecovered(2, payloadOf(2), testSrc); !placed {
		t.Fatal("recovered seq 2 filling the head gap was not placed")
	}
	got := drain(r)
	if len(got) != 2 || got[0] != 2 || got[1] != 3 {
		t.Fatalf("delivery after gap-filling recovery = %v, want [2 3]", got)
	}
}

// TestRebaselineAdmitsStandbyLowSeqAfterHubSwitch reproduces defect D32 and proves
// the fix. After the release point has advanced far past one window (a busy prior-hub
// stream, as the pre-kill iperf3 baseline does on the real edge), a single LOW
// outer-seq from a freshly-handshaking standby hub — a separate process whose
// outer-seq restarts near 1 — lands in the SUSPECT branch, and a lone frame cannot
// corroborate a resync (that needs resyncCorroborate distinct low seqs, which do not
// arrive within the failover window), so it is DROPPED. Rebaseline() — the trusted
// hub-switch signal — re-anchors the release point so the standby's first frame is
// admitted immediately.
func TestRebaselineAdmitsStandbyLowSeqAfterHubSwitch(t *testing.T) {
	clk := newFakeClock()
	const window = 64
	r := reseq.New(window, time.Second, clk)

	// Prior hub: a busy contiguous stream advances the release point well past one
	// window (next == priorHi afterwards).
	const priorHi = 200
	for s := uint64(0); s < priorHi; s++ {
		r.Observe(s, payloadOf(s), testSrc)
	}
	_ = drain(r)

	// Standby hub restarts its outer-seq near 1: that frame is >1 window below next, so
	// admit() routes it to the SUSPECT branch and the lone frame is dropped (the D32
	// repro precondition).
	const standbySeq = 1
	before := r.Stats().DroppedSuspect
	r.Observe(standbySeq, payloadOf(standbySeq), testSrc)
	if got := drain(r); len(got) != 0 {
		t.Fatalf("standby low-seq frame was delivered WITHOUT a rebaseline: %v — D32 repro precondition broken", got)
	}
	if r.Stats().DroppedSuspect <= before {
		t.Fatal("standby low-seq frame was not dropped as suspect (DroppedSuspect did not increase) — D32 repro precondition broken")
	}

	// The hub-failover switch calls Rebaseline: the DATA-frame sender provably changed to
	// the operator-configured standby, so the next frame re-anchors the release point.
	r.Rebaseline(netip.AddrPort{})
	if s := r.Stats(); s.Rebaselines != 1 {
		t.Fatalf("Rebaselines = %d, want 1", s.Rebaselines)
	}

	// Now the standby's stream is admitted and delivered in order.
	r.Observe(standbySeq, payloadOf(standbySeq), testSrc)
	r.Observe(standbySeq+1, payloadOf(standbySeq+1), testSrc)
	if got := drain(r); !equalSeqs(got, []uint64{standbySeq, standbySeq + 1}) {
		t.Fatalf("after Rebaseline the standby stream delivered %v, want [%d %d]", got, standbySeq, standbySeq+1)
	}
}

// TestRebaselineDiscardsBufferedPreSwitchFrames verifies Rebaseline drops the buffered
// (pre-switch) frames — they belong to the dead prior hub's stream — while leaving the
// already-released FIFO of prior legitimate deliveries intact.
func TestRebaselineDiscardsBufferedPreSwitchFrames(t *testing.T) {
	clk := newFakeClock()
	r := reseq.New(64, time.Hour, clk)

	r.Observe(0, payloadOf(0), testSrc) // delivered, next==1
	r.Observe(2, payloadOf(2), testSrc) // buffered behind the head gap at 1
	r.Observe(3, payloadOf(3), testSrc) // buffered
	if r.Buffered() != 2 {
		t.Fatalf("precondition: Buffered = %d, want 2", r.Buffered())
	}
	// The already-released item (seq 0) is still poppable.
	if r.Pending() != 1 {
		t.Fatalf("precondition: Pending = %d, want 1", r.Pending())
	}

	r.Rebaseline(netip.AddrPort{})
	if r.Buffered() != 0 {
		t.Fatalf("Rebaseline did not discard buffered pre-switch frames: Buffered = %d, want 0", r.Buffered())
	}
	// The prior legitimate delivery survives the rebaseline.
	if got := drain(r); !equalSeqs(got, []uint64{0}) {
		t.Fatalf("released FIFO after Rebaseline = %v, want [0] (prior deliveries must be untouched)", got)
	}
}

// TestRebaselineToLowSurvivesStaleHighStragglerRace is the reseq-level D36 repro (plan
// review R126). Under the saturation precondition the plain Rebaseline (unpin + trust
// the NEXT frame) loses a race: after a PEER RESTART re-baseline, a stale HIGH-seq
// straggler still draining from the OLD boot can land BEFORE the restarted low-seq init
// and re-pin `next` HIGH, and with once-per-epoch restart dedup recovery is then blocked.
// RebaselineToLow re-anchors ONLY on a frame more than one window below the pre-rebaseline
// release point, so the stale-high straggler is SUSPECT-dropped and the subsequent low
// init still admits. The parallel with plain Rebaseline is deliberate: swap RebaselineToLow
// for Rebaseline here and the straggler re-pins next high, the low init is dropped, and the
// final delivery assertion fails.
func TestRebaselineToLowSurvivesStaleHighStragglerRace(t *testing.T) {
	clk := newFakeClock()
	const window = 64
	r := reseq.New(window, time.Second, clk)

	// Prior boot: a busy contiguous stream advances the release point well past one
	// window (next == priorHi afterwards).
	const priorHi = 200
	for s := uint64(0); s < priorHi; s++ {
		r.Observe(s, payloadOf(s), testSrc)
	}
	_ = drain(r)

	// Authenticated peer restart: the low-anchor re-baseline pins the pending anchor at
	// the current (high) release point rather than unpinning outright.
	r.RebaselineToLow()
	if s := r.Stats(); s.Rebaselines != 1 {
		t.Fatalf("Rebaselines = %d, want 1", s.Rebaselines)
	}

	// The re-pin RACE: a stale OLD-boot HIGH-seq straggler arrives FIRST. It must be
	// SUSPECT-dropped and must NOT re-pin `next` high (which a plain Rebaseline would
	// let it do, blocking recovery).
	const staleHigh = priorHi + 5 // above the old release point: a genuine "high" straggler
	beforeSuspect := r.Stats().DroppedSuspect
	r.RebaselineToLow() // idempotent: a repeated restart signal keeps the original high anchor
	if s := r.Stats(); s.Rebaselines != 2 {
		t.Fatalf("Rebaselines = %d after a repeated restart signal, want 2", s.Rebaselines)
	}
	r.Observe(staleHigh, payloadOf(staleHigh), testSrc)
	if got := drain(r); len(got) != 0 {
		t.Fatalf("stale-high straggler was DELIVERED %v — it re-pinned next high (D36 race not closed)", got)
	}
	if r.Stats().DroppedSuspect <= beforeSuspect {
		t.Fatal("stale-high straggler was not SUSPECT-dropped (DroppedSuspect did not increase)")
	}

	// Now the genuine restarted-stream low-seq init arrives: it is more than one window
	// below the pre-rebaseline release point, so it re-anchors and DELIVERS, and it must
	// NOT itself count as a suspect drop.
	const lowInit = 1
	suspectBeforeLow := r.Stats().DroppedSuspect
	r.Observe(lowInit, payloadOf(lowInit), testSrc)
	r.Observe(lowInit+1, payloadOf(lowInit+1), testSrc)
	if got := drain(r); !equalSeqs(got, []uint64{lowInit, lowInit + 1}) {
		t.Fatalf("after RebaselineToLow the restarted low stream delivered %v, want [%d %d]", got, lowInit, lowInit+1)
	}
	if r.Stats().DroppedSuspect != suspectBeforeLow {
		t.Fatalf("the low-seq init was counted as a suspect drop: DroppedSuspect %d -> %d", suspectBeforeLow, r.Stats().DroppedSuspect)
	}
}

// TestRebaselineToLowNoOpBeforeFirstObserve pins that a low-anchor re-baseline on an
// UNSTARTED ring (a torn-down peer re-instantiated a fresh resequencer) enters NO pending
// mode: the first Observe pins `next` normally, whatever its seq, exactly as a fresh ring
// behaves. It bumps the diagnostic counter but must not blackhole the first stream.
func TestRebaselineToLowNoOpBeforeFirstObserve(t *testing.T) {
	clk := newFakeClock()
	r := reseq.New(64, time.Second, clk)

	r.RebaselineToLow()
	if s := r.Stats(); s.Rebaselines != 1 {
		t.Fatalf("Rebaselines = %d, want 1", s.Rebaselines)
	}
	// A fresh high-seq stream pins next and delivers in order — no pending-low gate.
	r.Observe(5000, payloadOf(5000), testSrc)
	r.Observe(5001, payloadOf(5001), testSrc)
	if got := drain(r); !equalSeqs(got, []uint64{5000, 5001}) {
		t.Fatalf("unstarted RebaselineToLow blackholed the first stream: delivered %v, want [5000 5001]", got)
	}
}

// TestRebaselineToLowSmallAnchorSelfHeals is the FIX-1 regression (round-2 review). A
// RebaselineToLow at a SMALL release point (next <= window) must NOT arm the low-anchor
// gate: the re-anchor predicate `seq < anchor && anchor - seq > window` is UNSATISFIABLE
// for the restarted sender's first DATA (outer-seq ~1) when anchor <= window+1, so a
// pin would blackhole the whole restarted stream FOREVER — a regression from the pre-T119
// resync self-heal. Realistic trigger: light traffic / an early restart / a crash-loop
// (a first restart re-anchors next~1, a second restart within one window pins a tiny
// anchor). The fix falls back to a plain unpin at a small anchor, so the new-boot stream
// re-anchors on its first frame and DELIVERS.
func TestRebaselineToLowSmallAnchorSelfHeals(t *testing.T) {
	clk := newFakeClock()
	const window = 64
	r := reseq.New(window, time.Second, clk)

	// Old boot: only 50 contiguous frames, so the release point is SMALL (next == 50 <=
	// window). This is the light-traffic / early-restart precondition.
	const oldBoot = 50
	for s := uint64(0); s < oldBoot; s++ {
		r.Observe(s, payloadOf(s), testSrc)
	}
	_ = drain(r)

	// Authenticated peer restart at the small anchor. FIX 1: no pending-low gate armed
	// (it would be unsatisfiable); plain unpin instead.
	r.RebaselineToLow()
	if s := r.Stats(); s.Rebaselines != 1 {
		t.Fatalf("Rebaselines = %d, want 1", s.Rebaselines)
	}

	// The restarted new-boot stream (outer-seq restarts near 1). Before the fix these were
	// all SUSPECT-dropped forever (a permanent blackhole); after the fix the first frame
	// re-anchors next and the stream DELIVERS in order.
	var want []uint64
	for s := uint64(1); s <= 8; s++ {
		r.Observe(s, payloadOf(s), testSrc)
		want = append(want, s)
	}
	if got := drain(r); !equalSeqs(got, want) {
		t.Fatalf("small-anchor restart blackholed the new-boot stream: delivered %v, want %v", got, want)
	}
	// The stream self-healed on the FIRST frame, not by dropping the whole window.
	if s := r.Stats(); s.DroppedSuspect != 0 {
		t.Fatalf("small-anchor restart SUSPECT-dropped %d new-boot frames; want 0 (plain unpin re-anchors)", s.DroppedSuspect)
	}
}

// TestRebaselineToLowThenRebaselineDelivers is the FIX-2 regression (round-2 review). A
// plain Rebaseline() (the D32 hub-failover path) must CLEAR a pending low-anchor left by a
// prior RebaselineToLow whose restarted low init has not yet re-anchored next. Without the
// fix, Rebaseline resets `started` but leaves pendingLow armed against the stale
// pendingLowAnchor, so every post-failover fail-back frame is re-classified against that
// stale anchor and SUSPECT-dropped — violating Rebaseline's "next Observe re-anchors next"
// postcondition. The fix clears pendingLow, restoring the plain unpin-and-trust-next path.
func TestRebaselineToLowThenRebaselineDelivers(t *testing.T) {
	clk := newFakeClock()
	const window = 64
	r := reseq.New(window, time.Second, clk)

	// Prior boot advances the release point well past one window: next == 200.
	const priorHi = 200
	for s := uint64(0); s < priorHi; s++ {
		r.Observe(s, payloadOf(s), testSrc)
	}
	_ = drain(r)

	// Peer restart arms the pending low-anchor at 200 (the low init has NOT yet arrived).
	r.RebaselineToLow()

	// A D32 hub failover now Rebaselines the SAME resequencer. FIX 2: this clears the
	// pending low-anchor so the standby's fail-back stream is not gated against the stale
	// anchor.
	r.Rebaseline(netip.AddrPort{})

	// The standby's fail-back stream (a fresh outer-seq run, here 300..399). Before the fix
	// these were all SUSPECT-dropped (0/100 delivered) against the stale pendingLowAnchor=200;
	// after the fix the first frame re-anchors next and the stream DELIVERS in order.
	var want []uint64
	for s := uint64(300); s < 400; s++ {
		r.Observe(s, payloadOf(s), testSrc)
		want = append(want, s)
	}
	if got := drain(r); !equalSeqs(got, want) {
		t.Fatalf("RebaselineToLow→Rebaseline left the pending gate in force: delivered %d frames, want %d", len(got), len(want))
	}
	if s := r.Stats(); s.DroppedSuspect != 0 {
		t.Fatalf("post-Rebaseline fail-back stream SUSPECT-dropped %d frames; want 0 (stale pendingLow not cleared)", s.DroppedSuspect)
	}
}

// isContiguousAscending reports whether s is a strictly +1 ascending run.
func isContiguousAscending(s []uint64) bool {
	for i := 1; i < len(s); i++ {
		if s[i] != s[i-1]+1 {
			return false
		}
	}
	return true
}

// TestRebaselineToLowPlainUnpinAtWindowPlusOne is the round-3 FIX-3 lower boundary
// (review R150). At release point next == window+1 the low-anchor gate is NOT armed:
// the re-anchor predicate `seq < anchor && anchor - seq > window` requires
// anchor >= window+2 to admit any positive seq, so at anchor == window+1 it is
// unsatisfiable and arming would blackhole. RebaselineToLow must fall back to a PLAIN
// unpin here, so the restarted stream re-anchors on its first frame with no suspect
// drops — exactly the D32 hub-failover behaviour.
func TestRebaselineToLowPlainUnpinAtWindowPlusOne(t *testing.T) {
	clk := newFakeClock()
	const window = 64
	r := reseq.New(window, time.Second, clk)

	// Advance the release point to EXACTLY window+1 (observe seqs 0..window, i.e.
	// window+1 frames, leaves next == window+1).
	for s := uint64(0); s <= window; s++ {
		r.Observe(s, payloadOf(s), testSrc)
	}
	_ = drain(r) // next == window+1

	r.RebaselineToLow()

	// Restarted new-boot stream (outer-seq near 1): the plain unpin re-anchors on the
	// first frame and the stream DELIVERS with zero suspect drops (gate never armed).
	var want []uint64
	for s := uint64(1); s <= 6; s++ {
		r.Observe(s, payloadOf(s), testSrc)
		want = append(want, s)
	}
	if got := drain(r); !equalSeqs(got, want) {
		t.Fatalf("window+1 anchor did not take the plain-unpin path: delivered %v, want %v", got, want)
	}
	if s := r.Stats(); s.DroppedSuspect != 0 {
		t.Fatalf("window+1 anchor armed the gate (SUSPECT-dropped %d); want plain unpin, 0 drops", s.DroppedSuspect)
	}
}

// TestRebaselineToLowArmedAtWindowPlusTwoSatisfiedBySeq1 is the round-3 FIX-3 upper
// boundary (review R150). At release point next == window+2 the gate IS armed and its
// SOLE in-budget re-anchor frame is seq 1 (anchor - 1 == window+1 > window). A stale
// HIGH straggler is SUSPECT-dropped; seq 1 then re-anchors and the new boot delivers.
// This pins the exact boundary the round-2 guard (arm only when next > window+1) opens.
func TestRebaselineToLowArmedAtWindowPlusTwoSatisfiedBySeq1(t *testing.T) {
	clk := newFakeClock()
	const window = 64
	r := reseq.New(window, time.Second, clk)

	// Advance to EXACTLY window+2 (observe seqs 0..window+1 == window+2 frames).
	for s := uint64(0); s <= window+1; s++ {
		r.Observe(s, payloadOf(s), testSrc)
	}
	_ = drain(r) // next == window+2

	r.RebaselineToLow() // arms pendingLow at anchor == window+2

	// A stale-high old-boot straggler must be SUSPECT-dropped, not re-pin next high.
	const staleHigh = window + 5
	r.Observe(staleHigh, payloadOf(staleHigh), testSrc)
	if got := drain(r); len(got) != 0 {
		t.Fatalf("stale-high straggler delivered %v at the window+2 boundary — gate not armed", got)
	}

	// seq 1 — the ONLY in-budget re-anchor at this boundary — re-anchors and delivers.
	r.Observe(1, payloadOf(1), testSrc)
	r.Observe(2, payloadOf(2), testSrc)
	if got := drain(r); !equalSeqs(got, []uint64{1, 2}) {
		t.Fatalf("seq 1 did not re-anchor the window+2 gate: delivered %v, want [1 2]", got)
	}
}

// TestRebaselineToLowGateRecoversWhenInitFrameLost is the round-3 FIX-3 core regression
// (fable probe, reproduced 0/499; review R150). At anchor == window+2 the only in-budget
// re-anchor frame is seq 1. If that lone wrapped-init frame is LOST — loss under
// saturation is the D36 premise — every later new-boot frame fails `anchor - seq > window`
// and, on the round-2 logic, is SUSPECT-dropped FOREVER (a permanent blackhole: 0/N
// delivered). The bounded gate falls back to a plain unpin after O(window) drops, so the
// new-boot stream self-heals and DELIVERS its tail instead of blackholing.
func TestRebaselineToLowGateRecoversWhenInitFrameLost(t *testing.T) {
	clk := newFakeClock()
	const window = 64
	r := reseq.New(window, time.Second, clk)

	// Advance to EXACTLY window+2 so the gate arms with seq 1 as the sole re-anchor.
	for s := uint64(0); s <= window+1; s++ {
		r.Observe(s, payloadOf(s), testSrc)
	}
	_ = drain(r) // next == window+2
	r.RebaselineToLow()

	// seq 1 is LOST. The restarted stream continues at 2,3,...,last. Each fails the
	// re-anchor predicate and is SUSPECT-dropped until the bounded gate falls back.
	const last = 3 * window
	for s := uint64(2); s <= last; s++ {
		r.Observe(s, payloadOf(s), testSrc)
	}
	got := drain(r)

	if len(got) == 0 {
		t.Fatal("init frame lost blackholed the whole new-boot stream (0/N) — bounded gate did not fall back")
	}
	if !isContiguousAscending(got) {
		t.Fatalf("recovered delivery is not a contiguous run: %v", got)
	}
	if got[len(got)-1] != last {
		t.Fatalf("recovered stream did not catch up: last delivered %d, want %d", got[len(got)-1], last)
	}
	if got[0] <= 1 {
		t.Fatalf("stream delivered from seq %d — the bounded gate should drop a run before falling back", got[0])
	}
	if s := r.Stats(); s.DroppedSuspect == 0 {
		t.Fatal("expected the bounded gate to SUSPECT-drop a bounded run before self-healing; got 0")
	}
}

// TestObserveRecoveredDroppedWhileGateArmed is the round-3 FIX-4 regression (fable probe,
// reproduced next 2→210 Skipped:208; review R150). FEC ObserveRecovered is production-wired
// to the SAME per-peer resequencer and otherwise bypasses the low-anchor gate, so a parity-
// recovered OLD-boot frame in [anchor, anchor+window) would be PLACED while the gate is
// armed; then the re-anchor (which did not clear the ring) leaves that stale cell live, and
// expire() jumps next HIGH past the restarted stream, delivering a stale frame. The fix (a)
// clears the ring on re-anchor and (b) DROPS recovered frames while the gate is armed. This
// pins both: a recovered frame while armed is dropped and seats nothing, and the subsequent
// low init re-anchors cleanly with no stale delivery and no high re-pin.
func TestObserveRecoveredDroppedWhileGateArmed(t *testing.T) {
	clk := newFakeClock()
	const window = 64
	r := reseq.New(window, time.Second, clk)

	// Busy prior boot: next == 200, well past one window.
	const priorHi = 200
	for s := uint64(0); s < priorHi; s++ {
		r.Observe(s, payloadOf(s), testSrc)
	}
	_ = drain(r)
	r.RebaselineToLow() // arms pendingLow at 200

	// A parity-recovered OLD-boot frame inside [anchor, anchor+window) arrives while armed.
	// It must be DROPPED (not placed) and must seat NOTHING in the ring.
	const staleRecovered = priorHi + 5
	if placed := r.ObserveRecovered(staleRecovered, payloadOf(staleRecovered), testSrc); placed {
		t.Fatal("recovered old-boot frame was PLACED while the low-anchor gate was armed")
	}
	if b := r.Buffered(); b != 0 {
		t.Fatalf("recovered-while-armed seated %d cells; want 0 (gate must drop it)", b)
	}

	// The genuine restarted low init now re-anchors. Delivery must be the NEW-boot stream
	// from seq 1 — never the stale recovered frame, and next must NOT have jumped high.
	r.Observe(1, payloadOf(1), testSrc)
	r.Observe(2, payloadOf(2), testSrc)
	got := drain(r)
	if !equalSeqs(got, []uint64{1, 2}) {
		t.Fatalf("after a recovered-while-armed interleave the restart delivered %v, want [1 2] (stale re-pin/deliver)", got)
	}
}
