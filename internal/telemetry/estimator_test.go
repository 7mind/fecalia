package telemetry

import (
	"math"
	"testing"
	"time"
)

// TestRTTConvergence feeds a square-wave RTT trace (base ± amplitude) and asserts
// the smoothed RTT converges to the injected base. For an alternating ±J input
// the RFC 6298 recursion (alpha = 1/8) leaves srtt oscillating within J/15 of the
// base, well inside the tolerance.
func TestRTTConvergence(t *testing.T) {
	const (
		base      = 50 * time.Millisecond
		amplitude = 10 * time.Millisecond
		samples   = 4000
		tolerance = 1 * time.Millisecond
	)
	e := NewEstimator(0)
	for i := 0; i < samples; i++ {
		s := base + amplitude
		if i%2 == 1 {
			s = base - amplitude
		}
		e.ObserveRTT(s)
	}
	got := e.Estimate().RTT
	if d := absDuration(got - base); d > tolerance {
		t.Fatalf("RTT = %v, want %v ± %v (off by %v)", got, base, tolerance, d)
	}
}

// TestJitterConvergence asserts the jitter estimate (RFC 6298 RTTVAR) converges
// to the injected sample deviation. A ±J square wave has mean-absolute-deviation
// exactly J; the estimator settles at ~1.07J because srtt lags the input, which
// the 20% tolerance absorbs.
func TestJitterConvergence(t *testing.T) {
	const (
		base      = 50 * time.Millisecond
		amplitude = 10 * time.Millisecond // injected jitter (sample MAD from mean)
		samples   = 4000
		tolerance = 2 * time.Millisecond // 20% of amplitude
	)
	e := NewEstimator(0)
	for i := 0; i < samples; i++ {
		s := base + amplitude
		if i%2 == 1 {
			s = base - amplitude
		}
		e.ObserveRTT(s)
	}
	got := e.Estimate().Jitter
	if d := absDuration(got - amplitude); d > tolerance {
		t.Fatalf("Jitter = %v, want %v ± %v (off by %v)", got, amplitude, tolerance, d)
	}
}

// TestPerPathLossConvergence feeds a per-path probe-echo stream with a
// deterministic fraction of missing echoes and asserts the windowed per-path loss
// converges to the injected rate. Per-path loss is measured from probe-echo
// ProbeSeq gaps, not the (connection-global) outer DATA sequence.
func TestPerPathLossConvergence(t *testing.T) {
	cases := []struct {
		name     string
		dropMod  uint64 // echo for seq lost where seq%dropMod == 0
		wantLoss float64
	}{
		{"no loss", 0, 0.0},
		{"ten percent", 10, 0.10},
		{"five percent", 20, 0.05},
	}
	const (
		total     = 6000
		tolerance = 0.02
	)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := NewEstimator(0)
			for seq := uint64(0); seq < total; seq++ {
				if tc.dropMod != 0 && seq%tc.dropMod == 0 {
					continue // echo lost: never observed
				}
				e.ObserveProbeEcho(seq)
			}
			got := e.Estimate().Loss
			if math.Abs(got-tc.wantLoss) > tolerance {
				t.Fatalf("loss = %.4f, want %.4f ± %.2f", got, tc.wantLoss, tolerance)
			}
		})
	}
}

// TestPerPathLossIgnoresStriping is the regression for the connection-global
// outer-seq defect: per-path loss now comes from the dense per-path probe-echo
// sequence, so a path that carries only a strided fraction of the connection's
// DATA (the scheduler striping half its frames to another path) does not read as
// 50% loss. The probe echoes are contiguous regardless of DATA striping, so loss
// stays 0.
func TestPerPathLossIgnoresStriping(t *testing.T) {
	e := NewEstimator(0)
	for seq := uint64(0); seq < 4000; seq++ {
		e.ObserveProbeEcho(seq) // every probe on this path is echoed
	}
	if got := e.Estimate().Loss; got != 0 {
		t.Fatalf("per-path loss = %.4f with no lost echoes, want 0 (striping must not read as loss)", got)
	}
}

// TestLossWindowReordering verifies a late (reordered) echo within the window
// retroactively clears its gap, so transient reordering is not mistaken for loss.
func TestLossWindowReordering(t *testing.T) {
	e := NewEstimator(8)
	// Observe echoes 0,1,3,4,5,6,7 (2 arrives late); highest = 7, window = 8.
	for _, seq := range []uint64{0, 1, 3, 4, 5, 6, 7} {
		e.ObserveProbeEcho(seq)
	}
	if got := e.Estimate().Loss; math.Abs(got-1.0/8.0) > 1e-9 {
		t.Fatalf("pre-reorder loss = %.4f, want %.4f", got, 1.0/8.0)
	}
	e.ObserveProbeEcho(2) // late arrival fills the gap
	if got := e.Estimate().Loss; got != 0 {
		t.Fatalf("post-reorder loss = %.4f, want 0", got)
	}
}

// TestLossMidStreamAttach is the warmup regression: an estimator whose FIRST observed
// echo carries a large ProbeSeq, followed by a contiguous run, reads loss 0 (the
// never-seen prefix below the first observed seq is not charged as loss), not ~1.0.
func TestLossMidStreamAttach(t *testing.T) {
	const start = 100_000
	e := NewEstimator(0)
	for seq := uint64(start); seq < start+2000; seq++ {
		e.ObserveProbeEcho(seq)
	}
	if got := e.Estimate().Loss; got != 0 {
		t.Fatalf("mid-stream-attach loss = %.4f, want 0 (prefix before first seq must not count)", got)
	}
}

// TestLossMidStreamAttachClamp pins the first-observed clamp regime of the loss
// denominator: when the first RECEIVED echo lands at seq > 0 (the initial probes
// dropped — ObserveProbeEcho only ever sees received echoes), the window's lower
// bound is clamped to the first observed seq, so the denominator is highest-first+1
// exactly — including the single-echo n=1 case, where an off-by-one denominator of 0
// would make the fraction ill-defined (0/0) — until the window bound takes over.
func TestLossMidStreamAttachClamp(t *testing.T) {
	const win = 8
	const first = uint64(100) // echoes for seqs 0..99 lost; first received echo
	e := NewEstimator(win)
	e.ObserveProbeEcho(first)
	if got := e.Estimate().Loss; got != 0 {
		t.Fatalf("single echo at seq %d: Loss = %v, want 0 (must be well-defined)", first, got)
	}
	// Clamp regime: one missing echo right after the first reads as 1 over
	// highest-first+1 while that span is within the window.
	for seq := first + 2; seq < first+win; seq++ {
		e.ObserveProbeEcho(seq)
		want := 1 / float64(seq-first+1)
		if got := e.Estimate().Loss; got != want {
			t.Fatalf("after seq %d (clamp regime): Loss = %v, want %v", seq, got, want)
		}
	}
	// Past the clamp regime the window bound takes over and the gap slides out.
	for seq := first + win; seq < first+win*3; seq++ {
		e.ObserveProbeEcho(seq)
	}
	if got := e.Estimate().Loss; got != 0 {
		t.Fatalf("saturated window after the gap slid out: Loss = %v, want 0", got)
	}
}

// TestSingleDropFractionScalesWithDenominator: a single dropped probe at a SMALL
// window denominator (n=8, early regime) reads as a large loss FRACTION (1/8), while
// the IDENTICAL single drop diluted across a saturated 512-sample window reads as
// 1/512.
func TestSingleDropFractionScalesWithDenominator(t *testing.T) {
	const dropSeq = 3

	small := NewEstimator(512)
	for seq := uint64(0); seq < 8; seq++ {
		if seq == dropSeq {
			continue // this probe's echo is dropped: never observed
		}
		small.ObserveProbeEcho(seq)
	}
	if got, want := small.Estimate().Loss, 1.0/8; got != want {
		t.Fatalf("small-sample Loss = %v, want exactly %v (1 drop / 8 samples)", got, want)
	}

	full := NewEstimator(512)
	for seq := uint64(0); seq < 512; seq++ {
		if seq == dropSeq {
			continue // the identical single drop, now at the saturated denominator
		}
		full.ObserveProbeEcho(seq)
	}
	if got, want := full.Estimate().Loss, 1.0/512; got != want {
		t.Fatalf("saturated Loss = %v, want exactly %v (1 drop / 512 samples)", got, want)
	}
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}
