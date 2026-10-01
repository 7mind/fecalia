package bind

import (
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/bond"
	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/frame"
)

// TestMultipathTxBytesCountedOnSend asserts the transport's writes accumulate, per path,
// exactly the OUTER-wire bytes written to that path's socket (T23). The peer socket's read
// length is the ground-truth wire size, so TxBytes must equal the sum of the sizes the peer
// observed — and only the path holding the lane (index 0) is charged.
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

	peer, peerAP := rawPeer(t)
	newRemoteTransport(t, m.peerState, 987).join(m.paths[0], peerAP)

	const frames = 3
	for i := 0; i < frames; i++ {
		if err := m.Send([][]byte{[]byte("inner-wg-datagram")}, m.virt); err != nil {
			t.Fatalf("Send %d: %v", i, err)
		}
	}

	// The transport also writes lane keepalives and repeats unacknowledged datagrams, so
	// the peer keeps receiving. Every write lands in the peer's socket before it is
	// counted: whenever the socket is drained and no write is in progress, the counter
	// equals the bytes read.
	codec, _ := frame.NewCodec(psk)
	buf := make([]byte, maxDatagram)
	var wantTx uint64
	datagrams := 0
	deadline := time.Now().Add(2 * time.Second)
	for {
		if err := peer.SetReadDeadline(time.Now().Add(10 * time.Millisecond)); err != nil {
			t.Fatalf("set read deadline: %v", err)
		}
		for {
			n, err := peer.Read(buf)
			if err != nil {
				break
			}
			wantTx += uint64(n)
			if fr, err := codec.Decode(buf[:n]); err == nil {
				if control, ok := fr.(frame.Control); ok && control.ControlType == bond.DataType && n > bond.Overhead {
					datagrams++
				}
			}
		}
		if datagrams >= frames && m.PeerSnapshots()[0].Paths[0].TxBytes == wantTx {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("path 0 TxBytes = %d, want %d (sum of wire bytes the peer received, %d datagrams)",
				m.PeerSnapshots()[0].Paths[0].TxBytes, wantTx, datagrams)
		}
	}

	snaps := m.PeerSnapshots()[0].Paths
	if len(snaps) != 2 {
		t.Fatalf("PeerSnapshots()[0].Paths len = %d, want 2", len(snaps))
	}
	// The path without a lane carried nothing.
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
	_, want := rawPeer(t)
	m.paths[0].setRemote(want)

	if got := m.PeerSnapshots()[0].Paths[0].Remote; got != want {
		t.Errorf("path 0 Remote = %v, want %v (follows setRemote repoint)", got, want)
	}
}

// TestMultipathRxBytesCountedOnReceive asserts the per-path readLoop accumulates the
// OUTER-wire bytes it pulls off the socket (T23), independent of frame kind — here a
// transport data frame delivered to a specific path's socket. The count is charged only to the
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

	// Send one data frame to path 1's socket and drain it through the fan-in drainer so
	// the receive definitely completed before we read the counter.
	remote := newRemoteTransport(t, m.peerState, 987)
	cl, src := dialPath(t, m.paths[1])
	remote.join(m.paths[1], src)
	payload := []byte("opaque-wireguard-datagram")
	if _, err := cl.Write(remote.wire(remote.small(m.paths[1], payload))); err != nil {
		t.Fatalf("write: %v", err)
	}
	receiveOne(t, fn)

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
		t.Fatal("path 1 RxBytes never advanced after an inbound data frame")
	}
	// The wire size is the encoded frame length; it must exceed the inner payload
	// (outer framing + MAC) and be charged only to the receiving path.
	if got <= uint64(len(payload)) {
		t.Errorf("path 1 RxBytes = %d, want > inner payload len %d (outer framing)", got, len(payload))
	}
	if snaps := m.PeerSnapshots()[0].Paths; snaps[0].RxBytes != 0 {
		t.Errorf("path 0 RxBytes = %d, want 0 (frame arrived on path 1)", snaps[0].RxBytes)
	}
}
