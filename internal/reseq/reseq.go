package reseq

import (
	"net/netip"
	"sync"
	"time"
)

// Clock is the injected time source. Production wires a monotonic wall clock
// (SystemClock); tests wire a hand-advanced fake so ordering, window, and
// timeout behaviour are deterministic with no real sleeps. It is intentionally
// structurally identical to telemetry.Clock so the same fake can drive both.
type Clock interface {
	Now() time.Time
}

// SystemClock is the production Clock backed by the monotonic wall clock.
type SystemClock struct{}

// Now returns the current monotonic time.
func (SystemClock) Now() time.Time { return time.Now() }

// Discontinuity/resync guard tuning. The resequencer does not assume its caller
// authenticated the sequence it is handed, and a genuine peer PROCESS RESTART
// resets the peer's sequence counter to 1. Neither may be trusted to move the
// release point arbitrarily.
const (
	// resyncFactor (K) bounds how far AHEAD of the release point a single
	// unauthenticated frame is trusted to advance the window as a plausible loss
	// burst. Under bounded cross-path reorder a legitimate frame sits within one
	// window of next; a forward jump up to K windows is still treated as a loss
	// burst (the sound bounded-memory window-advance handles it in O(window)), but
	// a jump of K windows or more is SUSPECT: a single such frame never advances
	// next. K=4 leaves generous headroom above the 1-window reorder bound while
	// capping the release point a single trusted frame can skip to (K-1) windows.
	resyncFactor = 4

	// resyncCorroborate (C) is how many out-of-band frames carrying DISTINCT seqs
	// that mutually span less than one window must be observed before the release
	// point is re-pinned. A genuine peer restart (1,2,3,...) or a consistent
	// long-outage forward jump emits connected, distinct seqs that trivially
	// corroborate. A repeated identical seq does NOT advance the count: a single
	// junk or forged datagram re-delivered — a network duplicate, or a forger
	// replaying one datagram — contributes only ONE distinct seq, so it can never
	// self-corroborate. Corroboration therefore requires C INDEPENDENT junk seqs to
	// land mutually within one window; each is independent in 2^64, so that occurs
	// with probability ~(window/2^64)^(C-1) ~ 1e-32 for C=3 and window=2048 — junk
	// never triggers a resync, while a real discontinuity resyncs after losing at
	// most C-1 frames. Any in-band (near-current) frame resets the run, so ordinary
	// traffic interspersed with rare junk never accumulates a false corroboration.
	resyncCorroborate = 3
)

// holdBoundFloor is the smallest dynamic per-gap hold SetHoldBound will accept
// (T241, D93). Even an ultra-low-RTT multi-path bond can reorder by a few
// milliseconds of scheduler/queue jitter that the path RTT does not capture, so
// the installed bound is never allowed to shrink the reorder cushion below
// this; the construction timeout remains the upper cap.
const holdBoundFloor = 10 * time.Millisecond

// Item is one released inner datagram plus the outer source address of the
// frame that carried it. The source travels with the payload so the multipath
// Bind can pin its single virtual endpoint to the FIRST delivered frame's
// source even when that frame was buffered and released out of arrival order.
type Item struct {
	Payload []byte
	Src     netip.AddrPort
}

// slot is one ring cell. A live cell (occupied) holds exactly one buffered
// datagram whose sequence maps to this index; seq is retained so a stale cell (an
// already-drained seq that shares the index modulo window) is never mistaken for
// a live one.
type slot struct {
	seq        uint64
	src        netip.AddrPort
	payload    []byte
	observedAt time.Time
	occupied   bool
}

// Resequencer is a bounded-window, timeout-based receive resequencing buffer. It
// consumes datagrams tagged with the bonding transport's OWN outer sequence
// number (never WireGuard's inner counter) arriving out of
// order across paths, and releases the decoded inner datagrams in strictly
// ascending outer-seq order before they reach the WireGuard engine. This absorbs
// multipath reorder entirely in the outer layer so WG's RFC 6479 anti-replay
// filter (window 8128, see docs/p0-findings.md §6) only ever sees monotonically
// increasing inner counters and never drops a legitimately-delivered packet.
//
// Guarantees (see the package tests):
//   - Ordering: released frames are strictly ascending in outer-seq. A frame
//     whose seq is below the release point is dropped, never delivered late, so
//     delivery is monotonic and WG sees no replay-window regression.
//   - Exactly-once: duplicates (same seq re-observed before or after release) are
//     dropped; every deliverable frame is released exactly once.
//   - Bounded memory: at most `window` frames are buffered. A frame at or beyond
//     next+window forces the window to advance (releasing/skipping the tail),
//     so an adversarial reorder/loss trace cannot grow the buffer without bound.
//   - Timeout progress: when a seq is missing, buffered successors are held at
//     most `timeout` from their first observation; then the gap is skipped
//     (treated as lost) and the run released, rather than stalling forever.
//     Independently observed later gaps retain their remaining recovery time,
//     but do not start a fresh timeout merely because an earlier gap hid them.
//   - Bounded compute: every Observe runs in O(window). No frame — however wild
//     its (unauthenticated, possibly forged) seq — can make the release point
//     advance one seq at a time across a gap of up to 2^64 under the mutex.
//   - Discontinuity resilience: an unauthenticated frame whose seq lies farther
//     than resyncFactor*window from next never moves the release point on its own.
//     A genuine peer restart or long outage — a run of frames carrying DISTINCT
//     seqs that mutually fall within one window — re-pins (resyncs) the release
//     point within a bounded number of frames; uniformly-random junk seqs, and a
//     single junk/forged seq re-delivered any number of times, do not.
//
// Concurrency: every method is guarded by one mutex, so the shared instance is
// safe for the multipath Bind's per-path receive goroutines. The mutex is held
// only for in-memory bookkeeping — never across a syscall — so it does not
// perturb the Bind's lock-free virtual-endpoint fast path or its
// syscall-outside-mutex send discipline.
type Resequencer struct {
	window  uint64
	timeout time.Duration
	clock   Clock

	mu      sync.Mutex
	started bool   // false until the first Observe or RebaselineAt pins the release point
	next    uint64 // lowest outer-seq not yet released
	ring    []slot // len == window; indexed by seq % window
	buf     int    // number of occupied slots

	ready    []Item // FIFO of released items awaiting Pop
	readyPos int    // read cursor into ready (front of the FIFO)

	// held lists, from heldPos on and in order of arrival, the sequences that
	// were buffered behind a gap. The first of them still buffered was observed
	// before every other buffered frame: a hold's deadline runs from it. Those
	// released since are passed over when they reach the front.
	held    []uint64
	heldPos int

	// Head-of-line timeout: when next is a gap but frames ahead are buffered,
	// waiting is armed with a deadline measured from when the gap first formed.
	// armedAt records that same arm instant so the hold's elapsed time can be
	// accrued into holdNanos when the hold ends (T242, D93).
	waiting     bool
	deadline    time.Time
	armedAt     time.Time
	armedWindow time.Duration

	// Discontinuity/resync guard: the current run of out-of-band (suspect) frames.
	// resyncSeqs holds the DISTINCT suspect seqs seen in the run (a repeated
	// identical seq does not advance corroboration); [resyncLo, resyncHi] is the
	// run's seq span. When resyncSeqs reaches resyncCorroborate distinct seqs the
	// run is deemed a real discontinuity and the release point re-pins (see
	// tryResync). It holds at most resyncCorroborate seqs, so memory stays bounded.
	resyncSeqs []uint64
	resyncLo   uint64
	resyncHi   uint64

	// Diagnostics (read via the accessors; useful for the bounded-memory asserts).
	highWater   int    // max occupied slots ever held
	dropDup     uint64 // frames dropped as duplicates
	dropLate    uint64 // frames dropped as already-past the release point
	dropSuspect uint64 // out-of-band frames dropped while (not yet) corroborating
	skipped     uint64 // seqs skipped (treated lost) by window-advance or timeout
	releasedN   uint64 // frames released for delivery
	resyncs     uint64 // release-point re-pins after a corroborated discontinuity
	rebaselines uint64 // release-point re-baselines forced by a trusted control event (the peer's epoch changed)

	// HoL-stall / hold accounting (T242). holds counts head-of-line gaps that armed
	// a hold; holdNanos is the cumulative time such gaps spent held before the hold
	// ended (by a timeout skip, a fill, a window advance, or a re-pin), measured via
	// the injected Clock.
	holds           uint64
	holdNanos       uint64
	deadlineWakeups uint64
	gapFills        uint64

	// holdBound is the per-gap hold (T241): the bind installs it via SetHoldBound,
	// already clamped to [holdBoundFloor, timeout]. Zero means "unset" — arm() then
	// uses the full construction timeout.
	holdBound time.Duration

	notify func()
	closed bool
}

// New returns a resequencer buffering at most window outer-seq positions and
// holding a head-of-line-blocked run for at most timeout before skipping the gap.
// window must be positive; timeout may be zero (a missing head seq is skipped as
// soon as the next Observe/Pop advances the clock past the arrival instant).
func New(window uint64, timeout time.Duration, clock Clock) *Resequencer {
	if window == 0 {
		panic("reseq: window must be positive")
	}
	if clock == nil {
		panic("reseq: clock must be non-nil")
	}
	return &Resequencer{
		window:  window,
		timeout: timeout,
		clock:   clock,
		ring:    make([]slot, window),
	}
}

// SetHoldBound installs the per-gap hold (T241, D93). The bound is clamped to
// [holdBoundFloor, timeout] — the construction timeout stays the worst-case cap.
// Safe to call at any cadence.
func (r *Resequencer) SetHoldBound(d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if d < holdBoundFloor {
		d = holdBoundFloor
	}
	if d > r.timeout {
		d = r.timeout
	}
	r.holdBound = d
}

// SetNotifier installs the non-blocking change notification used by the Bind's
// single receive drainer. The callback must not call back into this Resequencer.
func (r *Resequencer) SetNotifier(notify func()) {
	r.mu.Lock()
	r.notify = notify
	r.mu.Unlock()
}

// ArmedDeadline returns the exact deadline of the current head-of-line gap.
func (r *Resequencer) ArmedDeadline() (time.Time, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.waiting {
		return time.Time{}, false
	}
	return r.deadline, r.waiting
}

// Close publishes teardown to any receiver parked on this resequencer.
func (r *Resequencer) Close() {
	r.mu.Lock()
	if !r.closed {
		r.closed = true
		r.endHoldLocked(r.clock.Now())
		r.notifyLocked()
	}
	r.mu.Unlock()
}

// notifyLocked publishes an edge after every deadline-affecting transition.
// Production supplies a non-blocking coalescing channel send.
func (r *Resequencer) notifyLocked() {
	if r.notify != nil {
		r.notify()
	}
}

// effectiveHoldLocked returns the per-gap hold arm() applies. Caller holds r.mu.
func (r *Resequencer) effectiveHoldLocked() time.Duration {
	if r.holdBound == 0 {
		return r.timeout
	}
	return r.holdBound
}

// Observe ingests one inner payload under its outer-seq. It takes ownership of
// payload (the caller must not mutate it afterwards). src is the outer source
// address the frame arrived from. Any frames that become deliverable are appended
// to the ready FIFO for Pop. It returns true only when this outer sequence was
// newly admitted; duplicates, stale frames, and suspect discontinuities return
// false.
func (r *Resequencer) Observe(seq uint64, payload []byte, src netip.AddrPort) bool {
	now := r.clock.Now()
	r.mu.Lock()
	defer r.mu.Unlock()

	if !r.started {
		r.started = true
		r.next = seq
	}

	// Give up on any head-of-line gap whose hold time has elapsed before deciding
	// where this frame lands.
	r.expire(now)

	// Classify the frame against the release window and the discontinuity guard.
	// admit adjusts next (window-advance or resync) when warranted and reports
	// whether the frame now lands inside [next, next+window) and should be placed.
	if !r.admit(seq, now) {
		return false
	}

	cell := &r.ring[seq%r.window]
	if cell.occupied && cell.seq == seq {
		r.dropDup++
		return false
	}
	fillsGap := r.waiting && seq == r.next
	cell.seq = seq
	cell.src = src
	cell.payload = payload
	cell.observedAt = now
	cell.occupied = true
	r.buf++
	if seq != r.next {
		r.held = append(r.held, seq)
	}
	if r.buf > r.highWater {
		r.highWater = r.buf
	}
	if fillsGap {
		r.gapFills++
		r.endHoldLocked(now)
	}

	r.drain()
	r.arm(now)
	return true
}

// admit classifies seq against the release window and the discontinuity guard and
// decides whether the frame should be buffered. It returns true when seq now lies
// inside [next, next+window) and should be placed; false (having bumped the
// appropriate drop counter) when the frame is dropped. It may advance next by a
// bounded amount on a plausible loss burst, or re-pin next on a corroborated
// discontinuity. Caller holds r.mu.
func (r *Resequencer) admit(seq uint64, now time.Time) bool {
	if seq < r.next {
		// Below the release point. A frame more than one window below next is
		// impossibly late under bounded reorder — treat it as SUSPECT (a peer
		// restart resets outerSeq to 1, so its frames land here). A frame within a
		// window below next is an ordinary straggler/duplicate.
		if r.next-seq > r.window {
			if r.tryResync(seq, now) {
				return true
			}
			r.dropSuspect++
			return false
		}
		r.resyncReset() // near-current traffic: not a discontinuity
		r.dropLate++
		return false
	}
	// seq >= next.
	if seq-r.next >= resyncFactor*r.window {
		// Too far ahead to be a plausible loss burst — SUSPECT (garbage-decoded or
		// forged high seq). A single such frame must not advance next.
		if r.tryResync(seq, now) {
			return true
		}
		r.dropSuspect++
		return false
	}
	// In-window, or a moderate (plausible loss-burst) forward jump: legitimate
	// near-current traffic, so any in-progress discontinuity run is broken.
	r.resyncReset()
	if seq-r.next >= r.window {
		// Beyond the window: advance the release point so the frame fits at the top
		// of the window, releasing buffered frames below the new base in order and
		// skipping the (assumed lost) gaps. This is what bounds memory.
		r.endHoldLocked(now)
		r.advanceTo(seq - r.window + 1)
	}
	return true
}

// Pop returns the next in-order released inner datagram, or ok=false when none is
// ready. It also advances the head-of-line timeout, so a receive goroutine that
// only drains still makes timeout progress.
func (r *Resequencer) Pop() (Item, bool) {
	now := r.clock.Now()
	r.mu.Lock()
	defer r.mu.Unlock()

	r.expire(now)

	if r.readyPos >= len(r.ready) {
		return Item{}, false
	}
	it := r.ready[r.readyPos]
	r.ready[r.readyPos] = Item{} // release the payload reference
	r.readyPos++
	if r.readyPos >= len(r.ready) {
		// FIFO drained: reset to reuse the backing array, keeping it bounded.
		r.ready = r.ready[:0]
		r.readyPos = 0
	}
	return it, true
}

// drain releases the contiguous run starting at next into the ready FIFO. Caller
// holds r.mu.
func (r *Resequencer) drain() {
	for {
		cell := &r.ring[r.next%r.window]
		if !cell.occupied || cell.seq != r.next {
			return
		}
		r.release(cell)
		r.next++
	}
}

// advanceTo moves next up to target, releasing any occupied cell it passes (in
// ascending order) and counting the empty positions as skipped (lost) seqs. It is
// O(window), NEVER O(target-next): all occupied cells live in [next, next+window),
// so once next reaches next+window the remaining gap is provably empty and is
// closed by arithmetic rather than iterated. Caller holds r.mu; target must be
// > next.
func (r *Resequencer) advanceTo(target uint64) {
	// Only [next, next+window) can hold occupied cells; iterate at most that far.
	limit := target
	if limit > r.next+r.window {
		limit = r.next + r.window
	}
	for r.next < limit {
		cell := &r.ring[r.next%r.window]
		if cell.occupied && cell.seq == r.next {
			r.release(cell)
		} else {
			r.skipped++
		}
		r.next++
	}
	if r.next < target {
		// The remaining gap [next, target) is entirely empty — no occupied cell can
		// live a full window ahead of the old release point. Close it by arithmetic.
		r.skipped += target - r.next
		r.next = target
	}
}

// release moves one occupied cell's payload into the ready FIFO and clears it.
// Caller holds r.mu.
func (r *Resequencer) release(cell *slot) {
	r.ready = append(r.ready, Item{Payload: cell.payload, Src: cell.src})
	cell.occupied = false
	cell.payload = nil
	cell.observedAt = time.Time{}
	r.buf--
	r.releasedN++
}

// endHoldLocked accrues the elapsed time of the currently-armed head-of-line hold
// into holdNanos and disarms it (T242). It is the SINGLE accounting point for a
// hold ENDING — whether by a timeout skip, a fill, a window advance, or a re-pin —
// so holdNanos stays a faithful sum of every armed hold's
// arm→end lifetime. A no-op when no hold is armed. The now-armedAt guard tolerates a
// non-advancing fake clock (contributes 0) and never adds a negative interval. Caller
// holds r.mu.
func (r *Resequencer) endHoldLocked(now time.Time) {
	if !r.waiting {
		return
	}
	if d := now.Sub(r.armedAt); d > 0 {
		r.holdNanos += uint64(d.Nanoseconds())
	}
	r.waiting = false
	r.armedWindow = 0
	r.notifyLocked()
}

// expire skips a head-of-line gap whose hold time has elapsed by jumping next to
// the smallest buffered seq (treating the intervening seqs as lost) and releasing
// the now-contiguous run. Caller holds r.mu.
func (r *Resequencer) expire(now time.Time) {
	for r.waiting {
		if now.Before(r.deadline) {
			return
		}
		r.deadlineWakeups++
		minSeq, ok := r.smallestBuffered()
		if !ok {
			r.endHoldLocked(now)
			return
		}
		// Skip the lost gap [next, minSeq), then release from minSeq onward.
		if r.next < minSeq {
			r.skipped += minSeq - r.next
			r.next = minSeq
		}
		r.drain()
		r.endHoldLocked(now)
		r.arm(now)
	}
}

// arm (re)evaluates the head-of-line timeout after a change. When next is a gap
// but buffered frames sit ahead of it, it starts the hold clock at the oldest
// still-buffered successor observation. A gap exposed behind an earlier gap
// therefore retains its remaining recovery time instead of receiving a fresh
// full hold. Otherwise arm disarms. Caller holds r.mu.
func (r *Resequencer) arm(now time.Time) {
	cell := &r.ring[r.next%r.window]
	headPresent := cell.occupied && cell.seq == r.next
	if !headPresent && r.buf > 0 {
		if !r.waiting {
			r.waiting = true
			hold := r.effectiveHoldLocked()
			observedAt, ok := r.oldestBufferedObservation()
			if !ok {
				panic("reseq: buffered count has no occupied slot")
			}
			r.deadline = observedAt.Add(hold)
			r.armedAt = now
			r.armedWindow = hold
			r.holds++
			r.notifyLocked()
		}
	} else {
		// The head is now present (the gap was FILLED) or the buffer emptied: accrue
		// this hold's elapsed time and disarm (T242). A no-op when no hold was armed.
		r.endHoldLocked(now)
	}
}

// tryResync feeds one out-of-band (suspect) seq into the discontinuity guard. It
// re-pins the release point ONLY after resyncCorroborate DISTINCT suspect seqs
// whose values mutually span less than one window (a genuine peer restart or long
// outage emits connected, distinct seqs that corroborate; uniformly-random junk
// seqs, each independent in 2^64, do not, and a single junk/forged seq re-delivered
// contributes only ONE distinct value so it can never self-corroborate). It
// returns true and performs the resync when the corroboration threshold is met —
// re-pinning next to the triggering seq so the caller places it as the new head
// and delivery resumes immediately, without a phantom gap or an extra timeout —
// and false while still collecting or when the seq fails to corroborate the
// current run. Caller holds r.mu.
func (r *Resequencer) tryResync(seq uint64, now time.Time) bool {
	if len(r.resyncSeqs) == 0 || r.spanExceeds(seq) {
		// Start (or restart) the run at this seq: it does not corroborate the
		// current run, so it becomes the anchor of a fresh one.
		r.resyncLo, r.resyncHi = seq, seq
		r.resyncSeqs = append(r.resyncSeqs[:0], seq)
		return false
	}
	if r.runContains(seq) {
		// A repeated identical seq does NOT advance corroboration: a lone junk or
		// forged datagram re-delivered must not count as independent corroboration.
		return false
	}
	if seq < r.resyncLo {
		r.resyncLo = seq
	}
	if seq > r.resyncHi {
		r.resyncHi = seq
	}
	r.resyncSeqs = append(r.resyncSeqs, seq)
	if len(r.resyncSeqs) < resyncCorroborate {
		return false
	}
	// Corroborated discontinuity: re-pin to the triggering seq and resume delivery.
	r.resync(seq, now)
	return true
}

// runContains reports whether seq is already one of the distinct suspect seqs in
// the current corroboration run. The run holds at most resyncCorroborate seqs, so
// this scan is O(C). Caller holds r.mu.
func (r *Resequencer) runContains(seq uint64) bool {
	for _, s := range r.resyncSeqs {
		if s == seq {
			return true
		}
	}
	return false
}

// spanExceeds reports whether adding seq to the current corroboration run would
// make its seq span reach or exceed one window (i.e. seq does not mutually fall
// within one window of the run so far). Caller holds r.mu.
func (r *Resequencer) spanExceeds(seq uint64) bool {
	lo, hi := r.resyncLo, r.resyncHi
	if seq < lo {
		lo = seq
	}
	if seq > hi {
		hi = seq
	}
	return hi-lo >= r.window
}

// resync re-pins the release point to base after a confirmed discontinuity,
// discarding all buffered frames (they belong to the pre-discontinuity stream) in
// O(window). The already-released FIFO is untouched — those were legitimate prior
// deliveries. Caller holds r.mu.
func (r *Resequencer) resync(base uint64, now time.Time) {
	for i := range r.ring {
		r.ring[i] = slot{}
	}
	r.buf = 0
	r.held, r.heldPos = r.held[:0], 0
	r.next = base
	r.endHoldLocked(now) // T242: account the abandoned hold's elapsed time, then disarm.
	r.resyncReset()
	r.resyncs++
}

// resyncReset abandons the current corroboration run. Caller holds r.mu.
func (r *Resequencer) resyncReset() {
	r.resyncSeqs = r.resyncSeqs[:0]
	r.resyncLo = 0
	r.resyncHi = 0
}

// RebaselineAt starts a trusted stream at its known first sequence: it discards
// the buffered frames (they belong to the previous stream) in O(window), ends any
// armed hold and pins the release point at next. The bind calls it when the
// peer's epoch changes — a TRUSTED control event, so it bypasses the tryResync
// corroboration guard. The already-released FIFO of prior legitimate deliveries
// is untouched. Takes r.mu; the caller must NOT hold it.
func (r *Resequencer) RebaselineAt(next uint64) {
	if next == 0 {
		panic("reseq: explicit first sequence must be nonzero")
	}
	now := r.clock.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.ring {
		r.ring[i] = slot{}
	}
	r.buf = 0
	r.held, r.heldPos = r.held[:0], 0
	r.started = true
	r.next = next
	r.endHoldLocked(now) // T242: account the discarded hold's elapsed time, then disarm.
	r.resyncReset()
	r.rebaselines++
	r.notifyLocked()
}

// smallestBuffered scans the ring for the lowest occupied seq at or above next.
// All occupied seqs lie in [next, next+window), so this is bounded by window and
// runs only on the (rare) timeout path. Caller holds r.mu.
func (r *Resequencer) smallestBuffered() (uint64, bool) {
	best := uint64(0)
	found := false
	for i := range r.ring {
		c := &r.ring[i]
		if c.occupied && c.seq >= r.next && (!found || c.seq < best) {
			best = c.seq
			found = true
		}
	}
	return best, found
}

// oldestBufferedObservation returns when the receiver first had evidence of any
// gap at or after next: when the earliest arrival still buffered was observed.
//
// It runs every time a hold is armed, and that is on every frame while one
// that came early by a faster lane waits for those sent before it: each of them
// fills the gap at the head and exposes the next. A scan of the window here
// held a receiver to a few thousand datagrams a second
// (`TestGapFillsBehindAnEarlyFrameDoNotScanTheWindow`). Caller holds r.mu.
func (r *Resequencer) oldestBufferedObservation() (time.Time, bool) {
	for ; r.heldPos < len(r.held); r.heldPos++ {
		seq := r.held[r.heldPos]
		if cell := &r.ring[seq%r.window]; cell.occupied && cell.seq == seq && seq >= r.next {
			if r.heldPos > len(r.held)/2 {
				// What was passed over is dropped once it is most of the list,
				// so the list stays within twice what arrived since its front.
				r.held = r.held[:copy(r.held, r.held[r.heldPos:])]
				r.heldPos = 0
			}
			return cell.observedAt, true
		}
	}
	r.held, r.heldPos = r.held[:0], 0
	return time.Time{}, false
}

// Buffered reports the number of frames currently held (not yet released). It is
// always <= window, which the bounded-memory tests assert.
func (r *Resequencer) Buffered() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf
}

// Pending reports the number of released-but-not-yet-popped items in the FIFO.
func (r *Resequencer) Pending() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.ready) - r.readyPos
}

// HighWater reports the maximum number of frames ever buffered simultaneously.
func (r *Resequencer) HighWater() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.highWater
}

// Stats is a snapshot of the resequencer's cumulative counters.
type Stats struct {
	Released       uint64 // frames released for delivery
	DroppedDup     uint64 // frames dropped as duplicates
	DroppedOld     uint64 // frames dropped as already-past the release point
	DroppedSuspect uint64 // out-of-band frames dropped while (not yet) corroborating
	Skipped        uint64 // seqs skipped (lost) by window-advance or timeout
	Resyncs        uint64 // release-point re-pins after a corroborated discontinuity
	Rebaselines    uint64 // release-point re-baselines forced by a trusted control event (the peer's epoch changed)

	// HoL-stall / hold accounting (T242).
	Holds           uint64 // head-of-line gaps that armed a hold
	HoldNanos       uint64 // cumulative nanoseconds gaps spent held before a timeout skip, a fill, a window advance, or a re-pin
	ArmedDeadline   time.Time
	ArmedWindow     time.Duration
	DeadlineWakeups uint64
	GapFills        uint64
}

// Stats returns a snapshot of the cumulative counters.
func (r *Resequencer) Stats() Stats {
	r.mu.Lock()
	defer r.mu.Unlock()
	stats := Stats{
		Released:       r.releasedN,
		DroppedDup:     r.dropDup,
		DroppedOld:     r.dropLate,
		DroppedSuspect: r.dropSuspect,
		Skipped:        r.skipped,
		Resyncs:        r.resyncs,
		Rebaselines:    r.rebaselines,

		Holds:           r.holds,
		HoldNanos:       r.holdNanos,
		DeadlineWakeups: r.deadlineWakeups,
		GapFills:        r.gapFills,
	}
	if r.waiting {
		stats.ArmedDeadline = r.deadline
		stats.ArmedWindow = r.armedWindow
	}
	return stats
}
