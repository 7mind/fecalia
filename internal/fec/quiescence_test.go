package fec

import (
	"math/rand"
	"testing"
	"time"
)

// TestDecoderUnrecoverableQuiescenceAccurate is the D24 fix witness: an incomplete
// group whose cardinality is known but which lost more than K shards must be folded
// into Stats().Unrecoverable once its recovery deadline has definitively passed — even
// at QUIESCENCE, when no newer group is ever offered to advance the retain-window
// high-water. On the pre-fix decoder (which accounted unrecoverable ONLY on window
// eviction, and evicted only when a new group advanced the high-water) this counter
// stayed 0 forever at quiescence, so /metrics overstated recovery exactly after an
// incident. The fix also counts each group EXACTLY ONCE: a second scrape must not
// double-count, and the later window eviction must not re-count an already-accounted
// group.
func TestDecoderUnrecoverableQuiescenceAccurate(t *testing.T) {
	cfg := Config{DataShards: 4, ParityShards: 2, Deadline: testDeadline}
	d, err := NewDecoder(cfg)
	if err != nil {
		t.Fatal(err)
	}
	const window = 4
	d.SetRetainWindow(window)
	clk := newFakeClock()
	d.SetClock(clk)
	const recoveryDeadline = 500 * time.Millisecond
	d.SetRecoveryDeadline(recoveryDeadline)

	enc, err := NewEncoder(cfg, newFakeClock())
	if err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewSource(24))

	// Group 0 loses K+1 = 3 data shards: only 1 data + 2 parity = 3 < M=4 survive, so it
	// can never reconstruct. Its parity IS received, so M is known and the missing count
	// (3) is well-defined.
	data0, parity0 := admitAll(t, enc, randomPayloads(rng, cfg.DataShards))
	const droppedCount = 3
	for _, s := range allShards(data0, parity0) {
		if ds, ok := s.(DataShard); ok && ds.Index < droppedCount {
			continue
		}
		if _, err := d.Offer(s); err != nil {
			t.Fatalf("offer group 0: %v", err)
		}
	}

	// QUIESCENCE: no further groups are offered. Before the recovery deadline elapses,
	// the group might still, in principle, receive a late shard, so it is NOT counted.
	if got := d.Stats().Unrecoverable; got != 0 {
		t.Fatalf("before recovery deadline: unrecoverable = %d, want 0", got)
	}

	// The recovery deadline passes with the group still incomplete and NO new group to
	// trigger eviction. Stats must now fold the doomed group in.
	clk.advance(recoveryDeadline)
	if got := d.Stats().Unrecoverable; got != droppedCount {
		t.Fatalf("at quiescence after deadline: unrecoverable = %d, want %d (counter under-reports at quiescence)", got, droppedCount)
	}

	// Idempotent: a repeated scrape must not double-count the same group.
	if got := d.Stats().Unrecoverable; got != droppedCount {
		t.Fatalf("repeated scrape double-counted: unrecoverable = %d, want %d", got, droppedCount)
	}

	// The group is accounted WITHOUT being evicted from the reconstruction buffer.
	if _, ok := d.groups[0]; !ok {
		t.Fatal("group 0 must remain in the reconstruction buffer after quiescence accounting")
	}

	// Now offer enough newer groups to slide group 0 out of the retain window. Its
	// eviction must NOT count it a second time (each group counted exactly once). The
	// filler groups carry only data (M never learned), so they never contribute.
	for g := 1; g <= window+2; g++ {
		if _, err := d.Offer(DataShard{Group: GroupID(g), Index: 0, Payload: []byte{1}}); err != nil {
			t.Fatalf("offer filler %d: %v", g, err)
		}
	}
	if _, ok := d.groups[0]; ok {
		t.Fatal("group 0 was not evicted from the retain window")
	}
	if got := d.Stats().Unrecoverable; got != droppedCount {
		t.Fatalf("window eviction double-counted an already-accounted group: unrecoverable = %d, want %d", got, droppedCount)
	}
	if got := d.Stats().Recovered; got != 0 {
		t.Fatalf("recovered = %d, want 0 (loss exceeded budget)", got)
	}
}

// TestDecoderRecoveryDeadlineDisabledPreservesWindowAccounting asserts the D24
// snapshot-time accounting is OFF by default (recoveryDeadline == 0): with no deadline
// configured, a doomed group is counted only on window eviction, exactly the
// pre-existing behaviour, and a mid-life Stats scrape counts nothing early.
func TestDecoderRecoveryDeadlineDisabledPreservesWindowAccounting(t *testing.T) {
	cfg := Config{DataShards: 4, ParityShards: 2, Deadline: testDeadline}
	d, err := NewDecoder(cfg)
	if err != nil {
		t.Fatal(err)
	}
	const window = 4
	d.SetRetainWindow(window)
	// Deliberately DO NOT SetRecoveryDeadline: snapshot-time accounting stays disabled.
	clk := newFakeClock()
	d.SetClock(clk)

	enc, err := NewEncoder(cfg, newFakeClock())
	if err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewSource(25))
	data0, parity0 := admitAll(t, enc, randomPayloads(rng, cfg.DataShards))
	const droppedCount = 3
	for _, s := range allShards(data0, parity0) {
		if ds, ok := s.(DataShard); ok && ds.Index < droppedCount {
			continue
		}
		if _, err := d.Offer(s); err != nil {
			t.Fatalf("offer group 0: %v", err)
		}
	}

	// Even after arbitrary time passes, with accounting disabled nothing is counted at
	// quiescence.
	clk.advance(time.Hour)
	if got := d.Stats().Unrecoverable; got != 0 {
		t.Fatalf("accounting disabled but unrecoverable = %d, want 0 at quiescence", got)
	}

	// Eviction still accounts it (pure group-window behaviour preserved).
	for g := 1; g <= window+2; g++ {
		if _, err := d.Offer(DataShard{Group: GroupID(g), Index: 0, Payload: []byte{1}}); err != nil {
			t.Fatalf("offer filler %d: %v", g, err)
		}
	}
	if got := d.Stats().Unrecoverable; got != droppedCount {
		t.Fatalf("window eviction accounting: unrecoverable = %d, want %d", got, droppedCount)
	}
}
