package frame

import (
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"

	"golang.org/x/crypto/chacha20"

	"github.com/7mind/wanbond/internal/config"
)

// Wire model (requirement-6 groundwork — no plaintext magic bytes, no fixed
// offsets). Every frame on the wire is:
//
//	nonce[nonceLen] || obf(body) [ || tag[tagLen] ]
//
//   - nonce is nonceLen fresh random bytes (high entropy, never constant).
//   - body is (kind byte || kind-specific header || opaque payload). It is
//     obfuscated by XOR with an XChaCha20 keystream derived from a PSK-bound
//     obfuscation subkey and the per-frame nonce, so the type discriminant and
//     header never sit at a constant offset — a byte-histogram over many random
//     encodings shows no constant byte position.
//   - tag is present ONLY for authenticated kinds (CONTROL, PROBE): an
//     Encrypt-then-MAC HMAC-SHA256 (truncated to tagLen) over nonce||obf(body)
//     keyed by a distinct PSK-bound auth subkey. Decode verifies it and rejects
//     tampered or PSK-mismatched frames. DATA and PARITY are unauthenticated by
//     design (DoS-grade forgery accepted — the inner WireGuard layer
//     authenticates the real payload); they carry no tag but are still
//     keystream-obfuscated so the no-fixed-offset property holds and the peer,
//     sharing the PSK, derives the same keystream to decode them.
//
// The two subkeys are derived from the PSK with HKDF-SHA256 under distinct info
// labels, so the obfuscation stream and the authentication MAC never share key
// material. All primitives are vetted (crypto/hkdf, x/crypto/chacha20,
// crypto/hmac) — no hand-rolled crypto.
//
// The outer-seq DATA field is this codec's OWN sequence space (see
// docs/p0-findings.md §6); it is never the inner WireGuard counter, and this
// codec never inspects the opaque WG payload (§4).

const (
	// nonceLen is the XChaCha20 nonce length carried in the clear at the head of
	// every frame.
	nonceLen = chacha20.NonceSizeX // 24
	// tagLen is the truncated HMAC-SHA256 authentication tag length appended to
	// authenticated frames.
	tagLen = 16
	// subkeyLen is the length of each HKDF-derived subkey.
	subkeyLen = 32

	infoObf  = "wanbond outer-frame obfuscation v1"
	infoAuth = "wanbond outer-frame authentication v1"
)

// Kind is the outer frame discriminant. Values are nonzero so a zeroed buffer
// never decodes to a valid kind.
type Kind uint8

const (
	// KindData wraps one opaque WireGuard datagram with multipath/FEC metadata.
	KindData Kind = 1
	// KindParity carries an FEC parity symbol for a fec-group.
	KindParity Kind = 2
	// KindProbe is an authenticated path-probe (RTT / liveness) frame.
	KindProbe Kind = 3
	// KindControl is an authenticated out-of-band control frame.
	KindControl Kind = 4
)

func (k Kind) valid() bool {
	return k == KindData || k == KindParity || k == KindProbe || k == KindControl
}

// authenticated reports whether frames of this kind append (and verify) a MAC.
func (k Kind) authenticated() bool {
	return k == KindProbe || k == KindControl
}

// Frame is the closed sum over the four outer frame kinds. The unexported
// marker method prevents other packages from adding kinds.
type Frame interface {
	// Kind returns the frame's discriminant.
	Kind() Kind
	// appendBody appends the plaintext body (kind byte || header || payload).
	appendBody(dst []byte) []byte
	isFrame()
}

// Data wraps a single opaque WireGuard datagram. OuterSeq is this codec's own
// sequence space (never the inner WG counter); PathID/FECGroup/Flags feed the
// multipath scheduler (T12), FEC (T14), and resequencer (T18).
type Data struct {
	OuterSeq uint64
	PathID   uint8
	FECGroup uint32
	Flags    uint8
	Payload  []byte
}

// Parity carries one FEC parity symbol for a fec-group.
type Parity struct {
	FECGroup    uint32
	ParityIndex uint16
	PathID      uint8
	Payload     []byte
}

// Probe is an authenticated path probe used for RTT and liveness estimation.
type Probe struct {
	PathID         uint8
	ProbeSeq       uint64
	TimestampNanos int64
	Payload        []byte
}

// Control is an authenticated out-of-band control frame.
type Control struct {
	ControlType uint8
	Payload     []byte
}

func (Data) isFrame()    {}
func (Parity) isFrame()  {}
func (Probe) isFrame()   {}
func (Control) isFrame() {}

func (Data) Kind() Kind    { return KindData }
func (Parity) Kind() Kind  { return KindParity }
func (Probe) Kind() Kind   { return KindProbe }
func (Control) Kind() Kind { return KindControl }

func (f Data) appendBody(dst []byte) []byte {
	dst = append(dst, byte(KindData))
	dst = binary.BigEndian.AppendUint64(dst, f.OuterSeq)
	dst = append(dst, f.PathID)
	dst = binary.BigEndian.AppendUint32(dst, f.FECGroup)
	dst = append(dst, f.Flags)
	return append(dst, f.Payload...)
}

func (f Parity) appendBody(dst []byte) []byte {
	dst = append(dst, byte(KindParity))
	dst = binary.BigEndian.AppendUint32(dst, f.FECGroup)
	dst = binary.BigEndian.AppendUint16(dst, f.ParityIndex)
	dst = append(dst, f.PathID)
	return append(dst, f.Payload...)
}

func (f Probe) appendBody(dst []byte) []byte {
	dst = append(dst, byte(KindProbe))
	dst = append(dst, f.PathID)
	dst = binary.BigEndian.AppendUint64(dst, f.ProbeSeq)
	dst = binary.BigEndian.AppendUint64(dst, uint64(f.TimestampNanos))
	return append(dst, f.Payload...)
}

func (f Control) appendBody(dst []byte) []byte {
	dst = append(dst, byte(KindControl))
	dst = append(dst, f.ControlType)
	return append(dst, f.Payload...)
}

// ErrMalformed is returned by Decode for any input too short or structurally
// invalid to be a frame.
var ErrMalformed = errors.New("frame: malformed input")

// ErrAuth is returned by Decode when an authenticated frame fails its MAC check
// (tampered ciphertext or PSK mismatch).
var ErrAuth = errors.New("frame: authentication failed")

// Encode serializes f into a self-contained wire frame under the given PSK. It
// fails only if the PSK is unset or the system CSPRNG is unavailable.
func Encode(psk config.Key, f Frame) ([]byte, error) {
	obfKey, authKey, err := subkeys(psk)
	if err != nil {
		return nil, err
	}

	body := f.appendBody(nil)

	nonce := make([]byte, nonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("frame: read nonce: %w", err)
	}

	obfuscate(obfKey, nonce, body)

	out := make([]byte, 0, nonceLen+len(body)+tagLen)
	out = append(out, nonce...)
	out = append(out, body...)

	if f.Kind().authenticated() {
		out = append(out, tag(authKey, nonce, body)...)
	}
	return out, nil
}

// Decode parses a wire frame under the given PSK. It verifies the MAC of
// authenticated frames and rejects tampered or PSK-mismatched ones. It never
// panics on malformed input.
//
// Authentication guarantee: a frame is accepted as an authenticated kind
// (CONTROL/PROBE) only if its MAC verifies under the PSK, so tampered or
// PSK-mismatched authenticated frames are rejected. Flipping the (obfuscated)
// kind byte can re-label an authenticated frame as an unauthenticated kind
// (DATA/PARITY), which then decodes without a MAC — this is not a downgrade
// break, because DATA/PARITY are forgeable by design (the inner WireGuard layer
// authenticates the real payload). No mutation can make a frame decode as an
// authentic CONTROL/PROBE.
func Decode(psk config.Key, raw []byte) (Frame, error) {
	obfKey, authKey, err := subkeys(psk)
	if err != nil {
		return nil, err
	}
	// Need at least the nonce plus one body byte (the kind discriminant).
	if len(raw) < nonceLen+1 {
		return nil, fmt.Errorf("%w: %d bytes, need >= %d", ErrMalformed, len(raw), nonceLen+1)
	}

	nonce := raw[:nonceLen]
	rest := raw[nonceLen:]

	// Recover the (obfuscated) kind byte to learn whether a tag is present and
	// where the body ends, without trusting any plaintext offset.
	kind := Kind(peekByte(obfKey, nonce, rest[0]))
	if !kind.valid() {
		return nil, fmt.Errorf("%w: unknown kind %d", ErrMalformed, uint8(kind))
	}

	obfBody := rest
	if kind.authenticated() {
		if len(rest) < tagLen+1 {
			return nil, fmt.Errorf("%w: authenticated frame too short", ErrMalformed)
		}
		obfBody = rest[:len(rest)-tagLen]
		gotTag := rest[len(rest)-tagLen:]
		wantTag := tag(authKey, nonce, obfBody)
		if !hmac.Equal(gotTag, wantTag) {
			return nil, ErrAuth
		}
	}

	body := make([]byte, len(obfBody))
	copy(body, obfBody)
	obfuscate(obfKey, nonce, body)

	// body[0] is the kind byte again; it must agree with the peeked kind.
	if Kind(body[0]) != kind {
		return nil, fmt.Errorf("%w: kind byte inconsistent", ErrMalformed)
	}
	return decodeBody(kind, body[1:])
}

// decodeBody parses the header+payload region (the body after the kind byte).
func decodeBody(kind Kind, b []byte) (Frame, error) {
	r := reader{b: b}
	switch kind {
	case KindData:
		seq, e1 := r.u64()
		pathID, e2 := r.u8()
		group, e3 := r.u32()
		flags, e4 := r.u8()
		if err := firstErr(e1, e2, e3, e4); err != nil {
			return nil, err
		}
		return Data{OuterSeq: seq, PathID: pathID, FECGroup: group, Flags: flags, Payload: r.rest()}, nil
	case KindParity:
		group, e1 := r.u32()
		idx, e2 := r.u16()
		pathID, e3 := r.u8()
		if err := firstErr(e1, e2, e3); err != nil {
			return nil, err
		}
		return Parity{FECGroup: group, ParityIndex: idx, PathID: pathID, Payload: r.rest()}, nil
	case KindProbe:
		pathID, e1 := r.u8()
		probeSeq, e2 := r.u64()
		ts, e3 := r.u64()
		if err := firstErr(e1, e2, e3); err != nil {
			return nil, err
		}
		return Probe{PathID: pathID, ProbeSeq: probeSeq, TimestampNanos: int64(ts), Payload: r.rest()}, nil
	case KindControl:
		ctype, e1 := r.u8()
		if err := firstErr(e1); err != nil {
			return nil, err
		}
		return Control{ControlType: ctype, Payload: r.rest()}, nil
	default:
		// Unreachable: kind was validated by the caller.
		return nil, fmt.Errorf("%w: unknown kind %d", ErrMalformed, uint8(kind))
	}
}

// subkeys derives the obfuscation and authentication subkeys from the PSK.
func subkeys(psk config.Key) (obfKey, authKey []byte, err error) {
	if !psk.IsSet() {
		return nil, nil, errors.New("frame: PSK is not set")
	}
	secret := psk.Bytes()
	obfKey, err = hkdf.Key(sha256.New, secret[:], nil, infoObf, subkeyLen)
	if err != nil {
		return nil, nil, fmt.Errorf("frame: derive obf key: %w", err)
	}
	authKey, err = hkdf.Key(sha256.New, secret[:], nil, infoAuth, subkeyLen)
	if err != nil {
		return nil, nil, fmt.Errorf("frame: derive auth key: %w", err)
	}
	return obfKey, authKey, nil
}

// obfuscate XORs body in place with the XChaCha20 keystream derived from obfKey
// and nonce. It is its own inverse.
func obfuscate(obfKey, nonce, body []byte) {
	c, err := chacha20.NewUnauthenticatedCipher(obfKey, nonce)
	if err != nil {
		// obfKey is always subkeyLen and nonce is always nonceLen, so the cipher
		// construction cannot fail; a failure indicates a programmer error.
		panic(fmt.Sprintf("frame: chacha20 init: %v", err))
	}
	c.XORKeyStream(body, body)
}

// peekByte recovers a single obfuscated body byte without consuming the shared
// keystream used for the full-body decode.
func peekByte(obfKey, nonce []byte, b byte) byte {
	buf := []byte{b}
	obfuscate(obfKey, nonce, buf)
	return buf[0]
}

// tag computes the truncated Encrypt-then-MAC authentication tag over
// nonce||obfBody.
func tag(authKey, nonce, obfBody []byte) []byte {
	m := hmac.New(sha256.New, authKey)
	m.Write(nonce)
	m.Write(obfBody)
	return m.Sum(nil)[:tagLen]
}

// reader is a fail-fast big-endian cursor over a byte slice.
type reader struct {
	b   []byte
	off int
}

func (r *reader) need(n int) error {
	if r.off+n > len(r.b) {
		return fmt.Errorf("%w: need %d more bytes at offset %d", ErrMalformed, n, r.off)
	}
	return nil
}

func (r *reader) u8() (uint8, error) {
	if err := r.need(1); err != nil {
		return 0, err
	}
	v := r.b[r.off]
	r.off++
	return v, nil
}

func (r *reader) u16() (uint16, error) {
	if err := r.need(2); err != nil {
		return 0, err
	}
	v := binary.BigEndian.Uint16(r.b[r.off:])
	r.off += 2
	return v, nil
}

func (r *reader) u32() (uint32, error) {
	if err := r.need(4); err != nil {
		return 0, err
	}
	v := binary.BigEndian.Uint32(r.b[r.off:])
	r.off += 4
	return v, nil
}

func (r *reader) u64() (uint64, error) {
	if err := r.need(8); err != nil {
		return 0, err
	}
	v := binary.BigEndian.Uint64(r.b[r.off:])
	r.off += 8
	return v, nil
}

// rest returns the remaining (opaque payload) bytes as a fresh copy so the
// decoded frame never aliases the caller's buffer.
func (r *reader) rest() []byte {
	n := len(r.b) - r.off
	if n == 0 {
		r.off = len(r.b)
		return nil
	}
	out := make([]byte, n)
	copy(out, r.b[r.off:])
	r.off = len(r.b)
	return out
}

func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}
