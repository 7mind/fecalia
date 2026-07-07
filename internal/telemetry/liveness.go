package telemetry

import (
	"time"

	"github.com/7mind/wanbond/internal/log"
)

// Clock is the injectable time source the liveness machinery reads. Production
// code passes SystemClock; tests pass a hand-advanced fake so transitions are
// deterministic and instant.
type Clock interface {
	Now() time.Time
}

// SystemClock is the wall-clock Clock.
type SystemClock struct{}

// Now returns the current wall-clock time.
func (SystemClock) Now() time.Time { return time.Now() }

// PathState is the liveness verdict for one path. Path liveness is entirely this
// codec's concern: the inner WireGuard keepalive is per-peer, not per-path, so a
// dead uplink under a live peer is invisible to WireGuard and must be detected
// here.
type PathState uint8

const (
	// StateDown means no fresh probe echo has been observed within the detection
	// threshold; the path is presumed unusable.
	StateDown PathState = iota
	// StateUp means the path has recently returned probe echoes.
	StateUp
)

// String renders the state as the lowercase token used in structured logs.
func (s PathState) String() string {
	switch s {
	case StateUp:
		return "up"
	case StateDown:
		return "down"
	default:
		return "unknown"
	}
}

// LivenessConfig holds the up/down detection thresholds. DownAfter is the
// silence duration that marks an up path down; UpAfterSuccesses is the number of
// consecutive echoes required to bring a down path up. The asymmetry is the
// hysteresis: a single lost echo never flaps the path, and a single recovered
// echo never prematurely declares it healthy.
type LivenessConfig struct {
	DownAfter        time.Duration
	UpAfterSuccesses int
}

// Default per-path liveness detection timing (T13/T37/T39). These are the SINGLE
// SOURCE OF TRUTH for probe cadence and up/down hysteresis. The daemon
// (internal/device) builds its ProberConfig from them and runs its probe loop at
// DefaultProbeInterval; the e2e acceptance table (test/e2e/thresholds.go) derives
// its PLiveness* budget from them. Neither can silently diverge from the timing the
// daemon actually runs, which is the D16 reconciliation: one place to retune.
//
//   - DefaultDownAfter is the silence that marks an UP path DOWN.
//   - DefaultProbeInterval is the probe emission AND liveness Tick cadence; detection
//     latency is therefore bounded by DownAfter + one interval.
//   - DefaultUpSuccesses is the consecutive echoes that bring a DOWN path UP.
//   - DefaultLossWindow (0) takes the estimator's default trailing window.
//
// DownAfter spans DefaultProbeInterval*6, so SIX consecutive lost echoes are needed
// before a false DOWN — the D13 jitter-safety margin. T39 tightened the pair from
// 1500ms/250ms to 1200ms/200ms to WIDEN the P1 3s-recovery margin while KEEPING
// that six-lost-echo false-down tolerance exactly (the ratio is unchanged). Detection
// latency drops from ~1.75s to ~1.4s worst case, and the receive-path liveness sweep
// (bind, D15) makes that detection robust to probe-loop-ticker starvation under load.
const (
	DefaultProbeInterval = 200 * time.Millisecond
	DefaultDownAfter     = 1200 * time.Millisecond
	DefaultUpSuccesses   = 3
	DefaultLossWindow    = 0
)

// Liveness is the per-path up/down state machine. It is driven by two events:
// RecordEcho on every authenticated probe echo, and Tick on a periodic timer.
// It starts Down and requires UpAfterSuccesses echoes before declaring Up, so a
// path is never reported healthy before it has actually answered.
type Liveness struct {
	cfg   LivenessConfig
	clock Clock
	log   log.Logger

	state      PathState
	lastGood   time.Time
	haveGood   bool
	goodStreak int
}

// NewLiveness builds a per-path liveness machine. The supplied logger is tagged
// with the path name so every transition record carries the per-path field.
func NewLiveness(pathName string, cfg LivenessConfig, clock Clock, logger log.Logger) *Liveness {
	return &Liveness{
		cfg:   cfg,
		clock: clock,
		log:   logger.Path(pathName),
		state: StateDown,
	}
}

// RecordEcho registers a successful, authenticated probe echo as a liveness
// heartbeat. Only echoes that are CONSECUTIVE — no more than DownAfter apart —
// accumulate toward Up; an echo that arrives after a longer silence starts the
// streak over, so UpAfterSuccesses stray echoes spread across arbitrary silence
// never bring a Down path Up. Once UpAfterSuccesses consecutive heartbeats
// accumulate, a Down path transitions Up.
func (l *Liveness) RecordEcho() {
	now := l.clock.Now()
	if l.haveGood && now.Sub(l.lastGood) > l.cfg.DownAfter {
		l.goodStreak = 0 // silence broke the run of consecutive echoes
	}
	l.lastGood = now
	l.haveGood = true
	l.goodStreak++
	if l.state == StateDown && l.goodStreak >= l.cfg.UpAfterSuccesses {
		l.transition(StateUp, now)
	}
}

// Tick re-evaluates liveness against the clock. An Up path with no heartbeat for
// longer than DownAfter transitions Down; a Down path that has gone silent past
// DownAfter has its partial up-streak reset, so recovery requires a fresh run of
// UpAfterSuccesses consecutive echoes rather than echoes accumulated before the
// silence. Call Tick at least as often as the probe interval so detection latency
// stays within DownAfter plus one interval.
func (l *Liveness) Tick() {
	now := l.clock.Now()
	if !l.haveGood {
		return
	}
	stale := now.Sub(l.lastGood) > l.cfg.DownAfter
	switch {
	case l.state == StateUp && stale:
		l.goodStreak = 0
		l.transition(StateDown, now)
	case l.state == StateDown && stale:
		l.goodStreak = 0
	}
}

// State returns the current liveness verdict.
func (l *Liveness) State() PathState { return l.state }

func (l *Liveness) transition(to PathState, now time.Time) {
	from := l.state
	l.state = to
	l.log.Info("path liveness transition",
		"from", from.String(),
		"to", to.String(),
		"silence_ms", now.Sub(l.lastGood).Milliseconds(),
	)
}
