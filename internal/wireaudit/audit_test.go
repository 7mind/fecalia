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
	if ok, msg := rep.EntropyOK(); !ok {
		t.Fatalf("EntropyOK on random wire: %s", msg)
	}
	if ok, _ := rep.OK(); !ok {
		t.Fatalf("random wire failed the combined audit")
	}
	// Sanity: a 1200-byte uniform-random payload's empirical entropy sits well above
	// the threshold but strictly below the 8.0 ceiling.
	if rep.MeanEntropy <= MeanEntropyThreshold || rep.MeanEntropy >= entropyMaxBitsPerByte {
		t.Fatalf("mean entropy %.4f not in (%.2f, %.1f)", rep.MeanEntropy, MeanEntropyThreshold, entropyMaxBitsPerByte)
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

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
