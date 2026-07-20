package telemetry

import (
	"sync"
	"time"

	"github.com/7mind/wanbond/internal/log"
)

// PMTUFloor is the outer path-MTU floor the discovery search never probes below and
// the value reported before the first search converges: the IPv6 minimum link MTU
// (RFC 8200 §5, 1280), the smallest datagram every underlay must carry without
// fragmentation. It matches config.minPathMTU (the accepted lower bound for an
// operator-declared per-path mtu) but is redeclared here rather than imported —
// internal/config must not become a dependency of the telemetry plane. The search
// assumes a datagram at the floor always traverses the path, so the floor is the
// binary search's known-good lower bound.
const PMTUFloor = 1280

// PMTUProbe is the seam the PMTU discovery machine drives to test one candidate outer
// path-MTU: it sends a padded MTU probe sized to onWire outer bytes (DF set, T201; the
// echo carries the same on-wire size, T202) and reports whether a matching echo
// returned before the probe deadline. echoed=true means a datagram of onWire bytes
// traversed the path in both directions; echoed=false means it was dropped or rejected
// as too large (locally EMSGSIZE under DF, or silently in-network). A benign "too
// large, no echo" is echoed=false with err=nil; err is non-nil ONLY for an unexpected
// transport failure, which leaves the search unconverged so a later tick retries.
//
// Production wires ProbePMTU to a Prober's SendPaddedProbe plus an echo-await on the
// injected Clock; unit tests supply a synchronous size predicate so the search runs
// with no real network and no sleep.
type PMTUProbe interface {
	ProbePMTU(onWire int) (echoed bool, err error)
}

// PMTUConfig configures per-path PMTU discovery.
type PMTUConfig struct {
	// ConfiguredMTU is the operator-declared outer path MTU (config.Path.MTU): zero
	// means "unset — auto-discover"; any non-zero value PINS the path so discovery
	// NEVER runs and PathMTU reports the configured value verbatim. This is how the
	// T200 operator knob and auto-discovery compose: an explicit declaration is
	// authoritative.
	ConfiguredMTU int
	// DefaultMTU is the search CEILING when the path is not pinned — the assumed
	// underlay path MTU before measurement. The caller passes bind.DefaultPathMTU
	// (1500); it is injected rather than imported because internal/bind depends on the
	// telemetry plane, not the reverse.
	DefaultMTU int
	// RefreshInterval is the slow periodic re-probe cadence once converged: after this
	// much clock time has elapsed since the last convergence, the next Tick re-runs the
	// search. Zero disables periodic refresh (the path is still re-probed on a DOWN->UP
	// transition and on NotifyRoam).
	RefreshInterval time.Duration
	// JunkHeadroom is the maximum AmneziaWG junk-prefix length to reserve from the
	// discovered USABLE outer envelope (config.Amnezia.MaxJunkPrefix; defect D85,
	// fix-direction 4). The padded MTU probes measure PROBE-plane datagrams that do NOT
	// carry the junk prefix real WG DATA carries, so the raw echoing size over-estimates
	// the usable outer size on an obfuscated path. UsablePathMTU subtracts this headroom
	// so a full-size obfuscated DATA datagram cannot exceed the path MTU and
	// EMSGSIZE/black-hole; the raw PathMTU gauge is left unchanged. Zero (plain WireGuard)
	// leaves usable == raw, byte-identical.
	JunkHeadroom int
}

// pmtuState is the discovery machine's search phase.
type pmtuState uint8

const (
	// pmtuUnstarted is the initial phase: no search has run. The first Tick on an UP,
	// unpinned path starts one.
	pmtuUnstarted pmtuState = iota
	// pmtuSearching means a search is in progress or a prior search failed on a
	// transport error and must be retried.
	pmtuSearching
	// pmtuConverged means the search settled on a PMTU; only a re-probe trigger
	// (DOWN->UP, roam, or periodic refresh) restarts it.
	pmtuConverged
)

// PMTUDiscovery is the per-path PMTU discovery state machine (T206, defect D85). It
// binary-searches the largest padded-probe on-wire size that still echoes — between
// the PMTUFloor and a ceiling of the configured-or-default path MTU — then holds,
// re-probing on a path DOWN->UP transition, an endpoint roam (NotifyRoam), or a slow
// periodic refresh. A pinned path (ConfiguredMTU != 0) never probes. It is colocated
// with the Prober whose padded probes it drives and reads the same injectable Clock,
// so it is unit-testable against a synthetic echo transport with no real network.
//
// Concurrency: Tick must be driven from a SINGLE goroutine (the per-path probe/liveness
// loop), exactly like Prober.Tick — two concurrent Ticks are not supported. NotifyRoam
// and PathMTU may be called concurrently from other goroutines; they take an internal
// mutex held only briefly. The mutex is NOT held across a ProbePMTU call, so a probe
// that blocks awaiting an echo never blocks a concurrent PathMTU/NotifyRoam reader.
type PMTUDiscovery struct {
	pathName     string
	floor        int
	ceiling      int
	pinned       bool
	refresh      time.Duration
	junkHeadroom int
	probe        PMTUProbe
	clock        Clock
	log          log.Logger

	mu             sync.Mutex
	state          pmtuState
	discovered     int
	prevUp         bool
	haveState      bool
	reprobePending bool
	lastConverged  time.Time
}

// NewPMTUDiscovery builds the discovery machine for one path. When cfg.ConfiguredMTU
// is non-zero the path is PINNED: discovered is fixed at that value and no probe is
// ever sent. Otherwise the ceiling is cfg.DefaultMTU (clamped up to the floor so the
// search window is never empty) and the reported PMTU starts at the conservative
// PMTUFloor until the first search converges.
func NewPMTUDiscovery(pathName string, cfg PMTUConfig, probe PMTUProbe, clock Clock, logger log.Logger) *PMTUDiscovery {
	pinned := cfg.ConfiguredMTU != 0
	ceiling := cfg.DefaultMTU
	if pinned {
		ceiling = cfg.ConfiguredMTU
	}
	if ceiling < PMTUFloor {
		ceiling = PMTUFloor
	}
	discovered := PMTUFloor
	if pinned {
		// The operator's declaration is authoritative and needs no measurement.
		discovered = ceiling
	}
	return &PMTUDiscovery{
		pathName:     pathName,
		floor:        PMTUFloor,
		ceiling:      ceiling,
		pinned:       pinned,
		refresh:      cfg.RefreshInterval,
		junkHeadroom: cfg.JunkHeadroom,
		probe:        probe,
		clock:        clock,
		log:          logger.Path(pathName),
		state:        pmtuUnstarted,
		discovered:   discovered,
	}
}

// Pinned reports whether this path has an operator-declared MTU and so never runs
// discovery.
func (d *PMTUDiscovery) Pinned() bool { return d.pinned }

// PathMTU returns the current per-path discovered outer PMTU: the configured value for
// a pinned path, the conservative floor before the first search converges, or the
// largest echoing on-wire size the last search settled on. It is the snapshot accessor
// the exposition layer reads for the wanbond_path_mtu gauge.
func (d *PMTUDiscovery) PathMTU() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.discovered
}

// UsablePathMTU returns the discovered outer PMTU reduced by the obfuscation junk-prefix
// headroom (PMTUConfig.JunkHeadroom): the largest outer datagram size real WG DATA may
// occupy on this path WITHOUT the AmneziaWG junk prefix pushing it past the measured path
// MTU (defect D85, fix-direction 4). PathMTU reports the RAW measured path MTU (the
// physical property, exposed as the wanbond_path_mtu gauge); UsablePathMTU is the value
// downstream inner-MTU sizing must use so a full-size obfuscated DATA datagram cannot
// exceed the path and EMSGSIZE/black-hole. With no obfuscation (JunkHeadroom == 0) the two
// are byte-identical. The result is clamped to >= 0 (a junk reserve larger than the raw
// PMTU is degenerate config, not a negative size).
func (d *PMTUDiscovery) UsablePathMTU() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	usable := d.discovered - d.junkHeadroom
	if usable < 0 {
		usable = 0
	}
	return usable
}

// NotifyRoam records an endpoint roam (a concentrator learned-endpoint change or an
// edge hub-failover repoint) as a re-probe trigger: the next Tick on an UP path re-runs
// the search, because the new underlay path may have a different PMTU. It is a no-op on
// a pinned path.
func (d *PMTUDiscovery) NotifyRoam() {
	d.mu.Lock()
	d.reprobePending = true
	d.mu.Unlock()
}

// Tick advances the discovery machine against the path's current liveness state; the
// probe/liveness loop calls it once per interval. It observes DOWN->UP transitions and
// the periodic-refresh deadline to decide whether a (re-)search is due, then runs the
// search OUTSIDE the lock. A pinned path, and a path that is currently DOWN (which
// cannot echo), never probe. It returns the transport error from a failed search (the
// machine stays unconverged and retries on the next tick) and nil otherwise.
func (d *PMTUDiscovery) Tick(state PathState) error {
	d.mu.Lock()
	shouldSearch := d.decideLocked(state)
	d.mu.Unlock()
	if !shouldSearch {
		return nil
	}

	pmtu, err := d.search()
	if err != nil {
		// Leave state == pmtuSearching so the next tick retries; do not latch a PMTU.
		return err
	}

	d.mu.Lock()
	d.discovered = pmtu
	d.state = pmtuConverged
	d.lastConverged = d.clock.Now()
	d.mu.Unlock()
	d.log.Info("path pmtu converged", "pmtu", pmtu, "floor", d.floor, "ceiling", d.ceiling)
	return nil
}

// decideLocked updates the transition/refresh bookkeeping and reports whether Tick
// should run a search now. The caller holds d.mu. It sets state to pmtuSearching when
// it returns true so a concurrent PathMTU read observes the in-flight search phase.
func (d *PMTUDiscovery) decideLocked(state PathState) bool {
	up := state == StateUp
	// A DOWN->UP transition re-triggers discovery: the recovered path may present a
	// different PMTU (e.g. after a link change during the outage).
	if d.haveState && !d.prevUp && up {
		d.reprobePending = true
	}
	d.prevUp = up
	d.haveState = true

	if d.pinned {
		return false
	}
	// Slow periodic refresh: once converged, re-probe after the configured interval.
	if d.state == pmtuConverged && d.refresh > 0 && d.clock.Now().Sub(d.lastConverged) >= d.refresh {
		d.reprobePending = true
	}
	// A DOWN path cannot echo a probe; defer discovery until it is UP.
	if !up {
		return false
	}
	// Search when not yet converged (first-ever search, or a retry after a transport
	// error left it in pmtuSearching) or when a re-probe trigger fired.
	if d.state != pmtuConverged || d.reprobePending {
		d.reprobePending = false
		d.state = pmtuSearching
		return true
	}
	return false
}

// search binary-searches the largest on-wire size in [floor, ceiling] that still
// echoes and returns it. floor is the known-good lower bound (a floor-sized datagram is
// assumed to always traverse the path), so the result is at least floor. It reads only
// immutable fields plus the injected probe, so it runs without d.mu held; a transport
// error aborts the search and is returned to the caller.
func (d *PMTUDiscovery) search() (int, error) {
	lo, hi := d.floor, d.ceiling
	for lo < hi {
		// Round the midpoint up so a two-wide window probes hi, letting lo advance to it.
		mid := (lo + hi + 1) / 2
		echoed, err := d.probe.ProbePMTU(mid)
		if err != nil {
			return 0, err
		}
		if echoed {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return lo, nil
}
