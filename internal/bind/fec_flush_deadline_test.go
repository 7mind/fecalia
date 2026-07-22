package bind

import "testing"

// TestFecFlushDeadlineDrivesAdaptiveController closes the D96 test gap: it invokes
// m.fecFlushDeadline() itself — the exact TryLock-guarded entry point fecTickLoop drives —
// instead of calling driveAdaptiveControllerLocked directly (as
// TestAdaptiveControllerDrivesEncoderParity does). The call is made with m.mu UNheld, from
// the test goroutine, so fecFlushDeadline's own TryLock succeeds and the drive runs through
// production code: peer iteration, the adaptive drive, and encoder.Tick(), all under the
// lock it acquires and releases itself.
func TestFecFlushDeadlineDrivesAdaptiveController(t *testing.T) {
	psk := testKey(t, 0x54)
	clk := newFakeClock()
	m, probers := newAdaptiveProbingMultipath(t, 1, psk, clk)
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	fc, ac := adaptiveFECConfigs()

	// Before any drive the encoder emits zero parity (T29 baseline).
	if got := admitFullGroupParity(t, m, fc.DataShards); got != 0 {
		t.Fatalf("initial adaptive parity = %d, want 0 (controller starts at M=0)", got)
	}

	// Bring the path Up with measured loss, then drive through the PRODUCTION path: call
	// fecFlushDeadline() itself (m.mu unheld here), not driveAdaptiveControllerLocked.
	bringProberUpWithLoss(t, probers[0], psk, clk, 40, 6)
	loss := probers[0].Estimate().Loss

	m.fecFlushDeadline()

	// The T263 snapshot proves the drive ran: adaptive Parity > 0, smoothed loss > 0, and
	// the eligible signal reflects the single up path's measured loss.
	snaps := m.PeerSnapshots()
	if len(snaps) == 0 {
		t.Fatalf("PeerSnapshots returned no peer")
	}
	adaptive := snaps[0].FEC.Adaptive
	if adaptive == nil {
		t.Fatalf("adaptive-mode snapshot FEC.Adaptive == nil, want the driven decision")
	}
	if adaptive.Parity <= 0 || adaptive.Parity > ac.MaxParity {
		t.Fatalf("snapshot Adaptive.Parity = %d, want in (0,%d] after fecFlushDeadline drove the controller", adaptive.Parity, ac.MaxParity)
	}
	if adaptive.SmoothedLoss <= 0 {
		t.Fatalf("snapshot Adaptive.SmoothedLoss = %v, want > 0", adaptive.SmoothedLoss)
	}
	if adaptive.EligibleLoss != loss {
		t.Fatalf("snapshot Adaptive.EligibleLoss = %v, want the up path's measured loss %v", adaptive.EligibleLoss, loss)
	}
	if adaptive.EligiblePaths < 1 {
		t.Fatalf("snapshot Adaptive.EligiblePaths = %d, want >= 1 (the up path)", adaptive.EligiblePaths)
	}

	// The encoder is actually retargeted: a group closed after the flush emits exactly the
	// driven parity, not the pre-drive zero.
	if got := admitFullGroupParity(t, m, fc.DataShards); got != adaptive.Parity {
		t.Fatalf("encoder emitted %d parity after fecFlushDeadline, want the driven target %d", got, adaptive.Parity)
	}
}

// TestFecFlushDeadlineSkipsDriveWhenLocked proves fecFlushDeadline's TryLock guard: with
// m.mu held BY THE TEST (simulating a concurrent Send/Close/AddPath), fecFlushDeadline must
// return immediately without driving the controller — never blocking (which would deadlock
// the test) and never touching the published snapshot.
func TestFecFlushDeadlineSkipsDriveWhenLocked(t *testing.T) {
	psk := testKey(t, 0x55)
	clk := newFakeClock()
	m, probers := newAdaptiveProbingMultipath(t, 1, psk, clk)
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	// Seed a non-zero published decision by driving once through the production path, so
	// the "unchanged" assertion below is non-vacuous (distinguishes "held" from
	// "zero-initialized and never touched").
	bringProberUpWithLoss(t, probers[0], psk, clk, 40, 6)
	m.fecFlushDeadline()

	seeded := m.PeerSnapshots()
	if len(seeded) == 0 {
		t.Fatalf("PeerSnapshots returned no peer")
	}
	seededAdaptive := seeded[0].FEC.Adaptive
	if seededAdaptive == nil || seededAdaptive.Parity <= 0 || seededAdaptive.SmoothedLoss <= 0 {
		t.Fatalf("seed drive published %+v, want non-nil with Parity > 0 and SmoothedLoss > 0", seededAdaptive)
	}

	// Hold m.mu as a concurrent caller would, then invoke fecFlushDeadline: its TryLock
	// must fail and it must return without driving — not deadlock.
	m.mu.Lock()
	m.fecFlushDeadline()
	m.mu.Unlock()

	after := m.PeerSnapshots()
	if len(after) == 0 {
		t.Fatalf("PeerSnapshots returned no peer")
	}
	afterAdaptive := after[0].FEC.Adaptive
	if afterAdaptive == nil {
		t.Fatalf("adaptive-mode snapshot FEC.Adaptive == nil after locked call, want the unchanged seeded decision")
	}
	if afterAdaptive.Parity != seededAdaptive.Parity {
		t.Fatalf("snapshot Adaptive.Parity changed to %d while m.mu was held, want unchanged %d (TryLock should have skipped the drive)", afterAdaptive.Parity, seededAdaptive.Parity)
	}
	if afterAdaptive.SmoothedLoss != seededAdaptive.SmoothedLoss {
		t.Fatalf("snapshot Adaptive.SmoothedLoss changed to %v while m.mu was held, want unchanged %v", afterAdaptive.SmoothedLoss, seededAdaptive.SmoothedLoss)
	}
	if afterAdaptive.EligiblePaths != seededAdaptive.EligiblePaths {
		t.Fatalf("snapshot Adaptive.EligiblePaths changed to %d while m.mu was held, want unchanged %d", afterAdaptive.EligiblePaths, seededAdaptive.EligiblePaths)
	}
}
