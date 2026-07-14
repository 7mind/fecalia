package sched

import (
	"io"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/log"
	"github.com/7mind/wanbond/internal/telemetry"
)

// fakeClock is a hand-advanced telemetry.Clock: transitions are deterministic
// and instant, with no real sleeps.
type fakeClock struct{ now time.Time }

func newFakeClock() *fakeClock { return &fakeClock{now: time.Unix(1_700_000_000, 0)} }

func (c *fakeClock) Now() time.Time          { return c.now }
func (c *fakeClock) advance(d time.Duration) { c.now = c.now.Add(d) }

// fakeHealth is a settable PathHealth for driving precise liveness traces.
type fakeHealth struct{ s telemetry.PathState }

func (h *fakeHealth) State() telemetry.PathState { return h.s }

func (h *fakeHealth) up()   { h.s = telemetry.StateUp }
func (h *fakeHealth) down() { h.s = telemetry.StateDown }

func discardLogger(t testing.TB) log.Logger {
	t.Helper()
	l, err := log.New("debug", io.Discard)
	if err != nil {
		t.Fatalf("build logger: %v", err)
	}
	return l
}

func newSched(t testing.TB, clock telemetry.Clock, failback time.Duration, h ...PathHealth) *ActiveBackup {
	t.Helper()
	s, err := NewActiveBackup(h, Config{FailbackAfter: failback}, clock, discardLogger(t))
	if err != nil {
		t.Fatalf("NewActiveBackup: %v", err)
	}
	return s
}

// TestActiveBackupAllTrafficOnPrimary is acceptance bullet 1: with two paths UP,
// EVERY Pick selects the active (preferred primary, index 0); the backup carries
// nothing.
func TestActiveBackupAllTrafficOnPrimary(t *testing.T) {
	clock := newFakeClock()
	primary := &fakeHealth{s: telemetry.StateUp}
	backup := &fakeHealth{s: telemetry.StateUp}
	s := newSched(t, clock, time.Second, primary, backup)

	for i := 0; i < 1000; i++ {
		if got := s.Pick(ClassData); got != 0 {
			t.Fatalf("Pick #%d = %d, want 0 (all traffic on the primary while both paths are up)", i, got)
		}
		clock.advance(time.Millisecond)
	}
}

// TestActiveBackupFailoverOnPrimaryDown is acceptance bullet 2: once the primary
// reports Down, egress switches to the backup at once (the scheduler reacts the
// moment liveness flips; the detection window itself is T13's DownAfter).
func TestActiveBackupFailoverOnPrimaryDown(t *testing.T) {
	clock := newFakeClock()
	primary := &fakeHealth{s: telemetry.StateUp}
	backup := &fakeHealth{s: telemetry.StateUp}
	s := newSched(t, clock, time.Second, primary, backup)

	if got := s.Pick(ClassData); got != 0 {
		t.Fatalf("initial Pick = %d, want 0", got)
	}
	primary.down()
	if got := s.Pick(ClassData); got != 1 {
		t.Fatalf("Pick after primary DOWN = %d, want 1 (failover to backup)", got)
	}
}

// TestActiveBackupFailoverWithinDetectionWindow drives REAL T13 liveness machines
// (not the fake health) so the failover is proven end-to-end through T13's
// up/down hysteresis: the backup fails over strictly after the primary's
// DownAfter detection window elapses, on the same injected fake clock (no real
// sleeps).
func TestActiveBackupFailoverWithinDetectionWindow(t *testing.T) {
	clock := newFakeClock()
	lg := discardLogger(t)
	const (
		downAfter = 500 * time.Millisecond
		upAfter   = 2
	)
	cfg := telemetry.LivenessConfig{DownAfter: downAfter, UpAfterSuccesses: upAfter}
	primary := telemetry.NewLiveness("primary", cfg, clock, lg)
	backup := telemetry.NewLiveness("backup", cfg, clock, lg)

	// Bring both paths UP: UpAfterSuccesses consecutive echoes each.
	for i := 0; i < upAfter; i++ {
		primary.RecordEcho()
		backup.RecordEcho()
		clock.advance(time.Millisecond)
	}
	if primary.State() != telemetry.StateUp || backup.State() != telemetry.StateUp {
		t.Fatalf("setup: primary=%v backup=%v, want both up", primary.State(), backup.State())
	}

	s := newSched(t, clock, time.Second, primary, backup)
	if got := s.Pick(ClassData); got != 0 {
		t.Fatalf("initial Pick = %d, want 0 (primary active)", got)
	}

	// Pin a known last-good instant for the primary, then let it fall silent. T13
	// declares a path down only once the silence STRICTLY exceeds DownAfter.
	primary.RecordEcho()

	// At exactly DownAfter of silence the primary is still up, so egress must stay
	// on it (no premature failover within the detection window).
	clock.advance(downAfter)
	backup.RecordEcho()
	primary.Tick()
	backup.Tick()
	if primary.State() != telemetry.StateUp {
		t.Fatalf("primary went down at exactly DownAfter, want still up")
	}
	if got := s.Pick(ClassData); got != 0 {
		t.Fatalf("Pick within detection window = %d, want 0 (no premature failover)", got)
	}

	// One tick past DownAfter with no primary echo: T13 declares the primary down,
	// and the scheduler must fail egress over to the backup.
	clock.advance(time.Nanosecond)
	backup.RecordEcho()
	primary.Tick()
	backup.Tick()
	if primary.State() != telemetry.StateDown {
		t.Fatalf("primary past DownAfter = %v, want down", primary.State())
	}
	if got := s.Pick(ClassData); got != 1 {
		t.Fatalf("Pick after detection window = %d, want 1 (failover to backup)", got)
	}
}

// TestActiveBackupNoThrashUnderFlapping is acceptance bullet 3: under a flapping
// primary the selection must NOT flip-flop. Once failed over to the backup, a
// primary that repeatedly recovers and dies within the failback dwell never
// steals egress back; egress returns to the primary only after it stays
// continuously up for FailbackAfter. All timing is on the injected fake clock.
func TestActiveBackupNoThrashUnderFlapping(t *testing.T) {
	clock := newFakeClock()
	const failback = 1 * time.Second
	primary := &fakeHealth{s: telemetry.StateUp}
	backup := &fakeHealth{s: telemetry.StateUp}
	s := newSched(t, clock, failback, primary, backup)

	// Start on the primary, then fail it -> egress moves to the backup.
	if got := s.Pick(ClassData); got != 0 {
		t.Fatalf("initial Pick = %d, want 0", got)
	}
	primary.down()
	if got := s.Pick(ClassData); got != 1 {
		t.Fatalf("Pick after primary down = %d, want 1", got)
	}

	// Flap the primary up/down in steps far shorter than the failback dwell,
	// sampling Pick throughout. Each dip must restart the dwell, so egress never
	// returns to the primary — no thrash.
	const step = 100 * time.Millisecond // 10 steps < failback (1s)
	for cycle := 0; cycle < 5; cycle++ {
		primary.up()
		clock.advance(step)
		if got := s.Pick(ClassData); got != 1 {
			t.Fatalf("cycle %d up-phase Pick = %d, want 1 (failback debounced, no thrash)", cycle, got)
		}
		primary.down()
		clock.advance(step)
		if got := s.Pick(ClassData); got != 1 {
			t.Fatalf("cycle %d down-phase Pick = %d, want 1", cycle, got)
		}
	}

	// Now the primary stabilises: continuously up. Just before the dwell elapses
	// egress is still on the backup; once the dwell passes it fails back.
	primary.up()
	if got := s.Pick(ClassData); got != 1 { // dwell just (re)started
		t.Fatalf("Pick at start of stable window = %d, want 1", got)
	}
	clock.advance(failback - time.Nanosecond)
	if got := s.Pick(ClassData); got != 1 {
		t.Fatalf("Pick just before dwell elapses = %d, want 1 (still holding backup)", got)
	}
	clock.advance(2 * time.Nanosecond)
	if got := s.Pick(ClassData); got != 0 {
		t.Fatalf("Pick after dwell elapses = %d, want 0 (failback to recovered primary)", got)
	}
}

// TestActiveBackupNoEligiblePath: with every path down, Pick reports no eligible
// path (negative) rather than silently returning a dead path.
func TestActiveBackupNoEligiblePath(t *testing.T) {
	clock := newFakeClock()
	primary := &fakeHealth{s: telemetry.StateDown}
	backup := &fakeHealth{s: telemetry.StateDown}
	s := newSched(t, clock, time.Second, primary, backup)
	if got := s.Pick(ClassData); got >= 0 {
		t.Fatalf("Pick with all paths down = %d, want negative (no eligible path)", got)
	}
}

// TestActiveBackupFailbackDeadActiveMovesImmediately: if the active backup dies
// mid-dwell while the primary is recovering, egress must move at once (to the
// eligible primary) rather than stall on the dead backup for the rest of the
// dwell.
func TestActiveBackupFailbackDeadActiveMovesImmediately(t *testing.T) {
	clock := newFakeClock()
	primary := &fakeHealth{s: telemetry.StateUp}
	backup := &fakeHealth{s: telemetry.StateUp}
	s := newSched(t, clock, time.Hour, primary, backup)

	// Fail over to the backup.
	primary.down()
	if got := s.Pick(ClassData); got != 1 {
		t.Fatalf("Pick after primary down = %d, want 1", got)
	}
	// Primary recovers (failback dwell begins, but it is an hour long)...
	primary.up()
	if got := s.Pick(ClassData); got != 1 {
		t.Fatalf("Pick mid-dwell = %d, want 1 (debounced)", got)
	}
	// ...and now the backup itself dies. The only eligible path is the primary, so
	// egress must move there immediately despite the unelapsed dwell.
	backup.down()
	if got := s.Pick(ClassData); got != 0 {
		t.Fatalf("Pick after backup died mid-dwell = %d, want 0 (immediate move to only eligible path)", got)
	}
}

// TestActiveBackupPerPathPacing is the defect-D65 reproduction: with pacing on and
// HETEROGENEOUS per-path capacities, the ACTIVE path's admits must be bounded by
// THAT path's OWN capacity (not the slowest link's), overflow returns PickPaced
// (distinct from PickNone), and a ClassControl frame is admitted even with an empty
// bucket. Before the per-path pacing impl every Pick admits (no shedding), so this
// FAILS for the right reason (paced==0); after, it PASSES.
func TestActiveBackupPerPathPacing(t *testing.T) {
	clock := newFakeClock()
	primary := &fakeHealth{s: telemetry.StateUp}
	backup := &fakeHealth{s: telemetry.StateUp}
	const (
		primaryCap = 1000.0 // fast active primary (frames/sec)
		backupCap  = 200.0  // slow backup (frames/sec)
		burst      = 8.0
	)
	cfg := Config{
		FailbackAfter:     time.Second,
		Pacing:            true,
		PerPathCapacities: []float64{primaryCap, backupCap},
		PacingBursts:      []float64{burst, burst},
	}
	s, err := NewActiveBackup([]PathHealth{primary, backup}, cfg, clock, discardLogger(t))
	if err != nil {
		t.Fatalf("NewActiveBackup: %v", err)
	}

	// Offer ~5000 ClassData frames over a 1s advancing-clock window (0.2ms/frame),
	// far above the primary's 1000 fps drain: pacing must shed the overflow.
	const (
		frames = 5000
		step   = 200 * time.Microsecond // frames*step = 1s
	)
	admitted, paced := 0, 0
	for i := 0; i < frames; i++ {
		switch got := s.Pick(ClassData); got {
		case 0:
			admitted++
		case PickPaced:
			paced++
		default:
			t.Fatalf("Pick #%d = %d, want 0 (primary) or PickPaced", i, got)
		}
		clock.advance(step)
	}

	// The primary's admits are bounded by ITS OWN capacity·T + burst. The window that
	// funded the Picks spans frames*step of refill (the initial full burst plus one
	// step's refill before each Pick), so cap·window + burst is a sound upper bound.
	window := (time.Duration(frames) * step).Seconds()
	upper := primaryCap*window + burst
	if float64(admitted) > upper {
		t.Fatalf("primary admitted %d, exceeds its OWN cap bound %.0f (cap·T+burst)", admitted, upper)
	}
	// D65 regression guard: a fast active primary must NOT be capped at the slow
	// backup's rate. With the weighted bottleneck-scalar sizing the primary would be
	// held to backupCap; per-path sizing must let it admit strictly MORE.
	backupBound := backupCap*window + burst
	if float64(admitted) <= backupBound {
		t.Fatalf("primary admitted %d, not above the backup-rate bound %.0f — a fast primary was capped at the slow backup's rate (D65 regression)", admitted, backupBound)
	}
	if paced == 0 {
		t.Fatalf("no frames were paced out over a ~5000-frame overload; pacing did not shed")
	}
	if PickPaced == PickNone {
		t.Fatalf("PickPaced (%d) must be distinct from PickNone (%d)", PickPaced, PickNone)
	}

	// ClassControl is pacing-EXEMPT (defect D22): drain the primary bucket to empty at
	// a single frozen instant (no clock advance ⇒ no refill), then a control frame
	// still egresses on the active path 0.
	s2, err := NewActiveBackup([]PathHealth{primary, backup}, cfg, clock, discardLogger(t))
	if err != nil {
		t.Fatalf("NewActiveBackup: %v", err)
	}
	// burst tokens, then the next ClassData Pick must be shed (bucket empty).
	for i := 0; i < int(burst); i++ {
		if got := s2.Pick(ClassData); got != 0 {
			t.Fatalf("drain Pick #%d = %d, want 0 (within burst)", i, got)
		}
	}
	if got := s2.Pick(ClassData); got != PickPaced {
		t.Fatalf("Pick past the burst at a frozen instant = %d, want PickPaced (empty bucket)", got)
	}
	if got := s2.Pick(ClassControl); got != 0 {
		t.Fatalf("ClassControl Pick with an empty bucket = %d, want 0 (control frames are pacing-exempt)", got)
	}
}

// TestActiveBackupPacingFailoverUsesOwnBucket proves per-path buckets: after failover
// to the backup, egress is paced by the BACKUP's own capacity, and the backup's
// bucket is full on failover (it was idle while the primary was active).
func TestActiveBackupPacingFailoverUsesOwnBucket(t *testing.T) {
	clock := newFakeClock()
	primary := &fakeHealth{s: telemetry.StateUp}
	backup := &fakeHealth{s: telemetry.StateUp}
	const burst = 4.0
	cfg := Config{
		FailbackAfter:     time.Hour, // no failback interference
		Pacing:            true,
		PerPathCapacities: []float64{1000.0, 200.0},
		PacingBursts:      []float64{burst, burst},
	}
	s, err := NewActiveBackup([]PathHealth{primary, backup}, cfg, clock, discardLogger(t))
	if err != nil {
		t.Fatalf("NewActiveBackup: %v", err)
	}
	// Warm the primary, then fail it: the backup becomes active with a FULL bucket.
	if got := s.Pick(ClassData); got != 0 {
		t.Fatalf("initial Pick = %d, want 0", got)
	}
	primary.down()
	// At a frozen instant the backup admits exactly its burst, then sheds.
	admitted := 0
	for i := 0; i < int(burst)+3; i++ {
		switch got := s.Pick(ClassData); got {
		case 1:
			admitted++
		case PickPaced:
		default:
			t.Fatalf("post-failover Pick #%d = %d, want 1 or PickPaced", i, got)
		}
	}
	if admitted != int(burst) {
		t.Fatalf("backup admitted %d at a frozen instant, want its full burst %d (own, full-on-failover bucket)", admitted, int(burst))
	}
}

// TestActiveBackupPacingSetPathsResizeNoPanic covers the T30 Close→Open durable-
// membership path: SetPaths swaps in a DIFFERENT path count and the next Pick must
// index the per-path bucket slice in range (no panic).
func TestActiveBackupPacingSetPathsResizeNoPanic(t *testing.T) {
	clock := newFakeClock()
	p0 := &fakeHealth{s: telemetry.StateUp}
	p1 := &fakeHealth{s: telemetry.StateUp}
	cfg := Config{
		FailbackAfter:     time.Second,
		Pacing:            true,
		PerPathCapacities: []float64{1000.0, 200.0},
		PacingBursts:      []float64{8.0, 8.0},
	}
	s, err := NewActiveBackup([]PathHealth{p0, p1}, cfg, clock, discardLogger(t))
	if err != nil {
		t.Fatalf("NewActiveBackup: %v", err)
	}
	if got := s.Pick(ClassData); got != 0 {
		t.Fatalf("initial Pick = %d, want 0", got)
	}
	// Grow to three paths (a wholesale health replacement) — the bucket slice must
	// resize so the next Pick does not index out of range.
	p2 := &fakeHealth{s: telemetry.StateUp}
	if err := s.SetPaths([]PathHealth{p0, p1, p2}); err != nil {
		t.Fatalf("SetPaths grow: %v", err)
	}
	if got := s.Pick(ClassData); got != 0 {
		t.Fatalf("Pick after SetPaths grow = %d, want 0", got)
	}
	// Shrink to a single path.
	if err := s.SetPaths([]PathHealth{p2}); err != nil {
		t.Fatalf("SetPaths shrink: %v", err)
	}
	if got := s.Pick(ClassData); got != 0 {
		t.Fatalf("Pick after SetPaths shrink = %d, want 0", got)
	}
	// AddPath then RemovePath must keep the bucket slice aligned too.
	if _, err := s.AddPath(p0); err != nil {
		t.Fatalf("AddPath: %v", err)
	}
	if err := s.RemovePath(0); err != nil {
		t.Fatalf("RemovePath: %v", err)
	}
	if got := s.Pick(ClassData); got < 0 && got != PickPaced {
		t.Fatalf("Pick after AddPath/RemovePath = %d, want a valid index or PickPaced", got)
	}
}

// TestActiveBackupPacingValidation covers the pacing fail-fast guards in
// NewActiveBackup: length mismatch and non-positive per-path capacity/burst.
func TestActiveBackupPacingValidation(t *testing.T) {
	clock := newFakeClock()
	lg := discardLogger(t)
	h := []PathHealth{AlwaysUp{}, AlwaysUp{}}
	bad := []Config{
		{Pacing: true, PerPathCapacities: []float64{1000}, PacingBursts: []float64{8, 8}},       // capacities len mismatch
		{Pacing: true, PerPathCapacities: []float64{1000, 200}, PacingBursts: []float64{8}},     // bursts len mismatch
		{Pacing: true, PerPathCapacities: []float64{1000, 0}, PacingBursts: []float64{8, 8}},    // zero capacity
		{Pacing: true, PerPathCapacities: []float64{1000, 200}, PacingBursts: []float64{8, -1}}, // negative burst
		{Pacing: true, PerPathCapacities: nil, PacingBursts: nil},                               // missing slices
	}
	for i, c := range bad {
		if _, err := NewActiveBackup(h, c, clock, lg); err == nil {
			t.Fatalf("bad pacing config #%d succeeded, want error", i)
		}
	}
	// A well-formed pacing config constructs.
	if _, err := NewActiveBackup(h, Config{Pacing: true, PerPathCapacities: []float64{1000, 200}, PacingBursts: []float64{8, 8}}, clock, lg); err != nil {
		t.Fatalf("well-formed pacing config: %v", err)
	}
}

// TestActiveBackupConstructorValidation covers the fail-fast guards.
func TestActiveBackupConstructorValidation(t *testing.T) {
	clock := newFakeClock()
	lg := discardLogger(t)
	if _, err := NewActiveBackup(nil, Config{}, clock, lg); err == nil {
		t.Fatal("NewActiveBackup with no health sources succeeded, want error")
	}
	if _, err := NewActiveBackup([]PathHealth{nil}, Config{}, clock, lg); err == nil {
		t.Fatal("NewActiveBackup with a nil health source succeeded, want error")
	}
	if _, err := NewActiveBackup([]PathHealth{AlwaysUp{}}, Config{}, nil, lg); err == nil {
		t.Fatal("NewActiveBackup with nil clock succeeded, want error")
	}
	if _, err := NewActiveBackup([]PathHealth{AlwaysUp{}}, Config{}, clock, nil); err == nil {
		t.Fatal("NewActiveBackup with nil logger succeeded, want error")
	}
}
