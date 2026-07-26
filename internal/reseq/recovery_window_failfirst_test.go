package reseq_test

import (
	"net/netip"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/reseq"
)

// TestFECRecoveryWindowFailFirst reproduces T314's missing receiver contract:
// fecActive forces every sparse gap to the global 250 ms cap even when the
// receiver has a shorter authenticated service/reorder bound.
func TestFECRecoveryWindowFailFirst(t *testing.T) {
	clk := newFakeClock()
	r := reseq.New(64, 250*time.Millisecond, clk)
	r.SetFECActive(true)
	r.SetRecoveryWindow(reseq.RecoveryWindow{
		Enabled:    true,
		Revision:   1,
		PathKey:    7,
		Source:     testSrc,
		Hold:       60 * time.Millisecond,
		ValidUntil: clk.Now().Add(250 * time.Millisecond),
	})

	r.ObserveFromPath(0, []byte("zero"), testSrc, 7)
	if got := drainHold(r); len(got) != 1 || got[0] != "zero" {
		t.Fatalf("seed delivery = %v, want [zero]", got)
	}
	r.ObserveFromPath(2, []byte("two"), testSrc, 7)
	clk.advance(60 * time.Millisecond)
	if got := drainHold(r); len(got) != 1 || got[0] != "two" {
		t.Fatalf("authenticated fast-recovery deadline delivery = %v, want [two]", got)
	}
}

func newRecoveryWindowResequencer(clk *fakeClock, hold time.Duration) *reseq.Resequencer {
	r := reseq.New(64, 250*time.Millisecond, clk)
	r.SetFECActive(true)
	r.SetRecoveryWindow(reseq.RecoveryWindow{
		Enabled:    true,
		Revision:   1,
		PathKey:    7,
		Source:     testSrc,
		Hold:       hold,
		ValidUntil: clk.Now().Add(250 * time.Millisecond),
	})
	r.ObserveFromPath(0, []byte("zero"), testSrc, 7)
	drainHold(r)
	r.ObserveFromPath(2, []byte("two"), testSrc, 7)
	return r
}

func TestFECRecoveryWindowBoundaryAndRepair(t *testing.T) {
	const window = 60 * time.Millisecond
	t.Run("W-1ns repair fills exactly once in order", func(t *testing.T) {
		clk := newFakeClock()
		r := newRecoveryWindowResequencer(clk, window)
		clk.advance(window - time.Nanosecond)
		if !r.ObserveRecovered(1, []byte("one"), testSrc) {
			t.Fatal("repair immediately before W was rejected")
		}
		if got := drainHold(r); len(got) != 2 || got[0] != "one" || got[1] != "two" {
			t.Fatalf("delivery after W-1ns repair = %v, want [one two]", got)
		}
		if r.ObserveRecovered(1, []byte("duplicate"), testSrc) {
			t.Fatal("duplicate repair was accepted")
		}
		if stats := r.Stats(); stats.RecoveryArmed ||
			stats.FastWindowArms != 1 ||
			stats.GapFills != 1 ||
			stats.DeadlineWakeups != 0 {
			t.Fatalf("repair-fill observability = %+v", stats)
		}
	})

	t.Run("W is expired and repair is rejected", func(t *testing.T) {
		clk := newFakeClock()
		r := newRecoveryWindowResequencer(clk, window)
		clk.advance(window)
		if r.ObserveRecovered(1, []byte("late"), testSrc) {
			t.Fatal("repair at the half-open deadline W was accepted")
		}
		if got := drainHold(r); len(got) != 1 || got[0] != "two" {
			t.Fatalf("delivery at W = %v, want [two]", got)
		}
		if stats := r.Stats(); stats.RecoveryArmed ||
			stats.FastWindowArms != 1 ||
			stats.GapFills != 0 ||
			stats.DeadlineWakeups != 1 {
			t.Fatalf("deadline-wake observability = %+v", stats)
		}
	})
}

func TestFECRecoveryWindowDeadlineNotificationAndRearm(t *testing.T) {
	clk := newFakeClock()
	changed := make(chan struct{}, 1)
	r := reseq.New(64, 250*time.Millisecond, clk)
	r.SetNotifier(func() {
		select {
		case changed <- struct{}{}:
		default:
		}
	})
	r.SetFECActive(true)
	r.SetRecoveryWindow(reseq.RecoveryWindow{
		Enabled:    true,
		Revision:   1,
		PathKey:    7,
		Source:     testSrc,
		Hold:       60 * time.Millisecond,
		ValidUntil: clk.Now().Add(250 * time.Millisecond),
	})
	for len(changed) > 0 {
		<-changed
	}
	r.ObserveFromPath(0, []byte("zero"), testSrc, 7)
	drainHold(r)
	armedAt := clk.Now()
	r.ObserveFromPath(2, []byte("two"), testSrc, 7)
	select {
	case <-changed:
	default:
		t.Fatal("gap arm did not publish a change notification")
	}
	if deadline, armed := r.ArmedDeadline(); !armed || deadline != armedAt.Add(60*time.Millisecond) {
		t.Fatalf("armed deadline = %v,%v, want %v,true", deadline, armed, armedAt.Add(60*time.Millisecond))
	}

	clk.advance(59 * time.Millisecond)
	for len(changed) > 0 {
		<-changed
	}
	r.ObserveFromPath(2, nil, testSrc, 8)
	rearmedAt := clk.Now()
	select {
	case <-changed:
	default:
		t.Fatal("conservative rearm did not publish a change notification")
	}
	if deadline, armed := r.ArmedDeadline(); !armed || deadline != rearmedAt.Add(250*time.Millisecond) {
		t.Fatalf("second-key rearm = %v,%v, want %v,true", deadline, armed, rearmedAt.Add(250*time.Millisecond))
	}
	if stats := r.Stats(); stats.RecoveryArmed ||
		stats.ArmedDeadline != rearmedAt.Add(250*time.Millisecond) ||
		stats.ArmedWindow != 250*time.Millisecond ||
		stats.FastWindowArms != 1 ||
		stats.FallbackWindowArms != 1 {
		t.Fatalf("fallback-rearm observability = %+v", stats)
	}
	clk.advance(250 * time.Millisecond)
	if got := drainHold(r); len(got) != 1 || got[0] != "two" {
		t.Fatalf("delivery at conservative rearm = %v, want [two]", got)
	}
}

func TestFECRecoveryWindowSourceTransitionRearmsConservatively(t *testing.T) {
	clk := newFakeClock()
	r := newRecoveryWindowResequencer(clk, 60*time.Millisecond)
	clk.advance(59 * time.Millisecond)
	roamed := netip.MustParseAddrPort("192.0.2.2:51820")
	r.ObserveFromPath(2, nil, roamed, 7)
	rearmedAt := clk.Now()
	if deadline, armed := r.ArmedDeadline(); !armed || deadline != rearmedAt.Add(250*time.Millisecond) {
		t.Fatalf("source-transition rearm = %v,%v, want %v,true", deadline, armed, rearmedAt.Add(250*time.Millisecond))
	}
}

func TestFECRecoveryWindowRebaselineClearsEvidenceAndNotifies(t *testing.T) {
	clk := newFakeClock()
	r := newRecoveryWindowResequencer(clk, 60*time.Millisecond)
	changed := make(chan struct{}, 1)
	r.SetNotifier(func() {
		select {
		case changed <- struct{}{}:
		default:
		}
	})
	r.Rebaseline(netip.AddrPort{})
	select {
	case <-changed:
	default:
		t.Fatal("rebaseline did not notify the receive drainer")
	}
	if deadline, armed := r.ArmedDeadline(); armed || !deadline.IsZero() {
		t.Fatalf("rebaseline left deadline armed: %v,%v", deadline, armed)
	}

	r.ObserveFromPath(0, []byte("new-zero"), testSrc, 7)
	drainHold(r)
	r.ObserveFromPath(2, []byte("new-two"), testSrc, 7)
	if deadline, armed := r.ArmedDeadline(); !armed || deadline != clk.Now().Add(250*time.Millisecond) {
		t.Fatalf("post-rebaseline gap reused old fast evidence: %v,%v", deadline, armed)
	}
}

func TestFECOffReleasesFastWindowGapImmediatelyAndNotifies(t *testing.T) {
	clk := newFakeClock()
	r := newRecoveryWindowResequencer(clk, 60*time.Millisecond)
	changed := make(chan struct{}, 1)
	r.SetNotifier(func() {
		select {
		case changed <- struct{}{}:
		default:
		}
	})
	r.SetFECActive(false)
	select {
	case <-changed:
	default:
		t.Fatal("FEC-off transition did not notify the receive drainer")
	}
	if got := drainHold(r); len(got) != 1 || got[0] != "two" {
		t.Fatalf("FEC-off delivery = %v, want [two] immediately", got)
	}
}

func TestFECRecoveryWindowFillExpireAndCloseNotify(t *testing.T) {
	t.Run("repair fill", func(t *testing.T) {
		clk := newFakeClock()
		r := newRecoveryWindowResequencer(clk, 60*time.Millisecond)
		changed := make(chan struct{}, 1)
		r.SetNotifier(func() {
			select {
			case changed <- struct{}{}:
			default:
			}
		})
		clk.advance(60*time.Millisecond - time.Nanosecond)
		if !r.ObserveRecovered(1, []byte("one"), testSrc) {
			t.Fatal("pre-expiry repair rejected")
		}
		select {
		case <-changed:
		default:
			t.Fatal("repair fill did not publish a change notification")
		}
	})

	t.Run("expiry", func(t *testing.T) {
		clk := newFakeClock()
		r := newRecoveryWindowResequencer(clk, 60*time.Millisecond)
		changed := make(chan struct{}, 1)
		r.SetNotifier(func() {
			select {
			case changed <- struct{}{}:
			default:
			}
		})
		clk.advance(60 * time.Millisecond)
		drainHold(r)
		select {
		case <-changed:
		default:
			t.Fatal("expiry did not publish a change notification")
		}
	})

	t.Run("close", func(t *testing.T) {
		clk := newFakeClock()
		r := newRecoveryWindowResequencer(clk, 60*time.Millisecond)
		changed := make(chan struct{}, 1)
		r.SetNotifier(func() {
			select {
			case changed <- struct{}{}:
			default:
			}
		})
		r.Close()
		select {
		case <-changed:
		default:
			t.Fatal("close did not publish a change notification")
		}
		if _, armed := r.ArmedDeadline(); armed {
			t.Fatal("close left the deadline armed")
		}
	})
}

func TestFECRecoveryWindowBoundedAdvanceStartsFreshGap(t *testing.T) {
	const (
		window = uint64(64)
		hold   = 60 * time.Millisecond
	)
	clk := newFakeClock()
	r := reseq.New(window, 250*time.Millisecond, clk)
	r.SetFECActive(true)
	r.SetRecoveryWindow(reseq.RecoveryWindow{
		Enabled:    true,
		Revision:   1,
		PathKey:    7,
		Source:     testSrc,
		Hold:       hold,
		ValidUntil: clk.Now().Add(time.Second),
	})
	r.ObserveFromPath(0, []byte("zero"), testSrc, 7)
	drainHold(r)
	r.ObserveFromPath(2, []byte("two"), testSrc, 7)
	clk.advance(hold - time.Nanosecond)

	// seq65 sits exactly one window ahead of the current gap at seq1. Admitting
	// it advances the bounded window, releases seq2, and exposes a distinct gap
	// [3,65) at the current injected time.
	r.ObserveFromPath(65, []byte("sixty-five"), testSrc, 7)
	if got := drainHold(r); len(got) != 1 || got[0] != "two" {
		t.Fatalf("bounded-advance delivery = %v, want [two]", got)
	}
	wantFresh := clk.Now().Add(hold)
	if deadline, armed := r.ArmedDeadline(); !armed || deadline != wantFresh {
		t.Fatalf("new gap deadline = %v,%v, want fresh %v,true", deadline, armed, wantFresh)
	}

	clk.advance(time.Nanosecond)
	if got := drainHold(r); len(got) != 0 {
		t.Fatalf("new gap inherited prior W and released early: %v", got)
	}
	clk.advance(hold - time.Nanosecond)
	if got := drainHold(r); len(got) != 1 || got[0] != "sixty-five" {
		t.Fatalf("fresh-gap expiry delivery = %v, want [sixty-five]", got)
	}
	if stats := r.Stats(); stats.Skipped != 63 || stats.Released != 3 {
		t.Fatalf("bounded advance stats = skipped %d released %d, want 63/3", stats.Skipped, stats.Released)
	}
	if got := drainHold(r); len(got) != 0 {
		t.Fatalf("bounded advance delivered a frame twice: %v", got)
	}
}

func TestClosedResequencerCannotRetainOrRepublishFastEvidence(t *testing.T) {
	clk := newFakeClock()
	r := reseq.New(16, 250*time.Millisecond, clk)
	r.SetFECActive(true)
	window := reseq.RecoveryWindow{
		Enabled:    true,
		Revision:   1,
		PathKey:    7,
		Source:     testSrc,
		Hold:       60 * time.Millisecond,
		ValidUntil: clk.Now().Add(time.Second),
	}
	r.SetRecoveryWindow(window)
	r.Close()
	window.Revision = 2
	if r.SetRecoveryGeneration(2, []reseq.RecoveryWindow{window}) {
		t.Fatal("post-Close recovery publication was accepted")
	}
	r.ObserveFromPath(0, []byte("zero"), testSrc, 7)
	drainHold(r)
	r.ObserveFromPath(2, []byte("two"), testSrc, 7)
	if deadline, armed := r.ArmedDeadline(); !armed || deadline != clk.Now().Add(250*time.Millisecond) {
		t.Fatalf("Close retained fast recovery evidence: %v,%v", deadline, armed)
	}
}

func TestRecoveryPublicationRequiresExactWindowGeneration(t *testing.T) {
	clk := newFakeClock()
	r := reseq.New(16, 250*time.Millisecond, clk)
	r.SetFECActive(true)
	if r.SetRecoveryGeneration(2, []reseq.RecoveryWindow{{
		Enabled:    true,
		Revision:   1,
		PathKey:    7,
		Source:     testSrc,
		Hold:       60 * time.Millisecond,
		ValidUntil: clk.Now().Add(time.Second),
	}}) {
		t.Fatal("publication accepted a venue from a different generation")
	}
	r.ObserveFromPath(0, []byte("zero"), testSrc, 7)
	drainHold(r)
	r.ObserveFromPath(2, []byte("two"), testSrc, 7)
	if deadline, armed := r.ArmedDeadline(); !armed || deadline != clk.Now().Add(250*time.Millisecond) {
		t.Fatalf("mismatched generation armed fast: %v,%v", deadline, armed)
	}
}

func TestRecoveryPublicationOrdersSameTopologyAndKeepsLiveGapImmutable(t *testing.T) {
	clk := newFakeClock()
	r := reseq.New(16, 250*time.Millisecond, clk)
	authority := &reseq.RecoveryAuthority{}
	authority.AdvanceTo(1, clk.Now())
	r.SetRecoveryAuthority(authority)
	r.SetFECActive(true)
	first := reseq.RecoveryWindow{
		Enabled:    true,
		Revision:   1,
		PathKey:    7,
		Source:     testSrc,
		Hold:       60 * time.Millisecond,
		ValidUntil: clk.Now().Add(time.Second),
	}
	if !r.SetRecoveryPublication(1, 1, []reseq.RecoveryWindow{first}) {
		t.Fatal("initial publication rejected")
	}
	r.ObserveFromPath(0, []byte("zero"), testSrc, 7)
	drainHold(r)
	r.ObserveFromPath(2, []byte("two"), testSrc, 7)
	immutableDeadline := clk.Now().Add(first.Hold)
	clk.advance(10 * time.Millisecond)

	second := first
	second.Hold = 100 * time.Millisecond
	if !r.SetRecoveryPublication(1, 2, []reseq.RecoveryWindow{second}) {
		t.Fatal("newer same-topology publication rejected")
	}
	if deadline, armed := r.ArmedDeadline(); !armed || deadline != immutableDeadline {
		t.Fatalf("same-topology evidence changed live gap: %v,%v want %v,true", deadline, armed, immutableDeadline)
	}
	if r.SetRecoveryPublication(1, 1, []reseq.RecoveryWindow{first}) {
		t.Fatal("older same-topology publication accepted")
	}
	if !r.SetRecoveryPublication(1, 2, []reseq.RecoveryWindow{second}) {
		t.Fatal("exact publication replay was not idempotent")
	}
	if r.SetRecoveryPublication(1, 2, []reseq.RecoveryWindow{first}) {
		t.Fatal("equal revision accepted different evidence")
	}

	r.ObserveFromPath(1, []byte("one"), testSrc, 7)
	if got := drainHold(r); len(got) != 2 || got[0] != "one" || got[1] != "two" {
		t.Fatalf("gap fill delivery = %v, want [one two]", got)
	}
	r.ObserveFromPath(4, []byte("four"), testSrc, 7)
	if deadline, armed := r.ArmedDeadline(); !armed || deadline != clk.Now().Add(second.Hold) {
		t.Fatalf("future gap did not use newer evidence: %v,%v", deadline, armed)
	}
}

func TestRecoveryAuthorityPreemptsOldFastExpiryBeforePublication(t *testing.T) {
	clk := newFakeClock()
	r := reseq.New(16, 250*time.Millisecond, clk)
	authority := &reseq.RecoveryAuthority{}
	authority.AdvanceTo(1, clk.Now())
	r.SetRecoveryAuthority(authority)
	r.SetFECActive(true)
	window := reseq.RecoveryWindow{
		Enabled:    true,
		Revision:   1,
		PathKey:    7,
		Source:     testSrc,
		Hold:       60 * time.Millisecond,
		ValidUntil: clk.Now().Add(time.Second),
	}
	if !r.SetRecoveryPublication(1, 1, []reseq.RecoveryWindow{window}) {
		t.Fatal("initial publication rejected")
	}
	r.ObserveFromPath(0, []byte("zero"), testSrc, 7)
	drainHold(r)
	r.ObserveFromPath(2, []byte("two"), testSrc, 7)
	clk.advance(window.Hold)
	authority.AdvanceTo(2, clk.Now())

	if got := drainHold(r); len(got) != 0 {
		t.Fatalf("old fast expiry released across topology authority advance: %v", got)
	}
	if deadline, armed := r.ArmedDeadline(); !armed || deadline != clk.Now().Add(250*time.Millisecond) {
		t.Fatalf("authority transition deadline = %v,%v, want fresh T", deadline, armed)
	}
	window.Revision = 2
	window.Hold = 100 * time.Millisecond
	if !r.SetRecoveryPublication(2, 1, []reseq.RecoveryWindow{window}) {
		t.Fatal("current-topology publication rejected")
	}
	if deadline, _ := r.ArmedDeadline(); deadline != clk.Now().Add(250*time.Millisecond) {
		t.Fatalf("current evidence shortened transition gap to %v", deadline)
	}
}

func TestRecoveryAuthorityLeavesFECOffGapUnchanged(t *testing.T) {
	clk := newFakeClock()
	r := reseq.New(16, 250*time.Millisecond, clk)
	authority := &reseq.RecoveryAuthority{}
	authority.AdvanceTo(1, clk.Now())
	r.SetRecoveryAuthority(authority)
	r.SetHoldBound(40 * time.Millisecond)
	r.Observe(0, []byte("zero"), testSrc)
	drainHold(r)
	r.Observe(2, []byte("two"), testSrc)
	want := clk.Now().Add(40 * time.Millisecond)
	authority.AdvanceTo(2, clk.Now())
	if deadline, armed := r.ArmedDeadline(); !armed || deadline != want {
		t.Fatalf("FEC-off authority update changed gap: %v,%v want %v,true", deadline, armed, want)
	}
}

func TestClosedResequencerRejectsAuthoritativePublication(t *testing.T) {
	clk := newFakeClock()
	r := reseq.New(16, 250*time.Millisecond, clk)
	authority := &reseq.RecoveryAuthority{}
	authority.AdvanceTo(1, clk.Now())
	r.SetRecoveryAuthority(authority)
	r.SetFECActive(true)
	r.Close()
	authority.AdvanceTo(2, clk.Now())
	if r.SetRecoveryPublication(2, 1, []reseq.RecoveryWindow{{
		Enabled:    true,
		Revision:   2,
		PathKey:    7,
		Source:     testSrc,
		Hold:       60 * time.Millisecond,
		ValidUntil: clk.Now().Add(time.Second),
	}}) {
		t.Fatal("closed resequencer accepted authoritative publication")
	}
}

func TestRecoveryAuthorityCoalescesAndKeepsGenerationTimeCoherent(t *testing.T) {
	clk := newFakeClock()
	authority := &reseq.RecoveryAuthority{}
	changed := make(chan struct{}, 1)
	authority.SetChangeSignal(changed)

	firstAt := clk.Now()
	authority.AdvanceTo(1, firstAt)
	clk.advance(time.Millisecond)
	secondAt := clk.Now()
	authority.AdvanceTo(2, secondAt)
	clk.advance(time.Millisecond)
	thirdAt := clk.Now()
	authority.AdvanceTo(3, thirdAt)
	if len(changed) != 1 {
		t.Fatalf("coalesced authority notifications = %d, want 1", len(changed))
	}
	if state := authority.State(); state.Generation != 3 || state.TransitionAt != thirdAt {
		t.Fatalf("latest authority state = %+v, want generation 3 at %v", state, thirdAt)
	}

	<-changed
	authority.AdvanceTo(2, clk.Now().Add(time.Second))
	select {
	case <-changed:
		t.Fatal("older generation emitted a notification")
	default:
	}
	if state := authority.State(); state.Generation != 3 || state.TransitionAt != thirdAt {
		t.Fatalf("older generation changed authority state to %+v", state)
	}

	authority.SetChangeSignal(nil)
	authority.AdvanceTo(4, clk.Now())
	select {
	case <-changed:
		t.Fatal("detached authority emitted a notification")
	default:
	}
}
