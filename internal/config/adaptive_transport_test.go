package config

import (
	"strings"
	"testing"
)

func TestAdaptivePolicyOwnsPacing(t *testing.T) {
	for _, pacing := range []string{"", "pacing_enabled = true\n", "pacing_enabled = false\n"} {
		body := fill(edgeConfig) + "\n[scheduler]\npolicy = \"adaptive\"\n" + pacing
		cfg, err := Load(writeConfig(t, 0o600, body))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Scheduler.Policy != PolicyAdaptive || cfg.Scheduler.PerPathShapers != nil {
			t.Fatalf("adaptive policy composed with legacy shapers: %+v", cfg.Scheduler)
		}
	}
}

func TestAdaptiveRejectsLegacyFEC(t *testing.T) {
	body := fill(edgeConfig) + "\n[scheduler]\npolicy = \"adaptive\"\n[fec]\nenabled = true\n"
	if _, err := Load(writeConfig(t, 0o600, body)); err == nil || !strings.Contains(err.Error(), "fec.enabled must be false") {
		t.Fatalf("legacy FEC configuration: %v", err)
	}
}

func TestAdaptiveRejectsIgnoredCapacityLimit(t *testing.T) {
	body := strings.ReplaceAll(byteShaperFixture("192.0.2.1", "8Mbit", "45ms", 1500, "pacing_enabled = true\n"), "active-backup", "adaptive")
	body = strings.Replace(body, "link_bandwidth = \"8Mbit\"", "link_bandwidth = \"8Mbit\"\nlink_bandwidth_limit = \"10Mbit\"", 1)
	if _, err := Load(writeConfig(t, 0o600, body)); err == nil || !strings.Contains(err.Error(), "supported only by active-backup") {
		t.Fatalf("silently ignored explicit capacity limit: %v", err)
	}
}
