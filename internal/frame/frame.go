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

// DataOverhead is the number of bytes a KindData frame adds on top of its opaque
// payload on the wire: the clear nonce plus the DATA header (kind || outer-seq ||
// path-id || fec-group || fec-index || flags). DATA frames are unauthenticated
// (see the wire model above), so they carry no tag; this figure is therefore exact
// and is what the multipath Bind subtracts from the path MTU when sizing the inner
// tunnel (see internal/bind mtu.go). The fec-index byte is carried on every DATA
// frame — 0 and inert when FEC is disabled (T24) — so the codec's wire layout is
// invariant to the FEC toggle.
const DataOverhead = nonceLen + // clear nonce
	1 + // kind discriminant
	8 + // outer-seq (uint64)
	1 + // path-id (uint8)
	4 + // fec-group (uint32)
	1 + // fec-index (uint8): shard position within the FEC group (T24)
	1 // flags (uint8)

// ParityOverhead is the number of bytes a KindParity frame adds on top of its
// shard payload on the wire: the clear nonce plus the PARITY header (kind ||
// fec-group || parity-index || data-count || path-id). PARITY, like DATA, is
// unauthenticated and carries no tag, so this figure is exact. The multipath Bind
// uses it to size the FEC parity-overhead MTU penalty (T24): a full-size parity
// frame is 5 bytes larger on the wire than a full-size DATA frame carrying the same
// inner payload, so with FEC enabled the inner MTU is reduced so BOTH fit the path
// MTU without fragmentation (see internal/bind mtu.go).
const ParityOverhead = nonceLen + // clear nonce
	1 + // kind discriminant
	4 + // fec-group (uint32)
	2 + // parity-index (uint16)
	1 + // data-count (uint8)
	1 // path-id (uint8)

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
// sequence space (never the inner WG counter); PathID/FECGroup/FECIndex/Flags feed
// the multipath scheduler (T12), FEC (T14/T24), and resequencer (T18). FECIndex is
// the data shard's position within its FEC group (0..M-1); it is 0 and inert when
// FEC is disabled. The receiver reconstructs the FEC data-shard bytes as
// OuterSeq || Payload, so a shard recovered from parity carries its own outer-seq
// (T24) — no separate per-group base state is needed to resequence a recovered
// frame.
type Data struct {
	OuterSeq uint64
	PathID   uint8
	FECGroup uint32
	FECIndex uint8
	Flags    uint8
	Payload  []byte
}

// Parity carries one FEC parity symbol for a fec-group. DataCount is the group
// cardinality M (the number of data shards the parity protects); the decoder learns
// M from any surviving parity frame, so it must ride the wire (T24).
type Parity struct {
	FECGroup    uint32
	ParityIndex uint16
	DataCount   uint8
	PathID      uint8
	Payload     []byte
}

// Probe is an authenticated path probe used for RTT and liveness estimation.
//
// IsEcho is the request/response discriminant. A probe emitted by an originator
// (telemetry.Prober.SendProbe) carries IsEcho=false; the responder reflects it
// verbatim with IsEcho=true (telemetry.Reflector.Reflect). The two are otherwise
// byte-for-body-identical, so the receiving transport needs this bit to route an
// inbound frame: IsEcho=false is a peer probe to REFLECT, IsEcho=true is an echo
// of our own probe to FEED into that path's Prober. Marking the echo also breaks
// the otherwise-unbounded reflect-of-a-reflect loop (an echo is never reflected).
//
// SessionID is the originator's random per-boot session identity (T38, defect
// D12). It sits inside the MAC-covered body (adjacent to IsEcho), so an attacker
// can neither forge nor flip it. The responder reflects it verbatim. It TAGS the
// session (which boot the probe stream belongs to); on its own it is NOT proof of
// freshness (a captured probe carries a "never-seen" SessionID too), so the
// session-epoch reset is gated on the Challenge below, never on the SessionID
// merely being novel.
//
// Challenge is the responder-contributed freshness token (T38 redesign, defect
// D12). Like SessionID it lives inside obf(body) under the MAC, so it is both
// confidential (only a PSK holder can read it) and unforgeable. Its meaning is
// keyed by IsEcho:
//
//   - On an ECHO (IsEcho=true, emitted by the Reflector): Challenge is the
//     responder's CURRENT per-path issued challenge — a fresh random value the
//     originator must echo back to prove liveness.
//   - On a PROBE (IsEcho=false, emitted by the originator): Challenge is the last
//     issued challenge the originator learned from an echo (zero before it has
//     seen any). The responder adopts a new session epoch (resetting its
//     anti-replay high-water) ONLY when this equals its live issued challenge, so
//     a replayed probe — which can never carry the responder's current challenge —
//     can never seize the epoch or lock out the live peer.
type Probe struct {
	PathID         uint8
	ProbeSeq       uint64
	TimestampNanos int64
	IsEcho         bool
	SessionID      uint64
	Challenge      uint64
	Payload        []byte
}

// Control is an authenticated out-of-band control frame.
//
// Seq is a strictly-monotonic per-(peer, ControlType) sequence number: the
// freshness material the STATEFUL control-handling layer (telemetry.ControlGuard)
// uses to reject replays of a security-relevant control message (defect D4). The
// codec (Decode) verifies only the PSK HMAC and keeps no per-peer state, so a
// passively-captured valid control frame — e.g. a rekey — replays with a passing
// MAC; the downstream guard tracks a per-type high-water on Seq and drops the
// replay. Seq rides INSIDE obf(body) under the MAC (like Probe.ProbeSeq), so an
// attacker can neither read it nor forge/advance it to smuggle a replay past the
// guard.
type Control struct {
	ControlType uint8
	Seq         uint64
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
	dst = append(dst, f.FECIndex)
	dst = append(dst, f.Flags)
	return append(dst, f.Payload...)
}

func (f Parity) appendBody(dst []byte) []byte {
	dst = append(dst, byte(KindParity))
	dst = binary.BigEndian.AppendUint32(dst, f.FECGroup)
	dst = binary.BigEndian.AppendUint16(dst, f.ParityIndex)
	dst = append(dst, f.DataCount)
	dst = append(dst, f.PathID)
	return append(dst, f.Payload...)
}

func (f Probe) appendBody(dst []byte) []byte {
	dst = append(dst, byte(KindProbe))
	dst = append(dst, f.PathID)
	dst = binary.BigEndian.AppendUint64(dst, f.ProbeSeq)
	dst = binary.BigEndian.AppendUint64(dst, uint64(f.TimestampNanos))
	dst = append(dst, boolByte(f.IsEcho))
	dst = binary.BigEndian.AppendUint64(dst, f.SessionID)
	dst = binary.BigEndian.AppendUint64(dst, f.Challenge)
	return append(dst, f.Payload...)
}

// boolByte encodes a bool as a single wire byte (0 or 1).
func boolByte(b bool) byte {
	if b {
		return 1
	}
	return 0
}

func (f Control) appendBody(dst []byte) []byte {
	dst = append(dst, byte(KindControl))
	dst = append(dst, f.ControlType)
	dst = binary.BigEndian.AppendUint64(dst, f.Seq)
	return append(dst, f.Payload...)
}

// ErrMalformed is returned by Decode for any input too short or structurally
// invalid to be a frame.
var ErrMalformed = errors.New("frame: malformed input")

// ErrAuth is returned by Decode when an authenticated frame fails its MAC check
// (tampered ciphertext or PSK mismatch).
var ErrAuth = errors.New("frame: authentication failed")

// Codec is a reusable, PSK-bound frame encoder/decoder. It derives the HKDF
// obfuscation and authentication subkeys ONCE at construction and reuses them
// (plus per-call scratch buffers) across every Encode/Decode, and it inits the
// XChaCha20 keystream exactly once per frame. This resolves defect D5: the old
// package-level Encode/Decode re-derived both subkeys with HKDF-SHA256 and
// double-init'd ChaCha20 (once in peekByte, once for the full body) on EVERY
// frame — prohibitive on the per-datagram datapath the multipath Bind drives.
//
// A Codec is NOT safe for concurrent use: its scratch buffers are shared across
// calls. Construct one per goroutine (the Bind gives each per-path receive loop
// its own Codec and guards the shared send Codec with the Bind mutex).
type Codec struct {
	obfKey     []byte
	authKey    []byte
	encScratch []byte // reused body buffer for Encode
	decScratch []byte // reused body buffer for Decode
}

// NewCodec derives the PSK-bound subkeys once and returns a reusable Codec. It
// fails only if the PSK is unset.
func NewCodec(psk config.Key) (*Codec, error) {
	obfKey, authKey, err := subkeys(psk)
	if err != nil {
		return nil, err
	}
	return &Codec{obfKey: obfKey, authKey: authKey}, nil
}

// Encode appends the wire encoding of f to dst and returns the extended slice,
// letting the caller reuse one buffer across sends (pass dst[:0]). It fails only
// if the system CSPRNG is unavailable.
func (c *Codec) Encode(dst []byte, f Frame) ([]byte, error) {
	// Build the plaintext body (kind || header || payload) in reusable scratch.
	c.encScratch = f.appendBody(c.encScratch[:0])
	body := c.encScratch

	start := len(dst)
	dst = append(dst, make([]byte, nonceLen)...)
	nonce := dst[start : start+nonceLen]
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("frame: read nonce: %w", err)
	}

	// Obfuscate the body in scratch, then append it; a single keystream init.
	obfuscate(c.obfKey, nonce, body)
	dst = append(dst, body...)

	if f.Kind().authenticated() {
		// After the append above, nonce and obfBody are contiguous in dst's
		// (possibly reallocated) backing array; tag over nonce||obfBody.
		nonce = dst[start : start+nonceLen]
		obfBody := dst[start+nonceLen:]
		dst = append(dst, tag(c.authKey, nonce, obfBody)...)
	}
	return dst, nil
}

// Decode parses a wire frame. It verifies the MAC of authenticated frames and
// rejects tampered or PSK-mismatched ones. It never panics on malformed input.
// The returned frame's payload is a fresh copy, so the caller may reuse raw and
// the Codec's scratch immediately.
//
// Authentication guarantee: a frame is accepted as an authenticated kind
// (CONTROL/PROBE) only if its MAC verifies under the PSK, so tampered or
// PSK-mismatched authenticated frames are rejected. Flipping the (obfuscated)
// kind byte can re-label an authenticated frame as an unauthenticated kind
// (DATA/PARITY), which then decodes without a MAC — this is not a downgrade
// break, because DATA/PARITY are forgeable by design (the inner WireGuard layer
// authenticates the real payload). No mutation can make a frame decode as an
// authentic CONTROL/PROBE.
func (c *Codec) Decode(raw []byte) (Frame, error) {
	// Need at least the nonce plus one body byte (the kind discriminant).
	if len(raw) < nonceLen+1 {
		return nil, fmt.Errorf("%w: %d bytes, need >= %d", ErrMalformed, len(raw), nonceLen+1)
	}

	nonce := raw[:nonceLen]
	rest := raw[nonceLen:]

	// One keystream for the whole decode: init the cipher once, consume the
	// first keystream byte to recover the (obfuscated) kind, then continue the
	// SAME keystream over the remaining body. This avoids the old peekByte's
	// second ChaCha20 init.
	cipher, err := chacha20.NewUnauthenticatedCipher(c.obfKey, nonce)
	if err != nil {
		// obfKey is subkeyLen and nonce is nonceLen, so init cannot fail; a
		// failure indicates a programmer error.
		panic(fmt.Sprintf("frame: chacha20 init: %v", err))
	}
	kindByte := []byte{rest[0]}
	cipher.XORKeyStream(kindByte, kindByte)
	kind := Kind(kindByte[0])
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
		wantTag := tag(c.authKey, nonce, obfBody)
		if !hmac.Equal(gotTag, wantTag) {
			return nil, ErrAuth
		}
	}

	// Reconstruct the plaintext body in reusable scratch. Byte 0 is the kind we
	// already recovered; the remaining bytes continue the same keystream.
	c.decScratch = append(c.decScratch[:0], obfBody...)
	body := c.decScratch
	body[0] = kindByte[0]
	if len(body) > 1 {
		cipher.XORKeyStream(body[1:], body[1:])
	}
	return decodeBody(kind, body[1:])
}

// Encode serializes f into a self-contained wire frame under the given PSK. It
// is a convenience wrapper that builds a one-shot Codec; the per-datagram
// datapath uses a long-lived Codec instead (see Codec / defect D5). It fails
// only if the PSK is unset or the system CSPRNG is unavailable.
func Encode(psk config.Key, f Frame) ([]byte, error) {
	c, err := NewCodec(psk)
	if err != nil {
		return nil, err
	}
	return c.Encode(nil, f)
}

// Decode parses a wire frame under the given PSK. It is a convenience wrapper
// that builds a one-shot Codec; hot paths reuse a long-lived Codec instead.
func Decode(psk config.Key, raw []byte) (Frame, error) {
	c, err := NewCodec(psk)
	if err != nil {
		return nil, err
	}
	return c.Decode(raw)
}

// decodeBody parses the header+payload region (the body after the kind byte).
func decodeBody(kind Kind, b []byte) (Frame, error) {
	r := reader{b: b}
	switch kind {
	case KindData:
		seq, e1 := r.u64()
		pathID, e2 := r.u8()
		group, e3 := r.u32()
		fecIndex, e4 := r.u8()
		flags, e5 := r.u8()
		if err := firstErr(e1, e2, e3, e4, e5); err != nil {
			return nil, err
		}
		return Data{OuterSeq: seq, PathID: pathID, FECGroup: group, FECIndex: fecIndex, Flags: flags, Payload: r.rest()}, nil
	case KindParity:
		group, e1 := r.u32()
		idx, e2 := r.u16()
		dataCount, e3 := r.u8()
		pathID, e4 := r.u8()
		if err := firstErr(e1, e2, e3, e4); err != nil {
			return nil, err
		}
		return Parity{FECGroup: group, ParityIndex: idx, DataCount: dataCount, PathID: pathID, Payload: r.rest()}, nil
	case KindProbe:
		pathID, e1 := r.u8()
		probeSeq, e2 := r.u64()
		ts, e3 := r.u64()
		echo, e4 := r.u8()
		sessionID, e5 := r.u64()
		challenge, e6 := r.u64()
		if err := firstErr(e1, e2, e3, e4, e5, e6); err != nil {
			return nil, err
		}
		return Probe{PathID: pathID, ProbeSeq: probeSeq, TimestampNanos: int64(ts), IsEcho: echo != 0, SessionID: sessionID, Challenge: challenge, Payload: r.rest()}, nil
	case KindControl:
		ctype, e1 := r.u8()
		seq, e2 := r.u64()
		if err := firstErr(e1, e2); err != nil {
			return nil, err
		}
		return Control{ControlType: ctype, Seq: seq, Payload: r.rest()}, nil
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
