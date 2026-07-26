package monitor

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/7mind/wanbond/internal/metrics"
	"github.com/7mind/wanbond/internal/shaper"
)

func TestRecoveryObservabilityMirrorsMetricsSnapshot(t *testing.T) {
	snapshot := BuildSnapshot(fakeSource{
		paths: []metrics.PathSnapshot{{Name: "wan0", Shaper: &shaper.Snapshot{}}},
		fec:   []metrics.FECSnapshot{{}},
		reseq: []metrics.ReseqSnapshot{{}},
	}, Info{}, true, true)
	wire, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{
		`"stagedGroups"`,
		`"stagedDataFrames"`,
		`"groupDecisions"`,
		`"deadlineDecisions"`,
		`"deadlineMisses"`,
		`"deadlineMaxOvershootSeconds"`,
		`"openGroupDeadlineUnixNano"`,
		`"recovery"`,
		`"offerPresent"`,
		`"fastEligible"`,
		`"transitionFrozen"`,
		`"writerExclusive"`,
		`"fallbackReason"`,
		`"serviceBoundNanos"`,
		`"rttAgeNanos"`,
		`"headroomNanos"`,
		`"windowNanos"`,
		`"outerPriorityEmittedBytes"`,
		`"outerPriorityErrorBytes"`,
		`"recoveryCutActive"`,
		`"recoveryCutDeadlineUnixNano"`,
		`"recoveryCutDatagrams"`,
		`"recoveryCutSocketCalls"`,
		`"fecGroupOwnedHighWaterBytes"`,
		`"memoryRetainedHighWaterBytes"`,
		`"recoveryArmed"`,
		`"armedDeadlineUnixNano"`,
		`"armedWindowNanos"`,
		`"deadlineWakeups"`,
		`"gapFills"`,
		`"fastWindowArms"`,
		`"fallbackWindowArms"`,
	} {
		if !strings.Contains(string(wire), field) {
			t.Errorf("monitor snapshot omitted %s: %s", field, wire)
		}
	}
}
