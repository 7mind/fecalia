package bind

import (
	"sync"
	"time"

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
// (teardown) and dropped. It is a no-op when the bind has no probers or is closed.
func (m *Multipath) emitProbes() {
	m.mu.Lock()
	if len(m.paths) == 0 || m.probers == nil {
		m.mu.Unlock()
		return
	}
	type target struct {
		ps *pathState
		pr *telemetry.Prober
	}
	targets := make([]target, 0, len(m.paths))
	for _, ps := range m.paths {
		if ps.prober == nil {
			continue
		}
		targets = append(targets, target{ps: ps, pr: ps.prober})
	}
	m.mu.Unlock()

	for _, t := range targets {
		if remote, ok := t.ps.getRemote(); ok {
			if raw, err := t.pr.SendProbe(); err == nil {
				// UDP writes are goroutine-safe; this races no in-flight Send.
				_, _ = t.ps.conn.WriteToUDPAddrPort(raw, remote)
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
