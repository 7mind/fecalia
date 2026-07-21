//go:build e2e

package e2e

// TestE2ELossyPathPMTUConvergence (T237, defect D91) is the END-TO-END confirmation,
// over a real socket/netlink path, of the N-CONSECUTIVE PMTU-confirmation fix (T235/T236):
// on a partially-lossy carrier that drops a DETERMINISTIC FRACTION of OVERSIZE outer
// datagrams, PMTU discovery must converge AT/BELOW the reliably-carried size — NOT above
// it — so full-size DATA is not black-holed.
//
// REPRODUCE-FIRST / RED-on-pre-fix. D91: the PMTU binary search used to accept a candidate
// on-wire size on a SINGLE echo (internal/telemetry/pmtu.go). On a 5G-like path dropping a
// FRACTION of packets a size ABOVE the reliably-carried MTU still echoes on the probes that
// happen to pass, so single-echo acceptance converged tens of bytes too high and black-holed
// full-size DATA (field: converged inner 1331 vs a reliable ~1268–1300, 28% full-MTU loss,
// TCP 0 bytes rx). T235 replaced single-echo with N-consecutive confirmation
// (defaultPMTUConfirmations = 3; confirmCandidate short-circuits on the FIRST non-echo);
// T236 wired it through the device (pmtuConfigFor leaves Confirmations at its zero-value
// default, so reliability convergence is always on). The D91 RED is captured DETERMINISTICALLY
// at the UNIT level (internal/telemetry/pmtu_test.go: an intermittent-echo fake with
// Confirmations:1 converges ABOVE the reliable threshold, N=3 converges at/below). The pre-fix
// (single-echo) tree cannot be run in THIS test — the daemon binary is the fixed tree — so this
// test PINS THE POST-FIX INVARIANT end-to-end: over a real lossy socket path, N-consecutive
// confirmation converges at/below the reliable threshold T (never runs away to the ceiling).
//
// DETERMINISTIC LOSSY FIXTURE (the crux — why this is NOT TestE2EPMTUDiscovery/T230). T230
// pins a HARD outer link MTU (outerMTU=1400): an oversize probe there EMSGSIZE-fails at the
// edge DETERMINISTICALLY, so even the pre-fix single-echo search would converge correctly —
// a hard limit does not exercise the D91 fix at all. D91 is the PARTIALLY-lossy case where an
// oversize probe SOMETIMES echoes. So here BOTH veths stay at the default 1500 (NO outerMTU
// pin): every candidate up to the search ceiling (bind.DefaultPathMTU = 1500) physically fits
// the local link and goes ON THE WIRE, and a middlebox drops a DETERMINISTIC fraction of the
// OVERSIZE ones (installLossyOversizeDrop): `ip length > T` + `numgen inc mod 3 == 0` drops
// EVERY 3rd outer datagram whose IP length exceeds T. Deterministic — unlike netem's random
// loss.
//
// OVERHEAD ARITHMETIC (probe on-wire size vs the length-match threshold; mirrors
// internal/telemetry/pmtuprobe.go outerIPUDPOverhead=28). The PMTU search works in OUTER
// IP-level path-MTU units: a candidate `mid` IS the full outer IP datagram size on the wire.
// ProbePMTU sizes the padded probe's UDP payload `mid - outerIPUDPOverhead(28)` bytes so the
// resulting IPv4/UDP datagram is EXACTLY `mid`. The middlebox's `ip length` match reads that
// same full IP total-length field, so `ip length > T` (T = lossyOversizeThreshold = 1400)
// rejects candidates mid >= 1401 and passes candidates mid <= 1400. With N=3 confirmation and
// every-3rd-oversize dropped: among ANY 3 consecutive oversize probes exactly one is dropped
// (numgen inc mod 3 == 0), so NO oversize candidate can accumulate 3 consecutive echoes ->
// every mid > T is rejected (the first non-echo short-circuits the candidate, hi = mid-1); a
// candidate mid <= T is never matched -> echoes 3/3 -> accepted (lo = mid). Net: the search
// converges to EXACTLY T = 1400 (outer), and the T209 resizer folds that to the inner TUN MTU
// InnerMTU(1400) = 1300 (= 1400 - 28 IPv4/UDP - 40 DATA-frame - 32 WG-transport). Boot TUN is
// InnerMTU(DefaultPathMTU=1500) = 1400; a PRE-FIX runaway (converged above T) would tighten to
// some InnerMTU(>1400) != 1300 and this test's exact-convergence wait would TIME OUT.
//
// CONVERGENCE TOLERANCE BAND. The invariant is the inequality PMTUFloor < converged <= T (the
// converged OUTER pmtu is at/below the reliable threshold AND did not collapse to the floor).
// The fixture converges DETERMINISTICALLY to exactly T, so the exact-convergence wait targets
// InnerMTU(T); the subsequent explicit band assertion — InnerMTU(PMTUFloor) < tun <= InnerMTU(T)
// — documents the invariant and its 120-byte outer band (T - PMTUFloor = 1400 - 1280), whose
// UPPER edge (<= InnerMTU(T)) is the load-bearing discriminator against a runaway toward the
// ceiling and whose LOWER edge (> InnerMTU(PMTUFloor)) rejects a degenerate collapse to the
// floor.
//
// The lossy path is the PRIMARY (paths[0]); a healthy 1500 secondary rides alongside so the
// bond is robustly UP and the daemon's assumed-1500 ceiling is exercised on a real second path
// (it converges to 1500, is min'd out by the lossy path's InnerMTU(1400), and — because the
// drop rule is scoped to the lossy path's ingress veth via `iifname` — never perturbs the
// lossy path's numgen counter, keeping the every-3rd drop PER-PATH deterministic; a counter
// shared across two concurrently-probing paths would not be).
//
// HARDWARE TIER — DO NOT RUN IN THE DEFAULT GATE. Like every `//go:build e2e` test here it
// needs root, /dev/net/tun, tc, and nftables inside a network namespace; the non-privileged
// gate only COMPILES + `go vet -tags e2e`s + lints it. The privileged RUN (GREEN on the fixed
// tree) is validated on remote hardware (T238; o3.7mind.io / llm-ubuntu-0.pgtr.7mind.io).
// VALIDATE THERE WITH `-run TestE2ELossyPathPMTUConvergence -count=3`, NEVER the full suite:
// two root-netns suites deadlock to the 600s timeout and TestDNSHubResolveAndReroute
// false-fails against the o3 host's PERSISTENT root-netns concentrator (wanbond0@mtu1280).
// This test builds its OWN per-test isolated netns (SetupWithPaths) and needs NO root-netns
// concentrator, so it does not collide with that persistent process. It binds no /metrics
// listener (the assert reads the link MTU directly), so it claims no port in netns.go's
// metricsPortRegistry.

import (
	"strconv"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/bind"
	"github.com/7mind/wanbond/internal/telemetry"
)

const (
	// lossyOversizeThreshold (T) is the reliably-carried outer path-MTU: the middlebox drops
	// a deterministic fraction of every outer datagram whose IP length EXCEEDS it, so a probe
	// candidate mid > T fails within any 3-consecutive-probe window while a candidate mid <= T
	// always echoes. It sits strictly between PMTUFloor (1280) and the search ceiling
	// bind.DefaultPathMTU (1500), leaving a clear (T, 1500] band the N=3 search must reject and
	// a [1280, T] band it must accept.
	lossyOversizeThreshold = 1400

	// lossyDropEveryNth K: the middlebox drops every K-th OVERSIZE datagram (numgen inc mod K
	// == 0). K == the confirmation count (defaultPMTUConfirmations = 3) is sufficient: among
	// any K consecutive oversize probes exactly one is dropped, so no oversize candidate can
	// echo K consecutive times.
	lossyDropEveryNth = 3

	// lossyDropTable / lossyDropChain name the nftables table+chain installed in the
	// concentrator (transit) netns that drops the deterministic fraction of oversize outer
	// probes, mirroring the field 5G carrier's partial loss on full-size datagrams (D91).
	lossyDropTable = "wbd91"
	lossyDropChain = "lossydrop"
)

func TestE2ELossyPathPMTUConvergence(t *testing.T) {
	bin := buildWanbond(t)

	// The lossy path is the PRIMARY (paths[0]); the healthy 1500 secondary keeps the bond up.
	// NEITHER declares outerMTU: both veths stay at 1500 so oversize probes go ON THE WIRE and
	// the middlebox — not a hard link MTU (T230's mechanism) — is the operative loss source.
	// NO `mtu` knob is declared either, so auto-discovery is the sole PMTU source. The veth
	// names match T210/T230; the netns suite is sequential (fixed names forbid parallel) and
	// Setup idempotently pre-deletes them.
	lossy := pathSpec{name: "cellular", edgeIP: "10.100.2.1", concIP: "10.100.2.2", edgeVeth: "wbBe", concVeth: "wbBc", delayMs: 20}
	healthy := pathSpec{name: "starlink", edgeIP: "10.100.1.1", concIP: "10.100.1.2", edgeVeth: "wbAe", concVeth: "wbAc", delayMs: 20}
	paths := []pathSpec{lossy, healthy}

	top := SetupWithPaths(t, paths)
	installLossyOversizeDrop(t, top, lossy.concVeth, lossyOversizeThreshold)

	// "", 0 declares NO per-path mtu knob: the sizing must come from auto-discovery.
	edge, conc := setupConstrainedTunnel(t, top, bin, paths, "", 0)
	if !top.pingUntil(concInner, 15*time.Second) {
		t.Fatalf("tunnel never came up\n--- edge ---\n%s\n--- conc ---\n%s", edge.log(), conc.log())
	}

	// The lossy path's N=3 search must converge at EXACTLY the reliable threshold T = 1400
	// (outer), which the T209 resizer folds — as the min across paths — into the inner TUN MTU
	// InnerMTU(1400). A PRE-FIX single-echo runaway would converge ABOVE T and tighten to some
	// InnerMTU(>1400) != this target, so this exact-convergence wait would TIME OUT.
	wantTun := bind.InnerMTU(lossyOversizeThreshold, false) // InnerMTU(1400) = 1300
	if !top.waitLinkMTU(t, tunDev, false, wantTun, pmtuConvergeTimeout) {
		t.Fatalf("wanbond0 MTU = %d after %s, want auto-discovered InnerMTU(%d) = %d — PMTU discovery did NOT converge at/below the reliably-carried threshold T=%d over the deterministic-loss path (a single-echo/pre-fix search runs away ABOVE T and never tightens to this value)\n--- edge ---\n%s",
			top.linkMTU(t, tunDev, false), pmtuConvergeTimeout, lossyOversizeThreshold, wantTun, lossyOversizeThreshold, edge.log())
	}

	// Post-fix invariant band (outer): PMTUFloor < converged <= T, read via the inner TUN MTU
	// (InnerMTU is monotonic in the outer PMTU). The UPPER bound is the load-bearing D91
	// discriminator (converged did NOT run away toward the ceiling); the LOWER bound rejects a
	// degenerate collapse to the floor.
	got := top.linkMTU(t, tunDev, false)
	ceilInner := bind.InnerMTU(lossyOversizeThreshold, false) // InnerMTU(1400) = 1300
	floorInner := bind.InnerMTU(telemetry.PMTUFloor, false)   // InnerMTU(1280) = 1180
	if got > ceilInner {
		t.Fatalf("converged wanbond0 MTU = %d > InnerMTU(T=%d) = %d — the PMTU search ran AWAY above the reliably-carried threshold (D91 regression: single-echo acceptance on an intermittently-echoing oversize candidate)\n--- edge ---\n%s",
			got, lossyOversizeThreshold, ceilInner, edge.log())
	}
	if got <= floorInner {
		t.Fatalf("converged wanbond0 MTU = %d <= InnerMTU(PMTUFloor=%d) = %d — discovery collapsed to the floor instead of converging at the reliably-carried threshold\n--- edge ---\n%s",
			got, telemetry.PMTUFloor, floorInner, edge.log())
	}
	t.Logf("lossy-path convergence: wanbond0 = %d (InnerMTU(%d)) — the N=%d-consecutive search converged AT/BELOW the reliably-carried threshold T=%d over a path dropping every %drd oversize outer datagram, NOT away to the ceiling InnerMTU(%d)=%d (D91 post-fix invariant, end-to-end)",
		got, lossyOversizeThreshold, lossyDropEveryNth, lossyOversizeThreshold, lossyDropEveryNth, bind.DefaultPathMTU, bind.InnerMTU(bind.DefaultPathMTU, false))
}

// installLossyOversizeDrop installs an nftables input-hook rule in the concentrator
// (far-side/transit) netns that drops EVERY lossyDropEveryNth-th OVERSIZE outer probe
// arriving from the lossy path's ingress veth (concVeth) — the deterministic emulation of a
// 5G carrier dropping a fraction of full-size datagrams (D91). The rule matches, in order:
//   - iifname concVeth      — scope to the lossy path so its numgen counter is advanced ONLY
//     by that path's probes (a counter shared across two concurrently-probing paths would not
//     be per-path deterministic);
//   - ip length > threshold — the OVERSIZE outer datagrams (the IP total-length field equals
//     the PMTU search's candidate on-wire size; see the file header's overhead arithmetic);
//   - udp dport listenPort  — the tunnel's outer probes only (small WG handshake/keepalive
//     datagrams never match the length test, so they never advance the counter);
//   - numgen inc mod K == 0 — increments only for MATCHED (oversize) datagrams and fires on
//     every K-th, dropping it. Deterministic, unlike netem's random loss.
//
// The rule lives in the concentrator netns, so top.Teardown (which kills the netns holder)
// reaps it with the namespace; a best-effort t.Cleanup deletes the table first (cleanups run
// LIFO, so this runs BEFORE Teardown while the netns is still alive) for hygiene on a reused
// fixture.
func installLossyOversizeDrop(t *testing.T, top *Topology, concVeth string, threshold int) {
	t.Helper()
	top.nsenter("nft", "add", "table", "ip", lossyDropTable)
	top.nsenter("nft", "add", "chain", "ip", lossyDropTable, lossyDropChain,
		"{", "type", "filter", "hook", "input", "priority", "0", ";", "}")
	top.nsenter("nft", "add", "rule", "ip", lossyDropTable, lossyDropChain,
		"iifname", concVeth,
		"ip", "length", ">", strconv.Itoa(threshold),
		"udp", "dport", strconv.Itoa(listenPort),
		"numgen", "inc", "mod", strconv.Itoa(lossyDropEveryNth), "==", "0",
		"drop")
	t.Cleanup(func() {
		_ = top.tryRun("nsenter", "-t", strconv.Itoa(top.pid), "-n", "nft", "delete", "table", "ip", lossyDropTable)
	})
}
