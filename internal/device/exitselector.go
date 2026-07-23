package device

import (
	"encoding/hex"
	"fmt"
	"strings"
	"sync"

	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/log"
)

// ipcSetter is the engine seam exitSelector.Switch repoints default-route
// ownership through: a single IpcSet that inserts the /1 default-route split
// entries onto the target peer. *awgdevice.Device satisfies it; a fake satisfies
// it in unit tests so Switch's idempotence and error paths run without a live
// engine.
type ipcSetter interface {
	IpcSet(string) error
}

// exitPeer is one exit-capable peer's render state the selector needs to move
// default-route ownership onto it: the lowercase-hex public key that selects the
// peer in a UAPI SET, and the /1 default-route split entries (v4 and/or v6 exactly
// as configured) it carries when it is the active exit. The splits are IDENTICAL
// across exit-capable peers (T250 rule 2: with N>1 exits every exit peer must list
// the same default-route entry set), but are computed per-peer for robustness.
type exitPeer struct {
	publicKeyHex string
	splits       []string
}

// exitSelector owns WHICH exit-capable peer carries the default route on a
// multi-exit edge (G28/M105, Q69a). Exactly one exit peer is "active" at a time
// and owns the wg-quick-style /1+/1 default-route split in the engine's
// allowed-ips trie; the others are WARM STANDBYS carrying only their inner /32
// (kept warm end-to-end by keepalive, provable via the T261 inner ping). Switch
// repoints ownership by IpcSet-ing the /1 splits onto the target peer: WireGuard's
// allowed-ips trie STEALS each prefix from its current owner on insert
// (steal-on-insert), so the previous owner loses it atomically per prefix WITHOUT
// a replace_allowed_ips (which would wipe the target's inner /32) and WITHOUT a
// re-handshake (the target session is already warm). Kernel routes are untouched —
// all peers share the one wanbond0 TUN, so the trie alone decides which peer a
// default-route packet egresses to.
//
// The API is deliberately narrow (Q69 future scope): a single active exit, no
// split-by-destination policy hooks. T255 (composition) builds on Switch/ActiveExit;
// auto-promotion (T269) subscribes onActiveExhausted to the ACTIVE exit's controller.
type exitSelector struct {
	engine ipcSetter
	log    log.Logger

	mu     sync.Mutex
	active string              // name of the exit peer currently owning the default route
	exits  map[string]exitPeer // exit-capable peers keyed by configured name
	// order is the exit-capable peer names in CONFIG order — the deterministic order the
	// auto-promotion standby search walks (Q75: "first HEALTHY exit-capable peer in config
	// order"). Fixed at construction. Guarded by mu (read only, never mutated).
	order []string
	// ctrls are the per-peer hub-failover controllers keyed by exit-peer name — the endpoint-
	// list-exhaustion seam auto-promotion subscribes onActiveExhausted to. nil until
	// enableAutoPromotion wires it (the single-exit / concentrator shapes leave auto-promotion
	// off). A peer without a controller (single-endpoint exit) is absent from the map: it has no
	// exhaustion signal, so it is simply never subscribed. Guarded by mu.
	ctrls map[string]exitController
	// health answers the warm-standby health question during standby selection (healthy = WG
	// session established + at least one path up). nil until enableAutoPromotion wires it.
	// Guarded by mu.
	health exitHealth
}

// exitController is the per-peer hub-failover controller seam the exit selector subscribes its
// endpoint-list-exhaustion trigger to (SetOnExhausted) and RE-SUBSCRIBES on every Switch so the
// signal always tracks the current active exit. *hubFailover satisfies it; a fake drives the
// promotion path in unit tests. The callback fires OUTSIDE the controller's own mu (see
// hubFailover.check), so the selector may take its own mu — and re-subscribe (which re-takes a
// controller mu) — from inside the callback without inverting the s.mu -> controller.mu order.
type exitController interface {
	SetOnExhausted(cb func())
}

// exitHealth reports whether a candidate exit-capable peer is a HEALTHY warm standby fit for
// auto-promotion: its WG session is established AND at least one of its paths is up (Q75). The
// device wires it over the live engine (per-peer last-handshake age) and each exit peer's own
// liveness plane; a fake drives the selector's promotion logic in unit tests.
type exitHealth interface {
	healthy(name string) bool
}

// unknownExitError is the typed error exitSelector.Switch returns when the
// requested name is not a configured exit-capable peer — an unknown name, or a
// configured peer that is not mode=default-route. Switch mutates no state before
// returning it.
type unknownExitError struct {
	name string
}

func (e *unknownExitError) Error() string {
	return fmt.Sprintf("exitselector: %q is not a configured exit-capable peer", e.name)
}

// newExitSelector builds the active-exit selector for cfg over the engine, or
// returns nil when the selector does not apply: fewer than two exit-capable
// (mode=default-route) peers, or a non-edge role (mode=default-route is edge-only).
// The boot-active exit is the FIRST exit-capable peer in config order (Q74, no
// persistence) — the SAME peer uapiConfig's standbyExitPeers boot render leaves
// owning the default route, so the selector's notion of the active owner matches
// the engine trie at boot. engine must already carry every peer (IpcSet from
// uapiConfig has run) so Switch's insert REPOINTS an existing peer rather than
// creating one.
func newExitSelector(cfg *config.Config, engine ipcSetter, lg log.Logger) *exitSelector {
	idx := exitCapablePeerIndices(cfg)
	if len(idx) < 2 {
		return nil
	}
	ids := cfg.PeerIdentities()
	exits := make(map[string]exitPeer, len(idx))
	order := make([]string, len(idx))
	for k, i := range idx {
		peer := cfg.WireGuard.Peers[i]
		pub := peer.PublicKey.Bytes()
		exits[ids[i].Name] = exitPeer{
			publicKeyHex: hex.EncodeToString(pub[:]),
			splits:       defaultRouteSplits(peer.AllowedIPs),
		}
		order[k] = ids[i].Name
	}
	return &exitSelector{
		engine: engine,
		log:    lg.Component("exitselector"),
		active: ids[idx[0]].Name,
		exits:  exits,
		order:  order,
	}
}

// defaultRouteSplits returns the /1 split entries for the DEFAULT-ROUTE (/0)
// allowed_ips only (0.0.0.0/0 -> 0.0.0.0/1+128.0.0.0/1, ::/0 -> ::/1+8000::/1),
// reusing the same splitDefaultRoute helper uapiConfig renders the engine with, so
// a Switch installs byte-for-byte the split the active peer's boot render carried.
// Non-default entries (the inner /32) are excluded: they are the peer's OWN warm-
// standby address and must never move.
func defaultRouteSplits(allowedIPs []string) []string {
	var splits []string
	for _, cidr := range allowedIPs {
		if !isDefaultRoute(cidr) {
			continue
		}
		splits = append(splits, splitDefaultRoute(cidr)...)
	}
	return splits
}

// ActiveExit reports the name of the exit-capable peer currently owning the
// default route. Exposed for the monitor (T255).
func (s *exitSelector) ActiveExit() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active
}

// Switch moves default-route ownership to the named exit-capable peer on OPERATOR request (the
// manual UI switch, T258/T260). It is the manual entry point onto switchLocked (reason=manual);
// see switchLocked for the steal-on-insert mechanics. A manual switch WINS over auto-promotion:
// it re-subscribes the exhaustion trigger onto the chosen peer, and a subsequently-firing stale
// exhaustion signal for the peer we moved off is a no-op (onActiveExhausted's active-guard).
func (s *exitSelector) Switch(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.switchLocked(name, "manual")
}

// switchLocked moves default-route ownership to the named exit-capable peer under s.mu, tagging
// the cause (reason) into the Info switch log so an operator's manual switch (reason=manual) and
// a health-driven promotion (reason=auto-promotion) are distinguishable in logs. It validates
// name is a configured exit-capable peer (returning a typed *unknownExitError and mutating
// NOTHING otherwise), no-ops idempotently when name is already active, and otherwise issues ONE
// IpcSet inserting the peer's /1 default-route splits — steal-on-insert repoints ownership
// atomically per prefix, so the previous owner loses the /1s while both peers keep their inner
// /32, with no replace_allowed_ips and no re-handshake. On an effected switch it RE-SUBSCRIBES
// the exhaustion trigger onto the new active exit's controller so auto-promotion always tracks
// the current active exit. Caller holds s.mu.
func (s *exitSelector) switchLocked(name, reason string) error {
	target, ok := s.exits[name]
	if !ok {
		return &unknownExitError{name: name}
	}
	if name == s.active {
		// The target already owns the default route: no trie change is needed and a
		// redundant IpcSet is avoided entirely.
		return nil
	}
	from := s.active

	// Select the target peer by public key and INSERT the /1 splits. No
	// replace_allowed_ips (it would wipe the target's inner /32); the bare
	// allowed_ip inserts repoint each prefix off its current owner via the trie's
	// steal-on-insert.
	var b strings.Builder
	fmt.Fprintf(&b, "public_key=%s\n", target.publicKeyHex)
	for _, split := range target.splits {
		fmt.Fprintf(&b, "allowed_ip=%s\n", split)
	}
	if err := s.engine.IpcSet(b.String()); err != nil {
		return fmt.Errorf("exitselector: switch default route from %q to %q: %w", from, name, err)
	}
	s.active = name
	s.resubscribeLocked(from, name)
	s.log.Info("active exit switched", "from", from, "to", name, "reason", reason)
	return nil
}

// enableAutoPromotion wires health-driven auto-promotion (T269): it records the per-peer
// exhaustion-signal controllers (keyed by exit-peer name) and the warm-standby health seam, then
// subscribes the exhaustion trigger onto the CURRENTLY-ACTIVE exit's controller. Called ONCE at
// boot after the controllers exist. nil ctrls or nil health leaves auto-promotion disabled (the
// single-exit / no-controller shapes). A peer absent from ctrls (a single-endpoint exit with no
// controller) simply carries no exhaustion signal and is never subscribed.
func (s *exitSelector) enableAutoPromotion(ctrls map[string]exitController, health exitHealth) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ctrls = ctrls
	s.health = health
	s.subscribeActiveLocked()
}

// subscribeActiveLocked subscribes onActiveExhausted onto the CURRENTLY-ACTIVE exit's controller
// (if it has one). The closure captures the active peer name so a stale signal that fires after
// egress has already moved off that peer is a no-op (onActiveExhausted's active-guard). Caller
// holds s.mu; SetOnExhausted takes the controller's own mu (s.mu -> controller.mu order).
func (s *exitSelector) subscribeActiveLocked() {
	if s.ctrls == nil {
		return
	}
	if c, ok := s.ctrls[s.active]; ok {
		active := s.active
		c.SetOnExhausted(func() { s.onActiveExhausted(active) })
	}
}

// resubscribeLocked moves the exhaustion trigger from the previous active exit's controller to
// the new one so auto-promotion always tracks the current active exit (called under s.mu on
// every effected switch, manual or auto). The previous peer's subscription is CLEARED so its
// controller stops driving the selector, and the new peer's is armed (if it has a controller). A
// peer without a controller is skipped — it has no exhaustion signal. No-op when auto-promotion
// is not wired (ctrls nil). Caller holds s.mu.
func (s *exitSelector) resubscribeLocked(from, to string) {
	if s.ctrls == nil {
		return
	}
	if c, ok := s.ctrls[from]; ok {
		c.SetOnExhausted(nil)
	}
	if c, ok := s.ctrls[to]; ok {
		c.SetOnExhausted(func() { s.onActiveExhausted(to) })
	}
}

// onActiveExhausted is the endpoint-list-exhaustion handler auto-promotion subscribes onto the
// CURRENTLY-ACTIVE exit's controller. exhausted is the peer name the firing controller belongs
// to (captured at subscription). It auto-promotes egress to the first HEALTHY warm-standby
// exit-capable peer in config order — reusing switchLocked's steal-on-insert repoint (NO
// re-handshake; the standby is already warm) — and logs the move at Info with
// reason=auto-promotion. It is a NO-OP when:
//   - the exhausted peer is no longer the active exit: a manual Switch or a prior promotion
//     already moved egress off it. MANUAL WINS — a stale signal never overrides a standing
//     choice, and this is also the NO-AUTO-FAILBACK guard (the promoted peer stays active until
//     IT itself exhausts).
//   - no standby is healthy: egress stays on the dead exit (nothing to promote to) and the
//     condition is logged once per outage (the controller latch suppresses repeats) — do NOT
//     thrash.
//
// It runs on the firing controller's poll goroutine, which has ALREADY released that controller's
// mu (hubFailover.check fires the callback outside the lock), so taking s.mu here and
// re-subscribing (which re-takes controller mus, including the firing one's, to clear it) cannot
// deadlock against the caller.
func (s *exitSelector) onActiveExhausted(exhausted string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.active != exhausted {
		return
	}
	target := s.firstHealthyStandbyLocked()
	if target == "" {
		s.log.Info("active exit endpoint list exhausted; no healthy warm standby to promote to — egress stays on the failed exit",
			"from", exhausted, "reason", "auto-promotion")
		return
	}
	if err := s.switchLocked(target, "auto-promotion"); err != nil {
		s.log.Warn("auto-promotion switch failed", "from", exhausted, "to", target, "error", err.Error())
	}
}

// firstHealthyStandbyLocked returns the name of the first HEALTHY exit-capable peer in config
// order that is not the current active exit, or "" when none is healthy. Caller holds s.mu.
func (s *exitSelector) firstHealthyStandbyLocked() string {
	if s.health == nil {
		return ""
	}
	for _, name := range s.order {
		if name == s.active {
			continue
		}
		if s.health.healthy(name) {
			return name
		}
	}
	return ""
}
