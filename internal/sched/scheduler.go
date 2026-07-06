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
	Pick() int
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
