//go:build linux

package device

import (
	"encoding/binary"
	"fmt"

	"golang.org/x/sys/unix"
)

type linkGSOLimits struct {
	MaxSize     uint32
	MaxSegments uint32
}

func linkGSOLimitsRequest(ifindex int, limits linkGSOLimits) []byte {
	const attrLen = unix.SizeofRtAttr + 4
	total := unix.SizeofNlMsghdr + unix.SizeofIfInfomsg + 2*attrLen
	buf := make([]byte, total)
	en := binary.NativeEndian

	en.PutUint32(buf[0:4], uint32(total))
	en.PutUint16(buf[4:6], unix.RTM_NEWLINK)
	en.PutUint16(buf[6:8], unix.NLM_F_REQUEST|unix.NLM_F_ACK)
	en.PutUint32(buf[8:12], 1)

	off := unix.SizeofNlMsghdr
	buf[off] = unix.AF_UNSPEC
	en.PutUint32(buf[off+4:off+8], uint32(ifindex))

	off += unix.SizeofIfInfomsg
	en.PutUint16(buf[off:off+2], attrLen)
	en.PutUint16(buf[off+2:off+4], unix.IFLA_GSO_MAX_SIZE)
	en.PutUint32(buf[off+4:off+8], limits.MaxSize)
	off += attrLen
	en.PutUint16(buf[off:off+2], attrLen)
	en.PutUint16(buf[off+2:off+4], unix.IFLA_GSO_MAX_SEGS)
	en.PutUint32(buf[off+4:off+8], limits.MaxSegments)
	return buf
}

func linkGSOLimitsReadRequest(ifindex int) []byte {
	total := unix.SizeofNlMsghdr + unix.SizeofIfInfomsg
	buf := make([]byte, total)
	en := binary.NativeEndian
	en.PutUint32(buf[0:4], uint32(total))
	en.PutUint16(buf[4:6], unix.RTM_GETLINK)
	en.PutUint16(buf[6:8], unix.NLM_F_REQUEST)
	en.PutUint32(buf[8:12], 1)
	off := unix.SizeofNlMsghdr
	buf[off] = unix.AF_UNSPEC
	en.PutUint32(buf[off+4:off+8], uint32(ifindex))
	return buf
}

func parseLinkGSOLimits(buf []byte, ifindex int) (linkGSOLimits, error) {
	en := binary.NativeEndian
	for len(buf) >= unix.SizeofNlMsghdr {
		msgLen := int(en.Uint32(buf[0:4]))
		if msgLen < unix.SizeofNlMsghdr || msgLen > len(buf) {
			return linkGSOLimits{}, fmt.Errorf("malformed rtnetlink link response: message length %d in %d-byte buffer", msgLen, len(buf))
		}
		msgType := en.Uint16(buf[4:6])
		if msgType == unix.NLMSG_ERROR {
			if msgLen < unix.SizeofNlMsghdr+4 {
				return linkGSOLimits{}, fmt.Errorf("truncated rtnetlink link error")
			}
			code := int32(en.Uint32(buf[unix.SizeofNlMsghdr : unix.SizeofNlMsghdr+4]))
			if code != 0 {
				return linkGSOLimits{}, unix.Errno(-code)
			}
		}
		if msgType == unix.RTM_NEWLINK {
			if msgLen < unix.SizeofNlMsghdr+unix.SizeofIfInfomsg {
				return linkGSOLimits{}, fmt.Errorf("truncated RTM_NEWLINK response")
			}
			info := unix.SizeofNlMsghdr
			gotIndex := int(int32(en.Uint32(buf[info+4 : info+8])))
			if gotIndex == ifindex {
				attrs := buf[info+unix.SizeofIfInfomsg : msgLen]
				var limits linkGSOLimits
				for len(attrs) >= unix.SizeofRtAttr {
					attrLen := int(en.Uint16(attrs[0:2]))
					if attrLen < unix.SizeofRtAttr || attrLen > len(attrs) {
						return linkGSOLimits{}, fmt.Errorf("malformed RTM_NEWLINK attribute length %d in %d-byte payload", attrLen, len(attrs))
					}
					if attrLen >= unix.SizeofRtAttr+4 {
						switch en.Uint16(attrs[2:4]) {
						case unix.IFLA_GSO_MAX_SIZE:
							limits.MaxSize = en.Uint32(attrs[4:8])
						case unix.IFLA_GSO_MAX_SEGS:
							limits.MaxSegments = en.Uint32(attrs[4:8])
						}
					}
					aligned := nlaAlign(attrLen)
					if aligned > len(attrs) {
						return linkGSOLimits{}, fmt.Errorf("truncated RTM_NEWLINK attribute padding")
					}
					attrs = attrs[aligned:]
				}
				if limits.MaxSize == 0 || limits.MaxSegments == 0 {
					return linkGSOLimits{}, fmt.Errorf("RTM_NEWLINK response for ifindex %d omitted GSO limits", ifindex)
				}
				return limits, nil
			}
		}
		aligned := nlaAlign(msgLen)
		if aligned > len(buf) {
			return linkGSOLimits{}, fmt.Errorf("truncated rtnetlink link response padding")
		}
		buf = buf[aligned:]
	}
	return linkGSOLimits{}, fmt.Errorf("rtnetlink response omitted interface ifindex %d", ifindex)
}

func setLinkGSOLimits(name string, limits linkGSOLimits) error {
	if limits.MaxSize == 0 || limits.MaxSegments == 0 {
		return fmt.Errorf("GSO maximum size and segments must be positive")
	}
	idx, err := ifIndex(name)
	if err != nil {
		return err
	}
	fd, err := rtnetlinkSocket()
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	sa := &unix.SockaddrNetlink{Family: unix.AF_NETLINK}
	if err := unix.Sendto(fd, linkGSOLimitsRequest(idx, limits), 0, sa); err != nil {
		return fmt.Errorf("send GSO limit rtnetlink request for %q: %w", name, err)
	}
	if err := recvAck(fd); err != nil {
		return fmt.Errorf("set GSO limits %q size=%d segments=%d: %w",
			name, limits.MaxSize, limits.MaxSegments, err)
	}
	return nil
}

func readLinkGSOLimits(name string) (linkGSOLimits, error) {
	idx, err := ifIndex(name)
	if err != nil {
		return linkGSOLimits{}, err
	}
	fd, err := rtnetlinkSocket()
	if err != nil {
		return linkGSOLimits{}, err
	}
	defer func() { _ = unix.Close(fd) }()
	sa := &unix.SockaddrNetlink{Family: unix.AF_NETLINK}
	if err := unix.Sendto(fd, linkGSOLimitsReadRequest(idx), 0, sa); err != nil {
		return linkGSOLimits{}, fmt.Errorf("send GSO limit readback request for %q: %w", name, err)
	}
	buf := make([]byte, 64*1024)
	n, _, err := unix.Recvfrom(fd, buf, 0)
	if err != nil {
		return linkGSOLimits{}, fmt.Errorf("receive GSO limit readback for %q: %w", name, err)
	}
	limits, err := parseLinkGSOLimits(buf[:n], idx)
	if err != nil {
		return linkGSOLimits{}, fmt.Errorf("read GSO limits %q: %w", name, err)
	}
	return limits, nil
}

// ifUp sets IFF_UP on the named interface via SIOCSIFFLAGS (see netdevice(7)),
// closing the silent-dead-tunnel footgun (I1): a write to a DOWN TUN yields EIO,
// and until now nothing brought wanbond0 up — the operator had to run `ip link
// set up` out of band. It reads the current flags first (SIOCGIFFLAGS) and ORs in
// IFF_UP rather than overwriting the flag word, so whatever OTHER flags the
// kernel already set on interface creation (POINTOPOINT, NOARP, MULTICAST, …)
// survive untouched. Addressing stays operator-owned; this touches ONLY the
// administrative up/down flag, mirroring the golang.org/x/sys/unix ioctl idiom
// used for SO_BINDTODEVICE in internal/bind/pathsock_linux.go.
func ifUp(name string) error {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM, 0)
	if err != nil {
		return fmt.Errorf("open control socket: %w", err)
	}
	defer func() { _ = unix.Close(fd) }()

	ifr, err := unix.NewIfreq(name)
	if err != nil {
		return fmt.Errorf("build ifreq for %q: %w", name, err)
	}
	if err := unix.IoctlIfreq(fd, unix.SIOCGIFFLAGS, ifr); err != nil {
		return fmt.Errorf("SIOCGIFFLAGS %q: %w", name, err)
	}
	ifr.SetUint16(ifr.Uint16() | unix.IFF_UP)
	if err := unix.IoctlIfreq(fd, unix.SIOCSIFFLAGS, ifr); err != nil {
		return fmt.Errorf("SIOCSIFFLAGS %q: %w", name, err)
	}
	return nil
}

// setLinkMTU sets the named interface's link MTU via SIOCSIFMTU (see netdevice(7)),
// the runtime counterpart to the boot-time sizing tun.CreateTUN applies from
// tunMTU(cfg) (T205). It is how the T209/D85 resizer adjusts the LIVE wanbond0 MTU as
// the min inner MTU across UP paths changes — mirroring the native golang.org/x/sys/unix
// ioctl idiom ifUp uses for SIOCSIFFLAGS, and the direct-netlink posture of
// installRoutes (route_linux.go), rather than shelling out to `ip link set mtu`. The
// value is stored as a uint32 in ifr_mtu; the caller passes a positive inner MTU.
func setLinkMTU(name string, mtu int) error {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM, 0)
	if err != nil {
		return fmt.Errorf("open control socket: %w", err)
	}
	defer func() { _ = unix.Close(fd) }()

	ifr, err := unix.NewIfreq(name)
	if err != nil {
		return fmt.Errorf("build ifreq for %q: %w", name, err)
	}
	ifr.SetUint32(uint32(mtu))
	if err := unix.IoctlIfreq(fd, unix.SIOCSIFMTU, ifr); err != nil {
		return fmt.Errorf("SIOCSIFMTU %q=%d: %w", name, mtu, err)
	}
	return nil
}

// ifState reads the named interface's administrative up/down flag (SIOCGIFFLAGS) and MTU
// (SIOCGIFMTU) WITHOUT modifying anything — the read-only counterpart to ifUp, used to name
// the probable cause when a TUN write fails with EIO (I3/D39): a write to a DOWN interface is
// the textbook case, but naming the ACTUAL state (rather than assuming) also covers the case
// where the link is up yet writes still fail (a driver/NIC fault), so the diagnostic never
// asserts a cause it did not verify.
func ifState(name string) (up bool, mtu int, err error) {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM, 0)
	if err != nil {
		return false, 0, fmt.Errorf("open control socket: %w", err)
	}
	defer func() { _ = unix.Close(fd) }()

	flagsIfr, err := unix.NewIfreq(name)
	if err != nil {
		return false, 0, fmt.Errorf("build ifreq for %q: %w", name, err)
	}
	if err := unix.IoctlIfreq(fd, unix.SIOCGIFFLAGS, flagsIfr); err != nil {
		return false, 0, fmt.Errorf("SIOCGIFFLAGS %q: %w", name, err)
	}
	up = flagsIfr.Uint16()&unix.IFF_UP != 0

	mtuIfr, err := unix.NewIfreq(name)
	if err != nil {
		return up, 0, fmt.Errorf("build ifreq for %q: %w", name, err)
	}
	if err := unix.IoctlIfreq(fd, unix.SIOCGIFMTU, mtuIfr); err != nil {
		return up, 0, fmt.Errorf("SIOCGIFMTU %q: %w", name, err)
	}
	mtu = int(int32(mtuIfr.Uint32()))
	return up, mtu, nil
}
