//go:build e2e

package e2e

import (
	"context"
	"errors"
	"testing"
	"time"
)

type deadlineAwareFECIperfDummy struct{}

func (deadlineAwareFECIperfDummy) combinedOutput(
	ctx context.Context,
	_ string,
	_ ...string,
) ([]byte, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// Regression: T324 field round 5 — iperf's -t duration bounds its transmit
// interval, not the fixture subprocess's wall-clock lifetime.
func TestFECIperfCommandRunnerHonorsWallClockDeadline(t *testing.T) {
	testRunner := func(t *testing.T, runner fecIperfCommandRunner, command string, args ...string) {
		t.Helper()
		const deadline = 50 * time.Millisecond
		ctx, cancel := context.WithTimeout(context.Background(), deadline)
		defer cancel()

		started := time.Now()
		_, err := runner.combinedOutput(ctx, command, args...)
		elapsed := time.Since(started)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf(
				"runner returned after %s with %v, want context deadline exceeded within %s",
				elapsed,
				err,
				deadline,
			)
		}
		if elapsed > 4*deadline {
			t.Fatalf("runner observed deadline after %s, want <= %s", elapsed, 4*deadline)
		}
	}

	t.Run("dummy", func(t *testing.T) {
		testRunner(t, deadlineAwareFECIperfDummy{}, "ignored")
	})
	t.Run("exec-adapter", func(t *testing.T) {
		testRunner(t, execFECIperfCommandRunner{}, "sleep", "0.3")
	})
}
