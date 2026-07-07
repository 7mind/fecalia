//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"os"
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

// P1 active-backup priority indices: the scheduler's health slice is priority-
// ordered, index 0 the preferred primary and index 1 the failover backup — matching
// the config path order (DefaultPaths[0] primary, DefaultPaths[1] backup). The
// "scheduler active path change" log records the destination index as its "to"
// field, so a to==primaryPathIdx transition is a failback and to==backupPathIdx is a
// failover.
const (
	primaryPathIdx = 0
	backupPathIdx  = 1
)

// TestP1FailoverRepeatedFlap is the SECOND half of the T20 acceptance that
// TestP1Failover (a single kill+restore) does not cover: "repeated flap does not
// wedge the tunnel". It runs ONE long-lived saturating bidirectional bulk flow across
// SEVERAL kill/restore cycles of the ACTIVE WAN and asserts the tunnel recovers every
// cycle — within P1RecoverySeconds, both directions, measured the same sound
// per-direction way as TestP1Failover — and that the single flow survives ALL cycles
// with no WireGuard-session reset.
//
// Non-vacuity guard (the opus T20-r1 finding). A repeated-flap test is only meaningful
// if each cycle genuinely kills the ACTIVE egress path. After a restore the recovered
// primary does not reclaim egress instantly: the scheduler debounces failback with a
// 5s dwell (sched.Config.FailbackAfter), so a blind time.Sleep between cycles could
// kill an already-idle backup and pass trivially. Instead, before every kill this test
// confirms — in BOTH daemons' logs — that egress sits on the primary. For cycles >= 2 it
// waits for a FRESH to-primary "scheduler active path change" (to==0) logged after that
// cycle's restore: that IS the genuine FAILBACK and the anti-wedge proof for the prior
// cycle. Cycle 1 has no prior restore and the lossless primary logs its to-primary
// selection only once at cold-start (before any post-setup instant, and never repeated),
// so it instead reads the CURRENT active path — the most-recent transition's destination
// — and asserts it is the primary. Either way the precondition guarantees the next kill
// hits the active path.
//
// Both daemons run at INFO so those transitions are observable (as in TestP1Failover).
// The per-cycle metric line `FLAP_CYCLE=<n> RECOVERY_MS=<ms>` is grep-able for a
// pass-rate/distribution report over `-run TestP1FailoverRepeatedFlap -count=N`.
func TestP1FailoverRepeatedFlap(t *testing.T) {
	const (
		flapCycles = 3
		// flapFailoverPoll bounds how long we wait to OBSERVE both ends' failover
		// switch after a kill. It is set WELL ABOVE P1RecoverySeconds (not budget+1s)
		// so a heavily-late failover is still OBSERVED and MEASURED — then asserted
		// against the budget with its true magnitude via the per-cycle Errorf below —
		// rather than lost to an unmeasured non-observation Fatalf. The old budget+1s
		// (4s) window was the T20-review measurement gap: a genuine >4s recovery tail
		// fell OUTSIDE it and was reported as "never switched" (an unmeasured Fatalf)
		// instead of "switched late by N ms" (a measured, magnitude-bearing failure).
		flapFailoverPoll = time.Duration(P1RecoverySeconds)*time.Second + 5*time.Second
		// flapFailbackPoll bounds the wait for both ends to fail egress BACK to the
		// primary after a restore: the 5s FailbackAfter dwell + up-detect (3×200ms) +
		// margin for the D15 under-load detection tail. If failback does not complete
		// within this, the tunnel has wedged on the backup.
		flapFailbackPoll = 12 * time.Second
		flapRampBefore   = 2500 * time.Millisecond
	)

	bin := buildWanbond(t)
	top := Setup(t)
	edge, conc := setupMultipathTunnelLevel(t, top, bin, DefaultPaths, "info")

	if !top.pingUntil(concInner, 15*time.Second) {
		t.Fatalf("bond never came up\n--- edge ---\n%s\n--- conc ---\n%s", edge.log(), conc.log())
	}

	primary := DefaultPaths[primaryPathIdx]  // starlink — the active-backup primary
	secondary := DefaultPaths[backupPathIdx] // cellular — the failover backup

	// One saturating bidirectional flow spans EVERY cycle: it recreates the D15 CPU
	// load and is the data-plane-survival proof — it must finish (exit 0) with positive
	// throughput both ways, proving the one WireGuard session (hence the flow) survived
	// all the reroutes. Size its lifetime to the worst-case cycle budget so it is still
	// running throughout the loop no matter how the per-cycle waits resolve.
	loadWindow := flapRampBefore +
		time.Duration(flapCycles)*(flapFailoverPoll+flapFailbackPoll+time.Second) +
		4*time.Second
	loadSecs := int(loadWindow.Seconds()) + 1

	top.startProc(t, "iperf3-server", "nsenter", "-t", strconv.Itoa(top.pid), "-n", "iperf3", "-s", "-1", "-B", concInner)
	time.Sleep(400 * time.Millisecond)
	load := exec.Command("iperf3", "-c", concInner, "-t", strconv.Itoa(loadSecs), "--bidir", "-J")
	loadOut := &lockedBuffer{}
	load.Stdout, load.Stderr = loadOut, loadOut
	if err := load.Start(); err != nil {
		t.Fatalf("start load flow: %v", err)
	}

	// Let the flow ramp and both ends reach steady state on the primary before cycle 1.
	time.Sleep(flapRampBefore)

	// sinceRef is the reference instant after which a to-primary transition confirms
	// egress has (re)claimed the primary before the next kill. It is used only for
	// cycles >= 2, where it is the PREVIOUS cycle's restore instant, so each confirmation
	// is that cycle's genuine FAILBACK (a to-primary transition logged strictly after the
	// restore). Cycle 1 does not use it — see the cycle-1 branch below — so its zero
	// value is never read.
	var sinceRef time.Time

	for cycle := 1; cycle <= flapCycles; cycle++ {
		// Precondition (non-vacuity): egress must be on the primary on BOTH ends before
		// we kill it, so the kill genuinely hits the ACTIVE path. This is also the
		// anti-wedge assertion for the prior cycle.
		if cycle == 1 {
			// Cycle 1 cannot demand a FRESH to-primary transition: the cold-start
			// selection to the primary (from:-1→to:0) is logged during bring-up, before
			// any instant we could stamp after setup, and — DefaultPaths being lossless —
			// the primary never re-flaps, so no further to-primary transition is ever
			// logged. A windowed waitBothSwitchTo would therefore time out with a FALSE
			// wedge before any kill. Instead assert the CURRENT active path is the primary
			// by reading the most-recent transition's destination on both ends — robust to
			// the cold-start transition predating any reference instant, and the true
			// non-vacuity precondition (both ends are on the primary now).
			if !waitBothActiveOn(edge, conc, primaryPathIdx, flapFailbackPoll) {
				t.Fatalf("cycle 1: egress not on the primary on both ends within %v at start — cold-start selection never settled on the primary\n--- edge ---\n%s\n--- conc ---\n%s",
					flapFailbackPoll, edge.log(), conc.log())
			}
		} else if _, _, ok := waitBothSwitchTo(edge, conc, sinceRef, primaryPathIdx, flapFailbackPoll); !ok {
			t.Fatalf("cycle %d: egress never (re)claimed the primary on both ends within %v — tunnel wedged on the backup after the prior cycle\n--- edge ---\n%s\n--- conc ---\n%s",
				cycle, flapFailbackPoll, edge.log(), conc.log())
		}

		// Kill the ACTIVE primary and stamp T0 for this cycle.
		killAt := time.Now()
		top.Blackhole(primary.name)

		// Per-direction failover latency, read from each daemon's to-backup transition
		// after this cycle's kill — the same sound, un-confounded measurement as
		// TestP1Failover. End-to-end bidirectional recovery is the SLOWER of the two.
		edgeSwitch, concSwitch, ok := waitBothSwitchTo(edge, conc, killAt, backupPathIdx, flapFailoverPoll)
		if !ok {
			top.Restore(primary.name)
			t.Fatalf("cycle %d: both ends did not fail over to the backup within %v (edge=%s conc=%s %s) — no scheduler transition logged after the kill\n--- edge ---\n%s\n--- conc ---\n%s",
				cycle, flapFailoverPoll, latencyStr(edgeSwitch), latencyStr(concSwitch), readLoadAvg(), edge.log(), conc.log())
		}
		recovery := edgeSwitch
		if concSwitch > recovery {
			recovery = concSwitch
		}
		// Record host load on the metric line: the repeated-flap tail is sensitive to
		// shared-VM CPU contention (4 vCPU, possibly multi-tenant), so every per-cycle
		// measurement carries the load that produced it — that is what lets a genuine
		// product tail be told apart from host-contention noise in the run log (D18).
		t.Logf("FLAP_CYCLE=%d RECOVERY_MS=%d budget_ms=%d edge_switch_ms=%d conc_switch_ms=%d %s",
			cycle, recovery.Milliseconds(), int64(P1RecoverySeconds)*1000,
			edgeSwitch.Milliseconds(), concSwitch.Milliseconds(), readLoadAvg())
		if recovery >= time.Duration(P1RecoverySeconds)*time.Second {
			t.Errorf("cycle %d: bidirectional recovery %v exceeded P1 budget %ds (edge_switch=%v conc_switch=%v %s)",
				cycle, recovery, P1RecoverySeconds, edgeSwitch, concSwitch, readLoadAvg())
		}

		// Restore the primary; the next iteration's precondition wait confirms failback.
		restoreAt := time.Now()
		top.Restore(primary.name)
		sinceRef = restoreAt
	}

	// Final anti-wedge check: after the last restore the bond must return to the
	// primary too (the loop's top-of-cycle check does not cover the last cycle).
	if _, _, ok := waitBothSwitchTo(edge, conc, sinceRef, primaryPathIdx, flapFailbackPoll); !ok {
		t.Errorf("after the final cycle egress never returned to the primary on both ends within %v — tunnel wedged on the backup\n--- edge ---\n%s\n--- conc ---\n%s",
			flapFailbackPoll, edge.log(), conc.log())
	}

	// Data-plane survival across ALL cycles: the one flow that spanned every kill must
	// have completed with traffic both ways and no reset.
	loadErr := load.Wait()
	fwd, rev := iperfBidirMbps(loadOut.String())
	if loadErr != nil || fwd <= 0 || rev <= 0 {
		t.Errorf("bulk flow did not survive %d failover cycles (exit err=%v, forward=%.1f Mbit/s, reverse=%.1f Mbit/s) — a WG-session reset?\n%s",
			flapCycles, loadErr, fwd, rev, loadOut.String())
	}

	// Sanity: the backup path is still reachable (it carried the bond during each cycle).
	if !top.Reachable(secondary.name, 3) {
		t.Errorf("backup path %q unreachable after repeated flap", secondary.name)
	}

	t.Logf("repeated-flap: survived %d kill/restore cycles of the active WAN with one spanning flow (forward=%.1f Mbit/s reverse=%.1f Mbit/s), egress failed back to the primary each cycle",
		flapCycles, fwd, rev)
}

// waitBothSwitchTo polls both daemons' logs until EACH has logged a "scheduler active
// path change" whose destination is toIdx at some instant strictly after `after`, or
// the deadline elapses. It returns the two per-daemon latencies from `after` to that
// transition (or -1 for a daemon that never switched) and whether both were observed.
// It is used both to MEASURE failover (toIdx = backupPathIdx, from the kill instant)
// and to CONFIRM failback (toIdx = primaryPathIdx, from the restore instant) before the
// next kill.
func waitBothSwitchTo(edge, conc *proc, after time.Time, toIdx int, deadline time.Duration) (edgeLat, concLat time.Duration, ok bool) {
	stop := time.Now().Add(deadline)
	for {
		edgeLat = switchToLatency(edge.log(), after, toIdx)
		concLat = switchToLatency(conc.log(), after, toIdx)
		if edgeLat >= 0 && concLat >= 0 {
			return edgeLat, concLat, true
		}
		if time.Now().After(stop) {
			return edgeLat, concLat, false
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// currentActivePathIdx returns the destination index of the MOST-RECENT "scheduler
// active path change" transition in a daemon's log, or -1 if none has been logged. It
// is NOT windowed by an instant: it reports the scheduler's CURRENT active path
// regardless of when that transition was logged. Cycle 1's precondition uses it to
// confirm the cold-start selection put egress on the primary WITHOUT demanding a fresh
// post-setup transition — the lossless primary logs its to-primary selection only once
// at bring-up and never re-flaps, so no such fresh transition ever exists.
func currentActivePathIdx(logText string) int {
	idx := -1
	var latest time.Time
	for _, line := range strings.Split(logText, "\n") {
		if !strings.Contains(line, "scheduler active path change") {
			continue
		}
		var rec struct {
			Time time.Time `json:"time"`
			To   int       `json:"to"`
		}
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		if idx < 0 || rec.Time.After(latest) {
			latest = rec.Time
			idx = rec.To
		}
	}
	return idx
}

// waitBothActiveOn polls both daemons' logs until EACH reports its CURRENT active path
// (the most-recent transition's destination) is toIdx, or the deadline elapses. Unlike
// waitBothSwitchTo it asserts the PRESENT active path rather than a fresh transition
// after some instant, so it confirms cycle 1's cold-start selection without requiring a
// to-primary transition logged after a reference time.
func waitBothActiveOn(edge, conc *proc, toIdx int, deadline time.Duration) bool {
	stop := time.Now().Add(deadline)
	for {
		if currentActivePathIdx(edge.log()) == toIdx && currentActivePathIdx(conc.log()) == toIdx {
			return true
		}
		if time.Now().After(stop) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// switchToLatency returns the delay from `after` to the EARLIEST "scheduler active
// path change" transition to destination index toIdx logged strictly after `after`, or
// -1 if none. It is schedSwitchLatency refined by the transition's "to" field, so a
// failover (to the backup) and a failback (to the primary) can be told apart in a log
// that accumulates both across repeated cycles.
func switchToLatency(logText string, after time.Time, toIdx int) time.Duration {
	best := time.Duration(-1)
	for _, line := range strings.Split(logText, "\n") {
		if !strings.Contains(line, "scheduler active path change") {
			continue
		}
		var rec struct {
			Time time.Time `json:"time"`
			To   int       `json:"to"`
		}
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		if rec.To != toIdx || !rec.Time.After(after) {
			continue
		}
		d := rec.Time.Sub(after)
		if best < 0 || d < best {
			best = d
		}
	}
	return best
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

// readLoadAvg returns the host's 1/5/15-minute load averages as a compact
// "load=1min,5min,15min" token (or "load=?" if /proc/loadavg is unreadable). The
// repeated-flap failover tail is sensitive to shared-VM CPU contention — the e2e host
// runs on 4 vCPU and may be multi-tenant — so every per-cycle metric line and every
// budget-exceeded/non-observation failure stamps the load that accompanied it. That
// is the robustness the D18 investigation added: a future over-budget cycle is
// self-classifying from the log alone (a high load average points at host contention;
// a low one at a genuine product regression) instead of needing an out-of-band host
// snapshot that no longer exists by the time the failure is read.
func readLoadAvg() string {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return "load=?"
	}
	f := strings.Fields(string(b))
	if len(f) < 3 {
		return "load=?"
	}
	return fmt.Sprintf("load=%s,%s,%s", f[0], f[1], f[2])
}
