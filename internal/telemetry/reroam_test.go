package telemetry

import (
	"errors"
	"testing"
)

// TestReflectorReRoamSameSessionNoEpochReset is the T16 responder-side invariant: a
// per-path re-roam (the edge's public IP changed — a NAT rebind / CGNAT churn) is
// NOT a peer restart. The edge process did not restart, so its SessionID is
// unchanged and its ProbeSeq keeps climbing; only the source ADDRESS the probe
// arrives from is new (learned separately, in the Bind's handleInbound). The
// Reflector never sees that address — it authenticates the frame — so a re-roamed
// probe takes the ordinary within-session path: it is accepted, the anti-replay
// high-water CONTINUES to advance (no epoch reset), and a replayed pre-roam probe
// is still rejected. Contrast TestReflectorSessionEpochResetOnPeerRestart, where a
// NEW SessionID DOES reset the epoch. Distinguishing the two by SessionID is what
// stops a re-roam from being mistaken for a restart (which would needlessly reset
// the anti-replay window) and stops a restart from being masked by within-session
// replay rejection.
func TestReflectorReRoamSameSessionNoEpochReset(t *testing.T) {
	psk := testPSK(t, 0x16)
	r := NewReflector(psk, newTestRand())
	const (
		pathID = uint8(1)
		sess   = uint64(0x1616_1616_1616_1616) // the edge's per-boot session — UNCHANGED by a re-roam
	)

	// The edge's session is adopted and has advanced to some high-water.
	next := adoptSession(t, r, psk, pathID, sess, 0) // high-water at 1, next == 2
	if _, _, err := r.Reflect(encodeProbe(t, psk, pathID, sess, next, false, 0)); err != nil {
		t.Fatalf("pre-roam probe seq %d rejected: %v", next, err)
	}
	preRoamHighWater := next // this seq is now the high-water
	next++

	// --- re-roam: SAME SessionID, seq keeps climbing (the source address, which the
	// Reflector never inspects, is all that changed). It must be accepted as ordinary
	// within-session traffic, advancing the high-water — NO epoch reset. ---
	for i := 0; i < 3; i++ {
		if _, _, err := r.Reflect(encodeProbe(t, psk, pathID, sess, next, false, 0)); err != nil {
			t.Fatalf("re-roamed probe seq %d rejected — a same-session source change must not reset or stall the epoch: %v", next, err)
		}
		next++
	}

	// The anti-replay high-water CONTINUED across the re-roam: a replay of a pre-roam
	// probe is still rejected. Had the re-roam been mistaken for a restart and reset
	// the epoch, this stale seq would have been (wrongly) accepted.
	if _, _, err := r.Reflect(encodeProbe(t, psk, pathID, sess, preRoamHighWater, false, 0)); !errors.Is(err, ErrReplay) {
		t.Fatalf("pre-roam replay after re-roam: got %v, want ErrReplay (high-water must persist across a re-roam)", err)
	}

	// Sanity: a genuine restart (a DIFFERENT SessionID) still takes the epoch-reset
	// path via the challenge handshake — proving the two cases remain distinct.
	echo, _, err := r.Reflect(encodeProbe(t, psk, pathID, 0x9999_9999_9999_9999, 0, false, 0))
	if err != nil {
		t.Fatalf("restart bootstrap after re-roam rejected: %v", err)
	}
	ch := echoChallenge(t, psk, echo)
	if _, _, err := r.Reflect(encodeProbe(t, psk, pathID, 0x9999_9999_9999_9999, 1, false, ch)); err != nil {
		t.Fatalf("restart adoption after re-roam rejected: %v", err)
	}
}
