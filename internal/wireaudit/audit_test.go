package wireaudit

import (
	"math/rand"
	"testing"
)

// randFrames returns n frames of length frameLen filled from rng (uniform random,
// the statistical stand-in for XChaCha20 keystream output).
func randFrames(rng *rand.Rand, n, frameLen int) []Frame {
	out := make([]Frame, n)
	for i := range out {
		f := make([]byte, frameLen)
		for j := range f {
			f[j] = byte(rng.Intn(256))
		}
		out[i] = f
	}
	return out
}

// randSessions builds sessionCount sessions of framesPerSession random frames of a
// fixed length. Each session draws from an independent seed so it stands in for a
// fresh tunnel session.
func randSessions(sessionCount, framesPerSession, frameLen int) [][]Frame {
	sessions := make([][]Frame, sessionCount)
	for s := range sessions {
		rng := rand.New(rand.NewSource(int64(1000 + s)))
		sessions[s] = randFrames(rng, framesPerSession, frameLen)
	}
	return sessions
}

// TestRandomWireHasNoConstantOffset is the positive baseline: a set of random
// frames across >= MinSessions sessions must exhibit no constant byte position and
// pass the entropy check — the property the real obfuscated wire is designed to have.
func TestRandomWireHasNoConstantOffset(t *testing.T) {
	sessions := randSessions(MinSessions, 200, 1200)
	rep := Audit(sessions)

	if ok, msg := rep.SessionsOK(); !ok {
		t.Fatalf("SessionsOK: %s", msg)
	}
	if ok, msg := rep.ConstantByteOK(); !ok {
		t.Fatalf("ConstantByteOK on random wire: %s", msg)
	}
	if ok, msg := rep.OffsetDistributionOK(); !ok {
		t.Fatalf("OffsetDistributionOK on random wire: %s", msg)
	}
	if ok, msg := rep.EntropyOK(); !ok {
		t.Fatalf("EntropyOK on random wire: %s", msg)
	}
	if ok, msg := rep.CoverageOK(1024); !ok {
		t.Fatalf("CoverageOK on random wire: %s", msg)
	}
	if ok, _ := rep.OK(); !ok {
		t.Fatalf("random wire failed the combined audit")
	}
	// Sanity: a 1200-byte uniform-random payload's empirical entropy sits well above
	// the threshold but strictly below the 8.0 ceiling.
	if rep.MeanEntropy <= MeanEntropyThreshold || rep.MeanEntropy >= entropyMaxBitsPerByte {
		t.Fatalf("mean entropy %.4f not in (%.2f, %.1f)", rep.MeanEntropy, MeanEntropyThreshold, entropyMaxBitsPerByte)
	}
	// Every well-sampled offset must clear the per-offset distribution threshold with
	// margin: 200*MinSessions = 1000 samples/offset => bias ~0.18 bits => ~7.82.
	if len(rep.LowEntropyOffsets) != 0 {
		t.Fatalf("random wire has %d low-entropy offsets: %v", len(rep.LowEntropyOffsets), rep.LowEntropyOffsets)
	}
	if rep.HighestJudgedOffset != rep.MaxFrameLen-1 {
		t.Fatalf("highest judged offset %d, want %d (all fixed-length offsets should be judged)", rep.HighestJudgedOffset, rep.MaxFrameLen-1)
	}
}

// TestPlantedConstantByteIsDetectedAndPinpointed is the TEETH of the constant-byte
// detector: a single byte forced to a fixed value at a known offset across every
// frame of every session must make the audit FAIL and REPORT that exact offset and
// value. Without this proof the detector could be vacuous.
func TestPlantedConstantByteIsDetectedAndPinpointed(t *testing.T) {
	const plantOffset = 137
	const plantValue = 0x42

	sessions := randSessions(MinSessions, 200, 1200)
	for _, sess := range sessions {
		for _, f := range sess {
			f[plantOffset] = plantValue
		}
	}

	rep := Audit(sessions)
	ok, msg := rep.ConstantByteOK()
	if ok {
		t.Fatalf("planted constant at offset %d NOT detected — detector is vacuous", plantOffset)
	}
	if len(rep.ConstantOffsets) != 1 {
		t.Fatalf("expected exactly 1 constant offset, got %d: %v", len(rep.ConstantOffsets), rep.ConstantOffsets)
	}
	got := rep.ConstantOffsets[0]
	if got.Offset != plantOffset {
		t.Errorf("reported offset %d, want %d", got.Offset, plantOffset)
	}
	if got.Value != plantValue {
		t.Errorf("reported value 0x%02x, want 0x%02x", got.Value, plantValue)
	}
	if got.Sessions != MinSessions {
		t.Errorf("reported %d sessions, want %d", got.Sessions, MinSessions)
	}
	// The failure message must pinpoint the offset for the operator.
	if !contains(msg, "offset 137") {
		t.Errorf("failure message does not pinpoint the offset: %q", msg)
	}
}

// TestEntropyCheckHasTeeth proves the entropy check is non-vacuous: a set of large
// LOW-entropy payloads (a repeating 4-symbol pattern, ~2 bits/byte) must fail the
// entropy assertion, while large random payloads pass it.
func TestEntropyCheckHasTeeth(t *testing.T) {
	// Low-entropy large frames: a 4-symbol cycle => 2 bits/byte, far below 7.5.
	lowSessions := make([][]Frame, MinSessions)
	for s := range lowSessions {
		frames := make([]Frame, 200)
		for i := range frames {
			f := make([]byte, 1200)
			for j := range f {
				f[j] = byte(j % 4)
			}
			frames[i] = f
		}
		lowSessions[s] = frames
	}
	repLow := Audit(lowSessions)
	if ok, msg := repLow.EntropyOK(); ok {
		t.Fatalf("low-entropy wire PASSED the entropy check — vacuous; %s", msg)
	}
	if repLow.MeanEntropy > 3.0 {
		t.Fatalf("4-symbol pattern measured %.4f bits/byte, expected ~2", repLow.MeanEntropy)
	}

	// Random large frames: must pass.
	repHigh := Audit(randSessions(MinSessions, 200, 1200))
	if ok, msg := repHigh.EntropyOK(); !ok {
		t.Fatalf("random wire FAILED the entropy check: %s", msg)
	}
}

// TestUnderSampledConstantOffsetNotFlagged verifies the variable-length min-sample
// rule in BOTH directions: an offset reached by only a FEW frames (< MinSamplesPerOffset)
// is NOT flagged even though it is constant there, while a WELL-sampled constant
// offset at the same time IS flagged. This proves the rule neither falsely clears a
// real fingerprint nor falsely flags a sparsely-present offset.
func TestUnderSampledConstantOffsetNotFlagged(t *testing.T) {
	const wellSampledOffset = 5 // present in every (short and long) frame
	const tailOffset = 1500     // present only in the few long frames
	const wellSampledValue = 0xAB

	rng := rand.New(rand.NewSource(7))
	sessions := make([][]Frame, MinSessions)
	for s := range sessions {
		// Many short frames (length 100) — plenty of samples for offset 5.
		frames := randFrames(rng, 100, 100)
		// A HANDFUL of long frames (length 2000) — the ONLY ones reaching tailOffset.
		// Fewer than MinSamplesPerOffset across the whole capture.
		long := randFrames(rng, 3, 2000)
		for _, f := range long {
			f[tailOffset] = 0x77 // constant, but under-sampled
		}
		frames = append(frames, long...)
		// Plant a WELL-sampled constant at offset 5 in every frame.
		for _, f := range frames {
			f[wellSampledOffset] = wellSampledValue
		}
		sessions[s] = frames
	}

	rep := Audit(sessions)

	// The under-sampled tail offset must NOT be flagged (3*MinSessions = 15 < 32).
	for _, c := range rep.ConstantOffsets {
		if c.Offset == tailOffset {
			t.Errorf("under-sampled offset %d flagged constant (%d samples) — min-sample rule failed", tailOffset, c.Samples)
		}
	}
	// The well-sampled constant MUST be flagged.
	found := false
	for _, c := range rep.ConstantOffsets {
		if c.Offset == wellSampledOffset {
			found = true
			if c.Value != wellSampledValue {
				t.Errorf("offset %d value 0x%02x, want 0x%02x", wellSampledOffset, c.Value, wellSampledValue)
			}
		}
	}
	if !found {
		t.Errorf("well-sampled constant at offset %d NOT flagged", wellSampledOffset)
	}
}

// TestSingleSessionConstantNotFlagged proves the cross-session requirement: a byte
// held constant across MANY frames of a SINGLE session, at an offset no other
// session reaches, must NOT be flagged (Sessions=1 < MinSessionsPerOffset) — it is
// not a cross-session fingerprint.
func TestSingleSessionConstantNotFlagged(t *testing.T) {
	const tailOffset = 1500

	rng := rand.New(rand.NewSource(11))
	sessions := make([][]Frame, MinSessions)
	// Session 0: many long frames, all constant at tailOffset — > MinSamplesPerOffset
	// samples but from ONE session only.
	s0 := randFrames(rng, 100, 2000)
	for _, f := range s0 {
		f[tailOffset] = 0x55
	}
	sessions[0] = s0
	// Other sessions: only short frames, never reaching tailOffset.
	for s := 1; s < MinSessions; s++ {
		sessions[s] = randFrames(rng, 100, 100)
	}

	rep := Audit(sessions)
	for _, c := range rep.ConstantOffsets {
		if c.Offset == tailOffset {
			t.Errorf("single-session constant at offset %d flagged (samples=%d sessions=%d) — cross-session rule failed",
				tailOffset, c.Samples, c.Sessions)
		}
	}
}

// TestWithinSessionVaryingAcrossSessionsNotFlagged proves that a byte constant
// WITHIN each session but DIFFERENT across sessions is not a fingerprint: pooled,
// the offset varies, so it must not be flagged.
func TestWithinSessionVaryingAcrossSessionsNotFlagged(t *testing.T) {
	const offset = 7

	rng := rand.New(rand.NewSource(13))
	sessions := make([][]Frame, MinSessions)
	for s := range sessions {
		frames := randFrames(rng, 100, 100)
		for _, f := range frames {
			f[offset] = byte(s) // constant within the session, distinct per session
		}
		sessions[s] = frames
	}

	rep := Audit(sessions)
	for _, c := range rep.ConstantOffsets {
		if c.Offset == offset {
			t.Errorf("offset %d constant-within-session-but-varying-across flagged 0x%02x — must not be", offset, c.Value)
		}
	}
}

// TestInsufficientSessions verifies SessionsOK rejects a capture below MinSessions.
func TestInsufficientSessions(t *testing.T) {
	rep := Audit(randSessions(MinSessions-1, 200, 1200))
	if ok, _ := rep.SessionsOK(); ok {
		t.Fatalf("SessionsOK accepted %d sessions, want rejection below %d", MinSessions-1, MinSessions)
	}
}

// TestEntropySampleTooSmall verifies EntropyOK fails loud when too few large frames
// are present, rather than passing on a noisy mean.
func TestEntropySampleTooSmall(t *testing.T) {
	// Only a handful of large frames — below MinEntropyFrameSamples.
	sessions := randSessions(MinSessions, 5, 1200)
	rep := Audit(sessions)
	if rep.EntropyFrameCount >= MinEntropyFrameSamples {
		t.Fatalf("test setup produced %d large frames, expected < %d", rep.EntropyFrameCount, MinEntropyFrameSamples)
	}
	if ok, _ := rep.EntropyOK(); ok {
		t.Fatalf("EntropyOK passed with only %d large frames (< %d)", rep.EntropyFrameCount, MinEntropyFrameSamples)
	}
}

// TestSmallFramesExcludedFromEntropy confirms that genuinely-random SMALL frames
// (whose finite-sample entropy is well below 8) do NOT drag the mean-entropy metric
// down — they are excluded by the MinEntropyFrameLen cutoff. This is the subtlety a
// naive whole-capture mean would get wrong.
func TestSmallFramesExcludedFromEntropy(t *testing.T) {
	rng := rand.New(rand.NewSource(17))
	sessions := make([][]Frame, MinSessions)
	for s := range sessions {
		// A flood of small random frames (32 bytes: max log2(32)=5 bits/byte) ...
		small := randFrames(rng, 500, 32)
		// ... plus enough large random frames to satisfy the sample floor.
		large := randFrames(rng, 40, 1400)
		sessions[s] = append(small, large...)
	}
	rep := Audit(sessions)
	if rep.EntropyFrameCount < MinEntropyFrameSamples {
		t.Fatalf("only %d large frames, need >= %d", rep.EntropyFrameCount, MinEntropyFrameSamples)
	}
	if ok, msg := rep.EntropyOK(); !ok {
		t.Fatalf("EntropyOK failed despite the small frames being excluded: %s", msg)
	}
}

// TestLowCardinalityOffsetIsDetectedAndPinpointed is the TEETH of the per-offset
// distribution check: a byte that is MULTI-valued (so the single-valued constant
// check does NOT flag it) but low-cardinality — a 4-value round-robin — at a
// well-sampled offset must be caught by OffsetDistributionOK and pinpointed. This is
// the decisive gap the single-valued detector alone misses.
func TestLowCardinalityOffsetIsDetectedAndPinpointed(t *testing.T) {
	const plantOffset = 100
	values := []byte{0x10, 0x20, 0x30, 0x40}

	// 150 frames/session * MinSessions = 750 samples/offset (>= MinSamplesPerOffsetDist).
	sessions := randSessions(MinSessions, 150, 1200)
	i := 0
	for _, sess := range sessions {
		for _, f := range sess {
			f[plantOffset] = values[i%len(values)]
			i++
		}
	}

	rep := Audit(sessions)

	// The single-valued check must NOT flag it (it is 4-valued, not constant).
	if ok, _ := rep.ConstantByteOK(); !ok {
		t.Fatalf("ConstantByteOK flagged a 4-valued offset — should only flag single-valued")
	}
	// The distribution check MUST flag it and pinpoint the offset.
	ok, msg := rep.OffsetDistributionOK()
	if ok {
		t.Fatalf("low-cardinality offset %d NOT detected — distribution check is vacuous", plantOffset)
	}
	found := false
	for _, o := range rep.LowEntropyOffsets {
		if o.Offset == plantOffset {
			found = true
			if o.Distinct != len(values) {
				t.Errorf("offset %d distinct=%d, want %d", plantOffset, o.Distinct, len(values))
			}
			// 4 equiprobable values => 2.0 bits/byte.
			if o.Entropy < 1.9 || o.Entropy > 2.1 {
				t.Errorf("offset %d entropy %.4f, want ~2.0 bits", plantOffset, o.Entropy)
			}
		}
	}
	if !found {
		t.Fatalf("distribution check failed but did not pinpoint offset %d; report: %s", plantOffset, msg)
	}
	if !contains(msg, "offset 100") {
		t.Errorf("failure message does not pinpoint the offset: %q", msg)
	}
}

// TestPlaintextKindByteEscapeIsCaught reproduces the reviewer's DECISIVE escape: an
// obfuscation regression that leaks a plaintext WireGuard-style kind discriminant in
// {1,2,3,4} at offset 24 escapes BOTH the single-valued check (it is multi-valued
// across the DATA/PARITY/PROBE/CONTROL mix) AND the frame-entropy check (a ~16-byte
// structured header over an encrypted MTU payload still measures > 7.5 bits/byte).
// It asserts the audit now CATCHES it via the per-offset distribution check, with the
// realistic SKEWED traffic mix, and records the measured numbers.
func TestPlaintextKindByteEscapeIsCaught(t *testing.T) {
	const kindOffset = 24
	// Realistic skewed message-type mix: DATA dominates, then PARITY/PROBE/CONTROL.
	mix := func(i int) byte {
		switch r := i % 20; {
		case r < 14: // 70% DATA
			return 0x01
		case r < 17: // 15% PARITY
			return 0x02
		case r < 19: // 10% PROBE
			return 0x03
		default: // 5% CONTROL
			return 0x04
		}
	}

	// MTU-sized frames with an otherwise-random (encrypted) body — only the kind byte
	// leaks. 200 frames/session * MinSessions = 1000 samples at offset 24.
	sessions := randSessions(MinSessions, 200, 1400)
	i := 0
	for _, sess := range sessions {
		for _, f := range sess {
			f[kindOffset] = mix(i)
			i++
		}
	}

	rep := Audit(sessions)

	// The mean/min/p5 frame entropy stays healthy — the escape defeats check #3.
	if ok, _ := rep.EntropyOK(); !ok {
		t.Fatalf("setup invalid: a single leaked header byte should not drop frame entropy below threshold")
	}
	// The single-valued check stays green — the escape defeats check #1.
	if ok, _ := rep.ConstantByteOK(); !ok {
		t.Fatalf("setup invalid: a {1..4}-valued kind byte is not single-valued")
	}
	// The distribution check CATCHES it.
	ok, msg := rep.OffsetDistributionOK()
	if ok {
		t.Fatalf("plaintext kind-byte escape NOT caught — the audit gives false assurance; %s", msg)
	}
	var kind OffsetEntropy
	for _, o := range rep.LowEntropyOffsets {
		if o.Offset == kindOffset {
			kind = o
		}
	}
	if kind.Offset != kindOffset {
		t.Fatalf("distribution check did not pinpoint the kind byte at offset %d; report: %s", kindOffset, msg)
	}
	// Skewed 4-symbol distribution entropy H ~= 1.32 bits — far below the 6.5
	// threshold. Record the numbers for the audit trail.
	t.Logf("plaintext kind-byte escape CAUGHT: offset %d entropy %.4f bits/byte (%d distinct values over %d frames / %d sessions), threshold %.2f, margin %.2f bits",
		kind.Offset, kind.Entropy, kind.Distinct, kind.Samples, kind.Sessions, PerOffsetEntropyThreshold, PerOffsetEntropyThreshold-kind.Entropy)
	if kind.Entropy > 2.0 {
		t.Errorf("kind-byte entropy %.4f exceeds 2 bits — skewed 4-symbol distribution should be ~1.3 bits", kind.Entropy)
	}
}

// TestEntropySubsetLeakDetected is the TEETH of the frame-entropy p5 quantile: a
// small SUBSET (~13%) of large frames drawn from a restricted alphabet (entropy
// ~6.6 bits/byte, ABOVE the hard per-frame floor but below the p5 floor) leaves the
// MEAN healthy yet must fail the audit via the 5th-percentile floor.
func TestEntropySubsetLeakDetected(t *testing.T) {
	rng := rand.New(rand.NewSource(23))
	sessions := make([][]Frame, MinSessions)
	for s := range sessions {
		clean := randFrames(rng, 100, 1400) // full-entropy large frames
		// Leak: 15 large frames over a 100-symbol alphabet => ~log2(100)=6.64 bits,
		// above PerFrameEntropyFloor (6.0) but below LargeFrameEntropyP5Floor (7.0).
		leak := make([]Frame, 15)
		for i := range leak {
			f := make([]byte, 1400)
			for j := range f {
				f[j] = byte(rng.Intn(100))
			}
			leak[i] = f
		}
		sessions[s] = append(clean, leak...)
	}

	rep := Audit(sessions)
	if rep.MeanEntropy < MeanEntropyThreshold {
		t.Fatalf("setup invalid: the subset should leave the MEAN (%.4f) above the threshold so the p5 floor is what catches it", rep.MeanEntropy)
	}
	if rep.MinFrameEntropy < PerFrameEntropyFloor {
		t.Fatalf("setup invalid: leak entropy %.4f fell below the hard floor %.2f — this case must exercise the p5 floor, not the min floor", rep.MinFrameEntropy, PerFrameEntropyFloor)
	}
	ok, msg := rep.EntropyOK()
	if ok {
		t.Fatalf("entropy subset leak NOT detected — the p5 quantile floor is vacuous; mean=%.4f min=%.4f p5=%.4f", rep.MeanEntropy, rep.MinFrameEntropy, rep.P5FrameEntropy)
	}
	if !contains(msg, "5th-percentile") {
		t.Errorf("failure not attributed to the p5 floor: %q", msg)
	}
}

// TestFullyPlaintextFrameCaughtByMinFloor proves the hard per-frame floor catches a
// single fully-low-entropy (plaintext) large frame that a healthy mean would hide.
func TestFullyPlaintextFrameCaughtByMinFloor(t *testing.T) {
	rng := rand.New(rand.NewSource(29))
	sessions := make([][]Frame, MinSessions)
	for s := range sessions {
		clean := randFrames(rng, 200, 1400)
		if s == 0 {
			// One all-zero (0 bits/byte) large frame in the whole capture.
			clean = append(clean, make([]byte, 1400))
		}
		sessions[s] = clean
	}
	rep := Audit(sessions)
	if rep.MeanEntropy < MeanEntropyThreshold {
		t.Fatalf("setup invalid: one plaintext frame in ~1000 should not drop the mean (%.4f) below threshold", rep.MeanEntropy)
	}
	ok, msg := rep.EntropyOK()
	if ok {
		t.Fatal("a fully-plaintext large frame was NOT caught by the per-frame floor")
	}
	if !contains(msg, "floor") {
		t.Errorf("failure not attributed to a per-frame floor: %q", msg)
	}
}

// TestCoverageGuardShallowRegion proves CoverageOK fails when the fully-judged offset
// region does not reach the required depth (e.g. a traffic-mix change shrank frames),
// and passes when it does.
func TestCoverageGuardShallowRegion(t *testing.T) {
	// All frames only 600 bytes long => offsets judged up to 599.
	shallow := Audit(randSessions(MinSessions, 200, 600))
	if ok, _ := shallow.CoverageOK(1024); ok {
		t.Fatalf("CoverageOK accepted a region reaching only offset %d (< 1024)", shallow.HighestJudgedOffset)
	}
	if ok, msg := shallow.CoverageOK(500); !ok {
		t.Fatalf("CoverageOK rejected a 600-byte region against a 500-offset floor: %s", msg)
	}

	deep := Audit(randSessions(MinSessions, 200, 1400))
	if ok, msg := deep.CoverageOK(1024); !ok {
		t.Fatalf("CoverageOK rejected a 1400-byte region against a 1024-offset floor: %s", msg)
	}
}

// TestUnderSampledOffsetNotDistributionJudged verifies that an offset reached by
// fewer than MinSamplesPerOffsetDist frames is NOT distribution-judged (so a genuine
// uniform-but-thinly-sampled tail offset is never falsely flagged low-entropy), and
// is counted in coverage instead.
func TestUnderSampledOffsetNotDistributionJudged(t *testing.T) {
	rng := rand.New(rand.NewSource(31))
	sessions := make([][]Frame, MinSessions)
	for s := range sessions {
		// 200 short frames (len 100) — offset 50 gets 1000 samples (judged).
		short := randFrames(rng, 200, 100)
		// A handful of long frames (len 2000) with a CONSTANT byte at offset 1500:
		// only 5*10 = 50 samples < MinSamplesPerOffsetDist, so it must NOT be flagged
		// low-entropy despite entropy 0 there.
		long := randFrames(rng, 10, 2000)
		for _, f := range long {
			f[1500] = 0x00
		}
		sessions[s] = append(short, long...)
	}
	rep := Audit(sessions)
	for _, o := range rep.LowEntropyOffsets {
		if o.Offset == 1500 {
			t.Errorf("under-sampled offset 1500 (%d samples) flagged low-entropy — distribution min-sample rule failed", o.Samples)
		}
	}
	if rep.UnderSampledOffsets == 0 {
		t.Errorf("expected the deep tail offsets to be counted under-sampled, got 0")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
