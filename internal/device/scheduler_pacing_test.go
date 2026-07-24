package device

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/sched"
	"github.com/7mind/wanbond/internal/telemetry"
)

// writeActiveBackupPacingConfig writes an edge config selecting the active-backup
// policy with two HETEROGENEOUS declared links (a fast "starlink" primary, a slow
// "5g" backup), so config.derivePacingFromBDP (T152) sizes DISTINCT per-path
// capacities/bursts on cfg.Scheduler, and loads it. pacingEnabled toggles the
// [scheduler] pacing_enabled knob T153 wires into selectScheduler.
func writeActiveBackupPacingConfig(t *testing.T, pacingEnabled bool) *config.Config {
	t.Helper()
	privRaw, _ := genX25519(t)
	_, pubRaw := genX25519(t)

	body := `role = "edge"
psk = "` + randB64Key(t) + `"

[[paths]]
name = "starlink"
source_addr = "192.0.2.10"
link_bandwidth = "50Mbit"
link_rtt = "45ms"

[[paths]]
name = "5g"
source_addr = "192.0.2.20"
link_bandwidth = "10Mbit"
link_rtt = "30ms"

[wireguard]
private_key = "` + base64.StdEncoding.EncodeToString(privRaw) + `"
[[wireguard.peers]]
public_key = "` + base64.StdEncoding.EncodeToString(pubRaw) + `"
endpoint = "203.0.113.5:51820"
allowed_ips = ["0.0.0.0/0"]

[metrics]
listen = "127.0.0.1:9095"

[log]
level = "info"

[scheduler]
policy = "active-backup"
`
	if pacingEnabled {
		body += "pacing_enabled = true\n"
	}

	path := filepath.Join(t.TempDir(), "wanbond.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatalf("chmod config: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load config: %v\n%s", err, body)
	}
	return cfg
}

// TestSelectSchedulerExactByteOwnershipDisablesLegacyPacing verifies T299's
// atomic ownership cut: once config has derived per-path exact-byte shapers, the
// scheduler retains selection/offered-load behavior but its legacy frame-token
// policer cannot shed.
func TestSelectSchedulerExactByteOwnershipDisablesLegacyPacing(t *testing.T) {
	cfg := writeActiveBackupPacingConfig(t, true)
	if !cfg.Scheduler.PacingEnabled {
		t.Fatal("cfg.Scheduler.PacingEnabled must be true")
	}
	if len(cfg.Scheduler.PerPathCapacities) != 2 || len(cfg.Scheduler.PacingBursts) != 2 {
		t.Fatalf("derived per-path vectors = %v / %v, want length 2 each", cfg.Scheduler.PerPathCapacities, cfg.Scheduler.PacingBursts)
	}
	if len(cfg.Scheduler.PerPathShapers) != 2 {
		t.Fatalf("derived exact-byte shapers = %v, want length 2", cfg.Scheduler.PerPathShapers)
	}
	primaryCap := cfg.Scheduler.PerPathCapacities[0]
	backupCap := cfg.Scheduler.PerPathCapacities[1]
	if !(primaryCap > backupCap) {
		t.Fatalf("expected the faster starlink primary to derive a HIGHER capacity than the 5g backup, got primary=%g backup=%g", primaryCap, backupCap)
	}

	health := []sched.PathHealth{sched.AlwaysUp{}, sched.AlwaysUp{}}
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	scheduler, err := selectScheduler(cfg, health, nil, clock, discardLogger(t))
	if err != nil {
		t.Fatalf("selectScheduler: %v", err)
	}
	ab, ok := scheduler.(*sched.ActiveBackup)
	if !ok {
		t.Fatalf("selectScheduler returned %T, want *sched.ActiveBackup", scheduler)
	}

	// Offer ~5000 ClassData frames over a 1s advancing-clock window. Neither the
	// legacy Pick nor the production PickUnpaced seam may shed: admission belongs
	// exclusively to the bind-owned byte shaper.
	const (
		frames = 5000
		step   = 200 * time.Microsecond // frames*step = 1s
	)
	for i := 0; i < frames; i++ {
		if got := ab.Pick(sched.ClassData, 1); got != 0 {
			t.Fatalf("legacy Pick #%d = %d, want active path 0 with frame-token pacing disabled", i, got)
		}
		if got := ab.PickUnpaced(sched.ClassData, 1); got != 0 {
			t.Fatalf("PickUnpaced #%d = %d, want active path 0", i, got)
		}
		clock.advance(step)
	}
}

// TestSelectSchedulerActiveBackupPacingDisabled is the T153 no-regression check:
// pacing_enabled=false must build a byte-for-byte pre-change ActiveBackup — Pick
// never sheds (returns the active index every time), even under the SAME sustained
// overload TestSelectSchedulerActiveBackupPacingEnabled uses.
func TestSelectSchedulerActiveBackupPacingDisabled(t *testing.T) {
	cfg := writeActiveBackupPacingConfig(t, false)
	if cfg.Scheduler.PacingEnabled {
		t.Fatal("cfg.Scheduler.PacingEnabled must be false")
	}
	if cfg.Scheduler.PerPathCapacities != nil || cfg.Scheduler.PacingBursts != nil {
		t.Fatalf("derived per-path vectors must stay nil with pacing disabled, got %v / %v", cfg.Scheduler.PerPathCapacities, cfg.Scheduler.PacingBursts)
	}

	health := []sched.PathHealth{sched.AlwaysUp{}, sched.AlwaysUp{}}
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	scheduler, err := selectScheduler(cfg, health, nil, clock, discardLogger(t))
	if err != nil {
		t.Fatalf("selectScheduler: %v", err)
	}
	ab, ok := scheduler.(*sched.ActiveBackup)
	if !ok {
		t.Fatalf("selectScheduler returned %T, want *sched.ActiveBackup", scheduler)
	}

	const (
		frames = 5000
		step   = 200 * time.Microsecond
	)
	for i := 0; i < frames; i++ {
		if got := ab.Pick(sched.ClassData, 1); got != 0 {
			t.Fatalf("Pick #%d = %d, want 0 (pacing disabled must never shed — pre-change behaviour)", i, got)
		}
		clock.advance(step)
	}

	// Equivalent to the untouched pre-T153 wiring: FailbackAfter only, no pacing.
	unwired, err := sched.NewActiveBackup(health, sched.Config{FailbackAfter: defaultFailbackDwell}, telemetry.SystemClock{}, discardLogger(t))
	if err != nil {
		t.Fatalf("NewActiveBackup (reference): %v", err)
	}
	for i := 0; i < 100; i++ {
		if got := unwired.Pick(sched.ClassData, 1); got != 0 {
			t.Fatalf("reference unwired Pick #%d = %d, want 0", i, got)
		}
	}
}
