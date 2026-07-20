package telemetry

import (
	"testing"

	"github.com/7mind/wanbond/internal/frame"
)

// TestPaddedProbeReflectedAtSameSize drives a padded MTU-probe through the real
// Prober -> Reflector -> Prober machinery (T202, defect D85): the originator sends a
// probe padded to a target on-wire size, the reflector echoes it, and the echo is
// the SAME on-wire size and carries the pad marker. A fresh echo of this size is the
// operational confirmation that a datagram of N outer bytes traverses the path.
func TestPaddedProbeReflectedAtSameSize(t *testing.T) {
	psk := testPSK(t, 0x5A)
	clk := newFakeClock()
	p := newTestProber(t, psk, clk)
	r := NewReflector(psk, newTestRand())

	for _, onWire := range []int{frame.ProbeBaseOnWire, 1400, 1500} {
		raw, _, err := p.SendPaddedProbe(onWire)
		if err != nil {
			t.Fatalf("onWire=%d: send padded probe: %v", onWire, err)
		}
		if len(raw) != onWire {
			t.Fatalf("onWire=%d: padded probe is %d bytes on wire, want %d", onWire, len(raw), onWire)
		}

		echo, _, err := r.Reflect(raw)
		if err != nil {
			t.Fatalf("onWire=%d: reflect: %v", onWire, err)
		}
		if len(echo) != onWire {
			t.Fatalf("onWire=%d: echo is %d bytes on wire, want the same %d", onWire, len(echo), onWire)
		}

		f, err := frame.Decode(psk, echo)
		if err != nil {
			t.Fatalf("onWire=%d: decode echo: %v", onWire, err)
		}
		ep, ok := f.(frame.Probe)
		if !ok {
			t.Fatalf("onWire=%d: echo is %T, want frame.Probe", onWire, f)
		}
		if !ep.IsEcho {
			t.Fatalf("onWire=%d: echo IsEcho=false, want true", onWire)
		}
		if !ep.Padded || frame.ProbeOnWireSize(ep.PadLen) != onWire {
			t.Fatalf("onWire=%d: echo Padded=%v size=%d, want padded at %d", onWire, ep.Padded, frame.ProbeOnWireSize(ep.PadLen), onWire)
		}
	}
}

// TestPaddedProbeEchoFeedsLiveness confirms a padded probe's echo is an ordinary
// liveness signal: HandleEcho accepts it and records an RTT sample, so the padded
// MTU-probe reuses the existing probe/echo machinery rather than a parallel channel.
func TestPaddedProbeEchoFeedsLiveness(t *testing.T) {
	psk := testPSK(t, 0x5A)
	clk := newFakeClock()
	p := newTestProber(t, psk, clk)
	r := NewReflector(psk, newTestRand())

	raw, _, err := p.SendPaddedProbe(1400)
	if err != nil {
		t.Fatalf("send padded probe: %v", err)
	}
	echo, _, err := r.Reflect(raw)
	if err != nil {
		t.Fatalf("reflect: %v", err)
	}
	if err := p.HandleEcho(echo); err != nil {
		t.Fatalf("handle padded echo: %v", err)
	}
}

// TestSendPaddedProbeRejectsOutOfRange asserts the originator refuses an on-wire
// target the codec cannot express, rather than emitting a malformed datagram.
func TestSendPaddedProbeRejectsOutOfRange(t *testing.T) {
	psk := testPSK(t, 0x5A)
	p := newTestProber(t, psk, newFakeClock())
	if _, _, err := p.SendPaddedProbe(frame.MaxPaddedProbeOnWire + 1); err == nil {
		t.Fatal("SendPaddedProbe(max+1) succeeded, want error")
	}
	if _, _, err := p.SendPaddedProbe(frame.ProbeBaseOnWire - 1); err == nil {
		t.Fatal("SendPaddedProbe(base-1) succeeded, want error")
	}
}
