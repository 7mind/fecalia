package telemetry

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/log"
)

func livenessCfg() LivenessConfig {
	return LivenessConfig{DownAfter: 3 * time.Second, UpAfterSuccesses: 3}
}

// TestLivenessUpHysteresis asserts a Down path stays Down until UpAfterSuccesses
// consecutive echoes accumulate, then transitions Up — a single stray echo never
// declares the path healthy.
func TestLivenessUpHysteresis(t *testing.T) {
	clk := newFakeClock()
	l := NewLiveness("starlink", livenessCfg(), clk, discardLogger(t))

	if l.State() != StateDown {
		t.Fatalf("initial state = %v, want down", l.State())
	}
	l.RecordEcho()
	if l.State() != StateDown {
		t.Fatalf("after 1 echo state = %v, want down (need 3)", l.State())
	}
	l.RecordEcho()
	if l.State() != StateDown {
		t.Fatalf("after 2 echoes state = %v, want down (need 3)", l.State())
	}
	l.RecordEcho()
	if l.State() != StateUp {
		t.Fatalf("after 3 echoes state = %v, want up", l.State())
	}
}

// TestLivenessStaleEchoesDoNotFlapUp is the regression for the silence-hysteresis
// defect: UpAfterSuccesses echoes separated by silence longer than DownAfter must
// NOT bring a Down path Up, because they are not consecutive. Reproduces the
// reported failure (DownAfter=1s, 3 echoes 10 minutes apart).
func TestLivenessStaleEchoesDoNotFlapUp(t *testing.T) {
	clk := newFakeClock()
	cfg := LivenessConfig{DownAfter: time.Second, UpAfterSuccesses: 3}
	l := NewLiveness("starlink", cfg, clk, discardLogger(t))

	for i := 0; i < 3; i++ {
		l.RecordEcho()
		if l.State() != StateDown {
			t.Fatalf("stale echo %d flapped path up (silence resets the streak)", i)
		}
		clk.advance(10 * time.Minute)
	}
}

// TestLivenessTickResetsStalePartialStreak asserts that a partial up-streak
// accumulated while Down is reset by a Tick after the staleness threshold, so
// recovery requires a fresh consecutive run — not echoes bridged across silence.
func TestLivenessTickResetsStalePartialStreak(t *testing.T) {
	clk := newFakeClock()
	cfg := LivenessConfig{DownAfter: time.Second, UpAfterSuccesses: 3}
	l := NewLiveness("starlink", cfg, clk, discardLogger(t))

	// Accumulate 2 of the 3 echoes needed (still Down).
	l.RecordEcho()
	l.RecordEcho()
	if l.State() != StateDown {
		t.Fatalf("state = %v after 2 echoes, want down", l.State())
	}
	// Silence past the threshold, then a Tick clears the partial streak.
	clk.advance(cfg.DownAfter + time.Millisecond)
	l.Tick()
	// A single fresh echo must now leave the streak at 1, not reach 3.
	l.RecordEcho()
	if l.State() != StateDown {
		t.Fatalf("state = %v: stale partial streak was not reset by Tick", l.State())
	}
	// Two more consecutive echoes complete the fresh run and bring it up.
	l.RecordEcho()
	l.RecordEcho()
	if l.State() != StateUp {
		t.Fatalf("state = %v after a fresh consecutive run, want up", l.State())
	}
}

// TestLivenessDownDetection asserts an Up path is marked Down only after silence
// strictly exceeds DownAfter, and not before.
func TestLivenessDownDetection(t *testing.T) {
	clk := newFakeClock()
	cfg := livenessCfg()
	l := NewLiveness("starlink", cfg, clk, discardLogger(t))

	for i := 0; i < cfg.UpAfterSuccesses; i++ {
		l.RecordEcho()
	}
	if l.State() != StateUp {
		t.Fatalf("state = %v, want up", l.State())
	}

	// Silence just under the threshold: still up.
	clk.advance(cfg.DownAfter)
	l.Tick()
	if l.State() != StateUp {
		t.Fatalf("at exactly DownAfter state = %v, want up (strict >)", l.State())
	}

	// Cross the threshold: down.
	clk.advance(time.Millisecond)
	l.Tick()
	if l.State() != StateDown {
		t.Fatalf("past DownAfter state = %v, want down", l.State())
	}
}

// TestLivenessRideThroughSurvivesMicroOutage is the D86 root-cause reproduction:
// with a positive RideThrough dwell, an UP path that goes silent past DownAfter but
// well within DownAfter+RideThrough must NOT flap DOWN. It models the field failure
// (DownAfter=1s, a 1.3s micro-outage) and fails before the fix, when Tick marks the
// path DOWN at DownAfter regardless of RideThrough.
func TestLivenessRideThroughSurvivesMicroOutage(t *testing.T) {
	clk := newFakeClock()
	cfg := LivenessConfig{DownAfter: time.Second, UpAfterSuccesses: 3, RideThrough: 2 * time.Second}
	l := NewLiveness("starlink", cfg, clk, discardLogger(t))

	for i := 0; i < cfg.UpAfterSuccesses; i++ {
		l.RecordEcho()
	}
	if l.State() != StateUp {
		t.Fatalf("state = %v, want up", l.State())
	}

	// A 1.3s micro-outage exceeds DownAfter (1s) but is far inside the
	// DownAfter+RideThrough (3s) dwell: the UP path must ride through it.
	clk.advance(1300 * time.Millisecond)
	l.Tick()
	if l.State() != StateUp {
		t.Fatalf("state = %v after a 1.3s micro-outage, want up (ride-through dwell)", l.State())
	}
}

// TestLivenessRideThroughEventualDown asserts the dwell is bounded: an UP path stays
// UP at exactly DownAfter+RideThrough (strict >) and transitions DOWN one tick past
// it. Before the fix it fails, because the path is already DOWN at DownAfter.
func TestLivenessRideThroughEventualDown(t *testing.T) {
	clk := newFakeClock()
	cfg := LivenessConfig{DownAfter: time.Second, UpAfterSuccesses: 3, RideThrough: 2 * time.Second}
	l := NewLiveness("starlink", cfg, clk, discardLogger(t))

	for i := 0; i < cfg.UpAfterSuccesses; i++ {
		l.RecordEcho()
	}

	// Silence at exactly the DownAfter+RideThrough dwell: still up (strict >).
	clk.advance(cfg.DownAfter + cfg.RideThrough)
	l.Tick()
	if l.State() != StateUp {
		t.Fatalf("at exactly DownAfter+RideThrough state = %v, want up (strict >)", l.State())
	}

	// One tick past the dwell: down.
	clk.advance(time.Millisecond)
	l.Tick()
	if l.State() != StateDown {
		t.Fatalf("past DownAfter+RideThrough state = %v, want down", l.State())
	}
}

// TestLivenessRecovery asserts a path that flapped Down recovers to Up after the
// hysteresis count of fresh echoes.
func TestLivenessRecovery(t *testing.T) {
	clk := newFakeClock()
	cfg := livenessCfg()
	l := NewLiveness("cellular", cfg, clk, discardLogger(t))

	for i := 0; i < cfg.UpAfterSuccesses; i++ {
		l.RecordEcho()
	}
	clk.advance(cfg.DownAfter + time.Second)
	l.Tick()
	if l.State() != StateDown {
		t.Fatalf("state = %v, want down before recovery", l.State())
	}

	for i := 0; i < cfg.UpAfterSuccesses-1; i++ {
		l.RecordEcho()
	}
	if l.State() != StateDown {
		t.Fatalf("mid-recovery state = %v, want down", l.State())
	}
	l.RecordEcho()
	if l.State() != StateUp {
		t.Fatalf("post-recovery state = %v, want up", l.State())
	}
}

// TestLivenessTransitionLogged asserts the down transition is logged with the
// per-path field, satisfying the acceptance requirement that the transition is
// observable per path.
func TestLivenessTransitionLogged(t *testing.T) {
	clk := newFakeClock()
	cfg := livenessCfg()
	var buf bytes.Buffer
	logger, err := log.New("debug", &buf)
	if err != nil {
		t.Fatalf("build logger: %v", err)
	}
	l := NewLiveness("starlink", cfg, clk, logger)

	for i := 0; i < cfg.UpAfterSuccesses; i++ {
		l.RecordEcho()
	}
	clk.advance(cfg.DownAfter + time.Second)
	l.Tick()

	out := buf.String()
	if !strings.Contains(out, `"`+log.FieldPath+`":"starlink"`) {
		t.Fatalf("transition log missing per-path field %q:\n%s", log.FieldPath, out)
	}
	if !strings.Contains(out, `"to":"down"`) {
		t.Fatalf("transition log missing to=down:\n%s", out)
	}
}
