package sched

import (
	"github.com/7mind/wanbond/internal/telemetry"
)

// Scheduler is the send-side path-selection policy the multipath Bind consults
// for every outbound datagram. It is the extension seam for the send scheduler:
// active-backup failover (ActiveBackup, the P1 MVP) is one implementation; the
// later weighted / FEC-aware policy (T21) is a DIFFERENT implementation of this
// same interface, swapped in at the composition root with no Bind change.
type Scheduler interface {
	// Pick returns the index of the path the next datagram must egress on, in
	// the path priority order the scheduler was built over (index 0 = preferred
	// primary). It returns a negative value when no path is currently eligible.
	// Pick is safe for concurrent callers on the Bind's send path.
	//
	// Pick MAY be stateful: a weighted/aggregating scheduler advances its
	// distribution bookkeeping (deficit/round-robin credits, pacing tokens, offered-
	// load meter) on every call, so ONE Pick consumes ONE frame-selection slot.
	// Callers that only want the liveness-derived active set refreshed — without
	// perturbing distribution — MUST call Recompute, not Pick (see the T40 eager-
	// failover nudge in the multipath Bind).
	Pick() int

	// Recompute refreshes the scheduler's liveness-derived selection state (which
	// paths are eligible, which is the active primary) from the current PathHealth,
	// and logs any active-path transition, WITHOUT selecting or consuming a frame
	// slot: it advances no distribution/pacing/load state. It is the split-out
	// "recompute the eligible/active set from liveness" half of the old Pick, so the
	// eager-failover nudge (defect D18/T40) can drive an egress-lull failover recompute
	// without stealing a weighted-distribution slot the way a spurious Pick would. For
	// an idempotent scheduler (active-backup) it is exactly Pick without the return;
	// for a stateful one (weighted) it is strictly the non-consuming subset. It is
	// safe for concurrent callers and never calls back into the Bind.
	Recompute()
}

// PathQuality is the per-path measured-quality source the weighted scheduler reads
// to derive distribution weights: the read side of the T13 telemetry Estimate
// (RTT/Jitter/Loss), ANALOGOUS to PathHealth but exposing the quality snapshot
// rather than the up/down verdict. *telemetry.Prober satisfies BOTH PathHealth and
// PathQuality (its Estimate() is mutex-guarded, so it meets the same concurrency
// contract PathHealth documents), which is how one *Prober per path feeds liveness
// AND weight to the scheduler. The weighted scheduler's unit tests drive a synthetic
// PathQuality so the RTT/loss → weight formula is exercised on hand-built Estimates
// with no real probe stream.
type PathQuality interface {
	Estimate() telemetry.Estimate
}

// DynamicScheduler is a Scheduler whose path set can change at runtime (T30): a
// path may be admitted or dropped WITHOUT rebuilding the scheduler or disturbing
// the active selection of the surviving paths. The multipath Bind holds its
// scheduler as a plain Scheduler and type-asserts to this richer interface only
// when a runtime path add/remove is requested, so the Scheduler seam (and the
// T21 weighted policy that will implement it) is preserved: a scheduler that
// does not support dynamic membership simply cannot back a reloadable bind.
//
// The index space is the SAME priority-ordered index Pick returns, and the Bind
// keeps its own path slice index-aligned with this scheduler's path list, so an
// index returned by Pick always addresses the matching path. Both methods are
// safe for concurrent callers.
type DynamicScheduler interface {
	Scheduler
	// AddPath admits h as a NEW lowest-priority path (highest index) and returns
	// that index. Appending at the tail cannot change any existing path's index
	// nor steal the active selection from a higher-priority survivor: an
	// active-backup policy only ever selects the new path once every
	// higher-priority path is down. h must be non-nil.
	AddPath(h PathHealth) (int, error)
	// RemovePath drops the path at index i (shifting higher indices down by one)
	// and repairs the cached selection so a surviving path keeps being selected
	// and, if the dropped path was the active one, egress fails over to the best
	// remaining path on the next Pick. i must be in range.
	RemovePath(i int) error
	// SetPaths replaces the ENTIRE path/health list with health (priority order,
	// index 0 = preferred primary) and clears any cached selection, so the next
	// Pick re-derives the active path from live liveness. The Bind calls it on
	// every Open to re-align the scheduler's membership with the path slice it
	// rebuilds from the current definitions — the single reconciliation point that
	// makes a runtime path add/remove survive a Close→Open cycle index-aligned
	// (T30). It is safe for concurrent callers; health must be non-empty and hold
	// no nil element.
	SetPaths(health []PathHealth) error
}

// PathHealth is the per-path liveness the scheduler consumes: the read side of
// the T13 telemetry liveness state machine, so the scheduler reads exactly the
// up/down verdict T13 already computes (with its own probe-loss hysteresis)
// rather than duplicating detection here.
//
// Concurrency contract: Pick runs on the Bind send path CONCURRENTLY with the
// probe/timer goroutines that mutate liveness, so a production PathHealth MUST
// be INTERNALLY SYNCHRONIZED. *telemetry.Prober satisfies this — its State()
// takes the Prober's mutex (probe.go). A bare *telemetry.Liveness does NOT: it
// is not independently synchronized (probe.go: "must be reached only through
// the Prober") and its State() is an unguarded field read (liveness.go), so
// wiring a live, probe-driven *Liveness directly here is a data race — use a
// *Prober or an explicitly-locked adapter instead. (The sched unit tests drive
// a raw *Liveness on a single goroutine with no concurrent writer, which is not
// a race and is fine for testing.)
type PathHealth interface {
	State() telemetry.PathState
}

// AlwaysUp is a PathHealth that reports StateUp unconditionally. It is the
// composition-root placeholder used until the probe-transport wiring (a
// follow-up task) drives real per-path *telemetry.Prober liveness: with every
// path statically Up, the active-backup scheduler keeps all egress on the
// preferred primary (the data-thrift bring-up behaviour), and swapping in real
// Probers later activates failover with no further Bind or scheduler change.
type AlwaysUp struct{}

// State reports the path as up.
func (AlwaysUp) State() telemetry.PathState { return telemetry.StateUp }
