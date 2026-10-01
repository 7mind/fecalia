package bind

import (
	"net"
	"net/netip"
	"testing"

	"github.com/7mind/wanbond/internal/config"
)

// TestPromotedDeferredPathReportsBindModeAndDevice: a path promoted from deferred
// reports the same bind mode and bound device in PathTraffic as the same definition
// admitted by AddPath.
func TestPromotedDeferredPathReportsBindModeAndDevice(t *testing.T) {
	const device = "wan0"
	def := config.Path{Name: "late", SourceAddr: netip.MustParseAddr(unassignableSource), Bind: config.BindModeDevice}
	bound := config.Path{Name: "bound", SourceAddr: netip.MustParseAddr("127.0.0.1")}

	resolve := func(_ netip.Addr, mode config.BindMode) string {
		if mode == config.BindModeDevice {
			return device
		}
		return ""
	}
	listen := func(_ netip.Addr, _ uint16, _ string) (*net.UDPConn, error, error) {
		c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
		return c, nil, err
	}
	traffic := func(m *Multipath) PathTraffic {
		t.Helper()
		for _, p := range m.PeerSnapshots()[0].Paths {
			if p.Name == def.Name {
				return p
			}
		}
		t.Fatalf("path %q is not live", def.Name)
		return PathTraffic{}
	}

	added, _ := newWarnCapturingMultipath(t, []config.Path{bound}, testKey(t, 0xB1))
	added.resolveDeviceBind = resolve
	added.addPathListen = listen
	if err := added.AddPath(def); err != nil {
		t.Fatalf("AddPath: %v", err)
	}
	want := traffic(added)
	if want.BindMode != config.BindModeDevice || want.BoundDevice != device {
		t.Fatalf("AddPath reports bind mode %q device %q, want %q %q", want.BindMode, want.BoundDevice, config.BindModeDevice, device)
	}

	promoted, _ := newWarnCapturingMultipath(t, []config.Path{bound, def}, testKey(t, 0xB2))
	if len(promoted.deferred) != 1 {
		t.Fatalf("precondition: deferred=%d, want 1", len(promoted.deferred))
	}
	promoted.resolveDeviceBind = resolve
	promoted.deferredListen = listen
	promoted.reconcileDeferred()
	if len(promoted.deferred) != 0 {
		t.Fatalf("path did not promote: deferred=%d, want 0", len(promoted.deferred))
	}
	got := traffic(promoted)
	if got.BindMode != want.BindMode || got.BoundDevice != want.BoundDevice {
		t.Fatalf("promoted path reports bind mode %q device %q, want %q %q as AddPath reports",
			got.BindMode, got.BoundDevice, want.BindMode, want.BoundDevice)
	}
}
