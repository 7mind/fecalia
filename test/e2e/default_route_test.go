//go:build e2e

package e2e

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The two /1 prefixes wg-quick (and uapiConfig's splitDefaultRoute) expand a /0
// default route into. mode=default-route installs exactly these into wanbond0.
const (
	defaultRouteLo = "0.0.0.0/1"
	defaultRouteHi = "128.0.0.0/1"
	// arbitraryDest is a destination covered by 0.0.0.0/1 but by no other route in the
	// fixture netns (which has no default route and only per-path /24s), so a route
	// lookup for it resolves to wanbond0 ONLY when the split default route is installed.
	arbitraryDest = "8.8.8.8"
)

// TestDefaultRouteWiring is the I6/Q41 acceptance: with mode=default-route on the
// edge, the daemon installs the two /1 routes via wanbond0 after the interface is up,
// an arbitrary destination egresses through the tunnel (its route lookup resolves to
// wanbond0), and the routes are WITHDRAWN on Close.
//
// It sets tun_persist=true deliberately: the interface then SURVIVES Close, so the
// post-Close "routes are gone" assertion exercises the daemon's own route REMOVAL
// (removeRoutes) rather than being trivially satisfied by the interface — and its
// routes — disappearing on teardown. No concentrator process is needed: route
// programming happens after dev.Up (tolerant boot, like the tun_persist tests), and
// "egresses through the tunnel" is proven operationally by the kernel route lookup
// selecting wanbond0 — actually forwarding an arbitrary destination end-to-end would
// require the concentrator ip_forward/MASQUERADE that Q41 EXPLICITLY excludes from
// this task (the documented C3/C6 recipes), so it is out of scope here by design.
//
// Gated behind the e2e build tag (netns + root); exercised for COMPILATION and vet in
// CI, privileged execution deferred to the hardware suite (G2 pattern).
func TestDefaultRouteWiring(t *testing.T) {
	top := Setup(t)
	p := top.path("starlink")
	bin := buildWanbond(t)

	edgePriv, _ := genKey(t)
	_, concPub := genKey(t)
	psk := randKey(t)

	dir := t.TempDir()
	// tun_persist=true so wanbond0 outlives Close and the route-removal path is what
	// clears the /1 routes. mode="default-route" opts the peer into full-tunnel wiring;
	// allowed_ips = 0.0.0.0/0 is the /0 that splits into the two /1 routes.
	cfg := writeConfig(t, filepath.Join(dir, "edge.toml"), fmt.Sprintf(`role = "edge"
psk = "%s"
tun_persist = true

[[paths]]
name = "starlink"
source_addr = "%s"

[wireguard]
private_key = "%s"

[[wireguard.peers]]
public_key = "%s"
endpoint = "%s:%d"
allowed_ips = ["0.0.0.0/0"]
mode = "default-route"

[log]
level = "error"
`, psk, p.edgeIP, edgePriv, concPub, p.concIP, listenPort))

	// Clean up the persistent device so it does not leak into later subtests sharing
	// this netns (the fixture Teardown only removes the path veths, not wanbond0).
	t.Cleanup(func() { _ = top.tryRun("ip", "link", "del", tunDev) })

	edge := top.startProc(t, "edge", bin, "--config", cfg)
	if !top.waitLink(tunDev, false, 5*time.Second) {
		t.Fatalf("wanbond0 never appeared\n%s", edge.log())
	}

	// Operator-owned addressing (the daemon assigns none): give wanbond0 an inner
	// address so the route lookup below can select a source. The route INSTALL itself
	// does not depend on this — the daemon installed the /1s at bring-up.
	top.run("ip", "addr", "add", edgeInner+"/24", "dev", tunDev)

	// The two /1 routes must be present via wanbond0 while the daemon runs.
	if got := top.routesOnDev(t, tunDev); !strings.Contains(got, defaultRouteLo) || !strings.Contains(got, defaultRouteHi) {
		t.Fatalf("default-route wiring absent: `ip route show dev %s` = %q, want both %s and %s\n%s",
			tunDev, strings.TrimSpace(got), defaultRouteLo, defaultRouteHi, edge.log())
	}

	// An arbitrary destination egresses through the tunnel: its route lookup resolves
	// to wanbond0 (covered by 0.0.0.0/1, and by nothing else in this netns).
	if got := top.routeGet(t, arbitraryDest); !strings.Contains(got, "dev "+tunDev) {
		t.Fatalf("arbitrary destination %s does not egress through the tunnel: `ip route get` = %q, want dev %s",
			arbitraryDest, strings.TrimSpace(got), tunDev)
	}

	// Stop the daemon. With tun_persist=true wanbond0 survives, so ONLY the daemon's
	// own route removal can withdraw the /1 routes.
	top.stopAndWait(t, edge)
	if !top.waitLink(tunDev, false, 2*time.Second) {
		t.Fatalf("persistent wanbond0 disappeared after stop (tun_persist=true) — cannot attribute route removal to the daemon")
	}
	if got := top.routesOnDev(t, tunDev); strings.Contains(got, defaultRouteLo) || strings.Contains(got, defaultRouteHi) {
		t.Fatalf("default-route wiring survived Close: `ip route show dev %s` still = %q", tunDev, strings.TrimSpace(got))
	}
	t.Logf("mode=default-route: two /1 routes installed via wanbond0, arbitrary dest egressed the tunnel, routes withdrawn on Close (I6)")
}

// TestDefaultRouteRegressionGuard is the I6/Q41 default-unchanged case: WITHOUT
// mode=default-route the daemon installs NO route into wanbond0 at all — the first
// route programming is confined to the mode being explicitly enabled.
func TestDefaultRouteRegressionGuard(t *testing.T) {
	top := Setup(t)
	p := top.path("starlink")
	bin := buildWanbond(t)

	edgePriv, _ := genKey(t)
	_, concPub := genKey(t)
	psk := randKey(t)

	dir := t.TempDir()
	// A plain edge config: no mode, a host-route allowed_ip. The daemon must program
	// no routes (addressing and routing stay operator-owned).
	cfg := writeConfig(t, filepath.Join(dir, "edge.toml"), fmt.Sprintf(`role = "edge"
psk = "%s"

[[paths]]
name = "starlink"
source_addr = "%s"

[wireguard]
private_key = "%s"

[[wireguard.peers]]
public_key = "%s"
endpoint = "%s:%d"
allowed_ips = ["%s/32"]

[log]
level = "error"
`, psk, p.edgeIP, edgePriv, concPub, p.concIP, listenPort, concInner))

	edge := top.startProc(t, "edge", bin, "--config", cfg)
	if !top.waitLink(tunDev, false, 5*time.Second) {
		t.Fatalf("wanbond0 never appeared\n%s", edge.log())
	}

	// No default route without the mode. `ip route show dev wanbond0` must carry neither
	// /1 route; with no operator address assigned it is empty entirely.
	if got := top.routesOnDev(t, tunDev); strings.Contains(got, defaultRouteLo) || strings.Contains(got, defaultRouteHi) || strings.TrimSpace(got) != "" {
		t.Fatalf("route installed without mode=default-route: `ip route show dev %s` = %q, want none\n%s",
			tunDev, strings.TrimSpace(got), edge.log())
	}
	t.Logf("without mode=default-route: no route installed on wanbond0 (default behaviour unchanged)")
}

// routesOnDev returns `ip route show dev <dev>` for the edge (test) network namespace.
func (top *Topology) routesOnDev(t *testing.T, dev string) string {
	t.Helper()
	return top.runOut("ip", "route", "show", "dev", dev)
}

// routeGet returns `ip route get <dst>` for the edge (test) network namespace — the
// kernel's route lookup, naming the device an arbitrary destination would egress.
func (top *Topology) routeGet(t *testing.T, dst string) string {
	t.Helper()
	return top.runOut("ip", "route", "get", dst)
}
