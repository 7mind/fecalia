//go:build linux

package device

import (
	"encoding/binary"
	"testing"

	"golang.org/x/sys/unix"
)

func TestLinkGSOMaxSizeRequest(t *testing.T) {
	const ifindex = 27
	const size = 16 * 1024
	buf := linkGSOMaxSizeRequest(ifindex, size)
	en := binary.NativeEndian

	const wantLen = unix.SizeofNlMsghdr + unix.SizeofIfInfomsg + unix.SizeofRtAttr + 4
	if len(buf) != wantLen {
		t.Fatalf("request length = %d, want %d", len(buf), wantLen)
	}
	if got := en.Uint32(buf[0:4]); got != wantLen {
		t.Fatalf("nlmsg length = %d, want %d", got, wantLen)
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
	if got := en.Uint16(buf[attr : attr+2]); got != unix.SizeofRtAttr+4 {
		t.Fatalf("attribute length = %d, want %d", got, unix.SizeofRtAttr+4)
	}
	if got := en.Uint16(buf[attr+2 : attr+4]); got != unix.IFLA_GSO_MAX_SIZE {
		t.Fatalf("attribute type = %d, want IFLA_GSO_MAX_SIZE", got)
	}
	if got := en.Uint32(buf[attr+4 : attr+8]); got != size {
		t.Fatalf("GSO max size = %d, want %d", got, size)
	}
}

func TestLinkGSOMaxSizeReadRequest(t *testing.T) {
	const ifindex = 27
	buf := linkGSOMaxSizeReadRequest(ifindex)
	en := binary.NativeEndian

	const wantLen = unix.SizeofNlMsghdr + unix.SizeofIfInfomsg
	if got := len(buf); got != wantLen {
		t.Fatalf("request length = %d, want %d", got, wantLen)
	}
	if got := en.Uint16(buf[4:6]); got != unix.RTM_GETLINK {
		t.Fatalf("nlmsg type = %d, want RTM_GETLINK", got)
	}
	info := unix.SizeofNlMsghdr
	if got := int32(en.Uint32(buf[info+4 : info+8])); got != ifindex {
		t.Fatalf("ifindex = %d, want %d", got, ifindex)
	}
}

func TestParseLinkGSOMaxSize(t *testing.T) {
	const ifindex = 27
	const want = 5_000
	reply := linkGSOMaxSizeRequest(ifindex, want)
	got, err := parseLinkGSOMaxSize(reply, ifindex)
	if err != nil {
		t.Fatalf("parse GSO max size: %v", err)
	}
	if got != want {
		t.Fatalf("GSO max size = %d, want %d", got, want)
	}
}
