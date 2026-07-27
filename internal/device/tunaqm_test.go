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
		LeafKind:           "fq_codel",
		Limit:              tunAQMLimit,
		Flows:              tunAQMFlows,
		Quantum:            target.MTU,
		Target:             tunAQMTarget,
		Interval:           tunAQMInterval,
		MemoryLimit:        tunAQMMemoryLimit,
		ECN:                true,
		DropBatch:          tunAQMDropBatch,
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
		Epoch: 4, RateBytesPerSecond: 680_000, TxQueueLen: tunAQMTxQueueLen, MTU: 1395,
	}
	first, err := reconciler.Reconcile(target)
	if err != nil {
		t.Fatal(err)
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
		t.Fatal(err)
	}
	restored, err := reconciler.Reconcile(target)
	if err != nil {
		t.Fatal(err)
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
		Epoch: 9, RateBytesPerSecond: 680_000, TxQueueLen: tunAQMTxQueueLen, MTU: 1395,
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
