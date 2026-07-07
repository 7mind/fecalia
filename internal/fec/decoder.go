package fec

import (
	"fmt"
	"sort"

	"github.com/klauspost/reedsolomon"
)

// Decoder reassembles FEC groups from surviving shards and reconstructs data
// frames lost in transit. For any group that loses at most K of its M+K shards,
// the Decoder returns every data frame that was not directly received. It is
// content-agnostic and NOT safe for concurrent use.
//
// Recovery requires at least one surviving ParityShard, which conveys the group
// cardinality M and the uniform shard length. This is never a real limitation:
// whenever a DATA frame is actually lost, at least one of the K parity frames
// survives (a loss of D>=1 data frames within the <=K budget leaves K-(losses-D)
// >= 1 parity), so a group that needs recovery always has the metadata to do it.
// A group that loses only parity keeps all its data and needs no recovery.
type Decoder struct {
	cfg    Config
	codecs map[int]reedsolomon.Encoder
	groups map[GroupID]*groupState

	// retainWindow bounds memory: groupState for a group more than this many
	// GroupIDs behind the highest offered group is evicted (see tooOld). 0 disables
	// the window entirely, leaving eviction to explicit Forget calls.
	retainWindow  int
	highWater     GroupID
	haveHighWater bool

	// recovered counts data shards reconstructed from parity (never directly
	// received); unrecoverable counts data shards a group lost for good — its
	// cardinality was known (a parity frame was seen) yet fewer than M shards
	// survived, so the group is evicted from the retain window still incomplete.
	// Both are cumulative over the decoder's life and read via Stats; the T24
	// datapath surfaces them on /metrics.
	recovered     uint64
	unrecoverable uint64
}

// DecoderStats is a snapshot of the decoder's cumulative recovery counters.
type DecoderStats struct {
	// Recovered is the number of data shards reconstructed from parity.
	Recovered uint64
	// Unrecoverable is the number of data shards lost beyond repair capacity — the
	// per-group missing count of a group evicted from the retain window before it
	// could be completed (its M was known from a parity frame but < M shards
	// survived). A group whose parity was ALSO entirely lost (M never learned) is
	// not counted here: with no surviving parity the decoder has no information
	// about the group's cardinality, and the loss is accounted downstream by the
	// resequencer's gap timeout instead.
	Unrecoverable uint64
}

// Stats returns a snapshot of the decoder's cumulative recovery counters.
func (d *Decoder) Stats() DecoderStats {
	return DecoderStats{Recovered: d.recovered, Unrecoverable: d.unrecoverable}
}

// accountEviction folds a group being dropped from the retain window into the
// unrecoverable counter: a group whose cardinality M is known (a parity frame was
// seen) but is not yet done was lost for good — fewer than M of its M+K shards
// survived — so its still-missing data shards are counted unrecoverable. A done
// group (all data present or already reconstructed) and a group whose M was never
// learned contribute nothing. maybeReconstruct has already pruned any buffered data
// index >= M once M was known, so len(gs.data) counts only in-range survivors.
func (d *Decoder) accountEviction(gs *groupState) {
	if gs.done || gs.dataCount == -1 {
		return
	}
	if missing := gs.dataCount - len(gs.data); missing > 0 {
		d.unrecoverable += uint64(missing)
	}
}

// defaultRetainWindow bounds decoder memory out of the box: without eviction the
// groups map grows without bound — done groups retain buffers, and a group whose
// parity is entirely lost (M never learned) buffers its data forever, and GroupID
// (uint32) wraparound would eventually collide with stale retained state. The T24
// datapath tunes this window via SetRetainWindow to match its reordering/latency
// budget; the default merely guarantees the mechanism is on and memory is bounded.
const defaultRetainWindow = 4096

type groupState struct {
	dataCount int // M; -1 until a parity shard is seen
	shardLen  int // uniform shard length; -1 until a parity shard is seen
	data      map[int][]byte
	parity    map[int][]byte
	done      bool
}

// NewDecoder validates cfg and returns a Decoder. Only ParityShards (K) is read
// from cfg; the per-group data count M is carried on the wire.
func NewDecoder(cfg Config) (*Decoder, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &Decoder{
		cfg:          cfg,
		codecs:       make(map[int]reedsolomon.Encoder),
		groups:       make(map[GroupID]*groupState),
		retainWindow: defaultRetainWindow,
	}, nil
}

func (d *Decoder) state(g GroupID) *groupState {
	gs, ok := d.groups[g]
	if !ok {
		gs = &groupState{
			dataCount: -1,
			shardLen:  -1,
			data:      make(map[int][]byte),
			parity:    make(map[int][]byte),
		}
		d.groups[g] = gs
	}
	return gs
}

// markDone marks a group complete and releases its per-shard buffers: a done group
// never reconstructs again (Offer short-circuits on gs.done), so retaining its
// data/parity maps would only leak memory.
func (gs *groupState) markDone() {
	gs.done = true
	gs.data = nil
	gs.parity = nil
}

// Forget drops all retained state for a group, releasing its buffers. The T24
// datapath calls this once it knows no further shards for the group can arrive (or
// that the group is no longer worth recovering). Offering a shard for a forgotten,
// still-in-window group simply rebuilds fresh state.
func (d *Decoder) Forget(g GroupID) {
	delete(d.groups, g)
}

// SetRetainWindow configures the sliding retained-group window: groupState for a
// group more than n GroupIDs behind the highest offered group is evicted on the
// next advance of the high-water mark. n <= 0 disables the window (eviction then
// happens only via Forget). This is the eviction MECHANISM; the T24 datapath
// chooses the window/low-water POLICY to fit its reordering and latency budget.
func (d *Decoder) SetRetainWindow(n int) {
	d.retainWindow = n
}

// tooOld reports whether group g has fallen behind the high-water mark by more than
// the retained-group window. The comparison is uint32-wraparound-safe (it measures
// signed distance), so GroupID rollover does not resurrect stale groups.
func (d *Decoder) tooOld(g GroupID) bool {
	if d.retainWindow <= 0 || !d.haveHighWater {
		return false
	}
	behind := int32(d.highWater - g)
	return behind > int32(d.retainWindow)
}

// advanceHighWater folds g into the high-water mark and, when the mark actually
// advances, evicts every retained group that has fallen outside the window.
func (d *Decoder) advanceHighWater(g GroupID) {
	advanced := false
	if !d.haveHighWater {
		d.highWater = g
		d.haveHighWater = true
		advanced = true
	} else if int32(g-d.highWater) > 0 {
		d.highWater = g
		advanced = true
	}
	if !advanced || d.retainWindow <= 0 {
		return
	}
	for id, gs := range d.groups {
		if d.tooOld(id) {
			d.accountEviction(gs)
			delete(d.groups, id)
		}
	}
}

// Offer feeds one surviving shard into its group and returns any data payloads
// that became recoverable as a result — data frames that were NOT directly
// received but were reconstructed from parity. Directly-received data shards are
// never echoed back. It returns nil until the group has accumulated at least M
// shards (M learned from a parity shard) with at least one data frame missing; at
// that point every still-missing data index is reconstructed and returned once,
// and the group is marked complete. Offering to a completed group returns nil.
func (d *Decoder) Offer(s Shard) ([]Recovered, error) {
	g := s.GroupID()
	if d.tooOld(g) {
		return nil, nil // group already evicted; a very-late shard for it is unrecoverable by design
	}
	d.advanceHighWater(g)

	gs := d.state(g)
	if gs.done {
		return nil, nil
	}

	switch sh := s.(type) {
	case DataShard:
		// A data index can never be valid at or above maxShards-K: RS admits at most
		// maxShards total shards, so any group has m <= maxShards-K data shards and
		// valid indices 0..m-1. Rejecting an out-of-range index here — before it is
		// buffered — bounds a single group's data index space (a flood of distinct
		// bogus indices can no longer grow gs.data without bound) and stops one bogus
		// shard from being buffered to permanently poison the group.
		if sh.Index < 0 || sh.Index >= maxShards-d.cfg.ParityShards {
			return nil, fmt.Errorf("fec: data shard index %d out of range [0,%d) in group %d", sh.Index, maxShards-d.cfg.ParityShards, sh.Group)
		}
		buf := make([]byte, len(sh.Payload))
		copy(buf, sh.Payload)
		gs.data[sh.Index] = buf
	case ParityShard:
		if err := gs.observeParity(sh, d.cfg.ParityShards); err != nil {
			return nil, err
		}
	default:
		// Unreachable: Shard is a closed sum over DataShard and ParityShard.
		return nil, fmt.Errorf("fec: unknown shard type %T", s)
	}

	return d.maybeReconstruct(g, gs)
}

// observeParity records a parity shard and pins/checks the group's cardinality
// and shard geometry, failing fast on an inconsistent group.
func (gs *groupState) observeParity(p ParityShard, parityShards int) error {
	// A parity position is only ever 0..K-1. Rejecting an out-of-range index here —
	// before it is buffered — bounds gs.parity to at most K entries and keeps a bogus
	// index out of the group's state entirely.
	if p.Index < 0 || p.Index >= parityShards {
		return fmt.Errorf("fec: parity index %d out of range [0,%d) for group %d", p.Index, parityShards, p.Group)
	}
	if p.DataCount < 1 {
		return fmt.Errorf("fec: parity for group %d carries invalid DataCount %d", p.Group, p.DataCount)
	}
	// DataCount is the group cardinality m carried on the wire. RS admits m+K <=
	// maxShards, so m can never exceed maxShards-K. Reject an oversized DataCount
	// here, before maybeReconstruct runs its O(m) missing-index scan and allocates
	// an m+K shard table: a bogus DataCount near 2^31 would otherwise force a
	// multi-billion-iteration loop and a multi-gigabyte allocation before
	// reedsolomon.New rejected it.
	if maxData := maxShards - parityShards; p.DataCount > maxData {
		return fmt.Errorf("fec: parity for group %d carries DataCount %d exceeding max %d", p.Group, p.DataCount, maxData)
	}
	// A parity payload IS one RS shard, so its length is the group's uniform shard
	// length. Reject a payload too short to hold a data shard's length prefix:
	// trusting it would pin shardLen < lenPrefixLen and later panic encodeDataShard
	// (fec.go). The library must not trust wire-derived geometry.
	if len(p.Payload) < lenPrefixLen {
		return fmt.Errorf("fec: parity for group %d has payload %d bytes, shorter than the %d-byte length prefix", p.Group, len(p.Payload), lenPrefixLen)
	}
	if gs.dataCount == -1 {
		gs.dataCount = p.DataCount
	} else if gs.dataCount != p.DataCount {
		return fmt.Errorf("fec: inconsistent DataCount for group %d: %d vs %d", p.Group, gs.dataCount, p.DataCount)
	}
	if gs.shardLen == -1 {
		gs.shardLen = len(p.Payload)
	} else if gs.shardLen != len(p.Payload) {
		return fmt.Errorf("fec: inconsistent shard length for group %d: %d vs %d", p.Group, gs.shardLen, len(p.Payload))
	}
	buf := make([]byte, len(p.Payload))
	copy(buf, p.Payload)
	gs.parity[p.Index] = buf
	return nil
}

// maybeReconstruct attempts to complete a group once its cardinality is known.
func (d *Decoder) maybeReconstruct(g GroupID, gs *groupState) ([]Recovered, error) {
	if gs.dataCount == -1 {
		return nil, nil // M not yet known; keep buffering
	}
	m := gs.dataCount

	// A buffered data shard whose index is within the static wire bound (accepted at
	// Offer time, when m was not yet known) but >= this group's actual cardinality m
	// cannot belong to the group. Drop it rather than wedging the group: leaving it
	// buffered would make every reconstruct attempt fail on the stale out-of-range
	// index, so a later valid <=K shard set could never recover. Dropping it also
	// releases its buffer.
	for idx := range gs.data {
		if idx >= m {
			delete(gs.data, idx)
		}
	}

	missing := make([]int, 0)
	for i := 0; i < m; i++ {
		if _, ok := gs.data[i]; !ok {
			missing = append(missing, i)
		}
	}
	if len(missing) == 0 {
		gs.markDone() // all data present; nothing to recover
		return nil, nil
	}

	if len(gs.data)+len(gs.parity) < m {
		return nil, nil // fewer than M shards survive; recovery impossible so far
	}

	codec, err := d.codec(m)
	if err != nil {
		return nil, err
	}

	shards := make([][]byte, m+d.cfg.ParityShards)
	for idx, payload := range gs.data {
		// encodeDataShard's precondition is shardLen >= lenPrefixLen+len(payload).
		// A wire-derived data payload that overruns the group's shard length would
		// otherwise be silently truncated while its length prefix records the full
		// length, feeding RS inconsistent bytes and fabricating recovered payloads.
		// Fail fast instead of trusting the geometry.
		if lenPrefixLen+len(payload) > gs.shardLen {
			return nil, fmt.Errorf("fec: data shard %d payload (%d bytes) does not fit group %d shard length %d", idx, len(payload), g, gs.shardLen)
		}
		shards[idx] = encodeDataShard(payload, gs.shardLen)
	}
	for j, pb := range gs.parity {
		if j < 0 || j >= d.cfg.ParityShards {
			return nil, fmt.Errorf("fec: parity index %d out of range for group %d (K=%d)", j, g, d.cfg.ParityShards)
		}
		shards[m+j] = pb
	}

	if err := codec.Reconstruct(shards); err != nil {
		return nil, fmt.Errorf("fec: reed-solomon reconstruct group %d (m=%d k=%d): %w", g, m, d.cfg.ParityShards, err)
	}

	sort.Ints(missing)
	recovered := make([]Recovered, 0, len(missing))
	for _, idx := range missing {
		payload, err := decodeDataShard(shards[idx])
		if err != nil {
			return nil, fmt.Errorf("fec: decode reconstructed shard %d of group %d: %w", idx, g, err)
		}
		recovered = append(recovered, Recovered{Group: g, Index: idx, Payload: payload})
	}
	d.recovered += uint64(len(recovered))
	gs.markDone()
	return recovered, nil
}

// codec returns the cached Reed-Solomon codec for m data shards and K parity.
func (d *Decoder) codec(m int) (reedsolomon.Encoder, error) {
	if c, ok := d.codecs[m]; ok {
		return c, nil
	}
	c, err := reedsolomon.New(m, d.cfg.ParityShards)
	if err != nil {
		return nil, fmt.Errorf("fec: build reed-solomon codec (m=%d k=%d): %w", m, d.cfg.ParityShards, err)
	}
	d.codecs[m] = c
	return c, nil
}
