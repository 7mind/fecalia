package bond_test

import (
	"slices"
	"testing"
	"time"
)

// Performance-Blackbox-Group: lane choice survives a transient low sample,
// while a persistent improvement carries subsequent voice originals.
func TestUnloadedDelayChangesChooseTheBetterVoiceLane(t *testing.T) {
	for _, persistent := range []bool{false, true} {
		name, wanted := "one-low-sample", 0
		if persistent {
			name, wanted = "persistent-improvement", 1
		}
		t.Run(name, func(t *testing.T) {
			lanes := []modelLane{{rate: 1e6, delay: 20 * time.Millisecond}, {rate: 1e6, delay: 40 * time.Millisecond}}
			lanes[1].condition = func(side int, at time.Duration) modelCondition {
				delay := 40 * time.Millisecond
				if at >= 5*time.Second && (persistent || at < 5050*time.Millisecond) {
					delay = 10 * time.Millisecond
				}
				return modelCondition{rate: 1e6, delay: delay, buffer: 100 * time.Millisecond}
			}
			o := (policyRun{lanes: lanes, seconds: 14, trafficAt: 10, voice: true}).run(t)
			for side, routes := range o.voiceRoutes {
				chosen, total := 0, 0
				for _, route := range routes {
					if route.at >= 11000 && route.at < 13000 {
						total++
						if route.lane == wanted {
							chosen++
						}
					}
				}
				if total < 90 || chosen*100 < total*95 {
					t.Errorf("direction %d preferred lane %d carried %d/%d voice originals", side, wanted, chosen, total)
				}
			}
		})
	}
}

// Performance-Blackbox-Group: delay/window policy keeps voice responsive and
// bulk productive under independent forward and reverse delay variation.
func TestDelayNoisePreservesVoiceAndBulkService(t *testing.T) {
	for _, direction := range []string{"forward", "reverse"} {
		t.Run(direction, func(t *testing.T) {
			checkDelayNoiseService(t, direction)
		})
	}
}

func checkDelayNoiseService(t *testing.T, direction string) {
	t.Helper()
	lanes := []modelLane{{rate: 1.25e6, delay: 20 * time.Millisecond}}
	lanes[0].condition = func(side int, at time.Duration) modelCondition {
		delay := 20 * time.Millisecond
		if direction == "both" || direction == "forward" && side == 0 || direction == "reverse" && side == 1 {
			delay += time.Duration(int(at/(100*time.Millisecond))%2*2-1) * 10 * time.Millisecond
		}
		return modelCondition{rate: 1.25e6, delay: delay, buffer: 400 * time.Millisecond}
	}
	m := policyRun{lanes: lanes, seconds: 30, trafficAt: 5, voice: true, bulk: true}
	o := m.run(t)
	for side, stream := range o.voice {
		var rtts []time.Duration
		lost, sent := 0, 0
		for _, v := range stream {
			if v.sent >= 10000 && v.sent < 28000 {
				sent++
				if v.arrived < 0 {
					lost++
				} else {
					rtts = append(rtts, v.rtt)
				}
			}
		}
		slices.Sort(rtts)
		if sent < 850 || lost*100 >= sent || len(rtts) == 0 {
			t.Fatalf("direction %d voice lost %d/%d", side, lost, sent)
		}
		if p99 := rtts[len(rtts)*99/100]; p99 >= 150*time.Millisecond {
			t.Errorf("direction %d voice RTT p99 %s >= 150ms", side, p99)
		}
		var bulk float64
		for _, bytes := range o.bulk[side][20:] {
			bulk += bytes
		}
		bulk /= 10
		if reference := m.reference(25 * time.Second)[side]; bulk < .75*reference {
			t.Errorf("direction %d bulk %.0f B/s < 75%% of independent reference %.0f B/s", side, bulk, reference)
		}
		t.Logf("direction %d voice lost %d/%d p99 %s; bulk %.0f B/s", side, lost, sent, rtts[len(rtts)*99/100], bulk)
	}
}
