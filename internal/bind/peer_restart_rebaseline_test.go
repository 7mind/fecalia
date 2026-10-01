package bind

import (
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/frame"
)

// The two process epochs a "restart" spans: the pre-restart boot and the post-restart boot.
// Each is both the probe session id and the boot its transport announces in its hellos, as
// in production. Distinct values make the second adoption an epoch change (a new-session
// adoption over an already-adopted path), on which the transport starts the peer's stream
// over.
const (
	preRestartSession  uint64 = 0xAAAA0000AAAA0001
	postRestartSession uint64 = 0xBBBB0000BBBB0002
)

// reflectProbeIssuedChallenge drives ONE inbound PROBE (encoded under psk, carrying hello)
// through handleInbound → dispatchInbound's non-echo Probe branch — the reflector
// adopt/restart path plus the transport's hello learning — and returns the issued challenge
// carried in the reflected echo (read off the raw peer socket). Sending the reflector's own
// live challenge back on the NEXT probe is what makes it adopt (the responder-contributed-
// challenge protocol), so a caller learns the challenge here and echoes it next.
func reflectProbeIssuedChallenge(t testing.TB, m *Multipath, view *peerPathState, psk config.Key, peer *net.UDPConn, peerAP netip.AddrPort, sessionID, probeSeq, echoedChallenge uint64, hello []byte) uint64 {
	t.Helper()
	raw, err := frame.Encode(psk, frame.Probe{
		PathID:         view.id,
		ProbeSeq:       probeSeq,
		TimestampNanos: time.Now().UnixNano(),
		SessionID:      sessionID,
		Challenge:      echoedChallenge,
		Payload:        hello,
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
// peer through: first adoption (the transport starts the stream at its first datagram), a
// stream advancing the release point, an authenticated RESTART (new-session adopt carrying
// the new boot's hello → the transport re-baselines THIS peer's resequencer), a STALE
// old-boot straggler that must NOT be delivered, the restarted boot's FIRST datagram that
// must then be delivered, and a same-epoch probe that must NOT re-anchor — all while a
// second bound peer (witnessPeer) is left untouched.
//
// It documents the failing-without-the-wiring contract: with the re-baseline on a changed
// remote epoch removed, the restart leaves the release point where the old boot's stream
// advanced it, and the restarted boot's first datagram is dropped as late — the delivery
// assertion below fails (the D36 repro).
func runPeerRestartRebaselineScenario(
	t *testing.T,
	m *Multipath,
	targetView *peerPathState, targetPeer *peerState, targetPSK config.Key,
	witnessView *peerPathState, witnessPeer *peerState,
) {
	t.Helper()
	peer, peerAP := rawPeer(t)
	witnessSrc := netip.MustParseAddrPort("198.51.100.4:51820")

	rq := targetPeer.resequencer.Load()
	wrq := witnessPeer.resequencer.Load()
	if rq == nil || wrq == nil {
		t.Fatalf("resequencer not instantiated: target=%v witness=%v", rq != nil, wrq != nil)
	}
	restarts := make(chan string, 4)
	m.SetOnPeerRestart(func(name string) { restarts <- name })

	// --- First adoption (session preRestartSession): learn challenge, then adopt. The
	//     transport learns the peer's process and starts its stream. A first-ever adoption
	//     is NOT a restart. ---
	oldBoot := newRemoteTransport(t, targetPeer, preRestartSession)
	c := reflectProbeIssuedChallenge(t, m, targetView, targetPSK, peer, peerAP, preRestartSession, 0, 0, oldBoot.hello())
	_ = reflectProbeIssuedChallenge(t, m, targetView, targetPSK, peer, peerAP, preRestartSession, 1, c, oldBoot.hello())
	adopted := rq.Stats().Rebaselines

	// --- The pre-restart boot's stream advances BOTH peers' release point (via the real
	//     receive path). ---
	witness := newRemoteTransport(t, witnessPeer, preRestartSession)
	witness.join(witnessView, witnessSrc)
	witnessBaseline := wrq.Stats().Rebaselines
	for _, payload := range []string{"boot-1", "boot-2", "boot-3"} {
		m.handleInbound(targetView, oldBoot.wire(oldBoot.bulk(targetView, []byte(payload))), peerAP)
		if it, ok := rq.Pop(); !ok || string(it.Payload) != payload {
			t.Fatalf("pre-restart datagram %q not delivered: ok=%v payload=%q", payload, ok, it.Payload)
		}
		m.handleInbound(witnessView, witness.wire(witness.bulk(witnessView, []byte("witness-"+payload))), witnessSrc)
		if it, ok := wrq.Pop(); !ok || string(it.Payload) != "witness-"+payload {
			t.Fatalf("witness datagram for %q not delivered: ok=%v payload=%q", payload, ok, it.Payload)
		}
	}
	select {
	case name := <-restarts:
		t.Fatalf("first adoption of peer %q reported as a restart", name)
	default:
	}

	// --- Authenticated PEER RESTART (session postRestartSession): learn the live challenge,
	//     then adopt with it. This adoption is over an ALREADY-adopted path under a DIFFERENT
	//     session, and its hello announces a new boot → the transport re-baselines THIS peer. ---
	newBoot := newRemoteTransport(t, targetPeer, postRestartSession)
	c2 := reflectProbeIssuedChallenge(t, m, targetView, targetPSK, peer, peerAP, postRestartSession, 0, 0, newBoot.hello())
	if got := rq.Stats().Rebaselines; got != adopted {
		t.Fatalf("a restarted boot's probe WITHOUT the live challenge re-baselined: Rebaselines %d → %d", adopted, got)
	}
	_ = reflectProbeIssuedChallenge(t, m, targetView, targetPSK, peer, peerAP, postRestartSession, 1, c2, newBoot.hello())
	if got := rq.Stats().Rebaselines; got != adopted+1 {
		t.Fatalf("peer restart did NOT re-baseline the target resequencer: Rebaselines=%d, want %d "+
			"(the re-baseline on a changed remote epoch is the code under test)", got, adopted+1)
	}
	select {
	case name := <-restarts:
		if name != targetPeer.name {
			t.Fatalf("restart reported for peer %q, want %q", name, targetPeer.name)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("restart of a known peer not reported")
	}

	// --- STALE RACE: an OLD-boot straggler still draining from carrier/modem queues lands
	//     BEFORE the new boot's first datagram. It must be dropped and must NOT move the
	//     release point (which would block recovery). ---
	m.handleInbound(targetView, oldBoot.wire(oldBoot.bulk(targetView, []byte("stale-straggler"))), peerAP)
	if it, ok := rq.Pop(); ok {
		t.Fatalf("stale old-boot straggler was DELIVERED (%q) (D36 race not closed)", it.Payload)
	}
	if got := rq.Buffered(); got != 0 {
		t.Fatalf("stale old-boot straggler was buffered: Buffered=%d, want 0", got)
	}

	// --- The restarted stream's FIRST datagram now admits: the release point is back at
	//     the start of a stream, so it DELIVERS, and it must NOT count as a suspect drop. ---
	suspectBeforeLow := rq.Stats().DroppedSuspect
	m.handleInbound(targetView, newBoot.wire(newBoot.bulk(targetView, []byte("wrapped-wg-init"))), peerAP)
	it, ok := rq.Pop()
	if !ok || string(it.Payload) != "wrapped-wg-init" {
		t.Fatalf("restarted boot's first datagram NOT delivered after restart re-anchor: ok=%v payload=%q", ok, it.Payload)
	}
	if got := rq.Stats().DroppedSuspect; got != suspectBeforeLow {
		t.Fatalf("the restarted boot's first datagram was counted as a suspect drop: DroppedSuspect %d → %d", suspectBeforeLow, got)
	}

	// --- A SAME-epoch (non-restart) probe must NOT re-anchor: a within-session probe
	//     repeats the same hello, so the transport leaves the release point alone. ---
	rebBefore := rq.Stats().Rebaselines
	_ = reflectProbeIssuedChallenge(t, m, targetView, targetPSK, peer, peerAP, postRestartSession, 2, 0, newBoot.hello())
	if got := rq.Stats().Rebaselines; got != rebBefore {
		t.Fatalf("a same-epoch (non-restart) probe re-baselined: Rebaselines %d → %d", rebBefore, got)
	}
	m.handleInbound(targetView, newBoot.wire(newBoot.bulk(targetView, []byte("second"))), peerAP)
	if it, ok := rq.Pop(); !ok || string(it.Payload) != "second" {
		t.Fatalf("restarted stream did not continue after a same-epoch probe: ok=%v payload=%q", ok, it.Payload)
	}

	// --- The other bound peer's resequencer is UNDISTURBED by the target's restart: it was
	//     not re-baselined, and its stream continues at its own release point. ---
	if got := wrq.Stats().Rebaselines; got != witnessBaseline {
		t.Fatalf("witness peer re-baselined by the target's restart: Rebaselines %d → %d", witnessBaseline, got)
	}
	m.handleInbound(witnessView, witness.wire(witness.bulk(witnessView, []byte("witness-next"))), witnessSrc)
	if it, ok := wrq.Pop(); !ok || string(it.Payload) != "witness-next" {
		t.Fatalf("witness stream did not continue (its release point was disturbed by the target's restart): ok=%v payload=%q", ok, it.Payload)
	}
	select {
	case name := <-restarts:
		t.Fatalf("a second restart was reported for peer %q", name)
	default:
	}
}

// TestPeerRestartRebaselinesPrimaryResequencer restarts the PRIMARY (edge single-concentrator)
// peer and asserts the restarted stream re-anchors while a second bound peer is untouched.
func TestPeerRestartRebaselinesPrimaryResequencer(t *testing.T) {
	pskA := testKey(t, 0x11)
	pskB := testKey(t, 0x22)
	m, beta := newConcentratorPairForRestart(t, pskA, pskB)

	primary := m.peerState
	runPeerRestartRebaselineScenario(t, m,
		m.paths[0], primary, pskA,
		peerPathByName(beta, "a"), beta)
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
		m.paths[0], primary)
}

// newConcentratorPairForRestart builds an Open 2-peer concentrator over one shared socket: the
// primary keyed on pskA and a beta peer registered via AddConcentratorPeer keyed on pskB, with
// beta's heavy receive datapath (its resequencer) instantiated so a test can drive data at it
// directly rather than waiting for the demux to lazily bind its first PROBE.
func newConcentratorPairForRestart(t *testing.T, pskA, pskB config.Key) (*Multipath, *peerState) {
	t.Helper()
	clk := newFakeClock()
	paths := loopbackPaths(1) // one shared socket, path "a"
	m, _ := newProbingMultipath(t, paths, pskA, clk)

	betaProbers, betaFactory := concPeerWiring(t, paths, pskB, 0x0BEEF, clk)
	if err := m.AddConcentratorPeer("beta", pskB, betaProbers, betaFactory); err != nil {
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
