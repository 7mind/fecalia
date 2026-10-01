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
	dto "github.com/prometheus/client_model/go"
)

// fakeSource is a static Source that returns a fixed set of per-(peer,path)
// snapshots, standing in for the live traffic/telemetry planes so the exposition can
// be asserted against known values. peerNames defaults to nil (len 0), which
// NewCollector's `len(PeerNames()) > 1` test treats identically to a single bound
// peer — the pre-T94 back-compat (no `peer` label) exposition — so every EXISTING
// test below that does not set peerNames keeps exercising the byte-compatible shape.
type fakeSource struct {
	paths        []PathSnapshot
	reseq        []ReseqSnapshot
	session      SessionSnapshot
	peerSessions []PeerSessionSnapshot
	peerNames    []string
	adaptive     []AdaptiveSnapshot
}

type fakeEngineSource struct {
	fakeSource
	outbound EngineOutboundSnapshot
}

func (f fakeEngineSource) EngineOutbound() EngineOutboundSnapshot { return f.outbound }

func (f fakeSource) Paths() []PathSnapshot               { return f.paths }
func (f fakeSource) Reseq() []ReseqSnapshot              { return f.reseq }
func (f fakeSource) Session() SessionSnapshot            { return f.session }
func (f fakeSource) PeerSessions() []PeerSessionSnapshot { return f.peerSessions }
func (f fakeSource) PeerNames() []string                 { return f.peerNames }
func (f fakeSource) Adaptive() []AdaptiveSnapshot        { return f.adaptive }

func testLogger(t *testing.T) log.Logger {
	t.Helper()
	lg, err := log.New("error", io.Discard)
	if err != nil {
		t.Fatalf("log.New: %v", err)
	}
	return lg
}

// startServer builds a loopback metrics server over src, starts it, and registers
// cleanup. It returns the running server. The T211 liveness-budget verdict is nil (no
// wanbond_liveness_budget_sane series); see startServerWithLivenessBudget for that
// gauge's registration tests.
func startServer(t *testing.T, src Source) *Server {
	t.Helper()
	return startServerWithLivenessBudget(t, src, nil)
}

// startServerWithLivenessBudget is startServer plus an explicit T211 livenessBudgetSane
// verdict, for the wanbond_liveness_budget_sane gauge tests.
func startServerWithLivenessBudget(t *testing.T, src Source, livenessBudgetSane *bool) *Server {
	t.Helper()
	srv, err := NewServer("127.0.0.1:0", src, livenessBudgetSane, testLogger(t))
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

// TestLivenessBudgetSaneGaugeReSetOnReload: the
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

// TestExpositionProbeSendErrorsSeries is the D96 item 4 exposition regression: a
// path whose originating-PROBE seam accumulated unexpected send failures must expose
// wanbond_path_probe_send_errors_total rising for EXACTLY that path, leaving an
// unaffected path's series at zero.
func TestExpositionProbeSendErrorsSeries(t *testing.T) {
	src := fakeSource{paths: []PathSnapshot{
		{Name: "starlink", State: telemetry.StateUp, ProbeSendErrors: 3},
		{Name: "cellular", State: telemetry.StateUp, ProbeSendErrors: 0},
	}}
	srv := startServer(t, src)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	exp, err := Fetch(ctx, http.DefaultClient, srv.URL())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !exp.Has(MetricProbeSendErrors) {
		t.Fatalf("%s not registered", MetricProbeSendErrors)
	}
	for _, c := range []struct {
		path string
		want float64
	}{
		{"starlink", 3},
		{"cellular", 0},
	} {
		got, ok := exp.PathValue(MetricProbeSendErrors, c.path)
		if !ok {
			t.Errorf("%s{path=%q} missing", MetricProbeSendErrors, c.path)
			continue
		}
		if got != c.want {
			t.Errorf("%s{path=%q} = %v, want %v", MetricProbeSendErrors, c.path, got, c.want)
		}
	}
}

// TestExpositionSocketWriteErrorsSeries: a path's transport-datagram socket write
// errors surface as the wanbond_path_socket_write_errors_total counter for EXACTLY
// that path, independent of its probe send errors.
func TestExpositionSocketWriteErrorsSeries(t *testing.T) {
	src := fakeSource{paths: []PathSnapshot{
		{Name: "starlink", State: telemetry.StateUp, SocketWriteErrors: 7, ProbeSendErrors: 3},
		{Name: "cellular", State: telemetry.StateUp},
	}}
	srv := startServer(t, src)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	exp, err := Fetch(ctx, http.DefaultClient, srv.URL())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	family, ok := exp.Families()[MetricSocketWriteErrors]
	if !ok {
		t.Fatalf("%s not registered", MetricSocketWriteErrors)
	}
	if family.GetType() != dto.MetricType_COUNTER {
		t.Errorf("%s type = %s, want COUNTER", MetricSocketWriteErrors, family.GetType())
	}
	for _, c := range []struct {
		path string
		want float64
	}{
		{"starlink", 7},
		{"cellular", 0},
	} {
		if got, ok := exp.PathValue(MetricSocketWriteErrors, c.path); !ok || got != c.want {
			t.Errorf("%s{path=%q} = %v (present=%v), want %v", MetricSocketWriteErrors, c.path, got, ok, c.want)
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
		{MetricProbeSendErrors, "starlink", 0},
		{MetricProbeSendErrors, "cellular", 0},
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

// TestExpositionPeerSessionSinglePeer asserts a single-peer Source's PeerSessions()
// entry (Peer "") exposes wanbond_peer_session_established UNLABELLED — following the
// same T94/D58 back-compat rule as the resequencer series, distinct from the
// connection-scoped wanbond_session_established (T256, G28, M106).
func TestExpositionPeerSessionSinglePeer(t *testing.T) {
	src := fakeSource{peerSessions: []PeerSessionSnapshot{{Established: true, LastHandshakeSeconds: 3}}}
	srv := startServer(t, src)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	exp, err := Fetch(ctx, http.DefaultClient, srv.URL())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	if !exp.Has(MetricPeerSessionEstablished) {
		t.Fatalf("%s not registered", MetricPeerSessionEstablished)
	}
	if v, ok := exp.Value(MetricPeerSessionEstablished); !ok || v != 1 {
		t.Errorf("%s = %v (present=%v), want 1 unlabelled", MetricPeerSessionEstablished, v, ok)
	}
}

// TestExpositionPeerSessionTwoPeers asserts a 2-peer Source's PeerSessions() carries a
// DISTINCT `peer`-labelled wanbond_peer_session_established series per bound peer, with
// independent established verdicts — the multi-peer half of the back-compat rule
// (T256, G28, M106).
func TestExpositionPeerSessionTwoPeers(t *testing.T) {
	src := fakeSource{
		peerNames: []string{"edge1", "edge2"},
		peerSessions: []PeerSessionSnapshot{
			{Peer: "edge1", Established: true, LastHandshakeSeconds: 3},
			{Peer: "edge2", Established: false, LastHandshakeSeconds: 0},
		},
	}
	srv := startServer(t, src)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	exp, err := Fetch(ctx, http.DefaultClient, srv.URL())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	if got, ok := exp.PeerValue(MetricPeerSessionEstablished, "edge1"); !ok || got != 1 {
		t.Errorf("edge1 %s = %v (present=%v), want 1", MetricPeerSessionEstablished, got, ok)
	}
	if got, ok := exp.PeerValue(MetricPeerSessionEstablished, "edge2"); !ok || got != 0 {
		t.Errorf("edge2 %s = %v (present=%v), want 0", MetricPeerSessionEstablished, got, ok)
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

// TestExpositionSinglePeerByteCompatible asserts a single-bound-peer Source's raw
// scrape text carries NO `peer` label anywhere — the T94 back-compat rule: a
// single-peer exposition is byte-identical to the pre-T94 series (path and
// resequencer alike), never adding an empty-valued label pair.
func TestExpositionSinglePeerByteCompatible(t *testing.T) {
	src := fakeSource{
		paths: []PathSnapshot{{Name: "starlink", TxBytes: 123456, State: telemetry.StateUp}},
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
		`wanbond_resequencer_released_frames_total 42`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("exposition missing line %q\n---\n%s", want, text)
		}
	}
}

// TestExpositionTwoPeerSeries asserts a 2-peer Source's scrape carries DISTINCT `peer`
// labels on the path and resequencer series, attributable to each edge, with
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

	if got, ok := exp.PeerValue(MetricReseqReleased, "edge1"); !ok || got != 5 {
		t.Errorf("primary resequencer released = %v (present=%v), want 5", got, ok)
	}
	if got, ok := exp.PeerValue(MetricReseqReleased, "edge2"); !ok || got != 900 {
		t.Errorf("edge2 resequencer released = %v (present=%v), want 900 (independent of primary)", got, ok)
	}
}

// TestExpositionReseqRebaselineAndDropSuspect drives a REAL reseq.Resequencer
// through a RebaselineAt() and a dropSuspect-triggering Observe (T118),
// then scrapes /metrics and asserts both counters are reflected in the
// single-peer (no `peer` label) exposition — proving the restart-recovery
// counters propagate end-to-end from the resequencer's own increments, not just
// that a synthetic Stats value round-trips through the collector.
func TestExpositionReseqRebaselineAndDropSuspect(t *testing.T) {
	const window = 8
	r := reseq.New(window, time.Second, reseq.SystemClock{})

	// Advance the release point well past one window so a lone frame far below it
	// is SUSPECT: it starts a discontinuity run but cannot corroborate one alone.
	for s := uint64(0); s < 50; s++ {
		r.Observe(s, []byte{byte(s)}, netip.AddrPort{})
	}
	for {
		if _, ok := r.Pop(); !ok {
			break
		}
	}

	r.Observe(1, []byte{1}, netip.AddrPort{})
	if _, ok := r.Pop(); ok {
		t.Fatal("expected the far-below-window frame to be dropped, not delivered")
	}
	if got := r.Stats().DroppedSuspect; got != 1 {
		t.Fatalf("precondition: DroppedSuspect = %d, want 1", got)
	}

	r.RebaselineAt(1)
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

func TestEngineOutboundExposition(t *testing.T) {
	src := fakeEngineSource{outbound: EngineOutboundSnapshot{
		TUNBytes: 65536,
		TUNBatchFrames: EngineBatchHistogram{
			Count:   2,
			Frames:  9,
			Buckets: map[uint64]uint64{1: 1, 8: 2, 128: 2},
		},
		SendBytes:                 66000,
		SendBatchFrames:           EngineBatchHistogram{Count: 1, Frames: 8, Buckets: map[uint64]uint64{8: 1, 128: 1}},
		EncryptionQueueContainers: 1,
		EncryptionQueueHighWater:  2,
		PeerQueueContainers:       1,
		PeerQueueHighWater:        1,
		ActiveSendFrames:          8,
		ActiveSendBytes:           66000,
		ActiveSendFramesHighWater: 128,
		ActiveSendBytesHighWater:  65536,
	}}
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
		`wanbond_engine_tun_bytes_total 65536`,
		`wanbond_engine_tun_batch_frames_bucket{le="8"} 2`,
		`wanbond_engine_tun_batch_frames_sum 9`,
		`wanbond_engine_tun_batch_frames_count 2`,
		`wanbond_engine_send_bytes_total 66000`,
		`wanbond_engine_encryption_queue_high_water_containers 2`,
		`wanbond_engine_peer_queue_high_water_containers 1`,
		`wanbond_engine_active_send_frames_high_water 128`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("exposition missing line %q\n---\n%s", want, text)
		}
	}
}
