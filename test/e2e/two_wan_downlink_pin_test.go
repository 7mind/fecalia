//go:build e2e

package e2e

// TestE2ETwoWANDownlinkPinsActive (T248, defect D94, goal G27) is the netns
// resolution gate for the D94 downlink-pinning fix (T246): on a SINGLE-socket
// concentrator serving a TWO-WAN active-backup edge, downlink DATA must ride the
// edge's ACTIVE WAN (not flap onto the metered standby at probe cadence), and must
// FAIL OVER onto the surviving WAN when the active one dies. It also carries a
// GATED assertion for the G26 (T242) resequencer immediate-release counter.
//
// TOPOLOGY — the netns realization of D94's "standard one-socket concentrator".
// The defect's mechanism is purely that the concentrator has ONE socket and
// demuxInbound (multipath.go, locate by symbol) routes EVERY edge-WAN source address
// to the SAME single peerPathState (D94 root cause). The faithful netns analog
// of a concentrator with one public listener reachable over both edge WANs is a
// concentrator that declares ONE path bound to the WILDCARD address (source_addr =
// "0.0.0.0"): its single UDP socket receives datagrams arriving on BOTH veths (the
// edge's path A → concIP-A, path B → concIP-B), so the concentrator sees two
// distinct edge source APs on one socket — exactly the D94 condition. The two WANs
// remain SEPARATE veth pairs (DefaultPaths: starlink=paths[0]=ACTIVE,
// cellular=paths[1]=STANDBY), so each is independently Blackhole-able for the
// failover phase, and the edge's per-path rx_bytes counter cleanly attributes each
// downlink datagram to the WAN it arrived on.
//
// HOW THE PER-WAN DOWNLINK SPLIT IS COUNTED. Downlink DATA (concentrator → edge)
// egresses the concentrator's single socket toward the learned edge source AP and
// arrives on that WAN's edge veth, where the edge path socket bound to that WAN's
// source address receives it and charges wanbond_path_rx_bytes_total{path=<wan>}
// (multipath.go rxBytes accounting — every inbound outer datagram counts). The edge
// exposes /metrics on t248MetricsListen; the active-WAN downlink SHARE over a window
// is Δrx[active] / (Δrx[active] + Δrx[standby]). Probe/echo traffic is symmetric
// across both WANs and small (200 ms cadence) relative to a saturating DATA stream,
// so the share is dominated by where the downlink DATA lands. This is the sanctioned
// counting method (edge-side per-path rx byte counters via /metrics).
//
// RED-FIRST EVIDENCE (argument; the RED run is deferred to the T249 hardware pass —
// the local sandbox has no privileged netns for the -tags e2e tier). Against the
// PRE-FIX daemon (commit 381fc66, before T246's per-sender-path remote table) this
// fixture is RED by the CONFIRMED D94 mechanism (H94, 10 orchestrator-validated
// evidence items): the concentrator's Probe branch calls ps.setRemote(srcAP)
// UNCONDITIONALLY on every authenticated probe, setRemote overwrites the learned
// remote every call, and the DATA branch never sets it — so learning is probe-only.
// The edge's emitProbes iterates EVERY (peer,path) prober INCLUDING the idle metered
// standby and egresses each on its own per-path socket at DefaultProbeInterval =
// 200 ms, so the single-socket concentrator's ONE learned remote alternates between
// the edge's two WAN source addresses at ~200 ms cadence. Downlink DATA targets
// exactly ps.getRemote() of the scheduler-picked (single) path, so the remote flap IS
// a destination flap: ~50 % of wall-clock — hence ~50 % of downlink DATA — lands on
// the METERED STANDBY WAN. The pre-fix run therefore fails the steady-state assertion
// (active share ≈ 0.5, far below t248MinActiveShare = 0.95). The unit-level RED
// evidence for the same mechanism was captured in T246
// (bind.TestConcentratorDownlinkPinsToActivePath was RED with "after standby probe
// getRemote = <standby>, want <active> STICKY"). On the FIXED tree this fixture is
// GREEN: T246 pins selection to the edge's active path (sticky, address-match-gated
// DATA confirmation; the authenticated probe plane owns freshness).
//
// FAILOVER window rationale. When the active WAN dies the concentrator's single
// declared path gives the scheduler nothing to switch, so the DESTINATION itself
// must follow the surviving WAN. The bind-level fallback is checkRemoteDead's
// one-time sticky move once the SELECTED entry has seen no authenticated probe for
// bind.remoteDeadAfter = 2 × telemetry.DefaultDownAfter = 2.4 s. t248DownlinkFailoverBound
// mirrors that bound plus harness slack, so the post-failover measurement waits past
// the destination-fallback horizon before asserting the downlink has resumed on the
// standby WAN. (The edge's own uplink active-backup failover at DownAfter = 1.2 s can
// move the destination sooner via confirmDataRemote once uplink DATA rides the
// standby, but the belt-and-suspenders remoteDeadAfter path is the bound this test
// waits on.)
//
// G26 (T242) INTERACTION — GATED. D94's flap-alternated downlink disarms G26/D93's
// single-path immediate release (the flap makes the edge resequencer see a 2-key
// trailing set, so the conservative full hold stays armed). Once the flap is gone the
// edge resequencer's immediate-release counter should engage. T242 (the hol-metrics
// task) may not be merged in this base, so the assertion is GATED: it probes /metrics
// for a resequencer immediate-release series (t248ImmediateRelease) and, when ABSENT,
// t.Log()s and SKIPS the sub-assertion rather than merging a red assertion. When T242
// has merged and the series is present, it asserts the counter MOVED across the
// steady-state window.
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

	// t248SteadyWindow is the steady-state observation window over which the per-WAN
	// downlink rx split is measured. 4 s spans ~20 probe intervals, so probe/echo
	// overhead on the standby is dominated by the saturating downlink DATA stream, and
	// the pre-fix ~50 % flap versus the post-fix ~100 % pin are cleanly separable at the
	// 0.95 threshold.
	t248SteadyWindow = 4 * time.Second

	// t248MinActiveShare is the minimum share of downlink bytes that must arrive on the
	// ACTIVE WAN in steady state (task acceptance: >= 95 %). Pre-fix behaviour is ~50 %
	// on the standby at the 200 ms probe cadence; the fix pins ~100 % to the active WAN
	// (the small remainder is symmetric probe/echo overhead on the standby).
	t248MinActiveShare = 0.95

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

func TestE2ETwoWANDownlinkPinsActive(t *testing.T) {
	bin := buildWanbond(t)
	top := Setup(t)

	active := DefaultPaths[primaryPathIdx] // starlink — active
	standby := DefaultPaths[backupPathIdx] // cellular — metered standby

	edge, conc := setupT248SingleSocketConc(t, top, bin, DefaultPaths)
	if !top.pingUntil(concInner, 15*time.Second) {
		t.Fatalf("tunnel never came up\n--- edge ---\n%s\n--- conc ---\n%s", edge.log(), conc.log())
	}

	// Both WANs must be liveness-UP before measuring: the standby's presence (and its
	// own probe stream to the concentrator) is what makes the pre-fix flap possible, so
	// a measurement taken before it is up would not exercise the defect.
	waitPathUp(t, t248MetricsURL, active.name, 1, 10*time.Second)
	waitPathUp(t, t248MetricsURL, standby.name, 1, 10*time.Second)

	// --- Phase 1: steady-state downlink pins to the ACTIVE WAN. ---
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
	holBefore, holSeries, holPresent := t248ImmediateRelease(before)
	time.Sleep(t248SteadyWindow)
	after := scrapeMetrics(t, t248MetricsURL)

	activeDelta := t248RxDelta(t, before, after, active.name)
	standbyDelta := t248RxDelta(t, before, after, standby.name)
	total := activeDelta + standbyDelta
	if total < t248MinDownlinkBytes {
		t.Fatalf("steady-state downlink volume too low to judge the split: active(%q) Δrx=%.0f + standby(%q) Δrx=%.0f = %.0f bytes < floor %d — the downlink stream did not run\n--- edge ---\n%s",
			active.name, activeDelta, standby.name, standbyDelta, total, t248MinDownlinkBytes, edge.log())
	}
	activeShare := activeDelta / total
	t.Logf("steady state: downlink rx active(%q)=%.0f standby(%q)=%.0f bytes; active share = %.3f",
		active.name, activeDelta, standby.name, standbyDelta, activeShare)
	if activeShare < t248MinActiveShare {
		t.Errorf("steady-state downlink active-WAN share = %.3f, want >= %.2f — the concentrator's single-socket downlink is not pinned to the edge's ACTIVE WAN (D94: last-prober flap splits ~50%% onto the metered standby %q at the 200 ms probe cadence)\n--- edge ---\n%s",
			activeShare, t248MinActiveShare, standby.name, edge.log())
	}

	// --- G26 (T242) GATED sub-assertion: the resequencer immediate-release counter
	// engaged once the flap is gone. Present only when T242's hol-metrics have merged. ---
	if holPresent {
		holAfter, _, _ := t248ImmediateRelease(after)
		if holAfter <= holBefore {
			t.Errorf("resequencer immediate-release counter %q did not move across the steady-state window (%.0f -> %.0f) — with the D94 flap gone the edge's single-source immediate release should engage\n--- edge ---\n%s",
				holSeries, holBefore, holAfter, edge.log())
		} else {
			t.Logf("G26: immediate-release counter %q moved %.0f -> %.0f over the steady window", holSeries, holBefore, holAfter)
		}
	} else {
		t.Logf("G26: no resequencer immediate-release series present on /metrics (T242 hol-metrics not merged in this base) — SKIPPING the immediate-release sub-assertion; enable it once T242 lands")
	}

	// --- Phase 2: kill the ACTIVE WAN — the downlink must fail over to the STANDBY. ---
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
	if foStandbyShare < t248MinActiveShare {
		t.Errorf("post-failover downlink surviving-WAN share = %.3f, want >= %.2f — the concentrator's downlink did not follow the edge onto the surviving WAN %q after the active WAN %q died (bind.remoteDeadAfter = 2×DownAfter destination fallback)\n--- edge ---\n%s",
			foStandbyShare, t248MinActiveShare, standby.name, active.name, edge.log())
	}
}

// setupT248SingleSocketConc brings up the two-WAN active-backup edge (both DefaultPaths
// uplinks, /metrics on t248MetricsListen, default active-backup scheduler) against a
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
	// socket). active-backup is the default (no [scheduler] block).
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

// t248ImmediateRelease locates the G26 (T242) resequencer immediate-release counter in a
// scrape and returns its value, the matched series name, and whether it is present. The
// exact series name is owned by T242 (not merged in this base), so the lookup is by
// pattern — any resequencer family whose name signals immediate release ("immediate" or
// the D94-cited "hol" head-of-line marker) — rather than a hard-coded name that could
// drift from T242's final choice. Absence gates the sub-assertion off (see the caller).
func t248ImmediateRelease(exp metrics.Exposition) (value float64, series string, present bool) {
	for name := range exp.Families() {
		lower := strings.ToLower(name)
		if !strings.Contains(lower, "resequencer") {
			continue
		}
		if !strings.Contains(lower, "immediate") && !strings.Contains(lower, "hol") {
			continue
		}
		if v, ok := exp.Value(name); ok {
			return v, name, true
		}
	}
	return 0, "", false
}
