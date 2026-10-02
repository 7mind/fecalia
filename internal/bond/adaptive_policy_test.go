//go:build adaptivepolicy

package bond_test

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"
	"time"
)

// Progression-BG, specified by adaptive-policy plan section 4. Stage 0 runs
// these gates explicitly; remove the progression build tag after stages 1–3.
func policyProfiles() map[string][]modelLane {
	return map[string][]modelLane{
		"radio":     policyLinks([2][2]float64{{62500, 12.5e6}, {50000, 156250}}),
		"gigaradio": policyLinks([2][2]float64{{37.5e6, 37.5e6}, {37.5e6, 37.5e6}}),
	}
}

func policyLinks(rates [2][2]float64) []modelLane {
	lanes := make([]modelLane, 2)
	for lane := range lanes {
		lanes[lane] = modelLane{rate: rates[0][lane], delay: time.Duration(20+20*lane) * time.Millisecond,
			condition: func(side int, at time.Duration) modelCondition {
				base, jitter := time.Duration(20+20*lane)*time.Millisecond, time.Duration(10+20*lane)*time.Millisecond
				draw := rand.New(rand.NewPCG(uint64(at/(100*time.Millisecond)), uint64(1+side*2+lane)))
				return modelCondition{rate: rates[side][lane], delay: base + time.Duration(draw.Int64N(int64(2*jitter))) - jitter,
					loss: []float64{0.004, 0}[lane], buffer: 100 * time.Millisecond}
			}}
	}
	return lanes
}

func policyChange(lanes []modelLane, lane int, change func(side int, at time.Duration, c modelCondition) modelCondition) []modelLane {
	result := slices.Clone(lanes)
	old := result[lane]
	result[lane].condition = func(side int, at time.Duration) modelCondition { return change(side, at, old.at(side, at)) }
	return result
}

func policyQuantile(values []time.Duration, numerator int) time.Duration {
	if len(values) == 0 {
		return 0
	}
	slices.Sort(values)
	return values[min(len(values)-1, len(values)*numerator/100)]
}

func checkPolicyVoice(t *testing.T, o policyOutcome, from, until int, latency time.Duration, strictLoss bool) {
	t.Helper()
	for side, stream := range o.voice {
		var rtts []time.Duration
		var arrivals []int
		lost, sent, consecutive, worst := 0, 0, 0, 0
		for _, v := range stream {
			if v.sent < from*1000 || v.sent >= until*1000 {
				continue
			}
			sent++
			if v.arrived < 0 {
				lost++
				consecutive++
				worst = max(worst, consecutive)
				continue
			}
			consecutive = 0
			rtts = append(rtts, v.rtt)
			arrivals = append(arrivals, v.arrived)
		}
		slices.Sort(arrivals)
		gap := 0
		for i := 1; i < len(arrivals); i++ {
			gap = max(gap, arrivals[i]-arrivals[i-1])
		}
		p99 := policyQuantile(slices.Clone(rtts), 99)
		t.Logf("voice direction %d in [%d,%d): lost %d/%d, consecutive %d, gap %dms, RTT p50 %s p99 %s", side, from, until, lost, sent, worst, gap, policyQuantile(rtts, 50), p99)
		if sent == 0 || len(arrivals) < 2 {
			t.Errorf("direction %d has no non-vacuous voice measurement", side)
			continue
		}
		if gap >= 150 {
			t.Errorf("direction %d voice gap %dms >= 150ms", side, gap)
		}
		if worst > 3 {
			t.Errorf("direction %d lost %d consecutive voice datagrams", side, worst)
		}
		if strictLoss && lost != 0 || !strictLoss && lost*100 >= sent {
			t.Errorf("direction %d voice loss %d/%d violates gate", side, lost, sent)
		}
		if latency > 0 && p99 > latency {
			t.Errorf("direction %d voice RTT p99 %s > %s", side, p99, latency)
		}
	}
}

func checkPolicyBulk(t *testing.T, m policyRun, o policyOutcome, from, until int, ratio float64) {
	t.Helper()
	for side := range o.bulk {
		var got, reference float64
		for second := from; second < until; second++ {
			got += o.bulk[side][second]
			for sample := 0; sample < 10; sample++ {
				reference += m.reference(time.Duration(second)*time.Second + time.Duration(sample)*100*time.Millisecond)[side] / 10
			}
		}
		got /= float64(until - from)
		reference /= float64(until - from)
		t.Logf("bulk direction %d in [%d,%d): %.0f B/s, reference %.0f B/s", side, from, until, got, reference)
		if reference <= 0 {
			t.Fatalf("direction %d reference is non-positive", side)
		}
		if got < ratio*reference {
			t.Errorf("direction %d bulk %.0f B/s < %.0f%% of available goodput %.0f B/s", side, got, ratio*100, reference)
		}
	}
}

func checkPolicyBulkDeadline(t *testing.T, m policyRun, o policyOutcome, deadline int, ratio float64) {
	t.Helper()
	checkPolicyBulk(t, m, o, deadline-1, deadline, ratio)
}

func checkPolicyProgress(t *testing.T, o policyOutcome, from, until int) {
	t.Helper()
	for side := range o.bulk {
		for second := from; second < until; second++ {
			if o.bulk[side][second] == 0 {
				t.Errorf("direction %d has no TCP delivery in outage second %d", side, second)
			}
		}
	}
}

func checkPolicyRoute(t *testing.T, o policyOutcome, from, until, lane int, dwell time.Duration) {
	t.Helper()
	for side, routes := range o.voiceRoutes {
		last, changed, seen := -1, -1, false
		for _, route := range routes {
			if route.at < from*1000 || route.at >= until*1000 {
				continue
			}
			seen = true
			if lane >= 0 && route.lane != lane {
				t.Errorf("direction %d call primary lane %d at %.3fs, requires lane %d", side, route.lane, float64(route.at)/1000, lane)
				break
			}
			if last >= 0 && route.lane != last {
				if changed >= 0 && time.Duration(route.at-changed)*time.Millisecond < dwell {
					t.Errorf("direction %d call moves twice in %s at %.3fs", side, dwell, float64(route.at)/1000)
					break
				}
				changed = route.at
			}
			last = route.lane
		}
		if !seen {
			t.Errorf("direction %d has no primary voice transmissions in [%d,%d)", side, from, until)
		}
	}
}

func TestAdaptivePolicy1aBlackout(t *testing.T) {
	for family, lanes := range policyProfiles() {
		for failed := 0; failed < 2; failed++ {
			for _, bulk := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/lane%d/bulk%t", family, failed, bulk), func(t *testing.T) {
					changed := policyChange(lanes, failed, func(side int, at time.Duration, c modelCondition) modelCondition {
						c.dark = at >= 20*time.Second && at < 35*time.Second
						return c
					})
					m := policyRun{lanes: changed, seconds: 45, trafficAt: 2, bulk: bulk, voice: true}
					o := m.run(t)
					checkPolicyVoice(t, o, 2, 45, 0, false)
					checkPolicyVoice(t, o, 21, 35, []time.Duration{110 * time.Millisecond, 182 * time.Millisecond}[1-failed], false)
					if bulk {
						checkPolicyProgress(t, o, 21, 35)
						checkPolicyBulkDeadline(t, m, o, 23, .75)
						checkPolicyBulk(t, m, o, 23, 35, .75)
					}
				})
			}
		}
	}
}

func TestAdaptivePolicy1bRecovery(t *testing.T) {
	for family, lanes := range policyProfiles() {
		for failed := 0; failed < 2; failed++ {
			t.Run(fmt.Sprintf("%s/lane%d", family, failed), func(t *testing.T) {
				changed := policyChange(lanes, failed, func(side int, at time.Duration, c modelCondition) modelCondition {
					c.dark = at >= 20*time.Second && at < 35*time.Second
					return c
				})
				m := policyRun{lanes: changed, seconds: 45, trafficAt: 2, bulk: true, voice: true}
				o := m.run(t)
				checkPolicyVoice(t, o, 35, 45, 150*time.Millisecond, false)
				for side := range o.laneBulk {
					if o.laneBulk[side][failed][35]+o.laneBulk[side][failed][36] == 0 {
						t.Errorf("direction %d recovered lane has no bulk within 2s", side)
					}
				}
				checkPolicyBulk(t, m, o, 40, 45, .75)
				checkPolicyBulkDeadline(t, m, o, 40, .75)
			})
		}
	}
}

func TestAdaptivePolicy1cOneWayBlackout(t *testing.T) {
	for family, lanes := range policyProfiles() {
		for failed := 0; failed < 2; failed++ {
			for direction := 0; direction < 2; direction++ {
				for _, bulk := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/lane%d/direction%d/bulk%t", family, failed, direction, bulk), func(t *testing.T) {
						changed := policyChange(lanes, failed, func(side int, at time.Duration, c modelCondition) modelCondition {
							c.dark = side == direction && at >= 20*time.Second && at < 35*time.Second
							return c
						})
						m := policyRun{lanes: changed, seconds: 45, trafficAt: 2, bulk: bulk, voice: true}
						o := m.run(t)
						checkPolicyVoice(t, o, 2, 45, 0, false)
						checkPolicyVoice(t, o, 21, 35, []time.Duration{110 * time.Millisecond, 182 * time.Millisecond}[1-failed], false)
						if bulk {
							checkPolicyProgress(t, o, 21, 35)
							checkPolicyBulkDeadline(t, m, o, 23, .75)
							checkPolicyBulk(t, m, o, 23, 35, .75)
						}
					})
				}
			}
		}
	}
}

func TestAdaptivePolicy2aRateFalls(t *testing.T) {
	for family, lanes := range policyProfiles() {
		for _, buffer := range []time.Duration{10 * time.Millisecond, 500 * time.Millisecond} {
			t.Run(fmt.Sprintf("%s/buffer%s", family, buffer), func(t *testing.T) {
				changed := policyChange(lanes, 1, func(side int, at time.Duration, c modelCondition) modelCondition {
					c.buffer = buffer
					if at >= 20*time.Second {
						c.rate *= .25
					}
					return c
				})
				m := policyRun{lanes: changed, seconds: 40, trafficAt: 2, bulk: true, voice: true}
				o := m.run(t)
				checkPolicyVoice(t, o, 18, 40, 150*time.Millisecond, false)
				checkPolicyBulk(t, m, o, 25, 40, .75)
				checkPolicyBulkDeadline(t, m, o, 25, .75)
				for side, states := range o.states {
					for i := 201; i < 260; i++ {
						if delta := states[i].Expired - states[i-1].Expired; delta > 3 {
							t.Errorf("direction %d has burst of %d expired datagrams at %.1fs", side, delta, float64(i)/10)
							break
						}
					}
				}
			})
		}
	}
}

func TestAdaptivePolicy2bRateRises(t *testing.T) {
	for family, lanes := range policyProfiles() {
		t.Run(family, func(t *testing.T) {
			changed := policyChange(lanes, 1, func(side int, at time.Duration, c modelCondition) modelCondition {
				if at < 20*time.Second {
					c.rate *= .2
				}
				return c
			})
			m := policyRun{lanes: changed, seconds: 45, trafficAt: 2, bulk: true, voice: true}
			o := m.run(t)
			checkPolicyVoice(t, o, 20, 45, 150*time.Millisecond, false)
			checkPolicyBulk(t, m, o, 30, 45, .75)
			checkPolicyBulkDeadline(t, m, o, 30, .75)
		})
	}
}

func TestAdaptivePolicy2cPlanChanges(t *testing.T) {
	for family, lanes := range policyProfiles() {
		t.Run(family, func(t *testing.T) {
			changed := policyChange(lanes, 0, func(side int, at time.Duration, c modelCondition) modelCondition {
				c.rate, c.policed, c.loss = 62500, true, 0
				if at >= 20*time.Second && at < 50*time.Second {
					c.rate = []float64{12.5e6, 1.875e6}[side]
					c.policed = false
				}
				return c
			})
			m := policyRun{lanes: changed, seconds: 65, trafficAt: 2, bulk: true, voice: true}
			o := m.run(t)
			checkPolicyVoice(t, o, 2, 65, 150*time.Millisecond, false)
			checkPolicyRoute(t, o, 5, 65, 0, 0)
			checkPolicyBulk(t, m, o, 40, 50, .75)
			checkPolicyBulkDeadline(t, m, o, 40, .75)
			for side := range o.laneSent {
				var sent, dropped float64
				for second := 53; second < 65; second++ {
					sent += o.laneSent[side][0][second]
					dropped += o.laneDropped[side][0][second]
				}
				if sent == 0 || dropped/sent >= .05 {
					t.Errorf("direction %d standby lane drops %.1f%% after 3s", side, 100*dropped/sent)
				}
			}
		})
	}
}

func TestAdaptivePolicy2dCellularGrants(t *testing.T) {
	for family, lanes := range policyProfiles() {
		t.Run(family, func(t *testing.T) {
			changed := policyChange(lanes, 1, func(side int, at time.Duration, c modelCondition) modelCondition {
				draw := rand.New(rand.NewPCG(uint64(at/(100*time.Millisecond)), uint64(side+13)))
				c.rate = []float64{6.25e6, 750000}[side] * (1 + .6*(2*draw.Float64()-1))
				c.delay = 25*time.Millisecond + time.Duration(draw.Int64N(int64(10*time.Millisecond))) - 5*time.Millisecond
				c.buffer = 400 * time.Millisecond
				return c
			})
			m := policyRun{lanes: changed, seconds: 40, trafficAt: 2, bulk: true, voice: true}
			o := m.run(t)
			checkPolicyVoice(t, o, 10, 40, 150*time.Millisecond, false)
			checkPolicyBulk(t, m, o, 20, 40, .7)
		})
	}
}

func checkPolicyShift(t *testing.T, o policyOutcome, betterRTT time.Duration, from, until int) {
	t.Helper()
	for side, stream := range o.voice {
		var rtts []time.Duration
		for _, v := range stream {
			if v.sent >= from*1000 && v.sent < until*1000 && v.arrived >= 0 {
				rtts = append(rtts, v.rtt)
			}
		}
		if len(rtts) == 0 {
			t.Fatalf("direction %d has no samples", side)
		}
		median := policyQuantile(rtts, 50)
		if median > betterRTT+20*time.Millisecond {
			t.Errorf("direction %d RTT median %s > better lane %s + 20ms", side, median, betterRTT)
		}
	}
}

func checkPolicyBulkReduction(t *testing.T, o policyOutcome, from, until int) {
	t.Helper()
	for side, stream := range o.bulk {
		before := 0.0
		for _, bytes := range stream[from-5 : from] {
			before += bytes / 5
		}
		run := 0
		for second := from; second < until; second++ {
			if stream[second] < .75*before {
				run++
			} else {
				run = 0
			}
			if run > 2 {
				t.Errorf("direction %d bulk cut >25%% for %ds after latency change; before %.0f B/s, now %.0f", side, run, before, stream[second])
				break
			}
		}
	}
}

func TestAdaptivePolicy3aCallLaneGainsDelay(t *testing.T) {
	for family, lanes := range policyProfiles() {
		for _, bulk := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/bulk%t", family, bulk), func(t *testing.T) {
				changed := policyChange(lanes, 0, func(side int, at time.Duration, c modelCondition) modelCondition {
					if at >= 20*time.Second {
						c.delay += 100 * time.Millisecond
					}
					return c
				})
				m := policyRun{lanes: changed, seconds: 35, trafficAt: 2, bulk: bulk, voice: true}
				o := m.run(t)
				checkPolicyShift(t, o, 80*time.Millisecond, 22, 24)
				if bulk {
					checkPolicyBulkReduction(t, o, 20, 35)
				}
			})
		}
	}
}

func TestAdaptivePolicy3bOtherLaneBecomesBetter(t *testing.T) {
	for family, lanes := range policyProfiles() {
		for _, bulk := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/bulk%t", family, bulk), func(t *testing.T) {
				changed := policyChange(lanes, 1, func(side int, at time.Duration, c modelCondition) modelCondition {
					if at >= 20*time.Second {
						c.delay = 5*time.Millisecond + c.delay/10
					}
					return c
				})
				m := policyRun{lanes: changed, seconds: 40, trafficAt: 2, bulk: bulk, voice: true}
				o := m.run(t)
				checkPolicyShift(t, o, 18*time.Millisecond, 25, 30)
				checkPolicyRoute(t, o, 25, 40, 1, 0)
				checkPolicyRoute(t, o, 5, 40, -1, 5*time.Second)
				if bulk {
					checkPolicyBulkReduction(t, o, 20, 40)
				}
			})
		}
	}
}

func TestAdaptivePolicy3cBothLanesGainDelay(t *testing.T) {
	for family, lanes := range policyProfiles() {
		for _, bulk := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/bulk%t", family, bulk), func(t *testing.T) {
				changed := lanes
				for lane := range lanes {
					changed = policyChange(changed, lane, func(side int, at time.Duration, c modelCondition) modelCondition {
						if at >= 20*time.Second {
							c.delay += 50 * time.Millisecond
						}
						return c
					})
				}
				m := policyRun{lanes: changed, seconds: 40, trafficAt: 2, bulk: bulk, voice: true}
				o := m.run(t)
				checkPolicyVoice(t, o, 20, 40, 0, true)
				if bulk {
					checkPolicyBulkReduction(t, o, 20, 40)
				}
			})
		}
	}
}

func TestAdaptivePolicy0ColdTransfer(t *testing.T) {
	for family, lanes := range policyProfiles() {
		t.Run(family, func(t *testing.T) {
			m := policyRun{lanes: lanes, seconds: 45, trafficAt: 30, bulk: true, voice: false}
			o := m.run(t)
			for side, stream := range o.bulk {
				capacity := 0.0
				for _, lane := range lanes {
					capacity += lane.at(side, 37*time.Second).rate
				}
				if stream[36] < .6*capacity {
					t.Errorf("direction %d first transfer %.0f B/s at 7s <60%% of pair %.0f B/s", side, stream[36], capacity)
				}
			}
		})
	}
}
