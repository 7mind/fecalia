package device

import (
	"strings"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/log"
)

// warnCapturingLogger builds a WARN-level capturing logger over a syncBuffer for the
// startup-diagnostic assertions.
func warnCapturingLogger(t *testing.T) (log.Logger, *syncBuffer) {
	t.Helper()
	buf := &syncBuffer{}
	lg, err := log.New("warn", buf)
	if err != nil {
		t.Fatalf("log.New: %v", err)
	}
	return lg, buf
}

func boolPtrDevice(b bool) *bool { return &b }

// TestStartupWarnOnOverBudget is the T211 WARN-and-allow acceptance (D86 decision 4): an
// over-budget failover configuration (verdict false) must emit EXACTLY ONE startup WARN
// naming the offending numbers, while a within-budget config (verdict true) and an absent
// verdict (nil) must emit nothing. The daemon is never blocked — this is the soft
// complement to the down_after lower-floor hard reject in config validation.
func TestStartupWarnOnOverBudget(t *testing.T) {
	t.Run("over-budget emits one WARN naming the numbers", func(t *testing.T) {
		lg, buf := warnCapturingLogger(t)
		cfg := &config.Config{
			Liveness: config.Liveness{DownAfter: 1200 * time.Millisecond},
			Paths: []config.Path{
				{Name: "starlink", RideThrough: 2 * time.Second},
				{Name: "5g"},
			},
			// budget = 1.2s + 2s + 2*200ms = 3.6s > 3s -> false.
			LivenessBudgetSane: boolPtrDevice(false),
		}

		warnOverBudgetLiveness(lg, cfg)

		out := buf.String()
		if got := strings.Count(out, `"level":"WARN"`); got != 1 {
			t.Fatalf("WARN count = %d, want exactly 1; log:\n%s", got, out)
		}
		if !strings.Contains(out, "liveness failover budget exceeds") {
			t.Errorf("WARN message missing the budget diagnostic; log:\n%s", out)
		}
		// The WARN must name the offending numbers so the operator can act.
		for _, want := range []string{"starlink", "3.6s", "2s", "3s"} {
			if !strings.Contains(out, want) {
				t.Errorf("WARN missing %q; log:\n%s", want, out)
			}
		}
	})

	t.Run("within-budget is silent", func(t *testing.T) {
		lg, buf := warnCapturingLogger(t)
		cfg := &config.Config{
			Liveness:           config.Liveness{DownAfter: 1200 * time.Millisecond},
			Paths:              []config.Path{{Name: "starlink"}, {Name: "5g"}},
			LivenessBudgetSane: boolPtrDevice(true),
		}

		warnOverBudgetLiveness(lg, cfg)

		if out := buf.String(); out != "" {
			t.Errorf("within-budget config emitted output, want silence; log:\n%s", out)
		}
	})

	t.Run("nil verdict is a no-op", func(t *testing.T) {
		lg, buf := warnCapturingLogger(t)
		cfg := &config.Config{
			Liveness: config.Liveness{DownAfter: 1200 * time.Millisecond},
			Paths:    []config.Path{{Name: "starlink"}},
		}

		warnOverBudgetLiveness(lg, cfg)

		if out := buf.String(); out != "" {
			t.Errorf("nil verdict emitted output, want silence; log:\n%s", out)
		}
	})
}
