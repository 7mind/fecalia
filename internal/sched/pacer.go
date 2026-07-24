package sched

import (
	"time"

	"github.com/7mind/wanbond/internal/log"
)

// shedLogInterval bounds how often the pacer-shedding diagnostic is emitted, so a
// sustained overload logs a periodic coalesced record rather than one line per
// dropped frame.
const shedLogInterval = 1 * time.Second

// pacerConfig configures the per-path token-bucket pacer. It is the sched-owned
// projection of the pacing-relevant WeightedConfig fields, so the pacer does not
// depend on the scheduler config type and can be reused by ActiveBackup (D65).
type pacerConfig struct {
	// Pacing turns per-path pacing on. When false the buckets are inert accountants:
	// tryConsume always admits and no frame is ever shed, but the buckets are still
	// maintained so toggling pacing needs no re-seed.
	Pacing bool
	// CapacityFPS is the per-path token-bucket refill rate in frame slots per second
	// (the scheduler's PerPathCapacity). Each bucket gains CapacityFPS·elapsed tokens
	// per refill, capped at BurstFrames.
	CapacityFPS float64
	// BurstFrames is the per-path token-bucket burst (capacity) in frame slots (the
	// scheduler's PacingBurst). A freshly seeded bucket holds BurstFrames tokens.
	BurstFrames float64
}

// pacer is the per-path token-bucket send pacer extracted from WeightedScheduler
// (defect D65) so a second scheduler (ActiveBackup) can reuse the exact pacing
// state and logic. It bounds each path's egress rate and, by SHEDDING overflow
// rather than queuing it, bounds the send backlog that would otherwise inflate
// latency-under-load (P0 §7).
//
// It is CALLER-LOCKED: the pacer holds NO mutex of its own. Every method is a
// "…Locked" body in disguise — the owning scheduler MUST guard every call under
// its own lock (WeightedScheduler holds s.mu around each pacer call, exactly as
// it did when this state was inlined). This keeps the pacer a passive helper with
// no independent concurrency contract to reconcile against the scheduler's.
type pacer struct {
	cfg pacerConfig
	log log.Logger

	// Per-path token buckets, parallel to the owner's path list (global path index).
	tokens   []float64
	lastFill time.Time
	haveFill bool

	// Shed-log rate limiter: under sustained overload shedding is per-frame
	// (thousands/s), so the "pacer shedding" diagnostic is coalesced to at most one
	// record per shedLogInterval, carrying the count shed since the last record.
	shedCount   int
	lastShedLog time.Time
}

// newPacer builds a pacer over n paths with every bucket seeded full (BurstFrames).
// logger is the already-Component'd scheduler logger, so the coalesced shed
// diagnostic reads identically to the pre-extraction inlined form.
func newPacer(n int, cfg pacerConfig, logger log.Logger) pacer {
	return pacer{
		cfg:    cfg,
		log:    logger,
		tokens: fullBuckets(n, cfg.BurstFrames),
	}
}

// refill tops up every per-path token bucket by CapacityFPS·elapsed, capped at
// BurstFrames. The first call seeds every bucket full. It is a no-op accountant
// when pacing is disabled (tryConsume always admits), but the buckets are still
// maintained so toggling pacing needs no re-seed. Caller holds the owner's lock.
func (p *pacer) refill(now time.Time) {
	if !p.haveFill {
		p.haveFill = true
		p.lastFill = now
		for i := range p.tokens {
			p.tokens[i] = p.cfg.BurstFrames
		}
		return
	}
	dt := now.Sub(p.lastFill).Seconds()
	if dt < 0 {
		dt = 0
	}
	add := p.cfg.CapacityFPS * dt
	for i := range p.tokens {
		p.tokens[i] += add
		if p.tokens[i] > p.cfg.BurstFrames {
			p.tokens[i] = p.cfg.BurstFrames
		}
	}
	p.lastFill = now
}

// tryConsume ADMITS OR SHEDS one whole BATCH covering `frames` offered wire frames, and
// on admission deducts `frames` tokens from path idx's bucket. With pacing disabled it
// always admits and spends nothing (the buckets are inert). Caller holds the owner's
// lock.
//
// THE ADMISSION PREDICATE IS UNCHANGED BY THIS (tasks:T291): it is still "the bucket
// holds >= 1 token", exactly as before the batch carried more than one frame. Shedding
// stays BATCH-ATOMIC because the bind has no per-buffer shed path — Multipath.Send either
// writes the whole batch or returns errPacerShedding for all of it (multipath.go:2703-
// 2710) — so admission cannot be split frame-by-frame within one call even though the
// charge now is.
//
// THE CHARGE IS NOW `frames` TOKENS, NOT ONE (tasks:T291, decisions:K35 §3e). Before this
// change CapacityFPS meant two different units inside the same pacerConfig: it sized the
// refill rate as a WIRE-frame rate (config.SizePacingFromBDP, config.go:353-367) while
// tryConsume spent exactly one token per admitted BATCH — so an enabled pacer admitted
// roughly the BATCH FACTOR (measured 1.52-3.07, load-dependent) times the declared link
// bandwidth, defeating the standing-queue bound pacing exists to enforce (P0 §7). Charging
// `frames` instead of 1 makes the charge and the refill speak the same unit, exactly as
// D95 already did for the gate's numerator (weighted.go observeLoadLocked). This is a
// NO-OP whenever pacing is disabled — every shipped default and the e2e fixture — because
// the buckets are inert there; it is a LIVE rate-binding change whenever pacing is
// enabled.
//
// THE BUCKET MAY GO NEGATIVE, precisely the precedent accountProbe already sets below: an
// admitted batch larger than the remaining tokens is still let through (the predicate only
// asked for >= 1) and drains the bucket past zero, so SUBSEQUENT batches shed until refill
// catches up. That is by design — the bucket bounds the LONG-RUN admitted rate, not the
// admission of any one call, exactly as CapacityFPS's doc comment already promises.
//
// PARITY IS CHARGED HERE BUT NEVER ADMITTED (defects:D108, narrowed but NOT closed by this
// change). `frames` is the SAME value the D95 fix meters at the call site — len(bufs) plus
// the FEC parity carry (multipath.go) — so once a batch carrying parity is admitted, the
// parity shards it carried are charged to this bucket one batch late, same as the data
// frames. But parity shards egress unconditionally (multipath.go:2769-2777, written outside
// any admission decision) — no shed decision is ever taken FOR them; they can only drive
// this bucket negative, the same "charged but never admitted" posture accountProbe already
// documents for out-of-band probe frames. Closing that (charging parity AT GENERATION TIME
// rather than one batch late through the carry) is D108's open half and stays out of scope
// here.
func (p *pacer) tryConsume(idx int, frames int) bool {
	if !p.cfg.Pacing {
		return true
	}
	if p.tokens[idx] >= 1 {
		p.tokens[idx] -= float64(frames)
		return true
	}
	return false
}

// accountProbe deducts one token from path idx's bucket to RESERVE pacing headroom
// for a frame that egresses OUTSIDE the Pick/tryConsume admission path — wanbond's own
// PROBE frames (frame.KindProbe) and their reflected echoes, which emitProbes and
// dispatchInbound write DIRECTLY to the path socket, bypassing Send->Pick->token-bucket
// (T145). It is the "charged" half of the exempt-but-charged middle tier of the pacing
// priority model: the probe itself is NEVER shed or delayed here (strict priority — the
// caller has already written it to the wire, unconditionally), this call only spends the
// token so the bucket briefly PRE-DRAINS. The bucket MAY go negative; tryConsume then
// yields (sheds ClassData) until refill catches up, which is exactly the headroom the
// pacer must budget so paced DATA plus the probe stream (plus reflected echoes) cannot
// jointly oversubscribe the link and starve probes past DownAfter into a spurious
// path-DOWN. It is a no-op when pacing is disabled (the buckets are inert) or idx is out
// of range (a stale index from a concurrent membership change — probe accounting is
// best-effort headroom, never correctness-critical). Caller holds the owner's lock.
func (p *pacer) accountProbe(idx int) {
	if !p.cfg.Pacing {
		return
	}
	if idx < 0 || idx >= len(p.tokens) {
		return
	}
	p.tokens[idx]--
}

// shed records one paced-out (shed) frame and emits the coalesced "pacer shedding"
// diagnostic at most once per shedLogInterval, carrying the count shed since the
// last record and the current offered-load rate (loadFPS). It returns PickPaced so
// the Send path can map the drop to a distinct, non-outage error/log: the paths are
// healthy, this is deliberate rate limiting. Caller holds the owner's lock.
func (p *pacer) shed(now time.Time, loadFPS float64) int {
	p.shedCount++
	if p.lastShedLog.IsZero() || now.Sub(p.lastShedLog) >= shedLogInterval {
		p.log.Info("scheduler pacer shedding",
			"shed_frames", p.shedCount,
			"load_fps", loadFPS,
		)
		p.lastShedLog = now
		p.shedCount = 0
	}
	return PickPaced
}

// addPath appends one new full (BurstFrames) bucket at the tail, keeping the token
// slice index-aligned with the owner's path list after a runtime path admission.
// Caller holds the owner's lock.
func (p *pacer) addPath() {
	p.tokens = append(p.tokens, p.cfg.BurstFrames)
}

// removePath drops path i's bucket (shifting higher indices down by one), keeping
// the token slice index-aligned with the owner's path list after a runtime path
// removal. Caller holds the owner's lock.
func (p *pacer) removePath(i int) {
	p.tokens = append(p.tokens[:i], p.tokens[i+1:]...)
}

// reset rebuilds the token buckets as n full buckets and clears the fill clock, so
// the next refill re-seeds from now. It mirrors the membership-replacement reset in
// the owner's SetPaths. Caller holds the owner's lock.
func (p *pacer) reset(n int) {
	p.tokens = fullBuckets(n, p.cfg.BurstFrames)
	p.haveFill = false
}

// fullBuckets returns n token buckets each seeded to burst.
func fullBuckets(n int, burst float64) []float64 {
	t := make([]float64, n)
	for i := range t {
		t[i] = burst
	}
	return t
}
