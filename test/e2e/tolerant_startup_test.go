//go:build e2e

package e2e

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/bind"
	"github.com/7mind/wanbond/internal/metrics"
	"github.com/7mind/wanbond/internal/telemetry"
)

// T60 is the netns e2e for the W1 startup-resilience feature END TO END: T51's
// tolerant Open() (defer a well-formed but not-yet-assignable source_addr instead of
// refusing the whole bond) composed with T55's background reconcile (promote a
// deferred path to live once its address appears), plus the two fast-fail edges
// (zero bindable paths; a malformed source_addr) that tolerance must NOT paper over.

const (
	// t60MetricsListen is the edge's /metrics endpoint for this file's tests, on a
	// port none of the other e2e files use (see test/e2e/p2/p3/p4/pacing for 9095-97).
	t60MetricsListen = "127.0.0.1:9098"
	t60MetricsURL    = "http://" + t60MetricsListen + "/metrics"

	// t60UnassignableSrcA/B mirror internal/bind/tolerant_bind_test.go's
	// unassignableSource: 192.0.2.0/24 is TEST-NET-1 (RFC 5737), reserved for
	// documentation and guaranteed never configured on a real interface, so a bind
	// against it deterministically fails EADDRNOTAVAIL without any netns fixture.
	t60UnassignableSrcA = "192.0.2.1"
	t60UnassignableSrcB = "192.0.2.2"
)

// t60DeferredPath is the THIRD emulated uplink (distinct veth names from
// DefaultPaths' starlink/cellular): its edge address is deliberately withheld at
// Setup (deferEdgeAddr), so wbCe exists and is up but 10.100.3.1 is not yet owned by
// any interface — the well-formed-but-not-yet-assignable condition T51 defers.
// top.AddEdgeAddr("modem5g") later adds it, simulating the address appearing.
var t60DeferredPath = pathSpec{
	name: "modem5g", edgeIP: "10.100.3.1", concIP: "10.100.3.2",
	edgeVeth: "wbCe", concVeth: "wbCc", delayMs: 20,
	deferEdgeAddr: true,
}

// t60PromoteTimeout bounds how long the T55 background reconcile may take to bind,
// promote, and bring the deferred path's liveness to Up after its address appears:
// one worst-case reconcile tick (bind.DefaultReconcileInterval, the poll cadence)
// plus the liveness up-transition (DefaultUpSuccesses consecutive probe echoes at
// DefaultProbeInterval), plus harness slack for scheduling jitter under the fixture's
// shared CPU — mirroring the analytical-bound style of thresholds.go's
// PLivenessFailoverBudget/PLivenessDetectBudget.
const t60PromoteTimeout = bind.DefaultReconcileInterval +
	time.Duration(telemetry.DefaultUpSuccesses)*telemetry.DefaultProbeInterval +
	3*time.Second

// TestTolerantStartupDeferredPathPromotes is the T60 acceptance: bring the bond up
// with TWO paths where one (modem5g) names a well-formed source_addr that is not yet
// assignable on any interface. The bond must come up on the survivor (starlink) —
// handshake + traffic — while modem5g is present in config but excluded from the
// live path set (not fatal). Adding the missing address must then let T55's
// background reconcile bind, promote, and carry traffic on modem5g.
func TestTolerantStartupDeferredPathPromotes(t *testing.T) {
	bin := buildWanbond(t)
	survivor := DefaultPaths[0] // starlink
	paths := []pathSpec{survivor, t60DeferredPath}
	top := SetupWithPaths(t, paths)
	edge, conc := setupT60Tunnel(t, top, bin, paths, t60MetricsListen)

	// (1) The bond comes up on the survivor alone: modem5g's source_addr is not yet
	// assignable, so its bind deferred rather than failing the whole Open (T51).
	if !top.pingUntil(concInner, 15*time.Second) {
		t.Fatalf("bond never came up on the survivor path %q while %q was deferred\n--- edge ---\n%s\n--- conc ---\n%s",
			survivor.name, t60DeferredPath.name, edge.log(), conc.log())
	}
	mbps := top.iperf3Mbps(t, concInner, 3)
	if mbps <= 0 {
		t.Fatalf("survivor path %q carried non-positive throughput %.2f Mbit/s", survivor.name, mbps)
	}

	// The deferred path is PRESENT in config but excluded from the live/served set:
	// bind.Multipath.PathSnapshots() — what the /metrics endpoint scrapes — reports
	// only BOUND paths (m.paths), so a still-deferred path's wanbond_path_up series is
	// simply ABSENT, not zero. This operationalizes "present-but-down, not fatal":
	// the daemon is running, the survivor is up, and the deferred path has no live
	// series until T55 promotes it.
	before := scrapeMetrics(t, t60MetricsURL)
	if _, ok := before.PathValue(metrics.MetricUp, t60DeferredPath.name); ok {
		t.Fatalf("deferred path %q already has a wanbond_path_up series before its address was added; it should be absent (not yet promoted)", t60DeferredPath.name)
	}
	if v, ok := before.PathValue(metrics.MetricUp, survivor.name); !ok || v != 1 {
		t.Fatalf("survivor path %q wanbond_path_up = %v (present=%v), want 1", survivor.name, v, ok)
	}
	t.Logf("pre-promote: survivor %q up (iperf3=%.1f Mbit/s), deferred %q absent from live metrics as expected", survivor.name, mbps, t60DeferredPath.name)

	// (2) The missing address appears — e.g. the 5G modem's DHCP lease completes.
	top.AddEdgeAddr(t60DeferredPath.name)

	// T55's background reconcile must bind, promote, and bring the path's liveness to
	// Up within the analytical budget above.
	waitPathUp(t, t60MetricsURL, t60DeferredPath.name, 1, t60PromoteTimeout)

	// Prove the PROMOTED path itself carries traffic (not merely that the bond
	// survives): blackhole the survivor so only the reconciled path can forward. The
	// active-backup scheduler's Pick can only land here once modem5g is genuinely
	// admitted and healthy.
	top.Blackhole(survivor.name)
	if !top.pingUntil(concInner, time.Duration(P1RecoverySeconds)*time.Second+2*time.Second) {
		t.Fatalf("tunnel did not recover on the promoted path %q once the survivor %q was blackholed\n--- edge ---\n%s\n--- conc ---\n%s",
			t60DeferredPath.name, survivor.name, edge.log(), conc.log())
	}
	promotedMbps := top.iperf3Mbps(t, concInner, 3)
	if promotedMbps <= 0 {
		t.Fatalf("promoted path %q carried non-positive throughput %.2f Mbit/s", t60DeferredPath.name, promotedMbps)
	}
	top.Restore(survivor.name)

	t.Logf("T60: survivor %q up first (iperf3=%.1f Mbit/s) while %q was deferred; after its address was added, T55 promoted it and it alone carried iperf3=%.1f Mbit/s",
		survivor.name, mbps, t60DeferredPath.name, promotedMbps)
}

// TestTolerantStartupFastFailModes asserts the two edges tolerance must NOT paper
// over: zero bindable paths (every source_addr unassignable) is a FATAL Open, and a
// malformed source_addr is rejected at config LOAD, before the daemon ever attempts a
// bind. Both run in the test-process network namespace (the TestMain re-exec unshared
// it); neither needs the veth topology, since both source addresses are TEST-NET-1
// (RFC 5737, never assignable on any real interface) or simply invalid syntax.
func TestTolerantStartupFastFailModes(t *testing.T) {
	bin := buildWanbond(t)

	t.Run("zero_bindable_paths_is_fatal", func(t *testing.T) {
		// Determinism (hardware-robustness fix): the zero-bindable premise is that BOTH
		// source_addrs fail to bind, i.e. a bind to a non-local address returns
		// EADDRNOTAVAIL. On this kernel that rejection needs TWO conditions, BOTH pinned
		// here so the premise holds regardless of host default or subtest order:
		//   (1) at least one interface UP — with lo DOWN a non-local bind SUCCEEDS even at
		//       ip_nonlocal_bind=0 (empirical kernel probe on llm-ubuntu-0); this subtest
		//       builds no topology, so it must bring lo up itself (bringLoopbackUp);
		//   (2) net.ipv4.ip_nonlocal_bind=0 — a host at 1 lets a non-local bind succeed
		//       (disableNonlocalBind, which fails loudly if it cannot pin the value).
		// Together they make "no configured path can bind -> fatal Open" deterministic.
		// The original fix pinned only (2) and mis-identified the variable, so the daemon
		// came up (both TEST-NET-1 addresses bound) and the test hung — the false-negative
		// observed on llm-ubuntu-0 when this subtest ran alone (lo down).
		bringLoopbackUp(t)
		disableNonlocalBind(t)

		cfg := writeT60EdgeConfig(t, fmt.Sprintf(`[[paths]]
name = "a"
source_addr = "%s"

[[paths]]
name = "b"
source_addr = "%s"
`, t60UnassignableSrcA, t60UnassignableSrcB))

		code, out := runWanbondOnce(t, bin, cfg, 10*time.Second)
		if code == 0 {
			t.Fatalf("wanbond exited 0 with zero bindable paths (both %s/%s are TEST-NET-1, never assignable); want a fatal non-zero exit\n%s",
				t60UnassignableSrcA, t60UnassignableSrcB, out)
		}
		if !strings.Contains(out, "wanbond starting") {
			t.Fatalf("expected the daemon to log startup before failing at Open — that is what distinguishes this fatal-Open case from a config-load error; output:\n%s", out)
		}
		if !strings.Contains(out, "no configured path could bind") {
			t.Fatalf("expected the bind's zero-bindable fatal error message; output:\n%s", out)
		}
		t.Logf("zero-bindable-paths: wanbond exited %d as expected:\n%s", code, out)
	})

	t.Run("malformed_source_addr_is_config_load_error", func(t *testing.T) {
		cfg := writeT60EdgeConfig(t, `[[paths]]
name = "a"
source_addr = "not-an-ip-address"
`)

		code, out := runWanbondOnce(t, bin, cfg, 10*time.Second)
		if code == 0 {
			t.Fatalf("wanbond exited 0 with a malformed source_addr; want a config-load error\n%s", out)
		}
		if strings.Contains(out, "wanbond starting") {
			t.Fatalf("a malformed source_addr must be rejected at config LOAD, before the daemon starts (the startup log should never appear); output:\n%s", out)
		}
		if !strings.Contains(out, "invalid source_addr") {
			t.Fatalf("expected config.normalize's invalid-source_addr error; output:\n%s", out)
		}
		t.Logf("malformed-source_addr: wanbond exited %d at config load as expected:\n%s", code, out)
	})
}

// writeT60EdgeConfig writes a minimal, otherwise-valid edge config (role, psk,
// wireguard private/peer key, a bogus-but-syntactically-fine peer endpoint) with
// pathsBlock spliced in as the [[paths]] section(s) — the ONLY thing under test in
// TestTolerantStartupFastFailModes. Neither subtest reaches the peer, so the
// endpoint/allowed_ips values need not be routable.
//
// [log] level is "info" (NOT "error"): the zero-bindable subtest distinguishes a
// fatal Open() from a config-load failure by asserting the "wanbond starting" marker
// (main.Info, cmd/wanbond/main.go) IS present — that marker is logged at Info, so an
// "error" level would suppress it and the assertion could never hold (the daemon runs
// but logs nothing at Info). The malformed-source_addr subtest still sees NO
// "wanbond starting" because config load fails BEFORE that log, independent of level.
func writeT60EdgeConfig(t *testing.T, pathsBlock string) string {
	t.Helper()
	edgePriv, _ := genKey(t)
	_, concPub := genKey(t)
	psk := randKey(t)
	dir := t.TempDir()
	return writeConfig(t, filepath.Join(dir, "edge.toml"), fmt.Sprintf(`role = "edge"
psk = "%s"

%s
[wireguard]
private_key = "%s"

[[wireguard.peers]]
public_key = "%s"
endpoint = "192.0.2.53:51820"
allowed_ips = ["10.10.0.2/32"]

[log]
level = "info"
`, psk, pathsBlock, edgePriv, concPub))
}

// runWanbondOnce runs bin --config cfgPath to completion — no signal is sent; the
// process must exit ON ITS OWN — within deadline, and returns its exit code and
// combined stdout+stderr. It fails the test if the process does not exit within the
// deadline: for these two fast-fail scenarios a hang would mean Open (or config
// load) blocked instead of failing fast, which is itself a defect worth surfacing
// loudly rather than silently killing the process and reporting a false pass.
func runWanbondOnce(t *testing.T, bin, cfgPath string, deadline time.Duration) (exitCode int, output string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "--config", cfgPath)
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("wanbond did not exit within %s (expected a fast fail); output so far:\n%s", deadline, out)
	}
	if err == nil {
		return 0, string(out)
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), string(out)
	}
	t.Fatalf("run wanbond --config %s: %v\n%s", cfgPath, err, out)
	return -1, string(out)
}

// setupT60Tunnel is setupMultipathTunnelLevel plus an edge [metrics] listen block:
// T60 needs to scrape the edge's per-path wanbond_path_up series to observe the
// deferred path being absent, then promoted. It otherwise mirrors
// setupMultipathTunnelLevel's config shape and bring-up sequence exactly.
func setupT60Tunnel(t *testing.T, top *Topology, bin string, paths []pathSpec, metricsListen string) (edge, conc *proc) {
	t.Helper()

	edgePriv, edgePub := genKey(t)
	concPriv, concPub := genKey(t)
	psk := randKey(t)

	var edgePaths, concPaths strings.Builder
	for _, p := range paths {
		fmt.Fprintf(&edgePaths, "[[paths]]\nname = %q\nsource_addr = %q\ndest_addr = \"%s:%d\"\n\n", p.name, p.edgeIP, p.concIP, listenPort)
		fmt.Fprintf(&concPaths, "[[paths]]\nname = %q\nsource_addr = %q\n\n", p.name, p.concIP)
	}
	// The wireguard peer endpoint (edge -> concentrator) seeds the virtual endpoint;
	// the first configured path's concentrator address serves (mirrors
	// setupMultipathTunnelLevel).
	primary := paths[0]
	metricsBlock := fmt.Sprintf("[metrics]\nlisten = %q\n\n", metricsListen)

	dir := t.TempDir()
	edgeCfg := writeConfig(t, filepath.Join(dir, "edge.toml"), fmt.Sprintf(`role = "edge"
psk = "%s"

%s%s[wireguard]
private_key = "%s"

[[wireguard.peers]]
public_key = "%s"
endpoint = "%s:%d"
allowed_ips = ["%s/32"]

[log]
level = "error"
`, psk, edgePaths.String(), metricsBlock, edgePriv, concPub, primary.concIP, listenPort, concInner))

	concCfg := writeConfig(t, filepath.Join(dir, "conc.toml"), fmt.Sprintf(`role = "concentrator"
psk = "%s"

%s[wireguard]
private_key = "%s"
listen_port = %d

[[wireguard.peers]]
public_key = "%s"
allowed_ips = ["%s/32"]

[log]
level = "error"
`, psk, concPaths.String(), concPriv, listenPort, edgePub, edgeInner))

	conc = top.startProc(t, "concentrator", "nsenter", "-t", strconv.Itoa(top.pid), "-n", bin, "--config", concCfg)
	edge = top.startProc(t, "edge", bin, "--config", edgeCfg)

	if !top.waitLink(tunDev, false, 5*time.Second) {
		t.Fatalf("edge %s never appeared\n%s", tunDev, edge.log())
	}
	if !top.waitLink(tunDev, true, 5*time.Second) {
		t.Fatalf("concentrator %s never appeared\n%s", tunDev, conc.log())
	}
	top.run("ip", "addr", "add", edgeInner+"/24", "dev", tunDev)
	top.run("ip", "link", "set", tunDev, "up")
	top.nsenter("ip", "addr", "add", concInner+"/24", "dev", tunDev)
	top.nsenter("ip", "link", "set", tunDev, "up")
	return edge, conc
}

// scrapeMetrics is a one-shot scrape of url, failing the test on any error (used
// once the endpoint is already known to be up — unlike waitPathUp's tolerant poll).
func scrapeMetrics(t *testing.T, url string) metrics.Exposition {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	exp, err := metrics.Fetch(ctx, http.DefaultClient, url)
	if err != nil {
		t.Fatalf("scrape %s: %v", url, err)
	}
	return exp
}

// waitPathUp polls url's /metrics until the named path's wanbond_path_up series
// reads want, or fails the test at deadline. A scrape error is tolerated mid-poll
// (transient — never the terminal condition), since the assertion is about the
// SERIES VALUE appearing, not about the endpoint's own availability (already proven
// up by an earlier successful scrape in the caller).
func waitPathUp(t *testing.T, url, path string, want float64, deadline time.Duration) {
	t.Helper()
	end := time.Now().Add(deadline)
	var lastErr error
	for time.Now().Before(end) {
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		exp, err := metrics.Fetch(ctx, http.DefaultClient, url)
		cancel()
		if err != nil {
			lastErr = err
		} else if v, ok := exp.PathValue(metrics.MetricUp, path); ok && v == want {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("wanbond_path_up{path=%q} never reached %v within %s (last scrape error: %v)", path, want, deadline, lastErr)
}
