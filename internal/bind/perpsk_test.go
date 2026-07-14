package bind

import (
	"testing"

	"github.com/7mind/wanbond/internal/frame"
)

// TestPeerStatePerPSKCodecAndReflector is the T84 acceptance: two peerStates built through the
// per-peer construction seam (newPeerState) from DISTINCT psks derive independent frame Codecs
// and probe Reflectors. One peer's authenticated frame is rejected by the other's Decode
// (cross-psk frames are rejected on the MAC), and each Reflector authenticates a probe ONLY
// under its own psk. This is the property the concentrator relies on: PSK is a per-peer
// attribute now, not a bond-wide singleton, so a frame minted under peer A's key cannot be
// mistaken for peer B's.
func TestPeerStatePerPSKCodecAndReflector(t *testing.T) {
	pskA := testKey(t, 0x11)
	pskB := testKey(t, 0x22)

	// The scheduler/prober collaborators are irrelevant to codec/reflector derivation, so this
	// exercises the PSK seam in isolation (nil is never dereferenced by newPeerState).
	peerA := newPeerState("peer-a", pskA, nil, nil, nil)
	peerB := newPeerState("peer-b", pskB, nil, nil, nil)

	// --- Codec: a frame encoded under peer A's psk fails peer B's Decode. ---
	encA, err := peerA.newCodec()
	if err != nil {
		t.Fatalf("peer A encode codec: %v", err)
	}
	decA, err := peerA.newCodec()
	if err != nil {
		t.Fatalf("peer A decode codec: %v", err)
	}
	decB, err := peerB.newCodec()
	if err != nil {
		t.Fatalf("peer B decode codec: %v", err)
	}

	// A PROBE frame is authenticated (MAC-covered under the peer's authKey). Peer A's own codec
	// round-trips it back to an authentic probe; peer B's codec, keyed by a DIFFERENT psk, can
	// NEVER recover it AS an authentic probe — reaching KindProbe requires both the obfuscated
	// kind byte to survive AND the MAC to verify under authKeyB, which is cryptographically
	// infeasible for a frame MAC'd under authKeyA. (Decode's exact rejection is ErrMalformed or
	// ErrAuth depending on how the random nonce garbles the kind byte under the wrong obfKey, so
	// the deterministic invariant asserted is "never accepted as a probe", not a specific error.)
	wire, err := encA.Encode(nil, frame.Probe{PathID: 0, ProbeSeq: 7, TimestampNanos: 1234, SessionID: 0xABCD})
	if err != nil {
		t.Fatalf("peer A encode: %v", err)
	}
	gotA, err := decA.Decode(wire)
	if err != nil {
		t.Fatalf("peer A must decode a frame minted under its own psk: %v", err)
	}
	if gotA.Kind() != frame.KindProbe {
		t.Fatalf("peer A decoded its own probe as kind %d, want KindProbe", gotA.Kind())
	}
	if gotB, err := decB.Decode(wire); err == nil && gotB.Kind() == frame.KindProbe {
		t.Fatalf("peer B accepted peer A's authenticated probe under a different psk: %+v", gotB)
	}

	// --- Reflector: a probe authenticates (and is reflected) ONLY under its own peer's psk. ---
	probeA, err := frame.Encode(pskA, frame.Probe{PathID: 0, ProbeSeq: 1, TimestampNanos: 99, SessionID: 0x1})
	if err != nil {
		t.Fatalf("encode probe under psk A: %v", err)
	}
	if _, err := peerA.reflector.Reflect(probeA); err != nil {
		t.Fatalf("peer A reflector must authenticate a probe under its own psk: %v", err)
	}
	// Under peer B's psk the probe never reflects: it fails the MAC, or (when the wrong obfKey
	// garbles the kind into an unauthenticated one) decodes to a non-probe the Reflector refuses.
	if _, err := peerB.reflector.Reflect(probeA); err == nil {
		t.Fatal("peer B reflector reflected a probe minted under psk A (cross-psk probe must be rejected)")
	}
}
