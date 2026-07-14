//go:build linux

package device

import (
	"fmt"

	"github.com/amnezia-vpn/amneziawg-go/tun"
	"golang.org/x/sys/unix"
)

// setTUNPersist toggles the TUNSETPERSIST flag on the TUN device's own fd (see
// tuntap(4), I7 / Q38). With persist=true the kernel keeps the interface after
// the daemon's last fd closes, so wanbond0 — and every address/route/rule the
// operator attached to it — survives a full daemon stop/start; the next Up
// re-adopts the SAME persistent device by name via CreateTUN's TUNSETIFF,
// preserving its ifindex. amneziawg-go's NativeTun.Close only closes the
// fd/netlink socket — it never issues RTM_DELLINK — so this flag alone suffices
// for the link to outlive Close, and Close needs no change.
//
// It is called UNCONDITIONALLY with cfg.TUNPersist, so persist=false explicitly
// CLEARS the flag: a device left persistent by a prior tun_persist=true run and
// re-adopted under tun_persist=false becomes non-persistent again and disappears
// on the next Close, restoring today's default teardown semantics exactly. On a
// freshly created (already non-persistent) TUN, TUNSETPERSIST(0) is a harmless
// idempotent no-op.
//
// The ioctl runs under the runtime poller's Control so it never races the fd's
// netpoll registration or forces the fd out of non-blocking mode (a bare
// File().Fd() would).
func setTUNPersist(tunDev tun.Device, persist bool) error {
	arg := 0
	if persist {
		arg = 1
	}
	sc, err := tunDev.File().SyscallConn()
	if err != nil {
		return fmt.Errorf("tun syscall conn: %w", err)
	}
	var ioctlErr error
	if err := sc.Control(func(fd uintptr) {
		ioctlErr = unix.IoctlSetInt(int(fd), unix.TUNSETPERSIST, arg)
	}); err != nil {
		return fmt.Errorf("tun control: %w", err)
	}
	if ioctlErr != nil {
		return fmt.Errorf("TUNSETPERSIST(%d): %w", arg, ioctlErr)
	}
	return nil
}
