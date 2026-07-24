package shaper

import (
	"context"
	"errors"
	"math"
	"runtime"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"
)

type fakeClock struct {
	mu                  sync.Mutex
	now                 time.Time
	advanceAfterNextNow time.Duration
	timers              map[*fakeTimer]struct{}
}

type fakeTimer struct {
	clock   *fakeClock
	ch      chan time.Time
	at      time.Time
	stopped bool
	fired   bool
}

type observedContext struct {
	context.Context
	once         sync.Once
	doneObserved chan struct{}
}

func observeContext(ctx context.Context) *observedContext {
	return &observedContext{
		Context:      ctx,
		doneObserved: make(chan struct{}),
	}
}

func (c *observedContext) Done() <-chan struct{} {
	c.once.Do(func() {
		close(c.doneObserved)
	})
	return c.Context.Done()
}

func newFakeClock() *fakeClock {
	return &fakeClock{
		now:    time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC),
		timers: make(map[*fakeTimer]struct{}),
	}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now
	c.now = c.now.Add(c.advanceAfterNextNow)
	c.advanceAfterNextNow = 0
	return now
}

func (c *fakeClock) NewTimerAt(deadline time.Time) Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	timer := &fakeTimer{
		clock: c,
		ch:    make(chan time.Time, 1),
		at:    deadline,
	}
	c.timers[timer] = struct{}{}
	c.fireDueLocked()
	return timer
}

func (c *fakeClock) Advance(delay time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(delay)
	c.fireDueLocked()
	c.mu.Unlock()
	runtime.Gosched()
}

func (c *fakeClock) MoveWithoutFiring(delay time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(delay)
	c.mu.Unlock()
}

func (c *fakeClock) AdvanceAfterNextNow(delay time.Duration) {
	c.mu.Lock()
	c.advanceAfterNextNow = delay
	c.mu.Unlock()
}

func (c *fakeClock) FireDue() {
	c.mu.Lock()
	c.fireDueLocked()
	c.mu.Unlock()
	runtime.Gosched()
}

func (c *fakeClock) HasActiveTimerAt(deadline time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for timer := range c.timers {
		if !timer.stopped && !timer.fired && timer.at.Equal(deadline) {
			return true
		}
	}
	return false
}

func (c *fakeClock) fireDueLocked() {
	for timer := range c.timers {
		if timer.stopped || timer.fired || timer.at.After(c.now) {
			continue
		}
		timer.fired = true
		timer.ch <- c.now
	}
}

func (t *fakeTimer) C() <-chan time.Time {
	return t.ch
}

func (t *fakeTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	if t.stopped || t.fired {
		return false
	}
	t.stopped = true
	delete(t.clock.timers, t)
	return true
}

func validConfig() Config {
	return Config{
		RateBytesPerSecond:         1_000,
		PriorityRateBytesPerSecond: 200,
		DataBudgetBytes:            300,
		ControlReserveBytes:        100,
		MaxDatagramBytes:           100,
		PriorityBurstBytes:         100,
	}
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition did not become true")
		}
		runtime.Gosched()
		time.Sleep(time.Millisecond)
	}
}

func waitResult(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("operation did not complete")
		return nil
	}
}

func TestNewValidatesModelBounds(t *testing.T) {
	clock := newFakeClock()
	write := func([]byte) error { return nil }

	config := validConfig()
	config.DataBudgetBytes = config.MaxDatagramBytes
	shaper, err := New(config, clock, write)
	if err != nil {
		t.Fatalf("B == Lmax must be accepted: %v", err)
	}
	if err := shaper.Close(); err != nil {
		t.Fatal(err)
	}
	config = validConfig()
	config.DataBudgetBytes = math.MaxInt - config.ControlReserveBytes
	shaper, err = New(config, clock, write)
	if err != nil {
		t.Fatalf("exact MaxInt Q must be accepted: %v", err)
	}
	if got := shaper.Snapshot().QueueBudgetBytes; got != math.MaxInt || got < 0 {
		t.Fatalf("Q gauge = %d, want exact non-negative MaxInt=%d", got, math.MaxInt)
	}
	if err := shaper.Close(); err != nil {
		t.Fatal(err)
	}

	tests := map[string]Config{
		"B below Lmax": func() Config {
			config := validConfig()
			config.DataBudgetBytes = config.MaxDatagramBytes - 1
			return config
		}(),
		"C differs from Lmax": func() Config {
			config := validConfig()
			config.ControlReserveBytes++
			return config
		}(),
		"R equals Rp": func() Config {
			config := validConfig()
			config.RateBytesPerSecond = config.PriorityRateBytesPerSecond
			return config
		}(),
		"non-finite R": func() Config {
			config := validConfig()
			config.RateBytesPerSecond = math.Inf(1)
			return config
		}(),
		"Lmax serialization duration overflows": func() Config {
			config := validConfig()
			config.RateBytesPerSecond = 1e-12
			config.PriorityRateBytesPerSecond = 0
			return config
		}(),
		"Q overflows int": func() Config {
			config := validConfig()
			config.DataBudgetBytes = math.MaxInt
			return config
		}(),
		"priority delay bound exceeds time.Duration": func() Config {
			config := validConfig()
			config.PriorityRateBytesPerSecond = math.Nextafter(config.RateBytesPerSecond, 0)
			return config
		}(),
	}
	for name, config := range tests {
		config := config
		t.Run(name, func(t *testing.T) {
			if shaper, err := New(config, newFakeClock(), write); err == nil {
				_ = shaper.Close()
				t.Fatal("constructor accepted an invalid model")
			}
		})
	}
}

func TestValidateConfigErrorMessages(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{
			name: "R",
			mutate: func(config *Config) {
				config.RateBytesPerSecond = math.Inf(1)
			},
			want: "shaper rate R must be finite and positive",
		},
		{
			name: "Rp",
			mutate: func(config *Config) {
				config.PriorityRateBytesPerSecond = -1
			},
			want: "priority rate Rp must be finite and non-negative",
		},
		{
			name: "R greater than Rp",
			mutate: func(config *Config) {
				config.RateBytesPerSecond = config.PriorityRateBytesPerSecond
			},
			want: "shaper rate R must be greater than priority rate Rp",
		},
		{
			name: "Lmax",
			mutate: func(config *Config) {
				config.MaxDatagramBytes = 0
			},
			want: "maximum datagram Lmax must be positive",
		},
		{
			name: "B",
			mutate: func(config *Config) {
				config.DataBudgetBytes = config.MaxDatagramBytes - 1
			},
			want: "data budget B must be at least Lmax",
		},
		{
			name: "Pburst",
			mutate: func(config *Config) {
				config.PriorityBurstBytes = -1
			},
			want: "priority burst Pburst must be non-negative",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := validConfig()
			test.mutate(&config)
			err := ValidateConfig(config)
			if err == nil || err.Error() != test.want {
				t.Fatalf("ValidateConfig error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestReservationOrderSurvivesDelayedCopyPublication(t *testing.T) {
	clock := newFakeClock()
	writes := make(chan byte, 2)
	shaper, err := New(validConfig(), clock, func(datagram []byte) error {
		writes <- datagram[0]
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = shaper.Close() }()

	// Caller A reserves the earlier virtual deadline, then stalls before its
	// caller-owned payload has been copied and published.
	firstReservation, err := shaper.reserve(context.Background(), ClassData, 100)
	if err != nil {
		t.Fatal(err)
	}

	secondResult := make(chan error, 1)
	go func() {
		secondResult <- shaper.WriteBatch(
			context.Background(),
			ClassData,
			[][]byte{bytesOf(2, 100)},
		)
	}()
	waitFor(t, func() bool {
		shaper.mu.Lock()
		defer shaper.mu.Unlock()
		for _, datagram := range shaper.queue {
			if len(datagram.payload) > 0 && datagram.payload[0] == 2 {
				return true
			}
		}
		return false
	})

	clock.Advance(100 * time.Millisecond)
	firstDone, err := shaper.enqueue(firstReservation, bytesOf(1, 100))
	if err != nil {
		t.Fatal(err)
	}

	order := []byte{
		waitValue(t, writes, "first reserved datagram was not written"),
		waitValue(t, writes, "second reserved datagram was not written"),
	}
	if err := waitResult(t, firstDone); err != nil {
		t.Fatal(err)
	}
	if err := waitResult(t, secondResult); err != nil {
		t.Fatal(err)
	}
	if order[0] != 1 || order[1] != 2 {
		t.Fatalf("write order = %v, want reservation order [1 2]", order)
	}
}

func TestImmutableVirtualFinishDeadlinesAndExactByteEnvelope(t *testing.T) {
	clock := newFakeClock()
	base := clock.Now()
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})

	var mu sync.Mutex
	var writeTimes []time.Time
	write := func([]byte) error {
		mu.Lock()
		writeTimes = append(writeTimes, clock.Now())
		call := len(writeTimes)
		mu.Unlock()
		if call == 1 {
			close(firstStarted)
			<-releaseFirst
		}
		return nil
	}

	shaper, err := New(validConfig(), clock, write)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := shaper.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	result := make(chan error, 1)
	go func() {
		result <- shaper.WriteBatch(
			context.Background(),
			ClassData,
			[][]byte{make([]byte, 100), make([]byte, 50), make([]byte, 80)},
		)
	}()

	select {
	case <-firstStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("startup datagram was not written immediately")
	}
	waitFor(t, func() bool {
		shaper.mu.Lock()
		defer shaper.mu.Unlock()
		return len(shaper.queue) == 2
	})

	shaper.mu.Lock()
	secondDeadline := shaper.queue[0].deadline
	thirdDeadline := shaper.queue[1].deadline
	tailBeforePriority := shaper.tail
	shaper.mu.Unlock()

	if want := base.Add(100 * time.Millisecond); !secondDeadline.Equal(want) {
		t.Fatalf("second deadline = %v, want %v", secondDeadline, want)
	}
	if want := base.Add(150 * time.Millisecond); !thirdDeadline.Equal(want) {
		t.Fatalf("third deadline = %v, want %v", thirdDeadline, want)
	}
	if want := base.Add(230 * time.Millisecond); !tailBeforePriority.Equal(want) {
		t.Fatalf("tail = %v, want %v", tailBeforePriority, want)
	}

	if err := shaper.AccountPriority(100); err != nil {
		t.Fatal(err)
	}
	shaper.mu.Lock()
	if !shaper.queue[0].deadline.Equal(secondDeadline) ||
		!shaper.queue[1].deadline.Equal(thirdDeadline) {
		t.Fatal("AccountPriority changed an existing immutable deadline")
	}
	if want := base.Add(330 * time.Millisecond); !shaper.tail.Equal(want) {
		t.Fatalf("future tail = %v, want %v", shaper.tail, want)
	}
	shaper.mu.Unlock()

	close(releaseFirst)
	waitFor(t, func() bool {
		return clock.HasActiveTimerAt(secondDeadline)
	})
	clock.Advance(99 * time.Millisecond)
	mu.Lock()
	if got := len(writeTimes); got != 1 {
		t.Fatalf("writes before byte deadline = %d, want 1", got)
	}
	mu.Unlock()

	clock.Advance(time.Millisecond)
	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(writeTimes) == 2
	})
	waitFor(t, func() bool {
		return clock.HasActiveTimerAt(thirdDeadline)
	})
	clock.Advance(50 * time.Millisecond)
	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(writeTimes) == 3
	})

	if err := waitResult(t, result); err != nil {
		t.Fatal(err)
	}

	clock.Advance(time.Second)
	idleWriteTime := clock.Now()
	if err := shaper.WriteBatch(context.Background(), ClassData, [][]byte{make([]byte, 20)}); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if !writeTimes[0].Equal(base) ||
		!writeTimes[1].Equal(base.Add(100*time.Millisecond)) ||
		!writeTimes[2].Equal(base.Add(150*time.Millisecond)) ||
		!writeTimes[3].Equal(idleWriteTime) {
		t.Fatalf("write times = %v", writeTimes)
	}
}

func TestWriterErrorDoesNotStopLaterDatagrams(t *testing.T) {
	clock := newFakeClock()
	sentinel := errors.New("writer failed")
	var calls int
	shaper, err := New(validConfig(), clock, func([]byte) error {
		calls++
		if calls == 1 {
			return sentinel
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = shaper.Close() }()

	if err := shaper.WriteBatch(context.Background(), ClassData, [][]byte{{1}}); !errors.Is(err, sentinel) {
		t.Fatalf("first error = %v, want %v", err, sentinel)
	}

	result := make(chan error, 1)
	go func() {
		result <- shaper.WriteBatch(context.Background(), ClassData, [][]byte{{2}})
	}()
	clock.Advance(time.Millisecond)
	if err := waitResult(t, result); err != nil {
		t.Fatalf("second write failed: %v", err)
	}
}

func TestBatchWriterErrorStopsUnstartedSuffixAndReportsPrefix(t *testing.T) {
	clock := newFakeClock()
	sentinel := errors.New("writer failed")
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var mu sync.Mutex
	var calls []byte
	shaper, err := New(validConfig(), clock, func(datagram []byte) error {
		mu.Lock()
		calls = append(calls, datagram[0])
		call := len(calls)
		mu.Unlock()
		if call == 1 {
			close(firstStarted)
			<-releaseFirst
		}
		if datagram[0] == 2 {
			return sentinel
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = shaper.Close() }()

	type outcome struct {
		result BatchResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := shaper.WriteDatagrams(
			context.Background(),
			[]Datagram{
				{Class: ClassData, Payload: bytesOf(1, 100)},
				{Class: ClassData, Payload: bytesOf(2, 100)},
				{Class: ClassData, Payload: bytesOf(3, 100)},
			},
		)
		done <- outcome{result: result, err: err}
	}()

	waitChannel(t, firstStarted, "first datagram did not reach writer")
	waitFor(t, func() bool {
		shaper.mu.Lock()
		defer shaper.mu.Unlock()
		return len(shaper.queue) == 2 &&
			shaper.queue[0].ready &&
			shaper.queue[1].ready
	})
	close(releaseFirst)
	clock.Advance(100 * time.Millisecond)

	got := waitValue(t, done, "batch did not return after terminal writer error")
	if !errors.Is(got.err, sentinel) {
		t.Fatalf("batch error = %v, want %v", got.err, sentinel)
	}
	if got.result.Accepted != 3 || got.result.Emitted != 1 || got.result.FailedIndex != 1 {
		t.Fatalf("result = %+v, want accepted=3 emitted=1 failed-index=1", got.result)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 2 || calls[0] != 1 || calls[1] != 2 {
		t.Fatalf("writer calls = %v, want terminal prefix [1 2] with suffix unstarted", calls)
	}
	snapshot := shaper.Snapshot()
	if snapshot.AcceptedBytes != 300 ||
		snapshot.EmittedBytes != 100 ||
		snapshot.AsyncWriteErrors != 1 ||
		snapshot.AsyncWriteErrorBytes != 200 {
		t.Fatalf("terminal batch byte reconciliation = %+v", snapshot)
	}
}

func TestSeparateClassBudgetsBackpressureBeforeCopyAndCancellation(t *testing.T) {
	clock := newFakeClock()
	config := validConfig()
	config.DataBudgetBytes = 100

	started := make(chan []byte, 8)
	releaseFirst := make(chan struct{})
	var mu sync.Mutex
	call := 0
	shaper, err := New(config, clock, func(datagram []byte) error {
		copied := append([]byte(nil), datagram...)
		started <- copied
		mu.Lock()
		call++
		current := call
		mu.Unlock()
		if current == 1 {
			<-releaseFirst
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := shaper.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	firstResult := make(chan error, 1)
	go func() {
		firstResult <- shaper.WriteBatch(
			context.Background(),
			ClassData,
			[][]byte{bytesOf(1, 100)},
		)
	}()
	first := <-started
	if first[0] != 1 {
		t.Fatalf("first payload marker = %d", first[0])
	}

	secondResult := make(chan error, 1)
	go func() {
		secondResult <- shaper.WriteBatch(
			context.Background(),
			ClassData,
			[][]byte{bytesOf(2, 100)},
		)
	}()
	waitFor(t, func() bool {
		shaper.mu.Lock()
		defer shaper.mu.Unlock()
		return shaper.retainedDataBytes == config.DataBudgetBytes
	})

	// DATA has filled exactly B while the entire C reserve remains unused.
	// A later DATA datagram must still wait rather than borrow from C.
	blockedData := bytesOf(3, 100)
	blockedDataContext := observeContext(context.Background())
	blockedDataResult := make(chan error, 1)
	go func() {
		blockedDataResult <- shaper.WriteBatch(
			blockedDataContext,
			ClassData,
			[][]byte{blockedData},
		)
	}()
	waitChannel(t, blockedDataContext.doneObserved, "DATA did not reach capacity backpressure")
	assertNotReady(t, blockedDataResult, "capacity-blocked DATA completed")

	controlResult := make(chan error, 1)
	go func() {
		controlResult <- shaper.WriteBatch(
			context.Background(),
			ClassControl,
			[][]byte{bytesOf(4, 100)},
		)
	}()
	waitFor(t, func() bool {
		shaper.mu.Lock()
		defer shaper.mu.Unlock()
		return shaper.retainedControlBytes == config.ControlReserveBytes
	})

	shaper.mu.Lock()
	if shaper.retainedDataBytes != config.DataBudgetBytes {
		t.Fatalf("DATA retained = %d, want exact B=%d", shaper.retainedDataBytes, config.DataBudgetBytes)
	}
	if shaper.retainedControlBytes != config.ControlReserveBytes {
		t.Fatalf("CONTROL retained = %d, want exact C=%d", shaper.retainedControlBytes, config.ControlReserveBytes)
	}
	if got, want := shaper.retainedDataBytes+shaper.retainedControlBytes, config.DataBudgetBytes+config.ControlReserveBytes; got != want {
		t.Fatalf("retained = %d, want exact Q=%d", got, want)
	}
	if got := shaper.retainedDataBytes + shaper.retainedControlBytes + shaper.inFlightBytes; got > config.DataBudgetBytes+config.ControlReserveBytes+config.MaxDatagramBytes {
		t.Fatalf("retained plus in-flight = %d, exceeds Q+Lmax", got)
	}
	shaper.mu.Unlock()

	controlContext, cancelControl := context.WithCancel(context.Background())
	observedControlContext := observeContext(controlContext)
	blockedControlResult := make(chan error, 1)
	go func() {
		blockedControlResult <- shaper.WriteBatch(
			observedControlContext,
			ClassControl,
			[][]byte{bytesOf(5, 100)},
		)
	}()
	waitChannel(t, observedControlContext.doneObserved, "CONTROL did not reach capacity backpressure")
	assertNotReady(t, blockedControlResult, "capacity-blocked CONTROL completed")
	cancelControl()
	if err := waitResult(t, blockedControlResult); !errors.Is(err, context.Canceled) {
		t.Fatalf("blocked CONTROL error = %v, want context cancellation", err)
	}

	// The source remains caller-owned until byte capacity has been reserved.
	blockedData[0] = 9
	close(releaseFirst)
	if err := waitResult(t, firstResult); err != nil {
		t.Fatal(err)
	}

	clock.Advance(100 * time.Millisecond)
	second := <-started
	if second[0] != 2 {
		t.Fatalf("second payload marker = %d", second[0])
	}
	if err := waitResult(t, secondResult); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		shaper.mu.Lock()
		defer shaper.mu.Unlock()
		for _, datagram := range shaper.queue {
			if len(datagram.payload) > 0 && datagram.payload[0] == 9 {
				bound := time.Duration(float64(config.DataBudgetBytes+config.ControlReserveBytes+config.MaxDatagramBytes) /
					config.RateBytesPerSecond * float64(time.Second))
				return !datagram.deadline.After(clock.Now().Add(bound))
			}
		}
		return false
	})

	clock.Advance(100 * time.Millisecond)
	control := <-started
	if control[0] != 4 {
		t.Fatalf("CONTROL payload marker = %d", control[0])
	}
	if err := waitResult(t, controlResult); err != nil {
		t.Fatal(err)
	}

	clock.Advance(100 * time.Millisecond)
	afterBackpressure := <-started
	if afterBackpressure[0] != 9 {
		t.Fatalf("payload copied before admission: marker = %d, want 9", afterBackpressure[0])
	}
	if err := waitResult(t, blockedDataResult); err != nil {
		t.Fatal(err)
	}

	select {
	case extra := <-started:
		t.Fatalf("cancelled CONTROL was written: marker %d", extra[0])
	default:
	}
}

func TestAggregateLargerThanDataBudgetStreamsByIndividualReservations(t *testing.T) {
	clock := newFakeClock()
	config := validConfig()
	config.DataBudgetBytes = 100
	config.ControlReserveBytes = 60
	config.MaxDatagramBytes = 60
	config.PriorityBurstBytes = 60

	writes := make(chan []byte, 3)
	shaper, err := New(config, clock, func(datagram []byte) error {
		writes <- append([]byte(nil), datagram...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = shaper.Close() }()

	result := make(chan error, 1)
	observedContext := observeContext(context.Background())
	go func() {
		result <- shaper.WriteBatch(
			observedContext,
			ClassData,
			[][]byte{bytesOf(1, 60), bytesOf(2, 60), bytesOf(3, 60)},
		)
	}()

	if got := (<-writes)[0]; got != 1 {
		t.Fatalf("first marker = %d", got)
	}
	waitChannel(t, observedContext.doneObserved, "aggregate did not backpressure beyond B")
	assertNotReady(t, result, "aggregate larger than B completed without streaming")
	clock.Advance(60 * time.Millisecond)
	if got := (<-writes)[0]; got != 2 {
		t.Fatalf("second marker = %d", got)
	}
	clock.Advance(60 * time.Millisecond)
	if got := (<-writes)[0]; got != 3 {
		t.Fatalf("third marker = %d", got)
	}
	if err := waitResult(t, result); err != nil {
		t.Fatal(err)
	}
}

func TestPriorityDebtClearanceUsesNetRateIncludingNearPriorityRate(t *testing.T) {
	tests := []struct {
		name          string
		rate          float64
		priorityRate  float64
		initialDebt   int
		priorityBurst int
		step          time.Duration
		arrival       int
		steps         int
		oldBoundStep  int
	}{
		{
			name:          "ordinary",
			rate:          1_000,
			priorityRate:  200,
			initialDebt:   200,
			priorityBurst: 100,
			step:          25 * time.Millisecond,
			arrival:       5,
			steps:         15,
			oldBoundStep:  8,
		},
		{
			name:          "near Rp",
			rate:          1_000,
			priorityRate:  900,
			initialDebt:   100,
			priorityBurst: 100,
			step:          100 * time.Millisecond,
			arrival:       90,
			steps:         20,
			oldBoundStep:  1,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			clock := newFakeClock()
			config := validConfig()
			config.RateBytesPerSecond = test.rate
			config.PriorityRateBytesPerSecond = test.priorityRate
			config.PriorityBurstBytes = test.priorityBurst

			admitted := make(chan struct{}, 1)
			shaper, err := New(config, clock, func([]byte) error {
				admitted <- struct{}{}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = shaper.Close() }()

			for debt := test.initialDebt; debt > 0; {
				chunk := min(debt, config.MaxDatagramBytes)
				if err := shaper.AccountPriority(chunk); err != nil {
					t.Fatal(err)
				}
				debt -= chunk
			}
			snapshot := shaper.Snapshot()
			if want := time.Duration(test.steps) * test.step; snapshot.PriorityDelayBound != want {
				t.Fatalf(
					"snapshot Dp = %v, want fake-clock admission bound %v",
					snapshot.PriorityDelayBound,
					want,
				)
			}

			writeContext := observeContext(context.Background())
			result := make(chan error, 1)
			go func() {
				result <- shaper.WriteBatch(writeContext, ClassData, [][]byte{{1}})
			}()
			waitChannel(t, writeContext.doneObserved, "DATA call did not wait on initial priority debt")

			if err := shaper.AccountPriority(test.priorityBurst); err != nil {
				t.Fatal(err)
			}
			for step := 1; step <= test.steps; step++ {
				if err := shaper.AccountPriority(test.arrival); err != nil {
					t.Fatal(err)
				}
				shaper.mu.Lock()
				priorityDeadline := shaper.priorityTail
				shaper.mu.Unlock()
				waitFor(t, func() bool {
					return clock.HasActiveTimerAt(priorityDeadline)
				})
				clock.MoveWithoutFiring(test.step)
				clock.FireDue()
				if step == test.oldBoundStep {
					assertNotReady(t, admitted, "DATA admitted by the obsolete P0/R bound")
				}
			}

			select {
			case <-admitted:
			case <-time.After(2 * time.Second):
				shaper.mu.Lock()
				priorityTail := shaper.priorityTail
				tail := shaper.tail
				retained := shaper.retainedDataBytes
				queued := len(shaper.queue)
				shaper.mu.Unlock()
				t.Fatalf(
					"not admitted by Dp=(P0+Pburst)/(R-Rp) after %v: now=%v priorityTail=%v tail=%v retained=%d queued=%d",
					time.Duration(test.steps)*test.step,
					clock.Now(),
					priorityTail,
					tail,
					retained,
					queued,
				)
			}
			if err := waitResult(t, result); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPriorityDebitAtMaturedDeadlineCannotRevokeAdmission(t *testing.T) {
	clock := newFakeClock()
	config := validConfig()
	config.RateBytesPerSecond = 1_000
	config.PriorityRateBytesPerSecond = 100
	shaper, err := New(config, clock, func([]byte) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = shaper.Close() }()

	if err := shaper.AccountPriority(100); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		result <- shaper.WriteBatch(context.Background(), ClassData, [][]byte{{1}})
	}()
	priorityDeadline := clock.Now().Add(100 * time.Millisecond)
	waitFor(t, func() bool {
		return clock.HasActiveTimerAt(priorityDeadline)
	})

	// Adversarial exact-boundary order: the new priority debit wins s.mu before
	// the old debt timer fires. The reservation already waiting on the matured
	// half-open [start,deadline) envelope must still publish at the deadline;
	// this debit affects only later reservations and future serialization.
	clock.MoveWithoutFiring(100 * time.Millisecond)
	if err := shaper.AccountPriority(10); err != nil {
		t.Fatal(err)
	}
	clock.FireDue()
	waitFor(t, func() bool {
		shaper.mu.Lock()
		defer shaper.mu.Unlock()
		return shaper.retainedDataBytes == 1 && len(shaper.queue) == 1
	})

	clock.Advance(10 * time.Millisecond)
	if err := waitResult(t, result); err != nil {
		t.Fatal(err)
	}
}

func TestPriorityDebitBeforeDeadlineExtendsDeferredWaiter(t *testing.T) {
	clock := newFakeClock()
	config := validConfig()
	config.RateBytesPerSecond = 1_000
	config.PriorityRateBytesPerSecond = 100
	shaper, err := New(config, clock, func([]byte) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = shaper.Close() }()

	if err := shaper.AccountPriority(100); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		result <- shaper.WriteBatch(context.Background(), ClassData, [][]byte{{1}})
	}()
	firstDeadline := clock.Now().Add(100 * time.Millisecond)
	waitFor(t, func() bool {
		return clock.HasActiveTimerAt(firstDeadline)
	})

	// The debit linearizes one nanosecond before the captured deadline, but the
	// waiting goroutine does not observe the notification until the old deadline:
	// the Clock returns D-epsilon to AccountPriority and atomically moves to D
	// immediately afterward. Every debit in [call,D) must extend the waiter even
	// when scheduling defers its observation until D.
	clock.MoveWithoutFiring(100*time.Millisecond - time.Nanosecond)
	clock.AdvanceAfterNextNow(time.Nanosecond)
	if err := shaper.AccountPriority(10); err != nil {
		t.Fatal(err)
	}
	clock.FireDue()
	waitFor(t, func() bool {
		return clock.HasActiveTimerAt(firstDeadline.Add(10 * time.Millisecond))
	})
	shaper.mu.Lock()
	retainedAtOldDeadline := shaper.retainedDataBytes
	shaper.mu.Unlock()
	if retainedAtOldDeadline != 0 {
		t.Fatalf("D-epsilon debit left %d DATA bytes admitted at old deadline D", retainedAtOldDeadline)
	}

	clock.Advance(10 * time.Millisecond)
	if err := waitResult(t, result); err != nil {
		t.Fatal(err)
	}
}

func TestPriorityAdmissionBoundWithZeroInitialDebtAndPostCallBurst(t *testing.T) {
	clock := newFakeClock()
	config := validConfig()
	config.RateBytesPerSecond = 10_000
	config.PriorityRateBytesPerSecond = 2_000
	config.DataBudgetBytes = config.MaxDatagramBytes

	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	writes := make(chan byte, 3)
	writeCount := 0
	shaper, err := New(config, clock, func(payload []byte) error {
		writeCount++
		if writeCount == 1 {
			close(firstEntered)
			<-releaseFirst
		}
		writes <- payload[0]
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = shaper.Close() }()

	fullBudget := make(chan error, 1)
	go func() {
		fullBudget <- shaper.WriteBatch(context.Background(), ClassData, [][]byte{
			bytesOf(1, config.MaxDatagramBytes),
			bytesOf(2, config.MaxDatagramBytes),
		})
	}()
	waitChannel(t, firstEntered, "first full-budget datagram did not enter the writer")
	waitFor(t, func() bool {
		shaper.mu.Lock()
		defer shaper.mu.Unlock()
		return shaper.retainedDataBytes == config.DataBudgetBytes
	})

	// This call starts while P0=0 and waits only because B is full. Inject the
	// permitted post-call Pburst before capacity frees; the obsolete P0/R bound
	// is zero, but the call must observe the burst and remain blocked.
	targetContext := observeContext(context.Background())
	target := make(chan error, 1)
	go func() {
		target <- shaper.WriteBatch(targetContext, ClassData, [][]byte{{3}})
	}()
	waitChannel(t, targetContext.doneObserved, "P0=0 target did not wait on the full DATA budget")
	if err := shaper.AccountPriority(config.PriorityBurstBytes); err != nil {
		t.Fatal(err)
	}
	priorityDeadline := clock.Now().Add(10 * time.Millisecond)
	waitFor(t, func() bool {
		return clock.HasActiveTimerAt(priorityDeadline)
	})
	assertNotReady(t, target, "P0=0 target ignored the post-call Pburst")

	close(releaseFirst)
	if marker := (<-writes); marker != 1 {
		t.Fatalf("first marker = %d, want 1", marker)
	}
	clock.Advance(10 * time.Millisecond)
	if marker := (<-writes); marker != 2 {
		t.Fatalf("second marker = %d, want 2", marker)
	}
	waitFor(t, func() bool {
		shaper.mu.Lock()
		defer shaper.mu.Unlock()
		return shaper.retainedDataBytes == 1
	})
	dp := time.Duration(
		float64(config.PriorityBurstBytes) /
			(config.RateBytesPerSecond - config.PriorityRateBytesPerSecond) *
			float64(time.Second),
	)
	if elapsed := clock.Now().Sub(time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)); elapsed > dp {
		t.Fatalf("P0=0 admission at %v, want <= Dp=Pburst/(R-Rp)=%v", elapsed, dp)
	}

	clock.Advance(20*time.Millisecond + time.Nanosecond)
	if marker := (<-writes); marker != 3 {
		t.Fatalf("target marker = %d, want 3", marker)
	}
	if err := waitResult(t, fullBudget); err != nil {
		t.Fatal(err)
	}
	if err := waitResult(t, target); err != nil {
		t.Fatal(err)
	}
}

func TestPathInstancesHaveIndependentVirtualTime(t *testing.T) {
	clock := newFakeClock()
	config := validConfig()
	aStarted := make(chan struct{}, 1)
	bStarted := make(chan struct{}, 1)

	pathA, err := New(config, clock, func([]byte) error {
		aStarted <- struct{}{}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pathA.Close() }()
	pathB, err := New(config, clock, func([]byte) error {
		bStarted <- struct{}{}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pathB.Close() }()

	if err := pathA.AccountPriority(100); err != nil {
		t.Fatal(err)
	}
	pathAContext := observeContext(context.Background())
	aResult := make(chan error, 1)
	go func() {
		aResult <- pathA.WriteBatch(pathAContext, ClassData, [][]byte{{1}})
	}()
	bResult := make(chan error, 1)
	go func() {
		bResult <- pathB.WriteBatch(context.Background(), ClassData, [][]byte{{2}})
	}()

	waitChannel(t, pathAContext.doneObserved, "path A did not wait on its own debt")
	select {
	case <-bStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("independent path B did not start immediately")
	}
	assertNotReady(t, aStarted, "path A started before its own debt cleared")
	if err := waitResult(t, bResult); err != nil {
		t.Fatal(err)
	}

	clock.Advance(100 * time.Millisecond)
	select {
	case <-aStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("path A did not start after its own debt cleared")
	}
	if err := waitResult(t, aResult); err != nil {
		t.Fatal(err)
	}
}

func TestBlockedAdmissionCancellationAndCloseDoNotLeak(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	clock := newFakeClock()
	var writes int
	shaper, err := New(validConfig(), clock, func([]byte) error {
		writes++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := shaper.AccountPriority(100); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- shaper.WriteBatch(ctx, ClassData, [][]byte{bytesOf(7, 100)})
	}()
	waitFor(t, func() bool {
		clock.mu.Lock()
		defer clock.mu.Unlock()
		return len(clock.timers) > 0
	})
	cancel()
	if err := waitResult(t, result); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
	if writes != 0 {
		t.Fatalf("cancelled datagram reached writer %d times", writes)
	}
	if err := shaper.Close(); err != nil {
		t.Fatal(err)
	}

	afterClose := make(chan error, 1)
	go func() {
		afterClose <- shaper.WriteBatch(context.Background(), ClassData, [][]byte{{1}})
	}()
	if err := waitResult(t, afterClose); !errors.Is(err, ErrClosed) {
		t.Fatalf("write after Close error = %v, want ErrClosed", err)
	}
}

func bytesOf(marker byte, size int) []byte {
	datagram := make([]byte, size)
	for i := range datagram {
		datagram[i] = marker
	}
	return datagram
}

func assertNotReady[T any](t *testing.T, result <-chan T, message string) {
	t.Helper()
	select {
	case value := <-result:
		t.Fatalf("%s: %v", message, value)
	default:
	}
}

func waitChannel(t *testing.T, signal <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatal(message)
	}
}

func waitValue[T any](t *testing.T, values <-chan T, message string) T {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(2 * time.Second):
		t.Fatal(message)
		var zero T
		return zero
	}
}
