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

	"github.com/7mind/wanbond/internal/metrics"
)

// T121 is the privileged netns e2e for the D36/D37 one-sided-restart recovery fix
// (goals:G7). It is the VALIDATION half of D36's confirmed root cause: after a peer
// restarts, its outer-seq resets to ~1, far below the stale-high release point the
// pre-restart saturating stream advanced the SURVIVING side's per-peer resequencer
// `next` to. Absent a trusted re-anchor, admit() drops those low-seq frames — including
// the wrapped WG init/response — as SUSPECT (reseq.go), so the tunnel stays down until a
// WG rescue timer fires (field-reported as a multi-minute outage). T119 wired the
// authenticated peer-restart epoch change (T116/T117) to the per-peer
// Resequencer.RebaselineToLow re-anchor in dispatchInbound, and T120 drives a forced WG
// handshake initiation off the first path-up edge (D37). This test proves both, in BOTH
// restart directions, and captures the counters + 0->1 timestamps for the D36 record.
//
// Two SEPARATE runs (each its own fresh topology — the fixture's fixed veth names forbid
// two live topologies, mirroring TestStandbyLivenessBidirectional):
//
//   - restart-only-edge (run A): the SURVIVOR is the concentrator. Saturate edge->conc so
//     the concentrator's resequencer `next` advances past resequencerWindow (2048), then
//     restart ONLY the edge process (live paths, no endpoint change; no failover).
//   - restart-only-concentrator (run B): the SURVIVOR is the edge. Saturate conc->edge
//     (iperf3 reverse) so the edge's resequencer `next` advances past the window, then
//     restart ONLY the concentrator process (no failover — the concentrator role runs no
//     failover controller at all).
//
// For each direction it asserts:
//
//	(1) wanbond_session_established on the RESTARTED side transitions 0->1 within
//	    r121ReconvergeBudget — well under WG's own rescue timers (the ~180s
//	    REJECT_AFTER_TIME keypair expiry / the field multi-minute outage), targeting the
//	    ~25s both-ends-fresh baseline. The true per-direction magnitude is LOGGED (static
//	    analysis predicts ~10s for the edge-restart direction).
//	(2) the SURVIVING side's per-peer resequencer counters show
//	    wanbond_resequencer_rebaselines_total delta >= 1 (the trusted restart re-anchor
//	    fired) AND ~0 wanbond_resequencer_dropped_suspect_frames_total delta (the wrapped
//	    init was re-anchored, NOT suspect-dropped — the D36 pathology would drop dozens to
//	    hundreds over the outage).
//	(3) D37: from cold start, the edge's time-to-first-handshake tracks the first path-up
//	    edge (+~1 RTT), NOT a 5s WG retransmit-timer multiple — asserted from the edge log
//	    timestamps at INITIAL bring-up (before any restart), in both runs.
//
// EXECUTION IS DEFERRED (G2 pattern). The netns tier needs the privileged two-namespace
// fixture + CAP_NET_ADMIN + /dev/net/tun and is NOT run in the unit environment; this file
// must COMPILE and vet under -tags e2e (`go vet -tags e2e ./test/e2e` + `go build -tags e2e
// ./test/e2e`) and, run WITHOUT privileges, SKIPS (see requireNetAdmin) rather than fails.
// The privileged run is deferred to the o3 (aarch64) + llm-ubuntu-0 (amd64) hosts.
//
// RUNBOOK (privileged execution, per the o3-hardware-e2e G2 pattern — NOT part of the
// merge gate). SSH always with `-F none` (the workers' system ssh_config is broken); sudo
// resets PATH, so re-inject it for the test process (it shells out to go/ip/nsenter/iperf3).
// Sync the working tree first (tar-over-ssh, excluding .git .cq .codegraph .claude vendor
// result) and `apt-get install -y iperf3 gcc` + Go 1.26.4 to /usr/local/go once per host.
//
//	# aarch64 (Oracle Cloud):
//	ssh -F none -i /run/agenix/llm-ssh-key ubuntu@o3.7mind.io \
//	  'cd ~/wanbond && sudo env PATH=/usr/local/go/bin:/usr/sbin:/usr/bin:/sbin:/bin \
//	     HOME=/root GOTOOLCHAIN=local GOCACHE=/tmp/gocache \
//	     go test -tags e2e ./test/e2e -run TestOneSidedRestartRecovery -v'
//
//	# amd64 (different network):
//	ssh -F none -i /run/agenix/llm-ssh-key ubuntu@llm-ubuntu-0.pgtr.7mind.io \
//	  'cd ~/wanbond && sudo env PATH=/usr/local/go/bin:/usr/sbin:/usr/bin:/sbin:/bin \
//	     HOME=/root GOTOOLCHAIN=local GOCACHE=/tmp/gocache \
//	     go test -tags e2e ./test/e2e -run TestOneSidedRestartRecovery -v'
const (
	// r121EdgeMetrics is the EDGE's /metrics listen (loopback — the edge runs in the base
	// test-process netns, so 127.0.0.1 is directly scrapeable). The concentrator runs in the
	// PEER netns, where the T17 requireLoopback invariant (internal/metrics/server.go,
	// docs/design.md:740) UNCONDITIONALLY refuses any non-loopback bind — so it too binds
	// 127.0.0.1 (r121ConcMetricsListen/r121ConcMetricsURL), reachable from the base netns only
	// by dialing INTO the peer netns (fetchMetricsInNetns/netnsMetricsClient — like p2/p3/p4
	// and multipeer_hardened_test.go's hwMetricsHost). Both sides use the SAME port 9104
	// (distinct netns — no collision); 9104 is this file's registry entry (see netns.go
	// metricsPortRegistry).
	r121MetricsPort       = 9104
	r121EdgeMetrics       = "127.0.0.1:9104"
	r121EdgeMetricsURL    = "http://" + r121EdgeMetrics + "/metrics"
	r121ConcMetricsListen = "127.0.0.1:9104"
	r121ConcMetricsURL    = "http://" + r121ConcMetricsListen + "/metrics"

	// r121ReconvergeBudget bounds one-sided-restart reconvergence (session_established
	// 0->1). It sits WELL under WG's own rescue timers and targets the ~25s both-ends-fresh
	// baseline, with headroom so a slow CI host does not flake. The measured per-direction
	// magnitude is LOGGED for the D36 record (edge-restart predicted ~10s).
	r121ReconvergeBudget = 30 * time.Second

	// r121D37Budget bounds cold-start time-to-first-handshake measured from the first
	// path-up edge: it must track path-up (+~1 RTT), NOT a 5s WG retransmit-timer multiple
	// (the D37 symptom). Strictly below one 5s retransmit so a regression to the retransmit
	// path (>=5s) fails the assertion.
	r121D37Budget = 4 * time.Second

	// r121SatSeconds is the saturating-flow duration: long enough to push the surviving
	// side's resequencer `next` WELL past resequencerWindow (2048) frames — the D36
	// precondition, without which the restarted peer's outer-seq-~1 frames would admit
	// normally and the SUSPECT-drop pathology would never arise.
	r121SatSeconds = 6

	// r121ReseqWindow mirrors internal/bind.resequencerWindow (2048, unexported): the
	// precondition asserts the survivor released MORE than one window of frames, so its
	// release point `next` is provably past the window before the restart.
	r121ReseqWindow = 2048

	// r121DropSuspectSlack tolerates at most a few genuine stale-high reorder stragglers
	// being SUSPECT-dropped around the restart (which T119's low-anchor re-anchor drops
	// CORRECTLY). It is far below the dozens-to-hundreds the D36 pathology would drop while
	// every wrapped-init retransmit is suspect-dropped over the multi-minute outage, so it
	// still catches the defect while not flaking on bounded cross-path reorder.
	r121DropSuspectSlack = 3
)

// TestOneSidedRestartRecovery runs both D36/D37 restart directions (run A: restart only
// the edge; run B: restart only the concentrator), each on its own fresh topology.
func TestOneSidedRestartRecovery(t *testing.T) {
	requireNetAdmin(t)
	bin := buildWanbond(t)

	t.Run("restart-only-edge", func(t *testing.T) {
		testRestartOnlyEdge(t, bin)
	})
	t.Run("restart-only-concentrator", func(t *testing.T) {
		testRestartOnlyConcentrator(t, bin)
	})
}

// testRestartOnlyEdge is run A: the concentrator is the SURVIVOR. The edge saturates the
// bond (edge->conc), advancing the concentrator's per-peer resequencer `next` past the
// window; then ONLY the edge process restarts. The concentrator must re-anchor (rebaseline)
// on the edge's authenticated restart epoch and admit the wrapped init instead of
// suspect-dropping it, and the edge's session_established must return 0->1 within budget.
func testRestartOnlyEdge(t *testing.T, bin string) {
	t.Helper()
	top := SetupWithPaths(t, DefaultPaths)

	edge, conc, edgeArgv, _ := r121BringUp(t, top, bin, DefaultPaths)

	if !top.pingUntil(concInner, 15*time.Second) {
		t.Fatalf("bond never came up\n--- edge ---\n%s\n--- conc ---\n%s", edge.log(), conc.log())
	}
	// (1) initial convergence: the restarted side we will re-assert is the edge; confirm it
	// established at all before the restart so the post-restart 0->1 is a genuine transition.
	waitSessionEstablished(t, r121EdgeMetricsURL, r121ReconvergeBudget)
	// (3) D37 cold-start first-handshake, from the edge log at initial bring-up.
	assertD37FirstHandshake(t, edge)

	// D36 precondition: saturate edge->conc so the CONCENTRATOR resequencer `next` advances
	// well past the window. iperf3 client on the edge, one-shot server in the peer netns.
	top.startProc(t, "iperf3-server", "nsenter", "-t", strconv.Itoa(top.pid), "-n", "iperf3", "-s", "-1", "-B", concInner)
	time.Sleep(400 * time.Millisecond)
	top.startProc(t, "iperf3-load", "iperf3", "-c", concInner, "-t", strconv.Itoa(r121SatSeconds))
	time.Sleep(time.Duration(r121SatSeconds+1) * time.Second) // let the flow run to completion + queues drain

	before := fetchMetricsInNetns(t, top.pid, r121ConcMetricsURL)
	relBefore := r121PeerCounter(t, before, metrics.MetricReseqReleased, "concentrator survivor")
	if relBefore <= r121ReseqWindow {
		t.Fatalf("D36 precondition unmet: concentrator survivor released only %.0f frames (<= window %d) — `next` did not advance past the window, so a restart could not exercise the SUSPECT-drop path",
			relBefore, r121ReseqWindow)
	}
	rebaseBefore := r121PeerCounter(t, before, metrics.MetricReseqRebaselines, "concentrator survivor")
	dropBefore := r121PeerCounter(t, before, metrics.MetricReseqDroppedSuspect, "concentrator survivor")

	// RESTART ONLY THE EDGE (tun_persist keeps wanbond0 + its inner addr across the stop).
	restartAt := time.Now()
	top.stopAndWait(t, edge)
	edge = top.startProc(t, "edge", edgeArgv...)
	if !r121WaitReadopted(t, edge, 5*time.Second) {
		t.Fatalf("restarted edge did not re-adopt %s: no 'tunnel interface up' record in the restarted process log within 5s\n%s", tunDev, edge.log())
	}

	// (1) the restarted edge's session_established returns 0->1 within budget.
	waitSessionEstablished(t, r121EdgeMetricsURL, r121ReconvergeBudget)
	reconv := time.Since(restartAt)
	if !top.pingUntil(concInner, r121ReconvergeBudget) {
		t.Fatalf("tunnel did not carry inner traffic within %s of the edge restart\n--- edge ---\n%s\n--- conc ---\n%s",
			r121ReconvergeBudget, edge.log(), conc.log())
	}

	// (2) the SURVIVING concentrator re-anchored (rebaselines>=1) and did NOT suspect-drop
	// the wrapped init (~0 dropSuspect delta).
	after := fetchMetricsInNetns(t, top.pid, r121ConcMetricsURL)
	rebaseAfter := r121PeerCounter(t, after, metrics.MetricReseqRebaselines, "concentrator survivor")
	dropAfter := r121PeerCounter(t, after, metrics.MetricReseqDroppedSuspect, "concentrator survivor")
	r121AssertRecovery(t, "edge-restart", reconv, rebaseBefore, rebaseAfter, dropBefore, dropAfter, relBefore)
}

// testRestartOnlyConcentrator is run B: the edge is the SURVIVOR. The concentrator
// saturates the bond in REVERSE (conc->edge via iperf3 -R), advancing the edge's per-peer
// resequencer `next` past the window; then ONLY the concentrator process restarts (no
// failover — the concentrator role runs none). The edge must re-anchor on the
// concentrator's authenticated restart epoch, and the concentrator's session_established
// must return 0->1 within budget.
func testRestartOnlyConcentrator(t *testing.T, bin string) {
	t.Helper()
	top := SetupWithPaths(t, DefaultPaths)

	edge, conc, _, concArgv := r121BringUp(t, top, bin, DefaultPaths)

	if !top.pingUntil(concInner, 15*time.Second) {
		t.Fatalf("bond never came up\n--- edge ---\n%s\n--- conc ---\n%s", edge.log(), conc.log())
	}
	waitSessionEstablishedInNetns(t, top.pid, r121ConcMetricsURL, r121ReconvergeBudget)
	// (3) D37 cold-start first-handshake is an EDGE property; assert it on the edge log at
	// initial bring-up (the concentrator is the responder and initiates nothing).
	assertD37FirstHandshake(t, edge)

	// D36 precondition: saturate conc->edge (reverse) so the EDGE resequencer `next` advances
	// past the window. The one-shot server runs in the peer netns; iperf3 -R makes the SERVER
	// (concentrator side) send to the client (edge), so the edge is the receiver.
	top.startProc(t, "iperf3-server", "nsenter", "-t", strconv.Itoa(top.pid), "-n", "iperf3", "-s", "-1", "-B", concInner)
	time.Sleep(400 * time.Millisecond)
	top.startProc(t, "iperf3-load", "iperf3", "-c", concInner, "-R", "-t", strconv.Itoa(r121SatSeconds))
	time.Sleep(time.Duration(r121SatSeconds+1) * time.Second)

	before := scrapeMetrics(t, r121EdgeMetricsURL)
	relBefore := r121PeerCounter(t, before, metrics.MetricReseqReleased, "edge survivor")
	if relBefore <= r121ReseqWindow {
		t.Fatalf("D36 precondition unmet: edge survivor released only %.0f frames (<= window %d) — `next` did not advance past the window",
			relBefore, r121ReseqWindow)
	}
	rebaseBefore := r121PeerCounter(t, before, metrics.MetricReseqRebaselines, "edge survivor")
	dropBefore := r121PeerCounter(t, before, metrics.MetricReseqDroppedSuspect, "edge survivor")

	// RESTART ONLY THE CONCENTRATOR (tun_persist keeps the peer-netns wanbond0 + concInner).
	restartAt := time.Now()
	top.stopAndWait(t, conc)
	conc = top.startProc(t, "concentrator", concArgv...)
	if !r121WaitReadopted(t, conc, 5*time.Second) {
		t.Fatalf("restarted concentrator did not re-adopt %s: no 'tunnel interface up' record in the restarted process log within 5s\n%s", tunDev, conc.log())
	}

	// (1) the restarted concentrator's session_established returns 0->1 within budget.
	waitSessionEstablishedInNetns(t, top.pid, r121ConcMetricsURL, r121ReconvergeBudget)
	reconv := time.Since(restartAt)
	if !top.pingUntil(concInner, r121ReconvergeBudget) {
		t.Fatalf("tunnel did not carry inner traffic within %s of the concentrator restart\n--- edge ---\n%s\n--- conc ---\n%s",
			r121ReconvergeBudget, edge.log(), conc.log())
	}

	// (2) the SURVIVING edge re-anchored and did NOT suspect-drop the wrapped init.
	after := scrapeMetrics(t, r121EdgeMetricsURL)
	rebaseAfter := r121PeerCounter(t, after, metrics.MetricReseqRebaselines, "edge survivor")
	dropAfter := r121PeerCounter(t, after, metrics.MetricReseqDroppedSuspect, "edge survivor")
	r121AssertRecovery(t, "concentrator-restart", reconv, rebaseBefore, rebaseAfter, dropBefore, dropAfter, relBefore)
}

// r121AssertRecovery is the shared (2) assertion + D36-record logging for both directions:
// the survivor rebaselined at least once and did not suspect-drop the wrapped init.
func r121AssertRecovery(t *testing.T, dir string, reconv time.Duration, rebaseBefore, rebaseAfter, dropBefore, dropAfter, released float64) {
	t.Helper()
	rebaseDelta := rebaseAfter - rebaseBefore
	dropDelta := dropAfter - dropBefore
	// Capture the counters + reconvergence magnitude in the test output for the D36 record.
	t.Logf("D36/%s: session_established 0->1 in %s (budget %s); survivor released=%.0f (> window %d) "+
		"rebaselines %.0f->%.0f (delta %.0f) dropSuspect %.0f->%.0f (delta %.0f)",
		dir, reconv.Round(time.Millisecond), r121ReconvergeBudget, released, r121ReseqWindow,
		rebaseBefore, rebaseAfter, rebaseDelta, dropBefore, dropAfter, dropDelta)
	if rebaseDelta < 1 {
		t.Errorf("D36/%s: survivor rebaselines delta = %.0f, want >= 1 — the authenticated peer-restart epoch change did "+
			"not re-anchor the survivor's resequencer, so the restarted peer's low-outer-seq wrapped init would be "+
			"SUSPECT-dropped (the D36 multi-minute-outage pathology). T119 wires this via dispatchInbound's "+
			"RebaselineToLow on epochChanged.", dir, rebaseDelta)
	}
	if dropDelta > r121DropSuspectSlack {
		t.Errorf("D36/%s: survivor dropSuspect delta = %.0f across the restart, want ~0 (<= %d slack for stale-high "+
			"reorder stragglers) — the wrapped WG init/response was SUSPECT-dropped rather than re-anchored, which is "+
			"exactly the D36 defect: recovery then waits on a WG rescue timer instead of the trusted re-anchor.",
			dir, dropDelta, r121DropSuspectSlack)
	}
}

// assertD37FirstHandshake is the (3) D37 assertion: from cold start, the edge's first WG
// handshake tracks the first path-up edge (+~1 RTT), NOT a 5s WG retransmit-timer multiple.
// It reads the edge log timestamps (INFO 'path liveness transition' to=up and 'session
// established'), the same evidence TestSessionEstablishedTransitions uses.
func assertD37FirstHandshake(t *testing.T, edge *proc) {
	t.Helper()
	logText := edge.log()
	pathUpAt, ok := pathLivenessUpTime(logText)
	if !ok {
		t.Fatalf("D37: no 'path liveness transition' to=up record in the edge log\n%s", logText)
	}
	sessionAt, ok := logRecordTime(logText, "session established")
	if !ok {
		t.Fatalf("D37: no 'session established' record in the edge log\n%s", logText)
	}
	if sessionAt.Before(pathUpAt) {
		t.Errorf("D37: session established (%s) preceded path up (%s); expected path up FIRST",
			sessionAt.Format(time.RFC3339Nano), pathUpAt.Format(time.RFC3339Nano))
	}
	delta := sessionAt.Sub(pathUpAt)
	t.Logf("D37 cold start: first handshake %s after the first path-up edge (budget %s)", delta.Round(time.Millisecond), r121D37Budget)
	if delta > r121D37Budget {
		t.Errorf("D37: cold-start time-to-first-handshake = %s after path-up, want <= %s (~1 RTT) — a value tracking a 5s "+
			"WG retransmit-timer multiple means the first init fired pre-liveness and was NOT re-driven off the path-up "+
			"edge (the D37 symptom). T120 drives a forced SendHandshakeInitiation off the first path-up edge.",
			delta, r121D37Budget)
	}
}

// r121BringUp starts the concentrator (peer netns, via nsenter) and the edge (base netns),
// waits for both wanbond0 TUNs, plumbs the inner overlay addresses, and returns both procs
// plus the exact argv each was started with (so a restart re-launches an identical process).
// Both ends run tun_persist=true so the inner address survives a full daemon stop/start, and
// at [log] level "info" so the D37 path-up / session-established records are emitted. Both
// ends bind their /metrics on loopback (127.0.0.1) — the T17 requireLoopback invariant
// (internal/metrics/server.go) unconditionally refuses any non-loopback bind — so the
// concentrator's endpoint (r121ConcMetricsListen) is reachable from the base netns only by
// dialing INTO the peer netns (fetchMetricsInNetns/waitSessionEstablishedInNetns).
func r121BringUp(t *testing.T, top *Topology, bin string, paths []pathSpec) (edge, conc *proc, edgeArgv, concArgv []string) {
	t.Helper()

	edgePriv, edgePub := genKey(t)
	concPriv, concPub := genKey(t)
	psk := randKey(t)

	dir := t.TempDir()
	edgeCfg := writeConfig(t, filepath.Join(dir, "edge.toml"),
		r121EdgeConfig(psk, edgePriv, concPub, paths, r121EdgeMetrics))
	concCfg := writeConfig(t, filepath.Join(dir, "conc.toml"),
		r121ConcConfig(psk, concPriv, edgePub, paths, r121ConcMetricsListen))

	edgeArgv = []string{bin, "--config", edgeCfg}
	concArgv = []string{"nsenter", "-t", strconv.Itoa(top.pid), "-n", bin, "--config", concCfg}

	conc = top.startProc(t, "concentrator", concArgv...)
	edge = top.startProc(t, "edge", edgeArgv...)

	if !top.waitLink(tunDev, false, 5*time.Second) {
		t.Fatalf("edge %s never appeared\n%s", tunDev, edge.log())
	}
	if !top.waitLink(tunDev, true, 5*time.Second) {
		t.Fatalf("concentrator %s never appeared\n%s", tunDev, conc.log())
	}
	top.run("ip", "addr", "add", edgeInner+"/24", "dev", tunDev)
	top.run("ip", "link", "set", tunDev, "up")
	top.nsenter("ip", "addr", "add", concInner+"/24", "dev", tunDev)
	top.nsenter("ip", "link", "set", tunDev, "up")
	return edge, conc, edgeArgv, concArgv
}

// r121EdgeConfig renders the edge TOML: one source-bound socket per path targeting that
// path's concentrator address, tun_persist so the inner addr survives a restart, an edge
// /metrics block, and info-level logging for the D37 records.
func r121EdgeConfig(psk, edgePriv, concPub string, paths []pathSpec, metricsListen string) string {
	var b strings.Builder
	for _, p := range paths {
		fmt.Fprintf(&b, "[[paths]]\nname = %q\nsource_addr = %q\ndest_addr = \"%s:%d\"\n\n", p.name, p.edgeIP, p.concIP, listenPort)
	}
	return fmt.Sprintf(`role = "edge"
psk = "%s"
tun_persist = true

%s[metrics]
listen = %q

[wireguard]
private_key = "%s"

[[wireguard.peers]]
public_key = "%s"
endpoint = "%s:%d"
allowed_ips = ["%s/32"]

[log]
level = "info"
`, psk, b.String(), metricsListen, edgePriv, concPub, paths[primaryPathIdx].concIP, listenPort, concInner)
}

// r121ConcConfig renders the concentrator TOML: one source-bound socket per path, tun_persist,
// a /metrics block bound to loopback (127.0.0.1 — required by the T17 requireLoopback
// invariant; scraped from the base netns via fetchMetricsInNetns/waitSessionEstablishedInNetns),
// and info logs.
func r121ConcConfig(psk, concPriv, edgePub string, paths []pathSpec, metricsListen string) string {
	var b strings.Builder
	for _, p := range paths {
		fmt.Fprintf(&b, "[[paths]]\nname = %q\nsource_addr = %q\n\n", p.name, p.concIP)
	}
	return fmt.Sprintf(`role = "concentrator"
psk = "%s"
tun_persist = true

%s[metrics]
listen = %q

[wireguard]
private_key = "%s"
listen_port = %d

[[wireguard.peers]]
public_key = "%s"
allowed_ips = ["%s/32"]

[log]
level = "info"
`, psk, b.String(), metricsListen, concPriv, listenPort, edgePub, edgeInner)
}

// waitSessionEstablishedInNetns polls url's /metrics — reached by dialing INTO the peer
// netns (pid), via netnsMetricsClient — until wanbond_session_established reads 1, or fails
// at deadline. The netns-aware analogue of waitSessionEstablished (session_established_test.go),
// needed here because the concentrator's /metrics binds loopback INSIDE the peer netns
// (r121ConcMetricsListen — the T17 requireLoopback invariant forbids binding its uplink
// address), unlike i2's base-netns-reachable endpoint. A mid-poll scrape error is tolerated
// (the endpoint may not be up yet), mirroring waitSessionEstablished's own tolerance and
// hwFixture.waitPeerPathUp's netns-dial pattern. Reuses ONE netnsMetricsClient across the
// poll (DisableKeepAlives dials fresh each scrape, so the socket re-opens inside the peer
// netns every time).
func waitSessionEstablishedInNetns(t *testing.T, pid int, url string, deadline time.Duration) {
	t.Helper()
	client := netnsMetricsClient(pid)
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		exp, err := metrics.Fetch(ctx, client, url)
		cancel()
		if err == nil {
			if v, ok := exp.Value(metrics.MetricSessionEstablished); ok && v == 1 {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("wanbond_session_established never reached 1 within %s", deadline)
}

// r121PeerCounter reads a resequencer counter for the survivor's sole peer. Because each
// daemon binds EXACTLY ONE peer, metrics.NewCollector sets multiPeer = len(PeerNames())>1
// = FALSE (internal/metrics/metrics.go:260-266), so the reseq series are emitted UNLABELED
// — the value lives in the no-label exposition read by Value(name), exactly as
// internal/metrics' TestExpositionReseqRebaselineAndDropSuspect asserts. (Since D58 no
// exposition emits a peer="" label at all: a single-peer exposition is UNLABELED and a
// multi-peer one carries every peer's configured name, so there is no multi-peer peer=""
// series to probe here — this survivor is single-peer, so its counter is the no-label
// Value(name) series.) Missing is treated as 0 for a rebaseline/drop counter (a
// rebaseline/drop that never happened yields no series until the first increment); the
// caller's deltas remain correct. `released` must be present once traffic flowed, so its
// absence is surfaced as a real fault rather than silently read as 0.
func r121PeerCounter(t *testing.T, exp metrics.Exposition, name, who string) float64 {
	t.Helper()
	if v, ok := exp.Value(name); ok {
		return v
	}
	if name == metrics.MetricReseqReleased {
		t.Fatalf("%s absent from the %s /metrics after traffic flowed (single-peer no-label exposition)", name, who)
	}
	return 0
}

// r121WaitReadopted asserts the RESTARTED process actually re-opened the persistent TUN,
// polling its OWN post-restart log for the 'tunnel interface up' record (cmd/wanbond/main.go).
// Under tun_persist=true the kernel link SURVIVES the stop, so a plain `ip link show`
// (top.waitLink) is VACUOUS here — it passes whether or not the new process adopted the
// device, yet round 1's message claimed 're-adopted'. device.Up emits 'tunnel interface up'
// only AFTER CreateTUN's TUNSETIFF successfully (re-)adopts wanbond0 BY NAME
// (internal/device/persist_linux.go). p.log() is the fresh post-restart buffer (startProc
// returns a NEW proc with its own output), so this record proves re-adoption by THIS process,
// not a stale persistent link left by its predecessor.
func r121WaitReadopted(t *testing.T, p *proc, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, ok := logRecordTime(p.log(), "tunnel interface up"); ok {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// requireNetAdmin skips (does NOT fail) when this process cannot build the netns fixture and
// bring the daemons up — so `go test -tags e2e` in an unprivileged environment SKIPS this
// scenario rather than failing it. Under the privileged hardware suite (sudo / the TestMain
// user+net namespace) both probes succeed and the test runs.
//
// It probes TWO independent capabilities, /dev/net/tun FIRST:
//
//   - /dev/net/tun must exist: device.Up calls CreateTUN (TUNSETIFF) and hard-fails without
//     it. TestMain's `unshare -Urmn` grants CAP_NET_ADMIN inside the userns, so the dummy-link
//     probe below SUCCEEDS there even when the sandbox has NO /dev/net/tun — in which case both
//     subtests would otherwise FAIL ~5s later at daemon bring-up ("CreateTUN(wanbond0) failed").
//     Probing the tun device up front makes an unprivileged run SKIP BEFORE any daemon comes up.
//   - CAP_NET_ADMIN to create the veth/netem topology, probed by adding+deleting a dummy link.
func requireNetAdmin(t *testing.T) {
	t.Helper()
	if _, err := os.Stat("/dev/net/tun"); err != nil {
		t.Skipf("/dev/net/tun unavailable (%v) — device.Up's CreateTUN(wanbond0) would fail; the one-sided-restart e2e requires the privileged suite; skipping", err)
	}
	const probe = "wbR121probe"
	if out, err := exec.Command("ip", "link", "add", probe, "type", "dummy").CombinedOutput(); err != nil {
		t.Skipf("no CAP_NET_ADMIN to build the netns fixture (%v: %s) — the one-sided-restart e2e requires the privileged suite; skipping",
			err, strings.TrimSpace(string(out)))
	}
	_ = exec.Command("ip", "link", "del", probe).Run()
}
