package metrics

import (
	"context"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/reseq"
)

// TestExpositionReseqHoldSignal asserts the T242 HoL-stall series — registration,
// exact names, and values from a scripted Source. The holds/hold-seconds counter
// PAIR gives the mean hold (hold_seconds/holds). hold_seconds is derived from the
// resequencer's nanosecond accumulator (HoldNanos/1e9).
func TestExpositionReseqHoldSignal(t *testing.T) {
	const holdNanos = uint64(500_000_000) // 0.5s cumulative held time
	src := fakeSource{reseq: []ReseqSnapshot{{Stats: reseq.Stats{
		Holds:     3,
		HoldNanos: holdNanos,
	}}}}
	srv := startServer(t, src)

	// Registration + naming: the raw single-peer scrape carries the two series
	// by their exact names, with no `peer` label (T94 back-compat shape).
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
		`wanbond_resequencer_hol_holds_total 3`,
		`wanbond_resequencer_hol_hold_seconds_total 0.5`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("exposition missing line %q\n---\n%s", want, text)
		}
	}

	// Values via the parsed exposition helper.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	exp, err := Fetch(ctx, http.DefaultClient, srv.URL())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got, ok := exp.Value(MetricReseqHolds); !ok || got != 3 {
		t.Errorf("%s = %v (present=%v), want 3", MetricReseqHolds, got, ok)
	}
	if got, ok := exp.Value(MetricReseqHoldSeconds); !ok || got != 0.5 {
		t.Errorf("%s = %v (present=%v), want 0.5", MetricReseqHoldSeconds, got, ok)
	}
}

// t242Clock is a hand-advanced reseq.Clock so the end-to-end propagation test drives
// a REAL resequencer through a held-then-skipped gap deterministically.
type t242Clock struct{ now time.Time }

func (c *t242Clock) Now() time.Time { return c.now }

// TestExpositionReseqHoldSignalEndToEnd drives a REAL reseq.Resequencer through a
// head-of-line gap that is held for its full timeout and then skipped, under a fake
// clock, then scrapes /metrics and asserts the hold counters propagate end-to-end
// from the resequencer's own increments (not merely that a synthetic Stats
// round-trips).
func TestExpositionReseqHoldSignalEndToEnd(t *testing.T) {
	clk := &t242Clock{now: time.Unix(1_700_000_000, 0)}
	const (
		window  = 64
		timeout = 250 * time.Millisecond
	)
	r := reseq.New(window, timeout, clk)

	r.Observe(0, []byte{0}, netip.AddrPort{})
	for {
		if _, ok := r.Pop(); !ok {
			break
		}
	}
	// A gap at 1 arms a hold; 2,3 are released when the hold elapses.
	r.Observe(2, []byte{2}, netip.AddrPort{})
	r.Observe(3, []byte{3}, netip.AddrPort{})
	clk.now = clk.now.Add(timeout)
	for {
		if _, ok := r.Pop(); !ok {
			break
		}
	}

	s := r.Stats()
	if s.Holds != 1 || s.HoldNanos != uint64(timeout.Nanoseconds()) || s.Released != 3 {
		t.Fatalf("precondition: Holds=%d HoldNanos=%d Released=%d, want 1/%d/3", s.Holds, s.HoldNanos, s.Released, timeout.Nanoseconds())
	}

	src := fakeSource{reseq: []ReseqSnapshot{{Stats: s}}}
	srv := startServer(t, src)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	exp, err := Fetch(ctx, http.DefaultClient, srv.URL())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got, ok := exp.Value(MetricReseqHolds); !ok || got != 1 {
		t.Errorf("%s = %v (present=%v), want 1", MetricReseqHolds, got, ok)
	}
	if got, ok := exp.Value(MetricReseqHoldSeconds); !ok || got != timeout.Seconds() {
		t.Errorf("%s = %v (present=%v), want %v", MetricReseqHoldSeconds, got, ok, timeout.Seconds())
	}
}

// TestExpositionReseqDeadlineSignal asserts the live-gap deadline series: the armed
// deadline (as a Unix timestamp) and window of the current head-of-line gap, the
// deadline-wakeup and gap-fill counters, and that both gauges read 0 while disarmed.
func TestExpositionReseqDeadlineSignal(t *testing.T) {
	const (
		armedDeadline = "wanbond_resequencer_armed_deadline_timestamp_seconds"
		armedWindow   = "wanbond_resequencer_armed_window_seconds"
		wakeups       = "wanbond_resequencer_deadline_wakeups_total"
		gapFills      = "wanbond_resequencer_gap_fills_total"
	)
	scrape := func(stats reseq.Stats) Exposition {
		t.Helper()
		srv := startServer(t, fakeSource{reseq: []ReseqSnapshot{{Stats: stats}}})
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		exp, err := Fetch(ctx, http.DefaultClient, srv.URL())
		if err != nil {
			t.Fatalf("Fetch: %v", err)
		}
		return exp
	}

	armed := scrape(reseq.Stats{
		ArmedDeadline:   time.Unix(1_700_000_000, 500_000_000),
		ArmedWindow:     18 * time.Millisecond,
		DeadlineWakeups: 19,
		GapFills:        20,
	})
	for name, want := range map[string]float64{
		armedDeadline: 1_700_000_000.5,
		armedWindow:   0.018,
		wakeups:       19,
		gapFills:      20,
	} {
		if got, ok := armed.Value(name); !ok || got != want {
			t.Errorf("%s = %v (present=%v), want %v", name, got, ok, want)
		}
	}

	disarmed := scrape(reseq.Stats{DeadlineWakeups: 19, GapFills: 20})
	for _, name := range []string{armedDeadline, armedWindow} {
		if got, ok := disarmed.Value(name); !ok || got != 0 {
			t.Errorf("disarmed %s = %v (present=%v), want 0", name, got, ok)
		}
	}
}
