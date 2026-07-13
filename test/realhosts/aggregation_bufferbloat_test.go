//go:build realhosts

package realhosts

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Aggregation + bufferbloat parameters for the real-host tier. This test reuses the
// P1 multipath topology (the NAT'd edge gets TWO source IPs on its single physical
// uplink, each pinned by one wanbond path, both reaching the ONE concentrator public
// endpoint — see multipath_failover_test.go) but runs the T21 WEIGHTED aggregation
// scheduler with T53 per-link pacing, and records two families of numbers the
// CPU-bound netns e2e fixture cannot produce:
//
//  1. Per-path throughput and the BONDED-vs-SUM aggregation ratio. Each path is
//     isolated in turn by SCOPED-blackholing the OTHER path's egress to the
//     concentrator (the exact mechanism multipath_failover_test uses), so all data
//     rides the surviving path and iperf3 measures that path alone; the bonded run
//     leaves both paths up. ratio = bonded / (path0 + path1).
//
//  2. Loaded RTT vs idle RTT (the bufferbloat delta) under a sustained saturating
//     transfer, with the weighted scheduler PACING the paths from the operator-
//     declared per-link bandwidth/RTT (aggDeclaredBandwidth / aggDeclaredRTT →
//     SizePacingFromBDP). Idle RTT is a no-load ping; loaded RTT is a ping taken
//     while a saturating iperf3 fills the pipe; delta = loaded − idle.
//
// CAVEAT (documented, not a defect): both edge source IPs egress the SAME physical
// uplink through the symmetric NAT, so the two logical paths share one physical
// bottleneck. The aggregation ratio therefore measures the scheduler's multiplexing
// behaviour over a shared link — it is NOT expected to approach 2.0 (true bandwidth
// aggregation would need two DISTINCT physical uplinks). The number is recorded
// as-is; per Q12/M10 this tier is REPORT-ONLY and gates nothing.
//
// The declared per-link values are OPERATOR-DECLARED inputs to the pacing sizer, not
// runtime-measured link capacities (wanbond never auto-tunes them live — Q20).
const (
	// aggDeclaredBandwidth / aggDeclaredRTT are the operator-declared per-link pacing
	// inputs written on BOTH edge paths (link_bandwidth must be all-or-nothing under
	// the weighted policy). They are conservative placeholders for a cross-region
	// internet path between the two standing hosts; they size the BDP-derived pace
	// (config.SizePacingFromBDP), nothing else, and never gate the recorded results.
	aggDeclaredBandwidth = "100Mbit"
	aggDeclaredRTT       = "60ms"

	// aggParallelStreams is the iperf3 stream count used for EVERY throughput sample
	// (bonded and each isolated path) so the aggregation ratio is apples-to-apples.
	// A saturating parallel offer also drives offered load past the weighted gate's
	// engage fraction so aggregation actually engages for the bonded sample.
	aggParallelStreams = 8

	// aggPathDownTimeout bounds the wait for a blackholed path to be marked liveness
	// "down" before an isolated measurement (the scoped blackhole stops its probe
	// echoes; the daemon then transitions it down).
	aggPathDownTimeout = 30 * time.Second

	// aggBufferbloatLoadSecs is the lifetime of the saturating flow the loaded-RTT
	// ping runs inside. It must exceed aggBufferbloatRamp + the ~10s ping window so
	// the queue stays full for the whole loaded-RTT measurement.
	aggBufferbloatLoadSecs = 20
	// aggBufferbloatRamp lets the saturating flow ramp and fill the bottleneck queue
	// before the loaded-RTT ping starts.
	aggBufferbloatRamp = 3 * time.Second
	// aggFlowTimeout bounds the whole SSH+iperf3 invocation of the saturating flow.
	aggFlowTimeout = 90 * time.Second
)

// TestRealAggregationBufferbloat brings the P1 multipath bond up over the real
// internet under the WEIGHTED aggregation scheduler with per-link pacing, then
// records (a) per-path + bonded throughput and their aggregation ratio and (b) the
// idle-vs-loaded RTT bufferbloat delta under a saturating transfer. REPORT-ONLY per
// Q12/M10: bringing the tunnel up and recording the numbers IS the acceptance; the
// only hard assertions are liveness (handshake completed, both paths reached "up",
// every iperf3 sample returned a positive rate, both pings parsed). NO Mbit/s or ms
// threshold gates it. Every host mutation (secondary address, ip rules/tables,
// systemd units, iperf3 server, the saturating flow, scoped blackholes) is torn down
// idempotently on every exit path via t.Cleanup, mirroring the smoke and multipath
// siblings; D21 is honoured — every iperf3 client is time-bounded and reaped.
func TestRealAggregationBufferbloat(t *testing.T) {
	cfg := LoadConfig()
	r := NewRunner(cfg)

	t.Logf("realhosts config: edge=%s concentrator=%s conc-public-ip=%s ssh-key=%s",
		cfg.Edge.target(), cfg.Conc.target(), cfg.ConcPubIP, cfg.SSHKey)

	// 1. Provision both hosts (concentrator additionally gets the tunnel-iface INPUT
	//    ACCEPT rule, or TCP-through-tunnel is REJECTed on OCI).
	provision(t, r, cfg.Edge, ProvisionOpts{})
	provision(t, r, cfg.Conc, ProvisionOpts{TunnelIface: tunnelIface})

	// 2. Sync + native build on each host; remove the synced repo (and its secret
	//    configs) on exit.
	root := repoRoot(t)
	syncAndBuild(t, r, cfg.Edge, root)
	t.Cleanup(func() { removeRemoteDir(t, r, cfg.Edge) })
	syncAndBuild(t, r, cfg.Conc, root)
	t.Cleanup(func() { removeRemoteDir(t, r, cfg.Conc) })

	// 3. Resolve the edge uplink, verify the scoped blackhole is SSH-safe, then set up
	//    the two source-IP paths + per-path policy routing (idempotent; torn down on exit).
	plan := resolveEdgePathPlan(t, r, cfg.Edge, cfg.ConcPubIP)
	t.Logf("edge uplink: iface=%s primary=%s alt=%s gateway=%q prefix=/%d",
		plan.iface, plan.primaryIP, plan.altIP, plan.gateway, plan.prefixLen)
	assertControlHostNotConcentrator(t, r, cfg.Edge, plan.concPubIP)
	setupEdgeTwoPaths(t, r, cfg.Edge, plan)

	// 4. Key material (X25519 per end + shared PSK), same scheme as the P0 smoke.
	edgePriv, edgePub := genSmokeKey(t)
	concPriv, concPub := genSmokeKey(t)
	psk := randSmokeKey(t)

	// 5. The concentrator binds its single virtual endpoint to its primary source IP;
	//    it learns both edge 4-tuples from authenticated traffic (single-path conc).
	concSrc := primaryIP(t, r, cfg.Conc)
	t.Logf("concentrator source addr: %s", concSrc)

	// 6. Write the 0600 configs. The concentrator is a plain single-path listener
	//    (the scheduler is send-side, so only the EDGE carries the weighted policy).
	//    The edge lists TWO paths pinning the two source IPs, each declaring the
	//    operator per-link bandwidth/RTT, and selects the weighted scheduler with
	//    pacing enabled (T53: link_bandwidth/link_rtt → SizePacingFromBDP → paced
	//    weighted policy). Both ends run at "info" so liveness/scheduler transitions
	//    are journalled.
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
	t.Logf("edge scheduler: policy=weighted pacing_enabled=true; per-link (operator-declared) bandwidth=%s rtt=%s on %s+%s",
		aggDeclaredBandwidth, aggDeclaredRTT, mpPrimaryPathName, mpBackupPathName)

	concCfgPath := smokeRemoteDir + "/conc.toml"
	edgeCfgPath := smokeRemoteDir + "/edge.toml"
	writeRemoteFile(t, r, cfg.Conc, concCfgPath, concCfg)
	writeRemoteFile(t, r, cfg.Edge, edgeCfgPath, edgeCfg)

	// 7. Clear leftovers, then start the concentrator (listening first) and the edge,
	//    each as a transient unit; teardown is registered inside startDaemon.
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

	// 10. Both paths must ESTABLISH (each reaches liveness "up" only after authenticated
	//     echoes on its own source-IP 4-tuple), so "both up" is the "traffic observed on
	//     both paths" evidence for the bonded sample.
	ups := waitBothPathsUp(t, r, cfg.Edge, []string{mpPrimaryPathName, mpBackupPathName}, mpPathUpTimeout)
	for _, name := range []string{mpPrimaryPathName, mpBackupPathName} {
		t.Logf("PATH ESTABLISHED [%s]: liveness up (silence_ms at transition = %d)", name, ups[name])
	}

	// 11. iperf3 server on the concentrator inner IP (torn down on exit).
	startIperfServer(t, r, cfg.Conc, smokeConcInner)

	// --- Throughput aggregation ---------------------------------------------------

	// 12a. Bonded: both paths up, weighted aggregation engaged under the saturating offer.
	bonded := iperfTCP(t, r, cfg.Edge, smokeConcInner, aggParallelStreams)
	t.Logf("BONDED throughput (%s+%s, %d streams): %.2f Mbit/s (retransmits=%d)",
		mpPrimaryPathName, mpBackupPathName, aggParallelStreams, bonded.mbps, bonded.retransmits)

	// 12b. Primary alone: blackhole the BACKUP path's egress (its source table) so all
	//      data rides the primary; wait for the backup to be marked down, measure, restore.
	path0 := isolatedPathThroughput(t, r, cfg.Edge, plan, mpBackupPathName, mpBackupTable, mpPrimaryPathName)
	t.Logf("PER-PATH throughput [%s] (backup blackholed): %.2f Mbit/s (retransmits=%d)",
		mpPrimaryPathName, path0.mbps, path0.retransmits)

	// 12c. Backup alone: blackhole the PRIMARY path's egress so all data rides the backup.
	path1 := isolatedPathThroughput(t, r, cfg.Edge, plan, mpPrimaryPathName, mpPrimaryTable, mpBackupPathName)
	t.Logf("PER-PATH throughput [%s] (primary blackholed): %.2f Mbit/s (retransmits=%d)",
		mpBackupPathName, path1.mbps, path1.retransmits)

	// 12d. Aggregation ratio (report-only). NOTE the shared-physical-uplink caveat above:
	//      a ratio near/below 1.0 is EXPECTED here and is not a defect.
	sumMbps := path0.mbps + path1.mbps
	ratio := 0.0
	if sumMbps > 0 {
		ratio = bonded.mbps / sumMbps
	}
	t.Logf("AGGREGATION: bonded=%.2f  sum(%.2f+%.2f)=%.2f  ratio(bonded/sum)=%.3f  [shared physical uplink — ratio ~<=1 expected]",
		bonded.mbps, path0.mbps, path1.mbps, sumMbps, ratio)

	// Liveness assertions for the throughput family: every sample must be a positive rate.
	if bonded.mbps <= 0 {
		t.Errorf("bonded throughput non-positive (%.2f Mbit/s) — bond did not carry data", bonded.mbps)
	}
	if path0.mbps <= 0 {
		t.Errorf("per-path throughput [%s] non-positive (%.2f Mbit/s)", mpPrimaryPathName, path0.mbps)
	}
	if path1.mbps <= 0 {
		t.Errorf("per-path throughput [%s] non-positive (%.2f Mbit/s)", mpBackupPathName, path1.mbps)
	}

	// --- Bufferbloat: idle vs loaded RTT ------------------------------------------

	// 13. Both paths are up again (restored by isolatedPathThroughput). Measure idle
	//     RTT (no load), then loaded RTT during a saturating flow; delta is the
	//     bufferbloat under load. The saturating flow is time-bounded and reaped (D21).
	idleRTT := measureRTT(t, r, cfg.Edge, smokeConcInner)
	t.Logf("IDLE RTT (no load): %.3f ms", idleRTT)

	flow := startLoadFlow(t, r, cfg.Edge, smokeConcInner, aggBufferbloatLoadSecs, aggParallelStreams)
	time.Sleep(aggBufferbloatRamp)
	loadedRTT := measureRTT(t, r, cfg.Edge, smokeConcInner)
	_ = flow.Wait() // let the bounded saturating flow drain (report-only; its rate is not recorded here)

	bufferbloatMs := loadedRTT - idleRTT
	t.Logf("LOADED RTT (under saturating %d-stream flow): %.3f ms; BUFFERBLOAT delta = %.3f ms",
		aggParallelStreams, loadedRTT, bufferbloatMs)

	// Liveness assertions for the bufferbloat family: both pings must have parsed to a
	// positive RTT (measureRTT already fatals on an unparseable/failed ping).
	if idleRTT <= 0 || loadedRTT <= 0 {
		t.Errorf("non-positive RTT (idle=%.3f loaded=%.3f ms) — ping did not measure", idleRTT, loadedRTT)
	}

	// Final report block (report-only; nothing below gates the test).
	t.Logf("=== REAL-HOST AGGREGATION + BUFFERBLOAT RESULTS ===\n"+
		"  scheduler:        weighted, pacing on (declared %s / %s per link)\n"+
		"  per-path %s:     %.2f Mbit/s\n"+
		"  per-path %s:     %.2f Mbit/s\n"+
		"  sum of paths:     %.2f Mbit/s\n"+
		"  bonded:           %.2f Mbit/s\n"+
		"  aggregation ratio: %.3f  [shared physical uplink]\n"+
		"  idle RTT:         %.3f ms\n"+
		"  loaded RTT:       %.3f ms\n"+
		"  bufferbloat delta: %.3f ms",
		aggDeclaredBandwidth, aggDeclaredRTT,
		mpPrimaryPathName, path0.mbps,
		mpBackupPathName, path1.mbps,
		sumMbps, bonded.mbps, ratio,
		idleRTT, loadedRTT, bufferbloatMs)
}

// isolatedPathThroughput measures the throughput of a single path in isolation by
// scoped-blackholing the OTHER path's egress to the concentrator (blackHoleTable is
// that path's source-routing table), waiting until the blackholed path is marked
// liveness "down" so no data leaks onto it, then running a saturating iperf3 over the
// surviving path (measurePath, for logging only). It restores the blackhole and waits
// for the blackholed path to return to "up" before returning, so the caller is left
// with BOTH paths up. All timestamps compare against the EDGE clock (the same clock
// that stamps the daemon's slog), matching multipath_failover_test's discipline.
func isolatedPathThroughput(t *testing.T, r *Runner, edge Host, plan edgePathPlan, blackHolePath string, blackHoleTable int, measurePath string) tcpMeasurement {
	t.Helper()

	downMarker := edgeClockNow(t, r, edge)
	blackholeEdgePath(t, r, edge, plan, blackHoleTable)
	waitPathState(t, r, edge, blackHolePath, "down", downMarker, aggPathDownTimeout)
	t.Logf("isolated %s: %s egress blackholed (table %d) and marked down", measurePath, blackHolePath, blackHoleTable)

	m := iperfTCP(t, r, edge, smokeConcInner, aggParallelStreams)

	restoreEdgePath(t, r, edge, plan, blackHoleTable)
	upMarker := edgeClockNow(t, r, edge)
	waitPathState(t, r, edge, blackHolePath, "up", upMarker, mpPathUpTimeout)
	t.Logf("isolated %s: %s egress restored and back up", measurePath, blackHolePath)
	return m
}

// aggLivenessRecord is the subset of a "path liveness transition" slog line this test
// reads (carrying the emit time so the LATEST post-marker transition wins).
type aggLivenessRecord struct {
	Time time.Time `json:"time"`
	Msg  string    `json:"msg"`
	Path string    `json:"path"`
	To   string    `json:"to"`
}

// latestPathStateAfter returns the "to" state of the LAST "path liveness transition"
// for name logged strictly after `after`, or "" if none. Taking the latest (not the
// first) transition lets a caller observe the CURRENT state across a down→up cycle,
// which pathUpSilences (first-up-per-path) deliberately cannot.
func latestPathStateAfter(journal, name string, after time.Time) string {
	state := ""
	var best time.Time
	for _, line := range strings.Split(journal, "\n") {
		if !strings.Contains(line, "path liveness transition") {
			continue
		}
		var rec aggLivenessRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		if rec.Msg != "path liveness transition" || rec.Path != name || !rec.Time.After(after) {
			continue
		}
		if state == "" || rec.Time.After(best) {
			best, state = rec.Time, rec.To
		}
	}
	return state
}

// waitPathState polls the edge daemon journal until name's latest liveness transition
// after `after` reaches want ("up"/"down"), or the deadline elapses (fatal — a path
// that will not change state under the blackhole/restore breaks the isolation the
// per-path measurement depends on).
func waitPathState(t *testing.T, r *Runner, host Host, name, want string, after time.Time, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		journal := readDaemonJournal(t, r, host, smokeUnit)
		if latestPathStateAfter(journal, name, after) == want {
			return
		}
		if time.Now().After(deadline) {
			dumpDaemonLog(t, r, host)
			t.Fatalf("path %q did not reach liveness %q within %v after %s", name, want, d, after.Format(time.RFC3339Nano))
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// startLoadFlow starts a saturating multi-stream iperf3 TCP client on the edge (to
// serverIP) for secs seconds and returns the running command for the caller to Wait
// on. It mirrors startSpanningFlow's D21 reap discipline (never leak a saturating
// flow on the shared concentrator) but takes an explicit stream count so the flow can
// SATURATE the pipe for the loaded-RTT measurement. The report is discarded (only the
// loaded-vs-idle RTT delta is of interest here), so its output is not returned.
func startLoadFlow(t *testing.T, r *Runner, edge Host, serverIP string, secs, streams int) *exec.Cmd {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), aggFlowTimeout)
	args := fmt.Sprintf("iperf3 -c %s -t %d -P %d -J", serverIP, secs, streams)
	cmd := exec.CommandContext(ctx, "ssh", r.sshArgs(edge, args)...)
	var out strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &out

	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatalf("edge: start saturating iperf3 flow failed: %v", err)
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
	t.Logf("edge: saturating iperf3 flow started (%s, %ds, %d streams)", serverIP, secs, streams)
	return cmd
}
