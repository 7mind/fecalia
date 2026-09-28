package bind

import (
	"bytes"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/bond"
	"github.com/7mind/wanbond/internal/frame"
)

func TestAdaptiveFirstBulkPacketsMayArriveReversed(t *testing.T) {
	for _, delay := range []time.Duration{0, 180 * time.Millisecond} {
		t.Run(delay.String(), func(t *testing.T) { testAdaptiveReorderedStart(t, delay) })
	}
}

func testAdaptiveReorderedStart(t *testing.T, delay time.Duration) {
	clock := newFakeClock()
	m, _, _ := newProbingMultipath(t, loopbackPaths(1), testKey(t, 0x42), clock)
	if err := m.EnableAdaptive(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.Open(0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	_, source := rawPeer(t)
	a := m.adaptive.Load()
	sender := bond.New(bond.Epoch{Boot: 987, Generation: 1})
	sender.SetRemote(a.transport.Epoch(), true)
	now := time.Now()
	sender.Path(0, 0, 30*time.Millisecond, now)
	a.learn(m.peers[0].paths[0], source, bond.Hello(sender.Epoch(), 0), true)
	var frames []frame.Control
	for i := byte(1); i <= 2; i++ {
		at := now.Add(time.Duration(i) * 20 * time.Millisecond)
		if err := sender.Enqueue(bytes.Repeat([]byte{i}, 1000), at); err != nil {
			t.Fatal(err)
		}
		frames = append(frames, sender.Poll(at)[0].Frame)
	}
	m.SetPeerRemote(source)
	a.learn(m.peers[0].paths[0], source, bond.Hello(sender.Epoch(), 0), false)
	for _, index := range []int{1, 0} {
		a.receive(m.peers[0].paths[0], source, frames[index])
		if index == 1 {
			clock.advance(delay)
			if _, ok := m.resequencer.Load().Pop(); ok {
				t.Fatalf("bulk gap expired within transport repair lifetime at %s", delay)
			}
		}
	}
	for i := byte(1); i <= 2; i++ {
		got, ok := m.resequencer.Load().Pop()
		if !ok || !bytes.Equal(got.Payload, bytes.Repeat([]byte{i}, 1000)) {
			t.Fatalf("bulk packet %d missing or out of order: received=%v payload=%x", i, ok, got.Payload[:min(4, len(got.Payload))])
		}
	}
}

// BG regression: capability negotiation must not change padded PMTU echoes.
func TestAdaptivePaddedProbeEcho(t *testing.T) {
	psk := testKey(t, 0x42)
	m, _, _ := newProbingMultipath(t, loopbackPaths(1), psk, newFakeClock())
	if err := m.EnableAdaptive(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.Open(0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	peer, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	raw, err := frame.Encode(psk, frame.Probe{PathID: 0, ProbeSeq: 1, TimestampNanos: time.Now().UnixNano(), SessionID: 987, Padded: true, PadLen: 1000})
	if err != nil {
		t.Fatal(err)
	}
	m.handleInbound(m.paths[0], raw, netip.MustParseAddrPort(peer.LocalAddr().String()))
	if err := peer.SetReadDeadline(time.Now().Add(200 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 2000)
	n, err := peer.Read(b)
	if err != nil {
		t.Fatalf("padded probe was not reflected: %v", err)
	}
	if n != len(raw) {
		t.Fatalf("echo changed wire size: %d != %d", n, len(raw))
	}
	decoded, err := frame.Decode(psk, b[:n])
	if err != nil {
		t.Fatal(err)
	}
	if echo, ok := decoded.(frame.Probe); !ok || !echo.Padded || !echo.IsEcho || len(echo.Payload) != 0 {
		t.Fatalf("invalid PMTU echo: %#v", decoded)
	}
}

// BG regression: independent small datagrams must survive cross-path reorder,
// including arrivals after a later datagram has already reached the engine.
func TestAdaptiveSmallDatagramsDoNotWaitForMissingPredecessor(t *testing.T) {
	clk := newFakeClock()
	m, _, _ := newProbingMultipath(t, loopbackPaths(1), testKey(t, 0x42), clk)
	if err := m.EnableAdaptive(); err != nil {
		t.Fatal(err)
	}
	receive, _, err := m.Open(0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	_, source := rawPeer(t)
	a := m.adaptive.Load()
	sender := bond.New(bond.Epoch{Boot: 987, Generation: 1})
	sender.SetRemote(a.transport.Epoch(), true)
	now := time.Now()
	sender.Path(0, 0, 30*time.Millisecond, now)
	a.learn(m.peers[0].paths[0], source, bond.Hello(sender.Epoch(), 0), true)
	var frames []frame.Control
	for i := byte(1); i <= 3; i++ {
		at := now.Add(time.Duration(i) * 10 * time.Millisecond)
		if err := sender.Enqueue([]byte{i}, at); err != nil {
			t.Fatal(err)
		}
		frames = append(frames, sender.Poll(at)[0].Frame)
	}
	got := make(chan byte, 3)
	go func() {
		for {
			packets, sizes, endpoints := [][]byte{make([]byte, 2048)}, make([]int, 1), make([]Endpoint, 1)
			if _, err := receive[0](packets, sizes, endpoints); err != nil {
				return
			}
			got <- packets[0][0]
		}
	}()
	for _, index := range []int{0, 2, 1} {
		a.receive(m.peers[0].paths[0], source, frames[index])
		select {
		case value := <-got:
			if value != byte(index+1) {
				t.Fatalf("got %d, want %d", value, index+1)
			}
		case <-time.After(200 * time.Millisecond):
			t.Fatalf("small datagram %d blocked by missing predecessor or discarded after reorder", index+1)
		}
	}
}

func TestAdaptiveRejectsLegacyData(t *testing.T) {
	m, _, _ := newProbingMultipath(t, loopbackPaths(1), testKey(t, 0x42), newFakeClock())
	if err := m.EnableAdaptive(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.Open(0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	m.dispatchInbound(m.paths[0], frame.Data{OuterSeq: 1, Payload: []byte("unauthenticated")}, nil, netip.MustParseAddrPort("127.0.0.1:51820"))
	if _, ok := m.resequencer.Load().Pop(); ok {
		t.Fatal("legacy unauthenticated data entered adaptive receive stream")
	}
}

func TestAdaptiveRetiresRemovedSocketRoutes(t *testing.T) {
	m, _, _ := newProbingMultipath(t, loopbackPaths(2), testKey(t, 0x42), newFakeClock())
	if err := m.EnableAdaptive(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.Open(0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	_, source := rawPeer(t)
	a := m.adaptive.Load()
	removed := m.paths[1]
	hello := bond.Hello(bond.Epoch{Boot: 987, Generation: 1}, 0)
	a.learn(removed, source, hello, true)
	if err := m.RemovePath(removed.name); err != nil {
		t.Fatal(err)
	}
	a.learn(removed, source, hello, false)
	for _, path := range m.PeerSnapshots()[0].Adaptive.Paths {
		if path.Up {
			t.Fatalf("removed socket remained eligible: %+v", path)
		}
	}
}

func TestAdaptiveInvalidSecondaryPeerDoesNotHangOpen(t *testing.T) {
	m, _, _ := newProbingMultipath(t, loopbackPaths(1), testKey(t, 0x42), newFakeClock())
	if err := m.EnableAdaptive(); err != nil {
		t.Fatal(err)
	}
	if err := m.AddConcentratorPeer("no-probes", testKey(t, 0x43), m.scheduler, nil, nil); err != nil {
		t.Fatal(err)
	}
	opened := make(chan error, 1)
	go func() { _, _, err := m.Open(0); opened <- err }()
	select {
	case err := <-opened:
		if err == nil {
			_ = m.Close()
			t.Fatal("peer without authenticated probes accepted")
		}
	case <-time.After(500 * time.Millisecond):
		m.mu.Lock()
		if m.recvClosed != nil {
			close(m.recvClosed)
			m.recvClosed = nil
		}
		m.mu.Unlock()
		<-opened
		t.Fatal("invalid secondary peer left Open waiting on a running adaptive worker")
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
}
