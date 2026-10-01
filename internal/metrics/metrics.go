package metrics

import (
	"net/netip"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/7mind/wanbond/internal/reseq"
	"github.com/7mind/wanbond/internal/telemetry"
)

// Per-peer labelling (T94). A concentrator (G4) binds multiple peers, each with its own
// path set and resequencer, so /metrics must attribute path/resequencer
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
// namespace prefixes every wanbond metric name; pathSubsystem,
// resequencerSubsystem, and sessionSubsystem partition the per-path, resequencer,
// and WG-session series.
const (
	namespace            = "wanbond"
	pathSubsystem        = "path"
	resequencerSubsystem = "resequencer"
	sessionSubsystem     = "session"
	engineSubsystem      = "engine"

	// labelPath is the single label carried by every per-path series; its value is
	// the stable path name from configuration (e.g. "starlink").
	labelPath = "path"
	// labelPeer is the per-bound-peer label (T94) carried by path/resequencer series
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
	MetricProbeSendErrors = "wanbond_path_probe_send_errors_total"
	// MetricSocketWriteErrors is the per-path cumulative count of UDP socket write
	// errors for transport datagrams; generated PROBE and reflected-echo failures are
	// excluded. Sourced verbatim from PathSnapshot.SocketWriteErrors.
	MetricSocketWriteErrors = "wanbond_path_socket_write_errors_total"
)

// Resequencer metric names (T94). These are per-PEER (a peer's
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
	// HoL-stall / hold signal (T242). The holds/hold-seconds counter PAIR gives
	// operators the mean hold (hold_seconds_total / holds_total — the 250 ms class
	// of latency that was previously invisible). hold_seconds_total is derived from
	// the resequencer's nanosecond accumulator (HoldNanos) at scrape time.
	MetricReseqHolds       = "wanbond_resequencer_hol_holds_total"
	MetricReseqHoldSeconds = "wanbond_resequencer_hol_hold_seconds_total"
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
)

// MetricLivenessBudgetSane is the D86-decision-4 WARN-arm failover-budget gauge (T211).
// Unlike every other series above, the value is
// CONFIG-DERIVED (not sourced from Source at scrape time): seeded at daemon startup from
// config.Config.LivenessBudgetSane and re-set on a reload whose applied path add/remove
// changes the worst-case ride_through (Server.SetLivenessBudgetSane), registered as a
// gauge alongside (not through) the Source-driven collector — see NewServer. It carries no
// labels and is present for EVERY config (the failover budget
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

// newLivenessBudgetGauge builds the wanbond_liveness_budget_sane gauge (T211) seeded at
// sane's value. Config-derived, not
// re-read at scrape time; the concrete gauge is returned so the Server retains it and can
// re-set it on a reload whose applied path change moved the worst-case ride_through.
func newLivenessBudgetGauge(sane bool) prometheus.Gauge {
	g := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "liveness_budget_sane",
		Help:      "Config-derived failover-budget verdict (1 = per-direction failover budget fits the 3s P1 recovery deadline, 0 = over-budget; see docs/design.md).",
	})
	g.Set(boolValue(sane))
	return g
}

// newTunMTUGauge builds the wanbond_tun_mtu gauge (T209, defect D85) seeded to the
// boot-time TUN MTU. Like the sanity gauge above it is retained (not re-read from
// Source at scrape time): the concrete prometheus.Gauge is returned so the Server can
// re-set it via SetTunMTU when the runtime resizer adjusts the live link. It is present
// for every config (the TUN always has an MTU).
func newTunMTUGauge(mtu int) prometheus.Gauge {
	g := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "tun_mtu",
		Help:      "Current wanbond0 link (TUN) MTU in bytes: the min inner MTU across UP paths (T209, defect D85).",
	})
	g.Set(float64(mtu))
	return g
}

// PathSnapshot is the current per-path signal set the exposition layer reports.
// It fuses traffic accounting (TxBytes/RxBytes/Throughput, sourced from the bind)
// with the telemetry plane's quality estimate and liveness verdict
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
	ProbeSendErrors uint64
	// SocketWriteErrors is the cumulative count of UDP socket write errors for
	// transport datagrams on this path. Read verbatim from bind.PathTraffic.
	SocketWriteErrors uint64
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
// resequencer with no local aggregation. It is per-PEER, not
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
// exposition layer. The collector calls Paths/Reseq at every scrape, so an
// implementation must be safe for concurrent use and must return a consistent
// snapshot (unique (peer,path) names) cheaply — it is on the scrape hot path.
// PeerNames, by contrast, is queried ONCE at NewCollector construction (see the
// package-level back-compat rule) — implementations may compute it fresh from the
// same underlying per-peer state Paths/Reseq read; it need not be cached.
type Source interface {
	// Paths returns the current per-(peer,path) snapshots.
	Paths() []PathSnapshot
	// Reseq returns the current per-peer resequencer counters (T94).
	Reseq() []ReseqSnapshot
	// Adaptive returns the current per-peer transport state: lanes, their
	// control state and the transport's queue counters.
	Adaptive() []AdaptiveSnapshot
	// Session returns the current connection-scoped WG-session snapshot (I2).
	Session() SessionSnapshot
	// PeerSessions returns the current per-peer WG-session snapshot (T256, G28, M106):
	// ONE entry per bound peer's OWN session health, distinct from the connection-scoped
	// Session() above. Follows the same PeerNames()-driven back-compat rule as
	// Paths/Reseq: exactly one entry (Peer "") for a single-bound-peer
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
}

type EngineOutboundSource interface {
	EngineOutbound() EngineOutboundSnapshot
}

// collector is a prometheus.Collector that reads a Source at scrape time and
// emits per-path const-metrics plus the resequencer counters. Reading at
// scrape time (rather than mirroring into GaugeVecs on an update path) keeps the
// exposition consistent with the live telemetry with no duplicated state and no
// staleness window. multiPeer is decided once at construction (see the
// package-level back-compat rule) and gates whether the `peer` label is ever
// attached, for the collector's whole life.
type collector struct {
	src       Source
	multiPeer bool

	txBytes      *prometheus.Desc
	rxBytes      *prometheus.Desc
	loss         *prometheus.Desc
	rtt          *prometheus.Desc
	jitter       *prometheus.Desc
	throughput   *prometheus.Desc
	up           *prometheus.Desc
	pmtu         *prometheus.Desc
	probeErrs    *prometheus.Desc
	socketErrors *prometheus.Desc

	reseqReleased       *prometheus.Desc
	reseqDroppedDup     *prometheus.Desc
	reseqDroppedOld     *prometheus.Desc
	reseqDroppedSuspect *prometheus.Desc
	reseqSkipped        *prometheus.Desc
	reseqResyncs        *prometheus.Desc
	reseqRebaselines    *prometheus.Desc
	reseqHolds          *prometheus.Desc
	reseqHoldSeconds    *prometheus.Desc
	reseqMetrics        []reseqMetric

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
	makeReseqMetric := func(
		name string,
		help string,
		valueType prometheus.ValueType,
		value func(ReseqSnapshot) float64,
	) reseqMetric {
		return reseqMetric{desc: desc(resequencerSubsystem, name, help, peerScopedLabels), valueType: valueType, value: value}
	}
	return &collector{
		src:          src,
		multiPeer:    multiPeer,
		txBytes:      desc(pathSubsystem, "tx_bytes_total", "Total bytes transmitted on the path.", pathLabels),
		rxBytes:      desc(pathSubsystem, "rx_bytes_total", "Total bytes received on the path.", pathLabels),
		loss:         desc(pathSubsystem, "loss_ratio", "Per-path probe loss fraction in [0,1].", pathLabels),
		rtt:          desc(pathSubsystem, "rtt_seconds", "Smoothed per-path round-trip time in seconds.", pathLabels),
		jitter:       desc(pathSubsystem, "jitter_seconds", "Smoothed per-path RTT deviation (jitter) in seconds.", pathLabels),
		throughput:   desc(pathSubsystem, "throughput_bits_per_second", "Current per-path throughput in bits per second.", pathLabels),
		up:           desc(pathSubsystem, "up", "Per-path liveness (1 = up, 0 = down).", pathLabels),
		pmtu:         desc(pathSubsystem, "mtu", "Per-path discovered outer path MTU in bytes (configured value on a pinned path, else the largest padded-probe on-wire size that echoes).", pathLabels),
		probeErrs:    desc(pathSubsystem, "probe_send_errors_total", "Unexpected locally-originated ordinary/PMTU PROBE socket write failures. Expected PMTU EMSGSIZE too-large verdicts are excluded; PMTU failures return to discovery and ordinary failures are counted then discarded.", pathLabels),
		socketErrors: desc(pathSubsystem, "socket_write_errors_total", "UDP socket write errors for transport datagrams; excludes generated outer PROBE and reflected-echo failures.", pathLabels),

		reseqReleased:       desc(resequencerSubsystem, "released_frames_total", "Frames released for delivery by the resequencer.", peerScopedLabels),
		reseqDroppedDup:     desc(resequencerSubsystem, "dropped_duplicate_frames_total", "Frames dropped by the resequencer as duplicates.", peerScopedLabels),
		reseqDroppedOld:     desc(resequencerSubsystem, "dropped_stale_frames_total", "Frames dropped by the resequencer as already past the release point.", peerScopedLabels),
		reseqDroppedSuspect: desc(resequencerSubsystem, "dropped_suspect_frames_total", "Out-of-band frames dropped by the resequencer while not yet corroborating.", peerScopedLabels),
		reseqSkipped:        desc(resequencerSubsystem, "skipped_seqs_total", "Sequence numbers skipped (lost) by the resequencer's window-advance or timeout.", peerScopedLabels),
		reseqResyncs:        desc(resequencerSubsystem, "resyncs_total", "Resequencer release-point re-pins after a corroborated discontinuity.", peerScopedLabels),
		reseqRebaselines:    desc(resequencerSubsystem, "rebaselines_total", "Resequencer release-point re-baselines forced by a trusted control event (e.g. hub failover).", peerScopedLabels),

		reseqHolds:       desc(resequencerSubsystem, "hol_holds_total", "Head-of-line gaps that armed a hold (denominator of the mean hold; pair with hol_hold_seconds_total).", peerScopedLabels),
		reseqHoldSeconds: desc(resequencerSubsystem, "hol_hold_seconds_total", "Cumulative seconds head-of-line gaps spent held before a timeout skip or a fill (numerator of the mean hold).", peerScopedLabels),
		reseqMetrics: []reseqMetric{
			makeReseqMetric("armed_deadline_timestamp_seconds", "Absolute Unix timestamp in seconds of the live head-of-line deadline; 0 while disarmed.", prometheus.GaugeValue, func(r ReseqSnapshot) float64 { return timestampSeconds(r.ArmedDeadline) }),
			makeReseqMetric("armed_window_seconds", "Duration in seconds from the live gap arm instant to its deadline; 0 while disarmed.", prometheus.GaugeValue, func(r ReseqSnapshot) float64 { return r.ArmedWindow.Seconds() }),
			makeReseqMetric("deadline_wakeups_total", "Cumulative head-of-line releases evaluated at or after their armed deadline.", prometheus.CounterValue, func(r ReseqSnapshot) float64 { return float64(r.DeadlineWakeups) }),
			makeReseqMetric("gap_fills_total", "Cumulative armed head-of-line gaps filled before release.", prometheus.CounterValue, func(r ReseqSnapshot) float64 { return float64(r.GapFills) }),
		},

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
	ch <- c.socketErrors
	ch <- c.reseqReleased
	ch <- c.reseqDroppedDup
	ch <- c.reseqDroppedOld
	ch <- c.reseqDroppedSuspect
	ch <- c.reseqSkipped
	ch <- c.reseqResyncs
	ch <- c.reseqRebaselines
	ch <- c.reseqHolds
	ch <- c.reseqHoldSeconds
	for _, metric := range c.reseqMetrics {
		ch <- metric.desc
	}
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
}

// Collect reads the Source once and emits one const-metric per per-(peer,path)
// series, then the per-peer resequencer counters, then the two
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
		ch <- prometheus.MustNewConstMetric(c.socketErrors, prometheus.CounterValue, float64(p.SocketWriteErrors), labels...)
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
		for _, metric := range c.reseqMetrics {
			ch <- prometheus.MustNewConstMetric(metric.desc, metric.valueType, metric.value(r), labels...)
		}
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
	}
}

func histogramBuckets(buckets map[uint64]uint64) map[float64]uint64 {
	out := make(map[float64]uint64, len(buckets))
	for bound, count := range buckets {
		out[float64(bound)] = count
	}
	return out
}

// pathLabelValues returns the label values for a per-path series in Desc-declared
// order ({path} or {path,peer}) — see NewCollector's pathLabels.
func (c *collector) pathLabelValues(name, peer string) []string {
	if c.multiPeer {
		return []string{name, peer}
	}
	return []string{name}
}

// peerLabelValues returns the label values for a per-peer (resequencer/session) series:
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
