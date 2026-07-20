//go:build linux

package bind

import (
	"net"
	"net/netip"
	"syscall"
	"testing"
)

// getMTUDiscover reads the socket's MTU-discovery policy (IP_MTU_DISCOVER for a v4
// socket, IPV6_MTU_DISCOVER for a v6 one) off the raw fd, so the test asserts the
// kernel-visible option value rather than trusting the setter returned nil.
func getMTUDiscover(t *testing.T, conn *net.UDPConn, isV6 bool) int {
	t.Helper()
	raw, err := conn.SyscallConn()
	if err != nil {
		t.Fatalf("SyscallConn: %v", err)
	}
	var val int
	var opErr error
	if err := raw.Control(func(fd uintptr) {
		if isV6 {
			val, opErr = syscall.GetsockoptInt(int(fd), syscall.IPPROTO_IPV6, syscall.IPV6_MTU_DISCOVER)
		} else {
			val, opErr = syscall.GetsockoptInt(int(fd), syscall.IPPROTO_IP, syscall.IP_MTU_DISCOVER)
		}
	}); err != nil {
		t.Fatalf("raw.Control: %v", err)
	}
	if opErr != nil {
		t.Fatalf("getsockopt MTU_DISCOVER: %v", opErr)
	}
	return val
}

// TestPathSocketSetsDF is the T201 reproduce-first regression: a path socket bound by
// listenPath must egress with the Don't-Fragment MTU-discovery policy PMTUDISC_DO, so
// oversized sends surface EMSGSIZE and padded-probe PMTU discovery is meaningful. It
// checks BOTH families — the v4 (IP_MTU_DISCOVER) and v6 (IPV6_MTU_DISCOVER) options
// have distinct levels/names and are set by distinct branches of setDontFragment.
// Before the DF Control hook was wired, the socket carried the kernel default
// (PMTUDISC_WANT/DONT, != DO) and this failed.
func TestPathSocketSetsDF(t *testing.T) {
	cases := []struct {
		name string
		src  netip.Addr
		isV6 bool
		want int
	}{
		{name: "v4", src: netip.MustParseAddr("127.0.0.1"), isV6: false, want: syscall.IP_PMTUDISC_DO},
		{name: "v6", src: netip.MustParseAddr("::1"), isV6: true, want: syscall.IPV6_PMTUDISC_DO},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// dev == "" exercises the source-IP-pinned branch (listenSourcePinned); the
			// device-bind branch shares the same pathSocketControl hook, so DF is set on
			// both socket-creation paths.
			conn, deviceErr, err := listenPath(tc.src, 0, "")
			if err != nil {
				t.Fatalf("listenPath(%s): %v", tc.src, err)
			}
			if deviceErr != nil {
				t.Fatalf("listenPath(%s): unexpected deviceErr: %v", tc.src, deviceErr)
			}
			t.Cleanup(func() { _ = conn.Close() })

			if got := getMTUDiscover(t, conn, tc.isV6); got != tc.want {
				t.Errorf("MTU_DISCOVER = %d, want PMTUDISC_DO (%d)", got, tc.want)
			}
		})
	}
}
