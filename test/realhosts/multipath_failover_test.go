//go:build realhosts

package realhosts

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// P1 multipath/failover parameters for the real-host tier. The edge (behind a
// symmetric NAT, one physical uplink) is given TWO source IPs — its primary plus
// a secondary address on the same interface — and each wanbond path pins one of
// them (bind.selectDeviceBinds source-IP-binds a multi-address interface), so the
// two paths present two distinct 4-tuples through the NAT to the ONE concentrator
// public endpoint. The concentrator runs a SINGLE virtual endpoint that reflects
// each path's probe to the source it arrived from and learns the surviving edge
// endpoint from authenticated traffic, so egress failover is measured EDGE-side
// (the concentrator has one path and never logs a scheduler transition).
const (
	// mpPrimaryPathName / mpBackupPathName are the two edge path names, in
	// active-backup priority order: index 0 (primary) carries all DATA, index 1
	// (backup) carries only probes until the primary fails.
	mpPrimaryPathName = "wan0"
	mpBackupPathName  = "wan1"

	// mpPrimaryPathIdx / mpBackupPathIdx mirror the config path order in the
	// scheduler's priority-ordered health slice; a "scheduler active path change"
	// with to==mpBackupPathIdx is the failover this test measures.
	mpPrimaryPathIdx = 0
	mpBackupPathIdx  = 1

	// Policy-routing tables and rule preferences for the two edge source IPs. Each
	// path's source IP is routed via its own table so the ACTIVE path's egress can
	// be blackholed in isolation (replace its table's default with a blackhole)
	// without touching the backup — the real-host analogue of the netns Blackhole.
	mpPrimaryTable    = 5210
	mpBackupTable     = 5211
	mpPrimaryRulePref = 5210
	mpBackupRulePref  = 5211

	// mpLoadSeconds is the lifetime of the long-lived TCP transfer that spans the
	// kill: long enough to ramp, survive the failover, and keep flowing on the
	// backup afterwards so its survival is proven by a positive final throughput.
	mpLoadSeconds = 30
	// mpRampBefore lets the flow ramp and both ends settle on the primary before
	// the kill.
	mpRampBefore = 3 * time.Second
	// mpPathUpTimeout bounds the wait for BOTH edge paths to reach liveness "up"
	// (each needs telemetry.DefaultUpSuccesses authenticated echoes on its own
	// 4-tuple — the "traffic observed on both paths" evidence).
	mpPathUpTimeout = 40 * time.Second
	// mpFlowTimeout bounds the whole SSH+iperf3 invocation of the spanning flow.
	mpFlowTimeout = 90 * time.Second

	// envEdgeAltIP overrides the derived secondary edge source IP. Set it to an IP
	// the edge's uplink subnet/NAT is known to SNAT (e.g. a second address assigned
	// in the cloud console) when the derived candidate is not free or not routable.
	envEdgeAltIP = "WANBOND_EDGE_ALT_IP"
	// mpAltHostOffset is added (mod the host range) to the primary's last octet to
	// derive the default secondary IP when envEdgeAltIP is unset.
	mpAltHostOffset = 64
)

// edgePathPlan is the resolved edge uplink topology used to set up the two
// source-IP paths and their policy routing.
type edgePathPlan struct {
	iface     string // physical uplink device
	gateway   string // default-route gateway ("" when the uplink is on-link)
	prefixLen int    // uplink subnet prefix length
	primaryIP string // the edge's primary source IP (path 0)
	altIP     string // the secondary source IP added for path 1
}

// TestRealMultipathFailover is the T34 real-host multipath/failover validation. It
// gives the NAT'd edge TWO paths to the ONE concentrator over its single physical
// uplink (two source IPs + policy routing), brings the P1 multipath bond up over
// the real internet, confirms BOTH paths establish (both reach liveness "up", the
// "traffic observed on both paths" telemetry), then blackholes the ACTIVE path's
// egress mid-flow and confirms a long-lived TCP transfer survives — recording the
// edge-side failover time. REPORT-ONLY per Q12: executing and recording IS the
// acceptance; it gates nothing. A slow-but-recovered failover is recorded, not
// failed; only a genuine wedge (the flow does not survive) is an error. Every host
// mutation (secondary address, ip rules, routing tables, systemd units, iperf3,
// the spanning flow) is torn down idempotently on every exit path via t.Cleanup.
func TestRealMultipathFailover(t *testing.T) {
	cfg := LoadConfig()
	r := NewRunner(cfg)

	t.Logf("realhosts config: edge=%s concentrator=%s conc-public-ip=%s ssh-key=%s",
		cfg.Edge.target(), cfg.Conc.target(), cfg.ConcPubIP, cfg.SSHKey)

	// 1. Provision both hosts; the concentrator additionally gets the tunnel-iface
	//    INPUT ACCEPT rule (or TCP-through-tunnel is REJECTed on OCI).
	provision(t, r, cfg.Edge, ProvisionOpts{})
	provision(t, r, cfg.Conc, ProvisionOpts{TunnelIface: tunnelIface})

	// 2. Sync + native build on each host; remove the synced repo (and the secret
	//    configs written into it) on exit.
	root := repoRoot(t)
	syncAndBuild(t, r, cfg.Edge, root)
	t.Cleanup(func() { removeRemoteDir(t, r, cfg.Edge) })
	syncAndBuild(t, r, cfg.Conc, root)
	t.Cleanup(func() { removeRemoteDir(t, r, cfg.Conc) })

	// 3. Resolve the edge uplink and set up the two source-IP paths + per-path
	//    policy routing (idempotent; torn down on exit).
	plan := resolveEdgePathPlan(t, r, cfg.Edge, cfg.ConcPubIP)
	t.Logf("edge uplink: iface=%s primary=%s alt=%s gateway=%q prefix=/%d",
		plan.iface, plan.primaryIP, plan.altIP, plan.gateway, plan.prefixLen)
	setupEdgeTwoPaths(t, r, cfg.Edge, plan)

	// 4. Key material (X25519 per end + shared PSK), same scheme as the P0 smoke.
	edgePriv, edgePub := genSmokeKey(t)
	concPriv, concPub := genSmokeKey(t)
	psk := randSmokeKey(t)

	// 5. The concentrator binds its single virtual endpoint to its primary source IP.
	concSrc := primaryIP(t, r, cfg.Conc)
	t.Logf("concentrator source addr: %s", concSrc)

	// 6. Write the 0600 configs. The edge lists TWO paths pinning the two source IPs;
	//    neither carries dest_addr, so both reuse the wireguard peer endpoint (the
	//    single concentrator public IP) — the source-routed multipath case. Both ends
	//    run at "info" so the scheduler/liveness transitions are journalled.
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
level = "info"
`, psk, mpPrimaryPathName, plan.primaryIP, mpBackupPathName, plan.altIP,
		edgePriv, concPub, cfg.ConcPubIP, smokeListenPort, smokeConcInner)

	concCfgPath := smokeRemoteDir + "/conc.toml"
	edgeCfgPath := smokeRemoteDir + "/edge.toml"
	writeRemoteFile(t, r, cfg.Conc, concCfgPath, concCfg)
	writeRemoteFile(t, r, cfg.Edge, edgeCfgPath, edgeCfg)

	// 7. Clear any leftover unit/interface, then start the concentrator (listening
	//    first) and the edge, each as a transient unit; teardown is registered inside
	//    startDaemon and runs even on failure.
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

	// 9. Drive the handshake: the first ping through the tunnel establishes the WG
	//    session over the active path.
	if !pingUntil(t, r, cfg.Edge, smokeConcInner, handshakeTimeout) {
		dumpDaemonLog(t, r, cfg.Conc)
		dumpDaemonLog(t, r, cfg.Edge)
		t.Fatalf("handshake never completed: %s unreachable from the edge through the tunnel", smokeConcInner)
	}
	t.Logf("HANDSHAKE OK over the active path")

	// 10. Both paths must ESTABLISH: each reaches liveness "up" only after
	//     DefaultUpSuccesses authenticated probe echoes on its OWN source-IP 4-tuple,
	//     so "both up" IS the "traffic observed on both paths" evidence. Record each
	//     path's up transition (telemetry).
	ups := waitBothPathsUp(t, r, cfg.Edge, []string{mpPrimaryPathName, mpBackupPathName}, mpPathUpTimeout)
	for _, name := range []string{mpPrimaryPathName, mpBackupPathName} {
		t.Logf("PATH ESTABLISHED [%s]: liveness up (silence_ms at transition = %d)", name, ups[name])
	}

	// 11. Start the iperf3 server (bound to the concentrator inner IP) and a
	//     long-lived TCP client from the edge that will SPAN the kill. Its reap is
	//     registered immediately (D21: never leak a saturating flow on the shared host).
	startIperfServer(t, r, cfg.Conc, smokeConcInner)
	flow, flowOut := startSpanningFlow(t, r, cfg.Edge, smokeConcInner, mpLoadSeconds)

	// 12. Let the flow ramp and both ends settle on the primary, then blackhole the
	//     ACTIVE path's egress and stamp T0.
	time.Sleep(mpRampBefore)
	killAt := time.Now()
	blackholeEdgePath(t, r, cfg.Edge, mpPrimaryTable)
	t.Logf("blackholed ACTIVE path %q (table %d) at T0", mpPrimaryPathName, mpPrimaryTable)

	// 13. Await the spanning flow, then restore the primary's egress (so the next run
	//     starts clean; the teardown also restores it defensively).
	flowErr := flow.Wait()
	restoreEdgePath(t, r, cfg.Edge, plan, mpPrimaryTable)

	// 14. Edge-side failover latency: the earliest "scheduler active path change" to
	//     the backup index logged after the kill. The reroute is a sub-ms update, so
	//     the transition timestamp IS the recovery instant.
	journal := readDaemonJournal(t, r, cfg.Edge, smokeUnit)
	failover := schedulerSwitchAfter(journal, killAt, mpBackupPathIdx)
	if failover < 0 {
		dumpDaemonLog(t, r, cfg.Edge)
		t.Errorf("no edge scheduler transition to the backup (idx %d) logged after the kill — did the active path actually go down?", mpBackupPathIdx)
	} else {
		t.Logf("FAILOVER_MS=%d (edge egress %q[%d] -> %q[%d])",
			failover.Milliseconds(), mpPrimaryPathName, mpPrimaryPathIdx, mpBackupPathName, mpBackupPathIdx)
	}

	// 15. Data-plane survival: the spanning TCP flow must have completed with positive
	//     throughput and no reset. A genuine wedge/reset (non-zero exit or zero
	//     throughput) is an ERROR; a slow-but-recovered failover was already RECORDED
	//     above (report-only — timing never gates).
	rep, parseErr := parseIperfReport(flowOut.String())
	mbps := rep.End.SumSent.BitsPerSecond / 1e6
	if flowErr != nil || parseErr != nil || mbps <= 0 {
		t.Errorf("spanning TCP flow did not survive failover (exit err=%v, parse err=%v, %.2f Mbit/s) — a WG-session reset?\n%s",
			flowErr, parseErr, mbps, flowOut.String())
	} else {
		t.Logf("FLOW SURVIVED: %.2f Mbit/s over %ds spanning the kill (retransmits=%d)",
			mbps, mpLoadSeconds, rep.End.SumSent.Retransmits)
	}

	// Final report block (report-only; nothing below gates the test).
	t.Logf("=== REAL-HOST MULTIPATH/FAILOVER RESULTS ===\n"+
		"  paths established:  %s(up) + %s(up)  [distinct source IPs %s / %s through the NAT]\n"+
		"  active-path kill:   %s egress blackholed mid-flow\n"+
		"  edge failover:      %s\n"+
		"  spanning TCP flow:  %.2f Mbit/s (survived=%t)",
		mpPrimaryPathName, mpBackupPathName, plan.primaryIP, plan.altIP,
		mpPrimaryPathName, latencyMs(failover), mbps, flowErr == nil && parseErr == nil && mbps > 0)
}

// resolveEdgePathPlan probes the edge for its uplink device, primary source IP,
// default gateway, and subnet prefix (all toward the concentrator's public IP),
// and picks the secondary source IP (envEdgeAltIP or derived from the primary).
func resolveEdgePathPlan(t *testing.T, r *Runner, edge Host, concPubIP string) edgePathPlan {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), smokeSSHTimeout)
	defer cancel()

	// One round-trip returns "IFACE SRC GW PREFIX" toward the concentrator.
	probe := fmt.Sprintf(`set -e
CONC=%s
IFACE=$(ip route get "$CONC" | sed -n 's/.* dev \([^ ]*\).*/\1/p' | head -1)
SRC=$(ip route get "$CONC" | sed -n 's/.*src \([0-9.]*\).*/\1/p' | head -1)
GW=$(ip route show default | sed -n 's/^default via \([0-9.]*\).*/\1/p' | head -1)
PREFIX=$(ip -o -f inet addr show dev "$IFACE" | sed -n "s#.*inet $SRC/\([0-9]*\).*#\1#p" | head -1)
echo "$IFACE $SRC $GW $PREFIX"`, concPubIP)
	res, err := r.Run(ctx, edge, probe)
	if err != nil {
		t.Fatalf("edge: uplink probe failed: %v", err)
	}
	fields := strings.Fields(res.Stdout)
	if len(fields) < 4 {
		t.Fatalf("edge: could not parse uplink topology from %q (want 'IFACE SRC GW PREFIX')", res.Stdout)
	}
	iface, src, gw, prefixStr := fields[0], fields[1], fields[2], fields[3]
	if iface == "" || src == "" {
		t.Fatalf("edge: empty uplink iface (%q) or src (%q)", iface, src)
	}
	prefixLen, err := strconv.Atoi(prefixStr)
	if err != nil || prefixLen < 1 || prefixLen > 32 {
		t.Fatalf("edge: invalid uplink prefix %q: %v", prefixStr, err)
	}

	return edgePathPlan{
		iface:     iface,
		gateway:   gw,
		prefixLen: prefixLen,
		primaryIP: src,
		altIP:     deriveEdgeAltIP(t, src),
	}
}

// deriveEdgeAltIP returns the secondary edge source IP: envEdgeAltIP verbatim if
// set, else the primary with its last octet shifted by mpAltHostOffset (kept in
// the [2,253] host range and distinct from the primary). The derived candidate is
// a BEST-EFFORT guess — the orchestrator must ensure it is free on the subnet and
// SNAT'd by the edge's NAT (see the package assumptions), or override it.
func deriveEdgeAltIP(t *testing.T, primary string) string {
	t.Helper()
	if v := strings.TrimSpace(os.Getenv(envEdgeAltIP)); v != "" {
		if _, err := netip.ParseAddr(v); err != nil {
			t.Fatalf("%s=%q is not a valid IP: %v", envEdgeAltIP, v, err)
		}
		return v
	}
	addr, err := netip.ParseAddr(primary)
	if err != nil || !addr.Is4() {
		t.Fatalf("edge: cannot derive a secondary IP from primary %q (need an IPv4 address); set %s", primary, envEdgeAltIP)
	}
	b := addr.As4()
	last := int(b[3])
	// Map into [2,253] so the candidate is never the network (.0), broadcast (.255),
	// or the usual gateway (.1); shift, then bump once if it lands on the primary.
	cand := 2 + ((last - 2 + mpAltHostOffset + 252) % 252)
	if cand == last {
		cand = 2 + ((last - 2 + mpAltHostOffset + 253) % 252)
	}
	b[3] = byte(cand)
	return netip.AddrFrom4(b).String()
}

// defaultRoute renders the default-route body for a path table: "via GW dev IFACE"
// when a gateway exists, or the on-link "dev IFACE" otherwise.
func (p edgePathPlan) defaultRoute() string {
	if p.gateway != "" {
		return fmt.Sprintf("via %s dev %s", p.gateway, p.iface)
	}
	return "dev " + p.iface
}

// setupEdgeTwoPaths adds the secondary source address to the uplink and installs a
// per-source-IP policy-routing table for each path (primary + alt), so each path's
// egress can be blackholed independently later. Idempotent (addr replace, rule
// del-then-add, route replace) and fully torn down via a t.Cleanup registered here.
func setupEdgeTwoPaths(t *testing.T, r *Runner, edge Host, plan edgePathPlan) {
	t.Helper()

	// Teardown FIRST (registered before any mutation) so a partial setup still cleans
	// up. Restores both tables' defaults, drops the rules+tables, and removes the
	// secondary address. Best-effort: logs, never fails the test.
	t.Cleanup(func() {
		teardown := strings.Join([]string{
			delRule(plan.primaryIP, mpPrimaryTable, mpPrimaryRulePref),
			delRule(plan.altIP, mpBackupTable, mpBackupRulePref),
			fmt.Sprintf("sudo ip route flush table %d 2>/dev/null", mpPrimaryTable),
			fmt.Sprintf("sudo ip route flush table %d 2>/dev/null", mpBackupTable),
			fmt.Sprintf("sudo ip addr del %s/%d dev %s 2>/dev/null", plan.altIP, plan.prefixLen, plan.iface),
			"true",
		}, "; ")
		ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cancel()
		if _, err := r.Run(ctx, edge, teardown); err != nil {
			t.Logf("cleanup: edge: two-path teardown: %v", err)
		}
	})

	setup := strings.Join([]string{
		// Secondary source address on the same uplink.
		fmt.Sprintf("sudo ip addr replace %s/%d dev %s", plan.altIP, plan.prefixLen, plan.iface),
		// Primary path table + rule.
		addRule(plan.primaryIP, mpPrimaryTable, mpPrimaryRulePref),
		fmt.Sprintf("sudo ip route replace default %s table %d", plan.defaultRoute(), mpPrimaryTable),
		// Backup path table + rule.
		addRule(plan.altIP, mpBackupTable, mpBackupRulePref),
		fmt.Sprintf("sudo ip route replace default %s table %d", plan.defaultRoute(), mpBackupTable),
	}, " && ")

	ctx, cancel := context.WithTimeout(context.Background(), smokeSSHTimeout)
	defer cancel()
	if _, err := r.Run(ctx, edge, setup); err != nil {
		t.Fatalf("edge: two-path setup failed: %v", err)
	}
	t.Logf("edge: two-path routing up (primary %s -> table %d, backup %s -> table %d)",
		plan.primaryIP, mpPrimaryTable, plan.altIP, mpBackupTable)
}

// addRule renders an idempotent `ip rule add from <ip> lookup <table>` (delete any
// prior identical rule first so a re-run never stacks duplicates).
func addRule(ip string, table, pref int) string {
	return fmt.Sprintf("sudo ip rule del from %s lookup %d pref %d 2>/dev/null; sudo ip rule add from %s lookup %d pref %d",
		ip, table, pref, ip, table, pref)
}

// delRule renders a best-effort `ip rule del from <ip> lookup <table>`.
func delRule(ip string, table, pref int) string {
	return fmt.Sprintf("sudo ip rule del from %s lookup %d pref %d 2>/dev/null", ip, table, pref)
}

// blackholeEdgePath replaces the given path table's default route with a blackhole,
// dropping all egress sourced from that path's IP (its probes stop echoing -> the
// edge marks the path down -> the scheduler fails over). Fatal on SSH error.
func blackholeEdgePath(t *testing.T, r *Runner, edge Host, table int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), smokeSSHTimeout)
	defer cancel()
	if _, err := r.Run(ctx, edge, fmt.Sprintf("sudo ip route replace blackhole default table %d", table)); err != nil {
		t.Fatalf("edge: blackhole table %d failed: %v", table, err)
	}
}

// restoreEdgePath restores a blackholed path table's normal default route.
// Best-effort: logs, never fails the test (the teardown restores it too).
func restoreEdgePath(t *testing.T, r *Runner, edge Host, plan edgePathPlan, table int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), smokeSSHTimeout)
	defer cancel()
	if _, err := r.Run(ctx, edge, fmt.Sprintf("sudo ip route replace default %s table %d", plan.defaultRoute(), table)); err != nil {
		t.Logf("edge: restore table %d: %v", table, err)
	}
}

// startSpanningFlow starts a single-stream iperf3 TCP client on the edge (to
// serverIP) that runs for secs seconds — long enough to span the kill — capturing
// its -J report. It registers reap of BOTH the local ssh process and any remote
// iperf3 client IMMEDIATELY (D21: never leak a saturating flow on the shared host),
// and returns the running command plus its captured output for the caller to Wait
// on after the kill.
func startSpanningFlow(t *testing.T, r *Runner, edge Host, serverIP string, secs int) (*exec.Cmd, *strings.Builder) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), mpFlowTimeout)
	args := fmt.Sprintf("iperf3 -c %s -t %d -J", serverIP, secs)
	cmd := exec.CommandContext(ctx, "ssh", r.sshArgs(edge, args)...)
	out := &strings.Builder{}
	cmd.Stdout, cmd.Stderr = out, out

	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatalf("edge: start spanning iperf3 flow failed: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		// Reap any remote iperf3 client left behind (best-effort).
		rctx, rcancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer rcancel()
		if _, err := r.Run(rctx, edge, "pkill -f 'iperf3 -c' 2>/dev/null; true"); err != nil {
			t.Logf("cleanup: edge: reap iperf3 client: %v", err)
		}
	})
	t.Logf("edge: spanning iperf3 flow started (%s, %ds)", serverIP, secs)
	return cmd, out
}

// readDaemonJournal returns the raw slog-JSON lines the wanbond unit wrote on host
// (journalctl -o cat strips the journal prefixes), for transition parsing.
// Best-effort: returns "" on error.
func readDaemonJournal(t *testing.T, r *Runner, host Host, unit string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), smokeSSHTimeout)
	defer cancel()
	res, err := r.Run(ctx, host, "sudo journalctl -u "+unit+" -o cat --no-pager 2>&1; true")
	if err != nil {
		t.Logf("%s: could not read daemon journal: %v", host.Role, err)
		return ""
	}
	return res.Stdout
}

// waitBothPathsUp polls host's daemon journal until every named path has logged a
// "path liveness transition" to "up", or the deadline elapses. It returns each
// path's silence_ms at its up transition. A path that never comes up fails the test.
func waitBothPathsUp(t *testing.T, r *Runner, host Host, names []string, d time.Duration) map[string]int64 {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		journal := readDaemonJournal(t, r, host, smokeUnit)
		ups := pathUpSilences(journal)
		all := true
		for _, n := range names {
			if _, ok := ups[n]; !ok {
				all = false
				break
			}
		}
		if all {
			return ups
		}
		if time.Now().After(deadline) {
			dumpDaemonLog(t, r, host)
			t.Fatalf("not all paths reached liveness up within %v (want %v, saw %v)", d, names, keysOf(ups))
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// livenessRecord is the subset of a "path liveness transition" slog line this tier
// reads.
type livenessRecord struct {
	Msg       string `json:"msg"`
	Path      string `json:"path"`
	To        string `json:"to"`
	SilenceMs int64  `json:"silence_ms"`
}

// pathUpSilences scans journal for the FIRST "path liveness transition" to "up" per
// path name, returning path -> silence_ms at that transition.
func pathUpSilences(journal string) map[string]int64 {
	ups := make(map[string]int64)
	for _, line := range strings.Split(journal, "\n") {
		if !strings.Contains(line, "path liveness transition") {
			continue
		}
		var rec livenessRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		if rec.Msg != "path liveness transition" || rec.To != "up" || rec.Path == "" {
			continue
		}
		if _, seen := ups[rec.Path]; !seen {
			ups[rec.Path] = rec.SilenceMs
		}
	}
	return ups
}

// schedulerSwitchAfter returns the delay from `after` to the EARLIEST "scheduler
// active path change" transition whose destination is toIdx logged strictly after
// `after`, or -1 if none. It is the edge-side per-direction failover latency: the
// reroute is a sub-ms update, so the transition timestamp is the recovery instant.
func schedulerSwitchAfter(journal string, after time.Time, toIdx int) time.Duration {
	best := time.Duration(-1)
	for _, line := range strings.Split(journal, "\n") {
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
		if d := rec.Time.Sub(after); best < 0 || d < best {
			best = d
		}
	}
	return best
}

// parseIperfReport unmarshals an iperf3 -J report (reusing the smoke tier's
// iperfReport shape), returning a parse error for empty/garbled output.
func parseIperfReport(out string) (iperfReport, error) {
	var rep iperfReport
	if strings.TrimSpace(out) == "" {
		return rep, fmt.Errorf("empty iperf3 output")
	}
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		return rep, err
	}
	return rep, nil
}

// latencyMs renders a measured failover latency, or "n/a" for a missing (-1) one.
func latencyMs(d time.Duration) string {
	if d < 0 {
		return "n/a"
	}
	return fmt.Sprintf("%d ms", d.Milliseconds())
}

// keysOf returns the keys of a path->silence map (for diagnostics).
func keysOf(m map[string]int64) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
