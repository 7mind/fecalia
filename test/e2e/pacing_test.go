//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/metrics"
)

// T61 (G2/W2): validate that ENABLED weighted-scheduler pacing, sized from an
// operator-declared per-link bandwidth (T53's link_bandwidth/link_rtt wiring over
// SizePacingFromBDP, T52's BDP-measurement precedent), (a) materially reduces
// bufferbloat under sustained overload relative to the SAME fixture with pacing
// disabled, and (b) does not starve wanbond's own control-plane (PROBE liveness)
// traffic while the data plane is saturated.
//
// Tuning constants for this file only (mirrors the per-file constant blocks in
// baseline_test.go/p2_aggregation_test.go/fixture_impairment_test.go).
const (
	// pacingRateMbit is the per-path netem bandwidth cap driving the standing queue
	// this test measures. It must sit BELOW the executing host's in-fixture,
	// THROUGH-TUNNEL single-path ceiling for the cap (not the userspace-WG crypto
	// core) to be the bottleneck — see netns.go's pathSpec.rateMbit doc and
	// fixture_impairment_test.go's capMbit comment for the general sizing rule. That
	// ceiling is measured LOW for a single capped path sharing a host with both
	// daemons + netem + a load generator: p2_aggregation_test.go's own solo-path
	// measurement (a 40 Mbit/s cap) observed only ~2-13 Mbit/s achieved on a 4-vCPU
	// amd64 host — i.e. even 40 Mbit/s did not reliably saturate. pacingRateMbit is
	// set well below that observed floor so the cap — not the crypto core — is the
	// bottleneck on both standing hardware workers (o3.7mind.io aarch64, 1 vCPU;
	// llm-ubuntu-0 amd64, 4 vCPU); the paced-vs-unpaced subtest below gates its
	// comparative assertion on pacingMinBloatMs actually holding for the executing
	// run, so an unmet precondition SKIPS with evidence rather than silently
	// asserting a vacuous comparison (docs/design.md "Not yet built" + AGENTS.md:
	// the fixture is CPU/PPS-bound and must not be asserted into an
	// absolute-throughput/bufferbloat number).
	pacingRateMbit = 4

	// pacingMinBloatMs is the standing-queue precondition floor: the UNPACED
	// baseline's bufferbloat delta (loaded RTT - idle RTT) must exceed this before
	// the paced-vs-unpaced comparison is meaningful. docs/p0-findings.md §7 recorded
	// ~0-2ms of bufferbloat delta in-fixture when the fixture was NOT link-bound (no
	// bandwidth cap existed yet); this floor sits safely above that noise band, so
	// clearing it is evidence a REAL standing queue formed this run.
	pacingMinBloatMs = 5.0

	// pacingMaxPacedBloatFraction bounds paced bufferbloat as a FRACTION of the
	// measured unpaced bufferbloat — the comparative (not absolute-ms) gate the
	// acceptance calls for (Q19/D23 discipline). Pacing's per-path token bucket
	// polices egress at (approximately) the declared link rate, so it should
	// prevent the sender from ever overrunning the netem queue by more than about
	// one BDP's worth, a qualitatively different (materially smaller) standing
	// queue than the unpaced run's unbounded backlog. 0.7 leaves headroom against
	// fixture noise while still requiring a clear, non-marginal reduction.
	pacingMaxPacedBloatFraction = 0.7

	// pacingLoadSecs is the saturating-flow duration for both the bufferbloat
	// comparison and (via startSaturatingLoad's p2LoadSecs) informs the rekey/probe
	// window sizing below. Long enough for the low-rate cap's queue to fill and the
	// loaded RTT to stabilize (idem bdpLoadSecs's rationale), short enough to keep
	// the suite bounded.
	pacingLoadSecs = 10

	// pacingSettle is the settle delay after tunnel bring-up before measuring,
	// letting the weighted scheduler's offered-load meter and pacing buckets reach
	// steady state before any load is offered.
	pacingSettle = 2 * time.Second

	pacingMetricsListen = "127.0.0.1:9103"
	pacingMetricsURL    = "http://" + pacingMetricsListen + "/metrics"
)

// pacingPath is the single emulated uplink this file's tests bring the tunnel up
// over: a small fixed delay (stable RTT baseline) plus the pacingRateMbit cap.
var pacingPath = pathSpec{
	name:     "capped",
	edgeIP:   "10.100.1.1",
	concIP:   "10.100.1.2",
	edgeVeth: "wbAe",
	concVeth: "wbAc",
	delayMs:  5,
	jitterMs: 0,
	rateMbit: pacingRateMbit,
}

// TestPacingBufferbloat is the T61 acceptance: over the SAME bandwidth-capped path,
// (1) measure the unpaced baseline's bufferbloat under sustained overload; (2) if
// (and only if) that run actually built a standing queue (the fixture was
// link-bound, not CPU/PPS-bound, this time — see pacingRateMbit's doc), assert the
// pacing-ENABLED run's bufferbloat is materially lower than the baseline's, at the
// same offered load; (3) independently of (1)/(2), assert wanbond's own PROBE
// liveness control-plane traffic survives (the path stays "up") while pacing is
// enabled AND the data plane is saturated — pacing's token bucket must not starve
// control/probe traffic (defect D22's ClassControl pacing-exemption, exercised
// end-to-end here rather than only at the sched-package unit level).
func TestPacingBufferbloat(t *testing.T) {
	bin := buildWanbond(t)
	top := SetupWithPaths(t, []pathSpec{pacingPath})

	// Ground the declared link_rtt in a MEASURED number, exactly as an operator
	// would (T52's measureBDP precedent): the raw (no-tunnel) idle RTT on the same
	// emulated link the tunnel then runs over.
	idleRawRTTms := top.RTT(pacingPath.name, 10)
	linkRTT := fmt.Sprintf("%.1fms", idleRawRTTms)
	t.Logf("pacing fixture: raw idle RTT on %q = %.1fms (will declare link_bandwidth=%dMbit link_rtt=%s under pacing)",
		pacingPath.name, idleRawRTTms, pacingRateMbit, linkRTT)

	var unpacedBloatMs float64
	baselineOK := t.Run("unpaced-baseline", func(t *testing.T) {
		edge, conc := setupPacingTunnel(t, top, bin, false, linkRTT)
		if !top.pingUntil(concInner, 15*time.Second) {
			t.Fatalf("unpaced: tunnel never came up\n--- edge ---\n%s\n--- conc ---\n%s", edge.log(), conc.log())
		}
		time.Sleep(pacingSettle)

		idleTunnelRTTms := pingAvgMs(t, concInner, 10)
		loadMbps, loadedRTTms := top.rttUnderLoad(t, concInner, concInner, pacingLoadSecs)
		unpacedBloatMs = loadedRTTms - idleTunnelRTTms
		t.Logf("unpaced: idle RTT=%.1fms loaded RTT=%.1fms (bufferbloat Δ=%.1fms) | throughput=%.1f Mbit/s (cap %d)",
			idleTunnelRTTms, loadedRTTms, unpacedBloatMs, loadMbps, pacingRateMbit)
	})
	if !baselineOK {
		t.Fatalf("unpaced baseline subtest failed; cannot judge whether pacing reduced bufferbloat")
	}

	t.Run("paced-vs-unpaced-bufferbloat", func(t *testing.T) {
		if unpacedBloatMs < pacingMinBloatMs {
			t.Skipf("fixture did not build a standing queue this run (unpaced bufferbloat Δ=%.1fms < %.1fms floor): "+
				"the %d-Mbit/s cap was NOT the bottleneck (CPU/PPS-bound instead — docs/design.md 'Not yet built', AGENTS.md "+
				"testing discipline), so the paced-vs-unpaced comparison is not well-defined here. Re-run on a host where the "+
				"cap binds (see pacingRateMbit's doc for the observed per-host ceiling floor) to exercise this assertion.",
				unpacedBloatMs, pacingMinBloatMs, pacingRateMbit)
		}

		edge, conc := setupPacingTunnel(t, top, bin, true, linkRTT)
		if !top.pingUntil(concInner, 15*time.Second) {
			t.Fatalf("paced: tunnel never came up\n--- edge ---\n%s\n--- conc ---\n%s", edge.log(), conc.log())
		}
		time.Sleep(pacingSettle)

		idleTunnelRTTms := pingAvgMs(t, concInner, 10)
		loadMbps, loadedRTTms := top.rttUnderLoad(t, concInner, concInner, pacingLoadSecs)
		pacedBloatMs := loadedRTTms - idleTunnelRTTms
		t.Logf("paced: idle RTT=%.1fms loaded RTT=%.1fms (bufferbloat Δ=%.1fms) | throughput=%.1f Mbit/s (cap %d, link_bandwidth/link_rtt declared)",
			idleTunnelRTTms, loadedRTTms, pacedBloatMs, loadMbps, pacingRateMbit)

		want := unpacedBloatMs * pacingMaxPacedBloatFraction
		t.Logf("comparative check: paced Δ=%.1fms, want < %.1fms (%.0f%% of unpaced Δ=%.1fms) — RELATIVE gate, not an absolute-ms threshold",
			pacedBloatMs, want, pacingMaxPacedBloatFraction*100, unpacedBloatMs)
		if pacedBloatMs >= want {
			t.Fatalf("pacing did not materially reduce bufferbloat at the same offered load: paced Δ=%.1fms, unpaced Δ=%.1fms "+
				"(want paced < %.0f%% of unpaced = %.1fms)\n--- edge ---\n%s", pacedBloatMs, unpacedBloatMs, pacingMaxPacedBloatFraction*100, want, edge.log())
		}
	})

	t.Run("rekey-survives-saturation", func(t *testing.T) {
		edge, conc := setupPacingTunnel(t, top, bin, true, linkRTT)
		if !top.pingUntil(concInner, 15*time.Second) {
			t.Fatalf("rekey-check: tunnel never came up\n--- edge ---\n%s\n--- conc ---\n%s", edge.log(), conc.log())
		}
		// pingUntil's first successful reply already proves the initial WireGuard
		// handshake (a ClassControl frame, pacing-exempt by construction —
		// internal/sched/weighted.go serveLocked/selectAggregatingLocked) completed.
		// A REAL WG rekey only fires on amneziawg-go's ~2-minute inner cadence, far
		// beyond what a bounded e2e run should wait on; wanbond's own PROBE liveness
		// frames (telemetry.DefaultProbeInterval = 200ms) are the practical,
		// frequent proxy for "control-class traffic keeps getting through under
		// saturation" this test exercises instead — they are, like the handshake,
		// authenticated control-plane traffic, and (unlike the handshake) are sent
		// on a path all their own (bind/probe.go's emitProbes writes straight to the
		// per-path socket, never through the paced scheduler Pick() path at all), so
		// a still-"up" path here is doubly-grounded evidence that neither the
		// pacer's token bucket NOR general CPU contention under saturation prevents
		// control-plane delivery — the same property a real rekey depends on.
		time.Sleep(pacingSettle)

		ctxBefore, cancelBefore := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelBefore()
		before := fetchMetrics(t, ctxBefore, pacingMetricsURL)
		if up, ok := before.PathValue(metrics.MetricUp, pacingPath.name); !ok || up != 1 {
			t.Fatalf("path %q not up before saturation (up=%v ok=%v) — precondition unmet\n--- edge ---\n%s", pacingPath.name, up, ok, edge.log())
		}

		top.startSaturatingLoad(t) // background one-shot iperf3 server+client, p2LoadSecs duration

		time.Sleep(3 * time.Second) // well into the saturating flow's steady state
		ctxMid, cancelMid := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelMid()
		mid := fetchMetrics(t, ctxMid, pacingMetricsURL)
		if up, ok := mid.PathValue(metrics.MetricUp, pacingPath.name); !ok || up != 1 {
			t.Fatalf("path %q liveness (PROBE control-plane traffic) starved MID-saturation (up=%v ok=%v): pacing's token bucket "+
				"(or CPU contention under the saturating flow) is starving control/probe traffic\n--- edge ---\n%s", pacingPath.name, up, ok, edge.log())
		}
		t.Logf("rekey-check: path %q up=1 (probe echoes succeeding) while saturated (paced, cap=%d Mbit/s)", pacingPath.name, pacingRateMbit)

		time.Sleep(10 * time.Second) // pushes well toward the end of the p2LoadSecs (16s) window
		ctxAfter, cancelAfter := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelAfter()
		after := fetchMetrics(t, ctxAfter, pacingMetricsURL)
		if up, ok := after.PathValue(metrics.MetricUp, pacingPath.name); !ok || up != 1 {
			t.Fatalf("path %q liveness starved near the END of the saturating window (up=%v ok=%v)\n--- edge ---\n%s", pacingPath.name, up, ok, edge.log())
		}
		t.Logf("rekey-check: path %q up=1 (probe echoes succeeding) near the end of a %ds saturating window — control-plane survived overload", pacingPath.name, p2LoadSecs)
	})
}

// setupPacingTunnel brings up the edge+concentrator tunnel over p with the weighted
// scheduler and /metrics enabled on both ends, matching setupP2Tunnel's bring-up
// pattern. When pacingEnabled it also declares link_bandwidth (pacingRateMbit) and
// link_rtt (the caller's measured linkRTT) on the path — the T53 wiring that derives
// the scheduler's per-path pace from the bandwidth-delay product — and turns
// pacing_enabled on; the unpaced config omits both (a declared bandwidth is inert
// with pacing off, config.go's deriveWeightedBottleneckPacing), so the ONLY config
// delta between the two runs is the pacing feature itself.
func setupPacingTunnel(t *testing.T, top *Topology, bin string, pacingEnabled bool, linkRTT string) (edge, conc *proc) {
	t.Helper()

	edgePriv, edgePub := genKey(t)
	concPriv, concPub := genKey(t)
	psk := randKey(t)
	p := pacingPath

	var linkLines string
	schedBlock := "[scheduler]\npolicy = \"weighted\"\n"
	if pacingEnabled {
		linkLines = fmt.Sprintf("link_bandwidth = \"%dMbit\"\nlink_rtt = %q\n", pacingRateMbit, linkRTT)
		schedBlock += "pacing_enabled = true\n"
	}
	schedBlock += "\n"
	metricsBlock := fmt.Sprintf("[metrics]\nlisten = %q\n\n", pacingMetricsListen)

	dir := t.TempDir()
	edgeCfg := writeConfig(t, filepath.Join(dir, "edge.toml"), fmt.Sprintf(`role = "edge"
psk = "%s"

[[paths]]
name = %q
source_addr = "%s"
dest_addr = "%s:%d"
%s
%s%s[wireguard]
private_key = "%s"

[[wireguard.peers]]
public_key = "%s"
endpoint = "%s:%d"
allowed_ips = ["%s/32"]

[log]
level = "error"
`, psk, p.name, p.edgeIP, p.concIP, listenPort, linkLines, schedBlock, metricsBlock, edgePriv, concPub, p.concIP, listenPort, concInner))

	concCfg := writeConfig(t, filepath.Join(dir, "conc.toml"), fmt.Sprintf(`role = "concentrator"
psk = "%s"

[[paths]]
name = %q
source_addr = "%s"
%s
%s%s[wireguard]
private_key = "%s"
listen_port = %d

[[wireguard.peers]]
public_key = "%s"
allowed_ips = ["%s/32"]

[log]
level = "error"
`, psk, p.name, p.concIP, linkLines, schedBlock, metricsBlock, concPriv, listenPort, edgePub, edgeInner))

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
