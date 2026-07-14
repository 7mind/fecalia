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
	// specs is the ORDERED concentrator endpoint set, one failoverSpec per configured
	// endpoint entry (config.Peer.EndpointSpecs), in TOML order. Each spec OWNS its
	// current expansion ([]netip.AddrPort): a literal is a fixed single entry; a hostname
	// is its latest resolved record set (empty until the resolver — wired by a later task —
	// first calls updateResolution). The FLATTENED concatenation of every spec's expansion,
	// in spec order, is the failover order; the flattened index is what `check` advances.
	// The set is MUTABLE: updateResolution swaps a spec's expansion in place under mu.
	specs []failoverSpec
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
	// activeSpec and activeAddr are the SPEC-SCOPED identity of the ACTIVE endpoint (R70):
	// the pair (owning spec index, AddrPort) — NEVER a bare flattened index and NEVER a
	// bare AddrPort. Because a hostname may legitimately re-resolve onto the SAME AddrPort
	// as another spec's literal (T67's load-time duplicate check is textual host:port
	// only), duplicate AddrPort VALUES may appear across different specs in the flattened
	// list; a bare-value re-map could silently bind the active pointer to the wrong spec's
	// entry, so the active endpoint is addressed by (specIdx, AddrPort). activeSpec == -1
	// means no active entry yet (every spec's expansion is empty). Guarded by mu.
	activeSpec int
	activeAddr netip.AddrPort
	// idx is the ACTIVE endpoint's position in the FLATTENED list, DERIVED from
	// (activeSpec, activeAddr) and re-mapped after every mutation. It is a read-only cache
	// for observation/logging; the source of truth is the spec-scoped identity above. -1
	// when there is no active entry. Guarded by mu.
	idx int
	// lastSwitch is when the active endpoint was last (re)selected — initialized to
	// construction time so endpoint 0 gets the SAME settle grace at boot that a switched
	// endpoint gets, preventing a boot-time false failover while probers are still DOWN
	// before the first echo returns. Guarded by mu.
	lastSwitch time.Time
}

// failoverSpec is one ordered endpoint entry together with its CURRENT expansion. A
// literal entry's expansion is the fixed single AddrPort; a hostname entry's expansion is
// its latest resolved record set (Q32 multi-record), swapped in place by updateResolution.
// The spec itself (config.EndpointSpec) is retained so the controller can tell a hostname
// spec — which may grow/shrink at runtime — from an immutable literal, e.g. when deciding
// whether the failover loop may ever have work to do.
type failoverSpec struct {
	spec  config.EndpointSpec
	addrs []netip.AddrPort
}

// newHubFailover builds the controller over an ordered list of IP-LITERAL endpoints — the
// legacy all-literal form (each address becomes its own single-entry literal spec). It is
// a pure constructor (no goroutine, no engine dependency beyond the injected collaborators)
// so its decision logic is unit-testable against a fake clock, fake health, and fake
// remote/rehandshake.
func newHubFailover(endpoints []netip.AddrPort, health []hubHealth, remote peerRemote, rh rehandshake, clock telemetry.Clock, settle time.Duration, lg log.Logger) *hubFailover {
	specs := make([]failoverSpec, len(endpoints))
	for i, ap := range endpoints {
		specs[i] = failoverSpec{spec: config.EndpointSpec{Addr: ap}, addrs: []netip.AddrPort{ap}}
	}
	return newHubFailoverFromSpecs(specs, health, remote, rh, clock, settle, lg)
}

// newHubFailoverFromSpecs builds the controller over the ordered, spec-keyed endpoint set.
// The active endpoint is initialized to the FIRST flattened entry (spec order); when every
// spec's expansion is empty (e.g. a hostname-only peer before the resolver has run) there
// is no active entry yet (activeSpec == -1) and check takes no action until an expansion
// appears. Like newHubFailover it injects no goroutine or engine dependency.
func newHubFailoverFromSpecs(specs []failoverSpec, health []hubHealth, remote peerRemote, rh rehandshake, clock telemetry.Clock, settle time.Duration, lg log.Logger) *hubFailover {
	h := &hubFailover{
		specs:       specs,
		health:      health,
		remote:      remote,
		rehandshake: rh,
		clock:       clock,
		settle:      settle,
		lastSwitch:  clock.Now(),
		log:         lg,
	}
	h.activeSpec, h.activeAddr = h.entryAtLocked(0)
	h.idx = h.flatIndexLocked(h.activeSpec, h.activeAddr)
	return h
}

// flatLenLocked is the number of endpoints in the FLATTENED list (the sum of every spec's
// current expansion length). Caller holds mu.
func (h *hubFailover) flatLenLocked() int {
	n := 0
	for i := range h.specs {
		n += len(h.specs[i].addrs)
	}
	return n
}

// canFailoverLocked reports whether the controller could EVER take a failover action: the
// flattened list already holds >= 2 endpoints, or SOME spec is a hostname whose runtime
// resolution may yet grow it. An all-literal set with a single entry can never grow, so its
// loop is a pure no-op and need not run. Caller holds mu.
func (h *hubFailover) canFailoverLocked() bool {
	if h.flatLenLocked() >= 2 {
		return true
	}
	for i := range h.specs {
		if h.specs[i].spec.IsName {
			return true
		}
	}
	return false
}

// flatIndexLocked returns the position of (specIdx, addr) in the FLATTENED list, or -1 if
// no such entry exists. It matches BOTH the owning spec index AND the AddrPort — never the
// bare value — so a duplicate AddrPort contributed by a DIFFERENT spec is skipped (R70);
// within the owning spec the FIRST occurrence wins. Caller holds mu.
func (h *hubFailover) flatIndexLocked(specIdx int, addr netip.AddrPort) int {
	flat := 0
	for si := range h.specs {
		for _, a := range h.specs[si].addrs {
			if si == specIdx && a == addr {
				return flat
			}
			flat++
		}
	}
	return -1
}

// entryAtLocked maps a FLATTENED index back to its (owning spec index, AddrPort), or
// (-1, zero) when flatIdx is out of range (including an empty flattened list). Caller holds mu.
func (h *hubFailover) entryAtLocked(flatIdx int) (int, netip.AddrPort) {
	if flatIdx < 0 {
		return -1, netip.AddrPort{}
	}
	n := 0
	for si := range h.specs {
		for _, a := range h.specs[si].addrs {
			if n == flatIdx {
				return si, a
			}
			n++
		}
	}
	return -1, netip.AddrPort{}
}

// containsAddrPort reports whether addrs holds target (exact AddrPort equality).
func containsAddrPort(addrs []netip.AddrPort, target netip.AddrPort) bool {
	for _, a := range addrs {
		if a == target {
			return true
		}
	}
	return false
}

// updateResolution swaps specIdx's expansion under h.mu — the sole mutation point of the
// endpoint set — and reconciles the active pointer WITHIN its owning spec (R70 spec-scoped
// identity):
//   - BOOT ADOPTION (activeSpec == -1: every spec's expansion was empty, e.g. a hostname-
//     only peer before the resolver first ran): the first resolution that makes the
//     flattened list non-empty ADOPTS its head as the active endpoint — set (activeSpec,
//     activeAddr), point the bond via exactly ONE SetPeerRemote, one rehandshake, and ARM
//     the settle dwell (lastSwitch = now). Without this a single-hostname peer's bond would
//     never receive any endpoint: check cannot rescue it either, because a one-record
//     expansion keeps the flattened length at 1, permanently under check's total<2 guard.
//   - A STANDBY-only change (specIdx != activeSpec) NEVER touches the bond: the active
//     entry is untouched; only the DERIVED flattened idx is re-mapped (an earlier spec
//     growing/shrinking shifts the active entry's flattened position without moving it).
//   - When the ACTIVE spec's own expansion changes: if the active AddrPort SURVIVES the
//     swap within that spec, the active entry is stable — strictly NO repoint (Q31/D32
//     no-op suppression), only the idx re-maps. If the active AddrPort is GONE, the active
//     endpoint's IP has genuinely changed: repoint via exactly ONE SetPeerRemote (D32
//     disruptive → Rebaseline + rehandshake) and one rehandshake to the new expansion's
//     first entry, and RE-ARM the settle dwell (lastSwitch = now) so the freshly-repointed
//     endpoint gets its full grace before a subsequent all-down check may advance off it.
//
// A change that empties the active spec (nothing to point at) leaves the active identity
// stale — the flattened list no longer contains it — and the next check on hub loss
// advances off it; no repoint is issued to a non-existent endpoint.
func (h *hubFailover) updateResolution(specIdx int, addrs []netip.AddrPort) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if specIdx < 0 || specIdx >= len(h.specs) {
		return
	}
	h.specs[specIdx].addrs = addrs

	switch {
	case h.activeSpec == -1:
		// No active entry yet (every spec was empty at boot). If this resolution populated
		// the flattened list, adopt its head, point the bond at it, and arm the settle dwell.
		if adoptSpec, adoptAddr := h.entryAtLocked(0); adoptSpec != -1 {
			h.activeSpec = adoptSpec
			h.activeAddr = adoptAddr
			h.lastSwitch = h.clock.Now()
			h.remote.SetPeerRemote(adoptAddr)
			if h.rehandshake != nil {
				h.rehandshake()
			}
			h.log.Warn("hub failover: first endpoint resolution; adopted active concentrator endpoint and re-handshaked",
				"spec_index", adoptSpec, "to_endpoint", adoptAddr.String())
		}
	case specIdx == h.activeSpec:
		if !containsAddrPort(addrs, h.activeAddr) && len(addrs) > 0 {
			// The active endpoint's own spec re-resolved off the current AddrPort: its IP
			// changed. Repoint the bond once, re-handshake against the new first entry, and
			// re-arm the settle dwell against the freshly-repointed endpoint.
			prev := h.activeAddr
			h.activeAddr = addrs[0]
			h.lastSwitch = h.clock.Now()
			h.remote.SetPeerRemote(h.activeAddr)
			if h.rehandshake != nil {
				h.rehandshake()
			}
			h.log.Warn("hub failover: active concentrator endpoint re-resolved; repointed remote and re-handshaked",
				"spec_index", specIdx, "from_endpoint", prev.String(), "to_endpoint", h.activeAddr.String())
		}
		// Survived (or empty expansion): no repoint. idx re-maps below.
	}

	h.idx = h.flatIndexLocked(h.activeSpec, h.activeAddr)
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

	// GUARD: a FLATTENED list of fewer than two endpoints takes NO failover action — no
	// advance, no remote repoint, no re-handshake — so a one-concentrator deployment is
	// byte-for-byte the pre-T57 behaviour, and a hostname peer whose resolution has not yet
	// yielded a second endpoint waits rather than churning.
	total := h.flatLenLocked()
	if total < 2 {
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

	// Advance in FLATTENED order from the active entry's current flattened position. The
	// position is re-derived from the spec-scoped identity every step, so a standby spec
	// that grew/shrank since the last advance is walked at its new offset. A missing active
	// (activeSpec == -1, or its AddrPort no longer present) resumes from the head.
	prev := h.flatIndexLocked(h.activeSpec, h.activeAddr)
	var nextIdx int
	if prev < 0 {
		nextIdx = 0
	} else {
		nextIdx = (prev + 1) % total
	}
	nextSpec, next := h.entryAtLocked(nextIdx)
	h.activeSpec = nextSpec
	h.activeAddr = next
	h.idx = nextIdx
	h.lastSwitch = now

	// Repoint the DATA/PROBE plane FIRST so probes immediately start re-arming detection
	// against the new endpoint, then initiate the fresh WG re-handshake toward it.
	h.remote.SetPeerRemote(next)
	if h.rehandshake != nil {
		h.rehandshake()
	}
	h.log.Warn("hub failover: all paths to active concentrator down; switched endpoint and re-handshaked",
		"from_index", prev, "to_index", nextIdx, "to_spec", nextSpec, "to_endpoint", next.String(), "endpoints", total)
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

// peerNeedsHubFailover reports whether a peer's endpoint set warrants a hub-failover
// controller: it does when the peer carries ANY hostname spec (its runtime resolution may
// yield further endpoints, and even a single hostname can re-resolve onto a new IP the bond
// must be repointed at) OR when >= 2 IP-literal endpoints are configured (an ordered
// active-standby list). A single-IP-literal peer needs NO controller — it is byte-for-byte
// the pre-G5 single-concentrator deployment (Q29), so the failover path never constructs
// or runs for it.
func peerNeedsHubFailover(peer config.Peer) bool {
	for _, s := range peer.EndpointSpecs {
		if s.IsName {
			return true
		}
	}
	return len(peer.Endpoints) >= 2
}

// startHubFailover builds and starts the edge-side hub-failover monitor for cfg's
// concentrator peer, or returns a NO-OP stopper when hub failover does not apply: the
// concentrator role (it roams edges dynamically and has no endpoint list), a bind without
// the probe transport (no probers → no liveness plane), or no peer whose endpoint set
// warrants a controller (peerNeedsHubFailover — a single-IP-literal deployment gets none,
// staying behaviour-identical to pre-G5). The edge bonds every path to a SINGLE
// concentrator, so the whole per-path prober set IS the liveness of the paths to that
// concentrator (hub loss = every one DOWN); the controller drives the first qualifying
// peer. The returned stopper is what device.Close invokes to halt the loop.
func startHubFailover(cfg *config.Config, mp *bind.Multipath, probers []*telemetry.Prober, dev *awgdevice.Device, lg log.Logger) func() {
	if cfg.Role != config.RoleEdge || len(probers) == 0 {
		return func() {}
	}
	for _, peer := range cfg.WireGuard.Peers {
		if !peerNeedsHubFailover(peer) {
			continue
		}
		health := make([]hubHealth, len(probers))
		for i, pr := range probers {
			health[i] = pr
		}
		// Seed each spec's expansion: a literal is its fixed single AddrPort; a hostname
		// starts EMPTY — resolution is a later task's job (Q30), not wired here.
		specs := make([]failoverSpec, len(peer.EndpointSpecs))
		for i, s := range peer.EndpointSpecs {
			var addrs []netip.AddrPort
			if !s.IsName {
				addrs = []netip.AddrPort{s.Addr}
			}
			specs[i] = failoverSpec{spec: s, addrs: addrs}
		}
		ctrl := newHubFailoverFromSpecs(
			specs, health, mp,
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
// no-op (no-op stopper) for a controller that can NEVER fail over (canFailoverLocked: an
// all-literal set with fewer than two entries — no hostname to grow it) or a non-positive
// interval. A hostname peer whose expansion is still empty DOES start the loop: its
// resolution may later populate a second endpoint, and check no-ops cheaply until then.
func (h *hubFailover) startHubFailoverLoop(interval time.Duration) (stop func()) {
	h.mu.Lock()
	can := h.canFailoverLocked()
	h.mu.Unlock()
	if !can || interval <= 0 {
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
