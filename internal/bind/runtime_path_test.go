package bind

import (
	"bytes"
	"crypto/rand"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/bond"
	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/frame"
	"github.com/7mind/wanbond/internal/telemetry"
)

// remoteProcessBoot is the process epoch the reflecting end of these tests announces in
// the hellos its echoes carry.
const remoteProcessBoot uint64 = 987

// reflectHello reflects one emitted probe as the remote end does: the echo carries the
// remote transport's hello, from which the bind learns (and renews) a lane on the path.
func reflectHello(t testing.TB, refl *telemetry.Reflector, psk config.Key, probe frame.Probe) []byte {
	t.Helper()
	raw, err := frame.Encode(psk, probe)
	if err != nil {
		t.Fatalf("re-encode probe: %v", err)
	}
	accepted, err := refl.AcceptProbe(raw)
	if err != nil {
		t.Fatalf("reflect path %d: %v", probe.PathID, err)
	}
	echo, err := refl.EncodeAcceptedProbe(accepted, bond.Hello(bond.Epoch{Boot: remoteProcessBoot, Generation: 1}, 0))
	if err != nil {
		t.Fatalf("encode echo for path %d: %v", probe.PathID, err)
	}
	return echo
}

// probeRound drives one healthy probe cadence step against the given path indices:
// emitProbes on every open path, then for each (peer, path) reads the emitted probe,
// reflects it, and feeds the echo back so the path's prober records a heartbeat and the
// transport learns the lane. It advances the fake clock by one probe interval.
func probeRound(t testing.TB, m *Multipath, clk *fakeClock, refl *telemetry.Reflector, codec *frame.Codec, psk config.Key, peers map[int]*net.UDPConn, aps map[int]netip.AddrPort) {
	t.Helper()
	m.emitProbes()
	clk.advance(testProbeRTT)
	for idx, peer := range peers {
		m.handleInbound(m.paths[idx], reflectHello(t, refl, psk, readProbe(t, peer, codec)), aps[idx])
	}
	clk.advance(testProbeInterval - testProbeRTT)
}

// upLanes returns, in ascending order, the local path ids on which the transport of the
// bound peer at index peer holds a lane it may send on: the paths admitted to the bond.
func upLanes(t testing.TB, m *Multipath, peer int) []uint8 {
	t.Helper()
	snapshot := m.PeerSnapshots()[peer].Adaptive
	ids := []uint8{}
	if snapshot == nil {
		t.Fatalf("peer %d has no open transport", peer)
		return ids
	}
	for _, lane := range snapshot.Paths {
		if lane.Up {
			ids = append(ids, uint8(lane.Path>>8))
		}
	}
	slices.Sort(ids)
	return ids
}

// TestMultipathAddPathAdmitsWhenHealthy is the T30 add acceptance: a path added at
// runtime opens its own socket + prober, becomes a lane of the transport only once its
// probes are echoed, and disturbs neither the surviving path's remote and lane nor the
// single virtual endpoint.
func TestMultipathAddPathAdmitsWhenHealthy(t *testing.T) {
	psk := testKey(t, 0x31)
	clk := newFakeClock()
	m, probers := newProbingMultipath(t, loopbackPaths(1), psk, clk)
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
	if got := upLanes(t, m, 0); !slices.Equal(got, []uint8{m.paths[0].id}) {
		t.Fatalf("lanes = %v, want only the primary's", got)
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

	// Down until healthy: the transport still holds only the primary's lane.
	if got := upLanes(t, m, 0); !slices.Equal(got, []uint8{m.paths[0].id}) {
		t.Fatalf("lanes right after add = %v, want only the primary's (added path not yet echoed)", got)
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

	// Admission proof: the transport now holds a lane on the runtime-added path.
	if got := upLanes(t, m, 0); !slices.Equal(got, []uint8{m.paths[0].id, added.id}) {
		t.Fatalf("lanes after the added path's echoes = %v, want the primary's and the added path's", got)
	}

	// Blackhole the primary: its liveness goes down while the runtime-added path, whose
	// probes are still echoed, stays up and keeps its lane.
	rounds := int(testProbeDownAfter/testProbeInterval) + 3
	for i := 0; i < rounds; i++ {
		m.emitProbes()
		clk.advance(testProbeRTT)
		readProbe(t, peer0, codec) // drain the primary's probe, no echo (blackhole)
		m.handleInbound(m.paths[1], reflectHello(t, refl, psk, readProbe(t, peer1, codec)), ap1)
		clk.advance(testProbeInterval - testProbeRTT)
	}
	if probers[0].State() != telemetry.StateDown {
		t.Fatalf("blackholed primary state = %v, want down", probers[0].State())
	}
	if added.prober.State() != telemetry.StateUp {
		t.Fatalf("added path state = %v after the primary's blackhole, want up", added.prober.State())
	}
	if got := upLanes(t, m, 0); !slices.Contains(got, added.id) {
		t.Fatalf("lanes after primary blackhole = %v, want the runtime-added path's among them", got)
	}
}

// TestMultipathRemovePathDrainsAndCloses is the T30 remove acceptance: removing a
// path closes its socket and makes the transport forget its lane, while the surviving
// path, the virtual endpoint, and the per-peer resequencing continue undisturbed — a
// datagram the removed path had already delivered stays queued, and the flow keeps
// being delivered in order on the remaining path.
func TestMultipathRemovePathDrainsAndCloses(t *testing.T) {
	psk := testKey(t, 0x32)
	clk := newFakeClock()
	m, _ := newProbingMultipath(t, loopbackPaths(2), psk, clk)
	fns, _, err := m.Open(0)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	fn := fns[0]

	virtBefore := m.virt
	survivor := m.paths[0]
	removed := m.paths[1]
	removedConn := removed.conn

	remote := newRemoteTransport(t, m.peerState, 987)
	survivorClient, survivorSrc := dialPath(t, survivor)
	removedClient, removedSrc := dialPath(t, removed)
	remote.join(survivor, survivorSrc)
	remote.join(removed, removedSrc)
	send := func(cl *net.UDPConn, f frame.Control) {
		t.Helper()
		if _, err := cl.Write(remote.wire(f)); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	recv := func() []byte {
		t.Helper()
		got, _ := receiveOne(t, fn)
		return append([]byte(nil), got...)
	}

	// An ordered flow is delivered on the surviving path BEFORE the removal.
	send(survivorClient, remote.bulk(survivor, []byte("pre-remove")))
	if got := recv(); !bytes.Equal(got, []byte("pre-remove")) {
		t.Fatalf("pre-remove delivery = %q, want %q", got, "pre-remove")
	}

	// The next datagram of the flow is still to come; the one after it arrives on the
	// path about to be removed and waits in the resequencer.
	next := remote.bulk(survivor, []byte("post-remove"))
	send(removedClient, remote.bulk(removed, []byte("queued-on-removed")))
	deadline := time.Now().Add(2 * time.Second)
	for m.resequencer.Load().Buffered() != 1 {
		if time.Now().After(deadline) {
			t.Fatal("the removed path's datagram never reached the resequencer")
		}
		time.Sleep(time.Millisecond)
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
	if got := upLanes(t, m, 0); !slices.Equal(got, []uint8{survivor.id}) {
		t.Fatalf("lanes after remove = %v, want only the survivor's", got)
	}

	// The removed path's socket is closed: a write on it now fails.
	if _, werr := removedConn.WriteToUDPAddrPort([]byte("x"), netip.MustParseAddrPort("127.0.0.1:9")); werr == nil {
		t.Fatal("removed path socket still writable: it was not closed")
	}

	// The flow continues on the surviving path with the NEXT datagram, and the one the
	// removed path queued follows it: the per-peer resequencing is not reset by the
	// removal.
	send(survivorClient, next)
	if got := recv(); !bytes.Equal(got, []byte("post-remove")) {
		t.Fatalf("post-remove delivery = %q, want %q (surviving path/resequencing disturbed)", got, "post-remove")
	}
	if got := recv(); !bytes.Equal(got, []byte("queued-on-removed")) {
		t.Fatalf("delivery after the gap closed = %q, want %q (the removal dropped a queued datagram)", got, "queued-on-removed")
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
// set / transport lanes from the lock-free receive fan-in or the send path trips the
// detector; a clean run proves the mutation is serialized against them.
func TestMultipathRuntimePathSetRace(t *testing.T) {
	psk := testKey(t, 0x33)
	clk := newFakeClock()
	m, _ := newProbingMultipath(t, loopbackPaths(1), psk, clk)
	fns, _, err := m.Open(0)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	fn := fns[0]

	peer0, ap0 := rawPeer(t)
	m.paths[0].setRemote(ap0)
	survivor := m.paths[0]
	remote := newRemoteTransport(t, m.peerState, 987)
	feed, feedSrc := dialPath(t, survivor)

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

	// Feeder: a datagram flow into the surviving path so the fan-in delivers. The hello
	// that established the lane is repeated as a probe cadence repeats it, so the lane
	// outlives the run.
	steady.Add(1)
	go func() {
		defer steady.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			remote.join(survivor, feedSrc)
			_, _ = feed.Write(remote.wire(remote.small(survivor, []byte("d"))))
		}
	}()

	// Sender: hammers the send hot path (the transport's queue and lanes).
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
// stale m.defs and RESURRECTED the removed path — a probed-but-unselectable path. Now
// m.defs/m.probers track the removal, so the removed path stays gone and the reopened
// transport learns a lane on the surviving path alone and sends over it.
func TestMultipathRuntimeRemoveSurvivesReopen(t *testing.T) {
	psk := testKey(t, 0x34)
	clk := newFakeClock()
	m, probers := newProbingMultipath(t, loopbackPaths(2), psk, clk) // "a","b"
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

	// Transport and path slice are consistent: once the fresh socket's probes are echoed
	// the transport holds the surviving path's lane and no other.
	m.paths[0].setRemote(ap0)
	probeRound(t, m, clk, refl, codec, psk, map[int]*net.UDPConn{0: peer0}, map[int]netip.AddrPort{0: ap0})
	if got := upLanes(t, m, 0); !slices.Equal(got, []uint8{m.paths[0].id}) {
		t.Fatalf("lanes after reopen = %v, want only the survivor's (transport/path desync)", got)
	}

	// A Send egresses on the surviving path.
	payload := []byte("post-reopen")
	if err := m.Send([][]byte{payload}, m.virt); err != nil {
		t.Fatalf("Send after reopen = %v, want nil", err)
	}
	if data, _ := readTransportData(t, peer0, codec); !bytes.HasSuffix(data.Payload, payload) {
		t.Fatalf("datagram on the surviving path = %x, want it to end in %q", data.Payload, payload)
	}
}

// TestMultipathRuntimeAddSurvivesReopen is the T30 reopen-consistency regression
// (review criticism 1a): a runtime AddPath must SURVIVE the Close→Open lifecycle. Before
// the fix, Open rebuilt m.paths from the stale m.defs (boot paths only), so the added
// path's prober had NO path to Tick it and its liveness froze. Now m.defs/m.probers track
// the add, so the added path persists as a fully-wired path the transport learns a lane
// on, with no frozen zombie entry.
func TestMultipathRuntimeAddSurvivesReopen(t *testing.T) {
	psk := testKey(t, 0x35)
	clk := newFakeClock()
	m, probers := newProbingMultipath(t, loopbackPaths(1), psk, clk) // "a"
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

	// No frozen zombie: the added path (index 1) starts without a lane after reopen
	// (fresh socket, no echoes yet), so once the primary's probes are echoed the
	// transport holds the primary's lane alone and a Send egresses on the primary.
	m.paths[0].setRemote(ap0)
	probeRound(t, m, clk, refl, codec, psk, map[int]*net.UDPConn{0: peer0}, map[int]netip.AddrPort{0: ap0})
	if got := upLanes(t, m, 0); !slices.Equal(got, []uint8{m.paths[0].id}) {
		t.Fatalf("lanes after reopen = %v, want only the primary's (added path not yet echoed)", got)
	}
	payload := []byte("post-reopen")
	if err := m.Send([][]byte{payload}, m.virt); err != nil {
		t.Fatalf("Send after reopen = %v, want nil", err)
	}
	if data, _ := readTransportData(t, peer0, codec); !bytes.HasSuffix(data.Payload, payload) {
		t.Fatalf("datagram on the primary = %x, want it to end in %q", data.Payload, payload)
	}

	// Prove the reopened added path is fully wired (its prober Ticked, its echoes
	// teaching the transport a lane), not a zombie: bring it up alongside the primary,
	// then blackhole the primary and confirm the reopened added path stays up with its
	// lane.
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
	if got := upLanes(t, m, 0); !slices.Equal(got, []uint8{m.paths[0].id, m.paths[1].id}) {
		t.Fatalf("lanes with both up = %v, want both paths'", got)
	}
	rounds := int(testProbeDownAfter/testProbeInterval) + 3
	for i := 0; i < rounds; i++ {
		m.emitProbes()
		clk.advance(testProbeRTT)
		readProbe(t, peer0, codec) // drain primary probe, no echo (blackhole)
		m.handleInbound(m.paths[1], reflectHello(t, refl, psk, readProbe(t, peer1, codec)), ap1)
		clk.advance(testProbeInterval - testProbeRTT)
	}
	if probers[0].State() != telemetry.StateDown {
		t.Fatalf("blackholed primary state = %v, want down", probers[0].State())
	}
	if m.paths[1].prober.State() != telemetry.StateUp {
		t.Fatalf("reopened added path state after the primary's blackhole = %v, want up", m.paths[1].prober.State())
	}
	if got := upLanes(t, m, 0); !slices.Contains(got, m.paths[1].id) {
		t.Fatalf("lanes after primary blackhole = %v, want the reopened runtime-added path's among them", got)
	}
}

// TestMultipathRemoveReopenAddPathIDUnique is the T30 remove->reopen->add PathID
// high-water regression (review criticism, reproduced). A removal opens a gap in the
// stamp space while the survivor keeps its ORIGINAL higher prober PathID; before the
// fix Open reset the id counter to len(m.paths), so the next runtime AddPath re-minted
// a PathID that COLLIDED with the survivor's live stamp. Two live paths then emitted
// probes at the same (PathID, SessionID); the peer's Reflector keys anti-replay and
// the session challenge PER PathID, so the strict-monotonic replay filter dropped one
// of the two independent ProbeSeq streams -> probe loss / false-DOWN on the collided
// path. This asserts every live path carries a DISTINCT on-wire PathID (both its
// prober stamp and the pathState.id its lanes are numbered by) across
// remove->reopen->add, and that the two agree per path. It FAILS on 8d81f2b (b and c both at PathID 1).
func TestMultipathRemoveReopenAddPathIDUnique(t *testing.T) {
	psk := testKey(t, 0x36)
	clk := newFakeClock()
	m, _ := newProbingMultipath(t, loopbackPaths(2), psk, clk) // "a","b" -> stamps 0,1
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	// Survivor "b" carries prober PathID 1; removing "a" opens the gap at stamp 0.
	if got := m.paths[1].prober.PathID(); got != 1 {
		t.Fatalf("boot path b prober PathID = %d, want 1", got)
	}
	if err := m.RemovePath("a"); err != nil {
		t.Fatalf("RemovePath: %v", err)
	}

	// Cycle Close->Open (the amneziawg Down/Up lifecycle) then admit a runtime path.
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("re-Open: %v", err)
	}
	if len(m.paths) != 1 || m.paths[0].name != "b" {
		t.Fatalf("after reopen paths = %v, want [b]", pathNamesOf(m))
	}
	// Survivor keeps its ORIGINAL stamp across the reopen (never renumbered), and its
	// lane id agrees with the prober stamp.
	if got := m.paths[0].prober.PathID(); got != 1 {
		t.Fatalf("survivor b prober PathID after reopen = %d, want 1 (stamp must be stable)", got)
	}
	if m.paths[0].id != m.paths[0].prober.PathID() {
		t.Fatalf("survivor lane id %d != prober stamp %d after reopen", m.paths[0].id, m.paths[0].prober.PathID())
	}

	if err := m.AddPath(config.Path{Name: "c", SourceAddr: netip.MustParseAddr("127.0.0.1")}); err != nil {
		t.Fatalf("AddPath: %v", err)
	}
	if len(m.paths) != 2 {
		t.Fatalf("paths after add = %d, want 2", len(m.paths))
	}

	// Every live path must carry a DISTINCT on-wire PathID, and its lane id must
	// equal its prober stamp so the transport and PROBE agree on the wire.
	seenProber := map[uint8]string{}
	seenData := map[uint8]string{}
	for _, ps := range m.paths {
		if ps.id != ps.prober.PathID() {
			t.Fatalf("path %q lane id %d != prober stamp %d", ps.name, ps.id, ps.prober.PathID())
		}
		if other, dup := seenProber[ps.prober.PathID()]; dup {
			t.Fatalf("prober PathID collision: paths %q and %q both stamp PathID %d "+
				"(two live paths at identical (PathID,SessionID) -> cross-stream probe-replay drops)",
				other, ps.name, ps.prober.PathID())
		}
		seenProber[ps.prober.PathID()] = ps.name
		if other, dup := seenData[ps.id]; dup {
			t.Fatalf("lane id collision: paths %q and %q both use pathState.id %d", other, ps.name, ps.id)
		}
		seenData[ps.id] = ps.name
	}
}

// pathNamesOf snapshots the active path names for a diagnostic message.
func pathNamesOf(m *Multipath) []string {
	out := make([]string, len(m.paths))
	for i, ps := range m.paths {
		out[i] = ps.name
	}
	return out
}
