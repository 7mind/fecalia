package device

import (
	"encoding/binary"

	"github.com/amnezia-vpn/amneziawg-go/v3/conn"
)

const (
	flowSourceOffset      = 2
	flowDestinationOffset = 18
	flowPortsOffset       = 34
	ipProtocolTCP         = 6
	ipProtocolUDP         = 17
)

func packetMetadata(packet []byte) conn.PacketMetadata {
	return conn.PacketMetadata{Flow: packetFlow(packet), ACK: packetACK(packet)}
}

func packetACK(packet []byte) (ack conn.TCPACK) {
	if len(packet) < 20 {
		return
	}
	var offset, length int
	switch packet[0] >> 4 {
	case 4:
		if packet[0] != 0x45 || packet[9] != ipProtocolTCP || binary.BigEndian.Uint16(packet[6:8])&0x3fff != 0 {
			return
		}
		offset, length = 20, int(binary.BigEndian.Uint16(packet[2:4]))
		ack.TrafficClass = packet[1]
	case 6:
		if len(packet) < 40 || packet[6] != ipProtocolTCP {
			return
		}
		offset, length = 40, 40+int(binary.BigEndian.Uint16(packet[4:6]))
		ack.TrafficClass = (packet[0]&15)<<4 | packet[1]>>4
	default:
		return
	}
	if length > len(packet) || length < offset+20 {
		return
	}
	tcp := packet[offset:length]
	if int(tcp[12]>>4)*4 != len(tcp) || tcp[12]&15 != 0 || tcp[13] != 0x10 || binary.BigEndian.Uint16(tcp[18:20]) != 0 {
		return
	}
	ack.Sequence = binary.BigEndian.Uint32(tcp[4:8])
	ack.Acknowledgement = binary.BigEndian.Uint32(tcp[8:12])
	ack.Window = binary.BigEndian.Uint16(tcp[14:16])
	if ack.Window == 0 {
		return
	}
	for options := tcp[20:]; len(options) != 0; {
		switch options[0] {
		case 0:
			for _, padding := range options {
				if padding != 0 {
					return
				}
			}
			options = nil
		case 1:
			options = options[1:]
		case 8:
			if ack.Timestamp || len(options) < 10 || options[1] != 10 {
				return
			}
			ack.Timestamp = true
			ack.TSVal = binary.BigEndian.Uint32(options[2:6])
			ack.TSEcr = binary.BigEndian.Uint32(options[6:10])
			options = options[10:]
		default:
			return
		}
	}
	ack.Eligible = true
	return
}

// Flow identities use the IP addresses, terminal protocol and TCP/UDP ports.
// Fragments and unrecognised extension headers share an address/protocol queue;
// classification never changes the packet or its forwarding eligibility.
func packetFlow(packet []byte) (flow conn.FlowID) {
	if len(packet) == 0 {
		return
	}
	var offset int
	switch packet[0] >> 4 {
	case 4:
		if len(packet) < 20 {
			return
		}
		offset = int(packet[0]&15) * 4
		length := int(binary.BigEndian.Uint16(packet[2:4]))
		if offset < 20 || length < offset || length > len(packet) {
			return
		}
		packet = packet[:length]
		flow[0], flow[1] = 4, packet[9]
		copy(flow[flowSourceOffset:], packet[12:16])
		copy(flow[flowDestinationOffset:], packet[16:20])
		if binary.BigEndian.Uint16(packet[6:8])&0x3fff != 0 {
			return
		}
	case 6:
		if len(packet) < 40 {
			return
		}
		length := 40 + int(binary.BigEndian.Uint16(packet[4:6]))
		if length > len(packet) {
			return
		}
		packet = packet[:length]
		flow[0], flow[1] = 6, packet[6]
		copy(flow[flowSourceOffset:], packet[8:24])
		copy(flow[flowDestinationOffset:], packet[24:40])
		offset = 40
		for flow[1] == 0 || flow[1] == 43 || flow[1] == 60 {
			if offset+2 > len(packet) {
				return
			}
			next := offset + (int(packet[offset+1])+1)*8
			if next > len(packet) {
				return
			}
			flow[1], offset = packet[offset], next
		}
	default:
		return
	}
	if (flow[1] == ipProtocolTCP || flow[1] == ipProtocolUDP) && offset+4 <= len(packet) {
		copy(flow[flowPortsOffset:], packet[offset:offset+4])
	}
	return
}
