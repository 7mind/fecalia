package sched

import (
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/telemetry"
)

// compile-time proof ActiveBackup carries pacing headroom for out-of-band probe egress
// (ProbeBudget, T145/D76) — the same seam *WeightedScheduler proves.
var _ ProbeBudget = (*ActiveBackup)(nil)

// newPacingAB builds a single-path pacing-enabled active-backup scheduler seeded full, for the
// D76 AccountProbe unit contracts (mirroring newWeighted in weighted_test.go). The path is
// StateUp so Pick selects it (index 0), and its single (n==1) token bucket is s.pacers[0].
func newPacingAB(t testing.TB, clock telemetry.Clock, capFPS, burst float64) *ActiveBackup {
	t.Helper()
	h := []PathHealth{&fakeHealth{s: telemetry.StateUp}}
	s, err := NewActiveBackup(h, Config{
		FailbackAfter:     time.Second,
		Pacing:            true,
		PerPathCapacities: []float64{capFPS},
		PacingBursts:      []float64{burst},
	}, clock, discardLogger(t))
	if err != nil {
		t.Fatalf("NewActiveBackup(pacing): %v", err)
	}
	return s
}

// TestActiveBackupAccountProbeDeductsOneTokenWithoutShedding mirrors the T145
// *WeightedScheduler contract for D76: AccountProbe deducts EXACTLY one token per call from the
// named path's bucket, never sheds or delays (the probe is already on the wire), and the bucket
// MAY go NEGATIVE (strict priority: the probe egress is charged even past the burst,
// pre-draining the bucket).
func TestActiveBackupAccountProbeDeductsOneTokenWithoutShedding(t *testing.T) {
	clock := newFakeClock()
	const burst = 4.0
	s := newPacingAB(t, clock, 1000.0, burst)

	// Fresh bucket is seeded full (burst).
	if got := s.pacers[0].tokens[0]; got != burst {
		t.Fatalf("fresh bucket = %g, want burst %g", got, burst)
	}

	// Charge past the burst so the bucket goes NEGATIVE: strict priority means the probe is
	// always emitted, so its token is always spent even when no headroom remains.
	const probes = 6 // > burst (4)
	for i := 0; i < probes; i++ {
		s.AccountProbe(0)
	}
	want := burst - float64(probes) // 4 - 6 = -2
	if got := s.pacers[0].tokens[0]; got != want {
		t.Fatalf("after %d probes bucket = %g, want %g (exactly one token per probe, may go negative)", probes, got, want)
	}
	if want >= 0 {
		t.Fatal("test misconfigured: expected the charge to drive the bucket negative (strict priority)")
	}
}

// TestActiveBackupAccountProbeReservesClassDataHeadroom is the behavioural D76 assertion: a
// probe charged via AccountProbe removes EXACTLY one ClassData admission slot on the active
// path, so the out-of-band probe stream reserves data headroom rather than being invisible to
// the pacer. Measured by counting how many ClassData frames the active path admits before
// shedding, with and without a fixed probe charge, at a pinned clock (dt=0 so refill is inert
// and the burst is the only admission budget) — the difference must equal the charge.
func TestActiveBackupAccountProbeReservesClassDataHeadroom(t *testing.T) {
	admitsAfterCharge := func(charge int) int {
		clock := newFakeClock()
		const burst = 8.0
		s := newPacingAB(t, clock, 1000.0, burst)

		// One Pick seeds the bucket full (haveFill) and consumes one token — identical in both
		// arms, so it cancels out of the difference. The clock is never advanced, so every later
		// refill adds 0 and the remaining burst is the whole admission budget.
		s.Pick(ClassData)
		for i := 0; i < charge; i++ {
			s.AccountProbe(0)
		}
		admits := 0
		for i := 0; i < 100; i++ {
			if s.Pick(ClassData) >= 0 {
				admits++
			}
		}
		return admits
	}

	const charge = 3
	base := admitsAfterCharge(0)
	charged := admitsAfterCharge(charge)
	if base <= charge {
		t.Fatalf("test misconfigured: baseline admitted only %d ClassData frames, need > charge=%d for a non-vacuous difference", base, charge)
	}
	if base-charged != charge {
		t.Fatalf("charging %d probes freed %d ClassData admission slots, want exactly %d (each probe reserves one DATA token, D76): base=%d charged=%d",
			charge, base-charged, charge, base, charged)
	}
}

// TestActiveBackupAccountProbeNoopWhenInertOrOutOfRange guards the two no-op paths: with pacing
// OFF the scheduler has no per-path buckets (AccountProbe must be a silent no-op), and an
// out-of-range index (a stale index from a concurrent membership change — probe accounting is
// best-effort headroom) must be silently ignored rather than panic.
func TestActiveBackupAccountProbeNoopWhenInertOrOutOfRange(t *testing.T) {
	clock := newFakeClock()

	// Pacing off: no buckets at all, AccountProbe is a no-op (must not panic).
	off := newSched(t, clock, time.Second, &fakeHealth{s: telemetry.StateUp})
	if len(off.pacers) != 0 {
		t.Fatalf("pacing-off scheduler has %d pacers, want 0", len(off.pacers))
	}
	off.AccountProbe(0) // must not panic on the empty pacer slice

	// Pacing on: an out-of-range index must not panic and must not touch the in-range bucket.
	on := newPacingAB(t, clock, 1000.0, 4.0)
	snap := on.pacers[0].tokens[0]
	on.AccountProbe(-1)
	on.AccountProbe(1)  // len(pacers) == 1, so index 1 is out of range
	on.AccountProbe(99) // far out of range
	if on.pacers[0].tokens[0] != snap {
		t.Fatalf("an out-of-range AccountProbe mutated the in-range bucket: %g -> %g", snap, on.pacers[0].tokens[0])
	}
}
