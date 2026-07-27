package metrics

import (
	"net/netip"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/7mind/wanbond/internal/congestion"
	"github.com/7mind/wanbond/internal/reseq"
	"github.com/7mind/wanbond/internal/shaper"
	"github.com/7mind/wanbond/internal/telemetry"
)

// Per-peer labelling (T94). A concentrator (G4) binds multiple peers, each with its own
// path set, FEC plane, and resequencer, so /metrics must attribute path/resequencer/FEC
// series to the edge they came from. BACK-COMPAT RULE (pick-one, see G4's open question):
// the `peer` label is OMITTED ENTIRELY — not emitted with an empty/default value — for a
// single-bound-peer Source (PeerNames() reports exactly one name, always "" for the
// single-peer edge/hub/concentrator-primary), so a single-peer scrape's series are
// byte-identical to the pre-T94 exposition (no label ever added or removed makes a
// PromQL selector's label set change shape mid-life). ONLY when 2+ peers are bound does
// the label appear, carrying each peer's BoundPeerNames() value verbatim — which, on a
// multi-peer concentrator, is EVERY configured peer's own name, including the
// first-configured one: device.Up plumbs ids[0].Name into the primary's peerState
// (bind.Multipath.SetPrimaryPeerName) whenever more than one peer is configured, so
// peer="" appears ONLY on the true single-peer exposition (D58). Because the label set
// is a property of the whole scrape (Prometheus requires every sample of one metric
// family to share one label schema), this is decided ONCE at NewCollector construction
// from Source.PeerNames() — never per-scrape — matching the peer set's documented static
// cardinality (a peer is bound at Open/AddConcentratorPeer, never added/removed at
// runtime).
//
// namespace prefixes every wanbond metric name; pathSubsystem, fecSubsystem,
// resequencerSubsystem, and sessionSubsystem partition the per-path, FEC, resequencer,
// and WG-session series.
const (
	namespace            = "wanbond"
	pathSubsystem        = "path"
	fecSubsystem         = "fec"
	resequencerSubsystem = "resequencer"
	sessionSubsystem     = "session"
	engineSubsystem      = "engine"
	tunAQMSubsystem      = "tun_aqm"
	// aggregationSubsystem partitions the weighted-scheduler aggregation-gate series
	// (T146). The smoothed offered-load gauge deliberately carries NO subsystem (it is
	// wanbond_offered_load_fps, not wanbond_aggregation_…) since it is the load the gate
	// observes, not a property of the gate itself.
	aggregationSubsystem = "aggregation"

	// labelPath is the single label carried by every per-path series; its value is
	// the stable path name from configuration (e.g. "starlink").
	labelPath = "path"
	// labelPeer is the per-bound-peer label (T94) carried by path/resequencer/FEC series
	// ONLY on a multi-peer Source — see the back-compat rule above.
	labelPeer = "peer"
)

// Per-path metric names, exported so tests and future e2e harnesses can assert
// series by name without restating the FQ-name construction.
const (
	MetricTxBytes    = "wanbond_path_tx_bytes_total"
	MetricRxBytes    = "wanbond_path_rx_bytes_total"
	MetricLoss       = "wanbond_path_loss_ratio"
	MetricRTT        = "wanbond_path_rtt_seconds"
	MetricJitter     = "wanbond_path_jitter_seconds"
	MetricThroughput = "wanbond_path_throughput_bits_per_second"
	MetricUp         = "wanbond_path_up"
	// MetricPathMTU is the per-path discovered outer PMTU in bytes (T206, defect D85):
	// the largest padded-probe on-wire size the per-path discovery machine confirmed
	// still echoes, the operator-configured mtu on a pinned path, or the conservative
	// floor before the first search converges. Sourced verbatim from PathSnapshot.PMTU.
	MetricPathMTU = "wanbond_path_mtu"
	// MetricProbeSendErrors is the per-path cumulative count of unexpected socket
	// write failures for locally-originated ordinary and PMTU PROBE frames. Ordinary
	// failures are counted but not returned to a caller; unexpected PMTU failures are
	// counted and returned to discovery. Expected PMTU EMSGSIZE verdicts and reactive
	// reflected-echo write failures are excluded. Sourced verbatim from
	// PathSnapshot.ProbeSendErrors.
	MetricProbeSendErrors                  = "wanbond_path_probe_send_errors_total"
	MetricProbePriorityCoalesced           = "wanbond_path_probe_priority_coalesced_total"
	MetricPMTUAdmissionCanceled            = "wanbond_path_pmtu_admission_canceled_total"
	MetricEchoPriorityOverflow             = "wanbond_path_echo_priority_overflow_total"
	MetricShaperAcceptedDatagrams          = "wanbond_path_shaper_accepted_datagrams_total"
	MetricShaperEmittedDatagrams           = "wanbond_path_shaper_emitted_datagrams_total"
	MetricShaperWriteErrors                = "wanbond_path_shaper_write_errors_total"
	MetricSocketWriteErrors                = "wanbond_path_socket_write_errors_total"
	MetricShaperQueueDataBytes             = "wanbond_path_shaper_queue_data_bytes"
	MetricShaperQueueControlBytes          = "wanbond_path_shaper_queue_control_bytes"
	MetricShaperQueueBytes                 = "wanbond_path_shaper_queue_bytes"
	MetricShaperInFlightBytes              = "wanbond_path_shaper_in_flight_bytes"
	MetricShaperScheduledDelay             = "wanbond_path_shaper_scheduled_delay_seconds"
	MetricShaperRateBytesPerSecond         = "wanbond_path_shaper_rate_bytes_per_second"
	MetricShaperDataBudgetBytes            = "wanbond_path_shaper_data_budget_bytes"
	MetricShaperControlReserveBytes        = "wanbond_path_shaper_control_reserve_bytes"
	MetricShaperQueueBudgetBytes           = "wanbond_path_shaper_queue_budget_bytes"
	MetricShaperMaxDatagramBytes           = "wanbond_path_shaper_max_datagram_bytes"
	MetricShaperAcceptedBytes              = "wanbond_path_shaper_accepted_bytes_total"
	MetricShaperEmittedBytes               = "wanbond_path_shaper_emitted_bytes_total"
	MetricShaperOuterPriorityBytes         = "wanbond_path_shaper_outer_priority_bytes_total"
	MetricShaperPriorityDebtBytes          = "wanbond_path_shaper_priority_debt_bytes"
	MetricShaperPriorityRateBytesPerSecond = "wanbond_path_shaper_priority_rate_bytes_per_second"
	MetricShaperPriorityBurstBytes         = "wanbond_path_shaper_priority_burst_bytes"
	MetricShaperPriorityDelayBound         = "wanbond_path_shaper_priority_delay_bound_seconds"
	MetricShaperPriorityRetainedBytes      = "wanbond_path_shaper_priority_retained_bytes"
	MetricShaperFECGroupOwnedBytes         = "wanbond_path_shaper_fec_group_owned_bytes"
	MetricShaperRecoveryRetainedBytes      = "wanbond_path_shaper_recovery_retained_bytes"
	MetricShaperMemoryBoundBytes           = "wanbond_path_shaper_memory_bound_bytes"
	MetricShaperMemoryRetainedBytes        = "wanbond_path_shaper_memory_retained_bytes"
	MetricShaperAdmissionWaits             = "wanbond_path_shaper_admission_waits_total"
	MetricShaperAdmissionWaitSeconds       = "wanbond_path_shaper_admission_wait_seconds_total"
	MetricShaperAdmissionCanceledDatagrams = "wanbond_path_shaper_admission_canceled_datagrams_total"
	MetricShaperAsyncWriteErrors           = "wanbond_path_shaper_async_write_errors_total"
	MetricShaperAsyncWriteErrorBytes       = "wanbond_path_shaper_async_write_error_bytes_total"
	MetricShaperAsyncWriteEMSGSIZEErrors   = "wanbond_path_shaper_async_write_emsgsize_errors_total"
	MetricShaperAsyncWriteEMSGSIZEBytes    = "wanbond_path_shaper_async_write_emsgsize_bytes_total"
)

// FEC metric names. These connection-scoped series (no path label — FEC
// repair/recovery is per-connection, not per-uplink) are populated from the live FEC
// plane (T24): repair = parity frames emitted (the fixed-ratio overhead), recovered =
// data frames reconstructed from parity, unrecoverable = data frames lost beyond
// repair capacity.
const (
	MetricFECData          = "wanbond_fec_data_packets_total"
	MetricFECRepair        = "wanbond_fec_repair_packets_total"
	MetricFECRecovered     = "wanbond_fec_recovered_packets_total"
	MetricFECUnrecoverable = "wanbond_fec_unrecoverable_packets_total"
	// Byte-denominated FEC overhead (T29). The adaptive-vs-fixed overhead comparison the
	// P4 acceptance makes is in BYTES (parity shards are max-shard-sized while DATA frames
	// vary), so these expose the byte numerator/denominator the frame counters above cannot:
	// overhead = repair_bytes / data_bytes.
	MetricFECDataBytes   = "wanbond_fec_data_bytes_total"
	MetricFECRepairBytes = "wanbond_fec_repair_bytes_total"
	// MetricFECResidualLoss is the post-FEC-recovery connection loss fraction (T29): the
	// share of outer-seqs neither natively received nor reconstructed from parity — the loss
	// FEC did not mask. It is the P4 residual-loss acceptance signal.
	MetricFECResidualLoss = "wanbond_fec_residual_loss_ratio"
	// Adaptive-FEC controller decision series (T263, D96, G29). Present ONLY for a peer
	// whose FECSnapshot.Adaptive is non-nil (the adaptive controller is engaged); absent
	// entirely for a fixed-ratio or FEC-off peer, mirroring the AggregationSnapshot
	// absent-series behaviour (T146).
	MetricFECAdaptiveParity   = "wanbond_fec_adaptive_parity"
	MetricFECSmoothedLoss     = "wanbond_fec_smoothed_loss"
	MetricFECEligiblePathLoss = "wanbond_fec_eligible_path_loss"
	MetricFECEligiblePaths    = "wanbond_fec_eligible_paths"
)

// Resequencer metric names (T94). Like the FEC series, these are per-PEER (a peer's
// resequencer buffers its whole bonded stream, not one uplink), so they carry no path
// label — only the conditional peer label. Sourced verbatim from reseq.Stats (see
// ReseqSnapshot); see reseq.Stats' field comments for each counter's exact meaning.
const (
	MetricReseqReleased       = "wanbond_resequencer_released_frames_total"
	MetricReseqDroppedDup     = "wanbond_resequencer_dropped_duplicate_frames_total"
	MetricReseqDroppedOld     = "wanbond_resequencer_dropped_stale_frames_total"
	MetricReseqDroppedSuspect = "wanbond_resequencer_dropped_suspect_frames_total"
	MetricReseqSkipped        = "wanbond_resequencer_skipped_seqs_total"
	MetricReseqResyncs        = "wanbond_resequencer_resyncs_total"
	MetricReseqRebaselines    = "wanbond_resequencer_rebaselines_total"
	// HoL-stall / hold signal (T242, D93 observability leg). The holds/hold-seconds
	// counter PAIR gives operators the mean hold (hold_seconds_total / holds_total —
	// the 250 ms class of latency that was previously invisible), and
	// immediate_releases_total counts the D93 single-delivering-path fast-path
	// releases DISTINCTLY from timeout skips, so a rising immediate-releases signals
	// the amplifier is disarmed. hold_seconds_total is derived from the resequencer's
	// nanosecond accumulator (HoldNanos) at scrape time. See reseq.Stats field
	// comments for the exact Skipped-vs-immediate semantics.
	MetricReseqHolds             = "wanbond_resequencer_hol_holds_total"
	MetricReseqHoldSeconds       = "wanbond_resequencer_hol_hold_seconds_total"
	MetricReseqImmediateReleases = "wanbond_resequencer_immediate_releases_total"
)

// WG-session metric names (I2). These connection-scoped series (no path label — the WG
// session is per-connection, not per-uplink) expose whether the amneziawg engine has a
// live session and how stale its last handshake is. Together they distinguish a tunnel
// that is STILL CONVERGING (established = 0, no completed handshake) from one that is
// WEDGED (a path is up but the handshake is absent or has aged out) — the signal
// D35/D36/D37 all presented identically without.
const (
	MetricSessionEstablished   = "wanbond_session_established"
	MetricSessionLastHandshake = "wanbond_session_last_handshake_seconds"
)

// MetricPeerSessionEstablished is the per-peer WG-session liveness gauge (T256, G28,
// M106): unlike wanbond_session_established above (the connection-scoped, most-recent-
// handshake-across-all-peers reading), this attributes the SAME 1/0 established verdict
// to ONE specific bound peer — the proof of session health a warm-standby promotion
// decision needs for a SPECIFIC candidate concentrator, not "some session is live".
// Follows the T94/D58 per-peer label rule below (peerLabelValues): labelled `peer=<name>`
// only once 2+ peers are bound; a single-peer Source still emits this series, unlabelled.
const MetricPeerSessionEstablished = "wanbond_peer_session_established"

const (
	MetricEngineTUNBytes                  = "wanbond_engine_tun_bytes_total"
	MetricEngineTUNBatchFrames            = "wanbond_engine_tun_batch_frames"
	MetricEngineSendBytes                 = "wanbond_engine_send_bytes_total"
	MetricEngineSendBatchFrames           = "wanbond_engine_send_batch_frames"
	MetricEngineEncryptionQueueContainers = "wanbond_engine_encryption_queue_containers"
	MetricEngineEncryptionQueueHighWater  = "wanbond_engine_encryption_queue_high_water_containers"
	MetricEnginePeerQueueContainers       = "wanbond_engine_peer_queue_containers"
	MetricEnginePeerQueueHighWater        = "wanbond_engine_peer_queue_high_water_containers"
	MetricEngineActiveSendFrames          = "wanbond_engine_active_send_frames"
	MetricEngineActiveSendBytes           = "wanbond_engine_active_send_bytes"
	MetricEngineActiveSendFramesHighWater = "wanbond_engine_active_send_frames_high_water"
	MetricEngineActiveSendBytesHighWater  = "wanbond_engine_active_send_bytes_high_water"
	MetricEngineAdmissionLimitBytes       = "wanbond_engine_admission_limit_bytes"
	MetricEngineAdmissionRetainedBytes    = "wanbond_engine_admission_retained_bytes"
	MetricEngineAdmissionHighWaterBytes   = "wanbond_engine_admission_high_water_bytes"
	MetricEngineAdmissionWaits            = "wanbond_engine_admission_waits_total"
	MetricEngineAdmissionWaitSeconds      = "wanbond_engine_admission_wait_seconds_total"
	MetricEngineAdmissionOversizeBatches  = "wanbond_engine_admission_oversize_batches_total"
)

// MetricWeightedCapacitySane is the Q52 WARN-arm capacity-sanity gauge (T144).
// Unlike every other series above, it is CONFIG-DERIVED, not sourced from Source at
// scrape time: its value is seeded at daemon startup from the loaded
// config.Config.WeightedCapacitySane verdict and re-set on a reload that changes the
// verdict via a path add/remove (D74, Server.SetWeightedCapacitySane), registered as a
// gauge alongside (not through) the Source-driven collector — see NewServer. It carries no labels at
// all (config-derived, not per-peer — exempt from the labelPeer back-compat rule) and
// the family is ABSENT ENTIRELY under the active-backup policy (a nil verdict). Under
// the weighted policy it reads 1 when every path declares link_bandwidth (SANE-VERIFIED)
// or 0 when at least one path's declaration is missing or partial (UNVERIFIABLE) — see
// docs/install.md §3a for the operator remedy.
const MetricWeightedCapacitySane = "wanbond_weighted_capacity_sane"

// MetricLivenessBudgetSane is the D86-decision-4 WARN-arm failover-budget gauge (T211),
// the liveness-timing twin of MetricWeightedCapacitySane. Like it, the value is
// CONFIG-DERIVED (not sourced from Source at scrape time): seeded at daemon startup from
// config.Config.LivenessBudgetSane and re-set on a reload whose applied path add/remove
// changes the worst-case ride_through (Server.SetLivenessBudgetSane). It carries no
// labels. Unlike the weighted gauge it is present for EVERY config (the failover budget
// always applies), reading 1 when the analytical per-direction failover budget fits the
// 3s P1 recovery deadline (SANE) or 0 when it exceeds it (OVER-BUDGET — the operator has
// widened down_after/ride_through past the transparent-failover deadline; see the startup
// WARN and docs/design.md).
const MetricLivenessBudgetSane = "wanbond_liveness_budget_sane"

// MetricTunMTU is the current wanbond0 link (TUN) MTU in bytes (T209, defect D85): the
// min inner MTU across UP paths the runtime resizer holds the interface at. It is
// seeded at daemon startup from the boot-time tunMTU (T205) and re-set whenever the
// resizer adjusts the live link (Server.SetTunMTU) as path liveness/PMTU membership
// changes. It carries no labels (connection-scoped, not per-path — the per-path
// discovered PMTU is the separate wanbond_path_mtu series).
const MetricTunMTU = "wanbond_tun_mtu"

// Aggregation-gate metric names (T146, Q54). These four PER-PEER series expose the
// weighted scheduler's data-thrift aggregation gate: whether striping is currently
// engaged, the smoothed offered load driving it, and the STATIC engage/disengage
// thresholds it compares that load against. They obey the T94 single-peer-omits-label
// back-compat rule (peer label present only on a multi-peer Source) and, unlike the
// config-derived MetricWeightedCapacitySane, are sourced from Source.Aggregation() at
// scrape time. The whole set is ABSENT under active-backup (its scheduler exposes no
// gate — Source.Aggregation() returns no entry for that peer).
const (
	// MetricAggregationEngaged is a per-peer bool gauge: 1 while the gate is engaged
	// (traffic striped across every eligible path), 0 while collapsed (primary-only).
	MetricAggregationEngaged = "wanbond_aggregation_engaged"
	// MetricOfferedLoadFPS is the per-peer smoothed offered-load estimate in
	// frames/second (EWMA of offered WIRE FRAMES — inner data plus any FEC
	// parity egressing on the chosen path, folded per Pick call) driving the
	// gate.
	MetricOfferedLoadFPS = "wanbond_offered_load_fps"
	// MetricAggregationEngageThreshold is the per-peer STATIC engage threshold in
	// frames/second (engage_fraction * per_path_capacity_fps): the offered load above
	// which the gate engages.
	MetricAggregationEngageThreshold = "wanbond_aggregation_engage_threshold_fps"
	// MetricAggregationDisengageThreshold is the per-peer STATIC disengage threshold in
	// frames/second (disengage_fraction * per_path_capacity_fps): the offered load
	// below which, sustained, the gate collapses.
	MetricAggregationDisengageThreshold = "wanbond_aggregation_disengage_threshold_fps"
)

// newWeightedCapacityGauge builds the wanbond_weighted_capacity_sane gauge (T144) seeded
// at sane's value. Unlike the collector it is never re-read at scrape time (its truth is
// config-derived, not live telemetry); the concrete prometheus.Gauge is returned so the
// Server can retain it and re-set it on a reload that changes the config-derived verdict
// after a path add/remove (D74) — the value is NOT fixed for the collector's whole life.
func newWeightedCapacityGauge(sane bool) prometheus.Gauge {
	g := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "weighted_capacity_sane",
		Help:      "Config-derived weighted-policy capacity-sanity verdict (1 = every path declares link_bandwidth, 0 = unverifiable; see docs/install.md).",
	})
	g.Set(weightedCapacitySaneValue(sane))
	return g
}

// weightedCapacitySaneValue maps the T144 verdict to the gauge value.
func weightedCapacitySaneValue(sane bool) float64 {
	if sane {
		return 1
	}
	return 0
}

// newLivenessBudgetGauge builds the wanbond_liveness_budget_sane gauge (T211) seeded at
// sane's value, the liveness-budget twin of newWeightedCapacityGauge. Config-derived, not
// re-read at scrape time; the concrete gauge is returned so the Server retains it and can
// re-set it on a reload whose applied path change moved the worst-case ride_through.
func newLivenessBudgetGauge(sane bool) prometheus.Gauge {
	g := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "liveness_budget_sane",
		Help:      "Config-derived failover-budget verdict (1 = per-direction failover budget fits the 3s P1 recovery deadline, 0 = over-budget; see docs/design.md).",
	})
	g.Set(weightedCapacitySaneValue(sane))
	return g
}

// newTunMTUGauge builds the wanbond_tun_mtu gauge (T209, defect D85) seeded to the
// boot-time TUN MTU. Like the sanity gauges above it is retained (not re-read from
// Source at scrape time): the concrete prometheus.Gauge is returned so the Server can
// re-set it via SetTunMTU when the runtime resizer adjusts the live link. It is present
// for every config (the TUN always has an MTU), unlike the conditionally-registered
// verdict gauges.
func newTunMTUGauge(mtu int) prometheus.Gauge {
	g := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "tun_mtu",
		Help:      "Current wanbond0 link (TUN) MTU in bytes: the min inner MTU across UP paths (T209, defect D85).",
	})
	g.Set(float64(mtu))
	return g
}

// FECSnapshot is the current connection-scoped FEC signal set the exposition layer
// reports (T24). It is sourced from the multipath Bind's FEC counters, read at scrape
// time like the per-path snapshots. All zero when FEC is disabled.
type FECSnapshot struct {
	// Peer attributes this snapshot to a bound peer (T94); see the package-level
	// back-compat rule for when it surfaces as the `peer` label. "" on a Source with a
	// single bound peer.
	Peer string
	// DataPackets is the cumulative count of DATA frames the FEC encoder emitted — the
	// denominator of the fixed-ratio overhead (RepairPackets/DataPackets tends to M/K).
	DataPackets uint64
	// RepairPackets is the cumulative count of parity frames emitted — the fixed-ratio
	// FEC overhead in packets.
	RepairPackets uint64
	// RecoveredPackets is the cumulative count of data frames reconstructed from parity.
	RecoveredPackets uint64
	// UnrecoverablePackets is the cumulative count of data frames lost beyond FEC repair
	// capacity.
	UnrecoverablePackets uint64
	// DataBytes / RepairBytes are the cumulative DATA and parity frame WIRE bytes — the
	// byte-denominated overhead numerator/denominator (T29): overhead = RepairBytes/DataBytes.
	DataBytes   uint64
	RepairBytes uint64
	// ResidualLossRatio is the current post-FEC-recovery connection loss fraction in [0,1]
	// (T29) — the loss FEC did not mask (the P4 acceptance signal). Zero when FEC is off.
	ResidualLossRatio    float64
	StagedGroups         uint64
	StagedDataFrames     uint64
	GroupDecisions       uint64
	DeadlineDecisions    uint64
	DeadlineMisses       uint64
	DeadlineMaxOvershoot time.Duration
	OpenGroupDeadline    time.Time
	Recovery             RecoveryStats
	// Adaptive is the adaptive-FEC controller's most recent published decision (T263,
	// D96), mirrored verbatim from bind.FECStats.Adaptive. It is nil for a fixed-ratio or
	// FEC-off peer, so the collector fabricates no adaptive series where none exists — the
	// AggregationSnapshot absent-series precedent (T146).
	Adaptive *AdaptiveFECStats
}

// RecoveryStats mirrors both directions of the peer recovery coordinator
// without exposing raw session/contract identities as metric values or labels.
type RecoveryStats struct {
	Sender   RecoveryDirectionStats
	Receiver RecoveryDirectionStats
}

type RecoveryDirectionStats struct {
	OfferPresent     bool
	FastEligible     bool
	TransitionFrozen bool
	WriterExclusive  bool
	FreshUntil       time.Time
	OfferWrites      uint64
	ACKWrites        uint64
	OfferAccepts     uint64
	ACKAccepts       uint64
	Rotations        uint64
	SessionRestarts  uint64
	StaleRejections  uint64
	WrongRejections  uint64
	ReplayRejections uint64
	FallbackReason   string
	ServiceBound     time.Duration
	RTTAge           time.Duration
	Headroom         time.Duration
	Window           time.Duration
}

// AdaptiveFECStats is the adaptive-FEC controller's per-drive decision (T263, D96),
// mirrored verbatim from bind.AdaptiveFECStats: Parity is the target parity count M the
// encoder was retargeted to (ctrl.Parity()); SmoothedLoss the controller's EWMA loss
// estimate; EligibleLoss the raw probe-measured loss the drive Observed over the
// sample-eligible data-carrying paths (T272/T324 — fresh pre-recovery DATA loss
// conservatively combined with probe loss for one stable active-backup carrier, the
// weight-weighted probe mix under weighted striping); EligiblePaths the count of those
// paths (0 on the hold branch).
type AdaptiveFECStats struct {
	Parity        int
	SmoothedLoss  float64
	EligibleLoss  float64
	EligiblePaths int
}

// PathSnapshot is the current per-path signal set the exposition layer reports.
// It fuses traffic accounting (TxBytes/RxBytes/Throughput, sourced from the bind
// and scheduler) with the telemetry plane's quality estimate and liveness verdict
// (Estimate/State, sourced verbatim from a Prober's Estimate()/State()). The
// metrics layer never measures these itself; it reads a Source at scrape time.
type PathSnapshot struct {
	// Peer attributes this snapshot to a bound peer (T94); see the package-level
	// back-compat rule for when it surfaces as the `peer` label. "" on a Source with a
	// single bound peer.
	Peer string
	// Name is the stable path identifier used as the `path` label. It must be
	// unique within one peer's entries of a single Source.Paths() result (T94: it need
	// not be globally unique across peers — a per-(peer,path) pair is the true key, which
	// is why the throughput last-sample map in internal/device/metrics.go keys on both).
	Name string
	// TxBytes and RxBytes are cumulative byte counters for the path.
	TxBytes uint64
	RxBytes uint64
	// ThroughputBitsPerSecond is the path's current send+receive throughput.
	ThroughputBitsPerSecond float64
	// Estimate carries per-path RTT/jitter/loss, read verbatim from telemetry.
	Estimate telemetry.Estimate
	// State is the per-path liveness verdict, read verbatim from telemetry.
	State telemetry.PathState
	// PMTU is the per-path discovered outer path MTU in bytes (T206, defect D85), read
	// verbatim from the path's telemetry.PMTUDiscovery snapshot accessor -> the
	// wanbond_path_mtu gauge. Like the addressing fields below, the DEFINITION and
	// exposition land here now; the value-wiring from the discovery machine through
	// internal/device/metrics.go rides with the TUN-resize task (T209), so it is
	// zero-valued until then.
	PMTU int
	// ProbeSendErrors is the cumulative count of unexpected locally-originated
	// ordinary/PMTU PROBE socket write failures for this path. Expected PMTU
	// EMSGSIZE verdicts are excluded. Read verbatim from bind.PathTraffic.
	ProbeSendErrors         uint64
	ProbePriorityCoalesced  uint64
	PMTUAdmissionCanceled   uint64
	EchoPriorityOverflow    uint64
	ShaperAcceptedDatagrams uint64
	ShaperEmittedDatagrams  uint64
	ShaperWriteErrors       uint64
	SocketWriteErrors       uint64
	// Shaper is nil when exact-byte shaping is disabled for this path.
	Shaper *shaper.Snapshot
	// Congestion is nil when the active-backup closed-loop controller is disabled.
	Congestion *congestion.Snapshot
	// The following addressing fields are the runtime-resolved per-path
	// networking metadata the monitoring UI surfaces (G21). They are DEFINED here
	// (T214) but the value-wiring from bind.PathTraffic through
	// internal/device/metrics.go is T220's job — they are zero-valued until then.
	// The Prometheus collector ignores them (no new series); they exist solely for
	// the monitor.BuildSnapshot read path.
	//
	// BindMode is the resolved bind mode ("source"|"device"|"auto"); BoundDevice
	// the resolved SO_BINDTODEVICE interface name (empty when source-pinned).
	BindMode    string
	BoundDevice string
	// Source is the bound local source address of the path's socket; Remote the
	// current wire remote the path points at (on the concentrator role, the
	// connected edge's observed source). Zero (invalid) until the path is bound.
	Source netip.Addr
	Remote netip.AddrPort
}

// ReseqSnapshot is the current per-peer resequencer signal set the exposition layer
// reports (T94). It embeds reseq.Stats verbatim — mirroring how PathSnapshot embeds
// telemetry.Estimate/PathState fields — read at scrape time from the peer's
// resequencer with no local aggregation. Like FECSnapshot it is per-PEER, not
// per-path: a peer's resequencer buffers its whole bonded stream, not one uplink.
type ReseqSnapshot struct {
	// Peer attributes this snapshot to a bound peer (T94); see the package-level
	// back-compat rule for when it surfaces as the `peer` label. "" on a Source with a
	// single bound peer.
	Peer string
	reseq.Stats
}

// AggregationSnapshot is the current per-peer weighted-scheduler aggregation-gate signal
// set the exposition layer reports (T146, Q54). It is sourced from the peer's scheduler
// at scrape time (via the Bind's per-peer snapshot); a peer whose scheduler exposes no
// gate (active-backup) contributes NO AggregationSnapshot, so its four series are absent.
// The threshold fields are STATIC (fixed at scheduler construction from
// engage/disengage_fraction * per_path_capacity_fps) — exposed as gauges so an operator
// can read the engaged/offered/threshold triple as one coherent snapshot.
type AggregationSnapshot struct {
	// Peer attributes this snapshot to a bound peer (T94); see the package-level
	// back-compat rule for when it surfaces as the `peer` label. "" on a Source with a
	// single bound peer.
	Peer string
	// Aggregating is the current gate verdict → the wanbond_aggregation_engaged 0/1
	// gauge (1 = striping across every eligible path, 0 = collapsed to primary-only).
	Aggregating bool
	// OfferedLoadFPS is the smoothed offered-load estimate (frames/second) driving the
	// gate → the wanbond_offered_load_fps gauge.
	OfferedLoadFPS float64
	// EngageThresholdFPS is the STATIC engage_fraction*per_path_capacity_fps threshold
	// (frames/second) → the wanbond_aggregation_engage_threshold_fps gauge.
	EngageThresholdFPS float64
	// DisengageThresholdFPS is the STATIC disengage_fraction*per_path_capacity_fps
	// threshold (frames/second) → the wanbond_aggregation_disengage_threshold_fps gauge.
	DisengageThresholdFPS float64
}

// SessionSnapshot is the current WG-session signal set the exposition layer reports
// (I2). It is sourced at scrape time from the amneziawg engine's peer last-handshake
// state by the device layer; the bind stays WG-unaware. The device layer owns the
// freshness policy (whether a completed-but-aged handshake still counts as
// established), so the metrics layer merely exposes the resolved verdict and age.
type SessionSnapshot struct {
	// Established is the current WG-session liveness verdict: true when a handshake has
	// completed AND is still within the session-validity window (fresh). A tunnel that
	// has never handshaked (still converging) or whose handshake has aged out (wedged)
	// reports false.
	Established bool
	// LastHandshakeAge is the elapsed time since the peer's most recent completed
	// handshake. It is zero when no handshake has ever completed (Established is then
	// false); read together with Established it disambiguates "never handshaked"
	// (Established=false, age=0) from "handshake aged out" (Established=false, age large).
	LastHandshakeAge time.Duration
}

// PeerSessionSnapshot is ONE bound peer's OWN WG-session health (T256, G28, M106).
// Unlike SessionSnapshot above — which collapses every configured peer into a single
// connection-scoped "is SOME session live" verdict (the most recent handshake across
// all peers) — this is keyed to a SPECIFIC peer, the signal warm-standby promotion needs
// ("is THIS candidate concentrator's session established", not "is some session
// established"). Follows the T94/D58 per-peer back-compat rule: Peer is meaningful only
// once 2+ peers are bound; a single-peer Source's PeerSessions() still returns exactly
// one entry, with Peer "", so the existing exposition is unchanged (see the
// package-level back-compat rule and PeerNames()).
type PeerSessionSnapshot struct {
	// Peer attributes this snapshot to a bound peer; see the package-level back-compat rule.
	Peer string
	// Established is this peer's own WG-session liveness verdict — a completed handshake
	// still within the session-validity window — computed the same way as
	// SessionSnapshot.Established but keyed to THIS peer's own last handshake rather than
	// the connection-wide max.
	Established bool
	// LastHandshakeSeconds is the elapsed time, in seconds, since this peer's most recent
	// completed handshake; zero when it has never handshaked (Established is then false).
	LastHandshakeSeconds float64
}

// Source is the read-only seam between the traffic/telemetry planes and the
// exposition layer. The collector calls Paths/FEC/Reseq at every scrape, so an
// implementation must be safe for concurrent use and must return a consistent
// snapshot (unique (peer,path) names) cheaply — it is on the scrape hot path.
// PeerNames, by contrast, is queried ONCE at NewCollector construction (see the
// package-level back-compat rule) — implementations may compute it fresh from the
// same underlying per-peer state Paths/FEC/Reseq read; it need not be cached.
type Source interface {
	// Paths returns the current per-(peer,path) snapshots.
	Paths() []PathSnapshot
	// FEC returns the current per-peer connection-scoped FEC counters (T24, T94).
	FEC() []FECSnapshot
	// Reseq returns the current per-peer resequencer counters (T94).
	Reseq() []ReseqSnapshot
	// Aggregation returns the current per-peer weighted-scheduler aggregation-gate
	// snapshots (T146). It returns ONE entry per peer whose scheduler exposes a gate
	// (the weighted policy) and NO entry for a peer without one (active-backup), so the
	// four aggregation series are absent whenever no bound peer runs the weighted policy.
	Aggregation() []AggregationSnapshot
	// Session returns the current connection-scoped WG-session snapshot (I2).
	Session() SessionSnapshot
	// PeerSessions returns the current per-peer WG-session snapshot (T256, G28, M106):
	// ONE entry per bound peer's OWN session health, distinct from the connection-scoped
	// Session() above. Follows the same PeerNames()-driven back-compat rule as
	// Paths/FEC/Reseq/Aggregation: exactly one entry (Peer "") for a single-bound-peer
	// Source, one meaningful entry per peer once 2+ are bound.
	PeerSessions() []PeerSessionSnapshot
	// PeerNames returns the STATIC set of bound peer names (BoundPeerNames order):
	// len == 1 selects the single-peer back-compat exposition (the `peer` label is
	// omitted); len > 1 selects the per-peer exposition (see the package-level
	// back-compat rule).
	PeerNames() []string
}

type EngineBatchHistogram struct {
	Count   uint64
	Frames  uint64
	Buckets map[uint64]uint64
}

type EngineOutboundSnapshot struct {
	TUNBytes                  uint64
	TUNBatchFrames            EngineBatchHistogram
	SendBytes                 uint64
	SendBatchFrames           EngineBatchHistogram
	EncryptionQueueContainers uint64
	EncryptionQueueHighWater  uint64
	PeerQueueContainers       uint64
	PeerQueueHighWater        uint64
	ActiveSendFrames          uint64
	ActiveSendBytes           uint64
	ActiveSendFramesHighWater uint64
	ActiveSendBytesHighWater  uint64
	AdmissionLimitBytes       uint64
	AdmissionRetainedBytes    uint64
	AdmissionHighWaterBytes   uint64
	AdmissionWaits            uint64
	AdmissionWaitNanoseconds  uint64
	AdmissionOversizeBatches  uint64
}

type EngineOutboundSource interface {
	EngineOutbound() EngineOutboundSnapshot
}

type TUNAQMSnapshot struct {
	TargetRateBytesPerSecond  float64
	ActualRateBytesPerSecond  float64
	TargetTxQueueLen          int
	ActualTxQueueLen          int
	TargetEpoch               uint64
	ActualEpoch               uint64
	TargetQueueLimitPackets   int
	ActualQueueLimitPackets   int
	ActualFlowLimitPackets    int
	TargetGSOMaxSizeBytes     int
	ActualGSOMaxSizeBytes     int
	TargetGSOMaxSegments      int
	ActualGSOMaxSegments      int
	TargetAdmissionLimitBytes int
	ActualFresh               bool
	RateFresh                 bool
	ActualQueueLengthPackets  int
	ActualBacklogBytes        int
	ActualDrops               uint64
	QueueLimitDeferred        bool
	GSOLimitsDeferred         bool
	AdmissionLimitDeferred    bool
	ActualObservedAt          time.Time
}

type TUNAQMSource interface {
	TUNAQM() *TUNAQMSnapshot
}

// collector is a prometheus.Collector that reads a Source at scrape time and
// emits per-path const-metrics plus the FEC/resequencer counters. Reading at
// scrape time (rather than mirroring into GaugeVecs on an update path) keeps the
// exposition consistent with the live telemetry with no duplicated state and no
// staleness window. multiPeer is decided once at construction (see the
// package-level back-compat rule) and gates whether the `peer` label is ever
// attached, for the collector's whole life.
type collector struct {
	src       Source
	multiPeer bool

	txBytes           *prometheus.Desc
	rxBytes           *prometheus.Desc
	loss              *prometheus.Desc
	rtt               *prometheus.Desc
	jitter            *prometheus.Desc
	throughput        *prometheus.Desc
	up                *prometheus.Desc
	pmtu              *prometheus.Desc
	probeErrs         *prometheus.Desc
	probeCoalesced    *prometheus.Desc
	pmtuCanceled      *prometheus.Desc
	echoOverflow      *prometheus.Desc
	shaperAccepted    *prometheus.Desc
	shaperEmitted     *prometheus.Desc
	shaperErrors      *prometheus.Desc
	socketErrors      *prometheus.Desc
	shaperMetrics     []shaperMetric
	congestionMetrics []congestionMetric

	fecData          *prometheus.Desc
	fecRepair        *prometheus.Desc
	fecRecovered     *prometheus.Desc
	fecUnrecoverable *prometheus.Desc
	fecDataBytes     *prometheus.Desc
	fecRepairBytes   *prometheus.Desc
	fecResidualLoss  *prometheus.Desc

	fecAdaptiveParity   *prometheus.Desc
	fecSmoothedLoss     *prometheus.Desc
	fecEligiblePathLoss *prometheus.Desc
	fecEligiblePaths    *prometheus.Desc
	fecMetrics          []fecMetric
	recoveryMetrics     []recoveryMetric
	recoveryRejections  *prometheus.Desc
	recoveryFallback    *prometheus.Desc

	reseqReleased          *prometheus.Desc
	reseqDroppedDup        *prometheus.Desc
	reseqDroppedOld        *prometheus.Desc
	reseqDroppedSuspect    *prometheus.Desc
	reseqSkipped           *prometheus.Desc
	reseqResyncs           *prometheus.Desc
	reseqRebaselines       *prometheus.Desc
	reseqHolds             *prometheus.Desc
	reseqHoldSeconds       *prometheus.Desc
	reseqImmediateReleases *prometheus.Desc
	reseqMetrics           []reseqMetric

	aggregationEngaged    *prometheus.Desc
	offeredLoad           *prometheus.Desc
	aggregationEngageTh   *prometheus.Desc
	aggregationDisengageT *prometheus.Desc

	sessionEstablished   *prometheus.Desc
	sessionLastHandshake *prometheus.Desc

	peerSessionEstablished *prometheus.Desc

	engineTUNBytes                  *prometheus.Desc
	engineTUNBatchFrames            *prometheus.Desc
	engineSendBytes                 *prometheus.Desc
	engineSendBatchFrames           *prometheus.Desc
	engineEncryptionQueueContainers *prometheus.Desc
	engineEncryptionQueueHighWater  *prometheus.Desc
	enginePeerQueueContainers       *prometheus.Desc
	enginePeerQueueHighWater        *prometheus.Desc
	engineActiveSendFrames          *prometheus.Desc
	engineActiveSendBytes           *prometheus.Desc
	engineActiveSendFramesHighWater *prometheus.Desc
	engineActiveSendBytesHighWater  *prometheus.Desc
	engineAdmissionLimitBytes       *prometheus.Desc
	engineAdmissionRetainedBytes    *prometheus.Desc
	engineAdmissionHighWaterBytes   *prometheus.Desc
	engineAdmissionWaits            *prometheus.Desc
	engineAdmissionWaitSeconds      *prometheus.Desc
	engineAdmissionOversizeBatches  *prometheus.Desc

	tunAQMTargetRate           *prometheus.Desc
	tunAQMActualRate           *prometheus.Desc
	tunAQMTargetQueue          *prometheus.Desc
	tunAQMActualQueue          *prometheus.Desc
	tunAQMTargetEpoch          *prometheus.Desc
	tunAQMActualEpoch          *prometheus.Desc
	tunAQMTargetLimit          *prometheus.Desc
	tunAQMActualLimit          *prometheus.Desc
	tunAQMActualFlow           *prometheus.Desc
	tunAQMTargetGSOMaxSize     *prometheus.Desc
	tunAQMActualGSOMaxSize     *prometheus.Desc
	tunAQMTargetGSOMaxSegments *prometheus.Desc
	tunAQMActualGSOMaxSegments *prometheus.Desc
	tunAQMTargetAdmissionLimit *prometheus.Desc
	tunAQMActualFresh          *prometheus.Desc
	tunAQMRateFresh            *prometheus.Desc
	tunAQMActualQueueLength    *prometheus.Desc
	tunAQMActualBacklog        *prometheus.Desc
	tunAQMActualDrops          *prometheus.Desc
	tunAQMQueueLimitDeferred   *prometheus.Desc
	tunAQMGSOLimitsDeferred    *prometheus.Desc
	tunAQMAdmissionDeferred    *prometheus.Desc
	tunAQMObservedTime         *prometheus.Desc
}

type shaperMetric struct {
	desc      *prometheus.Desc
	valueType prometheus.ValueType
	value     func(shaper.Snapshot) float64
}

type congestionMetric struct {
	desc      *prometheus.Desc
	valueType prometheus.ValueType
	value     func(congestion.Snapshot) float64
}

type fecMetric struct {
	desc      *prometheus.Desc
	valueType prometheus.ValueType
	value     func(FECSnapshot) float64
}

type recoveryMetric struct {
	desc       *prometheus.Desc
	valueType  prometheus.ValueType
	value      func(RecoveryDirectionStats) float64
	directions []string
}

type reseqMetric struct {
	desc      *prometheus.Desc
	valueType prometheus.ValueType
	value     func(ReseqSnapshot) float64
}

// NewCollector builds the wanbond metrics collector over src. Register it into a
// dedicated prometheus.Registry (see NewServer); it deliberately does not touch
// the global default registry (no-globals discipline). It queries src.PeerNames()
// ONCE here to fix the `peer` label's presence for the collector's whole life (T94):
// Prometheus requires every sample of one metric family to share one label schema,
// so the omit-vs-include back-compat decision cannot be made per-scrape.
func NewCollector(src Source) prometheus.Collector {
	multiPeer := len(src.PeerNames()) > 1
	pathLabels := []string{labelPath}
	peerScopedLabels := []string(nil)
	if multiPeer {
		pathLabels = []string{labelPath, labelPeer}
		peerScopedLabels = []string{labelPeer}
	}
	desc := func(subsystem, name, help string, labels []string) *prometheus.Desc {
		return prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, name), help, labels, nil)
	}
	makeShaperMetric := func(
		name string,
		help string,
		valueType prometheus.ValueType,
		value func(shaper.Snapshot) float64,
	) shaperMetric {
		return shaperMetric{
			desc:      desc(pathSubsystem, name, help, pathLabels),
			valueType: valueType,
			value:     value,
		}
	}
	makeCongestionMetric := func(
		name string,
		help string,
		valueType prometheus.ValueType,
		value func(congestion.Snapshot) float64,
	) congestionMetric {
		return congestionMetric{
			desc:      desc(pathSubsystem, name, help, pathLabels),
			valueType: valueType,
			value:     value,
		}
	}
	makeFECMetric := func(
		subsystem string,
		name string,
		help string,
		valueType prometheus.ValueType,
		value func(FECSnapshot) float64,
	) fecMetric {
		return fecMetric{desc: desc(subsystem, name, help, peerScopedLabels), valueType: valueType, value: value}
	}
	directionLabels := append(append([]string(nil), peerScopedLabels...), "direction")
	makeRecoveryMetric := func(
		subsystem string,
		name string,
		help string,
		valueType prometheus.ValueType,
		value func(RecoveryDirectionStats) float64,
		directions ...string,
	) recoveryMetric {
		return recoveryMetric{
			desc:       desc(subsystem, name, help, directionLabels),
			valueType:  valueType,
			value:      value,
			directions: directions,
		}
	}
	makeReseqMetric := func(
		name string,
		help string,
		valueType prometheus.ValueType,
		value func(ReseqSnapshot) float64,
	) reseqMetric {
		return reseqMetric{desc: desc(resequencerSubsystem, name, help, peerScopedLabels), valueType: valueType, value: value}
	}
	reasonLabels := append(append([]string(nil), directionLabels...), "reason")
	return &collector{
		src:            src,
		multiPeer:      multiPeer,
		txBytes:        desc(pathSubsystem, "tx_bytes_total", "Total bytes transmitted on the path.", pathLabels),
		rxBytes:        desc(pathSubsystem, "rx_bytes_total", "Total bytes received on the path.", pathLabels),
		loss:           desc(pathSubsystem, "loss_ratio", "Per-path probe loss fraction in [0,1].", pathLabels),
		rtt:            desc(pathSubsystem, "rtt_seconds", "Smoothed per-path round-trip time in seconds.", pathLabels),
		jitter:         desc(pathSubsystem, "jitter_seconds", "Smoothed per-path RTT deviation (jitter) in seconds.", pathLabels),
		throughput:     desc(pathSubsystem, "throughput_bits_per_second", "Current per-path throughput in bits per second.", pathLabels),
		up:             desc(pathSubsystem, "up", "Per-path liveness (1 = up, 0 = down).", pathLabels),
		pmtu:           desc(pathSubsystem, "mtu", "Per-path discovered outer path MTU in bytes (configured value on a pinned path, else the largest padded-probe on-wire size that echoes).", pathLabels),
		probeErrs:      desc(pathSubsystem, "probe_send_errors_total", "Unexpected locally-originated ordinary/PMTU PROBE socket write failures. Expected PMTU EMSGSIZE too-large verdicts are excluded; PMTU failures return to discovery and ordinary failures are counted then discarded.", pathLabels),
		probeCoalesced: desc(pathSubsystem, "probe_priority_coalesced_total", "Ordinary probe cadences coalesced because retained generated-priority reserve P was full.", pathLabels),
		pmtuCanceled:   desc(pathSubsystem, "pmtu_admission_canceled_total", "PMTU probes canceled while waiting for retained generated-priority admission.", pathLabels),
		echoOverflow:   desc(pathSubsystem, "echo_priority_overflow_total", "Reactive echoes dropped without blocking receive because retained generated-priority reserve P was full.", pathLabels),
		shaperAccepted: desc(pathSubsystem, "shaper_accepted_datagrams_total", "DATA/PARITY and inner-control datagrams made ready after copying caller memory into the path's exact-byte shaper.", pathLabels),
		shaperEmitted:  desc(pathSubsystem, "shaper_emitted_datagrams_total", "Shaped DATA/PARITY and inner-control datagrams successfully written to the UDP socket.", pathLabels),
		shaperErrors:   desc(pathSubsystem, "shaper_write_errors_total", "Shaped calls returning a terminal error after any accepted/emitted prefix.", pathLabels),
		socketErrors:   desc(pathSubsystem, "socket_write_errors_total", "UDP socket write errors from shaped and direct DATA/PARITY/inner-control datagrams; excludes generated outer PROBE and reflected-echo failures.", pathLabels),
		shaperMetrics: []shaperMetric{
			makeShaperMetric("shaper_queue_data_bytes", "Current retained DATA/PARITY bytes, including pending-copy placeholders; bounded by B.", prometheus.GaugeValue, func(s shaper.Snapshot) float64 { return float64(s.QueueDataBytes) }),
			makeShaperMetric("shaper_queue_control_bytes", "Current retained inner-control bytes, including pending-copy placeholders; bounded by C=Lmax.", prometheus.GaugeValue, func(s shaper.Snapshot) float64 { return float64(s.QueueControlBytes) }),
			makeShaperMetric("shaper_queue_bytes", "Current retained shaped bytes across both classes, including pending-copy placeholders; bounded by Q=B+C.", prometheus.GaugeValue, func(s shaper.Snapshot) float64 { return float64(s.QueueBytes) }),
			makeShaperMetric("shaper_in_flight_bytes", "Current UDP-writer bytes outside retained budget Q; either zero or one datagram no larger than Lmax.", prometheus.GaugeValue, func(s shaper.Snapshot) float64 { return float64(s.InFlightBytes) }),
			makeShaperMetric("shaper_scheduled_delay_seconds", "Current virtual serialization-tail delay in seconds; already-assigned datagram deadlines are immutable.", prometheus.GaugeValue, func(s shaper.Snapshot) float64 { return s.ScheduledDelay.Seconds() }),
			makeShaperMetric("shaper_rate_bytes_per_second", "Configured exact-byte shaper wire rate R in bytes per second.", prometheus.GaugeValue, func(s shaper.Snapshot) float64 { return s.RateBytesPerSecond }),
			makeShaperMetric("shaper_data_budget_bytes", "Configured exact-byte shaper DATA/PARITY queue budget B in bytes.", prometheus.GaugeValue, func(s shaper.Snapshot) float64 { return float64(s.DataBudgetBytes) }),
			makeShaperMetric("shaper_control_reserve_bytes", "Configured exact-byte shaper inner-control queue reserve C in bytes.", prometheus.GaugeValue, func(s shaper.Snapshot) float64 { return float64(s.ControlReserveBytes) }),
			makeShaperMetric("shaper_queue_budget_bytes", "Configured exact-byte shaper total reserved-byte queue budget Q=B+C in bytes.", prometheus.GaugeValue, func(s shaper.Snapshot) float64 { return float64(s.QueueBudgetBytes) }),
			makeShaperMetric("shaper_max_datagram_bytes", "Configured exact-byte shaper maximum encoded datagram size Lmax in bytes.", prometheus.GaugeValue, func(s shaper.Snapshot) float64 { return float64(s.MaxDatagramBytes) }),
			makeShaperMetric("shaper_accepted_bytes_total", "Total DATA/PARITY and inner-control bytes reserved before copying caller memory.", prometheus.CounterValue, func(s shaper.Snapshot) float64 { return float64(s.AcceptedBytes) }),
			makeShaperMetric("shaper_emitted_bytes_total", "Total DATA/PARITY and inner-control bytes successfully written to the UDP socket.", prometheus.CounterValue, func(s shaper.Snapshot) float64 { return float64(s.EmittedBytes) }),
			makeShaperMetric("shaper_outer_priority_bytes_total", "Total authenticated outer PROBE/echo bytes admitted to retained priority storage and charged to virtual time.", prometheus.CounterValue, func(s shaper.Snapshot) float64 { return float64(s.OuterPriorityBytes) }),
			makeShaperMetric("shaper_outer_priority_emitted_bytes_total", "Authenticated outer PROBE/echo wire bytes successfully emitted by the socket writer.", prometheus.CounterValue, func(s shaper.Snapshot) float64 { return float64(s.OuterPriorityEmittedBytes) }),
			makeShaperMetric("shaper_outer_priority_error_bytes_total", "Authenticated outer PROBE/echo bytes assigned to a terminal socket-writer error.", prometheus.CounterValue, func(s shaper.Snapshot) float64 { return float64(s.OuterPriorityErrorBytes) }),
			makeShaperMetric("shaper_priority_debt_bytes", "Current outer-priority serialization debt P0 in bytes.", prometheus.GaugeValue, func(s shaper.Snapshot) float64 { return s.PriorityDebtBytes }),
			makeShaperMetric("shaper_priority_rate_bytes_per_second", "Configured sustained outer-priority rate Rp in bytes per second.", prometheus.GaugeValue, func(s shaper.Snapshot) float64 { return s.PriorityRateBytesPerSecond }),
			makeShaperMetric("shaper_priority_burst_bytes", "Configured outer-priority burst allowance Pburst in bytes.", prometheus.GaugeValue, func(s shaper.Snapshot) float64 { return float64(s.PriorityBurstBytes) }),
			makeShaperMetric("shaper_priority_delay_bound_seconds", "Current DATA/inner-control admission bound Dp=(P0+Pburst)/(R-Rp) in seconds under generated priority Rp<R.", prometheus.GaugeValue, func(s shaper.Snapshot) float64 { return s.PriorityDelayBound.Seconds() }),
			makeShaperMetric("shaper_priority_retained_bytes", "Current retained generated-priority bytes; bounded by P=Pburst.", prometheus.GaugeValue, func(s shaper.Snapshot) float64 { return float64(s.PriorityRetainedBytes) }),
			makeShaperMetric("shaper_fec_group_owned_bytes", "Current conservative one-group FEC ownership reservation Fgroup.", prometheus.GaugeValue, func(s shaper.Snapshot) float64 { return float64(s.FECGroupOwnedBytes) }),
			makeShaperMetric("shaper_recovery_retained_bytes", "Current exact DATA+parity wire bytes retained inside the owned recovery group.", prometheus.GaugeValue, func(s shaper.Snapshot) float64 { return float64(s.RecoveryRetainedBytes) }),
			makeShaperMetric("shaper_memory_bound_bytes", "Configured exact retained-memory bound Mtotal=B+C+P+Fgroup+Lio.", prometheus.GaugeValue, func(s shaper.Snapshot) float64 { return float64(s.MemoryBoundBytes) }),
			makeShaperMetric("shaper_memory_retained_bytes", "Current retained and in-flight bytes across B+C+P+Fgroup+Lio.", prometheus.GaugeValue, func(s shaper.Snapshot) float64 { return float64(s.MemoryRetainedBytes) }),
			makeShaperMetric("shaper_recovery_cut_active", "Whether one exclusive recovery cut currently owns the path socket deadline (1=yes).", prometheus.GaugeValue, func(s shaper.Snapshot) float64 { return boolValue(s.RecoveryCutActive) }),
			makeShaperMetric("shaper_recovery_cut_deadline_timestamp_seconds", "Absolute Unix timestamp in seconds of the active recovery cut socket deadline; 0 while inactive.", prometheus.GaugeValue, func(s shaper.Snapshot) float64 { return timestampSeconds(s.RecoveryCutDeadline) }),
			makeShaperMetric("shaper_recovery_cut_datagrams", "Number of DATA/parity/control datagrams in the active exclusive recovery cut; 0 while inactive.", prometheus.GaugeValue, func(s shaper.Snapshot) float64 { return float64(s.RecoveryCutDatagrams) }),
			makeShaperMetric("shaper_recovery_cut_socket_calls_total", "Cumulative UDP socket writer calls made as members of exclusive recovery cuts.", prometheus.CounterValue, func(s shaper.Snapshot) float64 { return float64(s.RecoveryCutSocketCalls) }),
			makeShaperMetric("shaper_fec_group_owned_high_water_bytes", "High-water conservative FEC group ownership reservation Fgroup in bytes.", prometheus.GaugeValue, func(s shaper.Snapshot) float64 { return float64(s.FECGroupOwnedHighWaterBytes) }),
			makeShaperMetric("shaper_memory_retained_high_water_bytes", "High-water retained and in-flight memory across B+C+P+Fgroup+Lio in bytes.", prometheus.GaugeValue, func(s shaper.Snapshot) float64 { return float64(s.MemoryRetainedHighWaterBytes) }),
			makeShaperMetric("shaper_admission_waits_total", "Total datagram admissions that encountered queue-capacity or priority-debt backpressure.", prometheus.CounterValue, func(s shaper.Snapshot) float64 { return float64(s.AdmissionWaits) }),
			makeShaperMetric("shaper_admission_wait_seconds_total", "Cumulative seconds spent waiting for exact-byte shaper admission.", prometheus.CounterValue, func(s shaper.Snapshot) float64 { return s.AdmissionWaitDuration.Seconds() }),
			makeShaperMetric("shaper_admission_canceled_datagrams_total", "Total datagrams never reserved because their admission context was canceled or expired.", prometheus.CounterValue, func(s shaper.Snapshot) float64 { return float64(s.AdmissionCanceledDatagrams) }),
			makeShaperMetric("shaper_async_write_errors_total", "Actual UDP writer calls failing asynchronously, excluding EMSGSIZE.", prometheus.CounterValue, func(s shaper.Snapshot) float64 { return float64(s.AsyncWriteErrors) }),
			makeShaperMetric("shaper_async_write_error_bytes_total", "Reserved bytes assigned to a generic writer failure, including its retired unstarted batch suffix.", prometheus.CounterValue, func(s shaper.Snapshot) float64 { return float64(s.AsyncWriteErrorBytes) }),
			makeShaperMetric("shaper_async_write_emsgsize_errors_total", "Actual UDP writer calls failing asynchronously with EMSGSIZE.", prometheus.CounterValue, func(s shaper.Snapshot) float64 { return float64(s.AsyncWriteEMSGSIZEErrors) }),
			makeShaperMetric("shaper_async_write_emsgsize_bytes_total", "Reserved bytes assigned to an EMSGSIZE writer failure, including its retired unstarted batch suffix.", prometheus.CounterValue, func(s shaper.Snapshot) float64 { return float64(s.AsyncWriteEMSGSIZEBytes) }),
		},
		congestionMetrics: []congestionMetric{
			makeCongestionMetric("congestion_outer_wire_bytes_total", "Actual outer IP/UDP/frame bytes emitted in the current path process lifetime.", prometheus.CounterValue, func(s congestion.Snapshot) float64 {
				return float64(s.Actual.OuterWireBytes)
			}),
			makeCongestionMetric("congestion_inner_data_bytes_total", "Inner DATA payload bytes represented by emitted native DATA frames in the current path process lifetime.", prometheus.CounterValue, func(s congestion.Snapshot) float64 {
				return float64(s.Actual.InnerDataBytes)
			}),
			makeCongestionMetric("congestion_target_outer_bytes_per_second", "Closed-loop target outer wire rate for the active carrier.", prometheus.GaugeValue, func(s congestion.Snapshot) float64 {
				return s.Target.OuterRateBytesPerSecond
			}),
			makeCongestionMetric("congestion_target_ingress_bytes_per_second", "Closed-loop sender-side TUN ingress target after measured outer/inner overhead.", prometheus.GaugeValue, func(s congestion.Snapshot) float64 {
				return s.Target.IngressRateBytesPerSecond
			}),
			makeCongestionMetric("congestion_installed_ingress_bytes_per_second", "Last exact sender-side TUN ingress rate read back for this controller target.", prometheus.GaugeValue, func(s congestion.Snapshot) float64 {
				return s.InstalledIngress.RateBytesPerSecond
			}),
			makeCongestionMetric("congestion_installed_fresh", "Whether the installed TUN ingress readback matches the acknowledged controller epoch and target (1=yes).", prometheus.GaugeValue, func(s congestion.Snapshot) float64 {
				return boolValue(s.InstalledIngress.Fresh)
			}),
			makeCongestionMetric("congestion_delivered_bytes_per_second", "Delivered outer wire rate derived from successive emission-counter observations.", prometheus.GaugeValue, func(s congestion.Snapshot) float64 {
				return s.DeliveredRateBytesPerSecond
			}),
			makeCongestionMetric("congestion_base_rtt_seconds", "Minimum probe RTT observed in the current carrier epoch.", prometheus.GaugeValue, func(s congestion.Snapshot) float64 {
				return s.BaseRTT.Seconds()
			}),
			makeCongestionMetric("congestion_queue_delay_seconds", "Probe RTT above the current carrier epoch's base RTT.", prometheus.GaugeValue, func(s congestion.Snapshot) float64 {
				return s.QueueDelay.Seconds()
			}),
			makeCongestionMetric("congestion_authenticated_loss_ratio", "Authenticated pre-recovery DATA loss ratio associated with the current carrier contract.", prometheus.GaugeValue, func(s congestion.Snapshot) float64 {
				return s.Actual.AuthenticatedLoss
			}),
			makeCongestionMetric("congestion_loss_fresh", "Whether the authenticated DATA-loss sample passed carrier identity and freshness checks (1=yes).", prometheus.GaugeValue, func(s congestion.Snapshot) float64 {
				return boolValue(s.Actual.LossFresh)
			}),
			makeCongestionMetric("congestion_carrier_epoch", "Local monotonic active-carrier generation; A-B-A transitions create distinct epochs.", prometheus.GaugeValue, func(s congestion.Snapshot) float64 {
				return float64(s.Target.Epoch.Generation)
			}),
			makeCongestionMetric("congestion_held", "Whether the controller froze the prior target because the observation could not safely advance it (1=yes).", prometheus.GaugeValue, func(s congestion.Snapshot) float64 {
				return boolValue(s.Held)
			}),
			makeCongestionMetric("congestion_retarget_pending", "Whether another controller decision awaits exact installed-rate readback and post-readback settling (1=yes).", prometheus.GaugeValue, func(s congestion.Snapshot) float64 {
				return boolValue(s.AwaitingInstalled)
			}),
			makeCongestionMetric("congestion_target_changes", "Controller target changes made in the current active-carrier epoch.", prometheus.GaugeValue, func(s congestion.Snapshot) float64 {
				return float64(s.TargetChanges)
			}),
		},

		fecData:          desc(fecSubsystem, "data_packets_total", "FEC DATA packets emitted (the fixed-ratio overhead denominator).", peerScopedLabels),
		fecRepair:        desc(fecSubsystem, "repair_packets_total", "FEC parity packets emitted (the fixed-ratio overhead).", peerScopedLabels),
		fecRecovered:     desc(fecSubsystem, "recovered_packets_total", "Data packets reconstructed via FEC.", peerScopedLabels),
		fecUnrecoverable: desc(fecSubsystem, "unrecoverable_packets_total", "Data packets lost beyond FEC repair capacity.", peerScopedLabels),
		fecDataBytes:     desc(fecSubsystem, "data_bytes_total", "FEC DATA-frame wire bytes emitted (the byte overhead denominator).", peerScopedLabels),
		fecRepairBytes:   desc(fecSubsystem, "repair_bytes_total", "FEC parity-frame wire bytes emitted (the byte overhead numerator).", peerScopedLabels),
		fecResidualLoss:  desc(fecSubsystem, "residual_loss_ratio", "Post-FEC-recovery connection loss fraction in [0,1] (loss FEC did not mask).", peerScopedLabels),

		fecAdaptiveParity:   desc(fecSubsystem, "adaptive_parity", "Adaptive-FEC controller's current target parity count M (present only while the controller is engaged).", peerScopedLabels),
		fecSmoothedLoss:     desc(fecSubsystem, "smoothed_loss", "Adaptive-FEC controller's EWMA smoothed loss estimate in [0,1] (present only while the controller is engaged).", peerScopedLabels),
		fecEligiblePathLoss: desc(fecSubsystem, "eligible_path_loss", "Loss the adaptive-FEC drive observed: the maximum of fresh authenticated pre-recovery DATA loss and probe loss for one stable active-backup carrier, or the weight-weighted probe mix under weighted striping (present only while the controller is engaged).", peerScopedLabels),
		fecEligiblePaths:    desc(fecSubsystem, "eligible_paths", "Count of sample-eligible data-carrying paths the adaptive-FEC drive considered; 0 on the hold branch (present only while the controller is engaged).", peerScopedLabels),
		fecMetrics: []fecMetric{
			makeFECMetric(fecSubsystem, "staged_groups", "Current sender-owned FEC groups staged but not yet resolved; bounded by one.", prometheus.GaugeValue, func(f FECSnapshot) float64 { return float64(f.StagedGroups) }),
			makeFECMetric(fecSubsystem, "staged_data_frames", "Current DATA frames staged in the sender-owned undecided FEC group.", prometheus.GaugeValue, func(f FECSnapshot) float64 { return float64(f.StagedDataFrames) }),
			makeFECMetric(fecSubsystem, "group_decisions_total", "Cumulative immutable FEC group decisions published before DATA/parity emission.", prometheus.CounterValue, func(f FECSnapshot) float64 { return float64(f.GroupDecisions) }),
			makeFECMetric(fecSubsystem, "deadline_decisions_total", "Cumulative FEC groups decided by the exact NextDeadline timer.", prometheus.CounterValue, func(f FECSnapshot) float64 { return float64(f.DeadlineDecisions) }),
			makeFECMetric(fecSubsystem, "deadline_misses_total", "Cumulative FEC deadline decisions whose dispatch overshoot exceeded G=10ms.", prometheus.CounterValue, func(f FECSnapshot) float64 { return float64(f.DeadlineMisses) }),
			makeFECMetric(fecSubsystem, "deadline_max_overshoot_seconds", "Maximum FEC decision dispatch overshoot in seconds for the current sender generation.", prometheus.GaugeValue, func(f FECSnapshot) float64 { return f.DeadlineMaxOvershoot.Seconds() }),
			makeFECMetric(fecSubsystem, "open_group_deadline_timestamp_seconds", "Absolute Unix timestamp in seconds of the currently open FEC group deadline; 0 when none is open.", prometheus.GaugeValue, func(f FECSnapshot) float64 { return timestampSeconds(f.OpenGroupDeadline) }),
		},
		recoveryMetrics: []recoveryMetric{
			makeRecoveryMetric("recovery_contract", "offer_present", "Whether the direction currently owns an offered recovery contract (1=yes).", prometheus.GaugeValue, func(r RecoveryDirectionStats) float64 { return boolValue(r.OfferPresent) }, "sender", "receiver"),
			makeRecoveryMetric("recovery_contract", "fast_eligible", "Whether the direction's exact ACKed contract currently permits fast recovery (1=yes).", prometheus.GaugeValue, func(r RecoveryDirectionStats) float64 { return boolValue(r.FastEligible) }, "sender", "receiver"),
			makeRecoveryMetric("recovery_contract", "transition_frozen", "Whether the direction currently reports a service-transition freeze (1=yes).", prometheus.GaugeValue, func(r RecoveryDirectionStats) float64 { return boolValue(r.TransitionFrozen) }, "sender", "receiver"),
			makeRecoveryMetric("recovery_contract", "writer_exclusive", "Whether the direction's offered service requires one exclusive socket writer (1=yes).", prometheus.GaugeValue, func(r RecoveryDirectionStats) float64 { return boolValue(r.WriterExclusive) }, "sender", "receiver"),
			makeRecoveryMetric("recovery_contract", "fresh_until_timestamp_seconds", "Absolute Unix timestamp through which the direction's installed contract decision remains fresh; 0 when absent.", prometheus.GaugeValue, func(r RecoveryDirectionStats) float64 { return timestampSeconds(r.FreshUntil) }, "sender", "receiver"),
			makeRecoveryMetric("recovery_contract", "offer_writes_total", "Cumulative authenticated outbound recovery OFFER writes.", prometheus.CounterValue, func(r RecoveryDirectionStats) float64 { return float64(r.OfferWrites) }, "sender"),
			makeRecoveryMetric("recovery_contract", "ack_writes_total", "Cumulative inbound-contract ACK writes completed on current authenticated venues.", prometheus.CounterValue, func(r RecoveryDirectionStats) float64 { return float64(r.ACKWrites) }, "receiver"),
			makeRecoveryMetric("recovery_contract", "offer_accepts_total", "Cumulative inbound recovery OFFERs accepted after validation.", prometheus.CounterValue, func(r RecoveryDirectionStats) float64 { return float64(r.OfferAccepts) }, "receiver"),
			makeRecoveryMetric("recovery_contract", "ack_accepts_total", "Cumulative outbound-contract ACKs accepted into a local lease.", prometheus.CounterValue, func(r RecoveryDirectionStats) float64 { return float64(r.ACKAccepts) }, "sender"),
			makeRecoveryMetric("recovery_contract", "rotations_total", "Cumulative outbound ContractID rotations, excluding the initial OFFER.", prometheus.CounterValue, func(r RecoveryDirectionStats) float64 { return float64(r.Rotations) }, "sender"),
			makeRecoveryMetric("recovery_contract", "session_restarts_total", "Cumulative inbound authenticated process-session changes that invalidated old evidence.", prometheus.CounterValue, func(r RecoveryDirectionStats) float64 { return float64(r.SessionRestarts) }, "receiver"),
			makeRecoveryMetric("recovery_contract", "service_bound_seconds", "Advertised completion service bound for the direction; 0 when absent.", prometheus.GaugeValue, func(r RecoveryDirectionStats) float64 { return r.ServiceBound.Seconds() }, "sender", "receiver"),
			makeRecoveryMetric("recovery", "rtt_age_seconds", "Maximum age of qualified UP/fresh RTT evidence in the installed receiver decision.", prometheus.GaugeValue, func(r RecoveryDirectionStats) float64 { return r.RTTAge.Seconds() }, "receiver"),
			makeRecoveryMetric("recovery", "headroom_seconds", "Derived receiver RTT headroom H in the installed decision.", prometheus.GaugeValue, func(r RecoveryDirectionStats) float64 { return r.Headroom.Seconds() }, "receiver"),
			makeRecoveryMetric("recovery", "window_seconds", "Installed receiver hold W; conservative T while fast recovery is unavailable.", prometheus.GaugeValue, func(r RecoveryDirectionStats) float64 { return r.Window.Seconds() }, "receiver"),
		},
		recoveryRejections: desc("recovery_contract", "rejections_total", "Cumulative rejected recovery messages partitioned by direction and bounded reason.", reasonLabels),
		recoveryFallback:   desc("recovery_contract", "fallback", "Current direction-specific fallback reason as a bounded one-hot gauge; all zero while fast recovery is eligible.", reasonLabels),

		reseqReleased:       desc(resequencerSubsystem, "released_frames_total", "Frames released for delivery by the resequencer.", peerScopedLabels),
		reseqDroppedDup:     desc(resequencerSubsystem, "dropped_duplicate_frames_total", "Frames dropped by the resequencer as duplicates.", peerScopedLabels),
		reseqDroppedOld:     desc(resequencerSubsystem, "dropped_stale_frames_total", "Frames dropped by the resequencer as already past the release point.", peerScopedLabels),
		reseqDroppedSuspect: desc(resequencerSubsystem, "dropped_suspect_frames_total", "Out-of-band frames dropped by the resequencer while not yet corroborating.", peerScopedLabels),
		reseqSkipped:        desc(resequencerSubsystem, "skipped_seqs_total", "Sequence numbers skipped (lost) by the resequencer's window-advance or timeout.", peerScopedLabels),
		reseqResyncs:        desc(resequencerSubsystem, "resyncs_total", "Resequencer release-point re-pins after a corroborated discontinuity.", peerScopedLabels),
		reseqRebaselines:    desc(resequencerSubsystem, "rebaselines_total", "Resequencer release-point re-baselines forced by a trusted control event (e.g. hub failover).", peerScopedLabels),

		reseqHolds:             desc(resequencerSubsystem, "hol_holds_total", "Head-of-line gaps that armed a hold (denominator of the mean hold; pair with hol_hold_seconds_total).", peerScopedLabels),
		reseqHoldSeconds:       desc(resequencerSubsystem, "hol_hold_seconds_total", "Cumulative seconds head-of-line gaps spent held before a timeout skip, a single-path immediate release, or a fill (numerator of the mean hold).", peerScopedLabels),
		reseqImmediateReleases: desc(resequencerSubsystem, "immediate_releases_total", "Head-of-line gaps released via the D93 single-delivering-path fast path (counted distinctly from timeout skips; rising = the D93 amplifier is disarmed).", peerScopedLabels),
		reseqMetrics: []reseqMetric{
			makeReseqMetric("recovery_armed", "Whether the live head-of-line gap uses an authenticated fast recovery window (1=yes).", prometheus.GaugeValue, func(r ReseqSnapshot) float64 { return boolValue(r.RecoveryArmed) }),
			makeReseqMetric("armed_deadline_timestamp_seconds", "Absolute Unix timestamp in seconds of the live head-of-line deadline; 0 while disarmed.", prometheus.GaugeValue, func(r ReseqSnapshot) float64 { return timestampSeconds(r.ArmedDeadline) }),
			makeReseqMetric("armed_window_seconds", "Duration in seconds from the live gap arm instant to its deadline; 0 while disarmed.", prometheus.GaugeValue, func(r ReseqSnapshot) float64 { return r.ArmedWindow.Seconds() }),
			makeReseqMetric("deadline_wakeups_total", "Cumulative head-of-line releases evaluated at or after their armed deadline.", prometheus.CounterValue, func(r ReseqSnapshot) float64 { return float64(r.DeadlineWakeups) }),
			makeReseqMetric("gap_fills_total", "Cumulative armed head-of-line gaps filled before release.", prometheus.CounterValue, func(r ReseqSnapshot) float64 { return float64(r.GapFills) }),
			makeReseqMetric("fast_window_arms_total", "Cumulative gaps initially armed from exact authenticated recovery evidence.", prometheus.CounterValue, func(r ReseqSnapshot) float64 { return float64(r.FastWindowArms) }),
			makeReseqMetric("fallback_window_arms_total", "Cumulative gaps armed or re-armed with the conservative timeout.", prometheus.CounterValue, func(r ReseqSnapshot) float64 { return float64(r.FallbackWindowArms) }),
		},

		aggregationEngaged:    desc(aggregationSubsystem, "engaged", "Weighted-scheduler aggregation gate (1 = striping across every eligible path, 0 = collapsed to primary-only).", peerScopedLabels),
		offeredLoad:           desc("", "offered_load_fps", "Smoothed offered load in wire frames/second (inner data plus any FEC parity egressing on the chosen path, EWMA) driving the aggregation gate.", peerScopedLabels),
		aggregationEngageTh:   desc(aggregationSubsystem, "engage_threshold_fps", "Static engage threshold in frames/second (engage_fraction * per_path_capacity_fps): offered load above which the gate engages.", peerScopedLabels),
		aggregationDisengageT: desc(aggregationSubsystem, "disengage_threshold_fps", "Static disengage threshold in frames/second (disengage_fraction * per_path_capacity_fps): offered load below which, sustained, the gate collapses.", peerScopedLabels),

		sessionEstablished:   desc(sessionSubsystem, "established", "WG session liveness (1 = a handshake has completed and is still fresh, 0 = still converging or wedged).", nil),
		sessionLastHandshake: desc(sessionSubsystem, "last_handshake_seconds", "Age in seconds of the peer's most recent completed WG handshake (0 when none has completed).", nil),

		peerSessionEstablished: desc("", "peer_session_established", "Per-peer WG session liveness (1 = that peer's own handshake has completed and is still fresh, 0 = still converging or wedged); distinct from the connection-scoped wanbond_session_established.", peerScopedLabels),

		engineTUNBytes:                  desc(engineSubsystem, "tun_bytes_total", "Inner packet bytes read from TUN into outbound engine containers.", nil),
		engineTUNBatchFrames:            desc(engineSubsystem, "tun_batch_frames", "Distribution of inner frames grouped into one outbound container after Linux GSO splitting.", nil),
		engineSendBytes:                 desc(engineSubsystem, "send_bytes_total", "Encrypted WireGuard bytes handed to Bind.Send.", nil),
		engineSendBatchFrames:           desc(engineSubsystem, "send_batch_frames", "Distribution of encrypted WireGuard frames handed to one Bind.Send call.", nil),
		engineEncryptionQueueContainers: desc(engineSubsystem, "encryption_queue_containers", "Outbound containers currently queued for encryption.", nil),
		engineEncryptionQueueHighWater:  desc(engineSubsystem, "encryption_queue_high_water_containers", "Maximum observed outbound container depth of the shared encryption queue.", nil),
		enginePeerQueueContainers:       desc(engineSubsystem, "peer_queue_containers", "Outbound containers currently queued ahead of peer sequential senders.", nil),
		enginePeerQueueHighWater:        desc(engineSubsystem, "peer_queue_high_water_containers", "Maximum observed container depth of any peer outbound queue.", nil),
		engineActiveSendFrames:          desc(engineSubsystem, "active_send_frames", "Encrypted WireGuard frames currently held across synchronous Bind.Send calls.", nil),
		engineActiveSendBytes:           desc(engineSubsystem, "active_send_bytes", "Encrypted WireGuard bytes currently held across synchronous Bind.Send calls.", nil),
		engineActiveSendFramesHighWater: desc(engineSubsystem, "active_send_frames_high_water", "Maximum encrypted WireGuard frames concurrently held across Bind.Send calls.", nil),
		engineActiveSendBytesHighWater:  desc(engineSubsystem, "active_send_bytes_high_water", "Maximum encrypted WireGuard bytes concurrently held across Bind.Send calls.", nil),
		engineAdmissionLimitBytes:       desc(engineSubsystem, "admission_limit_bytes", "Aggregate per-peer exact-wire-byte admission limit retained through terminal Bind completion.", nil),
		engineAdmissionRetainedBytes:    desc(engineSubsystem, "admission_retained_bytes", "Exact encrypted WireGuard bytes admitted but not yet terminally completed by the Bind.", nil),
		engineAdmissionHighWaterBytes:   desc(engineSubsystem, "admission_high_water_bytes", "Sum of per-peer maximum exact-wire-byte backlog retained through terminal Bind completion.", nil),
		engineAdmissionWaits:            desc(engineSubsystem, "admission_waits_total", "Whole outbound batches that waited for per-peer byte admission.", nil),
		engineAdmissionWaitSeconds:      desc(engineSubsystem, "admission_wait_seconds_total", "Cumulative time whole outbound batches waited for per-peer byte admission.", nil),
		engineAdmissionOversizeBatches:  desc(engineSubsystem, "admission_oversize_batches_total", "Whole outbound batches larger than their admission target; expected to remain zero when kernel GSO readback is fresh.", nil),

		tunAQMTargetRate:           desc(tunAQMSubsystem, "target_rate_bytes_per_second", "Requested sender-side TUN ingress rate for the current controller epoch.", nil),
		tunAQMActualRate:           desc(tunAQMSubsystem, "actual_rate_bytes_per_second", "Kernel-read-back HTB rate on the TUN interface.", nil),
		tunAQMTargetQueue:          desc(tunAQMSubsystem, "target_tx_queue_length", "Requested TUN interface transmit queue length.", nil),
		tunAQMActualQueue:          desc(tunAQMSubsystem, "actual_tx_queue_length", "Kernel-read-back TUN interface transmit queue length.", nil),
		tunAQMTargetEpoch:          desc(tunAQMSubsystem, "target_epoch", "Aggregate active-carrier generation requested from the kernel.", nil),
		tunAQMActualEpoch:          desc(tunAQMSubsystem, "actual_epoch", "Aggregate active-carrier generation whose kernel readback matched the target.", nil),
		tunAQMTargetLimit:          desc(tunAQMSubsystem, "target_queue_limit_packets", "Requested bounded fair-queue packet limit derived from the admitted service backlog.", nil),
		tunAQMActualLimit:          desc(tunAQMSubsystem, "actual_queue_limit_packets", "Kernel-read-back bounded fair-queue packet limit.", nil),
		tunAQMActualFlow:           desc(tunAQMSubsystem, "actual_flow_limit_packets", "Kernel-read-back per-flow packet limit on the bounded fair queue.", nil),
		tunAQMTargetGSOMaxSize:     desc(tunAQMSubsystem, "target_gso_max_size_bytes", "Requested pre-TUN maximum GSO container size derived from the local-delay budget.", nil),
		tunAQMActualGSOMaxSize:     desc(tunAQMSubsystem, "actual_gso_max_size_bytes", "Kernel-read-back pre-TUN maximum GSO container size.", nil),
		tunAQMTargetGSOMaxSegments: desc(tunAQMSubsystem, "target_gso_max_segments", "Requested pre-TUN maximum GSO segment count derived from the local-delay budget.", nil),
		tunAQMActualGSOMaxSegments: desc(tunAQMSubsystem, "actual_gso_max_segments", "Kernel-read-back pre-TUN maximum GSO segment count.", nil),
		tunAQMTargetAdmissionLimit: desc(tunAQMSubsystem, "target_engine_admission_limit_bytes", "Requested per-peer exact-wire-byte engine backlog consistent with one bounded whole GSO batch.", nil),
		tunAQMActualFresh:          desc(tunAQMSubsystem, "actual_fresh", "Whether qdisc topology, bounded-fq parameters, rate, queue length, and epoch matched at the latest readback (1=yes).", nil),
		tunAQMRateFresh:            desc(tunAQMSubsystem, "rate_fresh", "Whether the exact requested HTB rate and controller epoch matched even when a capacity shrink remained safely deferred (1=yes).", nil),
		tunAQMActualQueueLength:    desc(tunAQMSubsystem, "actual_queue_length_packets", "Kernel-read-back live packet count in the TUN qdisc tree.", nil),
		tunAQMActualBacklog:        desc(tunAQMSubsystem, "actual_backlog_bytes", "Kernel-read-back live byte backlog in the TUN qdisc tree.", nil),
		tunAQMActualDrops:          desc(tunAQMSubsystem, "drops_total", "Kernel-read-back cumulative drops in the TUN root qdisc.", nil),
		tunAQMQueueLimitDeferred:   desc(tunAQMSubsystem, "queue_limit_deferred", "Whether a queue-limit shrink awaits a packet count no greater than the requested bound (1=yes).", nil),
		tunAQMGSOLimitsDeferred:    desc(tunAQMSubsystem, "gso_limits_deferred", "Whether a GSO-limit shrink awaits an empty TUN qdisc backlog (1=yes).", nil),
		tunAQMAdmissionDeferred:    desc(tunAQMSubsystem, "engine_admission_limit_deferred", "Whether an engine admission shrink awaits every peer's retained bytes fitting the requested bound (1=yes).", nil),
		tunAQMObservedTime:         desc(tunAQMSubsystem, "actual_observed_timestamp_seconds", "Unix timestamp of the latest exact kernel qdisc/link readback.", nil),
	}
}

// Describe sends every descriptor; the collector's series set (including whether the
// `peer` label is attached) is fixed for the collector's whole life even though the
// label VALUES are discovered at Collect time.
func (c *collector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.txBytes
	ch <- c.rxBytes
	ch <- c.loss
	ch <- c.rtt
	ch <- c.jitter
	ch <- c.throughput
	ch <- c.up
	ch <- c.pmtu
	ch <- c.probeErrs
	ch <- c.probeCoalesced
	ch <- c.pmtuCanceled
	ch <- c.echoOverflow
	ch <- c.shaperAccepted
	ch <- c.shaperEmitted
	ch <- c.shaperErrors
	ch <- c.socketErrors
	for _, metric := range c.shaperMetrics {
		ch <- metric.desc
	}
	for _, metric := range c.congestionMetrics {
		ch <- metric.desc
	}
	ch <- c.fecData
	ch <- c.fecRepair
	ch <- c.fecRecovered
	ch <- c.fecUnrecoverable
	ch <- c.fecDataBytes
	ch <- c.fecRepairBytes
	ch <- c.fecResidualLoss
	ch <- c.fecAdaptiveParity
	ch <- c.fecSmoothedLoss
	ch <- c.fecEligiblePathLoss
	ch <- c.fecEligiblePaths
	for _, metric := range c.fecMetrics {
		ch <- metric.desc
	}
	for _, metric := range c.recoveryMetrics {
		ch <- metric.desc
	}
	ch <- c.recoveryRejections
	ch <- c.recoveryFallback
	ch <- c.reseqReleased
	ch <- c.reseqDroppedDup
	ch <- c.reseqDroppedOld
	ch <- c.reseqDroppedSuspect
	ch <- c.reseqSkipped
	ch <- c.reseqResyncs
	ch <- c.reseqRebaselines
	ch <- c.reseqHolds
	ch <- c.reseqHoldSeconds
	ch <- c.reseqImmediateReleases
	for _, metric := range c.reseqMetrics {
		ch <- metric.desc
	}
	ch <- c.aggregationEngaged
	ch <- c.offeredLoad
	ch <- c.aggregationEngageTh
	ch <- c.aggregationDisengageT
	ch <- c.sessionEstablished
	ch <- c.sessionLastHandshake
	ch <- c.peerSessionEstablished
	ch <- c.engineTUNBytes
	ch <- c.engineTUNBatchFrames
	ch <- c.engineSendBytes
	ch <- c.engineSendBatchFrames
	ch <- c.engineEncryptionQueueContainers
	ch <- c.engineEncryptionQueueHighWater
	ch <- c.enginePeerQueueContainers
	ch <- c.enginePeerQueueHighWater
	ch <- c.engineActiveSendFrames
	ch <- c.engineActiveSendBytes
	ch <- c.engineActiveSendFramesHighWater
	ch <- c.engineActiveSendBytesHighWater
	ch <- c.engineAdmissionLimitBytes
	ch <- c.engineAdmissionRetainedBytes
	ch <- c.engineAdmissionHighWaterBytes
	ch <- c.engineAdmissionWaits
	ch <- c.engineAdmissionWaitSeconds
	ch <- c.engineAdmissionOversizeBatches
	ch <- c.tunAQMTargetRate
	ch <- c.tunAQMActualRate
	ch <- c.tunAQMTargetQueue
	ch <- c.tunAQMActualQueue
	ch <- c.tunAQMTargetEpoch
	ch <- c.tunAQMActualEpoch
	ch <- c.tunAQMTargetLimit
	ch <- c.tunAQMActualLimit
	ch <- c.tunAQMActualFlow
	ch <- c.tunAQMTargetGSOMaxSize
	ch <- c.tunAQMActualGSOMaxSize
	ch <- c.tunAQMTargetGSOMaxSegments
	ch <- c.tunAQMActualGSOMaxSegments
	ch <- c.tunAQMTargetAdmissionLimit
	ch <- c.tunAQMActualFresh
	ch <- c.tunAQMRateFresh
	ch <- c.tunAQMActualQueueLength
	ch <- c.tunAQMActualBacklog
	ch <- c.tunAQMActualDrops
	ch <- c.tunAQMQueueLimitDeferred
	ch <- c.tunAQMGSOLimitsDeferred
	ch <- c.tunAQMAdmissionDeferred
	ch <- c.tunAQMObservedTime
}

// Collect reads the Source once and emits one const-metric per per-(peer,path)
// series, then the per-peer FEC and resequencer counters, then the two
// connection-scoped WG-session series.
func (c *collector) Collect(ch chan<- prometheus.Metric) {
	for _, p := range c.src.Paths() {
		labels := c.pathLabelValues(p.Name, p.Peer)
		ch <- prometheus.MustNewConstMetric(c.txBytes, prometheus.CounterValue, float64(p.TxBytes), labels...)
		ch <- prometheus.MustNewConstMetric(c.rxBytes, prometheus.CounterValue, float64(p.RxBytes), labels...)
		ch <- prometheus.MustNewConstMetric(c.loss, prometheus.GaugeValue, p.Estimate.Loss, labels...)
		ch <- prometheus.MustNewConstMetric(c.rtt, prometheus.GaugeValue, p.Estimate.RTT.Seconds(), labels...)
		ch <- prometheus.MustNewConstMetric(c.jitter, prometheus.GaugeValue, p.Estimate.Jitter.Seconds(), labels...)
		ch <- prometheus.MustNewConstMetric(c.throughput, prometheus.GaugeValue, p.ThroughputBitsPerSecond, labels...)
		ch <- prometheus.MustNewConstMetric(c.up, prometheus.GaugeValue, upValue(p.State), labels...)
		ch <- prometheus.MustNewConstMetric(c.pmtu, prometheus.GaugeValue, float64(p.PMTU), labels...)
		ch <- prometheus.MustNewConstMetric(c.probeErrs, prometheus.CounterValue, float64(p.ProbeSendErrors), labels...)
		ch <- prometheus.MustNewConstMetric(c.probeCoalesced, prometheus.CounterValue, float64(p.ProbePriorityCoalesced), labels...)
		ch <- prometheus.MustNewConstMetric(c.pmtuCanceled, prometheus.CounterValue, float64(p.PMTUAdmissionCanceled), labels...)
		ch <- prometheus.MustNewConstMetric(c.echoOverflow, prometheus.CounterValue, float64(p.EchoPriorityOverflow), labels...)
		if p.Shaper != nil {
			ch <- prometheus.MustNewConstMetric(c.shaperAccepted, prometheus.CounterValue, float64(p.ShaperAcceptedDatagrams), labels...)
			ch <- prometheus.MustNewConstMetric(c.shaperEmitted, prometheus.CounterValue, float64(p.ShaperEmittedDatagrams), labels...)
			ch <- prometheus.MustNewConstMetric(c.shaperErrors, prometheus.CounterValue, float64(p.ShaperWriteErrors), labels...)
			for _, metric := range c.shaperMetrics {
				ch <- prometheus.MustNewConstMetric(metric.desc, metric.valueType, metric.value(*p.Shaper), labels...)
			}
		}
		if p.Congestion != nil {
			for _, metric := range c.congestionMetrics {
				ch <- prometheus.MustNewConstMetric(metric.desc, metric.valueType, metric.value(*p.Congestion), labels...)
			}
		}
		ch <- prometheus.MustNewConstMetric(c.socketErrors, prometheus.CounterValue, float64(p.SocketWriteErrors), labels...)
	}
	for _, f := range c.src.FEC() {
		labels := c.peerLabelValues(f.Peer)
		ch <- prometheus.MustNewConstMetric(c.fecData, prometheus.CounterValue, float64(f.DataPackets), labels...)
		ch <- prometheus.MustNewConstMetric(c.fecRepair, prometheus.CounterValue, float64(f.RepairPackets), labels...)
		ch <- prometheus.MustNewConstMetric(c.fecRecovered, prometheus.CounterValue, float64(f.RecoveredPackets), labels...)
		ch <- prometheus.MustNewConstMetric(c.fecUnrecoverable, prometheus.CounterValue, float64(f.UnrecoverablePackets), labels...)
		ch <- prometheus.MustNewConstMetric(c.fecDataBytes, prometheus.CounterValue, float64(f.DataBytes), labels...)
		ch <- prometheus.MustNewConstMetric(c.fecRepairBytes, prometheus.CounterValue, float64(f.RepairBytes), labels...)
		ch <- prometheus.MustNewConstMetric(c.fecResidualLoss, prometheus.GaugeValue, f.ResidualLossRatio, labels...)
		if f.Adaptive != nil {
			ch <- prometheus.MustNewConstMetric(c.fecAdaptiveParity, prometheus.GaugeValue, float64(f.Adaptive.Parity), labels...)
			ch <- prometheus.MustNewConstMetric(c.fecSmoothedLoss, prometheus.GaugeValue, f.Adaptive.SmoothedLoss, labels...)
			ch <- prometheus.MustNewConstMetric(c.fecEligiblePathLoss, prometheus.GaugeValue, f.Adaptive.EligibleLoss, labels...)
			ch <- prometheus.MustNewConstMetric(c.fecEligiblePaths, prometheus.GaugeValue, float64(f.Adaptive.EligiblePaths), labels...)
		}
		for _, metric := range c.fecMetrics {
			ch <- prometheus.MustNewConstMetric(metric.desc, metric.valueType, metric.value(f), labels...)
		}
		for _, metric := range c.recoveryMetrics {
			for _, direction := range metric.directions {
				directionStats := recoveryDirection(f.Recovery, direction)
				directionLabels := append(append([]string(nil), labels...), direction)
				ch <- prometheus.MustNewConstMetric(metric.desc, metric.valueType, metric.value(directionStats), directionLabels...)
			}
		}
		for _, direction := range []string{"sender", "receiver"} {
			directionStats := recoveryDirection(f.Recovery, direction)
			for _, reason := range []struct {
				name  string
				value uint64
			}{
				{name: "stale", value: directionStats.StaleRejections},
				{name: "wrong", value: directionStats.WrongRejections},
				{name: "replay", value: directionStats.ReplayRejections},
			} {
				reasonLabels := append(append(append([]string(nil), labels...), direction), reason.name)
				ch <- prometheus.MustNewConstMetric(c.recoveryRejections, prometheus.CounterValue, float64(reason.value), reasonLabels...)
			}
			for _, reason := range []string{"no_offer", "unacked", "stale", "wrong", "replay", "shared", "transition", "restart", "saturated"} {
				value := float64(0)
				if directionStats.FallbackReason == reason {
					value = 1
				}
				reasonLabels := append(append(append([]string(nil), labels...), direction), reason)
				ch <- prometheus.MustNewConstMetric(c.recoveryFallback, prometheus.GaugeValue, value, reasonLabels...)
			}
		}
	}
	for _, r := range c.src.Reseq() {
		labels := c.peerLabelValues(r.Peer)
		ch <- prometheus.MustNewConstMetric(c.reseqReleased, prometheus.CounterValue, float64(r.Released), labels...)
		ch <- prometheus.MustNewConstMetric(c.reseqDroppedDup, prometheus.CounterValue, float64(r.DroppedDup), labels...)
		ch <- prometheus.MustNewConstMetric(c.reseqDroppedOld, prometheus.CounterValue, float64(r.DroppedOld), labels...)
		ch <- prometheus.MustNewConstMetric(c.reseqDroppedSuspect, prometheus.CounterValue, float64(r.DroppedSuspect), labels...)
		ch <- prometheus.MustNewConstMetric(c.reseqSkipped, prometheus.CounterValue, float64(r.Skipped), labels...)
		ch <- prometheus.MustNewConstMetric(c.reseqResyncs, prometheus.CounterValue, float64(r.Resyncs), labels...)
		ch <- prometheus.MustNewConstMetric(c.reseqRebaselines, prometheus.CounterValue, float64(r.Rebaselines), labels...)
		ch <- prometheus.MustNewConstMetric(c.reseqHolds, prometheus.CounterValue, float64(r.Holds), labels...)
		ch <- prometheus.MustNewConstMetric(c.reseqHoldSeconds, prometheus.CounterValue, float64(r.HoldNanos)/1e9, labels...)
		ch <- prometheus.MustNewConstMetric(c.reseqImmediateReleases, prometheus.CounterValue, float64(r.ImmediateReleases), labels...)
		for _, metric := range c.reseqMetrics {
			ch <- prometheus.MustNewConstMetric(metric.desc, metric.valueType, metric.value(r), labels...)
		}
	}

	for _, a := range c.src.Aggregation() {
		labels := c.peerLabelValues(a.Peer)
		ch <- prometheus.MustNewConstMetric(c.aggregationEngaged, prometheus.GaugeValue, aggregatingValue(a.Aggregating), labels...)
		ch <- prometheus.MustNewConstMetric(c.offeredLoad, prometheus.GaugeValue, a.OfferedLoadFPS, labels...)
		ch <- prometheus.MustNewConstMetric(c.aggregationEngageTh, prometheus.GaugeValue, a.EngageThresholdFPS, labels...)
		ch <- prometheus.MustNewConstMetric(c.aggregationDisengageT, prometheus.GaugeValue, a.DisengageThresholdFPS, labels...)
	}

	sess := c.src.Session()
	ch <- prometheus.MustNewConstMetric(c.sessionEstablished, prometheus.GaugeValue, establishedValue(sess.Established))
	ch <- prometheus.MustNewConstMetric(c.sessionLastHandshake, prometheus.GaugeValue, sess.LastHandshakeAge.Seconds())

	for _, ps := range c.src.PeerSessions() {
		labels := c.peerLabelValues(ps.Peer)
		ch <- prometheus.MustNewConstMetric(c.peerSessionEstablished, prometheus.GaugeValue, establishedValue(ps.Established), labels...)
	}

	if src, ok := c.src.(EngineOutboundSource); ok {
		outbound := src.EngineOutbound()
		ch <- prometheus.MustNewConstMetric(c.engineTUNBytes, prometheus.CounterValue, float64(outbound.TUNBytes))
		ch <- prometheus.MustNewConstHistogram(c.engineTUNBatchFrames, outbound.TUNBatchFrames.Count, float64(outbound.TUNBatchFrames.Frames), histogramBuckets(outbound.TUNBatchFrames.Buckets))
		ch <- prometheus.MustNewConstMetric(c.engineSendBytes, prometheus.CounterValue, float64(outbound.SendBytes))
		ch <- prometheus.MustNewConstHistogram(c.engineSendBatchFrames, outbound.SendBatchFrames.Count, float64(outbound.SendBatchFrames.Frames), histogramBuckets(outbound.SendBatchFrames.Buckets))
		ch <- prometheus.MustNewConstMetric(c.engineEncryptionQueueContainers, prometheus.GaugeValue, float64(outbound.EncryptionQueueContainers))
		ch <- prometheus.MustNewConstMetric(c.engineEncryptionQueueHighWater, prometheus.GaugeValue, float64(outbound.EncryptionQueueHighWater))
		ch <- prometheus.MustNewConstMetric(c.enginePeerQueueContainers, prometheus.GaugeValue, float64(outbound.PeerQueueContainers))
		ch <- prometheus.MustNewConstMetric(c.enginePeerQueueHighWater, prometheus.GaugeValue, float64(outbound.PeerQueueHighWater))
		ch <- prometheus.MustNewConstMetric(c.engineActiveSendFrames, prometheus.GaugeValue, float64(outbound.ActiveSendFrames))
		ch <- prometheus.MustNewConstMetric(c.engineActiveSendBytes, prometheus.GaugeValue, float64(outbound.ActiveSendBytes))
		ch <- prometheus.MustNewConstMetric(c.engineActiveSendFramesHighWater, prometheus.GaugeValue, float64(outbound.ActiveSendFramesHighWater))
		ch <- prometheus.MustNewConstMetric(c.engineActiveSendBytesHighWater, prometheus.GaugeValue, float64(outbound.ActiveSendBytesHighWater))
		ch <- prometheus.MustNewConstMetric(c.engineAdmissionLimitBytes, prometheus.GaugeValue, float64(outbound.AdmissionLimitBytes))
		ch <- prometheus.MustNewConstMetric(c.engineAdmissionRetainedBytes, prometheus.GaugeValue, float64(outbound.AdmissionRetainedBytes))
		ch <- prometheus.MustNewConstMetric(c.engineAdmissionHighWaterBytes, prometheus.GaugeValue, float64(outbound.AdmissionHighWaterBytes))
		ch <- prometheus.MustNewConstMetric(c.engineAdmissionWaits, prometheus.CounterValue, float64(outbound.AdmissionWaits))
		ch <- prometheus.MustNewConstMetric(c.engineAdmissionWaitSeconds, prometheus.CounterValue, float64(outbound.AdmissionWaitNanoseconds)/float64(time.Second))
		ch <- prometheus.MustNewConstMetric(c.engineAdmissionOversizeBatches, prometheus.CounterValue, float64(outbound.AdmissionOversizeBatches))
	}
	if src, ok := c.src.(TUNAQMSource); ok {
		if snapshot := src.TUNAQM(); snapshot != nil {
			ch <- prometheus.MustNewConstMetric(c.tunAQMTargetRate, prometheus.GaugeValue, snapshot.TargetRateBytesPerSecond)
			ch <- prometheus.MustNewConstMetric(c.tunAQMActualRate, prometheus.GaugeValue, snapshot.ActualRateBytesPerSecond)
			ch <- prometheus.MustNewConstMetric(c.tunAQMTargetQueue, prometheus.GaugeValue, float64(snapshot.TargetTxQueueLen))
			ch <- prometheus.MustNewConstMetric(c.tunAQMActualQueue, prometheus.GaugeValue, float64(snapshot.ActualTxQueueLen))
			ch <- prometheus.MustNewConstMetric(c.tunAQMTargetEpoch, prometheus.GaugeValue, float64(snapshot.TargetEpoch))
			ch <- prometheus.MustNewConstMetric(c.tunAQMActualEpoch, prometheus.GaugeValue, float64(snapshot.ActualEpoch))
			ch <- prometheus.MustNewConstMetric(c.tunAQMTargetLimit, prometheus.GaugeValue, float64(snapshot.TargetQueueLimitPackets))
			ch <- prometheus.MustNewConstMetric(c.tunAQMActualLimit, prometheus.GaugeValue, float64(snapshot.ActualQueueLimitPackets))
			ch <- prometheus.MustNewConstMetric(c.tunAQMActualFlow, prometheus.GaugeValue, float64(snapshot.ActualFlowLimitPackets))
			ch <- prometheus.MustNewConstMetric(c.tunAQMTargetGSOMaxSize, prometheus.GaugeValue, float64(snapshot.TargetGSOMaxSizeBytes))
			ch <- prometheus.MustNewConstMetric(c.tunAQMActualGSOMaxSize, prometheus.GaugeValue, float64(snapshot.ActualGSOMaxSizeBytes))
			ch <- prometheus.MustNewConstMetric(c.tunAQMTargetGSOMaxSegments, prometheus.GaugeValue, float64(snapshot.TargetGSOMaxSegments))
			ch <- prometheus.MustNewConstMetric(c.tunAQMActualGSOMaxSegments, prometheus.GaugeValue, float64(snapshot.ActualGSOMaxSegments))
			ch <- prometheus.MustNewConstMetric(c.tunAQMTargetAdmissionLimit, prometheus.GaugeValue, float64(snapshot.TargetAdmissionLimitBytes))
			ch <- prometheus.MustNewConstMetric(c.tunAQMActualFresh, prometheus.GaugeValue, boolValue(snapshot.ActualFresh))
			ch <- prometheus.MustNewConstMetric(c.tunAQMRateFresh, prometheus.GaugeValue, boolValue(snapshot.RateFresh))
			ch <- prometheus.MustNewConstMetric(c.tunAQMActualQueueLength, prometheus.GaugeValue, float64(snapshot.ActualQueueLengthPackets))
			ch <- prometheus.MustNewConstMetric(c.tunAQMActualBacklog, prometheus.GaugeValue, float64(snapshot.ActualBacklogBytes))
			ch <- prometheus.MustNewConstMetric(c.tunAQMActualDrops, prometheus.CounterValue, float64(snapshot.ActualDrops))
			ch <- prometheus.MustNewConstMetric(c.tunAQMQueueLimitDeferred, prometheus.GaugeValue, boolValue(snapshot.QueueLimitDeferred))
			ch <- prometheus.MustNewConstMetric(c.tunAQMGSOLimitsDeferred, prometheus.GaugeValue, boolValue(snapshot.GSOLimitsDeferred))
			ch <- prometheus.MustNewConstMetric(c.tunAQMAdmissionDeferred, prometheus.GaugeValue, boolValue(snapshot.AdmissionLimitDeferred))
			ch <- prometheus.MustNewConstMetric(c.tunAQMObservedTime, prometheus.GaugeValue, timestampSeconds(snapshot.ActualObservedAt))
		}
	}
}

func histogramBuckets(buckets map[uint64]uint64) map[float64]uint64 {
	out := make(map[float64]uint64, len(buckets))
	for bound, count := range buckets {
		out[float64(bound)] = count
	}
	return out
}

func recoveryDirection(stats RecoveryStats, direction string) RecoveryDirectionStats {
	switch direction {
	case "sender":
		return stats.Sender
	case "receiver":
		return stats.Receiver
	default:
		panic("metrics: invalid recovery direction")
	}
}

// pathLabelValues returns the label values for a per-path series in Desc-declared
// order ({path} or {path,peer}) — see NewCollector's pathLabels.
func (c *collector) pathLabelValues(name, peer string) []string {
	if c.multiPeer {
		return []string{name, peer}
	}
	return []string{name}
}

// peerLabelValues returns the label values for a per-peer (FEC/resequencer) series:
// {peer} in multi-peer mode, no labels at all (the pre-T94 shape) otherwise.
func (c *collector) peerLabelValues(peer string) []string {
	if c.multiPeer {
		return []string{peer}
	}
	return nil
}

// establishedValue maps the WG-session liveness verdict to the
// wanbond_session_established gauge value.
func establishedValue(established bool) float64 {
	if established {
		return 1
	}
	return 0
}

// aggregatingValue maps the aggregation-gate verdict to the
// wanbond_aggregation_engaged gauge value.
func aggregatingValue(aggregating bool) float64 {
	if aggregating {
		return 1
	}
	return 0
}

func boolValue(value bool) float64 {
	if value {
		return 1
	}
	return 0
}

func timestampSeconds(value time.Time) float64 {
	if value.IsZero() {
		return 0
	}
	return float64(value.UnixNano()) / 1e9
}

// upValue maps a liveness verdict to the wanbond_path_up gauge value.
func upValue(s telemetry.PathState) float64 {
	if s == telemetry.StateUp {
		return 1
	}
	return 0
}
