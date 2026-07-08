package bind

import (
	"encoding/binary"

	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/sched"
)

// Default WireGuard message-type discriminants and the keepalive size, mirrored from
// amneziawg-go/device (noise-protocol.go) rather than imported: the bind depends on the
// conn package only, and pulling the whole device package in for these constants would
// couple the transport to the engine's platform/init machinery. The type is the
// little-endian uint32 at the head of the message; the keepalive is a transport message
// with empty content — MessageTransportHeaderSize (16) + poly1305 tag (16). Kept in
// lockstep with device.Default*Type / device.MessageKeepaliveSize.
const (
	wgMessageInitiationType  uint32 = 1 // handshake initiation (device.DefaultMessageInitiationType)
	wgMessageResponseType    uint32 = 2 // handshake response (device.DefaultMessageResponseType)
	wgMessageCookieReplyType uint32 = 3 // cookie reply (device.DefaultMessageCookieReplyType)
	wgMessageTransportType   uint32 = 4 // transport data / keepalive (device.DefaultMessageTransportType)

	// wgTypeLen is the length of the leading little-endian uint32 message type.
	wgTypeLen = 4
	// wgKeepaliveSize is the on-wire size of a WireGuard keepalive (device.MessageKeepaliveSize
	// == MessageTransportSize). AmneziaWG does NOT junk-prefix transport frames, so a
	// keepalive stays this size under obfuscation; a transport frame longer than this
	// carries tunnelled payload and is bulk data.
	wgKeepaliveSize = 32
	// wgMagicHeaderMin is amneziawg-go's threshold for a custom magic header taking effect:
	// device.go applies a configured header only when it is > 4, otherwise the default type
	// word stands (so a header of 1..4 is a no-op and the wire word is the default). The
	// classifier mirrors that rule exactly so it matches what is actually on the wire.
	wgMagicHeaderMin uint32 = 4
)

// wgClassifier maps an outbound WireGuard datagram to its pacer traffic class (defect
// D22), parameterized by the tunnel's AmneziaWG obfuscation profile so classification is
// correct in advanced-security mode — wanbond's PRIMARY deployment — and not only in
// vanilla WireGuard. AmneziaWG breaks a hardcoded classifier two ways (amneziawg-go
// send.go): (a) custom magic headers h1..h4 replace the literal 1/2/3/4 type words, and
// (b) s1/s2 junk bytes are PREPENDED to handshake initiation/response datagrams, shifting
// the type word off offset 0. This classifier holds the CONFIGURED type words and junk
// prefix lengths and reads the type word at the right offset for each candidate type.
//
// A zero (unconfigured) profile yields the vanilla classifier: default type words, no
// junk prefix.
type wgClassifier struct {
	initType      uint32 // effective handshake-initiation type word (h1, or default)
	responseType  uint32 // effective handshake-response type word (h2, or default)
	cookieType    uint32 // effective cookie-reply type word (h3, or default)
	transportType uint32 // effective transport (data/keepalive) type word (h4, or default)
	initJunk      int    // s1: junk bytes prepended before an initiation packet's type word
	responseJunk  int    // s2: junk bytes prepended before a response packet's type word
}

// newWGClassifier builds the classifier from the tunnel's Amnezia profile. It mirrors
// amneziawg-go's ">4 or default" magic-header rule (device.go) so the type words match
// the wire exactly, and takes s1/s2 as the initiation/response junk-prefix lengths. An
// unconfigured (zero-value) profile produces the vanilla classifier.
func newWGClassifier(a config.Amnezia) wgClassifier {
	return wgClassifier{
		initType:      effectiveMagic(a.H1, wgMessageInitiationType),
		responseType:  effectiveMagic(a.H2, wgMessageResponseType),
		cookieType:    effectiveMagic(a.H3, wgMessageCookieReplyType),
		transportType: effectiveMagic(a.H4, wgMessageTransportType),
		initJunk:      junkLen(a.S1),
		responseJunk:  junkLen(a.S2),
	}
}

// effectiveMagic returns the type word actually placed on the wire for a header: the
// configured value when it exceeds wgMagicHeaderMin (amneziawg-go's activation rule),
// otherwise the default. This matches device.go, where a header of 1..4 is ignored.
func effectiveMagic(configured, def uint32) uint32 {
	if configured > wgMagicHeaderMin {
		return configured
	}
	return def
}

// junkLen clamps a junk-prefix length to a non-negative offset (a zero/omitted profile
// has no prefix; config validation forbids a negative value).
func junkLen(s int) int {
	if s < 0 {
		return 0
	}
	return s
}

// classify maps one outbound WireGuard message to its pacer traffic class. Order matters
// for the safety property "DATA never classifies as control": a transport frame leads
// with the transport type word at offset 0 and is checked FIRST, so a bulk data frame
// (transport, size > keepalive) always resolves to ClassData before any control branch is
// considered. Handshake initiation/response type words are read at their junk-shifted
// offsets (s1/s2). A frame that matches nothing — including AmneziaWG junk packets — is
// data (fail toward pacing; an unrecognised frame must not gain the control-class bypass).
func (c wgClassifier) classify(pkt []byte) sched.FrameClass {
	// Transport (data or keepalive): type word at offset 0, never junk-prefixed. Only a
	// keepalive-sized transport is a control frame; anything larger is tunnelled bulk data.
	if t, ok := readType(pkt, 0); ok && t == c.transportType {
		if len(pkt) == wgKeepaliveSize {
			return sched.ClassControl
		}
		return sched.ClassData
	}
	// Cookie reply: type word at offset 0, not junk-prefixed.
	if t, ok := readType(pkt, 0); ok && t == c.cookieType {
		return sched.ClassControl
	}
	// Handshake initiation: type word after s1 junk bytes.
	if t, ok := readType(pkt, c.initJunk); ok && t == c.initType {
		return sched.ClassControl
	}
	// Handshake response: type word after s2 junk bytes.
	if t, ok := readType(pkt, c.responseJunk); ok && t == c.responseType {
		return sched.ClassControl
	}
	return sched.ClassData
}

// classifyBatch returns the pacer traffic class for a batch of WireGuard datagrams handed
// to Send in one call. The batch is classified ClassControl when ANY buffer is a control
// frame: the batch shares one Pick (one path selection / one pacing decision), so biasing
// a mixed batch toward the control class keeps a co-batched handshake/keepalive out of the
// shed set — the conservative, rekey-favouring choice. In practice WireGuard emits control
// frames in their own Send calls, so a mixed batch is rare. An empty batch is data.
func (c wgClassifier) classifyBatch(bufs [][]byte) sched.FrameClass {
	for _, b := range bufs {
		if c.classify(b) == sched.ClassControl {
			return sched.ClassControl
		}
	}
	return sched.ClassData
}

// readType reads the little-endian uint32 message-type word at byte offset off, reporting
// ok=false when the buffer is too short to hold a type word at that offset.
func readType(pkt []byte, off int) (uint32, bool) {
	if off < 0 || off+wgTypeLen > len(pkt) {
		return 0, false
	}
	return binary.LittleEndian.Uint32(pkt[off : off+wgTypeLen]), true
}
