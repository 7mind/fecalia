package bind

import (
	"encoding/binary"
	"testing"

	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/sched"
)

// wgMsg builds a synthetic WireGuard message: junk prefix bytes, then the little-endian
// uint32 type word at offset junk, padded to total length. It models both vanilla frames
// (junk=0) and AmneziaWG handshakes whose type word is shifted behind an s1/s2 junk
// prefix.
func wgMsg(msgType uint32, junk, total int) []byte {
	b := make([]byte, total)
	// Fill the junk prefix with a non-zero, non-matching pattern so a naive read at
	// offset 0 does NOT accidentally see a valid type word.
	for i := 0; i < junk && i < total; i++ {
		b[i] = 0xAB
	}
	if junk+wgTypeLen <= total {
		binary.LittleEndian.PutUint32(b[junk:junk+wgTypeLen], msgType)
	}
	return b
}

// TestClassifyWireGuardVanilla verifies the frame-type -> pacer-class mapping in vanilla
// mode (defect D22): handshake init/response and cookie reply are control; a transport
// message is control ONLY at the keepalive size and data otherwise; malformed/unknown
// frames are data.
func TestClassifyWireGuardVanilla(t *testing.T) {
	c := newWGClassifier(config.Amnezia{}) // zero profile == vanilla
	cases := []struct {
		name string
		pkt  []byte
		want sched.FrameClass
	}{
		{"handshake initiation", wgMsg(wgMessageInitiationType, 0, 148), sched.ClassControl},
		{"handshake response", wgMsg(wgMessageResponseType, 0, 92), sched.ClassControl},
		{"cookie reply", wgMsg(wgMessageCookieReplyType, 0, 64), sched.ClassControl},
		{"keepalive (empty transport)", wgMsg(wgMessageTransportType, 0, wgKeepaliveSize), sched.ClassControl},
		{"transport data (one MTU)", wgMsg(wgMessageTransportType, 0, 1420), sched.ClassData},
		{"transport just over keepalive", wgMsg(wgMessageTransportType, 0, wgKeepaliveSize+1), sched.ClassData},
		{"unknown type", wgMsg(99, 0, 100), sched.ClassData},
		{"too short for a type word", []byte{1, 0}, sched.ClassData},
		{"empty", nil, sched.ClassData},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := c.classify(tc.pkt); got != tc.want {
				t.Fatalf("classify(%s) = %d, want %d", tc.name, got, tc.want)
			}
		})
	}
}

// asecProfile is a representative AmneziaWG advanced-security profile: custom magic
// headers (all > 4 and distinct, so amneziawg-go actually installs them as the wire type
// words) and non-zero handshake junk prefixes s1/s2 (init 148+s1 != response 92+s2, per
// the engine's size-distinctness rule).
func asecProfile() config.Amnezia {
	return config.Amnezia{
		Jc: 4, Jmin: 8, Jmax: 80,
		S1: 15, S2: 23,
		H1: 0x11111111, H2: 0x22222222, H3: 0x33333333, H4: 0x44444444,
	}
}

// TestClassifyWireGuardAmneziaControlFrames is the defect-D22 regression test for
// wanbond's PRIMARY deployment mode. Under AmneziaWG the type word is a custom magic
// header (h1..h4) and handshakes carry an s1/s2 junk PREFIX, so a hardcoded vanilla
// classifier (literal 1/2/3/4 at offset 0) misses every control frame and pacing stays
// frame-type-blind. The profile-parameterized classifier must still recognise them.
func TestClassifyWireGuardAmneziaControlFrames(t *testing.T) {
	a := asecProfile()
	c := newWGClassifier(a)

	// Handshake initiation: s1 junk bytes, then the h1 magic type word.
	initPkt := wgMsg(a.H1, a.S1, a.S1+148)
	if got := c.classify(initPkt); got != sched.ClassControl {
		t.Fatalf("ASec handshake initiation classified %d, want ClassControl (defect D22: junk-shifted h1 header missed)", got)
	}
	// Handshake response: s2 junk bytes, then the h2 magic type word.
	respPkt := wgMsg(a.H2, a.S2, a.S2+92)
	if got := c.classify(respPkt); got != sched.ClassControl {
		t.Fatalf("ASec handshake response classified %d, want ClassControl (defect D22: junk-shifted h2 header missed)", got)
	}
	// Cookie reply: h3 magic type word at offset 0 (no junk prefix).
	cookiePkt := wgMsg(a.H3, 0, 64)
	if got := c.classify(cookiePkt); got != sched.ClassControl {
		t.Fatalf("ASec cookie reply classified %d, want ClassControl (defect D22: custom h3 header missed)", got)
	}
	// Keepalive: h4 transport magic at offset 0, keepalive-sized (transport is NOT
	// junk-prefixed under AmneziaWG, so it stays wgKeepaliveSize).
	kaPkt := wgMsg(a.H4, 0, wgKeepaliveSize)
	if got := c.classify(kaPkt); got != sched.ClassControl {
		t.Fatalf("ASec keepalive classified %d, want ClassControl (defect D22: custom h4 transport header missed)", got)
	}
}

// TestClassifyWireGuardAmneziaDataStaysData verifies the safety property under ASec: a
// bulk transport data frame (h4 magic, larger than keepalive) is never mistaken for a
// control frame, so the exemption cannot be abused to bypass pacing for data.
func TestClassifyWireGuardAmneziaDataStaysData(t *testing.T) {
	a := asecProfile()
	c := newWGClassifier(a)

	dataPkt := wgMsg(a.H4, 0, 1420) // transport, tunnelled payload
	if got := c.classify(dataPkt); got != sched.ClassData {
		t.Fatalf("ASec transport data classified %d, want ClassData (data must never be exempted)", got)
	}
	// Smallest possible non-keepalive transport (48 bytes: 16 header + 16 padded content
	// + 16 tag) must still be data.
	minData := wgMsg(a.H4, 0, 48)
	if got := c.classify(minData); got != sched.ClassData {
		t.Fatalf("ASec minimal transport data (48B) classified %d, want ClassData", got)
	}
	// An AmneziaWG junk cover packet (random leading bytes matching no configured type at
	// any expected offset) classifies as data.
	junkPkt := wgMsg(0xDEADBEEF, 0, 120)
	if got := c.classify(junkPkt); got != sched.ClassData {
		t.Fatalf("ASec junk cover packet classified %d, want ClassData", got)
	}
}

// TestClassifyMagicHeaderActivationRule checks the mirror of amneziawg-go's rule that a
// magic header takes effect only when > 4: a configured header of 1..4 is a no-op and the
// default type word stands, so the classifier must keep matching the default.
func TestClassifyMagicHeaderActivationRule(t *testing.T) {
	// H4 <= 4, so the engine keeps MessageTransportType at the default 4; a keepalive is
	// still emitted with the DEFAULT transport word.
	a := config.Amnezia{Jc: 4, Jmin: 8, Jmax: 80, S1: 15, S2: 23, H1: 1, H2: 2, H3: 3, H4: 4}
	c := newWGClassifier(a)
	if got := c.classify(wgMsg(wgMessageTransportType, 0, wgKeepaliveSize)); got != sched.ClassControl {
		t.Fatalf("keepalive with default transport word classified %d, want ClassControl (header 1..4 must be a no-op, default stands)", got)
	}
}

// TestClassifyBatch verifies batch classification: control iff ANY buffer is a control
// frame; an all-data (or empty) batch is data.
func TestClassifyBatch(t *testing.T) {
	c := newWGClassifier(config.Amnezia{})
	data := wgMsg(wgMessageTransportType, 0, 1420)
	ctrl := wgMsg(wgMessageInitiationType, 0, 148)

	if got := c.classifyBatch([][]byte{data, data}); got != sched.ClassData {
		t.Fatalf("all-data batch = %d, want ClassData", got)
	}
	if got := c.classifyBatch([][]byte{data, ctrl, data}); got != sched.ClassControl {
		t.Fatalf("mixed batch with a control frame = %d, want ClassControl", got)
	}
	if got := c.classifyBatch(nil); got != sched.ClassData {
		t.Fatalf("empty batch = %d, want ClassData", got)
	}
}
