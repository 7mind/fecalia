package device

import (
	"context"
	"crypto/ecdh"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/amnezia-vpn/amneziawg-go/tun/tuntest"
	"github.com/coder/websocket"
	"go.uber.org/goleak"

	"github.com/7mind/wanbond/internal/bind"
	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/metrics"
	"github.com/7mind/wanbond/internal/monitor"
	"github.com/7mind/wanbond/internal/telemetry"
)

// T170 end-to-end: stand up the REAL monitoring pipeline — the production
// metricsSource adapter (newMetricsSource over a controllable trafficProvider +
// sessionSnapshotter, reusing the metrics-adapter test fixtures) feeding a real
// monitor.Server — and drive it over an ACTUAL coder/websocket client. Unlike
// internal/monitor/server_test.go (which feeds the server a hand-built
// metrics.Source fake), these tests exercise the whole Bind-snapshot ->
// metricsSource -> BuildSnapshot -> WS chain and, critically, assert the pushed
// stream tracks LIVE changes to the underlying Source (a guard against a frozen
// or placeholder feed), for both the single-peer (edge) and multi-peer
// (concentrator) shapes.

const (
	// monitorReadTimeout bounds a single frame read. The server pushes at ~1Hz
	// (monitor.monitorPushInterval, unexported), so any healthy frame arrives
	// well within this budget.
	monitorReadTimeout = 4 * time.Second
	// monitorObserveTimeout bounds read-until-observed: how long to keep reading
	// pushed frames waiting for a mutation to surface (~a dozen 1Hz frames).
	monitorObserveTimeout = 12 * time.Second
)

// syncSession is a mutable, concurrency-safe sessionSnapshotter. The monitor's
// push goroutine reads SessionSnapshot() while the test mutates the snapshot, so
// (unlike the immutable value-type fakeSession) the field access must be guarded
// to stay race-free under -race.
type syncSession struct {
	mu   sync.Mutex
	snap metrics.SessionSnapshot
}

func (s *syncSession) SessionSnapshot() metrics.SessionSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snap
}

func (s *syncSession) set(snap metrics.SessionSnapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snap = snap
}

// dialMonitor stands up a monitor.Server over src on loopback (no token),
// Start()s it, dials /ws with a real websocket client, and returns a
// read-one-snapshot closure plus a cleanup. The caller MUST `defer cleanup()`
// AFTER `defer goleak.VerifyNone(t)` so that (LIFO) the client+server close
// first and goleak then observes a fully-drained runtime.
func dialMonitor(t *testing.T, src metrics.Source) (readSnap func(*testing.T) monitor.MonitorSnapshot, cleanup func()) {
	t.Helper()

	srv, err := monitor.NewServer("127.0.0.1:0", "", src, monitor.Info{}, nil, discardLogger(t))
	if err != nil {
		t.Fatalf("monitor.NewServer: %v", err)
	}
	srv.Start()

	url := fmt.Sprintf("ws://%s/ws", srv.Addr().String())
	dialCtx, dialCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer dialCancel()
	c, resp, err := websocket.Dial(dialCtx, url, nil)
	if err != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Close(ctx)
		t.Fatalf("websocket.Dial(%q): %v", url, err)
	}
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}

	readSnap = func(t *testing.T) monitor.MonitorSnapshot {
		t.Helper()
		readCtx, cancel := context.WithTimeout(context.Background(), monitorReadTimeout)
		defer cancel()
		typ, data, err := c.Read(readCtx)
		if err != nil {
			t.Fatalf("read monitor frame: %v", err)
		}
		if typ != websocket.MessageText {
			t.Fatalf("frame type = %v, want MessageText", typ)
		}
		var snap monitor.MonitorSnapshot
		if err := json.Unmarshal(data, &snap); err != nil {
			t.Fatalf("unmarshal MonitorSnapshot: %v (payload=%s)", err, data)
		}
		return snap
	}

	cleanup = func() {
		_ = c.CloseNow()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Close(ctx); err != nil {
			t.Errorf("monitor Close: %v", err)
		}
	}
	return readSnap, cleanup
}

// readUntil reads pushed frames until pred holds or monitorObserveTimeout
// elapses. A frozen/placeholder feed never satisfies a mutation predicate, so
// this fails (with the last-seen frame) rather than passing on stale data.
func readUntil(t *testing.T, readSnap func(*testing.T) monitor.MonitorSnapshot, desc string, pred func(monitor.MonitorSnapshot) bool) monitor.MonitorSnapshot {
	t.Helper()
	deadline := time.Now().Add(monitorObserveTimeout)
	var last monitor.MonitorSnapshot
	for time.Now().Before(deadline) {
		last = readSnap(t)
		if pred(last) {
			return last
		}
	}
	t.Fatalf("no pushed frame satisfied %q within %v; last frame = %+v", desc, monitorObserveTimeout, last)
	return last
}

func approxEq(got, want float64) bool { return math.Abs(got-want) < 1e-9 }

// dialMonitorWithInfo is dialMonitor with a caller-supplied monitor.Info (dialMonitor always
// passes the zero Info): it wires src+info into a real monitor.Server, dials /ws for real, and
// returns the same read-one-snapshot/cleanup idiom.
func dialMonitorWithInfo(t *testing.T, src metrics.Source, info monitor.Info) (readSnap func(*testing.T) monitor.MonitorSnapshot, cleanup func()) {
	t.Helper()

	srv, err := monitor.NewServer("127.0.0.1:0", "", src, info, nil, discardLogger(t))
	if err != nil {
		t.Fatalf("monitor.NewServer: %v", err)
	}
	srv.Start()

	url := fmt.Sprintf("ws://%s/ws", srv.Addr().String())
	dialCtx, dialCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer dialCancel()
	c, resp, err := websocket.Dial(dialCtx, url, nil)
	if err != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Close(ctx)
		t.Fatalf("websocket.Dial(%q): %v", url, err)
	}
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}

	readSnap = func(t *testing.T) monitor.MonitorSnapshot {
		t.Helper()
		readCtx, cancel := context.WithTimeout(context.Background(), monitorReadTimeout)
		defer cancel()
		typ, data, err := c.Read(readCtx)
		if err != nil {
			t.Fatalf("read monitor frame: %v", err)
		}
		if typ != websocket.MessageText {
			t.Fatalf("frame type = %v, want MessageText", typ)
		}
		var snap monitor.MonitorSnapshot
		if err := json.Unmarshal(data, &snap); err != nil {
			t.Fatalf("unmarshal MonitorSnapshot: %v (payload=%s)", err, data)
		}
		return snap
	}

	cleanup = func() {
		_ = c.CloseNow()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Close(ctx); err != nil {
			t.Errorf("monitor Close: %v", err)
		}
	}
	return readSnap, cleanup
}

// dialMonitorAt dials /ws on an ALREADY-RUNNING monitor.Server (e.g. the real
// up()-wired tun.monitorSrv, T223) via 127.0.0.1:<bound port> — mirroring
// internal/monitor/server_test.go's readOneSnapshot, which dials by extracted port
// rather than the listener's own Addr().String() because a wildcard ("0.0.0.0:port")
// bind is not itself a dialable client destination. token, if non-empty, is sent as
// a Bearer Authorization header. Returns the raw frame bytes (for a byte-level
// redaction scan) alongside the decoded snapshot, plus a cleanup closing the client.
func dialMonitorAt(t *testing.T, srv *monitor.Server, token string) (readRaw func(*testing.T) (json.RawMessage, monitor.MonitorSnapshot), cleanup func()) {
	t.Helper()

	port := srv.Addr().(*net.TCPAddr).Port
	url := fmt.Sprintf("ws://127.0.0.1:%d/ws", port)
	var opts *websocket.DialOptions
	if token != "" {
		opts = &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + token}}}
	}
	dialCtx, dialCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer dialCancel()
	c, resp, err := websocket.Dial(dialCtx, url, opts)
	if err != nil {
		t.Fatalf("websocket.Dial(%q): %v", url, err)
	}
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}

	readRaw = func(t *testing.T) (json.RawMessage, monitor.MonitorSnapshot) {
		t.Helper()
		readCtx, cancel := context.WithTimeout(context.Background(), monitorReadTimeout)
		defer cancel()
		typ, data, err := c.Read(readCtx)
		if err != nil {
			t.Fatalf("read monitor frame: %v", err)
		}
		if typ != websocket.MessageText {
			t.Fatalf("frame type = %v, want MessageText", typ)
		}
		var snap monitor.MonitorSnapshot
		if err := json.Unmarshal(data, &snap); err != nil {
			t.Fatalf("unmarshal MonitorSnapshot: %v (payload=%s)", err, data)
		}
		return json.RawMessage(data), snap
	}
	cleanup = func() { _ = c.CloseNow() }
	return readRaw, cleanup
}

// fullPublicKeyB64 derives the FULL (untruncated) base64 WG public key for priv, so a test can
// scan a frame's raw bytes for its absence (Q63 — only the truncated fingerprint may ever be
// present on the wire).
func fullPublicKeyB64(t *testing.T, priv config.Key) string {
	t.Helper()
	raw := priv.Bytes()
	sk, err := ecdh.X25519().NewPrivateKey(raw[:])
	if err != nil {
		t.Fatalf("derive full public key: %v", err)
	}
	return base64.StdEncoding.EncodeToString(sk.PublicKey().Bytes())
}

// TestMonitorLiveWSReflectsSourceSinglePeer drives the real adapter for an edge
// (single, unnamed peer): the first frame is the flat single-peer shape, then a
// mutation to the underlying Source — path flipped DOWN, byte counters grown
// (the throughput driver; the rate derivation itself is unit-tested in
// TestMetricsSourceDerivesThroughput), RTT/loss changed, FEC populated, WG
// session established — MUST surface on a later pushed frame.
func TestMonitorLiveWSReflectsSourceSinglePeer(t *testing.T) {
	defer goleak.VerifyNone(t)

	prov := &fakeProvider{}
	clock := &fakeClock{now: time.Unix(1000, 0)}
	sess := &syncSession{}
	src := newMetricsSource(prov, sess, fakePeerSessions{}, clock)

	prov.set([]bind.PeerSnapshot{{
		Name: "",
		Paths: []bind.PathTraffic{
			{Name: "starlink", TxBytes: 1000, RxBytes: 2000,
				Estimate: telemetry.Estimate{RTT: 40 * time.Millisecond, Jitter: 5 * time.Millisecond, Loss: 0.01},
				State:    telemetry.StateUp},
		},
	}})

	readSnap, cleanup := dialMonitor(t, src)
	defer cleanup()

	first := readSnap(t)
	if first.MultiPeer {
		t.Fatalf("single-peer source: MultiPeer=true, want false (flat shape)")
	}
	if len(first.PeerNames) != 1 || first.PeerNames[0] != "" {
		t.Fatalf("PeerNames=%v, want [\"\"] (single unnamed edge peer)", first.PeerNames)
	}
	if len(first.Paths) != 1 || first.Paths[0].Name != "starlink" || first.Paths[0].Peer != "" {
		t.Fatalf("paths=%+v, want a single flat starlink path (peer \"\")", first.Paths)
	}
	if !first.Paths[0].Up {
		t.Fatalf("starlink Up=false at boot, want true")
	}
	if !approxEq(first.Paths[0].RTTSeconds, 0.04) {
		t.Errorf("boot RTTSeconds=%g, want ~0.04", first.Paths[0].RTTSeconds)
	}

	// Mutate the underlying Source to prove the WS feed is LIVE, not a frozen
	// placeholder. advance the clock so a scrape can derive a rate at all (the
	// rate value is not asserted here — it is racy over the async 1Hz push
	// cadence and is covered deterministically by the adapter unit test).
	clock.advance(2 * time.Second)
	sess.set(metrics.SessionSnapshot{Established: true, LastHandshakeAge: 15 * time.Second})
	prov.set([]bind.PeerSnapshot{{
		Name: "",
		Paths: []bind.PathTraffic{
			{Name: "starlink", TxBytes: 1_000_000, RxBytes: 2_000_000,
				Estimate: telemetry.Estimate{RTT: 90 * time.Millisecond, Jitter: 12 * time.Millisecond, Loss: 0.2},
				State:    telemetry.StateDown},
		},
		FEC: bind.FECStats{DataFrames: 120, ParityFrames: 40, Recovered: 7, Unrecoverable: 1, DataBytes: 60000, ParityBytes: 8000, ResidualLoss: 0.002},
	}})

	// A later pushed frame must reflect ALL of the level-triggered changes.
	got := readUntil(t, readSnap, "path down + counters grown + FEC populated + session established", func(s monitor.MonitorSnapshot) bool {
		return len(s.Paths) == 1 && !s.Paths[0].Up &&
			s.Paths[0].TxBytes == 1_000_000 && s.Paths[0].RxBytes == 2_000_000 &&
			len(s.FEC) == 1 && s.FEC[0].DataPackets == 120 &&
			s.Session.Established
	})
	if !approxEq(got.Paths[0].RTTSeconds, 0.09) {
		t.Errorf("reflected RTTSeconds=%g, want ~0.09 (changed from boot)", got.Paths[0].RTTSeconds)
	}
	if !approxEq(got.Paths[0].Loss, 0.2) {
		t.Errorf("reflected Loss=%g, want ~0.2", got.Paths[0].Loss)
	}
	if got.FEC[0].RecoveredPackets != 7 || got.FEC[0].UnrecoverablePackets != 1 {
		t.Errorf("reflected FEC recovered/unrecoverable = %d/%d, want 7/1", got.FEC[0].RecoveredPackets, got.FEC[0].UnrecoverablePackets)
	}
	if got.Session.LastHandshakeSeconds != 15 {
		t.Errorf("reflected session lastHandshakeSeconds=%g, want 15", got.Session.LastHandshakeSeconds)
	}
}

// TestMonitorLiveWSReflectsSourceMultiPeer drives the real adapter for a
// concentrator (two bound peers): the frame carries MultiPeer=true and distinct
// per-peer path/FEC sections, and a live mutation to ONE peer surfaces on a
// later frame without disturbing the other's section.
func TestMonitorLiveWSReflectsSourceMultiPeer(t *testing.T) {
	defer goleak.VerifyNone(t)

	prov := &fakeProvider{}
	clock := &fakeClock{now: time.Unix(1000, 0)}
	sess := &syncSession{snap: metrics.SessionSnapshot{Established: true, LastHandshakeAge: 5 * time.Second}}
	src := newMetricsSource(prov, sess, fakePeerSessions{}, clock)

	prov.set([]bind.PeerSnapshot{
		{Name: "", Paths: []bind.PathTraffic{{Name: "starlink", TxBytes: 10, State: telemetry.StateUp}}, FEC: bind.FECStats{DataFrames: 10}},
		{Name: "edge2", Paths: []bind.PathTraffic{{Name: "lte", TxBytes: 20, State: telemetry.StateUp}}, FEC: bind.FECStats{DataFrames: 700}},
	})

	readSnap, cleanup := dialMonitor(t, src)
	defer cleanup()

	first := readSnap(t)
	if !first.MultiPeer {
		t.Fatalf("2-peer source: MultiPeer=false, want true (per-peer sections)")
	}
	if len(first.PeerNames) != 2 {
		t.Fatalf("PeerNames=%v, want 2 entries", first.PeerNames)
	}
	pathPeers := map[string]bool{}
	for _, p := range first.Paths {
		pathPeers[p.Peer] = true
	}
	if !pathPeers[""] || !pathPeers["edge2"] {
		t.Fatalf("path peer labels=%v, want distinct \"\" and \"edge2\" sections", pathPeers)
	}
	fecByPeer := map[string]uint64{}
	for _, f := range first.FEC {
		fecByPeer[f.Peer] = f.DataPackets
	}
	if fecByPeer[""] != 10 || fecByPeer["edge2"] != 700 {
		t.Fatalf("per-peer FEC DataPackets=%v, want \"\":10 edge2:700", fecByPeer)
	}

	// Live reflection: grow edge2's FEC and flip its path DOWN; a later frame
	// must carry the change on edge2's section while the primary's is untouched.
	prov.set([]bind.PeerSnapshot{
		{Name: "", Paths: []bind.PathTraffic{{Name: "starlink", TxBytes: 10, State: telemetry.StateUp}}, FEC: bind.FECStats{DataFrames: 10}},
		{Name: "edge2", Paths: []bind.PathTraffic{{Name: "lte", TxBytes: 20, State: telemetry.StateDown}}, FEC: bind.FECStats{DataFrames: 999}},
	})
	got := readUntil(t, readSnap, "edge2 FEC grows to 999 + its path flips down", func(s monitor.MonitorSnapshot) bool {
		if !s.MultiPeer || len(s.FEC) != 2 {
			return false
		}
		var edge2FEC uint64
		edge2Down := false
		for _, f := range s.FEC {
			if f.Peer == "edge2" {
				edge2FEC = f.DataPackets
			}
		}
		for _, p := range s.Paths {
			if p.Peer == "edge2" {
				edge2Down = !p.Up
			}
		}
		return edge2FEC == 999 && edge2Down
	})
	// The primary peer's section stays as configured (not clobbered by edge2's mutation).
	var primaryFEC uint64
	primaryUp := false
	for _, f := range got.FEC {
		if f.Peer == "" {
			primaryFEC = f.DataPackets
		}
	}
	for _, p := range got.Paths {
		if p.Peer == "" {
			primaryUp = p.Up
		}
	}
	if primaryFEC != 10 || !primaryUp {
		t.Errorf("primary section = FEC %d / up %v, want 10 / true (unchanged by edge2's mutation)", primaryFEC, primaryUp)
	}
}

// TestMonitorE2E_LoopbackFullAddressingEndpointsAndFingerprint is the T223 loopback acceptance:
// unlike TestMonitorWire_InfoFields (which inspects tun.monitorInfo's Go values in-process), this
// dials the REAL up()-wired [monitor] endpoint (real device + real Bind + real monitor.Server) over
// an ACTUAL WebSocket connection and asserts the WIRE frame — the whole T219/T220/T222 gate,
// end-to-end. The edge config's two literal hub endpoints (peerNeedsHubFailover) give the real
// up()-wired hubFailover controller a non-empty endpoint list to expose through Info.Endpoints.
func TestMonitorE2E_LoopbackFullAddressingEndpointsAndFingerprint(t *testing.T) {
	defer goleak.VerifyNone(t)

	cfg := writeEdgeConfig(t, `["203.0.113.1:51820", "198.51.100.7:51820"]`, false)
	cfg.Monitor = config.Monitor{Listen: "127.0.0.1:0"}
	chtun := tuntest.NewChannelTUN()

	tun, err := up(cfg, discardLogger(t), chtun.TUN(), "wanbondtest0", inertFactory, "test")
	if err != nil {
		t.Fatalf("up: %v", err)
	}
	defer tun.Close()

	if tun.monitorSrv == nil {
		t.Fatal("up did not start the monitor endpoint despite a configured [monitor].listen")
	}

	readRaw, cleanup := dialMonitorAt(t, tun.monitorSrv, "")
	defer cleanup()

	raw, snap := readRaw(t)

	if snap.AddressingHidden {
		t.Fatalf("loopback bind must reveal addressing (addressingHidden=false): %s", raw)
	}
	if len(snap.Paths) != 1 || snap.Paths[0].Addressing == nil {
		t.Fatalf("expected exactly one path with per-path addressing revealed: %s", raw)
	}
	if snap.Paths[0].Addressing.Source != "127.0.0.1" {
		t.Fatalf("path source addr = %q, want the configured 127.0.0.1: %s", snap.Paths[0].Addressing.Source, raw)
	}
	if snap.Paths[0].Addressing.Remote == "" {
		t.Fatalf("path remote addr is empty, want the configured hub endpoint: %s", raw)
	}

	// Ordered hub-endpoint list with exactly one active entry (the boot default before any
	// failover), sourced from the up()-wired hubFailover via newEndpointsProvider (T222).
	if len(snap.Endpoints) != 2 {
		t.Fatalf("endpoints = %+v, want the 2 configured hub endpoints", snap.Endpoints)
	}
	activeCount := 0
	for _, e := range snap.Endpoints {
		if e.Address == "" {
			t.Fatalf("endpoint address blank on a loopback (revealed) bind: %+v", snap.Endpoints)
		}
		if e.Active {
			activeCount++
		}
	}
	if activeCount != 1 {
		t.Fatalf("active endpoint count = %d, want exactly 1: %+v", activeCount, snap.Endpoints)
	}

	// Truncated WG fingerprint (Q63), present on any binding.
	if len(snap.WGPublicKeyFingerprint) != wgFingerprintLen {
		t.Fatalf("fingerprint length = %d, want %d (%q)", len(snap.WGPublicKeyFingerprint), wgFingerprintLen, snap.WGPublicKeyFingerprint)
	}

	// R242/Q63: the fingerprint is a PREFIX of the local public key, never the full key, and the
	// full key must never appear anywhere in the raw frame bytes — there is no full-key field.
	fullPub := fullPublicKeyB64(t, cfg.WireGuard.PrivateKey)
	if !strings.HasPrefix(fullPub, snap.WGPublicKeyFingerprint) {
		t.Fatalf("fingerprint %q is not a prefix of the real public key %q", snap.WGPublicKeyFingerprint, fullPub)
	}
	if strings.Contains(string(raw), fullPub) {
		t.Fatalf("frame leaks the FULL WG public key (Q63 — fingerprint only): %s", raw)
	}
}

// TestMonitorE2E_NonLoopbackRedactsAddressingButKeepsFingerprint is the T223 non-loopback
// acceptance: a token-authorized wildcard bind, dialed over a REAL WebSocket connection to the
// up()-wired [monitor] endpoint, must carry addressingHidden=true and NO address string anywhere
// in the raw frame bytes (per-path source/remote AND every hub-endpoint address), while the
// truncated fingerprint (Q63) still survives — and, as on loopback, the full key is never present.
func TestMonitorE2E_NonLoopbackRedactsAddressingButKeepsFingerprint(t *testing.T) {
	defer goleak.VerifyNone(t)

	const token = "e2e-secret-monitor-token"
	cfg := writeEdgeConfig(t, `["203.0.113.1:51820", "198.51.100.7:51820"]`, false)
	cfg.Monitor = config.Monitor{Listen: "0.0.0.0:0", Token: token}
	chtun := tuntest.NewChannelTUN()

	tun, err := up(cfg, discardLogger(t), chtun.TUN(), "wanbondtest0", inertFactory, "test")
	if err != nil {
		t.Fatalf("up: %v", err)
	}
	defer tun.Close()

	if tun.monitorSrv == nil {
		t.Fatal("up did not start the monitor endpoint despite a configured [monitor].listen")
	}

	readRaw, cleanup := dialMonitorAt(t, tun.monitorSrv, token)
	defer cleanup()

	raw, snap := readRaw(t)

	if !snap.AddressingHidden {
		t.Fatalf("non-loopback (wildcard) bind must redact addressing (addressingHidden=true): %s", raw)
	}
	if len(snap.Paths) != 1 || snap.Paths[0].Addressing != nil {
		t.Fatalf("path addressing must be nil off-loopback: %s", raw)
	}
	for _, e := range snap.Endpoints {
		if e.Address != "" {
			t.Fatalf("endpoint address must be blanked off-loopback, got %+v", snap.Endpoints)
		}
	}
	// No address string of any kind — per-path source/remote OR hub-endpoint — anywhere in the
	// raw frame BYTES, not merely absent from the typed fields (a byte-level scan catches a
	// redaction bug that leaks through an untyped/extra JSON field the typed unmarshal ignores).
	for _, addr := range []string{"127.0.0.1", "203.0.113.1", "198.51.100.7"} {
		if strings.Contains(string(raw), addr) {
			t.Fatalf("redacted non-loopback frame leaked address %q: %s", addr, raw)
		}
	}

	// The fingerprint (Q63) is NOT part of the redactable surface: it survives on any binding.
	if len(snap.WGPublicKeyFingerprint) != wgFingerprintLen {
		t.Fatalf("fingerprint missing/wrong length off-loopback: %q", snap.WGPublicKeyFingerprint)
	}
	fullPub := fullPublicKeyB64(t, cfg.WireGuard.PrivateKey)
	if strings.Contains(string(raw), fullPub) {
		t.Fatalf("frame leaks the FULL WG public key (Q63 — fingerprint only): %s", raw)
	}
}

// TestMonitorE2E_ActiveEndpointMovesAfterForcedHubFailover is the T223/R242 freshness
// acceptance: the active hub-endpoint entry in a LATER pushed /ws frame must reflect a hub
// failover that happens AFTER the connection is established — proving BuildSnapshot evaluates
// info.Endpoints() FRESH on every snapshot (a live per-snapshot provider), not a value captured
// once at monitor.Server construction.
//
// Driving a REAL forced failover through the up()-wired production controller would require the
// real WG probe transport to observe hub loss over actual UDP sockets within the settle dwell —
// not deterministically drivable from this untagged channel-TUN harness (no listening peer to
// bring paths up in the first place, and no in-process access to the up()-internal hubFailover:
// it is a local variable closed over by Info.Endpoints, never stored on *Tunnel). Per T223's
// explicit fallback, this test instead drives the failover at the seam T222 actually wires
// end-to-end: newEndpointsProvider over a directly-constructed *hubFailover, fed as monitor.Info
// into a REAL monitor.Server (the same production monitor.NewServer/BuildSnapshot/
// newEndpointsProvider/hubFailover.check code paths up() itself uses), dialed over a REAL
// WebSocket — so the assertion exercises the wire format and the live-provider re-evaluation
// exactly as a client would observe it.
func TestMonitorE2E_ActiveEndpointMovesAfterForcedHubFailover(t *testing.T) {
	defer goleak.VerifyNone(t)

	prov := &fakeProvider{}
	clock := &fakeClock{now: time.Unix(2000, 0)}
	sess := &syncSession{}
	src := newMetricsSource(prov, sess, fakePeerSessions{}, clock)
	prov.set([]bind.PeerSnapshot{{Name: "", Paths: []bind.PathTraffic{
		{Name: "a", State: telemetry.StateUp},
	}}})

	eps := mustEndpoints(t, "203.0.113.9:51820", "198.51.100.9:51820")
	hp := []hubHealth{&fakeHealth{telemetry.StateUp}, &fakeHealth{telemetry.StateUp}}
	fclk := &fakeClock{now: time.Unix(1000, 0)}
	fo := newHubFailover(eps, hp, &recordingRemote{}, func() {}, fclk, testSettle, discardLogger(t))
	solo := []config.PeerIdentity{{Name: "solo"}}

	info := monitor.Info{
		Role:                   string(config.RoleEdge),
		WGPublicKeyFingerprint: "AbCdEfGhIj",
		Endpoints:              newEndpointsProvider(solo, map[string]*hubFailover{"solo": fo}),
	}

	readSnap, cleanup := dialMonitorWithInfo(t, src, info)
	defer cleanup()

	first := readSnap(t)
	if len(first.Endpoints) != 2 || !first.Endpoints[0].Active || first.Endpoints[1].Active {
		t.Fatalf("before failover: endpoints = %+v, want [0] active", first.Endpoints)
	}

	// Force hub loss (both endpoints' health DOWN) and advance past the settle dwell so check()
	// switches the active endpoint — the same production check() the up()-wired loop calls.
	hp[0].(*fakeHealth).state = telemetry.StateDown
	hp[1].(*fakeHealth).state = telemetry.StateDown
	fclk.advance(testSettle + time.Second)
	fo.check()

	// A LATER pushed frame — read AFTER the forced switch — must reflect the moved active entry:
	// this is the freshness property a value captured once at Server construction would violate.
	got := readUntil(t, readSnap, "active endpoint moves to [1] after forced hub failover", func(s monitor.MonitorSnapshot) bool {
		return len(s.Endpoints) == 2 && !s.Endpoints[0].Active && s.Endpoints[1].Active
	})
	if got.Endpoints[1].Address != "198.51.100.9:51820" {
		t.Fatalf("active endpoint after failover = %+v, want [1]=198.51.100.9:51820 active", got.Endpoints)
	}
}
