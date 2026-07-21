package device

import (
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/bind"
	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/log"
	"github.com/7mind/wanbond/internal/telemetry"
)

// flagProbe records whether ProbePMTU was ever called, so a test can assert a PINNED
// path never probes. It is safe for the discovery goroutine to call concurrently.
type flagProbe struct{ called atomic.Bool }

func (p *flagProbe) ProbePMTU(int) (bool, error) { p.called.Store(true); return false, nil }

// maxConsecutiveProbe always echoes and records, per candidate on-wire size, the greatest
// number of times that single size was probed in a row — which equals the machine's
// N-consecutive confirmation count, since an all-echo search runs the full confirmation
// loop for every candidate. It lets a device test prove the reliability config reached the
// telemetry machine without coupling to the private default constant.
type maxConsecutiveProbe struct {
	mu      sync.Mutex
	perSize map[int]int
	maxSame int
}

func (p *maxConsecutiveProbe) ProbePMTU(sz int) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.perSize == nil {
		p.perSize = map[int]int{}
	}
	p.perSize[sz]++
	if p.perSize[sz] > p.maxSame {
		p.maxSame = p.perSize[sz]
	}
	return true, nil
}

// TestPMTUConfigForReliabilityDefaults pins the device-side wiring T236 owns: pmtuConfigFor
// leaves Confirmations at 0 (so telemetry applies its N-consecutive reliability default,
// D91 — the fix can never be silently weakened from the device layer) and SafetyMargin at 0
// (byte-identical reporting; the opt-in stays unwired per the G25 plan), pins a path with an
// operator mtu, and reserves the junk headroom.
func TestPMTUConfigForReliabilityDefaults(t *testing.T) {
	const junk = 24
	cfg := pmtuConfigFor(config.Path{Name: "5g"}, junk)
	if cfg.Confirmations > 0 {
		t.Fatalf("pmtuConfigFor set Confirmations=%d; must leave it 0 so the N-consecutive reliability default (D91) applies", cfg.Confirmations)
	}
	if cfg.SafetyMargin != 0 {
		t.Fatalf("pmtuConfigFor set SafetyMargin=%d; must default to 0 (byte-identical reporting)", cfg.SafetyMargin)
	}
	if cfg.ConfiguredMTU != 0 {
		t.Fatalf("non-pinned path ConfiguredMTU=%d, want 0", cfg.ConfiguredMTU)
	}
	if cfg.DefaultMTU != bind.DefaultPathMTU {
		t.Fatalf("DefaultMTU=%d, want %d", cfg.DefaultMTU, bind.DefaultPathMTU)
	}
	if cfg.JunkHeadroom != junk {
		t.Fatalf("JunkHeadroom=%d, want %d", cfg.JunkHeadroom, junk)
	}
	if pinned := pmtuConfigFor(config.Path{Name: "starlink", MTU: 1400}, junk); pinned.ConfiguredMTU != 1400 {
		t.Fatalf("pinned ConfiguredMTU=%d, want 1400 (operator mtu authoritative)", pinned.ConfiguredMTU)
	}
}

// TestPMTUConfigForReliabilityReachesMachine proves the N-consecutive reliability config
// actually reaches the telemetry machine via pmtuConfigFor: a full all-echo search probes
// every accepted candidate MORE than once, which single-echo acceptance (the pre-D91
// regression, Confirmations:1) never would.
func TestPMTUConfigForReliabilityReachesMachine(t *testing.T) {
	probe := &maxConsecutiveProbe{}
	d := telemetry.NewPMTUDiscovery("5g", pmtuConfigFor(config.Path{Name: "5g"}, 0),
		probe, telemetry.SystemClock{}, discardLogger(t))
	if err := d.Tick(telemetry.StateUp); err != nil { // one synchronous search
		t.Fatalf("Tick: %v", err)
	}
	if probe.maxSame <= 1 {
		t.Fatalf("each candidate was probed at most %d time(s); the N-consecutive reliability default (D91) did not reach the machine (single-echo acceptance regressed)", probe.maxSame)
	}
}

// TestPMTUConfigForNoBootDipBeforeConvergence locks the no-boot-dip invariant (R245): a
// non-pinned discovery built via pmtuConfigFor reports 0 before its first convergence, so
// the T209 resizer keeps the configured-or-default sizing until a REAL PMTU is measured.
func TestPMTUConfigForNoBootDipBeforeConvergence(t *testing.T) {
	d := telemetry.NewPMTUDiscovery("5g", pmtuConfigFor(config.Path{Name: "5g"}, 0),
		&flagProbe{}, telemetry.SystemClock{}, discardLogger(t))
	if got := d.PathMTUOrZero(); got != 0 {
		t.Fatalf("pre-convergence PathMTUOrZero = %d, want 0 (no boot-time dip)", got)
	}
}

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
