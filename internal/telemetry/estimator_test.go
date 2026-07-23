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

// TestConnLossGlobalStream asserts the connection-scoped estimator, fed the
// contiguous global outer-seq, converges to the injected connection loss.
func TestConnLossGlobalStream(t *testing.T) {
	const (
		total     = 6000
		dropMod   = 10 // 10% connection loss
		wantLoss  = 0.10
		tolerance = 0.02
	)
	c := NewConnLoss(0)
	for seq := uint64(0); seq < total; seq++ {
		if seq%dropMod == 0 {
			continue
		}
		c.Observe(seq)
	}
	if got := c.Loss(); math.Abs(got-wantLoss) > tolerance {
		t.Fatalf("connection loss = %.4f, want %.4f ± %.2f", got, wantLoss, tolerance)
	}
}

// TestConnLossMidStreamAttach is the warmup regression: a receiver that attaches
// mid-stream sees its FIRST observed outer-seq at a large value, then a contiguous
// run. Loss must be ~0 (the never-seen prefix below the first observed seq is not
// charged as loss), not ~1.0.
func TestConnLossMidStreamAttach(t *testing.T) {
	const start = 100_000
	c := NewConnLoss(0)
	for seq := uint64(start); seq < start+2000; seq++ {
		c.Observe(seq)
	}
	if got := c.Loss(); got != 0 {
		t.Fatalf("mid-stream-attach loss = %.4f, want 0 (prefix before first seq must not count)", got)
	}
}

// TestConnLossMisusedPerPathReadsStriping documents WHY per-path loss must not
// use the outer-seq: feeding the connection estimator a single path's strided
// subset (every other seq) reads ~50% loss. This is expected — ConnLoss is
// connection-scoped and must be fed the global stream — and is the exact failure
// that moving per-path loss to the probe stream avoids.
func TestConnLossMisusedPerPathReadsStriping(t *testing.T) {
	c := NewConnLoss(0)
	for seq := uint64(0); seq < 8000; seq += 2 { // this path carries only even seqs
		c.Observe(seq)
	}
	got := c.Loss()
	if math.Abs(got-0.5) > 0.02 {
		t.Fatalf("strided-subset loss = %.4f, want ~0.5 (documents connection scope)", got)
	}
}

// TestLossSamplesGrowsThenSaturates asserts Estimate().LossSamples tracks the
// loss window's denominator: it grows 1..win as echoes arrive on a fresh
// window (early regime, window not yet full) and then holds at win once the
// window saturates. This is the denominator the min-sample floor (D96) gates
// on, so a single drop at a small n (e.g. 1/11) is not conflated with the same
// drop at a saturated n (e.g. 1/512).
func TestLossSamplesGrowsThenSaturates(t *testing.T) {
	const win = 8
	e := NewEstimator(win)
	if got := e.Estimate().LossSamples; got != 0 {
		t.Fatalf("LossSamples before any echo = %d, want 0", got)
	}
	for seq := uint64(0); seq < win; seq++ {
		e.ObserveProbeEcho(seq)
		want := int(seq) + 1 // early regime: denominator grows 1..win
		if got := e.Estimate().LossSamples; got != want {
			t.Fatalf("after seq %d: LossSamples = %d, want %d", seq, got, want)
		}
	}
	// Window now saturated; further echoes must hold the denominator at win.
	for seq := uint64(win); seq < win*4; seq++ {
		e.ObserveProbeEcho(seq)
		if got := e.Estimate().LossSamples; got != win {
			t.Fatalf("after seq %d (saturated): LossSamples = %d, want %d", seq, got, win)
		}
	}
}

// TestLossSamplesMidStreamAttachClamp pins the first-observed clamp regime of
// the denominator: when the first RECEIVED echo lands at seq > 0 (the initial
// probes dropped — ObserveProbeEcho only ever sees received echoes), the
// window's lower bound is clamped to the first observed seq, and LossSamples
// must equal highest-first+1 exactly — including the single-echo n=1 case,
// where an off-by-one denominator of 0 would make fraction() ill-defined
// (0/0). Kills the `lower = w.first + 1` mutant, which
// TestLossSamplesGrowsThenSaturates (seq starts at 0, clamp never binds)
// cannot detect.
func TestLossSamplesMidStreamAttachClamp(t *testing.T) {
	const win = 8
	const first = uint64(100) // echoes for seqs 0..99 lost; first received echo
	e := NewEstimator(win)
	// Single echo at seq=first: n must be exactly 1 (not 0), and the loss
	// fraction over that one received sample must be a well-defined 0.
	e.ObserveProbeEcho(first)
	est := e.Estimate()
	if est.LossSamples != 1 {
		t.Fatalf("single echo at seq %d: LossSamples = %d, want 1", first, est.LossSamples)
	}
	if est.Loss != 0 {
		t.Fatalf("single echo at seq %d: Loss = %v, want 0 (must be well-defined)", first, est.Loss)
	}
	// Clamp regime: while highest-first+1 <= win the lower bound is first, so
	// the denominator must equal highest-first+1 exactly.
	for seq := first + 1; seq < first+win; seq++ {
		e.ObserveProbeEcho(seq)
		want := int(seq-first) + 1
		if got := e.Estimate().LossSamples; got != want {
			t.Fatalf("after seq %d (clamp regime): LossSamples = %d, want %d", seq, got, want)
		}
	}
	// Past the clamp regime the window bound takes over: denominator holds at win.
	for seq := first + win; seq < first+win*3; seq++ {
		e.ObserveProbeEcho(seq)
		if got := e.Estimate().LossSamples; got != win {
			t.Fatalf("after seq %d (saturated): LossSamples = %d, want %d", seq, got, win)
		}
	}
}

// TestSingleDropSmallSampleReadsLargeFraction is the D96 mechanism-3 / E4 telemetry-side
// oracle: it pins the DENOMINATOR PRECONDITION the adaptive-FEC min-sample floor
// (internal/bind.minAdaptiveLossSamples, 32) exists to guard against. A single dropped
// probe at a SMALL window denominator (n=8, early regime, well below the floor) reads as a
// large loss FRACTION (1/8 = 12.5%) — indistinguishable, from Loss alone, from a
// genuinely lossy path, even though only one probe was ever lost. The IDENTICAL single
// drop, diluted across a saturated 512-sample window (the telemetry default), reads as a
// small, trustworthy fraction (1/512 ~= 0.195%). Estimate() exposes LossSamples precisely
// so a caller can tell these two cases apart from Loss alone; this test pins the exact
// numbers the floor is calibrated against — a single drop CANNOT, by itself, cross any
// reasonable raise gate once the denominator has grown past the floor.
func TestSingleDropSmallSampleReadsLargeFraction(t *testing.T) {
	const floor = 32 // mirrors internal/bind.minAdaptiveLossSamples (D96 mechanism 3)
	const dropSeq = 3

	// n=8: one drop among the first 8 probes — early regime, well below the floor.
	small := NewEstimator(512)
	for seq := uint64(0); seq < 8; seq++ {
		if seq == dropSeq {
			continue // this probe's echo is dropped: never observed
		}
		small.ObserveProbeEcho(seq)
	}
	smallEst := small.Estimate()
	if smallEst.LossSamples != 8 {
		t.Fatalf("small-sample LossSamples = %d, want 8", smallEst.LossSamples)
	}
	if smallEst.LossSamples >= floor {
		t.Fatalf("setup: LossSamples = %d, want < floor %d (early regime)", smallEst.LossSamples, floor)
	}
	if got, want := smallEst.Loss, 1.0/8; got != want {
		t.Fatalf("small-sample Loss = %v, want exactly %v (1 drop / 8 samples)", got, want)
	}

	// n=512 (saturated default window): the SAME single drop, diluted across a full window,
	// reads as a small fraction — the precondition the floor exists to exploit.
	full := NewEstimator(512)
	for seq := uint64(0); seq < 512; seq++ {
		if seq == dropSeq {
			continue // the identical single drop, now at the saturated denominator
		}
		full.ObserveProbeEcho(seq)
	}
	fullEst := full.Estimate()
	if fullEst.LossSamples != 512 {
		t.Fatalf("saturated LossSamples = %d, want 512", fullEst.LossSamples)
	}
	if got, want := fullEst.Loss, 1.0/512; got != want {
		t.Fatalf("saturated Loss = %v, want exactly %v (1 drop / 512 samples)", got, want)
	}

	// The precondition itself: an IDENTICAL drop count (1) reads as a wildly different
	// FRACTION purely as a function of the denominator — exactly what a caller must gate on
	// (LossSamples), rather than trusting Loss alone, to avoid a small-sample spike crossing
	// a raise gate that a saturated-window reading of the same single drop never would.
	if !(smallEst.Loss > 10*fullEst.Loss) {
		t.Fatalf("small-sample fraction %.4f is not >> saturated fraction %.4f; the denominator precondition is not exercised", smallEst.Loss, fullEst.Loss)
	}
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}
