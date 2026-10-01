//go:build e2e

package e2e

// TestE2ETwoWANDownlinkFailsOver (T248, defect D94, goal G27) is the netns gate for a
// SINGLE-socket concentrator serving a TWO-WAN edge: the downlink must run over the
// concentrator's one socket while both edge WANs are up, and must FAIL OVER onto the
// surviving WAN when one of them dies.
//
// TOPOLOGY — the netns realization of D94's "standard one-socket concentrator".
// The concentrator has ONE socket and demuxInbound (multipath.go, locate by symbol)
// routes EVERY edge-WAN source address to the SAME single peerPathState. The faithful
// netns analog of a concentrator with one public listener reachable over both edge WANs
// is a concentrator that declares ONE path bound to the WILDCARD address (source_addr =
// "0.0.0.0"): its single UDP socket receives datagrams arriving on BOTH veths (the
// edge's path A → concIP-A, path B → concIP-B), so the concentrator sees two
// distinct edge source APs on one socket. The two WANs remain SEPARATE veth pairs
// (DefaultPaths: starlink=paths[0], cellular=paths[1]), so each is independently
// Blackhole-able for the failover phase, and the edge's per-path rx_bytes counter
// cleanly attributes each downlink datagram to the WAN it arrived on.
//
// HOW THE PER-WAN DOWNLINK SPLIT IS COUNTED. Downlink data (concentrator → edge)
// egresses the concentrator's single socket toward a learned edge source AP and
// arrives on that WAN's edge veth, where the edge path socket bound to that WAN's
// source address receives it and charges wanbond_path_rx_bytes_total{path=<wan>}
// (multipath.go rxBytes accounting — every inbound outer datagram counts). The edge
// exposes /metrics on t248MetricsListen. The transport schedules the downlink over
// BOTH authenticated edge mappings while both WANs are up, so the steady-state split
// between them is logged, not asserted; after one WAN dies the surviving-WAN share is
// Δrx[survivor] / (Δrx[survivor] + Δrx[dead]).
//
// FAILOVER window rationale. t248DownlinkFailoverBound mirrors bind.remoteDeadAfter =
// 2 × telemetry.DefaultDownAfter = 2.4 s — the probe-silence horizon after which the
// concentrator's checkRemoteDead moves the selected destination off the dead WAN — plus
// harness slack, so the post-failover measurement waits past that horizon before
// asserting the downlink has resumed on the surviving WAN.
//
// HARDWARE TIER — DO NOT RUN IN THE DEFAULT GATE. Like every //go:build e2e test here
// it needs root, /dev/net/tun, tc, and a network namespace; it is compiled and
// `go vet -tags e2e` / lint GREEN locally and executed ONLY on the privileged
// netns/real-host tier (o3.7mind.io aarch64 + llm-ubuntu-0 amd64), per-test isolated
// netns (o3 concentrator collision), within the e2e time budget, -count=3 stable.

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/metrics"
)

const (
	// t248MetricsListen is this file's edge /metrics endpoint, on a port no other e2e
	// file binds (see the metrics-port registry in netns.go — 9112 is the next unused
	// port). The edge runs in the base netns, so http.DefaultClient scrapes it directly.
	t248MetricsListen = "127.0.0.1:9112"
	t248MetricsURL    = "http://" + t248MetricsListen + "/metrics"

	// t248ConcWildcardSource makes the concentrator bind its single path socket to the
	// wildcard address, so ONE socket receives datagrams arriving over BOTH edge WAN
	// veths — the netns realization of D94's single-public-socket concentrator (see the
	// file header). A specific concIP would bind only one veth and could never surface
	// the two-source-into-one-socket condition the defect is about.
	t248ConcWildcardSource = "0.0.0.0"

	// t248SteadyWindow is the observation window over which the per-WAN downlink rx
	// split is measured. 4 s spans ~20 probe intervals, so probe/echo overhead is
	// dominated by the saturating downlink stream.
	t248SteadyWindow = 4 * time.Second

	// t248MinSurvivorShare is the minimum share of downlink bytes that must arrive on
	// the SURVIVING WAN after the other one died (>= 95 %; the dead WAN's link is down,
	// so its remainder is ~0).
	t248MinSurvivorShare = 0.95

	// t248MinDownlinkBytes is a floor on the total downlink volume observed across both
	// WANs in a window, so a share computed over a dead/near-idle measurement (which
	// could read a spurious 0/0 or an all-probe-overhead split) fails loudly rather than
	// passing vacuously. A saturating iperf3 downlink over t248SteadyWindow moves orders
	// of magnitude more than this; it only rejects "no DATA flowed".
	t248MinDownlinkBytes = 256 * 1024
)

// t248DownlinkFailoverBound mirrors bind.remoteDeadAfter (= 2 × telemetry.DefaultDownAfter)
// — the probe-silence horizon after which the concentrator's checkRemoteDead moves the
// downlink destination off the dead active WAN — plus harness slack, so the post-failover
// measurement waits past the destination-fallback horizon before asserting the downlink
// resumed on the surviving WAN. It is a var (not a const) because it composes the
// telemetry-derived PLivenessDownAfter.
var t248DownlinkFailoverBound = 2*PLivenessDownAfter + 2*time.Second

func TestE2ETwoWANDownlinkFailsOver(t *testing.T) {
	bin := buildWanbond(t)
	top := Setup(t)

	active := DefaultPaths[primaryPathIdx] // starlink — the WAN that is killed
	standby := DefaultPaths[backupPathIdx] // cellular — the surviving WAN

	edge, conc := setupT248SingleSocketConc(t, top, bin, DefaultPaths)
	if !top.pingUntil(concInner, 15*time.Second) {
		t.Fatalf("tunnel never came up\n--- edge ---\n%s\n--- conc ---\n%s", edge.log(), conc.log())
	}

	// Both WANs must be liveness-UP before measuring, so the concentrator's single
	// socket holds both edge mappings.
	waitPathUp(t, t248MetricsURL, active.name, 1, 10*time.Second)
	waitPathUp(t, t248MetricsURL, standby.name, 1, 10*time.Second)

	// --- Phase 1: steady-state downlink runs with both WANs up. ---
	// A saturating downlink DATA stream (concentrator → edge): the iperf3 server binds
	// the edge inner address in the base netns, the client runs INSIDE the concentrator
	// netns and connects to it, so all bulk DATA travels concentrator → edge = downlink.
	loadSecs := int(t248SteadyWindow.Seconds()) + 6
	top.startProc(t, "iperf3-server", "iperf3", "-s", "-1", "-B", edgeInner)
	time.Sleep(400 * time.Millisecond)
	top.startProc(t, "iperf3-downlink", "nsenter", "-t", strconv.Itoa(top.pid), "-n",
		"iperf3", "-c", edgeInner, "-t", strconv.Itoa(loadSecs))
	time.Sleep(600 * time.Millisecond) // let the flow ramp before sampling the window

	before := scrapeMetrics(t, t248MetricsURL)
	time.Sleep(t248SteadyWindow)
	after := scrapeMetrics(t, t248MetricsURL)

	activeDelta := t248RxDelta(t, before, after, active.name)
	standbyDelta := t248RxDelta(t, before, after, standby.name)
	total := activeDelta + standbyDelta
	if total < t248MinDownlinkBytes {
		t.Fatalf("steady-state downlink volume too low to judge the split: active(%q) Δrx=%.0f + standby(%q) Δrx=%.0f = %.0f bytes < floor %d — the downlink stream did not run\n--- edge ---\n%s",
			active.name, activeDelta, standby.name, standbyDelta, total, t248MinDownlinkBytes, edge.log())
	}
	t.Logf("steady state: downlink rx active(%q)=%.0f standby(%q)=%.0f bytes; active share = %.3f",
		active.name, activeDelta, standby.name, standbyDelta, activeDelta/total)

	// --- Phase 2: kill one WAN — the downlink must fail over to the SURVIVING one. ---
	top.Blackhole(active.name)
	t.Cleanup(func() { top.Restore(active.name) })
	// Wait past the concentrator's destination-fallback horizon (remoteDeadAfter) so the
	// selection has moved off the dead active WAN before the post-failover measurement.
	time.Sleep(t248DownlinkFailoverBound)

	// The tunnel must still carry traffic over the surviving WAN.
	if !top.pingUntil(concInner, 10*time.Second) {
		t.Fatalf("tunnel did not recover over the surviving WAN %q within the failover window\n--- edge ---\n%s\n--- conc ---\n%s",
			standby.name, edge.log(), conc.log())
	}

	foLoadSecs := int(t248SteadyWindow.Seconds()) + 6
	top.startProc(t, "iperf3-server-fo", "iperf3", "-s", "-1", "-B", edgeInner)
	time.Sleep(400 * time.Millisecond)
	top.startProc(t, "iperf3-downlink-fo", "nsenter", "-t", strconv.Itoa(top.pid), "-n",
		"iperf3", "-c", edgeInner, "-t", strconv.Itoa(foLoadSecs))
	time.Sleep(600 * time.Millisecond)

	foBefore := scrapeMetrics(t, t248MetricsURL)
	time.Sleep(t248SteadyWindow)
	foAfter := scrapeMetrics(t, t248MetricsURL)

	foStandbyDelta := t248RxDelta(t, foBefore, foAfter, standby.name)
	foActiveDelta := t248RxDelta(t, foBefore, foAfter, active.name) // ~0: link is down
	foTotal := foStandbyDelta + foActiveDelta
	if foTotal < t248MinDownlinkBytes {
		t.Fatalf("post-failover downlink volume too low to judge resumption: standby(%q) Δrx=%.0f + active(%q) Δrx=%.0f = %.0f bytes < floor %d — the downlink did not resume on the surviving WAN\n--- edge ---\n%s",
			standby.name, foStandbyDelta, active.name, foActiveDelta, foTotal, t248MinDownlinkBytes, edge.log())
	}
	foStandbyShare := foStandbyDelta / foTotal
	t.Logf("post-failover: downlink rx standby(%q)=%.0f active(%q)=%.0f bytes; standby share = %.3f",
		standby.name, foStandbyDelta, active.name, foActiveDelta, foStandbyShare)
	if foStandbyShare < t248MinSurvivorShare {
		t.Errorf("post-failover downlink surviving-WAN share = %.3f, want >= %.2f — the concentrator's downlink did not follow the edge onto the surviving WAN %q after WAN %q died\n--- edge ---\n%s",
			foStandbyShare, t248MinSurvivorShare, standby.name, active.name, edge.log())
	}
}

// setupT248SingleSocketConc brings up the two-WAN edge (both DefaultPaths
// uplinks, /metrics on t248MetricsListen) against a
// SINGLE-socket concentrator whose one path binds the WILDCARD address, so its single
// UDP socket receives over BOTH edge veths — the netns realization of D94's one-public-
// socket concentrator (see the file header). The concentrator runs in the peer netns.
func setupT248SingleSocketConc(t *testing.T, top *Topology, bin string, paths []pathSpec) (edge, conc *proc) {
	t.Helper()

	edgePriv, edgePub := genKey(t)
	concPriv, concPub := genKey(t)
	psk := randKey(t)

	// Edge: two paths, each pinned to its own WAN source address and destined to that
	// WAN's concentrator veth address (both land on the concentrator's one wildcard
	// socket).
	var edgePaths strings.Builder
	for _, p := range paths {
		fmt.Fprintf(&edgePaths, "[[paths]]\nname = %q\nsource_addr = %q\ndest_addr = \"%s:%d\"\n\n", p.name, p.edgeIP, p.concIP, listenPort)
	}

	dir := t.TempDir()
	edgeCfg := writeConfig(t, filepath.Join(dir, "edge.toml"), fmt.Sprintf(`role = "edge"
psk = "%s"

%s[metrics]
listen = "%s"

[wireguard]
private_key = "%s"

[[wireguard.peers]]
public_key = "%s"
endpoint = "%s:%d"
allowed_ips = ["%s/32"]

[log]
level = "info"
`, psk, edgePaths.String(), t248MetricsListen, edgePriv, concPub, paths[primaryPathIdx].concIP, listenPort, concInner))

	// Concentrator: ONE path, WILDCARD source so the single socket serves both edge WANs.
	concCfg := writeConfig(t, filepath.Join(dir, "conc.toml"), fmt.Sprintf(`role = "concentrator"
psk = "%s"

[[paths]]
name = "public"
source_addr = "%s"

[wireguard]
private_key = "%s"
listen_port = %d

[[wireguard.peers]]
public_key = "%s"
allowed_ips = ["%s/32"]

[log]
level = "info"
`, psk, t248ConcWildcardSource, concPriv, listenPort, edgePub, edgeInner))

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

// t248RxDelta returns the growth of wanbond_path_rx_bytes_total{path=name} between two
// edge scrapes, failing the test if either scrape lacks the series.
func t248RxDelta(t *testing.T, before, after metrics.Exposition, name string) float64 {
	t.Helper()
	b, ok := before.PathValue(metrics.MetricRxBytes, name)
	if !ok {
		t.Fatalf("first scrape missing %s{path=%q}", metrics.MetricRxBytes, name)
	}
	a, ok := after.PathValue(metrics.MetricRxBytes, name)
	if !ok {
		t.Fatalf("second scrape missing %s{path=%q}", metrics.MetricRxBytes, name)
	}
	return a - b
}
