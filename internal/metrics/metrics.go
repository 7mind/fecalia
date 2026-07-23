package metrics

import (
	"net/netip"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/7mind/wanbond/internal/reseq"
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
	// MetricProbeSendErrors is the per-path cumulative count of PROBE-frame socket
	// write errors emitProbes has dropped (defect D96 item 4, composes with D90):
	// a path whose probes cannot egress was previously indistinguishable from a
	// path with 100% probe loss. Sourced verbatim from PathSnapshot.ProbeSendErrors.
	MetricProbeSendErrors = "wanbond_path_probe_send_errors_total"
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
	// frames/second (Pick calls per second, EWMA) driving the gate.
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
	ResidualLossRatio float64
	// Adaptive is the adaptive-FEC controller's most recent published decision (T263,
	// D96), mirrored verbatim from bind.FECStats.Adaptive. It is nil for a fixed-ratio or
	// FEC-off peer, so the collector fabricates no adaptive series where none exists — the
	// AggregationSnapshot absent-series precedent (T146).
	Adaptive *AdaptiveFECStats
}

// AdaptiveFECStats is the adaptive-FEC controller's per-drive decision (T263, D96),
// mirrored verbatim from bind.AdaptiveFECStats: Parity is the target parity count M the
// encoder was retargeted to (ctrl.Parity()); SmoothedLoss the controller's EWMA loss
// estimate; EligibleLoss the raw probe-measured loss the drive Observed over the
// sample-eligible data-carrying paths (T272 — the active path's loss under
// active-backup, the weight-weighted mix under weighted striping); EligiblePaths the
// count of those paths (0 on the hold branch).
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
	// ProbeSendErrors is the cumulative count of PROBE-frame socket write errors
	// emitProbes has dropped for this path (defect D96 item 4), read verbatim from
	// bind.PathTraffic.ProbeSendErrors.
	ProbeSendErrors uint64
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
	// PeerNames returns the STATIC set of bound peer names (BoundPeerNames order):
	// len == 1 selects the single-peer back-compat exposition (the `peer` label is
	// omitted); len > 1 selects the per-peer exposition (see the package-level
	// back-compat rule).
	PeerNames() []string
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

	txBytes    *prometheus.Desc
	rxBytes    *prometheus.Desc
	loss       *prometheus.Desc
	rtt        *prometheus.Desc
	jitter     *prometheus.Desc
	throughput *prometheus.Desc
	up         *prometheus.Desc
	pmtu       *prometheus.Desc
	probeErrs  *prometheus.Desc

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

	aggregationEngaged    *prometheus.Desc
	offeredLoad           *prometheus.Desc
	aggregationEngageTh   *prometheus.Desc
	aggregationDisengageT *prometheus.Desc

	sessionEstablished   *prometheus.Desc
	sessionLastHandshake *prometheus.Desc
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
	return &collector{
		src:        src,
		multiPeer:  multiPeer,
		txBytes:    desc(pathSubsystem, "tx_bytes_total", "Total bytes transmitted on the path.", pathLabels),
		rxBytes:    desc(pathSubsystem, "rx_bytes_total", "Total bytes received on the path.", pathLabels),
		loss:       desc(pathSubsystem, "loss_ratio", "Per-path probe loss fraction in [0,1].", pathLabels),
		rtt:        desc(pathSubsystem, "rtt_seconds", "Smoothed per-path round-trip time in seconds.", pathLabels),
		jitter:     desc(pathSubsystem, "jitter_seconds", "Smoothed per-path RTT deviation (jitter) in seconds.", pathLabels),
		throughput: desc(pathSubsystem, "throughput_bits_per_second", "Current per-path throughput in bits per second.", pathLabels),
		up:         desc(pathSubsystem, "up", "Per-path liveness (1 = up, 0 = down).", pathLabels),
		pmtu:       desc(pathSubsystem, "mtu", "Per-path discovered outer path MTU in bytes (configured value on a pinned path, else the largest padded-probe on-wire size that echoes).", pathLabels),
		probeErrs:  desc(pathSubsystem, "probe_send_errors_total", "Per-path PROBE-frame socket write errors (count-and-continue; a path whose probes cannot egress is otherwise indistinguishable from 100% probe loss).", pathLabels),

		fecData:          desc(fecSubsystem, "data_packets_total", "FEC DATA packets emitted (the fixed-ratio overhead denominator).", peerScopedLabels),
		fecRepair:        desc(fecSubsystem, "repair_packets_total", "FEC parity packets emitted (the fixed-ratio overhead).", peerScopedLabels),
		fecRecovered:     desc(fecSubsystem, "recovered_packets_total", "Data packets reconstructed via FEC.", peerScopedLabels),
		fecUnrecoverable: desc(fecSubsystem, "unrecoverable_packets_total", "Data packets lost beyond FEC repair capacity.", peerScopedLabels),
		fecDataBytes:     desc(fecSubsystem, "data_bytes_total", "FEC DATA-frame wire bytes emitted (the byte overhead denominator).", peerScopedLabels),
		fecRepairBytes:   desc(fecSubsystem, "repair_bytes_total", "FEC parity-frame wire bytes emitted (the byte overhead numerator).", peerScopedLabels),
		fecResidualLoss:  desc(fecSubsystem, "residual_loss_ratio", "Post-FEC-recovery connection loss fraction in [0,1] (loss FEC did not mask).", peerScopedLabels),

		fecAdaptiveParity:   desc(fecSubsystem, "adaptive_parity", "Adaptive-FEC controller's current target parity count M (present only while the controller is engaged).", peerScopedLabels),
		fecSmoothedLoss:     desc(fecSubsystem, "smoothed_loss", "Adaptive-FEC controller's EWMA smoothed loss estimate in [0,1] (present only while the controller is engaged).", peerScopedLabels),
		fecEligiblePathLoss: desc(fecSubsystem, "eligible_path_loss", "Raw probe-measured loss the adaptive-FEC drive Observed over the sample-eligible data-carrying paths: the active path's loss under active-backup, the weight-weighted mix under weighted striping (present only while the controller is engaged).", peerScopedLabels),
		fecEligiblePaths:    desc(fecSubsystem, "eligible_paths", "Count of sample-eligible data-carrying paths the adaptive-FEC drive considered; 0 on the hold branch (present only while the controller is engaged).", peerScopedLabels),

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

		aggregationEngaged:    desc(aggregationSubsystem, "engaged", "Weighted-scheduler aggregation gate (1 = striping across every eligible path, 0 = collapsed to primary-only).", peerScopedLabels),
		offeredLoad:           desc("", "offered_load_fps", "Smoothed offered load in frames/second (EWMA of Pick calls) driving the aggregation gate.", peerScopedLabels),
		aggregationEngageTh:   desc(aggregationSubsystem, "engage_threshold_fps", "Static engage threshold in frames/second (engage_fraction * per_path_capacity_fps): offered load above which the gate engages.", peerScopedLabels),
		aggregationDisengageT: desc(aggregationSubsystem, "disengage_threshold_fps", "Static disengage threshold in frames/second (disengage_fraction * per_path_capacity_fps): offered load below which, sustained, the gate collapses.", peerScopedLabels),

		sessionEstablished:   desc(sessionSubsystem, "established", "WG session liveness (1 = a handshake has completed and is still fresh, 0 = still converging or wedged).", nil),
		sessionLastHandshake: desc(sessionSubsystem, "last_handshake_seconds", "Age in seconds of the peer's most recent completed WG handshake (0 when none has completed).", nil),
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
	ch <- c.aggregationEngaged
	ch <- c.offeredLoad
	ch <- c.aggregationEngageTh
	ch <- c.aggregationDisengageT
	ch <- c.sessionEstablished
	ch <- c.sessionLastHandshake
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

// upValue maps a liveness verdict to the wanbond_path_up gauge value.
func upValue(s telemetry.PathState) float64 {
	if s == telemetry.StateUp {
		return 1
	}
	return 0
}
