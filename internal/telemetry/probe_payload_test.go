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

// Regression: T324 review round 2 — c03555d peers parse WBRC directly from
// Probe.Payload. A result peer must preserve authenticated liveness and negotiate
// recovery contracts in both directions with that base behavior.
func TestProbePayloadMixedVersionRecoveryNegotiatesBothDirections(t *testing.T) {
	psk := testPSK(t, 0x5C)
	clock := newFakeClock()
	resultProber := newTestProber(t, psk, clock)
	baseProber := newTestProber(t, psk, clock)
	baseReflector := NewReflector(psk, newTestRand())
	resultReflector := NewReflector(psk, newTestRand())

	bringUp := func(prober *Prober, reflector *Reflector) {
		t.Helper()
		for range proberCfg().Liveness.UpAfterSuccesses {
			raw, err := prober.SendProbe()
			if err != nil {
				t.Fatalf("send bootstrap probe: %v", err)
			}
			echo, _, err := reflector.Reflect(raw)
			if err != nil {
				t.Fatalf("reflect bootstrap probe: %v", err)
			}
			if err := prober.HandleEcho(echo); err != nil {
				t.Fatalf("handle bootstrap echo: %v", err)
			}
		}
		if prober.State() != StateUp {
			t.Fatalf("probe liveness = %s, want up", prober.State())
		}
	}
	bringUp(resultProber, baseReflector)
	bringUp(baseProber, resultReflector)

	offer := RecoveryContractMessage{
		Type:         RecoveryContractOffer,
		Enabled:      true,
		ServiceBound: 10 * time.Millisecond,
		Lifetime:     RecoveryContractLifetime,
		ContractID:   7,
	}
	offerPayload, err := EncodeRecoveryContract(offer)
	if err != nil {
		t.Fatal(err)
	}
	feedback := &DataLossFeedback{
		ObservedSessionID: 11,
		ContractID:        12,
		CarrierPathID:     1,
		CarrierGeneration: 1,
		ReportID:          1,
		Received:          31,
		Lost:              1,
	}
	resultOfferPayload, err := EncodeProbePayload(offerPayload, feedback)
	if err != nil {
		t.Fatal(err)
	}

	baseReflectRecovery := func(raw []byte) []byte {
		t.Helper()
		accepted, err := baseReflector.AcceptProbe(raw)
		if err != nil {
			t.Fatalf("base accept result probe: %v", err)
		}
		echoPayload := accepted.Probe.Payload
		message, recognized, decodeErr := DecodeRecoveryContract(accepted.Probe.Payload)
		if decodeErr == nil && recognized && message.Type == RecoveryContractOffer {
			message.Type = RecoveryContractACK
			echoPayload, err = EncodeRecoveryContract(message)
			if err != nil {
				t.Fatalf("base encode ACK: %v", err)
			}
		}
		echo, err := baseReflector.EncodeAcceptedProbe(accepted, echoPayload)
		if err != nil {
			t.Fatalf("base encode echo: %v", err)
		}
		return echo
	}
	resultReflectRecovery := func(raw []byte) []byte {
		t.Helper()
		accepted, err := resultReflector.AcceptProbe(raw)
		if err != nil {
			t.Fatalf("result accept base probe: %v", err)
		}
		recoveryPayload := accepted.Probe.Payload
		if recovery, _, recognized, payloadErr := DecodeProbePayload(accepted.Probe.Payload); recognized {
			if payloadErr != nil {
				recoveryPayload = nil
			} else {
				recoveryPayload = recovery
			}
		}
		echoPayload := accepted.Probe.Payload
		message, recognized, decodeErr := DecodeRecoveryContract(recoveryPayload)
		if decodeErr == nil && recognized && message.Type == RecoveryContractOffer {
			message.Type = RecoveryContractACK
			echoPayload, err = EncodeRecoveryContract(message)
			if err != nil {
				t.Fatalf("result encode ACK: %v", err)
			}
		}
		echo, err := resultReflector.EncodeAcceptedProbe(accepted, echoPayload)
		if err != nil {
			t.Fatalf("result encode echo: %v", err)
		}
		return echo
	}
	exchange := func(prober *Prober, payload []byte, reflect func([]byte) []byte) []byte {
		t.Helper()
		raw, _, err := prober.SendProbePayload(payload)
		if err != nil {
			t.Fatalf("send recovery OFFER: %v", err)
		}
		echo := reflect(raw)
		fresh, err := prober.HandleEchoProbe(echo)
		if err != nil {
			t.Fatalf("handle recovery echo: %v", err)
		}
		return fresh.Payload
	}

	resultEchoPayload := exchange(resultProber, resultOfferPayload, baseReflectRecovery)
	baseEchoPayload := exchange(baseProber, offerPayload, resultReflectRecovery)
	resultRecovery, _, recognized, err := DecodeProbePayload(resultEchoPayload)
	if err != nil || !recognized {
		t.Fatalf("result echo envelope = recognized %v err %v", recognized, err)
	}
	resultACK, _, err := DecodeRecoveryContract(resultRecovery)
	if err != nil {
		t.Fatalf("result decode base ACK: %v", err)
	}
	baseACK, _, err := DecodeRecoveryContract(baseEchoPayload)
	if err != nil {
		t.Fatalf("base decode result ACK: %v", err)
	}
	if resultProber.State() != StateUp || baseProber.State() != StateUp ||
		resultACK.Type != RecoveryContractACK || baseACK.Type != RecoveryContractACK {
		t.Fatalf(
			"mixed-version exchange = liveness result:%s base:%s contracts result->base:%d base->result:%d, want up/up ACK/ACK",
			resultProber.State(),
			baseProber.State(),
			resultACK.Type,
			baseACK.Type,
		)
	}
}
