package bind

import (
	"bytes"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/netip"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/frame"
	"github.com/7mind/wanbond/internal/log"
	"github.com/7mind/wanbond/internal/sched"
	"github.com/7mind/wanbond/internal/telemetry"
)

// newMultipath builds a Multipath over paths with a default active-backup
// scheduler whose paths are all statically Up, so path selection reduces to the
// preferred primary (index 0) — the T12 send behaviour these tests assert. The
// scheduler's own failover/hysteresis logic is exercised in internal/sched. It
// passes no probers, so the probe transport is inert here (exercised separately
// in probe_test.go).
func newMultipath(t testing.TB, paths []config.Path, psk config.Key) (*Multipath, error) {
	t.Helper()
	health := make([]sched.PathHealth, len(paths))
	for i := range health {
		health[i] = sched.AlwaysUp{}
	}
	lg, err := log.New("error", io.Discard)
	if err != nil {
		t.Fatalf("build logger: %v", err)
	}
	scheduler, err := sched.NewActiveBackup(health, sched.Config{FailbackAfter: time.Second}, telemetry.SystemClock{}, lg)
	if err != nil {
		t.Fatalf("build scheduler: %v", err)
	}
	return NewMultipath(paths, psk, scheduler, nil, nil, nil, nil, config.Amnezia{}, lg)
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
// engine never observes per-packet endpoint churn.
//
// Note it does NOT assert per-path remote learning from these DATA frames: after
// D9, remote-learning is authenticated-only (from PROBE/echo frames), so a DATA
// frame no longer teaches a path its return remote — that is asserted in
// probe_test.go (TestMultipathRemoteLearnedFromProbeNotData).
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

	// Send a DATA frame to EACH path's socket (OuterSeq i on path i); the Bind-owned
	// readers read them and feed the shared resequencer, and the single drainer
	// delivers both — under the SAME virtual endpoint.
	//
	// INTERLEAVE send+receive per path (defect D19): the two paths are read by
	// INDEPENDENT reader goroutines with NO cross-path arrival ordering. If both
	// frames were sent up front and OuterSeq 1 happened to be observed first, the
	// resequencer would pin its release point to 1 and drop the later OuterSeq 0 as
	// late — the second receive would then block forever. Sending OuterSeq i and
	// receiving it before sending OuterSeq i+1 pins the release point deterministically
	// (0 then 1), so both frames are delivered in order regardless of reader scheduling.
	payload := []byte("opaque-wireguard-datagram")
	var gotEps []Endpoint
	for i := 0; i < 2; i++ {
		dst := m.paths[i].conn.LocalAddr().(*net.UDPAddr)
		sendDataTo(t, psk, dst, uint8(i), payload)

		bufs := [][]byte{make([]byte, 2048)}
		sizes := make([]int, 1)
		eps := make([]Endpoint, 1)
		n, err := fn(bufs, sizes, eps)
		if err != nil {
			t.Fatalf("receive %d: %v", i, err)
		}
		if n != 1 {
			t.Fatalf("receive %d: got n=%d, want 1", i, n)
		}
		if !bytes.Equal(bufs[0][:sizes[0]], payload) {
			t.Fatalf("receive %d: inner payload = %q, want %q", i, bufs[0][:sizes[0]], payload)
		}
		gotEps = append(gotEps, eps[0])
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

// TestMultipathSendPicksHealthyPath round-trips a datagram: with only path 1
// carrying a remote, Send must route over path 1 (first healthy path with a known
// remote), wrap it in a DATA frame, and the wire must decode back to the payload.
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
	m, err := newMultipath(t, loopbackPaths(1), psk)
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

// stubScheduler returns a fixed Pick value (and a no-op Recompute), to exercise
// Send's negative-sentinel mapping without a live liveness machine.
type stubScheduler struct{ pick int }

func (s stubScheduler) Pick(_ sched.FrameClass, _ int) int { return s.pick }
func (s stubScheduler) Recompute()                         {}
func (s stubScheduler) DataPaths() []sched.DataPath        { return nil }

// TestMultipathSendPacerSheddingDistinct: a PickPaced shed (paths healthy, rate
// limited) maps to errPacerShedding, DISTINCT from the ErrNoHealthyPath a PickNone
// outage yields, so operator logs and the e2e log-grep harness can tell deliberate
// pacer shedding from total path failure (criticism #3). The drop behavior is
// identical (Send returns an error and the datagram is not sent) — only the error, and
// thus the diagnostic, differs.
func TestMultipathSendPacerSheddingDistinct(t *testing.T) {
	psk := testKey(t, 0x5A)
	cases := []struct {
		name string
		pick int
		want error
	}{
		{"paced shed", sched.PickPaced, errPacerShedding},
		{"no eligible path", sched.PickNone, ErrNoHealthyPath},
	}
	lg, err := log.New("error", io.Discard)
	if err != nil {
		t.Fatalf("build logger: %v", err)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, err := NewMultipath(loopbackPaths(1), psk, stubScheduler{pick: tc.pick}, nil, nil, nil, nil, config.Amnezia{}, lg)
			if err != nil {
				t.Fatalf("NewMultipath: %v", err)
			}
			if _, _, err := m.Open(0); err != nil {
				t.Fatalf("Open: %v", err)
			}
			t.Cleanup(func() { _ = m.Close() })
			err = m.Send([][]byte{[]byte("x")}, m.virt)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Send with pick=%d returned %v, want %v", tc.pick, err, tc.want)
			}
			// The shed error must NOT be conflated with the outage error (distinct
			// sentinels), so a paced shed never reads as no-healthy-path.
			if tc.pick == sched.PickPaced && errors.Is(err, ErrNoHealthyPath) {
				t.Fatal("pacer shedding conflated with no-healthy-path outage")
			}
		})
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
// and panicking on the next Open at m.probers[i]. NewMultipath must reject the pairing.
func TestNewMultipathRejectsUnpairedProberFactory(t *testing.T) {
	psk := testKey(t, 0x37)
	paths := loopbackPaths(1)
	health := []sched.PathHealth{sched.AlwaysUp{}}
	lg, err := log.New("error", io.Discard)
	if err != nil {
		t.Fatalf("build logger: %v", err)
	}
	scheduler, err := sched.NewActiveBackup(health, sched.Config{FailbackAfter: time.Second}, telemetry.SystemClock{}, lg)
	if err != nil {
		t.Fatalf("build scheduler: %v", err)
	}
	factory := func(name string, id uint8, _ time.Duration) *telemetry.Prober { return nil }
	if _, err := NewMultipath(paths, psk, scheduler, nil, factory, nil, nil, config.Amnezia{}, lg); err == nil {
		t.Fatal("NewMultipath(newProber!=nil, probers==nil) succeeded, want rejection")
	}
}
