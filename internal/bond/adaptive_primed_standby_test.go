//go:build adaptivepolicy

package bond_test

import (
	"testing"
	"time"
)

// Performance-Progression-Blackbox-Group: voice priming precedes an upload rate fall.
func TestAdaptivePolicyVoicePrimedStandbyStartsUpload(t *testing.T) {
	m := policyRun{lanes: []modelLane{
		{rate: 62500, delay: 15 * time.Millisecond, buffer: 100 * time.Millisecond},
		{condition: func(side int, at time.Duration) modelCondition {
			rate := 375000.0
			if at >= 10*time.Second {
				rate = 50000
			}
			if side == 0 {
				rate = 12.5e6
			}
			return modelCondition{rate: rate, delay: 10 * time.Millisecond, buffer: 100 * time.Millisecond}
		}},
	}, seconds: 25, trafficAt: 0, voice: true, bulk: true,
		bulkDirection: policyBulkUplink,
		bulkPeriods:   []policyBulkPeriod{{from: 10 * time.Second, until: 25 * time.Second}}}
	o := m.run(t)
	checkPolicyVoice(t, o, 10, 25, 150*time.Millisecond, false)
	reference := m.reference(14 * time.Second)[1]
	if reference <= 0 {
		t.Fatal("upload reference is non-positive")
	}
	got := o.bulk[1][14]
	t.Logf("upload in [14,15): %.0f B/s, reference %.0f B/s", got, reference)
	if got < .75*reference {
		for second := 10; second < 20; second++ {
			state := o.states[1][second*10+9]
			t.Logf("at %ds: upload %.0f B/s, expired %d", second+1, o.bulk[1][second], state.Expired)
			for _, lane := range state.Paths {
				t.Logf("lane %d: target %.0f capacity %.0f queue %s flight %d/%d", lane.Path, lane.Rate, lane.Capacity, lane.QueueDelay, lane.InFlight, lane.Window)
			}
		}
		t.Errorf("upload %.0f B/s < 75%% of available goodput %.0f B/s", got, reference)
	}
}
