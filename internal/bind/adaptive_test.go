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
	fc, ac := adaptiveFECConfigs()
	return newAdaptiveProbingMultipathCfg(t, n, psk, clk, 0, fc, ac)
}

// newAdaptiveProbingMultipathCfg generalizes newAdaptiveProbingMultipath over the FEC/
// adaptive configs and each prober's loss window (window<=0 uses the telemetry default,
// 512 — matching newAdaptiveProbingMultipath). It is the E3 anti-phase trajectory's entry
// point (adaptive_e3_test.go), which needs the D96 residual-SLA field shape rather than
// the legacy safety-factor default adaptiveFECConfigs returns.
func newAdaptiveProbingMultipathCfg(t testing.TB, n int, psk config.Key, clk telemetry.Clock, window int, fc fec.Config, ac adaptivefec.Config) (*Multipath, []*telemetry.Prober) {
	t.Helper()
	lg, err := log.New("error", io.Discard)
	if err != nil {
		t.Fatalf("build logger: %v", err)
	}
	paths := loopbackPaths(n)
	cfg := telemetry.ProberConfig{
		LossWindow: window,
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
	m, err := NewMultipath(paths, psk, scheduler, probers, newProber, &fc, &ac, config.Amnezia{}, lg)
	if err != nil {
		t.Fatalf("NewMultipath(adaptive): %v", err)
	}
	// Wire the SAME fake clock the probers/scheduler read into the bind's adaptive-drive
	// throttle seam (pre-Open), so driveAdaptiveControllerLocked's self-throttle advances in
	// lockstep with liveness transitions instead of on the wall clock (D97).
	m.clock = clk
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

// sendCleanProbes sends `total` probes through prober and echoes EVERY one (no loss),
// advancing clk by one RTT per echo. It does NOT Tick or assert liveness/loss, so a caller
// can grow a path's LossSamples (past the min-sample floor) without disturbing its existing
// loss window — the earlier drops stay in the trailing window and only the denominator grows.
func sendCleanProbes(t testing.TB, pr *telemetry.Prober, psk config.Key, clk *fakeClock, total int) {
	t.Helper()
	reflector := telemetry.NewReflector(psk, rand.Reader)
	for i := 0; i < total; i++ {
		raw, err := pr.SendProbe()
		if err != nil {
			t.Fatalf("send probe %d: %v", i, err)
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
}

// bringProberUpClean drives prober StateUp with ZERO probe loss (every probe echoed).
func bringProberUpClean(t testing.TB, pr *telemetry.Prober, psk config.Key, clk *fakeClock, total int) {
	t.Helper()
	sendCleanProbes(t, pr, psk, clk, total)
	pr.Tick()
	if pr.State() != telemetry.StateUp {
		t.Fatalf("prober not Up after %d clean probes", total)
	}
	if loss := pr.Estimate().Loss; loss != 0 {
		t.Fatalf("clean prober loss = %v, want 0", loss)
	}
}

// TestAdaptiveControllerDrivesFromActivePathNotStandby is the D96 mechanism-2 / E1 oracle:
// under active-backup the controller input must be the loss on the path that ACTUALLY carries
// DATA (the single active path), NOT a role-agnostic MAX across every StateUp prober. It builds
// a two-path active-backup bond whose PRIMARY (index 0, the active data path) is clean while a
// StateUp STANDBY (index 1) reports ~11% probe loss. Post-change the drive Observes the active
// path's loss (0) and holds M at 0; the pre-change role-agnostic MAX fed the standby's ~11% loss
// into Observe and raised M. The assertion pins the published eligible signal to the active
// path's loss (0), which FAILS on the pre-change tree (it published the standby's ~0.11).
func TestAdaptiveControllerDrivesFromActivePathNotStandby(t *testing.T) {
	psk := testKey(t, 0x54)
	clk := newFakeClock()
	m, probers := newAdaptiveProbingMultipath(t, 2, psk, clk)
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	// Primary (index 0) clean and well past the min-sample floor; standby (index 1) StateUp
	// with a spread ~11% probe loss. Both eligible, so a role-agnostic MAX would see 0.11.
	bringProberUpClean(t, probers[0], psk, clk, 48)
	bringProberUpWithLoss(t, probers[1], psk, clk, 45, 5)
	activeLoss := probers[0].Estimate().Loss
	standbyLoss := probers[1].Estimate().Loss
	if activeLoss != 0 || standbyLoss <= 0.05 {
		t.Fatalf("scenario setup: activeLoss=%v (want 0), standbyLoss=%v (want > 0.05)", activeLoss, standbyLoss)
	}

	// Refresh the scheduler's cached active selection from liveness so DataPaths reports the
	// primary as the sole data carrier (both up -> best eligible == index 0).
	m.scheduler.Recompute()

	m.mu.Lock()
	m.driveAdaptiveControllerLocked(m.peerState)
	parityAfter := m.fecSend.Load().ctrl.Parity()
	m.mu.Unlock()

	snaps := m.PeerSnapshots()
	if len(snaps) == 0 {
		t.Fatalf("PeerSnapshots returned no peer")
	}
	adaptive := snaps[0].FEC.Adaptive
	if adaptive == nil {
		t.Fatalf("adaptive-mode snapshot FEC.Adaptive == nil")
	}
	// The controller input is the ACTIVE data path's loss (0), not the standby's ~0.11.
	if adaptive.EligibleLoss != activeLoss {
		t.Fatalf("EligibleLoss = %v, want the active data path's loss %v (not the standby's %v)",
			adaptive.EligibleLoss, activeLoss, standbyLoss)
	}
	// A clean active path leaves M at 0; the pre-change tree raised it off the standby's loss.
	if parityAfter != 0 {
		t.Fatalf("controller raised M to %d off a clean active path; want 0 (standby loss must not drive M)", parityAfter)
	}
	// Exactly one data path (the active) is sample-eligible.
	if adaptive.EligiblePaths != 1 {
		t.Fatalf("EligiblePaths = %d, want 1 (the single active data path)", adaptive.EligiblePaths)
	}
}

// TestAdaptiveControllerFloorsSmallSampleSpike is the D96 mechanism-3 / E4 oracle: a data
// path whose loss window is still in its EARLY regime — a single dropped probe at a small
// LossSamples denominator, which reads as a large loss FRACTION — is EXCLUDED by the min-sample
// floor and does NOT raise M. It builds a two-path active-backup bond whose ACTIVE data path
// (index 0) has exactly one dropped probe over a handful of samples (< minAdaptiveLossSamples),
// a spike a role-only fix would still Observe and raise M on, alongside a clean StateUp standby.
// The floor makes the drive HOLD (no sample-eligible data path) so M stays 0 despite the spike.
func TestAdaptiveControllerFloorsSmallSampleSpike(t *testing.T) {
	psk := testKey(t, 0x55)
	clk := newFakeClock()
	m, probers := newAdaptiveProbingMultipath(t, 2, psk, clk)
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	// Active data path (index 0): a single charged drop over ~11 samples — StateUp (the tail is
	// consecutive echoes) but a small-denominator spike below the floor. (The very first probe's
	// drop is the un-chargeable window prefix, so drop=2 yields one in-window drop.) Standby
	// (index 1) clean and well past the floor.
	bringProberUpWithLoss(t, probers[0], psk, clk, 12, 2)
	bringProberUpClean(t, probers[1], psk, clk, 48)
	activeEst := probers[0].Estimate()
	if activeEst.Loss <= 0 {
		t.Fatalf("active path loss = %v, want a positive small-sample spike", activeEst.Loss)
	}
	if activeEst.LossSamples >= minAdaptiveLossSamples {
		t.Fatalf("active path LossSamples = %d, want below the floor %d (early regime)", activeEst.LossSamples, minAdaptiveLossSamples)
	}

	m.scheduler.Recompute()
	m.mu.Lock()
	before := m.fecSend.Load().ctrl.Parity()
	m.driveAdaptiveControllerLocked(m.peerState)
	after := m.fecSend.Load().ctrl.Parity()
	m.mu.Unlock()
	if before != 0 || after != 0 {
		t.Fatalf("controller M moved %d -> %d off a small-sample spike; want held at 0 (floor must suppress it)", before, after)
	}

	snaps := m.PeerSnapshots()
	if len(snaps) == 0 {
		t.Fatalf("PeerSnapshots returned no peer")
	}
	adaptive := snaps[0].FEC.Adaptive
	if adaptive == nil {
		t.Fatalf("adaptive-mode snapshot FEC.Adaptive == nil")
	}
	// The floor left no sample-eligible data path, so the drive took the count==0 HOLD branch.
	if adaptive.EligiblePaths != 0 {
		t.Fatalf("EligiblePaths = %d, want 0 (the sole data path is below the min-sample floor)", adaptive.EligiblePaths)
	}
}

// TestAdaptiveControllerSinglePathEarlyRegimeHoldThenObserve pins the intended single-path
// behaviour change (D96 mechanism 3): the sole data path HOLDs while its LossSamples is below
// the min-sample floor (an early-regime denominator too small to trust), then Observes normally
// once the window fills past the floor — at which point the controller input equals the path's
// raw per-path loss, byte-identical to the pre-change single-path signal (max over one path ==
// that path's loss). The earlier drops persist in the trailing loss window as the denominator
// grows, so the loss the drive Observes at floor-crossing is the same raw fraction.
func TestAdaptiveControllerSinglePathEarlyRegimeHoldThenObserve(t *testing.T) {
	psk := testKey(t, 0x56)
	clk := newFakeClock()
	m, probers := newAdaptiveProbingMultipath(t, 1, psk, clk)
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	// Phase 1 — EARLY REGIME: StateUp with two drops over ~12 samples (below the floor). Despite
	// a clearly positive loss reading, the drive HOLDs: the small denominator is untrustworthy.
	bringProberUpWithLoss(t, probers[0], psk, clk, 12, 2)
	early := probers[0].Estimate()
	if early.Loss <= 0 || early.LossSamples >= minAdaptiveLossSamples {
		t.Fatalf("phase-1 estimate = %+v, want positive loss and LossSamples < %d", early, minAdaptiveLossSamples)
	}
	m.scheduler.Recompute()
	m.mu.Lock()
	m.driveAdaptiveControllerLocked(m.peerState)
	held := m.fecSend.Load().ctrl.Parity()
	m.mu.Unlock()
	if held != 0 {
		t.Fatalf("phase-1 M = %d, want 0 (early-regime HOLD, below the min-sample floor)", held)
	}
	if ep := m.PeerSnapshots()[0].FEC.Adaptive.EligiblePaths; ep != 0 {
		t.Fatalf("phase-1 EligiblePaths = %d, want 0 (HOLD)", ep)
	}

	// Phase 2 — FLOOR MET: keep echoing cleanly so the window denominator grows past the floor
	// while the two early drops remain in the trailing window. The drive now Observes.
	sendCleanProbes(t, probers[0], psk, clk, 30)
	probers[0].Tick()
	full := probers[0].Estimate()
	if full.LossSamples < minAdaptiveLossSamples {
		t.Fatalf("phase-2 LossSamples = %d, want >= floor %d", full.LossSamples, minAdaptiveLossSamples)
	}
	m.scheduler.Recompute()
	m.mu.Lock()
	m.driveAdaptiveControllerLocked(m.peerState)
	m.mu.Unlock()
	adaptive := m.PeerSnapshots()[0].FEC.Adaptive
	if adaptive.EligiblePaths != 1 {
		t.Fatalf("phase-2 EligiblePaths = %d, want 1 (the sole data path now Observes)", adaptive.EligiblePaths)
	}
	// Byte-identical steady-state single-path signal: the controller input IS the path's raw loss.
	if adaptive.EligibleLoss != full.Loss {
		t.Fatalf("phase-2 EligibleLoss = %v, want the sole data path's raw loss %v", adaptive.EligibleLoss, full.Loss)
	}
}

// TestWeightedDataPathLossMix is the deterministic weighted-striping unit oracle (T272,
// folded into the E-suite as E1's weighted-aggregation counterpart — see
// TestAdaptiveControllerDrivesFromActivePathNotStandby for the active-backup case) over the
// pure mix helper weightedDataPathLoss (which dataPathLossLocked delegates to): under weighted
// aggregation the controller input is the WEIGHT-WEIGHTED MIX of the aggregating paths' losses,
// with the per-path min-sample floor applied and the surviving weights RENORMALIZED over the
// sample-eligible subset. It asserts the full mix, the renormalization when the floor excludes a
// strict subset, the empty-eligible-subset HOLD, and the single-carrier (active-backup /
// collapsed) primary-only case that the aggregation-gate-disengage discontinuity steps to.
func TestWeightedDataPathLossMix(t *testing.T) {
	const floor = minAdaptiveLossSamples
	est := func(loss float64, samples int) telemetry.Estimate {
		return telemetry.Estimate{Loss: loss, LossSamples: samples}
	}
	// A backing table keyed by DataPath.Index; missing indices report unreadable.
	build := func(m map[int]telemetry.Estimate) func(int) (telemetry.Estimate, bool) {
		return func(idx int) (telemetry.Estimate, bool) {
			e, ok := m[idx]
			return e, ok
		}
	}
	approx := func(got, want float64) bool { return got-want < 1e-9 && want-got < 1e-9 }

	// Case A — full aggregating mix, all paths sample-eligible: weight-weighted mean.
	dps := []sched.DataPath{{Index: 0, Weight: 0.5}, {Index: 1, Weight: 0.3}, {Index: 2, Weight: 0.2}}
	tbl := map[int]telemetry.Estimate{
		0: est(0.10, floor),
		1: est(0.20, floor+100),
		2: est(0.30, floor),
	}
	got, count := weightedDataPathLoss(dps, build(tbl))
	want := 0.5*0.10 + 0.3*0.20 + 0.2*0.30 // weightTotal == 1.0
	if count != 3 || !approx(got, want) {
		t.Fatalf("full mix: got (%v, %d), want (%v, 3)", got, count, want)
	}

	// Case B — RENORMALIZATION: path 1 falls below the floor and is excluded; the mix is taken
	// over {0,2} and renormalized by their weight sum (0.7), NOT the original 1.0.
	tbl[1] = est(0.99, floor-1) // a large fraction at a tiny denominator — must be floored out
	got, count = weightedDataPathLoss(dps, build(tbl))
	want = (0.5*0.10 + 0.2*0.30) / (0.5 + 0.2)
	if count != 2 || !approx(got, want) {
		t.Fatalf("renormalized subset: got (%v, %d), want (%v, 2) renormalized over {0,2}", got, count, want)
	}
	// The floored path's large loss must not leak into the mix (guard against sum/1.0 mistakes).
	if approx(got, 0.5*0.10+0.3*0.99+0.2*0.30) {
		t.Fatalf("renormalized mix leaked the floored path's loss: %v", got)
	}

	// Case C — every data path below the floor: EMPTY eligible subset -> HOLD (0, 0).
	allSmall := map[int]telemetry.Estimate{0: est(0.5, floor-1), 1: est(0.5, 1), 2: est(0.5, floor-10)}
	got, count = weightedDataPathLoss(dps, build(allSmall))
	if count != 0 || got != 0 {
		t.Fatalf("all-below-floor: got (%v, %d), want (0, 0) HOLD", got, count)
	}

	// Case D — single-carrier (active-backup, or a collapsed data-thrift weighted bond): the mix
	// collapses to the sole primary's own loss at weight 1.0.
	single := []sched.DataPath{{Index: 0, Weight: 1.0}}
	got, count = weightedDataPathLoss(single, build(map[int]telemetry.Estimate{0: est(0.17, floor)}))
	if count != 1 || !approx(got, 0.17) {
		t.Fatalf("single carrier: got (%v, %d), want (0.17, 1)", got, count)
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

	// Force the path Down and re-drive with no eligible path — the whole sequence under m.mu.
	// Holding the lock across clk.advance + probers[0].Tick() + driveAdaptiveControllerLocked
	// EXCLUDES the concurrent fecTickLoop tick (its TryLock skips): otherwise, in the window
	// after the advance while the prober still reports StateUp, a background tick could Observe
	// at the advanced fake time and stamp the throttle, spuriously throttling this explicit
	// drive. The prober is a documented leaf lock (State/Tick take only their own mutex, never
	// call back into the Bind), so acquiring it under m.mu is legal. After the stamp reorder
	// (D97) the hold branch never stamps, so this re-drive needs no haveControlTick reset to be
	// admitted — the throttle sees the 400ms fake-clock advance (> adaptiveControlInterval).
	m.mu.Lock()
	clk.advance(testProbeDownAfter + testProbeInterval)
	probers[0].Tick()
	if st := probers[0].State(); st != telemetry.StateDown {
		m.mu.Unlock()
		t.Fatalf("prober after silence = %v, want down", st)
	}
	fs := m.fecSend.Load()
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
