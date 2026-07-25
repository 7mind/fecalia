// Package shaper provides byte-accounted, class-aware datagram shaping.
package shaper

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"syscall"
	"time"
)

var ErrClosed = errors.New("shaper closed")

type Class uint8

const (
	ClassData Class = iota
	ClassControl
)

type Config struct {
	RateBytesPerSecond         float64
	PriorityRateBytesPerSecond float64
	DataBudgetBytes            int
	ControlReserveBytes        int
	MaxDatagramBytes           int
	PriorityBurstBytes         int
	PriorityReserveBytes       int
	FECGroupReserveBytes       int
	RecoveryWriteSlack         time.Duration
}

type Timer interface {
	C() <-chan time.Time
	Stop() bool
}

type Clock interface {
	Now() time.Time
	NewTimerAt(time.Time) Timer
}

type WriteFunc func([]byte) error

type PriorityGenerator func() ([]byte, WriteFunc, error)

type Datagram struct {
	Class   Class
	Payload []byte
	Write   WriteFunc
}

type BatchResult struct {
	Accepted    int
	Emitted     int
	FailedIndex int
}

type Snapshot struct {
	QueueDataBytes             int
	QueueControlBytes          int
	QueueBytes                 int
	InFlightBytes              int
	ScheduledDelay             time.Duration
	RateBytesPerSecond         float64
	DataBudgetBytes            int
	ControlReserveBytes        int
	QueueBudgetBytes           int
	MaxDatagramBytes           int
	AcceptedBytes              uint64
	EmittedBytes               uint64
	OuterPriorityBytes         uint64
	PriorityDebtBytes          float64
	PriorityRateBytesPerSecond float64
	PriorityBurstBytes         int
	PriorityDelayBound         time.Duration
	PriorityRetainedBytes      int
	FECGroupOwnedBytes         int
	RecoveryRetainedBytes      int
	MemoryBoundBytes           int
	MemoryRetainedBytes        int
	RecoveryContractEnabled    bool
	AdmissionWaits             uint64
	AdmissionWaitDuration      time.Duration
	AdmissionCanceledDatagrams uint64
	AsyncWriteErrors           uint64
	AsyncWriteErrorBytes       uint64
	AsyncWriteEMSGSIZEErrors   uint64
	AsyncWriteEMSGSIZEBytes    uint64
}

type SystemClock struct{}

func (SystemClock) Now() time.Time {
	return time.Now()
}

func (SystemClock) NewTimerAt(deadline time.Time) Timer {
	return systemTimer{Timer: time.NewTimer(time.Until(deadline))}
}

type systemTimer struct {
	*time.Timer
}

func (t systemTimer) C() <-chan time.Time {
	return t.Timer.C
}

type queuedDatagram struct {
	class     Class
	size      int
	payload   []byte
	deadline  time.Time
	done      chan error
	ready     bool
	batch     *batchState
	index     int
	write     WriteFunc
	recovery  *recoveryState
	retention queueRetention
}

type reservation struct {
	datagram *queuedDatagram
}

type batchState struct {
	reserved    int
	accepted    int
	emitted     int
	failedIndex int
	err         error
	datagrams   []*queuedDatagram
}

type queueRetention uint8

const (
	retainClass queueRetention = iota
	retainPriority
	retainRecovery
)

type priorityWaiter struct {
	deadline time.Time
	started  time.Time
	finished bool
}

// RecoveryControl binds one complete FEC group to one exclusive socket writer
// generation. InstallDeadline must update the socket-wide absolute write
// deadline, including any write already blocked in the kernel.
type RecoveryControl struct {
	Enabled         bool
	InstallDeadline func(time.Time) error
	ClearDeadline   func() error
	Abort           func(error)
}

type RecoveryContract struct {
	Enabled           bool
	WriteSlack        time.Duration
	GroupReserveBytes int
}

type recoveryState struct {
	batch     *batchState
	control   RecoveryControl
	remaining int
	terminal  bool
	aborted   bool
}

type Shaper struct {
	config Config
	clock  Clock
	write  WriteFunc

	mu                         sync.Mutex
	queue                      []*queuedDatagram
	retainedDataBytes          int
	retainedControlBytes       int
	retainedPriorityBytes      int
	fecGroupOwnedBytes         int
	retainedRecoveryWireBytes  int
	inFlightBytes              int
	tail                       time.Time
	priorityTail               time.Time
	priorityWaiters            map[*priorityWaiter]struct{}
	acceptedBytes              uint64
	emittedBytes               uint64
	outerPriorityBytes         uint64
	admissionWaits             uint64
	admissionWaitDuration      time.Duration
	admissionCanceledDatagrams uint64
	asyncWriteErrors           uint64
	asyncWriteErrorBytes       uint64
	asyncWriteEMSGSIZEErrors   uint64
	asyncWriteEMSGSIZEBytes    uint64
	changed                    chan struct{}
	closed                     bool
	terminalCause              error
	calls                      sync.WaitGroup
	workerDone                 chan struct{}
	recovery                   *recoveryState
}

func New(config Config, clock Clock, write WriteFunc) (*Shaper, error) {
	if err := ValidateConfig(config); err != nil {
		return nil, err
	}
	if clock == nil {
		return nil, errors.New("clock is required")
	}
	if write == nil {
		return nil, errors.New("write function is required")
	}

	shaper := &Shaper{
		config:          config,
		clock:           clock,
		write:           write,
		priorityWaiters: make(map[*priorityWaiter]struct{}),
		changed:         make(chan struct{}),
		workerDone:      make(chan struct{}),
	}
	go shaper.run()
	return shaper, nil
}

// ValidateConfig verifies that all configured byte budgets and modeled delays
// can be represented by the shaper's runtime types.
func ValidateConfig(config Config) error {
	if math.IsNaN(config.RateBytesPerSecond) ||
		math.IsInf(config.RateBytesPerSecond, 0) ||
		config.RateBytesPerSecond <= 0 {
		return errors.New("shaper rate R must be finite and positive")
	}
	if math.IsNaN(config.PriorityRateBytesPerSecond) ||
		math.IsInf(config.PriorityRateBytesPerSecond, 0) ||
		config.PriorityRateBytesPerSecond < 0 {
		return errors.New("priority rate Rp must be finite and non-negative")
	}
	if config.RateBytesPerSecond <= config.PriorityRateBytesPerSecond {
		return errors.New("shaper rate R must be greater than priority rate Rp")
	}
	if config.MaxDatagramBytes <= 0 {
		return errors.New("maximum datagram Lmax must be positive")
	}
	if config.DataBudgetBytes < config.MaxDatagramBytes {
		return errors.New("data budget B must be at least Lmax")
	}
	if config.ControlReserveBytes != config.MaxDatagramBytes {
		return errors.New("C must equal Lmax")
	}
	if config.DataBudgetBytes > math.MaxInt-config.ControlReserveBytes {
		return errors.New("queue budget B+C must fit in int")
	}
	if config.PriorityBurstBytes < 0 {
		return errors.New("priority burst Pburst must be non-negative")
	}
	if config.PriorityReserveBytes < config.PriorityBurstBytes {
		return errors.New("priority reserve P must be at least Pburst")
	}
	if config.FECGroupReserveBytes < 0 {
		return errors.New("FEC group reserve Fgroup must be non-negative")
	}
	if config.FECGroupReserveBytes == 0 && config.RecoveryWriteSlack != 0 {
		return errors.New("recovery slack I requires a positive Fgroup")
	}
	if config.FECGroupReserveBytes > 0 && config.RecoveryWriteSlack <= 0 {
		return errors.New("positive Fgroup requires recovery slack I")
	}
	memoryBound := config.DataBudgetBytes
	for _, term := range []int{
		config.ControlReserveBytes,
		config.PriorityReserveBytes,
		config.FECGroupReserveBytes,
		config.MaxDatagramBytes,
	} {
		if term > math.MaxInt-memoryBound {
			return errors.New("memory bound Mtotal must fit in int")
		}
		memoryBound += term
	}
	serializationNanoseconds := float64(config.MaxDatagramBytes) /
		config.RateBytesPerSecond * float64(time.Second)
	if math.IsInf(serializationNanoseconds, 0) ||
		serializationNanoseconds >= math.Ldexp(1, 63) {
		return errors.New("Lmax/R serialization interval exceeds time.Duration")
	}
	priorityBoundNanoseconds := 2 * float64(config.PriorityBurstBytes) /
		(config.RateBytesPerSecond - config.PriorityRateBytesPerSecond) *
		float64(time.Second)
	if math.IsInf(priorityBoundNanoseconds, 0) ||
		priorityBoundNanoseconds >= math.Ldexp(1, 63) {
		return errors.New("modeled priority delay bound exceeds time.Duration")
	}
	return nil
}

func (s *Shaper) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.clock.Now()
	scheduledDelay := s.tail.Sub(now)
	if scheduledDelay < 0 {
		scheduledDelay = 0
	}
	priorityDelay := s.priorityTail.Sub(now)
	if priorityDelay < 0 {
		priorityDelay = 0
	}
	priorityDebtBytes := priorityDelay.Seconds() * s.config.RateBytesPerSecond
	priorityDelayBound := bytesDuration(
		priorityDebtBytes+float64(s.config.PriorityBurstBytes),
		s.config.RateBytesPerSecond-s.config.PriorityRateBytesPerSecond,
	)

	return Snapshot{
		QueueDataBytes:             s.retainedDataBytes,
		QueueControlBytes:          s.retainedControlBytes,
		QueueBytes:                 s.retainedDataBytes + s.retainedControlBytes,
		InFlightBytes:              s.inFlightBytes,
		ScheduledDelay:             scheduledDelay,
		RateBytesPerSecond:         s.config.RateBytesPerSecond,
		DataBudgetBytes:            s.config.DataBudgetBytes,
		ControlReserveBytes:        s.config.ControlReserveBytes,
		QueueBudgetBytes:           s.config.DataBudgetBytes + s.config.ControlReserveBytes,
		MaxDatagramBytes:           s.config.MaxDatagramBytes,
		AcceptedBytes:              s.acceptedBytes,
		EmittedBytes:               s.emittedBytes,
		OuterPriorityBytes:         s.outerPriorityBytes,
		PriorityDebtBytes:          priorityDebtBytes,
		PriorityRateBytesPerSecond: s.config.PriorityRateBytesPerSecond,
		PriorityBurstBytes:         s.config.PriorityBurstBytes,
		PriorityDelayBound:         priorityDelayBound,
		PriorityRetainedBytes:      s.retainedPriorityBytes,
		FECGroupOwnedBytes:         s.fecGroupOwnedBytes,
		RecoveryRetainedBytes:      s.retainedRecoveryWireBytes,
		MemoryBoundBytes:           s.memoryBound(),
		MemoryRetainedBytes: s.retainedDataBytes + s.retainedControlBytes +
			s.retainedPriorityBytes + s.fecGroupOwnedBytes + s.inFlightBytes,
		RecoveryContractEnabled:    s.config.FECGroupReserveBytes > 0,
		AdmissionWaits:             s.admissionWaits,
		AdmissionWaitDuration:      s.admissionWaitDuration,
		AdmissionCanceledDatagrams: s.admissionCanceledDatagrams,
		AsyncWriteErrors:           s.asyncWriteErrors,
		AsyncWriteErrorBytes:       s.asyncWriteErrorBytes,
		AsyncWriteEMSGSIZEErrors:   s.asyncWriteEMSGSIZEErrors,
		AsyncWriteEMSGSIZEBytes:    s.asyncWriteEMSGSIZEBytes,
	}
}

func (s *Shaper) memoryBound() int {
	return s.config.DataBudgetBytes +
		s.config.ControlReserveBytes +
		s.config.PriorityReserveBytes +
		s.config.FECGroupReserveBytes +
		s.config.MaxDatagramBytes
}

func (s *Shaper) RecoveryContract() RecoveryContract {
	return RecoveryContract{
		Enabled:           s.config.FECGroupReserveBytes > 0,
		WriteSlack:        s.config.RecoveryWriteSlack,
		GroupReserveBytes: s.config.FECGroupReserveBytes,
	}
}

func bytesDuration(size float64, rate float64) time.Duration {
	nanoseconds := size / rate * float64(time.Second)
	if nanoseconds >= float64(math.MaxInt64) {
		return time.Duration(math.MaxInt64)
	}
	return time.Duration(math.Ceil(nanoseconds))
}

func (s *Shaper) WriteBatch(ctx context.Context, class Class, datagrams [][]byte) error {
	items := make([]Datagram, len(datagrams))
	for i, datagram := range datagrams {
		items[i] = Datagram{Class: class, Payload: datagram}
	}
	_, err := s.WriteDatagrams(ctx, items)
	return err
}

func (s *Shaper) WriteDatagrams(ctx context.Context, datagrams []Datagram) (BatchResult, error) {
	if ctx == nil {
		return BatchResult{FailedIndex: -1}, errors.New("context is required")
	}
	for index, datagram := range datagrams {
		if datagram.Class != ClassData && datagram.Class != ClassControl {
			return BatchResult{FailedIndex: -1}, fmt.Errorf("datagram %d has invalid class %d", index, datagram.Class)
		}
		if len(datagram.Payload) == 0 || len(datagram.Payload) > s.config.MaxDatagramBytes {
			return BatchResult{FailedIndex: -1}, fmt.Errorf(
				"datagram %d length %d outside [1,Lmax=%d]",
				index,
				len(datagram.Payload),
				s.config.MaxDatagramBytes,
			)
		}
	}
	if err := s.beginCall(); err != nil {
		return BatchResult{FailedIndex: -1}, err
	}
	defer s.calls.Done()

	batch := &batchState{failedIndex: -1}
	results := make([]<-chan error, 0, len(datagrams))
	var admissionError error
	for index, datagram := range datagrams {
		reserved, err := s.reserveDatagram(ctx, datagram.Class, len(datagram.Payload), batch, index)
		if err != nil {
			admissionError = err
			break
		}

		payload := append([]byte(nil), datagram.Payload...)
		result, err := s.enqueue(reserved, payload)
		results = append(results, result)
		if err != nil {
			admissionError = err
			break
		}
	}

	var firstError error
	for _, result := range results {
		if err := <-result; err != nil && firstError == nil {
			firstError = err
		}
	}
	if firstError == nil {
		firstError = admissionError
	}

	s.mu.Lock()
	if admissionError != nil &&
		(errors.Is(admissionError, context.Canceled) ||
			errors.Is(admissionError, context.DeadlineExceeded)) {
		s.admissionCanceledDatagrams += uint64(len(datagrams) - batch.reserved)
	}
	result := BatchResult{
		Accepted:    batch.accepted,
		Emitted:     batch.emitted,
		FailedIndex: batch.failedIndex,
	}
	s.mu.Unlock()
	return result, firstError
}

// WritePriority waits cancellably for retained generated-priority capacity,
// copies only after reserving it, and emits through the same serialized writer
// as DATA. A non-nil per-datagram writer captures an explicit destination (for
// reactive echoes); nil uses the path writer.
func (s *Shaper) WritePriority(ctx context.Context, payload []byte, write WriteFunc) error {
	return s.WritePriorityGenerated(ctx, len(payload), func() ([]byte, WriteFunc, error) {
		return payload, write, nil
	})
}

// WritePriorityGenerated reserves the declared exact wire length before calling
// generate. PMTU uses this ordering so waiting admission consumes neither probe
// sequence space nor an RTT timestamp.
func (s *Shaper) WritePriorityGenerated(ctx context.Context, size int, generate PriorityGenerator) error {
	if ctx == nil {
		return errors.New("context is required")
	}
	if size == 0 || size > s.config.MaxDatagramBytes {
		return fmt.Errorf("priority length %d outside [1,Lmax=%d]", size, s.config.MaxDatagramBytes)
	}
	if generate == nil {
		return errors.New("priority generator is required")
	}
	if err := s.beginCall(); err != nil {
		return err
	}
	defer s.calls.Done()

	datagram, err := s.reservePriority(ctx, size, true)
	if err != nil {
		return err
	}
	payload, write, err := generate()
	if err == nil && len(payload) != size {
		err = fmt.Errorf("generated priority length %d does not match reserved length %d", len(payload), size)
	}
	if _, finishErr := s.finishPriorityGeneration(datagram, payload, write, err); finishErr != nil {
		return finishErr
	}
	return <-datagram.done
}

// TryWritePriority admits generated priority without waiting. Ordinary probes
// and reactive echoes use it so receive/cadence goroutines never block behind P.
// The returned completion channel belongs to the admitted write.
func (s *Shaper) TryWritePriority(payload []byte, write WriteFunc) (bool, <-chan error, error) {
	return s.TryWritePriorityGenerated(len(payload), func() ([]byte, WriteFunc, error) {
		return payload, write, nil
	})
}

// TryWritePriorityGenerated reserves before generating and returns without
// calling generate when P has no capacity.
func (s *Shaper) TryWritePriorityGenerated(size int, generate PriorityGenerator) (bool, <-chan error, error) {
	if size == 0 || size > s.config.MaxDatagramBytes {
		return false, nil, fmt.Errorf("priority length %d outside [1,Lmax=%d]", size, s.config.MaxDatagramBytes)
	}
	if generate == nil {
		return false, nil, errors.New("priority generator is required")
	}
	if err := s.beginCall(); err != nil {
		return false, nil, err
	}
	datagram, err := s.reservePriority(context.Background(), size, false)
	if err != nil {
		s.calls.Done()
		if errors.Is(err, errPriorityFull) {
			return false, nil, nil
		}
		return false, nil, err
	}
	payload, write, err := generate()
	if err == nil && len(payload) != size {
		err = fmt.Errorf("generated priority length %d does not match reserved length %d", len(payload), size)
	}
	if _, finishErr := s.finishPriorityGeneration(datagram, payload, write, err); finishErr != nil {
		s.calls.Done()
		return true, nil, finishErr
	}
	completion := make(chan error, 1)
	go func() {
		completion <- <-datagram.done
		s.calls.Done()
	}()
	return true, completion, nil
}

// finishPriorityGeneration publishes a successfully generated payload only
// while its accepted reservation remains queued. Stop may retire that
// placeholder while generate runs outside the mutex; in that case the already
// published terminal completion wins over any later generator-local outcome.
func (s *Shaper) finishPriorityGeneration(
	datagram *queuedDatagram,
	payload []byte,
	write WriteFunc,
	generationErr error,
) (retired bool, err error) {
	s.mu.Lock()
	index := -1
	for candidateIndex, queued := range s.queue {
		if queued == datagram {
			index = candidateIndex
			break
		}
	}
	if index < 0 {
		s.mu.Unlock()
		return true, nil
	}
	if generationErr != nil {
		s.queue = append(s.queue[:index], s.queue[index+1:]...)
		s.releaseQueued(datagram)
		s.accountAsyncWriteFailureLocked(generationErr, datagram.size, false)
		s.notifyLocked()
		s.mu.Unlock()
		datagram.done <- generationErr
		close(datagram.done)
		return false, generationErr
	}
	datagram.payload = append([]byte(nil), payload...)
	datagram.write = write
	datagram.ready = true
	s.notifyLocked()
	s.mu.Unlock()
	return false, nil
}

// WriteRecovery takes ownership of a complete immutable single-path FEC group.
// The group consumes the sole Fgroup reservation, installs one absolute
// cutStart+I socket deadline before publishing the cut, and charges virtual
// time once while making every tranche write runnable at cutStart.
func (s *Shaper) WriteRecovery(
	ctx context.Context,
	datagrams []Datagram,
	control RecoveryControl,
) (BatchResult, error) {
	if ctx == nil {
		return BatchResult{FailedIndex: -1}, errors.New("context is required")
	}
	if !control.Enabled {
		return s.WriteDatagrams(ctx, datagrams)
	}
	if s.config.FECGroupReserveBytes == 0 {
		return BatchResult{FailedIndex: -1}, errors.New("recovery contract is disabled")
	}
	if control.InstallDeadline == nil || control.ClearDeadline == nil || control.Abort == nil {
		return BatchResult{FailedIndex: -1}, errors.New("complete recovery control is required")
	}
	total := 0
	for index, datagram := range datagrams {
		if datagram.Class != ClassData && datagram.Class != ClassControl {
			return BatchResult{FailedIndex: -1}, fmt.Errorf("datagram %d has invalid class %d", index, datagram.Class)
		}
		if len(datagram.Payload) == 0 || len(datagram.Payload) > s.config.MaxDatagramBytes {
			return BatchResult{FailedIndex: -1}, fmt.Errorf(
				"datagram %d length %d outside [1,Lmax=%d]",
				index,
				len(datagram.Payload),
				s.config.MaxDatagramBytes,
			)
		}
		if len(datagram.Payload) > math.MaxInt-total {
			return BatchResult{FailedIndex: -1}, errors.New("recovery tranche byte count overflows int")
		}
		total += len(datagram.Payload)
	}
	if len(datagrams) == 0 {
		return BatchResult{FailedIndex: -1}, nil
	}
	if total > s.config.FECGroupReserveBytes {
		return BatchResult{FailedIndex: -1}, fmt.Errorf(
			"recovery tranche %d bytes exceeds Fgroup=%d",
			total,
			s.config.FECGroupReserveBytes,
		)
	}
	if err := s.beginCall(); err != nil {
		return BatchResult{FailedIndex: -1}, err
	}
	defer s.calls.Done()

	batch := &batchState{failedIndex: -1}
	for {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return BatchResult{FailedIndex: -1}, ErrClosed
		}
		if s.recovery == nil {
			now := s.clock.Now()
			cutStart := s.tail
			if cutStart.Before(now) {
				cutStart = now
			}
			deadline := cutStart.Add(s.config.RecoveryWriteSlack)
			if err := control.InstallDeadline(deadline); err != nil {
				s.mu.Unlock()
				control.Abort(err)
				return BatchResult{FailedIndex: -1}, err
			}
			recovery := &recoveryState{
				batch:     batch,
				control:   control,
				remaining: len(datagrams),
			}
			s.recovery = recovery
			s.fecGroupOwnedBytes = s.config.FECGroupReserveBytes
			s.retainedRecoveryWireBytes += total
			s.tail = cutStart.Add(s.byteDuration(total))
			for index, item := range datagrams {
				datagram := &queuedDatagram{
					class:     item.Class,
					size:      len(item.Payload),
					payload:   item.Payload,
					deadline:  cutStart,
					done:      make(chan error, 1),
					ready:     true,
					batch:     batch,
					index:     index,
					write:     item.Write,
					recovery:  recovery,
					retention: retainRecovery,
				}
				s.queue = append(s.queue, datagram)
				batch.datagrams = append(batch.datagrams, datagram)
				batch.reserved++
				batch.accepted++
				s.acceptedBytes += uint64(datagram.size)
			}
			s.notifyLocked()
			s.mu.Unlock()
			break
		}
		changed := s.changed
		s.admissionWaits++
		started := s.clock.Now()
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			s.mu.Lock()
			s.admissionWaitDuration += s.clock.Now().Sub(started)
			s.mu.Unlock()
			return BatchResult{FailedIndex: -1}, ctx.Err()
		case <-changed:
			s.mu.Lock()
			s.admissionWaitDuration += s.clock.Now().Sub(started)
			s.mu.Unlock()
		}
	}

	var firstError error
	for _, datagram := range batch.datagrams {
		if err := <-datagram.done; err != nil && firstError == nil {
			firstError = err
		}
	}
	s.mu.Lock()
	result := BatchResult{
		Accepted:    batch.accepted,
		Emitted:     batch.emitted,
		FailedIndex: batch.failedIndex,
	}
	s.mu.Unlock()
	return result, firstError
}

var errPriorityFull = errors.New("priority reserve full")

func (s *Shaper) reservePriority(ctx context.Context, size int, wait bool) (*queuedDatagram, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return nil, ErrClosed
		}
		if err := ctx.Err(); err != nil {
			s.mu.Unlock()
			return nil, err
		}
		now := s.clock.Now()
		capacityReady := size <= s.config.PriorityReserveBytes-s.retainedPriorityBytes
		if capacityReady {
			deadline := s.tail
			if deadline.Before(now) {
				deadline = now
			}
			s.tail = deadline.Add(s.byteDuration(size))
			priorityBase := s.priorityTail
			if priorityBase.Before(now) {
				priorityBase = now
			}
			s.priorityTail = priorityBase.Add(s.byteDuration(size))
			for waiter := range s.priorityWaiters {
				if waiter.deadline.IsZero() || now.Before(waiter.deadline) {
					waiter.deadline = s.priorityTail
				}
			}
			s.retainedPriorityBytes += size
			s.acceptedBytes += uint64(size)
			s.outerPriorityBytes += uint64(size)
			datagram := &queuedDatagram{
				size:      size,
				deadline:  deadline,
				done:      make(chan error, 1),
				retention: retainPriority,
			}
			s.queue = append(s.queue, datagram)
			s.notifyLocked()
			s.mu.Unlock()
			return datagram, nil
		}
		if !wait {
			s.mu.Unlock()
			return nil, errPriorityFull
		}
		s.admissionWaits++
		started := now
		changed := s.changed
		s.mu.Unlock()

		select {
		case <-ctx.Done():
			s.mu.Lock()
			s.admissionWaitDuration += s.clock.Now().Sub(started)
			s.mu.Unlock()
			return nil, ctx.Err()
		case <-changed:
		}
		s.mu.Lock()
		s.admissionWaitDuration += s.clock.Now().Sub(started)
		s.mu.Unlock()
	}
}

func (s *Shaper) beginCall() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	s.calls.Add(1)
	return nil
}

func (s *Shaper) reserve(ctx context.Context, class Class, size int) (reservation, error) {
	return s.reserveDatagram(ctx, class, size, nil, -1)
}

func (s *Shaper) reserveDatagram(
	ctx context.Context,
	class Class,
	size int,
	batch *batchState,
	index int,
) (reservation, error) {
	var waiter *priorityWaiter
	for {
		if err := ctx.Err(); err != nil {
			return reservation{}, err
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return reservation{}, ErrClosed
		}
		if err := ctx.Err(); err != nil {
			s.mu.Unlock()
			return reservation{}, err
		}
		if batch != nil && batch.err != nil {
			err := batch.err
			s.mu.Unlock()
			return reservation{}, err
		}

		now := s.clock.Now()
		if waiter == nil &&
			(s.priorityTail.After(now) || !s.capacityAvailable(class, size)) {
			waiter = &priorityWaiter{started: now}
			s.priorityWaiters[waiter] = struct{}{}
			s.admissionWaits++
			if s.priorityTail.After(now) {
				waiter.deadline = s.priorityTail
			}
			defer s.unregisterPriorityWaiter(waiter)
		}
		priorityMatured := waiter != nil &&
			!waiter.deadline.IsZero() &&
			!now.Before(waiter.deadline)
		priorityEligible := waiter == nil ||
			waiter.deadline.IsZero() ||
			priorityMatured
		if priorityEligible && s.capacityAvailable(class, size) {
			if waiter != nil {
				s.finishAdmissionWaitLocked(waiter, now)
			}
			deadline := s.tail
			if deadline.Before(now) {
				deadline = now
			}
			s.tail = deadline.Add(s.byteDuration(size))
			s.retain(class, size)
			if batch != nil {
				batch.reserved++
			}
			s.acceptedBytes += uint64(size)
			datagram := &queuedDatagram{
				class:    class,
				size:     size,
				deadline: deadline,
				done:     make(chan error, 1),
				batch:    batch,
				index:    index,
			}
			s.queue = append(s.queue, datagram)
			s.notifyLocked()
			s.mu.Unlock()
			return reservation{datagram: datagram}, nil
		}

		changed := s.changed
		var timer Timer
		if waiter != nil && !waiter.deadline.IsZero() && !priorityMatured {
			timer = s.clock.NewTimerAt(waiter.deadline)
		}
		s.mu.Unlock()

		if timer == nil {
			select {
			case <-ctx.Done():
				return reservation{}, ctx.Err()
			case <-changed:
			}
			continue
		}

		select {
		case <-ctx.Done():
			timer.Stop()
			return reservation{}, ctx.Err()
		case <-changed:
			timer.Stop()
		case <-timer.C():
		}
	}
}

func (s *Shaper) unregisterPriorityWaiter(waiter *priorityWaiter) {
	s.mu.Lock()
	if !waiter.finished {
		s.finishAdmissionWaitLocked(waiter, s.clock.Now())
	}
	delete(s.priorityWaiters, waiter)
	s.mu.Unlock()
}

func (s *Shaper) finishAdmissionWaitLocked(waiter *priorityWaiter, now time.Time) {
	waiter.finished = true
	if now.After(waiter.started) {
		s.admissionWaitDuration += now.Sub(waiter.started)
	}
}

func (s *Shaper) capacityAvailable(class Class, size int) bool {
	switch class {
	case ClassData:
		return size <= s.config.DataBudgetBytes-s.retainedDataBytes
	case ClassControl:
		return size <= s.config.ControlReserveBytes-s.retainedControlBytes
	default:
		panic("validated class changed")
	}
}

func (s *Shaper) retain(class Class, size int) {
	switch class {
	case ClassData:
		s.retainedDataBytes += size
	case ClassControl:
		s.retainedControlBytes += size
	default:
		panic("validated class changed")
	}
}

func (s *Shaper) release(class Class, size int) {
	switch class {
	case ClassData:
		s.retainedDataBytes -= size
	case ClassControl:
		s.retainedControlBytes -= size
	default:
		panic("validated class changed")
	}
}

func (s *Shaper) enqueue(reserved reservation, payload []byte) (<-chan error, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return reserved.datagram.done, ErrClosed
	}
	if reserved.datagram.batch != nil && reserved.datagram.batch.err != nil {
		s.notifyLocked()
		return reserved.datagram.done, reserved.datagram.batch.err
	}

	reserved.datagram.payload = payload
	reserved.datagram.ready = true
	if reserved.datagram.batch != nil {
		reserved.datagram.batch.accepted++
	}
	s.notifyLocked()
	return reserved.datagram.done, nil
}

func (s *Shaper) AccountPriority(size int) error {
	if size <= 0 || size > s.config.MaxDatagramBytes {
		return fmt.Errorf("priority length %d outside [1,Lmax=%d]", size, s.config.MaxDatagramBytes)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}

	now := s.clock.Now()
	priorityBase := s.priorityTail
	if priorityBase.Before(now) {
		priorityBase = now
	}
	s.priorityTail = priorityBase.Add(s.byteDuration(size))
	for waiter := range s.priorityWaiters {
		if waiter.deadline.IsZero() || now.Before(waiter.deadline) {
			waiter.deadline = s.priorityTail
		}
	}

	tailBase := s.tail
	if tailBase.Before(now) {
		tailBase = now
	}
	s.tail = tailBase.Add(s.byteDuration(size))
	s.outerPriorityBytes += uint64(size)
	s.notifyLocked()
	return nil
}

func (s *Shaper) Close() error {
	s.Stop()
	return s.Wait()
}

// Stop atomically rejects new admission, retires every queued datagram, and
// wakes capacity and timer waiters. It does not wait for the writer currently
// in flight; Wait supplies that separate quiescence barrier.
func (s *Shaper) Stop() {
	s.StopWithError(ErrClosed)
}

// StopWithError is Stop with an originating terminal cause. Every accepted
// queued datagram completes with cause and enters exactly one terminal byte
// bucket. An in-flight writer still accounts its actual syscall result, but if
// that result is an error after cause was published, its caller receives cause.
func (s *Shaper) StopWithError(cause error) {
	if cause == nil {
		panic("shaper: StopWithError requires a cause")
	}
	var abort func()
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		s.terminalCause = cause
		for _, datagram := range s.queue {
			s.releaseQueued(datagram)
			if datagram.batch != nil && datagram.batch.err == nil {
				datagram.batch.err = cause
				datagram.batch.failedIndex = datagram.index
			}
			s.accountAsyncWriteFailureLocked(cause, datagram.size, false)
			datagram.done <- cause
			close(datagram.done)
		}
		s.queue = nil
		abort = s.terminateRecoveryLocked(cause)
		s.notifyLocked()
	}
	s.mu.Unlock()
	if abort != nil {
		abort()
	}
}

// Wait blocks until the worker and every admitted call have returned. Stop
// must run first so no new call can race the WaitGroup barrier.
func (s *Shaper) Wait() error {
	<-s.workerDone
	s.calls.Wait()
	return nil
}

func (s *Shaper) run() {
	defer close(s.workerDone)
	for {
		s.mu.Lock()
		for len(s.queue) == 0 && !s.closed {
			changed := s.changed
			s.mu.Unlock()
			<-changed
			s.mu.Lock()
		}
		if s.closed {
			s.mu.Unlock()
			return
		}

		datagram := s.queue[0]
		if datagram.batch != nil &&
			datagram.batch.err != nil &&
			datagram.index > datagram.batch.failedIndex {
			s.queue = s.queue[1:]
			s.releaseQueued(datagram)
			err := datagram.batch.err
			s.accountAsyncWriteFailureLocked(err, datagram.size, false)
			abort, err := s.finishRecoveryDatagramLocked(datagram, err)
			s.notifyLocked()
			s.mu.Unlock()
			if abort != nil {
				abort()
			}
			datagram.done <- err
			close(datagram.done)
			continue
		}
		if !datagram.ready {
			changed := s.changed
			s.mu.Unlock()
			<-changed
			continue
		}
		delay := datagram.deadline.Sub(s.clock.Now())
		if delay > 0 {
			changed := s.changed
			timer := s.clock.NewTimerAt(datagram.deadline)
			s.mu.Unlock()
			select {
			case <-changed:
				timer.Stop()
			case <-timer.C():
			}
			continue
		}

		s.queue = s.queue[1:]
		s.releaseQueued(datagram)
		s.inFlightBytes = datagram.size
		s.notifyLocked()
		s.mu.Unlock()

		write := datagram.write
		if write == nil {
			write = s.write
		}
		actualErr := write(datagram.payload)

		s.mu.Lock()
		s.inFlightBytes = 0
		err := actualErr
		if actualErr == nil {
			s.emittedBytes += uint64(datagram.size)
		} else {
			s.accountAsyncWriteFailureLocked(actualErr, datagram.size, true)
			if s.closed && s.terminalCause != nil {
				err = s.terminalCause
			}
		}
		if datagram.batch != nil {
			if err == nil {
				datagram.batch.emitted++
			} else if datagram.batch.err == nil {
				datagram.batch.err = err
				datagram.batch.failedIndex = datagram.index
			}
		}
		var abort func()
		if err != nil && s.recovery != nil && datagram.recovery == nil {
			if s.recovery.batch.err == nil {
				s.recovery.batch.err = err
				s.recovery.batch.failedIndex = -1
			}
			abort = s.abortRecoveryLocked(s.recovery, err)
		}
		var finishAbort func()
		finishAbort, err = s.finishRecoveryDatagramLocked(datagram, err)
		if abort == nil {
			abort = finishAbort
		}
		s.notifyLocked()
		s.mu.Unlock()
		if abort != nil {
			abort()
		}
		datagram.done <- err
		close(datagram.done)
	}
}

func (s *Shaper) releaseQueued(datagram *queuedDatagram) {
	switch datagram.retention {
	case retainClass:
		s.release(datagram.class, datagram.size)
	case retainPriority:
		s.retainedPriorityBytes -= datagram.size
	case retainRecovery:
		s.retainedRecoveryWireBytes -= datagram.size
	default:
		panic("invalid queue retention")
	}
}

func (s *Shaper) finishRecoveryDatagramLocked(datagram *queuedDatagram, err error) (func(), error) {
	recovery := datagram.recovery
	if recovery == nil || recovery.terminal {
		return nil, err
	}
	recovery.remaining--
	if recovery.remaining != 0 {
		return nil, err
	}
	recovery.terminal = true
	clearErr := recovery.control.ClearDeadline()
	if err == nil {
		err = clearErr
	}
	if err != nil && recovery.batch.err == nil {
		recovery.batch.err = err
		recovery.batch.failedIndex = datagram.index
	}
	var abort func()
	if err != nil {
		abort = s.abortRecoveryLocked(recovery, err)
	}
	s.fecGroupOwnedBytes = 0
	if s.recovery == recovery {
		s.recovery = nil
	}
	return abort, err
}

func (s *Shaper) terminateRecoveryLocked(err error) func() {
	if s.recovery == nil || s.recovery.terminal {
		return nil
	}
	recovery := s.recovery
	recovery.terminal = true
	if recovery.batch.err == nil {
		recovery.batch.err = err
		recovery.batch.failedIndex = 0
	}
	_ = recovery.control.ClearDeadline()
	abort := s.abortRecoveryLocked(recovery, err)
	s.fecGroupOwnedBytes = 0
	s.recovery = nil
	return abort
}

func (s *Shaper) abortRecoveryLocked(recovery *recoveryState, err error) func() {
	if recovery.aborted {
		return nil
	}
	recovery.aborted = true
	return func() {
		recovery.control.Abort(err)
	}
}

func (s *Shaper) accountAsyncWriteFailureLocked(err error, size int, countError bool) {
	if errors.Is(err, syscall.EMSGSIZE) {
		if countError {
			s.asyncWriteEMSGSIZEErrors++
		}
		s.asyncWriteEMSGSIZEBytes += uint64(size)
		return
	}
	if countError {
		s.asyncWriteErrors++
	}
	s.asyncWriteErrorBytes += uint64(size)
}

func (s *Shaper) notifyLocked() {
	close(s.changed)
	s.changed = make(chan struct{})
}

func (s *Shaper) byteDuration(size int) time.Duration {
	nanoseconds := float64(size) / s.config.RateBytesPerSecond * float64(time.Second)
	return time.Duration(math.Ceil(nanoseconds))
}
