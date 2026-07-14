//go:build e2e

package e2e

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/metrics"
)

// T97 is the privileged netns e2e for the G4 multi-peer concentrator: ONE concentrator
// terminates TWO edges, each edge bonded across its OWN pair of uplinks, and the test
// proves the peers stay ISOLATED end to end — the threat model of the multi-tenant hub:
//
//   - each edge's inner stream verifies INDEPENDENTLY (a bulk TCP transfer through each
//     tunnel completes; TCP's own integrity check means a cross-peer corruption would
//     surface as a reset / zero bytes, so two positive transfers prove no cross-talk);
//   - one edge's disruption (kill+restart) does NOT interrupt the other's live tunnel;
//   - return traffic routes to the CORRECT edge (the concentrator's WireGuard allowed_ips
//     crypto-routing sends each inner /32 back to its own peer);
//   - an edge NAT-rebind (source-address move on one uplink) recovers via the concentrator
//     re-learning the roamed endpoint from the next authenticated PROBE (T37), while the
//     other edge is undisturbed;
//   - a spoofed unbound-source UDP flood at the listen port degrades only bootstrap
//     latency (report-only, M10/Q12) WITHOUT evicting a live peer or corrupting a
//     cross-peer stream.
//
// It also scrapes the concentrator /metrics and asserts the T94 PER-PEER-labelled series
// attribute traffic to the correct edge for BOTH peers.
//
// EXECUTION IS DEFERRED (G2 pattern). The netns tier needs the privileged two-namespace
// fixture + CAP_NET_ADMIN and is NOT run in the unit environment; this file must COMPILE
// and vet under -tags e2e (`go vet -tags e2e ./test/e2e` + `go build -tags e2e ./test/e2e`)
// and is executed on the o3.7mind.io (aarch64) + llm-ubuntu-0 (amd64) hosts.
//
// TOPOLOGY (why bridges). The concentrator runs in the BASE (test-process) network
// namespace — so its 127.0.0.1 /metrics endpoint is directly scrapeable by the test — and
// owns two uplink source addresses, one per emulated WAN /24. Each edge is its OWN network
// namespace (a PID-addressed holder, like the base fixture and hub_failover) with its own
// `wanbond0` TUN, bonded across two uplinks. Each WAN /24 is an L2 bridge in the base
// namespace carrying the concentrator's uplink address and BOTH edges' uplink on that WAN,
// so every edge reaches both concentrator uplinks without any routing:
//
//	base netns (concentrator, inner 10.10.0.2):
//	   bridge wbMp1 = 10.101.1.254/24 (conc uplink "wan1")   bridge wbMp2 = 10.101.2.254/24 (conc uplink "wan2")
//	        │                    │                                 │                    │
//	   edgeA wan1           edgeB wan1                         edgeA wan2           edgeB wan2
//	   10.101.1.1           10.101.1.2                         10.101.2.1           10.101.2.2
//	   ── edgeA netns (inner 10.10.0.1) ──                     ── edgeB netns (inner 10.10.0.3) ──
//
// The two uplinks carry DISTINCT netem delay (20ms vs 55ms) so a bonded edge's frames
// arrive out of order and exercise the connection-scoped resequencer independently per
// peer. Each edge's outer-control PSK (its top-level psk) equals that edge's per-peer psk
// in the concentrator config (T81: multi-peer requires present, pairwise-distinct per-peer
// psks), so the concentrator authenticates each edge's probe plane with the right key.
//
// PER-PEER METRIC LABEL. On a multi-peer concentrator EVERY bound peer — including the
// first-configured one — carries its configured name as the `peer` label (D58):
// device.Up plumbs ids[0].Name into the primary's peerState (SetPrimaryPeerName)
// whenever more than one peer is configured, so peer="" appears only on a true
// single-peer exposition. Edge A, configured FIRST, therefore surfaces under
// `peer="edge-alpha"`, and edge B under `peer="edge-beta"`. The two labels are DISTINCT,
// which is all the attribution assertions need.
const (
	// WAN-1 fabric: bridge + concentrator uplink address (the "wan1" path source_addr).
	mpBr1     = "wbMp1"
	mpConc1IP = "10.101.1.254"
	mpConc1EP = mpConc1IP + ":51820"
	// WAN-2 fabric.
	mpBr2     = "wbMp2"
	mpConc2IP = "10.101.2.254"
	mpConc2EP = mpConc2IP + ":51820"

	// Edge A uplinks (bridge-port veth / in-namespace veth / address), one per WAN.
	mpA1Port = "wbMpA1p"
	mpA1Edge = "wbMpA1e"
	mpA1IP   = "10.101.1.1"
	mpA2Port = "wbMpA2p"
	mpA2Edge = "wbMpA2e"
	mpA2IP   = "10.101.2.1"
	// Edge B uplinks.
	mpB1Port = "wbMpB1p"
	mpB1Edge = "wbMpB1e"
	mpB1IP   = "10.101.1.2"
	mpB2Port = "wbMpB2p"
	mpB2Edge = "wbMpB2e"
	mpB2IP   = "10.101.2.2"

	// Inner overlay: the concentrator serves 10.10.0.2 (reused concInner); each edge owns
	// its own inner /32 so the concentrator's per-peer allowed_ips crypto-routes replies
	// to the correct peer. edgeInner (10.10.0.1) is edge A; edge B is a fresh address.
	mpEdgeAInner = edgeInner // 10.10.0.1
	mpEdgeBInner = "10.10.0.3"

	// Concentrator path names (the metrics `path` label) — shared by both peers' per-path
	// series, since both peers arrive on the concentrator's same two uplink sockets.
	mpWan1 = "wan1"
	mpWan2 = "wan2"

	// The two edges' configured peer names on the concentrator. edge A is configured FIRST
	// (the primary) but — since D58 — still surfaces under its OWN configured name, exactly
	// like edge B (see the per-peer-label note above).
	mpPeerAConfigName = "edge-alpha"
	mpPeerBConfigName = "edge-beta"
	mpPeerALabel      = mpPeerAConfigName
	mpPeerBLabel      = mpPeerBConfigName

	// Per-uplink netem delay (ms). Distinct across a bond so frames reorder and the
	// resequencer is genuinely exercised per peer.
	mpDelayWan1 = 20
	mpDelayWan2 = 55

	// A spare unenrolled source on the wan1 bridge, from which the spoofed flood is sent:
	// a real address on the fabric that is NOT a configured peer, so its datagrams reach
	// the listen port but establish no session (the "unbound source" of the threat model).
	mpFloodSrcIP = "10.101.1.9"

	// mpMetricsListen is the CONCENTRATOR's /metrics endpoint for this file, on a port no
	// other e2e file uses (see the metrics-port registry in netns.go).
	mpMetricsListen = "127.0.0.1:9102"
	mpMetricsURL    = "http://" + mpMetricsListen + "/metrics"

	// iperf3 server ports for the two edges' concurrent transfers (a one-shot `-s -1`
	// server serves ONE client, so concurrent A+B transfers need distinct ports).
	mpIperfPortA = 5301
	mpIperfPortB = 5302
)

// mpBringUpDeadline bounds how long a freshly-started edge daemon may take to complete its
// handshake and carry inner traffic. Generous: it must cover binary start, the WG initial
// handshake, and the first probe cadence tick on the shared, CPU/PPS-bound netns fixture.
const mpBringUpDeadline = 20 * time.Second

// mpEdge is one edge namespace: a PID-addressed holder running a wanbond edge daemon over
// two uplinks, plus the handles the test needs to drive traffic INTO its namespace and
// restart/roam it. It mirrors the base Topology's nsenter discipline but is edge-scoped.
type mpEdge struct {
	t         *testing.T
	base      *Topology // for startProc (daemon launched via nsenter into this edge's netns)
	name      string
	pid       int
	holder    *exec.Cmd
	bin       string
	cfgPath   string
	inner     string
	peerLabel string
	proc      *proc
}

// TestMultiPeerConcentratorIsolation is the T97 acceptance (`-run MultiPeer` selects it).
// It stands up the concentrator + two bonded edges once, then runs the isolation phases in
// order against the shared fixture: independent inner streams, one-edge-restart leaves the
// other uninterrupted, per-peer /metrics attribution, single-edge NAT-rebind recovery with
// the other undisturbed, and a spoofed unbound-source flood that evicts no live peer.
func TestMultiPeerConcentratorIsolation(t *testing.T) {
	mp := setupMultiPeer(t)

	// (0) Both bonds up: each edge reaches the concentrator's inner address, and the
	// concentrator's per-peer path_up series read 1 for BOTH peers on BOTH uplinks.
	if !mp.edgeA.pingUntil(concInner, mpBringUpDeadline) {
		t.Fatalf("edge A bond never came up\n--- edge A ---\n%s\n--- conc ---\n%s", mp.edgeA.proc.log(), mp.conc.log())
	}
	if !mp.edgeB.pingUntil(concInner, mpBringUpDeadline) {
		t.Fatalf("edge B bond never came up\n--- edge B ---\n%s\n--- conc ---\n%s", mp.edgeB.proc.log(), mp.conc.log())
	}
	for _, pl := range []string{mpPeerALabel, mpPeerBLabel} {
		for _, wan := range []string{mpWan1, mpWan2} {
			waitPeerPathUp(t, mpMetricsURL, pl, wan, 1, mpBringUpDeadline)
		}
	}

	t.Run("independent-inner-streams", func(t *testing.T) {
		// A bulk TCP transfer through EACH tunnel, concurrently. Both completing with
		// positive throughput proves the two inner streams verify independently: TCP's
		// checksum means any cross-peer byte corruption would reset the connection or
		// truncate the transfer, so two positive results are unambiguous no-cross-talk.
		xa := mp.edgeA.startTransfer(t, concInner, mpIperfPortA, 6)
		xb := mp.edgeB.startTransfer(t, concInner, mpIperfPortB, 6)
		if mbps := xa.result(t); mbps <= 0 {
			t.Fatalf("edge A inner transfer measured non-positive throughput %.2f Mbit/s", mbps)
		}
		if mbps := xb.result(t); mbps <= 0 {
			t.Fatalf("edge B inner transfer measured non-positive throughput %.2f Mbit/s", mbps)
		}

		// Per-peer attribution: after both edges pushed traffic, the concentrator's
		// per-(peer,path) byte counters must be positive for BOTH peers, and the
		// per-peer resequencer series must exist for both (the two uplinks' distinct
		// delay reorders frames the connection-scoped resequencer releases). Absolute
		// counts are report-only (M10/Q12); the asserted invariant is per-peer presence.
		exp := scrapeMetrics(t, mpMetricsURL)
		for _, pl := range []string{mpPeerALabel, mpPeerBLabel} {
			var peerBytes float64
			for _, wan := range []string{mpWan1, mpWan2} {
				rx, ok := exp.PeerPathValue(metrics.MetricRxBytes, pl, wan)
				if !ok {
					t.Fatalf("no %s{peer=%q,path=%q} series — per-peer attribution missing", metrics.MetricRxBytes, pl, wan)
				}
				peerBytes += rx
			}
			if peerBytes <= 0 {
				t.Fatalf("peer %q carried non-positive rx bytes across its uplinks — the concentrator did not attribute its traffic", pl)
			}
			if _, ok := exp.PeerValue(metrics.MetricReseqReleased, pl); !ok {
				t.Fatalf("no %s{peer=%q} resequencer series for a bonded peer", metrics.MetricReseqReleased, pl)
			}
			t.Logf("peer %q attributed %.0f rx bytes across wan1+wan2 (report-only)", pl, peerBytes)
		}
	})

	t.Run("edgeA-restart-leaves-edgeB-uninterrupted", func(t *testing.T) {
		// A background transfer through edge B that SPANS edge A's kill+restart. If the
		// concentrator's handling of edge A's teardown/re-handshake perturbed edge B's
		// session, this single TCP connection would reset (result() fails on a non-nil
		// iperf3 exit); a positive result proves edge B was uninterrupted.
		xb := mp.edgeB.startTransfer(t, concInner, mpIperfPortB, 12)

		time.Sleep(1 * time.Second) // let edge B's flow establish and ramp
		mp.edgeA.stop()             // kill edge A's daemon (its TUN + sessions drop)
		t.Logf("killed edge A daemon; edge B transfer must survive the outage + edge A's restart")
		time.Sleep(1 * time.Second) // edge A stays down briefly
		mp.edgeA.startDaemon(t)     // restart in the SAME namespace; re-handshakes fresh

		// Edge A recovers: its bond re-establishes and carries inner traffic again.
		if !mp.edgeA.pingUntil(concInner, mpBringUpDeadline) {
			t.Fatalf("edge A did not recover after restart\n--- edge A ---\n%s\n--- conc ---\n%s", mp.edgeA.proc.log(), mp.conc.log())
		}

		// Edge B's transfer completed across the whole window with no reset.
		if mbps := xb.result(t); mbps <= 0 {
			t.Fatalf("edge B transfer was interrupted by edge A's restart (non-positive %.2f Mbit/s)", mbps)
		}

		// Edge B's per-peer liveness was never torn down (still up on both uplinks).
		exp := scrapeMetrics(t, mpMetricsURL)
		for _, wan := range []string{mpWan1, mpWan2} {
			if v, ok := exp.PeerPathValue(metrics.MetricUp, mpPeerBLabel, wan); !ok || v != 1 {
				t.Fatalf("edge B %s{peer=%q,path=%q} = %v (ok=%v), want 1 — edge B liveness was disturbed by edge A's restart", metrics.MetricUp, mpPeerBLabel, wan, v, ok)
			}
		}
	})

	t.Run("per-peer-metrics-attribute-to-correct-edge", func(t *testing.T) {
		// Drive a short, per-edge amount of inner traffic, then assert the concentrator's
		// per-peer series carry BOTH peer labels with independent counters. This is the
		// "attribute traffic to the correct edge" acceptance: the two peers surface as two
		// distinct label sets, each with its own path/FEC/resequencer view.
		if mbps := mp.edgeA.iperf3Mbps(t, concInner, mpIperfPortA, 3); mbps <= 0 {
			t.Fatalf("edge A transfer non-positive %.2f Mbit/s", mbps)
		}
		if mbps := mp.edgeB.iperf3Mbps(t, concInner, mpIperfPortB, 3); mbps <= 0 {
			t.Fatalf("edge B transfer non-positive %.2f Mbit/s", mbps)
		}

		exp := scrapeMetrics(t, mpMetricsURL)
		// Both peers present with a per-peer FEC series (T94 registers per-peer FEC/reseq
		// on a multi-peer Source), attributing to two DISTINCT labels.
		for _, pl := range []string{mpPeerALabel, mpPeerBLabel} {
			if _, ok := exp.PeerValue(metrics.MetricFECData, pl); !ok {
				t.Fatalf("no %s{peer=%q} series — the concentrator is not exposing per-peer FEC for both edges", metrics.MetricFECData, pl)
			}
			var txTotal float64
			for _, wan := range []string{mpWan1, mpWan2} {
				tx, ok := exp.PeerPathValue(metrics.MetricTxBytes, pl, wan)
				if !ok {
					t.Fatalf("no %s{peer=%q,path=%q} — per-peer tx attribution missing", metrics.MetricTxBytes, pl, wan)
				}
				txTotal += tx
			}
			if txTotal <= 0 {
				t.Fatalf("peer %q has non-positive tx bytes — the concentrator did not attribute its return traffic", pl)
			}
		}
		if mpPeerALabel == mpPeerBLabel {
			t.Fatal("the two peers must surface under DISTINCT metric labels")
		}
	})

	t.Run("edgeA-nat-rebind-recovers-edgeB-unaffected", func(t *testing.T) {
		// Move edge A's wan1 uplink to a fresh source address (a NAT rebind / CGNAT churn),
		// while a background transfer through edge B runs. Edge A's device-bound socket
		// keeps sending from the new source; the concentrator re-learns the endpoint from
		// the next authenticated probe (T37). Edge B must be undisturbed throughout.
		xb := mp.edgeB.startTransfer(t, concInner, mpIperfPortB, 10)
		time.Sleep(1 * time.Second)

		newIP := reHost(mpA1IP, 121)
		t.Logf("edge A NAT-rebind: wan1 source %s -> %s (mid-flow)", mpA1IP, newIP)
		mp.edgeA.readdressUplink(mpA1Edge, newIP)

		// Prove the RE-BOUND wan1 itself recovered (not merely that edge A survived on
		// wan2): once wan1 is roamed, bring edge A's wan2 DOWN so only the re-bound wan1
		// can carry traffic — a successful ping then means the concentrator re-learned the
		// roamed wan1 endpoint. Mirrors the T16 roaming test's unambiguous proof.
		mp.edgeA.setUplink(mpA2Edge, false)
		if !mp.edgeA.pingUntil(concInner, time.Duration(P1RecoverySeconds)*time.Second+3*time.Second) {
			t.Fatalf("edge A wan1 did not recover on its new source %s (wan2 down)\n--- edge A ---\n%s\n--- conc ---\n%s", newIP, mp.edgeA.proc.log(), mp.conc.log())
		}
		mp.edgeA.setUplink(mpA2Edge, true) // restore edge A's second uplink

		// Edge B was unaffected: its transfer completed with no reset.
		if mbps := xb.result(t); mbps <= 0 {
			t.Fatalf("edge B transfer was disturbed by edge A's NAT-rebind (non-positive %.2f Mbit/s)", mbps)
		}
	})

	t.Run("spoofed-unbound-source-flood-evicts-no-live-peer", func(t *testing.T) {
		// Blast garbage UDP at the concentrator's wan1 listen socket from an unenrolled
		// source on the fabric (no established session). The threat model (M10/Q12): this
		// may degrade BOOTSTRAP latency for a NEW peer (report-only — not asserted here,
		// as there is no fresh bootstrapping peer in this fixture), but it must NOT evict a
		// LIVE peer or corrupt a cross-peer stream.
		const floodPackets = 3000
		floodUnboundSource(t, mpFloodSrcIP, mpConc1EP, floodPackets)
		t.Logf("sent %d spoofed unbound-source datagrams at %s (bootstrap-latency impact is report-only)", floodPackets, mpConc1EP)

		// Neither live peer was evicted: both still carry inner traffic, and both peers'
		// per-uplink liveness still reads up.
		if !mp.edgeA.pingUntil(concInner, mpBringUpDeadline) {
			t.Fatalf("edge A evicted by the unbound-source flood\n--- conc ---\n%s", mp.conc.log())
		}
		if !mp.edgeB.pingUntil(concInner, mpBringUpDeadline) {
			t.Fatalf("edge B evicted by the unbound-source flood\n--- conc ---\n%s", mp.conc.log())
		}
		exp := scrapeMetrics(t, mpMetricsURL)
		for _, pl := range []string{mpPeerALabel, mpPeerBLabel} {
			for _, wan := range []string{mpWan1, mpWan2} {
				if v, ok := exp.PeerPathValue(metrics.MetricUp, pl, wan); !ok || v != 1 {
					t.Fatalf("peer %q %s{path=%q} = %v (ok=%v) after the flood, want 1 — a live peer was evicted", pl, metrics.MetricUp, wan, v, ok)
				}
			}
		}

		// Cross-peer streams remain intact end to end: a fresh transfer through each
		// tunnel still completes positively.
		if mbps := mp.edgeA.iperf3Mbps(t, concInner, mpIperfPortA, 3); mbps <= 0 {
			t.Fatalf("edge A cross-peer stream corrupted after the flood (non-positive %.2f Mbit/s)", mbps)
		}
		if mbps := mp.edgeB.iperf3Mbps(t, concInner, mpIperfPortB, 3); mbps <= 0 {
			t.Fatalf("edge B cross-peer stream corrupted after the flood (non-positive %.2f Mbit/s)", mbps)
		}
	})
}

// setupMultiPeer builds the whole fixture: the two WAN bridges in the base namespace, the
// concentrator daemon (two uplinks, two peers) in the base namespace, and two bonded edge
// daemons each in its own PID-addressed holder namespace. It addresses every TUN and
// returns once both bonds' TUNs exist. All teardown is registered via t.Cleanup.
func setupMultiPeer(t *testing.T) *multiPeerFixture {
	t.Helper()
	bin := buildWanbond(t)

	base := &Topology{t: t}
	base.run("ip", "link", "set", "lo", "up")

	// Idempotent pre-delete of the fixed-name bridges/ports (a prior test's teardown can
	// race the kernel reap in the reused base namespace), then build the two WAN bridges.
	for _, dev := range []string{mpA1Port, mpA2Port, mpB1Port, mpB2Port, mpBr1, mpBr2} {
		_ = base.tryRun("ip", "link", "del", dev)
	}
	for _, br := range []struct{ name, addr string }{{mpBr1, mpConc1IP}, {mpBr2, mpConc2IP}} {
		base.run("ip", "link", "add", br.name, "type", "bridge", "stp_state", "0", "forward_delay", "0")
		base.run("ip", "addr", "add", br.addr+"/24", "dev", br.name)
		base.run("ip", "link", "set", br.name, "up")
	}
	// Spare unenrolled source on the wan1 bridge for the spoofed flood.
	base.run("ip", "addr", "add", mpFloodSrcIP+"/24", "dev", mpBr1)
	t.Cleanup(func() {
		for _, dev := range []string{mpA1Port, mpA2Port, mpB1Port, mpB2Port, mpBr1, mpBr2} {
			_ = base.tryRun("ip", "link", "del", dev)
		}
	})

	// WireGuard identities: one concentrator keypair with two peers; one keypair per edge.
	// Each edge's outer-control PSK (its top-level psk) is ALSO its per-peer psk on the
	// concentrator (T81 multi-peer: present + pairwise-distinct). The concentrator's own
	// top-level psk is unused for demux in multi-peer mode but must be a valid key.
	concPriv, concPub := genKey(t)
	edgeAPriv, edgeAPub := genKey(t)
	edgeBPriv, edgeBPub := genKey(t)
	pskA := randKey(t)
	pskB := randKey(t)
	topPSK := randKey(t)

	dir := t.TempDir()
	concCfg := writeConfig(t, filepath.Join(dir, "conc.toml"), fmt.Sprintf(`role = "concentrator"
psk = "%s"

[[paths]]
name = "%s"
source_addr = "%s"

[[paths]]
name = "%s"
source_addr = "%s"

[metrics]
listen = "%s"

[wireguard]
private_key = "%s"
listen_port = %d

[[wireguard.peers]]
public_key = "%s"
name = "%s"
psk = "%s"
allowed_ips = ["%s/32"]

[[wireguard.peers]]
public_key = "%s"
name = "%s"
psk = "%s"
allowed_ips = ["%s/32"]

[log]
level = "info"
`, topPSK,
		mpWan1, mpConc1IP,
		mpWan2, mpConc2IP,
		mpMetricsListen,
		concPriv, listenPort,
		edgeAPub, mpPeerAConfigName, pskA, mpEdgeAInner,
		edgeBPub, mpPeerBConfigName, pskB, mpEdgeBInner))

	edgeACfg := writeConfig(t, filepath.Join(dir, "edgeA.toml"), mpEdgeConfig(pskA, edgeAPriv, concPub, mpA1IP, mpA2IP))
	edgeBCfg := writeConfig(t, filepath.Join(dir, "edgeB.toml"), mpEdgeConfig(pskB, edgeBPriv, concPub, mpB1IP, mpB2IP))

	// Concentrator first (it must be listening before the edges initiate), in the base
	// namespace so its /metrics endpoint is on the test's own 127.0.0.1.
	conc := base.startProc(t, "concentrator", bin, "--config", concCfg)
	if !base.waitLink(tunDev, false, 5*time.Second) {
		t.Fatalf("concentrator %s never appeared\n%s", tunDev, conc.log())
	}
	base.run("ip", "addr", "add", concInner+"/24", "dev", tunDev)
	base.run("ip", "link", "set", tunDev, "up")

	// Edge A and edge B, each in its own namespace with two uplinks.
	edgeA := newMPEdge(t, base, bin, "edgeA", edgeACfg, mpEdgeAInner, mpPeerALabel,
		[]mpUplink{{mpA1Port, mpA1Edge, mpA1IP, mpDelayWan1}, {mpA2Port, mpA2Edge, mpA2IP, mpDelayWan2}})
	edgeB := newMPEdge(t, base, bin, "edgeB", edgeBCfg, mpEdgeBInner, mpPeerBLabel,
		[]mpUplink{{mpB1Port, mpB1Edge, mpB1IP, mpDelayWan1}, {mpB2Port, mpB2Edge, mpB2IP, mpDelayWan2}})

	return &multiPeerFixture{t: t, base: base, conc: conc, edgeA: edgeA, edgeB: edgeB}
}

// multiPeerFixture is the assembled fixture handed to the test phases.
type multiPeerFixture struct {
	t     *testing.T
	base  *Topology
	conc  *proc
	edgeA *mpEdge
	edgeB *mpEdge
}

// mpUplink describes one edge uplink: the bridge-side veth end (on the WAN bridge), the
// in-namespace veth end (the source-bound interface, netem-shaped), its address, and the
// egress netem delay in ms.
type mpUplink struct {
	port    string
	edge    string
	ip      string
	delayMs int
}

// newMPEdge opens a fresh network namespace, wires each uplink veth pair from the matching
// WAN bridge into it (addressed + netem-shaped), then starts and addresses the edge daemon.
func newMPEdge(t *testing.T, base *Topology, bin, name, cfgPath, inner, peerLabel string, uplinks []mpUplink) *mpEdge {
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

	e := &mpEdge{t: t, base: base, name: name, pid: pid, holder: holder, bin: bin, cfgPath: cfgPath, inner: inner, peerLabel: peerLabel}
	(&Topology{t: t, pid: pid}).waitForNetns()
	e.run("ip", "link", "set", "lo", "up")

	for _, u := range uplinks {
		bridge := mpBridgeForIP(u.ip)
		base.run("ip", "link", "add", u.port, "type", "veth", "peer", "name", u.edge)
		base.run("ip", "link", "set", u.edge, "netns", strconv.Itoa(pid))
		base.run("ip", "link", "set", u.port, "master", bridge)
		base.run("ip", "link", "set", u.port, "up")

		// The netns move is async w.r.t. in-namespace addressing: the device becoming
		// visible does NOT guarantee the immediately-following `addr add` succeeds (D33).
		// Retry the ACTUAL addressing op (bounded) so setup waits for genuine usability.
		deadline := time.Now().Add(5 * time.Second)
		for {
			err := e.tryRun("ip", "addr", "add", u.ip+"/24", "dev", u.edge)
			if err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: could not add %s/24 to %q in netns %d within 5s: %v", name, u.ip, u.edge, pid, err)
			}
			time.Sleep(50 * time.Millisecond)
		}
		e.run("ip", "link", "set", u.edge, "up")
		// Distinct per-uplink egress delay so a bonded edge's frames reorder.
		e.run("tc", "qdisc", "add", "dev", u.edge, "root", "netem", "delay", strconv.Itoa(u.delayMs)+"ms")
	}

	e.startDaemon(t)
	return e
}

// mpBridgeForIP returns the WAN bridge an uplink address belongs to (by its /24).
func mpBridgeForIP(ip string) string {
	if strings.HasPrefix(ip, "10.101.1.") {
		return mpBr1
	}
	return mpBr2
}

// mpEdgeConfig renders an edge config bonded across two uplinks to the concentrator's two
// WAN addresses. Both edges share the concentrator's public key and endpoint; only the psk,
// private key, and per-uplink source addresses differ.
func mpEdgeConfig(psk, edgePriv, concPub, up1Src, up2Src string) string {
	return fmt.Sprintf(`role = "edge"
psk = "%s"

[[paths]]
name = "%s"
source_addr = "%s"
dest_addr = "%s"

[[paths]]
name = "%s"
source_addr = "%s"
dest_addr = "%s"

[wireguard]
private_key = "%s"

[[wireguard.peers]]
public_key = "%s"
endpoint = "%s"
allowed_ips = ["%s/32"]

[log]
level = "info"
`, psk,
		mpWan1, up1Src, mpConc1EP,
		mpWan2, up2Src, mpConc2EP,
		edgePriv,
		concPub, mpConc1EP, concInner)
}

// startDaemon (re)starts the edge daemon inside this edge's namespace and addresses its
// TUN. Used at initial bring-up and by restart (after stop()).
func (e *mpEdge) startDaemon(t *testing.T) {
	t.Helper()
	e.proc = e.base.startProc(t, e.name, "nsenter", "-t", strconv.Itoa(e.pid), "-n", e.bin, "--config", e.cfgPath)
	if !e.waitTUN(5 * time.Second) {
		t.Fatalf("%s %s never appeared\n%s", e.name, tunDev, e.proc.log())
	}
	e.run("ip", "addr", "add", e.inner+"/24", "dev", tunDev)
	e.run("ip", "link", "set", tunDev, "up")
}

// stop kills the edge daemon and waits for it to exit (its TUN + sessions drop with it).
// The original startProc cleanup still fires at test end and tolerates the double-kill.
func (e *mpEdge) stop() {
	if e.proc == nil || e.proc.cmd.Process == nil {
		return
	}
	_ = e.proc.cmd.Process.Kill()
	_, _ = e.proc.cmd.Process.Wait()
}

// waitTUN polls until this edge's wanbond0 exists in its namespace.
func (e *mpEdge) waitTUN(d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if e.tryRun("ip", "link", "show", tunDev) == nil {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// readdressUplink flushes and re-adds an uplink's edge-side address, simulating a NAT
// rebind / edge public-IP move (its netem qdisc is preserved — flush touches addresses,
// not qdiscs).
func (e *mpEdge) readdressUplink(edgeVeth, newIP string) {
	e.run("ip", "addr", "flush", "dev", edgeVeth)
	e.run("ip", "addr", "add", newIP+"/24", "dev", edgeVeth)
	e.run("ip", "link", "set", edgeVeth, "up")
}

// setUplink brings an uplink's edge-side veth up or down (to isolate a single path).
func (e *mpEdge) setUplink(edgeVeth string, up bool) {
	state := "down"
	if up {
		state = "up"
	}
	e.run("ip", "link", "set", edgeVeth, state)
}

// pingUntil pings ip from THIS edge's namespace until it answers or d elapses.
func (e *mpEdge) pingUntil(ip string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if e.tryRun("ping", "-c", "1", "-W", "1", ip) == nil {
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}

// iperf3Mbps runs a one-shot iperf3 TCP transfer from this edge to serverIP (the
// concentrator inner address, served in the base namespace) for secs seconds and returns
// the sender throughput in Mbit/s.
func (e *mpEdge) iperf3Mbps(t *testing.T, serverIP string, port, secs int) float64 {
	t.Helper()
	startBaseIperfServer(t, e.base, serverIP, port)
	out := e.runOut("iperf3", "-c", serverIP, "-p", strconv.Itoa(port), "-t", strconv.Itoa(secs), "-J")
	return parseIperfMbps(t, out)
}

// startTransfer starts a background iperf3 transfer from this edge to serverIP, returning
// immediately; result() awaits it. Used for flows that must SPAN a mid-run disruption.
func (e *mpEdge) startTransfer(t *testing.T, serverIP string, port, secs int) *transfer {
	t.Helper()
	startBaseIperfServer(t, e.base, serverIP, port)
	cmd := exec.Command("nsenter", "-t", strconv.Itoa(e.pid), "-n", "iperf3", "-c", serverIP, "-p", strconv.Itoa(port), "-t", strconv.Itoa(secs), "-J")
	out := &lockedBuffer{}
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		t.Fatalf("%s: start iperf3 client: %v", e.name, err)
	}
	return &transfer{cmd: cmd, out: out}
}

// run executes a command inside this edge's namespace, failing the test on error.
func (e *mpEdge) run(args ...string) {
	e.t.Helper()
	full := append([]string{"-t", strconv.Itoa(e.pid), "-n"}, args...)
	if out, err := exec.Command("nsenter", full...).CombinedOutput(); err != nil {
		e.t.Fatalf("%s: nsenter %s: %v\n%s", e.name, strings.Join(args, " "), err, out)
	}
}

// tryRun executes a command inside this edge's namespace, returning the error (no fatal).
func (e *mpEdge) tryRun(args ...string) error {
	full := append([]string{"-t", strconv.Itoa(e.pid), "-n"}, args...)
	return exec.Command("nsenter", full...).Run()
}

// runOut executes a command inside this edge's namespace and returns its combined output,
// failing the test on error.
func (e *mpEdge) runOut(args ...string) string {
	e.t.Helper()
	full := append([]string{"-t", strconv.Itoa(e.pid), "-n"}, args...)
	out, err := exec.Command("nsenter", full...).CombinedOutput()
	if err != nil {
		e.t.Fatalf("%s: nsenter %s: %v\n%s", e.name, strings.Join(args, " "), err, out)
	}
	return string(out)
}

// startBaseIperfServer starts a one-shot iperf3 server bound to serverIP:port in the base
// (concentrator) namespace and returns once it reaches LISTEN.
func startBaseIperfServer(t *testing.T, base *Topology, serverIP string, port int) {
	t.Helper()
	base.startProc(t, "iperf3-server", "iperf3", "-s", "-1", "-B", serverIP, "-p", strconv.Itoa(port))
	baseWaitIperfListen(t, port)
}

// baseWaitIperfListen polls the base namespace for a TCP LISTEN socket on port (never
// connecting — the server is one-shot), failing the test at iperfListenTimeout.
func baseWaitIperfListen(t *testing.T, port int) {
	t.Helper()
	suffix := ":" + strconv.Itoa(port)
	deadline := time.Now().Add(iperfListenTimeout)
	for time.Now().Before(deadline) {
		out, err := exec.Command("ss", "-ltn").CombinedOutput()
		if err == nil {
			for _, line := range strings.Split(string(out), "\n") {
				fields := strings.Fields(line)
				if len(fields) >= 4 && strings.HasSuffix(fields[3], suffix) {
					return
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("base iperf3 server never reached LISTEN on port %d within %s", port, iperfListenTimeout)
}

// parseIperfMbps extracts the sender throughput (Mbit/s) from iperf3 -J output.
func parseIperfMbps(t *testing.T, out string) float64 {
	t.Helper()
	var r struct {
		End struct {
			SumSent struct {
				BitsPerSecond float64 `json:"bits_per_second"`
			} `json:"sum_sent"`
		} `json:"end"`
	}
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("parse iperf3 json: %v\n%s", err, out)
	}
	return r.End.SumSent.BitsPerSecond / 1e6
}

// waitPeerPathUp polls url's /metrics until the PER-PEER wanbond_path_up series for the
// given (peer,path) reads want, or fails at deadline. A mid-poll scrape error is tolerated
// (transient). It is the T94 per-peer analogue of waitPathUp.
func waitPeerPathUp(t *testing.T, url, peer, path string, want float64, deadline time.Duration) {
	t.Helper()
	end := time.Now().Add(deadline)
	var lastErr error
	for time.Now().Before(end) {
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		exp, err := metrics.Fetch(ctx, http.DefaultClient, url)
		cancel()
		if err != nil {
			lastErr = err
		} else if v, ok := exp.PeerPathValue(metrics.MetricUp, peer, path); ok && v == want {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("%s{peer=%q,path=%q} never reached %v within %s (last scrape error: %v)", metrics.MetricUp, peer, path, want, deadline, lastErr)
}

// floodUnboundSource sends count garbage UDP datagrams to dstAddr from localAddr — a real,
// unenrolled source on the fabric, so the datagrams reach the listen port but establish no
// session (the "spoofed unbound source" of the threat model). It runs in the base namespace
// (the test process's namespace, where both localAddr and the concentrator listen).
func floodUnboundSource(t *testing.T, localAddr, dstAddr string, count int) {
	t.Helper()
	la, err := net.ResolveUDPAddr("udp", localAddr+":0")
	if err != nil {
		t.Fatalf("resolve flood source %s: %v", localAddr, err)
	}
	ra, err := net.ResolveUDPAddr("udp", dstAddr)
	if err != nil {
		t.Fatalf("resolve flood dest %s: %v", dstAddr, err)
	}
	conn, err := net.DialUDP("udp", la, ra)
	if err != nil {
		t.Fatalf("dial flood socket %s->%s: %v", localAddr, dstAddr, err)
	}
	defer func() { _ = conn.Close() }()
	junk := make([]byte, 128)
	if _, err := rand.Read(junk); err != nil {
		t.Fatalf("read flood payload: %v", err)
	}
	for i := 0; i < count; i++ {
		if _, err := conn.Write(junk); err != nil {
			// A transient ENOBUFS under a tight loop is not a test failure — the flood is a
			// best-effort saturation, not an exact packet count.
			break
		}
	}
}
