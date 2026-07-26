package bind

import (
	"net/netip"
	"testing"
	"time"

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
	feedback.observeData(
		carrier.pathID,
		carrier.pathKey,
		carrier.source,
		carrier.topologyGeneration,
	)
	feedback.recordRecovered(2, carrier)
	feedback.recordLost(3, 1)
	feedback.recordRecovered(3, carrier)

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
