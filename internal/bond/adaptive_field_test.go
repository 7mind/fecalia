//go:build adaptivepolicy

package bond_test

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"
)

func policyFieldLinks(t *testing.T) []modelLane {
	t.Helper()
	type condition struct {
		Rate   float64 `json:"rate"`
		Delay  int     `json:"delay"`
		Jitter int     `json:"jitter"`
		Loss   float64 `json:"loss"`
		Police bool    `json:"police"`
		Buffer int     `json:"buffer_ms"`
	}
	type direction struct {
		Satellite condition `json:"1"`
		Mobile    condition `json:"2"`
	}
	var profile struct {
		Hub  direction `json:"hub"`
		Edge direction `json:"edge"`
	}
	data, err := os.ReadFile("../../test/vm/profiles/field-standby.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &profile); err != nil {
		t.Fatal(err)
	}
	conditions := [2][2]condition{{profile.Hub.Satellite, profile.Hub.Mobile}, {profile.Edge.Satellite, profile.Edge.Mobile}}
	lanes := make([]modelLane, 2)
	for lane := range lanes {
		for side := range conditions {
			if c := conditions[side][lane]; c.Rate <= 0 || c.Delay <= 0 || c.Jitter != 0 || c.Loss != 0 {
				t.Fatal("field fixture requires positive rate/delay and fixed loss-free timing")
			}
		}
		lanes[lane] = modelLane{rate: conditions[0][lane].Rate * 125000, delay: time.Duration(conditions[0][lane].Delay) * time.Millisecond,
			condition: func(side int, at time.Duration) modelCondition {
				c := conditions[side][lane]
				return modelCondition{rate: c.Rate * 125000, delay: time.Duration(c.Delay) * time.Millisecond,
					policed: c.Police, buffer: time.Duration(c.Buffer) * time.Millisecond}
			}}
	}
	return lanes
}

// Performance-Blackbox-Group: record the independent voice-only reference.
func TestAdaptiveFieldStandbyIdleReference(t *testing.T) {
	for topology, name := range []string{"satellite", "mobile", "pair"} {
		t.Run(name, func(t *testing.T) {
			lanes := policyFieldLinks(t)
			if topology != policyPairIdle {
				lanes = policyChange(lanes, 1-topology, func(side int, at time.Duration, c modelCondition) modelCondition {
					c.dark = true
					return c
				})
			}
			o := (policyRun{lanes: lanes, seconds: 22, trafficAt: 2, voice: true}).run(t)
			for side, stream := range o.voice {
				var rtts []time.Duration
				for _, v := range stream {
					if v.sent >= 5000 && v.sent < 19000 && v.arrived >= 0 {
						rtts = append(rtts, v.rtt)
					}
				}
				if len(rtts) < 600 {
					t.Fatalf("direction %d has only %d/700 reference replies", side, len(rtts))
				}
				t.Logf("direction %d idle replies %d/700 RTT p50 %s p99 %s", side, len(rtts), policyQuantile(slices.Clone(rtts), 50), policyQuantile(rtts, 99))
			}
		})
	}
}

// Progression-Blackbox-Group: section 4 outage/recovery gates on current caps.
func TestAdaptiveFieldStandbyOutage(t *testing.T) {
	for failed := 0; failed < 2; failed++ {
		for _, direction := range []int{-1, 0, 1} {
			for _, bulk := range []bool{false, true} {
				t.Run(fmt.Sprintf("lane%d/direction%d/bulk%t", failed, direction, bulk), func(t *testing.T) {
					lanes := policyChange(policyFieldLinks(t), failed, func(side int, at time.Duration, c modelCondition) modelCondition {
						c.dark = (direction == -1 || side == direction) && at >= 20*time.Second && at < 35*time.Second
						return c
					})
					m := policyRun{lanes: lanes, seconds: 45, trafficAt: 2, bulk: bulk, voice: true}
					o := m.run(t)
					checkPolicyVoice(t, o, 2, 45, 0, false)
					// Independent f75668e voice-only p99 in [5,19)s, three runs.
					limit := []time.Duration{41 * time.Millisecond, 59 * time.Millisecond}[1-failed] + 50*time.Millisecond
					checkPolicyVoiceLatency(t, o, 21, 35, [2]time.Duration{limit, limit})
					checkPolicyVoiceLatency(t, o, 35, 45, [2]time.Duration{91 * time.Millisecond, 91 * time.Millisecond})
					if bulk {
						checkPolicyProgress(t, o, 21, 35)
						checkPolicyBulkDeadline(t, m, o, 23, .75)
						checkPolicyBulk(t, m, o, 23, 35, .75)
						for side := range o.laneBulk {
							if o.laneBulk[side][failed][35]+o.laneBulk[side][failed][36] == 0 {
								t.Errorf("direction %d recovered lane has no bulk within 2s", side)
							}
						}
						checkPolicyBulkDeadline(t, m, o, 40, .75)
						checkPolicyBulk(t, m, o, 40, 45, .75)
					}
				})
			}
		}
	}
}

// Performance-Blackbox-Group: separate directional service from duplex contention.
func TestAdaptiveFieldStandbyDirectionalService(t *testing.T) {
	for _, direction := range []struct {
		name string
		mode policyBulkDirection
		side int
	}{{"downlink", policyBulkDownlink, 0}, {"uplink", policyBulkUplink, 1}} {
		for _, voice := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/voice%t", direction.name, voice), func(t *testing.T) {
				m := policyRun{lanes: policyFieldLinks(t), seconds: 50, trafficAt: 5,
					bulk: true, voice: voice, bulkDirection: direction.mode}
				o := m.run(t)
				for _, delivered := range o.bulk[1-direction.side] {
					if delivered != 0 {
						t.Fatal("the inactive bulk direction delivered payload")
					}
				}
				var delivered float64
				for second := 45; second < 50; second++ {
					delivered += o.bulk[direction.side][second]
				}
				delivered /= 5
				reference := m.reference(45 * time.Second)
				if reference[1-direction.side] != 0 || reference[direction.side] <= 0 {
					t.Fatalf("directional reference is invalid: %v", reference)
				}
				t.Logf("direction %s in [45,50): %.0f B/s, independent reference %.0f B/s",
					direction.name, delivered, reference[direction.side])
				if delivered < .75*reference[direction.side] {
					t.Errorf("bulk %.0f B/s < 75%% of independent reference %.0f B/s", delivered, reference[direction.side])
				}
				if voice {
					checkPolicyVoice(t, o, 5, 50, 150*time.Millisecond, false)
				}
			})
		}
	}
}
