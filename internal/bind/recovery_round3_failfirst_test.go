package bind

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/frame"
	"github.com/7mind/wanbond/internal/shaper"
)

func TestRecoveryTrancheUsesOneAbsoluteDeadlineWithoutRetry(t *testing.T) {
	m := openRecoveryReviewBind(t, 1, testKey(t, 0xB1))
	path := m.paths[0]
	var mu sync.Mutex
	var active time.Time
	var installed []time.Time
	var writerDeadlines []time.Time
	path.setWriteDeadline = func(deadline time.Time) error {
		mu.Lock()
		active = deadline
		installed = append(installed, deadline)
		mu.Unlock()
		return nil
	}
	path.writeUDP = func(payload []byte, _ netip.AddrPort) (int, error) {
		mu.Lock()
		writerDeadlines = append(writerDeadlines, active)
		mu.Unlock()
		return len(payload), nil
	}

	writes := recoveryReviewWrites(path, []byte{1}, []byte{2}, []byte{3})
	if err := recoveryReviewOwner(m).emit(11, writes); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(installed) != 2 || installed[0].IsZero() || !installed[1].IsZero() {
		t.Fatalf("deadline lifecycle = %v, want one absolute install and one clear", installed)
	}
	if len(writerDeadlines) != len(writes) {
		t.Fatalf("writer calls = %d, want exactly %d with no retry", len(writerDeadlines), len(writes))
	}
	for index, deadline := range writerDeadlines {
		if deadline != installed[0] {
			t.Fatalf("writer[%d] deadline = %s, want shared absolute %s",
				index, deadline, installed[0])
		}
	}
}

func TestCloseInterruptsSocketBlockedShaperAndStaleRetirementCannotAffectReopen(t *testing.T) {
	psk := testKey(t, 0xB2)
	m := openRecoveryReviewBind(t, 1, psk)
	old := m.paths[0]
	writerStarted := make(chan struct{})
	var startOnce sync.Once
	old.writeUDP = func([]byte, netip.AddrPort) (int, error) {
		startOnce.Do(func() { close(writerStarted) })
		buffer := make([]byte, 1)
		_, _, err := old.conn.ReadFromUDPAddrPort(buffer)
		return 0, err
	}
	writeDone := make(chan error, 1)
	go func() {
		_, err := old.shaper.WriteDatagrams(context.Background(), []shaper.Datagram{{
			Class:   shaper.ClassData,
			Payload: []byte{1},
		}})
		writeDone <- err
	}()
	select {
	case <-writerStarted:
	case <-time.After(time.Second):
		t.Fatal("shaper writer did not enter its blocking socket call")
	}

	closeDone := make(chan error, 1)
	started := time.Now()
	go func() { closeDone <- m.Close() }()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(250 * time.Millisecond):
		_ = old.conn.Close()
		<-closeDone
		t.Fatal("Multipath.Close did not close the socket before joining the blocked shaper writer")
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("Close took %s, want bounded socket-interrupt teardown", elapsed)
	}
	if err := <-writeDone; !errors.Is(err, net.ErrClosed) {
		t.Fatalf("blocked writer completion = %v, want socket close", err)
	}

	if _, _, err := m.Open(0); err != nil {
		t.Fatal(err)
	}
	replacement := m.paths[0]
	if replacement == old || replacement.sharedPathState == old.sharedPathState {
		t.Fatal("Open reused the retired socket generation")
	}
	replacement.setRemote(netip.MustParseAddrPort("127.0.0.1:9"))
	retired := make(chan struct{}, 1)
	m.afterRecoveryRetire = func(got *sharedPathState) {
		if got == old.sharedPathState {
			retired <- struct{}{}
		}
	}
	m.abortRecoveryGeneration(old, errors.New("stale predecessor failure"))
	select {
	case <-retired:
	case <-time.After(time.Second):
		t.Fatal("stale retirement did not complete")
	}
	m.mu.Lock()
	remaining := len(m.paths)
	var current *peerPathState
	if remaining == 1 {
		current = m.paths[0]
	}
	m.mu.Unlock()
	if remaining != 1 || current != replacement {
		t.Fatalf("stale retirement changed replacement: remaining=%d current=%p replacement=%p",
			remaining, current, replacement)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRecoveryCutKeepsLowerDataBeforeHigherControlAndFreezesPostCutPriority(t *testing.T) {
	psk := testKey(t, 0xB3)
	m := openRecoveryReviewBind(t, 1, psk)
	path := m.paths[0]
	codec, err := frame.NewCodec(psk)
	if err != nil {
		t.Fatal(err)
	}
	dataWire, err := codec.Encode(nil, frame.Data{OuterSeq: 20, Payload: []byte{0xD1}})
	if err != nil {
		t.Fatal(err)
	}
	controlWire, err := codec.Encode(nil, frame.Data{OuterSeq: 21, Payload: []byte{0xC1}})
	if err != nil {
		t.Fatal(err)
	}

	var emittedMu sync.Mutex
	var emitted [][]byte
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var first sync.Once
	path.setWriteDeadline = func(time.Time) error { return nil }
	path.writeUDP = func(payload []byte, _ netip.AddrPort) (int, error) {
		first.Do(func() {
			close(firstStarted)
			<-releaseFirst
		})
		emittedMu.Lock()
		emitted = append(emitted, append([]byte(nil), payload...))
		emittedMu.Unlock()
		return len(payload), nil
	}
	writes := recoveryReviewWrites(path, dataWire, controlWire)
	writes[1].class = shaper.ClassControl
	groupDone := make(chan error, 1)
	go func() { groupDone <- recoveryReviewOwner(m).emit(12, writes) }()
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("recovery DATA did not enter writer")
	}

	shaped := path.shaper.(recoveryPathShaper)
	var generated int
	var priorityDone []<-chan error
	for attempt := byte(0); attempt < 5; attempt++ {
		admitted, done, writeErr := shaped.TryWritePriorityGenerated(
			recoveryReviewShaperConfig().MaxEncodedDatagramBytes,
			func() ([]byte, shaper.WriteFunc, error) {
				generated++
				payload := bytes.Repeat([]byte{0xE0 + attempt}, recoveryReviewShaperConfig().MaxEncodedDatagramBytes)
				return payload, nil, nil
			},
		)
		if writeErr != nil {
			t.Fatal(writeErr)
		}
		if admitted {
			priorityDone = append(priorityDone, done)
		}
	}
	if len(priorityDone) != 2 || generated != 2 {
		t.Fatalf("post-cut priority admissions/generations = %d/%d, want bounded P=2/2",
			len(priorityDone), generated)
	}
	for range 3 {
		m.emitProbes()
	}
	if got := path.probePriorityCoalesced.Load(); got != 3 {
		t.Fatalf("post-cut ordinary probe coalescence = %d, want 3", got)
	}

	close(releaseFirst)
	if err := <-groupDone; err != nil {
		t.Fatal(err)
	}
	for _, done := range priorityDone {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	emittedMu.Lock()
	got := append([][]byte(nil), emitted...)
	emittedMu.Unlock()
	if len(got) != 4 {
		t.Fatalf("emitted datagrams = %d, want DATA, CONTROL, then two priority", len(got))
	}
	for index, wantSeq := range []uint64{20, 21} {
		decoded, decodeErr := codec.Decode(got[index])
		if decodeErr != nil {
			t.Fatal(decodeErr)
		}
		if seq := decoded.(frame.Data).OuterSeq; seq != wantSeq {
			t.Fatalf("writer[%d] OuterSeq = %d, want %d", index, seq, wantSeq)
		}
	}
	if got[2][0] != 0xE0 || got[3][0] != 0xE1 {
		t.Fatalf("post-cut priority order = %x/%x, want E0/E1 after group", got[2][0], got[3][0])
	}
	raw, err := path.prober.SendProbe()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := codec.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if seq := decoded.(frame.Probe).ProbeSeq; seq != 0 {
		t.Fatalf("coalesced post-cut probes consumed sequence: next=%d, want 0", seq)
	}
}

func saturateProductionPriority(
	t testing.TB,
	path *peerPathState,
) func() {
	t.Helper()
	writeStarted := make(chan struct{})
	releaseWrite := make(chan struct{})
	var first sync.Once
	path.writeUDP = func(payload []byte, _ netip.AddrPort) (int, error) {
		first.Do(func() {
			close(writeStarted)
			<-releaseWrite
		})
		return len(payload), nil
	}
	dataDone := make(chan error, 1)
	go func() {
		_, err := path.shaper.WriteDatagrams(context.Background(), []shaper.Datagram{{
			Class:   shaper.ClassData,
			Payload: []byte{1},
		}})
		dataDone <- err
	}()
	select {
	case <-writeStarted:
	case <-time.After(time.Second):
		t.Fatal("DATA did not block the production writer")
	}

	shaped := path.shaper.(recoveryPathShaper)
	var priorityDone []<-chan error
	for range 2 {
		admitted, done, err := shaped.TryWritePriority(
			make([]byte, recoveryReviewShaperConfig().MaxEncodedDatagramBytes),
			nil,
		)
		if err != nil || !admitted {
			t.Fatalf("priority saturation admission = %v, %v", admitted, err)
		}
		priorityDone = append(priorityDone, done)
	}
	return func() {
		close(releaseWrite)
		if err := <-dataDone; err != nil {
			t.Fatal(err)
		}
		for _, done := range priorityDone {
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestProductionPriorityOverflowTelemetrySeparatesProbeEchoAndPMTU(t *testing.T) {
	t.Run("ordinary probe coalesces before generation", func(t *testing.T) {
		psk := testKey(t, 0xB4)
		m := openRecoveryReviewBind(t, 1, psk)
		path := m.paths[0]
		finish := saturateProductionPriority(t, path)
		defer finish()

		started := time.Now()
		m.emitProbes()
		if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
			t.Fatalf("ordinary probe overflow blocked for %s", elapsed)
		}
		if got := path.probePriorityCoalesced.Load(); got != 1 {
			t.Fatalf("ordinary probe coalesced = %d, want 1", got)
		}
		if got := path.echoPriorityOverflow.Load(); got != 0 {
			t.Fatalf("echo overflow = %d after ordinary probe, want 0", got)
		}
		if got := path.pmtuAdmissionCanceled.Load(); got != 0 {
			t.Fatalf("PMTU cancellation = %d after ordinary probe, want 0", got)
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
			t.Fatalf("coalesced ordinary probe invoked generator: next sequence=%d", seq)
		}
	})

	t.Run("reactive echo overflows independently", func(t *testing.T) {
		psk := testKey(t, 0xB5)
		m := openRecoveryReviewBind(t, 1, psk)
		path := m.paths[0]
		finish := saturateProductionPriority(t, path)
		defer finish()

		_, source := rawPeer(t)
		request, err := frame.Encode(psk, frame.Probe{
			PathID:         0,
			ProbeSeq:       41,
			TimestampNanos: time.Now().UnixNano(),
		})
		if err != nil {
			t.Fatal(err)
		}
		started := time.Now()
		m.handleInbound(path, request, source)
		if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
			t.Fatalf("reactive echo overflow blocked receive for %s", elapsed)
		}
		if got := path.echoPriorityOverflow.Load(); got != 1 {
			t.Fatalf("reactive echo overflow = %d, want 1", got)
		}
		if got := path.probePriorityCoalesced.Load(); got != 0 {
			t.Fatalf("ordinary probe coalesced = %d after reactive echo, want 0", got)
		}
		if got := path.pmtuAdmissionCanceled.Load(); got != 0 {
			t.Fatalf("PMTU cancellation = %d after reactive echo, want 0", got)
		}
	})
}
