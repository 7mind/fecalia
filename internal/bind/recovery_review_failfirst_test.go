package bind

import (
	"bytes"
	"context"
	"errors"
	"net/netip"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/frame"
	"github.com/7mind/wanbond/internal/shaper"
)

func recoveryReviewShaperConfig() config.PathShaperConfig {
	cfg := priorityTestShaperConfig()
	cfg.FECGroupReserveBytes = 16 * cfg.MaxEncodedDatagramBytes
	cfg.RecoveryWriteSlack = 10 * time.Millisecond
	return cfg
}

func openRecoveryReviewBind(t testing.TB, pathCount int, psk config.Key) *Multipath {
	t.Helper()
	m, _, _ := newProbingMultipath(t, loopbackPaths(pathCount), psk, newFakeClock())
	cfg := recoveryReviewShaperConfig()
	m.shaperConfigs = make([]config.PathShaperConfig, pathCount)
	for index := range m.shaperConfigs {
		m.shaperConfigs[index] = cfg
	}
	if _, _, err := m.Open(0); err != nil {
		t.Fatal(err)
	}
	for _, path := range m.paths {
		path.setRemote(netip.MustParseAddrPort("127.0.0.1:9"))
	}
	t.Cleanup(func() { _ = m.Close() })
	return m
}

func recoveryReviewOwner(m *Multipath) *fecSendOwner {
	return &fecSendOwner{
		m:     m,
		peer:  m.peerState,
		clock: m.clock,
		ctx:   context.Background(),
	}
}

func recoveryReviewWrites(path *peerPathState, payloads ...[]byte) []fecPreparedWrite {
	writes := make([]fecPreparedWrite, len(payloads))
	remote, _ := path.getRemote()
	for index, payload := range payloads {
		writes[index] = fecPreparedWrite{
			path:   path,
			remote: remote,
			shaped: true,
			class:  shaper.ClassData,
			wire:   fecWire{b: payload},
		}
	}
	return writes
}

func TestExactByteShaperConfigUsesDerivedPriorityReserve(t *testing.T) {
	cfg := config.PathShaperConfig{
		ProbeBurstBytes:      200,
		PriorityReserveBytes: 300,
	}
	got := exactByteShaperConfig(cfg)
	if got.PriorityReserveBytes != cfg.PriorityReserveBytes {
		t.Fatalf("PriorityReserveBytes = %d, want derived P=%d (not Pburst=%d)",
			got.PriorityReserveBytes, cfg.PriorityReserveBytes, cfg.ProbeBurstBytes)
	}
}

func TestSharedSocketReportsRecoveryContractDisabled(t *testing.T) {
	cfg := shaper.Config{
		RateBytesPerSecond:         1_000_000,
		PriorityRateBytesPerSecond: 10_000,
		DataBudgetBytes:            4_000,
		ControlReserveBytes:        1_000,
		MaxDatagramBytes:           1_000,
		PriorityBurstBytes:         2_000,
		PriorityReserveBytes:       2_000,
		FECGroupReserveBytes:       8_000,
		RecoveryWriteSlack:         10 * time.Millisecond,
	}
	writer, err := shaper.New(cfg, shaper.SystemClock{}, func([]byte) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.Close() }()

	shared := &sharedPathState{name: "shared"}
	first := &peerPathState{sharedPathState: shared, shaper: writer}
	second := &peerPathState{sharedPathState: shared}
	shared.addViewLocked(first)
	shared.addViewLocked(second)
	contract := first.recoveryContract()
	if contract.Enabled {
		t.Fatal("shared socket reports bounded recovery enabled")
	}
}

func TestRecoveryAbortRemovesFailedGenerationFromProductionBind(t *testing.T) {
	psk := testKey(t, 0xD1)
	m, _, _ := newProbingMultipath(t, loopbackPaths(1), psk, newFakeClock())
	m.shaperConfigs = []config.PathShaperConfig{priorityTestShaperConfig()}
	if _, _, err := m.Open(0); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Close() }()

	failed := m.paths[0]
	m.abortRecoveryGeneration(failed, errors.New("recovery deadline failed"))
	waitForReviewCondition(t, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return len(m.paths) == 0
	})
}

func waitForReviewCondition(t testing.TB, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition did not become true")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestPMTUWriteStageErrClosedDoesNotCountAdmissionCancellation(t *testing.T) {
	psk := testKey(t, 0xD2)
	m, _, _ := newProbingMultipath(t, loopbackPaths(1), psk, newFakeClock())
	cfg := priorityTestShaperConfig()
	cfg.PriorityReserveBytes = cfg.ProbeBurstBytes
	m.shaperConfigs = []config.PathShaperConfig{cfg}
	if _, _, err := m.Open(0); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Close() }()

	_, peerAP := rawPeer(t)
	m.paths[0].setRemote(peerAP)
	m.paths[0].writeUDP = func([]byte, netip.AddrPort) (int, error) {
		return 0, shaper.ErrClosed
	}
	result := startPMTUProbe(m.PMTUProbe("a"), 1500)
	waitPendingPMTUProbe(t, m.paths[0])
	m.emitProbes()
	got := <-result
	if !errors.Is(got.err, shaper.ErrClosed) {
		t.Fatalf("ProbePMTU error = %v, want write-stage shaper.ErrClosed", got.err)
	}
	if count := m.paths[0].pmtuAdmissionCanceled.Load(); count != 0 {
		t.Fatalf("PMTU admission cancellations = %d, want 0 after successful reservation/generation", count)
	}
	if count := m.paths[0].probeSendErrors.Load(); count != 1 {
		t.Fatalf("PMTU send errors = %d, want 1", count)
	}
	if got.echoed {
		t.Fatal("closed-socket PMTU probe unexpectedly echoed")
	}
}

func TestPMTUClosedGenerationBeforeAdmissionCountsCancellation(t *testing.T) {
	psk := testKey(t, 0xD3)
	m, _, _ := newProbingMultipath(t, loopbackPaths(1), psk, newFakeClock())
	cfg := priorityTestShaperConfig()
	cfg.PriorityReserveBytes = cfg.ProbeBurstBytes
	m.shaperConfigs = []config.PathShaperConfig{cfg}
	if _, _, err := m.Open(0); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Close() }()

	_, peerAP := rawPeer(t)
	path := m.paths[0]
	path.setRemote(peerAP)
	writeStarted := make(chan struct{})
	releaseWrite := make(chan struct{})
	var firstWrite sync.Once
	path.writeUDP = func(payload []byte, _ netip.AddrPort) (int, error) {
		firstWrite.Do(func() {
			close(writeStarted)
			<-releaseWrite
		})
		return len(payload), nil
	}

	dataDone := make(chan error, 1)
	go func() {
		_, writeErr := path.shaper.WriteDatagrams(context.Background(), []shaper.Datagram{{
			Class:   shaper.ClassData,
			Payload: []byte{1},
		}})
		dataDone <- writeErr
	}()
	<-writeStarted
	shaped := path.shaper.(recoveryPathShaper)
	priorityCompletions := make([]<-chan error, 0, 2)
	for range 2 {
		admitted, completion, err := shaped.TryWritePriority(
			make([]byte, cfg.MaxEncodedDatagramBytes),
			nil,
		)
		if err != nil || !admitted {
			t.Fatalf("fill P admission = %v, %v", admitted, err)
		}
		priorityCompletions = append(priorityCompletions, completion)
	}

	result := startPMTUProbe(m.PMTUProbe("a"), 1500)
	waitPendingPMTUProbe(t, path)
	emitDone := make(chan struct{})
	go func() {
		m.emitProbes()
		close(emitDone)
	}()
	writer := path.shaper.(*shaper.Shaper)
	waitForReviewCondition(t, func() bool {
		return writer.Snapshot().AdmissionWaits > 0
	})
	path.shaper.(stagedPathShaper).Stop()
	close(releaseWrite)
	got := <-result
	if !errors.Is(got.err, shaper.ErrClosed) {
		t.Fatalf("ProbePMTU error = %v, want pre-generation shaper.ErrClosed", got.err)
	}
	if count := path.pmtuAdmissionCanceled.Load(); count != 1 {
		t.Fatalf("PMTU admission cancellations = %d, want 1", count)
	}
	<-emitDone
	if err := <-dataDone; err != nil {
		t.Fatal(err)
	}
	for _, completion := range priorityCompletions {
		if err := <-completion; !errors.Is(err, shaper.ErrClosed) {
			t.Fatalf("queued priority completion = %v, want shaper.ErrClosed", err)
		}
	}
	raw, err := path.prober.SendProbe()
	if err != nil {
		t.Fatal(err)
	}
	codec, err := frame.NewCodec(psk)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := codec.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if seq := decoded.(frame.Probe).ProbeSeq; seq != 0 {
		t.Fatalf("first post-cancellation probe sequence = %d, want 0: canceled PMTU invoked generator", seq)
	}
}

func TestRecoveryFailureRetiresExactProductionGeneration(t *testing.T) {
	for _, failure := range []string{"install", "clear", "write"} {
		t.Run(failure, func(t *testing.T) {
			m := openRecoveryReviewBind(t, 1, testKey(t, 0xD4))
			path := m.paths[0]
			sentinel := errors.New(failure + " failed")
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
			retired := make(chan struct{}, 1)
			m.afterRecoveryRetire = func(got *sharedPathState) {
				if got == path.sharedPathState {
					retired <- struct{}{}
				}
			}

			err := recoveryReviewOwner(m).emit(1, recoveryReviewWrites(path, []byte{1}, []byte{2}))
			if !errors.Is(err, sentinel) {
				t.Fatalf("emit error = %v, want %v", err, sentinel)
			}
			if _, err := path.shaper.WriteDatagrams(context.Background(), []shaper.Datagram{{
				Class:   shaper.ClassData,
				Payload: []byte{3},
			}}); !errors.Is(err, shaper.ErrClosed) {
				t.Fatalf("subsequent failed-generation admission = %v, want shaper.ErrClosed", err)
			}
			select {
			case <-retired:
			case <-time.After(2 * time.Second):
				t.Fatal("failed generation did not retire")
			}
			m.mu.Lock()
			remaining := len(m.paths)
			m.mu.Unlock()
			if remaining != 0 {
				t.Fatalf("failed generation remains in bind/scheduler view: %d paths", remaining)
			}
			if path.recoveryContract().Enabled {
				t.Fatal("failed generation still reports bounded recovery")
			}
			if remote, ok := path.getRemote(); ok {
				t.Fatalf("failed generation retained remote %s", remote)
			}
		})
	}
}

func TestStaleRecoveryFailureCannotRetireReplacementGeneration(t *testing.T) {
	psk := testKey(t, 0xD5)
	m := openRecoveryReviewBind(t, 1, psk)
	old := m.paths[0]
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.Open(0); err != nil {
		t.Fatal(err)
	}
	replacement := m.paths[0]
	if replacement == old || replacement.sharedPathState == old.sharedPathState {
		t.Fatal("Close/Open reused recovery generation identity")
	}
	retired := make(chan struct{}, 1)
	m.afterRecoveryRetire = func(got *sharedPathState) {
		if got == old.sharedPathState {
			retired <- struct{}{}
		}
	}
	m.abortRecoveryGeneration(old, errors.New("stale recovery failure"))
	select {
	case <-retired:
	case <-time.After(2 * time.Second):
		t.Fatal("stale recovery retirement did not complete")
	}
	m.mu.Lock()
	remaining := len(m.paths)
	var got *peerPathState
	if remaining > 0 {
		got = m.paths[0]
	}
	m.mu.Unlock()
	if remaining != 1 || got != replacement {
		t.Fatalf("stale retirement changed replacement generation: remaining=%d got=%p want=%p",
			remaining, got, replacement)
	}
	if !replacement.recoveryContract().Enabled {
		t.Fatal("replacement generation lost recovery eligibility")
	}
}

func TestProductionFECEmitEligibilityAndFallback(t *testing.T) {
	t.Run("single path eligible", func(t *testing.T) {
		m := openRecoveryReviewBind(t, 1, testKey(t, 0xD6))
		path := m.paths[0]
		var deadlines []time.Time
		path.setWriteDeadline = func(deadline time.Time) error {
			deadlines = append(deadlines, deadline)
			return nil
		}
		var emitted [][]byte
		path.writeUDP = func(payload []byte, _ netip.AddrPort) (int, error) {
			emitted = append(emitted, append([]byte(nil), payload...))
			return len(payload), nil
		}
		if !path.recoveryContract().Enabled {
			t.Fatal("exclusive single-path generation reports recovery disabled")
		}
		snapshot := m.PeerSnapshots()[0].Paths[0].Shaper
		if snapshot == nil || !snapshot.RecoveryContractEnabled {
			t.Fatalf("exclusive path reported snapshot contract = %+v, want enabled", snapshot)
		}
		if err := recoveryReviewOwner(m).emit(2, recoveryReviewWrites(path, []byte{1}, []byte{2})); err != nil {
			t.Fatal(err)
		}
		if len(deadlines) != 2 || deadlines[0].IsZero() || !deadlines[1].IsZero() {
			t.Fatalf("single-path deadline lifecycle = %v, want install+clear", deadlines)
		}
		if !equalByteSlices(emitted, [][]byte{{1}, {2}}) {
			t.Fatalf("single-path emitted = %v", emitted)
		}
	})

	t.Run("shared socket disabled", func(t *testing.T) {
		m := openRecoveryReviewBind(t, 1, testKey(t, 0xD7))
		path := m.paths[0]
		path.addViewLocked(&peerPathState{sharedPathState: path.sharedPathState})
		deadlineCalls := 0
		path.setWriteDeadline = func(time.Time) error {
			deadlineCalls++
			return nil
		}
		path.writeUDP = func(payload []byte, _ netip.AddrPort) (int, error) {
			return len(payload), nil
		}
		if path.recoveryContract().Enabled {
			t.Fatal("shared path reports recovery enabled")
		}
		snapshot := m.PeerSnapshots()[0].Paths[0].Shaper
		if snapshot == nil || snapshot.RecoveryContractEnabled {
			t.Fatalf("shared path reported snapshot contract = %+v, want disabled", snapshot)
		}
		if err := recoveryReviewOwner(m).emit(3, recoveryReviewWrites(path, []byte{1}, []byte{2})); err != nil {
			t.Fatal(err)
		}
		if deadlineCalls != 0 {
			t.Fatalf("shared fallback installed %d recovery deadlines", deadlineCalls)
		}
	})

	t.Run("mixed path disabled", func(t *testing.T) {
		m := openRecoveryReviewBind(t, 2, testKey(t, 0xD8))
		first, second := m.paths[0], m.paths[1]
		deadlineCalls := 0
		for _, path := range []*peerPathState{first, second} {
			path.setWriteDeadline = func(time.Time) error {
				deadlineCalls++
				return nil
			}
			path.writeUDP = func(payload []byte, _ netip.AddrPort) (int, error) {
				return len(payload), nil
			}
		}
		writes := append(
			recoveryReviewWrites(first, []byte{1}),
			recoveryReviewWrites(second, []byte{2})...,
		)
		if err := recoveryReviewOwner(m).emit(4, writes); err != nil {
			t.Fatal(err)
		}
		if deadlineCalls != 0 {
			t.Fatalf("mixed-path fallback installed %d coordinated deadlines", deadlineCalls)
		}
	})
}

func equalByteSlices(left, right [][]byte) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if !bytes.Equal(left[index], right[index]) {
			return false
		}
	}
	return true
}

func TestRecoveryDeadlineInterruptsRunningProductionWriter(t *testing.T) {
	m := openRecoveryReviewBind(t, 1, testKey(t, 0xD9))
	path := m.paths[0]
	installed := make(chan time.Time, 1)
	var deadlineMu sync.Mutex
	var installedDeadline time.Time
	path.setWriteDeadline = func(deadline time.Time) error {
		if !deadline.IsZero() {
			deadlineMu.Lock()
			installedDeadline = deadline
			deadlineMu.Unlock()
			installed <- deadline
		}
		return nil
	}
	writeStarted := make(chan struct{})
	var writeOnce sync.Once
	path.writeUDP = func([]byte, netip.AddrPort) (int, error) {
		writeOnce.Do(func() { close(writeStarted) })
		<-installed
		return 0, os.ErrDeadlineExceeded
	}
	predecessor := make(chan error, 1)
	go func() {
		_, err := path.shaper.WriteDatagrams(context.Background(), []shaper.Datagram{{
			Class:   shaper.ClassData,
			Payload: []byte{0xA1},
		}})
		predecessor <- err
	}()
	select {
	case <-writeStarted:
	case <-time.After(time.Second):
		t.Fatal("ordinary predecessor did not enter the production Lio writer")
	}

	retired := make(chan struct{}, 1)
	m.afterRecoveryRetire = func(*sharedPathState) { retired <- struct{}{} }
	result := make(chan error, 1)
	started := time.Now()
	go func() {
		result <- recoveryReviewOwner(m).emit(5, recoveryReviewWrites(path, []byte{1}))
	}()
	if err := <-predecessor; !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("predecessor error = %v, want deadline installed by recovery cut", err)
	}
	if err := <-result; !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("interrupted writer error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("deadline-bounded predecessor/recovery completion took %s", elapsed)
	}
	deadlineMu.Lock()
	gotDeadline := installedDeadline
	deadlineMu.Unlock()
	if gotDeadline.IsZero() {
		t.Fatal("recovery cut did not install an absolute socket deadline")
	}
	select {
	case <-retired:
	case <-time.After(2 * time.Second):
		t.Fatal("deadline-interrupted generation did not retire")
	}
}

func TestRecoveryCutPreservesInnerControlOuterSequenceFIFOAndExcludesPriority(t *testing.T) {
	psk := testKey(t, 0xDA)
	m := openRecoveryReviewBind(t, 1, psk)
	path := m.paths[0]
	codec, err := frame.NewCodec(psk)
	if err != nil {
		t.Fatal(err)
	}
	controlWire, err := codec.Encode(nil, frame.Data{OuterSeq: 10, Payload: []byte{0xC1}})
	if err != nil {
		t.Fatal(err)
	}
	dataWire, err := codec.Encode(nil, frame.Data{OuterSeq: 11, Payload: []byte{0xD1}})
	if err != nil {
		t.Fatal(err)
	}
	var emittedMu sync.Mutex
	var emitted [][]byte
	firstStarted := make(chan struct{})
	release := make(chan struct{})
	var first sync.Once
	path.setWriteDeadline = func(time.Time) error { return nil }
	path.writeUDP = func(payload []byte, _ netip.AddrPort) (int, error) {
		first.Do(func() {
			close(firstStarted)
			<-release
		})
		emittedMu.Lock()
		emitted = append(emitted, append([]byte(nil), payload...))
		emittedMu.Unlock()
		return len(payload), nil
	}
	writes := recoveryReviewWrites(path, controlWire, dataWire)
	writes[0].class = shaper.ClassControl
	groupDone := make(chan error, 1)
	go func() { groupDone <- recoveryReviewOwner(m).emit(6, writes) }()
	<-firstStarted
	shaped := path.shaper.(recoveryPathShaper)
	admitted, priorityDone, err := shaped.TryWritePriority([]byte{0xE1}, nil)
	if err != nil || !admitted {
		t.Fatalf("post-cut priority admission = %v, %v", admitted, err)
	}
	close(release)
	if err := <-groupDone; err != nil {
		t.Fatal(err)
	}
	if err := <-priorityDone; err != nil {
		t.Fatal(err)
	}
	emittedMu.Lock()
	got := append([][]byte(nil), emitted...)
	emittedMu.Unlock()
	if len(got) != 3 || !bytes.Equal(got[2], []byte{0xE1}) {
		t.Fatalf("writer order = %d datagrams, want recovery control/data then priority: %v", len(got), got)
	}
	for index, wantSeq := range []uint64{10, 11} {
		decoded, err := codec.Decode(got[index])
		if err != nil {
			t.Fatal(err)
		}
		if seq := decoded.(frame.Data).OuterSeq; seq != wantSeq {
			t.Fatalf("writer[%d] OuterSeq = %d, want %d", index, seq, wantSeq)
		}
	}
}
