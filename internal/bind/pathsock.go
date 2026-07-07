package bind

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"syscall"
)

// listenPath binds one path's UDP socket to port on the path's source.
//
// It PREFERS binding the socket to the source's INTERFACE (SO_BINDTODEVICE) with
// a wildcard local address, rather than pinning the specific source IP. That is
// what makes an edge's path survive a public-IP change mid-session (a re-roam /
// NAT rebind — T16): when the interface's address changes, a device-bound socket
// keeps sending, now sourced from the interface's NEW address, and the far side's
// Bind re-learns that source from the next authenticated probe (T37). A socket
// pinned to the OLD source IP would instead fail every send with ENETUNREACH once
// that address is removed, and the path could never recover without a re-Open
// (which would reset the WireGuard session).
//
// Device binding is BEST-EFFORT: when the source is loopback/unspecified, its
// interface cannot be resolved, or SO_BINDTODEVICE is not permitted (it needs
// CAP_NET_RAW; the daemon runs privileged, but the unit tests bind loopback
// unprivileged), listenPath falls back to binding the specific source address —
// the pre-T16 behaviour — so nothing regresses. For the single-address uplink
// interfaces this is used on, "bind the device, source from its address" is
// identical to "bind the source IP" until the address actually changes.
//
// Same-port coexistence: several paths may share one port (a re-Open passes the
// engine's fixed listen port back for every path). Distinct SO_BINDTODEVICE
// sockets on the same wildcard port do NOT collide (the kernel keys them by
// device), so device binding does not reintroduce the per-path bind conflict that
// distinct source IPs previously avoided.
func listenPath(src netip.Addr, port uint16) (*net.UDPConn, error) {
	if dev := interfaceForAddr(src); dev != "" {
		if c, err := listenOnDevice(src, port, dev); err == nil {
			return c, nil
		}
		// SO_BINDTODEVICE denied / unsupported: fall back to source-IP binding.
	}
	laddr := &net.UDPAddr{IP: net.IP(src.AsSlice()), Port: int(port)}
	return net.ListenUDP("udp", laddr)
}

// interfaceForAddr returns the name of the non-loopback interface that currently
// owns src, or "" when src is loopback/unspecified/invalid or no interface holds
// it. It is the resolution step that lets a source-address config select a device
// to bind, so the socket can outlive that address changing.
func interfaceForAddr(src netip.Addr) string {
	if !src.IsValid() || src.IsLoopback() || src.IsUnspecified() {
		return ""
	}
	want := src.Unmap()
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			if ip, ok := netip.AddrFromSlice(ipn.IP); ok && ip.Unmap() == want {
				return ifc.Name
			}
		}
	}
	return ""
}

// listenOnDevice binds a UDP socket to the family-matched wildcard address on port
// and pins it to dev via SO_BINDTODEVICE (bindToDevice, platform-specific). It
// returns an error the caller treats as "fall back to source-IP binding".
func listenOnDevice(src netip.Addr, port uint16, dev string) (*net.UDPConn, error) {
	host, network := "0.0.0.0", "udp4"
	if !src.Is4() && !src.Is4In6() {
		host, network = "::", "udp6"
	}
	lc := net.ListenConfig{
		Control: func(_, _ string, c syscall.RawConn) error {
			var operr error
			if err := c.Control(func(fd uintptr) { operr = bindToDevice(fd, dev) }); err != nil {
				return err
			}
			return operr
		},
	}
	pc, err := lc.ListenPacket(context.Background(), network, net.JoinHostPort(host, strconv.Itoa(int(port))))
	if err != nil {
		return nil, err
	}
	uc, ok := pc.(*net.UDPConn)
	if !ok {
		_ = pc.Close()
		return nil, fmt.Errorf("bind: device %q listener is %T, want *net.UDPConn", dev, pc)
	}
	return uc, nil
}
