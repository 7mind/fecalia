package metrics

import (
	"context"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/log"
	"github.com/7mind/wanbond/internal/reseq"
	"github.com/7mind/wanbond/internal/telemetry"
)

// fakeSource is a static Source that returns a fixed set of per-(peer,path)
// snapshots, standing in for the live traffic/telemetry planes so the exposition can
// be asserted against known values. peerNames defaults to nil (len 0), which
// NewCollector's `len(PeerNames()) > 1` test treats identically to a single bound
// peer — the pre-T94 back-compat (no `peer` label) exposition — so every EXISTING
// test below that does not set peerNames keeps exercising the byte-compatible shape.
type fakeSource struct {
	paths     []PathSnapshot
	fec       []FECSnapshot
	reseq     []ReseqSnapshot
	session   SessionSnapshot
	peerNames []string
}

func (f fakeSource) Paths() []PathSnapshot    { return f.paths }
func (f fakeSource) FEC() []FECSnapshot       { return f.fec }
func (f fakeSource) Reseq() []ReseqSnapshot   { return f.reseq }
func (f fakeSource) Session() SessionSnapshot { return f.session }
func (f fakeSource) PeerNames() []string      { return f.peerNames }

func testLogger(t *testing.T) log.Logger {
	t.Helper()
	lg, err := log.New("error", io.Discard)
	if err != nil {
		t.Fatalf("log.New: %v", err)
	}
	return lg
}

// startServer builds a loopback metrics server over src, starts it, and registers
// cleanup. It returns the running server.
func startServer(t *testing.T, src Source) *Server {
	t.Helper()
	srv, err := NewServer("127.0.0.1:0", src, testLogger(t))
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	srv.Start()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := srv.Close(ctx); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return srv
}

// TestExpositionPerPathSeries drives the registry with synthetic per-path
// telemetry, scrapes the running endpoint, and asserts the exposition carries the
// expected per-path gauges/counters (bytes, loss, RTT, throughput, jitter, up)
// with the injected values.
func TestExpositionPerPathSeries(t *testing.T) {
	src := fakeSource{paths: []PathSnapshot{
		{
			Name:                    "starlink",
			TxBytes:                 123456,
			RxBytes:                 654321,
			ThroughputBitsPerSecond: 8_000_000,
			Estimate: telemetry.Estimate{
				RTT:    50 * time.Millisecond,
				Jitter: 5 * time.Millisecond,
				Loss:   0.1,
			},
			State: telemetry.StateUp,
		},
		{
			Name:                    "cellular",
			TxBytes:                 1000,
			RxBytes:                 2000,
			ThroughputBitsPerSecond: 500_000,
			Estimate: telemetry.Estimate{
				RTT:    120 * time.Millisecond,
				Jitter: 30 * time.Millisecond,
				Loss:   0.25,
			},
			State: telemetry.StateDown,
		},
	}}

	srv := startServer(t, src)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	exp, err := Fetch(ctx, http.DefaultClient, srv.URL())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	type check struct {
		metric string
		path   string
		want   float64
	}
	checks := []check{
		{MetricTxBytes, "starlink", 123456},
		{MetricRxBytes, "starlink", 654321},
		{MetricLoss, "starlink", 0.1},
		{MetricRTT, "starlink", 0.05},
		{MetricJitter, "starlink", 0.005},
		{MetricThroughput, "starlink", 8_000_000},
		{MetricUp, "starlink", 1},
		{MetricTxBytes, "cellular", 1000},
		{MetricRxBytes, "cellular", 2000},
		{MetricLoss, "cellular", 0.25},
		{MetricRTT, "cellular", 0.12},
		{MetricThroughput, "cellular", 500_000},
		{MetricUp, "cellular", 0},
	}
	for _, c := range checks {
		got, ok := exp.PathValue(c.metric, c.path)
		if !ok {
			t.Errorf("series %s{path=%q} missing", c.metric, c.path)
			continue
		}
		if got != c.want {
			t.Errorf("%s{path=%q} = %v, want %v", c.metric, c.path, got, c.want)
		}
	}
}

// TestExpositionSessionSeries drives the collector with a live-shaped WG-session
// snapshot and asserts the two connection-scoped session series (I2) register and
// carry the injected verdict/age: established maps to the 0/1 gauge and the last
// handshake age maps to the seconds gauge.
func TestExpositionSessionSeries(t *testing.T) {
	src := fakeSource{session: SessionSnapshot{
		Established:      true,
		LastHandshakeAge: 12 * time.Second,
	}}
	srv := startServer(t, src)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	exp, err := Fetch(ctx, http.DefaultClient, srv.URL())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	if !exp.Has(MetricSessionEstablished) {
		t.Fatalf("%s not registered", MetricSessionEstablished)
	}
	if v, ok := exp.Value(MetricSessionEstablished); !ok || v != 1 {
		t.Errorf("%s = %v (present=%v), want 1", MetricSessionEstablished, v, ok)
	}
	if v, ok := exp.Value(MetricSessionLastHandshake); !ok || v != 12 {
		t.Errorf("%s = %v (present=%v), want 12", MetricSessionLastHandshake, v, ok)
	}
}

// TestExpositionSessionNotEstablished asserts that a not-yet-converged session (no
// completed handshake) exposes established=0 with a zero handshake age — the "still
// converging" reading the metric exists to make observable.
func TestExpositionSessionNotEstablished(t *testing.T) {
	srv := startServer(t, fakeSource{})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	exp, err := Fetch(ctx, http.DefaultClient, srv.URL())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	if v, ok := exp.Value(MetricSessionEstablished); !ok || v != 0 {
		t.Errorf("%s = %v (present=%v), want 0", MetricSessionEstablished, v, ok)
	}
	if v, ok := exp.Value(MetricSessionLastHandshake); !ok || v != 0 {
		t.Errorf("%s = %v (present=%v), want 0", MetricSessionLastHandshake, v, ok)
	}
}

// TestExpositionRawText asserts the raw exposition text carries a per-path series
// line verbatim, proving the text-format surface (not just the parsed view) is
// what a Prometheus scraper would see.
func TestExpositionRawText(t *testing.T) {
	src := fakeSource{paths: []PathSnapshot{{
		Name:    "starlink",
		TxBytes: 123456,
		State:   telemetry.StateUp,
	}}}
	srv := startServer(t, src)

	req, err := http.NewRequest(http.MethodGet, srv.URL(), nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	text := string(body)

	for _, want := range []string{
		`wanbond_path_tx_bytes_total{path="starlink"} 123456`,
		`wanbond_path_up{path="starlink"} 1`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("exposition missing line %q\n---\n%s", want, text)
		}
	}
}

// TestFECPlaceholdersRegistered asserts the FEC counters are registered now and
// exposed with a zero value (populated later in P3).
func TestFECPlaceholdersRegistered(t *testing.T) {
	srv := startServer(t, fakeSource{fec: []FECSnapshot{{}}})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	exp, err := Fetch(ctx, http.DefaultClient, srv.URL())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	for _, name := range []string{MetricFECData, MetricFECRepair, MetricFECRecovered, MetricFECUnrecoverable} {
		if !exp.Has(name) {
			t.Errorf("FEC placeholder %s not registered", name)
			continue
		}
		v, ok := exp.Value(name)
		if !ok {
			t.Errorf("FEC placeholder %s has no unlabeled sample", name)
			continue
		}
		if v != 0 {
			t.Errorf("FEC placeholder %s = %v, want 0", name, v)
		}
	}
}

// TestExpositionFECCounters drives the collector with a live-shaped FEC snapshot and
// asserts the three connection-scoped FEC series carry the injected values (T24), not
// the pre-T24 constant zero — the exposition reads the FEC plane, not a placeholder.
func TestExpositionFECCounters(t *testing.T) {
	src := fakeSource{fec: []FECSnapshot{{
		DataPackets:          300,
		RepairPackets:        120,
		RecoveredPackets:     7,
		UnrecoverablePackets: 2,
	}}}
	srv := startServer(t, src)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	exp, err := Fetch(ctx, http.DefaultClient, srv.URL())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	checks := []struct {
		name string
		want float64
	}{
		{MetricFECData, 300},
		{MetricFECRepair, 120},
		{MetricFECRecovered, 7},
		{MetricFECUnrecoverable, 2},
	}
	for _, c := range checks {
		got, ok := exp.Value(c.name)
		if !ok {
			t.Errorf("FEC series %s missing", c.name)
			continue
		}
		if got != c.want {
			t.Errorf("%s = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestExpositionSinglePeerByteCompatible asserts a single-bound-peer Source's raw
// scrape text carries NO `peer` label anywhere — the T94 back-compat rule: a
// single-peer exposition is byte-identical to the pre-T94 series (path/FEC/
// resequencer alike), never adding an empty-valued label pair.
func TestExpositionSinglePeerByteCompatible(t *testing.T) {
	src := fakeSource{
		paths: []PathSnapshot{{Name: "starlink", TxBytes: 123456, State: telemetry.StateUp}},
		fec:   []FECSnapshot{{DataPackets: 300, RepairPackets: 120}},
		reseq: []ReseqSnapshot{{Stats: reseq.Stats{Released: 42}}},
	}
	srv := startServer(t, src)

	req, err := http.NewRequest(http.MethodGet, srv.URL(), nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	text := string(body)

	if strings.Contains(text, "peer=") {
		t.Errorf("single-peer exposition unexpectedly carries a `peer` label:\n%s", text)
	}
	for _, want := range []string{
		`wanbond_path_tx_bytes_total{path="starlink"} 123456`,
		`wanbond_fec_data_packets_total 300`,
		`wanbond_fec_repair_packets_total 120`,
		`wanbond_resequencer_released_frames_total 42`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("exposition missing line %q\n---\n%s", want, text)
		}
	}
}

// TestExpositionTwoPeerSeries asserts a 2-peer Source's scrape carries DISTINCT `peer`
// labels on the path, resequencer, and FEC series, attributable to each edge, with
// independent counters (T94) — the multi-peer half of the back-compat rule. The PRIMARY
// (first-configured) peer carries its own configured non-empty name too, not "" (D58):
// device.Up plumbs the primary's configured identity name into the bind
// (bind.Multipath.SetPrimaryPeerName) whenever more than one peer is configured, so a
// two-peer concentrator's metrics attribute every series — including the primary's — to a
// named edge.
func TestExpositionTwoPeerSeries(t *testing.T) {
	src := fakeSource{
		peerNames: []string{"edge1", "edge2"},
		paths: []PathSnapshot{
			{Peer: "edge1", Name: "starlink", TxBytes: 100, ThroughputBitsPerSecond: 111, State: telemetry.StateUp},
			{Peer: "edge2", Name: "starlink", TxBytes: 900, ThroughputBitsPerSecond: 999, State: telemetry.StateUp},
		},
		fec: []FECSnapshot{
			{Peer: "edge1", DataPackets: 10},
			{Peer: "edge2", DataPackets: 700},
		},
		reseq: []ReseqSnapshot{
			{Peer: "edge1", Stats: reseq.Stats{Released: 5}},
			{Peer: "edge2", Stats: reseq.Stats{Released: 900}},
		},
	}
	srv := startServer(t, src)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	exp, err := Fetch(ctx, http.DefaultClient, srv.URL())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	if got, ok := exp.PeerPathValue(MetricTxBytes, "edge1", "starlink"); !ok || got != 100 {
		t.Errorf("primary path tx_bytes = %v (present=%v), want 100", got, ok)
	}
	if got, ok := exp.PeerPathValue(MetricTxBytes, "edge2", "starlink"); !ok || got != 900 {
		t.Errorf("edge2 path tx_bytes = %v (present=%v), want 900", got, ok)
	}
	if got, ok := exp.PeerPathValue(MetricThroughput, "edge1", "starlink"); !ok || got != 111 {
		t.Errorf("primary path throughput = %v (present=%v), want 111", got, ok)
	}
	if got, ok := exp.PeerPathValue(MetricThroughput, "edge2", "starlink"); !ok || got != 999 {
		t.Errorf("edge2 path throughput = %v (present=%v), want 999", got, ok)
	}

	if got, ok := exp.PeerValue(MetricFECData, "edge1"); !ok || got != 10 {
		t.Errorf("primary FEC data packets = %v (present=%v), want 10", got, ok)
	}
	if got, ok := exp.PeerValue(MetricFECData, "edge2"); !ok || got != 700 {
		t.Errorf("edge2 FEC data packets = %v (present=%v), want 700 (independent of primary)", got, ok)
	}

	if got, ok := exp.PeerValue(MetricReseqReleased, "edge1"); !ok || got != 5 {
		t.Errorf("primary resequencer released = %v (present=%v), want 5", got, ok)
	}
	if got, ok := exp.PeerValue(MetricReseqReleased, "edge2"); !ok || got != 900 {
		t.Errorf("edge2 resequencer released = %v (present=%v), want 900 (independent of primary)", got, ok)
	}
}

// TestExpositionReseqRebaselineAndDropSuspect drives a REAL reseq.Resequencer
// through a Rebaseline() and a dropSuspect-triggering ObserveRecovered (T118),
// then scrapes /metrics and asserts both counters are reflected in the
// single-peer (no `peer` label) exposition — proving the restart-recovery
// counters propagate end-to-end from the resequencer's own increments, not just
// that a synthetic Stats value round-trips through the collector.
func TestExpositionReseqRebaselineAndDropSuspect(t *testing.T) {
	const window = 8
	r := reseq.New(window, time.Second, reseq.SystemClock{})

	// Advance the release point well past one window so a lone recovered frame
	// far below it lands in ObserveRecovered's SUSPECT branch (reseq.go:247-249),
	// which never attempts a resync and so needs no corroborating frames.
	for s := uint64(0); s < 50; s++ {
		r.Observe(s, []byte{byte(s)}, netip.AddrPort{})
	}
	for {
		if _, ok := r.Pop(); !ok {
			break
		}
	}

	if placed := r.ObserveRecovered(1, []byte{1}, netip.AddrPort{}); placed {
		t.Fatal("expected the far-below-window recovered frame to be dropped, not placed")
	}
	if got := r.Stats().DroppedSuspect; got != 1 {
		t.Fatalf("precondition: DroppedSuspect = %d, want 1", got)
	}

	r.Rebaseline()
	if got := r.Stats().Rebaselines; got != 1 {
		t.Fatalf("precondition: Rebaselines = %d, want 1", got)
	}

	src := fakeSource{reseq: []ReseqSnapshot{{Stats: r.Stats()}}}
	srv := startServer(t, src)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	exp, err := Fetch(ctx, http.DefaultClient, srv.URL())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	if got, ok := exp.Value(MetricReseqRebaselines); !ok || got != 1 {
		t.Errorf("%s = %v (present=%v), want 1", MetricReseqRebaselines, got, ok)
	}
	if got, ok := exp.Value(MetricReseqDroppedSuspect); !ok || got != 1 {
		t.Errorf("%s = %v (present=%v), want 1", MetricReseqDroppedSuspect, got, ok)
	}
}
