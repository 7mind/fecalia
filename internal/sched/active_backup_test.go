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

// admitH builds a pacing-OFF PathAdmission (no per-path pacing) for the DynamicScheduler
// membership calls — the concise form for the failover/liveness tests that never enable pacing.
func admitH(h PathHealth) PathAdmission { return PathAdmission{Health: h} }

// admit builds a PathAdmission carrying an explicit identity-sourced per-path pace (defect D79),
// for the pacing-enabled membership tests.
func admit(h PathHealth, capFPS, burst float64) PathAdmission {
	return PathAdmission{Health: h, Pacing: PathPacing{CapacityFPS: capFPS, BurstFrames: burst}}
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
	if err := s.SetPaths([]PathAdmission{admit(p0, 1000, 8), admit(p1, 200, 8), admit(p2, 500, 8)}); err != nil {
		t.Fatalf("SetPaths grow: %v", err)
	}
	if got := s.Pick(ClassData); got != 0 {
		t.Fatalf("Pick after SetPaths grow = %d, want 0", got)
	}
	// Shrink to a single path.
	if err := s.SetPaths([]PathAdmission{admit(p2, 500, 8)}); err != nil {
		t.Fatalf("SetPaths shrink: %v", err)
	}
	if got := s.Pick(ClassData); got != 0 {
		t.Fatalf("Pick after SetPaths shrink = %d, want 0", got)
	}
	// AddPath then RemovePath must keep the bucket slice aligned too.
	if _, err := s.AddPath(admit(p0, 1000, 8)); err != nil {
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

// TestActiveBackupPacingRemoveToEmptyThenAddPath is a regression test for the
// index-out-of-range panic (criticism 1a, R181): RemovePath is a legal way to drop
// the DynamicScheduler's path set to EMPTY (the contract only requires i-in-range),
// and a subsequent AddPath must re-grow the bucket slice WITHOUT reading
// s.pacers[len-1] off an empty slice. Before the fix this panicked
// "index out of range [-1]" in AddPath; after it, the re-grown path's bucket is
// seeded full from the immutable config tail.
func TestActiveBackupPacingRemoveToEmptyThenAddPath(t *testing.T) {
	clock := newFakeClock()
	only := &fakeHealth{s: telemetry.StateUp}
	const (
		capFPS = 700.0
		burst  = 6.0
	)
	cfg := Config{
		FailbackAfter:     time.Second,
		Pacing:            true,
		PerPathCapacities: []float64{capFPS},
		PacingBursts:      []float64{burst},
	}
	s, err := NewActiveBackup([]PathHealth{only}, cfg, clock, discardLogger(t))
	if err != nil {
		t.Fatalf("NewActiveBackup: %v", err)
	}
	// Drop the set to empty — a legal state; Pick then reports PickNone.
	if err := s.RemovePath(0); err != nil {
		t.Fatalf("RemovePath to empty: %v", err)
	}
	if got := s.Pick(ClassData); got != PickNone {
		t.Fatalf("Pick on the empty set = %d, want PickNone", got)
	}
	// Re-grow. Before the D65 fix this panicked at s.pacers[len(s.pacers)-1] on an empty slice;
	// AddPath now seeds the new bucket from the admission's OWN identity-sourced pace (D79) — no
	// read of the (empty) live slice at all — so the empty-slice hazard is gone by construction.
	regrown := &fakeHealth{s: telemetry.StateUp}
	idx, err := s.AddPath(admit(regrown, capFPS, burst))
	if err != nil {
		t.Fatalf("AddPath after remove-to-empty: %v", err)
	}
	if idx != 0 {
		t.Fatalf("AddPath returned index %d, want 0 (the re-grown sole path)", idx)
	}
	// The re-grown bucket carries the SUPPLIED per-path capacity/burst.
	if got := s.pacers[0].cfg.CapacityFPS; got != capFPS {
		t.Fatalf("re-grown bucket CapacityFPS = %g, want the supplied %g", got, capFPS)
	}
	if got := s.pacers[0].cfg.BurstFrames; got != burst {
		t.Fatalf("re-grown bucket BurstFrames = %g, want the supplied %g", got, burst)
	}
	// A fresh full bucket admits its burst at a frozen instant, then sheds.
	admitted := 0
	for i := 0; i < int(burst)+3; i++ {
		switch got := s.Pick(ClassData); got {
		case 0:
			admitted++
		case PickPaced:
		default:
			t.Fatalf("post-regrow Pick #%d = %d, want 0 or PickPaced", i, got)
		}
	}
	if admitted != int(burst) {
		t.Fatalf("re-grown path admitted %d at a frozen instant, want its full burst %d", admitted, int(burst))
	}
}

// TestActiveBackupPacingRemoveToEmptyThenSetPaths is a regression test for the
// index-out-of-range panic (criticism 1b, R181): after RemovePath empties the path
// set, SetPaths must rebuild the bucket slice WITHOUT reading old[len-1] off an
// empty slice. Before the fix this panicked "index out of range [-1]" in
// resizeActiveBackupPacers; after it, every rebuilt bucket is seeded full from the
// immutable config tail.
func TestActiveBackupPacingRemoveToEmptyThenSetPaths(t *testing.T) {
	clock := newFakeClock()
	only := &fakeHealth{s: telemetry.StateUp}
	const (
		capFPS = 900.0
		burst  = 5.0
	)
	cfg := Config{
		FailbackAfter:     time.Second,
		Pacing:            true,
		PerPathCapacities: []float64{capFPS},
		PacingBursts:      []float64{burst},
	}
	s, err := NewActiveBackup([]PathHealth{only}, cfg, clock, discardLogger(t))
	if err != nil {
		t.Fatalf("NewActiveBackup: %v", err)
	}
	if err := s.RemovePath(0); err != nil {
		t.Fatalf("RemovePath to empty: %v", err)
	}
	// Rebuild to two paths from the empty state. Before the D65 fix this panicked at
	// old[len(old)-1] on an empty slice; SetPaths now seeds every bucket from its admission's OWN
	// identity-sourced pace (D79), reading nothing off the (empty) live slice, so the hazard is
	// gone by construction.
	a := &fakeHealth{s: telemetry.StateUp}
	b := &fakeHealth{s: telemetry.StateUp}
	if err := s.SetPaths([]PathAdmission{admit(a, capFPS, burst), admit(b, capFPS, burst)}); err != nil {
		t.Fatalf("SetPaths after remove-to-empty: %v", err)
	}
	if len(s.pacers) != 2 {
		t.Fatalf("bucket slice len = %d after SetPaths, want 2", len(s.pacers))
	}
	// Both rebuilt buckets carry the SUPPLIED per-path capacity/burst.
	for i := range s.pacers {
		if got := s.pacers[i].cfg.CapacityFPS; got != capFPS {
			t.Fatalf("rebuilt bucket[%d] CapacityFPS = %g, want the supplied %g", i, got, capFPS)
		}
		if got := s.pacers[i].cfg.BurstFrames; got != burst {
			t.Fatalf("rebuilt bucket[%d] BurstFrames = %g, want the supplied %g", i, got, burst)
		}
	}
	// Active path 0 admits its full burst at a frozen instant, then sheds.
	admitted := 0
	for i := 0; i < int(burst)+3; i++ {
		switch got := s.Pick(ClassData); got {
		case 0:
			admitted++
		case PickPaced:
		default:
			t.Fatalf("post-SetPaths Pick #%d = %d, want 0 or PickPaced", i, got)
		}
	}
	if admitted != int(burst) {
		t.Fatalf("rebuilt active path admitted %d at a frozen instant, want its full burst %d", admitted, int(burst))
	}
}

// TestActiveBackupPacingDisabledIsNoOp is T151 scenario (a): with Pacing left off
// (the zero value), Pick is byte-for-byte its pre-pacing self — EVERY ClassData
// frame is admitted on the active path, even under an offered load far above any
// per-path capacity a pacing config would impose. This is the regression guard
// that toggling pacing off truly disables shedding rather than merely raising the
// bound.
func TestActiveBackupPacingDisabledIsNoOp(t *testing.T) {
	clock := newFakeClock()
	primary := &fakeHealth{s: telemetry.StateUp}
	backup := &fakeHealth{s: telemetry.StateUp}
	// Config{Pacing: false} — the zero value; PerPathCapacities/PacingBursts unset.
	s := newSched(t, clock, time.Second, primary, backup)

	// Offer at the same overload cadence TestActiveBackupPerPathPacing uses (which,
	// with pacing ON, sheds thousands of frames): 5000 frames at 0.2ms/frame.
	const (
		frames = 5000
		step   = 200 * time.Microsecond
	)
	admitted := 0
	for i := 0; i < frames; i++ {
		got := s.Pick(ClassData)
		if got != 0 {
			t.Fatalf("Pick #%d = %d, want 0 (pacing disabled: every frame admits on the active path)", i, got)
		}
		admitted++
		clock.advance(step)
	}
	if admitted != frames {
		t.Fatalf("admitted = %d, want all %d frames (pacing disabled is a pure no-op, nothing is ever shed)", admitted, frames)
	}
}

// TestActiveBackupPacingFailoverSaturatedPrimaryDoesNotStarveBackup is T151
// scenario (b): the active path changes on failover, and pacing then draws from
// the NEW active path's OWN bucket at that path's OWN rate. A primary saturated
// (fully drained, shedding) right up to the failover must NOT starve the backup
// — the backup's bucket is independent and full — and a backup far FASTER than
// the old primary must not be throttled down to the old primary's rate.
func TestActiveBackupPacingFailoverSaturatedPrimaryDoesNotStarveBackup(t *testing.T) {
	clock := newFakeClock()
	primary := &fakeHealth{s: telemetry.StateUp}
	backup := &fakeHealth{s: telemetry.StateUp}
	const (
		primaryCap   = 50.0 // slow primary
		primaryBurst = 5.0
		backupCap    = 2000.0 // fast backup
		backupBurst  = 20.0
	)
	cfg := Config{
		FailbackAfter:     time.Hour, // no failback interference
		Pacing:            true,
		PerPathCapacities: []float64{primaryCap, backupCap},
		PacingBursts:      []float64{primaryBurst, backupBurst},
	}
	s, err := NewActiveBackup([]PathHealth{primary, backup}, cfg, clock, discardLogger(t))
	if err != nil {
		t.Fatalf("NewActiveBackup: %v", err)
	}

	// Saturate the primary at a single frozen instant (no clock advance ⇒ no
	// refill): exactly its burst admits, then every further frame sheds.
	primaryAdmitted := 0
	for i := 0; i < int(primaryBurst)+10; i++ {
		switch got := s.Pick(ClassData); got {
		case 0:
			primaryAdmitted++
		case PickPaced:
		default:
			t.Fatalf("primary saturation Pick #%d = %d, want 0 or PickPaced", i, got)
		}
	}
	if primaryAdmitted != int(primaryBurst) {
		t.Fatalf("primary admitted %d while saturating, want exactly its burst %d", primaryAdmitted, int(primaryBurst))
	}

	// Fail over to the backup (still the same frozen instant).
	primary.down()
	if got := s.Pick(ClassData); got != 1 {
		t.Fatalf("first Pick after failover = %d, want 1 (backup active)", got)
	}
	backupAdmitted := 1
	for i := 0; i < int(backupBurst)+10; i++ {
		switch got := s.Pick(ClassData); got {
		case 1:
			backupAdmitted++
		case PickPaced:
		default:
			t.Fatalf("post-failover Pick #%d = %d, want 1 or PickPaced", i, got)
		}
	}
	// The backup's bucket is its OWN — full on failover regardless of the primary
	// having just been drained to empty — so it admits its OWN (larger) burst, not
	// the primary's exhausted state and not the primary's smaller burst.
	if backupAdmitted != int(backupBurst) {
		t.Fatalf("backup admitted %d at a frozen instant right after failover, want its OWN full burst %d (not starved by the saturated primary)", backupAdmitted, int(backupBurst))
	}

	// Over an advancing window the backup's sustained admit rate is bounded by ITS
	// OWN capacity and — the D65 regression this guards — strictly exceeds what the
	// old (slower) primary's rate would have allowed, proving it is not throttled
	// down to the old active path's rate.
	const (
		windowFrames = 2000
		step         = 200 * time.Microsecond // windowFrames*step = 400ms
	)
	windowAdmitted := 0
	for i := 0; i < windowFrames; i++ {
		if got := s.Pick(ClassData); got == 1 {
			windowAdmitted++
		}
		clock.advance(step)
	}
	window := (time.Duration(windowFrames) * step).Seconds()
	backupUpper := backupCap*window + backupBurst
	primaryUpper := primaryCap*window + primaryBurst
	if float64(windowAdmitted) > backupUpper {
		t.Fatalf("backup admitted %d over the window, exceeds its OWN cap bound %.1f", windowAdmitted, backupUpper)
	}
	if float64(windowAdmitted) <= primaryUpper {
		t.Fatalf("backup admitted %d over the window, not above the old primary's rate bound %.1f — the backup was throttled to the old active path's rate (D65 regression)", windowAdmitted, primaryUpper)
	}
}

// TestActiveBackupPacingSentinelDistinctness is T151 scenario (c): PickPaced
// (healthy path, momentarily out of tokens) and PickNone (no eligible path at
// all) are distinct sentinels returned in the correct, distinct situations —
// never conflated.
func TestActiveBackupPacingSentinelDistinctness(t *testing.T) {
	clock := newFakeClock()
	primary := &fakeHealth{s: telemetry.StateUp}
	backup := &fakeHealth{s: telemetry.StateUp}
	const burst = 4.0
	cfg := Config{
		FailbackAfter:     time.Second,
		Pacing:            true,
		PerPathCapacities: []float64{1000.0, 200.0},
		PacingBursts:      []float64{burst, burst},
	}
	s, err := NewActiveBackup([]PathHealth{primary, backup}, cfg, clock, discardLogger(t))
	if err != nil {
		t.Fatalf("NewActiveBackup: %v", err)
	}

	// Drain the (healthy) active path's bucket at a frozen instant, then the next
	// frame must be PickPaced — the path is up, just momentarily out of tokens.
	for i := 0; i < int(burst); i++ {
		if got := s.Pick(ClassData); got != 0 {
			t.Fatalf("drain Pick #%d = %d, want 0 (within burst)", i, got)
		}
	}
	got := s.Pick(ClassData)
	if got != PickPaced {
		t.Fatalf("Pick with an empty bucket on a healthy path = %d, want PickPaced", got)
	}
	if got == PickNone {
		t.Fatalf("a healthy-but-paced path must not report PickNone")
	}

	// Now take EVERY path down. Regardless of token state, this must report
	// PickNone (no eligible path) — never PickPaced, which would misreport a total
	// outage as a mere rate-limit.
	primary.down()
	backup.down()
	got = s.Pick(ClassData)
	if got != PickNone {
		t.Fatalf("Pick with all paths down = %d, want PickNone", got)
	}
	if got == PickPaced {
		t.Fatalf("a total outage must not report PickPaced")
	}
	if PickPaced == PickNone {
		t.Fatalf("PickPaced (%d) and PickNone (%d) must be distinct sentinels", PickPaced, PickNone)
	}
}

// TestActiveBackupPacingClassControlExemptionColdStartAndSustainedShedding is
// T151 scenario (d): ClassControl's pacing exemption (defect D22) holds BOTH at
// cold start (before the bucket has ever been touched) AND while ClassData is
// under sustained shedding — in neither case does a control Pick consume a
// token or otherwise perturb the bucket state.
func TestActiveBackupPacingClassControlExemptionColdStartAndSustainedShedding(t *testing.T) {
	clock := newFakeClock()
	primary := &fakeHealth{s: telemetry.StateUp}
	backup := &fakeHealth{s: telemetry.StateUp}
	const (
		capFPS = 100.0
		burst  = 5.0
	)
	cfg := Config{
		FailbackAfter:     time.Second,
		Pacing:            true,
		PerPathCapacities: []float64{capFPS, capFPS},
		PacingBursts:      []float64{burst, burst},
	}
	s, err := NewActiveBackup([]PathHealth{primary, backup}, cfg, clock, discardLogger(t))
	if err != nil {
		t.Fatalf("NewActiveBackup: %v", err)
	}

	// Cold start: the VERY FIRST Pick is a control frame, before any ClassData Pick
	// has ever run. It must admit on the active path, and — because the pacing
	// branch for ClassControl returns before refill/consume — the bucket must be
	// left untouched (still un-seeded).
	if got := s.Pick(ClassControl); got != 0 {
		t.Fatalf("cold-start ClassControl Pick = %d, want 0 (active, exempt)", got)
	}
	if s.pacers[0].haveFill {
		t.Fatalf("cold-start ClassControl Pick touched the bucket (haveFill=true), want it untouched")
	}
	// Confirm the bucket really was untouched: the first ClassData Pick still gets
	// a freshly-seeded FULL bucket (burst admits before the first shed).
	admitted := 0
	for i := 0; i < int(burst)+3; i++ {
		switch got := s.Pick(ClassData); got {
		case 0:
			admitted++
		case PickPaced:
		default:
			t.Fatalf("post-cold-start Pick #%d = %d, want 0 or PickPaced", i, got)
		}
	}
	if admitted != int(burst) {
		t.Fatalf("admitted %d after the cold-start control Pick, want the full burst %d (control Pick spent no token)", admitted, int(burst))
	}

	// Sustained shedding: the active bucket is now drained (from the loop above).
	// Keep offering ClassData at the same frozen instant to confirm sustained
	// shedding, then interleave ClassControl Picks and prove they neither admit
	// via the data path's accounting nor refill/consume any token.
	for i := 0; i < 20; i++ {
		if got := s.Pick(ClassData); got != PickPaced {
			t.Fatalf("sustained-shedding Pick #%d = %d, want PickPaced (bucket drained)", i, got)
		}
	}
	tokensBefore := s.pacers[0].tokens[0]
	for i := 0; i < 5; i++ {
		if got := s.Pick(ClassControl); got != 0 {
			t.Fatalf("ClassControl during sustained shedding, call #%d = %d, want 0 (still exempt)", i, got)
		}
	}
	if got := s.pacers[0].tokens[0]; got != tokensBefore {
		t.Fatalf("bucket tokens changed from %g to %g across 5 ClassControl Picks during sustained shedding, want unchanged (exempt)", tokensBefore, got)
	}
	// ClassData immediately after is still shed — the control Picks did not refill
	// or otherwise grant it any token.
	if got := s.Pick(ClassData); got != PickPaced {
		t.Fatalf("ClassData Pick right after the ClassControl run = %d, want still PickPaced", got)
	}
}

// TestActiveBackupPacingBurstAbsorptionAfterIdle is T151 scenario (e): a burst of
// frames no larger than PacingBurst, offered after an idle span long enough to
// fully refill, is admitted WITHOUT shedding — and the refill is capped exactly
// at the burst (idle does not accumulate unbounded credit).
func TestActiveBackupPacingBurstAbsorptionAfterIdle(t *testing.T) {
	clock := newFakeClock()
	primary := &fakeHealth{s: telemetry.StateUp}
	backup := &fakeHealth{s: telemetry.StateUp}
	const (
		capFPS = 100.0
		burst  = 10.0
	)
	cfg := Config{
		FailbackAfter:     time.Second,
		Pacing:            true,
		PerPathCapacities: []float64{capFPS, capFPS},
		PacingBursts:      []float64{burst, burst},
	}
	s, err := NewActiveBackup([]PathHealth{primary, backup}, cfg, clock, discardLogger(t))
	if err != nil {
		t.Fatalf("NewActiveBackup: %v", err)
	}

	// Drain the bucket to empty first, so the subsequent refill is observably
	// earned from the idle span, not left over from the initial full seed.
	for i := 0; i < int(burst); i++ {
		if got := s.Pick(ClassData); got != 0 {
			t.Fatalf("drain Pick #%d = %d, want 0", i, got)
		}
	}
	if got := s.Pick(ClassData); got != PickPaced {
		t.Fatalf("Pick past the burst = %d, want PickPaced (bucket drained)", got)
	}

	// Idle for well over burst/capacity seconds (100ms here; 1s is ample margin),
	// which refills the bucket, capped at burst.
	clock.advance(1 * time.Second)

	// A burst of exactly `burst` frames, offered back-to-back at this now-refilled
	// instant, must ALL admit — no shedding.
	admitted, shed := 0, 0
	for i := 0; i < int(burst); i++ {
		switch got := s.Pick(ClassData); got {
		case 0:
			admitted++
		case PickPaced:
			shed++
		default:
			t.Fatalf("post-idle burst Pick #%d = %d, want 0 or PickPaced", i, got)
		}
	}
	if admitted != int(burst) {
		t.Fatalf("post-idle burst admitted %d, want all %d (burst <= PacingBurst after idle must not shed)", admitted, int(burst))
	}
	if shed != 0 {
		t.Fatalf("post-idle burst shed %d frames, want 0", shed)
	}
	// The refill is capped at burst, not unbounded: the NEXT frame (beyond burst)
	// at the same frozen instant must shed.
	if got := s.Pick(ClassData); got != PickPaced {
		t.Fatalf("Pick past the post-idle burst = %d, want PickPaced (refill capped at burst, no unbounded idle credit)", got)
	}
}

// TestActiveBackupPacingSetPathsMembershipChangePacesNewMembership is T151
// scenario (f) — the T30 pacer regression (R162 criticism 3): a SetPaths that
// CHANGES the path count rebuilds the per-path bucket slice so the next Pick
// indexes in range (no panic) AND paces correctly against the NEW membership —
// not merely "doesn't crash", but bounded by each admission's OWN identity-sourced
// capacity/burst (defect D79: a grown-in path paces at ITS supplied rate, never a
// positional/tail carry).
func TestActiveBackupPacingSetPathsMembershipChangePacesNewMembership(t *testing.T) {
	clock := newFakeClock()
	p0 := &fakeHealth{s: telemetry.StateUp}
	p1 := &fakeHealth{s: telemetry.StateUp}
	const (
		p2Cap   = 500.0 // the grown-in path's OWN identity-sourced pace
		p2Burst = 6.0
	)
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
	if got := len(s.pacers); got != 2 {
		t.Fatalf("bucket slice len = %d before resize, want 2 == len(health)", got)
	}

	// Grow to three paths: a Close->Open-style wholesale membership replacement. The grown-in
	// tail p2 is admitted with its OWN distinct pace (p2Cap/p2Burst), NOT the config tail.
	p2 := &fakeHealth{s: telemetry.StateUp}
	if err := s.SetPaths([]PathAdmission{admit(p0, 1000, 8), admit(p1, 200, 8), admit(p2, p2Cap, p2Burst)}); err != nil {
		t.Fatalf("SetPaths grow: %v", err)
	}
	if got := len(s.pacers); got != 3 {
		t.Fatalf("bucket slice len = %d after grow, want 3 == len(health)", got)
	}
	// Fail every path but the new (grown-in) tail, so it becomes active and its
	// bucket — rebuilt from its OWN admission — is the one exercised.
	p0.down()
	p1.down()
	if got := s.Pick(ClassData); got != 2 {
		t.Fatalf("Pick with only the grown-in path up = %d, want 2", got)
	}
	// The grown-in path paces at ITS supplied burst (p2Burst) — verify it actually paces at
	// that rate: exactly p2Burst admits at a frozen instant, then a shed. The Pick just above
	// already consumed the bucket's first token.
	admitted := 1
	for i := 0; i < int(p2Burst)+5; i++ {
		switch got := s.Pick(ClassData); got {
		case 2:
			admitted++
		case PickPaced:
		default:
			t.Fatalf("grown-in-path Pick #%d = %d, want 2 or PickPaced", i, got)
		}
	}
	if admitted != int(p2Burst) {
		t.Fatalf("grown-in path admitted %d at a frozen instant, want its OWN supplied burst %d (identity re-pace failed)", admitted, int(p2Burst))
	}

	// Shrink to a single (surviving) path — the bucket slice must resize down too,
	// and the sole survivor must still pace correctly at its OWN supplied rate (a fresh, full
	// bucket after the wholesale SetPaths rebuild).
	if err := s.SetPaths([]PathAdmission{admit(p2, p2Cap, p2Burst)}); err != nil {
		t.Fatalf("SetPaths shrink: %v", err)
	}
	if got := len(s.pacers); got != 1 {
		t.Fatalf("bucket slice len = %d after shrink, want 1 == len(health)", got)
	}
	if got := s.Pick(ClassData); got != 0 {
		t.Fatalf("Pick after shrink = %d, want 0 (sole survivor)", got)
	}
	admitted = 1 // the Pick just above already consumed one token
	for i := 0; i < int(p2Burst)+5; i++ {
		switch got := s.Pick(ClassData); got {
		case 0:
			admitted++
		case PickPaced:
		default:
			t.Fatalf("post-shrink Pick #%d = %d, want 0 or PickPaced", i, got)
		}
	}
	if admitted != int(p2Burst) {
		t.Fatalf("post-shrink sole survivor admitted %d at a frozen instant, want its OWN supplied burst %d", admitted, int(p2Burst))
	}
}
