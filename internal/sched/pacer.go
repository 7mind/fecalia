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

// tryConsume spends one token from path idx's bucket, reporting whether the frame
// is admitted. With pacing disabled it always admits (the buckets are inert).
// Caller holds the owner's lock.
//
// ONE TOKEN PER ADMITTED PICK — UNCHANGED BY THE D95 UNITS FIX, DELIBERATELY. Since D95
// a Scheduler.Pick carries a `frames` count and the OFFERED-LOAD meter folds all of them
// in, but this bucket is still charged exactly one token per admitted call, so an
// admitted BATCH spends one token no matter how many wire frames it puts on the path.
// Bringing the pacing charge into the same unit as the refill rate (CapacityFPS, a
// wire-frame rate) is tasks:T291; it is a SEPARATE change with its own behaviour note
// because it alters the egress rate binding, whereas the D95 fix alters only the gate's
// numerator. The intermediate state is intentional, not half-wired — and it is inert on
// every shipped default, all of which run pacing OFF.
func (p *pacer) tryConsume(idx int) bool {
	if !p.cfg.Pacing {
		return true
	}
	if p.tokens[idx] >= 1 {
		p.tokens[idx]--
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
