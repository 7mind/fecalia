package bind

import (
	"bytes"
	"crypto/rand"
	"io"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/frame"
	"github.com/7mind/wanbond/internal/log"
	"github.com/7mind/wanbond/internal/sched"
	"github.com/7mind/wanbond/internal/telemetry"
)

// fakeClock is a hand-advanced telemetry.Clock. The probe-transport tests drive
// emitProbes / handleInbound synchronously on a single goroutine, so those tests
// need no internal synchronization. The adaptive tests, however, wire this SAME
// clock into the bind's clock seam (Multipath.clock), where the background
// fecTickLoop goroutine reads Now() concurrently with the test goroutine's
// advance() — so the clock IS mutex-guarded (a plain now field would be a
// -race-flagged data race). In-repo precedent: internal/device/metrics_test.go's
// fakeClock. Liveness transitions remain deterministic under -race.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{now: time.Unix(1_700_000_000, 0)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// probeLiveness are the detection thresholds these tests use: short enough to
// keep the fake-clock arithmetic terse, with the same shape as production
// (DownAfter spans several intervals, UpAfterSuccesses > 1 for hysteresis).
const (
	testProbeInterval  = 100 * time.Millisecond
	testProbeDownAfter = 300 * time.Millisecond
	testProbeUpSucc    = 3
	testProbeRTT       = 10 * time.Millisecond
	// testProbeSessionID is the fixed per-boot probe session id these bind tests
	// stamp their Probers with (T38/D12); any consistent value works because each
	// Prober handles only echoes of its own probes.
	testProbeSessionID uint64 = 0xABCDEF0123456789
)

// newProbingMultipath builds a Multipath wired with one live *telemetry.Prober
// per path and a real active-backup scheduler over those SAME probers, plus the
// injected fake clock. FailbackAfter is set far beyond any test horizon so
// failback never interferes with a failover assertion.
func newProbingMultipath(t testing.TB, paths []config.Path, psk config.Key, clk telemetry.Clock) (*Multipath, []*telemetry.Prober, sched.Scheduler) {
	t.Helper()
	lg, err := log.New("error", io.Discard)
	if err != nil {
		t.Fatalf("build logger: %v", err)
	}
	cfg := telemetry.ProberConfig{
		LossWindow: 0,
		Liveness:   telemetry.LivenessConfig{DownAfter: testProbeDownAfter, UpAfterSuccesses: testProbeUpSucc},
	}
	newProber := func(name string, id uint8, _ time.Duration) *telemetry.Prober {
		return telemetry.NewProber(name, id, testProbeSessionID, psk, cfg, clk, lg)
	}
	probers := make([]*telemetry.Prober, len(paths))
	health := make([]sched.PathHealth, len(paths))
	for i := range paths {
		probers[i] = newProber(paths[i].Name, uint8(i), paths[i].RideThrough)
		health[i] = probers[i]
	}
	scheduler, err := sched.NewActiveBackup(health, sched.Config{FailbackAfter: time.Hour}, clk, lg)
	if err != nil {
		t.Fatalf("build scheduler: %v", err)
	}
	m, err := NewMultipath(paths, psk, scheduler, probers, newProber, nil, nil, config.Amnezia{}, lg)
	if err != nil {
		t.Fatalf("NewMultipath: %v", err)
	}
	return m, probers, scheduler
}

// rawPeer is a loopback UDP socket standing in for the remote (concentrator)
// end: the edge's per-path socket sends probes here, and the test reflects them.
func rawPeer(t testing.TB) (*net.UDPConn, netip.AddrPort) {
	t.Helper()
	c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatalf("listen raw peer: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c, c.LocalAddr().(*net.UDPAddr).AddrPort()
}

// readProbe reads one datagram off a raw peer socket and decodes it as a probe.
func readProbe(t testing.TB, peer *net.UDPConn, codec *frame.Codec) frame.Probe {
	t.Helper()
	if err := peer.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	buf := make([]byte, maxDatagram)
	n, err := peer.Read(buf)
	if err != nil {
		t.Fatalf("read probe: %v", err)
	}
	fr, err := codec.Decode(buf[:n])
	if err != nil {
		t.Fatalf("decode probe: %v", err)
	}
	probe, ok := fr.(frame.Probe)
	if !ok {
		t.Fatalf("emitted frame is %T, want frame.Probe", fr)
	}
	return probe
}

// TestMultipathReflectsProbe: an authenticated inbound PROBE (IsEcho=false) is
// reflected verbatim (same ProbeSeq/TimestampNanos, IsEcho now true) back to its
// source, and the path learns that source as its return remote (D11).
func TestMultipathReflectsProbe(t *testing.T) {
	psk := testKey(t, 0x21)
	clk := newFakeClock()
	m, _, _ := newProbingMultipath(t, loopbackPaths(1), psk, clk)
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	// A raw peer that will receive the reflected echo (its addr is the probe src).
	peer, peerAP := rawPeer(t)

	const seq = 7
	ts := clk.Now().UnixNano()
	raw, err := frame.Encode(psk, frame.Probe{PathID: 0, ProbeSeq: seq, TimestampNanos: ts, IsEcho: false})
	if err != nil {
		t.Fatalf("encode probe: %v", err)
	}
	m.handleInbound(m.paths[0], raw, peerAP)
	if it, ok := m.resequencer.Load().Pop(); ok {
		t.Fatalf("probe delivered up the WG path (payload %q), want dropped", it.Payload)
	}

	// The path learned the probe source as its remote — authenticated learning.
	if remote, ok := m.paths[0].getRemote(); !ok || remote != peerAP {
		t.Fatalf("path remote = %v (ok=%v), want %v learned from the probe", remote, ok, peerAP)
	}

	// The echo landed at the source: same seq/ts, IsEcho flipped true.
	codec, _ := frame.NewCodec(psk)
	echo := readProbe(t, peer, codec)
	if !echo.IsEcho {
		t.Fatalf("reflected frame IsEcho=false, want true")
	}
	if echo.ProbeSeq != seq || echo.TimestampNanos != ts || echo.PathID != 0 {
		t.Fatalf("reflected probe = %+v, want seq=%d ts=%d path=0 verbatim", echo, seq, ts)
	}
}

// TestMultipathRemoteLearnedFromProbeNotData is the D9 assertion: a DATA frame no
// longer teaches a path its return remote (unauthenticated learning removed), but
// an authenticated PROBE does.
func TestMultipathRemoteLearnedFromProbeNotData(t *testing.T) {
	psk := testKey(t, 0x22)
	clk := newFakeClock()
	m, _, _ := newProbingMultipath(t, loopbackPaths(1), psk, clk)
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	dataSrc := netip.MustParseAddrPort("192.0.2.10:4000")
	dataCodec, _ := frame.NewCodec(psk)
	dataRaw, err := dataCodec.Encode(nil, frame.Data{OuterSeq: 1, PathID: 0, Payload: []byte("wg")})
	if err != nil {
		t.Fatalf("encode data: %v", err)
	}
	m.handleInbound(m.paths[0], dataRaw, dataSrc)
	if it, ok := m.resequencer.Load().Pop(); !ok || !bytes.Equal(it.Payload, []byte("wg")) {
		t.Fatalf("DATA not resequenced up the WG path: ok=%v payload=%q, want %q", ok, it.Payload, "wg")
	}
	if _, ok := m.paths[0].getRemote(); ok {
		t.Fatal("path learned a remote from an unauthenticated DATA frame: D9 not resolved")
	}

	// An authenticated probe from a DIFFERENT source IS learned.
	probeSrc := netip.MustParseAddrPort("192.0.2.20:5000")
	probeRaw, err := frame.Encode(psk, frame.Probe{PathID: 0, ProbeSeq: 0, TimestampNanos: clk.Now().UnixNano()})
	if err != nil {
		t.Fatalf("encode probe: %v", err)
	}
	m.handleInbound(m.paths[0], probeRaw, probeSrc)
	if remote, ok := m.paths[0].getRemote(); !ok || remote != probeSrc {
		t.Fatalf("path remote = %v (ok=%v), want %v learned from the probe", remote, ok, probeSrc)
	}
}

// TestMultipathEchoFeedsProber: an inbound echo (IsEcho=true) is fed into the
// path's Prober, yielding an RTT sample — i.e. echo handling is wired to liveness.
func TestMultipathEchoFeedsProber(t *testing.T) {
	psk := testKey(t, 0x23)
	clk := newFakeClock()
	m, probers, _ := newProbingMultipath(t, loopbackPaths(1), psk, clk)
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	// Originate a probe through the path's own Prober, reflect it, then feed the
	// echo back through the receive path.
	probeRaw, err := probers[0].SendProbe()
	if err != nil {
		t.Fatalf("send probe: %v", err)
	}
	reflector := telemetry.NewReflector(psk, rand.Reader)
	echo, _, err := reflector.Reflect(probeRaw)
	if err != nil {
		t.Fatalf("reflect: %v", err)
	}
	clk.advance(testProbeRTT)

	src := netip.MustParseAddrPort("192.0.2.30:6000")
	m.handleInbound(m.paths[0], echo, src)
	if got := probers[0].Estimate().RTT; got != testProbeRTT {
		t.Fatalf("prober RTT after echo = %v, want %v (echo not fed into the prober)", got, testProbeRTT)
	}
}

// TestMultipathProbeLoopEmits: emitProbes sends exactly one PROBE per path to that
// path's remote each call, with a monotonically increasing per-path ProbeSeq.
func TestMultipathProbeLoopEmits(t *testing.T) {
	psk := testKey(t, 0x24)
	clk := newFakeClock()
	m, _, _ := newProbingMultipath(t, loopbackPaths(2), psk, clk)
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	peers := make([]*net.UDPConn, 2)
	for i := range peers {
		peer, peerAP := rawPeer(t)
		peers[i] = peer
		m.paths[i].setRemote(peerAP)
	}
	codec, _ := frame.NewCodec(psk)

	for tick := uint64(0); tick < 2; tick++ {
		m.emitProbes()
		for i := range peers {
			probe := readProbe(t, peers[i], codec)
			if probe.IsEcho {
				t.Fatalf("tick %d path %d: emitted an echo, want an originating probe", tick, i)
			}
			if probe.PathID != uint8(i) {
				t.Fatalf("tick %d: emitted probe PathID=%d, want %d", tick, probe.PathID, i)
			}
			if probe.ProbeSeq != tick {
				t.Fatalf("tick %d path %d: ProbeSeq=%d, want %d", tick, i, probe.ProbeSeq, tick)
			}
		}
	}
}

// TestMultipathProbeDrivesFailover is the end-to-end acceptance: a healthy probe
// exchange brings both paths Up (scheduler selects the primary), then blackholing
// the primary's echoes marks it Down within the detection window and the scheduler
// fails egress over to the backup — all deterministic under the injected clock.
func TestMultipathProbeDrivesFailover(t *testing.T) {
	psk := testKey(t, 0x25)
	clk := newFakeClock()
	m, probers, scheduler := newProbingMultipath(t, loopbackPaths(2), psk, clk)
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	peers := make([]*net.UDPConn, 2)
	peerAPs := make([]netip.AddrPort, 2)
	for i := range peers {
		peer, peerAP := rawPeer(t)
		peers[i] = peer
		peerAPs[i] = peerAP
		m.paths[i].setRemote(peerAP)
	}
	reflector := telemetry.NewReflector(psk, rand.Reader)
	codec, _ := frame.NewCodec(psk)

	// echoPath reads the probe the last emitProbes sent on path p and, when echo is
	// true, reflects it back into the path's prober; otherwise it drops it (a
	// blackholed path: the probe left but nothing comes back).
	echoPath := func(p int, echo bool) {
		probe := readProbe(t, peers[p], codec)
		if !echo {
			return
		}
		raw, err := frame.Encode(psk, probe)
		if err != nil {
			t.Fatalf("re-encode probe: %v", err)
		}
		reflected, _, err := reflector.Reflect(raw)
		if err != nil {
			t.Fatalf("reflect path %d: %v", p, err)
		}
		m.handleInbound(m.paths[p], reflected, peerAPs[p])
	}

	// Bring both paths Up: UpAfterSuccesses healthy rounds, echoing both.
	for i := 0; i < testProbeUpSucc; i++ {
		m.emitProbes() // Ticks both probers, sends a probe on each
		clk.advance(testProbeRTT)
		echoPath(0, true)
		echoPath(1, true)
		clk.advance(testProbeInterval - testProbeRTT)
	}
	if probers[0].State() != telemetry.StateUp || probers[1].State() != telemetry.StateUp {
		t.Fatalf("after healthy exchange states = (%v,%v), want (up,up)", probers[0].State(), probers[1].State())
	}
	if idx := scheduler.Pick(sched.ClassData); idx != 0 {
		t.Fatalf("scheduler Pick = %d while both up, want the primary 0", idx)
	}

	// Blackhole path 0: keep echoing path 1, drop path 0's echoes. After the
	// detection window elapses, a Tick marks path 0 Down and the scheduler fails
	// egress over to the backup path 1.
	rounds := int(testProbeDownAfter/testProbeInterval) + 3
	for i := 0; i < rounds; i++ {
		m.emitProbes() // Ticks both; path 0 goes stale as its silence grows
		clk.advance(testProbeRTT)
		echoPath(0, false) // blackholed: probe drained, no echo
		echoPath(1, true)  // backup stays healthy
		clk.advance(testProbeInterval - testProbeRTT)
	}
	if probers[0].State() != telemetry.StateDown {
		t.Fatalf("blackholed path 0 state = %v, want down", probers[0].State())
	}
	if probers[1].State() != telemetry.StateUp {
		t.Fatalf("backup path 1 state = %v, want up", probers[1].State())
	}
	if idx := scheduler.Pick(sched.ClassData); idx != 1 {
		t.Fatalf("scheduler Pick = %d after primary blackhole, want failover to backup 1", idx)
	}

	// Failover is usable: the backup has a known remote, so a Send routes over it.
	if err := m.Send([][]byte{[]byte("post-failover")}, m.virt); err != nil {
		t.Fatalf("Send after failover: %v", err)
	}
}

// TestMultipathEmitProbesCountsTxBytes is the D48 regression: a PROBE frame that
// emitProbes writes to the wire must count into the path's txBytes, the same
// true-wire-volume counter the DATA/PARITY send paths use — not just DATA/PARITY.
// Fails against the pre-D48 code, where emitProbes never touched ps.txBytes and
// an idle standby's tx counter stayed flat despite genuinely transmitting probes.
func TestMultipathEmitProbesCountsTxBytes(t *testing.T) {
	psk := testKey(t, 0x26)
	clk := newFakeClock()
	m, _, _ := newProbingMultipath(t, loopbackPaths(1), psk, clk)
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	peer, peerAP := rawPeer(t)
	m.paths[0].setRemote(peerAP)

	if before := m.paths[0].txBytes.Load(); before != 0 {
		t.Fatalf("txBytes before emitProbes = %d, want 0", before)
	}

	m.emitProbes()

	if err := peer.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	buf := make([]byte, maxDatagram)
	n, err := peer.Read(buf)
	if err != nil {
		t.Fatalf("read emitted probe: %v", err)
	}

	if got, want := m.paths[0].txBytes.Load(), uint64(n); got != want {
		t.Fatalf("txBytes after emitProbes = %d, want %d (the emitted probe's wire length)", got, want)
	}
}

// TestMultipathEchoReflectionCountsTxBytes is the D48 regression for the receive
// side: an inbound PROBE's echo, written back by dispatchInbound's reflection
// path, must count into the path's txBytes exactly like a DATA/PARITY write.
// Fails against the pre-D48 code, where the echo write never touched ps.txBytes.
func TestMultipathEchoReflectionCountsTxBytes(t *testing.T) {
	psk := testKey(t, 0x27)
	clk := newFakeClock()
	m, _, _ := newProbingMultipath(t, loopbackPaths(1), psk, clk)
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	peer, peerAP := rawPeer(t)

	if before := m.paths[0].txBytes.Load(); before != 0 {
		t.Fatalf("txBytes before echo reflection = %d, want 0", before)
	}

	const seq = 3
	ts := clk.Now().UnixNano()
	raw, err := frame.Encode(psk, frame.Probe{PathID: 0, ProbeSeq: seq, TimestampNanos: ts, IsEcho: false})
	if err != nil {
		t.Fatalf("encode probe: %v", err)
	}
	m.handleInbound(m.paths[0], raw, peerAP)

	if err := peer.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	buf := make([]byte, maxDatagram)
	n, err := peer.Read(buf)
	if err != nil {
		t.Fatalf("read reflected echo: %v", err)
	}

	if got, want := m.paths[0].txBytes.Load(), uint64(n); got != want {
		t.Fatalf("txBytes after echo reflection = %d, want %d (the reflected echo's wire length)", got, want)
	}
}

// TestMultipathEmitProbesCountsSendErrors is the D96 item 4 regression: a PROBE
// socket write error emitProbes drops must accumulate the path's probeSendErrors
// counter (wanbond_path_probe_send_errors_total) rather than vanish silently.
// Two paths are probed; only path 0's socket is closed before emitProbes runs, so
// only its write fails — asserting the counter rises for EXACTLY that path, not
// path 1's, and that path 1's probe still egresses normally (count-and-continue,
// no behaviour change to probing).
func TestMultipathEmitProbesCountsSendErrors(t *testing.T) {
	psk := testKey(t, 0x28)
	clk := newFakeClock()
	m, _, _ := newProbingMultipath(t, loopbackPaths(2), psk, clk)
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	_, peerAP0 := rawPeer(t)
	peer1, peerAP1 := rawPeer(t)
	m.paths[0].setRemote(peerAP0)
	m.paths[1].setRemote(peerAP1)

	if before := m.paths[0].probeSendErrors.Load(); before != 0 {
		t.Fatalf("path 0 probeSendErrors before emitProbes = %d, want 0", before)
	}
	if before := m.paths[1].probeSendErrors.Load(); before != 0 {
		t.Fatalf("path 1 probeSendErrors before emitProbes = %d, want 0", before)
	}

	// Force path 0's write to fail: close its socket out from under the snapshot
	// (mirroring the doc comment's "concurrent Close" scenario) while leaving path
	// 1's socket open.
	if err := m.paths[0].conn.Close(); err != nil {
		t.Fatalf("close path 0 conn: %v", err)
	}

	m.emitProbes()

	if got := m.paths[0].probeSendErrors.Load(); got != 1 {
		t.Fatalf("path 0 probeSendErrors after emitProbes = %d, want 1", got)
	}
	if got := m.paths[1].probeSendErrors.Load(); got != 0 {
		t.Fatalf("path 1 probeSendErrors after emitProbes = %d, want 0 (its socket never failed)", got)
	}

	// path 1's probe still egresses normally: the drop on path 0 must not perturb it.
	if err := peer1.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	buf := make([]byte, maxDatagram)
	if _, err := peer1.Read(buf); err != nil {
		t.Fatalf("read path 1 probe: %v", err)
	}
}
