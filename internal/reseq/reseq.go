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

// singleSourceTrailingWindow is the trailing look-back over which the resequencer
// counts DISTINCT delivering-path keys to decide whether immediate release is
// safe (D93). When exactly one delivering path has been observed within this
// window a head-of-line gap is genuine loss (a single path preserves order, so a
// straggler cannot be in flight) and its successors are released with ~0 hold; a
// second distinct key seen within the window re-arms the full timeout hold, and
// the window itself IS the re-arm dwell (immediate release resumes only once a
// whole window has elapsed with a single key).
//
// It is sized to EXCEED the failback straggler horizon so the single→multi
// transition cannot fast-release a straggler: the receive gap hold is capped at
// resequencerTimeout (250 ms, internal/bind/multipath.go), which bounds how long
// a genuine cross-path straggler can trail its head, so a 2x margin (500 ms)
// guarantees that when a second path first appears its key is still inside the
// window that governs any straggler the first path left behind. A larger window
// is strictly more conservative (holds longer); a smaller one risks releasing a
// straggler past its gap at the transition, so 2x is the minimum sound multiple.
const singleSourceTrailingWindow = 2 * (250 * time.Millisecond)

// holdBoundFloor is the smallest dynamic per-gap hold SetHoldBound will accept
// (T241, D93). Even an ultra-low-RTT multi-path bond can reorder by a few
// milliseconds of scheduler/queue jitter that the path RTT does not capture, so
// the RTT-derived bound is never allowed to shrink the reorder cushion below
// this; the construction timeout remains the upper cap.
const holdBoundFloor = 10 * time.Millisecond

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
	// armedAt records that same arm instant so the hold's elapsed time can be
	// accrued into holdNanos when the hold ends (T242, D93).
	waiting  bool
	deadline time.Time
	armedAt  time.Time

	// Discontinuity/resync guard: the current run of out-of-band (suspect) frames.
	// resyncSeqs holds the DISTINCT suspect seqs seen in the run (a repeated
	// identical seq does not advance corroboration); [resyncLo, resyncHi] is the
	// run's seq span. When resyncSeqs reaches resyncCorroborate distinct seqs the
	// run is deemed a real discontinuity and the release point re-pins (see
	// tryResync). It holds at most resyncCorroborate seqs, so memory stays bounded.
	resyncSeqs []uint64
	resyncLo   uint64
	resyncHi   uint64

	// Pending low-anchor re-baseline (peer restart, T119). After RebaselineToLow the
	// release point is UNPINNED but NOT re-anchored on the next arbitrary frame:
	// pendingLow keeps `next` held at pendingLowAnchor (the pre-rebaseline release
	// point) so a stale HIGH-seq straggler still draining from the OLD boot is
	// SUSPECT-dropped, and `next` re-anchors ONLY when a genuine restarted-stream
	// low-seq — a frame more than one window BELOW pendingLowAnchor — arrives. This
	// closes the D36 re-pin race the plain Rebaseline leaves open (plan review R126).
	pendingLow       bool
	pendingLowAnchor uint64

	// pendingLowDrops counts consecutive SUSPECT drops taken while the low-anchor gate
	// is armed. It BOUNDS the gate so it can never permanently blackhole (round-3 FIX 3,
	// review R150): at anchor == window+2 the only in-budget re-anchor frame is seq 1, so
	// if that lone wrapped-init frame is LOST every later new-boot frame fails
	// `anchor - seq > window` and would be SUSPECT-dropped forever. After O(window) such
	// drops the gate FALLS BACK to a plain unpin (started=false), which self-heals via the
	// existing resync-corroboration path. Reset to 0 whenever the gate is (re-)armed or
	// cleared.
	pendingLowDrops uint64

	// pendingSrc / pendingSrcActive arm a SOURCE-IDENTITY gate after a plain Rebaseline (D34,
	// the D32 hub-failover path): the release point re-anchors only on a frame from the EXPECTED
	// new standby hub endpoint, so a stale straggler still draining from the OLD hub (a DIFFERENT
	// source) cannot re-pin `next` to a wrong value before the standby's stream arrives. The
	// ordered failover endpoints are PUBLIC hub addresses (the edge is the NAT'd side, not the
	// hub), so the standby's inbound frames carry src == the endpoint the edge failed over to.
	// Armed only when Rebaseline is given a valid expected source; a zero source leaves it off
	// (re-anchor on the next frame — the pre-D34 trust-next behaviour). Cleared when next re-anchors.
	pendingSrc       netip.AddrPort
	pendingSrcActive bool
	// pendingSrcDrops BOUNDS the source gate the same way pendingLowDrops bounds the low-anchor
	// gate: it counts consecutive wrong-source frames SUSPECT-dropped while the gate is armed, and
	// after O(window) of them (the expected source never arriving — a mis-config or a further
	// failover) the gate FALLS BACK to re-anchoring on the next frame, so it can never permanently
	// blackhole the stream. Reset to 0 whenever the gate is (re-)armed or cleared.
	pendingSrcDrops uint64

	// Diagnostics (read via the accessors; useful for the bounded-memory asserts).
	highWater   int    // max occupied slots ever held
	dropDup     uint64 // frames dropped as duplicates
	dropLate    uint64 // frames dropped as already-past the release point
	dropSuspect uint64 // out-of-band frames dropped while (not yet) corroborating
	skipped     uint64 // seqs skipped (treated lost) by window-advance or timeout
	releasedN   uint64 // frames released for delivery
	resyncs     uint64 // release-point re-pins after a corroborated discontinuity
	rebaselines uint64 // release-point re-baselines forced by a trusted control event (e.g. hub failover, peer restart)

	// HoL-stall / hold accounting (T242, D93) — the observability leg of the D93
	// single-path immediate release. holds counts head-of-line gaps that armed a
	// hold; holdNanos is the cumulative time such gaps spent held before the hold
	// ended (by a timeout skip, a single-path immediate release, a fill, or a
	// discontinuity re-pin), measured via the injected Clock; immediateReleases
	// counts the gaps whose successors were released via the D93 fast path.
	//
	// An immediate release ALSO increments skipped (by the lost gap's seq count):
	// skipped's meaning is UNCHANGED and backward-consistent — total seqs treated as
	// lost, whether by window-advance, timeout, or fast-path skip. The fast-path-vs-
	// timeout distinction is recoverable WITHOUT redefining skipped, because
	// immediateReleases separately counts the fast-path release EVENTS: a rising
	// immediateReleases with skipped means the D93 amplifier is disarmed (single
	// path), whereas skipped rising while immediateReleases stays flat is a genuine
	// timeout HoL stall (the amplifier armed).
	holds             uint64
	holdNanos         uint64
	immediateReleases uint64

	// Single-delivering-path immediate release (D93). A head-of-line gap normally
	// waits the full timeout in case a straggler reordered across paths fills it.
	// When the frames are provably arriving over ONE delivering path, a gap is
	// genuine loss (that path preserves order) and its successors may release with
	// ~0 hold. These fields track the trailing distinct-path evidence that gates it.
	//
	// pathKnown is ORTHOGONAL to any particular pathKey value (R251): the legacy
	// Observe ingests with UNKNOWN identity and forces the conservative full hold,
	// while ObserveFromPath supplies a known, OPAQUE delivering-path key — and a
	// real path may legitimately compose to key 0, so "unknown" must never be
	// conflated with key == 0. It reflects the identity of the most recent ingest.
	// holdBound is the DYNAMIC per-gap hold (T241, D93): the bind derives it from
	// the measured path RTT (k*SRTT) and installs it via SetHoldBound, already
	// clamped to [holdBoundFloor, timeout]. Zero means "unset" — arm() then uses
	// the full construction timeout, so behaviour is byte-identical until the
	// bind wires a bound in. While fecActive the bound is IGNORED and the full
	// timeout applies: a parity reconstruction may still fill the gap, and an
	// RTT-shortened skip would advance the release point past the recovery
	// (dropping the recovered frame as late), neutralizing FEC.
	holdBound time.Duration

	fecActive bool // SetFECActive: FEC on ⇒ suppress immediate release (a parity repair may still fill a gap)
	// multiPathExpected (SetMultiPathExpected): the sender runs an aggregating (weighted)
	// scheduler — suppress immediate release entirely (see SetMultiPathExpected for the
	// retained-pending-measurement rationale, defect D95, tasks:T293 branch 4).
	multiPathExpected bool
	pathKnown         bool      // the most recent ingest carried a known delivering-path identity (ObserveFromPath); legacy Observe ⇒ false
	trailKey          uint32    // the most recently observed known pathKey (the current single-path candidate)
	trailKeyValid     bool      // trailKey holds a value (at least one known observation since the last reset)
	multiKeyUntil     time.Time // immediate release stays suppressed until this instant (a second distinct key, or a rebaseline, was seen within the trailing window)
	multiKeyValid     bool      // multiKeyUntil holds a live suppression deadline
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

// Observe ingests one DATA frame's inner payload under its outer-seq with UNKNOWN
// delivering-path identity — the conservative legacy ingest. An unknown identity
// never enables single-path immediate release, so every existing call site and
// test behaves EXACTLY as before D93: a head-of-line gap always waits the full
// timeout. It is a thin wrapper over ingest; ObserveFromPath is the new primary
// ingest that carries a known delivering-path key.
//
// It takes ownership of payload (the caller must not mutate it afterwards); the
// multipath Bind passes the freshly-decoded frame.Data.Payload, which aliases
// nothing else. src is the outer source address the frame arrived from. Any
// frames that become deliverable are appended to the ready FIFO for Pop.
func (r *Resequencer) Observe(seq uint64, payload []byte, src netip.AddrPort) {
	r.ingest(seq, payload, src, false, 0)
}

// ObserveFromPath ingests one DATA frame carrying an OPAQUE delivering-path
// discriminator pathKey (D93). reseq imposes NO structure on pathKey: any distinct
// value is a distinct delivering path (the bind composes it from the local
// receiving-path id and the sender-stamped frame PathID). When frames arrive over
// a single delivering path a head-of-line gap is genuine loss and its successors
// release with ~0 hold; a second distinct key re-arms the full hold. Semantics are
// otherwise identical to Observe. A real path may compose to key 0, so key 0 is a
// perfectly valid KNOWN path — distinct from Observe's unknown identity.
func (r *Resequencer) ObserveFromPath(seq uint64, payload []byte, src netip.AddrPort, pathKey uint32) {
	r.ingest(seq, payload, src, true, pathKey)
}

// SetFECActive toggles whether an FEC decoder is repairing this stream. While FEC
// is active a head-of-line gap may still be filled by a parity-reconstructed
// frame (ObserveRecovered), so immediate release is suppressed entirely and the
// full hold gives recovery time to land. The bind calls this at FEC setup/teardown.
func (r *Resequencer) SetFECActive(active bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fecActive = active
}

// SetMultiPathExpected marks this stream as one whose sender runs an AGGREGATING
// (weighted) scheduler, suppressing single-path immediate release entirely (D93 follow-up;
// the o3 TestP2Aggregation regression). Immediate release is a SINGLE-PATH optimization,
// and this suppression is RETAINED PENDING a link-bound-venue A/B — unmeasured,
// default-under-uncertainty (defect D95, decisions:K35, tasks:T293 branch 4), NOT the
// earlier burstiness-coupling theory (that gate-engagement narrative is superseded by the
// frame-accurate offered-load fix, decisions:K35 §2). Whether the resequencer's reordering
// buffer is load-bearing for genuine two-path striping on a weighted bond was left
// unanswered: the only available measurement venue could not be caught link-bound in both
// arms of the suppression A/B, so no controlled comparison exists either way. Removing this
// suppression is authorised only by a link-bound-venue A/B where BOTH arms are link-bound
// (a beefier host, or the real two-host setup) that shows it safe to remove.
// Active-backup — the D93 field target — never sets this and keeps the full fast path.
func (r *Resequencer) SetMultiPathExpected(expected bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.multiPathExpected = expected
}

// SetHoldBound installs the dynamic per-gap hold (T241, D93): the bind derives it
// from the measured delivering-path RTT (k*SRTT) so a low-RTT bond pays only a
// small multiple of its real reorder horizon per genuine multi-path gap instead
// of the fixed construction timeout. The bound is clamped to
// [holdBoundFloor, timeout] — the construction timeout stays the worst-case cap —
// and is IGNORED while FEC is active (see holdBound). Safe to call at any cadence
// from the bind's telemetry loop.
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

// effectiveHoldLocked returns the per-gap hold arm() applies: the full
// construction timeout when FEC is active or no dynamic bound is installed,
// otherwise the clamped RTT-derived bound. Caller holds r.mu.
func (r *Resequencer) effectiveHoldLocked() time.Duration {
	if r.fecActive || r.holdBound == 0 {
		return r.timeout
	}
	return r.holdBound
}

// ingest is the shared body of Observe (known == false, unknown identity) and
// ObserveFromPath (known == true, carrying pathKey). It records the trailing
// delivering-path evidence before classifying the frame so the head-of-line
// timeout logic sees the up-to-date single-vs-multi-path state.
func (r *Resequencer) ingest(seq uint64, payload []byte, src netip.AddrPort, known bool, pathKey uint32) {
	now := r.clock.Now()
	r.mu.Lock()
	defer r.mu.Unlock()

	// Record the delivering-path identity FIRST, so a second distinct key arriving
	// with this frame re-arms the full hold before expire() decides the gap below.
	if known {
		r.trackPathKey(pathKey, now)
	} else {
		r.pathKnown = false
	}

	if !r.started {
		if r.pendingSrcActive && src != r.pendingSrc {
			// D34: a hub-failover Rebaseline armed the source-identity gate — re-anchor only on
			// a frame from the expected new standby hub, so a stale old-hub straggler (a
			// different source) cannot re-pin the release point. Bounded self-heal: after
			// O(window) wrong-source frames the expected source is presumed gone (mis-config or
			// a further failover) and we re-anchor on the next frame, so the gate never
			// blackholes the stream.
			r.pendingSrcDrops++
			if r.pendingSrcDrops <= r.window {
				r.dropSuspect++
				return
			}
		}
		r.started = true
		r.next = seq
		r.pendingSrcActive = false
		r.pendingSrcDrops = 0
	}

	// Give up on any head-of-line gap whose hold time has elapsed before deciding
	// where this frame lands.
	r.expire(now)

	// Classify the frame against the release window and the discontinuity guard.
	// admit adjusts next (window-advance or resync) when warranted and reports
	// whether the frame now lands inside [next, next+window) and should be placed.
	if !r.admit(seq, now) {
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

// ObserveRecovered ingests a payload the FEC decoder reconstructed from parity (T24).
// Unlike Observe it NEVER moves or re-pins the release point: a recovered frame is a
// REPAIR for a past gap, so if it has already fallen below the release point it is
// dropped as too-late, and it can neither window-advance nor corroborate a resync — so
// FEC recovery can never CAUSE the burst loss it exists to prevent (a late batch of K
// recovered seqs below `next` must not be able to dump the live buffer via a backward
// resync). It returns whether the frame was PLACED (landed at or above the release
// point and will be delivered in order); a dropped-late recovered frame returns false,
// which the datapath uses to keep the /metrics recovered counter honest — counting
// frames actually delivered ahead of the release point, not merely reconstructed.
func (r *Resequencer) ObserveRecovered(seq uint64, payload []byte, src netip.AddrPort) bool {
	now := r.clock.Now()
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.pendingLow {
		// A low-anchor re-baseline (peer restart) is armed: a parity-RECOVERED frame is
		// by definition part of the PRE-restart stream — the gate is up precisely because
		// a restart re-anchor is pending, and a frame recoverable from the old boot's
		// parity belongs to that old boot. Seating it would place a stale old-boot cell in
		// [anchor, anchor+window) that keeps the head-of-line timeout live and lets
		// expire() jump `next` high past the restarted stream (round-3 FIX 4b, review
		// R150). ObserveRecovered otherwise bypasses admit entirely, so without this it
		// never consults the gate. Drop it — FEC recovery must not seat state while the
		// gate arbitrates the restart. Returns false so the /metrics recovered counter
		// stays honest (nothing was delivered).
		r.dropSuspect++
		return false
	}

	if !r.started {
		// D64: a recovered (parity-reconstructed) frame must NEVER establish or re-pin the
		// release point — that is Observe's job, and this method's documented contract is that
		// it "NEVER moves or re-pins the release point". Before the first LIVE Observe (a fresh
		// ring, or a plain Rebaseline that cleared `started` without arming the pendingLow
		// gate), seating a recovered frame here would re-pin `next` to a repaired PAST seq and
		// dump the live buffer. Drop it as too-early; a live Observe pins `next` normally.
		r.dropSuspect++
		return false
	}
	r.expire(now)

	// The placement window is EXACTLY [next, next+window). A recovered frame below next
	// is too late (drop, and — crucially — never feed the resync guard). One at or beyond
	// next+window would require a window-advance we refuse to perform for a reconstructed
	// frame, since that would skip live seqs on the strength of a repair. Neither branch
	// touches next or the corroboration run.
	if seq < r.next {
		if r.next-seq > r.window {
			r.dropSuspect++
		} else {
			r.dropLate++
		}
		return false
	}
	if seq-r.next >= r.window {
		r.dropLate++
		return false
	}

	cell := &r.ring[seq%r.window]
	if cell.occupied && cell.seq == seq {
		r.dropDup++
		return false // already buffered (a received frame or a prior recovery)
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
	return true
}

// admit classifies seq against the release window and the discontinuity guard and
// decides whether the frame should be buffered. It returns true when seq now lies
// inside [next, next+window) and should be placed; false (having bumped the
// appropriate drop counter) when the frame is dropped. It may advance next by a
// bounded amount on a plausible loss burst, or re-pin next on a corroborated
// discontinuity. Caller holds r.mu.
func (r *Resequencer) admit(seq uint64, now time.Time) bool {
	if r.pendingLow {
		// A low-anchor re-baseline (peer restart, T119) is in effect: re-anchor the
		// release point ONLY on the genuine restarted-stream low-seq — a frame more
		// than one window BELOW the pre-rebaseline release point. A stale HIGH-seq
		// straggler still draining from the OLD boot (at or near the old release
		// point) is an ordinary SUSPECT-drop and must NOT re-pin `next` high, so
		// recovery is not blocked once-per-epoch (defect D36 re-pin race). The
		// seq < anchor guard is load-bearing: without it a straggler at or above the
		// anchor would underflow the unsigned subtraction and spuriously re-anchor.
		if seq < r.pendingLowAnchor && r.pendingLowAnchor-seq > r.window {
			r.pendingLow = false
			r.pendingLowDrops = 0
			r.resyncReset()
			// CLEAR the ring/buf on re-anchor (mirror resync, round-3 FIX 4a, review
			// R150): a stale occupied cell left from the OLD boot — e.g. a parity-
			// recovered old-boot frame seated in [anchor, anchor+window) — must NOT
			// survive the re-anchor to next=seq. If it did, its cell would keep the
			// head-of-line timeout live and expire() would jump `next` high past the
			// restarted stream, suspect-dropping the new boot and delivering the stale
			// frame; a surviving occupied cell also corrupts buf accounting on the next
			// same-index placement (occupied-cell overwrite still does buf++).
			for i := range r.ring {
				r.ring[i] = slot{}
			}
			r.buf = 0
			r.waiting = false
			r.next = seq
			return true
		}
		// BOUND the gate so a LOST re-anchor init frame can never permanently blackhole
		// the restarted stream (round-3 FIX 3, review R150). After O(window) consecutive
		// pendingLow suspect-drops, FALL BACK to a plain unpin (started=false): the next
		// Observe re-anchors `next` and the existing resync-corroboration guard self-heals
		// thereafter, exactly as the D32 hub-failover Rebaseline does — bounded loss
		// instead of a permanent blackhole.
		r.pendingLowDrops++
		if r.pendingLowDrops > r.window {
			r.pendingLow = false
			r.pendingLowDrops = 0
			r.started = false
			r.resyncReset()
		}
		r.dropSuspect++
		return false
	}
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

// singleSourceImmediate reports whether a head-of-line gap may be released with
// ~0 hold instead of waiting the full timeout (D93). It holds ONLY when the frames
// are provably arriving over ONE delivering path — a known identity (not the legacy
// unknown ingest), FEC not active (a parity repair could still fill the gap), and
// exactly one distinct pathKey observed within the trailing window (no second key
// re-armed the hold and no rebaseline cold-start is in force). Over a single path a
// gap is genuine loss, not reorder, so releasing its successors immediately is
// sound; every other case degrades to the conservative full hold, never unsafe.
// Caller holds r.mu.
func (r *Resequencer) singleSourceImmediate(now time.Time) bool {
	if r.fecActive || r.multiPathExpected || !r.pathKnown || !r.trailKeyValid {
		return false
	}
	if r.multiKeyValid && now.Before(r.multiKeyUntil) {
		return false
	}
	return true
}

// endHoldLocked accrues the elapsed time of the currently-armed head-of-line hold
// into holdNanos and disarms it (T242, D93). It is the SINGLE accounting point for a
// hold ENDING — whether by a timeout skip, a single-path immediate release, a fill,
// or a discontinuity re-pin — so holdNanos stays a faithful sum of every armed hold's
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
}

// expire skips a head-of-line gap whose hold time has elapsed — or, over a single
// delivering path, whose successors may release immediately (D93) — by jumping next
// to the smallest buffered seq (treating the intervening seqs as lost) and releasing
// the now-contiguous run. Caller holds r.mu.
func (r *Resequencer) expire(now time.Time) {
	if !r.waiting {
		return
	}
	// A gap proceeds either because its hold elapsed (a timeout skip) OR — while the
	// deadline is still in the future — because the single-delivering-path fast path
	// permits an immediate release (D93). A deadline that has already elapsed is a
	// timeout skip even if the fast path would also apply, so immediate is set ONLY on
	// the pre-deadline fast-path branch.
	immediate := false
	if now.Before(r.deadline) {
		if !r.singleSourceImmediate(now) {
			return
		}
		immediate = true
	}
	minSeq, ok := r.smallestBuffered()
	if !ok {
		r.endHoldLocked(now)
		return
	}
	// Skip the lost gap [next, minSeq), then release from minSeq onward.
	for r.next < minSeq {
		r.skipped++
		r.next++
	}
	r.drain()
	if immediate {
		// D93 fast-path release EVENT (distinct from a timeout skip; NOT folded into
		// skipped — see the counter comments). skipped above still counted the lost
		// gap's seqs, keeping that counter's meaning unchanged.
		r.immediateReleases++
	}
	// Accrue the hold's elapsed time and clear the armed state BEFORE re-arming, so a
	// distinct SECOND gap exposed by this release gets its own fresh hold (its own
	// holds++/armedAt/full deadline) rather than inheriting this gap's already-elapsed
	// deadline (which would skip it with ~zero hold).
	r.endHoldLocked(now)
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
			r.deadline = now.Add(r.effectiveHoldLocked())
			r.armedAt = now
			r.holds++
		}
	} else {
		// The head is now present (the gap was FILLED) or the buffer emptied: accrue
		// this hold's elapsed time and disarm (T242). A no-op when no hold was armed.
		r.endHoldLocked(now)
	}
}

// trackPathKey folds one known delivering-path key into the trailing single-path
// evidence (D93). The first key after a reset becomes the current single-path
// candidate; a key DIFFERENT from the candidate re-arms the full hold for a whole
// trailing window from now (the window IS the re-arm dwell) and adopts the new key
// as the candidate, so a return to a single key re-enables immediate release only
// once the window elapses with no further foreign key. Sustained interleave keeps
// pushing the deadline forward, so immediate release never flaps on. A garbled or
// spoofed sender PathID can only ADD distinct keys, which only ever FORCES the
// conservative full hold — DoS-neutral, never unsafe. Caller holds r.mu.
func (r *Resequencer) trackPathKey(key uint32, now time.Time) {
	r.pathKnown = true
	if !r.trailKeyValid {
		r.trailKey = key
		r.trailKeyValid = true
		return
	}
	if key != r.trailKey {
		r.multiKeyUntil = now.Add(singleSourceTrailingWindow)
		r.multiKeyValid = true
		r.trailKey = key
	}
}

// resetPathTracking clears the trailing single-path evidence after a rebaseline
// (hub failover / peer restart) and holds immediate release suppressed for a full
// trailing window from now (D93). A control-event rebaseline is exactly when the
// delivering topology is in flux and reorder is likeliest, so the stream must
// re-establish single-path confidence before the fast path re-engages rather than
// inheriting the pre-rebaseline candidate. Caller holds r.mu.
func (r *Resequencer) resetPathTracking(now time.Time) {
	r.pathKnown = false
	r.trailKeyValid = false
	r.trailKey = 0
	r.multiKeyUntil = now.Add(singleSourceTrailingWindow)
	r.multiKeyValid = true
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

// Rebaseline discards the buffered (pre-switch) frames and UNPINS the release
// point so the NEXT Observe re-anchors `next` to that frame's outer-seq, exactly as
// if the stream had just started. It exists for the edge-side HUB FAILOVER (T57): a
// concentrator switch changes the DATA-frame SENDER, and the standby is a separate
// process whose outer-seq restarts near 1 — far below the release point the prior
// hub's high-rate stream advanced `next` to. The unauthenticated tryResync path
// cannot rescue this in time: it needs resyncCorroborate (3) distinct low seqs
// within one window, but a freshly re-handshaking standby emits only ~1 DATA frame
// (the handshake response) per RekeyTimeout, so corroboration falls outside the
// failover window and the response is dropped as SUSPECT — the tunnel never
// re-establishes (defect D32). Unlike a DATA frame (forgeable by design, hence the
// corroboration guard), THIS re-pin is driven by a TRUSTED control event — the
// operator-configured ordered-endpoint switch, above the unauthenticated wire — so
// re-anchoring on the next single frame is sound. Buffered frames (from the dead
// prior hub) are discarded in O(window); the already-released FIFO of prior
// legitimate deliveries is untouched. Idempotent and safe before the first Observe
// (started stays false). Takes r.mu; the caller must NOT hold it.
func (r *Resequencer) Rebaseline(expectedSrc netip.AddrPort) {
	now := r.clock.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.resetPathTracking(now) // D93: a rebaseline resets the trailing single-path evidence.
	for i := range r.ring {
		r.ring[i] = slot{}
	}
	r.buf = 0
	r.started = false
	r.endHoldLocked(now) // T242: account the discarded hold's elapsed time, then disarm.
	// A full unpin SUPERSEDES any pending low-anchor re-baseline (T119). If a peer
	// restart had armed pendingLow (via RebaselineToLow) and the restarted low-seq
	// init had not yet re-anchored `next`, a subsequent D32 hub failover must NOT
	// leave the stale pendingLow gate in force: admit would keep classifying every
	// fail-back frame against the now-stale pendingLowAnchor and blackhole the
	// standby's stream, violating this method's documented "next Observe re-anchors
	// next" postcondition. Clearing it restores the plain unpin-and-trust-next path.
	r.pendingLow = false
	r.pendingLowDrops = 0
	// Arm the D34 source-identity gate when the caller supplies the expected new hub endpoint:
	// the next re-anchor accepts only a frame from that source (a stale old-hub straggler is
	// SUSPECT-dropped, bounded by pendingSrcDrops). A zero/unknown expected source leaves the gate
	// off — re-anchor on the next frame, the pre-D34 behaviour idempotence/metrics callers rely on.
	r.pendingSrc = expectedSrc
	r.pendingSrcActive = expectedSrc.IsValid()
	r.pendingSrcDrops = 0
	r.resyncReset()
	r.rebaselines++
}

// RebaselineToLow re-baselines the release point for a PEER RESTART (T119) using a
// LOW-ANCHOR rule the plain Rebaseline lacks. Like Rebaseline it discards the
// buffered pre-restart frames in O(window) and leaves the already-released FIFO
// untouched, but instead of unpinning `next` and trusting the NEXT frame to
// re-anchor, it PINS a pending low-anchor at the current release point: `next`
// re-anchors ONLY when a frame more than one window BELOW that point arrives — the
// genuine restarted-stream low-seq (the wrapped WG init, outer-seq ~1). Any stale
// HIGH-seq straggler still draining from the OLD boot lands at or near the old
// release point and is SUSPECT-dropped, so it can never re-pin `next` high and
// block recovery (the D36 re-pin race under the saturation precondition that a
// plain Rebaseline — which re-anchors on the first frame, high straggler included —
// cannot survive; plan review R126). Idempotent across repeated restart signals: a
// second call while a re-anchor is still pending keeps the ORIGINAL (high) anchor.
// Safe before the first Observe (an UNSTARTED ring needs no re-anchor — the first
// Observe pins next normally).
//
// The low-anchor gate is armed ONLY when the pre-rebaseline release point is high
// enough for it to be satisfiable: the re-anchor predicate is
// `seq < pendingLowAnchor && pendingLowAnchor - seq > window`, and the restarted
// sender's first DATA is outer-seq ~1, so it can only re-anchor when
// pendingLowAnchor >= window+2 (i.e. next > window+1). For a SMALL anchor
// (next <= window+1) the predicate is UNSATISFIABLE for any low seq — nothing would
// ever clear pendingLow and every subsequent frame would be SUSPECT-dropped forever
// (a permanent blackhole; the pre-T119 code self-heals here via resync corroboration,
// so pinning would be a regression). This is the realistic light-traffic / early-
// restart / crash-loop case. So when the anchor is small we fall back to a PLAIN
// unpin (started=false), re-anchoring on the next frame exactly as the D32
// Rebaseline does — bounded, self-healing loss instead of a blackhole. Takes r.mu;
// the caller must NOT hold it.
//
// Even a correctly-armed gate (anchor >= window+2) is BOUNDED so LOSS cannot blackhole
// it (round-3 FIX 3): the sole in-budget re-anchor frame at anchor == window+2 is
// outer-seq 1, and if that lone init frame is lost every later new-boot frame fails the
// predicate; admit therefore counts consecutive pending-low suspect-drops and, after
// O(window) of them, falls back to a plain unpin that self-heals via resync corroboration.
func (r *Resequencer) RebaselineToLow() {
	now := r.clock.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.resetPathTracking(now) // D93: a rebaseline resets the trailing single-path evidence.
	for i := range r.ring {
		r.ring[i] = slot{}
	}
	r.buf = 0
	r.endHoldLocked(now) // T242: account the discarded hold's elapsed time, then disarm.
	// A peer-restart low-anchor re-baseline SUPERSEDES any pending hub-failover source gate
	// (D34): the low-anchor gate below arbitrates the re-anchor now, so a stale pendingSrc must
	// not also gate it.
	r.pendingSrc = netip.AddrPort{}
	r.pendingSrcActive = false
	r.pendingSrcDrops = 0
	r.resyncReset()
	r.rebaselines++
	switch {
	case r.pendingLow:
		// Already pending from an earlier restart signal this epoch: keep the ORIGINAL
		// (high) anchor — idempotent under repeated restart signals for the same epoch.
	case r.started && r.next > r.window+1:
		// The release point is high enough for the low-anchor gate to be satisfiable:
		// hold `next` at the pre-rebaseline release point so stale-high stragglers
		// SUSPECT-drop until the restarted low-seq re-anchors it.
		r.pendingLow = true
		r.pendingLowAnchor = r.next
		r.pendingLowDrops = 0
	default:
		// UNSTARTED ring, or a small release point (next <= window+1) where a low-anchor
		// gate would be UNSATISFIABLE and permanently blackhole the restarted stream.
		// Fall back to a plain unpin: the next Observe re-anchors next, self-healing
		// exactly as the D32 hub-failover Rebaseline does. On an unstarted ring this is
		// a no-op (started is already false).
		r.started = false
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
	Released       uint64 // frames released for delivery
	DroppedDup     uint64 // frames dropped as duplicates
	DroppedOld     uint64 // frames dropped as already-past the release point
	DroppedSuspect uint64 // out-of-band frames dropped while (not yet) corroborating
	Skipped        uint64 // seqs skipped (lost) by window-advance or timeout — INCLUDING those skipped by a D93 immediate release; ImmediateReleases separately counts the fast-path release EVENTS so the two are distinguishable
	Resyncs        uint64 // release-point re-pins after a corroborated discontinuity
	Rebaselines    uint64 // release-point re-baselines forced by a trusted control event (e.g. hub failover, peer restart)

	// HoL-stall / hold accounting (T242, D93 observability leg).
	Holds             uint64 // head-of-line gaps that armed a hold
	HoldNanos         uint64 // cumulative nanoseconds gaps spent held before a timeout skip, a single-path immediate release, a fill, or a discontinuity re-pin
	ImmediateReleases uint64 // gaps released via the D93 single-delivering-path fast path — NOT folded into Skipped (see Skipped): a rising ImmediateReleases means the amplifier is disarmed
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
		Rebaselines:    r.rebaselines,

		Holds:             r.holds,
		HoldNanos:         r.holdNanos,
		ImmediateReleases: r.immediateReleases,
	}
}
