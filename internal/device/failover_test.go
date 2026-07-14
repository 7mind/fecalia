package device

import (
	"io"
	"net/netip"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/log"
	"github.com/7mind/wanbond/internal/telemetry"
)

// fakeHealth is a hand-driven per-path liveness verdict so a test can force the exact
// hub-loss (all DOWN) / partial (some UP) states the controller must distinguish.
type fakeHealth struct{ state telemetry.PathState }

func (f *fakeHealth) State() telemetry.PathState { return f.state }

// recordingRemote records the last endpoint SetPeerRemote was pointed at and how many
// times, standing in for the live Bind so the switch is observable without an engine.
type recordingRemote struct {
	last  netip.AddrPort
	calls int
}

func (r *recordingRemote) SetPeerRemote(ap netip.AddrPort) {
	r.last = ap
	r.calls++
}

func discardLogger(t *testing.T) log.Logger {
	t.Helper()
	lg, err := log.New("error", io.Discard)
	if err != nil {
		t.Fatalf("build logger: %v", err)
	}
	return lg
}

func mustEndpoints(t *testing.T, ss ...string) []netip.AddrPort {
	t.Helper()
	eps := make([]netip.AddrPort, len(ss))
	for i, s := range ss {
		ap, err := netip.ParseAddrPort(s)
		if err != nil {
			t.Fatalf("parse endpoint %q: %v", s, err)
		}
		eps[i] = ap
	}
	return eps
}

const testSettle = 3 * time.Second

// TestHubFailoverSwitchesOnHubLoss is the core acceptance: with a 2-endpoint list,
// forcing ALL paths to endpoint#1 DOWN switches the peer remote to endpoint#2 AND
// initiates exactly one re-handshake — while ANY path being UP takes no action at all
// (hub loss = EVERY path down, distinct from a single path failing).
func TestHubFailoverSwitchesOnHubLoss(t *testing.T) {
	eps := mustEndpoints(t, "203.0.113.1:51820", "198.51.100.7:51820")
	hp := []hubHealth{&fakeHealth{telemetry.StateUp}, &fakeHealth{telemetry.StateUp}}
	rem := &recordingRemote{}
	handshakes := 0
	clk := &fakeClock{now: time.Unix(1000, 0)}
	h := newHubFailover(eps, hp, rem, func() { handshakes++ }, clk, testSettle, discardLogger(t))

	// Both paths UP → no failover, even long after the settle dwell.
	clk.advance(10 * time.Second)
	h.check()
	if rem.calls != 0 || handshakes != 0 {
		t.Fatalf("failover fired while a path was UP: remote switches=%d handshakes=%d", rem.calls, handshakes)
	}
	if h.idx != 0 {
		t.Fatalf("active index moved off 0 while a path was up: %d", h.idx)
	}

	// Hub loss: every path DOWN. The settle dwell since construction has elapsed (+10s).
	hp[0].(*fakeHealth).state = telemetry.StateDown
	hp[1].(*fakeHealth).state = telemetry.StateDown
	h.check()

	if rem.calls != 1 {
		t.Fatalf("expected exactly one remote switch on hub loss, got %d", rem.calls)
	}
	if rem.last != eps[1] {
		t.Fatalf("switched remote = %v, want standby endpoint#2 %v", rem.last, eps[1])
	}
	if handshakes != 1 {
		t.Fatalf("expected exactly one re-handshake on hub loss, got %d", handshakes)
	}
	if h.idx != 1 {
		t.Fatalf("active index = %d, want 1 (advanced to the standby)", h.idx)
	}
}

// TestHubFailoverPartialDownNoFailover pins the load-bearing acceptance property that
// hub loss is EVERY path DOWN, distinct from a single-path failure (which the per-path
// schedulers already handle). With a 2-endpoint list and the settle dwell fully elapsed
// — so a broken "any path down" detector WOULD advance — one path UP and one path DOWN
// must take NO hub-failover action: no remote switch, no re-handshake, no index move.
// This is the case none of the both-Up / both-Down tests exercise; without it, a
// regression of allDownLocked from all-down to any-down would pass the whole suite.
func TestHubFailoverPartialDownNoFailover(t *testing.T) {
	eps := mustEndpoints(t, "203.0.113.1:51820", "198.51.100.7:51820")
	hp := []hubHealth{&fakeHealth{telemetry.StateUp}, &fakeHealth{telemetry.StateDown}}
	rem := &recordingRemote{}
	handshakes := 0
	clk := &fakeClock{now: time.Unix(1000, 0)}
	h := newHubFailover(eps, hp, rem, func() { handshakes++ }, clk, testSettle, discardLogger(t))

	// Settle dwell elapsed: an any-down detector would fire here; an all-down one must not.
	clk.advance(testSettle + time.Second)
	h.check()

	if rem.calls != 0 || handshakes != 0 {
		t.Fatalf("hub failover fired on a partial-down state (one path UP): remote switches=%d handshakes=%d — hub loss must require EVERY path down", rem.calls, handshakes)
	}
	if h.idx != 0 {
		t.Fatalf("active index moved off 0 while a path was UP: %d (single-path loss must not advance the hub)", h.idx)
	}
}

// TestHubFailoverSingleEndpointNoAction is the GUARD: a SINGLE-endpoint list must take
// NO failover action even under sustained hub loss — no remote switch, no re-handshake,
// no index movement — so a one-concentrator deployment behaves EXACTLY as pre-T57. It is
// non-vacuous: absent the len<2 guard, the wrap arithmetic (0+1)%1==0 would "switch" to
// the same endpoint and fire a re-handshake, which this asserts never happens.
func TestHubFailoverSingleEndpointNoAction(t *testing.T) {
	eps := mustEndpoints(t, "203.0.113.1:51820")
	hp := []hubHealth{&fakeHealth{telemetry.StateDown}, &fakeHealth{telemetry.StateDown}}
	rem := &recordingRemote{}
	handshakes := 0
	clk := &fakeClock{now: time.Unix(1000, 0)}
	h := newHubFailover(eps, hp, rem, func() { handshakes++ }, clk, testSettle, discardLogger(t))

	for i := 0; i < 5; i++ {
		clk.advance(time.Minute)
		h.check()
	}
	if rem.calls != 0 {
		t.Fatalf("single-endpoint list switched the remote %d times, want 0", rem.calls)
	}
	if handshakes != 0 {
		t.Fatalf("single-endpoint list re-handshaked %d times, want 0", handshakes)
	}
	if h.idx != 0 {
		t.Fatalf("single-endpoint list moved active index to %d, want 0", h.idx)
	}
}

// TestHubFailoverReArmsAndWraps proves detection re-arms against the NEW active endpoint
// and, at the end of the list, WRAPS to index 0 (the documented end-of-list policy). With
// a 2-endpoint list held fully DOWN, successive settle-separated checks advance 0→1→0.
func TestHubFailoverReArmsAndWraps(t *testing.T) {
	eps := mustEndpoints(t, "203.0.113.1:51820", "198.51.100.7:51820")
	hp := []hubHealth{&fakeHealth{telemetry.StateDown}, &fakeHealth{telemetry.StateDown}}
	rem := &recordingRemote{}
	handshakes := 0
	clk := &fakeClock{now: time.Unix(1000, 0)}
	h := newHubFailover(eps, hp, rem, func() { handshakes++ }, clk, testSettle, discardLogger(t))

	// First advance: 0 → 1 (settle since construction has elapsed).
	clk.advance(testSettle + time.Second)
	h.check()
	if h.idx != 1 || rem.last != eps[1] || rem.calls != 1 {
		t.Fatalf("first advance: idx=%d last=%v calls=%d, want idx=1 last=%v calls=1", h.idx, rem.last, rem.calls, eps[1])
	}

	// Re-arm on the new endpoint, still fully down: after another settle, wrap 1 → 0.
	clk.advance(testSettle + time.Second)
	h.check()
	if h.idx != 0 || rem.last != eps[0] || rem.calls != 2 {
		t.Fatalf("wrap advance: idx=%d last=%v calls=%d, want idx=0 last=%v calls=2 (wrap to index 0)", h.idx, rem.last, rem.calls, eps[0])
	}
	if handshakes != 2 {
		t.Fatalf("re-handshakes=%d, want 2 (one per advance)", handshakes)
	}
}

// TestHubFailoverSettleDwellDefersReAdvance proves the settle dwell prevents a premature
// second advance: right after a switch, a STILL-DOWN reading (which is expected while the
// new endpoint's echoes have not yet returned) must NOT skip to the next endpoint until
// the dwell elapses. Without this, a healthy standby would be skipped on the very next
// tick before its liveness could recover.
func TestHubFailoverSettleDwellDefersReAdvance(t *testing.T) {
	realEps := mustEndpoints(t, "203.0.113.1:51820", "198.51.100.7:51820", "192.0.2.9:51820")
	hp := []hubHealth{&fakeHealth{telemetry.StateDown}, &fakeHealth{telemetry.StateDown}}
	rem := &recordingRemote{}
	handshakes := 0
	clk := &fakeClock{now: time.Unix(1000, 0)}
	h := newHubFailover(realEps, hp, rem, func() { handshakes++ }, clk, testSettle, discardLogger(t))

	// First advance 0 → 1.
	clk.advance(testSettle + time.Second)
	h.check()
	if rem.calls != 1 || h.idx != 1 {
		t.Fatalf("first advance: calls=%d idx=%d, want 1 and 1", rem.calls, h.idx)
	}

	// Within the dwell (< settle since the switch): no second advance despite still-down.
	clk.advance(testSettle - time.Millisecond)
	h.check()
	if rem.calls != 1 || h.idx != 1 {
		t.Fatalf("advanced within settle dwell: calls=%d idx=%d, want no change (1,1)", rem.calls, h.idx)
	}

	// Dwell elapsed: now advance 1 → 2.
	clk.advance(2 * time.Millisecond)
	h.check()
	if rem.calls != 2 || h.idx != 2 || rem.last != realEps[2] {
		t.Fatalf("advance after dwell: calls=%d idx=%d last=%v, want 2, 2, %v", rem.calls, h.idx, rem.last, realEps[2])
	}
}

// mustAP parses a single "ip:port" endpoint or fails the test.
func mustAP(t *testing.T, s string) netip.AddrPort {
	t.Helper()
	ap, err := netip.ParseAddrPort(s)
	if err != nil {
		t.Fatalf("parse endpoint %q: %v", s, err)
	}
	return ap
}

// litSpec builds a literal failoverSpec (fixed single-entry expansion), as resolveEndpoints
// produces for an IP-literal endpoint entry.
func litSpec(t *testing.T, s string) failoverSpec {
	t.Helper()
	ap := mustAP(t, s)
	return failoverSpec{spec: config.EndpointSpec{Addr: ap}, addrs: []netip.AddrPort{ap}}
}

// nameSpec builds a hostname failoverSpec carrying an initial expansion (which may be
// empty, modelling a not-yet-resolved hostname at boot).
func nameSpec(host string, port uint16, addrs ...netip.AddrPort) failoverSpec {
	return failoverSpec{spec: config.EndpointSpec{Host: host, Port: port, IsName: true}, addrs: addrs}
}

// TestHubFailoverStandbyRecordSwapNoRepoint is acceptance (1): swapping a STANDBY spec's
// expansion (a hostname re-resolving off the active spec) must NOT touch the bond — zero
// SetPeerRemote, zero re-handshake — and must leave the active pointer on its own entry.
func TestHubFailoverStandbyRecordSwapNoRepoint(t *testing.T) {
	active := mustAP(t, "203.0.113.1:51820")
	specs := []failoverSpec{
		litSpec(t, "203.0.113.1:51820"),                                         // spec 0: active literal
		nameSpec("standby.example.com", 51820, mustAP(t, "198.51.100.7:51820")), // spec 1: standby hostname
	}
	hp := []hubHealth{&fakeHealth{telemetry.StateUp}, &fakeHealth{telemetry.StateUp}}
	rem := &recordingRemote{}
	handshakes := 0
	clk := &fakeClock{now: time.Unix(1000, 0)}
	h := newHubFailoverFromSpecs(specs, hp, rem, func() { handshakes++ }, clk, testSettle, discardLogger(t))
	if h.activeSpec != 0 || h.activeAddr != active {
		t.Fatalf("boot active = (%d,%v), want (0,%v)", h.activeSpec, h.activeAddr, active)
	}

	// The standby hostname re-resolves onto a DIFFERENT record.
	h.updateResolution(1, []netip.AddrPort{mustAP(t, "192.0.2.9:51820")})

	if rem.calls != 0 || handshakes != 0 {
		t.Fatalf("standby-record swap touched the bond: SetPeerRemote=%d handshakes=%d, want 0,0", rem.calls, handshakes)
	}
	if h.activeSpec != 0 || h.activeAddr != active {
		t.Fatalf("standby-record swap moved the active pointer to (%d,%v), want (0,%v)", h.activeSpec, h.activeAddr, active)
	}
	if h.idx != 0 {
		t.Fatalf("active flattened idx = %d, want 0 (unchanged; the standby follows the active spec)", h.idx)
	}
}

// TestHubFailoverActiveIPChangeRepointsOnce is acceptance (2): when the ACTIVE spec (a
// hostname) re-resolves off its current AddrPort, the active endpoint's IP has changed, so
// the bond is repointed via EXACTLY one SetPeerRemote + one re-handshake to the new record.
func TestHubFailoverActiveIPChangeRepointsOnce(t *testing.T) {
	oldA := mustAP(t, "203.0.113.1:51820")
	newA := mustAP(t, "203.0.113.2:51820")
	specs := []failoverSpec{
		nameSpec("hub.example.com", 51820, oldA), // spec 0: active hostname
		litSpec(t, "198.51.100.7:51820"),         // spec 1: literal standby
	}
	hp := []hubHealth{&fakeHealth{telemetry.StateUp}}
	rem := &recordingRemote{}
	handshakes := 0
	clk := &fakeClock{now: time.Unix(1000, 0)}
	h := newHubFailoverFromSpecs(specs, hp, rem, func() { handshakes++ }, clk, testSettle, discardLogger(t))
	if h.activeSpec != 0 || h.activeAddr != oldA {
		t.Fatalf("boot active = (%d,%v), want (0,%v)", h.activeSpec, h.activeAddr, oldA)
	}

	h.updateResolution(0, []netip.AddrPort{newA})

	if rem.calls != 1 {
		t.Fatalf("active-IP change SetPeerRemote=%d, want exactly 1", rem.calls)
	}
	if rem.last != newA {
		t.Fatalf("repointed remote = %v, want new active record %v", rem.last, newA)
	}
	if handshakes != 1 {
		t.Fatalf("active-IP change re-handshakes=%d, want exactly 1", handshakes)
	}
	if h.activeSpec != 0 || h.activeAddr != newA {
		t.Fatalf("active pointer = (%d,%v), want (0,%v)", h.activeSpec, h.activeAddr, newA)
	}
}

// TestHubFailoverUnchangedActiveIPNoRepoint is acceptance (3): re-resolving the ACTIVE spec
// onto a record set that STILL contains the current active AddrPort is a no-op for the bond
// (Q31/D32 suppression) — zero SetPeerRemote, zero re-handshake.
func TestHubFailoverUnchangedActiveIPNoRepoint(t *testing.T) {
	active := mustAP(t, "203.0.113.1:51820")
	extra := mustAP(t, "203.0.113.9:51820")
	specs := []failoverSpec{
		nameSpec("hub.example.com", 51820, active), // spec 0: active hostname
		litSpec(t, "198.51.100.7:51820"),           // spec 1: literal standby
	}
	hp := []hubHealth{&fakeHealth{telemetry.StateUp}}
	rem := &recordingRemote{}
	handshakes := 0
	clk := &fakeClock{now: time.Unix(1000, 0)}
	h := newHubFailoverFromSpecs(specs, hp, rem, func() { handshakes++ }, clk, testSettle, discardLogger(t))

	// Re-resolve: the record set changed (grew) but STILL contains the active AddrPort.
	h.updateResolution(0, []netip.AddrPort{active, extra})

	if rem.calls != 0 || handshakes != 0 {
		t.Fatalf("unchanged active IP touched the bond: SetPeerRemote=%d handshakes=%d, want 0,0", rem.calls, handshakes)
	}
	if h.activeSpec != 0 || h.activeAddr != active {
		t.Fatalf("active pointer = (%d,%v), want (0,%v) (survived the swap)", h.activeSpec, h.activeAddr, active)
	}
	if h.idx != 0 {
		t.Fatalf("active flattened idx = %d, want 0", h.idx)
	}
}

// TestHubFailoverIdxReMapsOnExpansionGrowShrink is acceptance (4): when a STANDBY spec
// ordered BEFORE the active entry grows or shrinks, the active entry's FLATTENED index
// re-maps to its new offset — the active identity is spec-scoped, not a frozen flat index —
// while the bond is never touched (standby-only change).
func TestHubFailoverIdxReMapsOnExpansionGrowShrink(t *testing.T) {
	activeLit := mustAP(t, "198.51.100.7:51820")
	specs := []failoverSpec{
		nameSpec("early.example.com", 51820), // spec 0: hostname, EMPTY at boot
		litSpec(t, "198.51.100.7:51820"),     // spec 1: literal → boot-active (flat[0])
	}
	hp := []hubHealth{&fakeHealth{telemetry.StateUp}}
	rem := &recordingRemote{}
	handshakes := 0
	clk := &fakeClock{now: time.Unix(1000, 0)}
	h := newHubFailoverFromSpecs(specs, hp, rem, func() { handshakes++ }, clk, testSettle, discardLogger(t))
	if h.activeSpec != 1 || h.activeAddr != activeLit || h.idx != 0 {
		t.Fatalf("boot active = (%d,%v) idx=%d, want (1,%v) idx=0", h.activeSpec, h.activeAddr, h.idx, activeLit)
	}

	// Grow the earlier standby spec 0 from [] to two records: active flat idx 0 → 2.
	h.updateResolution(0, []netip.AddrPort{mustAP(t, "203.0.113.1:51820"), mustAP(t, "203.0.113.2:51820")})
	if rem.calls != 0 || handshakes != 0 {
		t.Fatalf("growing a standby spec touched the bond: SetPeerRemote=%d handshakes=%d, want 0,0", rem.calls, handshakes)
	}
	if h.activeSpec != 1 || h.activeAddr != activeLit || h.idx != 2 {
		t.Fatalf("after grow: active=(%d,%v) idx=%d, want (1,%v) idx=2", h.activeSpec, h.activeAddr, h.idx, activeLit)
	}

	// Shrink the earlier standby spec 0 to one record: active flat idx 2 → 1.
	h.updateResolution(0, []netip.AddrPort{mustAP(t, "203.0.113.1:51820")})
	if rem.calls != 0 || handshakes != 0 {
		t.Fatalf("shrinking a standby spec touched the bond: SetPeerRemote=%d handshakes=%d, want 0,0", rem.calls, handshakes)
	}
	if h.activeSpec != 1 || h.activeAddr != activeLit || h.idx != 1 {
		t.Fatalf("after shrink: active=(%d,%v) idx=%d, want (1,%v) idx=1", h.activeSpec, h.activeAddr, h.idx, activeLit)
	}
}

// TestPeerNeedsHubFailover is acceptance (5): a single-IP-literal peer warrants NO
// controller (byte-for-byte pre-G5), while a peer with any hostname spec OR >= 2 flattened
// literals does. This is the exact construction gate startHubFailover applies per peer.
func TestPeerNeedsHubFailover(t *testing.T) {
	lit := func(s string) config.EndpointSpec { return config.EndpointSpec{Addr: mustAP(t, s)} }
	name := func(h string, p uint16) config.EndpointSpec {
		return config.EndpointSpec{Host: h, Port: p, IsName: true}
	}

	cases := []struct {
		name string
		peer config.Peer
		want bool
	}{
		{
			name: "single IP literal → no controller",
			peer: config.Peer{
				Endpoints:     mustEndpoints(t, "203.0.113.1:51820"),
				EndpointSpecs: []config.EndpointSpec{lit("203.0.113.1:51820")},
			},
			want: false,
		},
		{
			name: "two IP literals → controller",
			peer: config.Peer{
				Endpoints:     mustEndpoints(t, "203.0.113.1:51820", "198.51.100.7:51820"),
				EndpointSpecs: []config.EndpointSpec{lit("203.0.113.1:51820"), lit("198.51.100.7:51820")},
			},
			want: true,
		},
		{
			name: "single hostname → controller (may re-resolve onto a new IP)",
			peer: config.Peer{
				Endpoints:     nil,
				EndpointSpecs: []config.EndpointSpec{name("hub.example.com", 51820)},
			},
			want: true,
		},
		{
			name: "hostname + literal → controller",
			peer: config.Peer{
				Endpoints:     mustEndpoints(t, "203.0.113.1:51820"),
				EndpointSpecs: []config.EndpointSpec{name("hub.example.com", 51820), lit("203.0.113.1:51820")},
			},
			want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := peerNeedsHubFailover(tc.peer); got != tc.want {
				t.Fatalf("peerNeedsHubFailover = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestHubFailoverBootAdoptsFirstResolution is acceptance (7): a hostname-only peer boots
// with every spec's expansion EMPTY (activeSpec == -1) — check can never rescue it (a
// one-record expansion keeps the flattened length at 1, under the total<2 guard), so the
// first resolution MUST adopt its head as the active endpoint and point the bond at it via
// exactly one SetPeerRemote + one re-handshake, arming the settle dwell.
func TestHubFailoverBootAdoptsFirstResolution(t *testing.T) {
	first := mustAP(t, "203.0.113.1:51820")
	specs := []failoverSpec{
		nameSpec("hub.example.com", 51820), // sole spec: hostname, EMPTY at boot
	}
	hp := []hubHealth{&fakeHealth{telemetry.StateDown}}
	rem := &recordingRemote{}
	handshakes := 0
	clk := &fakeClock{now: time.Unix(1000, 0)}
	h := newHubFailoverFromSpecs(specs, hp, rem, func() { handshakes++ }, clk, testSettle, discardLogger(t))
	if h.activeSpec != -1 || h.idx != -1 {
		t.Fatalf("boot active = (spec %d, idx %d), want (spec -1, idx -1) (all specs empty)", h.activeSpec, h.idx)
	}

	// First resolution populates the sole hostname spec: adopt its head, point the bond.
	h.updateResolution(0, []netip.AddrPort{first})

	if rem.calls != 1 || rem.last != first {
		t.Fatalf("first resolution SetPeerRemote=%d last=%v, want 1 and %v (adopt head)", rem.calls, rem.last, first)
	}
	if handshakes != 1 {
		t.Fatalf("first resolution re-handshakes=%d, want 1", handshakes)
	}
	if h.activeSpec != 0 || h.activeAddr != first || h.idx != 0 {
		t.Fatalf("post-adoption active = (%d,%v) idx=%d, want (0,%v) idx=0", h.activeSpec, h.activeAddr, h.idx, first)
	}
	// The dwell is armed at adoption time, not construction time.
	if !h.lastSwitch.Equal(clk.now) {
		t.Fatalf("lastSwitch=%v, want %v (armed at adoption)", h.lastSwitch, clk.now)
	}
}

// TestHubFailoverRepointResetsSettleDwell is acceptance (8): an active-IP-change repoint
// (T73's liveness-loss re-resolution flow) must RE-ARM the settle dwell, so the next
// all-down check within the dwell does NOT immediately advance off the just-repointed
// endpoint — a second disruptive SetPeerRemote before the new address can prove itself.
func TestHubFailoverRepointResetsSettleDwell(t *testing.T) {
	oldA := mustAP(t, "203.0.113.1:51820")
	newA := mustAP(t, "203.0.113.2:51820")
	standby := mustAP(t, "198.51.100.7:51820")
	specs := []failoverSpec{
		nameSpec("hub.example.com", 51820, oldA), // spec 0: active hostname
		litSpec(t, "198.51.100.7:51820"),         // spec 1: literal standby
	}
	hp := []hubHealth{&fakeHealth{telemetry.StateDown}, &fakeHealth{telemetry.StateDown}}
	rem := &recordingRemote{}
	handshakes := 0
	clk := &fakeClock{now: time.Unix(1000, 0)}
	h := newHubFailoverFromSpecs(specs, hp, rem, func() { handshakes++ }, clk, testSettle, discardLogger(t))

	// Let the boot dwell fully elapse so, absent a reset, the next all-down check WOULD advance.
	clk.advance(testSettle + time.Second)

	// The active spec re-resolves to a new IP under hub loss: repoint (one switch).
	h.updateResolution(0, []netip.AddrPort{newA})
	if rem.calls != 1 || rem.last != newA {
		t.Fatalf("repoint SetPeerRemote=%d last=%v, want 1 and %v", rem.calls, rem.last, newA)
	}

	// The repoint re-armed the dwell: a check WITHIN the settle must NOT advance off the
	// just-repointed endpoint, even though every path still reads DOWN.
	clk.advance(testSettle - time.Millisecond)
	h.check()
	if rem.calls != 1 {
		t.Fatalf("advanced within the dwell after a repoint: SetPeerRemote=%d, want 1 (no second switch)", rem.calls)
	}
	if h.activeSpec != 0 || h.activeAddr != newA {
		t.Fatalf("active moved off the repointed endpoint within the dwell: (%d,%v), want (0,%v)", h.activeSpec, h.activeAddr, newA)
	}

	// Once the dwell elapses relative to the repoint, the controller may advance (to standby).
	clk.advance(2 * time.Millisecond)
	h.check()
	if rem.calls != 2 || rem.last != standby {
		t.Fatalf("no advance after the dwell elapsed: SetPeerRemote=%d last=%v, want 2 and %v", rem.calls, rem.last, standby)
	}
}

// TestHubFailoverCrossSpecDuplicateNoSpuriousReMap is acceptance (6), the R70 case: a
// hostname STANDBY spec re-resolving onto the SAME AddrPort as the active spec's own literal
// must NOT move the active pointer (spec-scoped identity, not bare value) and must not touch
// the bond; a subsequent hub-loss advance then walks the FLATTENED order correctly past the
// duplicate rather than being confused by it.
func TestHubFailoverCrossSpecDuplicateNoSpuriousReMap(t *testing.T) {
	dup := mustAP(t, "203.0.113.1:51820")   // shared AddrPort across specs 0 and 1
	tail := mustAP(t, "198.51.100.7:51820") // spec 2 literal standby
	specs := []failoverSpec{
		nameSpec("shadow.example.com", 51820), // spec 0: hostname, EMPTY at boot
		litSpec(t, "203.0.113.1:51820"),       // spec 1: literal dup → boot-active (flat[0])
		litSpec(t, "198.51.100.7:51820"),      // spec 2: literal standby
	}
	hp := []hubHealth{&fakeHealth{telemetry.StateDown}, &fakeHealth{telemetry.StateDown}}
	rem := &recordingRemote{}
	handshakes := 0
	clk := &fakeClock{now: time.Unix(1000, 0)}
	h := newHubFailoverFromSpecs(specs, hp, rem, func() { handshakes++ }, clk, testSettle, discardLogger(t))
	if h.activeSpec != 1 || h.activeAddr != dup || h.idx != 0 {
		t.Fatalf("boot active = (%d,%v) idx=%d, want (1,%v) idx=0", h.activeSpec, h.activeAddr, h.idx, dup)
	}

	// The standby hostname (spec 0) re-resolves onto the SAME AddrPort as the active spec-1
	// literal. A bare-value re-map would rebind the active pointer to spec 0's duplicate at
	// flat index 0; the spec-scoped identity must keep it on spec 1 (now at flat index 1).
	h.updateResolution(0, []netip.AddrPort{dup})
	if rem.calls != 0 || handshakes != 0 {
		t.Fatalf("cross-spec duplicate re-resolution touched the bond: SetPeerRemote=%d handshakes=%d, want 0,0", rem.calls, handshakes)
	}
	if h.activeSpec != 1 || h.activeAddr != dup {
		t.Fatalf("active pointer spuriously re-mapped to (%d,%v), want (1,%v) (own spec's entry)", h.activeSpec, h.activeAddr, dup)
	}
	if h.idx != 1 {
		t.Fatalf("active flattened idx = %d, want 1 (spec-1 dup, past spec-0's duplicate at 0)", h.idx)
	}

	// A subsequent hub-loss advance must walk the flattened order to spec 2's tail literal
	// at flat index 2 — NOT stall on the duplicate at index 0/1.
	clk.advance(testSettle + time.Second)
	h.check()
	if rem.calls != 1 || rem.last != tail {
		t.Fatalf("advance after duplicate: SetPeerRemote=%d last=%v, want 1 and %v (flattened order to spec-2 tail)", rem.calls, rem.last, tail)
	}
	if h.activeSpec != 2 || h.activeAddr != tail || h.idx != 2 {
		t.Fatalf("post-advance active = (%d,%v) idx=%d, want (2,%v) idx=2", h.activeSpec, h.activeAddr, h.idx, tail)
	}
	if handshakes != 1 {
		t.Fatalf("advance re-handshakes=%d, want 1", handshakes)
	}
}
