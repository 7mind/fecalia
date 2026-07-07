package telemetry

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/frame"
	"github.com/7mind/wanbond/internal/log"
)

// NewSessionID draws a random 64-bit per-boot probe session identity from the
// system CSPRNG (T38, defect D12). One value is drawn per process boot and shared
// across every path's Prober; it identifies the boot so a peer restart presents a
// SessionID the surviving responder has never seen, resetting that peer's
// anti-replay high-water (see Reflector). It fails only if the CSPRNG is
// unavailable. A random (not sequential) id is used deliberately: an attacker must
// not be able to predict or roll the id forward, and unpredictability is what lets
// the responder treat a never-before-seen SessionID as a genuine liveness proof.
func NewSessionID() (uint64, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0, fmt.Errorf("telemetry: read session id: %w", err)
	}
	return binary.BigEndian.Uint64(b[:]), nil
}

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

// ErrSessionMismatch is returned by HandleEcho when an echo's SessionID does not
// match this Prober's own per-boot session id (T38, defect D12). Every probe this
// Prober sends is stamped with its SessionID and the responder reflects it
// verbatim, so a genuine echo of THIS boot's probe always carries it. An echo
// carrying a different SessionID is a stale echo of a previous boot's probe (still
// in flight across a local restart) or a replay of one; rejecting it keeps a fresh
// Prober's high-water from being advanced by a prior session's ProbeSeq.
var ErrSessionMismatch = errors.New("telemetry: probe SessionID does not match this session")

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
	pathName  string
	pathID    uint8
	sessionID uint64
	psk       config.Key
	clock     Clock

	mu      sync.Mutex
	nextSeq uint64
	guard   AntiReplay
	est     *Estimator
	live    *Liveness
}

// NewProber builds a Prober for one path. sessionID is this node's random
// per-boot session identity (see NewSessionID); it is stamped into every probe
// this Prober emits and, on the responder, keys the anti-replay reset that lets a
// restarted peer's paths recover (T38, defect D12). All Probers of one boot share
// the same sessionID (it identifies the boot, not the path). The logger is used
// (path-tagged) for liveness transition logging.
func NewProber(
	pathName string,
	pathID uint8,
	sessionID uint64,
	psk config.Key,
	cfg ProberConfig,
	clock Clock,
	logger log.Logger,
) *Prober {
	return &Prober{
		pathName:  pathName,
		pathID:    pathID,
		sessionID: sessionID,
		psk:       psk,
		clock:     clock,
		est:       NewEstimator(cfg.LossWindow),
		live:      NewLiveness(pathName, cfg.Liveness, clock, logger),
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
		SessionID:      p.sessionID,
	}
	p.nextSeq++
	return frame.Encode(p.psk, probe)
}

// HandleEcho processes one received echo frame. It rejects, without touching
// liveness or the RTT estimate: forged/tampered echoes (frame.Decode fails the
// HMAC -> ErrAuth), non-probe frames, echoes for another path (ErrPathMismatch),
// echoes stamped with a foreign SessionID (ErrSessionMismatch, a stale cross-boot
// echo — T38/D12), and replayed/stale ProbeSeq (ErrReplay, defect D4). A genuine echo's ProbeSeq
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
	// The SessionID is immutable after construction, so it is read without the lock.
	// A genuine echo of one of THIS boot's probes always carries our own SessionID
	// (we stamped it; the responder reflected it verbatim). A mismatch is a stale
	// cross-boot echo or a replay of one — reject it before it can advance this
	// boot's high-water or liveness.
	if probe.SessionID != p.sessionID {
		return ErrSessionMismatch
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

// sessionPath is the composite anti-replay key: one strict-monotonic ProbeSeq
// high-water per (originator session, path). See Reflector.
type sessionPath struct {
	pathID    uint8
	sessionID uint64
}

// Reflector is the responder side of the probe exchange. Reflect authenticates
// an inbound probe, rejects replays per originating path (D4), and returns a fresh
// authenticated encoding of the same probe (verbatim ProbeSeq and TimestampNanos,
// but with IsEcho set) so the initiator can compute the round-trip from its own
// send timestamp and its transport can tell the echo apart from a peer probe. The
// responder never manufactures a timestamp, so it needs no synchronized clock.
//
// A single Reflector serves EVERY path, and each path carries its own ProbeSeq
// space (both start at 0), so the anti-replay high-water is keyed by (SessionID,
// PathID). The SessionID is the originator's random per-boot identity carried
// inside the MAC-covered probe body (T38, defect D12):
//
//   - WITHIN a session (SessionID == the path's current session): strict-monotonic
//     replay rejection is preserved exactly as before (D4). A replayed/stale
//     ProbeSeq is rejected.
//   - A BRAND-NEW SessionID never seen on the path (peer reboot): adopted as the
//     path's new current session, its high-water reset to accept the ProbeSeq=0
//     stream. This is what unblocks a restarted peer whose nextSeq reset to 0 —
//     without it the surviving Reflector would reject every fresh probe against the
//     prior session's high-water until the counter organically passed it (D12).
//   - Anti-rollback rule: a SessionID that was seen before but is NOT the path's
//     current session is RETIRED, and any probe bearing it is rejected as ErrReplay
//     — it can never again force a reset. The reset is therefore gated on the
//     SessionID being one no prior traffic carried. Because the SessionID lives
//     inside the MAC-covered body, only the live peer (holding the PSK) can mint a
//     frame under a SessionID no eavesdropper has captured; an attacker is confined
//     to replaying captured frames, all of which carry already-retired SessionIDs.
//     A live peer's fresh, unseen, authenticated SessionID is thus the liveness
//     proof that gates the reset, and a superseded session is never revived.
//
// Memory: r.guards holds one AntiReplay per (SessionID, PathID) ever observed —
// one extra entry per genuine peer reboot per path (a rare event on the
// minutes-to-hours scale), which also serves as the "retired session" set that
// enforces the anti-rollback rule above.
//
// Concurrency: Reflect may be called from all per-path receive goroutines at
// once; it is guarded by an internal mutex.
type Reflector struct {
	psk config.Key

	mu      sync.Mutex
	current map[uint8]uint64            // pathID -> the path's live (current) SessionID
	guards  map[sessionPath]*AntiReplay // (pathID, SessionID) -> per-session high-water; keys are EVERY session ever seen
}

// NewReflector builds a Reflector authenticating under psk.
func NewReflector(psk config.Key) *Reflector {
	return &Reflector{
		psk:     psk,
		current: make(map[uint8]uint64),
		guards:  make(map[sessionPath]*AntiReplay),
	}
}

// Reflect decodes and re-encodes one probe as its echo. It returns frame.ErrAuth
// for a forged/tampered probe, an error for a non-probe frame, and ErrReplay for
// a ProbeSeq replayed/stale within its session or a probe bearing a retired
// (superseded) SessionID (anti-rollback — see Reflector).
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
	fresh := r.acceptLocked(probe.PathID, probe.SessionID, probe.ProbeSeq)
	r.mu.Unlock()

	if !fresh {
		return nil, ErrReplay
	}
	// Mark the reflection as an echo so the originator's transport routes it into
	// its Prober (HandleEcho) rather than reflecting it again. ProbeSeq and
	// TimestampNanos are preserved verbatim so the originator computes the RTT from
	// its own send timestamp.
	probe.IsEcho = true
	return frame.Encode(r.psk, probe)
}

// acceptLocked applies the (SessionID, PathID)-keyed anti-replay decision for one
// probe and reports whether it is fresh. The caller holds r.mu. See Reflector for
// the full rule set (within-session monotonicity, new-session reset, anti-rollback
// on retired sessions).
func (r *Reflector) acceptLocked(pathID uint8, sessionID, probeSeq uint64) bool {
	key := sessionPath{pathID: pathID, sessionID: sessionID}
	curSession, hasCurrent := r.current[pathID]
	switch {
	case hasCurrent && sessionID == curSession:
		// Current live session: strict-monotonic replay rejection (D4). The guard
		// for the current session always exists (created when it was adopted).
		return r.guards[key].Accept(probeSeq)
	case r.guards[key] != nil:
		// Seen before but not current => retired/superseded. Replaying it (a
		// rollback attempt) must never force a reset.
		return false
	default:
		// Brand-new SessionID never seen on this path: a live peer proved liveness
		// with a fresh MAC-authenticated identity no replay could carry. Adopt it as
		// the path's current session and reset the high-water to accept its stream.
		guard := &AntiReplay{}
		accepted := guard.Accept(probeSeq)
		r.guards[key] = guard
		r.current[pathID] = sessionID
		return accepted
	}
}
