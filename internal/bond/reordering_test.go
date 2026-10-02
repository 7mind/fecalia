package bond

import (
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/frame"
)

// A path that delivers in order reorders nothing. The attempts one
// acknowledgement confirms were compared with each other in map order, so an
// in-order path measured a reordering of the time one acknowledgement spans,
// differently on every run.
func TestInOrderPathShowsNoReordering(t *testing.T) {
	type inFlight struct {
		at    time.Time
		to    int
		frame frame.Control
	}
	const delay = 20 * time.Millisecond
	start := time.Unix(100, 0)
	peers := [2]*Transport{New(Epoch{Boot: 1, Generation: 1}), New(Epoch{Boot: 2, Generation: 1})}
	for side, p := range peers {
		p.SetRemote(peers[1-side].Epoch(), true)
	}
	var wire []inFlight
	now := start
	for tick := 0; tick < 3000; tick++ {
		now = start.Add(time.Duration(tick) * time.Millisecond)
		for side, p := range peers {
			p.Path(0, 0, 2*delay, now)
			if side == 0 {
				for range 4 {
					_ = p.Enqueue(make([]byte, 1300), PacketMetadata{Flow: FlowID{4, 6, 1}}, now)
				}
			}
			for _, tx := range poll(p, now) {
				wire = append(wire, inFlight{now.Add(delay), 1 - side, tx.Frame})
			}
		}
		for len(wire) > 0 && !wire[0].at.After(now) {
			if _, err := peers[wire[0].to].Receive(0, wire[0].frame, now); err != nil {
				t.Fatal(err)
			}
			wire = wire[1:]
		}
	}
	if reordering := time.Duration(peers[0].paths[0].reordering.value(now)); reordering != 0 {
		t.Fatalf("a path that delivers in order measured a reordering of %s", reordering)
	}
}
