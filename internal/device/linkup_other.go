//go:build !linux

package device

import "errors"

// ifUp is unavailable off Linux (SIOCSIFFLAGS is a Linux ioctl). wanbond targets
// Linux; this stub only keeps the package cross-compilable, mirroring
// internal/bind/pathsock_other.go.
func ifUp(string) error {
	return errors.New("device: bringing the interface up is only supported on Linux")
}
