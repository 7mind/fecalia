package device

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amnezia-vpn/amneziawg-go/tun/tuntest"

	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/dnsresolve"
)

// writeConcentratorConfig writes a single-path concentrator config with numPeers WireGuard peers
// to a 0600 temp file and loads it. With one peer the top-level psk is the sole authenticator
// (no per-peer psk); with more than one, each peer carries its own distinct name + psk (the G4
// multi-peer surface). listenPort is the concentrator's WG listen port (also the outer socket
// port Open binds on 127.0.0.1).
func writeConcentratorConfig(t *testing.T, numPeers int, listenPort int) *config.Config {
	t.Helper()
	privRaw, _ := genX25519(t)

	var peers strings.Builder
	for i := 0; i < numPeers; i++ {
		_, pubRaw := genX25519(t)
		fmt.Fprintf(&peers, "\n[[wireguard.peers]]\npublic_key = \"%s\"\nallowed_ips = [\"10.0.%d.0/24\"]\n",
			base64.StdEncoding.EncodeToString(pubRaw), i)
		if numPeers > 1 {
			// Multi-peer: each peer needs a unique name and its own distinct psk.
			fmt.Fprintf(&peers, "name = \"edge-%d\"\npsk = \"%s\"\n", i, randB64Key(t))
		}
	}

	body := fmt.Sprintf(`role = "concentrator"
psk = "%s"

[[paths]]
name = "a"
source_addr = "127.0.0.1"

[wireguard]
private_key = "%s"
listen_port = %d
%s`, randB64Key(t), base64.StdEncoding.EncodeToString(privRaw), listenPort, peers.String())

	path := filepath.Join(t.TempDir(), "concentrator.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatalf("chmod config: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load config: %v\n%s", err, body)
	}
	return cfg
}

// TestUpTwoPeerConcentratorWiresPerPeerState is the T93 device-level acceptance: a 2-peer
// concentrator config brought up through up() yields TWO bound peerStates, each with its own
// stable virtual endpoint. The deep per-peer prober/scheduler/psk assertions live in the bind
// package (TestConcentratorTwoPeersEachOwnWiring); this pins the device wiring end to end.
func TestUpTwoPeerConcentratorWiresPerPeerState(t *testing.T) {
	cfg := writeConcentratorConfig(t, 2, 53971)
	chtun := tuntest.NewChannelTUN()

	factoryCalls := 0
	factory := func() (dnsresolve.Resolver, error) {
		factoryCalls++
		return &dnsresolve.FakeResolver{}, nil
	}

	tun, err := up(cfg, discardLogger(t), chtun.TUN(), "wanbondtest0", factory)
	if err != nil {
		t.Fatalf("up on a 2-peer concentrator failed: %v", err)
	}
	defer tun.Close()

	// A concentrator peer carries no hostname endpoint spec (validate forbids it), so the DNS
	// resolver is provably never constructed (Q29 inertness).
	if factoryCalls != 0 {
		t.Fatalf("resolver factory invoked %d times for a concentrator, want 0", factoryCalls)
	}

	names := tun.bind.BoundPeerNames()
	if len(names) != 2 {
		t.Fatalf("bound peers = %v, want 2", names)
	}
	// The second identity is wired under its configured name; the embedded primary carries the
	// empty name (peers[0]).
	if names[1] != "edge-1" {
		t.Fatalf("second bound peer name = %q, want %q", names[1], "edge-1")
	}
	virts := tun.bind.PeerVirtEndpoints()
	if len(virts) != 2 {
		t.Fatalf("peer virtual endpoints = %d, want 2", len(virts))
	}
	if virts[0] == virts[1] {
		t.Fatal("the two peers share ONE virtual endpoint (invariant A1: one virt per peer)")
	}
}

// TestUpSinglePeerConcentratorOnePeerState is the T93 single-peer no-regression guard: a
// single-peer concentrator config yields EXACTLY one peerState (the embedded primary, empty
// name) — byte-identical wiring to the pre-G4 single-peer path.
func TestUpSinglePeerConcentratorOnePeerState(t *testing.T) {
	cfg := writeConcentratorConfig(t, 1, 53972)
	chtun := tuntest.NewChannelTUN()

	factory := func() (dnsresolve.Resolver, error) {
		return &dnsresolve.FakeResolver{}, nil
	}

	tun, err := up(cfg, discardLogger(t), chtun.TUN(), "wanbondtest0", factory)
	if err != nil {
		t.Fatalf("up on a single-peer concentrator failed: %v", err)
	}
	defer tun.Close()

	if names := tun.bind.BoundPeerNames(); len(names) != 1 || names[0] != "" {
		t.Fatalf("bound peers = %v, want exactly one primary (empty name)", names)
	}
	if virts := tun.bind.PeerVirtEndpoints(); len(virts) != 1 {
		t.Fatalf("peer virtual endpoints = %d, want 1", len(virts))
	}
}
