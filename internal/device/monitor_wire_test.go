package device

import (
	"context"
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

	tun, err := up(cfg, discardLogger(t), chtun.TUN(), "wanbondtest0", inertFactory)
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

			tun, err := up(cfg, discardLogger(t), chtun.TUN(), "wanbondtest0", inertFactory)
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

	tun, err := up(cfg, discardLogger(t), chtun.TUN(), "wanbondtest0", inertFactory)
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

	tun, err := up(cfg, discardLogger(t), chtun.TUN(), "wanbondtest0", inertFactory)
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
