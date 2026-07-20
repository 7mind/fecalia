package bind

import (
	"errors"
	"net"
	"syscall"
	"testing"
)

// TestSendEMSGSIZECounted is the T201 reproduce-first regression for the send-side
// half of the DF change: when a path write fails with EMSGSIZE — the explicit
// "datagram exceeds path MTU, DF set" the DF policy surfaces in place of silent
// fragmentation — Send must COUNT the drop on the chosen path's per-path counter and
// still RETURN the error (fail-fast: the loss is surfaced, never swallowed). Before
// accountSendError was wired the write error propagated but the drop was uncounted, so
// the counter stayed 0 and this failed.
//
// The EMSGSIZE is forced deterministically and portably by sending an inner datagram
// whose framed OUTER size exceeds the maximum IP packet (~65507 bytes of UDP payload):
// the kernel rejects it with EMSGSIZE regardless of the DF policy or link MTU, so the
// test needs no oversized-MTU link and runs on every platform.
func TestSendEMSGSIZECounted(t *testing.T) {
	psk := testKey(t, 0x7E)
	m, err := newMultipath(t, loopbackPaths(2), psk)
	if err != nil {
		t.Fatalf("NewMultipath: %v", err)
	}
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	// The chosen path (index 0) needs a remote for Send to reach the write syscall.
	peer, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatalf("listen peer: %v", err)
	}
	defer peer.Close()
	m.paths[0].setRemote(peer.LocalAddr().(*net.UDPAddr).AddrPort())

	// 70000 bytes of inner payload frames to an outer datagram larger than the maximum
	// UDP payload (~65507), so WriteToUDPAddrPort fails with EMSGSIZE.
	oversized := make([]byte, 70000)
	const sends = 3
	for i := 0; i < sends; i++ {
		err := m.Send([][]byte{oversized}, m.virt)
		if err == nil {
			t.Fatalf("Send %d: expected EMSGSIZE, got nil (oversized datagram was not rejected)", i)
		}
		if !errors.Is(err, syscall.EMSGSIZE) {
			t.Fatalf("Send %d: error = %v, want EMSGSIZE (fail-fast: the error must still propagate)", i, err)
		}
	}

	if got := m.paths[0].emsgsizeDrops.Load(); got != sends {
		t.Errorf("path 0 emsgsizeDrops = %d, want %d (every EMSGSIZE send counted)", got, sends)
	}
	// The unchosen path never wrote, so its counter is untouched.
	if got := m.paths[1].emsgsizeDrops.Load(); got != 0 {
		t.Errorf("path 1 emsgsizeDrops = %d, want 0 (never selected)", got)
	}
	// A non-EMSGSIZE write error must NOT touch the counter: accountSendError only
	// classifies EMSGSIZE, returning any other error unchanged.
	other := &net.OpError{Op: "write", Err: syscall.ECONNREFUSED}
	if err := m.accountSendError(m.paths[0], other); !errors.Is(err, syscall.ECONNREFUSED) {
		t.Errorf("accountSendError(ECONNREFUSED) = %v, want it returned unchanged", err)
	}
	if got := m.paths[0].emsgsizeDrops.Load(); got != sends {
		t.Errorf("path 0 emsgsizeDrops = %d after a non-EMSGSIZE error, want %d (unchanged)", got, sends)
	}
}
