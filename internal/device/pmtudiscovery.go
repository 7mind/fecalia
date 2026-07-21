package device

import (
	"sync"
	"time"

	"github.com/7mind/wanbond/internal/bind"
	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/telemetry"
)

// pmtuRefreshInterval is the slow periodic re-probe cadence PMTU discovery uses once a
// path has converged (T228, defect D88): after this much time the next tick re-runs the
// search so a silent underlay path-MTU change is eventually re-measured even without a
// DOWN->UP transition or an endpoint roam. It is deliberately coarse — a converged path
// MTU is stable — so re-probes (which each spend a handful of padded probes) stay rare.
const pmtuRefreshInterval = 5 * time.Minute

// pmtuConfigFor builds the telemetry.PMTUConfig for one path's discovery machine. It
// deliberately leaves Confirmations UNSET (zero) so telemetry.NewPMTUDiscovery applies its
// N-consecutive reliability default (defect D91): reliability-aware convergence is ALWAYS
// on for a discovered path, with NO operator knob that could be forgotten and no way to
// weaken it from the device layer. It likewise leaves SafetyMargin at 0 — the
// byte-identical reporting default; T235 keeps SafetyMargin an available-but-unwired
// telemetry opt-in (NOT a required operator knob, per the G25 plan), so no config surface
// is threaded here. A non-zero ConfiguredMTU (the T210 operator mtu) PINS the path so it is
// never probed; JunkHeadroom reserves the Amnezia junk prefix out of the usable envelope.
func pmtuConfigFor(p config.Path, junk int) telemetry.PMTUConfig {
	return telemetry.PMTUConfig{
		ConfiguredMTU:   p.MTU, // non-zero PINS the path (operator knob authoritative)
		DefaultMTU:      bind.DefaultPathMTU,
		RefreshInterval: pmtuRefreshInterval,
		JunkHeadroom:    junk,
		// Confirmations omitted -> defaultPMTUConfirmations (D91 reliability-aware convergence).
		// SafetyMargin omitted (0) -> reported PMTU byte-identical to the raw discovered value.
	}
}

// startPMTUDiscovery constructs one telemetry.PMTUDiscovery per configured path and
// drives each on its OWN dedicated goroutine (T228, defect D88), then wires the
// discovered PMTUs into the metrics/monitor Sources so the T209 resizer folds them
// (T229). It is called ONLY from the privileged Up() path (never the up() test seam),
// so the fake-TUN unit tests issue no real padded probes; the netlink/socket behaviour
// is e2e-covered (T230).
//
// A path with an EXPLICIT configured mtu is PINNED (telemetry.PMTUDiscovery treats a
// non-zero ConfiguredMTU as authoritative and never probes), so the T210 operator knob
// stays authoritative and composes with auto-discovery.
//
// DEDICATED per-path goroutine (R245): PMTUDiscovery.Tick runs a SYNCHRONOUS blocking
// binary search (each probe waits up to its echo-await deadline), so it must NOT be
// driven from the shared single-goroutine liveness/probe loop (which would stall
// liveness for every path and risk a false path-DOWN) NOR from another path's discovery
// goroutine. Each path's search therefore blocks only its own goroutine; liveness
// probing (StartProbeLoop) and every other path's discovery run undisturbed. Liveness is
// read side-effect-free from the primary peer's Prober (State()) — NOT from the metrics
// Source, whose Paths() mutates the throughput last-sample state (T165).
func (t *Tunnel) startPMTUDiscovery() {
	machines := make(map[string]*telemetry.PMTUDiscovery, len(t.cfg.Paths))
	junk := t.cfg.Amnezia.MaxJunkPrefix()
	stops := make([]func(), 0, len(t.cfg.Paths))
	for i := range t.cfg.Paths {
		p := t.cfg.Paths[i]
		probe := t.bind.PMTUProbe(p.Name)
		if probe == nil {
			// No probe transport for this path (probing disabled): discovery is inert, and
			// the T209 resizer keeps the configured-or-default sizing.
			continue
		}
		d := telemetry.NewPMTUDiscovery(p.Name, pmtuConfigFor(p, junk), probe, telemetry.SystemClock{}, t.log)
		machines[p.Name] = d
		// A SINGLE bind roam callback covers BOTH re-probe triggers: the concentrator
		// learned-endpoint change (dispatchInbound setRemote) AND the edge hub-failover
		// repoint (SetPeerRemote -> setRemote on the primary peer), so no separate
		// device-side failover wiring is needed (R245). DOWN->UP re-probe comes from Tick's
		// PathState input.
		t.bind.OnPathRoam(p.Name, d.NotifyRoam)
		var pr *telemetry.Prober
		if i < len(t.primaryProbers) {
			pr = t.primaryProbers[i]
		}
		stops = append(stops, startPMTUDiscoveryLoop(d, pr, telemetry.DefaultProbeInterval))
	}
	t.pmtuDiscoverers = machines
	t.stopPMTUDiscovery = func() {
		for _, s := range stops {
			s()
		}
	}

	// Wire the discovered PMTUs into BOTH Sources (T229): the metrics Source the T209
	// resizer folds (so a constrained/roaming path shrinks/regrows wanbond0), and the
	// monitor Source so the /monitor endpoint shows the same value. Uses the T226 converged
	// accessor (PathMTUOrZero -> 0 until first convergence), so a non-pinned path keeps the
	// configured-or-default fallback until a REAL PMTU is measured — no boot-time dip.
	lookup := func(name string) int {
		if d := machines[name]; d != nil {
			return d.PathMTUOrZero()
		}
		return 0
	}
	if ms, ok := t.metricsSrc.(*metricsSource); ok {
		ms.setPMTULookup(lookup)
	}
	if ms, ok := t.monitorSrc.(*metricsSource); ok {
		ms.setPMTULookup(lookup)
	}
}

// startPMTUDiscoveryLoop drives one path's PMTUDiscovery on its own goroutine, ticking
// on interval with the path's current liveness read from the primary peer's Prober
// (side-effect-free). It returns an idempotent stop func Close invokes. A pinned path
// still runs the loop, but its Tick is a cheap no-op (it never probes).
func startPMTUDiscoveryLoop(d *telemetry.PMTUDiscovery, pr *telemetry.Prober, interval time.Duration) func() {
	stop := make(chan struct{})
	var once sync.Once
	go func() {
		tk := time.NewTicker(interval)
		defer tk.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tk.C:
				state := telemetry.StateDown
				if pr != nil {
					state = pr.State()
				}
				// A transport error leaves the search unconverged; the next tick retries.
				_ = d.Tick(state)
			}
		}
	}()
	return func() { once.Do(func() { close(stop) }) }
}
