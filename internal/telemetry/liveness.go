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
	// RideThrough is the extra down-side dwell an UP path tolerates before it is
	// marked DOWN: an UP path transitions DOWN only after silence exceeds
	// DownAfter+RideThrough. It adds down-side hysteresis so a brief micro-outage
	// (silence past DownAfter but within the dwell) does not flap a healthy path.
	// The DOWN-side streak-reset threshold and RecordEcho's consecutive-echo window
	// both stay DownAfter, so recovery and up-side hysteresis are unchanged. The
	// zero value is the historical behavior: an UP path goes DOWN at DownAfter.
	RideThrough time.Duration
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

// RecoveryBudget is the SINGLE SOURCE OF TRUTH for the P1 transparent-failover
// recovery deadline (D86 decision 4, honoring D16): after the active WAN is
// killed, the surviving flow must have throughput restored within this budget,
// in BOTH directions, with no connection reset. It is hoisted here — beside the
// Default* liveness timing it composes with — so the e2e acceptance table
// (test/e2e/thresholds.go) and the daemon's startup budget verdict
// (internal/config) both derive from ONE value instead of two tables that drift.
// test/e2e asserts RecoveryBudget == time.Duration(P1RecoverySeconds)*time.Second
// so the seconds-count and Duration representations can never diverge.
const RecoveryBudget = 3 * time.Second

// FailoverBudget is the PURE analytical per-direction failover-recovery bound for
// a path with the given liveness timing: the worst-case detect term (downAfter +
// one probe interval, the strict-'>' Tick needs one full interval past downAfter
// to fire) plus one interval of headroom that also absorbs the sub-ms reroute —
// i.e. downAfter + rideThrough + 2*probeInterval. rideThrough is the path's dwell
// (D86 decision 3): a path that rides through transient silence for rideThrough
// before being marked down adds exactly that dwell to the detect term. Both ends
// detect concurrently and symmetrically, so this bounds end-to-end BIDIRECTIONAL
// recovery. At the defaults (1200ms downAfter, 0 rideThrough, 200ms interval) it
// is 1.6s. Callers compare it against RecoveryBudget to judge budget sanity.
func FailoverBudget(downAfter, rideThrough, probeInterval time.Duration) time.Duration {
	return downAfter + rideThrough + 2*probeInterval
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
		l.transition(StateUp, now, l.cfg.DownAfter)
	}
}

// Tick re-evaluates liveness against the clock. An Up path with no heartbeat for
// longer than DownAfter+RideThrough transitions Down — the RideThrough dwell is the
// down-side hysteresis that lets a healthy path survive a micro-outage. A Down path
// that has gone silent past DownAfter has its partial up-streak reset, so recovery
// requires a fresh run of UpAfterSuccesses consecutive echoes rather than echoes
// accumulated before the silence; the reset threshold stays DownAfter (RideThrough
// governs only the up->down transition), so recovery semantics are unchanged. With
// RideThrough=0 the down transition also fires at DownAfter, identical to the
// historical behavior. Call Tick at least as often as the probe interval so detection
// latency stays within DownAfter+RideThrough plus one interval.
func (l *Liveness) Tick() {
	now := l.clock.Now()
	if !l.haveGood {
		return
	}
	silence := now.Sub(l.lastGood)
	downThreshold := l.cfg.DownAfter + l.cfg.RideThrough
	switch {
	case l.state == StateUp && silence > downThreshold:
		l.goodStreak = 0
		l.transition(StateDown, now, downThreshold)
	case l.state == StateDown && silence > l.cfg.DownAfter:
		l.goodStreak = 0
	}
}

// State returns the current liveness verdict.
func (l *Liveness) State() PathState { return l.state }

// transition records the state change and logs it with the effective threshold the
// silence had to cross: DownAfter+RideThrough for the up->down transition (the
// ride-through dwell) and the DownAfter consecutive-echo window for down->up.
func (l *Liveness) transition(to PathState, now time.Time, threshold time.Duration) {
	from := l.state
	l.state = to
	l.log.Info("path liveness transition",
		"from", from.String(),
		"to", to.String(),
		"silence_ms", now.Sub(l.lastGood).Milliseconds(),
		"threshold_ms", threshold.Milliseconds(),
	)
}
