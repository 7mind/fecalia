package bind

import (
	"net"
	"testing"
	"time"
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
