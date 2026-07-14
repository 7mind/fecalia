package device

import (
	"fmt"
	"strings"
	"testing"

	"github.com/7mind/wanbond/internal/bind"
	"github.com/7mind/wanbond/internal/log"
)

// newInfoCapturingLogger is newCapturingLogger's INFO-level twin (tundiag_test.go builds
// its capturing logger at "error", which would silently drop the INFO record this suite
// asserts on): this test needs BOTH Info and Error records to reach the buffer.
func newInfoCapturingLogger(t *testing.T) (log.Logger, *syncBuffer) {
	t.Helper()
	buf := &syncBuffer{}
	lg, err := log.New("info", buf)
	if err != nil {
		t.Fatalf("log.New: %v", err)
	}
	return lg, buf
}

// TestEngineLoggerCoalescesNoHealthyPathDuringWarmup is the I4 acceptance check: every
// no-healthy-path record (bind.ErrNoHealthyPath, as the engine's real send.go wraps it —
// "%v - Failed to send handshake initiation: %v", peer, err) that arrives BEFORE the first
// path reaches liveness up is coalesced into exactly ONE INFO "waiting for path liveness"
// line and logs ZERO ERRORs; the SAME record arriving AFTER a path has been up logs at
// ERROR (a genuine outage), and is not coalesced.
//
// Mutation-verify: deleting the `!everHadLivePath()` warmup gate in engineLogger's Errorf
// closure makes the pre-up records fall straight to wg.Error, so the zero-ERROR assertion
// below fails — this test cannot pass against that mutant.
func TestEngineLoggerCoalescesNoHealthyPathDuringWarmup(t *testing.T) {
	lg, buf := newInfoCapturingLogger(t)
	everUp := false
	el := engineLogger(lg, "info", func() bool { return everUp })

	// Several failed handshake-initiation attempts land before the first path comes up —
	// exactly the boot-time probe race I4 targets. Multiple calls must coalesce to ONE INFO.
	for i := 0; i < 3; i++ {
		el.Errorf("%v - Failed to send handshake initiation: %v", "peer(test)", bind.ErrNoHealthyPath)
	}

	pre := buf.String()
	if got := strings.Count(pre, "waiting for path liveness"); got != 1 {
		t.Fatalf("pre-up INFO count = %d, want exactly 1; log:\n%s", got, pre)
	}
	if strings.Count(pre, `"level":"ERROR"`) != 0 {
		t.Fatalf("pre-up ERROR count != 0; log:\n%s", pre)
	}
	if strings.Count(pre, `"level":"INFO"`) != 1 {
		t.Fatalf("pre-up total INFO record count != 1; log:\n%s", pre)
	}

	// A path has now reached liveness up: the SAME no-healthy-path record must log at
	// ERROR (a real outage), not be swallowed into the coalesced warmup line.
	everUp = true
	el.Errorf("%v - Failed to send handshake initiation: %v", "peer(test)", bind.ErrNoHealthyPath)

	post := buf.String()
	if strings.Count(post, `"level":"ERROR"`) != 1 {
		t.Fatalf("post-up ERROR count != 1; log:\n%s", post)
	}
	if !strings.Contains(post, "Failed to send handshake initiation") {
		t.Fatalf("post-up ERROR record missing the engine's message; log:\n%s", post)
	}
	// No SECOND coalesced INFO line was added post-up.
	if got := strings.Count(post, "waiting for path liveness"); got != 1 {
		t.Fatalf("total INFO count after the post-up record = %d, want still exactly 1; log:\n%s", got, post)
	}
}

// TestEngineLoggerUnrelatedErrorAlwaysLogsDuringWarmup asserts the warmup gate is scoped
// to bind.ErrNoHealthyPath ONLY: an unrelated engine Errorf record (not wrapping it) must
// still log at ERROR even before the first path is up, so a genuine unrelated failure is
// never silently swallowed by the I4 gate.
func TestEngineLoggerUnrelatedErrorAlwaysLogsDuringWarmup(t *testing.T) {
	lg, buf := newInfoCapturingLogger(t)
	el := engineLogger(lg, "info", func() bool { return false })

	el.Errorf("%v - Failed to decode initiation message", "peer(test)")

	got := buf.String()
	if strings.Count(got, `"level":"ERROR"`) != 1 {
		t.Fatalf("unrelated error ERROR count != 1 during warmup; log:\n%s", got)
	}
	if strings.Contains(got, "waiting for path liveness") {
		t.Fatalf("unrelated error must not trigger the warmup INFO coalescing; log:\n%s", got)
	}
}

// TestEngineLoggerWrappedNoHealthyPathDetected asserts argsHaveNoHealthyPath matches a
// wrapped bind.ErrNoHealthyPath (errors.Is, not string-matching), so a future engine or
// wanbond wrapping layer that fmt.Errorf("...: %w", ...)s the sentinel is still gated.
func TestEngineLoggerWrappedNoHealthyPathDetected(t *testing.T) {
	lg, buf := newInfoCapturingLogger(t)
	el := engineLogger(lg, "info", func() bool { return false })

	wrapped := fmt.Errorf("bind: send: %w", bind.ErrNoHealthyPath)
	el.Errorf("%v - Failed to send handshake initiation: %v", "peer(test)", wrapped)

	got := buf.String()
	if strings.Count(got, "waiting for path liveness") != 1 {
		t.Fatalf("wrapped ErrNoHealthyPath not coalesced to the warmup INFO line; log:\n%s", got)
	}
	if strings.Count(got, `"level":"ERROR"`) != 0 {
		t.Fatalf("wrapped ErrNoHealthyPath unexpectedly logged at ERROR during warmup; log:\n%s", got)
	}
}
