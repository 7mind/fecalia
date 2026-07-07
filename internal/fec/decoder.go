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

	// Discontinuity guard over the (UNAUTHENTICATED) wire GroupID, mirroring the
	// resequencer's suspect/corroborate design (internal/reseq/reseq.go). DATA/PARITY
	// frames are forgeable, so junk decodes as a valid kind ~1/256 carrying a
	// uniformly-random GroupID; a single such frame must NOT be trusted to move the
	// high-water arbitrarily (which would evict every live group and blackhole all
	// subsequent real groups as tooOld). A GroupID implausibly far from the high-water
	// is SUSPECT: it neither advances the high-water nor is buffered until
	// groupResyncCorroborate DISTINCT suspect ids mutually within one retain window
	// corroborate a genuine discontinuity (a peer encoder reopen resets nextGroup to 0,
	// emitting a connected run of low ids that corroborates; independent junk ids, each
	// uniform in 2^32, do not). resyncGroups holds the current run's distinct ids;
	// [resyncLo,resyncHi] is its span.
	resyncGroups []GroupID
	resyncLo     GroupID
	resyncHi     GroupID

	// recovered counts data shards reconstructed from parity (never directly
	// received); unrecoverable counts data shards a group lost for good — its
	// cardinality was known (a parity frame was seen) yet fewer than M shards
	// survived, so the group is evicted from the retain window still incomplete.
	// Both are cumulative over the decoder's life and read via Stats; the T24
	// datapath surfaces them on /metrics.
	recovered     uint64
	unrecoverable uint64
}

// GroupID discontinuity-guard tuning, mirroring reseq's resyncFactor/resyncCorroborate.
const (
	// groupResyncFactor bounds how far AHEAD of the high-water a single frame is
	// trusted to advance it. The encoder emits group ids strictly sequentially, so a
	// legitimate forward step is tiny (a handful, from fully-lost intervening groups);
	// a jump of groupResyncFactor retain-windows or more is SUSPECT and a single such
	// frame never advances the high-water. This is the group-space analogue of reseq's
	// forward-jump bound.
	groupResyncFactor = 4
	// groupResyncCorroborate is how many DISTINCT suspect group ids mutually within one
	// retain window must be observed before the high-water re-pins. A peer encoder
	// reopen (ids restart at 0) emits a connected run that corroborates after losing at
	// most groupResyncCorroborate-1 groups; uniformly-random junk ids never corroborate.
	groupResyncCorroborate = 3
)

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

// admitGroup decides whether a shard for group g may be processed, updating the
// high-water and eviction frontier from a TRUSTED view of the wire GroupID. It
// returns true when g is a plausible current/recent group (place the shard) and
// false when g is SUSPECT and dropped (a lone junk/forged id awaiting corroboration).
// A plausible forward step advances the high-water and evicts stale groups; an
// in-window straggler is processed without moving the frontier; an implausibly far
// id (a random junk group, or the low ids of a peer encoder reopen) is routed through
// the corroboration guard so no single frame can poison the decoder. Mirrors
// reseq.admit.
func (d *Decoder) admitGroup(g GroupID) bool {
	if !d.haveHighWater {
		d.highWater = g
		d.haveHighWater = true
		return true
	}
	ahead := int32(g - d.highWater) // >0 ahead, <0 behind, uint32-wraparound-safe
	if ahead > 0 {
		if d.retainWindow > 0 && uint32(ahead) >= uint32(groupResyncFactor*d.retainWindow) {
			// Implausibly far ahead: a single frame must not leap the frontier here (it
			// would evict every live group). Corroborate before trusting.
			return d.tryGroupResync(g)
		}
		// Plausible forward step: advance the frontier and evict groups outside the window.
		d.groupResyncReset()
		d.highWater = g
		d.evictStale()
		return true
	}
	// g at or behind the high-water.
	if d.tooOld(g) {
		// Far behind the window: an ordinary straggler is impossibly late, but a peer
		// encoder reopen (ids reset to 0) lands here — corroborate a genuine reset rather
		// than blackholing every post-reopen group as tooOld.
		return d.tryGroupResync(g)
	}
	d.groupResyncReset() // in-window near-current traffic breaks any suspect run
	return true
}

// evictStale drops every retained group that has fallen outside the retain window,
// accounting each as unrecoverable. Caller has just advanced the high-water.
func (d *Decoder) evictStale() {
	if d.retainWindow <= 0 {
		return
	}
	for id, gs := range d.groups {
		if d.tooOld(id) {
			d.accountEviction(gs)
			delete(d.groups, id)
		}
	}
}

// tryGroupResync feeds one suspect group id into the discontinuity guard, re-pinning
// the high-water ONLY after groupResyncCorroborate DISTINCT suspect ids mutually span
// less than one retain window (a peer reopen's connected low-id run corroborates;
// uniformly-random junk ids, each independent in 2^32, do not, and a single id
// re-delivered contributes only ONE distinct value). It returns true and performs the
// resync on the corroborating id (so the caller places it as the new frontier), false
// while still collecting. Mirrors reseq.tryResync.
func (d *Decoder) tryGroupResync(g GroupID) bool {
	if len(d.resyncGroups) == 0 || d.groupSpanExceeds(g) {
		d.resyncLo, d.resyncHi = g, g
		d.resyncGroups = append(d.resyncGroups[:0], g)
		return false
	}
	if d.groupRunContains(g) {
		return false // a re-delivered id is not independent corroboration
	}
	if g < d.resyncLo {
		d.resyncLo = g
	}
	if g > d.resyncHi {
		d.resyncHi = g
	}
	d.resyncGroups = append(d.resyncGroups, g)
	if len(d.resyncGroups) < groupResyncCorroborate {
		return false
	}
	d.groupResync(g)
	return true
}

// groupSpanExceeds reports whether adding g to the current suspect run would make its
// id span reach or exceed one retain window (so g does not corroborate the run).
func (d *Decoder) groupSpanExceeds(g GroupID) bool {
	if d.retainWindow <= 0 {
		return true // no window: nothing corroborates, so a junk id never resyncs
	}
	lo, hi := d.resyncLo, d.resyncHi
	if g < lo {
		lo = g
	}
	if g > hi {
		hi = g
	}
	return uint64(hi-lo) >= uint64(d.retainWindow)
}

// groupRunContains reports whether g is already one of the distinct suspect ids in the
// current run. The run holds at most groupResyncCorroborate ids, so this is O(C).
func (d *Decoder) groupRunContains(g GroupID) bool {
	for _, s := range d.resyncGroups {
		if s == g {
			return true
		}
	}
	return false
}

// groupResync re-pins the high-water to base after a corroborated discontinuity,
// discarding all buffered groups (they belong to the pre-discontinuity epoch). The
// discarded groups are NOT accounted unrecoverable: a peer reset (or a corroborated
// junk run) is not a repair failure, and counting it would inflate the /metrics loss.
func (d *Decoder) groupResync(base GroupID) {
	d.groups = make(map[GroupID]*groupState)
	d.highWater = base
	d.groupResyncReset()
}

// groupResyncReset abandons the current suspect run.
func (d *Decoder) groupResyncReset() {
	d.resyncGroups = d.resyncGroups[:0]
	d.resyncLo = 0
	d.resyncHi = 0
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
	if !d.admitGroup(g) {
		// SUSPECT wire GroupID (a lone junk/forged id, or a not-yet-corroborated
		// reopen): drop the shard without touching the frontier so one forged frame
		// cannot evict live groups or blackhole subsequent real ones.
		return nil, nil
	}

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
