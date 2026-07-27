//go:build !linux

package device

import "errors"

// ifUp is unavailable off Linux (SIOCSIFFLAGS is a Linux ioctl). wanbond targets
// Linux; this stub only keeps the package cross-compilable, mirroring
// internal/bind/pathsock_other.go.
func ifUp(string) error {
	return errors.New("device: bringing the interface up is only supported on Linux")
}

// ifState is unavailable off Linux (SIOCGIFFLAGS/SIOCGIFMTU are Linux ioctls); see ifUp.
func ifState(string) (up bool, mtu int, err error) {
	return false, 0, errors.New("device: inspecting interface state is only supported on Linux")
}

// setLinkMTU is unavailable off Linux (SIOCSIFMTU is a Linux ioctl); see ifUp.
func setLinkMTU(string, int) error {
	return errors.New("device: setting the interface MTU is only supported on Linux")
}

// setLinkGSOMaxSize is unavailable off Linux (IFLA_GSO_MAX_SIZE is a Linux
// rtnetlink attribute); see ifUp.
func setLinkGSOMaxSize(string, uint32) error {
	return errors.New("device: setting the interface GSO limit is only supported on Linux")
}

// readLinkGSOMaxSize is unavailable off Linux; see setLinkGSOMaxSize.
func readLinkGSOMaxSize(string) (uint32, error) {
	return 0, errors.New("device: reading the interface GSO limit is only supported on Linux")
}
