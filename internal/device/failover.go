package device

import (
	"net/netip"
	"sync"
	"time"

	awgdevice "github.com/amnezia-vpn/amneziawg-go/device"

	"github.com/7mind/wanbond/internal/bind"
	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/log"
	"github.com/7mind/wanbond/internal/telemetry"
)

// hubFailoverSettle is the dwell a newly-selected concentrator endpoint is given to
// prove itself LIVE before the controller may advance again. It exists to keep a
// STILL-DOWN reading — caused merely by echoes not having returned from the just-
// selected hub yet — from skipping straight past a healthy standby.
//
// It must comfortably exceed the liveness UP-recovery latency: a DOWN path comes UP
// after telemetry.DefaultUpSuccesses (3) probe echoes at telemetry.DefaultProbeInterval
// (200ms) ≈ 600ms once probes reach a reachable hub. At 3s the dwell clears that with a
// wide margin for jitter/RTT, and also bounds the re-advance cadence while a whole hub
// fleet is down to at most one switch (one fresh handshake) per 3s. Hub loss is a
// coarse, rare event (a concentrator down), distinct from the sub-second PER-PATH
// failover the schedulers own, so a switch latency on this scale is acceptable.
const hubFailoverSettle = 3 * time.Second

// hubHealth is one path's liveness verdict, the read side of the T13 telemetry state
// machine (*telemetry.Prober satisfies it). The hub-failover controller reads exactly
// the same up/down verdict the schedulers select on, so "hub loss" is derived from the
// very liveness plane per-path failover already runs on — no second detector.
type hubHealth interface {
	State() telemetry.PathState
}

// peerRemote repoints the whole bond's wire remote at a new concentrator endpoint
// (*bind.Multipath satisfies it via SetPeerRemote). It is the action half of a hub
// switch on the DATA/PROBE plane; the re-handshake is separate (rehandshake).
type peerRemote interface {
	SetPeerRemote(ap netip.AddrPort)
}

// rehandshake drops the current WireGuard session and initiates a FRESH one against the
// peer's static key — no hub-to-hub state handoff. The device wires it to the engine
// peer (expire keypairs + send handshake initiation); a test injects a counter.
type rehandshake func()

// hubFailover is the edge-side active-standby concentrator failover controller (Q18/T57).
// It watches the per-path liveness plane and, when EVERY path to the currently-active
// concentrator is DOWN simultaneously (HUB LOSS — distinct from a single path failing,
// which the schedulers already absorb), advances to the next endpoint in the ordered
// concentrator list, repoints every path's remote at it, and initiates a fresh WG
// re-handshake. It re-arms against the new endpoint and, if that one is also fully down,
// advances again.
//
// End-of-list policy: WRAP (round-robin modulo the list length). Once the last standby
// is exhausted the controller cycles back to index 0 and keeps retrying every endpoint
// in order. Wrap is chosen over stop because it preserves availability — a concentrator
// that RECOVERS earlier in the list is retried and settled on within one full cycle,
// whereas stopping at the last endpoint would strand the edge on a dead hub even after
// endpoint 0 came back. The settle dwell bounds the churn to one switch (one handshake)
// per hubFailoverSettle while the whole fleet is down, so the round-robin is a slow,
// bounded retry, not a storm.
//
// GUARD (must-hold invariant): a SINGLE-endpoint list takes NO failover action at all —
// check returns immediately, so the remote is never repointed and no re-handshake is
// initiated. A one-concentrator deployment therefore behaves EXACTLY as it did before
// T57 (the endpoints-list "list" is the one-element normalization of the legacy single
// endpoint form, per T54).
type hubFailover struct {
	// endpoints is the ORDERED concentrator endpoint list (config.Peer.Endpoints):
	// index 0 = active/primary at boot, the rest ordered standbys. Immutable after
	// construction. IP:port only — no DNS resolution (the T54 constraint).
	endpoints []netip.AddrPort
	// health is the per-path liveness, one entry per configured path. Immutable after
	// construction (the prober set is fixed for the tunnel's life).
	health []hubHealth

	remote      peerRemote
	rehandshake rehandshake
	clock       telemetry.Clock
	// settle is the dwell a freshly-selected endpoint gets before another advance is
	// allowed (hubFailoverSettle in production; a test injects its own).
	settle time.Duration
	log    log.Logger

	mu sync.Mutex
	// idx is the ACTIVE endpoint index (0 at boot). Guarded by mu.
	idx int
	// lastSwitch is when the active endpoint was last (re)selected — initialized to
	// construction time so endpoint 0 gets the SAME settle grace at boot that a switched
	// endpoint gets, preventing a boot-time false failover while probers are still DOWN
	// before the first echo returns. Guarded by mu.
	lastSwitch time.Time
}

// newHubFailover builds the controller over the ordered endpoint list and the per-path
// liveness set. It is a pure constructor (no goroutine, no engine dependency beyond the
// injected collaborators) so its decision logic is unit-testable against a fake clock,
// fake health, and fake remote/rehandshake.
func newHubFailover(endpoints []netip.AddrPort, health []hubHealth, remote peerRemote, rh rehandshake, clock telemetry.Clock, settle time.Duration, lg log.Logger) *hubFailover {
	return &hubFailover{
		endpoints:   endpoints,
		health:      health,
		remote:      remote,
		rehandshake: rh,
		clock:       clock,
		settle:      settle,
		lastSwitch:  clock.Now(),
		log:         lg,
	}
}

// allDownLocked reports HUB LOSS: every path's liveness to the active concentrator is
// DOWN simultaneously. An empty health set is NOT hub loss (there is nothing to declare
// dead — a bind without the probe transport never drives this). Caller holds mu.
func (h *hubFailover) allDownLocked() bool {
	if len(h.health) == 0 {
		return false
	}
	for _, hp := range h.health {
		if hp.State() != telemetry.StateDown {
			return false
		}
	}
	return true
}

// check is one failover-evaluation step: on confirmed hub loss (and once the active
// endpoint's settle dwell has elapsed) it advances to the next endpoint, repoints the
// bond's remote, and initiates a fresh re-handshake. It is idempotent and cheap in the
// steady state (any path up, or a single-endpoint list) — a length check plus a liveness
// sweep — so a periodic loop may call it at the probe cadence.
func (h *hubFailover) check() {
	h.mu.Lock()
	defer h.mu.Unlock()

	// GUARD: a single-endpoint (or empty) list takes NO failover action — no advance, no
	// remote repoint, no re-handshake — so a one-concentrator deployment is byte-for-byte
	// the pre-T57 behaviour.
	if len(h.endpoints) < 2 {
		return
	}
	if !h.allDownLocked() {
		return
	}
	// Settle dwell: give the currently-active endpoint time for its liveness to recover
	// before advancing, so a not-yet-echoed (transiently DOWN) healthy endpoint is not
	// skipped. Applies at boot (endpoint 0) and after every switch.
	now := h.clock.Now()
	if now.Sub(h.lastSwitch) < h.settle {
		return
	}

	prev := h.idx
	h.idx = (h.idx + 1) % len(h.endpoints)
	next := h.endpoints[h.idx]
	h.lastSwitch = now

	// Repoint the DATA/PROBE plane FIRST so probes immediately start re-arming detection
	// against the new endpoint, then initiate the fresh WG re-handshake toward it.
	h.remote.SetPeerRemote(next)
	if h.rehandshake != nil {
		h.rehandshake()
	}
	h.log.Warn("hub failover: all paths to active concentrator down; switched endpoint and re-handshaked",
		"from_index", prev, "to_index", h.idx, "to_endpoint", next.String(), "endpoints", len(h.endpoints))
}

// deviceRehandshake returns a rehandshake bound to the engine peer identified by pk: it
// EXPIRES the peer's current keypairs (dropping the old hub's session — a fresh session
// with NO hub-to-hub state handoff) and sends a fresh handshake initiation, which the
// Bind fans out to the just-repointed standby endpoint. All hubs in the ordered set
// share the peer's single WireGuard static key, so the SAME peer identity re-handshakes
// against whichever hub is now active. A peer the engine cannot resolve (never configured
// — impossible for the edge's sole peer) yields a no-op so the loop never dereferences
// nil. This is the ONLY engine-peer coupling the failover path takes; it stays in the
// device package alongside the rest of the engine wiring.
func deviceRehandshake(dev *awgdevice.Device, pk config.Key) rehandshake {
	key := awgdevice.NoisePublicKey(pk.Bytes())
	return func() {
		peer := dev.LookupPeer(key)
		if peer == nil {
			return
		}
		// Drop the current session so the re-handshake starts a FRESH keypair (no hub-to-hub
		// handoff); ExpireCurrentKeypairs also backdates lastSentHandshake so the immediately
		// following initiation is not suppressed by the RekeyTimeout guard.
		peer.ExpireCurrentKeypairs()
		_ = peer.SendHandshakeInitiation(false)
	}
}

// startHubFailover builds and starts the edge-side hub-failover monitor for cfg's
// concentrator peer, or returns a NO-OP stopper when hub failover does not apply: the
// concentrator role (it roams edges dynamically and has no endpoint list), a bind without
// the probe transport (no probers → no liveness plane), or no peer carrying an ordered
// (>= 2) endpoint list (a single-concentrator deployment, which the GUARD keeps
// behaviour-identical to pre-T57). The edge bonds every path to a SINGLE concentrator, so
// the whole per-path prober set IS the liveness of the paths to that concentrator (hub
// loss = every one DOWN); the controller drives the first peer carrying an ordered
// endpoint list. The returned stopper is what device.Close invokes to halt the loop.
func startHubFailover(cfg *config.Config, mp *bind.Multipath, probers []*telemetry.Prober, dev *awgdevice.Device, lg log.Logger) func() {
	if cfg.Role != config.RoleEdge || len(probers) == 0 {
		return func() {}
	}
	for _, peer := range cfg.WireGuard.Peers {
		if len(peer.Endpoints) < 2 {
			continue
		}
		health := make([]hubHealth, len(probers))
		for i, pr := range probers {
			health[i] = pr
		}
		ctrl := newHubFailover(
			peer.Endpoints, health, mp,
			deviceRehandshake(dev, peer.PublicKey),
			telemetry.SystemClock{}, hubFailoverSettle,
			lg.Component("hubfailover"),
		)
		// Poll at the probe cadence: check is cheap (a length check plus a liveness sweep)
		// and the settle dwell bounds actual switches, so a responsive poll only tightens
		// detection latency without churning the remote.
		return ctrl.startHubFailoverLoop(telemetry.DefaultProbeInterval)
	}
	return func() {}
}

// startHubFailoverLoop launches the failover-evaluation goroutine: it calls check every
// interval until the returned stopper is invoked (idempotent). It mirrors the bind's
// StartProbeLoop/StartReconcileLoop lifecycle glue — a wall-clock ticker driving a
// decision function whose every timing choice runs through the injected Clock, so a test
// drives check directly against a fake clock and never starts this goroutine. It is a
// no-op (no-op stopper) for a single-endpoint list (nothing to fail over to) or a
// non-positive interval.
func (h *hubFailover) startHubFailoverLoop(interval time.Duration) (stop func()) {
	if len(h.endpoints) < 2 || interval <= 0 {
		return func() {}
	}
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				h.check()
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(done) }) }
}
