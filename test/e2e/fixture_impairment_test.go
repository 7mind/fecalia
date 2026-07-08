//go:build e2e

package e2e

import (
	"strconv"
	"strings"
	"testing"
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
//	    fraction in the expected band around the configured percentage.
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
