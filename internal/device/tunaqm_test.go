package device

import (
	"errors"
	"fmt"
	"math"
	"testing"
	"time"

	awgdevice "github.com/amnezia-vpn/amneziawg-go/device"
)

const tunAQMTxQueueLen = 32

type memoryTUNAQMKernel struct {
	actual   tunAQMActualState
	applyErr error
}

type recordingTUNAQMKernel struct {
	memoryTUNAQMKernel
	events []string
}

func (k *recordingTUNAQMKernel) Apply(target tunAQMTargetState) (tunAQMApplyResult, error) {
	k.events = append(k.events, fmt.Sprintf("capacity:%d", target.AdmissionLimitBytes))
	return k.memoryTUNAQMKernel.Apply(target)
}

func (k *memoryTUNAQMKernel) Apply(target tunAQMTargetState) (tunAQMApplyResult, error) {
	if k.applyErr != nil {
		return tunAQMApplyResult{}, k.applyErr
	}
	k.actual = tunAQMActualState{
		RateBytesPerSecond: target.RateBytesPerSecond,
		BurstBytes:         target.BurstBytes,
		TxQueueLen:         target.TxQueueLen,
		RootKind:           "htb",
		LeafKind:           "bfifo",
		LimitBytes:         target.QueueLimitBytes,
		GSOMaxSize:         target.GSOMaxSize,
		GSOMaxSegments:     target.GSOMaxSegments,
		ObservedAt:         time.Now(),
	}
	return tunAQMApplyResult{}, nil
}

func (k *memoryTUNAQMKernel) Read() (tunAQMActualState, error) {
	return k.actual, nil
}

type deferredTUNAQMKernel struct {
	actual tunAQMActualState
}

func (k *deferredTUNAQMKernel) Apply(target tunAQMTargetState) (tunAQMApplyResult, error) {
	k.actual.RateBytesPerSecond = target.RateBytesPerSecond
	k.actual.BurstBytes = target.BurstBytes
	k.actual.TxQueueLen = target.TxQueueLen
	k.actual.ObservedAt = time.Now()
	if target.QueueLimitBytes < k.actual.LimitBytes &&
		k.actual.BacklogBytes > target.QueueLimitBytes {
		return tunAQMApplyResult{QueueLimitDeferred: true}, nil
	}
	k.actual.LimitBytes = target.QueueLimitBytes
	k.actual.GSOMaxSize = target.GSOMaxSize
	k.actual.GSOMaxSegments = target.GSOMaxSegments
	return tunAQMApplyResult{}, nil
}

func (k *deferredTUNAQMKernel) Read() (tunAQMActualState, error) {
	return k.actual, nil
}

func testTUNAQMReconciliationContract(t testing.TB, kernel tunAQMKernel) {
	t.Helper()
	reconciler, err := newTUNAQMReconciler(kernel)
	if err != nil {
		t.Fatal(err)
	}
	target := tunAQMTargetState{
		Epoch: 4, RateBytesPerSecond: 680_000, TxQueueLen: tunAQMTxQueueLen,
		BurstBytes: 13_950, MTU: 1395, QueueLimitBytes: 65_000,
		GSOMaxSize: 13_950, GSOMaxSegments: 10, AdmissionLimitBytes: 14_270,
	}
	first, err := reconciler.Reconcile(target)
	if err != nil {
		t.Fatalf("initial reconciliation: %v", err)
	}
	if !first.Actual.Fresh ||
		first.Actual.Epoch != target.Epoch ||
		first.Actual.RateBytesPerSecond != target.RateBytesPerSecond ||
		first.Actual.TxQueueLen != target.TxQueueLen {
		t.Fatalf("first reconciliation = %+v", first)
	}

	drift := target
	drift.RateBytesPerSecond = 400_000
	drift.TxQueueLen = 128
	drift.MTU = 1200
	if _, err := kernel.Apply(drift); err != nil {
		t.Fatalf("inject drift: %v", err)
	}
	restored, err := reconciler.Reconcile(target)
	if err != nil {
		t.Fatalf("restore drift: %v", err)
	}
	if !restored.Actual.Fresh ||
		restored.Actual.RateBytesPerSecond != target.RateBytesPerSecond ||
		restored.Actual.TxQueueLen != target.TxQueueLen {
		t.Fatalf("reconciliation after drift = %+v", restored)
	}
}

func TestTUNAQMReconciliationContractDummy(t *testing.T) {
	testTUNAQMReconciliationContract(t, &memoryTUNAQMKernel{})
}

func TestTUNAQMUsesByteBoundedLeaf(t *testing.T) {
	kernel := &memoryTUNAQMKernel{}
	reconciler, err := newTUNAQMReconciler(kernel)
	if err != nil {
		t.Fatal(err)
	}
	target := tunAQMTargetState{
		Epoch: 5, RateBytesPerSecond: 680_000, TxQueueLen: tunAQMTxQueueLen,
		BurstBytes: 13_950, MTU: 1395, QueueLimitBytes: 65_000,
		GSOMaxSize: 13_950, GSOMaxSegments: 10, AdmissionLimitBytes: 14_270,
	}
	snapshot, err := reconciler.Reconcile(target)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Actual.LeafKind != "bfifo" {
		t.Fatalf("TUN leaf kind = %q, want byte-bounded bfifo", snapshot.Actual.LeafKind)
	}
}

func TestTUNAQMTransitionOrdersCapacityAndAdmissionByDirection(t *testing.T) {
	initial := tunAQMTargetState{
		Epoch: 5, RateBytesPerSecond: 680_000, TxQueueLen: 100,
		BurstBytes: 13_950, MTU: 1395, QueueLimitBytes: 80_000,
		GSOMaxSize: 13_950, GSOMaxSegments: 10, AdmissionLimitBytes: 20_000,
	}
	newTransition := func(t *testing.T, admissionApplied *bool) (
		*tunAQMTransition,
		*recordingTUNAQMKernel,
	) {
		t.Helper()
		kernel := &recordingTUNAQMKernel{}
		reconciler, err := newTUNAQMReconciler(kernel)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := reconciler.Reconcile(initial); err != nil {
			t.Fatal(err)
		}
		kernel.events = nil
		actualAdmissionLimit := initial.AdmissionLimitBytes
		transition, err := newTUNAQMTransition(
			reconciler,
			func(limit int) (bool, error) {
				kernel.events = append(kernel.events, fmt.Sprintf("admission:%d", limit))
				if *admissionApplied {
					actualAdmissionLimit = limit
				}
				return *admissionApplied, nil
			},
			func() int {
				return actualAdmissionLimit
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		if got := reconciler.Snapshot().ActualAdmissionLimitBytes; got != initial.AdmissionLimitBytes {
			t.Fatalf("initial actual admission limit = %d, want %d",
				got, initial.AdmissionLimitBytes)
		}
		return transition, kernel
	}

	t.Run("growth", func(t *testing.T) {
		admissionApplied := true
		transition, kernel := newTransition(t, &admissionApplied)
		target := initial
		target.AdmissionLimitBytes = 30_000
		target.TxQueueLen = 150
		target.QueueLimitBytes = 120_000
		snapshot, err := transition.Reconcile(target)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"capacity:30000", "admission:30000"}
		if fmt.Sprint(kernel.events) != fmt.Sprint(want) {
			t.Fatalf("growth operations = %v, want %v", kernel.events, want)
		}
		if snapshot.Target != target ||
			snapshot.Actual.TxQueueLen != target.TxQueueLen ||
			snapshot.Actual.LimitBytes != target.QueueLimitBytes ||
			!snapshot.Actual.Fresh ||
			snapshot.AdmissionDeferred ||
			snapshot.ActualAdmissionLimitBytes != target.AdmissionLimitBytes {
			t.Fatalf("growth convergence = %+v, transition = %+v", snapshot, transition)
		}
	})

	t.Run("deferred shrink", func(t *testing.T) {
		admissionApplied := false
		transition, kernel := newTransition(t, &admissionApplied)
		target := initial
		target.Epoch = 6
		target.RateBytesPerSecond = 500_000
		target.AdmissionLimitBytes = 10_000
		target.BurstBytes = 10_000
		target.TxQueueLen = 50
		target.QueueLimitBytes = 40_000
		target.GSOMaxSize = 10_000
		target.GSOMaxSegments = 7
		snapshot, err := transition.Reconcile(target)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"admission:10000", "capacity:20000"}
		if fmt.Sprint(kernel.events) != fmt.Sprint(want) {
			t.Fatalf("deferred-shrink operations = %v, want %v", kernel.events, want)
		}
		if snapshot.Target != target ||
			snapshot.Actual.RateBytesPerSecond != target.RateBytesPerSecond ||
			snapshot.Actual.Epoch != target.Epoch ||
			snapshot.Actual.BurstBytes != initial.BurstBytes ||
			snapshot.Actual.TxQueueLen != initial.TxQueueLen ||
			snapshot.Actual.LimitBytes != initial.QueueLimitBytes ||
			snapshot.Actual.GSOMaxSize != initial.GSOMaxSize ||
			snapshot.Actual.GSOMaxSegments != initial.GSOMaxSegments ||
			snapshot.ActualAdmissionLimitBytes != initial.AdmissionLimitBytes ||
			snapshot.Actual.Fresh ||
			!snapshot.RateFresh ||
			!snapshot.AdmissionDeferred {
			t.Fatalf("deferred-shrink snapshot = %+v, want desired rate with installed capacity", snapshot)
		}

		admissionApplied = true
		kernel.events = nil
		snapshot, err = transition.Reconcile(target)
		if err != nil {
			t.Fatal(err)
		}
		want = []string{"admission:10000", "capacity:10000"}
		if fmt.Sprint(kernel.events) != fmt.Sprint(want) {
			t.Fatalf("converged-shrink operations = %v, want %v", kernel.events, want)
		}
		if snapshot.Target != target ||
			snapshot.Actual.TxQueueLen != target.TxQueueLen ||
			snapshot.Actual.LimitBytes != target.QueueLimitBytes ||
			!snapshot.Actual.Fresh ||
			snapshot.AdmissionDeferred ||
			snapshot.ActualAdmissionLimitBytes != target.AdmissionLimitBytes {
			t.Fatalf("shrink convergence = %+v, transition = %+v", snapshot, transition)
		}
	})
}

func TestTUNAQMDeferredCapacityDoesNotBlockExactRateAcknowledgment(t *testing.T) {
	kernel := &deferredTUNAQMKernel{
		actual: tunAQMActualState{
			RateBytesPerSecond: 680_000,
			BurstBytes:         13_950,
			TxQueueLen:         tunAQMTxQueueLen,
			RootKind:           "htb",
			LeafKind:           "bfifo",
			LimitBytes:         65_000,
			GSOMaxSize:         13_950,
			GSOMaxSegments:     10,
			QueueLength:        61,
			BacklogBytes:       83_645,
			ObservedAt:         time.Now(),
		},
	}
	reconciler, err := newTUNAQMReconciler(kernel)
	if err != nil {
		t.Fatal(err)
	}
	target := tunAQMTargetState{
		Epoch: 5, RateBytesPerSecond: 600_000, TxQueueLen: tunAQMTxQueueLen,
		BurstBytes: 13_950, MTU: 1395, QueueLimitBytes: 60_000,
		GSOMaxSize: 13_950, GSOMaxSegments: 10, AdmissionLimitBytes: 14_270,
	}
	deferred, err := reconciler.Reconcile(target)
	if err != nil {
		t.Fatal(err)
	}
	if deferred.Target.QueueLimitBytes != 60_000 ||
		deferred.Actual.LimitBytes != 65_000 ||
		deferred.Actual.Fresh ||
		!deferred.RateFresh ||
		!deferred.QueueLimitDeferred {
		t.Fatalf("deferred reconciliation = %+v", deferred)
	}

	kernel.actual.QueueLength = 0
	kernel.actual.BacklogBytes = 0
	applied, err := reconciler.Reconcile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !applied.Actual.Fresh ||
		!applied.RateFresh ||
		applied.QueueLimitDeferred ||
		applied.Actual.LimitBytes != target.QueueLimitBytes {
		t.Fatalf("post-drain reconciliation = %+v", applied)
	}
}

func TestTUNAQMQueueGeometryCoversRingAndManagedLeafServiceWindows(t *testing.T) {
	tests := []struct {
		name          string
		rate          float64
		window        int
		peerCount     int
		gsoBytes      int
		wantRing      int
		wantLeafBytes int
	}{
		{name: "Pi BDP", rate: 680_000, window: 45_000, peerCount: 1, gsoBytes: 13_950, wantRing: 21, wantLeafBytes: 45_000},
		{name: "o3 synthetic burst", rate: 1_020_000, window: 60_000, peerCount: 1, gsoBytes: 19_530, wantRing: 30, wantLeafBytes: 60_000},
		{name: "two peers", rate: 1_360_000, window: 45_000, peerCount: 2, gsoBytes: 12_555, wantRing: 30, wantLeafBytes: 90_000},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := deriveTUNAQMQueueGeometry(
				test.rate, test.window, test.peerCount, test.gsoBytes,
				engineOutboundBatchDelayBudget, 1395, 1395,
			)
			if err != nil {
				t.Fatal(err)
			}
			if got.RingSlots != test.wantRing ||
				got.LeafLimitBytes != test.wantLeafBytes ||
				got.HTBBurstBytes != test.gsoBytes {
				t.Fatalf(
					"queue geometry = %+v, want ring/leaf-bytes/burst %d/%d/%d",
					got,
					test.wantRing,
					test.wantLeafBytes,
					test.gsoBytes,
				)
			}
		})
	}
}

func TestTUNAQMLeafBacklogBoundIsIndependentOfPacketSize(t *testing.T) {
	const (
		rateBytesPerSecond = 680_000
		admissionBytes     = 51_093
		mtu                = 1395
		gsoBytes           = 12_555
	)
	geometry, err := deriveTUNAQMQueueGeometry(
		rateBytesPerSecond,
		admissionBytes,
		1,
		gsoBytes,
		engineOutboundBatchDelayBudget,
		mtu,
		mtu,
	)
	if err != nil {
		t.Fatal(err)
	}
	wantLeafLimitBytes := ((admissionBytes + minimumInnerPacketBytes - 1) /
		minimumInnerPacketBytes) * minimumInnerPacketBytes
	if geometry.LeafLimitBytes != wantLeafLimitBytes {
		t.Fatalf(
			"leaf byte limit = %d, want %d for W=%d",
			geometry.LeafLimitBytes,
			wantLeafLimitBytes,
			admissionBytes,
		)
	}
	fullMTUBacklogBytes := (geometry.LeafLimitBytes / mtu) * mtu
	if fullMTUBacklogBytes > geometry.LeafLimitBytes ||
		fullMTUBacklogBytes+mtu <= geometry.LeafLimitBytes {
		t.Fatalf(
			"full-MTU backlog boundary %d does not respect byte limit %d",
			fullMTUBacklogBytes,
			geometry.LeafLimitBytes,
		)
	}
}

func TestTUNAQMRingIsOnlyTransientFullMTUHandoff(t *testing.T) {
	const (
		rateBytesPerSecond = 680_000
		admissionBytes     = 51_093
		mtu                = 1395
		gsoBytes           = 12_555
	)
	geometry, err := deriveTUNAQMQueueGeometry(
		rateBytesPerSecond,
		admissionBytes,
		1,
		gsoBytes,
		engineOutboundBatchDelayBudget,
		mtu,
		mtu,
	)
	if err != nil {
		t.Fatal(err)
	}
	arrivalBytes := int(math.Ceil(
		rateBytesPerSecond * engineOutboundBatchDelayBudget.Seconds(),
	))
	maximumTransientSlots := (arrivalBytes + geometry.HTBBurstBytes + mtu - 1) / mtu
	if geometry.RingSlots !=
		maximumTransientSlots+tunRingImplementationGuardSlots {
		t.Fatalf(
			"ring permits %d full-MTU bytes in %d slots, want transient handoff %d slots plus %d implementation guard (%d bytes)",
			geometry.RingSlots*mtu,
			geometry.RingSlots,
			maximumTransientSlots,
			tunRingImplementationGuardSlots,
			(maximumTransientSlots+tunRingImplementationGuardSlots)*mtu,
		)
	}
}

func TestTUNAQMHTBBurstHasExactTCTextReadback(t *testing.T) {
	geometry, err := deriveTUNAQMQueueGeometry(
		680_000,
		51_093,
		1,
		15_345,
		engineOutboundBatchDelayBudget,
		1395,
		1395,
	)
	if err != nil {
		t.Fatal(err)
	}
	if geometry.HTBBurstBytes != 15_360 {
		t.Fatalf("HTB burst = %d, want exact tc-renderable 15360", geometry.HTBBurstBytes)
	}
}

func TestTUNIngressPressureUsesCounterDeltasWithoutReplay(t *testing.T) {
	previous := tunIngressPressureCounters{
		ObservedAt:               time.Unix(20, 0),
		TUNBytes:                 100_000,
		AdmissionWaitNanoseconds: uint64(2 * time.Second),
	}
	current := tunIngressPressureCounters{
		ObservedAt:               time.Unix(21, 0),
		TUNBytes:                 750_000,
		AdmissionWaitNanoseconds: uint64(3 * time.Second),
	}
	delta, err := deriveTUNIngressPressureDelta(previous, current)
	if err != nil {
		t.Fatal(err)
	}
	if delta.Interval != time.Second ||
		delta.TUNBytes != 650_000 ||
		delta.AdmissionWaitDuration != time.Second {
		t.Fatalf("pressure delta = %+v", delta)
	}

	replayed := current
	replayed.ObservedAt = time.Unix(22, 0)
	delta, err = deriveTUNIngressPressureDelta(current, replayed)
	if err != nil {
		t.Fatal(err)
	}
	if delta.TUNBytes != 0 || delta.AdmissionWaitDuration != 0 {
		t.Fatalf("unchanged cumulative counters replayed pressure: %+v", delta)
	}
}

func TestTUNAQMRingAndLeafServiceBoundUsesFullMTUPackets(t *testing.T) {
	const (
		rateBytesPerSecond = 1_020_000
		dataBudgetBytes    = 51_000
		mtu                = 1395
	)
	bounds, err := deriveEngineOutboundBounds(
		rateBytesPerSecond, 1, mtu, mtu, dataBudgetBytes,
	)
	if err != nil {
		t.Fatal(err)
	}
	geometry, err := deriveTUNAQMQueueGeometry(
		rateBytesPerSecond,
		bounds.AdmissionLimitBytes,
		1,
		bounds.GSOMaxSize,
		bounds.MaxBatchServiceTime,
		mtu,
		mtu,
	)
	if err != nil {
		t.Fatal(err)
	}
	arrivalBytes := int(math.Ceil(
		rateBytesPerSecond * bounds.MaxBatchServiceTime.Seconds(),
	))
	requiredRingSlots := (arrivalBytes+geometry.HTBBurstBytes+mtu-1)/mtu +
		tunRingImplementationGuardSlots
	if geometry.RingSlots != requiredRingSlots {
		t.Fatalf(
			"derived ring slots = %d, want %d",
			geometry.RingSlots,
			requiredRingSlots,
		)
	}
	wantMaximumServiceTime := time.Duration(
		float64(geometry.RingSlots*mtu+geometry.LeafLimitBytes) /
			rateBytesPerSecond * float64(time.Second),
	)
	if geometry.MaximumServiceTime != wantMaximumServiceTime {
		t.Fatalf(
			"maximum service time = %s, want full-MTU ring plus byte leaf %s",
			geometry.MaximumServiceTime,
			wantMaximumServiceTime,
		)
	}
}

func TestEngineOutboundBoundsFitWholeBatchWithinDelayBudget(t *testing.T) {
	tests := []struct {
		name     string
		rate     float64
		peers    int
		mtu      int
		budget   int
		wantSize int
		wantSegs int
		wantGate int
	}{
		{
			name: "cycle 6 Pi reduced target", rate: 659_031.0606091819,
			peers: 1, mtu: 1319, budget: 45_000,
			wantSize: 11_871, wantSegs: 9, wantGate: 57_159,
		},
		{
			name: "cycle 6 o3 target", rate: 1_020_000,
			peers: 1, mtu: 1395, budget: 60_000,
			wantSize: 19_530, wantSegs: 14, wantGate: 79_978,
		},
		{
			name: "two peers use per-peer rate", rate: 1_360_000,
			peers: 2, mtu: 1395, budget: 45_000,
			wantSize: 12_555, wantSegs: 9, wantGate: 57_843,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := deriveEngineOutboundBounds(
				test.rate, test.peers, test.mtu, test.mtu, test.budget,
			)
			if err != nil {
				t.Fatal(err)
			}
			if got.GSOMaxSize != test.wantSize ||
				got.GSOMaxSegments != test.wantSegs ||
				got.AdmissionLimitBytes != test.wantGate {
				t.Fatalf("bounds = %+v, want size=%d segments=%d gate=%d",
					got, test.wantSize, test.wantSegs, test.wantGate)
			}
			if got.MaxBatchServiceTime > engineOutboundBatchDelayBudget {
				t.Fatalf("whole-batch service time = %s, want <= %s",
					got.MaxBatchServiceTime, engineOutboundBatchDelayBudget)
			}
		})
	}
}

func TestEngineOutboundBoundsRejectRateBelowOneDatagramBudget(t *testing.T) {
	if _, err := deriveEngineOutboundBounds(50_000, 1, 1395, 1395, 45_000); err == nil {
		t.Fatal("derived bounds below one legal WireGuard datagram per delay budget")
	}
}

func TestEngineOutboundBoundsUseMaximumMTUAcrossResize(t *testing.T) {
	got, err := deriveEngineOutboundBounds(659_031.0606091819, 1, 1319, 1320, 45_000)
	if err != nil {
		t.Fatal(err)
	}
	if got.GSOMaxSize != 11_871 || got.GSOMaxSegments != 9 ||
		got.AdmissionLimitBytes != 57_168 {
		t.Fatalf("resize-safe bounds = %+v, want size=11871 segments=9 gate=57168", got)
	}
}

func TestEngineOutboundBoundsPreserveOneBDPAndWholeBatch(t *testing.T) {
	const (
		rateBytesPerSecond = 680_000
		dataBudgetBytes    = 45_000
		mtu                = 1395
	)
	got, err := deriveEngineOutboundBounds(
		rateBytesPerSecond, 1, mtu, mtu, dataBudgetBytes,
	)
	if err != nil {
		t.Fatal(err)
	}
	wholeBatchBytes := got.GSOMaxSegments * (mtu + awgdevice.MessageTransportSize)
	wantAdmissionBytes := dataBudgetBytes + wholeBatchBytes
	if got.AdmissionLimitBytes != wantAdmissionBytes {
		t.Fatalf(
			"admission limit = %d, want B+C = %d+%d = %d",
			got.AdmissionLimitBytes,
			dataBudgetBytes,
			wholeBatchBytes,
			wantAdmissionBytes,
		)
	}

	geometry, err := deriveTUNAQMQueueGeometry(
		rateBytesPerSecond,
		got.AdmissionLimitBytes,
		1,
		got.GSOMaxSize,
		got.MaxBatchServiceTime,
		mtu,
		mtu,
	)
	if err != nil {
		t.Fatal(err)
	}
	servicePackets := (wantAdmissionBytes + minimumInnerPacketBytes - 1) /
		minimumInnerPacketBytes
	if geometry.LeafLimitBytes != servicePackets*minimumInnerPacketBytes {
		t.Fatalf(
			"leaf service capacity = %d bytes, want at least B+C = %d slots",
			geometry.LeafLimitBytes,
			servicePackets,
		)
	}
}

func TestTUNAQMFailedReconciliationPublishesObservedActualAsStale(t *testing.T) {
	kernel := &memoryTUNAQMKernel{
		actual: tunAQMActualState{
			RateBytesPerSecond: 400_000,
			TxQueueLen:         128,
			RootKind:           "pfifo_fast",
			ObservedAt:         time.Unix(10, 0),
		},
		applyErr: errors.New("injected apply failure"),
	}
	reconciler, err := newTUNAQMReconciler(kernel)
	if err != nil {
		t.Fatal(err)
	}
	target := tunAQMTargetState{
		Epoch: 9, RateBytesPerSecond: 680_000, TxQueueLen: tunAQMTxQueueLen,
		BurstBytes: 13_950, MTU: 1395, QueueLimitBytes: 65_000,
		GSOMaxSize: 13_950, GSOMaxSegments: 10, AdmissionLimitBytes: 14_270,
	}
	snapshot, err := reconciler.Reconcile(target)
	if err == nil {
		t.Fatal("Reconcile succeeded, want injected apply failure")
	}
	if snapshot.Actual.Fresh ||
		snapshot.Actual.RateBytesPerSecond != 400_000 ||
		snapshot.Actual.TxQueueLen != 128 ||
		snapshot.Actual.ObservedAt != time.Unix(10, 0) {
		t.Fatalf("stale actual = %+v, want last exact kernel observation", snapshot.Actual)
	}
}
