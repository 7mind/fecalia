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
}

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
		cfg:    cfg,
		codecs: make(map[int]reedsolomon.Encoder),
		groups: make(map[GroupID]*groupState),
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

// Offer feeds one surviving shard into its group and returns any data payloads
// that became recoverable as a result — data frames that were NOT directly
// received but were reconstructed from parity. Directly-received data shards are
// never echoed back. It returns nil until the group has accumulated at least M
// shards (M learned from a parity shard) with at least one data frame missing; at
// that point every still-missing data index is reconstructed and returned once,
// and the group is marked complete. Offering to a completed group returns nil.
func (d *Decoder) Offer(s Shard) ([]Recovered, error) {
	gs := d.state(s.GroupID())
	if gs.done {
		return nil, nil
	}

	switch sh := s.(type) {
	case DataShard:
		if sh.Index < 0 {
			return nil, fmt.Errorf("fec: negative data shard index %d in group %d", sh.Index, sh.Group)
		}
		buf := make([]byte, len(sh.Payload))
		copy(buf, sh.Payload)
		gs.data[sh.Index] = buf
	case ParityShard:
		if err := gs.observeParity(sh); err != nil {
			return nil, err
		}
	default:
		// Unreachable: Shard is a closed sum over DataShard and ParityShard.
		return nil, fmt.Errorf("fec: unknown shard type %T", s)
	}

	return d.maybeReconstruct(s.GroupID(), gs)
}

// observeParity records a parity shard and pins/checks the group's cardinality
// and shard geometry, failing fast on an inconsistent group.
func (gs *groupState) observeParity(p ParityShard) error {
	if p.DataCount < 1 {
		return fmt.Errorf("fec: parity for group %d carries invalid DataCount %d", p.Group, p.DataCount)
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

	missing := make([]int, 0)
	for i := 0; i < m; i++ {
		if _, ok := gs.data[i]; !ok {
			missing = append(missing, i)
		}
	}
	if len(missing) == 0 {
		gs.done = true // all data present; nothing to recover
		return nil, nil
	}

	if len(gs.data)+len(gs.parity) < m {
		return nil, nil // fewer than M shards survive; recovery impossible so far
	}

	for idx := range gs.data {
		if idx >= m {
			return nil, fmt.Errorf("fec: data shard index %d out of range for group %d (M=%d)", idx, g, m)
		}
	}

	codec, err := d.codec(m)
	if err != nil {
		return nil, err
	}

	shards := make([][]byte, m+d.cfg.ParityShards)
	for idx, payload := range gs.data {
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
	gs.done = true
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
