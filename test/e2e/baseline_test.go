//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// bloatPort is the iperf3 port for the under-load (bufferbloat) measurement,
// distinct from the default 5201 the clean-throughput run uses.
const bloatPort = 5202

// TestP0Baseline records the single-path P0 baseline the P0-findings doc's
// pacing/bufferbloat section (section 7) reports. For EACH emulated uplink it
// brings the pass-through tunnel up over THAT path, then measures three numbers:
//
//  1. idle RTT — round-trip latency with no load (underlay and through-tunnel);
//  2. saturated throughput — iperf3 TCP goodput through the tunnel;
//  3. RTT-under-load (bufferbloat) — RTT to the far inner IP sampled WHILE a
//     saturating iperf3 runs, exposing the standing-queue latency inflation.
//
// The delta (3) − (1) is the bufferbloat figure that drives the verdict on
// whether the bonding scheduler must pace egress / bound in-flight bytes.
//
// It cannot run in the sandbox (no /dev/net/tun); the orchestrator runs it on
// real hardware and substitutes the measured numbers into docs/p0-findings.md.
func TestP0Baseline(t *testing.T) {
	bin := buildWanbond(t)
	for _, p := range DefaultPaths {
		p := p // capture for the subtest closure
		t.Run(p.name, func(t *testing.T) {
			top := Setup(t)
			edge, conc := setupP0Tunnel(t, top, bin, p)

			// (1) idle RTT: underlay (netem delay only) and end-to-end through
			// the established tunnel, with no competing load.
			idleUnderlay := top.RTT(p.name, 10)
			idleTunnel := pingAvgMs(t, concInner, 10)

			// (2) saturated throughput through the tunnel (clean run).
			satMbps := top.iperf3Mbps(t, concInner, 4)
			if satMbps <= 0 {
				t.Fatalf("path %q: saturated throughput non-positive %.2f Mbit/s\n--- edge ---\n%s\n--- conc ---\n%s",
					p.name, satMbps, edge.log(), conc.log())
			}

			// (3) RTT-under-load: ping the far inner IP WHILE a saturating
			// iperf3 runs, so the standing queue's latency inflation is visible.
			loadMbps, loadedTunnel := top.rttUnderLoad(t, concInner, concInner, 8)

			bloat := loadedTunnel - idleTunnel
			t.Logf("P0 baseline [%s]: idle RTT underlay=%.1fms tunnel=%.1fms | "+
				"saturated throughput=%.1f Mbit/s | under-load RTT=%.1fms (load=%.1f Mbit/s) | "+
				"bufferbloat Δ=%.1fms",
				p.name, idleUnderlay, idleTunnel, satMbps, loadedTunnel, loadMbps, bloat)
		})
	}
}

// setupP0Tunnel brings the pass-through P0 tunnel up over the given path,
// parameterizing the bring-up pattern of TestP0PassThrough by path choice. It
// returns the running edge and concentrator processes for diagnostics.
func setupP0Tunnel(t *testing.T, top *Topology, bin string, p pathSpec) (edge, conc *proc) {
	t.Helper()

	edgePriv, edgePub := genKey(t)
	concPriv, concPub := genKey(t)
	psk := randKey(t) // outer-control PSK: required by config validation, unused by P0 pass-through

	dir := t.TempDir()
	edgeCfg := writeConfig(t, filepath.Join(dir, "edge.toml"), fmt.Sprintf(`role = "edge"
psk = "%s"

[[paths]]
name = "%s"
source_addr = "%s"

[wireguard]
private_key = "%s"

[[wireguard.peers]]
public_key = "%s"
endpoint = "%s:%d"
allowed_ips = ["%s/32"]

[log]
level = "error"
`, psk, p.name, p.edgeIP, edgePriv, concPub, p.concIP, listenPort, concInner))

	concCfg := writeConfig(t, filepath.Join(dir, "conc.toml"), fmt.Sprintf(`role = "concentrator"
psk = "%s"

[[paths]]
name = "%s"
source_addr = "%s"

[wireguard]
private_key = "%s"
listen_port = %d

[[wireguard.peers]]
public_key = "%s"
allowed_ips = ["%s/32"]

[log]
level = "error"
`, psk, p.name, p.concIP, concPriv, listenPort, edgePub, edgeInner))

	// Concentrator first (must be listening before the edge initiates), then edge.
	conc = top.startProc(t, "concentrator", "nsenter", "-t", strconv.Itoa(top.pid), "-n", bin, "--config", concCfg)
	edge = top.startProc(t, "edge", bin, "--config", edgeCfg)

	if !top.waitLink(tunDev, false, 5*time.Second) {
		t.Fatalf("path %q: edge %s never appeared\n%s", p.name, tunDev, edge.log())
	}
	if !top.waitLink(tunDev, true, 5*time.Second) {
		t.Fatalf("path %q: concentrator %s never appeared\n%s", p.name, tunDev, conc.log())
	}
	top.run("ip", "addr", "add", edgeInner+"/24", "dev", tunDev)
	top.run("ip", "link", "set", tunDev, "up")
	top.nsenter("ip", "addr", "add", concInner+"/24", "dev", tunDev)
	top.nsenter("ip", "link", "set", tunDev, "up")

	if !top.pingUntil(concInner, 15*time.Second) {
		t.Fatalf("path %q: tunnel never came up: %s unreachable through the tunnel\n--- edge ---\n%s\n--- concentrator ---\n%s",
			p.name, concInner, edge.log(), conc.log())
	}
	return edge, conc
}

// rttUnderLoad saturates the tunnel to serverIP with a background iperf3 TCP
// flow while sampling ping RTT to pingIP, returning the measured throughput
// (Mbit/s) and the average RTT observed under load (ms). The gap between this
// RTT and the idle RTT is the bufferbloat inflation.
func (top *Topology) rttUnderLoad(t *testing.T, serverIP, pingIP string, secs int) (mbps, loadedRTTms float64) {
	t.Helper()
	// Use a port distinct from the default (5201) the clean-throughput measurement
	// just used: rebinding 5201 immediately can hit the prior socket's TIME_WAIT
	// and leave the fresh one-shot server not listening (client -> "connection
	// refused"). A separate port sidesteps that entirely.
	port := strconv.Itoa(bloatPort)
	top.startProc(t, "iperf3-bloat-server", "nsenter", "-t", strconv.Itoa(top.pid), "-n", "iperf3", "-s", "-1", "-B", serverIP, "-p", port)
	top.waitIperfListen(t, bloatPort) // poll for LISTEN (never connect: server is one-shot)

	// Run the saturating client in the background; capture output/err only (no
	// t.Fatalf off the test goroutine — that is illegal in Go's testing).
	type clientResult struct {
		out []byte
		err error
	}
	ch := make(chan clientResult, 1)
	go func() {
		out, err := exec.Command("iperf3", "-c", serverIP, "-p", port, "-t", strconv.Itoa(secs), "-J").CombinedOutput()
		ch <- clientResult{out: out, err: err}
	}()

	// Let the queue build for ~1s, then sample RTT across most of the remaining
	// load window (0.5s spacing) so the standing-queue latency is captured.
	time.Sleep(1 * time.Second)
	samples := secs - 2
	if samples < 3 {
		samples = 3
	}
	pingOut := top.runOut("ping", "-c", strconv.Itoa(samples), "-i", "0.5", "-W", "2", pingIP)
	loadedRTTms = parsePingAvgMs(t, pingOut)

	r := <-ch
	if r.err != nil {
		t.Fatalf("iperf3 under load to %s: %v\n%s", serverIP, r.err, r.out)
	}
	mbps = parseIperfSentMbps(t, r.out)
	return mbps, loadedRTTms
}

// pingAvgMs pings target from the edge netns count times and returns the average
// round-trip time in milliseconds.
func pingAvgMs(t *testing.T, target string, count int) float64 {
	t.Helper()
	out, err := exec.Command("ping", "-c", strconv.Itoa(count), "-i", "0.2", "-W", "2", target).CombinedOutput()
	if err != nil {
		t.Fatalf("ping %s: %v\n%s", target, err, out)
	}
	return parsePingAvgMs(t, string(out))
}

// parsePingAvgMs extracts the avg field from a ping "rtt min/avg/max/mdev" line.
func parsePingAvgMs(t *testing.T, out string) float64 {
	t.Helper()
	idx := strings.Index(out, "min/avg/max")
	if idx < 0 {
		t.Fatalf("no rtt line in ping output:\n%s", out)
	}
	eq := strings.Index(out[idx:], "=")
	fields := strings.Fields(out[idx+eq+1:])
	nums := strings.Split(fields[0], "/")
	avg, err := strconv.ParseFloat(nums[1], 64)
	if err != nil {
		t.Fatalf("parse avg rtt %q: %v", nums[1], err)
	}
	return avg
}

// parseIperfSentMbps extracts the sender throughput (Mbit/s) from iperf3 -J JSON.
func parseIperfSentMbps(t *testing.T, out []byte) float64 {
	t.Helper()
	var r struct {
		End struct {
			SumSent struct {
				BitsPerSecond float64 `json:"bits_per_second"`
			} `json:"sum_sent"`
		} `json:"end"`
	}
	if err := json.Unmarshal(out, &r); err != nil {
		t.Fatalf("parse iperf3 json: %v\n%s", err, out)
	}
	return r.End.SumSent.BitsPerSecond / 1e6
}
