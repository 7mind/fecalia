package telemetry

import (
	"errors"
	"testing"

	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/frame"
)

// ctrlRekey is a representative SECURITY-RELEVANT control type (the "rekey now"
// example used across the frame tests): replaying it must not re-drive a rekey, so
// it is the type the D4 anti-replay guard protects.
const ctrlRekey uint8 = 4

// decodeControl authenticates raw under psk (the codec MAC check) and returns the
// decoded Control, matching the receive path: a frame reaches the guard only AFTER
// the codec has verified its HMAC.
func decodeControl(t *testing.T, psk config.Key, raw []byte) frame.Control {
	t.Helper()
	f, err := frame.Decode(psk, raw)
	if err != nil {
		t.Fatalf("decode control: %v", err)
	}
	c, ok := f.(frame.Control)
	if !ok {
		t.Fatalf("frame is %T, want frame.Control", f)
	}
	return c
}

// TestControlCodecStatelessAcceptsReplay characterizes the D4 ROOT CAUSE at the
// codec layer: frame.Decode verifies the CONTROL HMAC but keeps no per-peer state,
// so the SAME captured control frame decodes successfully TWICE. This is correct for
// a stateless codec — it is exactly why replay defense must live in the stateful
// ControlGuard, not in Decode.
func TestControlCodecStatelessAcceptsReplay(t *testing.T) {
	psk := testPSK(t, 0x5A)
	raw, err := frame.Encode(psk, frame.Control{ControlType: ctrlRekey, Seq: 1, Payload: []byte("rekey")})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	for i := 0; i < 2; i++ {
		if _, err := frame.Decode(psk, raw); err != nil {
			t.Fatalf("stateless decode %d rejected a valid frame: %v", i, err)
		}
	}
}

// TestControlReplayRejected is the D4 CONTROL-frame acceptance (T44): a captured,
// validly-authenticated security-relevant control frame replayed with a
// non-advancing Seq is REJECTED (ErrControlReplay), a fresh advancing Seq is
// ACCEPTED, and a stale Seq below the high-water is rejected — the control-layer
// analogue of TestProbeReplayRejected on the PROBE path.
func TestControlReplayRejected(t *testing.T) {
	psk := testPSK(t, 0x5A)
	g := NewControlGuard(ctrlRekey)

	// A captured valid-MAC rekey control at Seq 1.
	raw1, err := frame.Encode(psk, frame.Control{ControlType: ctrlRekey, Seq: 1, Payload: []byte("rekey")})
	if err != nil {
		t.Fatalf("encode control 1: %v", err)
	}
	// First delivery advances the per-type high-water and is accepted.
	if err := g.Admit(decodeControl(t, psk, raw1)); err != nil {
		t.Fatalf("fresh control rejected: %v", err)
	}
	// Replay of the SAME frame (identical Seq, still a valid MAC): rejected — the
	// exact D4 vulnerability the stateless codec cannot catch.
	if err := g.Admit(decodeControl(t, psk, raw1)); !errors.Is(err, ErrControlReplay) {
		t.Fatalf("replayed control accepted: got %v, want ErrControlReplay", err)
	}

	// A fresh control ADVANCING the Seq is accepted (legitimate traffic passes).
	raw2, err := frame.Encode(psk, frame.Control{ControlType: ctrlRekey, Seq: 2, Payload: []byte("rekey")})
	if err != nil {
		t.Fatalf("encode control 2: %v", err)
	}
	if err := g.Admit(decodeControl(t, psk, raw2)); err != nil {
		t.Fatalf("advancing control rejected: %v", err)
	}
	// A stale Seq (<= high-water) is rejected even with a valid MAC.
	if err := g.Admit(decodeControl(t, psk, raw1)); !errors.Is(err, ErrControlReplay) {
		t.Fatalf("stale control accepted: got %v, want ErrControlReplay", err)
	}
}

// TestControlGuardPerTypeIndependent asserts the high-water is keyed PER control
// type: two security-relevant types have independent Seq spaces, so a fresh Seq on
// one is not rejected as a replay of the other, while a genuine per-type replay is.
func TestControlGuardPerTypeIndependent(t *testing.T) {
	psk := testPSK(t, 0x5A)
	const ctrlOther uint8 = 7
	g := NewControlGuard(ctrlRekey, ctrlOther)

	admit := func(ctype uint8, seq uint64) error {
		raw, err := frame.Encode(psk, frame.Control{ControlType: ctype, Seq: seq})
		if err != nil {
			t.Fatalf("encode type=%d seq=%d: %v", ctype, seq, err)
		}
		return g.Admit(decodeControl(t, psk, raw))
	}

	if err := admit(ctrlRekey, 5); err != nil {
		t.Fatalf("rekey seq 5: %v", err)
	}
	// A different type at its own (lower) seq is NOT seen as a rekey replay.
	if err := admit(ctrlOther, 1); err != nil {
		t.Fatalf("other-type seq 1 rejected as a cross-type replay: %v", err)
	}
	// A genuine per-type replay is still rejected.
	if err := admit(ctrlRekey, 5); !errors.Is(err, ErrControlReplay) {
		t.Fatalf("rekey seq 5 replay: got %v, want ErrControlReplay", err)
	}
}

// TestControlNonSecurityTypeNotGuarded asserts a control type NOT registered as
// security-relevant is admitted repeatedly (no monotonic-Seq contract): D4 concerns
// replayed SECURITY-relevant control (a rekey), and sequence-gating an unregistered
// type would wrongly drop legitimate repeats.
func TestControlNonSecurityTypeNotGuarded(t *testing.T) {
	psk := testPSK(t, 0x5A)
	g := NewControlGuard(ctrlRekey) // type 9 is NOT registered
	for i := 0; i < 3; i++ {
		raw, err := frame.Encode(psk, frame.Control{ControlType: 9, Seq: 0})
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		if err := g.Admit(decodeControl(t, psk, raw)); err != nil {
			t.Fatalf("non-security control iteration %d rejected: %v", i, err)
		}
	}
}
