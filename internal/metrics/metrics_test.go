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
	paths       []PathSnapshot
	fec         []FECSnapshot
	reseq       []ReseqSnapshot
	aggregation []AggregationSnapshot
	session     SessionSnapshot
	peerNames   []string
}

func (f fakeSource) Paths() []PathSnapshot              { return f.paths }
func (f fakeSource) FEC() []FECSnapshot                 { return f.fec }
func (f fakeSource) Reseq() []ReseqSnapshot             { return f.reseq }
func (f fakeSource) Aggregation() []AggregationSnapshot { return f.aggregation }
func (f fakeSource) Session() SessionSnapshot           { return f.session }
func (f fakeSource) PeerNames() []string                { return f.peerNames }

func testLogger(t *testing.T) log.Logger {
	t.Helper()
	lg, err := log.New("error", io.Discard)
	if err != nil {
		t.Fatalf("log.New: %v", err)
	}
	return lg
}

// startServer builds a loopback metrics server over src, starts it, and registers
// cleanup. It returns the running server. weightedCapacitySane is nil (the
// non-weighted-policy shape — no wanbond_weighted_capacity_sane series at all) for
// every existing caller; see startServerWithCapacity for the T144 registration tests.
func startServer(t *testing.T, src Source) *Server {
	t.Helper()
	return startServerWithCapacity(t, src, nil)
}

// startServerWithCapacity is startServer plus an explicit T144 weightedCapacitySane
// verdict, for the wanbond_weighted_capacity_sane registration tests. The T211
// liveness-budget verdict is left nil (no wanbond_liveness_budget_sane series); see
// startServerWithLivenessBudget for that gauge's registration tests.
func startServerWithCapacity(t *testing.T, src Source, weightedCapacitySane *bool) *Server {
	t.Helper()
	srv, err := NewServer("127.0.0.1:0", src, weightedCapacitySane, nil, testLogger(t))
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

// startServerWithLivenessBudget is startServer plus an explicit T211 livenessBudgetSane
// verdict (weighted verdict left nil), for the wanbond_liveness_budget_sane gauge tests.
func startServerWithLivenessBudget(t *testing.T, src Source, livenessBudgetSane *bool) *Server {
	t.Helper()
	srv, err := NewServer("127.0.0.1:0", src, nil, livenessBudgetSane, testLogger(t))
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

// TestLivenessBudgetSaneGaugeValue asserts the T211 gauge, when a verdict IS supplied,
// exposes an unlabeled series carrying exactly that value — 1 within budget, 0 over.
func TestLivenessBudgetSaneGaugeValue(t *testing.T) {
	for _, tc := range []struct {
		name string
		sane bool
		want float64
	}{
		{"within-budget", true, 1},
		{"over-budget", false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sane := tc.sane
			srv := startServerWithLivenessBudget(t, fakeSource{}, &sane)

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			exp, err := Fetch(ctx, http.DefaultClient, srv.URL())
			if err != nil {
				t.Fatalf("Fetch: %v", err)
			}
			got, ok := exp.Value(MetricLivenessBudgetSane)
			if !ok {
				t.Fatalf("%s series absent, want present", MetricLivenessBudgetSane)
			}
			if got != tc.want {
				t.Errorf("%s = %v, want %v", MetricLivenessBudgetSane, got, tc.want)
			}
		})
	}
}

// TestLivenessBudgetSaneGaugeReSetOnReload mirrors the D74 weighted-gauge guard: the
// gauge is NOT frozen at construction — SetLivenessBudgetSane (called by device.Reload
// when an applied path change moves the worst-case ride_through) must move the LIVE
// scraped series. Boots within-budget (1), re-sets to over-budget (0), asserts 0.
func TestLivenessBudgetSaneGaugeReSetOnReload(t *testing.T) {
	sane := true
	srv := startServerWithLivenessBudget(t, fakeSource{}, &sane)

	scrape := func() float64 {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		exp, err := Fetch(ctx, http.DefaultClient, srv.URL())
		if err != nil {
			t.Fatalf("Fetch: %v", err)
		}
		got, ok := exp.Value(MetricLivenessBudgetSane)
		if !ok {
			t.Fatalf("%s series absent, want present", MetricLivenessBudgetSane)
		}
		return got
	}

	if got := scrape(); got != 1 {
		t.Fatalf("%s at boot = %v, want 1 (within budget)", MetricLivenessBudgetSane, got)
	}
	srv.SetLivenessBudgetSane(false)
	if got := scrape(); got != 0 {
		t.Errorf("%s after SetLivenessBudgetSane(false) = %v, want 0", MetricLivenessBudgetSane, got)
	}
}

// TestExpositionPathMTUSeries asserts the wanbond_path_mtu gauge (T206) registers and
// carries each path's discovered PMTU from PathSnapshot.PMTU verbatim, per `path`
// label — the discovery machine's snapshot mirrored into the exposition.
func TestExpositionPathMTUSeries(t *testing.T) {
	src := fakeSource{paths: []PathSnapshot{
		{Name: "starlink", State: telemetry.StateUp, PMTU: 1400},
		{Name: "cellular", State: telemetry.StateUp, PMTU: 1280},
	}}
	srv := startServer(t, src)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	exp, err := Fetch(ctx, http.DefaultClient, srv.URL())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !exp.Has(MetricPathMTU) {
		t.Fatalf("%s not registered", MetricPathMTU)
	}
	for _, c := range []struct {
		path string
		want float64
	}{
		{"starlink", 1400},
		{"cellular", 1280},
	} {
		got, ok := exp.PathValue(MetricPathMTU, c.path)
		if !ok {
			t.Errorf("%s{path=%q} missing", MetricPathMTU, c.path)
			continue
		}
		if got != c.want {
			t.Errorf("%s{path=%q} = %v, want %v", MetricPathMTU, c.path, got, c.want)
		}
	}
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

// TestExpositionAdaptiveFECSinglePeer is the T263/D96 acceptance at the metrics-package
// seam: a single-peer FECSnapshot carrying a non-nil Adaptive emits all four adaptive
// series with NO `peer` label (the T94 single-peer back-compat rule) and the exact
// injected values.
func TestExpositionAdaptiveFECSinglePeer(t *testing.T) {
	src := fakeSource{fec: []FECSnapshot{{
		DataPackets: 300,
		Adaptive: &AdaptiveFECStats{
			Parity:        3,
			SmoothedLoss:  0.021,
			EligibleLoss:  0.05,
			EligiblePaths: 2,
		},
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
		{MetricFECAdaptiveParity, 3},
		{MetricFECSmoothedLoss, 0.021},
		{MetricFECEligiblePathLoss, 0.05},
		{MetricFECEligiblePaths, 2},
	}
	for _, c := range checks {
		got, ok := exp.Value(c.name) // unlabeled series (single-peer omits `peer`)
		if !ok {
			t.Errorf("%s missing (or carries an unexpected peer label)", c.name)
			continue
		}
		if got != c.want {
			t.Errorf("%s = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestExpositionAdaptiveFECMultiPeer is the T263/D96 multi-peer acceptance: a 2-peer
// Source with one peer running the adaptive controller and the other fixed-ratio
// carries the `peer` label on the adaptive peer's series, with the fixed-ratio peer
// contributing none of the four adaptive series.
func TestExpositionAdaptiveFECMultiPeer(t *testing.T) {
	src := fakeSource{
		peerNames: []string{"edge1", "edge2"},
		fec: []FECSnapshot{
			{Peer: "edge1", DataPackets: 10, Adaptive: &AdaptiveFECStats{Parity: 4, SmoothedLoss: 0.03, EligibleLoss: 0.06, EligiblePaths: 3}},
			{Peer: "edge2", DataPackets: 700}, // fixed-ratio: no Adaptive
		},
	}
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
		{MetricFECAdaptiveParity, 4},
		{MetricFECSmoothedLoss, 0.03},
		{MetricFECEligiblePathLoss, 0.06},
		{MetricFECEligiblePaths, 3},
	}
	for _, c := range checks {
		got, ok := exp.PeerValue(c.name, "edge1")
		if !ok {
			t.Errorf("%s{peer=\"edge1\"} missing", c.name)
			continue
		}
		if got != c.want {
			t.Errorf("%s{peer=\"edge1\"} = %v, want %v", c.name, got, c.want)
		}
		if _, ok := exp.PeerValue(c.name, "edge2"); ok {
			t.Errorf("%s{peer=\"edge2\"} present, want absent (fixed-ratio peer carries no Adaptive)", c.name)
		}
	}
}

// TestAdaptiveFECAbsentWhenNil is the T263/D96 absent-series acceptance: a Source whose
// FECSnapshot.Adaptive is nil (fixed-ratio or FEC-off) exposes NONE of the four adaptive
// series, mirroring TestAggregationAbsentUnderActiveBackup.
func TestAdaptiveFECAbsentWhenNil(t *testing.T) {
	srv := startServer(t, fakeSource{fec: []FECSnapshot{{DataPackets: 300}}})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	exp, err := Fetch(ctx, http.DefaultClient, srv.URL())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	for _, name := range []string{
		MetricFECAdaptiveParity,
		MetricFECSmoothedLoss,
		MetricFECEligiblePathLoss,
		MetricFECEligiblePaths,
	} {
		if exp.Has(name) {
			t.Errorf("%s registered under a nil Adaptive FECSnapshot, want absent entirely", name)
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

	r.Rebaseline(netip.AddrPort{})
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

// TestExpositionAggregationSinglePeer is T146 acceptance (i) at the metrics-package
// seam: a single-peer weighted Source.Aggregation() emits all four Q54 gauge families
// with NO `peer` label (the T94 single-peer back-compat rule), engaged reads 0 at idle,
// and each threshold gauge carries exactly engage/disengage_fraction*per_path_capacity_fps.
func TestExpositionAggregationSinglePeer(t *testing.T) {
	const (
		perPathCapacityFPS = 700.0
		engageFraction     = 0.9
		disengageFraction  = 0.5
	)
	engageTh := engageFraction * perPathCapacityFPS       // 630
	disengageTh := disengageFraction * perPathCapacityFPS // 350

	src := fakeSource{aggregation: []AggregationSnapshot{{
		Peer:                  "",
		Aggregating:           false, // idle
		OfferedLoadFPS:        0,
		EngageThresholdFPS:    engageTh,
		DisengageThresholdFPS: disengageTh,
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
		{MetricAggregationEngaged, 0},
		{MetricOfferedLoadFPS, 0},
		{MetricAggregationEngageThreshold, engageTh},
		{MetricAggregationDisengageThreshold, disengageTh},
	}
	for _, c := range checks {
		if !exp.Has(c.name) {
			t.Errorf("aggregation series %s absent, want present", c.name)
			continue
		}
		got, ok := exp.Value(c.name) // unlabeled series (single-peer omits `peer`)
		if !ok {
			t.Errorf("%s has no unlabeled sample (single-peer must omit the peer label)", c.name)
			continue
		}
		if got != c.want {
			t.Errorf("%s = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestExpositionAggregationEngagedGauge asserts the engaged gauge reflects the live gate
// verdict: Aggregating=true maps to 1 (striping), independent of the threshold gauges.
func TestExpositionAggregationEngagedGauge(t *testing.T) {
	src := fakeSource{aggregation: []AggregationSnapshot{{
		Aggregating:           true,
		OfferedLoadFPS:        640,
		EngageThresholdFPS:    630,
		DisengageThresholdFPS: 350,
	}}}
	srv := startServer(t, src)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	exp, err := Fetch(ctx, http.DefaultClient, srv.URL())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if v, ok := exp.Value(MetricAggregationEngaged); !ok || v != 1 {
		t.Errorf("%s = %v (present=%v), want 1 (gate engaged)", MetricAggregationEngaged, v, ok)
	}
	if v, ok := exp.Value(MetricOfferedLoadFPS); !ok || v != 640 {
		t.Errorf("%s = %v (present=%v), want 640", MetricOfferedLoadFPS, v, ok)
	}
}

// TestExpositionAggregationMultiPeer is T146 acceptance (iii) at the metrics seam: a
// 2-peer Source.Aggregation() carries the `peer` label (PeerValue resolves each series),
// with each edge's own gate verdict, offered load, and thresholds.
func TestExpositionAggregationMultiPeer(t *testing.T) {
	src := fakeSource{
		peerNames: []string{"edge1", "edge2"},
		aggregation: []AggregationSnapshot{
			{Peer: "edge1", Aggregating: false, OfferedLoadFPS: 10, EngageThresholdFPS: 630, DisengageThresholdFPS: 350},
			{Peer: "edge2", Aggregating: true, OfferedLoadFPS: 900, EngageThresholdFPS: 810, DisengageThresholdFPS: 450},
		},
	}
	srv := startServer(t, src)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	exp, err := Fetch(ctx, http.DefaultClient, srv.URL())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	checks := []struct {
		name string
		peer string
		want float64
	}{
		{MetricAggregationEngaged, "edge1", 0},
		{MetricAggregationEngaged, "edge2", 1},
		{MetricOfferedLoadFPS, "edge1", 10},
		{MetricOfferedLoadFPS, "edge2", 900},
		{MetricAggregationEngageThreshold, "edge1", 630},
		{MetricAggregationEngageThreshold, "edge2", 810},
		{MetricAggregationDisengageThreshold, "edge1", 350},
		{MetricAggregationDisengageThreshold, "edge2", 450},
	}
	for _, c := range checks {
		got, ok := exp.PeerValue(c.name, c.peer)
		if !ok {
			t.Errorf("%s{peer=%q} missing", c.name, c.peer)
			continue
		}
		if got != c.want {
			t.Errorf("%s{peer=%q} = %v, want %v", c.name, c.peer, got, c.want)
		}
	}
}

// TestAggregationAbsentUnderActiveBackup is T146 acceptance (ii) at the metrics seam: a
// Source whose Aggregation() returns no entries (the shape an active-backup Bind
// presents — no peer's scheduler exposes a gate) exposes NONE of the four aggregation
// families, not a present-but-empty or present-at-zero series.
func TestAggregationAbsentUnderActiveBackup(t *testing.T) {
	srv := startServer(t, fakeSource{})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	exp, err := Fetch(ctx, http.DefaultClient, srv.URL())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	for _, name := range []string{
		MetricAggregationEngaged,
		MetricOfferedLoadFPS,
		MetricAggregationEngageThreshold,
		MetricAggregationDisengageThreshold,
	} {
		if exp.Has(name) {
			t.Errorf("%s registered under an empty (active-backup) Aggregation(), want absent entirely", name)
		}
	}
}

// TestWeightedCapacitySaneAbsentUnderActiveBackup asserts the T144 gauge family is
// registered AT ALL only when a T144 verdict is supplied: NewServer(..., nil, ...) —
// the shape config.Config.WeightedCapacitySane presents under the active-backup
// policy — must expose no wanbond_weighted_capacity_sane series whatsoever, not a
// present-but-empty family and not a present-with-zero-value series.
func TestWeightedCapacitySaneAbsentUnderActiveBackup(t *testing.T) {
	srv := startServerWithCapacity(t, fakeSource{}, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	exp, err := Fetch(ctx, http.DefaultClient, srv.URL())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if exp.Has(MetricWeightedCapacitySane) {
		t.Errorf("%s registered under a nil (active-backup) verdict, want absent entirely", MetricWeightedCapacitySane)
	}
}

// TestWeightedCapacitySaneGaugeValue asserts the T144 gauge, when a verdict IS
// supplied, exposes an unlabeled series carrying exactly that value — 1 for
// SANE-VERIFIED, 0 for UNVERIFIABLE.
func TestWeightedCapacitySaneGaugeValue(t *testing.T) {
	for _, tc := range []struct {
		name string
		sane bool
		want float64
	}{
		{"sane-verified", true, 1},
		{"unverifiable", false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sane := tc.sane
			srv := startServerWithCapacity(t, fakeSource{}, &sane)

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			exp, err := Fetch(ctx, http.DefaultClient, srv.URL())
			if err != nil {
				t.Fatalf("Fetch: %v", err)
			}
			got, ok := exp.Value(MetricWeightedCapacitySane)
			if !ok {
				t.Fatalf("%s series absent, want present", MetricWeightedCapacitySane)
			}
			if got != tc.want {
				t.Errorf("%s = %v, want %v", MetricWeightedCapacitySane, got, tc.want)
			}
		})
	}
}

// TestWeightedCapacitySaneGaugeReSetOnReload is the D74 regression guard: the gauge is
// NOT frozen at its construction value — SetWeightedCapacitySane (called by device.Reload
// after a path add/remove recomputes the verdict) must move the LIVE scraped series. Boots
// SANE-VERIFIED (1), re-sets to UNVERIFIABLE (0), and asserts a fresh scrape reads 0. On
// the unfixed code the gauge is a throwaway local with no setter, so the value stays 1.
func TestWeightedCapacitySaneGaugeReSetOnReload(t *testing.T) {
	sane := true
	srv := startServerWithCapacity(t, fakeSource{}, &sane)

	scrape := func() float64 {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		exp, err := Fetch(ctx, http.DefaultClient, srv.URL())
		if err != nil {
			t.Fatalf("Fetch: %v", err)
		}
		got, ok := exp.Value(MetricWeightedCapacitySane)
		if !ok {
			t.Fatalf("%s series absent, want present", MetricWeightedCapacitySane)
		}
		return got
	}

	if got := scrape(); got != 1 {
		t.Fatalf("%s at boot = %v, want 1 (sane-verified)", MetricWeightedCapacitySane, got)
	}

	// A reload flips the config-derived verdict to unverifiable.
	srv.SetWeightedCapacitySane(false)

	if got := scrape(); got != 0 {
		t.Errorf("%s after SetWeightedCapacitySane(false) = %v, want 0 (frozen gauge / no setter — D74)", MetricWeightedCapacitySane, got)
	}
}

// TestSetWeightedCapacitySaneNoopWithoutGauge asserts the setter is a safe no-op under
// the active-backup policy (a nil verdict at construction — no gauge registered): a
// reload calling SetWeightedCapacitySane must neither panic nor introduce the series.
func TestSetWeightedCapacitySaneNoopWithoutGauge(t *testing.T) {
	srv := startServerWithCapacity(t, fakeSource{}, nil)

	srv.SetWeightedCapacitySane(true) // must not panic on a nil retained gauge

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	exp, err := Fetch(ctx, http.DefaultClient, srv.URL())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if exp.Has(MetricWeightedCapacitySane) {
		t.Errorf("%s appeared after SetWeightedCapacitySane on an active-backup server, want absent", MetricWeightedCapacitySane)
	}
}
