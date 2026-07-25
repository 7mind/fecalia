package shaper

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

// These capability gates are intentionally small: they prove that the current
// shaper cannot represent the four T312 contracts before the behavioral matrix
// exercises their ordering and lifecycle semantics.
func TestFailFirstGeneratedPriorityHasRetainedAdmission(t *testing.T) {
	if _, ok := reflect.TypeOf(Config{}).FieldByName("PriorityReserveBytes"); !ok {
		t.Fatal("generated priority has no retained P admission budget")
	}
	if _, ok := reflect.TypeOf((*Shaper)(nil)).MethodByName("WritePriority"); !ok {
		t.Fatal("generated priority still bypasses the serialized writer")
	}
}

func TestFailFirstRecoveryGroupHasOwnedAdmission(t *testing.T) {
	if _, ok := reflect.TypeOf(Config{}).FieldByName("FECGroupReserveBytes"); !ok {
		t.Fatal("the shaper has no owned Fgroup reservation")
	}
	if _, ok := reflect.TypeOf((*Shaper)(nil)).MethodByName("WriteRecovery"); !ok {
		t.Fatal("FEC groups can only enter through ordinary per-datagram admission")
	}
}

func TestFailFirstMemorySnapshotCannotStateMtotal(t *testing.T) {
	snapshot := reflect.TypeOf(Snapshot{})
	for _, field := range []string{
		"PriorityRetainedBytes",
		"FECGroupOwnedBytes",
		"MemoryBoundBytes",
		"MemoryRetainedBytes",
	} {
		if _, ok := snapshot.FieldByName(field); !ok {
			t.Errorf("Snapshot has no %s term for Mtotal accounting", field)
		}
	}
}

func TestFailFirstRecoveryCutCannotDeadlineBlockedPredecessor(t *testing.T) {
	if _, ok := reflect.TypeOf(Config{}).FieldByName("RecoveryWriteSlack"); !ok {
		t.Fatal("the writer contract has no cumulative kernel-call slack I")
	}
	if _, ok := reflect.TypeOf((*Shaper)(nil)).MethodByName("RecoveryContract"); !ok {
		t.Fatal("the shaper cannot advertise or install a recovery-cut deadline")
	}
}

func recoveryConfig() Config {
	config := validConfig()
	config.FECGroupReserveBytes = 600
	config.RecoveryWriteSlack = 10 * time.Millisecond
	return config
}

type recoveryDeadlineRecorder struct {
	mu        sync.Mutex
	installed []time.Time
	cleared   int
	aborted   []error
	notify    chan time.Time
}

func newRecoveryDeadlineRecorder() *recoveryDeadlineRecorder {
	return &recoveryDeadlineRecorder{notify: make(chan time.Time, 4)}
}

func (r *recoveryDeadlineRecorder) control() RecoveryControl {
	return RecoveryControl{
		Enabled: true,
		InstallDeadline: func(deadline time.Time) error {
			r.mu.Lock()
			r.installed = append(r.installed, deadline)
			r.mu.Unlock()
			r.notify <- deadline
			return nil
		},
		ClearDeadline: func() error {
			r.mu.Lock()
			r.cleared++
			r.mu.Unlock()
			return nil
		},
		Abort: func(err error) {
			r.mu.Lock()
			r.aborted = append(r.aborted, err)
			r.mu.Unlock()
		},
	}
}

func TestRecoveryCutOrdersPrefixPriorityAndCompleteGroups(t *testing.T) {
	clock := newFakeClock()
	var mu sync.Mutex
	var order []byte
	shaper, err := New(recoveryConfig(), clock, func(payload []byte) error {
		mu.Lock()
		order = append(order, payload[0])
		mu.Unlock()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = shaper.Close() }()

	prefix := make(chan error, 1)
	go func() {
		prefix <- shaper.WriteBatch(context.Background(), ClassData, [][]byte{
			bytesOf(1, 100),
			bytesOf(2, 100),
			bytesOf(3, 100),
		})
	}()
	waitFor(t, func() bool {
		shaper.mu.Lock()
		defer shaper.mu.Unlock()
		return len(shaper.queue) >= 2
	})
	admitted, priorityDone, err := shaper.TryWritePriority(bytesOf(4, 10), nil)
	if err != nil || !admitted {
		t.Fatalf("pre-cut priority admission = %v, %v", admitted, err)
	}

	firstControl := newRecoveryDeadlineRecorder()
	firstGroup := make(chan error, 1)
	go func() {
		_, err := shaper.WriteRecovery(context.Background(), []Datagram{
			{Class: ClassData, Payload: bytesOf(5, 10)},
			{Class: ClassData, Payload: bytesOf(6, 10)},
		}, firstControl.control())
		firstGroup <- err
	}()
	firstDeadline := waitValue(t, firstControl.notify, "first recovery cut did not install a deadline")
	if want := clock.Now().Add(310*time.Millisecond + recoveryConfig().RecoveryWriteSlack); !firstDeadline.Equal(want) {
		t.Fatalf("first cut deadline = %s, want prefixVirtualTail+I = %s", firstDeadline, want)
	}
	clock.Advance(400 * time.Millisecond)
	if err := waitResult(t, prefix); err != nil {
		t.Fatal(err)
	}
	if err := waitResult(t, priorityDone); err != nil {
		t.Fatal(err)
	}
	if err := waitResult(t, firstGroup); err != nil {
		t.Fatal(err)
	}

	admitted, postPriorityDone, err := shaper.TryWritePriority(bytesOf(7, 10), nil)
	if err != nil || !admitted {
		t.Fatalf("post-cut priority admission = %v, %v", admitted, err)
	}
	secondControl := newRecoveryDeadlineRecorder()
	secondGroup := make(chan error, 1)
	go func() {
		_, err := shaper.WriteRecovery(context.Background(), []Datagram{
			{Class: ClassData, Payload: bytesOf(8, 10)},
		}, secondControl.control())
		secondGroup <- err
	}()
	_ = waitValue(t, secondControl.notify, "second recovery cut did not install a deadline")
	clock.Advance(20 * time.Millisecond)
	if err := waitResult(t, postPriorityDone); err != nil {
		t.Fatal(err)
	}
	if err := waitResult(t, secondGroup); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if got, want := order, []byte{1, 2, 3, 4, 5, 6, 7, 8}; !reflect.DeepEqual(got, want) {
		t.Fatalf("writer order = %v, want prefix→pre-cut priority→group→post-cut priority→later group %v", got, want)
	}
}

func TestRecoveryCutDeadlineTerminatesBlockedPredecessorAndGroup(t *testing.T) {
	clock := newFakeClock()
	deadlineInstalled := make(chan struct{})
	var installOnce sync.Once
	var writes int
	shaper, err := New(recoveryConfig(), clock, func([]byte) error {
		writes++
		<-deadlineInstalled
		return context.DeadlineExceeded
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = shaper.Close() }()

	predecessor := make(chan error, 1)
	go func() {
		predecessor <- shaper.WriteBatch(context.Background(), ClassData, [][]byte{bytesOf(1, 100)})
	}()
	waitFor(t, func() bool {
		return shaper.Snapshot().InFlightBytes == 100
	})
	control := newRecoveryDeadlineRecorder()
	recovery := control.control()
	install := recovery.InstallDeadline
	recovery.InstallDeadline = func(deadline time.Time) error {
		err := install(deadline)
		installOnce.Do(func() { close(deadlineInstalled) })
		return err
	}
	group := make(chan error, 1)
	go func() {
		_, err := shaper.WriteRecovery(context.Background(), []Datagram{
			{Class: ClassData, Payload: bytesOf(2, 100)},
			{Class: ClassData, Payload: bytesOf(3, 100)},
		}, recovery)
		group <- err
	}()
	_ = waitValue(t, control.notify, "cut did not deadline the blocked predecessor")
	if err := waitResult(t, predecessor); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("predecessor error = %v, want deadline exceeded", err)
	}
	if err := waitResult(t, group); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("group error = %v, want predecessor's terminal deadline", err)
	}
	if writes != 1 {
		t.Fatalf("writer calls = %d, want only the blocked predecessor and no tranche retry", writes)
	}
	control.mu.Lock()
	defer control.mu.Unlock()
	if len(control.installed) != 1 || control.cleared != 1 || len(control.aborted) != 1 {
		t.Fatalf("deadline lifecycle installs/clears/aborts = %d/%d/%d, want 1/1/1",
			len(control.installed), control.cleared, len(control.aborted))
	}
}

func TestRecoveryMemoryPeakEqualsMtotalAndDrains(t *testing.T) {
	clock := newFakeClock()
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var once sync.Once
	var releaseOnce sync.Once
	shaper, err := New(recoveryConfig(), clock, func([]byte) error {
		once.Do(func() {
			close(firstStarted)
			<-releaseFirst
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		releaseOnce.Do(func() { close(releaseFirst) })
		_ = shaper.Close()
	}()

	dataDone := make(chan error, 1)
	go func() {
		dataDone <- shaper.WriteBatch(context.Background(), ClassData, [][]byte{
			bytesOf(1, 100),
			bytesOf(2, 100),
			bytesOf(3, 100),
			bytesOf(4, 100),
		})
	}()
	waitChannel(t, firstStarted, "first Lio write did not start")
	controlDone := make(chan error, 1)
	go func() {
		controlDone <- shaper.WriteBatch(context.Background(), ClassControl, [][]byte{bytesOf(5, 100)})
	}()
	waitFor(t, func() bool {
		return shaper.Snapshot().QueueControlBytes == 100
	})
	admitted, priorityDone, err := shaper.TryWritePriority(bytesOf(6, 100), nil)
	if err != nil || !admitted {
		t.Fatalf("priority admission = %v, %v", admitted, err)
	}
	cut := newRecoveryDeadlineRecorder()
	groupDone := make(chan error, 1)
	go func() {
		_, err := shaper.WriteRecovery(context.Background(), []Datagram{
			{Class: ClassData, Payload: bytesOf(7, 100)},
			{Class: ClassData, Payload: bytesOf(8, 100)},
		}, cut.control())
		groupDone <- err
	}()
	_ = waitValue(t, cut.notify, "memory-peak group was not admitted")
	time.Sleep(20 * time.Millisecond)
	snapshot := shaper.Snapshot()
	if snapshot.QueueDataBytes != 300 ||
		snapshot.QueueControlBytes != 100 ||
		snapshot.PriorityRetainedBytes != 100 ||
		snapshot.FECGroupOwnedBytes != 600 ||
		snapshot.InFlightBytes != 100 {
		t.Fatalf("did not attain independent B/C/P/Fgroup/Lio peaks: %+v", snapshot)
	}
	if snapshot.MemoryRetainedBytes != snapshot.MemoryBoundBytes {
		t.Fatalf("peak retained = %d, want exact Mtotal=%d: %+v",
			snapshot.MemoryRetainedBytes, snapshot.MemoryBoundBytes, snapshot)
	}
	if snapshot.AcceptedBytes != snapshot.EmittedBytes+
		snapshot.AsyncWriteErrorBytes+
		snapshot.AsyncWriteEMSGSIZEBytes+
		uint64(snapshot.QueueBytes+snapshot.PriorityRetainedBytes+
			snapshot.RecoveryRetainedBytes+snapshot.InFlightBytes) {
		t.Fatalf("live byte conservation failed at peak: %+v", snapshot)
	}

	releaseOnce.Do(func() { close(releaseFirst) })
	clock.Advance(2 * time.Second)
	for _, result := range []<-chan error{dataDone, controlDone, priorityDone, groupDone} {
		if err := waitResult(t, result); err != nil {
			t.Fatal(err)
		}
	}
	snapshot = shaper.Snapshot()
	if snapshot.MemoryRetainedBytes != 0 {
		t.Fatalf("retained memory after drain = %d, want 0", snapshot.MemoryRetainedBytes)
	}
	if snapshot.AcceptedBytes != snapshot.EmittedBytes+
		snapshot.AsyncWriteErrorBytes+
		snapshot.AsyncWriteEMSGSIZEBytes {
		t.Fatalf("byte conservation failed after drain: %+v", snapshot)
	}
}

func TestRecoveryDeadlineInstallFailureOwnsNothingAndAbortsOnce(t *testing.T) {
	clock := newFakeClock()
	writes := 0
	shaper, err := New(recoveryConfig(), clock, func([]byte) error {
		writes++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = shaper.Close() }()

	sentinel := errors.New("deadline install failed")
	aborts := 0
	result, err := shaper.WriteRecovery(context.Background(), []Datagram{
		{Class: ClassData, Payload: bytesOf(1, 100)},
	}, RecoveryControl{
		Enabled:         true,
		InstallDeadline: func(time.Time) error { return sentinel },
		ClearDeadline:   func() error { return nil },
		Abort: func(err error) {
			aborts++
			if !errors.Is(err, sentinel) {
				t.Errorf("abort error = %v, want %v", err, sentinel)
			}
		},
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("WriteRecovery error = %v, want %v", err, sentinel)
	}
	if result.Accepted != 0 || result.Emitted != 0 || writes != 0 || aborts != 1 {
		t.Fatalf("failed install result=%+v writes=%d aborts=%d, want no ownership/write and one abort",
			result, writes, aborts)
	}
	if snapshot := shaper.Snapshot(); snapshot.FECGroupOwnedBytes != 0 || snapshot.MemoryRetainedBytes != 0 {
		t.Fatalf("failed install retained memory: %+v", snapshot)
	}
}

func TestSecondRecoveryGroupBackpressuresAndCancellationIsPreOwnership(t *testing.T) {
	clock := newFakeClock()
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	shaper, err := New(recoveryConfig(), clock, func([]byte) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = shaper.Close() }()

	firstDone := make(chan error, 1)
	go func() {
		_, err := shaper.WriteRecovery(context.Background(), []Datagram{{
			Class:   ClassData,
			Payload: bytesOf(1, 100),
			Write: func([]byte) error {
				close(firstStarted)
				<-releaseFirst
				return nil
			},
		}}, newRecoveryDeadlineRecorder().control())
		firstDone <- err
	}()
	_ = waitValue(t, firstStarted, "first recovery group did not reach writer")

	secondPayload := bytesOf(2, 100)
	ctx, cancel := context.WithCancel(context.Background())
	secondDone := make(chan error, 1)
	go func() {
		_, err := shaper.WriteRecovery(ctx, []Datagram{{
			Class:   ClassData,
			Payload: secondPayload,
		}}, newRecoveryDeadlineRecorder().control())
		secondDone <- err
	}()
	waitFor(t, func() bool {
		return shaper.Snapshot().AdmissionWaits > 0
	})
	secondPayload[0] = 9
	cancel()
	if err := waitResult(t, secondDone); !errors.Is(err, context.Canceled) {
		t.Fatalf("second group error = %v, want pre-ownership cancellation", err)
	}
	if snapshot := shaper.Snapshot(); snapshot.FECGroupOwnedBytes != recoveryConfig().FECGroupReserveBytes {
		t.Fatalf("second group coexisted with first: %+v", snapshot)
	}
	close(releaseFirst)
	if err := waitResult(t, firstDone); err != nil {
		t.Fatal(err)
	}
}

func TestPriorityCoalescenceDoesNotInvokeGenerator(t *testing.T) {
	config := recoveryConfig()
	clock := newFakeClock()
	started := make(chan struct{})
	release := make(chan struct{})
	shaper, err := New(config, clock, func([]byte) error {
		select {
		case <-started:
		default:
			close(started)
			<-release
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = shaper.Close() }()

	dataDone := make(chan error, 1)
	go func() {
		_, writeErr := shaper.WriteDatagrams(context.Background(), []Datagram{{
			Class:   ClassData,
			Payload: bytesOf(0, 1),
		}})
		dataDone <- writeErr
	}()
	_ = waitValue(t, started, "predecessor write did not start")

	admitted, done, err := shaper.TryWritePriorityGenerated(
		config.PriorityReserveBytes,
		func() ([]byte, WriteFunc, error) {
			return bytesOf(1, config.PriorityReserveBytes), nil, nil
		},
	)
	if err != nil || !admitted {
		t.Fatalf("first priority admission = %v, %v", admitted, err)
	}
	generated := false
	admitted, _, err = shaper.TryWritePriorityGenerated(1, func() ([]byte, WriteFunc, error) {
		generated = true
		return bytesOf(2, 1), nil, nil
	})
	if err != nil || admitted {
		t.Fatalf("saturated priority admission = %v, %v; want coalesced", admitted, err)
	}
	if generated {
		t.Fatal("coalesced priority admission consumed generation side effects")
	}

	close(release)
	clock.Advance(time.Second)
	if err := waitResult(t, dataDone); err != nil {
		t.Fatal(err)
	}
	if err := waitResult(t, done); err != nil {
		t.Fatal(err)
	}
}
