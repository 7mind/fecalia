//go:build !linux

package device

import (
	"errors"

	"github.com/amnezia-vpn/amneziawg-go/tun"
)

// setTUNPersist is unavailable off Linux (TUNSETPERSIST is a Linux ioctl).
// wanbond targets Linux; this stub only keeps the package cross-compilable,
// mirroring internal/device/linkup_other.go.
func setTUNPersist(tun.Device, bool) error {
	return errors.New("device: TUN persistence is only supported on Linux")
}
