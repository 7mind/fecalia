//go:build adaptivepolicy

package bond_test

import (
	"testing"
	"time"
)

// Performance-Blackbox-Group: scenario 2a with a buffered low-latency lane.
func TestAdaptivePolicy2aBufferedLowLatencyLaneFalls(t *testing.T) {
	const allowedExpiredBurst = 3
	m := policyRun{lanes: []modelLane{
		{condition: func(side int, at time.Duration) modelCondition {
			rate := 250000.0
			if at >= 12*time.Second {
				rate = 62500
			}
			return modelCondition{rate: rate, delay: 15 * time.Millisecond, buffer: 100 * time.Millisecond}
		}},
		{rate: 750000, delay: 25 * time.Millisecond},
	}, seconds: 20, trafficAt: 2, voice: true, bulk: true}
	o := m.run(t)
	checkPolicyVoice(t, o, 12, 18, 150*time.Millisecond, false)
	checkPolicyBulkDeadline(t, m, o, 17, .75)
	for side, states := range o.states {
		for sample := 121; sample < 180; sample++ {
			if expired := states[sample].Expired - states[sample-1].Expired; expired > allowedExpiredBurst {
				t.Errorf("direction %d has burst of %d expired datagrams at %.1fs", side, expired, float64(sample)/10)
				break
			}
		}
	}
}
