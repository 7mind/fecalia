package bind

import (
	"net/netip"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/reseq"
)

// At 600 Mbit/s the bond carries about 50000 datagrams a second. A lost one is
// repaired 100-250 ms later, by when 5000-12500 later datagrams have arrived.
// The resequencer must still be holding the gap: abandoning it is a loss to
// TCP, and with a window of 2048 frames it was abandoned as soon as the flow
// exceeded about 10000 datagrams a second, holding a TCP transfer near
// 100 Mbit/s on 300+300 Mbit/s links (VM runs of 2026-09-30: every skipped
// sequence arrived afterwards as a stale frame).
func TestResequencerHoldsARepairAtHighRate(t *testing.T) {
	const (
		datagramsPerSecond = 50000
		repairAfter        = 250 * time.Millisecond
	)
	clock := newFakeClock()
	rq := reseq.New(resequencerWindow, resequencerTimeout, clock)
	rq.SetMultiPathExpected(true)
	rq.SetHoldBound(adaptiveReorderHold)
	source := netip.MustParseAddrPort("192.0.2.1:51820")
	later := uint64(datagramsPerSecond * repairAfter / time.Second)
	step := repairAfter / time.Duration(later)
	rq.Observe(1, []byte{1}, source)
	// Sequence 2 is lost; its repair arrives after the datagrams behind it.
	for seq := uint64(3); seq < 3+later; seq++ {
		clock.advance(step)
		rq.Observe(seq, []byte{1}, source)
	}
	rq.Observe(2, []byte{2}, source)
	if skipped := rq.Stats().Skipped; skipped != 0 {
		t.Fatalf("resequencer abandoned %d sequences while %d later datagrams arrived in %s", skipped, later, repairAfter)
	}
	for want := uint64(1); want < 3+later; want++ {
		if _, ok := rq.Pop(); !ok {
			t.Fatalf("sequence %d was not released in order", want)
		}
	}
}
