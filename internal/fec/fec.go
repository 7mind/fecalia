package fec

import (
	"encoding/binary"
	"fmt"
	"time"
)

// maxShards is the Reed-Solomon field limit: RS over GF(2^8) admits at most 256
// shards total (data + parity) per coding group.
const maxShards = 256

// lenPrefixLen is the width of the big-endian length header prepended to each
// data payload before it is padded to the group's uniform shard length. It lets
// the decoder recover the exact original byte length after Reed-Solomon
// reconstruction, since RS operates only on fixed-length shards. A uint32 prefix
// covers any datagram this codec will ever wrap.
const lenPrefixLen = 4

// Config parameterizes a Reed-Solomon FEC group: DataShards (N) opaque DATA
// frames are grouped and protected by ParityShards (K) parity frames, so the
// receiver recovers up to K arbitrary losses per group. Deadline bounds grouping
// latency: a group that does not fill to N within Deadline is flushed early over
// however many data frames it holds, so the parity is never delayed unboundedly
// waiting for a group to fill.
type Config struct {
	// DataShards is N, the maximum number of DATA frames grouped before parity is
	// emitted. Must be >= 1.
	DataShards int
	// ParityShards is K, the number of parity frames emitted per group and the
	// maximum number of per-group losses the receiver can recover. Must be >= 1.
	ParityShards int
	// Deadline bounds the grouping latency: an open, partially-filled group is
	// flushed (parity emitted over its current data frames) once this much time
	// has elapsed since its first frame. Must be > 0.
	Deadline time.Duration
}

func (c Config) validate() error {
	if c.DataShards < 1 {
		return fmt.Errorf("fec: DataShards must be >= 1, got %d", c.DataShards)
	}
	if c.ParityShards < 1 {
		return fmt.Errorf("fec: ParityShards must be >= 1, got %d", c.ParityShards)
	}
	if c.DataShards+c.ParityShards > maxShards {
		return fmt.Errorf("fec: DataShards+ParityShards must be <= %d, got %d", maxShards, c.DataShards+c.ParityShards)
	}
	if c.Deadline <= 0 {
		return fmt.Errorf("fec: Deadline must be > 0, got %s", c.Deadline)
	}
	return nil
}

// GroupID identifies one FEC coding group. The encoder assigns group ids
// monotonically; the decoder buckets received shards by this id.
type GroupID uint32

// Shard is the closed sum over the two kinds of frame an FEC group emits: a
// DataShard wrapping one opaque payload, and a ParityShard carrying redundancy.
// The unexported marker prevents other packages from introducing new kinds.
type Shard interface {
	// GroupID returns the coding group this shard belongs to.
	GroupID() GroupID
	isShard()
}

// DataShard is one opaque DATA payload admitted into a group, tagged with its
// coding coordinates so the decoder can place it. It carries no group cardinality
// (M), because a data frame is emitted immediately on admission — before the
// group's final size is known — to keep datapath latency low; the decoder learns
// M from any ParityShard of the group.
type DataShard struct {
	Group   GroupID
	Index   int    // position within the group, 0..M-1
	Payload []byte // the opaque original bytes (never inspected)
}

// GroupID returns the shard's coding group.
func (d DataShard) GroupID() GroupID { return d.Group }
func (DataShard) isShard()           {}

// ParityShard is one Reed-Solomon parity frame for a group. It carries DataCount
// (M) and its Payload length equals the group's uniform shard length, so a single
// received parity frame tells the decoder both the group cardinality and the
// shard geometry it needs to reconstruct losses.
type ParityShard struct {
	Group     GroupID
	Index     int    // parity position, 0..K-1; RS shard index is DataCount+Index
	DataCount int    // M: number of data shards this parity protects
	Payload   []byte // parity bytes; len == group shard length
}

// GroupID returns the shard's coding group.
func (p ParityShard) GroupID() GroupID { return p.Group }
func (ParityShard) isShard()           {}

// Recovered is a data payload the decoder reconstructed from parity — one that
// was NOT directly received. Directly-received data frames are never echoed back
// as Recovered; the caller already holds them.
type Recovered struct {
	Group   GroupID
	Index   int
	Payload []byte
}

// encodeDataShard renders one payload as a fixed-length RS shard: a big-endian
// uint32 length prefix followed by the payload, right-padded with zeros to
// shardLen. shardLen is guaranteed >= lenPrefixLen+len(payload) by the caller.
func encodeDataShard(payload []byte, shardLen int) []byte {
	s := make([]byte, shardLen)
	binary.BigEndian.PutUint32(s, uint32(len(payload)))
	copy(s[lenPrefixLen:], payload)
	return s
}

// decodeDataShard inverts encodeDataShard, returning the exact original payload.
// It fails fast on a length prefix that overruns the shard, which would indicate
// corruption or a decoder/encoder mismatch rather than a recoverable condition.
func decodeDataShard(shard []byte) ([]byte, error) {
	if len(shard) < lenPrefixLen {
		return nil, fmt.Errorf("fec: shard too short for length prefix: %d bytes", len(shard))
	}
	n := int(binary.BigEndian.Uint32(shard[:lenPrefixLen]))
	if n > len(shard)-lenPrefixLen {
		return nil, fmt.Errorf("fec: shard length prefix %d overruns shard payload %d", n, len(shard)-lenPrefixLen)
	}
	out := make([]byte, n)
	copy(out, shard[lenPrefixLen:lenPrefixLen+n])
	return out, nil
}
