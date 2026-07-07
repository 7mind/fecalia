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
// When dev is non-empty the socket is bound to that INTERFACE (SO_BINDTODEVICE)
// with a wildcard local address instead of pinning the specific source IP. That
// is what makes an edge's path survive a public-IP change mid-session (a re-roam
// / NAT rebind — T16): when the interface's address changes, a device-bound
// socket keeps sending, now sourced from the interface's NEW address, and the far
// side's Bind re-learns that source from the next authenticated probe (T37). A
// socket pinned to the OLD source IP would instead fail every send with
// ENETUNREACH once that address is removed, and the path could never recover
// without a re-Open (which would reset the WireGuard session).
//
// dev is chosen by planPathBinds / selectDeviceBinds and is non-empty ONLY when
// device binding is provably equivalent to pinning the configured source_addr and
// no other path contends for the device (see selectDeviceBinds). When dev is ""
// the socket pins the specific source IP — the pre-T16 behaviour.
//
// Device binding is also BEST-EFFORT even when selected: SO_BINDTODEVICE needs
// CAP_NET_RAW (the daemon runs privileged, but the unit tests bind loopback
// unprivileged) and is Linux-only, so a device-bind failure falls back to
// source-IP binding rather than failing Open.
func listenPath(src netip.Addr, port uint16, dev string) (*net.UDPConn, error) {
	if dev != "" {
		if c, err := listenOnDevice(src, port, dev); err == nil {
			return c, nil
		}
		// SO_BINDTODEVICE denied / unsupported: fall back to source-IP binding.
	}
	laddr := &net.UDPAddr{IP: net.IP(src.AsSlice()), Port: int(port)}
	return net.ListenUDP("udp", laddr)
}

// ifaceInfo is the resolution of a source address against the host's interfaces:
// the non-loopback interface currently holding the address (dev, "" when none
// does) and how many addresses of that source's family (IPv4 vs IPv6) the
// interface carries. familyCount drives the device-bind decision — a device-bind
// socket is only equivalent to the configured source pin when the interface holds
// exactly one address of the family.
type ifaceInfo struct {
	dev         string
	familyCount int
}

// planPathBinds resolves every path source against a SINGLE snapshot of the
// host's interfaces and returns the per-path bind plan (see selectDeviceBinds):
// index i holds the device to SO_BINDTODEVICE-bind path i to, or "" to pin path
// i's specific source IP. A snapshot failure resolves every source to an empty
// dev, so all paths fall back to source-IP binding.
func planPathBinds(srcs []netip.Addr) []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		ifaces = nil
	}
	return selectDeviceBinds(srcs, func(s netip.Addr) ifaceInfo {
		return interfaceInfo(s, ifaces)
	})
}

// selectDeviceBinds decides, per source address, whether its path socket may be
// bound to the source's interface (SO_BINDTODEVICE + wildcard) or must pin the
// specific source IP. Device binding — the T16 roam-surviving mode — is chosen
// ONLY when it is provably equivalent to pinning the configured source_addr AND
// no other path contends for the device:
//
//   - the source resolves to a non-loopback interface (dev != ""),
//   - that interface carries exactly ONE address of the source's family, so a
//     wildcard-on-device socket can only ever source from the configured address
//     (a multi-address interface would let the kernel pick a DIFFERENT source via
//     route-based selection, voiding source_addr's pin — Criticism 2), and
//   - exactly ONE configured path resolves to that interface, because two
//     wildcard+device sockets on the same port and device collide EADDRINUSE;
//     source-IP binding keeps their two DISTINCT specific-IP sockets, which
//     coexist on one port — the pre-T16 behaviour (Criticism 1).
//
// Every other path falls back to source-IP binding, exactly as before T16 (at the
// cost of not surviving a same-address readdress for those paths — the pre-T16
// status quo). The returned slice is parallel to srcs: a non-empty dev at i means
// "device-bind path i to it"; "" means "source-IP-bind path i".
func selectDeviceBinds(srcs []netip.Addr, resolve func(netip.Addr) ifaceInfo) []string {
	infos := make([]ifaceInfo, len(srcs))
	devPaths := make(map[string]int, len(srcs))
	for i, s := range srcs {
		infos[i] = resolve(s)
		if infos[i].dev != "" {
			devPaths[infos[i].dev]++
		}
	}
	out := make([]string, len(srcs))
	for i := range srcs {
		info := infos[i]
		if info.dev != "" && info.familyCount == 1 && devPaths[info.dev] == 1 {
			out[i] = info.dev
		}
	}
	return out
}

// interfaceInfo resolves src against ifaces (a single net.Interfaces snapshot):
// it returns the non-loopback interface that currently owns src and the count of
// same-family addresses on it. A loopback/unspecified/invalid src, or no owning
// interface, yields an empty dev (familyCount 0). It is the resolution step that
// lets a source-address config select a device to bind, so the socket can outlive
// that address changing.
func interfaceInfo(src netip.Addr, ifaces []net.Interface) ifaceInfo {
	if !src.IsValid() || src.IsLoopback() || src.IsUnspecified() {
		return ifaceInfo{}
	}
	want := src.Unmap()
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		owns := false
		familyCount := 0
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip, ok := netip.AddrFromSlice(ipn.IP)
			if !ok {
				continue
			}
			ip = ip.Unmap()
			if ip.Is4() == want.Is4() {
				familyCount++
			}
			if ip == want {
				owns = true
			}
		}
		if owns {
			return ifaceInfo{dev: ifc.Name, familyCount: familyCount}
		}
	}
	return ifaceInfo{}
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
