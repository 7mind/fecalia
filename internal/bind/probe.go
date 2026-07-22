package bind

import (
	"sync"
	"time"

	"github.com/7mind/wanbond/internal/reseq"
	"github.com/7mind/wanbond/internal/sched"
	"github.com/7mind/wanbond/internal/telemetry"
)

// emitProbes performs one probe cadence step: for every currently-open path it
// emits one authenticated PROBE frame (IsEcho=false) to that path's learned/
// configured remote, then Ticks that path's Prober so liveness advances against
// the injected clock. A path without a known remote yet is still Ticked (so a
// silent path is detected Down) but nothing is sent — there is nowhere to send.
//
// Concurrency mirrors Send: the path/prober set is snapshotted under m.mu, then
// released before any socket I/O, so emission neither holds the lock across a
// syscall nor blocks the lock-free receive fast path. A concurrent Close closes
// the sockets out from under the snapshot; the resulting write error is benign
// (teardown) and count-and-continue (defect D96 item 4): it is tallied into the
// path's probeSendErrors counter (wanbond_path_probe_send_errors_total) rather than
// silently dropped, then probing proceeds exactly as before — no behaviour change.
// It is a no-op when the bind has no probers or is closed.
func (m *Multipath) emitProbes() {
	m.mu.Lock()
	if len(m.paths) == 0 || m.probers == nil {
		m.mu.Unlock()
		return
	}
	type target struct {
		ps *peerPathState
		pr *telemetry.Prober
		// budget is this peer's scheduler as a ProbeBudget (nil when it carries no pacing
		// headroom, e.g. pacing disabled or a non-implementing scheduler); idx is ps's
		// scheduler index (its position in p.paths). Captured under m.mu so the snapshot is
		// coherent, then used lock-free below to charge the probe's pacing token (T145).
		budget sched.ProbeBudget
		idx    int
	}
	// Probe EVERY bound peer's paths (T93): a concentrator initiates its own probe stream to
	// each edge over that edge-peer's per-(peer,path) prober, so every peer's liveness/RTT is
	// measured for its OWN scheduler. On the single-peer edge/hub m.peers holds only the
	// primary, so this is byte-identical to the pre-split single-peer sweep.
	// holdUpdate carries one peer's resequencer plus its paths' probers so the
	// RTT-adaptive per-gap hold (T241, D93) can be refreshed lock-free below at
	// this probe cadence — the natural per-path telemetry consult point, never the
	// per-datagram hot path.
	type holdUpdate struct {
		rq  *reseq.Resequencer
		prs []*telemetry.Prober
	}
	targets := make([]target, 0, len(m.paths))
	holds := make([]holdUpdate, 0, len(m.peers))
	for _, p := range m.peers {
		budget, _ := p.scheduler.(sched.ProbeBudget)
		for i, ps := range p.paths {
			if ps.prober == nil {
				continue
			}
			targets = append(targets, target{ps: ps, pr: ps.prober, budget: budget, idx: i})
		}
		if rq := p.resequencer.Load(); rq != nil {
			prs := make([]*telemetry.Prober, 0, len(p.paths))
			for _, ps := range p.paths {
				if ps.prober != nil {
					prs = append(prs, ps.prober)
				}
			}
			if len(prs) > 0 {
				holds = append(holds, holdUpdate{rq: rq, prs: prs})
			}
		}
	}
	m.mu.Unlock()

	// Refresh each peer's dynamic per-gap hold from its paths' measured RTT (T241,
	// D93): the MAX smoothed RTT across the peer's probed paths bounds the
	// cross-path reorder horizon (a straggler can trail its head by at most about
	// the slowest path's RTT), so hold = holdBoundRTTMultiple x maxSRTT, clamped by
	// the resequencer to [its floor, resequencerTimeout]. Paths with no RTT sample
	// yet contribute nothing; with no sample at all the bound is left unset and the
	// resequencer keeps the full fixed hold (conservative).
	for _, h := range holds {
		var maxRTT time.Duration
		for _, pr := range h.prs {
			if rtt := pr.Estimate().RTT; rtt > maxRTT {
				maxRTT = rtt
			}
		}
		if maxRTT > 0 {
			h.rq.SetHoldBound(holdBoundRTTMultiple * maxRTT)
		}
	}

	now := time.Now()
	for _, t := range targets {
		// One-time sticky DEAD fallback for the selected downlink destination (T246,
		// defect D94), evaluated at probe cadence — never the per-datagram hot path.
		t.ps.checkRemoteDead(now)
		if remote, ok := t.ps.getRemote(); ok {
			if raw, err := t.pr.SendProbe(); err == nil {
				// UDP writes are goroutine-safe; this races no in-flight Send.
				if _, werr := t.ps.conn.WriteToUDPAddrPort(raw, remote); werr == nil {
					// True-wire-volume accounting (D48): a PROBE frame is real egress
					// traffic, so it counts toward txBytes exactly like a DATA/PARITY
					// write — only on a nil write error, matching the Send hot path.
					t.ps.txBytes.Add(uint64(len(raw)))
					// Exempt-but-charged probe accounting (T145): a PROBE frame egresses
					// OUTSIDE the paced Send->Pick path, so it is never shed or delayed, but
					// it IS charged against the path's token bucket so paced ClassData yields
					// the headroom the probe stream consumes — otherwise DATA + probes jointly
					// oversubscribe a pace sized at ~link rate and starve probes into a
					// spurious path-DOWN. No-op when the scheduler carries no pacing headroom.
					if t.budget != nil {
						t.budget.AccountProbe(t.idx)
					}
				} else {
					// The write failed (e.g. a concurrent Close raced the probe-loop
					// goroutine, or a transient socket error): count it so a path whose
					// probes cannot egress is observable at /metrics instead of reading
					// identically to a path with 100% probe loss (defect D96 item 4).
					t.ps.probeSendErrors.Add(1)
				}
			}
		}
		t.pr.Tick()
	}
	// Eager failover nudge (defect D18): recompute the scheduler's active egress path
	// each probe cadence so a liveness DOWN switches egress even when no application
	// Send is driving Pick. This is the timer-driven companion to the receive-tick
	// nudge (see tickLivenessFromReceive) — under CPU starvation this loop's ticker
	// lags, which is why the receive-tick path carries the guarantee, but when the
	// loop does run it keeps the selection fresh independent of egress traffic.
	m.nudgeSchedulerActive()
}

// StartProbeLoop launches the probe cadence goroutine: it calls emitProbes every
// interval until the returned stop function is invoked. The caller (device.Up)
// starts it AFTER the engine has opened the bind and stops it BEFORE Close, so the
// loop only runs while the sockets exist (emitProbes is a safe no-op if it races a
// closed bind). The returned stopper is idempotent.
//
// The cadence uses a wall-clock ticker because it is production timing glue, not
// liveness logic: every liveness decision the loop drives runs through the
// injected telemetry.Clock the Probers hold (SendProbe stamps it, Tick reads it),
// so tests drive emitProbes directly against a fake clock and never start this
// goroutine. It is a no-op (returning a no-op stopper) when the bind has no
// probers or interval <= 0.
func (m *Multipath) StartProbeLoop(interval time.Duration) (stop func()) {
	if m.probers == nil || interval <= 0 {
		return func() {}
	}
	// Arm the receive-path liveness sweep at the same cadence (D15): a receiver may
	// now advance liveness when the timer goroutine below is starved under load.
	m.sweepIntervalNanos.Store(int64(interval))
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				m.emitProbes()
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(done) }) }
}
