//go:build !linux

package device

import "errors"

type linkGSOLimits struct {
	MaxSize     uint32
	MaxSegments uint32
}

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

func setLinkGSOLimits(string, linkGSOLimits) error {
	return errors.New("device: setting interface GSO limits is only supported on Linux")
}

func readLinkGSOLimits(string) (linkGSOLimits, error) {
	return linkGSOLimits{}, errors.New("device: reading interface GSO limits is only supported on Linux")
}
