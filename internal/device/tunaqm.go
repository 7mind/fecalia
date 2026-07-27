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
	QueueLimitBytes     int
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
	LimitBytes         int
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
	Target                    tunAQMTargetState
	Actual                    tunAQMActualState
	ActualAdmissionLimitBytes int
	RateFresh                 bool
	QueueLimitDeferred        bool
	RingSizeDeferred          bool
	GSOLimitsDeferred         bool
	AdmissionDeferred         bool
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

type tunAQMTransition struct {
	reconciler           *tunAQMReconciler
	trySetAdmissionLimit func(int) (bool, error)
	readAdmissionLimit   func() int
	ringHighWater        int
}

func newTUNAQMReconciler(kernel tunAQMKernel) (*tunAQMReconciler, error) {
	if kernel == nil {
		return nil, errors.New("TUN AQM kernel is required")
	}
	return &tunAQMReconciler{kernel: kernel}, nil
}

func newTUNAQMTransition(
	reconciler *tunAQMReconciler,
	trySetAdmissionLimit func(int) (bool, error),
	readAdmissionLimit func() int,
) (*tunAQMTransition, error) {
	if reconciler == nil {
		return nil, errors.New("TUN AQM reconciler is required")
	}
	if trySetAdmissionLimit == nil {
		return nil, errors.New("engine outbound admission setter is required")
	}
	if readAdmissionLimit == nil {
		return nil, errors.New("engine outbound admission reader is required")
	}
	ringHighWater := reconciler.Snapshot().Actual.TxQueueLen
	if ringHighWater <= 0 {
		return nil, errors.New("initialized TUN AQM ring readback is required")
	}
	transition := &tunAQMTransition{
		reconciler:           reconciler,
		trySetAdmissionLimit: trySetAdmissionLimit,
		readAdmissionLimit:   readAdmissionLimit,
		ringHighWater:        ringHighWater,
	}
	actualAdmissionLimit := transition.actualAdmissionLimit()
	transition.reconciler.SetAdmissionState(actualAdmissionLimit, false)
	return transition, nil
}

func (t *tunAQMTransition) actualAdmissionLimit() int {
	limit := t.readAdmissionLimit()
	if limit <= 0 {
		panic("engine outbound admission actual limit must be positive")
	}
	return limit
}

func (t *tunAQMTransition) Reconcile(
	target tunAQMTargetState,
) (tunAQMSnapshot, error) {
	if err := validateTUNAQMTarget(target); err != nil {
		return t.reconciler.Snapshot(), err
	}
	if target.TxQueueLen < t.ringHighWater {
		target.TxQueueLen = t.ringHighWater
	} else {
		t.ringHighWater = target.TxQueueLen
	}
	actualAdmissionLimit := t.actualAdmissionLimit()
	switch {
	case target.AdmissionLimitBytes > actualAdmissionLimit:
		snapshot, err := t.reconciler.Reconcile(target)
		if err != nil {
			return snapshot, err
		}
		admissionApplied, err := t.trySetAdmissionLimit(target.AdmissionLimitBytes)
		if err != nil {
			t.reconciler.SetAdmissionState(t.actualAdmissionLimit(), true)
			return t.reconciler.Snapshot(),
				fmt.Errorf("grow engine outbound admission: %w", err)
		}
		installedAdmissionLimit := t.actualAdmissionLimit()
		if admissionApplied && installedAdmissionLimit != target.AdmissionLimitBytes {
			panic("engine outbound admission growth did not install requested limit")
		}
		if !admissionApplied && installedAdmissionLimit != actualAdmissionLimit {
			panic("deferred engine outbound admission growth changed applied limit")
		}
		t.reconciler.SetAdmissionState(installedAdmissionLimit, !admissionApplied)
		return t.reconciler.Snapshot(), nil

	case target.AdmissionLimitBytes < actualAdmissionLimit:
		admissionApplied, err := t.trySetAdmissionLimit(target.AdmissionLimitBytes)
		if err != nil {
			t.reconciler.SetAdmissionState(t.actualAdmissionLimit(), true)
			return t.reconciler.Snapshot(),
				fmt.Errorf("shrink engine outbound admission: %w", err)
		}
		installedAdmissionLimit := t.actualAdmissionLimit()
		if !admissionApplied {
			if installedAdmissionLimit != actualAdmissionLimit {
				panic("deferred engine outbound admission shrink changed applied limit")
			}
			installedTarget, err := heldTUNAQMTarget(
				target,
				t.reconciler.Snapshot().Actual,
				installedAdmissionLimit,
			)
			if err != nil {
				return t.reconciler.Snapshot(), err
			}
			_, err = t.reconciler.Reconcile(installedTarget)
			if err != nil {
				t.reconciler.SetAdmissionState(installedAdmissionLimit, true)
				return t.reconciler.Snapshot(), err
			}
			return t.reconciler.SetDeferredAdmissionTarget(
				target,
				installedAdmissionLimit,
			), nil
		}
		if installedAdmissionLimit != target.AdmissionLimitBytes {
			panic("engine outbound admission shrink did not install requested limit")
		}
		t.reconciler.SetAdmissionState(installedAdmissionLimit, false)
		return t.reconciler.Reconcile(target)

	default:
		t.reconciler.SetAdmissionState(actualAdmissionLimit, false)
		return t.reconciler.Reconcile(target)
	}
}

func validateTUNAQMTarget(target tunAQMTargetState) error {
	if math.IsNaN(target.RateBytesPerSecond) ||
		math.IsInf(target.RateBytesPerSecond, 0) ||
		target.RateBytesPerSecond <= 0 {
		return errors.New("TUN AQM target rate must be finite and positive")
	}
	if target.BurstBytes <= 0 || target.TxQueueLen <= 0 ||
		target.MTU <= 0 || target.QueueLimitBytes <= 0 ||
		target.GSOMaxSize <= 0 || target.GSOMaxSegments <= 0 ||
		target.AdmissionLimitBytes <= 0 {
		return errors.New("TUN AQM burst, queue, GSO, and engine-admission targets must be positive")
	}
	return nil
}

func heldTUNAQMTarget(
	desired tunAQMTargetState,
	actual tunAQMActualState,
	actualAdmissionLimitBytes int,
) (tunAQMTargetState, error) {
	held := tunAQMTargetState{
		Epoch:               desired.Epoch,
		RateBytesPerSecond:  desired.RateBytesPerSecond,
		BurstBytes:          actual.BurstBytes,
		TxQueueLen:          actual.TxQueueLen,
		MTU:                 desired.MTU,
		QueueLimitBytes:     actual.LimitBytes,
		GSOMaxSize:          actual.GSOMaxSize,
		GSOMaxSegments:      actual.GSOMaxSegments,
		AdmissionLimitBytes: actualAdmissionLimitBytes,
	}
	if err := validateTUNAQMTarget(held); err != nil {
		return tunAQMTargetState{}, fmt.Errorf(
			"hold installed TUN AQM capacity during admission shrink: %w",
			err,
		)
	}
	return held, nil
}

func (r *tunAQMReconciler) Reconcile(target tunAQMTargetState) (tunAQMSnapshot, error) {
	if err := validateTUNAQMTarget(target); err != nil {
		return tunAQMSnapshot{}, err
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

func (r *tunAQMReconciler) SetAdmissionState(actualLimitBytes int, deferred bool) {
	if actualLimitBytes <= 0 {
		panic("engine outbound admission actual limit must be positive")
	}
	r.mu.Lock()
	r.snapshot.ActualAdmissionLimitBytes = actualLimitBytes
	r.snapshot.AdmissionDeferred = deferred
	r.mu.Unlock()
}

func (r *tunAQMReconciler) SetDeferredAdmissionTarget(
	target tunAQMTargetState,
	actualLimitBytes int,
) tunAQMSnapshot {
	if actualLimitBytes <= 0 {
		panic("engine outbound admission actual limit must be positive")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	tolerance := target.RateBytesPerSecond * 0.01
	if !r.snapshot.RateFresh ||
		r.snapshot.Actual.Epoch != target.Epoch ||
		math.Abs(r.snapshot.Actual.RateBytesPerSecond-target.RateBytesPerSecond) > tolerance {
		panic("deferred engine admission target lacks exact rate and epoch readback")
	}
	r.snapshot.Target = target
	r.snapshot.Actual.Fresh = false
	r.snapshot.ActualAdmissionLimitBytes = actualLimitBytes
	r.snapshot.AdmissionDeferred = true
	return r.snapshot
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
		actual.LeafKind != "bfifo" ||
		actual.BurstBytes != target.BurstBytes {
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
		if actual.LimitBytes < target.QueueLimitBytes {
			return errors.New("TUN AQM queue-limit deferral has no safe installed superset")
		}
	} else if actual.LimitBytes != target.QueueLimitBytes {
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
		TargetQueueLimitBytes:     snapshot.Target.QueueLimitBytes,
		ActualQueueLimitBytes:     snapshot.Actual.LimitBytes,
		TargetGSOMaxSizeBytes:     snapshot.Target.GSOMaxSize,
		ActualGSOMaxSizeBytes:     snapshot.Actual.GSOMaxSize,
		TargetGSOMaxSegments:      snapshot.Target.GSOMaxSegments,
		ActualGSOMaxSegments:      snapshot.Actual.GSOMaxSegments,
		TargetAdmissionLimitBytes: snapshot.Target.AdmissionLimitBytes,
		ActualAdmissionLimitBytes: snapshot.ActualAdmissionLimitBytes,
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
	if actual.RootKind != "htb" || actual.LeafKind != "bfifo" {
		return fmt.Errorf("TUN AQM qdisc readback root=%q leaf=%q, want htb/bfifo",
			actual.RootKind, actual.LeafKind)
	}
	if actual.BurstBytes != target.BurstBytes {
		return fmt.Errorf("TUN AQM HTB burst readback %d bytes, want %d",
			actual.BurstBytes, target.BurstBytes)
	}
	if actual.LimitBytes != target.QueueLimitBytes {
		return fmt.Errorf(
			"TUN AQM bfifo readback limit=%d bytes, want %d",
			actual.LimitBytes, target.QueueLimitBytes,
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
	LeafLimitBytes     int
	HTBBurstBytes      int
	MaximumServiceTime time.Duration
}

type tunIngressPressureCounters struct {
	ObservedAt               time.Time
	TUNBytes                 uint64
	AdmissionWaitNanoseconds uint64
}

type tunIngressPressureDelta struct {
	Interval              time.Duration
	TUNBytes              uint64
	AdmissionWaitDuration time.Duration
}

type tunIngressPressureSampler struct {
	previous tunIngressPressureCounters
}

func (s *tunIngressPressureSampler) Sample(
	current tunIngressPressureCounters,
) (tunIngressPressureDelta, error) {
	previous := s.previous
	s.previous = current
	return deriveTUNIngressPressureDelta(previous, current)
}

func deriveTUNIngressPressureDelta(
	previous tunIngressPressureCounters,
	current tunIngressPressureCounters,
) (tunIngressPressureDelta, error) {
	if previous.ObservedAt.IsZero() || current.ObservedAt.IsZero() ||
		!current.ObservedAt.After(previous.ObservedAt) {
		return tunIngressPressureDelta{}, errors.New(
			"TUN ingress pressure observation time must advance",
		)
	}
	if current.TUNBytes < previous.TUNBytes ||
		current.AdmissionWaitNanoseconds < previous.AdmissionWaitNanoseconds {
		return tunIngressPressureDelta{}, errors.New(
			"TUN ingress pressure counters moved backward",
		)
	}
	waitNanoseconds :=
		current.AdmissionWaitNanoseconds - previous.AdmissionWaitNanoseconds
	if waitNanoseconds > uint64(math.MaxInt64) {
		return tunIngressPressureDelta{}, errors.New(
			"TUN ingress admission-wait delta overflows duration",
		)
	}
	return tunIngressPressureDelta{
		Interval:              current.ObservedAt.Sub(previous.ObservedAt),
		TUNBytes:              current.TUNBytes - previous.TUNBytes,
		AdmissionWaitDuration: time.Duration(waitNanoseconds),
	}, nil
}

const (
	tcSizeKibibyte                  = 1024
	tcSizeKibibytePrintWindow       = 16
	tunRingImplementationGuardSlots = 1
)

func exactTCHTBBurstBytes(sizeBytes int) (int, error) {
	remainder := sizeBytes % tcSizeKibibyte
	increment := 0
	switch {
	case remainder > 0 && remainder < tcSizeKibibytePrintWindow:
		increment = tcSizeKibibytePrintWindow - remainder
	case remainder > tcSizeKibibyte-tcSizeKibibytePrintWindow:
		increment = tcSizeKibibyte - remainder
	}
	if sizeBytes > math.MaxInt-increment {
		return 0, errors.New("TUN AQM HTB burst normalization overflows int")
	}
	return sizeBytes + increment, nil
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
	maxSegments := wireBudget / maxWireDatagram
	if maxSegments < 1 {
		maxSegments = 1
	}
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
	serviceNanoseconds := math.Ceil(
		float64(wholeBatchBytes) / perPeerRate * float64(time.Second),
	)
	if serviceNanoseconds > float64(math.MaxInt64) {
		return engineOutboundBounds{}, errors.New(
			"engine outbound maximum batch service time overflows duration",
		)
	}
	admissionLimit := dataBudgetBytes + wholeBatchBytes
	serviceTime := time.Duration(serviceNanoseconds)
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
	gsoMaxSizeBytes int,
	maximumBatchServiceTime time.Duration,
	currentPacketBytes int,
	maximumPacketBytes int,
) (tunAQMQueueGeometry, error) {
	if math.IsNaN(aggregateRateBytesPerSecond) ||
		math.IsInf(aggregateRateBytesPerSecond, 0) ||
		aggregateRateBytesPerSecond <= 0 ||
		admissionLimitBytes <= 0 ||
		peerCount <= 0 ||
		gsoMaxSizeBytes <= 0 ||
		maximumBatchServiceTime <= 0 ||
		currentPacketBytes < minimumInnerPacketBytes ||
		maximumPacketBytes < currentPacketBytes ||
		maximumPacketBytes <= 0 {
		return tunAQMQueueGeometry{}, errors.New(
			"TUN AQM rate, admission limit, peer count, GSO maximum, and batch service time must be positive; current packet size must cover the minimum packet and maximum packet size must cover current",
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
	htbBurstBytes, err := exactTCHTBBurstBytes(gsoMaxSizeBytes)
	if err != nil {
		return tunAQMQueueGeometry{}, err
	}
	arrivalBytesFloat := aggregateRateBytesPerSecond *
		maximumBatchServiceTime.Seconds()
	if arrivalBytesFloat > float64(maxInt) {
		return tunAQMQueueGeometry{}, errors.New(
			"TUN AQM batch-service arrivals overflow int",
		)
	}
	arrivalBytes := int(math.Ceil(arrivalBytesFloat))
	if arrivalBytes > maxInt-htbBurstBytes {
		return tunAQMQueueGeometry{}, errors.New(
			"TUN AQM transient handoff byte bound overflows int",
		)
	}
	handoffBytes := arrivalBytes + htbBurstBytes
	if handoffBytes > maxInt-(currentPacketBytes-1) {
		return tunAQMQueueGeometry{}, errors.New(
			"TUN AQM transient handoff slot rounding overflows int",
		)
	}
	minimumPacketSlots := (handoffBytes + minimumInnerPacketBytes - 1) /
		minimumInnerPacketBytes
	if minimumPacketSlots > maxInt-tunRingImplementationGuardSlots {
		return tunAQMQueueGeometry{}, errors.New(
			"TUN AQM ptr-ring implementation guard overflows int",
		)
	}
	ringSlots := minimumPacketSlots + tunRingImplementationGuardSlots
	if uint64(ringSlots) > uint64(^uint32(0)) {
		return tunAQMQueueGeometry{}, errors.New(
			"TUN AQM ptr-ring slot count overflows Linux tx_queue_len",
		)
	}
	fullMTUHandoffSlots := (handoffBytes + currentPacketBytes - 1) /
		currentPacketBytes
	fullMTUServiceSlots :=
		fullMTUHandoffSlots + tunRingImplementationGuardSlots
	if fullMTUServiceSlots > maxInt/maximumPacketBytes ||
		serviceSlots > maxInt/minimumInnerPacketBytes {
		return tunAQMQueueGeometry{}, errors.New(
			"TUN AQM total service window overflows int",
		)
	}
	ringWindowBytes := fullMTUServiceSlots * maximumPacketBytes
	leafWindowBytes := serviceSlots * minimumInnerPacketBytes
	if ringWindowBytes > maxInt-leafWindowBytes {
		return tunAQMQueueGeometry{}, errors.New(
			"TUN AQM total service window overflows int",
		)
	}
	totalWindowBytes := ringWindowBytes + leafWindowBytes
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
		LeafLimitBytes:     leafWindowBytes,
		HTBBurstBytes:      htbBurstBytes,
		MaximumServiceTime: maximumServiceTime,
	}, nil
}
