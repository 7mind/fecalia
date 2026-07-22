package sched

import (
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/7mind/wanbond/internal/log"
	"github.com/7mind/wanbond/internal/telemetry"
)

// WeightedConfig tunes the weighted-aggregation scheduler. The device builds it
// from config.SchedulerConfig; sched holds its own struct so the package does not
// depend on config. Every field is validated by NewWeighted (fail-fast at the
// composition root), mirroring the config-boundary validation.
type WeightedConfig struct {
	// PerPathCapacity is the reference per-path capacity in frame-selection slots per
	// second: the denominator the aggregation load-gate compares offered load against,
	// and (when Pacing is on) the per-path token-bucket refill rate. There is no
	// measured BDP (P0 §7), so capacity is expressed in the only unit the scheduler
	// observes — Pick() invocations per second. Must be > 0.
	PerPathCapacity float64
	// EngageFraction engages aggregation once smoothed offered load exceeds
	// EngageFraction*PerPathCapacity. In (0,1].
	EngageFraction float64
	// DisengageFraction collapses aggregation to primary-only once smoothed offered
	// load stays below DisengageFraction*PerPathCapacity for CollapseDwell. In
	// [0,EngageFraction): the gap is the hysteresis band that keeps the metered path
	// from dribbling on/off around a single threshold (data-thrift, requirement 2).
	DisengageFraction float64
	// CollapseDwell is the sustained-low-load dwell before collapsing to primary-only
	// (the temporal half of the hysteresis, mirroring the active-backup failback dwell).
	CollapseDwell time.Duration
	// LoadTau is the time constant of the exponentially-weighted offered-load rate
	// estimator. Must be > 0.
	LoadTau time.Duration

	// Pacing turns per-path send-pacing on. When false the token buckets are bypassed
	// (a documented no-op: P0 §7 could not empirically size the pace in the unmetered
	// fixture), but PerPathCapacity still drives the aggregation gate.
	Pacing bool
	// PacingBurst is the per-path token-bucket burst in frame slots. Must be > 0 when
	// Pacing is on.
	PacingBurst float64

	// WeightRTTFloor floors RTT in the weight formula so a cold path (no RTT samples
	// yet, near-zero RTT) cannot be handed unbounded weight. Must be > 0.
	WeightRTTFloor time.Duration
	// WeightLossFloor floors the loss term under the square root so a zero-loss path
	// gets a large-but-finite weight and two zero-loss paths split by inverse-RTT
	// alone. Must be > 0.
	WeightLossFloor float64
}

func (c WeightedConfig) validate() error {
	if c.PerPathCapacity <= 0 {
		return fmt.Errorf("sched: weighted PerPathCapacity must be > 0, got %g", c.PerPathCapacity)
	}
	if c.EngageFraction <= 0 || c.EngageFraction > 1 {
		return fmt.Errorf("sched: weighted EngageFraction must be in (0,1], got %g", c.EngageFraction)
	}
	if c.DisengageFraction < 0 || c.DisengageFraction >= c.EngageFraction {
		return fmt.Errorf("sched: weighted DisengageFraction must be in [0,EngageFraction=%g), got %g", c.EngageFraction, c.DisengageFraction)
	}
	if c.CollapseDwell < 0 {
		return fmt.Errorf("sched: weighted CollapseDwell must be >= 0, got %s", c.CollapseDwell)
	}
	if c.LoadTau <= 0 {
		return fmt.Errorf("sched: weighted LoadTau must be > 0, got %s", c.LoadTau)
	}
	if c.Pacing && c.PacingBurst <= 0 {
		return fmt.Errorf("sched: weighted PacingBurst must be > 0 when pacing is enabled, got %g", c.PacingBurst)
	}
	if c.WeightRTTFloor <= 0 {
		return fmt.Errorf("sched: weighted WeightRTTFloor must be > 0, got %s", c.WeightRTTFloor)
	}
	if c.WeightLossFloor <= 0 {
		return fmt.Errorf("sched: weighted WeightLossFloor must be > 0, got %g", c.WeightLossFloor)
	}
	return nil
}

// pacerConfig projects the pacing-relevant fields (Pacing, PerPathCapacity as the
// refill rate, PacingBurst as the bucket capacity) onto the sched-owned pacerConfig
// the extracted pacer consumes (defect D65).
func (c WeightedConfig) pacerConfig() pacerConfig {
	return pacerConfig{
		Pacing:      c.Pacing,
		CapacityFPS: c.PerPathCapacity,
		BurstFrames: c.PacingBurst,
	}
}

// FECPolicy is the P3+ forward-error-correction extension seam. Given the path a
// frame was scheduled onto and the current eligible set, it returns ADDITIONAL path
// indices the frame should ALSO egress on for redundancy (e.g. a repair copy on a
// second path). This is a documented hook, NOT an implementation: the current
// single-path Scheduler.Pick returns one index, so wiring redundant transmission
// requires the P3 Bind change that fans a frame out to several sockets. Until then
// a nil policy adds no redundancy and RedundantPaths returns nothing, so the seam is
// inert but present and unit-tested.
type FECPolicy interface {
	RedundantPaths(chosen int, eligible []int) []int
}

// WeightedScheduler is the T21 send-side scheduler: a weighted-aggregation policy
// that, under load, stripes a single flow across every eligible path in proportion
// to a per-path RTT/loss-derived weight, and at low load COLLAPSES to the primary
// (index 0) so the metered backup path stays ~idle (data-thrift, requirement 2). It
// engages the backup only once offered load exceeds one path's capacity and
// disengages with hysteresis (a two-threshold band plus a collapse dwell) so the
// metered path never dribbles on/off. Per-path send-pacing (token buckets) bounds
// each path's egress rate so aggregation does not build a standing queue that
// inflates latency-under-load (P0 §7); pacing is a config no-op when disabled.
//
// It reads per-path liveness from PathHealth (the T13 up/down verdict) and per-path
// quality from PathQuality (the T13 Estimate: RTT/loss). In production BOTH are the
// SAME *telemetry.Prober per path; the unit tests drive synthetic sources.
//
// Weight formula (Mathis-style throughput proxy). A single steady-state TCP flow's
// bandwidth is ∝ 1/(RTT·√p) (Mathis et al.); used here as a RELATIVE path-quality
// proxy it directs more of the aggregate flow onto the path a flow would run faster
// on. Per eligible path:
//
//	w_i = 1 / ( max(RTT_i, RTTFloor) · sqrt(Loss_i + LossFloor) )
//
// normalized over the eligible set. RTTFloor bounds a cold/zero-RTT path; LossFloor
// keeps a zero-loss path finite (two zero-loss paths then split by inverse-RTT
// alone). There is no measured bandwidth to use (Estimate is RTT/Jitter/Loss only,
// P0 §7), so an RTT/loss proxy is the honest choice.
//
// Concurrency mirrors ActiveBackup: every field is guarded by s.mu; the send path
// recomputes under the lock and never calls back into the Bind, so a Bind holding
// its own send lock across Pick cannot deadlock.
//
// Statefulness and the T40 nudge. Pick is STATEFUL — it advances the offered-load
// meter, the aggregation gate, the pacing buckets, and the smooth-weighted-round-
// robin credits, so ONE Pick == ONE frame. The eager-failover nudge (defect D18)
// must therefore NOT call Pick (it would steal a distribution slot and skew the
// weights); it calls Recompute, which refreshes only the liveness-derived eligible/
// active set and logs any active-path transition, touching NONE of that per-frame
// distribution state.
type WeightedScheduler struct {
	health  []PathHealth  // index = priority; [0] is the preferred primary
	quality []PathQuality // parallel to health; element may be nil (neutral weight)
	clock   telemetry.Clock
	cfg     WeightedConfig
	log     log.Logger
	fec     FECPolicy // P3+ redundancy seam; nil = no FEC

	mu sync.Mutex

	// Offered-load meter: an exponentially-weighted event-rate estimator (each Pick is
	// one event; the estimate converges to the offered Pick rate in events/sec).
	loadRate float64
	haveLoad bool
	lastLoad time.Time

	// Aggregation gate (hysteresis).
	aggregating bool
	belowSince  time.Time // when load first fell below the disengage threshold; zero == not pending

	// Smooth-weighted-round-robin credits, parallel to health (global path index).
	current []float64

	// Per-path send-pacing (token buckets + coalesced shed log), extracted into a
	// caller-locked helper (defect D65) so ActiveBackup can reuse it. It is embedded
	// so its state (tokens, haveFill, …) and delegated ops promote onto the scheduler
	// and stay guarded under s.mu exactly as when they were inlined. Every access here
	// is under s.mu — the pacer holds no lock of its own.
	pacer

	// Active-primary cache: the best eligible path, for eager-failover transition
	// logging (parity with ActiveBackup). -1 == none.
	active int
}

// compile-time proof WeightedScheduler is a (dynamic) Scheduler that also carries
// pacing headroom for out-of-band probe egress (ProbeBudget, T145).
var (
	_ Scheduler        = (*WeightedScheduler)(nil)
	_ DynamicScheduler = (*WeightedScheduler)(nil)
	_ ProbeBudget      = (*WeightedScheduler)(nil)
)

// NewWeighted builds the weighted-aggregation scheduler over parallel health and
// quality slices in priority order (index 0 = preferred primary). health and quality
// must be the same length; a quality element MAY be nil (that path then gets a
// neutral weight until a real PathQuality is wired). It fails fast on an empty or
// length-mismatched set, a nil health element, a nil dependency, or an invalid config.
func NewWeighted(health []PathHealth, quality []PathQuality, cfg WeightedConfig, clock telemetry.Clock, logger log.Logger) (*WeightedScheduler, error) {
	if len(health) == 0 {
		return nil, fmt.Errorf("sched: at least one path health source is required")
	}
	if len(quality) != len(health) {
		return nil, fmt.Errorf("sched: quality set (%d) must be one entry per health source (%d)", len(quality), len(health))
	}
	for i, h := range health {
		if h == nil {
			return nil, fmt.Errorf("sched: path %d health source is nil", i)
		}
	}
	if clock == nil {
		return nil, fmt.Errorf("sched: clock is required")
	}
	if logger == nil {
		return nil, fmt.Errorf("sched: logger is required")
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	componentLog := logger.Component("sched")
	return &WeightedScheduler{
		health:  append([]PathHealth(nil), health...),
		quality: append([]PathQuality(nil), quality...),
		clock:   clock,
		cfg:     cfg,
		log:     componentLog,
		current: make([]float64, len(health)),
		pacer:   newPacer(len(health), cfg.pacerConfig(), componentLog),
		active:  -1,
	}, nil
}

// SetFEC installs the P3+ redundancy policy (nil clears it). It is the documented
// extension point for FEC-aware path selection; the current single-path Pick does
// not consult it (redundant transmission needs the P3 Bind fan-out), but
// RedundantPaths exposes it so a P3 Bind can query the additional paths per frame.
func (s *WeightedScheduler) SetFEC(fec FECPolicy) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fec = fec
}

// RedundantPaths reports the ADDITIONAL path indices a frame scheduled onto chosen
// should also egress on, per the installed FECPolicy over the current eligible set.
// It returns nil when no policy is installed (no FEC). It is the P3 hook a redundant-
// transmission Bind would call alongside Pick; today it is a documented, unit-tested
// seam with no in-tree caller on the hot path.
func (s *WeightedScheduler) RedundantPaths(chosen int) []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fec == nil {
		return nil
	}
	return s.fec.RedundantPaths(chosen, s.eligibleLocked())
}

// Pick selects the path for the NEXT frame and advances all per-frame distribution
// state (offered-load meter, aggregation gate, pacing buckets, round-robin credits).
// It returns a negative value when no path is eligible OR when every eligible path
// is paced out (the frame is dropped rather than queued — pacing bounds egress and,
// by dropping the overflow, bounds the send backlog). It is safe for concurrent
// callers.
func (s *WeightedScheduler) Pick(class FrameClass) int {
	now := s.clock.Now()
	s.mu.Lock()
	defer s.mu.Unlock()

	// Capture the inter-sample idle gap BEFORE observeLoadLocked advances lastLoad: a
	// long gap since the previous offered frame is itself the strongest evidence of low
	// load, and the collapse hysteresis must count it (criticism #1 — otherwise an
	// abrupt overload stop leaves the gate engaged across the whole idle span and the
	// next low burst dribbles onto the metered path).
	gap := s.loadGapLocked(now)
	s.recomputeLocked(now)       // liveness refresh + eager-failover log
	s.observeLoadLocked(now)     // this Pick is one offered frame (decays across the gap)
	s.updateGateLocked(now, gap) // engage/disengage aggregation (hysteresis, idle-aware)
	s.refill(now)                // top up pacing buckets

	eligible := s.eligibleLocked()
	if len(eligible) == 0 {
		return PickNone
	}
	if !s.aggregating {
		// Data-thrift: at low load only the primary (best eligible) carries traffic;
		// the metered backup stays idle. A path-down excludes it from eligible, so
		// eligible[0] is the surviving highest-priority path — failover is preserved.
		return s.serveLocked(now, eligible[0], class)
	}
	weights := s.weightsLocked(eligible)
	return s.selectAggregatingLocked(now, eligible, weights, class)
}

// AccountProbe implements ProbeBudget (T145): it deducts one pacing token from path
// pathIdx's bucket for a PROBE frame (or reflected echo) the bind has written to that
// path's socket OUTSIDE Pick, so the token bucket reserves headroom for wanbond's own
// probe stream instead of budgeting it zero. The probe is NEVER shed or delayed here —
// strict priority, the frame is already on the wire — this only CHARGES: the bucket may
// go negative and pre-drain so a subsequent ClassData Pick yields (PickPaced) until
// refill catches up. This is the "exempt-but-charged" middle tier of the three-tier
// priority model (FrameClass doc); ClassControl remains exempt AND uncharged (defect
// D22), ClassData fully paced. A no-op when pacing is off or pathIdx is out of range. It
// is safe for concurrent callers.
func (s *WeightedScheduler) AccountProbe(pathIdx int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.accountProbe(pathIdx)
}

// AggregationSnapshot is a point-in-time, mutex-guarded read of the weighted
// scheduler's aggregation gate: whether it is currently engaged, the smoothed
// offered-load estimate driving it, and the engage/disengage thresholds
// (EngageFraction/DisengageFraction * PerPathCapacity) it compares that estimate
// against. It is the read seam the T146 metrics plumbing polls; AggregationSnapshot
// itself advances no per-frame distribution state.
type AggregationSnapshot struct {
	// Aggregating reports whether the gate is currently engaged (traffic striped
	// across every eligible path) or collapsed (primary-only, data-thrift).
	Aggregating bool
	// OfferedLoadFPS is the current smoothed offered-load estimate (Pick calls per
	// second, EWMA over LoadTau) driving the gate.
	OfferedLoadFPS float64
	// EngageThresholdFPS is EngageFraction*PerPathCapacity: the load level above
	// which the gate engages.
	EngageThresholdFPS float64
	// DisengageThresholdFPS is DisengageFraction*PerPathCapacity: the load level
	// below which, sustained for CollapseDwell, the gate collapses.
	DisengageThresholdFPS float64
}

// AggregationSnapshot returns a point-in-time snapshot of the aggregation gate
// state under s.mu. It is a read-only accessor for consumers (the T146 metrics
// plumbing) that must observe the gate without perturbing any per-frame
// distribution state the way a Pick or Recompute call would.
func (s *WeightedScheduler) AggregationSnapshot() AggregationSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return AggregationSnapshot{
		Aggregating:           s.aggregating,
		OfferedLoadFPS:        s.loadRate,
		EngageThresholdFPS:    s.cfg.EngageFraction * s.cfg.PerPathCapacity,
		DisengageThresholdFPS: s.cfg.DisengageFraction * s.cfg.PerPathCapacity,
	}
}

// loadGapLocked returns the wall-clock gap since the previous offered-load sample, or
// 0 before the first sample. Caller holds s.mu.
func (s *WeightedScheduler) loadGapLocked(now time.Time) time.Duration {
	if !s.haveLoad {
		return 0
	}
	gap := now.Sub(s.lastLoad)
	if gap < 0 {
		gap = 0
	}
	return gap
}

// Recompute refreshes only the liveness-derived eligible/active set from the current
// PathHealth and logs any active-path transition. It advances NONE of the per-frame
// distribution state (load meter, gate, pacing, round-robin credits), so the eager-
// failover nudge (defect D18/T40) drives an egress-lull failover recompute through it
// WITHOUT stealing a weighted-distribution slot the way a spurious Pick would. It is
// safe for concurrent callers.
func (s *WeightedScheduler) Recompute() {
	now := s.clock.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recomputeLocked(now)
}

// DataPaths implements the Scheduler read seam (D96 fix (a)): it reports which paths
// currently CARRY DATA and each one's share of the aggregate flow, in Pick's priority-
// ordered index space. While AGGREGATING it returns EVERY eligible path (live liveness,
// eligibleLocked) with its normalized distribution weight (weightsLocked, summing to
// ~1.0) — exactly the striped set and proportions Pick's swrr draws from. When collapsed
// to primary-only (data-thrift) it returns the SOLE primary — the best eligible path,
// eligible[0], the same one Pick's non-aggregating branch serves — at Weight 1.0. It
// returns an EMPTY slice when no path is eligible.
//
// It reads the cached aggregation-gate state (s.aggregating) and the LIVE eligible set
// under s.mu WITHOUT advancing any per-frame distribution state (load meter, gate,
// pacing, round-robin credits) — a pure read, mirroring AggregationSnapshot — and never
// calls back into the Bind, so a caller holding the Bind's m.mu invokes it in the
// documented m.mu->scheduler order. The returned slice is freshly allocated and owned by
// the caller. It is safe for concurrent callers.
func (s *WeightedScheduler) DataPaths() []DataPath {
	s.mu.Lock()
	defer s.mu.Unlock()
	eligible := s.eligibleLocked()
	if len(eligible) == 0 {
		return nil
	}
	if !s.aggregating {
		// Data-thrift: only the primary (best eligible) carries traffic, matching Pick's
		// non-aggregating branch (serveLocked over eligible[0]).
		return []DataPath{{Index: eligible[0], Weight: 1.0}}
	}
	weights := s.weightsLocked(eligible)
	dps := make([]DataPath, len(eligible))
	for k, gi := range eligible {
		dps[k] = DataPath{Index: gi, Weight: weights[k]}
	}
	return dps
}

// recomputeLocked refreshes the active-primary cache (best eligible path) and logs a
// transition on change. Caller holds s.mu.
func (s *WeightedScheduler) recomputeLocked(_ time.Time) {
	best := s.bestEligibleLocked()
	s.setActiveLocked(best)
}

// serveLocked applies pacing to a single chosen path: it returns idx when the frame
// is admitted, or PickPaced when pacing is on and the path has no token (the frame is
// shed — dropped, bounding egress and backlog — and the shedding diagnostic is
// rate-limited). A ClassControl frame is pacing-EXEMPT (defect D22): it egresses on
// idx unconditionally, spending no token and never shed, so bulk overload cannot
// starve WireGuard rekey. Caller holds s.mu.
func (s *WeightedScheduler) serveLocked(now time.Time, idx int, class FrameClass) int {
	if class == ClassControl {
		return idx
	}
	if s.tryConsume(idx) {
		return idx
	}
	return s.shed(now, s.loadRate)
}

// selectAggregatingLocked picks a path proportional to weight (smooth weighted round
// robin) and applies pacing. When the round-robin's ideal pick is paced out it falls
// through to any other eligible path that still has a token, so aggregate egress is
// bounded by the SUM of the per-path paces; when every eligible path is paced out it
// sheds the frame (PickPaced). A ClassControl frame is pacing-EXEMPT (defect D22): it
// egresses on the weight-ideal path spending no token and is never shed, so bulk
// overload across every path cannot starve WireGuard rekey. Caller holds s.mu.
func (s *WeightedScheduler) selectAggregatingLocked(now time.Time, eligible []int, weights []float64, class FrameClass) int {
	ideal := s.swrrPickLocked(eligible, weights)
	if class == ClassControl {
		return ideal
	}
	if s.tryConsume(ideal) {
		return ideal
	}
	for _, gi := range eligible {
		if gi == ideal {
			continue
		}
		if s.tryConsume(gi) {
			return gi
		}
	}
	return s.shed(now, s.loadRate)
}

// swrrPickLocked is nginx's smooth weighted round-robin over the eligible set: it
// advances each eligible path's credit by its weight, picks the max-credit path, and
// subtracts the total weight from the winner. Over many calls the selection frequency
// of each path converges to its weight share, interleaved smoothly rather than
// bursted. Caller holds s.mu; weights is aligned to eligible order.
func (s *WeightedScheduler) swrrPickLocked(eligible []int, weights []float64) int {
	var total float64
	for _, w := range weights {
		total += w
	}
	best := -1
	var bestCur float64
	for k, gi := range eligible {
		s.current[gi] += weights[k]
		if best < 0 || s.current[gi] > bestCur {
			best = gi
			bestCur = s.current[gi]
		}
	}
	s.current[best] -= total
	return best
}

// weightsLocked computes the normalized Mathis-style weight for each eligible path
// from its PathQuality Estimate (RTT/loss). A path with NO usable quality — a nil
// quality source, or an all-zero Estimate (RTT==0: no probe sample yet) — is treated
// as genuinely NEUTRAL: it receives the MEAN of the measured paths' weights, so an
// unwired/cold path splits evenly rather than dominating. This matters because the
// floors would otherwise hand an all-zero Estimate the MAXIMUM weight (RTT floored to
// WeightRTTFloor, zero loss), letting a health-only path admitted via AddPath siphon
// the dominant share — the opposite of neutral. When every eligible path is neutral,
// all get equal weight. Caller holds s.mu; the result is aligned to eligible order and
// sums to 1.
func (s *WeightedScheduler) weightsLocked(eligible []int) []float64 {
	w := make([]float64, len(eligible))
	measured := make([]bool, len(eligible))
	rttFloor := float64(s.cfg.WeightRTTFloor)
	var sumMeasured float64
	nMeasured := 0
	for k, gi := range eligible {
		q := s.quality[gi]
		if q == nil {
			continue // unwired: neutral, filled below
		}
		est := q.Estimate()
		if est.RTT <= 0 {
			continue // no RTT sample yet (cold estimator): neutral, filled below
		}
		rtt := float64(est.RTT)
		if rtt < rttFloor {
			rtt = rttFloor
		}
		loss := est.Loss
		if loss < 0 {
			loss = 0
		}
		wi := 1.0 / (rtt * math.Sqrt(loss+s.cfg.WeightLossFloor))
		w[k] = wi
		measured[k] = true
		sumMeasured += wi
		nMeasured++
	}
	// Neutral weight: the mean of the measured weights (so a neutral path splits evenly
	// with the measured set, never outweighing it); equal weights when nothing measured.
	neutral := 1.0
	if nMeasured > 0 {
		neutral = sumMeasured / float64(nMeasured)
	}
	var sum float64
	for k := range w {
		if !measured[k] {
			w[k] = neutral
		}
		sum += w[k]
	}
	if sum <= 0 {
		for k := range w {
			w[k] = 1.0 / float64(len(w))
		}
		return w
	}
	for k := range w {
		w[k] /= sum
	}
	return w
}

// observeLoadLocked folds one offered frame into the exponentially-weighted event-
// rate estimator. Each event adds 1/tau (per-second units) after decaying the prior
// estimate by exp(-dt/tau); the steady-state estimate converges to the offered rate
// in events/sec. It is robust to dt==0 (many Picks at one clock instant just add
// without decay). Caller holds s.mu.
func (s *WeightedScheduler) observeLoadLocked(now time.Time) {
	tau := s.cfg.LoadTau.Seconds()
	if !s.haveLoad {
		s.haveLoad = true
		s.lastLoad = now
		s.loadRate = 0
	} else {
		dt := now.Sub(s.lastLoad).Seconds()
		if dt < 0 {
			dt = 0
		}
		s.loadRate *= math.Exp(-dt / tau)
		s.lastLoad = now
	}
	s.loadRate += 1.0 / tau
}

// updateGateLocked engages or disengages aggregation from the smoothed offered load,
// with hysteresis: engage the instant load exceeds EngageFraction*capacity; collapse
// back to primary-only only after load stays below DisengageFraction*capacity for
// CollapseDwell. The two-threshold band plus the dwell keep the metered path from
// flapping on/off around a single point (requirement 2). gap is the wall-clock idle
// span since the previous offered frame.
//
// Idle time IS low load (criticism #1). The gate only advances on a Pick, so an
// overload that stops ABRUPTLY (load still above disengage on the last frame) would
// otherwise stay engaged across an arbitrarily long idle span and stripe the first
// ~CollapseDwell of the NEXT low burst onto the metered backup — a data-thrift leak.
// Two provisions close it: (a) an idle gap that alone reaches CollapseDwell forces an
// immediate collapse, since a gap that long is itself proof of sustained low load; and
// (b) belowSince is BACKDATED to when load actually dropped (the previous sample,
// now-gap) rather than seeded at this post-idle frame, so the dwell counts the idle.
// The load EWMA decays across gap in observeLoadLocked, so a post-idle frame reads
// ~0 and the collapse fires on the FIRST frame of the next burst. Caller holds s.mu.
func (s *WeightedScheduler) updateGateLocked(now time.Time, gap time.Duration) {
	engage := s.cfg.EngageFraction * s.cfg.PerPathCapacity
	disengage := s.cfg.DisengageFraction * s.cfg.PerPathCapacity
	if !s.aggregating {
		if s.loadRate > engage {
			s.aggregating = true
			s.belowSince = time.Time{}
			s.log.Info("scheduler aggregation change", "to", "aggregating", "from", "collapsed",
				"load_fps", s.loadRate, "engage_threshold_fps", engage, "disengage_threshold_fps", disengage)
		}
		return
	}
	// (a) A single idle gap of at least the dwell is conclusive: collapse now.
	if gap >= s.cfg.CollapseDwell {
		s.aggregating = false
		s.belowSince = time.Time{}
		s.log.Info("scheduler aggregation change", "to", "collapsed", "from", "aggregating",
			"reason", "idle gap", "gap", gap.String(), "load_fps", s.loadRate,
			"engage_threshold_fps", engage, "disengage_threshold_fps", disengage)
		return
	}
	if s.loadRate < disengage {
		if s.belowSince.IsZero() {
			// (b) Backdate to when load dropped: the previous sample (now-gap), not this
			// post-idle frame, so the idle span counts toward the dwell.
			s.belowSince = now.Add(-gap)
		}
		if now.Sub(s.belowSince) >= s.cfg.CollapseDwell {
			s.aggregating = false
			s.belowSince = time.Time{}
			s.log.Info("scheduler aggregation change", "to", "collapsed", "from", "aggregating",
				"reason", "sustained low load", "load_fps", s.loadRate, "engage_threshold_fps", engage, "disengage_threshold_fps", disengage)
		}
		return
	}
	// Load rose back above the disengage threshold: reset the collapse dwell.
	s.belowSince = time.Time{}
}

// eligibleLocked returns the global indices of paths reporting StateUp, in priority
// order. Caller holds s.mu.
func (s *WeightedScheduler) eligibleLocked() []int {
	e := make([]int, 0, len(s.health))
	for i := range s.health {
		if s.health[i].State() == telemetry.StateUp {
			e = append(e, i)
		}
	}
	return e
}

// bestEligibleLocked returns the lowest-index path reporting StateUp, or -1.
func (s *WeightedScheduler) bestEligibleLocked() int {
	for i := range s.health {
		if s.health[i].State() == telemetry.StateUp {
			return i
		}
	}
	return -1
}

// setActiveLocked stores the active-primary index and logs a transition only when it
// changes, so a saturated send path does not log on every Pick. The message matches
// ActiveBackup's ("scheduler active path change") so downstream log parsing (the e2e
// failover harness) reads either policy identically.
func (s *WeightedScheduler) setActiveLocked(idx int) {
	if s.active == idx {
		return
	}
	from := s.active
	s.active = idx
	s.log.Info("scheduler active path change", "from", from, "to", idx, "reason", "weighted recompute")
}

// AddPath admits h as a new lowest-priority path and returns its index (the new
// tail). Its quality source is h itself when h also satisfies PathQuality (the
// production *Prober does); a health-ONLY source (no PathQuality) is recorded with a
// nil quality and weightsLocked then gives it the NEUTRAL (mean) weight, so it splits
// evenly and never siphons the dominant share. Appending at the tail leaves every
// existing index unchanged and the new path only carries traffic once aggregation
// engages, so a runtime admission never disturbs the surviving paths' distribution
// (T30). It is safe for concurrent callers.
func (s *WeightedScheduler) AddPath(p PathAdmission) (int, error) {
	if p.Health == nil {
		return 0, fmt.Errorf("sched: cannot add a nil path health source")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.health = append(s.health, p.Health)
	s.quality = append(s.quality, asQuality(p.Health))
	s.current = append(s.current, 0)
	// The weighted scheduler paces with a SINGLE shared-scalar bucket (cfg.pacerConfig()), not a
	// per-path token bucket, so it has NO analogue of the D79 per-path positional carry: p.Pacing
	// is accepted for interface symmetry and deliberately ignored. addPath appends one shared
	// bucket seeded from the immutable scalar config, so a runtime-added path can never inherit a
	// wrong per-path rate — there is no per-path rate to inherit.
	s.addPath()
	return len(s.health) - 1, nil
}

// RemovePath drops the path at index i (shifting higher indices down by one) and
// clears the cached active selection so the next Pick/Recompute re-derives it from
// live liveness. The round-robin credits are positional and compacted with the path
// list; a brief re-warm of the smoothing after a membership change is harmless. It is
// safe for concurrent callers.
func (s *WeightedScheduler) RemovePath(i int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i < 0 || i >= len(s.health) {
		return fmt.Errorf("sched: remove path index %d out of range [0,%d)", i, len(s.health))
	}
	s.health = append(s.health[:i], s.health[i+1:]...)
	s.quality = append(s.quality[:i], s.quality[i+1:]...)
	s.current = append(s.current[:i], s.current[i+1:]...)
	s.removePath(i)
	s.active = -1
	return nil
}

// SetPaths replaces the entire health list (priority order, index 0 = preferred
// primary), rebuilding the parallel quality slice from the new sources, resetting the
// round-robin credits and pacing buckets, and collapsing aggregation to primary-only
// so the next Pick re-derives everything from live liveness. The Bind calls it on
// every Open to re-align the scheduler with the freshly-rebuilt path slice (T30). It
// fails fast on an empty set or a nil element, matching NewWeighted. It is safe for
// concurrent callers.
func (s *WeightedScheduler) SetPaths(paths []PathAdmission) error {
	if len(paths) == 0 {
		return fmt.Errorf("sched: at least one path health source is required")
	}
	for i, p := range paths {
		if p.Health == nil {
			return fmt.Errorf("sched: path %d health source is nil", i)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.health = make([]PathHealth, len(paths))
	s.quality = make([]PathQuality, len(paths))
	for i, p := range paths {
		s.health[i] = p.Health
		s.quality[i] = asQuality(p.Health)
	}
	s.current = make([]float64, len(paths))
	// The weighted pacer is a single shared-scalar bucket set, reset to len(paths) buckets seeded
	// from the immutable scalar config; each admission's per-path Pacing is inert here (no per-
	// path rate exists to key by identity), so there is no D79-style positional carry to fix.
	s.reset(len(paths))
	// T190: collapsing the gate as part of a wholesale path replacement IS an aggregation
	// state change, so emit the canonical "scheduler aggregation change" record (matching the
	// engage/idle-gap sites' shape) instead of flipping to collapsed silently. Only when the
	// gate WAS engaged — a replace while already collapsed is a no-op transition. s.reset (the
	// pacer) leaves the load EWMA untouched, so s.loadRate still reflects the load at replacement.
	if s.aggregating {
		engage := s.cfg.EngageFraction * s.cfg.PerPathCapacity
		disengage := s.cfg.DisengageFraction * s.cfg.PerPathCapacity
		s.log.Info("scheduler aggregation change", "to", "collapsed", "from", "aggregating",
			"reason", "paths replaced", "load_fps", s.loadRate,
			"engage_threshold_fps", engage, "disengage_threshold_fps", disengage)
	}
	s.aggregating = false
	s.belowSince = time.Time{}
	s.active = -1
	return nil
}

// asQuality returns h as a PathQuality when it also implements that interface (the
// production *telemetry.Prober does — one source drives both liveness and weight), or
// nil so the path gets a neutral weight.
func asQuality(h PathHealth) PathQuality {
	if q, ok := h.(PathQuality); ok {
		return q
	}
	return nil
}
