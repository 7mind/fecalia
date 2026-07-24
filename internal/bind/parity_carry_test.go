package bind

import (
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/fec"
	"github.com/7mind/wanbond/internal/log"
	"github.com/7mind/wanbond/internal/sched"
	"github.com/7mind/wanbond/internal/telemetry"
)

// --- T290 step 4b: the FEC parity carry at the bind (defect D95, K35 §3c) ------------
//
// Multipath.Send tells its scheduler how many OFFERED WIRE FRAMES one selection decision
// covers: len(bufs) plus the peer's pending FEC parity carry. These tests pin that the
// carry neither drops a parity frame nor counts one twice, and that with FEC OFF the
// datapath is byte-identical to "offered load == len(bufs)".

// countingScheduler wraps the real WeightedScheduler and records what Send offered it.
// Everything except Pick is promoted from the embedded scheduler, so the bind sees a
// genuine DynamicScheduler/ProbeBudget and the gate under test is the real one.
type countingScheduler struct {
	*sched.WeightedScheduler
	picks  atomic.Int64
	frames atomic.Int64
}

func (c *countingScheduler) Pick(class sched.FrameClass, frames int) int {
	c.picks.Add(1)
	c.frames.Add(int64(frames))
	return c.WeightedScheduler.Pick(class, frames)
}

// newCountingMultipath is newWeightedMultipath with the offered-frame counter spliced in
// between the bind and the real weighted scheduler.
func newCountingMultipath(t testing.TB, paths []config.Path, psk config.Key, fecCfg *fec.Config, cfg sched.WeightedConfig, clk telemetry.Clock) (*Multipath, *countingScheduler) {
	t.Helper()
	lg, err := log.New("error", io.Discard)
	if err != nil {
		t.Fatalf("build logger: %v", err)
	}
	health := make([]sched.PathHealth, len(paths))
	quality := make([]sched.PathQuality, len(paths))
	for i := range paths {
		health[i] = sched.AlwaysUp{}
	}
	ws, err := sched.NewWeighted(health, quality, cfg, clk, lg)
	if err != nil {
		t.Fatalf("build weighted scheduler: %v", err)
	}
	counter := &countingScheduler{WeightedScheduler: ws}
	m, err := NewMultipath(paths, psk, counter, nil, nil, fecCfg, nil, config.Amnezia{}, lg)
	if err != nil {
		t.Fatalf("NewMultipath(counting): %v", err)
	}
	return m, counter
}

// TestOfferedFramesEqualBatchSizeWithFECOff is step 4b(g): with FEC OFF the parity carry
// is NEVER non-zero and the scheduler is offered exactly len(bufs) frames per Send. This
// is the byte-identity proof for the shipped default (FEC is opt-in and off everywhere by
// default): the carry mechanism adds nothing to that datapath but a constant-zero atomic
// read.
func TestOfferedFramesEqualBatchSizeWithFECOff(t *testing.T) {
	cfg := weightedOfferedLoadCfg()
	psk := testKey(t, 0x9a)
	clk := newFakeClock()
	m, counter := newCountingMultipath(t, loopbackPaths(1), psk, nil, cfg, clk)
	openWeightedToPeer(t, m)

	const (
		sends = 12
		batch = 5
		dt    = 3 * time.Millisecond
	)
	payloads := payloadStream(batch)
	for i := 0; i < sends; i++ {
		if i > 0 {
			clk.advance(dt)
		}
		if err := m.Send(payloads, m.virt); err != nil {
			t.Fatalf("Send #%d: %v", i, err)
		}
		if carry := m.parityCarry.Load(); carry != 0 {
			t.Fatalf("parity carry = %d after Send #%d with FEC OFF, want 0 (no parity exists to carry)", carry, i)
		}
	}

	if got, want := counter.picks.Load(), int64(sends); got != want {
		t.Fatalf("scheduler saw %d Pick calls, want %d (one selection decision per Send)", got, want)
	}
	if got, want := counter.frames.Load(), int64(sends*batch); got != want {
		t.Fatalf("scheduler was offered %d frames over %d Sends of %d buffers, want %d", got, sends, batch, want)
	}

	expected := make([]int, sends)
	for i := range expected {
		expected[i] = batch
	}
	wantFPS := foldOfferedLoad(expected, dt, cfg.LoadTau)
	if got := counter.AggregationSnapshot().OfferedLoadFPS; !closeEnough(got, wantFPS, 1e-9) {
		t.Fatalf("smoothed offered load = %g fps, want %g fps (analytic fold of %d batches of %d)", got, wantFPS, sends, batch)
	}
}

// TestParityCarryAccountsEveryWrittenParityFrameExactlyOnce is step 4b(h): with FEC ON,
// over a run of R batched Sends the TOTAL frames the scheduler was offered, plus whatever
// carry is still pending, must equal the DATA plus PARITY frames the datapath actually
// wrote to the socket — as counted by the pre-existing FEC metrics counters. That is the
// no-drop / no-double-count invariant of the one-batch-late carry (K35 §3c): every parity
// frame that reached the wire is metered exactly once, in the Send after it egressed.
func TestParityCarryAccountsEveryWrittenParityFrameExactlyOnce(t *testing.T) {
	cfg := weightedOfferedLoadCfg()
	psk := testKey(t, 0x9b)
	clk := newFakeClock()
	fecCfg := &fec.Config{DataShards: fecDataShards, ParityShards: fecParityShards, Deadline: testFECDeadline}
	m, counter := newCountingMultipath(t, loopbackPaths(1), psk, fecCfg, cfg, clk)
	openWeightedToPeer(t, m)

	const (
		rounds = 25
		dt     = 4 * time.Millisecond
	)
	batch := payloadStream(fecDataShards)
	for i := 0; i < rounds; i++ {
		if i > 0 {
			clk.advance(dt)
		}
		if err := m.Send(batch, m.virt); err != nil {
			t.Fatalf("Send #%d: %v", i, err)
		}
		// The carry can never accumulate: each Send drains it, and each Send closes
		// exactly one group, so at most one group's parity is ever pending.
		if carry := m.parityCarry.Load(); carry != uint64(fecParityShards) {
			t.Fatalf("parity carry = %d after Send #%d, want %d (exactly the last group's parity — a growing carry means Send is not draining it)",
				carry, i, fecParityShards)
		}
	}

	fs := m.fecSend.Load()
	if fs == nil {
		t.Fatal("fixture error: FEC sender is nil with FEC configured")
	}
	wrote := fs.dataFrames.Load() + fs.parityFrames.Load()
	offered := uint64(counter.frames.Load())
	pending := m.parityCarry.Load()
	if offered+pending != wrote {
		t.Fatalf("scheduler was offered %d frames with %d still pending in the carry, but %d frames (%d data + %d parity) reached the socket: "+
			"the carry dropped or double-counted %d frames over %d batches",
			offered, pending, wrote, fs.dataFrames.Load(), fs.parityFrames.Load(), int64(wrote)-int64(offered+pending), rounds)
	}
	if got, want := counter.picks.Load(), int64(rounds); got != want {
		t.Fatalf("scheduler saw %d Pick calls, want %d (the carry must ride the existing per-batch Pick, adding no second seam)", got, want)
	}
	t.Logf("over %d batches: %d data + %d parity written, %d offered to the scheduler, %d pending in the carry",
		rounds, fs.dataFrames.Load(), fs.parityFrames.Load(), offered, pending)
}

// TestEmptySendMetersPendingParityCarry is step 4b(i), and it records WHICH empty-batch
// semantic was implemented: a Send with NO buffers but a PENDING parity carry still calls
// Pick and meters the carry, so no parity is ever silently lost; a Send with no buffers
// AND no pending carry returns early WITHOUT calling Pick.
//
// WHY THAT SPLIT. Returning early on len(bufs) == 0 alone would strand the last group's
// parity behind whatever gap follows, which is exactly the under-count the carry exists to
// remove. Conversely, calling Pick with nothing to offer would violate the frames >= 1
// caller contract (the scheduler panics on it) AND would re-register a spurious offered
// event — a pre-existing wart of the old unconditional per-batch Pick, removed here.
func TestEmptySendMetersPendingParityCarry(t *testing.T) {
	cfg := weightedOfferedLoadCfg()
	psk := testKey(t, 0x9c)
	clk := newFakeClock()
	fecCfg := &fec.Config{DataShards: fecDataShards, ParityShards: fecParityShards, Deadline: testFECDeadline}
	m, counter := newCountingMultipath(t, loopbackPaths(1), psk, fecCfg, cfg, clk)
	openWeightedToPeer(t, m)

	// One full group: K data frames out, M parity frames written, M left in the carry.
	if err := m.Send(payloadStream(fecDataShards), m.virt); err != nil {
		t.Fatalf("Send (full group): %v", err)
	}
	if got := m.parityCarry.Load(); got != uint64(fecParityShards) {
		t.Fatalf("parity carry after the first group = %d, want %d", got, fecParityShards)
	}
	picksAfterGroup, framesAfterGroup := counter.picks.Load(), counter.frames.Load()

	// (i) An EMPTY batch with a pending carry: Pick IS called, offered exactly the carry.
	if err := m.Send(nil, m.virt); err != nil {
		t.Fatalf("empty Send with a pending carry: %v", err)
	}
	if got, want := counter.picks.Load(), picksAfterGroup+1; got != want {
		t.Fatalf("Pick calls after the empty-with-carry Send = %d, want %d (the carry must be metered, not stranded)", got, want)
	}
	if got, want := counter.frames.Load(), framesAfterGroup+int64(fecParityShards); got != want {
		t.Fatalf("offered frames after the empty-with-carry Send = %d, want %d (exactly the %d pending parity frames)", got, want, fecParityShards)
	}
	if got := m.parityCarry.Load(); got != 0 {
		t.Fatalf("parity carry after it was metered = %d, want 0 (Swap(0) must consume it)", got)
	}

	// An EMPTY batch with NO pending carry: no Pick at all, and no error.
	picksAfterDrain, framesAfterDrain := counter.picks.Load(), counter.frames.Load()
	if err := m.Send(nil, m.virt); err != nil {
		t.Fatalf("empty Send with an empty carry: %v", err)
	}
	if got := counter.picks.Load(); got != picksAfterDrain {
		t.Fatalf("Pick calls after the empty-and-drained Send = %d, want %d (nothing was offered, so nothing may be metered)", got, picksAfterDrain)
	}
	if got := counter.frames.Load(); got != framesAfterDrain {
		t.Fatalf("offered frames after the empty-and-drained Send = %d, want %d", got, framesAfterDrain)
	}
}

// TestFECOnThriftLoadStaysCollapsedThroughTheCarry is step 4b(j): the FEC-ON THRIFT
// COMPOSITION. It re-drives the sched-level FEC-expansion boundary
// (TestFECExpansionThriftCollapseBoundary's 4+2-at-honest-capacity case) END TO END
// through the real Send path and the real parity carry, so the boundary is shown not to
// be an artefact of the unit-level rate model: with parity actually flowing to the socket,
// a K=4/M=2 thrift load against a capacity declared at the path's HONEST WIRE RATE still
// collapses the gate, and the metered path still goes idle.
//
// SCOPE, per decisions:K35 §3h: the margin here depends on the capacity being sized from
// the WIRE frame rate. Against the e2e fixture's deliberately conservative 3000 the same
// 4+2 load does NOT collapse — that case is pinned at unit level and is documented
// behaviour, not a defect.
func TestFECOnThriftLoadStaysCollapsedThroughTheCarry(t *testing.T) {
	const (
		// The honest wire frame rate of the measured 40 Mbit/s path: 40e6/(8*1470) ~=
		// 3401 fps. A link's wire capacity is FEC-independent, which is why it is the
		// sizing that makes both gate directions correct at once (K35 §3h).
		honestWireCapacityFPS = 3400.0
		// The 12 Mbit/s "5G-idle" thrift DATA rate tasks:T284 measured (1042-1061 wire
		// fps with FEC off); the conservative upper end is used.
		thriftDataFPS = 1061.0
		collapseDwell = 300 * time.Millisecond
	)

	cfg := weightedOfferedLoadCfg()
	cfg.PerPathCapacity = honestWireCapacityFPS
	cfg.CollapseDwell = collapseDwell
	engage := cfg.EngageFraction * cfg.PerPathCapacity
	disengage := cfg.DisengageFraction * cfg.PerPathCapacity

	// Each Send is one full FEC group: K data frames plus M parity frames on the wire.
	const wireFramesPerSend = fecDataShards + fecParityShards
	expansion := float64(wireFramesPerSend) / float64(fecDataShards)
	thriftWireFPS := expansion * thriftDataFPS
	fMax := disengage / thriftDataFPS
	if expansion >= fMax {
		t.Fatalf("fixture error: expansion %.3f must be below fMax = DisengageFraction*capacity/thriftDataFPS = %.4f for this case to collapse",
			expansion, fMax)
	}

	psk := testKey(t, 0x9d)
	clk := newFakeClock()
	fecCfg := &fec.Config{DataShards: fecDataShards, ParityShards: fecParityShards, Deadline: testFECDeadline}
	m, counter := newCountingMultipath(t, loopbackPaths(1), psk, fecCfg, cfg, clk)
	openWeightedToPeer(t, m)

	batch := payloadStream(fecDataShards)
	drive := func(wireFPS float64, span time.Duration) {
		dt := time.Duration(float64(time.Second) * wireFramesPerSend / wireFPS)
		for elapsed := time.Duration(0); elapsed < span; elapsed += dt {
			if err := m.Send(batch, m.virt); err != nil {
				t.Fatalf("Send at %g wire fps: %v", wireFPS, err)
			}
			clk.advance(dt)
		}
	}

	// Engage the gate with a saturating offer, so the assertion below measures the
	// COLLAPSE direction (where the metered-cost guarantee lives).
	drive(2*honestWireCapacityFPS, offeredWarmupTaus*cfg.LoadTau)
	if !counter.AggregationSnapshot().Aggregating {
		t.Fatalf("fixture error: gate did not engage at %g offered wire fps against engage %g", 2*honestWireCapacityFPS, engage)
	}

	// Drop to the thrift load and hold it well past the collapse dwell.
	drive(thriftWireFPS, 6*collapseDwell)

	snap := counter.AggregationSnapshot()
	if snap.Aggregating {
		t.Fatalf("gate stayed ENGAGED at a %g wire fps FEC-%d+%d thrift load (data %g fps x expansion %.2f) after %s: "+
			"smoothed %.1f fps against disengage %g — the metered path must collapse",
			thriftWireFPS, fecDataShards, fecParityShards, thriftDataFPS, expansion, 6*collapseDwell, snap.OfferedLoadFPS, disengage)
	}

	fs := m.fecSend.Load()
	if fs == nil {
		t.Fatal("fixture error: FEC sender is nil with FEC configured")
	}
	if fs.parityFrames.Load() == 0 {
		t.Fatal("fixture error: no parity frames were written, so the carry path was never exercised")
	}
	offered := uint64(counter.frames.Load())
	wrote := fs.dataFrames.Load() + fs.parityFrames.Load()
	if offered+m.parityCarry.Load() != wrote {
		t.Fatalf("offered %d frames + %d pending carry != %d written: the carry drifted over the run", offered, m.parityCarry.Load(), wrote)
	}
	t.Logf("FEC %d+%d thrift composition: offered %g wire fps (data %g x f=%.2f), smoothed %.1f fps, disengage %g, "+
		"margin %.1f fps (%.1f%% of the threshold); fMax = %.4f; %d data + %d parity frames written, %d offered",
		fecDataShards, fecParityShards, thriftWireFPS, thriftDataFPS, expansion, snap.OfferedLoadFPS, disengage,
		disengage-thriftWireFPS, 100*(disengage-thriftWireFPS)/disengage, fMax,
		fs.dataFrames.Load(), fs.parityFrames.Load(), offered)
}
