//go:build failfirst

package bind

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/adaptivefec"
	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/fec"
	"github.com/7mind/wanbond/internal/frame"
	"github.com/7mind/wanbond/internal/log"
	"github.com/7mind/wanbond/internal/shaper"
	"go.uber.org/goleak"
)

const (
	failFirstDispatchSLO = 10 * time.Millisecond
	failFirstSpinLimit   = 100_000
)

var errFailFirstWriter = errors.New("failfirst terminal writer error")

type failFirstEvent struct {
	at          time.Time
	name        string
	detail      string
	frame       frame.Frame
	openAtWrite bool
}

type failFirstWriter struct {
	mu      sync.Mutex
	clock   *fakeClock
	encoder *fec.Encoder
	codec   *frame.Codec
	events  []failFirstEvent

	accepted   int
	emitted    int
	errors     int
	failedCall []frame.Frame

	failDecided  bool
	failed       bool
	blockDecided bool
	blocked      bool
	entered      chan struct{}
	release      chan struct{}
	enterOnce    sync.Once
	releaseOnce  sync.Once
	groupOpen    func() bool
}

func (w *failFirstWriter) record(name, detail string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.events = append(w.events, failFirstEvent{at: w.clock.Now(), name: name, detail: detail})
}

func (w *failFirstWriter) WriteDatagrams(_ context.Context, datagrams []shaper.Datagram) (shaper.BatchResult, error) {
	w.mu.Lock()
	open := w.groupOpen()
	if !open {
		w.events = append(w.events, failFirstEvent{at: w.clock.Now(), name: "decision-ready-observed"})
	}
	decodedFrames := make([]frame.Frame, 0, len(datagrams))
	for _, datagram := range datagrams {
		decoded, err := w.codec.Decode(datagram.Payload)
		if err != nil {
			w.mu.Unlock()
			return shaper.BatchResult{FailedIndex: -1}, fmt.Errorf("failfirst decode writer datagram: %w", err)
		}
		decodedFrames = append(decodedFrames, decoded)
		name := fmt.Sprintf("writer-%T", decoded)
		switch decoded.(type) {
		case frame.Data:
			name = "writer-DATA"
		case frame.Parity:
			name = "writer-PARITY"
		}
		w.events = append(w.events, failFirstEvent{
			at:          w.clock.Now(),
			name:        name,
			frame:       decoded,
			openAtWrite: open,
		})
	}
	shouldBlock := w.blockDecided && !open && !w.blocked
	if shouldBlock {
		w.blocked = true
	}
	shouldFail := w.failDecided && !open && !w.failed
	if shouldFail {
		w.failed = true
		w.failedCall = append([]frame.Frame(nil), decodedFrames...)
		w.accepted += len(datagrams)
		w.errors += len(datagrams)
		w.events = append(w.events, failFirstEvent{
			at:     w.clock.Now(),
			name:   "writer-error",
			detail: fmt.Sprintf("%s failed-tranche=%d", errFailFirstWriter, len(datagrams)),
		})
	}
	if !shouldFail {
		w.accepted += len(datagrams)
		w.emitted += len(datagrams)
	}
	w.mu.Unlock()

	if shouldBlock {
		w.enterOnce.Do(func() { close(w.entered) })
		<-w.release
	}
	if shouldFail {
		return shaper.BatchResult{Accepted: len(datagrams), Emitted: 0, FailedIndex: 0}, errFailFirstWriter
	}
	return shaper.BatchResult{Accepted: len(datagrams), Emitted: len(datagrams), FailedIndex: -1}, nil
}

func (*failFirstWriter) AccountPriority(int) error { return nil }

func (w *failFirstWriter) Close() error {
	if w.release != nil {
		w.releaseOnce.Do(func() { close(w.release) })
	}
	return nil
}

func (w *failFirstWriter) snapshot() (events []failFirstEvent, accepted, emitted, writeErrors int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]failFirstEvent(nil), w.events...), w.accepted, w.emitted, w.errors
}

func (w *failFirstWriter) failedTranche() []frame.Frame {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]frame.Frame(nil), w.failedCall...)
}

func (w *failFirstWriter) trace() string {
	events, accepted, emitted, writeErrors := w.snapshot()
	var b strings.Builder
	for i, event := range events {
		fmt.Fprintf(&b, "%02d t=%s %s", i+1, event.at.Format(time.RFC3339Nano), event.name)
		if event.openAtWrite {
			b.WriteString(" group-open=true")
		}
		if event.detail != "" {
			fmt.Fprintf(&b, " %s", event.detail)
		}
		if i+1 < len(events) {
			b.WriteString("; ")
		}
	}
	fmt.Fprintf(&b, " | accepted=%d emitted=%d errors=%d", accepted, emitted, writeErrors)
	return b.String()
}

type failFirstFixture struct {
	t      testing.TB
	m      *Multipath
	cfg    fec.Config
	clock  *fakeClock
	writer *failFirstWriter
}

type failFirstSubmission struct {
	done chan error
}

func newFailFirstFixture(t testing.TB, cfg fec.Config, adaptiveCfg *adaptivefec.Config, configureWriter func(*failFirstWriter)) *failFirstFixture {
	t.Helper()
	clk := newFakeClock()
	psk := testKey(t, 0x35)
	codec, err := frame.NewCodec(psk)
	if err != nil {
		t.Fatal(err)
	}
	writer := &failFirstWriter{clock: clk, codec: codec}
	if configureWriter != nil {
		configureWriter(writer)
	}
	lg, err := log.New("error", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	bindCfg := cfg
	bindCfg.Deadline = maxFECDeadline
	m, err := NewMultipathWithShapers(
		loopbackPaths(1),
		psk,
		&unpacedSelectionRecorder{},
		nil,
		nil,
		&bindCfg,
		adaptiveCfg,
		config.Amnezia{},
		[]config.PathShaperConfig{{
			RateBytesPerSecond:      10_000_000,
			DataBurstBytes:          1472,
			ControlReserveBytes:     1472,
			MaxEncodedDatagramBytes: 1472,
			ProbeRateBytesPerSecond: 1,
			ProbeBurstBytes:         2944,
		}},
		lg,
	)
	if err != nil {
		t.Fatal(err)
	}
	m.newPathShaper = func(shaper.Config, shaper.WriteFunc) (pathShaper, error) {
		return writer, nil
	}
	m.clock = clk
	if _, _, err := m.Open(0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	m.paths[0].setRemote(netip.MustParseAddrPort("127.0.0.1:9"))

	enc, err := fec.NewEncoder(cfg, clk)
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	fs := m.fecSend.Load()
	if fs.ctrl != nil {
		enc.SetParity(fs.ctrl.Parity())
	}
	fs.enc = enc
	m.mu.Unlock()
	writer.encoder = enc
	writer.groupOpen = func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		_, open := enc.NextDeadline()
		return open
	}
	return &failFirstFixture{t: t, m: m, cfg: cfg, clock: clk, writer: writer}
}

func (f *failFirstFixture) submit(payloads ...[]byte) *failFirstSubmission {
	submission := &failFirstSubmission{done: make(chan error, 1)}
	go func() {
		submission.done <- f.m.Send(payloads, f.m.virt)
	}()
	return submission
}

func (f *failFirstFixture) await(submission *failFirstSubmission) error {
	for i := 0; i < failFirstSpinLimit; i++ {
		select {
		case err := <-submission.done:
			return err
		default:
			runtime.Gosched()
		}
	}
	f.t.Fatal("submission did not resolve within bounded scheduler yields")
	return nil
}

func (f *failFirstFixture) waitGroupOpen() time.Time {
	for i := 0; i < failFirstSpinLimit; i++ {
		f.m.mu.Lock()
		due, open := f.writer.encoder.NextDeadline()
		f.m.mu.Unlock()
		if open {
			f.writer.record("group-open", fmt.Sprintf("due=%s", due.Format(time.RFC3339Nano)))
			return due
		}
		runtime.Gosched()
	}
	f.t.Fatal("FEC group did not open within bounded scheduler yields")
	return time.Time{}
}

func (f *failFirstFixture) waitDecision() {
	if !f.decisionReadyWithinSpins() {
		f.t.Fatal("FEC decision did not complete within bounded scheduler yields")
	}
}

func (f *failFirstFixture) decisionReadyWithinSpins() bool {
	for i := 0; i < failFirstSpinLimit; i++ {
		f.m.mu.Lock()
		_, open := f.writer.encoder.NextDeadline()
		f.m.mu.Unlock()
		if !open {
			f.writer.record("decision-ready", "")
			return true
		}
		runtime.Gosched()
	}
	return false
}

func (f *failFirstFixture) waitWriterBlocked() {
	for i := 0; i < failFirstSpinLimit; i++ {
		select {
		case <-f.writer.entered:
			return
		default:
			runtime.Gosched()
		}
	}
	f.t.Fatal("decided-group writer did not block within bounded scheduler yields")
}

func (f *failFirstFixture) flush() {
	f.writer.record("deadline-fire", "")
	f.m.fecFlushDeadline()
}

func (f *failFirstFixture) assertNoWriterWhileOpen() {
	f.t.Helper()
	events, _, _, _ := f.writer.snapshot()
	for _, event := range events {
		if event.frame != nil && event.openAtWrite {
			f.t.Errorf("%s became writer-visible before its immutable FEC decision; trace: %s", event.name, f.writer.trace())
			return
		}
	}
}

func frameCounts(events []failFirstEvent) (data, parity int) {
	for _, event := range events {
		switch event.frame.(type) {
		case frame.Data:
			data++
		case frame.Parity:
			parity++
		}
	}
	return data, parity
}

func assertGroupFrames(t testing.TB, events []failFirstEvent, group uint32, wantData, wantParity int) {
	t.Helper()
	dataIndices := make(map[uint8]struct{}, wantData)
	parityIndices := make(map[uint16]struct{}, wantParity)
	outerSeqs := make(map[uint64]struct{}, wantData)
	for _, event := range events {
		switch decoded := event.frame.(type) {
		case frame.Data:
			if decoded.FECGroup == group {
				if decoded.OuterSeq == 0 {
					t.Fatalf("group %d DATA index %d has zero OuterSeq", group, decoded.FECIndex)
				}
				if _, duplicate := dataIndices[decoded.FECIndex]; duplicate {
					t.Fatalf("group %d duplicated DATA index %d", group, decoded.FECIndex)
				}
				if _, duplicate := outerSeqs[decoded.OuterSeq]; duplicate {
					t.Fatalf("group %d duplicated DATA OuterSeq %d", group, decoded.OuterSeq)
				}
				dataIndices[decoded.FECIndex] = struct{}{}
				outerSeqs[decoded.OuterSeq] = struct{}{}
			}
		case frame.Parity:
			if decoded.FECGroup == group {
				if _, duplicate := parityIndices[decoded.ParityIndex]; duplicate {
					t.Fatalf("group %d duplicated PARITY index %d", group, decoded.ParityIndex)
				}
				if int(decoded.DataCount) != wantData {
					t.Fatalf("group %d PARITY DataCount = %d, want %d", group, decoded.DataCount, wantData)
				}
				parityIndices[decoded.ParityIndex] = struct{}{}
			}
		}
	}
	if len(dataIndices) != wantData || len(parityIndices) != wantParity {
		t.Fatalf("group %d DATA/PARITY = %d/%d, want %d/%d", group, len(dataIndices), len(parityIndices), wantData, wantParity)
	}
	for i := 0; i < wantData; i++ {
		if _, ok := dataIndices[uint8(i)]; !ok {
			t.Fatalf("group %d missing DATA index %d", group, i)
		}
	}
	for i := 0; i < wantParity; i++ {
		if _, ok := parityIndices[uint16(i)]; !ok {
			t.Fatalf("group %d missing PARITY index %d", group, i)
		}
	}
}

func TestFailFirstFECSizeAndDeadlineDecisionOrdering(t *testing.T) {
	t.Run("Kdata-size-close", func(t *testing.T) {
		cfg := fec.Config{DataShards: 2, ParityShards: 1, Deadline: 80 * time.Millisecond}
		f := newFailFirstFixture(t, cfg, nil, nil)
		send := f.submit([]byte("size-a"), []byte("size-b"))
		if err := f.await(send); err != nil {
			t.Fatal(err)
		}
		f.waitDecision()
		events, _, _, _ := f.writer.snapshot()
		assertGroupFrames(t, events, 0, 2, 1)
		f.assertNoWriterWhileOpen()
		t.Logf("size-close trace: %s", f.writer.trace())
	})

	t.Run("partial-deadline-close", func(t *testing.T) {
		cfg := fec.Config{DataShards: 4, ParityShards: 1, Deadline: 80 * time.Millisecond}
		f := newFailFirstFixture(t, cfg, nil, nil)
		send := f.submit([]byte("partial"))
		f.waitGroupOpen()
		f.assertNoWriterWhileOpen()
		f.clock.advance(cfg.Deadline)
		f.flush()
		f.waitDecision()
		if err := f.await(send); err != nil {
			t.Fatal(err)
		}
		events, _, _, _ := f.writer.snapshot()
		assertGroupFrames(t, events, 0, 1, 1)
		f.assertNoWriterWhileOpen()
		t.Logf("deadline-close trace: %s", f.writer.trace())
	})
}

func TestFailFirstFECDeadlineDispatchBound(t *testing.T) {
	cfg := fec.Config{DataShards: 4, ParityShards: 1, Deadline: 80 * time.Millisecond}

	t.Run("periodic-phase-can-close-near-2D", func(t *testing.T) {
		f := newFailFirstFixture(t, cfg, nil, nil)
		f.clock.advance(time.Millisecond)
		send := f.submit([]byte("phase-offset"))
		due := f.waitGroupOpen()

		f.clock.advance(cfg.Deadline - time.Millisecond)
		f.flush()
		if open := f.writer.groupOpen(); !open {
			t.Fatal("phase fixture closed at the first periodic tick before Encoder.NextDeadline")
		}
		f.clock.advance(cfg.Deadline)
		f.flush()
		f.waitDecision()
		if err := f.await(send); err != nil {
			t.Fatal(err)
		}
		decisionAt := f.clock.Now()
		f.assertNoWriterWhileOpen()
		t.Logf("phase trace: %s", f.writer.trace())
		if decisionAt.After(due.Add(failFirstDispatchSLO)) {
			t.Errorf("periodic dispatch decided at openedAt+%s, after D+G=%s",
				decisionAt.Sub(due.Add(-cfg.Deadline)), cfg.Deadline+failFirstDispatchSLO)
		}
	})

	t.Run("owner-boundary-contention-prioritizes-decision-over-two-queued-admissions", func(t *testing.T) {
		f := newFailFirstFixture(t, cfg, nil, nil)
		owner := newFailFirstOwnerAdapter(f)
		defer owner.close()
		executing := owner.submitAdmission([]byte("owner-boundary"), true)
		owner.waitExecutingBoundary()
		f.clock.advance(cfg.Deadline)
		f.writer.record("deadline-fire", "contention=true executingAdmission=1")
		f.m.fecFlushDeadline()

		first := owner.submitAdmission([]byte("queued-1"), false)
		second := owner.submitAdmission([]byte("queued-2"), false)
		f.writer.record("queued-admissions", "queuedAdmissions=2")
		owner.releaseExecutingBoundary()
		if err := f.await(executing); err != nil {
			t.Fatal(err)
		}
		if err := f.await(first); err != nil {
			t.Fatal(err)
		}
		if err := f.await(second); err != nil {
			t.Fatal(err)
		}
		if !f.decisionReadyWithinSpins() {
			t.Errorf("due decision was not ready at the owner-command boundary before two queued admissions")
		}
		if err := f.await(owner.submitDecisionAndEmit()); err != nil {
			t.Fatal(err)
		}

		events, _, _, _ := f.writer.snapshot()
		decisionIndex := -1
		firstGroupWriter := -1
		payloadCounts := map[string]int{}
		for i, event := range events {
			if strings.HasPrefix(event.name, "decision-ready") && decisionIndex < 0 {
				decisionIndex = i
			}
			if decoded, ok := event.frame.(frame.Data); ok {
				payload := string(decoded.Payload)
				if payload == "owner-boundary" || payload == "queued-1" || payload == "queued-2" {
					payloadCounts[payload]++
					if firstGroupWriter < 0 {
						firstGroupWriter = i
					}
				}
			}
		}
		t.Logf("contention trace: %s", f.writer.trace())
		if decisionIndex < 0 || firstGroupWriter < 0 || decisionIndex > firstGroupWriter {
			t.Errorf("due decision did not precede executing+queued admissions becoming writer-visible: decision=%d firstGroupWriter=%d", decisionIndex, firstGroupWriter)
		}
		for _, payload := range []string{"owner-boundary", "queued-1", "queued-2"} {
			if payloadCounts[payload] != 1 {
				t.Errorf("payload %q became writer-visible %d times, want exactly once", payload, payloadCounts[payload])
			}
		}
		assertGroupFrames(t, events, 0, 3, 1)
		f.assertNoWriterWhileOpen()
	})
}

type failFirstOwnerCommand struct {
	payload  []byte
	pause    bool
	decision bool
	done     chan error
}

type failFirstStagedData struct {
	shard   fec.DataShard
	outer   uint64
	payload []byte
}

// failFirstOwnerAdapter is a real buffered owner-command mailbox. Its current loop
// mirrors the production defect by choosing already-queued admission commands after
// a post-Admit boundary instead of re-checking the expired Encoder.NextDeadline.
// T309 retargets this one adapter to the production owner without changing assertions.
type failFirstOwnerAdapter struct {
	fixture      *failFirstFixture
	mailbox      chan failFirstOwnerCommand
	boundary     chan struct{}
	release      chan struct{}
	closed       chan struct{}
	boundaryOnce sync.Once
	releaseOnce  sync.Once
	staged       []failFirstStagedData
}

func newFailFirstOwnerAdapter(fixture *failFirstFixture) *failFirstOwnerAdapter {
	adapter := &failFirstOwnerAdapter{
		fixture:  fixture,
		mailbox:  make(chan failFirstOwnerCommand, 8),
		boundary: make(chan struct{}),
		release:  make(chan struct{}),
		closed:   make(chan struct{}),
	}
	go adapter.run()
	return adapter
}

func (a *failFirstOwnerAdapter) submitAdmission(payload []byte, pause bool) *failFirstSubmission {
	submission := &failFirstSubmission{done: make(chan error, 1)}
	a.mailbox <- failFirstOwnerCommand{payload: append([]byte(nil), payload...), pause: pause, done: submission.done}
	return submission
}

func (a *failFirstOwnerAdapter) submitDecisionAndEmit() *failFirstSubmission {
	submission := &failFirstSubmission{done: make(chan error, 1)}
	a.mailbox <- failFirstOwnerCommand{decision: true, done: submission.done}
	return submission
}

func (a *failFirstOwnerAdapter) waitExecutingBoundary() {
	for i := 0; i < failFirstSpinLimit; i++ {
		select {
		case <-a.boundary:
			return
		default:
			runtime.Gosched()
		}
	}
	a.fixture.t.Fatal("owner command did not reach the post-Admit boundary")
}

func (a *failFirstOwnerAdapter) releaseExecutingBoundary() {
	a.releaseOnce.Do(func() { close(a.release) })
}

func (a *failFirstOwnerAdapter) close() {
	close(a.mailbox)
	<-a.closed
}

func (a *failFirstOwnerAdapter) run() {
	defer close(a.closed)
	for command := range a.mailbox {
		if command.decision {
			command.done <- a.decideAndEmit()
			continue
		}
		a.fixture.m.mu.Lock()
		if !command.pause {
			if due, open := a.fixture.writer.encoder.NextDeadline(); open && !a.fixture.clock.Now().Before(due) {
				a.fixture.writer.record("owner-choice", "mailbox-admission-before-expired-deadline")
			}
		}
		outer := a.fixture.m.peerState.outerSeq.Add(1)
		shard, parity, err := a.fixture.writer.encoder.Admit(fecShardPayload(outer, command.payload))
		if err == nil && parity != nil {
			err = errors.New("owner mailbox admission unexpectedly size-closed the test group")
		}
		if err == nil {
			a.staged = append(a.staged, failFirstStagedData{
				shard:   shard,
				outer:   outer,
				payload: append([]byte(nil), command.payload...),
			})
		}
		if command.pause {
			a.fixture.writer.record("owner-command-paused", "executingAdmission=1 boundary=post-Admit-not-inside-Encoder.Admit")
			a.boundaryOnce.Do(func() { close(a.boundary) })
			<-a.release
			a.fixture.writer.record("owner-command-release", "")
		}
		a.fixture.m.mu.Unlock()
		command.done <- err
	}
}

func (a *failFirstOwnerAdapter) decideAndEmit() error {
	a.fixture.flush()
	a.fixture.waitDecision()

	a.fixture.m.mu.Lock()
	path := a.fixture.m.peerState.paths[0]
	datagrams := make([]shaper.Datagram, 0, len(a.staged))
	for _, staged := range a.staged {
		wire, err := a.fixture.m.peerState.sendCodec.Encode(nil, frame.Data{
			OuterSeq: staged.outer,
			PathID:   path.id,
			FECGroup: uint32(staged.shard.Group),
			FECIndex: uint8(staged.shard.Index),
			Payload:  staged.payload,
		})
		if err != nil {
			a.fixture.m.mu.Unlock()
			return err
		}
		datagrams = append(datagrams, shaper.Datagram{Class: shaper.ClassData, Payload: wire})
	}
	a.fixture.m.mu.Unlock()
	result, err := a.fixture.writer.WriteDatagrams(context.Background(), datagrams)
	if err != nil {
		return err
	}
	if result.Accepted != len(datagrams) || result.Emitted != len(datagrams) {
		return fmt.Errorf("owner staged DATA result accepted/emitted=%d/%d, want %d/%d",
			result.Accepted, result.Emitted, len(datagrams), len(datagrams))
	}
	return nil
}

func TestFailFirstAdaptiveM0SnapshotIsImmutable(t *testing.T) {
	cfg := fec.Config{DataShards: 2, ParityShards: 1, Deadline: 80 * time.Millisecond}
	adaptiveCfg := adaptivefec.DefaultConfig()
	adaptiveCfg.DataShards = cfg.DataShards
	adaptiveCfg.MaxParity = cfg.ParityShards
	f := newFailFirstFixture(t, cfg, &adaptiveCfg, nil)
	fs := f.m.fecSend.Load()
	if fs.ctrl == nil || fs.ctrl.Parity() != 0 {
		t.Fatalf("adaptive controller did not initialize M=0")
	}

	first := f.submit([]byte("adaptive-zero"))
	f.waitGroupOpen()
	f.assertNoWriterWhileOpen()
	f.m.mu.Lock()
	target := fs.ctrl.Observe(1)
	fs.enc.SetParity(target)
	f.m.mu.Unlock()
	if target != 1 {
		t.Fatalf("adaptive controller target after loss = %d, want 1", target)
	}
	f.writer.record("adaptive-target-change", "M=0->M=1")
	f.clock.advance(cfg.Deadline)
	f.flush()
	f.waitDecision()
	if err := f.await(first); err != nil {
		t.Fatal(err)
	}
	events, _, _, _ := f.writer.snapshot()
	assertGroupFrames(t, events, 0, 1, 0)

	next := f.submit([]byte("next-a"), []byte("next-b"))
	if err := f.await(next); err != nil {
		t.Fatal(err)
	}
	f.waitDecision()
	events, _, _, _ = f.writer.snapshot()
	assertGroupFrames(t, events, 1, 2, 1)
	f.assertNoWriterWhileOpen()
	t.Logf("adaptive trace: %s", f.writer.trace())
}

func TestFailFirstFECDecidedGroupWriterErrorIsTerminal(t *testing.T) {
	cfg := fec.Config{DataShards: 2, ParityShards: 1, Deadline: 80 * time.Millisecond}
	f := newFailFirstFixture(t, cfg, nil, func(writer *failFirstWriter) {
		writer.failDecided = true
	})
	send := f.submit([]byte("error-a"), []byte("error-b"))
	err := f.await(send)
	if !errors.Is(err, errFailFirstWriter) {
		t.Fatalf("size-close Send error = %v, want sentinel %v", err, errFailFirstWriter)
	}
	f.writer.record("send-resolved", errFailFirstWriter.Error())
	eventsBefore, accepted, emitted, writeErrors := f.writer.snapshot()
	failedTranche := f.writer.failedTranche()
	if len(failedTranche) < 2 {
		t.Fatalf("decided failing writer tranche has %d frames, want multiple group frames", len(failedTranche))
	}
	if accepted != emitted+writeErrors || writeErrors != len(failedTranche) {
		t.Fatalf("writer accounting accepted/emitted/errors = %d/%d/%d, failed tranche=%d; want accepted=emitted+terminal-errors for every tranche member",
			accepted, emitted, writeErrors, len(failedTranche))
	}
	var failedGroup uint32
	for i, failed := range failedTranche {
		var group uint32
		switch decoded := failed.(type) {
		case frame.Data:
			group = decoded.FECGroup
		case frame.Parity:
			group = decoded.FECGroup
		default:
			t.Fatalf("failed decided tranche frame %d has type %T, want DATA/PARITY", i, failed)
		}
		if i == 0 {
			failedGroup = group
		} else if group != failedGroup {
			t.Fatalf("failed decided tranche spans groups %d and %d", failedGroup, group)
		}
	}
	errorIndex := -1
	for i, event := range eventsBefore {
		if event.name == "writer-error" {
			errorIndex = i
			break
		}
	}
	if errorIndex < 0 {
		t.Fatal("terminal writer error event missing")
	}
	for _, event := range eventsBefore[errorIndex+1:] {
		switch decoded := event.frame.(type) {
		case frame.Data:
			if decoded.FECGroup == failedGroup {
				t.Fatalf("same-group DATA retry became writer-visible before Send resolved")
			}
		case frame.Parity:
			if decoded.FECGroup == failedGroup {
				t.Fatalf("same-group PARITY retry became writer-visible before Send resolved")
			}
		}
	}
	if open := f.writer.groupOpen(); open {
		t.Fatal("writer error reopened decided group")
	}
	f.flush()
	eventsAfter, acceptedAfter, emittedAfter, errorsAfter := f.writer.snapshot()
	if acceptedAfter != accepted || emittedAfter != emitted || errorsAfter != writeErrors {
		t.Fatalf("decided-group writer accounting changed after terminal error: before=%d/%d/%d after=%d/%d/%d",
			accepted, emitted, writeErrors, acceptedAfter, emittedAfter, errorsAfter)
	}
	for _, event := range eventsAfter[len(eventsBefore):] {
		switch decoded := event.frame.(type) {
		case frame.Data:
			if decoded.FECGroup == failedGroup {
				t.Fatalf("same-group DATA retry became writer-visible after extra deadline fire")
			}
		case frame.Parity:
			if decoded.FECGroup == failedGroup {
				t.Fatalf("same-group PARITY retry became writer-visible after extra deadline fire")
			}
		}
	}
	f.assertNoWriterWhileOpen()
	t.Logf("writer-error trace: %s", f.writer.trace())
}

func TestFailFirstFECCloseBeforeAndAfterDueHasNoLateParity(t *testing.T) {
	cfg := fec.Config{DataShards: 4, ParityShards: 1, Deadline: 80 * time.Millisecond}
	t.Run("before-due", func(t *testing.T) {
		f := newFailFirstFixture(t, cfg, nil, nil)
		send := f.submit([]byte("close-before"))
		f.waitGroupOpen()
		if err := f.m.Close(); err != nil {
			t.Fatal(err)
		}
		_ = f.await(send)
		f.clock.advance(cfg.Deadline)
		f.flush()
		events, _, _, _ := f.writer.snapshot()
		_, parity := frameCounts(events)
		if parity != 0 {
			t.Fatalf("Close before due emitted %d late parity frames, want 0", parity)
		}
		f.assertNoWriterWhileOpen()
		t.Logf("close-before trace: %s", f.writer.trace())
	})

	t.Run("after-due", func(t *testing.T) {
		f := newFailFirstFixture(t, cfg, nil, nil)
		send := f.submit([]byte("close-after"))
		f.waitGroupOpen()
		f.clock.advance(cfg.Deadline)
		f.flush()
		f.waitDecision()
		if err := f.await(send); err != nil {
			t.Fatal(err)
		}
		if err := f.m.Close(); err != nil {
			t.Fatal(err)
		}
		f.flush()
		events, _, _, _ := f.writer.snapshot()
		assertGroupFrames(t, events, 0, 1, 1)
		f.assertNoWriterWhileOpen()
		t.Logf("close-after trace: %s", f.writer.trace())
	})
}

func TestFailFirstFECSizeClosedBlockedWriterResolvesQueuedWaitersWithoutLeak(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	cfg := fec.Config{DataShards: 2, ParityShards: 1, Deadline: 80 * time.Millisecond}
	f := newFailFirstFixture(t, cfg, nil, func(writer *failFirstWriter) {
		writer.blockDecided = true
		writer.entered = make(chan struct{})
		writer.release = make(chan struct{})
	})
	first := f.submit([]byte("blocking-a"), []byte("blocking-b"))
	f.waitWriterBlocked()
	second := f.submit([]byte("queued-a"))
	third := f.submit([]byte("queued-b"))
	f.writer.record("queued-admissions", "queuedAdmissions=2")
	if err := f.m.Close(); err != nil {
		t.Fatal(err)
	}
	_ = f.await(first)
	_ = f.await(second)
	_ = f.await(third)
	f.assertNoWriterWhileOpen()
	t.Logf("blocked-waiter trace: %s", f.writer.trace())
}
