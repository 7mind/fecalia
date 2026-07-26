package monitor

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/metrics"
	"github.com/7mind/wanbond/internal/shaper"
)

func TestRecoveryObservabilityMirrorsMetricsSnapshot(t *testing.T) {
	freshSender := time.Unix(100, 101)
	freshReceiver := time.Unix(200, 202)
	recovery := metrics.RecoveryStats{
		Sender: metrics.RecoveryDirectionStats{
			OfferPresent:     true,
			FastEligible:     true,
			TransitionFrozen: true,
			WriterExclusive:  true,
			FreshUntil:       freshSender,
			OfferWrites:      1,
			ACKWrites:        2,
			OfferAccepts:     3,
			ACKAccepts:       4,
			Rotations:        5,
			SessionRestarts:  6,
			StaleRejections:  7,
			WrongRejections:  8,
			ReplayRejections: 9,
			FallbackReason:   "sender-fallback",
			ServiceBound:     10 * time.Nanosecond,
			RTTAge:           11 * time.Nanosecond,
			Headroom:         12 * time.Nanosecond,
			Window:           13 * time.Nanosecond,
		},
		Receiver: metrics.RecoveryDirectionStats{
			OfferPresent:     true,
			FastEligible:     true,
			TransitionFrozen: true,
			WriterExclusive:  true,
			FreshUntil:       freshReceiver,
			OfferWrites:      14,
			ACKWrites:        15,
			OfferAccepts:     16,
			ACKAccepts:       17,
			Rotations:        18,
			SessionRestarts:  19,
			StaleRejections:  20,
			WrongRejections:  21,
			ReplayRejections: 22,
			FallbackReason:   "receiver-fallback",
			ServiceBound:     23 * time.Nanosecond,
			RTTAge:           24 * time.Nanosecond,
			Headroom:         25 * time.Nanosecond,
			Window:           26 * time.Nanosecond,
		},
	}
	snapshot := BuildSnapshot(fakeSource{
		paths: []metrics.PathSnapshot{{Name: "wan0", Shaper: &shaper.Snapshot{}}},
		fec:   []metrics.FECSnapshot{{Recovery: recovery}},
		reseq: []metrics.ReseqSnapshot{{}},
	}, Info{}, true, true)
	wantSender := monitorRecoveryDirection(recovery.Sender)
	wantReceiver := monitorRecoveryDirection(recovery.Receiver)
	if got := snapshot.FEC[0].Recovery.Sender; got != wantSender {
		t.Fatalf("sender recovery snapshot = %+v, want %+v", got, wantSender)
	}
	if got := snapshot.FEC[0].Recovery.Receiver; got != wantReceiver {
		t.Fatalf("receiver recovery snapshot = %+v, want %+v", got, wantReceiver)
	}
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
		`"sender"`,
		`"receiver"`,
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
