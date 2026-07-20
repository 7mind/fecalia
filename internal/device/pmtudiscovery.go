package device

import (
	"sync"
	"time"

	"github.com/7mind/wanbond/internal/bind"
	"github.com/7mind/wanbond/internal/metrics"
	"github.com/7mind/wanbond/internal/telemetry"
)

// pmtuRefreshInterval is the slow periodic re-probe cadence PMTU discovery uses once a
// path has converged (T228, defect D88): after this much time the next tick re-runs the
// search so a silent underlay path-MTU change is eventually re-measured even without a
// DOWN->UP transition or an endpoint roam. It is deliberately coarse — a converged path
// MTU is stable — so re-probes (which each spend a handful of padded probes) stay rare.
const pmtuRefreshInterval = 5 * time.Minute

// startPMTUDiscovery constructs one telemetry.PMTUDiscovery per configured path and
// drives each on its OWN dedicated goroutine (T228, defect D88). It is called ONLY from
// the privileged Up() path (never the up() test seam), so the fake-TUN unit tests issue
// no real padded probes; the netlink/socket behaviour is e2e-covered (T230).
//
// A path with an EXPLICIT configured mtu is PINNED (telemetry.PMTUDiscovery treats a
// non-zero ConfiguredMTU as authoritative and never probes), so the T210 operator knob
// stays authoritative and composes with auto-discovery. The per-path discovery machine
// reports its converged PMTU via the metrics mapping (T229), which the T209 resizer
// folds into wanbond0's link MTU.
//
// DEDICATED per-path goroutine (R245): PMTUDiscovery.Tick runs a SYNCHRONOUS blocking
// binary search (each probe waits up to its echo-await deadline), so it must NOT be
// driven from the shared single-goroutine liveness/probe loop (which would stall
// liveness for every path and risk a false path-DOWN) NOR from another path's discovery
// goroutine. Each path's search therefore blocks only its own goroutine; liveness
// probing (StartProbeLoop) and every other path's discovery run undisturbed.
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
		d := telemetry.NewPMTUDiscovery(p.Name, telemetry.PMTUConfig{
			ConfiguredMTU:   p.MTU, // non-zero PINS the path (operator knob authoritative)
			DefaultMTU:      bind.DefaultPathMTU,
			RefreshInterval: pmtuRefreshInterval,
			JunkHeadroom:    junk,
		}, probe, telemetry.SystemClock{}, t.log)
		machines[p.Name] = d
		// A SINGLE bind roam callback covers BOTH re-probe triggers: the concentrator
		// learned-endpoint change (dispatchInbound setRemote) AND the edge hub-failover
		// repoint (SetPeerRemote -> setRemote on the primary peer), so no separate
		// device-side failover wiring is needed (R245). DOWN->UP re-probe comes from Tick's
		// PathState input.
		t.bind.OnPathRoam(p.Name, d.NotifyRoam)
		stops = append(stops, startPMTUDiscoveryLoop(d, t.metricsSrc, p.Name, telemetry.DefaultProbeInterval))
	}
	t.pmtuDiscoverers = machines
	t.stopPMTUDiscovery = func() {
		for _, s := range stops {
			s()
		}
	}
}

// startPMTUDiscoveryLoop drives one path's PMTUDiscovery on its own goroutine, ticking
// on interval with the path's current liveness read from the metrics Source. It returns
// an idempotent stop func Close invokes. A pinned path still runs the loop, but its Tick
// is a cheap no-op (it never probes).
func startPMTUDiscoveryLoop(d *telemetry.PMTUDiscovery, src metrics.Source, pathName string, interval time.Duration) func() {
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
				// A transport error leaves the search unconverged; the next tick retries.
				_ = d.Tick(pathStateFromSource(src, pathName))
			}
		}
	}()
	return func() { once.Do(func() { close(stop) }) }
}

// pathStateFromSource folds the named path's liveness from the metrics Source: UP when
// ANY bound peer reports it UP (matching sampleMTU's per-path fold, T225/tunresize),
// else DOWN. A DOWN path cannot echo a probe, so PMTUDiscovery.Tick defers its search
// until the path recovers.
func pathStateFromSource(src metrics.Source, name string) telemetry.PathState {
	for _, ps := range src.Paths() {
		if ps.Name == name && ps.State == telemetry.StateUp {
			return telemetry.StateUp
		}
	}
	return telemetry.StateDown
}
