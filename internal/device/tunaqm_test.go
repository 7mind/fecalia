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
		{name: "o3 synthetic burst", burstBytes: 58_880, peerCount: 1, mtu: 1395, want: 75},
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
