package bind

import (
	"io"
	"math"
	"net"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/fec"
	"github.com/7mind/wanbond/internal/log"
	"github.com/7mind/wanbond/internal/sched"
	"github.com/7mind/wanbond/internal/telemetry"
)

// --- T290 step 1: the D95 reproduce-first fixture (frames vs batches) ---------------
//
// D95's dominant, hardware-measured cause (tasks:T284, decisions:K35 §2) is a UNITS
// mismatch: Multipath.Send calls scheduler.Pick ONCE PER BATCH, outside the per-buffer
// loop, so the weighted scheduler's offered-load estimator counts Send BATCHES per
// second while PerPathCapacity — the denominator it is compared against — is a WIRE
// FRAME rate (config.SizePacingFromBDP computes bandwidth/(8*avgWireFrameBytes); the
// e2e fixture derives its 3000 from p2RateMbit/8/innerMTU). The three tests below pin
// the CORRECT (frame-accurate) semantic end-to-end through the real Send path:
//
//	(1a) TestSendMetersEveryBufferOfTheBatchAsOneOfferedFrame — units, FEC OFF.
//	(1b) TestBatchedOfferAboveEngageWireRateEngagesTheGate    — the D95 signature.
//	(1c) TestSendMetersFECParityAsOfferedWireFrames           — units, FEC ON.
//
// The fixture is a FAKE-CLOCK one: the weighted scheduler is built over an injected
// clock the test advances by hand, so the offered rate fed to the estimator is exact
// and the EWMA trajectory is reproducible under -race. It harvests tasks:T283's
// scaffolding (commit d4654c7): the EWMA-aware generator, the k*LoadTau warm-up window
// and its exclusion from the settled-sample checks, and the self-verifying observed
// trough/peak checks. T283's saturation-dwell assertions are NOT harvested — that
// mechanism was dropped when decisions:K34 was superseded — and neither is its
// still-collapsed-at-the-close-of-warm-up assertion: with a frame-accurate estimator
// the gate INSTANT-engages the moment the smoothed wire rate clears EngageFraction*
// capacity, which happens inside the warm-up window by construction, so that particular
// T283 assertion cannot hold here. The starts-collapsed precondition below is its
// (weaker, but valid) analogue.

// Offered-load fixture constants. They are named rather than inlined so every
// expectation below is derived from them analytically (never from a magic literal).
const (
	// offeredLoadTau is the offered-load EWMA time constant the fixture configures.
	offeredLoadTau = 200 * time.Millisecond

	// offeredLoadCapacityFPS is the per-path capacity the fixture declares, in WIRE
	// frames per second. It matches test/e2e/p2_aggregation_test.go's
	// p2PerPathCapacityFPS so the unit fixture and the hardware fixture speak the same
	// number. Engage sits at 0.9*3000 = 2700 fps, disengage at 0.5*3000 = 1500 fps.
	offeredLoadCapacityFPS = 3000.0

	// offeredBatchBuffers (B) is the batch size the units assertion (1a) offers in ONE
	// Send. It is >= 4 so the frames-vs-batches discrepancy is unmistakable: the
	// frame-accurate estimator must move by B, the batch-counting one moves by 1.
	offeredBatchBuffers = 4

	// fecDataShards/fecParityShards (K/M) are the FEC geometry of the (1c) units
	// assertion: each Send of exactly K buffers fills exactly one group, so exactly M
	// parity frames egress on the SAME chosen path and must be metered as offered wire
	// frames too (decisions:K35 §3c).
	fecDataShards   = 4
	fecParityShards = 2

	// offeredWarmupTaus (k) is the EWMA warm-up window in units of LoadTau, harvested
	// from tasks:T283: at k=5 the estimator is within ~1% of the offered rate, so
	// samples inside k*LoadTau of the first Send are excluded from the settled-rate
	// self-checks.
	offeredWarmupTaus = 5
)

// weightedOfferedLoadCfg is the weighted-scheduler config the offered-load fixtures
// drive: the e2e aggregation fixture's capacity and hysteresis band, pacing OFF (the
// shipped default; the buckets are inert so nothing is shed and the offered meter is
// the only thing under test).
func weightedOfferedLoadCfg() sched.WeightedConfig {
	return sched.WeightedConfig{
		PerPathCapacity:   offeredLoadCapacityFPS,
		EngageFraction:    0.9,
		DisengageFraction: 0.5,
		CollapseDwell:     2 * time.Second,
		LoadTau:           offeredLoadTau,
		Pacing:            false,
		PacingBurst:       8,
		WeightRTTFloor:    time.Millisecond,
		WeightLossFloor:   1e-3,
	}
}

// newWeightedMultipath builds a single-peer Multipath whose scheduler is a
// WeightedScheduler over an INJECTED clock, so a test drives the offered-load estimator
// on fake time. fecCfg is nil for the FEC-off fixtures and non-nil for the parity ones.
// It returns the bind and the scheduler so the test can read AggregationSnapshot
// (offered load + gate state) without perturbing any per-frame distribution state.
func newWeightedMultipath(t testing.TB, paths []config.Path, psk config.Key, fecCfg *fec.Config, cfg sched.WeightedConfig, clk telemetry.Clock) (*Multipath, *sched.WeightedScheduler) {
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
	m, err := NewMultipath(paths, psk, ws, nil, nil, fecCfg, nil, config.Amnezia{}, lg)
	if err != nil {
		t.Fatalf("NewMultipath(weighted): %v", err)
	}
	return m, ws
}

// openWeightedToPeer opens m on an ephemeral port, points its single path at a fresh
// peer UDP socket, and starts a drainer so the sender never stalls on a full receive
// buffer. It returns nothing the caller must close — cleanup is registered on t.
func openWeightedToPeer(t testing.TB, m *Multipath) {
	t.Helper()
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	peer, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatalf("listen peer: %v", err)
	}
	done := make(chan struct{})
	go func() {
		buf := make([]byte, maxDatagram)
		for {
			select {
			case <-done:
				return
			default:
			}
			_ = peer.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
			if _, _, err := peer.ReadFromUDPAddrPort(buf); err != nil {
				select {
				case <-done:
					return
				default:
				}
			}
		}
	}()
	t.Cleanup(func() {
		close(done)
		_ = peer.Close()
	})
	m.paths[0].setRemote(peer.LocalAddr().(*net.UDPAddr).AddrPort())
}

// foldOfferedLoad replays observeLoadLocked's recurrence ANALYTICALLY over a sequence of
// per-Send offered frame counts spaced dt apart, so every expectation in this file is
// derived from the estimator's definition rather than a magic literal. The FIRST sample
// seeds loadRate = 0 and then adds frames/tau (no decay — there is no previous sample);
// every later sample decays by exp(-dt/tau) first. tau is LoadTau in seconds.
func foldOfferedLoad(frames []int, dt time.Duration, tau time.Duration) float64 {
	tauSec := tau.Seconds()
	rate := 0.0
	for i, n := range frames {
		if i > 0 {
			rate *= math.Exp(-dt.Seconds() / tauSec)
		}
		rate += float64(n) / tauSec
	}
	return rate
}

// closeEnough reports whether got is within relTol (relative) of want.
func closeEnough(got, want, relTol float64) bool {
	if want == 0 {
		return math.Abs(got) <= relTol
	}
	return math.Abs(got-want)/math.Abs(want) <= relTol
}

// TestSendMetersEveryBufferOfTheBatchAsOneOfferedFrame is T290 step (1a): the UNITS
// assertion, FEC OFF. ONE Send carrying a batch of B buffers offers the scheduler B
// WIRE frames, so it must move the smoothed offered-load estimate by exactly B frames —
// the analytic observeLoadLocked value, first-sample seed branch included — and a
// SECOND batched Send dt later must move it by B again after the exp(-dt/tau) decay.
//
// RED before the T290 fix: Multipath.Send calls Pick ONCE PER BATCH (multipath.go:2702,
// outside the `for _, b := range bufs` loop at :2728), so the estimator advances by ONE
// frame per Send no matter how many buffers the batch carried, and the observed value is
// B times too small.
func TestSendMetersEveryBufferOfTheBatchAsOneOfferedFrame(t *testing.T) {
	if offeredBatchBuffers < 4 {
		t.Fatalf("fixture error: offeredBatchBuffers = %d, want >= 4", offeredBatchBuffers)
	}
	cfg := weightedOfferedLoadCfg()
	psk := testKey(t, 0x95)
	clk := newFakeClock()
	m, ws := newWeightedMultipath(t, loopbackPaths(1), psk, nil, cfg, clk)
	openWeightedToPeer(t, m)

	if ws.AggregationSnapshot().OfferedLoadFPS != 0 {
		t.Fatalf("fixture error: offered load before the first Send = %g, want 0", ws.AggregationSnapshot().OfferedLoadFPS)
	}

	// Batch #1: the first-sample seed branch — loadRate is seeded 0 and then gains
	// B/tau, with no decay (there is no previous sample).
	if err := m.Send(payloadStream(offeredBatchBuffers), m.virt); err != nil {
		t.Fatalf("Send batch #1: %v", err)
	}
	wantOne := foldOfferedLoad([]int{offeredBatchBuffers}, 0, cfg.LoadTau)
	gotOne := ws.AggregationSnapshot().OfferedLoadFPS
	if !closeEnough(gotOne, wantOne, 1e-9) {
		t.Fatalf("after ONE Send of %d buffers offered load = %g fps, want %g fps "+
			"(D95: Send calls Pick once per BATCH, so the estimator counts batches/s, not wire frames/s — "+
			"it is reading the 1-frame value %g)",
			offeredBatchBuffers, gotOne, wantOne, foldOfferedLoad([]int{1}, 0, cfg.LoadTau))
	}

	// Batch #2 one dt later: the decay branch — the prior estimate decays by
	// exp(-dt/tau) and then gains another B/tau.
	const dt = 5 * time.Millisecond
	clk.advance(dt)
	if err := m.Send(payloadStream(offeredBatchBuffers), m.virt); err != nil {
		t.Fatalf("Send batch #2: %v", err)
	}
	wantTwo := foldOfferedLoad([]int{offeredBatchBuffers, offeredBatchBuffers}, dt, cfg.LoadTau)
	gotTwo := ws.AggregationSnapshot().OfferedLoadFPS
	if !closeEnough(gotTwo, wantTwo, 1e-9) {
		t.Fatalf("after TWO Sends of %d buffers (dt=%s) offered load = %g fps, want %g fps (analytic EWMA fold)",
			offeredBatchBuffers, dt, gotTwo, wantTwo)
	}
}

// D95-signature generator constants for (1b). The offered WIRE-frame rate sawtooths
// between these bounds while the BATCH rate — the quantity today's estimator actually
// measures — is that rate divided by signatureBatchBuffers.
const (
	// signatureBatchBuffers is the per-Send batch size. The o3 measurement (T284) found
	// a batch factor of 3.07 at saturation; 8 is the same order and makes the two
	// regimes unambiguous.
	signatureBatchBuffers = 8

	// signatureWireMinFPS/signatureWireMaxFPS bound the offered WIRE frame rate. Both
	// clear the engage threshold (0.9*3000 = 2700 fps) with margin, while the
	// corresponding BATCH rates (375..687.5 /s) sit far below the disengage threshold
	// (0.5*3000 = 1500 fps) — exactly D95's signature: a genuinely saturated path whose
	// batch-counted "offered load" reads as idle. The min/max ratio is 0.545, matching
	// the o3 honest-loss signature's trough/peak ~0.54 (reviews:R302), so the ramp is a
	// genuine AIMD swing rather than the ~1.3% ripple T283's shallower fixture produced.
	signatureWireMinFPS = 3000.0
	signatureWireMaxFPS = 5500.0

	// signatureSawtoothPeriod is the honest-loss AIMD ramp period: the offered wire rate
	// ramps linearly from min to max and drops sharply back. It is >= LoadTau so a
	// GENUINE dip is exercised (reviews:R302's non-blocking observation on T283, whose
	// 100 ms period against a 200 ms LoadTau smoothed to a ~1.3% ripple — far shallower
	// than the o3 signature's trough/peak ~0.54).
	signatureSawtoothPeriod = 250 * time.Millisecond

	// signaturePostWarmupPeriods is how many full sawtooth periods are driven AFTER the
	// warm-up window closes, so the settled-rate self-checks see several complete
	// ramp/drop cycles.
	signaturePostWarmupPeriods = 4
)

// signatureWireFPS is the (1b) generator: a linear additive-increase ramp from
// signatureWireMinFPS to signatureWireMaxFPS over signatureSawtoothPeriod followed by a
// sharp multiplicative-decrease drop, in OFFERED WIRE FRAMES per second. elapsed is
// measured from the first Send on the fake clock.
func signatureWireFPS(elapsed time.Duration) float64 {
	phase := elapsed % signatureSawtoothPeriod
	frac := float64(phase) / float64(signatureSawtoothPeriod)
	return signatureWireMinFPS + (signatureWireMaxFPS-signatureWireMinFPS)*frac
}

// TestBatchedOfferAboveEngageWireRateEngagesTheGate is T290 step (1b): the BEHAVIOURAL
// assertion, the D95 signature itself. A sustained batched offer whose WIRE-frame rate
// stays above EngageFraction*PerPathCapacity (2700 fps) while its BATCH rate stays below
// DisengageFraction*PerPathCapacity (1500 fps) must ENGAGE the aggregation gate: the
// path is genuinely saturated, and the gate's denominator is a wire-frame capacity.
//
// RED before the T290 fix: the estimator sees ~375-450 batches/s, never approaches the
// 2700 fps engage threshold, and the gate stays collapsed forever — the exact o3
// observation behind D95 (wanbond_aggregation_engaged pinned at 0 while the path carried
// 3300-3530 wire fps).
//
// Self-verification. The generator's LEGALITY is checked analytically up front (a
// regime-independent property of the constants), and the observed settled trough/peak of
// the smoothed estimate is captured and reported in the failure message — so the RED run
// itself prints which unit the estimator is speaking. The settled band is asserted
// against the wire rate only AFTER the gate assertion passes, because before the fix the
// estimate is legitimately in the other unit and a band check there would fail as a
// fixture error rather than as the defect.
func TestBatchedOfferAboveEngageWireRateEngagesTheGate(t *testing.T) {
	cfg := weightedOfferedLoadCfg()
	engage := cfg.EngageFraction * cfg.PerPathCapacity
	disengage := cfg.DisengageFraction * cfg.PerPathCapacity

	// Fixture legality, from the constants alone.
	if signatureWireMinFPS <= engage {
		t.Fatalf("fixture error: min offered WIRE rate %g must exceed the engage threshold %g", signatureWireMinFPS, engage)
	}
	if signatureWireMaxFPS/signatureBatchBuffers >= disengage {
		t.Fatalf("fixture error: max offered BATCH rate %g must stay below the disengage threshold %g (the D95 signature requires it)",
			signatureWireMaxFPS/signatureBatchBuffers, disengage)
	}
	if signatureSawtoothPeriod < cfg.LoadTau {
		t.Fatalf("fixture error: sawtooth period %s must be >= LoadTau %s so a genuine dip is exercised (R302)", signatureSawtoothPeriod, cfg.LoadTau)
	}

	psk := testKey(t, 0x96)
	clk := newFakeClock()
	m, ws := newWeightedMultipath(t, loopbackPaths(1), psk, nil, cfg, clk)
	openWeightedToPeer(t, m)

	if ws.AggregationSnapshot().Aggregating {
		t.Fatal("fixture error: gate already engaged before any Send, want collapsed")
	}

	warmup := offeredWarmupTaus * cfg.LoadTau
	driveSpan := warmup + signaturePostWarmupPeriods*signatureSawtoothPeriod
	batch := payloadStream(signatureBatchBuffers)

	var elapsed time.Duration
	haveSettled := false
	var settledMin, settledMax float64
	for elapsed < driveSpan {
		wire := signatureWireFPS(elapsed)
		// One Send offers signatureBatchBuffers wire frames, so the inter-Send interval
		// that realises `wire` frames/s is signatureBatchBuffers/wire seconds.
		dt := time.Duration(float64(time.Second) * signatureBatchBuffers / wire)
		if err := m.Send(batch, m.virt); err != nil {
			t.Fatalf("Send at elapsed=%s: %v", elapsed, err)
		}
		if elapsed >= warmup {
			load := ws.AggregationSnapshot().OfferedLoadFPS
			if !haveSettled {
				settledMin, settledMax, haveSettled = load, load, true
			}
			if load < settledMin {
				settledMin = load
			}
			if load > settledMax {
				settledMax = load
			}
		}
		clk.advance(dt)
		elapsed += dt
	}
	if !haveSettled {
		t.Fatal("fixture error: no settled (post-warm-up) samples collected")
	}

	snap := ws.AggregationSnapshot()
	if !snap.Aggregating {
		t.Fatalf("gate stayed COLLAPSED after %s of batched offer at %g-%g WIRE fps (batches of %d, i.e. %g-%g batches/s), "+
			"want ENGAGED. Settled smoothed offered load was [%.1f,%.1f] fps against engage %g / disengage %g — "+
			"that band is the BATCH rate, not the wire rate: D95's frames-vs-batches units defect (K35 §2)",
			driveSpan-warmup, signatureWireMinFPS, signatureWireMaxFPS, signatureBatchBuffers,
			signatureWireMinFPS/signatureBatchBuffers, signatureWireMaxFPS/signatureBatchBuffers,
			settledMin, settledMax, engage, disengage)
	}

	// Post-engage self-verification: the gate must have engaged because the estimator
	// measured the WIRE rate, not for some unrelated reason. The settled band must sit
	// inside the offered wire band (with a small allowance for the EWMA's per-event
	// quantisation) and above the engage threshold throughout.
	const bandTol = 0.05
	if settledMin < engage {
		t.Fatalf("settled min smoothed offered load = %.1f fps, want >= engage threshold %g (the gate engaged, but the estimate is not tracking the wire rate)", settledMin, engage)
	}
	if settledMin < signatureWireMinFPS*(1-bandTol) || settledMax > signatureWireMaxFPS*(1+bandTol) {
		t.Fatalf("settled smoothed offered load [%.1f,%.1f] fps is outside the offered wire band [%g,%g] +/- %.0f%%",
			settledMin, settledMax, signatureWireMinFPS, signatureWireMaxFPS, bandTol*100)
	}
	if settledMax <= settledMin {
		t.Fatalf("settled band [%.1f,%.1f] shows no ripple: the sawtooth dip was not exercised", settledMin, settledMax)
	}
	t.Logf("settled smoothed offered load [%.1f,%.1f] fps (trough/peak %.3f) against engage %g, disengage %g; batch rate %g-%g /s",
		settledMin, settledMax, settledMin/settledMax, engage, disengage,
		signatureWireMinFPS/signatureBatchBuffers, signatureWireMaxFPS/signatureBatchBuffers)
}

// TestSendMetersFECParityAsOfferedWireFrames is T290 step (1c): the FEC-PARITY UNITS
// assertion. With FEC enabled at K data + M parity, a Send of exactly K buffers fills
// exactly one group, so K data frames AND M parity frames egress on the SAME
// scheduler-chosen path and consume that path's WIRE capacity. The offered-load estimate
// must therefore move by K+M wire frames per Send, not by K (and certainly not by 1) —
// otherwise the gate's numerator is a DEMAND rate over a WIRE-frame denominator and, at
// 4+2, a fully saturated ~3400 fps path meters only ~2267 fps, below engage 2700:
// D95's failure mode restored for every FEC-enabled deployment (decisions:K35 §3c).
//
// Parity is metered through the ONE-BATCH-LATE carry (K35 §3c): the group's parity is
// not known until the encoder's Admit runs INSIDE the per-buffer loop, after Pick has
// already stamped the chosen path into every frame, so the parity that actually reached
// the socket is counted by the NEXT Send. The expected frame sequence over R Sends of K
// buffers is therefore [K, K+M, K+M, ...] — the first Send carries no inherited parity.
//
// RED before the T290 fix: the estimator advances by exactly 1 per Send regardless of K
// or M.
func TestSendMetersFECParityAsOfferedWireFrames(t *testing.T) {
	cfg := weightedOfferedLoadCfg()
	psk := testKey(t, 0x97)
	clk := newFakeClock()
	fecCfg := &fec.Config{DataShards: fecDataShards, ParityShards: fecParityShards, Deadline: testFECDeadline}
	m, ws := newWeightedMultipath(t, loopbackPaths(1), psk, fecCfg, cfg, clk)
	openWeightedToPeer(t, m)

	const (
		sends = 6
		dt    = 5 * time.Millisecond
	)
	batch := payloadStream(fecDataShards)
	for i := 0; i < sends; i++ {
		if i > 0 {
			clk.advance(dt)
		}
		if err := m.Send(batch, m.virt); err != nil {
			t.Fatalf("Send #%d: %v", i, err)
		}
	}

	// Analytic expectation: [K, K+M, K+M, ...] folded through observeLoadLocked.
	want := make([]int, sends)
	for i := range want {
		want[i] = fecDataShards + fecParityShards
	}
	want[0] = fecDataShards // no parity has egressed yet when the first Send picks
	wantFPS := foldOfferedLoad(want, dt, cfg.LoadTau)

	dataOnly := make([]int, sends)
	for i := range dataOnly {
		dataOnly[i] = fecDataShards
	}
	batchOnly := make([]int, sends)
	for i := range batchOnly {
		batchOnly[i] = 1
	}

	got := ws.AggregationSnapshot().OfferedLoadFPS
	if !closeEnough(got, wantFPS, 1e-9) {
		t.Fatalf("after %d Sends of %d buffers with FEC %d+%d, offered load = %g fps, want %g fps "+
			"(data+parity wire frames). Data-alone would read %g fps and batch-counting %g fps — "+
			"the observed value must be the WIRE-frame one, in the same unit as PerPathCapacity (K35 §3c)",
			sends, fecDataShards, fecDataShards, fecParityShards, got, wantFPS,
			foldOfferedLoad(dataOnly, dt, cfg.LoadTau), foldOfferedLoad(batchOnly, dt, cfg.LoadTau))
	}

	// Cross-check against the frames the datapath actually wrote to the socket: the
	// scheduler must have been offered every one of them except the still-pending
	// last-group carry (M frames, consumed by the next Send).
	fs := m.fecSend.Load()
	if fs == nil {
		t.Fatal("fixture error: FEC sender is nil with FEC configured")
	}
	wroteData := fs.dataFrames.Load()
	wroteParity := fs.parityFrames.Load()
	if wroteData != uint64(sends*fecDataShards) || wroteParity != uint64(sends*fecParityShards) {
		t.Fatalf("wrote %d data / %d parity frames, want %d / %d (fixture: each Send fills exactly one group)",
			wroteData, wroteParity, sends*fecDataShards, sends*fecParityShards)
	}
	var offered int
	for _, n := range want {
		offered += n
	}
	if uint64(offered)+uint64(fecParityShards) != wroteData+wroteParity {
		t.Fatalf("offered frame total %d + pending carry %d != frames written %d (carry dropped or double-counted)",
			offered, fecParityShards, wroteData+wroteParity)
	}
}
