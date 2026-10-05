package metrics

import (
	"strconv"

	"github.com/7mind/wanbond/internal/bond"
	"github.com/prometheus/client_golang/prometheus"
)

// AdaptiveSnapshot is one bound peer's transport state. Peer follows the same
// rule as the other per-peer snapshots: "" on a single-bound-peer Source.
type AdaptiveSnapshot struct {
	Peer  string
	State bond.Snapshot
	// LanePaths names the local path each lane of State.Paths leaves through.
	LanePaths map[bond.PathID]string
}

// AdaptiveSource is the part of Source the transport collector reads.
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
	smallQueueDrops                     *prometheus.Desc
	smallQueueResidence                 *prometheus.Desc
	coalescedACKs                       *prometheus.Desc
	admissionDrops, aqmDrops            *prometheus.Desc
	duplicates                          *prometheus.Desc
	rejected                            *prometheus.Desc
	realtimeMoves                       *prometheus.Desc
}

func newAdaptiveCollector(source AdaptiveSource) *adaptiveCollector {
	makeMetric := func(name, help string, kind prometheus.ValueType, value func(bond.PathStats) float64) adaptiveMetric {
		return adaptiveMetric{prometheus.NewDesc("wanbond_adaptive_"+name, help, []string{"peer", "lane"}, nil), kind, value}
	}
	return &adaptiveCollector{source: source, paths: []adaptiveMetric{
		makeMetric("transit_floor_seconds", "Receiver-relative transit floor for the most recently sampled wire-size bucket; not absolute propagation time.", prometheus.GaugeValue, func(p bond.PathStats) float64 { return p.TransitFloor.Seconds() }),
		makeMetric("transit_floor_known", "Most recently sampled wire-size bucket has a transit floor.", prometheus.GaugeValue, func(p bond.PathStats) float64 {
			if p.TransitFloorKnown {
				return 1
			}
			return 0
		}),
		makeMetric("transit_floor_age_seconds", "Age of the evidence that set the most recently sampled transit floor.", prometheus.GaugeValue, func(p bond.PathStats) float64 { return p.TransitFloorAge.Seconds() }),
		makeMetric("path_delay_seconds", "Stage 0 unloaded round trip used as the path-delay input; refreshed only while idle or calibrating.", prometheus.GaugeValue, func(p bond.PathStats) float64 { return p.PathDelay.Seconds() }),
		makeMetric("rank_seconds", "Latency score used to rank lanes for real-time traffic.", prometheus.GaugeValue, func(p bond.PathStats) float64 { return p.Rank.Seconds() }),
		makeMetric("liveness_state", "ACK-progress liveness: 0 dead, 1 live, 2 suspect. Suspect allows interactive traffic and forces alternate real-time copies.", prometheus.GaugeValue, func(p bond.PathStats) float64 {
			switch p.Liveness {
			case bond.LaneLive:
				return 1
			case bond.LaneSuspect:
				return 2
			}
			return 0
		}),
		makeMetric("liveness_age_seconds", "Age of the latest physical ACK progress; zero while that evidence is unknown.", prometheus.GaugeValue, func(p bond.PathStats) float64 { return p.LivenessAge.Seconds() }),
		makeMetric("ack_progress_known", "Lane has received an authenticated physical ACK progress report in this peer epoch.", prometheus.GaugeValue, func(p bond.PathStats) float64 {
			if p.ACKProgressKnown {
				return 1
			}
			return 0
		}),
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
		makeMetric("realtime_original_packets_total", "First submissions of real-time datagrams to this lane; excludes copies, repairs and small TCP datagrams.", prometheus.CounterValue, func(p bond.PathStats) float64 { return float64(p.RealtimeOriginals) }),
		makeMetric("bulk_original_packets_total", "First submissions of bulk datagrams to this lane; excludes copies, repairs and small datagrams.", prometheus.CounterValue, func(p bond.PathStats) float64 { return float64(p.BulkOriginals) }),
		makeMetric("received_bulk_packets_total", "Physical bulk DATA receipts on this lane, including repeated datagrams; excludes frame replays, small datagrams and keepalives.", prometheus.CounterValue, func(p bond.PathStats) float64 { return float64(p.BulkReceived) }),
		makeMetric("acked_bytes_total", "Wire bytes acknowledged by the peer.", prometheus.CounterValue, func(p bond.PathStats) float64 { return float64(p.ACKed) }),
		makeMetric("repair_packets_total", "Additional copies, including small-packet replication.", prometheus.CounterValue, func(p bond.PathStats) float64 { return float64(p.Retransmits) }),
		makeMetric("discovering", "Lane has not yet observed a congestion signal and follows measured delivery.", prometheus.GaugeValue, func(p bond.PathStats) float64 {
			if p.Discovering {
				return 1
			}
			return 0
		}),
		makeMetric("capacity_bytes_per_second", "Demonstrated capacity the target is held below; 0 while the lane has none.", prometheus.GaugeValue, func(p bond.PathStats) float64 { return p.Capacity }),
		makeMetric("congestion_threshold_seconds", "Queue delay above which the lane is taken to queue.", prometheus.GaugeValue, func(p bond.PathStats) float64 { return p.Threshold.Seconds() }),
		makeMetric("delay_signals_total", "Control intervals judged congested by queue delay.", prometheus.CounterValue, func(p bond.PathStats) float64 { return float64(p.Decisions.DelaySignals) }),
		makeMetric("loss_signals_total", "Control intervals judged congested by material loss.", prometheus.CounterValue, func(p bond.PathStats) float64 { return float64(p.Decisions.LossSignals) }),
		makeMetric("stall_signals_total", "Control intervals whose queue delay was put down to a stall of the path, not to congestion.", prometheus.CounterValue, func(p bond.PathStats) float64 { return float64(p.Decisions.StallSignals) }),
		makeMetric("discovery_congestion_ends_total", "Discoveries ended by a congestion signal.", prometheus.CounterValue, func(p bond.PathStats) float64 { return float64(p.Decisions.DiscoveryCongested) }),
		makeMetric("discovery_plateau_ends_total", "Discoveries ended by delivery that stopped growing at the pacing rate.", prometheus.CounterValue, func(p bond.PathStats) float64 { return float64(p.Decisions.DiscoveryPlateau) }),
		makeMetric("capacity_remeasures_total", "Capacity estimates replaced by measured delivery after repeated cuts.", prometheus.CounterValue, func(p bond.PathStats) float64 { return float64(p.Decisions.CapacityRemeasured) }),
		makeMetric("capacity_decays_total", "Reductions of a held capacity estimate by a congestion signal.", prometheus.CounterValue, func(p bond.PathStats) float64 { return float64(p.Decisions.CapacityDecays) }),
		makeMetric("pulses_total", "Probes above the capacity estimate.", prometheus.CounterValue, func(p bond.PathStats) float64 { return float64(p.Decisions.Pulses) }),
		makeMetric("pulse_wins_total", "Probes that drew no congestion signal and raised the estimate.", prometheus.CounterValue, func(p bond.PathStats) float64 { return float64(p.Decisions.PulseWins) }),
		makeMetric("pulse_losses_total", "Probes that found the limit.", prometheus.CounterValue, func(p bond.PathStats) float64 { return float64(p.Decisions.PulseLosses) }),
		makeMetric("rediscoveries_total", "Returns to discovery after consecutive probes without a congestion signal.", prometheus.CounterValue, func(p bond.PathStats) float64 { return float64(p.Decisions.Rediscoveries) }),
		makeMetric("up", "Authenticated lane lease is current and ACK-progress liveness allows interactive traffic; bulk additionally requires live state.", prometheus.GaugeValue, func(p bond.PathStats) float64 {
			if p.Up {
				return 1
			}
			return 0
		}),
	}, drops: prometheus.NewDesc("wanbond_adaptive_queue_drops_total", "Datagrams dropped by bounded queue admission or residence time.", []string{"peer"}, nil),
		expired:             prometheus.NewDesc("wanbond_adaptive_expired_packets_total", "Packets whose bounded repair lifetime expired.", []string{"peer"}, nil),
		coalescedACKs:       prometheus.NewDesc("wanbond_adaptive_coalesced_tcp_acks_total", "Unsent pure TCP acknowledgements superseded by newer cumulative acknowledgements.", []string{"peer"}, nil),
		interactiveDrops:    prometheus.NewDesc("wanbond_adaptive_interactive_queue_drops_total", "Small datagrams dropped by bounded queue admission or residence time; included in queue_drops_total.", []string{"peer"}, nil),
		smallQueueDrops:     prometheus.NewDesc("wanbond_adaptive_small_queue_drops_total", "Small datagrams dropped before first transmission, by size/protocol class and queue cause; sums to interactive_queue_drops_total.", []string{"peer", "class", "cause"}, nil),
		smallQueueResidence: prometheus.NewDesc("wanbond_adaptive_small_queue_residence_seconds", "Local queue residence before the first transmission, by size/protocol class; excludes copies, repairs, coalesced acknowledgements and queued drops.", []string{"peer", "class"}, nil),
		admissionDrops:      prometheus.NewDesc("wanbond_adaptive_admission_drops_total", "Datagrams refused because the queued and outstanding datagram limit was full; included in queue_drops_total.", []string{"peer"}, nil),
		aqmDrops:            prometheus.NewDesc("wanbond_adaptive_aqm_drops_total", "Bulk datagrams dropped by the CoDel schedule; included in queue_drops_total. The remainder of queue_drops_total exceeded a residence bound.", []string{"peer"}, nil),
		interactiveQueued:   prometheus.NewDesc("wanbond_adaptive_interactive_queued_packets", "Small datagrams waiting for their first transmission.", []string{"peer"}, nil),
		rejected:            prometheus.NewDesc("wanbond_adaptive_rejected_frames_total", "Authenticated frames rejected by the transport, by validation cause.", []string{"peer", "cause"}, nil),
		realtimeMoves:       prometheus.NewDesc("wanbond_adaptive_realtime_original_path_moves_total", "Changes of lane between first submissions of real-time datagrams; excludes copies, repairs and small TCP datagrams.", []string{"peer"}, nil),
		duplicates:          prometheus.NewDesc("wanbond_adaptive_duplicate_packets_total", "Received datagrams that had arrived before: the peer's repairs and copies of datagrams it could not confirm in time.", []string{"peer"}, nil)}
}

func (c *adaptiveCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, m := range c.paths {
		ch <- m.desc
	}
	ch <- c.drops
	ch <- c.expired
	ch <- c.interactiveDrops
	ch <- c.smallQueueDrops
	ch <- c.smallQueueResidence
	ch <- c.interactiveQueued
	ch <- c.coalescedACKs
	ch <- c.admissionDrops
	ch <- c.aqmDrops
	ch <- c.duplicates
	ch <- c.rejected
	ch <- c.realtimeMoves
}

func (c *adaptiveCollector) Collect(ch chan<- prometheus.Metric) {
	for _, peer := range c.source.Adaptive() {
		for cause, count := range peer.State.Rejected {
			ch <- prometheus.MustNewConstMetric(c.rejected, prometheus.CounterValue, float64(count), peer.Peer, bond.RejectionCause(cause).String())
		}
		for _, p := range peer.State.Paths {
			for _, m := range c.paths {
				ch <- prometheus.MustNewConstMetric(m.desc, m.kind, m.value(p), peer.Peer, strconv.Itoa(int(p.Path)))
			}
		}
		ch <- prometheus.MustNewConstMetric(c.drops, prometheus.CounterValue, float64(peer.State.QueueDrops), peer.Peer)
		ch <- prometheus.MustNewConstMetric(c.expired, prometheus.CounterValue, float64(peer.State.Expired), peer.Peer)
		ch <- prometheus.MustNewConstMetric(c.interactiveDrops, prometheus.CounterValue, float64(peer.State.InteractiveQueueDrops), peer.Peer)
		for _, queue := range []struct {
			class string
			drops bond.SmallQueueDropStats
		}{{"realtime", peer.State.RealtimeQueueDrops}, {"small_tcp", peer.State.SmallTCPQueueDrops}} {
			for _, drop := range []struct {
				cause string
				count uint64
			}{{"admission", queue.drops.Admission}, {"deadline", queue.drops.Deadline}, {"stale", queue.drops.Stale}} {
				ch <- prometheus.MustNewConstMetric(c.smallQueueDrops, prometheus.CounterValue, float64(drop.count), peer.Peer, queue.class, drop.cause)
			}
		}
		for _, queue := range []struct {
			class     string
			residence bond.SmallQueueResidenceStats
		}{{"realtime", peer.State.RealtimeQueueResidence}, {"small_tcp", peer.State.SmallTCPQueueResidence}} {
			buckets := make(map[float64]uint64)
			for i, bound := range bond.SmallQueueResidenceBounds() {
				buckets[bound.Seconds()] = queue.residence.Buckets[i]
			}
			ch <- prometheus.MustNewConstHistogram(c.smallQueueResidence, queue.residence.Count, queue.residence.TotalSeconds, buckets, peer.Peer, queue.class)
		}
		ch <- prometheus.MustNewConstMetric(c.interactiveQueued, prometheus.GaugeValue, float64(peer.State.InteractiveQueued), peer.Peer)
		ch <- prometheus.MustNewConstMetric(c.coalescedACKs, prometheus.CounterValue, float64(peer.State.CoalescedACKs), peer.Peer)
		ch <- prometheus.MustNewConstMetric(c.admissionDrops, prometheus.CounterValue, float64(peer.State.AdmissionDrops), peer.Peer)
		ch <- prometheus.MustNewConstMetric(c.aqmDrops, prometheus.CounterValue, float64(peer.State.AQMDrops), peer.Peer)
		ch <- prometheus.MustNewConstMetric(c.duplicates, prometheus.CounterValue, float64(peer.State.Duplicates), peer.Peer)
		ch <- prometheus.MustNewConstMetric(c.realtimeMoves, prometheus.CounterValue, float64(peer.State.RealtimeMoves), peer.Peer)
	}
}
