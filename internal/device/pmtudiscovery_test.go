package device

import (
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/log"
	"github.com/7mind/wanbond/internal/telemetry"
)

// flagProbe records whether ProbePMTU was ever called, so a test can assert a PINNED
// path never probes. It is safe for the discovery goroutine to call concurrently.
type flagProbe struct{ called atomic.Bool }

func (p *flagProbe) ProbePMTU(int) (bool, error) { p.called.Store(true); return false, nil }

// TestPMTUDiscoveryLoopPinnedNeverProbesAndStops locks two T228/R245 properties on the
// per-path discovery goroutine: a PINNED path (explicit configured mtu) never issues a
// probe (Tick is a no-op — the operator knob is authoritative), and the returned stop
// func halts the goroutine and is idempotent (no double-close panic, no leak).
func TestPMTUDiscoveryLoopPinnedNeverProbesAndStops(t *testing.T) {
	lg, err := log.New("error", io.Discard)
	if err != nil {
		t.Fatalf("log.New: %v", err)
	}
	probe := &flagProbe{}
	d := telemetry.NewPMTUDiscovery("wan",
		telemetry.PMTUConfig{ConfiguredMTU: 1400, DefaultMTU: 1500},
		probe, telemetry.SystemClock{}, lg)

	stop := startPMTUDiscoveryLoop(d, nil, time.Millisecond)
	time.Sleep(20 * time.Millisecond) // many ticks; a pinned path must probe on none
	stop()
	stop() // idempotent

	if probe.called.Load() {
		t.Fatal("a PINNED path issued a PMTU probe; discovery must never run when mtu is configured")
	}
	if got := d.PathMTU(); got != 1400 {
		t.Fatalf("pinned PathMTU = %d, want the configured 1400", got)
	}
}
