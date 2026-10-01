package bind

import (
	"net"
	"net/netip"
	"syscall"
	"testing"
	"time"
)

// TestSendEMSGSIZECounted is the T201 regression for the send-side half of the DF
// change: when a path write fails with EMSGSIZE — the explicit "datagram exceeds path
// MTU, DF set" the DF policy surfaces in place of silent fragmentation — the drop must
// be COUNTED on that path's per-path socket-write-error counter (the loss is surfaced,
// never swallowed) and must not count as transmitted bytes.
//
// The EMSGSIZE is forced deterministically and portably through the path's write seam,
// so the test needs no small-MTU link and runs on every platform.
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

	// The transport holds a lane on path 0 only, so every datagram is written there.
	_, peerAP := rawPeer(t)
	m.paths[0].writeUDP = func([]byte, netip.AddrPort) (int, error) {
		return 0, &net.OpError{Op: "write", Net: "udp", Err: syscall.EMSGSIZE}
	}
	newRemoteTransport(t, m.peerState, 987).join(m.paths[0], peerAP)

	const sends = 3
	for i := 0; i < sends; i++ {
		if err := m.Send([][]byte{{byte(i)}}, m.virt); err != nil {
			t.Fatalf("Send %d: %v", i, err)
		}
	}

	// Each datagram's write is refused once, and again whenever the transport repeats
	// it, so the counter reaches at least one per datagram.
	deadline := time.Now().Add(2 * time.Second)
	for m.PeerSnapshots()[0].Paths[0].SocketWriteErrors < sends {
		if time.Now().After(deadline) {
			t.Fatalf("path 0 SocketWriteErrors = %d, want at least %d (every EMSGSIZE write counted)",
				m.PeerSnapshots()[0].Paths[0].SocketWriteErrors, sends)
		}
		time.Sleep(time.Millisecond)
	}
	snaps := m.PeerSnapshots()[0].Paths
	if snaps[0].TxBytes != 0 {
		t.Errorf("path 0 TxBytes = %d, want 0 (a refused write transmitted nothing)", snaps[0].TxBytes)
	}
	// The path without a lane never wrote, so its counter is untouched.
	if snaps[1].SocketWriteErrors != 0 {
		t.Errorf("path 1 SocketWriteErrors = %d, want 0 (never selected)", snaps[1].SocketWriteErrors)
	}
}
