package bind

import (
	"net"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/config"
)

// TestMultipathTxBytesCountedOnSend asserts Send accumulates, per path, exactly the
// OUTER-wire bytes it writes to that path's socket (T23). The peer socket's read length
// is the ground-truth wire size, so TxBytes must equal the sum of the sizes the peer
// observed — and only the chosen path (index 0, the sole one with a remote) is charged.
func TestMultipathTxBytesCountedOnSend(t *testing.T) {
	psk := testKey(t, 0x71)
	m, err := newMultipath(t, loopbackPaths(2), psk)
	if err != nil {
		t.Fatalf("NewMultipath: %v", err)
	}
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	peer, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatalf("listen peer: %v", err)
	}
	defer peer.Close()
	m.paths[0].setRemote(peer.LocalAddr().(*net.UDPAddr).AddrPort())

	const frames = 3
	var wantTx uint64
	buf := make([]byte, maxDatagram)
	for i := 0; i < frames; i++ {
		if err := m.Send([][]byte{[]byte("inner-wg-datagram")}, m.virt); err != nil {
			t.Fatalf("Send %d: %v", i, err)
		}
		n, err := peer.Read(buf)
		if err != nil {
			t.Fatalf("peer read %d: %v", i, err)
		}
		wantTx += uint64(n)
	}

	snaps := m.PeerSnapshots()[0].Paths
	if len(snaps) != 2 {
		t.Fatalf("PeerSnapshots()[0].Paths len = %d, want 2", len(snaps))
	}
	if snaps[0].TxBytes != wantTx {
		t.Errorf("path 0 TxBytes = %d, want %d (sum of wire bytes the peer received)", snaps[0].TxBytes, wantTx)
	}
	// The unchosen path (no remote) carried nothing.
	if snaps[1].TxBytes != 0 {
		t.Errorf("path 1 TxBytes = %d, want 0 (never selected)", snaps[1].TxBytes)
	}
	// Send does not touch the receive counter.
	if snaps[0].RxBytes != 0 {
		t.Errorf("path 0 RxBytes = %d, want 0 (no inbound)", snaps[0].RxBytes)
	}
}

// TestPeerSnapshots_CarriesAddressing asserts PeerSnapshots surfaces each path's
// runtime networking identity on PathTraffic (T216, G21): the bound Source and
// LocalAddr and the resolved BindMode are populated for a bound path, Remote is
// zero until a remote is set, and Remote follows a setRemote repoint. It reads
// the fields off the same lock-free snapshot the counters use.
func TestPeerSnapshots_CarriesAddressing(t *testing.T) {
	psk := testKey(t, 0x72)
	// Set the effective bind mode the way config.normalize would in production
	// (the loopbackPaths helper leaves it empty); BindModeSource keeps the
	// harness's existing source-IP-pin behavior so binding is unperturbed.
	defs := loopbackPaths(2)
	for i := range defs {
		defs[i].Bind = config.BindModeSource
	}
	m, err := newMultipath(t, defs, psk)
	if err != nil {
		t.Fatalf("NewMultipath: %v", err)
	}
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	snaps := m.PeerSnapshots()[0].Paths
	if len(snaps) != 2 {
		t.Fatalf("PeerSnapshots()[0].Paths len = %d, want 2", len(snaps))
	}
	p0 := snaps[0]
	if !p0.Source.IsValid() {
		t.Errorf("path 0 Source not populated")
	}
	if !p0.LocalAddr.IsValid() {
		t.Errorf("path 0 LocalAddr not populated from the bound socket")
	}
	if p0.BindMode != config.BindModeSource {
		t.Errorf("path 0 BindMode = %q, want %q", p0.BindMode, config.BindModeSource)
	}
	if p0.Remote.IsValid() {
		t.Errorf("path 0 Remote = %v, want zero before any remote is set", p0.Remote)
	}

	// A setRemote repoint must be reflected in the next snapshot.
	peer, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatalf("listen peer: %v", err)
	}
	defer peer.Close()
	want := peer.LocalAddr().(*net.UDPAddr).AddrPort()
	m.paths[0].setRemote(want)

	if got := m.PeerSnapshots()[0].Paths[0].Remote; got != want {
		t.Errorf("path 0 Remote = %v, want %v (follows setRemote repoint)", got, want)
	}
}

// TestMultipathRxBytesCountedOnReceive asserts the per-path readLoop accumulates the
// OUTER-wire bytes it pulls off the socket (T23), independent of frame kind — here a
// DATA frame delivered to a specific path's socket. The count is charged only to the
// receiving path.
func TestMultipathRxBytesCountedOnReceive(t *testing.T) {
	psk := testKey(t, 0x72)
	m, err := newMultipath(t, loopbackPaths(2), psk)
	if err != nil {
		t.Fatalf("NewMultipath: %v", err)
	}
	fns, _, err := m.Open(0)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	fn := fns[0]

	// Send one DATA frame to path 1's socket and drain it through the fan-in drainer so
	// the receive definitely completed before we read the counter.
	dst := m.paths[1].conn.LocalAddr().(*net.UDPAddr)
	payload := []byte("opaque-wireguard-datagram")
	sendDataTo(t, psk, dst, 1, payload)

	bufs := [][]byte{make([]byte, 2048)}
	sizes := make([]int, 1)
	eps := make([]Endpoint, 1)
	if _, err := fn(bufs, sizes, eps); err != nil {
		t.Fatalf("receive: %v", err)
	}

	// The counter is written by the reader goroutine; the drainer wakes off a separate
	// signal, so poll briefly for the Add to land rather than assuming ordering.
	var got uint64
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		snaps := m.PeerSnapshots()[0].Paths
		if snaps[1].RxBytes > 0 {
			got = snaps[1].RxBytes
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got == 0 {
		t.Fatal("path 1 RxBytes never advanced after an inbound DATA frame")
	}
	// The wire size is the encoded frame length; it must exceed the inner payload
	// (outer DATA header + MAC) and be charged only to the receiving path.
	if got <= uint64(len(payload)) {
		t.Errorf("path 1 RxBytes = %d, want > inner payload len %d (outer framing)", got, len(payload))
	}
	if snaps := m.PeerSnapshots()[0].Paths; snaps[0].RxBytes != 0 {
		t.Errorf("path 0 RxBytes = %d, want 0 (frame arrived on path 1)", snaps[0].RxBytes)
	}
}
