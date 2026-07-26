package bind

import (
	"crypto/rand"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/adaptivefec"
	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/fec"
	"github.com/7mind/wanbond/internal/frame"
	"github.com/7mind/wanbond/internal/sched"
	"github.com/7mind/wanbond/internal/telemetry"
)

type adaptiveDataPathsOverride struct {
	sched.Scheduler
	dataPaths []sched.DataPath
}

func (s *adaptiveDataPathsOverride) DataPaths() []sched.DataPath {
	return append([]sched.DataPath(nil), s.dataPaths...)
}

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
// (driveAdaptiveController -> dataPathLossLocked -> the controller -> the encoder),
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
// Each phase's probe shaping and sampling runs under one m.mu critical section;
// each immutable sample is applied synchronously by the owner, so the scripted
// trajectory stays deterministic.
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
	// shapes under m.mu, then drives the controller driveCount times after releasing it.
	runPhase := func(shape func(), driveCount int) AdaptiveFECStats {
		t.Helper()
		m.mu.Lock()
		if shape != nil {
			shape()
		}
		m.scheduler.Recompute()
		m.mu.Unlock()
		for i := 0; i < driveCount; i++ {
			clk.advance(adaptiveControlInterval)
			m.driveAdaptiveController(m.peerState)
		}

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

// TestAdaptiveControllerUsesAuthenticatedActiveCarrierDataLoss is the H139
// regression witness: priority PROBEs remain lossless while one ordinary outer
// DATA frame disappears after the adaptive sender selected M=0. The receiver's
// FEC recovery window expires that gap, and its next authenticated peer probe
// must feed the active-carrier loss back into the sender's adaptive controller.
func TestAdaptiveControllerUsesAuthenticatedActiveCarrierDataLoss(t *testing.T) {
	psk := testKey(t, 0x59)
	clk := newFakeClock()
	fc, ac := residualAdaptiveFECConfigs()
	sender, senderProbers := newAdaptiveProbingMultipathCfg(t, 1, psk, clk, 0, fc, ac)
	receiver, receiverProbers := newAdaptiveProbingMultipathCfg(t, 1, psk, clk, 0, fc, ac)
	if _, _, err := sender.Open(0); err != nil {
		t.Fatalf("open sender: %v", err)
	}
	t.Cleanup(func() { _ = sender.Close() })
	if _, _, err := receiver.Open(0); err != nil {
		t.Fatalf("open receiver: %v", err)
	}
	t.Cleanup(func() { _ = receiver.Close() })

	bringProberUpClean(t, senderProbers[0], psk, clk, 40)
	bringProberUpClean(t, receiverProbers[0], psk, clk, 40)
	joinNegotiatingPeers(t, sender, receiver)
	if got := senderProbers[0].Estimate().Loss; got != 0 {
		t.Fatalf("sender priority-PROBE loss = %v, want 0", got)
	}
	if got := sender.PeerSnapshots()[0].FEC.Adaptive.Parity; got != 0 {
		t.Fatalf("initial adaptive parity = %d, want M=0 with lossless priority PROBEs", got)
	}

	codec, err := frame.NewCodec(psk)
	if err != nil {
		t.Fatalf("build frame codec: %v", err)
	}
	payloads := payloadStream(fc.DataShards)
	data := make([][]byte, fc.DataShards)
	for i, payload := range payloads {
		data[i], err = codec.Encode(nil, frame.Data{
			OuterSeq: uint64(i + 1),
			PathID:   sender.paths[0].id,
			FECGroup: 1,
			FECIndex: uint8(i),
			Payload:  payload,
		})
		if err != nil {
			t.Fatalf("encode DATA %d: %v", i, err)
		}
	}

	source := leftAddrPort(t, sender.paths[0].conn)
	deliver := func(raw []byte) {
		t.Helper()
		decoded, decodeErr := codec.Decode(raw)
		if decodeErr != nil {
			t.Fatalf("decode synthetic DATA/PARITY: %v", decodeErr)
		}
		receiver.dispatchInbound(receiver.paths[0], decoded, raw, source)
	}
	for i, raw := range data {
		if i == 1 {
			continue
		}
		deliver(raw)
	}
	clk.advance(resequencerTimeout)
	for {
		if _, ok := receiver.resequencer.Load().Pop(); !ok {
			break
		}
	}
	recovery := receiver.resequencer.Load().Stats()
	if recovery.Skipped != 1 || recovery.DeadlineWakeups == 0 {
		t.Fatalf("receiver recovery outcome = %+v, want one expired DATA gap", recovery)
	}

	sender.paths[0].setRemote(leftAddrPort(t, receiver.paths[0].conn))
	receiver.paths[0].setRemote(leftAddrPort(t, sender.paths[0].conn))
	reportID := func() uint64 {
		sender.dataLoss.mu.Lock()
		defer sender.dataLoss.mu.Unlock()
		return sender.dataLoss.lastReportID
	}
	waitForReport := func(before uint64) {
		t.Helper()
		deadline := time.Now().Add(time.Second)
		for reportID() <= before {
			if time.Now().After(deadline) {
				t.Fatalf("DATA-loss report did not arrive after report %d", before)
			}
			time.Sleep(time.Millisecond)
		}
	}
	waitForReceiverContract := func(session, contractID uint64) {
		t.Helper()
		deadline := time.Now().Add(time.Second)
		for {
			snapshot := receiver.contracts.receivedSnapshot()
			if snapshot.present && !snapshot.invalid &&
				snapshot.session == session &&
				snapshot.message.ContractID == contractID &&
				clk.Now().Before(snapshot.validUntil) {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf(
					"receiver did not adopt sender recovery identity (%d,%d): %+v",
					session,
					contractID,
					snapshot,
				)
			}
			time.Sleep(time.Millisecond)
		}
	}
	beforeReport := reportID()
	receiver.emitProbes()
	waitForReport(beforeReport)
	clk.advance(adaptiveControlInterval)
	sender.driveAdaptiveController(sender.peerState)

	if got := senderProbers[0].Estimate().Loss; got != 0 {
		t.Fatalf("sender priority-PROBE loss after DATA expiry = %v, want 0", got)
	}
	adaptive := sender.PeerSnapshots()[0].FEC.Adaptive
	if adaptive.Parity <= 0 {
		t.Fatalf("adaptive parity after authenticated active-carrier DATA-loss feedback = %d, want > 0", adaptive.Parity)
	}

	nextSeq := uint64(fc.DataShards + 1)
	nextGroup := uint32(2)
	recoveryEncoder, err := fec.NewEncoder(fc, clk)
	if err != nil {
		t.Fatalf("build recovery encoder: %v", err)
	}
	if err := recoveryEncoder.SetNextGroup(fec.GroupID(nextGroup)); err != nil {
		t.Fatalf("set recovery group: %v", err)
	}
	recoveryEncoder.SetParity(adaptive.Parity)
	recoveredBefore := receiver.PeerSnapshots()[0].FEC.Recovered
	skippedBefore := receiver.resequencer.Load().Stats().Skipped
	var recoveryParity []fec.ParityShard
	for i := range fc.DataShards {
		payload := []byte("parity-protected-DATA")
		dataShard, parity, admitErr := recoveryEncoder.Admit(fecShardPayload(nextSeq, payload))
		if admitErr != nil {
			t.Fatalf("admit recovery DATA %d: %v", i, admitErr)
		}
		if i != 1 {
			raw, encodeErr := codec.Encode(nil, frame.Data{
				OuterSeq: nextSeq,
				PathID:   sender.paths[0].id,
				FECGroup: uint32(dataShard.Group),
				FECIndex: uint8(dataShard.Index),
				Payload:  payload,
			})
			if encodeErr != nil {
				t.Fatalf("encode recovery DATA %d: %v", i, encodeErr)
			}
			deliver(raw)
		}
		recoveryParity = parity
		nextSeq++
	}
	for _, parity := range recoveryParity {
		raw, encodeErr := codec.Encode(nil, frame.Parity{
			FECGroup:    uint32(parity.Group),
			ParityIndex: uint16(parity.Index),
			DataCount:   uint8(parity.DataCount),
			PathID:      sender.paths[0].id,
			Payload:     parity.Payload,
		})
		if encodeErr != nil {
			t.Fatalf("encode recovery PARITY %d: %v", parity.Index, encodeErr)
		}
		deliver(raw)
	}
	nextGroup++
	delivered := 0
	for {
		if _, ok := receiver.resequencer.Load().Pop(); !ok {
			break
		}
		delivered++
	}
	if got := receiver.PeerSnapshots()[0].FEC.Recovered; got != recoveredBefore+1 {
		t.Fatalf("recovered DATA after adaptive parity raise = %d, want %d", got, recoveredBefore+1)
	}
	if got := receiver.resequencer.Load().Stats().Skipped; got != skippedBefore {
		t.Fatalf("resequencer skipped after recoverable DATA loss = %d, want %d", got, skippedBefore)
	}
	if delivered != fc.DataShards {
		t.Fatalf("delivered recovery group DATA = %d, want %d", delivered, fc.DataShards)
	}
	beforeReport = reportID()
	receiver.emitProbes()
	waitForReport(beforeReport)
	clk.advance(adaptiveControlInterval)
	sender.driveAdaptiveController(sender.peerState)
	sustained := sender.PeerSnapshots()[0].FEC.Adaptive
	if got, want := sustained.EligibleLoss, 1.0/float64(fc.DataShards); got != want {
		t.Fatalf("active-carrier loss after FEC recovery = %v, want %v", got, want)
	}
	if sustained.Parity < adaptive.Parity {
		t.Fatalf("recovered DATA loss shed parity: got %d want at least %d", sustained.Parity, adaptive.Parity)
	}

	driveClean := func(iterations int) {
		t.Helper()
		for range iterations {
			sender.emitProbes()
			session, contractID, ok := sender.contracts.localOfferIdentity()
			if !ok {
				t.Fatal("sender has no recovery identity")
			}
			waitForReceiverContract(session, contractID)
			for i := range fc.DataShards {
				raw, encodeErr := codec.Encode(nil, frame.Data{
					OuterSeq: nextSeq,
					PathID:   sender.paths[0].id,
					FECGroup: nextGroup,
					FECIndex: uint8(i),
					Payload:  []byte("clean-loaded-DATA"),
				})
				if encodeErr != nil {
					t.Fatalf("encode clean DATA: %v", encodeErr)
				}
				deliver(raw)
				nextSeq++
			}
			nextGroup++

			beforeReport := reportID()
			receiver.emitProbes()
			waitForReport(beforeReport)
			clk.advance(adaptiveControlInterval)
			sender.driveAdaptiveController(sender.peerState)
		}
	}

	raised := sustained.Parity
	driveClean(5)
	if got := sender.PeerSnapshots()[0].FEC.Adaptive.Parity; got < raised {
		t.Fatalf("clean DATA shed parity inside dwell: got %d want at least %d", got, raised)
	}
	driveClean(35)
	if got := sender.PeerSnapshots()[0].FEC.Adaptive.Parity; got != 0 {
		t.Fatalf("clean DATA parity after dwell = %d, want 0", got)
	}
}

func TestAdaptiveControllerIgnoresSingleCarrierFeedbackWhileWeighted(t *testing.T) {
	psk := testKey(t, 0x5A)
	clk := newFakeClock()
	fc, ac := residualAdaptiveFECConfigs()
	m, probers := newAdaptiveProbingMultipathCfg(t, 2, psk, clk, 0, fc, ac)
	m.scheduler = &adaptiveDataPathsOverride{
		Scheduler: m.scheduler,
		dataPaths: []sched.DataPath{
			{Index: 0, Weight: 0.5},
			{Index: 1, Weight: 0.5},
		},
	}
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("open multipath: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	for _, prober := range probers {
		bringProberUpClean(t, prober, psk, clk, 40)
	}
	if !m.dataLoss.accept(telemetry.DataLossFeedback{
		ObservedSessionID: 1,
		ContractID:        2,
		CarrierPathID:     0,
		CarrierGeneration: 1,
		ReportID:          1,
		Lost:              1,
	}, 3, false, clk.Now()) {
		t.Fatal("accept single-carrier feedback")
	}

	loss, count := m.dataPathLossLocked(m.peerState, clk.Now())
	if loss != 0 || count != 2 {
		t.Fatalf("weighted adaptive sample = loss %v count %d, want clean two-path probe signal", loss, count)
	}
}
