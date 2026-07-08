package bind

import (
	"encoding/binary"

	"github.com/7mind/wanbond/internal/sched"
)

// WireGuard message-type discriminants and the keepalive size, mirrored from
// amneziawg-go/device (noise-protocol.go) rather than imported: the bind depends on
// the conn package only, and pulling the whole device package in for four constants
// would couple the transport to the engine's platform/init machinery. The type is the
// little-endian uint32 in the first four bytes of every WireGuard message; the high
// three bytes are zero for a valid type. Kept in lockstep with device.Message*Type /
// device.MessageKeepaliveSize (the packages cannot sensibly import each other here, so
// the values are mirrored with this cross-reference).
const (
	wgMessageInitiationType  uint32 = 1 // handshake initiation (device.MessageInitiationType)
	wgMessageResponseType    uint32 = 2 // handshake response (device.MessageResponseType)
	wgMessageCookieReplyType uint32 = 3 // cookie reply (device.MessageCookieReplyType)
	wgMessageTransportType   uint32 = 4 // transport data / keepalive (device.MessageTransportType)

	// wgTypeLen is the length of the leading little-endian uint32 message type.
	wgTypeLen = 4
	// wgKeepaliveSize is the on-wire size of a WireGuard keepalive: a transport message
	// with EMPTY content — MessageTransportHeaderSize (16) + poly1305 tag (16). A
	// transport message longer than this carries tunnelled payload and is bulk data.
	// Mirrors device.MessageKeepaliveSize (== MessageTransportSize).
	wgKeepaliveSize = 32
)

// classifyWireGuard maps one outbound WireGuard message to its pacer traffic class
// (defect D22). Handshake initiation/response and cookie reply are control frames; a
// transport message is a control frame ONLY when it is a keepalive (empty content,
// exactly wgKeepaliveSize bytes) and otherwise bulk data. A buffer too short to hold a
// type word, or an unrecognised type, is treated as data (fail toward pacing — an
// unknown frame must not gain the control-class bypass).
func classifyWireGuard(pkt []byte) sched.FrameClass {
	if len(pkt) < wgTypeLen {
		return sched.ClassData
	}
	switch binary.LittleEndian.Uint32(pkt[:wgTypeLen]) {
	case wgMessageInitiationType, wgMessageResponseType, wgMessageCookieReplyType:
		return sched.ClassControl
	case wgMessageTransportType:
		if len(pkt) == wgKeepaliveSize {
			return sched.ClassControl
		}
		return sched.ClassData
	default:
		return sched.ClassData
	}
}

// classifyBatch returns the pacer traffic class for a batch of WireGuard datagrams
// handed to Send in one call. The batch is classified ClassControl when ANY buffer is
// a control frame: the batch shares one Pick (one path selection / one pacing
// decision), so biasing a mixed batch toward the control class keeps a co-batched
// handshake/keepalive out of the shed set — the conservative, rekey-favouring choice.
// In practice WireGuard emits control frames in their own Send calls, so a mixed batch
// is rare. An empty batch is data (no control frame to protect).
func classifyBatch(bufs [][]byte) sched.FrameClass {
	for _, b := range bufs {
		if classifyWireGuard(b) == sched.ClassControl {
			return sched.ClassControl
		}
	}
	return sched.ClassData
}
