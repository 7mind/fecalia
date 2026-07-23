package device

import (
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	awgdevice "github.com/amnezia-vpn/amneziawg-go/device"

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

	// a now fully exhausts. The stale exhaustion signal must NOT move egress off the operator's b.
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
