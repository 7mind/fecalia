package bind

import (
	"bytes"
	"crypto/rand"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/frame"
	"github.com/7mind/wanbond/internal/telemetry"
)

// sendDataSeqTo encodes a DATA frame with an explicit outer-seq under psk and sends
// it from a fresh client socket to dst. It is the seq-parameterized companion to
// sendDataTo, used where the resequencer must see monotonic outer-seqs across sends.
func sendDataSeqTo(t testing.TB, psk config.Key, dst *net.UDPAddr, pathID uint8, seq uint64, payload []byte) {
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
	wire, err := codec.Encode(nil, frame.Data{OuterSeq: seq, PathID: pathID, Payload: payload})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if _, err := cl.Write(wire); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// probeRound drives one healthy probe cadence step against the given path indices:
// emitProbes on every open path, then for each (peer, path) reads the emitted probe,
// reflects it, and feeds the echo back so the path's prober records a heartbeat. It
// advances the fake clock by one probe interval.
func probeRound(t testing.TB, m *Multipath, clk *fakeClock, refl *telemetry.Reflector, codec *frame.Codec, psk config.Key, peers map[int]*net.UDPConn, aps map[int]netip.AddrPort) {
	t.Helper()
	m.emitProbes()
	clk.advance(testProbeRTT)
	for idx, peer := range peers {
		probe := readProbe(t, peer, codec)
		raw, err := frame.Encode(psk, probe)
		if err != nil {
			t.Fatalf("re-encode probe: %v", err)
		}
		echo, err := refl.Reflect(raw)
		if err != nil {
			t.Fatalf("reflect path %d: %v", idx, err)
		}
		m.handleInbound(m.paths[idx], echo, aps[idx])
	}
	clk.advance(testProbeInterval - testProbeRTT)
}

// TestMultipathAddPathAdmitsWhenHealthy is the T30 add acceptance: a path added at
// runtime opens its own socket + prober, is admitted to the scheduler, becomes
// selectable only once its probes report healthy, and disturbs neither the surviving
// path's remote nor the single virtual endpoint.
func TestMultipathAddPathAdmitsWhenHealthy(t *testing.T) {
	psk := testKey(t, 0x31)
	clk := newFakeClock()
	m, probers, scheduler := newProbingMultipath(t, loopbackPaths(1), psk, clk)
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	virtBefore := m.virt
	refl := telemetry.NewReflector(psk, rand.Reader)
	codec, _ := frame.NewCodec(psk)

	peer0, ap0 := rawPeer(t)
	m.paths[0].setRemote(ap0)
	primaryRemoteBefore, _ := m.paths[0].getRemote()

	// Bring the primary up.
	for i := 0; i < testProbeUpSucc; i++ {
		probeRound(t, m, clk, refl, codec, psk, map[int]*net.UDPConn{0: peer0}, map[int]netip.AddrPort{0: ap0})
	}
	if probers[0].State() != telemetry.StateUp {
		t.Fatalf("primary state = %v, want up", probers[0].State())
	}
	if idx := scheduler.Pick(); idx != 0 {
		t.Fatalf("Pick = %d, want 0 (primary active)", idx)
	}

	// --- Add a second path at runtime. ---
	if err := m.AddPath(config.Path{Name: "runtime-b", SourceAddr: netip.MustParseAddr("127.0.0.1")}); err != nil {
		t.Fatalf("AddPath: %v", err)
	}
	if len(m.paths) != 2 {
		t.Fatalf("paths = %d, want 2 after add", len(m.paths))
	}
	added := m.paths[1]
	if added.id == m.paths[0].id {
		t.Fatalf("added path reused the primary's id %d (surviving id must be stable)", added.id)
	}
	if added.conn == nil {
		t.Fatal("added path has no socket")
	}
	if added.prober == nil {
		t.Fatal("added path has no prober")
	}

	// Survivor and virtual endpoint undisturbed by the add.
	if m.virt != virtBefore {
		t.Fatal("virtual endpoint object changed on add (engine would see churn)")
	}
	if r, _ := m.paths[0].getRemote(); r != primaryRemoteBefore {
		t.Fatalf("primary remote changed on add: %v != %v", r, primaryRemoteBefore)
	}

	// Down until healthy: the scheduler still selects the primary.
	if idx := scheduler.Pick(); idx != 0 {
		t.Fatalf("Pick = %d right after add, want 0 (added path not yet healthy)", idx)
	}

	// Bring BOTH up; the added path records heartbeats and goes up.
	peer1, ap1 := rawPeer(t)
	added.setRemote(ap1)
	for i := 0; i < testProbeUpSucc; i++ {
		probeRound(t, m, clk, refl, codec, psk,
			map[int]*net.UDPConn{0: peer0, 1: peer1}, map[int]netip.AddrPort{0: ap0, 1: ap1})
	}
	if added.prober.State() != telemetry.StateUp {
		t.Fatalf("added path state = %v, want up (probes not driving its liveness)", added.prober.State())
	}

	// Admission proof: blackhole the primary; egress fails over to the runtime-added
	// path, which is only possible if it was admitted to the scheduler.
	rounds := int(testProbeDownAfter/testProbeInterval) + 3
	for i := 0; i < rounds; i++ {
		m.emitProbes()
		clk.advance(testProbeRTT)
		readProbe(t, peer0, codec) // drain the primary's probe, no echo (blackhole)
		probe := readProbe(t, peer1, codec)
		raw, _ := frame.Encode(psk, probe)
		echo, err := refl.Reflect(raw)
		if err != nil {
			t.Fatalf("reflect backup: %v", err)
		}
		m.handleInbound(m.paths[1], echo, ap1)
		clk.advance(testProbeInterval - testProbeRTT)
	}
	if probers[0].State() != telemetry.StateDown {
		t.Fatalf("blackholed primary state = %v, want down", probers[0].State())
	}
	if idx := scheduler.Pick(); idx != 1 {
		t.Fatalf("Pick = %d after primary blackhole, want 1 (failover to the runtime-added path)", idx)
	}
}

// TestMultipathRemovePathDrainsAndCloses is the T30 remove acceptance: removing a
// path closes its socket and drops it from the scheduler, while the surviving path,
// the virtual endpoint, and the connection-global outer-seq resequencing continue
// undisturbed — a DATA flow keeps being delivered on the remaining path.
func TestMultipathRemovePathDrainsAndCloses(t *testing.T) {
	psk := testKey(t, 0x32)
	clk := newFakeClock()
	m, _, _ := newProbingMultipath(t, loopbackPaths(2), psk, clk)
	fns, _, err := m.Open(0)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	fn := fns[0]

	virtBefore := m.virt
	survivor := m.paths[0]
	survivorAddr := survivor.conn.LocalAddr().(*net.UDPAddr)
	removedConn := m.paths[1].conn

	recv := func() []byte {
		bufs := [][]byte{make([]byte, 2048)}
		sizes := make([]int, 1)
		eps := make([]Endpoint, 1)
		n, err := fn(bufs, sizes, eps)
		if err != nil {
			t.Fatalf("receive: %v", err)
		}
		if n != 1 {
			t.Fatalf("receive n = %d, want 1", n)
		}
		return append([]byte(nil), bufs[0][:sizes[0]]...)
	}

	// A DATA flow is delivered on the surviving path BEFORE the removal (pins the
	// resequencer's release point at outer-seq 100).
	sendDataSeqTo(t, psk, survivorAddr, 0, 100, []byte("pre-remove"))
	if got := recv(); !bytes.Equal(got, []byte("pre-remove")) {
		t.Fatalf("pre-remove delivery = %q, want %q", got, "pre-remove")
	}

	// --- Remove the backup path at runtime. ---
	if err := m.RemovePath("b"); err != nil {
		t.Fatalf("RemovePath: %v", err)
	}
	if len(m.paths) != 1 {
		t.Fatalf("paths = %d, want 1 after remove", len(m.paths))
	}
	if m.paths[0] != survivor {
		t.Fatal("surviving path object changed on remove")
	}
	if m.virt != virtBefore {
		t.Fatal("virtual endpoint object changed on remove")
	}

	// The removed path's socket is closed: a write on it now fails.
	if _, werr := removedConn.WriteToUDPAddrPort([]byte("x"), netip.MustParseAddrPort("127.0.0.1:9")); werr == nil {
		t.Fatal("removed path socket still writable: it was not closed")
	}

	// The flow continues on the surviving path with the NEXT outer-seq: the
	// connection-global resequencing is not reset by the removal.
	sendDataSeqTo(t, psk, survivorAddr, 0, 101, []byte("post-remove"))
	if got := recv(); !bytes.Equal(got, []byte("post-remove")) {
		t.Fatalf("post-remove delivery = %q, want %q (surviving path/resequencing disturbed)", got, "post-remove")
	}

	// Cannot remove the last remaining path (would tear down the virtual endpoint).
	if err := m.RemovePath("a"); err == nil {
		t.Fatal("removing the last path succeeded, want refusal")
	}
	// Removing an unknown path errors.
	if err := m.RemovePath("nope"); err == nil {
		t.Fatal("removing an unknown path succeeded, want error")
	}
}

// TestMultipathRuntimePathSetRace is the crux concurrency guard (T30): runtime
// AddPath/RemovePath run CONCURRENTLY with the Send and receive hot paths and the
// probe loop. Under `go test -race` any unsynchronized access to the mutating path
// set / scheduler from the lock-free receive fan-in or the send path trips the
// detector; a clean run proves the mutation is serialized against them.
func TestMultipathRuntimePathSetRace(t *testing.T) {
	psk := testKey(t, 0x33)
	clk := newFakeClock()
	m, _, _ := newProbingMultipath(t, loopbackPaths(1), psk, clk)
	fns, _, err := m.Open(0)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	fn := fns[0]

	peer0, ap0 := rawPeer(t)
	m.paths[0].setRemote(ap0)
	survivorAddr := m.paths[0].conn.LocalAddr().(*net.UDPAddr)

	var steady, drain sync.WaitGroup
	stop := make(chan struct{})

	// Drainer: stands in for the engine's receive goroutine; exits on Close.
	drain.Add(1)
	go func() {
		defer drain.Done()
		bufs := [][]byte{make([]byte, 2048)}
		sizes := make([]int, 1)
		eps := make([]Endpoint, 1)
		for {
			if _, err := fn(bufs, sizes, eps); err != nil {
				return
			}
		}
	}()

	// Feeder: a monotonic DATA flow into the surviving path so the fan-in delivers.
	steady.Add(1)
	go func() {
		defer steady.Done()
		var seq atomic.Uint64
		for {
			select {
			case <-stop:
				return
			default:
			}
			sendDataSeqTo(t, psk, survivorAddr, 0, seq.Add(1), []byte("d"))
		}
	}()

	// Sender: hammers the send hot path (Pick + m.paths read under m.mu).
	steady.Add(1)
	go func() {
		defer steady.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = m.Send([][]byte{[]byte("x")}, m.virt)
		}
	}()

	// Probe loop: snapshots the (mutating) path set under m.mu.
	steady.Add(1)
	go func() {
		defer steady.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			m.emitProbes()
		}
	}()

	// Mutator (inline): add then remove a distinct path repeatedly, overlapping the
	// steady-state hot paths. Each iteration is add+remove, so no path is left behind.
	// 200 iterations against a 255-id space (boot uses id 0, so ids 1..200 are minted)
	// never exhausts the id space, so any AddPath error here is UNEXPECTED. Fatal on it
	// rather than break: a silent break on iteration 0 would let this crux race guard
	// pass VACUOUSLY with zero add/remove coverage (masking an AddPath regression).
	for i := 0; i < 200; i++ {
		name := "rt-" + strconv.Itoa(i)
		if err := m.AddPath(config.Path{Name: name, SourceAddr: netip.MustParseAddr("127.0.0.1")}); err != nil {
			t.Fatalf("AddPath(%s) on iteration %d: %v (unexpected — the id space is not exhausted at 200 iters)", name, i, err)
		}
		if err := m.RemovePath(name); err != nil {
			t.Fatalf("RemovePath(%s) on iteration %d: %v", name, i, err)
		}
	}

	close(stop)
	steady.Wait() // feeder/sender/prober stopped; no AddPath is in flight now
	_ = m.Close() // releases the drainer and retires all readers
	drain.Wait()
	_ = peer0
}

// TestMultipathRuntimeRemoveSurvivesReopen is the T30 reopen-consistency regression
// (review criticism 1b): a runtime RemovePath must SURVIVE the amneziawg Close→Open
// lifecycle (Down/Up + IpcSet rebind). Before the fix, Open rebuilt m.paths from the
// stale m.defs and RESURRECTED the removed path with no scheduler entry — a
// probed-but-unselectable path that desynced the scheduler from the path slice. Now
// m.defs/m.probers track the removal and Open reconciles the scheduler, so the
// removed path stays gone and Pick still addresses the surviving path with no
// errNoHealthyPath.
func TestMultipathRuntimeRemoveSurvivesReopen(t *testing.T) {
	psk := testKey(t, 0x34)
	clk := newFakeClock()
	m, probers, scheduler := newProbingMultipath(t, loopbackPaths(2), psk, clk) // "a","b"
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	refl := telemetry.NewReflector(psk, rand.Reader)
	codec, _ := frame.NewCodec(psk)

	// Bring both boot paths up.
	peer0, ap0 := rawPeer(t)
	peer1, ap1 := rawPeer(t)
	m.paths[0].setRemote(ap0)
	m.paths[1].setRemote(ap1)
	for i := 0; i < testProbeUpSucc; i++ {
		probeRound(t, m, clk, refl, codec, psk,
			map[int]*net.UDPConn{0: peer0, 1: peer1}, map[int]netip.AddrPort{0: ap0, 1: ap1})
	}
	if probers[0].State() != telemetry.StateUp || probers[1].State() != telemetry.StateUp {
		t.Fatalf("boot paths not both up: a=%v b=%v", probers[0].State(), probers[1].State())
	}

	// Remove the backup path "b" at runtime.
	if err := m.RemovePath("b"); err != nil {
		t.Fatalf("RemovePath: %v", err)
	}

	// --- Cycle Close→Open (the exact bind lifecycle amneziawg drives on Down/Up). ---
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("re-Open: %v", err)
	}

	// The removed path stays gone: exactly one path, and it is "a".
	if len(m.paths) != 1 {
		t.Fatalf("paths after reopen = %d, want 1 (removed path was resurrected from stale m.defs)", len(m.paths))
	}
	if m.paths[0].name != "a" {
		t.Fatalf("surviving path after reopen = %q, want \"a\"", m.paths[0].name)
	}

	// Scheduler and path slice are consistent: Pick addresses the surviving path (a is
	// still up — probers persist across reopen), never an out-of-range zombie index.
	idx := scheduler.Pick()
	if idx != 0 {
		t.Fatalf("Pick after reopen = %d, want 0 (scheduler/path desync)", idx)
	}
	if idx >= len(m.paths) {
		t.Fatalf("Pick returned out-of-range index %d for %d paths (frozen zombie entry)", idx, len(m.paths))
	}

	// No errNoHealthyPath: a Send egresses on the surviving path once its remote is
	// re-seeded on the fresh socket.
	m.paths[0].setRemote(ap0)
	if err := m.Send([][]byte{[]byte("post-reopen")}, m.virt); err != nil {
		t.Fatalf("Send after reopen = %v, want nil (no errNoHealthyPath)", err)
	}
}

// TestMultipathRuntimeAddSurvivesReopen is the T30 reopen-consistency regression
// (review criticism 1a): a runtime AddPath must SURVIVE the Close→Open lifecycle. Before
// the fix, Open rebuilt m.paths from the stale m.defs (boot paths only), so the added
// path's prober was still in the scheduler at index N with NO path to Tick it: its
// liveness froze, Pick could pin to N, and Send's bounds check (N >= len(m.paths))
// rejected every datagram — total egress outage. Now m.defs/m.probers track the add and
// Open reconciles the scheduler, so the added path persists as a fully-wired, selectable
// path with no frozen zombie entry.
func TestMultipathRuntimeAddSurvivesReopen(t *testing.T) {
	psk := testKey(t, 0x35)
	clk := newFakeClock()
	m, probers, scheduler := newProbingMultipath(t, loopbackPaths(1), psk, clk) // "a"
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	refl := telemetry.NewReflector(psk, rand.Reader)
	codec, _ := frame.NewCodec(psk)

	peer0, ap0 := rawPeer(t)
	m.paths[0].setRemote(ap0)
	for i := 0; i < testProbeUpSucc; i++ {
		probeRound(t, m, clk, refl, codec, psk, map[int]*net.UDPConn{0: peer0}, map[int]netip.AddrPort{0: ap0})
	}

	// Add a second path at runtime (starts down until its probes report healthy).
	if err := m.AddPath(config.Path{Name: "runtime-b", SourceAddr: netip.MustParseAddr("127.0.0.1")}); err != nil {
		t.Fatalf("AddPath: %v", err)
	}
	if len(m.paths) != 2 {
		t.Fatalf("paths = %d, want 2 after add", len(m.paths))
	}

	// --- Cycle Close→Open. ---
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("re-Open: %v", err)
	}

	// The added path persists across the reopen at its slice index, with a live prober.
	if len(m.paths) != 2 {
		t.Fatalf("paths after reopen = %d, want 2 (runtime add did not survive)", len(m.paths))
	}
	if m.paths[1].name != "runtime-b" {
		t.Fatalf("added path after reopen = %q, want \"runtime-b\"", m.paths[1].name)
	}
	if m.paths[1].prober == nil {
		t.Fatal("added path lost its prober across the reopen")
	}

	// No frozen zombie: the added path (index 1) starts DOWN after reopen (fresh socket,
	// no echoes yet), so Pick selects the primary (index 0, still up), NEVER an
	// out-of-range index, and Send succeeds on the primary.
	if idx := scheduler.Pick(); idx < 0 || idx >= len(m.paths) {
		t.Fatalf("Pick after reopen = %d out of range [0,%d) (scheduler/path desync)", idx, len(m.paths))
	} else if idx != 0 {
		t.Fatalf("Pick after reopen = %d, want 0 (primary up, added path not yet healthy)", idx)
	}
	// Route this proof-of-egress Send to a throwaway sink so it does not pollute peer0's
	// receive buffer (the probe reads below would otherwise mis-read the DATA frame),
	// then restore the primary's real remote for the probe cadence.
	sinkPeer, sinkAP := rawPeer(t)
	m.paths[0].setRemote(sinkAP)
	if err := m.Send([][]byte{[]byte("post-reopen")}, m.virt); err != nil {
		t.Fatalf("Send after reopen = %v, want nil (frozen zombie would cause errNoHealthyPath)", err)
	}
	_ = sinkPeer
	m.paths[0].setRemote(ap0)

	// Prove the reopened added path is fully wired (selectable, its prober Ticked), not a
	// zombie: bring it up alongside the primary (it must NOT steal egress), then
	// blackhole the primary and confirm egress fails over onto the reopened added path.
	peer1, ap1 := rawPeer(t)
	m.paths[1].setRemote(ap1)
	for i := 0; i < testProbeUpSucc; i++ {
		probeRound(t, m, clk, refl, codec, psk,
			map[int]*net.UDPConn{0: peer0, 1: peer1}, map[int]netip.AddrPort{0: ap0, 1: ap1})
	}
	if probers[0].State() != telemetry.StateUp {
		t.Fatalf("primary state after reopen = %v, want up", probers[0].State())
	}
	if m.paths[1].prober.State() != telemetry.StateUp {
		t.Fatalf("reopened added path state = %v, want up (its prober is not being Ticked — frozen zombie)", m.paths[1].prober.State())
	}
	if idx := scheduler.Pick(); idx != 0 {
		t.Fatalf("Pick with both up = %d, want 0 (added path must not steal egress)", idx)
	}
	rounds := int(testProbeDownAfter/testProbeInterval) + 3
	for i := 0; i < rounds; i++ {
		m.emitProbes()
		clk.advance(testProbeRTT)
		readProbe(t, peer0, codec) // drain primary probe, no echo (blackhole)
		probe := readProbe(t, peer1, codec)
		raw, _ := frame.Encode(psk, probe)
		echo, err := refl.Reflect(raw)
		if err != nil {
			t.Fatalf("reflect backup: %v", err)
		}
		m.handleInbound(m.paths[1], echo, ap1)
		clk.advance(testProbeInterval - testProbeRTT)
	}
	if idx := scheduler.Pick(); idx != 1 {
		t.Fatalf("Pick after primary blackhole = %d, want 1 (failover onto the reopened runtime-added path)", idx)
	}
}
