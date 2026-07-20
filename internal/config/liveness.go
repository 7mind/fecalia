package config

import (
	"fmt"
	"time"
)

// defaultLivenessDownAfter mirrors telemetry.DefaultDownAfter (T203, D86 decision
// 3): internal/telemetry imports internal/config (probe.go), so config cannot
// import telemetry without a cycle — the value is restated here with this
// cross-reference, mirroring the defaultAdaptiveSafetyFactor/
// defaultAvgWireFrameBytes precedent elsewhere in this package. Applied by
// Liveness.applyDefaults when down_after is left unset, so an existing config
// with no [liveness] block keeps today's fixed 1200ms detection threshold
// byte-for-byte.
const defaultLivenessDownAfter = 1200 * time.Millisecond

// livenessProbeInterval mirrors telemetry.DefaultProbeInterval — the FIXED probe
// emission and liveness-Tick cadence. probe_interval is intentionally NOT a
// config knob this pass (T203/D86): the up/down detector's Tick performs its
// silence check every probe interval, so exposing probe_interval as an
// independent knob would let an operator shrink it out from under a
// separately-configured down_after (or vice versa), turning the floor below
// into a second validated cross-field relationship instead of one documented
// constant. It exists here only to derive minLivenessDownAfter.
const livenessProbeInterval = 200 * time.Millisecond

// minLivenessDownAfter is the R241 lower bound (D86 decision 3): down_after must
// be >= 2*livenessProbeInterval (400ms). Below this floor, the liveness Tick's
// strict-'>' silence check — invoked every probe interval — outruns the echo
// cadence: fewer than two probe intervals cannot even carry one round-trip
// echo, so every path permanently flaps DOWN regardless of actual link health.
// Enforced as a hard REJECT, not a WARN-and-clamp: silently clamping an
// operator's too-aggressive value would leave them believing they configured
// fast failover when they configured a permanently-down path instead. A large
// (over-budget) down_after is intentionally NOT rejected here — the
// WARN-and-allow upper-side budget verdict is T211's job (D86 decision 4).
const minLivenessDownAfter = 2 * livenessProbeInterval

// Liveness configures the D86 top-level up/down detection threshold (decision
// 3): down_after is the silence duration that marks an UP path DOWN,
// overriding the compiled-in telemetry.DefaultDownAfter. An ABSENT [liveness]
// block is INERT — DownAfter defaults to defaultLivenessDownAfter, byte-
// identical to today's hardcoded behaviour. This task (T203) adds ONLY the
// config surface (parse + default + validate); plumbing DownAfter into the
// running scheduler is T207's job — internal/device/device.go and
// internal/telemetry/liveness.go are untouched here.
type Liveness struct {
	// DownAfter is the silence duration that marks an UP path DOWN, parsed from
	// DownAfterRaw in applyDefaults. Defaults to defaultLivenessDownAfter when
	// DownAfterRaw is left empty. Must be > 0 and >= minLivenessDownAfter after
	// defaulting (validate).
	DownAfter time.Duration `toml:"-"`
	// DownAfterRaw is the TOML Go-duration string form of DownAfter, e.g.
	// "1200ms" / "2s" (mirrors FEC.DeadlineRaw, config.go:410-458 — go-toml/v2
	// cannot decode a TOML string directly into a bare time.Duration field).
	// Parsed in applyDefaults; an unparseable value fails fast.
	DownAfterRaw string `toml:"down_after"`
}

// applyDefaults fills DownAfter from DownAfterRaw, defaulting to
// defaultLivenessDownAfter when left empty — mirroring DNS.applyDefaults /
// FEC.parseDurations+applyDefaults. Only the parse itself is fail-fast here
// (unparseable duration syntax); the >0/floor range checks stay in validate,
// unchanged, matching the rest of this package's *Raw-field convention.
func (l *Liveness) applyDefaults() error {
	if l.DownAfterRaw == "" {
		l.DownAfter = defaultLivenessDownAfter
		return nil
	}
	d, err := time.ParseDuration(l.DownAfterRaw)
	if err != nil {
		return fmt.Errorf("liveness.down_after: invalid duration %q: %w", l.DownAfterRaw, err)
	}
	l.DownAfter = d
	return nil
}

// validate enforces the D86/R241 down_after floor (T203): down_after must be
// > 0 AND >= minLivenessDownAfter (2x the fixed probe interval, 400ms) — see
// minLivenessDownAfter's doc for why a lower value permanently flaps every
// path DOWN. A large down_after is accepted here; the upper-side WARN-and-
// allow budget verdict is T211's job (D86 decision 4), out of scope for this
// task.
func (l Liveness) validate() error {
	if l.DownAfter <= 0 {
		return fmt.Errorf("liveness.down_after must be > 0, got %s", l.DownAfter)
	}
	if l.DownAfter < minLivenessDownAfter {
		return fmt.Errorf("liveness.down_after must be >= %s (2x the fixed %s probe interval; a lower value makes the liveness Tick's silence check outrun the echo cadence and permanently flap every path DOWN), got %s",
			minLivenessDownAfter, livenessProbeInterval, l.DownAfter)
	}
	return nil
}
