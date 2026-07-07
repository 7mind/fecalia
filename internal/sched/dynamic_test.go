package sched

import (
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/telemetry"
)

// TestActiveBackupAddPathLowestPriority is the T30 admission invariant: a path
// added at runtime joins as the lowest-priority backup and does NOT steal egress
// from a healthy higher-priority survivor. It only carries traffic once every
// higher-priority path is down.
func TestActiveBackupAddPathLowestPriority(t *testing.T) {
	clock := newFakeClock()
	primary := &fakeHealth{s: telemetry.StateUp}
	s := newSched(t, clock, time.Second, primary)
	if got := s.Pick(); got != 0 {
		t.Fatalf("initial Pick = %d, want 0", got)
	}

	// Admit a second path (initially down): the active selection is undisturbed.
	added := &fakeHealth{s: telemetry.StateDown}
	idx, err := s.AddPath(added)
	if err != nil {
		t.Fatalf("AddPath: %v", err)
	}
	if idx != 1 {
		t.Fatalf("AddPath index = %d, want 1 (appended as lowest priority)", idx)
	}
	if got := s.Pick(); got != 0 {
		t.Fatalf("Pick after admitting a down backup = %d, want 0 (survivor undisturbed)", got)
	}

	// It becomes selectable only once it is healthy AND the primary is down.
	added.up()
	if got := s.Pick(); got != 0 {
		t.Fatalf("Pick with healthy backup but healthy primary = %d, want 0", got)
	}
	primary.down()
	if got := s.Pick(); got != 1 {
		t.Fatalf("Pick with primary down = %d, want 1 (failover to the added path)", got)
	}

	if _, err := s.AddPath(nil); err == nil {
		t.Fatal("AddPath(nil) succeeded, want error")
	}
}

// TestActiveBackupRemovePathActive is the T30 removal invariant: removing the
// currently-active path fails egress over to the best remaining path on the next
// Pick, while removing a NON-active path leaves the active selection untouched.
func TestActiveBackupRemovePathActive(t *testing.T) {
	clock := newFakeClock()
	primary := &fakeHealth{s: telemetry.StateUp}
	backup := &fakeHealth{s: telemetry.StateUp}
	third := &fakeHealth{s: telemetry.StateUp}
	s := newSched(t, clock, time.Second, primary, backup, third)
	if got := s.Pick(); got != 0 {
		t.Fatalf("initial Pick = %d, want 0", got)
	}

	// Remove the NON-active backup (index 1): the active (primary) is untouched, and
	// third shifts down from index 2 to index 1.
	if err := s.RemovePath(1); err != nil {
		t.Fatalf("RemovePath(1): %v", err)
	}
	if got := s.Pick(); got != 0 {
		t.Fatalf("Pick after removing a non-active path = %d, want 0 (survivor undisturbed)", got)
	}

	// Now remove the ACTIVE primary (index 0): egress must fail over to the remaining
	// path (third, now at index 0).
	if err := s.RemovePath(0); err != nil {
		t.Fatalf("RemovePath(0): %v", err)
	}
	if got := s.Pick(); got != 0 {
		t.Fatalf("Pick after removing the active path = %d, want 0 (the sole survivor)", got)
	}
	// third is the only path left and it is up.
	third.down()
	if got := s.Pick(); got >= 0 {
		t.Fatalf("Pick with the sole survivor down = %d, want negative", got)
	}

	if err := s.RemovePath(5); err == nil {
		t.Fatal("RemovePath out of range succeeded, want error")
	}
}

// TestActiveBackupRemoveDoesNotThrashFailback checks the failback-dwell bookkeeping
// survives a removal: removing the pending failback candidate abandons the dwell
// cleanly rather than failing back onto a stale index.
func TestActiveBackupRemoveDoesNotThrashFailback(t *testing.T) {
	clock := newFakeClock()
	primary := &fakeHealth{s: telemetry.StateUp}
	backup := &fakeHealth{s: telemetry.StateUp}
	s := newSched(t, clock, time.Hour, primary, backup)

	// Fail over to the backup, then let the primary recover so a failback dwell arms
	// (pending = index 0, the primary).
	primary.down()
	if got := s.Pick(); got != 1 {
		t.Fatalf("Pick after primary down = %d, want 1", got)
	}
	primary.up()
	if got := s.Pick(); got != 1 {
		t.Fatalf("Pick mid-dwell = %d, want 1 (debounced)", got)
	}

	// Remove the pending failback candidate (the primary, index 0). backup shifts to
	// index 0 and stays active; the dwell is abandoned with no stale pending index.
	if err := s.RemovePath(0); err != nil {
		t.Fatalf("RemovePath(0): %v", err)
	}
	if got := s.Pick(); got != 0 {
		t.Fatalf("Pick after removing the pending candidate = %d, want 0 (backup, now sole path)", got)
	}
}
