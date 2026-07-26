package bind

import (
	"net/netip"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/adaptivefec"
	"github.com/7mind/wanbond/internal/fec"
	"github.com/7mind/wanbond/internal/frame"
	"github.com/7mind/wanbond/internal/telemetry"
)

func testDataLossFeedback(reportID, carrierGeneration uint64) telemetry.DataLossFeedback {
	return telemetry.DataLossFeedback{
		ObservedSessionID: 11,
		ContractID:        12,
		CarrierPathID:     2,
		CarrierGeneration: carrierGeneration,
		ReportID:          reportID,
		Received:          31,
		Lost:              1,
	}
}

func TestDataLossFeedbackRejectsReplayWrongIdentityAndStaleSample(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	feedback := &dataLossFeedbackCoordinator{}
	report := testDataLossFeedback(1, 1)
	if !feedback.accept(report, 21, false, now) {
		t.Fatal("first authenticated current-session report was rejected")
	}
	if feedback.accept(report, 21, false, now) {
		t.Fatal("replayed report ID was accepted")
	}
	if feedback.accept(testDataLossFeedback(2, 0), 21, false, now) {
		t.Fatal("older carrier generation was accepted")
	}
	if feedback.accept(testDataLossFeedback(2, 2), 22, false, now) {
		t.Fatal("unadopted reporter session replaced the current session")
	}

	if loss, fresh, ever := feedback.sample(2, 11, 12, now); !fresh || !ever || loss != 1.0/32 {
		t.Fatalf("matching sample = loss %v fresh %v ever %v", loss, fresh, ever)
	}
	for _, mismatch := range []struct {
		pathID     uint8
		sessionID  uint64
		contractID uint64
	}{
		{pathID: 2, sessionID: 13, contractID: 12},
		{pathID: 2, sessionID: 11, contractID: 14},
	} {
		if _, fresh, _ := feedback.sample(mismatch.pathID, mismatch.sessionID, mismatch.contractID, now); fresh {
			t.Fatalf("wrong identity %+v produced a fresh sample", mismatch)
		}
	}
	if _, fresh, ever := feedback.sample(3, 11, 12, now); fresh || !ever {
		t.Fatalf("transitioned path sample = fresh %v ever %v, want HOLD", fresh, ever)
	}
	if _, fresh, ever := feedback.sample(2, 11, 12, now); fresh || !ever {
		t.Fatalf("old path feedback reactivated after A-B-A transition: fresh %v ever %v", fresh, ever)
	}
	replacement := testDataLossFeedback(2, 2)
	if !feedback.accept(replacement, 21, false, now) {
		t.Fatal("new carrier-generation report after path transition was rejected")
	}
	if _, fresh, _ := feedback.sample(2, 11, 12, now); !fresh {
		t.Fatal("new carrier-generation report did not restore current path evidence")
	}
	if _, fresh, ever := feedback.sample(2, 11, 12, now.Add(dataLossFeedbackFreshness+time.Nanosecond)); fresh || !ever {
		t.Fatalf("stale sample = fresh %v ever %v, want false/true", fresh, ever)
	}

	if !feedback.accept(testDataLossFeedback(3, 3), 22, true, now) {
		t.Fatal("authenticated adopted reporter session was rejected")
	}
}

func TestDataLossFeedbackIsPerPeer(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	first := &dataLossFeedbackCoordinator{}
	second := &dataLossFeedbackCoordinator{}
	if !first.accept(testDataLossFeedback(1, 1), 21, false, now) {
		t.Fatal("first peer report rejected")
	}
	if _, fresh, ever := second.sample(2, 11, 12, now); fresh || ever {
		t.Fatalf("first peer feedback leaked into second peer: fresh %v ever %v", fresh, ever)
	}
}

func TestDataLossFeedbackConservesNativeRecoveredAndFinalOutcomes(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	feedback := &dataLossFeedbackCoordinator{}
	carrier := dataLossCarrier{
		pathID:             2,
		pathKey:            0x102,
		source:             netip.MustParseAddrPort("192.0.2.1:51820"),
		topologyGeneration: 7,
	}
	feedback.recordNative(1, carrier)
	feedback.recordRecovered(2, carrier)
	feedback.recordLost(3, 1)

	report := feedback.buildReport(receivedRecoverySnapshot{
		present:    true,
		session:    11,
		validUntil: now.Add(time.Second),
		generation: carrier.topologyGeneration,
		message: telemetry.RecoveryContractMessage{
			ContractID: 12,
		},
	}, now)
	if report == nil {
		t.Fatal("non-empty DATA interval produced no report")
	}
	if report.Received != 1 || report.Lost != 2 {
		t.Fatalf("DATA outcome conservation = received %d lost %d, want 1/2", report.Received, report.Lost)
	}
	if got, want := report.Loss(), 2.0/3; got != want {
		t.Fatalf("DATA loss = %v, want %v", got, want)
	}
}

// Regression: T324 review round 2 — native duplicates, stale DATA, and late
// arrivals must not enter the receiver outcome interval as new observations.
func TestDataLossFeedbackCountsOnlyUniqueAcceptedNativeOutcomes(t *testing.T) {
	clock := newFakeClock()
	m, _, _ := newProbingMultipath(t, loopbackPaths(1), testKey(t, 0x5B), clock)
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("open multipath: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	m.resequencer.Load().SetMultiPathExpected(true)

	source := netip.MustParseAddrPort("192.0.2.1:51820")
	deliver := func(seq uint64) {
		m.dispatchInbound(m.paths[0], frame.Data{
			OuterSeq: seq,
			PathID:   m.paths[0].id,
			Payload:  []byte{byte(seq)},
		}, nil, source)
	}
	deliver(1)
	deliver(1)
	deliver(3)
	clock.advance(resequencerTimeout)
	for {
		if _, ok := m.resequencer.Load().Pop(); !ok {
			break
		}
	}
	deliver(2) // late: sequence 2 already finalized as lost
	deliver(1) // stale: sequence 1 already released

	m.dataLoss.mu.Lock()
	carrier := m.dataLoss.carrier
	topologyGeneration := m.dataLoss.carrier.topologyGeneration
	m.dataLoss.mu.Unlock()
	if m.resequencer.Load().ObserveRecovered(2, []byte{2}, source) {
		m.dataLoss.recordRecovered(2, carrier)
		t.Fatal("recovered DATA was admitted after the same sequence finalized as lost")
	}
	report := m.dataLoss.buildReport(receivedRecoverySnapshot{
		present:    true,
		session:    11,
		validUntil: clock.Now().Add(time.Second),
		generation: topologyGeneration,
		message: telemetry.RecoveryContractMessage{
			ContractID: 12,
		},
	}, clock.Now())
	if report == nil {
		t.Fatal("DATA outcome interval produced no report")
	}
	if report.Received != 2 || report.Lost != 1 {
		t.Fatalf(
			"DATA outcome conservation = received %d lost %d, want unique native/final 2/1",
			report.Received,
			report.Lost,
		)
	}
}

// Regression: T324 field round 4 — outcomes from the previous carrier epoch
// must not enter the first feedback interval for a newly accepted carrier.
func TestDataLossFeedbackCarrierTransitionDoesNotAttributePriorEpochOutcomes(t *testing.T) {
	for _, test := range []struct {
		name       string
		resolveGap func(*testing.T, *Multipath, netip.AddrPort, dataLossCarrier)
	}{
		{
			name: "final gap",
			resolveGap: func(_ *testing.T, m *Multipath, source netip.AddrPort, _ dataLossCarrier) {
				m.clock.(*fakeClock).advance(resequencerTimeout)
				m.dispatchInbound(m.paths[0], frame.Data{
					OuterSeq: 4,
					PathID:   1,
					Payload:  []byte{4},
				}, nil, source)
			},
		},
		{
			name: "recovered gap",
			resolveGap: func(t *testing.T, m *Multipath, source netip.AddrPort, carrier dataLossCarrier) {
				receiver := &fecReceiver{}
				m.observeRecovered(
					receiver,
					m.resequencer.Load(),
					[]fec.Recovered{{Payload: fecShardPayload(2, []byte{2})}},
					source,
					m.dataLoss,
					carrier,
				)
				if got := receiver.deliveredRecovered.Load(); got != 1 {
					t.Fatalf("transition recovery delivered = %d, want 1", got)
				}
				m.dispatchInbound(m.paths[0], frame.Data{
					OuterSeq: 4,
					PathID:   1,
					Payload:  []byte{4},
				}, nil, source)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			clock := newFakeClock()
			m, _, _ := newProbingMultipath(t, loopbackPaths(1), testKey(t, 0x60), clock)
			if _, _, err := m.Open(0); err != nil {
				t.Fatalf("open multipath: %v", err)
			}
			t.Cleanup(func() { _ = m.Close() })
			m.resequencer.Load().SetMultiPathExpected(true)

			sourceA := netip.MustParseAddrPort("192.0.2.1:51820")
			sourceB := netip.MustParseAddrPort("198.51.100.1:51820")
			deliver := func(seq uint64, pathID uint8, source netip.AddrPort) {
				t.Helper()
				m.dispatchInbound(m.paths[0], frame.Data{
					OuterSeq: seq,
					PathID:   pathID,
					Payload:  []byte{byte(seq)},
				}, nil, source)
			}
			deliver(1, 0, sourceA)
			deliver(3, 0, sourceA)

			generation := m.contracts.receivedSnapshot().generation
			carrierB := dataLossCarrier{
				pathID:             1,
				pathKey:            reseqPathKey(m.paths[0].id, 1),
				source:             sourceB,
				topologyGeneration: generation,
			}
			test.resolveGap(t, m, sourceB, carrierB)

			contract := receivedRecoverySnapshot{
				present:    true,
				session:    11,
				validUntil: clock.Now().Add(time.Second),
				generation: generation,
				message: telemetry.RecoveryContractMessage{
					ContractID: 12,
				},
			}
			transitionReport := m.dataLoss.buildReport(contract, clock.Now())
			if transitionReport == nil {
				t.Fatal("first B-native outcome produced no DATA feedback report")
			}
			if transitionReport.CarrierPathID != 1 ||
				transitionReport.Received != 1 ||
				transitionReport.Lost != 0 {
				t.Errorf(
					"first B interval = path %d received %d lost %d, want clean B-only 1/0",
					transitionReport.CarrierPathID,
					transitionReport.Received,
					transitionReport.Lost,
				)
			}

			_, adaptiveConfig := residualAdaptiveFECConfigs()
			controller, err := adaptivefec.NewController(adaptiveConfig, clock)
			if err != nil {
				t.Fatalf("build adaptive controller: %v", err)
			}
			if got := controller.Observe(transitionReport.Loss()); got != 0 {
				t.Errorf("parity after transition-only outcomes = %d, want 0", got)
			}

			deliver(6, 1, sourceB)
			clock.advance(resequencerTimeout)
			for {
				if _, ok := m.resequencer.Load().Pop(); !ok {
					break
				}
			}
			stableReport := m.dataLoss.buildReport(contract, clock.Now())
			if stableReport == nil {
				t.Fatal("stable B-native loss produced no DATA feedback report")
			}
			if stableReport.Received != 1 || stableReport.Lost != 1 {
				t.Fatalf(
					"stable B interval = received %d lost %d, want 1/1",
					stableReport.Received,
					stableReport.Lost,
				)
			}
			clock.advance(adaptiveControlInterval)
			if got := controller.Observe(stableReport.Loss()); got <= 0 {
				t.Fatalf("parity after stable B-native loss = %d, want > 0", got)
			}
		})
	}
}

func seedFeedbackOnlyProbe(t *testing.T, m *Multipath, seq uint64) {
	t.Helper()
	contract := m.contracts.receivedSnapshot()
	if !contract.present || contract.invalid || !contract.acked {
		t.Fatal("precondition: peer has no fresh received recovery contract")
	}
	source, ok := m.paths[0].getRemote()
	if !ok {
		t.Fatal("precondition: peer path has no remote")
	}
	m.dataLoss.recordNative(seq, dataLossCarrier{
		pathID:             m.paths[0].id,
		pathKey:            reseqPathKey(m.paths[0].id, m.paths[0].id),
		source:             source,
		topologyGeneration: contract.generation,
	})
}

func waitForAcceptedFeedback(t *testing.T, m *Multipath) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		m.dataLoss.mu.Lock()
		accepted := m.dataLoss.everAccepted
		m.dataLoss.mu.Unlock()
		if accepted {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for feedback-only probe acceptance")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestFeedbackOnlyProbePreservesReceivedRecoveryEvidence(t *testing.T) {
	psk := testKey(t, 0x5D)
	sender := openNegotiatingPeer(t, psk)
	receiver := openNegotiatingPeer(t, psk)
	joinNegotiatingPeers(t, sender, receiver)

	before := receiver.contracts.receivedSnapshot()
	if !before.acked {
		t.Fatal("precondition: receiver has no acknowledged recovery venue")
	}
	seedFeedbackOnlyProbe(t, sender, 1)
	sender.emitProbes()
	waitForAcceptedFeedback(t, receiver)

	after := receiver.contracts.receivedSnapshot()
	if !after.acked || after.generation != before.generation ||
		after.evidenceRevision != before.evidenceRevision ||
		len(after.venues) != len(before.venues) {
		t.Fatalf(
			"feedback-only non-echo changed received recovery evidence: before=%+v after=%+v",
			before,
			after,
		)
	}
}

func TestFeedbackOnlyEchoPreservesLocalRecoveryEvidence(t *testing.T) {
	psk := testKey(t, 0x5E)
	sender := openNegotiatingPeer(t, psk)
	receiver := openNegotiatingPeer(t, psk)
	joinNegotiatingPeers(t, sender, receiver)

	if !sender.paths[0].recoveryContract().Enabled {
		t.Fatal("precondition: sender has no fresh local recovery ACK")
	}
	beforeSamples := sender.paths[0].prober.Estimate().LossSamples
	seedFeedbackOnlyProbe(t, sender, 1)
	sender.emitProbes()
	waitForAcceptedFeedback(t, receiver)

	deadline := time.Now().Add(time.Second)
	for sender.paths[0].prober.Estimate().LossSamples < beforeSamples+2 &&
		time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := sender.paths[0].prober.Estimate().LossSamples; got < beforeSamples+2 {
		t.Fatalf("feedback-only echo was not observed: loss samples %d, want at least %d", got, beforeSamples+2)
	}
	if !sender.paths[0].recoveryContract().Enabled {
		t.Fatal("feedback-only echo revoked fresh local recovery ACK evidence")
	}
}
