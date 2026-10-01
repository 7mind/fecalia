package bind

import (
	"errors"
	"net"
	"net/netip"
	"syscall"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/config"
	"go.uber.org/goleak"
)

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

// blockWriterOnSocket makes the path's next write block until its socket is closed, as
// a write stuck in the kernel does, and returns a channel closed once it has entered.
func blockWriterOnSocket(pp *peerPathState) <-chan struct{} {
	conn := pp.conn
	entered := make(chan struct{})
	pp.writeUDP = func([]byte, netip.AddrPort) (int, error) {
		close(entered)
		buffer := make([]byte, 1)
		_, _, err := conn.ReadFromUDPAddrPort(buffer)
		return 0, err
	}
	return entered
}

// blockWriterOnRelease makes the path's next write block until release is closed, then
// succeed without touching the socket. It returns a channel closed once the write has
// entered.
func blockWriterOnRelease(pp *peerPathState, release <-chan struct{}) <-chan struct{} {
	entered := make(chan struct{})
	pp.writeUDP = func(payload []byte, _ netip.AddrPort) (int, error) {
		close(entered)
		<-release
		return len(payload), nil
	}
	return entered
}

func TestDirectWriteGenerationGateClosesSocketBeforeWriterJoin(t *testing.T) {
	for _, test := range []struct {
		name string
		run  func(*testing.T)
	}{
		{
			name: "transport write versus Close",
			run: func(t *testing.T) {
				m, err := newMultipath(t, loopbackPaths(1), testKey(t, 0xF0))
				if err != nil {
					t.Fatal(err)
				}
				if _, _, err := m.Open(0); err != nil {
					t.Fatal(err)
				}
				_, remoteAddr := rawPeer(t)
				pp := m.paths[0]
				oldConn := pp.conn
				writerEntered := blockWriterOnSocket(pp)
				newRemoteTransport(t, m.peerState, 987).join(pp, remoteAddr)

				if err := m.Send([][]byte{[]byte("in-flight")}, m.virt); err != nil {
					t.Fatal(err)
				}
				select {
				case <-writerEntered:
				case <-time.After(time.Second):
					t.Fatal("the transport's write did not enter the direct-write gate")
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
				if got := pp.socketWriteErrors.Load(); got != 1 {
					t.Fatalf("interrupted transport write counted %d socket errors, want 1", got)
				}
				if _, err := oldConn.WriteToUDPAddrPort([]byte{3}, remoteAddr); !errors.Is(err, net.ErrClosed) {
					t.Fatalf("retired socket write = %v, want net.ErrClosed", err)
				}
			},
		},
		{
			name: "generated PROBE versus RemovePath",
			run: func(t *testing.T) {
				m, _ := newProbingMultipath(t, loopbackPaths(2), testKey(t, 0xF1), newFakeClock())
				if _, _, err := m.Open(0); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = m.Close() })
				_, remoteAddr := rawPeer(t)
				for _, path := range m.paths {
					path.setRemote(remoteAddr)
				}
				pp := m.paths[0]
				oldConn := pp.conn
				writerEntered := blockWriterOnSocket(pp)

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
			},
		},
	} {
		t.Run(test.name, test.run)
	}
}

func TestTransitionMutexSerializesCloseAndReplacementOpen(t *testing.T) {
	m, _ := newProbingMultipath(t, loopbackPaths(1), testKey(t, 0xF9), newFakeClock())
	if _, _, err := m.Open(0); err != nil {
		t.Fatal(err)
	}
	_, remoteAddr := rawPeer(t)
	old := m.paths[0]
	oldConn := old.conn
	old.setRemote(remoteAddr)

	// A probe write that does not return holds the old generation's retirement open.
	releaseWriter := make(chan struct{})
	writerEntered := blockWriterOnRelease(old, releaseWriter)
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

	closeDone := make(chan error, 1)
	go func() { closeDone <- m.Close() }()
	waitDirectWriteAdmissionClosed(t, old.sharedPathState)

	openDone := make(chan error, 1)
	go func() {
		_, _, err := m.Open(0)
		openDone <- err
	}()
	select {
	case err := <-openDone:
		close(releaseWriter)
		t.Fatalf("replacement Open returned before old retirement release: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	m.mu.Lock()
	boundPaths, sharedSockets := len(m.paths), len(m.shared)
	m.mu.Unlock()
	if boundPaths != 0 || sharedSockets != 0 {
		close(releaseWriter)
		t.Fatalf("replacement published before old retirement: paths=%d shared=%d", boundPaths, sharedSockets)
	}
	select {
	case err := <-closeDone:
		close(releaseWriter)
		t.Fatalf("Close returned while an old-generation writer was still in flight: %v", err)
	default:
	}

	close(releaseWriter)
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
	if err := <-openDone; err != nil {
		t.Fatal(err)
	}
	<-probesDone
	if _, err := oldConn.WriteToUDPAddrPort([]byte{1}, remoteAddr); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("old socket write after the replacement Open = %v, want net.ErrClosed", err)
	}
	if len(m.paths) != 1 || m.paths[0] == old || m.paths[0].conn == oldConn {
		t.Fatal("replacement Open did not publish one distinct socket generation")
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentWriteGenerationTransitionMatrix(t *testing.T) {
	t.Run("deferred promotion retains a generation with a write in flight", func(t *testing.T) {
		m, _ := newProbingMultipath(t, loopbackPaths(1), testKey(t, 0xC3), newFakeClock())
		if _, _, err := m.Open(0); err != nil {
			t.Fatal(err)
		}
		releaseWriter := make(chan struct{})
		t.Cleanup(func() { _ = m.Close() })
		_, remoteAddr := rawPeer(t)
		retained := m.paths[0]
		retained.setRemote(remoteAddr)
		m.addPathListen = func(netip.Addr, uint16, string) (*net.UDPConn, error, error) {
			return nil, nil, syscall.EADDRNOTAVAIL
		}
		if err := m.AddPath(config.Path{
			Name:       "deferred-concurrent",
			SourceAddr: netip.MustParseAddr("127.0.0.2"),
		}); err != nil {
			t.Fatal(err)
		}
		m.deferredListen = func(src netip.Addr, port uint16, dev string) (*net.UDPConn, error, error) {
			return listenPath(src, port, dev)
		}

		writerEntered := blockWriterOnRelease(retained, releaseWriter)
		probesDone := make(chan struct{})
		go func() {
			m.emitProbes()
			close(probesDone)
		}()
		select {
		case <-writerEntered:
		case <-time.After(time.Second):
			close(releaseWriter)
			t.Fatal("generated PROBE did not enter the direct-write gate")
		}
		promotionDone := make(chan struct{})
		go func() {
			m.reconcileDeferred()
			close(promotionDone)
		}()
		select {
		case <-promotionDone:
		case <-time.After(time.Second):
			close(releaseWriter)
			t.Fatal("deferred promotion blocked behind a write in flight on another path")
		}
		m.mu.Lock()
		deferred, paths := len(m.deferred), len(m.paths)
		m.mu.Unlock()
		retained.writeMu.Lock()
		retired := retained.writesClosed
		retained.writeMu.Unlock()
		close(releaseWriter)
		<-probesDone
		if deferred != 0 || paths != 2 {
			t.Fatalf("promotion result = deferred:%d paths:%d, want 0/2", deferred, paths)
		}
		if retired {
			t.Fatal("deferred promotion retired the retained socket generation")
		}
		if got := retained.probeSendErrors.Load(); got != 0 {
			t.Fatalf("write across retained-path promotion counted %d errors, want 0", got)
		}
		if got := retained.txBytes.Load(); got == 0 {
			t.Fatal("write across retained-path promotion was not completed")
		}
	})

	t.Run("peer teardown and rebind retain socket generation", func(t *testing.T) {
		clock := newFakeClock()
		paths := loopbackPaths(1)
		m, _ := newProbingMultipath(t, paths, testKey(t, 0xC4), clock)
		betaProbers, betaFactory := concPeerWiring(t, paths, testKey(t, 0xC5), 0xC5, clock)
		if err := m.AddConcentratorPeer("beta", testKey(t, 0xC5), betaProbers, betaFactory); err != nil {
			t.Fatal(err)
		}
		if _, _, err := m.Open(0); err != nil {
			t.Fatal(err)
		}
		releaseWriter := make(chan struct{})
		t.Cleanup(func() { _ = m.Close() })
		beta := m.peersByName["beta"]
		_, remoteAddr := rawPeer(t)
		// Only beta's view has a remote, so the blocked probe write is beta's.
		beta.paths[0].setRemote(remoteAddr)

		writerEntered := blockWriterOnRelease(beta.paths[0], releaseWriter)
		probesDone := make(chan struct{})
		go func() {
			m.emitProbes()
			close(probesDone)
		}()
		select {
		case <-writerEntered:
		case <-time.After(time.Second):
			close(releaseWriter)
			t.Fatal("generated PROBE did not enter the direct-write gate")
		}
		rebindDone := make(chan bool, 1)
		go func() {
			tornDown := m.TearDownPeer("beta")
			m.ensurePeerReceiveInstantiated(beta)
			rebindDone <- tornDown
		}()
		var tornDown bool
		select {
		case tornDown = <-rebindDone:
		case <-time.After(time.Second):
			close(releaseWriter)
			t.Fatal("peer teardown/rebind blocked behind a write in flight on its socket")
		}
		beta.paths[0].writeMu.Lock()
		retired := beta.paths[0].writesClosed
		beta.paths[0].writeMu.Unlock()
		close(releaseWriter)
		<-probesDone
		if !tornDown {
			t.Fatal("down beta peer was not torn down")
		}
		if retired {
			t.Fatal("peer teardown/rebind stopped the live socket generation")
		}
		if got := beta.paths[0].probeSendErrors.Load(); got != 0 {
			t.Fatalf("write across peer teardown/rebind counted %d errors, want 0", got)
		}
		if got := beta.paths[0].txBytes.Load(); got == 0 {
			t.Fatal("write across peer teardown/rebind was not completed")
		}
	})
}

func TestGenerationRollbackStages(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	// twoPeers opens a concentrator whose second peer has no runtime prober factory, so a
	// fan-out that must mint that peer a prober fails after the primary was attached.
	twoPeers := func(t *testing.T, paths []config.Path) (*Multipath, *peerState) {
		t.Helper()
		clock := newFakeClock()
		m, _ := newProbingMultipath(t, paths, testKey(t, 0xF3), clock)
		betaProbers, _ := concPeerWiring(t, paths, testKey(t, 0xF4), 0xF4, clock)
		if err := m.AddConcentratorPeer("beta", testKey(t, 0xF4), betaProbers, nil); err != nil {
			t.Fatal(err)
		}
		return m, m.peersByName["beta"]
	}

	t.Run("later peer fanout failure", func(t *testing.T) {
		m, beta := twoPeers(t, loopbackPaths(1))
		if _, _, err := m.Open(0); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = m.Close() }()

		var rejectedConn *net.UDPConn
		m.addPathListen = func(src netip.Addr, port uint16, dev string) (*net.UDPConn, error, error) {
			conn, deviceErr, err := listenPath(src, port, dev)
			rejectedConn = conn
			return conn, deviceErr, err
		}
		beforeDefs := len(m.defs)
		beforeShared := len(m.shared)
		beforePrimaryProbers := len(m.probers)
		beforeBetaProbers := len(beta.probers)
		beforeID := m.nextPathID
		if err := m.AddPath(config.Path{
			Name:       "rejected",
			SourceAddr: netip.MustParseAddr("127.0.0.1"),
		}); err == nil {
			t.Fatal("AddPath succeeded although a bound peer could not be given a prober")
		}
		if rejectedConn == nil {
			t.Fatal("runtime AddPath did not expose a fresh socket to the rejected fanout")
		}
		if _, err := rejectedConn.WriteToUDPAddrPort([]byte{1}, netip.MustParseAddrPort("127.0.0.1:9")); !errors.Is(err, net.ErrClosed) {
			t.Fatalf("rejected runtime-add socket write = %v, want net.ErrClosed", err)
		}
		if len(m.defs) != beforeDefs ||
			len(m.shared) != beforeShared ||
			len(m.probers) != beforePrimaryProbers ||
			len(beta.probers) != beforeBetaProbers ||
			len(m.paths) != 1 ||
			len(beta.paths) != 1 ||
			m.nextPathID != beforeID {
			t.Fatal("later-peer failure changed live or durable membership")
		}
		if views := m.shared[0].views.Load(); views == nil || len(*views) != 2 {
			t.Fatal("later-peer failure disturbed the surviving socket's peer views")
		}
	})

	t.Run("deferred promotion later-peer failure", func(t *testing.T) {
		paths := []config.Path{
			{Name: "bindable", SourceAddr: netip.MustParseAddr("127.0.0.1")},
			{Name: "deferred", SourceAddr: netip.MustParseAddr(unassignableSource)},
		}
		m, beta := twoPeers(t, paths)
		if _, _, err := m.Open(0); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = m.Close() }()
		if len(m.deferred) != 1 {
			t.Fatalf("deferred baseline = %d, want 1", len(m.deferred))
		}

		// Beta's prober set falls short of the membership: the promotion must fail.
		beta.probers = beta.probers[:1]
		conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
		if err != nil {
			t.Fatal(err)
		}
		var retirement socketGenerationRetirement
		m.mu.Lock()
		err = m.promoteDeferredLocked(m.deferred[0], conn, "", &retirement)
		m.mu.Unlock()
		if err == nil {
			t.Fatal("promoteDeferredLocked succeeded although a bound peer has no prober for the path")
		}
		if err := retirement.retire(); err != nil {
			t.Fatal(err)
		}
		if len(m.deferred) != 1 ||
			len(m.shared) != 1 ||
			len(m.paths) != 1 ||
			len(beta.paths) != 1 ||
			len(m.defs) != 2 ||
			len(m.probers) != 2 {
			t.Fatal("failed promotion changed deferred, durable, or live membership")
		}
		if _, err := conn.WriteToUDPAddrPort([]byte{1}, netip.MustParseAddrPort("127.0.0.1:9")); !errors.Is(err, net.ErrClosed) {
			t.Fatalf("failed-promotion socket write = %v, want net.ErrClosed", err)
		}
	})
}

func TestRepeatedGenerationTransitionsDoNotLeak(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	m, err := newMultipath(t, loopbackPaths(1), testKey(t, 0xE5))
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
