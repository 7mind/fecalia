//go:build failfirst

package bind

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/fec"
	"github.com/7mind/wanbond/internal/frame"
	"github.com/7mind/wanbond/internal/log"
	"github.com/7mind/wanbond/internal/sched"
	"github.com/7mind/wanbond/internal/shaper"
)

type serialSubKAdmission struct {
	group fec.GroupID
	index int
	due   time.Time
}

func awaitSerialSubKAdmission(t testing.TB, admitted <-chan serialSubKAdmission) serialSubKAdmission {
	t.Helper()
	for range failFirstSpinLimit {
		select {
		case got := <-admitted:
			return got
		default:
			runtime.Gosched()
		}
	}
	t.Fatal("serial shaped Send did not reach owned FEC admission")
	return serialSubKAdmission{}
}

func awaitSerialSubKReturn(t testing.TB, returned <-chan int, want int) {
	t.Helper()
	for range failFirstSpinLimit {
		select {
		case got := <-returned:
			if got != want {
				t.Fatalf("serial shaped Send return order = %d, want %d", got, want)
			}
			return
		default:
			runtime.Gosched()
		}
	}
	t.Fatalf("serial shaped Send #%d did not acknowledge after owned admission; batch still waits for pending group decision/emission, so Send #%d cannot enter before the FEC deadline", want, want+1)
}

func TestFailFirstShapedFECSerialSubKAcknowledgesOwnedAdmission(t *testing.T) {
	cfg := fec.Config{DataShards: 3, ParityShards: 1, Deadline: 80 * time.Millisecond}
	f := newFailFirstFixture(t, cfg, nil, func(writer *failFirstWriter) {
		writer.blockDecided = true
		writer.entered = make(chan struct{})
		writer.release = make(chan struct{})
	})
	t.Cleanup(func() {
		f.writer.releaseOnce.Do(func() { close(f.writer.release) })
	})
	owner := f.m.fecSend.Load().owner
	admitted := make(chan serialSubKAdmission, cfg.DataShards)
	owner.afterAdmit = func(got fecOwnerAdmission, _ *fecOwnerBatch, _ int) {
		admitted <- serialSubKAdmission{group: got.group, index: got.index, due: got.due}
	}
	t.Cleanup(func() {
		owner.afterAdmit = nil
	})

	original := [][]byte{
		[]byte("serial-payload-1"),
		[]byte("serial-payload-2"),
		[]byte("serial-payload-3"),
	}
	wantPayloads := make([][]byte, len(original))
	for i := range original {
		wantPayloads[i] = append([]byte(nil), original[i]...)
	}
	returned := make(chan int, cfg.DataShards)
	continueSerial := make(chan struct{})
	serialDone := make(chan error, 1)
	go func() {
		for i := range original {
			if err := f.m.Send([][]byte{original[i]}, f.m.virt); err != nil {
				serialDone <- fmt.Errorf("serial shaped Send #%d: %w", i+1, err)
				return
			}
			for j := range original[i] {
				original[i][j] = byte('A' + i)
			}
			returned <- i + 1
			if i < len(original)-1 {
				<-continueSerial
			}
		}
		serialDone <- nil
	}()

	first := awaitSerialSubKAdmission(t, admitted)
	if first.group != 0 || first.index != 0 || first.due.IsZero() {
		t.Fatalf("first owned admission = %+v, want group=0 index=0 and an armed deadline", first)
	}
	f.assertNoWriterWhileOpen()
	awaitSerialSubKReturn(t, returned, 1)
	if groups, frames := f.m.fecSend.Load().stagingSnapshot(); groups != 1 || frames != 1 {
		t.Fatalf("ownership after first acknowledged partial = groups:%d DATA:%d, want bounded 1/1", groups, frames)
	}
	continueSerial <- struct{}{}

	second := awaitSerialSubKAdmission(t, admitted)
	if second.group != first.group || second.index != 1 || second.due != first.due {
		t.Fatalf("second owned admission = %+v, want group=%d index=1 due=%s", second, first.group, first.due)
	}
	f.assertNoWriterWhileOpen()
	awaitSerialSubKReturn(t, returned, 2)
	if groups, frames := f.m.fecSend.Load().stagingSnapshot(); groups != 1 || frames != uint64(cfg.DataShards-1) {
		t.Fatalf("ownership after K-1 acknowledged partials = groups:%d DATA:%d, want bounded 1/%d",
			groups, frames, cfg.DataShards-1)
	}
	continueSerial <- struct{}{}

	third := awaitSerialSubKAdmission(t, admitted)
	if third.group != first.group || third.index != 2 || !third.due.IsZero() {
		t.Fatalf("third owned admission = %+v, want size-close group=%d index=2 with no deadline", third, first.group)
	}
	awaitSerialSubKReturn(t, returned, 3)
	if err := <-serialDone; err != nil {
		t.Fatal(err)
	}
	f.waitWriterBlocked()
	f.assertNoWriterWhileOpen()

	events, _, _, _ := f.writer.snapshot()
	assertGroupFrames(t, events, uint32(first.group), cfg.DataShards, cfg.ParityShards)
	var data []frame.Data
	for _, event := range events {
		if decoded, ok := event.frame.(frame.Data); ok {
			data = append(data, decoded)
		}
	}
	if len(data) != cfg.DataShards {
		t.Fatalf("wire DATA count = %d, want %d", len(data), cfg.DataShards)
	}
	for i, decoded := range data {
		if decoded.OuterSeq != uint64(i+1) || decoded.FECIndex != uint8(i) ||
			!bytes.Equal(decoded.Payload, wantPayloads[i]) {
			t.Fatalf("wire DATA #%d = seq:%d group:%d index:%d payload:%q, want seq:%d group:%d index:%d payload:%q",
				i+1, decoded.OuterSeq, decoded.FECGroup, decoded.FECIndex, decoded.Payload,
				i+1, first.group, i, wantPayloads[i])
		}
	}
	f.writer.releaseOnce.Do(func() { close(f.writer.release) })
	for range failFirstSpinLimit {
		if groups, dataFrames := f.m.fecSend.Load().stagingSnapshot(); groups == 0 && dataFrames == 0 {
			break
		}
		runtime.Gosched()
	}
	snapshot := f.m.PeerSnapshots()[0].FEC
	if snapshot.GroupDecisions != 1 || snapshot.DeadlineDecisions != 0 ||
		snapshot.StagedGroups != 0 || snapshot.StagedDataFrames != 0 {
		t.Fatalf("size-closed serial sub-K FEC snapshot = %+v, want one size decision, no deadline decision, and no staged ownership", snapshot)
	}
}

func TestFailFirstShapedFECPreAcknowledgementStopRejectsUnownedSuffix(t *testing.T) {
	cfg := fec.Config{DataShards: 4, ParityShards: 1, Deadline: 80 * time.Millisecond}
	f := newFailFirstFixture(t, cfg, nil, nil)
	owner := f.m.fecSend.Load().owner
	firstAdmitted := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	var copies atomic.Int64
	owner.beforeCopy = func(*fecOwnerBatch, int) {
		copies.Add(1)
	}
	owner.afterAdmit = func(_ fecOwnerAdmission, _ *fecOwnerBatch, index int) {
		if index == 0 {
			once.Do(func() { close(firstAdmitted) })
			<-release
		}
	}
	t.Cleanup(func() {
		owner.beforeCopy = nil
		owner.afterAdmit = nil
	})

	sentinel := errors.New("pre-ack owner stop")
	sendDone := make(chan error, 1)
	go func() {
		sendDone <- f.m.Send([][]byte{
			[]byte("owned-prefix"),
			[]byte("unowned-middle"),
			[]byte("unowned-suffix"),
		}, f.m.virt)
	}()
	<-firstAdmitted
	owner.Stop(sentinel)
	close(release)
	err := <-sendDone
	if !errors.Is(err, sentinel) {
		t.Fatalf("pre-ack shaped Send error = %v, want %v", err, sentinel)
	}
	owner.Wait()
	if got := copies.Load(); got != 1 {
		t.Fatalf("caller buffers copied before synchronous rejection = %d, want owned prefix only", got)
	}
	if got := f.m.outerSeq.Load(); got != 1 {
		t.Fatalf("OuterSeq consumed before synchronous rejection = %d, want owned prefix only", got)
	}
	if groups, frames := f.m.fecSend.Load().stagingSnapshot(); groups != 0 || frames != 0 {
		t.Fatalf("staged ownership after pre-ack rejection = groups:%d DATA:%d, want 0/0", groups, frames)
	}
	events, _, _, _ := f.writer.snapshot()
	if data, parity := frameCounts(events); data != 0 || parity != 0 {
		t.Fatalf("pre-ack rejected batch became wire-visible as DATA/PARITY=%d/%d", data, parity)
	}
}

func TestFailFirstFECQueuedPublicationIsBoundedAndOrdered(t *testing.T) {
	cfg := fec.Config{DataShards: 3, ParityShards: 1, Deadline: 80 * time.Millisecond}
	f := newFailFirstFixture(t, cfg, nil, nil)
	owner := f.m.fecSend.Load().owner
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	var once sync.Once
	owner.beforeBatch = func(*fecOwnerBatch) {
		once.Do(func() {
			close(firstEntered)
			<-releaseFirst
		})
	}
	t.Cleanup(func() {
		owner.beforeBatch = nil
	})

	first := f.submit([]byte("publication-1"))
	<-firstEntered
	second := f.submit([]byte("publication-2"))
	third := f.submit([]byte("publication-3"))
	submissions := []*failFirstSubmission{first, second, third}
	for index, submission := range submissions {
		if submission.batch == nil || submission.terminal != submission.batch.done {
			t.Fatalf("submission #%d terminal is not correlated to its published owner batch", index+1)
		}
		if len(submission.batch.bufs) != 1 ||
			string(submission.batch.bufs[0]) != fmt.Sprintf("publication-%d", index+1) {
			t.Fatalf("submission #%d correlated batch payload = %q", index+1, submission.batch.bufs)
		}
		for prior := range index {
			if submission.batch == submissions[prior].batch ||
				submission.terminal == submissions[prior].terminal {
				t.Fatalf("submissions #%d and #%d share a batch/terminal identity", prior+1, index+1)
			}
		}
	}
	close(releaseFirst)
	for index, submission := range submissions {
		if err := f.await(submission); err != nil {
			t.Fatalf("submission #%d terminal error: %v", index+1, err)
		}
	}
	events, _, _, _ := f.writer.snapshot()
	assertGroupFrames(t, events, 0, cfg.DataShards, cfg.ParityShards)
}

func TestFailFirstPacingOffFECRemainsSynchronousThroughDeadlineAndWrite(t *testing.T) {
	cfg := fec.Config{DataShards: 3, ParityShards: 1, Deadline: 80 * time.Millisecond}
	f := newFailFirstFixture(t, cfg, nil, nil)
	f.m.shaperConfigs = nil
	f.m.peerState.scheduler = stubScheduler{pick: 0}
	path := f.m.paths[0]
	sentinel := errors.New("pacing-off writer failure")
	path.writeUDP = func([]byte, netip.AddrPort) (int, error) {
		return 0, sentinel
	}
	admitted := make(chan struct{})
	var once sync.Once
	f.m.fecSend.Load().owner.afterAdmit = func(_ fecOwnerAdmission, _ *fecOwnerBatch, _ int) {
		once.Do(func() { close(admitted) })
	}
	t.Cleanup(func() {
		f.m.fecSend.Load().owner.afterAdmit = nil
	})

	sendDone := make(chan error, 1)
	go func() {
		sendDone <- f.m.Send([][]byte{[]byte("direct-partial")}, f.m.virt)
	}()
	<-admitted
	for range failFirstSpinLimit {
		select {
		case err := <-sendDone:
			t.Fatalf("pacing-off partial Send returned before deadline decision/write: %v", err)
		default:
			runtime.Gosched()
		}
	}
	f.clock.advance(cfg.Deadline)
	f.flush()
	if err := <-sendDone; !errors.Is(err, sentinel) {
		t.Fatalf("pacing-off deadline-flushed Send error = %v, want synchronous %v", err, sentinel)
	}
}

func newAdmissionAckProductionBind(t testing.TB, cfg fec.Config) *Multipath {
	t.Helper()
	logger, err := log.New("error", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	shaperConfig := recoveryReviewShaperConfig()
	m, err := NewMultipathWithShapers(
		loopbackPaths(1),
		testKey(t, 0xA8),
		&unpacedSelectionRecorder{},
		nil,
		nil,
		&cfg,
		nil,
		config.Amnezia{},
		[]config.PathShaperConfig{shaperConfig},
		logger,
	)
	if err != nil {
		t.Fatal(err)
	}
	m.clock = newFakeClock()
	if _, _, err := m.Open(0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	m.paths[0].setRemote(netip.MustParseAddrPort("127.0.0.1:9"))
	if !m.paths[0].recoveryContract().Enabled {
		t.Fatal("production admission-ack fixture lacks exclusive recovery contract")
	}
	return m
}

func TestFailFirstShapedFECPostAcknowledgementFailureRetiresExactGeneration(t *testing.T) {
	for _, failure := range []string{"install", "clear", "write"} {
		t.Run(failure, func(t *testing.T) {
			cfg := fec.Config{DataShards: 3, ParityShards: 1, Deadline: 80 * time.Millisecond}
			m := newAdmissionAckProductionBind(t, cfg)
			path := m.paths[0]
			sentinel := errors.New("post-ack " + failure + " failure")
			path.setWriteDeadline = func(deadline time.Time) error {
				switch {
				case failure == "install" && !deadline.IsZero():
					return sentinel
				case failure == "clear" && deadline.IsZero():
					return sentinel
				default:
					return nil
				}
			}
			path.writeUDP = func(payload []byte, _ netip.AddrPort) (int, error) {
				if failure == "write" {
					return 0, sentinel
				}
				return len(payload), nil
			}
			var retirements atomic.Int64
			retired := make(chan struct{}, 1)
			m.afterRecoveryRetire = func(got *sharedPathState) {
				if got == path.sharedPathState {
					retirements.Add(1)
					retired <- struct{}{}
				}
			}

			for index := range cfg.DataShards {
				payload := []byte(fmt.Sprintf("post-ack-%d", index))
				if err := m.Send([][]byte{payload}, m.virt); err != nil {
					t.Fatalf("shaped Send #%d returned post-ack failure synchronously: %v", index+1, err)
				}
			}
			select {
			case <-retired:
			case <-time.After(2 * time.Second):
				t.Fatal("post-ack failure did not retire its socket generation")
			}
			m.abortRecoveryGeneration(path, sentinel)
			for range failFirstSpinLimit {
				runtime.Gosched()
			}
			if got := retirements.Load(); got != 1 {
				t.Fatalf("matching generation retirements = %d, want exactly one", got)
			}
			if !path.recoveryFailed.Load() {
				t.Fatal("post-ack failure left failed generation admission enabled")
			}
			if got := path.shapedWriteErrors.Load(); got != 1 {
				t.Fatalf("shaped terminal call errors = %d, want 1", got)
			}
			snapshot := path.shaper.(*shaper.Shaper).Snapshot()
			switch failure {
			case "install":
				if snapshot.AcceptedBytes != 0 || snapshot.EmittedBytes != 0 ||
					snapshot.AsyncWriteErrorBytes != 0 {
					t.Fatalf("install-failure accounting = %+v, want no admitted wire bytes", snapshot)
				}
			case "clear":
				if snapshot.AcceptedBytes == 0 || snapshot.AcceptedBytes != snapshot.EmittedBytes ||
					snapshot.AsyncWriteErrorBytes != 0 {
					t.Fatalf("clear-failure accounting = %+v, want every admitted byte emitted", snapshot)
				}
			case "write":
				if snapshot.AcceptedBytes == 0 || snapshot.EmittedBytes != 0 ||
					snapshot.AsyncWriteErrors != 1 ||
					snapshot.AsyncWriteErrorBytes != snapshot.AcceptedBytes ||
					path.socketWriteErrors.Load() != 1 {
					t.Fatalf("write-failure accounting = snapshot:%+v socket-errors:%d, want accepted bytes assigned exactly once to one terminal socket failure",
						snapshot, path.socketWriteErrors.Load())
				}
			}
		})
	}
}

type serialSubKPathScheduler struct {
	mu    sync.Mutex
	picks []int
	next  int
}

func (s *serialSubKPathScheduler) Pick(sched.FrameClass, int) int {
	return s.pick()
}

func (s *serialSubKPathScheduler) PickUnpaced(sched.FrameClass, int) int {
	return s.pick()
}

func (s *serialSubKPathScheduler) pick() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.next >= len(s.picks) {
		return sched.PickNone
	}
	pick := s.picks[s.next]
	s.next++
	return pick
}

func (*serialSubKPathScheduler) SelectPath() int { return 0 }
func (*serialSubKPathScheduler) Recompute()      {}
func (*serialSubKPathScheduler) DataPaths() []sched.DataPath {
	return []sched.DataPath{{Index: 0, Weight: 0.5}, {Index: 1, Weight: 0.5}}
}

func TestFailFirstShapedFECMixedPathFailureRetiresOnlyFailingGeneration(t *testing.T) {
	logger, err := log.New("error", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	fecConfig := &fec.Config{DataShards: 3, ParityShards: 1, Deadline: 80 * time.Millisecond}
	shaperConfig := recoveryReviewShaperConfig()
	scheduler := &serialSubKPathScheduler{picks: []int{0, 1, 0}}
	m, err := NewMultipathWithShapers(
		loopbackPaths(2),
		testKey(t, 0xA9),
		scheduler,
		nil,
		nil,
		fecConfig,
		nil,
		config.Amnezia{},
		[]config.PathShaperConfig{shaperConfig, shaperConfig},
		logger,
	)
	if err != nil {
		t.Fatal(err)
	}
	m.clock = newFakeClock()
	if _, _, err := m.Open(0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	failed, survivor := m.paths[0], m.paths[1]
	failed.setRemote(netip.MustParseAddrPort("127.0.0.1:9"))
	survivor.setRemote(netip.MustParseAddrPort("127.0.0.1:10"))
	sentinel := errors.New("mixed-path A writer failure")
	failed.writeUDP = func([]byte, netip.AddrPort) (int, error) {
		return 0, sentinel
	}
	survivor.writeUDP = func(payload []byte, _ netip.AddrPort) (int, error) {
		return len(payload), nil
	}
	retired := make(chan *sharedPathState, 2)
	m.afterRecoveryRetire = func(got *sharedPathState) {
		retired <- got
	}

	for index := range fecConfig.DataShards {
		if err := m.Send([][]byte{[]byte(fmt.Sprintf("mixed-%d", index))}, m.virt); err != nil {
			t.Fatalf("mixed-path shaped Send #%d: %v", index+1, err)
		}
	}
	select {
	case got := <-retired:
		if got != failed.sharedPathState {
			t.Fatalf("retired socket generation = %p, want failing A %p", got, failed.sharedPathState)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("failing mixed-path generation did not retire")
	}
	if survivor.recoveryFailed.Load() {
		t.Fatal("path A failure marked surviving path B failed")
	}
	m.mu.Lock()
	remaining := append([]*peerPathState(nil), m.paths...)
	m.mu.Unlock()
	if len(remaining) != 1 || remaining[0] != survivor {
		t.Fatalf("remaining paths after A failure = %v, want only live B", remaining)
	}
	m.abortRecoveryGeneration(failed, errors.New("stale A failure"))
	for range failFirstSpinLimit {
		runtime.Gosched()
	}
	select {
	case duplicate := <-retired:
		t.Fatalf("stale A failure retired another generation %p", duplicate)
	default:
	}
	if survivor.recoveryFailed.Load() {
		t.Fatal("stale A failure affected surviving path B")
	}
}

func TestFailFirstShapedFECCloseDuringFallbackWriteDoesNotRetireLifecycleGeneration(t *testing.T) {
	lg, err := log.New("error", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.PathShaperConfig{
		RateBytesPerSecond:      10_000_000,
		DataBurstBytes:          1472,
		ControlReserveBytes:     1472,
		MaxEncodedDatagramBytes: 1472,
		ProbeRateBytesPerSecond: 1,
		ProbeBurstBytes:         2944,
		PriorityReserveBytes:    2944,
	}
	m, err := NewMultipathWithShapers(
		loopbackPaths(1),
		testKey(t, 0xD9),
		&unpacedSelectionRecorder{},
		nil,
		nil,
		&fec.Config{DataShards: 2, ParityShards: 1, Deadline: testFECDeadline},
		nil,
		config.Amnezia{},
		[]config.PathShaperConfig{cfg},
		lg,
	)
	if err != nil {
		t.Fatal(err)
	}
	blocking := &generationBlockingShaper{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	m.newPathShaper = func(shaper.Config, shaper.WriteFunc) (pathShaper, error) {
		return blocking, nil
	}
	if _, _, err := m.Open(0); err != nil {
		t.Fatal(err)
	}
	path := m.paths[0]
	path.setRemote(netip.MustParseAddrPort("127.0.0.1:9"))
	retired := make(chan *sharedPathState, 1)
	m.afterRecoveryRetire = func(got *sharedPathState) {
		retired <- got
	}

	sendDone := make(chan error, 1)
	go func() {
		sendDone <- m.Send([][]byte{[]byte("close-a"), []byte("close-b")}, m.virt)
	}()
	select {
	case err := <-sendDone:
		if err != nil {
			t.Fatalf("shaped admission acknowledgement = %v, want nil before fallback write completes", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shaped Send did not acknowledge owned admission before fallback write completed")
	}
	select {
	case <-blocking.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("size-closed group did not enter fallback WriteDatagrams")
	}

	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if path.recoveryFailed.Load() {
		t.Fatal("ordinary Close cancellation retired the fallback write's recovery generation")
	}
	select {
	case got := <-retired:
		t.Fatalf("ordinary Close cancellation launched recovery retirement for %p", got)
	default:
	}
}

func TestFailFirstShapedFECTrueFailureLinearizesBeforeConcurrentCloseWithoutLockInversion(t *testing.T) {
	cfg := fec.Config{DataShards: 3, ParityShards: 1, Deadline: 80 * time.Millisecond}
	f := newFailFirstFixture(t, cfg, nil, func(writer *failFirstWriter) {
		writer.failDecided = true
	})
	owner := f.m.fecSend.Load().owner
	failureLinearized := make(chan struct{})
	releaseAbort := make(chan struct{})
	var entered sync.Once
	var released sync.Once
	owner.beforeRecoveryAbort = func() {
		entered.Do(func() { close(failureLinearized) })
		<-releaseAbort
	}
	t.Cleanup(func() {
		released.Do(func() { close(releaseAbort) })
	})

	if err := f.m.Send([][]byte{
		[]byte("failure-a"),
		[]byte("failure-b"),
		[]byte("failure-c"),
	}, f.m.virt); err != nil {
		t.Fatalf("post-admission shaped Send = %v, want nil", err)
	}
	select {
	case <-failureLinearized:
	case <-time.After(5 * time.Second):
		t.Fatal("true fallback write failure did not reach recovery-abort linearization seam")
	}

	closeDone := make(chan error, 1)
	go func() {
		closeDone <- f.m.Close()
	}()
	for range failFirstSpinLimit {
		if owner.isStopping() {
			break
		}
		runtime.Gosched()
	}
	if !owner.isStopping() {
		released.Do(func() { close(releaseAbort) })
		t.Fatal("Close could not acquire the owner submission mutex while a true recovery abort was paused")
	}
	released.Do(func() { close(releaseAbort) })
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("true recovery failure and concurrent Close deadlocked")
	}
}

func TestFailFirstShapedFECCloseAccountsAcknowledgedPartialBeforeReopen(t *testing.T) {
	cfg := fec.Config{DataShards: 3, ParityShards: 1, Deadline: 80 * time.Millisecond}
	f := newFailFirstFixture(t, cfg, nil, nil)
	old := f.m.fecSend.Load()
	path := f.m.paths[0]
	retired := make(chan *sharedPathState, 1)
	f.m.afterRecoveryRetire = func(got *sharedPathState) {
		retired <- got
	}
	payload := []byte("acknowledged-partial")
	if err := f.m.Send([][]byte{payload}, f.m.virt); err != nil {
		t.Fatal(err)
	}
	for i := range payload {
		payload[i] = 'X'
	}
	if groups, frames := old.stagingSnapshot(); groups != 1 || frames != 1 {
		t.Fatalf("old acknowledged ownership = groups:%d DATA:%d, want 1/1", groups, frames)
	}
	if err := f.m.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-old.owner.done:
	default:
		t.Fatal("Close returned before acknowledged partial owner exited")
	}
	if groups, frames := old.stagingSnapshot(); groups != 0 || frames != 0 {
		t.Fatalf("old ownership after Close = groups:%d DATA:%d, want 0/0", groups, frames)
	}
	if path.recoveryFailed.Load() {
		t.Fatal("ordinary Close of acknowledged partial retired its recovery generation")
	}
	select {
	case got := <-retired:
		t.Fatalf("ordinary Close of acknowledged partial launched recovery retirement for %p", got)
	default:
	}
	events, _, _, _ := f.writer.snapshot()
	if data, parity := frameCounts(events); data != 0 || parity != 0 {
		t.Fatalf("Close emitted acknowledged partial as DATA/PARITY=%d/%d", data, parity)
	}

	if _, _, err := f.m.Open(0); err != nil {
		t.Fatal(err)
	}
	f.m.paths[0].setRemote(netip.MustParseAddrPort("127.0.0.1:9"))
	replacement := f.m.fecSend.Load()
	if replacement == nil || replacement == old || replacement.owner == old.owner {
		t.Fatal("reopen did not publish a distinct FEC owner generation")
	}
	if err := f.m.Send([][]byte{
		[]byte("replacement-1"),
		[]byte("replacement-2"),
		[]byte("replacement-3"),
	}, f.m.virt); err != nil {
		t.Fatal(err)
	}
	for range failFirstSpinLimit {
		if groups, frames := replacement.stagingSnapshot(); groups == 0 && frames == 0 {
			break
		}
		runtime.Gosched()
	}
	events, _, _, _ = f.writer.snapshot()
	var replacementGroup uint32
	found := false
	for _, event := range events {
		if decoded, ok := event.frame.(frame.Data); ok {
			replacementGroup = decoded.FECGroup
			found = true
			break
		}
	}
	if !found || replacementGroup == 0 {
		t.Fatalf("replacement wire GroupID = %d (found=%v), want nonzero after discarded old group 0", replacementGroup, found)
	}
}
