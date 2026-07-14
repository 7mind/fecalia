package bind

import (
	"crypto/rand"
	"net"
	"net/netip"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/frame"
	"github.com/7mind/wanbond/internal/sched"
	"github.com/7mind/wanbond/internal/telemetry"
	"go.uber.org/goleak"
)

// fakeDeferredBinder is the injected listen seam (m.deferredListen) the reconcile
// tests drive: it returns EADDRNOTAVAIL — the "source_addr not yet assignable" signal
// reconcileDeferred reads to keep a path deferred — until arm() is called, after which
// it binds a real loopback socket (the address "became assignable"). The returned
// socket is adopted by m.paths on a successful promote and closed by m.Close, so the
// seam leaks nothing. It is safe for the single-goroutine synchronous test driver.
type fakeDeferredBinder struct {
	mu    sync.Mutex
	armed bool
	// devs records the dev argument of every listen() call, in call order, so a test
	// can assert what the caller's forced-device-bind DECISION threaded through to the
	// actual bind — the T106 round-2 gap: dev was accepted but never inspected by any
	// test, so neutering that decision to always "" passed the full suite.
	devs []string
}

func (f *fakeDeferredBinder) arm() {
	f.mu.Lock()
	f.armed = true
	f.mu.Unlock()
}

func (f *fakeDeferredBinder) listen(_ netip.Addr, _ uint16, dev string) (*net.UDPConn, error) {
	f.mu.Lock()
	f.devs = append(f.devs, dev)
	armed := f.armed
	f.mu.Unlock()
	if !armed {
		// Exactly the error a real not-yet-assignable source_addr yields; reconcileDeferred
		// keeps the path deferred and retries next tick.
		return nil, syscall.EADDRNOTAVAIL
	}
	// The address is now assignable: bind a real loopback socket standing in for the
	// interface that just came up, so the promoted path has a working transport the test
	// can probe over.
	return net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
}

// TestReconcilePromotesDeferredPathToLive is the T55 core acceptance: a path DEFERRED
// at Open because its source_addr was not assignable (EADDRNOTAVAIL) is BOUND and
// PROMOTED to a live path by the background reconcile once the address becomes
// assignable — brought into m.paths, the scheduler, and its reader — so the scheduler
// then selects it, WITHOUT a Close→Open restart. It also asserts the promoted path
// reuses its deferred boot prober (id-stamp continuity, T51). The promotion assertion
// FAILS on the pre-T55 code, which has no reconcile: a deferred path stays Down forever.
func TestReconcilePromotesDeferredPathToLive(t *testing.T) {
	psk := testKey(t, 0x55)
	clk := newFakeClock()
	paths := []config.Path{
		{Name: "bindable", SourceAddr: netip.MustParseAddr("127.0.0.1")},
		{Name: "deferred", SourceAddr: netip.MustParseAddr(unassignableSource)},
	}
	m, probers, scheduler := newProbingMultipath(t, paths, psk, clk)
	binder := &fakeDeferredBinder{}
	m.deferredListen = binder.listen

	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	// Precondition (T51): path 1 is deferred, not live.
	if len(m.paths) != 1 || len(m.deferred) != 1 {
		t.Fatalf("after Open: paths=%d deferred=%d, want 1 and 1", len(m.paths), len(m.deferred))
	}

	// Reconcile while the address is STILL unassignable: nothing is promoted, the path
	// stays deferred and is retried.
	m.reconcileDeferred()
	if len(m.paths) != 1 || len(m.deferred) != 1 {
		t.Fatalf("reconcile before the address is assignable promoted a path: paths=%d deferred=%d", len(m.paths), len(m.deferred))
	}

	// The source_addr BECOMES assignable; the next reconcile binds and promotes it.
	binder.arm()
	m.reconcileDeferred()
	if len(m.paths) != 2 {
		t.Fatalf("live paths after reconcile = %d, want 2 (deferred path promoted)", len(m.paths))
	}
	if len(m.deferred) != 0 {
		t.Fatalf("deferred set after promote = %d, want 0", len(m.deferred))
	}
	promoted := m.paths[1]
	if promoted.name != "deferred" {
		t.Fatalf("promoted path name = %q, want %q", promoted.name, "deferred")
	}
	if promoted.conn == nil {
		t.Fatal("promoted path has no socket")
	}
	// Stamp continuity (T51): the promoted path reuses its deferred boot prober, so its
	// id-stamp is unchanged and its prober is the SAME object the scheduler selects on.
	if promoted.prober != probers[1] {
		t.Fatal("promoted path did not reuse its deferred boot prober (stamp continuity lost)")
	}
	if promoted.id != probers[1].PathID() {
		t.Fatalf("promoted path id = %d, want its reserved prober stamp %d", promoted.id, probers[1].PathID())
	}

	// The promoted path is now in the SCHEDULER: bring both paths Up, blackhole the
	// original primary, and assert egress fails over to the promoted path — only possible
	// if reconcile admitted it to the scheduler as a live, selectable path.
	refl := telemetry.NewReflector(psk, rand.Reader)
	codec, _ := frame.NewCodec(psk)
	peer0, ap0 := rawPeer(t)
	peer1, ap1 := rawPeer(t)
	m.paths[0].setRemote(ap0)
	promoted.setRemote(ap1)
	for i := 0; i < testProbeUpSucc; i++ {
		probeRound(t, m, clk, refl, codec, psk,
			map[int]*net.UDPConn{0: peer0, 1: peer1}, map[int]netip.AddrPort{0: ap0, 1: ap1})
	}
	if promoted.prober.State() != telemetry.StateUp {
		t.Fatalf("promoted path state = %v, want up (probes not driving its liveness)", promoted.prober.State())
	}
	if idx := scheduler.Pick(sched.ClassData); idx != 0 {
		t.Fatalf("Pick = %d while both up, want the primary 0", idx)
	}

	// Blackhole the primary: drop its echoes, keep echoing the promoted path. After the
	// detection window the primary goes Down and the scheduler fails egress over to the
	// promoted path (index 1).
	rounds := int(testProbeDownAfter/testProbeInterval) + 3
	for i := 0; i < rounds; i++ {
		m.emitProbes()
		clk.advance(testProbeRTT)
		_ = readProbe(t, peer0, codec) // primary blackholed: drain its probe, do not echo
		probe1 := readProbe(t, peer1, codec)
		raw, err := frame.Encode(psk, probe1)
		if err != nil {
			t.Fatalf("re-encode probe: %v", err)
		}
		echo, err := refl.Reflect(raw)
		if err != nil {
			t.Fatalf("reflect promoted path: %v", err)
		}
		m.handleInbound(m.paths[1], echo, ap1)
		clk.advance(testProbeInterval - testProbeRTT)
	}
	if probers[0].State() != telemetry.StateDown {
		t.Fatalf("blackholed primary state = %v, want down", probers[0].State())
	}
	if idx := scheduler.Pick(sched.ClassData); idx != 1 {
		t.Fatalf("Pick = %d after primary blackhole, want failover to the promoted path 1", idx)
	}
	// The failover is usable end to end: a Send routes over the promoted path.
	if err := m.Send([][]byte{[]byte("post-promote")}, m.virt); err != nil {
		t.Fatalf("Send over the promoted path: %v", err)
	}
}

// TestReconcileSkipsPathRemovedBeforeBind is the race guard: a deferred path removed
// (a config reload drops it) BEFORE it ever binds must NOT be resurrected by a later
// reconcile once its address becomes assignable. RemovePath retires it from the
// deferred set, so reconcile has nothing to promote.
func TestReconcileSkipsPathRemovedBeforeBind(t *testing.T) {
	psk := testKey(t, 0x56)
	clk := newFakeClock()
	paths := []config.Path{
		{Name: "bindable", SourceAddr: netip.MustParseAddr("127.0.0.1")},
		{Name: "deferred", SourceAddr: netip.MustParseAddr(unassignableSource)},
	}
	m, _, _ := newProbingMultipath(t, paths, psk, clk)
	binder := &fakeDeferredBinder{}
	m.deferredListen = binder.listen

	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	if len(m.deferred) != 1 {
		t.Fatalf("after Open: deferred = %d, want 1", len(m.deferred))
	}
	// The still-deferred path is dropped by a reload before it ever bound.
	if err := m.RemovePath("deferred"); err != nil {
		t.Fatalf("RemovePath(deferred): %v", err)
	}
	if len(m.deferred) != 0 {
		t.Fatalf("deferred after remove = %d, want 0 (removal retires it)", len(m.deferred))
	}

	// Its address now becomes assignable; reconcile must NOT resurrect the removed path.
	binder.arm()
	m.reconcileDeferred()
	if len(m.paths) != 1 {
		t.Fatalf("live paths = %d after reconcile, want 1 (a removed deferred path must not be promoted)", len(m.paths))
	}
	for _, ps := range m.paths {
		if ps.name == "deferred" {
			t.Fatal("a path removed before it bound was promoted by reconcile")
		}
	}
}

// TestReconcileThreadsForcedDeviceBind is the T106 round-2 gap: the AddPath/deferred-
// reconcile device-mode wiring (I5) was UNVERIFIED — reconcileDeferred computes dev via
// m.resolveDeviceBind (resolveForcedDeviceBind in production) and passes it straight
// through to m.deferredListen, but nothing inspected that argument, so neutering the
// decision to always return "" (deactivating device-bind everywhere at runtime) passed
// the full bind suite. This overrides m.resolveDeviceBind to a deterministic fake (no
// real interface needed) and asserts the fake's decision is threaded, per deferred
// path, into deferredListen's dev parameter: a BindModeDevice path must receive the
// resolved device, while a BindModeAuto sibling must still receive "" (AddPath/
// reconcile's pre-I5 behaviour, D30, is unchanged by this task). It FAILS if the
// threading is dropped anywhere between resolveDeviceBind and deferredListen.
func TestReconcileThreadsForcedDeviceBind(t *testing.T) {
	psk := testKey(t, 0x59)
	clk := newFakeClock()
	paths := []config.Path{
		{Name: "bindable", SourceAddr: netip.MustParseAddr("127.0.0.1")},
		{Name: "deferred-auto", SourceAddr: netip.MustParseAddr(unassignableSource), Bind: config.BindModeAuto},
		{Name: "deferred-device", SourceAddr: netip.MustParseAddr(unassignableSource), Bind: config.BindModeDevice},
	}
	m, _, _ := newProbingMultipath(t, paths, psk, clk)
	binder := &fakeDeferredBinder{}
	m.deferredListen = binder.listen
	// Deterministic fake decision: BindModeDevice resolves to "wan0"; every other mode
	// yields "" — exactly resolveForcedDeviceBind's real contract, without touching
	// net.Interfaces().
	m.resolveDeviceBind = func(_ netip.Addr, mode config.BindMode) string {
		if mode == config.BindModeDevice {
			return "wan0"
		}
		return ""
	}

	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	if len(m.deferred) != 2 {
		t.Fatalf("after Open: deferred = %d, want 2", len(m.deferred))
	}

	binder.arm()
	m.reconcileDeferred()

	if len(m.paths) != 3 || len(m.deferred) != 0 {
		t.Fatalf("after reconcile: paths=%d deferred=%d, want 3 and 0", len(m.paths), len(m.deferred))
	}
	if len(binder.devs) != 2 {
		t.Fatalf("deferredListen called %d times, want 2", len(binder.devs))
	}
	// binder.devs is in m.deferred's iteration order, which matches the order the two
	// deferred defs were appended in Open (deferred-auto then deferred-device);
	// m.paths' promotion order matches too (reconcileDeferred iterates m.deferred in
	// order and appends each promotion to m.paths) — asserted below.
	if binder.devs[0] != "" {
		t.Fatalf("deferred-auto path's threaded dev = %q, want \"\" (I5 AddPath/reconcile pre-existing auto behaviour unchanged)", binder.devs[0])
	}
	if binder.devs[1] != "wan0" {
		t.Fatalf("deferred-device path's threaded dev = %q, want %q (resolveDeviceBind's decision was not threaded into deferredListen)", binder.devs[1], "wan0")
	}
	if m.paths[1].name != "deferred-auto" || m.paths[2].name != "deferred-device" {
		t.Fatalf("promoted path order = [%q %q], want [deferred-auto deferred-device]", m.paths[1].name, m.paths[2].name)
	}
}

// TestReconcileLoopStopsCleanly asserts the background reconcile goroutine terminates
// on its stopper with no goroutine leak (the shutdown contract Close relies on), and
// that the stopper is idempotent. goleak.VerifyNone fails if the loop goroutine
// outlives stop().
func TestReconcileLoopStopsCleanly(t *testing.T) {
	defer goleak.VerifyNone(t)
	psk := testKey(t, 0x57)
	clk := newFakeClock()
	m, _, _ := newProbingMultipath(t, loopbackPaths(1), psk, clk)
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}

	stop := m.StartReconcileLoop(1 * time.Millisecond)
	time.Sleep(10 * time.Millisecond) // let it tick a few times against the empty deferred set
	stop()
	stop() // idempotent

	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// TestReconcileLoopNoopWithoutProbeTransport asserts a bind without the probe transport
// (no probers — the T12 unit binds, which never defer) gets a no-op reconcile loop and
// stopper, so nothing is started to leak.
func TestReconcileLoopNoopWithoutProbeTransport(t *testing.T) {
	defer goleak.VerifyNone(t)
	psk := testKey(t, 0x58)
	m, err := newMultipath(t, loopbackPaths(1), psk)
	if err != nil {
		t.Fatalf("NewMultipath: %v", err)
	}
	stop := m.StartReconcileLoop(1 * time.Millisecond)
	stop() // must be safe: no goroutine was started
}
