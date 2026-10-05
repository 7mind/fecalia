//go:build adaptivepolicy

package bond_test

import (
	"testing"
	"time"
)

// Performance-Blackbox-Group: a call on the lower-latency lane must not
// prevent queued TCP from discovering an uplink rate increase.
func TestAdaptiveVoiceLaneUplinkUpgrade(t *testing.T) {
	const changedAt = 20
	lanes := []modelLane{
		{rate: 62500, delay: 18 * time.Millisecond, policed: true},
		{condition: func(side int, at time.Duration) modelCondition {
			rate := 12.5e6
			if side == 1 {
				rate = 50000
				if at >= changedAt*time.Second {
					rate = 250000
				}
			}
			return modelCondition{rate: rate, delay: 10 * time.Millisecond, buffer: 100 * time.Millisecond}
		}},
	}
	m := policyRun{lanes: lanes, seconds: 40, trafficAt: 2, voice: true, bulk: true, bulkDirection: policyBulkUplink}
	o := m.run(t)
	checkPolicyVoice(t, o, changedAt, 40, 150*time.Millisecond, false)
	const deadline = changedAt + 10
	reference := m.reference(deadline * time.Second)[1]
	if reference <= 0 {
		t.Fatal("uplink reference is non-positive")
	}
	for _, interval := range [][2]int{{deadline - 1, deadline}, {deadline, 40}} {
		var delivered float64
		for _, bytes := range o.bulk[1][interval[0]:interval[1]] {
			delivered += bytes
		}
		rate := delivered / float64(interval[1]-interval[0])
		t.Logf("uplink in [%d,%d): %.0f B/s; independent available reference %.0f B/s", interval[0], interval[1], rate, reference)
		if rate < .75*reference {
			t.Errorf("uplink %.0f B/s <75%% of available reference %.0f B/s", rate, reference)
		}
	}
	for _, second := range []int{19, 21, 25, 29, 35} {
		t.Logf("at %ds: %+v", second, o.states[1][second*10].Paths)
	}
}
