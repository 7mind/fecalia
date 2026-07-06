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
// heartbeat. Once UpAfterSuccesses consecutive heartbeats accumulate, a Down
// path transitions Up.
func (l *Liveness) RecordEcho() {
	now := l.clock.Now()
	l.lastGood = now
	l.haveGood = true
	l.goodStreak++
	if l.state == StateDown && l.goodStreak >= l.cfg.UpAfterSuccesses {
		l.transition(StateUp, now)
	}
}

// Tick re-evaluates liveness against the clock. An Up path with no heartbeat for
// longer than DownAfter transitions Down. Call it at least as often as the
// probe interval so detection latency stays within DownAfter plus one interval.
func (l *Liveness) Tick() {
	now := l.clock.Now()
	if l.state == StateUp && l.haveGood && now.Sub(l.lastGood) > l.cfg.DownAfter {
		l.goodStreak = 0
		l.transition(StateDown, now)
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
