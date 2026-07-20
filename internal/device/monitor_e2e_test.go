package device

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"go.uber.org/goleak"

	"github.com/7mind/wanbond/internal/bind"
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

	srv, err := monitor.NewServer("127.0.0.1:0", "", src, monitor.Info{}, discardLogger(t))
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
	src := newMetricsSource(prov, sess, clock)

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
	src := newMetricsSource(prov, sess, clock)

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
