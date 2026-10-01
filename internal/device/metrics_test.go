package device

import (
	"context"
	"io"
	"net/http"
	"net/netip"
	"reflect"
	"sync"
	"testing"
	"time"

	awgdevice "github.com/amnezia-vpn/amneziawg-go/v3/device"

	"github.com/7mind/wanbond/internal/bind"
	"github.com/7mind/wanbond/internal/bond"
	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/log"
	"github.com/7mind/wanbond/internal/metrics"
	"github.com/7mind/wanbond/internal/reseq"
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

// fakePeerSessions is a peerSessionSnapshotter whose per-peer snapshot list the test
// controls, standing in for the engine-backed peerSessionMonitor so the adapter's
// PeerSessions() pass-through and the newMetricsSource call sites run without a live
// engine (T256, G28, M106).
type fakePeerSessions struct{ snaps []metrics.PeerSessionSnapshot }

func (f fakePeerSessions) PeerSessionSnapshots() []metrics.PeerSessionSnapshot { return f.snaps }

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
	src := newMetricsSource(prov, fakeSession{}, fakePeerSessions{}, &fakeClock{now: time.Unix(1000, 0)})

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

// TestMetricsSourceMapsProbeSendErrors is the D96 item 4 threading regression: the
// adapter must copy bind.PathTraffic.ProbeSendErrors verbatim into
// metrics.PathSnapshot.ProbeSendErrors for exactly the path that accumulated
// them, leaving an unaffected path at zero.
func TestMetricsSourceMapsProbeSendErrors(t *testing.T) {
	prov := &fakeProvider{}
	prov.set([]bind.PeerSnapshot{{
		Name: "",
		Paths: []bind.PathTraffic{
			{Name: "starlink", ProbeSendErrors: 7},
			{Name: "cellular", ProbeSendErrors: 0},
		},
	}})
	src := newMetricsSource(prov, fakeSession{}, fakePeerSessions{}, &fakeClock{now: time.Unix(1000, 0)})

	got := src.Paths()
	if len(got) != 2 {
		t.Fatalf("Paths len = %d, want 2", len(got))
	}
	if got[0].Name != "starlink" || got[0].ProbeSendErrors != 7 {
		t.Errorf("path 0 = %+v, want starlink ProbeSendErrors=7", got[0])
	}
	if got[1].Name != "cellular" || got[1].ProbeSendErrors != 0 {
		t.Errorf("path 1 = %+v, want cellular ProbeSendErrors=0", got[1])
	}
}

// TestMetricsSource_PathsCarriesAddressing asserts the adapter copies the runtime
// addressing fields (Source, Remote, BindMode, BoundDevice) verbatim from
// bind.PathTraffic into metrics.PathSnapshot (T220): pass-through only, no derivation.
// BindMode is a config.BindMode (defined string type) on the bind side and a plain
// string on the metrics side, so the mapping is a string conversion, not a rename.
func TestMetricsSource_PathsCarriesAddressing(t *testing.T) {
	prov := &fakeProvider{}
	source := netip.MustParseAddr("10.0.0.5")
	remote := netip.MustParseAddrPort("203.0.113.7:51820")
	prov.set([]bind.PeerSnapshot{{
		Name: "",
		Paths: []bind.PathTraffic{{
			Name:        "starlink",
			BindMode:    config.BindModeDevice,
			BoundDevice: "eth0",
			Source:      source,
			Remote:      remote,
		}},
	}})
	src := newMetricsSource(prov, fakeSession{}, fakePeerSessions{}, &fakeClock{now: time.Unix(1000, 0)})

	got := src.Paths()
	if len(got) != 1 {
		t.Fatalf("Paths len = %d, want 1", len(got))
	}
	if got[0].BindMode != string(config.BindModeDevice) {
		t.Errorf("BindMode = %q, want %q", got[0].BindMode, config.BindModeDevice)
	}
	if got[0].BoundDevice != "eth0" {
		t.Errorf("BoundDevice = %q, want %q", got[0].BoundDevice, "eth0")
	}
	if got[0].Source != source {
		t.Errorf("Source = %v, want %v", got[0].Source, source)
	}
	if got[0].Remote != remote {
		t.Errorf("Remote = %v, want %v", got[0].Remote, remote)
	}
}

// TestMetricsSourcePathsAddressingNoNewSeries asserts the addressing fields carried on
// metrics.PathSnapshot (T220) do not change the Prometheus exposition: scraping a
// collector whose Source reports non-zero addressing produces the exact same raw text
// as one whose Source reports zero-valued addressing, on otherwise-identical path data.
// This proves the collector still ignores the new fields — no new label or series.
func TestMetricsSourcePathsAddressingNoNewSeries(t *testing.T) {
	prov := &fakeProvider{}
	prov.set([]bind.PeerSnapshot{{
		Name: "",
		Paths: []bind.PathTraffic{{
			Name:    "starlink",
			TxBytes: 123456,
			RxBytes: 654321,
			State:   telemetry.StateUp,
		}},
	}})
	baseline := scrapeText(t, newMetricsSource(prov, fakeSession{}, fakePeerSessions{}, &fakeClock{now: time.Unix(1000, 0)}))

	prov2 := &fakeProvider{}
	prov2.set([]bind.PeerSnapshot{{
		Name: "",
		Paths: []bind.PathTraffic{{
			Name:        "starlink",
			TxBytes:     123456,
			RxBytes:     654321,
			State:       telemetry.StateUp,
			Source:      netip.MustParseAddr("10.0.0.5"),
			Remote:      netip.MustParseAddrPort("203.0.113.7:51820"),
			BindMode:    config.BindModeDevice,
			BoundDevice: "eth0",
		}},
	}})
	withAddressing := scrapeText(t, newMetricsSource(prov2, fakeSession{}, fakePeerSessions{}, &fakeClock{now: time.Unix(1000, 0)}))

	if baseline != withAddressing {
		t.Errorf("exposition differs when addressing fields are populated:\n--- baseline ---\n%s\n--- with addressing ---\n%s", baseline, withAddressing)
	}
}

// scrapeText starts a metrics.Server over src and returns the raw /metrics response
// body, so tests can compare the exposition's text-format surface byte-for-byte.
func scrapeText(t *testing.T, src metrics.Source) string {
	t.Helper()
	lg, err := log.New("error", io.Discard)
	if err != nil {
		t.Fatalf("log.New: %v", err)
	}
	srv, err := metrics.NewServer("127.0.0.1:0", src, nil, lg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	srv.Start()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := srv.Close(ctx); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()

	resp, err := http.Get(srv.URL())
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	return string(body)
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
	src := newMetricsSource(prov, fakeSession{}, fakePeerSessions{}, &fakeClock{now: time.Unix(1000, 0)})

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

// TestMetricsSourceDerivesThroughput asserts the second scrape reports throughput equal
// to the (tx+rx) byte-counter delta times 8, divided by the elapsed seconds.
func TestMetricsSourceDerivesThroughput(t *testing.T) {
	prov := &fakeProvider{}
	clock := &fakeClock{now: time.Unix(0, 0)}
	src := newMetricsSource(prov, fakeSession{}, fakePeerSessions{}, clock)

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
	src := newMetricsSource(prov, fakeSession{}, fakePeerSessions{}, clock)

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
	src := newMetricsSource(prov, fakeSession{}, fakePeerSessions{}, &fakeClock{now: time.Unix(0, 0)})

	got := src.PeerNames()
	if len(got) != 1 || got[0] != "" {
		t.Errorf("PeerNames = %v, want [\"\"]", got)
	}
}

// TestMetricsSourceTwoPeersDistinctSeries asserts a 2-peer source (a concentrator, T94)
// carries distinct `Peer` values on each returned Path/Reseq snapshot,
// independent counters per peer, and correctly keys the throughput last-sample map by
// (peer,path) — two peers with a same-named path ("starlink") each derive their OWN
// rate from their OWN byte-counter delta, not a rate clobbered by the other peer's
// counter.
func TestMetricsSourceTwoPeersDistinctSeries(t *testing.T) {
	prov := &fakeProvider{}
	clock := &fakeClock{now: time.Unix(0, 0)}
	src := newMetricsSource(prov, fakeSession{}, fakePeerSessions{}, clock)

	prov.set([]bind.PeerSnapshot{
		{
			Name:  "",
			Paths: []bind.PathTraffic{{Name: "starlink", TxBytes: 1000, RxBytes: 0}},
			Reseq: reseq.Stats{Released: 50},
		},
		{
			Name:  "edge2",
			Paths: []bind.PathTraffic{{Name: "starlink", TxBytes: 5000, RxBytes: 0}},
			Reseq: reseq.Stats{Released: 900},
		},
	})
	_ = src.Paths() // seed the first sample for both (peer,path) pairs

	clock.advance(1 * time.Second)
	prov.set([]bind.PeerSnapshot{
		{
			Name:  "",
			Paths: []bind.PathTraffic{{Name: "starlink", TxBytes: 1100, RxBytes: 0}}, // +100 bytes
			Reseq: reseq.Stats{Released: 51},
		},
		{
			Name:  "edge2",
			Paths: []bind.PathTraffic{{Name: "starlink", TxBytes: 6000, RxBytes: 0}}, // +1000 bytes
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

// TestMetricsSourcePeerSessionsTwoPeers is the T256/G28/M106 acceptance check at the
// adapter level: a 2-peer source's PeerSessions() carries the correct names, Established
// verdicts, and handshake ages, sourced from a REAL peerSessionMonitor reading a
// fakeEngine's UAPI dump — proving the metricsSource.PeerSessions() wiring end to end,
// not merely a fake pass-through.
func TestMetricsSourcePeerSessionsTwoPeers(t *testing.T) {
	now := time.Unix(10_000, 0)
	fresh := now.Add(-3 * time.Second)
	aged := now.Add(-(awgdevice.RejectAfterTime + time.Second))
	dump := uapiPeerKey("aaaa", fresh) + uapiPeerKey("bbbb", aged)
	peerMon := newPeerSessionMonitor(fakeEngine{dump: dump}, &fakeClock{now: now}, []monitoredPeer{
		{name: "edge1", publicKey: "aaaa"},
		{name: "edge2", publicKey: "bbbb"},
	})
	src := newMetricsSource(&fakeProvider{}, fakeSession{}, peerMon, &fakeClock{now: now})

	got := src.PeerSessions()
	if len(got) != 2 {
		t.Fatalf("PeerSessions len = %d, want 2", len(got))
	}
	byPeer := map[string]metrics.PeerSessionSnapshot{}
	for _, p := range got {
		byPeer[p.Peer] = p
	}
	if !byPeer["edge1"].Established || byPeer["edge1"].LastHandshakeSeconds != 3 {
		t.Errorf("edge1 = %+v, want Established=true age=3s", byPeer["edge1"])
	}
	wantAge := (awgdevice.RejectAfterTime + time.Second).Seconds()
	if byPeer["edge2"].Established || byPeer["edge2"].LastHandshakeSeconds != wantAge {
		t.Errorf("edge2 = %+v, want Established=false age=%gs (aged out)", byPeer["edge2"], wantAge)
	}
}

// TestMetricsSourcePeerSessionsSinglePeerBackCompat asserts a single-bound-peer source's
// PeerSessions() returns exactly one entry with Peer "" (T94/D58 back-compat), matching
// the existing Reseq single-peer shape, while still resolving that one
// peer's real established verdict.
func TestMetricsSourcePeerSessionsSinglePeerBackCompat(t *testing.T) {
	now := time.Unix(10_000, 0)
	fresh := now.Add(-3 * time.Second)
	peerMon := newPeerSessionMonitor(fakeEngine{dump: uapiPeerKey("aaaa", fresh)}, &fakeClock{now: now}, []monitoredPeer{
		{name: "", publicKey: "aaaa"},
	})
	src := newMetricsSource(&fakeProvider{}, fakeSession{}, peerMon, &fakeClock{now: now})

	got := src.PeerSessions()
	if len(got) != 1 || got[0].Peer != "" {
		t.Fatalf("PeerSessions = %+v, want one entry with Peer \"\"", got)
	}
	if !got[0].Established || got[0].LastHandshakeSeconds != 3 {
		t.Errorf("PeerSessions[0] = %+v, want Established=true age=3s", got[0])
	}
}

// TestMetricsSourcePMTULookup verifies the T229 mapping: PathSnapshot.PMTU carries the
// per-path discovered PMTU from the wired lookup — 0 before it is wired (no boot dip, the
// resizer keeps its configured-or-default fallback), and the RAW value verbatim once
// wired (the amnezia junk headroom is subtracted ONCE downstream in sampleMTU/T225, never
// here).
func TestMetricsSourcePMTULookup(t *testing.T) {
	prov := &fakeProvider{}
	src := newMetricsSource(prov, fakeSession{}, fakePeerSessions{}, &fakeClock{now: time.Unix(1000, 0)})
	prov.set([]bind.PeerSnapshot{{Paths: []bind.PathTraffic{{Name: "cellular", TxBytes: 1, RxBytes: 1}}}})

	if got := src.Paths()[0].PMTU; got != 0 {
		t.Fatalf("unwired PMTU = %d, want 0 (no boot dip)", got)
	}

	src.setPMTULookup(func(name string) int {
		if name == "cellular" {
			return 1400
		}
		return 0
	})
	if got := src.Paths()[0].PMTU; got != 1400 {
		t.Fatalf("wired PMTU = %d, want the discovered 1400 (raw, un-junked)", got)
	}
}

// TestAdaptiveNamesLanesAfterTheirLocalPath: a lane's id carries its local
// path's wire id in the high byte; the adapter names each lane after that path
// so the monitor can show names rather than ids.
func TestAdaptiveNamesLanesAfterTheirLocalPath(t *testing.T) {
	provider := &fakeProvider{}
	provider.set([]bind.PeerSnapshot{
		{Name: "hub", Paths: []bind.PathTraffic{{Name: "starlink", ID: 0}, {Name: "5g", ID: 1}},
			Adaptive: &bond.Snapshot{QueueDrops: 3, Paths: []bond.PathStats{{Path: 0}, {Path: 256}, {Path: 257}}}},
		{Name: "unopened", Paths: []bind.PathTraffic{{Name: "starlink", ID: 0}}},
	})
	src := newMetricsSource(provider, fakeSession{}, fakePeerSessions{}, &fakeClock{now: time.Unix(0, 0)})

	got := src.Adaptive()
	if len(got) != 1 || got[0].Peer != "hub" || got[0].State.QueueDrops != 3 {
		t.Fatalf("Adaptive() = %+v, want the one peer with a transport", got)
	}
	want := map[bond.PathID]string{0: "starlink", 256: "5g", 257: "5g"}
	if !reflect.DeepEqual(got[0].LanePaths, want) {
		t.Fatalf("LanePaths = %v, want %v", got[0].LanePaths, want)
	}
}
