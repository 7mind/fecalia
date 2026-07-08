package bind

import (
	"net/netip"
	"testing"
)

// TestSelectDeviceBinds is the T16 rework regression for the device-bind SELECTION
// rule (the two review criticisms fable reproduced under `unshare -Urn`). Device
// binding (SO_BINDTODEVICE + wildcard source — the roam-surviving mode) must be
// chosen ONLY when it is provably equivalent to pinning the configured
// source_addr AND no other path contends for the device; every other path must
// fall back to source-IP binding (the pre-T16 behaviour that lets distinct
// specific-IP sockets coexist on one port). It exercises selectDeviceBinds with a
// fake resolver so it needs no privilege / real interfaces.
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
			got := selectDeviceBinds(tc.srcs, build(tc.table))
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
