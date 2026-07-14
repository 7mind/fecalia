package bind

import (
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/frame"
)

// The two sessionIDs a "restart" spans: the pre-restart boot and the post-restart boot.
// Distinct values make the second adoption an epoch change (a new-sessionID adoption over
// an already-adopted path), which is exactly what the reflector reports as epochChanged.
const (
	preRestartSession  uint64 = 0xAAAA0000AAAA0001
	postRestartSession uint64 = 0xBBBB0000BBBB0002
	// restartHighSeq is a release point far past resequencerWindow (2048): the pre-restart
	// boot's busy stream advanced `next` here, so the restarted boot's low outer-seq init is
	// >1 window below it — the SUSPECT region that, before T119, blackholed the wrapped init.
	restartHighSeq uint64 = 9000
)

// deliverDATA encodes one DATA frame under psk and pushes it through the FULL receive path
// (handleInbound → dispatchInbound's DATA branch → the peer's resequencer), exactly as a
// wire datagram would arrive on view. It exercises the per-peer routing rather than poking
// the resequencer directly, so the assertions read the resequencer the wiring actually feeds.
func deliverDATA(t testing.TB, m *Multipath, view *peerPathState, psk config.Key, seq uint64, payload []byte, src netip.AddrPort) {
	t.Helper()
	codec, err := frame.NewCodec(psk)
	if err != nil {
		t.Fatalf("build data codec: %v", err)
	}
	raw, err := codec.Encode(nil, frame.Data{OuterSeq: seq, PathID: view.id, Payload: payload})
	if err != nil {
		t.Fatalf("encode data seq %d: %v", seq, err)
	}
	m.handleInbound(view, raw, src)
}

// reflectProbeIssuedChallenge drives ONE inbound PROBE (encoded under psk) through
// handleInbound → dispatchInbound's non-echo Probe branch — the reflector adopt/restart
// path plus the T119 epochChanged→RebaselineToLow wiring — and returns the issued challenge
// carried in the reflected echo (read off the raw peer socket). Sending the reflector's own
// live challenge back on the NEXT probe is what makes it adopt (the responder-contributed-
// challenge protocol), so a caller learns the challenge here and echoes it next.
func reflectProbeIssuedChallenge(t testing.TB, m *Multipath, view *peerPathState, psk config.Key, peer *net.UDPConn, peerAP netip.AddrPort, sessionID, probeSeq, echoedChallenge uint64) uint64 {
	t.Helper()
	raw, err := frame.Encode(psk, frame.Probe{
		PathID:         view.id,
		ProbeSeq:       probeSeq,
		TimestampNanos: time.Now().UnixNano(),
		SessionID:      sessionID,
		Challenge:      echoedChallenge,
	})
	if err != nil {
		t.Fatalf("encode probe (session %#x seq %d): %v", sessionID, probeSeq, err)
	}
	m.handleInbound(view, raw, peerAP)
	codec, err := frame.NewCodec(psk)
	if err != nil {
		t.Fatalf("build echo codec: %v", err)
	}
	echo := readProbe(t, peer, codec)
	return echo.Challenge
}

// runPeerRestartRebaselineScenario is the shared body of the T119 acceptance. It drives one
// peer through: bootstrap adoption (no re-baseline), a busy stream advancing `next` far past
// one window, an authenticated RESTART (new-sessionID adopt → epochChanged=true → the wiring
// low-anchor re-baselines THIS peer's resequencer), a STALE-HIGH old-boot straggler that must
// NOT re-pin `next` high, the wrapped LOW-seq init that must then admit, and a same-epoch probe
// that must NOT re-anchor — all while a second bound peer (witnessPeer) is left untouched.
//
// It documents the failing-without-the-wiring contract: with the epochChanged→RebaselineToLow
// call removed, the restart leaves `next` pinned high, Rebaselines stays 0, and the wrapped
// low-seq init is SUSPECT-dropped — the delivery assertion below fails (the D36 repro).
func runPeerRestartRebaselineScenario(
	t *testing.T,
	m *Multipath,
	targetView *peerPathState, targetPeer *peerState, targetPSK config.Key,
	witnessView *peerPathState, witnessPeer *peerState, witnessPSK config.Key,
) {
	t.Helper()
	peer, peerAP := rawPeer(t)

	rq := targetPeer.resequencer.Load()
	wrq := witnessPeer.resequencer.Load()
	if rq == nil || wrq == nil {
		t.Fatalf("resequencer not instantiated: target=%v witness=%v", rq != nil, wrq != nil)
	}

	dataSrc := netip.MustParseAddrPort("203.0.113.9:51820")
	lowSrc := netip.MustParseAddrPort("198.51.100.4:51820")

	// --- Bootstrap adoption (session preRestartSession): learn challenge, then adopt. A
	//     first-ever adoption is NOT a restart, so it must NOT re-baseline. ---
	c := reflectProbeIssuedChallenge(t, m, targetView, targetPSK, peer, peerAP, preRestartSession, 0, 0)
	_ = reflectProbeIssuedChallenge(t, m, targetView, targetPSK, peer, peerAP, preRestartSession, 1, c)
	if got := rq.Stats().Rebaselines; got != 0 {
		t.Fatalf("bootstrap adoption re-baselined the resequencer: Rebaselines=%d, want 0", got)
	}

	// --- The pre-restart boot's busy stream advances BOTH peers' release point far past one
	//     window (via the real DATA receive path). ---
	deliverDATA(t, m, targetView, targetPSK, restartHighSeq, []byte("boot-high"), dataSrc)
	if _, ok := rq.Pop(); !ok {
		t.Fatalf("high boot seq %d not delivered — release point did not advance", restartHighSeq)
	}
	deliverDATA(t, m, witnessView, witnessPSK, restartHighSeq, []byte("witness-high"), dataSrc)
	if _, ok := wrq.Pop(); !ok {
		t.Fatalf("witness high boot seq not delivered — its release point did not advance")
	}

	// --- Authenticated PEER RESTART (session postRestartSession): learn the live challenge,
	//     then adopt with it. This adoption is over an ALREADY-adopted path under a DIFFERENT
	//     session → epochChanged=true → the T119 wiring low-anchor re-baselines THIS peer. ---
	c2 := reflectProbeIssuedChallenge(t, m, targetView, targetPSK, peer, peerAP, postRestartSession, 0, 0)
	_ = reflectProbeIssuedChallenge(t, m, targetView, targetPSK, peer, peerAP, postRestartSession, 1, c2)
	if got := rq.Stats().Rebaselines; got != 1 {
		t.Fatalf("peer restart did NOT re-baseline the target resequencer: Rebaselines=%d, want 1 "+
			"(the epochChanged→RebaselineToLow wiring is the code under test)", got)
	}

	// --- STALE-HIGH RACE: a stale OLD-boot high-seq straggler still draining from carrier/modem
	//     queues lands BEFORE the low init. It must be SUSPECT-dropped and must NOT re-pin `next`
	//     high (which a plain unpin-and-trust-next re-baseline would allow, blocking recovery). ---
	beforeSuspect := rq.Stats().DroppedSuspect
	deliverDATA(t, m, targetView, targetPSK, restartHighSeq+50, []byte("stale-high-straggler"), dataSrc)
	if it, ok := rq.Pop(); ok {
		t.Fatalf("stale-high straggler was DELIVERED (%q) — it re-pinned next high (D36 race not closed)", it.Payload)
	}
	if rq.Stats().DroppedSuspect <= beforeSuspect {
		t.Fatalf("stale-high straggler not SUSPECT-dropped: DroppedSuspect stayed %d", beforeSuspect)
	}

	// --- The wrapped WG init (restarted stream, outer-seq ~1) now admits: it is >1 window below
	//     the pre-rebaseline release point, so it re-anchors and DELIVERS, and it must NOT itself
	//     count as a suspect drop. ---
	suspectBeforeLow := rq.Stats().DroppedSuspect
	deliverDATA(t, m, targetView, targetPSK, 1, []byte("wrapped-wg-init"), lowSrc)
	it, ok := rq.Pop()
	if !ok || string(it.Payload) != "wrapped-wg-init" {
		t.Fatalf("wrapped low-seq init NOT delivered after restart re-anchor: ok=%v payload=%q", ok, it.Payload)
	}
	if got := rq.Stats().DroppedSuspect; got != suspectBeforeLow {
		t.Fatalf("the low-seq init was counted as a suspect drop: DroppedSuspect %d → %d", suspectBeforeLow, got)
	}

	// --- A SAME-epoch (non-restart) probe must NOT re-anchor: a within-session probe reports
	//     epochChanged=false, so the wiring leaves the release point alone. ---
	rebBefore := rq.Stats().Rebaselines
	_ = reflectProbeIssuedChallenge(t, m, targetView, targetPSK, peer, peerAP, postRestartSession, 2, 0)
	if got := rq.Stats().Rebaselines; got != rebBefore {
		t.Fatalf("a same-epoch (non-restart) probe re-baselined: Rebaselines %d → %d", rebBefore, got)
	}

	// --- The other bound peer's resequencer is UNDISTURBED by the target's restart. ---
	if got := wrq.Stats().Rebaselines; got != 0 {
		t.Fatalf("witness peer re-baselined by the target's restart: Rebaselines=%d, want 0", got)
	}
	wSuspectBefore := wrq.Stats().DroppedSuspect
	deliverDATA(t, m, witnessView, witnessPSK, 1, []byte("witness-should-drop"), lowSrc)
	if it, ok := wrq.Pop(); ok {
		t.Fatalf("witness delivered a low seq (%q) — its release point was disturbed by the target's restart", it.Payload)
	}
	if got := wrq.Stats().DroppedSuspect; got <= wSuspectBefore {
		t.Fatalf("witness did not reject the low seq as suspect (its next moved): DroppedSuspect stayed %d", wSuspectBefore)
	}
}

// TestPeerRestartRebaselinesPrimaryResequencer restarts the PRIMARY (edge single-concentrator)
// peer and asserts the wrapped low-seq init re-anchors while a second bound peer is untouched.
func TestPeerRestartRebaselinesPrimaryResequencer(t *testing.T) {
	pskA := testKey(t, 0x11)
	pskB := testKey(t, 0x22)
	m, beta := newConcentratorPairForRestart(t, pskA, pskB)

	primary := m.peerState
	runPeerRestartRebaselineScenario(t, m,
		m.paths[0], primary, pskA,
		peerPathByName(beta, "a"), beta, pskB)
}

// TestPeerRestartRebaselinesConcentratorPeerResequencer restarts an AddConcentratorPeer peer
// and asserts the SAME single wiring site re-anchors its per-peer resequencer, leaving the
// primary untouched — proving the demux-resolved per-peer view covers concentrator peers too.
func TestPeerRestartRebaselinesConcentratorPeerResequencer(t *testing.T) {
	pskA := testKey(t, 0x33)
	pskB := testKey(t, 0x44)
	m, beta := newConcentratorPairForRestart(t, pskA, pskB)

	primary := m.peerState
	runPeerRestartRebaselineScenario(t, m,
		peerPathByName(beta, "a"), beta, pskB,
		m.paths[0], primary, pskA)
}

// newConcentratorPairForRestart builds an Open 2-peer concentrator over one shared socket: the
// primary keyed on pskA and a beta peer registered via AddConcentratorPeer keyed on pskB, with
// beta's heavy receive datapath (its resequencer) instantiated so a test can drive DATA at it
// directly rather than waiting for the demux to lazily bind its first PROBE.
func newConcentratorPairForRestart(t *testing.T, pskA, pskB config.Key) (*Multipath, *peerState) {
	t.Helper()
	clk := newFakeClock()
	paths := loopbackPaths(1) // one shared socket, path "a"
	m, _, _ := newProbingMultipath(t, paths, pskA, clk)

	betaSched, betaProbers, betaFactory := concPeerWiring(t, paths, pskB, 0x0BEEF, clk)
	if err := m.AddConcentratorPeer("beta", pskB, betaSched, betaProbers, betaFactory); err != nil {
		t.Fatalf("AddConcentratorPeer: %v", err)
	}
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	beta := m.peersByName["beta"]
	if beta == nil {
		t.Fatal("beta peer not registered")
	}
	// A concentrator peer's resequencer is lazily built on its first bound PROBE; instantiate it
	// eagerly here so the test drives its receive plane deterministically.
	m.ensurePeerReceiveInstantiated(beta)
	return m, beta
}
