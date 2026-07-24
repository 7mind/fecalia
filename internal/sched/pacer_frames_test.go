package sched

import (
	"math"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/telemetry"
)

// --- T291 step 4: charge the pacing token bucket per offered frame, not per batch -----
//
// PerPathCapacity/CapacityFPS is ONE constant serving both the aggregation gate's
// denominator (defect D95, tasks:T290) and the token-bucket refill rate (pacer.go:86).
// Before this task tryConsume spent exactly ONE token per admitted Pick regardless of
// how many wire frames `frames` said that Pick covered, so the two uses of the same
// field disagreed on units. These tests pin pacer.tryConsume(idx, frames): the ADMISSION
// predicate stays "bucket holds >= 1 token" (unchanged, still batch-atomic — the bind
// has no per-buffer shed path), but the CHARGE is now `frames` tokens, allowing the
// bucket to go negative exactly as accountProbe already does.
//
// (i) pacing OFF — a batched Pick touches no bucket. (ii) pacing ON — a batch of N
// admitted frames leaves the bucket exactly N lower. (iii) OVERSHOOT — a batch larger
// than the remaining tokens is still admitted and drives the balance negative,
// subsequent batches shed until refill catches up, and the long-run admitted frame
// rate converges to CapacityFPS (plus a comparison against the pre-change per-batch
// charge, which would have admitted ~batch-factor more). (iv) ClassControl spends
// nothing, even for a multi-frame batch. (v) AccountProbe (T145) is unchanged by this
// seam — it still charges exactly one token per call, independent of `frames`.

// --- (i) pacing OFF: batched Pick touches no bucket ------------------------------------

// TestPacingOffBatchedPickTouchesNoBucket is test (i): with pacing disabled, Pick(class,
// frames) for a large, varying `frames` never perturbs the token bucket — tryConsume
// returns true unconditionally without reading or writing p.tokens (pacer.go). A fresh
// bucket is seeded to PacingBurst at construction and refill (still run unconditionally
// as an accountant, per its doc comment) can only cap it back at PacingBurst, so the
// bucket must read PacingBurst before, during and after a run of oversized batches.
func TestPacingOffBatchedPickTouchesNoBucket(t *testing.T) {
	clock := newFakeClock()
	primary := &fakeQuality{state: telemetry.StateUp, est: telemetry.Estimate{RTT: 10 * time.Millisecond}}
	cfg := weightedCfg() // Pacing == false
	cfg.PacingBurst = 8
	s := newWeighted(t, clock, cfg, primary)

	if got := s.tokens[0]; got != cfg.PacingBurst {
		t.Fatalf("fresh bucket = %g, want burst %g", got, cfg.PacingBurst)
	}

	const dt = 10 * time.Millisecond
	for i, frames := range []int{1, 3, 50, 200, 1} {
		idx := s.Pick(ClassData, frames)
		if idx != 0 {
			t.Fatalf("pick %d: Pick(ClassData, %d) = %d, want 0 (pacing off never sheds)", i, frames, idx)
		}
		if got := s.tokens[0]; got != cfg.PacingBurst {
			t.Fatalf("pick %d: bucket = %g after Pick(_, %d) with pacing OFF, want unchanged burst %g (frames must not touch the bucket)", i, got, frames, cfg.PacingBurst)
		}
		clock.advance(dt)
	}
}

// --- (ii) pacing ON: a batch of N frames leaves the bucket exactly N lower -------------

// TestPacingOnBatchChargesExactlyNTokens is test (ii): the FIRST Pick on a fresh
// scheduler seeds the bucket to PacingBurst (the initial refill) and then, since the
// predicate only requires >= 1 token and the seeded bucket comfortably clears it, admits
// the batch and deducts exactly `frames` tokens — not one.
func TestPacingOnBatchChargesExactlyNTokens(t *testing.T) {
	clock := newFakeClock()
	primary := &fakeQuality{state: telemetry.StateUp, est: telemetry.Estimate{RTT: 10 * time.Millisecond}}
	cfg := weightedCfg()
	cfg.Pacing = true
	cfg.PacingBurst = 1000
	s := newWeighted(t, clock, cfg, primary)

	const frames = 37
	idx := s.Pick(ClassData, frames)
	if idx != 0 {
		t.Fatalf("Pick(ClassData, %d) = %d, want 0 (admitted: burst %g comfortably clears the >= 1 predicate)", frames, idx, cfg.PacingBurst)
	}
	want := cfg.PacingBurst - float64(frames)
	if got := s.tokens[0]; got != want {
		t.Fatalf("bucket after one %d-frame batch = %g, want %g (burst %g - frames %d): the batch must charge frames tokens, not one", frames, got, want, cfg.PacingBurst, frames)
	}

	// A second batch of a different size confirms the charge tracks `frames` each call,
	// not a fixed amount.
	const frames2 = 121
	idx2 := s.Pick(ClassData, frames2)
	if idx2 != 0 {
		t.Fatalf("second Pick(ClassData, %d) = %d, want 0", frames2, idx2)
	}
	want2 := want - float64(frames2)
	if got := s.tokens[0]; got != want2 {
		t.Fatalf("bucket after second %d-frame batch = %g, want %g", frames2, got, want2)
	}
}

// --- (iii) OVERSHOOT: admitted-but-negative, shed-until-refill, long-run convergence --

// TestPacingOvershootAdmitsThenDrivesBalanceNegative is test (iii)'s first half: a batch
// LARGER than the remaining tokens is still ADMITTED (the predicate only asks for
// >= 1, exactly like accountProbe's precedent), and the deduction of the full `frames`
// count drives the balance negative. A frame offered immediately afterward (no elapsed
// time, so no refill) then sheds, and stays shed until enough real time has passed for
// refill to bring the bucket back to >= 1 — at which point admission resumes.
func TestPacingOvershootAdmitsThenDrivesBalanceNegative(t *testing.T) {
	logger := discardLogger(t)
	cfg := pacerConfig{Pacing: true, CapacityFPS: 100, BurstFrames: 5}
	p := newPacer(1, cfg, logger)

	t0 := time.Unix(1_700_000_000, 0)
	p.refill(t0) // first call seeds every bucket to BurstFrames (5)
	if got := p.tokens[0]; got != 5 {
		t.Fatalf("seeded bucket = %g, want burst 5", got)
	}

	// A 50-frame batch against a 5-token bucket: predicate (5 >= 1) admits, then
	// deducts the full 50, landing at 5 - 50 = -45.
	if ok := p.tryConsume(0, 50); !ok {
		t.Fatal("a batch larger than the bucket must still be ADMITTED (predicate is >= 1 token, unchanged by T291)")
	}
	if got, want := p.tokens[0], -45.0; got != want {
		t.Fatalf("bucket after the oversized batch = %g, want %g (charge the full 50, allowed to go negative)", got, want)
	}

	// Immediately afterward (no elapsed time, no refill), a single-frame batch must shed:
	// the balance is deeply negative, well below the >= 1 admission floor.
	p.refill(t0) // dt == 0: no-op refill
	if ok := p.tryConsume(0, 1); ok {
		t.Fatal("a frame offered against a negative balance with no elapsed refill must be SHED, not admitted")
	}
	if got, want := p.tokens[0], -45.0; got != want {
		t.Fatalf("a shed call must not touch the bucket: got %g, want unchanged %g", got, want)
	}

	// Advance real time until refill brings the bucket back to >= 1 (needs 46 tokens at
	// 100 fps = 460ms), then confirm admission resumes and the charge still lands.
	t1 := t0.Add(460 * time.Millisecond)
	p.refill(t1)
	if got := p.tokens[0]; got < 1 {
		t.Fatalf("bucket after 460ms of refill at 100fps = %g, want >= 1 (refill must have caught up)", got)
	}
	beforeConsume := p.tokens[0]
	if ok := p.tryConsume(0, 1); !ok {
		t.Fatal("admission must resume once refill has brought the bucket back to >= 1")
	}
	if got, want := p.tokens[0], beforeConsume-1; got != want {
		t.Fatalf("bucket after resumed admission = %g, want %g", got, want)
	}
}

// oldTryConsume reimplements the PRE-T291 admission rule for comparison only: it kept
// the same >= 1 admission predicate but charged exactly ONE token per admitted call
// regardless of how many wire frames the call covered (pacer.go's tryConsume before this
// task; production code no longer contains this form; charge is one). It exists solely so
// TestPacingLongRunAdmittedRateConvergesToCapacity can quantify the batch-factor
// over-admission tasks:T291 fixes, side by side with the real (post-fix) pacer.
func oldTryConsume(tokens *float64) bool {
	if *tokens >= 1 {
		*tokens--
		return true
	}
	return false
}

// TestPacingLongRunAdmittedRateConvergesToCapacity is test (iii)'s second half — the
// operational proof that charging `frames` tokens per batch (not one) binds the LONG-RUN
// admitted rate to CapacityFPS, and the direct regression test for the pre-fix ~1.5-3x
// over-admission (decisions:K35 §3e, defects:D95).
//
// A sustained offer of fixed-size batches at a batch rate whose WIRE-frame rate exceeds
// CapacityFPS by battFactor (2x here, inside the 1.52-3.07 range measured pre-fix)
// exercises the token bucket's steady-state admit/shed cycling: once the bucket's
// balance goes negative on an oversized admission, it sheds every batch until refill
// alone (capacity*dt per step) brings it back to >= 1, at which point it admits once
// more and repeats. Averaged over a long enough fake-clock window, that cycling makes
// the ADMITTED frame rate converge to CapacityFPS (every refilled token is eventually
// consumed by exactly one admission, in the limit) regardless of the batch size — this
// is the classic token-bucket policing property, and it is what "the unit is now
// consistent end to end" means operationally.
//
// The pre-fix (`oldTryConsume`) charge of ONE token per admitted call, by contrast, never
// drives the bucket negative fast enough to shed at all here (refill outruns a 1-token
// charge even under 2x offered load), so it admits EVERY batch and the admitted frame
// rate equals the full OFFERED rate — i.e. ~battFactor times CapacityFPS, reproducing the
// over-admission this task fixes.
func TestPacingLongRunAdmittedRateConvergesToCapacity(t *testing.T) {
	const (
		capacityFPS = 2000.0 // CapacityFPS: refill rate, frames/s
		burstFrames = 200.0  // BurstFrames: bucket size, frames
		frames      = 20     // wire frames per offered batch
		dt          = 5 * time.Millisecond
		battFactor  = 2.0              // offered wire-frame rate = battFactor * capacityFPS
		window      = 20 * time.Second // fake-clock measurement window
		tolerance   = 0.03             // 3% relative tolerance on the converged rate
	)
	// Sanity: the fixture must actually offer at battFactor*capacityFPS wire fps.
	batchesPerSec := 1.0 / dt.Seconds()
	offeredFPS := float64(frames) * batchesPerSec
	if math.Abs(offeredFPS-battFactor*capacityFPS)/(battFactor*capacityFPS) > 1e-9 {
		t.Fatalf("fixture misconfigured: offered %v fps, want exactly %vx capacity (%v fps)", offeredFPS, battFactor, battFactor*capacityFPS)
	}

	logger := discardLogger(t)
	cfg := pacerConfig{Pacing: true, CapacityFPS: capacityFPS, BurstFrames: burstFrames}
	newP := newPacer(1, cfg, logger)

	now := time.Unix(1_700_000_000, 0)
	steps := int(window / dt)

	var newAdmittedFrames float64
	// oldTokens mirrors a bucket charged the pre-T291 way: same refill, same >= 1
	// predicate, but a flat 1-token deduction per admitted call (oldTryConsume).
	oldTokens := burstFrames
	var oldAdmittedFrames float64

	for i := 0; i < steps; i++ {
		now = now.Add(dt)
		newP.refill(now)
		if newP.tryConsume(0, frames) {
			newAdmittedFrames += frames
		}

		// old model: same refill law, applied to its own token counter.
		oldTokens += capacityFPS * dt.Seconds()
		if oldTokens > burstFrames {
			oldTokens = burstFrames
		}
		if oldTryConsume(&oldTokens) {
			oldAdmittedFrames += frames
		}
	}

	newRateFPS := newAdmittedFrames / window.Seconds()
	oldRateFPS := oldAdmittedFrames / window.Seconds()

	relErr := math.Abs(newRateFPS-capacityFPS) / capacityFPS
	if relErr > tolerance {
		t.Fatalf("post-fix (frames-charged) long-run admitted rate = %.1f fps over %v, want within %.0f%% of CapacityFPS=%.0f (relative error %.3f)",
			newRateFPS, window, tolerance*100, capacityFPS, relErr)
	}

	// The pre-fix model must reproduce the documented over-admission: it should land
	// close to the full OFFERED rate (battFactor*capacityFPS), not the declared capacity.
	wantOldFPS := battFactor * capacityFPS
	oldRelErr := math.Abs(oldRateFPS-wantOldFPS) / wantOldFPS
	if oldRelErr > tolerance {
		t.Fatalf("pre-fix (one-token-per-batch) long-run admitted rate = %.1f fps, want within %.0f%% of the offered rate %.0f fps (battFactor=%v) — the reproduction of the pre-fix over-admission is not exercising the fixture as intended (relative error %.3f)",
			oldRateFPS, tolerance*100, wantOldFPS, battFactor, oldRelErr)
	}
	// Non-vacuous: the fix must actually have moved the admitted rate down by
	// approximately battFactor relative to the pre-fix model.
	ratio := oldRateFPS / newRateFPS
	if ratio < battFactor*0.9 || ratio > battFactor*1.1 {
		t.Fatalf("pre-fix/post-fix admitted-rate ratio = %.3f, want ~%.1f (the batch factor this task removes): old=%.1f fps new=%.1f fps", ratio, battFactor, oldRateFPS, newRateFPS)
	}
}

// --- (iv) ClassControl spends nothing, even for a multi-frame batch -------------------

// TestPacingControlFrameSpendsNothingEvenForLargeBatch is test (iv): ClassControl stays
// pacing-EXEMPT and UNCHARGED (defect D22) under T291's per-frame charge exactly as it
// was under the old per-batch charge — serveLocked/selectAggregatingLocked short-circuit
// on class == ClassControl BEFORE calling tryConsume, so the bucket is never touched
// regardless of how large `frames` is.
func TestPacingControlFrameSpendsNothingEvenForLargeBatch(t *testing.T) {
	clock := newFakeClock()
	primary := &fakeQuality{state: telemetry.StateUp, est: telemetry.Estimate{RTT: 10 * time.Millisecond}}
	backup := &fakeQuality{state: telemetry.StateUp, est: telemetry.Estimate{RTT: 10 * time.Millisecond}}
	cfg := weightedCfg()
	cfg.Pacing = true
	cfg.PacingBurst = 4
	s := newWeighted(t, clock, cfg, primary, backup)

	const dt = 100 * time.Microsecond
	driveUntilAggregating(t, s, clock, dt)

	// driveUntilAggregating advances the clock once more right after the Pick that
	// flips the gate (it checks s.aggregating AFTER clock.advance), so the very next
	// Pick sees a pending refill gap unrelated to what this test measures. One
	// warm-up Pick at the current clock time settles that gap (refill's lastFill
	// catches up to "now") before the snapshot, so every further Pick below runs at
	// dt == 0 and any token movement can only come from the frames charge.
	s.Pick(ClassControl, 1)
	before := append([]float64(nil), s.tokens...)

	const bigFrames = 500 // far larger than any bucket
	for i := 0; i < 20; i++ {
		idx := s.Pick(ClassControl, bigFrames)
		if idx == PickPaced || idx == PickNone {
			t.Fatalf("control frame with frames=%d shed/outaged: Pick = %d, want a healthy path index", bigFrames, idx)
		}
	}

	for i, want := range before {
		if got := s.tokens[i]; got != want {
			t.Fatalf("bucket[%d] moved from %g to %g after control-only Picks with frames=%d: ClassControl must spend nothing regardless of frames", i, want, got, bigFrames)
		}
	}
}

// --- (v) AccountProbe (T145) is unchanged by the T291 per-frame charge ----------------

// TestAccountProbeUnaffectedByPerFrameCharge is test (v): AccountProbe has no `frames`
// parameter and is not part of the tryConsume seam this task changes — it still deducts
// EXACTLY one token per call (T145's contract), independent of whatever `frames` value
// concurrent Pick calls on OTHER paths are charging. This guards against a shared
// implementation detail (e.g. a helper refactored to always charge "the current batch's
// frames") accidentally coupling AccountProbe's one-token contract to Pick's frames.
func TestAccountProbeUnaffectedByPerFrameCharge(t *testing.T) {
	clock := newFakeClock()
	primary := &fakeQuality{state: telemetry.StateUp, est: telemetry.Estimate{RTT: 10 * time.Millisecond}}
	cfg := weightedCfg()
	cfg.Pacing = true
	cfg.PacingBurst = 1000
	s := newWeighted(t, clock, cfg, primary)

	// Charge a large, multi-frame ClassData batch first, so the bucket is not at a
	// round/fresh value when AccountProbe runs (guards against a coincidental match).
	s.Pick(ClassData, 137)
	before := s.tokens[0]

	const probes = 5
	for i := 0; i < probes; i++ {
		s.AccountProbe(0)
	}
	want := before - float64(probes)
	if got := s.tokens[0]; got != want {
		t.Fatalf("bucket after %d AccountProbe calls = %g, want %g (exactly one token per probe, unaffected by the preceding 137-frame Pick)", probes, got, want)
	}
}
