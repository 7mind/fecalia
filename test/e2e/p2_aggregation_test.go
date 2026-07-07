//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/7mind/wanbond/internal/metrics"
)

// P2 (aggregation + data-thrift) e2e tuning. These are derived from the emulated
// per-path rate cap and the inner MTU, and are the levers the on-hardware operator
// retunes if the fixture's achievable frame rate differs from the emulation.
//
// WHY A RATE CAP IS MANDATORY (the T23 well-definedness note). Without a per-path
// bandwidth cap the fixture's bottleneck is the single userspace WireGuard crypto core
// (~150-170 Mbit/s on one vCPU), NOT the links — and that one crypto engine is SHARED by
// both paths. Bonding two uncapped paths therefore does NOT raise throughput (the CPU
// ceiling is unchanged), so "sum of the two paths' individual throughputs" is ill-defined
// and the aggregation ratio would be vacuous (~0.5, always failing). Capping each path
// well BELOW the crypto ceiling makes the LINK the bottleneck: a solo capped path
// saturates at ~its cap, and a bonded run can approach the SUM of the two caps while the
// aggregate stays under the crypto ceiling. Only then is the >= P2BondedMinFraction ratio
// a real aggregation measurement rather than a CPU-ceiling artefact.
const (
	// p2RateMbit is the per-path netem egress cap. Two paths => ~2*p2RateMbit aggregate,
	// which MUST stay below the host's single-core WG crypto ceiling for aggregation to
	// show. 40+40=80 Mbit/s clears a ~150 Mbit/s ceiling with margin; raise on a faster
	// host only while keeping 2*p2RateMbit under that host's measured ceiling.
	p2RateMbit = 40

	// p2MetricsListen is the loopback /metrics endpoint both daemons bind (each in its own
	// network namespace, so the identical address does not collide). p2MetricsURL is the
	// scrape URL metrics.Fetch drives.
	p2MetricsListen = "127.0.0.1:9095"
	p2MetricsURL    = "http://" + p2MetricsListen + "/metrics"

	// p2PerPathCapacityFPS sets the weighted scheduler's aggregation gate. Aggregation
	// engages once the offered frame rate exceeds EngageFraction(0.9)*capacity and
	// collapses to primary-only below DisengageFraction(0.5)*capacity. It is chosen so a
	// SINGLE saturated capped path (~p2RateMbit/8/innerMTU ≈ 3400 fps at 40 Mbit/s and a
	// ~1400-byte inner MTU) sits ABOVE the engage threshold — so a bonded saturating flow
	// (which first fills the primary, driving offered load past engage) opens the backup —
	// while a sub-capacity thrift flow stays below the disengage threshold and collapses.
	// 3000 fps => engage at 2700 (< ~3400 one-path fps: engages under bond) and disengage
	// at 1500 (a <15 Mbit/s thrift flow, ~1300 fps, stays collapsed). Retune with the cap.
	p2PerPathCapacityFPS = 3000.0

	// p2LoadSecs is the saturating flow duration; p2WindowSettle/p2WindowSecs carve the
	// steady-state measurement window out of the middle so ramp-up and tail are excluded.
	p2LoadSecs     = 16
	p2WindowSettle = 5 * time.Second
	p2WindowSecs   = 7

	// p2ThriftBitrate is the sub-one-path-capacity offered load for the data-thrift phase:
	// low enough that the weighted gate stays collapsed on the primary, so the metered
	// (5G/cellular) path carries < P2MeteredMaxByteFraction of DATA bytes.
	p2ThriftBitrate = "12M"
	p2ThriftSecs    = 12
)

// TestP2Aggregation is the P2 acceptance (T23): under saturating load the bonded
// throughput reaches at least P2BondedMinFraction of the sum of the two paths' INDIVIDUAL
// (solo) throughputs, and while the primary (Starlink) is healthy a sub-capacity flow
// keeps the metered backup (5G/cellular) below P2MeteredMaxByteFraction of DATA bytes.
// BOTH quantities are read from the daemon's /metrics endpoint (via metrics.Fetch), not
// from iperf3 or a packet capture — the endpoint is the operator-facing surface this
// phase certifies.
//
// Interpretation of "bonded >= 0.85 * sum of the two paths' individual throughputs"
// (documented because the phrasing admits more than one reading): each path's INDIVIDUAL
// throughput is measured in its OWN solo run (only that path configured), and the bonded
// AGGREGATE wire throughput (both paths, from /metrics) is compared against 0.85 * their
// sum. All three quantities are per-path OUTER-wire throughput read from /metrics
// (wanbond_path_tx/rx_bytes_total deltas), so the comparison is dimensionally consistent
// (wire-vs-wire) and robust: it directly tests that bonding preserves each path's solo
// capacity (aggregation efficiency), rather than folding the goodput/wire framing overhead
// into the threshold. The subtests run sequentially (no t.Parallel) so each solo topology
// is torn down before the next Setup — the fixed veth names forbid two live topologies.
func TestP2Aggregation(t *testing.T) {
	bin := buildWanbond(t)

	var soloStarlink, soloCellular float64
	t.Run("solo-starlink", func(t *testing.T) {
		soloStarlink = runSoloSaturated(t, bin, "starlink")
		t.Logf("solo starlink wire throughput = %.1f Mbit/s", soloStarlink)
	})
	t.Run("solo-cellular", func(t *testing.T) {
		soloCellular = runSoloSaturated(t, bin, "cellular")
		t.Logf("solo cellular wire throughput = %.1f Mbit/s", soloCellular)
	})

	t.Run("bonded-aggregation", func(t *testing.T) {
		if soloStarlink <= 0 || soloCellular <= 0 {
			t.Fatalf("solo baselines not measured (starlink=%.1f cellular=%.1f); cannot judge aggregation",
				soloStarlink, soloCellular)
		}
		bondedAgg := runBondedSaturated(t, bin)
		want := P2BondedMinFraction * (soloStarlink + soloCellular)
		t.Logf("bonded aggregate wire throughput = %.1f Mbit/s; want >= %.1f (%.2f * (%.1f + %.1f))",
			bondedAgg, want, P2BondedMinFraction, soloStarlink, soloCellular)
		if bondedAgg < want {
			t.Fatalf("bonded aggregate throughput %.1f Mbit/s < %.1f Mbit/s (%.0f%% of the solo sum) — aggregation did not reach P2BondedMinFraction",
				bondedAgg, want, P2BondedMinFraction*100)
		}
	})

	t.Run("data-thrift", func(t *testing.T) {
		runDataThrift(t, bin)
	})
}

// runSoloSaturated brings the tunnel up over a SINGLE rate-capped path with the weighted
// scheduler + /metrics, saturates it, and returns that path's steady-state OUTER-wire
// throughput (Mbit/s) read from the edge /metrics byte counters.
func runSoloSaturated(t *testing.T, bin, name string) float64 {
	t.Helper()
	spec := p2Path(name)
	top := SetupWithPaths(t, []pathSpec{spec})
	edge, conc := setupP2Tunnel(t, top, bin, []pathSpec{spec})
	if !top.pingUntil(concInner, 15*time.Second) {
		t.Fatalf("solo %s: tunnel never came up\n--- edge ---\n%s\n--- conc ---\n%s", name, edge.log(), conc.log())
	}
	tputs := top.measureSaturatedWireThroughput(t, []string{name})
	return tputs[name]
}

// runBondedSaturated brings the tunnel up over BOTH rate-capped paths with the weighted
// scheduler, saturates it so aggregation engages, and returns the AGGREGATE (both paths)
// OUTER-wire throughput (Mbit/s) from the edge /metrics. It also scrapes the CONCENTRATOR
// /metrics (via metrics.Fetch inside the concentrator netns) to satisfy the both-daemons
// requirement and to confirm the far end observed both paths carrying the bonded flow.
func runBondedSaturated(t *testing.T, bin string) float64 {
	t.Helper()
	paths := p2Paths()
	top := SetupWithPaths(t, paths)
	edge, conc := setupP2Tunnel(t, top, bin, paths)
	if !top.pingUntil(concInner, 15*time.Second) {
		t.Fatalf("bonded: tunnel never came up\n--- edge ---\n%s\n--- conc ---\n%s", edge.log(), conc.log())
	}

	tputs := top.measureSaturatedWireThroughput(t, []string{"starlink", "cellular"})

	// Both daemons via metrics.Fetch: the concentrator endpoint is loopback-bound INSIDE
	// its own netns, so it is scraped from within that netns. Assert its per-path series
	// are present and both paths received bonded traffic (rx > 0) — the far-end cross-check
	// that aggregation, not single-path fallback, delivered the flow.
	concExp := fetchMetricsInNetns(t, top.pid, p2MetricsURL)
	for _, p := range []string{"starlink", "cellular"} {
		rx, ok := concExp.PathValue(metrics.MetricRxBytes, p)
		if !ok {
			t.Fatalf("concentrator /metrics missing %s{path=%q}", metrics.MetricRxBytes, p)
		}
		if rx <= 0 {
			t.Errorf("concentrator saw 0 rx bytes on path %q — aggregation did not use it", p)
		}
	}

	return tputs["starlink"] + tputs["cellular"]
}

// runDataThrift drives a SUB-capacity flow while the primary is healthy and asserts the
// metered backup carries < P2MeteredMaxByteFraction of the DATA (tx) bytes, all read from
// the edge /metrics. It measures the DELTA over the flow window (not cumulative counters)
// so any startup transient before the weighted gate settled is excluded.
func runDataThrift(t *testing.T, bin string) {
	t.Helper()
	paths := p2Paths()
	top := SetupWithPaths(t, paths)
	edge, conc := setupP2Tunnel(t, top, bin, paths)
	if !top.pingUntil(concInner, 15*time.Second) {
		t.Fatalf("thrift: tunnel never came up\n--- edge ---\n%s\n--- conc ---\n%s", edge.log(), conc.log())
	}

	// Let the offered-load meter settle to the idle (collapsed) state before sampling.
	time.Sleep(2 * time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	before := fetchMetrics(t, ctx, p2MetricsURL)

	// Primary must be healthy for the data-thrift guarantee to apply.
	if up, ok := before.PathValue(metrics.MetricUp, "starlink"); !ok || up != 1 {
		t.Fatalf("primary (starlink) not healthy at thrift start (up=%v ok=%v) — thrift precondition unmet\n%s",
			up, ok, edge.log())
	}

	// A bandwidth-limited flow that fits comfortably inside one path's capacity, so the
	// weighted gate stays collapsed on the primary.
	top.startProc(t, "iperf3-thrift-server", "nsenter", "-t", strconv.Itoa(top.pid), "-n", "iperf3", "-s", "-1", "-B", concInner)
	time.Sleep(500 * time.Millisecond)
	top.run("iperf3", "-c", concInner, "-t", strconv.Itoa(p2ThriftSecs), "-b", p2ThriftBitrate)

	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	after := fetchMetrics(t, ctx2, p2MetricsURL)

	starTx := deltaPathValue(t, before, after, metrics.MetricTxBytes, "starlink")
	cellTx := deltaPathValue(t, before, after, metrics.MetricTxBytes, "cellular")
	total := starTx + cellTx
	if total <= 0 {
		t.Fatalf("no DATA tx observed over the thrift window (starlink=%.0f cellular=%.0f) — flow did not run", starTx, cellTx)
	}
	frac := cellTx / total
	t.Logf("data-thrift: cellular tx = %.0f B, starlink tx = %.0f B, cellular fraction = %.4f (want < %.4f)",
		cellTx, starTx, frac, P2MeteredMaxByteFraction)
	if frac >= P2MeteredMaxByteFraction {
		t.Fatalf("metered (cellular) carried %.2f%% of DATA bytes while primary healthy, want < %.2f%% — data-thrift violated",
			frac*100, P2MeteredMaxByteFraction*100)
	}
}

// measureSaturatedWireThroughput starts a saturating TCP upload, waits for steady state,
// scrapes the edge /metrics at the window's start and end, and returns each named path's
// OUTER-wire throughput (Mbit/s) = (delta tx+rx bytes)*8 / window. The upload direction
// exercises edge egress aggregation; the reverse ACKs are small and counted uniformly in
// every run, so they do not skew the wire-vs-wire comparison.
func (top *Topology) measureSaturatedWireThroughput(t *testing.T, names []string) map[string]float64 {
	t.Helper()
	top.startProc(t, "iperf3-load-server", "nsenter", "-t", strconv.Itoa(top.pid), "-n", "iperf3", "-s", "-1", "-B", concInner)
	time.Sleep(500 * time.Millisecond)

	// The load runs in the background (startProc registers its own terminating cleanup);
	// we sample /metrics across a steady-state window inside its lifetime.
	top.startProc(t, "iperf3-load", "iperf3", "-c", concInner, "-t", strconv.Itoa(p2LoadSecs))

	time.Sleep(p2WindowSettle)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	before := fetchMetrics(t, ctx, p2MetricsURL)
	start := time.Now()

	time.Sleep(p2WindowSecs * time.Second)
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	after := fetchMetrics(t, ctx2, p2MetricsURL)
	elapsed := time.Since(start).Seconds()

	out := make(map[string]float64, len(names))
	for _, name := range names {
		txd := deltaPathValue(t, before, after, metrics.MetricTxBytes, name)
		rxd := deltaPathValue(t, before, after, metrics.MetricRxBytes, name)
		out[name] = (txd + rxd) * 8 / elapsed / 1e6
	}
	return out
}

// deltaPathValue returns after-before for a per-path counter series, failing if either
// scrape lacked the series (a missing series is a wiring defect, not a zero).
func deltaPathValue(t *testing.T, before, after metrics.Exposition, name, path string) float64 {
	t.Helper()
	b, ok := before.PathValue(name, path)
	if !ok {
		t.Fatalf("first scrape missing %s{path=%q}", name, path)
	}
	a, ok := after.PathValue(name, path)
	if !ok {
		t.Fatalf("second scrape missing %s{path=%q}", name, path)
	}
	return a - b
}

// fetchMetrics scrapes an in-namespace /metrics endpoint via metrics.Fetch.
func fetchMetrics(t *testing.T, ctx context.Context, url string) metrics.Exposition {
	t.Helper()
	exp, err := metrics.Fetch(ctx, http.DefaultClient, url)
	if err != nil {
		t.Fatalf("scrape %s: %v", url, err)
	}
	return exp
}

// fetchMetricsInNetns scrapes a loopback /metrics endpoint that lives inside the
// concentrator's network namespace, via metrics.Fetch. It runs the fetch on a goroutine
// pinned (runtime.LockOSThread) to a thread that it moves into the peer netns with
// unix.Setns; the goroutine then EXITS WITHOUT unlocking, so the Go runtime terminates
// that now-namespace-polluted OS thread rather than returning it (dirty) to the pool. Only
// the socket DIAL is namespace-sensitive and it happens synchronously inside metrics.Fetch
// on this locked thread; once connected, the socket's namespace is fixed, so the HTTP
// read/write may run anywhere. This is the standard Go idiom for a one-shot cross-netns
// dial and needs no /run/netns mount.
func fetchMetricsInNetns(t *testing.T, pid int, url string) metrics.Exposition {
	t.Helper()
	type result struct {
		exp metrics.Exposition
		err error
	}
	done := make(chan result, 1)
	go func() {
		runtime.LockOSThread() // deliberately never unlocked: goroutine exit kills the thread

		nsPath := fmt.Sprintf("/proc/%d/ns/net", pid)
		f, err := os.Open(nsPath)
		if err != nil {
			done <- result{err: fmt.Errorf("open %s: %w", nsPath, err)}
			return
		}
		defer f.Close()
		if err := unix.Setns(int(f.Fd()), unix.CLONE_NEWNET); err != nil {
			done <- result{err: fmt.Errorf("setns into concentrator netns: %w", err)}
			return
		}

		// A dedicated client (no keep-alive) so the dial — the only netns-sensitive step —
		// resolves on THIS locked thread inside the concentrator netns.
		client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		exp, err := metrics.Fetch(ctx, client, url)
		done <- result{exp: exp, err: err}
	}()

	r := <-done
	if r.err != nil {
		t.Fatalf("scrape concentrator %s in netns: %v", url, r.err)
	}
	return r.exp
}

// p2Path returns the named DefaultPaths spec with the P2 per-path rate cap applied.
func p2Path(name string) pathSpec {
	for _, p := range DefaultPaths {
		if p.name == name {
			p.rateMbit = p2RateMbit
			return p
		}
	}
	panic("p2Path: unknown path " + name)
}

// p2Paths returns both DefaultPaths specs with the P2 per-path rate cap applied.
func p2Paths() []pathSpec {
	return []pathSpec{p2Path("starlink"), p2Path("cellular")}
}

// setupP2Tunnel brings up the edge+concentrator tunnel over paths with the WEIGHTED
// scheduler (so aggregation engages under load) and the /metrics endpoint enabled on both
// ends. It mirrors setupMultipathTunnel's addressing/bring-up but adds the [scheduler] and
// [metrics] config blocks T23 needs.
func setupP2Tunnel(t *testing.T, top *Topology, bin string, paths []pathSpec) (edge, conc *proc) {
	t.Helper()

	edgePriv, edgePub := genKey(t)
	concPriv, concPub := genKey(t)
	psk := randKey(t)

	var edgePaths, concPaths strings.Builder
	for _, p := range paths {
		fmt.Fprintf(&edgePaths, "[[paths]]\nname = %q\nsource_addr = %q\ndest_addr = \"%s:%d\"\n\n", p.name, p.edgeIP, p.concIP, listenPort)
		fmt.Fprintf(&concPaths, "[[paths]]\nname = %q\nsource_addr = %q\n\n", p.name, p.concIP)
	}
	primary := paths[0]

	// The weighted scheduler with an aggregation gate sized to the emulated per-path cap
	// (see p2PerPathCapacityFPS). Pacing is left OFF so the LINKS, not a token bucket, are
	// the throughput bottleneck; the gate alone engages/collapses aggregation.
	schedBlock := fmt.Sprintf("[scheduler]\npolicy = \"weighted\"\nper_path_capacity_fps = %.1f\n\n", p2PerPathCapacityFPS)
	metricsBlock := fmt.Sprintf("[metrics]\nlisten = %q\n\n", p2MetricsListen)

	dir := t.TempDir()
	edgeCfg := writeConfig(t, filepath.Join(dir, "edge.toml"), fmt.Sprintf(`role = "edge"
psk = "%s"

%s%s%s[wireguard]
private_key = "%s"

[[wireguard.peers]]
public_key = "%s"
endpoint = "%s:%d"
allowed_ips = ["%s/32"]

[log]
level = "error"
`, psk, edgePaths.String(), schedBlock, metricsBlock, edgePriv, concPub, primary.concIP, listenPort, concInner))

	concCfg := writeConfig(t, filepath.Join(dir, "conc.toml"), fmt.Sprintf(`role = "concentrator"
psk = "%s"

%s%s%s[wireguard]
private_key = "%s"
listen_port = %d

[[wireguard.peers]]
public_key = "%s"
allowed_ips = ["%s/32"]

[log]
level = "error"
`, psk, concPaths.String(), schedBlock, metricsBlock, concPriv, listenPort, edgePub, edgeInner))

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
