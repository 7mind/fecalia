package adaptivefec

import (
	"testing"
	"time"
)

// fakeClock is a hand-advanced Clock: the simulation harness advances it one
// synthetic probe interval per loss sample, so every trajectory is deterministic
// and instant (no real sleeps, -race clean).
type fakeClock struct {
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Unix(1_700_000_000, 0)}
}

func (c *fakeClock) Now() time.Time { return c.now }

func (c *fakeClock) advance(d time.Duration) { c.now = c.now.Add(d) }

// simStep is the synthetic per-sample probe interval the harness advances the
// clock by between loss samples. Kept well below RateInterval and Dwell so the
// slew limiter and dwell span several samples (that spread is what the stability
// assertions exercise).
const simStep = 100 * time.Millisecond

// runTrace drives ctrl with one loss sample per element of losses, advancing the
// fake clock by simStep after each Observe, and returns the target parity M
// recorded after every sample. The returned slice has the same length as losses.
func runTrace(ctrl *Controller, clk *fakeClock, losses []float64) []int {
	out := make([]int, len(losses))
	for i, l := range losses {
		out[i] = ctrl.Observe(l)
		clk.advance(simStep)
	}
	return out
}

// countChanges returns the number of indices i>0 where ms[i] != ms[i-1]: the
// number of parity-ratio changes over a trace. It is the flap metric.
func countChanges(ms []int) int {
	n := 0
	for i := 1; i < len(ms); i++ {
		if ms[i] != ms[i-1] {
			n++
		}
	}
	return n
}

// maxOf returns the maximum value in ms (0 for an empty slice).
func maxOf(ms []int) int {
	m := 0
	for _, v := range ms {
		if v > m {
			m = v
		}
	}
	return m
}

// constTrace returns a slice of n samples all equal to loss.
func constTrace(n int, loss float64) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = loss
	}
	return out
}

// squareTrace returns n samples alternating hi, lo, hi, lo, … starting at hi.
func squareTrace(n int, hi, lo float64) []float64 {
	out := make([]float64, n)
	for i := range out {
		if i%2 == 0 {
			out[i] = hi
		} else {
			out[i] = lo
		}
	}
	return out
}

// newDefaultController builds a Controller from DefaultConfig on a fresh fake
// clock, failing the test on a construction error.
func newDefaultController(t *testing.T) (*Controller, *fakeClock) {
	t.Helper()
	clk := newFakeClock()
	ctrl, err := NewController(DefaultConfig(), clk)
	if err != nil {
		t.Fatalf("NewController(DefaultConfig): %v", err)
	}
	return ctrl, clk
}
