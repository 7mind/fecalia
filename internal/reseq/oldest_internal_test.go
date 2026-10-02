package reseq

import (
	"math/rand/v2"
	"net/netip"
	"testing"
	"time"
)

type steppedClock struct{ now time.Time }

func (c *steppedClock) Now() time.Time { return c.now }

// The oldest buffered observation is kept in order of arrival; it must be what
// a scan of the window finds, through reordering, duplicates, loss, expiry and
// jumps beyond the window. The list must also stay bounded.
func TestOldestBufferedObservationMatchesAScanOfTheWindow(t *testing.T) {
	const window = 64
	clock := &steppedClock{now: time.Unix(1_700_000_000, 0)}
	r := New(window, 50*time.Millisecond, clock)
	r.RebaselineAt(1)
	random := rand.New(rand.NewPCG(1, 2))
	src := netip.MustParseAddrPort("192.0.2.1:51820")
	scan := func() (time.Time, bool) {
		var oldest time.Time
		found := false
		for i := range r.ring {
			if cell := &r.ring[i]; cell.occupied && cell.seq >= r.next && (!found || cell.observedAt.Before(oldest)) {
				oldest, found = cell.observedAt, true
			}
		}
		return oldest, found
	}
	base := uint64(1)
	for step := range 200000 {
		clock.now = clock.now.Add(time.Duration(random.IntN(3000)) * time.Microsecond)
		seq := base + uint64(random.IntN(40))
		switch random.IntN(50) {
		case 0:
			seq += 3 * window // a burst lost: the window advances
		case 1:
			r.RebaselineAt(seq)
		}
		r.Observe(seq, []byte{1}, src)
		if random.IntN(3) == 0 {
			base++
		}
		for random.IntN(2) == 0 {
			if _, ok := r.Pop(); !ok {
				break
			}
		}
		r.mu.Lock()
		wantAt, want := scan()
		gotAt, got := r.oldestBufferedObservation()
		held := len(r.held)
		r.mu.Unlock()
		if got != want || !gotAt.Equal(wantAt) {
			t.Fatalf("step %d: oldest buffered observation %v (%v), a scan finds %v (%v)", step, gotAt, got, wantAt, want)
		}
		if held > 4*window {
			t.Fatalf("step %d: %d arrivals listed for a window of %d", step, held, window)
		}
	}
}
