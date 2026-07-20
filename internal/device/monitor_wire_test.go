package device

import (
	"context"
	"crypto/ecdh"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/amnezia-vpn/amneziawg-go/tun/tuntest"
	"github.com/coder/websocket"
	"go.uber.org/goleak"

	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/dnsresolve"
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

	// LIVE-provider failover: build a controller with two endpoints directly, wrap it in the
	// PRODUCTION provider, and assert the active entry MOVES after a forced switch — the same
	// provider closure returns the fresh active state on its second call (R242 freshness).
	eps := mustEndpoints(t, "203.0.113.1:51820", "198.51.100.7:51820")
	hp := []hubHealth{&fakeHealth{telemetry.StateUp}, &fakeHealth{telemetry.StateUp}}
	clk := &fakeClock{now: time.Unix(1000, 0)}
	fo := newHubFailover(eps, hp, &recordingRemote{}, func() {}, clk, testSettle, discardLogger(t))
	provider := newEndpointsProvider(fo)

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
