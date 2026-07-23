package device

import (
	"net/netip"
	"strings"
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

// T255 (G28/M105, Q72 composition proof): this file proves the two-level composition SCOPED to
// WITHIN-concentrator (non-exhausting) endpoint failure. exitSelector (T254) owns WHICH exit-
// capable peer (a concentrator, in this shape) carries the default route in the engine's
// allowed-ips trie; hubFailover (T253) owns WHICH of that concentrator's OWN endpoints the bond's
// wire remote currently points at. The claim under test: when the SELECTED concentrator's active
// endpoint fails but its endpoint list is NOT exhausted, hubFailover advances among that
// concentrator's own endpoints while exitSelector's active-exit pointer, and the engine's trie
// ownership it tracks, do not move — the two controllers read/write DISJOINT state (the trie vs
// the bind's per-peer remote seam) and so never fight. Cross-concentrator auto-promotion on
// EXHAUSTION (T269) is a distinct, out-of-scope claim.

// twoNamedPeerCompositionEngine builds a REAL vendored amneziawg-go engine (channel TUN) over a
// bind.Multipath carrying TWO NAMED bind-level peers "a" and "b" — mirroring the exact naming a
// multi-exit edge's production wiring gives each configured exit peer (SetPrimaryPeerName renames
// the embedded primary to the first exit peer's name, AddConcentratorPeer registers each
// additional one, T252/D101). Each carries its own single-path AlwaysUp scheduler so no liveness
// goroutine ever runs. Neither peer's paths are ever Open()ed: both IpcSet/IpcGet (an allowed_ip
// insert lands in the trie at IpcSet time regardless of the device's up-state) and
// SetPeerRemoteFor ("safe on a CLOSED bind (no paths)", per its doc comment) work without a live
// socket, so this test drives neither traffic nor a goroutine — only the two disjoint planes T255
// composes: the engine trie and the bind's per-peer remote seam.
func twoNamedPeerCompositionEngine(t *testing.T, lg log.Logger) (*awgdevice.Device, *bind.Multipath) {
	t.Helper()
	paths := []config.Path{{Name: "p", SourceAddr: netip.MustParseAddr("127.0.0.1")}}

	pskA := keyFromRaw(t, mustRandom(t, 32))
	schedA, err := sched.NewActiveBackup([]sched.PathHealth{sched.AlwaysUp{}}, sched.Config{FailbackAfter: time.Second}, telemetry.SystemClock{}, lg)
	if err != nil {
		t.Fatalf("build scheduler a: %v", err)
	}
	mp, err := bind.NewMultipath(paths, pskA, schedA, nil, nil, nil, nil, config.Amnezia{}, lg)
	if err != nil {
		t.Fatalf("build multipath bind: %v", err)
	}
	if err := mp.SetPrimaryPeerName("a"); err != nil {
		t.Fatalf("SetPrimaryPeerName(a): %v", err)
	}

	pskB := keyFromRaw(t, mustRandom(t, 32))
	schedB, err := sched.NewActiveBackup([]sched.PathHealth{sched.AlwaysUp{}}, sched.Config{FailbackAfter: time.Second}, telemetry.SystemClock{}, lg)
	if err != nil {
		t.Fatalf("build scheduler b: %v", err)
	}
	if err := mp.AddConcentratorPeer("b", pskB, schedB, nil, nil); err != nil {
		t.Fatalf("AddConcentratorPeer(b): %v", err)
	}

	chtun := tuntest.NewChannelTUN()
	dev := awgdevice.NewDevice(chtun.TUN(), mp, engineLogger(lg, "error", mp.EverHadLivePath))
	t.Cleanup(dev.Close)
	return dev, mp
}

// peerRawBlocks parses a UAPI GET dump into each peer's ordered, VERBATIM "key=value" lines
// (excluding the public_key line itself), keyed by the lowercase-hex public key the block begins
// with. It is the byte-level comparison unit TestWithinConcentratorFailoverKeepsSelectionOnA uses
// to prove peer B's WHOLE engine record — allowed-ips, endpoint, handshake state, everything the
// engine renders for it — is byte-identical before and after peer A's endpoint failover, a
// stronger claim than checking any single field in isolation.
func peerRawBlocks(dump string) map[string][]string {
	out := map[string][]string{}
	cur := ""
	for _, line := range strings.Split(dump, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if key == "public_key" {
			cur = val
			if _, seen := out[cur]; !seen {
				out[cur] = []string{}
			}
			continue
		}
		if cur != "" {
			out[cur] = append(out[cur], line)
		}
	}
	return out
}

// diffPeerBlocks reports the first line present in one snapshot but not at the same position in
// the other (order-sensitive: the engine renders a peer's fields in a stable order, so a
// reordering is itself a change worth catching), or "" if the two are identical.
func diffPeerBlocks(before, after []string) string {
	if len(before) != len(after) {
		return "line count changed"
	}
	for i := range before {
		if before[i] != after[i] {
			return "line " + before[i] + " -> " + after[i]
		}
	}
	return ""
}

// TestWithinConcentratorFailoverKeepsSelectionOnA is the T255 acceptance: concentrator "a" is
// selected as the default exit (exitSelector, T254) and carries 2 endpoints (hubFailover, T253).
// Driving A's ACTIVE endpoint down past the settle dwell — with A's SECOND endpoint healthy, so
// A's endpoint list is NOT exhausted — must advance A's OWN endpoint-failover controller to A's
// standby endpoint via A's own remote seam (SetPeerRemoteFor) while:
//   - exitSelector.ActiveExit() stays == "a" (no cross-concentrator move — that is T269's
//     exhaustion-only auto-promotion, out of scope here);
//   - the /1+/1 default-route splits stay under "a" in the engine's allowed-ips trie (IpcGet),
//     never touching "b";
//   - peer "b" is byte-untouched: its ENTIRE engine record (splits, endpoint, handshake state) is
//     identical before and after, and its bind-level remote is never repointed (B's hub-failover
//     seam is simply never invoked in this test — a structural, not incidental, guarantee).
//
// This is the within-concentrator (non-exhausting) half of the two-level composition: exitSelector
// and A's hubFailover controller read/write DISJOINT state (the trie vs. the per-peer remote seam)
// and so never fight over which peer is "active".
func TestWithinConcentratorFailoverKeepsSelectionOnA(t *testing.T) {
	lg := discardLogger(t)
	cfg, aHex, bHex := twoExitEdgeConfig(t)

	dev, mp := twoNamedPeerCompositionEngine(t, lg)
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

	// A's OWN per-peer hub-failover controller (T253), wired through the REAL production per-peer
	// remote seam (peerRemoteFor -> mp.SetPeerRemoteFor("a", ...)) — not a fake — so the repoint
	// half of the composition is the genuine production code path. Health and clock are
	// hand-driven fakes (this is a unit-level composition proof, not an e2e).
	epsA := mustEndpoints(t, "203.0.113.1:51820", "203.0.113.2:51820")
	hpA := []hubHealth{&fakeHealth{telemetry.StateUp}, &fakeHealth{telemetry.StateUp}}
	handshakesA := 0
	clk := &fakeClock{now: time.Unix(1000, 0)}
	foA := newHubFailover(epsA, hpA, peerRemoteFor{mp: mp, name: "a", log: lg}, func() { handshakesA++ }, clk, testSettle, lg)
	exhaustionsA := 0
	foA.SetOnExhausted(func() { exhaustionsA++ })
	if foA.activeAddr != epsA[0] {
		t.Fatalf("boot active endpoint = %v, want %v (first endpoint)", foA.activeAddr, epsA[0])
	}

	// Snapshot peer B's whole engine record BEFORE A's failover, for the byte-identical
	// before/after comparison below.
	bBefore := peerRawBlocks(mustIpcGet(t, dev))[bHex]
	if bBefore == nil {
		t.Fatalf("peer b block not found in boot IpcGet dump")
	}

	// Drive A's ACTIVE endpoint down (both configured paths DOWN — hub loss for A specifically),
	// past the settle dwell, with A's SECOND endpoint untried: this is a single advance out of 2,
	// NOT a full wrap, so A's endpoint list is explicitly NOT exhausted (T269's auto-promotion
	// would trigger only on exhaustion, and is out of scope for this task).
	hpA[0].(*fakeHealth).state = telemetry.StateDown
	hpA[1].(*fakeHealth).state = telemetry.StateDown
	clk.advance(testSettle + time.Second)
	foA.check()

	if foA.idx != 1 || foA.activeAddr != epsA[1] {
		t.Fatalf("A's endpoint-failover did not advance to its own standby: idx=%d activeAddr=%v, want idx=1 addr=%v",
			foA.idx, foA.activeAddr, epsA[1])
	}
	if handshakesA != 1 {
		t.Fatalf("A's endpoint failover re-handshakes=%d, want exactly 1", handshakesA)
	}
	if exhaustionsA != 0 {
		t.Fatalf("A's endpoint list raised exhaustion (%d signals) after a single (non-wrapping) advance — it must NOT be exhausted with a healthy second endpoint untried", exhaustionsA)
	}

	// Egress selection (exitSelector) is UNMOVED: still "a", both at the API level and in the
	// engine's own allowed-ips trie — the /1+/1 splits never crossed to "b".
	if got := sel.ActiveExit(); got != "a" {
		t.Fatalf("ActiveExit() after A's within-concentrator endpoint failover = %q, want %q (no cross-concentrator move)", got, "a")
	}
	dumpAfter := mustIpcGet(t, dev)
	owned := perPeerAllowedIPs(dumpAfter)
	for _, split := range []string{"0.0.0.0/1", "128.0.0.0/1"} {
		if !owned[aHex][split] {
			t.Fatalf("after A's endpoint failover, %s not owned by a; a owns %v", split, keys(owned[aHex]))
		}
		if owned[bHex][split] {
			t.Fatalf("after A's endpoint failover, %s leaked to b; b owns %v", split, keys(owned[bHex]))
		}
	}
	if !owned[aHex]["10.0.0.1/32"] {
		t.Fatalf("peer a lost its inner /32 across the endpoint failover; a owns %v", keys(owned[aHex]))
	}
	if !owned[bHex]["10.0.1.1/32"] {
		t.Fatalf("peer b lost its inner /32 across the endpoint failover; b owns %v", keys(owned[bHex]))
	}

	// Peer b is byte-untouched: its WHOLE engine record (every rendered field, in order) is
	// identical before and after — a stronger check than the trie-only assertion above.
	bAfter := peerRawBlocks(dumpAfter)[bHex]
	if diff := diffPeerBlocks(bBefore, bAfter); diff != "" {
		t.Fatalf("peer b's engine record changed across A's endpoint failover: %s\nbefore=%v\nafter=%v", diff, bBefore, bAfter)
	}

	// Peer b's bind-level remote is untouched by construction: this test never calls
	// SetPeerRemoteFor("b", ...) or constructs any hub-failover controller for b — A's controller
	// is wired ONLY through peerRemoteFor{mp, "a", ...}, so a repoint of A structurally cannot
	// reach b's bind-level state (the per-peer isolation SetPeerRemoteFor itself enforces is
	// pinned at the bind layer by TestSetPeerRemoteForRepointsOnlyTargetPeer; this composition
	// test's contribution is the engine-trie + exit-selector half above).
}
