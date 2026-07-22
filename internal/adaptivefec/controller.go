package adaptivefec

import (
	"math"
	"time"
)

// Controller is the pure adaptive-FEC state machine. It consumes per-path loss
// samples via Observe and emits a target parity count M for a fixed group of K
// data shards, applying EWMA smoothing, a safety-factored redundancy map,
// hysteresis, a slew-rate limit, and a lower-side dwell (see the package doc).
//
// A Controller holds no globals and does no I/O; it reads time only through its
// injected Clock. It is NOT safe for concurrent use — its owner serializes
// access, exactly as internal/telemetry.Estimator does.
type Controller struct {
	cfg   Config
	clock Clock

	// raiseGate/lowerGate are the effective hysteresis band gates for the active sizing
	// mode: the configured RaiseThreshold/LowerThreshold in legacy SafetyFactor mode, or
	// the derived quantization-aware gates in residual-SLA mode (see Config.effectiveThresholds).
	raiseGate float64
	lowerGate float64

	// smoothed EWMA loss; seeded by the first sample.
	smoothed float64
	haveEWMA bool

	// m is the current target parity count.
	m int

	// lastChange is when m last moved; rate limiting is measured from it.
	lastChange   time.Time
	haveChanged  bool
	lowSince     time.Time // when smoothed loss last entered the <=LowerThreshold region
	haveLowSince bool
}

// NewController validates cfg and returns a Controller starting at M=0 (no
// standing redundancy until loss is observed). It fails fast on an invalid
// config or a nil clock.
func NewController(cfg Config, clock Clock) (*Controller, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if clock == nil {
		return nil, errNilClock
	}
	raise, lower := cfg.effectiveThresholds()
	return &Controller{cfg: cfg, clock: clock, raiseGate: raise, lowerGate: lower}, nil
}

// Observe folds one per-path loss sample, measured now (read from the injected
// Clock), into the controller and returns the resulting target parity count M.
// A loss outside [0,1] (including NaN) is a telemetry boundary artifact and is
// clamped rather than rejected, so a transient bad estimate cannot destabilize
// the loop.
func (c *Controller) Observe(loss float64) int {
	now := c.clock.Now()
	s := c.updateEWMA(clampLoss(loss))

	// Track how long smoothed loss has stayed in the lower (shed-eligible) region;
	// the dwell for a decrease is measured from lowSince.
	if s <= c.lowerGate {
		if !c.haveLowSince {
			c.lowSince = now
			c.haveLowSince = true
		}
	} else {
		c.haveLowSince = false
	}

	target := c.bandTarget(s, now)
	c.slewToward(target, now)
	return c.m
}

// updateEWMA folds one clamped sample into the smoothed loss and returns it. The
// first sample seeds the average directly.
func (c *Controller) updateEWMA(loss float64) float64 {
	if !c.haveEWMA {
		c.smoothed = loss
		c.haveEWMA = true
	} else {
		c.smoothed = c.cfg.Alpha*loss + (1-c.cfg.Alpha)*c.smoothed
	}
	return c.smoothed
}

// bandTarget applies the hysteresis bands to pick the parity level m should move
// toward, giving the loop three regions. The band gates are the EFFECTIVE
// raiseGate/lowerGate resolved at construction (Config.effectiveThresholds): the
// configured RaiseThreshold/LowerThreshold in legacy SafetyFactor mode, or the derived
// quantization-aware gates in residual-SLA mode.
//
//   - RAISE (s >= raiseGate): m may only INCREASE, toward the safety-factored
//     redundancy map. A decrease here would defeat the raise band, so it is
//     forbidden; this is why m does not oscillate while loss stays elevated.
//   - LOWER (s <= lowerGate): m COLLAPSES toward 0 — but only after the loss
//     has stayed quiet for the full Dwell. Being below the lower band is exactly
//     the "near-zero loss" condition, so the shed target is 0 (zero-at-zero), NOT
//     the raw map: the map's ceil() would pin any infinitesimal EWMA tail at M=1
//     and standing redundancy would never fully clear on a clean path.
//   - DEADBAND (in between): HOLD. A loss signal oscillating inside [lowerGate,raiseGate]
//     cannot move m, so it cannot flap.
//
// The raise-fast / lower-after-dwell asymmetry is the anti-thrash core.
func (c *Controller) bandTarget(s float64, now time.Time) int {
	switch {
	case s >= c.raiseGate:
		return maxInt(c.m, c.redundancyMap(s))
	case s <= c.lowerGate:
		if c.haveLowSince && now.Sub(c.lowSince) >= c.cfg.Dwell {
			return 0
		}
		return c.m
	default:
		return c.m
	}
}

// redundancyMap converts a smoothed loss fraction into the parity count M. In the
// RESIDUAL-SLA mode (TargetResidual set) it delegates to residualTargetParity,
// which inverts the binomial residual model to the smallest M meeting the target.
// Otherwise it uses the legacy SafetyFactor map: the smallest M whose group
// tolerates safety*loss, i.e. M/(K+M) >= safety*loss:
//
//	M = ceil( K * e / (1 - e) ),  e = safety*loss
//
// e saturates to MaxParity as e -> 1, and loss <= 0 maps to exactly 0 (zero
// overhead when clean). The result is clamped to [0, MaxParity]. Both modes are
// monotone non-decreasing and zero-at-zero, so the surrounding hysteresis and
// slew machinery is identical regardless of which mode is active.
func (c *Controller) redundancyMap(loss float64) int {
	if c.cfg.TargetResidual > 0 {
		return c.residualTargetParity(loss)
	}
	e := loss * c.cfg.SafetyFactor
	if e <= 0 {
		return 0
	}
	if e >= 1 {
		return c.cfg.MaxParity
	}
	m := int(math.Ceil(float64(c.cfg.DataShards) * e / (1 - e)))
	if m < 0 {
		m = 0
	}
	if m > c.cfg.MaxParity {
		m = c.cfg.MaxParity
	}
	return m
}

// slewToward moves m toward target subject to the rate limit: m changes by at
// most MaxStep, and no change may occur until RateInterval has elapsed since the
// previous change. The first change is permitted immediately.
func (c *Controller) slewToward(target int, now time.Time) {
	if target == c.m {
		return
	}
	if c.haveChanged && now.Sub(c.lastChange) < c.cfg.RateInterval {
		return
	}
	step := target - c.m
	if step > c.cfg.MaxStep {
		step = c.cfg.MaxStep
	} else if step < -c.cfg.MaxStep {
		step = -c.cfg.MaxStep
	}
	c.m += step
	c.lastChange = now
	c.haveChanged = true
}

// Parity returns the current target parity count M without advancing state.
func (c *Controller) Parity() int { return c.m }

// SmoothedLoss returns the current EWMA loss estimate. This is a read-only
// signal a future integration MAY also feed to the weighted scheduler (T21);
// this package wires nothing.
func (c *Controller) SmoothedLoss() float64 { return c.smoothed }

// Overhead returns the current parity fraction M/(K+M): the fraction of on-wire
// shards that are parity at the present target. 0 when M is 0.
func (c *Controller) Overhead() float64 {
	if c.m == 0 {
		return 0
	}
	return float64(c.m) / float64(c.cfg.DataShards+c.m)
}

func clampLoss(loss float64) float64 {
	if math.IsNaN(loss) || loss < 0 {
		return 0
	}
	if loss > 1 {
		return 1
	}
	return loss
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
