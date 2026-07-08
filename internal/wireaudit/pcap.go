package wireaudit

import (
	"encoding/binary"
	"fmt"
)

// Classic libpcap savefile constants (the format `tcpdump -w` writes). The layout
// is trivial: a 24-byte global header, then a sequence of records each with a
// 16-byte header (ts_sec, ts_usec, incl_len, orig_len) followed by incl_len bytes
// of link-layer packet data. No external dependency (gopacket etc.) is warranted —
// parsing this by hand is a few dozen lines and keeps go.mod minimal.
const (
	pcapGlobalHeaderLen = 24
	pcapRecordHeaderLen = 16
	pcapInclLenOff      = 8  // offset of incl_len within a record header
	pcapLinkTypeOff     = 20 // offset of the link-type field in the global header

	// linkTypeEthernet (DLT_EN10MB) is the link type tcpdump reports for a veth.
	linkTypeEthernet = 1

	ethHeaderLen = 14
	ethTypeOff   = 12
	ethTypeIPv4  = 0x0800

	ipMinHeaderLen = 20
	ipTotalLenOff  = 2
	ipFlagsFragOff = 6 // 16-bit flags(3) + fragment-offset(13) field
	ipProtoOff     = 9
	ipProtoUDP     = 17

	// ipFlagMoreFragments (MF) and ipFragOffsetMask isolate the fragmentation state
	// of an IPv4 datagram. A packet with a non-zero fragment offset carries no UDP
	// header (its first bytes are payload continuation), and a first fragment with MF
	// set carries only a truncated payload — either would pool a garbage "frame" into
	// the audit, so both are skipped.
	ipFlagMoreFragments = 0x2000
	ipFragOffsetMask    = 0x1FFF

	udpHeaderLen = 8
	udpSrcOff    = 0
	udpDstOff    = 2
)

// Classic-pcap magic numbers, read as a big-endian uint32 over the first four
// bytes. The value encodes both the timestamp resolution and the writer's byte
// order, so switching on it yields the record byte order.
const (
	magicMicroBE = 0xa1b2c3d4 // microsecond ts, big-endian writer
	magicNanoBE  = 0xa1b23c4d // nanosecond ts, big-endian writer
	magicMicroLE = 0xd4c3b2a1 // microsecond ts, little-endian writer
	magicNanoLE  = 0x4d3cb2a1 // nanosecond ts, little-endian writer
)

// ParsePcap extracts the UDP payloads whose source OR destination port equals
// udpPort from a classic libpcap savefile with Ethernet link-layer framing (what
// `tcpdump -i <veth> -w file udp port <p>` produces). Each returned Frame is a
// fresh copy of one outer UDP payload — a wanbond wire frame. Non-IPv4, non-UDP,
// fragmented, and off-port packets are skipped. Ethernet padding on short frames is
// stripped by bounding the payload with the IPv4 total-length field, so a padded
// 60-byte minimum Ethernet frame does not contribute spurious trailing zero bytes.
func ParsePcap(data []byte, udpPort uint16) ([]Frame, error) {
	if len(data) < pcapGlobalHeaderLen {
		return nil, fmt.Errorf("wireaudit: pcap too short: %d bytes < %d-byte global header", len(data), pcapGlobalHeaderLen)
	}
	magic := binary.BigEndian.Uint32(data[0:4])
	var bo binary.ByteOrder
	switch magic {
	case magicMicroBE, magicNanoBE:
		bo = binary.BigEndian
	case magicMicroLE, magicNanoLE:
		bo = binary.LittleEndian
	default:
		return nil, fmt.Errorf("wireaudit: not a classic pcap savefile (magic 0x%08x)", magic)
	}
	linkType := bo.Uint32(data[pcapLinkTypeOff : pcapLinkTypeOff+4])
	if linkType != linkTypeEthernet {
		return nil, fmt.Errorf("wireaudit: unsupported link type %d (want Ethernet=%d)", linkType, linkTypeEthernet)
	}

	var frames []Frame
	off := pcapGlobalHeaderLen
	for off+pcapRecordHeaderLen <= len(data) {
		inclLen := int(bo.Uint32(data[off+pcapInclLenOff : off+pcapInclLenOff+4]))
		off += pcapRecordHeaderLen
		if inclLen < 0 || off+inclLen > len(data) {
			return nil, fmt.Errorf("wireaudit: truncated pcap record at byte %d (incl_len %d, %d bytes remain)", off, inclLen, len(data)-off)
		}
		pkt := data[off : off+inclLen]
		off += inclLen
		if payload, ok := udpPayload(pkt, udpPort); ok {
			frames = append(frames, append([]byte(nil), payload...))
		}
	}
	return frames, nil
}

// udpPayload returns the UDP payload of an Ethernet/IPv4/UDP packet whose source or
// destination port matches port, or ok=false if pkt is not such a packet. The
// payload is bounded by the IPv4 total-length field (not the captured length), so
// Ethernet frame padding is excluded.
func udpPayload(pkt []byte, port uint16) ([]byte, bool) {
	if len(pkt) < ethHeaderLen {
		return nil, false
	}
	if binary.BigEndian.Uint16(pkt[ethTypeOff:ethTypeOff+2]) != ethTypeIPv4 {
		return nil, false
	}
	ip := pkt[ethHeaderLen:]
	if len(ip) < ipMinHeaderLen {
		return nil, false
	}
	ihl := int(ip[0]&0x0f) * 4
	if ihl < ipMinHeaderLen || len(ip) < ihl {
		return nil, false
	}
	if ip[ipProtoOff] != ipProtoUDP {
		return nil, false
	}
	// Skip any fragment: a non-first fragment (non-zero offset) has no UDP header, so
	// its payload bytes would be misread as ports and a coincidental match would pool
	// a garbage frame; a first fragment with MF set is truncated. wanbond frames fit
	// the path MTU, so a fragmented UDP datagram here is never a real wanbond frame.
	fragField := binary.BigEndian.Uint16(ip[ipFlagsFragOff : ipFlagsFragOff+2])
	if fragField&ipFragOffsetMask != 0 || fragField&ipFlagMoreFragments != 0 {
		return nil, false
	}
	totalLen := int(binary.BigEndian.Uint16(ip[ipTotalLenOff : ipTotalLenOff+2]))
	// Bound the IP datagram by the captured bytes (guards against a truncated
	// snaplen or a corrupt total-length field).
	if totalLen > len(ip) {
		totalLen = len(ip)
	}
	if totalLen < ihl+udpHeaderLen {
		return nil, false
	}
	udp := ip[ihl:totalLen]
	if len(udp) < udpHeaderLen {
		return nil, false
	}
	src := binary.BigEndian.Uint16(udp[udpSrcOff : udpSrcOff+2])
	dst := binary.BigEndian.Uint16(udp[udpDstOff : udpDstOff+2])
	if src != port && dst != port {
		return nil, false
	}
	return udp[udpHeaderLen:], true
}
