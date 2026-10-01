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
// guard: with a SATURATING bidirectional bulk flow loading both ends, one WAN is
// killed and BOTH ends must detect the dead path within P1RecoverySeconds —
// reliably, with margin, not just on a lucky run — and the flow must survive (no
// WireGuard-session reset).
//
// What is measured, and why THIS way. Each end's liveness plane marks the killed
// path DOWN independently and logs a "path liveness transition" record. The test
// reads those two timestamps from the two daemons' logs — a sub-millisecond,
// un-confounded measurement — and takes recovery = max(edge, conc):
//
//   - edge_down is the edge's detection of the dead path;
//   - conc_down is the concentrator's — the term D15/D16 previously under-budgeted
//     and the one that tailed past 3s under load.
//
// The transport moves traffic off a lane whose acknowledgements stall without
// waiting for that verdict and logs no record when it does, so the instant egress
// leaves the dead path is not observable from the logs; liveness detection is what
// the hub-failover controller and wanbond_path_up key on.
//
// A DATA-plane ping-gap probe was rejected as the timing metric: on the emulated
// single post-failover path it shares one netem queue with the saturating flow and
// is tail-dropped for seconds, which measures congestion, not failover. Instead the
// saturating flow IS the data-plane proof: it spans the kill and must complete with
// positive throughput in both directions, proving the one WireGuard session (hence
// the flow) survived the reroute.
//
// The load is what made the D15 tail jitter: the concentrator absorbing a saturating
// flood on 4 vCPU starved its probe-loop ticker, delaying its detect. The T39 fix
// advances liveness off the receive path too, so the concentrator-side detect no
// longer waits on that starved timer.
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

	primary := DefaultPaths[primaryPathIdx]  // starlink — the WAN that is killed
	secondary := DefaultPaths[backupPathIdx] // cellular — the surviving WAN

	// Let both ends settle so both paths are established on BOTH the edge and the
	// concentrator before the kill.
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
	// Reap the saturating flow on EVERY exit path, not just load.Wait() on success: a
	// Fatalf before Wait would otherwise leak an uncapped ~70s --bidir iperf3 that keeps
	// saturating the (shared) e2e host and contaminates subsequent/concurrent runs (D21).
	// Kill after a completed Wait is a harmless no-op.
	t.Cleanup(func() {
		if load.Process != nil {
			_ = load.Process.Kill()
		}
	})

	// Let the flow ramp and both ends reach steady state, then kill the WAN and stamp
	// the instant.
	time.Sleep(3 * time.Second)
	killAt := time.Now()
	top.Blackhole(primary.name)
	t.Logf("killed WAN %q at T0", primary.name)

	// Await the bulk flow: a preserved single WG session keeps the connection across
	// the reroute, so a healthy failover exits 0 with positive throughput; a session
	// reset surfaces as a non-zero exit.
	loadErr := load.Wait()
	top.Restore(primary.name)

	// Per-end detection latency, read from each daemon's liveness transition.
	edgeDown := pathLivenessLatency(edge.log(), primary.name, livenessDown, killAt)
	concDown := pathLivenessLatency(conc.log(), primary.name, livenessDown, killAt)
	if edgeDown < 0 || concDown < 0 {
		t.Fatalf("could not measure both ends' detection (edge=%s conc=%s) — no liveness down transition logged for %q after the kill\n--- edge ---\n%s\n--- conc ---\n%s",
			latencyStr(edgeDown), latencyStr(concDown), primary.name, edge.log(), conc.log())
	}
	// End-to-end bidirectional recovery is governed by the SLOWER of the two ends.
	recovery := edgeDown
	if concDown > recovery {
		recovery = concDown
	}

	// The single parseable metric line the multi-run harness greps.
	t.Logf("RECOVERY_MS=%d budget_ms=%d failover_budget_ms=%d edge_down_ms=%d conc_down_ms=%d",
		recovery.Milliseconds(), int64(P1RecoverySeconds)*1000, PLivenessFailoverBudget.Milliseconds(),
		edgeDown.Milliseconds(), concDown.Milliseconds())

	// Data-plane survival: the flow that spanned the kill must have carried traffic
	// both ways with no reset.
	fwd, rev := iperfBidirMbps(loadOut.String())
	if loadErr != nil || fwd <= 0 || rev <= 0 {
		t.Errorf("bulk flow did not survive failover (exit err=%v, forward=%.1f Mbit/s, reverse=%.1f Mbit/s) — a WG-session reset?\n%s",
			loadErr, fwd, rev, loadOut.String())
	}

	// Sanity: the surviving path carries the recovered bond.
	if !top.Reachable(secondary.name, 3) {
		t.Errorf("surviving path %q unreachable after failover", secondary.name)
	}

	if recovery >= time.Duration(P1RecoverySeconds)*time.Second {
		t.Errorf("bidirectional recovery %v exceeded P1 budget %ds (edge_down=%v conc_down=%v)\n--- edge ---\n%s\n--- conc ---\n%s",
			recovery, P1RecoverySeconds, edgeDown, concDown, edge.log(), conc.log())
	}
}

// Config path order indices: DefaultPaths[0] is the WAN the failover tests kill,
// DefaultPaths[1] the one that survives.
const (
	primaryPathIdx = 0
	backupPathIdx  = 1
)

// livenessUp / livenessDown are the "to" values of a "path liveness transition" record
// (telemetry.PathState.String()).
const (
	livenessUp   = "up"
	livenessDown = "down"
)

// TestP1FailoverRepeatedFlap is the SECOND half of the T20 acceptance that
// TestP1Failover (a single kill+restore) does not cover: "repeated flap does not
// wedge the tunnel". It runs ONE long-lived saturating bidirectional bulk flow across
// SEVERAL kill/restore cycles of one WAN and asserts both ends detect the loss every
// cycle — within P1RecoverySeconds, measured the same sound per-end way as
// TestP1Failover — that the restored WAN returns to service on both ends every cycle,
// and that the single flow survives ALL cycles with no WireGuard-session reset.
//
// Non-vacuity guard (the opus T20-r1 finding). A repeated-flap test is only meaningful
// if each cycle genuinely kills a path that is in service. So before every kill this
// test confirms — in BOTH daemons' logs — that the path's liveness is UP. For cycles
// >= 2 it waits for a FRESH to=up "path liveness transition" logged after that cycle's
// restore: that IS the genuine return to service and the anti-wedge proof for the prior
// cycle. Cycle 1 has no prior restore, so it instead reads the CURRENT liveness — the
// most-recent transition's destination — and asserts it is up.
//
// Both daemons run at INFO so those transitions are observable (as in TestP1Failover).
// The per-cycle metric line `FLAP_CYCLE=<n> RECOVERY_MS=<ms>` is grep-able for a
// pass-rate/distribution report over `-run TestP1FailoverRepeatedFlap -count=N`.
func TestP1FailoverRepeatedFlap(t *testing.T) {
	const (
		flapCycles = 3
		// flapFailoverPoll bounds how long we wait to OBSERVE both ends' detection
		// after a kill. It is set WELL ABOVE P1RecoverySeconds (not budget+1s)
		// so a heavily-late detection is still OBSERVED and MEASURED — then asserted
		// against the budget with its true magnitude via the per-cycle Errorf below —
		// rather than lost to an unmeasured non-observation Fatalf. The old budget+1s
		// (4s) window was the T20-review measurement gap: a genuine >4s recovery tail
		// fell OUTSIDE it and was reported as "never switched" (an unmeasured Fatalf)
		// instead of "switched late by N ms" (a measured, magnitude-bearing failure).
		flapFailoverPoll = time.Duration(P1RecoverySeconds)*time.Second + 5*time.Second
		// flapRestorePoll bounds the wait for both ends to mark the restored path UP
		// again: up-detect (3×200ms) + margin for the D15 under-load detection tail. If
		// the path does not return within this, the tunnel has wedged on the survivor.
		flapRestorePoll = 12 * time.Second
		flapRampBefore  = 2500 * time.Millisecond
	)

	bin := buildWanbond(t)
	top := Setup(t)
	edge, conc := setupMultipathTunnelLevel(t, top, bin, DefaultPaths, "info")

	if !top.pingUntil(concInner, 15*time.Second) {
		t.Fatalf("bond never came up\n--- edge ---\n%s\n--- conc ---\n%s", edge.log(), conc.log())
	}

	primary := DefaultPaths[primaryPathIdx]  // starlink — the WAN that is flapped
	secondary := DefaultPaths[backupPathIdx] // cellular — the surviving WAN

	// One saturating bidirectional flow spans EVERY cycle: it recreates the D15 CPU
	// load and is the data-plane-survival proof — it must finish (exit 0) with positive
	// throughput both ways, proving the one WireGuard session (hence the flow) survived
	// all the reroutes. Size its lifetime to the worst-case cycle budget so it is still
	// running throughout the loop no matter how the per-cycle waits resolve.
	loadWindow := flapRampBefore +
		time.Duration(flapCycles)*(flapFailoverPoll+flapRestorePoll+time.Second) +
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
	// Reap the saturating flow on EVERY exit path, not just the load.Wait() on success:
	// an early Fatalf (a non-observation or a wedge) before Wait would
	// otherwise leak an uncapped ~70s --bidir iperf3 that keeps saturating the shared e2e
	// host and contaminates subsequent/concurrent runs (D21). Kill after a completed Wait
	// is a harmless no-op.
	t.Cleanup(func() {
		if load.Process != nil {
			_ = load.Process.Kill()
		}
	})

	// Let the flow ramp and both ends reach steady state before cycle 1.
	time.Sleep(flapRampBefore)

	// sinceRef is the reference instant after which a to=up transition confirms the
	// path has returned to service before the next kill. It is used only for
	// cycles >= 2, where it is the PREVIOUS cycle's restore instant. Cycle 1 does not use
	// it — see the cycle-1 branch below — so its zero value is never read.
	var sinceRef time.Time

	for cycle := 1; cycle <= flapCycles; cycle++ {
		// Precondition (non-vacuity): the path must be UP on BOTH ends before we kill
		// it, so the kill genuinely hits a path in service. This is also the
		// anti-wedge assertion for the prior cycle.
		if cycle == 1 {
			// Cycle 1 cannot demand a FRESH to=up transition: the bring-up transition
			// is logged before any instant we could stamp after setup, and —
			// DefaultPaths being lossless — the path never re-flaps, so no further
			// to=up transition is ever logged. Instead assert the CURRENT liveness
			// is up by reading the most-recent transition's destination on both ends.
			if !waitBothPathCurrently(edge, conc, primary.name, livenessUp, flapRestorePoll) {
				t.Fatalf("cycle 1: path %q not up on both ends within %v at start\n--- edge ---\n%s\n--- conc ---\n%s",
					primary.name, flapRestorePoll, edge.log(), conc.log())
			}
		} else if _, _, ok := waitBothPathLiveness(edge, conc, primary.name, livenessUp, sinceRef, flapRestorePoll); !ok {
			t.Fatalf("cycle %d: path %q never returned to up on both ends within %v — tunnel wedged on the survivor after the prior cycle\n--- edge ---\n%s\n--- conc ---\n%s",
				cycle, primary.name, flapRestorePoll, edge.log(), conc.log())
		}

		// Kill the path and stamp T0 for this cycle.
		killAt := time.Now()
		top.Blackhole(primary.name)

		// Per-end detection latency, read from each daemon's to=down transition
		// after this cycle's kill — the same sound, un-confounded measurement as
		// TestP1Failover. End-to-end bidirectional recovery is the SLOWER of the two.
		edgeDown, concDown, ok := waitBothPathLiveness(edge, conc, primary.name, livenessDown, killAt, flapFailoverPoll)
		if !ok {
			top.Restore(primary.name)
			t.Fatalf("cycle %d: both ends did not mark %q down within %v (edge=%s conc=%s %s) — no liveness transition logged after the kill\n--- edge ---\n%s\n--- conc ---\n%s",
				cycle, primary.name, flapFailoverPoll, latencyStr(edgeDown), latencyStr(concDown), readLoadAvg(), edge.log(), conc.log())
		}
		recovery := edgeDown
		if concDown > recovery {
			recovery = concDown
		}
		// Record host load on the metric line: the repeated-flap tail is sensitive to
		// shared-VM CPU contention (4 vCPU, possibly multi-tenant), so every per-cycle
		// measurement carries the load that produced it — that is what lets a genuine
		// product tail be told apart from host-contention noise in the run log (D18).
		t.Logf("FLAP_CYCLE=%d RECOVERY_MS=%d budget_ms=%d edge_down_ms=%d conc_down_ms=%d %s",
			cycle, recovery.Milliseconds(), int64(P1RecoverySeconds)*1000,
			edgeDown.Milliseconds(), concDown.Milliseconds(), readLoadAvg())
		if recovery >= time.Duration(P1RecoverySeconds)*time.Second {
			t.Errorf("cycle %d: bidirectional recovery %v exceeded P1 budget %ds (edge_down=%v conc_down=%v %s)",
				cycle, recovery, P1RecoverySeconds, edgeDown, concDown, readLoadAvg())
		}

		// Restore the path; the next iteration's precondition wait confirms its return.
		restoreAt := time.Now()
		top.Restore(primary.name)
		sinceRef = restoreAt
	}

	// Final anti-wedge check: after the last restore the path must return to service
	// too (the loop's top-of-cycle check does not cover the last cycle).
	if _, _, ok := waitBothPathLiveness(edge, conc, primary.name, livenessUp, sinceRef, flapRestorePoll); !ok {
		t.Errorf("after the final cycle path %q never returned to up on both ends within %v — tunnel wedged on the survivor\n--- edge ---\n%s\n--- conc ---\n%s",
			primary.name, flapRestorePoll, edge.log(), conc.log())
	}

	// Data-plane survival across ALL cycles: the one flow that spanned every kill must
	// have completed with traffic both ways and no reset.
	loadErr := load.Wait()
	fwd, rev := iperfBidirMbps(loadOut.String())
	if loadErr != nil || fwd <= 0 || rev <= 0 {
		t.Errorf("bulk flow did not survive %d failover cycles (exit err=%v, forward=%.1f Mbit/s, reverse=%.1f Mbit/s) — a WG-session reset?\n%s",
			flapCycles, loadErr, fwd, rev, loadOut.String())
	}

	// Sanity: the surviving path is still reachable (it carried the bond during each cycle).
	if !top.Reachable(secondary.name, 3) {
		t.Errorf("surviving path %q unreachable after repeated flap", secondary.name)
	}

	t.Logf("repeated-flap: survived %d kill/restore cycles of one WAN with one spanning flow (forward=%.1f Mbit/s reverse=%.1f Mbit/s), the path returned to service each cycle",
		flapCycles, fwd, rev)
}

// livenessRecord is the subset of a "path liveness transition" slog line the failover
// tests read: its time, the per-path field, and the transition's target state.
type livenessRecord struct {
	Time time.Time `json:"time"`
	Msg  string    `json:"msg"`
	Path string    `json:"path"`
	To   string    `json:"to"`
}

// pathLivenessRecords returns every "path liveness transition" record for path in a
// daemon's JSON log, in log order.
func pathLivenessRecords(logText, path string) []livenessRecord {
	var out []livenessRecord
	for _, line := range strings.Split(logText, "\n") {
		if !strings.Contains(line, "path liveness transition") {
			continue
		}
		var rec livenessRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		if rec.Msg == "path liveness transition" && rec.Path == path {
			out = append(out, rec)
		}
	}
	return out
}

// pathLivenessLatency returns the delay from `after` to the EARLIEST liveness
// transition of path to state `to` logged strictly after `after`, or -1 if none. It is
// the per-end detection latency: the instant that end's liveness plane changed its
// verdict on the path.
func pathLivenessLatency(logText, path, to string, after time.Time) time.Duration {
	best := time.Duration(-1)
	for _, rec := range pathLivenessRecords(logText, path) {
		if rec.To != to || !rec.Time.After(after) {
			continue
		}
		d := rec.Time.Sub(after)
		if best < 0 || d < best {
			best = d
		}
	}
	return best
}

// waitBothPathLiveness polls both daemons' logs until EACH has logged a liveness
// transition of path to state `to` at some instant strictly after `after`, or the
// deadline elapses. It returns the two per-daemon latencies from `after` to that
// transition (or -1 for a daemon that never logged one) and whether both were
// observed. It is used both to MEASURE detection (to = down, from the kill instant)
// and to CONFIRM the return to service (to = up, from the restore instant) before the
// next kill.
func waitBothPathLiveness(edge, conc *proc, path, to string, after time.Time, deadline time.Duration) (edgeLat, concLat time.Duration, ok bool) {
	stop := time.Now().Add(deadline)
	for {
		edgeLat = pathLivenessLatency(edge.log(), path, to, after)
		concLat = pathLivenessLatency(conc.log(), path, to, after)
		if edgeLat >= 0 && concLat >= 0 {
			return edgeLat, concLat, true
		}
		if time.Now().After(stop) {
			return edgeLat, concLat, false
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// currentPathLiveness returns the target state of the MOST-RECENT liveness transition
// of path in a daemon's log, or "" if none has been logged. It is NOT windowed by an
// instant: it reports the path's CURRENT liveness regardless of when that transition
// was logged.
func currentPathLiveness(logText, path string) string {
	state := ""
	var latest time.Time
	for _, rec := range pathLivenessRecords(logText, path) {
		if state == "" || rec.Time.After(latest) {
			latest = rec.Time
			state = rec.To
		}
	}
	return state
}

// waitBothPathCurrently polls both daemons' logs until EACH reports path's CURRENT
// liveness (the most-recent transition's destination) is state, or the deadline
// elapses.
func waitBothPathCurrently(edge, conc *proc, path, state string, deadline time.Duration) bool {
	stop := time.Now().Add(deadline)
	for {
		if currentPathLiveness(edge.log(), path) == state && currentPathLiveness(conc.log(), path) == state {
			return true
		}
		if time.Now().After(stop) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
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
