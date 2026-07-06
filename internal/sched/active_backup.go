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
}

// compile-time proof ActiveBackup is a Scheduler.
var _ Scheduler = (*ActiveBackup)(nil)

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
	return &ActiveBackup{
		health:  health,
		clock:   clock,
		cfg:     cfg,
		log:     logger.Component("sched"),
		active:  -1,
		pending: -1,
	}, nil
}

// Pick returns the index of the current active path, recomputing the selection
// against live path health and the clock. It is safe for concurrent callers.
func (s *ActiveBackup) Pick() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recomputeLocked(s.clock.Now())
	return s.active
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
