package frame

import (
	"bytes"
	"encoding/base64"
	"errors"
	"math/rand"
	"reflect"
	"testing"

	"github.com/7mind/wanbond/internal/config"
)

// testPSK builds a config.Key from 32 deterministic bytes seeded by seed.
func testPSK(t testing.TB, seed byte) config.Key {
	t.Helper()
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = seed ^ byte(i*31+7)
	}
	var k config.Key
	if err := k.UnmarshalText([]byte(base64.StdEncoding.EncodeToString(raw))); err != nil {
		t.Fatalf("build PSK: %v", err)
	}
	return k
}

// sampleFrames returns one representative frame of each kind, including
// non-empty and empty payloads.
func sampleFrames() []Frame {
	return []Frame{
		Data{OuterSeq: 0xDEADBEEFCAFEBABE, PathID: 3, FECGroup: 0x01020304, Flags: 0xA5, Payload: []byte("opaque wireguard datagram bytes")},
		Data{OuterSeq: 0, PathID: 0, FECGroup: 0, Flags: 0, Payload: nil},
		Parity{FECGroup: 0x11223344, ParityIndex: 0x7F0E, PathID: 2, Payload: []byte{0xFF, 0x00, 0x10, 0x20}},
		Parity{FECGroup: 1, ParityIndex: 0, PathID: 0, Payload: nil},
		Probe{PathID: 1, ProbeSeq: 42, TimestampNanos: 1_700_000_000_123_456_789, SessionID: 0x0102030405060708, Challenge: 0x1122334455667788, Payload: []byte("probe")},
		Probe{PathID: 0, ProbeSeq: 0, TimestampNanos: -1, SessionID: 0, Challenge: 0, Payload: nil},
		Control{ControlType: 9, Payload: []byte("control payload")},
		Control{ControlType: 0, Payload: nil},
	}
}

func TestRoundTrip(t *testing.T) {
	psk := testPSK(t, 0x5A)
	for i, want := range sampleFrames() {
		raw, err := Encode(psk, want)
		if err != nil {
			t.Fatalf("frame %d: encode: %v", i, err)
		}
		got, err := Decode(psk, raw)
		if err != nil {
			t.Fatalf("frame %d: decode: %v", i, err)
		}
		if !reflect.DeepEqual(want, got) {
			t.Fatalf("frame %d: round-trip mismatch:\n want %#v\n got  %#v", i, want, got)
		}
		if got.Kind() != want.Kind() {
			t.Fatalf("frame %d: kind mismatch: want %d got %d", i, want.Kind(), got.Kind())
		}
	}
}

// TestCodecRoundTrip exercises the reusable Codec (defect D5): one Codec,
// constructed once, encodes and decodes every sample frame, and its Encode/Decode
// agree with the package-level one-shot wrappers.
func TestCodecRoundTrip(t *testing.T) {
	psk := testPSK(t, 0x5A)
	enc, err := NewCodec(psk)
	if err != nil {
		t.Fatalf("NewCodec: %v", err)
	}
	dec, err := NewCodec(psk)
	if err != nil {
		t.Fatalf("NewCodec: %v", err)
	}
	for i, want := range sampleFrames() {
		raw, err := enc.Encode(nil, want)
		if err != nil {
			t.Fatalf("frame %d: codec encode: %v", i, err)
		}
		got, err := dec.Decode(raw)
		if err != nil {
			t.Fatalf("frame %d: codec decode: %v", i, err)
		}
		if !reflect.DeepEqual(want, got) {
			t.Fatalf("frame %d: codec round-trip mismatch:\n want %#v\n got  %#v", i, want, got)
		}
		// The package-level wrapper must accept a Codec-produced frame too.
		if got2, err := Decode(psk, raw); err != nil || !reflect.DeepEqual(want, got2) {
			t.Fatalf("frame %d: package Decode of codec output: got %#v err %v", i, got2, err)
		}
	}
}

// TestCodecEncodeBufferReuse confirms the dst-append API reuses one buffer across
// encodes without corrupting output: each frame appended to a growing scratch
// slice decodes back to itself.
func TestCodecEncodeBufferReuse(t *testing.T) {
	psk := testPSK(t, 0x77)
	c, err := NewCodec(psk)
	if err != nil {
		t.Fatalf("NewCodec: %v", err)
	}
	buf := make([]byte, 0, 16)
	for i, want := range sampleFrames() {
		out, err := c.Encode(buf[:0], want)
		if err != nil {
			t.Fatalf("frame %d: encode: %v", i, err)
		}
		buf = out // reuse the (possibly grown) backing array next iteration
		got, err := c.Decode(out)
		if err != nil {
			t.Fatalf("frame %d: decode: %v", i, err)
		}
		if !reflect.DeepEqual(want, got) {
			t.Fatalf("frame %d: reuse round-trip mismatch:\n want %#v\n got  %#v", i, want, got)
		}
	}
}

// TestCodecReuseDecodeStability decodes many frames through ONE Codec to catch any
// scratch-buffer aliasing: an earlier decode's returned payload must survive a
// later decode intact.
func TestCodecReuseDecodeStability(t *testing.T) {
	psk := testPSK(t, 0x33)
	c, err := NewCodec(psk)
	if err != nil {
		t.Fatalf("NewCodec: %v", err)
	}
	first := Data{OuterSeq: 1, PathID: 1, Payload: []byte("first-payload-value")}
	rawFirst, _ := c.Encode(nil, first)
	got1, err := c.Decode(rawFirst)
	if err != nil {
		t.Fatalf("decode first: %v", err)
	}
	firstPayload := got1.(Data).Payload

	// Decode an unrelated, longer frame through the SAME codec.
	second := Data{OuterSeq: 2, PathID: 2, Payload: bytes.Repeat([]byte{0xAB}, 200)}
	rawSecond, _ := c.Encode(nil, second)
	if _, err := c.Decode(rawSecond); err != nil {
		t.Fatalf("decode second: %v", err)
	}

	// The first decode's payload must be unchanged (not aliased into scratch).
	if !bytes.Equal(firstPayload, []byte("first-payload-value")) {
		t.Fatalf("first payload corrupted by a later decode: %q", firstPayload)
	}
}

// TestCodecPSKMismatch: an authenticated frame from one Codec is rejected by a
// Codec built from a different PSK.
func TestCodecPSKMismatch(t *testing.T) {
	a, err := NewCodec(testPSK(t, 0x11))
	if err != nil {
		t.Fatalf("NewCodec: %v", err)
	}
	b, err := NewCodec(testPSK(t, 0x22))
	if err != nil {
		t.Fatalf("NewCodec: %v", err)
	}
	raw, err := a.Encode(nil, Control{ControlType: 1, Payload: []byte("secret")})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if _, err := b.Decode(raw); err == nil {
		t.Fatal("cross-PSK authenticated frame accepted")
	}
}

// TestNewCodecRejectsUnsetPSK: the constructor fails fast on an unset PSK.
func TestNewCodecRejectsUnsetPSK(t *testing.T) {
	var unset config.Key
	if _, err := NewCodec(unset); err == nil {
		t.Fatal("NewCodec accepted an unset PSK")
	}
}

// TestAuthenticatedFramesCarryTag confirms CONTROL/PROBE append a tag and
// DATA/PARITY do not.
func TestAuthenticatedFramesCarryTag(t *testing.T) {
	psk := testPSK(t, 0x5A)
	cases := []struct {
		f    Frame
		auth bool
	}{
		{Data{Payload: []byte("x")}, false},
		{Parity{Payload: []byte("x")}, false},
		{Probe{Payload: []byte("x")}, true},
		{Control{Payload: []byte("x")}, true},
	}
	for _, c := range cases {
		raw, err := Encode(psk, c.f)
		if err != nil {
			t.Fatal(err)
		}
		body := c.f.appendBody(nil)
		wantLen := nonceLen + len(body)
		if c.auth {
			wantLen += tagLen
		}
		if len(raw) != wantLen {
			t.Fatalf("kind %d: encoded len %d, want %d (auth=%v)", c.f.Kind(), len(raw), wantLen, c.auth)
		}
	}
}

// TestTamperedRejected verifies the authentication guarantee for CONTROL/PROBE
// frames: no single-byte mutation is ever accepted AS an authenticated frame,
// and every mutation in the MAC-covered region (everything after the kind byte,
// including the tag) fails the MAC check with ErrAuth. The PROBE case carries a
// non-zero SessionID and Challenge (T38) so those body bytes are exercised as
// MAC-covered too.
//
// The one place a mutation is "accepted" is a flip of the kind byte itself that
// re-labels the frame as an unauthenticated kind (DATA/PARITY). That is not a
// break: DATA/PARITY are forgeable by design (DoS-grade risk accepted — the
// inner WireGuard layer authenticates the real payload), so an attacker who can
// flip the kind byte could equally have injected a fresh DATA/PARITY frame. The
// invariant that matters — a tampered frame is never accepted as an authentic
// CONTROL/PROBE — holds for every mutation.
func TestTamperedRejected(t *testing.T) {
	psk := testPSK(t, 0x5A)
	for _, f := range []Frame{
		Probe{PathID: 1, ProbeSeq: 7, TimestampNanos: 123, SessionID: 0xDEADBEEFCAFEF00D, Challenge: 0xC0FFEE0DDF00D123, Payload: []byte("liveness")},
		Control{ControlType: 4, Payload: []byte("rekey now")},
	} {
		raw, err := Encode(psk, f)
		if err != nil {
			t.Fatal(err)
		}
		// The kind byte sits at offset nonceLen; everything after it is
		// MAC-covered.
		const kindOffset = nonceLen
		for i := 0; i < len(raw); i++ {
			mutated := append([]byte(nil), raw...)
			mutated[i] ^= 0x01
			got, err := Decode(psk, mutated)
			// Never accepted as an authenticated frame.
			if err == nil && got.Kind().authenticated() {
				t.Fatalf("kind %d: flip at byte %d accepted as authenticated frame %#v", f.Kind(), i, got)
			}
			// Any mutation strictly after the kind byte is covered by the MAC and
			// must fail authentication.
			if i > kindOffset {
				if !errors.Is(err, ErrAuth) {
					t.Fatalf("kind %d: flip at byte %d (MAC-covered): got %v, want ErrAuth", f.Kind(), i, err)
				}
			}
		}
		// A flip inside the tag region must fail the MAC specifically.
		mutated := append([]byte(nil), raw...)
		mutated[len(mutated)-1] ^= 0x80
		if _, err := Decode(psk, mutated); !errors.Is(err, ErrAuth) {
			t.Fatalf("kind %d: tag flip: got %v, want ErrAuth", f.Kind(), err)
		}
		// Truncating the tag must be rejected.
		if _, err := Decode(psk, raw[:len(raw)-1]); err == nil {
			t.Fatalf("kind %d: truncated tag accepted", f.Kind())
		}
	}
}

// TestPSKMismatchRejected verifies that authenticated frames encoded under one
// PSK are rejected when decoded under a different PSK.
func TestPSKMismatchRejected(t *testing.T) {
	pskA := testPSK(t, 0x11)
	pskB := testPSK(t, 0x22)
	for _, f := range []Frame{
		Probe{PathID: 1, ProbeSeq: 7, TimestampNanos: 123, SessionID: 0xDEADBEEFCAFEF00D, Challenge: 0xC0FFEE0DDF00D123, Payload: []byte("liveness")},
		Control{ControlType: 4, Payload: []byte("rekey now")},
	} {
		raw, err := Encode(pskA, f)
		if err != nil {
			t.Fatal(err)
		}
		// A wrong-PSK AUTHENTICATED frame must never decode as a valid frame of the
		// SAME kind: its HMAC cannot pass under the wrong authKey. Note Decode CAN
		// return err==nil ~2/256 of the time even here — the wrong obfKey de-obfuscates
		// the body to garbage whose uniformly-random kind byte occasionally lands on the
		// UNAUTHENTICATED KindData/KindParity, which carry no MAC by design (D9 threat
		// model; inner WireGuard authenticates real DATA). That is expected, not an auth
		// failure, so assert on the decoded KIND, not merely on err (defect D17).
		if f2, err := Decode(pskB, raw); err == nil && f2.Kind() == f.Kind() {
			t.Fatalf("kind %d: PSK-mismatched authenticated frame accepted as the same kind", f.Kind())
		}
	}
}

// TestByteHistogramNoConstantPosition asserts that across many encodings of
// random payloads, no byte position is constant — the requirement-6 no-fixed-
// offset property. Fixed-length payloads keep every encoding the same length so
// every position is present in every sample.
func TestByteHistogramNoConstantPosition(t *testing.T) {
	psk := testPSK(t, 0x5A)
	rng := rand.New(rand.NewSource(1))
	const (
		samples     = 256
		payloadSize = 64
	)

	build := func(kind Kind, payload []byte) Frame {
		switch kind {
		case KindData:
			return Data{OuterSeq: rng.Uint64(), PathID: uint8(rng.Intn(256)), FECGroup: rng.Uint32(), Flags: uint8(rng.Intn(256)), Payload: payload}
		case KindParity:
			return Parity{FECGroup: rng.Uint32(), ParityIndex: uint16(rng.Intn(1 << 16)), PathID: uint8(rng.Intn(256)), Payload: payload}
		case KindProbe:
			return Probe{PathID: uint8(rng.Intn(256)), ProbeSeq: rng.Uint64(), TimestampNanos: int64(rng.Uint64()), SessionID: rng.Uint64(), Challenge: rng.Uint64(), Payload: payload}
		case KindControl:
			return Control{ControlType: uint8(rng.Intn(256)), Payload: payload}
		default:
			t.Fatalf("unknown kind %d", kind)
			return nil
		}
	}

	for _, kind := range []Kind{KindData, KindParity, KindProbe, KindControl} {
		var encodings [][]byte
		frameLen := -1
		for s := 0; s < samples; s++ {
			payload := make([]byte, payloadSize)
			rng.Read(payload)
			raw, err := Encode(psk, build(kind, payload))
			if err != nil {
				t.Fatal(err)
			}
			if frameLen == -1 {
				frameLen = len(raw)
			}
			if len(raw) != frameLen {
				t.Fatalf("kind %d: inconsistent encoded length %d vs %d", kind, len(raw), frameLen)
			}
			encodings = append(encodings, raw)
		}
		for pos := 0; pos < frameLen; pos++ {
			first := encodings[0][pos]
			constant := true
			for _, e := range encodings[1:] {
				if e[pos] != first {
					constant = false
					break
				}
			}
			if constant {
				t.Fatalf("kind %d: byte position %d is constant (=0x%02x) across %d encodings", kind, pos, first, samples)
			}
		}
	}
}

// TestPropertyRoundTrip is a deterministic property gate: many random frames of
// random kinds round-trip exactly. It runs without -fuzz so CI has a stable
// check.
func TestPropertyRoundTrip(t *testing.T) {
	psk := testPSK(t, 0x33)
	rng := rand.New(rand.NewSource(42))
	for i := 0; i < 5000; i++ {
		want := randomFrame(rng)
		raw, err := Encode(psk, want)
		if err != nil {
			t.Fatalf("iter %d: encode: %v", i, err)
		}
		got, err := Decode(psk, raw)
		if err != nil {
			t.Fatalf("iter %d: decode %#v: %v", i, want, err)
		}
		if !reflect.DeepEqual(want, got) {
			t.Fatalf("iter %d: mismatch:\n want %#v\n got  %#v", i, want, got)
		}
	}
}

func randomFrame(rng *rand.Rand) Frame {
	payload := randomBytes(rng)
	switch rng.Intn(4) {
	case 0:
		return Data{OuterSeq: rng.Uint64(), PathID: uint8(rng.Intn(256)), FECGroup: rng.Uint32(), Flags: uint8(rng.Intn(256)), Payload: payload}
	case 1:
		return Parity{FECGroup: rng.Uint32(), ParityIndex: uint16(rng.Intn(1 << 16)), PathID: uint8(rng.Intn(256)), Payload: payload}
	case 2:
		return Probe{PathID: uint8(rng.Intn(256)), ProbeSeq: rng.Uint64(), TimestampNanos: int64(rng.Uint64()), SessionID: rng.Uint64(), Challenge: rng.Uint64(), Payload: payload}
	default:
		return Control{ControlType: uint8(rng.Intn(256)), Payload: payload}
	}
}

func randomBytes(rng *rand.Rand) []byte {
	n := rng.Intn(200)
	if n == 0 {
		return nil
	}
	b := make([]byte, n)
	rng.Read(b)
	return b
}

// FuzzDecode asserts Decode never panics on arbitrary input, and that any frame
// it does accept re-encodes and re-decodes to the same value (no information is
// invented or lost on the accepted path).
func FuzzDecode(f *testing.F) {
	psk := testPSK(f, 0x5A)
	for _, fr := range sampleFrames() {
		raw, err := Encode(psk, fr)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(raw)
	}
	f.Add([]byte(nil))
	f.Add(bytes.Repeat([]byte{0}, nonceLen+1))

	f.Fuzz(func(t *testing.T, raw []byte) {
		got, err := Decode(psk, raw)
		if err != nil {
			return // rejection is fine; the property is "no panic".
		}
		reRaw, err := Encode(psk, got)
		if err != nil {
			t.Fatalf("re-encode accepted frame: %v", err)
		}
		got2, err := Decode(psk, reRaw)
		if err != nil {
			t.Fatalf("re-decode accepted frame: %v", err)
		}
		if !reflect.DeepEqual(got, got2) {
			t.Fatalf("accepted frame not stable under re-encode:\n first %#v\n again %#v", got, got2)
		}
	})
}
