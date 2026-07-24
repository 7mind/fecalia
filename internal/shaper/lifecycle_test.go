package shaper

import (
	"context"
	"errors"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

func waitForAcceptedQueuedDatagrams(t *testing.T, s *Shaper, accepted, queued int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		s.mu.Lock()
		ready := len(s.queue) == queued &&
			s.queue[0].batch != nil &&
			s.queue[0].batch.accepted == accepted &&
			s.inFlightBytes > 0
		for _, datagram := range s.queue {
			ready = ready && datagram.ready
		}
		s.mu.Unlock()
		if ready {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("shaper did not reach accepted=%d with %d ready queued datagrams", accepted, queued)
		}
		runtime.Gosched()
	}
}

func assertEmptyAfterWait(t *testing.T, s *Shaper) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.queue) != 0 ||
		s.retainedDataBytes != 0 ||
		s.retainedControlBytes != 0 ||
		s.inFlightBytes != 0 ||
		len(s.priorityWaiters) != 0 {
		t.Fatalf(
			"retained state after Wait: queue=%d data=%d control=%d inFlight=%d waiters=%d",
			len(s.queue),
			s.retainedDataBytes,
			s.retainedControlBytes,
			s.inFlightBytes,
			len(s.priorityWaiters),
		)
	}
}

func TestStopRetiresQueueBeforeWaitAndRejectsNewAdmission(t *testing.T) {
	const lmax = 128
	writerEntered := make(chan struct{})
	releaseWriter := make(chan struct{})
	var writes atomic.Int32
	s, err := New(Config{
		RateBytesPerSecond:         1,
		PriorityRateBytesPerSecond: 0,
		DataBudgetBytes:            2 * lmax,
		ControlReserveBytes:        lmax,
		MaxDatagramBytes:           lmax,
	}, SystemClock{}, func([]byte) error {
		if writes.Add(1) == 1 {
			close(writerEntered)
			<-releaseWriter
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	type callResult struct {
		result BatchResult
		err    error
	}
	callDone := make(chan callResult, 1)
	go func() {
		result, err := s.WriteDatagrams(context.Background(), []Datagram{
			{Class: ClassData, Payload: make([]byte, lmax)},
			{Class: ClassData, Payload: make([]byte, lmax)},
		})
		callDone <- callResult{result: result, err: err}
	}()
	select {
	case <-writerEntered:
	case <-time.After(time.Second):
		t.Fatal("first writer did not enter")
	}
	waitForAcceptedQueuedDatagrams(t, s, 2, 1)

	s.Stop()
	if _, err := s.WriteDatagrams(context.Background(), []Datagram{{
		Class:   ClassData,
		Payload: []byte{1},
	}}); !errors.Is(err, ErrClosed) {
		t.Fatalf("post-Stop admission error = %v, want ErrClosed", err)
	}
	select {
	case got := <-callDone:
		t.Fatalf("admitted call returned before in-flight writer quiesced: %+v", got)
	default:
	}

	close(releaseWriter)
	select {
	case got := <-callDone:
		if !errors.Is(got.err, ErrClosed) {
			t.Fatalf("queued call error = %v, want ErrClosed", got.err)
		}
		if got.result.Accepted != 2 || got.result.Emitted != 1 || got.result.FailedIndex != 1 {
			t.Fatalf("queued call result = %+v, want accepted=2 emitted=1 failedIndex=1", got.result)
		}
	case <-time.After(time.Second):
		t.Fatal("admitted call did not finish after writer release")
	}
	if err := s.Wait(); err != nil {
		t.Fatal(err)
	}
	if got := writes.Load(); got != 1 {
		t.Fatalf("writer calls = %d, want only the in-flight datagram", got)
	}
	assertEmptyAfterWait(t, s)
}

func TestRepeatedStopWaitReturnsRetainedStateToBaseline(t *testing.T) {
	const (
		lmax       = 4096
		iterations = 50
	)
	for i := 0; i < iterations; i++ {
		writerEntered := make(chan struct{})
		releaseWriter := make(chan struct{})
		s, err := New(Config{
			RateBytesPerSecond:         1,
			PriorityRateBytesPerSecond: 0,
			DataBudgetBytes:            3 * lmax,
			ControlReserveBytes:        lmax,
			MaxDatagramBytes:           lmax,
		}, SystemClock{}, func([]byte) error {
			select {
			case <-writerEntered:
			default:
				close(writerEntered)
			}
			<-releaseWriter
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		callDone := make(chan error, 1)
		go func() {
			_, err := s.WriteDatagrams(context.Background(), []Datagram{
				{Class: ClassData, Payload: make([]byte, lmax)},
				{Class: ClassData, Payload: make([]byte, lmax)},
				{Class: ClassControl, Payload: make([]byte, lmax)},
			})
			callDone <- err
		}()
		select {
		case <-writerEntered:
		case <-time.After(time.Second):
			t.Fatalf("iteration %d: writer did not enter", i)
		}
		waitForAcceptedQueuedDatagrams(t, s, 3, 2)
		s.Stop()
		close(releaseWriter)
		if err := <-callDone; !errors.Is(err, ErrClosed) {
			t.Fatalf("iteration %d: call error = %v, want ErrClosed", i, err)
		}
		if err := s.Wait(); err != nil {
			t.Fatalf("iteration %d: Wait: %v", i, err)
		}
		assertEmptyAfterWait(t, s)
	}
}

func TestStopWakesPriorityWaiterAndRetiresItsTimer(t *testing.T) {
	clock := newFakeClock()
	var writes atomic.Int32
	s, err := New(validConfig(), clock, func([]byte) error {
		writes.Add(1)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AccountPriority(100); err != nil {
		t.Fatal(err)
	}

	callDone := make(chan error, 1)
	go func() {
		callDone <- s.WriteBatch(context.Background(), ClassData, [][]byte{{1}})
	}()
	priorityDeadline := clock.Now().Add(100 * time.Millisecond)
	waitFor(t, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		if len(s.priorityWaiters) != 1 {
			return false
		}
		for waiter := range s.priorityWaiters {
			if !waiter.deadline.Equal(priorityDeadline) {
				return false
			}
		}
		return clock.HasActiveTimerAt(priorityDeadline)
	})

	s.Stop()
	waitDone := make(chan error, 1)
	go func() {
		waitDone <- s.Wait()
	}()
	if err := waitResult(t, callDone); !errors.Is(err, ErrClosed) {
		t.Fatalf("priority-blocked call error = %v, want ErrClosed", err)
	}
	if err := waitResult(t, waitDone); err != nil {
		t.Fatal(err)
	}
	assertEmptyAfterWait(t, s)
	clock.mu.Lock()
	retainedTimers := len(clock.timers)
	activeTimers := 0
	for timer := range clock.timers {
		if !timer.stopped && !timer.fired {
			activeTimers++
		}
	}
	clock.mu.Unlock()
	if retainedTimers != 0 || activeTimers != 0 {
		t.Fatalf("timers after Stop/Wait = retained:%d active:%d, want 0/0", retainedTimers, activeTimers)
	}
	if got := writes.Load(); got != 0 {
		t.Fatalf("writer calls = %d, want 0 for priority-blocked admission", got)
	}
}
