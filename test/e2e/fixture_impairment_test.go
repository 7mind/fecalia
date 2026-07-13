//go:build e2e

package e2e

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/bind"
	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/frame"
)

// Impairment self-test knobs. This self-test measures capMbit over the RAW veth
// links (no tunnel, see TestFixtureImpairment below), so the emulated LINK — not
// userspace-WG crypto — is trivially the bottleneck and a standing queue can form.
// Reused THROUGH the tunnel (T21/T23 pacing, T25/T29 FEC-recovery), a cap is only
// link-bound when it sits below the EXECUTING host's measured in-fixture tunnel
// ceiling, which is CPU/PPS-bound (both daemons plus the load generator share the
// host's cores), a lower bound that scales with core count and NOT a
// link-throughput spec: ~12–46 Mbit/s single-flow on a 1-vCPU aarch64 host
// (docs/p0-findings.md:216-225), ~13 Mbit/s single-path (up to ~47–87 Mbit/s FEC
// single-flow) on a 4-vCPU amd64 host. Sizing rule: cap < ceiling for single-path,
// 2×cap < ceiling for aggregation.
const (
	capMbit    = 50   // per-path bandwidth cap under test (Mbit/s)
	lossTarget = 10.0 // config-time uniform loss under test (percent)

	// Cap tolerance: netem-rate TCP goodput sits a little under the shaped rate
	// (header overhead) and jitters run-to-run; accept a generous band centred on
	// the cap. The point is link-bound ~= cap, NOT the tunnel's CPU/PPS-bound ceiling.
	capLoMbit = 35.0
	capHiMbit = 56.0

	// Loss tolerance: uniform netem loss over lossProbes ICMP echoes lands near
	// the configured percentage (binomial std-dev ~1.3% at 500 probes); accept a
	// band around it.
	lossLoPct = 5.0
	lossHiPct = 18.0

	// lossProbes is the number of ICMP echoes used to estimate path loss.
	lossProbes = 500

	// bdpLoadSecs is the sustained-load duration (seconds) for the BDP/bufferbloat
	// sub-test (c): long enough for netem's rate-limiting queue to fill and the
	// under-load RTT to stabilise, short enough to keep the suite fast.
	bdpLoadSecs = 8
)

// impairmentPaths is a bespoke two-path topology exercising the OPTIONAL knobs:
// "capped" carries a bandwidth cap (no loss); "lossy" carries config-time loss
// (no cap). Both use a small fixed delay so the raw-link measurements are stable.
// They reuse DefaultPaths' veth names/IPs — safe because each test owns its own
// topology and tears it down.
var impairmentPaths = []pathSpec{
	{name: "capped", edgeIP: "10.100.1.1", concIP: "10.100.1.2", edgeVeth: "wbAe", concVeth: "wbAc", delayMs: 5, jitterMs: 0, rateMbit: capMbit},
	{name: "lossy", edgeIP: "10.100.2.1", concIP: "10.100.2.2", edgeVeth: "wbBe", concVeth: "wbBc", delayMs: 5, jitterMs: 0, lossPct: lossTarget},
}

// TestFixtureImpairment demonstrates both new fixture knobs operationally, over
// the RAW veth links (no tunnel), so the measurements reflect the emulated link
// — not the userspace-WG crypto:
//
//	(a) bandwidth cap — iperf3 TCP through the capped path measures within a
//	    stated tolerance of the cap (link-bound, not CPU-bound);
//	(b) controlled loss — ICMP echoes through the lossy path measure a loss
//	    fraction in the expected band around the configured percentage;
//	(c) BDP — sustained load through the capped path builds a standing queue,
//	    and the achieved throughput + idle-vs-loaded RTT delta ground the
//	    per-path bandwidth-delay product (T52/G2/W2b) that SizePacingFromBDP
//	    consumes, instead of the synthetic defaultPerPathCapacityFPS default.
//
// The loss knob is measured with ping (not iperf3) on purpose: netem loss is on
// the edge egress, so a dropped echo-request directly reflects the configured
// drop rate, and ping has no TCP control channel to stall on the lossy link (the
// flake iperf3 UDP suffered — its control connection shared the lossy path).
func TestFixtureImpairment(t *testing.T) {
	top := SetupWithPaths(t, impairmentPaths)

	// (a) Bandwidth cap. The qdisc must carry the rate, and the shaped TCP
	// throughput must land near the cap — the raw veth (no tunnel) is the medium, so
	// the netem rate is the bottleneck regardless of the tunnel's CPU/PPS ceiling.
	capped := top.path("capped")
	if q := top.QdiscShow("capped"); !strings.Contains(q, "rate") {
		t.Errorf("capped path qdisc missing rate cap: %q", strings.TrimSpace(q))
	}
	mbps := top.iperf3Mbps(t, capped.concIP, 4)
	if mbps < capLoMbit || mbps > capHiMbit {
		t.Errorf("capped path throughput = %.1f Mbit/s, want cap ~%d Mbit/s (%.0f-%.0f tolerance, link-bound not CPU-bound)",
			mbps, capMbit, capLoMbit, capHiMbit)
	} else {
		t.Logf("capped path: %.1f Mbit/s (cap %d, link-bound)", mbps, capMbit)
	}

	// (b) Controlled loss. The qdisc must carry the loss, and measured ICMP loss
	// must fall in the band around the configured percentage.
	if q := top.QdiscShow("lossy"); !strings.Contains(q, "loss") {
		t.Errorf("lossy path qdisc missing loss knob: %q", strings.TrimSpace(q))
	}
	loss := top.pingLossPct(t, "lossy", lossProbes)
	if loss < lossLoPct || loss > lossHiPct {
		t.Errorf("lossy path measured ICMP loss = %.1f%%, want ~%.0f%% (%.0f-%.0f band)",
			loss, lossTarget, lossLoPct, lossHiPct)
	} else {
		t.Logf("lossy path: measured ICMP loss %.1f%% (configured %.0f%%)", loss, lossTarget)
	}

	// (c) BDP + standing-queue delay, measured on the same capped raw link as (a),
	// with pacing out of the picture entirely (no tunnel is up here — T52 grounds
	// the *inputs* SizePacingFromBDP consumes; T53/T61 wire and validate pacing
	// itself). Deterministic and report-only: no assertion on the measured
	// numbers, only that the measurement pipeline (load, ping, sizing) runs clean.
	t.Run("bdp", func(t *testing.T) {
		top.measureBDP(t, "capped")
	})
}

// measureBDP grounds SizePacingFromBDP's inputs in fixture numbers instead of the
// synthetic defaultPerPathCapacityFPS default (T52/G2/W2b): it measures idle RTT
// on the named path, then saturates it with a sustained TCP flow (rttUnderLoad)
// to build a standing queue behind netem's rate limiter, sampling RTT under that
// load. The bufferbloat delta (loaded − idle RTT) and the achieved throughput
// feed SizePacingFromBDP exactly as it will be driven from an operator-declared
// per-link bandwidth (BDP = bandwidth × RTT, capacity in frames/s via the
// average on-wire outer-frame size). Numbers are logged, not asserted — the
// point is that the derivation runs deterministically, not any specific value.
func (top *Topology) measureBDP(t *testing.T, name string) {
	t.Helper()
	p := top.path(name)

	idleRTTms := top.RTT(name, 10)
	loadMbps, loadedRTTms := top.rttUnderLoad(t, p.concIP, p.concIP, bdpLoadSecs)
	if loadMbps <= 0 {
		t.Fatalf("path %q: BDP-load throughput non-positive %.2f Mbit/s", name, loadMbps)
	}
	bufferbloatMs := loadedRTTms - idleRTTms

	// avgWireFrameBytes: the full-MTU datagram plus frame.DataOverhead, the
	// "conservative choice" SizePacingFromBDP's own doc comment calls for — see
	// internal/config/config.go.
	avgWireFrameBytes := float64(bind.DefaultPathMTU + frame.DataOverhead)
	rtt := time.Duration(idleRTTms * float64(time.Millisecond))
	sizing, err := config.SizePacingFromBDP(loadMbps*1e6, rtt, avgWireFrameBytes)
	if err != nil {
		t.Fatalf("path %q: SizePacingFromBDP(%.1f Mbit/s, %s, %.0fB): %v", name, loadMbps, rtt, avgWireFrameBytes, err)
	}
	bdpBytes := sizing.BurstFrames * avgWireFrameBytes

	t.Logf("path %q BDP: idle RTT=%.1fms loaded RTT=%.1fms (bufferbloat Δ=%.1fms) | "+
		"achieved throughput=%.1f Mbit/s | BDP=%.0f bytes (%.1f frames @ %.0fB/frame) | "+
		"SizePacingFromBDP -> capacityFPS=%.1f burstFrames=%.1f",
		name, idleRTTms, loadedRTTms, bufferbloatMs, loadMbps, bdpBytes, sizing.BurstFrames, avgWireFrameBytes,
		sizing.CapacityFPS, sizing.BurstFrames)
}

// pingLossPct sends count ICMP echoes from the edge netns to the named path's
// concentrator end and returns the packet-loss percentage ping reports. Because
// the loss netem is on the edge egress, this measures exactly the configured
// drop rate, free of any TCP control-channel confound.
func (top *Topology) pingLossPct(t *testing.T, name string, count int) float64 {
	t.Helper()
	p := top.path(name)
	// -i 0.01 needs root (the e2e suite runs under sudo); -W 1 bounds each wait.
	out := top.runOut("ping", "-c", strconv.Itoa(count), "-i", "0.01", "-W", "1", p.concIP)
	// "<n> packets transmitted, <m> received, <x>% packet loss, time ..."
	idx := strings.Index(out, "% packet loss")
	if idx < 0 {
		t.Fatalf("path %q: no packet-loss line in ping output:\n%s", name, out)
	}
	start := strings.LastIndex(out[:idx], " ") + 1
	pct, err := strconv.ParseFloat(strings.TrimSpace(out[start:idx]), 64)
	if err != nil {
		t.Fatalf("path %q: parse ping loss %q: %v", name, out[start:idx], err)
	}
	return pct
}
