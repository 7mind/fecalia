//go:build failfirst

package bind

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/adaptivefec"
	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/fec"
	"github.com/7mind/wanbond/internal/frame"
	"github.com/7mind/wanbond/internal/log"
	"github.com/7mind/wanbond/internal/shaper"
	"github.com/7mind/wanbond/internal/telemetry"
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
	mu     sync.Mutex
	clock  *fakeClock
	codec  *frame.Codec
	events []failFirstEvent

	accepted             int
	emitted              int
	errors               int
	decidedTranche       []frame.Frame
	failedCall           []frame.Frame
	failedTrancheEmitted int
	failedTrancheErrors  int
	callSizes            []int

	failDecided  bool
	failEmitted  int
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
	w.callSizes = append(w.callSizes, len(datagrams))
	open := w.groupOpen()
	decodedFrames := make([]frame.Frame, 0, len(datagrams))
	for _, datagram := range datagrams {
		decoded, err := w.codec.Decode(datagram.Payload)
		if err != nil {
			w.mu.Unlock()
			return shaper.BatchResult{FailedIndex: -1}, fmt.Errorf("failfirst decode writer datagram: %w", err)
		}
		decodedFrames = append(decodedFrames, decoded)
	}
	if !open {
		groups := make(map[uint32]struct{})
		for _, decoded := range decodedFrames {
			switch decoded := decoded.(type) {
			case frame.Data:
				groups[decoded.FECGroup] = struct{}{}
			case frame.Parity:
				groups[decoded.FECGroup] = struct{}{}
			}
		}
		for group := range groups {
			w.events = append(w.events, failFirstEvent{
				at:     w.clock.Now(),
				name:   "group-decision-observed",
				detail: fmt.Sprintf("group=%d", group),
			})
		}
	}
	for _, decoded := range decodedFrames {
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
	if w.failDecided && !open && !w.failed {
		w.decidedTranche = append(w.decidedTranche, decodedFrames...)
	}
	shouldFail := w.failDecided && !open && !w.failed && len(w.decidedTranche) >= 2
	if shouldFail {
		emittedPrefix := min(w.failEmitted, len(datagrams))
		w.failed = true
		w.failedCall = append([]frame.Frame(nil), w.decidedTranche...)
		w.accepted += len(datagrams)
		w.emitted += emittedPrefix
		w.errors += len(datagrams) - emittedPrefix
		w.failedTrancheEmitted += emittedPrefix
		w.failedTrancheErrors += len(datagrams) - emittedPrefix
		w.events = append(w.events, failFirstEvent{
			at:     w.clock.Now(),
			name:   "writer-error",
			detail: fmt.Sprintf("%s failed-tranche=%d failed-call=%d", errFailFirstWriter, len(w.failedCall), len(datagrams)),
		})
	}
	if !shouldFail {
		w.accepted += len(datagrams)
		w.emitted += len(datagrams)
		if w.failDecided && !open && !w.failed {
			w.failedTrancheEmitted += len(datagrams)
		}
	}
	w.mu.Unlock()

	if shouldBlock {
		w.enterOnce.Do(func() { close(w.entered) })
		<-w.release
	}
	if shouldFail {
		return shaper.BatchResult{
			Accepted:    len(datagrams),
			Emitted:     min(w.failEmitted, len(datagrams)),
			FailedIndex: min(w.failEmitted, len(datagrams)),
		}, errFailFirstWriter
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

func (w *failFirstWriter) failedTranche() ([]frame.Frame, int, int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]frame.Frame(nil), w.failedCall...), w.failedTrancheEmitted, w.failedTrancheErrors
}

func (w *failFirstWriter) calls() []int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]int(nil), w.callSizes...)
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
	m, err := NewMultipathWithShapers(
		loopbackPaths(1),
		psk,
		&unpacedSelectionRecorder{},
		nil,
		nil,
		&cfg,
		adaptiveCfg,
		config.Amnezia{},
		[]config.PathShaperConfig{{
			RateBytesPerSecond:      10_000_000,
			DataBurstBytes:          1472,
			ControlReserveBytes:     1472,
			MaxEncodedDatagramBytes: 1472,
			ProbeRateBytesPerSecond: 1,
			ProbeBurstBytes:         2944,
			PriorityReserveBytes:    2944,
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

	fs := m.fecSend.Load()
	writer.groupOpen = func() bool {
		return fs.openDeadlineNanos.Load() != 0
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
		if nanos := f.m.fecSend.Load().openDeadlineNanos.Load(); nanos != 0 {
			due := time.Unix(0, nanos)
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
		if f.m.fecSend.Load().openDeadlineNanos.Load() == 0 {
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
	if err := checkGroupFrames(events, group, wantData, wantParity); err != nil {
		t.Fatal(err)
	}
}

func checkGroupFrames(events []failFirstEvent, group uint32, wantData, wantParity int) error {
	dataIndices := make(map[uint8]struct{}, wantData)
	parityIndices := make(map[uint16]struct{}, wantParity)
	outerSeqs := make(map[uint64]struct{}, wantData)
	for _, event := range events {
		switch decoded := event.frame.(type) {
		case frame.Data:
			if decoded.FECGroup == group {
				if decoded.OuterSeq == 0 {
					return fmt.Errorf("group %d DATA index %d has zero OuterSeq", group, decoded.FECIndex)
				}
				if _, duplicate := dataIndices[decoded.FECIndex]; duplicate {
					return fmt.Errorf("group %d duplicated DATA index %d", group, decoded.FECIndex)
				}
				if _, duplicate := outerSeqs[decoded.OuterSeq]; duplicate {
					return fmt.Errorf("group %d duplicated DATA OuterSeq %d", group, decoded.OuterSeq)
				}
				dataIndices[decoded.FECIndex] = struct{}{}
				outerSeqs[decoded.OuterSeq] = struct{}{}
			}
		case frame.Parity:
			if decoded.FECGroup == group {
				if _, duplicate := parityIndices[decoded.ParityIndex]; duplicate {
					return fmt.Errorf("group %d duplicated PARITY index %d", group, decoded.ParityIndex)
				}
				if int(decoded.DataCount) != wantData {
					return fmt.Errorf("group %d PARITY DataCount = %d, want %d", group, decoded.DataCount, wantData)
				}
				parityIndices[decoded.ParityIndex] = struct{}{}
			}
		}
	}
	if len(dataIndices) != wantData || len(parityIndices) != wantParity {
		return fmt.Errorf("group %d DATA/PARITY = %d/%d, want %d/%d", group, len(dataIndices), len(parityIndices), wantData, wantParity)
	}
	for i := 0; i < wantData; i++ {
		if _, ok := dataIndices[uint8(i)]; !ok {
			return fmt.Errorf("group %d missing DATA index %d", group, i)
		}
	}
	for i := 0; i < wantParity; i++ {
		if _, ok := parityIndices[uint16(i)]; !ok {
			return fmt.Errorf("group %d missing PARITY index %d", group, i)
		}
	}
	return nil
}

func groupDecisionIndex(events []failFirstEvent, group uint32) int {
	want := fmt.Sprintf("group=%d", group)
	for i, event := range events {
		if event.name == "group-decision-observed" && event.detail == want {
			return i
		}
	}
	return -1
}

func commandAdmissionIndex(events []failFirstEvent, payload string) int {
	want := "payload=" + payload + " "
	for i, event := range events {
		if event.name == "command-admitted" && strings.HasPrefix(event.detail, want) {
			return i
		}
	}
	return -1
}

func assertPayloadAfterGroupDecision(t testing.TB, events []failFirstEvent, payload string, group uint32) {
	t.Helper()
	decision := groupDecisionIndex(events, group)
	writer := -1
	for i, event := range events {
		if decoded, ok := event.frame.(frame.Data); ok && string(decoded.Payload) == payload {
			writer = i
			break
		}
	}
	if decision < 0 || writer < 0 || decision > writer {
		t.Errorf("payload %q group %d writer visibility did not follow its group decision: decision=%d writer=%d", payload, group, decision, writer)
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

	t.Run("exact-owner-timer-closes-at-next-deadline", func(t *testing.T) {
		f := newFailFirstFixture(t, cfg, nil, nil)
		f.clock.advance(time.Millisecond)
		send := f.submit([]byte("phase-offset"))
		due := f.waitGroupOpen()

		if delta := due.Sub(f.clock.Now()); delta > 0 {
			f.clock.advance(delta)
		}
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
		snap := f.m.PeerSnapshots()[0].FEC
		if snap.DeadlineDecisions != 1 || snap.DeadlineMisses != 0 || snap.DeadlineMaxOvershoot != 0 {
			t.Errorf("exact deadline stats decisions/misses/max = %d/%d/%s, want 1/0/0",
				snap.DeadlineDecisions, snap.DeadlineMisses, snap.DeadlineMaxOvershoot)
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

		executingAdmitted := owner.awaitAdmission(executing)
		firstAdmitted := owner.awaitAdmission(first)
		secondAdmitted := owner.awaitAdmission(second)
		events, _, _, _ := f.writer.snapshot()
		group0Decision := groupDecisionIndex(events, 0)
		firstQueuedAdmission := commandAdmissionIndex(events, "queued-1")
		secondQueuedAdmission := commandAdmissionIndex(events, "queued-2")
		if group0Decision < 0 || group0Decision > firstQueuedAdmission || group0Decision > secondQueuedAdmission {
			t.Errorf("group-0 decision did not precede both already-mailboxed Admit commands: decision=%d queued=%d/%d",
				group0Decision, firstQueuedAdmission, secondQueuedAdmission)
		}
		if executingAdmitted.group != 0 {
			t.Errorf("executing owner command entered group %d, want group 0", executingAdmitted.group)
		}
		if firstAdmitted.group != 1 || secondAdmitted.group != 1 {
			t.Errorf("queued commands entered groups %d/%d, want group 1 after group-0 decision",
				firstAdmitted.group, secondAdmitted.group)
		}

		if group0Decision < 0 {
			if err := f.await(owner.submitDecisionAndEmit(0)); err != nil {
				t.Fatal(err)
			}
		}
		if err := f.await(executing.done); err != nil {
			t.Fatal(err)
		}

		if firstAdmitted.group == 1 && secondAdmitted.group == 1 {
			due := firstAdmitted.due
			if secondAdmitted.due.After(due) {
				due = secondAdmitted.due
			}
			if delta := due.Sub(f.clock.Now()); delta > 0 {
				f.clock.advance(delta)
			}
			if err := f.await(owner.submitDecisionAndEmit(1)); err != nil {
				t.Fatal(err)
			}
		}
		if err := f.await(first.done); err != nil {
			t.Fatal(err)
		}
		if err := f.await(second.done); err != nil {
			t.Fatal(err)
		}

		events, _, _, _ = f.writer.snapshot()
		payloadCounts := map[string]int{}
		for _, event := range events {
			if decoded, ok := event.frame.(frame.Data); ok {
				payload := string(decoded.Payload)
				if payload == "owner-boundary" || payload == "queued-1" || payload == "queued-2" {
					payloadCounts[payload]++
				}
			}
		}
		t.Logf("contention trace: %s", f.writer.trace())
		for _, payload := range []string{"owner-boundary", "queued-1", "queued-2"} {
			if payloadCounts[payload] != 1 {
				t.Errorf("payload %q became writer-visible %d times, want exactly once", payload, payloadCounts[payload])
			}
		}
		if err := checkGroupFrames(events, 0, 1, 1); err != nil {
			t.Errorf("group 0: %v", err)
		}
		if err := checkGroupFrames(events, 1, 2, 1); err != nil {
			t.Errorf("group 1: %v", err)
		}
		assertPayloadAfterGroupDecision(t, events, "owner-boundary", 0)
		assertPayloadAfterGroupDecision(t, events, "queued-1", 1)
		assertPayloadAfterGroupDecision(t, events, "queued-2", 1)
		f.assertNoWriterWhileOpen()
	})
}

func TestFailFirstFECDeadlineDispatchStatsAndInvalidation(t *testing.T) {
	cfg := fec.Config{DataShards: 4, ParityShards: 1, Deadline: 80 * time.Millisecond}
	f := newFailFirstFixture(t, cfg, nil, nil)
	contracts := newRecoveryContractCoordinator(0xD15, f.clock)
	if err := contracts.begin(true, 125*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	offered := contracts.offerSnapshot()
	offer, _, err := telemetry.DecodeRecoveryContract(offered.payload)
	if err != nil {
		t.Fatal(err)
	}
	ack := offer
	ack.Type = telemetry.RecoveryContractACK
	ackPayload, err := telemetry.EncodeRecoveryContract(ack)
	if err != nil {
		t.Fatal(err)
	}
	contracts.recordOffer(0, telemetryProbeHeader{sessionID: 0xD15, probeSeq: 1, challenge: 2}, offered)
	if !contracts.acceptACK(0, 0xD15, 1, ackPayload) {
		t.Fatal("precondition: recovery contract ACK rejected")
	}
	f.m.peerState.contracts = contracts
	misses := make(chan fecDeadlineMiss, 2)
	f.m.fecDeadlineInvalidator = func(miss fecDeadlineMiss) {
		misses <- miss
	}

	send := f.submit([]byte("overshoot"))
	due := f.waitGroupOpen()
	overshoot := fecDeadlineDispatchGrace + time.Millisecond
	f.clock.advance(due.Sub(f.clock.Now()) + overshoot)
	f.waitDecision()
	if err := f.await(send); err != nil {
		t.Fatal(err)
	}

	snap := f.m.PeerSnapshots()[0].FEC
	if snap.DeadlineDecisions != 1 || snap.DeadlineMisses != 1 || snap.DeadlineMaxOvershoot != overshoot {
		t.Fatalf("deadline stats decisions/misses/max = %d/%d/%s, want 1/1/%s",
			snap.DeadlineDecisions, snap.DeadlineMisses, snap.DeadlineMaxOvershoot, overshoot)
	}
	select {
	case miss := <-misses:
		if miss.Group != 0 || miss.Due != due || miss.Overshoot != overshoot {
			t.Fatalf("deadline invalidation = %+v, want group=0 due=%s overshoot=%s", miss, due, overshoot)
		}
	default:
		t.Fatal("deadline SLO miss did not invoke invalidation seam")
	}
	if contracts.fastEligible() {
		t.Fatal("deadline SLO miss retained acknowledged fast-recovery eligibility")
	}
	if payload := contracts.payload(); len(payload) == 0 {
		t.Fatal("deadline SLO miss disabled negotiation instead of publishing a rotated recovery OFFER")
	}
	next := f.submit([]byte("blocked-a"), []byte("blocked-b"), []byte("blocked-c"), []byte("blocked-d"))
	select {
	case err := <-next.done:
		t.Fatalf("subsequent DATA completed before the invalidated service rotated: %v", err)
	default:
	}
	outerAfterMiss := f.m.outerSeq.Load()
	f.clock.advance(conservativeRecoveryService)
	var rotated telemetry.RecoveryContractMessage
	for i := 0; i < failFirstSpinLimit; i++ {
		payload := contracts.payload()
		candidate, recognized, decodeErr := telemetry.DecodeRecoveryContract(payload)
		if decodeErr == nil && recognized && candidate.ContractID > offer.ContractID {
			rotated = candidate
			break
		}
		runtime.Gosched()
	}
	if rotated.ContractID <= offer.ContractID {
		t.Fatal("deadline SLO miss did not rotate a fresh recovery OFFER after the drain interval")
	}
	if f.m.outerSeq.Load() != outerAfterMiss {
		t.Fatal("blocked DATA opened a new FEC group before recovery-service rotation")
	}
	select {
	case err := <-next.done:
		t.Fatalf("subsequent DATA completed before the new recovery contract fallback: %v", err)
	default:
	}
	f.clock.advance(conservativeRecoveryService)
	if err := f.await(next); err != nil {
		t.Fatal(err)
	}
	f.flush()
	select {
	case duplicate := <-misses:
		t.Fatalf("deadline invalidation repeated after group decision: %+v", duplicate)
	default:
	}
}

type failFirstOwnerAdmission struct {
	group   uint32
	index   int
	due     time.Time
	payload string
}

type failFirstOwnerSubmission struct {
	admitted chan failFirstOwnerAdmission
	done     *failFirstSubmission
}

type failFirstOwnerAdapter struct {
	fixture     *failFirstFixture
	owner       *fecSendOwner
	boundary    chan struct{}
	release     chan struct{}
	releaseOnce sync.Once
	admissions  sync.Map
}

func newFailFirstOwnerAdapter(fixture *failFirstFixture) *failFirstOwnerAdapter {
	adapter := &failFirstOwnerAdapter{
		fixture:  fixture,
		owner:    fixture.m.fecSend.Load().owner,
		boundary: make(chan struct{}),
		release:  make(chan struct{}),
	}
	adapter.owner.afterAdmit = func(admission fecOwnerAdmission, batch *fecOwnerBatch, index int) {
		payload := string(batch.bufs[index])
		fixture.writer.record("command-admitted", fmt.Sprintf(
			"payload=%s group=%d index=%d", payload, admission.group, admission.index))
		admitted, ok := adapter.admissions.Load(batch)
		if !ok {
			fixture.t.Fatalf("owner adapter lost admission channel for payload %q", payload)
		}
		admitted.(chan failFirstOwnerAdmission) <- failFirstOwnerAdmission{
			group:   uint32(admission.group),
			index:   admission.index,
			due:     admission.due,
			payload: payload,
		}
		adapter.admissions.Delete(batch)
		if payload != "owner-boundary" {
			return
		}
		fixture.writer.record("owner-command-paused", "executingAdmission=1 boundary=post-Admit-not-inside-Encoder.Admit")
		close(adapter.boundary)
		<-adapter.release
		fixture.writer.record("owner-command-release", "")
	}
	return adapter
}

func (a *failFirstOwnerAdapter) submitAdmission(payload []byte, _ bool) *failFirstOwnerSubmission {
	path := a.fixture.m.peerState.paths[0]
	remote, ok := path.getRemote()
	if !ok {
		a.fixture.t.Fatal("owner adapter path has no remote")
	}
	batch := &fecOwnerBatch{
		bufs:    [][]byte{append([]byte(nil), payload...)},
		classes: []shaper.Class{shaper.ClassData},
		path:    path,
		remote:  remote,
		shaped:  true,
		done:    make(chan error, 1),
	}
	admitted := make(chan failFirstOwnerAdmission, 1)
	a.admissions.Store(batch, admitted)
	submission := &failFirstOwnerSubmission{
		admitted: admitted,
		done:     &failFirstSubmission{done: batch.done},
	}
	go func() {
		if err := a.owner.publish(batch); err != nil {
			batch.done <- err
		}
	}()
	return submission
}

func (a *failFirstOwnerAdapter) submitDecisionAndEmit(group uint32) *failFirstSubmission {
	submission := &failFirstSubmission{done: make(chan error, 1)}
	go func() {
		a.owner.signalDeadline()
		for i := 0; i < failFirstSpinLimit; i++ {
			if a.owner.fs.openDeadlineNanos.Load() == 0 {
				submission.done <- nil
				return
			}
			runtime.Gosched()
		}
		submission.done <- fmt.Errorf("owner decision for group %d did not complete", group)
	}()
	return submission
}

func (a *failFirstOwnerAdapter) awaitAdmission(submission *failFirstOwnerSubmission) failFirstOwnerAdmission {
	for i := 0; i < failFirstSpinLimit; i++ {
		select {
		case admitted := <-submission.admitted:
			return admitted
		default:
			runtime.Gosched()
		}
	}
	a.fixture.t.Fatal("owner command was not admitted within bounded scheduler yields")
	return failFirstOwnerAdmission{}
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
	a.owner.afterAdmit = nil
}

func TestFailFirstAdaptiveM0SnapshotIsImmutable(t *testing.T) {
	cfg := fec.Config{DataShards: 2, ParityShards: 1, Deadline: 80 * time.Millisecond}
	adaptiveCfg := adaptivefec.DefaultConfig()
	adaptiveCfg.DataShards = cfg.DataShards
	adaptiveCfg.MaxParity = cfg.ParityShards
	f := newFailFirstFixture(t, cfg, &adaptiveCfg, nil)
	fs := f.m.fecSend.Load()
	if fs.ctrl == nil || fs.adaptiveParity.Load() != 0 {
		t.Fatalf("adaptive controller did not initialize M=0")
	}

	first := f.submit([]byte("adaptive-zero"))
	f.waitGroupOpen()
	f.assertNoWriterWhileOpen()
	if err := fs.owner.submitAdaptiveSample(fecAdaptiveSample{now: f.clock.Now(), loss: 1, count: 1}); err != nil {
		t.Fatal(err)
	}
	target := int(fs.adaptiveParity.Load())
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
	failedTranche, trancheEmitted, trancheErrors := f.writer.failedTranche()
	if len(failedTranche) < 2 {
		t.Fatalf("decided failing writer tranche has %d frames, want multiple group frames", len(failedTranche))
	}
	if accepted != emitted+writeErrors {
		t.Fatalf("writer accounting accepted/emitted/errors = %d/%d/%d; want accepted=emitted+terminal-errors",
			accepted, emitted, writeErrors)
	}
	if trancheEmitted+trancheErrors != len(failedTranche) || trancheErrors == 0 {
		t.Fatalf("decided tranche accounting emitted/errors=%d/%d, aggregate frames=%d; want each frame emitted or terminally failed across calls",
			trancheEmitted, trancheErrors, len(failedTranche))
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

func TestFailFirstFECBatchStopsSequenceAllocationAfterFailedGroup(t *testing.T) {
	const buffers = 257
	cfg := fec.Config{DataShards: 2, ParityShards: 1, Deadline: 80 * time.Millisecond}
	f := newFailFirstFixture(t, cfg, nil, func(writer *failFirstWriter) {
		writer.failDecided = true
	})

	err := f.await(f.submit(payloadStream(buffers)...))
	if !errors.Is(err, errFailFirstWriter) {
		t.Fatalf("257-buffer Send error = %v, want %v", err, errFailFirstWriter)
	}
	if got := f.m.outerSeq.Load(); got != uint64(cfg.DataShards) {
		t.Fatalf("outer sequence after first-group failure = %d, want consumed prefix %d", got, cfg.DataShards)
	}

	if err := f.await(f.submit([]byte("next-a"), []byte("next-b"))); err != nil {
		t.Fatalf("next Send after terminal group error: %v", err)
	}
	if got := f.m.outerSeq.Load(); got != uint64(2*cfg.DataShards) {
		t.Fatalf("outer sequence after recovery Send = %d, want %d (no failed-batch suffix gap)", got, 2*cfg.DataShards)
	}
}

func TestFailFirstFECWriterPrefixFailureKeepsOwnerForNextSend(t *testing.T) {
	cfg := fec.Config{DataShards: 2, ParityShards: 1, Deadline: 80 * time.Millisecond}
	f := newFailFirstFixture(t, cfg, nil, func(writer *failFirstWriter) {
		writer.failDecided = true
		writer.failEmitted = 1
	})

	err := f.await(f.submit([]byte("failed-a"), []byte("failed-b")))
	if !errors.Is(err, errFailFirstWriter) {
		t.Fatalf("first Send error = %v, want %v", err, errFailFirstWriter)
	}
	afterFailure := f.m.PeerSnapshots()[0].FEC
	if afterFailure.DataFrames != 1 || afterFailure.ParityFrames != 0 {
		t.Fatalf("emitted prefix DATA/PARITY = %d/%d, want 1/0", afterFailure.DataFrames, afterFailure.ParityFrames)
	}

	if err := f.await(f.submit([]byte("next-a"), []byte("next-b"))); err != nil {
		t.Fatalf("next Send after writer error: %v", err)
	}
	afterRecovery := f.m.PeerSnapshots()[0].FEC
	if afterRecovery.DataFrames != 3 || afterRecovery.ParityFrames != 1 {
		t.Fatalf("post-recovery DATA/PARITY = %d/%d, want 3/1", afterRecovery.DataFrames, afterRecovery.ParityFrames)
	}
	if got := f.m.outerSeq.Load(); got != 4 {
		t.Fatalf("outer sequence after failed+successful groups = %d, want 4", got)
	}
}

func TestFailFirstFECSendPublishesOneOwnerCommandForLargeBatch(t *testing.T) {
	const buffers = 257
	cfg := fec.Config{DataShards: 4, ParityShards: 1, Deadline: 80 * time.Millisecond}
	f := newFailFirstFixture(t, cfg, nil, nil)
	var publications atomic.Int64
	publicationShape := make(chan [2]int, 1)
	finalAdmitted := make(chan struct{})
	var finalOnce sync.Once
	owner := f.m.fecSend.Load().owner
	owner.afterPublish = func(bufs, completionCapacity int) {
		publications.Add(1)
		publicationShape <- [2]int{bufs, completionCapacity}
	}
	owner.afterAdmit = func(_ fecOwnerAdmission, _ *fecOwnerBatch, index int) {
		if index == buffers-1 {
			finalOnce.Do(func() { close(finalAdmitted) })
		}
	}

	payloads := payloadStream(buffers)
	send := f.submit(payloads...)
	<-finalAdmitted
	f.clock.advance(cfg.Deadline)
	f.flush()
	if err := f.await(send); err != nil {
		t.Fatal(err)
	}
	owner.afterPublish = nil
	owner.afterAdmit = nil
	if got := publications.Load(); got != 1 {
		t.Fatalf("owner command publications = %d, want one per original Send", got)
	}
	if got := <-publicationShape; got != [2]int{buffers, 1} {
		t.Fatalf("published batch shape = %v, want [%d buffers, 1 completion]", got, buffers)
	}
	events, _, _, _ := f.writer.snapshot()
	var (
		dataCount   int
		parityCount int
	)
	for _, event := range events {
		switch decoded := event.frame.(type) {
		case frame.Data:
			dataCount++
			if decoded.OuterSeq != uint64(dataCount) {
				t.Fatalf("DATA order at position %d has outer sequence %d", dataCount, decoded.OuterSeq)
			}
			if !bytes.Equal(decoded.Payload, payloads[dataCount-1]) {
				t.Fatalf("DATA payload at position %d = %q, want %q", dataCount, decoded.Payload, payloads[dataCount-1])
			}
		case frame.Parity:
			parityCount++
		}
	}
	wantParity := (buffers + cfg.DataShards - 1) / cfg.DataShards
	if dataCount != buffers || parityCount != wantParity {
		t.Fatalf("wire DATA/PARITY count = %d/%d, want %d/%d", dataCount, parityCount, buffers, wantParity)
	}
}

func TestFailFirstFECCompleteGroupUsesOneShaperHandoff(t *testing.T) {
	cfg := fec.Config{DataShards: 2, ParityShards: 1, Deadline: 80 * time.Millisecond}
	f := newFailFirstFixture(t, cfg, nil, nil)
	if err := f.await(f.submit([]byte("group-a"), []byte("group-b"))); err != nil {
		t.Fatal(err)
	}
	if got := f.writer.calls(); len(got) != 1 || got[0] != 3 {
		t.Fatalf("WriteDatagrams call sizes = %v, want one immutable complete-group handoff [3]", got)
	}
}

func TestFailFirstFECPublishedSendWaitsForStopAcknowledgement(t *testing.T) {
	cfg := fec.Config{DataShards: 2, ParityShards: 1, Deadline: 80 * time.Millisecond}
	f := newFailFirstFixture(t, cfg, nil, nil)
	owner := f.m.fecSend.Load().owner
	entered := make(chan struct{})
	release := make(chan struct{})
	var enterOnce sync.Once
	var copies atomic.Int64
	owner.beforeBatch = func(*fecOwnerBatch) {
		enterOnce.Do(func() { close(entered) })
		<-release
	}
	owner.beforeCopy = func(*fecOwnerBatch, int) {
		copies.Add(1)
	}
	t.Cleanup(func() {
		owner.beforeBatch = nil
		owner.beforeCopy = nil
	})

	payload := []byte("caller-owned")
	send := f.submit(payload)
	<-entered
	closeDone := make(chan error, 1)
	go func() { closeDone <- f.m.Close() }()

	var (
		returnedEarly bool
		sendErr       error
	)
	for i := 0; i < failFirstSpinLimit; i++ {
		select {
		case sendErr = <-send.done:
			returnedEarly = true
		default:
			runtime.Gosched()
			continue
		}
		break
	}
	if returnedEarly {
		copy(payload, "mutated-after")
	}
	close(release)
	if err := <-closeDone; err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !returnedEarly {
		sendErr = <-send.done
	}
	if returnedEarly {
		t.Fatalf("published Send returned before owner Stop acknowledgement (error=%v)", sendErr)
	}
	copy(payload, "after-return")
	runtime.Gosched()
	if got := copies.Load(); got != 0 {
		t.Fatalf("owner copied %d caller buffers after Stop; want published batch rejected before its first read", got)
	}
}

func TestFailFirstFECWholeCloseRejectsLateLazyOwnerPublication(t *testing.T) {
	fecCfg := &fec.Config{DataShards: 2, ParityShards: 1, Deadline: 80 * time.Millisecond}
	m, _, target, _ := lazyConcentratorFEC(t, testKey(t, 0x71), testKey(t, 0x72), fecCfg)
	if target.fecSend.Load() != nil {
		t.Fatal("lazy target unexpectedly has a sender before publication")
	}

	lazyReady := make(chan struct{})
	releaseLazy := make(chan struct{})
	lazyDone := make(chan struct{})
	detached := make(chan struct{})
	continueClose := make(chan struct{})
	var captured *fecSendOwner
	m.beforeLazyFECPublish = func(peer *peerState, fs *fecSender) {
		if peer != target {
			return
		}
		captured = fs.owner
		close(lazyReady)
		<-releaseLazy
	}
	m.afterCloseFECDetach = func() {
		close(detached)
		<-continueClose
	}

	go func() {
		m.ensurePeerReceiveInstantiated(target)
		close(lazyDone)
	}()
	<-lazyReady
	closeDone := make(chan error, 1)
	go func() { closeDone <- m.Close() }()
	<-detached
	close(releaseLazy)
	<-lazyDone
	if target.fecSend.Load() == nil {
		close(continueClose)
		t.Fatal("fixture did not publish the lazy sender after Close detachment")
	}
	close(continueClose)
	if err := <-closeDone; err != nil {
		t.Fatalf("Close: %v", err)
	}
	m.beforeLazyFECPublish = nil
	m.afterCloseFECDetach = nil

	late := target.fecSend.Load()
	exited := false
	select {
	case <-captured.done:
		exited = true
	default:
	}
	if late != nil || !exited {
		if late != nil {
			late.owner.Stop(errClosed)
			late.owner.Wait()
			target.fecSend.CompareAndSwap(late, nil)
		} else if !exited {
			captured.Stop(errClosed)
			captured.Wait()
		}
		t.Fatalf("Close left late lazy sender=%p, owner exited=%v; want nil/true", late, exited)
	}

	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("reopen after late-publication Close: %v", err)
	}
	reopened := target.fecSend.Load()
	if reopened == nil {
		t.Fatal("reopen did not publish a fresh sender for the configured peer")
	}
	if reopened.owner == captured {
		t.Fatalf("reopen reused exited owner %p", captured)
	}
}

func TestFailFirstAdaptiveSampleSubmissionDoesNotInvertBindLock(t *testing.T) {
	cfg := fec.Config{DataShards: 1, ParityShards: 1, Deadline: 80 * time.Millisecond}
	adaptiveCfg := adaptivefec.DefaultConfig()
	adaptiveCfg.DataShards = cfg.DataShards
	adaptiveCfg.MaxParity = cfg.ParityShards
	f := newFailFirstFixture(t, cfg, &adaptiveCfg, func(writer *failFirstWriter) {
		writer.blockDecided = true
		writer.entered = make(chan struct{})
		writer.release = make(chan struct{})
	})
	owner := f.m.fecSend.Load().owner
	adaptiveLock := make(chan struct{})
	var adaptiveOnce sync.Once
	owner.beforeAdaptiveLock = func() {
		adaptiveOnce.Do(func() { close(adaptiveLock) })
	}
	t.Cleanup(func() { owner.beforeAdaptiveLock = nil })

	send := f.submit([]byte("adaptive-lock-order"))
	f.waitWriterBlocked()
	f.m.mu.Lock()
	f.writer.releaseOnce.Do(func() { close(f.writer.release) })
	<-adaptiveLock

	sampleOwner, sample, ok := f.m.adaptiveSampleLocked(f.m.peerState)
	if !ok || sampleOwner != owner {
		f.m.mu.Unlock()
		t.Fatal("adaptive sample did not resolve the current owner")
	}
	f.m.mu.Unlock()
	if err := sampleOwner.submitAdaptiveSample(sample); err != nil {
		t.Fatal(err)
	}
	_ = f.await(send)
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
