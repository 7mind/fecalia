package shaper

import (
	"context"
	"errors"
	"math"
	"syscall"
	"testing"
	"time"
)

func TestSnapshotReportsBackpressureAndCancellationWithoutSheds(t *testing.T) {
	clock := newFakeClock()
	config := validConfig()
	config.DataBudgetBytes = 100

	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	writes := 0
	shaper, err := New(config, clock, func([]byte) error {
		writes++
		if writes == 1 {
			close(firstStarted)
			<-releaseFirst
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = shaper.Close() }()

	first := make(chan error, 1)
	go func() {
		first <- shaper.WriteBatch(context.Background(), ClassData, [][]byte{make([]byte, 100)})
	}()
	waitChannel(t, firstStarted, "first datagram did not enter the writer")

	second := make(chan error, 1)
	go func() {
		second <- shaper.WriteBatch(context.Background(), ClassData, [][]byte{make([]byte, 100)})
	}()
	waitFor(t, func() bool {
		snapshot := shaper.Snapshot()
		return snapshot.QueueDataBytes == config.DataBudgetBytes
	})

	ctx, cancel := context.WithCancel(context.Background())
	blocked := make(chan struct {
		result BatchResult
		err    error
	}, 1)
	go func() {
		result, err := shaper.WriteDatagrams(ctx, []Datagram{
			{Class: ClassData, Payload: []byte{1}},
			{Class: ClassData, Payload: []byte{2}},
			{Class: ClassData, Payload: []byte{3}},
		})
		blocked <- struct {
			result BatchResult
			err    error
		}{result: result, err: err}
	}()
	waitFor(t, func() bool {
		return shaper.Snapshot().AdmissionWaits == 1
	})

	snapshot := shaper.Snapshot()
	if snapshot.QueueDataBytes != config.DataBudgetBytes ||
		snapshot.QueueControlBytes != 0 ||
		snapshot.QueueBytes != config.DataBudgetBytes ||
		snapshot.InFlightBytes != config.MaxDatagramBytes {
		t.Fatalf("full-B occupancy snapshot = %+v", snapshot)
	}
	if snapshot.QueueDataBytes > snapshot.DataBudgetBytes ||
		snapshot.QueueControlBytes > snapshot.ControlReserveBytes ||
		snapshot.QueueBytes > snapshot.QueueBudgetBytes {
		t.Fatalf("queue occupancy exceeds configured budgets: %+v", snapshot)
	}
	if snapshot.ScheduledDelay <= 0 {
		t.Fatalf("scheduled delay = %v, want positive", snapshot.ScheduledDelay)
	}
	if snapshot.AsyncWriteErrors != 0 ||
		snapshot.AsyncWriteEMSGSIZEErrors != 0 ||
		snapshot.AdmissionCanceledDatagrams != 0 {
		t.Fatalf("backpressure was misclassified as failure: %+v", snapshot)
	}

	clock.MoveWithoutFiring(50 * time.Millisecond)
	cancel()
	outcome := waitValue(t, blocked, "cancelled admission did not return")
	if !errors.Is(outcome.err, context.Canceled) {
		t.Fatalf("admission error = %v, want context cancellation", outcome.err)
	}
	if outcome.result.Accepted != 0 {
		t.Fatalf("accepted = %d, want 0", outcome.result.Accepted)
	}
	snapshot = shaper.Snapshot()
	if snapshot.AdmissionCanceledDatagrams != 3 {
		t.Fatalf("cancelled datagrams = %d, want unaccepted suffix 3", snapshot.AdmissionCanceledDatagrams)
	}
	if snapshot.AdmissionWaitDuration != 50*time.Millisecond {
		t.Fatalf("wait duration = %v, want 50ms", snapshot.AdmissionWaitDuration)
	}

	close(releaseFirst)
	if err := waitResult(t, first); err != nil {
		t.Fatal(err)
	}
	clock.Advance(50 * time.Millisecond)
	if err := waitResult(t, second); err != nil {
		t.Fatal(err)
	}
	snapshot = shaper.Snapshot()
	if snapshot.AcceptedBytes != 200 || snapshot.EmittedBytes != 200 {
		t.Fatalf("accepted/emitted bytes = %d/%d, want 200/200", snapshot.AcceptedBytes, snapshot.EmittedBytes)
	}
}

func TestSnapshotCountsReservedPlaceholderAsAcceptedBytes(t *testing.T) {
	clock := newFakeClock()
	shaper, err := New(validConfig(), clock, func([]byte) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = shaper.Close() }()

	reserved, err := shaper.reserve(context.Background(), ClassData, 100)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := shaper.Snapshot()
	if snapshot.QueueBytes != 100 ||
		snapshot.AcceptedBytes != 100 ||
		snapshot.AcceptedBytes != uint64(snapshot.QueueBytes+snapshot.InFlightBytes) {
		t.Fatalf("reserved placeholder snapshot = %+v", snapshot)
	}

	done, err := shaper.enqueue(reserved, make([]byte, 100))
	if err != nil {
		t.Fatal(err)
	}
	if err := waitResult(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotReconcilesReservedUnpublishedSuffixAfterWriterFailure(t *testing.T) {
	clock := newFakeClock()
	sentinel := errors.New("writer failed")
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	shaper, err := New(validConfig(), clock, func([]byte) error {
		close(firstStarted)
		<-releaseFirst
		return sentinel
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = shaper.Close() }()

	batch := &batchState{failedIndex: -1}
	first, err := shaper.reserveDatagram(context.Background(), ClassData, 100, batch, 0)
	if err != nil {
		t.Fatal(err)
	}
	firstDone, err := shaper.enqueue(first, make([]byte, 100))
	if err != nil {
		t.Fatal(err)
	}
	waitChannel(t, firstStarted, "first datagram did not enter writer")

	second, err := shaper.reserveDatagram(context.Background(), ClassData, 100, batch, 1)
	if err != nil {
		t.Fatal(err)
	}
	close(releaseFirst)
	if err := waitResult(t, firstDone); !errors.Is(err, sentinel) {
		t.Fatalf("first writer error = %v, want %v", err, sentinel)
	}
	if err := waitResult(t, second.datagram.done); !errors.Is(err, sentinel) {
		t.Fatalf("reserved suffix error = %v, want %v", err, sentinel)
	}

	snapshot := shaper.Snapshot()
	if batch.reserved != 2 ||
		batch.accepted != 1 ||
		snapshot.AcceptedBytes != 200 ||
		snapshot.EmittedBytes != 0 ||
		snapshot.AsyncWriteErrors != 1 ||
		snapshot.AsyncWriteErrorBytes != 200 {
		t.Fatalf("reserved suffix reconciliation = batch %+v snapshot %+v", batch, snapshot)
	}
}

func TestSnapshotReportsPriorityDebtAndNetRateDelayBound(t *testing.T) {
	clock := newFakeClock()
	config := validConfig()
	config.RateBytesPerSecond = 1_000
	config.PriorityRateBytesPerSecond = 900
	config.PriorityBurstBytes = 100
	shaper, err := New(config, clock, func([]byte) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = shaper.Close() }()

	if err := shaper.AccountPriority(100); err != nil {
		t.Fatal(err)
	}
	snapshot := shaper.Snapshot()
	if snapshot.OuterPriorityBytes != 100 ||
		snapshot.PriorityDebtBytes != 100 ||
		snapshot.PriorityRateBytesPerSecond != 900 ||
		snapshot.PriorityBurstBytes != 100 {
		t.Fatalf("priority snapshot = %+v", snapshot)
	}
	want := 2 * time.Second
	if snapshot.PriorityDelayBound != want {
		t.Fatalf(
			"Dp = %v, want (P0+Pburst)/(R-Rp) = %v",
			snapshot.PriorityDelayBound,
			want,
		)
	}
}

func TestSnapshotCancellationCountsOnlyUnacceptedSuffix(t *testing.T) {
	clock := newFakeClock()
	config := validConfig()
	config.DataBudgetBytes = 100
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	firstReleased := false
	defer func() {
		if !firstReleased {
			close(releaseFirst)
		}
	}()
	writes := 0
	shaper, err := New(config, clock, func([]byte) error {
		writes++
		if writes == 1 {
			close(firstStarted)
			<-releaseFirst
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = shaper.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	type outcome struct {
		result BatchResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := shaper.WriteDatagrams(ctx, []Datagram{
			{Class: ClassData, Payload: make([]byte, 100)},
			{Class: ClassData, Payload: make([]byte, 100)},
			{Class: ClassData, Payload: make([]byte, 100)},
		})
		done <- outcome{result: result, err: err}
	}()
	waitChannel(t, firstStarted, "first accepted datagram did not enter writer")
	waitFor(t, func() bool {
		snapshot := shaper.Snapshot()
		return snapshot.AcceptedBytes == 200 && snapshot.AdmissionWaits >= 1
	})
	cancel()
	close(releaseFirst)
	firstReleased = true
	clock.Advance(100 * time.Millisecond)

	got := waitValue(t, done, "partially accepted batch did not complete")
	if !errors.Is(got.err, context.Canceled) ||
		got.result.Accepted != 2 ||
		got.result.Emitted != 2 {
		t.Fatalf("partial cancellation outcome = %+v, error %v", got.result, got.err)
	}
	snapshot := shaper.Snapshot()
	if snapshot.AdmissionCanceledDatagrams != 1 {
		t.Fatalf("canceled datagrams = %d, want only unaccepted suffix 1", snapshot.AdmissionCanceledDatagrams)
	}
}

func TestSnapshotReconcilesAcceptedEmittedAndAsyncWriteErrors(t *testing.T) {
	clock := newFakeClock()
	generic := errors.New("writer failed")
	shaper, err := New(validConfig(), clock, func(datagram []byte) error {
		switch datagram[0] {
		case 1:
			return generic
		case 2:
			return syscall.EMSGSIZE
		default:
			return nil
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = shaper.Close() }()

	write := func(marker byte, size int, advance time.Duration) error {
		result := make(chan error, 1)
		go func() {
			result <- shaper.WriteBatch(context.Background(), ClassData, [][]byte{bytesOf(marker, size)})
		}()
		if advance > 0 {
			waitFor(t, func() bool {
				return clock.HasActiveTimerAt(clock.Now().Add(advance))
			})
			clock.Advance(advance)
		}
		return waitResult(t, result)
	}
	if err := write(1, 10, 0); !errors.Is(err, generic) {
		t.Fatalf("generic write error = %v", err)
	}
	if err := write(2, 20, 10*time.Millisecond); !errors.Is(err, syscall.EMSGSIZE) {
		t.Fatalf("EMSGSIZE write error = %v", err)
	}
	if err := write(3, 30, 20*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := shaper.AccountPriority(50); err != nil {
		t.Fatal(err)
	}

	snapshot := shaper.Snapshot()
	if snapshot.AcceptedBytes != 60 ||
		snapshot.EmittedBytes != 30 ||
		snapshot.AsyncWriteErrors != 1 ||
		snapshot.AsyncWriteErrorBytes != 10 ||
		snapshot.AsyncWriteEMSGSIZEErrors != 1 ||
		snapshot.AsyncWriteEMSGSIZEBytes != 20 ||
		snapshot.OuterPriorityBytes != 50 {
		t.Fatalf("byte/error snapshot = %+v", snapshot)
	}
	reconciled := snapshot.EmittedBytes +
		snapshot.AsyncWriteErrorBytes +
		snapshot.AsyncWriteEMSGSIZEBytes +
		uint64(snapshot.QueueBytes+snapshot.InFlightBytes)
	if snapshot.AcceptedBytes != reconciled {
		t.Fatalf("accepted bytes = %d, reconciled terminal/current bytes = %d", snapshot.AcceptedBytes, reconciled)
	}
	if math.IsNaN(snapshot.PriorityDebtBytes) {
		t.Fatal("priority debt must remain finite")
	}
}
