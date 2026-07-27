package device

import (
	"sync"
	"time"
)

type outboundAdmissionReservation struct {
	admission *outboundAdmission
	bytes     int64
}

type outboundAdmissionSnapshot struct {
	limitBytes     int64
	retainedBytes  int64
	highWaterBytes int64
	waits          uint64
	waitDuration   time.Duration
	oversize       uint64
}

type outboundAdmission struct {
	mu              sync.Mutex
	changed         chan struct{}
	running         bool
	generation      uint64
	limit           int64
	retained        int64
	highWater       int64
	waits           uint64
	waitDuration    time.Duration
	oversizeBatches uint64
}

func newOutboundAdmission(limit int64) *outboundAdmission {
	if limit < 0 {
		panic("device: negative outbound admission limit")
	}
	return &outboundAdmission{limit: limit, changed: make(chan struct{})}
}

func (a *outboundAdmission) start() {
	a.mu.Lock()
	a.running = true
	a.generation++
	a.broadcastLocked()
	a.mu.Unlock()
}

func (a *outboundAdmission) stop() {
	a.mu.Lock()
	a.running = false
	a.generation++
	a.broadcastLocked()
	a.mu.Unlock()
}

func (a *outboundAdmission) setLimit(limit int64) {
	if limit <= 0 {
		panic("device: outbound admission limit must be positive")
	}
	a.mu.Lock()
	a.limit = limit
	a.broadcastLocked()
	a.mu.Unlock()
}

func (a *outboundAdmission) reserve(bytes int64) (*outboundAdmissionReservation, bool) {
	if bytes <= 0 {
		panic("device: outbound admission reservation must be positive")
	}
	a.mu.Lock()
	if !a.running {
		a.mu.Unlock()
		return nil, false
	}
	if a.limit == 0 {
		a.mu.Unlock()
		return nil, true
	}
	generation := a.generation
	waitStarted := time.Time{}
	for a.retained != 0 && a.retained+bytes > a.limit {
		if waitStarted.IsZero() {
			waitStarted = time.Now()
			a.waits++
		}
		changed := a.changed
		a.mu.Unlock()
		<-changed
		a.mu.Lock()
		if !a.running || a.generation != generation {
			if !waitStarted.IsZero() {
				a.waitDuration += time.Since(waitStarted)
			}
			a.mu.Unlock()
			return nil, false
		}
	}
	if !waitStarted.IsZero() {
		a.waitDuration += time.Since(waitStarted)
	}
	a.retained += bytes
	if a.retained > a.highWater {
		a.highWater = a.retained
	}
	if bytes > a.limit {
		a.oversizeBatches++
	}
	a.mu.Unlock()
	return &outboundAdmissionReservation{admission: a, bytes: bytes}, true
}

func (r *outboundAdmissionReservation) release() {
	if r == nil || r.admission == nil || r.bytes <= 0 {
		panic("device: invalid outbound admission release")
	}
	a := r.admission
	a.mu.Lock()
	a.retained -= r.bytes
	if a.retained < 0 {
		panic("device: negative outbound admission retention")
	}
	r.admission = nil
	r.bytes = 0
	a.broadcastLocked()
	a.mu.Unlock()
}

func (a *outboundAdmission) broadcastLocked() {
	close(a.changed)
	a.changed = make(chan struct{})
}

func (a *outboundAdmission) snapshot() outboundAdmissionSnapshot {
	a.mu.Lock()
	defer a.mu.Unlock()
	return outboundAdmissionSnapshot{
		limitBytes:     a.limit,
		retainedBytes:  a.retained,
		highWaterBytes: a.highWater,
		waits:          a.waits,
		waitDuration:   a.waitDuration,
		oversize:       a.oversizeBatches,
	}
}
