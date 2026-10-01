package metrics

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/7mind/wanbond/internal/bond"
)

type fixedAdaptive []AdaptiveSnapshot

func (f fixedAdaptive) Adaptive() []AdaptiveSnapshot { return f }

// The lane's control decisions, capacity estimate and threshold are exported,
// so a target held low can be traced to its cause from the endpoint.
func TestAdaptiveCollectorExportsControlDecisions(t *testing.T) {
	source := fixedAdaptive{{Peer: "hub", State: bond.Snapshot{
		Duplicates: 9,
		Paths: []bond.PathStats{{
			Path: 256, Capacity: 130000, Threshold: 30 * time.Millisecond,
			Decisions: bond.Decisions{DelaySignals: 1, LossSignals: 2, DiscoveryCongested: 3, DiscoveryPlateau: 4,
				CapacityRemeasured: 5, CapacityDecays: 6, Pulses: 7, PulseWins: 8, PulseLosses: 10, Rediscoveries: 11},
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
