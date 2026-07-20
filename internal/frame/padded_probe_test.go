package frame

import (
	"errors"
	"reflect"
	"testing"
)

// historicalUnpaddedProbeOnWire is the on-wire size of an empty-payload probe under
// the PRE-extension encoding: clear nonce || fixed probe body || tag, with no
// PadLen field or padding. A padded-probe extension that widened this would break
// the "unpadded probes byte-identical on wire" invariant.
const historicalUnpaddedProbeOnWire = nonceLen + probeFixedBody + tagLen // 24 + 35 + 16 = 75

// TestPaddedProbeRoundTrip encodes a padded probe at boundary sizes, asserts the
// datagram hits the requested on-wire size exactly, and that decode recovers the
// pad length and every header field. This is the T202 reproduce-first codec test:
// it fails before the padding encoding exists (an unpadded encode is 75 bytes and
// decode reports Padded=false).
func TestPaddedProbeRoundTrip(t *testing.T) {
	psk := testPSK(t, 0x5A)
	for _, onWire := range []int{ProbeBaseOnWire, 1400, 1500} {
		padLen, err := PadLenForOnWire(onWire)
		if err != nil {
			t.Fatalf("onWire=%d: PadLenForOnWire: %v", onWire, err)
		}
		want := Probe{
			PathID:         7,
			ProbeSeq:       0xA1B2C3D4E5F60718,
			TimestampNanos: 1_700_000_000_123_456_789,
			SessionID:      0x0102030405060708,
			Challenge:      0x1122334455667788,
			Padded:         true,
			PadLen:         padLen,
		}
		raw, err := Encode(psk, want)
		if err != nil {
			t.Fatalf("onWire=%d: encode: %v", onWire, err)
		}
		if len(raw) != onWire {
			t.Fatalf("onWire=%d: encoded datagram is %d bytes, want exactly %d", onWire, len(raw), onWire)
		}
		got, err := Decode(psk, raw)
		if err != nil {
			t.Fatalf("onWire=%d: decode: %v", onWire, err)
		}
		if !reflect.DeepEqual(want, got) {
			t.Fatalf("onWire=%d: round-trip mismatch:\n want %#v\n got  %#v", onWire, want, got)
		}
		probe, ok := got.(Probe)
		if !ok {
			t.Fatalf("onWire=%d: decoded frame kind %d, want probe", onWire, got.Kind())
		}
		if !probe.Padded || probe.PadLen != padLen {
			t.Fatalf("onWire=%d: decoded Padded=%v PadLen=%d, want true/%d", onWire, probe.Padded, probe.PadLen, padLen)
		}
		if ProbeOnWireSize(probe.PadLen) != onWire {
			t.Fatalf("onWire=%d: ProbeOnWireSize(%d)=%d, want %d", onWire, probe.PadLen, ProbeOnWireSize(probe.PadLen), onWire)
		}
	}
}

// TestPaddedProbeEchoCarriesSize models the reflector's echo path at the codec
// level: decode a padded probe, flip IsEcho (as telemetry.Reflector does), re-encode
// and confirm the echo is the SAME on-wire size and still carries the pad length.
func TestPaddedProbeEchoCarriesSize(t *testing.T) {
	psk := testPSK(t, 0x33)
	const onWire = 1400
	padLen, err := PadLenForOnWire(onWire)
	if err != nil {
		t.Fatalf("PadLenForOnWire: %v", err)
	}
	raw, err := Encode(psk, Probe{PathID: 2, ProbeSeq: 9, TimestampNanos: 42, SessionID: 0xABCD, Challenge: 0x1, Padded: true, PadLen: padLen})
	if err != nil {
		t.Fatalf("encode probe: %v", err)
	}
	f, err := Decode(psk, raw)
	if err != nil {
		t.Fatalf("decode probe: %v", err)
	}
	probe := f.(Probe)
	probe.IsEcho = true
	echo, err := Encode(psk, probe)
	if err != nil {
		t.Fatalf("encode echo: %v", err)
	}
	if len(echo) != onWire {
		t.Fatalf("echo datagram is %d bytes, want %d (same size as request)", len(echo), onWire)
	}
	back, err := Decode(psk, echo)
	if err != nil {
		t.Fatalf("decode echo: %v", err)
	}
	ep := back.(Probe)
	if !ep.IsEcho || !ep.Padded || ProbeOnWireSize(ep.PadLen) != onWire {
		t.Fatalf("echo IsEcho=%v Padded=%v onWire=%d, want echo padded at %d", ep.IsEcho, ep.Padded, ProbeOnWireSize(ep.PadLen), onWire)
	}
}

// TestPaddedProbeTruncatedRejected drives decodeBody directly with a padded-probe
// body whose declared PadLen disagrees with the trailing byte count — the
// self-consistency check must reject both a short (truncated) and a long
// (oversized) body as malformed. Driving decodeBody isolates the length check from
// the MAC (an in-flight truncation would fail the MAC first).
func TestPaddedProbeTruncatedRejected(t *testing.T) {
	// body layout AFTER the kind byte (decodeBody receives b = body[1:]):
	// pathID(1) || probeSeq(8) || ts(8) || flags(1) || sessionID(8) || challenge(8) || padLen(2) || pad...
	header := func(padLen uint16) []byte {
		b := make([]byte, 0, 34)
		b = append(b, 7)                             // pathID
		b = append(b, 0, 0, 0, 0, 0, 0, 0, 1)        // probeSeq
		b = append(b, 0, 0, 0, 0, 0, 0, 0, 2)        // ts
		b = append(b, probeFlagPadded)               // flags: padded, not echo
		b = append(b, 0, 0, 0, 0, 0, 0, 0, 3)        // sessionID
		b = append(b, 0, 0, 0, 0, 0, 0, 0, 4)        // challenge
		b = append(b, byte(padLen>>8), byte(padLen)) // declared PadLen
		return b
	}
	cases := map[string]struct {
		declared uint16
		actual   int
	}{
		"truncated": {declared: 10, actual: 3}, // claims 10 pad bytes, only 3 present
		"oversized": {declared: 4, actual: 9},  // claims 4 pad bytes, 9 present
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			body := append(header(tc.declared), make([]byte, tc.actual)...)
			_, err := decodeBody(KindProbe, body)
			if !errors.Is(err, ErrMalformed) {
				t.Fatalf("decodeBody(%s) err = %v, want ErrMalformed", name, err)
			}
		})
	}
}

// TestPaddedProbeOversizedRejected asserts the encode-side range guard rejects a
// target beyond MaxPaddedProbeOnWire, both via the size→PadLen helper and via a
// hand-built oversized Probe.
func TestPaddedProbeOversizedRejected(t *testing.T) {
	if _, err := PadLenForOnWire(MaxPaddedProbeOnWire + 1); !errors.Is(err, ErrMalformed) {
		t.Fatalf("PadLenForOnWire(max+1) err = %v, want ErrMalformed", err)
	}
	if _, err := PadLenForOnWire(ProbeBaseOnWire - 1); !errors.Is(err, ErrMalformed) {
		t.Fatalf("PadLenForOnWire(base-1) err = %v, want ErrMalformed", err)
	}
	psk := testPSK(t, 0x7E)
	oversized := Probe{PathID: 1, Padded: true, PadLen: uint16(MaxPaddedProbeOnWire)} // ~9000+9000 on-wire
	if _, err := Encode(psk, oversized); !errors.Is(err, ErrMalformed) {
		t.Fatalf("Encode(oversized padded probe) err = %v, want ErrMalformed", err)
	}
	withPayload := Probe{PathID: 1, Padded: true, PadLen: 8, Payload: []byte("x")}
	if _, err := Encode(psk, withPayload); !errors.Is(err, ErrMalformed) {
		t.Fatalf("Encode(padded probe + payload) err = %v, want ErrMalformed", err)
	}
}

// TestUnpaddedProbeByteIdentical guards the backward-compat invariant: an unpadded
// probe (the SendProbe / existing-test shape) encodes to exactly the historical
// on-wire size — no PadLen field, no padding — and its flags byte round-trips
// Padded=false.
func TestUnpaddedProbeByteIdentical(t *testing.T) {
	psk := testPSK(t, 0x11)
	raw, err := Encode(psk, Probe{PathID: 3, ProbeSeq: 5, TimestampNanos: 99, SessionID: 0xDEAD, Challenge: 0xBEEF})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if len(raw) != historicalUnpaddedProbeOnWire {
		t.Fatalf("unpadded probe encoded to %d bytes, want historical %d (extension must not widen unpadded probes)", len(raw), historicalUnpaddedProbeOnWire)
	}
	f, err := Decode(psk, raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	probe := f.(Probe)
	if probe.Padded || probe.PadLen != 0 {
		t.Fatalf("decoded unpadded probe reports Padded=%v PadLen=%d, want false/0", probe.Padded, probe.PadLen)
	}
}
