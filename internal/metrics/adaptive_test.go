package metrics

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/7mind/wanbond/internal/bond"
	"github.com/7mind/wanbond/internal/frame"
)

type fixedAdaptive []AdaptiveSnapshot

func TestRealtimeOriginalsExcludeCopiesAndTCPACKs(t *testing.T) {
	start := time.Unix(100, 0)
	p := bond.New(bond.Epoch{Boot: 1, Generation: 1})
	p.SetRemote(bond.Epoch{Boot: 2, Generation: 1}, true)
	for lane := range 2 {
		if err := p.Path(bond.PathID(lane), bond.PathID(lane), time.Duration(40+40*lane)*time.Millisecond, start); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.Enqueue(make([]byte, 224), bond.PacketMetadata{Flow: bond.FlowID{4, 17}}, start); err != nil {
		t.Fatal(err)
	}
	var repairs uint64
	bulkTransmissions := 0
	tcpSent := false
	for tick := 0; tick < 400; tick++ {
		now := start.Add(time.Duration(tick) * time.Millisecond)
		if tick == 70 {
			if err := p.Enqueue(make([]byte, 96), bond.PacketMetadata{Flow: bond.FlowID{4, 6}}, now); err != nil {
				t.Fatal(err)
			}
		}
		if tick == 90 {
			if err := p.Enqueue(make([]byte, 1280), bond.PacketMetadata{Flow: bond.FlowID{4, 6}}, now); err != nil {
				t.Fatal(err)
			}
		}
		if tick == 100 {
			p.Disable(0)
			if err := p.Enqueue(make([]byte, 224), bond.PacketMetadata{Flow: bond.FlowID{4, 17}}, now); err != nil {
				t.Fatal(err)
			}
		}
		tx, err := p.Poll(now)
		if err != nil {
			t.Fatal(err)
		}
		for _, sent := range tx {
			if sent.Frame.ControlType == bond.DataType && len(sent.Frame.Payload) == bond.Overhead-frame.ControlOverhead+96 {
				tcpSent = true
			}
			if sent.Frame.ControlType == bond.DataType && len(sent.Frame.Payload) == bond.Overhead-frame.ControlOverhead+1280 {
				bulkTransmissions++
			}
		}
	}
	state := p.Snapshot(start.Add(400 * time.Millisecond))
	if bulkTransmissions < 2 {
		t.Fatalf("only %d bulk transmissions; no bulk copy or repair exercised the exclusion", bulkTransmissions)
	}
	for _, lane := range state.Paths {
		repairs += lane.Retransmits
	}
	if repairs == 0 {
		t.Fatal("no copy or repair exercised the exclusion")
	}
	if !tcpSent {
		t.Fatal("no small TCP datagram exercised the exclusion")
	}
	registry := prometheus.NewRegistry()
	if err := registry.Register(newAdaptiveCollector(fixedAdaptive{{State: state}})); err != nil {
		t.Fatal(err)
	}
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	originals, moves, bulk := false, false, false
	for _, family := range families {
		switch family.GetName() {
		case "wanbond_adaptive_realtime_original_packets_total":
			originals = true
			if len(family.Metric) != 2 {
				t.Fatalf("original counter has %d lanes, want 2", len(family.Metric))
			}
			for _, metric := range family.Metric {
				if value := metric.GetCounter().GetValue(); value != 1 {
					t.Fatalf("lane original counter = %v, want 1", value)
				}
			}
		case "wanbond_adaptive_realtime_original_path_moves_total":
			moves = true
			if len(family.Metric) != 1 || family.Metric[0].GetCounter().GetValue() != 1 {
				t.Fatalf("primary route counter = %v, want one move", family.Metric)
			}
		case "wanbond_adaptive_bulk_original_packets_total":
			bulk = true
			total := 0.0
			for _, metric := range family.Metric {
				total += metric.GetCounter().GetValue()
			}
			if total != 1 {
				t.Fatalf("bulk originals = %v, want 1 excluding small TCP and copies", total)
			}
		}
	}
	if !originals || !moves || !bulk {
		t.Fatalf("primary counters missing: originals=%t moves=%t bulk=%t", originals, moves, bulk)
	}
}

func TestRejectedTransportFrameIsCounted(t *testing.T) {
	p := bond.New(bond.Epoch{Boot: 1, Generation: 1})
	if _, err := p.Receive(0, frame.Control{}, time.Unix(100, 0)); err == nil {
		t.Fatal("malformed frame accepted")
	}
	registry := prometheus.NewRegistry()
	if err := registry.Register(newAdaptiveCollector(fixedAdaptive{{State: p.Snapshot(time.Unix(100, 0))}})); err != nil {
		t.Fatal(err)
	}
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() != "wanbond_adaptive_rejected_frames_total" {
			continue
		}
		for _, metric := range family.Metric {
			for _, label := range metric.Label {
				if label.GetName() == "cause" && label.GetValue() == "malformed" {
					if metric.GetCounter().GetValue() != 1 {
						t.Fatalf("malformed rejection counter = %v", metric.GetCounter().GetValue())
					}
					return
				}
			}
		}
	}
	t.Fatal("transport rejection has no exported cause counter")
}

func (f fixedAdaptive) Adaptive() []AdaptiveSnapshot { return f }

// The lane's control decisions, capacity estimate and threshold are exported,
// so a target held low can be traced to its cause from the endpoint.
func TestAdaptiveCollectorExportsControlDecisions(t *testing.T) {
	source := fixedAdaptive{{Peer: "hub", State: bond.Snapshot{
		Duplicates: 9,
		Paths: []bond.PathStats{{
			Path: 256, Capacity: 130000, Threshold: 30 * time.Millisecond,
			Decisions: bond.Decisions{DelaySignals: 1, LossSignals: 2, DiscoveryCongested: 3, DiscoveryPlateau: 4,
				CapacityRemeasured: 5, CapacityDecays: 6, Pulses: 7, PulseWins: 8, PulseLosses: 10, Rediscoveries: 11, StallSignals: 12},
		}},
	}}}
	registry := prometheus.NewRegistry()
	if err := registry.Register(newAdaptiveCollector(source)); err != nil {
		t.Fatal(err)
	}
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]*dto.Metric{}
	for _, family := range families {
		if family.GetName() == "wanbond_adaptive_rejected_frames_total" {
			if len(family.Metric) != int(bond.RejectionCauses) {
				t.Fatalf("rejection causes: %d", len(family.Metric))
			}
			continue
		}
		if len(family.Metric) != 1 {
			t.Fatalf("%s has %d samples, want 1", family.GetName(), len(family.Metric))
		}
		got[family.GetName()] = family.Metric[0]
	}
	for name, want := range map[string]float64{
		"wanbond_adaptive_capacity_bytes_per_second":       130000,
		"wanbond_adaptive_congestion_threshold_seconds":    0.03,
		"wanbond_adaptive_delay_signals_total":             1,
		"wanbond_adaptive_loss_signals_total":              2,
		"wanbond_adaptive_stall_signals_total":             12,
		"wanbond_adaptive_discovery_congestion_ends_total": 3,
		"wanbond_adaptive_discovery_plateau_ends_total":    4,
		"wanbond_adaptive_capacity_remeasures_total":       5,
		"wanbond_adaptive_capacity_decays_total":           6,
		"wanbond_adaptive_pulses_total":                    7,
		"wanbond_adaptive_pulse_wins_total":                8,
		"wanbond_adaptive_pulse_losses_total":              10,
		"wanbond_adaptive_rediscoveries_total":             11,
		"wanbond_adaptive_duplicate_packets_total":         9,
	} {
		metric, ok := got[name]
		if !ok {
			t.Errorf("%s is not exported", name)
			continue
		}
		if value := metricValue(metric); value != want {
			t.Errorf("%s = %v, want %v", name, value, want)
		}
		labels := map[string]string{}
		for _, label := range metric.Label {
			labels[label.GetName()] = label.GetValue()
		}
		if labels["peer"] != "hub" || name != "wanbond_adaptive_duplicate_packets_total" && labels["lane"] != "256" {
			t.Errorf("%s labels = %v", name, labels)
		}
	}
}
