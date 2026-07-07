package telemetry

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/frame"
	"github.com/7mind/wanbond/internal/log"
)

// NewSessionID draws a 64-bit per-boot probe session identity from rand (the
// injected CSPRNG — crypto/rand.Reader in production; T38, defect D12). One value
// is drawn per process boot and shared across every path's Prober; it TAGS the
// boot's probe stream so a peer restart presents a distinct SessionID. Note the
// SessionID alone is NOT a freshness proof (a captured probe carries a never-seen
// SessionID too): the responder's session-epoch reset is gated on the
// responder-contributed Challenge, not on the SessionID being novel (see
// Reflector). It fails only if the CSPRNG cannot deliver 8 bytes.
func NewSessionID(rand io.Reader) (uint64, error) {
	v, err := readUint64(rand)
	if err != nil {
		return 0, fmt.Errorf("telemetry: read session id: %w", err)
	}
	return v, nil
}

// readUint64 draws a big-endian uint64 from r, failing only if r cannot deliver 8
// bytes. It is the shared draw for both session ids and reflector challenges.
func readUint64(r io.Reader) (uint64, error) {
	var b [8]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return 0, err
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
	// learnedChallenge is the last issued challenge this Prober learned from a fresh
	// echo; it is stamped into subsequent probes so the responder can verify liveness
	// and (after a restart) adopt this session. Zero until the first echo is seen.
	learnedChallenge uint64
	est              *Estimator
	live             *Liveness
}

// NewProber builds a Prober for one path. sessionID is this node's per-boot
// session identity (see NewSessionID); it is stamped into every probe this Prober
// emits to TAG the boot's stream (T38, defect D12). The responder's session-epoch
// reset that lets a restarted peer's paths recover is gated on the
// responder-contributed challenge this Prober echoes back, not on the sessionID
// alone. All Probers of one boot share the same sessionID (it identifies the boot,
// not the path). The logger is used (path-tagged) for liveness transition logging.
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
		// Echo back the responder's last-issued challenge (zero until we have seen an
		// echo) so the responder can verify our liveness and adopt this session.
		Challenge: p.learnedChallenge,
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
	// Record the responder's live issued challenge from this FRESH echo so our
	// subsequent probes prove liveness and (after a restart) get us re-adopted. Only
	// fresh echoes update it, so a replayed old echo can never roll the learned
	// challenge backward and stall re-adoption.
	p.learnedChallenge = probe.Challenge
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

// reflectorPath is the Reflector's bounded per-path state: exactly one entry per
// PathID, so memory is O(paths) with NO retired-session set. It holds the
// currently-adopted originator session, its strict-monotonic within-session
// anti-replay high-water, and the LIVE issued challenge peers must echo back to
// authorize a session-epoch reset.
type reflectorPath struct {
	session   uint64     // the adopted originator SessionID (meaningful once adopted)
	guard     AntiReplay // ProbeSeq high-water for the currently-adopted session
	challenge uint64     // issuedChallenge: the live freshness token, rotated on every adoption
	adopted   bool       // whether any session has been adopted on this path yet
}

// Reflector is the responder side of the probe exchange. Reflect authenticates an
// inbound probe and returns a fresh authenticated ECHO of it (verbatim ProbeSeq /
// TimestampNanos / SessionID, IsEcho set, and the path's live issued challenge
// stamped into Challenge) so the initiator can compute the round-trip AND learn the
// challenge. The responder never manufactures a timestamp, so it needs no
// synchronized clock.
//
// Freshness is RESPONDER-CONTRIBUTED (T38 redesign, defect D12). Each path holds a
// random issuedChallenge that the Reflector delivers inside every echo (confidential
// + MAC-covered, so only the live PSK-holding peer can read it). A probe authorizes
// a session-epoch reset ONLY if it echoes back that live challenge — which a replay
// attacker, who never sees the current challenge, can never do. Unpredictability of
// a SessionID is therefore NOT treated as freshness: a novel SessionID alone resets
// nothing; only a live-challenge echo does. Per inbound PROBE with SessionID S and
// echoed Challenge C on a path whose live state is (session, high-water,
// issuedChallenge):
//
//   - S == the adopted session: strict-monotonic within-session replay rejection
//     (D4). A fresh ProbeSeq is accepted and reflected; a stale/replayed one is
//     rejected (ErrReplay, and NOT reflected — no echo oracle for a known duplicate).
//   - S != the adopted session AND C == issuedChallenge: a genuine peer that received
//     our live echo. Adopt S as the new epoch and reset the high-water so its
//     seq-from-0 stream is accepted (the D12 restart recovery), then ROTATE
//     issuedChallenge so this very adoption probe can never be replayed later to
//     re-adopt a superseded session.
//   - S != the adopted session AND C != issuedChallenge: EITHER a replay attacker
//     (stale/zero challenge — cannot seize the epoch) OR a freshly-restarted peer
//     that has not yet learned our challenge. Do NOT adopt/reset, but STILL reflect
//     the current issuedChallenge so a genuine peer learns it and is adopted on its
//     next probe. A replayed frame is thus harmless: reflected 1:1, never seizing.
//
// Recovery after a genuine peer restart takes ~2 probe intervals + RTT (the
// bootstrap probe learns the challenge; the next probe carries it and is adopted) —
// well within the T13 detection window.
//
// Memory is O(paths): the paths map is keyed by the uint8 PathID (at most 256
// entries) and each entry is fixed-size, with no per-session accumulation.
//
// Concurrency: Reflect may be called from all per-path receive goroutines at once;
// it is guarded by an internal mutex. The injected rand source is read only under
// that mutex.
type Reflector struct {
	psk  config.Key
	rand io.Reader

	mu    sync.Mutex
	paths map[uint8]*reflectorPath
}

// NewReflector builds a Reflector authenticating under psk and drawing its per-path
// issued challenges from rand (crypto/rand.Reader in production; a deterministic
// reader in tests). rand is injected rather than referenced as a package global so
// the challenge stream is controllable under test (no-globals/DI).
func NewReflector(psk config.Key, rand io.Reader) *Reflector {
	return &Reflector{
		psk:   psk,
		rand:  rand,
		paths: make(map[uint8]*reflectorPath),
	}
}

// Reflect decodes and re-encodes one probe as its echo. It returns frame.ErrAuth
// for a forged/tampered probe, an error for a non-probe frame, ErrReplay for a
// ProbeSeq replayed/stale within the adopted session, and a wrapped error if the
// CSPRNG cannot deliver a challenge. Every other authenticated probe — including a
// cross-session probe that does NOT carry the live challenge — is reflected; it
// simply does not reset the epoch.
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
	issued, reflect, err := r.acceptLocked(probe.PathID, probe.SessionID, probe.ProbeSeq, probe.Challenge)
	r.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if !reflect {
		return nil, ErrReplay
	}
	// Mark the reflection as an echo so the originator's transport routes it into its
	// Prober (HandleEcho) rather than reflecting it again, and stamp the path's live
	// issued challenge so the peer can prove liveness on its next probe. ProbeSeq /
	// TimestampNanos / SessionID are preserved verbatim.
	probe.IsEcho = true
	probe.Challenge = issued
	return frame.Encode(r.psk, probe)
}

// acceptLocked applies the responder-contributed-challenge decision for one probe.
// It reports the issued challenge to stamp into the echo, whether to reflect at all
// (reflect=false => a within-session duplicate, surfaced as ErrReplay), and any
// CSPRNG error. The caller holds r.mu. See Reflector for the full rule set.
func (r *Reflector) acceptLocked(pathID uint8, sessionID, probeSeq, echoedChallenge uint64) (issued uint64, reflect bool, err error) {
	st, ok := r.paths[pathID]
	if !ok {
		ch, derr := r.drawChallenge()
		if derr != nil {
			return 0, false, derr
		}
		st = &reflectorPath{challenge: ch}
		r.paths[pathID] = st
	}

	switch {
	case st.adopted && sessionID == st.session:
		// Current live session: strict-monotonic within-session replay rejection (D4).
		if !st.guard.Accept(probeSeq) {
			return 0, false, nil // known duplicate/stale: reject, do not reflect
		}
		return st.challenge, true, nil
	case echoedChallenge == st.challenge:
		// Cross-session probe echoing our LIVE challenge: a genuine peer that received
		// our echo. Adopt the new epoch, reset the high-water for its seq-from-0 stream,
		// then ROTATE the challenge so this adoption probe can never be replayed later
		// to re-adopt a superseded session.
		st.session = sessionID
		st.adopted = true
		st.guard = AntiReplay{}
		st.guard.Accept(probeSeq)
		ch, derr := r.drawChallenge()
		if derr != nil {
			return 0, false, derr
		}
		st.challenge = ch
		return st.challenge, true, nil
	default:
		// Cross-session probe WITHOUT our live challenge: a replay attacker (which can
		// never carry the current challenge) or a not-yet-bootstrapped restarted peer.
		// Never adopt/reset; still reflect the live challenge so a genuine peer learns
		// it and is adopted on its next probe.
		return st.challenge, true, nil
	}
}

// drawChallenge draws a fresh NON-ZERO issued challenge from the injected rand
// source. Zero is excluded so it can never collide with a peer's "no challenge
// learned yet" sentinel (a just-restarted peer's bootstrap probe carries
// Challenge=0), which would otherwise let that bootstrap probe be adopted without
// proving liveness. It fails only if the CSPRNG is unavailable.
func (r *Reflector) drawChallenge() (uint64, error) {
	for {
		v, err := readUint64(r.rand)
		if err != nil {
			return 0, fmt.Errorf("telemetry: draw challenge: %w", err)
		}
		if v != 0 {
			return v, nil
		}
	}
}
