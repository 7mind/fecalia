package congestion_test

import (
	"math"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/congestion"
)

func TestControllerReducesCongestedCarrierAndHoldsStaleFeedback(t *testing.T) {
	controller, err := congestion.New(1_000_000, 0)
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
	if err := controller.ObserveInstalledIngress(congestion.InstalledIngressState{
		At:                 at.Add(1100 * time.Millisecond),
		Epoch:              epoch,
		RateBytesPerSecond: congested.Target.IngressRateBytesPerSecond,
		Fresh:              true,
	}); err != nil {
		t.Fatal(err)
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

	queueCongested, err := controller.Observe(congestion.ActualState{
		At:               at.Add(3 * time.Second),
		Epoch:            epoch,
		OuterWireBytes:   2_295_000,
		InnerDataBytes:   1_900_000,
		RTT:              80 * time.Millisecond,
		FeedbackEverSeen: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if queueCongested.Target != stale.Target || !queueCongested.Held {
		t.Fatalf("first stale queue-delay sample changed target: target=%+v held=%v, want held %+v",
			queueCongested.Target, queueCongested.Held, stale.Target)
	}
	queueCongested, err = controller.Observe(congestion.ActualState{
		At:               at.Add(4 * time.Second),
		Epoch:            epoch,
		OuterWireBytes:   3_017_500,
		InnerDataBytes:   2_500_000,
		RTT:              80 * time.Millisecond,
		FeedbackEverSeen: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if queueCongested.Target.OuterRateBytesPerSecond >= stale.Target.OuterRateBytesPerSecond {
		t.Fatalf("stale DATA feedback blocked local queue-delay decrease: target=%g prior=%g",
			queueCongested.Target.OuterRateBytesPerSecond,
			stale.Target.OuterRateBytesPerSecond)
	}
}

func TestControllerHoldsRepeatedRetargetUntilInstalledRateSettles(t *testing.T) {
	controller, err := congestion.New(1_000_000, 0)
	if err != nil {
		t.Fatal(err)
	}
	epoch := congestion.CarrierEpoch{PathID: 3, Generation: 8}
	at := time.Unix(150, 0)
	initial, err := controller.Observe(congestion.ActualState{
		At: at, Epoch: epoch, RTT: 40 * time.Millisecond,
		LossFresh: true, FeedbackEverSeen: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	first, err := controller.Observe(congestion.ActualState{
		At: at.Add(200 * time.Millisecond), Epoch: epoch,
		OuterWireBytes: 170_000, InnerDataBytes: 136_000,
		RTT: 80 * time.Millisecond, AuthenticatedLoss: 0.01,
		LossFresh: true, FeedbackEverSeen: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Target.OuterRateBytesPerSecond >= initial.Target.OuterRateBytesPerSecond {
		t.Fatalf("first congested control tick held target %g, want prompt decrease below %g",
			first.Target.OuterRateBytesPerSecond, initial.Target.OuterRateBytesPerSecond)
	}
	if first.TargetChanges != 1 || !first.AwaitingInstalled {
		t.Fatalf("first retarget state = changes %d awaiting=%t, want 1/true",
			first.TargetChanges, first.AwaitingInstalled)
	}
	if first.InstalledIngress.Fresh {
		t.Fatal("first retarget retained a fresh installed-rate acknowledgment")
	}

	second, err := controller.Observe(congestion.ActualState{
		At: at.Add(400 * time.Millisecond), Epoch: epoch,
		OuterWireBytes: 314_500, InnerDataBytes: 251_600,
		RTT: 80 * time.Millisecond, AuthenticatedLoss: 0.01,
		LossFresh: true, FeedbackEverSeen: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.Target != first.Target || !second.Held {
		t.Fatalf("second congested tick retargeted before installed-rate readback: target=%+v held=%t, want held %+v",
			second.Target, second.Held, first.Target)
	}
	if second.TargetChanges != 1 {
		t.Fatalf("held tick target changes = %d, want 1", second.TargetChanges)
	}
	if err := controller.ObserveInstalledIngress(congestion.InstalledIngressState{
		At:                 at.Add(450 * time.Millisecond),
		Epoch:              epoch,
		RateBytesPerSecond: first.Target.IngressRateBytesPerSecond,
		Fresh:              true,
	}); err != nil {
		t.Fatal(err)
	}
	if !controller.Snapshot().InstalledIngress.Fresh {
		t.Fatal("exact installed-rate acknowledgment remained stale")
	}

	beforeSettle, err := controller.Observe(congestion.ActualState{
		At: at.Add(time.Second), Epoch: epoch,
		OuterWireBytes: 748_000, InnerDataBytes: 598_400,
		RTT: 80 * time.Millisecond, AuthenticatedLoss: 0.01,
		LossFresh: true, FeedbackEverSeen: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if beforeSettle.Target != first.Target || !beforeSettle.Held {
		t.Fatalf("target changed before post-readback settle: target=%+v held=%t, want held %+v",
			beforeSettle.Target, beforeSettle.Held, first.Target)
	}
	if err := controller.ObserveInstalledIngress(congestion.InstalledIngressState{
		At:                 at.Add(time.Second),
		Epoch:              epoch,
		RateBytesPerSecond: first.Target.IngressRateBytesPerSecond,
		Fresh:              true,
	}); err != nil {
		t.Fatal(err)
	}
	if got := controller.Snapshot().InstalledIngress.At; got != at.Add(450*time.Millisecond) {
		t.Fatalf("repeated exact installed-rate readback moved settle start to %s, want %s",
			got, at.Add(450*time.Millisecond))
	}

	afterSettle, err := controller.Observe(congestion.ActualState{
		At: at.Add(1500 * time.Millisecond), Epoch: epoch,
		OuterWireBytes: 1_109_250, InnerDataBytes: 887_400,
		RTT: 80 * time.Millisecond, AuthenticatedLoss: 0.01,
		LossFresh: true, FeedbackEverSeen: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if afterSettle.Target.OuterRateBytesPerSecond >= first.Target.OuterRateBytesPerSecond ||
		afterSettle.Held {
		t.Fatalf("target after exact readback and settle = %+v held=%t, want second decrease",
			afterSettle.Target, afterSettle.Held)
	}
	if afterSettle.TargetChanges != 2 || !afterSettle.AwaitingInstalled {
		t.Fatalf("second retarget state = changes %d awaiting=%t, want 2/true",
			afterSettle.TargetChanges, afterSettle.AwaitingInstalled)
	}
	if afterSettle.InstalledIngress.Fresh {
		t.Fatal("second retarget retained the prior installed rate as fresh")
	}
}

func TestControllerCarrierEpochDeprecatesPriorActualState(t *testing.T) {
	controller, err := congestion.New(1_000_000, 0)
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

func TestControllerDiscoversCapacityAboveSeedAndPromptlyReduces(t *testing.T) {
	const seed = 1_000_000.0
	controller, err := congestion.New(seed, 0)
	if err != nil {
		t.Fatal(err)
	}
	epoch := congestion.CarrierEpoch{PathID: 4, Generation: 12}
	at := time.Unix(250, 0)
	snapshot, err := controller.Observe(congestion.ActualState{
		At: at, Epoch: epoch, RTT: 40 * time.Millisecond,
		LossFresh: true, FeedbackEverSeen: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var outerBytes, innerBytes uint64
	for step := 1; step <= 12; step++ {
		target := snapshot.Target.OuterRateBytesPerSecond
		outerBytes += uint64(target * 2)
		innerBytes += uint64(target * 2 / 1.25)
		at = at.Add(2 * time.Second)
		snapshot, err = controller.Observe(congestion.ActualState{
			At: at, Epoch: epoch,
			OuterWireBytes: outerBytes, InnerDataBytes: innerBytes,
			RTT: 40 * time.Millisecond, LossFresh: true, FeedbackEverSeen: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.AwaitingInstalled {
			if err := controller.ObserveInstalledIngress(congestion.InstalledIngressState{
				At: at.Add(100 * time.Millisecond), Epoch: epoch,
				RateBytesPerSecond: snapshot.Target.IngressRateBytesPerSecond,
				Fresh:              true,
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if snapshot.Target.OuterRateBytesPerSecond <= seed {
		t.Fatalf("clean loaded target = %g, want discovered capacity above seed %g",
			snapshot.Target.OuterRateBytesPerSecond, seed)
	}

	beforeCongestion := snapshot.Target.OuterRateBytesPerSecond
	outerBytes += uint64(beforeCongestion * 2)
	innerBytes += uint64(beforeCongestion * 2 / 1.25)
	congested, err := controller.Observe(congestion.ActualState{
		At: at.Add(2 * time.Second), Epoch: epoch,
		OuterWireBytes: outerBytes, InnerDataBytes: innerBytes,
		RTT: 100 * time.Millisecond, AuthenticatedLoss: 0.01,
		LossFresh: true, FeedbackEverSeen: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if congested.Target.OuterRateBytesPerSecond >= beforeCongestion {
		t.Fatalf("congested target = %g, want prompt decrease below %g",
			congested.Target.OuterRateBytesPerSecond, beforeCongestion)
	}
}

func TestControllerHonorsDistinctExplicitLimit(t *testing.T) {
	const (
		seed  = 1_000_000.0
		limit = 1_200_000.0
	)
	controller, err := congestion.New(seed, limit)
	if err != nil {
		t.Fatal(err)
	}
	epoch := congestion.CarrierEpoch{PathID: 5, Generation: 1}
	at := time.Unix(280, 0)
	snapshot, err := controller.Observe(congestion.ActualState{
		At: at, Epoch: epoch, RTT: 40 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	var outerBytes uint64
	for step := 0; step < 10; step++ {
		target := snapshot.Target.OuterRateBytesPerSecond
		outerBytes += uint64(target * 2)
		at = at.Add(2 * time.Second)
		snapshot, err = controller.Observe(congestion.ActualState{
			At: at, Epoch: epoch, RTT: 40 * time.Millisecond,
			OuterWireBytes: outerBytes,
		})
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.AwaitingInstalled {
			if err := controller.ObserveInstalledIngress(congestion.InstalledIngressState{
				At: at.Add(100 * time.Millisecond), Epoch: epoch,
				RateBytesPerSecond: snapshot.Target.IngressRateBytesPerSecond,
				Fresh:              true,
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if snapshot.Target.OuterRateBytesPerSecond != limit {
		t.Fatalf("clean loaded target = %g, want explicit limit %g",
			snapshot.Target.OuterRateBytesPerSecond, limit)
	}
}

func TestControllerIgnoresUnloadedOuterInnerExpansion(t *testing.T) {
	controller, err := congestion.New(1_000_000, 0)
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
		math.Abs(unloaded.Target.IngressRateBytesPerSecond-646_000) > 0.001 {
		t.Fatalf("unloaded sample changed expansion/ingress target: ratio=%g ingress=%g",
			unloaded.OverheadRatio, unloaded.Target.IngressRateBytesPerSecond)
	}
}

func TestControllerIngressPressureBacksOffAndRecoversAfterSettledCleanRun(t *testing.T) {
	controller, err := congestion.New(1_000_000, 0)
	if err != nil {
		t.Fatal(err)
	}
	epoch := congestion.CarrierEpoch{PathID: 3, Generation: 9}
	at := time.Unix(400, 0)
	initial, err := controller.Observe(congestion.ActualState{
		At: at, Epoch: epoch, RTT: 40 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(initial.IngressServiceHeadroom-0.95) > 0.0001 {
		t.Fatalf(
			"initial ingress service headroom = %g, want 0.95",
			initial.IngressServiceHeadroom,
		)
	}
	if err := controller.ObserveInstalledIngress(congestion.InstalledIngressState{
		At:                 at.Add(100 * time.Millisecond),
		Epoch:              epoch,
		RateBytesPerSecond: initial.Target.IngressRateBytesPerSecond,
		Fresh:              true,
	}); err != nil {
		t.Fatal(err)
	}

	saturated, err := controller.ObserveIngressPressure(
		congestion.IngressPressureState{
			At:                    at.Add(2 * time.Second),
			Epoch:                 epoch,
			AdmissionWaitDuration: time.Second,
			Interval:              time.Second,
			RingPending:           true,
			Loaded:                true,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if saturated.Target.IngressRateBytesPerSecond >=
		initial.Target.IngressRateBytesPerSecond {
		t.Fatalf(
			"cycle-18 pressure left ingress at %g, want below %g",
			saturated.Target.IngressRateBytesPerSecond,
			initial.Target.IngressRateBytesPerSecond,
		)
	}
	if saturated.Target.OuterRateBytesPerSecond !=
		initial.Target.OuterRateBytesPerSecond {
		t.Fatalf(
			"ingress pressure changed outer target from %g to %g",
			initial.Target.OuterRateBytesPerSecond,
			saturated.Target.OuterRateBytesPerSecond,
		)
	}
	if !saturated.IngressPressure ||
		math.Abs(saturated.IngressPressureRatio-1) > 0.0001 ||
		!saturated.AwaitingInstalled {
		t.Fatalf("saturated ingress state = %+v", saturated)
	}

	pending, err := controller.ObserveIngressPressure(
		congestion.IngressPressureState{
			At:                    at.Add(3 * time.Second),
			Epoch:                 epoch,
			AdmissionWaitDuration: time.Second,
			Interval:              time.Second,
			RingPending:           true,
			Loaded:                true,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if pending.Target != saturated.Target ||
		pending.IngressHeadroomChanges != saturated.IngressHeadroomChanges {
		t.Fatalf(
			"pressure replayed before readback: pending=%+v saturated=%+v",
			pending,
			saturated,
		)
	}
	if _, err := controller.ObserveIngressPressure(
		congestion.IngressPressureState{
			At:                    at.Add(3 * time.Second),
			Epoch:                 epoch,
			AdmissionWaitDuration: time.Second,
			Interval:              time.Second,
			Loaded:                true,
		},
	); err == nil {
		t.Fatal("stale ingress-pressure interval was replayed")
	}
	if err := controller.ObserveInstalledIngress(congestion.InstalledIngressState{
		At:                 at.Add(3100 * time.Millisecond),
		Epoch:              epoch,
		RateBytesPerSecond: saturated.Target.IngressRateBytesPerSecond,
		Fresh:              true,
	}); err != nil {
		t.Fatal(err)
	}
	settled, err := controller.Observe(congestion.ActualState{
		At:    at.Add(4200 * time.Millisecond),
		Epoch: epoch,
		RTT:   40 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if settled.AwaitingInstalled {
		t.Fatalf("exact ingress readback remained unsettled: %+v", settled)
	}

	unloaded, err := controller.ObserveIngressPressure(
		congestion.IngressPressureState{
			At:       at.Add(4400 * time.Millisecond),
			Epoch:    epoch,
			Interval: 200 * time.Millisecond,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if unloaded.Target != saturated.Target {
		t.Fatalf("idle interval recovered ingress headroom: %+v", unloaded)
	}

	for step := 0; step < 2; step++ {
		clean, err := controller.ObserveIngressPressure(
			congestion.IngressPressureState{
				At:       at.Add(time.Duration(4600+step*200) * time.Millisecond),
				Epoch:    epoch,
				Interval: 200 * time.Millisecond,
				Loaded:   true,
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		if clean.Target != saturated.Target {
			t.Fatalf(
				"headroom recovered after only %d clean intervals: %+v",
				step+1,
				clean,
			)
		}
	}
	recovered, err := controller.ObserveIngressPressure(
		congestion.IngressPressureState{
			At:       at.Add(5 * time.Second),
			Epoch:    epoch,
			Interval: 200 * time.Millisecond,
			Loaded:   true,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Target.IngressRateBytesPerSecond <=
		saturated.Target.IngressRateBytesPerSecond {
		t.Fatalf(
			"three clean settled intervals left ingress at %g, want above %g",
			recovered.Target.IngressRateBytesPerSecond,
			saturated.Target.IngressRateBytesPerSecond,
		)
	}

	nextEpoch := congestion.CarrierEpoch{PathID: 4, Generation: 10}
	reset, err := controller.Observe(congestion.ActualState{
		At: at.Add(5200 * time.Millisecond), Epoch: nextEpoch, RTT: 45 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(reset.IngressServiceHeadroom-0.95) > 0.0001 ||
		reset.IngressHeadroomChanges != 0 {
		t.Fatalf("carrier reset ingress state = %+v", reset)
	}
}

func TestControllerLoadedRingPendingWithoutWaitDoesNotReduceIngressHeadroom(t *testing.T) {
	controller, err := congestion.New(1_000_000, 0)
	if err != nil {
		t.Fatal(err)
	}
	epoch := congestion.CarrierEpoch{PathID: 3, Generation: 9}
	at := time.Unix(450, 0)
	initial, err := controller.Observe(congestion.ActualState{
		At: at, Epoch: epoch, RTT: 40 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.ObserveInstalledIngress(congestion.InstalledIngressState{
		At:                 at.Add(100 * time.Millisecond),
		Epoch:              epoch,
		RateBytesPerSecond: initial.Target.IngressRateBytesPerSecond,
		Fresh:              true,
	}); err != nil {
		t.Fatal(err)
	}

	ringOnly, err := controller.ObserveIngressPressure(
		congestion.IngressPressureState{
			At:          at.Add(2 * time.Second),
			Epoch:       epoch,
			Interval:    time.Second,
			RingPending: true,
			Loaded:      true,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if ringOnly.IngressPressure ||
		ringOnly.Target != initial.Target ||
		ringOnly.IngressHeadroomChanges != 0 {
		t.Fatalf(
			"loaded ring occupancy without admission wait changed ingress control state: initial=%+v ringOnly=%+v",
			initial,
			ringOnly,
		)
	}
	subthreshold, err := controller.ObserveIngressPressure(
		congestion.IngressPressureState{
			At:                    at.Add(3 * time.Second),
			Epoch:                 epoch,
			AdmissionWaitDuration: 400 * time.Millisecond,
			Interval:              time.Second,
			RingPending:           true,
			Loaded:                true,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if subthreshold.IngressPressure ||
		subthreshold.Target != initial.Target ||
		subthreshold.IngressHeadroomChanges != 0 {
		t.Fatalf(
			"loaded ring occupancy with sub-threshold admission wait changed ingress control state: initial=%+v subthreshold=%+v",
			initial,
			subthreshold,
		)
	}
}

func TestControllerLoadedPressurePreemptsUnrelatedOuterRetargetSettlement(t *testing.T) {
	controller, err := congestion.New(1_000_000, 0)
	if err != nil {
		t.Fatal(err)
	}
	epoch := congestion.CarrierEpoch{PathID: 3, Generation: 9}
	at := time.Unix(500, 0)
	initial, err := controller.Observe(congestion.ActualState{
		At: at, Epoch: epoch, RTT: 40 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.ObserveInstalledIngress(congestion.InstalledIngressState{
		At:                 at.Add(100 * time.Millisecond),
		Epoch:              epoch,
		RateBytesPerSecond: initial.Target.IngressRateBytesPerSecond,
		Fresh:              true,
	}); err != nil {
		t.Fatal(err)
	}
	retargeted, err := controller.Observe(congestion.ActualState{
		At:             at.Add(2 * time.Second),
		Epoch:          epoch,
		OuterWireBytes: 2_000_000,
		RTT:            40 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !retargeted.AwaitingInstalled ||
		retargeted.Target.OuterRateBytesPerSecond <=
			initial.Target.OuterRateBytesPerSecond {
		t.Fatalf("loaded outer sample did not enter retarget settlement: %+v", retargeted)
	}

	pressured, err := controller.ObserveIngressPressure(
		congestion.IngressPressureState{
			At:                    at.Add(2100 * time.Millisecond),
			Epoch:                 epoch,
			AdmissionWaitDuration: 100 * time.Millisecond,
			Interval:              100 * time.Millisecond,
			RingPending:           true,
			Loaded:                true,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if pressured.IngressServiceHeadroom >=
		retargeted.IngressServiceHeadroom ||
		pressured.IngressHeadroomChanges !=
			retargeted.IngressHeadroomChanges+1 {
		t.Fatalf(
			"loaded local pressure was starved by outer retarget settlement: retargeted=%+v pressured=%+v",
			retargeted,
			pressured,
		)
	}
	repeated, err := controller.ObserveIngressPressure(
		congestion.IngressPressureState{
			At:                    at.Add(2200 * time.Millisecond),
			Epoch:                 epoch,
			AdmissionWaitDuration: 100 * time.Millisecond,
			Interval:              100 * time.Millisecond,
			RingPending:           true,
			Loaded:                true,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if repeated.IngressServiceHeadroom !=
		pressured.IngressServiceHeadroom ||
		repeated.IngressHeadroomChanges !=
			pressured.IngressHeadroomChanges {
		t.Fatalf("pressure repeated before exact ingress readback: %+v", repeated)
	}
	if err := controller.ObserveInstalledIngress(congestion.InstalledIngressState{
		At:                 at.Add(2250 * time.Millisecond),
		Epoch:              epoch,
		RateBytesPerSecond: pressured.Target.IngressRateBytesPerSecond,
		Fresh:              true,
	}); err != nil {
		t.Fatal(err)
	}
	afterReadback, err := controller.ObserveIngressPressure(
		congestion.IngressPressureState{
			At:                    at.Add(2300 * time.Millisecond),
			Epoch:                 epoch,
			AdmissionWaitDuration: 100 * time.Millisecond,
			Interval:              100 * time.Millisecond,
			RingPending:           true,
			Loaded:                true,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if afterReadback.IngressServiceHeadroom !=
		pressured.IngressServiceHeadroom ||
		afterReadback.IngressHeadroomChanges !=
			pressured.IngressHeadroomChanges {
		t.Fatalf(
			"pressure repeated before its exact readback settled: pressured=%+v after=%+v",
			pressured,
			afterReadback,
		)
	}
	if err := controller.ObserveInstalledIngress(congestion.InstalledIngressState{
		At:                 at.Add(3 * time.Second),
		Epoch:              epoch,
		RateBytesPerSecond: pressured.Target.IngressRateBytesPerSecond,
		Fresh:              true,
	}); err != nil {
		t.Fatal(err)
	}
	afterSettlement, err := controller.ObserveIngressPressure(
		congestion.IngressPressureState{
			At:                    at.Add(3250 * time.Millisecond),
			Epoch:                 epoch,
			AdmissionWaitDuration: 100 * time.Millisecond,
			Interval:              100 * time.Millisecond,
			RingPending:           true,
			Loaded:                true,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if afterSettlement.IngressServiceHeadroom >=
		pressured.IngressServiceHeadroom ||
		afterSettlement.IngressHeadroomChanges !=
			pressured.IngressHeadroomChanges+1 {
		t.Fatalf(
			"settled exact ingress readback did not release pressure gate: pressured=%+v after=%+v",
			pressured,
			afterSettlement,
		)
	}
}

// Behavioral-Active Blackbox-Atomic. Regression: D130 cycle 25.
func TestControllerCycle25NoLossVariableRTTDoesNotCollapse(t *testing.T) {
	const (
		seed  = 1_000_000.0
		limit = 1_000_000.0
	)
	controller, err := congestion.New(seed, limit)
	if err != nil {
		t.Fatal(err)
	}
	epoch := congestion.CarrierEpoch{PathID: 1, Generation: 1}
	at := time.Unix(600, 0)
	const baseRTT = 35_422_288 * time.Nanosecond
	snapshot, err := controller.Observe(congestion.ActualState{
		At: at, Epoch: epoch, RTT: baseRTT,
		LossFresh: true, FeedbackEverSeen: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	initialTarget := snapshot.Target.OuterRateBytesPerSecond
	queueDelay := []time.Duration{
		56_091 * time.Microsecond,
		58_344 * time.Microsecond,
		52_702 * time.Microsecond,
		57_018 * time.Microsecond,
		64_860 * time.Microsecond,
		55_465 * time.Microsecond,
		46_732 * time.Microsecond,
		47_781 * time.Microsecond,
		29_220 * time.Microsecond,
		47_150 * time.Microsecond,
		61_981 * time.Microsecond,
		51_619 * time.Microsecond,
		53_326 * time.Microsecond,
		35_173 * time.Microsecond,
		29_334 * time.Microsecond,
		22_801 * time.Microsecond,
		47_953 * time.Microsecond,
		47_911 * time.Microsecond,
		33_630 * time.Microsecond,
		30_736 * time.Microsecond,
		18_492 * time.Microsecond,
		12_731 * time.Microsecond,
		21_504 * time.Microsecond,
		21_665 * time.Microsecond,
		28_129 * time.Microsecond,
		51_253 * time.Microsecond,
		58_205 * time.Microsecond,
		64_363 * time.Microsecond,
		58_332 * time.Microsecond,
		41_943 * time.Microsecond,
		24_458 * time.Microsecond,
		16_120 * time.Microsecond,
	}
	var outerBytes, innerBytes uint64
	for index, delay := range queueDelay {
		target := snapshot.Target.OuterRateBytesPerSecond
		outerBytes += uint64(target * 2)
		innerBytes += uint64(target * 2 / 1.25)
		at = at.Add(2 * time.Second)
		snapshot, err = controller.Observe(congestion.ActualState{
			At: at, Epoch: epoch,
			OuterWireBytes: outerBytes, InnerDataBytes: innerBytes,
			RTT:          baseRTT + delay,
			RTTVariation: 13_517 * time.Microsecond,
			LossFresh:    true, FeedbackEverSeen: true,
		})
		if err != nil {
			t.Fatalf("trace sample %d: %v", index, err)
		}
		if snapshot.Target.OuterRateBytesPerSecond < target {
			t.Fatalf(
				"cycle-25 trace sample %d decreased target from %g to %g",
				index,
				target,
				snapshot.Target.OuterRateBytesPerSecond,
			)
		}
		if snapshot.AwaitingInstalled {
			if err := controller.ObserveInstalledIngress(congestion.InstalledIngressState{
				At: at.Add(100 * time.Millisecond), Epoch: epoch,
				RateBytesPerSecond: snapshot.Target.IngressRateBytesPerSecond,
				Fresh:              true,
			}); err != nil {
				t.Fatalf("trace sample %d installed readback: %v", index, err)
			}
		}
	}
	if snapshot.Target.OuterRateBytesPerSecond < initialTarget {
		t.Fatalf(
			"cycle-25 zero-loss variable-RTT target = %g, want no decrease below initial %g",
			snapshot.Target.OuterRateBytesPerSecond,
			initialTarget,
		)
	}
}

// Behavioral-Active Blackbox-Atomic. Regression: D130 cycle 27.
func TestControllerCycle27IsolatedQueueCrossingDoesNotDecrease(t *testing.T) {
	const seed = 1_275_000.0
	controller, err := congestion.New(seed, 0)
	if err != nil {
		t.Fatal(err)
	}
	epoch := congestion.CarrierEpoch{PathID: 1, Generation: 1}
	at := time.Unix(650, 0)
	const baseRTT = 30_284 * time.Microsecond
	snapshot, err := controller.Observe(congestion.ActualState{
		At: at, Epoch: epoch, RTT: baseRTT,
		FeedbackEverSeen: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	initialTarget := snapshot.Target.OuterRateBytesPerSecond
	samples := []struct {
		queueDelay   time.Duration
		rttVariation time.Duration
	}{
		{18_505 * time.Microsecond, 8_202 * time.Microsecond},
		{19_961 * time.Microsecond, 5_088 * time.Microsecond},
		{13_940 * time.Microsecond, 5_290 * time.Microsecond},
		{12_352 * time.Microsecond, 4_716 * time.Microsecond},
		{14_332 * time.Microsecond, 4_061 * time.Microsecond},
		{20_374 * time.Microsecond, 4_061 * time.Microsecond},
		{25_630 * time.Microsecond, 4_061 * time.Microsecond},
		{33_506 * time.Microsecond, 4_061 * time.Microsecond},
		{28_121 * time.Microsecond, 4_061 * time.Microsecond},
	}
	var outerBytes, innerBytes uint64
	for index, sample := range samples {
		outerBytes += uint64(initialTarget)
		innerBytes += uint64(initialTarget / 1.25)
		at = at.Add(time.Second)
		snapshot, err = controller.Observe(congestion.ActualState{
			At: at, Epoch: epoch,
			OuterWireBytes: outerBytes, InnerDataBytes: innerBytes,
			RTT:              baseRTT + sample.queueDelay,
			RTTVariation:     sample.rttVariation,
			FeedbackEverSeen: true,
		})
		if err != nil {
			t.Fatalf("trace sample %d: %v", index, err)
		}
		if snapshot.Target.OuterRateBytesPerSecond != initialTarget {
			t.Fatalf(
				"cycle-27 trace sample %d changed target from %g to %g",
				index,
				initialTarget,
				snapshot.Target.OuterRateBytesPerSecond,
			)
		}
	}
}

// Behavioral-Active Blackbox-Atomic. D130 delay qualification uses elapsed time.
func TestControllerDelayCongestionRequiresContinuousElapsedDwell(t *testing.T) {
	controller, err := congestion.New(1_000_000, 0)
	if err != nil {
		t.Fatal(err)
	}
	epoch := congestion.CarrierEpoch{PathID: 1, Generation: 1}
	at := time.Unix(675, 0)
	const baseRTT = 40 * time.Millisecond
	initial, err := controller.Observe(congestion.ActualState{
		At: at, Epoch: epoch, RTT: baseRTT,
		FeedbackEverSeen: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	target := initial.Target.OuterRateBytesPerSecond
	var outerBytes, innerBytes uint64
	observe := func(offset time.Duration, queueDelay time.Duration) congestion.Snapshot {
		t.Helper()
		elapsed := offset - at.Sub(time.Unix(675, 0))
		outerBytes += uint64(target * elapsed.Seconds())
		innerBytes += uint64(target * elapsed.Seconds() / 1.25)
		at = time.Unix(675, 0).Add(offset)
		snapshot, observeErr := controller.Observe(congestion.ActualState{
			At: at, Epoch: epoch,
			OuterWireBytes: outerBytes, InnerDataBytes: innerBytes,
			RTT: baseRTT + queueDelay, RTTVariation: time.Millisecond,
			FeedbackEverSeen: true,
		})
		if observeErr != nil {
			t.Fatal(observeErr)
		}
		return snapshot
	}

	for _, sample := range []struct {
		offset     time.Duration
		queueDelay time.Duration
	}{
		{100 * time.Millisecond, 30 * time.Millisecond},
		{900 * time.Millisecond, 10 * time.Millisecond},
		{time.Second, 30 * time.Millisecond},
		{1900 * time.Millisecond, 30 * time.Millisecond},
	} {
		if snapshot := observe(sample.offset, sample.queueDelay); snapshot.Target.OuterRateBytesPerSecond != target {
			t.Fatalf("target changed before continuous one-second dwell at %s: %g",
				sample.offset, snapshot.Target.OuterRateBytesPerSecond)
		}
	}
	decreased := observe(2*time.Second, 30*time.Millisecond)
	want := target * 0.85
	if math.Abs(decreased.Target.OuterRateBytesPerSecond-want) > 0.001 {
		t.Fatalf("one-second continuous queue target = %g, want %g",
			decreased.Target.OuterRateBytesPerSecond, want)
	}
}

// Behavioral-Active Blackbox-Atomic. D130 settlement cannot earn delay dwell.
func TestControllerDelayDwellStartsAfterRetargetSettlement(t *testing.T) {
	controller, err := congestion.New(1_000_000, 0)
	if err != nil {
		t.Fatal(err)
	}
	epoch := congestion.CarrierEpoch{PathID: 1, Generation: 1}
	origin := time.Unix(690, 0)
	at := origin
	const baseRTT = 40 * time.Millisecond
	snapshot, err := controller.Observe(congestion.ActualState{
		At: at, Epoch: epoch, RTT: baseRTT,
	})
	if err != nil {
		t.Fatal(err)
	}
	var outerBytes, innerBytes uint64
	observe := func(offset time.Duration, queueDelay time.Duration) congestion.Snapshot {
		t.Helper()
		nextAt := origin.Add(offset)
		elapsed := nextAt.Sub(at)
		outerBytes += uint64(snapshot.Target.OuterRateBytesPerSecond * elapsed.Seconds())
		innerBytes += uint64(snapshot.Target.OuterRateBytesPerSecond * elapsed.Seconds() / 1.25)
		at = nextAt
		var observeErr error
		snapshot, observeErr = controller.Observe(congestion.ActualState{
			At: at, Epoch: epoch,
			OuterWireBytes: outerBytes, InnerDataBytes: innerBytes,
			RTT: baseRTT + queueDelay, RTTVariation: time.Millisecond,
		})
		if observeErr != nil {
			t.Fatal(observeErr)
		}
		return snapshot
	}

	increased := observe(time.Second, 0)
	if !increased.AwaitingInstalled {
		t.Fatalf("clean loaded sample did not retarget: %+v", increased)
	}
	target := increased.Target.OuterRateBytesPerSecond
	duringWait := observe(2*time.Second, 30*time.Millisecond)
	if duringWait.Target.OuterRateBytesPerSecond != target {
		t.Fatalf("queue sample changed unsettled target from %g to %g",
			target, duringWait.Target.OuterRateBytesPerSecond)
	}
	if err := controller.ObserveInstalledIngress(congestion.InstalledIngressState{
		At:                 origin.Add(2100 * time.Millisecond),
		Epoch:              epoch,
		RateBytesPerSecond: duringWait.Target.IngressRateBytesPerSecond,
		Fresh:              true,
	}); err != nil {
		t.Fatal(err)
	}
	for _, offset := range []time.Duration{
		3200 * time.Millisecond,
		4100 * time.Millisecond,
	} {
		if observed := observe(offset, 30*time.Millisecond); observed.Target.OuterRateBytesPerSecond != target {
			t.Fatalf("target changed before post-settlement dwell at %s: %g",
				offset, observed.Target.OuterRateBytesPerSecond)
		}
	}
	decreased := observe(4200*time.Millisecond, 30*time.Millisecond)
	want := target * 0.85
	if math.Abs(decreased.Target.OuterRateBytesPerSecond-want) > 0.001 {
		t.Fatalf("post-settlement dwell target = %g, want %g",
			decreased.Target.OuterRateBytesPerSecond, want)
	}
}

// Behavioral-Active Blackbox-Atomic. D130 discontinuities reset armed delay dwell.
func TestControllerDelayDwellResetsAcrossDiscontinuities(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		interrupt  congestion.ActualState
		finalEpoch congestion.CarrierEpoch
	}{
		{
			name: "unloaded",
			interrupt: congestion.ActualState{
				OuterWireBytes: 850_000,
				InnerDataBytes: 680_000,
				RTT:            70 * time.Millisecond,
			},
			finalEpoch: congestion.CarrierEpoch{PathID: 1, Generation: 1},
		},
		{
			name: "counter-regression",
			interrupt: congestion.ActualState{
				RTT: 70 * time.Millisecond,
			},
			finalEpoch: congestion.CarrierEpoch{PathID: 1, Generation: 1},
		},
		{
			name: "carrier-epoch",
			interrupt: congestion.ActualState{
				Epoch: congestion.CarrierEpoch{PathID: 2, Generation: 2},
				RTT:   40 * time.Millisecond,
			},
			finalEpoch: congestion.CarrierEpoch{PathID: 2, Generation: 2},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			controller, err := congestion.New(1_000_000, 0)
			if err != nil {
				t.Fatal(err)
			}
			firstEpoch := congestion.CarrierEpoch{PathID: 1, Generation: 1}
			at := time.Unix(695, 0)
			initial, err := controller.Observe(congestion.ActualState{
				At: at, Epoch: firstEpoch, RTT: 40 * time.Millisecond,
				FeedbackEverSeen: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			target := initial.Target.OuterRateBytesPerSecond
			armed, err := controller.Observe(congestion.ActualState{
				At: at.Add(time.Second), Epoch: firstEpoch,
				OuterWireBytes: uint64(target), InnerDataBytes: uint64(target / 1.25),
				RTT: 70 * time.Millisecond, RTTVariation: time.Millisecond,
				FeedbackEverSeen: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			if armed.Target.OuterRateBytesPerSecond != target {
				t.Fatalf("first delay crossing changed target to %g, want %g",
					armed.Target.OuterRateBytesPerSecond, target)
			}

			interrupt := testCase.interrupt
			interrupt.At = at.Add(1500 * time.Millisecond)
			if interrupt.Epoch == (congestion.CarrierEpoch{}) {
				interrupt.Epoch = firstEpoch
			}
			interrupt.RTTVariation = time.Millisecond
			interrupt.FeedbackEverSeen = true
			if _, err := controller.Observe(interrupt); err != nil {
				t.Fatal(err)
			}

			finalOuter := interrupt.OuterWireBytes + uint64(target*0.5)
			finalInner := interrupt.InnerDataBytes + uint64(target*0.5/1.25)
			afterReset, err := controller.Observe(congestion.ActualState{
				At: at.Add(2 * time.Second), Epoch: testCase.finalEpoch,
				OuterWireBytes: finalOuter, InnerDataBytes: finalInner,
				RTT: 70 * time.Millisecond, RTTVariation: time.Millisecond,
				FeedbackEverSeen: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			if afterReset.Target.OuterRateBytesPerSecond != target {
				t.Fatalf("first post-%s delay crossing changed target to %g, want %g",
					testCase.name, afterReset.Target.OuterRateBytesPerSecond, target)
			}
		})
	}
}

// Behavioral-Active Blackbox-Atomic. Regression: D130 emitted bytes are not delivery.
func TestControllerCongestedDemandLimitedIntervalUsesMultiplicativeDecrease(t *testing.T) {
	controller, err := congestion.New(1_000_000, 0)
	if err != nil {
		t.Fatal(err)
	}
	epoch := congestion.CarrierEpoch{PathID: 1, Generation: 1}
	at := time.Unix(700, 0)
	initial, err := controller.Observe(congestion.ActualState{
		At: at, Epoch: epoch, RTT: 40 * time.Millisecond,
		LossFresh: true, FeedbackEverSeen: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	offered := initial.Target.OuterRateBytesPerSecond * 0.51
	congested, err := controller.Observe(congestion.ActualState{
		At: at.Add(time.Second), Epoch: epoch,
		OuterWireBytes: uint64(offered), InnerDataBytes: uint64(offered / 1.25),
		RTT: 80 * time.Millisecond, RTTVariation: time.Millisecond,
		AuthenticatedLoss: 0.01, LossFresh: true, FeedbackEverSeen: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := initial.Target.OuterRateBytesPerSecond * 0.85
	if math.Abs(congested.Target.OuterRateBytesPerSecond-want) > 0.001 {
		t.Fatalf(
			"51%%-loaded congested target = %g, want one 0.85 decrease to %g",
			congested.Target.OuterRateBytesPerSecond,
			want,
		)
	}
}

// Behavioral-Active Blackbox-Atomic. Regression: D130 must retain a queue response.
func TestControllerSustainedQueueExceedsVariationMarginWithinTwentyFiveProbes(t *testing.T) {
	controller, err := congestion.New(1_000_000, 1_000_000)
	if err != nil {
		t.Fatal(err)
	}
	epoch := congestion.CarrierEpoch{PathID: 1, Generation: 1}
	at := time.Unix(800, 0)
	const baseRTT = 35 * time.Millisecond
	snapshot, err := controller.Observe(congestion.ActualState{
		At: at, Epoch: epoch, RTT: baseRTT, RTTVariation: 5 * time.Millisecond,
		LossFresh: true, FeedbackEverSeen: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	srtt := float64(baseRTT)
	rttVariation := float64(5 * time.Millisecond)
	const elevatedRTT = float64(335 * time.Millisecond)
	var outerBytes, innerBytes uint64
	var decreasedAt int
	for probe := 1; probe <= 25; probe++ {
		deviation := math.Abs(srtt - elevatedRTT)
		rttVariation = 0.75*rttVariation + 0.25*deviation
		srtt = 0.875*srtt + 0.125*elevatedRTT
		targetBefore := snapshot.Target.OuterRateBytesPerSecond
		outerBytes += uint64(targetBefore * 0.2)
		innerBytes += uint64(targetBefore * 0.2 / 1.25)
		at = at.Add(200 * time.Millisecond)
		snapshot, err = controller.Observe(congestion.ActualState{
			At: at, Epoch: epoch,
			OuterWireBytes: outerBytes, InnerDataBytes: innerBytes,
			RTT: time.Duration(srtt), RTTVariation: time.Duration(rttVariation),
			LossFresh: true, FeedbackEverSeen: true,
		})
		if err != nil {
			t.Fatalf("probe %d: %v", probe, err)
		}
		if snapshot.Target.OuterRateBytesPerSecond < targetBefore {
			decreasedAt = probe
			break
		}
		if snapshot.AwaitingInstalled {
			if err := controller.ObserveInstalledIngress(congestion.InstalledIngressState{
				At: at.Add(time.Millisecond), Epoch: epoch,
				RateBytesPerSecond: snapshot.Target.IngressRateBytesPerSecond,
				Fresh:              true,
			}); err != nil {
				t.Fatalf("probe %d installed readback: %v", probe, err)
			}
		}
	}
	if decreasedAt == 0 {
		t.Fatalf("stable 300ms queue produced no decrease within 25 probes: %+v", snapshot)
	}
}

// Behavioral-Active Blackbox-Atomic. Regression: D130 loss bypasses RTT noise qualification.
func TestControllerFreshLossDecreasesImmediatelyDespiteHighRTTVariation(t *testing.T) {
	const authenticatedLoss = 0.01
	controller, err := congestion.New(1_000_000, 0)
	if err != nil {
		t.Fatal(err)
	}
	epoch := congestion.CarrierEpoch{PathID: 1, Generation: 1}
	at := time.Unix(900, 0)
	initial, err := controller.Observe(congestion.ActualState{
		At: at, Epoch: epoch, RTT: 40 * time.Millisecond,
		RTTVariation: 100 * time.Millisecond,
		LossFresh:    true, FeedbackEverSeen: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	armed, err := controller.Observe(congestion.ActualState{
		At: at.Add(500 * time.Millisecond), Epoch: epoch,
		OuterWireBytes: uint64(initial.Target.OuterRateBytesPerSecond * 0.5),
		InnerDataBytes: uint64(initial.Target.OuterRateBytesPerSecond * 0.5 / 1.25),
		RTT:            500 * time.Millisecond,
		RTTVariation:   100 * time.Millisecond,
		LossFresh:      true, FeedbackEverSeen: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if armed.Target != initial.Target {
		t.Fatalf("first high-variation delay crossing changed target: got %+v want %+v",
			armed.Target, initial.Target)
	}
	lost, err := controller.Observe(congestion.ActualState{
		At: at.Add(time.Second), Epoch: epoch,
		OuterWireBytes:    uint64(initial.Target.OuterRateBytesPerSecond),
		InnerDataBytes:    uint64(initial.Target.OuterRateBytesPerSecond / 1.25),
		RTT:               40 * time.Millisecond,
		RTTVariation:      100 * time.Millisecond,
		AuthenticatedLoss: authenticatedLoss,
		LossFresh:         true,
		FeedbackEverSeen:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := initial.Target.OuterRateBytesPerSecond * 0.85
	if math.Abs(lost.Target.OuterRateBytesPerSecond-want) > 0.001 {
		t.Fatalf("fresh-loss target = %g, want immediate decrease to %g",
			lost.Target.OuterRateBytesPerSecond, want)
	}
}

// Behavioral-Active Blackbox-Atomic. Regression: D130 duration arithmetic must not wrap.
func TestControllerMaximumRTTVariationCannotBecomeCongestion(t *testing.T) {
	controller, err := congestion.New(1_000_000, 0)
	if err != nil {
		t.Fatal(err)
	}
	epoch := congestion.CarrierEpoch{PathID: 1, Generation: 1}
	at := time.Unix(1000, 0)
	initial, err := controller.Observe(congestion.ActualState{
		At: at, Epoch: epoch, RTT: 40 * time.Millisecond,
		LossFresh: true, FeedbackEverSeen: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	observed, err := controller.Observe(congestion.ActualState{
		At: at.Add(time.Second), Epoch: epoch,
		OuterWireBytes: uint64(initial.Target.OuterRateBytesPerSecond),
		InnerDataBytes: uint64(initial.Target.OuterRateBytesPerSecond / 1.25),
		RTT:            80 * time.Millisecond,
		RTTVariation:   time.Duration(1<<63 - 1),
		LossFresh:      true, FeedbackEverSeen: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if observed.Target.OuterRateBytesPerSecond < initial.Target.OuterRateBytesPerSecond {
		t.Fatalf(
			"maximum RTT variation decreased target from %g to %g",
			initial.Target.OuterRateBytesPerSecond,
			observed.Target.OuterRateBytesPerSecond,
		)
	}
}
