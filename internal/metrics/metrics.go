package metrics

import (
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
// scrape time: its value is fixed once at daemon startup from the loaded
// config.Config.WeightedCapacitySane verdict, registered as a STATIC gauge alongside
// (not through) the Source-driven collector — see NewServer. It carries no labels at
// all (config-derived, not per-peer — exempt from the labelPeer back-compat rule) and
// the family is ABSENT ENTIRELY under the active-backup policy (a nil verdict). Under
// the weighted policy it reads 1 when every path declares link_bandwidth (SANE-VERIFIED)
// or 0 when at least one path's declaration is missing or partial (UNVERIFIABLE) — see
// docs/install.md §3a for the operator remedy.
const MetricWeightedCapacitySane = "wanbond_weighted_capacity_sane"

// newWeightedCapacityGauge builds the static wanbond_weighted_capacity_sane gauge
// (T144) fixed at sane's value for the collector's whole life — unlike collector, it
// is never re-read at scrape time, matching its config-derived (not live-telemetry)
// source of truth.
func newWeightedCapacityGauge(sane bool) prometheus.Collector {
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

	fecData          *prometheus.Desc
	fecRepair        *prometheus.Desc
	fecRecovered     *prometheus.Desc
	fecUnrecoverable *prometheus.Desc
	fecDataBytes     *prometheus.Desc
	fecRepairBytes   *prometheus.Desc
	fecResidualLoss  *prometheus.Desc

	reseqReleased       *prometheus.Desc
	reseqDroppedDup     *prometheus.Desc
	reseqDroppedOld     *prometheus.Desc
	reseqDroppedSuspect *prometheus.Desc
	reseqSkipped        *prometheus.Desc
	reseqResyncs        *prometheus.Desc
	reseqRebaselines    *prometheus.Desc

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

		fecData:          desc(fecSubsystem, "data_packets_total", "FEC DATA packets emitted (the fixed-ratio overhead denominator).", peerScopedLabels),
		fecRepair:        desc(fecSubsystem, "repair_packets_total", "FEC parity packets emitted (the fixed-ratio overhead).", peerScopedLabels),
		fecRecovered:     desc(fecSubsystem, "recovered_packets_total", "Data packets reconstructed via FEC.", peerScopedLabels),
		fecUnrecoverable: desc(fecSubsystem, "unrecoverable_packets_total", "Data packets lost beyond FEC repair capacity.", peerScopedLabels),
		fecDataBytes:     desc(fecSubsystem, "data_bytes_total", "FEC DATA-frame wire bytes emitted (the byte overhead denominator).", peerScopedLabels),
		fecRepairBytes:   desc(fecSubsystem, "repair_bytes_total", "FEC parity-frame wire bytes emitted (the byte overhead numerator).", peerScopedLabels),
		fecResidualLoss:  desc(fecSubsystem, "residual_loss_ratio", "Post-FEC-recovery connection loss fraction in [0,1] (loss FEC did not mask).", peerScopedLabels),

		reseqReleased:       desc(resequencerSubsystem, "released_frames_total", "Frames released for delivery by the resequencer.", peerScopedLabels),
		reseqDroppedDup:     desc(resequencerSubsystem, "dropped_duplicate_frames_total", "Frames dropped by the resequencer as duplicates.", peerScopedLabels),
		reseqDroppedOld:     desc(resequencerSubsystem, "dropped_stale_frames_total", "Frames dropped by the resequencer as already past the release point.", peerScopedLabels),
		reseqDroppedSuspect: desc(resequencerSubsystem, "dropped_suspect_frames_total", "Out-of-band frames dropped by the resequencer while not yet corroborating.", peerScopedLabels),
		reseqSkipped:        desc(resequencerSubsystem, "skipped_seqs_total", "Sequence numbers skipped (lost) by the resequencer's window-advance or timeout.", peerScopedLabels),
		reseqResyncs:        desc(resequencerSubsystem, "resyncs_total", "Resequencer release-point re-pins after a corroborated discontinuity.", peerScopedLabels),
		reseqRebaselines:    desc(resequencerSubsystem, "rebaselines_total", "Resequencer release-point re-baselines forced by a trusted control event (e.g. hub failover).", peerScopedLabels),

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
	ch <- c.fecData
	ch <- c.fecRepair
	ch <- c.fecRecovered
	ch <- c.fecUnrecoverable
	ch <- c.fecDataBytes
	ch <- c.fecRepairBytes
	ch <- c.fecResidualLoss
	ch <- c.reseqReleased
	ch <- c.reseqDroppedDup
	ch <- c.reseqDroppedOld
	ch <- c.reseqDroppedSuspect
	ch <- c.reseqSkipped
	ch <- c.reseqResyncs
	ch <- c.reseqRebaselines
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

// upValue maps a liveness verdict to the wanbond_path_up gauge value.
func upValue(s telemetry.PathState) float64 {
	if s == telemetry.StateUp {
		return 1
	}
	return 0
}
