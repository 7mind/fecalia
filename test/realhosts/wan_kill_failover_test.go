//go:build realhosts

package realhosts

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// T63 mid-transfer WAN-kill parameters. This tier exercises TWO independent
// mid-transfer kills against the two standing hosts (edge = symmetric-NAT amd64
// llm-ubuntu-0, concentrator = PUBLIC aarch64 o3), each report-only per M10/Q12:
// it asserts ONLY liveness (the transfer continues or resumes) and LOGS the
// failover timing — no absolute Mbit/s or ms threshold gates anything.
//
//  1. LINK failover  — the P1 active-backup scheduler: a sustained flow spans a
//     mid-transfer kill of the ACTIVE WAN path's egress (an iptables OUTPUT DROP
//     on the EDGE, scoped to that path's source IP toward the concentrator), and
//     the SAME TCP flow must survive on the backup path.
//  2. HUB failover   — the Q18/T57 edge-side ordered-endpoint switch: the edge is
//     configured with endpoints=[o3:portA, o3:portB], both fronting the SAME o3
//     concentrator process (portA/portB are DNAT'd to the concentrator's internal
//     listen port), and a mid-transfer kill of the ACTIVE endpoint portA (an
//     iptables raw-PREROUTING DROP on o3, scoped to udp dport portA) forces the
//     edge's hubFailover to switch to the standby endpoint portB and re-handshake;
//     the transfer must RESUME over portB via the fresh session.
//
// Every host mutation (edge OUTPUT rule, o3 DNAT + INPUT-accept + raw DROP,
// daemons, iperf3, the spanning flow) is torn down idempotently on EVERY exit
// path via t.Cleanup — in particular o3's firewall is ALWAYS restored so a killed
// test never leaves o3 dropping its own traffic. Every DROP is scoped to a
// specific UDP port/source so it can never sever the SSH (tcp/22) control channel
// to o3, and o3 is NEVER deprovisioned/rebooted — only live, reversible iptables
// rules are used.
const (
	// t63ConcListenPort is the concentrator's INTERNAL WireGuard listen port for the
	// HUB-failover scenario (bound to o3's primary source IP). The edge never dials it
	// directly; the two public endpoint ports below are DNAT'd onto it. Distinct from
	// smokeListenPort so the two subtests never collide on a port.
	t63ConcListenPort = 51830
	// t63EndpointPortA / t63EndpointPortB are the two PUBLIC UDP ports on o3 the edge's
	// ordered endpoint list targets: portA is endpoints[0] (active at boot), portB is
	// endpoints[1] (the ordered standby). Both are DNAT'd on o3 to t63ConcListenPort, so
	// they front the ONE concentrator process; killing portA's reachability drives the
	// edge's hub-failover switch to portB. Note: the OCI security list must permit
	// inbound UDP on both ports for the hardware run (as it already does for the smoke
	// tier's WG port).
	t63EndpointPortA = 51831
	t63EndpointPortB = 51832

	// t63HubActiveIdx / t63HubStandbyIdx mirror the edge's ordered endpoint indices:
	// endpoints[0]=portA is active at boot, endpoints[1]=portB the standby. A hub-
	// failover switch with to_index==t63HubStandbyIdx is the transition this test times.
	t63HubActiveIdx  = 0
	t63HubStandbyIdx = 1

	// t63HubPathName is the edge's single path name in the HUB-failover scenario. One
	// path suffices: with a single path there is no LINK-level failover to absorb the
	// active-endpoint kill, so recovery can ONLY come from the hub-failover switch —
	// isolating the T57 behaviour under test.
	t63HubPathName = "wan0"

	// t63LoadSeconds is the lifetime of the sustained transfer that is running at the
	// moment of each kill (bounded so D21 never leaks a saturating flow on a shared host).
	t63LoadSeconds = 30
	// t63RampBefore lets the flow ramp and the tunnel settle before the kill.
	t63RampBefore = 3 * time.Second

	// t63PathUpTimeout bounds the wait for the edge path(s) to reach liveness "up".
	t63PathUpTimeout = 40 * time.Second
	// t63HubSwitchTimeout bounds the wait for the hub-failover switch after the portA
	// kill: the controller's settle dwell (hubFailoverSettle = 3s) plus path-down
	// detection plus generous jitter margin.
	t63HubSwitchTimeout = 60 * time.Second
	// t63ResumeTimeout bounds the wait for the data plane to come back over portB after
	// the switch + re-handshake.
	t63ResumeTimeout = 45 * time.Second
)

// TestRealMidTransferWANKill provisions and natively builds wanbond on both standing
// hosts ONCE, then runs the two mid-transfer WAN-kill scenarios as subtests sharing
// that build. Report-only per Q12: executing and recording the failover timing IS the
// acceptance; only a genuine wedge (a flow that neither survives nor resumes) is an
// error.
func TestRealMidTransferWANKill(t *testing.T) {
	cfg := LoadConfig()
	r := NewRunner(cfg)

	t.Logf("realhosts config: edge=%s concentrator=%s conc-public-ip=%s ssh-key=%s",
		cfg.Edge.target(), cfg.Conc.target(), cfg.ConcPubIP, cfg.SSHKey)

	// Provision both hosts (idempotent). The concentrator additionally gets the
	// tunnel-interface INPUT ACCEPT rule so inner iperf3 through the tunnel is not
	// REJECTed on OCI.
	provision(t, r, cfg.Edge, ProvisionOpts{})
	provision(t, r, cfg.Conc, ProvisionOpts{TunnelIface: tunnelIface})

	// Sync + native build on each host once; remove the synced repo (and the secret
	// configs written into it) on exit.
	root := repoRoot(t)
	syncAndBuild(t, r, cfg.Edge, root)
	t.Cleanup(func() { removeRemoteDir(t, r, cfg.Edge) })
	syncAndBuild(t, r, cfg.Conc, root)
	t.Cleanup(func() { removeRemoteDir(t, r, cfg.Conc) })

	t.Run("link_failover", func(t *testing.T) { runLinkFailoverT63(t, r, cfg) })
	t.Run("hub_failover", func(t *testing.T) { runHubFailoverT63(t, r, cfg) })
}

// runLinkFailoverT63 is scenario 1 (LINK failover). It gives the NAT'd edge TWO
// source-IP paths to the ONE concentrator (reusing the multipath two-path plumbing),
// brings the bond up, starts a sustained TCP transfer, then MID-TRANSFER drops the
// ACTIVE path's egress with an iptables OUTPUT DROP on the EDGE (scoped to that path's
// source IP toward the concentrator public IP) and confirms the SAME flow survives on
// the backup path — recording the edge-side scheduler failover time.
func runLinkFailoverT63(t *testing.T, r *Runner, cfg Config) {
	// 1. Resolve the edge uplink and set up the two source-IP paths + policy routing.
	plan := resolveEdgePathPlan(t, r, cfg.Edge, cfg.ConcPubIP)
	t.Logf("edge uplink: iface=%s primary=%s alt=%s gateway=%q prefix=/%d",
		plan.iface, plan.primaryIP, plan.altIP, plan.gateway, plan.prefixLen)
	// SAFETY GUARD: the OUTPUT DROP matches `-d concPubIP`, so if the edge reaches the
	// control host (SSH) VIA concPubIP the rule would sever management SSH. Fail fast
	// before any mutation rather than trust the topology assumption (same failure mode
	// the multipath tier guards against for its scoped blackhole).
	assertControlHostNotConcentrator(t, r, cfg.Edge, plan.concPubIP)
	setupEdgeTwoPaths(t, r, cfg.Edge, plan)

	// 2. Key material (X25519 per end + shared PSK).
	edgePriv, edgePub := genSmokeKey(t)
	concPriv, concPub := genSmokeKey(t)
	psk := randSmokeKey(t)

	// 3. The concentrator binds its single virtual endpoint to its primary source IP.
	concSrc := primaryIP(t, r, cfg.Conc)
	t.Logf("concentrator source addr: %s", concSrc)

	// 4. Configs: the edge lists TWO paths pinning the two source IPs, both reusing the
	//    single concentrator public endpoint (source-routed multipath). Both ends run at
	//    "info" so scheduler/liveness transitions are journalled.
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

	concCfgPath := smokeRemoteDir + "/t63-link-conc.toml"
	edgeCfgPath := smokeRemoteDir + "/t63-link-edge.toml"
	writeRemoteFile(t, r, cfg.Conc, concCfgPath, concCfg)
	writeRemoteFile(t, r, cfg.Edge, edgeCfgPath, edgeCfg)

	// 5. Clean slate, then start concentrator (listening first) and edge.
	preClean(t, r, cfg.Conc)
	preClean(t, r, cfg.Edge)
	startDaemon(t, r, cfg.Conc, concCfgPath)
	startDaemon(t, r, cfg.Edge, edgeCfgPath)

	// 6. Wait for each TUN, address the inner /24, drive the handshake.
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
	if !pingUntil(t, r, cfg.Edge, smokeConcInner, handshakeTimeout) {
		dumpDaemonLog(t, r, cfg.Conc)
		dumpDaemonLog(t, r, cfg.Edge)
		t.Fatalf("handshake never completed: %s unreachable from the edge through the tunnel", smokeConcInner)
	}
	t.Logf("HANDSHAKE OK over the active path")

	// 7. Both paths must ESTABLISH (each reaches liveness "up").
	ups := waitBothPathsUp(t, r, cfg.Edge, []string{mpPrimaryPathName, mpBackupPathName}, t63PathUpTimeout)
	for _, name := range []string{mpPrimaryPathName, mpBackupPathName} {
		t.Logf("PATH ESTABLISHED [%s]: liveness up (silence_ms at transition = %d)", name, ups[name])
	}

	// 8. iperf3 server on the concentrator inner IP, then a long-lived TCP client from
	//    the edge that will SPAN the kill (its reap is registered immediately, D21).
	startIperfServer(t, r, cfg.Conc, smokeConcInner)
	flow, flowOut := startSpanningFlow(t, r, cfg.Edge, smokeConcInner, t63LoadSeconds)

	// 9. Let the flow ramp, then drop the ACTIVE path's egress with an EDGE iptables
	//    OUTPUT DROP and stamp T0 from the EDGE clock (one clock domain with the daemon
	//    slog timestamps). The rule is `-p udp -s primaryIP -d concPubIP -j DROP`: it
	//    matches ONLY the active path's tunnel egress (bound to primaryIP) toward the
	//    concentrator, so management SSH (dest = the control host, never concPubIP) and
	//    the backup path (source altIP) are untouched. killEdgePathEgressT63 registers
	//    the reversing `-D` as a t.Cleanup so a killed test still restores the edge.
	time.Sleep(t63RampBefore)
	killAt := edgeClockNow(t, r, cfg.Edge)
	killEdgePathEgressT63(t, r, cfg.Edge, plan.primaryIP, plan.concPubIP)
	t.Logf("dropped ACTIVE path %q egress (iptables OUTPUT -s %s -d %s -p udp DROP) at edge-T0=%s",
		mpPrimaryPathName, plan.primaryIP, plan.concPubIP, killAt.Format(time.RFC3339Nano))

	// 10. Await the spanning flow.
	flowErr := flow.Wait()

	// 11. Edge-side failover latency: the earliest "scheduler active path change" to the
	//     backup index logged after T0 (single edge-clock domain; the reroute is sub-ms
	//     so the transition timestamp is the recovery instant).
	journal := readDaemonJournal(t, r, cfg.Edge, smokeUnit)
	failover := schedulerSwitchAfter(journal, killAt, mpBackupPathIdx)
	if failover < 0 {
		dumpDaemonLog(t, r, cfg.Edge)
		t.Errorf("no edge scheduler transition to the backup (idx %d) logged after the kill — did the active path actually go down?", mpBackupPathIdx)
	} else {
		t.Logf("LINK_FAILOVER_MS=%d (edge egress %q[%d] -> %q[%d])",
			failover.Milliseconds(), mpPrimaryPathName, mpPrimaryPathIdx, mpBackupPathName, mpBackupPathIdx)
	}

	// 12. Data-plane survival: the SAME spanning TCP flow must have completed with
	//     positive throughput (report-only; the timing above never gates — only a genuine
	//     wedge/reset is an error).
	rep, parseErr := parseIperfReport(flowOut.String())
	mbps := rep.End.SumSent.BitsPerSecond / 1e6
	if flowErr != nil || parseErr != nil || mbps <= 0 {
		t.Errorf("spanning TCP flow did not survive the link kill (exit err=%v, parse err=%v, %.2f Mbit/s) — a WG-session reset?\n%s",
			flowErr, parseErr, mbps, flowOut.String())
	} else {
		t.Logf("FLOW SURVIVED: %.2f Mbit/s over %ds spanning the kill (retransmits=%d)",
			mbps, t63LoadSeconds, rep.End.SumSent.Retransmits)
	}

	t.Logf("=== T63 LINK-FAILOVER RESULTS ===\n"+
		"  paths established:  %s(up) + %s(up)  [source IPs %s / %s]\n"+
		"  active-path kill:   %s egress dropped mid-flow (edge iptables OUTPUT)\n"+
		"  edge failover:      %s\n"+
		"  spanning TCP flow:  %.2f Mbit/s (survived=%t)",
		mpPrimaryPathName, mpBackupPathName, plan.primaryIP, plan.altIP,
		mpPrimaryPathName, latencyMs(failover), mbps, flowErr == nil && parseErr == nil && mbps > 0)
}

// runHubFailoverT63 is scenario 2 (HUB failover, Q18/T57). The edge is configured with
// an ORDERED 2-endpoint concentrator list endpoints=[o3:portA, o3:portB]; both ports
// front the SAME o3 concentrator process (DNAT'd to its internal listen port), so the
// two endpoints are two reachable forms of the one real concentrator host. A sustained
// transfer is running when the ACTIVE endpoint portA is made unreachable (an iptables
// raw-PREROUTING DROP on o3 scoped to udp dport portA); with a single path there is no
// LINK failover to absorb it, so the edge's hubFailover switches to the standby endpoint
// portB and re-handshakes, and the transfer RESUMES over portB. The edge-side switch
// time is recorded; report-only.
func runHubFailoverT63(t *testing.T, r *Runner, cfg Config) {
	// 1. Key material.
	edgePriv, edgePub := genSmokeKey(t)
	concPriv, concPub := genSmokeKey(t)
	psk := randSmokeKey(t)

	// 2. The concentrator binds its INTERNAL listen port to its primary source IP; the
	//    edge's two public endpoint ports are DNAT'd onto it (setupO3DualEndpointT63).
	concSrc := primaryIP(t, r, cfg.Conc)
	edgeSrc := primaryIP(t, r, cfg.Edge)
	t.Logf("path source addrs: edge=%s concentrator=%s (internal listen port %d)", edgeSrc, concSrc, t63ConcListenPort)

	// 3. Configs. The concentrator listens on the INTERNAL port; the edge dials the
	//    ORDERED endpoint list [o3:portA, o3:portB] via the T54 `endpoints = [...]` key.
	//    endpoints[0]=portA is active at boot (device UAPI seeds endpoint=Endpoints[0]).
	//    Both ends at "info" so the hub-failover Warn line is journalled.
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
`, psk, t63HubPathName, concSrc, concPriv, t63ConcListenPort, edgePub, smokeEdgeInner)

	edgeCfg := fmt.Sprintf(`role = "edge"
psk = "%s"

[[paths]]
name = "%s"
source_addr = "%s"

[wireguard]
private_key = "%s"

[[wireguard.peers]]
public_key = "%s"
endpoints = ["%s:%d", "%s:%d"]
allowed_ips = ["%s/32"]

[log]
level = "info"
`, psk, t63HubPathName, edgeSrc, edgePriv, concPub,
		cfg.ConcPubIP, t63EndpointPortA, cfg.ConcPubIP, t63EndpointPortB, smokeConcInner)

	concCfgPath := smokeRemoteDir + "/t63-hub-conc.toml"
	edgeCfgPath := smokeRemoteDir + "/t63-hub-edge.toml"
	writeRemoteFile(t, r, cfg.Conc, concCfgPath, concCfg)
	writeRemoteFile(t, r, cfg.Edge, edgeCfgPath, edgeCfg)

	// 4. Clean slate, start the concentrator (listening on the internal port), install
	//    the two DNAT endpoint aliases + the internal-port INPUT accept on o3, then start
	//    the edge (which dials portA).
	preClean(t, r, cfg.Conc)
	preClean(t, r, cfg.Edge)
	startDaemon(t, r, cfg.Conc, concCfgPath)
	setupO3DualEndpointT63(t, r, cfg.Conc, concSrc)
	startDaemon(t, r, cfg.Edge, edgeCfgPath)

	// 5. Wait for each TUN, address the inner /24, drive the handshake over portA.
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
	if !pingUntil(t, r, cfg.Edge, smokeConcInner, handshakeTimeout) {
		dumpDaemonLog(t, r, cfg.Conc)
		dumpDaemonLog(t, r, cfg.Edge)
		t.Fatalf("handshake never completed over the active endpoint portA (%d)", t63EndpointPortA)
	}
	t.Logf("HANDSHAKE OK over the active endpoint portA (%d)", t63EndpointPortA)

	// 6. The single path must ESTABLISH.
	ups := waitBothPathsUp(t, r, cfg.Edge, []string{t63HubPathName}, t63PathUpTimeout)
	t.Logf("PATH ESTABLISHED [%s]: liveness up (silence_ms at transition = %d)", t63HubPathName, ups[t63HubPathName])

	// 7. iperf3 server on the concentrator inner IP, then a sustained transfer that is
	//    running at the moment of the kill (its reap is registered immediately, D21).
	startIperfServer(t, r, cfg.Conc, smokeConcInner)
	flow, _ := startSpanningFlow(t, r, cfg.Edge, smokeConcInner, t63LoadSeconds)

	// 8. Let the transfer ramp, then make the ACTIVE endpoint portA unreachable on o3
	//    (iptables raw-PREROUTING DROP on udp dport portA — matched BEFORE the nat DNAT,
	//    so the standby portB's DNAT'd traffic is untouched). Stamp T0 from the EDGE clock.
	//    killO3EndpointT63 registers the reversing `-D` as a t.Cleanup so o3's firewall is
	//    ALWAYS restored even if the test is killed here.
	time.Sleep(t63RampBefore)
	killAt := edgeClockNow(t, r, cfg.Edge)
	killO3EndpointT63(t, r, cfg.Conc, concSrc, t63EndpointPortA)
	t.Logf("dropped ACTIVE endpoint portA (%d) on o3 (iptables raw PREROUTING -d %s --dport %d DROP) at edge-T0=%s",
		t63EndpointPortA, concSrc, t63EndpointPortA, killAt.Format(time.RFC3339Nano))

	// 9. Await the hub-failover switch to the standby endpoint (index 1). The old spanning
	//    flow's WG session is torn down by the re-handshake, so it is reaped rather than
	//    awaited for survival.
	switchDelay := waitHubSwitchT63(t, r, cfg.Edge, killAt, t63HubStandbyIdx, t63HubSwitchTimeout)
	if switchDelay < 0 {
		dumpDaemonLog(t, r, cfg.Edge)
		t.Errorf("no hub-failover switch to the standby endpoint (idx %d) logged after the portA kill — did all paths to the active endpoint go down?", t63HubStandbyIdx)
	} else {
		t.Logf("HUB_FAILOVER_MS=%d (endpoint idx %d[portA] -> %d[portB])", switchDelay.Milliseconds(), t63HubActiveIdx, t63HubStandbyIdx)
	}

	// 10. Reap the pre-kill spanning flow so its (reset) client frees the single iperf3
	//     server before the fresh resume transfer.
	reapEdgeFlowT63(t, r, cfg.Edge)
	_ = flow // reaped via t.Cleanup + reapEdgeFlowT63; not awaited for survival.

	// 11. Confirm the data plane RESUMED over the standby endpoint portB: the tunnel must
	//     ping again (fresh session over portB), and a fresh TCP transfer must complete
	//     with positive throughput. A genuine failure to resume is an ERROR; the switch
	//     timing above is report-only.
	if !pingUntil(t, r, cfg.Edge, smokeConcInner, t63ResumeTimeout) {
		dumpDaemonLog(t, r, cfg.Conc)
		dumpDaemonLog(t, r, cfg.Edge)
		t.Fatalf("tunnel did not resume over the standby endpoint portB (%d) after hub failover", t63EndpointPortB)
	}
	resume := iperfTCP(t, r, cfg.Edge, smokeConcInner, 1)
	if resume.mbps <= 0 {
		t.Errorf("post-failover transfer did not resume over portB (%.2f Mbit/s)", resume.mbps)
	} else {
		t.Logf("TRANSFER RESUMED over portB (%d): %.2f Mbit/s (retransmits=%d)", t63EndpointPortB, resume.mbps, resume.retransmits)
	}

	t.Logf("=== T63 HUB-FAILOVER RESULTS ===\n"+
		"  ordered endpoints:  [o3:%d(active), o3:%d(standby)] -> internal port %d\n"+
		"  active-endpoint kill: portA(%d) DROP'd on o3 mid-transfer (iptables raw PREROUTING)\n"+
		"  edge hub failover:  %s (idx %d -> %d, re-handshake)\n"+
		"  resumed transfer:   %.2f Mbit/s over portB",
		t63EndpointPortA, t63EndpointPortB, t63ConcListenPort,
		t63EndpointPortA, latencyMs(switchDelay), t63HubActiveIdx, t63HubStandbyIdx, resume.mbps)
}

// killEdgePathEgressT63 installs an EDGE iptables OUTPUT DROP that blocks ONLY the given
// path source IP's UDP egress toward the concentrator public IP — the active path's
// tunnel traffic — and registers its removal as a t.Cleanup so a killed test still
// restores the edge. The `-s srcIP -d concPubIP -p udp` scope leaves management SSH
// (dest = the control host, never concPubIP) and the backup path (a different source IP)
// untouched. Fatal on SSH error.
func killEdgePathEgressT63(t *testing.T, r *Runner, edge Host, srcIP, concPubIP string) {
	t.Helper()
	rule := fmt.Sprintf("-p udp -s %s -d %s -j DROP", srcIP, concPubIP)
	// Register the reversing delete FIRST so even a panic between add and the explicit
	// path below restores the edge. The `-D` tolerates an already-removed rule.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cancel()
		if _, err := r.Run(ctx, edge, "sudo iptables -D OUTPUT "+rule+" 2>/dev/null; true"); err != nil {
			t.Logf("cleanup: edge: remove OUTPUT DROP (%s): %v", rule, err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), smokeSSHTimeout)
	defer cancel()
	if _, err := r.Run(ctx, edge, "sudo iptables -A OUTPUT "+rule); err != nil {
		t.Fatalf("edge: install OUTPUT DROP (%s) failed: %v", rule, err)
	}
}

// setupO3DualEndpointT63 fronts the ONE concentrator process with TWO reachable public
// UDP endpoint ports on o3: it DNATs udp dport portA and portB (destined to the
// concentrator's primary source IP) to the concentrator's INTERNAL listen port, and
// inserts an INPUT ACCEPT for that internal port at the chain head so the (post-DNAT)
// outer WireGuard datagrams are not caught by OCI's appended REJECT. Every rule is udp-
// and port-scoped (never tcp/22) and is torn down via a t.Cleanup registered BEFORE any
// mutation, so a killed test always restores o3's firewall. Fatal on SSH error.
func setupO3DualEndpointT63(t *testing.T, r *Runner, conc Host, concSrc string) {
	t.Helper()
	dnat := func(port int) string {
		return fmt.Sprintf("-t nat PREROUTING -p udp -d %s --dport %d -j DNAT --to-destination %s:%d",
			concSrc, port, concSrc, t63ConcListenPort)
	}
	inputAccept := fmt.Sprintf("INPUT -p udp -d %s --dport %d -j ACCEPT", concSrc, t63ConcListenPort)

	// Teardown registered before any mutation (best-effort; logs, never fails).
	t.Cleanup(func() {
		teardown := strings.Join([]string{
			"sudo iptables -t nat -D PREROUTING " + strings.TrimPrefix(dnat(t63EndpointPortA), "-t nat PREROUTING ") + " 2>/dev/null",
			"sudo iptables -t nat -D PREROUTING " + strings.TrimPrefix(dnat(t63EndpointPortB), "-t nat PREROUTING ") + " 2>/dev/null",
			"sudo iptables -D " + inputAccept + " 2>/dev/null",
			"true",
		}, "; ")
		ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cancel()
		if _, err := r.Run(ctx, conc, teardown); err != nil {
			t.Logf("cleanup: concentrator: dual-endpoint teardown: %v", err)
		}
	})

	// -I inserts the DNAT/ACCEPT at the chain head so they precede any OCI-appended
	// REJECT. Idempotent: delete any identical prior rule first so a re-run never stacks
	// duplicates (the del tolerates a missing rule so `set -e` is not tripped).
	addIdem := func(table, chain, body string) string {
		tbl := ""
		if table != "" {
			tbl = "-t " + table + " "
		}
		return fmt.Sprintf("sudo iptables %s-D %s %s 2>/dev/null || true; sudo iptables %s-I %s %s", tbl, chain, body, tbl, chain, body)
	}
	setup := strings.Join([]string{
		"set -e",
		addIdem("nat", "PREROUTING", fmt.Sprintf("-p udp -d %s --dport %d -j DNAT --to-destination %s:%d", concSrc, t63EndpointPortA, concSrc, t63ConcListenPort)),
		addIdem("nat", "PREROUTING", fmt.Sprintf("-p udp -d %s --dport %d -j DNAT --to-destination %s:%d", concSrc, t63EndpointPortB, concSrc, t63ConcListenPort)),
		addIdem("", "INPUT", fmt.Sprintf("-p udp -d %s --dport %d -j ACCEPT", concSrc, t63ConcListenPort)),
	}, "\n")

	ctx, cancel := context.WithTimeout(context.Background(), smokeSSHTimeout)
	defer cancel()
	if _, err := r.Run(ctx, conc, setup); err != nil {
		t.Fatalf("concentrator: dual-endpoint DNAT setup failed: %v", err)
	}
	t.Logf("concentrator: dual endpoint up (o3:%d and o3:%d DNAT -> internal :%d; INPUT accept :%d)",
		t63EndpointPortA, t63EndpointPortB, t63ConcListenPort, t63ConcListenPort)
}

// killO3EndpointT63 makes the given ACTIVE endpoint port unreachable on o3 by inserting
// an iptables raw-PREROUTING DROP on its inbound udp dport — matched BEFORE the nat DNAT,
// so ONLY packets originally destined to portA are dropped while the standby portB's
// DNAT'd traffic (originally dport portB) still reaches the concentrator. It registers
// the reversing `-D` as a t.Cleanup so o3's firewall is ALWAYS restored even if the test
// is killed here (leaving a DROP on o3 that blocks its own traffic is dangerous). The
// rule is udp/port-scoped and never touches the SSH (tcp/22) control channel. Fatal on
// SSH error.
func killO3EndpointT63(t *testing.T, r *Runner, conc Host, concSrc string, port int) {
	t.Helper()
	rule := fmt.Sprintf("-t raw PREROUTING -p udp -d %s --dport %d -j DROP", concSrc, port)
	body := "-p udp -d " + concSrc + fmt.Sprintf(" --dport %d -j DROP", port)
	// Register the reversing delete FIRST (best-effort; MUST always restore o3).
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cancel()
		if _, err := r.Run(ctx, conc, "sudo iptables -t raw -D PREROUTING "+body+" 2>/dev/null; true"); err != nil {
			t.Logf("cleanup: concentrator: restore raw DROP (%s): %v", rule, err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), smokeSSHTimeout)
	defer cancel()
	if _, err := r.Run(ctx, conc, "sudo iptables -t raw -I PREROUTING "+body); err != nil {
		t.Fatalf("concentrator: install raw DROP (%s) failed: %v", rule, err)
	}
}

// reapEdgeFlowT63 kills any lingering iperf3 client on the edge (best-effort) so a reset
// spanning flow frees the single concentrator iperf3 server before the resume transfer.
func reapEdgeFlowT63(t *testing.T, r *Runner, edge Host) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	if _, err := r.Run(ctx, edge, "pkill -f 'iperf3 -c' 2>/dev/null; true"); err != nil {
		t.Logf("edge: reap spanning iperf3 client: %v", err)
	}
}

// waitHubSwitchT63 polls the edge daemon journal until a hub-failover switch to toIdx is
// logged after `after`, returning the edge-clock delay from the kill to that switch, or
// -1 if none is seen within d. Both the T0 marker and the switch timestamp come from the
// edge clock (single domain).
func waitHubSwitchT63(t *testing.T, r *Runner, edge Host, after time.Time, toIdx int, d time.Duration) time.Duration {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		journal := readDaemonJournal(t, r, edge, smokeUnit)
		if sw := hubSwitchAfterT63(journal, after, toIdx); sw >= 0 {
			return sw
		}
		if time.Now().After(deadline) {
			return -1
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// hubSwitchAfterT63 returns the delay from `after` to the EARLIEST edge-side "hub
// failover ... switched endpoint" transition whose to_index is toIdx logged strictly
// after `after`, or -1 if none. It mirrors schedulerSwitchAfter but reads the T57
// hub-failover Warn line (device/failover.go) instead of the per-path scheduler line.
func hubSwitchAfterT63(journal string, after time.Time, toIdx int) time.Duration {
	best := time.Duration(-1)
	for _, line := range strings.Split(journal, "\n") {
		if !strings.Contains(line, "hub failover") {
			continue
		}
		var rec struct {
			Time    time.Time `json:"time"`
			ToIndex int       `json:"to_index"`
		}
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		if rec.ToIndex != toIdx || !rec.Time.After(after) {
			continue
		}
		if d := rec.Time.Sub(after); best < 0 || d < best {
			best = d
		}
	}
	return best
}
