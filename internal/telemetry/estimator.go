package telemetry

import (
	"sync"
	"time"
)

// Smoothing constants for the RTT/jitter estimator. They mirror the RFC 6298
// SRTT/RTTVAR recursion (alpha = 1/8, beta = 1/4): srtt tracks the smoothed
// round-trip time and rttvar the smoothed absolute deviation of samples from it,
// which is the jitter this codec reports.
const (
	rttAlpha = 0.125
	rttBeta  = 0.25

	// defaultLossWindow is the sequence-space width of the windowed loss estimator
	// (per-path over probe-echo ProbeSeq gaps; connection-scoped over the outer-seq
	// in ConnLoss) when a caller passes a non-positive window.
	defaultLossWindow = 512
)

// Estimate is a point-in-time snapshot of a path's measured quality. RTT,
// Jitter, and Loss all derive from the path's ACTIVE probe stream: RTT/Jitter
// from probe-echo timings, and Loss from gaps in the probe-echo ProbeSeq (NOT
// the connection-global outer DATA sequence — see the Estimator doc below and
// ConnLoss).
type Estimate struct {
	// RTT is the smoothed round-trip time.
	RTT time.Duration
	// Jitter is the smoothed absolute deviation of RTT samples (RFC 6298 RTTVAR).
	Jitter time.Duration
	// Loss is the per-path loss fraction in [0,1] over the current probe-echo
	// ProbeSeq window.
	Loss float64
}

// Estimator fuses per-path quality signals: an EWMA RTT and jitter estimator
// (ObserveRTT) and a windowed per-path loss estimator, BOTH fed by the path's
// active probe stream. Per-path loss is derived from gaps in the ProbeSeq of
// received probe echoes (ObserveProbeEcho), NOT from the outer DATA sequence:
// the outer-seq is connection-global — a single sequence the send scheduler
// stripes across all paths (T12) and the receiver resequences into one global
// order (T18) — so counting a single path's outer-seq gaps would read scheduler
// striping (and a mid-stream path attach) as loss. Connection-scoped outer-seq
// loss is a separate, correctly-scoped metric: see ConnLoss.
//
// Estimator holds no clock, does no I/O, and is NOT safe for concurrent use; its
// owner (Prober) serializes access. It is exercised directly on synthetic traces.
type Estimator struct {
	haveRTT bool
	srtt    float64 // nanoseconds
	rttvar  float64 // nanoseconds
	loss    *lossWindow
}

// NewEstimator builds an Estimator whose per-path loss estimate is computed over
// a trailing window of lossWindow probe echoes (defaultLossWindow when
// non-positive).
func NewEstimator(lossWindow int) *Estimator {
	return &Estimator{loss: newLossWindow(lossWindow)}
}

// ObserveRTT folds one round-trip-time sample into the smoothed RTT and jitter.
// The first sample seeds srtt directly and rttvar at half the sample, per RFC
// 6298.
func (e *Estimator) ObserveRTT(sample time.Duration) {
	s := float64(sample)
	if !e.haveRTT {
		e.srtt = s
		e.rttvar = s / 2
		e.haveRTT = true
		return
	}
	d := e.srtt - s
	if d < 0 {
		d = -d
	}
	e.rttvar = (1-rttBeta)*e.rttvar + rttBeta*d
	e.srtt = (1-rttAlpha)*e.srtt + rttAlpha*s
}

// ObserveProbeEcho folds one received probe echo's ProbeSeq into the per-path
// loss estimate. The prober assigns ProbeSeq densely (0,1,2,…) on this path, so a
// gap in received echo seqs is a probe that did not round-trip — i.e. per-path
// loss. Echoes may arrive out of order; a late arrival within the window
// retroactively fills its gap, and a duplicate is idempotent.
func (e *Estimator) ObserveProbeEcho(seq uint64) {
	e.loss.observe(seq)
}

// Estimate returns the current fused snapshot.
func (e *Estimator) Estimate() Estimate {
	return Estimate{
		RTT:    time.Duration(e.srtt),
		Jitter: time.Duration(e.rttvar),
		Loss:   e.loss.fraction(),
	}
}

// lossWindow is a sliding-window loss estimator over a monotonic sequence space.
// It tracks, for the trailing win sequence numbers ending at the highest seen,
// which were received; loss is the unfilled fraction of that window. It never
// inspects payloads — only sequence presence — so a burst of missing sequence
// numbers reads as loss without any per-packet state beyond the ring.
//
// The window is bounded below by the FIRST observed sequence, not by zero, so a
// stream that starts (or a receiver that attaches) mid-sequence does not count
// the never-seen prefix as loss.
type lossWindow struct {
	win     int
	recv    []bool
	first   uint64
	highest uint64
	have    bool
}

func newLossWindow(win int) *lossWindow {
	if win <= 0 {
		win = defaultLossWindow
	}
	return &lossWindow{win: win, recv: make([]bool, win)}
}

func (w *lossWindow) slot(seq uint64) int { return int(seq % uint64(w.win)) }

func (w *lossWindow) observe(seq uint64) {
	if !w.have {
		w.have = true
		w.first = seq
		w.highest = seq
		for i := range w.recv {
			w.recv[i] = false
		}
		w.recv[w.slot(seq)] = true
		return
	}
	if seq > w.highest {
		// The positions (highest, seq] are newly exposed by the window advancing;
		// mark them missing, then record the one that actually arrived.
		if seq-w.highest >= uint64(w.win) {
			for i := range w.recv {
				w.recv[i] = false
			}
		} else {
			for k := w.highest + 1; k <= seq; k++ {
				w.recv[w.slot(k)] = false
			}
		}
		w.recv[w.slot(seq)] = true
		w.highest = seq
		return
	}
	// Late/reordered arrival: fill its slot only if it still falls in the window.
	if w.highest-seq < uint64(w.win) {
		w.recv[w.slot(seq)] = true
	}
}

func (w *lossWindow) fraction() float64 {
	if !w.have {
		return 0
	}
	lower := uint64(0)
	if w.highest+1 > uint64(w.win) {
		lower = w.highest + 1 - uint64(w.win)
	}
	if lower < w.first {
		lower = w.first // never charge loss for sequences before the first observed
	}
	n := w.highest - lower + 1
	missing := 0
	for k := lower; k <= w.highest; k++ {
		if !w.recv[w.slot(k)] {
			missing++
		}
	}
	return float64(missing) / float64(n)
}

// ConnLoss estimates CONNECTION-SCOPED loss from the connection-global outer-seq
// DATA stream. The outer-seq is a single sequence the send scheduler stripes
// across every path (T12) and the receiver resequences into one global order
// (T18), so this is explicitly NOT a per-path metric: feeding it a single path's
// frames would read scheduler striping as loss. Feed it EVERY received DATA
// frame's OuterSeq regardless of which path delivered it. For per-path loss use
// Estimator/Prober, which measures the active probe stream instead.
//
// ConnLoss is safe for concurrent use by the per-path receive goroutines.
type ConnLoss struct {
	mu sync.Mutex
	w  *lossWindow
}

// NewConnLoss builds a ConnLoss over a trailing window of window outer-sequence
// numbers (defaultLossWindow when non-positive).
func NewConnLoss(window int) *ConnLoss {
	return &ConnLoss{w: newLossWindow(window)}
}

// Observe folds one received DATA frame's connection-global OuterSeq into the
// loss estimate.
func (c *ConnLoss) Observe(outerSeq uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.w.observe(outerSeq)
}

// Loss returns the current connection loss fraction in [0,1].
func (c *ConnLoss) Loss() float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.w.fraction()
}
