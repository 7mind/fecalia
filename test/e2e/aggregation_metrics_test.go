//go:build e2e

package e2e

import (
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/metrics"
)

// T146 (G13, Q54): the /metrics half of the per-peer aggregation-gate plumbing. The
// weighted scheduler's data-thrift gate is exposed as four PER-PEER gauges —
// wanbond_aggregation_engaged, wanbond_offered_load_fps, and the STATIC
// wanbond_aggregation_{engage,disengage}_threshold_fps — through the same Source→collector
// seam the FEC/resequencer series already use. This file certifies the operator-facing
// exposition end to end on the netns fixture:
//
//	(i)   a single-peer WEIGHTED edge exposes all four families; both threshold gauges equal
//	      engage/disengage_fraction * per_path_capacity_fps (within a small relative
//	      tolerance); wanbond_aggregation_engaged reads 0 at idle;
//	(ii)  under the ACTIVE-BACKUP policy NONE of the four families is present (its scheduler
//	      exposes no gate); and
//	(iii) on the multi-peer concentrator fixture (setupMultiPeerSched with a weighted block)
//	      the series carry the `peer` label — Exposition.PeerValue resolves each edge's gate.
//
// EXECUTION IS DEFERRED (the G2/T97 pattern): the netns tier needs the privileged fixture
// + CAP_NET_ADMIN and is NOT run in the unit environment; this file must COMPILE and vet
// under -tags e2e and is executed on the o3.7mind.io (aarch64) + llm-ubuntu-0 (amd64) hosts.
// The label/threshold/absence LOGIC is additionally covered by the metrics- and
// device-package unit tests (internal/metrics, internal/device).
const (
	// aggMetricsListen is this file's /metrics endpoint, on a port no other e2e file binds
	// (see the metrics-port registry in netns.go — 9108 is the next unused port).
	aggMetricsListen = "127.0.0.1:9108"
	aggMetricsURL    = "http://" + aggMetricsListen + "/metrics"

	// aggPerPathCapacityFPS sizes the weighted aggregation gate. 700 is guard-consistent
	// with the default engage_fraction (0.9*700 = 630 fps sits at/below the ~666.7 fps
	// 8mbit-implied ceiling — the same sizing weighted_capacity_warn_test.go uses), so this
	// file never trips the T142 hard-fail guard; with NO link_bandwidth declared it is a
	// pure T144-WARN start.
	aggPerPathCapacityFPS = 700.0

	// The config defaults the collector's threshold gauges are computed from: the daemon
	// applies engage_fraction = 0.9 and disengage_fraction = 0.5 when left unset
	// (internal/config defaultEngageFraction/defaultDisengageFraction), so the expected
	// gauge values are these fractions * aggPerPathCapacityFPS.
	aggEngageFraction    = 0.9
	aggDisengageFraction = 0.5

	// aggMetricsWaitTimeout bounds how long the edge's /metrics endpoint may take to come up
	// (TUN creation, engine bring-up, THEN the metrics listener — see internal/device.up).
	aggMetricsWaitTimeout = 15 * time.Second

	// aggRelTol is the relative tolerance for the threshold-gauge equality: the gauge is a
	// direct float64 pass-through of fraction*capacity, so any nonzero slack only guards
	// against text-exposition round-trip formatting, not a real computation.
	aggRelTol = 1e-6
)

// aggSeriesNames is the full set of four aggregation families, for present/absent assertions.
var aggSeriesNames = []string{
	metrics.MetricAggregationEngaged,
	metrics.MetricOfferedLoadFPS,
	metrics.MetricAggregationEngageThreshold,
	metrics.MetricAggregationDisengageThreshold,
}

// setupAggEdge starts a SINGLE edge daemon over the two DefaultPaths uplinks with the
// /metrics endpoint on aggMetricsListen and the given [scheduler] block spliced in verbatim
// ("" = default active-backup). Like setupT144WeightedEdge it needs no concentrator — the
// scheduler is bound and /metrics comes up at Open regardless of whether the (deliberately
// unreachable) peer ever handshakes, which is exactly the idle state acceptance (i)/(ii)
// assert on. [log] level is "info" so the T144 capacity WARN is not suppressed.
func setupAggEdge(t *testing.T, top *Topology, bin, schedulerBlock string) *proc {
	t.Helper()
	edgePriv, _ := genKey(t)
	_, concPub := genKey(t)
	psk := randKey(t)

	var edgePaths strings.Builder
	for _, p := range DefaultPaths {
		fmt.Fprintf(&edgePaths, "[[paths]]\nname = %q\nsource_addr = %q\n\n", p.name, p.edgeIP)
	}

	dir := t.TempDir()
	edgeCfg := writeConfig(t, filepath.Join(dir, "edge.toml"), fmt.Sprintf(`role = "edge"
psk = "%s"

%s%s[metrics]
listen = "%s"

[wireguard]
private_key = "%s"

[[wireguard.peers]]
public_key = "%s"
endpoint = "192.0.2.53:51820"
allowed_ips = ["10.10.0.2/32"]

[log]
level = "info"
`, psk, edgePaths.String(), schedulerBlock, aggMetricsListen, edgePriv, concPub))

	return top.startProc(t, "edge", bin, "--config", edgeCfg)
}

// aggWeightedSchedBlock is the weighted [scheduler] block sized by aggPerPathCapacityFPS,
// with engage/disengage fractions left at their defaults (so the threshold gauges are the
// aggEngageFraction/aggDisengageFraction * capacity this file asserts).
func aggWeightedSchedBlock() string {
	return fmt.Sprintf("[scheduler]\npolicy = \"weighted\"\nper_path_capacity_fps = %.1f\n\n", aggPerPathCapacityFPS)
}

// relClose reports whether got is within aggRelTol relative error of want (want != 0).
func relClose(got, want float64) bool {
	return math.Abs(got-want) <= aggRelTol*math.Abs(want)
}

// TestAggregationMetricsSinglePeerWeighted is T146 acceptance (i): a single-peer weighted
// edge exposes all four aggregation families with NO `peer` label; the two threshold gauges
// equal engage/disengage_fraction*per_path_capacity_fps within aggRelTol; and
// wanbond_aggregation_engaged reads 0 while idle.
func TestAggregationMetricsSinglePeerWeighted(t *testing.T) {
	bin := buildWanbond(t)
	top := Setup(t)

	edge := setupAggEdge(t, top, bin, aggWeightedSchedBlock())
	if !top.waitLink(tunDev, false, 5*time.Second) {
		t.Fatalf("edge %s never appeared\n%s", tunDev, edge.log())
	}

	exp := waitT144MetricsReady(t, aggMetricsURL, aggMetricsWaitTimeout)

	for _, name := range aggSeriesNames {
		if !exp.Has(name) {
			t.Fatalf("weighted single-peer edge is missing aggregation family %s\n%s", name, edge.log())
		}
	}

	if v, ok := exp.Value(metrics.MetricAggregationEngaged); !ok || v != 0 {
		t.Errorf("%s = %v (present=%v), want 0 (gate collapsed at idle)", metrics.MetricAggregationEngaged, v, ok)
	}
	if _, ok := exp.Value(metrics.MetricOfferedLoadFPS); !ok {
		t.Errorf("%s absent, want present (single-peer, unlabeled)", metrics.MetricOfferedLoadFPS)
	}

	wantEngage := aggEngageFraction * aggPerPathCapacityFPS       // 630
	wantDisengage := aggDisengageFraction * aggPerPathCapacityFPS // 350
	if v, ok := exp.Value(metrics.MetricAggregationEngageThreshold); !ok || !relClose(v, wantEngage) {
		t.Errorf("%s = %v (present=%v), want ~%v (engage_fraction*per_path_capacity_fps)",
			metrics.MetricAggregationEngageThreshold, v, ok, wantEngage)
	}
	if v, ok := exp.Value(metrics.MetricAggregationDisengageThreshold); !ok || !relClose(v, wantDisengage) {
		t.Errorf("%s = %v (present=%v), want ~%v (disengage_fraction*per_path_capacity_fps)",
			metrics.MetricAggregationDisengageThreshold, v, ok, wantDisengage)
	}
	t.Logf("T146 (i): four families present; engage=%.1f disengage=%.1f; engaged=0 at idle", wantEngage, wantDisengage)
}

// TestAggregationMetricsAbsentActiveBackup is T146 acceptance (ii): under the default
// active-backup policy (no [scheduler] block) the scheduler exposes no aggregation gate, so
// NONE of the four families is present.
func TestAggregationMetricsAbsentActiveBackup(t *testing.T) {
	bin := buildWanbond(t)
	top := Setup(t)

	edge := setupAggEdge(t, top, bin, "")
	if !top.waitLink(tunDev, false, 5*time.Second) {
		t.Fatalf("edge %s never appeared\n%s", tunDev, edge.log())
	}

	exp := waitT144MetricsReady(t, aggMetricsURL, aggMetricsWaitTimeout)
	for _, name := range aggSeriesNames {
		if exp.Has(name) {
			t.Errorf("active-backup edge unexpectedly exposes aggregation family %s, want absent entirely\n%s", name, edge.log())
		}
	}
	t.Logf("T146 (ii): active-backup exposes none of the four aggregation families")
}

// TestAggregationMetricsMultiPeerWeighted is T146 acceptance (iii): on the multi-peer
// concentrator fixture under the weighted policy, each bound peer's aggregation gate surfaces
// with the peer's `peer` label — Exposition.PeerValue resolves the series for BOTH edges
// (edge-alpha and edge-beta, per the D58 primary-peer naming). Both threshold gauges carry
// engage/disengage_fraction*per_path_capacity_fps under each peer label.
func TestAggregationMetricsMultiPeerWeighted(t *testing.T) {
	// The fixture's processes and interfaces are held alive by t.Cleanup closures the setup
	// registers, so the returned handle is not needed for these scrape-only assertions.
	setupMultiPeerSched(t, aggWeightedSchedBlock())

	// The concentrator runs in the base namespace, so its /metrics is directly scrapeable.
	// Both peers are bound at concentrator startup (config-static multi-peer), so the
	// aggregation series appear once /metrics is up — no live tunnel is required.
	exp := waitT144MetricsReady(t, mpMetricsURL, aggMetricsWaitTimeout)

	wantEngage := aggEngageFraction * aggPerPathCapacityFPS
	wantDisengage := aggDisengageFraction * aggPerPathCapacityFPS
	for _, peer := range []string{mpPeerALabel, mpPeerBLabel} {
		if v, ok := exp.PeerValue(metrics.MetricAggregationEngaged, peer); !ok || v != 0 {
			t.Errorf("%s{peer=%q} = %v (present=%v), want 0 (idle)", metrics.MetricAggregationEngaged, peer, v, ok)
		}
		if _, ok := exp.PeerValue(metrics.MetricOfferedLoadFPS, peer); !ok {
			t.Errorf("%s{peer=%q} missing", metrics.MetricOfferedLoadFPS, peer)
		}
		if v, ok := exp.PeerValue(metrics.MetricAggregationEngageThreshold, peer); !ok || !relClose(v, wantEngage) {
			t.Errorf("%s{peer=%q} = %v (present=%v), want ~%v", metrics.MetricAggregationEngageThreshold, peer, v, ok, wantEngage)
		}
		if v, ok := exp.PeerValue(metrics.MetricAggregationDisengageThreshold, peer); !ok || !relClose(v, wantDisengage) {
			t.Errorf("%s{peer=%q} = %v (present=%v), want ~%v", metrics.MetricAggregationDisengageThreshold, peer, v, ok, wantDisengage)
		}
	}
	// Guard against a single-peer regression collapsing the label: the unlabeled series must
	// NOT be present on a multi-peer scrape (the T94 rule attaches the peer label to all).
	if _, ok := exp.Value(metrics.MetricAggregationEngaged); ok {
		t.Errorf("%s has an UNLABELED sample on a multi-peer concentrator, want every sample peer-labelled", metrics.MetricAggregationEngaged)
	}
	t.Logf("T146 (iii): both %q and %q resolve all four aggregation series via PeerValue", mpPeerALabel, mpPeerBLabel)
}
