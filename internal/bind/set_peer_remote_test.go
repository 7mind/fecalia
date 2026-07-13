package bind

import (
	"net/netip"
	"testing"
)

// TestSetPeerRemoteRepointsEveryPath is the bind half of the T57 hub-failover switch:
// SetPeerRemote must repoint EVERY path's wire remote at the standby concentrator —
// OVERRIDING an already-learned remote — and update the default remote so a subsequent
// Open seeds fresh paths at the standby. This is a DELIBERATE contrast with ParseEndpoint,
// which only fills a path's remote when it is UNSET; a hub switch, by contrast, retargets
// the whole bond, so it must override. Seeding both paths with a distinct "active" remote
// first makes the assertion non-vacuous: it fails on any implementation that skips a path
// that already has a remote (the ParseEndpoint semantics).
func TestSetPeerRemoteRepointsEveryPath(t *testing.T) {
	psk := testKey(t, 0x5A)
	m, err := newMultipath(t, loopbackPaths(2), psk)
	if err != nil {
		t.Fatalf("NewMultipath: %v", err)
	}
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	// Both paths have already learned/configured a remote toward the ACTIVE concentrator.
	active := netip.MustParseAddrPort("203.0.113.1:51820")
	for _, ps := range m.paths {
		ps.setRemote(active)
	}

	// Hub loss → switch the peer remote to the STANDBY concentrator.
	standby := netip.MustParseAddrPort("198.51.100.7:51820")
	m.SetPeerRemote(standby)

	for i, ps := range m.paths {
		got, ok := ps.getRemote()
		if !ok || got != standby {
			t.Fatalf("path %d remote = %v (known=%v), want standby %v (SetPeerRemote must override an existing remote)", i, got, ok, standby)
		}
	}

	// The default remote is updated too, so the next Open seeds fresh paths at the standby.
	m.mu.Lock()
	dr, has := m.defaultRemote, m.hasDefaultRemote
	m.mu.Unlock()
	if !has || dr != standby {
		t.Fatalf("defaultRemote = %v (set=%v), want %v", dr, has, standby)
	}
}
