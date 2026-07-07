package bind

import (
	"encoding/binary"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/7mind/wanbond/internal/fec"
)

// FEC datapath integration (T24).
//
// The fixed-ratio Reed-Solomon engine (internal/fec) is wired into the multipath
// datapath as a SEND-side encoder and a RECEIVE-side decoder that composes with the
// T18 resequencer, T21 weighted scheduling, and T30 dynamic paths without regressing
// any of them. FEC is OFF unless a config carries an [fec] block (NewMultipath's
// fecCfg is nil otherwise), in which case the datapath is byte-for-byte the pre-T24
// behaviour.
//
// Wire mapping. The FEC data-shard bytes are OuterSeq || inner: the encoder codes
// parity over that, but the 8-byte outer-seq prefix is NOT transmitted — the DATA
// frame carries the outer-seq in its header and the inner datagram in its payload,
// and both sender and receiver reconstitute the shard bytes as
// header-seq || frame-payload. This keeps a recovered shard SELF-DESCRIBING (its
// reconstructed bytes carry its own outer-seq, so a frame reconstructed from parity
// resequences at its ORIGINAL outer-seq even when EVERY data frame of the group was
// lost) while adding only one wire byte per DATA frame (the shard index in the DATA
// header) and the group cardinality M per PARITY frame. See internal/frame.
//
// Concurrency. The encoder rides the Send path, already serialized under m.mu, plus
// a single deadline-tick goroutine that TryLocks m.mu (never blocking, so it can
// never deadlock Close's readersWG.Wait held under m.mu — the tickLivenessFromReceive
// discipline). The decoder is fed from the per-path readLoop goroutines (many
// concurrent), so it is guarded by its own mutex, disjoint from m.mu and from the
// resequencer's — matching the resequencer's lock discipline (never held across a
// syscall). Recovered frames are handed to the resequencer exactly like a
// natively-received frame, so FEC recovery slots in strictly BEFORE resequencing.

// fecSeqPrefixLen is the width of the big-endian outer-seq prefix that heads each
// FEC data-shard's coded bytes. It is reconstructed from the DATA frame header on
// both ends and never travels the wire (see the wire mapping above).
const fecSeqPrefixLen = 8

// maxFECDeadline is the authoritative upper bound on the FEC group-close deadline,
// coupling it to the receive resequencer's per-gap timeout (T24, defect #4). A group
// flushed by the deadline emits its parity `deadline` after the group opened; the
// reconstructed frames must reach the resequencer BEFORE it skips the gap
// (resequencerTimeout after the gap forms), or recovery is structurally too late — the
// gap is skipped first and the recovered frame dropped as past the release point. Held
// at half the resequencer timeout, the deadline leaves the other half for propagation
// and reconstruction. config validation enforces a matching load-time bound; this is
// the authoritative guard for any fec.Config built directly.
const maxFECDeadline = resequencerTimeout / 2

// fecRetainGroups bounds the decoder's retained-group window (SetRetainWindow): a
// group more than this many group-ids behind the newest offered group is evicted,
// which is also when an incomplete group is finally accounted unrecoverable. It is
// sized well above the reorder/loss span a single group can straddle so a group is
// never evicted while its own shards are still legitimately in flight, yet small
// enough that an unrecoverable group is accounted promptly rather than after a
// 4096-group default. A group spans a handful of outer-seqs, so this comfortably
// exceeds the resequencerWindow's reordering budget in group units.
const fecRetainGroups = 512

// fecSender is the send-side FEC state: the group-forming encoder (accessed only
// under the Bind's m.mu, on the Send path and the deadline-tick goroutine) plus the
// parity-overhead counters the /metrics exposition reports. The counters are
// atomics read lock-free at snapshot time, mirroring the T23 per-path byte counters.
type fecSender struct {
	enc *fec.Encoder

	parityFrames atomic.Uint64
	parityBytes  atomic.Uint64
}

// fecReceiver is the receive-side FEC state: the recovery decoder guarded by its own
// mutex (the per-path readLoop goroutines feed it concurrently). deliveredRecovered is
// the HONEST recovery count for /metrics — data frames a reconstruction actually
// delivered to the resequencer AHEAD of its release point (not merely reconstructed:
// a frame rebuilt after the resequencer already skipped its gap is dropped as late and
// must not be counted, else /metrics overstates recovery). It is an atomic so the
// snapshot reads it lock-free.
type fecReceiver struct {
	mu  sync.Mutex
	dec *fec.Decoder

	deliveredRecovered atomic.Uint64
}

// offer feeds one surviving shard into the decoder and returns any data payloads it
// reconstructed (never-received frames). It holds the decoder mutex only for the
// in-memory reconstruct — never across a syscall — so it never blocks the send path.
func (r *fecReceiver) offer(s fec.Shard) ([]fec.Recovered, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.dec.Offer(s)
}

// stats snapshots the decoder's cumulative recovery counters under its mutex.
func (r *fecReceiver) stats() fec.DecoderStats {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.dec.Stats()
}

// FECStats is a consistent snapshot of the Bind's FEC counters (T24), the shape the
// metrics.Source adapter maps onto the connection-scoped /metrics FEC series.
// ParityFrames/ParityBytes are the send-side overhead (parity emitted); Recovered and
// Unrecoverable are the receive-side outcome (data shards reconstructed from parity,
// and data shards lost beyond repair capacity). FEC being connection-scoped, these
// carry no path label.
type FECStats struct {
	ParityFrames  uint64
	ParityBytes   uint64
	Recovered     uint64
	Unrecoverable uint64
}

// fecShardPayload builds the FEC data-shard coded bytes for outer-seq seq over the
// opaque inner datagram: seq (8 BE) || inner. The sender codes parity over this; the
// receiver reconstitutes the identical bytes from the DATA frame header + payload so
// the decoder's shards match what the encoder coded.
func fecShardPayload(seq uint64, inner []byte) []byte {
	b := make([]byte, fecSeqPrefixLen+len(inner))
	binary.BigEndian.PutUint64(b, seq)
	copy(b[fecSeqPrefixLen:], inner)
	return b
}

// splitFECShardPayload inverts fecShardPayload, returning the outer-seq and a fresh
// copy of the inner datagram. It fails fast on a shard too short to hold the prefix,
// which would indicate a decoder/encoder mismatch rather than a recoverable
// condition. The inner copy lets the resequencer take ownership without retaining the
// larger coded-shard backing array.
func splitFECShardPayload(b []byte) (uint64, []byte, error) {
	if len(b) < fecSeqPrefixLen {
		return 0, nil, fmt.Errorf("bind: recovered FEC shard %d bytes, shorter than the %d-byte outer-seq prefix", len(b), fecSeqPrefixLen)
	}
	seq := binary.BigEndian.Uint64(b[:fecSeqPrefixLen])
	inner := make([]byte, len(b)-fecSeqPrefixLen)
	copy(inner, b[fecSeqPrefixLen:])
	return seq, inner, nil
}
