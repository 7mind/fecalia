package adaptivefec

import (
	"math"
	"testing"
	"time"
)

// Fail-fast construction: DefaultConfig is valid, and every individually broken
// invariant is rejected by NewController rather than misbehaving at runtime.
func TestNewControllerRejectsInvalidConfig(t *testing.T) {
	clk := newFakeClock()
	if _, err := NewController(DefaultConfig(), clk); err != nil {
		t.Fatalf("DefaultConfig should be valid, got %v", err)
	}
	if _, err := NewController(DefaultConfig(), nil); err == nil {
		t.Fatalf("nil clock should be rejected")
	}

	base := DefaultConfig()
	cases := map[string]func(c *Config){
		"K<1":                   func(c *Config) { c.DataShards = 0 },
		"MaxParity<1":           func(c *Config) { c.MaxParity = 0 },
		"K+MaxParity>MaxShards": func(c *Config) { c.DataShards = 200; c.MaxParity = 100 },
		"alpha=0":               func(c *Config) { c.Alpha = 0 },
		"alpha>1":               func(c *Config) { c.Alpha = 1.5 },
		"alphaNaN":              func(c *Config) { c.Alpha = math.NaN() },
		"lower<0":               func(c *Config) { c.LowerThreshold = -0.1 },
		"raise>=1":              func(c *Config) { c.RaiseThreshold = 1.0 },
		"lower>=raise":          func(c *Config) { c.LowerThreshold = 0.06; c.RaiseThreshold = 0.05 },
		"safety<1":              func(c *Config) { c.SafetyFactor = 0.9 },
		"safetyInf":             func(c *Config) { c.SafetyFactor = math.Inf(1) },
		"maxStep<1":             func(c *Config) { c.MaxStep = 0 },
		"rateInterval<=0":       func(c *Config) { c.RateInterval = 0 },
		"dwell<0":               func(c *Config) { c.Dwell = -time.Second },
	}
	for name, mutate := range cases {
		cfg := base
		mutate(&cfg)
		if _, err := NewController(cfg, clk); err == nil {
			t.Errorf("%s: expected construction error, got nil", name)
		}
	}
}

// Observe clamps a telemetry loss outside [0,1] (including NaN) rather than
// panicking or letting it destabilize the smoothed estimate.
func TestObserveClampsOutOfRangeLoss(t *testing.T) {
	ctrl, clk := newDefaultController(t)
	for _, l := range []float64{-1, 2, math.NaN(), math.Inf(1), 0.5} {
		ctrl.Observe(l)
		clk.advance(simStep)
		if s := ctrl.SmoothedLoss(); s < 0 || s > 1 || math.IsNaN(s) {
			t.Fatalf("smoothed loss out of [0,1] after loss=%g: %g", l, s)
		}
	}
}

// The redundancy map yields exactly 0 at 0 loss and saturates at MaxParity, and
// is monotone non-decreasing — the properties the control law relies on.
func TestRedundancyMapBoundsAndMonotonicity(t *testing.T) {
	ctrl, _ := newDefaultController(t)
	if m := ctrl.redundancyMap(0); m != 0 {
		t.Fatalf("redundancyMap(0)=%d, want 0", m)
	}
	if m := ctrl.redundancyMap(1.0); m != DefaultMaxParity {
		t.Fatalf("redundancyMap(1.0)=%d, want MaxParity %d", m, DefaultMaxParity)
	}
	prev := -1
	for p := 0.0; p <= 1.0+1e-9; p += 0.01 {
		m := ctrl.redundancyMap(p)
		if m < 0 || m > DefaultMaxParity {
			t.Fatalf("redundancyMap(%.2f)=%d out of [0,%d]", p, m, DefaultMaxParity)
		}
		if m < prev {
			t.Fatalf("redundancyMap not monotone: dropped to %d at p=%.2f", m, p)
		}
		prev = m
		// The mapped group must actually tolerate safety*loss (recover-with-margin).
		if m > 0 && m < DefaultMaxParity {
			tol := float64(m) / float64(DefaultDataShards+m)
			need := p * DefaultSafetyFactor
			if tol < need-1e-9 {
				t.Fatalf("redundancyMap(%.2f)=%d tolerates %.4f < required %.4f", p, m, tol, need)
			}
		}
	}
}
