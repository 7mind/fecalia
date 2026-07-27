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
		RTT: 80 * time.Millisecond, LossFresh: true, FeedbackEverSeen: true,
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
		RTT: 80 * time.Millisecond, LossFresh: true, FeedbackEverSeen: true,
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
		RTT: 80 * time.Millisecond, LossFresh: true, FeedbackEverSeen: true,
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
		RTT: 80 * time.Millisecond, LossFresh: true, FeedbackEverSeen: true,
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
		RTT: 100 * time.Millisecond, LossFresh: true, FeedbackEverSeen: true,
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

func TestControllerUnloadedRingPendingDoesNotReduceIngressHeadroom(t *testing.T) {
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

	unloaded, err := controller.ObserveIngressPressure(
		congestion.IngressPressureState{
			At:          at.Add(2 * time.Second),
			Epoch:       epoch,
			Interval:    time.Second,
			RingPending: true,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if unloaded.IngressPressure ||
		unloaded.Target != initial.Target ||
		unloaded.IngressHeadroomChanges != 0 {
		t.Fatalf(
			"unloaded ring occupancy changed ingress control state: initial=%+v unloaded=%+v",
			initial,
			unloaded,
		)
	}
}
