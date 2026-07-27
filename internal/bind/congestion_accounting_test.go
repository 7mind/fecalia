package bind

import (
	"net/netip"
	"testing"
)

func TestOuterWireAccountingIncludesIPUDPHeadersAndInnerDataOnly(t *testing.T) {
	for _, test := range []struct {
		name     string
		source   netip.Addr
		overhead int
	}{
		{name: "ipv4", source: netip.MustParseAddr("192.0.2.1"), overhead: IPv4UDPOverhead},
		{name: "ipv6", source: netip.MustParseAddr("2001:db8::1"), overhead: IPv6UDPOverhead},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := &peerPathState{sharedPathState: &sharedPathState{src: test.source}}
			path.recordOuterWrite(1200)
			if got := path.txBytes.Load(); got != 1200 {
				t.Fatalf("payload accounting = %d, want 1200", got)
			}
			if got := path.outerWireBytes.Load(); got != uint64(1200+test.overhead) {
				t.Fatalf("outer wire accounting = %d, want %d", got, 1200+test.overhead)
			}
			if got := path.innerDataBytes.Load(); got != 0 {
				t.Fatalf("outer-only write changed inner DATA accounting: %d", got)
			}
		})
	}
}
