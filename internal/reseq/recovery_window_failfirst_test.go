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
