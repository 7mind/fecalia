package device

import (
	"sync"
	"time"

	"github.com/7mind/wanbond/internal/bind"
	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/log"
	"github.com/7mind/wanbond/internal/metrics"
	"github.com/7mind/wanbond/internal/telemetry"
)

// mtuResizeDwell is the debounce a LOOSENING (MTU-increasing) target must remain
// stable for before it is applied to the live wanbond0 link (T209, defect D85),
// reusing the scheduler's failback dwell (defaultFailbackDwell): a path flapping
// DOWN/UP otherwise thrashes the link MTU up and back. A TIGHTENING target (a
// smaller-PMTU path became the constraint) is applied IMMEDIATELY — running at too
// large an MTU risks IP fragmentation / PMTUD blackholes, the very failure mode this
// sizing exists to avoid — so only the loosening direction is debounced, exactly as
// active-backup fails over instantly but fails back only after the dwell.
const mtuResizeDwell = defaultFailbackDwell

// pathMTUSample is one path's input to the runtime TUN-resize recompute (T209, D85):
// its current liveness verdict and its effective outer path MTU (the per-path
// discovered PMTU from telemetry.PMTUDiscovery once wired through the metrics Source,
// else its configured-or-default MTU). The resizer folds the samples for every
// configured path into min(bind.InnerMTU(pmtu, fec)) across the UP ones.
type pathMTUSample struct {
	state telemetry.PathState
	pmtu  int
}

// minInnerMTU returns min(bind.InnerMTU(pmtu, fecEnabled)) across the UP paths in
// samples, with ok=true when at least one path is UP. With NO path UP it returns
// ok=false and the caller keeps the current link MTU (a fully-down tunnel must not
// resize to a degenerate value). It is a pure function so the recompute decision is
// unit-testable with no netlink socket (the netlink apply itself is e2e-covered, T212).
func minInnerMTU(samples []pathMTUSample, fecEnabled bool) (int, bool) {
	min := 0
	ok := false
	for _, s := range samples {
		if s.state != telemetry.StateUp {
			continue
		}
		inner := bind.InnerMTU(s.pmtu, fecEnabled)
		if !ok || inner < min {
			min = inner
			ok = true
		}
	}
	return min, ok
}

// mtuResizer drives the runtime wanbond0 link-MTU adjustment (T209, defect D85). On
// each recompute it folds the current per-path liveness+PMTU samples into the min
// inner MTU across UP paths and, when that differs from the applied link MTU, sets the
// live link MTU via the injected apply (setLinkMTU in production). A tightening
// (smaller) target is applied at once; a loosening (larger) target is debounced for
// dwell so a flapping path does not thrash the link. The boot-time tunMTU (T205) is
// the initial applied value; this only adds the runtime adjustment. recompute is safe
// for concurrent callers; all mutable fields are guarded by mu.
type mtuResizer struct {
	name       string
	fecEnabled bool
	dwell      time.Duration
	clock      telemetry.Clock
	// apply sets the live link MTU (setLinkMTU in production). A returned error is
	// WARNed and leaves the applied value unchanged so the next recompute retries.
	apply func(mtu int) error
	// gauge records the applied MTU to the wanbond_tun_mtu exposition; nil when no
	// gauge sink is wired. Called only on a successful apply.
	gauge func(mtu int)
	log   log.Logger

	mu           sync.Mutex
	applied      int       // the link MTU currently in force
	pending      int       // a debounced loosening candidate; 0 == none pending
	pendingSince time.Time // when the current loosening candidate first appeared
}

// newMTUResizer builds the resizer for one tunnel, seeded with the boot-time TUN MTU
// (T205) as the applied value so the first runtime change is measured against it.
func newMTUResizer(name string, bootMTU int, fecEnabled bool, dwell time.Duration, clock telemetry.Clock, apply func(mtu int) error, gauge func(mtu int), lg log.Logger) *mtuResizer {
	return &mtuResizer{
		name:       name,
		fecEnabled: fecEnabled,
		dwell:      dwell,
		clock:      clock,
		apply:      apply,
		gauge:      gauge,
		log:        lg,
		applied:    bootMTU,
	}
}

// currentMTU returns the link MTU the resizer believes is currently in force.
func (r *mtuResizer) currentMTU() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.applied
}

// recompute folds the current samples into the target min inner MTU across UP paths
// and applies it to the link when warranted (tighten now, loosen after the dwell). It
// is idempotent when the target already equals the applied MTU and safe for concurrent
// callers. The gauge sink (which the device routes through reloadMu) is invoked here,
// OUTSIDE the resizer lock, so it can never invert lock order against the metrics-rebind
// path that reads currentMTU() under reloadMu.
func (r *mtuResizer) recompute(samples []pathMTUSample) {
	applied, changed := r.decide(samples)
	if changed && r.gauge != nil {
		r.gauge(applied)
	}
}

// decide runs the recompute-and-decide under r.mu and performs the netlink apply,
// returning the new applied MTU and whether it changed. Tighten (a smaller-PMTU path
// became the constraint) is applied at once; loosen (the constraining path left) is
// debounced for dwell so a flapping path does not thrash the link. On a successful
// apply it WARNs — a runtime MTU change is operationally significant (it can strand
// in-flight larger packets, so it is not a silent adjustment). An apply error is WARNed
// and leaves the applied value unchanged so the next recompute retries.
func (r *mtuResizer) decide(samples []pathMTUSample) (int, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.clock.Now()

	target, ok := minInnerMTU(samples, r.fecEnabled)
	if !ok || target == r.applied {
		// No UP path (keep the current MTU) or already at target: cancel any pending
		// loosen so a resolved flap does not later fire a stale apply.
		r.pending = 0
		return r.applied, false
	}
	if target > r.applied {
		// Loosen (the constraining path left). Debounce so a flapping path does not
		// thrash the link MTU upward and back.
		if r.pending != target {
			r.pending = target
			r.pendingSince = now
		}
		if now.Sub(r.pendingSince) < r.dwell {
			return r.applied, false
		}
	}
	// Tighten immediately, or a debounced loosen whose dwell has elapsed.
	r.pending = 0
	prev := r.applied
	if err := r.apply(target); err != nil {
		r.log.Warn("tun mtu resize failed", "interface", r.name, "from", prev, "to", target, "error", err.Error())
		return r.applied, false
	}
	r.applied = target
	r.log.Warn("tun mtu resized", "interface", r.name, "from", prev, "to", target)
	return target, true
}

// sampleMTU builds the per-path liveness+PMTU samples the resizer folds, reading the
// LIVE per-path snapshot from the metrics Source and each path's effective outer MTU.
// A path's PMTU is its discovered value (PathSnapshot.PMTU, from telemetry.PMTUDiscovery)
// when positive, else its configured-or-default MTU (0 -> bind.DefaultPathMTU). On a
// multi-peer concentrator the Source repeats each path once per bound peer; a path is
// folded to a single sample here, treated UP if ANY bound peer reports it UP (the
// uplink carries that peer's traffic), with the PMTU taken from any entry (a per-path
// property, equal across peers).
func sampleMTU(src metrics.Source, cfg *config.Config) []pathMTUSample {
	// Reserve the amnezia junk-prefix headroom on the effective outer MTU, mirroring
	// tunMTU (T225/D85): the runtime resizer must reserve the SAME per-packet junk bytes
	// the boot sizing does, else a path-membership change on an obfuscated bond would
	// loosen wanbond0 past the junk-safe envelope and re-expose the D85 shredding on
	// obfuscated DATA. 0 (byte-identical) when obfuscation is off.
	junk := cfg.Amnezia.MaxJunkPrefix()
	configured := make(map[string]int, len(cfg.Paths))
	for _, p := range cfg.Paths {
		mtu := p.MTU
		if mtu == 0 {
			mtu = bind.DefaultPathMTU
		}
		configured[p.Name] = mtu
	}
	byName := make(map[string]*pathMTUSample, len(cfg.Paths))
	order := make([]string, 0, len(cfg.Paths))
	for _, ps := range src.Paths() {
		pmtu := ps.PMTU
		if pmtu <= 0 {
			pmtu = configured[ps.Name]
		}
		if pmtu <= 0 {
			// A snapshot path absent from cfg (anomalous): fall back to the assumed
			// underlay MTU rather than fold a degenerate inner MTU.
			pmtu = bind.DefaultPathMTU
		}
		// Junk headroom applies to the outer envelope regardless of the pmtu source
		// (discovered, configured, or default), matching tunMTU's InnerMTU(pathMTU-junk).
		pmtu -= junk
		cur, seen := byName[ps.Name]
		if !seen {
			order = append(order, ps.Name)
			byName[ps.Name] = &pathMTUSample{state: ps.State, pmtu: pmtu}
			continue
		}
		if ps.State == telemetry.StateUp {
			cur.state = telemetry.StateUp
		}
		if cur.pmtu <= 0 {
			cur.pmtu = pmtu
		}
	}
	samples := make([]pathMTUSample, 0, len(order))
	for _, n := range order {
		samples = append(samples, *byName[n])
	}
	return samples
}

// startMTUResizeLoop drives the resizer on the probe cadence (T209): each tick it
// samples the live per-path liveness+PMTU and recomputes the target link MTU, applying
// a change through the resizer's debounce. It is started ONLY on the privileged Up()
// path — never the up() test seam — so the fake-TUN unit tests never issue a real
// netlink resize (the netlink apply is e2e-covered, T212). It returns an idempotent
// stop func Close invokes.
func startMTUResizeLoop(r *mtuResizer, src metrics.Source, cfg *config.Config, interval time.Duration) func() {
	stop := make(chan struct{})
	var once sync.Once
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				r.recompute(sampleMTU(src, cfg))
			}
		}
	}()
	return func() { once.Do(func() { close(stop) }) }
}
