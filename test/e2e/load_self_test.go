//go:build e2e

package e2e

import (
	"fmt"
	"math"
	"net"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/metrics"
)

// TestLoadDriverSelfTest is the T141 harness self-test (Q55): it grounds
// DriveUDPLoad, MetricsSampler, and the ParseLogLines/AwaitLogLine log capturer
// operationally, against a real weighted-policy tunnel over a rate-capped path —
// the shared dependency the observability and probe-protection e2e tasks (Q55)
// build on. Two subtests:
//
//	(a) sustained-load-tx-bytes-and-metrics-sampling — drives an offered load
//	    ABOVE the scheduler's engage_fraction*per_path_capacity_fps threshold but
//	    AT OR BELOW its pacing capacity for >= 5s, so nothing is shed, and checks
//	    the achieved rate lands within loadSelfTestToleranceFraction of the
//	    target BOTH from the driver's own send accounting and from the daemon's
//	    OWN wanbond_path_tx_bytes_total counter (sampled repeatedly, not just
//	    before/after, by a MetricsSampler) — proving the driver is genuinely
//	    rate-calibrated, not merely fire-and-forget.
//	(b) log-capture-under-deliberate-overload — drives an offered load far ABOVE
//	    the pacing capacity and asserts the log capturer observes the coalesced
//	    "scheduler pacer shedding" record WHILE that load runs, plus confirms it
//	    already captured the "path liveness transition" record from bring-up.
const (
	// loadSelfTestRateMbit is the netem egress cap on the emulated path (~5 Mbit,
	// per the T141 acceptance) — the "rate-capped path" the daemon's own pacing
	// capacity (loadSelfTestCapacityFPS below) is deliberately set BELOW, so the
	// daemon's token bucket — not the netem link — is what governs whether a
	// frame is shed in subtest (b).
	loadSelfTestRateMbit = 5

	// loadSelfTestCapacityFPS is the weighted scheduler's per_path_capacity_fps
	// under test: both the aggregation-gate denominator and (with pacing_enabled)
	// the per-path token-bucket refill rate (internal/sched/weighted.go). At the
	// default EngageFraction=0.9 the engage threshold is 0.9*400=360 fps.
	loadSelfTestCapacityFPS = 400.0

	// loadSelfTestSustainedFPS is subtest (a)'s target offered load: above the
	// 360-fps engage threshold, but at/below the 400-fps pacing capacity, so a
	// steady sender is not shed and the sent/tx-bytes comparison is well-defined.
	loadSelfTestSustainedFPS = 380.0

	// loadSelfTestOverloadFPS is subtest (b)'s deliberately-excessive target,
	// several times loadSelfTestCapacityFPS plus the default 64-frame pacing
	// burst, so shedding is certain within one shedLogInterval (1s).
	loadSelfTestOverloadFPS = 3000.0

	// loadSelfTestPayloadBytes is each UDP datagram's payload size — comfortably
	// under the inner tunnel MTU (~1412B: bind.InnerMTU(1500, false)), so one
	// DriveUDPLoad send maps to exactly one wanbond DATA frame (no IP
	// fragmentation splitting it into two). Sized large enough (1200B) that the
	// fixed per-frame wire overhead (own IP/UDP header + wanbond DataOverhead +
	// WireGuard transport overhead, ~88B measured) is a small fraction (~7%) of
	// the frame, leaving headroom under loadSelfTestToleranceFraction for the
	// sent-bytes-vs-wire-tx_bytes comparison (a small fixed payload like 512B
	// pushed that overhead to ~18%, uncomfortably close to the 20% band).
	loadSelfTestPayloadBytes = 1200

	// loadSelfTestSustainedDuration is subtest (a)'s load duration: the >= 5s the
	// T141 acceptance requires, plus margin.
	loadSelfTestSustainedDuration = 6 * time.Second
	// loadSelfTestOverloadDuration is subtest (b)'s load duration: several
	// shedLogInterval (1s) periods, so the coalesced shed record has time to fire.
	loadSelfTestOverloadDuration = 4 * time.Second

	// loadSelfTestSampleInterval is the MetricsSampler's poll cadence during
	// subtest (a)'s load window.
	loadSelfTestSampleInterval = 150 * time.Millisecond

	// loadSelfTestToleranceFraction bounds how far the achieved rate (both the
	// driver's own send accounting and the daemon's wire tx_bytes delta) may
	// stray from the target — the T141 acceptance's +/-20% band.
	loadSelfTestToleranceFraction = 0.20

	// loadSelfTestSettle lets the weighted scheduler's offered-load meter and
	// pacing buckets reach steady state after tunnel bring-up before any load is
	// offered (mirrors pacingSettle's rationale).
	loadSelfTestSettle = 2 * time.Second

	// loadSelfTestSinkPort is the UDP load sink's port on the concentrator inner
	// (tunnel) address — distinct from every WireGuard/iperf3/metrics port this
	// file's tunnel also binds.
	loadSelfTestSinkPort = 6001

	loadSelfTestMetricsListen = "127.0.0.1:9105"
	loadSelfTestMetricsURL    = "http://" + loadSelfTestMetricsListen + "/metrics"
)

// loadSelfTestPath is the single emulated uplink this file's test brings the
// tunnel up over: a small fixed delay plus the loadSelfTestRateMbit cap.
var loadSelfTestPath = pathSpec{
	name:     "capped",
	edgeIP:   "10.100.1.1",
	concIP:   "10.100.1.2",
	edgeVeth: "wbAe",
	concVeth: "wbAc",
	delayMs:  5,
	jitterMs: 0,
	rateMbit: loadSelfTestRateMbit,
}

func TestLoadDriverSelfTest(t *testing.T) {
	bin := buildWanbond(t)
	top := SetupWithPaths(t, []pathSpec{loadSelfTestPath})

	edge, conc := setupLoadSelfTestTunnel(t, top, bin)
	if !top.pingUntil(concInner, 15*time.Second) {
		t.Fatalf("load self-test: tunnel never came up\n--- edge ---\n%s\n--- conc ---\n%s", edge.log(), conc.log())
	}
	time.Sleep(loadSelfTestSettle)

	sinkAddr := net.JoinHostPort(concInner, strconv.Itoa(loadSelfTestSinkPort))

	t.Run("sustained-load-tx-bytes-and-metrics-sampling", func(t *testing.T) {
		sampler := StartMetricsSampler(t, loadSelfTestMetricsURL, loadSelfTestSampleInterval)

		result := top.DriveUDPLoad(t, edgeInner, sinkAddr, UDPLoadSpec{
			TargetFPS:    loadSelfTestSustainedFPS,
			PayloadBytes: loadSelfTestPayloadBytes,
			Duration:     loadSelfTestSustainedDuration,
		})
		sampler.Stop()

		if result.Elapsed < loadSelfTestSustainedDuration {
			t.Fatalf("driver ran %s, want >= %s (the T141 sustained-load floor)", result.Elapsed, loadSelfTestSustainedDuration)
		}

		achievedOff := math.Abs(result.AchievedFPS-loadSelfTestSustainedFPS) / loadSelfTestSustainedFPS
		t.Logf("driver: sent %d frames (%d bytes) over %s -> achieved %.1f fps (target %.1f fps, off %.1f%%)",
			result.SentFrames, result.SentBytes, result.Elapsed, result.AchievedFPS, loadSelfTestSustainedFPS, achievedOff*100)
		if achievedOff > loadSelfTestToleranceFraction {
			t.Fatalf("driver achieved %.1f fps, want target %.1f fps within +/-%.0f%% (off by %.1f%%)",
				result.AchievedFPS, loadSelfTestSustainedFPS, loadSelfTestToleranceFraction*100, achievedOff*100)
		}

		samples := sampler.Samples()
		if len(samples) < 1 {
			t.Fatalf("metrics sampler retained 0 gauge samples across the %s load window, want >= 1", loadSelfTestSustainedDuration)
		}
		if _, ok := samples[0].Exp.PathValue(metrics.MetricUp, loadSelfTestPath.name); !ok {
			t.Fatalf("first sampled scrape missing gauge series %s{path=%q}", metrics.MetricUp, loadSelfTestPath.name)
		}
		t.Logf("metrics sampler retained %d gauge samples during the load window", len(samples))

		txDelta := sampler.PathValueDelta(t, metrics.MetricTxBytes, loadSelfTestPath.name)
		wantBytes := float64(result.SentFrames) * float64(loadSelfTestPayloadBytes)
		txOff := math.Abs(txDelta-wantBytes) / wantBytes
		t.Logf("wire tx_bytes delta = %.0f (want ~%.0f from %d sent frames x %dB payload, off %.1f%%)",
			txDelta, wantBytes, result.SentFrames, loadSelfTestPayloadBytes, txOff*100)
		if txOff > loadSelfTestToleranceFraction {
			t.Fatalf("wanbond_path_tx_bytes_total delta = %.0f bytes, want within +/-%.0f%% of the %d sent frames' %.0f payload bytes (off by %.1f%%)\n--- edge ---\n%s",
				txDelta, loadSelfTestToleranceFraction*100, result.SentFrames, wantBytes, txOff*100, edge.log())
		}
	})

	t.Run("log-capture-under-deliberate-overload", func(t *testing.T) {
		upSeen := false
		for _, l := range ParseLogLines(edge.log()) {
			if l.Msg == "path liveness transition" {
				upSeen = true
				break
			}
		}
		if !upSeen {
			t.Fatalf("expected >= 1 %q record captured from tunnel bring-up, found none\n%s", "path liveness transition", edge.log())
		}

		top.DriveUDPLoad(t, edgeInner, sinkAddr, UDPLoadSpec{
			TargetFPS:    loadSelfTestOverloadFPS,
			PayloadBytes: loadSelfTestPayloadBytes,
			Duration:     loadSelfTestOverloadDuration,
		})

		line, ok := AwaitLogLine(t, edge, "scheduler pacer shedding", 5*time.Second)
		if !ok {
			t.Fatalf("expected a %q record within 5s of driving %.0f fps (well above the %.0f-fps pacing capacity) but none appeared\n%s",
				"scheduler pacer shedding", loadSelfTestOverloadFPS, loadSelfTestCapacityFPS, edge.log())
		}
		shed, _ := line.FieldFloat("shed_frames")
		t.Logf("log capturer observed %q (shed_frames=%.0f) under deliberate overload (%.0f fps offered vs %.0f fps pacing capacity)",
			line.Msg, shed, loadSelfTestOverloadFPS, loadSelfTestCapacityFPS)
	})
}

// setupLoadSelfTestTunnel brings up the edge+concentrator tunnel over
// loadSelfTestPath with the weighted scheduler (per_path_capacity_fps set
// directly, pacing_enabled), /metrics, and info-level structured logging enabled
// on both ends — info level (unlike most e2e tests' "error") is what makes the
// "path liveness transition" and "scheduler pacer shedding" records observable to
// the log capturer.
func setupLoadSelfTestTunnel(t *testing.T, top *Topology, bin string) (edge, conc *proc) {
	t.Helper()

	edgePriv, edgePub := genKey(t)
	concPriv, concPub := genKey(t)
	psk := randKey(t)
	p := loadSelfTestPath

	schedBlock := fmt.Sprintf("[scheduler]\npolicy = \"weighted\"\nper_path_capacity_fps = %.1f\npacing_enabled = true\n\n", loadSelfTestCapacityFPS)
	metricsBlock := fmt.Sprintf("[metrics]\nlisten = %q\n\n", loadSelfTestMetricsListen)

	dir := t.TempDir()
	edgeCfg := writeConfig(t, filepath.Join(dir, "edge.toml"), fmt.Sprintf(`role = "edge"
psk = "%s"

[[paths]]
name = %q
source_addr = "%s"
dest_addr = "%s:%d"

%s%s[wireguard]
private_key = "%s"

[[wireguard.peers]]
public_key = "%s"
endpoint = "%s:%d"
allowed_ips = ["%s/32"]

[log]
level = "info"
`, psk, p.name, p.edgeIP, p.concIP, listenPort, schedBlock, metricsBlock, edgePriv, concPub, p.concIP, listenPort, concInner))

	concCfg := writeConfig(t, filepath.Join(dir, "conc.toml"), fmt.Sprintf(`role = "concentrator"
psk = "%s"

[[paths]]
name = %q
source_addr = "%s"

%s%s[wireguard]
private_key = "%s"
listen_port = %d

[[wireguard.peers]]
public_key = "%s"
allowed_ips = ["%s/32"]

[log]
level = "info"
`, psk, p.name, p.concIP, schedBlock, metricsBlock, concPriv, listenPort, edgePub, edgeInner))

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
