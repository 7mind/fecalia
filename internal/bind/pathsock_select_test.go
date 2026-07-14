package bind

import (
	"net"
	"net/netip"
	"testing"

	"github.com/7mind/wanbond/internal/config"
)

// autoModes returns a config.BindMode slice of n BindModeAuto entries, the mode
// every pre-I5 selectDeviceBinds test exercised implicitly.
func autoModes(n int) []config.BindMode {
	modes := make([]config.BindMode, n)
	for i := range modes {
		modes[i] = config.BindModeAuto
	}
	return modes
}

// TestSelectDeviceBinds is the T16 rework regression for the device-bind SELECTION
// rule (the two review criticisms fable reproduced under `unshare -Urn`), now run
// under explicit BindModeAuto (I5) to pin it as the REGRESSION GUARD that auto
// reproduces the pre-I5 heuristic byte-for-byte on these fixtures. Device binding
// (SO_BINDTODEVICE + wildcard source — the roam-surviving mode) must be chosen
// ONLY when it is provably equivalent to pinning the configured source_addr AND no
// other path contends for the device; every other path must fall back to
// source-IP binding (the pre-T16 behaviour that lets distinct specific-IP sockets
// coexist on one port). It exercises selectDeviceBinds with a fake resolver so it
// needs no privilege / real interfaces.
func TestSelectDeviceBinds(t *testing.T) {
	addr := func(s string) netip.Addr { return netip.MustParseAddr(s) }

	// build wraps a source-address -> ifaceInfo table as a resolver.
	build := func(table map[netip.Addr]ifaceInfo) func(netip.Addr) ifaceInfo {
		return func(a netip.Addr) ifaceInfo { return table[a] }
	}

	tests := []struct {
		name    string
		srcs    []netip.Addr
		table   map[netip.Addr]ifaceInfo
		wantDev []string
	}{
		{
			// A solo path on a single-address interface: device-bind (roam benefit).
			name:    "solo single-address interface device-binds",
			srcs:    []netip.Addr{addr("198.51.100.10")},
			table:   map[netip.Addr]ifaceInfo{addr("198.51.100.10"): {dev: "eth0", familyCount: 1}},
			wantDev: []string{"eth0"},
		},
		{
			// Criticism 1: two paths whose sources live on the SAME interface. Neither
			// may device-bind (the second wildcard+device socket collides EADDRINUSE);
			// both source-IP-bind so their two distinct specific-IP sockets coexist.
			name: "shared interface -> all source-IP-bound",
			srcs: []netip.Addr{addr("192.0.2.10"), addr("192.0.2.11")},
			table: map[netip.Addr]ifaceInfo{
				addr("192.0.2.10"): {dev: "eth0", familyCount: 2},
				addr("192.0.2.11"): {dev: "eth0", familyCount: 2},
			},
			wantDev: []string{"", ""},
		},
		{
			// Criticism 2: a solo path on a MULTI-address interface. A wildcard+device
			// socket would let the kernel pick a different source, voiding the pin, so
			// it must source-IP-bind despite being the only path on the device.
			name:    "solo multi-address interface -> source-IP-bound",
			srcs:    []netip.Addr{addr("203.0.113.5")},
			table:   map[netip.Addr]ifaceInfo{addr("203.0.113.5"): {dev: "eth0", familyCount: 2}},
			wantDev: []string{""},
		},
		{
			// A source that resolves to no interface (loopback / unresolved) never
			// device-binds — it falls straight through to source-IP binding.
			name:    "unresolved source -> source-IP-bound",
			srcs:    []netip.Addr{addr("127.0.0.1")},
			table:   map[netip.Addr]ifaceInfo{},
			wantDev: []string{""},
		},
		{
			// The mixed real case: two solo single-address uplinks on distinct devices
			// both device-bind, while a third pair sharing a device does not — the
			// decision is per-path, not all-or-nothing.
			name: "two solo uplinks device-bind, shared pair does not",
			srcs: []netip.Addr{addr("198.51.100.10"), addr("203.0.113.20"), addr("192.0.2.10"), addr("192.0.2.11")},
			table: map[netip.Addr]ifaceInfo{
				addr("198.51.100.10"): {dev: "wan0", familyCount: 1},
				addr("203.0.113.20"):  {dev: "wan1", familyCount: 1},
				addr("192.0.2.10"):    {dev: "lan0", familyCount: 2},
				addr("192.0.2.11"):    {dev: "lan0", familyCount: 2},
			},
			wantDev: []string{"wan0", "wan1", "", ""},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := selectDeviceBinds(tc.srcs, autoModes(len(tc.srcs)), build(tc.table))
			if len(got) != len(tc.wantDev) {
				t.Fatalf("selectDeviceBinds returned %d entries, want %d (%v)", len(got), len(tc.wantDev), got)
			}
			for i := range got {
				if got[i] != tc.wantDev[i] {
					t.Fatalf("path %d bind = %q, want %q (full: %v want %v)", i, got[i], tc.wantDev[i], got, tc.wantDev)
				}
			}
		})
	}
}

// TestSelectDeviceBindsForcedSource is the D38 trap regression (I5): a SOLE path on
// a single-address interface is EXACTLY the case BindModeAuto device-binds (see
// "solo single-address interface device-binds" above) — and exactly the source-
// policy-routed VLAN-per-WAN topology (defects:D38) where a wildcard+device socket
// silently defeats `ip rule from <source_addr>`. BindModeSource must force
// source-IP pinning here regardless, discriminating it from BindModeAuto on the
// identical fixture.
func TestSelectDeviceBindsForcedSource(t *testing.T) {
	addr := netip.MustParseAddr("198.51.100.10")
	table := map[netip.Addr]ifaceInfo{addr: {dev: "eth0", familyCount: 1}}
	resolve := func(a netip.Addr) ifaceInfo { return table[a] }

	got := selectDeviceBinds([]netip.Addr{addr}, []config.BindMode{config.BindModeSource}, resolve)
	if len(got) != 1 || got[0] != "" {
		t.Fatalf("selectDeviceBinds(BindModeSource) = %v, want [\"\"] (never SO_BINDTODEVICE)", got)
	}

	// Sanity: the SAME fixture under BindModeAuto device-binds — confirms the
	// forced-source result above is actually discriminating the mode, not merely
	// reflecting an unresolvable/multi-address fixture.
	gotAuto := selectDeviceBinds([]netip.Addr{addr}, autoModes(1), resolve)
	if len(gotAuto) != 1 || gotAuto[0] != "eth0" {
		t.Fatalf("selectDeviceBinds(BindModeAuto) = %v, want [\"eth0\"] (the D38 trap this test discriminates against)", gotAuto)
	}
}

// TestSelectDeviceBindsForcedDevice covers BindModeDevice (I5): it device-binds
// whenever the source resolves to an interface — even one BindModeAuto would have
// refused (multi-address, or shared with another path) — and falls back to
// source-IP binding only when the interface cannot be resolved at all.
func TestSelectDeviceBindsForcedDevice(t *testing.T) {
	addr := func(s string) netip.Addr { return netip.MustParseAddr(s) }

	t.Run("resolvable multi-address interface still device-binds", func(t *testing.T) {
		// BindModeAuto refuses this fixture (see "solo multi-address interface ->
		// source-IP-bound" above); BindModeDevice must device-bind it anyway.
		src := addr("203.0.113.5")
		table := map[netip.Addr]ifaceInfo{src: {dev: "eth0", familyCount: 2}}
		resolve := func(a netip.Addr) ifaceInfo { return table[a] }

		got := selectDeviceBinds([]netip.Addr{src}, []config.BindMode{config.BindModeDevice}, resolve)
		if len(got) != 1 || got[0] != "eth0" {
			t.Fatalf("selectDeviceBinds(BindModeDevice, multi-address) = %v, want [\"eth0\"]", got)
		}
	})

	t.Run("unresolvable interface falls back to source-IP binding", func(t *testing.T) {
		src := addr("127.0.0.1") // loopback: interfaceInfo never resolves it (see TestFamilyBindCount's siblings)
		resolve := func(netip.Addr) ifaceInfo { return ifaceInfo{} }

		got := selectDeviceBinds([]netip.Addr{src}, []config.BindMode{config.BindModeDevice}, resolve)
		if len(got) != 1 || got[0] != "" {
			t.Fatalf("selectDeviceBinds(BindModeDevice, unresolvable) = %v, want [\"\"] (fallback to source-IP binding)", got)
		}
	})
}

// TestSelectForcedDeviceBind is the T106 round-2 gap closure: resolveForcedDeviceBind
// (AddPath / the T55 deferred-path reconcile — a single, isolated forced-device bind
// with no other-path contention, unlike Open's selectDeviceBinds) previously called
// net.Interfaces() inline, so nothing exercised its per-mode DECISION without a real
// interface. selectForcedDeviceBind splits that decision out (mirroring
// planPathBinds/selectDeviceBinds' split), so it is tested here through a fake
// resolver across all three BindModes plus the unresolvable-interface fallback. This
// FAILS if resolveForcedDeviceBind (or this decision) is neutered to always return ""
// regardless of mode — the exact round-1 review gap (mutating it to unconditionally
// return "" passed the full pre-round-2 bind suite).
func TestSelectForcedDeviceBind(t *testing.T) {
	src := netip.MustParseAddr("198.51.100.10")
	resolvable := func(netip.Addr) ifaceInfo { return ifaceInfo{dev: "wan0", familyCount: 1} }
	unresolvable := func(netip.Addr) ifaceInfo { return ifaceInfo{} }

	tests := []struct {
		name    string
		mode    config.BindMode
		resolve func(netip.Addr) ifaceInfo
		want    string
	}{
		{"source never device-binds", config.BindModeSource, resolvable, ""},
		{"auto never device-binds here (D30)", config.BindModeAuto, resolvable, ""},
		{"device+resolvable uses the resolved dev", config.BindModeDevice, resolvable, "wan0"},
		{"device+unresolvable falls back to source-IP binding", config.BindModeDevice, unresolvable, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := selectForcedDeviceBind(src, tc.mode, tc.resolve)
			if got != tc.want {
				t.Fatalf("selectForcedDeviceBind(%s) = %q, want %q", tc.mode, got, tc.want)
			}
		})
	}
}

// TestResolveForcedDeviceBindAgreesWithRealInterfaces exercises resolveForcedDeviceBind
// itself — the real net.Interfaces()-snapshotting wrapper, not the fake-resolver
// decision tests above — so it is sensitive to the EXACT round-1 review mutation
// (neutering resolveForcedDeviceBind to unconditionally return "", which "made the
// entire runtime-path device-bind activation inert" yet passed the full pre-round-2
// bind suite): TestSelectForcedDeviceBind calls selectForcedDeviceBind directly and
// never reaches resolveForcedDeviceBind's own body, so it alone cannot catch that
// mutation. It discovers its own fixture (a real non-loopback, single-address
// interface, the same shape TestSelectDeviceBinds' "solo single-address interface"
// case fakes) rather than hardcoding one, so it stays portable: it asks interfaceInfo
// directly what that candidate resolves to, then requires
// resolveForcedDeviceBind(candidate, BindModeDevice) to agree. It skips — never
// fails — on a host with no such interface (e.g. a bare loopback-only container),
// the one environment it structurally cannot exercise; every dev/CI host this repo
// targets has at least one.
func TestResolveForcedDeviceBindAgreesWithRealInterfaces(t *testing.T) {
	ifaces, err := net.Interfaces()
	if err != nil {
		t.Skipf("net.Interfaces: %v", err)
	}
	var candidate netip.Addr
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip, ok := netip.AddrFromSlice(ipn.IP)
			if !ok {
				continue
			}
			if info := interfaceInfo(ip, ifaces); info.dev != "" && info.familyCount == 1 {
				candidate = ip
				break
			}
		}
		if candidate.IsValid() {
			break
		}
	}
	if !candidate.IsValid() {
		t.Skip("host has no non-loopback single-address interface to exercise resolveForcedDeviceBind against")
	}

	if got := resolveForcedDeviceBind(candidate, config.BindModeSource); got != "" {
		t.Fatalf("resolveForcedDeviceBind(BindModeSource) = %q, want \"\"", got)
	}
	if got := resolveForcedDeviceBind(candidate, config.BindModeAuto); got != "" {
		t.Fatalf("resolveForcedDeviceBind(BindModeAuto) = %q, want \"\"", got)
	}
	want := interfaceInfo(candidate, ifaces).dev
	if want == "" {
		t.Fatal("test bug: candidate's own interfaceInfo resolved to \"\", the picker's filter above should have excluded it")
	}
	if got := resolveForcedDeviceBind(candidate, config.BindModeDevice); got != want {
		t.Fatalf("resolveForcedDeviceBind(BindModeDevice) = %q, want %q (the resolved interface) -- fails if resolveForcedDeviceBind is neutered to always return \"\"", got, want)
	}
}

// TestPlanPathBinds exercises the real planPathBinds wrapper (real net.Interfaces()
// snapshot, no injected resolver) end to end: a loopback source never resolves to a
// non-loopback interface, so it source-IP-binds under every mode — the mode plumbing
// through planPathBinds compiles and behaves the same as selectDeviceBinds direct.
func TestPlanPathBinds(t *testing.T) {
	srcs := []netip.Addr{netip.MustParseAddr("127.0.0.1")}
	for _, mode := range []config.BindMode{config.BindModeSource, config.BindModeDevice, config.BindModeAuto} {
		t.Run(string(mode), func(t *testing.T) {
			got := planPathBinds(srcs, []config.BindMode{mode})
			if len(got) != 1 || got[0] != "" {
				t.Fatalf("planPathBinds(%s, loopback) = %v, want [\"\"]", mode, got)
			}
		})
	}
}

// TestFamilyBindCount is the D13 regression for interfaceInfo's family-count rule.
// An up interface virtually always carries a kernel fe80::/10 link-local alongside
// its configured global v6 address; counting the link-local pushed familyCount>=2
// so a GLOBAL v6 source never qualified for device binding (T16 re-roam survival).
// The kernel never source-selects a link-local for a global destination, so the
// link-local must be EXCLUDED from the count of a global v6 source — while a v4
// source, and a link-local v6 source, count every same-family address unchanged.
func TestFamilyBindCount(t *testing.T) {
	addr := func(s string) netip.Addr { return netip.MustParseAddr(s) }
	globalV6 := addr("2001:db8::10")
	linkLocalV6 := addr("fe80::1")
	otherGlobalV6 := addr("2001:db8::11")

	tests := []struct {
		name       string
		want       netip.Addr
		addrs      []netip.Addr
		wantFamily int
		wantOwns   bool
	}{
		{
			// The defect case: a global v6 source alongside the kernel link-local.
			// The link-local must not count, so familyCount == 1 and the source
			// still qualifies for device binding.
			name:       "global v6 with kernel link-local excludes link-local",
			want:       globalV6,
			addrs:      []netip.Addr{globalV6, linkLocalV6},
			wantFamily: 1,
			wantOwns:   true,
		},
		{
			// Two GLOBAL v6 addresses genuinely contend: both count, so the source
			// does not qualify (a wildcard+device socket could source from either).
			name:       "two global v6 addresses both count",
			want:       globalV6,
			addrs:      []netip.Addr{globalV6, otherGlobalV6, linkLocalV6},
			wantFamily: 2,
			wantOwns:   true,
		},
		{
			// A link-local v6 SOURCE is unaffected: every same-family (incl.
			// link-local) address counts, exactly as before.
			name:       "link-local v6 source counts link-locals",
			want:       linkLocalV6,
			addrs:      []netip.Addr{linkLocalV6, addr("fe80::2")},
			wantFamily: 2,
			wantOwns:   true,
		},
		{
			// A v4 source is unaffected by the v6 rule; a coexisting v6 (of either
			// scope) is a different family and never counts.
			name:       "v4 source unaffected by v6 addresses",
			want:       addr("192.0.2.10"),
			addrs:      []netip.Addr{addr("192.0.2.10"), globalV6, linkLocalV6},
			wantFamily: 1,
			wantOwns:   true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			familyCount, owns := familyBindCount(tc.want, tc.addrs)
			if familyCount != tc.wantFamily {
				t.Errorf("familyCount = %d, want %d", familyCount, tc.wantFamily)
			}
			if owns != tc.wantOwns {
				t.Errorf("owns = %v, want %v", owns, tc.wantOwns)
			}
		})
	}
}
