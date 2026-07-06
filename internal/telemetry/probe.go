package telemetry

import (
	"errors"
	"fmt"
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

// AntiReplay is a monotonic ProbeSeq high-water filter. Accept advances the mark
// and returns true only for a strictly increasing sequence, so a captured probe
// replayed later — or reordered behind a newer one — is rejected. Probes are
// periodic and their ProbeSeq is assigned monotonically, so strict monotonicity
// costs no legitimate probe while closing the replay window (D4).
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

// ProberConfig configures a Prober: the passive loss window width and the
// liveness detection thresholds.
type ProberConfig struct {
	LossWindow int
	Liveness   LivenessConfig
}

// Prober is the initiator side of the per-path probe exchange. It emits
// authenticated probes (SendProbe), consumes their authenticated echoes
// (HandleEcho) to measure RTT/jitter and drive liveness, and folds observed
// DATA outer-seq numbers into passive loss (ObserveData). It holds an injectable
// Clock and a probe-frame interface only, so it is unit-testable against
// synthetic echo traces with no real network.
type Prober struct {
	pathName string
	pathID   uint8
	psk      config.Key
	clock    Clock

	nextSeq uint64
	guard   AntiReplay

	est  *Estimator
	live *Liveness
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
	probe := frame.Probe{
		PathID:         p.pathID,
		ProbeSeq:       p.nextSeq,
		TimestampNanos: p.clock.Now().UnixNano(),
	}
	p.nextSeq++
	return frame.Encode(p.psk, probe)
}

// HandleEcho processes one received echo frame. It rejects forged or tampered
// echoes (frame.Decode fails the HMAC -> ErrAuth), non-probe frames, and
// replayed/stale ProbeSeq (ErrReplay, defect D4). Only an echo that authenticates
// AND is fresh updates the RTT/jitter estimate and the liveness heartbeat; a
// rejected echo leaves all estimator and liveness state untouched.
func (p *Prober) HandleEcho(raw []byte) error {
	f, err := frame.Decode(p.psk, raw)
	if err != nil {
		return err
	}
	probe, ok := f.(frame.Probe)
	if !ok {
		return fmt.Errorf("telemetry: expected probe echo, got frame kind %d", f.Kind())
	}
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

// ObserveData folds one observed DATA outer-sequence number into the passive
// loss estimate.
func (p *Prober) ObserveData(seq uint64) { p.est.ObserveDataSeq(seq) }

// Tick advances the liveness state machine against the clock; call it at least
// once per probe interval.
func (p *Prober) Tick() { p.live.Tick() }

// Estimate returns the current per-path quality snapshot.
func (p *Prober) Estimate() Estimate { return p.est.Estimate() }

// State returns the current liveness verdict.
func (p *Prober) State() PathState { return p.live.State() }

// Reflector is the responder side of the probe exchange. Reflect authenticates
// an inbound probe, rejects replays (D4), and returns a fresh authenticated
// encoding of the same probe (verbatim ProbeSeq and TimestampNanos) so the
// initiator can compute the round-trip from its own send timestamp. The
// responder never manufactures a timestamp, so it needs no synchronized clock.
type Reflector struct {
	psk   config.Key
	guard AntiReplay
}

// NewReflector builds a Reflector authenticating under psk.
func NewReflector(psk config.Key) *Reflector {
	return &Reflector{psk: psk}
}

// Reflect decodes and re-encodes one probe as its echo. It returns frame.ErrAuth
// for a forged/tampered probe, an error for a non-probe frame, and ErrReplay for
// a replayed/stale ProbeSeq.
func (r *Reflector) Reflect(raw []byte) ([]byte, error) {
	f, err := frame.Decode(r.psk, raw)
	if err != nil {
		return nil, err
	}
	probe, ok := f.(frame.Probe)
	if !ok {
		return nil, fmt.Errorf("telemetry: expected probe, got frame kind %d", f.Kind())
	}
	if !r.guard.Accept(probe.ProbeSeq) {
		return nil, ErrReplay
	}
	return frame.Encode(r.psk, probe)
}
