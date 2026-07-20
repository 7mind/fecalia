package device

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/telemetry"
)

// writeLivenessConfig writes a two-path edge config whose optional [liveness] block and
// per-path ride_through are injected verbatim, then loads it (exercising T203's parse +
// default + validate). An empty livenessBlock / rideThrough leaves the field at its loaded
// default (down_after -> telemetry.DefaultDownAfter, ride_through -> 0).
func writeLivenessConfig(t *testing.T, livenessBlock, path0RideThrough, path1RideThrough string) *config.Config {
	t.Helper()
	privRaw, _ := genX25519(t)
	_, pubRaw := genX25519(t)
	line := func(rt string) string {
		if rt == "" {
			return ""
		}
		return fmt.Sprintf("ride_through = %q\n", rt)
	}
	body := fmt.Sprintf(`role = "edge"
psk = "%s"
%s
[[paths]]
name = "a"
source_addr = "127.0.0.1"
%s
[[paths]]
name = "b"
source_addr = "127.0.0.2"
%s
[wireguard]
private_key = "%s"

[[wireguard.peers]]
public_key = "%s"
endpoints = ["127.0.0.1:1"]
allowed_ips = ["10.0.0.0/24"]
`, randB64Key(t), livenessBlock,
		line(path0RideThrough), line(path1RideThrough),
		base64.StdEncoding.EncodeToString(privRaw),
		base64.StdEncoding.EncodeToString(pubRaw))

	path := filepath.Join(t.TempDir(), "edge.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil { // config.Load requires 0600 exactly
		t.Fatalf("chmod config: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	return cfg
}

// TestBuildSchedulerLivenessFromConfig is the D86/T207 acceptance: buildScheduler must build a
// PER-PATH telemetry.ProberConfig from the loaded config — DownAfter from the global
// cfg.Liveness.DownAfter and RideThrough from THAT path's cfg.Paths[i].RideThrough — NOT the
// pre-T207 single hardcoded telemetry.Default* literal. proberConfigForPath is the mapping
// under test (the same function buildScheduler's newProber closure and the runtime
// bind.ProberFactory call for every boot-time and runtime path).
func TestBuildSchedulerLivenessFromConfig(t *testing.T) {
	// Custom: down_after=2s globally; path "a" rides through 2s, path "b" leaves it at 0.
	t.Run("custom-per-path", func(t *testing.T) {
		cfg := writeLivenessConfig(t, "[liveness]\ndown_after = \"2s\"\n", "2s", "")
		if cfg.Liveness.DownAfter != 2*time.Second {
			t.Fatalf("precondition: cfg.Liveness.DownAfter = %s, want 2s", cfg.Liveness.DownAfter)
		}
		if cfg.Paths[0].RideThrough != 2*time.Second || cfg.Paths[1].RideThrough != 0 {
			t.Fatalf("precondition: ride_through = {%s, %s}, want {2s, 0}", cfg.Paths[0].RideThrough, cfg.Paths[1].RideThrough)
		}

		want0 := telemetry.ProberConfig{
			LossWindow: telemetry.DefaultLossWindow,
			Liveness: telemetry.LivenessConfig{
				DownAfter:        2 * time.Second,
				UpAfterSuccesses: telemetry.DefaultUpSuccesses,
				RideThrough:      2 * time.Second,
			},
		}
		if got := proberConfigForPath(cfg, cfg.Paths[0].RideThrough); got != want0 {
			t.Errorf("path 0 ProberConfig = %+v, want %+v", got, want0)
		}

		want1 := telemetry.ProberConfig{
			LossWindow: telemetry.DefaultLossWindow,
			Liveness: telemetry.LivenessConfig{
				DownAfter:        2 * time.Second,
				UpAfterSuccesses: telemetry.DefaultUpSuccesses,
				RideThrough:      0,
			},
		}
		if got := proberConfigForPath(cfg, cfg.Paths[1].RideThrough); got != want1 {
			t.Errorf("path 1 ProberConfig = %+v, want %+v", got, want1)
		}
	})

	// Identity guard: a fully-defaulted config (no [liveness], no ride_through) maps to EXACTLY
	// the pre-T207 hardcoded literal, so behaviour is byte-identical when the block is omitted.
	t.Run("defaults-identity", func(t *testing.T) {
		cfg := writeLivenessConfig(t, "", "", "")
		want := telemetry.ProberConfig{
			LossWindow: telemetry.DefaultLossWindow,
			Liveness: telemetry.LivenessConfig{
				DownAfter:        telemetry.DefaultDownAfter,
				UpAfterSuccesses: telemetry.DefaultUpSuccesses,
			},
		}
		for i := range cfg.Paths {
			if got := proberConfigForPath(cfg, cfg.Paths[i].RideThrough); got != want {
				t.Errorf("path %d ProberConfig = %+v, want default-identity %+v", i, got, want)
			}
		}
	})
}
