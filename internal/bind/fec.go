package bind

import (
	"encoding/binary"
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/7mind/wanbond/internal/adaptivefec"
	"github.com/7mind/wanbond/internal/fec"
	"github.com/7mind/wanbond/internal/telemetry"
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

// fecResidualLossWindow is the trailing outer-seq width of the receive-side residual-
// loss estimator (T29): the post-FEC-recovery loss the P4 acceptance bounds. It is
// deliberately WIDER than the telemetry default (512) for two reasons: (1) resolution —
// the P4 bound is 0.5%, so the window must resolve finer than 1/512≈0.2%; 8192 gives
// ~0.012% granularity, well under the bound; (2) stability — averaging the residual over
// ~thousands of groups smooths the per-group binomial variance into a steady-state figure.
// It stays far below the resequencer window's reordering budget, and is wide enough that a
// recovered frame arriving a group or two after its gap still retroactively fills it, so
// genuinely-unrecovered seqs (whole-group and beyond-budget losses) are the only residual.
const fecResidualLossWindow = 8192

// adaptiveControlInterval throttles how often the FEC tick loop folds a fresh loss
// sample into the adaptive controller (T29). It matches the probe cadence — the rate at
// which per-path loss estimates actually refresh — so the controller's EWMA sees ~one
// sample per probe interval as its tuning (internal/adaptivefec) assumes, regardless of
// the (possibly much smaller) FEC group-close deadline the tick loop runs at.
const adaptiveControlInterval = telemetry.DefaultProbeInterval

// fecSender is the send-side FEC state: the group-forming encoder (accessed only
// under the Bind's m.mu, on the Send path and the deadline-tick goroutine) plus the
// parity-overhead counters the /metrics exposition reports. The counters are
// atomics read lock-free at snapshot time, mirroring the T23 per-path byte counters.
type fecSender struct {
	enc *fec.Encoder

	// ctrl is the adaptive-FEC controller (T29), nil in fixed-ratio mode. It is driven
	// (Observe) and read (Parity) ONLY from under the Bind's m.mu — the FEC tick loop's
	// throttled drive and, transitively, the Send path — so it needs no lock of its own,
	// matching the encoder's single-writer discipline. lastControlTick/haveControlTick
	// throttle the drive to adaptiveControlInterval and are likewise m.mu-guarded. They are
	// stamped ONLY on the Observe branch (an eligible sample was folded in), never on the
	// no-eligible-path hold — a hold does not consume the interval (D97), so the drive stays
	// admitted each tick until an eligible path returns. The interval is measured against the
	// bind's injected clock (Multipath.clock), not the wall clock.
	ctrl            *adaptivefec.Controller
	lastControlTick time.Time
	haveControlTick bool

	// adaptive{Parity,SmoothedLoss,EligibleLoss,EligiblePaths} publish the controller's
	// most recent drive decision for the lock-free FEC snapshot (T263). They are written
	// ONLY at the single serialized drive locus (driveAdaptiveControllerLocked, under
	// m.mu) and read lock-free at scrape time — the same discipline as
	// fecReceiver.deliveredRecovered. The two float64 signals are stored as their IEEE-754
	// bit patterns (math.Float64bits) so an atomic.Uint64 carries them. They are meaningful
	// only in adaptive mode (ctrl != nil); a fixed-ratio peer never drives the controller
	// and reports FECStats.Adaptive == nil.
	adaptiveParity        atomic.Int64  // target parity M (ctrl.Parity())
	adaptiveSmoothedLoss  atomic.Uint64 // EWMA smoothed loss, math.Float64bits
	adaptiveEligibleLoss  atomic.Uint64 // max eligible-path loss, math.Float64bits
	adaptiveEligiblePaths atomic.Int64  // count of eligible (StateUp+probed) paths

	dataFrames   atomic.Uint64
	dataBytes    atomic.Uint64
	parityFrames atomic.Uint64
	parityBytes  atomic.Uint64
}

// publishAdaptiveDrive records a completed controller drive into the lock-free snapshot
// fields: the retargeted parity M and EWMA smoothed loss alongside the eligible-path
// signal that drove them. Called under m.mu from driveAdaptiveControllerLocked after
// Observe/SetParity.
func (fs *fecSender) publishAdaptiveDrive(parity int, smoothedLoss, eligibleLoss float64, eligiblePaths int) {
	fs.adaptiveParity.Store(int64(parity))
	fs.adaptiveSmoothedLoss.Store(math.Float64bits(smoothedLoss))
	fs.adaptiveEligibleLoss.Store(math.Float64bits(eligibleLoss))
	fs.adaptiveEligiblePaths.Store(int64(eligiblePaths))
}

// publishAdaptiveEligible records ONLY the eligible-path signal, leaving the held
// parity/smoothed-loss decision untouched. It is the 'no eligible path' hold branch's
// publish (eligibleLoss/eligiblePaths both 0): the controller did not Observe, so its
// last driven parity and smoothed loss must NOT be clobbered.
func (fs *fecSender) publishAdaptiveEligible(eligibleLoss float64, eligiblePaths int) {
	fs.adaptiveEligibleLoss.Store(math.Float64bits(eligibleLoss))
	fs.adaptiveEligiblePaths.Store(int64(eligiblePaths))
}

// adaptiveSnapshot reads the published controller decision lock-free into an
// AdaptiveFECStats. The caller has already established this is an adaptive-mode sender
// (ctrl != nil) before fabricating a series.
func (fs *fecSender) adaptiveSnapshot() AdaptiveFECStats {
	return AdaptiveFECStats{
		Parity:        int(fs.adaptiveParity.Load()),
		SmoothedLoss:  math.Float64frombits(fs.adaptiveSmoothedLoss.Load()),
		EligibleLoss:  math.Float64frombits(fs.adaptiveEligibleLoss.Load()),
		EligiblePaths: int(fs.adaptiveEligiblePaths.Load()),
	}
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

	// connLoss is the POST-FEC-RECOVERY residual-loss estimator (T29, the P4 signal):
	// every natively-received DATA outer-seq AND every FEC-reconstructed outer-seq is
	// Observed into it, so its Loss() fraction is exactly the connection-scoped loss that
	// survived FEC — outer-seqs neither received nor recovered. It is internally
	// synchronized (its own mutex), fed concurrently by the per-path readLoop goroutines
	// exactly like the resequencer, and read lock-free-enough at scrape time.
	connLoss *telemetry.ConnLoss
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
// DataFrames is the send-side DATA-frame count (the fixed-ratio overhead's
// denominator); ParityFrames/ParityBytes are the send-side overhead (parity emitted);
// Recovered and Unrecoverable are the receive-side outcome (data shards reconstructed
// from parity, and data shards lost beyond repair capacity). FEC being
// connection-scoped, these carry no path label. The frame overhead the operator cares
// about is ParityFrames/DataFrames, which tends to ParityShards/DataShards (M/K) once
// groups fill; both are counted only for frames that actually reached the socket, so
// the ratio reflects wire cost actually spent.
type FECStats struct {
	DataFrames    uint64
	DataBytes     uint64
	ParityFrames  uint64
	ParityBytes   uint64
	Recovered     uint64
	Unrecoverable uint64
	// ResidualLoss is the current post-FEC-recovery connection loss fraction in [0,1]
	// (T29): the share of outer-seqs neither natively received nor reconstructed from
	// parity. It is the P4 acceptance signal — the loss FEC did not mask. Zero when FEC is
	// disabled or no data has flowed.
	ResidualLoss float64
	// Adaptive is the adaptive-FEC controller's most recent published decision (T263),
	// present ONLY for a peer running the adaptive controller (fecSender.ctrl != nil). It
	// is nil for a fixed-ratio or FEC-off peer, so no adaptive series is fabricated where
	// none exists — the PeerSnapshot.Aggregation nil-precedent (T146).
	Adaptive *AdaptiveFECStats
}

// AdaptiveFECStats is the adaptive-FEC controller's per-drive decision, published at the
// single serialized drive locus and scraped lock-free through the FEC snapshot chain
// (T263). Parity is the target parity count M the encoder was retargeted to
// (ctrl.Parity()); SmoothedLoss the controller's EWMA loss estimate (ctrl.SmoothedLoss());
// EligibleLoss the max raw probe-measured loss across the drive's eligible (StateUp +
// probed) paths; EligiblePaths the count of those paths. On the 'no eligible path' hold
// branch EligiblePaths is 0 (and EligibleLoss 0) while Parity/SmoothedLoss HOLD their last
// driven value — the count reaching 0 is how an operator observes that hold.
type AdaptiveFECStats struct {
	Parity        int
	SmoothedLoss  float64
	EligibleLoss  float64
	EligiblePaths int
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
