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

// T61 (G2/W2): exercise enabled exact-byte per-path shaping, sized from an
// operator-declared link_bandwidth/link_rtt, under sustained overload. Netns RTT
// and throughput remain report-only because the fixture is CPU/PPS-bound; the
// functional gate verifies that PROBE liveness survives saturation.
//
// Tuning constants for this file only (mirrors the per-file constant blocks in
// baseline_test.go/p2_aggregation_test.go/fixture_impairment_test.go).
const (
	// pacingRateMbit defines both the netem cap and the declared link_bandwidth.
	// The measurements below characterize a run but do not assert throughput or
	// bufferbloat from this CPU/PPS-bound fixture.
	pacingRateMbit = 4

	// pacingLoadSecs is the saturating-flow duration for both report-only load
	// measurements and (via startSaturatingLoad's p2LoadSecs) the rekey/probe
	// window sizing below. Long enough for the low-rate cap's queue to fill and the
	// loaded RTT to stabilize (idem bdpLoadSecs's rationale), short enough to keep
	// the suite bounded.
	pacingLoadSecs = 10

	// pacingSettle is the settle delay after tunnel bring-up before measuring,
	// letting liveness, scheduler telemetry, and exact-byte shaper state reach
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

// TestPacingBufferbloat records unpaced and paced RTT/throughput over the same
// capped path without treating CPU/PPS-bound netns measurements as acceptance
// criteria. Its functional gate verifies that direct PROBE/ECHO writes remain
// live under saturation while their encoded bytes advance future DATA debt.
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
		t.Fatalf("unpaced baseline subtest failed; report-only comparison unavailable")
	}

	t.Run("paced-report-only", func(t *testing.T) {
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
		t.Logf("report-only comparison: paced Δ=%.1fms, unpaced Δ=%.1fms; exact-byte acceptance is counter-based in TestPacingTCPByteEnvelope",
			pacedBloatMs, unpacedBloatMs)
	})

	t.Run("rekey-survives-saturation", func(t *testing.T) {
		edge, conc := setupPacingTunnel(t, top, bin, true, linkRTT)
		if !top.pingUntil(concInner, 15*time.Second) {
			t.Fatalf("rekey-check: tunnel never came up\n--- edge ---\n%s\n--- conc ---\n%s", edge.log(), conc.log())
		}
		// pingUntil's first successful reply proves the initial WireGuard handshake,
		// whose control-path selection uses PickUnpaced rather than DATA scheduling.
		// A REAL WG rekey only fires on amneziawg-go's ~2-minute inner cadence, far
		// beyond what a bounded e2e run should wait on; wanbond's own PROBE liveness
		// frames (telemetry.DefaultProbeInterval = 200ms) are the practical,
		// frequent proxy for "control-class traffic keeps getting through under
		// saturation" this test exercises instead. emitProbes writes directly to the
		// per-path socket; each successful PROBE/ECHO write calls AccountPriority for
		// its exact encoded bytes, advancing only future DATA debt while admitted
		// DATA deadlines remain immutable.
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
			t.Fatalf("path %q liveness (PROBE control-plane traffic) failed MID-saturation (up=%v ok=%v): "+
				"direct priority delivery or CPU contention did not preserve control/probe traffic\n--- edge ---\n%s", pacingPath.name, up, ok, edge.log())
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
// pattern. When pacingEnabled it declares link_bandwidth (the exact-byte refill
// rate R) and link_rtt (used with R to size B=ceil(R*RTT)); C=Lmax independently.
// It then turns pacing_enabled on. The unpaced config omits both, so the only
// config delta is shaping itself.
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
