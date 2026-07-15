package bind

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"syscall"

	"github.com/7mind/wanbond/internal/config"
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
// Device binding is also BEST-EFFORT even when selected: on Linux <5.7,
// SO_BINDTODEVICE needs CAP_NET_RAW, which the shipped systemd units don't
// grant (CapabilityBoundingSet is CAP_NET_ADMIN only), so a pre-5.7 daemon
// also falls back to source-IP binding; on >=5.7 it needs no capability at
// all (see bindToDevice, pathsock_linux.go, D40). The unit tests bind
// loopback unprivileged too, exercising that same fallback path. It is also
// Linux-only, so a device-bind failure falls back to source-IP binding
// rather than failing Open.
//
// The middle return, deviceErr, is the underlying SO_BINDTODEVICE failure exactly
// when dev != "" and the device bind was attempted and failed (nil when dev == "",
// or when it succeeded) — the fallback fact plus its cause, alongside conn, so a
// caller can log a forced bind="device" path's silent-until-D53 fallback to
// source-IP pinning WITHOUT this file importing internal/log (Open, AddPath, and
// reconcileDeferred are the loggers; this stays logging-free). deviceErr alone does
// NOT mean a fallback socket exists: the caller MUST gate any "fell back to
// source-IP pinning" claim on err == nil (D53 round 2) — deviceErr only says a
// device bind was attempted and failed, not that the source-IP fallback attempted
// here actually produced a working socket.
func listenPath(src netip.Addr, port uint16, dev string) (conn *net.UDPConn, deviceErr error, err error) {
	if dev != "" {
		c, derr := listenOnDevice(src, port, dev)
		if derr == nil {
			return c, nil, nil
		}
		// SO_BINDTODEVICE denied / unsupported: fall back to source-IP binding.
		deviceErr = derr
	}
	laddr := &net.UDPAddr{IP: net.IP(src.AsSlice()), Port: int(port)}
	conn, err = net.ListenUDP("udp", laddr)
	return conn, deviceErr, err
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
// dev, so all paths (other than a forced BindModeDevice — see selectDeviceBinds)
// fall back to source-IP binding. modes is parallel to srcs and holds each
// path's RESOLVED bind mode (config.Path.Bind after normalize(), I5) — never
// empty on a loaded config, but selectDeviceBinds treats any value other than
// BindModeSource/BindModeDevice as BindModeAuto defensively.
func planPathBinds(srcs []netip.Addr, modes []config.BindMode) []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		ifaces = nil
	}
	return selectDeviceBinds(srcs, modes, func(s netip.Addr) ifaceInfo {
		return interfaceInfo(s, ifaces)
	})
}

// selectDeviceBinds decides, per source address, whether its path socket may be
// bound to the source's interface (SO_BINDTODEVICE + wildcard) or must pin the
// specific source IP, honoring each path's RESOLVED BindMode (I5, Q42):
//
//   - config.BindModeSource forces source-IP pinning unconditionally: the
//     source's interface is never even consulted for a device-bind decision.
//     This is the D38 escape hatch — a source-policy-routed uplink (one address
//     per VLAN interface) that the BindModeAuto heuristic below would otherwise
//     device-bind, silently defeating `ip rule from <source_addr>`.
//
//   - config.BindModeDevice forces a device bind unconditionally: the path's
//     interface is used whenever it resolves, regardless of family count or
//     contention with another configured path. An unresolvable interface (no
//     owning interface found) falls back to source-IP binding; a resolved-but-
//     failing SO_BINDTODEVICE (permission, unsupported) falls back the same way
//     at listenPath's setsockopt layer.
//
//   - config.BindModeAuto (and, defensively, any other/unset value) reproduces
//     the pre-I5 heuristic BYTE-FOR-BYTE: device binding — the T16 roam-
//     surviving mode — is chosen ONLY when it is provably equivalent to pinning
//     the configured source_addr AND no other path contends for the device:
//
//   - the source resolves to a non-loopback interface (dev != ""),
//
//   - that interface carries exactly ONE address of the source's family, so a
//     wildcard-on-device socket can only ever source from the configured address
//     (a multi-address interface would let the kernel pick a DIFFERENT source via
//     route-based selection, voiding source_addr's pin — Criticism 2), and
//
//   - exactly ONE configured path resolves to that interface, because two
//     wildcard+device sockets on the same port and device collide EADDRINUSE;
//     source-IP binding keeps their two DISTINCT specific-IP sockets, which
//     coexist on one port — the pre-T16 behaviour (Criticism 1).
//
// Every auto-mode path that fails those checks falls back to source-IP binding,
// exactly as before T16 (at the cost of not surviving a same-address readdress
// for those paths — the pre-T16 status quo). The returned slice is parallel to
// srcs: a non-empty dev at i means "device-bind path i to it"; "" means
// "source-IP-bind path i". modes is parallel to srcs.
func selectDeviceBinds(srcs []netip.Addr, modes []config.BindMode, resolve func(netip.Addr) ifaceInfo) []string {
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
		switch modes[i] {
		case config.BindModeSource:
			// out[i] stays "": never SO_BINDTODEVICE, regardless of what the
			// interface resolution would have decided (the D38 escape hatch).
		case config.BindModeDevice:
			// Forced device-bind: use the path's interface unconditionally when it
			// resolves. info.dev is already "" when unresolvable, which is the
			// fallback-to-source-IP-binding outcome.
			out[i] = info.dev
		default: // config.BindModeAuto, or any other/unset value.
			if info.dev != "" && info.familyCount == 1 && devPaths[info.dev] == 1 {
				out[i] = info.dev
			}
		}
	}
	return out
}

// resolveForcedDeviceBind resolves src's interface for a single, isolated
// forced-device-mode bind (AddPath / the T55 deferred-path reconcile), which —
// unlike Open's planPathBinds/selectDeviceBinds — binds one path at a time with
// no other-path contention to check. It returns "" (source-IP-bind) for any mode
// other than config.BindModeDevice, or when the interface cannot be resolved, so
// the caller's listenPath call falls back to source-IP binding exactly as the
// unresolvable-interface case does in selectDeviceBinds.
//
// It is a thin real-interfaces wrapper around selectForcedDeviceBind (the DECISION,
// split out below — T106 round 2) exactly as planPathBinds wraps selectDeviceBinds:
// the mode check short-circuits BEFORE the net.Interfaces() snapshot so a non-device
// path never pays that syscall.
func resolveForcedDeviceBind(src netip.Addr, mode config.BindMode) string {
	if mode != config.BindModeDevice {
		return ""
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		ifaces = nil
	}
	return selectForcedDeviceBind(src, mode, func(s netip.Addr) ifaceInfo {
		return interfaceInfo(s, ifaces)
	})
}

// selectForcedDeviceBind is resolveForcedDeviceBind's DECISION, split from the
// net.Interfaces() snapshot the same way selectDeviceBinds is split from
// planPathBinds (T106 round 2), so it is unit-testable with a fake resolver and no
// real interfaces: source -> "" (never device-bind, the D38 escape hatch),
// auto/other -> "" HERE (this is the FORCED-device decision only) — a runtime auto path's
// device-bind is now decided separately by Multipath.autoRuntimeDeviceBind, which applies
// Open's selectDeviceBinds contention heuristic over the membership (D30, closed), device+
// resolvable -> the resolved dev, device+unresolvable -> "" (fallback to source-IP binding,
// exactly as selectDeviceBinds' unresolvable case).
func selectForcedDeviceBind(src netip.Addr, mode config.BindMode, resolve func(netip.Addr) ifaceInfo) string {
	if mode != config.BindModeDevice {
		return ""
	}
	return resolve(src).dev
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
		ips := make([]netip.Addr, 0, len(addrs))
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip, ok := netip.AddrFromSlice(ipn.IP)
			if !ok {
				continue
			}
			ips = append(ips, ip)
		}
		familyCount, owns := familyBindCount(want, ips)
		if owns {
			return ifaceInfo{dev: ifc.Name, familyCount: familyCount}
		}
	}
	return ifaceInfo{}
}

// familyBindCount reports, for a source address want, how many of an interface's
// addresses count toward the device-bind equivalence decision (see
// selectDeviceBinds) and whether want itself is present. familyCount must equal 1
// for a device-bind to be provably equivalent to pinning want: it counts the
// same-family addresses the kernel could otherwise source-select from.
//
// For a GLOBAL (non-link-local) v6 source, fe80::/10 link-local addresses are
// EXCLUDED from the count: an up interface virtually always carries a kernel
// link-local alongside its configured global v6 address, but the kernel never
// source-selects a link-local for a global destination, so a link-local co-resident
// does not void the global source_addr pin — a wildcard+device socket still sources
// only from the configured global address (defect D13). For a v4 source or a
// link-local v6 source every same-family address is counted, unchanged: a
// link-local-only interface, and any v4 interface, is unaffected.
func familyBindCount(want netip.Addr, addrs []netip.Addr) (familyCount int, owns bool) {
	excludeLinkLocal := !want.Is4() && !want.IsLinkLocalUnicast()
	for _, ip := range addrs {
		ip = ip.Unmap()
		if ip.Is4() == want.Is4() && (!excludeLinkLocal || !ip.IsLinkLocalUnicast()) {
			familyCount++
		}
		if ip == want {
			owns = true
		}
	}
	return familyCount, owns
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
