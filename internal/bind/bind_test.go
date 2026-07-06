package bind

import (
	"bytes"
	"encoding/base64"
	"net"
	"net/netip"
	"syscall"
	"testing"

	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/frame"
)

// testKey builds a valid 32-byte config.Key seeded by b.
func testKey(t testing.TB, b byte) config.Key {
	t.Helper()
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = b ^ byte(i)
	}
	var k config.Key
	if err := k.UnmarshalText([]byte(base64.StdEncoding.EncodeToString(raw))); err != nil {
		t.Fatalf("build key: %v", err)
	}
	return k
}

// loopbackPaths returns n paths all bound to 127.0.0.1 (distinct sockets, random
// ports).
func loopbackPaths(n int) []config.Path {
	paths := make([]config.Path, n)
	for i := range paths {
		paths[i] = config.Path{Name: string(rune('a' + i)), SourceAddr: netip.MustParseAddr("127.0.0.1")}
	}
	return paths
}

// sendDataTo encodes a DATA frame under psk and sends it from a fresh client
// socket to dst, returning the client's local AddrPort so the test can assert the
// path learned it.
func sendDataTo(t testing.TB, psk config.Key, dst *net.UDPAddr, pathID uint8, payload []byte) netip.AddrPort {
	t.Helper()
	cl, err := net.DialUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")}, dst)
	if err != nil {
		t.Fatalf("dial %s: %v", dst, err)
	}
	defer cl.Close()
	codec, err := frame.NewCodec(psk)
	if err != nil {
		t.Fatalf("new codec: %v", err)
	}
	wire, err := codec.Encode(nil, frame.Data{OuterSeq: uint64(pathID), PathID: pathID, Payload: payload})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if _, err := cl.Write(wire); err != nil {
		t.Fatalf("write: %v", err)
	}
	return cl.LocalAddr().(*net.UDPAddr).AddrPort()
}

// TestMultipathVirtualEndpointIdentity is the core §3 invariant: N per-path
// sockets deliver received datagrams under ONE stable virtual endpoint, so the
// engine never observes per-packet endpoint churn. It also checks the per-path
// endpoint bookkeeping: each path learns its sender's remote.
func TestMultipathVirtualEndpointIdentity(t *testing.T) {
	psk := testKey(t, 0x5A)
	m, err := NewMultipath(loopbackPaths(2), psk)
	if err != nil {
		t.Fatalf("NewMultipath: %v", err)
	}
	fns, _, err := m.Open(0)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	if len(fns) != 2 {
		t.Fatalf("got %d receive funcs, want 2", len(fns))
	}

	payload := []byte("opaque-wireguard-datagram")
	var gotEps []Endpoint
	var learned []netip.AddrPort
	for i, fn := range fns {
		dst := m.paths[i].conn.LocalAddr().(*net.UDPAddr)
		clientAP := sendDataTo(t, psk, dst, uint8(i), payload)
		learned = append(learned, clientAP)

		bufs := [][]byte{make([]byte, 2048)}
		sizes := make([]int, 1)
		eps := make([]Endpoint, 1)
		n, err := fn(bufs, sizes, eps)
		if err != nil {
			t.Fatalf("path %d receive: %v", i, err)
		}
		if n != 1 {
			t.Fatalf("path %d: got n=%d, want 1", i, n)
		}
		if !bytes.Equal(bufs[0][:sizes[0]], payload) {
			t.Fatalf("path %d: inner payload = %q, want %q", i, bufs[0][:sizes[0]], payload)
		}
		gotEps = append(gotEps, eps[0])
	}

	// Virtual-endpoint identity: both paths returned the SAME endpoint object.
	if gotEps[0] != gotEps[1] {
		t.Fatalf("per-path receives returned different endpoints (%v vs %v): virtual-endpoint identity violated",
			gotEps[0].DstToString(), gotEps[1].DstToString())
	}

	// Per-path bookkeeping: each path learned its own sender's remote.
	for i := range m.paths {
		remote, ok := m.paths[i].getRemote()
		if !ok {
			t.Fatalf("path %d did not learn a remote", i)
		}
		if remote != learned[i] {
			t.Fatalf("path %d learned remote %v, want %v", i, remote, learned[i])
		}
	}
}

// TestMultipathParseEndpointStable verifies ParseEndpoint returns the same stable
// virtual endpoint (pointer identity via ==) and seeds every path's default
// remote so the edge can send immediately after Open.
func TestMultipathParseEndpointStable(t *testing.T) {
	psk := testKey(t, 0x11)
	m, err := NewMultipath(loopbackPaths(2), psk)
	if err != nil {
		t.Fatalf("NewMultipath: %v", err)
	}
	fns, _, err := m.Open(0)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	_ = fns

	ep1, err := m.ParseEndpoint("203.0.113.5:51820")
	if err != nil {
		t.Fatalf("ParseEndpoint: %v", err)
	}
	ep2, err := m.ParseEndpoint("203.0.113.5:51820")
	if err != nil {
		t.Fatalf("ParseEndpoint: %v", err)
	}
	if ep1 != ep2 {
		t.Fatal("ParseEndpoint returned different endpoints: identity not stable")
	}
	if ep1.DstToString() != "203.0.113.5:51820" {
		t.Fatalf("virtual endpoint dst = %q, want 203.0.113.5:51820", ep1.DstToString())
	}
	// Every path now has the default remote and would be pickable.
	for i := range m.paths {
		if _, ok := m.paths[i].getRemote(); !ok {
			t.Fatalf("path %d has no remote after ParseEndpoint", i)
		}
	}
}

// TestMultipathParseEndpointBeforeOpen exercises the real engine ordering: UAPI
// config (ParseEndpoint) is applied BEFORE the bind is opened. The stashed
// default remote must reach the paths at Open.
func TestMultipathParseEndpointBeforeOpen(t *testing.T) {
	psk := testKey(t, 0x22)
	m, err := NewMultipath(loopbackPaths(1), psk)
	if err != nil {
		t.Fatalf("NewMultipath: %v", err)
	}
	if _, err := m.ParseEndpoint("198.51.100.7:1234"); err != nil {
		t.Fatalf("ParseEndpoint: %v", err)
	}
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	remote, ok := m.paths[0].getRemote()
	if !ok || remote.String() != "198.51.100.7:1234" {
		t.Fatalf("path remote = %v (ok=%v), want 198.51.100.7:1234", remote, ok)
	}
}

// TestMultipathDestAddrOverridesDefault confirms a path's configured dest_addr
// takes precedence over the peer endpoint default.
func TestMultipathDestAddrOverridesDefault(t *testing.T) {
	psk := testKey(t, 0x33)
	paths := loopbackPaths(2)
	paths[1].DestAddr = netip.MustParseAddrPort("192.0.2.9:9999")
	m, err := NewMultipath(paths, psk)
	if err != nil {
		t.Fatalf("NewMultipath: %v", err)
	}
	if _, err := m.ParseEndpoint("203.0.113.5:51820"); err != nil {
		t.Fatalf("ParseEndpoint: %v", err)
	}
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	r0, _ := m.paths[0].getRemote()
	if r0.String() != "203.0.113.5:51820" {
		t.Errorf("path 0 remote = %v, want the peer-endpoint default", r0)
	}
	r1, _ := m.paths[1].getRemote()
	if r1.String() != "192.0.2.9:9999" {
		t.Errorf("path 1 remote = %v, want its dest_addr override", r1)
	}
}

// TestMultipathSendPicksHealthyPath round-trips a datagram: with only path 1
// carrying a remote, Send must route over path 1 (first healthy path with a known
// remote), wrap it in a DATA frame, and the wire must decode back to the payload.
func TestMultipathSendRoutesAndFrames(t *testing.T) {
	psk := testKey(t, 0x44)
	m, err := NewMultipath(loopbackPaths(2), psk)
	if err != nil {
		t.Fatalf("NewMultipath: %v", err)
	}
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	// A receiver socket standing in for the remote peer.
	peer, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatalf("listen peer: %v", err)
	}
	defer peer.Close()
	peerAP := peer.LocalAddr().(*net.UDPAddr).AddrPort()

	// Only path 0 gets a remote → Send must choose it.
	m.paths[0].setRemote(peerAP)

	payload := []byte("inner-wg-bytes")
	if err := m.Send([][]byte{payload}, m.virt); err != nil {
		t.Fatalf("Send: %v", err)
	}

	buf := make([]byte, maxDatagram)
	n, err := peer.Read(buf)
	if err != nil {
		t.Fatalf("peer read: %v", err)
	}
	codec, _ := frame.NewCodec(psk)
	fr, err := codec.Decode(buf[:n])
	if err != nil {
		t.Fatalf("decode wire: %v", err)
	}
	data, ok := fr.(frame.Data)
	if !ok {
		t.Fatalf("wire frame is %T, want frame.Data", fr)
	}
	if data.PathID != 0 {
		t.Errorf("DATA path-id = %d, want 0", data.PathID)
	}
	if data.OuterSeq == 0 {
		t.Errorf("DATA outer-seq = 0, want a populated own sequence")
	}
	if !bytes.Equal(data.Payload, payload) {
		t.Errorf("DATA payload = %q, want %q", data.Payload, payload)
	}
}

// TestMultipathSendNoHealthyPath: with no path holding a remote, Send fails
// rather than silently dropping.
func TestMultipathSendNoHealthyPath(t *testing.T) {
	psk := testKey(t, 0x55)
	m, err := NewMultipath(loopbackPaths(1), psk)
	if err != nil {
		t.Fatalf("NewMultipath: %v", err)
	}
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	if err := m.Send([][]byte{[]byte("x")}, m.virt); err == nil {
		t.Fatal("Send with no remote-bearing path succeeded, want error")
	}
}

// TestMultipathWrongEndpointType: Send rejects a foreign endpoint type.
func TestMultipathWrongEndpointType(t *testing.T) {
	psk := testKey(t, 0x66)
	m, err := NewMultipath(loopbackPaths(1), psk)
	if err != nil {
		t.Fatalf("NewMultipath: %v", err)
	}
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	if err := m.Send([][]byte{[]byte("x")}, notOurEndpoint{}); err == nil {
		t.Fatal("Send accepted a foreign endpoint type, want ErrWrongEndpointType")
	}
}

type notOurEndpoint struct{}

func (notOurEndpoint) ClearSrc()           {}
func (notOurEndpoint) SrcToString() string { return "" }
func (notOurEndpoint) DstToString() string { return "" }
func (notOurEndpoint) DstToBytes() []byte  { return nil }
func (notOurEndpoint) DstIP() netip.Addr   { return netip.Addr{} }
func (notOurEndpoint) SrcIP() netip.Addr   { return netip.Addr{} }

// TestMultipathBatchSizePositive is a contract check.
func TestMultipathBatchSizePositive(t *testing.T) {
	psk := testKey(t, 0x77)
	m, err := NewMultipath(loopbackPaths(1), psk)
	if err != nil {
		t.Fatalf("NewMultipath: %v", err)
	}
	if m.BatchSize() < 1 {
		t.Fatalf("BatchSize = %d, want >= 1", m.BatchSize())
	}
}

// TestMultipathLargeRecvBuffer asserts each per-path socket's SO_RCVBUF is at
// least as large as a plain socket's default — i.e. our SetReadBuffer request did
// not SHRINK it and took effect to the extent the kernel allows. The kernel caps
// the 7 MiB request at net.core.rmem_max (often smaller in CI/namespaces), so the
// test asserts a lower bound rather than the exact requested size and logs both.
func TestMultipathLargeRecvBuffer(t *testing.T) {
	psk := testKey(t, 0x88)
	m, err := NewMultipath(loopbackPaths(1), psk)
	if err != nil {
		t.Fatalf("NewMultipath: %v", err)
	}
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	got := rcvBuf(t, m.paths[0].conn)

	plain, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatalf("listen plain: %v", err)
	}
	defer plain.Close()
	def := rcvBuf(t, plain)

	t.Logf("per-path SO_RCVBUF=%d bytes (requested %d), plain-default SO_RCVBUF=%d bytes", got, socketRecvBuffer, def)
	if got < def {
		t.Fatalf("per-path SO_RCVBUF %d < plain default %d: SetReadBuffer shrank the buffer", got, def)
	}
	if got <= 0 {
		t.Fatalf("per-path SO_RCVBUF is non-positive: %d", got)
	}
}

// rcvBuf reads SO_RCVBUF off a UDP socket via its raw file descriptor.
func rcvBuf(t *testing.T, c *net.UDPConn) int {
	t.Helper()
	raw, err := c.SyscallConn()
	if err != nil {
		t.Fatalf("SyscallConn: %v", err)
	}
	var val int
	var sockErr error
	if err := raw.Control(func(fd uintptr) {
		val, sockErr = syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF)
	}); err != nil {
		t.Fatalf("raw control: %v", err)
	}
	if sockErr != nil {
		t.Fatalf("getsockopt SO_RCVBUF: %v", sockErr)
	}
	return val
}
