package wireaudit

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// buildEthIPUDP assembles one Ethernet/IPv4/UDP packet carrying payload between the
// given ports, then appends trailing padding bytes (as the NIC pads short frames).
// The IPv4 total-length field covers only the real datagram, so a correct parser
// strips the padding.
func buildEthIPUDP(srcPort, dstPort uint16, payload []byte, padding int) []byte {
	udp := make([]byte, udpHeaderLen+len(payload))
	binary.BigEndian.PutUint16(udp[0:2], srcPort)
	binary.BigEndian.PutUint16(udp[2:4], dstPort)
	binary.BigEndian.PutUint16(udp[4:6], uint16(len(udp)))
	// udp[6:8] checksum left zero (valid: UDP checksum is optional over IPv4).
	copy(udp[udpHeaderLen:], payload)

	ip := make([]byte, ipMinHeaderLen)
	ip[0] = 0x45 // version 4, IHL 5 (20 bytes)
	total := ipMinHeaderLen + len(udp)
	binary.BigEndian.PutUint16(ip[ipTotalLenOff:ipTotalLenOff+2], uint16(total))
	ip[8] = 64 // TTL
	ip[ipProtoOff] = ipProtoUDP

	eth := make([]byte, ethHeaderLen)
	binary.BigEndian.PutUint16(eth[ethTypeOff:ethTypeOff+2], ethTypeIPv4)

	pkt := append(eth, ip...)
	pkt = append(pkt, udp...)
	if padding > 0 {
		pkt = append(pkt, bytes.Repeat([]byte{0xFF}, padding)...)
	}
	return pkt
}

// buildEthIPFragment assembles an Ethernet/IPv4 packet with the raw 16-bit
// flags/fragment-offset field set to fragField and the given IP payload (NOT a real
// UDP datagram — a fragment carries opaque continuation bytes). proto is set to UDP
// so only the fragment guard, not the proto check, can exclude it.
func buildEthIPFragment(fragField uint16, payload []byte) []byte {
	ip := make([]byte, ipMinHeaderLen)
	ip[0] = 0x45
	binary.BigEndian.PutUint16(ip[ipTotalLenOff:ipTotalLenOff+2], uint16(ipMinHeaderLen+len(payload)))
	binary.BigEndian.PutUint16(ip[ipFlagsFragOff:ipFlagsFragOff+2], fragField)
	ip[8] = 64
	ip[ipProtoOff] = ipProtoUDP

	eth := make([]byte, ethHeaderLen)
	binary.BigEndian.PutUint16(eth[ethTypeOff:ethTypeOff+2], ethTypeIPv4)

	pkt := append(eth, ip...)
	return append(pkt, payload...)
}

// buildPcap wraps packets in a classic libpcap savefile with the given byte order
// and Ethernet link type.
func buildPcap(bo binary.ByteOrder, magic uint32, pkts [][]byte) []byte {
	var b bytes.Buffer
	hdr := make([]byte, pcapGlobalHeaderLen)
	// The magic constant is the value ParsePcap reads big-endian over the first four
	// on-disk bytes, so lay it down big-endian regardless of the record byte order.
	// Everything AFTER the magic is written in the file's declared byte order (bo).
	binary.BigEndian.PutUint32(hdr[0:4], magic)
	bo.PutUint16(hdr[4:6], 2)        // version major
	bo.PutUint16(hdr[6:8], 4)        // version minor
	bo.PutUint32(hdr[16:20], 262144) // snaplen
	bo.PutUint32(hdr[pcapLinkTypeOff:pcapLinkTypeOff+4], linkTypeEthernet)
	b.Write(hdr)

	for _, pkt := range pkts {
		rec := make([]byte, pcapRecordHeaderLen)
		bo.PutUint32(rec[0:4], 1_700_000_000) // ts_sec
		bo.PutUint32(rec[4:8], 0)             // ts_usec
		bo.PutUint32(rec[pcapInclLenOff:pcapInclLenOff+4], uint32(len(pkt)))
		bo.PutUint32(rec[12:16], uint32(len(pkt))) // orig_len
		b.Write(rec)
		b.Write(pkt)
	}
	return b.Bytes()
}

func TestParsePcapExtractsOnPortPayloadsAndStripsPadding(t *testing.T) {
	const port = 51820

	wantA := []byte("wanbond-frame-A-high-entropy-xxxx")
	wantB := []byte("frame-B")
	pkts := [][]byte{
		buildEthIPUDP(40000, port, wantA, 0),  // dst on port
		buildEthIPUDP(port, 40001, wantB, 20), // src on port, with Ethernet padding
		buildEthIPUDP(1234, 5678, []byte("off-port"), 0),
	}

	for _, tc := range []struct {
		name  string
		bo    binary.ByteOrder
		magic uint32
	}{
		{"little-endian-micro", binary.LittleEndian, magicMicroLE},
		{"big-endian-micro", binary.BigEndian, magicMicroBE},
		{"little-endian-nano", binary.LittleEndian, magicNanoLE},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := buildPcap(tc.bo, tc.magic, pkts)
			frames, err := ParsePcap(data, port)
			if err != nil {
				t.Fatalf("ParsePcap: %v", err)
			}
			if len(frames) != 2 {
				t.Fatalf("got %d frames, want 2 (off-port packet excluded)", len(frames))
			}
			if !bytes.Equal(frames[0], wantA) {
				t.Errorf("frame 0 = %q, want %q", frames[0], wantA)
			}
			// The padding must be stripped: frame B is exactly wantB, not wantB+0xFF*20.
			if !bytes.Equal(frames[1], wantB) {
				t.Errorf("frame 1 = %q (len %d), want %q (len %d) — Ethernet padding not stripped",
					frames[1], len(frames[1]), wantB, len(wantB))
			}
		})
	}
}

func TestParsePcapRejectsNonPcap(t *testing.T) {
	if _, err := ParsePcap([]byte("not a pcap file at all!!"), 51820); err == nil {
		t.Fatal("expected an error for non-pcap input")
	}
}

func TestParsePcapRejectsTruncatedRecord(t *testing.T) {
	port := uint16(51820)
	data := buildPcap(binary.LittleEndian, magicMicroLE, [][]byte{
		buildEthIPUDP(40000, port, []byte("payload"), 0),
	})
	// Chop the last few bytes so the final record's incl_len overruns the buffer.
	truncated := data[:len(data)-4]
	if _, err := ParsePcap(truncated, port); err == nil {
		t.Fatal("expected a truncation error")
	}
}

// TestParsePcapSkipsFragments proves the fragment guard: a non-first fragment and a
// first fragment with MF set — whose leading bytes are crafted to look like the
// target ports — must NOT be pooled as frames, while an ordinary (unfragmented,
// DF-set) UDP packet on the port still IS. Extra garbage 'frames' could only CLEAR a
// genuine constant, so this closes a false-pass direction.
func TestParsePcapSkipsFragments(t *testing.T) {
	const port = 51820

	// Fragment leading bytes = port,port so the pre-fix parser would have matched.
	frag := make([]byte, 40)
	binary.BigEndian.PutUint16(frag[0:2], port)
	binary.BigEndian.PutUint16(frag[2:4], port)

	realPayload := []byte("real-wanbond-frame")
	pkts := [][]byte{
		buildEthIPFragment(0x0001, frag),              // non-first fragment (offset != 0)
		buildEthIPFragment(ipFlagMoreFragments, frag), // first fragment, MF set
		buildEthIPUDP(port, 40000, realPayload, 0),    // ordinary UDP on the port
	}
	data := buildPcap(binary.LittleEndian, magicMicroLE, pkts)
	frames, err := ParsePcap(data, port)
	if err != nil {
		t.Fatalf("ParsePcap: %v", err)
	}
	if len(frames) != 1 {
		t.Fatalf("got %d frames, want 1 (both fragments must be skipped)", len(frames))
	}
	if !bytes.Equal(frames[0], realPayload) {
		t.Errorf("frame 0 = %q, want %q", frames[0], realPayload)
	}
}

// TestParsePcapAcceptsDontFragment confirms the guard does NOT reject an ordinary
// packet that merely has the Don't-Fragment (DF) bit set (0x4000) with zero offset.
func TestParsePcapAcceptsDontFragment(t *testing.T) {
	const port = 51820
	pkt := buildEthIPUDP(port, 40000, []byte("df-frame"), 0)
	// Set DF (0x4000) in the flags/frag field — neither MF nor a non-zero offset.
	binary.BigEndian.PutUint16(pkt[ethHeaderLen+ipFlagsFragOff:ethHeaderLen+ipFlagsFragOff+2], 0x4000)
	data := buildPcap(binary.LittleEndian, magicMicroLE, [][]byte{pkt})
	frames, err := ParsePcap(data, port)
	if err != nil {
		t.Fatalf("ParsePcap: %v", err)
	}
	if len(frames) != 1 {
		t.Fatalf("got %d frames, want 1 (DF-set packet must be accepted)", len(frames))
	}
}

func TestParsePcapEmptyIsNoFrames(t *testing.T) {
	data := buildPcap(binary.LittleEndian, magicMicroLE, nil)
	frames, err := ParsePcap(data, 51820)
	if err != nil {
		t.Fatalf("ParsePcap on empty savefile: %v", err)
	}
	if len(frames) != 0 {
		t.Fatalf("got %d frames from an empty savefile, want 0", len(frames))
	}
}
