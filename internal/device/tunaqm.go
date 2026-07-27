package device

import (
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/7mind/wanbond/internal/metrics"
	"github.com/amnezia-vpn/amneziawg-go/conn"
	awgdevice "github.com/amnezia-vpn/amneziawg-go/device"
)

const (
	engineOutboundBatchDelayBudget = 20 * time.Millisecond
	linuxDefaultGSOMaxSize         = 64 * 1024
	minimumInnerPacketBytes        = 20
)

type tunAQMTargetState struct {
	Epoch               uint64
	RateBytesPerSecond  float64
	BurstBytes          int
	TxQueueLen          int
	MTU                 int
	QueueLimit          int
	GSOMaxSize          int
	GSOMaxSegments      int
	AdmissionLimitBytes int
}

type tunAQMActualState struct {
	Epoch              uint64
	RateBytesPerSecond float64
	BurstBytes         int
	TxQueueLen         int
	RootKind           string
	LeafKind           string
	Limit              int
	FlowLimit          int
	Quantum            int
	InitialQuantum     int
	GSOMaxSize         int
	GSOMaxSegments     int
	QueueLength        int
	BacklogBytes       int
	RingPending        bool
	Drops              uint64
	ObservedAt         time.Time
	Fresh              bool
}

type tunAQMSnapshot struct {
	Target             tunAQMTargetState
	Actual             tunAQMActualState
	RateFresh          bool
	QueueLimitDeferred bool
	RingSizeDeferred   bool
	GSOLimitsDeferred  bool
	AdmissionDeferred  bool
}

type tunAQMApplyResult struct {
	QueueLimitDeferred bool
	RingSizeDeferred   bool
	GSOLimitsDeferred  bool
}

type tunAQMKernel interface {
	Apply(tunAQMTargetState) (tunAQMApplyResult, error)
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
	if target.BurstBytes <= 0 || target.TxQueueLen <= 0 ||
		target.MTU <= 0 || target.QueueLimit <= 0 ||
		target.GSOMaxSize <= 0 || target.GSOMaxSegments <= 0 ||
		target.AdmissionLimitBytes <= 0 {
		return tunAQMSnapshot{}, errors.New("TUN AQM burst, queue, GSO, and engine-admission targets must be positive")
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.snapshot.Target = target
	r.snapshot.Actual.Fresh = false
	r.snapshot.RateFresh = false
	r.snapshot.QueueLimitDeferred = false
	r.snapshot.RingSizeDeferred = false
	r.snapshot.GSOLimitsDeferred = false
	actual, readErr := r.kernel.Read()
	if readErr == nil {
		actual.Fresh = false
		r.snapshot.Actual = actual
		if validateTUNAQMReadback(target, actual) == nil {
			actual.Epoch = target.Epoch
			actual.Fresh = true
			r.snapshot.Actual = actual
			r.snapshot.RateFresh = true
			return r.snapshot, nil
		}
	}
	apply, err := r.kernel.Apply(target)
	if err != nil {
		return r.snapshot, err
	}
	actual, err = r.kernel.Read()
	if err != nil {
		return r.snapshot, err
	}
	actual.Fresh = false
	r.snapshot.Actual = actual
	if err := validateTUNAQMReadback(target, actual); err == nil {
		actual.Epoch = target.Epoch
		actual.Fresh = true
		r.snapshot.Actual = actual
		r.snapshot.RateFresh = true
		return r.snapshot, nil
	}
	if err := validateDeferredTUNAQMReadback(target, actual, apply); err != nil {
		return r.snapshot, err
	}
	actual.Epoch = target.Epoch
	r.snapshot.Actual = actual
	r.snapshot.RateFresh = true
	r.snapshot.QueueLimitDeferred = apply.QueueLimitDeferred
	r.snapshot.RingSizeDeferred = apply.RingSizeDeferred
	r.snapshot.GSOLimitsDeferred = apply.GSOLimitsDeferred
	return r.snapshot, nil
}

func (r *tunAQMReconciler) SetAdmissionDeferred(deferred bool) {
	r.mu.Lock()
	r.snapshot.AdmissionDeferred = deferred
	r.mu.Unlock()
}

func validateDeferredTUNAQMReadback(
	target tunAQMTargetState,
	actual tunAQMActualState,
	apply tunAQMApplyResult,
) error {
	if !apply.QueueLimitDeferred && !apply.RingSizeDeferred &&
		!apply.GSOLimitsDeferred {
		return validateTUNAQMReadback(target, actual)
	}
	if actual.RootKind != "htb" ||
		actual.LeafKind != "fq" ||
		actual.BurstBytes != target.BurstBytes ||
		actual.Quantum != target.MTU ||
		actual.InitialQuantum != target.MTU {
		return errors.New("TUN AQM non-deferred readback fields do not match target")
	}
	if apply.RingSizeDeferred {
		if actual.TxQueueLen < target.TxQueueLen ||
			!actual.RingPending {
			return errors.New("TUN AQM ring-size deferral has no occupied installed superset")
		}
	} else if actual.TxQueueLen != target.TxQueueLen {
		return errors.New("TUN AQM ring-size readback does not match target")
	}
	if apply.QueueLimitDeferred {
		if actual.Limit < target.QueueLimit ||
			actual.FlowLimit < target.QueueLimit {
			return errors.New("TUN AQM queue-limit deferral has no safe installed superset")
		}
	} else if actual.Limit != target.QueueLimit ||
		actual.FlowLimit != target.QueueLimit {
		return errors.New("TUN AQM queue-limit readback does not match target")
	}
	if apply.GSOLimitsDeferred {
		if actual.GSOMaxSize < target.GSOMaxSize ||
			actual.GSOMaxSegments < target.GSOMaxSegments {
			return errors.New("TUN AQM GSO deferral has no queued installed superset")
		}
	} else if actual.GSOMaxSize != target.GSOMaxSize ||
		actual.GSOMaxSegments != target.GSOMaxSegments {
		return errors.New("TUN AQM GSO readback does not match target")
	}
	tolerance := target.RateBytesPerSecond * 0.01
	if math.Abs(actual.RateBytesPerSecond-target.RateBytesPerSecond) > tolerance {
		return errors.New("TUN AQM deferred rate readback does not match target")
	}
	if actual.ObservedAt.IsZero() {
		return errors.New("TUN AQM deferred readback observation time is required")
	}
	return nil
}

func (r *tunAQMReconciler) Snapshot() tunAQMSnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.snapshot
}

func (r *tunAQMReconciler) MetricsSnapshot() *metrics.TUNAQMSnapshot {
	snapshot := r.Snapshot()
	return &metrics.TUNAQMSnapshot{
		TargetRateBytesPerSecond:  snapshot.Target.RateBytesPerSecond,
		ActualRateBytesPerSecond:  snapshot.Actual.RateBytesPerSecond,
		TargetHTBBurstBytes:       snapshot.Target.BurstBytes,
		ActualHTBBurstBytes:       snapshot.Actual.BurstBytes,
		TargetTxQueueLen:          snapshot.Target.TxQueueLen,
		ActualTxQueueLen:          snapshot.Actual.TxQueueLen,
		TargetEpoch:               snapshot.Target.Epoch,
		ActualEpoch:               snapshot.Actual.Epoch,
		TargetQueueLimitPackets:   snapshot.Target.QueueLimit,
		ActualQueueLimitPackets:   snapshot.Actual.Limit,
		ActualFlowLimitPackets:    snapshot.Actual.FlowLimit,
		TargetGSOMaxSizeBytes:     snapshot.Target.GSOMaxSize,
		ActualGSOMaxSizeBytes:     snapshot.Actual.GSOMaxSize,
		TargetGSOMaxSegments:      snapshot.Target.GSOMaxSegments,
		ActualGSOMaxSegments:      snapshot.Actual.GSOMaxSegments,
		TargetAdmissionLimitBytes: snapshot.Target.AdmissionLimitBytes,
		ActualFresh:               snapshot.Actual.Fresh,
		RateFresh:                 snapshot.RateFresh,
		ActualQueueLengthPackets:  snapshot.Actual.QueueLength,
		ActualBacklogBytes:        snapshot.Actual.BacklogBytes,
		ActualRingPending:         snapshot.Actual.RingPending,
		ActualDrops:               snapshot.Actual.Drops,
		QueueLimitDeferred:        snapshot.QueueLimitDeferred,
		RingSizeDeferred:          snapshot.RingSizeDeferred,
		GSOLimitsDeferred:         snapshot.GSOLimitsDeferred,
		AdmissionLimitDeferred:    snapshot.AdmissionDeferred,
		ActualObservedAt:          snapshot.Actual.ObservedAt,
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
	if actual.BurstBytes != target.BurstBytes {
		return fmt.Errorf("TUN AQM HTB burst readback %d bytes, want %d",
			actual.BurstBytes, target.BurstBytes)
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
	if actual.GSOMaxSize != target.GSOMaxSize ||
		actual.GSOMaxSegments != target.GSOMaxSegments {
		return fmt.Errorf(
			"TUN AQM GSO readback size=%d segments=%d, want size=%d segments=%d",
			actual.GSOMaxSize, actual.GSOMaxSegments,
			target.GSOMaxSize, target.GSOMaxSegments,
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

type engineOutboundBounds struct {
	AdmissionLimitBytes int
	GSOMaxSize          int
	GSOMaxSegments      int
	MaxBatchServiceTime time.Duration
}

type tunAQMQueueGeometry struct {
	RingSlots          int
	FQLimit            int
	HTBBurstBytes      int
	MaximumServiceTime time.Duration
}

func deriveEngineOutboundBounds(
	aggregateRateBytesPerSecond float64,
	peerCount int,
	currentMTU int,
	maximumMTU int,
	dataBudgetBytes int,
) (engineOutboundBounds, error) {
	if math.IsNaN(aggregateRateBytesPerSecond) ||
		math.IsInf(aggregateRateBytesPerSecond, 0) ||
		aggregateRateBytesPerSecond <= 0 ||
		peerCount <= 0 ||
		currentMTU <= 0 ||
		maximumMTU < currentMTU ||
		dataBudgetBytes <= 0 {
		return engineOutboundBounds{}, errors.New(
			"engine outbound rate/peer count/MTUs/data budget must be positive and maximum MTU must cover current MTU",
		)
	}
	perPeerRate := aggregateRateBytesPerSecond / float64(peerCount)
	maxWireDatagram := maximumMTU + awgdevice.MessageTransportSize
	wireBudget := int(math.Floor(perPeerRate * engineOutboundBatchDelayBudget.Seconds()))
	if wireBudget < maxWireDatagram {
		return engineOutboundBounds{}, fmt.Errorf(
			"engine outbound %s delay budget permits %d bytes at %g B/s, below one %d-byte WireGuard datagram",
			engineOutboundBatchDelayBudget, wireBudget, perPeerRate, maxWireDatagram,
		)
	}
	maxSegments := wireBudget / maxWireDatagram
	if maxSegments > conn.IdealBatchSize {
		maxSegments = conn.IdealBatchSize
	}
	if sizeSegments := linuxDefaultGSOMaxSize / currentMTU; maxSegments > sizeSegments {
		maxSegments = sizeSegments
	}
	if maxSegments < 1 {
		return engineOutboundBounds{}, errors.New(
			"engine outbound delay budget cannot admit one GSO segment",
		)
	}
	wholeBatchBytes := maxSegments * maxWireDatagram
	if dataBudgetBytes > math.MaxInt-wholeBatchBytes {
		return engineOutboundBounds{}, errors.New(
			"engine outbound BDP plus whole-batch admission limit overflows int",
		)
	}
	admissionLimit := dataBudgetBytes + wholeBatchBytes
	serviceTime := time.Duration(
		float64(wholeBatchBytes) / perPeerRate * float64(time.Second),
	)
	if serviceTime > engineOutboundBatchDelayBudget {
		return engineOutboundBounds{}, fmt.Errorf(
			"engine outbound maximum batch service time %s exceeds %s",
			serviceTime, engineOutboundBatchDelayBudget,
		)
	}
	return engineOutboundBounds{
		AdmissionLimitBytes: admissionLimit,
		GSOMaxSize:          maxSegments * currentMTU,
		GSOMaxSegments:      maxSegments,
		MaxBatchServiceTime: serviceTime,
	}, nil
}

func deriveTUNAQMQueueGeometry(
	aggregateRateBytesPerSecond float64,
	admissionLimitBytes int,
	peerCount int,
	deviceBatchSize int,
	gsoMaxSizeBytes int,
) (tunAQMQueueGeometry, error) {
	if math.IsNaN(aggregateRateBytesPerSecond) ||
		math.IsInf(aggregateRateBytesPerSecond, 0) ||
		aggregateRateBytesPerSecond <= 0 ||
		admissionLimitBytes <= 0 ||
		peerCount <= 0 ||
		deviceBatchSize <= 0 ||
		gsoMaxSizeBytes <= 0 {
		return tunAQMQueueGeometry{}, errors.New(
			"TUN AQM rate, admission limit, peer count, device batch size, and GSO maximum must be positive",
		)
	}
	maxInt := int(^uint(0) >> 1)
	if admissionLimitBytes > maxInt/peerCount {
		return tunAQMQueueGeometry{}, errors.New(
			"TUN AQM aggregate admission window overflows int",
		)
	}
	windowBytes := admissionLimitBytes * peerCount
	if windowBytes > maxInt-(minimumInnerPacketBytes-1) {
		return tunAQMQueueGeometry{}, errors.New(
			"TUN AQM service-slot rounding overflows int",
		)
	}
	serviceSlots := (windowBytes + minimumInnerPacketBytes - 1) /
		minimumInnerPacketBytes
	if gsoMaxSizeBytes > maxInt-(minimumInnerPacketBytes-1) {
		return tunAQMQueueGeometry{}, errors.New(
			"TUN AQM direct-burst slot rounding overflows int",
		)
	}
	directBurstSlots := (gsoMaxSizeBytes + minimumInnerPacketBytes - 1) /
		minimumInnerPacketBytes
	if directBurstSlots < deviceBatchSize {
		directBurstSlots = deviceBatchSize
	}
	if serviceSlots > maxInt-directBurstSlots {
		return tunAQMQueueGeometry{}, errors.New(
			"TUN AQM guarded ptr-ring slot count overflows int",
		)
	}
	ringSlots := serviceSlots + directBurstSlots
	if uint64(ringSlots) > uint64(^uint32(0)) {
		return tunAQMQueueGeometry{}, errors.New(
			"TUN AQM ptr-ring slot count overflows Linux tx_queue_len",
		)
	}
	if ringSlots > maxInt/minimumInnerPacketBytes ||
		serviceSlots > maxInt/minimumInnerPacketBytes {
		return tunAQMQueueGeometry{}, errors.New(
			"TUN AQM total service window overflows int",
		)
	}
	ringWindowBytes := ringSlots * minimumInnerPacketBytes
	fqWindowBytes := serviceSlots * minimumInnerPacketBytes
	if ringWindowBytes > maxInt-fqWindowBytes {
		return tunAQMQueueGeometry{}, errors.New(
			"TUN AQM total service window overflows int",
		)
	}
	totalWindowBytes := ringWindowBytes + fqWindowBytes
	serviceNanoseconds := float64(totalWindowBytes) /
		aggregateRateBytesPerSecond *
		float64(time.Second)
	if serviceNanoseconds > float64(time.Duration(1<<63-1)) {
		return tunAQMQueueGeometry{}, errors.New(
			"TUN AQM maximum service time overflows duration",
		)
	}
	maximumServiceTime := time.Duration(serviceNanoseconds)
	if maximumServiceTime <= 0 {
		return tunAQMQueueGeometry{}, errors.New(
			"TUN AQM maximum service time must be positive",
		)
	}
	return tunAQMQueueGeometry{
		RingSlots:          ringSlots,
		FQLimit:            serviceSlots,
		HTBBurstBytes:      gsoMaxSizeBytes,
		MaximumServiceTime: maximumServiceTime,
	}, nil
}
