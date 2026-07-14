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
	"github.com/7mind/wanbond/internal/frame"
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
	// Both identities are wired under their configured names — including the embedded
	// primary (peers[0]), which device.Up renames via SetPrimaryPeerName once a second
	// peer is configured (D58).
	if names[0] != "edge-0" {
		t.Fatalf("primary bound peer name = %q, want %q", names[0], "edge-0")
	}
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

// writeConcentratorConfigKnownPSKs writes a 2-peer, single-path concentrator config whose two
// peers carry the given DISTINCT, KNOWN base64 psks, so a test can verify each bound peer is
// keyed on its OWN psk end to end. Peer order matches config.PeerIdentities: peer 0 (the embedded
// primary, "edge-0") is keyed on psk0B64, peer 1 ("edge-1") on psk1B64. The top-level psk is a
// throwaway (a multi-peer concentrator draws each peer's effective psk from the per-peer psk).
func writeConcentratorConfigKnownPSKs(t *testing.T, listenPort int, psk0B64, psk1B64 string) *config.Config {
	t.Helper()
	privRaw, _ := genX25519(t)
	_, pub0 := genX25519(t)
	_, pub1 := genX25519(t)
	body := fmt.Sprintf(`role = "concentrator"
psk = "%s"

[[paths]]
name = "a"
source_addr = "127.0.0.1"

[wireguard]
private_key = "%s"
listen_port = %d

[[wireguard.peers]]
public_key = "%s"
allowed_ips = ["10.0.0.0/24"]
name = "edge-0"
psk = "%s"

[[wireguard.peers]]
public_key = "%s"
allowed_ips = ["10.0.1.0/24"]
name = "edge-1"
psk = "%s"
`, randB64Key(t), base64.StdEncoding.EncodeToString(privRaw), listenPort,
		base64.StdEncoding.EncodeToString(pub0), psk0B64,
		base64.StdEncoding.EncodeToString(pub1), psk1B64)

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

// decodesAsProbe reports whether raw decodes and MAC-verifies as a PROBE under psk — the
// operational test of "a prober is keyed on THIS psk": a probe verifies under a codec of its own
// psk and under no other.
func decodesAsProbe(t *testing.T, psk config.Key, raw []byte) bool {
	t.Helper()
	codec, err := frame.NewCodec(psk)
	if err != nil {
		t.Fatalf("build codec: %v", err)
	}
	fr, err := codec.Decode(raw)
	if err != nil {
		return false
	}
	_, ok := fr.(frame.Probe)
	return ok
}

// TestUpTwoPeerConcentratorKeysEachPeerOnItsOwnPSK is the T93 device-level MUTATION guard for the
// per-peer PSK wiring the concentrator loop performs in up() — buildScheduler(cfg, id.PSK, ...)
// for each additional peer's prober set (device.go:301) and AddConcentratorPeer(id.Name, id.PSK,
// ...) registering the peer under its own psk (device.go:306). Both mutants replace id.PSK with
// ids[0].PSK (keying an additional peer on the PRIMARY's psk) yet leave the rest of the suite
// green — the bind tests supply their OWN factories and the other device tests only check
// names/virt counts. This test brings up a 2-peer concentrator with KNOWN, DISTINCT per-peer psks
// through the real up() wiring and asserts the additional peer (edge-1) is keyed end to end on its
// OWN psk, on BOTH datapath planes:
//   - PROBER (send): edge-1's boot PROBE verifies under its own psk and NOT the primary's — kills
//     the device.go:301 mutant (which mints edge-1's prober under ids[0].PSK);
//   - REFLECTOR (receive): edge-1's Reflector authenticates a probe minted under its own psk and
//     REJECTS one minted under the primary's — kills the device.go:306 mutant (which registers
//     edge-1's codec/reflector under ids[0].PSK).
func TestUpTwoPeerConcentratorKeysEachPeerOnItsOwnPSK(t *testing.T) {
	// Two KNOWN, distinct 32-byte psks. raw0 keys the primary (peers[0], "edge-0"); raw1 keys
	// "edge-1" (peers[1]) — the peer the mutated device.go:301/306 lines would mis-key.
	var raw0, raw1 [32]byte
	for i := range raw0 {
		raw0[i] = 0xA0
		raw1[i] = 0xB1
	}
	psk0 := keyFromRaw(t, raw0[:])
	psk1 := keyFromRaw(t, raw1[:])
	if psk0.Bytes() == psk1.Bytes() {
		t.Fatal("fixture setup: the two peer psks must differ")
	}
	cfg := writeConcentratorConfigKnownPSKs(t, 53973,
		base64.StdEncoding.EncodeToString(raw0[:]), base64.StdEncoding.EncodeToString(raw1[:]))

	chtun := tuntest.NewChannelTUN()
	factory := func() (dnsresolve.Resolver, error) { return &dnsresolve.FakeResolver{}, nil }
	tun, err := up(cfg, discardLogger(t), chtun.TUN(), "wanbondtest0", factory)
	if err != nil {
		t.Fatalf("up on a 2-peer concentrator failed: %v", err)
	}
	defer tun.Close()

	if names := tun.bind.BoundPeerNames(); len(names) != 2 || names[0] != "edge-0" || names[1] != "edge-1" {
		t.Fatalf("bound peers = %v, want [\"edge-0\" \"edge-1\"]", names)
	}

	// --- PROBER plane (device.go:301): edge-1's boot PROBE authenticates under psk1, not psk0. ---
	probe1, err := tun.bind.PeerBootProbe(1, 0)
	if err != nil {
		t.Fatalf("PeerBootProbe(edge-1): %v", err)
	}
	if !decodesAsProbe(t, psk1, probe1) {
		t.Fatal("edge-1's boot probe does NOT verify under its OWN psk — its prober is keyed on the wrong peer's psk (device.go:301)")
	}
	if decodesAsProbe(t, psk0, probe1) {
		t.Fatal("edge-1's boot probe verifies under the PRIMARY's psk — its prober was minted under ids[0].PSK (device.go:301 mutant)")
	}
	// The primary is independently keyed on psk0 (proves the two peers are genuinely distinct, not
	// coincidentally both psk1).
	probe0, err := tun.bind.PeerBootProbe(0, 0)
	if err != nil {
		t.Fatalf("PeerBootProbe(primary): %v", err)
	}
	if !decodesAsProbe(t, psk0, probe0) || decodesAsProbe(t, psk1, probe0) {
		t.Fatal("the primary peer is not keyed on psk0")
	}

	// --- REFLECTOR plane (device.go:306): edge-1's reflector authenticates a psk1 probe, rejects
	// a psk0 one. This is the plane the prober check above does NOT cover — device.go:306 keys the
	// peer's codec/reflector, not its (separately-built) prober. ---
	probeUnderP1, err := frame.Encode(psk1, frame.Probe{PathID: 0, ProbeSeq: 1, TimestampNanos: 123, SessionID: 0xE1})
	if err != nil {
		t.Fatalf("encode probe under psk1: %v", err)
	}
	if _, err := tun.bind.PeerReflect(1, probeUnderP1); err != nil {
		t.Fatalf("edge-1's reflector must authenticate a probe under its OWN psk, got: %v (reflector keyed on the wrong psk — device.go:306)", err)
	}
	probeUnderP0, err := frame.Encode(psk0, frame.Probe{PathID: 0, ProbeSeq: 1, TimestampNanos: 123, SessionID: 0xE0})
	if err != nil {
		t.Fatalf("encode probe under psk0: %v", err)
	}
	if _, err := tun.bind.PeerReflect(1, probeUnderP0); err == nil {
		t.Fatal("edge-1's reflector authenticated a probe minted under the PRIMARY's psk — it was registered under ids[0].PSK (device.go:306 mutant)")
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
