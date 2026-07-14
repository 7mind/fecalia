package bind

import (
	"bytes"
	"crypto/rand"
	"io"
	"net"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/frame"
	"github.com/7mind/wanbond/internal/log"
	"github.com/7mind/wanbond/internal/sched"
	"github.com/7mind/wanbond/internal/telemetry"
)

// addAlwaysUpPeerOverShared constructs a SECOND peerState with its OWN distinct virt,
// scheduler (AlwaysUp so Pick is deterministically index 0), send Codec, and a single
// peerPathState VIEW over the already-bound shared socket m.shared[sharedIdx]. It stands
// in for the concentrator's per-peer wiring (a later G4 task): binding the peer into
// m.peers/m.peersByName AND registering its virt in m.peerByVirt so an outbound Send to
// that virt routes to THIS peerState. It must run after m.Open.
func addAlwaysUpPeerOverShared(t testing.TB, m *Multipath, name string, psk config.Key, sharedIdx int) *peerState {
	t.Helper()
	lg, err := log.New("error", io.Discard)
	if err != nil {
		t.Fatalf("build logger: %v", err)
	}
	scheduler, err := sched.NewActiveBackup([]sched.PathHealth{sched.AlwaysUp{}}, sched.Config{FailbackAfter: time.Second}, telemetry.SystemClock{}, lg)
	if err != nil {
		t.Fatalf("build second-peer scheduler: %v", err)
	}
	sendCodec, err := frame.NewCodec(psk)
	if err != nil {
		t.Fatalf("build second-peer send codec: %v", err)
	}
	pathCodec, err := frame.NewCodec(psk)
	if err != nil {
		t.Fatalf("build second-peer path codec: %v", err)
	}
	p := &peerState{
		name:      name,
		virt:      &udpEndpoint{},
		scheduler: scheduler,
		reflector: telemetry.NewReflector(psk, rand.Reader),
		sendCodec: sendCodec,
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	p.paths = append(p.paths, &peerPathState{sharedPathState: m.shared[sharedIdx], peer: p, codec: pathCodec})
	m.peers = append(m.peers, p)
	m.peersByName[name] = p
	m.peerByVirt[p.virt] = p
	return p
}

// recvUDP reads one datagram off c within a short deadline; ok=false on timeout.
func recvUDP(t testing.TB, c *net.UDPConn) (b []byte, ok bool) {
	t.Helper()
	if err := c.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	buf := make([]byte, maxDatagram)
	n, err := c.Read(buf)
	if err != nil {
		return nil, false
	}
	return append([]byte(nil), buf[:n]...), true
}

// expectNoUDP asserts c receives nothing within a short window (proves a Send did NOT
// egress to this receiver).
func expectNoUDP(t testing.TB, c *net.UDPConn, who string) {
	t.Helper()
	if err := c.SetReadDeadline(time.Now().Add(150 * time.Millisecond)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	buf := make([]byte, maxDatagram)
	if n, err := c.Read(buf); err == nil {
		t.Fatalf("%s received %d bytes, want nothing (Send must not egress here)", who, n)
	}
}

// decodeData decodes a wire datagram as a DATA frame, failing the test otherwise.
func decodeData(t testing.TB, psk config.Key, wire []byte) frame.Data {
	t.Helper()
	codec, err := frame.NewCodec(psk)
	if err != nil {
		t.Fatalf("new codec: %v", err)
	}
	fr, err := codec.Decode(wire)
	if err != nil {
		t.Fatalf("decode wire: %v", err)
	}
	data, ok := fr.(frame.Data)
	if !ok {
		t.Fatalf("wire frame is %T, want frame.Data", fr)
	}
	return data
}

// TestMultipathSendRoutesPerPeerVirt is the T85 acceptance: with TWO peerStates each
// holding a DISTINCT virtual endpoint over DISTINCT path sets, Send resolves the owning
// peer from the endpoint and drives ONLY that peer's outerSeq / scheduler / send Codec
// and per-(peer,path) egress. A Send to peer A's endpoint advances only A's outerSeq and
// egresses on A's path; a Send to peer B's endpoint is fully independent; a Send to an
// unknown endpoint errors and touches NEITHER peer.
func TestMultipathSendRoutesPerPeerVirt(t *testing.T) {
	psk := testKey(t, 0x85)
	// Two shared sockets, "a" and "b": peer A egresses on socket a, peer B on socket b —
	// genuinely distinct path sets, not merely distinct remotes over one socket.
	m, err := newMultipath(t, loopbackPaths(2), psk)
	if err != nil {
		t.Fatalf("NewMultipath: %v", err)
	}
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	// A receiver socket per peer, standing in for each peer's remote end.
	recvA, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatalf("listen recvA: %v", err)
	}
	defer recvA.Close()
	recvB, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatalf("listen recvB: %v", err)
	}
	defer recvB.Close()
	apA := recvA.LocalAddr().(*net.UDPAddr).AddrPort()
	apB := recvB.LocalAddr().(*net.UDPAddr).AddrPort()

	// Peer A is the primary; it egresses on its path 0 (shared socket "a") to recvA.
	peerA := m.peerState
	peerA.paths[0].setRemote(apA)
	pathA := peerA.paths[0]

	// Peer B views the OTHER shared socket "b" and egresses to recvB.
	peerB := addAlwaysUpPeerOverShared(t, m, "peer-B", psk, 1)
	peerB.paths[0].setRemote(apB)
	pathB := peerB.paths[0]

	// Distinct virts is the whole premise.
	if peerA.virt == peerB.virt {
		t.Fatal("the two peers share one virtual endpoint; the routing premise is void")
	}

	// --- Send to peer A's endpoint: only A's outerSeq/path is touched, egress hits recvA. ---
	payloadA := []byte("inner-for-peer-A")
	if err := m.Send([][]byte{payloadA}, peerA.virt); err != nil {
		t.Fatalf("Send to peer A: %v", err)
	}
	wire, ok := recvUDP(t, recvA)
	if !ok {
		t.Fatal("peer A's remote received nothing; Send did not egress on A's path")
	}
	if data := decodeData(t, psk, wire); !bytes.Equal(data.Payload, payloadA) {
		t.Fatalf("peer A wire payload = %q, want %q", data.Payload, payloadA)
	}
	expectNoUDP(t, recvB, "peer B's remote (after Send to A)")

	if got := peerA.outerSeq.Load(); got != 1 {
		t.Fatalf("peer A outerSeq = %d after one Send, want 1", got)
	}
	if got := peerB.outerSeq.Load(); got != 0 {
		t.Fatalf("peer B outerSeq = %d after a Send to A, want 0 (independent)", got)
	}
	if got := pathA.txBytes.Load(); got == 0 {
		t.Fatal("peer A path txBytes did not advance on a Send to A")
	}
	if got := pathB.txBytes.Load(); got != 0 {
		t.Fatalf("peer B path txBytes = %d after a Send to A, want 0", got)
	}

	// --- Send to peer B's endpoint: fully independent — only B advances, egress hits recvB. ---
	payloadB := []byte("inner-for-peer-B")
	if err := m.Send([][]byte{payloadB}, peerB.virt); err != nil {
		t.Fatalf("Send to peer B: %v", err)
	}
	wire, ok = recvUDP(t, recvB)
	if !ok {
		t.Fatal("peer B's remote received nothing; Send did not egress on B's path")
	}
	if data := decodeData(t, psk, wire); !bytes.Equal(data.Payload, payloadB) {
		t.Fatalf("peer B wire payload = %q, want %q", data.Payload, payloadB)
	}
	expectNoUDP(t, recvA, "peer A's remote (after Send to B)")

	if got := peerB.outerSeq.Load(); got != 1 {
		t.Fatalf("peer B outerSeq = %d after one Send, want 1", got)
	}
	if got := peerA.outerSeq.Load(); got != 1 {
		t.Fatalf("peer A outerSeq = %d after a Send to B, want 1 (unchanged)", got)
	}
	if got := pathB.txBytes.Load(); got == 0 {
		t.Fatal("peer B path txBytes did not advance on a Send to B")
	}

	// --- Send to an UNKNOWN endpoint: errors and touches NEITHER peer. ---
	beforeA, beforeB := peerA.outerSeq.Load(), peerB.outerSeq.Load()
	beforeTxA, beforeTxB := pathA.txBytes.Load(), pathB.txBytes.Load()
	unknown := &udpEndpoint{} // a well-typed endpoint never registered in peerByVirt
	if err := m.Send([][]byte{[]byte("nowhere")}, unknown); err == nil {
		t.Fatal("Send to an unknown endpoint succeeded, want an error")
	}
	if got := peerA.outerSeq.Load(); got != beforeA {
		t.Fatalf("peer A outerSeq changed to %d on an unknown-endpoint Send (want %d)", got, beforeA)
	}
	if got := peerB.outerSeq.Load(); got != beforeB {
		t.Fatalf("peer B outerSeq changed to %d on an unknown-endpoint Send (want %d)", got, beforeB)
	}
	if got := pathA.txBytes.Load(); got != beforeTxA {
		t.Fatalf("peer A txBytes changed to %d on an unknown-endpoint Send (want %d)", got, beforeTxA)
	}
	if got := pathB.txBytes.Load(); got != beforeTxB {
		t.Fatalf("peer B txBytes changed to %d on an unknown-endpoint Send (want %d)", got, beforeTxB)
	}
	expectNoUDP(t, recvA, "peer A's remote (after unknown-endpoint Send)")
	expectNoUDP(t, recvB, "peer B's remote (after unknown-endpoint Send)")
}
