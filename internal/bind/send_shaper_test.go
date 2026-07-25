package bind

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/frame"
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
func (*classRecordingShaper) AccountPriority(int) error {
	return nil
}

type forwardingRecordingShaper struct {
	write        shaper.WriteFunc
	firstEntered chan struct{}
	releaseFirst chan struct{}
	once         sync.Once

	mu      sync.Mutex
	classes []shaper.Class
	sizes   []int
}

func (s *forwardingRecordingShaper) WriteDatagrams(_ context.Context, datagrams []shaper.Datagram) (shaper.BatchResult, error) {
	s.once.Do(func() {
		close(s.firstEntered)
		<-s.releaseFirst
	})
	for i, datagram := range datagrams {
		s.mu.Lock()
		s.classes = append(s.classes, datagram.Class)
		s.sizes = append(s.sizes, len(datagram.Payload))
		s.mu.Unlock()
		if err := s.write(datagram.Payload); err != nil {
			return shaper.BatchResult{Accepted: i + 1, Emitted: i, FailedIndex: i}, err
		}
	}
	return shaper.BatchResult{Accepted: len(datagrams), Emitted: len(datagrams), FailedIndex: -1}, nil
}

func (*forwardingRecordingShaper) AccountPriority(int) error { return nil }
func (*forwardingRecordingShaper) Close() error              { return nil }

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
		PriorityReserveBytes:    2944,
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
		PriorityReserveBytes:    2944,
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
		PriorityReserveBytes:    2944,
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

func TestMaximumMixedBatchControlLastPreservesFIFOAndUsesCurrentRemote(t *testing.T) {
	paths := loopbackPaths(1)
	recorder := &unpacedSelectionRecorder{}
	lg, err := log.New("error", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	const lmax = 1472
	cfg := config.PathShaperConfig{
		RateBytesPerSecond:      10_000_000,
		DataBurstBytes:          2 * lmax,
		ControlReserveBytes:     lmax,
		MaxEncodedDatagramBytes: lmax,
		ProbeRateBytesPerSecond: 14_720,
		ProbeBurstBytes:         2 * lmax,
		PriorityReserveBytes:    2 * lmax,
	}
	m, err := NewMultipathWithShapers(
		paths,
		testKey(t, 0xD7),
		recorder,
		nil,
		nil,
		nil,
		nil,
		config.Amnezia{},
		[]config.PathShaperConfig{cfg},
		lg,
	)
	if err != nil {
		t.Fatal(err)
	}
	forwarder := &forwardingRecordingShaper{
		firstEntered: make(chan struct{}),
		releaseFirst: make(chan struct{}),
	}
	m.newPathShaper = func(_ shaper.Config, write shaper.WriteFunc) (pathShaper, error) {
		forwarder.write = write
		return forwarder, nil
	}
	if _, _, err := m.Open(0); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Close() }()

	oldRemote, oldAddr := rawPeer(t)
	newRemote, newAddr := rawPeer(t)
	m.paths[0].setRemote(oldAddr)

	dataA := wgMsg(wgMessageTransportType, 0, lmax-frame.DataOverhead)
	dataB := wgMsg(wgMessageTransportType, 0, lmax-frame.DataOverhead)
	controlLast := wgMsg(wgMessageInitiationType, 0, 148)
	sendResult := make(chan error, 1)
	go func() {
		sendResult <- m.Send([][]byte{dataA, dataB, controlLast}, m.virt)
	}()
	select {
	case <-forwarder.firstEntered:
	case <-time.After(time.Second):
		t.Fatal("first maximum DATA datagram did not reach the selected path shaper")
	}

	// Rekey the path while its first shaped datagram is retained. The writer must
	// resolve the current remote at actual emission, not retain the old address
	// captured at admission/selection time.
	m.paths[0].setRemote(newAddr)
	close(forwarder.releaseFirst)
	select {
	case err := <-sendResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("mixed batch did not make progress after the first write was released")
	}

	recorder.mu.Lock()
	if recorder.legacyCalls != 0 || recorder.unpacedCalls != 1 || recorder.offeredFrames != 3 || recorder.class != sched.ClassData {
		t.Fatalf("selection = legacy:%d unpaced:%d frames:%d class:%d, want 0/1/3/ClassData",
			recorder.legacyCalls, recorder.unpacedCalls, recorder.offeredFrames, recorder.class)
	}
	recorder.mu.Unlock()

	forwarder.mu.Lock()
	gotClasses := append([]shaper.Class(nil), forwarder.classes...)
	gotSizes := append([]int(nil), forwarder.sizes...)
	forwarder.mu.Unlock()
	wantClasses := []shaper.Class{shaper.ClassData, shaper.ClassData, shaper.ClassControl}
	if len(gotClasses) != len(wantClasses) {
		t.Fatalf("classes = %v, want %v", gotClasses, wantClasses)
	}
	for i := range wantClasses {
		if gotClasses[i] != wantClasses[i] {
			t.Fatalf("classes = %v, want %v", gotClasses, wantClasses)
		}
	}
	if gotSizes[0]+gotSizes[1] != cfg.DataBurstBytes {
		t.Fatalf("maximum bulk encoded bytes = %d, want exact B=%d", gotSizes[0]+gotSizes[1], cfg.DataBurstBytes)
	}
	if gotSizes[2] > cfg.ControlReserveBytes {
		t.Fatalf("control encoded bytes = %d, exceed C=%d", gotSizes[2], cfg.ControlReserveBytes)
	}

	codec, err := frame.NewCodec(testKey(t, 0xD7))
	if err != nil {
		t.Fatal(err)
	}
	wantPayloads := [][]byte{dataA, dataB, controlLast}
	buf := make([]byte, maxDatagram)
	if err := newRemote.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	for i, want := range wantPayloads {
		n, err := newRemote.Read(buf)
		if err != nil {
			t.Fatalf("new remote datagram %d: %v", i, err)
		}
		decoded, err := codec.Decode(buf[:n])
		if err != nil {
			t.Fatal(err)
		}
		data, ok := decoded.(frame.Data)
		if !ok || !bytes.Equal(data.Payload, want) {
			t.Fatalf("new remote datagram %d = %#v, want DATA payload length %d", i, decoded, len(want))
		}
	}
	if err := oldRemote.SetReadDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if _, err := oldRemote.Read(buf); err == nil {
		t.Fatal("old remote received a datagram admitted before the remote rekey")
	} else if !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("old remote read error = %v, want timeout", err)
	}
}
