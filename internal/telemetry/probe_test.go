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

// TestProbeEchoRTT asserts a full send -> reflect -> handle round-trip yields the
// elapsed round-trip time as the first RTT sample (which seeds srtt exactly).
func TestProbeEchoRTT(t *testing.T) {
	psk := testPSK(t, 0x5A)
	clk := newFakeClock()
	p := newTestProber(t, psk, clk)
	r := NewReflector(psk)

	raw, err := p.SendProbe()
	if err != nil {
		t.Fatalf("send probe: %v", err)
	}
	echo, err := r.Reflect(raw)
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
	r := NewReflector(psk)

	raw, err := p.SendProbe()
	if err != nil {
		t.Fatalf("send probe: %v", err)
	}
	echo, err := r.Reflect(raw)
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

// TestProbeReplayRejected asserts the anti-replay high-water mark rejects a
// replayed or stale ProbeSeq on both the reflector and the prober — defect D4.
func TestProbeReplayRejected(t *testing.T) {
	psk := testPSK(t, 0x5A)
	clk := newFakeClock()
	p := newTestProber(t, psk, clk)
	r := NewReflector(psk)

	raw, err := p.SendProbe()
	if err != nil {
		t.Fatalf("send probe: %v", err)
	}
	echo, err := r.Reflect(raw)
	if err != nil {
		t.Fatalf("reflect: %v", err)
	}
	// Reflector rejects a replayed inbound probe.
	if _, err := r.Reflect(raw); !errors.Is(err, ErrReplay) {
		t.Fatalf("reflector accepted replayed probe: %v", err)
	}
	// Prober accepts the fresh echo once, then rejects its replay.
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
	r := NewReflector(psk)

	ctrl, err := frame.Encode(psk, frame.Control{ControlType: 1, Payload: []byte("x")})
	if err != nil {
		t.Fatalf("encode control: %v", err)
	}
	if _, err := r.Reflect(ctrl); err == nil {
		t.Fatal("reflector accepted a control frame")
	}
	if err := p.HandleEcho(ctrl); err == nil {
		t.Fatal("prober accepted a control frame as an echo")
	}
}

// TestReflectorPerPathAntiReplay asserts a single Reflector serving multiple
// paths keys its anti-replay by PathID: each path's ProbeSeq space is independent
// (both start at 0), so path B's opening probe is not rejected as a replay of path
// A's, while a genuine per-path replay still is.
func TestReflectorPerPathAntiReplay(t *testing.T) {
	psk := testPSK(t, 0x5A)
	clk := newFakeClock()
	r := NewReflector(psk)

	probeOnPath := func(pathID uint8, seq uint64) []byte {
		raw, err := frame.Encode(psk, frame.Probe{
			PathID:         pathID,
			ProbeSeq:       seq,
			TimestampNanos: clk.Now().UnixNano(),
		})
		if err != nil {
			t.Fatalf("encode probe path=%d seq=%d: %v", pathID, seq, err)
		}
		return raw
	}

	// Path A seq 0 accepted.
	if _, err := r.Reflect(probeOnPath(1, 0)); err != nil {
		t.Fatalf("path A seq 0: %v", err)
	}
	// Path B seq 0 must ALSO be accepted (independent seq space).
	if _, err := r.Reflect(probeOnPath(2, 0)); err != nil {
		t.Fatalf("path B seq 0 rejected as a cross-path replay: %v", err)
	}
	// A genuine per-path replay (path A seq 0 again) is rejected.
	if _, err := r.Reflect(probeOnPath(1, 0)); !errors.Is(err, ErrReplay) {
		t.Fatalf("path A replay: got %v, want ErrReplay", err)
	}
	// Path A advancing to seq 1 is still fine.
	if _, err := r.Reflect(probeOnPath(1, 1)); err != nil {
		t.Fatalf("path A seq 1: %v", err)
	}
}

// encodeProbe is a test helper minting one authenticated probe/echo frame.
func encodeProbe(t *testing.T, psk config.Key, pathID uint8, sessionID, seq uint64, isEcho bool) []byte {
	t.Helper()
	raw, err := frame.Encode(psk, frame.Probe{
		PathID:    pathID,
		ProbeSeq:  seq,
		IsEcho:    isEcho,
		SessionID: sessionID,
	})
	if err != nil {
		t.Fatalf("encode probe session=%#x seq=%d: %v", sessionID, seq, err)
	}
	return raw
}

// TestReflectorSessionEpochResetOnPeerRestart is the core D12 fix: WITHIN a
// session strict-monotonic replay rejection is preserved (D4), but a NEW authentic
// SessionID (a peer restart whose ProbeSeq reset to 0) resets the per-path
// high-water so the restarted peer's fresh seq-from-0 stream is ACCEPTED — without
// this the responder would reject every fresh probe until the counter organically
// passed the prior session's high-water (the liveness deadlock).
func TestReflectorSessionEpochResetOnPeerRestart(t *testing.T) {
	psk := testPSK(t, 0x5A)
	r := NewReflector(psk)
	const (
		pathID = uint8(1)
		s1     = uint64(0x1111_1111_1111_1111)
		s2     = uint64(0x2222_2222_2222_2222)
	)

	// Session 1 advances 0,1,2.
	for seq := uint64(0); seq <= 2; seq++ {
		if _, err := r.Reflect(encodeProbe(t, psk, pathID, s1, seq, false)); err != nil {
			t.Fatalf("session 1 seq %d: %v", seq, err)
		}
	}
	// D4 preserved WITHIN the session: a replayed/stale seq is rejected.
	if _, err := r.Reflect(encodeProbe(t, psk, pathID, s1, 1, false)); !errors.Is(err, ErrReplay) {
		t.Fatalf("within-session replay: got %v, want ErrReplay", err)
	}

	// Peer restart: a NEW SessionID with seq from 0. The high-water must reset so
	// the restarted peer's opening probes are accepted (D12).
	for seq := uint64(0); seq <= 2; seq++ {
		if _, err := r.Reflect(encodeProbe(t, psk, pathID, s2, seq, false)); err != nil {
			t.Fatalf("restart session 2 seq %d rejected (D12 deadlock): %v", seq, err)
		}
	}
	// The new session then enforces its own within-session monotonicity.
	if _, err := r.Reflect(encodeProbe(t, psk, pathID, s2, 2, false)); !errors.Is(err, ErrReplay) {
		t.Fatalf("session 2 replay: got %v, want ErrReplay", err)
	}
}

// TestReflectorRejectsRolledBackSession is the anti-rollback assertion: once a
// session is superseded by a newer one, replaying the OLD SessionID (any seq, high
// or low) must NOT force a reset — an attacker confined to replaying captured
// frames can never revive a retired session or roll the high-water back.
func TestReflectorRejectsRolledBackSession(t *testing.T) {
	psk := testPSK(t, 0x5A)
	r := NewReflector(psk)
	const (
		pathID = uint8(1)
		sOld   = uint64(0xAAAA_AAAA_AAAA_AAAA)
		sNew   = uint64(0xBBBB_BBBB_BBBB_BBBB)
	)

	// Old session establishes a high-water at seq 5.
	for _, seq := range []uint64{0, 3, 5} {
		if _, err := r.Reflect(encodeProbe(t, psk, pathID, sOld, seq, false)); err != nil {
			t.Fatalf("old session seq %d: %v", seq, err)
		}
	}
	// Peer restarts to a new session (supersedes the old one).
	if _, err := r.Reflect(encodeProbe(t, psk, pathID, sNew, 0, false)); err != nil {
		t.Fatalf("adopt new session: %v", err)
	}

	// Rollback attempt: replay the OLD session with a low seq — must be rejected,
	// NOT treated as a fresh reset.
	if _, err := r.Reflect(encodeProbe(t, psk, pathID, sOld, 0, false)); !errors.Is(err, ErrReplay) {
		t.Fatalf("rollback (old session, low seq): got %v, want ErrReplay", err)
	}
	// Even a HIGHER, never-seen seq under the retired session is rejected: the whole
	// session is dead, not just its already-seen seqs.
	if _, err := r.Reflect(encodeProbe(t, psk, pathID, sOld, 99, false)); !errors.Is(err, ErrReplay) {
		t.Fatalf("rollback (old session, high seq): got %v, want ErrReplay", err)
	}
	// The current (new) session is unaffected and keeps advancing.
	if _, err := r.Reflect(encodeProbe(t, psk, pathID, sNew, 1, false)); err != nil {
		t.Fatalf("current session after rollback attempts: %v", err)
	}
}

// TestReflectorSessionPerPathIndependent asserts the (SessionID,PathID) key keeps
// each path's session state independent: a new session on one path never resets or
// retires another path's session.
func TestReflectorSessionPerPathIndependent(t *testing.T) {
	psk := testPSK(t, 0x5A)
	r := NewReflector(psk)
	const s1 = uint64(0x0101_0101_0101_0101)

	if _, err := r.Reflect(encodeProbe(t, psk, 1, s1, 5, false)); err != nil {
		t.Fatalf("path 1 session s1 seq 5: %v", err)
	}
	// Same session id on a DIFFERENT path starts fresh (seq 0 accepted).
	if _, err := r.Reflect(encodeProbe(t, psk, 2, s1, 0, false)); err != nil {
		t.Fatalf("path 2 session s1 seq 0 rejected as cross-path replay: %v", err)
	}
	// Path 1's high-water is untouched by path 2: replaying path 1 seq 5 rejected.
	if _, err := r.Reflect(encodeProbe(t, psk, 1, s1, 5, false)); !errors.Is(err, ErrReplay) {
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
	foreign := encodeProbe(t, psk, 1, testSessionID^0xFFFF, 0, true)
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
	echo, err := NewReflector(psk).Reflect(raw)
	if err != nil {
		t.Fatalf("reflect: %v", err)
	}
	if err := p.HandleEcho(echo); err != nil {
		t.Fatalf("genuine same-session echo rejected: %v", err)
	}
}

// TestProbeSessionEpochSurvivesPeerRestart is the end-to-end (in-memory, fake
// clock) D12 acceptance: a surviving responder brings an originator Up, and after
// the originator "restarts" (a fresh Prober with a NEW session and seq-from-0) the
// SAME responder reflects the fresh stream so the restarted originator comes Up
// again — no organic-counter-catch-up wait, no liveness deadlock.
func TestProbeSessionEpochSurvivesPeerRestart(t *testing.T) {
	psk := testPSK(t, 0x5A)
	clk := newFakeClock()
	cfg := proberCfg()
	r := NewReflector(psk) // the surviving peer, retains high-water across the restart

	bringUp := func(p *Prober) {
		for i := 0; i < cfg.Liveness.UpAfterSuccesses; i++ {
			raw, err := p.SendProbe()
			if err != nil {
				t.Fatalf("send probe: %v", err)
			}
			echo, err := r.Reflect(raw)
			if err != nil {
				t.Fatalf("reflect: %v", err)
			}
			clk.advance(10 * time.Millisecond)
			if err := p.HandleEcho(echo); err != nil {
				t.Fatalf("handle echo: %v", err)
			}
			p.Tick()
		}
	}

	// Boot 1 comes Up against the responder.
	boot1 := NewProber("starlink", 1, 0xB0071111, psk, cfg, clk, discardLogger(t))
	bringUp(boot1)
	if boot1.State() != StateUp {
		t.Fatalf("boot 1 state = %v, want up", boot1.State())
	}

	// Restart: a brand-new Prober with a NEW session id and nextSeq back at 0,
	// against the SAME (unrestarted) responder whose high-water is now at 2.
	boot2 := NewProber("starlink", 1, 0xB0072222, psk, cfg, clk, discardLogger(t))
	bringUp(boot2)
	if boot2.State() != StateUp {
		t.Fatalf("restarted boot 2 never came up (D12 liveness deadlock): state = %v", boot2.State())
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
	r := NewReflector(psk)

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
			echo, err := r.Reflect(raw)
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
// goroutines, validating the PathID-keyed anti-replay map under the race detector.
func TestReflectorConcurrent(t *testing.T) {
	psk := testPSK(t, 0x5A)
	r := NewReflector(psk)

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
				if _, err := r.Reflect(raw); err != nil {
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
	r := NewReflector(psk)

	const rtt = 20 * time.Millisecond
	for i := 0; i < cfg.Liveness.UpAfterSuccesses; i++ {
		raw, err := p.SendProbe()
		if err != nil {
			t.Fatalf("send probe %d: %v", i, err)
		}
		echo, err := r.Reflect(raw)
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
