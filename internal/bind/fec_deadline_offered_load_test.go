package bind

import (
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/fec"
)

// --- T294 step 1: the D109 reproduce-first fixture (deadline-close phantom) ----------
//
// fecFlushDeadline selects a path for a DEADLINE-closed FEC group's straggler parity with
// peer.scheduler.Pick(sched.ClassData, 1). A deadline flush emits NO data frame, and the
// parity it writes is metered — correctly, exactly once — through the peer's parity carry
// on the peer's NEXT Send (decisions:K35 §3c/§9.4). So after T290 made every other Pick's
// `frames` argument denote real WIRE frames, that literal 1 is a PHANTOM offered wire
// frame: the only Pick caller in the repo whose argument stands for no frame. It biases the
// offered-load estimator UP by one frame per deadline flush, in the DISENGAGE direction —
// keeping the metered path engaged at the idle/thrift load where deadline closes DOMINATE
// (defect D109).
//
// This pins the SAME invariant TestParityCarryAccountsEveryWrittenParityFrameExactlyOnce
// pins for SIZE-closed groups — the total wire frames the scheduler was asked to place (the
// sum of Pick's `frames` over every call) plus whatever is still pending in the carry
// EQUALS the data + parity frames the datapath actually wrote to the socket — but drives
// groups that close by DEADLINE, the path T290's fixture never exercises (it only fills
// groups by size). The phantom lives on that path alone.

// TestFECDeadlineFlushMetersNoPhantomOfferedFrame is T294 step 1. It drives a low-rate
// FEC-on flow whose groups NEVER fill (fewer than K buffers per Send), spaced far enough
// apart that the bind's own deadline-tick goroutine (fecTickLoop → fecFlushDeadline, the
// real production flusher) closes each partial group before the next Send opens a new one.
// It then asserts the offered-vs-written frame-count invariant.
//
// RED before the T294 fix: fecFlushDeadline calls Pick(sched.ClassData, 1), so the counting
// scheduler is offered one PHANTOM frame per deadline flush on top of the parity already
// metered through the carry, and the offered total exceeds the written total by the number
// of deadline flushes.
func TestFECDeadlineFlushMetersNoPhantomOfferedFrame(t *testing.T) {
	const (
		// partialBatch < fecDataShards, so no group ever closes by FILL — every close is a
		// DEADLINE close through fecFlushDeadline, exactly the regime D109 lives in.
		partialBatch = 2
		rounds       = 6
		// The FEC encoder runs on a REAL (SystemClock) clock — NOT the scheduler's injected
		// fake clock — and the bind's fecTickLoop ticks at flushDeadline in real time, so a
		// real span past the grouping deadline is what closes each open group. sendSpacing is
		// a generous multiple of flushDeadline, so each partial group is provably deadline-
		// flushed by the tick loop before the next Send opens a new one (time.Sleep never
		// undersleeps); the assertion is a frame COUNT invariant, independent of exact timing.
		flushDeadline = 10 * time.Millisecond
		sendSpacing   = 50 * time.Millisecond
	)
	if partialBatch >= fecDataShards {
		t.Fatalf("fixture error: partialBatch %d must be < fecDataShards %d so groups close by deadline, not fill", partialBatch, fecDataShards)
	}

	cfg := weightedOfferedLoadCfg()
	psk := testKey(t, 0x9e)
	clk := newFakeClock()
	fecCfg := &fec.Config{DataShards: fecDataShards, ParityShards: fecParityShards, Deadline: flushDeadline}
	m, counter := newCountingMultipath(t, loopbackPaths(1), psk, fecCfg, cfg, clk)
	openWeightedToPeer(t, m) // starts the bind's fecTickLoop deadline-flush goroutine

	for i := 0; i < rounds; i++ {
		// Offer a PARTIAL group: partialBatch data frames out, the group left OPEN (it does
		// not reach K), so it can only ever be closed by the deadline tick loop.
		if err := m.Send(payloadStream(partialBatch), m.virt); err != nil {
			t.Fatalf("Send #%d: %v", i, err)
		}
		// Let the deadline tick loop close and flush the open partial group before the next
		// Send. sendSpacing >> flushDeadline, so the group is flushed (and its parity credited
		// to the carry) with margin to spare.
		time.Sleep(sendSpacing)
	}
	// Settle: no more Sends, so once the final partial group has been deadline-flushed the
	// tick loop finds no open group and writes nothing further — the counters below are then
	// stable and read without a concurrent writer.
	time.Sleep(sendSpacing)

	fs := m.fecSend.Load()
	if fs == nil {
		t.Fatal("fixture error: FEC sender is nil with FEC configured")
	}
	wroteData := fs.dataFrames.Load()
	wroteParity := fs.parityFrames.Load()
	wrote := wroteData + wroteParity
	offered := uint64(counter.frames.Load())
	pending := m.parityCarry.Load()

	// The deadline path must actually have been exercised: whole groups' worth of parity
	// reached the socket, none of it from a size-close (no Send ever filled a group).
	if wroteParity == 0 {
		t.Fatal("fixture error: no parity frames were written, so the deadline-flush path was never exercised")
	}
	if wroteParity%uint64(fecParityShards) != 0 {
		t.Fatalf("wrote %d parity frames, not a whole multiple of M=%d: a group closed with unexpected parity", wroteParity, fecParityShards)
	}
	if wroteData != uint64(rounds*partialBatch) {
		t.Fatalf("wrote %d data frames, want %d (each of %d rounds offers exactly %d data buffers)", wroteData, rounds*partialBatch, rounds, partialBatch)
	}

	if offered+pending != wrote {
		t.Fatalf("scheduler was asked to place %d wire frames with %d still pending in the carry, "+
			"but %d frames (%d data + %d parity) actually reached the socket over %d deadline-closed rounds: "+
			"the offered total EXCEEDS the written total by %d — one PHANTOM offered frame per fecFlushDeadline "+
			"Pick(sched.ClassData, 1), on top of the parity already metered exactly once via the carry (defect D109, K35 §3c/§9.4)",
			offered, pending, wrote, wroteData, wroteParity, rounds, int64(offered+pending)-int64(wrote))
	}
	t.Logf("over %d deadline-closed rounds: %d data + %d parity written, %d offered to the scheduler, %d pending; offered+pending == written (no phantom)",
		rounds, wroteData, wroteParity, offered, pending)
}
