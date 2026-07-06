package fec

import (
	"bytes"
	"math/rand"
	"strings"
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

// TestDecoderRejectsOversizedDataPayload proves the maybeReconstruct oversized-data
// guard rejects a DataShard whose lenPrefixLen+len(Payload) exceeds the group shard
// length, which would otherwise be silently truncated by encodeDataShard, feed RS
// inconsistent bytes, and fabricate recovered payloads.
//
// The assertion is deliberately SPECIFIC to that guard's error ("does not fit ...
// shard length"). A weaker "err != nil" assertion is vacuous: on the pre-fix decoder
// the truncated shard still reconstructs, and decodeDataShard on the reconstructed
// garbage shard happens to return a DIFFERENT error ("length prefix ... overruns")
// incidentally — so the group is guarded by an accident downstream, not by the
// maybeReconstruct check. Matching the guard's own message discriminates the two:
// this test FAILS on the pre-fix decoder (which never emits that message) and PASSES
// on the fix.
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
	if !strings.Contains(err.Error(), "does not fit") || !strings.Contains(err.Error(), "shard length") {
		t.Fatalf("expected the maybeReconstruct oversized-data guard error (\"does not fit ... shard length\"), got a different (incidental) error: %v", err)
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

// TestDecoderBoundsPerGroupDataEntries proves a SINGLE group cannot buffer an
// unbounded number of distinct-index data entries: an index at or above maxShards-K
// can never belong to any group, so it is rejected at Offer time rather than
// buffered. The sliding window bounds the group COUNT; this bounds per-group state.
// On the pre-fix decoder (which only rejected negative indices) this flood would
// retain 100000 entries in one group; here it is capped at maxShards-K.
func TestDecoderBoundsPerGroupDataEntries(t *testing.T) {
	cfg := Config{DataShards: 4, ParityShards: 2, Deadline: testDeadline}
	d, err := NewDecoder(cfg)
	if err != nil {
		t.Fatal(err)
	}
	const flood = 100000
	rejected := 0
	for i := 0; i < flood; i++ {
		if _, err := d.Offer(DataShard{Group: 0, Index: i, Payload: []byte{1}}); err != nil {
			rejected++
		}
	}
	bound := maxShards - cfg.ParityShards
	gs, ok := d.groups[0]
	if !ok {
		t.Fatal("group 0 unexpectedly absent")
	}
	if len(gs.data) > bound {
		t.Fatalf("per-group data map holds %d entries, want <= %d (unbounded per-group memory)", len(gs.data), bound)
	}
	if rejected == 0 {
		t.Fatal("expected out-of-range indices to be rejected, none were")
	}
}

// TestDecoderBogusIndexDoesNotWedgeGroup proves that a bogus-index data shard does
// NOT permanently poison an otherwise-recoverable group. Two faults are covered:
//
//   - an out-of-range index (>= maxShards-K) is rejected at Offer time, never
//     buffered, so a subsequent valid <=K shard set still recovers; and
//   - an index within the static wire bound but >= this group's actual cardinality m
//     (only knowable once a parity pins m) is DROPPED by maybeReconstruct rather than
//     wedging the group — on the pre-fix decoder it returned "data shard index out of
//     range" on every reconstruct attempt and the missing data was never recovered.
func TestDecoderBogusIndexDoesNotWedgeGroup(t *testing.T) {
	cfg := Config{DataShards: 4, ParityShards: 2, Deadline: testDeadline}

	// recoverDropZero feeds data 1..3 + both parity (data 0 dropped) and returns the
	// recovered payload for index 0, or nil if the group failed to recover.
	recoverDropZero := func(t *testing.T, d *Decoder, g GroupID, data []DataShard, parity []ParityShard) []byte {
		t.Helper()
		var got []byte
		feed := []Shard{data[1], data[2], data[3], parity[0], parity[1]}
		for _, s := range feed {
			rec, err := d.Offer(s)
			if err != nil {
				t.Fatalf("offer %T: %v", s, err)
			}
			for _, r := range rec {
				if r.Index == 0 {
					got = r.Payload
				}
			}
		}
		return got
	}

	t.Run("out-of-range index rejected at offer, group still recovers", func(t *testing.T) {
		enc, err := NewEncoder(cfg, newFakeClock())
		if err != nil {
			t.Fatal(err)
		}
		rng := rand.New(rand.NewSource(7))
		originals := randomPayloads(rng, cfg.DataShards)
		data, parity := admitAll(t, enc, originals)
		g := data[0].Group

		d, err := NewDecoder(cfg)
		if err != nil {
			t.Fatal(err)
		}
		// maxShards-K = 254; 9999 can never be a valid data index.
		if _, err := d.Offer(DataShard{Group: g, Index: 9999, Payload: []byte{0xFF}}); err == nil {
			t.Fatal("expected out-of-range index 9999 to be rejected at Offer")
		}
		got := recoverDropZero(t, d, g, data, parity)
		if !bytes.Equal(got, originals[0]) {
			t.Fatalf("bogus offer wedged the group: want %x got %x", originals[0], got)
		}
	})

	t.Run("within-bound index >= m dropped, not wedged", func(t *testing.T) {
		enc, err := NewEncoder(cfg, newFakeClock())
		if err != nil {
			t.Fatal(err)
		}
		rng := rand.New(rand.NewSource(9))
		originals := randomPayloads(rng, cfg.DataShards)
		data, parity := admitAll(t, enc, originals)
		g := data[0].Group

		d, err := NewDecoder(cfg)
		if err != nil {
			t.Fatal(err)
		}
		// Index 200 is < maxShards-K (accepted at Offer, m not yet known) but >= the
		// real m=4 once parity pins it; maybeReconstruct must drop it, not wedge.
		if _, err := d.Offer(DataShard{Group: g, Index: 200, Payload: []byte{0xFF}}); err != nil {
			t.Fatalf("within-bound index 200 should be accepted at Offer: %v", err)
		}
		got := recoverDropZero(t, d, g, data, parity)
		if !bytes.Equal(got, originals[0]) {
			t.Fatalf("within-bound-but->=m shard wedged the group: want %x got %x", originals[0], got)
		}
	})
}

// TestDecoderRejectsOversizedDataCount proves observeParity bounds the group
// cardinality BEFORE maybeReconstruct's O(m) missing-index scan and m+K allocation:
// a ParityShard whose DataCount exceeds maxShards-K (here ~2^30) is rejected up
// front rather than forcing a multi-billion-iteration loop and multi-gigabyte
// allocation before reedsolomon.New would reject it.
func TestDecoderRejectsOversizedDataCount(t *testing.T) {
	cfg := Config{DataShards: 4, ParityShards: 2, Deadline: testDeadline}
	d, err := NewDecoder(cfg)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := d.Offer(ParityShard{Group: 0, Index: 0, DataCount: 1 << 30, Payload: make([]byte, 8)})
	if err == nil {
		t.Fatalf("expected error on oversized DataCount, got recovered=%v", rec)
	}
	if !strings.Contains(err.Error(), "DataCount") {
		t.Fatalf("expected a DataCount-bound error, got: %v", err)
	}
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
