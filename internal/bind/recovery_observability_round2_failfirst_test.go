package bind

import (
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/reseq"
	"github.com/7mind/wanbond/internal/telemetry"
)

func recoveryDirectionValue(t *testing.T, stats RecoveryStats, direction string) reflect.Value {
	t.Helper()
	value := reflect.ValueOf(stats)
	field := value.FieldByName(direction)
	if !field.IsValid() {
		t.Fatalf("RecoveryStats has no independent %s direction: %+v", direction, stats)
	}
	return field
}

func recoveryObservationField(t *testing.T, stats RecoveryStats, direction, name string) reflect.Value {
	t.Helper()
	value := reflect.ValueOf(stats)
	if nested := value.FieldByName(direction); nested.IsValid() {
		value = nested
	}
	field := value.FieldByName(name)
	if !field.IsValid() {
		t.Fatalf("RecoveryStats %s has no field %s: %+v", direction, name, stats)
	}
	return field
}

func TestRecoveryStatsDirectionsAreIndependent(t *testing.T) {
	t.Run("inbound fast outbound unacked", func(t *testing.T) {
		coordinator := newRecoveryContractCoordinator(0xD201, newFakeClock())
		if err := coordinator.begin(true, 80*time.Millisecond); err != nil {
			t.Fatal(err)
		}
		message := receiverContractMessage(1, 70*time.Millisecond)
		if _, ok := coordinator.acceptOffer(receiverContractSession, message, func() {}); !ok {
			t.Fatal("inbound offer rejected")
		}
		if !completeReceiverACK(
			coordinator,
			receiverContractSession,
			message,
			reseqPathKey(0, 9),
			receiverContractSource,
		) {
			t.Fatal("inbound ACK completion rejected")
		}
		if !coordinator.commitReceiverDecision(
			coordinator.receivedSnapshot().generation,
			1,
			recoveryReceiverDecision{
				offerPresent:   true,
				fastEligible:   true,
				fallbackReason: "",
				rttAge:         5 * time.Millisecond,
				headroom:       20 * time.Millisecond,
				window:         90 * time.Millisecond,
			},
		) {
			t.Fatal("receiver decision commit rejected")
		}

		stats := coordinator.stats()
		sender := recoveryDirectionValue(t, stats, "Sender")
		receiver := recoveryDirectionValue(t, stats, "Receiver")
		if sender.FieldByName("FastEligible").Bool() {
			t.Fatal("unacknowledged outbound offer reported sender fast eligibility")
		}
		if !receiver.FieldByName("FastEligible").Bool() ||
			receiver.FieldByName("Window").Interface().(time.Duration) != 90*time.Millisecond ||
			receiver.FieldByName("FallbackReason").String() != "" {
			t.Fatalf("inbound receiver decision = %+v", receiver.Interface())
		}
	})

	t.Run("outbound fast inbound absent", func(t *testing.T) {
		coordinator := newRecoveryContractCoordinator(0xD202, newFakeClock())
		if err := coordinator.begin(true, 80*time.Millisecond); err != nil {
			t.Fatal(err)
		}
		acknowledgeCurrentContract(t, coordinator, 0, 1)

		stats := coordinator.stats()
		sender := recoveryDirectionValue(t, stats, "Sender")
		receiver := recoveryDirectionValue(t, stats, "Receiver")
		if !sender.FieldByName("FastEligible").Bool() {
			t.Fatal("exact outbound ACK did not report sender fast eligibility")
		}
		if receiver.FieldByName("FastEligible").Bool() ||
			receiver.FieldByName("Window").Interface().(time.Duration) != conservativeRecoveryService ||
			receiver.FieldByName("FallbackReason").String() != "no_offer" {
			t.Fatalf("absent inbound receiver decision = %+v", receiver.Interface())
		}
	})
}

func TestReceiverObservationWaitsForAcceptedPublication(t *testing.T) {
	m, probers, _ := openRecoveryWindowPeer(t, 1)
	bringProberUpClean(t, probers[0], m.psk, m.clock.(*fakeClock), testProbeUpSucc)
	message := receiverContractMessage(1, 80*time.Millisecond)
	if _, ok := m.contracts.acceptOffer(receiverContractSession, message, func() {}); !ok {
		t.Fatal("offer rejected")
	}
	pathKey := reseqPathKey(m.paths[0].id, 9)
	if !completeReceiverACK(m.contracts, receiverContractSession, message, pathKey, receiverContractSource) {
		t.Fatal("ACK completion rejected")
	}

	paused := make(chan struct{}, 1)
	release := make(chan struct{})
	var once sync.Once
	m.beforeRecoveryPublish = func(_ *peerState, _ *reseq.Resequencer, _ uint64) {
		once.Do(func() { paused <- struct{}{} })
		<-release
	}
	done := make(chan struct{})
	go func() {
		m.refreshPeerRecoveryWindow(m.peerState)
		close(done)
	}()
	<-paused

	stats := m.contracts.stats()
	if got := recoveryObservationField(t, stats, "Receiver", "Window").Interface().(time.Duration); got != conservativeRecoveryService {
		t.Fatalf("pre-commit receiver window = %v, want installed conservative T=%v", got, conservativeRecoveryService)
	}
	if got := recoveryObservationField(t, stats, "Receiver", "FallbackReason").String(); got != "transition" {
		t.Fatalf("pre-commit receiver fallback = %q, want transition", got)
	}
	close(release)
	<-done
}

func TestReceiverDecisionUsesOnlyQualifiedRTTEvidence(t *testing.T) {
	m, probers, clock := openRecoveryWindowPeer(t, 2)
	bringProberUpAtRTT(t, probers[1], m.psk, clock, 120*time.Millisecond)
	clock.advance(2 * time.Second)
	probers[1].Tick()
	bringProberUpClean(t, probers[0], m.psk, clock, testProbeUpSucc)

	message := receiverContractMessage(1, 80*time.Millisecond)
	if _, ok := m.contracts.acceptOffer(receiverContractSession, message, func() {}); !ok {
		t.Fatal("offer rejected")
	}
	pathKey := reseqPathKey(m.paths[0].id, 9)
	if !completeReceiverACK(m.contracts, receiverContractSession, message, pathKey, receiverContractSource) {
		t.Fatal("ACK completion rejected")
	}
	m.refreshPeerRecoveryWindow(m.peerState)

	stats := m.contracts.stats()
	receiverAge := recoveryObservationField(t, stats, "Receiver", "RTTAge").Interface().(time.Duration)
	freshAge := clock.Now().Sub(probers[0].RecoveryRTT().SampledAt)
	if receiverAge != freshAge {
		t.Fatalf("receiver RTT age = %v, want qualified UP path age %v; stale DOWN path dominated", receiverAge, freshAge)
	}
	wantHeadroom := recoveryRTTHeadroom(probers[0].RecoveryRTT().RTT)
	if got := recoveryObservationField(t, stats, "Receiver", "Headroom").Interface().(time.Duration); got != wantHeadroom {
		t.Fatalf("receiver H = %v, want qualified H=%v", got, wantHeadroom)
	}
}

func TestReceiverSaturationReportsConservativeDecision(t *testing.T) {
	m, probers, clock := openRecoveryWindowPeer(t, 1)
	bringProberUpClean(t, probers[0], m.psk, clock, testProbeUpSucc)
	message := receiverContractMessage(1, 245*time.Millisecond)
	if _, ok := m.contracts.acceptOffer(receiverContractSession, message, func() {}); !ok {
		t.Fatal("offer rejected")
	}
	pathKey := reseqPathKey(m.paths[0].id, 9)
	if !completeReceiverACK(m.contracts, receiverContractSession, message, pathKey, receiverContractSource) {
		t.Fatal("ACK completion rejected")
	}
	m.refreshPeerRecoveryWindow(m.peerState)

	stats := m.contracts.stats()
	if got := recoveryObservationField(t, stats, "Receiver", "Window").Interface().(time.Duration); got != conservativeRecoveryService {
		t.Fatalf("saturated receiver W = %v, want conservative T=%v", got, conservativeRecoveryService)
	}
	if got := recoveryObservationField(t, stats, "Receiver", "FallbackReason").String(); got != "saturated" {
		t.Fatalf("saturated receiver fallback = %q, want saturated", got)
	}
}

func TestReceiverDecisionClearsAcrossInvalidationDisableAndClose(t *testing.T) {
	m, probers, clock := openRecoveryWindowPeer(t, 1)
	bringProberUpClean(t, probers[0], m.psk, clock, testProbeUpSucc)
	message := receiverContractMessage(1, 80*time.Millisecond)
	if _, ok := m.contracts.acceptOffer(receiverContractSession, message, func() {}); !ok {
		t.Fatal("offer rejected")
	}
	pathKey := reseqPathKey(m.paths[0].id, 9)
	if !completeReceiverACK(m.contracts, receiverContractSession, message, pathKey, receiverContractSource) {
		t.Fatal("ACK completion rejected")
	}
	m.refreshPeerRecoveryWindow(m.peerState)

	m.invalidatePeerRecoveryEvidence(m.peerState)
	stats := m.contracts.stats()
	if got := recoveryObservationField(t, stats, "Receiver", "Window").Interface().(time.Duration); got != conservativeRecoveryService {
		t.Fatalf("invalidated receiver W = %v, want T", got)
	}
	if got := recoveryObservationField(t, stats, "Receiver", "FallbackReason").String(); got != "transition" {
		t.Fatalf("invalidated fallback = %q, want transition", got)
	}

	m.contracts.disable()
	stats = m.contracts.stats()
	if got := recoveryObservationField(t, stats, "Receiver", "RTTAge").Interface().(time.Duration); got != 0 {
		t.Fatalf("disabled receiver retained RTT age %v", got)
	}
	if got := recoveryObservationField(t, stats, "Receiver", "Headroom").Interface().(time.Duration); got != 0 {
		t.Fatalf("disabled receiver retained H %v", got)
	}
	if got := recoveryObservationField(t, stats, "Receiver", "Window").Interface().(time.Duration); got != conservativeRecoveryService {
		t.Fatalf("disabled receiver W = %v, want T", got)
	}
	if got := recoveryObservationField(t, stats, "Receiver", "FallbackReason").String(); got != "no_offer" {
		t.Fatalf("disabled receiver fallback = %q, want no_offer", got)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	stats = m.contracts.stats()
	receiver := recoveryDirectionValue(t, stats, "Receiver")
	if receiver.FieldByName("FastEligible").Bool() ||
		!receiver.FieldByName("FreshUntil").Interface().(time.Time).IsZero() ||
		receiver.FieldByName("ServiceBound").Interface().(time.Duration) != 0 ||
		receiver.FieldByName("RTTAge").Interface().(time.Duration) != 0 ||
		receiver.FieldByName("Headroom").Interface().(time.Duration) != 0 ||
		receiver.FieldByName("Window").Interface().(time.Duration) != conservativeRecoveryService ||
		receiver.FieldByName("FallbackReason").String() != "no_offer" {
		t.Fatalf("closed receiver retained decision gauges: %+v", receiver.Interface())
	}
}

func TestCloseOpenCountsRotationWithoutSessionRestart(t *testing.T) {
	m, _, _ := openShapedRecoveryWindowPeer(t, 1)
	if got := m.contracts.stats().Sender.Rotations; got != 0 {
		t.Fatalf("initial rotations = %d, want 0", got)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.Open(0); err != nil {
		t.Fatal(err)
	}
	stats := m.contracts.stats()
	if stats.Sender.Rotations != 1 || stats.Receiver.SessionRestarts != 0 {
		t.Fatalf("Close/Open rotation/restart = %d/%d, want 1/0", stats.Sender.Rotations, stats.Receiver.SessionRestarts)
	}
}

func TestACKTelemetryIgnoresNonContractPayloadButCountsRecognizedMalformed(t *testing.T) {
	coordinator := newRecoveryContractCoordinator(0xD203, newFakeClock())
	if err := coordinator.begin(true, 80*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	before := coordinator.stats()
	if coordinator.acceptACK(0, coordinator.session, 1, nil) {
		t.Fatal("empty legacy payload accepted as ACK")
	}
	afterLegacy := coordinator.stats()
	if afterLegacy.Sender.WrongRejections != before.Sender.WrongRejections ||
		afterLegacy.Sender.FallbackReason != before.Sender.FallbackReason {
		t.Fatalf("non-contract payload changed recovery telemetry: before=%+v after=%+v", before, afterLegacy)
	}

	malformed := []byte{'W', 'B', 'R', 'C', 1}
	if _, recognized, err := telemetry.DecodeRecoveryContract(malformed); err == nil || !recognized {
		t.Fatalf("fixture is not a recognized malformed contract: recognized=%v err=%v", recognized, err)
	}
	if coordinator.acceptACK(0, coordinator.session, 2, malformed) {
		t.Fatal("recognized malformed payload accepted as ACK")
	}
	afterMalformed := coordinator.stats()
	if afterMalformed.Sender.WrongRejections != before.Sender.WrongRejections+1 {
		t.Fatalf("recognized malformed rejection delta = %d, want 1",
			afterMalformed.Sender.WrongRejections-before.Sender.WrongRejections)
	}
}
