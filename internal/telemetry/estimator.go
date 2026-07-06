package telemetry

import "time"

// Smoothing constants for the RTT/jitter estimator. They mirror the RFC 6298
// SRTT/RTTVAR recursion (alpha = 1/8, beta = 1/4): srtt tracks the smoothed
// round-trip time and rttvar the smoothed absolute deviation of samples from it,
// which is the jitter this codec reports.
const (
	rttAlpha = 0.125
	rttBeta  = 0.25

	// defaultLossWindow is the sequence-space width of the passive loss estimator
	// when a caller passes a non-positive window.
	defaultLossWindow = 512
)

// Estimate is a point-in-time snapshot of a path's measured quality. RTT and
// Jitter derive from active probe echoes; Loss derives from passive outer-seq
// gap accounting over the DATA stream.
type Estimate struct {
	// RTT is the smoothed round-trip time.
	RTT time.Duration
	// Jitter is the smoothed absolute deviation of RTT samples (RFC 6298 RTTVAR).
	Jitter time.Duration
	// Loss is the passive loss fraction in [0,1] over the current sequence window.
	Loss float64
}

// Estimator fuses two independent quality signals for one path: an EWMA RTT and
// jitter estimator fed by probe echoes (ObserveRTT), and a windowed loss
// estimator fed by observed DATA outer-sequence numbers (ObserveDataSeq). It
// holds no clock and does no I/O, so it is exercised directly on synthetic
// traces.
type Estimator struct {
	haveRTT bool
	srtt    float64 // nanoseconds
	rttvar  float64 // nanoseconds
	loss    *lossWindow
}

// NewEstimator builds an Estimator whose passive loss estimate is computed over
// a trailing window of lossWindow sequence numbers (defaultLossWindow when
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

// ObserveDataSeq folds one observed DATA outer-sequence number into the passive
// loss estimate. Sequence numbers may arrive out of order; late arrivals within
// the window retroactively fill their gap.
func (e *Estimator) ObserveDataSeq(seq uint64) {
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

// lossWindow is a sliding-window passive loss estimator over a monotonic
// sequence space. It tracks, for the trailing win sequence numbers ending at the
// highest seen, which were received; loss is the unfilled fraction of that
// window. It never inspects payloads — only outer-seq presence — so a burst of
// missing sequence numbers reads as loss without any per-packet state beyond the
// ring.
type lossWindow struct {
	win     int
	recv    []bool
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
	n := uint64(w.win)
	if w.highest+1 < n {
		n = w.highest + 1
	}
	missing := 0
	for k := w.highest + 1 - n; k <= w.highest; k++ {
		if !w.recv[w.slot(k)] {
			missing++
		}
	}
	return float64(missing) / float64(n)
}
