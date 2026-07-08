package fec

import (
	"fmt"
	"time"

	"github.com/klauspost/reedsolomon"
)

// Encoder groups opaque DATA payloads into Reed-Solomon coding groups and emits
// parity. Two events close a group and produce its K parity shards:
//
//   - Admit fills the group to N data shards, or
//   - Tick observes that the grouping Deadline has elapsed since the group opened
//     (a partially-filled group is coded over its current M<N data shards).
//
// The Encoder is content-agnostic: it treats each payload as opaque bytes and
// never inspects the wrapped datagram. It is NOT safe for concurrent use; the
// datapath drives one Encoder from a single goroutine.
type Encoder struct {
	cfg   Config
	clock Clock

	// codecs caches one Reed-Solomon codec per (data-shard-count, parity-count)
	// pair. The data count varies with the group fill (full N, or a deadline-flushed
	// partial M in [1,N]); the parity count is cfg.ParityShards in fixed mode but
	// varies per group in [0,cfg.ParityShards] when the adaptive controller drives it
	// via SetParity (T29), so the key spans both dimensions.
	codecs map[codecKey]reedsolomon.Encoder

	// targetParity is the parity count the NEXT opened group will use. It is
	// cfg.ParityShards at construction (fixed-ratio behaviour) and is retargeted by
	// SetParity as the adaptive controller resizes redundancy (T29). It is read once
	// per group at openGroup and clamped to [0,cfg.ParityShards] there.
	targetParity int

	nextGroup GroupID

	// Open-group state. hasOpen is false between groups.
	hasOpen bool
	group   GroupID
	opened  time.Time
	// groupParity is the parity count fixed for the CURRENTLY-OPEN group: snapshotted
	// from targetParity at openGroup so a group's M cannot change once it has opened
	// (T29 — the decoder learns a group's data count from any surviving parity, and
	// reconstructs it against a ceiling-parity codec that is prefix-consistent with the
	// smaller per-group parity the encoder used). 0 means the open group emits no parity.
	groupParity int
	pending     [][]byte // admitted payloads (owned copies), one per data shard
}

// codecKey identifies a cached Reed-Solomon codec by its (data, parity) shard
// counts. Both vary at runtime — data with the group fill, parity with the
// adaptive controller — so the cache is keyed on the pair.
type codecKey struct {
	data   int
	parity int
}

// NewEncoder validates cfg and returns an Encoder driven by clock. It fails fast
// on an invalid configuration.
func NewEncoder(cfg Config, clock Clock) (*Encoder, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if clock == nil {
		return nil, fmt.Errorf("fec: clock must not be nil")
	}
	return &Encoder{
		cfg:          cfg,
		clock:        clock,
		codecs:       make(map[codecKey]reedsolomon.Encoder),
		targetParity: cfg.ParityShards,
	}, nil
}

// SetParity retargets the parity count applied to groups opened AFTER this call.
// The value is clamped to [0, cfg.ParityShards]: cfg.ParityShards is the fixed
// ceiling both ends agree on (the receiver's decoder is built at it), and 0 means
// subsequent groups emit no parity (redundancy fully shed). It does NOT resize the
// currently-open group — a group's parity is fixed at openGroup — so an in-flight
// group always decodes against the metadata it was coded with. The adaptive
// controller (internal/adaptivefec, T29) drives this; fixed-ratio callers never
// touch it. Like the rest of the Encoder it is NOT safe for concurrent use: the
// datapath serializes SetParity with Admit/Tick/Flush under its own send lock.
func (e *Encoder) SetParity(parity int) {
	if parity < 0 {
		parity = 0
	}
	if parity > e.cfg.ParityShards {
		parity = e.cfg.ParityShards
	}
	e.targetParity = parity
}

// Admit takes one opaque DATA payload into the current group and returns the
// DataShard the caller must transmit immediately. When this admission fills the
// group to N shards, the group is closed and its K ParityShards are returned;
// otherwise the parity slice is nil and the caller polls Tick to enforce the
// deadline. Admit copies payload, so the caller may reuse its buffer.
func (e *Encoder) Admit(payload []byte) (DataShard, []ParityShard, error) {
	if !e.hasOpen {
		e.openGroup()
	}
	idx := len(e.pending)
	buf := make([]byte, len(payload))
	copy(buf, payload)
	e.pending = append(e.pending, buf)

	ds := DataShard{Group: e.group, Index: idx, Payload: append([]byte(nil), buf...)}

	if len(e.pending) >= e.cfg.DataShards {
		parity, err := e.close()
		if err != nil {
			return DataShard{}, nil, err
		}
		return ds, parity, nil
	}
	return ds, nil, nil
}

// Tick enforces the grouping deadline against the injected clock. If an open
// group has existed for at least Deadline, it is closed and its K ParityShards
// returned (coded over the M data shards admitted so far). Otherwise it returns
// nil. Tick is how a partially-filled — or entirely idle-then-single-frame —
// group still gets its parity within the deadline. Poll it at least as often as
// Deadline; NextDeadline reports when the next Tick is due.
func (e *Encoder) Tick() ([]ParityShard, error) {
	if !e.hasOpen {
		return nil, nil
	}
	if e.clock.Now().Sub(e.opened) < e.cfg.Deadline {
		return nil, nil
	}
	return e.close()
}

// Flush closes the current open group unconditionally (e.g. on shutdown),
// returning its K ParityShards, or nil if no group is open. It ignores the
// deadline.
func (e *Encoder) Flush() ([]ParityShard, error) {
	if !e.hasOpen {
		return nil, nil
	}
	return e.close()
}

// NextDeadline reports the instant at which the current open group must be
// flushed, and whether a group is currently open. A scheduler uses it to arm a
// timer instead of busy-polling Tick.
func (e *Encoder) NextDeadline() (time.Time, bool) {
	if !e.hasOpen {
		return time.Time{}, false
	}
	return e.opened.Add(e.cfg.Deadline), true
}

func (e *Encoder) openGroup() {
	e.hasOpen = true
	e.group = e.nextGroup
	e.nextGroup++
	e.opened = e.clock.Now()
	e.groupParity = e.targetParity
	e.pending = e.pending[:0]
}

// close codes the open group's M pending data shards into K parity shards and
// resets open-group state. It never returns nil parity for a non-empty group.
func (e *Encoder) close() ([]ParityShard, error) {
	m := len(e.pending)
	if m == 0 {
		// Unreachable: a group only opens on Admit, so it always holds >= 1 shard.
		e.hasOpen = false
		return nil, nil
	}

	// A group opened with a zero parity target carries no redundancy: its data frames
	// were already emitted on admission, so close it without coding any parity. The
	// receiver never learns this group's cardinality (no parity frame) and simply
	// delivers whatever data survives — which is the intended "no FEC this group"
	// semantics the adaptive controller selects when loss is low (T29).
	k := e.groupParity
	if k == 0 {
		e.hasOpen = false
		return nil, nil
	}

	shardLen := lenPrefixLen
	for _, p := range e.pending {
		if l := lenPrefixLen + len(p); l > shardLen {
			shardLen = l
		}
	}

	codec, err := e.codec(m, k)
	if err != nil {
		return nil, err
	}

	shards := make([][]byte, m+k)
	for i, p := range e.pending {
		shards[i] = encodeDataShard(p, shardLen)
	}
	for j := 0; j < k; j++ {
		shards[m+j] = make([]byte, shardLen)
	}
	if err := codec.Encode(shards); err != nil {
		return nil, fmt.Errorf("fec: reed-solomon encode (m=%d k=%d len=%d): %w", m, k, shardLen, err)
	}

	group := e.group
	parity := make([]ParityShard, k)
	for j := 0; j < k; j++ {
		parity[j] = ParityShard{
			Group:     group,
			Index:     j,
			DataCount: m,
			Payload:   shards[m+j],
		}
	}

	e.hasOpen = false
	return parity, nil
}

// codec returns the cached Reed-Solomon codec for m data shards and k parity
// shards, building it once on first use. In fixed mode k is always cfg.ParityShards;
// in adaptive mode k varies per group in [1,cfg.ParityShards]. A group coded with
// k < cfg.ParityShards parity still decodes at the receiver, which is built at the
// cfg.ParityShards ceiling: klauspost's Vandermonde parity is prefix-consistent, so
// parity index j is identical whether the code is RS(m,k) or RS(m,ceiling).
//
// This prefix consistency is an UNDOCUMENTED property of reedsolomon's DEFAULT New()
// matrix (Vandermonde x top-inverse), NOT a public API guarantee (D25). It is PINNED
// two ways: the go.mod require note next to github.com/klauspost/reedsolomon, and
// TestKlauspostParityPrefixStableInvariant, which fails loudly if a library upgrade
// flips the default and would otherwise silently corrupt every recovered payload.
func (e *Encoder) codec(m, k int) (reedsolomon.Encoder, error) {
	key := codecKey{data: m, parity: k}
	if c, ok := e.codecs[key]; ok {
		return c, nil
	}
	c, err := reedsolomon.New(m, k)
	if err != nil {
		return nil, fmt.Errorf("fec: build reed-solomon codec (m=%d k=%d): %w", m, k, err)
	}
	e.codecs[key] = c
	return c, nil
}
