//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/metrics"
)

// These counter-based regressions replace T297's intentionally RED clustered-UDP
// policer fixture. The netns tier remains CPU/PPS-bound, so it asserts byte
// conservation, configured bounds, and path selection rather than an absolute
// application throughput or RTT product claim.

const (
	pacingCounterMetricsListen = "127.0.0.1:9114"
	pacingCounterMetricsURL    = "http://" + pacingCounterMetricsListen + "/metrics"
	pacingCounterLoadSeconds   = 10
	pacingCounterFailoverSecs  = 3
	pacingCounterLinkRTT       = "5ms"
)

var pacingCounterPaths = []pathSpec{
	{name: "starlink", edgeIP: "10.100.1.1", concIP: "10.100.1.2", edgeVeth: "wbAe", concVeth: "wbAc", delayMs: 5, rateMbit: 8},
	{name: "cellular", edgeIP: "10.100.2.1", concIP: "10.100.2.2", edgeVeth: "wbBe", concVeth: "wbBc", delayMs: 8, rateMbit: 8},
}

func TestPacingTCPByteEnvelope(t *testing.T) {
	bin := buildWanbond(t)
	tests := []struct {
		name     string
		rateMbit int
	}{
		{name: "off", rateMbit: 0},
		{name: "8Mbit", rateMbit: 8},
		{name: "5Mbit", rateMbit: 5},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			top := SetupWithPaths(t, pacingCounterPaths)
			edge, conc := setupPacingCounterTunnel(t, top, bin, test.rateMbit)
			if !top.pingUntil(concInner, 15*time.Second) {
				t.Fatalf("tunnel never came up\n--- edge ---\n%s\n--- concentrator ---\n%s", edge.log(), conc.log())
			}

			before := fetchPacingCounterMetrics(t)
			if test.rateMbit == 0 {
				assertPacingCounterShaperAbsent(t, before)
				beforeTx := pacingCounterPathValue(t, before, metrics.MetricTxBytes, pacingCounterPaths[0].name)
				if mbps := top.iperf3Mbps(t, concInner, pacingCounterLoadSeconds); mbps <= 0 {
					t.Fatalf("unpaced TCP carried no application bytes: %.3f Mbit/s", mbps)
				}
				after := fetchPacingCounterMetrics(t)
				afterTx := pacingCounterPathValue(t, after, metrics.MetricTxBytes, pacingCounterPaths[0].name)
				if afterTx <= beforeTx {
					t.Fatalf("unpaced primary path tx delta = %.0f, want positive", afterTx-beforeTx)
				}
				assertPacingCounterShaperAbsent(t, after)
				assertNoLegacyPacerShedding(t, edge)
				return
			}

			before = waitPathShaperDrained(t, pacingCounterMetricsURL, pacingCounterPaths[0].name)
			sampler := StartMetricsSampler(t, pacingCounterMetricsURL, 100*time.Millisecond)
			start := time.Now()
			if mbps := top.iperf3Mbps(t, concInner, pacingCounterLoadSeconds); mbps <= 0 {
				t.Fatalf("paced TCP carried no application bytes: %.3f Mbit/s", mbps)
			}
			elapsed := time.Since(start)
			afterLoad := fetchPacingCounterMetrics(t)
			sampler.Stop()

			primary := pacingCounterPaths[0].name
			drained := waitPathShaperDrained(t, pacingCounterMetricsURL, primary)
			assertPacingCounterSamples(t, sampler.Samples(), primary)
			assertPacingCounterLoad(t, before, afterLoad, drained, primary, elapsed)
			assertPacingCounterZeroFailureDeltas(t, before, drained, pacingCounterPaths)

			top.BlockEgress(primary)
			t.Cleanup(func() { top.UnblockEgress(primary) })
			waitPacingCounterPathState(t, primary, 0)
			if !top.pingUntil(concInner, 8*time.Second) {
				t.Fatalf("TCP path did not survive active-backup failover\n--- edge ---\n%s\n--- concentrator ---\n%s", edge.log(), conc.log())
			}
			failoverBefore := fetchPacingCounterMetrics(t)
			if mbps := top.iperf3Mbps(t, concInner, pacingCounterFailoverSecs); mbps <= 0 {
				t.Fatalf("post-failover TCP carried no application bytes: %.3f Mbit/s", mbps)
			}
			failoverAfter := waitPathShaperDrained(t, pacingCounterMetricsURL, pacingCounterPaths[1].name)
			assertPacingCounterFailover(t, failoverBefore, failoverAfter)
			assertPacingCounterZeroFailureDeltas(t, drained, failoverAfter, pacingCounterPaths)
			assertNoLegacyPacerShedding(t, edge)
		})
	}
}

func setupPacingCounterTunnel(t *testing.T, top *Topology, bin string, rateMbit int) (edge, conc *proc) {
	t.Helper()
	edgePriv, edgePub := genKey(t)
	concPriv, concPub := genKey(t)
	psk := randKey(t)

	var edgePaths, concPaths strings.Builder
	for _, path := range pacingCounterPaths {
		link := ""
		if rateMbit > 0 {
			link = fmt.Sprintf("link_bandwidth = %q\nlink_rtt = %q\n", fmt.Sprintf("%dMbit", rateMbit), pacingCounterLinkRTT)
		}
		fmt.Fprintf(&edgePaths, "[[paths]]\nname = %q\nsource_addr = %q\ndest_addr = \"%s:%d\"\n%s\n",
			path.name, path.edgeIP, path.concIP, listenPort, link)
		fmt.Fprintf(&concPaths, "[[paths]]\nname = %q\nsource_addr = %q\n%s\n", path.name, path.concIP, link)
	}
	scheduler := "[scheduler]\npolicy = \"active-backup\"\n"
	if rateMbit > 0 {
		scheduler += "pacing_enabled = true\n"
	}
	scheduler += "\n"
	metricsBlock := fmt.Sprintf("[metrics]\nlisten = %q\n\n", pacingCounterMetricsListen)

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
level = "info"
`, psk, edgePaths.String(), scheduler, metricsBlock, edgePriv, concPub, pacingCounterPaths[0].concIP, listenPort, concInner))
	concCfg := writeConfig(t, filepath.Join(dir, "conc.toml"), fmt.Sprintf(`role = "concentrator"
psk = "%s"

%s%s%s[wireguard]
private_key = "%s"
listen_port = %d

[[wireguard.peers]]
public_key = "%s"
allowed_ips = ["%s/32"]

[log]
level = "info"
`, psk, concPaths.String(), scheduler, metricsBlock, concPriv, listenPort, edgePub, edgeInner))

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

func fetchPacingCounterMetrics(t *testing.T) metrics.Exposition {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return fetchMetrics(t, ctx, pacingCounterMetricsURL)
}

func pacingCounterPathValue(t testing.TB, exp metrics.Exposition, name, path string) float64 {
	t.Helper()
	value, ok := exp.PathValue(name, path)
	if !ok {
		t.Fatalf("missing %s{path=%q}", name, path)
	}
	return value
}

func assertPacingCounterShaperAbsent(t testing.TB, exp metrics.Exposition) {
	t.Helper()
	for _, path := range pacingCounterPaths {
		if value, ok := exp.PathValue(metrics.MetricShaperAcceptedBytes, path.name); ok {
			t.Fatalf("pacing-off exposed shaper series for %q: accepted_bytes=%v", path.name, value)
		}
	}
}

func assertPacingCounterSamples(t testing.TB, samples []MetricsSample, path string) {
	t.Helper()
	if len(samples) < 2 {
		t.Fatalf("metrics sampler retained %d samples, want at least 2", len(samples))
	}
	var maxData, maxControl, maxTotal, maxInFlight float64
	for index, sample := range samples {
		data := pacingCounterPathValue(t, sample.Exp, metrics.MetricShaperQueueDataBytes, path)
		control := pacingCounterPathValue(t, sample.Exp, metrics.MetricShaperQueueControlBytes, path)
		total := pacingCounterPathValue(t, sample.Exp, metrics.MetricShaperQueueBytes, path)
		inFlight := pacingCounterPathValue(t, sample.Exp, metrics.MetricShaperInFlightBytes, path)
		b := pacingCounterPathValue(t, sample.Exp, metrics.MetricShaperDataBudgetBytes, path)
		c := pacingCounterPathValue(t, sample.Exp, metrics.MetricShaperControlReserveBytes, path)
		q := pacingCounterPathValue(t, sample.Exp, metrics.MetricShaperQueueBudgetBytes, path)
		lmax := pacingCounterPathValue(t, sample.Exp, metrics.MetricShaperMaxDatagramBytes, path)
		if data > b || control > c || total > q || inFlight > lmax {
			t.Fatalf("sample %d at %s violates B/C/Q/Lmax: data=%v/%v control=%v/%v total=%v/%v in_flight=%v/%v",
				index, sample.At, data, b, control, c, total, q, inFlight, lmax)
		}
		maxData = math.Max(maxData, data)
		maxControl = math.Max(maxControl, control)
		maxTotal = math.Max(maxTotal, total)
		maxInFlight = math.Max(maxInFlight, inFlight)
	}
	t.Logf("sampled shaper maxima: DATA=%.0f CONTROL=%.0f total=%.0f in-flight=%.0f across %d scrapes",
		maxData, maxControl, maxTotal, maxInFlight, len(samples))
}

func assertPacingCounterLoad(
	t testing.TB,
	before metrics.Exposition,
	windowEnd metrics.Exposition,
	drained metrics.Exposition,
	path string,
	elapsed time.Duration,
) {
	t.Helper()
	drainedDelta := func(name string) float64 {
		return pacingCounterPathValue(t, drained, name, path) - pacingCounterPathValue(t, before, name, path)
	}
	acceptedBytes := drainedDelta(metrics.MetricShaperAcceptedBytes)
	emittedBytes := drainedDelta(metrics.MetricShaperEmittedBytes)
	acceptedDatagrams := drainedDelta(metrics.MetricShaperAcceptedDatagrams)
	emittedDatagrams := drainedDelta(metrics.MetricShaperEmittedDatagrams)
	txBytes := drainedDelta(metrics.MetricTxBytes)
	waits := drainedDelta(metrics.MetricShaperAdmissionWaits)
	if acceptedBytes <= 0 || emittedBytes <= 0 || acceptedDatagrams <= 0 || emittedDatagrams <= 0 || txBytes <= 0 {
		t.Fatalf("paced TCP deltas must all be positive: accepted/emitted bytes=%.0f/%.0f datagrams=%.0f/%.0f path_tx=%.0f",
			acceptedBytes, emittedBytes, acceptedDatagrams, emittedDatagrams, txBytes)
	}
	if waits <= 0 {
		t.Fatalf("paced TCP admission waits delta = %.0f, want positive backpressure", waits)
	}
	if acceptedBytes != emittedBytes || acceptedDatagrams != emittedDatagrams {
		t.Fatalf("drained paced TCP accounting: accepted/emitted bytes=%.0f/%.0f datagrams=%.0f/%.0f",
			acceptedBytes, emittedBytes, acceptedDatagrams, emittedDatagrams)
	}
	rate := pacingCounterPathValue(t, windowEnd, metrics.MetricShaperRateBytesPerSecond, path)
	lmax := pacingCounterPathValue(t, windowEnd, metrics.MetricShaperMaxDatagramBytes, path)
	envelope := rate * elapsed.Seconds()
	slack := math.Max(0.05*envelope, 2*lmax)
	windowTxBytes := pacingCounterPathValue(t, windowEnd, metrics.MetricTxBytes, path) -
		pacingCounterPathValue(t, before, metrics.MetricTxBytes, path)
	if windowTxBytes > envelope+slack {
		t.Fatalf("ten-second total-path byte envelope: tx=%.0f > R*T+max(5%%,2Lmax)=%.0f (R=%.0f T=%s slack=%.0f)",
			windowTxBytes, envelope+slack, rate, elapsed, slack)
	}
}

func assertPacingCounterZeroFailureDeltas(
	t testing.TB,
	before metrics.Exposition,
	after metrics.Exposition,
	paths []pathSpec,
) {
	t.Helper()
	names := []string{
		metrics.MetricProbeSendErrors,
		metrics.MetricShaperWriteErrors,
		metrics.MetricSocketWriteErrors,
		metrics.MetricShaperAdmissionCanceledDatagrams,
		metrics.MetricShaperAsyncWriteErrors,
		metrics.MetricShaperAsyncWriteErrorBytes,
		metrics.MetricShaperAsyncWriteEMSGSIZEErrors,
		metrics.MetricShaperAsyncWriteEMSGSIZEBytes,
	}
	for _, path := range paths {
		for _, name := range names {
			delta := pacingCounterPathValue(t, after, name, path.name) - pacingCounterPathValue(t, before, name, path.name)
			if delta != 0 {
				t.Fatalf("%s{path=%q} delta = %.0f, want zero under ordinary TCP/backpressure", name, path.name, delta)
			}
		}
	}
}

func waitPacingCounterPathState(t *testing.T, path string, want float64) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		exp := fetchPacingCounterMetrics(t)
		if got := pacingCounterPathValue(t, exp, metrics.MetricUp, path); got == want {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("path %q did not reach up=%v", path, want)
}

func assertPacingCounterFailover(t testing.TB, before, after metrics.Exposition) {
	t.Helper()
	primary := pacingCounterPaths[0].name
	backup := pacingCounterPaths[1].name
	delta := func(name, path string) float64 {
		return pacingCounterPathValue(t, after, name, path) - pacingCounterPathValue(t, before, name, path)
	}
	backupBytes := delta(metrics.MetricShaperEmittedBytes, backup)
	backupDatagrams := delta(metrics.MetricShaperEmittedDatagrams, backup)
	backupTx := delta(metrics.MetricTxBytes, backup)
	if backupBytes <= 0 || backupDatagrams <= 0 || backupTx <= 0 {
		t.Fatalf("failover backup deltas must be positive: emitted_bytes=%.0f datagrams=%.0f path_tx=%.0f",
			backupBytes, backupDatagrams, backupTx)
	}
	lmax := pacingCounterPathValue(t, after, metrics.MetricShaperMaxDatagramBytes, primary)
	primaryBytes := delta(metrics.MetricShaperEmittedBytes, primary)
	if limit := math.Max(0.05*backupBytes, 2*lmax); primaryBytes > limit {
		t.Fatalf("primary continued carrying bulk after failover: primary=%.0f backup=%.0f limit=%.0f", primaryBytes, backupBytes, limit)
	}
}

func assertNoLegacyPacerShedding(t testing.TB, process *proc) {
	t.Helper()
	for _, line := range ParseLogLines(process.log()) {
		if line.Msg == "scheduler pacer shedding" {
			t.Fatalf("exact-byte shaping emitted legacy loss-policer record: %+v", line)
		}
	}
}
