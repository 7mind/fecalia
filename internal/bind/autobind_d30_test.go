package bind

import (
	"net/netip"
	"testing"

	"github.com/7mind/wanbond/internal/config"
)

// TestAutoRuntimeDeviceBindClosesD30 is the D30 regression: a runtime-added or promoted
// AUTO-mode path now device-binds when its source resolves to a single-family interface that no
// OTHER configured path contends for (Open's selectDeviceBinds heuristic), instead of the pre-D30
// unconditional source-IP-pin. Before the fix, AddPath/reconcileDeferred routed auto paths only
// through resolveForcedDeviceBind — which returns "" for auto — so a runtime auto path NEVER
// device-bound (autoRuntimeDeviceBind did not exist); this test asserts the uncontended path now
// device-binds and a contended one still source-IP-pins, using a fake interface resolver so no
// real interface need appear on the host.
func TestAutoRuntimeDeviceBindClosesD30(t *testing.T) {
	psk := testKey(t, 0x30)
	clk := newFakeClock()
	// Three configured auto paths: "a" and "c" share interface wan0 (contended); "b" is alone on
	// wan1 (uncontended, single family).
	paths := []config.Path{
		{Name: "a", SourceAddr: netip.MustParseAddr("10.0.0.1"), Bind: config.BindModeAuto},
		{Name: "b", SourceAddr: netip.MustParseAddr("10.0.1.1"), Bind: config.BindModeAuto},
		{Name: "c", SourceAddr: netip.MustParseAddr("10.0.0.2"), Bind: config.BindModeAuto},
	}
	m, _, _ := newProbingMultipath(t, paths, psk, clk)
	// Deterministic fake interface resolution (no net.Interfaces()): 10.0.0.x -> wan0 (single
	// family), 10.0.1.1 -> wan1 (single family), anything else unresolvable.
	m.resolveIface = func(s netip.Addr) ifaceInfo {
		switch s.String() {
		case "10.0.0.1", "10.0.0.2":
			return ifaceInfo{dev: "wan0", familyCount: 1}
		case "10.0.1.1":
			return ifaceInfo{dev: "wan1", familyCount: 1}
		}
		return ifaceInfo{}
	}

	// b's source resolves to wan1, which no other configured path uses -> device-bind (the D30
	// fix; pre-fix this was always "").
	if got := m.autoRuntimeDeviceBind(netip.MustParseAddr("10.0.1.1"), config.BindModeAuto); got != "wan1" {
		t.Fatalf("uncontended single-family auto path: dev = %q, want %q (D30: an auto runtime path should device-bind its uncontended interface)", got, "wan1")
	}
	// a's source resolves to wan0, but c ALSO resolves to wan0 -> device contention -> source-IP-pin.
	if got := m.autoRuntimeDeviceBind(netip.MustParseAddr("10.0.0.1"), config.BindModeAuto); got != "" {
		t.Fatalf("contended auto path (shares wan0 with c): dev = %q, want \"\" (selectDeviceBinds device-uniqueness must decline)", got)
	}
	// A multi-address interface (familyCount > 1) must NOT device-bind even when otherwise alone.
	m.resolveIface = func(s netip.Addr) ifaceInfo {
		if s.String() == "10.0.1.1" {
			return ifaceInfo{dev: "wan1", familyCount: 2}
		}
		return ifaceInfo{}
	}
	if got := m.autoRuntimeDeviceBind(netip.MustParseAddr("10.0.1.1"), config.BindModeAuto); got != "" {
		t.Fatalf("multi-address interface: dev = %q, want \"\" (family-count>1 must decline, source_addr pin cannot be guaranteed)", got)
	}
	// A non-auto mode is never decided here (forced-device is resolveDeviceBind's job; source pins).
	if got := m.autoRuntimeDeviceBind(netip.MustParseAddr("10.0.1.1"), config.BindModeSource); got != "" {
		t.Fatalf("BindModeSource: dev = %q, want \"\"", got)
	}
}
