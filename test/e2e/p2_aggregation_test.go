//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"net"
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

// P2 (aggregation) e2e tuning.
//
// THE SINGLE-HOST FIXTURE IS CPU/PPS-BOUND: both userspace-WireGuard daemons, the load
// generator and netem share the host's cores, so a bonded-throughput RATIO is not
// measurable here and is not asserted. What this test enforces in-fixture is the
// FUNCTIONAL two-path carriage: under a saturating flow both per-path sockets carry the
// flow's datagrams and the far end reassembles them.
const (
	// p2RateMbit is the per-path netem egress cap.
	p2RateMbit = 40

	// p2MetricsListen is the loopback /metrics endpoint both daemons bind (each in its own
	// network namespace, so the identical address does not collide). p2MetricsURL is the
	// scrape URL metrics.Fetch drives.
	p2MetricsListen = "127.0.0.1:9095"
	p2MetricsURL    = "http://" + p2MetricsListen + "/metrics"

	// p2LoadSecs is the saturating flow duration; p2WindowSettle/p2WindowSecs carve the
	// steady-state measurement window out of the middle so ramp-up and tail are excluded.
	p2LoadSecs     = 16
	p2WindowSettle = 5 * time.Second
	p2WindowSecs   = 7

	// p2StripingMinBytes is the per-path carriage floor for the striping subtest. The
	// per-path tx/rx counters count ALL outer bytes — including the per-path liveness
	// PROBES/echoes and the transport's keepalives sent on EVERY path — so a bare
	// "delta > 0" is VACUOUS (satisfied by probes with zero striping). Probe+echo traffic
	// over the ~p2WindowSecs (7 s) window is < ~20 KB/path (a few frames/s of small probes,
	// both directions). This 50 KB floor sits far above that noise yet far below what a
	// saturating striped flow delivers on the second path even at the low in-fixture rates
	// (hundreds of KB to MBs over the window), so clearing it PROVES real carriage on that
	// socket, not probe chatter.
	p2StripingMinBytes = 50_000
)

// TestP2Aggregation is the FUNCTIONAL aggregation proof, robust to the fixture's
// CPU/PPS-boundedness (it makes NO throughput-ratio assertion): the transport drives two
// REAL per-path UDP sockets concurrently end-to-end and the far end reassembles from
// both. Both quantities are read from the daemons' /metrics endpoints (via
// metrics.Fetch), not from iperf3 or a packet capture.
func TestP2Aggregation(t *testing.T) {
	bin := buildWanbond(t)

	t.Run("bonded-striping", func(t *testing.T) {
		runBondedStriping(t, bin)
	})
}

// runBondedStriping brings BOTH paths up, drives a saturating flow, and asserts — over a
// steady-state window — that:
//
//   - the edge sent the flow on BOTH per-path sockets — measured as the per-path tx DELTA
//     exceeding p2StripingMinBytes, a floor set above probe/echo noise; and
//   - the concentrator RECEIVED and reassembled it from BOTH sockets — measured as the
//     per-path rx DELTA exceeding the same floor.
func runBondedStriping(t *testing.T, bin string) {
	t.Helper()
	paths := p2Paths()
	top := SetupWithPaths(t, paths)
	edge, conc := setupP2Tunnel(t, top, bin, paths)
	if !top.pingUntil(concInner, 15*time.Second) {
		t.Fatalf("striping: tunnel never came up\n--- edge ---\n%s\n--- conc ---\n%s", edge.log(), conc.log())
	}

	top.startSaturatingLoad(t)
	time.Sleep(p2WindowSettle) // let the flow ramp onto both paths

	ctxB, cancelB := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelB()
	edgeBefore := fetchMetrics(t, ctxB, p2MetricsURL)
	concBefore := fetchMetricsInNetns(t, top.pid, p2MetricsURL)

	time.Sleep(p2WindowSecs * time.Second)

	ctxA, cancelA := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelA()
	edgeAfter := fetchMetrics(t, ctxA, p2MetricsURL)
	concAfter := fetchMetricsInNetns(t, top.pid, p2MetricsURL)

	starTx := deltaPathValue(t, edgeBefore, edgeAfter, metrics.MetricTxBytes, "starlink")
	cellTx := deltaPathValue(t, edgeBefore, edgeAfter, metrics.MetricTxBytes, "cellular")
	t.Logf("bonded-striping: edge tx over window — starlink=%.0f B, cellular=%.0f B (floor %d B)", starTx, cellTx, p2StripingMinBytes)
	if starTx < p2StripingMinBytes {
		t.Fatalf("edge sent only %.0f B on starlink under saturating load (< %d floor = probe noise) — the flow did not ride this socket\n--- edge ---\n%s",
			starTx, p2StripingMinBytes, edge.log())
	}
	if cellTx < p2StripingMinBytes {
		t.Fatalf("edge sent only %.0f B on cellular under saturating load (< %d floor = probe noise) — the transport did not stripe the flow onto the second socket\n--- edge ---\n%s",
			cellTx, p2StripingMinBytes, edge.log())
	}

	starRx := deltaPathValue(t, concBefore, concAfter, metrics.MetricRxBytes, "starlink")
	cellRx := deltaPathValue(t, concBefore, concAfter, metrics.MetricRxBytes, "cellular")
	t.Logf("bonded-striping: conc rx over window — starlink=%.0f B, cellular=%.0f B (floor %d B)", starRx, cellRx, p2StripingMinBytes)
	if starRx < p2StripingMinBytes {
		t.Fatalf("concentrator received only %.0f B on starlink over the window (< %d floor) — far end did not reassemble this path's traffic\n--- conc ---\n%s",
			starRx, p2StripingMinBytes, conc.log())
	}
	if cellRx < p2StripingMinBytes {
		t.Fatalf("concentrator received only %.0f B on cellular over the window (< %d floor = probe noise) — far end reassembled nothing from the second path; concurrent two-path carriage unproven\n--- conc ---\n%s",
			cellRx, p2StripingMinBytes, conc.log())
	}
}

// startSaturatingLoad launches a one-shot concentrator-side iperf3 server and a saturating
// TCP upload from the edge (p2LoadSecs), both in the background with their own terminating
// cleanup. Callers sample /metrics across a steady-state window inside the flow's lifetime.
func (top *Topology) startSaturatingLoad(t *testing.T) {
	t.Helper()
	top.startProc(t, "iperf3-load-server", "nsenter", "-t", strconv.Itoa(top.pid), "-n", "iperf3", "-s", "-1", "-B", concInner)
	time.Sleep(500 * time.Millisecond)
	top.startProc(t, "iperf3-load", "iperf3", "-c", concInner, "-t", strconv.Itoa(p2LoadSecs))
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

// fetchMetricsInNetns scrapes, once and fatally on error, a loopback /metrics endpoint that
// lives inside the concentrator's network namespace (pid), via a netnsMetricsClient. The
// socket must be OPENED inside the peer netns — see netnsMetricsClient for the T25 root-cause
// note on why the namespace switch belongs in the custom DialContext.
func fetchMetricsInNetns(t *testing.T, pid int, url string) metrics.Exposition {
	t.Helper()
	client := netnsMetricsClient(pid)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	exp, err := metrics.Fetch(ctx, client, url)
	if err != nil {
		t.Fatalf("scrape concentrator %s in netns: %v", url, err)
	}
	return exp
}

// netnsMetricsClient builds an http.Client whose dial opens its socket INSIDE the
// network namespace of pid, so a loopback /metrics endpoint bound in THAT namespace
// (e.g. a concentrator running in a peer netns) is reachable from the base
// test-process netns. Reusable across many scrapes (used by both the one-shot
// fetchMetricsInNetns and the tolerant hardened-fixture path-up poll).
//
// The subtlety a prior version got wrong (T25 root-cause): net/http does its dial on
// a BACKGROUND goroutine (Transport.dialConnFor), so merely moving the CALLING thread
// into the netns (LockOSThread+Setns, then Fetch) does NOT confine the socket — the
// dial goroutine runs on a different, ROOT-netns thread and connects to whatever binds
// the same loopback address in the ROOT netns. Because an edge daemon may bind the
// IDENTICAL 127.0.0.1:<port> /metrics in the root netns, such a scrape silently reads
// the EDGE endpoint instead of the concentrator (its per-path series look plausible,
// so it passed unnoticed until a test asserted a concentrator-ONLY quantity). The
// namespace switch therefore belongs in the custom DialContext — the exact place the
// socket() syscall runs — on a thread pinned (runtime.LockOSThread) and moved into the
// peer netns; that goroutine then EXITS WITHOUT unlocking, so the Go runtime discards
// the now namespace-polluted OS thread rather than returning it (dirty) to the pool.
// Only the socket creation is namespace-sensitive; once connected, the socket's
// namespace is fixed, so the HTTP read/write may run anywhere. This needs no
// /run/netns mount.
func netnsMetricsClient(pid int) *http.Client {
	nsPath := fmt.Sprintf("/proc/%d/ns/net", pid)

	dialInNetns := func(ctx context.Context, network, addr string) (net.Conn, error) {
		type result struct {
			conn net.Conn
			err  error
		}
		done := make(chan result, 1)
		go func() {
			runtime.LockOSThread() // deliberately never unlocked: goroutine exit kills the thread

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
			// The socket() syscall runs HERE, on this locked, peer-netns thread, so the
			// connection is opened inside the concentrator's namespace. addr is a loopback
			// IP literal (no DNS, single address), so net.Dialer opens exactly one socket on
			// this goroutine — no background resolver/happy-eyeballs goroutine escapes it.
			var d net.Dialer
			c, err := d.DialContext(ctx, network, addr)
			done <- result{conn: c, err: err}
		}()
		r := <-done
		return r.conn, r.err
	}

	return &http.Client{Transport: &http.Transport{DisableKeepAlives: true, DialContext: dialInNetns}}
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

// setupP2Tunnel brings up the edge+concentrator tunnel over paths with the /metrics
// endpoint enabled on both ends. It mirrors setupMultipathTunnel's addressing/bring-up
// but adds the [metrics] config block.
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

	metricsBlock := fmt.Sprintf("[metrics]\nlisten = %q\n\n", p2MetricsListen)

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

%s%s[wireguard]
private_key = "%s"
listen_port = %d

[[wireguard.peers]]
public_key = "%s"
allowed_ips = ["%s/32"]

[log]
level = "error"
`, psk, concPaths.String(), metricsBlock, concPriv, listenPort, edgePub, edgeInner))

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
