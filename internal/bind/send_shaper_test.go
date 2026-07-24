package bind

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/log"
	"github.com/7mind/wanbond/internal/sched"
	"github.com/7mind/wanbond/internal/shaper"
)

type unpacedSelectionRecorder struct {
	mu            sync.Mutex
	legacyCalls   int
	unpacedCalls  int
	offeredFrames int
	class         sched.FrameClass
}

type classRecordingShaper struct {
	mu      sync.Mutex
	classes []shaper.Class
}

func (s *classRecordingShaper) WriteDatagrams(_ context.Context, datagrams []shaper.Datagram) (shaper.BatchResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, datagram := range datagrams {
		s.classes = append(s.classes, datagram.Class)
	}
	return shaper.BatchResult{Accepted: len(datagrams), Emitted: len(datagrams), FailedIndex: -1}, nil
}

func (*classRecordingShaper) Close() error { return nil }

func TestShapedSendStopsSuffixAndReportsAcceptedVersusEmitted(t *testing.T) {
	paths := loopbackPaths(1)
	recorder := &unpacedSelectionRecorder{}
	lg, err := log.New("error", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	shapers := []config.PathShaperConfig{{
		RateBytesPerSecond:      10_000_000,
		DataBurstBytes:          1472,
		ControlReserveBytes:     1472,
		MaxEncodedDatagramBytes: 1472,
		ProbeRateBytesPerSecond: 1,
		ProbeBurstBytes:         2944,
	}}
	m, err := NewMultipathWithShapers(
		paths,
		testKey(t, 0xD2),
		recorder,
		nil,
		nil,
		nil,
		nil,
		config.Amnezia{},
		shapers,
		lg,
	)
	if err != nil {
		t.Fatal(err)
	}

	writeErr := errors.New("injected terminal writer error")
	var writeCalls atomic.Int32
	m.newPathShaper = func(cfg shaper.Config, _ shaper.WriteFunc) (pathShaper, error) {
		return shaper.New(cfg, shaper.SystemClock{}, func([]byte) error {
			if writeCalls.Add(1) == 2 {
				return writeErr
			}
			return nil
		})
	}
	if _, _, err := m.Open(0); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Close() }()
	m.paths[0].setRemote(netip.MustParseAddrPort("127.0.0.1:9"))

	err = m.Send(payloadStream(3), m.virt)
	if !errors.Is(err, writeErr) {
		t.Fatalf("Send error = %v, want injected writer error", err)
	}
	if got := writeCalls.Load(); got != 2 {
		t.Fatalf("writer calls = %d, want emitted prefix plus failing datagram only", got)
	}
	if got := m.outerSeq.Load(); got != 2 {
		t.Fatalf("outer sequence = %d, want unstarted suffix left unencoded", got)
	}
	path := m.PeerSnapshots()[0].Paths[0]
	if path.ShaperAcceptedDatagrams != 2 || path.ShaperEmittedDatagrams != 1 ||
		path.ShaperWriteErrors != 1 || path.SocketWriteErrors != 0 {
		t.Fatalf("shaper counters = accepted:%d emitted:%d errors:%d socket:%d, want 2/1/1/0",
			path.ShaperAcceptedDatagrams,
			path.ShaperEmittedDatagrams,
			path.ShaperWriteErrors,
			path.SocketWriteErrors,
		)
	}

	if err := m.Send(payloadStream(1), m.virt); err != nil {
		t.Fatalf("future Send after terminal call error: %v", err)
	}
	path = m.PeerSnapshots()[0].Paths[0]
	if path.ShaperAcceptedDatagrams != 3 || path.ShaperEmittedDatagrams != 2 ||
		path.ShaperWriteErrors != 1 {
		t.Fatalf("post-recovery counters = accepted:%d emitted:%d errors:%d, want 3/2/1",
			path.ShaperAcceptedDatagrams,
			path.ShaperEmittedDatagrams,
			path.ShaperWriteErrors,
		)
	}
}

func TestShapedMixedBatchPreservesPerBufferClass(t *testing.T) {
	paths := loopbackPaths(1)
	recorder := &unpacedSelectionRecorder{}
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
	}
	m, err := NewMultipathWithShapers(paths, testKey(t, 0xD3), recorder, nil, nil, nil, nil, config.Amnezia{}, []config.PathShaperConfig{cfg}, lg)
	if err != nil {
		t.Fatal(err)
	}
	classSpy := &classRecordingShaper{}
	m.newPathShaper = func(shaper.Config, shaper.WriteFunc) (pathShaper, error) {
		return classSpy, nil
	}
	if _, _, err := m.Open(0); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Close() }()
	m.paths[0].setRemote(netip.MustParseAddrPort("127.0.0.1:9"))

	if err := m.Send([][]byte{
		wgMsg(wgMessageTransportType, 0, 1100),
		wgMsg(wgMessageTransportType, 0, 1100),
		wgMsg(wgMessageInitiationType, 0, 148),
	}, m.virt); err != nil {
		t.Fatal(err)
	}
	classSpy.mu.Lock()
	defer classSpy.mu.Unlock()
	want := []shaper.Class{shaper.ClassData, shaper.ClassData, shaper.ClassControl}
	if len(classSpy.classes) != len(want) {
		t.Fatalf("shaper classes = %v, want %v", classSpy.classes, want)
	}
	for i := range want {
		if classSpy.classes[i] != want[i] {
			t.Fatalf("shaper classes = %v, want %v", classSpy.classes, want)
		}
	}
}

func (s *unpacedSelectionRecorder) Pick(sched.FrameClass, int) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.legacyCalls++
	return sched.PickPaced
}

func (s *unpacedSelectionRecorder) PickUnpaced(class sched.FrameClass, frames int) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.unpacedCalls++
	s.offeredFrames += frames
	s.class = class
	return 0
}

func (*unpacedSelectionRecorder) SelectPath() int { return 0 }
func (*unpacedSelectionRecorder) Recompute()      {}
func (*unpacedSelectionRecorder) DataPaths() []sched.DataPath {
	return []sched.DataPath{{Index: 0, Weight: 1}}
}

func TestShapedMixedBatchSelectsAndObservesOnceWithoutLegacyAdmission(t *testing.T) {
	paths := loopbackPaths(1)
	recorder := &unpacedSelectionRecorder{}
	lg, err := log.New("error", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	shapers := []config.PathShaperConfig{{
		RateBytesPerSecond:      10_000_000,
		DataBurstBytes:          1472,
		ControlReserveBytes:     1472,
		MaxEncodedDatagramBytes: 1472,
		ProbeRateBytesPerSecond: 14_720,
		ProbeBurstBytes:         2944,
	}}
	m, err := NewMultipathWithShapers(
		paths,
		testKey(t, 0xD1),
		recorder,
		nil,
		nil,
		nil,
		nil,
		config.Amnezia{},
		shapers,
		lg,
	)
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

	dataA := wgMsg(wgMessageTransportType, 0, 1100)
	dataB := wgMsg(wgMessageTransportType, 0, 1100)
	controlLast := wgMsg(wgMessageInitiationType, 0, 148)
	if err := m.Send([][]byte{dataA, dataB, controlLast}, m.virt); err != nil {
		t.Fatal(err)
	}

	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if recorder.legacyCalls != 0 || recorder.unpacedCalls != 1 {
		t.Fatalf("selection calls: legacy=%d unpaced=%d, want 0/1", recorder.legacyCalls, recorder.unpacedCalls)
	}
	if recorder.offeredFrames != 3 {
		t.Fatalf("offered frames = %d, want 3", recorder.offeredFrames)
	}
	if recorder.class != sched.ClassData {
		t.Fatalf("mixed-batch scheduler class = %d, want ClassData", recorder.class)
	}

	_ = wirePeer.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, maxDatagram)
	for i := 0; i < 3; i++ {
		if _, _, err := wirePeer.ReadFromUDPAddrPort(buf); err != nil {
			t.Fatalf("wire datagram %d: %v", i, err)
		}
	}
}
