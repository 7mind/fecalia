package bind

import (
	"crypto/rand"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/adaptivefec"
	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/fec"
	"github.com/7mind/wanbond/internal/telemetry"
)

// E3 — the deterministic anti-phase trajectory (D96).
//
// residualAdaptiveFECConfigs returns a matched (fec.Config, adaptivefec.Config) pair in
// RESIDUAL-SLA mode: the SAME D96 field shape (K=8, target_residual=0.001) the pure-
// controller E2 sweep uses (internal/adaptivefec/residual_gate_test.go), so this test's M
// targets are literally the same residual-model plateaus already pinned there.
func residualAdaptiveFECConfigs() (fec.Config, adaptivefec.Config) {
	const k, ceiling = 8, 6
	fc := fec.Config{DataShards: k, ParityShards: ceiling, Deadline: 50 * time.Millisecond}
	ac := adaptivefec.DefaultConfig()
	ac.DataShards = k
	ac.MaxParity = ceiling
	ac.SafetyFactor = 0
	ac.TargetResidual = 0.001
	return fc, ac
}

// sendProbesWithLeadingDrops sends `total` probes through pr, DROPPING the first `drop`
// of them (never echoed, so each reads as per-path loss) and echoing the remaining
// total-drop cleanly, advancing clk by one RTT per echo. pr is assumed already StateUp
// (this does not Tick). Because each probe's ring slot is unique while total <= the
// prober's configured loss window (512 here) and this call's `total` probes are the LAST
// `total` sequence numbers sent once it returns, the resulting Estimate() reflects EXACTLY
// this call: LossSamples == total and Loss == drop/total, independent of any earlier
// traffic on pr (older samples slide out of the trailing window).
func sendProbesWithLeadingDrops(t testing.TB, pr *telemetry.Prober, psk config.Key, clk *fakeClock, total, drop int) {
	t.Helper()
	reflector := telemetry.NewReflector(psk, rand.Reader)
	for i := 0; i < total; i++ {
		raw, err := pr.SendProbe()
		if err != nil {
			t.Fatalf("send probe %d: %v", i, err)
		}
		if i < drop {
			continue // dropped: never echoed
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

// TestAdaptiveControllerAntiPhaseTrajectory is the D96 / E3 oracle: it drives ONE peer's
// adaptive controller through a SCRIPTED, deterministic two-path (active-backup) loss trace
// exercising all three D96 mechanisms end-to-end through the REAL Multipath wiring
// (driveAdaptiveControllerLocked -> dataPathLossLocked -> the controller -> the encoder),
// asserting EXCLUSIVELY via the G29 AdaptiveFECStats snapshot surface (T263) so the
// observability contract is pinned alongside the control behavior:
//
//   - phase 1 (BLIP): an isolated one-quantum (1/512) lost probe on the ACTIVE data path.
//     Mechanism 1's derived, quantization-aware raise gate (T273) floors the residual-mode
//     raise gate at two quanta (2/512), so a single quantum must NOT cross it: M stays 0
//     (no over-provisioning of an isolated blip).
//   - phase 2 (SUSTAINED REAL ACTIVE-PATH LOSS): the active path's measured loss rises to a
//     sustained ~3.5% (18/512) — comfortably below the LEGACY 5% RaiseThreshold but above
//     the derived residual-SLA gate. M must track it up, converging to the residual model's
//     target parity (mechanism 1) within the controller's slew bound (MaxStep=2).
//   - phase 3 (CLEARED): the active path returns to clean. M must HOLD through the Dwell
//     window (no premature shed), then fall to 0.
//   - phase 4 (STANDBY NOISE): AFTER shedding, the STANDBY path — which never carries data
//     under active-backup — is driven lossy. Mechanism 2's data-path-only signal selection
//     (T272) must NOT let it re-raise M.
//
// Each phase's probe shaping AND its drive iterations run under a single m.mu critical
// section (the discipline TestAdaptiveControllerHoldsWithNoEligiblePath documents): this
// EXCLUDES the concurrent real-wall-clock fecTickLoop goroutine (its TryLock skips) for the
// whole phase, so the scripted trajectory is fully deterministic despite the background
// ticker.
func TestAdaptiveControllerAntiPhaseTrajectory(t *testing.T) {
	psk := testKey(t, 0x58)
	clk := newFakeClock()
	fc, ac := residualAdaptiveFECConfigs()
	m, probers := newAdaptiveProbingMultipathCfg(t, 2, psk, clk, 0, fc, ac)
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	active, standby := probers[0], probers[1]

	// Establish a stable, both-up baseline (well past the min-sample floor) before scripting
	// the trajectory; the floor and liveness mechanics are proven separately by E1/E4.
	bringProberUpClean(t, active, psk, clk, 40)
	bringProberUpClean(t, standby, psk, clk, 40)

	// runPhase shapes probe traffic (shape, or nil to leave the probers untouched) and then
	// drives the controller driveCount times, ALL under one m.mu critical section, and
	// returns the resulting G29 snapshot read afterward (PeerSnapshots takes no m.mu, so it
	// is called only once unlocked).
	runPhase := func(shape func(), driveCount int) AdaptiveFECStats {
		t.Helper()
		m.mu.Lock()
		if shape != nil {
			shape()
		}
		m.scheduler.Recompute()
		for i := 0; i < driveCount; i++ {
			clk.advance(adaptiveControlInterval)
			m.driveAdaptiveControllerLocked(m.peerState)
		}
		m.mu.Unlock()

		snaps := m.PeerSnapshots()
		if len(snaps) == 0 {
			t.Fatalf("PeerSnapshots returned no peer")
		}
		if snaps[0].FEC.Adaptive == nil {
			t.Fatalf("adaptive-mode snapshot FEC.Adaptive == nil")
		}
		return *snaps[0].FEC.Adaptive
	}

	// --- Phase 1: BLIP on the active path (1 drop / 512, one estimator quantum). ---
	phase1 := runPhase(func() {
		sendProbesWithLeadingDrops(t, active, psk, clk, 512, 1)
		if got, want := active.Estimate().Loss, 1.0/512; got != want {
			t.Fatalf("phase1 setup: active loss = %v, want exactly %v (1/512 quantum blip)", got, want)
		}
	}, 5)
	if phase1.Parity != 0 {
		t.Fatalf("phase1 (blip): Parity = %d, want 0 (a one-quantum blip must not over-provision)", phase1.Parity)
	}

	// --- Phase 2: SUSTAINED real active-path loss (~3.5%, 18/512). ---
	const wantPeak = 2 // residualTargetParity(K=8,target=0.001) at loss~=0.0352 (see internal/adaptivefec's E2 model — the same plateau TestResidualRaiseGateDerivedFromTarget/TestResidualNearGateNoFlapAndRaise pin for this field shape)
	phase2 := runPhase(func() {
		sendProbesWithLeadingDrops(t, active, psk, clk, 512, 18)
		if got, want := active.Estimate().Loss, 18.0/512; got != want {
			t.Fatalf("phase2 setup: active loss = %v, want exactly %v (18/512 sustained)", got, want)
		}
	}, 15)
	if phase2.Parity != wantPeak {
		t.Fatalf("phase2 (sustained real loss): Parity = %d, want %d (residual-model target, converged within the slew bound)", phase2.Parity, wantPeak)
	}
	if phase2.EligiblePaths != 1 || phase2.EligibleLoss != 18.0/512 {
		t.Fatalf("phase2 snapshot = %+v, want EligiblePaths=1 EligibleLoss=%v (the active path's own raw loss)", phase2, 18.0/512)
	}

	// --- Phase 3: loss CLEARS — M must HOLD through Dwell, then shed to 0. ---
	phase3Hold := runPhase(func() {
		sendProbesWithLeadingDrops(t, active, psk, clk, 512, 0)
		if got := active.Estimate().Loss; got != 0 {
			t.Fatalf("phase3 setup: active loss = %v, want 0 (cleared)", got)
		}
	}, 20) // well inside the dwell-hold window (Dwell=3s / adaptiveControlInterval + EWMA decay)
	if phase3Hold.Parity != wantPeak {
		t.Fatalf("phase3 (dwell hold): Parity = %d, want %d held (dwell must not have elapsed yet)", phase3Hold.Parity, wantPeak)
	}
	phase3Shed := runPhase(nil, 25) // total well past the dwell + slew back to 0
	if phase3Shed.Parity != 0 {
		t.Fatalf("phase3 (post-dwell shed): Parity = %d, want 0", phase3Shed.Parity)
	}

	// --- Phase 4: STANDBY NOISE — must NEVER re-raise M (it carries no data). ---
	phase4 := runPhase(func() {
		sendProbesWithLeadingDrops(t, standby, psk, clk, 100, 15) // ~15% standby loss
		if got := standby.Estimate().Loss; got <= 0.05 {
			t.Fatalf("phase4 setup: standby loss = %v, want > 0.05 (noticeable noise)", got)
		}
	}, 10)
	if phase4.Parity != 0 {
		t.Fatalf("phase4 (standby noise): Parity = %d, want 0 (standby carries no data under active-backup, must not re-raise M)", phase4.Parity)
	}
	if phase4.EligiblePaths != 1 || phase4.EligibleLoss != 0 {
		t.Fatalf("phase4 snapshot = %+v, want EligiblePaths=1 EligibleLoss=0 (the still-clean active path, not the noisy standby)", phase4)
	}
}
