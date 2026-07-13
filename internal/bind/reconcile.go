package bind

import (
	"fmt"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/7mind/wanbond/internal/frame"
	"github.com/7mind/wanbond/internal/sched"
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
// the path's source IP on the bind's shared port, identical to AddPath's runtime bind
// (net.ListenUDP on the source). It returns EADDRNOTAVAIL for as long as no interface
// holds the address — which is exactly the signal reconcileDeferred reads to keep the
// path deferred and retry — and succeeds once the address becomes assignable.
func defaultDeferredListen(src netip.Addr, port uint16) (*net.UDPConn, error) {
	laddr := &net.UDPAddr{IP: net.IP(src.AsSlice()), Port: int(port)}
	return net.ListenUDP("udp", laddr)
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
	dyn, ok := m.scheduler.(sched.DynamicScheduler)
	if !ok {
		return
	}
	// In-place filter: keep the paths that did NOT promote this tick. kept aliases the
	// deferred backing array, and its length never exceeds the loop index, so appending
	// a still-deferred entry never clobbers one not yet read (the standard filter idiom).
	kept := m.deferred[:0]
	for _, dp := range m.deferred {
		c, err := m.deferredListen(dp.def.SourceAddr, m.openPort)
		if err != nil {
			kept = append(kept, dp) // still not assignable (or a transient fault): retry next tick
			continue
		}
		if err := m.promoteDeferredLocked(dyn, dp, c); err != nil {
			// The bind succeeded but promotion did not (a scheduler/path index skew, or a
			// codec build error): close the fresh socket and keep the path deferred so the
			// next tick retries cleanly, rather than leaking the socket or half-admitting.
			_ = c.Close()
			kept = append(kept, dp)
		}
	}
	m.deferred = kept
}

// promoteDeferredLocked turns a just-bound deferred path into a LIVE one under m.mu. It
// wraps the fresh socket in a pathState that REUSES the deferred path's preserved boot
// prober — so the promoted path keeps its reserved path-id STAMP (no renumber, no
// collision with the peer's per-path reflector state), the continuity T51 preserved for
// exactly this step — seeds its remote from the config dest_addr or the learned default,
// appends it to m.paths, admits its prober to the scheduler as the new lowest-priority
// path, and spawns its Bind-owned reader. It mirrors AddPath's admission MINUS minting a
// prober and MINUS touching the durable membership (a deferred path is ALREADY in
// m.defs/m.probers and its id-stamp is ALREADY reserved in m.nextPathID by Open), so the
// caller need only drop it from m.deferred on success. It rolls a failed admission back
// completely and returns the error, leaving m.paths and the scheduler as it found them.
func (m *Multipath) promoteDeferredLocked(dyn sched.DynamicScheduler, dp deferredPath, c *net.UDPConn) error {
	// Large SO_RCVBUF, best-effort (kernel-capped, needs no privilege) — as in Open/AddPath.
	_ = c.SetReadBuffer(socketRecvBuffer)
	codec, err := frame.NewCodec(m.psk)
	if err != nil {
		return err // programmer error (psk validated at construction); never hit in practice
	}
	// Reuse the boot prober's IMMUTABLE stamp for the DATA-frame path-id, exactly as Open
	// and AddPath do, so DATA and PROBE agree on the wire and the promoted path is never
	// renumbered. The deferred-path machinery is single-peer today (the concentrator fan-out
	// of a promoted deferred path is a later G4 task), so this attaches the primary peer's
	// view over the freshly-bound shared socket.
	shared := &sharedPathState{name: dp.def.Name, id: dp.prober.PathID(), src: dp.def.SourceAddr, conn: c}
	ps := &peerPathState{sharedPathState: shared, peer: m.peerState, codec: codec, prober: dp.prober}
	switch {
	case dp.def.DestAddr.IsValid():
		ps.setRemote(dp.def.DestAddr)
	case m.hasDefaultRemote:
		ps.setRemote(m.defaultRemote)
	}
	// Append to the shared socket list + the peer's path slice, then admit the prober to the
	// scheduler as the new tail; the peer's path slice and its scheduler are index-aligned,
	// so the scheduler's returned index must equal the new path's slice index. A mismatch
	// would mis-route datagrams, so fail loudly and roll back.
	m.shared = append(m.shared, shared)
	m.paths = append(m.paths, ps)
	schedIdx, err := dyn.AddPath(ps.prober)
	if err != nil {
		m.paths = m.paths[:len(m.paths)-1]
		m.shared = m.shared[:len(m.shared)-1]
		return err
	}
	if schedIdx != len(m.paths)-1 {
		bindIdx := len(m.paths) - 1
		_ = dyn.RemovePath(schedIdx)
		m.paths = m.paths[:len(m.paths)-1]
		m.shared = m.shared[:len(m.shared)-1]
		return fmt.Errorf("bind: scheduler/path index skew after deferred promote: sched=%d bind=%d", schedIdx, bindIdx)
	}
	// Spawn the promoted path's Bind-owned reader on the CURRENT Open span's delivery
	// signal; readersWG tracks it so a subsequent Close joins it (no goroutine leak).
	m.readersWG.Add(1)
	go m.readLoop(ps, m.deliverSignal)
	return nil
}
