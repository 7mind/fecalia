package metrics

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/7mind/wanbond/internal/telemetry"
)

// namespace prefixes every wanbond metric name; pathSubsystem and fecSubsystem
// partition the per-path and (future) FEC series.
const (
	namespace     = "wanbond"
	pathSubsystem = "path"
	fecSubsystem  = "fec"

	// labelPath is the single label carried by every per-path series; its value is
	// the stable path name from configuration (e.g. "starlink").
	labelPath = "path"
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

// FECSnapshot is the current connection-scoped FEC signal set the exposition layer
// reports (T24). It is sourced from the multipath Bind's FEC counters, read at scrape
// time like the per-path snapshots. All zero when FEC is disabled.
type FECSnapshot struct {
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
	// Name is the stable path identifier used as the `path` label. It must be
	// unique within a single Source.Paths() result.
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

// Source is the read-only seam between the traffic/telemetry planes and the
// exposition layer. The collector calls Paths at every scrape, so an
// implementation must be safe for concurrent use and must return a consistent
// snapshot (unique path names) cheaply — it is on the scrape hot path.
type Source interface {
	// Paths returns the current per-path snapshots.
	Paths() []PathSnapshot
	// FEC returns the current connection-scoped FEC counters (T24).
	FEC() FECSnapshot
}

// collector is a prometheus.Collector that reads a Source at scrape time and
// emits per-path const-metrics plus the placeholder FEC counters. Reading at
// scrape time (rather than mirroring into GaugeVecs on an update path) keeps the
// exposition consistent with the live telemetry with no duplicated state and no
// staleness window.
type collector struct {
	src Source

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
}

// NewCollector builds the wanbond metrics collector over src. Register it into a
// dedicated prometheus.Registry (see NewServer); it deliberately does not touch
// the global default registry (no-globals discipline).
func NewCollector(src Source) prometheus.Collector {
	pathLabels := []string{labelPath}
	desc := func(subsystem, name, help string, labels []string) *prometheus.Desc {
		return prometheus.NewDesc(prometheus.BuildFQName(namespace, subsystem, name), help, labels, nil)
	}
	return &collector{
		src:        src,
		txBytes:    desc(pathSubsystem, "tx_bytes_total", "Total bytes transmitted on the path.", pathLabels),
		rxBytes:    desc(pathSubsystem, "rx_bytes_total", "Total bytes received on the path.", pathLabels),
		loss:       desc(pathSubsystem, "loss_ratio", "Per-path probe loss fraction in [0,1].", pathLabels),
		rtt:        desc(pathSubsystem, "rtt_seconds", "Smoothed per-path round-trip time in seconds.", pathLabels),
		jitter:     desc(pathSubsystem, "jitter_seconds", "Smoothed per-path RTT deviation (jitter) in seconds.", pathLabels),
		throughput: desc(pathSubsystem, "throughput_bits_per_second", "Current per-path throughput in bits per second.", pathLabels),
		up:         desc(pathSubsystem, "up", "Per-path liveness (1 = up, 0 = down).", pathLabels),

		fecData:          desc(fecSubsystem, "data_packets_total", "FEC DATA packets emitted (the fixed-ratio overhead denominator).", nil),
		fecRepair:        desc(fecSubsystem, "repair_packets_total", "FEC parity packets emitted (the fixed-ratio overhead).", nil),
		fecRecovered:     desc(fecSubsystem, "recovered_packets_total", "Data packets reconstructed via FEC.", nil),
		fecUnrecoverable: desc(fecSubsystem, "unrecoverable_packets_total", "Data packets lost beyond FEC repair capacity.", nil),
		fecDataBytes:     desc(fecSubsystem, "data_bytes_total", "FEC DATA-frame wire bytes emitted (the byte overhead denominator).", nil),
		fecRepairBytes:   desc(fecSubsystem, "repair_bytes_total", "FEC parity-frame wire bytes emitted (the byte overhead numerator).", nil),
		fecResidualLoss:  desc(fecSubsystem, "residual_loss_ratio", "Post-FEC-recovery connection loss fraction in [0,1] (loss FEC did not mask).", nil),
	}
}

// Describe sends every descriptor; the collector's series set is fixed even
// though its per-path label values are discovered at Collect time.
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
}

// Collect reads the Source once and emits one const-metric per per-path series,
// then the three connection-scoped FEC counters read from the live FEC plane.
func (c *collector) Collect(ch chan<- prometheus.Metric) {
	for _, p := range c.src.Paths() {
		ch <- prometheus.MustNewConstMetric(c.txBytes, prometheus.CounterValue, float64(p.TxBytes), p.Name)
		ch <- prometheus.MustNewConstMetric(c.rxBytes, prometheus.CounterValue, float64(p.RxBytes), p.Name)
		ch <- prometheus.MustNewConstMetric(c.loss, prometheus.GaugeValue, p.Estimate.Loss, p.Name)
		ch <- prometheus.MustNewConstMetric(c.rtt, prometheus.GaugeValue, p.Estimate.RTT.Seconds(), p.Name)
		ch <- prometheus.MustNewConstMetric(c.jitter, prometheus.GaugeValue, p.Estimate.Jitter.Seconds(), p.Name)
		ch <- prometheus.MustNewConstMetric(c.throughput, prometheus.GaugeValue, p.ThroughputBitsPerSecond, p.Name)
		ch <- prometheus.MustNewConstMetric(c.up, prometheus.GaugeValue, upValue(p.State), p.Name)
	}
	f := c.src.FEC()
	ch <- prometheus.MustNewConstMetric(c.fecData, prometheus.CounterValue, float64(f.DataPackets))
	ch <- prometheus.MustNewConstMetric(c.fecRepair, prometheus.CounterValue, float64(f.RepairPackets))
	ch <- prometheus.MustNewConstMetric(c.fecRecovered, prometheus.CounterValue, float64(f.RecoveredPackets))
	ch <- prometheus.MustNewConstMetric(c.fecUnrecoverable, prometheus.CounterValue, float64(f.UnrecoverablePackets))
	ch <- prometheus.MustNewConstMetric(c.fecDataBytes, prometheus.CounterValue, float64(f.DataBytes))
	ch <- prometheus.MustNewConstMetric(c.fecRepairBytes, prometheus.CounterValue, float64(f.RepairBytes))
	ch <- prometheus.MustNewConstMetric(c.fecResidualLoss, prometheus.GaugeValue, f.ResidualLossRatio)
}

// upValue maps a liveness verdict to the wanbond_path_up gauge value.
func upValue(s telemetry.PathState) float64 {
	if s == telemetry.StateUp {
		return 1
	}
	return 0
}
