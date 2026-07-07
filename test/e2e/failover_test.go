//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestP1Failover is the P1 failover-recovery acceptance and the D15 regression
// guard: with a SATURATING bidirectional bulk flow loading both ends, the ACTIVE
// WAN is killed and the bond must reroute egress in BOTH directions within
// P1RecoverySeconds — reliably, with margin, not just on a lucky run — and the flow
// must survive (no WireGuard-session reset).
//
// What is measured, and why THIS way. Recovery is "throughput restored in both
// directions after the active WAN dies". Bidirectional traffic is restored the
// instant BOTH ends have rerouted egress off the dead path; the reroute itself is a
// sub-millisecond Pick()/data-structure update, so the recovery latency in each
// direction is precisely when that end's scheduler logs its "active path change"
// failover. The test reads those two timestamps from the two daemons' logs — a
// sub-millisecond, un-confounded measurement — and takes recovery = max(edge, conc):
//
//   - edge_switch is the FORWARD direction (edge egress → backup);
//   - conc_switch is the REPLY direction (concentrator egress → backup) — the term
//     D15/D16 previously under-budgeted and the one that tailed past 3s under load.
//
// A DATA-plane ping-gap probe was rejected as the timing metric: on the emulated
// single post-failover path it shares one netem queue with the saturating flow and
// is tail-dropped for seconds, which measures congestion, not failover. Instead the
// saturating flow IS the data-plane proof: it spans the kill and must complete with
// positive throughput in both directions, proving the one WireGuard session (hence
// the flow) survived the reroute. (Cross-check: with the load removed a clean
// 20ms-cadence ping gap recovers in ~1.5s, matching the switch latencies — the log
// metric is a faithful proxy for data-plane recovery, just without the confound.)
//
// The load is what made the D15 tail jitter: the concentrator absorbing a saturating
// flood on 4 vCPU starved its probe-loop ticker, delaying the reply-direction detect.
// The T39 fix advances liveness off the receive path too, so the concentrator-side
// switch no longer waits on that starved timer.
//
// Run it MANY times to characterise the tail: `-run TestP1Failover -count=20`. Each
// -count iteration is an independent bring-up + kill + measure; the per-run
// `RECOVERY_MS=` line is parseable for the pass-rate/distribution report.
func TestP1Failover(t *testing.T) {
	bin := buildWanbond(t)
	top := Setup(t)
	edge, conc := setupMultipathTunnelLevel(t, top, bin, DefaultPaths, "info")

	if !top.pingUntil(concInner, 15*time.Second) {
		t.Fatalf("bond never came up\n--- edge ---\n%s\n--- conc ---\n%s", edge.log(), conc.log())
	}

	primary := DefaultPaths[0]   // starlink — the active-backup primary (all egress rides it)
	secondary := DefaultPaths[1] // cellular — the failover backup

	// Let both ends settle so the primary is the established active path on BOTH the
	// edge (egress) and the concentrator (replies) before the kill.
	time.Sleep(1500 * time.Millisecond)

	// A saturating bidirectional bulk flow spans the whole window: it recreates the
	// D15 CPU load AND is the data-plane-survival proof (it must finish with positive
	// throughput both ways). It is uncapped, so both ends' WG crypto runs flat out.
	const loadSecs = 12
	top.startProc(t, "iperf3-server", "nsenter", "-t", strconv.Itoa(top.pid), "-n", "iperf3", "-s", "-1", "-B", concInner)
	time.Sleep(400 * time.Millisecond)
	load := exec.Command("iperf3", "-c", concInner, "-t", strconv.Itoa(loadSecs), "--bidir", "-J")
	loadOut := &lockedBuffer{}
	load.Stdout, load.Stderr = loadOut, loadOut
	if err := load.Start(); err != nil {
		t.Fatalf("start load flow: %v", err)
	}

	// Let the flow ramp and both ends reach steady state on the primary, then kill the
	// active WAN and stamp the instant.
	time.Sleep(3 * time.Second)
	killAt := time.Now()
	top.Blackhole(primary.name)
	t.Logf("killed active WAN %q at T0", primary.name)

	// Await the bulk flow: a preserved single WG session keeps the connection across
	// the reroute, so a healthy failover exits 0 with positive throughput; a session
	// reset surfaces as a non-zero exit.
	loadErr := load.Wait()
	top.Restore(primary.name)

	// Per-direction failover latency, read from each daemon's scheduler transition.
	edgeSwitch := schedSwitchLatency(edge.log(), killAt)
	concSwitch := schedSwitchLatency(conc.log(), killAt)
	if edgeSwitch < 0 || concSwitch < 0 {
		t.Fatalf("could not measure both failover switches (edge=%s conc=%s) — no scheduler transition logged after the kill\n--- edge ---\n%s\n--- conc ---\n%s",
			latencyStr(edgeSwitch), latencyStr(concSwitch), edge.log(), conc.log())
	}
	// End-to-end bidirectional recovery is governed by the SLOWER of the two ends: both
	// directions carry traffic once both have rerouted.
	recovery := edgeSwitch
	if concSwitch > recovery {
		recovery = concSwitch
	}

	// The single parseable metric line the multi-run harness greps.
	t.Logf("RECOVERY_MS=%d budget_ms=%d failover_budget_ms=%d edge_switch_ms=%d conc_switch_ms=%d",
		recovery.Milliseconds(), int64(P1RecoverySeconds)*1000, PLivenessFailoverBudget.Milliseconds(),
		edgeSwitch.Milliseconds(), concSwitch.Milliseconds())

	// Data-plane survival: the flow that spanned the kill must have carried traffic
	// both ways with no reset.
	fwd, rev := iperfBidirMbps(loadOut.String())
	if loadErr != nil || fwd <= 0 || rev <= 0 {
		t.Errorf("bulk flow did not survive failover (exit err=%v, forward=%.1f Mbit/s, reverse=%.1f Mbit/s) — a WG-session reset?\n%s",
			loadErr, fwd, rev, loadOut.String())
	}

	// Sanity: the backup carries the recovered bond.
	if !top.Reachable(secondary.name, 3) {
		t.Errorf("backup path %q unreachable after failover", secondary.name)
	}

	if recovery >= time.Duration(P1RecoverySeconds)*time.Second {
		t.Errorf("bidirectional recovery %v exceeded P1 budget %ds (edge_switch=%v conc_switch=%v)\n--- edge ---\n%s\n--- conc ---\n%s",
			recovery, P1RecoverySeconds, edgeSwitch, concSwitch, edge.log(), conc.log())
	}
}

// schedSwitchLatency returns the delay from killAt to the first "scheduler active
// path change" transition in a daemon's JSON log after the kill, or -1 if none is
// found (the daemon log is slog JSON: {"time":...,"msg":"scheduler active path
// change",...}). It is the per-direction failover-recovery latency: the reroute is a
// sub-ms update, so the transition timestamp is when that end's egress moves to the
// surviving path.
func schedSwitchLatency(logText string, killAt time.Time) time.Duration {
	best := time.Duration(-1)
	for _, line := range strings.Split(logText, "\n") {
		if !strings.Contains(line, "scheduler active path change") {
			continue
		}
		var rec struct {
			Time time.Time `json:"time"`
		}
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		if rec.Time.Before(killAt) {
			continue // the initial cold-start selection, not the failover
		}
		d := rec.Time.Sub(killAt)
		if best < 0 || d < best {
			best = d
		}
	}
	return best
}

// iperfBidirMbps parses an iperf3 --bidir -J report, returning the forward
// (client→server, sum_sent) and reverse (server→client, sum_received) throughput in
// Mbit/s. Zero for either direction (or an unparseable report) signals the flow did
// not carry traffic that way.
func iperfBidirMbps(out string) (forward, reverse float64) {
	var r struct {
		End struct {
			SumSent struct {
				BitsPerSecond float64 `json:"bits_per_second"`
			} `json:"sum_sent"`
			SumReceived struct {
				BitsPerSecond float64 `json:"bits_per_second"`
			} `json:"sum_received"`
		} `json:"end"`
	}
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		return 0, 0
	}
	return r.End.SumSent.BitsPerSecond / 1e6, r.End.SumReceived.BitsPerSecond / 1e6
}

// latencyStr renders a measured latency, or "n/a" for a missing (-1) measurement.
func latencyStr(d time.Duration) string {
	if d < 0 {
		return "n/a"
	}
	return fmt.Sprintf("%d", d.Milliseconds())
}
