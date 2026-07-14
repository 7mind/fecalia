package telemetry

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/frame"
)

func proberCfg() ProberConfig {
	return ProberConfig{
		LossWindow: 0,
		Liveness:   LivenessConfig{DownAfter: 3 * time.Second, UpAfterSuccesses: 3},
	}
}

func newTestProber(t *testing.T, psk config.Key, clk Clock) *Prober {
	t.Helper()
	return NewProber("starlink", 1, testSessionID, psk, proberCfg(), clk, discardLogger(t))
}

// encodeProbe is a test helper minting one authenticated probe/echo frame with an
// explicit echoed Challenge (the responder-contributed freshness token, T38).
func encodeProbe(t *testing.T, psk config.Key, pathID uint8, sessionID, seq uint64, isEcho bool, challenge uint64) []byte {
	t.Helper()
	raw, err := frame.Encode(psk, frame.Probe{
		PathID:    pathID,
		ProbeSeq:  seq,
		IsEcho:    isEcho,
		SessionID: sessionID,
		Challenge: challenge,
	})
	if err != nil {
		t.Fatalf("encode probe session=%#x seq=%d: %v", sessionID, seq, err)
	}
	return raw
}

// echoChallenge decodes an echo and returns the responder's issued Challenge it
// carries (what a peer must echo back to be adopted).
func echoChallenge(t *testing.T, psk config.Key, echo []byte) uint64 {
	t.Helper()
	f, err := frame.Decode(psk, echo)
	if err != nil {
		t.Fatalf("decode echo: %v", err)
	}
	probe, ok := f.(frame.Probe)
	if !ok {
		t.Fatalf("echo is %T, want frame.Probe", f)
	}
	return probe.Challenge
}

// adoptSession drives the two-round challenge handshake so r adopts sessionID on
// pathID starting at ProbeSeq startSeq: round 1 (challenge 0) is reflected and
// teaches us the live issued challenge; round 2 carries that challenge at
// startSeq+1 and is adopted (resetting the high-water, which then sits at
// startSeq+1). It returns the next unused ProbeSeq (startSeq+2).
func adoptSession(t *testing.T, r *Reflector, psk config.Key, pathID uint8, sessionID, startSeq uint64) uint64 {
	t.Helper()
	echo, _, err := r.Reflect(encodeProbe(t, psk, pathID, sessionID, startSeq, false, 0))
	if err != nil {
		t.Fatalf("bootstrap probe rejected: %v", err)
	}
	ch := echoChallenge(t, psk, echo)
	if _, _, err := r.Reflect(encodeProbe(t, psk, pathID, sessionID, startSeq+1, false, ch)); err != nil {
		t.Fatalf("adoption probe (challenge echoed) rejected: %v", err)
	}
	return startSeq + 2
}

// TestProbeEchoRTT asserts a full send -> reflect -> handle round-trip yields the
// elapsed round-trip time as the first RTT sample (which seeds srtt exactly).
func TestProbeEchoRTT(t *testing.T) {
	psk := testPSK(t, 0x5A)
	clk := newFakeClock()
	p := newTestProber(t, psk, clk)
	r := NewReflector(psk, newTestRand())

	raw, err := p.SendProbe()
	if err != nil {
		t.Fatalf("send probe: %v", err)
	}
	echo, _, err := r.Reflect(raw)
	if err != nil {
		t.Fatalf("reflect: %v", err)
	}
	const rtt = 37 * time.Millisecond
	clk.advance(rtt)
	if err := p.HandleEcho(echo); err != nil {
		t.Fatalf("handle echo: %v", err)
	}
	if got := p.Estimate().RTT; got != rtt {
		t.Fatalf("measured RTT = %v, want %v", got, rtt)
	}
}

// TestForgedProbeRejected asserts a tampered echo fails the PSK HMAC (frame.ErrAuth)
// and that a rejected echo leaves estimator and liveness state untouched — the
// estimator only accepts probes that Decode successfully.
func TestForgedProbeRejected(t *testing.T) {
	psk := testPSK(t, 0x5A)
	clk := newFakeClock()
	p := newTestProber(t, psk, clk)
	r := NewReflector(psk, newTestRand())

	raw, err := p.SendProbe()
	if err != nil {
		t.Fatalf("send probe: %v", err)
	}
	echo, _, err := r.Reflect(raw)
	if err != nil {
		t.Fatalf("reflect: %v", err)
	}
	// Flip a byte in the MAC-covered region (the last byte is inside the tag).
	tampered := append([]byte(nil), echo...)
	tampered[len(tampered)-1] ^= 0x01

	if err := p.HandleEcho(tampered); !errors.Is(err, frame.ErrAuth) {
		t.Fatalf("handle tampered echo: got %v, want frame.ErrAuth", err)
	}
	if got := p.Estimate().RTT; got != 0 {
		t.Fatalf("rejected echo mutated RTT estimate: %v", got)
	}
	if p.State() != StateDown {
		t.Fatalf("rejected echo advanced liveness: state = %v", p.State())
	}
	// The high-water mark must not have advanced: a subsequent genuine echo of the
	// same ProbeSeq is still accepted.
	if err := p.HandleEcho(echo); err != nil {
		t.Fatalf("genuine echo after tampered one rejected: %v", err)
	}
}

// TestWrongPSKProbeRejected asserts an echo authenticated under a different PSK
// is rejected (the reflector reflects nothing, and direct decode fails).
func TestWrongPSKProbeRejected(t *testing.T) {
	pskA := testPSK(t, 0x11)
	pskB := testPSK(t, 0x22)
	clk := newFakeClock()
	p := newTestProber(t, pskA, clk)

	// A probe/echo minted under pskB must not authenticate under pskA.
	foreign, err := frame.Encode(pskB, frame.Probe{PathID: 1, ProbeSeq: 0, TimestampNanos: clk.Now().UnixNano()})
	if err != nil {
		t.Fatalf("encode foreign probe: %v", err)
	}
	if err := p.HandleEcho(foreign); err == nil {
		t.Fatal("echo under wrong PSK accepted")
	}
}

// TestProbeReplayRejected asserts the within-session anti-replay high-water mark
// rejects a replayed/stale ProbeSeq on both the reflector (once a session is
// adopted) and the prober — defect D4.
func TestProbeReplayRejected(t *testing.T) {
	psk := testPSK(t, 0x5A)

	// Reflector side: adopt a session, then a replayed within-session ProbeSeq is
	// rejected as ErrReplay.
	r := NewReflector(psk, newTestRand())
	const session = uint64(0x9999_0000_9999_0000)
	next := adoptSession(t, r, psk, 1, session, 0) // high-water at 1
	if _, _, err := r.Reflect(encodeProbe(t, psk, 1, session, next, false, 0)); err != nil {
		t.Fatalf("fresh within-session probe rejected: %v", err)
	}
	if _, _, err := r.Reflect(encodeProbe(t, psk, 1, session, next, false, 0)); !errors.Is(err, ErrReplay) {
		t.Fatalf("reflector accepted replayed within-session probe: %v", err)
	}

	// Prober side: a genuine echo is accepted once, its replay rejected.
	clk := newFakeClock()
	p := newTestProber(t, psk, clk)
	raw, err := p.SendProbe()
	if err != nil {
		t.Fatalf("send probe: %v", err)
	}
	echo, _, err := NewReflector(psk, newTestRand()).Reflect(raw)
	if err != nil {
		t.Fatalf("reflect: %v", err)
	}
	if err := p.HandleEcho(echo); err != nil {
		t.Fatalf("first echo: %v", err)
	}
	if err := p.HandleEcho(echo); !errors.Is(err, ErrReplay) {
		t.Fatalf("prober accepted replayed echo: %v", err)
	}
}

// TestAntiReplayMonotonic is a focused check on the high-water filter: strictly
// increasing seqs are accepted; equal or lower seqs are rejected.
func TestAntiReplayMonotonic(t *testing.T) {
	var a AntiReplay
	for _, seq := range []uint64{0, 1, 2, 5} {
		if !a.Accept(seq) {
			t.Fatalf("fresh seq %d rejected", seq)
		}
	}
	for _, seq := range []uint64{5, 4, 0, 3} {
		if a.Accept(seq) {
			t.Fatalf("stale/replayed seq %d accepted", seq)
		}
	}
	if hw, ok := a.HighWater(); !ok || hw != 5 {
		t.Fatalf("high-water = (%d,%v), want (5,true)", hw, ok)
	}
}

// TestNonProbeFrameRejected asserts a non-probe authenticated frame is rejected
// by both the reflector and the prober.
func TestNonProbeFrameRejected(t *testing.T) {
	psk := testPSK(t, 0x5A)
	clk := newFakeClock()
	p := newTestProber(t, psk, clk)
	r := NewReflector(psk, newTestRand())

	ctrl, err := frame.Encode(psk, frame.Control{ControlType: 1, Payload: []byte("x")})
	if err != nil {
		t.Fatalf("encode control: %v", err)
	}
	if _, _, err := r.Reflect(ctrl); err == nil {
		t.Fatal("reflector accepted a control frame")
	}
	if err := p.HandleEcho(ctrl); err == nil {
		t.Fatal("prober accepted a control frame as an echo")
	}
}

// TestReflectorPerPathAntiReplay asserts a single Reflector serving multiple paths
// keys its within-session anti-replay by PathID: each path's ProbeSeq space is
// independent (both start at 0), so path B's probe is not rejected as a replay of
// path A's, while a genuine per-path replay still is. Each path is adopted through
// its own challenge handshake first (the within-session guard only exists once a
// session is adopted).
func TestReflectorPerPathAntiReplay(t *testing.T) {
	psk := testPSK(t, 0x5A)
	r := NewReflector(psk, newTestRand())
	const session = uint64(0x7777_7777_7777_7777)

	nextA := adoptSession(t, r, psk, 1, session, 0)
	nextB := adoptSession(t, r, psk, 2, session, 0)

	// Path A fresh probe accepted.
	if _, _, err := r.Reflect(encodeProbe(t, psk, 1, session, nextA, false, 0)); err != nil {
		t.Fatalf("path A fresh probe: %v", err)
	}
	// Path B has an independent seq space: its own next is accepted, not seen as an
	// A replay.
	if _, _, err := r.Reflect(encodeProbe(t, psk, 2, session, nextB, false, 0)); err != nil {
		t.Fatalf("path B independent seq rejected as a cross-path replay: %v", err)
	}
	// A genuine per-path replay (path A seq nextA again) is rejected.
	if _, _, err := r.Reflect(encodeProbe(t, psk, 1, session, nextA, false, 0)); !errors.Is(err, ErrReplay) {
		t.Fatalf("path A replay: got %v, want ErrReplay", err)
	}
}

// TestReflectorSessionEpochResetOnPeerRestart is the core D12 fix: WITHIN a session
// strict-monotonic replay rejection is preserved (D4), but a peer restart (a NEW
// SessionID with seq-from-0) recovers via the challenge handshake — its bootstrap
// probe is reflected (teaching it the challenge) and its next probe, echoing that
// challenge, is ADOPTED, resetting the per-path high-water so the seq-from-0 stream
// is accepted. Without this the responder would reject every fresh probe until the
// counter organically passed the prior high-water (the liveness deadlock).
func TestReflectorSessionEpochResetOnPeerRestart(t *testing.T) {
	psk := testPSK(t, 0x5A)
	r := NewReflector(psk, newTestRand())
	const (
		pathID = uint8(1)
		s1     = uint64(0x1111_1111_1111_1111)
		s2     = uint64(0x2222_2222_2222_2222)
	)

	// Session 1 adopted, then advances.
	next := adoptSession(t, r, psk, pathID, s1, 0) // high-water at 1, next == 2
	for seq := next; seq <= next+1; seq++ {
		if _, _, err := r.Reflect(encodeProbe(t, psk, pathID, s1, seq, false, 0)); err != nil {
			t.Fatalf("session 1 seq %d: %v", seq, err)
		}
	}
	// D4 preserved WITHIN the session: a replayed/stale seq is rejected.
	if _, _, err := r.Reflect(encodeProbe(t, psk, pathID, s1, next, false, 0)); !errors.Is(err, ErrReplay) {
		t.Fatalf("within-session replay: got %v, want ErrReplay", err)
	}

	// Peer restart: a NEW SessionID with seq from 0. Its bootstrap probe (challenge
	// 0) is NOT adopted but IS reflected, teaching it the live challenge; its next
	// probe echoes that challenge and IS adopted, resetting the high-water (D12).
	echo, _, err := r.Reflect(encodeProbe(t, psk, pathID, s2, 0, false, 0))
	if err != nil {
		t.Fatalf("restart bootstrap probe rejected: %v", err)
	}
	ch := echoChallenge(t, psk, echo)
	if _, _, err := r.Reflect(encodeProbe(t, psk, pathID, s2, 1, false, ch)); err != nil {
		t.Fatalf("restart adoption probe rejected (D12 deadlock): %v", err)
	}
	// The new session then enforces its own within-session monotonicity.
	if _, _, err := r.Reflect(encodeProbe(t, psk, pathID, s2, 1, false, 0)); !errors.Is(err, ErrReplay) {
		t.Fatalf("session 2 replay: got %v, want ErrReplay", err)
	}
}

// TestReflectorReplayCannotSeizeSession is the core redesign security assertion —
// the exact round-1 reproduction that the peer-chosen-random-SessionID design
// failed. A replayed/injected probe bearing a SessionID the responder never adopted
// and a WRONG (stale or zero) challenge can NEVER authorize an epoch reset, so it
// cannot retire the legit peer's live session or roll its high-water back. The
// injected probe is reflected 1:1 (harmless), never seizing.
func TestReflectorReplayCannotSeizeSession(t *testing.T) {
	psk := testPSK(t, 0x5A)
	r := NewReflector(psk, newTestRand())
	const (
		pathID = uint8(1)
		sLegit = uint64(0xAAAA_AAAA_AAAA_AAAA)
		sEvil  = uint64(0xBBBB_BBBB_BBBB_BBBB)
	)

	// The legit peer adopts its session and advances its high-water.
	next := adoptSession(t, r, psk, pathID, sLegit, 0) // high-water at 1
	for _, seq := range []uint64{next, next + 1} {
		if _, _, err := r.Reflect(encodeProbe(t, psk, pathID, sLegit, seq, false, 0)); err != nil {
			t.Fatalf("legit seq %d: %v", seq, err)
		}
	}
	highWater := next + 1 // 3

	// Attack: inject probes with a never-adopted SessionID and WRONG challenges, at
	// both low and high seqs. None may adopt/reset — each is merely reflected.
	for _, atk := range []struct{ seq, ch uint64 }{
		{0, 0}, {0, 0xDEAD}, {highWater + 100, 0}, {highWater + 100, 0x1234},
	} {
		if _, _, err := r.Reflect(encodeProbe(t, psk, pathID, sEvil, atk.seq, false, atk.ch)); err != nil {
			t.Fatalf("injected probe seq=%d ch=%#x unexpectedly errored: %v", atk.seq, atk.ch, err)
		}
	}

	// The legit session was NOT seized: its next in-order probe is still accepted...
	if _, _, err := r.Reflect(encodeProbe(t, psk, pathID, sLegit, highWater+1, false, 0)); err != nil {
		t.Fatalf("legit session was seized/retired by the replay: %v", err)
	}
	// ...and its high-water was NOT rolled back: replaying an already-seen seq is
	// still rejected.
	if _, _, err := r.Reflect(encodeProbe(t, psk, pathID, sLegit, highWater, false, 0)); !errors.Is(err, ErrReplay) {
		t.Fatalf("legit high-water rolled back by the injection: got %v, want ErrReplay", err)
	}
}

// TestReflectorAdoptionProbeCannotReadopt asserts the issued challenge rotates on
// every adoption, so a captured adoption probe (which carried the pre-rotation
// challenge) can never be replayed later to re-adopt a superseded session.
func TestReflectorAdoptionProbeCannotReadopt(t *testing.T) {
	psk := testPSK(t, 0x5A)
	r := NewReflector(psk, newTestRand())
	const (
		pathID = uint8(1)
		s1     = uint64(0x0505_0505_0505_0505)
		s2     = uint64(0x0606_0606_0606_0606)
	)

	// Bootstrap + adopt s1; capture its adoption probe (carrying the then-live ch1).
	echo, _, err := r.Reflect(encodeProbe(t, psk, pathID, s1, 0, false, 0))
	if err != nil {
		t.Fatalf("s1 bootstrap: %v", err)
	}
	ch1 := echoChallenge(t, psk, echo)
	adoptProbe := encodeProbe(t, psk, pathID, s1, 1, false, ch1)
	if _, _, err := r.Reflect(adoptProbe); err != nil {
		t.Fatalf("adopt s1: %v", err)
	}

	// Peer restarts to s2 and adopts (the challenge has rotated since s1's adoption).
	echo2, _, err := r.Reflect(encodeProbe(t, psk, pathID, s2, 0, false, 0))
	if err != nil {
		t.Fatalf("s2 bootstrap: %v", err)
	}
	ch2 := echoChallenge(t, psk, echo2)
	if _, _, err := r.Reflect(encodeProbe(t, psk, pathID, s2, 1, false, ch2)); err != nil {
		t.Fatalf("adopt s2: %v", err)
	}

	// Replay the captured s1 adoption probe: ch1 is now stale, so it must NOT
	// re-adopt s1 (merely reflected).
	if _, _, err := r.Reflect(adoptProbe); err != nil {
		t.Fatalf("replayed adoption probe errored unexpectedly: %v", err)
	}
	// s2 remains the live session: its in-order probe is still accepted.
	if _, _, err := r.Reflect(encodeProbe(t, psk, pathID, s2, 2, false, 0)); err != nil {
		t.Fatalf("s2 was re-seized by the replayed s1 adoption probe: %v", err)
	}
}

// TestReflectorSessionPerPathIndependent asserts per-path state is independent: a
// new session on one path never resets or retires another path's session.
func TestReflectorSessionPerPathIndependent(t *testing.T) {
	psk := testPSK(t, 0x5A)
	r := NewReflector(psk, newTestRand())
	const s1 = uint64(0x0101_0101_0101_0101)

	// Adopt s1 on path 1 with its high-water advanced to 5.
	adoptSession(t, r, psk, 1, s1, 4) // adoption probes at seq 4,5 -> high-water 5
	// The same session id on a DIFFERENT path is independent: adopt it there too.
	adoptSession(t, r, psk, 2, s1, 0)
	// Path 1's high-water is untouched by path 2: replaying path 1 seq 5 rejected.
	if _, _, err := r.Reflect(encodeProbe(t, psk, 1, s1, 5, false, 0)); !errors.Is(err, ErrReplay) {
		t.Fatalf("path 1 replay after path 2 activity: got %v, want ErrReplay", err)
	}
}

// TestProberRejectsForeignSessionEcho asserts a Prober rejects an echo stamped with
// a SessionID other than its own (a stale cross-boot echo or a replay of one),
// leaving liveness and the RTT estimate untouched, while still accepting a genuine
// same-session echo.
func TestProberRejectsForeignSessionEcho(t *testing.T) {
	psk := testPSK(t, 0x5A)
	clk := newFakeClock()
	p := newTestProber(t, psk, clk) // pathID 1, session testSessionID

	// A well-formed, authenticated echo for this path but a DIFFERENT session.
	foreign := encodeProbe(t, psk, 1, testSessionID^0xFFFF, 0, true, 0)
	if err := p.HandleEcho(foreign); !errors.Is(err, ErrSessionMismatch) {
		t.Fatalf("foreign-session echo: got %v, want ErrSessionMismatch", err)
	}
	if p.State() != StateDown {
		t.Fatalf("foreign-session echo advanced liveness: state = %v", p.State())
	}
	if got := p.Estimate().RTT; got != 0 {
		t.Fatalf("foreign-session echo mutated RTT estimate: %v", got)
	}

	// A genuine echo of this Prober's own probe (same session) is still accepted.
	raw, err := p.SendProbe()
	if err != nil {
		t.Fatalf("send probe: %v", err)
	}
	echo, _, err := NewReflector(psk, newTestRand()).Reflect(raw)
	if err != nil {
		t.Fatalf("reflect: %v", err)
	}
	if err := p.HandleEcho(echo); err != nil {
		t.Fatalf("genuine same-session echo rejected: %v", err)
	}
}

// TestProberLearnsAndEchoesChallenge asserts the Prober records the responder's
// issued challenge from a fresh echo and stamps it into its subsequent probes, so a
// restarted originator's second probe carries the live challenge and is adopted.
func TestProberLearnsAndEchoesChallenge(t *testing.T) {
	psk := testPSK(t, 0x5A)
	clk := newFakeClock()
	p := newTestProber(t, psk, clk)
	r := NewReflector(psk, newTestRand())

	// Bootstrap probe carries challenge 0 (nothing learned yet).
	raw0, err := p.SendProbe()
	if err != nil {
		t.Fatalf("send probe 0: %v", err)
	}
	if got := echoChallenge(t, psk, raw0); got != 0 {
		t.Fatalf("bootstrap probe challenge = %#x, want 0", got)
	}
	echo0, _, err := r.Reflect(raw0)
	if err != nil {
		t.Fatalf("reflect 0: %v", err)
	}
	issued := echoChallenge(t, psk, echo0)
	if issued == 0 {
		t.Fatal("responder issued a zero challenge")
	}
	if err := p.HandleEcho(echo0); err != nil {
		t.Fatalf("handle echo 0: %v", err)
	}

	// The next probe must now carry the learned challenge, and the responder adopts
	// this session on it.
	raw1, err := p.SendProbe()
	if err != nil {
		t.Fatalf("send probe 1: %v", err)
	}
	if got := echoChallenge(t, psk, raw1); got != issued {
		t.Fatalf("second probe challenge = %#x, want learned %#x", got, issued)
	}
	if _, _, err := r.Reflect(raw1); err != nil {
		t.Fatalf("adoption probe rejected: %v", err)
	}
}

// TestProbeSessionEpochSurvivesPeerRestart is the end-to-end (in-memory, fake
// clock) D12 acceptance: a surviving responder brings an originator Up, and after
// the originator "restarts" (a fresh Prober with a NEW session and seq-from-0) the
// SAME responder reflects the fresh stream so the restarted originator comes Up
// again — the responder-contributed challenge bootstraps re-adoption, no liveness
// deadlock.
func TestProbeSessionEpochSurvivesPeerRestart(t *testing.T) {
	psk := testPSK(t, 0x5A)
	clk := newFakeClock()
	cfg := proberCfg()
	r := NewReflector(psk, newTestRand()) // the surviving peer

	// bringUp drives the Prober Up and returns how many of its probes the responder
	// surfaced as a peer-restart epoch change (the new Reflect flag, T116).
	bringUp := func(p *Prober) (epochChanges int) {
		for i := 0; i < cfg.Liveness.UpAfterSuccesses; i++ {
			raw, err := p.SendProbe()
			if err != nil {
				t.Fatalf("send probe: %v", err)
			}
			echo, epochChanged, err := r.Reflect(raw)
			if err != nil {
				t.Fatalf("reflect: %v", err)
			}
			if epochChanged {
				epochChanges++
			}
			clk.advance(10 * time.Millisecond)
			if err := p.HandleEcho(echo); err != nil {
				t.Fatalf("handle echo: %v", err)
			}
			p.Tick()
		}
		return epochChanges
	}

	// Boot 1 comes Up against the responder. Its adoption is a FIRST-EVER bootstrap,
	// not a restart, so no probe reports an epoch change.
	boot1 := NewProber("starlink", 1, 0xB0071111, psk, cfg, clk, discardLogger(t))
	if got := bringUp(boot1); got != 0 {
		t.Fatalf("boot 1 first-ever bootstrap surfaced %d epoch changes, want 0", got)
	}
	if boot1.State() != StateUp {
		t.Fatalf("boot 1 state = %v, want up", boot1.State())
	}

	// Restart: a brand-new Prober with a NEW session id and nextSeq back at 0,
	// against the SAME (unrestarted) responder whose high-water is already advanced.
	// The adoption over the already-adopted path is a RESTART: it surfaces the epoch
	// change EXACTLY ONCE (the challenge-carrying adoption probe; the bootstrap probe
	// and the following within-session probe do not).
	boot2 := NewProber("starlink", 1, 0xB0072222, psk, cfg, clk, discardLogger(t))
	if got := bringUp(boot2); got != 1 {
		t.Fatalf("restarted boot 2 surfaced %d epoch changes, want exactly 1", got)
	}
	if boot2.State() != StateUp {
		t.Fatalf("restarted boot 2 never came up (D12 liveness deadlock): state = %v", boot2.State())
	}
}

// reflectAdopt drives the two-round challenge handshake so r adopts sessionID on
// pathID and returns the next unused ProbeSeq plus whether the adoption (round 2)
// surfaced a peer-restart epoch change (T116). Round 1 (challenge 0) is reflected
// and teaches the live challenge; round 2 carries it and is adopted. The bootstrap
// round must never itself report an epoch change (it adopts nothing).
func reflectAdopt(t *testing.T, r *Reflector, psk config.Key, pathID uint8, sessionID, startSeq uint64) (nextSeq uint64, epochChanged bool) {
	t.Helper()
	echo, ec0, err := r.Reflect(encodeProbe(t, psk, pathID, sessionID, startSeq, false, 0))
	if err != nil {
		t.Fatalf("bootstrap probe (session %#x) rejected: %v", sessionID, err)
	}
	if ec0 {
		t.Fatalf("bootstrap probe (session %#x, challenge 0) reported epochChanged=true, want false", sessionID)
	}
	ch := echoChallenge(t, psk, echo)
	_, ec1, err := r.Reflect(encodeProbe(t, psk, pathID, sessionID, startSeq+1, false, ch))
	if err != nil {
		t.Fatalf("adoption probe (session %#x) rejected: %v", sessionID, err)
	}
	return startSeq + 2, ec1
}

// TestReflectEpochChangedOnPeerRestart is the T116 acceptance for the peer-restart
// signal surfaced from Reflect: (a) a restart adoption over an already-adopted path
// reports epochChanged=true EXACTLY ONCE even across multiple paths of the same
// boot; (b) a first-ever bootstrap adoption reports false; (c) a cross-session probe
// WITHOUT the live challenge (replay/forgery) reports false; (d) within-session
// probes and per-path duplicates report false.
func TestReflectEpochChangedOnPeerRestart(t *testing.T) {
	psk := testPSK(t, 0x5A)
	r := NewReflector(psk, newTestRand())
	const (
		p1 = uint8(1)
		p2 = uint8(2)
		s1 = uint64(0xB0071111) // first boot
		s2 = uint64(0xB0072222) // second boot (restart)
		s3 = uint64(0xB0073333) // third boot (a later restart)
	)

	// (b) First-ever bootstrap adoption is NOT a restart: false on every path of the
	// first boot (both paths adopt s1 for the first time).
	if _, ec := reflectAdopt(t, r, psk, p1, s1, 0); ec {
		t.Fatal("first-ever bootstrap adoption on path 1 reported epochChanged=true, want false")
	}
	if _, ec := reflectAdopt(t, r, psk, p2, s1, 0); ec {
		t.Fatal("first-ever bootstrap adoption on path 2 reported epochChanged=true, want false")
	}

	// (a) The peer restarts to s2. The first already-adopted path to re-adopt reports
	// the epoch change; the SECOND path of the same boot carries the same sessionID
	// and is deduped at the Reflector to false.
	if _, ec := reflectAdopt(t, r, psk, p1, s2, 10); !ec {
		t.Fatal("restart adoption on path 1 reported epochChanged=false, want true")
	}
	if _, ec := reflectAdopt(t, r, psk, p2, s2, 10); ec {
		t.Fatal("restart adoption on path 2 (same boot) reported epochChanged=true; per-epoch dedup failed")
	}

	// (c) A cross-session probe WITHOUT the live challenge (a replay attacker or a
	// forgery) is reflected but adopts nothing, so it never reports an epoch change —
	// neither with a zero challenge nor with a wrong non-zero one.
	if _, ec, err := r.Reflect(encodeProbe(t, psk, p1, s3, 0, false, 0)); err != nil || ec {
		t.Fatalf("cross-session probe (zero challenge): err=%v epochChanged=%v, want nil/false", err, ec)
	}
	if _, ec, err := r.Reflect(encodeProbe(t, psk, p1, s3, 1, false, 0x1234_5678_9ABC_DEF0)); err != nil || ec {
		t.Fatalf("cross-session probe (wrong challenge): err=%v epochChanged=%v, want nil/false", err, ec)
	}

	// (d) Within-session probes and per-path duplicates never report an epoch change:
	// s2 is still the adopted session on p1 with its high-water at 11, so seq 12 is a
	// fresh in-session probe and a re-sent seq 12 is a rejected duplicate.
	if _, ec, err := r.Reflect(encodeProbe(t, psk, p1, s2, 12, false, 0)); err != nil || ec {
		t.Fatalf("within-session probe: err=%v epochChanged=%v, want nil/false", err, ec)
	}
	if _, ec, err := r.Reflect(encodeProbe(t, psk, p1, s2, 12, false, 0)); !errors.Is(err, ErrReplay) || ec {
		t.Fatalf("within-session duplicate: err=%v epochChanged=%v, want ErrReplay/false", err, ec)
	}

	// A subsequent GENUINE restart to a new epoch fires again — the dedup is per new
	// epoch, not once-for-all.
	if _, ec := reflectAdopt(t, r, psk, p1, s3, 20); !ec {
		t.Fatal("restart to a new epoch reported epochChanged=false, want true")
	}
}

// TestHandleEchoRejectsWrongPath asserts a Prober rejects an echo carrying another
// path's PathID (ErrPathMismatch) and does not count it as this path's heartbeat —
// otherwise one live path would mask every other path's death.
func TestHandleEchoRejectsWrongPath(t *testing.T) {
	psk := testPSK(t, 0x5A)
	clk := newFakeClock()
	p := newTestProber(t, psk, clk) // pathID 1

	foreign, err := frame.Encode(psk, frame.Probe{
		PathID:         2, // not this prober's path
		ProbeSeq:       0,
		TimestampNanos: clk.Now().UnixNano(),
	})
	if err != nil {
		t.Fatalf("encode foreign-path echo: %v", err)
	}
	if err := p.HandleEcho(foreign); !errors.Is(err, ErrPathMismatch) {
		t.Fatalf("wrong-path echo: got %v, want ErrPathMismatch", err)
	}
	if p.State() != StateDown {
		t.Fatalf("wrong-path echo advanced liveness: state = %v", p.State())
	}
	if got := p.Estimate().RTT; got != 0 {
		t.Fatalf("wrong-path echo mutated RTT estimate: %v", got)
	}
}

// TestProberConcurrent drives a Prober from several goroutines at once — the T12
// ownership model (receive goroutine calling HandleEcho, timer goroutine calling
// SendProbe/Tick, plus readers) — so the race detector validates the mutex.
func TestProberConcurrent(t *testing.T) {
	psk := testPSK(t, 0x5A)
	p := NewProber("starlink", 1, testSessionID, psk, proberCfg(), SystemClock{}, discardLogger(t))
	r := NewReflector(psk, newTestRand())

	const iters = 2000
	var wg sync.WaitGroup
	wg.Add(3)

	// Sender + echo handler (one path's receive path).
	go func() {
		defer wg.Done()
		for i := 0; i < iters; i++ {
			raw, err := p.SendProbe()
			if err != nil {
				t.Errorf("send probe: %v", err)
				return
			}
			echo, _, err := r.Reflect(raw)
			if err != nil {
				t.Errorf("reflect: %v", err)
				return
			}
			_ = p.HandleEcho(echo)
		}
	}()
	// Timer goroutine.
	go func() {
		defer wg.Done()
		for i := 0; i < iters; i++ {
			p.Tick()
		}
	}()
	// Reader goroutine (metrics scrape).
	go func() {
		defer wg.Done()
		for i := 0; i < iters; i++ {
			_ = p.Estimate()
			_ = p.State()
		}
	}()
	wg.Wait()
}

// TestReflectorConcurrent reflects probes for distinct paths from concurrent
// goroutines, validating the PathID-keyed state map under the race detector. The
// probes carry no live challenge, so none are adopted — each is reflected — which
// exercises the default (no-reset) path under contention.
func TestReflectorConcurrent(t *testing.T) {
	psk := testPSK(t, 0x5A)
	r := NewReflector(psk, newTestRand())

	const (
		paths = 4
		iters = 1000
	)
	var wg sync.WaitGroup
	wg.Add(paths)
	for path := 0; path < paths; path++ {
		go func(pathID uint8) {
			defer wg.Done()
			for seq := uint64(0); seq < iters; seq++ {
				raw, err := frame.Encode(psk, frame.Probe{PathID: pathID, ProbeSeq: seq})
				if err != nil {
					t.Errorf("encode: %v", err)
					return
				}
				if _, _, err := r.Reflect(raw); err != nil {
					t.Errorf("reflect path=%d seq=%d: %v", pathID, seq, err)
					return
				}
			}
		}(uint8(path))
	}
	wg.Wait()
}

// TestProberDrivesLiveness is an end-to-end (in-memory) check that a healthy
// probe exchange brings the path Up, and that a subsequent probe blackhole
// (echoes stop) marks it Down within the detection threshold.
func TestProberDrivesLiveness(t *testing.T) {
	psk := testPSK(t, 0x5A)
	clk := newFakeClock()
	cfg := proberCfg()
	p := newTestProber(t, psk, clk)
	r := NewReflector(psk, newTestRand())

	const rtt = 20 * time.Millisecond
	for i := 0; i < cfg.Liveness.UpAfterSuccesses; i++ {
		raw, err := p.SendProbe()
		if err != nil {
			t.Fatalf("send probe %d: %v", i, err)
		}
		echo, _, err := r.Reflect(raw)
		if err != nil {
			t.Fatalf("reflect %d: %v", i, err)
		}
		clk.advance(rtt)
		if err := p.HandleEcho(echo); err != nil {
			t.Fatalf("handle echo %d: %v", i, err)
		}
		p.Tick()
	}
	if p.State() != StateUp {
		t.Fatalf("after healthy exchange state = %v, want up", p.State())
	}
	if got := p.Estimate().RTT; absDuration(got-rtt) > 5*time.Millisecond {
		t.Fatalf("RTT estimate = %v, want ~%v", got, rtt)
	}

	// Blackhole: no more echoes. After the detection threshold elapses, Tick marks
	// the path down.
	clk.advance(cfg.Liveness.DownAfter + time.Millisecond)
	p.Tick()
	if p.State() != StateDown {
		t.Fatalf("after blackhole state = %v, want down", p.State())
	}
}
