package device

import (
	"sync"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/bind"
	"github.com/7mind/wanbond/internal/metrics"
	"github.com/7mind/wanbond/internal/reseq"
	"github.com/7mind/wanbond/internal/sched"
	"github.com/7mind/wanbond/internal/telemetry"
)

// fakeProvider is a trafficProvider whose per-peer snapshot the test controls, standing
// in for the live Bind so the adapter's mapping and rate derivation are exercised
// without an engine.
type fakeProvider struct {
	mu    sync.Mutex
	peers []bind.PeerSnapshot
}

func (f *fakeProvider) set(peers []bind.PeerSnapshot) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.peers = peers
}

func (f *fakeProvider) PeerSnapshots() []bind.PeerSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]bind.PeerSnapshot(nil), f.peers...)
}

// fakeClock is a manually-advanced Clock so the throughput derivation is deterministic.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

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

var _ telemetry.Clock = (*fakeClock)(nil)

// fakeSession is a sessionSnapshotter whose snapshot the test controls, standing in for
// the engine-backed sessionMonitor so the adapter's Session() pass-through and the
// newMetricsSource call sites run without a live engine.
type fakeSession struct{ snap metrics.SessionSnapshot }

func (f fakeSession) SessionSnapshot() metrics.SessionSnapshot { return f.snap }

// TestMetricsSourceMapsFields asserts the adapter copies the per-(peer,path) byte
// counters and telemetry verbatim into the metrics.PathSnapshot, preserving order, for
// a single-peer (unnamed primary) source.
func TestMetricsSourceMapsFields(t *testing.T) {
	prov := &fakeProvider{}
	prov.set([]bind.PeerSnapshot{{
		Name: "",
		Paths: []bind.PathTraffic{
			{Name: "starlink", TxBytes: 100, RxBytes: 200, Estimate: telemetry.Estimate{RTT: 45 * time.Millisecond, Loss: 0.01}, State: telemetry.StateUp},
			{Name: "cellular", TxBytes: 5, RxBytes: 7, State: telemetry.StateDown},
		},
	}})
	src := newMetricsSource(prov, fakeSession{}, &fakeClock{now: time.Unix(1000, 0)})

	got := src.Paths()
	if len(got) != 2 {
		t.Fatalf("Paths len = %d, want 2", len(got))
	}
	if got[0].Name != "starlink" || got[0].Peer != "" || got[0].TxBytes != 100 || got[0].RxBytes != 200 {
		t.Errorf("path 0 = %+v, want starlink peer=\"\" tx=100 rx=200", got[0])
	}
	if got[0].Estimate.RTT != 45*time.Millisecond || got[0].State != telemetry.StateUp {
		t.Errorf("path 0 telemetry = %+v/%v, want RTT 45ms/StateUp", got[0].Estimate, got[0].State)
	}
	if got[1].Name != "cellular" || got[1].State != telemetry.StateDown {
		t.Errorf("path 1 = %+v, want cellular/StateDown", got[1])
	}
	// First scrape of each path has no prior sample, so throughput is zero.
	if got[0].ThroughputBitsPerSecond != 0 || got[1].ThroughputBitsPerSecond != 0 {
		t.Errorf("first-scrape throughput = %g/%g, want 0/0", got[0].ThroughputBitsPerSecond, got[1].ThroughputBitsPerSecond)
	}
}

// TestMetricsSourceMapsFEC asserts the adapter passes the Bind's per-peer
// connection-scoped FEC counters (T24, T94) verbatim onto the metrics.FECSnapshot the
// exposition reads, for a single-peer (unnamed primary) source.
func TestMetricsSourceMapsFEC(t *testing.T) {
	prov := &fakeProvider{}
	prov.set([]bind.PeerSnapshot{{
		Name: "",
		FEC:  bind.FECStats{DataFrames: 82, ParityFrames: 33, ParityBytes: 4096, Recovered: 5, Unrecoverable: 1},
	}})
	src := newMetricsSource(prov, fakeSession{}, &fakeClock{now: time.Unix(1000, 0)})

	got := src.FEC()
	if len(got) != 1 {
		t.Fatalf("FEC len = %d, want 1", len(got))
	}
	if got[0].Peer != "" {
		t.Errorf("Peer = %q, want \"\"", got[0].Peer)
	}
	if got[0].DataPackets != 82 {
		t.Errorf("DataPackets = %d, want 82 (data frames)", got[0].DataPackets)
	}
	if got[0].RepairPackets != 33 {
		t.Errorf("RepairPackets = %d, want 33 (parity frames)", got[0].RepairPackets)
	}
	if got[0].RecoveredPackets != 5 {
		t.Errorf("RecoveredPackets = %d, want 5", got[0].RecoveredPackets)
	}
	if got[0].UnrecoverablePackets != 1 {
		t.Errorf("UnrecoverablePackets = %d, want 1", got[0].UnrecoverablePackets)
	}
}

// TestMetricsSourceMapsReseq asserts the adapter passes the Bind's per-peer
// resequencer counters (T94) verbatim onto the metrics.ReseqSnapshot the exposition
// reads, for a single-peer (unnamed primary) source.
func TestMetricsSourceMapsReseq(t *testing.T) {
	prov := &fakeProvider{}
	prov.set([]bind.PeerSnapshot{{
		Name:  "",
		Reseq: reseq.Stats{Released: 900, DroppedDup: 3, DroppedOld: 2, DroppedSuspect: 1, Skipped: 4, Resyncs: 2, Rebaselines: 1},
	}})
	src := newMetricsSource(prov, fakeSession{}, &fakeClock{now: time.Unix(1000, 0)})

	got := src.Reseq()
	if len(got) != 1 {
		t.Fatalf("Reseq len = %d, want 1", len(got))
	}
	if got[0].Peer != "" {
		t.Errorf("Peer = %q, want \"\"", got[0].Peer)
	}
	if got[0].Stats != (reseq.Stats{Released: 900, DroppedDup: 3, DroppedOld: 2, DroppedSuspect: 1, Skipped: 4, Resyncs: 2, Rebaselines: 1}) {
		t.Errorf("Stats = %+v, want the injected counters verbatim", got[0].Stats)
	}
}

// TestMetricsSourceMapsAggregation asserts the adapter passes a peer's weighted-scheduler
// aggregation-gate snapshot (T146) verbatim onto the metrics.AggregationSnapshot the
// exposition reads, for a single-peer (unnamed primary) source.
func TestMetricsSourceMapsAggregation(t *testing.T) {
	prov := &fakeProvider{}
	prov.set([]bind.PeerSnapshot{{
		Name: "",
		Aggregation: &sched.AggregationSnapshot{
			Aggregating:           true,
			OfferedLoadFPS:        640,
			EngageThresholdFPS:    630,
			DisengageThresholdFPS: 350,
		},
	}})
	src := newMetricsSource(prov, fakeSession{}, &fakeClock{now: time.Unix(1000, 0)})

	got := src.Aggregation()
	if len(got) != 1 {
		t.Fatalf("Aggregation len = %d, want 1", len(got))
	}
	want := metrics.AggregationSnapshot{
		Peer:                  "",
		Aggregating:           true,
		OfferedLoadFPS:        640,
		EngageThresholdFPS:    630,
		DisengageThresholdFPS: 350,
	}
	if got[0] != want {
		t.Errorf("Aggregation[0] = %+v, want %+v", got[0], want)
	}
}

// TestMetricsSourceAggregationAbsentForActiveBackup asserts a peer whose Bind snapshot
// carries a nil Aggregation (an active-backup peer — no scheduler gate) contributes NO
// entry to src.Aggregation(), so the four series are absent for it (T146).
func TestMetricsSourceAggregationAbsentForActiveBackup(t *testing.T) {
	prov := &fakeProvider{}
	prov.set([]bind.PeerSnapshot{
		{Name: "edge1", Aggregation: nil}, // active-backup: no gate
		{Name: "edge2", Aggregation: &sched.AggregationSnapshot{Aggregating: true, EngageThresholdFPS: 630, DisengageThresholdFPS: 350}},
	})
	src := newMetricsSource(prov, fakeSession{}, &fakeClock{now: time.Unix(1000, 0)})

	got := src.Aggregation()
	if len(got) != 1 {
		t.Fatalf("Aggregation len = %d, want 1 (only the weighted peer reports a gate)", len(got))
	}
	if got[0].Peer != "edge2" {
		t.Errorf("Aggregation[0].Peer = %q, want %q (the nil-gate active-backup peer must be skipped)", got[0].Peer, "edge2")
	}
}

// TestMetricsSourceDerivesThroughput asserts the second scrape reports throughput equal
// to the (tx+rx) byte-counter delta times 8, divided by the elapsed seconds.
func TestMetricsSourceDerivesThroughput(t *testing.T) {
	prov := &fakeProvider{}
	clock := &fakeClock{now: time.Unix(0, 0)}
	src := newMetricsSource(prov, fakeSession{}, clock)

	prov.set([]bind.PeerSnapshot{{Paths: []bind.PathTraffic{{Name: "starlink", TxBytes: 1000, RxBytes: 0}}}})
	if got := src.Paths()[0].ThroughputBitsPerSecond; got != 0 {
		t.Fatalf("first scrape throughput = %g, want 0", got)
	}

	// 2 seconds later, +2_000_000 total bytes → 2_000_000*8/2 = 8_000_000 bit/s.
	clock.advance(2 * time.Second)
	prov.set([]bind.PeerSnapshot{{Paths: []bind.PathTraffic{{Name: "starlink", TxBytes: 1000, RxBytes: 2_000_000}}}})
	got := src.Paths()[0].ThroughputBitsPerSecond
	const want = 8_000_000.0
	if got != want {
		t.Errorf("derived throughput = %g bit/s, want %g", got, want)
	}
}

// TestMetricsSourceThroughputNonNegativeOnReset asserts a backward-moving counter (only
// possible across a Close→Open reset) yields zero rather than a negative throughput.
func TestMetricsSourceThroughputNonNegativeOnReset(t *testing.T) {
	prov := &fakeProvider{}
	clock := &fakeClock{now: time.Unix(0, 0)}
	src := newMetricsSource(prov, fakeSession{}, clock)

	prov.set([]bind.PeerSnapshot{{Paths: []bind.PathTraffic{{Name: "starlink", TxBytes: 9_000_000, RxBytes: 0}}}})
	_ = src.Paths()

	clock.advance(time.Second)
	prov.set([]bind.PeerSnapshot{{Paths: []bind.PathTraffic{{Name: "starlink", TxBytes: 10, RxBytes: 0}}}}) // reset
	if got := src.Paths()[0].ThroughputBitsPerSecond; got != 0 {
		t.Errorf("throughput after counter reset = %g, want 0 (no negative rate)", got)
	}
}

// TestMetricsSourcePeerNamesSinglePeer asserts a single-bound-peer source reports the
// pre-T94 back-compat name set: exactly one entry, "" (BoundPeerNames' primary value).
func TestMetricsSourcePeerNamesSinglePeer(t *testing.T) {
	prov := &fakeProvider{}
	prov.set([]bind.PeerSnapshot{{Name: ""}})
	src := newMetricsSource(prov, fakeSession{}, &fakeClock{now: time.Unix(0, 0)})

	got := src.PeerNames()
	if len(got) != 1 || got[0] != "" {
		t.Errorf("PeerNames = %v, want [\"\"]", got)
	}
}

// TestMetricsSourceTwoPeersDistinctSeries asserts a 2-peer source (a concentrator, T94)
// carries distinct `Peer` values on each returned Path/FEC/Reseq snapshot,
// independent counters per peer, and correctly keys the throughput last-sample map by
// (peer,path) — two peers with a same-named path ("starlink") each derive their OWN
// rate from their OWN byte-counter delta, not a rate clobbered by the other peer's
// counter.
func TestMetricsSourceTwoPeersDistinctSeries(t *testing.T) {
	prov := &fakeProvider{}
	clock := &fakeClock{now: time.Unix(0, 0)}
	src := newMetricsSource(prov, fakeSession{}, clock)

	prov.set([]bind.PeerSnapshot{
		{
			Name:  "",
			Paths: []bind.PathTraffic{{Name: "starlink", TxBytes: 1000, RxBytes: 0}},
			FEC:   bind.FECStats{DataFrames: 10, ParityFrames: 2},
			Reseq: reseq.Stats{Released: 50},
		},
		{
			Name:  "edge2",
			Paths: []bind.PathTraffic{{Name: "starlink", TxBytes: 5000, RxBytes: 0}},
			FEC:   bind.FECStats{DataFrames: 700, ParityFrames: 140},
			Reseq: reseq.Stats{Released: 900},
		},
	})
	_ = src.Paths() // seed the first sample for both (peer,path) pairs

	clock.advance(1 * time.Second)
	prov.set([]bind.PeerSnapshot{
		{
			Name:  "",
			Paths: []bind.PathTraffic{{Name: "starlink", TxBytes: 1100, RxBytes: 0}}, // +100 bytes
			FEC:   bind.FECStats{DataFrames: 11, ParityFrames: 2},
			Reseq: reseq.Stats{Released: 51},
		},
		{
			Name:  "edge2",
			Paths: []bind.PathTraffic{{Name: "starlink", TxBytes: 6000, RxBytes: 0}}, // +1000 bytes
			FEC:   bind.FECStats{DataFrames: 705, ParityFrames: 141},
			Reseq: reseq.Stats{Released: 950},
		},
	})

	paths := src.Paths()
	if len(paths) != 2 {
		t.Fatalf("Paths len = %d, want 2", len(paths))
	}
	byPeer := map[string]metrics.PathSnapshot{}
	for _, p := range paths {
		byPeer[p.Peer] = p
	}
	if got, want := byPeer[""].ThroughputBitsPerSecond, 100.0*8; got != want {
		t.Errorf("primary starlink throughput = %g, want %g (own +100B delta)", got, want)
	}
	if got, want := byPeer["edge2"].ThroughputBitsPerSecond, 1000.0*8; got != want {
		t.Errorf("edge2 starlink throughput = %g, want %g (own +1000B delta, not clobbered by primary)", got, want)
	}

	fec := src.FEC()
	if len(fec) != 2 {
		t.Fatalf("FEC len = %d, want 2", len(fec))
	}
	fecByPeer := map[string]metrics.FECSnapshot{}
	for _, f := range fec {
		fecByPeer[f.Peer] = f
	}
	if got, want := fecByPeer[""].DataPackets, uint64(11); got != want {
		t.Errorf("primary FEC DataPackets = %d, want %d", got, want)
	}
	if got, want := fecByPeer["edge2"].DataPackets, uint64(705); got != want {
		t.Errorf("edge2 FEC DataPackets = %d, want %d (independent of primary's counter)", got, want)
	}

	reseqSnaps := src.Reseq()
	if len(reseqSnaps) != 2 {
		t.Fatalf("Reseq len = %d, want 2", len(reseqSnaps))
	}
	reseqByPeer := map[string]metrics.ReseqSnapshot{}
	for _, r := range reseqSnaps {
		reseqByPeer[r.Peer] = r
	}
	if got, want := reseqByPeer[""].Released, uint64(51); got != want {
		t.Errorf("primary Reseq Released = %d, want %d", got, want)
	}
	if got, want := reseqByPeer["edge2"].Released, uint64(950); got != want {
		t.Errorf("edge2 Reseq Released = %d, want %d (independent of primary's counter)", got, want)
	}

	names := src.PeerNames()
	if len(names) != 2 {
		t.Fatalf("PeerNames len = %d, want 2", len(names))
	}
	if (names[0] != "" || names[1] != "edge2") && (names[0] != "edge2" || names[1] != "") {
		t.Errorf("PeerNames = %v, want [\"\", \"edge2\"] in some order", names)
	}
}
