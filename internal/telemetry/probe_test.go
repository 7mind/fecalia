package telemetry

import (
	"errors"
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
	return NewProber("starlink", 1, psk, proberCfg(), clk, discardLogger(t))
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
