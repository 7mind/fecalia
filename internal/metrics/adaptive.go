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
	source                              AdaptiveSource
	paths                               []adaptiveMetric
	drops, expired                      *prometheus.Desc
	interactiveDrops, interactiveQueued *prometheus.Desc
	coalescedACKs                       *prometheus.Desc
	admissionDrops, aqmDrops            *prometheus.Desc
}

func newAdaptiveCollector(source AdaptiveSource) *adaptiveCollector {
	makeMetric := func(name, help string, kind prometheus.ValueType, value func(bond.PathStats) float64) adaptiveMetric {
		return adaptiveMetric{prometheus.NewDesc("wanbond_adaptive_"+name, help, []string{"peer", "lane"}, nil), kind, value}
	}
	return &adaptiveCollector{source: source, paths: []adaptiveMetric{
		makeMetric("target_rate_bytes_per_second", "Sender pacing target.", prometheus.GaugeValue, func(p bond.PathStats) float64 { return p.Rate }),
		makeMetric("send_rate_bytes_per_second", "Measured wire rate submitted by the adaptive sender.", prometheus.GaugeValue, func(p bond.PathStats) float64 { return p.SendRate }),
		makeMetric("delivery_rate_bytes_per_second", "Authenticated receiver delivery estimate.", prometheus.GaugeValue, func(p bond.PathStats) float64 { return p.DeliveryRate }),
		makeMetric("rtt_seconds", "DATA acknowledgement RTT.", prometheus.GaugeValue, func(p bond.PathStats) float64 { return p.RTT.Seconds() }),
		makeMetric("rtt_variation_seconds", "Smoothed absolute DATA acknowledgement RTT variation.", prometheus.GaugeValue, func(p bond.PathStats) float64 { return p.RTTVariation.Seconds() }),
		makeMetric("unloaded_rtt_variation_seconds", "RTT variation observed after two seconds without application datagram transmission.", prometheus.GaugeValue, func(p bond.PathStats) float64 { return p.IdleRTTVariation.Seconds() }),
		makeMetric("unloaded_forward_variation_seconds", "Keepalive forward-transit variation after two seconds without application datagram transmission; excludes ACK return delay.", prometheus.GaugeValue, func(p bond.PathStats) float64 { return p.IdleForwardVariation.Seconds() }),
		makeMetric("feedback_rtt_seconds", "Smoothed time until newly confirmed deliveries were acknowledged, including receipt buffering.", prometheus.GaugeValue, func(p bond.PathStats) float64 { return p.FeedbackRTT.Seconds() }),
		makeMetric("feedback_rtt_variation_seconds", "Smoothed absolute delivery-confirmation RTT variation.", prometheus.GaugeValue, func(p bond.PathStats) float64 { return p.FeedbackRTTVariation.Seconds() }),
		makeMetric("base_rtt_seconds", "Minimum DATA acknowledgement RTT.", prometheus.GaugeValue, func(p bond.PathStats) float64 { return p.BaseRTT.Seconds() }),
		makeMetric("queue_delay_seconds", "Forward transit delay above its observed minimum.", prometheus.GaugeValue, func(p bond.PathStats) float64 { return p.QueueDelay.Seconds() }),
		makeMetric("in_flight_bytes", "Unacknowledged wire bytes.", prometheus.GaugeValue, func(p bond.PathStats) float64 { return float64(p.InFlight) }),
		makeMetric("window_bytes", "Current in-flight allowance, including startup discovery limit.", prometheus.GaugeValue, func(p bond.PathStats) float64 { return float64(p.Window) }),
		makeMetric("sent_bytes_total", "Wire bytes submitted by the adaptive sender.", prometheus.CounterValue, func(p bond.PathStats) float64 { return float64(p.Sent) }),
		makeMetric("interactive_sent_bytes_total", "Wire bytes of small datagrams submitted on this lane, including copies; included in sent_bytes_total.", prometheus.CounterValue, func(p bond.PathStats) float64 { return float64(p.InteractiveSent) }),
		makeMetric("acked_bytes_total", "Wire bytes acknowledged by the peer.", prometheus.CounterValue, func(p bond.PathStats) float64 { return float64(p.ACKed) }),
		makeMetric("repair_packets_total", "Additional copies, including small-packet replication.", prometheus.CounterValue, func(p bond.PathStats) float64 { return float64(p.Retransmits) }),
		makeMetric("discovering", "Lane has not yet observed a congestion signal and follows measured delivery.", prometheus.GaugeValue, func(p bond.PathStats) float64 {
			if p.Discovering {
				return 1
			}
			return 0
		}),
		makeMetric("up", "Authenticated lane lease is current and data acknowledgements have not stalled.", prometheus.GaugeValue, func(p bond.PathStats) float64 {
			if p.Up {
				return 1
			}
			return 0
		}),
	}, drops: prometheus.NewDesc("wanbond_adaptive_queue_drops_total", "Datagrams dropped by bounded queue admission or residence time.", []string{"peer"}, nil),
		expired:           prometheus.NewDesc("wanbond_adaptive_expired_packets_total", "Packets whose bounded repair lifetime expired.", []string{"peer"}, nil),
		coalescedACKs:     prometheus.NewDesc("wanbond_adaptive_coalesced_tcp_acks_total", "Unsent pure TCP acknowledgements superseded by newer cumulative acknowledgements.", []string{"peer"}, nil),
		interactiveDrops:  prometheus.NewDesc("wanbond_adaptive_interactive_queue_drops_total", "Small datagrams dropped by bounded queue admission or residence time; included in queue_drops_total.", []string{"peer"}, nil),
		admissionDrops:    prometheus.NewDesc("wanbond_adaptive_admission_drops_total", "Datagrams refused because the queued and outstanding datagram limit was full; included in queue_drops_total.", []string{"peer"}, nil),
		aqmDrops:          prometheus.NewDesc("wanbond_adaptive_aqm_drops_total", "Bulk datagrams dropped by the CoDel schedule; included in queue_drops_total. The remainder of queue_drops_total exceeded a residence bound.", []string{"peer"}, nil),
		interactiveQueued: prometheus.NewDesc("wanbond_adaptive_interactive_queued_packets", "Small datagrams waiting for their first transmission.", []string{"peer"}, nil)}
}

func (c *adaptiveCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, m := range c.paths {
		ch <- m.desc
	}
	ch <- c.drops
	ch <- c.expired
	ch <- c.interactiveDrops
	ch <- c.interactiveQueued
	ch <- c.coalescedACKs
	ch <- c.admissionDrops
	ch <- c.aqmDrops
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
		ch <- prometheus.MustNewConstMetric(c.interactiveDrops, prometheus.CounterValue, float64(peer.State.InteractiveQueueDrops), peer.Peer)
		ch <- prometheus.MustNewConstMetric(c.interactiveQueued, prometheus.GaugeValue, float64(peer.State.InteractiveQueued), peer.Peer)
		ch <- prometheus.MustNewConstMetric(c.coalescedACKs, prometheus.CounterValue, float64(peer.State.CoalescedACKs), peer.Peer)
		ch <- prometheus.MustNewConstMetric(c.admissionDrops, prometheus.CounterValue, float64(peer.State.AdmissionDrops), peer.Peer)
		ch <- prometheus.MustNewConstMetric(c.aqmDrops, prometheus.CounterValue, float64(peer.State.AQMDrops), peer.Peer)
	}
}
