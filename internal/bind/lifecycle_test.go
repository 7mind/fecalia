package bind

import (
	"errors"
	"net"
	"testing"

	"github.com/amnezia-vpn/amneziawg-go/conn"
)

// TestMultipathEngineLifecycleCloseThenOpen reproduces the bind-lifecycle defect
// (facet a): the amneziawg engine calls Bind.Close() BEFORE every Open()
// (device.upLocked → BindUpdate → closeBindLocked). Before the fix, that pre-open
// Close set a sticky `closed` flag that Open never reset, so the first Send after
// bring-up returned net.ErrClosed and the tunnel could not even transmit a
// handshake. Post-fix, Close clears state and Open rebuilds it, so Send succeeds.
func TestMultipathEngineLifecycleCloseThenOpen(t *testing.T) {
	psk := testKey(t, 0xAB)
	m, err := newMultipath(t, loopbackPaths(1), psk)
	if err != nil {
		t.Fatalf("NewMultipath: %v", err)
	}
	// Engine lifecycle: BindUpdate closes the (not-yet-open) bind first.
	if err := m.Close(); err != nil {
		t.Fatalf("pre-open Close: %v", err)
	}
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open after pre-open Close: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	peer, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatalf("listen peer: %v", err)
	}
	defer peer.Close()
	m.paths[0].setRemote(peer.LocalAddr().(*net.UDPAddr).AddrPort())

	if err := m.Send([][]byte{[]byte("hello")}, m.virt); err != nil {
		if errors.Is(err, net.ErrClosed) {
			t.Fatalf("Send returned net.ErrClosed after engine Close→Open lifecycle: %v", err)
		}
		t.Fatalf("Send after engine lifecycle Close→Open: %v", err)
	}
}

// TestMultipathReopenAfterClose reproduces the bind-lifecycle defect (facet b): a
// Down/Up cycle is Close (on an open bind) then Open again. Before the fix, Close
// left m.paths populated, so the second Open returned conn.ErrBindAlreadyOpen and
// the bind was not reopenable. conn.StdNetBind supports this cycle; so must we.
func TestMultipathReopenAfterClose(t *testing.T) {
	psk := testKey(t, 0xCD)
	m, err := newMultipath(t, loopbackPaths(1), psk)
	if err != nil {
		t.Fatalf("NewMultipath: %v", err)
	}
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, _, err := m.Open(0); err != nil {
		if errors.Is(err, conn.ErrBindAlreadyOpen) {
			t.Fatalf("re-Open after Close returned ErrBindAlreadyOpen: bind is not reopenable: %v", err)
		}
		t.Fatalf("re-Open after Close: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
}
