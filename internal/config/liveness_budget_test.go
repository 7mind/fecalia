package config

import (
	"strings"
	"testing"
)

// boolPtr is a test helper for the *bool computed-verdict fields (WeightedCapacitySane,
// LivenessBudgetSane), used both here and by the golden-shape config tests.
func boolPtr(b bool) *bool { return &b }

// TestLivenessBudgetVerdict is the D86-decision-4 WARN-and-allow acceptance (T211):
// a config whose failover budget EXCEEDS the 3s P1 recovery deadline must still LOAD
// (never rejected), carrying LivenessBudgetSane=false; a default config (1.6s budget)
// must load with LivenessBudgetSane=true. The verdict is ALWAYS non-nil — the budget
// applies to every config, unlike the weighted-only WeightedCapacitySane.
func TestLivenessBudgetVerdict(t *testing.T) {
	t.Run("defaults are within budget (true)", func(t *testing.T) {
		path := writeConfig(t, 0o600, fill(edgeConfig))
		c, err := Load(path)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if c.LivenessBudgetSane == nil {
			t.Fatal("LivenessBudgetSane = nil, want non-nil (the budget verdict applies to every config)")
		}
		if !*c.LivenessBudgetSane {
			t.Errorf("LivenessBudgetSane = false at defaults (budget 1.6s <= 3s), want true")
		}
	})

	t.Run("over-budget down_after loads with verdict false", func(t *testing.T) {
		// 5s down_after -> budget = 5s + 0 + 2*200ms = 5.4s > 3s -> false.
		body := fill(edgeConfig) + "\n[liveness]\ndown_after = \"5s\"\n"
		path := writeConfig(t, 0o600, body)
		c, err := Load(path)
		if err != nil {
			t.Fatalf("Load: %v, want no error (over-budget config must LOAD, WARN-and-allow)", err)
		}
		if c.LivenessBudgetSane == nil {
			t.Fatal("LivenessBudgetSane = nil, want non-nil")
		}
		if *c.LivenessBudgetSane {
			t.Errorf("LivenessBudgetSane = true for 5s down_after (budget 5.4s > 3s), want false")
		}
	})

	t.Run("ride_through pushes a default down_after over budget", func(t *testing.T) {
		// down_after defaults to 1.2s; a 2s ride_through -> budget = 1.2 + 2 + 0.4 = 3.6s > 3s.
		body := strings.Replace(fill(edgeConfig), "name = \"starlink\"\nsource_addr = \"192.0.2.10\"",
			"name = \"starlink\"\nsource_addr = \"192.0.2.10\"\nride_through = \"2s\"", 1)
		path := writeConfig(t, 0o600, body)
		c, err := Load(path)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if c.LivenessBudgetSane == nil || *c.LivenessBudgetSane {
			t.Errorf("LivenessBudgetSane = %v for 2s ride_through (budget 3.6s > 3s), want false", derefBool(c.LivenessBudgetSane))
		}
	})
}
