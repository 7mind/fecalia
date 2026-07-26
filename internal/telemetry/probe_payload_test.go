package telemetry

import (
	"bytes"
	"testing"
	"time"
)

func TestProbePayloadPreservesLegacyRecoveryOnlyEncoding(t *testing.T) {
	recovery, err := EncodeRecoveryContract(RecoveryContractMessage{
		Type:         RecoveryContractOffer,
		Enabled:      true,
		ServiceBound: 10 * time.Millisecond,
		Lifetime:     RecoveryContractLifetime,
		ContractID:   7,
	})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := EncodeProbePayload(recovery, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(payload, recovery) {
		t.Fatalf("recovery-only payload changed: got %x want %x", payload, recovery)
	}
	if _, _, recognized, err := DecodeProbePayload(payload); err != nil || recognized {
		t.Fatalf("legacy payload envelope decode = recognized %v err %v, want unrecognized", recognized, err)
	}
}

func TestProbePayloadRoundTripsRecoveryAndDataLoss(t *testing.T) {
	recovery := []byte("recovery-record")
	want := &DataLossFeedback{
		ObservedSessionID: 0x101,
		ContractID:        0x202,
		CarrierPathID:     3,
		CarrierGeneration: 4,
		ReportID:          5,
		Received:          31,
		Lost:              1,
	}
	payload, err := EncodeProbePayload(recovery, want)
	if err != nil {
		t.Fatal(err)
	}
	gotRecovery, got, recognized, err := DecodeProbePayload(payload)
	if err != nil || !recognized {
		t.Fatalf("decode = recognized %v err %v", recognized, err)
	}
	if !bytes.Equal(gotRecovery, recovery) || got == nil || *got != *want {
		t.Fatalf("round trip = recovery %x feedback %+v, want %x %+v", gotRecovery, got, recovery, want)
	}
	if got.Loss() != 1.0/32 {
		t.Fatalf("loss = %v, want %v", got.Loss(), 1.0/32)
	}
}

func TestProbePayloadRejectsMalformedDataLossReport(t *testing.T) {
	_, err := EncodeProbePayload(nil, &DataLossFeedback{
		ObservedSessionID: 1,
		ContractID:        2,
		CarrierGeneration: 3,
		ReportID:          4,
	})
	if err == nil {
		t.Fatal("empty DATA-loss interval encoded successfully")
	}

	payload, err := EncodeProbePayload(nil, &DataLossFeedback{
		ObservedSessionID: 1,
		ContractID:        2,
		CarrierGeneration: 3,
		ReportID:          4,
		Received:          1,
	})
	if err != nil {
		t.Fatal(err)
	}
	payload = payload[:len(payload)-1]
	if _, _, recognized, err := DecodeProbePayload(payload); err == nil || !recognized {
		t.Fatalf("truncated decode = recognized %v err %v, want recognized malformed", recognized, err)
	}
}
