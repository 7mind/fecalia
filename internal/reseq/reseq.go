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
//     (treated as lost) and the run released, rather than stalling forever.
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

	// Diagnostics (read via the accessors; useful for the bounded-memory asserts).
	highWater int    // max occupied slots ever held
	dropDup   uint64 // frames dropped as duplicates
	dropLate  uint64 // frames dropped as already-past the release point
	skipped   uint64 // seqs skipped (treated lost) by window-advance or timeout
	releasedN uint64 // frames released for delivery
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

	if seq < r.next {
		// Already released or skipped past: an old frame or a late duplicate.
		r.dropLate++
		return
	}
	if seq >= r.next+r.window {
		// Beyond the window: advance the release point so the frame fits at the
		// top of the window, releasing any buffered frames below the new base in
		// order and skipping the (assumed lost) gaps. This is what bounds memory.
		r.advanceTo(seq - r.window + 1)
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
// ascending order) and counting the empty positions as skipped (lost) seqs.
// Caller holds r.mu; target must be > next.
func (r *Resequencer) advanceTo(target uint64) {
	for r.next < target {
		cell := &r.ring[r.next%r.window]
		if cell.occupied && cell.seq == r.next {
			r.release(cell)
		} else {
			r.skipped++
		}
		r.next++
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
	Released   uint64 // frames released for delivery
	DroppedDup uint64 // frames dropped as duplicates
	DroppedOld uint64 // frames dropped as already-past the release point
	Skipped    uint64 // seqs skipped (lost) by window-advance or timeout
}

// Stats returns a snapshot of the cumulative counters.
func (r *Resequencer) Stats() Stats {
	r.mu.Lock()
	defer r.mu.Unlock()
	return Stats{
		Released:   r.releasedN,
		DroppedDup: r.dropDup,
		DroppedOld: r.dropLate,
		Skipped:    r.skipped,
	}
}
