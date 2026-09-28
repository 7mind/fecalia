package metrics

import (
	"strconv"

	"github.com/7mind/wanbond/internal/bond"
	"github.com/prometheus/client_golang/prometheus"
)

type AdaptiveSnapshot struct {
	Peer  string
	State bond.Snapshot
}

type AdaptiveSource interface {
	Adaptive() []AdaptiveSnapshot
}

type adaptiveMetric struct {
	desc  *prometheus.Desc
	kind  prometheus.ValueType
	value func(bond.PathStats) float64
}

type adaptiveCollector struct {
	source         AdaptiveSource
	paths          []adaptiveMetric
	drops, expired *prometheus.Desc
}

func newAdaptiveCollector(source AdaptiveSource) *adaptiveCollector {
	makeMetric := func(name, help string, kind prometheus.ValueType, value func(bond.PathStats) float64) adaptiveMetric {
		return adaptiveMetric{prometheus.NewDesc("wanbond_adaptive_"+name, help, []string{"peer", "lane"}, nil), kind, value}
	}
	return &adaptiveCollector{source: source, paths: []adaptiveMetric{
		makeMetric("target_rate_bytes_per_second", "Sender pacing target.", prometheus.GaugeValue, func(p bond.PathStats) float64 { return p.Rate }),
		makeMetric("delivery_rate_bytes_per_second", "Authenticated receiver delivery estimate.", prometheus.GaugeValue, func(p bond.PathStats) float64 { return p.DeliveryRate }),
		makeMetric("rtt_seconds", "DATA acknowledgement RTT.", prometheus.GaugeValue, func(p bond.PathStats) float64 { return p.RTT.Seconds() }),
		makeMetric("base_rtt_seconds", "Minimum DATA acknowledgement RTT.", prometheus.GaugeValue, func(p bond.PathStats) float64 { return p.BaseRTT.Seconds() }),
		makeMetric("queue_delay_seconds", "Forward transit delay above its observed minimum.", prometheus.GaugeValue, func(p bond.PathStats) float64 { return p.QueueDelay.Seconds() }),
		makeMetric("in_flight_bytes", "Unacknowledged wire bytes.", prometheus.GaugeValue, func(p bond.PathStats) float64 { return float64(p.InFlight) }),
		makeMetric("sent_bytes_total", "Wire bytes submitted by the adaptive sender.", prometheus.CounterValue, func(p bond.PathStats) float64 { return float64(p.Sent) }),
		makeMetric("acked_bytes_total", "Wire bytes acknowledged by the peer.", prometheus.CounterValue, func(p bond.PathStats) float64 { return float64(p.ACKed) }),
		makeMetric("repair_packets_total", "Additional copies, including small-packet replication.", prometheus.CounterValue, func(p bond.PathStats) float64 { return float64(p.Retransmits) }),
		makeMetric("up", "Authenticated lane lease is current and data acknowledgements have not stalled.", prometheus.GaugeValue, func(p bond.PathStats) float64 {
			if p.Up {
				return 1
			}
			return 0
		}),
	}, drops: prometheus.NewDesc("wanbond_adaptive_queue_drops_total", "Datagrams dropped by bounded queue admission or residence time.", []string{"peer"}, nil),
		expired: prometheus.NewDesc("wanbond_adaptive_expired_packets_total", "Packets whose bounded repair lifetime expired.", []string{"peer"}, nil)}
}

func (c *adaptiveCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, m := range c.paths {
		ch <- m.desc
	}
	ch <- c.drops
	ch <- c.expired
}

func (c *adaptiveCollector) Collect(ch chan<- prometheus.Metric) {
	for _, peer := range c.source.Adaptive() {
		for _, p := range peer.State.Paths {
			for _, m := range c.paths {
				ch <- prometheus.MustNewConstMetric(m.desc, m.kind, m.value(p), peer.Peer, strconv.Itoa(int(p.Path)))
			}
		}
		ch <- prometheus.MustNewConstMetric(c.drops, prometheus.CounterValue, float64(peer.State.QueueDrops), peer.Peer)
		ch <- prometheus.MustNewConstMetric(c.expired, prometheus.CounterValue, float64(peer.State.Expired), peer.Peer)
	}
}
