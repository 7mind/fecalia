package telemetry

import (
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/frame"
)

func TestRecoveryContractV1ExactVectors(t *testing.T) {
	offer := RecoveryContractMessage{
		Type:         RecoveryContractOffer,
		Enabled:      true,
		ServiceBound: 125 * time.Millisecond,
		Lifetime:     1200 * time.Millisecond,
		ContractID:   0x0102030405060708,
	}
	payload, err := EncodeRecoveryContract(offer)
	if err != nil {
		t.Fatal(err)
	}
	const wantHex = "574252430101010000000007735940000004b00102030405060708"
	if got := hex.EncodeToString(payload); got != wantHex {
		t.Fatalf("OFFER bytes = %s, want %s", got, wantHex)
	}
	got, recognized, err := DecodeRecoveryContract(payload)
	if err != nil || !recognized || got != offer {
		t.Fatalf("DecodeRecoveryContract(OFFER) = %+v,%v,%v, want %+v,true,nil", got, recognized, err, offer)
	}

	ack := offer
	ack.Type = RecoveryContractACK
	payload, err = EncodeRecoveryContract(ack)
	if err != nil {
		t.Fatal(err)
	}
	const wantACKHex = "574252430102010000000007735940000004b00102030405060708"
	if got := hex.EncodeToString(payload); got != wantACKHex {
		t.Fatalf("ACK bytes = %s, want %s", got, wantACKHex)
	}
}

func TestRecoveryContractV1MalformedUnknownDisabledAndPadded(t *testing.T) {
	disabled := RecoveryContractMessage{
		Type:       RecoveryContractOffer,
		Lifetime:   RecoveryContractLifetime,
		ContractID: 9,
	}
	payload, err := EncodeRecoveryContract(disabled)
	if err != nil {
		t.Fatal(err)
	}
	got, recognized, err := DecodeRecoveryContract(payload)
	if err != nil || !recognized || got != disabled {
		t.Fatalf("disabled round trip = %+v,%v,%v", got, recognized, err)
	}

	unknown := append([]byte(nil), payload...)
	unknown[4]++
	if _, recognized, err := DecodeRecoveryContract(unknown); recognized || err != nil {
		t.Fatalf("unknown version = recognized %v err %v, want ignored", recognized, err)
	}

	for name, malformed := range map[string][]byte{
		"truncated": payload[:len(payload)-1],
		"bad type":  append([]byte(nil), payload...),
		"bad flag":  append([]byte(nil), payload...),
	} {
		switch name {
		case "bad type":
			malformed[5] = 0xff
		case "bad flag":
			malformed[6] = 0x80
		}
		if _, recognized, err := DecodeRecoveryContract(malformed); !recognized || !errors.Is(err, ErrRecoveryContractMalformed) {
			t.Fatalf("%s = recognized %v err %v, want recognized malformed", name, recognized, err)
		}
	}

	psk := testPSK(t, 0x51)
	padded := frame.Probe{Padded: true, PadLen: 3, Payload: payload}
	if _, err := frame.Encode(psk, padded); !errors.Is(err, frame.ErrMalformed) {
		t.Fatalf("padded contract probe encode error = %v, want frame.ErrMalformed", err)
	}
}
