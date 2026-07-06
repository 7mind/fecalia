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
