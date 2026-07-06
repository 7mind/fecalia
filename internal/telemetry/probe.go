package telemetry

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/frame"
	"github.com/7mind/wanbond/internal/log"
)

// ErrReplay is returned when a probe's ProbeSeq is at or below the per-path
// high-water mark — a replayed or stale probe (defect D4). It is distinct from
// frame.ErrAuth (a forged/tampered frame that fails the PSK HMAC): ErrAuth
// rejects frames an attacker fabricated, ErrReplay rejects genuine frames an
// attacker (or the network) delivered a second time.
var ErrReplay = errors.New("telemetry: probe sequence replayed or stale")

// ErrPathMismatch is returned when an echo's PathID does not match the Prober's
// path — an echo that arrived on (or was steered to) the wrong path must not be
// counted as this path's heartbeat, or a single live path could mask every other
// path's death.
var ErrPathMismatch = errors.New("telemetry: probe PathID does not match this path")

// AntiReplay is a monotonic ProbeSeq high-water filter. Accept advances the mark
// and returns true only for a strictly increasing sequence, so a captured probe
// replayed later — or reordered behind a newer one — is rejected. Probes are
// periodic and their ProbeSeq is assigned monotonically PER PATH, so strict
// monotonicity costs no legitimate probe while closing the replay window (D4). It
// is not internally synchronized; its owner serializes access.
type AntiReplay struct {
	high uint64
	have bool
}

// Accept reports whether seq is fresh (strictly greater than every prior
// accepted seq) and, if so, advances the high-water mark.
func (a *AntiReplay) Accept(seq uint64) bool {
	if a.have && seq <= a.high {
		return false
	}
	a.high = seq
	a.have = true
	return true
}

// HighWater returns the current high-water mark and whether any seq was accepted.
func (a *AntiReplay) HighWater() (uint64, bool) { return a.high, a.have }

// ProberConfig configures a Prober: the per-path probe-echo loss window width and
// the liveness detection thresholds.
type ProberConfig struct {
	LossWindow int
	Liveness   LivenessConfig
}

// Prober is the initiator side of one path's probe exchange. It emits
// authenticated probes (SendProbe), consumes their authenticated echoes
// (HandleEcho) to measure RTT/jitter/per-path-loss and drive liveness. It holds
// an injectable Clock and speaks only in probe-frame bytes, so it is unit-testable
// against synthetic echo traces with no real network.
//
// Concurrency: T12 drives HandleEcho from a per-path receive goroutine and
// SendProbe/Tick from a timer goroutine. All exported methods take an internal
// mutex, so a *Prober is safe for concurrent use; the estimator, liveness machine,
// and anti-replay filter it owns are not independently synchronized and must be
// reached only through the Prober.
type Prober struct {
	pathName string
	pathID   uint8
	psk      config.Key
	clock    Clock

	mu      sync.Mutex
	nextSeq uint64
	guard   AntiReplay
	est     *Estimator
	live    *Liveness
}

// NewProber builds a Prober for one path. The logger is used (path-tagged) for
// liveness transition logging.
func NewProber(
	pathName string,
	pathID uint8,
	psk config.Key,
	cfg ProberConfig,
	clock Clock,
	logger log.Logger,
) *Prober {
	return &Prober{
		pathName: pathName,
		pathID:   pathID,
		psk:      psk,
		clock:    clock,
		est:      NewEstimator(cfg.LossWindow),
		live:     NewLiveness(pathName, cfg.Liveness, clock, logger),
	}
}

// SendProbe builds and encodes the next authenticated probe frame, stamping it
// with the current clock time so the echo yields a round-trip sample. The
// returned bytes are ready for the wire.
func (p *Prober) SendProbe() ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	probe := frame.Probe{
		PathID:         p.pathID,
		ProbeSeq:       p.nextSeq,
		TimestampNanos: p.clock.Now().UnixNano(),
	}
	p.nextSeq++
	return frame.Encode(p.psk, probe)
}

// HandleEcho processes one received echo frame. It rejects, without touching
// liveness or the RTT estimate: forged/tampered echoes (frame.Decode fails the
// HMAC -> ErrAuth), non-probe frames, echoes for another path (ErrPathMismatch),
// and replayed/stale ProbeSeq (ErrReplay, defect D4). A genuine echo's ProbeSeq
// is folded into the per-path loss estimate whether or not it is fresh (a
// duplicate is idempotent, a reorder fills its gap), but only a FRESH echo yields
// a new RTT sample and a liveness heartbeat.
func (p *Prober) HandleEcho(raw []byte) error {
	f, err := frame.Decode(p.psk, raw)
	if err != nil {
		return err
	}
	probe, ok := f.(frame.Probe)
	if !ok {
		return fmt.Errorf("telemetry: expected probe echo, got frame kind %d", f.Kind())
	}
	if probe.PathID != p.pathID {
		return ErrPathMismatch
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	// Loss accounting sees every genuine echo of this path: a duplicate re-marks an
	// already-received slot (idempotent) and a reordered echo fills its gap.
	p.est.ObserveProbeEcho(probe.ProbeSeq)

	if !p.guard.Accept(probe.ProbeSeq) {
		return ErrReplay
	}
	rtt := p.clock.Now().Sub(time.Unix(0, probe.TimestampNanos))
	if rtt < 0 {
		// A negative sample can only come from clock skew between send and receive;
		// clamp rather than poison the EWMA with a negative RTT.
		rtt = 0
	}
	p.est.ObserveRTT(rtt)
	p.live.RecordEcho()
	return nil
}

// Tick advances the liveness state machine against the clock; call it at least
// once per probe interval.
func (p *Prober) Tick() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.live.Tick()
}

// Estimate returns the current per-path quality snapshot.
func (p *Prober) Estimate() Estimate {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.est.Estimate()
}

// State returns the current liveness verdict.
func (p *Prober) State() PathState {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.live.State()
}

// Reflector is the responder side of the probe exchange. Reflect authenticates
// an inbound probe, rejects replays per originating path (D4), and returns a fresh
// authenticated encoding of the same probe (verbatim ProbeSeq and TimestampNanos)
// so the initiator can compute the round-trip from its own send timestamp. The
// responder never manufactures a timestamp, so it needs no synchronized clock.
//
// A single Reflector serves EVERY path, so its anti-replay high-water is keyed by
// PathID: each path carries its own ProbeSeq space (both start at 0), and sharing
// one high-water would reject a second path's opening probes as replays.
//
// Concurrency: Reflect may be called from all per-path receive goroutines at
// once; it is guarded by an internal mutex.
type Reflector struct {
	psk config.Key

	mu     sync.Mutex
	guards map[uint8]*AntiReplay
}

// NewReflector builds a Reflector authenticating under psk.
func NewReflector(psk config.Key) *Reflector {
	return &Reflector{psk: psk, guards: make(map[uint8]*AntiReplay)}
}

// Reflect decodes and re-encodes one probe as its echo. It returns frame.ErrAuth
// for a forged/tampered probe, an error for a non-probe frame, and ErrReplay for
// a ProbeSeq replayed/stale on its originating path.
func (r *Reflector) Reflect(raw []byte) ([]byte, error) {
	f, err := frame.Decode(r.psk, raw)
	if err != nil {
		return nil, err
	}
	probe, ok := f.(frame.Probe)
	if !ok {
		return nil, fmt.Errorf("telemetry: expected probe, got frame kind %d", f.Kind())
	}

	r.mu.Lock()
	guard := r.guards[probe.PathID]
	if guard == nil {
		guard = &AntiReplay{}
		r.guards[probe.PathID] = guard
	}
	fresh := guard.Accept(probe.ProbeSeq)
	r.mu.Unlock()

	if !fresh {
		return nil, ErrReplay
	}
	return frame.Encode(r.psk, probe)
}
