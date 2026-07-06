package fec

import (
	"bytes"
	"math/rand"
	"testing"
)

// TestDecoderRejectsShortParityPayload proves the decoder no longer trusts
// wire-derived geometry: a ParityShard whose Payload is shorter than the 4-byte
// length prefix is rejected with an error rather than pinning shardLen < 4 and
// panicking encodeDataShard during reconstruction (the reviewer's repro (a)).
func TestDecoderRejectsShortParityPayload(t *testing.T) {
	cfg := Config{DataShards: 2, ParityShards: 1, Deadline: testDeadline}
	d, err := NewDecoder(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// One data present, one missing — the exact precondition that used to panic.
	if _, err := d.Offer(DataShard{Group: 0, Index: 0, Payload: []byte("hi")}); err != nil {
		t.Fatalf("data offer: %v", err)
	}
	rec, err := d.Offer(ParityShard{Group: 0, Index: 0, DataCount: 2, Payload: []byte{0, 0}})
	if err == nil {
		t.Fatalf("expected error on short parity payload, got recovered=%v", rec)
	}
	if rec != nil {
		t.Fatalf("expected no recovered payloads on malformed parity, got %v", rec)
	}
}

// TestDecoderRejectsOversizedDataPayload proves the decoder fails fast rather than
// silently truncating a DataShard whose lenPrefixLen+len(Payload) exceeds the
// group shard length, which would otherwise feed RS inconsistent bytes and
// fabricate recovered payloads (the reviewer's repro (b)).
func TestDecoderRejectsOversizedDataPayload(t *testing.T) {
	cfg := Config{DataShards: 2, ParityShards: 1, Deadline: testDeadline}
	d, err := NewDecoder(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Data payload (20 bytes) cannot fit the shardLen=8 the parity establishes.
	if _, err := d.Offer(DataShard{Group: 0, Index: 0, Payload: make([]byte, 20)}); err != nil {
		t.Fatalf("data offer: %v", err)
	}
	rec, err := d.Offer(ParityShard{Group: 0, Index: 0, DataCount: 2, Payload: make([]byte, 8)})
	if err == nil {
		t.Fatalf("expected error on oversized data payload, got recovered=%v", rec)
	}
	if rec != nil {
		t.Fatalf("expected no fabricated payloads on oversized data, got %v", rec)
	}
}

// TestDecoderRejectsInconsistentParityGeometry checks the cross-shard geometry
// invariant: two parity shards for one group that disagree on M or on shard length
// are rejected, never merged into a corrupt group.
func TestDecoderRejectsInconsistentParityGeometry(t *testing.T) {
	cfg := Config{DataShards: 4, ParityShards: 3, Deadline: testDeadline}

	t.Run("DataCount", func(t *testing.T) {
		d, err := NewDecoder(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := d.Offer(ParityShard{Group: 0, Index: 0, DataCount: 3, Payload: make([]byte, 8)}); err != nil {
			t.Fatalf("first parity: %v", err)
		}
		if _, err := d.Offer(ParityShard{Group: 0, Index: 1, DataCount: 4, Payload: make([]byte, 8)}); err == nil {
			t.Fatal("expected error on inconsistent DataCount")
		}
	})

	t.Run("ShardLen", func(t *testing.T) {
		d, err := NewDecoder(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := d.Offer(ParityShard{Group: 0, Index: 0, DataCount: 3, Payload: make([]byte, 8)}); err != nil {
			t.Fatalf("first parity: %v", err)
		}
		if _, err := d.Offer(ParityShard{Group: 0, Index: 1, DataCount: 3, Payload: make([]byte, 12)}); err == nil {
			t.Fatal("expected error on inconsistent shard length")
		}
	})
}

// TestDecoderReleasesDoneGroupBuffers asserts that once a group completes its
// data/parity buffers are released, so a long-lived decoder does not retain shard
// bytes for every group it has ever finished.
func TestDecoderReleasesDoneGroupBuffers(t *testing.T) {
	cfg := Config{DataShards: 3, ParityShards: 2, Deadline: testDeadline}
	enc, err := NewEncoder(cfg, newFakeClock())
	if err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewSource(11))
	originals := randomPayloads(rng, cfg.DataShards)
	data, parity := admitAll(t, enc, originals)

	d, err := NewDecoder(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Drop data index 0; feed the rest so the group reconstructs and completes.
	got := make(map[int][]byte)
	for _, s := range append([]Shard{data[1], data[2]}, parity[0], parity[1]) {
		if ds, ok := s.(DataShard); ok {
			got[ds.Index] = ds.Payload
		}
		rec, err := d.Offer(s)
		if err != nil {
			t.Fatalf("offer: %v", err)
		}
		for _, r := range rec {
			got[r.Index] = r.Payload
		}
	}
	if !bytes.Equal(got[0], originals[0]) {
		t.Fatalf("did not recover data 0: want %x got %x", originals[0], got[0])
	}

	gs, ok := d.groups[0]
	if !ok {
		t.Fatal("group state unexpectedly evicted")
	}
	if !gs.done {
		t.Fatal("group not marked done after full recovery")
	}
	if gs.data != nil || gs.parity != nil {
		t.Fatalf("done group retained buffers: data=%v parity=%v", gs.data, gs.parity)
	}
}

// TestDecoderEvictsStaleGroups asserts the sliding-window mechanism bounds memory:
// groups (here permanently stuck, since only a data shard ever arrives and M is
// never learned) beyond the retained-group window are evicted rather than buffered
// forever.
func TestDecoderEvictsStaleGroups(t *testing.T) {
	cfg := Config{DataShards: 4, ParityShards: 2, Deadline: testDeadline}
	d, err := NewDecoder(cfg)
	if err != nil {
		t.Fatal(err)
	}
	const window = 4
	d.SetRetainWindow(window)

	const groups = 100
	for g := 0; g < groups; g++ {
		// Only a data shard: M is never learned, so this group would buffer forever
		// without eviction.
		if _, err := d.Offer(DataShard{Group: GroupID(g), Index: 0, Payload: []byte{1}}); err != nil {
			t.Fatalf("offer group %d: %v", g, err)
		}
	}
	if len(d.groups) > window+1 {
		t.Fatalf("retained %d groups, want <= %d (memory unbounded)", len(d.groups), window+1)
	}
	if _, ok := d.groups[0]; ok {
		t.Fatal("stale group 0 was not evicted")
	}
	// The most recent group is retained.
	if _, ok := d.groups[GroupID(groups-1)]; !ok {
		t.Fatal("most recent group was evicted")
	}
}

// TestDecoderForget asserts the explicit eviction mechanism releases a group.
func TestDecoderForget(t *testing.T) {
	cfg := Config{DataShards: 4, ParityShards: 2, Deadline: testDeadline}
	d, err := NewDecoder(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Offer(DataShard{Group: 7, Index: 0, Payload: []byte{1}}); err != nil {
		t.Fatalf("offer: %v", err)
	}
	if _, ok := d.groups[7]; !ok {
		t.Fatal("group 7 not buffered")
	}
	d.Forget(7)
	if _, ok := d.groups[7]; ok {
		t.Fatal("group 7 still present after Forget")
	}
}

// TestDecoderEvictionWraparoundSafe asserts the window comparison is
// uint32-wraparound-safe: a group at the top of the GroupID space is correctly
// treated as OLD once wrapped-around ids advance past it — a naive unsigned
// compare would misjudge it as newest and never evict, colliding with stale state.
func TestDecoderEvictionWraparoundSafe(t *testing.T) {
	cfg := Config{DataShards: 4, ParityShards: 2, Deadline: testDeadline}
	d, err := NewDecoder(cfg)
	if err != nil {
		t.Fatal(err)
	}
	const window = 4
	d.SetRetainWindow(window)

	const maxID = GroupID(0xFFFFFFFF)
	if _, err := d.Offer(DataShard{Group: maxID, Index: 0, Payload: []byte{1}}); err != nil {
		t.Fatalf("offer maxID: %v", err)
	}
	// Wrapped ids 0..5 advance the high-water mark past the window ahead of maxID.
	for g := GroupID(0); g <= 5; g++ {
		if _, err := d.Offer(DataShard{Group: g, Index: 0, Payload: []byte{1}}); err != nil {
			t.Fatalf("offer %d: %v", g, err)
		}
	}
	if _, ok := d.groups[maxID]; ok {
		t.Fatal("wrapped-around stale group 0xFFFFFFFF was not evicted (unsigned compare bug)")
	}
	if d.tooOld(maxID) != true {
		t.Fatal("tooOld should report the wrapped-around group as old")
	}
}
