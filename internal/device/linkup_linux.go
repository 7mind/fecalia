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
