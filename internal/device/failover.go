package device

import (
	"encoding/hex"
	"fmt"
	"net/netip"
	"sync"
	"time"

	awgdevice "github.com/amnezia-vpn/amneziawg-go/device"

	"github.com/7mind/wanbond/internal/bind"
	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/dnsresolve"
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
	// install populates the ENGINE peer's endpoint via the UAPI/IpcSet path (R70). It is used on
	// the FIRST-RESOLVE INSTALL PATH only — the boot-adoption of a peer that came up endpoint-less —
	// because SetPeerRemote (remote) repoints the bind's per-path remotes but NEVER sets the engine
	// peer's endpoint, so after an endpoint-less boot a bare rehandshake would have no endpoint to
	// transmit to. It is a REQUIRED constructor collaborator (newHubFailoverFromSpecs) — the device
	// wires it to deviceInstallEndpoint at construction, so a lost production wiring line is a
	// compile error, never a silent degradation to SetPeerRemote. Subsequent re-resolves of an
	// already-installed peer take the remote (SetPeerRemote) repoint path — the engine's virtual
	// endpoint stays stable per A1; only the bind remotes move.
	install func(netip.AddrPort)
	clock   telemetry.Clock
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
	// onExhausted is the endpoint-list-EXHAUSTION subscriber (T269's exit selector subscribes via
	// SetOnExhausted). It is invoked ONCE per outage — on the RISING edge into the exhausted state —
	// OUTSIDE h.mu (a subscriber may read this controller, so invoking it under the lock could
	// deadlock). nil until a subscriber registers: within-concentrator round-robin failover (Q72)
	// needs no subscriber, so the single/legacy shapes leave it unset. Guarded by mu.
	onExhausted func()
	// exhaustedLatched latches the exhaustion signal so onExhausted fires only on the RISING edge of
	// the exhausted state, not every tick while the outage persists. Cleared the instant any path to
	// the active concentrator recovers (checkLocked's not-allDown branch), so a fresh outage
	// re-signals. Guarded by mu.
	exhaustedLatched bool
	// downAdvances counts round-robin advances taken under CONTINUOUS hub loss (reset the instant any
	// path recovers). A MULTI-endpoint peer is EXHAUSTED once this reaches the flattened list length —
	// a full wrap in which every configured endpoint was tried and found down (R267). Guarded by mu.
	downAdvances int
	// downSince is the onset of the CURRENT hub-loss episode — set on the transition INTO allDown
	// (checkLocked, when the previous reading was not allDown) and cleared the instant any path
	// recovers. It is the reference the SINGLE-endpoint exhaustion dwell is measured from, because a
	// single-endpoint peer never switches, so its lastSwitch is frozen at construction and cannot time
	// a mid-life outage's dwell: without this, a fresh sub-second blackout on a single-endpoint exit
	// would raise exhaustion on its FIRST allDown reading and let T269's sticky auto-promotion
	// permanently move egress off it (D100 follow-up). Zero when not currently in a hub-loss episode.
	// Guarded by mu.
	downSince time.Time
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
	// An all-literal controller starts with every spec's expansion non-empty, so activeSpec is set
	// at construction and the FIRST-RESOLVE INSTALL PATH (h.install in updateResolution) is never
	// reached — it takes no engine-endpoint install. Pass a no-op for the required install collaborator.
	return newHubFailoverFromSpecs(specs, health, remote, rh, func(netip.AddrPort) {}, clock, settle, lg)
}

// newHubFailoverFromSpecs builds the controller over the ordered, spec-keyed endpoint set.
// The active endpoint is initialized to the FIRST flattened entry (spec order); when every
// spec's expansion is empty (e.g. a hostname-only peer before the resolver has run) there
// is no active entry yet (activeSpec == -1) and check takes no action until an expansion
// appears. Like newHubFailover it injects no goroutine or engine dependency. install is a
// REQUIRED collaborator (the FIRST-RESOLVE INSTALL PATH, R70): making it a constructor
// parameter — rather than a field patched in afterwards — turns a lost production wiring line
// into a compile error instead of a silent degradation to SetPeerRemote.
func newHubFailoverFromSpecs(specs []failoverSpec, health []hubHealth, remote peerRemote, rh rehandshake, install func(netip.AddrPort), clock telemetry.Clock, settle time.Duration, lg log.Logger) *hubFailover {
	h := &hubFailover{
		specs:       specs,
		health:      health,
		remote:      remote,
		rehandshake: rh,
		install:     install,
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
			// FIRST-RESOLVE INSTALL PATH (R70): the peer booted endpoint-less, so the engine peer
			// has NO endpoint yet. INSTALL the resolved endpoint on the engine peer via the UAPI
			// path (install) FIRST — a SetPeerRemote repoint would move the bind's per-path remotes
			// but leave the engine peer endpoint-less, so the following rehandshake would have no
			// endpoint to transmit to — THEN rehandshake, which now has an addressable endpoint.
			h.install(adoptAddr)
			if h.rehandshake != nil {
				h.rehandshake()
			}
			h.log.Warn("hub failover: first endpoint resolution; installed active concentrator endpoint on the engine peer and re-handshaked",
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

// allPathsDown reports HUB LOSS — every path to the active concentrator DOWN — under h.mu.
// It is the read half of the coordination surface the re-resolution controller (T73) shares
// with the failover controller (Q34): the resolver reads the SAME liveness sweep the failover
// loop advances on, so a liveness-loss out-of-band re-resolve and a hub-loss advance derive
// from one detector, never two that could disagree.
func (h *hubFailover) allPathsDown() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.allDownLocked()
}

// activeSpecIndex returns the owning spec index of the currently-active endpoint under h.mu,
// or -1 when there is no active entry yet (every spec's expansion empty). The re-resolution
// controller (T73) reads it to know WHICH spec a liveness-loss trigger must re-resolve out of
// band; the identity is spec-scoped (R70), so this is the spec index, never a flattened index.
func (h *hubFailover) activeSpecIndex() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.activeSpec
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

// SetOnExhausted registers (or clears, with nil) the endpoint-list-EXHAUSTION subscriber — the
// cross-concentrator exit selector (T269) that promotes to another concentrator once THIS peer's
// own endpoint list is fully exhausted. Within-concentrator single/partial endpoint failure stays
// this controller's own round-robin business (Q72); only full exhaustion crosses the boundary, and
// it does so through this one seam. The callback fires OUTSIDE h.mu (see onExhausted).
func (h *hubFailover) SetOnExhausted(cb func()) {
	h.mu.Lock()
	h.onExhausted = cb
	h.mu.Unlock()
}

// check is one failover-evaluation step: on confirmed hub loss (and once the active endpoint's
// settle dwell has elapsed) it advances to the next endpoint, repoints the bond's remote, initiates
// a fresh re-handshake, and — on a full flattened-list wrap (or a single-endpoint peer's sole
// endpoint down) — raises the endpoint-list-exhaustion signal (R267). It is idempotent and cheap in
// the steady state (any path up) — a length check plus a liveness sweep — so a periodic loop may
// call it at the probe cadence. The exhaustion subscriber is invoked here, AFTER the lock is
// released, so a subscriber that reads this controller cannot deadlock against check's own mu.
func (h *hubFailover) check() {
	if h.checkLocked() {
		h.mu.Lock()
		cb := h.onExhausted
		h.mu.Unlock()
		if cb != nil {
			cb()
		}
	}
}

// checkLocked performs the failover-evaluation step under h.mu and reports whether it just crossed
// into endpoint-list exhaustion (the caller then fires onExhausted outside the lock). It is factored
// out of check ONLY to keep the exhaustion callback off the lock.
func (h *hubFailover) checkLocked() (raisedExhaustion bool) {
	h.mu.Lock()
	defer h.mu.Unlock()

	// An endpoint-less controller (no expansion yet — a hostname-only peer before its first
	// resolution) has nothing to evaluate or exhaust: neither advance nor signal.
	total := h.flatLenLocked()
	if total == 0 {
		return false
	}

	if !h.allDownLocked() {
		// The active concentrator is reachable on at least one path: clear the down-episode
		// tracking so a subsequent outage starts a fresh wrap/exhaustion evaluation, clear the
		// down-onset so the next episode's dwell is timed from its OWN transition into allDown, and
		// re-arm the exhaustion latch so a fresh outage re-signals.
		h.downAdvances = 0
		h.exhaustedLatched = false
		h.downSince = time.Time{}
		return false
	}

	now := h.clock.Now()
	// Record the ONSET of this hub-loss episode (the transition INTO allDown). The single-endpoint
	// exhaustion dwell is measured from this onset — see the total == 1 branch below.
	if h.downSince.IsZero() {
		h.downSince = now
	}

	// GUARD (total == 1, single-endpoint peer): take NO round-robin action — no advance, no remote
	// repoint, no re-handshake — so a one-concentrator, single-endpoint deployment is byte-for-byte
	// the pre-T57 behaviour. Its sole endpoint being allDown persisting PAST the settle dwell IS
	// endpoint-list exhaustion (R267), so the signal — and ONLY the signal — is raised for it. The
	// dwell here is measured from the OUTAGE ONSET (downSince), NOT lastSwitch: a single-endpoint peer
	// never switches, so lastSwitch is frozen at construction and would only ever dwell the BOOT
	// outage — a mid-life fresh outage would then exhaust on its first allDown reading. Gating on the
	// onset gives every fresh outage its full settle dwell, so a sub-second blackout blip cannot raise
	// exhaustion (which T269's sticky auto-promotion would treat as a permanent move off this exit).
	if total < 2 {
		if now.Sub(h.downSince) < h.settle {
			return false
		}
		return h.raiseExhaustedLocked()
	}

	// Settle dwell (total >= 2): give the currently-active endpoint time for its liveness to recover
	// before advancing, so a not-yet-echoed (transiently DOWN) healthy endpoint is not skipped. Applies
	// at boot (endpoint 0) and after every switch. This gate is measured from lastSwitch (re-armed on
	// every advance), so a full exhaustion wrap already requires `total` settle-separated advances of
	// CONTINUOUS hub loss (>= total*settle of persistence). That advance cadence already imposes
	// settle-scale onset persistence, so the onset (downSince) gate is deliberately NOT additionally
	// applied to the multi-endpoint wrap path: it would be redundant and would perturb the existing
	// multi-endpoint advance timing.
	if now.Sub(h.lastSwitch) < h.settle {
		return false
	}

	// MULTI-endpoint peer (total >= 2): advance in FLATTENED order from the active entry's current
	// flattened position. The position is re-derived from the spec-scoped identity every step, so a
	// standby spec that grew/shrank since the last advance is walked at its new offset. A missing
	// active (activeSpec == -1, or its AddrPort no longer present) resumes from the head.
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

	// Endpoint-list-exhaustion detection (R267): a FULL flattened-list wrap — `total` advances under
	// CONTINUOUS hub loss (downAdvances is reset the instant any path recovers, above) — means every
	// configured endpoint has been tried and found down. Raise the signal once per outage (the latch
	// suppresses repeats), then re-arm the wrap counter so the round-robin keeps retrying regardless.
	h.downAdvances++
	if h.downAdvances >= total {
		h.downAdvances = 0
		return h.raiseExhaustedLocked()
	}
	return false
}

// raiseExhaustedLocked latches the endpoint-list-exhaustion state, returning true EXACTLY on the
// rising edge into that state so the caller fires onExhausted once per outage. The latch clears when
// any path to the active concentrator recovers (checkLocked's not-allDown branch), so a fresh outage
// re-signals. Caller holds h.mu.
func (h *hubFailover) raiseExhaustedLocked() bool {
	if h.exhaustedLatched {
		return false
	}
	h.exhaustedLatched = true
	return true
}

// EndpointState is one entry of the FLATTENED ordered endpoint list returned by
// EndpointsSnapshot: its address and whether it is the currently ACTIVE endpoint.
type EndpointState struct {
	Addr   netip.AddrPort
	Active bool
}

// EndpointsSnapshot returns the FLATTENED ordered endpoint list (spec order, then
// within-spec expansion order — the same order flatIndexLocked/entryAtLocked walk) under
// h.mu, marking exactly the entry at h.idx active. h.idx is the flatIndexLocked-derived
// cache of the (activeSpec, activeAddr) spec-scoped identity (R70), already re-mapped after
// every mutation (updateResolution, check), so this reuses that single source of truth
// rather than re-deriving activeness per entry — a DNS-expanded hostname spec therefore
// renders every current expansion record in order, with only its live entry marked active.
// No entry is marked active when h.idx == -1 (activeSpec == -1: every spec's expansion is
// still empty, e.g. a hostname-only peer before its first resolution).
func (h *hubFailover) EndpointsSnapshot() []EndpointState {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]EndpointState, 0, h.flatLenLocked())
	flat := 0
	for si := range h.specs {
		for _, a := range h.specs[si].addrs {
			out = append(out, EndpointState{Addr: a, Active: flat == h.idx})
			flat++
		}
	}
	return out
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

// composeRehandshakes fans ONE trigger out to every supplied per-peer rehandshake, in order. The
// edge multi-peer bring-up (T251/Q68b) composes one deviceRehandshake per configured concentrator
// peer so a single first-path-up edge initiates to ALL of them, not just the primary. It is a pure
// combinator over the rehandshake collaborator (no engine dependency), so a test drives it with
// counters to prove every peer's initiation fires.
func composeRehandshakes(rhs []rehandshake) rehandshake {
	return func() {
		for _, rh := range rhs {
			rh()
		}
	}
}

// deviceRehandshakeAllPeers returns a rehandshake that forces a fresh WG handshake initiation
// against EVERY configured peer's static key — the edge multi-peer warm bring-up (T251/Q68b):
// the first-path-up latch drives all N concentrator sessions warm concurrently, not only the
// primary (peers[0]). It composes the per-peer deviceRehandshake for each peer, so every peer's
// keypair expiry + initiation is byte-identical to the single-peer path; a single-peer edge yields
// exactly one, matching the pre-T251 primary-only trigger.
func deviceRehandshakeAllPeers(dev *awgdevice.Device, peers []config.Peer) rehandshake {
	rhs := make([]rehandshake, len(peers))
	for i, p := range peers {
		rhs[i] = deviceRehandshake(dev, p.PublicKey)
	}
	return composeRehandshakes(rhs)
}

// startFirstPathUpHandshake wires the bind's first-path-up latch (T117, Multipath.SetOnFirstPathUp)
// to a forced WG handshake initiation against the edge's single concentrator peer (D37/T120).
//
// Motivation: amneziawg-go's own boot-time initiation can race the bind's Open — the FIRST
// SendHandshakeInitiation, issued before any path telemetry exists, may hit "no healthy path"
// (bind: ErrNoHealthyPath, the D37 symptom) and get dropped, yet the engine still stamps
// peer.lastSentHandshake. A bare retry shortly after can then be silently suppressed by the
// engine's RekeyTimeout guard, leaving the tunnel to wait out the engine's own ~5s retransmit
// timer instead of re-initiating the moment a path is actually usable. rh reuses the
// deviceRehandshake pattern: ExpireCurrentKeypairs backdates lastSentHandshake so the immediately
// following initiation is never suppressed by that guard; on a cold boot with no keypairs yet it
// is a no-op, so this only ever helps, never disrupts, an established session.
//
// The caller MUST register this before mp.StartProbeLoop can flip any path Up: the callback's
// edge is NOT retroactive (T117 — SetOnFirstPathUp does not fire for an edge that already
// happened), so registering it any later could race the very first probe response and silently
// miss the one moment this exists to catch.
//
// rh is the injected rehandshake collaborator (mirroring newHubFailoverFromSpecs's rehandshake
// parameter) so a unit test can substitute a counter for deviceRehandshake — this function itself
// needs no *awgdevice.Device at all, only mp and cfg.Role. It is a no-op for the concentrator
// role: the concentrator is the responder to every edge and initiates nothing
// (startFailoverAndResolution's concentrator no-op stays untouched).
func startFirstPathUpHandshake(cfg *config.Config, mp *bind.Multipath, rh rehandshake) {
	if cfg.Role != config.RoleEdge {
		return
	}
	mp.SetOnFirstPathUp(rh)
}

// deviceInstallEndpoint returns an install function that populates the ENGINE peer's endpoint via
// the UAPI/IpcSet path — an `endpoint=` line for pk routed through the engine to
// Multipath.ParseEndpoint (R70). This is the ONLY way to give the engine peer an addressable
// endpoint: SetPeerRemote repoints the bind's per-path remotes but leaves the engine peer's
// endpoint untouched, so after an endpoint-less tolerant boot a bare rehandshake has no endpoint
// to transmit to. The device installs the FIRST resolved endpoint through this before the first
// rehandshake; ParseEndpoint also seeds the (previously remoteless) per-path remotes with it, so
// the initiation egresses toward the resolved address. Re-issuing `public_key=` for an existing
// peer only updates its endpoint — it does not reset keys or allowed-ips. An IpcSet failure is
// logged; the loop retries on the next resolve. It stays in the device package alongside the rest
// of the engine wiring, mirroring deviceRehandshake.
func deviceInstallEndpoint(dev *awgdevice.Device, pk config.Key, lg log.Logger) func(netip.AddrPort) {
	raw := pk.Bytes()
	pubHex := hex.EncodeToString(raw[:])
	return func(ap netip.AddrPort) {
		if err := dev.IpcSet(fmt.Sprintf("public_key=%s\nendpoint=%s\n", pubHex, ap.String())); err != nil {
			lg.Warn("hub failover: failed to install resolved endpoint on the engine peer",
				"endpoint", ap.String(), "error", err.Error())
		}
	}
}

// peerRemoteFor adapts Multipath.SetPeerRemoteFor (the T252 per-peer repoint seam) to the
// peerRemote interface for ONE named peer, so a per-peer hub-failover controller repoints ONLY its
// own peer's per-path remotes — never the bind-global default or another peer's remotes (D100). It
// is the multi-exit-edge dual of handing the controller *bind.Multipath directly (whose SetPeerRemote
// drives the primary/single-peer bind-global path). A cross-peer collision (SetPeerRemoteFor's
// fail-fast guard) is a wiring defect, so it is LOGGED rather than silently dropped; SetPeerRemoteFor
// leaves all state untouched on that error, so nothing is half-repointed.
type peerRemoteFor struct {
	mp   *bind.Multipath
	name string
	log  log.Logger
}

func (p peerRemoteFor) SetPeerRemote(ap netip.AddrPort) {
	if err := p.mp.SetPeerRemoteFor(p.name, ap); err != nil {
		p.log.Warn("hub failover: per-peer remote repoint failed",
			"peer", p.name, "endpoint", ap.String(), "error", err.Error())
	}
}

// deviceInstallEndpointFor is the MULTI-EXIT edge install collaborator (the FIRST-RESOLVE INSTALL
// PATH, R70) for one named peer. Unlike the single-peer deviceInstallEndpoint — which only issues the
// engine `endpoint=` line and relies on the bind-global default — it FIRST routes the resolved
// endpoint through the T252 per-peer seam (SetPeerRemoteFor), which seeds edgePeerByRemote +
// configuredRemote and repoints the peer's paths, THEN issues the engine `endpoint=` line. Order is
// load-bearing: the engine's IpcSet calls Bind.ParseEndpoint(ap), which resolves the OWNING peer's
// virt through edgePeerByRemote — so an endpoint-less (hostname-only) peer whose remote was NOT seeded
// at boot must be keyed to this peer BEFORE ParseEndpoint runs, or ParseEndpoint would misresolve it
// onto the primary's virt and its WG traffic would egress to the wrong concentrator (exactly the D100
// cross-wiring). A SetPeerRemoteFor error (cross-peer collision — a wiring defect) is logged and the
// engine install is skipped, leaving all state untouched.
func deviceInstallEndpointFor(dev *awgdevice.Device, mp *bind.Multipath, name string, pk config.Key, lg log.Logger) func(netip.AddrPort) {
	ipcInstall := deviceInstallEndpoint(dev, pk, lg)
	return func(ap netip.AddrPort) {
		if err := mp.SetPeerRemoteFor(name, ap); err != nil {
			lg.Warn("hub failover: per-peer endpoint install (seam repoint) failed; skipping engine install",
				"peer", name, "endpoint", ap.String(), "error", err.Error())
			return
		}
		ipcInstall(ap)
	}
}

// composeStops fans one aggregate stopper out to every supplied per-peer stopper (in order). It lets
// startFailoverAndResolution return ONE stopFailover / stopResolution that halts EVERY per-peer loop,
// while the device retains its existing two-stopper Close contract. Each supplied stopper is already
// idempotent (startHubFailoverLoop / startResolutionLoop guard with sync.Once), so the composed
// stopper is too.
func composeStops(stops []func()) func() {
	return func() {
		for _, s := range stops {
			s()
		}
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

// failoverDeps supplies, per eligible peer index i, the engine/bind collaborators a hub-failover
// controller needs — that peer's OWN liveness plane, remote-repoint seam, WG rehandshake, engine-
// endpoint install, and clock. It abstracts *bind.Multipath + *awgdevice.Device + the per-(peer,path)
// prober sets OUT of the per-peer wiring loop (startFailoverAndResolution), so that loop — and its
// per-peer isolation contract (D100: EVERY eligible peer gets its own controller, none dropped) — is
// exercisable by a wiring test with fakes, while production wires it to the real bind + engine
// (deviceFailoverDeps). i indexes cfg.WireGuard.Peers; name is ids[i].Name; multiExit is len(peers) > 1
// (whether the per-peer T252 seam applies rather than the bind-global legacy path).
type failoverDeps interface {
	// health is peer i's OWN liveness plane (one hubHealth per that peer's probe path). An EMPTY slice
	// means peer i has no probe transport, so the loop builds no controller for it (pre-G5 behaviour).
	health(i int) []hubHealth
	// remote is peer i's repoint seam: the bind-global SetPeerRemote for a single-peer edge, or the
	// per-peer SetPeerRemoteFor seam (keyed by name) for a multi-exit edge (D100).
	remote(i int, name string, multiExit bool) peerRemote
	// rehandshake is peer i's WG re-handshake, keyed by that peer's public key.
	rehandshake(i int) rehandshake
	// install is peer i's engine-endpoint install (the FIRST-RESOLVE INSTALL PATH, R70): the plain
	// engine install for a single-peer edge, or the seeded per-peer install path for a multi-exit edge.
	install(i int, name string, multiExit bool) func(netip.AddrPort)
	// clock is the controllers' time source (production: the system clock).
	clock() telemetry.Clock
}

// deviceFailoverDeps is the PRODUCTION failoverDeps: it wires each eligible peer's controller to the
// real bind (mp), that peer's per-(peer,path) prober set (perPeerProbers[i]), and the engine (dev),
// exactly as the pre-refactor inline loop did — the seam selection (bind-global vs per-peer T252) and
// the public-key-keyed rehandshake/install are byte-for-byte identical.
type deviceFailoverDeps struct {
	cfg            *config.Config
	mp             *bind.Multipath
	perPeerProbers [][]*telemetry.Prober
	dev            *awgdevice.Device
	lg             log.Logger
}

func (d deviceFailoverDeps) health(i int) []hubHealth {
	probers := d.perPeerProbers[i]
	health := make([]hubHealth, len(probers))
	for j, pr := range probers {
		health[j] = pr
	}
	return health
}

func (d deviceFailoverDeps) remote(i int, name string, multiExit bool) peerRemote {
	if multiExit {
		return peerRemoteFor{mp: d.mp, name: name, log: d.lg.Component("hubfailover")}
	}
	return d.mp
}

func (d deviceFailoverDeps) rehandshake(i int) rehandshake {
	return deviceRehandshake(d.dev, d.cfg.WireGuard.Peers[i].PublicKey)
}

func (d deviceFailoverDeps) install(i int, name string, multiExit bool) func(netip.AddrPort) {
	hlg := d.lg.Component("hubfailover")
	pk := d.cfg.WireGuard.Peers[i].PublicKey
	if multiExit {
		return deviceInstallEndpointFor(d.dev, d.mp, name, pk, hlg)
	}
	return deviceInstallEndpoint(d.dev, pk, hlg)
}

func (d deviceFailoverDeps) clock() telemetry.Clock { return telemetry.SystemClock{} }

// startFailoverAndResolution builds and starts, PER ELIGIBLE PEER, an edge-side hub-failover monitor
// AND its re-resolution controller (T253/D100), returning two AGGREGATE stoppers (device.Close invokes
// both — each fans out to every per-peer loop) AND the per-peer controllers keyed by stable peer name.
// The keyed map lets the monitor layer wire a LIVE endpoints provider over each controller's
// EndpointsSnapshot (T222) and lets the cross-concentrator exit selector (T269) subscribe to each
// controller's exhaustion signal. It returns no-op stoppers and a nil map when hub failover does not
// apply anywhere: the concentrator role (it roams edges dynamically and has no endpoint list), a bind
// without the probe transport (no probers → no liveness plane), or no peer that warrants a controller.
//
// A peer warrants a controller when it can round-robin / re-resolve (peerNeedsHubFailover: >=2
// literals or a hostname) OR — on a MULTI-EXIT edge — it is an exit-capable (mode=default-route) peer
// carrying a SINGLE literal endpoint (R267). That single-literal exit peer can never fail over, but
// its sole endpoint staying down past the dwell IS endpoint-list exhaustion, the signal T269's
// cross-concentrator auto-promotion acts on; it gets an EXHAUSTION-ONLY controller whose poll runs
// (startHubFailoverLoop exhaustionOnly) purely to raise that signal — no advance/repoint/rehandshake.
// A SINGLE-peer edge, and any non-exit single-literal peer, get NO controller, staying
// behaviour-identical to pre-G5.
//
// Per-peer isolation (D100 structurally gone): EVERY eligible peer — not merely the first qualifying
// one — gets its OWN controller wired to that peer's OWN per-(peer,path) prober set (perPeerProbers[i],
// its OWN liveness plane — hub loss = every one of THAT peer's probers DOWN), its OWN rehandshake and
// engine-endpoint install keyed by that peer's public key, and, on a MULTI-EXIT edge, its OWN remote
// seam (SetPeerRemoteFor / the seeded install path, NOT the bind-global SetPeerRemote — so peer B's hub
// switch never disturbs the remote peer A relies on). A SINGLE-peer edge keeps the legacy bind-global
// SetPeerRemote / plain engine-install path byte-for-byte (its Bind primary peer is unnamed, so the
// per-peer seam does not apply). ids is index-aligned with cfg.WireGuard.Peers (config.PeerIdentities),
// supplying each peer's stable name — the SetPeerRemoteFor / peersByName key and the map key.
//
// Each controller's spec expansions come from boot (the bounded initial resolve, Q30): a hostname that
// resolved at boot starts with a non-empty expansion (activeSpec set, endpoint already installed at
// boot via the UAPI render), while one that did not resolve starts EMPTY (activeSpec == -1) and the
// re-resolution loop's first success adopts it through the FIRST-RESOLVE INSTALL PATH (ctrl.install,
// R70) — now correctly routed per-peer, so a NON-primary endpoint-less peer is driven by its OWN
// controller instead of never (or cross-wiring onto the primary's virt): the D100 mechanism.
func startFailoverAndResolution(cfg *config.Config, deps failoverDeps, ids []config.PeerIdentity, boot bootEndpoints, lg log.Logger) (stopFailover, stopResolution func(), ctrls map[string]*hubFailover) {
	noop := func() {}
	if cfg.Role != config.RoleEdge {
		return noop, noop, nil
	}
	// Multi-exit edge (2+ peers): each per-peer controller repoints/installs through the T252 per-peer
	// seam keyed by the peer's stable name. A single-peer edge keeps the bind-global legacy path.
	multiExit := len(cfg.WireGuard.Peers) > 1
	// Exit-capable (mode=default-route) peers, by config-order index — the peers whose endpoint-list
	// exhaustion the cross-concentrator exit selector (T269) promotes off. On a multi-exit edge such a
	// peer needs an EXHAUSTION-ONLY controller even when it carries a single literal endpoint (R267:
	// the minimal one-endpoint-per-concentrator topology), which peerNeedsHubFailover alone does not
	// cover.
	exitCapable := make(map[int]bool)
	if multiExit {
		for _, i := range exitCapablePeerIndices(cfg) {
			exitCapable[i] = true
		}
	}
	ctrls = make(map[string]*hubFailover)
	var failStops, resStops []func()
	for i, peer := range cfg.WireGuard.Peers {
		// A peer warrants a controller when it can round-robin/re-resolve (peerNeedsHubFailover) OR —
		// on a multi-exit edge — it is an exit-capable peer with a single literal endpoint: it can
		// never fail over, but its sole endpoint going down past the dwell IS endpoint-list exhaustion
		// (R267), the signal T269's auto-promotion acts on. A SINGLE-peer edge, and any non-exit
		// single-literal peer, keep the pre-G5 no-controller behaviour (byte-identical).
		exhaustionOnly := !peerNeedsHubFailover(peer) && exitCapable[i]
		if !peerNeedsHubFailover(peer) && !exhaustionOnly {
			continue
		}
		health := deps.health(i)
		if len(health) == 0 {
			// This peer has no probe transport (a bind built without probers): no liveness plane, so
			// no hub-loss detector. Skip it — the controllerless behaviour is byte-for-byte pre-G5.
			continue
		}
		name := ids[i].Name
		hlg := lg.Component("hubfailover")

		// The remote-repoint + engine-endpoint install seams come from deps: on a MULTI-EXIT edge the
		// per-peer SetPeerRemoteFor / seeded install path (D100), on a single-peer edge the bind-global
		// SetPeerRemote / plain engine install. The engine-endpoint install (the FIRST-RESOLVE INSTALL
		// PATH, R70) is a REQUIRED constructor collaborator — boot adoption of an endpoint-less peer
		// installs the resolved endpoint on the engine peer through it, then rehandshakes — so passing it
		// at construction (not patching a field afterwards) keeps a lost wiring line a compile error.
		ctrl := newHubFailoverFromSpecs(
			boot.specs[i], health,
			deps.remote(i, name, multiExit),
			deps.rehandshake(i),
			deps.install(i, name, multiExit),
			deps.clock(), hubFailoverSettle,
			hlg,
		)
		// Poll at the probe cadence: check is cheap (a length check plus a liveness sweep) and the
		// settle dwell bounds actual switches, so a responsive poll only tightens detection latency
		// without churning the remote. exhaustionOnly forces the poll to run for a single-literal exit
		// peer (which canFailoverLocked would otherwise leave un-polled) so its total==1 exhaustion is
		// still detected — signal only, no round-robin (R267/T269).
		failStops = append(failStops, ctrl.startHubFailoverLoop(telemetry.DefaultProbeInterval, exhaustionOnly))
		// Alongside failover, start the re-resolution controller (T73) over THIS peer's ctrl for a
		// peer carrying hostname specs: it re-resolves each on the [dns] poll cadence and out-of-band
		// on hub loss, feeding fresh records through ctrl.updateResolution (Q34 — the two coordinate
		// purely through the shared lock and update API), so it updates ONLY this peer's specs. An
		// all-literal peer has no hostname to track, so it starts nothing.
		resStops = append(resStops, startResolution(cfg, ctrl, peer, boot.resolver, lg))
		ctrls[name] = ctrl
	}
	if len(ctrls) == 0 {
		return noop, noop, nil
	}
	return composeStops(failStops), composeStops(resStops), ctrls
}

// startResolution builds and starts the re-resolution controller for peer's hostname endpoint
// specs over the already-constructed hub-failover controller, or returns a no-op stopper when the
// peer carries no hostname spec (an all-literal peer never re-resolves) or the resolver could not
// be constructed (nil — logged at boot). The resolver is the one built ONCE at boot from the
// (validated) [dns] block and shared with the bounded initial resolve, so hostname re-resolution
// never fails tunnel bring-up: hub failover itself is independent of re-resolution.
func startResolution(cfg *config.Config, ctrl *hubFailover, peer config.Peer, resolver dnsresolve.Resolver, lg log.Logger) func() {
	targets := nameTargetsFromSpecs(peer.EndpointSpecs)
	if len(targets) == 0 || resolver == nil {
		return func() {}
	}
	res := newResolution(
		resolver, ctrl, targets, pathFamiliesFromPaths(cfg.Paths),
		telemetry.SystemClock{}, cfg.DNS.PollInterval, cfg.DNS.Timeout,
		lg.Component("dnsresolve"),
	)
	// Drive step at the probe cadence: step is cheap in the steady state (a liveness read plus a
	// clock compare) and gates the actual poll on the [dns] poll interval internally, so a
	// responsive tick only tightens the liveness-loss re-resolve latency without over-resolving.
	return res.startResolutionLoop(telemetry.DefaultProbeInterval)
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
//
// exhaustionOnly overrides the canFailoverLocked gate for the R267/T269 single-literal EXIT peer
// (one literal endpoint, so it can NEVER round-robin): its poll must still run so check's total==1
// branch can raise endpoint-list EXHAUSTION when that sole endpoint stays down past the dwell — the
// signal-only path (no advance/repoint/rehandshake) the cross-concentrator exit selector promotes
// off. Such a controller runs iff it has an endpoint to exhaust (flatLen >= 1); a zero-endpoint
// controller has nothing to signal, so the loop stays a no-op.
func (h *hubFailover) startHubFailoverLoop(interval time.Duration, exhaustionOnly bool) (stop func()) {
	h.mu.Lock()
	run := h.canFailoverLocked() || (exhaustionOnly && h.flatLenLocked() >= 1)
	h.mu.Unlock()
	if !run || interval <= 0 {
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
