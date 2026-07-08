// Package wireaudit implements the requirement-6 (DPI-resistance) wire-format
// obfuscation audit. Given the outer UDP payloads (wanbond wire frames) captured
// across MULTIPLE fresh tunnel sessions, it asserts two properties of the
// obfuscated wire:
//
//  1. Constant-byte-position: NO byte offset holds a single constant value across
//     the whole capture. A byte that is identical at the same offset in every
//     packet of every session is a static DPI fingerprint. (A byte constant only
//     WITHIN one session but varying across sessions is fine — it is not a
//     cross-session signature — so the detector requires an offset be constant
//     across frames drawn from >= MinSessionsPerOffset distinct sessions before it
//     flags it.)
//
//  2. Payload entropy: the mean per-packet Shannon entropy of the large frames
//     exceeds MeanEntropyThreshold bits/byte.
//
// The wanbond frame is nonce[24] || obf(body) [|| tag[16]] (see internal/frame):
// the nonce is fresh random bytes, the body is XChaCha20-keystream-obfuscated, and
// the tag is a truncated HMAC — so the frame is high-entropy and free of fixed
// offsets BY DESIGN. This audit CONFIRMS that empirically over the real captured
// wire; a genuine constant offset or a sub-threshold mean entropy is a real
// requirement-6 defect, not a reason to relax the thresholds.
//
// The analysis is pure and unit-testable on synthetic frame sets without root or a
// live capture; the privileged pcap capture lives in the -tags e2e test that feeds
// its parsed frames here.
package wireaudit

import (
	"fmt"
	"math"
	"strings"
)

// Frame is one captured outer UDP payload — a single wanbond wire frame.
type Frame = []byte

const (
	// MinSessions is the minimum number of FRESH tunnel sessions (distinct
	// session id / keys / nonces) the capture must cover. A cross-session constant
	// byte is the fingerprint the audit hunts, so it needs several independent
	// sessions to distinguish a true cross-session constant from a within-session
	// artefact.
	MinSessions = 5

	// MinSamplesPerOffset is the minimum number of captured frames that must reach
	// a byte offset before the audit will judge that offset constant or varying.
	// Frames are variable-length: an offset near the tail exists only in the
	// largest packets, so a rarely-reached offset carries too few samples to judge.
	// An offset seen in fewer than this many frames is UNDER-SAMPLED — reported as
	// neither constant nor cleared — so a sparsely-present offset is never falsely
	// flagged constant (nor falsely cleared).
	MinSamplesPerOffset = 32

	// MinSessionsPerOffset is the minimum number of DISTINCT sessions whose frames
	// must reach an offset before a single-value verdict counts as a cross-session
	// fingerprint. Requiring >= 2 sessions means a byte that merely happens to be
	// constant within ONE session (but is absent from, or varies in, the others) is
	// never flagged — only a genuine cross-session constant is.
	MinSessionsPerOffset = 2

	// MinEntropyFrameLen is the smallest frame (in bytes) that contributes to the
	// mean-entropy metric. Shannon entropy estimated from a finite byte histogram is
	// biased LOW for small payloads: a payload of L uniform-random bytes cannot
	// exercise all 256 symbols and its expected empirical entropy is depressed by
	// the Miller-Madow finite-sample bias, ~= (256-1)/(2*L*ln2) bits below 8. So a
	// small PROBE or amnezia-junk frame that is genuinely random still measures well
	// under 8 bits/byte, and a naive "mean > 7.5" over ALL frames would FALSELY
	// FAIL on them. Restricting the metric to frames >= 1024 bytes bounds that bias
	// to ~= 255/(2*1024*ln2) ~= 0.18 bits, so a uniform-random 1024-byte payload has
	// expected empirical entropy ~= 7.82 bits/byte — comfortably above the threshold.
	MinEntropyFrameLen = 1024

	// MinEntropyFrameSamples is the minimum count of frames >= MinEntropyFrameLen the
	// capture must carry before the mean-entropy assertion is statistically
	// meaningful. Below it the mean is noise and the audit fails loud (drive more
	// bulk traffic) rather than passing vacuously.
	MinEntropyFrameSamples = 100

	// MeanEntropyThreshold is the mean per-packet payload entropy (bits/byte) the
	// large frames must exceed. XChaCha20 keystream output is indistinguishable from
	// uniform random, whose expected empirical entropy at MinEntropyFrameLen is
	// ~= 7.82 bits/byte (see MinEntropyFrameLen); MTU-sized frames reach ~= 7.9. The
	// 7.5 threshold sits ~0.3 bits below that empirical expectation (margin for
	// finite-sample variance) yet far above any structured or low-entropy plaintext,
	// so a genuinely-random payload never fails while a plaintext leak would.
	MeanEntropyThreshold = 7.5

	// entropyMaxBitsPerByte is the Shannon-entropy ceiling for a byte alphabet.
	entropyMaxBitsPerByte = 8.0
)

// ConstantOffset records one offending byte offset that held a single value across
// the capture: the fingerprint the audit exists to catch.
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

// Report is the outcome of an audit over the captured sessions.
type Report struct {
	// Sessions is the number of distinct capture sessions supplied.
	Sessions int
	// TotalFrames is the total number of frames across all sessions.
	TotalFrames int
	// MaxFrameLen is the longest frame observed (the highest offset judged).
	MaxFrameLen int
	// ConstantOffsets holds every well-sampled, cross-session-constant offset. Empty
	// means the constant-byte property holds.
	ConstantOffsets []ConstantOffset
	// MeanEntropy is the mean per-packet Shannon entropy (bits/byte) over frames
	// >= MinEntropyFrameLen.
	MeanEntropy float64
	// EntropyFrameCount is how many frames >= MinEntropyFrameLen fed MeanEntropy.
	EntropyFrameCount int
}

// Audit runs both requirement-6 checks over frames grouped by session and returns
// the combined report. The constant-byte check pools every frame of every session
// (an offset is constant only if it holds one value across the whole pool); the
// per-offset session set enforces the cross-session requirement.
func Audit(sessions [][]Frame) Report {
	rep := Report{Sessions: len(sessions)}
	for _, sess := range sessions {
		rep.TotalFrames += len(sess)
	}
	rep.ConstantOffsets, rep.MaxFrameLen = detectConstantOffsets(sessions)
	rep.MeanEntropy, rep.EntropyFrameCount = meanEntropy(sessions)
	return rep
}

// offsetAcc accumulates, for one byte offset, whether every sampled value has been
// identical, how many frames reached the offset, and which sessions contributed.
type offsetAcc struct {
	first    byte
	allSame  bool
	seen     bool
	samples  int
	sessions map[int]struct{}
}

// detectConstantOffsets finds every byte offset that is (a) reached by at least
// MinSamplesPerOffset frames, (b) reached by at least MinSessionsPerOffset distinct
// sessions, and (c) held a single value across all of them. It returns those
// offsets and the longest frame seen.
func detectConstantOffsets(sessions [][]Frame) ([]ConstantOffset, int) {
	maxLen := 0
	for _, sess := range sessions {
		for _, f := range sess {
			if len(f) > maxLen {
				maxLen = len(f)
			}
		}
	}
	if maxLen == 0 {
		return nil, 0
	}
	accs := make([]offsetAcc, maxLen)
	for si, sess := range sessions {
		for _, f := range sess {
			for off := 0; off < len(f); off++ {
				a := &accs[off]
				b := f[off]
				if !a.seen {
					a.seen = true
					a.first = b
					a.allSame = true
					a.sessions = make(map[int]struct{})
				} else if b != a.first {
					a.allSame = false
				}
				a.samples++
				a.sessions[si] = struct{}{}
			}
		}
	}
	var out []ConstantOffset
	for off := 0; off < maxLen; off++ {
		a := &accs[off]
		if a.samples >= MinSamplesPerOffset && len(a.sessions) >= MinSessionsPerOffset && a.allSame {
			out = append(out, ConstantOffset{
				Offset:   off,
				Value:    a.first,
				Samples:  a.samples,
				Sessions: len(a.sessions),
			})
		}
	}
	return out, maxLen
}

// shannonEntropy returns the Shannon entropy (bits/byte) of payload's byte
// histogram. An empty payload has zero entropy.
func shannonEntropy(payload []byte) float64 {
	if len(payload) == 0 {
		return 0
	}
	var hist [256]int
	for _, b := range payload {
		hist[b]++
	}
	n := float64(len(payload))
	h := 0.0
	for _, c := range hist {
		if c == 0 {
			continue
		}
		p := float64(c) / n
		h -= p * math.Log2(p)
	}
	return h
}

// meanEntropy averages shannonEntropy over every frame >= MinEntropyFrameLen and
// returns the mean and the contributing frame count. Frames below the cutoff are
// excluded (their finite-sample entropy bias would depress the mean regardless of
// the ciphertext quality — see MinEntropyFrameLen).
func meanEntropy(sessions [][]Frame) (float64, int) {
	var sum float64
	count := 0
	for _, sess := range sessions {
		for _, f := range sess {
			if len(f) >= MinEntropyFrameLen {
				sum += shannonEntropy(f)
				count++
			}
		}
	}
	if count == 0 {
		return 0, 0
	}
	return sum / float64(count), count
}

// SessionsOK reports whether the capture covered enough fresh sessions.
func (r Report) SessionsOK() (bool, string) {
	if r.Sessions < MinSessions {
		return false, fmt.Sprintf("captured %d fresh sessions, need >= %d", r.Sessions, MinSessions)
	}
	return true, fmt.Sprintf("captured %d fresh sessions (>= %d)", r.Sessions, MinSessions)
}

// ConstantByteOK reports whether the constant-byte-position property holds. On
// failure the message PINPOINTS every offending offset, its constant value, and its
// sample/session coverage.
func (r Report) ConstantByteOK() (bool, string) {
	if len(r.ConstantOffsets) == 0 {
		return true, fmt.Sprintf("no constant byte position over %d frames (max frame len %d, min %d samples / %d sessions per offset)",
			r.TotalFrames, r.MaxFrameLen, MinSamplesPerOffset, MinSessionsPerOffset)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d constant byte position(s) — DPI fingerprint:", len(r.ConstantOffsets))
	for _, c := range r.ConstantOffsets {
		fmt.Fprintf(&b, " offset %d = 0x%02x (constant over %d frames across %d sessions);",
			c.Offset, c.Value, c.Samples, c.Sessions)
	}
	return false, b.String()
}

// EntropyOK reports whether the mean payload entropy clears the threshold with a
// large-enough sample.
func (r Report) EntropyOK() (bool, string) {
	if r.EntropyFrameCount < MinEntropyFrameSamples {
		return false, fmt.Sprintf("only %d frames >= %d bytes (need >= %d) — entropy sample too small; drive more bulk traffic",
			r.EntropyFrameCount, MinEntropyFrameLen, MinEntropyFrameSamples)
	}
	if r.MeanEntropy < MeanEntropyThreshold {
		return false, fmt.Sprintf("mean payload entropy %.4f bits/byte < %.2f threshold (over %d frames >= %d bytes)",
			r.MeanEntropy, MeanEntropyThreshold, r.EntropyFrameCount, MinEntropyFrameLen)
	}
	return true, fmt.Sprintf("mean payload entropy %.4f bits/byte >= %.2f (over %d frames >= %d bytes; max %.1f)",
		r.MeanEntropy, MeanEntropyThreshold, r.EntropyFrameCount, MinEntropyFrameLen, entropyMaxBitsPerByte)
}

// OK reports whether ALL three checks pass, joining each check's message.
func (r Report) OK() (bool, string) {
	sOK, sMsg := r.SessionsOK()
	cOK, cMsg := r.ConstantByteOK()
	eOK, eMsg := r.EntropyOK()
	msg := strings.Join([]string{sMsg, cMsg, eMsg}, "\n")
	return sOK && cOK && eOK, msg
}
