//go:build e2e

package e2e

// TestE2EResequencerHoLLatency (T243, defect D93) is the END-TO-END confirmation, over a
// real socket/TUN/netem path, of the D93 single-delivering-path immediate-release fix
// (T240 reseq policy + T241 bind pathKey wiring, both merged at 381fc66). On ONE active
// delivering path with deterministic p% DATA loss, the fixed resequencer releases a
// head-of-line gap's already-arrived successors with ~0 hold instead of stalling them the
// full resequencerTimeout (250 ms). This test pins that post-fix invariant end-to-end:
//
//   (a) LOADED RTT through the tunnel under a concurrent inner stream stays near the idle
//       baseline (the worst sampled inner-RTT rises < d93MaxHoLDeltaMs above idle) — NOT the
//       ~250 ms-per-drop spike the pre-fix fixed hold produced;
//   (b) single-stream inner TCP under the SAME loss does not collapse — its throughput stays
//       >= d93MinLossyTCPMbps (a Mathis-model-derived absolute floor; the loss itself caps
//       TCP far below the loss-free baseline, so a fraction-of-baseline bound is unsound);
//   (c) two-path reorder correctness is unaffected — pinned by TestP2Aggregation (the weighted
//       two-real-socket aggregation e2e) end-to-end and by internal/bind/multipath_d93_test.go
//       (TestSameSrcInterleavedPathIDsStillHeld) + internal/reseq at the unit level.
//
// D93 FIELD SIGNATURE (what this reproduces). On a single active path (active-backup steady
// state, only the primary delivering DATA) packets arrive already in order, so the ONLY source
// of an OuterSeq gap is a genuine path drop — never cross-path reorder. The pre-fix resequencer
// nonetheless held every in-order frame buffered AHEAD of the gap for the full 250 ms
// resequencerTimeout (internal/bind/multipath.go) before skipping the never-arriving head, then
// released the run as one burst. On a ~ms-RTT path that fixed hold is ~6x the base RTT and pays
// PER drop, inflating the effective RTT (field: ~40 ms idle -> ~305 ms loaded) and collapsing
// single-stream TCP (throughput proportional to 1/RTT: field ~8-14 Mbit tunneled vs ~31 Mbit
// raw) — with ZERO reordering benefit, because a lone path cannot reorder against itself. The
// fix (internal/reseq/reseq.go singleSourceImmediate): when exactly one delivering pathKey has
// been observed within singleSourceTrailingWindow, a gap is genuine loss and its successors
// release immediately; a second distinct key re-arms the full hold, preserving reorder safety.
//
// TOPOLOGY. TWO bonded veth paths under the DEFAULT scheduler (empty [scheduler] normalizes to
// active-backup, config.go PolicyActiveBackup): paths[0] is the PRIMARY active path carrying ALL
// DATA (sched.NewActiveBackup Pick returns the single active), paths[1] a healthy standby that
// carries no DATA in steady state — this two-path config IS the single-delivering-path state
// D93 targets (a lone delivering pathKey at the receiver). The standby keeps the bond robustly
// UP (a real second configured path, exercising the min-across-paths sizing) but never delivers
// DATA, so the concentrator's receive resequencer observes exactly ONE delivering pathKey and
// the fix's immediate release engages. NO mtu knob (auto-1500), FEC OFF (no [fec] block, so the
// FEC-active suppression of immediate release is not in play — singleSourceImmediate requires
// !fecActive). Low per-path delay (d93PathDelayMs) keeps the veth RTT ~ms, so the pre-fix
// 250 ms hold is an unmistakable multiple of the base and post-fix loss-induced TCP degradation
// stays mild enough that the lenient (b) bound is meaningful (Mathis: on a ~ms RTT the fast
// retransmit recovers within ~ms, so throughput stays a large fraction of the loss-free rate).
//
// DETERMINISTIC LOSS (the crux — deterministic so -count=3 is stable, unlike netem's random
// loss). installD93DataDrop installs, in the CONCENTRATOR (transit) netns on the input hook, an
// nft rule dropping every d93DropEveryNth-th FULL-SIZED DATA datagram arriving on the ACTIVE
// path's ingress veth in the edge->concentrator direction (the upload leg; the D93 receive-HoL
// mechanism is direction-symmetric — the resequencer runs per-peer on BOTH roles — so dropping
// the upload DATA stalls the CONCENTRATOR's resequencer, which HoL-blocks the ping REQUEST and
// the inner TCP data exactly as the field download stalled the edge's). The rule matches, IN
// ORDER:
//   - iifname concVeth[active] — scope to the active path so its numgen counter advances ONLY by
//     that path's DATA (a counter shared across paths would not be per-path deterministic; the
//     standby carries no DATA anyway);
//   - udp dport listenPort     — the tunnel's outer datagrams only;
//   - ip length > d93DataSizeThreshold — FULL-SIZED DATA only. This SPARES two frame classes:
//     (1) PROBE liveness frames (small) — so active-backup liveness stays UP and never fails DATA
//     over to the drop-free standby, which would mask the HoL effect; (2) the inner ICMP ping
//     frames (small) — so the ping is never DROPPED, only HoL-BLOCKED behind a dropped stream
//     datagram ahead of it in the shared per-peer OuterSeq space (this IS the HoL amplifier the
//     test measures: a tiny in-order frame stalled waiting for a big dropped one);
//   - numgen inc mod d93DropEveryNth == 0 — increments only for MATCHED (full DATA) datagrams,
//     dropping every Nth. Deterministic.
//
// LOSS-FRACTION / THRESHOLD ARITHMETIC (outer IP total-length units; mirrors the InnerMTU
// accounting in internal/bind and the T237 fixture's overhead notes). A full inner packet of
// InnerMTU(1500) = 1400 bytes rides in an outer datagram of 1400 + 40 (outer DATA frame) + 32
// (WG transport) + 28 (outer IPv4+UDP) = 1500 bytes IP length; a modest d93UDPDatagramLen-byte
// UDP stream datagram rides in 1300 + 8 (inner UDP) + 20 (inner IP) + 40 + 32 + 28 = 1428 bytes.
// A default inner ICMP ping (56-byte payload) rides in 56 + 8 + 20 + 40 + 32 + 28 = 184 bytes; a
// PROBE frame is a few dozen bytes. d93DataSizeThreshold = 1000 therefore sits strictly ABOVE
// every ping/probe/handshake datagram and strictly BELOW every full DATA/stream datagram, so
// `ip length > 1000` selects DATA-and-only-DATA. With numgen inc mod 15 == 0 the middlebox drops
// every 15th matched datagram: p = 1/15 = 6.67%, inside the 5-10% target — enough drop events to
// sample the HoL latency and drive genuine loss, small enough that inner TCP survives.
//
// LATENCY BOUND (a). d93MaxHoLDeltaMs = 100 ms bounds the worst sampled loaded inner-RTT ABOVE
// the idle inner-RTT. The pre-fix fixed 250 ms hold made a drop landing in the sample window add
// ~250 ms to the worst RTT (idle + ~250 ms); the fix removes the hold, leaving only the modest
// bufferbloat of a NON-saturating d93UDPRateMbit stream, so the worst loaded RTT stays a small
// delta above idle. 100 ms is < half the 250 ms pre-fix signature (a clear discriminator) yet
// generous headroom over veth scheduling/queue noise on a ~ms-RTT path. This is the PRIMARY D93
// discriminator.
//
// THROUGHPUT BOUND (b). d93MinLossyTCPMbps is an ABSOLUTE floor derived from the TCP loss model,
// not a fraction of the loss-free baseline: at p = 6.67% independent loss TCP is loss-limited
// regardless of any HoL behaviour — Mathis gives BW ~ (MSS/RTT)*(1.22/sqrt(p)) ~ (1240 B / ~5 ms)
// * 4.7 ~ 8-12 Mbit/s on this fixture, while the loss-free veth baseline is two orders of
// magnitude higher, so "retain 50% of baseline" is theoretically unattainable at this p (the
// first o3 run measured 6.8 Mbit/s lossy vs 249.7 Mbit/s loss-free — exactly the Mathis
// ceiling's neighbourhood). What DOES discriminate the fix: pre-fix, every drop stalled delivery
// ~250 ms, inflating the effective RTT ~50x and compounding into the field collapse (iperf3
// 0 bytes received / sub-Mbit dips) — Mathis at RTT ~250 ms predicts ~0.2 Mbit/s. The floor of
// 2 Mbit/s sits ~10x above that pre-fix prediction and ~3x below the fixed tree's measured
// 6.8 Mbit/s, so it is robust in both directions. The RTT bound in (a) remains the PRIMARY
// discriminator.
//
// HARDWARE TIER — DO NOT RUN IN THE DEFAULT GATE. Like every //go:build e2e test here it needs
// root, /dev/net/tun, tc, and nftables inside a network namespace; the non-privileged gate only
// COMPILES + `go vet -tags e2e`s + lints it. The privileged RUN (GREEN on the fixed tree; RED on
// the pre-fix base f92fe8e) is validated on remote hardware (T245; o3.7mind.io /
// llm-ubuntu-0.pgtr.7mind.io). VALIDATE THERE WITH `-run TestE2EResequencerHoLLatency -count=3`,
// NEVER the full suite (two root-netns suites deadlock to the 600 s timeout, and the o3 host runs
// a PERSISTENT root-netns concentrator that false-fails root-netns tests). This test builds its
// OWN per-test isolated netns (SetupWithPaths) and needs NO root-netns concentrator, so it does
// not collide with that persistent process. It binds no /metrics listener (the asserts read RTT
// and throughput directly), so it claims no port in netns.go's metricsPortRegistry.

import (
	"encoding/json"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	// d93DropTable / d93DropChain name the nftables table+chain installed in the concentrator
	// (transit) netns dropping the deterministic fraction of full-sized DATA datagrams on the
	// active path — the emulation of the field carrier's native per-path loss (D93).
	d93DropTable = "wbd93"
	d93DropChain = "holdrop"

	// d93DropEveryNth K: the middlebox drops every K-th full-sized DATA datagram (numgen inc mod
	// K == 0), for p = 1/K = 6.67% loss — inside the 5-10% target band: enough drop events to
	// sample the head-of-line latency, small enough that inner TCP survives with the fix.
	d93DropEveryNth = 15

	// d93DataSizeThreshold selects FULL-SIZED DATA datagrams (ip length > this) while sparing
	// small PROBE/handshake and inner-ICMP-ping datagrams (see the file header's arithmetic:
	// full DATA outer >= 1428 bytes, ping outer ~184, probe a few dozen). Sparing probes keeps
	// active-backup liveness UP (no failover to the drop-free standby); sparing pings makes the
	// ping HoL-BLOCKED behind a dropped stream datagram rather than directly dropped.
	d93DataSizeThreshold = 1000

	// d93UDPRateMbit / d93UDPDatagramLen size the background inner UDP stream for the loaded-RTT
	// measurement (a): a MODEST, non-saturating rate (so the ONLY RTT amplifier is the HoL hold,
	// not a standing queue) whose datagrams exceed d93DataSizeThreshold so the drop rule matches
	// them, generating the drop events the concurrent ping is then HoL-blocked behind.
	d93UDPRateMbit    = 10
	d93UDPDatagramLen = 1300

	// d93UDPLoadSecs / d93TCPLoadSecs bound the two load windows (kept short for the e2e budget;
	// long enough for the ping sample and the TCP flow to reach steady state).
	d93UDPLoadSecs = 5
	d93TCPLoadSecs = 4

	// d93MaxHoLDeltaMs bounds the worst sampled loaded inner-RTT above the idle inner-RTT: the
	// PRIMARY D93 discriminator. Pre-fix a drop in the window added ~250 ms; the fix leaves only
	// modest non-saturating bufferbloat. 100 ms is < half the 250 ms pre-fix signature.
	d93MaxHoLDeltaMs = 100.0

	// d93MinLossyTCPMbps is the SECONDARY gate: the ABSOLUTE floor lossy single-stream
	// TCP must clear, derived from the Mathis loss model (see the THROUGHPUT BOUND doc in
	// the file header): at p=6.67% the ceiling is ~8-12 Mbit/s with immediate release
	// (o3 measured 6.8), while the pre-fix 250 ms-per-drop hold predicts ~0.2 Mbit/s.
	// 2 Mbit/s sits ~10x above the pre-fix prediction and ~3x below the fixed measurement.
	d93MinLossyTCPMbps = 2.0

	// d93PathDelayMs is the per-path netem egress delay: low, so the veth inner RTT is ~ms and
	// the pre-fix 250 ms hold is an unmistakable multiple of it (see the file header).
	d93PathDelayMs = 3

	// d93UDPPort / d93TCPLossyPort are distinct from iperfDefaultPort (5201, the loss-free TCP
	// baseline) and bloatPort (5202) so the UDP-load and lossy-TCP phases never collide with the
	// baseline's socket (a rebind on the same port can hit the prior one-shot server's TIME_WAIT,
	// D3). The UDP iperf3 server still opens a TCP CONTROL socket on its port, so waitIperfListen
	// (which polls `ss -ltn`) observes it reaching LISTEN as for a TCP server.
	d93UDPPort      = 5203
	d93TCPLossyPort = 5204
)

func TestE2EResequencerHoLLatency(t *testing.T) {
	bin := buildWanbond(t)

	// paths[0] is the PRIMARY active path (all DATA, and where the drop rule lives); paths[1] a
	// healthy standby that carries only probes (no DATA) so the receiver sees a single delivering
	// pathKey — the D93 single-delivering-path steady state. NO mtu knob (auto-1500); FEC OFF
	// (no [fec] block). Veth names match the other netns tests; the suite is sequential (fixed
	// names forbid parallel) and Setup idempotently pre-deletes them.
	active := pathSpec{name: "cellular", edgeIP: "10.100.2.1", concIP: "10.100.2.2", edgeVeth: "wbBe", concVeth: "wbBc", delayMs: d93PathDelayMs}
	standby := pathSpec{name: "starlink", edgeIP: "10.100.1.1", concIP: "10.100.1.2", edgeVeth: "wbAe", concVeth: "wbAc", delayMs: d93PathDelayMs}
	paths := []pathSpec{active, standby}

	top := SetupWithPaths(t, paths)

	// Default [scheduler] (omitted) normalizes to active-backup, so setupMultipathTunnel brings
	// the bond up with paths[0] as the sole DATA-carrying path — no scheduler block to add.
	edge, conc := setupMultipathTunnel(t, top, bin, paths)
	if !top.pingUntil(concInner, 15*time.Second) {
		t.Fatalf("tunnel never came up\n--- edge ---\n%s\n--- conc ---\n%s", edge.log(), conc.log())
	}

	// Idle inner RTT (no load, no drops): the baseline the loaded-RTT bound (a) is measured
	// against, and the ~ms figure that makes the pre-fix 250 ms hold an unmistakable multiple.
	idleRTTms := pingAvgMs(t, concInner, 10)

	// Loss-free single-stream TCP baseline (b), measured BEFORE the drop rule so host conditions
	// are otherwise identical to the lossy run that follows.
	baselineMbps := top.iperf3Mbps(t, concInner, d93TCPLoadSecs)
	if baselineMbps <= 0 {
		t.Fatalf("loss-free TCP baseline measured non-positive throughput %.2f Mbit/s\n--- edge ---\n%s", baselineMbps, edge.log())
	}
	t.Logf("baseline: idle inner RTT=%.1fms, loss-free single-stream TCP=%.1f Mbit/s", idleRTTms, baselineMbps)

	// Arm the deterministic drop on the ACTIVE path's DATA (edge->concentrator), sparing
	// probes/pings by size. Probes stay UP so active-backup never fails DATA over to the standby.
	installD93DataDrop(t, top, active.concVeth, d93DataSizeThreshold, d93DropEveryNth)

	// (a) LOADED RTT: a modest, non-saturating background inner UDP stream generates the drop
	// events; the concurrent ping is HoL-blocked behind them iff the pre-fix fixed hold is in
	// force. The worst sampled loaded RTT must stay within d93MaxHoLDeltaMs of idle.
	worstLoadedRTTms := top.rttUnderUDPLoadMaxMs(t, concInner, concInner, d93UDPRateMbit, d93UDPDatagramLen, d93UDPLoadSecs)
	loadedDeltaMs := worstLoadedRTTms - idleRTTms
	t.Logf("(a) loaded worst inner RTT=%.1fms (idle=%.1fms, Δ=%.1fms) under a %d Mbit/s UDP stream with every-%dth DATA dropped (p=%.1f%%)",
		worstLoadedRTTms, idleRTTms, loadedDeltaMs, d93UDPRateMbit, d93DropEveryNth, 100.0/float64(d93DropEveryNth))
	if loadedDeltaMs >= d93MaxHoLDeltaMs {
		t.Fatalf("(a) worst loaded inner RTT rose %.1fms above idle (idle=%.1fms, worst=%.1fms), want < %.0fms — the pre-fix fixed %s head-of-line hold stalls each in-order frame behind a dropped one; the D93 single-delivering-path immediate release is NOT engaged\n--- edge ---\n%s",
			loadedDeltaMs, idleRTTms, worstLoadedRTTms, d93MaxHoLDeltaMs, resequencerTimeoutStr, edge.log())
	}

	// (b) TCP RECOVERY: single-stream inner TCP under the SAME loss must clear the
	// Mathis-derived absolute floor (see the THROUGHPUT BOUND doc above) — at this loss
	// rate TCP is loss-limited to ~8-12 Mbit/s with the fix, but the pre-fix 250 ms
	// per-drop hold inflated the effective RTT ~50x, predicting ~0.2 Mbit/s (the field
	// collapse). The loss-free baseline is logged for context only.
	lossyMbps := top.iperf3MbpsPort(t, concInner, d93TCPLoadSecs, d93TCPLossyPort)
	t.Logf("(b) lossy single-stream TCP=%.1f Mbit/s (floor %.1f Mbit/s; loss-free baseline %.1f Mbit/s, Mathis ceiling at p=%.1f%% is ~8-12 Mbit/s)",
		lossyMbps, d93MinLossyTCPMbps, baselineMbps, 100.0/float64(d93DropEveryNth))
	if lossyMbps < d93MinLossyTCPMbps {
		t.Fatalf("(b) lossy single-stream TCP collapsed to %.1f Mbit/s, want >= %.1f Mbit/s — at %.1f%% loss the Mathis ceiling is ~8-12 Mbit/s with immediate release, while the pre-fix %s-per-drop head-of-line hold predicts ~0.2 Mbit/s (the field 0-bytes-rx collapse)\n--- edge ---\n%s",
			lossyMbps, d93MinLossyTCPMbps, 100.0/float64(d93DropEveryNth), resequencerTimeoutStr, edge.log())
	}

	// (c) Two-path reorder correctness is UNAFFECTED. It is pinned end-to-end by TestP2Aggregation
	// (weighted, two real per-path UDP sockets delivering DATA concurrently through the same
	// per-peer resequencer) and at the unit level by internal/bind/multipath_d93_test.go
	// (TestSameSrcInterleavedPathIDsStillHeld: two delivering pathKeys keep the full hold) plus
	// internal/reseq. This file deliberately reuses that coverage rather than standing up parallel
	// two-path-reorder infrastructure.
	t.Logf("(c) two-path reorder correctness pinned by TestP2Aggregation (e2e) + internal/bind/multipath_d93_test.go and internal/reseq (unit) — not re-implemented here")

	t.Logf("D93 end-to-end: on ONE delivering path with %.1f%% deterministic DATA loss, loaded worst RTT stayed within %.0fms of idle and single-stream TCP cleared the %.1f Mbit/s Mathis floor — the single-delivering-path immediate release holds (T240/T241, D93, G26)",
		100.0/float64(d93DropEveryNth), d93MaxHoLDeltaMs, d93MinLossyTCPMbps)
}

// resequencerTimeoutStr is the human string of the pre-fix fixed head-of-line hold
// (internal/bind.resequencerTimeout = 250 ms), used only in this file's failure messages to name
// the RED signature the asserts discriminate against. The constant itself is unexported in the
// bind package; this literal mirrors it (a divergence would only weaken a diagnostic string).
const resequencerTimeoutStr = "250ms"

// installD93DataDrop installs an nftables input-hook rule in the concentrator (transit) netns
// that drops EVERY everyNth-th FULL-SIZED DATA datagram arriving from the active path's ingress
// veth (concVeth) in the edge->concentrator direction — the deterministic emulation of the field
// carrier's native per-path loss (D93). The rule matches, in order:
//   - iifname concVeth   — scope to the active path so its numgen counter advances ONLY by that
//     path's DATA (a counter shared across paths would not be per-path deterministic);
//   - udp dport listenPort — the tunnel's outer datagrams only;
//   - ip length > threshold — FULL-SIZED DATA only, sparing small PROBE/handshake frames (so
//     active-backup liveness stays UP and never fails DATA over to the drop-free standby) and the
//     inner ICMP ping frames (so the ping is HoL-BLOCKED behind a dropped stream datagram, not
//     dropped itself);
//   - numgen inc mod everyNth == 0 — increments only for MATCHED (full DATA) datagrams and fires
//     on every everyNth-th, dropping it. Deterministic, unlike netem's random loss.
//
// The rule lives in the concentrator netns, so top.Teardown (which kills the netns holder) reaps
// it with the namespace; a best-effort t.Cleanup deletes the table first (cleanups run LIFO, so
// this runs BEFORE Teardown while the netns is still alive) for hygiene on a reused fixture.
// It mirrors installLossyOversizeDrop (T237) — same location and structure, differing only in the
// size predicate's SENSE (T237 drops OVERSIZE probes to converge PMTU; D93 drops full DATA to
// inject loss) and the per-Nth divisor.
func installD93DataDrop(t *testing.T, top *Topology, concVeth string, threshold, everyNth int) {
	t.Helper()
	top.nsenter("nft", "add", "table", "ip", d93DropTable)
	top.nsenter("nft", "add", "chain", "ip", d93DropTable, d93DropChain,
		"{", "type", "filter", "hook", "input", "priority", "0", ";", "}")
	top.nsenter("nft", "add", "rule", "ip", d93DropTable, d93DropChain,
		"iifname", concVeth,
		"udp", "dport", strconv.Itoa(listenPort),
		"ip", "length", ">", strconv.Itoa(threshold),
		"numgen", "inc", "mod", strconv.Itoa(everyNth), "==", "0",
		"drop")
	t.Cleanup(func() {
		_ = top.tryRun("nsenter", "-t", strconv.Itoa(top.pid), "-n", "nft", "delete", "table", "ip", d93DropTable)
	})
}

// rttUnderUDPLoadMaxMs runs a background inner UDP stream (rateMbit, datagramLen-byte datagrams)
// edge->serverIP through the tunnel while sampling ping RTT to pingIP, and returns the WORST
// (max) inner RTT observed under load — the p95/worst signature the D93 loaded-RTT bound (a)
// rests on. A modest, non-saturating UDP rate is used deliberately (unlike rttUnderLoad's
// saturating TCP flow) so the ONLY RTT amplifier is the head-of-line hold, not a standing queue.
// The UDP client's own result is not asserted (its datagrams exist only to generate the
// deterministic drop events the concurrent ping is HoL-blocked behind); its output is captured
// for diagnostics on a client error.
func (top *Topology) rttUnderUDPLoadMaxMs(t *testing.T, serverIP, pingIP string, rateMbit, datagramLen, secs int) float64 {
	t.Helper()
	port := strconv.Itoa(d93UDPPort)
	// iperf3 UDP server: opens a TCP control socket on the same port, so waitIperfListen observes
	// it reaching LISTEN via `ss -ltn` exactly as for a TCP server.
	top.startProc(t, "iperf3-udp-server", "nsenter", "-t", strconv.Itoa(top.pid), "-n", "iperf3", "-s", "-1", "-B", serverIP, "-p", port)
	top.waitIperfListen(t, d93UDPPort)

	type clientResult struct {
		out []byte
		err error
	}
	ch := make(chan clientResult, 1)
	go func() {
		out, err := exec.Command("iperf3", "-c", serverIP, "-u",
			"-b", strconv.Itoa(rateMbit)+"M", "-l", strconv.Itoa(datagramLen),
			"-t", strconv.Itoa(secs), "-p", port).CombinedOutput()
		ch <- clientResult{out: out, err: err}
	}()

	// Let the stream ramp (~1s), then sample RTT across most of the remaining window at 0.1s
	// spacing so a drop-induced HoL stall is likely to be caught in the max.
	time.Sleep(1 * time.Second)
	samples := (secs - 1) * 10
	if samples < 20 {
		samples = 20
	}
	pingOut := top.runOut("ping", "-c", strconv.Itoa(samples), "-i", "0.1", "-W", "2", pingIP)
	worst := parsePingMaxMs(t, pingOut)

	r := <-ch
	if r.err != nil {
		// A UDP client returning non-zero is not itself a test failure (the datagrams' only job
		// is to generate drop events), but surface its output to aid diagnosis if the RTT is off.
		t.Logf("background UDP load client to %s exited: %v\n%s", serverIP, r.err, r.out)
	}
	return worst
}

// iperf3MbpsPort is iperf3Mbps with an explicit server port, so the lossy-TCP phase (b) uses a
// port distinct from the loss-free baseline's (iperfDefaultPort) and never rebinds it into a
// TIME_WAIT race (D3).
func (top *Topology) iperf3MbpsPort(t *testing.T, serverIP string, secs, port int) float64 {
	t.Helper()
	p := strconv.Itoa(port)
	top.startProc(t, "iperf3-server", "nsenter", "-t", strconv.Itoa(top.pid), "-n", "iperf3", "-s", "-1", "-B", serverIP, "-p", p)
	top.waitIperfListen(t, port)

	out := top.runOut("iperf3", "-c", serverIP, "-p", p, "-t", strconv.Itoa(secs), "-J")
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

// parsePingMaxMs extracts the MAX field from a ping "rtt min/avg/max/mdev" summary line (the
// worst observed RTT), distinct from parsePingAvgMs's avg field.
func parsePingMaxMs(t *testing.T, out string) float64 {
	t.Helper()
	idx := strings.Index(out, "min/avg/max")
	if idx < 0 {
		t.Fatalf("no rtt line in ping output:\n%s", out)
	}
	eq := strings.Index(out[idx:], "=")
	fields := strings.Fields(out[idx+eq+1:])
	nums := strings.Split(fields[0], "/")
	if len(nums) < 3 {
		t.Fatalf("malformed rtt summary %q", fields[0])
	}
	maxMs, err := strconv.ParseFloat(nums[2], 64)
	if err != nil {
		t.Fatalf("parse max rtt %q: %v", nums[2], err)
	}
	return maxMs
}
