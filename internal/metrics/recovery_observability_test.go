package metrics

import (
	"context"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/reseq"
	"github.com/7mind/wanbond/internal/shaper"
)

func TestRecoveryObservabilityReconcilesSourceAndScrape(t *testing.T) {
	for _, contract := range []struct {
		source any
		fields []string
	}{
		{FECSnapshot{}, []string{
			"StagedGroups",
			"StagedDataFrames",
			"GroupDecisions",
			"DeadlineDecisions",
			"DeadlineMisses",
			"DeadlineMaxOvershoot",
			"OpenGroupDeadline",
			"Recovery",
		}},
		{reseq.Stats{}, []string{
			"RecoveryArmed",
			"ArmedDeadline",
			"ArmedWindow",
			"DeadlineWakeups",
			"GapFills",
			"FastWindowArms",
			"FallbackWindowArms",
		}},
		{shaper.Snapshot{}, []string{
			"OuterPriorityEmittedBytes",
			"OuterPriorityErrorBytes",
			"RecoveryCutActive",
			"RecoveryCutDeadline",
			"RecoveryCutDatagrams",
			"RecoveryCutSocketCalls",
			"FECGroupOwnedHighWaterBytes",
			"MemoryRetainedHighWaterBytes",
		}},
	} {
		typ := reflect.TypeOf(contract.source)
		for _, field := range contract.fields {
			if _, ok := typ.FieldByName(field); !ok {
				t.Fatalf("%s has no production source field %s", typ, field)
			}
		}
	}
	server := startServer(t, fakeSource{
		paths: []PathSnapshot{{Name: "wan0", Shaper: &shaper.Snapshot{
			OuterPriorityEmittedBytes:    101,
			OuterPriorityErrorBytes:      102,
			RecoveryCutActive:            true,
			RecoveryCutDatagrams:         103,
			RecoveryCutSocketCalls:       104,
			FECGroupOwnedHighWaterBytes:  105,
			MemoryRetainedHighWaterBytes: 106,
		}}},
		fec: []FECSnapshot{{
			StagedGroups:         2,
			StagedDataFrames:     3,
			GroupDecisions:       4,
			DeadlineDecisions:    5,
			DeadlineMisses:       6,
			DeadlineMaxOvershoot: 7 * time.Millisecond,
			Recovery: RecoveryStats{
				Sender: RecoveryDirectionStats{
					OfferPresent:     true,
					WriterExclusive:  true,
					OfferWrites:      8,
					ACKAccepts:       11,
					Rotations:        12,
					StaleRejections:  31,
					WrongRejections:  32,
					ReplayRejections: 33,
					FallbackReason:   "unacked",
					ServiceBound:     14 * time.Millisecond,
				},
				Receiver: RecoveryDirectionStats{
					OfferPresent:     true,
					FastEligible:     true,
					WriterExclusive:  true,
					ACKWrites:        9,
					OfferAccepts:     10,
					SessionRestarts:  13,
					StaleRejections:  41,
					WrongRejections:  42,
					ReplayRejections: 43,
					ServiceBound:     24 * time.Millisecond,
					RTTAge:           15 * time.Millisecond,
					Headroom:         16 * time.Millisecond,
					Window:           17 * time.Millisecond,
				},
			},
		}},
		reseq: []ReseqSnapshot{{Stats: reseq.Stats{
			RecoveryArmed:      true,
			ArmedWindow:        18 * time.Millisecond,
			DeadlineWakeups:    19,
			GapFills:           20,
			FastWindowArms:     21,
			FallbackWindowArms: 22,
		}}},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	exp, err := Fetch(ctx, http.DefaultClient, server.URL())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"wanbond_fec_staged_groups",
		"wanbond_fec_staged_data_frames",
		"wanbond_fec_group_decisions_total",
		"wanbond_fec_deadline_decisions_total",
		"wanbond_fec_deadline_misses_total",
		"wanbond_fec_deadline_max_overshoot_seconds",
		"wanbond_fec_open_group_deadline_timestamp_seconds",
		"wanbond_recovery_contract_offer_present",
		"wanbond_recovery_contract_fast_eligible",
		"wanbond_recovery_contract_transition_frozen",
		"wanbond_recovery_contract_writer_exclusive",
		"wanbond_recovery_contract_fresh_until_timestamp_seconds",
		"wanbond_recovery_contract_offer_writes_total",
		"wanbond_recovery_contract_ack_writes_total",
		"wanbond_recovery_contract_offer_accepts_total",
		"wanbond_recovery_contract_ack_accepts_total",
		"wanbond_recovery_contract_rotations_total",
		"wanbond_recovery_contract_session_restarts_total",
		"wanbond_recovery_contract_rejections_total",
		"wanbond_recovery_contract_fallback",
		"wanbond_recovery_contract_service_bound_seconds",
		"wanbond_recovery_rtt_age_seconds",
		"wanbond_recovery_headroom_seconds",
		"wanbond_recovery_window_seconds",
		"wanbond_path_shaper_outer_priority_emitted_bytes_total",
		"wanbond_path_shaper_outer_priority_error_bytes_total",
		"wanbond_path_shaper_recovery_cut_active",
		"wanbond_path_shaper_recovery_cut_deadline_timestamp_seconds",
		"wanbond_path_shaper_recovery_cut_datagrams",
		"wanbond_path_shaper_recovery_cut_socket_calls_total",
		"wanbond_path_shaper_fec_group_owned_high_water_bytes",
		"wanbond_path_shaper_memory_retained_high_water_bytes",
		"wanbond_resequencer_recovery_armed",
		"wanbond_resequencer_armed_deadline_timestamp_seconds",
		"wanbond_resequencer_armed_window_seconds",
		"wanbond_resequencer_deadline_wakeups_total",
		"wanbond_resequencer_gap_fills_total",
		"wanbond_resequencer_fast_window_arms_total",
		"wanbond_resequencer_fallback_window_arms_total",
	} {
		if !exp.Has(name) {
			t.Errorf("production scrape omitted %s", name)
		}
	}
	for _, contract := range []struct {
		name      string
		direction string
		reason    string
		want      float64
	}{
		{"wanbond_recovery_contract_rejections_total", "sender", "wrong", 32},
		{"wanbond_recovery_contract_rejections_total", "receiver", "wrong", 42},
		{"wanbond_recovery_contract_fallback", "sender", "unacked", 1},
		{"wanbond_recovery_contract_fallback", "receiver", "unacked", 0},
	} {
		if got, ok := recoveryReasonMetricValue(exp, contract.name, contract.direction, contract.reason); !ok || got != contract.want {
			t.Errorf("%s{%q,%q} = %v, %v, want %v, true",
				contract.name, contract.direction, contract.reason, got, ok, contract.want)
		}
	}
	for name, want := range map[string]float64{
		"wanbond_fec_staged_groups":                      2,
		"wanbond_fec_staged_data_frames":                 3,
		"wanbond_fec_group_decisions_total":              4,
		"wanbond_fec_deadline_decisions_total":           5,
		"wanbond_fec_deadline_misses_total":              6,
		"wanbond_fec_deadline_max_overshoot_seconds":     0.007,
		"wanbond_resequencer_recovery_armed":             1,
		"wanbond_resequencer_armed_window_seconds":       0.018,
		"wanbond_resequencer_deadline_wakeups_total":     19,
		"wanbond_resequencer_gap_fills_total":            20,
		"wanbond_resequencer_fast_window_arms_total":     21,
		"wanbond_resequencer_fallback_window_arms_total": 22,
	} {
		if got, ok := exp.Value(name); !ok || got != want {
			t.Errorf("%s = %v, %v, want %v, true", name, got, ok, want)
		}
	}
	for _, contract := range []struct {
		name      string
		direction string
		want      float64
	}{
		{"wanbond_recovery_contract_offer_present", "sender", 1},
		{"wanbond_recovery_contract_fast_eligible", "sender", 0},
		{"wanbond_recovery_contract_offer_present", "receiver", 1},
		{"wanbond_recovery_contract_fast_eligible", "receiver", 1},
		{"wanbond_recovery_contract_writer_exclusive", "sender", 1},
		{"wanbond_recovery_contract_offer_writes_total", "sender", 8},
		{"wanbond_recovery_contract_ack_writes_total", "receiver", 9},
		{"wanbond_recovery_contract_offer_accepts_total", "receiver", 10},
		{"wanbond_recovery_contract_ack_accepts_total", "sender", 11},
		{"wanbond_recovery_contract_rotations_total", "sender", 12},
		{"wanbond_recovery_contract_session_restarts_total", "receiver", 13},
		{"wanbond_recovery_contract_service_bound_seconds", "sender", 0.014},
		{"wanbond_recovery_contract_service_bound_seconds", "receiver", 0.024},
		{"wanbond_recovery_rtt_age_seconds", "receiver", 0.015},
		{"wanbond_recovery_headroom_seconds", "receiver", 0.016},
		{"wanbond_recovery_window_seconds", "receiver", 0.017},
	} {
		if got, ok := recoveryDirectionMetricValue(exp, contract.name, contract.direction); !ok || got != contract.want {
			t.Errorf("%s{%q} = %v, %v, want %v, true", contract.name, contract.direction, got, ok, contract.want)
		}
	}
	for name, want := range map[string]float64{
		"wanbond_path_shaper_outer_priority_emitted_bytes_total": 101,
		"wanbond_path_shaper_outer_priority_error_bytes_total":   102,
		"wanbond_path_shaper_recovery_cut_active":                1,
		"wanbond_path_shaper_recovery_cut_datagrams":             103,
		"wanbond_path_shaper_recovery_cut_socket_calls_total":    104,
		"wanbond_path_shaper_fec_group_owned_high_water_bytes":   105,
		"wanbond_path_shaper_memory_retained_high_water_bytes":   106,
	} {
		if got, ok := exp.PathValue(name, "wan0"); !ok || got != want {
			t.Errorf("%s = %v, %v, want %v, true", name, got, ok, want)
		}
	}
}

func recoveryDirectionMetricValue(exp Exposition, name, direction string) (float64, bool) {
	family, ok := exp.Families()[name]
	if !ok {
		return 0, false
	}
	for _, metric := range family.GetMetric() {
		for _, label := range metric.GetLabel() {
			if label.GetName() == "direction" && label.GetValue() == direction {
				return metricValue(metric), true
			}
		}
	}
	return 0, false
}

func recoveryReasonMetricValue(exp Exposition, name, direction, reason string) (float64, bool) {
	family, ok := exp.Families()[name]
	if !ok {
		return 0, false
	}
	for _, metric := range family.GetMetric() {
		var gotDirection, gotReason string
		for _, label := range metric.GetLabel() {
			switch label.GetName() {
			case "direction":
				gotDirection = label.GetValue()
			case "reason":
				gotReason = label.GetValue()
			}
		}
		if gotDirection == direction && gotReason == reason {
			return metricValue(metric), true
		}
	}
	return 0, false
}
