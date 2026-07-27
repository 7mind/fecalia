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
	tunAQMTxQueueLen = 32
)

type tunAQMTargetState struct {
	Epoch              uint64
	RateBytesPerSecond float64
	TxQueueLen         int
	MTU                int
	QueueLimit         int
}

type tunAQMActualState struct {
	Epoch              uint64
	RateBytesPerSecond float64
	TxQueueLen         int
	RootKind           string
	LeafKind           string
	Limit              int
	FlowLimit          int
	Quantum            int
	InitialQuantum     int
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
	if target.TxQueueLen <= 0 || target.MTU <= 0 || target.QueueLimit <= 0 {
		return tunAQMSnapshot{}, errors.New("TUN AQM tx queue length, MTU, and queue limit must be positive")
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
		TargetQueueLimitPackets:  snapshot.Target.QueueLimit,
		ActualQueueLimitPackets:  snapshot.Actual.Limit,
		ActualFlowLimitPackets:   snapshot.Actual.FlowLimit,
		ActualFresh:              snapshot.Actual.Fresh,
		ActualObservedAt:         snapshot.Actual.ObservedAt,
	}
}

func validateTUNAQMReadback(target tunAQMTargetState, actual tunAQMActualState) error {
	if actual.TxQueueLen != target.TxQueueLen {
		return fmt.Errorf("TUN AQM tx queue length readback %d, want %d",
			actual.TxQueueLen, target.TxQueueLen)
	}
	if actual.RootKind != "htb" || actual.LeafKind != "fq" {
		return fmt.Errorf("TUN AQM qdisc readback root=%q leaf=%q, want htb/fq",
			actual.RootKind, actual.LeafKind)
	}
	if actual.Limit != target.QueueLimit ||
		actual.FlowLimit != target.QueueLimit ||
		actual.Quantum != target.MTU ||
		actual.InitialQuantum != target.MTU {
		return fmt.Errorf(
			"TUN AQM fq readback limit=%d flow_limit=%d quantum=%d initial_quantum=%d",
			actual.Limit, actual.FlowLimit, actual.Quantum, actual.InitialQuantum,
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

func deriveTUNAQMQueueLimit(dataBurstBytes, peerCount, mtu int) (int, error) {
	if dataBurstBytes <= 0 || peerCount <= 0 || mtu <= 0 {
		return 0, errors.New("TUN AQM burst bytes, peer count, and MTU must be positive")
	}
	maxInt := int(^uint(0) >> 1)
	if dataBurstBytes > maxInt/peerCount {
		return 0, errors.New("TUN AQM aggregate service backlog overflows int")
	}
	serviceBacklogBytes := dataBurstBytes * peerCount
	if serviceBacklogBytes > maxInt-(mtu-1) {
		return 0, errors.New("TUN AQM service backlog rounding overflows int")
	}
	servicePackets := (serviceBacklogBytes + mtu - 1) / mtu
	if servicePackets > maxInt-tunAQMTxQueueLen {
		return 0, errors.New("TUN AQM queue limit overflows int")
	}
	return servicePackets + tunAQMTxQueueLen, nil
}
