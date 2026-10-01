package bind

import (
	"bytes"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/frame"
	"github.com/7mind/wanbond/internal/telemetry"
)

// expectNoDatagram asserts that within a short window c receives no transport data frame
// carrying payload under any of the codecs (proves a Send did NOT egress to this receiver).
// The lane keepalives of the receiver's own peer are expected and skipped.
func expectNoDatagram(t testing.TB, c *net.UDPConn, who string, payload []byte, codecs ...*frame.Codec) {
	t.Helper()
	if err := c.SetReadDeadline(time.Now().Add(150 * time.Millisecond)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	buf := make([]byte, maxDatagram)
	for {
		n, err := c.Read(buf)
		if err != nil {
			return
		}
		for _, codec := range codecs {
			fr, err := codec.Decode(buf[:n])
			if err != nil {
				continue
			}
			if control, ok := fr.(frame.Control); ok && bytes.HasSuffix(control.Payload, payload) {
				t.Fatalf("%s received a datagram carrying %q (Send must not egress here)", who, payload)
			}
		}
	}
}

// datagramBytesSent returns the bytes of small datagrams the peer's transport has sent on
// all its lanes. Lane keepalives and acknowledgements are not counted.
func datagramBytesSent(t testing.TB, m *Multipath, peer int) uint64 {
	t.Helper()
	snapshot := m.PeerSnapshots()[peer].Adaptive
	var sent uint64
	if snapshot == nil {
		t.Fatalf("peer %d has no open transport", peer)
		return sent
	}
	for _, lane := range snapshot.Paths {
		sent += lane.InteractiveSent
	}
	return sent
}

// TestMultipathSendRoutesPerPeerVirt is the T85 acceptance: with TWO peerStates each
// holding a DISTINCT virtual endpoint and a lane on a DISTINCT path, Send resolves the
// owning peer from the endpoint and drives ONLY that peer's transport, send Codec and
// per-(peer,path) egress. A Send to peer A's endpoint is sent only by A's transport and
// egresses on A's path; a Send to peer B's endpoint is fully independent; a Send to an
// unknown endpoint errors and touches NEITHER peer.
func TestMultipathSendRoutesPerPeerVirt(t *testing.T) {
	pskA := testKey(t, 0x85)
	pskB := testKey(t, 0x86)
	// Two shared sockets, "a" and "b": peer A egresses on socket a, peer B on socket b —
	// genuinely distinct path sets, not merely distinct remotes over one socket.
	m, err := newMultipath(t, loopbackPaths(2), pskA)
	if err != nil {
		t.Fatalf("NewMultipath: %v", err)
	}
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	// A receiver socket per peer, standing in for each peer's remote end.
	recvA, apA := rawPeer(t)
	recvB, apB := rawPeer(t)
	codecA, _ := frame.NewCodec(pskA)
	codecB, _ := frame.NewCodec(pskB)

	// Peer A is the primary; it egresses on its path 0 (shared socket "a") to recvA.
	peerA := m.peerState
	pathA := peerA.paths[0]
	remoteA := newRemoteTransport(t, peerA, 987)
	remoteA.join(pathA, apA)

	// Peer B egresses on its view of the OTHER shared socket "b" to recvB.
	peerB := bindSecondPeer(t, m, "peer-B", pskB, telemetry.SystemClock{})
	pathB := peerPathByName(peerB, "b")
	remoteB := newRemoteTransport(t, peerB, 988)
	remoteB.join(pathB, apB)

	// Distinct virts is the whole premise.
	if peerA.virt == peerB.virt {
		t.Fatal("the two peers share one virtual endpoint; the routing premise is void")
	}

	// --- Send to peer A's endpoint: only A's transport is touched, egress hits recvA. ---
	payloadA := []byte("inner-for-peer-A")
	if err := m.Send([][]byte{payloadA}, peerA.virt); err != nil {
		t.Fatalf("Send to peer A: %v", err)
	}
	if data, _ := readTransportData(t, recvA, codecA); !bytes.HasSuffix(data.Payload, payloadA) {
		t.Fatalf("peer A wire payload = %x, want it to end in %q", data.Payload, payloadA)
	}
	expectNoDatagram(t, recvB, "peer B's remote (after Send to A)", payloadA, codecA, codecB)

	if got := datagramBytesSent(t, m, 0); got == 0 {
		t.Fatal("peer A's transport sent no datagram after a Send to A")
	}
	if got := datagramBytesSent(t, m, 1); got != 0 {
		t.Fatalf("peer B's transport sent %d datagram bytes after a Send to A, want 0 (independent)", got)
	}
	if got := pathA.txBytes.Load(); got == 0 {
		t.Fatal("peer A path txBytes did not advance on a Send to A")
	}

	// --- Send to peer B's endpoint: fully independent — only B advances, egress hits recvB. ---
	payloadB := []byte("inner-for-peer-B")
	remoteB.join(pathB, apB) // renew the lane, as the probe cadence does
	if err := m.Send([][]byte{payloadB}, peerB.virt); err != nil {
		t.Fatalf("Send to peer B: %v", err)
	}
	if data, _ := readTransportData(t, recvB, codecB); !bytes.HasSuffix(data.Payload, payloadB) {
		t.Fatalf("peer B wire payload = %x, want it to end in %q", data.Payload, payloadB)
	}
	expectNoDatagram(t, recvA, "peer A's remote (after Send to B)", payloadB, codecA, codecB)

	if got := datagramBytesSent(t, m, 1); got == 0 {
		t.Fatal("peer B's transport sent no datagram after a Send to B")
	}
	if got := pathB.txBytes.Load(); got == 0 {
		t.Fatal("peer B path txBytes did not advance on a Send to B")
	}

	// --- Send to an UNKNOWN endpoint: errors and touches NEITHER peer. ---
	unknown := &udpEndpoint{} // a well-typed endpoint never registered in peerByVirt
	nowhere := []byte("inner-for-nobody")
	if err := m.Send([][]byte{nowhere}, unknown); !errors.Is(err, ErrNoHealthyPath) {
		t.Fatalf("Send to an unknown endpoint = %v, want ErrNoHealthyPath", err)
	}
	expectNoDatagram(t, recvA, "peer A's remote (after unknown-endpoint Send)", nowhere, codecA, codecB)
	expectNoDatagram(t, recvB, "peer B's remote (after unknown-endpoint Send)", nowhere, codecA, codecB)
}
