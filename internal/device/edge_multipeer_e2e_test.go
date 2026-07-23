package device

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/amnezia-vpn/amneziawg-go/tun/tuntest"

	"github.com/7mind/wanbond/internal/bind"
	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/dnsresolve"
	"github.com/7mind/wanbond/internal/telemetry"
)

// freeLoopbackUDPPorts reserves n ephemeral UDP ports on 127.0.0.1 SIMULTANEOUSLY (all sockets
// held open at once so the OS hands back DISTINCT ports — a sequential reserve-then-release can
// hand the just-freed port straight back), then releases them, returning the n distinct port
// numbers for concentrator listen_port pinning. A brief TOCTOU window between release and the
// concentrator's bind is tolerated (the ports are drawn from the ephemeral range).
func freeLoopbackUDPPorts(t *testing.T, n int) []int {
	t.Helper()
	conns := make([]*net.UDPConn, 0, n)
	ports := make([]int, 0, n)
	for i := 0; i < n; i++ {
		c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			for _, cc := range conns {
				_ = cc.Close()
			}
			t.Fatalf("reserve loopback UDP port %d/%d: %v", i+1, n, err)
		}
		conns = append(conns, c)
		ports = append(ports, c.LocalAddr().(*net.UDPAddr).Port)
	}
	for _, c := range conns {
		_ = c.Close()
	}
	return ports
}

// writeLoadedConfig writes body to a 0600 temp file (config.Load requires exactly 0600) and loads
// it, failing the test with the offending body on any error.
func writeLoadedConfig(t *testing.T, name, body string) *config.Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	if err := os.Chmod(path, 0o600); err != nil { // defeat umask widening
		t.Fatalf("chmod %s: %v", name, err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load %s: %v\n%s", name, err, body)
	}
	return cfg
}

// TestEdgeMultiPeerWarmBringUp is the T251/Q68b/M105 acceptance: device.Up brings up ONE edge with
// TWO configured concentrator peers, both warm CONCURRENTLY, over TWO loopback uplinks against TWO
// in-process concentrator engines — no root, the pattern of monitor_e2e_test.go (real engines +
// real Binds over loopback UDP, driven through the production up() wiring). All three engines use
// the same non-default Amnezia profile, making this the configured multi-Device race/isolation
// regression for the local upstream #155 patch. It asserts, end to end:
//
//	(a) per-(peer,path) probers reach StateUp for ALL FOUR (peer,uplink) combinations — the edge's
//	    shared-socket fan-out probes every peer over every uplink (attachSharedPathLocked);
//	(b) BOTH peers complete a genuine WG handshake within the budget — the first-path-up trigger
//	    initiates to EVERY peer (deviceRehandshakeAllPeers), not just the primary, and persistent
//	    keepalive is rendered on every peer's engine endpoint (uapiConfig), keeping both warm;
//	(c) PeerSnapshots() returns exactly TWO named entries (the D58 2+-peer real-name rule);
//	(d) the edge-role teardown path leaves both warm peers INTACT: the real peerTeardownMonitor,
//	    built over the edge cfg's monitored set (concentratorMonitoredPeers), tears NOTHING down —
//	    the D50 guard keeps warm standbys alive on the edge role.
func TestEdgeMultiPeerWarmBringUp(t *testing.T) {
	edgePrivRaw, edgePubRaw := genX25519(t)
	conc0PrivRaw, conc0PubRaw := genX25519(t)
	conc1PrivRaw, conc1PubRaw := genX25519(t)
	b64 := base64.StdEncoding.EncodeToString

	// Distinct outer psks: one per bond. Each single-peer concentrator authenticates on its
	// top-level psk; the multi-peer edge carries the matching psk per peer.
	psk0 := randB64Key(t)
	psk1 := randB64Key(t)

	ports := freeLoopbackUDPPorts(t, 2)
	port0, port1 := ports[0], ports[1]

	// Two single-peer concentrators, each learning the SAME edge WG identity, on distinct loopback
	// listen ports. A concentrator peer carries no endpoint (it learns the edge's dynamically).
	conc0Cfg := writeLoadedConfig(t, "conc0.toml", fmt.Sprintf(`role = "concentrator"
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
`, psk0, b64(conc0PrivRaw), port0, b64(edgePubRaw)))

	conc1Cfg := writeLoadedConfig(t, "conc1.toml", fmt.Sprintf(`role = "concentrator"
psk = "%s"

[[paths]]
name = "a"
source_addr = "127.0.0.1"

[wireguard]
private_key = "%s"
listen_port = %d

[[wireguard.peers]]
public_key = "%s"
allowed_ips = ["10.0.1.0/24"]
`, psk1, b64(conc1PrivRaw), port1, b64(edgePubRaw)))

	// The edge: two uplinks (distinct loopback source addrs — a shared source collides EADDRINUSE at
	// the second bind, D10) and two concentrator peers, each with its own literal endpoint, name,
	// psk, and a distinct non-overlapping inner allowed_ip (T250 multi-exit rules). The top-level
	// psk is a throwaway — a multi-peer edge draws each bond's authenticator from the per-peer psk.
	edgeCfg := writeLoadedConfig(t, "edge.toml", fmt.Sprintf(`role = "edge"
psk = "%s"

[[paths]]
name = "a"
source_addr = "127.0.0.1"

[[paths]]
name = "b"
source_addr = "127.0.0.2"

[wireguard]
private_key = "%s"

[[wireguard.peers]]
public_key = "%s"
endpoint = "127.0.0.1:%d"
allowed_ips = ["10.0.0.0/24"]
name = "c0"
psk = "%s"

[[wireguard.peers]]
public_key = "%s"
endpoint = "127.0.0.1:%d"
allowed_ips = ["10.0.1.0/24"]
name = "c1"
psk = "%s"
`, randB64Key(t), b64(edgePrivRaw),
		b64(conc0PubRaw), port0, psk0,
		b64(conc1PubRaw), port1, psk1))

	amnezia := config.Amnezia{
		Jc: 4, Jmin: 8, Jmax: 80, S1: 15, S2: 92,
		H1: 1_111_111, H2: 2_222_222, H3: 3_333_333, H4: 4_444_444,
	}
	conc0Cfg.Amnezia = amnezia
	conc1Cfg.Amnezia = amnezia
	edgeCfg.Amnezia = amnezia

	inert := func() (dnsresolve.Resolver, error) { return &dnsresolve.FakeResolver{}, nil }

	// Concentrators first (they must be listening before the edge initiates), then the edge.
	conc0, err := up(conc0Cfg, discardLogger(t), tuntest.NewChannelTUN().TUN(), "wbmpc0", inert, "test")
	if err != nil {
		t.Fatalf("up concentrator 0: %v", err)
	}
	defer conc0.Close()
	conc1, err := up(conc1Cfg, discardLogger(t), tuntest.NewChannelTUN().TUN(), "wbmpc1", inert, "test")
	if err != nil {
		t.Fatalf("up concentrator 1: %v", err)
	}
	defer conc1.Close()

	edge, err := up(edgeCfg, discardLogger(t), tuntest.NewChannelTUN().TUN(), "wbmpe0", inert, "test")
	if err != nil {
		t.Fatalf("up edge (2 peers, 2 uplinks): %v", err)
	}
	defer edge.Close()

	conc0Hex := hex.EncodeToString(conc0PubRaw)
	conc1Hex := hex.EncodeToString(conc1PubRaw)

	// (a) + (b): poll until all four (peer,uplink) probers are UP AND both peers have a completed
	// WG handshake. The two conditions ride the same bring-up: probes drive liveness, the
	// first-path-up trigger + keepalive drive both handshakes.
	deadline := time.Now().Add(20 * time.Second)
	var lastSnaps []bind.PeerSnapshot
	var lastHS map[string]int64
	for time.Now().Before(deadline) {
		lastSnaps = edge.bind.PeerSnapshots()
		dump, derr := edge.dev.IpcGet()
		if derr != nil {
			t.Fatalf("edge IpcGet: %v", derr)
		}
		lastHS = perPeerHandshakeNano(dump)
		if allFourPathsUp(lastSnaps) && lastHS[conc0Hex] > 0 && lastHS[conc1Hex] > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	if !allFourPathsUp(lastSnaps) {
		t.Fatalf("not all 4 (peer,uplink) probers reached UP within budget: %s", describePeerPaths(lastSnaps))
	}
	if lastHS[conc0Hex] == 0 || lastHS[conc1Hex] == 0 {
		t.Fatalf("both peers did not complete a WG handshake within budget: c0 handshake=%d c1 handshake=%d\n%s",
			lastHS[conc0Hex], lastHS[conc1Hex], describePeerPaths(lastSnaps))
	}

	// (c): PeerSnapshots returns exactly two entries, named for the two configured concentrators
	// (the D58 2+-peer real-name rule renames the embedded primary to its configured name).
	if len(lastSnaps) != 2 {
		t.Fatalf("PeerSnapshots() = %d entries, want 2 (one per configured concentrator peer)", len(lastSnaps))
	}
	names := map[string]bool{lastSnaps[0].Name: true, lastSnaps[1].Name: true}
	if !names["c0"] || !names["c1"] {
		t.Fatalf("PeerSnapshots names = %q/%q, want c0 and c1 (2+ peers expose real names, D58)", lastSnaps[0].Name, lastSnaps[1].Name)
	}

	// (d): the D50 EDGE guard — the real teardown monitor, built exactly as up() builds it (over
	// concentratorMonitoredPeers of the edge cfg), tears NOTHING down. On the edge the two peers are
	// warm standbys, healthy by design; the concentrator-only reclaim must be inert here. Polled
	// against the LIVE engine so this is not a vacuous empty-dump check.
	tearer := &recordingTearer{}
	teardownMon := newPeerTeardownMonitor(edge.dev, tearer,
		concentratorMonitoredPeers(edgeCfg, edgeCfg.PeerIdentities()), telemetry.SystemClock{})
	for i := 0; i < 3; i++ {
		teardownMon.poll(discardInfo(t))
	}
	if got := tearer.snapshot(); len(got) != 0 {
		t.Fatalf("edge-role teardown monitor tore down %v, want nothing (warm standbys must stay intact, D50 guard)", got)
	}
}

// allFourPathsUp reports whether snaps carries exactly two peers, each with exactly two paths, and
// every one of the four (peer,path) prober states is StateUp.
func allFourPathsUp(snaps []bind.PeerSnapshot) bool {
	if len(snaps) != 2 {
		return false
	}
	for _, p := range snaps {
		if len(p.Paths) != 2 {
			return false
		}
		for _, pt := range p.Paths {
			if pt.State != telemetry.StateUp {
				return false
			}
		}
	}
	return true
}

// describePeerPaths renders the per-(peer,path) liveness state for a failure message.
func describePeerPaths(snaps []bind.PeerSnapshot) string {
	s := fmt.Sprintf("peers=%d", len(snaps))
	for _, p := range snaps {
		s += fmt.Sprintf(" | peer %q paths:", p.Name)
		for _, pt := range p.Paths {
			s += fmt.Sprintf(" %s=%v(remote=%s)", pt.Name, pt.State, pt.Remote)
		}
	}
	return s
}
