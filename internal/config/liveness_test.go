package config

import (
	"strings"
	"testing"
	"time"
)

// TestLivenessConfigRoundTrip is the D86/T203 acceptance case: an existing
// fixture that omits [liveness] and ride_through entirely must load with
// Liveness.DownAfter defaulted to defaultLivenessDownAfter (mirroring
// telemetry.DefaultDownAfter) and every path's RideThrough at its zero value —
// BYTE-IDENTICAL to today's fixed hardcoded behaviour, since nothing new is
// declared. A config that DOES set down_after/ride_through must round-trip
// those explicit values onto the typed fields unchanged.
func TestLivenessConfigRoundTrip(t *testing.T) {
	t.Run("unset yields defaults", func(t *testing.T) {
		path := writeConfig(t, 0o600, fill(edgeConfig))
		c, err := Load(path)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if c.Liveness.DownAfter != defaultLivenessDownAfter {
			t.Errorf("liveness.down_after = %s, want default %s", c.Liveness.DownAfter, defaultLivenessDownAfter)
		}
		for i, p := range c.Paths {
			if p.RideThrough != 0 {
				t.Errorf("path %d (%q) ride_through = %s, want 0 (unset default)", i, p.Name, p.RideThrough)
			}
		}
	})

	t.Run("set values round-trip", func(t *testing.T) {
		body := fill(edgeConfig) + "\n[liveness]\ndown_after = \"3s\"\n"
		body = strings.Replace(body, "name = \"starlink\"\nsource_addr = \"192.0.2.10\"",
			"name = \"starlink\"\nsource_addr = \"192.0.2.10\"\nride_through = \"5s\"", 1)
		path := writeConfig(t, 0o600, body)
		c, err := Load(path)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if c.Liveness.DownAfter != 3*time.Second {
			t.Errorf("liveness.down_after = %s, want 3s", c.Liveness.DownAfter)
		}
		if c.Paths[0].RideThrough != 5*time.Second {
			t.Errorf("path 0 ride_through = %s, want 5s", c.Paths[0].RideThrough)
		}
		if c.Paths[1].RideThrough != 0 {
			t.Errorf("path 1 (no ride_through set) = %s, want 0", c.Paths[1].RideThrough)
		}
	})
}

// TestLivenessConfigValidation is the D86/T203 validation matrix (acceptance):
// down_after <= 0, down_after below the 2x-probe-interval floor (400ms), and
// ride_through < 0 must all be rejected fail-fast at Load; an unparseable
// duration on either key must also fail; and a large (over-budget) down_after
// must load fine — the upper-side WARN-and-allow budget check is T211's job,
// not this task's.
func TestLivenessConfigValidation(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantErr string
	}{
		{
			name:    "down_after <= 0",
			body:    fill(edgeConfig) + "\n[liveness]\ndown_after = \"0s\"\n",
			wantErr: "liveness.down_after must be > 0",
		},
		{
			name:    "down_after below the 2x probe-interval floor (400ms)",
			body:    fill(edgeConfig) + "\n[liveness]\ndown_after = \"399ms\"\n",
			wantErr: "liveness.down_after must be >=",
		},
		{
			name:    "down_after unparseable",
			body:    fill(edgeConfig) + "\n[liveness]\ndown_after = \"not-a-duration\"\n",
			wantErr: "liveness.down_after: invalid duration",
		},
		{
			name: "ride_through < 0",
			body: strings.Replace(fill(edgeConfig), "name = \"starlink\"\nsource_addr = \"192.0.2.10\"",
				"name = \"starlink\"\nsource_addr = \"192.0.2.10\"\nride_through = \"-1s\"", 1),
			wantErr: "ride_through must be >= 0",
		},
		{
			name: "ride_through unparseable",
			body: strings.Replace(fill(edgeConfig), "name = \"starlink\"\nsource_addr = \"192.0.2.10\"",
				"name = \"starlink\"\nsource_addr = \"192.0.2.10\"\nride_through = \"not-a-duration\"", 1),
			wantErr: "invalid ride_through",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeConfig(t, 0o600, tc.body)
			_, err := Load(path)
			if err == nil {
				t.Fatalf("Load: want error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Load error = %q, want it to contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

// TestLivenessConfigAcceptsOverBudgetDownAfter confirms a large down_after —
// far above the 400ms floor — loads fine: the upper-side WARN-and-allow budget
// verdict (D86 decision 4) is T211's job, out of scope here.
func TestLivenessConfigAcceptsOverBudgetDownAfter(t *testing.T) {
	body := fill(edgeConfig) + "\n[liveness]\ndown_after = \"5s\"\n"
	path := writeConfig(t, 0o600, body)
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v, want no error (over-budget down_after must load fine this pass)", err)
	}
	if c.Liveness.DownAfter != 5*time.Second {
		t.Errorf("liveness.down_after = %s, want 5s", c.Liveness.DownAfter)
	}
}
