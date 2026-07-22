package bind

import (
	"crypto/rand"
	"io"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/adaptivefec"
	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/fec"
	"github.com/7mind/wanbond/internal/log"
	"github.com/7mind/wanbond/internal/sched"
	"github.com/7mind/wanbond/internal/telemetry"
)

// adaptiveFECConfigs returns a matched (fec.Config, adaptivefec.Config) pair for the
// adaptive-wiring tests: a small group so a full group closes on a few Admits, with the
// parity ceiling exposed to the controller as MaxParity.
func adaptiveFECConfigs() (fec.Config, adaptivefec.Config) {
	const k, ceiling = 6, 4
	fc := fec.Config{DataShards: k, ParityShards: ceiling, Deadline: 50 * time.Millisecond}
	ac := adaptivefec.DefaultConfig()
	ac.DataShards = k
	ac.MaxParity = ceiling
	return fc, ac
}

// newAdaptiveProbingMultipath builds a probing Multipath with the FEC plane in ADAPTIVE
// mode over n loopback paths, plus the fake clock the probers read. It mirrors
// newProbingMultipath but threads the FEC + adaptive configs through NewMultipath.
func newAdaptiveProbingMultipath(t testing.TB, n int, psk config.Key, clk telemetry.Clock) (*Multipath, []*telemetry.Prober) {
	t.Helper()
	lg, err := log.New("error", io.Discard)
	if err != nil {
		t.Fatalf("build logger: %v", err)
	}
	paths := loopbackPaths(n)
	cfg := telemetry.ProberConfig{
		LossWindow: 0,
		Liveness:   telemetry.LivenessConfig{DownAfter: testProbeDownAfter, UpAfterSuccesses: testProbeUpSucc},
	}
	newProber := func(name string, id uint8, _ time.Duration) *telemetry.Prober {
		return telemetry.NewProber(name, id, testProbeSessionID, psk, cfg, clk, lg)
	}
	probers := make([]*telemetry.Prober, n)
	health := make([]sched.PathHealth, n)
	for i := range paths {
		probers[i] = newProber(paths[i].Name, uint8(i), paths[i].RideThrough)
		health[i] = probers[i]
	}
	scheduler, err := sched.NewActiveBackup(health, sched.Config{FailbackAfter: time.Hour}, clk, lg)
	if err != nil {
		t.Fatalf("build scheduler: %v", err)
	}
	fc, ac := adaptiveFECConfigs()
	m, err := NewMultipath(paths, psk, scheduler, probers, newProber, &fc, &ac, config.Amnezia{}, lg)
	if err != nil {
		t.Fatalf("NewMultipath(adaptive): %v", err)
	}
	return m, probers
}

// bringProberUpWithLoss drives prober through send/reflect/handle-echo so it is StateUp
// and reports approximately lossFrac probe loss: it sends `total` probes and reflects all
// but a spread-out `drop` of them (a dropped probe's ProbeSeq is never observed, so it
// reads as loss), keeping the final run of echoes unbroken so the path is Up. It advances
// clk by one RTT per echo so no silence-driven Down fires.
func bringProberUpWithLoss(t testing.TB, pr *telemetry.Prober, psk config.Key, clk *fakeClock, total, drop int) {
	t.Helper()
	reflector := telemetry.NewReflector(psk, rand.Reader)
	// Drop the first `drop` even-indexed probes; the whole tail is delivered so the last
	// UpAfterSuccesses echoes are consecutive successes and the path settles Up.
	dropped := 0
	for i := 0; i < total; i++ {
		raw, err := pr.SendProbe()
		if err != nil {
			t.Fatalf("send probe %d: %v", i, err)
		}
		if dropped < drop && i%2 == 0 && i < total-testProbeUpSucc*2 {
			dropped++
			continue // this probe is "lost": never echoed
		}
		echo, _, err := reflector.Reflect(raw)
		if err != nil {
			t.Fatalf("reflect probe %d: %v", i, err)
		}
		clk.advance(testProbeRTT)
		if err := pr.HandleEcho(echo); err != nil {
			t.Fatalf("handle echo %d: %v", i, err)
		}
	}
	pr.Tick()
	if pr.State() != telemetry.StateUp {
		t.Fatalf("prober not Up after %d probes (%d dropped)", total, dropped)
	}
	if loss := pr.Estimate().Loss; loss <= 0 {
		t.Fatalf("prober loss = %v, want > 0 (dropped %d of %d)", loss, dropped, total)
	}
}

// TestAdaptiveControllerDrivesEncoderParity is the T29 wiring proof: with the FEC plane
// in adaptive mode, the FEC tick loop's controller drive reads the up path's measured
// loss and RETARGETS the encoder's per-group parity. Before any drive the encoder emits
// zero parity (the controller's starting point — no standing redundancy until loss is
// observed); after driving against a lossy-but-up path it emits the controller's sized
// parity, which is positive and at/below the ceiling.
func TestAdaptiveControllerDrivesEncoderParity(t *testing.T) {
	psk := testKey(t, 0x51)
	clk := newFakeClock()
	m, probers := newAdaptiveProbingMultipath(t, 1, psk, clk)
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	fc, ac := adaptiveFECConfigs()

	// Initial parity target: 0 (adopted from the controller at Open), so a group closed
	// now emits no parity.
	if got := admitFullGroupParity(t, m, fc.DataShards); got != 0 {
		t.Fatalf("initial adaptive parity = %d, want 0 (controller starts at M=0)", got)
	}

	// Bring the path Up with ~15% probe loss, then drive the controller once.
	bringProberUpWithLoss(t, probers[0], psk, clk, 40, 6)
	loss := probers[0].Estimate().Loss

	m.mu.Lock()
	m.driveAdaptiveControllerLocked(m.peerState)
	ctrl := m.fecSend.Load().ctrl
	target := ctrl.Parity()
	smoothed := ctrl.SmoothedLoss()
	m.mu.Unlock()

	if target <= 0 || target > ac.MaxParity {
		t.Fatalf("controller target after loss=%.3f is %d, want in (0,%d]", loss, target, ac.MaxParity)
	}

	// The lock-free FEC snapshot now reports the driven decision (T263): an adaptive-mode
	// peer fabricates the Adaptive series, the driven parity/smoothed loss match the
	// controller, and the eligible signal reflects the single up path's measured loss.
	snaps := m.PeerSnapshots()
	if len(snaps) == 0 {
		t.Fatalf("PeerSnapshots returned no peer")
	}
	adaptive := snaps[0].FEC.Adaptive
	if adaptive == nil {
		t.Fatalf("adaptive-mode snapshot FEC.Adaptive == nil, want the driven decision")
	}
	if adaptive.Parity != target {
		t.Fatalf("snapshot Adaptive.Parity = %d, want ctrl.Parity() %d", adaptive.Parity, target)
	}
	if adaptive.SmoothedLoss != smoothed {
		t.Fatalf("snapshot Adaptive.SmoothedLoss = %v, want ctrl.SmoothedLoss() %v", adaptive.SmoothedLoss, smoothed)
	}
	if adaptive.EligibleLoss != loss {
		t.Fatalf("snapshot Adaptive.EligibleLoss = %v, want the up path's measured loss %v", adaptive.EligibleLoss, loss)
	}
	if adaptive.EligiblePaths < 1 {
		t.Fatalf("snapshot Adaptive.EligiblePaths = %d, want >= 1 (the up path)", adaptive.EligiblePaths)
	}

	// The encoder now emits exactly the controller's target parity per group.
	if got := admitFullGroupParity(t, m, fc.DataShards); got != target {
		t.Fatalf("encoder emitted %d parity after drive, want the controller target %d", got, target)
	}
}

// TestAdaptiveControllerHoldsWithNoEligiblePath asserts the drive holds the current
// target when no up/probed path is available (nothing to size against), rather than
// slewing on a phantom zero-loss sample — and that the hold-branch publish (T263) still
// records the eligible signal (count 0) WITHOUT clobbering the held parity/smoothed
// values. To make the assertions non-vacuous the test first seeds NON-ZERO published
// state (a driven Parity > 0 against a lossy up path), then forces the path Down and
// re-drives: the eligible count must transition >=1 -> 0 while Parity and SmoothedLoss
// retain the previously driven non-zero values (distinguishing the hold from both the
// zero-initialized atomics and a clobbering full publish).
func TestAdaptiveControllerHoldsWithNoEligiblePath(t *testing.T) {
	psk := testKey(t, 0x52)
	clk := newFakeClock()
	m, probers := newAdaptiveProbingMultipath(t, 1, psk, clk)
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	// Seed: bring the path Up with measured loss and drive once, publishing a non-zero
	// decision (TestAdaptiveControllerDrivesEncoderParity proves this drive in detail).
	bringProberUpWithLoss(t, probers[0], psk, clk, 40, 6)
	m.mu.Lock()
	m.driveAdaptiveControllerLocked(m.peerState)
	ctrl := m.fecSend.Load().ctrl
	drivenParity := ctrl.Parity()
	drivenSmoothed := ctrl.SmoothedLoss()
	m.mu.Unlock()
	if drivenParity <= 0 || drivenSmoothed <= 0 {
		t.Fatalf("seed drive published parity=%d smoothed=%v, want both > 0", drivenParity, drivenSmoothed)
	}
	snaps := m.PeerSnapshots()
	if len(snaps) == 0 {
		t.Fatalf("PeerSnapshots returned no peer")
	}
	adaptive := snaps[0].FEC.Adaptive
	if adaptive == nil || adaptive.EligiblePaths < 1 {
		t.Fatalf("seeded snapshot Adaptive = %+v, want EligiblePaths >= 1", adaptive)
	}

	// Force the path Down: silence it past the liveness deadline, then Tick.
	clk.advance(testProbeDownAfter + testProbeInterval)
	probers[0].Tick()
	if probers[0].State() != telemetry.StateDown {
		t.Fatalf("prober after silence = %v, want down", probers[0].State())
	}

	// Re-drive with no eligible path. The drive's self-throttle reads the REAL clock
	// (time.Now), so reset haveControlTick under m.mu to admit this drive deterministically.
	m.mu.Lock()
	fs := m.fecSend.Load()
	fs.haveControlTick = false
	before := fs.ctrl.Parity()
	m.driveAdaptiveControllerLocked(m.peerState)
	after := fs.ctrl.Parity()
	m.mu.Unlock()
	if before != after {
		t.Fatalf("controller moved (%d -> %d) with no eligible path; want held", before, after)
	}

	// The snapshot records the eligible signal — the count transitioned >=1 -> 0, the
	// only way an operator sees the 'no eligible path' hold — while Parity and
	// SmoothedLoss hold the previously driven non-zero decision (T263).
	snaps = m.PeerSnapshots()
	if len(snaps) == 0 {
		t.Fatalf("PeerSnapshots returned no peer")
	}
	adaptive = snaps[0].FEC.Adaptive
	if adaptive == nil {
		t.Fatalf("adaptive-mode snapshot FEC.Adaptive == nil, want the held decision")
	}
	if adaptive.EligiblePaths != 0 {
		t.Fatalf("snapshot Adaptive.EligiblePaths = %d, want 0 (no eligible path)", adaptive.EligiblePaths)
	}
	if adaptive.Parity != drivenParity {
		t.Fatalf("snapshot Adaptive.Parity = %d, want the held driven target %d", adaptive.Parity, drivenParity)
	}
	if adaptive.SmoothedLoss != drivenSmoothed {
		t.Fatalf("snapshot Adaptive.SmoothedLoss = %v, want the held driven value %v", adaptive.SmoothedLoss, drivenSmoothed)
	}
}

// TestFixedRatioPeerHasNilAdaptiveSnapshot asserts a fixed-ratio FEC peer (no adaptive
// controller) leaves FECStats.Adaptive nil, so the snapshot fabricates no adaptive series
// where the controller does not exist (the Aggregation nil-precedent, T146).
func TestFixedRatioPeerHasNilAdaptiveSnapshot(t *testing.T) {
	psk := testKey(t, 0x53)
	fc := fec.Config{DataShards: 6, ParityShards: 2, Deadline: 50 * time.Millisecond}
	m := newMultipathFEC(t, loopbackPaths(1), psk, &fc)
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	m.mu.Lock()
	haveCtrl := m.fecSend.Load().ctrl != nil
	m.mu.Unlock()
	if haveCtrl {
		t.Fatalf("fixed-ratio bind has an adaptive controller; want none")
	}

	snaps := m.PeerSnapshots()
	if len(snaps) == 0 {
		t.Fatalf("PeerSnapshots returned no peer")
	}
	if snaps[0].FEC.Adaptive != nil {
		t.Fatalf("fixed-ratio snapshot FEC.Adaptive = %+v, want nil", snaps[0].FEC.Adaptive)
	}
}

// admitFullGroupParity admits k opaque frames into the send encoder (closing a full
// group) and returns how many parity shards it emitted. It drives the encoder directly to
// observe the per-group parity the current target produces.
func admitFullGroupParity(t testing.TB, m *Multipath, k int) int {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	var parity int
	for i := 0; i < k; i++ {
		_, par, err := m.fecSend.Load().enc.Admit([]byte{byte(i), 0xAA})
		if err != nil {
			t.Fatalf("admit %d: %v", i, err)
		}
		if par != nil {
			parity = len(par)
		}
	}
	return parity
}
