//go:build e2e

package e2e

// TestMultiConcentratorWarmStandby (T261, goal G28, milestone M108) is the netns
// e2e ACCEPTANCE for the Q74 multi-exit edge: ONE edge, TWO uplinks, bonded to TWO
// warm concentrator EXITS (exit-a, exit-b), with operator exit switching (POST
// /api/exit), within-concentrator endpoint failover (Q72), and cross-concentrator
// auto-promotion (Q75) composed end to end on the real wire. It is the netns
// realization of the in-process device-package acceptances (edge_multipeer_e2e /
// monitor_e2e / autopromote), built on the two-WAN netns harness (see
// two_wan_downlink_pin_test.go and hub_failover_test.go, read first).
//
// TOPOLOGY. The edge runs in the BASE (test-process) network namespace — like the
// T248 and hub-failover edges — so its LOOPBACK-bound [monitor] endpoint is directly
// reachable by the test process (which shares that namespace); the POST /api/exit
// control demands a kernel-loopback bind, and driving it "from within the edge netns"
// IS driving it from the base netns here. The two exit concentrators are each their
// own PID-addressed holder namespace (each owns its own fixed-name wanbond0 TUN, so
// they cannot share one namespace), wired onto a single shared L2 bridge in the base
// namespace so BOTH edge uplinks reach BOTH concentrators (the "shared uplink set"):
//
//	base netns (edge, inner 10.20.0.1):        bridge wbMcbr (L2, 10.140.0.0/24)
//	   uplink up1 10.140.0.11 ── wbMcU1p ┐
//	   uplink up2 10.140.0.12 ── wbMcU2p ┼── wbMcbr ──┬── wbMcAp ── conc A (10.140.0.2)   exit-a, inner 10.20.0.2
//	                                      │            └── wbMcBp ── conc B (10.140.0.3 + .4) exit-b, inner 10.20.0.3
//
// The edge's two uplinks source-pin (bind = "source") to 10.140.0.11 / .12; on the one
// shared /24 the kernel egresses each via the veth that OWNS its source address (the
// connected route's prefsrc), so the bond fans out over two genuinely distinct uplinks.
// Both exit peers carry mode = "default-route"; per the R255 admission rule each lists
// the SAME default-route entry (0.0.0.0/0) plus its OWN concentrator inner /32
// (10.20.0.2 for A, 10.20.0.3 for B), so a standby exit still renders a non-empty
// allowed_ips. exit-a is the FIRST exit-capable peer, so the exit selector boots it
// active (Q74, no persistence); exit-b boots as the WARM standby.
//
// exit-b has TWO endpoints (10.140.0.3:51820 and 10.140.0.4:51820 — two source-addr
// paths on ONE concentrator daemon, the "shape B to listen on 2 addresses/ports" of the
// task), so its OWN T57 hub-failover controller can advance between them; exit-a is a
// SINGLE-endpoint exit, which on a multi-exit edge gets the R267 EXHAUSTION-ONLY
// controller. Killing a concentrator endpoint = removing its address from the
// concentrator's veth (ip addr del), so the edge's probes to it go unanswered while the
// other address on the same interface stays reachable.
//
// DEFAULT-ROUTE EGRESS OBSERVATION. The edge installs the wg-quick-style /1+/1 split
// default route into wanbond0, so a continuous ping stream to an off-tunnel address
// (198.51.100.7, TEST-NET-2 — never connected) egresses through the ACTIVE exit,
// arrives (decrypted) at that concentrator's wanbond0, and increments its inner-
// interface rx counter (read via /proc/net/dev inside the concentrator netns). The
// concentrator drops the packet (no forwarding, the strict Q41 boundary) but the rx
// byte counter still moves — so "which concentrator is egressing the default route" is
// observed AT THE CONCENTRATORS' INNER INTERFACES, exactly as the acceptance requires.
// The inner /32s stay with their owning peers in the allowed-ips trie (only the /1
// default splits move on a switch), so a ping to each concentrator's inner /32 always
// reaches THAT concentrator regardless of which exit is active — the warm-standby proof.
//
// ASSERTIONS (in order, with explicit budgets):
//	(1) BOTH exit WG sessions establish and stay warm (peerSessions both established;
//	    ping each concentrator inner /32 concurrently);
//	(2) default-route traffic egresses via the boot-active exit (exit-a) ONLY;
//	(3) POST /api/exit exit-b → egress moves to exit-b within the switch budget (no
//	    handshake wait — exit-b was already warm);
//	(4) per-concentrator stats: the monitor snapshot carries per-peer paths/fec/reseq/
//	    endpoints/sessions for BOTH exits;
//	(5) Q72 composition: kill exit-b's ACTIVE endpoint → exit-b's OWN endpoint failover
//	    advances to its standby endpoint within the failover budget, activeExit STAYS
//	    exit-b, exit-a untouched;
//	(6) Q75 auto-promotion: kill ALL of exit-b's remaining endpoints (exhaustion) → the
//	    edge auto-promotes exit-a, activeExit becomes exit-a, the switch is logged
//	    reason=auto-promotion; a subsequent PARTIAL recovery of exit-b does NOT
//	    auto-fail-back (activeExit stays exit-a).
//
// HARDWARE TIER — DO NOT RUN IN THE DEFAULT GATE. Like every //go:build e2e test here it
// needs root, /dev/net/tun, and network namespaces; it is compiled and vet/lint GREEN
// locally and executed ONLY on the privileged netns/real-host tier (o3.7mind.io), per
// test in its own fresh netns (the o3 root-netns concentrator collision), -count=3 stable.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/7mind/wanbond/internal/monitor"
)

const (
	// Shared L2 underlay fabric (base netns). One /24, so every node is L2-adjacent and
	// both edge uplinks reach every concentrator address with no routing.
	mcBridge = "wbMcbr"

	mcEdgeUp1IP = "10.140.0.11" // edge uplink 1 source_addr
	mcEdgeUp2IP = "10.140.0.12" // edge uplink 2 source_addr
	mcU1Edge    = "wbMcU1e"     // edge-side veth for uplink 1 (base netns)
	mcU1Port    = "wbMcU1p"     // bridge-side veth for uplink 1
	mcU2Edge    = "wbMcU2e"
	mcU2Port    = "wbMcU2p"

	// exit-a: one concentrator daemon, one underlay address (single endpoint).
	mcConcAIP   = "10.140.0.2"
	mcConcAPort = "wbMcAp" // bridge-side veth
	mcConcACeth = "wbMcAc" // in-namespace veth
	// exit-b: one concentrator daemon listening on TWO underlay addresses (two
	// endpoints). .3 is the boot-active endpoint; .4 is the standby its own T57
	// controller fails over to.
	mcConcB1IP  = "10.140.0.3"
	mcConcB2IP  = "10.140.0.4"
	mcConcBPort = "wbMcBp"
	mcConcBCeth = "wbMcBc"

	mcListenPort = listenPort // 51820, reused (each concentrator in its own netns)

	// Inner overlay /24. The edge and both concentrators share it; each exit's inner
	// /32 is crypto-routed to its own peer, so both answer their own inner ping while
	// only the ACTIVE exit carries the default route.
	mcEdgeInner  = "10.20.0.1"
	mcConcAInner = "10.20.0.2"
	mcConcBInner = "10.20.0.3"

	// Configured exit-peer names (the monitor/metrics `peer` label and the POST
	// /api/exit body). exit-a is FIRST in config order, so it boots active (Q74).
	mcPeerA = "exit-a"
	mcPeerB = "exit-b"

	// The edge [monitor] loopback endpoint (base netns). Port 9113 is the next free
	// 127.0.0.1 port after the /metrics registry in netns.go (this file binds a
	// [monitor], not a /metrics, endpoint — see the registry note there).
	mcMonitorListen = "127.0.0.1:9113"
	mcMonitorWSURL  = "ws://" + mcMonitorListen + "/ws"
	mcExitURL       = "http://" + mcMonitorListen + "/api/exit"

	// mcDefaultRouteTarget is an off-tunnel (never-connected) address the continuous
	// default-route ping stream targets, so the traffic egresses through the active
	// exit and surfaces at its inner interface. No reply is expected (the concentrator
	// does not forward — the Q41 boundary); only its arrival is observed.
	mcDefaultRouteTarget = "198.51.100.7"

	// mcEgressSampleWindow is the window over which each concentrator's inner-interface
	// rx delta is sampled to decide which exit is egressing the default route.
	mcEgressSampleWindow = 1500 * time.Millisecond
	// mcEgressFloorBytes floors the ACTIVE exit's rx delta over a sample window so a
	// verdict is never read from probe/keepalive noise alone (the ping stream moves far
	// more than this; it only rejects "no default-route traffic flowed").
	mcEgressFloorBytes = 20 * 1024
	// mcEgressDominanceRatio is the minimum ratio of the active exit's rx delta to the
	// standby's over a window — the standby carries only small probe/keepalive traffic,
	// the active carries the saturating ping stream on top, so the split is decisive.
	mcEgressDominanceRatio = 5.0
)

// mcBringUp bounds initial bring-up of three daemons, four probers, and two concurrent
// WG handshakes on the shared, CPU/PPS-bound netns fixture. Generous by design.
const mcBringUp = 25 * time.Second

// mcSwitchBudget is the acceptance budget for a MANUAL exit switch (POST /api/exit) to
// move default-route egress onto the target exit. The target is already WARM (its
// session established, its inner /32 kept alive), so the switch is a steal-on-insert
// allowed-ips repoint with NO re-handshake — egress follows on the next packet, well
// within the Q74 5s target.
const mcSwitchBudget = 5 * time.Second

// mcSettle mirrors the unexported device.hubFailoverSettle (3s) — the dwell a freshly-
// selected concentrator endpoint gets before the controller may advance again (and, for
// the R267 single-endpoint exhaustion path, the outage dwell before exhaustion is
// raised). It is not a gate this test sets, only a bound folded into the windows below.
const mcSettle = 3 * time.Second

// mcEndpointFailoverBudget bounds exit-b's OWN within-concentrator endpoint failover
// (Q72/T57): all paths to the active endpoint DOWN → advance to the standby endpoint →
// re-handshake → resume. It composes the daemon's own timing constants (the D16 single-
// source-of-truth style), not a guessed sleep:
//
//	PLivenessFailoverBudget                              (all-paths-DOWN detect + headroom) 1.6s
//	+ mcSettle                                           (settle dwell; non-gating here — the
//	                                                      boot dwell elapsed long before)     3.0s
//	+ DefaultUpSuccesses*DefaultProbeInterval            (standby endpoint UP-recovery)       0.6s
//	+ 6s                                                 (WG re-handshake RTT + shared-host jitter)
//	                                                     ≈ 11.2s
var mcEndpointFailoverBudget = PLivenessFailoverBudget +
	mcSettle +
	time.Duration(PLivenessUpSuccesses)*PLivenessProbeInterval +
	6*time.Second

// mcAutoPromoteBudget bounds Q75 auto-promotion after exit-b's endpoints are ALL killed:
// exit-b's controller must detect the outage and complete a FULL flattened-list wrap
// (every endpoint tried and down) to raise the exhaustion signal, then the selector
// promotes exit-a (a steal-on-insert repoint, no re-handshake — exit-a is warm). With
// the pre-kill settle dwell already elapsed the first wrap advance is immediate, so the
// exhaustion signal lands ~detect + one settle after the kill; the promotion ACTION
// itself is sub-second (the Q75 <5s target). The budget carries harness slack over that
// analytical value; the MEASURED latency is logged so the <5s action target is visible.
var mcAutoPromoteBudget = PLivenessFailoverBudget + mcSettle + 6*time.Second

func TestMultiConcentratorWarmStandby(t *testing.T) {
	f := setupMultiConcentrator(t)

	// ---- (1) Both exit sessions establish and stay warm. ----
	// Warm proof #1: ping EACH concentrator's inner /32 concurrently. Each /32 is
	// crypto-routed to its own peer, so both answering means both bonds are up and both
	// sessions live — concurrently, not one-then-the-other.
	var wg sync.WaitGroup
	okA, okB := false, false
	wg.Add(2)
	go func() { defer wg.Done(); okA = f.base.pingUntil(mcConcAInner, mcBringUp) }()
	go func() { defer wg.Done(); okB = f.base.pingUntil(mcConcBInner, mcBringUp) }()
	wg.Wait()
	if !okA || !okB {
		t.Fatalf("both warm exits did not come up: exit-a inner reachable=%v, exit-b inner reachable=%v\n--- edge ---\n%s\n--- conc A ---\n%s\n--- conc B ---\n%s",
			okA, okB, f.edge.log(), f.concA.proc.log(), f.concB.proc.log())
	}

	readSnap, closeSnap := f.dialMonitor(t)
	defer closeSnap()

	// Warm proof #2: the monitor snapshot's per-peer session view reports BOTH exit peers'
	// OWN WG session established, and the boot-active exit is exit-a (Q74 first-exit rule).
	snap := readSnapUntil(t, readSnap, "both peer sessions established + activeExit=exit-a", mcBringUp,
		func(s monitor.MonitorSnapshot) bool {
			return peerSessionEstablished(s, mcPeerA) && peerSessionEstablished(s, mcPeerB) && s.ActiveExit == mcPeerA
		})
	t.Logf("(1) both exit sessions warm; boot activeExit=%q", snap.ActiveExit)

	// ---- Continuous default-route traffic (spans phases 2..6). ----
	// A steady ping stream to the default-route target egresses via the ACTIVE exit and
	// lands at its inner interface. The target address is assigned to EACH concentrator's
	// loopback (see setup), so the active concentrator TERMINATES it locally (echo reply)
	// rather than answering ICMP net-unreachable — which would make ping give up after one
	// packet. -M dont clears the DF bit so a 500-byte payload never trips EMSGSIZE on the
	// inner path; -w bounds the stream well past the whole test.
	flood := f.base.startProc(t, "default-route-flood",
		"ping", "-q", "-n", "-i", "0.02", "-s", "500", "-M", "dont", "-w", "180", mcDefaultRouteTarget)
	time.Sleep(700 * time.Millisecond) // let the stream ramp before the first sample

	// ---- (2) Default-route egress rides the boot-active exit (exit-a) ONLY. ----
	aDelta, bDelta := f.sampleEgress(t, mcEgressSampleWindow)
	if !egressDominant(aDelta, bDelta) {
		routeGet, _ := exec.Command("ip", "route", "get", mcDefaultRouteTarget).CombinedOutput()
		routes, _ := exec.Command("ip", "route", "show").CombinedOutput()
		t.Fatalf("(2) boot default-route egress is not pinned to exit-a: exit-a inner Δrx=%d, exit-b inner Δrx=%d (want exit-a >= %d and >= %.0fx exit-b)\n--- edge wanbond0 %s ---\n--- ip route get %s ---\n%s\n--- ip route show ---\n%s\n--- flood ping ---\n%s\n--- edge ---\n%s",
			aDelta, bDelta, mcEgressFloorBytes, mcEgressDominanceRatio, edgeDevLine(t, tunDev), mcDefaultRouteTarget, routeGet, routes, flood.log(), f.edge.log())
	}
	t.Logf("(2) default-route egress on exit-a: exit-a Δrx=%d, exit-b Δrx=%d", aDelta, bDelta)

	// ---- (3) POST /api/exit exit-b → egress moves to exit-b (no handshake wait). ----
	if got := f.postExit(t, mcPeerB); got != mcPeerB {
		t.Fatalf("(3) POST /api/exit %q returned activeExit=%q, want %q", mcPeerB, got, mcPeerB)
	}
	switchStart := time.Now()
	waitEgress(t, f, mcPeerB, mcSwitchBudget,
		"(3) default-route egress did not move to exit-b after POST /api/exit")
	t.Logf("(3) manual switch to exit-b: egress moved in %v (budget %v)", time.Since(switchStart).Round(time.Millisecond), mcSwitchBudget)
	// The monitor's live activeExit reflects the manual switch.
	readSnapUntil(t, readSnap, "activeExit=exit-b after manual switch", mcSwitchBudget,
		func(s monitor.MonitorSnapshot) bool { return s.ActiveExit == mcPeerB })

	// ---- (4) Per-concentrator stats: the snapshot carries per-peer paths/fec/reseq/
	// endpoints/sessions for BOTH exits. ----
	stats := readSnapUntil(t, readSnap, "per-peer paths/fec/reseq/endpoints/sessions for both exits", mcBringUp,
		func(s monitor.MonitorSnapshot) bool {
			return s.MultiPeer &&
				peerPathCount(s, mcPeerA) == 2 && peerPathCount(s, mcPeerB) == 2 &&
				hasPeer(fecPeers(s), mcPeerA) && hasPeer(fecPeers(s), mcPeerB) &&
				hasPeer(reseqPeers(s), mcPeerA) && hasPeer(reseqPeers(s), mcPeerB) &&
				peerSessionEstablished(s, mcPeerA) && peerSessionEstablished(s, mcPeerB) &&
				peerEndpointCount(s, mcPeerA) == 1 && peerEndpointCount(s, mcPeerB) == 2
		})
	assertOneActiveEndpointPerPeer(t, stats)
	t.Logf("(4) per-peer stats present: exit-a paths=%d endpoints=%d, exit-b paths=%d endpoints=%d; fec/reseq/sessions carry both peers",
		peerPathCount(stats, mcPeerA), peerEndpointCount(stats, mcPeerA),
		peerPathCount(stats, mcPeerB), peerEndpointCount(stats, mcPeerB))

	// ---- (5) Q72: kill exit-b's ACTIVE endpoint (.3) → its OWN T57 controller advances
	// to the standby endpoint (.4); activeExit STAYS exit-b, exit-a untouched. ----
	// Dwell so the boot settle since the manual switch has elapsed and the failover
	// advance is not gated by the settle dwell.
	time.Sleep(mcSettle + 500*time.Millisecond)
	f.concB.delAddr(mcConcB1IP)
	killActiveEP := time.Now()
	t.Logf("(5) killed exit-b ACTIVE endpoint %s (removed from conc-B veth)", mcConcB1IP)

	// exit-b's endpoint failover resumes default-route egress ON exit-b (its inner rx
	// grows again) within the endpoint-failover budget, and activeExit never left exit-b.
	waitEgress(t, f, mcPeerB, mcEndpointFailoverBudget,
		"(5) exit-b endpoint failover did not resume egress on exit-b's standby endpoint")
	epFailoverGap := time.Since(killActiveEP)
	// The within-concentrator endpoint failover was logged by exit-b's OWN controller,
	// and the monitor shows the active endpoint moved to the standby (.4) while activeExit
	// stayed exit-b and exit-a's single endpoint is untouched.
	if !edgeLoggedText(f.edge.log(), "hub failover: all paths to active concentrator down; switched endpoint and re-handshaked") {
		t.Fatalf("(5) edge journal has no within-concentrator 'hub failover: ... switched endpoint' line for exit-b's endpoint failover\n--- edge ---\n%s", f.edge.log())
	}
	post5 := readSnapUntil(t, readSnap, "exit-b active endpoint moved to standby, activeExit still exit-b", mcEndpointFailoverBudget,
		func(s monitor.MonitorSnapshot) bool {
			return s.ActiveExit == mcPeerB && activeEndpointAddr(s, mcPeerB) == mcConcB2IP+":"+strconv.Itoa(mcListenPort)
		})
	if !peerSessionEstablished(post5, mcPeerA) {
		t.Fatalf("(5) exit-a session was disturbed by exit-b's endpoint failover (peerSessions=%+v)", post5.PeerSessions)
	}
	t.Logf("(5) exit-b endpoint failover to standby %s in %v (budget %v); activeExit stayed %q, exit-a untouched",
		mcConcB2IP, epFailoverGap.Round(time.Millisecond), mcEndpointFailoverBudget, post5.ActiveExit)

	// ---- (6) Q75: kill ALL of exit-b's remaining endpoints (.4) → exhaustion →
	// auto-promote exit-a; a subsequent partial recovery of exit-b does NOT fail back. ----
	f.concB.delAddr(mcConcB2IP)
	killAllEP := time.Now()
	t.Logf("(6) killed exit-b's REMAINING endpoint %s (full endpoint-list exhaustion)", mcConcB2IP)

	// The edge auto-promotes exit-a: activeExit flips to exit-a (the promotion), and then
	// default-route egress moves to exit-a (the data plane catches up). The flip latency is
	// dominated by exit-b's endpoint-list-exhaustion DETECTION (detect + one settle dwell,
	// a daemon-timing floor of ~detect+mcSettle), after which the promotion ACTION itself —
	// the steal-on-insert allowed-ips repoint, the Q75 feature under test — is sub-second
	// (no re-handshake: exit-a is warm).
	readSnapUntil(t, readSnap, "activeExit auto-promoted to exit-a", mcAutoPromoteBudget,
		func(s monitor.MonitorSnapshot) bool { return s.ActiveExit == mcPeerA })
	promoteGap := time.Since(killAllEP)
	waitEgress(t, f, mcPeerA, mcAutoPromoteBudget, "(6) default-route egress did not auto-promote to exit-a")
	// The promotion is recorded reason=auto-promotion, distinguishing it from a manual switch.
	if !edgeLoggedAutoPromotion(f.edge.log(), mcPeerA) {
		t.Fatalf("(6) edge journal has no 'active exit switched' ... reason=auto-promotion to exit-a line\n--- edge ---\n%s", f.edge.log())
	}
	t.Logf("(6) auto-promotion to exit-a: activeExit flipped in %v (budget %v; the flip is bounded by exit-b's exhaustion detection ~detect+%v, the promotion action itself is sub-second)",
		promoteGap.Round(time.Millisecond), mcAutoPromoteBudget, mcSettle)

	// No auto-fail-back: bring exit-b PARTIALLY back (restore .3). exit-b re-establishes,
	// but egress must STAY on exit-a — a promoted exit holds until it itself exhausts.
	f.concB.addAddr(mcConcB1IP)
	t.Logf("(6) partial recovery of exit-b (restored %s); activeExit must NOT fail back", mcConcB1IP)
	const noFailbackDwell = 6 * time.Second
	deadline := time.Now().Add(noFailbackDwell)
	for time.Now().Before(deadline) {
		s := readSnap(t)
		if s.ActiveExit != mcPeerA {
			t.Fatalf("(6) activeExit auto-failed-back to %q after exit-b partial recovery; a promoted exit must NOT fail back (Q75)\n--- edge ---\n%s", s.ActiveExit, f.edge.log())
		}
	}
	// And egress is still on exit-a after the recovery dwell.
	aDelta, bDelta = f.sampleEgress(t, mcEgressSampleWindow)
	if !egressDominant(aDelta, bDelta) {
		t.Fatalf("(6) default-route egress left exit-a after exit-b partial recovery: exit-a Δrx=%d, exit-b Δrx=%d — no auto-fail-back is required\n--- edge ---\n%s",
			aDelta, bDelta, f.edge.log())
	}
	t.Logf("(6) no auto-fail-back: activeExit held exit-a for %v after exit-b partial recovery (exit-a Δrx=%d, exit-b Δrx=%d)", noFailbackDwell, aDelta, bDelta)
}

// ---- fixture ----

// mcFixture is the assembled multi-concentrator topology: the base-namespace edge
// (Topology + proc) and the two exit concentrators (each its own holder namespace).
type mcFixture struct {
	t     *testing.T
	base  *Topology
	edge  *proc
	concA *concNS
	concB *concNS
}

// concNS is one concentrator in its own PID-addressed holder namespace: the daemon
// proc, its underlay addresses on the shared bridge (one for exit-a, two for exit-b),
// and the handles to run commands / read counters inside it.
type concNS struct {
	t      *testing.T
	base   *Topology
	name   string
	pid    int
	holder *exec.Cmd
	ceth   string // in-namespace veth (where the underlay addresses live)
	proc   *proc
}

// setupMultiConcentrator builds the whole fixture and returns once both bonds' TUNs
// exist and are addressed. All teardown is registered via t.Cleanup.
func setupMultiConcentrator(t *testing.T) *mcFixture {
	t.Helper()
	bin := buildWanbond(t)

	base := &Topology{t: t}
	base.run("ip", "link", "set", "lo", "up")

	// Idempotent pre-delete of the fixed-name bridge + edge-side veths (a prior test's
	// teardown can race the kernel reap in the reused base namespace), then the L2 bridge.
	for _, dev := range []string{mcU1Edge, mcU2Edge, mcConcAPort, mcConcBPort, mcBridge} {
		_ = base.tryRun("ip", "link", "del", dev)
	}
	base.run("ip", "link", "add", mcBridge, "type", "bridge", "stp_state", "0", "forward_delay", "0")
	base.run("ip", "link", "set", mcBridge, "up")
	t.Cleanup(func() {
		for _, dev := range []string{mcU1Edge, mcU2Edge, mcConcAPort, mcConcBPort, mcBridge} {
			_ = base.tryRun("ip", "link", "del", dev)
		}
	})

	// Edge uplinks: two veth pairs, edge end in the base namespace (source-pinned), port
	// end on the bridge. Both reach every concentrator address on the /24.
	for _, u := range []struct{ edge, port, ip string }{
		{mcU1Edge, mcU1Port, mcEdgeUp1IP},
		{mcU2Edge, mcU2Port, mcEdgeUp2IP},
	} {
		base.run("ip", "link", "add", u.edge, "type", "veth", "peer", "name", u.port)
		base.run("ip", "addr", "add", u.ip+"/24", "dev", u.edge)
		base.run("ip", "link", "set", u.edge, "up")
		base.run("ip", "link", "set", u.port, "master", mcBridge)
		base.run("ip", "link", "set", u.port, "up")
	}

	concA := startConcHolder(t, base, "concA", mcConcAPort, mcConcACeth, []string{mcConcAIP})
	concB := startConcHolder(t, base, "concB", mcConcBPort, mcConcBCeth, []string{mcConcB1IP, mcConcB2IP})

	// Assign the default-route target to each concentrator's loopback so the ACTIVE exit
	// TERMINATES the default-route ping stream locally (echo reply) instead of returning
	// ICMP net-unreachable — the latter makes the ping generator give up after one packet.
	// The traffic still lands (and is counted) at the concentrator's wanbond0 rx first.
	concA.run("ip", "addr", "add", mcDefaultRouteTarget+"/32", "dev", "lo")
	concB.run("ip", "addr", "add", mcDefaultRouteTarget+"/32", "dev", "lo")

	// WireGuard identities: one keypair per concentrator, one for the edge. Each
	// concentrator authenticates the edge on its top-level psk, which equals the edge's
	// per-peer psk for that exit (T81 multi-peer: present + pairwise-distinct per peer).
	edgePriv, edgePub := genKey(t)
	concAPriv, concAPub := genKey(t)
	concBPriv, concBPub := genKey(t)
	pskA := randKey(t)
	pskB := randKey(t)

	dir := t.TempDir()

	// exit-a: single-endpoint concentrator (one underlay address).
	concACfg := writeConfig(t, filepath.Join(dir, "concA.toml"), fmt.Sprintf(`role = "concentrator"
psk = "%s"

[[paths]]
name = "pub"
source_addr = "%s"

[wireguard]
private_key = "%s"
listen_port = %d

[[wireguard.peers]]
public_key = "%s"
allowed_ips = ["%s/32"]

[log]
level = "info"
`, pskA, mcConcAIP, concAPriv, mcListenPort, edgePub, mcEdgeInner))

	// exit-b: ONE daemon listening on TWO underlay addresses (two endpoints) — the netns
	// "shape B to listen on 2 addresses/ports", so the edge's exit-b controller can fail
	// over between them within the same concentrator.
	concBCfg := writeConfig(t, filepath.Join(dir, "concB.toml"), fmt.Sprintf(`role = "concentrator"
psk = "%s"

[[paths]]
name = "pub1"
source_addr = "%s"

[[paths]]
name = "pub2"
source_addr = "%s"

[wireguard]
private_key = "%s"
listen_port = %d

[[wireguard.peers]]
public_key = "%s"
allowed_ips = ["%s/32"]

[log]
level = "info"
`, pskB, mcConcB1IP, mcConcB2IP, concBPriv, mcListenPort, edgePub, mcEdgeInner))

	// The edge: two source-pinned uplinks, a loopback [monitor] endpoint, and TWO
	// exit-capable (mode=default-route) peers — exit-a with a single endpoint, exit-b
	// with an ordered two-endpoint list. Each exit lists the SAME default-route entry
	// plus its OWN inner /32 (R255). The top-level psk is a throwaway (a multi-peer edge
	// draws each bond's authenticator from the per-peer psk).
	edgeCfg := writeConfig(t, filepath.Join(dir, "edge.toml"), fmt.Sprintf(`role = "edge"
psk = "%s"

[[paths]]
name = "up1"
source_addr = "%s"
bind = "source"

[[paths]]
name = "up2"
source_addr = "%s"
bind = "source"

[monitor]
listen = "%s"

[wireguard]
private_key = "%s"

[[wireguard.peers]]
public_key = "%s"
name = "%s"
psk = "%s"
mode = "default-route"
endpoint = "%s:%d"
allowed_ips = ["0.0.0.0/0", "%s/32"]

[[wireguard.peers]]
public_key = "%s"
name = "%s"
psk = "%s"
mode = "default-route"
endpoints = ["%s:%d", "%s:%d"]
allowed_ips = ["0.0.0.0/0", "%s/32"]

[log]
level = "info"
`, randKey(t),
		mcEdgeUp1IP, mcEdgeUp2IP,
		mcMonitorListen,
		edgePriv,
		concAPub, mcPeerA, pskA, mcConcAIP, mcListenPort, mcConcAInner,
		concBPub, mcPeerB, pskB, mcConcB1IP, mcListenPort, mcConcB2IP, mcListenPort, mcConcBInner))

	// Concentrators first (they must listen before the edge initiates), then the edge.
	concA.startDaemon(t, bin, concACfg)
	concB.startDaemon(t, bin, concBCfg)
	edge := base.startProc(t, "edge", bin, "--config", edgeCfg)

	if !base.waitLink(tunDev, false, 5*time.Second) {
		t.Fatalf("edge %s never appeared\n%s", tunDev, edge.log())
	}
	if !concA.waitTUN(5 * time.Second) {
		t.Fatalf("conc A %s never appeared\n%s", tunDev, concA.proc.log())
	}
	if !concB.waitTUN(5 * time.Second) {
		t.Fatalf("conc B %s never appeared\n%s", tunDev, concB.proc.log())
	}

	// Address the inner overlay: edge in the base namespace, each concentrator in its own.
	base.run("ip", "addr", "add", mcEdgeInner+"/24", "dev", tunDev)
	base.run("ip", "link", "set", tunDev, "up")
	concA.run("ip", "addr", "add", mcConcAInner+"/24", "dev", tunDev)
	concA.run("ip", "link", "set", tunDev, "up")
	concB.run("ip", "addr", "add", mcConcBInner+"/24", "dev", tunDev)
	concB.run("ip", "link", "set", tunDev, "up")

	return &mcFixture{t: t, base: base, edge: edge, concA: concA, concB: concB}
}

// startConcHolder opens a fresh network namespace, wires a veth pair from the shared
// bridge into it, assigns the given underlay address(es), and brings lo + both ends up.
// It returns a concNS bound to the holder PID so the caller runs the daemon inside it.
func startConcHolder(t *testing.T, base *Topology, name, portVeth, ceth string, addrs []string) *concNS {
	t.Helper()
	holder := exec.Command("unshare", "-n", "sleep", "600")
	if err := holder.Start(); err != nil {
		t.Fatalf("start %s netns holder: %v", name, err)
	}
	pid := holder.Process.Pid
	t.Cleanup(func() {
		_ = holder.Process.Kill()
		_, _ = holder.Process.Wait()
	})

	c := &concNS{t: t, base: base, name: name, pid: pid, holder: holder, ceth: ceth}
	(&Topology{t: t, pid: pid}).waitForNetns()

	// veth pair: bridge-side end onto the bridge, other end into the concentrator netns.
	base.run("ip", "link", "add", portVeth, "type", "veth", "peer", "name", ceth)
	base.run("ip", "link", "set", ceth, "netns", strconv.Itoa(pid))
	base.run("ip", "link", "set", portVeth, "master", mcBridge)
	base.run("ip", "link", "set", portVeth, "up")

	// The netns move is async w.r.t. in-namespace addressing (D33): retry the FIRST
	// address add (bounded) so setup waits for the device to be genuinely usable.
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := c.tryRun("ip", "addr", "add", addrs[0]+"/24", "dev", ceth)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: could not add %s/24 to %q in netns %d within 5s: %v", name, addrs[0], ceth, pid, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	// promote_secondaries: when a concentrator carries multiple same-subnet endpoints
	// (exit-b's .3 + .4), removing the PRIMARY at runtime (delAddr — killing an endpoint)
	// would ALSO delete every SECONDARY on that subnet by default, taking the whole
	// concentrator down instead of just one endpoint. Setting this makes the kernel PROMOTE
	// a secondary to primary instead, so killing exit-b's active endpoint leaves its standby
	// endpoint reachable — the premise of the Q72 within-concentrator failover phase.
	c.run("sysctl", "-w", "net.ipv4.conf."+ceth+".promote_secondaries=1")
	for _, a := range addrs[1:] {
		c.run("ip", "addr", "add", a+"/24", "dev", ceth)
	}
	c.run("ip", "link", "set", "lo", "up")
	c.run("ip", "link", "set", ceth, "up")
	return c
}

// startDaemon starts the concentrator daemon inside this namespace and waits for it to
// launch (the caller addresses its TUN once waitTUN passes).
func (c *concNS) startDaemon(t *testing.T, bin, cfgPath string) {
	t.Helper()
	c.proc = c.base.startProc(t, c.name, "nsenter", "-t", strconv.Itoa(c.pid), "-n", bin, "--config", cfgPath)
}

// waitTUN polls until this concentrator's wanbond0 exists in its namespace.
func (c *concNS) waitTUN(d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if c.tryRun("ip", "link", "show", tunDev) == nil {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// delAddr removes an underlay address from this concentrator's veth, simulating one of
// its listen endpoints dying (the edge's probes to it go unanswered while any other
// address on the same interface stays reachable).
func (c *concNS) delAddr(ip string) { c.run("ip", "addr", "del", ip+"/24", "dev", c.ceth) }

// addAddr restores a previously-removed underlay address (a partial concentrator recovery).
func (c *concNS) addAddr(ip string) { c.run("ip", "addr", "add", ip+"/24", "dev", c.ceth) }

// innerRxBytes reads this concentrator's wanbond0 receive byte counter from
// /proc/net/dev INSIDE its namespace — the inner-interface egress observation point.
// Decrypted default-route traffic the concentrator terminates increments it even though
// the concentrator forwards nothing (the Q41 boundary).
func (c *concNS) innerRxBytes(t *testing.T) uint64 {
	t.Helper()
	out := c.runOut("cat", "/proc/net/dev")
	for _, line := range strings.Split(out, "\n") {
		iface, stats, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(iface) != tunDev {
			continue
		}
		fields := strings.Fields(stats)
		if len(fields) == 0 {
			t.Fatalf("%s: malformed /proc/net/dev line for %s: %q", c.name, tunDev, line)
		}
		v, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			t.Fatalf("%s: parse %s rx bytes %q: %v", c.name, tunDev, fields[0], err)
		}
		return v
	}
	t.Fatalf("%s: no %s line in /proc/net/dev:\n%s", c.name, tunDev, out)
	return 0
}

func (c *concNS) run(args ...string) {
	c.t.Helper()
	full := append([]string{"-t", strconv.Itoa(c.pid), "-n"}, args...)
	if out, err := exec.Command("nsenter", full...).CombinedOutput(); err != nil {
		c.t.Fatalf("%s: nsenter %s: %v\n%s", c.name, strings.Join(args, " "), err, out)
	}
}

func (c *concNS) tryRun(args ...string) error {
	full := append([]string{"-t", strconv.Itoa(c.pid), "-n"}, args...)
	return exec.Command("nsenter", full...).Run()
}

func (c *concNS) runOut(args ...string) string {
	c.t.Helper()
	full := append([]string{"-t", strconv.Itoa(c.pid), "-n"}, args...)
	out, err := exec.Command("nsenter", full...).CombinedOutput()
	if err != nil {
		c.t.Fatalf("%s: nsenter %s: %v\n%s", c.name, strings.Join(args, " "), err, out)
	}
	return string(out)
}

// ---- egress observation ----

// sampleEgress measures each concentrator's inner-interface rx byte growth over window,
// returning (exit-a Δrx, exit-b Δrx). The active exit carries the default-route ping
// stream on top of small probe/keepalive traffic; the standby carries only the latter.
func (f *mcFixture) sampleEgress(t *testing.T, window time.Duration) (aDelta, bDelta uint64) {
	t.Helper()
	a0 := f.concA.innerRxBytes(t)
	b0 := f.concB.innerRxBytes(t)
	time.Sleep(window)
	return f.concA.innerRxBytes(t) - a0, f.concB.innerRxBytes(t) - b0
}

// edgeDevLine returns the /proc/net/dev counter line for dev in the base (edge) netns —
// a diagnostic for the phase-2 failure path (is the edge sending inner traffic at all).
func edgeDevLine(t *testing.T, dev string) string {
	t.Helper()
	out, err := exec.Command("cat", "/proc/net/dev").CombinedOutput()
	if err != nil {
		return fmt.Sprintf("read /proc/net/dev: %v", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if iface, _, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(iface) == dev {
			return strings.TrimSpace(line)
		}
	}
	return dev + " not found in /proc/net/dev"
}

// egressDominant reports whether activeDelta decisively dominates standbyDelta: above
// the noise floor and at least mcEgressDominanceRatio times the standby's growth.
func egressDominant(activeDelta, standbyDelta uint64) bool {
	if activeDelta < mcEgressFloorBytes {
		return false
	}
	return float64(activeDelta) >= mcEgressDominanceRatio*float64(standbyDelta)
}

// waitEgress polls the inner-interface rx split until the default-route egress is
// decisively on wantPeer (exit-a or exit-b), or fails at budget with the last sample.
func waitEgress(t *testing.T, f *mcFixture, wantPeer string, budget time.Duration, msg string) {
	t.Helper()
	deadline := time.Now().Add(budget)
	var aDelta, bDelta uint64
	for time.Now().Before(deadline) {
		aDelta, bDelta = f.sampleEgress(t, mcEgressSampleWindow)
		switch wantPeer {
		case mcPeerA:
			if egressDominant(aDelta, bDelta) {
				return
			}
		case mcPeerB:
			if egressDominant(bDelta, aDelta) {
				return
			}
		}
	}
	t.Fatalf("%s within %v: last sample exit-a Δrx=%d, exit-b Δrx=%d (want %q dominant)\n--- edge ---\n%s",
		msg, budget, aDelta, bDelta, wantPeer, f.edge.log())
}

// ---- monitor snapshot (websocket) ----

// dialMonitor dials the edge's loopback [monitor] /ws endpoint and returns a
// read-one-frame function plus a cleanup. The edge runs in the base (test) namespace,
// so 127.0.0.1 reaches it directly. It retries the dial briefly so a just-started
// endpoint is tolerated.
func (f *mcFixture) dialMonitor(t *testing.T) (readSnap func(*testing.T) monitor.MonitorSnapshot, cleanup func()) {
	t.Helper()
	var c *websocket.Conn
	deadline := time.Now().Add(10 * time.Second)
	for {
		dialCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		conn, resp, err := websocket.Dial(dialCtx, mcMonitorWSURL, nil)
		cancel()
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		if err == nil {
			c = conn
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("dial monitor %s: %v\n--- edge ---\n%s", mcMonitorWSURL, err, f.edge.log())
		}
		time.Sleep(200 * time.Millisecond)
	}

	readSnap = func(t *testing.T) monitor.MonitorSnapshot {
		t.Helper()
		readCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		typ, data, err := c.Read(readCtx)
		if err != nil {
			t.Fatalf("read monitor frame: %v", err)
		}
		if typ != websocket.MessageText {
			t.Fatalf("monitor frame type = %v, want text", typ)
		}
		var snap monitor.MonitorSnapshot
		if err := json.Unmarshal(data, &snap); err != nil {
			t.Fatalf("unmarshal MonitorSnapshot: %v (payload=%s)", err, data)
		}
		return snap
	}
	cleanup = func() { _ = c.CloseNow() }
	return readSnap, cleanup
}

// readSnapUntil reads pushed frames until pred holds or budget elapses, returning the
// satisfying frame (or failing with the last-seen one — a frozen feed never satisfies a
// mutation predicate).
func readSnapUntil(t *testing.T, readSnap func(*testing.T) monitor.MonitorSnapshot, desc string, budget time.Duration, pred func(monitor.MonitorSnapshot) bool) monitor.MonitorSnapshot {
	t.Helper()
	deadline := time.Now().Add(budget)
	var last monitor.MonitorSnapshot
	for time.Now().Before(deadline) {
		last = readSnap(t)
		if pred(last) {
			return last
		}
	}
	t.Fatalf("no monitor frame satisfied %q within %v; last frame = %+v", desc, budget, last)
	return last
}

// postExit POSTs {"peer":peer} to the loopback exit-control endpoint and returns the
// resulting activeExit, failing on any non-200 response.
func (f *mcFixture) postExit(t *testing.T, peer string) string {
	t.Helper()
	body, err := json.Marshal(map[string]string{"peer": peer})
	if err != nil {
		t.Fatalf("marshal exit request: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, mcExitURL, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build exit request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", mcExitURL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST %s peer=%q: status %d, body %s", mcExitURL, peer, resp.StatusCode, buf.String())
	}
	var out struct {
		ActiveExit string `json:"activeExit"`
	}
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("decode exit response %q: %v", buf.String(), err)
	}
	return out.ActiveExit
}

// ---- snapshot predicates ----

func peerSessionEstablished(s monitor.MonitorSnapshot, peer string) bool {
	for _, ps := range s.PeerSessions {
		if ps.Peer == peer {
			return ps.Established
		}
	}
	return false
}

func peerPathCount(s monitor.MonitorSnapshot, peer string) int {
	n := 0
	for _, p := range s.Paths {
		if p.Peer == peer {
			n++
		}
	}
	return n
}

func fecPeers(s monitor.MonitorSnapshot) []string {
	out := make([]string, 0, len(s.FEC))
	for _, f := range s.FEC {
		out = append(out, f.Peer)
	}
	return out
}

func reseqPeers(s monitor.MonitorSnapshot) []string {
	out := make([]string, 0, len(s.Reseq))
	for _, r := range s.Reseq {
		out = append(out, r.Peer)
	}
	return out
}

func hasPeer(names []string, peer string) bool {
	for _, n := range names {
		if n == peer {
			return true
		}
	}
	return false
}

func peerEndpointCount(s monitor.MonitorSnapshot, peer string) int {
	n := 0
	for _, e := range s.Endpoints {
		if e.Peer == peer {
			n++
		}
	}
	return n
}

// activeEndpointAddr returns the address of peer's currently-active endpoint in the
// snapshot, or "" if none is marked active for that peer.
func activeEndpointAddr(s monitor.MonitorSnapshot, peer string) string {
	for _, e := range s.Endpoints {
		if e.Peer == peer && e.Active {
			return e.Address
		}
	}
	return ""
}

// assertOneActiveEndpointPerPeer fails unless each exit peer's endpoint group has
// EXACTLY one active entry (the ordered active/standby shape).
func assertOneActiveEndpointPerPeer(t *testing.T, s monitor.MonitorSnapshot) {
	t.Helper()
	active := map[string]int{}
	for _, e := range s.Endpoints {
		if e.Active {
			active[e.Peer]++
		}
	}
	for _, peer := range []string{mcPeerA, mcPeerB} {
		if active[peer] != 1 {
			t.Fatalf("(4) exit %q has %d active endpoints, want exactly 1: %+v", peer, active[peer], s.Endpoints)
		}
	}
}

// ---- edge journal predicates ----

// edgeLoggedText reports whether any edge journal line contains want (a JSON slog
// record is one line, so a substring match on the message is sufficient).
func edgeLoggedText(logText, want string) bool {
	return strings.Contains(logText, want)
}

// edgeLoggedAutoPromotion reports whether the edge journal recorded an exit switch to
// toPeer with reason=auto-promotion (the Q75 promotion, distinct from a manual switch).
func edgeLoggedAutoPromotion(logText, toPeer string) bool {
	for _, line := range strings.Split(logText, "\n") {
		if strings.Contains(line, "active exit switched") &&
			strings.Contains(line, `"reason":"auto-promotion"`) &&
			strings.Contains(line, `"to":"`+toPeer+`"`) {
			return true
		}
	}
	return false
}
