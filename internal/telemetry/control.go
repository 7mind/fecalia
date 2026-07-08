package telemetry

import (
	"errors"
	"sync"

	"github.com/7mind/wanbond/internal/frame"
)

// ErrControlReplay is returned by ControlGuard.Admit when a security-relevant
// CONTROL frame's Seq is at or below the per-type high-water mark — a replayed or
// stale control message (defect D4). It is the control-layer analogue of ErrReplay
// on the PROBE path, and is likewise distinct from frame.ErrAuth: ErrAuth rejects a
// forged/tampered frame that fails the PSK HMAC, ErrControlReplay rejects a genuine,
// correctly-authenticated control frame that an attacker (or the network) delivered
// a second time — which the stateless codec (frame.Decode verifies only the MAC)
// cannot catch on its own.
var ErrControlReplay = errors.New("telemetry: control message replayed or stale")

// ControlGuard is the per-peer anti-replay state machine for authenticated CONTROL
// frames (defect D4). frame.Decode verifies a CONTROL frame's PSK HMAC but keeps NO
// per-peer state, so a passively-captured valid control frame — e.g. a rekey —
// replays with a passing MAC. Replay defense therefore belongs HERE, in the
// control-handling layer, exactly as the PROBE path's high-water (AntiReplay) and
// session challenge live in the Prober/Reflector.
//
// A CONTROL frame of a SECURITY-RELEVANT type carries a strictly-monotonic per-type
// Seq (frame.Control.Seq); the guard tracks a per-type high-water and rejects any
// non-advancing Seq as ErrControlReplay (a replay/stale message). Control types NOT
// registered as security-relevant carry no monotonic-Seq contract and are admitted
// unconditionally: replay of a non-security-relevant control message is out of D4's
// threat model (a replayed rekey is the concern), and sequence-gating a type that
// does not stamp a monotonic Seq would wrongly drop legitimate messages.
//
// One ControlGuard serves one peer (one PSK). It is internally synchronized so the
// per-path receive goroutines may share it, mirroring the single Reflector.
type ControlGuard struct {
	// secure is fixed at construction and never mutated, so Admit reads it without the
	// lock (a read-only map is safe for concurrent readers).
	secure map[uint8]bool

	mu     sync.Mutex
	guards map[uint8]*AntiReplay // per-security-type Seq high-water, created on first sight
}

// NewControlGuard builds a guard that applies replay rejection to the given
// security-relevant control types. A control type absent from securityRelevant is
// admitted without a sequence check (see ControlGuard).
func NewControlGuard(securityRelevant ...uint8) *ControlGuard {
	secure := make(map[uint8]bool, len(securityRelevant))
	for _, t := range securityRelevant {
		secure[t] = true
	}
	return &ControlGuard{secure: secure, guards: make(map[uint8]*AntiReplay)}
}

// Admit reports whether a decoded CONTROL frame is fresh. For a security-relevant
// control type it accepts a strictly-advancing Seq (advancing the per-type
// high-water) and returns ErrControlReplay for a non-advancing (replayed/stale) Seq.
// A non-security-relevant control type is always admitted.
func (g *ControlGuard) Admit(c frame.Control) error {
	if !g.secure[c.ControlType] {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	ar, ok := g.guards[c.ControlType]
	if !ok {
		ar = &AntiReplay{}
		g.guards[c.ControlType] = ar
	}
	if !ar.Accept(c.Seq) {
		return ErrControlReplay
	}
	return nil
}
