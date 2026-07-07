//go:build !linux

package bind

import "errors"

// bindToDevice is unavailable off Linux (SO_BINDTODEVICE is Linux-specific). The
// error makes listenPath fall back to binding the specific source address. wanbond
// targets Linux; this stub only keeps the package cross-compilable.
func bindToDevice(uintptr, string) error {
	return errors.New("bind: SO_BINDTODEVICE is only supported on Linux")
}
