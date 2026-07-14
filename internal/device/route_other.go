//go:build !linux

package device

import (
	"errors"
	"net/netip"
)

// installRoutes/removeRoutes are unavailable off Linux (route programming is done via
// rtnetlink, a Linux facility). wanbond targets Linux; these stubs only keep the
// package cross-compilable, mirroring linkup_other.go. They are reached only when a
// peer opts into mode=default-route (the caller skips them for an empty route set), so
// a default (no default-route) config cross-compiles and runs unchanged off Linux.
func installRoutes(string, []netip.Prefix) error {
	return errors.New("device: installing default-route wiring is only supported on Linux")
}

func removeRoutes(string, []netip.Prefix) error {
	return errors.New("device: removing default-route wiring is only supported on Linux")
}
