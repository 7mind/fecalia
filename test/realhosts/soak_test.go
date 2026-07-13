//go:build realhosts

package realhosts

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// SHORT bounded soak (T64) over the bonded tunnel between the two STANDING hosts:
//   - EDGE  = llm-ubuntu-0.pgtr.7mind.io (amd64, symmetric-NAT edge)
//   - CONC  = o3.7mind.io                (aarch64, PUBLIC 89.168.124.91)
//
// This is a LONGER, SAMPLED version of T58's loaded window (aggregation_bufferbloat_test.go):
// it brings the SAME two-source-IP weighted bond up over the real internet, then runs ONE
// sustained saturating transfer for a bounded window and SAMPLES the tunnel health across
// that window — throughput (per iperf3 interval), RTT + loss (periodic ping), and path
// liveness (periodic edge-journal path-state read) — emitting the per-sample time series
// plus a summary.
//
// Duration is MINUTES, not hours (Q19: the LONG soak runs DURING the supervised pilot, not
// as a pre-gate). soakWindow is a named const so the window is easy to see/tune. It is set
// to span the WireGuard rekey timer (~120 s), so the window crosses at least one rekey and
// the "transfer completed + tunnel stayed up" result is direct evidence the control plane
// (including a rekey) stayed healthy under sustained load.
//
// REPORT-ONLY (M10/Q12): the ONLY hard assertions are liveness — (1) the sustained transfer
// COMPLETED cleanly with positive throughput (a TCP flow cannot run its full -t window and
// report positive sender throughput unless the tunnel carried data for the whole window),
// and (2) the tunnel STAYED UP for the full window (a final in-tunnel ping still succeeds and
// neither path's latest liveness state is "down"). NO absolute throughput/RTT/loss threshold
// gates the test; every sample is logged.
//
// o3 SAFETY: o3 is the SHARED concentrator and must NEVER be deprovisioned/terminated/rebooted.
// This test only starts a bounded (soakWindow) iperf3 server + client and tears both down on
// every exit path (D21: the saturating flow is time-bounded via -t = soakWindow and reaped in
// t.Cleanup; the server unit, the tunnel, the secondary addr, and the routing tables are all
// torn down by the reused startIperfServer / startDaemon / setupEdgeTwoPaths cleanups). Being a
// good citizen on the shared host is why the window is bounded to minutes.
const (
	// soakWindow is the SUSTAINED-transfer window: minutes, not hours. Chosen to exceed
	// the ~120 s WireGuard rekey cadence so the window spans at least one rekey.
	soakWindow = 150 * time.Second

	// soakSampleInterval is the cadence at which RTT/loss/path-liveness are sampled across
	// the window (soakWindow/soakSampleInterval ~= 5 samples).
	soakSampleInterval = 30 * time.Second

	// soakPingCount is the ping packet count per RTT/loss sample (a few packets so a single
	// dropped packet during a rekey does not read as 100% loss).
	soakPingCount = 5

	// soakIperfInterval is the iperf3 -i reporting cadence: the sustained flow emits one
	// throughput+retransmit interval every soakIperfInterval seconds, yielding the
	// throughput time series parsed after the flow completes.
	soakIperfInterval = 10

	// soakFlowTimeout bounds the whole SSH+iperf3 invocation of the sustained flow: the
	// window plus a generous margin for ramp/teardown.
	soakFlowTimeout = soakWindow + 90*time.Second
)

// soakSample is one health sample taken during the window: the wall-clock offset from the
// window start, the ping RTT + loss, whether the tunnel answered at all (ok), and the latest
// liveness state of each bonded path as seen in the edge daemon journal.
type soakSample struct {
	elapsed time.Duration
	rttMs   float64
	lossPct float64
	ok      bool
	primary string
	backup  string
}

// TestRealSoakShort brings the T58 weighted bond up over the real internet between the two
// standing hosts, runs ONE sustained saturating transfer for soakWindow, and samples tunnel
// health across the window. REPORT-ONLY: it asserts ONLY that the transfer completed and the
// tunnel stayed up; the throughput/RTT/loss/liveness time series is logged, not gated. Every
// remote resource it starts is torn down on every exit path via the reused t.Cleanup helpers.
func TestRealSoakShort(t *testing.T) {
	cfg := LoadConfig()
	r := NewRunner(cfg)

	t.Logf("realhosts config: edge=%s concentrator=%s conc-public-ip=%s ssh-key=%s",
		cfg.Edge.target(), cfg.Conc.target(), cfg.ConcPubIP, cfg.SSHKey)
	t.Logf("soak parameters: window=%s sample-interval=%s ping-count=%d iperf-interval=%ds",
		soakWindow, soakSampleInterval, soakPingCount, soakIperfInterval)

	// 1. Provision both hosts (concentrator also gets the tunnel-iface INPUT ACCEPT rule).
	provision(t, r, cfg.Edge, ProvisionOpts{})
	provision(t, r, cfg.Conc, ProvisionOpts{TunnelIface: tunnelIface})

	// 2. Sync + native build on each host; remove the synced repo (and its secret configs)
	//    on exit.
	root := repoRoot(t)
	syncAndBuild(t, r, cfg.Edge, root)
	t.Cleanup(func() { removeRemoteDir(t, r, cfg.Edge) })
	syncAndBuild(t, r, cfg.Conc, root)
	t.Cleanup(func() { removeRemoteDir(t, r, cfg.Conc) })

	// 3. Resolve the edge uplink, verify the scoped-blackhole assumption is SSH-safe (the
	//    two-path setup installs no blackhole here, but the same control-host guard is a cheap
	//    invariant to keep), then set up the two source-IP paths + per-path policy routing
	//    (idempotent; torn down on exit).
	plan := resolveEdgePathPlan(t, r, cfg.Edge, cfg.ConcPubIP)
	t.Logf("edge uplink: iface=%s primary=%s alt=%s gateway=%q prefix=/%d",
		plan.iface, plan.primaryIP, plan.altIP, plan.gateway, plan.prefixLen)
	assertControlHostNotConcentrator(t, r, cfg.Edge, plan.concPubIP)
	setupEdgeTwoPaths(t, r, cfg.Edge, plan)

	// 4. Key material (X25519 per end + shared PSK), same scheme as the P0 smoke.
	edgePriv, edgePub := genSmokeKey(t)
	concPriv, concPub := genSmokeKey(t)
	psk := randSmokeKey(t)

	// 5. The concentrator binds its single virtual endpoint to its primary source IP.
	concSrc := primaryIP(t, r, cfg.Conc)
	t.Logf("concentrator source addr: %s", concSrc)

	// 6. Write the 0600 configs: a plain single-path concentrator listener, and an edge that
	//    lists TWO paths pinning the two source IPs and selects the WEIGHTED scheduler with
	//    per-link pacing — identical to T58's bond (reusing the operator-declared per-link
	//    bandwidth/RTT). Both ends run at "info" so liveness transitions are journalled.
	concCfg := fmt.Sprintf(`role = "concentrator"
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
level = "info"
`, psk, mpPrimaryPathName, concSrc, concPriv, smokeListenPort, edgePub, smokeEdgeInner)

	edgeCfg := fmt.Sprintf(`role = "edge"
psk = "%s"

[[paths]]
name = "%s"
source_addr = "%s"
link_bandwidth = "%s"
link_rtt = "%s"

[[paths]]
name = "%s"
source_addr = "%s"
link_bandwidth = "%s"
link_rtt = "%s"

[scheduler]
policy = "weighted"
pacing_enabled = true

[wireguard]
private_key = "%s"

[[wireguard.peers]]
public_key = "%s"
endpoint = "%s:%d"
allowed_ips = ["%s/32"]

[log]
level = "info"
`, psk,
		mpPrimaryPathName, plan.primaryIP, aggDeclaredBandwidth, aggDeclaredRTT,
		mpBackupPathName, plan.altIP, aggDeclaredBandwidth, aggDeclaredRTT,
		edgePriv, concPub, cfg.ConcPubIP, smokeListenPort, smokeConcInner)

	concCfgPath := smokeRemoteDir + "/conc.toml"
	edgeCfgPath := smokeRemoteDir + "/edge.toml"
	writeRemoteFile(t, r, cfg.Conc, concCfgPath, concCfg)
	writeRemoteFile(t, r, cfg.Edge, edgeCfgPath, edgeCfg)

	// 7. Clear leftovers, then start the concentrator (listening first) and the edge, each as
	//    a transient unit; teardown is registered inside startDaemon and runs even on failure.
	preClean(t, r, cfg.Conc)
	preClean(t, r, cfg.Edge)
	startDaemon(t, r, cfg.Conc, concCfgPath)
	startDaemon(t, r, cfg.Edge, edgeCfgPath)

	// 8. Wait for each TUN, then address the inner /24 (edge 10.10.0.1 / conc 10.10.0.2).
	if !waitRemoteLink(t, r, cfg.Conc, tunnelIface, linkAppearTimeout) {
		dumpDaemonLog(t, r, cfg.Conc)
		t.Fatalf("concentrator %s never appeared", tunnelIface)
	}
	if !waitRemoteLink(t, r, cfg.Edge, tunnelIface, linkAppearTimeout) {
		dumpDaemonLog(t, r, cfg.Edge)
		t.Fatalf("edge %s never appeared", tunnelIface)
	}
	addressLink(t, r, cfg.Conc, smokeConcInner)
	addressLink(t, r, cfg.Edge, smokeEdgeInner)

	// 9. Drive the handshake (first ping through the tunnel establishes the WG session).
	if !pingUntil(t, r, cfg.Edge, smokeConcInner, handshakeTimeout) {
		dumpDaemonLog(t, r, cfg.Conc)
		dumpDaemonLog(t, r, cfg.Edge)
		t.Fatalf("handshake never completed: %s unreachable from the edge through the tunnel", smokeConcInner)
	}
	t.Logf("HANDSHAKE OK over the weighted bond")

	// 10. Both paths must ESTABLISH (each reaches liveness "up" after authenticated echoes on
	//     its own 4-tuple) before the soak begins, so the bond is fully up for the window.
	ups := waitBothPathsUp(t, r, cfg.Edge, []string{mpPrimaryPathName, mpBackupPathName}, mpPathUpTimeout)
	for _, name := range []string{mpPrimaryPathName, mpBackupPathName} {
		t.Logf("PATH ESTABLISHED [%s]: liveness up (silence_ms at transition = %d)", name, ups[name])
	}

	// 11. iperf3 server on the concentrator inner IP (torn down on exit).
	startIperfServer(t, r, cfg.Conc, smokeConcInner)

	// --- Bounded soak: one sustained flow + sampled health across the window --------------

	// 12. Stamp the window start on the EDGE clock (the same clock that stamps the daemon's
	//     slog), so per-path liveness samples are read against the daemon's own timeline. Then
	//     start the sustained saturating flow for the whole window (D21: bounded via -t and
	//     reaped in t.Cleanup).
	soakStart := edgeClockNow(t, r, cfg.Edge)
	flow, flowOut := startSoakFlow(t, r, cfg.Edge, smokeConcInner, soakWindow, aggParallelStreams, soakIperfInterval)

	// 13. Sample RTT + loss + path liveness across the window while the flow runs. Sampling is
	//     NON-FATAL (report-only): every sample is recorded and logged; liveness is asserted at
	//     the end from the flow outcome and a final reachability check.
	loopStart := time.Now()
	deadline := loopStart.Add(soakWindow)
	var samples []soakSample
	for time.Now().Before(deadline) {
		elapsed := time.Since(loopStart)
		rttMs, lossPct, ok := soakPingSample(t, r, cfg.Edge, smokeConcInner)
		journal := readDaemonJournal(t, r, cfg.Edge, smokeUnit)
		s := soakSample{
			elapsed: elapsed,
			rttMs:   rttMs,
			lossPct: lossPct,
			ok:      ok,
			primary: soakPathStateOrUp(journal, mpPrimaryPathName, soakStart),
			backup:  soakPathStateOrUp(journal, mpBackupPathName, soakStart),
		}
		samples = append(samples, s)
		t.Logf("SAMPLE t=%5.1fs: rtt=%.3f ms loss=%.1f%% reachable=%t paths[%s=%s %s=%s]",
			s.elapsed.Seconds(), s.rttMs, s.lossPct, s.ok,
			mpPrimaryPathName, s.primary, mpBackupPathName, s.backup)

		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		if remaining < soakSampleInterval {
			time.Sleep(remaining)
		} else {
			time.Sleep(soakSampleInterval)
		}
	}

	// 14. Await the sustained flow and parse its per-interval throughput time series.
	flowErr := flow.Wait()
	rep, parseErr := parseSoakIperfReport(flowOut.String())

	// --- Throughput time series (report-only) ---------------------------------------------
	for _, iv := range rep.Intervals {
		t.Logf("IPERF interval %5.1f-%5.1fs: %.2f Mbit/s (retransmits=%d)",
			iv.Sum.Start, iv.Sum.End, iv.Sum.BitsPerSecond/1e6, iv.Sum.Retransmits)
	}
	mbps := rep.End.SumSent.BitsPerSecond / 1e6

	// --- Sampled health summary (report-only) ---------------------------------------------
	reachable := 0
	var rttSum, lossSum float64
	flaps := 0
	for _, s := range samples {
		if s.ok {
			reachable++
		}
		rttSum += s.rttMs
		lossSum += s.lossPct
		if s.primary == "down" || s.backup == "down" {
			flaps++
		}
	}
	n := len(samples)
	avgRTT, avgLoss := 0.0, 0.0
	if n > 0 {
		avgRTT = rttSum / float64(n)
		avgLoss = lossSum / float64(n)
	}

	// 15. Final reachability probe: the tunnel must STILL be up at the end of the window.
	//     measureRTT fatals if the in-tunnel ping cannot be measured, so it doubles as the
	//     "tunnel stayed up for the full window" liveness assertion.
	finalRTT := measureRTT(t, r, cfg.Edge, smokeConcInner)
	t.Logf("FINAL in-tunnel RTT after the window: %.3f ms", finalRTT)

	// 16. Liveness assertions (report-only: ONLY these; NO throughput/RTT/loss threshold).
	//     (a) the sustained transfer COMPLETED cleanly with positive throughput; (b) neither
	//     path's latest liveness state ended "down". Any transient rekey-window flap that
	//     RECOVERED is logged (flaps) but does not gate — the acceptance is that the window
	//     completed and the tunnel is up, not that no probe ever missed.
	if flowErr != nil || parseErr != nil || mbps <= 0 {
		t.Errorf("sustained soak transfer did NOT complete over the %s window (exit err=%v, parse err=%v, %.2f Mbit/s) — tunnel did not stay up\n%s",
			soakWindow, flowErr, parseErr, mbps, flowOut.String())
	} else {
		t.Logf("TRANSFER COMPLETED: %.2f Mbit/s sustained over %s (%d intervals, retransmits=%d)",
			mbps, soakWindow, len(rep.Intervals), rep.End.SumSent.Retransmits)
	}

	finalJournal := readDaemonJournal(t, r, cfg.Edge, smokeUnit)
	for _, name := range []string{mpPrimaryPathName, mpBackupPathName} {
		if latestPathStateAfter(finalJournal, name, soakStart) == "down" {
			dumpDaemonLog(t, r, cfg.Edge)
			t.Errorf("path %q ended the window in liveness state \"down\" — the bond did not stay up", name)
		}
	}

	// Final report block (report-only; nothing below gates the test).
	t.Logf("=== REAL-HOST SHORT SOAK RESULTS ===\n"+
		"  window:            %s (spans the ~120s WG rekey cadence)\n"+
		"  scheduler:         weighted, pacing on (declared %s / %s per link)\n"+
		"  sustained flow:    %.2f Mbit/s over %d intervals (retransmits=%d)\n"+
		"  samples:           %d taken every %s\n"+
		"  reachable samples: %d/%d\n"+
		"  avg sampled RTT:   %.3f ms\n"+
		"  avg sampled loss:  %.1f%%\n"+
		"  path-down flaps:   %d (recovered — window still completed)\n"+
		"  final in-tunnel RTT: %.3f ms",
		soakWindow, aggDeclaredBandwidth, aggDeclaredRTT,
		mbps, len(rep.Intervals), rep.End.SumSent.Retransmits,
		n, soakSampleInterval, reachable, n, avgRTT, avgLoss, flaps, finalRTT)
}

// startSoakFlow starts a sustained saturating multi-stream iperf3 TCP client on the edge (to
// serverIP) that runs for the whole window (secs), reporting one interval every ivalSecs so
// its -J output carries the throughput time series. It mirrors startSpanningFlow's D21 reap
// discipline (never leak a saturating flow on the shared concentrator) but takes an explicit
// stream count and interval, and returns the running command plus its captured -J output for
// the caller to Wait on. Named startSoakFlow (not a shared helper) so it never clashes with
// the sibling startLoadFlow/startSpanningFlow when merged alongside T58/T63.
func startSoakFlow(t *testing.T, r *Runner, edge Host, serverIP string, window time.Duration, streams, ivalSecs int) (*exec.Cmd, *strings.Builder) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), soakFlowTimeout)
	secs := int(window / time.Second)
	args := fmt.Sprintf("iperf3 -c %s -t %d -P %d -i %d -J", serverIP, secs, streams, ivalSecs)
	cmd := exec.CommandContext(ctx, "ssh", r.sshArgs(edge, args)...)
	out := &strings.Builder{}
	cmd.Stdout, cmd.Stderr = out, out

	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatalf("edge: start sustained soak iperf3 flow failed: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		rctx, rcancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer rcancel()
		if _, err := r.Run(rctx, edge, "pkill -f 'iperf3 -c' 2>/dev/null; true"); err != nil {
			t.Logf("cleanup: edge: reap iperf3 client: %v", err)
		}
	})
	t.Logf("edge: sustained soak iperf3 flow started (%s, %ds, %d streams, %ds intervals)",
		serverIP, secs, streams, ivalSecs)
	return cmd, out
}

// soakLossRe extracts the loss percentage from ping's "N% packet loss" summary line.
var soakLossRe = regexp.MustCompile(`([0-9.]+)% packet loss`)

// soakPingSample runs a short ping from the edge to ip and returns the average RTT (ms), the
// packet-loss percentage, and whether the tunnel answered at all (ok = loss < 100). It is
// NON-FATAL (unlike measureRTT): a ping that fails or fully drops is recorded as a liveness
// sample (rtt=0, loss=100, ok=false) rather than aborting the soak, so the whole time series
// is collected and liveness is judged at the end. Reuses rttRe from the smoke tier.
func soakPingSample(t *testing.T, r *Runner, edge Host, ip string) (rttMs, lossPct float64, ok bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), smokeSSHTimeout)
	defer cancel()
	// A 100%-loss ping exits non-zero; capture stdout regardless so loss/rtt still parse.
	res, _ := r.Run(ctx, edge, fmt.Sprintf("ping -c %d -W 2 %s", soakPingCount, ip))
	out := res.Stdout
	lossPct = 100
	if m := soakLossRe.FindStringSubmatch(out); m != nil {
		if v, err := strconv.ParseFloat(m[1], 64); err == nil {
			lossPct = v
		}
	}
	if m := rttRe.FindStringSubmatch(out); m != nil {
		if v, err := strconv.ParseFloat(m[1], 64); err == nil {
			rttMs = v
		}
	}
	return rttMs, lossPct, lossPct < 100
}

// soakPathStateOrUp returns the path's latest liveness state seen in the journal after the
// window-start marker, or "up" when there has been NO transition since the marker (a path
// that was already up at the window start and never transitioned is still up). This turns
// latestPathStateAfter's "" (no post-marker transition) into the operationally correct "up"
// for the per-sample liveness log. Reuses latestPathStateAfter from the T58 sibling.
func soakPathStateOrUp(journal, name string, after time.Time) string {
	if s := latestPathStateAfter(journal, name, after); s != "" {
		return s
	}
	return "up"
}

// soakIperfReport is the subset of an iperf3 -J report the soak reads: the per-interval sum
// (the throughput time series) plus the end sender totals. Named distinctly from the smoke
// tier's iperfReport because that shape omits intervals[].
type soakIperfReport struct {
	Intervals []struct {
		Sum struct {
			Start         float64 `json:"start"`
			End           float64 `json:"end"`
			BitsPerSecond float64 `json:"bits_per_second"`
			Retransmits   int     `json:"retransmits"`
		} `json:"sum"`
	} `json:"intervals"`
	End struct {
		SumSent struct {
			BitsPerSecond float64 `json:"bits_per_second"`
			Retransmits   int     `json:"retransmits"`
		} `json:"sum_sent"`
	} `json:"end"`
}

// parseSoakIperfReport unmarshals an iperf3 -J soak report, returning a parse error for
// empty/garbled output (a reset flow that never emitted a JSON document).
func parseSoakIperfReport(out string) (soakIperfReport, error) {
	var rep soakIperfReport
	if strings.TrimSpace(out) == "" {
		return rep, fmt.Errorf("empty iperf3 output")
	}
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		return rep, err
	}
	return rep, nil
}
