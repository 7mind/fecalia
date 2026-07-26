package bind

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/fec"
	"github.com/7mind/wanbond/internal/log"
	"github.com/7mind/wanbond/internal/sched"
	"github.com/7mind/wanbond/internal/shaper"
	"go.uber.org/goleak"
)

type closeBlockingShaper struct {
	closeStarted chan struct{}
	releaseClose chan struct{}
	closeOnce    sync.Once
}

type callbackClosingShaper struct {
	onClose func()
}

type trackedStagedShaper struct {
	stopped atomic.Bool
	waited  atomic.Bool
}

type controlledShaper struct {
	write   shaper.WriteFunc
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	unblock sync.Once

	mu      sync.Mutex
	stopped bool
	calls   sync.WaitGroup
	writes  atomic.Int32
}

func newControlledShaper(write shaper.WriteFunc, blocked bool) *controlledShaper {
	s := &controlledShaper{
		write:   write,
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	if !blocked {
		s.unblock.Do(func() { close(s.release) })
	}
	return s
}

func (s *controlledShaper) releaseWrite() {
	s.unblock.Do(func() { close(s.release) })
}

func (s *controlledShaper) WriteDatagrams(_ context.Context, datagrams []shaper.Datagram) (shaper.BatchResult, error) {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return shaper.BatchResult{FailedIndex: -1}, shaper.ErrClosed
	}
	s.calls.Add(1)
	s.mu.Unlock()
	defer s.calls.Done()

	s.once.Do(func() { close(s.entered) })
	<-s.release
	s.mu.Lock()
	stopped := s.stopped
	s.mu.Unlock()
	if stopped {
		return shaper.BatchResult{Accepted: len(datagrams), FailedIndex: 0}, shaper.ErrClosed
	}
	for i, datagram := range datagrams {
		if err := s.write(datagram.Payload); err != nil {
			return shaper.BatchResult{Accepted: len(datagrams), Emitted: i, FailedIndex: i}, err
		}
		s.writes.Add(1)
	}
	return shaper.BatchResult{Accepted: len(datagrams), Emitted: len(datagrams), FailedIndex: -1}, nil
}

func (s *controlledShaper) AccountPriority(int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return shaper.ErrClosed
	}
	return nil
}

func (s *controlledShaper) Close() error {
	s.Stop()
	return s.Wait()
}

func (s *controlledShaper) Stop() {
	s.mu.Lock()
	s.stopped = true
	s.mu.Unlock()
}

func (s *controlledShaper) Wait() error {
	s.calls.Wait()
	return nil
}

func (s *controlledShaper) isStopped() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopped
}

func (s *trackedStagedShaper) WriteDatagrams(_ context.Context, datagrams []shaper.Datagram) (shaper.BatchResult, error) {
	if s.stopped.Load() {
		return shaper.BatchResult{FailedIndex: -1}, shaper.ErrClosed
	}
	return shaper.BatchResult{Accepted: len(datagrams), Emitted: len(datagrams), FailedIndex: -1}, nil
}

func (s *trackedStagedShaper) AccountPriority(int) error {
	if s.stopped.Load() {
		return shaper.ErrClosed
	}
	return nil
}

func (s *trackedStagedShaper) Close() error {
	s.Stop()
	return s.Wait()
}

func (s *trackedStagedShaper) Stop() {
	s.stopped.Store(true)
}

func (s *trackedStagedShaper) Wait() error {
	s.waited.Store(true)
	return nil
}

type faultDynamicScheduler struct {
	sched.DynamicScheduler
	mu        sync.Mutex
	members   int
	addErr    error
	skewAdd   bool
	setErr    error
	forcePick int
	setCalls  int
}

func (s *faultDynamicScheduler) PickUnpaced(class sched.FrameClass, frames int) int {
	s.mu.Lock()
	forced := s.forcePick
	s.mu.Unlock()
	if forced >= 0 {
		return forced
	}
	return s.DynamicScheduler.(sched.UnpacedPicker).PickUnpaced(class, frames)
}

func (s *faultDynamicScheduler) AddPath(admission sched.PathAdmission) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.addErr != nil {
		return 0, s.addErr
	}
	idx, err := s.DynamicScheduler.AddPath(admission)
	if err != nil {
		return idx, err
	}
	s.members++
	if s.skewAdd {
		return idx + 1, nil
	}
	return idx, nil
}

func (s *faultDynamicScheduler) RemovePath(idx int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.DynamicScheduler.RemovePath(idx); err != nil {
		return err
	}
	s.members--
	return nil
}

func (s *faultDynamicScheduler) SetPaths(paths []sched.PathAdmission) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setCalls++
	if s.setErr != nil {
		return s.setErr
	}
	if err := s.DynamicScheduler.SetPaths(paths); err != nil {
		return err
	}
	s.members = len(paths)
	return nil
}

func (s *faultDynamicScheduler) memberCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.members
}

func (s *faultDynamicScheduler) force(index int) {
	s.mu.Lock()
	s.forcePick = index
	s.mu.Unlock()
}

func (s *faultDynamicScheduler) setPathsCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.setCalls
}

func (*callbackClosingShaper) WriteDatagrams(_ context.Context, datagrams []shaper.Datagram) (shaper.BatchResult, error) {
	return shaper.BatchResult{Accepted: len(datagrams), Emitted: len(datagrams), FailedIndex: -1}, nil
}

func (*callbackClosingShaper) AccountPriority(int) error {
	return nil
}

func (s *callbackClosingShaper) Close() error {
	s.onClose()
	return nil
}

func (*closeBlockingShaper) WriteDatagrams(_ context.Context, datagrams []shaper.Datagram) (shaper.BatchResult, error) {
	return shaper.BatchResult{Accepted: len(datagrams), Emitted: len(datagrams), FailedIndex: -1}, nil
}

func (*closeBlockingShaper) AccountPriority(int) error {
	return nil
}

func (s *closeBlockingShaper) Close() error {
	s.closeOnce.Do(func() {
		close(s.closeStarted)
		<-s.releaseClose
	})
	return nil
}

func lifecycleShaperConfig() config.PathShaperConfig {
	return config.PathShaperConfig{
		RateBytesPerSecond:      10_000_000,
		DataBurstBytes:          1472,
		ControlReserveBytes:     1472,
		MaxEncodedDatagramBytes: 1472,
		ProbeRateBytesPerSecond: 1,
		ProbeBurstBytes:         2944,
		PriorityReserveBytes:    2944,
	}
}

func assertBindLockReleasedDuringRetirement(t *testing.T, m *Multipath, retirementStarted <-chan struct{}, releaseRetirement chan<- struct{}) {
	t.Helper()
	select {
	case <-retirementStarted:
	case <-time.After(time.Second):
		t.Fatal("path generation retirement did not start")
	}

	acquired := make(chan struct{})
	go func() {
		m.mu.Lock()
		close(acquired)
		m.mu.Unlock()
	}()
	select {
	case <-acquired:
	case <-time.After(100 * time.Millisecond):
		close(releaseRetirement)
		select {
		case <-acquired:
		case <-time.After(time.Second):
			t.Error("bind mutex acquisition was not observed after releasing generation retirement")
			return
		}
		t.Error("bind mutex stayed held while path generation retirement waited")
		return
	}
	close(releaseRetirement)
}

func waitDirectWriteAdmissionClosed(t *testing.T, sp *sharedPathState) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		sp.writeMu.Lock()
		closed := sp.writesClosed
		sp.writeMu.Unlock()
		if closed {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("direct-write admission did not close")
		}
		time.Sleep(time.Millisecond)
	}
}

func assertBindLockAvailable(t *testing.T, m *Multipath) {
	t.Helper()
	acquired := make(chan struct{})
	go func() {
		m.mu.Lock()
		close(acquired)
		m.mu.Unlock()
	}()
	select {
	case <-acquired:
	case <-time.After(100 * time.Millisecond):
		t.Error("bind mutex acquisition was not observed while a direct writer drained")
	}
}

func TestDirectWriteGenerationGateClosesSocketBeforeWriterJoin(t *testing.T) {
	for _, test := range []struct {
		name string
		run  func(*testing.T)
	}{
		{
			name: "pacing-off Send versus Close",
			run: func(t *testing.T) {
				m, err := newMultipath(t, loopbackPaths(1), testKey(t, 0xF0))
				if err != nil {
					t.Fatal(err)
				}
				if _, _, err := m.Open(0); err != nil {
					t.Fatal(err)
				}
				if m.shaperConfigs != nil {
					t.Fatal("direct-write test unexpectedly enabled shaping")
				}
				_, remoteAddr := rawPeer(t)
				pp := m.paths[0]
				pp.setRemote(remoteAddr)
				oldConn := pp.conn
				writerEntered := make(chan struct{})
				pp.writeUDP = func([]byte, netip.AddrPort) (int, error) {
					close(writerEntered)
					buffer := make([]byte, 1)
					_, _, err := oldConn.ReadFromUDPAddrPort(buffer)
					return 0, err
				}

				sendDone := make(chan error, 1)
				go func() {
					sendDone <- m.Send([][]byte{wgMsg(wgMessageTransportType, 0, 64)}, m.virt)
				}()
				select {
				case <-writerEntered:
				case <-time.After(time.Second):
					t.Fatal("pacing-off Send did not enter the direct-write gate")
				}

				closeDone := make(chan error, 1)
				go func() { closeDone <- m.Close() }()
				waitDirectWriteAdmissionClosed(t, pp.sharedPathState)
				assertBindLockAvailable(t, m)
				if _, err := pp.writeToUDPAddrPort([]byte{1}, remoteAddr); !errors.Is(err, net.ErrClosed) {
					t.Fatalf("post-detach gated write = %v, want net.ErrClosed", err)
				}
				if _, err := oldConn.WriteToUDPAddrPort([]byte{2}, remoteAddr); !errors.Is(err, net.ErrClosed) {
					t.Fatalf("socket remained open while Close joined the blocked writer: %v", err)
				}
				select {
				case err := <-closeDone:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(time.Second):
					t.Fatal("Close did not join the socket-interrupted writer")
				}
				if err := <-sendDone; !errors.Is(err, net.ErrClosed) {
					t.Fatalf("interrupted pacing-off Send = %v, want net.ErrClosed", err)
				}
				if _, err := oldConn.WriteToUDPAddrPort([]byte{3}, remoteAddr); !errors.Is(err, net.ErrClosed) {
					t.Fatalf("retired socket write = %v, want net.ErrClosed", err)
				}
			},
		},
		{
			name: "generated PROBE versus RemovePath",
			run: func(t *testing.T) {
				m, _, _ := newProbingMultipath(t, loopbackPaths(2), testKey(t, 0xF1), newFakeClock())
				if _, _, err := m.Open(0); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = m.Close() })
				if m.shaperConfigs != nil {
					t.Fatal("direct-write test unexpectedly enabled shaping")
				}
				remote, remoteAddr := rawPeer(t)
				for _, path := range m.paths {
					path.setRemote(remoteAddr)
				}
				pp := m.paths[0]
				oldConn := pp.conn
				writerEntered := make(chan struct{})
				pp.writeUDP = func([]byte, netip.AddrPort) (int, error) {
					close(writerEntered)
					buffer := make([]byte, 1)
					_, _, err := oldConn.ReadFromUDPAddrPort(buffer)
					return 0, err
				}

				probesDone := make(chan struct{})
				go func() {
					m.emitProbes()
					close(probesDone)
				}()
				select {
				case <-writerEntered:
				case <-time.After(time.Second):
					t.Fatal("generated PROBE did not enter the direct-write gate")
				}

				removeDone := make(chan error, 1)
				go func() { removeDone <- m.RemovePath("a") }()
				waitDirectWriteAdmissionClosed(t, pp.sharedPathState)
				assertBindLockAvailable(t, m)
				if _, err := pp.writeToUDPAddrPort([]byte{1}, remoteAddr); !errors.Is(err, net.ErrClosed) {
					t.Fatalf("post-detach generated-path write = %v, want net.ErrClosed", err)
				}
				if _, err := oldConn.WriteToUDPAddrPort([]byte{2}, remoteAddr); !errors.Is(err, net.ErrClosed) {
					t.Fatalf("removed socket remained open while joining generated writer: %v", err)
				}
				select {
				case err := <-removeDone:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(time.Second):
					t.Fatal("RemovePath did not join the socket-interrupted generated writer")
				}
				select {
				case <-probesDone:
				case <-time.After(time.Second):
					t.Fatal("generated PROBE did not finish")
				}
				if _, err := oldConn.WriteToUDPAddrPort([]byte{3}, remoteAddr); !errors.Is(err, net.ErrClosed) {
					t.Fatalf("removed socket write = %v, want net.ErrClosed", err)
				}
				_ = remote
			},
		},
	} {
		t.Run(test.name, test.run)
	}
}

func TestTransitionMutexSerializesCloseAndReplacementOpen(t *testing.T) {
	m, _, _ := newProbingMultipath(t, loopbackPaths(1), testKey(t, 0xF9), newFakeClock())
	m.shaperConfigs = []config.PathShaperConfig{lifecycleShaperConfig()}
	blocker := &closeBlockingShaper{
		closeStarted: make(chan struct{}),
		releaseClose: make(chan struct{}),
	}
	m.newPathShaper = func(shaper.Config, shaper.WriteFunc) (pathShaper, error) {
		return blocker, nil
	}
	if _, _, err := m.Open(0); err != nil {
		t.Fatal(err)
	}
	oldConn := m.paths[0].conn
	_, remoteAddr := rawPeer(t)

	replacementFactoryEntered := make(chan struct{})
	var replacement *trackedStagedShaper
	var oldClosedBeforeReplacement atomic.Bool
	m.newPathShaper = func(shaper.Config, shaper.WriteFunc) (pathShaper, error) {
		if _, err := oldConn.WriteToUDPAddrPort([]byte{1}, remoteAddr); errors.Is(err, net.ErrClosed) {
			oldClosedBeforeReplacement.Store(true)
		}
		close(replacementFactoryEntered)
		replacement = &trackedStagedShaper{}
		return replacement, nil
	}

	closeDone := make(chan error, 1)
	go func() { closeDone <- m.Close() }()
	select {
	case <-blocker.closeStarted:
	case <-time.After(time.Second):
		t.Fatal("old-generation retirement did not block")
	}

	openAttempted := make(chan struct{})
	openDone := make(chan error, 1)
	go func() {
		close(openAttempted)
		_, _, err := m.Open(0)
		openDone <- err
	}()
	<-openAttempted
	select {
	case <-replacementFactoryEntered:
		close(blocker.releaseClose)
		t.Fatal("replacement Open bound a socket before old-generation retirement released")
	case <-time.After(50 * time.Millisecond):
	}
	m.mu.Lock()
	boundPaths, sharedSockets := len(m.paths), len(m.shared)
	m.mu.Unlock()
	if boundPaths != 0 || sharedSockets != 0 {
		close(blocker.releaseClose)
		t.Fatalf("replacement published before old retirement: paths=%d shared=%d", boundPaths, sharedSockets)
	}
	select {
	case err := <-openDone:
		close(blocker.releaseClose)
		t.Fatalf("replacement Open returned before old retirement release: %v", err)
	default:
	}

	close(blocker.releaseClose)
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
	if err := <-openDone; err != nil {
		t.Fatal(err)
	}
	if !oldClosedBeforeReplacement.Load() {
		t.Fatal("replacement shaper construction began before the old socket closed")
	}
	if replacement == nil || len(m.paths) != 1 || m.paths[0].shaper != replacement || m.paths[0].conn == oldConn {
		t.Fatal("replacement Open did not publish one distinct socket/shaper generation")
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestAddPathSchedulerSkewRollsBackActualAdmission(t *testing.T) {
	m, _, baseScheduler := newProbingMultipath(t, loopbackPaths(1), testKey(t, 0xF2), newFakeClock())
	cfg := lifecycleShaperConfig()
	m.shaperConfigs = []config.PathShaperConfig{cfg}
	var created []*trackedStagedShaper
	m.newPathShaper = func(shaper.Config, shaper.WriteFunc) (pathShaper, error) {
		s := &trackedStagedShaper{}
		created = append(created, s)
		return s, nil
	}
	if _, _, err := m.Open(0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	if len(created) != 1 {
		t.Fatalf("boot shapers = %d, want 1", len(created))
	}

	dyn, ok := baseScheduler.(sched.DynamicScheduler)
	if !ok {
		t.Fatal("test scheduler does not support dynamic membership")
	}
	fault := &faultDynamicScheduler{DynamicScheduler: dyn, members: 1, skewAdd: true}
	m.scheduler = fault
	beforeDefs := len(m.defs)
	beforeProbers := len(m.probers)
	beforeShared := len(m.shared)
	err := m.AddPathWithShaper(config.Path{
		Name:       "skewed",
		SourceAddr: netip.MustParseAddr("127.0.0.1"),
	}, cfg)
	if err == nil || err.Error() != "bind: scheduler/path index skew after add: sched=2 bind=1" {
		t.Fatalf("AddPath skew error = %v, want exact scheduler/path skew", err)
	}
	if got := fault.memberCount(); got != 1 {
		t.Fatalf("scheduler members after skew rollback = %d, want baseline 1", got)
	}
	if len(m.defs) != beforeDefs || len(m.probers) != beforeProbers || len(m.shared) != beforeShared {
		t.Fatalf(
			"durable membership changed after skew rollback: defs=%d/%d probers=%d/%d shared=%d/%d",
			len(m.defs),
			beforeDefs,
			len(m.probers),
			beforeProbers,
			len(m.shared),
			beforeShared,
		)
	}
	if len(created) != 2 {
		t.Fatalf("shapers after skew rollback = %d, want boot + rejected", len(created))
	}
	rejected := created[1]
	if !rejected.stopped.Load() || !rejected.waited.Load() {
		t.Fatalf("rejected shaper lifecycle = stopped:%v waited:%v, want true/true", rejected.stopped.Load(), rejected.waited.Load())
	}
	if err := rejected.AccountPriority(1); !errors.Is(err, shaper.ErrClosed) {
		t.Fatalf("rejected shaper admission = %v, want shaper.ErrClosed", err)
	}
}

func assertTrackedShapersRetired(t *testing.T, shapers []*trackedStagedShaper) {
	t.Helper()
	for i, retired := range shapers {
		if !retired.stopped.Load() || !retired.waited.Load() {
			t.Fatalf(
				"shaper %d lifecycle = stopped:%v waited:%v, want true/true",
				i,
				retired.stopped.Load(),
				retired.waited.Load(),
			)
		}
		if err := retired.AccountPriority(1); !errors.Is(err, shaper.ErrClosed) {
			t.Fatalf("shaper %d admission = %v, want shaper.ErrClosed", i, err)
		}
	}
}

func waitControlledShaperStopped(t *testing.T, s *controlledShaper) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for !s.isStopped() {
		if time.Now().After(deadline) {
			t.Fatal("controlled shaper did not stop")
		}
		time.Sleep(time.Millisecond)
	}
}

func waitControlledShaperEntered(t *testing.T, s *controlledShaper) {
	t.Helper()
	select {
	case <-s.entered:
	case <-time.After(time.Second):
		t.Fatal("controlled shaper did not admit the Send")
	}
}

func TestConcurrentSendGenerationTransitionMatrix(t *testing.T) {
	t.Run("Remove and same-name re-add retire selected generation", func(t *testing.T) {
		clk := newFakeClock()
		m, _, base := newProbingMultipath(t, loopbackPaths(2), testKey(t, 0xC1), clk)
		cfg := lifecycleShaperConfig()
		m.shaperConfigs = []config.PathShaperConfig{cfg, cfg}
		selector := &faultDynamicScheduler{
			DynamicScheduler: base.(sched.DynamicScheduler),
			members:          2,
			forcePick:        0,
		}
		m.scheduler = selector
		var generations []*controlledShaper
		m.newPathShaper = func(_ shaper.Config, write shaper.WriteFunc) (pathShaper, error) {
			s := newControlledShaper(write, len(generations) == 0)
			generations = append(generations, s)
			return s, nil
		}
		if _, _, err := m.Open(0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			for _, generation := range generations {
				generation.releaseWrite()
			}
			_ = m.Close()
		})
		remote, remoteAddr := rawPeer(t)
		for _, path := range m.paths {
			path.setRemote(remoteAddr)
		}

		sendDone := make(chan error, 1)
		go func() {
			sendDone <- m.Send([][]byte{wgMsg(wgMessageTransportType, 0, 64)}, m.virt)
		}()
		waitControlledShaperEntered(t, generations[0])

		transitionDone := make(chan error, 1)
		go func() {
			if err := m.RemovePath("a"); err != nil {
				transitionDone <- err
				return
			}
			transitionDone <- m.AddPathWithShaper(config.Path{
				Name:       "a",
				SourceAddr: netip.MustParseAddr("127.0.0.1"),
			}, cfg)
		}()
		waitControlledShaperStopped(t, generations[0])
		generations[0].releaseWrite()
		if err := <-sendDone; !errors.Is(err, shaper.ErrClosed) {
			t.Fatalf("Send across RemovePath = %v, want shaper.ErrClosed", err)
		}
		if err := <-transitionDone; err != nil {
			t.Fatal(err)
		}
		if len(generations) != 3 {
			t.Fatalf("shaper generations = %d, want two boot + one re-add", len(generations))
		}
		replacement := peerPathByName(m.peerState, "a")
		if replacement == nil || replacement.shaper != generations[2] {
			t.Fatal("same-name re-add did not install a distinct replacement shaper")
		}
		replacement.setRemote(remoteAddr)
		selector.force(1)
		if err := m.Send([][]byte{wgMsg(wgMessageTransportType, 0, 64)}, m.virt); err != nil {
			t.Fatalf("replacement Send: %v", err)
		}
		if generations[0].writes.Load() != 0 || generations[2].writes.Load() != 1 {
			t.Fatalf("generation emissions = old:%d replacement:%d, want 0/1", generations[0].writes.Load(), generations[2].writes.Load())
		}
		buf := make([]byte, maxDatagram)
		if err := remote.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		if _, err := remote.Read(buf); err != nil {
			t.Fatalf("replacement wire missing: %v", err)
		}
	})

	t.Run("Close and Open reconcile replaces generation", func(t *testing.T) {
		clk := newFakeClock()
		m, _, base := newProbingMultipath(t, loopbackPaths(1), testKey(t, 0xC2), clk)
		cfg := lifecycleShaperConfig()
		m.shaperConfigs = []config.PathShaperConfig{cfg}
		selector := &faultDynamicScheduler{
			DynamicScheduler: base.(sched.DynamicScheduler),
			members:          1,
			forcePick:        0,
		}
		m.scheduler = selector
		var generations []*controlledShaper
		m.newPathShaper = func(_ shaper.Config, write shaper.WriteFunc) (pathShaper, error) {
			s := newControlledShaper(write, len(generations) == 0)
			generations = append(generations, s)
			return s, nil
		}
		if _, _, err := m.Open(0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			for _, generation := range generations {
				generation.releaseWrite()
			}
			_ = m.Close()
		})
		remote, remoteAddr := rawPeer(t)
		m.paths[0].setRemote(remoteAddr)

		sendDone := make(chan error, 1)
		go func() {
			sendDone <- m.Send([][]byte{wgMsg(wgMessageTransportType, 0, 64)}, m.virt)
		}()
		waitControlledShaperEntered(t, generations[0])
		transitionDone := make(chan error, 1)
		go func() {
			if err := m.Close(); err != nil {
				transitionDone <- err
				return
			}
			_, _, err := m.Open(0)
			transitionDone <- err
		}()
		waitControlledShaperStopped(t, generations[0])
		generations[0].releaseWrite()
		if err := <-sendDone; !errors.Is(err, shaper.ErrClosed) {
			t.Fatalf("Send across Close/Open = %v, want shaper.ErrClosed", err)
		}
		if err := <-transitionDone; err != nil {
			t.Fatal(err)
		}
		if len(generations) != 2 || m.paths[0].shaper != generations[1] {
			t.Fatal("Close/Open did not install one distinct replacement shaper")
		}
		if got := selector.setPathsCalls(); got != 2 {
			t.Fatalf("SetPaths calls across Open/Close/Open = %d, want 2", got)
		}
		m.paths[0].setRemote(remoteAddr)
		if err := m.Send([][]byte{wgMsg(wgMessageTransportType, 0, 64)}, m.virt); err != nil {
			t.Fatalf("replacement Send: %v", err)
		}
		if generations[0].writes.Load() != 0 || generations[1].writes.Load() != 1 {
			t.Fatalf("generation emissions = old:%d replacement:%d, want 0/1", generations[0].writes.Load(), generations[1].writes.Load())
		}
		buf := make([]byte, maxDatagram)
		if err := remote.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		if _, err := remote.Read(buf); err != nil {
			t.Fatalf("replacement wire missing: %v", err)
		}
	})

	t.Run("deferred promotion retains selected generation", func(t *testing.T) {
		clk := newFakeClock()
		m, _, base := newProbingMultipath(t, loopbackPaths(1), testKey(t, 0xC3), clk)
		cfg := lifecycleShaperConfig()
		m.shaperConfigs = []config.PathShaperConfig{cfg}
		selector := &faultDynamicScheduler{
			DynamicScheduler: base.(sched.DynamicScheduler),
			members:          1,
			forcePick:        0,
		}
		m.scheduler = selector
		var generations []*controlledShaper
		m.newPathShaper = func(_ shaper.Config, write shaper.WriteFunc) (pathShaper, error) {
			s := newControlledShaper(write, len(generations) == 0)
			generations = append(generations, s)
			return s, nil
		}
		if _, _, err := m.Open(0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			for _, generation := range generations {
				generation.releaseWrite()
			}
			_ = m.Close()
		})
		_, remoteAddr := rawPeer(t)
		m.paths[0].setRemote(remoteAddr)
		m.addPathListen = func(netip.Addr, uint16, string) (*net.UDPConn, error, error) {
			return nil, nil, syscall.EADDRNOTAVAIL
		}
		if err := m.AddPathWithShaper(config.Path{
			Name:       "deferred-concurrent",
			SourceAddr: netip.MustParseAddr("127.0.0.2"),
		}, cfg); err != nil {
			t.Fatal(err)
		}
		m.deferredListen = func(src netip.Addr, port uint16, dev string) (*net.UDPConn, error, error) {
			return listenPath(src, port, dev)
		}

		sendDone := make(chan error, 1)
		go func() {
			sendDone <- m.Send([][]byte{wgMsg(wgMessageTransportType, 0, 64)}, m.virt)
		}()
		waitControlledShaperEntered(t, generations[0])
		promotionDone := make(chan struct{})
		go func() {
			m.reconcileDeferred()
			close(promotionDone)
		}()
		select {
		case <-promotionDone:
		case <-time.After(time.Second):
			t.Fatal("deferred promotion blocked behind admitted shaped Send")
		}
		if len(generations) != 2 || len(m.deferred) != 0 || len(m.paths) != 2 {
			t.Fatalf("promotion result = shapers:%d deferred:%d paths:%d, want 2/0/2", len(generations), len(m.deferred), len(m.paths))
		}
		if generations[0].isStopped() {
			t.Fatal("deferred promotion retired the retained socket generation")
		}
		generations[0].releaseWrite()
		if err := <-sendDone; err != nil {
			t.Fatalf("Send across retained-path promotion = %v, want nil", err)
		}
		if generations[0].writes.Load() != 1 || generations[1].writes.Load() != 0 {
			t.Fatalf("promotion emissions = retained:%d promoted:%d, want 1/0", generations[0].writes.Load(), generations[1].writes.Load())
		}
	})

	t.Run("peer teardown and rebind retain socket generation", func(t *testing.T) {
		lg, err := log.New("error", io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		primaryScheduler := &unpacedSelectionRecorder{}
		m, err := NewMultipathWithShapers(
			loopbackPaths(1),
			testKey(t, 0xC4),
			primaryScheduler,
			nil,
			nil,
			nil,
			nil,
			config.Amnezia{},
			[]config.PathShaperConfig{lifecycleShaperConfig()},
			lg,
		)
		if err != nil {
			t.Fatal(err)
		}
		betaScheduler := &unpacedSelectionRecorder{}
		if err := m.AddConcentratorPeer("beta", testKey(t, 0xC5), betaScheduler, nil, nil); err != nil {
			t.Fatal(err)
		}
		var generations []*controlledShaper
		m.newPathShaper = func(_ shaper.Config, write shaper.WriteFunc) (pathShaper, error) {
			s := newControlledShaper(write, len(generations) == 1)
			generations = append(generations, s)
			return s, nil
		}
		if _, _, err := m.Open(0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			for _, generation := range generations {
				generation.releaseWrite()
			}
			_ = m.Close()
		})
		beta := m.peersByName["beta"]
		_, remoteAddr := rawPeer(t)
		beta.paths[0].setRemote(remoteAddr)

		sendDone := make(chan error, 1)
		go func() {
			sendDone <- m.Send([][]byte{wgMsg(wgMessageTransportType, 0, 64)}, beta.virt)
		}()
		waitControlledShaperEntered(t, generations[1])
		rebindDone := make(chan bool, 1)
		go func() {
			tornDown := m.TearDownPeer("beta")
			m.ensurePeerReceiveInstantiated(beta)
			rebindDone <- tornDown
		}()
		select {
		case tornDown := <-rebindDone:
			if !tornDown {
				t.Fatal("down beta peer was not torn down")
			}
		case <-time.After(time.Second):
			t.Fatal("peer teardown/rebind blocked behind admitted shaped Send")
		}
		if beta.paths[0].shaper != generations[1] || generations[1].isStopped() {
			t.Fatal("peer teardown/rebind replaced or stopped the live socket shaper")
		}
		generations[1].releaseWrite()
		if err := <-sendDone; err != nil {
			t.Fatalf("Send across peer teardown/rebind = %v, want nil", err)
		}
		if generations[1].writes.Load() != 1 {
			t.Fatalf("retained beta generation emissions = %d, want 1", generations[1].writes.Load())
		}
	})
}

func TestShaperGenerationRollbackStages(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	t.Run("later peer fanout failure", func(t *testing.T) {
		clk := newFakeClock()
		paths := loopbackPaths(1)
		pskA := testKey(t, 0xF3)
		pskB := testKey(t, 0xF4)
		m, _, primaryBase := newProbingMultipath(t, paths, pskA, clk)
		primaryPeer := m.peerState
		cfg := lifecycleShaperConfig()
		m.shaperConfigs = []config.PathShaperConfig{cfg}
		primary := &faultDynamicScheduler{
			DynamicScheduler: primaryBase.(sched.DynamicScheduler),
			members:          1,
		}
		primaryPeer.scheduler = primary
		betaBase, betaProbers, betaFactory := concPeerWiring(t, paths, pskB, 0xF4, clk)
		beta := &faultDynamicScheduler{
			DynamicScheduler: betaBase.(sched.DynamicScheduler),
			members:          1,
		}
		if err := m.AddConcentratorPeer("beta", pskB, beta, betaProbers, betaFactory); err != nil {
			t.Fatal(err)
		}
		var created []*trackedStagedShaper
		m.newPathShaper = func(shaper.Config, shaper.WriteFunc) (pathShaper, error) {
			s := &trackedStagedShaper{}
			created = append(created, s)
			return s, nil
		}
		if _, _, err := m.Open(0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = m.Close() })
		if len(created) != 2 {
			t.Fatalf("boot shapers = %d, want 2", len(created))
		}

		injected := errors.New("injected later-peer scheduler add")
		beta.mu.Lock()
		beta.addErr = injected
		beta.mu.Unlock()
		var rejectedConn *net.UDPConn
		m.addPathListen = func(src netip.Addr, port uint16, dev string) (*net.UDPConn, error, error) {
			conn, deviceErr, err := listenPath(src, port, dev)
			rejectedConn = conn
			return conn, deviceErr, err
		}
		beforeDefs := len(m.defs)
		beforeShared := len(m.shared)
		beforePrimaryProbers := len(primaryPeer.probers)
		beforeBetaProbers := len(m.peersByName["beta"].probers)
		err := m.AddPathWithShaper(config.Path{
			Name:       "rejected",
			SourceAddr: netip.MustParseAddr("127.0.0.1"),
		}, cfg)
		if !errors.Is(err, injected) {
			t.Fatalf("AddPath error = %v, want injected identity", err)
		}
		if rejectedConn == nil {
			t.Fatal("runtime AddPath did not expose a fresh socket to the rejected fanout")
		}
		if _, err := rejectedConn.WriteToUDPAddrPort([]byte{1}, netip.MustParseAddrPort("127.0.0.1:9")); !errors.Is(err, net.ErrClosed) {
			t.Fatalf("rejected runtime-add socket write = %v, want net.ErrClosed", err)
		}
		if len(created) != 4 {
			t.Fatalf("shapers after rejected fanout = %d, want 4", len(created))
		}
		assertTrackedShapersRetired(t, created[2:])
		if primary.memberCount() != 1 || beta.memberCount() != 1 {
			t.Fatalf("scheduler membership after rollback = primary:%d beta:%d, want 1/1", primary.memberCount(), beta.memberCount())
		}
		if len(m.defs) != beforeDefs ||
			len(m.shared) != beforeShared ||
			len(primaryPeer.probers) != beforePrimaryProbers ||
			len(m.peersByName["beta"].probers) != beforeBetaProbers ||
			len(primaryPeer.paths) != 1 ||
			len(m.peersByName["beta"].paths) != 1 {
			t.Fatal("later-peer failure changed live or durable membership")
		}
	})

	t.Run("deferred promotion later-peer failure", func(t *testing.T) {
		clk := newFakeClock()
		paths := loopbackPaths(1)
		pskA := testKey(t, 0xF5)
		pskB := testKey(t, 0xF6)
		m, _, primaryBase := newProbingMultipath(t, paths, pskA, clk)
		primaryPeer := m.peerState
		cfg := lifecycleShaperConfig()
		m.shaperConfigs = []config.PathShaperConfig{cfg}
		primary := &faultDynamicScheduler{
			DynamicScheduler: primaryBase.(sched.DynamicScheduler),
			members:          1,
		}
		primaryPeer.scheduler = primary
		betaBase, betaProbers, betaFactory := concPeerWiring(t, paths, pskB, 0xF6, clk)
		beta := &faultDynamicScheduler{
			DynamicScheduler: betaBase.(sched.DynamicScheduler),
			members:          1,
		}
		if err := m.AddConcentratorPeer("beta", pskB, beta, betaProbers, betaFactory); err != nil {
			t.Fatal(err)
		}
		var created []*trackedStagedShaper
		m.newPathShaper = func(shaper.Config, shaper.WriteFunc) (pathShaper, error) {
			s := &trackedStagedShaper{}
			created = append(created, s)
			return s, nil
		}
		if _, _, err := m.Open(0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = m.Close() })
		m.addPathListen = func(netip.Addr, uint16, string) (*net.UDPConn, error, error) {
			return nil, nil, syscall.EADDRNOTAVAIL
		}
		deferredDef := config.Path{
			Name:       "deferred-rejected",
			SourceAddr: netip.MustParseAddr("127.0.0.2"),
		}
		if err := m.AddPathWithShaper(deferredDef, cfg); err != nil {
			t.Fatal(err)
		}
		if len(m.deferred) != 1 || len(created) != 2 {
			t.Fatalf("deferred baseline = deferred:%d shapers:%d, want 1/2", len(m.deferred), len(created))
		}

		injected := errors.New("injected deferred later-peer scheduler add")
		beta.mu.Lock()
		beta.addErr = injected
		beta.mu.Unlock()
		conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
		if err != nil {
			t.Fatal(err)
		}
		var retirement socketGenerationRetirement
		m.mu.Lock()
		err = m.promoteDeferredLocked(m.deferred[0], conn, &retirement)
		m.mu.Unlock()
		if !errors.Is(err, injected) {
			t.Fatalf("promoteDeferredLocked error = %v, want injected identity", err)
		}
		if err := retirement.retire(); err != nil {
			t.Fatal(err)
		}
		if len(created) != 4 {
			t.Fatalf("shapers after rejected promotion = %d, want 4", len(created))
		}
		assertTrackedShapersRetired(t, created[2:])
		if primary.memberCount() != 1 || beta.memberCount() != 1 {
			t.Fatalf("scheduler membership after promotion rollback = primary:%d beta:%d, want 1/1", primary.memberCount(), beta.memberCount())
		}
		if len(m.deferred) != 1 ||
			len(m.shared) != 1 ||
			len(primaryPeer.paths) != 1 ||
			len(m.peersByName["beta"].paths) != 1 ||
			len(m.defs) != 2 ||
			len(primaryPeer.probers) != 2 ||
			len(m.peersByName["beta"].probers) != 2 {
			t.Fatal("failed promotion changed deferred, durable, or live membership")
		}
		if _, err := conn.WriteToUDPAddrPort([]byte{1}, netip.MustParseAddrPort("127.0.0.1:9")); !errors.Is(err, net.ErrClosed) {
			t.Fatalf("failed-promotion socket write = %v, want net.ErrClosed", err)
		}
	})

	t.Run("Open SetPaths failure", func(t *testing.T) {
		defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
		m, _, base := newProbingMultipath(t, loopbackPaths(2), testKey(t, 0xF7), newFakeClock())
		cfg := lifecycleShaperConfig()
		m.shaperConfigs = []config.PathShaperConfig{cfg, cfg}
		m.fecCfg = &fec.Config{DataShards: 4, ParityShards: 1, Deadline: testFECDeadline}
		injected := errors.New("injected Open SetPaths")
		fault := &faultDynamicScheduler{
			DynamicScheduler: base.(sched.DynamicScheduler),
			members:          2,
			setErr:           injected,
		}
		m.scheduler = fault
		var created []*trackedStagedShaper
		m.newPathShaper = func(shaper.Config, shaper.WriteFunc) (pathShaper, error) {
			s := &trackedStagedShaper{}
			created = append(created, s)
			return s, nil
		}
		if _, _, err := m.Open(0); !errors.Is(err, injected) {
			t.Fatalf("Open error = %v, want injected SetPaths identity", err)
		}
		if len(created) != 2 {
			t.Fatalf("Open shapers = %d, want 2", len(created))
		}
		assertTrackedShapersRetired(t, created)
		if len(m.paths) != 0 || len(m.shared) != 0 || m.deliverSignal != nil ||
			m.recoveryAuthoritySignal != nil || m.recvClosed != nil {
			t.Fatal("failed Open retained per-generation bind state")
		}
		if m.fecSend.Load() != nil {
			t.Fatal("failed Open retained the FEC sender owner")
		}
		if fault.memberCount() != 2 {
			t.Fatalf("scheduler members after refused SetPaths = %d, want original 2", fault.memberCount())
		}
		if err := m.Close(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("Open construction failure closes socket before writer join", func(t *testing.T) {
		m, _, _ := newProbingMultipath(t, loopbackPaths(2), testKey(t, 0xF8), newFakeClock())
		cfg := lifecycleShaperConfig()
		m.shaperConfigs = []config.PathShaperConfig{cfg, cfg}
		_, remoteAddr := rawPeer(t)
		injected := errors.New("injected second shaper construction")
		writerEntered := make(chan struct{})
		writerDone := make(chan error, 1)
		var firstShared *sharedPathState
		var firstConn *net.UDPConn
		var created []*trackedStagedShaper
		factoryCalls := 0
		m.newPathShaper = func(shaper.Config, shaper.WriteFunc) (pathShaper, error) {
			factoryCalls++
			if factoryCalls == 2 {
				firstShared = m.shared[0]
				firstConn = firstShared.conn
				firstShared.writeUDP = func([]byte, netip.AddrPort) (int, error) {
					close(writerEntered)
					buffer := make([]byte, 1)
					_, _, err := firstConn.ReadFromUDPAddrPort(buffer)
					return 0, err
				}
				go func() {
					_, err := firstShared.writeToUDPAddrPort([]byte{1}, remoteAddr)
					writerDone <- err
				}()
				<-writerEntered
				return nil, injected
			}
			s := &trackedStagedShaper{}
			created = append(created, s)
			return s, nil
		}

		openDone := make(chan error, 1)
		go func() {
			_, _, err := m.Open(0)
			openDone <- err
		}()
		select {
		case <-writerEntered:
		case <-time.After(time.Second):
			t.Fatal("Open did not reach injected construction failure")
		}
		waitDirectWriteAdmissionClosed(t, firstShared)
		assertBindLockAvailable(t, m)
		if _, err := firstShared.writeToUDPAddrPort([]byte{2}, remoteAddr); !errors.Is(err, net.ErrClosed) {
			t.Fatalf("Open-unwind post-detach write = %v, want net.ErrClosed", err)
		}
		if _, err := firstConn.WriteToUDPAddrPort([]byte{3}, remoteAddr); !errors.Is(err, net.ErrClosed) {
			t.Fatalf("Open-unwind socket remained open while joining writer: %v", err)
		}
		select {
		case err := <-openDone:
			if !errors.Is(err, injected) {
				t.Fatalf("Open error = %v, want construction error identity", err)
			}
		case <-time.After(time.Second):
			t.Fatal("Open unwind did not join socket-interrupted writer")
		}
		if err := <-writerDone; !errors.Is(err, net.ErrClosed) {
			t.Fatalf("Open-unwind interrupted writer = %v, want net.ErrClosed", err)
		}
		if len(created) != 1 {
			t.Fatalf("constructed shapers = %d, want 1 before failure", len(created))
		}
		assertTrackedShapersRetired(t, created)
		if _, err := firstConn.WriteToUDPAddrPort([]byte{4}, remoteAddr); !errors.Is(err, net.ErrClosed) {
			t.Fatalf("Open-unwind retired socket write = %v, want net.ErrClosed", err)
		}
		if len(m.paths) != 0 || len(m.shared) != 0 || m.deliverSignal != nil ||
			m.recoveryAuthoritySignal != nil || m.recvClosed != nil {
			t.Fatal("construction-failed Open retained per-generation state")
		}
		if err := m.Close(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestCloseDetachesGenerationBeforeWaitingForShaper(t *testing.T) {
	m, _, _ := newProbingMultipath(t, loopbackPaths(1), testKey(t, 0xE1), newFakeClock())
	m.shaperConfigs = []config.PathShaperConfig{lifecycleShaperConfig()}
	blocker := &closeBlockingShaper{
		closeStarted: make(chan struct{}),
		releaseClose: make(chan struct{}),
	}
	m.newPathShaper = func(shaper.Config, shaper.WriteFunc) (pathShaper, error) {
		return blocker, nil
	}
	if _, _, err := m.Open(0); err != nil {
		t.Fatal(err)
	}

	result := make(chan error, 1)
	go func() {
		result <- m.Close()
	}()
	assertBindLockReleasedDuringRetirement(t, m, blocker.closeStarted, blocker.releaseClose)
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not finish after shaper retirement completed")
	}
}

func TestRemovePathDetachesGenerationBeforeWaitingForShaper(t *testing.T) {
	m, _, _ := newProbingMultipath(t, loopbackPaths(2), testKey(t, 0xE2), newFakeClock())
	cfg := lifecycleShaperConfig()
	m.shaperConfigs = []config.PathShaperConfig{cfg, cfg}
	blocker := &closeBlockingShaper{
		closeStarted: make(chan struct{}),
		releaseClose: make(chan struct{}),
	}
	m.newPathShaper = func(shaper.Config, shaper.WriteFunc) (pathShaper, error) {
		if len(m.paths) == 0 {
			return blocker, nil
		}
		return &closeBlockingShaper{
			closeStarted: make(chan struct{}),
			releaseClose: make(chan struct{}),
		}, nil
	}
	if _, _, err := m.Open(0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, pp := range m.paths {
			if remaining, ok := pp.shaper.(*closeBlockingShaper); ok {
				remaining.closeOnce.Do(func() {
					close(remaining.closeStarted)
					close(remaining.releaseClose)
				})
			}
		}
		_ = m.Close()
	})

	result := make(chan error, 1)
	go func() {
		result <- m.RemovePath("a")
	}()
	assertBindLockReleasedDuringRetirement(t, m, blocker.closeStarted, blocker.releaseClose)
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("RemovePath did not finish after shaper retirement completed")
	}
}

func TestRetirementCallbackCanAcquireBindAndSchedulerLocks(t *testing.T) {
	m, _, _ := newProbingMultipath(t, loopbackPaths(1), testKey(t, 0xE9), newFakeClock())
	m.shaperConfigs = []config.PathShaperConfig{lifecycleShaperConfig()}
	lockAcquired := make(chan struct{})
	m.newPathShaper = func(shaper.Config, shaper.WriteFunc) (pathShaper, error) {
		return &callbackClosingShaper{onClose: func() {
			m.mu.Lock()
			close(lockAcquired)
			m.mu.Unlock()
			m.scheduler.Recompute()
		}}, nil
	}
	if _, _, err := m.Open(0); err != nil {
		t.Fatal(err)
	}

	result := make(chan error, 1)
	go func() {
		result <- m.Close()
	}()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("retirement callback blocked acquiring bind or scheduler state")
	}
	select {
	case <-lockAcquired:
	default:
		t.Fatal("retirement callback did not acquire m.mu")
	}
}

func TestPendingPMTUTeardownErrorIdentity(t *testing.T) {
	for _, test := range []struct {
		name     string
		paths    []config.Path
		teardown func(*Multipath) error
	}{
		{
			name:  "close",
			paths: loopbackPaths(1),
			teardown: func(m *Multipath) error {
				return m.Close()
			},
		},
		{
			name:  "remove",
			paths: loopbackPaths(2),
			teardown: func(m *Multipath) error {
				return m.RemovePath("a")
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			m, _, _ := newProbingMultipath(t, test.paths, testKey(t, 0xE3), newFakeClock())
			if _, _, err := m.Open(0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = m.Close() })
			result := startPMTUProbe(m.PMTUProbe("a"), 1500)
			waitPendingPMTUProbe(t, m.paths[0])

			if err := test.teardown(m); err != nil {
				t.Fatal(err)
			}
			select {
			case got := <-result:
				if !errors.Is(got.err, net.ErrClosed) || got.echoed {
					t.Fatalf("pending ProbePMTU after teardown = (%v, %v), want (false, net.ErrClosed)", got.echoed, got.err)
				}
			case <-time.After(time.Second):
				t.Fatal("pending PMTU probe remained blocked after path teardown")
			}
		})
	}
}

func TestCloseRetiresQueuedWireAndReopenUsesFreshShaperGeneration(t *testing.T) {
	lg, err := log.New("error", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	cfg := lifecycleShaperConfig()
	cfg.RateBytesPerSecond = 64
	cfg.DataBurstBytes = 2 * cfg.MaxEncodedDatagramBytes
	m, err := NewMultipathWithShapers(
		loopbackPaths(1),
		testKey(t, 0xE4),
		&unpacedSelectionRecorder{},
		nil,
		nil,
		&fec.Config{DataShards: 1, ParityShards: 1, Deadline: testFECDeadline},
		nil,
		config.Amnezia{},
		[]config.PathShaperConfig{cfg},
		lg,
	)
	if err != nil {
		t.Fatal(err)
	}

	firstWriterEntered := make(chan struct{})
	releaseFirstWriter := make(chan struct{})
	secondWriterEntered := make(chan struct{})
	var generations []*shaper.Shaper
	var generationIndex atomic.Int32
	var firstWrites atomic.Int32
	var secondWrites atomic.Int32
	m.newPathShaper = func(got shaper.Config, write shaper.WriteFunc) (pathShaper, error) {
		generationIndex.Add(1)
		s, err := shaper.New(got, shaper.SystemClock{}, write)
		if err == nil {
			generations = append(generations, s)
		}
		return s, err
	}

	if _, _, err := m.Open(0); err != nil {
		t.Fatal(err)
	}
	remote, remoteAddr := rawPeer(t)
	defer remote.Close()
	m.paths[0].setRemote(remoteAddr)
	oldConn := m.paths[0].conn
	m.paths[0].writeUDP = func(payload []byte, remote netip.AddrPort) (int, error) {
		if firstWrites.Add(1) == 1 {
			close(firstWriterEntered)
			<-releaseFirstWriter
		}
		return oldConn.WriteToUDPAddrPort(payload, remote)
	}

	sendResult := make(chan error, 1)
	go func() {
		sendResult <- m.Send([][]byte{wgMsg(wgMessageTransportType, 0, 64)}, m.virt)
	}()
	select {
	case <-firstWriterEntered:
	case <-time.After(time.Second):
		t.Fatal("first generation writer did not enter")
	}

	closeResult := make(chan error, 1)
	go func() {
		closeResult <- m.Close()
	}()
	stopDeadline := time.Now().Add(time.Second)
	for {
		err := generations[0].AccountPriority(1)
		if errors.Is(err, shaper.ErrClosed) {
			break
		}
		if err != nil {
			close(releaseFirstWriter)
			t.Fatalf("old generation stop error = %v, want shaper.ErrClosed", err)
		}
		if time.Now().After(stopDeadline) {
			close(releaseFirstWriter)
			t.Fatal("old generation did not stop admission during Close")
		}
		time.Sleep(time.Millisecond)
	}
	close(releaseFirstWriter)

	select {
	case err := <-sendResult:
		if !errors.Is(err, shaper.ErrClosed) {
			t.Fatalf("queued shaped Send error = %v, want shaper.ErrClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("queued shaped Send did not unblock")
	}
	select {
	case err := <-closeResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not finish")
	}
	if _, err := oldConn.WriteToUDPAddrPort([]byte{1}, remoteAddr); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("old socket write after Close = %v, want net.ErrClosed", err)
	}

	buf := make([]byte, maxDatagram)
	if err := remote.SetReadDeadline(time.Now().Add(30 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if _, err := remote.Read(buf); err == nil {
		t.Fatal("socket-interrupted old-generation wire crossed the generation boundary")
	} else if !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("retired queue read = %v, want timeout", err)
	}

	if _, _, err := m.Open(0); err != nil {
		t.Fatal(err)
	}
	m.paths[0].setRemote(remoteAddr)
	replacementConn := m.paths[0].conn
	m.paths[0].writeUDP = func(payload []byte, remote netip.AddrPort) (int, error) {
		if secondWrites.Add(1) == 1 {
			close(secondWriterEntered)
		}
		return replacementConn.WriteToUDPAddrPort(payload, remote)
	}
	if len(generations) != 2 {
		t.Fatalf("shaper generation count = %d, want 2", len(generations))
	}
	if generations[0] == generations[1] {
		t.Fatalf("shaper generations = %p/%p, want two distinct instances", generations[0], generations[1])
	}
	if replacementConn == oldConn {
		t.Fatal("Close/Open reused the old UDP socket generation")
	}
	reopened := m.PeerSnapshots()[0].Paths[0].Shaper
	if reopened == nil {
		t.Fatal("reopened paced path omitted its shaper snapshot")
	}
	if reopened.AcceptedBytes != 0 ||
		reopened.EmittedBytes != 0 ||
		reopened.AdmissionWaits != 0 ||
		reopened.AsyncWriteErrors != 0 ||
		reopened.AsyncWriteEMSGSIZEErrors != 0 {
		t.Fatalf("replacement shaper inherited old generation counters: %+v", reopened)
	}

	replacementSend := make(chan error, 1)
	go func() {
		replacementSend <- m.Send([][]byte{wgMsg(wgMessageTransportType, 0, 64)}, m.virt)
	}()
	select {
	case <-secondWriterEntered:
		// The first replacement datagram has deadline Now. A retained old tail
		// at 64 B/s would keep it parked for seconds.
	case <-time.After(100 * time.Millisecond):
		t.Fatal("replacement shaper inherited old virtual time or queue state")
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-replacementSend:
		if !errors.Is(err, shaper.ErrClosed) {
			t.Fatalf("replacement queued Send error = %v, want shaper.ErrClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("replacement queued Send did not retire")
	}
	if got := firstWrites.Load(); got != 1 {
		t.Fatalf("old generation writer calls = %d, want only the in-flight DATA wire", got)
	}
	if got := secondWrites.Load(); got != 1 {
		t.Fatalf("replacement generation writer calls = %d, want only its first DATA wire", got)
	}
}

func TestRepeatedShaperGenerationTransitionsDoNotLeak(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	lg, err := log.New("error", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	cfg := lifecycleShaperConfig()
	m, err := NewMultipathWithShapers(
		loopbackPaths(1),
		testKey(t, 0xE5),
		&unpacedSelectionRecorder{},
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
	for i := 0; i < 25; i++ {
		if _, _, err := m.Open(0); err != nil {
			t.Fatalf("Open iteration %d: %v", i, err)
		}
		if err := m.Close(); err != nil {
			t.Fatalf("Close iteration %d: %v", i, err)
		}
	}
}

func TestRuntimeAndPeerLifecycleOwnExpectedShaperGeneration(t *testing.T) {
	pskA := testKey(t, 0xE6)
	pskB := testKey(t, 0xE7)
	clk := newFakeClock()
	paths := loopbackPaths(1)
	m, _, _ := newProbingMultipath(t, paths, pskA, clk)
	cfg := lifecycleShaperConfig()
	m.shaperConfigs = []config.PathShaperConfig{cfg}

	betaSched, betaProbers, betaFactory := concPeerWiring(t, paths, pskB, 0xE8, clk)
	if err := m.AddConcentratorPeer("beta", pskB, betaSched, betaProbers, betaFactory); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.Open(0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })

	beta := m.peersByName["beta"]
	retained := beta.paths[0].shaper
	if retained == nil {
		t.Fatal("beta boot socket generation has no shaper")
	}
	if !m.TearDownPeer("beta") {
		t.Fatal("down beta peer was not torn down")
	}
	m.ensurePeerReceiveInstantiated(beta)
	if beta.paths[0].shaper != retained {
		t.Fatal("peer heavy-state teardown/rebind replaced the live socket's shaper generation")
	}
	if err := retained.AccountPriority(1); err != nil {
		t.Fatalf("retained live generation rejected accounting after peer rebind: %v", err)
	}

	if err := m.AddPathWithShaper(config.Path{
		Name:       "runtime",
		SourceAddr: netip.MustParseAddr("127.0.0.1"),
	}, cfg); err != nil {
		t.Fatal(err)
	}
	primaryRuntime := peerPathByName(m.peerState, "runtime")
	betaRuntime := peerPathByName(beta, "runtime")
	if primaryRuntime == nil || betaRuntime == nil || primaryRuntime.shaper == nil || betaRuntime.shaper == nil {
		t.Fatal("runtime fan-out did not create one shaper per peer/path")
	}
	if primaryRuntime.shaper == betaRuntime.shaper {
		t.Fatal("runtime peers shared one shaper generation")
	}
	removedPrimary := primaryRuntime.shaper
	removedBeta := betaRuntime.shaper
	if err := m.RemovePath("runtime"); err != nil {
		t.Fatal(err)
	}
	for peer, retired := range map[string]pathShaper{
		"primary": removedPrimary,
		"beta":    removedBeta,
	} {
		if err := retired.AccountPriority(1); !errors.Is(err, shaper.ErrClosed) {
			t.Fatalf("%s removed generation accounting = %v, want shaper.ErrClosed", peer, err)
		}
	}
	if err := m.AddPathWithShaper(config.Path{
		Name:       "runtime",
		SourceAddr: netip.MustParseAddr("127.0.0.1"),
	}, cfg); err != nil {
		t.Fatal(err)
	}
	if replacement := peerPathByName(m.peerState, "runtime").shaper; replacement == removedPrimary {
		t.Fatal("runtime re-add reused the removed shaper generation")
	}

	beforeReopen := make(map[*peerState]map[string]pathShaper, len(m.peers))
	for _, peer := range m.peers {
		beforeReopen[peer] = make(map[string]pathShaper, len(peer.paths))
		for _, pp := range peer.paths {
			beforeReopen[peer][pp.name] = pp.shaper
		}
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.Open(0); err != nil {
		t.Fatal(err)
	}
	for peer, oldByName := range beforeReopen {
		for name, old := range oldByName {
			replacement := peerPathByName(peer, name)
			if replacement == nil || replacement.shaper == nil {
				t.Fatalf("peer %q path %q missing shaper after SetPaths reopen", peer.name, name)
			}
			if replacement.shaper == old {
				t.Fatalf("peer %q path %q reused its pre-reopen shaper", peer.name, name)
			}
			if err := old.AccountPriority(1); !errors.Is(err, shaper.ErrClosed) {
				t.Fatalf("peer %q path %q old generation = %v, want shaper.ErrClosed", peer.name, name, err)
			}
		}
	}

	m.addPathListen = func(netip.Addr, uint16, string) (*net.UDPConn, error, error) {
		return nil, nil, syscall.EADDRNOTAVAIL
	}
	deferredDef := config.Path{
		Name:       "deferred",
		SourceAddr: netip.MustParseAddr("127.0.0.2"),
	}
	if err := m.AddPathWithShaper(deferredDef, cfg); err != nil {
		t.Fatal(err)
	}
	if peerPathByName(m.peerState, deferredDef.Name) != nil {
		t.Fatal("deferred admission created a shaper before a socket generation existed")
	}
	m.deferredListen = func(src netip.Addr, port uint16, dev string) (*net.UDPConn, error, error) {
		return listenPath(src, port, dev)
	}
	m.reconcileDeferred()
	promotedPrimary := peerPathByName(m.peerState, deferredDef.Name)
	promotedBeta := peerPathByName(beta, deferredDef.Name)
	if promotedPrimary == nil || promotedBeta == nil ||
		promotedPrimary.shaper == nil || promotedBeta.shaper == nil {
		t.Fatal("deferred promotion did not create one shaper for each live peer/socket view")
	}
	if promotedPrimary.shaper == promotedBeta.shaper {
		t.Fatal("deferred promotion shared one shaper across peers")
	}
	promotedShapers := []pathShaper{promotedPrimary.shaper, promotedBeta.shaper}
	if err := m.RemovePath(deferredDef.Name); err != nil {
		t.Fatal(err)
	}
	for i, retired := range promotedShapers {
		if err := retired.AccountPriority(1); !errors.Is(err, shaper.ErrClosed) {
			t.Fatalf("promoted shaper %d after removal = %v, want shaper.ErrClosed", i, err)
		}
	}
}
