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

// defaultPMTUConfirmations is the number of CONSECUTIVE echoing probes a candidate
// on-wire size must accumulate before the binary search accepts it (defect D91). A
// single echo is not enough on a partially-lossy carrier (e.g. a 5G path dropping a
// fraction of packets): a size ABOVE the reliably-carried MTU still echoes on the probes
// that happen to pass, so a single-echo acceptance converges tens of bytes too high and
// black-holes full-size DATA. Requiring N consecutive successes rejects such a marginal
// size — the FIRST non-echo short-circuits the candidate as failed — so the search
// settles at/below the size that echoes RELIABLY. Three is a conservative default: it
// tolerates the isolated packet loss a genuinely-fitting size occasionally sees while
// still rejecting a size that only passes intermittently. PMTUConfig.Confirmations
// overrides it; a zero value maps to this default so existing callers are unchanged.
const defaultPMTUConfirmations = 3

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
	// Confirmations is the number of CONSECUTIVE echoing probes a candidate on-wire size
	// must accumulate before the binary search accepts it (defect D91). A zero value maps
	// to defaultPMTUConfirmations in NewPMTUDiscovery, so existing callers are unchanged.
	// A value of 1 restores the pre-fix single-echo acceptance (used only to reproduce the
	// D91 defect in tests). See defaultPMTUConfirmations for the rationale.
	Confirmations int
	// SafetyMargin is an OPTIONAL number of bytes subtracted from the REPORTED path MTU
	// (PathMTU, PathMTUOrZero, and the UsablePathMTU that composes on it) as an extra
	// cushion below the reliably-carried size (defect D91). It is DEFAULT 0: with no margin
	// the reported value is byte-identical to the raw discovered value. The raw stored
	// discovered value (and the raw search() result) are NEVER reduced by it — only the
	// reported view. A positive margin is clamped so the reported value never drops below
	// PMTUFloor.
	SafetyMargin int
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
	pathName      string
	floor         int
	ceiling       int
	pinned        bool
	refresh       time.Duration
	junkHeadroom  int
	confirmations int
	safetyMargin  int
	probe         PMTUProbe
	clock         Clock
	log           log.Logger

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
	confirmations := cfg.Confirmations
	if confirmations <= 0 {
		// Zero-value maps to the default so existing callers are unchanged.
		confirmations = defaultPMTUConfirmations
	}
	return &PMTUDiscovery{
		pathName:      pathName,
		floor:         PMTUFloor,
		ceiling:       ceiling,
		pinned:        pinned,
		refresh:       cfg.RefreshInterval,
		junkHeadroom:  cfg.JunkHeadroom,
		confirmations: confirmations,
		safetyMargin:  cfg.SafetyMargin,
		probe:         probe,
		clock:         clock,
		log:           logger.Path(pathName),
		state:         pmtuUnstarted,
		discovered:    discovered,
	}
}

// Pinned reports whether this path has an operator-declared MTU and so never runs
// discovery.
func (d *PMTUDiscovery) Pinned() bool { return d.pinned }

// PathMTU returns the current per-path discovered outer PMTU: the configured value for
// a pinned path, the conservative floor before the first search converges, or the
// largest echoing on-wire size the last search settled on. It is the snapshot accessor
// the exposition layer reads for the wanbond_path_mtu gauge. With a non-zero
// PMTUConfig.SafetyMargin the reported value is the discovered value less the margin
// (clamped to PMTUFloor); the raw stored value is unchanged and the default margin 0
// reports it byte-identically.
func (d *PMTUDiscovery) PathMTU() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.reportedLocked()
}

// reportedLocked applies the optional PMTUConfig.SafetyMargin to the raw discovered value
// to produce the value the accessors report (defect D91). The raw stored discovered value
// is NEVER modified — only this reported view. Margin 0 (the default) returns the raw
// value byte-identically; a positive margin is subtracted and clamped to PMTUFloor so an
// over-large margin can never report below the known-good floor. The caller holds d.mu.
//
// A PINNED path is exempt: the operator's declared MTU is authoritative and needs no
// measurement (NewPMTUDiscovery), so the safety margin — a cushion below a *measured*
// reliably-carried size — does not apply; a pinned path always reports its configured
// value verbatim regardless of SafetyMargin.
func (d *PMTUDiscovery) reportedLocked() int {
	if d.pinned || d.safetyMargin <= 0 {
		return d.discovered
	}
	reported := d.discovered - d.safetyMargin
	if reported < PMTUFloor {
		reported = PMTUFloor
	}
	return reported
}

// PathMTUOrZero returns the discovered PMTU ONLY once it is authoritative, and 0
// while it is not yet known (T226, defect D88). A pinned path (operator-declared mtu)
// is authoritative immediately, so it returns the configured value. A non-pinned path
// returns 0 until its FIRST search converges, then the discovered value. This is the
// value the metrics layer maps into metrics.PathSnapshot.PMTU: reporting 0 (rather
// than PathMTU's conservative 1280 floor) before the first convergence keeps the T209
// runtime resizer on its configured-or-default fallback, so a fresh unconstrained
// bond holds InnerMTU(1500) at boot instead of tightening to InnerMTU(1280) and
// regrowing after the loosen-dwell (the boot-time shrink-then-grow dip, R245).
func (d *PMTUDiscovery) PathMTUOrZero() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.pinned || d.state == pmtuConverged {
		return d.reportedLocked()
	}
	return 0
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
	// Compose on the REPORTED path MTU (safety-margined) so the usable envelope never
	// exceeds the margined reported value; junk headroom is then reserved on top.
	usable := d.reportedLocked() - d.junkHeadroom
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

// search binary-searches the largest on-wire size in [floor, ceiling] that echoes
// RELIABLY (d.confirmations consecutive successes, see confirmCandidate; defect D91) and
// returns it. floor is the known-good lower bound (a floor-sized datagram is assumed to
// always traverse the path), so the result is at least floor. It reads only immutable
// fields plus the injected probe, so it runs without d.mu held; a transport error aborts
// the search and is returned to the caller.
func (d *PMTUDiscovery) search() (int, error) {
	lo, hi := d.floor, d.ceiling
	for lo < hi {
		// Round the midpoint up so a two-wide window probes hi, letting lo advance to it.
		mid := (lo + hi + 1) / 2
		echoed, err := d.confirmCandidate(mid)
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

// confirmCandidate reports whether the on-wire size `mid` echoes RELIABLY: it counts as
// passing (echoed=true) only after d.confirmations CONSECUTIVE ProbePMTU(mid) successes
// (defect D91). It SHORT-CIRCUITS on the FIRST non-echo — treating the candidate as
// failed (echoed=false) so the binary search narrows downward (hi = mid-1) — which bounds
// the probes spent on a candidate to d.confirmations worst-case and rejects a marginal,
// intermittently-echoing size fast (the deadline-waiting drop is spent at most once per
// candidate). A genuine transport error (err != nil) aborts the whole search unconverged,
// exactly as a single-probe error did, so a later tick retries.
func (d *PMTUDiscovery) confirmCandidate(mid int) (bool, error) {
	for i := 0; i < d.confirmations; i++ {
		echoed, err := d.probe.ProbePMTU(mid)
		if err != nil {
			return false, err
		}
		if !echoed {
			return false, nil
		}
	}
	return true, nil
}
