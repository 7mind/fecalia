package congestion_test

import (
	"math"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/congestion"
)

func TestControllerReducesCongestedCarrierAndHoldsStaleFeedback(t *testing.T) {
	controller, err := congestion.New(1_000_000)
	if err != nil {
		t.Fatal(err)
	}
	epoch := congestion.CarrierEpoch{PathID: 7, Generation: 11}
	at := time.Unix(100, 0)
	initial, err := controller.Observe(congestion.ActualState{
		At:               at,
		Epoch:            epoch,
		RTT:              40 * time.Millisecond,
		LossFresh:        true,
		FeedbackEverSeen: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	congested, err := controller.Observe(congestion.ActualState{
		At:                at.Add(time.Second),
		Epoch:             epoch,
		OuterWireBytes:    850_000,
		InnerDataBytes:    700_000,
		RTT:               80 * time.Millisecond,
		AuthenticatedLoss: 0.02,
		LossFresh:         true,
		FeedbackEverSeen:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if congested.Target.OuterRateBytesPerSecond >= initial.Target.OuterRateBytesPerSecond {
		t.Fatalf("congested target = %g, want below initial %g",
			congested.Target.OuterRateBytesPerSecond,
			initial.Target.OuterRateBytesPerSecond)
	}
	if congested.Target.IngressRateBytesPerSecond >= congested.Target.OuterRateBytesPerSecond {
		t.Fatalf("ingress target = %g, want below outer target %g after measured overhead",
			congested.Target.IngressRateBytesPerSecond,
			congested.Target.OuterRateBytesPerSecond)
	}
	if congested.BaseRTT != 40*time.Millisecond || congested.QueueDelay != 40*time.Millisecond {
		t.Fatalf("RTT state = base %s queue %s, want 40ms/40ms",
			congested.BaseRTT, congested.QueueDelay)
	}

	stale, err := controller.Observe(congestion.ActualState{
		At:               at.Add(2 * time.Second),
		Epoch:            epoch,
		OuterWireBytes:   1_572_500,
		InnerDataBytes:   1_300_000,
		RTT:              40 * time.Millisecond,
		FeedbackEverSeen: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if stale.Target != congested.Target || !stale.Held {
		t.Fatalf("stale feedback target = %+v held=%v, want held %+v",
			stale.Target, stale.Held, congested.Target)
	}
}

func TestControllerCarrierEpochDeprecatesPriorActualState(t *testing.T) {
	controller, err := congestion.New(1_000_000)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Unix(200, 0)
	firstEpoch := congestion.CarrierEpoch{PathID: 1, Generation: 4}
	if _, err := controller.Observe(congestion.ActualState{
		At: at, Epoch: firstEpoch, RTT: 35 * time.Millisecond,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Observe(congestion.ActualState{
		At: at.Add(time.Second), Epoch: firstEpoch,
		OuterWireBytes: 850_000, InnerDataBytes: 700_000,
		RTT: 90 * time.Millisecond, AuthenticatedLoss: 0.05, LossFresh: true,
	}); err != nil {
		t.Fatal(err)
	}

	nextEpoch := congestion.CarrierEpoch{PathID: 2, Generation: 5}
	reset, err := controller.Observe(congestion.ActualState{
		At: at.Add(2 * time.Second), Epoch: nextEpoch,
		OuterWireBytes: 900_000, InnerDataBytes: 750_000,
		RTT: 55 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if reset.Target.Epoch != nextEpoch ||
		reset.BaseRTT != 55*time.Millisecond ||
		reset.QueueDelay != 0 ||
		!reset.Held {
		t.Fatalf("new epoch snapshot = %+v", reset)
	}
	if math.Abs(reset.Target.OuterRateBytesPerSecond-850_000) > 0.001 {
		t.Fatalf("new epoch target = %g, want conservative seed 850000",
			reset.Target.OuterRateBytesPerSecond)
	}
}

func TestControllerIgnoresUnloadedOuterInnerExpansion(t *testing.T) {
	controller, err := congestion.New(1_000_000)
	if err != nil {
		t.Fatal(err)
	}
	epoch := congestion.CarrierEpoch{PathID: 1, Generation: 1}
	at := time.Unix(300, 0)
	if _, err := controller.Observe(congestion.ActualState{
		At: at, Epoch: epoch, RTT: 30 * time.Millisecond,
	}); err != nil {
		t.Fatal(err)
	}
	unloaded, err := controller.Observe(congestion.ActualState{
		At: at.Add(time.Second), Epoch: epoch,
		OuterWireBytes: 100_000, InnerDataBytes: 10_000,
		RTT: 30 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(unloaded.OverheadRatio-1.25) > 0.001 ||
		math.Abs(unloaded.Target.IngressRateBytesPerSecond-680_000) > 0.001 {
		t.Fatalf("unloaded sample changed expansion/ingress target: ratio=%g ingress=%g",
			unloaded.OverheadRatio, unloaded.Target.IngressRateBytesPerSecond)
	}
}
