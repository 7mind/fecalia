//go:build linux

package device

import (
	"fmt"

	"golang.org/x/sys/unix"
)

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
