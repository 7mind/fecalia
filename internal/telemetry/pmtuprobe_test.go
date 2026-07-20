package telemetry

import (
	"errors"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/frame"
)

// neverAfter is an after-func whose deadline never fires: used when a test expects the
// echo (not the deadline) to complete the await.
func neverAfter(time.Duration) <-chan time.Time { return make(chan time.Time) }

// immediateAfter is an after-func whose deadline has already elapsed: used to drive the
// deadline branch deterministically with no real sleep.
func immediateAfter(time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	ch <- time.Time{}
	return ch
}

// TestEchoAwaitProbeMatchedEcho: a matched echo within the deadline -> (true, nil). The
// send func delivers the reflected echo synchronously and the deadline never fires.
func TestEchoAwaitProbeMatchedEcho(t *testing.T) {
	psk := testPSK(t, 0x5A)
	p := newTestProber(t, psk, newFakeClock())
	r := NewReflector(psk, newTestRand())

	var e *EchoAwaitProbe
	send := func(raw []byte) error {
		f, err := frame.Decode(psk, raw)
		if err != nil {
			return err
		}
		echo, _, rerr := r.Reflect(raw)
		if rerr != nil {
			return rerr
		}
		_ = p.HandleEcho(echo)                 // real liveness/loss fold, as T227 does
		e.NotifyEcho(f.(frame.Probe).ProbeSeq) // release the PMTU waiter
		return nil
	}
	e = NewEchoAwaitProbe(p, send, 0, neverAfter)

	echoed, err := e.ProbePMTU(1400)
	if err != nil || !echoed {
		t.Fatalf("matched echo: got (echoed=%v, err=%v), want (true, nil)", echoed, err)
	}
}

// TestEchoAwaitProbeDeadline: no echo by the deadline -> (false, nil). The send
// succeeds but delivers no echo, so the (immediate) deadline completes the await.
func TestEchoAwaitProbeDeadline(t *testing.T) {
	psk := testPSK(t, 0x5A)
	p := newTestProber(t, psk, newFakeClock())
	send := func([]byte) error { return nil } // sent, but nothing echoes it
	e := NewEchoAwaitProbe(p, send, 0, immediateAfter)

	echoed, err := e.ProbePMTU(1400)
	if err != nil || echoed {
		t.Fatalf("deadline: got (echoed=%v, err=%v), want (false, nil)", echoed, err)
	}
}

// TestEchoAwaitProbeEMSGSIZE: a local ErrProbeTooLarge (the DF EMSGSIZE the bind send
// maps) -> (false, nil), NOT an error; the deadline is never consulted.
func TestEchoAwaitProbeEMSGSIZE(t *testing.T) {
	psk := testPSK(t, 0x5A)
	p := newTestProber(t, psk, newFakeClock())
	send := func([]byte) error { return ErrProbeTooLarge }
	e := NewEchoAwaitProbe(p, send, 0, neverAfter)

	echoed, err := e.ProbePMTU(1500)
	if err != nil || echoed {
		t.Fatalf("EMSGSIZE: got (echoed=%v, err=%v), want (false, nil)", echoed, err)
	}
}

// TestEchoAwaitProbeTransportError: a non-EMSGSIZE send failure, and an out-of-bounds
// onWire, both surface as (_, err) so the search stays unconverged and retries.
func TestEchoAwaitProbeTransportError(t *testing.T) {
	psk := testPSK(t, 0x5A)
	p := newTestProber(t, psk, newFakeClock())

	boom := errors.New("boom")
	e := NewEchoAwaitProbe(p, func([]byte) error { return boom }, 0, neverAfter)
	if _, err := e.ProbePMTU(1400); !errors.Is(err, boom) {
		t.Fatalf("transport error: want boom, got %v", err)
	}

	// A candidate whose derived on-wire size (pathMTU - outerIPUDPOverhead) exceeds the
	// padded-probe bounds is rejected by SendPaddedProbe before any send.
	e2 := NewEchoAwaitProbe(p, func([]byte) error { return nil }, 0, neverAfter)
	if _, err := e2.ProbePMTU(frame.MaxPaddedProbeOnWire + outerIPUDPOverhead + 1); err == nil {
		t.Fatal("out-of-bounds path MTU: want error, got nil")
	}
}

// TestEchoAwaitProbeDecoupledFromAntiReplay is the R245 regression: a slow padded echo
// (seq N) must still complete its await even after a later, faster liveness echo (seq
// N+1) advanced the Prober's anti-replay high-water past N — because NotifyEcho matches
// the awaited seq directly, NOT via HandleEcho's guard verdict.
func TestEchoAwaitProbeDecoupledFromAntiReplay(t *testing.T) {
	psk := testPSK(t, 0x5A)
	p := newTestProber(t, psk, newFakeClock())
	r := NewReflector(psk, newTestRand())

	var e *EchoAwaitProbe
	send := func(raw []byte) error {
		f, err := frame.Decode(psk, raw)
		if err != nil {
			return err
		}
		pmtuSeq := f.(frame.Probe).ProbeSeq

		// A later liveness probe (seq N+1) echoes FIRST, advancing the guard high-water
		// past the in-flight PMTU seq N. HandleEcho(this later echo) is accepted.
		laterRaw, serr := p.SendProbe()
		if serr != nil {
			return serr
		}
		laterEcho, _, rerr := r.Reflect(laterRaw)
		if rerr != nil {
			return rerr
		}
		if herr := p.HandleEcho(laterEcho); herr != nil {
			return herr
		}

		// Now the PMTU echo (seq N) arrives. In the real path T227 calls BOTH: HandleEcho
		// (which now returns ErrReplay since N < high-water — verified below) AND NotifyEcho.
		pmtuEcho, _, rerr := r.Reflect(raw)
		if rerr != nil {
			return rerr
		}
		if herr := p.HandleEcho(pmtuEcho); !errors.Is(herr, ErrReplay) {
			t.Fatalf("expected the stale PMTU echo to be ErrReplay'd by the guard, got %v", herr)
		}
		// Decoupled release: NotifyEcho must complete the await despite the ErrReplay.
		e.NotifyEcho(pmtuSeq)
		return nil
	}
	e = NewEchoAwaitProbe(p, send, 0, neverAfter)

	echoed, err := e.ProbePMTU(1400)
	if err != nil || !echoed {
		t.Fatalf("decoupled await: got (echoed=%v, err=%v), want (true, nil)", echoed, err)
	}
}

// TestEchoAwaitProbeStaleSeqDoesNotUnblock: a NotifyEcho for a DIFFERENT seq must not
// release the awaiting probe — the deadline (not the echo) completes it.
func TestEchoAwaitProbeStaleSeqDoesNotUnblock(t *testing.T) {
	psk := testPSK(t, 0x5A)
	p := newTestProber(t, psk, newFakeClock())

	var e *EchoAwaitProbe
	send := func(raw []byte) error {
		f, err := frame.Decode(psk, raw)
		if err != nil {
			return err
		}
		e.NotifyEcho(f.(frame.Probe).ProbeSeq + 999) // wrong seq: must be a no-op
		return nil
	}
	e = NewEchoAwaitProbe(p, send, 0, immediateAfter)

	echoed, err := e.ProbePMTU(1400)
	if err != nil || echoed {
		t.Fatalf("stale-seq NotifyEcho: got (echoed=%v, err=%v), want (false, nil) via deadline", echoed, err)
	}
}

// TestEchoAwaitProbeSearchConvergesWithoutLossPollution is the combined (g)+(i)
// acceptance: a full PMTUDiscovery search driven by the REAL EchoAwaitProbe converges
// to the largest echoing on-wire size, and the intentionally-dropped oversize probes
// do NOT pollute the path's loss estimate (they are excluded).
func TestEchoAwaitProbeSearchConvergesWithoutLossPollution(t *testing.T) {
	// The modelled underlay's OUTER IP-level path MTU. ProbePMTU sizes the socket datagram
	// pathMTU-outerIPUDPOverhead, so the IP datagram is len(raw)+outerIPUDPOverhead; the
	// path refuses (EMSGSIZE) any IP datagram larger than pathMTU, so the search converges
	// on pathMTU itself.
	const pathMTU = 1400
	psk := testPSK(t, 0x5A)
	clk := newFakeClock()
	p := newTestProber(t, psk, clk)
	r := NewReflector(psk, newTestRand())

	var e *EchoAwaitProbe
	send := func(raw []byte) error {
		if len(raw)+outerIPUDPOverhead > pathMTU {
			// Oversize under DF: the kernel refuses it locally (EMSGSIZE). The probe is a
			// deliberate, expected drop — ProbePMTU must exclude its seq from loss.
			return ErrProbeTooLarge
		}
		f, err := frame.Decode(psk, raw)
		if err != nil {
			return err
		}
		echo, _, rerr := r.Reflect(raw)
		if rerr != nil {
			return rerr
		}
		_ = p.HandleEcho(echo) // in-range probe echoes: real loss/liveness fold
		e.NotifyEcho(f.(frame.Probe).ProbeSeq)
		return nil
	}
	e = NewEchoAwaitProbe(p, send, 0, neverAfter)

	d := NewPMTUDiscovery("cellular", PMTUConfig{DefaultMTU: 1500}, e, clk, discardLogger(t))
	if err := d.Tick(StateUp); err != nil {
		t.Fatalf("discovery search: %v", err)
	}
	if got := d.PathMTU(); got != pathMTU {
		t.Fatalf("converged PMTU = %d, want the outer path MTU %d", got, pathMTU)
	}
	if loss := p.Estimate().Loss; loss != 0 {
		t.Fatalf("per-path loss = %v after a search with dropped oversize probes, want 0 (excluded)", loss)
	}
}

// TestPathMTUOrZeroConvergedGate: the metrics accessor reports 0 until a non-pinned
// path's first search converges (avoiding the boot-time MTU dip, R245), and the
// configured value immediately for a pinned path.
func TestPathMTUOrZeroConvergedGate(t *testing.T) {
	clk := newFakeClock()

	// Non-pinned: 0 before convergence, the discovered value after.
	probe := &fakePMTUProbe{threshold: 1400}
	d := NewPMTUDiscovery("cellular", PMTUConfig{DefaultMTU: 1500}, probe, clk, discardLogger(t))
	if got := d.PathMTUOrZero(); got != 0 {
		t.Fatalf("pre-convergence PathMTUOrZero = %d, want 0 (no boot dip)", got)
	}
	if err := d.Tick(StateUp); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if got := d.PathMTUOrZero(); got != 1400 {
		t.Fatalf("post-convergence PathMTUOrZero = %d, want 1400", got)
	}

	// Pinned: authoritative immediately, no probing.
	dp := NewPMTUDiscovery("starlink", PMTUConfig{ConfiguredMTU: 1400, DefaultMTU: 1500}, probe, clk, discardLogger(t))
	if got := dp.PathMTUOrZero(); got != 1400 {
		t.Fatalf("pinned PathMTUOrZero = %d, want the configured 1400 immediately", got)
	}
}
