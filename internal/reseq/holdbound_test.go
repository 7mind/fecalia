package reseq_test

import (
	"net/netip"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/reseq"
)

// holdSrc is the fixed outer source the hold-bound tests ingest under; the hold
// logic is src-agnostic, so one address suffices.
var holdSrc = netip.MustParseAddrPort("192.0.2.1:1000")

// drainHold pops every currently-deliverable frame's payload (as a string —
// Item carries no seq, so the tests key deliveries by payload identity).
func drainHold(r *reseq.Resequencer) []string {
	var got []string
	for {
		it, ok := r.Pop()
		if !ok {
			return got
		}
		got = append(got, string(it.Payload))
	}
}

// gapHoldDuration measures, under the fake clock, how long a head-of-line gap is
// held before its successor releases: it pins the release point at 0
// (delivered), buffers seq 2 behind the missing seq 1, then advances the clock
// in 1 ms steps until seq 2 pops.
func gapHoldDuration(t *testing.T, r *reseq.Resequencer, clk *fakeClock) time.Duration {
	t.Helper()
	r.Observe(0, []byte("p0"), holdSrc)
	if got := drainHold(r); len(got) != 1 || got[0] != "p0" {
		t.Fatalf("seed delivery = %v, want [p0]", got)
	}
	r.Observe(2, []byte("p2"), holdSrc)
	if got := drainHold(r); len(got) != 0 {
		t.Fatalf("gap released %v before any hold elapsed", got)
	}
	start := clk.now
	for i := 0; i < 1000; i++ {
		clk.advance(time.Millisecond)
		r.Observe(2, nil, holdSrc) // duplicate: dropped, but ticks expire()
		if got := drainHold(r); len(got) > 0 {
			return clk.now.Sub(start)
		}
	}
	t.Fatalf("gap never released within 1s")
	return 0
}

// TestSetHoldBoundShortensHold is the T241 core: with a hold bound set, a gap is
// held ~the bound, not the full 250 ms construction timeout.
func TestSetHoldBoundShortensHold(t *testing.T) {
	clk := newFakeClock()
	r := reseq.New(64, 250*time.Millisecond, clk)
	r.SetHoldBound(80 * time.Millisecond) // e.g. k=4 x SRTT=20ms
	if held := gapHoldDuration(t, r, clk); held < 75*time.Millisecond || held > 90*time.Millisecond {
		t.Fatalf("gap held %v, want ~80ms (the dynamic bound, not the 250ms cap)", held)
	}
}

// TestSetHoldBoundClampsToCapAndFloor pins the clamp: a bound above the
// construction timeout is capped there (the worst-case bound is preserved), and
// a bound below the floor is raised to the floor.
func TestSetHoldBoundClampsToCapAndFloor(t *testing.T) {
	clk := newFakeClock()
	r := reseq.New(64, 250*time.Millisecond, clk)
	r.SetHoldBound(2 * time.Second) // k*SRTT on a slow path: capped at 250ms
	if held := gapHoldDuration(t, r, clk); held < 245*time.Millisecond || held > 260*time.Millisecond {
		t.Fatalf("over-cap bound held %v, want ~250ms (capped at the construction timeout)", held)
	}

	clk2 := newFakeClock()
	r2 := reseq.New(64, 250*time.Millisecond, clk2)
	r2.SetHoldBound(time.Millisecond) // k*SRTT on an ultra-low-RTT path: floored
	if held := gapHoldDuration(t, r2, clk2); held < 8*time.Millisecond || held > 20*time.Millisecond {
		t.Fatalf("under-floor bound held %v, want ~the floor (10ms)", held)
	}
}
