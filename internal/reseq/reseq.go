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

// Discontinuity/resync guard tuning. A DATA frame is UNAUTHENTICATED by design
// (internal/frame/frame.go: "DATA/PARITY forgeable by design"), so a garbage
// datagram decoding as KindData (p ~ 1/256) delivers a uniformly-random uint64
// OuterSeq, and a genuine peer PROCESS RESTART resets the peer's outerSeq counter
// to 1. Neither may be trusted to move the release point arbitrarily.
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

	// resyncCorroborate (C) is how many CONSECUTIVE out-of-band frames, whose
	// seqs mutually span less than one window, must be observed before the release
	// point is re-pinned. A genuine peer restart (1,2,3,...) or a consistent
	// long-outage forward jump emits connected seqs that trivially corroborate;
	// uniformly-random junk seqs, each independent in 2^64, corroborate only with
	// probability ~(window/2^64)^(C-1) ~ 1e-32 for C=3 and window=2048 — so junk
	// never triggers a resync, while a real discontinuity resyncs after losing at
	// most C-1 frames. Any in-band (near-current) frame resets the run, so ordinary
	// traffic interspersed with rare junk never accumulates a false corroboration.
	resyncCorroborate = 3
)

// Item is one released inner datagram plus the outer source address of the DATA
// frame that carried it. The source travels with the payload so the multipath
// Bind can pin its single virtual endpoint to the FIRST delivered frame's
// source even when that frame was buffered and released out of arrival order.
type Item struct {
	Payload []byte
	Src     netip.AddrPort
}

// slot is one ring cell. A live cell (occupied) holds exactly one buffered DATA
// frame whose OuterSeq maps to this index; seq is retained so a stale cell (an
// already-drained seq that shares the index modulo window) is never mistaken for
// a live one.
type slot struct {
	seq      uint64
	src      netip.AddrPort
	payload  []byte
	occupied bool
}

// Resequencer is a bounded-window, timeout-based receive resequencing buffer. It
// consumes DATA frames tagged with the multipath codec's OWN outer sequence
// number (frame.Data.OuterSeq — never WireGuard's inner counter) arriving out of
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
//   - Timeout progress: when the head-of-line seq is missing, buffered frames
//     ahead of the gap are held at most `timeout`; then the gap is skipped
//     (treated as lost) and the run released, rather than stalling forever. Each
//     distinct head-of-line gap gets its OWN full timeout.
//   - Bounded compute: every Observe runs in O(window). No frame — however wild
//     its (unauthenticated, possibly forged) seq — can make the release point
//     advance one seq at a time across a gap of up to 2^64 under the mutex.
//   - Discontinuity resilience: an unauthenticated frame whose seq lies farther
//     than resyncFactor*window from next never moves the release point on its own.
//     A genuine peer restart or long outage — a run of consecutive frames whose
//     seqs mutually fall within one window — re-pins (resyncs) the release point
//     within a bounded number of frames; uniformly-random junk seqs do not.
//
// Concurrency: every method is guarded by one mutex, so the shared instance is
// safe for the multipath Bind's per-path receive goroutines. The mutex is held
// only for in-memory bookkeeping — never across a syscall — so it does not
// perturb the Bind's lock-free virtual-endpoint fast path or its
// syscall-outside-mutex send discipline.
//
// FEC seam (T24): Observe is the sole ingestion point and is keyed purely by
// outer-seq. An FEC decoder inserted on the receive path recovers missing DATA
// frames from PARITY and feeds each recovered frame's ORIGINAL outer-seq through
// Observe, identically to a natively-received frame — so FEC recovery slots in
// BEFORE resequencing with no change to this type.
type Resequencer struct {
	window  uint64
	timeout time.Duration
	clock   Clock

	mu      sync.Mutex
	started bool   // false until the first Observe pins the initial next
	next    uint64 // lowest outer-seq not yet released
	ring    []slot // len == window; indexed by seq % window
	buf     int    // number of occupied slots

	ready    []Item // FIFO of released items awaiting Pop
	readyPos int    // read cursor into ready (front of the FIFO)

	// Head-of-line timeout: when next is a gap but frames ahead are buffered,
	// waiting is armed with a deadline measured from when the gap first formed.
	waiting  bool
	deadline time.Time

	// Discontinuity/resync guard: the current run of consecutive out-of-band
	// (suspect) frames. resyncN counts the run; [resyncLo, resyncHi] is its seq
	// span. When resyncN reaches resyncCorroborate the run is deemed a real
	// discontinuity and the release point re-pins (see tryResync).
	resyncN  int
	resyncLo uint64
	resyncHi uint64

	// Diagnostics (read via the accessors; useful for the bounded-memory asserts).
	highWater   int    // max occupied slots ever held
	dropDup     uint64 // frames dropped as duplicates
	dropLate    uint64 // frames dropped as already-past the release point
	dropSuspect uint64 // out-of-band frames dropped while (not yet) corroborating
	skipped     uint64 // seqs skipped (treated lost) by window-advance or timeout
	releasedN   uint64 // frames released for delivery
	resyncs     uint64 // release-point re-pins after a corroborated discontinuity
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

// Observe ingests one DATA frame's inner payload under its outer-seq. It takes
// ownership of payload (the caller must not mutate it afterwards); the multipath
// Bind passes the freshly-decoded frame.Data.Payload, which aliases nothing else.
// src is the outer source address the frame arrived from. Any frames that become
// deliverable are appended to the ready FIFO for Pop.
func (r *Resequencer) Observe(seq uint64, payload []byte, src netip.AddrPort) {
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
	if !r.admit(seq) {
		return
	}

	cell := &r.ring[seq%r.window]
	if cell.occupied && cell.seq == seq {
		r.dropDup++
		return
	}
	cell.seq = seq
	cell.src = src
	cell.payload = payload
	cell.occupied = true
	r.buf++
	if r.buf > r.highWater {
		r.highWater = r.buf
	}

	r.drain()
	r.arm(now)
}

// admit classifies seq against the release window and the discontinuity guard and
// decides whether the frame should be buffered. It returns true when seq now lies
// inside [next, next+window) and should be placed; false (having bumped the
// appropriate drop counter) when the frame is dropped. It may advance next by a
// bounded amount on a plausible loss burst, or re-pin next on a corroborated
// discontinuity. Caller holds r.mu.
func (r *Resequencer) admit(seq uint64) bool {
	if seq < r.next {
		// Below the release point. A frame more than one window below next is
		// impossibly late under bounded reorder — treat it as SUSPECT (a peer
		// restart resets outerSeq to 1, so its frames land here). A frame within a
		// window below next is an ordinary straggler/duplicate.
		if r.next-seq > r.window {
			if r.tryResync(seq) {
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
		if r.tryResync(seq) {
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
	r.buf--
	r.releasedN++
}

// expire skips a head-of-line gap whose hold time has elapsed: it jumps next to
// the smallest buffered seq (treating the intervening seqs as lost) and releases
// the now-contiguous run. Caller holds r.mu.
func (r *Resequencer) expire(now time.Time) {
	if !r.waiting || now.Before(r.deadline) {
		return
	}
	minSeq, ok := r.smallestBuffered()
	if !ok {
		r.waiting = false
		return
	}
	// Skip the lost gap [next, minSeq), then release from minSeq onward.
	for r.next < minSeq {
		r.skipped++
		r.next++
	}
	r.drain()
	// Clear the armed state BEFORE re-arming so that a distinct SECOND gap exposed
	// by this release gets its own full timeout rather than inheriting this gap's
	// already-elapsed deadline (which would skip it with ~zero hold).
	r.waiting = false
	r.arm(now)
}

// arm (re)evaluates the head-of-line timeout after a change. When next is a gap
// but buffered frames sit ahead of it, it starts the hold clock (only if not
// already running, so the deadline measures from when the gap first formed).
// Otherwise it disarms. Caller holds r.mu.
func (r *Resequencer) arm(now time.Time) {
	cell := &r.ring[r.next%r.window]
	headPresent := cell.occupied && cell.seq == r.next
	if !headPresent && r.buf > 0 {
		if !r.waiting {
			r.waiting = true
			r.deadline = now.Add(r.timeout)
		}
	} else {
		r.waiting = false
	}
}

// tryResync feeds one out-of-band (suspect) seq into the discontinuity guard. It
// re-pins the release point ONLY after resyncCorroborate consecutive suspect
// frames whose seqs mutually span less than one window (a genuine peer restart or
// long outage emits connected seqs that corroborate; uniformly-random junk seqs,
// each independent in 2^64, do not). It returns true and performs the resync when
// the corroboration threshold is met — re-pinning next to the triggering seq so
// the caller places it as the new head and delivery resumes immediately, without
// a phantom gap or an extra timeout — and false while still collecting or when the
// seq fails to corroborate the current run. Caller holds r.mu.
func (r *Resequencer) tryResync(seq uint64) bool {
	if r.resyncN == 0 || r.spanExceeds(seq) {
		// Start (or restart) the run at this seq: it does not corroborate the
		// current run, so it becomes the anchor of a fresh one.
		r.resyncLo, r.resyncHi, r.resyncN = seq, seq, 1
		return false
	}
	if seq < r.resyncLo {
		r.resyncLo = seq
	}
	if seq > r.resyncHi {
		r.resyncHi = seq
	}
	r.resyncN++
	if r.resyncN < resyncCorroborate {
		return false
	}
	// Corroborated discontinuity: re-pin to the triggering seq and resume delivery.
	r.resync(seq)
	return true
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
func (r *Resequencer) resync(base uint64) {
	for i := range r.ring {
		r.ring[i] = slot{}
	}
	r.buf = 0
	r.next = base
	r.waiting = false
	r.resyncReset()
	r.resyncs++
}

// resyncReset abandons the current corroboration run. Caller holds r.mu.
func (r *Resequencer) resyncReset() {
	r.resyncN = 0
	r.resyncLo = 0
	r.resyncHi = 0
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
}

// Stats returns a snapshot of the cumulative counters.
func (r *Resequencer) Stats() Stats {
	r.mu.Lock()
	defer r.mu.Unlock()
	return Stats{
		Released:       r.releasedN,
		DroppedDup:     r.dropDup,
		DroppedOld:     r.dropLate,
		DroppedSuspect: r.dropSuspect,
		Skipped:        r.skipped,
		Resyncs:        r.resyncs,
	}
}
