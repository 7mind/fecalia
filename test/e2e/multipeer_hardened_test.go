//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	awgdevice "github.com/amnezia-vpn/amneziawg-go/device"

	"github.com/7mind/wanbond/internal/metrics"
)

// T128 extends the G4 multi-peer concentrator netns e2e (multipeer_test.go/T97) with the
// externally-observable HARDENING behaviours goals:G8 adds on top of peer isolation:
//
//   - D47 (shared-NAT demux): two edge peers that egress from ONE apparent public IP — the
//     CGNAT/carrier-NAT threat model — still get DISTINCT concentrator sessions and carry
//     traffic simultaneously, because the source-demux key is the FULL AddrPort (address AND
//     port), not the bare address. This fixture builds a REAL Linux SNAT/conntrack NAT
//     gateway (not a simulated one) in the base netns: two edges in their OWN private
//     namespaces are source-NATed to the SAME apparent address as they transit toward the
//     concentrator, exactly like two devices behind one home/carrier NAT box. Conntrack
//     assigns each edge's flow a DISTINCT translated port automatically — verified against a
//     live kernel during development (unprivileged userns): two NATed UDP senders sharing one
//     translated address arrived at the receiver on two different source ports.
//   - D50 (level-triggered peer teardown/re-bind): killing one edge's daemon leaves its
//     concentrator-side session live (by handshake age) until WireGuard's own RejectAfterTime
//     (180s) elapses, at which point the peer-teardown monitor (T126) reclaims its heavy
//     state — asserted via BOTH the daemon's structured teardown INFO log line (log-grep,
//     T141's AwaitLogLine) and its /metrics liveness dropping to 0. The dead edge is then
//     restarted and must re-handshake and resume traffic.
//   - D58 (per-peer metric labels): every bound peer's /metrics series — including the
//     FIRST-configured peer — carries its OWN configured name, never the empty label a true
//     single-peer exposition would use.
//   - D49 (bootstrap-under-flood, best-effort): a spoofed unbound-source UDP flood at the
//     concentrator's listen port must not block a THIRD, freshly-bootstrapping edge's initial
//     handshake, nor disturb the two already-live NATed peers.
//   - D42 (deferred-path add/remove, where reachable): with 3 configured peers bound, a
//     runtime SIGHUP config reload that adds, then removes, a path on ONE peer's edge must not
//     panic the concentrator (nor disturb the other two peers' live sessions).
//   - D44 (deadline FEC parity for a non-primary peer, where reachable): with the FEC plane
//     configured, a NON-PRIMARY peer's light traffic — too little to ever fill a full K-sized
//     group — still gets its partial group's parity flushed by the deadline timer, exactly
//     like the primary's.
//
// EXECUTION IS DEFERRED (G2 pattern), like every other netns e2e file: this must COMPILE and
// vet clean under -tags e2e and SKIPS (requireNetAdmin) without CAP_NET_ADMIN or
// /dev/net/tun. It additionally needs the `iptables` binary + a kernel with nf_nat/conntrack
// for the D47 NAT gateway (very likely present on Ubuntu hosts — a base package, unlike
// iperf3); requireIPTables skips (not fails) when it is unavailable, mirroring
// requireNetAdmin's fail-soft contract. Privileged execution: o3.7mind.io (aarch64) +
// llm-ubuntu-0.pgtr.7mind.io (amd64), per the RUNBOOK in restart_onesided_test.go — run
// `-run TestMultiPeerHardenedDatapath -v`.
const (
	// hwPath1Name/hwPath2Name are the topology's two P2P paths (Topology/SetupWithPaths). hw1
	// carries every peer FROM THE START (directly for edge C, NATed for A1/A2 — D47); hw2
	// exists (its veth pair is wired at Setup) but is used by NO daemon config until the D42
	// subtest adds it via SIGHUP reload, then removes it again.
	hwPath1Name = "hw1"
	hwPath2Name = "hw2"

	// hwMetricsPort is the CONCENTRATOR's /metrics port (see the metricsPortRegistry table in
	// netns.go). This fixture's concentrator runs in the PEER netns (Topology.pid, like
	// p2_aggregation_test.go's / p3_fec_test.go's concentrator), so — like every wanbond
	// /metrics endpoint — it binds LOOPBACK (hwMetricsHost, 127.0.0.1): the T17 requireLoopback
	// invariant (internal/metrics/server.go, docs/design.md:740) UNCONDITIONALLY refuses any
	// non-loopback bind, so binding the uplink IP would fail the daemon at NewServer before the
	// TUN is created. 127.0.0.1 is reachable from WITHIN the concentrator's own netns, so the
	// test scrapes it by dialing INTO that netns (fetchMetricsInNetns / netnsMetricsClient), the
	// same mechanism p2/p3/p4 use for their peer-netns concentrators.
	hwMetricsHost = "127.0.0.1"
	hwMetricsPort = 9107

	// Inner overlay addresses for the three peers; concInner ("10.10.0.2") stays the
	// concentrator's own address as in every other e2e file.
	hwInnerAlpha = "10.10.0.11" // edge A1 (hw-alpha) — PRIMARY (first-configured peer, D58)
	hwInnerBeta  = "10.10.0.12" // edge A2 (hw-beta) — shares A1's apparent public IP (D47)
	hwInnerGamma = "10.10.0.13" // edge C (hw-gamma) — direct/unNATed; D49 bootstrap + D50 + D42

	hwPeerAlphaName = "hw-alpha"
	hwPeerBetaName  = "hw-beta"
	hwPeerGammaName = "hw-gamma"

	// hwNATSubnet is the aggregate private range routed+SNATed through the base netns for the
	// two shared-apparent-IP edges (A1: 10.212.1.0/30, A2: 10.212.2.0/30) — see
	// setupNATGateway.
	hwNATSubnet = "10.212.0.0/16"
	hwA1Priv    = "10.212.1.1" // A1's own private source address (behind the NAT)
	hwA1PortP   = "wbHwA1p"    // base-netns end of A1's point-to-point veth
	hwA1PortE   = "wbHwA1e"    // A1-netns end
	hwA2Priv    = "10.212.2.1"
	hwA2PortP   = "wbHwA2p"
	hwA2PortE   = "wbHwA2e"

	// hwFECDataShards/hwFECParityShards/hwFECDeadlineTOML configure the GLOBAL FEC plane
	// (D44): K=8 is well above what a handful of pings ever fills, so a non-primary peer's
	// partial group only closes via the deadline path, never the size-triggered one — exactly
	// what D44 guards.
	hwFECDataShards   = 8
	hwFECParityShards = 2
	hwFECDeadlineTOML = "80ms" // < maxFECDeadline (125ms); mirrored by hwFECDeadline below

	// hwFloodPackets mirrors multipeer_test.go's spoofed-flood packet count.
	hwFloodPackets = 3000

	hwIperfPortAlpha = 5311
	hwIperfPortBeta  = 5312

	// hwFloodSrcIP is a spare, unenrolled address added to hw1's edge-side veth for the D49
	// spoofed-source flood (a real address on the fabric that authenticates no session),
	// mirroring multipeer_test.go's mpFloodSrcIP.
	hwFloodSrcIP = "10.106.1.9"
)

// hwFECDeadline is the Go-side time.Duration mirror of hwFECDeadlineTOML, used to size the
// wait after driving light traffic before scraping for the deadline-flushed parity (D44).
const hwFECDeadline = 80 * time.Millisecond

// hwFECBlock is the [fec] TOML block shared verbatim by the concentrator and every edge config
// (matching test/e2e/p3_fec_test.go's convention — both ends of a connection need the SAME
// K/M/deadline to interoperate), built from the const trio above so they cannot drift apart.
var hwFECBlock = fmt.Sprintf("[fec]\nenabled = true\ndata_shards = %d\nparity_shards = %d\ndeadline = %q\n\n",
	hwFECDataShards, hwFECParityShards, hwFECDeadlineTOML)

// hwTeardownBudget bounds D50's wait for the concentrator's LEVEL-TRIGGERED peer-teardown
// monitor (T126) to reclaim a dead peer's heavy state: it is gated on WireGuard's own
// RejectAfterTime (the keypair's 180s validity window), not on our own liveness probes, so the
// wait is genuinely that long — see peerTeardownMonitor's doc (internal/device/session.go). The
// +15s margin covers the sessionPollInterval (200ms) poll cadence plus harness slack.
const hwTeardownBudget = awgdevice.RejectAfterTime + 15*time.Second

// hwPaths declares the topology's two P2P paths. Both veth pairs are wired at Setup time (so
// hw2 exists as a real link from t=0), but only hw1 is used by any daemon config until D42.
var hwPaths = []pathSpec{
	{name: hwPath1Name, edgeIP: "10.106.1.1", concIP: "10.106.1.2", edgeVeth: "wbHw1e", concVeth: "wbHw1c", delayMs: 5},
	{name: hwPath2Name, edgeIP: "10.106.2.1", concIP: "10.106.2.2", edgeVeth: "wbHw2e", concVeth: "wbHw2c", delayMs: 5},
}

// TestMultiPeerHardenedDatapath is the T128 acceptance. It stands up the concentrator (3
// configured peers, FEC on) plus edges A1+A2 (NATed behind one apparent IP, D47) once, then
// runs the hardening phases against the shared fixture — edge C's daemon is started partway
// through (inside the D49 subtest) so its FIRST bootstrap can be exercised under flood.
func TestMultiPeerHardenedDatapath(t *testing.T) {
	requireNetAdmin(t)
	requireIPTables(t)
	hw := setupHardenedMultiPeer(t)

	// Both NATed edges up: their bonds come up and the concentrator's per-peer path_up series
	// read 1 for both, proving the shared-apparent-IP demux (D47) already works at bring-up.
	if !hw.a1.pingUntil(concInner, mpBringUpDeadline) {
		t.Fatalf("NATed edge A1 (hw-alpha) never came up\n--- A1 ---\n%s\n--- conc ---\n%s", hw.a1.proc.log(), hw.conc.log())
	}
	if !hw.a2.pingUntil(concInner, mpBringUpDeadline) {
		t.Fatalf("NATed edge A2 (hw-beta) never came up\n--- A2 ---\n%s\n--- conc ---\n%s", hw.a2.proc.log(), hw.conc.log())
	}
	for _, pl := range []string{hwPeerAlphaName, hwPeerBetaName} {
		hw.waitPeerPathUp(t, pl, hwPath1Name, 1, mpBringUpDeadline)
	}

	t.Run("d47-shared-nat-demux-concurrent-traffic", func(t *testing.T) {
		testD47SharedNATDemux(t, hw)
	})

	t.Run("d58-per-peer-labels-include-first-configured", func(t *testing.T) {
		testD58PerPeerLabels(t, hw)
	})

	t.Run("d49-flood-does-not-block-fresh-bootstrap", func(t *testing.T) {
		testD49FloodDuringBootstrap(t, hw)
	})

	t.Run("d44-nonprimary-peer-deadline-fec-parity", func(t *testing.T) {
		testD44DeadlineFECParity(t, hw)
	})

	t.Run("d42-deferred-path-add-remove-multi-peer-no-panic", func(t *testing.T) {
		testD42DeferredPathAddRemove(t, hw)
	})

	// D50 runs LAST: it waits out WireGuard's own RejectAfterTime (~180s) to observe the
	// level-triggered teardown, so it must not block the cheaper subtests above.
	t.Run("d50-peer-teardown-and-rebind", func(t *testing.T) {
		testD50PeerTeardownAndRebind(t, hw)
	})
}

// testD47SharedNATDemux drives concurrent iperf3 transfers from A1 and A2 — two edges that
// share ONE apparent public IP via the base netns's SNAT gateway — and asserts BOTH complete
// with positive throughput AND attribute independently in /metrics. Two positive,
// independently attributed transfers through a SHARED apparent source is the D47 proof: a
// conflated (address-only) demux would corrupt or block one of the two streams — an inbound
// datagram misrouted to the wrong peer's WireGuard session fails to decrypt, so the affected
// edge's TCP transfer would stall to non-positive throughput. Both transfers completing is thus
// the core cross-talk-free proof; the per-peer metric below corroborates independent attribution.
//
// Per-peer attribution is asserted on TX bytes, NOT rx: on a multi-peer concentrator the OUTER
// inbound reader is ONE Bind-owned readLoop per SHARED path socket, and it accounts every
// received datagram to the PRIMARY peer's (peer,path) rx counter before source-demuxing the
// frame to its owning peer's session (internal/bind/multipath.go readLoop + demuxInbound,
// T23/T93). A non-primary peer's rx_bytes is therefore STRUCTURALLY 0 regardless of NAT — rx is
// a per-path, not per-peer, inbound quantity. TX bytes ARE per-peer (each peer's scheduler
// writes its OWN return traffic on the shared socket), so positive tx for BOTH shared-apparent-IP
// peers is the correct proof that the concentrator attributes each peer's stream independently.
func testD47SharedNATDemux(t *testing.T, hw *hwFixture) {
	xa := hw.a1.startTransfer(t, concInner, hwIperfPortAlpha, 6)
	xb := hw.a2.startTransfer(t, concInner, hwIperfPortBeta, 6)
	if mbps := xa.result(t); mbps <= 0 {
		t.Fatalf("NATed edge A1 (hw-alpha) transfer measured non-positive throughput %.2f Mbit/s", mbps)
	}
	if mbps := xb.result(t); mbps <= 0 {
		t.Fatalf("NATed edge A2 (hw-beta) transfer measured non-positive throughput %.2f Mbit/s", mbps)
	}

	exp := hw.scrapeMetrics(t)
	for _, pl := range []string{hwPeerAlphaName, hwPeerBetaName} {
		tx, ok := exp.PeerPathValue(metrics.MetricTxBytes, pl, hwPath1Name)
		if !ok {
			t.Fatalf("no %s{peer=%q,path=%q} series — the shared-apparent-IP peers were not attributed independently", metrics.MetricTxBytes, pl, hwPath1Name)
		}
		if tx <= 0 {
			t.Fatalf("peer %q carried non-positive tx bytes behind the shared NAT — the concentrator did not attribute its return traffic independently", pl)
		}
	}
	t.Logf("D47: edges A1+A2 shared one apparent public IP (%s) and both carried traffic independently", hwPaths[0].concIP)
}

// testD58PerPeerLabels scrapes /metrics and asserts EVERY currently-bound peer — including
// hw-alpha, the FIRST-configured peer — carries its OWN configured name as the `peer` label
// (D58), never the empty label a true single-peer exposition would use.
func testD58PerPeerLabels(t *testing.T, hw *hwFixture) {
	exp := hw.scrapeMetrics(t)
	for _, pl := range []string{hwPeerAlphaName, hwPeerBetaName} {
		if _, ok := exp.PeerPathValue(metrics.MetricUp, pl, hwPath1Name); !ok {
			t.Fatalf("no %s{peer=%q,path=%q} series — peer %q is missing its OWN label", metrics.MetricUp, pl, hwPath1Name, pl)
		}
	}
	if _, ok := exp.PeerPathValue(metrics.MetricUp, "", hwPath1Name); ok {
		t.Fatal("found a peer=\"\" series on a 3-peer concentrator — the first-configured peer leaked as unlabeled (D58 regression)")
	}
}

// testD49FloodDuringBootstrap starts edge C's daemon for the FIRST time (its TUN and initial
// handshake attempts begin), fires a spoofed-source UDP flood at the concentrator's listen
// port WHILE that first handshake is converging, then asserts the bootstrap still completes
// within budget and the two already-live NATed peers stay undisturbed.
func testD49FloodDuringBootstrap(t *testing.T, hw *hwFixture) {
	hw.startEdgeC(t)
	floodUnboundSource(t, hwFloodSrcIP, hwPaths[0].concIP+":"+strconv.Itoa(listenPort), hwFloodPackets)
	t.Logf("sent %d spoofed unbound-source datagrams while edge C (hw-gamma) was bootstrapping for the first time", hwFloodPackets)

	if !hw.edgeCPingUntil(mpBringUpDeadline) {
		t.Fatalf("edge C (hw-gamma) bootstrap was blocked by the spoofed-source flood\n--- edge C ---\n%s\n--- conc ---\n%s", hw.edgeC.log(), hw.conc.log())
	}

	// The two already-live NATed peers were not disturbed by the flood.
	if !hw.a1.pingUntil(concInner, mpBringUpDeadline) {
		t.Fatalf("edge A1 (hw-alpha) disturbed by the spoofed-source flood\n--- conc ---\n%s", hw.conc.log())
	}
	if !hw.a2.pingUntil(concInner, mpBringUpDeadline) {
		t.Fatalf("edge A2 (hw-beta) disturbed by the spoofed-source flood\n--- conc ---\n%s", hw.conc.log())
	}
}

// testD44DeadlineFECParity drives a HANDFUL of pings (far below hwFECDataShards) from the
// NON-PRIMARY peer hw-beta, waits past several hwFECDeadline ticks, and asserts the
// concentrator's per-peer FEC repair-packet counter for hw-beta advanced — proving the
// deadline flush applies to a non-primary peer's partial group, not only the primary's (D44).
func testD44DeadlineFECParity(t *testing.T, hw *hwFixture) {
	before := hw.scrapeMetrics(t)
	repairBefore, _ := before.PeerValue(metrics.MetricFECRepair, hwPeerBetaName)

	for i := 0; i < 3; i++ {
		_ = hw.a2.tryRun("ping", "-c", "1", "-W", "1", concInner) // best-effort: even a lost ping attempted a DATA frame
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(6 * hwFECDeadline) // several deadline ticks so a partial group is FLUSHED, not merely opened

	after := hw.scrapeMetrics(t)
	repairAfter, ok := after.PeerValue(metrics.MetricFECRepair, hwPeerBetaName)
	if !ok {
		t.Fatalf("no %s{peer=%q} series — the concentrator is not exposing per-peer FEC for the non-primary peer", metrics.MetricFECRepair, hwPeerBetaName)
	}
	if repairAfter <= repairBefore {
		t.Fatalf("peer %q (non-primary) %s did not advance (%v -> %v) after light traffic + %s — the deadline flush did not reach a non-primary peer's partial group (D44)",
			hwPeerBetaName, metrics.MetricFECRepair, repairBefore, repairAfter, 6*hwFECDeadline)
	}
	t.Logf("D44: non-primary peer %q's deadline-flushed FEC repair counter advanced %v -> %v from a handful of pings (well under K=%d)",
		hwPeerBetaName, repairBefore, repairAfter, hwFECDataShards)
}

// testD42DeferredPathAddRemove reloads (SIGHUP) the concentrator and edge C's configs to ADD
// the hw2 path, then reloads again to REMOVE it — with 3 peers bound throughout (2 of them,
// hw-alpha/hw-beta, entirely uninvolved in the reload) — and asserts neither daemon panics (its
// captured log never carries "panic:") and every peer is still reachable afterward.
func testD42DeferredPathAddRemove(t *testing.T, hw *hwFixture) {
	assertNoPanic := func(when string) {
		t.Helper()
		if strings.Contains(hw.conc.log(), "panic:") {
			t.Fatalf("concentrator log contains a panic %s the path add/remove reload:\n%s", when, hw.conc.log())
		}
		if strings.Contains(hw.edgeC.log(), "panic:") {
			t.Fatalf("edge C log contains a panic %s the path add/remove reload:\n%s", when, hw.edgeC.log())
		}
	}

	// --- ADD hw2 (edge C only) via SIGHUP on both ends. ---
	writeReload(t, hw.concCfgPath, hw.concConfig([]pathSpec{hwPaths[0], hwPaths[1]}))
	writeReload(t, hw.edgeCCfgPath, hw.edgeCConfig([]pathSpec{hwPaths[0], hwPaths[1]}))
	sighup(t, hw.conc)
	sighup(t, hw.edgeC)
	assertNoPanic("immediately after adding hw2")

	if !hw.edgeCPingUntil(10 * time.Second) {
		t.Fatalf("edge C lost connectivity after adding hw2 at runtime\n--- edge C ---\n%s\n--- conc ---\n%s", hw.edgeC.log(), hw.conc.log())
	}
	hw.waitPeerPathUp(t, hwPeerGammaName, hwPath2Name, 1, mpBringUpDeadline)

	// --- REMOVE hw2 again; edge C falls back to hw1 alone. ---
	writeReload(t, hw.concCfgPath, hw.concConfig([]pathSpec{hwPaths[0]}))
	writeReload(t, hw.edgeCCfgPath, hw.edgeCConfig([]pathSpec{hwPaths[0]}))
	sighup(t, hw.conc)
	sighup(t, hw.edgeC)
	assertNoPanic("immediately after removing hw2")

	if !hw.edgeCPingUntil(10 * time.Second) {
		t.Fatalf("edge C lost connectivity after removing hw2 at runtime\n--- edge C ---\n%s\n--- conc ---\n%s", hw.edgeC.log(), hw.conc.log())
	}

	// The two entirely-uninvolved peers were never disturbed by a path reload scoped to a
	// third peer — the ">1 peer, no panic" acceptance.
	if !hw.a1.pingUntil(concInner, mpBringUpDeadline) {
		t.Fatalf("edge A1 (hw-alpha) disturbed by edge C's path add/remove reload\n--- conc ---\n%s", hw.conc.log())
	}
	if !hw.a2.pingUntil(concInner, mpBringUpDeadline) {
		t.Fatalf("edge A2 (hw-beta) disturbed by edge C's path add/remove reload\n--- conc ---\n%s", hw.conc.log())
	}
	assertNoPanic("after the full add/remove cycle plus other-peer verification")
	t.Logf("D42: hw2 added then removed at runtime on edge C's config with 3 peers bound (hw-alpha/hw-beta untouched); no panic")
}

// testD50PeerTeardownAndRebind kills edge C's (hw-gamma, non-primary) daemon and awaits the
// concentrator's LEVEL-TRIGGERED peer-teardown monitor (D50/T126) reclaiming its heavy state —
// asserted via the structured teardown INFO log line AND /metrics liveness reading 0 — then
// restarts edge C and asserts it re-handshakes and resumes traffic.
func testD50PeerTeardownAndRebind(t *testing.T, hw *hwFixture) {
	// Snapshot the concentrator's ALREADY-EMITTED log records BEFORE killing edge C. The
	// concentrator logs the SAME teardown message ("...heavy state torn down") for its
	// non-primary peers (hw-beta AND hw-gamma) once at STARTUP, while their heavy state settles
	// before any edge has connected. Matching the teardown line from the start of the buffer
	// would therefore fire instantly on that benign startup record (with the wrong/settling
	// peer); we must wait for a NEW hw-gamma teardown emitted AFTER this kill.
	baseline := len(ParseLogLines(hw.conc.log()))
	hw.stopEdgeC()
	t.Logf("killed edge C (hw-gamma); awaiting the level-triggered teardown (gated on WireGuard's RejectAfterTime, up to %s)", hwTeardownBudget)

	// The fast liveness signal: hw-gamma's own path_up reads 0 well before the heavy-state
	// teardown fires (path liveness is OUR probe protocol, not gated on RejectAfterTime).
	hw.waitPeerPathUp(t, hwPeerGammaName, hwPath1Name, 0, PLivenessDetectBudget+5*time.Second)

	// The slow, level-triggered heavy-state reclaim: await the teardown INFO line for hw-gamma
	// specifically, appearing AFTER the pre-kill baseline, gated on WireGuard's RejectAfterTime.
	line, ok := awaitPeerTeardown(t, hw.conc, hwPeerGammaName, baseline, hwTeardownBudget)
	if !ok {
		t.Fatalf("concentrator never logged the D50 teardown INFO for %q within %s\n--- conc ---\n%s", hwPeerGammaName, hwTeardownBudget, hw.conc.log())
	}

	// /metrics still reflects the dead peer post-teardown.
	exp := hw.scrapeMetrics(t)
	if v, ok := exp.PeerPathValue(metrics.MetricUp, hwPeerGammaName, hwPath1Name); !ok || v != 0 {
		t.Fatalf("%s{peer=%q,path=%q} = %v (ok=%v) after teardown, want 0", metrics.MetricUp, hwPeerGammaName, hwPath1Name, v, ok)
	}

	// Restart: a fresh authenticated PROBE re-binds the source and re-instantiates the ring.
	hw.startEdgeC(t)
	if !hw.edgeCPingUntil(mpBringUpDeadline) {
		t.Fatalf("edge C (hw-gamma) did not recover after restart\n--- edge C ---\n%s\n--- conc ---\n%s", hw.edgeC.log(), hw.conc.log())
	}
	hw.waitPeerPathUp(t, hwPeerGammaName, hwPath1Name, 1, mpBringUpDeadline)
	t.Logf("D50: hw-gamma torn down (%s) and re-bound after restart", line.Msg)
}

// awaitPeerTeardown polls p's captured log for a "concentrator peer session lost; heavy state
// torn down" record whose peer field equals peer, considering ONLY records at index >= baseline
// (records parsed after the caller's pre-kill snapshot). This skips the benign STARTUP teardown
// records the concentrator emits for its non-primary peers before any edge connects, which a
// bare AwaitLogLine (message-only, from the buffer start) would match instantly with the wrong
// peer. It is the peer-scoped, baseline-anchored analogue of AwaitLogLine.
func awaitPeerTeardown(t *testing.T, p *proc, peer string, baseline int, timeout time.Duration) (LogLine, bool) {
	t.Helper()
	const msg = "concentrator peer session lost; heavy state torn down"
	deadline := time.Now().Add(timeout)
	for {
		lines := ParseLogLines(p.log())
		for i := baseline; i < len(lines); i++ {
			if lines[i].Msg != msg {
				continue
			}
			if pl, _ := lines[i].FieldString("peer"); pl == peer {
				return lines[i], true
			}
		}
		if time.Now().After(deadline) {
			return LogLine{}, false
		}
		time.Sleep(logLinePollInterval)
	}
}

// hwFixture is the assembled T128 fixture handed to every subtest.
type hwFixture struct {
	t   *testing.T
	top *Topology
	bin string

	conc        *proc
	concCfgPath string
	metricsURL  string

	a1, a2 *hwEdge // NATed (D47)

	// Edge C material: started lazily (inside the D49 subtest) so its FIRST bootstrap can be
	// observed under flood; re-rendered on every D42 reload and restarted by D50.
	edgeC        *proc
	edgeCCfgPath string
	edgeCArgv    []string

	// Key/psk material retained to re-render the concentrator and edge C configs (D42 reload).
	topPSK, concPriv, concPub               string
	pskAlpha, pskBeta, pskGamma             string
	edgeAPub, edgeBPub, edgeCPriv, edgeCPub string
}

// concConfig renders the concentrator TOML for the given path set (all 3 peers, unchanged).
//
// bind = "source" pins EVERY concentrator path socket to its specific source IP rather than
// letting the default "auto" mode SO_BINDTODEVICE-bind a single-address-interface path to the
// WILDCARD 0.0.0.0:<listen_port> (the T16 roam-survival mode, listenOnDevice). That wildcard
// bind is why D42's RUNTIME path-add (SIGHUP add hw2) fails under auto: AddPath source-IP-pins
// a runtime-added path (D30 — auto never device-binds at runtime), so hw2's specific
// 10.106.2.2:<port> bind COLLIDES EADDRINUSE with hw1's already-held wildcard 0.0.0.0:<port>.
// Source-pinning both makes the two DISTINCT specific-IP sockets coexist on one port, so the
// runtime add is "reachable" (D42's own hedge) — see the D30 auto-runtime-add gap reported
// alongside this re-validation. Source-bind is neutral for every other subtest (the
// concentrator's addresses never move here) and does not weaken the shared-NAT demux (D47),
// which is app-layer AddrPort keying independent of the socket's bind.
func (hw *hwFixture) concConfig(paths []pathSpec) string {
	var pb strings.Builder
	for _, p := range paths {
		fmt.Fprintf(&pb, "[[paths]]\nname = %q\nsource_addr = %q\n\n", p.name, p.concIP)
	}
	return fmt.Sprintf(`role = "concentrator"
psk = %q
bind = "source"

%s[metrics]
listen = %q

%s[wireguard]
private_key = %q
listen_port = %d

[[wireguard.peers]]
public_key = %q
name = %q
psk = %q
allowed_ips = ["%s/32"]

[[wireguard.peers]]
public_key = %q
name = %q
psk = %q
allowed_ips = ["%s/32"]

[[wireguard.peers]]
public_key = %q
name = %q
psk = %q
allowed_ips = ["%s/32"]

[log]
level = "info"
`, hw.topPSK, pb.String(), hw.metricsAddr(), hwFECBlock, hw.concPriv, listenPort,
		hw.edgeAPub, hwPeerAlphaName, hw.pskAlpha, hwInnerAlpha,
		hw.edgeBPub, hwPeerBetaName, hw.pskBeta, hwInnerBeta,
		hw.edgeCPub, hwPeerGammaName, hw.pskGamma, hwInnerGamma)
}

// metricsAddr is the concentrator's /metrics bind address: LOOPBACK (hwMetricsHost), as the
// T17 requireLoopback invariant demands. The concentrator runs in the peer netns, so this
// 127.0.0.1 endpoint is unreachable from the base netns directly — the test scrapes it by
// dialing INTO the peer netns (hw.scrapeMetrics / hw.waitPeerPathUp).
func (hw *hwFixture) metricsAddr() string {
	return hwMetricsHost + ":" + strconv.Itoa(hwMetricsPort)
}

// scrapeMetrics is a one-shot scrape of the concentrator's loopback /metrics from INSIDE its
// peer netns (fatal on error) — the in-netns analogue of the package-level scrapeMetrics,
// which assumes a base-netns-reachable endpoint. Mirrors p2/p3/p4's fetchMetricsInNetns.
func (hw *hwFixture) scrapeMetrics(t *testing.T) metrics.Exposition {
	t.Helper()
	return fetchMetricsInNetns(t, hw.top.pid, hw.metricsURL)
}

// waitPeerPathUp polls the concentrator's in-netns loopback /metrics until the PER-PEER
// wanbond_path_up series for (peer,path) reads want, or fails at deadline. A mid-poll scrape
// error is tolerated (the endpoint may not be up yet) — the in-netns analogue of the
// package-level waitPeerPathUp, which dials a base-netns-reachable endpoint on the default
// client. Reuses ONE netnsMetricsClient across the poll (DisableKeepAlives dials fresh each
// scrape, so the socket is re-opened inside the peer netns every time).
func (hw *hwFixture) waitPeerPathUp(t *testing.T, peer, path string, want float64, deadline time.Duration) {
	t.Helper()
	client := netnsMetricsClient(hw.top.pid)
	end := time.Now().Add(deadline)
	var lastErr error
	for time.Now().Before(end) {
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		exp, err := metrics.Fetch(ctx, client, hw.metricsURL)
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

// edgeCConfig renders edge C's TOML (direct/unNATed; source_addr is the given paths' own
// edgeIP) for the given path set — one [[paths]] entry per path.
func (hw *hwFixture) edgeCConfig(paths []pathSpec) string {
	var pb strings.Builder
	for _, p := range paths {
		fmt.Fprintf(&pb, "[[paths]]\nname = %q\nsource_addr = %q\ndest_addr = \"%s:%d\"\n\n", p.name, p.edgeIP, p.concIP, listenPort)
	}
	return fmt.Sprintf(`role = "edge"
psk = %q

%s%s[wireguard]
private_key = %q

[[wireguard.peers]]
public_key = %q
endpoint = "%s:%d"
allowed_ips = ["%s/32"]

[log]
level = "info"
`, hw.pskGamma, pb.String(), hwFECBlock, hw.edgeCPriv, hw.concPub, paths[0].concIP, listenPort, concInner)
}

// natEdgeConfig renders a NATed edge's (A1/A2) TOML: ONE path on hw1, source_addr the edge's
// PRIVATE (pre-NAT) address, dest_addr the concentrator's REAL hw1 uplink address (unaffected
// by NAT — only the SOURCE is translated in flight).
func natEdgeConfig(psk, edgePriv, concPub, privAddr string) string {
	return fmt.Sprintf(`role = "edge"
psk = %q

[[paths]]
name = %q
source_addr = %q
dest_addr = "%s:%d"

%s[wireguard]
private_key = %q

[[wireguard.peers]]
public_key = %q
endpoint = "%s:%d"
allowed_ips = ["%s/32"]

[log]
level = "info"
`, psk, hwPath1Name, privAddr, hwPaths[0].concIP, listenPort, hwFECBlock, edgePriv, concPub, hwPaths[0].concIP, listenPort, concInner)
}

// setupHardenedMultiPeer builds the T128 fixture: the two-path Topology (hw1 live, hw2 wired
// but unused), the concentrator (3 peers, FEC on) in the peer netns, the NAT gateway + two
// NATed edges (A1/A2, D47) up and started. Edge C's daemon is NOT started here (D49 starts it
// under flood); its config is rendered so the D42 reload helpers have it from the start.
func setupHardenedMultiPeer(t *testing.T) *hwFixture {
	t.Helper()
	bin := buildWanbond(t)
	top := SetupWithPaths(t, hwPaths)

	concPriv, concPub := genKey(t)
	edgeAPriv, edgeAPub := genKey(t)
	edgeBPriv, edgeBPub := genKey(t)
	edgeCPriv, edgeCPub := genKey(t)
	pskAlpha := randKey(t)
	pskBeta := randKey(t)
	pskGamma := randKey(t)
	topPSK := randKey(t)

	hw := &hwFixture{
		t: t, top: top, bin: bin,
		topPSK: topPSK, concPriv: concPriv, concPub: concPub,
		pskAlpha: pskAlpha, pskBeta: pskBeta, pskGamma: pskGamma,
		edgeAPub: edgeAPub, edgeBPub: edgeBPub, edgeCPriv: edgeCPriv, edgeCPub: edgeCPub,
	}
	hw.metricsURL = "http://" + hw.metricsAddr() + "/metrics"

	dir := t.TempDir()
	hw.concCfgPath = writeConfig(t, filepath.Join(dir, "conc.toml"), hw.concConfig([]pathSpec{hwPaths[0]}))
	startConcentrator(t, hw)
	top.nsenter("ip", "addr", "add", concInner+"/24", "dev", tunDev)
	top.nsenter("ip", "link", "set", tunDev, "up")

	// A spare unenrolled address on hw1's edge veth for the D49 spoofed-source flood.
	top.run("ip", "addr", "add", hwFloodSrcIP+"/24", "dev", hwPaths[0].edgeVeth)

	setupNATGateway(t, top)

	a1CfgPath := writeConfig(t, filepath.Join(dir, "a1.toml"), natEdgeConfig(pskAlpha, edgeAPriv, concPub, hwA1Priv))
	a2CfgPath := writeConfig(t, filepath.Join(dir, "a2.toml"), natEdgeConfig(pskBeta, edgeBPriv, concPub, hwA2Priv))
	hw.a1 = newHwNATEdge(t, top, "edgeA1", hwA1Priv+"/30", hwA1PortP, hwA1PortE, hwInnerAlpha, bin, a1CfgPath)
	hw.a2 = newHwNATEdge(t, top, "edgeA2", hwA2Priv+"/30", hwA2PortP, hwA2PortE, hwInnerBeta, bin, a2CfgPath)

	// Edge C's config is rendered now (needed by the D42 reload helpers) but its daemon is
	// started later by the caller (D49).
	hw.edgeCCfgPath = filepath.Join(dir, "edgeC.toml")
	writeConfig(t, hw.edgeCCfgPath, hw.edgeCConfig([]pathSpec{hwPaths[0]}))
	hw.edgeCArgv = []string{bin, "--config", hw.edgeCCfgPath}

	return hw
}

// concStartAttempts bounds startConcentrator's retries of the concentrator daemon.
const concStartAttempts = 4

// startConcentrator launches the concentrator daemon in the peer netns and waits for its TUN,
// RETRYING if the daemon exits before the TUN is usable. The concentrator binds its /metrics on
// loopback (127.0.0.1) INSIDE the peer netns; on a fast host that bind can momentarily lose a
// race with the peer netns's lo coming UP (SetupWithPaths brings it up, but the netlink state
// can lag a beat), so the daemon exits at Open with "listen 127.0.0.1:<port>: bind: cannot
// assign requested address" and its TUN disappears. A re-assert of lo up plus a fresh start a
// beat later succeeds once lo has settled — observed necessary on the amd64 host, never hit on
// the slower aarch64 one. This is a fixture-timing guard, not a product retry: the shipped
// daemon fails fast (correctly) when its metrics address is genuinely unbindable.
func startConcentrator(t *testing.T, hw *hwFixture) {
	t.Helper()
	for attempt := 1; ; attempt++ {
		hw.top.nsenter("ip", "link", "set", "lo", "up") // idempotent re-assert; closes the lo-up race
		hw.conc = hw.top.startProc(t, "concentrator", "nsenter", "-t", strconv.Itoa(hw.top.pid), "-n", hw.bin, "--config", hw.concCfgPath)
		if hw.top.waitLink(tunDev, true, 5*time.Second) {
			return
		}
		log := hw.conc.log()
		if hw.conc.cmd.Process != nil {
			_ = hw.conc.cmd.Process.Kill() // reap a hung (alive-but-no-TUN) daemon before retrying
			_, _ = hw.conc.cmd.Process.Wait()
		}
		if attempt >= concStartAttempts {
			t.Fatalf("concentrator %s never appeared after %d attempts\n%s", tunDev, attempt, log)
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// setupNATGateway turns the base (test-process) netns into the D47 SNAT gateway: enables IPv4
// forwarding and installs a POSTROUTING SNAT rule that translates BOTH NATed edges' private
// subnets to hw1's edge address as their packets transit toward the concentrator — the same
// mechanism (and validated behaviour: conntrack assigns each flow a DISTINCT translated port)
// a home/carrier NAT box uses for multiple devices behind one public IP.
func setupNATGateway(t *testing.T, top *Topology) {
	t.Helper()
	if err := os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1\n"), 0o644); err != nil {
		t.Fatalf("enable ip_forward in the base netns: %v", err)
	}
	natArgs := []string{"-t", "nat", "-A", "POSTROUTING", "-s", hwNATSubnet, "-o", hwPaths[0].edgeVeth,
		"-j", "SNAT", "--to-source", hwPaths[0].edgeIP}
	top.run("iptables", natArgs...)
	t.Cleanup(func() {
		delArgs := []string{"-t", "nat", "-D", "POSTROUTING", "-s", hwNATSubnet, "-o", hwPaths[0].edgeVeth,
			"-j", "SNAT", "--to-source", hwPaths[0].edgeIP}
		_ = top.tryRun("iptables", delArgs...)
	})
}

// requireIPTables skips (does NOT fail) when the `iptables` binary is unavailable — the D47
// NAT gateway's only dependency beyond requireNetAdmin's CAP_NET_ADMIN/tun probes. It performs
// no destructive check itself; a NAT-incapable-but-iptables-present kernel would fail with a
// clear iptables error at setupNATGateway's real rule-add, the actual capability probe.
func requireIPTables(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("iptables"); err != nil {
		t.Skipf("iptables not found (%v) — the D47 shared-NAT-demux gateway needs it; skipping", err)
	}
}

// startEdgeC (re)starts edge C's daemon DIRECTLY in the base (test-process) netns — it needs
// no NAT and no separate PID-held namespace, exactly like the plain edge in p0_test.go /
// reload_test.go — and addresses its freshly created TUN. The daemon is registered on the
// PARENT test's t (hw.t), NOT the subtest's, because edge C is a FIXTURE-lifetime process: it
// is first started inside the D49 subtest but must stay alive across the later D42 (SIGHUP
// reload) and D50 (teardown/restart) subtests. Registering it on the D49 subtest's t would
// SIGTERM it via startProc's t.Cleanup the moment D49 returns (leaving D42's SIGHUP and D50's
// teardown to act on a dead process). The passed t is used only for the immediate bring-up
// fatals, which belong to whichever subtest is starting edge C.
func (hw *hwFixture) startEdgeC(t *testing.T) {
	t.Helper()
	hw.edgeC = hw.top.startProc(hw.t, "edgeC", hw.edgeCArgv...)
	if !hw.top.waitLink(tunDev, false, 5*time.Second) {
		t.Fatalf("edge C %s never appeared\n%s", tunDev, hw.edgeC.log())
	}
	hw.top.run("ip", "addr", "add", hwInnerGamma+"/24", "dev", tunDev)
	hw.top.run("ip", "link", "set", tunDev, "up")
}

// stopEdgeC kills edge C's daemon and waits for it to exit (D50). The startProc cleanup still
// fires at test end and tolerates the double-kill.
func (hw *hwFixture) stopEdgeC() {
	if hw.edgeC == nil || hw.edgeC.cmd.Process == nil {
		return
	}
	_ = hw.edgeC.cmd.Process.Kill()
	_, _ = hw.edgeC.cmd.Process.Wait()
}

// edgeCPingUntil pings concInner from the base netns (where edge C's TUN lives) until it
// answers or d elapses.
func (hw *hwFixture) edgeCPingUntil(d time.Duration) bool {
	return hw.top.pingUntil(concInner, d)
}

// hwEdge is one NATed (shared-apparent-IP) edge namespace: a PID-addressed holder connected to
// the base netns by a PRIVATE point-to-point veth whose egress toward the concentrator is
// source-NATed (setupNATGateway) — the D47 fixture's A1/A2.
type hwEdge struct {
	t    *testing.T
	base *Topology
	name string
	pid  int
	proc *proc
}

// newHwNATEdge opens a fresh network namespace, wires its private point-to-point veth into the
// base netns (this side addressed by privCIDR, e.g. "10.212.1.1/30"; the base side gets the
// other address in that /30), adds a default route toward the base netns, then starts the edge
// daemon and addresses its TUN with innerAddr.
func newHwNATEdge(t *testing.T, base *Topology, name, privCIDR, baseVeth, edgeVeth, innerAddr, bin, cfgPath string) *hwEdge {
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

	e := &hwEdge{t: t, base: base, name: name, pid: pid}
	(&Topology{t: t, pid: pid}).waitForNetns()
	e.run("ip", "link", "set", "lo", "up")

	baseAddr := baseSideAddr(privCIDR)
	base.run("ip", "link", "add", baseVeth, "type", "veth", "peer", "name", edgeVeth)
	base.run("ip", "link", "set", edgeVeth, "netns", strconv.Itoa(pid))
	base.run("ip", "addr", "add", baseAddr, "dev", baseVeth)
	base.run("ip", "link", "set", baseVeth, "up")

	// The netns move is async w.r.t. in-namespace addressing (D33, mirrored from
	// multipeer_test.go's newMPEdge): retry the addressing op bounded rather than sleep-guess.
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := e.tryRun("ip", "addr", "add", privCIDR, "dev", edgeVeth)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: could not add %s to %q in netns %d within 5s: %v", name, privCIDR, edgeVeth, pid, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	e.run("ip", "link", "set", edgeVeth, "up")
	e.run("ip", "route", "add", "default", "via", addrOnly(baseAddr))

	e.proc = base.startProc(t, name, "nsenter", "-t", strconv.Itoa(pid), "-n", bin, "--config", cfgPath)
	if !e.waitTUN(5 * time.Second) {
		t.Fatalf("%s %s never appeared\n%s", name, tunDev, e.proc.log())
	}
	e.run("ip", "addr", "add", innerAddr+"/24", "dev", tunDev)
	e.run("ip", "link", "set", tunDev, "up")
	return e
}

// baseSideAddr derives the base-netns end of a /30 point-to-point link from the edge-side CIDR
// (e.g. "10.212.1.1/30" -> "10.212.1.2/30" — the OTHER usable address in the /30).
func baseSideAddr(edgeCIDR string) string {
	addr, prefix, _ := strings.Cut(edgeCIDR, "/")
	i := strings.LastIndex(addr, ".")
	lastOctet, _ := strconv.Atoi(addr[i+1:])
	return addr[:i+1] + strconv.Itoa(lastOctet+1) + "/" + prefix
}

// addrOnly strips the /prefix suffix off a CIDR string.
func addrOnly(cidr string) string {
	addr, _, _ := strings.Cut(cidr, "/")
	return addr
}

// waitTUN polls until this edge's wanbond0 exists in its namespace.
func (e *hwEdge) waitTUN(d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if e.tryRun("ip", "link", "show", tunDev) == nil {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// pingUntil pings ip from THIS edge's namespace until it answers or d elapses.
func (e *hwEdge) pingUntil(ip string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if e.tryRun("ping", "-c", "1", "-W", "1", ip) == nil {
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}

// startTransfer starts a background iperf3 transfer from this edge to serverIP (the
// concentrator's inner overlay address), returning immediately; result() awaits it. Unlike
// multipeer_test.go's mpEdge.startTransfer — whose concentrator runs in the BASE netns, so its
// inner TUN is bindable there — THIS fixture's concentrator runs in the PEER netns, so the
// iperf3 SERVER on serverIP must be started INSIDE that peer netns (where concInner's TUN
// lives); binding it in the base netns fails ("Cannot assign requested address"), the D77
// re-validation's first surfaced fixture defect. The client still runs in this edge's netns
// and reaches serverIP over the wanbond tunnel.
func (e *hwEdge) startTransfer(t *testing.T, serverIP string, port, secs int) *transfer {
	t.Helper()
	startConcIperfServer(t, e.base, serverIP, port)
	cmd := exec.Command("nsenter", "-t", strconv.Itoa(e.pid), "-n", "iperf3", "-c", serverIP, "-p", strconv.Itoa(port), "-t", strconv.Itoa(secs), "-J")
	out := &lockedBuffer{}
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		t.Fatalf("%s: start iperf3 client: %v", e.name, err)
	}
	return &transfer{cmd: cmd, out: out}
}

// startConcIperfServer starts a one-shot iperf3 server bound to serverIP:port INSIDE the
// concentrator's peer netns (top.pid) — where the concentrator's inner TUN address lives — and
// waits until it reaches LISTEN there. The base-netns startBaseIperfServer (multipeer_test.go)
// cannot be reused because concInner is not an address of the base netns in this fixture.
func startConcIperfServer(t *testing.T, top *Topology, serverIP string, port int) {
	t.Helper()
	top.startProc(t, "iperf3-server", "nsenter", "-t", strconv.Itoa(top.pid), "-n",
		"iperf3", "-s", "-1", "-B", serverIP, "-p", strconv.Itoa(port))
	waitIperfListenInNetns(t, top.pid, port)
}

// waitIperfListenInNetns polls the netns of pid for a TCP LISTEN socket on port (never
// connecting — the server is one-shot), failing at iperfListenTimeout. It is the peer-netns
// analogue of baseWaitIperfListen, reading kernel socket state via `ss -ltn` inside the netns.
func waitIperfListenInNetns(t *testing.T, pid, port int) {
	t.Helper()
	suffix := ":" + strconv.Itoa(port)
	deadline := time.Now().Add(iperfListenTimeout)
	for time.Now().Before(deadline) {
		out, err := exec.Command("nsenter", "-t", strconv.Itoa(pid), "-n", "ss", "-ltn").CombinedOutput()
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
	t.Fatalf("concentrator iperf3 server never reached LISTEN on port %d in netns %d within %s", port, pid, iperfListenTimeout)
}

// run executes a command inside this edge's namespace, failing the test on error.
func (e *hwEdge) run(args ...string) {
	e.t.Helper()
	full := append([]string{"-t", strconv.Itoa(e.pid), "-n"}, args...)
	if out, err := exec.Command("nsenter", full...).CombinedOutput(); err != nil {
		e.t.Fatalf("%s: nsenter %s: %v\n%s", e.name, strings.Join(args, " "), err, out)
	}
}

// tryRun executes a command inside this edge's namespace, returning the error (no fatal).
func (e *hwEdge) tryRun(args ...string) error {
	full := append([]string{"-t", strconv.Itoa(e.pid), "-n"}, args...)
	return exec.Command("nsenter", full...).Run()
}
