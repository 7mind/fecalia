//go:build linux

package device

import (
	"encoding/binary"
	"testing"

	"golang.org/x/sys/unix"
)

func TestLinkGSOLimitsRequest(t *testing.T) {
	const ifindex = 27
	want := linkGSOLimits{MaxSize: 16 * 1024, MaxSegments: 11}
	buf := linkGSOLimitsRequest(ifindex, want)
	en := binary.NativeEndian

	const attrLen = unix.SizeofRtAttr + 4
	const wantLen = unix.SizeofNlMsghdr + unix.SizeofIfInfomsg + 2*attrLen
	if len(buf) != wantLen {
		t.Fatalf("request length = %d, want %d", len(buf), wantLen)
	}
	if got := en.Uint16(buf[4:6]); got != unix.RTM_NEWLINK {
		t.Fatalf("nlmsg type = %d, want RTM_NEWLINK", got)
	}
	if got := en.Uint16(buf[6:8]); got != unix.NLM_F_REQUEST|unix.NLM_F_ACK {
		t.Fatalf("nlmsg flags = %#x, want request|ack", got)
	}
	info := unix.SizeofNlMsghdr
	if got := int32(en.Uint32(buf[info+4 : info+8])); got != ifindex {
		t.Fatalf("ifindex = %d, want %d", got, ifindex)
	}
	attr := info + unix.SizeofIfInfomsg
	if got := en.Uint16(buf[attr+2 : attr+4]); got != unix.IFLA_GSO_MAX_SIZE {
		t.Fatalf("first attribute type = %d, want IFLA_GSO_MAX_SIZE", got)
	}
	if got := en.Uint32(buf[attr+4 : attr+8]); got != want.MaxSize {
		t.Fatalf("GSO max size = %d, want %d", got, want.MaxSize)
	}
	attr += attrLen
	if got := en.Uint16(buf[attr+2 : attr+4]); got != unix.IFLA_GSO_MAX_SEGS {
		t.Fatalf("second attribute type = %d, want IFLA_GSO_MAX_SEGS", got)
	}
	if got := en.Uint32(buf[attr+4 : attr+8]); got != want.MaxSegments {
		t.Fatalf("GSO max segments = %d, want %d", got, want.MaxSegments)
	}
}

func TestParseLinkGSOLimits(t *testing.T) {
	const ifindex = 27
	want := linkGSOLimits{MaxSize: 16 * 1024, MaxSegments: 11}
	got, err := parseLinkGSOLimits(linkGSOLimitsRequest(ifindex, want), ifindex)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("GSO limits = %+v, want %+v", got, want)
	}
}

func TestLinkGSOLimitsReadRequest(t *testing.T) {
	const ifindex = 27
	buf := linkGSOLimitsReadRequest(ifindex)
	en := binary.NativeEndian
	if got := en.Uint16(buf[4:6]); got != unix.RTM_GETLINK {
		t.Fatalf("nlmsg type = %d, want RTM_GETLINK", got)
	}
	info := unix.SizeofNlMsghdr
	if got := int32(en.Uint32(buf[info+4 : info+8])); got != ifindex {
		t.Fatalf("ifindex = %d, want %d", got, ifindex)
	}
}
