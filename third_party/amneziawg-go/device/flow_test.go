package device

import (
	"bytes"
	"encoding/binary"
	"sync"
	"testing"
	"time"

	"github.com/amnezia-vpn/amneziawg-go/conn"
	"github.com/amnezia-vpn/amneziawg-go/conn/bindtest"
)

type flowCaptureBind struct {
	conn.Bind
	enabled  bool
	mu       sync.Mutex
	metadata []conn.PacketMetadata
}

func TestPacketFlowIdentity(t *testing.T) {
	for _, version := range []byte{4, 6} {
		header := 20
		if version == 6 {
			header = 40
		}
		packet := make([]byte, header+8)
		packet[0] = version << 4
		if version == 4 {
			packet[0] |= 5
			packet[9] = 17
			binary.BigEndian.PutUint16(packet[2:4], uint16(len(packet)))
			copy(packet[12:20], []byte{192, 0, 2, 1, 192, 0, 2, 2})
		} else {
			packet[6] = 17
			binary.BigEndian.PutUint16(packet[4:6], 8)
			packet[8], packet[23], packet[24], packet[39] = 0x20, 1, 0x20, 2
		}
		copy(packet[header:], []byte{0x12, 0x34, 0x56, 0x78, 0, 8, 0, 0})
		original := bytes.Clone(packet)
		flow := packetFlow(packet)
		if flow == (conn.FlowID{}) || !bytes.Equal(packet, original) {
			t.Fatal("valid flow was unclassified or packet mutated")
		}
		packet[header+1]++
		if packetFlow(packet) == flow {
			t.Fatal("different UDP source ports shared a flow")
		}
		packet[header+1]--
		packet[header+3]++
		if packetFlow(packet) == flow {
			t.Fatal("different UDP destination ports shared a flow")
		}
		packet[header+3]--
		packet[header+7]++
		if packetFlow(packet) != flow {
			t.Fatal("checksum change created a new flow")
		}
		if version == 4 {
			packet[6] = 0x20 // first fragment, more fragments follow
			first := packetFlow(packet)
			packet[6], packet[7] = 0, 1 // later fragment with unrelated payload
			packet[header]++
			if first == flow || first != packetFlow(packet) {
				t.Fatal("fragments were classified as transport headers")
			}
		} else {
			extended := append(bytes.Clone(packet[:header]), make([]byte, 8)...)
			extended = append(extended, packet[header:]...)
			extended[6], extended[header] = 60, 17
			binary.BigEndian.PutUint16(extended[4:6], 16)
			if packetFlow(extended) != flow {
				t.Fatal("IPv6 destination options changed the UDP identity")
			}
		}
		for end := range len(original) {
			if packetFlow(original[:end]) != (conn.FlowID{}) {
				t.Fatal("truncated IP packet received a flow identity")
			}
		}
	}
}

func ackTestPacket() []byte {
	packet := make([]byte, 52)
	packet[0], packet[9] = 0x45, 6
	binary.BigEndian.PutUint16(packet[2:4], uint16(len(packet)))
	copy(packet[12:20], []byte{192, 0, 2, 1, 192, 0, 2, 2})
	copy(packet[20:24], []byte{0x12, 0x34, 0x56, 0x78})
	binary.BigEndian.PutUint32(packet[24:28], 123)
	binary.BigEndian.PutUint32(packet[28:32], 456)
	packet[32], packet[33] = 8<<4, 0x10
	binary.BigEndian.PutUint16(packet[34:36], 4096)
	copy(packet[40:], []byte{1, 1, 8, 10, 0, 0, 0, 42, 0, 0, 0, 21})
	return packet
}

func TestPureACKMetadata(t *testing.T) {
	packet := ackTestPacket()
	ack := packetMetadata(packet).ACK
	if !ack.Eligible || ack.Sequence != 123 || ack.Acknowledgement != 456 || ack.Window != 4096 || !ack.Timestamp || ack.TSVal != 42 || ack.TSEcr != 21 {
		t.Fatalf("pure TCP ACK metadata missing: %+v", ack)
	}
	v6 := make([]byte, 40)
	v6[0], v6[6] = 0x60, 6
	binary.BigEndian.PutUint16(v6[4:6], uint16(len(packet)-20))
	v6 = append(v6, packet[20:]...)
	if packetMetadata(v6).ACK != ack {
		t.Fatal("IPv6 acknowledgement has different TCP metadata")
	}
	for name, mutate := range map[string]func([]byte){
		"syn":             func(p []byte) { p[33] |= 2 },
		"fin":             func(p []byte) { p[33] |= 1 },
		"ecn":             func(p []byte) { p[33] |= 0x40 },
		"reserved":        func(p []byte) { p[32] |= 1 },
		"urgent":          func(p []byte) { p[39] = 1 },
		"fragment":        func(p []byte) { p[6] = 0x20 },
		"sack":            func(p []byte) { p[42] = 5 },
		"unknown-option":  func(p []byte) { p[42] = 30 },
		"short-timestamp": func(p []byte) { p[43] = 9 },
		"payload":         func(p []byte) { p[32] = 5 << 4 },
		"zero-window":     func(p []byte) { p[34], p[35] = 0, 0 },
	} {
		t.Run(name, func(t *testing.T) {
			p := bytes.Clone(packet)
			mutate(p)
			if packetMetadata(p).ACK.Eligible {
				t.Fatal("non-redundant TCP information eligible for ACK coalescing")
			}
		})
	}
}

func FuzzPacketMetadata(f *testing.F) {
	f.Add(ackTestPacket())
	f.Add([]byte{0x60, 0, 0, 0})
	f.Fuzz(func(t *testing.T, packet []byte) {
		original := bytes.Clone(packet)
		meta := packetMetadata(packet)
		if !bytes.Equal(original, packet) {
			t.Fatal("classification modified IP packet")
		}
		if meta.ACK.Eligible && (meta.Flow[0] != 4 && meta.Flow[0] != 6 || meta.Flow[1] != ipProtocolTCP) {
			t.Fatal("ACK eligibility without a complete TCP flow identity")
		}
	})
}

func (b *flowCaptureBind) PacketMetadataEnabled() bool { return b.enabled }

func (b *flowCaptureBind) SendWithMetadata(bufs [][]byte, metadata []conn.PacketMetadata, ep conn.Endpoint, complete func()) error {
	defer complete()
	b.mu.Lock()
	b.metadata = append(b.metadata, metadata...)
	b.mu.Unlock()
	return b.Bind.Send(bufs, ep)
}

// GC: plaintext traverses the real TUN/encryption/decryption pipeline while the
// Bind receives only ciphertext and a separate local identity for scheduling.
func TestEncryptedFlowMetadata(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run(map[bool]string{true: "enabled", false: "disabled"}[enabled], func(t *testing.T) {
			binds := bindtest.NewChannelBinds()
			capture := &flowCaptureBind{Bind: binds[1], enabled: enabled}
			binds[1] = capture
			pair := genTestPairWithBinds(t, binds)
			pair.Send(t, Ping, nil)
			packet := ackTestPacket()
			copy(packet[12:16], pair[1].ip.AsSlice())
			copy(packet[16:20], pair[0].ip.AsSlice())
			pair[1].tun.Outbound <- packet
			select {
			case received := <-pair[0].tun.Inbound:
				if !bytes.Equal(received, packet) {
					t.Fatal("TCP acknowledgement changed in encryption pipeline")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("TCP acknowledgement did not traverse encryption pipeline")
			}
			capture.mu.Lock()
			defer capture.mu.Unlock()
			if !enabled {
				if len(capture.metadata) != 0 {
					t.Fatal("disabled Bind received flow metadata")
				}
				return
			}
			for _, meta := range capture.metadata {
				flow := meta.Flow
				if flow[0] == 4 && flow[1] == 6 && meta.ACK.Eligible && meta.ACK.Acknowledgement == 456 && bytes.Equal(flow[2:6], pair[1].ip.AsSlice()) && bytes.Equal(flow[18:22], pair[0].ip.AsSlice()) {
					return
				}
			}
			t.Fatal("encrypted packet arrived, but its IP flow identity did not reach the Bind")
		})
	}
}
