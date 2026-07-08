// Package wireaudit implements the requirement-6 (DPI-resistance) wire-format
// obfuscation audit. Given the outer UDP payloads (wanbond wire frames) captured
// across MULTIPLE fresh tunnel sessions, it asserts that the obfuscated wire carries
// no static or low-cardinality DPI signature and no low-entropy leak.
//
// The wanbond frame is nonce[24] || obf(body) [|| tag[16]] (see internal/frame):
// the nonce is fresh random bytes, the body is XChaCha20-keystream-obfuscated, and
// the tag is a truncated HMAC — so EVERY byte offset is keystream-uniform (~8
// bits/byte at thousands of samples) and every frame is high-entropy BY DESIGN.
// This audit CONFIRMS that empirically over the real captured wire; a constant
// offset, a low-cardinality offset, or a sub-threshold entropy is a real
// requirement-6 defect, not a reason to relax the thresholds.
//
// The audit runs four complementary checks, deliberately overlapping so a single
// blind spot cannot pass a signaturable wire:
//
//  1. Single-valued offset (ConstantByteOK): NO byte offset holds one constant value
//     across the whole capture — a fully-static fingerprint.
//
//  2. Offset value-distribution (OffsetDistributionOK): the per-offset Shannon
//     entropy of the byte VALUES seen at each well-sampled offset exceeds
//     PerOffsetEntropyThreshold. This catches a LOW-CARDINALITY signature that the
//     single-valued check misses — e.g. a plaintext kind discriminant in {1,2,3,4}
//     (<= 2 bits), a per-session id byte (<= log2(sessions) bits), or a skewed
//     counter high-byte — none of which are single-valued across the traffic mix.
//
//  3. Frame entropy (EntropyOK): the MEAN, the MINIMUM, and the 5th-percentile
//     per-packet payload entropy over the large frames each clear their thresholds,
//     so a small leaking SUBSET of plaintext frames cannot hide behind a healthy
//     mean.
//
//  4. Coverage (CoverageOK / the Report coverage fields): the distribution check's
//     fully-judged offset region spans the bulk-frame length, so a traffic-mix
//     change cannot silently shrink the audited region and pass vacuously.
//
// The analysis is pure and unit-testable on synthetic frame sets without root or a
// live capture; the privileged pcap capture lives in the -tags e2e test that feeds
// its parsed frames here.
package wireaudit

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// Frame is one captured outer UDP payload — a single wanbond wire frame.
type Frame = []byte

const (
	// MinSessions is the minimum number of FRESH tunnel sessions (distinct
	// session id / keys / nonces) the capture must cover. A cross-session constant
	// or low-cardinality byte is the fingerprint the audit hunts, so it needs
	// several independent sessions to distinguish a true cross-session signature
	// from a within-session artefact.
	MinSessions = 5

	// MinSamplesPerOffset is the minimum number of captured frames that must reach a
	// byte offset before the SINGLE-VALUED (constant) check judges it. Frames are
	// variable-length: an offset near the tail exists only in the largest packets, so
	// a rarely-reached offset carries too few samples to judge. An offset seen in
	// fewer than this many frames is UNDER-SAMPLED — reported as neither constant nor
	// cleared. Determining "is this offset single-valued" needs only a modest sample.
	MinSamplesPerOffset = 32

	// MinSamplesPerOffsetDist is the (higher) minimum sample count an offset needs
	// before its VALUE-DISTRIBUTION ENTROPY is judged. Entropy estimated from a byte
	// histogram is biased LOW for small samples: the maximum-likelihood estimator
	// underestimates by ~= (256-1)/(2*N*ln2) bits, so a uniform offset needs many
	// samples to measure near 8. At N = 512 that bias is ~= 255/(2*512*ln2) ~= 0.36
	// bits, so a keystream-uniform offset has expected empirical entropy ~= 7.64
	// bits/byte — comfortably above PerOffsetEntropyThreshold. Below this floor the
	// offset is UNDER-SAMPLED for the distribution check (counted in coverage, not
	// judged) so a genuine uniform offset is never falsely flagged low-entropy.
	MinSamplesPerOffsetDist = 512

	// MinSessionsPerOffset is the minimum number of DISTINCT sessions whose frames
	// must reach an offset before a verdict (single-valued OR low-entropy) counts. A
	// byte that is merely constant/low-cardinality within ONE session (but is absent
	// from, or varies in, the others) is not a cross-session fingerprint.
	MinSessionsPerOffset = 2

	// PerOffsetEntropyThreshold is the minimum per-offset value-distribution entropy
	// (bits/byte) a well-sampled offset must exceed. A keystream-uniform offset with
	// >= MinSamplesPerOffsetDist samples measures ~= 7.6 bits/byte (see the bias note
	// above); this 6.5 threshold sits ~1.1 bits below that empirical expectation
	// (margin for finite-sample variance) yet FAR above any low-cardinality
	// signature: a {1..4} kind byte is <= 2 bits, a per-session id byte over
	// MinSessions sessions is <= log2(5) ~= 2.3 bits, a near-constant counter
	// high-byte is ~= 0 bits — all caught with wide margin.
	PerOffsetEntropyThreshold = 6.5

	// MinEntropyFrameLen is the smallest frame (in bytes) that contributes to the
	// per-frame entropy metrics. Shannon entropy from a finite byte histogram is
	// biased low for small payloads (a 32-byte frame reaches at most log2(32)=5
	// bits/byte even if perfectly random), so a naive whole-capture mean would
	// FALSELY FAIL on small PROBE/junk frames. Restricting to frames >= 1024 bytes
	// bounds that bias to ~= 255/(2*1024*ln2) ~= 0.18 bits, so a uniform-random
	// 1024-byte payload has expected empirical entropy ~= 7.82 bits/byte.
	MinEntropyFrameLen = 1024

	// MinEntropyFrameSamples is the minimum count of frames >= MinEntropyFrameLen the
	// capture must carry before the frame-entropy assertions are statistically
	// meaningful. Below it the metrics are noise and the audit fails loud (drive more
	// bulk traffic) rather than passing vacuously.
	MinEntropyFrameSamples = 100

	// MeanEntropyThreshold is the mean per-packet payload entropy (bits/byte) the
	// large frames must exceed. ~0.3 bits below the ~7.82 empirical expectation for
	// uniform 1024-byte payloads (see MinEntropyFrameLen); far above any structured
	// plaintext. The mean ALONE is dilutable by a small plaintext subset, so it is
	// paired with the min and p5 floors below.
	MeanEntropyThreshold = 7.5

	// PerFrameEntropyFloor is a HARD per-frame floor: NO single large frame may fall
	// below it. A fully-plaintext / low-entropy large frame (a structured or
	// repetitive payload, ~2-5 bits/byte) is caught even if it is a tiny fraction of
	// the capture that leaves the mean healthy. Uniform 1024-byte frames measure
	// ~7.82 with negligible variance, so this 6.0 floor has ~1.8 bits of margin.
	PerFrameEntropyFloor = 6.0

	// LargeFrameEntropyP5Floor is the minimum 5th-percentile per-frame entropy over
	// the large frames — a leaking SUBSET up to ~5% (individually above the hard
	// PerFrameEntropyFloor but anomalously low) is caught here. Uniform large frames
	// have p5 ~= 7.8, so this 7.0 floor clears with ~0.8 bits of margin.
	LargeFrameEntropyP5Floor = 7.0

	// entropyMaxBitsPerByte is the Shannon-entropy ceiling for a byte alphabet.
	entropyMaxBitsPerByte = 8.0

	// p5Percentile is the low quantile used for the frame-entropy subset-leak floor.
	p5Percentile = 5.0
)

// ConstantOffset records one byte offset held to a single value across the capture.
type ConstantOffset struct {
	// Offset is the byte position (0-based) within the frame that was constant.
	Offset int
	// Value is the single byte value observed at Offset across every sampled frame.
	Value byte
	// Samples is how many frames reached Offset (all carried Value).
	Samples int
	// Sessions is how many distinct sessions contributed those samples.
	Sessions int
}

// OffsetEntropy records one well-sampled byte offset whose value-distribution
// entropy fell below PerOffsetEntropyThreshold: a low-cardinality DPI signature.
type OffsetEntropy struct {
	// Offset is the byte position (0-based) within the frame.
	Offset int
	// Entropy is the Shannon entropy (bits/byte) of the byte VALUES at Offset.
	Entropy float64
	// Distinct is how many distinct byte values appeared at Offset.
	Distinct int
	// Samples is how many frames reached Offset.
	Samples int
	// Sessions is how many distinct sessions contributed those samples.
	Sessions int
}

// Report is the outcome of an audit over the captured sessions.
type Report struct {
	// Sessions is the number of distinct capture sessions supplied.
	Sessions int
	// TotalFrames is the total number of frames across all sessions.
	TotalFrames int
	// MaxFrameLen is the longest frame observed (the highest offset that exists).
	MaxFrameLen int

	// ConstantOffsets holds every well-sampled, cross-session single-valued offset.
	ConstantOffsets []ConstantOffset
	// LowEntropyOffsets holds every well-sampled offset whose value distribution fell
	// below PerOffsetEntropyThreshold.
	LowEntropyOffsets []OffsetEntropy

	// JudgedOffsets is the count of offsets that received a value-distribution verdict
	// (>= MinSamplesPerOffsetDist samples across >= MinSessionsPerOffset sessions).
	JudgedOffsets int
	// UnderSampledOffsets is the count of offsets that exist but did NOT get a
	// distribution verdict (too few samples/sessions).
	UnderSampledOffsets int
	// HighestJudgedOffset is the largest offset that received a distribution verdict,
	// or -1 if none did. It measures how far into the frame the audit actually reaches.
	HighestJudgedOffset int

	// MeanEntropy is the mean per-packet Shannon entropy (bits/byte) over frames
	// >= MinEntropyFrameLen.
	MeanEntropy float64
	// MinFrameEntropy is the minimum per-frame entropy over those large frames.
	MinFrameEntropy float64
	// P5FrameEntropy is the 5th-percentile per-frame entropy over those large frames.
	P5FrameEntropy float64
	// EntropyFrameCount is how many frames >= MinEntropyFrameLen fed the metrics above.
	EntropyFrameCount int
}

// Audit runs every requirement-6 check over frames grouped by session and returns
// the combined report. The offset checks pool every frame of every session; the
// per-offset session set enforces the cross-session requirement.
func Audit(sessions [][]Frame) Report {
	rep := Report{Sessions: len(sessions), HighestJudgedOffset: -1}
	for _, sess := range sessions {
		rep.TotalFrames += len(sess)
	}
	oa := analyzeOffsets(sessions)
	rep.MaxFrameLen = oa.maxLen
	rep.ConstantOffsets = oa.constant
	rep.LowEntropyOffsets = oa.lowEntropy
	rep.JudgedOffsets = oa.judged
	rep.UnderSampledOffsets = oa.underSampled
	rep.HighestJudgedOffset = oa.highestJudged

	fs := frameEntropyStats(sessions)
	rep.MeanEntropy = fs.mean
	rep.MinFrameEntropy = fs.min
	rep.P5FrameEntropy = fs.p5
	rep.EntropyFrameCount = fs.count
	return rep
}

// offsetAcc accumulates, for one byte offset, the full value histogram, the sample
// count, and which sessions contributed.
type offsetAcc struct {
	hist     [256]int
	samples  int
	sessions map[int]struct{}
}

// offsetAnalysis is the per-offset pass result.
type offsetAnalysis struct {
	maxLen        int
	constant      []ConstantOffset
	lowEntropy    []OffsetEntropy
	judged        int
	underSampled  int
	highestJudged int
}

// analyzeOffsets builds a value histogram per byte offset across the whole pool and
// derives (a) single-valued offsets, (b) low-entropy (low-cardinality) offsets among
// those with enough samples for a distribution verdict, and (c) coverage counts.
func analyzeOffsets(sessions [][]Frame) offsetAnalysis {
	maxLen := 0
	for _, sess := range sessions {
		for _, f := range sess {
			if len(f) > maxLen {
				maxLen = len(f)
			}
		}
	}
	res := offsetAnalysis{maxLen: maxLen, highestJudged: -1}
	if maxLen == 0 {
		return res
	}
	accs := make([]offsetAcc, maxLen)
	for si, sess := range sessions {
		for _, f := range sess {
			for off := 0; off < len(f); off++ {
				a := &accs[off]
				a.hist[f[off]]++
				a.samples++
				if a.sessions == nil {
					a.sessions = make(map[int]struct{})
				}
				a.sessions[si] = struct{}{}
			}
		}
	}
	for off := 0; off < maxLen; off++ {
		a := &accs[off]
		sess := len(a.sessions)
		distinct, only := distinctInfo(&a.hist)

		// Single-valued (fully-constant) check: modest sample floor.
		if a.samples >= MinSamplesPerOffset && sess >= MinSessionsPerOffset && distinct == 1 {
			res.constant = append(res.constant, ConstantOffset{
				Offset: off, Value: only, Samples: a.samples, Sessions: sess,
			})
		}

		// Value-distribution entropy check: higher sample floor so a uniform offset
		// is not falsely flagged low-entropy by finite-sample bias.
		if a.samples >= MinSamplesPerOffsetDist && sess >= MinSessionsPerOffset {
			res.judged++
			if off > res.highestJudged {
				res.highestJudged = off
			}
			h := histEntropy(&a.hist, a.samples)
			if h < PerOffsetEntropyThreshold {
				res.lowEntropy = append(res.lowEntropy, OffsetEntropy{
					Offset: off, Entropy: h, Distinct: distinct, Samples: a.samples, Sessions: sess,
				})
			}
		} else {
			res.underSampled++
		}
	}
	return res
}

// distinctInfo returns the number of distinct byte values in a histogram and, when
// exactly one value is present, that value.
func distinctInfo(h *[256]int) (distinct int, only byte) {
	for v := 0; v < 256; v++ {
		if h[v] > 0 {
			distinct++
			only = byte(v)
		}
	}
	return distinct, only
}

// histEntropy returns the Shannon entropy (bits/byte) of a byte-value histogram over
// n total samples.
func histEntropy(h *[256]int, n int) float64 {
	if n == 0 {
		return 0
	}
	nf := float64(n)
	e := 0.0
	for _, c := range h {
		if c == 0 {
			continue
		}
		p := float64(c) / nf
		e -= p * math.Log2(p)
	}
	return e
}

// shannonEntropy returns the Shannon entropy (bits/byte) of payload's byte histogram.
func shannonEntropy(payload []byte) float64 {
	if len(payload) == 0 {
		return 0
	}
	var hist [256]int
	for _, b := range payload {
		hist[b]++
	}
	return histEntropy(&hist, len(payload))
}

// frameStats holds the per-frame entropy summary over the large frames.
type frameStats struct {
	mean, min, p5 float64
	count         int
}

// frameEntropyStats computes the mean, minimum, and 5th-percentile per-frame entropy
// over every frame >= MinEntropyFrameLen. Frames below the cutoff are excluded (their
// finite-sample bias would depress the metrics regardless of ciphertext quality).
func frameEntropyStats(sessions [][]Frame) frameStats {
	var es []float64
	var sum float64
	for _, sess := range sessions {
		for _, f := range sess {
			if len(f) >= MinEntropyFrameLen {
				h := shannonEntropy(f)
				es = append(es, h)
				sum += h
			}
		}
	}
	if len(es) == 0 {
		return frameStats{}
	}
	sort.Float64s(es)
	return frameStats{
		mean:  sum / float64(len(es)),
		min:   es[0],
		p5:    percentile(es, p5Percentile),
		count: len(es),
	}
}

// percentile returns the p-th percentile (nearest-rank) of a sorted ascending slice.
func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	rank := int(math.Ceil(p/100*float64(len(sorted)))) - 1
	if rank < 0 {
		rank = 0
	}
	if rank >= len(sorted) {
		rank = len(sorted) - 1
	}
	return sorted[rank]
}

// SessionsOK reports whether the capture covered enough fresh sessions.
func (r Report) SessionsOK() (bool, string) {
	if r.Sessions < MinSessions {
		return false, fmt.Sprintf("captured %d fresh sessions, need >= %d", r.Sessions, MinSessions)
	}
	return true, fmt.Sprintf("captured %d fresh sessions (>= %d)", r.Sessions, MinSessions)
}

// ConstantByteOK reports whether the single-valued-offset property holds. On failure
// it PINPOINTS every offending offset and value; on success it reports coverage so a
// green verdict is never silent about how much of the frame was judged.
func (r Report) ConstantByteOK() (bool, string) {
	if len(r.ConstantOffsets) == 0 {
		return true, fmt.Sprintf("no single-valued byte offset over %d frames (max frame len %d; %d offsets fully judged, highest judged %d, %d under-sampled)",
			r.TotalFrames, r.MaxFrameLen, r.JudgedOffsets, r.HighestJudgedOffset, r.UnderSampledOffsets)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d constant byte position(s) — DPI fingerprint:", len(r.ConstantOffsets))
	for _, c := range r.ConstantOffsets {
		fmt.Fprintf(&b, " offset %d = 0x%02x (constant over %d frames across %d sessions);",
			c.Offset, c.Value, c.Samples, c.Sessions)
	}
	return false, b.String()
}

// OffsetDistributionOK reports whether every well-sampled offset's value distribution
// clears PerOffsetEntropyThreshold. On failure it PINPOINTS each low-entropy offset —
// the low-cardinality signature the single-valued check misses.
func (r Report) OffsetDistributionOK() (bool, string) {
	if r.JudgedOffsets == 0 {
		return false, fmt.Sprintf("no offset had >= %d samples across >= %d sessions — distribution unjudged; drive more bulk traffic",
			MinSamplesPerOffsetDist, MinSessionsPerOffset)
	}
	if len(r.LowEntropyOffsets) == 0 {
		return true, fmt.Sprintf("all %d judged offsets clear %.2f bits/byte value entropy (highest judged offset %d)",
			r.JudgedOffsets, PerOffsetEntropyThreshold, r.HighestJudgedOffset)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d low-cardinality byte position(s) — DPI signature:", len(r.LowEntropyOffsets))
	for _, o := range r.LowEntropyOffsets {
		fmt.Fprintf(&b, " offset %d entropy %.3f bits/byte (%d distinct values over %d frames / %d sessions, threshold %.2f);",
			o.Offset, o.Entropy, o.Distinct, o.Samples, o.Sessions, PerOffsetEntropyThreshold)
	}
	return false, b.String()
}

// EntropyOK reports whether the frame-entropy metrics clear their thresholds with a
// large-enough sample: the mean, a hard per-frame floor (catches a fully-plaintext
// frame), and the 5th-percentile floor (catches a small leaking subset).
func (r Report) EntropyOK() (bool, string) {
	if r.EntropyFrameCount < MinEntropyFrameSamples {
		return false, fmt.Sprintf("only %d frames >= %d bytes (need >= %d) — entropy sample too small; drive more bulk traffic",
			r.EntropyFrameCount, MinEntropyFrameLen, MinEntropyFrameSamples)
	}
	if r.MinFrameEntropy < PerFrameEntropyFloor {
		return false, fmt.Sprintf("a large frame has entropy %.4f bits/byte < %.2f floor — a low-entropy (plaintext) frame is on the wire (min over %d frames >= %d bytes)",
			r.MinFrameEntropy, PerFrameEntropyFloor, r.EntropyFrameCount, MinEntropyFrameLen)
	}
	if r.P5FrameEntropy < LargeFrameEntropyP5Floor {
		return false, fmt.Sprintf("5th-percentile large-frame entropy %.4f bits/byte < %.2f floor — a low-entropy SUBSET is leaking (over %d frames >= %d bytes)",
			r.P5FrameEntropy, LargeFrameEntropyP5Floor, r.EntropyFrameCount, MinEntropyFrameLen)
	}
	if r.MeanEntropy < MeanEntropyThreshold {
		return false, fmt.Sprintf("mean payload entropy %.4f bits/byte < %.2f threshold (over %d frames >= %d bytes)",
			r.MeanEntropy, MeanEntropyThreshold, r.EntropyFrameCount, MinEntropyFrameLen)
	}
	return true, fmt.Sprintf("frame entropy OK: mean %.4f, min %.4f (floor %.2f), p5 %.4f (floor %.2f) bits/byte over %d frames >= %d bytes (max %.1f)",
		r.MeanEntropy, r.MinFrameEntropy, PerFrameEntropyFloor, r.P5FrameEntropy, LargeFrameEntropyP5Floor, r.EntropyFrameCount, MinEntropyFrameLen, entropyMaxBitsPerByte)
}

// CoverageOK reports whether the distribution check's fully-judged offset region
// reaches at least minJudgedOffset — a guard that a traffic-mix change has not
// silently shrunk the audited region below the bulk-frame length.
func (r Report) CoverageOK(minJudgedOffset int) (bool, string) {
	if r.HighestJudgedOffset < minJudgedOffset {
		return false, fmt.Sprintf("distribution check reached only offset %d (< %d required) — audited region too shallow; %d offsets judged, %d under-sampled (max frame %d)",
			r.HighestJudgedOffset, minJudgedOffset, r.JudgedOffsets, r.UnderSampledOffsets, r.MaxFrameLen)
	}
	return true, fmt.Sprintf("distribution check covers offsets 0..%d (>= %d required; max frame %d, %d under-sampled tail offsets)",
		r.HighestJudgedOffset, minJudgedOffset, r.MaxFrameLen, r.UnderSampledOffsets)
}

// OK reports whether every intrinsic check passes (Sessions, single-valued,
// distribution, frame entropy), joining each check's message. Coverage is asserted
// separately by the caller with its own minimum-offset expectation.
func (r Report) OK() (bool, string) {
	sOK, sMsg := r.SessionsOK()
	cOK, cMsg := r.ConstantByteOK()
	dOK, dMsg := r.OffsetDistributionOK()
	eOK, eMsg := r.EntropyOK()
	msg := strings.Join([]string{sMsg, cMsg, dMsg, eMsg}, "\n")
	return sOK && cOK && dOK && eOK, msg
}
