//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// FEC single-flow TCP collapse baseline parameters.
//
// The cap sits well below the CPU-bound tunnel throughput (P0 measured ~150-170
// Mbit/s of userspace-WG crypto on a 1-vCPU host), so the LINK — not the crypto
// — is the bottleneck; a fixed one-way delay then makes the loss×RTT product
// bite. The Mathis relation bounds single-flow TCP goodput at MSS/(RTT·sqrt(p)),
// so at a FIXED cap and RTT, goodput falls roughly with 1/sqrt(loss): this
// baseline quantifies exactly that collapse, which FEC recovery (T25/T29) is
// later measured against.
const (
	fecCapMbit   = 50 // per-path bandwidth cap (netem rate, Mbit/s)
	fecDelayMs   = 25 // one-way netem delay; RTT ~= 2× ~= 50ms — makes loss bite
	fecIperfSecs = 8  // single-flow TCP measurement window per loss point

	// fecCongestionControl pins the sender-side TCP congestion-control algorithm.
	// The collapse gate and the T25/T29 reference assume a LOSS-based CC (CUBIC):
	// a delay/BBR-based CC does not react to netem drops the same way, so on a
	// BBR-default host the single-flow collapse would not manifest, the gate would
	// fail spuriously, and the baseline would be invalid as the pre-FEC reference.
	// Linux iperf3 sets TCP_CONGESTION on the client socket via --congestion.
	fecCongestionControl = "cubic"

	// fecCollapseFrac is the collapse gate: single-flow TCP goodput at >=1%
	// configured loss must fall below this fraction of the 0%-loss capped figure.
	fecCollapseFrac = 0.5
)

// fecLossSweep are the configured netem loss points (percent). 0% is the capped,
// lossless reference; 0.5/1/2% quantify the collapse. Order matters: 0% first so
// the reference is measured before any loss is injected.
var fecLossSweep = []float64{0, 0.5, 1, 2}

// fecBaselinePath is a single capped path with a fixed delay and no config-time
// loss (loss is injected per sweep point via InjectLoss, which preserves the cap
// — see netns.go). It reuses DefaultPaths' veth names/IPs; safe because the test
// owns its own topology and tears it down.
var fecBaselinePath = []pathSpec{
	{name: "wan", edgeIP: "10.100.1.1", concIP: "10.100.1.2", edgeVeth: "wbAe", concVeth: "wbAc", delayMs: fecDelayMs, rateMbit: fecCapMbit},
}

// fecPoint is one sweep sample: the configured loss, the measured single-flow
// TCP goodput through the tunnel, and that goodput as a fraction of the 0%-loss
// figure.
type fecPoint struct {
	loss     float64
	mbps     float64
	fraction float64
}

// TestFECBaselineCollapse sweeps configured netem loss at a FIXED bandwidth cap
// and RTT, measuring single-flow TCP goodput THROUGH THE wanbond TUNNEL at each
// point, and asserts the long-fat-lossy-network collapse: goodput at >=1% loss
// falls materially below (< fecCollapseFrac of) the 0%-loss capped figure. It
// persists the table to docs/fec-baseline.md. This baseline PRECEDES and feeds
// T25/T29 (the pre-FEC reference); it does not depend on FEC.
//
// The tunnel is stood up ONCE at 0% loss (per the P0 pattern); the session then
// persists as loss is injected via InjectLoss (which preserves the cap), so each
// sweep point measures the same tunnel under a different drop rate.
func TestFECBaselineCollapse(t *testing.T) {
	// The collapse gate divides by the 0%-loss figure, which MUST be measured
	// first. Assert it at runtime so a reorder of fecLossSweep cannot make
	// baseline=0 -> frac=0 -> gate pass vacuously.
	if fecLossSweep[0] != 0 {
		t.Fatalf("fecLossSweep[0] = %g, want 0: the 0%%-loss reference must be measured first, else the collapse gate is vacuous", fecLossSweep[0])
	}

	top := SetupWithPaths(t, fecBaselinePath)
	fecBringUpTunnel(t, top, top.path("wan"))

	var results []fecPoint
	var baseline float64
	for _, loss := range fecLossSweep {
		// InjectLoss preserves delay+cap; loss=0 restores the capped, lossless base.
		top.InjectLoss("wan", loss)
		time.Sleep(500 * time.Millisecond) // let the qdisc change settle

		mbps := top.fecIperf3RecvMbps(t, concInner, fecIperfSecs)
		if loss == 0 {
			baseline = mbps
			if baseline <= 0 {
				t.Fatalf("0%% loss baseline throughput non-positive: %.2f Mbit/s", mbps)
			}
		}
		frac := 0.0
		if baseline > 0 {
			frac = mbps / baseline
		}
		results = append(results, fecPoint{loss: loss, mbps: mbps, fraction: frac})
		t.Logf("FEC baseline: loss=%.1f%%  single-flow TCP goodput=%.2f Mbit/s  frac=%.2f", loss, mbps, frac)
	}

	// The collapse: at >=1% configured loss, goodput must have fallen below
	// fecCollapseFrac of the 0%-loss figure — proving the fixture reproduces the
	// phenomenon FEC recovery is later measured against.
	for _, r := range results {
		if r.loss >= 1.0 && r.fraction >= fecCollapseFrac {
			t.Errorf("no collapse at %.1f%% loss: goodput %.2f Mbit/s = %.0f%% of the 0%%-loss figure (%.2f Mbit/s); expected < %.0f%%",
				r.loss, r.mbps, r.fraction*100, baseline, fecCollapseFrac*100)
		}
	}

	if err := os.WriteFile(fecBaselineDocPath(t), []byte(renderFECBaselineDoc(results)), 0o644); err != nil {
		t.Fatalf("write fec-baseline doc: %v", err)
	}
	t.Logf("wrote %s", fecBaselineDocPath(t))
}

// fecBringUpTunnel stands up the wanbond tunnel (edge + concentrator) over the
// single path p, following the P0 pattern: write both configs, start the
// concentrator (listening) then the edge, address wanbond0 on both ends, and
// ping until the WireGuard session is established. It reuses the p0_test.go
// helpers (buildWanbond, genKey, randKey, writeConfig, startProc, waitLink,
// pingUntil) and the wanbond0 inner addressing.
func fecBringUpTunnel(t *testing.T, top *Topology, p pathSpec) {
	t.Helper()
	bin := buildWanbond(t)
	edgePriv, edgePub := genKey(t)
	concPriv, concPub := genKey(t)
	psk := randKey(t) // outer-control PSK: required by config validation, unused by pass-through

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

	// Concentrator first (it must be listening before the edge initiates), then edge.
	conc := top.startProc(t, "concentrator", "nsenter", "-t", strconv.Itoa(top.pid), "-n", bin, "--config", concCfg)
	edge := top.startProc(t, "edge", bin, "--config", edgeCfg)

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

	// The first ping triggers the handshake; retry until the session is established.
	if !top.pingUntil(concInner, 15*time.Second) {
		t.Fatalf("tunnel never came up: %s unreachable through the tunnel\n--- edge ---\n%s\n--- concentrator ---\n%s",
			concInner, edge.log(), conc.log())
	}
}

// fecIperf3RecvMbps runs a single-flow TCP transfer to serverIP (inside the peer
// netns) for secs seconds with the congestion control pinned to
// fecCongestionControl, and returns the RECEIVER-side goodput in Mbit/s
// (end.sum_received.bits_per_second). It reads sum_received — NOT sum_sent —
// because at the collapsed low points the sender-side figure still counts
// unacked in-flight bytes, which materially overestimates delivered goodput; and
// delivered goodput is exactly the quantity FEC recovery (T25/T29) is later
// compared against. This is a T36-local variant that deliberately does NOT change
// the shared iperf3Mbps used by the P0/baseline tests.
func (top *Topology) fecIperf3RecvMbps(t *testing.T, serverIP string, secs int) float64 {
	t.Helper()
	top.startProc(t, "iperf3-server", "nsenter", "-t", strconv.Itoa(top.pid), "-n", "iperf3", "-s", "-1", "-B", serverIP)
	time.Sleep(500 * time.Millisecond) // allow the server to bind and listen

	out := top.runOut("iperf3", "-c", serverIP, "-t", strconv.Itoa(secs), "--congestion", fecCongestionControl, "-J")
	var r struct {
		End struct {
			SumReceived struct {
				BitsPerSecond float64 `json:"bits_per_second"`
			} `json:"sum_received"`
		} `json:"end"`
	}
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("parse iperf3 json: %v\n%s", err, out)
	}
	return r.End.SumReceived.BitsPerSecond / 1e6
}

// fecBaselineDocPath resolves docs/fec-baseline.md relative to the module root,
// found by walking up from the test's working directory to the go.mod.
func fecBaselineDocPath(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, "docs", "fec-baseline.md")
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("go.mod not found above working directory %s", dir)
		}
		dir = parent
	}
}

// renderFECBaselineDoc renders the committed baseline document from the measured
// sweep. Re-running TestFECBaselineCollapse overwrites docs/fec-baseline.md with
// fresh numbers; the file structure matches the placeholder version committed
// with the test so the diff is limited to the measured cells.
func renderFECBaselineDoc(results []fecPoint) string {
	var b strings.Builder
	b.WriteString("# Single-flow TCP collapse baseline (pre-FEC reference)\n\n")
	b.WriteString("Auto-generated by `TestFECBaselineCollapse` (`test/e2e/fec_baseline_test.go`).\n")
	b.WriteString("Regenerate with `just e2e-run TestFECBaselineCollapse` on a host with\n")
	b.WriteString("`CAP_NET_ADMIN` + `/dev/net/tun` (the netns e2e tier). Re-running overwrites\n")
	b.WriteString("this file with fresh measurements.\n\n")

	b.WriteString("## Fixture parameters\n\n")
	b.WriteString("| Parameter | Value |\n")
	b.WriteString("|---|---|\n")
	fmt.Fprintf(&b, "| Bandwidth cap (netem rate) | %d Mbit/s |\n", fecCapMbit)
	fmt.Fprintf(&b, "| One-way delay (netem) | %d ms (RTT ~= %d ms) |\n", fecDelayMs, 2*fecDelayMs)
	b.WriteString("| Loss model | netem uniform egress loss (edge side) |\n")
	b.WriteString("| Transport under test | single-flow TCP (iperf3) through the wanbond tunnel |\n")
	fmt.Fprintf(&b, "| TCP congestion control | %s (pinned via iperf3 `--congestion`) |\n", fecCongestionControl)
	b.WriteString("| Goodput metric | iperf3 `end.sum_received` (receiver-side delivered bytes) |\n")
	fmt.Fprintf(&b, "| iperf3 duration per point | %d s |\n", fecIperfSecs)
	b.WriteString("| Topology | single capped path, edge <-> concentrator netns, WireGuard tunnel |\n\n")

	b.WriteString("## Results\n\n")
	b.WriteString("Single-flow TCP goodput through the tunnel vs configured netem loss, at a\n")
	fmt.Fprintf(&b, "fixed %d Mbit/s cap and ~%d ms RTT:\n\n", fecCapMbit, 2*fecDelayMs)
	b.WriteString("| Configured loss | Goodput (Mbit/s) | Fraction of 0%-loss |\n")
	b.WriteString("|---|---|---|\n")
	for _, r := range results {
		fmt.Fprintf(&b, "| %.1f%% | %.1f | %.2f |\n", r.loss, r.mbps, r.fraction)
	}
	b.WriteString("\n")

	b.WriteString("## Interpretation\n\n")
	b.WriteString("At a fixed bandwidth cap and RTT, single-flow TCP goodput is bounded by the\n")
	b.WriteString("Mathis relation, goodput <= MSS / (RTT * sqrt(p)), where p is the packet-loss\n")
	b.WriteString("probability. Goodput therefore falls with roughly 1/sqrt(p): a fraction of a\n")
	b.WriteString("percent of loss on a long-fat path collapses a single flow far below the link\n")
	b.WriteString("capacity. The P0 real-host validation over live uplinks recorded the same\n")
	b.WriteString("effect — a single TCP flow over a ~29 ms RTT path held only ~18-48 Mbit/s\n")
	b.WriteString("under sub-percent (~0.1-0.8%) loss, well under the available capacity (see\n")
	b.WriteString("`docs/p0-findings.md` and the ledger handoff HO5 / goal G1 follow-up section).\n\n")
	b.WriteString("The results above reproduce the phenomenon in the netns fixture: at >=1%\n")
	fmt.Fprintf(&b, "configured loss the tunnel's single-flow TCP goodput drops below %.0f%% of the\n", fecCollapseFrac*100)
	b.WriteString("0%-loss capped figure. This is the pre-FEC reference the FEC-recovery work\n")
	b.WriteString("(T25/T29) is measured against: FEC should restore goodput toward the 0%-loss\n")
	b.WriteString("figure by masking the residual loss the transport otherwise reacts to.\n")
	return b.String()
}
