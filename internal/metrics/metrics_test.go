package metrics

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/log"
	"github.com/7mind/wanbond/internal/telemetry"
)

// fakeSource is a static Source that returns a fixed set of per-path snapshots,
// standing in for the live traffic/telemetry planes so the exposition can be
// asserted against known values.
type fakeSource struct {
	paths []PathSnapshot
	fec   FECSnapshot
}

func (f fakeSource) Paths() []PathSnapshot { return f.paths }
func (f fakeSource) FEC() FECSnapshot      { return f.fec }

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
	srv := startServer(t, fakeSource{})

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
	src := fakeSource{fec: FECSnapshot{
		DataPackets:          300,
		RepairPackets:        120,
		RecoveredPackets:     7,
		UnrecoverablePackets: 2,
	}}
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
