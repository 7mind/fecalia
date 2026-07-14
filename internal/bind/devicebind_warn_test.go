package bind

import (
	"bytes"
	"errors"
	"net"
	"net/netip"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/log"
	"github.com/7mind/wanbond/internal/sched"
	"github.com/7mind/wanbond/internal/telemetry"
)

// syncBuffer is a concurrency-safe bytes.Buffer (mirrors internal/device's test
// helper of the same name) so a logger write from under m.mu never races the
// test's read of the captured JSON log text.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// newWarnCapturingMultipath builds an OPEN, runtime-path-capable Multipath (probers +
// newProber + a DynamicScheduler — AddPath's preconditions) whose logger writes JSON
// records into the returned buffer at "info" level (so the D53 AUTO-fallback
// informational record is captured too, not just WARN/ERROR). It is the D53 test
// harness: every scenario below drives AddPath/reconcileDeferred over paths[] and then
// inspects buf for the forced-device-bind fallback WARN.
func newWarnCapturingMultipath(t *testing.T, paths []config.Path, psk config.Key) (*Multipath, *syncBuffer) {
	t.Helper()
	buf := &syncBuffer{}
	lg, err := log.New("info", buf)
	if err != nil {
		t.Fatalf("log.New: %v", err)
	}
	clk := telemetry.SystemClock{}
	cfg := telemetry.ProberConfig{
		LossWindow: 0,
		Liveness:   telemetry.LivenessConfig{DownAfter: testProbeDownAfter, UpAfterSuccesses: testProbeUpSucc},
	}
	newProber := func(name string, id uint8) *telemetry.Prober {
		return telemetry.NewProber(name, id, testProbeSessionID, psk, cfg, clk, lg)
	}
	probers := make([]*telemetry.Prober, len(paths))
	health := make([]sched.PathHealth, len(paths))
	for i := range paths {
		probers[i] = newProber(paths[i].Name, uint8(i))
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
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m, buf
}

// warnCount reports how many WARN-level JSON records appear in s.
func warnCount(s string) int {
	return strings.Count(s, `"level":"WARN"`)
}

// TestOpenWarnsOnUnresolvableForcedDeviceBind is the D53 layer-(a) acceptance check at
// Open's call site: a boot-time path configured bind="device" whose source address
// resolves to NO interface (unassignableSource — RFC 5737 TEST-NET-1, present on no
// real host) makes Open's planPathBinds/selectForcedDeviceBind fallback SILENT
// pre-D53. It must now log exactly ONE WARN naming the path and the (empty, since
// unresolvable) interface. Its source-IP-pin fallback attempt ALSO fails here
// (unassignableSource binds nowhere), so this exercises warnForcedDeviceStillDeferred
// (D53 round 2 / FIX 2), not the fallback-succeeded warnForcedDeviceUnresolvable — the
// distinction the message text carries, not the field shape this test asserts on.
// FAILS on the pre-D53 code, which never calls either warn helper.
func TestOpenWarnsOnUnresolvableForcedDeviceBind(t *testing.T) {
	psk := testKey(t, 0xD1)
	paths := []config.Path{
		{Name: "forced-unresolvable", SourceAddr: netip.MustParseAddr(unassignableSource), Bind: config.BindModeDevice},
		{Name: "bound", SourceAddr: netip.MustParseAddr("127.0.0.1")},
	}
	_, buf := newWarnCapturingMultipath(t, paths, psk)

	got := buf.String()
	if n := warnCount(got); n != 1 {
		t.Fatalf("WARN count = %d, want 1; log:\n%s", n, got)
	}
	if !strings.Contains(got, `"path":"forced-unresolvable"`) {
		t.Fatalf("WARN does not name the path; log:\n%s", got)
	}
	if !strings.Contains(got, `"interface":""`) {
		t.Fatalf("WARN does not name the (empty, unresolvable) interface; log:\n%s", got)
	}
	// D53 round 2 / FIX 2: the fallback bind FAILED too (unassignableSource), so the
	// WARN must NOT claim a source-IP-pinning fallback happened.
	if strings.Contains(got, "falling back to source-IP pinning") {
		t.Fatalf("WARN falsely claims a fallback that never materialized; log:\n%s", got)
	}
}

// TestAddPathWarnsOnUnresolvableForcedDeviceBind is the D53 layer-(a) acceptance check
// at AddPath's call site (a runtime path admission, distinct from Open's), driven via
// the REAL (unmocked) resolveDeviceBind seam so this exercises the actual
// resolveForcedDeviceBind decision, not a fake. FAILS on the pre-D53 code.
func TestAddPathWarnsOnUnresolvableForcedDeviceBind(t *testing.T) {
	psk := testKey(t, 0xD2)
	m, buf := newWarnCapturingMultipath(t, loopbackPaths(1), psk)

	def := config.Path{Name: "forced-unresolvable", SourceAddr: netip.MustParseAddr(unassignableSource), Bind: config.BindModeDevice}
	if err := m.AddPath(def); err != nil {
		t.Fatalf("AddPath: %v", err)
	}
	// The unresolvable source_addr also fails the source-IP-pin fallback bind itself
	// (EADDRNOTAVAIL — nothing on this host owns 192.0.2.1), so AddPath defers the path
	// rather than binding it live; the WARN under test fires BEFORE that bind attempt,
	// independent of whether the fallback bind itself later succeeds.
	if len(m.deferred) != 1 {
		t.Fatalf("precondition: deferred=%d, want 1 (AddPath should defer an unassignable source)", len(m.deferred))
	}

	got := buf.String()
	if n := warnCount(got); n != 1 {
		t.Fatalf("WARN count = %d, want 1; log:\n%s", n, got)
	}
	if !strings.Contains(got, `"path":"forced-unresolvable"`) {
		t.Fatalf("WARN does not name the path; log:\n%s", got)
	}
	if strings.Contains(got, "falling back to source-IP pinning") {
		t.Fatalf("WARN falsely claims a fallback that never materialized (path stays deferred); log:\n%s", got)
	}
}

// TestAddPathWarnsOnFailingDeviceSetsockoptFallback is the D53 layer-(b) acceptance
// check: a forced bind="device" path whose interface DOES resolve (via a fake
// m.resolveDeviceBind) but whose SO_BINDTODEVICE bind fails (via a fake
// m.addPathListen returning a non-nil deviceErr) must log exactly ONE WARN naming the
// path and the resolved interface — and the returned conn is a REAL, working
// loopback socket standing in for the source-IP-pinned fallback (D53's "the fallback
// still returns a working source-IP-bound socket" requirement). FAILS on the pre-D53
// code, which has no deviceErr return to log at all.
func TestAddPathWarnsOnFailingDeviceSetsockoptFallback(t *testing.T) {
	psk := testKey(t, 0xD3)
	m, buf := newWarnCapturingMultipath(t, loopbackPaths(1), psk)

	m.resolveDeviceBind = func(_ netip.Addr, mode config.BindMode) string {
		if mode == config.BindModeDevice {
			return "wan0"
		}
		return ""
	}
	var listenedSrc netip.Addr
	m.addPathListen = func(src netip.Addr, _ uint16, dev string) (*net.UDPConn, error, error) {
		if dev == "" {
			return nil, nil, errors.New("addPathListen: unexpected empty dev in this scenario")
		}
		listenedSrc = src
		// The "fallback still works" requirement: a REAL, bound, working UDP socket,
		// exactly as listenPath's source-IP-pin fallback produces on a real setsockopt
		// failure — paired with a non-nil deviceErr (the swallowed SO_BINDTODEVICE cause).
		c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.2"), Port: 0})
		if err != nil {
			return nil, nil, err
		}
		return c, errors.New("wan0: operation not permitted"), nil
	}

	def := config.Path{Name: "forced-setsockopt-fail", SourceAddr: netip.MustParseAddr("127.0.0.2"), Bind: config.BindModeDevice}
	if err := m.AddPath(def); err != nil {
		t.Fatalf("AddPath: %v", err)
	}
	if listenedSrc != def.SourceAddr {
		t.Fatalf("addPathListen src = %s, want %s", listenedSrc, def.SourceAddr)
	}
	if len(m.paths) != 2 {
		t.Fatalf("path count = %d, want 2 (the fallback conn must admit a LIVE path, not defer)", len(m.paths))
	}

	got := buf.String()
	if n := warnCount(got); n != 1 {
		t.Fatalf("WARN count = %d, want 1; log:\n%s", n, got)
	}
	if !strings.Contains(got, `"path":"forced-setsockopt-fail"`) {
		t.Fatalf("WARN does not name the path; log:\n%s", got)
	}
	if !strings.Contains(got, `"interface":"wan0"`) {
		t.Fatalf("WARN does not name the resolved interface; log:\n%s", got)
	}
}

// TestAddPathNoWarnOnSuccessfulDeviceBind is the required negative: a forced
// bind="device" path whose interface resolves AND whose bind succeeds (deviceErr ==
// nil) must log NO WARN at all — the fallback machinery never fired.
func TestAddPathNoWarnOnSuccessfulDeviceBind(t *testing.T) {
	psk := testKey(t, 0xD4)
	m, buf := newWarnCapturingMultipath(t, loopbackPaths(1), psk)

	m.resolveDeviceBind = func(_ netip.Addr, mode config.BindMode) string {
		if mode == config.BindModeDevice {
			return "wan0"
		}
		return ""
	}
	m.addPathListen = func(src netip.Addr, _ uint16, _ string) (*net.UDPConn, error, error) {
		c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.3"), Port: 0})
		return c, nil, err // deviceErr nil: the device bind SUCCEEDED, no fallback.
	}

	def := config.Path{Name: "forced-device-ok", SourceAddr: netip.MustParseAddr("127.0.0.3"), Bind: config.BindModeDevice}
	if err := m.AddPath(def); err != nil {
		t.Fatalf("AddPath: %v", err)
	}

	got := buf.String()
	if n := warnCount(got); n != 0 {
		t.Fatalf("WARN count = %d, want 0 (successful device bind must not warn); log:\n%s", n, got)
	}
}

// TestAddPathNoWarnOnSourceModePath is the other required negative: a bind="source"
// path never even attempts a device bind (the D38 escape hatch), so it must log NO
// WARN regardless of the (never consulted) resolveDeviceBind/addPathListen seams.
func TestAddPathNoWarnOnSourceModePath(t *testing.T) {
	psk := testKey(t, 0xD5)
	m, buf := newWarnCapturingMultipath(t, loopbackPaths(1), psk)

	def := config.Path{Name: "source-mode", SourceAddr: netip.MustParseAddr("127.0.0.4"), Bind: config.BindModeSource}
	if err := m.AddPath(def); err != nil {
		t.Fatalf("AddPath: %v", err)
	}

	got := buf.String()
	if n := warnCount(got); n != 0 {
		t.Fatalf("WARN count = %d, want 0 (a source-mode path never device-binds); log:\n%s", n, got)
	}
}

// TestReconcileDeferredWarnsOnFailingDeviceSetsockoptFallback is the D53 layer-(b)
// acceptance check driven via the deferredListen seam (reconcileDeferred, T55's
// background reconciler) rather than AddPath's addPathListen — the OTHER call site
// D53 names. It first lets Open defer a forced bind="device" path against the REAL
// (unmocked) unresolvable interface, then arms fake resolveDeviceBind/deferredListen
// seams simulating that same path's interface having since appeared BUT its
// SO_BINDTODEVICE failing, and asserts the reconcile tick logs exactly one NEW WARN
// (isolated from Open's own layer-(a) WARN via the pre-reconcile buffer offset) while
// still promoting the path to live over the working fallback socket.
func TestReconcileDeferredWarnsOnFailingDeviceSetsockoptFallback(t *testing.T) {
	psk := testKey(t, 0xD6)
	paths := []config.Path{
		{Name: "bound", SourceAddr: netip.MustParseAddr("127.0.0.1")},
		{Name: "forced-device", SourceAddr: netip.MustParseAddr(unassignableSource), Bind: config.BindModeDevice},
	}
	m, buf := newWarnCapturingMultipath(t, paths, psk)
	if len(m.deferred) != 1 {
		t.Fatalf("precondition: deferred=%d, want 1", len(m.deferred))
	}
	preLen := len(buf.String())

	m.resolveDeviceBind = func(_ netip.Addr, mode config.BindMode) string {
		if mode == config.BindModeDevice {
			return "wan0"
		}
		return ""
	}
	m.deferredListen = func(_ netip.Addr, _ uint16, dev string) (*net.UDPConn, error, error) {
		if dev == "" {
			return nil, nil, syscall.EADDRNOTAVAIL
		}
		c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
		if err != nil {
			return nil, nil, err
		}
		return c, errors.New("wan0: operation not permitted"), nil
	}

	m.reconcileDeferred()

	if len(m.deferred) != 0 {
		t.Fatalf("path did not promote over the working fallback conn: deferred=%d, want 0", len(m.deferred))
	}
	post := buf.String()[preLen:]
	if n := warnCount(post); n != 1 {
		t.Fatalf("WARN count since reconcile = %d, want 1; log:\n%s", n, post)
	}
	if !strings.Contains(post, `"path":"forced-device"`) {
		t.Fatalf("WARN does not name the path; log:\n%s", post)
	}
	if !strings.Contains(post, `"interface":"wan0"`) {
		t.Fatalf("WARN does not name the resolved interface; log:\n%s", post)
	}
}

// TestReconcileDeferredDedupesUnresolvableWarn is the D53 round-2 FIX 1 acceptance
// check: a deferred bind="device" path whose interface stays unresolvable across
// MULTIPLE reconcile ticks (the 1 Hz production cadence, DefaultReconcileInterval)
// must WARN only ONCE for the whole deferral window, not once per tick. Open's own
// admission WARN is excluded via the pre-reconcile buffer offset, mirroring
// TestReconcileDeferredWarnsOnFailingDeviceSetsockoptFallback. A second
// reconcileDeferred() call against the SAME still-unresolved condition must add ZERO
// new WARNs. FAILS on the pre-FIX1 code, which re-WARNs identically on every tick.
func TestReconcileDeferredDedupesUnresolvableWarn(t *testing.T) {
	psk := testKey(t, 0xD7)
	paths := []config.Path{
		{Name: "bound", SourceAddr: netip.MustParseAddr("127.0.0.1")},
		{Name: "forced-device", SourceAddr: netip.MustParseAddr(unassignableSource), Bind: config.BindModeDevice},
	}
	m, buf := newWarnCapturingMultipath(t, paths, psk)
	if len(m.deferred) != 1 {
		t.Fatalf("precondition: deferred=%d, want 1", len(m.deferred))
	}
	// Open's admission WARN already fired (the initial unresolvable transition); the
	// dedup latch should already be armed on the fresh deferredPath entry.
	if !m.deferred[0].warnedUnresolvable {
		t.Fatalf("precondition: Open's deferral did not arm the dedup latch")
	}
	preLen := len(buf.String())

	// Two consecutive ticks against the REAL (unmocked) resolveDeviceBind/deferredListen
	// seams: unassignableSource resolves to no interface and its fallback bind fails on
	// every tick, exactly the "persistently unresolvable" production scenario.
	m.reconcileDeferred()
	afterFirst := buf.String()
	m.reconcileDeferred()
	afterSecond := buf.String()

	if len(m.deferred) != 1 {
		t.Fatalf("path unexpectedly promoted: deferred=%d, want 1", len(m.deferred))
	}
	if n := warnCount(afterFirst[preLen:]); n != 0 {
		t.Fatalf("WARN count after 1st post-Open reconcile tick = %d, want 0 (Open already warned this transition); log:\n%s", n, afterFirst[preLen:])
	}
	if n := warnCount(afterSecond[len(afterFirst):]); n != 0 {
		t.Fatalf("WARN count after 2nd reconcile tick (same unresolved condition) = %d, want 0 (dedup must suppress the repeat); log:\n%s", n, afterSecond[len(afterFirst):])
	}
}

// TestReconcileDeferredReArmsAfterResolveThenUnresolve is the D53 round-2 FIX 1
// re-arm acceptance check: once a deferred path's dedup latch is cleared (the
// interface resolves, or the fallback bind starts working), a LATER unresolvable
// transition (e.g. a re-roam that drops the interface again) must WARN again — the
// dedup is per CONDITION-TRANSITION, not a permanent silence.
func TestReconcileDeferredReArmsAfterResolveThenUnresolve(t *testing.T) {
	psk := testKey(t, 0xD8)
	paths := []config.Path{
		{Name: "bound", SourceAddr: netip.MustParseAddr("127.0.0.1")},
		{Name: "forced-device", SourceAddr: netip.MustParseAddr(unassignableSource), Bind: config.BindModeDevice},
	}
	m, buf := newWarnCapturingMultipath(t, paths, psk)
	if len(m.deferred) != 1 {
		t.Fatalf("precondition: deferred=%d, want 1", len(m.deferred))
	}

	// Tick 1: the interface resolves and a real fallback socket binds (mirrors the
	// setsockopt-fallback scenario), so the path PROMOTES and the dedup latch would
	// have been cleared just before promotion.
	m.resolveDeviceBind = func(_ netip.Addr, mode config.BindMode) string {
		if mode == config.BindModeDevice {
			return "wan0"
		}
		return ""
	}
	m.deferredListen = func(_ netip.Addr, _ uint16, dev string) (*net.UDPConn, error, error) {
		if dev == "" {
			return nil, nil, syscall.EADDRNOTAVAIL
		}
		c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
		return c, nil, err
	}
	m.reconcileDeferred()
	if len(m.deferred) != 0 {
		t.Fatalf("path did not promote: deferred=%d, want 0", len(m.deferred))
	}

	// Re-add the SAME def at runtime (simulating a later re-roam that drops the
	// interface again) via AddPath's deferral path, driven by the REAL resolveDeviceBind
	// (restored) so it genuinely fails to resolve — a FRESH unresolvable transition.
	m.resolveDeviceBind = resolveForcedDeviceBind
	m.addPathListen = listenPath
	preLen := len(buf.String())
	def2 := config.Path{Name: "forced-device-2", SourceAddr: netip.MustParseAddr(unassignableSource), Bind: config.BindModeDevice}
	if err := m.AddPath(def2); err != nil {
		t.Fatalf("AddPath: %v", err)
	}
	got := buf.String()[preLen:]
	if n := warnCount(got); n != 1 {
		t.Fatalf("WARN count for the NEW unresolvable transition = %d, want 1 (re-arm must allow a fresh WARN); log:\n%s", n, got)
	}
	if !strings.Contains(got, `"path":"forced-device-2"`) {
		t.Fatalf("WARN does not name the new path; log:\n%s", got)
	}
}
