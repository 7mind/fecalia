package bond

import (
	"testing"
	"time"
)

func TestAttemptRetentionReleasesCongestionWindowBeforeLongRTO(t *testing.T) {
	now := time.Unix(100, 0)
	transport := New(Epoch{Boot: 1, Generation: 1})
	transport.SetRemote(Epoch{Boot: 2, Generation: 1}, true)
	transport.Path(0, 0, 60*time.Millisecond, now)
	if err := transport.Enqueue(make([]byte, 1200), now); err != nil {
		t.Fatal(err)
	}
	transport.Poll(now)
	path := transport.paths[0]
	if path.inflight == 0 {
		t.Fatal("reproduction requires an unacknowledged transmission")
	}
	// A transient router queue can push the RTO beyond the attempt-retention horizon.
	path.rttVariation = time.Second
	transport.Poll(now.Add(feedbackHorizon))
	if len(path.attempts) != 0 || path.inflight != 0 {
		t.Fatalf("retired attempts still occupy the congestion window: attempts=%d in_flight=%d", len(path.attempts), path.inflight)
	}
}

func TestRecentDataDelayIsNotLearnedAsIdleJitter(t *testing.T) {
	now := time.Unix(100, 0)
	a, b := New(Epoch{Boot: 1, Generation: 1}), New(Epoch{Boot: 2, Generation: 1})
	a.SetRemote(b.Epoch(), true)
	b.SetRemote(a.Epoch(), true)
	a.Path(0, 0, 60*time.Millisecond, now)
	b.Path(0, 0, 60*time.Millisecond, now)
	if err := a.Enqueue(make([]byte, 1200), now); err != nil {
		t.Fatal(err)
	}
	for _, tx := range a.Poll(now) {
		if _, err := b.Receive(tx.Path, tx.Frame, now.Add(300*time.Millisecond)); err != nil {
			t.Fatal(err)
		}
	}
	for _, tx := range b.Poll(now.Add(325 * time.Millisecond)) {
		if _, err := a.Receive(tx.Path, tx.Frame, now.Add(625*time.Millisecond)); err != nil {
			t.Fatal(err)
		}
	}
	if a.Snapshot(now).Paths[0].RTTVariation == 0 {
		t.Fatal("reproduction requires a delayed acknowledgement")
	}
	if got := a.Snapshot(now).Paths[0].IdleRTTVariation; got != 0 {
		t.Fatalf("recent data's queue delay was learned as idle jitter: %s", got)
	}
}

func TestIdleJitterDoesNotInheritLoadedRTTHistory(t *testing.T) {
	now := time.Unix(100, 0)
	a, b := New(Epoch{Boot: 1, Generation: 1}), New(Epoch{Boot: 2, Generation: 1})
	a.SetRemote(b.Epoch(), true)
	b.SetRemote(a.Epoch(), true)
	a.Path(0, 0, 60*time.Millisecond, now)
	b.Path(0, 0, 60*time.Millisecond, now)
	a.paths[0].rttVariation = 100 * time.Millisecond
	for _, tx := range a.Poll(now.Add(200 * time.Millisecond)) {
		if _, err := b.Receive(tx.Path, tx.Frame, now.Add(230*time.Millisecond)); err != nil {
			t.Fatal(err)
		}
	}
	for _, tx := range b.Poll(now.Add(255 * time.Millisecond)) {
		if _, err := a.Receive(tx.Path, tx.Frame, now.Add(285*time.Millisecond)); err != nil {
			t.Fatal(err)
		}
	}
	if a.paths[0].lastACK.IsZero() {
		t.Fatal("reproduction requires an idle acknowledgement")
	}
	if got := a.Snapshot(now).Paths[0].IdleRTTVariation; got != 0 {
		t.Fatalf("constant idle RTT inherited loaded variation: %s", got)
	}
}
