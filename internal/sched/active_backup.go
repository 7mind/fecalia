package sched

import (
	"fmt"
	"sync"
	"time"

	"github.com/7mind/wanbond/internal/log"
	"github.com/7mind/wanbond/internal/telemetry"
)

// Config tunes the active-backup scheduler.
type Config struct {
	// FailbackAfter is the failback hysteresis dwell: once a higher-priority
	// path recovers (returns to StateUp) while egress is on a lower-priority
	// backup, the scheduler holds on the backup until the higher-priority path
	// has stayed continuously Up for at least this long before failing egress
	// BACK to it. This is the anti-thrash guard: a primary that flaps up/down
	// never repeatedly steals egress back, because each dip restarts the dwell.
	//
	// Failover in the OTHER direction (active path going Down) is never delayed:
	// the scheduler switches to the best remaining path immediately, since a dead
	// active path cannot carry traffic while a timer runs. The asymmetry — instant
	// failover, debounced failback — is what makes recovery non-thrashing.
	FailbackAfter time.Duration

	// Pacing turns per-path send-pacing on for active-backup (defect D65). With it
	// off the scheduler is byte-for-byte its pre-pacing self: Pick returns the active
	// index and no frame is ever shed. With it on, each Pick of a ClassData frame
	// consumes one token from the ACTIVE path's OWN token bucket and returns PickPaced
	// (not the active index) when that bucket is empty; ClassControl is pacing-EXEMPT
	// (defect D22) — it egresses spending no token and is never shed. Shaping the
	// single active uplink to its own drain rate is what stops it self-inflicting the
	// ~1s Starlink bufferbloat this fix targets (D65 primary fix).
	Pacing bool
	// PerPathCapacities is the PER-PATH token-bucket refill rate in frame slots per
	// second, index-aligned to the health/priority slice (index 0 = preferred
	// primary). Because active-backup egresses on exactly ONE path at a time, each
	// bucket is sized from ITS OWN link_bandwidth/link_rtt BDP — NOT the weighted
	// scheduler's single shared BOTTLENECK scalar, which would cap a faster active
	// primary at the slowest backup's declared rate (a Starlink primary paced to a 5G
	// backup's rate), reimposing the artificial single-flow ceiling this fix removes.
	// Required — each entry > 0, len == len(health) — when Pacing is on; ignored off.
	PerPathCapacities []float64
	// PacingBursts is the PER-PATH token-bucket burst (capacity) in frame slots,
	// index-aligned to the health/priority slice (a freshly seeded bucket holds this
	// many tokens). Required — each entry > 0, len == len(PerPathCapacities) ==
	// len(health) — when Pacing is on; ignored when Pacing is off.
	PacingBursts []float64
}

// ActiveBackup is the P1 send-side scheduler: a single ACTIVE path (the
// highest-priority eligible one — index 0, the preferred primary, when it is
// up) carries ALL egress; every other path stays idle (data-thrift by
// construction). It reads per-path liveness from the injected T13 PathHealth
// sources and a clock for the failback dwell, so it is deterministic under test.
//
// Priority is the health-slice order: index 0 is the preferred primary, higher
// indices are progressively-lower-priority backups. A path is eligible when its
// PathHealth reports StateUp.
//
// Concurrency: the send path is concurrent, so Pick recomputes the active path
// under s.mu and every field it touches is guarded by that mutex. It never calls
// back into the Bind, so a Bind holding its own send lock across Pick cannot
// deadlock against it.
type ActiveBackup struct {
	health []PathHealth // index = priority; [0] is the preferred primary
	clock  telemetry.Clock
	cfg    Config
	log    log.Logger

	mu           sync.Mutex
	active       int       // cached selection; -1 == no eligible path
	pending      int       // failback candidate index; -1 == none pending
	pendingSince time.Time // when the current failback candidate first became eligible

	// Per-path send-pacing (defect D65): one caller-locked token bucket per path,
	// index-aligned to health (index 0 = preferred primary). Active-backup egresses
	// on exactly ONE path at a time, so each path carries its OWN BDP-sized bucket
	// (PerPathCapacities[i]/PacingBursts[i]) — NOT the weighted scheduler's single
	// shared-scalar pacer — so a fast active primary is never capped at the slowest
	// backup's rate. Each pacer holds a single (n==1) bucket; Pick refills and
	// consumes ONLY the active path's, and a failover path refills to full from its
	// idle span. It is nil when pacing is disabled, in which case Pick is byte-for-
	// byte its pre-pacing behaviour. Every access is under s.mu; the pacers hold no
	// lock of their own. It is resized in AddPath/RemovePath/SetPaths so it stays
	// index-aligned with health across membership changes (T30) and Pick never
	// indexes out of range.
	pacers []pacer
}

// compile-time proof ActiveBackup is a (dynamic) Scheduler.
var (
	_ Scheduler        = (*ActiveBackup)(nil)
	_ DynamicScheduler = (*ActiveBackup)(nil)
)

// NewActiveBackup builds the active-backup scheduler over health (priority
// order, index 0 = preferred primary). It fails fast on an empty health set or a
// nil dependency: the scheduler is a required, fully-wired collaborator, not an
// optional one.
func NewActiveBackup(health []PathHealth, cfg Config, clock telemetry.Clock, logger log.Logger) (*ActiveBackup, error) {
	if len(health) == 0 {
		return nil, fmt.Errorf("sched: at least one path health source is required")
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
	componentLog := logger.Component("sched")
	pacers, err := newActiveBackupPacers(cfg, len(health), componentLog)
	if err != nil {
		return nil, err
	}
	return &ActiveBackup{
		health:  health,
		clock:   clock,
		cfg:     cfg,
		log:     componentLog,
		active:  -1,
		pending: -1,
		pacers:  pacers,
	}, nil
}

// newActiveBackupPacers validates the per-path pacing config and builds one full
// (BurstFrames) single-bucket pacer per path, index-aligned to health. It returns a
// nil slice when pacing is disabled (the buckets are then never consulted). When
// pacing is on it fails fast unless there is exactly one capacity AND one burst per
// path and every capacity/burst is > 0 — each path's bucket is sized from ITS OWN
// BDP, so a missing or non-positive entry is a wiring error, not a soft default.
func newActiveBackupPacers(cfg Config, n int, logger log.Logger) ([]pacer, error) {
	if !cfg.Pacing {
		return nil, nil
	}
	if len(cfg.PerPathCapacities) != n || len(cfg.PacingBursts) != n {
		return nil, fmt.Errorf("sched: active-backup pacing needs one PerPathCapacity and one PacingBurst per path: got %d capacities, %d bursts, %d paths",
			len(cfg.PerPathCapacities), len(cfg.PacingBursts), n)
	}
	pacers := make([]pacer, n)
	for i := 0; i < n; i++ {
		if cfg.PerPathCapacities[i] <= 0 {
			return nil, fmt.Errorf("sched: active-backup PerPathCapacities[%d] must be > 0, got %g", i, cfg.PerPathCapacities[i])
		}
		if cfg.PacingBursts[i] <= 0 {
			return nil, fmt.Errorf("sched: active-backup PacingBursts[%d] must be > 0, got %g", i, cfg.PacingBursts[i])
		}
		pacers[i] = newPacer(1, pacerConfig{
			Pacing:      true,
			CapacityFPS: cfg.PerPathCapacities[i],
			BurstFrames: cfg.PacingBursts[i],
		}, logger)
	}
	return pacers, nil
}

// AddPath admits h as a new lowest-priority path and returns its index (the new
// tail). Appending at the tail leaves every existing index — including the cached
// active/pending — unchanged, and the new path can only become active once every
// higher-priority path is down, so a runtime admission never disturbs the active
// selection of the surviving paths (T30). It is safe for concurrent callers.
func (s *ActiveBackup) AddPath(h PathHealth) (int, error) {
	if h == nil {
		return 0, fmt.Errorf("sched: cannot add a nil path health source")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.health = append(s.health, h)
	if s.cfg.Pacing {
		// Keep the bucket slice index-aligned with health. A runtime-added path carries
		// no declared per-path BDP, so its bucket inherits the current tail's pace. The
		// live bucket slice can be EMPTY here — RemovePath is allowed to drop the set to
		// empty (a legal DynamicScheduler state; Pick then returns PickNone) and a later
		// AddPath re-grows it — so fall back to the immutable config tail, which is
		// guaranteed non-empty whenever pacing is on. A full bucket, like every fresh
		// pacer.
		seed := s.cfg.tailPacerConfig()
		if n := len(s.pacers); n > 0 {
			seed = s.pacers[n-1].cfg
		}
		s.pacers = append(s.pacers, newPacer(1, seed, s.log))
	}
	return len(s.health) - 1, nil
}

// RemovePath drops the health source at index i and repairs the cached selection
// so the surviving paths are undisturbed (T30). The active/pending indices are
// remapped by IDENTITY rather than by arithmetic: the survivor a cached index
// pointed at keeps being pointed at after the slice compaction, and a cached
// index that pointed at the removed path is cleared (-1). Clearing active makes
// the next Pick fail egress over to the best remaining path; clearing pending
// abandons an in-flight failback dwell whose candidate no longer exists. It is
// safe for concurrent callers.
func (s *ActiveBackup) RemovePath(i int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i < 0 || i >= len(s.health) {
		return fmt.Errorf("sched: remove path index %d out of range [0,%d)", i, len(s.health))
	}
	// Capture the identities the cached indices point at BEFORE compaction, unless
	// they point at the path being removed (then they are cleared).
	var activeH, pendingH PathHealth
	if s.active >= 0 && s.active != i {
		activeH = s.health[s.active]
	}
	if s.pending >= 0 && s.pending != i {
		pendingH = s.health[s.pending]
	}
	s.health = append(s.health[:i], s.health[i+1:]...)
	if s.cfg.Pacing {
		// Drop path i's bucket, keeping the slice index-aligned with the compacted
		// health list so a survivor's bucket stays with it and Pick never indexes stale.
		s.pacers = append(s.pacers[:i], s.pacers[i+1:]...)
	}
	s.active = indexOfHealth(s.health, activeH)
	s.pending = indexOfHealth(s.health, pendingH)
	if s.pending < 0 {
		s.pendingSince = time.Time{}
	}
	return nil
}

// SetPaths replaces the entire health list with a defensive copy of health and
// clears the cached selection (active/pending), so the next Pick re-derives the
// active path from live liveness. The Bind calls it on every Open to re-align the
// scheduler's membership with the freshly-rebuilt path slice, which is what makes a
// runtime path add/remove survive a Close→Open cycle index-aligned (T30). It fails
// fast on an empty set or a nil element, matching NewActiveBackup's contract. It is
// safe for concurrent callers.
func (s *ActiveBackup) SetPaths(health []PathHealth) error {
	if len(health) == 0 {
		return fmt.Errorf("sched: at least one path health source is required")
	}
	for i, h := range health {
		if h == nil {
			return fmt.Errorf("sched: path %d health source is nil", i)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.health = append([]PathHealth(nil), health...)
	if s.cfg.Pacing {
		// SetPaths replaces s.health wholesale (a Close→Open durable-membership swap that
		// may CHANGE the path count), so the bucket slice MUST be resized in lockstep —
		// otherwise the next Pick indexes tokens[] out of range and panics. Rebuild n
		// full, reseeded buckets: an overlapping index keeps its per-path pace; a
		// grown-in path inherits the previous tail's pace (no declared BDP is available
		// through this interface). When the live bucket slice is EMPTY (a prior
		// RemovePath dropped the set to empty), every rebuilt bucket falls back to the
		// immutable config tail.
		s.pacers = resizeActiveBackupPacers(s.pacers, len(health), s.cfg.tailPacerConfig(), s.log)
	}
	s.active = -1
	s.pending = -1
	s.pendingSince = time.Time{}
	return nil
}

// resizeActiveBackupPacers rebuilds the per-path bucket slice to exactly n full,
// reseeded single-bucket pacers (mirroring the membership-replacement reset in
// SetPaths). An index present in old keeps its per-path pace; an index beyond old
// inherits old's LAST pace (a grown-in path has no declared per-path BDP reachable
// through the health-only SetPaths signature). When old is EMPTY — a prior
// RemovePath legitimately dropped the path set to empty (a legal DynamicScheduler
// state) — there is no tail to inherit, so every bucket falls back to fallback (the
// immutable config tail, guaranteed non-empty whenever pacing is on). Caller holds
// the owner's lock.
func resizeActiveBackupPacers(old []pacer, n int, fallback pacerConfig, logger log.Logger) []pacer {
	np := make([]pacer, n)
	for i := 0; i < n; i++ {
		cfg := fallback
		switch {
		case i < len(old):
			cfg = old[i].cfg
		case len(old) > 0:
			cfg = old[len(old)-1].cfg
		}
		np[i] = newPacer(1, cfg, logger)
	}
	return np
}

// tailPacerConfig returns the pacerConfig seeded from the LAST configured path's
// per-path BDP. cfg is immutable and, whenever pacing is on, NewActiveBackup
// validated len(PerPathCapacities) == len(PacingBursts) == len(health) >= 1, so
// the config tail is ALWAYS a valid capacity/burst to seed a bucket re-grown after
// RemovePath emptied the live bucket slice. Call only with pacing enabled (the
// slices are empty and ignored when it is off).
func (c Config) tailPacerConfig() pacerConfig {
	last := len(c.PerPathCapacities) - 1
	return pacerConfig{
		Pacing:      true,
		CapacityFPS: c.PerPathCapacities[last],
		BurstFrames: c.PacingBursts[last],
	}
}

// indexOfHealth returns the index of h in hs by identity, or -1 when h is nil or
// absent. Interface equality is pointer identity for the concrete *Prober /
// health values the scheduler holds.
func indexOfHealth(hs []PathHealth, h PathHealth) int {
	if h == nil {
		return -1
	}
	for i := range hs {
		if hs[i] == h {
			return i
		}
	}
	return -1
}

// Pick returns the index of the current active path, recomputing the selection
// against live path health and the clock. It is safe for concurrent callers.
//
// When pacing is configured (defect D65) it also meters the ACTIVE path's OWN token
// bucket: a ClassData frame consumes one token from that path's bucket and Pick
// returns PickPaced (not the active index) when it is empty; ClassControl is pacing-
// EXEMPT (defect D22) — it egresses on the active path spending no token and is never
// shed, so bulk overload cannot starve WireGuard rekey. Because each path has its OWN
// BDP-sized bucket, a failover draws from the new active path's own (idle-refilled,
// full) bucket at that path's own rate. With pacing disabled the buckets are inert and
// this is byte-for-byte the pre-pacing behaviour (class is ignored, nothing is shed).
func (s *ActiveBackup) Pick(class FrameClass) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock.Now()
	s.recomputeLocked(now)
	if !s.cfg.Pacing || s.active < 0 {
		// Pacing off, or no eligible path (active == PickNone): return the selection
		// as-is. Guard s.active < 0 BEFORE indexing s.pacers[s.active].
		return s.active
	}
	if class == ClassControl {
		return s.active // pacing-exempt: no token spent, never shed (defect D22).
	}
	p := &s.pacers[s.active]
	p.refill(now)
	if p.tryConsume(0) {
		return s.active
	}
	// Healthy path, but its bucket is momentarily empty: shed this frame (PickPaced),
	// bounding egress and the send backlog. active-backup meters no offered-load rate,
	// so the coalesced shed diagnostic reports load 0.
	return p.shed(now, 0)
}

// Recompute re-derives the active path from current liveness and the failback
// dwell, exactly as Pick does, but discards the result. Active-backup's selection
// is a single cached index recomputed purely from liveness and the clock, so Pick
// is idempotent and Recompute IS Pick-without-the-return: the eager-failover nudge
// (defect D18/T40) can drive an egress-lull failover recompute through it with no
// behavioural difference from the old Pick-based nudge. It is safe for concurrent
// callers.
func (s *ActiveBackup) Recompute() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recomputeLocked(s.clock.Now())
}

// recomputeLocked re-derives the active path from current liveness and the
// failback dwell. Caller holds s.mu.
//
// Selection rule, in priority order:
//   - best = the lowest-index (highest-priority) path currently StateUp.
//   - No path up -> no active path.
//   - Cold start, or the current active is no longer up (best is a
//     lower-priority path) -> switch immediately (instant failover).
//   - best already IS the active -> stable, nothing to do.
//   - best is a HIGHER-priority path than the active (the primary recovered)
//     -> a failback candidate: hold on the current active until best has been
//     continuously up for FailbackAfter, THEN fail back. If the current active
//     itself goes down mid-dwell, move to best at once rather than stall.
func (s *ActiveBackup) recomputeLocked(now time.Time) {
	best := s.bestEligibleLocked()

	if best < 0 {
		s.pending = -1
		s.setActiveLocked(-1, "no eligible path")
		return
	}
	if s.active < 0 || best > s.active {
		// Cold start, or the active path is no longer eligible (best, the first
		// eligible index, sits below it) -> immediate (fail)over.
		s.pending = -1
		s.setActiveLocked(best, "failover")
		return
	}
	if best == s.active {
		s.pending = -1
		return
	}

	// best < s.active: a higher-priority path recovered -> debounced failback.
	if s.pending != best {
		s.pending = best
		s.pendingSince = now
	}
	if now.Sub(s.pendingSince) >= s.cfg.FailbackAfter {
		s.pending = -1
		s.setActiveLocked(best, "failback")
		return
	}
	// Still within the dwell: keep the current active while it stays eligible; if
	// it has gone down, we cannot stall on it — move to best now.
	if s.health[s.active].State() != telemetry.StateUp {
		s.pending = -1
		s.setActiveLocked(best, "failover during failback dwell")
	}
}

// bestEligibleLocked returns the lowest-index path reporting StateUp, or -1.
func (s *ActiveBackup) bestEligibleLocked() int {
	for i := range s.health {
		if s.health[i].State() == telemetry.StateUp {
			return i
		}
	}
	return -1
}

// setActiveLocked stores the active index and logs a transition only when it
// actually changes, so a saturated send path does not log on every Pick.
func (s *ActiveBackup) setActiveLocked(idx int, reason string) {
	if s.active == idx {
		return
	}
	from := s.active
	s.active = idx
	s.log.Info("scheduler active path change",
		"from", from,
		"to", idx,
		"reason", reason,
	)
}
