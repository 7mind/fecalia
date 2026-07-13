package device

import (
	"io"
	"net/netip"
	"testing"
	"time"

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
