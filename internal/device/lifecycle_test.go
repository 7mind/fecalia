package device

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	awgdevice "github.com/amnezia-vpn/amneziawg-go/device"
	"github.com/amnezia-vpn/amneziawg-go/tun/tuntest"
	"go.uber.org/goleak"

	"github.com/7mind/wanbond/internal/bind"
	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/dnsresolve"
	"github.com/7mind/wanbond/internal/sched"
	"github.com/7mind/wanbond/internal/telemetry"
)

// genX25519 returns a fresh X25519 private/public key pair as raw 32-byte slices — valid curve
// material so the engine's CreateMessageInitiation (a DH against the peer static key) succeeds.
func genX25519(t *testing.T) (privRaw, pubRaw []byte) {
	t.Helper()
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate X25519 key: %v", err)
	}
	return k.Bytes(), k.PublicKey().Bytes()
}

// keyFromRaw builds a config.Key from raw key bytes (via the only public constructor,
// UnmarshalText over base64), so a test can pass the SAME key to the engine (as hex UAPI) and to
// deviceRehandshake/deviceInstallEndpoint (as a config.Key).
func keyFromRaw(t *testing.T, raw []byte) config.Key {
	t.Helper()
	var k config.Key
	if err := k.UnmarshalText([]byte(base64.StdEncoding.EncodeToString(raw))); err != nil {
		t.Fatalf("build config.Key: %v", err)
	}
	return k
}

// randB64Key returns 32 random bytes base64-encoded (a placeholder PSK / key for a config file).
func randB64Key(t *testing.T) string {
	t.Helper()
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("read random: %v", err)
	}
	return base64.StdEncoding.EncodeToString(b[:])
}

// writeEdgeConfig writes a minimal single-path edge config to a 0600 temp file and loads it. The
// endpoints TOML array literal (e.g. `["hub.example.com:51820"]`) and the dns opt-in are the only
// per-test variation; every other field is a fixed loopback default.
func writeEdgeConfig(t *testing.T, endpointsTOML string, dns bool) *config.Config {
	t.Helper()
	privRaw, _ := genX25519(t)
	_, pubRaw := genX25519(t)
	dnsLine := ""
	if dns {
		dnsLine = "dns = true\n"
	}
	body := fmt.Sprintf(`role = "edge"
psk = "%s"

[[paths]]
name = "a"
source_addr = "127.0.0.1"

[wireguard]
private_key = "%s"

[[wireguard.peers]]
public_key = "%s"
%sendpoints = %s
allowed_ips = ["10.0.0.0/24"]
`, randB64Key(t),
		base64.StdEncoding.EncodeToString(privRaw),
		base64.StdEncoding.EncodeToString(pubRaw),
		dnsLine, endpointsTOML)

	path := filepath.Join(t.TempDir(), "edge.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil { // defeat umask widening (config.Load requires 0600 exactly)
		t.Fatalf("chmod config: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	return cfg
}

// neverResolver is a resolver whose every lookup returns an error — a name that never resolves.
type neverResolver struct{ calls int }

func (r *neverResolver) Lookup(_ context.Context, host string) ([]netip.Addr, time.Duration, bool, error) {
	r.calls++
	return nil, 0, false, fmt.Errorf("neverResolver: %q does not resolve", host)
}

// flakyThenResolver FAILS its first lookup (the bounded boot resolve — so the peer boots
// endpoint-less) then resolves host to addr on every subsequent lookup (the re-resolution loop's
// first successful poll). It models a name unresolvable in the boot window that comes good once the
// loop runs, so the endpoint reaches the engine peer only via the FIRST-RESOLVE INSTALL PATH (R70).
// calls is touched only on the boot goroutine (call 1) then the single resolution-loop goroutine
// (calls 2+), ordered by the loop's goroutine start — no concurrent access, so no lock is needed.
type flakyThenResolver struct {
	host  string
	addr  netip.Addr
	calls int
}

func (r *flakyThenResolver) Lookup(_ context.Context, host string) ([]netip.Addr, time.Duration, bool, error) {
	r.calls++
	if r.calls == 1 {
		return nil, 0, false, fmt.Errorf("flakyThenResolver: boot lookup fails for %q", host)
	}
	if host != r.host {
		return nil, 0, false, fmt.Errorf("flakyThenResolver: no such host %q", host)
	}
	return []netip.Addr{r.addr}, 0, false, nil
}

// TestUpTolerantBootEndpointless is acceptance (1): Up on a single-hostname peer whose name NEVER
// resolves must SUCCEED (tolerant boot, Q30 defer-and-reconcile — never hard-fail on an
// unresolvable name), and the engine peer must carry NO endpoint (UAPI get shows no endpoint=
// line) — the endpoint-less boot the re-resolution loop later completes on first success.
func TestUpTolerantBootEndpointless(t *testing.T) {
	cfg := writeEdgeConfig(t, `["hub.example.com:51820"]`, true)
	chtun := tuntest.NewChannelTUN()

	rslv := &neverResolver{}
	factoryCalls := 0
	factory := func() (dnsresolve.Resolver, error) {
		factoryCalls++
		return rslv, nil
	}

	tun, err := up(cfg, discardLogger(t), chtun.TUN(), "wanbondtest0", factory)
	if err != nil {
		t.Fatalf("up on a never-resolving single-hostname peer failed, want tolerant boot: %v", err)
	}
	defer tun.Close()

	if factoryCalls != 1 {
		t.Fatalf("resolver factory called %d times for a hostname peer, want exactly 1", factoryCalls)
	}
	if rslv.calls == 0 {
		t.Fatalf("bounded initial resolve never attempted a lookup, want >= 1")
	}

	uapi, err := tun.dev.IpcGet()
	if err != nil {
		t.Fatalf("IpcGet: %v", err)
	}
	if strings.Contains(uapi, "endpoint=") {
		t.Fatalf("tolerant boot on an unresolvable hostname installed an endpoint, want NONE (endpoint-less boot):\n%s", uapi)
	}
}

// TestUpZeroHostnameNoResolverNoLoop is acceptance (4): a config with ZERO hostname specs (an
// all-literal edge peer) constructs NO resolver and starts NO re-resolution loop — asserted via
// the wiring seam (the resolver factory is never invoked). The literal endpoint is still installed
// on the engine peer at boot, so a single-IP-literal deployment is byte-for-byte the pre-G5 render.
func TestUpZeroHostnameNoResolverNoLoop(t *testing.T) {
	cfg := writeEdgeConfig(t, `["127.0.0.1:51821"]`, false)
	chtun := tuntest.NewChannelTUN()

	factoryCalls := 0
	factory := func() (dnsresolve.Resolver, error) {
		factoryCalls++
		return &dnsresolve.FakeResolver{Hosts: map[string][]netip.Addr{}}, nil
	}

	tun, err := up(cfg, discardLogger(t), chtun.TUN(), "wanbondtest0", factory)
	if err != nil {
		t.Fatalf("up on an all-literal edge peer failed: %v", err)
	}
	defer tun.Close()

	if factoryCalls != 0 {
		t.Fatalf("resolver factory invoked %d times for a zero-hostname config, want 0 (provably inert, Q29)", factoryCalls)
	}

	uapi, err := tun.dev.IpcGet()
	if err != nil {
		t.Fatalf("IpcGet: %v", err)
	}
	if !strings.Contains(uapi, "endpoint=127.0.0.1:51821") {
		t.Fatalf("literal endpoint not installed at boot, want endpoint=127.0.0.1:51821:\n%s", uapi)
	}
}

// tripwireResolver is a dnsresolve.Resolver whose Lookup FAILS the test the instant it is
// EVER called — the Q29/G5 tripwire (T76). t.Errorf (unlike t.Fatalf/FailNow) is safe to
// call from any goroutine, so this catches a Lookup invoked either from the SYNCHRONOUS
// bounded boot resolve (resolveBootEndpoints, on the test's own goroutine) or from an
// asynchronously started re-resolution loop goroutine (startResolutionLoop).
type tripwireResolver struct{ t *testing.T }

func (r *tripwireResolver) Lookup(_ context.Context, host string) ([]netip.Addr, time.Duration, bool, error) {
	r.t.Errorf("Q29 TRIPWIRE: dnsresolve.Resolver.Lookup(%q) called on an all-IP-literal config — "+
		"DNS must stay PROVABLY inert when no peer opts in `dns = true` (zero DNS egress)", host)
	return nil, 0, false, fmt.Errorf("tripwireResolver: unexpected lookup for %q", host)
}

// TestUpAllLiteralTripwireNeverCallsLookup hardens TestUpZeroHostnameNoResolverNoLoop's
// factory-call-count proof (T74) with a direct, stronger guard (T76 / Q29): on an
// all-IP-literal config (every peer endpoint spec is an IP:port, no `dns = true` opt-in),
// the injected resolver factory returns a tripwireResolver whose Lookup fails the test the
// instant it is invoked — from ANY call site, sync or async — rather than relying solely on
// an after-the-fact call count. It also re-asserts the factory itself is never invoked
// (Q29: no dnsresolve.Resolver may even be CONSTRUCTED for a literal config), and lets the
// tunnel run past several probe cadences (long enough for a wrongly-started re-resolution
// goroutine to have ticked and called Lookup at least once) before closing.
//
// Mutation check: wiring a resolver (and driving a lookup through it) UNCONDITIONALLY —
// dropping the hasName/resolver!=nil gates in resolveBootEndpoints/startResolution — makes
// this test fail, both via the factory-call count and via the tripwire's Lookup failure.
func TestUpAllLiteralTripwireNeverCallsLookup(t *testing.T) {
	cfg := writeEdgeConfig(t, `["127.0.0.1:51821"]`, false)
	chtun := tuntest.NewChannelTUN()

	factoryCalls := 0
	factory := func() (dnsresolve.Resolver, error) {
		factoryCalls++
		return &tripwireResolver{t: t}, nil
	}

	tun, err := up(cfg, discardLogger(t), chtun.TUN(), "wanbondtest0", factory)
	if err != nil {
		t.Fatalf("up on an all-literal edge peer failed: %v", err)
	}
	// Let any wrongly-started re-resolution loop tick at least once before asserting and
	// closing: a mutant that starts the loop asynchronously (rather than resolving inline)
	// would otherwise race a bare post-Up assertion.
	time.Sleep(3 * telemetry.DefaultProbeInterval)
	tun.Close()

	if factoryCalls != 0 {
		t.Fatalf("resolver factory invoked %d times for a zero-hostname config, want 0 (provably inert, Q29)", factoryCalls)
	}
}

// TestCloseStopsResolutionLoopNoLeak is acceptance (3): Close stops the re-resolution loop (and the
// probe/reconcile/hub-failover loops) with no goroutine leak. goleak verifies zero surviving
// goroutines AFTER Close, under -race.
func TestCloseStopsResolutionLoopNoLeak(t *testing.T) {
	defer goleak.VerifyNone(t) // deferred first ⇒ runs LAST, after tun.Close below

	cfg := writeEdgeConfig(t, `["hub.example.com:51820"]`, true)
	chtun := tuntest.NewChannelTUN()

	factory := func() (dnsresolve.Resolver, error) {
		return &dnsresolve.FakeResolver{Hosts: map[string][]netip.Addr{}}, nil
	}

	tun, err := up(cfg, discardLogger(t), chtun.TUN(), "wanbondtest0", factory)
	if err != nil {
		t.Fatalf("up: %v", err)
	}
	// Let the loops run a few probe cadences so their goroutines are genuinely live before Close.
	time.Sleep(3 * telemetry.DefaultProbeInterval)
	tun.Close()
	// A second Close is idempotent and must not double-stop or panic.
	tun.Close()
}

// TestFirstResolveInstallsEndpointAndInitiatesHandshake is acceptance (2), the R70 FIRST-RESOLVE
// INSTALL PATH end to end against a REAL engine + real multipath bind (channel TUN, loopback
// socket). A hostname-only peer boots endpoint-less; the first successful resolve must (a) INSTALL
// the resolved endpoint on the engine peer via the UAPI/IpcSet path — so the engine peer reports it
// through UAPI get — AND (b) rehandshake, so a real handshake initiation actually egresses toward
// the resolved address (observed at a loopback listener). A fake-rehandshake-counter increment
// alone is NOT sufficient (R70): SetPeerRemote never populates the engine peer endpoint, so without
// the install the initiation would have no endpoint to transmit to.
func TestFirstResolveInstallsEndpointAndInitiatesHandshake(t *testing.T) {
	lg := discardLogger(t)

	// A loopback UDP listener stands in for the resolved concentrator address.
	conc, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("listen concentrator socket: %v", err)
	}
	defer conc.Close()
	concAP := netip.MustParseAddrPort(conc.LocalAddr().String())

	// Real multipath bind over one loopback path with an AlwaysUp scheduler, so the scheduler never
	// gates the initiation (a real prober would boot DOWN with no responder and Send would drop it).
	psk := keyFromRaw(t, mustRandom(t, 32))
	paths := []config.Path{{Name: "a", SourceAddr: netip.MustParseAddr("127.0.0.1")}}
	scheduler, err := sched.NewActiveBackup([]sched.PathHealth{sched.AlwaysUp{}}, sched.Config{FailbackAfter: time.Second}, telemetry.SystemClock{}, lg)
	if err != nil {
		t.Fatalf("build scheduler: %v", err)
	}
	mp, err := bind.NewMultipath(paths, psk, scheduler, nil, nil, nil, nil, config.Amnezia{})
	if err != nil {
		t.Fatalf("build multipath bind: %v", err)
	}

	// Real engine over a channel TUN, brought up endpoint-less (no endpoint= line).
	chtun := tuntest.NewChannelTUN()
	dev := awgdevice.NewDevice(chtun.TUN(), mp, engineLogger(lg, "error", mp.EverHadLivePath))
	defer dev.Close()

	edgePrivRaw, _ := genX25519(t)
	_, hubPubRaw := genX25519(t)
	var uapi strings.Builder
	fmt.Fprintf(&uapi, "private_key=%s\n", hex.EncodeToString(edgePrivRaw))
	fmt.Fprintf(&uapi, "public_key=%s\n", hex.EncodeToString(hubPubRaw))
	fmt.Fprintf(&uapi, "allowed_ip=0.0.0.0/0\n")
	if err := dev.IpcSet(uapi.String()); err != nil {
		t.Fatalf("IpcSet endpoint-less crypto config: %v", err)
	}
	if err := dev.Up(); err != nil {
		t.Fatalf("dev.Up: %v", err)
	}

	// The controller as the device wires it: install to the engine peer's UAPI endpoint path,
	// rehandshake to the engine peer, repoint to the bind. A single hostname spec, EMPTY at boot.
	hubKey := keyFromRaw(t, hubPubRaw)
	specs := []failoverSpec{nameSpec("hub.example.com", 51820)}
	hp := []hubHealth{&fakeHealth{telemetry.StateUp}}
	clk := &fakeClock{now: time.Unix(1000, 0)}
	ctrl := newHubFailoverFromSpecs(specs, hp, mp, deviceRehandshake(dev, hubKey), deviceInstallEndpoint(dev, hubKey, lg), clk, testSettle, lg)

	// Before any resolution the engine peer has NO endpoint.
	if before, err := dev.IpcGet(); err != nil {
		t.Fatalf("IpcGet (pre-resolve): %v", err)
	} else if strings.Contains(before, "endpoint=") {
		t.Fatalf("engine peer had an endpoint before the first resolve, want none:\n%s", before)
	}

	// FIRST successful resolve: boot adoption must install on the engine peer and rehandshake.
	ctrl.updateResolution(0, []netip.AddrPort{concAP})

	// (a) The engine peer now reports the resolved endpoint via UAPI get.
	after, err := dev.IpcGet()
	if err != nil {
		t.Fatalf("IpcGet (post-resolve): %v", err)
	}
	if !strings.Contains(after, "endpoint="+concAP.String()) {
		t.Fatalf("engine peer endpoint not installed on first resolve, want endpoint=%s:\n%s", concAP, after)
	}

	// (b) A real handshake initiation actually egressed toward the resolved address.
	if err := conc.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	buf := make([]byte, 4096)
	n, from, err := conc.ReadFromUDPAddrPort(buf)
	if err != nil {
		t.Fatalf("no handshake initiation observed at the resolved address %s (R70: install alone is not enough — the rehandshake must egress): %v", concAP, err)
	}
	if n == 0 {
		t.Fatalf("observed a zero-length datagram from %s, want a framed handshake initiation", from)
	}
}

// TestUpFirstResolveInstallsEndpointThroughProductionWiring drives the R70 FIRST-RESOLVE INSTALL
// PATH end to end through up() — the PRODUCTION wiring in startFailoverAndResolution — rather than a
// hand-wired controller (which the sibling TestFirstResolveInstallsEndpointAndInitiatesHandshake
// exercises for the egress half). A single-hostname peer whose name FAILS the bounded boot resolve
// boots endpoint-less (tolerant boot); the re-resolution loop's first successful poll must boot-adopt
// through the install collaborator up() wires (deviceInstallEndpoint), populating the ENGINE peer's
// endpoint — observable via IpcGet. This fails fast if that one production wiring line is ever lost:
// without ctrl.install the boot adoption could only reach the bind's per-path remotes, never the
// engine peer endpoint, so IpcGet would never gain the resolved endpoint. No probe responder is
// needed — boot adoption is not liveness-gated; it fires on the loop's first successful resolve
// (nextPollAt armed at construction, stepped at DefaultProbeInterval), well within the 2s bound.
func TestUpFirstResolveInstallsEndpointThroughProductionWiring(t *testing.T) {
	cfg := writeEdgeConfig(t, `["hub.example.com:51820"]`, true)
	chtun := tuntest.NewChannelTUN()

	resolved := netip.MustParseAddr("203.0.113.5")
	rslv := &flakyThenResolver{host: "hub.example.com", addr: resolved}
	factory := func() (dnsresolve.Resolver, error) { return rslv, nil }

	tun, err := up(cfg, discardLogger(t), chtun.TUN(), "wanbondtest0", factory)
	if err != nil {
		t.Fatalf("up on a hostname peer whose boot resolve fails, want tolerant boot: %v", err)
	}
	defer tun.Close()

	// Endpoint-less at boot: the bounded boot lookup failed, so no endpoint line yet.
	if before, err := tun.dev.IpcGet(); err != nil {
		t.Fatalf("IpcGet (pre-resolve): %v", err)
	} else if strings.Contains(before, "endpoint=") {
		t.Fatalf("engine peer carried an endpoint before the first successful resolve, want none:\n%s", before)
	}

	// The re-resolution loop resolves on its first poll and boot-adopts through the production
	// install wiring; poll IpcGet until the engine peer reports the resolved endpoint.
	want := "endpoint=" + netip.AddrPortFrom(resolved, 51820).String()
	deadline := time.Now().Add(2 * time.Second)
	for {
		uapi, err := tun.dev.IpcGet()
		if err != nil {
			t.Fatalf("IpcGet: %v", err)
		}
		if strings.Contains(uapi, want) {
			break
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("engine peer endpoint not installed through production wiring within 2s, want %q (R70 install path via startFailoverAndResolution):\n%s", want, uapi)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// mustRandom returns n random bytes or fails the test.
func mustRandom(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("read random: %v", err)
	}
	return b
}
