// Package shaper provides byte-accounted, class-aware datagram shaping.
package shaper

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
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

type Datagram struct {
	Class   Class
	Payload []byte
}

type BatchResult struct {
	Accepted    int
	Emitted     int
	FailedIndex int
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
	class    Class
	size     int
	payload  []byte
	deadline time.Time
	done     chan error
	ready    bool
	batch    *batchState
	index    int
}

type reservation struct {
	datagram *queuedDatagram
}

type batchState struct {
	accepted    int
	emitted     int
	failedIndex int
	err         error
}

type priorityWaiter struct {
	deadline time.Time
}

type Shaper struct {
	config Config
	clock  Clock
	write  WriteFunc

	mu                   sync.Mutex
	queue                []*queuedDatagram
	retainedDataBytes    int
	retainedControlBytes int
	inFlightBytes        int
	tail                 time.Time
	priorityTail         time.Time
	priorityWaiters      map[*priorityWaiter]struct{}
	changed              chan struct{}
	closed               bool
	calls                sync.WaitGroup
	workerDone           chan struct{}
}

func New(config Config, clock Clock, write WriteFunc) (*Shaper, error) {
	if err := validateConfig(config); err != nil {
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

func validateConfig(config Config) error {
	if math.IsNaN(config.RateBytesPerSecond) ||
		math.IsInf(config.RateBytesPerSecond, 0) ||
		config.RateBytesPerSecond <= 0 {
		return errors.New("R must be finite and positive")
	}
	if math.IsNaN(config.PriorityRateBytesPerSecond) ||
		math.IsInf(config.PriorityRateBytesPerSecond, 0) ||
		config.PriorityRateBytesPerSecond < 0 {
		return errors.New("Rp must be finite and non-negative")
	}
	if config.RateBytesPerSecond <= config.PriorityRateBytesPerSecond {
		return errors.New("R must be greater than Rp")
	}
	if config.MaxDatagramBytes <= 0 {
		return errors.New("Lmax must be positive")
	}
	if config.DataBudgetBytes < config.MaxDatagramBytes {
		return errors.New("B must be at least Lmax")
	}
	if config.ControlReserveBytes != config.MaxDatagramBytes {
		return errors.New("C must equal Lmax")
	}
	if config.PriorityBurstBytes < 0 {
		return errors.New("Pburst must be non-negative")
	}
	serializationNanoseconds := float64(config.MaxDatagramBytes) /
		config.RateBytesPerSecond * float64(time.Second)
	if math.IsInf(serializationNanoseconds, 0) ||
		serializationNanoseconds >= math.Ldexp(1, 63) {
		return errors.New("Lmax/R serialization interval exceeds time.Duration")
	}
	return nil
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
	result := BatchResult{
		Accepted:    batch.accepted,
		Emitted:     batch.emitted,
		FailedIndex: batch.failedIndex,
	}
	s.mu.Unlock()
	return result, firstError
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
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return reservation{}, ErrClosed
		}
		if batch != nil && batch.err != nil {
			err := batch.err
			s.mu.Unlock()
			return reservation{}, err
		}

		now := s.clock.Now()
		if waiter == nil &&
			(s.priorityTail.After(now) || !s.capacityAvailable(class, size)) {
			waiter = &priorityWaiter{}
			s.priorityWaiters[waiter] = struct{}{}
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
			deadline := s.tail
			if deadline.Before(now) {
				deadline = now
			}
			s.tail = deadline.Add(s.byteDuration(size))
			s.retain(class, size)
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
	delete(s.priorityWaiters, waiter)
	s.mu.Unlock()
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
	s.notifyLocked()
	return nil
}

func (s *Shaper) Close() error {
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		for _, datagram := range s.queue {
			s.release(datagram.class, datagram.size)
			datagram.done <- ErrClosed
			close(datagram.done)
		}
		s.queue = nil
		s.notifyLocked()
	}
	workerDone := s.workerDone
	s.mu.Unlock()

	<-workerDone
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
			s.release(datagram.class, datagram.size)
			err := datagram.batch.err
			s.notifyLocked()
			s.mu.Unlock()
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
		s.release(datagram.class, datagram.size)
		s.inFlightBytes = datagram.size
		s.notifyLocked()
		s.mu.Unlock()

		err := s.write(datagram.payload)

		s.mu.Lock()
		s.inFlightBytes = 0
		if datagram.batch != nil {
			if err == nil {
				datagram.batch.emitted++
			} else if datagram.batch.err == nil {
				datagram.batch.err = err
				datagram.batch.failedIndex = datagram.index
			}
		}
		s.notifyLocked()
		s.mu.Unlock()
		datagram.done <- err
		close(datagram.done)
	}
}

func (s *Shaper) notifyLocked() {
	close(s.changed)
	s.changed = make(chan struct{})
}

func (s *Shaper) byteDuration(size int) time.Duration {
	nanoseconds := float64(size) / s.config.RateBytesPerSecond * float64(time.Second)
	return time.Duration(math.Ceil(nanoseconds))
}
