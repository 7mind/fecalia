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

	// codecs caches one Reed-Solomon codec per data-shard count. A full group
	// always uses N; deadline-flushed partial groups use M in [1,N], so at most N
	// distinct codecs are ever built.
	codecs map[int]reedsolomon.Encoder

	nextGroup GroupID

	// Open-group state. hasOpen is false between groups.
	hasOpen bool
	group   GroupID
	opened  time.Time
	pending [][]byte // admitted payloads (owned copies), one per data shard
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
		cfg:    cfg,
		clock:  clock,
		codecs: make(map[int]reedsolomon.Encoder),
	}, nil
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

	shardLen := lenPrefixLen
	for _, p := range e.pending {
		if l := lenPrefixLen + len(p); l > shardLen {
			shardLen = l
		}
	}

	codec, err := e.codec(m)
	if err != nil {
		return nil, err
	}

	shards := make([][]byte, m+e.cfg.ParityShards)
	for i, p := range e.pending {
		shards[i] = encodeDataShard(p, shardLen)
	}
	for j := 0; j < e.cfg.ParityShards; j++ {
		shards[m+j] = make([]byte, shardLen)
	}
	if err := codec.Encode(shards); err != nil {
		return nil, fmt.Errorf("fec: reed-solomon encode (m=%d k=%d len=%d): %w", m, e.cfg.ParityShards, shardLen, err)
	}

	group := e.group
	parity := make([]ParityShard, e.cfg.ParityShards)
	for j := 0; j < e.cfg.ParityShards; j++ {
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

// codec returns the cached Reed-Solomon codec for m data shards and the
// configured K parity shards, building it once on first use.
func (e *Encoder) codec(m int) (reedsolomon.Encoder, error) {
	if c, ok := e.codecs[m]; ok {
		return c, nil
	}
	c, err := reedsolomon.New(m, e.cfg.ParityShards)
	if err != nil {
		return nil, fmt.Errorf("fec: build reed-solomon codec (m=%d k=%d): %w", m, e.cfg.ParityShards, err)
	}
	e.codecs[m] = c
	return c, nil
}
