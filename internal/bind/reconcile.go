package bind

import (
	"fmt"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/7mind/wanbond/internal/sched"
	"github.com/7mind/wanbond/internal/telemetry"
)

// DefaultReconcileInterval is the cadence at which StartReconcileLoop re-attempts the
// bind of the paths a tolerant Open (T51) left DEFERRED. It is a seconds-scale poll,
// not the sub-second probe cadence: a deferred path is a boot-time transient (a 5G
// modem acquiring a DHCP lease, Starlink emerging from an obstruction), an event that
// resolves on a human/network timescale, so a 1s cadence bounds the promote latency
// tightly while the tick — a single mutex-guarded length check when nothing is
// deferred (the steady state) — stays negligible.
const DefaultReconcileInterval = 1 * time.Second

// defaultDeferredListen is the production bind for a reconciled deferred path: it pins
// the path's source IP on the bind's shared port, identical to AddPath's runtime bind,
// unless dev is set (a forced BindModeDevice — I5), in which case it device-binds like
// Open/AddPath (falling back to source-IP binding on an unresolvable interface or a
// failed setsockopt — see listenPath). It returns EADDRNOTAVAIL for as long as no
// interface holds the address — which is exactly the signal reconcileDeferred reads to
// keep the path deferred and retry — and succeeds once the address becomes assignable.
func defaultDeferredListen(src netip.Addr, port uint16, dev string) (*net.UDPConn, error) {
	return listenPath(src, port, dev)
}

// StartReconcileLoop launches the background deferred-path reconciler (T55): every
// interval it re-attempts the bind of the paths a tolerant Open (T51) left DEFERRED
// (a well-formed source_addr that was not yet assignable at boot) and PROMOTES any
// that now binds to a live path — into m.paths, the scheduler, and its own reader —
// so a source_addr that becomes assignable after boot (a 5G modem that finally got
// its DHCP lease) is brought into the running bond WITHOUT a restart. It stops when
// the returned stopper is invoked (idempotent), so the daemon shuts it down before
// Close with no goroutine leak — the symmetric companion to StartProbeLoop. It is a
// no-op (returning a no-op stopper) on a bind without the probe transport (which never
// defers — Open makes every bind error fatal there) or interval <= 0.
//
// Mechanism (design decision): a bounded periodic POLL, not netlink route/addr
// subscription. Event-driven netlink (vishvananda/netlink AddrSubscribe) would add a
// new external dependency this module does not carry, and the deferred set is normally
// EMPTY (every path bound at boot), so the poll degenerates to a cheap length check;
// when a path is deferred, re-attempting a non-blocking ListenUDP once per interval
// promotes it within a bounded window. A fixed interval rather than a backoff: the
// deferred window is a bounded boot-time transient, so a steady cadence bounds the
// promote latency without a backoff schedule's complexity.
//
// The loop owns its own done channel (like StartProbeLoop) rather than riding the
// per-Open recvClosed: it is a DEVICE-lifecycle goroutine started after the first Open
// and stopped before Close, and it operates on whatever m.deferred the current Open
// span holds (a Close→Open rebuilds m.deferred; reconcileDeferred is a no-op while the
// bind is closed). Its cadence uses a wall-clock ticker because it is production timing
// glue; the bind mutation it performs is exercised directly in tests against the
// injected listen seam, so a test never starts this goroutine.
func (m *Multipath) StartReconcileLoop(interval time.Duration) (stop func()) {
	if m.newProber == nil || interval <= 0 {
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
				m.reconcileDeferred()
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(done) }) }
}

// reconcileDeferred is one background reconcile step (T55): it re-attempts the bind of
// every DEFERRED path and promotes any that now binds to a live path, leaving the rest
// deferred for the next tick. A path that still cannot bind — EADDRNOTAVAIL (address
// still not assignable) OR any other transient bind error — stays deferred and is
// retried; a bind fault never becomes fatal to the RUNNING bond (the tunnel is already
// up on the paths that bound). It runs entirely under m.mu, so it serializes with
// Send/Close/AddPath/RemovePath and the path slice + scheduler mutate together, and it
// is a no-op on a CLOSED bind (len(m.paths)==0) — so it races a concurrent Close
// harmlessly (Close either ran first, and this no-ops, or runs after and joins the
// reader this step just spawned via readersWG) — or when nothing is deferred (the
// steady state, a single length check).
func (m *Multipath) reconcileDeferred() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.paths) == 0 || len(m.deferred) == 0 {
		return // closed, or nothing to promote
	}
	// A deferred path exists only under the runtime path-down model tolerance requires
	// (probe transport + DynamicScheduler); Open never defers without it, so this
	// assertion holds. Guard defensively rather than type-assert-panic on the hot lock.
	// (Each peer's OWN scheduler is independently type-asserted inside the promotion
	// fan-out below — attachPeerPathLocked, via attachSharedPathLocked — so this check is
	// only the primary's early opt-out: a primary without dynamic membership support never
	// defers in the first place, per Open's tolerateDefer gate.)
	if _, ok := m.scheduler.(sched.DynamicScheduler); !ok {
		return
	}
	// In-place filter: keep the paths that did NOT promote this tick. kept aliases the
	// deferred backing array, and its length never exceeds the loop index, so appending
	// a still-deferred entry never clobbers one not yet read (the standard filter idiom).
	kept := m.deferred[:0]
	for _, dp := range m.deferred {
		dev := m.resolveDeviceBind(dp.def.SourceAddr, dp.def.Bind)
		c, err := m.deferredListen(dp.def.SourceAddr, m.openPort, dev)
		if err != nil {
			kept = append(kept, dp) // still not assignable (or a transient fault): retry next tick
			continue
		}
		if err := m.promoteDeferredLocked(dp, c); err != nil {
			// The bind succeeded but promotion did not (a scheduler/path index skew, or a
			// codec build error): close the fresh socket and keep the path deferred so the
			// next tick retries cleanly, rather than leaking the socket or half-admitting.
			_ = c.Close()
			kept = append(kept, dp)
		}
	}
	m.deferred = kept
}

// promoteDeferredLocked turns a just-bound deferred path into a LIVE one under m.mu. It fans
// the promotion out to EVERY bound peer (mirroring AddPath's attachSharedPathLocked fan-out,
// D42): a deferred def is ALREADY in the durable membership (m.defs) and every bound peer's
// p.probers already carries that peer's OWN index-aligned prober for it (the AddPath/Open
// admission that first recorded it fanned that far already) — so promotion REUSES each peer's
// existing prober (located by dp's index in m.defs) rather than minting a fresh one, giving
// every peer a receive-demux VIEW of the freshly-bound socket and a scheduler entry, not just
// the primary. Pre-fix, only the primary got a view/scheduler entry, leaving every non-primary
// concentrator peer's frames on this socket un-demuxed and its scheduler without an entry for
// the path until the next Close→Open. It mirrors AddPath's admission MINUS touching the
// durable membership (already in place) and MINUS minting fresh probers (reused instead), so
// the caller need only drop dp from m.deferred on success. On any peer's failure it rolls the
// WHOLE fan-out back (mirroring attachSharedPathLocked) and returns the error, leaving
// m.shared, every peer's paths, and every peer's scheduler as they were found.
func (m *Multipath) promoteDeferredLocked(dp deferredPath, c *net.UDPConn) error {
	// Large SO_RCVBUF, best-effort (kernel-capped, needs no privilege) — as in Open/AddPath.
	_ = c.SetReadBuffer(socketRecvBuffer)

	// Locate dp's index in the durable membership so each peer's ALREADY index-aligned
	// prober (p.probers[defIdx]) can be reused instead of minting a fresh one. It MUST be
	// found — a deferred path is by construction always present in m.defs — but a missing
	// index would corrupt id-stamp continuity if silently tolerated, so fail loudly instead.
	defIdx := -1
	for i := range m.defs {
		if m.defs[i].Name == dp.def.Name {
			defIdx = i
			break
		}
	}
	if defIdx < 0 {
		return fmt.Errorf("bind: promote deferred path %q: not present in the durable membership (m.defs) — wiring defect", dp.def.Name)
	}

	// Resolve every bound peer's OWN prober for this path up front (fail fast on any
	// divergence rather than partially fanning out and rolling back mid-attach).
	probers := make([]*telemetry.Prober, len(m.peers))
	for pi, p := range m.peers {
		if p.probers == nil || defIdx >= len(p.probers) {
			return fmt.Errorf("bind: promote deferred path %q: peer %q prober set is missing an entry at index %d — per-peer prober fan-out desync (wiring defect)", dp.def.Name, p.name, defIdx)
		}
		probers[pi] = p.probers[defIdx]
	}

	// Reuse the boot prober's IMMUTABLE stamp for the DATA-frame path-id, exactly as Open
	// and AddPath do, so DATA and PROBE agree on the wire and the promoted path is never
	// renumbered. probers[0] is the primary's — the same dp.prober the deferred record held.
	shared := &sharedPathState{name: dp.def.Name, id: probers[0].PathID(), src: dp.def.SourceAddr, conn: c}

	// FAN-OUT (single owner, shared with AddPath): instantiate the per-(peer,path) state for
	// EVERY currently-bound peer, reusing each peer's resolved prober and admitting it to
	// that peer's scheduler. attached[k] is m.peers[k]'s view of the freshly-bound socket. A
	// failure in any peer rolls back every peer already attached, so a partial fan-out never
	// leaks a half-admitted path.
	attached, err := m.attachSharedPathLocked(shared, dp.def, shared.id, probers)
	if err != nil {
		return err
	}

	m.shared = append(m.shared, shared)

	// One reader per SHARED socket (mirrors AddPath): the primary's view feeds it, and
	// demuxInbound resolves the actual owning peer per-datagram once the socket has >1 view.
	// readersWG tracks it so a subsequent Close joins it (no goroutine leak).
	m.readersWG.Add(1)
	go m.readLoop(attached[0], m.deliverSignal)
	return nil
}
