package bind

import (
	"crypto/rand"
	"net"
	"net/netip"
	"testing"

	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/frame"
	"github.com/7mind/wanbond/internal/sched"
	"github.com/7mind/wanbond/internal/telemetry"
)

// unassignableSource is a well-formed source address that is NOT present on any
// interface, so net.ListenUDP binding it fails with EADDRNOTAVAIL ("cannot assign
// requested address"). 192.0.2.0/24 is TEST-NET-1 (RFC 5737), reserved for
// documentation and guaranteed never to be configured on a real interface, so the
// bind failure is deterministic across hosts. This is the well-formed-but-not-yet-
// assignable case tolerant Open() must survive (a mobile edge whose 5G modem has no
// DHCP lease yet at boot).
const unassignableSource = "192.0.2.1"

// TestOpenToleratesUnassignableSourceAddr is the T51 acceptance case (i): with one
// bindable path and one whose well-formed source_addr is not-yet-assignable
// (EADDRNOTAVAIL), Open() brings the tunnel up on the bindable path instead of
// tearing the whole bond down, and the unbindable path is recorded DEFERRED and
// reported DOWN (its prober never echoes) so the scheduler excludes it. This test
// FAILS on the pre-T51 Open(), which returns fatally on the first per-path bind error.
func TestOpenToleratesUnassignableSourceAddr(t *testing.T) {
	psk := testKey(t, 0x51)
	clk := newFakeClock()
	paths := []config.Path{
		{Name: "bindable", SourceAddr: netip.MustParseAddr("127.0.0.1")},
		{Name: "deferred", SourceAddr: netip.MustParseAddr(unassignableSource)},
	}
	m, probers, scheduler := newProbingMultipath(t, paths, psk, clk)

	_, _, err := m.Open(0)
	if err != nil {
		t.Fatalf("Open returned fatal error on one bindable + one not-yet-assignable path: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	// Only the bindable path is live.
	if len(m.paths) != 1 {
		t.Fatalf("live paths = %d, want 1 (only the bindable path)", len(m.paths))
	}
	if m.paths[0].name != "bindable" {
		t.Fatalf("live path = %q, want %q", m.paths[0].name, "bindable")
	}

	// The unbindable path is PRESENT in the deferred set and marked DOWN (its prober,
	// never fed an echo, stays StateDown) so the scheduler excludes it — the runtime
	// path-down model reused at boot. This is the state T55's background reconcile
	// retries: a recorded not-yet-bound path def plus its (Down) prober.
	if len(m.deferred) != 1 {
		t.Fatalf("deferred paths = %d, want 1", len(m.deferred))
	}
	if m.deferred[0].def.Name != "deferred" {
		t.Fatalf("deferred path = %q, want %q", m.deferred[0].def.Name, "deferred")
	}
	if m.deferred[0].prober != probers[1] {
		t.Fatal("deferred path did not retain its boot-time prober (T55 must reuse the same stamp)")
	}
	if got := m.deferred[0].prober.State(); got != telemetry.StateDown {
		t.Fatalf("deferred path liveness = %v, want down", got)
	}

	// The scheduler was reconciled to the BOUND paths only, so once the bindable path
	// is healthy Pick selects it (index 0) and can never index the deferred path.
	refl := telemetry.NewReflector(psk, rand.Reader)
	codec, _ := frame.NewCodec(psk)
	peer0, ap0 := rawPeer(t)
	m.paths[0].setRemote(ap0)
	for i := 0; i < testProbeUpSucc; i++ {
		probeRound(t, m, clk, refl, codec, psk,
			map[int]*net.UDPConn{0: peer0}, map[int]netip.AddrPort{0: ap0})
	}
	if probers[0].State() != telemetry.StateUp {
		t.Fatalf("bindable path state = %v, want up", probers[0].State())
	}
	if idx := scheduler.Pick(sched.ClassData, 1); idx != 0 {
		t.Fatalf("Pick = %d, want 0 (the sole bound path)", idx)
	}
}

// TestOpenFailsWhenNoPathBinds is the T51 acceptance case (ii): if EVERY configured
// path's source_addr is not-yet-assignable, Open() STILL fails fatally — no transport
// means no tunnel (hard guard a). Tolerance never degrades to a zero-path bond.
func TestOpenFailsWhenNoPathBinds(t *testing.T) {
	psk := testKey(t, 0x52)
	clk := newFakeClock()
	paths := []config.Path{
		{Name: "a", SourceAddr: netip.MustParseAddr("192.0.2.1")},
		{Name: "b", SourceAddr: netip.MustParseAddr("192.0.2.2")},
	}
	m, _, _ := newProbingMultipath(t, paths, psk, clk)

	if _, _, err := m.Open(0); err == nil {
		_ = m.Close()
		t.Fatal("Open succeeded with zero bindable paths, want a fatal error (no transport => no tunnel)")
	}
	if len(m.paths) != 0 {
		t.Fatalf("live paths = %d after a failed Open, want 0", len(m.paths))
	}
}

// TestOpenFailsOnAddrInUse is the T51 acceptance case (iii): a bind error that is NOT
// EADDRNOTAVAIL — here EADDRINUSE (the source_addr:port is already occupied) — stays
// FATAL even when another path binds successfully. Only a not-yet-assignable address
// is tolerated; a genuine bind fault is not papered over.
func TestOpenFailsOnAddrInUse(t *testing.T) {
	psk := testKey(t, 0x53)
	clk := newFakeClock()

	// Occupy 127.0.0.5:P so a path pinned to that exact source+port collides with
	// EADDRINUSE. The blocker takes an ephemeral port P, which Open() then reuses for
	// every path (Open binds all paths on the port it is handed).
	blocker, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.5")})
	if err != nil {
		t.Fatalf("occupy blocker socket: %v", err)
	}
	defer blocker.Close()
	port := uint16(blocker.LocalAddr().(*net.UDPAddr).Port)

	paths := []config.Path{
		{Name: "bindable", SourceAddr: netip.MustParseAddr("127.0.0.4")},
		{Name: "inuse", SourceAddr: netip.MustParseAddr("127.0.0.5")},
	}
	m, _, _ := newProbingMultipath(t, paths, psk, clk)

	if _, _, err := m.Open(port); err == nil {
		_ = m.Close()
		t.Fatal("Open succeeded despite an EADDRINUSE path, want a fatal error (only EADDRNOTAVAIL is tolerated)")
	}
	if len(m.paths) != 0 {
		t.Fatalf("live paths = %d after a failed Open, want 0 (bound survivor must be rolled back)", len(m.paths))
	}
}
