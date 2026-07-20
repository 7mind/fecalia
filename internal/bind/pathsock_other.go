//go:build !linux

package bind

import "errors"

// bindToDevice is unavailable off Linux (SO_BINDTODEVICE is Linux-specific). The
// error makes listenPath fall back to binding the specific source address. wanbond
// targets Linux; this stub only keeps the package cross-compilable.
func bindToDevice(uintptr, string) error {
	return errors.New("bind: SO_BINDTODEVICE is only supported on Linux")
}

// setDontFragment is a portable no-op off Linux: the IP(V6)_MTU_DISCOVER=PMTUDISC_DO
// setsockopt that pins the Don't-Fragment bit (T201) is Linux-specific. wanbond targets
// Linux; this stub only keeps the package cross-compilable. It returns nil (not an
// error) because an unavailable DF policy must not fail path binding on a non-target
// platform — the socket simply keeps the kernel's default fragmentation behaviour.
func setDontFragment(uintptr, bool) error {
	return nil
}
