package device

import (
	"context"
	"crypto/ecdh"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/amnezia-vpn/amneziawg-go/tun/tuntest"
	"github.com/coder/websocket"
	"go.uber.org/goleak"

	"github.com/7mind/wanbond/internal/bind"
	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/dnsresolve"
	"github.com/7mind/wanbond/internal/metrics"
	"github.com/7mind/wanbond/internal/monitor"
	"github.com/7mind/wanbond/internal/telemetry"
)

// inertFactory is a resolver factory that returns an empty FakeResolver; every test here uses an
// all-literal endpoint so the factory is never actually invoked (Q29 inertness), but up() requires
// a non-nil factory.
func inertFactory() (dnsresolve.Resolver, error) {
	return &dnsresolve.FakeResolver{}, nil
}

// TestUpStartsMonitorEndpointReachableWS is the T169 primary acceptance: an edge config with
// [monitor].listen set brings up a monitor server that (1) is fed by a DEDICATED metrics.Source
// (t.monitorSrc), NOT the /metrics scraper's t.metricsSrc — so the two throughput derivations keep
// independent last-sample state (T165 invariant) — and (2) is reachable over WebSocket, streaming a
// well-formed MonitorSnapshot frame. goleak asserts a leak-free shutdown.
func TestUpStartsMonitorEndpointReachableWS(t *testing.T) {
	defer goleak.VerifyNone(t)

	cfg := writeEdgeConfig(t, `["127.0.0.1:51821"]`, false)
	cfg.Monitor = config.Monitor{Listen: "127.0.0.1:0"}
	chtun := tuntest.NewChannelTUN()

	tun, err := up(cfg, discardLogger(t), chtun.TUN(), "wanbondtest0", inertFactory, "test")
	if err != nil {
		t.Fatalf("up with [monitor] configured failed: %v", err)
	}
	defer tun.Close()

	if tun.monitorSrv == nil {
		t.Fatal("up did not start the monitor endpoint despite a configured [monitor].listen")
	}
	// The monitor MUST be fed by its own dedicated Source, distinct from the /metrics scraper's,
	// so the two independent scrape cadences do not corrupt each other's derived throughput.
	if tun.monitorSrc == nil {
		t.Fatal("monitorSrc is nil — the dedicated monitor Source was not constructed")
	}
	if tun.monitorSrc == tun.metricsSrc {
		t.Fatal("monitor shares the /metrics Source (t.monitorSrc == t.metricsSrc); it MUST have a dedicated Source")
	}

	url := fmt.Sprintf("ws://%s/ws", tun.monitorSrv.Addr().String())
	dialCtx, dialCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer dialCancel()
	c, resp, err := websocket.Dial(dialCtx, url, nil)
	if err != nil {
		t.Fatalf("websocket.Dial(%q): %v", url, err)
	}
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	defer func() { _ = c.CloseNow() }()

	readCtx, readCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer readCancel()
	typ, data, err := c.Read(readCtx)
	if err != nil {
		t.Fatalf("read first monitor frame: %v", err)
	}
	if typ != websocket.MessageText {
		t.Fatalf("frame type = %v, want MessageText", typ)
	}
	if err := json.Unmarshal(data, new(monitor.MonitorSnapshot)); err != nil {
		t.Fatalf("unmarshal MonitorSnapshot: %v (payload=%s)", err, data)
	}
}

// TestUpMonitorEdgeConcentratorParity pins the T169 parity requirement: role is a config-only
// field and BOTH RoleEdge and RoleConcentrator flow through the same up() wiring, so a [monitor]
// block starts the endpoint identically for each. Each subtest also asserts the dedicated Source.
func TestUpMonitorEdgeConcentratorParity(t *testing.T) {
	cases := []struct {
		name string
		cfg  func(t *testing.T) *config.Config
	}{
		{"edge", func(t *testing.T) *config.Config {
			return writeEdgeConfig(t, `["127.0.0.1:51821"]`, false)
		}},
		{"concentrator", func(t *testing.T) *config.Config {
			return writeConcentratorConfig(t, 2, 53981)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer goleak.VerifyNone(t)

			cfg := tc.cfg(t)
			cfg.Monitor = config.Monitor{Listen: "127.0.0.1:0"}
			chtun := tuntest.NewChannelTUN()

			tun, err := up(cfg, discardLogger(t), chtun.TUN(), "wanbondtest0", inertFactory, "test")
			if err != nil {
				t.Fatalf("up (%s) with [monitor] configured failed: %v", tc.name, err)
			}
			defer tun.Close()

			if tun.monitorSrv == nil {
				t.Fatalf("%s: up did not start the monitor endpoint (parity broken)", tc.name)
			}
			if tun.monitorSrc == nil || tun.monitorSrc == tun.metricsSrc {
				t.Fatalf("%s: monitor Source is not a dedicated Source", tc.name)
			}
		})
	}
}

// TestReloadReconcilesMonitorWithoutTearingTunnel is the T169 reload acceptance: a SIGHUP/Reload
// that changes [monitor] reconciles the endpoint (start/stop/rebind) WITHOUT recreating the engine
// (the tunnel is not torn down — t.dev is the SAME pointer throughout). It exercises stop
// (listen=""), (re)start, and a token-driven rebind at an unchanged loopback address.
func TestReloadReconcilesMonitorWithoutTearingTunnel(t *testing.T) {
	defer goleak.VerifyNone(t)

	cfg := writeEdgeConfig(t, `["127.0.0.1:51821"]`, false)
	cfg.Monitor = config.Monitor{Listen: "127.0.0.1:0"}
	chtun := tuntest.NewChannelTUN()

	tun, err := up(cfg, discardLogger(t), chtun.TUN(), "wanbondtest0", inertFactory, "test")
	if err != nil {
		t.Fatalf("up failed: %v", err)
	}
	defer tun.Close()

	if tun.monitorSrv == nil {
		t.Fatal("monitor endpoint not started at boot")
	}
	engine := tun.dev // the running engine; must survive every reload untouched
	addr1 := tun.monitorSrv.Addr().String()

	// reloadWith clones the running config and applies a monitor mutation, then Reloads.
	reloadWith := func(m config.Monitor) {
		t.Helper()
		next := *tun.cfg
		next.Monitor = m
		if err := tun.Reload(&next); err != nil {
			t.Fatalf("Reload(monitor=%+v): %v", m, err)
		}
		if tun.dev != engine {
			t.Fatalf("Reload recreated the engine (tunnel torn down) for monitor=%+v", m)
		}
	}

	// Stop: listen cleared => endpoint stops.
	reloadWith(config.Monitor{Listen: ""})
	if tun.monitorSrv != nil {
		t.Fatal("Reload with empty monitor.listen did not stop the endpoint")
	}

	// (Re)start: a fresh loopback listen brings it back.
	reloadWith(config.Monitor{Listen: "127.0.0.1:0"})
	if tun.monitorSrv == nil {
		t.Fatal("Reload turning monitor.listen back ON did not start the endpoint")
	}
	addr2 := tun.monitorSrv.Addr().String()

	// Rebind on a token change at an unchanged loopback address: the auth layer differs, so the
	// endpoint MUST rebind (a fresh OS-assigned port proves a new listener/server was stood up).
	reloadWith(config.Monitor{Listen: "127.0.0.1:0", Token: "sekret-token"})
	if tun.monitorSrv == nil {
		t.Fatal("token-change reload dropped the endpoint")
	}
	addr3 := tun.monitorSrv.Addr().String()
	if addr3 == addr2 {
		t.Fatalf("token change did not rebind the endpoint (addr unchanged %q)", addr3)
	}
	_ = addr1 // addr1 retained for symmetry; the invariant asserted is engine-identity + rebind
}

// TestReloadRevealAddressingFlipRebindsMonitorWithoutTearingTunnel is the T280 reload acceptance
// (f), modeled on TestReloadReconcilesMonitorWithoutTearingTunnel: a SIGHUP/Reload that flips ONLY
// [monitor].reveal_addressing at an UNCHANGED listen+token must REBIND the endpoint (the reveal
// verdict is baked in at monitor.NewServer construction, so a live flip requires a fresh server)
// WITHOUT recreating the engine, and the new reveal verdict must be OBSERVABLE over /ws. It uses a
// token-authorized non-loopback (wildcard) bind so the reveal verdict is visible on the wire
// (reveal off => addressingHidden=true; reveal on => false). MUTATION-VERIFY (T280): reverting the
// applyMonitorLocked change-detection guard extension (the reveal==t.monitorReveal term, device.go)
// makes the reveal-only reload an early-return no-op, so the endpoint never rebinds and the second
// frame still reads addressingHidden=true — turning this test red.
func TestReloadRevealAddressingFlipRebindsMonitorWithoutTearingTunnel(t *testing.T) {
	defer goleak.VerifyNone(t)

	const token = "reveal-reload-token"
	cfg := writeEdgeConfig(t, `["203.0.113.1:51820", "198.51.100.7:51820"]`, false)
	cfg.Monitor = config.Monitor{Listen: "0.0.0.0:0", Token: token, RevealAddressing: false}
	chtun := tuntest.NewChannelTUN()

	tun, err := up(cfg, discardLogger(t), chtun.TUN(), "wanbondtest0", inertFactory, "test")
	if err != nil {
		t.Fatalf("up failed: %v", err)
	}
	defer tun.Close()

	if tun.monitorSrv == nil {
		t.Fatal("monitor endpoint not started at boot")
	}
	engine := tun.dev // must survive the reveal-only reload untouched
	addrBefore := tun.monitorSrv.Addr().String()

	// assertReveal dials the CURRENT running endpoint over a real /ws and asserts the addressing
	// verdict; exit control must stay unavailable on this non-loopback bind either way.
	assertReveal := func(desc string, wantHidden bool) {
		t.Helper()
		readRaw, cleanup := dialMonitorAt(t, tun.monitorSrv, token)
		defer cleanup()
		raw, snap := readRaw(t)
		if snap.AddressingHidden != wantHidden {
			t.Fatalf("%s: addressingHidden = %v, want %v: %s", desc, snap.AddressingHidden, wantHidden, raw)
		}
		if snap.ExitControlAvailable {
			t.Fatalf("%s: exitControlAvailable must stay false on a non-loopback bind: %s", desc, raw)
		}
	}

	// Boot: reveal off on a non-loopback bind => addressing redacted.
	assertReveal("boot (reveal off)", true)

	// Flip reveal_addressing ON at the UNCHANGED listen+token: MUST rebind (a fresh OS-assigned
	// port proves a new listener/server) WITHOUT tearing the tunnel down.
	next := *tun.cfg
	next.Monitor = config.Monitor{Listen: "0.0.0.0:0", Token: token, RevealAddressing: true}
	if err := tun.Reload(&next); err != nil {
		t.Fatalf("reveal-only reload failed: %v", err)
	}
	if tun.dev != engine {
		t.Fatal("reveal-only reload recreated the engine (tunnel torn down)")
	}
	if tun.monitorSrv == nil {
		t.Fatal("reveal-only reload dropped the endpoint")
	}
	addrAfter := tun.monitorSrv.Addr().String()
	if addrAfter == addrBefore {
		t.Fatalf("reveal-only flip did not rebind the endpoint (addr unchanged %q) — silent no-op", addrAfter)
	}

	// After the flip: the new reveal verdict is observable over /ws (addressing now revealed).
	assertReveal("after reveal flip (reveal on)", false)
}

// TestReloadTokenRotationAtFixedPort is the regression guard for the rebind-order defect: a
// token-only change at an UNCHANGED FIXED listen address must rebind SUCCESSFULLY, not fail
// EADDRINUSE. The buggy order bound the new listener (monitor.NewServer -> net.Listen) BEFORE
// stopping the old one, so at a fixed port the new bind collided with the still-open old
// listener; the ':0' reload test masked it by getting a fresh OS-assigned port each rebind.
func TestReloadTokenRotationAtFixedPort(t *testing.T) {
	defer goleak.VerifyNone(t)

	// Grab a free loopback port, then release it so up() can bind that exact address. (A small
	// TOCTOU window between close and re-bind is acceptable for a regression test.)
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("probe listen: %v", err)
	}
	fixedAddr := probe.Addr().String() // 127.0.0.1:<fixed port>
	_ = probe.Close()

	cfg := writeEdgeConfig(t, `["127.0.0.1:51821"]`, false)
	cfg.Monitor = config.Monitor{Listen: fixedAddr}
	chtun := tuntest.NewChannelTUN()

	tun, err := up(cfg, discardLogger(t), chtun.TUN(), "wanbondtest0", inertFactory, "test")
	if err != nil {
		t.Fatalf("up with a fixed-port [monitor] failed: %v", err)
	}
	defer tun.Close()
	if tun.monitorSrv == nil {
		t.Fatal("monitor endpoint not started at boot")
	}
	if got := tun.monitorSrv.Addr().String(); got != fixedAddr {
		t.Fatalf("monitor bound %q, want the fixed %q", got, fixedAddr)
	}

	// Token rotation at the SAME fixed address: MUST succeed (rebind releases the old listener
	// before binding the new), not fail EADDRINUSE, and stay on the same port.
	next := *tun.cfg
	next.Monitor = config.Monitor{Listen: fixedAddr, Token: "rotated-token"}
	if err := tun.Reload(&next); err != nil {
		t.Fatalf("token rotation at fixed port %q failed (rebind-order defect): %v", fixedAddr, err)
	}
	if tun.monitorSrv == nil {
		t.Fatal("token rotation dropped the endpoint")
	}
	if got := tun.monitorSrv.Addr().String(); got != fixedAddr {
		t.Fatalf("after token rotation monitor bound %q, want the same fixed %q", got, fixedAddr)
	}
}

// TestMonitorWire_InfoFields is the T222 reproduction: it pins that up() builds the REAL
// monitor.Info (not the placeholder monitor.Info{} T219 left) and threads it into the
// [monitor] endpoint. It asserts, over the up()-wired t.monitorInfo:
//   - Role from config and the ldflags Version passed into up();
//   - a LIVE Uptime provider that reports a positive elapsed time (R242);
//   - the config-DECLARED per-path link params keyed to the metrics (peer,path) rule;
//   - the truncated (~10 base64 char) local WG public-key FINGERPRINT — a prefix of the
//     real public key, NEVER the recoverable full key (Q63);
//   - an empty endpoints list on the single-IP-literal edge (no failover controller).
//
// It then drives the LIVE hub-endpoints provider through a SIMULATED failover on a
// directly-constructed controller and asserts the active entry MOVES between two calls —
// the freshness property (R242) a value captured once at construction would violate.
//
// Fails before the wiring: the placeholder monitor.Info{} yields empty Role/Version/
// fingerprint, no PathLinks, a nil Uptime provider, and newEndpointsProvider does not exist.
func TestMonitorWire_InfoFields(t *testing.T) {
	defer goleak.VerifyNone(t)

	const wantVersion = "v9.9.9-wiretest"
	const wantBandwidthBps = 50_000_000.0
	const wantRTT = 45 * time.Millisecond

	// A single-IP-literal edge: no hub-failover controller (peerNeedsHubFailover is false),
	// so the up()-wired endpoints provider returns an empty list. The declared link params
	// are set post-Load directly on the path (buildPathLinks reads these fields).
	cfg := writeEdgeConfig(t, `["127.0.0.1:51821"]`, false)
	cfg.Monitor = config.Monitor{Listen: "127.0.0.1:0"}
	cfg.Paths[0].LinkBandwidthBitsPerSec = wantBandwidthBps
	cfg.Paths[0].LinkRTT = wantRTT
	chtun := tuntest.NewChannelTUN()

	tun, err := up(cfg, discardLogger(t), chtun.TUN(), "wanbondtest0", inertFactory, wantVersion)
	if err != nil {
		t.Fatalf("up failed: %v", err)
	}
	defer tun.Close()

	info := tun.monitorInfo

	if info.Role != string(config.RoleEdge) {
		t.Fatalf("Info.Role = %q, want %q", info.Role, config.RoleEdge)
	}
	if info.Version != wantVersion {
		t.Fatalf("Info.Version = %q, want %q", info.Version, wantVersion)
	}

	// Uptime MUST be a live provider (R242), reporting a positive elapsed time.
	if info.Uptime == nil {
		t.Fatal("Info.Uptime provider is nil; uptime must be a LIVE source, not a captured constant")
	}
	if up := info.Uptime(); up <= 0 {
		t.Fatalf("Info.Uptime() = %v, want > 0", up)
	}

	// Per-path declared link params, keyed by the single-peer (peer="") rule.
	link, ok := info.PathLinks[monitor.PathKey{Peer: "", Name: cfg.Paths[0].Name}]
	if !ok {
		t.Fatalf("Info.PathLinks missing key {Peer:\"\", Name:%q}; have %+v", cfg.Paths[0].Name, info.PathLinks)
	}
	if link.LinkBandwidthBps != wantBandwidthBps {
		t.Fatalf("PathLink.LinkBandwidthBps = %v, want %v", link.LinkBandwidthBps, wantBandwidthBps)
	}
	if link.LinkRttSeconds != wantRTT.Seconds() {
		t.Fatalf("PathLink.LinkRttSeconds = %v, want %v", link.LinkRttSeconds, wantRTT.Seconds())
	}

	// The fingerprint is the truncated base64 of the REAL local public key: a prefix of the
	// full key, exactly wgFingerprintLen chars, and never the full recoverable key (Q63).
	priv := cfg.WireGuard.PrivateKey.Bytes()
	sk, err := ecdh.X25519().NewPrivateKey(priv[:])
	if err != nil {
		t.Fatalf("derive expected public key: %v", err)
	}
	wantFull := base64.StdEncoding.EncodeToString(sk.PublicKey().Bytes())
	if len(info.WGPublicKeyFingerprint) != wgFingerprintLen {
		t.Fatalf("fingerprint length = %d, want %d (%q)", len(info.WGPublicKeyFingerprint), wgFingerprintLen, info.WGPublicKeyFingerprint)
	}
	if info.WGPublicKeyFingerprint != wantFull[:wgFingerprintLen] {
		t.Fatalf("fingerprint = %q, want prefix %q of the local public key", info.WGPublicKeyFingerprint, wantFull[:wgFingerprintLen])
	}
	if info.WGPublicKeyFingerprint == wantFull {
		t.Fatal("fingerprint equals the FULL public key; Q63 requires the truncated fingerprint only")
	}

	// The single-IP-literal edge has no hub-failover controller, so the endpoints provider
	// yields an empty list (T221 omits the section).
	if info.Endpoints == nil {
		t.Fatal("Info.Endpoints provider is nil; it must be a non-nil live provider")
	}
	if eps := info.Endpoints(); len(eps) != 0 {
		t.Fatalf("Info.Endpoints() = %+v on a single-endpoint edge, want empty", eps)
	}
	if len(info.ExitCapablePeers) != 0 {
		t.Fatalf("Info.ExitCapablePeers = %v for an inner-only edge, want empty", info.ExitCapablePeers)
	}

	// LIVE-provider failover: build a controller with two endpoints directly, wrap it in the
	// PRODUCTION provider, and assert the active entry MOVES after a forced switch — the same
	// provider closure returns the fresh active state on its second call (R242 freshness).
	eps := mustEndpoints(t, "203.0.113.1:51820", "198.51.100.7:51820")
	hp := []hubHealth{&fakeHealth{telemetry.StateUp}, &fakeHealth{telemetry.StateUp}}
	clk := &fakeClock{now: time.Unix(1000, 0)}
	fo := newHubFailover(eps, hp, &recordingRemote{}, func() {}, clk, testSettle, discardLogger(t))
	solo := []config.PeerIdentity{{Name: "solo"}}
	provider := newEndpointsProvider(solo, map[string]*hubFailover{"solo": fo})

	before := provider()
	if len(before) != 2 || before[0].Address != "203.0.113.1:51820" || !before[0].Active || before[1].Active {
		t.Fatalf("before failover: endpoints = %+v, want [0] active with address set", before)
	}

	// Force hub loss and advance past the settle dwell so check() switches the active endpoint.
	hp[0].(*fakeHealth).state = telemetry.StateDown
	hp[1].(*fakeHealth).state = telemetry.StateDown
	clk.advance(testSettle + time.Second)
	fo.check()

	after := provider()
	if len(after) != 2 || after[0].Active || !after[1].Active {
		t.Fatalf("after failover: endpoints = %+v, want the active entry MOVED to [1]", after)
	}
	if after[1].Address != "198.51.100.7:51820" {
		t.Fatalf("after failover: active address = %q, want 198.51.100.7:51820", after[1].Address)
	}
}

// buildTwoPeerWireFixture builds a 2-peer (east/west) production monitor wiring: each peer gets
// its OWN directly-constructed *hubFailover (east has 2 endpoints with [0] active, west has 1
// endpoint), composed through the PRODUCTION newEndpointsProvider (T257) into monitor.Info,
// alongside a 2-peer metrics.Source (newMetricsSource over a fakeProvider, carrying a
// fakePeerSessions with one entry per peer) and a fixed ActiveExit provider — the same seam up()
// wires end to end, without requiring a running engine/websocket.
func buildTwoPeerWireFixture(t *testing.T) (metrics.Source, monitor.Info) {
	t.Helper()

	ids := []config.PeerIdentity{{Name: "east"}, {Name: "west"}}

	eastEPs := mustEndpoints(t, "203.0.113.10:51820", "198.51.100.10:51820")
	eastHealth := []hubHealth{&fakeHealth{telemetry.StateUp}, &fakeHealth{telemetry.StateUp}}
	foEast := newHubFailover(eastEPs, eastHealth, &recordingRemote{}, func() {}, &fakeClock{now: time.Unix(1000, 0)}, testSettle, discardLogger(t))

	westEPs := mustEndpoints(t, "203.0.113.20:51820")
	westHealth := []hubHealth{&fakeHealth{telemetry.StateUp}}
	foWest := newHubFailover(westEPs, westHealth, &recordingRemote{}, func() {}, &fakeClock{now: time.Unix(1000, 0)}, testSettle, discardLogger(t))

	ctrls := map[string]*hubFailover{"east": foEast, "west": foWest}

	prov := &fakeProvider{}
	prov.set([]bind.PeerSnapshot{
		{Name: "east", Paths: []bind.PathTraffic{{Name: "p", State: telemetry.StateUp}}},
		{Name: "west", Paths: []bind.PathTraffic{{Name: "p", State: telemetry.StateUp}}},
	})
	peerSessions := fakePeerSessions{snaps: []metrics.PeerSessionSnapshot{
		{Peer: "east", Established: true, LastHandshakeSeconds: 5},
		{Peer: "west", Established: false, LastHandshakeSeconds: 0},
	}}
	src := newMetricsSource(prov, fakeSession{}, peerSessions, &fakeClock{now: time.Unix(1000, 0)})

	info := monitor.Info{
		Endpoints:        newEndpointsProvider(ids, ctrls),
		ActiveExit:       func() string { return "west" },
		ExitCapablePeers: []string{"east", "west"},
	}
	return src, info
}

// TestMonitorWire_PerPeerEndpointGroupsSessionsActiveExit is the T257 primary acceptance: a
// 2-peer edge snapshot groups the flat endpoint list by owning peer (each entry tagged with its
// OWN failover controller's peer name, preserving that controller's active/standby shape), carries
// one peerSessions entry per bound peer mirroring metrics.PeerSessions(), and surfaces the exit
// selector's ActiveExit() verbatim (a peer NAME, not an address).
func TestMonitorWire_PerPeerEndpointGroupsSessionsActiveExit(t *testing.T) {
	src, info := buildTwoPeerWireFixture(t)

	snap := monitor.BuildSnapshot(src, info, true, false)

	if !snap.MultiPeer {
		t.Fatalf("MultiPeer = false, want true for a 2-peer Source")
	}

	// Endpoints must appear as one ordered active/standby section per peer, side by side in
	// CONFIGURED order (docs/design.md): east's 2 entries (its own active/standby shape) first,
	// then west's 1 entry — not merely bucketable by peer. Assert the exact slice so a
	// nondeterministic-order regression (e.g. iterating ctrls as a bare map) fails deterministically.
	want := []monitor.EndpointSnapshot{
		{Peer: "east", Address: "203.0.113.10:51820", Active: true},
		{Peer: "east", Address: "198.51.100.10:51820", Active: false},
		{Peer: "west", Address: "203.0.113.20:51820", Active: true},
	}
	if !reflect.DeepEqual(snap.Endpoints, want) {
		t.Fatalf("endpoints = %+v, want exact ordered slice %+v", snap.Endpoints, want)
	}

	// peerSessions mirrors metrics.PeerSessions(): one entry per bound peer.
	if len(snap.PeerSessions) != 2 {
		t.Fatalf("peerSessions = %+v, want 2 entries", snap.PeerSessions)
	}
	psByPeer := map[string]monitor.PeerSessionSnapshot{}
	for _, ps := range snap.PeerSessions {
		psByPeer[ps.Peer] = ps
	}
	if !psByPeer["east"].Established || psByPeer["east"].LastHandshakeSeconds != 5 {
		t.Fatalf("east peerSession = %+v, want established=true lastHandshakeSeconds=5", psByPeer["east"])
	}
	if psByPeer["west"].Established {
		t.Fatalf("west peerSession = %+v, want established=false", psByPeer["west"])
	}

	// activeExit is the exit selector's ActiveExit() verbatim — a peer NAME.
	if snap.ActiveExit != "west" {
		t.Fatalf("activeExit = %q, want %q", snap.ActiveExit, "west")
	}
	if got := strings.Join(snap.ExitCapablePeers, ","); got != "east,west" {
		t.Fatalf("exitCapablePeers = %q, want %q", got, "east,west")
	}
}

// TestMonitorWire_PerPeerEndpointsRedactedKeepsGroupingAndActiveExit is the T257 addressingHidden
// acceptance: on a non-loopback (redacted) binding, every endpoint address is blanked but the peer
// grouping and active/standby flags survive, and activeExit — a peer NAME, not an address — is NOT
// redacted.
func TestMonitorWire_PerPeerEndpointsRedactedKeepsGroupingAndActiveExit(t *testing.T) {
	src, info := buildTwoPeerWireFixture(t)

	snap := monitor.BuildSnapshot(src, info, false, false)

	if !snap.AddressingHidden {
		t.Fatalf("AddressingHidden = false, want true when not revealed")
	}
	// Same exact-order assertion as the unredacted test (docs/design.md's configured-order
	// invariant survives redaction): east's 2 entries then west's 1, addresses blanked but peer
	// grouping and active/standby flags intact.
	want := []monitor.EndpointSnapshot{
		{Peer: "east", Address: "", Active: true},
		{Peer: "east", Address: "", Active: false},
		{Peer: "west", Address: "", Active: true},
	}
	if !reflect.DeepEqual(snap.Endpoints, want) {
		t.Fatalf("endpoints (redacted) = %+v, want exact ordered slice %+v", snap.Endpoints, want)
	}

	// activeExit is a peer NAME, never redacted.
	if snap.ActiveExit != "west" {
		t.Fatalf("activeExit (redacted) = %q, want %q (never redacted)", snap.ActiveExit, "west")
	}

	// Round trip through JSON, byte-scanning the raw frame: no configured address literal may
	// appear anywhere, matching the existing addressing-redaction rule.
	b, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, addr := range []string{"203.0.113.10", "198.51.100.10", "203.0.113.20"} {
		if strings.Contains(string(b), addr) {
			t.Fatalf("redacted frame leaked endpoint address %q: %s", addr, b)
		}
	}
}

// TestMonitorWire_SinglePeerEndpointsByteCompatible is the T257 back-compat acceptance: a
// single-bound-peer edge's endpoints provider still renders the flat pre-T257 shape byte-for-byte
// (Peer "" throughout), even though the underlying controller is now reached through the per-peer
// ctrls map/newEndpointsProvider machinery rather than firstEligibleCtrl (retired).
func TestMonitorWire_SinglePeerEndpointsByteCompatible(t *testing.T) {
	ids := []config.PeerIdentity{{Name: "onlypeer"}}
	eps := mustEndpoints(t, "203.0.113.30:51820", "198.51.100.30:51820")
	health := []hubHealth{&fakeHealth{telemetry.StateUp}, &fakeHealth{telemetry.StateUp}}
	fo := newHubFailover(eps, health, &recordingRemote{}, func() {}, &fakeClock{now: time.Unix(1000, 0)}, testSettle, discardLogger(t))

	provider := newEndpointsProvider(ids, map[string]*hubFailover{"onlypeer": fo})
	got := provider()

	if len(got) != 2 {
		t.Fatalf("endpoints = %+v, want 2", got)
	}
	for i, e := range got {
		if e.Peer != "" {
			t.Fatalf("endpoint[%d].Peer = %q, want \"\" on a single-bound-peer config (back-compat)", i, e.Peer)
		}
	}
	if !got[0].Active || got[1].Active {
		t.Fatalf("active flags = %+v, want [0] active", got)
	}
	if got[0].Address != "203.0.113.30:51820" || got[1].Address != "198.51.100.30:51820" {
		t.Fatalf("addresses = %+v, want the configured order preserved", got)
	}
}
