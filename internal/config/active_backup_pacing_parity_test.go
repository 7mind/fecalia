package config

import (
	"math"
	"testing"
	"time"
)

// TestActiveBackupPacingPerPathParityTable (T154, R162 criticism 1) is the PER-PATH
// PARITY TABLE test: for a table of link_bandwidth/link_rtt inputs (homogeneous and
// heterogeneous), active-backup + pacing_enabled must size EACH path's
// PerPathCapacities[i]/PacingBursts[i] from THAT path's OWN BDP — never any other
// path's — verified against a direct SizePacingFromBDP call per path. This
// complements (does not duplicate) TestActiveBackupPacingPerPathFromBDP in
// active_backup_pacing_test.go, which asserts the same shape for a single fixed
// 50Mbit/10Mbit fixture plus the fail-fast/reject cases; this table exercises
// distinct bandwidth/RTT combinations, including a homogeneous case where per-path
// parity still holds even though both paths happen to size identically.
func TestActiveBackupPacingPerPathParityTable(t *testing.T) {
	cases := []struct {
		name string
		bw0  string
		rtt0 string
		bw1  string
		rtt1 string
	}{
		{name: "heterogeneous: 100Mbit/20ms vs 25Mbit/60ms", bw0: "100Mbit", rtt0: "20ms", bw1: "25Mbit", rtt1: "60ms"},
		{name: "heterogeneous: 8Mbit/120ms vs 200Mbit/15ms (order reversed)", bw0: "8Mbit", rtt0: "120ms", bw1: "200Mbit", rtt1: "15ms"},
		{name: "homogeneous: both paths 40Mbit/50ms", bw0: "40Mbit", rtt0: "50ms", bw1: "40Mbit", rtt1: "50ms"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := fill(twoPathConfig(tc.bw0, tc.rtt0, tc.bw1, tc.rtt1)) + abPacing("pacing_enabled = true\n")
			path := writeConfig(t, 0o600, body)
			c, err := Load(path)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if len(c.Scheduler.PerPathCapacities) != 2 || len(c.Scheduler.PacingBursts) != 2 {
				t.Fatalf("per-path vectors = %v / %v, want length 2 each", c.Scheduler.PerPathCapacities, c.Scheduler.PacingBursts)
			}

			bw0, rtt0 := mustParseBandwidthRTT(t, tc.bw0, tc.rtt0)
			bw1, rtt1 := mustParseBandwidthRTT(t, tc.bw1, tc.rtt1)
			want0, err := SizePacingFromBDP(bw0, rtt0, defaultAvgWireFrameBytes)
			if err != nil {
				t.Fatalf("SizePacingFromBDP path0: %v", err)
			}
			want1, err := SizePacingFromBDP(bw1, rtt1, defaultAvgWireFrameBytes)
			if err != nil {
				t.Fatalf("SizePacingFromBDP path1: %v", err)
			}

			// Per-path parity: each path's capacity/burst equals SizePacingFromBDP of
			// THAT path's own link_bandwidth/link_rtt — not the other path's, and not
			// any shared/min-reduced value.
			if math.Abs(c.Scheduler.PerPathCapacities[0]-want0.CapacityFPS) > 1e-6 {
				t.Errorf("PerPathCapacities[0] = %g, want %g (path0's own BDP)", c.Scheduler.PerPathCapacities[0], want0.CapacityFPS)
			}
			if math.Abs(c.Scheduler.PerPathCapacities[1]-want1.CapacityFPS) > 1e-6 {
				t.Errorf("PerPathCapacities[1] = %g, want %g (path1's own BDP)", c.Scheduler.PerPathCapacities[1], want1.CapacityFPS)
			}
			if math.Abs(c.Scheduler.PacingBursts[0]-want0.BurstFrames) > 1e-6 {
				t.Errorf("PacingBursts[0] = %g, want %g (path0's own BDP)", c.Scheduler.PacingBursts[0], want0.BurstFrames)
			}
			if math.Abs(c.Scheduler.PacingBursts[1]-want1.BurstFrames) > 1e-6 {
				t.Errorf("PacingBursts[1] = %g, want %g (path1's own BDP)", c.Scheduler.PacingBursts[1], want1.BurstFrames)
			}
		})
	}
}

// mustParseBandwidthRTT reproduces the exact bandwidth/RTT parse the config loader
// performs on link_bandwidth/link_rtt, so the table test's "want" values are derived
// the same way the production code derives them, not hand-computed in the test.
func mustParseBandwidthRTT(t *testing.T, bw, rtt string) (float64, time.Duration) {
	t.Helper()
	bps, err := parseBandwidth(bw)
	if err != nil {
		t.Fatalf("parseBandwidth(%q): %v", bw, err)
	}
	d, err := time.ParseDuration(rtt)
	if err != nil {
		t.Fatalf("time.ParseDuration(%q): %v", rtt, err)
	}
	return bps, d
}

// TestActiveBackupPacingHeterogeneousDiffersFromWeightedBottleneck (T154, acceptance point
// 3, the KEY DISTINCTION assertion): for the SAME heterogeneous link set, active-backup
// sizes DISTINCT per-path capacities (the faster link keeps its own, higher
// CapacityFPS) whereas the weighted policy collapses to a SINGLE shared bottleneck
// scalar. The two sizings must differ for the faster path: active-backup's
// PerPathCapacities[0] (fast link) is strictly greater than PerPathCapacities[1] (slow
// link) and strictly greater than weighted's collapsed PerPathCapacityFPS scalar — the
// D65 regression this whole surface exists to prevent (a fast active primary must not
// be capped to the backup's bottleneck rate).
func TestActiveBackupPacingHeterogeneousDiffersFromWeightedBottleneck(t *testing.T) {
	const bwFast, rttFast = "80Mbit", "40ms"
	const bwSlow, rttSlow = "20Mbit", "60ms"

	abBody := fill(twoPathConfig(bwFast, rttFast, bwSlow, rttSlow)) + abPacing("pacing_enabled = true\n")
	abPath := writeConfig(t, 0o600, abBody)
	abCfg, err := Load(abPath)
	if err != nil {
		t.Fatalf("Load (active-backup): %v", err)
	}
	if len(abCfg.Scheduler.PerPathCapacities) != 2 {
		t.Fatalf("active-backup PerPathCapacities = %v, want length 2", abCfg.Scheduler.PerPathCapacities)
	}

	wBody := fill(twoPathConfig(bwFast, rttFast, bwSlow, rttSlow)) + "\n[scheduler]\npolicy = \"weighted\"\npacing_enabled = true\n"
	wPath := writeConfig(t, 0o600, wBody)
	wCfg, err := Load(wPath)
	if err != nil {
		t.Fatalf("Load (weighted): %v", err)
	}

	// Weighted collapses to a single shared scalar: no per-path vector is populated.
	if wCfg.Scheduler.PerPathCapacities != nil {
		t.Fatalf("weighted PerPathCapacities = %v, want nil (collapsed to the shared scalar, not a per-path vector)", wCfg.Scheduler.PerPathCapacities)
	}
	if wCfg.Scheduler.PerPathCapacityFPS <= 0 {
		t.Fatalf("weighted PerPathCapacityFPS = %g, want > 0 (bottleneck-derived)", wCfg.Scheduler.PerPathCapacityFPS)
	}

	// The weighted scalar is min-reduced to the slow (bottleneck) link's own BDP — the
	// same value active-backup's slow-path entry independently derives.
	wantBottleneck, err := SizePacingFromBDP(20e6, 60*time.Millisecond, defaultAvgWireFrameBytes)
	if err != nil {
		t.Fatalf("SizePacingFromBDP bottleneck: %v", err)
	}
	if math.Abs(wCfg.Scheduler.PerPathCapacityFPS-wantBottleneck.CapacityFPS) > 1e-6 {
		t.Fatalf("weighted PerPathCapacityFPS = %g, want %g (min-reduced to the bottleneck link's own BDP)", wCfg.Scheduler.PerPathCapacityFPS, wantBottleneck.CapacityFPS)
	}

	// The KEY DISTINCTION: active-backup's two per-path capacities are themselves
	// distinct (the faster link keeps its own higher CapacityFPS)...
	if abCfg.Scheduler.PerPathCapacities[0] == abCfg.Scheduler.PerPathCapacities[1] {
		t.Fatalf("active-backup PerPathCapacities[0] == PerPathCapacities[1] == %g, want distinct per-path capacities for a heterogeneous link set",
			abCfg.Scheduler.PerPathCapacities[0])
	}
	if !(abCfg.Scheduler.PerPathCapacities[0] > abCfg.Scheduler.PerPathCapacities[1]) {
		t.Fatalf("active-backup PerPathCapacities[0] (fast link) = %g, want > PerPathCapacities[1] (slow link) = %g",
			abCfg.Scheduler.PerPathCapacities[0], abCfg.Scheduler.PerPathCapacities[1])
	}
	// ...and the fast path's active-backup capacity differs from (exceeds) the
	// weighted policy's single min-bottleneck scalar for the identical link inputs —
	// under weighted the fast link would be capped down to the bottleneck; under
	// active-backup it is not.
	if abCfg.Scheduler.PerPathCapacities[0] == wCfg.Scheduler.PerPathCapacityFPS {
		t.Fatalf("active-backup fast-path capacity %g == weighted bottleneck scalar %g, want them to differ (active-backup must not min-reduce the fast link)",
			abCfg.Scheduler.PerPathCapacities[0], wCfg.Scheduler.PerPathCapacityFPS)
	}
	if !(abCfg.Scheduler.PerPathCapacities[0] > wCfg.Scheduler.PerPathCapacityFPS) {
		t.Fatalf("active-backup fast-path capacity %g, want > weighted bottleneck scalar %g",
			abCfg.Scheduler.PerPathCapacities[0], wCfg.Scheduler.PerPathCapacityFPS)
	}
}

// TestActiveBackupPacingNoPacingSchedulerBlockOmitted (T154, acceptance point 4, regression
// guard) is the P1 empty-config-surface case: a config that omits the [scheduler]
// block ENTIRELY (not merely a [scheduler] block with policy stated but pacing off, as
// TestActiveBackupNoPacingKeepsKnobsInert in active_backup_pacing_test.go already
// covers) must still load and validate, default the policy to active-backup, and
// leave every weighted knob and every pacing knob at its zero value — exactly the P1
// config surface as it shipped before T152/T153 introduced pacing at all.
func TestActiveBackupPacingNoPacingSchedulerBlockOmitted(t *testing.T) {
	path := writeConfig(t, 0o600, fill(edgeConfig))
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Scheduler.Policy != PolicyActiveBackup {
		t.Fatalf("Scheduler.Policy = %q, want default %q when [scheduler] is omitted", c.Scheduler.Policy, PolicyActiveBackup)
	}
	if c.Scheduler.PacingEnabled {
		t.Fatal("pacing must default to disabled")
	}
	if c.Scheduler.PerPathCapacityFPS != 0 || c.Scheduler.PacingBurstFrames != 0 {
		t.Fatalf("pacing scalar knobs must stay zero, got cap=%g burst=%g", c.Scheduler.PerPathCapacityFPS, c.Scheduler.PacingBurstFrames)
	}
	if c.Scheduler.PerPathCapacities != nil || c.Scheduler.PacingBursts != nil {
		t.Fatalf("per-path pacing vectors must be nil, got %v / %v", c.Scheduler.PerPathCapacities, c.Scheduler.PacingBursts)
	}
	if c.Scheduler.EngageFraction != 0 || c.Scheduler.DisengageFraction != 0 ||
		c.Scheduler.CollapseDwell != 0 || c.Scheduler.LoadTau != 0 ||
		c.Scheduler.WeightRTTFloor != 0 || c.Scheduler.WeightLossFloor != 0 {
		t.Fatalf("weighted-only knobs must stay zero/inert, got engage=%g disengage=%g collapse=%s tau=%s rttFloor=%s lossFloor=%g",
			c.Scheduler.EngageFraction, c.Scheduler.DisengageFraction, c.Scheduler.CollapseDwell,
			c.Scheduler.LoadTau, c.Scheduler.WeightRTTFloor, c.Scheduler.WeightLossFloor)
	}
}
