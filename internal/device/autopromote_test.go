package device

import (
	"encoding/hex"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	awgdevice "github.com/amnezia-vpn/amneziawg-go/device"

	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/log"
	"github.com/7mind/wanbond/internal/telemetry"
)

// infoCapturingLogger builds an INFO-level logger over a syncBuffer so an auto-promotion test can
// assert the reason=auto-promotion / reason=manual switch records.
func infoCapturingLogger(t *testing.T) (log.Logger, *syncBuffer) {
	t.Helper()
	buf := &syncBuffer{}
	lg, err := log.New("info", buf)
	if err != nil {
		t.Fatalf("log.New: %v", err)
	}
	return lg, buf
}

// fakeExitHealth is a hand-driven exitHealth so a test can declare exactly which warm-standby exit
// peers are HEALTHY (session established + a path up) without a live engine/liveness plane — the
// standby-selection logic (first healthy in config order) is then exercised deterministically.
type fakeExitHealth struct {
	mu      sync.Mutex
	healthy map[string]bool
}

func newFakeExitHealth() *fakeExitHealth { return &fakeExitHealth{healthy: map[string]bool{}} }

func (f *fakeExitHealth) set(name string, ok bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.healthy[name] = ok
}

func (f *fakeExitHealth) isHealthy(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.healthy[name]
}

// autoPromoteHarness wires a REAL amneziawg engine (booted from the two-exit uapiConfig so
// steal-on-insert is real), an exit selector over it, and a REAL per-peer hub-failover controller
// for the boot-active exit "a" (two literal endpoints, fake clock + fake per-path liveness), then
// enables auto-promotion subscribing the selector to a's exhaustion signal. Driving hpA down and
// stepping the fake clock past the settle dwell exhausts a's endpoint list, firing the promotion
// synchronously on ctrlA.check().
type autoPromoteHarness struct {
	dev    *awgdevice.Device
	sel    *exitSelector
	ctrlA  *hubFailover
	hpA    []*fakeHealth
	clkA   *fakeClock
	remA   *recordingRemote
	health *fakeExitHealth
	buf    *syncBuffer
	aHex   string
	bHex   string
}

func newAutoPromoteHarness(t *testing.T) *autoPromoteHarness {
	t.Helper()
	lg, buf := infoCapturingLogger(t)
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
		t.Fatalf("boot ActiveExit() = %q, want %q", got, "a")
	}

	// Controller for exit "a": two literal endpoints so exhaustion requires a FULL wrap (a single
	// advance is a within-concentrator T57 failover step, NOT exhaustion — acceptance (a)).
	epsA := mustEndpoints(t, "203.0.113.1:51820", "203.0.113.2:51820")
	hpA := []*fakeHealth{{telemetry.StateUp}, {telemetry.StateUp}}
	remA := &recordingRemote{}
	clkA := &fakeClock{now: time.Unix(1000, 0)}
	ctrlA := newHubFailover(epsA, []hubHealth{hpA[0], hpA[1]}, remA, func() {}, clkA, testSettle, lg)

	health := newFakeExitHealth()
	sel.enableAutoPromotion(
		map[string]exitController{"a": ctrlA},
		exitHealthFunc(health.isHealthy),
	)

	return &autoPromoteHarness{
		dev: dev, sel: sel, ctrlA: ctrlA, hpA: hpA, clkA: clkA, remA: remA,
		health: health, buf: buf, aHex: aHex, bHex: bHex,
	}
}

// exitHealthFunc adapts a bare predicate to the exitHealth interface.
type exitHealthFunc func(name string) bool

func (f exitHealthFunc) healthy(name string) bool { return f(name) }

// driveADown sets both of a's paths DOWN.
func (h *autoPromoteHarness) driveADown() {
	h.hpA[0].state = telemetry.StateDown
	h.hpA[1].state = telemetry.StateDown
}

// stepA advances a's clock past the settle dwell and runs one failover-evaluation step (which
// fires the exhaustion callback synchronously on a full wrap).
func (h *autoPromoteHarness) stepA() {
	h.clkA.advance(testSettle + time.Second)
	h.ctrlA.check()
}

// assertOwnsDefaultRoute asserts the /1+/1 default-route split is owned by wantHex and NOT by
// otherHex, and that each peer retains its own inner /32 (no teardown of the demoted exit).
func (h *autoPromoteHarness) assertOwnsDefaultRoute(t *testing.T, wantHex, otherHex, wantInner, otherInner string) {
	t.Helper()
	owned := perPeerAllowedIPs(mustIpcGet(t, h.dev))
	for _, split := range []string{"0.0.0.0/1", "128.0.0.0/1"} {
		if !owned[wantHex][split] {
			t.Fatalf("%s not owned by the active exit; it owns %v", split, keys(owned[wantHex]))
		}
		if owned[otherHex][split] {
			t.Fatalf("%s still owned by the demoted exit; it owns %v", split, keys(owned[otherHex]))
		}
	}
	if !owned[wantHex][wantInner] {
		t.Fatalf("active exit lost its inner /32 %s; it owns %v", wantInner, keys(owned[wantHex]))
	}
	if !owned[otherHex][otherInner] {
		t.Fatalf("demoted exit lost its inner /32 %s (unexpected teardown); it owns %v", otherInner, keys(owned[otherHex]))
	}
}

// TestExitSelectorAutoPromoteOnExhaustion is the T269 core acceptance: with exit "a" active and a
// HEALTHY warm standby "b", driving ALL of a's endpoints down PAST exhaustion auto-promotes egress
// to b — the /1 default-route split moves under b (not a) via steal-on-insert, a keeps its inner
// /32 (no teardown), ActiveExit() reflects b, and the switch logs at Info with reason=auto-promotion
// from=a to=b.
func TestExitSelectorAutoPromoteOnExhaustion(t *testing.T) {
	h := newAutoPromoteHarness(t)
	h.health.set("b", true) // b is a healthy warm standby.

	h.driveADown()
	// Two advances of a 2-endpoint list under continuous hub loss: 0->1 (first advance, not yet a
	// wrap), then 1->0 (full wrap = endpoint-list exhaustion → auto-promotion fires).
	h.stepA()
	if got := h.sel.ActiveExit(); got != "a" {
		t.Fatalf("after a single within-concentrator advance ActiveExit() = %q, want a (not yet exhausted)", got)
	}
	h.stepA()

	if got := h.sel.ActiveExit(); got != "b" {
		t.Fatalf("after exhaustion ActiveExit() = %q, want b (auto-promoted to the healthy warm standby)", got)
	}
	h.assertOwnsDefaultRoute(t, h.bHex, h.aHex, "10.0.1.1/32", "10.0.0.1/32")

	out := h.buf.String()
	if !strings.Contains(out, `"reason":"auto-promotion"`) || !strings.Contains(out, `"from":"a"`) || !strings.Contains(out, `"to":"b"`) {
		t.Fatalf("auto-promotion switch not logged with reason/from/to; log:\n%s", out)
	}
}

// TestExitSelectorSingleAdvanceDoesNotPromote is T269 acceptance (a): a within-concentrator T57
// failover advance (one endpoint tried on hub loss, NOT a full endpoint-list wrap) must NOT
// promote. The controller repoints a's remote onto its standby endpoint (T57 advances) while the
// active exit stays a and no auto-promotion log is emitted — proving the trigger is EXHAUSTION,
// not any endpoint failure.
func TestExitSelectorSingleAdvanceDoesNotPromote(t *testing.T) {
	h := newAutoPromoteHarness(t)
	h.health.set("b", true) // healthy standby exists, yet a partial failure must not use it.

	h.driveADown()
	h.stepA() // single advance 0 -> 1: within-concentrator failover, not exhaustion.

	if h.remA.calls != 1 {
		t.Fatalf("controller advance count = %d, want 1 (a's own T57 failover advanced)", h.remA.calls)
	}
	if got := h.sel.ActiveExit(); got != "a" {
		t.Fatalf("ActiveExit() = %q, want a (a single-endpoint advance must not promote)", got)
	}
	if strings.Contains(h.buf.String(), `"reason":"auto-promotion"`) {
		t.Fatalf("auto-promotion fired on a single within-concentrator advance; log:\n%s", h.buf.String())
	}
}

// TestExitSelectorManualSwitchWins is T269 acceptance (b): a MANUAL Switch during the failure
// window WINS and stands — a subsequent full exhaustion of the (now former) active exit does not
// override the operator's standing choice.
func TestExitSelectorManualSwitchWins(t *testing.T) {
	h := newAutoPromoteHarness(t)
	h.health.set("b", true)

	// Failure window opens: a's paths go down and one within-concentrator advance happens.
	h.driveADown()
	h.stepA()
	if got := h.sel.ActiveExit(); got != "a" {
		t.Fatalf("precondition ActiveExit() = %q, want a", got)
	}

	// Operator manually switches to b DURING the window (before a exhausts).
	if err := h.sel.Switch("b"); err != nil {
		t.Fatalf("manual Switch(b): %v", err)
	}
	if got := h.sel.ActiveExit(); got != "b" {
		t.Fatalf("after manual Switch(b) ActiveExit() = %q, want b", got)
	}
	if !strings.Contains(h.buf.String(), `"reason":"manual"`) {
		t.Fatalf("manual switch not logged with reason=manual; log:\n%s", h.buf.String())
	}

	// Make the DEMOTED exit "a" a HEALTHY promotion candidate. This is what makes the assertion
	// non-vacuous: a stale exhaustion signal for "a" now HAS a target to (wrongly) fail back to, so
	// only the manual-wins enforcement — the SetOnExhausted(nil) clear on the manual switch AND
	// onActiveExhausted's active-guard — keeps egress on b. Without this line, firstHealthyStandby
	// returns "" regardless, and deleting BOTH guards survives (nothing to promote to).
	h.health.set("a", true)

	// a now fully exhausts. The stale exhaustion signal must NOT move egress off the operator's b —
	// even though a is (per the health seam) a healthy candidate.
	h.stepA() // wrap 1 -> 0 -> exhaustion on a's controller.
	if got := h.sel.ActiveExit(); got != "b" {
		t.Fatalf("a's exhaustion overrode the manual choice: ActiveExit() = %q, want b (MANUAL WINS)", got)
	}
	h.assertOwnsDefaultRoute(t, h.bHex, h.aHex, "10.0.1.1/32", "10.0.0.1/32")
}

// TestExitSelectorNoAutoFailback is T269 acceptance (c) and (d): after auto-promotion to b, the
// original exit a RECOVERING (fully, and partially) does NOT move egress back to a — there is no
// auto-failback and no flap on a's intermittent recovery. Return to a is operator-driven only.
func TestExitSelectorNoAutoFailback(t *testing.T) {
	h := newAutoPromoteHarness(t)
	h.health.set("b", true)

	// Promote a -> b via exhaustion.
	h.driveADown()
	h.stepA()
	h.stepA()
	if got := h.sel.ActiveExit(); got != "b" {
		t.Fatalf("precondition: expected promotion to b, ActiveExit() = %q", got)
	}

	// (d) a PARTIALLY recovers (one path up): no flap back.
	h.hpA[0].state = telemetry.StateUp
	for i := 0; i < 3; i++ {
		h.stepA()
	}
	if got := h.sel.ActiveExit(); got != "b" {
		t.Fatalf("a's partial recovery moved egress back: ActiveExit() = %q, want b (no flap)", got)
	}

	// (c) a FULLY recovers: still no auto-failback.
	h.hpA[1].state = telemetry.StateUp
	for i := 0; i < 3; i++ {
		h.stepA()
	}
	if got := h.sel.ActiveExit(); got != "b" {
		t.Fatalf("a's full recovery auto-failed-back: ActiveExit() = %q, want b (no auto-failback)", got)
	}

	// Non-vacuity: mark the recovered exit "a" HEALTHY, then drive it to fully fail AGAIN (a stale
	// re-exhaustion of the demoted exit). This gives a naive/mutated selector a live target to fail
	// back to, so the assertion actually exercises no-failback enforcement (the promotion switch's
	// SetOnExhausted(nil) clear + onActiveExhausted's active-guard) rather than "nothing to promote
	// to". Egress must STILL stay on b. Deleting BOTH guards fails here (egress moves back to a).
	h.health.set("a", true)
	h.driveADown() // a fully fails again
	h.stepA()      // advance 0 -> 1
	h.stepA()      // wrap 1 -> 0 -> stale re-exhaustion on a's controller
	if got := h.sel.ActiveExit(); got != "b" {
		t.Fatalf("a's stale re-exhaustion failed back: ActiveExit() = %q, want b (no auto-failback even on a healthy re-failing demoted exit)", got)
	}
	h.assertOwnsDefaultRoute(t, h.bHex, h.aHex, "10.0.1.1/32", "10.0.0.1/32")
}

// TestExitSelectorNoHealthyStandbyStaysPut pins that when NO warm standby is healthy, exhaustion of
// the active exit leaves egress on the (dead) active exit rather than thrashing onto an unhealthy
// peer, and logs the condition.
func TestExitSelectorNoHealthyStandbyStaysPut(t *testing.T) {
	h := newAutoPromoteHarness(t)
	// b is NOT healthy — nothing to promote to.

	h.driveADown()
	h.stepA()
	h.stepA()

	if got := h.sel.ActiveExit(); got != "a" {
		t.Fatalf("ActiveExit() = %q, want a (no healthy standby → stay put, do not thrash)", got)
	}
	owned := perPeerAllowedIPs(mustIpcGet(t, h.dev))
	if !owned[h.aHex]["0.0.0.0/1"] {
		t.Fatalf("egress moved off a with no healthy standby; a owns %v", keys(owned[h.aHex]))
	}
	if !strings.Contains(h.buf.String(), "no healthy warm standby") {
		t.Fatalf("no-standby condition not logged; log:\n%s", h.buf.String())
	}
}

// TestDeviceExitHealth validates the PRODUCTION health seam: a peer is healthy only when its WG
// session is established (last-handshake within RejectAfterTime) AND at least one of its paths is
// up. It drives the two inputs independently over a fake engine dump + fake per-path liveness.
func TestDeviceExitHealth(t *testing.T) {
	const pub = "aa" // stand-in lowercase-hex key; only equality with the dump matters here.
	clk := &fakeClock{now: time.Unix(10_000, 0)}
	up := &fakeHealth{telemetry.StateUp}
	down := &fakeHealth{telemetry.StateDown}

	freshDump := "public_key=" + pub + "\nlast_handshake_time_sec=9990\nlast_handshake_time_nsec=0\n"
	staleSec := clk.now.Add(-awgdevice.RejectAfterTime - time.Second).Unix()
	staleDump := "public_key=" + pub + "\nlast_handshake_time_sec=" + strconv.FormatInt(staleSec, 10) + "\nlast_handshake_time_nsec=0\n"
	neverDump := "public_key=" + pub + "\nlast_handshake_time_sec=0\nlast_handshake_time_nsec=0\n"

	newHealth := func(dump string, hp ...hubHealth) *deviceExitHealth {
		return &deviceExitHealth{
			engine: fakeIpcGetter(dump),
			clock:  clk,
			expiry: awgdevice.RejectAfterTime,
			peers:  map[string]exitPeerHealth{"x": {publicKeyHex: pub, health: hp}},
		}
	}

	if !newHealth(freshDump, up).healthy("x") {
		t.Fatalf("established session + path up must be healthy")
	}
	if newHealth(freshDump, down).healthy("x") {
		t.Fatalf("all paths down must be unhealthy even with an established session")
	}
	if newHealth(staleDump, up).healthy("x") {
		t.Fatalf("a session older than RejectAfterTime must be unhealthy (wedged)")
	}
	if newHealth(neverDump, up).healthy("x") {
		t.Fatalf("a never-handshaked peer must be unhealthy")
	}
	if newHealth(freshDump, up).healthy("unknown") {
		t.Fatalf("an unconfigured name must read unhealthy")
	}
}

// fakeIpcGetter returns a fixed UAPI dump.
type fakeIpcGetter string

func (f fakeIpcGetter) IpcGet() (string, error) { return string(f), nil }

// threeExitEdgeConfig builds a multi-exit edge with THREE exit-capable (mode=default-route) peers
// a, b, c in config order — each carrying the default route plus its own distinct inner /32 —
// returning the config and the three peers' lowercase-hex public keys. It exists to pin the "first
// HEALTHY exit-capable peer in config order" standby walk when MORE THAN ONE candidate is healthy.
func threeExitEdgeConfig(t *testing.T) (cfg *config.Config, aHex, bHex, cHex string) {
	t.Helper()
	privRaw, _ := genX25519(t)
	_, aPubRaw := genX25519(t)
	_, bPubRaw := genX25519(t)
	_, cPubRaw := genX25519(t)
	mkPeer := func(pub []byte, inner, name string) config.Peer {
		return config.Peer{
			PublicKey:  keyFromRaw(t, pub),
			AllowedIPs: []string{"0.0.0.0/0", inner},
			Mode:       config.PeerModeDefaultRoute,
			Name:       name,
			PSK:        keyFromRaw(t, mustRandom(t, 32)),
		}
	}
	cfg = &config.Config{
		Role: config.RoleEdge,
		WireGuard: config.WireGuard{
			PrivateKey: keyFromRaw(t, privRaw),
			Peers: []config.Peer{
				mkPeer(aPubRaw, "10.0.0.1/32", "a"),
				mkPeer(bPubRaw, "10.0.1.1/32", "b"),
				mkPeer(cPubRaw, "10.0.2.1/32", "c"),
			},
		},
	}
	return cfg, hex.EncodeToString(aPubRaw), hex.EncodeToString(bPubRaw), hex.EncodeToString(cPubRaw)
}

// TestExitSelectorPromotesFirstHealthyInConfigOrder pins Q75's "first HEALTHY exit-capable peer in
// CONFIG order" standby walk when MORE THAN ONE standby is healthy. With exit "a" active and BOTH
// "b" and "c" healthy warm standbys, exhausting a must promote to b (first in config order), never
// c. The single-healthy-candidate tests only ever offer one target, so they cannot catch a mutation
// that reverses the order walk or keeps the LAST healthy candidate — this test does (it would read
// ActiveExit()="c").
func TestExitSelectorPromotesFirstHealthyInConfigOrder(t *testing.T) {
	lg := discardLogger(t)
	cfg, aHex, bHex, cHex := threeExitEdgeConfig(t)

	dev := newAllowedIPsTestEngine(t, lg)
	boot, err := uapiConfig(cfg, []bootEndpoint{{}, {}, {}})
	if err != nil {
		t.Fatalf("uapiConfig: %v", err)
	}
	if err := dev.IpcSet(boot); err != nil {
		t.Fatalf("IpcSet boot config: %v", err)
	}

	sel := newExitSelector(cfg, dev, lg)
	if sel == nil {
		t.Fatalf("newExitSelector returned nil for a three-exit edge")
	}
	if got := sel.ActiveExit(); got != "a" {
		t.Fatalf("boot ActiveExit() = %q, want a (first exit peer in config order)", got)
	}

	// Controller for "a": two literal endpoints so exhaustion requires a FULL wrap.
	epsA := mustEndpoints(t, "203.0.113.1:51820", "203.0.113.2:51820")
	hpA := []hubHealth{&fakeHealth{telemetry.StateUp}, &fakeHealth{telemetry.StateUp}}
	clk := &fakeClock{now: time.Unix(1000, 0)}
	ctrlA := newHubFailover(epsA, hpA, &recordingRemote{}, func() {}, clk, testSettle, lg)

	// BOTH b and c are healthy warm standbys — the discriminating condition.
	health := newFakeExitHealth()
	health.set("b", true)
	health.set("c", true)
	sel.enableAutoPromotion(map[string]exitController{"a": ctrlA}, exitHealthFunc(health.isHealthy))

	// Exhaust a (2-endpoint list: advance 0->1, then wrap 1->0 = exhaustion).
	hpA[0].(*fakeHealth).state = telemetry.StateDown
	hpA[1].(*fakeHealth).state = telemetry.StateDown
	clk.advance(testSettle + time.Second)
	ctrlA.check()
	clk.advance(testSettle + time.Second)
	ctrlA.check()

	if got := sel.ActiveExit(); got != "b" {
		t.Fatalf("promoted to %q with both b and c healthy; want b (FIRST healthy in config order, not last/reversed)", got)
	}
	owned := perPeerAllowedIPs(mustIpcGet(t, dev))
	for _, split := range []string{"0.0.0.0/1", "128.0.0.0/1"} {
		if !owned[bHex][split] {
			t.Fatalf("%s not under b after promotion; b owns %v", split, keys(owned[bHex]))
		}
		if owned[cHex][split] {
			t.Fatalf("%s under c — promotion skipped the first healthy standby; c owns %v", split, keys(owned[cHex]))
		}
		if owned[aHex][split] {
			t.Fatalf("%s still under a after promotion; a owns %v", split, keys(owned[aHex]))
		}
	}
}

// TestStartFailoverAndResolutionSingleLiteralExitWiring is the R267 WIRING-level acceptance for
// T269 (round 2): the minimal one-endpoint-per-concentrator topology — a MULTI-EXIT edge whose two
// exit-capable peers each carry EXACTLY ONE literal endpoint. peerNeedsHubFailover is false for such
// a peer (no hostname, <2 literals), so BEFORE the fix startFailoverAndResolution built ZERO
// controllers and auto-promotion had nothing to subscribe — INERT in exactly the topology R267
// requires (T253's total==1 exhaustion signal). The fix builds an exhaustion-only controller (with a
// RUNNING poll, despite canFailoverLocked being false) for each exit-capable single-literal peer,
// preserving the no-action guarantee (no advance/repoint/rehandshake — signal only). This test drives
// exit "a"'s SOLE path down past the dwell and asserts BOTH controllers were built AND that a's
// total==1 exhaustion promotes egress to the healthy warm standby "b" through the real engine's
// steal-on-insert. It fails (0 controllers built) under a revert of the peerNeedsHubFailover/wiring
// change.
func TestStartFailoverAndResolutionSingleLiteralExitWiring(t *testing.T) {
	lg := discardLogger(t)
	cfg, aHex, bHex := twoExitEdgeConfig(t)
	// Give each exit peer EXACTLY ONE literal endpoint (the R267 minimal topology).
	epA := mustEndpoints(t, "203.0.113.1:51820")
	epB := mustEndpoints(t, "198.51.100.1:51820")
	cfg.WireGuard.Peers[0].Endpoints = epA
	cfg.WireGuard.Peers[0].EndpointSpecs = []config.EndpointSpec{{Addr: epA[0]}}
	cfg.WireGuard.Peers[1].Endpoints = epB
	cfg.WireGuard.Peers[1].EndpointSpecs = []config.EndpointSpec{{Addr: epB[0]}}

	// Real engine booted from uapiConfig so the promotion's steal-on-insert is real.
	dev := newAllowedIPsTestEngine(t, lg)
	boot, err := uapiConfig(cfg, []bootEndpoint{{}, {}})
	if err != nil {
		t.Fatalf("uapiConfig: %v", err)
	}
	if err := dev.IpcSet(boot); err != nil {
		t.Fatalf("IpcSet boot config: %v", err)
	}

	// Controllers built through the PRODUCTION wiring (startFailoverAndResolution) with fake per-peer
	// liveness + a shared fake clock so the single-endpoint exhaustion step is deterministic.
	hpA := &fakeHealth{telemetry.StateUp}
	hpB := &fakeHealth{telemetry.StateUp}
	clk := &fakeClock{now: time.Unix(1000, 0)}
	deps := &fakeFailoverDeps{
		healthByPeer: [][]hubHealth{{hpA}, {hpB}},
		remotes:      []*recordingRemote{{}, {}},
		clk:          clk,
	}
	bootEps := bootEndpoints{specs: [][]failoverSpec{
		{litSpec(t, "203.0.113.1:51820")},
		{litSpec(t, "198.51.100.1:51820")},
	}}
	ids := cfg.PeerIdentities()
	stopFailover, stopResolution, ctrls := startFailoverAndResolution(cfg, deps, ids, bootEps, lg)
	// Halt the per-peer poll goroutines so the test drives check() deterministically.
	stopFailover()
	stopResolution()

	// THE R267 WIRING GAP FIX: both single-literal exit peers get a controller. Before the fix this
	// map was EMPTY (peerNeedsHubFailover false → continue), so auto-promotion was inert.
	if len(ctrls) != 2 {
		t.Fatalf("startFailoverAndResolution built %d controllers for single-literal exit peers, want 2 (R267: exhaustion-only controller per exit-capable single-literal peer on a multi-exit edge)", len(ctrls))
	}
	ctrlA, okA := ctrls["a"]
	ctrlB, okB := ctrls["b"]
	if !okA || !okB {
		t.Fatalf("controllers keyed by name: a present=%v b present=%v, want both", okA, okB)
	}

	// Auto-promotion over the real engine; b is a healthy warm standby.
	sel := newExitSelector(cfg, dev, lg)
	if sel == nil {
		t.Fatalf("newExitSelector returned nil for a two-exit edge")
	}
	health := newFakeExitHealth()
	health.set("b", true)
	sel.enableAutoPromotion(
		map[string]exitController{"a": ctrlA, "b": ctrlB},
		exitHealthFunc(health.isHealthy),
	)

	// Drive a's SOLE path down past the dwell: total==1 exhaustion (onset step + one past-dwell step)
	// raises the signal with NO advance/repoint (single endpoint), promoting egress to b.
	hpA.state = telemetry.StateDown
	ctrlA.check() // onset: records downSince, no signal yet
	clk.advance(hubFailoverSettle + time.Second)
	ctrlA.check() // past the dwell → total==1 exhaustion → auto-promotion fires

	if got := sel.ActiveExit(); got != "b" {
		t.Fatalf("after single-endpoint exhaustion ActiveExit() = %q, want b (auto-promotion off the one-endpoint exit)", got)
	}
	owned := perPeerAllowedIPs(mustIpcGet(t, dev))
	for _, split := range []string{"0.0.0.0/1", "128.0.0.0/1"} {
		if !owned[bHex][split] {
			t.Fatalf("%s not under b after promotion; b owns %v", split, keys(owned[bHex]))
		}
		if owned[aHex][split] {
			t.Fatalf("%s still under a after promotion; a owns %v", split, keys(owned[aHex]))
		}
	}
}
