package bind

import (
	"io"
	"net/netip"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/log"
	"github.com/7mind/wanbond/internal/sched"
	"github.com/7mind/wanbond/internal/telemetry"
)

// Distinct per-path pacer sizes for the two paths in the D79 identity test: slow and fast
// carry DIFFERENT capacity AND burst, so a positional/tail carry (the bug) is observable as
// one path wearing the OTHER's numbers.
const (
	slowCapFPS   = 200.0
	slowBurst    = 4.0
	fastCapFPS   = 1000.0
	fastBurst    = 16.0
	identityWait = time.Hour
)

// newPacingIdentityMultipath builds a pacing-ENABLED active-backup bind over two paths with
// DISTINCT per-path capacities/bursts (index 0 = "slow", index 1 = "fast"), returning the bind
// and its scheduler so a test can read each path's effective pacer config. Path 0's source is
// unassignable, so Open defers it and the bond comes up on path 1 alone.
func newPacingIdentityMultipath(t testing.TB, psk config.Key, clk telemetry.Clock) (*Multipath, *sched.ActiveBackup) {
	t.Helper()
	lg, err := log.New("error", io.Discard)
	if err != nil {
		t.Fatalf("build logger: %v", err)
	}
	paths := []config.Path{
		{Name: "slow", SourceAddr: netip.MustParseAddr(unassignableSource)},
		{Name: "fast", SourceAddr: netip.MustParseAddr("127.0.0.1")},
	}
	cfg := telemetry.ProberConfig{
		LossWindow: 0,
		Liveness:   telemetry.LivenessConfig{DownAfter: testProbeDownAfter, UpAfterSuccesses: testProbeUpSucc},
	}
	newProber := func(name string, id uint8, _ time.Duration) *telemetry.Prober {
		return telemetry.NewProber(name, id, testProbeSessionID, psk, cfg, clk, lg)
	}
	probers := make([]*telemetry.Prober, len(paths))
	health := make([]sched.PathHealth, len(paths))
	for i := range paths {
		probers[i] = newProber(paths[i].Name, uint8(i), paths[i].RideThrough)
		health[i] = probers[i]
	}
	scheduler, err := sched.NewActiveBackup(health, sched.Config{
		FailbackAfter:     identityWait,
		Pacing:            true,
		PerPathCapacities: []float64{slowCapFPS, fastCapFPS},
		PacingBursts:      []float64{slowBurst, fastBurst},
	}, clk, lg)
	if err != nil {
		t.Fatalf("build scheduler: %v", err)
	}
	m, err := NewMultipath(paths, psk, scheduler, probers, newProber, nil, nil, config.Amnezia{}, lg)
	if err != nil {
		t.Fatalf("NewMultipath: %v", err)
	}
	return m, scheduler
}

// TestPacingConfigKeyedByPathIdentityAcrossDeferral is the D79 regression: a pacing-enabled
// active-backup bond with DISTINCT per-path pacer sizes must key each path's token-bucket
// config by path IDENTITY across the Open-deferral churn, NOT by slice position. Path 0
// ("slow") defers at Open (unassignable source_addr), so only path 1 ("fast") binds; the sole
// bound path must then carry ITS OWN (fast) capacity/burst — not slow's, inherited via the
// positional/tail carry the reconcile SetPaths performed. After the deferred path is promoted,
// the promoted path must carry ITS OWN (slow) config while fast stays fast.
//
// Before the fix, the Open reconcile's SetPaths([]PathHealth{fast}) rebuilt the pacer slice by
// OLD-SLICE INDEX (resizeActiveBackupPacers), so bucket 0 — now the sole bound FAST path —
// inherited slow's capacity: assertion (A) fails.
func TestPacingConfigKeyedByPathIdentityAcrossDeferral(t *testing.T) {
	psk := testKey(t, 0x79)
	clk := newFakeClock()
	m, scheduler := newPacingIdentityMultipath(t, psk, clk)

	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	// Precondition: path 0 (slow) deferred, path 1 (fast) is the sole bound path.
	if len(m.paths) != 1 || len(m.deferred) != 1 {
		t.Fatalf("after Open: paths=%d deferred=%d, want 1 and 1", len(m.paths), len(m.deferred))
	}
	if m.paths[0].name != "fast" {
		t.Fatalf("sole bound path = %q, want %q", m.paths[0].name, "fast")
	}

	// (A) The sole bound path (fast) sits at scheduler index 0 and MUST carry its OWN
	// (fast) capacity/burst — not slow's, positionally carried from the pre-deferral slot.
	capFPS, burst, ok := scheduler.PathPacingForTest(0)
	if !ok {
		t.Fatal("scheduler reports no per-path pacer for the bound path (pacing should be on)")
	}
	if capFPS != fastCapFPS || burst != fastBurst {
		t.Fatalf("bound path (fast) pacer = {cap=%g burst=%g}, want its OWN {cap=%g burst=%g} "+
			"(D79: the reconcile carried the pacer config by slice position, so fast inherited slow's rate)",
			capFPS, burst, fastCapFPS, fastBurst)
	}

	// Promote the deferred slow path: it binds via the loopback stand-in and joins as the tail.
	binder := &fakeDeferredBinder{}
	m.deferredListen = binder.listen
	binder.arm()
	m.reconcileDeferred()
	if len(m.paths) != 2 || len(m.deferred) != 0 {
		t.Fatalf("after promote: paths=%d deferred=%d, want 2 and 0", len(m.paths), len(m.deferred))
	}
	if m.paths[1].name != "slow" {
		t.Fatalf("promoted path = %q, want %q", m.paths[1].name, "slow")
	}

	// (B) The promoted path (slow, scheduler index 1) MUST carry its OWN (slow) config, and
	// the pre-existing fast path (index 0) MUST still carry fast — no cross-contamination.
	capFast, burstFast, okFast := scheduler.PathPacingForTest(0)
	capSlow, burstSlow, okSlow := scheduler.PathPacingForTest(1)
	if !okFast || !okSlow {
		t.Fatalf("missing per-path pacer after promote: fastOK=%v slowOK=%v", okFast, okSlow)
	}
	if capFast != fastCapFPS || burstFast != fastBurst {
		t.Fatalf("fast pacer after promote = {cap=%g burst=%g}, want {cap=%g burst=%g}", capFast, burstFast, fastCapFPS, fastBurst)
	}
	if capSlow != slowCapFPS || burstSlow != slowBurst {
		t.Fatalf("promoted (slow) pacer = {cap=%g burst=%g}, want its OWN {cap=%g burst=%g} "+
			"(D79: AddPath seeded the promoted path from the tail config, not its own identity)",
			capSlow, burstSlow, slowCapFPS, slowBurst)
	}
}
