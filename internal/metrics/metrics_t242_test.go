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

// TestExpositionReseqHoldSignal asserts the T242 (D93 observability leg) HoL-stall
// series — registration, exact names, and values from a scripted Source. The
// holds/hold-seconds counter PAIR gives the mean hold (hold_seconds/holds), and
// immediate_releases is exposed DISTINCTLY from the skip counter so an operator can
// see the D93 amplifier is disarmed. hold_seconds is derived from the resequencer's
// nanosecond accumulator (HoldNanos/1e9).
func TestExpositionReseqHoldSignal(t *testing.T) {
	const holdNanos = uint64(500_000_000) // 0.5s cumulative held time
	src := fakeSource{reseq: []ReseqSnapshot{{Stats: reseq.Stats{
		Holds:             3,
		HoldNanos:         holdNanos,
		ImmediateReleases: 2,
	}}}}
	srv := startServer(t, src)

	// Registration + naming: the raw single-peer scrape carries the three new series
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
		`wanbond_resequencer_immediate_releases_total 2`,
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
	if got, ok := exp.Value(MetricReseqImmediateReleases); !ok || got != 2 {
		t.Errorf("%s = %v (present=%v), want 2", MetricReseqImmediateReleases, got, ok)
	}
}

// t242Clock is a fixed reseq.Clock so the end-to-end propagation test drives a REAL
// resequencer through a single-path immediate release deterministically (the fast path
// needs no clock advance — it releases with ~0 hold at the arm instant).
type t242Clock struct{ now time.Time }

func (c *t242Clock) Now() time.Time { return c.now }

// TestExpositionReseqHoldSignalEndToEnd drives a REAL reseq.Resequencer through a
// single-delivering-path immediate release under a fake clock, then scrapes /metrics
// and asserts the hold/immediate-release counters propagate end-to-end from the
// resequencer's own increments (not merely that a synthetic Stats round-trips), and
// that the immediate release did NOT inflate hold_seconds (fast path ⇒ ~0 held).
func TestExpositionReseqHoldSignalEndToEnd(t *testing.T) {
	clk := &t242Clock{now: time.Unix(1_700_000_000, 0)}
	const window = 64
	r := reseq.New(window, 250*time.Millisecond, clk)
	const key = uint32(0x99)

	r.ObserveFromPath(0, []byte{0}, netip.AddrPort{}, key)
	for {
		if _, ok := r.Pop(); !ok {
			break
		}
	}
	// A gap at 1 arms a hold; the single delivering path fast-releases 2,3 with ~0 hold.
	r.ObserveFromPath(2, []byte{2}, netip.AddrPort{}, key)
	r.ObserveFromPath(3, []byte{3}, netip.AddrPort{}, key)
	for {
		if _, ok := r.Pop(); !ok {
			break
		}
	}

	s := r.Stats()
	if s.Holds != 1 || s.ImmediateReleases != 1 || s.HoldNanos != 0 {
		t.Fatalf("precondition: Holds=%d ImmediateReleases=%d HoldNanos=%d, want 1/1/0", s.Holds, s.ImmediateReleases, s.HoldNanos)
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
	if got, ok := exp.Value(MetricReseqImmediateReleases); !ok || got != 1 {
		t.Errorf("%s = %v (present=%v), want 1", MetricReseqImmediateReleases, got, ok)
	}
	if got, ok := exp.Value(MetricReseqHoldSeconds); !ok || got != 0 {
		t.Errorf("%s = %v (present=%v), want 0 (fast path held ~0)", MetricReseqHoldSeconds, got, ok)
	}
}
