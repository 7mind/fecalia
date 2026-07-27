package device

import (
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/7mind/wanbond/internal/metrics"
)

const (
	tunAQMTxQueueLen    = 32
	tunAQMLimit         = 64
	tunAQMFlows         = 64
	tunAQMTarget        = 5 * time.Millisecond
	tunAQMInterval      = 100 * time.Millisecond
	tunAQMMemoryLimit   = 4 * 1024 * 1024
	tunAQMDropBatch     = 16
	tunAQMTimeTolerance = time.Millisecond
)

type tunAQMTargetState struct {
	Epoch              uint64
	RateBytesPerSecond float64
	TxQueueLen         int
	MTU                int
}

type tunAQMActualState struct {
	Epoch              uint64
	RateBytesPerSecond float64
	TxQueueLen         int
	RootKind           string
	LeafKind           string
	Limit              int
	Flows              int
	Quantum            int
	Target             time.Duration
	Interval           time.Duration
	MemoryLimit        int
	ECN                bool
	DropBatch          int
	ObservedAt         time.Time
	Fresh              bool
}

type tunAQMSnapshot struct {
	Target tunAQMTargetState
	Actual tunAQMActualState
}

type tunAQMKernel interface {
	Apply(tunAQMTargetState) error
	Read() (tunAQMActualState, error)
}

type tunAQMReconciler struct {
	mu       sync.Mutex
	kernel   tunAQMKernel
	snapshot tunAQMSnapshot
}

func newTUNAQMReconciler(kernel tunAQMKernel) (*tunAQMReconciler, error) {
	if kernel == nil {
		return nil, errors.New("TUN AQM kernel is required")
	}
	return &tunAQMReconciler{kernel: kernel}, nil
}

func (r *tunAQMReconciler) Reconcile(target tunAQMTargetState) (tunAQMSnapshot, error) {
	if math.IsNaN(target.RateBytesPerSecond) ||
		math.IsInf(target.RateBytesPerSecond, 0) ||
		target.RateBytesPerSecond <= 0 {
		return tunAQMSnapshot{}, errors.New("TUN AQM target rate must be finite and positive")
	}
	if target.TxQueueLen <= 0 || target.MTU <= 0 {
		return tunAQMSnapshot{}, errors.New("TUN AQM tx queue length and MTU must be positive")
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.snapshot.Target = target
	r.snapshot.Actual.Fresh = false
	actual, readErr := r.kernel.Read()
	if readErr == nil {
		actual.Fresh = false
		r.snapshot.Actual = actual
		if validateTUNAQMReadback(target, actual) == nil {
			actual.Epoch = target.Epoch
			actual.Fresh = true
			r.snapshot.Actual = actual
			return r.snapshot, nil
		}
	}
	if err := r.kernel.Apply(target); err != nil {
		return r.snapshot, err
	}
	actual, err := r.kernel.Read()
	if err != nil {
		return r.snapshot, err
	}
	actual.Fresh = false
	r.snapshot.Actual = actual
	if err := validateTUNAQMReadback(target, actual); err != nil {
		return r.snapshot, err
	}
	actual.Epoch = target.Epoch
	actual.Fresh = true
	r.snapshot.Actual = actual
	return r.snapshot, nil
}

func (r *tunAQMReconciler) Snapshot() tunAQMSnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.snapshot
}

func (r *tunAQMReconciler) MetricsSnapshot() *metrics.TUNAQMSnapshot {
	snapshot := r.Snapshot()
	return &metrics.TUNAQMSnapshot{
		TargetRateBytesPerSecond: snapshot.Target.RateBytesPerSecond,
		ActualRateBytesPerSecond: snapshot.Actual.RateBytesPerSecond,
		TargetTxQueueLen:         snapshot.Target.TxQueueLen,
		ActualTxQueueLen:         snapshot.Actual.TxQueueLen,
		TargetEpoch:              snapshot.Target.Epoch,
		ActualEpoch:              snapshot.Actual.Epoch,
		ActualFresh:              snapshot.Actual.Fresh,
		ActualObservedAt:         snapshot.Actual.ObservedAt,
	}
}

func validateTUNAQMReadback(target tunAQMTargetState, actual tunAQMActualState) error {
	if actual.TxQueueLen != target.TxQueueLen {
		return fmt.Errorf("TUN AQM tx queue length readback %d, want %d",
			actual.TxQueueLen, target.TxQueueLen)
	}
	if actual.RootKind != "htb" || actual.LeafKind != "fq_codel" {
		return fmt.Errorf("TUN AQM qdisc readback root=%q leaf=%q, want htb/fq_codel",
			actual.RootKind, actual.LeafKind)
	}
	if actual.Limit != tunAQMLimit ||
		actual.Flows != tunAQMFlows ||
		actual.Quantum != target.MTU ||
		actual.MemoryLimit != tunAQMMemoryLimit ||
		!actual.ECN ||
		actual.DropBatch != tunAQMDropBatch ||
		absDuration(actual.Target-tunAQMTarget) > tunAQMTimeTolerance ||
		absDuration(actual.Interval-tunAQMInterval) > tunAQMTimeTolerance {
		return fmt.Errorf(
			"TUN AQM fq_codel readback limit=%d flows=%d quantum=%d target=%s interval=%s memory_limit=%d ecn=%t drop_batch=%d",
			actual.Limit, actual.Flows, actual.Quantum, actual.Target, actual.Interval,
			actual.MemoryLimit, actual.ECN, actual.DropBatch,
		)
	}
	tolerance := target.RateBytesPerSecond * 0.01
	if math.Abs(actual.RateBytesPerSecond-target.RateBytesPerSecond) > tolerance {
		return fmt.Errorf("TUN AQM rate readback %g B/s, want %g B/s within 1%%",
			actual.RateBytesPerSecond, target.RateBytesPerSecond)
	}
	if actual.ObservedAt.IsZero() {
		return errors.New("TUN AQM readback observation time is required")
	}
	return nil
}

func absDuration(value time.Duration) time.Duration {
	if value < 0 {
		return -value
	}
	return value
}
