package config

import (
	"math"
	"strings"
	"testing"
	"time"
)

// abPacing builds the [scheduler] block selecting the active-backup policy with the
// given trailing knob lines appended (e.g. "pacing_enabled = true"). The policy is
// stated explicitly rather than relying on the omitted-block default so the fixtures
// read unambiguously.
func abPacing(knobs string) string {
	return "\n[scheduler]\npolicy = \"active-backup\"\n" + knobs
}

// TestActiveBackupPacingExplicitKnobs (acceptance i): active-backup + pacing_enabled
// with explicit per_path_capacity_fps + pacing_burst_frames loads and validates, and
// the explicit scalar knobs are replicated into the per-path vectors T153 consumes.
func TestActiveBackupPacingExplicitKnobs(t *testing.T) {
	body := fill(edgeConfig) + abPacing("pacing_enabled = true\nper_path_capacity_fps = 5000\npacing_burst_frames = 32\n")
	path := writeConfig(t, 0o600, body)
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !c.Scheduler.PacingEnabled {
		t.Fatal("pacing_enabled must be true")
	}
	if len(c.Scheduler.PerPathCapacities) != 2 || len(c.Scheduler.PacingBursts) != 2 {
		t.Fatalf("per-path vectors = %v / %v, want length 2 each", c.Scheduler.PerPathCapacities, c.Scheduler.PacingBursts)
	}
	for i, cap := range c.Scheduler.PerPathCapacities {
		if cap != 5000 {
			t.Fatalf("PerPathCapacities[%d] = %g, want 5000 (explicit scalar replicated per path)", i, cap)
		}
	}
	for i, b := range c.Scheduler.PacingBursts {
		if b != 32 {
			t.Fatalf("PacingBursts[%d] = %g, want 32 (explicit scalar replicated per path)", i, b)
		}
	}
	// Weighted-only aggregation/weight knobs stay zero/inert under active-backup.
	if c.Scheduler.EngageFraction != 0 || c.Scheduler.DisengageFraction != 0 ||
		c.Scheduler.CollapseDwell != 0 || c.Scheduler.LoadTau != 0 ||
		c.Scheduler.WeightRTTFloor != 0 || c.Scheduler.WeightLossFloor != 0 {
		t.Fatalf("weighted-only knobs must stay zero/inert under active-backup, got engage=%g disengage=%g collapse=%s tau=%s rttFloor=%s lossFloor=%g",
			c.Scheduler.EngageFraction, c.Scheduler.DisengageFraction, c.Scheduler.CollapseDwell,
			c.Scheduler.LoadTau, c.Scheduler.WeightRTTFloor, c.Scheduler.WeightLossFloor)
	}
}

// TestActiveBackupPacingPerPathFromBDP (acceptance ii): active-backup + pacing_enabled
// with link_bandwidth+link_rtt on ALL paths sizes PER-PATH capacities from EACH path's
// OWN BDP via SizePacingFromBDP. A heterogeneous link set yields DISTINCT per-path
// capacities (faster link -> higher CapacityFPS) — NOT min-reduced to the bottleneck.
func TestActiveBackupPacingPerPathFromBDP(t *testing.T) {
	body := fill(twoPathConfig("50Mbit", "45ms", "10Mbit", "30ms")) + abPacing("pacing_enabled = true\n")
	path := writeConfig(t, 0o600, body)
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want0, err := SizePacingFromBDP(50e6, 45*time.Millisecond, defaultAvgWireFrameBytes)
	if err != nil {
		t.Fatalf("SizePacingFromBDP path0: %v", err)
	}
	want1, err := SizePacingFromBDP(10e6, 30*time.Millisecond, defaultAvgWireFrameBytes)
	if err != nil {
		t.Fatalf("SizePacingFromBDP path1: %v", err)
	}
	if len(c.Scheduler.PerPathCapacities) != 2 || len(c.Scheduler.PacingBursts) != 2 {
		t.Fatalf("per-path vectors = %v / %v, want length 2 each", c.Scheduler.PerPathCapacities, c.Scheduler.PacingBursts)
	}
	if math.Abs(c.Scheduler.PerPathCapacities[0]-want0.CapacityFPS) > 1e-6 {
		t.Fatalf("PerPathCapacities[0] = %g, want %g (path0's OWN BDP)", c.Scheduler.PerPathCapacities[0], want0.CapacityFPS)
	}
	if math.Abs(c.Scheduler.PerPathCapacities[1]-want1.CapacityFPS) > 1e-6 {
		t.Fatalf("PerPathCapacities[1] = %g, want %g (path1's OWN BDP)", c.Scheduler.PerPathCapacities[1], want1.CapacityFPS)
	}
	if math.Abs(c.Scheduler.PacingBursts[0]-want0.BurstFrames) > 1e-6 {
		t.Fatalf("PacingBursts[0] = %g, want %g", c.Scheduler.PacingBursts[0], want0.BurstFrames)
	}
	if math.Abs(c.Scheduler.PacingBursts[1]-want1.BurstFrames) > 1e-6 {
		t.Fatalf("PacingBursts[1] = %g, want %g", c.Scheduler.PacingBursts[1], want1.BurstFrames)
	}
	// The load-bearing anti-bottleneck assertion: the faster primary must pace at its
	// OWN drain rate, strictly ABOVE the slower backup — NOT min-reduced to a shared
	// bottleneck scalar (which would reimpose the D65 ceiling on the active primary).
	if !(c.Scheduler.PerPathCapacities[0] > c.Scheduler.PerPathCapacities[1]) {
		t.Fatalf("expected distinct per-path capacities with the faster link higher, got [0]=%g [1]=%g (min-reduced?)",
			c.Scheduler.PerPathCapacities[0], c.Scheduler.PerPathCapacities[1])
	}
	// The shared weighted bottleneck scalar must NOT be populated under active-backup.
	if c.Scheduler.PerPathCapacityFPS != 0 || c.Scheduler.PacingBurstFrames != 0 {
		t.Fatalf("weighted shared bottleneck scalar must stay zero under active-backup, got cap=%g burst=%g",
			c.Scheduler.PerPathCapacityFPS, c.Scheduler.PacingBurstFrames)
	}
}

// TestActiveBackupPacingFailFastNeitherSource (acceptance iii, reproduction): pacing
// enabled under active-backup with NO link_bandwidth and NO explicit per_path_capacity_fps/
// pacing_burst_frames is a LOAD ERROR — the weighted synthetic ~10000fps default must
// NOT silently apply (a nominally-enabled-but-UNBINDING pace reproduces D65). Against
// the pre-T152 code this Load SUCCEEDS (the derive early-returns for non-weighted and
// nothing sizes the pace), so the test fails for the right reason: the fail-fast is
// missing.
func TestActiveBackupPacingFailFastNeitherSource(t *testing.T) {
	body := fill(edgeConfig) + abPacing("pacing_enabled = true\n")
	path := writeConfig(t, 0o600, body)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected a load error: pacing under active-backup with neither link_bandwidth nor explicit knobs must fail fast, got nil")
	}
	if !strings.Contains(err.Error(), "per_path_capacity_fps") && !strings.Contains(err.Error(), "link_bandwidth") {
		t.Fatalf("error = %q, want a NAMED fail-fast naming per_path_capacity_fps or link_bandwidth", err.Error())
	}
}

// TestActiveBackupPacingRejects (acceptance iv): a PARTIAL link_bandwidth declaration
// and setting BOTH the raw frame-slot knobs AND link_bandwidth each fail fast under the
// active-backup policy, mirroring the weighted-policy rules.
func TestActiveBackupPacingRejects(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "partial link_bandwidth (all-or-nothing)",
			body: fill(twoPathConfig("50Mbit", "45ms", "", "")) + abPacing("pacing_enabled = true\n"),
			want: "must be declared on ALL paths or none",
		},
		{
			name: "raw knobs and link_bandwidth are mutually exclusive",
			body: fill(twoPathConfig("50Mbit", "45ms", "10Mbit", "30ms")) + abPacing("pacing_enabled = true\nper_path_capacity_fps = 5000\n"),
			want: "mutually exclusive",
		},
		{
			name: "missing rtt under pacing",
			body: fill(twoPathConfig("50Mbit", "", "10Mbit", "")) + abPacing("pacing_enabled = true\n"),
			want: "link_rtt is required",
		},
		{
			name: "explicit capacity without burst",
			body: fill(edgeConfig) + abPacing("pacing_enabled = true\nper_path_capacity_fps = 5000\n"),
			want: "pacing_burst_frames must be > 0",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeConfig(t, 0o600, tc.body)
			_, err := Load(path)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tc.want)
			}
		})
	}
}

// TestActiveBackupNoPacingKeepsKnobsInert (acceptance v): active-backup WITHOUT pacing
// keeps every weighted knob AND every pacing knob zero/inert, and a declared
// link_bandwidth is inert (no per-path vectors are populated) — exactly today's P1
// config surface. This is the no-regression guard for the default policy.
func TestActiveBackupNoPacingKeepsKnobsInert(t *testing.T) {
	// Declare link_bandwidth on both paths but leave pacing OFF: the derivation must
	// no-op, so nothing is sized.
	body := fill(twoPathConfig("50Mbit", "45ms", "10Mbit", "30ms")) + abPacing("")
	path := writeConfig(t, 0o600, body)
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Scheduler.PacingEnabled {
		t.Fatal("pacing must default to disabled")
	}
	if c.Scheduler.PerPathCapacityFPS != 0 || c.Scheduler.PacingBurstFrames != 0 {
		t.Fatalf("pacing scalar knobs must stay zero under active-backup w/o pacing, got cap=%g burst=%g",
			c.Scheduler.PerPathCapacityFPS, c.Scheduler.PacingBurstFrames)
	}
	if c.Scheduler.PerPathCapacities != nil || c.Scheduler.PacingBursts != nil {
		t.Fatalf("per-path vectors must be nil under active-backup w/o pacing, got %v / %v",
			c.Scheduler.PerPathCapacities, c.Scheduler.PacingBursts)
	}
	if c.Scheduler.EngageFraction != 0 || c.Scheduler.DisengageFraction != 0 ||
		c.Scheduler.CollapseDwell != 0 || c.Scheduler.LoadTau != 0 ||
		c.Scheduler.WeightRTTFloor != 0 || c.Scheduler.WeightLossFloor != 0 {
		t.Fatalf("weighted-only knobs must stay zero/inert under active-backup w/o pacing")
	}
}
