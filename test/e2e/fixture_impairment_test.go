//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Impairment self-test knobs. capMbit is deliberately well below the CPU-bound
// tunnel throughput (P0 measured ~150-170 Mbit/s of userspace-WG crypto on a
// 1-vCPU host), so a rate-capped raw path is LINK-bound and a standing queue can
// form — the prerequisite for the bufferbloat/pacing (T21/T23) and FEC-recovery
// (T25/T29) work this fixture enables.
const (
	capMbit    = 50   // per-path bandwidth cap under test (Mbit/s)
	lossTarget = 10.0 // config-time uniform loss under test (percent)

	// Cap tolerance: netem-rate TCP goodput sits a little under the shaped rate
	// (header overhead) and jitters run-to-run; accept a generous band centred on
	// the cap. The point is link-bound ~= cap, NOT CPU-bound ~= 150.
	capLoMbit = 35.0
	capHiMbit = 56.0

	// Loss tolerance: uniform netem loss over a few thousand UDP datagrams lands
	// near the configured percentage; accept a band around it.
	lossLoPct = 5.0
	lossHiPct = 18.0
)

// impairmentPaths is a bespoke two-path topology exercising the OPTIONAL knobs:
// "capped" carries a bandwidth cap (no loss); "lossy" carries config-time loss
// (no cap). Both use a small fixed delay so the raw-link iperf3 measurements are
// stable. They reuse DefaultPaths' veth names/IPs — safe because each test owns
// its own topology and tears it down.
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
//	(b) controlled loss — iperf3 UDP through the lossy path measures a loss
//	    fraction in the expected band around the configured percentage.
func TestFixtureImpairment(t *testing.T) {
	top := SetupWithPaths(t, impairmentPaths)

	// (a) Bandwidth cap. The qdisc must carry the rate, and the shaped TCP
	// throughput must land near the cap — far below the ~150 Mbit/s CPU bound.
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

	// (b) Controlled loss. The qdisc must carry the loss, and measured UDP loss
	// must fall in the band around the configured percentage. Offered UDP rate is
	// kept well below the (uncapped) link so overflow does not confound netem loss.
	lossy := top.path("lossy")
	if q := top.QdiscShow("lossy"); !strings.Contains(q, "loss") {
		t.Errorf("lossy path qdisc missing loss knob: %q", strings.TrimSpace(q))
	}
	loss := top.udpLossPct(t, lossy.concIP, 20, 4)
	if loss < lossLoPct || loss > lossHiPct {
		t.Errorf("lossy path measured UDP loss = %.1f%%, want ~%.0f%% (%.0f-%.0f band)",
			loss, lossTarget, lossLoPct, lossHiPct)
	} else {
		t.Logf("lossy path: measured UDP loss %.1f%% (configured %.0f%%)", loss, lossTarget)
	}
}

// udpLossPct runs a one-shot iperf3 UDP transfer to serverIP (inside the peer
// netns) at mbit Mbit/s for secs seconds and returns the loss percentage the
// receiver reported.
func (top *Topology) udpLossPct(t *testing.T, serverIP string, mbit, secs int) float64 {
	t.Helper()
	top.startProc(t, "iperf3-udp-server", "nsenter", "-t", strconv.Itoa(top.pid), "-n", "iperf3", "-s", "-1", "-B", serverIP)
	time.Sleep(500 * time.Millisecond) // allow the server to bind and listen

	out := top.runOut("iperf3", "-c", serverIP, "-u", "-b", fmt.Sprintf("%dM", mbit), "-t", strconv.Itoa(secs), "-J")
	var r struct {
		End struct {
			Sum struct {
				LostPercent float64 `json:"lost_percent"`
				LostPackets int     `json:"lost_packets"`
				Packets     int     `json:"packets"`
			} `json:"sum"`
		} `json:"end"`
	}
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("parse iperf3 udp json: %v\n%s", err, out)
	}
	return r.End.Sum.LostPercent
}
