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
// split-by-destination policy hooks. T269 (auto-promotion) and T255 (composition)
// build on Switch/ActiveExit next.
type exitSelector struct {
	engine ipcSetter
	log    log.Logger

	mu     sync.Mutex
	active string              // name of the exit peer currently owning the default route
	exits  map[string]exitPeer // exit-capable peers keyed by configured name
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
	for _, i := range idx {
		peer := cfg.WireGuard.Peers[i]
		pub := peer.PublicKey.Bytes()
		exits[ids[i].Name] = exitPeer{
			publicKeyHex: hex.EncodeToString(pub[:]),
			splits:       defaultRouteSplits(peer.AllowedIPs),
		}
	}
	return &exitSelector{
		engine: engine,
		log:    lg.Component("exitselector"),
		active: ids[idx[0]].Name,
		exits:  exits,
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

// Switch moves default-route ownership to the named exit-capable peer. It
// validates name is a configured exit-capable peer (returning a typed
// *unknownExitError and mutating NOTHING otherwise), no-ops idempotently when name
// is already active, and otherwise issues ONE IpcSet inserting the peer's /1
// default-route splits — steal-on-insert repoints ownership atomically per prefix,
// so the previous owner loses the /1s while both peers keep their inner /32, with
// no replace_allowed_ips and no re-handshake. Every effected switch logs at Info
// with from/to.
func (s *exitSelector) Switch(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

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
	s.log.Info("active exit switched", "from", from, "to", name)
	return nil
}
