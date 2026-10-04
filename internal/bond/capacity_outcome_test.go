package bond_test

import (
	"fmt"
	"testing"
	"time"
)

// Performance-Blackbox-Group: sparse traffic must not prevent a later
// transfer from using the path, including after an earlier transfer.
func TestSparseAndResumedSendersKeepBulkProductive(t *testing.T) {
	checkSparseAndResumedSenders(t, false)
}

func checkSparseAndResumedSenders(t *testing.T, policed bool) {
	t.Helper()
	for _, resumed := range []bool{false, true} {
		t.Run(fmt.Sprintf("policed_%v/resumed_%v", policed, resumed), func(t *testing.T) {
			periods := []policyBulkPeriod{{12 * time.Second, 25 * time.Second}}
			if resumed {
				periods = append(periods, policyBulkPeriod{2 * time.Second, 7 * time.Second})
			}
			m := policyRun{lanes: []modelLane{{rate: 1.25e6, delay: 20 * time.Millisecond, policed: policed}},
				seconds: 25, trafficAt: 2, voice: true, bulk: true, bulkPeriods: periods}
			checkCapacityService(t, m, 20, 25)
		})
	}
}

// Performance-Blackbox-Group: a formerly slow lane must carry the extra
// demand after its service rate rises, rather than merely count probe wins.
func checkSlowLaneRateIncrease(t *testing.T) {
	t.Helper()
	m := policyRun{lanes: []modelLane{{condition: func(side int, at time.Duration) modelCondition {
		rate := 90e3
		if at >= 10*time.Second {
			rate = 625e3
		}
		return modelCondition{rate: rate, delay: 30 * time.Millisecond, buffer: 100 * time.Millisecond}
	}}}, seconds: 30, trafficAt: 2, voice: true, bulk: true}
	checkCapacityService(t, m, 20, 30)
}

// Performance-Blackbox-Group: batched receipts must not strand bulk at an
// estimate established by one release of the receiver's backlog.
func TestBatchedReceiptsKeepBulkProductive(t *testing.T) {
	lane := varyingLane{rate: 6.25e6, delay: 25 * time.Millisecond, buffer: 100 * time.Millisecond, batch: 50 * time.Millisecond}
	o := mixedLoad{lanes: []varyingLane{lane}, offered: 8e6, seconds: 30, failed: -1}.run()
	t.Logf("bulk %.0f B/s; voice %d/%d, p99 %s", o.bulk, o.voiceDelivered, o.voiceSent, o.voiceP99)
	if o.bulk < .75*lane.rate {
		t.Errorf("bulk %.0f B/s <75%% of %.0f B/s service", o.bulk, lane.rate)
	}
}

func checkCapacityService(t *testing.T, m policyRun, from, until int) {
	t.Helper()
	o := m.run(t)
	for side, stream := range o.bulk {
		var delivered float64
		for _, bytes := range stream[from:until] {
			delivered += bytes
		}
		rate := delivered / float64(until-from)
		reference := m.reference(time.Duration(from) * time.Second)[side]
		t.Logf("direction %d payload %.0f B/s, reference %.0f B/s", side, rate, reference)
		if rate < .75*reference {
			t.Errorf("direction %d bulk %.0f B/s <75%% of available reference %.0f B/s", side, rate, reference)
		}
	}
}
