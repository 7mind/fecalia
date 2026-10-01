package frame

import (
	"bytes"
	crand "crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
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
		Probe{PathID: 1, ProbeSeq: 42, TimestampNanos: 1_700_000_000_123_456_789, SessionID: 0x0102030405060708, Challenge: 0x1122334455667788, Payload: []byte("probe")},
		Probe{PathID: 0, ProbeSeq: 0, TimestampNanos: -1, SessionID: 0, Challenge: 0, Payload: nil},
		Control{ControlType: 9, Seq: 0x1122334455667788, Payload: []byte("control payload")},
		Control{ControlType: 0, Seq: 0, Payload: nil},
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
	first := Control{ControlType: 1, Seq: 1, Payload: []byte("first-payload-value")}
	rawFirst, _ := c.Encode(nil, first)
	got1, err := c.Decode(rawFirst)
	if err != nil {
		t.Fatalf("decode first: %v", err)
	}
	firstPayload := got1.(Control).Payload

	// Decode an unrelated, longer frame through the SAME codec.
	second := Control{ControlType: 2, Seq: 2, Payload: bytes.Repeat([]byte{0xAB}, 200)}
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
	orig := Control{ControlType: 1, Payload: []byte("secret")}
	raw, err := a.Encode(nil, orig)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	// The wrong obfKey de-obfuscates the kind byte to garbage: an unknown kind is
	// ErrMalformed, and one that lands on PROBE/CONTROL fails the MAC under the
	// wrong authKey. No outcome decodes.
	if f2, err := b.Decode(raw); err == nil {
		t.Fatalf("cross-PSK frame accepted as %#v", f2)
	} else if !errors.Is(err, ErrAuth) && !errors.Is(err, ErrMalformed) {
		t.Fatalf("cross-PSK frame: got %v, want ErrAuth or ErrMalformed", err)
	}
}

// TestNewCodecRejectsUnsetPSK: the constructor fails fast on an unset PSK.
func TestNewCodecRejectsUnsetPSK(t *testing.T) {
	var unset config.Key
	if _, err := NewCodec(unset); err == nil {
		t.Fatal("NewCodec accepted an unset PSK")
	}
}

// TestEveryFrameCarriesTag confirms every kind appends the authentication tag.
func TestEveryFrameCarriesTag(t *testing.T) {
	psk := testPSK(t, 0x5A)
	for _, f := range []Frame{
		Probe{Payload: []byte("x")},
		Control{Payload: []byte("x")},
	} {
		raw, err := Encode(psk, f)
		if err != nil {
			t.Fatal(err)
		}
		body := f.appendBody(nil)
		if wantLen := nonceLen + len(body) + tagLen; len(raw) != wantLen {
			t.Fatalf("kind %d: encoded len %d, want %d", f.Kind(), len(raw), wantLen)
		}
	}
}

// TestControlOverheadMatchesEncoding pins ControlOverhead to the codec: it is the
// encoded size of a payload-less CONTROL frame, and a payload adds exactly its
// own length on top.
func TestControlOverheadMatchesEncoding(t *testing.T) {
	psk := testPSK(t, 0x5A)
	empty, err := Encode(psk, Control{Payload: nil})
	if err != nil {
		t.Fatal(err)
	}
	if len(empty) != ControlOverhead {
		t.Fatalf("len(Encode(Control{Payload: nil})) = %d, want ControlOverhead %d", len(empty), ControlOverhead)
	}
	payload := bytes.Repeat([]byte{0xAB}, 1200)
	full, err := Encode(psk, Control{ControlType: 7, Seq: 1 << 40, Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	if len(full) != ControlOverhead+len(payload) {
		t.Fatalf("encoded CONTROL with %d payload bytes is %d bytes, want %d", len(payload), len(full), ControlOverhead+len(payload))
	}
}

// sealBody builds the wire image nonce || obf(body) || tag for an arbitrary
// plaintext body under psk, exactly as Codec.Encode seals a frame's body. It lets
// a test put a kind byte on the wire that no Frame value can produce.
func sealBody(t *testing.T, psk config.Key, body []byte, withTag bool) []byte {
	t.Helper()
	obfKey, authKey, err := subkeys(psk)
	if err != nil {
		t.Fatalf("subkeys: %v", err)
	}
	nonce := make([]byte, nonceLen)
	if _, err := crand.Read(nonce); err != nil {
		t.Fatalf("read nonce: %v", err)
	}
	obfBody := append([]byte(nil), body...)
	obfuscate(obfKey, nonce, obfBody)
	raw := append(append([]byte(nil), nonce...), obfBody...)
	if withTag {
		raw = append(raw, tag(authKey, nonce, obfBody)...)
	}
	return raw
}

// TestSealBodyMatchesEncode guards the white-box helper: sealing a valid frame's
// own body yields a datagram Decode accepts as that frame, so a rejection of a
// sealed image is attributable to its kind byte and not to a helper/codec drift.
func TestSealBodyMatchesEncode(t *testing.T) {
	psk := testPSK(t, 0x5A)
	for i, want := range sampleFrames() {
		got, err := Decode(psk, sealBody(t, psk, want.appendBody(nil), true))
		if err != nil {
			t.Fatalf("frame %d: decode sealed body: %v", i, err)
		}
		if !reflect.DeepEqual(want, got) {
			t.Fatalf("frame %d: sealed body mismatch:\n want %#v\n got  %#v", i, want, got)
		}
	}
}

// Kind bytes of the removed DATA and PARITY frames.
const (
	removedKindData   = 1
	removedKindParity = 2
)

// TestRemovedKindsRejected: a datagram whose kind byte de-obfuscates to the
// removed DATA (1) or PARITY (2) kind is rejected as malformed in both shapes a
// peer could send — the tag-less layout the removed transports emitted, and the
// same body sealed with a valid MAC by a PSK holder.
func TestRemovedKindsRejected(t *testing.T) {
	psk := testPSK(t, 0x5A)
	payload := []byte("opaque wireguard datagram bytes")

	// kind || outer-seq(8) || path-id || fec-group(4) || fec-index || flags || payload
	data := []byte{removedKindData}
	data = binary.BigEndian.AppendUint64(data, 0xDEADBEEFCAFEBABE)
	data = append(data, 3)
	data = binary.BigEndian.AppendUint32(data, 0x01020304)
	data = append(data, 0xC7, 0xA5)
	data = append(data, payload...)

	// kind || fec-group(4) || parity-index(2) || data-count || path-id || payload
	parity := []byte{removedKindParity}
	parity = binary.BigEndian.AppendUint32(parity, 0x11223344)
	parity = binary.BigEndian.AppendUint16(parity, 0x7F0E)
	parity = append(parity, 0xB3, 2)
	parity = append(parity, payload...)

	for name, body := range map[string][]byte{"data": data, "parity": parity, "data-empty": data[:1], "parity-empty": parity[:1]} {
		for _, withTag := range []bool{false, true} {
			got, err := Decode(psk, sealBody(t, psk, body, withTag))
			if !errors.Is(err, ErrMalformed) {
				t.Fatalf("%s (tag=%v): got frame %#v err %v, want ErrMalformed", name, withTag, got, err)
			}
		}
	}
}

// TestUnknownKindsRejected: only PROBE and CONTROL decode; every other kind byte,
// sealed with a valid MAC, is malformed.
func TestUnknownKindsRejected(t *testing.T) {
	psk := testPSK(t, 0x5A)
	control := Control{ControlType: 4, Seq: 9, Payload: []byte("payload")}.appendBody(nil)
	for k := 0; k < 256; k++ {
		if Kind(k) == KindProbe || Kind(k) == KindControl {
			continue
		}
		body := append([]byte(nil), control...)
		body[0] = byte(k)
		got, err := Decode(psk, sealBody(t, psk, body, true))
		if !errors.Is(err, ErrMalformed) {
			t.Fatalf("kind %d: got frame %#v err %v, want ErrMalformed", k, got, err)
		}
	}
}

// TestTamperedRejected verifies the authentication guarantee: no mutation of a
// valid frame's bytes decodes. Every single-bit flip of every byte, every other
// value of the kind byte, and every truncation is rejected, and every mutation
// in the region after the kind byte (header, payload, tag) fails the MAC check
// with ErrAuth. The PROBE case carries a non-zero SessionID and Challenge (T38)
// so those body bytes are exercised as MAC-covered too.
//
// The kind byte is MAC-covered as well, but Decode classifies it before it
// verifies the tag: rewriting it to the other valid kind fails the MAC, and
// rewriting it to anything else (including the removed DATA/PARITY kinds) is
// malformed.
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
		rejected := func(what string, mutated []byte) error {
			got, err := Decode(psk, mutated)
			if err == nil {
				t.Fatalf("kind %d: %s accepted as %#v", f.Kind(), what, got)
			}
			if !errors.Is(err, ErrAuth) && !errors.Is(err, ErrMalformed) {
				t.Fatalf("kind %d: %s: got %v, want ErrAuth or ErrMalformed", f.Kind(), what, err)
			}
			return err
		}
		// The kind byte sits at offset nonceLen; everything after it is
		// MAC-covered.
		const kindOffset = nonceLen
		for i := 0; i < len(raw); i++ {
			for bit := 0; bit < 8; bit++ {
				mutated := append([]byte(nil), raw...)
				mutated[i] ^= 1 << bit
				err := rejected(fmt.Sprintf("flip of bit %d at byte %d", bit, i), mutated)
				// Any mutation strictly after the kind byte is covered by the MAC and
				// must fail authentication.
				if i > kindOffset && !errors.Is(err, ErrAuth) {
					t.Fatalf("kind %d: flip of bit %d at byte %d (MAC-covered): got %v, want ErrAuth", f.Kind(), bit, i, err)
				}
			}
		}
		// XOR on the obfuscated kind byte is XOR on the plaintext kind, so this
		// enumerates every other kind value, among them the other valid kind and the
		// removed kinds 1 and 2.
		for x := 1; x < 256; x++ {
			mutated := append([]byte(nil), raw...)
			mutated[kindOffset] ^= byte(x)
			err := rejected(fmt.Sprintf("kind byte rewritten to %d", byte(f.Kind())^byte(x)), mutated)
			want := ErrMalformed
			if Kind(byte(f.Kind()) ^ byte(x)).valid() {
				want = ErrAuth
			}
			if !errors.Is(err, want) {
				t.Fatalf("kind %d: kind byte rewritten to %d: got %v, want %v", f.Kind(), byte(f.Kind())^byte(x), err, want)
			}
		}
		// Every truncation, including one that only shortens the tag.
		for n := 0; n < len(raw); n++ {
			rejected(fmt.Sprintf("truncation to %d bytes", n), raw[:n])
		}
		// Trailing garbage shifts the tag window.
		if err := rejected("one appended byte", append(append([]byte(nil), raw...), 0)); !errors.Is(err, ErrAuth) {
			t.Fatalf("kind %d: appended byte: got %v, want ErrAuth", f.Kind(), err)
		}
	}
}

// controlHeaderLen PINS the CONTROL wire contract the D4 anti-replay depends on: a
// CONTROL body is exactly kind(1) || controlType(1) || seq(8) || payload. It is a
// build-time invariant — a code change that drops or resizes the Seq breaks the
// arithmetic here and fails TestControlWireContract, so the freshness material the
// telemetry.ControlGuard relies on can never silently leave the wire (T44 / D4).
const controlHeaderLen = 1 /*kind*/ + 1 /*controlType*/ + 8 /*seq*/

// TestControlWireContract is the build-time invariant guarding the CONTROL wire
// format the D4 anti-replay rests on (T44): the monotonic Seq occupies a fixed
// position in the body AND rides inside the MAC-covered region. A regression that
// drops the Seq, moves it, or excludes it from the authenticated body fails this
// test — so a security-relevant control frame's replay-freshness material can never
// silently regress off the authenticated wire (which would reopen D4 without any
// other test noticing).
func TestControlWireContract(t *testing.T) {
	c := Control{ControlType: 4, Seq: 0x0102030405060708, Payload: []byte("rekey now")}

	// Body layout: kind || controlType || seq(big-endian) || payload.
	body := c.appendBody(nil)
	if len(body) != controlHeaderLen+len(c.Payload) {
		t.Fatalf("control body len %d, want %d (kind|type|seq|payload)", len(body), controlHeaderLen+len(c.Payload))
	}
	if Kind(body[0]) != KindControl {
		t.Fatalf("control body[0] = %d, want KindControl (%d)", body[0], KindControl)
	}
	if body[1] != c.ControlType {
		t.Fatalf("control body[1] = %d, want ControlType %d", body[1], c.ControlType)
	}
	if got := binary.BigEndian.Uint64(body[2:10]); got != c.Seq {
		t.Fatalf("control Seq at body[2:10] = %#x, want %#x", got, c.Seq)
	}

	// The Seq must be MAC-covered: on the wire the body follows the clear nonce, so
	// the Seq occupies wire offsets [nonceLen+2, nonceLen+10). Flipping any of those
	// bytes must fail authentication — proving the guard's freshness material cannot
	// be tampered without detection.
	psk := testPSK(t, 0x5A)
	raw, err := Encode(psk, c)
	if err != nil {
		t.Fatal(err)
	}
	for off := nonceLen + 2; off < nonceLen+controlHeaderLen; off++ {
		mutated := append([]byte(nil), raw...)
		mutated[off] ^= 0x01
		if _, err := Decode(psk, mutated); !errors.Is(err, ErrAuth) {
			t.Fatalf("flip at Seq wire byte %d: got %v, want ErrAuth (Seq must be MAC-covered)", off, err)
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
		// The wrong obfKey de-obfuscates the kind byte to garbage: an unknown kind
		// is ErrMalformed, and one that lands on PROBE/CONTROL fails the MAC under
		// the wrong authKey. No outcome decodes.
		if f2, err := Decode(pskB, raw); err == nil {
			t.Fatalf("kind %d: PSK-mismatched frame accepted as %#v", f.Kind(), f2)
		} else if !errors.Is(err, ErrAuth) && !errors.Is(err, ErrMalformed) {
			t.Fatalf("kind %d: PSK-mismatched frame: got %v, want ErrAuth or ErrMalformed", f.Kind(), err)
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
		case KindProbe:
			return Probe{PathID: uint8(rng.Intn(256)), ProbeSeq: rng.Uint64(), TimestampNanos: int64(rng.Uint64()), SessionID: rng.Uint64(), Challenge: rng.Uint64(), Payload: payload}
		case KindControl:
			return Control{ControlType: uint8(rng.Intn(256)), Seq: rng.Uint64(), Payload: payload}
		default:
			t.Fatalf("unknown kind %d", kind)
			return nil
		}
	}

	for _, kind := range []Kind{KindProbe, KindControl} {
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
	switch rng.Intn(2) {
	case 0:
		return Probe{PathID: uint8(rng.Intn(256)), ProbeSeq: rng.Uint64(), TimestampNanos: int64(rng.Uint64()), SessionID: rng.Uint64(), Challenge: rng.Uint64(), Payload: payload}
	default:
		return Control{ControlType: uint8(rng.Intn(256)), Seq: rng.Uint64(), Payload: payload}
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
