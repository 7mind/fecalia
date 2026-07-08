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
