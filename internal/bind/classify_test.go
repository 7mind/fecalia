package bind

import (
	"encoding/binary"
	"testing"

	"github.com/7mind/wanbond/internal/sched"
)

// wgMsg builds a synthetic WireGuard message of the given type and total length: the
// leading little-endian uint32 type word followed by zero padding to length.
func wgMsg(msgType uint32, length int) []byte {
	b := make([]byte, length)
	if length >= wgTypeLen {
		binary.LittleEndian.PutUint32(b[:wgTypeLen], msgType)
	}
	return b
}

// TestClassifyWireGuard verifies the frame-type -> pacer-class mapping (defect D22):
// handshake init/response and cookie reply are control; a transport message is control
// ONLY at the keepalive size and data otherwise; malformed/unknown frames are data.
func TestClassifyWireGuard(t *testing.T) {
	cases := []struct {
		name string
		pkt  []byte
		want sched.FrameClass
	}{
		{"handshake initiation", wgMsg(wgMessageInitiationType, 148), sched.ClassControl},
		{"handshake response", wgMsg(wgMessageResponseType, 92), sched.ClassControl},
		{"cookie reply", wgMsg(wgMessageCookieReplyType, 64), sched.ClassControl},
		{"keepalive (empty transport)", wgMsg(wgMessageTransportType, wgKeepaliveSize), sched.ClassControl},
		{"transport data (one MTU)", wgMsg(wgMessageTransportType, 1420), sched.ClassData},
		{"transport just over keepalive", wgMsg(wgMessageTransportType, wgKeepaliveSize+1), sched.ClassData},
		{"unknown type", wgMsg(99, 100), sched.ClassData},
		{"too short for a type word", []byte{1, 0}, sched.ClassData},
		{"empty", nil, sched.ClassData},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := classifyWireGuard(c.pkt); got != c.want {
				t.Fatalf("classifyWireGuard(%s) = %d, want %d", c.name, got, c.want)
			}
		})
	}
}

// TestClassifyBatch verifies the batch classification: control iff ANY buffer is a
// control frame; an all-data (or empty) batch is data.
func TestClassifyBatch(t *testing.T) {
	data := wgMsg(wgMessageTransportType, 1420)
	ctrl := wgMsg(wgMessageInitiationType, 148)

	if got := classifyBatch([][]byte{data, data}); got != sched.ClassData {
		t.Fatalf("all-data batch = %d, want ClassData", got)
	}
	if got := classifyBatch([][]byte{data, ctrl, data}); got != sched.ClassControl {
		t.Fatalf("mixed batch with a control frame = %d, want ClassControl", got)
	}
	if got := classifyBatch(nil); got != sched.ClassData {
		t.Fatalf("empty batch = %d, want ClassData", got)
	}
}
