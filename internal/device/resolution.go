package device

import (
	"context"
	"net/netip"
	"sync"
	"time"

	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/dnsresolve"
	"github.com/7mind/wanbond/internal/log"
	"github.com/7mind/wanbond/internal/telemetry"
)

// nameTarget is one hostname endpoint spec the re-resolution controller tracks: the index of
// its owning failoverSpec (which equals the entry's position in config.Peer.EndpointSpecs, since
// the failover specs are built 1:1 in TOML order) together with the host/port to resolve. Only
// hostname specs (EndpointSpec.IsName) become nameTargets; literal specs never re-resolve.
type nameTarget struct {
	specIdx int
	host    string
	port    uint16
}

// resolution is the edge-side re-resolution controller (Q31/T73). It re-resolves each opted-in
// hostname peer-endpoint spec on a fixed poll cadence AND out-of-band the instant every path to
// the ACTIVE concentrator goes DOWN, then hands the fresh record set to hubFailover's update API
// so the bond is repointed only on an ACTUAL change (D32 suppression lives inside updateResolution).
//
// It mirrors the hubFailover shape: a pure constructor over injected collaborators (a
// dnsresolve.Resolver, the hubFailover whose update API / active-spec identity / shared liveness
// plane it drives, a telemetry.Clock, and the [dns] poll interval + per-lookup timeout), so its
// decision logic is unit-testable against a fake resolver, a fake clock, and a hubFailover built
// over fake health/remote. Resolution runs entirely OFF the send hot path — its own goroutine
// performs the network lookups; results are applied only through updateResolution under the
// endpoint-set lock (Q34: the two controllers coordinate purely through that shared lock and the
// update API, never a second, competing repoint path).
//
// RETENTION INVARIANT (D46): a lookup that FAILS (error/timeout/NXDOMAIN) or yields an EMPTY
// address set NEVER publishes — the spec keeps its last-good expansion and the controller retries
// next tick. A working endpoint set is therefore never torn down by a transient resolver fault,
// and hubFailover never sees an empty active spec that its total<2 guard could strand the bond on.
type resolution struct {
	resolver dnsresolve.Resolver
	// fo is the hub-failover controller this resolver drives: the sole mutation point
	// (updateResolution), the active-spec identity, and the shared liveness plane all live there.
	fo *hubFailover
	// targets is the ordered set of hostname specs to re-resolve; empty for an all-literal peer,
	// in which case the loop is a no-op.
	targets []nameTarget
	// families is the set of IP families the local path sockets can source traffic from (a v4
	// source_addr path reaches only v4 endpoints, a v6 path only v6). A resolved address of a
	// family NO local path can source is unreachable and is dropped from a spec's expansion — else
	// an AAAA-only answer on v4-only paths would repoint the bond at an address no path can carry.
	families pathFamilies

	clock        telemetry.Clock
	pollInterval time.Duration
	timeout      time.Duration
	log          log.Logger

	mu sync.Mutex
	// nextPollAt is when the next SCHEDULED full poll of every hostname spec is due (clock time).
	// Initialized to construction time so the first step polls immediately (prompt boot adoption
	// for a hostname-only peer). Advanced by the TTL-clamped poll delay after each poll, and reset
	// after an out-of-band liveness-loss re-resolve so the two do not double-resolve back to back.
	// Guarded by mu.
	nextPollAt time.Time
	// lastAllDown is the previous liveness-loss reading, so the out-of-band re-resolve is
	// EDGE-triggered (fires once when hub loss is first observed, not every tick while it
	// persists — the poll loop covers a sustained outage without hammering the resolver).
	// Guarded by mu.
	lastAllDown bool
}

// newResolution builds the controller over its injected collaborators. It is a pure constructor
// (no goroutine, no engine dependency) so a test drives step/pollAll directly against a fake
// resolver and a fake clock. nextPollAt is armed at construction time so the first step polls
// immediately.
func newResolution(resolver dnsresolve.Resolver, fo *hubFailover, targets []nameTarget, families pathFamilies, clock telemetry.Clock, pollInterval, timeout time.Duration, lg log.Logger) *resolution {
	return &resolution{
		resolver:     resolver,
		fo:           fo,
		targets:      targets,
		families:     families,
		clock:        clock,
		pollInterval: pollInterval,
		timeout:      timeout,
		log:          lg,
		nextPollAt:   clock.Now(),
	}
}

// nameTargetsFromSpecs extracts the hostname specs (EndpointSpec.IsName) from an ordered spec
// list as nameTargets, tagging each with its index in that list — which equals its failoverSpec
// index, since both are built 1:1 in TOML order. Literal specs contribute nothing.
func nameTargetsFromSpecs(specs []config.EndpointSpec) []nameTarget {
	var ts []nameTarget
	for i, s := range specs {
		if s.IsName {
			ts = append(ts, nameTarget{specIdx: i, host: s.Host, port: s.Port})
		}
	}
	return ts
}

// pathFamilies records which IP families the local path sockets can source traffic from. A path
// binds a socket whose family is fixed by its source_addr (bind.listenOnDevice): a v4 source can
// only reach v4 endpoints, a v6 source only v6. Re-resolution filters a resolved record set to the
// families at least one local path can source, so a lookup answer of an unreachable family (an
// AAAA-only answer on a v4-only edge) never repoints the bond at an address no path can carry.
type pathFamilies struct {
	v4 bool
	v6 bool
}

// allows reports whether a resolved address's family can be sourced by at least one local path.
func (f pathFamilies) allows(a netip.Addr) bool {
	if a.Unmap().Is4() {
		return f.v4
	}
	return f.v6
}

// pathFamiliesFromPaths derives the usable-family set from the configured paths. Config validation
// guarantees every path carries a valid source_addr (Config.normalize), so each path contributes
// exactly one family and the result is non-empty for any peer that reaches this controller.
func pathFamiliesFromPaths(paths []config.Path) pathFamilies {
	var f pathFamilies
	for _, p := range paths {
		if p.SourceAddr.Unmap().Is4() {
			f.v4 = true
		} else {
			f.v6 = true
		}
	}
	return f
}

// step is one evaluation at the probe cadence: it first services a liveness-loss edge (an
// out-of-band re-resolve of the active spec the instant every path goes DOWN), then, if the
// scheduled poll is due, re-resolves every hostname spec and re-arms the next poll at the
// TTL-clamped cadence. It is cheap in the steady state (a liveness read plus a clock compare)
// and performs its network lookups off any lock, so a periodic loop may call it at probe cadence.
func (r *resolution) step() {
	r.checkLivenessLoss()

	r.mu.Lock()
	due := !r.clock.Now().Before(r.nextPollAt)
	r.mu.Unlock()
	if !due {
		return
	}
	delay := r.pollAll()
	r.mu.Lock()
	r.nextPollAt = r.clock.Now().Add(delay)
	r.mu.Unlock()
}

// pollAll re-resolves every tracked hostname spec once and returns the delay until the next
// scheduled poll: the configured poll interval, CLAMPED to the smallest positive TTL reported by
// any spec whose transport exposes one (DoH/DoT ttlOk) — the Q31 TTL nice-to-have — so a record
// about to expire is re-checked no later than its TTL. A lookup failure or empty result leaves
// that spec's expansion untouched (RETENTION INVARIANT) and does not shorten the delay.
func (r *resolution) pollAll() time.Duration {
	delay := r.pollInterval
	for _, t := range r.targets {
		if d := r.clampPollDelay(r.resolveTarget(t)); d < delay {
			delay = d
		}
	}
	return delay
}

// clampPollDelay returns the scheduled poll interval clamped DOWN to a positive, meaningful TTL,
// so a record about to expire is re-checked no later than its TTL (Q31 TTL clamp). A lookup that
// exposes no TTL (ttlOk=false — system resolver) or a non-positive one leaves the full interval.
// It is applied uniformly to the scheduled poll re-arm AND the out-of-band liveness-loss re-arm,
// so the clamp invariant holds on BOTH paths — including the hub-loss window where record
// freshness matters most.
func (r *resolution) clampPollDelay(minTTL time.Duration, ttlOk bool) time.Duration {
	if ttlOk && minTTL > 0 && minTTL < r.pollInterval {
		return minTTL
	}
	return r.pollInterval
}

// resolveTarget performs one context-bounded lookup for a single hostname spec and, on a
// non-empty SUCCESS, hands the family-filtered, ordered record set to hubFailover.updateResolution (which
// suppresses a no-op change and repoints only on an actual active-IP change). It returns the
// lookup's minimum TTL and whether that TTL is meaningful, for the caller's clamp.
//
// RETENTION INVARIANT (D46): on ANY failure (error/timeout/NXDOMAIN) OR an empty ordered set
// (NODATA / a CNAME-only chain / a family the local paths cannot use), it publishes NOTHING —
// the spec keeps its previous expansion — and reports (0, false). A previously-resolved hostname
// is therefore never collapsed to empty, which is exactly the condition that could strand the
// bond under hubFailover's total<2 guard.
func (r *resolution) resolveTarget(t nameTarget) (time.Duration, bool) {
	ctx, cancel := r.lookupContext()
	defer cancel()

	addrs, minTTL, ttlOk, err := r.resolver.Lookup(ctx, t.host)
	if err != nil {
		r.log.Debug("dns re-resolution: lookup failed; retaining last-good records",
			"host", t.host, "spec_index", t.specIdx, "error", err.Error())
		return 0, false
	}
	eps := orderAddrPorts(addrs, t.port, r.families)
	if len(eps) == 0 {
		r.log.Debug("dns re-resolution: no usable record for local path families; retaining last-good records",
			"host", t.host, "spec_index", t.specIdx)
		return 0, false
	}
	r.fo.updateResolution(t.specIdx, eps)
	return minTTL, ttlOk
}

// checkLivenessLoss fires the out-of-band re-resolve of the ACTIVE spec on the DOWN edge — the
// tick at which every path to the active concentrator first reads DOWN. It coordinates with
// hubFailover purely through the shared liveness read (allPathsDown) and active-spec identity
// (activeSpecIndex): a hub-loss advance and a liveness-loss re-resolve therefore derive from one
// detector. The trigger is edge-triggered so a sustained outage re-resolves once here (the poll
// loop covers the rest) rather than on every tick.
func (r *resolution) checkLivenessLoss() {
	down := r.fo.allPathsDown()

	r.mu.Lock()
	edge := down && !r.lastAllDown
	r.lastAllDown = down
	r.mu.Unlock()
	if !edge {
		return
	}

	active := r.fo.activeSpecIndex()
	t, ok := r.targetFor(active)
	if !ok {
		// The active spec is a literal (or there is no active entry yet): nothing to re-resolve.
		return
	}
	r.log.Warn("dns re-resolution: all paths to active concentrator down; out-of-band re-resolve",
		"host", t.host, "spec_index", t.specIdx)
	minTTL, ttlOk := r.resolveTarget(t)

	// An out-of-band re-resolve subsumes the scheduled poll for this instant; push the next poll
	// out — but re-arm at the TTL-CLAMPED delay, not a blind full interval, so a short-TTL record
	// is re-checked no later than its TTL on exactly the hub-loss path where freshness matters most.
	r.mu.Lock()
	r.nextPollAt = r.clock.Now().Add(r.clampPollDelay(minTTL, ttlOk))
	r.mu.Unlock()
}

// targetFor returns the nameTarget owning specIdx, or ok=false when no tracked hostname spec has
// that index (a literal active spec, or specIdx == -1 for no active entry).
func (r *resolution) targetFor(specIdx int) (nameTarget, bool) {
	if specIdx < 0 {
		return nameTarget{}, false
	}
	for _, t := range r.targets {
		if t.specIdx == specIdx {
			return t, true
		}
	}
	return nameTarget{}, false
}

// lookupContext returns a context bounded by the configured per-lookup timeout (a non-positive
// timeout yields an unbounded context — the caller's config validation keeps it positive).
func (r *resolution) lookupContext() (context.Context, context.CancelFunc) {
	if r.timeout <= 0 {
		return context.WithCancel(context.Background())
	}
	return context.WithTimeout(context.Background(), r.timeout)
}

// orderAddrPorts turns a resolver's mixed A/AAAA record set into the ordered, deduplicated
// []netip.AddrPort a failoverSpec expansion requires: IPv4 records first (in resolver order),
// then IPv6, each paired with the spec's port. The order is DETERMINISTIC so an unchanged DNS
// answer produces a byte-identical expansion tick after tick — the precondition for
// updateResolution's active-AddrPort survival check to suppress a no-op repoint. Invalid or
// duplicate addrs, and addrs of a family NO local path can source (fams), are dropped — so a
// record set that filters down to nothing yields an empty expansion the caller retains last-good on.
func orderAddrPorts(addrs []netip.Addr, port uint16, fams pathFamilies) []netip.AddrPort {
	seen := make(map[netip.Addr]struct{}, len(addrs))
	var v4, v6 []netip.AddrPort
	for _, a := range addrs {
		if !a.IsValid() || !fams.allows(a) {
			continue
		}
		canon := a.Unmap()
		if _, dup := seen[canon]; dup {
			continue
		}
		seen[canon] = struct{}{}
		ap := netip.AddrPortFrom(canon, port)
		if canon.Is4() {
			v4 = append(v4, ap)
		} else {
			v6 = append(v6, ap)
		}
	}
	return append(v4, v6...)
}

// startResolutionLoop launches the re-resolution goroutine: it calls step every interval until
// the returned stopper is invoked (idempotent). It mirrors startHubFailoverLoop — a wall-clock
// ticker at the probe cadence driving a decision method whose every timing choice runs through
// the injected Clock, so a test drives step directly against a fake clock and never starts this
// goroutine. It is a no-op (no-op stopper) for a controller with no hostname specs to track or a
// non-positive interval.
func (r *resolution) startResolutionLoop(interval time.Duration) (stop func()) {
	if len(r.targets) == 0 || interval <= 0 {
		return func() {}
	}
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				r.step()
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(done) }) }
}
