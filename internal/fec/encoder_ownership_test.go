package fec

import (
	"errors"
	"testing"

	"github.com/klauspost/reedsolomon"
)

func TestAdmitOwnedReturnsTransferredPayloadsOnSizeDecision(t *testing.T) {
	cfg := Config{DataShards: 2, ParityShards: 1, Deadline: testDeadline}
	enc, err := NewEncoder(cfg, newFakeClock())
	if err != nil {
		t.Fatal(err)
	}
	first := []byte("first")
	second := []byte("second")

	firstAdmission, decision, err := enc.AdmitOwned(first)
	if err != nil || decision != nil {
		t.Fatalf("first AdmitOwned = decision %v, err %v; want open group", decision, err)
	}
	if firstAdmission.Payload != nil {
		t.Fatalf("first admission Payload = %q, want nil while Encoder owns the slice", firstAdmission.Payload)
	}
	if len(enc.pending) != 1 || &enc.pending[0][0] != &first[0] {
		t.Fatal("Encoder did not retain the exact transferred payload while the group was open")
	}
	secondAdmission, decision, err := enc.AdmitOwned(second)
	if err != nil {
		t.Fatal(err)
	}
	if secondAdmission.Payload != nil {
		t.Fatalf("closing admission Payload = %q, want nil before GroupDecision returns ownership", secondAdmission.Payload)
	}
	if decision == nil || len(decision.Data) != 2 || len(decision.Parity) != 1 {
		t.Fatalf("decision = %#v, want two DATA and one PARITY", decision)
	}
	if &decision.Data[0].Payload[0] != &first[0] || &decision.Data[1].Payload[0] != &second[0] {
		t.Fatal("GroupDecision did not return the exact transferred payload slices")
	}
	if _, open := enc.NextDeadline(); open {
		t.Fatal("encoder retained an open group after size decision")
	}
	if enc.pending != nil {
		t.Fatalf("encoder pending = %#v after size decision, want nil", enc.pending)
	}
}

func TestAdmitOwnedReturnsTransferredPayloadsOnDeadlineAndParityZero(t *testing.T) {
	t.Run("deadline", func(t *testing.T) {
		clock := newFakeClock()
		cfg := Config{DataShards: 3, ParityShards: 1, Deadline: testDeadline}
		enc, err := NewEncoder(cfg, clock)
		if err != nil {
			t.Fatal(err)
		}
		payload := []byte("deadline")
		admission, decision, err := enc.AdmitOwned(payload)
		if err != nil || decision != nil {
			t.Fatalf("AdmitOwned = decision %v, err %v; want open group", decision, err)
		}
		if admission.Payload != nil {
			t.Fatalf("deadline admission Payload = %q, want nil", admission.Payload)
		}
		clock.advance(testDeadline)
		decision, err = enc.TickOwned()
		if err != nil {
			t.Fatal(err)
		}
		if decision == nil || len(decision.Data) != 1 || &decision.Data[0].Payload[0] != &payload[0] {
			t.Fatalf("deadline decision = %#v, want exact transferred payload", decision)
		}
	})

	t.Run("flush", func(t *testing.T) {
		cfg := Config{DataShards: 3, ParityShards: 1, Deadline: testDeadline}
		enc, err := NewEncoder(cfg, newFakeClock())
		if err != nil {
			t.Fatal(err)
		}
		payload := []byte("flush")
		admission, decision, err := enc.AdmitOwned(payload)
		if err != nil || decision != nil {
			t.Fatalf("AdmitOwned = decision %v, err %v; want open group", decision, err)
		}
		if admission.Payload != nil {
			t.Fatalf("flush admission Payload = %q, want nil", admission.Payload)
		}
		decision, err = enc.FlushOwned()
		if err != nil {
			t.Fatal(err)
		}
		if decision == nil || len(decision.Data) != 1 || &decision.Data[0].Payload[0] != &payload[0] {
			t.Fatalf("flush decision = %#v, want exact transferred payload", decision)
		}
	})

	t.Run("parity-zero", func(t *testing.T) {
		cfg := Config{DataShards: 1, ParityShards: 1, Deadline: testDeadline}
		enc, err := NewEncoder(cfg, newFakeClock())
		if err != nil {
			t.Fatal(err)
		}
		enc.SetParity(0)
		payload := []byte("no-parity")
		admission, decision, err := enc.AdmitOwned(payload)
		if err != nil {
			t.Fatal(err)
		}
		if admission.Payload != nil {
			t.Fatalf("parity-zero admission Payload = %q, want nil", admission.Payload)
		}
		if decision == nil || len(decision.Data) != 1 || len(decision.Parity) != 0 {
			t.Fatalf("parity-zero decision = %#v, want one DATA and no PARITY", decision)
		}
		if &decision.Data[0].Payload[0] != &payload[0] {
			t.Fatal("parity-zero decision did not return transferred payload")
		}
		if _, open := enc.NextDeadline(); open {
			t.Fatal("encoder retained parity-zero group")
		}
		if enc.pending != nil {
			t.Fatalf("encoder pending = %#v after parity-zero decision, want nil", enc.pending)
		}
	})
}

func TestAdmitOwnedReturnsPayloadsAndClearsGroupOnCodingError(t *testing.T) {
	cfg := Config{DataShards: 1, ParityShards: 1, Deadline: testDeadline}
	enc, err := NewEncoder(cfg, newFakeClock())
	if err != nil {
		t.Fatal(err)
	}
	codingErr := errors.New("injected coding failure")
	enc.encodeShards = func(reedsolomon.Encoder, [][]byte) error {
		return codingErr
	}
	payload := []byte("terminal")

	admission, decision, err := enc.AdmitOwned(payload)
	if !errors.Is(err, codingErr) {
		t.Fatalf("AdmitOwned error = %v, want %v", err, codingErr)
	}
	if admission.Payload != nil {
		t.Fatalf("error admission Payload = %q, want nil", admission.Payload)
	}
	if decision == nil || len(decision.Data) != 1 || &decision.Data[0].Payload[0] != &payload[0] {
		t.Fatalf("error decision = %#v, want exact transferred payload", decision)
	}
	if _, open := enc.NextDeadline(); open {
		t.Fatal("encoder retained failed group")
	}
	if enc.pending != nil {
		t.Fatalf("encoder pending = %#v after coding error, want nil", enc.pending)
	}

	enc.encodeShards = func(codec reedsolomon.Encoder, shards [][]byte) error {
		return codec.Encode(shards)
	}
	_, next, err := enc.AdmitOwned([]byte("next"))
	if err != nil {
		t.Fatalf("next group: %v", err)
	}
	if next == nil || next.Group == decision.Group {
		t.Fatalf("next decision group = %v, failed group = %v", next, decision.Group)
	}
}
