package adaptivefec

import (
	"fmt"
	"math"
	"time"
)

// MaxShards mirrors the Reed-Solomon field limit enforced in internal/fec
// (maxShards) and restated in internal/config (maxFECShards): a coding group
// carries at most 256 shards (data + parity) total over GF(2^8). It is mirrored
// here — with this cross-reference rather than an import — so the controller can
// fail-fast on an out-of-field parity bound without coupling to the datapath.
const MaxShards = 256

// Documented default control-law constants. They are the SINGLE SOURCE OF TRUTH
// for the tuning the simulation harness exercises; DefaultConfig assembles them.
// Every value is named and justified rather than appearing as a magic number in
// the algorithm.
const (
	// DefaultDataShards is K, the fixed number of data shards per coding group the
	// controller sizes parity for. Chosen to match a typical FEC group.
	DefaultDataShards = 10

	// DefaultMaxParity caps M at 100% overhead (M == K): beyond a 1:1 parity ratio
	// the marginal recovery per byte of overhead is poor, so the loop saturates
	// there rather than spending unbounded bandwidth under pathological loss.
	DefaultMaxParity = 10

	// DefaultAlpha is the EWMA weight of the newest loss sample. 0.3 keeps ~3-4
	// samples of memory: heavy enough to reject single-sample telemetry noise, light
	// enough to react to a genuine sustained shift within a few probe intervals.
	DefaultAlpha = 0.3

	// DefaultRaiseThreshold is the smoothed-loss level (5%) at/above which the loop
	// starts adding parity. Below it, occasional loss is cheaper to absorb via
	// retransmit/latency than to mask with standing redundancy.
	DefaultRaiseThreshold = 0.05

	// DefaultLowerThreshold is the smoothed-loss level (2%) at/below which the loop
	// is eligible to shed parity. The gap to RaiseThreshold is the hysteresis
	// deadband: loss wandering between 2% and 5% neither raises nor lowers M.
	DefaultLowerThreshold = 0.02

	// DefaultSafetyFactor is the headroom over the mean loss the parity is sized to
	// mask. 1.5 sizes each group to tolerate 1.5x the smoothed loss, covering the
	// binomial variance by which an individual group exceeds the mean. It applies
	// only when TargetResidual is unset (the legacy SafetyFactor sizing mode).
	DefaultSafetyFactor = 1.5

	// DefaultMaxStep bounds |ΔM| per RateInterval. A single spike can move M by at
	// most 2 parity shards per interval, so redundancy slews rather than jumps.
	DefaultMaxStep = 2

	// DefaultRateInterval is the minimum wall time between two M changes. Combined
	// with MaxStep it caps the redundancy slew rate at MaxStep shards per 500ms.
	DefaultRateInterval = 500 * time.Millisecond

	// DefaultDwell is how long smoothed loss must stay at/below LowerThreshold before
	// the first decrease. 3s of quiet is required to shed parity, so a brief lull in
	// a lossy period does not prematurely strip protection (raise fast, lower slow).
	DefaultDwell = 3 * time.Second
)

// Config parameterizes the adaptive FEC controller. All fields are validated at
// construction (fail-fast): a mis-tuned band or an out-of-field parity bound is
// rejected by NewController rather than producing undefined control behavior at
// runtime.
type Config struct {
	// DataShards is K, the fixed group size the controller sizes parity for. >= 1.
	DataShards int
	// MaxParity is the upper bound on the emitted M. >= 1 and DataShards+MaxParity
	// <= MaxShards (the Reed-Solomon field limit).
	MaxParity int
	// Alpha is the EWMA weight of the newest loss sample, in (0,1]. Smaller = more
	// smoothing.
	Alpha float64
	// RaiseThreshold is the smoothed-loss level at/above which M may rise. In
	// (LowerThreshold, 1). It is the effective raise gate ONLY in legacy SafetyFactor
	// mode; in residual-SLA mode (TargetResidual set) the raise gate is DERIVED from
	// TargetResidual (see effectiveThresholds) and this field only supplies the
	// deadband SHAPE (its ratio to LowerThreshold), not the absolute gate.
	RaiseThreshold float64
	// LowerThreshold is the smoothed-loss level at/below which M may fall. In
	// [0, RaiseThreshold). The gap to RaiseThreshold is the hysteresis deadband. As
	// with RaiseThreshold, in residual-SLA mode this is not the absolute lower gate —
	// the derived lower gate preserves this field's LowerThreshold/RaiseThreshold ratio
	// scaled onto the derived raise gate (see effectiveThresholds).
	LowerThreshold float64
	// SafetyFactor is the headroom multiplier on the smoothed loss the parity is
	// sized to mask. >= 1 in the SafetyFactor sizing mode (TargetResidual unset);
	// MUST be 0 when TargetResidual is set (the two modes are mutually exclusive).
	SafetyFactor float64
	// TargetResidual, when set (> 0), selects the RESIDUAL-SLA sizing mode and is
	// the PRIMARY parity surface: instead of the bare SafetyFactor multiplier the
	// controller derives M by inverting the binomial residual model
	// E[max(0,D-M)]/K (D ~ Bin(K, smoothed loss), see residual.go) to the smallest
	// M whose modeled residual is <= TargetResidual, clamped to [0, MaxParity]. It
	// SUPERSEDES SafetyFactor (which must be 0 when this is set). Must be in (0,1)
	// when set; 0 selects the legacy SafetyFactor path.
	//
	// When set it ALSO derives the hysteresis raise gate quantization-aware (D96):
	// max(TargetResidual, minRaiseGateQuanta*lossQuantum), so a sub-5% loss that would
	// miss the SLA raises M instead of being pinned by the fixed RaiseThreshold, while
	// a single loss quantum cannot flap parity (see effectiveThresholds).
	TargetResidual float64
	// MaxStep bounds |ΔM| applied per RateInterval. >= 1.
	MaxStep int
	// RateInterval is the minimum time between two M changes. > 0.
	RateInterval time.Duration
	// Dwell is the minimum time smoothed loss must stay at/below LowerThreshold
	// before the first decrease. >= 0.
	Dwell time.Duration
}

// lossWindowSize mirrors telemetry.defaultLossWindow (512): the per-path loss
// estimator reports the trailing-window loss fraction quantized to 1/window, so the
// smallest representable non-zero loss it can report is one quantum = 1/lossWindowSize
// (~0.00195). It is mirrored here — with this cross-reference rather than an import,
// exactly like MaxShards — so the residual-mode raise gate can be sized in estimator
// quanta without coupling the controller to internal/telemetry.
const lossWindowSize = 512

// lossQuantum is the width of one loss-estimator quantum, 1/lossWindowSize (~0.00195):
// the granularity below which the measured loss cannot be distinguished from zero.
const lossQuantum = 1.0 / lossWindowSize

// minRaiseGateQuanta floors the residual-SLA raise gate at this many loss quanta. The
// residual-mode crossover loss (where residualTargetParity first returns >=1) equals
// TargetResidual, which for a tight SLA (e.g. 0.001) sits BELOW one quantum and is
// therefore ill-posed against estimator quantization: a single sustained lost probe
// (one quantum) would otherwise cross it and flap parity 0<->1 at the dwell/slew
// cadence. Flooring the gate at two quanta guarantees the smallest raise-triggering
// loss is >= 2 quanta, so one quantum of loss cannot raise M. Must be >= 2.
const minRaiseGateQuanta = 2

// effectiveThresholds returns the raise/lower band gates the control law actually uses,
// resolving the sizing mode:
//
//   - LEGACY SafetyFactor mode (TargetResidual unset): the configured
//     RaiseThreshold/LowerThreshold VERBATIM, so that path is byte-for-byte unchanged.
//   - RESIDUAL-SLA mode (TargetResidual set): a DERIVED, quantization-aware band. The
//     raise gate is the crossover loss where residualTargetParity first returns >=1 —
//     which equals TargetResidual, since binomialResidual(K,loss,0)==loss — floored to
//     minRaiseGateQuanta quanta so estimator quantization cannot flap parity. The lower
//     gate preserves the configured deadband SHAPE (its LowerThreshold/RaiseThreshold
//     ratio) scaled onto the derived raise gate, keeping the raise-fast / lower-after-
//     dwell hysteresis with a non-empty deadband. Validate rejects a config whose
//     derived deadband would collapse below one quantum.
func (c Config) effectiveThresholds() (raise, lower float64) {
	if c.TargetResidual <= 0 {
		return c.RaiseThreshold, c.LowerThreshold
	}
	raise = c.TargetResidual // crossover: residualTargetParity(loss)>=1 iff loss>TargetResidual
	if floor := minRaiseGateQuanta * lossQuantum; floor > raise {
		raise = floor
	}
	ratio := 0.0
	if c.RaiseThreshold > 0 {
		ratio = c.LowerThreshold / c.RaiseThreshold // configured deadband shape, in [0,1)
	}
	lower = raise * ratio
	return raise, lower
}

// DefaultConfig returns a Config populated from the documented default control
// constants.
func DefaultConfig() Config {
	return Config{
		DataShards:     DefaultDataShards,
		MaxParity:      DefaultMaxParity,
		Alpha:          DefaultAlpha,
		RaiseThreshold: DefaultRaiseThreshold,
		LowerThreshold: DefaultLowerThreshold,
		SafetyFactor:   DefaultSafetyFactor,
		MaxStep:        DefaultMaxStep,
		RateInterval:   DefaultRateInterval,
		Dwell:          DefaultDwell,
	}
}

// Validate enforces the control-law invariants, failing fast so a mis-tuned
// controller is rejected at construction rather than misbehaving at runtime.
func (c Config) Validate() error {
	if c.DataShards < 1 {
		return fmt.Errorf("adaptivefec: DataShards (K) must be >= 1, got %d", c.DataShards)
	}
	if c.MaxParity < 1 {
		return fmt.Errorf("adaptivefec: MaxParity must be >= 1, got %d", c.MaxParity)
	}
	if c.DataShards+c.MaxParity > MaxShards {
		return fmt.Errorf("adaptivefec: DataShards+MaxParity must be <= %d (Reed-Solomon field limit), got %d", MaxShards, c.DataShards+c.MaxParity)
	}
	if !(c.Alpha > 0 && c.Alpha <= 1) || math.IsNaN(c.Alpha) {
		return fmt.Errorf("adaptivefec: Alpha must be in (0,1], got %g", c.Alpha)
	}
	if math.IsNaN(c.LowerThreshold) || c.LowerThreshold < 0 {
		return fmt.Errorf("adaptivefec: LowerThreshold must be >= 0, got %g", c.LowerThreshold)
	}
	if math.IsNaN(c.RaiseThreshold) || c.RaiseThreshold >= 1 {
		return fmt.Errorf("adaptivefec: RaiseThreshold must be < 1, got %g", c.RaiseThreshold)
	}
	if c.LowerThreshold >= c.RaiseThreshold {
		return fmt.Errorf("adaptivefec: LowerThreshold (%g) must be < RaiseThreshold (%g) so the hysteresis deadband is non-empty", c.LowerThreshold, c.RaiseThreshold)
	}
	if math.IsNaN(c.TargetResidual) {
		return fmt.Errorf("adaptivefec: TargetResidual must be a number, got NaN")
	}
	if c.TargetResidual != 0 {
		// Residual-SLA sizing mode: TargetResidual governs and SafetyFactor is inert,
		// so the two must not both be set (mutually exclusive, fail-fast).
		if c.TargetResidual < 0 || c.TargetResidual >= 1 {
			return fmt.Errorf("adaptivefec: TargetResidual must be in (0,1) when set, got %g", c.TargetResidual)
		}
		if c.SafetyFactor != 0 {
			return fmt.Errorf("adaptivefec: SafetyFactor and TargetResidual are mutually exclusive (TargetResidual is the primary residual-SLA surface); leave SafetyFactor 0 when TargetResidual is set, got SafetyFactor=%g", c.SafetyFactor)
		}
		// The band the control law actually uses in residual mode is DERIVED (see
		// effectiveThresholds), not the raw RaiseThreshold/LowerThreshold. Fail fast if the
		// derivation yields an inverted band or a deadband narrower than one loss quantum:
		// either would defeat the hysteresis (dwell/slew) the loop relies on to not flap.
		raise, lower := c.effectiveThresholds()
		if lower >= raise {
			return fmt.Errorf("adaptivefec: derived residual-mode band inverted: lower gate %g >= raise gate %g", lower, raise)
		}
		if raise-lower < lossQuantum {
			return fmt.Errorf("adaptivefec: derived residual-mode deadband %g is narrower than one loss quantum %g (widen the configured LowerThreshold/RaiseThreshold gap so the hysteresis holds)", raise-lower, lossQuantum)
		}
	} else if math.IsNaN(c.SafetyFactor) || math.IsInf(c.SafetyFactor, 0) || c.SafetyFactor < 1 {
		return fmt.Errorf("adaptivefec: SafetyFactor must be a finite value >= 1 (or set TargetResidual instead), got %g", c.SafetyFactor)
	}
	if c.MaxStep < 1 {
		return fmt.Errorf("adaptivefec: MaxStep must be >= 1, got %d", c.MaxStep)
	}
	if c.RateInterval <= 0 {
		return fmt.Errorf("adaptivefec: RateInterval must be > 0, got %s", c.RateInterval)
	}
	if c.Dwell < 0 {
		return fmt.Errorf("adaptivefec: Dwell must be >= 0, got %s", c.Dwell)
	}
	return nil
}
