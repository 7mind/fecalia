package bind

import (
	"context"
	"fmt"
	"math"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/frame"
	"github.com/7mind/wanbond/internal/shaper"
	"github.com/7mind/wanbond/internal/telemetry"
)

type priorityDebtClock struct {
	mu     sync.Mutex
	now    time.Time
	timers map[*priorityDebtTimer]struct{}
}

type priorityDebtTimer struct {
	clock   *priorityDebtClock
	ch      chan time.Time
	at      time.Time
	stopped bool
	fired   bool
}

func newPriorityDebtClock() *priorityDebtClock {
	return &priorityDebtClock{
		now:    time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC),
		timers: make(map[*priorityDebtTimer]struct{}),
	}
}

func (c *priorityDebtClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *priorityDebtClock) NewTimerAt(deadline time.Time) shaper.Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	timer := &priorityDebtTimer{
		clock: c,
		ch:    make(chan time.Time, 1),
		at:    deadline,
	}
	c.timers[timer] = struct{}{}
	c.fireDueLocked()
	return timer
}

func (c *priorityDebtClock) advanceWithoutFiring(delay time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(delay)
	c.mu.Unlock()
}

func (c *priorityDebtClock) fireDue() {
	c.mu.Lock()
	c.fireDueLocked()
	c.mu.Unlock()
	runtime.Gosched()
}

func (c *priorityDebtClock) hasActiveTimerAt(deadline time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for timer := range c.timers {
		if !timer.stopped && !timer.fired && timer.at.Equal(deadline) {
			return true
		}
	}
	return false
}

func (c *priorityDebtClock) fireDueLocked() {
	for timer := range c.timers {
		if timer.stopped || timer.fired || timer.at.After(c.now) {
			continue
		}
		timer.fired = true
		timer.ch <- c.now
	}
}

func (t *priorityDebtTimer) C() <-chan time.Time {
	return t.ch
}

func (t *priorityDebtTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	if t.stopped || t.fired {
		return false
	}
	t.stopped = true
	delete(t.clock.timers, t)
	return true
}

func waitForPriorityDebtTimer(t testing.TB, clock *priorityDebtClock, deadline time.Time) {
	t.Helper()
	timeout := time.Now().Add(time.Second)
	for !clock.hasActiveTimerAt(deadline) {
		if time.Now().After(timeout) {
			t.Fatalf("timed out waiting for DATA admission timer at %v", deadline)
		}
		runtime.Gosched()
	}
}

type priorityRecordingShaper struct {
	mu        sync.Mutex
	debits    []int
	onAccount func(int)
}

func (*priorityRecordingShaper) WriteDatagrams(_ context.Context, datagrams []shaper.Datagram) (shaper.BatchResult, error) {
	return shaper.BatchResult{Accepted: len(datagrams), Emitted: len(datagrams), FailedIndex: -1}, nil
}

func (s *priorityRecordingShaper) AccountPriority(size int) error {
	if s.onAccount != nil {
		s.onAccount(size)
	}
	s.mu.Lock()
	s.debits = append(s.debits, size)
	s.mu.Unlock()
	return nil
}

func (*priorityRecordingShaper) Close() error { return nil }

func (s *priorityRecordingShaper) debitSnapshot() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int(nil), s.debits...)
}

func priorityTestShaperConfig() config.PathShaperConfig {
	return config.PathShaperConfig{
		RateBytesPerSecond:      1_000_000,
		DataBurstBytes:          45_000,
		ControlReserveBytes:     1472,
		MaxEncodedDatagramBytes: 1472,
		ProbeRateBytesPerSecond: 14_720,
		ProbeBurstBytes:         2944,
	}
}

func openWithPriorityRecorder(t testing.TB, m *Multipath, recorder *priorityRecordingShaper) {
	t.Helper()
	m.shaperConfigs = []config.PathShaperConfig{priorityTestShaperConfig()}
	m.newPathShaper = func(shaper.Config, shaper.WriteFunc) (pathShaper, error) {
		return recorder, nil
	}
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
}

func TestGeneratedProbeWritesImmediatelyBeforeExactBytePriorityDebit(t *testing.T) {
	psk := testKey(t, 0xE1)
	clk := newFakeClock()
	m, _, _ := newProbingMultipath(t, loopbackPaths(1), psk, clk)
	peer, peerAP := rawPeer(t)
	codec, err := frame.NewCodec(psk)
	if err != nil {
		t.Fatal(err)
	}

	recorder := &priorityRecordingShaper{}
	recorder.onAccount = func(size int) {
		if err := peer.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatal(err)
		}
		raw := make([]byte, maxDatagram)
		n, err := peer.Read(raw)
		if err != nil {
			t.Fatalf("priority debit ran before generated PROBE became receiver-visible: %v", err)
		}
		if n != size {
			t.Fatalf("generated PROBE priority debit = %d bytes, encoded wire datagram = %d bytes", size, n)
		}
		decoded, err := codec.Decode(raw[:n])
		if err != nil {
			t.Fatal(err)
		}
		probe, ok := decoded.(frame.Probe)
		if !ok || probe.IsEcho {
			t.Fatalf("priority datagram = %#v, want originating authenticated PROBE", decoded)
		}
	}
	openWithPriorityRecorder(t, m, recorder)
	m.paths[0].setRemote(peerAP)

	m.emitProbes()

	if got := recorder.debitSnapshot(); len(got) != 1 {
		t.Fatalf("generated PROBE exact-byte priority debits = %v, want one post-write debit", got)
	}
}

func TestReflectedEchoWritesImmediatelyBeforeExactBytePriorityDebit(t *testing.T) {
	psk := testKey(t, 0xE2)
	clk := newFakeClock()
	m, _, _ := newProbingMultipath(t, loopbackPaths(1), psk, clk)
	peer, peerAP := rawPeer(t)
	codec, err := frame.NewCodec(psk)
	if err != nil {
		t.Fatal(err)
	}

	recorder := &priorityRecordingShaper{}
	recorder.onAccount = func(size int) {
		if err := peer.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatal(err)
		}
		raw := make([]byte, maxDatagram)
		n, err := peer.Read(raw)
		if err != nil {
			t.Fatalf("priority debit ran before reflected echo became receiver-visible: %v", err)
		}
		if n != size {
			t.Fatalf("reflected echo priority debit = %d bytes, encoded wire datagram = %d bytes", size, n)
		}
		decoded, err := codec.Decode(raw[:n])
		if err != nil {
			t.Fatal(err)
		}
		echo, ok := decoded.(frame.Probe)
		if !ok || !echo.IsEcho {
			t.Fatalf("priority datagram = %#v, want authenticated reflected echo", decoded)
		}
	}
	openWithPriorityRecorder(t, m, recorder)

	raw, err := frame.Encode(psk, frame.Probe{
		PathID:         0,
		ProbeSeq:       7,
		TimestampNanos: clk.Now().UnixNano(),
	})
	if err != nil {
		t.Fatal(err)
	}
	m.handleInbound(m.paths[0], raw, peerAP)

	if got := recorder.debitSnapshot(); len(got) != 1 {
		t.Fatalf("reflected echo exact-byte priority debits = %v, want one post-write debit", got)
	}
}

func TestFailedGeneratedProbeWriteCreatesNoPriorityDebt(t *testing.T) {
	psk := testKey(t, 0xE3)
	clk := newFakeClock()
	m, _, _ := newProbingMultipath(t, loopbackPaths(1), psk, clk)
	recorder := &priorityRecordingShaper{}
	openWithPriorityRecorder(t, m, recorder)

	_, peerAP := rawPeer(t)
	m.paths[0].setRemote(peerAP)
	if err := m.paths[0].conn.Close(); err != nil {
		t.Fatal(err)
	}
	m.emitProbes()

	if got := recorder.debitSnapshot(); len(got) != 0 {
		t.Fatalf("failed generated PROBE write created priority debt %v", got)
	}
	if got := m.paths[0].probeSendErrors.Load(); got != 1 {
		t.Fatalf("probe send errors = %d, want 1", got)
	}
}

type priorityEmission struct {
	at   time.Time
	size int
}

type priorityForwardingShaper struct {
	*shaper.Shaper

	mu     sync.Mutex
	debits []int
}

func (s *priorityForwardingShaper) AccountPriority(size int) error {
	if err := s.Shaper.AccountPriority(size); err != nil {
		return err
	}
	s.mu.Lock()
	s.debits = append(s.debits, size)
	s.mu.Unlock()
	return nil
}

func (s *priorityForwardingShaper) assertDebits(t testing.TB, count, size int) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.debits) != count {
		t.Fatalf("generated priority debit count = %d, want %d: %v", len(s.debits), count, s.debits)
	}
	for i, debit := range s.debits {
		if debit != size {
			t.Fatalf("generated priority debit %d = %d, want exact Lmax=%d", i, debit, size)
		}
	}
}

func TestFullDataBudgetObservesGeneratedPriorityNetRateBound(t *testing.T) {
	psk := testKey(t, 0xE4)
	telemetryClock := newFakeClock()
	m, _, _ := newProbingMultipath(t, loopbackPaths(1), psk, telemetryClock)
	m.scheduler = &unpacedSelectionRecorder{}
	_, peerAP := rawPeer(t)
	sample, err := frame.Encode(psk, frame.Probe{})
	if err != nil {
		t.Fatal(err)
	}
	lmax := len(sample)
	if lmax <= frame.DataOverhead {
		t.Fatalf("generated PROBE length %d does not admit a DATA payload", lmax)
	}

	const priorityStep = time.Millisecond
	rp := float64(lmax) / priorityStep.Seconds()
	rate := 1.1 * rp
	cfg := config.PathShaperConfig{
		RateBytesPerSecond:      rate,
		DataBurstBytes:          2 * lmax,
		ControlReserveBytes:     lmax,
		MaxEncodedDatagramBytes: lmax,
		ProbeRateBytesPerSecond: rp,
		ProbeBurstBytes:         2 * lmax,
	}
	shaperClock := newPriorityDebtClock()
	emissions := make(chan priorityEmission, 2)
	var liveShaper *priorityForwardingShaper
	m.shaperConfigs = []config.PathShaperConfig{cfg}
	m.newPathShaper = func(got shaper.Config, write shaper.WriteFunc) (pathShaper, error) {
		exact, err := shaper.New(got, shaperClock, func(payload []byte) error {
			emissions <- priorityEmission{at: shaperClock.Now(), size: len(payload)}
			return write(payload)
		})
		if err != nil {
			return nil, err
		}
		liveShaper = &priorityForwardingShaper{Shaper: exact}
		return liveShaper, nil
	}
	if _, _, err := m.Open(0); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Close() }()
	m.paths[0].setRemote(peerAP)

	// One successful receiver-visible PROBE establishes non-zero debt P0.
	m.emitProbes()
	liveShaper.assertDebits(t, 1, lmax)
	callStart := shaperClock.Now()
	serialization := time.Duration(math.Ceil(float64(lmax) / rate * float64(time.Second)))
	priorityDeadline := callStart.Add(serialization)
	data := wgMsg(wgMessageTransportType, 0, lmax-frame.DataOverhead)
	sendResult := make(chan error, 1)
	go func() {
		sendResult <- m.Send([][]byte{data, data}, m.virt)
	}()
	waitForPriorityDebtTimer(t, shaperClock, priorityDeadline)

	// The permitted coincident post-call Pburst consists of one generated PROBE
	// and one reflected authenticated echo, both exactly Lmax in this fixture.
	m.emitProbes()
	request, err := frame.Encode(psk, frame.Probe{
		PathID:         0,
		ProbeSeq:       99,
		TimestampNanos: telemetryClock.Now().UnixNano(),
	})
	if err != nil {
		t.Fatal(err)
	}
	m.handleInbound(m.paths[0], request, peerAP)
	liveShaper.assertDebits(t, 3, lmax)
	priorityDeadline = priorityDeadline.Add(2 * serialization)
	waitForPriorityDebtTimer(t, shaperClock, priorityDeadline)

	p0 := lmax
	pburst := 2 * lmax
	dp := time.Duration(float64(p0+pburst) / (rate - rp) * float64(time.Second))
	obsoleteP0OverR := time.Duration(float64(p0) / rate * float64(time.Second))
	shaperClock.advanceWithoutFiring(obsoleteP0OverR)
	shaperClock.fireDue()
	select {
	case got := <-emissions:
		t.Fatalf("DATA emitted at obsolete P0/R bound: %+v", got)
	default:
	}

	// Continue the declared Rp as one exact generated PROBE per priorityStep.
	// Account before firing the prior timer at each boundary, matching a
	// coincident priority arrival and preserving the configured worst case.
	elapsed := obsoleteP0OverR
	sustainedDebits := 0
	for nextArrival := priorityStep; nextArrival < dp; nextArrival += priorityStep {
		shaperClock.advanceWithoutFiring(nextArrival - elapsed)
		m.emitProbes()
		sustainedDebits++
		liveShaper.assertDebits(t, 3+sustainedDebits, lmax)
		now := shaperClock.Now()
		if !priorityDeadline.After(now) {
			// Packetized Rp can leave a sub-step gap after the prior debt
			// matures. This arrival linearizes after that maturity and cannot
			// revoke the already-waiting reservation.
			shaperClock.fireDue()
			elapsed = nextArrival
			break
		}
		priorityDeadline = priorityDeadline.Add(serialization)
		waitForPriorityDebtTimer(t, shaperClock, priorityDeadline)
		shaperClock.fireDue()
		elapsed = nextArrival
	}
	if elapsed < dp {
		shaperClock.advanceWithoutFiring(dp - elapsed)
		shaperClock.fireDue()
	}

	// Priority arrivals in the bound use the half-open [call,Dp) interval.
	// The due reservation matures at Dp; a debit linearized at that boundary
	// cannot revoke it and applies only to later reservations/future egress.
	first := waitPriorityEmission(t, emissions)
	if first.at.Sub(callStart) > dp {
		t.Fatalf("first DATA emission at %v, want <= exact Dp %v", first.at.Sub(callStart), dp)
	}
	if first.size != lmax {
		t.Fatalf("first DATA wire length = %d, want Lmax=%d", first.size, lmax)
	}

	shaperClock.advanceWithoutFiring(serialization + time.Nanosecond)
	shaperClock.fireDue()
	second := waitPriorityEmission(t, emissions)
	if second.size != lmax {
		t.Fatalf("second DATA wire length = %d, want Lmax=%d", second.size, lmax)
	}
	q := cfg.DataBurstBytes + cfg.ControlReserveBytes
	localBound := dp +
		time.Duration(float64(q)/rate*float64(time.Second)) +
		time.Duration(float64(lmax)/rate*float64(time.Second))
	if elapsed := second.at.Sub(callStart); elapsed > localBound {
		t.Fatalf("full-B local egress at %v, want <= Dp+Q/R+Lmax/R %v", elapsed, localBound)
	}
	select {
	case err := <-sendResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("full-B DATA send did not complete")
	}
}

func waitPriorityEmission(t testing.TB, emissions <-chan priorityEmission) priorityEmission {
	t.Helper()
	select {
	case emission := <-emissions:
		return emission
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for shaped DATA emission")
		return priorityEmission{}
	}
}

type priorityReceiveResult struct {
	payload []byte
	err     error
}

func receivePriorityPayload(fn ReceiveFunc) <-chan priorityReceiveResult {
	result := make(chan priorityReceiveResult, 1)
	go func() {
		bufs := [][]byte{make([]byte, 2048)}
		sizes := make([]int, 1)
		eps := make([]Endpoint, 1)
		n, err := fn(bufs, sizes, eps)
		if err != nil {
			result <- priorityReceiveResult{err: err}
			return
		}
		if n != 1 {
			result <- priorityReceiveResult{err: fmt.Errorf("receive returned %d datagrams, want 1", n)}
			return
		}
		result <- priorityReceiveResult{payload: append([]byte(nil), bufs[0][:sizes[0]]...)}
	}()
	return result
}

func waitPriorityPayload(t testing.TB, result <-chan priorityReceiveResult) []byte {
	t.Helper()
	select {
	case got := <-result:
		if got.err != nil {
			t.Fatal(got.err)
		}
		return got.payload
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for receiver-visible delivery")
		return nil
	}
}

func TestInnerControlBehindHeterogeneousPathGapWaitsForLowerSequenceThenDelivers(t *testing.T) {
	fn, send := d93Harness(t)

	send(0, 1, []byte("seed"))
	if got := waitPriorityPayload(t, receivePriorityPayload(fn)); string(got) != "seed" {
		t.Fatalf("seed payload = %q", got)
	}

	const simulatedPathGap = 40 * time.Millisecond
	start := time.Now()
	send(2, 2, []byte("inner-control"))
	fastArrival := receivePriorityPayload(fn)
	select {
	case got := <-fastArrival:
		t.Fatalf("higher-sequence inner control overtook lower DATA: payload=%q err=%v", got.payload, got.err)
	case <-time.After(simulatedPathGap / 2):
	}

	time.Sleep(simulatedPathGap / 2)
	send(1, 1, []byte("lower-data"))

	if got := waitPriorityPayload(t, fastArrival); string(got) != "lower-data" {
		t.Fatalf("first post-gap payload = %q, want lower DATA", got)
	}
	if got := waitPriorityPayload(t, receivePriorityPayload(fn)); string(got) != "inner-control" {
		t.Fatalf("second post-gap payload = %q, want inner control", got)
	}
	if elapsed := time.Since(start); elapsed < simulatedPathGap || elapsed >= 200*time.Millisecond {
		t.Fatalf("receiver-visible control delay = %v, want path gap %v plus active resequencer hold below 200ms", elapsed, simulatedPathGap)
	}
}

func TestFullDataBudgetReceiverVisibleBoundIncludesActiveResequencerHold(t *testing.T) {
	receive, receiverSend := d93Harness(t)
	receiverSend(0, 1, []byte("seed"))
	if got := waitPriorityPayload(t, receivePriorityPayload(receive)); string(got) != "seed" {
		t.Fatalf("seed payload = %q", got)
	}

	psk := testKey(t, 0xD9)
	m, _, _ := newProbingMultipath(t, loopbackPaths(1), psk, newFakeClock())
	m.scheduler = &unpacedSelectionRecorder{}
	_, peerAP := rawPeer(t)
	sample, err := frame.Encode(psk, frame.Probe{})
	if err != nil {
		t.Fatal(err)
	}
	lmax := len(sample)
	cfg := config.PathShaperConfig{
		RateBytesPerSecond:      7_500,
		DataBurstBytes:          2 * lmax,
		ControlReserveBytes:     lmax,
		MaxEncodedDatagramBytes: lmax,
		ProbeRateBytesPerSecond: float64(2*lmax) / telemetry.DefaultProbeInterval.Seconds(),
		ProbeBurstBytes:         2 * lmax,
	}
	emissions := make(chan time.Time, 2)
	writeIndex := 0
	m.shaperConfigs = []config.PathShaperConfig{cfg}
	m.newPathShaper = func(got shaper.Config, _ shaper.WriteFunc) (pathShaper, error) {
		return shaper.New(got, shaper.SystemClock{}, func(raw []byte) error {
			decoded, err := frame.Decode(psk, raw)
			if err != nil {
				return err
			}
			data, ok := decoded.(frame.Data)
			if !ok {
				return fmt.Errorf("shaped wire frame is %T, want DATA", decoded)
			}
			writeIndex++
			emissions <- time.Now()
			if writeIndex == 2 {
				// The lower sequence took a slower/lost path. Deliver the full-B
				// suffix on another path id so the real receiver must add its
				// active heterogeneous-path resequencer hold.
				receiverSend(data.OuterSeq, 2, data.Payload)
			}
			return nil
		})
	}
	if _, _, err := m.Open(0); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Close() }()
	m.paths[0].setRemote(peerAP)

	// Establish non-zero P0 through the real generated-PROBE integration.
	m.emitProbes()
	dataA := wgMsg(wgMessageTransportType, 0, lmax-frame.DataOverhead)
	dataB := append([]byte(nil), dataA...)
	dataB[len(dataB)-1] ^= 0x7f
	delivered := receivePriorityPayload(receive)
	start := time.Now()
	if err := m.Send([][]byte{dataA, dataB}, m.virt); err != nil {
		t.Fatal(err)
	}
	<-emissions
	lastEmission := <-emissions
	if got := waitPriorityPayload(t, delivered); string(got) != string(dataB) {
		t.Fatalf("receiver payload length/content mismatch: got %d bytes, want %d", len(got), len(dataB))
	}

	rate := cfg.RateBytesPerSecond
	rp := cfg.ProbeRateBytesPerSecond
	p0 := lmax
	pburst := cfg.ProbeBurstBytes
	dp := time.Duration(float64(p0+pburst) / (rate - rp) * float64(time.Second))
	q := cfg.DataBurstBytes + cfg.ControlReserveBytes
	localBound := dp +
		time.Duration(float64(q)/rate*float64(time.Second)) +
		time.Duration(float64(lmax)/rate*float64(time.Second))
	if elapsed := lastEmission.Sub(start); elapsed > localBound {
		t.Fatalf("full-B local egress at %v, want <= Dp+Q/R+Lmax/R %v", elapsed, localBound)
	}
	elapsed := time.Since(start)
	if elapsed < 200*time.Millisecond {
		t.Fatalf("receiver delivered after %v, want the active heterogeneous-path resequencer hold", elapsed)
	}
	if receiverBound := localBound + resequencerTimeout; elapsed > receiverBound {
		t.Fatalf("receiver-visible full-B delivery at %v, want <= local bound + active hold %v", elapsed, receiverBound)
	}
}
