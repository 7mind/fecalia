//go:build linux

package device

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"

	"golang.org/x/sys/unix"
)

// Route programming for mode=default-route (I6, Q41). When a peer opts into
// full-tunnel mode, the daemon installs the wg-quick-style split default route
// (the two /1 prefixes) INTO wanbond0 after the interface is up, and withdraws
// them on Close. It talks rtnetlink directly over an AF_NETLINK socket — matching
// the daemon's "no privileged shell-outs" posture (device.go) and the raw
// golang.org/x/sys/unix idiom already used for the SIOCSIFFLAGS link-up (see
// linkup_linux.go) — rather than shelling out to `ip route`.
//
// STRICT Q41 boundary: this installs plain scope-link device routes ONLY. It does
// NOT add policy-routing rules (ip rule / fwmark / suppress_prefixlength), SNAT, or
// any concentrator ip_forward/MASQUERADE/FORWARD programming — those stay documented
// C3/C6 operator recipes. The two /1s (rather than a literal /0) deliberately do NOT
// override the physical default route, so the encrypted underlay to the concentrator
// endpoint is unaffected (the same reason wg-quick uses the /1 split).

// rtmsgLen is sizeof(struct rtmsg): family, dst_len, src_len, tos, table,
// protocol, scope, type (8×u8) followed by flags (u32) — 12 bytes.
const rtmsgLen = 12

// nlaAlign rounds n up to the netlink attribute alignment (NLA_ALIGNTO = 4).
func nlaAlign(n int) int { return (n + 3) &^ 3 }

// ifIndex resolves an interface name to its kernel ifindex via SIOCGIFINDEX,
// mirroring the ioctl idiom in linkup_linux.go. A missing interface surfaces the
// raw errno (unix.ENODEV), which the removal path treats as "already gone".
func ifIndex(name string) (int, error) {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM, 0)
	if err != nil {
		return 0, fmt.Errorf("open control socket: %w", err)
	}
	defer func() { _ = unix.Close(fd) }()

	ifr, err := unix.NewIfreq(name)
	if err != nil {
		return 0, fmt.Errorf("build ifreq for %q: %w", name, err)
	}
	if err := unix.IoctlIfreq(fd, unix.SIOCGIFINDEX, ifr); err != nil {
		return 0, fmt.Errorf("SIOCGIFINDEX %q: %w", name, err)
	}
	return int(ifr.Uint32()), nil
}

// routeSocket opens and binds an AF_NETLINK rtnetlink socket for one batch of
// route requests. The caller closes it.
func routeSocket() (int, error) {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.NETLINK_ROUTE)
	if err != nil {
		return -1, fmt.Errorf("open rtnetlink socket: %w", err)
	}
	if err := unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		_ = unix.Close(fd)
		return -1, fmt.Errorf("bind rtnetlink socket: %w", err)
	}
	return fd, nil
}

// routeMsgFlags returns the netlink header flags for a route request. A delete
// (add=false) carries only NLM_F_REQUEST|NLM_F_ACK. An add carries additionally
// NLM_F_CREATE|NLM_F_REPLACE — deliberately NOT NLM_F_EXCL. Under tun_persist=true
// (T109) an unclean daemon death (SIGKILL/OOM/panic) leaves wanbond0 AND its /1
// routes in place because Close never ran; on restart an EXCL add would fail EEXIST
// on the first prefix, abort up(), and wedge every subsequent restart until an
// operator manually `ip route del`s. REPLACE adopts the leftover route — overwriting
// any stale attributes so it ends in exactly the intended state — matching the
// daemon's persistent-TUN adoption posture (persist_linux.go). It also makes a
// duplicate prefix in the computed set a no-op instead of a hard EEXIST failure.
// Kept as a pure function so the flag choice is unit-testable without a privileged
// netlink socket.
func routeMsgFlags(add bool) uint16 {
	flags := uint16(unix.NLM_F_REQUEST | unix.NLM_F_ACK)
	if add {
		flags |= unix.NLM_F_CREATE | unix.NLM_F_REPLACE
	}
	return flags
}

// programRoute sends a single RTM_NEWROUTE (add) or RTM_DELROUTE (remove) request
// for the scope-link device route prefix p via ifindex, then waits for the kernel
// ACK. The request carries exactly two attributes — RTA_DST (the masked prefix
// address) and RTA_OIF (the output interface) — with no gateway, so the kernel
// installs an onlink device route. The returned error, on a non-zero ACK, wraps the
// kernel errno so the caller can classify it (e.g. ESRCH/ENOENT on a delete of an
// already-absent route).
func programRoute(fd int, seq uint32, add bool, ifindex int, p netip.Prefix) error {
	msgType := uint16(unix.RTM_DELROUTE)
	if add {
		msgType = unix.RTM_NEWROUTE
	}
	flags := routeMsgFlags(add)

	addr := p.Addr()
	family := byte(unix.AF_INET)
	var ab []byte
	if addr.Is6() {
		family = unix.AF_INET6
		a := addr.As16()
		ab = a[:]
	} else {
		a := addr.As4()
		ab = a[:]
	}

	en := binary.NativeEndian
	rtaDstLen := 4 + len(ab)
	const rtaOifLen = 8
	total := unix.SizeofNlMsghdr + rtmsgLen + nlaAlign(rtaDstLen) + rtaOifLen

	buf := make([]byte, total)
	// nlmsghdr
	en.PutUint32(buf[0:4], uint32(total))
	en.PutUint16(buf[4:6], msgType)
	en.PutUint16(buf[6:8], flags)
	en.PutUint32(buf[8:12], seq)
	en.PutUint32(buf[12:16], 0) // pid 0: the kernel is the peer

	// rtmsg
	off := unix.SizeofNlMsghdr
	buf[off+0] = family
	buf[off+1] = byte(p.Bits()) // dst_len
	buf[off+2] = 0              // src_len
	buf[off+3] = 0              // tos
	buf[off+4] = unix.RT_TABLE_MAIN
	buf[off+5] = unix.RTPROT_BOOT
	buf[off+6] = unix.RT_SCOPE_LINK
	buf[off+7] = unix.RTN_UNICAST
	en.PutUint32(buf[off+8:off+12], 0) // rtm_flags

	// RTA_DST
	off += rtmsgLen
	en.PutUint16(buf[off+0:off+2], uint16(rtaDstLen))
	en.PutUint16(buf[off+2:off+4], unix.RTA_DST)
	copy(buf[off+4:], ab)

	// RTA_OIF
	off += nlaAlign(rtaDstLen)
	en.PutUint16(buf[off+0:off+2], uint16(rtaOifLen))
	en.PutUint16(buf[off+2:off+4], unix.RTA_OIF)
	en.PutUint32(buf[off+4:off+8], uint32(ifindex))

	sa := &unix.SockaddrNetlink{Family: unix.AF_NETLINK}
	if err := unix.Sendto(fd, buf, 0, sa); err != nil {
		return fmt.Errorf("send rtnetlink request: %w", err)
	}
	return recvAck(fd)
}

// recvAck reads the kernel's NLMSG_ERROR acknowledgement for the request just sent
// on fd and walks the returned netlink messages. A zero error code is the success ACK
// (NLM_F_ACK); a non-zero code is returned as the corresponding unix.Errno. Each
// message is an nlmsghdr (SizeofNlMsghdr bytes: len u32, type u16, flags u16, seq u32,
// pid u32) followed by its payload; an NLMSG_ERROR payload begins with an int32 errno
// (struct nlmsgerr). Messages are NLMSG_ALIGNTO(=4)-aligned, the same alignment as
// nlaAlign.
func recvAck(fd int) error {
	buf := make([]byte, unix.Getpagesize())
	n, _, err := unix.Recvfrom(fd, buf, 0)
	if err != nil {
		return fmt.Errorf("recv rtnetlink ack: %w", err)
	}
	en := binary.NativeEndian
	b := buf[:n]
	for len(b) >= unix.SizeofNlMsghdr {
		msgLen := int(en.Uint32(b[0:4]))
		msgType := en.Uint16(b[4:6])
		if msgLen < unix.SizeofNlMsghdr || msgLen > len(b) {
			return fmt.Errorf("malformed rtnetlink ack: message length %d in %d-byte buffer", msgLen, len(b))
		}
		if msgType == unix.NLMSG_ERROR {
			if msgLen < unix.SizeofNlMsghdr+4 {
				return fmt.Errorf("truncated NLMSG_ERROR payload (%d bytes)", msgLen)
			}
			code := int32(en.Uint32(b[unix.SizeofNlMsghdr : unix.SizeofNlMsghdr+4]))
			if code == 0 {
				return nil
			}
			return unix.Errno(-code)
		}
		b = b[nlaAlign(msgLen):]
	}
	return nil
}

// installRoutes installs each prefix as a scope-link device route via ifname. It is
// called only with a non-empty set (the caller skips a config with no default-route
// peer), so a plain tunnel never opens a netlink socket. On a mid-set failure it
// returns immediately, having installed every prefix BEFORE the failing one; because
// up() aborts before the Tunnel exists, Close/removeRoutes never runs, so the caller
// (device.go up()) is responsible for best-effort withdrawing this partial set before
// it propagates the error — otherwise, under tun_persist=true, those prefixes leak on
// the surviving interface. Re-install is idempotent (routeMsgFlags uses NLM_F_REPLACE),
// so a subsequent successful start overwrites any leak regardless.
func installRoutes(ifname string, prefixes []netip.Prefix) error {
	idx, err := ifIndex(ifname)
	if err != nil {
		return err
	}
	fd, err := routeSocket()
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()

	for i, p := range prefixes {
		if err := programRoute(fd, uint32(i+1), true, idx, p); err != nil {
			return fmt.Errorf("install route %s dev %s: %w", p, ifname, err)
		}
	}
	return nil
}

// removeRoutes withdraws each prefix installed by installRoutes. It is idempotent:
// an interface already gone (unix.ENODEV — the non-persistent Close destroyed
// wanbond0, taking its routes with it) is a no-op, and a route already dropped
// (unix.ESRCH/ENOENT — a double Close, or teardown-with-device) is tolerated per
// prefix. Any other error is aggregated and returned so Close can log it.
func removeRoutes(ifname string, prefixes []netip.Prefix) error {
	idx, err := ifIndex(ifname)
	if err != nil {
		if errors.Is(err, unix.ENODEV) {
			return nil
		}
		return err
	}
	fd, err := routeSocket()
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()

	var errs []error
	for i, p := range prefixes {
		err := programRoute(fd, uint32(i+1), false, idx, p)
		if err != nil && !errors.Is(err, unix.ESRCH) && !errors.Is(err, unix.ENOENT) {
			errs = append(errs, fmt.Errorf("remove route %s dev %s: %w", p, ifname, err))
		}
	}
	return errors.Join(errs...)
}
