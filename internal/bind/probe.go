package bind

import (
	"errors"
	"net/netip"
	"sync"
	"syscall"
	"time"

	"github.com/7mind/wanbond/internal/telemetry"
)

func mapPMTUProbeWriteError(err error) error {
	if errors.Is(err, syscall.EMSGSIZE) {
		return telemetry.ErrProbeTooLarge
	}
	return err
}

func (m *Multipath) emitProbePayload(ps *peerPathState, prober *telemetry.Prober, remote netip.AddrPort, payload []byte) {
	raw, _, err := prober.SendProbePayload(payload)
	if err != nil {
		ps.probeSendErrors.Add(1)
		return
	}
	// UDP writes are goroutine-safe; this races no in-flight Send.
	if _, err := ps.writeToUDPAddrPort(raw, remote); err != nil {
		// The write failed (e.g. a concurrent Close raced the probe-loop
		// goroutine, or a transient socket error): count it so a path whose
		// probes cannot egress is observable at /metrics instead of reading
		// identically to a path with 100% probe loss (defect D96 item 4).
		ps.probeSendErrors.Add(1)
		return
	}
	// True-wire-volume accounting (D48): a PROBE frame is real egress
	// traffic, so it counts toward txBytes — only on a nil write error.
	ps.recordOuterWrite(len(raw))
}

// emitProbes performs one probe cadence step: for every currently-open path it
// emits an authenticated local PROBE frame (IsEcho=false) carrying the peer
// transport's hello to that path's learned/configured remote, then Ticks that
// path's Prober so liveness advances against the injected clock. A pending PMTU
// probe occupies this local slot instead of emitting an ordinary liveness probe,
// but the following slot is always ordinary before another PMTU request can run;
// a reactive echo remains an immediate write outside this cadence. A path without
// a known remote yet is still Ticked (so a silent path is detected Down) but
// nothing is sent — there is nowhere to send.
//
// Concurrency mirrors Send: the path/prober set is snapshotted under m.mu, then
// released before any socket I/O, so emission neither holds the lock across a
// syscall nor blocks the lock-free receive fast path. Unexpected originating-PROBE
// write failures increment wanbond_path_probe_send_errors_total. An ordinary
// failure is then discarded so the cadence continues across other paths; a PMTU
// failure is returned to discovery. Expected PMTU EMSGSIZE is excluded from the
// counter and becomes discovery's too-large verdict. It is a no-op when the bind
// is closed.
func (m *Multipath) emitProbes() {
	m.mu.Lock()
	if len(m.paths) == 0 {
		m.mu.Unlock()
		return
	}
	type target struct {
		ps   *peerPathState
		peer *peerState
	}
	// Probe EVERY bound peer's paths (T93): a concentrator initiates its own probe stream to
	// each edge over that edge-peer's per-(peer,path) prober, so every peer's liveness/RTT is
	// measured on its own.
	targets := make([]target, 0, len(m.paths))
	for _, p := range m.peers {
		for _, ps := range p.paths {
			targets = append(targets, target{ps: ps, peer: p})
		}
	}
	m.mu.Unlock()

	now := time.Now()
	for _, t := range targets {
		// One-time sticky DEAD fallback for the selected destination (T246,
		// defect D94), evaluated at probe cadence — never the per-datagram hot path.
		t.ps.checkRemoteDead(now)
		remote, hasRemote := t.ps.getRemote()
		if request := t.ps.takePMTUProbe(); request != nil {
			request.done <- request.work()
		} else if hasRemote {
			var hello []byte
			if adaptive := t.peer.adaptive.Load(); adaptive != nil {
				hello = adaptive.hello(t.ps.id)
			}
			m.emitProbePayload(t.ps, t.ps.prober, remote, hello)
		}
		t.ps.prober.Tick()
	}
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
// goroutine. It is a no-op (returning a no-op stopper) when interval <= 0.
func (m *Multipath) StartProbeLoop(interval time.Duration) (stop func()) {
	if interval <= 0 {
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
