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

// TestPassiveLossConvergence feeds an in-order DATA sequence with a deterministic
// fraction of drops and asserts the windowed loss estimate converges to the
// injected rate. Loss is measured purely from outer-seq gaps — no payload
// inspection.
func TestPassiveLossConvergence(t *testing.T) {
	cases := []struct {
		name     string
		dropMod  uint64 // drop seq where seq%dropMod == 0
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
					continue // dropped: never observed
				}
				e.ObserveDataSeq(seq)
			}
			got := e.Estimate().Loss
			if math.Abs(got-tc.wantLoss) > tolerance {
				t.Fatalf("loss = %.4f, want %.4f ± %.2f", got, tc.wantLoss, tolerance)
			}
		})
	}
}

// TestLossWindowReordering verifies a late (reordered) arrival within the window
// retroactively clears its gap, so transient reordering is not mistaken for loss.
func TestLossWindowReordering(t *testing.T) {
	e := NewEstimator(8)
	// Observe 0,1,3,4,5,6,7 (2 arrives late); highest = 7, window = 8.
	for _, seq := range []uint64{0, 1, 3, 4, 5, 6, 7} {
		e.ObserveDataSeq(seq)
	}
	if got := e.Estimate().Loss; math.Abs(got-1.0/8.0) > 1e-9 {
		t.Fatalf("pre-reorder loss = %.4f, want %.4f", got, 1.0/8.0)
	}
	e.ObserveDataSeq(2) // late arrival fills the gap
	if got := e.Estimate().Loss; got != 0 {
		t.Fatalf("post-reorder loss = %.4f, want 0", got)
	}
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}
