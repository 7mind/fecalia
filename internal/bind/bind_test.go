package bind

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"maps"
	"net"
	"net/netip"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/bond"
	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/frame"
	"github.com/7mind/wanbond/internal/log"
	"github.com/7mind/wanbond/internal/telemetry"
)

// newMultipath builds a Multipath over paths with one wall-clock prober per path and
// no runtime prober factory, so nothing drives the probers unless a test does: the
// probe transport is exercised separately in probe_test.go.
func newMultipath(t testing.TB, paths []config.Path, psk config.Key) (*Multipath, error) {
	t.Helper()
	lg, err := log.New("error", io.Discard)
	if err != nil {
		t.Fatalf("build logger: %v", err)
	}
	probers := make([]*telemetry.Prober, len(paths))
	for i := range paths {
		probers[i] = telemetry.NewProber(paths[i].Name, uint8(i), testProbeSessionID, psk, telemetry.ProberConfig{}, telemetry.SystemClock{}, lg)
	}
	return NewMultipath(paths, psk, probers, nil, lg)
}

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

// remoteTransport stands in for the adaptive transport of the process at the far end of
// one bind peer. It builds the data frames that process would send, in the layout of
// internal/bond/wire.go, so a test chooses the lane, the arrival order and the class of
// every datagram; a real bond.Transport paces and waits for acknowledgements instead.
type remoteTransport struct {
	t     testing.TB
	local *adaptivePeer
	epoch bond.Epoch
	codec *frame.Codec
	// path is the far end's own path id: the one its hellos announce and its lanes are
	// numbered by.
	path uint8
	sent *remoteSequences
}

// remoteSequences is the sequence state of one remote process, shared by all its paths.
type remoteSequences struct {
	attempts   map[bond.PathID]uint64
	seq        uint64
	bulkOrder  uint64
	smallOrder uint64
}

// remoteInteractiveBit marks a delivery order as belonging to the small-datagram class.
const remoteInteractiveBit uint64 = 1 << 63

// newRemoteTransport returns the far end of peer's transport, running as process boot
// and sending from its path 0. The bind must be open.
func newRemoteTransport(t testing.TB, peer *peerState, boot uint64) *remoteTransport {
	t.Helper()
	local := peer.adaptive.Load()
	if local == nil {
		t.Fatal("peer has no open transport")
	}
	codec, err := peer.newCodec()
	if err != nil {
		t.Fatalf("new codec: %v", err)
	}
	return &remoteTransport{
		t:     t,
		local: local,
		epoch: bond.Epoch{Boot: boot, Generation: 1},
		codec: codec,
		sent:  &remoteSequences{attempts: make(map[bond.PathID]uint64)},
	}
}

// onPath returns the same process sending from another of its paths.
func (r *remoteTransport) onPath(path uint8) *remoteTransport {
	c := *r
	c.path = path
	return &c
}

// clone returns a copy that continues from the same sequence state. A frame the bind is
// expected to drop before the transport sees it is built from a clone, so it consumes no
// sequence of the original: had it not been dropped, it would be delivered.
func (r *remoteTransport) clone() *remoteTransport {
	c := *r
	sent := *r.sent
	sent.attempts = maps.Clone(r.sent.attempts)
	c.sent = &sent
	return &c
}

func (r *remoteTransport) hello() []byte {
	return bond.Hello(r.epoch, r.path)
}

// join makes the local transport learn a lane to this process on view at src, as the
// adopted hello of an authenticated probe exchange does. Repeating it renews the lane.
func (r *remoteTransport) join(view *peerPathState, src netip.AddrPort) {
	r.local.learn(view, src, r.hello(), true)
}

func (r *remoteTransport) data(view *peerPathState, order uint64, payload []byte) frame.Control {
	lane := bond.PathID(uint16(r.path)<<8 | uint16(view.id))
	r.sent.attempts[lane]++
	r.sent.seq++
	local := r.local.transport.Epoch()
	b := []byte{bond.Version}
	for _, value := range []uint64{r.epoch.Boot, r.epoch.Generation} {
		b = binary.BigEndian.AppendUint64(b, value)
	}
	b = binary.BigEndian.AppendUint16(b, uint16(lane))
	for _, value := range []uint64{local.Boot, local.Generation, r.sent.seq, order} {
		b = binary.BigEndian.AppendUint64(b, value)
	}
	return frame.Control{ControlType: bond.DataType, Seq: r.sent.attempts[lane], Payload: append(b, payload...)}
}

// bulk is the next datagram of the ordered class on view's lane: the receiver holds it
// in the resequencer until every earlier one has arrived or its gap has expired.
func (r *remoteTransport) bulk(view *peerPathState, payload []byte) frame.Control {
	r.sent.bulkOrder++
	return r.data(view, r.sent.bulkOrder, payload)
}

// small is the next datagram of the small class on view's lane: the receiver delivers
// it at once.
func (r *remoteTransport) small(view *peerPathState, payload []byte) frame.Control {
	r.sent.smallOrder++
	return r.data(view, r.sent.smallOrder|remoteInteractiveBit, payload)
}

func (r *remoteTransport) wire(f frame.Control) []byte {
	r.t.Helper()
	raw, err := r.codec.Encode(nil, f)
	if err != nil {
		r.t.Fatalf("encode transport frame: %v", err)
	}
	return raw
}

// dialPath returns a client socket connected to view's socket, and its local address.
func dialPath(t testing.TB, view *peerPathState) (*net.UDPConn, netip.AddrPort) {
	t.Helper()
	dst := view.conn.LocalAddr().(*net.UDPAddr)
	cl, err := net.DialUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")}, dst)
	if err != nil {
		t.Fatalf("dial %s: %v", dst, err)
	}
	t.Cleanup(func() { _ = cl.Close() })
	return cl, cl.LocalAddr().(*net.UDPAddr).AddrPort()
}

// receiveOne blocks on the engine-facing receive func for one datagram.
func receiveOne(t testing.TB, fn ReceiveFunc) ([]byte, Endpoint) {
	t.Helper()
	bufs := [][]byte{make([]byte, 2048)}
	sizes := make([]int, 1)
	eps := make([]Endpoint, 1)
	n, err := fn(bufs, sizes, eps)
	if err != nil {
		t.Fatalf("receive: %v", err)
	}
	if n != 1 {
		t.Fatalf("receive: got n=%d, want 1", n)
	}
	return bufs[0][:sizes[0]], eps[0]
}

// readTransportData reads peer until it holds a transport data frame carrying a
// datagram, skipping the transport's lane keepalives and acknowledgements, and
// returns the frame with its on-wire size.
func readTransportData(t testing.TB, peer *net.UDPConn, codec *frame.Codec) (frame.Control, int) {
	t.Helper()
	if err := peer.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	buf := make([]byte, maxDatagram)
	for {
		n, err := peer.Read(buf)
		if err != nil {
			t.Fatalf("read transport data: %v", err)
		}
		fr, err := codec.Decode(buf[:n])
		if err != nil {
			t.Fatalf("decode wire: %v", err)
		}
		control, ok := fr.(frame.Control)
		if ok && control.ControlType == bond.DataType && n > bond.Overhead {
			return control, n
		}
	}
}

// TestMultipathVirtualEndpointIdentity is the core §3 invariant: N per-path
// sockets deliver received datagrams under ONE stable virtual endpoint, so the
// engine never observes per-packet endpoint churn.
func TestMultipathVirtualEndpointIdentity(t *testing.T) {
	psk := testKey(t, 0x5A)
	m, err := newMultipath(t, loopbackPaths(2), psk)
	if err != nil {
		t.Fatalf("NewMultipath: %v", err)
	}
	// The fan-in receive model (T30) returns a SINGLE engine-facing ReceiveFunc
	// regardless of path count: the Bind owns one reader per path internally, and the
	// one drainer delivers every path's frames under the one virtual endpoint.
	fns, _, err := m.Open(0)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	if len(fns) != 1 {
		t.Fatalf("got %d receive funcs, want 1 (fan-in delivery)", len(fns))
	}
	fn := fns[0]

	// Send a transport datagram to EACH path's socket, each from its own source; the
	// Bind-owned readers hand them to the transport, and the single drainer delivers
	// both — under the SAME virtual endpoint.
	remote := newRemoteTransport(t, m.peerState, 987)
	payload := []byte("opaque-wireguard-datagram")
	var gotEps []Endpoint
	for i := 0; i < 2; i++ {
		cl, src := dialPath(t, m.paths[i])
		remote.join(m.paths[i], src)
		if _, err := cl.Write(remote.wire(remote.small(m.paths[i], payload))); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
		got, ep := receiveOne(t, fn)
		if !bytes.Equal(got, payload) {
			t.Fatalf("receive %d: inner payload = %q, want %q", i, got, payload)
		}
		gotEps = append(gotEps, ep)
	}

	// Virtual-endpoint identity: both delivered frames carried the SAME endpoint.
	if gotEps[0] != gotEps[1] {
		t.Fatalf("deliveries returned different endpoints (%v vs %v): virtual-endpoint identity violated",
			gotEps[0].DstToString(), gotEps[1].DstToString())
	}
}

// TestMultipathVirtualEndpointDstRace is the regression for the reproduced data
// race: the Bind pins the virtual endpoint's destination (via virtualEndpoint /
// ParseEndpoint) from receive goroutines while the engine reads that same field
// locklessly through the Dst* accessors. It pins from multiple goroutines while
// others hammer DstToBytes/DstToString/DstIP; under `go test -race` a plain
// (non-atomic) dst field trips the detector deterministically, and the atomic
// pointer makes it clean. T15's cross-path scheduling activates exactly this
// concurrency, so it is guarded here on the object T12 delivers.
func TestMultipathVirtualEndpointDstRace(t *testing.T) {
	psk := testKey(t, 0x99)
	m, err := newMultipath(t, loopbackPaths(4), psk)
	if err != nil {
		t.Fatalf("NewMultipath: %v", err)
	}

	const (
		writers = 4
		readers = 4
		iters   = 2000
	)
	var readersWg, writersWg sync.WaitGroup
	stop := make(chan struct{})

	// Readers: the engine-facing lockless accessors, spinning until stopped.
	for i := 0; i < readers; i++ {
		readersWg.Add(1)
		go func() {
			defer readersWg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_ = m.virt.DstToBytes()
				_ = m.virt.DstToString()
				_ = m.virt.DstIP()
			}
		}()
	}

	// Writers: pin the endpoint concurrently via the real Bind path (each with a
	// distinct address) plus a direct setDst hammer, so repeated writes overlap
	// the reads regardless of the once-guard.
	for i := 0; i < writers; i++ {
		writersWg.Add(1)
		go func(id int) {
			defer writersWg.Done()
			ap := netip.AddrPortFrom(netip.AddrFrom4([4]byte{10, 0, 0, byte(id + 1)}), uint16(1000+id))
			for j := 0; j < iters; j++ {
				m.virtualEndpoint(m.peerState, ap) // real pin path (guarded, publishes atomically)
				m.virt.setDst(ap)                  // direct hammer to stress the accessor
			}
		}(i)
	}

	writersWg.Wait() // writers finish; then release the readers
	close(stop)
	readersWg.Wait()

	// Sanity: after all the pinning, the endpoint has a valid destination.
	if !m.virt.dstValid() {
		t.Fatal("virtual endpoint destination never pinned")
	}
}

// TestMultipathParseEndpointStable verifies ParseEndpoint returns the same stable
// virtual endpoint (pointer identity via ==) and seeds every path's default
// remote so the edge can send immediately after Open.
func TestMultipathParseEndpointStable(t *testing.T) {
	psk := testKey(t, 0x11)
	m, err := newMultipath(t, loopbackPaths(2), psk)
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
	m, err := newMultipath(t, loopbackPaths(1), psk)
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
	m, err := newMultipath(t, paths, psk)
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

// TestMultipathSendRoutesAndFrames round-trips a datagram: Send hands it to the
// transport, which sends it over the lane a hello established, wrapped in a transport
// data frame of exactly bond.Overhead bytes, and the wire decodes back to the payload.
func TestMultipathSendRoutesAndFrames(t *testing.T) {
	psk := testKey(t, 0x44)
	m, err := newMultipath(t, loopbackPaths(2), psk)
	if err != nil {
		t.Fatalf("NewMultipath: %v", err)
	}
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	// A receiver socket standing in for the remote peer.
	peer, peerAP := rawPeer(t)

	// Only path 0 gets a lane → the transport must send over it.
	newRemoteTransport(t, m.peerState, 987).join(m.paths[0], peerAP)

	payload := []byte("inner-wg-bytes")
	if err := m.Send([][]byte{payload}, m.virt); err != nil {
		t.Fatalf("Send: %v", err)
	}

	codec, _ := frame.NewCodec(psk)
	data, onWire := readTransportData(t, peer, codec)
	if data.Seq == 0 {
		t.Errorf("data frame lane sequence = 0, want a populated own sequence")
	}
	if !bytes.HasSuffix(data.Payload, payload) {
		t.Errorf("data frame payload = %x, want it to end in %q", data.Payload, payload)
	}
	if onWire != len(payload)+bond.Overhead {
		t.Errorf("data frame is %d bytes on the wire, want payload %d + bond.Overhead %d", onWire, len(payload), bond.Overhead)
	}
	if got := m.paths[1].txBytes.Load(); got != 0 {
		t.Errorf("path 1 without a lane wrote %d bytes, want 0", got)
	}
}

// TestMultipathSendWaitsForHello: a closed bind refuses a Send rather than silently
// dropping it; an open one accepts it before any lane exists, writes nothing, and sends
// the waiting datagram once a hello has established a lane.
func TestMultipathSendWaitsForHello(t *testing.T) {
	psk := testKey(t, 0x55)
	m, err := newMultipath(t, loopbackPaths(1), psk)
	if err != nil {
		t.Fatalf("NewMultipath: %v", err)
	}
	payload := bytes.Repeat([]byte{0x5A}, 1000)
	if err := m.Send([][]byte{payload}, m.virt); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("Send on a bind that was never opened = %v, want net.ErrClosed", err)
	}
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	peer, peerAP := rawPeer(t)
	m.paths[0].setRemote(peerAP)
	if err := m.Send([][]byte{payload}, m.virt); err != nil {
		t.Fatalf("Send before the first hello: %v", err)
	}
	if got := m.paths[0].txBytes.Load(); got != 0 {
		t.Fatalf("path wrote %d bytes before any hello established a lane, want 0", got)
	}

	newRemoteTransport(t, m.peerState, 987).join(m.paths[0], peerAP)
	codec, _ := frame.NewCodec(psk)
	if data, _ := readTransportData(t, peer, codec); !bytes.HasSuffix(data.Payload, payload) {
		t.Fatalf("datagram sent after the hello does not carry the waiting payload")
	}

	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := m.Send([][]byte{payload}, m.virt); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("Send on a closed bind = %v, want net.ErrClosed", err)
	}
}

// TestMultipathWrongEndpointType: Send rejects a foreign endpoint type.
func TestMultipathWrongEndpointType(t *testing.T) {
	psk := testKey(t, 0x66)
	m, err := newMultipath(t, loopbackPaths(1), psk)
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
	m, err := newMultipath(t, loopbackPaths(1), psk)
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
	m, err := newMultipath(t, loopbackPaths(1), psk)
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

// TestNewMultipathRejectsUnpairedProberFactory pins the constructor pairing invariant
// (fable low defect): a runtime-path factory (newProber) without a boot-time prober
// slice would let AddPath append to a nil m.probers, desyncing m.paths from m.probers
// and panicking on the next Open at m.probers[i]. NewMultipath must reject the pairing,
// as it rejects any prober set that does not hold one prober per path.
func TestNewMultipathRejectsUnpairedProberFactory(t *testing.T) {
	psk := testKey(t, 0x37)
	paths := loopbackPaths(1)
	lg, err := log.New("error", io.Discard)
	if err != nil {
		t.Fatalf("build logger: %v", err)
	}
	factory := func(name string, id uint8, _ time.Duration) *telemetry.Prober { return nil }
	if _, err := NewMultipath(paths, psk, nil, factory, lg); err == nil {
		t.Fatal("NewMultipath(newProber!=nil, probers==nil) succeeded, want rejection")
	}
	if _, err := NewMultipath(paths, psk, []*telemetry.Prober{nil}, factory, lg); err == nil {
		t.Fatal("NewMultipath with a nil prober succeeded, want rejection")
	}
}
