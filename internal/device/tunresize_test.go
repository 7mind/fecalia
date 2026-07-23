package device

import (
	"io"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/bind"
	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/log"
	"github.com/7mind/wanbond/internal/metrics"
	"github.com/7mind/wanbond/internal/telemetry"
)

// stubMTUSource is a minimal metrics.Source returning a fixed path set, for exercising
// sampleMTU without a live bind/engine.
type stubMTUSource struct{ paths []metrics.PathSnapshot }

func (s stubMTUSource) Paths() []metrics.PathSnapshot               { return s.paths }
func (s stubMTUSource) FEC() []metrics.FECSnapshot                  { return nil }
func (s stubMTUSource) Reseq() []metrics.ReseqSnapshot              { return nil }
func (s stubMTUSource) Aggregation() []metrics.AggregationSnapshot  { return nil }
func (s stubMTUSource) Session() metrics.SessionSnapshot            { return metrics.SessionSnapshot{} }
func (s stubMTUSource) PeerSessions() []metrics.PeerSessionSnapshot { return nil }
func (s stubMTUSource) PeerNames() []string                         { return []string{""} }

// TestSampleMTUReservesJunkPrefix locks the T225<->T209 reconciliation: the runtime
// resizer reserves the amnezia junk-prefix headroom on the effective outer MTU, matching
// tunMTU, so a membership change on an obfuscated bond cannot loosen wanbond0 past the
// junk-safe envelope. Obfuscation off is byte-identical (junk = 0).
func TestSampleMTUReservesJunkPrefix(t *testing.T) {
	src := stubMTUSource{paths: []metrics.PathSnapshot{{Name: "wan", State: telemetry.StateUp, PMTU: 1500}}}
	off := sampleMTU(src, &config.Config{Paths: []config.Path{{Name: "wan"}}})
	on := sampleMTU(src, &config.Config{Paths: []config.Path{{Name: "wan"}}, Amnezia: config.Amnezia{S1: 30, S2: 50}})
	if len(off) != 1 || len(on) != 1 {
		t.Fatalf("expected 1 sample each, got off=%d on=%d", len(off), len(on))
	}
	if off[0].pmtu != 1500 {
		t.Fatalf("obfuscation-off pmtu = %d, want 1500 (byte-identical)", off[0].pmtu)
	}
	if got := off[0].pmtu - on[0].pmtu; got != 50 {
		t.Fatalf("junk reserve = %d, want 50 = max(S1=30,S2=50)", got)
	}
}

// newTestResizer builds an mtuResizer over a manually-advanced fakeClock and a
// recording apply, so the recompute-and-decide logic is exercised with no netlink
// socket (the netlink apply itself is e2e-covered, T212). It returns the resizer, the
// clock, and a pointer to the slice of MTUs the apply recorded in order.
func newTestResizer(t *testing.T, bootMTU int, fecEnabled bool, dwell time.Duration) (*mtuResizer, *fakeClock, *[]int) {
	t.Helper()
	lg, err := log.New("error", io.Discard)
	if err != nil {
		t.Fatalf("log.New: %v", err)
	}
	clk := &fakeClock{now: time.Unix(1000, 0)}
	var applied []int
	r := newMTUResizer("wanbond0", bootMTU, fecEnabled, dwell, clk,
		func(mtu int) error { applied = append(applied, mtu); return nil },
		nil, lg)
	return r, clk, &applied
}

// TestMinInnerMTURecompute is the T209/D85 acceptance: the pure recompute-and-decide
// logic sizes wanbond0 to min(bind.InnerMTU(pmtu, fec)) across the UP paths and
// DEBOUNCES a loosening change (a constraining path leaving) by the failback-style
// dwell. Sequence, for FEC off and on: paths {UP:1500, UP:1400} hold the boot
// InnerMTU(1400); the 1400 path going DOWN raises the target to InnerMTU(1500) but it
// is not applied until the dwell elapses.
func TestMinInnerMTURecompute(t *testing.T) {
	for _, fec := range []bool{false, true} {
		fec := fec
		t.Run(map[bool]string{false: "fec_off", true: "fec_on"}[fec], func(t *testing.T) {
			want1400 := bind.InnerMTU(1400, fec)
			want1500 := bind.InnerMTU(1500, fec)
			const dwell = 5 * time.Second

			// Boot: both paths configured, so the applied MTU starts at InnerMTU(1400)
			// (the min across all paths, T205).
			r, clk, applied := newTestResizer(t, want1400, fec, dwell)

			// Both UP: min inner MTU == InnerMTU(1400) == the applied value -> no change.
			bothUp := []pathMTUSample{
				{state: telemetry.StateUp, pmtu: 1500},
				{state: telemetry.StateUp, pmtu: 1400},
			}
			r.recompute(bothUp)
			if len(*applied) != 0 {
				t.Fatalf("fec=%v: applied %v with both paths up, want no resize", fec, *applied)
			}
			if got := r.currentMTU(); got != want1400 {
				t.Fatalf("fec=%v: currentMTU=%d, want %d (min across both up paths)", fec, got, want1400)
			}

			// The 1400 path goes DOWN: only the 1500 path is UP, so the target rises to
			// InnerMTU(1500). A loosening change is debounced -> NOT applied yet.
			down := []pathMTUSample{
				{state: telemetry.StateUp, pmtu: 1500},
				{state: telemetry.StateDown, pmtu: 1400},
			}
			r.recompute(down)
			if len(*applied) != 0 {
				t.Fatalf("fec=%v: applied %v immediately on loosen, want debounced until the dwell", fec, *applied)
			}

			// A recompute part-way through the dwell still holds.
			clk.advance(dwell - time.Nanosecond)
			r.recompute(down)
			if len(*applied) != 0 {
				t.Fatalf("fec=%v: applied %v before the dwell elapsed, want none", fec, *applied)
			}

			// Once the dwell has elapsed, the loosening target is applied.
			clk.advance(time.Nanosecond)
			r.recompute(down)
			if len(*applied) != 1 || (*applied)[0] != want1500 {
				t.Fatalf("fec=%v: applied=%v after the dwell, want exactly [%d]", fec, *applied, want1500)
			}
			if got := r.currentMTU(); got != want1500 {
				t.Fatalf("fec=%v: currentMTU=%d after loosen, want %d", fec, got, want1500)
			}
		})
	}
}

// TestMTUResizeTightenIsImmediate confirms a TIGHTENING change (a smaller-PMTU path
// becoming the constraint) is applied at once, not debounced: running at too large an
// MTU risks IP fragmentation / PMTUD blackholes, so the conservative direction must
// not wait out the dwell.
func TestMTUResizeTightenIsImmediate(t *testing.T) {
	const dwell = 5 * time.Second
	want1500 := bind.InnerMTU(1500, false)
	want1400 := bind.InnerMTU(1400, false)

	// Boot with a single 1500 path up.
	r, _, applied := newTestResizer(t, want1500, false, dwell)
	r.recompute([]pathMTUSample{{state: telemetry.StateUp, pmtu: 1500}})
	if len(*applied) != 0 {
		t.Fatalf("applied %v at the boot MTU, want no resize", *applied)
	}

	// A smaller-PMTU path joins UP -> tighten immediately, no clock advance.
	r.recompute([]pathMTUSample{
		{state: telemetry.StateUp, pmtu: 1500},
		{state: telemetry.StateUp, pmtu: 1400},
	})
	if len(*applied) != 1 || (*applied)[0] != want1400 {
		t.Fatalf("applied=%v on tighten, want immediate [%d]", *applied, want1400)
	}
}

// TestMTUResizeNoUpPathKeepsMTU confirms a fully-down tunnel (no UP path) keeps its
// current link MTU rather than resizing to a degenerate value.
func TestMTUResizeNoUpPathKeepsMTU(t *testing.T) {
	want1400 := bind.InnerMTU(1400, false)
	r, clk, applied := newTestResizer(t, want1400, false, 5*time.Second)

	allDown := []pathMTUSample{
		{state: telemetry.StateDown, pmtu: 1500},
		{state: telemetry.StateDown, pmtu: 1400},
	}
	r.recompute(allDown)
	clk.advance(time.Hour)
	r.recompute(allDown)
	if len(*applied) != 0 {
		t.Fatalf("applied %v with no up path, want the current MTU kept", *applied)
	}
	if got := r.currentMTU(); got != want1400 {
		t.Fatalf("currentMTU=%d with no up path, want the boot value %d kept", got, want1400)
	}
}

// TestMTUResizeFlapDoesNotThrash confirms a path that leaves and returns WITHIN the
// dwell never applies a loosening resize: the pending loosen is cancelled when the
// target returns to the applied value, so a flapping path does not thrash the link.
func TestMTUResizeFlapDoesNotThrash(t *testing.T) {
	const dwell = 5 * time.Second
	want1400 := bind.InnerMTU(1400, false)
	r, clk, applied := newTestResizer(t, want1400, false, dwell)

	down := []pathMTUSample{
		{state: telemetry.StateUp, pmtu: 1500},
		{state: telemetry.StateDown, pmtu: 1400},
	}
	bothUp := []pathMTUSample{
		{state: telemetry.StateUp, pmtu: 1500},
		{state: telemetry.StateUp, pmtu: 1400},
	}

	// Loosen pending (1400 down), then the 1400 path recovers before the dwell elapses.
	r.recompute(down)
	clk.advance(dwell / 2)
	r.recompute(bothUp)
	// Even after more time than a full dwell, the earlier loosen must not fire.
	clk.advance(dwell)
	r.recompute(bothUp)
	if len(*applied) != 0 {
		t.Fatalf("applied %v across a sub-dwell flap, want no resize", *applied)
	}
}
