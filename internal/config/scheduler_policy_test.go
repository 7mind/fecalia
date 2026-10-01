package config

import (
	"strings"
	"testing"
)

// TestSchedulerPolicyLoadsAsAdaptive: the adaptive transport is the only one, so
// an omitted [scheduler] block, a [scheduler] block without a policy, and an
// explicit policy = "adaptive" all load with PolicyAdaptive.
func TestSchedulerPolicyLoadsAsAdaptive(t *testing.T) {
	cases := []struct {
		name      string
		scheduler string
	}{
		{name: "scheduler block omitted", scheduler: ""},
		{name: "policy omitted", scheduler: "\n[scheduler]\n"},
		{name: "policy adaptive", scheduler: "\n[scheduler]\npolicy = \"adaptive\"\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, err := Load(writeConfig(t, 0o600, fill(edgeConfig)+tc.scheduler))
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if c.Scheduler.Policy != PolicyAdaptive {
				t.Fatalf("scheduler policy = %q, want %q", c.Scheduler.Policy, PolicyAdaptive)
			}
		})
	}
}

// TestSchedulerPolicyRejects: naming a removed or unknown transport fails the
// load with an error naming the key and the offending value, rather than
// silently running the adaptive transport.
func TestSchedulerPolicyRejects(t *testing.T) {
	for _, policy := range []string{"active-backup", "weighted", "round-robin"} {
		t.Run(policy, func(t *testing.T) {
			body := fill(edgeConfig) + "\n[scheduler]\npolicy = \"" + policy + "\"\n"
			_, err := Load(writeConfig(t, 0o600, body))
			if err == nil {
				t.Fatalf("policy %q loaded, want a scheduler.policy error", policy)
			}
			for _, want := range []string{"scheduler.policy must be", `"` + policy + `"`} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error = %q, want substring %q", err.Error(), want)
				}
			}
		})
	}
}

// TestRemovedKeysRejectedAsUnknown: the keys of the removed transports ([fec],
// the [scheduler] knobs other than policy, and the per-path link declarations)
// are no longer part of the configuration surface, so the strict decoder
// rejects each as an unknown key, naming its dotted path.
func TestRemovedKeysRejectedAsUnknown(t *testing.T) {
	withPathKey := func(line string) string {
		return strings.Replace(fill(edgeConfig), `source_addr = "192.0.2.10"`,
			"source_addr = \"192.0.2.10\"\n"+line, 1)
	}
	withSchedulerKey := func(line string) string {
		return fill(edgeConfig) + "\n[scheduler]\npolicy = \"adaptive\"\n" + line + "\n"
	}
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "fec table",
			body: fill(edgeConfig) + "\n[fec]\nenabled = false\n",
			want: "unknown key fec",
		},
		{
			name: "scheduler pacing_enabled",
			body: withSchedulerKey("pacing_enabled = false"),
			want: "unknown key scheduler.pacing_enabled",
		},
		{
			name: "scheduler per_path_capacity_fps",
			body: withSchedulerKey("per_path_capacity_fps = 10000.0"),
			want: "unknown key scheduler.per_path_capacity_fps",
		},
		{
			name: "scheduler pacing_burst_frames",
			body: withSchedulerKey("pacing_burst_frames = 64.0"),
			want: "unknown key scheduler.pacing_burst_frames",
		},
		{
			name: "scheduler engage_fraction",
			body: withSchedulerKey("engage_fraction = 0.9"),
			want: "unknown key scheduler.engage_fraction",
		},
		{
			name: "scheduler disengage_fraction",
			body: withSchedulerKey("disengage_fraction = 0.5"),
			want: "unknown key scheduler.disengage_fraction",
		},
		{
			name: "scheduler collapse_dwell",
			body: withSchedulerKey(`collapse_dwell = "2s"`),
			want: "unknown key scheduler.collapse_dwell",
		},
		{
			name: "scheduler load_tau",
			body: withSchedulerKey(`load_tau = "200ms"`),
			want: "unknown key scheduler.load_tau",
		},
		{
			name: "scheduler weight_rtt_floor",
			body: withSchedulerKey(`weight_rtt_floor = "1ms"`),
			want: "unknown key scheduler.weight_rtt_floor",
		},
		{
			name: "scheduler weight_loss_floor",
			body: withSchedulerKey("weight_loss_floor = 0.001"),
			want: "unknown key scheduler.weight_loss_floor",
		},
		{
			name: "path link_bandwidth",
			body: withPathKey(`link_bandwidth = "50Mbit"`),
			want: "unknown key paths.link_bandwidth",
		},
		{
			name: "path link_bandwidth_limit",
			body: withPathKey(`link_bandwidth_limit = "60Mbit"`),
			want: "unknown key paths.link_bandwidth_limit",
		},
		{
			name: "path link_rtt",
			body: withPathKey(`link_rtt = "45ms"`),
			want: "unknown key paths.link_rtt",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, 0o600, tc.body))
			if err == nil {
				t.Fatalf("expected error ending in %q, got nil", tc.want)
			}
			if !strings.HasSuffix(err.Error(), ": "+tc.want) {
				t.Fatalf("error = %q, want suffix %q", err.Error(), tc.want)
			}
		})
	}
}
