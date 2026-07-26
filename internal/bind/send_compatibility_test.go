package bind

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"runtime"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/fec"
	"github.com/7mind/wanbond/internal/log"
	"github.com/7mind/wanbond/internal/shaper"
)

func TestPacingOffPartialWriteKeepsBatchFramingAndFECAtomic(t *testing.T) {
	lg, err := log.New("error", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	fecCfg := &fec.Config{DataShards: 3, ParityShards: 1, Deadline: testFECDeadline}
	m, err := NewMultipath(loopbackPaths(1), testKey(t, 0xD4), stubScheduler{pick: 0}, nil, nil, fecCfg, nil, config.Amnezia{}, lg)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.Open(0); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Close() }()

	wirePeer, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer wirePeer.Close()
	m.paths[0].setRemote(wirePeer.LocalAddr().(*net.UDPAddr).AddrPort())

	oversized := make([]byte, 70_000)
	err = m.Send([][]byte{payloadStream(1)[0], oversized, payloadStream(1)[0]}, m.virt)
	if !errors.Is(err, syscall.EMSGSIZE) {
		t.Fatalf("partial write error = %v, want EMSGSIZE", err)
	}
	if got := m.outerSeq.Load(); got != 3 {
		t.Errorf("outer sequence after partial write = %d, want all 3 batch buffers framed atomically", got)
	}

	if err := m.Send(payloadStream(1), m.virt); err != nil {
		t.Errorf("next pacing-off Send = %v, want prior full FEC group already closed before the partial write", err)
	}
	if got := m.fecSend.Load().parityFrames.Load(); got != 1 {
		t.Errorf("next pacing-off Send emitted %d parity frames, want 1 from its fresh deadline-closed partial group", got)
	}
}

type generationBlockingShaper struct {
	once         sync.Once
	entered      chan struct{}
	release      chan struct{}
	ignoreCancel bool
}

func (s *generationBlockingShaper) WriteDatagrams(ctx context.Context, datagrams []shaper.Datagram) (shaper.BatchResult, error) {
	var err error
	s.once.Do(func() {
		close(s.entered)
		if s.ignoreCancel {
			<-s.release
			return
		}
		select {
		case <-s.release:
		case <-ctx.Done():
			err = ctx.Err()
		}
	})
	if err != nil {
		return shaper.BatchResult{FailedIndex: 0}, err
	}
	return shaper.BatchResult{Accepted: len(datagrams), Emitted: len(datagrams), FailedIndex: -1}, nil
}

func (*generationBlockingShaper) Close() error { return nil }
func (*generationBlockingShaper) AccountPriority(int) error {
	return nil
}

func TestPeerRebindWaitsForOldFECOwnerExit(t *testing.T) {
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
		testKey(t, 0xD7),
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
	if err := m.AddConcentratorPeer("beta", testKey(t, 0xD8), &unpacedSelectionRecorder{}, nil, nil); err != nil {
		t.Fatal(err)
	}
	blocking := &generationBlockingShaper{
		entered:      make(chan struct{}),
		release:      make(chan struct{}),
		ignoreCancel: true,
	}
	factoryCalls := 0
	m.newPathShaper = func(shaper.Config, shaper.WriteFunc) (pathShaper, error) {
		factoryCalls++
		if factoryCalls == 2 {
			return blocking, nil
		}
		return &classRecordingShaper{}, nil
	}
	if _, _, err := m.Open(0); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Close() }()

	secondary := m.peersByName["beta"]
	secondary.paths[0].setRemote(netip.MustParseAddrPort("127.0.0.1:9"))
	oldOwner := secondary.fecSend.Load().owner
	sendDone := make(chan error, 1)
	go func() {
		sendDone <- m.Send([][]byte{[]byte("old-a"), []byte("old-b")}, secondary.virt)
	}()
	<-blocking.entered

	teardownDone := make(chan bool, 1)
	go func() { teardownDone <- m.TearDownPeer("beta") }()
	for i := 0; i < 100_000 && secondary.fecSend.Load() != nil; i++ {
		runtime.Gosched()
	}
	if secondary.fecSend.Load() != nil {
		close(blocking.release)
		t.Fatal("teardown did not unpublish the old owner")
	}

	rebindDone := make(chan struct{})
	go func() {
		m.ensurePeerReceiveInstantiated(secondary)
		close(rebindDone)
	}()
	var overlapped bool
	for i := 0; i < 100_000; i++ {
		fs := secondary.fecSend.Load()
		if fs != nil {
			overlapped = fs.owner != oldOwner
			break
		}
		runtime.Gosched()
	}
	close(blocking.release)
	if !<-teardownDone {
		t.Fatal("secondary teardown was refused")
	}
	<-rebindDone
	<-sendDone
	if overlapped {
		t.Fatal("rebind published a new FEC owner before the old owner Wait completed")
	}
}

func TestShapedSendAdmissionSurvivesFECPlaneReinstantiation(t *testing.T) {
	lg, err := log.New("error", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	paths := loopbackPaths(1)
	cfg := config.PathShaperConfig{
		RateBytesPerSecond:      10_000_000,
		DataBurstBytes:          1472,
		ControlReserveBytes:     1472,
		MaxEncodedDatagramBytes: 1472,
		ProbeRateBytesPerSecond: 1,
		ProbeBurstBytes:         2944,
		PriorityReserveBytes:    2944,
	}
	primaryScheduler := &unpacedSelectionRecorder{}
	m, err := NewMultipathWithShapers(
		paths,
		testKey(t, 0xD5),
		primaryScheduler,
		nil,
		nil,
		&fec.Config{DataShards: 4, ParityShards: 1, Deadline: testFECDeadline},
		nil,
		config.Amnezia{},
		[]config.PathShaperConfig{cfg},
		lg,
	)
	if err != nil {
		t.Fatal(err)
	}
	secondaryScheduler := &unpacedSelectionRecorder{}
	if err := m.AddConcentratorPeer("beta", testKey(t, 0xD6), secondaryScheduler, nil, nil); err != nil {
		t.Fatal(err)
	}

	blocking := &generationBlockingShaper{entered: make(chan struct{}), release: make(chan struct{})}
	factoryCalls := 0
	m.newPathShaper = func(shaper.Config, shaper.WriteFunc) (pathShaper, error) {
		factoryCalls++
		if factoryCalls == 2 {
			return blocking, nil
		}
		return &classRecordingShaper{}, nil
	}
	if _, _, err := m.Open(0); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Close() }()
	if factoryCalls != 2 {
		t.Fatalf("path shaper factory calls = %d, want one per peer", factoryCalls)
	}

	secondary := m.peersByName["beta"]
	secondary.paths[0].setRemote(netip.MustParseAddrPort("127.0.0.1:9"))
	oldFEC := secondary.fecSend.Load()
	if oldFEC == nil {
		t.Fatal("secondary FEC sender is nil before teardown")
	}

	sendErr := make(chan error, 1)
	go func() {
		sendErr <- m.Send(payloadStream(2), secondary.virt)
	}()
	select {
	case <-blocking.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("shaped Send did not reach the blocking shaper")
	}
	select {
	case err := <-sendErr:
		if err != nil {
			t.Fatalf("shaped admission acknowledgement = %v, want nil before terminal write completion", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shaped Send did not acknowledge owned admission before terminal write completion")
	}

	if !m.TearDownPeer("beta") {
		t.Fatal("secondary peer teardown was refused")
	}
	m.ensurePeerReceiveInstantiated(secondary)
	newFEC := secondary.fecSend.Load()
	if newFEC == nil || newFEC == oldFEC {
		t.Fatalf("FEC sender after re-instantiation = %p, want fresh non-nil pointer distinct from %p", newFEC, oldFEC)
	}
	close(blocking.release)

	select {
	case <-oldFEC.owner.done:
	default:
		t.Fatal("peer teardown returned before the acknowledged old FEC owner exited")
	}
}
