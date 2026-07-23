package device

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	awgdevice "github.com/amnezia-vpn/amneziawg-go/device"
	"github.com/amnezia-vpn/amneziawg-go/tun/tuntest"

	"github.com/7mind/wanbond/internal/bind"
	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/telemetry"
)

// newFirstPathUpHarness builds a REAL, probing bind.Multipath for cfg's single loopback path,
// brings up a REAL engine over it (so the engine's own receive routines pump the bind's
// ReceiveFuncs — nothing here hand-rolls the receive pump), and starts a REAL loopback UDP
// "concentrator" that reflects PROBE frames exactly as a live hub's Reflector would. This drives
// the path to genuine telemetry.StateUp and fires the bind's T117 first-path-up latch for real,
// so startFirstPathUpHandshake's wiring is exercised end to end without needing a live WG peer at
// the other end of a real network link. The engine's own crypto peer (private/public key pair) is
// unrelated to cfg's peer — startFirstPathUpHandshake only reads cfg.Role, the injected rh
// collaborator stands in for deviceRehandshake, so no engine-peer coupling is needed here.
func newFirstPathUpHarness(t *testing.T, cfg *config.Config) *bind.Multipath {
	t.Helper()
	lg := discardLogger(t)
	psk := cfg.PeerIdentities()[0].PSK

	conc, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("listen concentrator socket: %v", err)
	}
	t.Cleanup(func() { _ = conc.Close() })
	concAP := netip.MustParseAddrPort(conc.LocalAddr().String())

	reflector := telemetry.NewReflector(psk, rand.Reader)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, from, rerr := conc.ReadFromUDPAddrPort(buf)
			if rerr != nil {
				return
			}
			echo, _, eerr := reflector.Reflect(append([]byte(nil), buf[:n]...))
			if eerr != nil {
				continue // not an authenticatable probe (e.g. a stray WG handshake init) — ignore
			}
			_, _ = conc.WriteToUDPAddrPort(echo, from)
		}
	}()

	sessionID, err := telemetry.NewSessionID(rand.Reader)
	if err != nil {
		t.Fatalf("new session id: %v", err)
	}
	scheduler, probers, newProber, err := buildScheduler(cfg, psk, sessionID, lg)
	if err != nil {
		t.Fatalf("build scheduler: %v", err)
	}
	mp, err := bind.NewMultipath(cfg.Paths, psk, scheduler, probers, newProber, nil, nil, cfg.Amnezia, lg)
	if err != nil {
		t.Fatalf("build multipath bind: %v", err)
	}

	chtun := tuntest.NewChannelTUN()
	dev := awgdevice.NewDevice(chtun.TUN(), mp, engineLogger(lg, "error", mp.EverHadLivePath))
	t.Cleanup(dev.Close)

	edgePrivRaw, _ := genX25519(t)
	_, peerPubRaw := genX25519(t)
	var uapi strings.Builder
	fmt.Fprintf(&uapi, "private_key=%s\n", hex.EncodeToString(edgePrivRaw))
	fmt.Fprintf(&uapi, "public_key=%s\n", hex.EncodeToString(peerPubRaw))
	fmt.Fprintf(&uapi, "allowed_ip=0.0.0.0/0\n")
	if err := dev.IpcSet(uapi.String()); err != nil {
		t.Fatalf("IpcSet crypto config: %v", err)
	}
	if err := dev.Up(); err != nil {
		t.Fatalf("dev.Up: %v", err)
	}

	mp.SetPeerRemote(concAP)
	return mp
}

// TestFirstPathUpHandshakeEdgeFiresExactlyOnce is the T120 acceptance: on an edge-role
// construction, the callback startFirstPathUpHandshake wires through Multipath.SetOnFirstPathUp
// fires EXACTLY ONCE — via a real (not fake) prober reaching liveness — when its single path
// genuinely reaches telemetry.StateUp, mirroring the injected-counter pattern
// TestHubFailoverSwitchesOnHubLoss (failover_test.go) uses for the rehandshake collaborator.
func TestFirstPathUpHandshakeEdgeFiresExactlyOnce(t *testing.T) {
	cfg := writeEdgeConfig(t, `["127.0.0.1:1"]`, false)
	mp := newFirstPathUpHarness(t, cfg)

	var calls atomic.Int32
	startFirstPathUpHandshake(cfg, mp, func() { calls.Add(1) })

	stopProbes := mp.StartProbeLoop(telemetry.DefaultProbeInterval)
	t.Cleanup(stopProbes)

	deadline := time.Now().Add(2 * time.Second)
	for calls.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("first-path-up handshake callback did not fire within 2s of the path reaching liveness")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// The callback fires off the receive hot path (T117); give a wrongly-repeated invocation
	// time to land before asserting exclusivity.
	time.Sleep(100 * time.Millisecond)
	if got := calls.Load(); got != 1 {
		t.Fatalf("first-path-up handshake callback fired %d times, want exactly 1", got)
	}
	if !mp.EverHadLivePath() {
		t.Fatalf("EverHadLivePath = false after the callback fired")
	}
}

// TestComposeRehandshakesFiresEveryPeer is the T251/Q68b acceptance for the multi-exit edge
// first-path-up fan-out: composeRehandshakes must invoke EVERY per-peer rehandshake exactly once
// per trigger (in order), so a single first-path-up edge initiates to all N concentrator peers —
// not just the primary. A mutant that fires only the first (the pre-T251 primary-only behaviour)
// leaves the standby peers' counters at zero and fails here.
func TestComposeRehandshakesFiresEveryPeer(t *testing.T) {
	const peers = 3
	var calls [peers]atomic.Int32
	var order []int
	var mu sync.Mutex
	rhs := make([]rehandshake, peers)
	for i := range rhs {
		i := i
		rhs[i] = func() {
			calls[i].Add(1)
			mu.Lock()
			order = append(order, i)
			mu.Unlock()
		}
	}

	trigger := composeRehandshakes(rhs)
	trigger()

	for i := range calls {
		if got := calls[i].Load(); got != 1 {
			t.Errorf("peer %d rehandshake fired %d times per trigger, want exactly 1 (every peer must initiate)", i, got)
		}
	}
	mu.Lock()
	gotOrder := append([]int(nil), order...)
	mu.Unlock()
	if len(gotOrder) != peers || gotOrder[0] != 0 || gotOrder[1] != 1 || gotOrder[2] != 2 {
		t.Fatalf("fan-out order = %v, want [0 1 2] (every peer, in configured order)", gotOrder)
	}

	// A second trigger fires each peer once more (idempotent per-call fan-out, not a one-shot).
	trigger()
	for i := range calls {
		if got := calls[i].Load(); got != 2 {
			t.Errorf("peer %d rehandshake fired %d times over two triggers, want 2", i, got)
		}
	}
}

// TestFirstPathUpHandshakeConcentratorWiresNoInitiation is the T120 concentrator-side
// acceptance: a concentrator-role construction wires NO initiation — the concentrator is the
// responder to every edge and initiates nothing (startFailoverAndResolution's concentrator
// no-op stays untouched). The path is driven to genuine liveness exactly as in the edge test, so
// a callback that never fires is not merely because liveness never happened.
func TestFirstPathUpHandshakeConcentratorWiresNoInitiation(t *testing.T) {
	cfg := writeConcentratorConfig(t, 1, 53974)
	mp := newFirstPathUpHarness(t, cfg)

	var calls atomic.Int32
	startFirstPathUpHandshake(cfg, mp, func() { calls.Add(1) })

	stopProbes := mp.StartProbeLoop(telemetry.DefaultProbeInterval)
	t.Cleanup(stopProbes)

	deadline := time.Now().Add(2 * time.Second)
	for !mp.EverHadLivePath() {
		if time.Now().After(deadline) {
			t.Fatalf("path never reached liveness within 2s — cannot assert the negative")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Give a wrongly-wired callback time to fire before asserting it never did.
	time.Sleep(100 * time.Millisecond)
	if got := calls.Load(); got != 0 {
		t.Fatalf("concentrator-role construction invoked the initiation callback %d times, want 0", got)
	}
}
