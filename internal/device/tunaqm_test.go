package device

import (
	"errors"
	"testing"
	"time"
)

type memoryTUNAQMKernel struct {
	actual   tunAQMActualState
	applyErr error
}

func (k *memoryTUNAQMKernel) Apply(target tunAQMTargetState) error {
	if k.applyErr != nil {
		return k.applyErr
	}
	k.actual = tunAQMActualState{
		RateBytesPerSecond: target.RateBytesPerSecond,
		TxQueueLen:         target.TxQueueLen,
		RootKind:           "htb",
		LeafKind:           "fq",
		Limit:              target.QueueLimit,
		FlowLimit:          target.QueueLimit,
		Quantum:            target.MTU,
		InitialQuantum:     target.MTU,
		GSOMaxSize:         target.GSOMaxSize,
		GSOMaxSegments:     target.GSOMaxSegments,
		ObservedAt:         time.Now(),
	}
	return nil
}

func (k *memoryTUNAQMKernel) Read() (tunAQMActualState, error) {
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
		MTU: 1395, QueueLimit: 65,
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
	if err := kernel.Apply(drift); err != nil {
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

func TestTUNAQMUsesBoundedNonDroppingLeaf(t *testing.T) {
	kernel := &memoryTUNAQMKernel{}
	reconciler, err := newTUNAQMReconciler(kernel)
	if err != nil {
		t.Fatal(err)
	}
	target := tunAQMTargetState{
		Epoch: 5, RateBytesPerSecond: 680_000, TxQueueLen: tunAQMTxQueueLen,
		MTU: 1395, QueueLimit: 65,
		GSOMaxSize: 13_950, GSOMaxSegments: 10, AdmissionLimitBytes: 14_270,
	}
	snapshot, err := reconciler.Reconcile(target)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Actual.LeafKind != "fq" {
		t.Fatalf("TUN leaf kind = %q, want bounded non-dropping fq", snapshot.Actual.LeafKind)
	}
}

func TestTUNAQMQueueLimitIncludesServiceBacklogAndDeviceQueue(t *testing.T) {
	tests := []struct {
		name       string
		burstBytes int
		peerCount  int
		mtu        int
		want       int
	}{
		{name: "Pi BDP", burstBytes: 45_000, peerCount: 1, mtu: 1395, want: 65},
		{name: "o3 synthetic burst", burstBytes: 60_000, peerCount: 1, mtu: 1395, want: 76},
		{name: "two peers", burstBytes: 45_000, peerCount: 2, mtu: 1395, want: 97},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := deriveTUNAQMQueueLimit(
				test.burstBytes, test.peerCount, test.mtu,
			)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("queue limit = %d, want %d", got, test.want)
			}
		})
	}
}

func TestEngineOutboundBoundsFitWholeBatchWithinDelayBudget(t *testing.T) {
	tests := []struct {
		name     string
		rate     float64
		peers    int
		mtu      int
		wantSize int
		wantSegs int
		wantGate int
	}{
		{
			name: "cycle 6 Pi reduced target", rate: 659_031.0606091819,
			peers: 1, mtu: 1319, wantSize: 11_871, wantSegs: 9, wantGate: 12_159,
		},
		{
			name: "cycle 6 o3 target", rate: 1_020_000,
			peers: 1, mtu: 1395, wantSize: 19_530, wantSegs: 14, wantGate: 19_978,
		},
		{
			name: "two peers use per-peer rate", rate: 1_360_000,
			peers: 2, mtu: 1395, wantSize: 12_555, wantSegs: 9, wantGate: 12_843,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := deriveEngineOutboundBounds(
				test.rate, test.peers, test.mtu, test.mtu,
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
			if got.MaxBatchServiceTime > engineOutboundDelayBudget {
				t.Fatalf("whole-batch service time = %s, want <= %s",
					got.MaxBatchServiceTime, engineOutboundDelayBudget)
			}
		})
	}
}

func TestEngineOutboundBoundsRejectRateBelowOneDatagramBudget(t *testing.T) {
	if _, err := deriveEngineOutboundBounds(50_000, 1, 1395, 1395); err == nil {
		t.Fatal("derived bounds below one legal WireGuard datagram per delay budget")
	}
}

func TestEngineOutboundBoundsUseMaximumMTUAcrossResize(t *testing.T) {
	got, err := deriveEngineOutboundBounds(659_031.0606091819, 1, 1319, 1320)
	if err != nil {
		t.Fatal(err)
	}
	if got.GSOMaxSize != 11_871 || got.GSOMaxSegments != 9 ||
		got.AdmissionLimitBytes != 12_168 {
		t.Fatalf("resize-safe bounds = %+v, want size=11871 segments=9 gate=12168", got)
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
		MTU: 1395, QueueLimit: 65,
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
