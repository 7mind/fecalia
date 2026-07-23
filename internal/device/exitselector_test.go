package device

import (
	"encoding/hex"
	"errors"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	awgdevice "github.com/amnezia-vpn/amneziawg-go/device"
	"github.com/amnezia-vpn/amneziawg-go/tun/tuntest"

	"github.com/7mind/wanbond/internal/bind"
	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/log"
	"github.com/7mind/wanbond/internal/sched"
	"github.com/7mind/wanbond/internal/telemetry"
)

// newAllowedIPsTestEngine builds a REAL vendored amneziawg-go engine over a channel TUN and a
// minimal single-path multipath Bind (AlwaysUp scheduler, no probers), for exercising the
// allowed-ips trie via IpcSet/IpcGet WITHOUT bringing the device up (an allowed_ip insert lands in
// the trie at IpcSet time regardless of the device's up-state, so no sockets are needed). The
// caller Closes the returned device.
func newAllowedIPsTestEngine(t *testing.T, lg log.Logger) *awgdevice.Device {
	t.Helper()
	psk := keyFromRaw(t, mustRandom(t, 32))
	paths := []config.Path{{Name: "a", SourceAddr: netip.MustParseAddr("127.0.0.1")}}
	scheduler, err := sched.NewActiveBackup([]sched.PathHealth{sched.AlwaysUp{}}, sched.Config{FailbackAfter: time.Second}, telemetry.SystemClock{}, lg)
	if err != nil {
		t.Fatalf("build scheduler: %v", err)
	}
	mp, err := bind.NewMultipath(paths, psk, scheduler, nil, nil, nil, nil, config.Amnezia{}, lg)
	if err != nil {
		t.Fatalf("build multipath bind: %v", err)
	}
	chtun := tuntest.NewChannelTUN()
	dev := awgdevice.NewDevice(chtun.TUN(), mp, engineLogger(lg, "error", mp.EverHadLivePath))
	t.Cleanup(dev.Close)
	return dev
}

// perPeerAllowedIPs parses an amneziawg UAPI GET dump into a per-peer set of allowed_ip CIDRs,
// keyed by the lowercase-hex public key each peer block begins with (the same form uapiConfig
// renders). An allowed_ip line is attributed to the most recent public_key= line, mirroring the
// engine's IpcGet block layout.
func perPeerAllowedIPs(dump string) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	cur := ""
	for _, line := range strings.Split(dump, "\n") {
		key, val, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch key {
		case "public_key":
			cur = val
			if _, seen := out[cur]; !seen {
				out[cur] = map[string]bool{}
			}
		case "allowed_ip":
			if cur != "" {
				out[cur][val] = true
			}
		}
	}
	return out
}

// recordingIpcSetter is a fake ipcSetter that records every IpcSet payload, so a test can assert
// exactly when Switch does — and does NOT — touch the engine (the idempotent no-op and the
// typed-error paths must issue no IpcSet at all).
type recordingIpcSetter struct {
	mu    sync.Mutex
	calls []string
}

func (r *recordingIpcSetter) IpcSet(s string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, s)
	return nil
}

func (r *recordingIpcSetter) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

// twoExitEdgeConfig builds a directly-constructed multi-exit edge Config with two exit-capable
// (mode=default-route) peers "a" and "b", each carrying the SAME default-route set (0.0.0.0/0) plus
// its own distinct inner /32 (the R255 shape). It returns the config and the two peers' lowercase-
// hex public keys (a, b) for asserting per-peer trie ownership. Built as a struct (not via
// config.Load) so the test owns the exact keys it asserts against; the multi-exit invariants this
// config satisfies are validated separately by the config package.
func twoExitEdgeConfig(t *testing.T) (cfg *config.Config, aHex, bHex string) {
	t.Helper()
	privRaw, _ := genX25519(t)
	_, aPubRaw := genX25519(t)
	_, bPubRaw := genX25519(t)
	cfg = &config.Config{
		Role: config.RoleEdge,
		WireGuard: config.WireGuard{
			PrivateKey: keyFromRaw(t, privRaw),
			Peers: []config.Peer{
				{
					PublicKey:  keyFromRaw(t, aPubRaw),
					AllowedIPs: []string{"0.0.0.0/0", "10.0.0.1/32"},
					Mode:       config.PeerModeDefaultRoute,
					Name:       "a",
					PSK:        keyFromRaw(t, mustRandom(t, 32)),
				},
				{
					PublicKey:  keyFromRaw(t, bPubRaw),
					AllowedIPs: []string{"0.0.0.0/0", "10.0.1.1/32"},
					Mode:       config.PeerModeDefaultRoute,
					Name:       "b",
					PSK:        keyFromRaw(t, mustRandom(t, 32)),
				},
			},
		},
	}
	return cfg, hex.EncodeToString(aPubRaw), hex.EncodeToString(bPubRaw)
}

func TestExitCapablePeerNamesPreservesConfigOrder(t *testing.T) {
	cfg, _, _ := twoExitEdgeConfig(t)
	got := exitCapablePeerNames(cfg, cfg.PeerIdentities())
	if joined := strings.Join(got, ","); joined != "a,b" {
		t.Fatalf("exitCapablePeerNames = %q, want %q", joined, "a,b")
	}

	cfg.Role = config.RoleConcentrator
	if got := exitCapablePeerNames(cfg, cfg.PeerIdentities()); len(got) != 0 {
		t.Fatalf("concentrator exitCapablePeerNames = %v, want empty", got)
	}
}

// TestExitSelectorStealOnInsert is T254 acceptance (a): it proves the vendored amneziawg-go engine
// REPOINTS ownership of an allowed-ips prefix on insert (steal-on-insert) — an IpcSet inserting a
// prefix already owned by peer A onto peer B moves it to B and removes it from A, per prefix, with
// no replace_allowed_ips. The whole active-exit design rests on this; if the fork deviated, the
// task must STOP rather than build on the assumption.
func TestExitSelectorStealOnInsert(t *testing.T) {
	lg := discardLogger(t)
	dev := newAllowedIPsTestEngine(t, lg)

	privRaw, _ := genX25519(t)
	_, aPubRaw := genX25519(t)
	_, bPubRaw := genX25519(t)
	aHex := hex.EncodeToString(aPubRaw)
	bHex := hex.EncodeToString(bPubRaw)

	// Boot: A owns the default-route /1 split PLUS its inner /32; B owns only its inner /32.
	var boot strings.Builder
	boot.WriteString("private_key=" + hex.EncodeToString(privRaw) + "\n")
	boot.WriteString("public_key=" + aHex + "\nallowed_ip=0.0.0.0/1\nallowed_ip=128.0.0.0/1\nallowed_ip=10.0.0.1/32\n")
	boot.WriteString("public_key=" + bHex + "\nallowed_ip=10.0.1.1/32\n")
	if err := dev.IpcSet(boot.String()); err != nil {
		t.Fatalf("IpcSet boot config: %v", err)
	}

	// Insert the /1 splits onto B (NO replace_allowed_ips) — the steal-on-insert operation.
	if err := dev.IpcSet("public_key=" + bHex + "\nallowed_ip=0.0.0.0/1\nallowed_ip=128.0.0.0/1\n"); err != nil {
		t.Fatalf("IpcSet steal onto B: %v", err)
	}

	dump, err := dev.IpcGet()
	if err != nil {
		t.Fatalf("IpcGet: %v", err)
	}
	owned := perPeerAllowedIPs(dump)

	// The /1s are now under B and NO LONGER under A; each peer retains its inner /32.
	for _, split := range []string{"0.0.0.0/1", "128.0.0.0/1"} {
		if !owned[bHex][split] {
			t.Fatalf("after steal, %s not owned by B (steal-on-insert did not repoint); B owns %v", split, keys(owned[bHex]))
		}
		if owned[aHex][split] {
			t.Fatalf("after steal, %s still owned by A (fork did not steal on insert); A owns %v — STOP: the exit-selector design assumption is invalid on this fork", split, keys(owned[aHex]))
		}
	}
	if !owned[aHex]["10.0.0.1/32"] {
		t.Fatalf("peer A lost its inner /32 on the steal; A owns %v", keys(owned[aHex]))
	}
	if !owned[bHex]["10.0.1.1/32"] {
		t.Fatalf("peer B lost its inner /32 on the steal; B owns %v", keys(owned[bHex]))
	}
}

// TestUapiConfigMultiExitStandbyBootRender pins the T254 boot render half of acceptance (b): a
// multi-exit edge's FIRST exit peer boots owning the /1 default-route split (plus its inner /32),
// while the STANDBY exit peer boots with ONLY its inner /32 (the /1 splits stripped). This is the
// pure render assertion; the engine-level ownership after a Switch is asserted in
// TestExitSelectorSwitchRepointsDefaultRoute.
func TestUapiConfigMultiExitStandbyBootRender(t *testing.T) {
	cfg, aHex, bHex := twoExitEdgeConfig(t)
	boot := []bootEndpoint{{}, {}}

	uapi, err := uapiConfig(cfg, boot)
	if err != nil {
		t.Fatalf("uapiConfig: %v", err)
	}
	owned := perPeerAllowedIPs(uapi)

	// Active exit "a" (first in config order): /1 splits + inner /32.
	for _, want := range []string{"0.0.0.0/1", "128.0.0.0/1", "10.0.0.1/32"} {
		if !owned[aHex][want] {
			t.Fatalf("boot-active exit a missing %s; a renders %v", want, keys(owned[aHex]))
		}
	}
	// Standby exit "b": inner /32 ONLY — the /1 splits are stripped so only one peer owns the
	// default route at boot.
	if !owned[bHex]["10.0.1.1/32"] {
		t.Fatalf("standby exit b missing its inner /32; b renders %v", keys(owned[bHex]))
	}
	for _, stripped := range []string{"0.0.0.0/1", "128.0.0.0/1"} {
		if owned[bHex][stripped] {
			t.Fatalf("standby exit b rendered the default-route split %s; it must boot with only its inner /32 (T254). b renders %v", stripped, keys(owned[bHex]))
		}
	}
}

// TestExitSelectorSwitchRepointsDefaultRoute is the engine-level half of T254 acceptance (b): boot
// a real engine from uapiConfig for a two-exit edge (a active, b standby), then Switch("b") and
// assert IpcGet shows the /1 default-route split under peer b ONLY, with both peers retaining their
// inner /32 — and ActiveExit() tracks the move. No re-handshake is issued (the target is warm): the
// switch is a bare allowed_ip insert.
func TestExitSelectorSwitchRepointsDefaultRoute(t *testing.T) {
	lg := discardLogger(t)
	cfg, aHex, bHex := twoExitEdgeConfig(t)

	dev := newAllowedIPsTestEngine(t, lg)
	boot, err := uapiConfig(cfg, []bootEndpoint{{}, {}})
	if err != nil {
		t.Fatalf("uapiConfig: %v", err)
	}
	if err := dev.IpcSet(boot); err != nil {
		t.Fatalf("IpcSet boot config: %v", err)
	}

	sel := newExitSelector(cfg, dev, lg)
	if sel == nil {
		t.Fatalf("newExitSelector returned nil for a two-exit edge")
	}
	if got := sel.ActiveExit(); got != "a" {
		t.Fatalf("boot ActiveExit() = %q, want %q (first exit peer in config order)", got, "a")
	}

	// Sanity: at boot the /1s are under a only (matching the render test) — a precondition for the
	// steal to be observable.
	pre := perPeerAllowedIPs(mustIpcGet(t, dev))
	if !pre[aHex]["0.0.0.0/1"] || pre[bHex]["0.0.0.0/1"] {
		t.Fatalf("boot trie unexpected: a owns /1=%v, b owns /1=%v", pre[aHex]["0.0.0.0/1"], pre[bHex]["0.0.0.0/1"])
	}

	if err := sel.Switch("b"); err != nil {
		t.Fatalf("Switch(b): %v", err)
	}
	if got := sel.ActiveExit(); got != "b" {
		t.Fatalf("after Switch(b), ActiveExit() = %q, want %q", got, "b")
	}

	owned := perPeerAllowedIPs(mustIpcGet(t, dev))
	for _, split := range []string{"0.0.0.0/1", "128.0.0.0/1"} {
		if !owned[bHex][split] {
			t.Fatalf("after Switch(b), %s not under b; b owns %v", split, keys(owned[bHex]))
		}
		if owned[aHex][split] {
			t.Fatalf("after Switch(b), %s still under a; a owns %v", split, keys(owned[aHex]))
		}
	}
	if !owned[aHex]["10.0.0.1/32"] {
		t.Fatalf("peer a lost its inner /32 on the switch; a owns %v", keys(owned[aHex]))
	}
	if !owned[bHex]["10.0.1.1/32"] {
		t.Fatalf("peer b lost its inner /32 on the switch; b owns %v", keys(owned[bHex]))
	}
}

// TestExitSelectorSwitchUnknownRejected is T254 acceptance (c): Switch to a name that is not a
// configured exit-capable peer — an entirely unknown name, OR a configured peer that is not
// mode=default-route — returns a typed *unknownExitError and changes NOTHING (no IpcSet, active
// exit unmoved).
func TestExitSelectorSwitchUnknownRejected(t *testing.T) {
	lg := discardLogger(t)
	cfg, _, _ := twoExitEdgeConfig(t)
	// Append a NON-exit peer (no mode) so "plain" is configured but not exit-capable.
	_, plainPubRaw := genX25519(t)
	cfg.WireGuard.Peers = append(cfg.WireGuard.Peers, config.Peer{
		PublicKey:  keyFromRaw(t, plainPubRaw),
		AllowedIPs: []string{"10.0.2.0/24"},
		Name:       "plain",
		PSK:        keyFromRaw(t, mustRandom(t, 32)),
	})

	engine := &recordingIpcSetter{}
	sel := newExitSelector(cfg, engine, lg)
	if sel == nil {
		t.Fatalf("newExitSelector returned nil for a two-exit edge")
	}

	for _, name := range []string{"nope", "plain"} {
		err := sel.Switch(name)
		if err == nil {
			t.Fatalf("Switch(%q) returned nil, want a typed unknownExitError", name)
		}
		var ue *unknownExitError
		if !errors.As(err, &ue) {
			t.Fatalf("Switch(%q) error = %T (%v), want *unknownExitError", name, err, err)
		}
		if got := sel.ActiveExit(); got != "a" {
			t.Fatalf("Switch(%q) moved the active exit to %q; a rejected switch must change nothing", name, got)
		}
		if engine.count() != 0 {
			t.Fatalf("Switch(%q) issued %d IpcSet call(s); a rejected switch must not touch the engine", name, engine.count())
		}
	}
}

// TestExitSelectorSwitchToActiveIsNoOp is T254 acceptance (d): Switch to the CURRENTLY active exit
// is an idempotent no-op — it returns nil, issues no IpcSet (the target already owns the default
// route), and leaves the active exit unchanged.
func TestExitSelectorSwitchToActiveIsNoOp(t *testing.T) {
	lg := discardLogger(t)
	cfg, _, _ := twoExitEdgeConfig(t)

	engine := &recordingIpcSetter{}
	sel := newExitSelector(cfg, engine, lg)
	if sel == nil {
		t.Fatalf("newExitSelector returned nil for a two-exit edge")
	}
	if got := sel.ActiveExit(); got != "a" {
		t.Fatalf("boot ActiveExit() = %q, want %q", got, "a")
	}

	if err := sel.Switch("a"); err != nil {
		t.Fatalf("Switch(a) (current active) returned %v, want nil (idempotent no-op)", err)
	}
	if engine.count() != 0 {
		t.Fatalf("Switch to the current active issued %d IpcSet call(s); it must be a no-op", engine.count())
	}
	if got := sel.ActiveExit(); got != "a" {
		t.Fatalf("Switch(a) changed the active exit to %q, want it unchanged at %q", got, "a")
	}
}

// TestNewExitSelectorNilForSingleExit pins that the selector does NOT apply below two exit-capable
// peers: a single-exit edge (one mode=default-route peer) and a non-edge role both yield nil, so the
// single-exit and concentrator shapes carry no exit-selector state.
func TestNewExitSelectorNilForSingleExit(t *testing.T) {
	lg := discardLogger(t)
	privRaw, _ := genX25519(t)
	_, pubRaw := genX25519(t)
	cfg := &config.Config{
		Role: config.RoleEdge,
		WireGuard: config.WireGuard{
			PrivateKey: keyFromRaw(t, privRaw),
			Peers: []config.Peer{{
				PublicKey:  keyFromRaw(t, pubRaw),
				AllowedIPs: []string{"0.0.0.0/0"},
				Mode:       config.PeerModeDefaultRoute,
			}},
		},
	}
	if sel := newExitSelector(cfg, &recordingIpcSetter{}, lg); sel != nil {
		t.Fatalf("newExitSelector for a single-exit edge = non-nil, want nil (the selector does not apply below 2 exit peers)")
	}
	cfg.Role = config.RoleConcentrator
	if sel := newExitSelector(cfg, &recordingIpcSetter{}, lg); sel != nil {
		t.Fatalf("newExitSelector for a concentrator = non-nil, want nil (mode=default-route is edge-only)")
	}
}

// mustIpcGet reads the engine's UAPI dump or fails the test.
func mustIpcGet(t *testing.T, dev *awgdevice.Device) string {
	t.Helper()
	dump, err := dev.IpcGet()
	if err != nil {
		t.Fatalf("IpcGet: %v", err)
	}
	return dump
}

// keys renders a set's keys for a failure message.
func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
