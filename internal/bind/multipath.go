package bind

import (
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/amnezia-vpn/amneziawg-go/v3/conn"

	"github.com/7mind/wanbond/internal/bond"
	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/frame"
	"github.com/7mind/wanbond/internal/log"
	"github.com/7mind/wanbond/internal/reseq"
	"github.com/7mind/wanbond/internal/telemetry"
)

// socketRecvBuffer is the SO_RCVBUF requested on every per-path socket, matching
// wireguard-go's StdNetBind (~7 MiB). A large receive buffer absorbs the bursts a
// bonded, reordered flow delivers while the userspace engine catches up; the
// pass-through P0 Bind used the OS default and lost datagrams under load (P0
// findings §1/§2). The kernel silently caps the request at net.core.rmem_max, so
// the effective size may be smaller on an untuned host — best-effort by design.
const socketRecvBuffer = 7 << 20

// multipathBatchSize is the buffer-vector capacity accepted by Send and exposed
// to the engine's receive workers. The vendored engine selects the maximum of
// this value and the TUN batch size, so the Bind must honor its full batch.
const multipathBatchSize = conn.IdealBatchSize

// maxDatagram bounds a single received outer datagram. It comfortably exceeds a
// full-MTU inner packet plus every outer/WG/amnezia-junk overhead.
const maxDatagram = 65535

// Receive-resequencer tuning (T18), sized against P0 findings §6.
//
// resequencerWindow is the maximum span of outer-seq positions the receive
// resequencer buffers while it waits for an out-of-order frame. It must cover
// the datagrams that arrive while a lost one is repaired: at 600 Mbit/s and a
// 250 ms repair, 12500. At 2048 a gap was abandoned, a loss to TCP, as soon as
// the flow exceeded about 10000 datagrams a second, which held a TCP transfer
// near 100 Mbit/s on 300+300 Mbit/s links. The engine's inner RFC 6479
// anti-replay window (131008 messages in the vendored engine; 8128 upstream,
// docs/p0-findings.md §6) sits 4x above it, so held frames released behind
// real-time ones that bypassed them stay inside the inner filter's tolerance.
//
// resequencerTimeout bounds how long a head-of-line-blocked run is held for a
// missing (presumed-lost) lower frame before that gap is skipped and the run
// released. It caps the added latency — and the stall a slow/lossy path can
// impose on the whole bond (P0 §7 head-of-line concern) — to a few multiples of
// a Starlink RTT (~45 ms) rather than holding a gap forever.
const (
	resequencerWindow  = 32768
	resequencerTimeout = 250 * time.Millisecond
)

// defaultMaxDemuxSources caps the source->peer demux map (peerBySource), the
// PROVISIONAL/unbound-source tracking state whose growth an attacker probes at
// bootstrap (Q26/Q27). It is sized SEPARATELY from the steady-state peer set
// (m.peersByName, bounded by the static configured [[wireguard.peers]]): a source
// AddrPort enters this map only on an authenticated PROBE (T88). The map is keyed by
// the full source netip.AddrPort (address+port, D47) — NOT the bare address — so two
// peers behind ONE public IP (CGNAT) bind to distinct entries and demux independently.
//
// This GLOBAL cap is the outer bound; within it a PER-PEER quota (maxDemuxSources /
// len(peers), floor 1, D49) is enforced so one party holding ONE valid psk that floods
// spoofed sources to its OWN peer exhausts only ITS OWN quota and never starves another
// peer's bootstrap PROBE (Q27(1) cross-peer isolation). Two eviction/drop regimes apply
// to a NEW source AddrPort:
//   - SAME-peer (roam port-churn): a peer already at its per-peer quota that authenticates
//     a NEW AddrPort for ITSELF evicts its OWN oldest binding (LRU within the peer) to admit
//     it — a live roaming peer is NEVER dropped and its footprint never grows past quota, so
//     it can never evict ANOTHER peer's slot (never-evict-live w.r.t. others holds).
//   - CROSS-peer (drop-on-exhaustion): a peer BELOW its quota whose NEW source would grow the
//     map past the GLOBAL cap is refused — it may not steal another peer's headroom. Bootstrap
//     degrades; WG retransmits re-drive it once a slot frees.
//
// An already-bound (live) source is NEVER evicted by another peer. Roam/re-affirm of an
// already-present AddrPort (T90) does not grow the map, so it is never blocked by the cap. A
// dead peer's bindings are reclaimed on teardown (TearDownPeer), freeing slots. The default is
// generous relative to any realistic concentrator's peer×roam-churn count yet bounds the flood
// surface.
const defaultMaxDemuxSources = 1024

// Forced-device-bind fallback log messages (D53). A path configured bind="device" is
// the operator's explicit, roam-surviving choice (T16): its socket keeps sending from
// the interface's NEW address across a mid-session re-roam, unlike a source-IP-pinned
// socket, which fails once the old address is removed. Pre-D53 the two ways that choice
// silently degrades to source-IP pinning — an unresolvable interface (layer a) and a
// failing SO_BINDTODEVICE setsockopt (layer b) — were unlogged, so an operator could run
// roam-fragile for the whole session with no signal. Both fallback-succeeded messages
// name the path and the (possibly empty) resolved interface so the two layers are
// distinguishable in the log.
//
// D53 round 2 (FIX 1 + FIX 2) refines this: forcedDeviceUnresolvableWarn now fires ONLY
// once the source-IP-pin fallback it describes has ACTUALLY materialized a working
// socket (err == nil at the call site) — claiming the fallback while the path stays
// DEFERRED (no socket exists at all) would be a false report. When the interface stays
// unresolved and the fallback bind itself also fails, forcedDeviceStillDeferredWarn
// reports that accurate, non-fallback fact instead, deduplicated per condition-
// transition (deferredPath.warnedUnresolvable) so a persistently-unresolvable deferred
// path — a normal boot-time transient reconcileDeferred retries at 1 Hz — WARNs once for
// the whole deferral window rather than once per tick.
const (
	forcedDeviceUnresolvableWarn = "bind: forced device bind (bind=\"device\") has no resolvable interface for this path's source address; falling back to source-IP pinning (roam survival across an address change is lost)"
	// forcedDeviceStillDeferredWarn is the accurate, non-fallback-claiming counterpart to
	// forcedDeviceUnresolvableWarn for when the source-IP-pin fallback attempt ITSELF
	// fails too (e.g. the source_addr is also not yet assignable): the path stays
	// deferred rather than falling back to anything, so no fallback claim is made.
	forcedDeviceStillDeferredWarn = "bind: forced device bind (bind=\"device\") has no resolvable interface for this path's source address; the source-IP-pin fallback attempt also did not bind, so the path stays deferred (roam survival across an address change remains lost until it resolves)"
	forcedDeviceSetsockoptWarn    = "bind: forced device bind (bind=\"device\") interface bind (SO_BINDTODEVICE) failed; falling back to source-IP pinning (roam survival across an address change is lost)"
	// autoDeviceSetsockoptInfo covers the PRE-EXISTING silent CAP/setsockopt fallback for
	// an AUTO-selected (not operator-forced) device bind: informational, not a WARN,
	// because the operator never asked for the roam-survival property BindModeAuto only
	// opportunistically grants.
	autoDeviceSetsockoptInfo = "bind: auto-selected device bind (SO_BINDTODEVICE) failed; falling back to source-IP pinning"
)

var (
	// ErrNoHealthyPath is exported (I4) so the device-package engineLogger adapter can
	// errors.Is-match it against the engine's wrapped Errorf args to gate the startup
	// no-healthy-path warmup coalescing without string-matching the log message.
	ErrNoHealthyPath = errors.New("bind: no healthy path with a known remote endpoint")
	errClosed        = net.ErrClosed
)

// sharedPathState is the per-SOCKET state of one configured uplink, SHARED across
// every peer bound beneath the Bind: its stable path-id, source address, and the
// source-bound UDP socket (drained by exactly one Bind-owned readLoop). A concentrator
// front-ends many peers on the SAME socket, so this identity is owned once per socket
// and referenced by every peer's per-(peer,path) view (peerPathState). On the single-peer
// edge/hub there is exactly one peer, so each shared path has exactly one peerPathState.
// The deferred-path machinery (deferredPath, m.deferred) likewise operates on this shared
// socket layer — a runtime add/remove of a shared path fans the per-(peer,path) state out
// to every bound peer (see attachSharedPathLocked / RemovePath).
type sharedPathState struct {
	name string
	id   uint8
	src  netip.Addr
	conn *net.UDPConn

	// writeMu makes UDP writes generation-scoped. Retirement closes admission
	// before waiting writes, so WaitGroup Add cannot race Wait.
	writeMu      sync.Mutex
	writes       sync.WaitGroup
	writesClosed bool
	// writeUDP is a test seam installed before a generation becomes active. Nil
	// uses conn.WriteToUDPAddrPort.
	writeUDP  func([]byte, netip.AddrPort) (int, error)
	closeOnce sync.Once
	closeErr  error
	// bindMode is the path's configured/effective bind mode; boundDevice is the
	// resolved SO_BINDTODEVICE interface it actually device-bound to ("" when
	// source-IP-pinned). Both are set once at socket creation and IMMUTABLE for the
	// socket's life, so PeerSnapshots reads them lock-free after releasing m.mu
	// (G21 monitoring, surfaced via PathTraffic; the value-wiring into the monitor
	// snapshot is T220).
	bindMode    config.BindMode
	boundDevice string

	// views is the per-peer VIEW set of THIS shared socket — one peerPathState per bound
	// peer — published copy-on-write through an atomic.Pointer so the single Bind-owned
	// readLoop demuxes an inbound datagram to its owning peer's view WITHOUT m.mu on the
	// receive hot path (the same lock-free-publish discipline peersView/resequencer use).
	// It is (re)published under m.mu at every fan-out site (Open / attachPeerPathLocked /
	// deferred promote, and each concentrator peer bind). On the single-peer edge/hub it
	// holds exactly one entry (the primary's view), which handleInbound reads as "no demux
	// needed" — the byte-identical fast path. len(views)>1 marks a shared concentrator
	// socket whose datagrams must be source-demuxed to the owning peer (T88).
	views atomic.Pointer[[]*peerPathState]
}

func (sp *sharedPathState) writeToUDPAddrPort(payload []byte, remote netip.AddrPort) (int, error) {
	sp.writeMu.Lock()
	if sp.writesClosed {
		sp.writeMu.Unlock()
		return 0, errClosed
	}
	sp.writes.Add(1)
	sp.writeMu.Unlock()
	defer sp.writes.Done()
	if sp.writeUDP != nil {
		return sp.writeUDP(payload, remote)
	}
	return sp.conn.WriteToUDPAddrPort(payload, remote)
}

func (sp *sharedPathState) stopWrites() {
	sp.writeMu.Lock()
	sp.writesClosed = true
	sp.writeMu.Unlock()
}

func (sp *sharedPathState) waitWrites() {
	sp.writes.Wait()
}

func (sp *sharedPathState) closeSocket() error {
	sp.closeOnce.Do(func() {
		if sp.conn != nil {
			sp.closeErr = sp.conn.Close()
		}
	})
	return sp.closeErr
}

// addViewLocked publishes pp as a per-peer view of this shared socket for the lock-free
// receive demux (T88). Copy-on-write: it republishes a NEW slice with pp appended, so a
// readLoop mid-iteration over the previously-published snapshot is never disturbed. The
// caller holds m.mu (every fan-out site does), which serializes writers; readers Load
// without a lock.
func (sp *sharedPathState) addViewLocked(pp *peerPathState) {
	old := sp.views.Load()
	var next []*peerPathState
	if old != nil {
		next = make([]*peerPathState, len(*old), len(*old)+1)
		copy(next, *old)
	}
	next = append(next, pp)
	sp.views.Store(&next)
}

// peerPathState is one peer's per-(peer,path) VIEW of a shared uplink: that peer's own
// decode Codec (each path receives on its own goroutine, so the Codec's scratch is never
// shared), the peer's own learned/configured return remote, the peer's own probe
// initiator for this path, and the peer's own per-path OUTER-wire byte counters. The
// remote is either configured (edge dest_addr / peer endpoint) or LEARNED from inbound
// traffic (concentrator) — but it lives strictly BELOW the engine's single virtual
// endpoint, so the engine never sees this per-(peer,path) bookkeeping churn.
//
// It EMBEDS its *sharedPathState so the socket identity (name/id/src/conn) is reached
// transparently, and back-references the owning peerState so a receive handler resolves
// exactly that peer's resequencer, reflector and transport.
type peerPathState struct {
	*sharedPathState
	peer  *peerState
	codec *frame.Codec
	// prober is this (peer,path)'s own probe initiator (nil when the bind runs without the
	// probe transport). It is set at path creation and immutable for the path's life,
	// so the Bind-owned receive goroutine reaches it via the peerPathState it already
	// holds — NOT through a shared, dynamically-mutated probers slice — which is
	// what keeps echo handling race-free while paths are added/removed at runtime (T30).
	prober *telemetry.Prober

	// pmtuProbe is this (peer,path)'s PMTU echo-await backend (T227, defect D88),
	// constructed alongside prober when the probe transport is present and nil otherwise.
	// The receive path routes a matched padded-probe echo to it via NotifyEcho (DECOUPLED
	// from HandleEcho's anti-replay verdict); the per-path PMTUDiscovery that device.Up
	// builds over the PRIMARY peer drives its ProbePMTU. Like prober it is set at path
	// creation and immutable, so the receive goroutine reaches it lock-free via the
	// peerPathState it already holds.
	pmtuProbe *telemetry.EchoAwaitProbe
	// pendingPMTU is the at-most-one padded probe waiting to consume this path's
	// next eligible probe-cadence slot. The slot after a PMTU attempt is reserved
	// for ordinary liveness, so consecutive search failures cannot suppress it.
	// PMTU discovery is single-goroutine per path, so a second pending request is an
	// invariant violation, not a queue to grow. generatedProbeMu also closes the
	// request atomically with path teardown.
	generatedProbeMu     sync.Mutex
	pendingPMTU          *generatedProbeRequest
	generatedProbeClosed bool
	ordinaryProbeDue     bool

	// txBytes/rxBytes are cumulative OUTER-wire byte counters for this (peer,path), the
	// per-path traffic accounting the /metrics exposition reports (T23). Both are
	// TRUE-WIRE-VOLUME counters: txBytes counts every outer datagram this path actually
	// writes to its socket — the transport's frames, PROBE frames emitted by emitProbes,
	// and PROBE echoes reflected back by dispatchInbound — each counted only once the
	// write returns a nil error; rxBytes counts every outer datagram this path's
	// readLoop receives. A healthy idle path still emits and echoes probes, so its
	// txBytes keeps advancing (D48). They are atomics so the send/receive/probe hot
	// paths increment them WITHOUT taking m.mu (lock-free) from whichever goroutine
	// performs the write, and the scrape/snapshot path reads them with a plain atomic
	// Load.
	txBytes atomic.Uint64
	rxBytes atomic.Uint64

	// probeSendErrors counts unexpected socket write failures for locally-originated
	// ordinary and PMTU PROBE attempts. Expected PMTU EMSGSIZE is excluded: discovery
	// consumes it as a too-large verdict. An unexpected PMTU failure is counted and
	// returned to discovery; an ordinary failure is counted then discarded so the
	// cadence continues across other paths. Incremented lock-free (no m.mu).
	probeSendErrors atomic.Uint64

	// socketWriteErrors counts the transport's datagram writes the UDP socket
	// refused. Per-(peer,path) and lock-free.
	socketWriteErrors atomic.Uint64

	mu sync.Mutex
	// remotes is the per-SENDER-PATH return-address table (T246, defect D94): one entry
	// per sender-stamped path id seen in this view's authenticated probe plane, replacing
	// the pre-D94 single scalar whose last-prober-wins overwrite flapped the concentrator's
	// downlink destination across the edge's WANs at probe cadence. FRESHNESS is owned by
	// the authenticated probe plane exclusively — a probe request (concentrator side,
	// stamped with the edge's path id) or an echo (edge side, stamped with this path's own
	// id) establishes/refreshes an entry. Nothing else introduces or moves an address.
	remotes map[uint8]*remoteEntry
	// selKey/selValid name the SELECTED entry — the destination getRemote() returns
	// (feeding emitProbes and the PMTU probes; the transport sends to the routes its own
	// hellos established). Selection is STICKY: established by the FIRST probe-learned
	// entry (the R253 cold-start rule), moved only by a one-time DEAD fallback when the
	// selected entry's probes go silent (checkRemoteDead), or by an explicit SetPeerRemote
	// override.
	selKey   uint8
	selValid bool
	// onRoam, when set (device.Up registers it on the PRIMARY peer's path via
	// Multipath.OnPathRoam), fires whenever the SELECTED destination's ADDRESS
	// changes — initial establishment, a DEAD fallback, an
	// in-place rebind of the selected entry, or a SetPeerRemote override — so the per-path
	// PMTUDiscovery re-probes (NotifyRoam) the possibly-different underlay PMTU (T227,
	// defect D88). It never fires on a per-path freshness refresh of a non-selected entry
	// nor on a same-address re-learn, so the pre-D94 per-probe-cadence churn is gone.
	onRoam func()
}

// remoteEntry is one sender-path's learned return address plus its freshness.
// lastProbe is stamped by every authenticated probe that establishes/refreshes the entry
// (and at seeding/override time, so a stale seed can go DEAD and fall back rather than
// wedging); checkRemoteDead compares it against remoteDeadAfter for the SELECTED entry.
type remoteEntry struct {
	addr      netip.AddrPort
	lastProbe time.Time
}

// peerState holds the per-PEER datapath state: the SINGLE virtual endpoint the engine
// holds for this peer, its adaptive transport, its probe reflector, its receive
// resequencer, its per-path probe initiators, and its per-(peer,path) views over the
// shared sockets. The single-peer edge/hub constructs EXACTLY ONE peerState (Multipath
// embeds it as the primary and the datapath reaches its fields through that embed); the
// concentrator constructs one peerState per bound peer. adaptive and resequencer are
// atomic pointers so the lock-free receive/send fast paths read them WITHOUT m.mu.
type peerState struct {
	adaptive atomic.Pointer[adaptivePeer]
	// name is the peer id/name, the key under which Multipath.peersByName holds this peer.
	// Empty on the single-peer edge/hub (there is only one peer to key) and on the
	// concentrator's primary UNTIL SetPrimaryPeerName re-keys it to its configured name
	// (D58) — device.Up calls that whenever more than one peer is configured, so in
	// practice this is empty only for the true single-peer case.
	name string

	// psk is THIS peer's effective pre-shared key: the sole seam from which every frame
	// Codec this peer derives (its transport's send codec and each per-(peer,path) receive
	// codec, via newCodec) and its probe Reflector authenticate. The single-peer edge/hub
	// sets it to the one configured psk; the concentrator sets a DIFFERENT psk per bound
	// peer, so one peer's codec/reflector rejects another peer's frames (T84).
	psk config.Key

	// Immutable per-peer collaborators, built at construction and persisting across the
	// Open→Close socket lifecycle (the concentrator pins virt's destination once for the
	// process life, so virt in particular must NOT be recreated per Open).
	virt      *udpEndpoint
	reflector *telemetry.Reflector
	// newProber mints a prober for a path admitted to THIS peer at runtime (T30).
	newProber ProberFactory
	// probers is this peer's BOOT-TIME per-path probe initiator set, in durable-membership
	// (m.defs) order — bound AND deferred. At Open each bound entry is bound onto its
	// peerPathState (pp.prober), and thereafter the hot paths reach a path's prober
	// through the peerPathState — never by indexing this slice — so a runtime path
	// add/remove cannot race echo handling.
	probers []*telemetry.Prober

	// configuredRemote is THIS peer's CONFIGURED wire remote — the concentrator endpoint an EDGE
	// peer statically targets (T251/Q68b). It seeds every one of this peer's paths at Open (and any
	// path added at runtime) so a MULTI-EXIT edge sends each peer's frames to ITS OWN
	// concentrator, never a single bind-global default that would conflate two peers onto one hub.
	// Unset on a concentrator peer (which learns its edge remote dynamically from authenticated
	// inbound) and on a single-peer edge/hub (which keeps the bind-global defaultRemote).
	// Seeded by SeedEdgePeerRemotes; durable across the Open/Close cycle.
	configuredRemote    netip.AddrPort
	hasConfiguredRemote bool

	// Per-Open state, (re)built by Open and cleared by Close.
	paths []*peerPathState
	// resequencer is this peer's receive resequencing buffer for bulk datagrams.
	// Published atomically so the per-path readLoop goroutines read it WITHOUT m.mu.
	resequencer atomic.Pointer[reseq.Resequencer]

	// lifecycleMu serializes lazy (re)instantiation of the resequencer on a readLoop
	// goroutine (ensurePeerReceiveInstantiated) against teardown's clearing of it
	// (teardownPeerLocked), so a teardown interleaving mid-instantiation can never
	// resurrect it on a torn-down peer that the next re-bind then reuses stale. It is a
	// LEAF lock taken alone by instantiation and Close finalization. TearDownPeer acquires
	// it before briefly taking m.mu, so no lifecycle wait occurs while the bind lock is
	// held; instantiation never reaches for m.mu.
	lifecycleMu sync.Mutex
}

// newPeerState builds the durable per-peer datapath state whose probe Reflector — and,
// through ps.psk, every frame Codec this peer later derives in Open/AddPath (via
// newCodec) — authenticate under THIS peer's psk (T84): the single-peer edge/hub mints
// exactly one (the primary) from the sole configured psk, while the concentrator mints
// one per bound peer from that peer's DIFFERENT effective psk, so cross-psk frames are
// rejected. virt is created here (not per Open) because the concentrator pins its
// destination once for the process life. The Reflector draws its per-path challenges
// from crypto/rand. The per-Open fields (paths, resequencer, adaptive) are left zero
// for Open to (re)build.
func newPeerState(name string, psk config.Key, newProber ProberFactory, probers []*telemetry.Prober) *peerState {
	return &peerState{
		name:      name,
		psk:       psk,
		virt:      &udpEndpoint{},
		reflector: telemetry.NewReflector(psk, rand.Reader),
		newProber: newProber,
		probers:   probers,
	}
}

// newCodec derives a fresh frame Codec bound to THIS peer's psk — the transport's send Codec and
// each per-(peer,path) receive Codec share the derivation but not the instance, since a Codec
// is not safe for concurrent use (each receive path decodes on its own goroutine). Deriving
// from ps.psk (not a Multipath-wide key) is what makes a path's receive Codec the codec of the
// peer the path is bound to (T84).
func (ps *peerState) newCodec() (*frame.Codec, error) {
	return frame.NewCodec(ps.psk)
}

// deferredPath is a configured path whose WELL-FORMED source_addr was not yet
// assignable at Open (net.ListenUDP -> EADDRNOTAVAIL: no interface holds the
// address, e.g. a 5G modem with no DHCP lease at boot). Rather than tear the whole
// bond down, Open records the path here instead of binding it: the tunnel comes up
// on the paths that DID bind, and this path stays DOWN — its prober, never fed an
// echo, reports StateDown, and with no socket the transport learns no lane on it. It
// carries the boot-time
// prober so the T55 background reconcile that retries the bind reuses the SAME
// path-id stamp instead of minting a new one. prober is nil only on a bind without
// the probe transport, which never defers (see Open).
type deferredPath struct {
	def    config.Path
	prober *telemetry.Prober
	// warnedUnresolvable is the FIX1 (D53 round 2) per-path dedup latch for
	// warnForcedDeviceStillDeferred: true once reconcileDeferred has WARNed that this
	// path's forced-device interface is unresolvable AND its source-IP-pin fallback
	// also failed to bind, so a persistently-unresolvable deferred path WARNs once for
	// the whole deferral window rather than once per 1 Hz reconcile tick. It is set on
	// Open/AddPath's initial deferral (the first WARN for this condition) and cleared
	// the moment a later tick's listen succeeds (the interface resolved, or the
	// fallback bind now works) — so a LATER unresolvable transition (a re-roam) WARNs
	// again. It has no meaning for a path that is not bind="device" (the WARN it
	// guards never fires for one).
	warnedUnresolvable bool
	// warnedPromoteFail is the D71 per-path dedup latch for the promote-failure WARN in
	// reconcileDeferred: true once this path has BOUND but FAILED promotion (a prober fan-out
	// desync or codec build error), so a persistently un-promotable deferred path WARNs
	// once for the whole failure window rather than once per 1 Hz reconcile tick. It is NOT
	// cleared on a later listen success (that would re-spam every tick, since the listen
	// re-succeeds each tick while promotion keeps failing); a path that finally promotes
	// leaves m.deferred, discarding the latch with it.
	warnedPromoteFail bool
}

// remoteDeadAfter is the probe-silence bound on the SELECTED remote entry after which
// checkRemoteDead performs the one-time sticky fallback to the freshest probe-learned
// entry (T246, defect D94). It is deliberately ABOVE the liveness DownAfter (1200 ms):
// path liveness declares the path down first; this table-level fallback is the
// concentrator's belt-and-suspenders — it has a single path, so the DESTINATION itself
// must follow the edge's surviving WAN. 2x DownAfter keeps ordinary probe jitter from
// ever tripping it while still bounding downlink-failover latency to a couple of liveness windows.
const remoteDeadAfter = 2 * telemetry.DefaultDownAfter

// setRemote SEEDS or OVERRIDES this view's downlink destination with an operator/
// control-plane address (edge config dest_addr at Open; SetPeerRemote at hub failover):
// it CLEARS the learned table (post-override, stale pre-override entries must not win a
// DEAD fallback) and installs ap as the selected entry under this path's OWN id. The
// entry is stamped probe-fresh at installation so a dead seed self-heals: if nothing
// refreshes it (edge side: the echoes keyed by this same own id; concentrator side: a
// request under a different key establishes a sibling entry) it goes DEAD after
// remoteDeadAfter and the selection falls back to a genuinely fresh entry.
func (ps *peerPathState) setRemote(ap netip.AddrPort) {
	ps.mu.Lock()
	prev, hadPrev := ps.selectedAddrLocked()
	ps.remotes = map[uint8]*remoteEntry{ps.id: {addr: ap, lastProbe: time.Now()}}
	ps.selKey, ps.selValid = ps.id, true
	cb := ps.onRoam
	ps.mu.Unlock()
	// The override is a deliberate repoint: fire the roam callback on an actual ADDRESS
	// change (not on a same-address re-seed, and not on the very first seed — the
	// pre-D94 first-set-silent behaviour Open's config seeding relies on).
	if hadPrev && prev != ap && cb != nil {
		cb()
	}
}

// learnRemoteFromProbe folds one AUTHENTICATED probe frame (request or echo — the MAC
// verified in Decode, the same trust setRemote's per-probe overwrite carried pre-D94)
// into the freshness table under the frame's sender-stamped path id (T246, defect D94).
// It establishes or refreshes-in-place the entry — the D9/D11 NAT-rebinding property:
// probes keep EVERY sender path's return address current — and NEVER moves the
// selection, with two deliberate exceptions: the FIRST entry ever established selects
// itself (the R253 cold-start rule — the destination must be valid for the
// concentrator's own probe/liveness plane to work), and an in-place ADDRESS change of
// the already-selected entry follows it (same sender path, new NAT binding) with one
// roam callback.
func (ps *peerPathState) learnRemoteFromProbe(senderPathID uint8, ap netip.AddrPort) {
	now := time.Now()
	ps.mu.Lock()
	prev, hadPrev := ps.selectedAddrLocked()
	if ps.remotes == nil {
		ps.remotes = make(map[uint8]*remoteEntry)
	}
	e, ok := ps.remotes[senderPathID]
	if !ok {
		e = &remoteEntry{}
		ps.remotes[senderPathID] = e
	}
	e.addr, e.lastProbe = ap, now
	if !ps.selValid {
		// R253 cold start: the first probe-established entry is selected, sticky.
		ps.selKey, ps.selValid = senderPathID, true
	}
	next, hadNext := ps.selectedAddrLocked()
	cb := ps.onRoam
	ps.mu.Unlock()
	if hadNext && (!hadPrev || prev != next) && cb != nil {
		cb()
	}
}

// checkRemoteDead performs the one-time sticky DEAD fallback (T246, defect D94): when
// the SELECTED entry has seen no authenticated probe for remoteDeadAfter, the selection
// moves ONCE to the freshest probe-learned entry and sticks there (never
// "whichever probed last" — all WANs probe at cadence, so that would re-flap). Called
// from the probe cadence (emitProbes), never the per-datagram hot path.
func (ps *peerPathState) checkRemoteDead(now time.Time) {
	ps.mu.Lock()
	if !ps.selValid {
		ps.mu.Unlock()
		return
	}
	sel := ps.remotes[ps.selKey]
	if sel == nil || now.Sub(sel.lastProbe) < remoteDeadAfter {
		ps.mu.Unlock()
		return
	}
	var freshKey uint8
	var fresh *remoteEntry
	for k, e := range ps.remotes {
		if k == ps.selKey {
			continue
		}
		if fresh == nil || e.lastProbe.After(fresh.lastProbe) {
			freshKey, fresh = k, e
		}
	}
	if fresh == nil || !fresh.lastProbe.After(sel.lastProbe) {
		// No living alternative: keep the selection (nothing better to fall back to).
		ps.mu.Unlock()
		return
	}
	prev, hadPrev := ps.selectedAddrLocked()
	ps.selKey = freshKey
	next, _ := ps.selectedAddrLocked()
	cb := ps.onRoam
	ps.mu.Unlock()
	if (!hadPrev || prev != next) && cb != nil {
		cb()
	}
}

// selectedAddrLocked returns the selected entry's address, if any. Caller holds ps.mu.
func (ps *peerPathState) selectedAddrLocked() (netip.AddrPort, bool) {
	if !ps.selValid {
		return netip.AddrPort{}, false
	}
	e := ps.remotes[ps.selKey]
	if e == nil {
		return netip.AddrPort{}, false
	}
	return e.addr, true
}

// getRemote returns the SELECTED downlink destination — the sticky selected
// entry's address (T246, defect D94) — feeding emitProbes and the PMTU probes.
func (ps *peerPathState) getRemote() (netip.AddrPort, bool) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return ps.selectedAddrLocked()
}

// errNoPathRemote is returned by the PMTU send seam when a probe is attempted before
// this path has learned a remote — a transient startup condition, so the search stays
// unconverged and retries on a later tick rather than treating it as a size verdict.
var errNoPathRemote = errors.New("bind: path has no remote for PMTU probe")
var errPMTUProbeAlreadyPending = errors.New("bind: path already has a pending PMTU probe")

type generatedProbeRequest struct {
	work func() error
	done chan error
}

func (ps *peerPathState) enqueuePMTUProbe(work func() error) error {
	request := &generatedProbeRequest{
		work: work,
		done: make(chan error, 1),
	}
	ps.generatedProbeMu.Lock()
	switch {
	case ps.generatedProbeClosed:
		ps.generatedProbeMu.Unlock()
		return errClosed
	case ps.pendingPMTU != nil:
		ps.generatedProbeMu.Unlock()
		return errPMTUProbeAlreadyPending
	default:
		ps.pendingPMTU = request
		ps.generatedProbeMu.Unlock()
	}
	return <-request.done
}

func (ps *peerPathState) takePMTUProbe() *generatedProbeRequest {
	ps.generatedProbeMu.Lock()
	defer ps.generatedProbeMu.Unlock()
	if ps.ordinaryProbeDue {
		ps.ordinaryProbeDue = false
		return nil
	}
	request := ps.pendingPMTU
	ps.pendingPMTU = nil
	if request != nil {
		ps.ordinaryProbeDue = true
	}
	return request
}

func (ps *peerPathState) closeGeneratedProbes() {
	ps.generatedProbeMu.Lock()
	ps.generatedProbeClosed = true
	request := ps.pendingPMTU
	ps.pendingPMTU = nil
	ps.generatedProbeMu.Unlock()
	if request != nil {
		request.done <- errClosed
	}
}

// buildPMTUProbe constructs this path's PMTU echo-await backend once its prober and
// shared socket are set (T227, defect D88). The dispatch func queues the complete
// generation/send work for this path's next eligible probe-cadence slot and blocks
// until emitProbes executes it. Sequence allocation, timestamping, and echo-waiter
// registration therefore happen in the actual slot, not before the cadence wait. A
// padded probe substitutes for the ordinary local probe in that slot, and the next
// slot is reserved for ordinary liveness before another PMTU request is eligible.
// It returns nil when the path has no prober (the probe transport is disabled),
// matching pmtuProbe's nil-safe contract at the NotifyEcho call site.
func (m *Multipath) buildPMTUProbe(ps *peerPathState) *telemetry.EchoAwaitProbe {
	if ps.prober == nil {
		return nil
	}
	send := func(raw []byte) error {
		remote, ok := ps.getRemote()
		if !ok {
			return errNoPathRemote
		}
		if _, err := ps.writeToUDPAddrPort(raw, remote); err != nil {
			mapped := mapPMTUProbeWriteError(err)
			if !errors.Is(mapped, telemetry.ErrProbeTooLarge) {
				ps.probeSendErrors.Add(1)
			}
			return mapped
		}
		ps.recordOuterWrite(len(raw))
		return nil
	}
	outerIPUDPOverhead := IPv4UDPOverhead
	if pathIsV6(ps.src) {
		outerIPUDPOverhead = IPv6UDPOverhead
	}
	return telemetry.NewCadencedEchoAwaitProbe(
		ps.prober,
		send,
		ps.enqueuePMTUProbe,
		outerIPUDPOverhead,
		0,
		nil,
	)
}

// PMTUProbe returns the PRIMARY peer's PMTU echo-await backend for the named path, or
// nil if the path or the probe transport is absent. It is the telemetry.PMTUProbe a
// per-path PMTUDiscovery drives (device.Up, T228, defect D88). Per-path (NOT per-peer):
// on a multi-peer concentrator the primary peer's socket carries the representative
// probe and the discovered PMTU is treated as a per-path property (equal across peers,
// matching the sampleMTU accounting).
func (m *Multipath) PMTUProbe(pathName string) *telemetry.EchoAwaitProbe {
	m.mu.Lock()
	defer m.mu.Unlock()
	pp := m.primaryPathByNameLocked(pathName)
	if pp == nil {
		return nil
	}
	return pp.pmtuProbe
}

// OnPathRoam registers cb to fire when the named path's PRIMARY-peer remote actually
// CHANGES — a concentrator learned-endpoint repoint — so device.Up can trigger a PMTU
// re-probe (PMTUDiscovery.NotifyRoam). It is a no-op when the path is absent. The edge
// hub-failover repoint is handled device-side (device.go) and needs no bind callback.
func (m *Multipath) OnPathRoam(pathName string, cb func()) {
	m.mu.Lock()
	pp := m.primaryPathByNameLocked(pathName)
	m.mu.Unlock()
	if pp == nil {
		return
	}
	pp.mu.Lock()
	pp.onRoam = cb
	pp.mu.Unlock()
}

// primaryPathByNameLocked returns the PRIMARY peer's path with the given name, or nil.
// The caller holds m.mu. The primary peer is m.peers[0] (the pre-split single peer the
// concentrator prepends; every additional bound peer is appended).
func (m *Multipath) primaryPathByNameLocked(name string) *peerPathState {
	if len(m.peers) == 0 {
		return nil
	}
	for _, pp := range m.peers[0].paths {
		if pp.name == name {
			return pp
		}
	}
	return nil
}

// ProberFactory mints a *telemetry.Prober for a path admitted at runtime (T30),
// stamped with the given stable path-id and the path's OWN ride_through dwell (D86/T207)
// so a runtime-added path measures liveness with the same configured hysteresis as a
// boot-time path. The device wires it capturing the SAME per-boot session id, global
// down_after threshold, clock, PSK, and logger the boot-time probers were built with, so a
// runtime path's liveness is measured identically to a boot-time path with the same
// ride_through (its probes join the same session so the peer's reflector adopts them without
// a challenge reset). A bind built without one cannot add paths at runtime.
type ProberFactory func(name string, id uint8, rideThrough time.Duration) *telemetry.Prober

// sourceBinding is one entry of the source->peer demux map (peerBySource): the peer a learned
// source AddrPort was bound to by an authenticated PROBE, plus a monotonic insertion sequence
// used ONLY to choose a peer's OWN oldest binding for LRU eviction when that peer authenticates
// a new AddrPort for itself while already at its per-peer quota (roam port-churn, D49). The seq
// orders a single peer's own bindings against each other; it is never compared across peers.
type sourceBinding struct {
	peer *peerState
	seq  uint64
}

// Multipath is the bonding conn.Bind: one UDP socket per configured path (bound to
// the path's source address), all fronted by a SINGLE stable virtual endpoint the
// engine holds per peer. Send hands each opaque WireGuard datagram to the peer's
// adaptive transport (internal/bond), which paces it over the lanes its
// authenticated hellos established; one Bind-owned reader per path feeds the
// transport's deliveries into the peer's resequencer, and a SINGLE engine-facing
// ReceiveFunc drains it and hands the inner WG datagram up under the shared virtual
// endpoint (the fan-in that lets paths be added/removed at runtime without the engine
// spawning receive goroutines — T30).
//
// Lifecycle: the sockets live for the duration of an Open→Close span, NOT the
// Multipath's whole life. The amneziawg engine calls Close() before every Open()
// (device.upLocked → BindUpdate → closeBindLocked, and on IpcSet listen_port /
// route-change events) and cycles Close↔Open on Down/Up, so — exactly like
// conn.StdNetBind — Open creates the per-path sockets and Close tears them down
// AND clears the path state so the next Open rebuilds from scratch. The "closed"
// state is simply "no bound sockets" (len(paths)==0); there is no separate sticky
// flag that a later Open would have to reset.
type Multipath struct {
	defs []config.Path

	// log is this bind's component-scoped logger (log.Component("bind"), D53), set once
	// at construction and never nil (NewMultipath fails fast on a nil logger, consistent
	// with its other required-collaborator checks). It is the sole place internal/bind
	// depends on internal/log; pathsock.go itself stays logging-free (see
	// warnForcedDeviceUnresolvable / warnForcedDeviceStillDeferred / warnDeviceBindFallback,
	// the call-site helpers that read it).
	log log.Logger

	// deferredListen binds a reconciled deferred path's socket (T55 background
	// reconcile). It is an injection seam: the default pins the source IP unless dev is
	// set (net.ListenUDP / listenPath, matching AddPath's runtime bind — I5), and a test
	// overrides it to drive the deferred→bound transition deterministically — a
	// source_addr "becoming assignable" — without a real interface address having to
	// appear on the host. dev is resolveForcedDeviceBind's decision for the deferred
	// path's resolved BindMode, computed by reconcileDeferred before the call. The
	// middle return is the underlying SO_BINDTODEVICE error when dev != "" and the
	// device bind failed and fell back to source-IP pinning (nil otherwise — no device
	// bind was attempted, or it succeeded), matching listenPath (D53); the caller logs
	// it rather than pathsock.go, which stays logging-free. Immutable after
	// construction, never nil.
	deferredListen func(src netip.Addr, port uint16, dev string) (*net.UDPConn, error, error)

	// resolveDeviceBind decides AddPath's and reconcileDeferred's per-path forced-
	// device bind (I5): whether a path's RESOLVED BindMode is config.BindModeDevice,
	// and if so, the interface its source_addr currently resolves to (see
	// resolveForcedDeviceBind — it has no other-path contention to check, unlike
	// Open's planPathBinds/selectDeviceBinds). It is an injection seam mirroring
	// deferredListen: the default is the real resolveForcedDeviceBind (a
	// net.Interfaces() snapshot), and a test overrides it to drive a BindModeDevice
	// path's dev deterministically without a real interface having to appear on the
	// host (T106 round 2). Immutable after construction, never nil.
	resolveDeviceBind func(src netip.Addr, mode config.BindMode) string

	// resolveIface resolves a source address to its interface (dev + family count) — the input
	// to the AUTO-mode runtime device-bind contention decision (D30, autoRuntimeDeviceBind).
	// Unlike resolveDeviceBind (forced-device only), an auto-mode runtime-added or promoted path
	// device-binds only when Open's selectDeviceBinds heuristic holds against the whole
	// membership; that heuristic needs each source's ifaceInfo. Injection seam mirroring
	// resolveDeviceBind: the default is a fresh net.Interfaces() snapshot per call, and a test
	// overrides it to drive interface resolution deterministically without real interfaces.
	// Immutable after construction, never nil.
	resolveIface func(src netip.Addr) ifaceInfo

	// addPathListen binds AddPath's runtime-admitted path's socket. It is an
	// injection seam mirroring deferredListen, scoped to AddPath's OWN call site:
	// AddPath calls the package-level listenPath directly (it is not routed through
	// deferredListen, which only reconcileDeferred drives), so without this seam no
	// test can observe what dev AddPath actually threads into the bind — resolving a
	// BindModeDevice interface via resolveDeviceBind and then discarding it at the
	// listen call passes the full suite (T106 round 3). The default is the real
	// listenPath; a test overrides it to capture the dev argument deterministically
	// without a real interface having to appear on the host. The middle return carries
	// the same setsockopt-fallback error as deferredListen (D53). Immutable after
	// construction, never nil.
	addPathListen func(src netip.Addr, port uint16, dev string) (*net.UDPConn, error, error)

	// clock is the bind-level injectable time source for the resequencers and the
	// receive drainer's park timer. It is an injection seam mirroring deferredListen /
	// resolveDeviceBind / addPathListen: the default is the wall clock (set in
	// NewMultipath), and a test overrides it PRE-OPEN with a hand-advanced one.
	// Immutable after Open, never nil.
	clock bindClock

	// beforeReceivePark is a test-only seam: the receive drainer calls it with the
	// instant it is about to park until.
	beforeReceivePark func(time.Time)

	// transitionMu serializes transport-generation changes while their blocking
	// retirement barriers run outside m.mu. The fixed order is transitionMu then
	// m.mu; Send never takes transitionMu.
	transitionMu sync.Mutex
	mu           sync.Mutex
	// openGeneration monotonically identifies each successful-or-attempted Open
	// construction. Per-Open senders and sockets snapshot it so delayed failures
	// cannot act on a replacement generation.
	openGeneration atomic.Uint64

	// The PRIMARY peer, embedded so the single-peer datapath (Send, the receive drainer,
	// the probe loop) and the single-peer tests reach its fields — virt, reflector,
	// newProber, probers, paths, resequencer, adaptive — transparently through promotion.
	// It is peers[0]; the concentrator's additional peers live in peers/peersByName.
	*peerState

	// peers is every bound peer (len 1 on the single-peer edge/hub). The runtime shared-
	// path add/remove fan-out iterates it so per-(peer,path) state is created/torn-down for
	// EVERY currently-bound peer (attachSharedPathLocked / RemovePath). peersByName keys
	// them by peer id/name.
	peers       []*peerState
	peersByName map[string]*peerState
	// peersView is the LOCK-FREE snapshot of the bound peer set the single engine-facing
	// receive drainer iterates (newReceiveFunc). m.peers is mutated only under m.mu (peer
	// wiring / fan-out); every mutation republishes this pointer (republishPeersLocked) so
	// the drainer enumerates EVERY bound peer's resequencer WITHOUT taking m.mu on the
	// receive hot path — the same lock-free-publish discipline the resequencer uses. It
	// is never nil after construction (the constructor publishes the primary-only view).
	peersView atomic.Pointer[[]*peerState]
	// peerByEndpoint is the inbound endpoint-keyed demux placeholder (still unused today: the
	// single-peer edge/hub needs no demux, and the source-keyed binding below is what the
	// concentrator receive path routes on).
	peerByEndpoint map[netip.AddrPort]*peerState
	// peerBySource is the inbound source-demux map: a learned source AddrPort → the peer it was
	// bound to by an authenticated PROBE (T88). It is keyed by the full source netip.AddrPort
	// (address+port, D47), NOT the bare address, so two peers behind ONE public IP (CGNAT, one
	// netip.Addr, distinct ports) occupy distinct entries and demux independently. The
	// concentrator's per-socket readLoops resolve a datagram's source to its owning peer through
	// it; a source is bound ONLY on the first PROBE that MAC-verifies under a peer's psk (D9/D11:
	// bindings, like remotes, are learned only from authenticated PROBEs). It is published
	// copy-on-write through an atomic.Pointer so the receive hot path resolves a bound source with
	// a lock-free Load (no m.mu on the receive hot path), and a new binding is installed
	// lock-free by a CAS republish of a
	// copy with the entry added (bindSourceToPeer). Nil until the first binding; the single-peer
	// edge/hub never consults it (one peer owns every socket — handleInbound's fast path skips the
	// demux entirely).
	peerBySource atomic.Pointer[map[netip.AddrPort]sourceBinding]
	// maxDemuxSources is the GLOBAL cap on peerBySource (the provisional/unbound-source demux
	// state) so a bootstrap flood cannot grow it without bound (Q26/Q27, see defaultMaxDemuxSources).
	// Within it bindSourceToPeer enforces a PER-PEER quota (maxDemuxSources/len(peers), floor 1,
	// D49) for cross-peer isolation. Set once at construction and read on the lock-free bind path
	// (bindSourceToPeer); a test may lower it to exercise cap/quota exhaustion. Zero (never set)
	// means "no cap".
	maxDemuxSources int
	// bindSeq is a monotonic counter stamped on each source binding to order a peer's OWN
	// bindings for per-peer LRU eviction (D49 roam port-churn). Read/incremented on the lock-free
	// bind path (bindSourceToPeer) from readLoop goroutines, so it is atomic; it takes no m.mu.
	bindSeq atomic.Uint64
	// peerByVirt routes an OUTBOUND Send to its owning peer: the engine hands Send the
	// single virtual endpoint (*udpEndpoint) it holds for a peer, and this map resolves
	// that pointer to the peer's datapath state (its adaptive transport and
	// per-(peer,path) set). It is the SEND-side dual of peerByEndpoint/peerBySource (which
	// demux INBOUND datagrams). Each peer's virt is DISTINCT, so the lookup is exact; an
	// endpoint not in this map is an unknown peer, which Send refuses rather than
	// misrouting onto some other peer's paths. The primary is registered at construction;
	// the concentrator registers each additional peer as it binds it. Read under m.mu on
	// the Send path; written under m.mu (or at single-threaded construction).
	peerByVirt map[*udpEndpoint]*peerState

	// edgePeerByRemote resolves an EDGE peer's CONFIGURED endpoint (netip.AddrPort) to its
	// peerState, so ParseEndpoint returns the OWNING peer's virt — each multi-exit edge peer holds
	// a DISTINCT virt and Send routes on it (peerByVirt). Without this a second edge peer's
	// configured endpoint would resolve to the primary's virt and its WG traffic would egress to the
	// WRONG concentrator (T251/Q68b). Populated by SeedEdgePeerRemotes before Open for a multi-peer
	// edge; empty on the single-peer edge/hub and the concentrator (which need no configured→peer
	// map — the former uses the bind-global default, the latter learns remotes from inbound). Its
	// non-emptiness ALSO marks "multi-exit edge mode" for the per-path remote-seeding precedence in
	// attachPeerPathLocked. Read/written under m.mu (or at single-threaded construction).
	edgePeerByRemote map[netip.AddrPort]*peerState

	// shared is the SHARED per-socket path list (the sockets themselves), rebuilt from
	// m.defs on every Open and mutated by the runtime add/remove. Each bound peer holds a
	// peerPathState VIEW over a subset of these; on the single-peer edge/hub primary.paths
	// is index-aligned with shared. len(shared)==0 is the "closed" (no sockets) state.
	shared []*sharedPathState

	// deferred holds the configured paths whose well-formed source_addr was not yet
	// assignable at the last Open (EADDRNOTAVAIL). They are NOT in shared — the
	// tunnel runs on the paths that bound — but are recorded here,
	// index-independent of shared, for the T55 background reconcile to retry as their
	// addresses appear. Rebuilt from scratch on every Open; guarded by m.mu. The deferred-
	// path machinery is SHARED (per-socket): a promoted deferred path fans its per-(peer,
	// path) state out to every peer exactly as a runtime AddPath does.
	deferred []deferredPath
	// defaultRemote is the fallback per-path remote (the peer's wireguard
	// endpoint) applied to any path without its own dest_addr. It may be set by
	// ParseEndpoint BEFORE Open, so it is stored here and applied at Open time.
	//
	// It is a SINGLE-PEER-EDGE (bind-global) concept, NOT a per-peer one. Reader audit
	// (T252): its ONLY seeding readers are attachPeerPathLocked (Open) and
	// attachSharedPathLocked (runtime AddPath), and BOTH read it only under
	// `len(m.edgePeerByRemote)==0` — i.e. single-peer-edge/hub mode; in a MULTI-EXIT edge
	// (edgePeerByRemote non-empty) each peer seeds from its OWN p.configuredRemote and this
	// field is inert. Written by ParseEndpoint and by setPeerRemoteLocked (the single-
	// controller SetPeerRemote). The per-peer repoint seam (setPeerRemoteForLocked) does NOT
	// write it — a per-peer hub switch has no bind-global meaning (T252/G28/M105).
	defaultRemote    netip.AddrPort
	hasDefaultRemote bool

	// Receive fan-in (T30). To let a path be added at runtime WITHOUT the engine
	// spawning a new receive goroutine (it only builds its receive goroutines once,
	// from the ReceiveFuncs Open returns), the per-path socket reads are decoupled
	// from the engine-facing delivery: the Bind owns one readLoop goroutine PER path
	// (tracked by readersWG) that reads its socket and feeds the shared resequencer,
	// and Open returns a SINGLE engine-facing ReceiveFunc that drains the resequencer
	// in order. A reader pokes deliverSignal after each datagram so the drainer wakes;
	// recvClosed is closed by Close to release both the drainer and any reader parked
	// on a delivery. openPort mirrors Open's bind port so a runtime-added path binds
	// consistently; nextPathID is a monotonic high-water that persists ACROSS Open
	// spans (a Close->Open never lowers it; only a process restart resets it), so a
	// surviving path is never renumbered and a freed id is never reused for the
	// process lifetime, and thus can never collide with the peer's per-path reflector
	// state.
	deliverSignal chan struct{}
	recvClosed    chan struct{}
	readersWG     sync.WaitGroup
	openPort      uint16
	nextPathID    uint16

	// Receive-path liveness sweep (T39, defect D15). Liveness DOWN-detection normally
	// rides StartProbeLoop's single wall-clock ticker goroutine (emitProbes → Tick).
	// Under heavy CPU load — e.g. the concentrator absorbing a saturating forward flood
	// on 4 vCPU — that timer goroutine can be scheduled with ~1s jitter, delaying a
	// path-DOWN transition (and thus the reply-direction failover) past the P1 budget.
	// The per-path receive goroutines, in contrast, are the goroutines the inbound
	// traffic is ALREADY scheduling, so driving Tick from them re-evaluates liveness
	// even when the timer goroutine is starved. sweepIntervalNanos is the probe
	// interval (0 until StartProbeLoop arms it — a bind without the probe transport
	// never sweeps); lastSweepNanos is the wall-clock high-water throttling the sweep
	// to at most once per interval so the receive hot path stays cheap.
	sweepIntervalNanos atomic.Int64
	lastSweepNanos     atomic.Int64

	// everUp is the STICKY "ever had a live path" predicate (I4): set true the first time
	// ANY path, for ANY bound peer, reaches liveness telemetry.StateUp (dispatchInbound,
	// on a fresh probe echo), and never cleared afterward — a later total outage is a
	// genuine failure signal, not a startup warmup. The device-package engineLogger
	// adapter consults EverHadLivePath to downgrade the startup no-healthy-path spam
	// (ErrNoHealthyPath) to a single coalesced INFO line until the first path comes up,
	// then lets it log at ERROR (a real outage) from then on.
	everUp atomic.Bool

	// onFirstPathUp is the optional injectable one-shot callback (D37 detection seam):
	// invoked EXACTLY ONCE, off the receive hot path, on the everUp latch's false->true
	// edge — the SAME moment EverHadLivePath starts reporting true. It keeps the bind
	// WG-unaware: dispatchInbound invokes an opaque func() rather than importing
	// anything from the device/engine layer, so the device-layer consumer (the
	// dependent task) wires whatever it needs (e.g. nudging the engine) through this
	// closure. nil (the default, set by NewMultipath) means "no callback" — dispatchInbound
	// checks for nil before invoking it, so an unset callback never panics. Set via
	// SetOnFirstPathUp; read lock-free (atomic.Pointer) so the receive hot path never
	// blocks on m.mu to check it.
	onFirstPathUp atomic.Pointer[func()]

	// onPeerRestart is invoked, off the receive path, with the peer's name when an
	// authenticated hello shows that an already known peer runs as a new process. The
	// peer has lost its engine sessions, which the engine itself notices only when
	// its new-handshake timer fires.
	onPeerRestart atomic.Pointer[func(string)]
}

// compile-time proof that Multipath satisfies the engine's Bind contract.
var _ Bind = (*Multipath)(nil)

// NewMultipath returns a closed multipath Bind over the configured paths; call
// Open to bind the per-path sockets. The PSK keys the outer framing and must be
// set (config validation guarantees it).
//
// probers is the per-path probe initiator set that drives on-wire liveness (T13/T37)
// and carries the transport's hellos: exactly one *telemetry.Prober per path, in path
// order. The transport learns its peer only from authenticated probes, so the set is
// required. A Reflector is built from the PSK to answer peer probes.
//
// newProber is the factory the runtime path-add path (AddPath, T30) uses to mint a
// prober for a newly-admitted path. Pass nil to forbid runtime path addition.
//
// lg is the structured logger (internal/log); it is component-scoped to "bind"
// (log.Logger.Component) and stored so the SO_BINDTODEVICE→source-IP fallback a
// forced bind="device" path can silently take is surfaced at WARN instead of the
// pre-D53 silence (see warnForcedDeviceUnresolvable / warnDeviceBindFallback). It is
// a required collaborator — fail fast on nil rather than let the bind run
// logging-blind.
func NewMultipath(paths []config.Path, psk config.Key, probers []*telemetry.Prober, newProber ProberFactory, lg log.Logger) (*Multipath, error) {
	if len(paths) == 0 {
		return nil, errors.New("bind: at least one path is required")
	}
	if !psk.IsSet() {
		return nil, errors.New("bind: PSK is required for outer framing")
	}
	if len(paths) > 256 {
		// path-id is a single wire byte (frame.Probe.PathID).
		return nil, fmt.Errorf("bind: at most 256 paths supported, got %d", len(paths))
	}
	if lg == nil {
		return nil, errors.New("bind: a logger is required")
	}
	if len(probers) != len(paths) {
		return nil, fmt.Errorf("bind: probers must have one entry per path (got %d, want %d)", len(probers), len(paths))
	}
	for i, pr := range probers {
		if pr == nil {
			return nil, fmt.Errorf("bind: prober %d is nil", i)
		}
	}
	primary := newPeerState("", psk, newProber, probers)
	m := &Multipath{
		defs:              append([]config.Path(nil), paths...),
		log:               lg.Component("bind"),
		deferredListen:    defaultDeferredListen,
		resolveDeviceBind: resolveForcedDeviceBind,
		resolveIface: func(s netip.Addr) ifaceInfo {
			ifaces, err := net.Interfaces()
			if err != nil {
				ifaces = nil
			}
			return interfaceInfo(s, ifaces)
		},
		addPathListen:    listenPath,
		clock:            systemClock{},
		peerState:        primary,
		peers:            []*peerState{primary},
		peersByName:      map[string]*peerState{primary.name: primary},
		peerByEndpoint:   map[netip.AddrPort]*peerState{},
		peerByVirt:       map[*udpEndpoint]*peerState{primary.virt: primary},
		edgePeerByRemote: map[netip.AddrPort]*peerState{},
		maxDemuxSources:  defaultMaxDemuxSources,
	}
	m.republishPeersLocked()
	return m, nil
}

// warnForcedDeviceUnresolvable logs the D53 layer-(a) fallback that ACTUALLY
// materialized: a path configured bind="device" whose source address resolved to NO
// live interface (dev == ""), whose source-IP-pin fallback attempt then produced a
// REAL working socket, so the operator's roam-survival choice was silently lost
// (pre-D53) unless this WARN surfaces it. Callers gate it on that fallback having
// materialized — the caller only reaches this AFTER a successful listen (err == nil,
// D53 round 2 / FIX 2) — so it never claims a fallback that did not happen; see
// warnForcedDeviceStillDeferred for the accurate message when the fallback attempt
// itself also fails. It is a no-op for every other case — BindModeSource/
// BindModeAuto never "fall back" by resolving to dev == "": that is their ordinary,
// non-forced decision (see selectDeviceBinds/selectForcedDeviceBind) and would flood
// the log if warned on. Called from the three sites that resolve a per-path forced-
// device decision AFTER their listen succeeds: Open (planPathBinds), AddPath, and
// reconcileDeferred (both via m.resolveDeviceBind).
func (m *Multipath) warnForcedDeviceUnresolvable(name string, mode config.BindMode, src netip.Addr, dev string) {
	if mode != config.BindModeDevice || dev != "" {
		return
	}
	m.log.Warn(forcedDeviceUnresolvableWarn, "path", name, "interface", dev, "source_addr", src.String())
}

// warnForcedDeviceStillDeferred logs the D53 round-2 (FIX 1 + FIX 2) accurate,
// non-fallback-claiming counterpart to warnForcedDeviceUnresolvable: a path
// configured bind="device" whose source address has NO resolvable interface (dev ==
// "") AND whose source-IP-pin fallback attempt this tick ALSO failed to bind, so the
// path stays (or becomes) deferred with NO socket at all — logging "falling back to
// source-IP pinning" here would be a false claim. It is a no-op unless mode ==
// BindModeDevice && dev == "" (the same guard as warnForcedDeviceUnresolvable), and
// is deduplicated per condition-transition via alreadyWarned (FIX 1): the caller
// threads deferredPath.warnedUnresolvable (reconcileDeferred's per-tick loop) or
// false (Open/AddPath's one-shot initial deferral) in, and this returns the value
// the caller should persist — true once WARNed, so a persistently-unresolvable
// deferred path WARNs once for the whole deferral window rather than once per 1 Hz
// reconcile tick, and the caller resets it to false the moment the interface
// resolves or the fallback bind starts working, re-arming a LATER transition.
func (m *Multipath) warnForcedDeviceStillDeferred(name string, mode config.BindMode, dev string, alreadyWarned bool) bool {
	if mode != config.BindModeDevice || dev != "" {
		return false
	}
	if !alreadyWarned {
		m.log.Warn(forcedDeviceStillDeferredWarn, "path", name, "interface", dev)
	}
	return true
}

// warnDeviceBindFallback logs the D53 layer-(b) fallback: deviceErr is the
// SO_BINDTODEVICE error listenPath (or a test's addPathListen/deferredListen seam)
// returns exactly when a device bind was attempted (dev != "") and failed. Callers
// invoke this only AFTER a successful listen (err == nil, D53 round 2 / FIX 2), so
// the accompanying conn is a REAL, working source-IP-pinned socket — never a claim
// of a fallback that did not materialize. It is a no-op whenever no device bind was
// attempted (dev == "") or it succeeded (deviceErr == nil). An operator-forced
// bind="device" logs at WARN (the roam-survival property they asked for is lost); an
// AUTO-selected device bind logs at INFO (the operator never asked for that
// property, so its loss is informational, not actionable) — this also covers the
// PRE-EXISTING silent CAP/setsockopt fallback AUTO could already hit.
func (m *Multipath) warnDeviceBindFallback(name string, mode config.BindMode, dev string, deviceErr error) {
	if deviceErr == nil {
		return
	}
	if mode == config.BindModeDevice {
		m.log.Warn(forcedDeviceSetsockoptWarn, "path", name, "interface", dev, "error", deviceErr.Error())
		return
	}
	m.log.Info(autoDeviceSetsockoptInfo, "path", name, "interface", dev, "error", deviceErr.Error())
}

// republishPeersLocked snapshots m.peers into the lock-free peersView the engine-facing
// receive drainer (newReceiveFunc) iterates. The caller MUST hold m.mu whenever the bind
// can be open (so the snapshot never races a concurrent m.peers append); the constructor
// is the sole exception (it runs before the Multipath is reachable by any goroutine). The
// snapshot is a fresh slice so a later append to m.peers never mutates a view the drainer
// is mid-iteration over.
func (m *Multipath) republishPeersLocked() {
	snap := make([]*peerState, len(m.peers))
	copy(snap, m.peers)
	m.peersView.Store(&snap)
}

// AddConcentratorPeer registers one ADDITIONAL bound peer with the Bind — the concentrator's
// per-peer wiring (G4/T93). Peer 0 (the embedded primary) is built by NewMultipath; the
// concentrator calls this once per additional configured peer, each with its OWN effective psk,
// boot-time per-path prober set, and runtime prober factory (all keyed on that
// peer's psk, so one peer's codec/reflector reject another's frames — T84/R72). The peer's
// STABLE virtual endpoint is minted here (newPeerState) and registered in peerByVirt so an
// outbound Send routes replies back to THIS peer; Open then builds this peer's per-(peer,path)
// view of every bound socket and (via newReceiveFunc) reports its
// virt to the engine on the first inbound frame (invariant A1: one virtual endpoint per peer).
//
// It MUST be called BEFORE Open (while the bind is closed): the per-(peer,path) views are
// rebuilt by Open from the registered peer set on every Open span (including each Close→Open
// cycle the engine drives on Down/Up and route changes), so a peer registered after the sockets
// are bound would be view-less and its frames never routed. probers must be
// index-aligned with the configured path membership (m.defs), exactly as the primary's are.
func (m *Multipath) AddConcentratorPeer(name string, psk config.Key, probers []*telemetry.Prober, newProber ProberFactory) error {
	if name == "" {
		return errors.New("bind: concentrator peer name is required")
	}
	if !psk.IsSet() {
		return errors.New("bind: concentrator peer psk is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.paths) != 0 {
		return errors.New("bind: concentrator peers must be registered before Open")
	}
	if len(probers) != len(m.defs) {
		return fmt.Errorf("bind: concentrator peer %q: probers must have one entry per configured path (got %d, want %d)", name, len(probers), len(m.defs))
	}
	for i, pr := range probers {
		if pr == nil {
			return fmt.Errorf("bind: concentrator peer %q: prober %d is nil", name, i)
		}
	}
	if name == m.name {
		return fmt.Errorf("bind: concentrator peer name %q collides with the primary peer", name)
	}
	if _, dup := m.peersByName[name]; dup {
		return fmt.Errorf("bind: duplicate concentrator peer name %q", name)
	}
	p := newPeerState(name, psk, newProber, probers)
	m.peers = append(m.peers, p)
	m.peersByName[name] = p
	m.peerByVirt[p.virt] = p
	// Publish the grown peer set to the lock-free receive drainer (newReceiveFunc) so the
	// new peer's resequencer is drained once Open builds it.
	m.republishPeersLocked()
	return nil
}

// SetPrimaryPeerName re-keys the embedded primary (peers[0]) from its NewMultipath-assigned
// name "" to the configured multi-peer identity name, so a concentrator's first-configured
// peer carries its own name on /metrics like every other bound peer instead of leaking as
// peer="" (D58). The concentrator wiring (device.Up) calls this with ids[0].Name exactly
// when more than one peer is configured, BEFORE registering any additional peer via
// AddConcentratorPeer — so a later AddConcentratorPeer's collision checks (name == m.name,
// the peersByName duplicate check) compare against the FINAL primary name and correctly
// reject a genuine clash. The single-peer edge/hub never calls this, so its primary keeps
// name="" — byte-identical exposition (T94). TearDownPeer's primary-refusal is unaffected:
// teardownPeerLocked keys on IDENTITY (p == m.peerState), never on name, so renaming the
// primary does not change its teardown-immunity. Must be called before Open (like
// AddConcentratorPeer) since it mutates peer identity the Open-built views assume stable.
func (m *Multipath) SetPrimaryPeerName(name string) error {
	if name == "" {
		return errors.New("bind: primary peer name is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.paths) != 0 {
		return errors.New("bind: primary peer name must be set before Open")
	}
	if _, dup := m.peersByName[name]; dup {
		return fmt.Errorf("bind: primary peer name %q collides with an already-registered concentrator peer", name)
	}
	delete(m.peersByName, m.name)
	m.name = name
	m.peersByName[name] = m.peerState
	m.republishPeersLocked()
	return nil
}

// Open binds one UDP socket per configured path to that path's source address on
// port (0 = random per socket), sets a large SO_RCVBUF on each, spawns one
// Bind-owned reader per path, and returns a SINGLE engine-facing ReceiveFunc (the
// fan-in drainer) plus the first path's bound port. Each path whose config carries
// a dest_addr — or, failing that, the peer's wireguard endpoint learned via
// ParseEndpoint — starts with a known remote; the rest are learned from inbound
// traffic.
//
// Open is the sole creator of the per-path sockets: the engine's bring-up path
// calls Close() first (on the still-unopened bind) and then Open(), so building
// the sockets HERE — not in NewMultipath — is what makes that Close→Open sequence
// (and every subsequent Down→Up) work. The engine passes the previously-bound
// port back on a re-Open; each path binds to it on its own distinct source
// address, and the first path's bound port is returned so the engine keeps a
// stable listen port across the cycle (matching conn.StdNetBind).
func (m *Multipath) Open(port uint16) (receiveFuncs []ReceiveFunc, boundPort uint16, retErr error) {
	m.transitionMu.Lock()
	defer m.transitionMu.Unlock()

	m.mu.Lock()
	if len(m.paths) != 0 {
		m.mu.Unlock()
		return nil, 0, conn.ErrBindAlreadyOpen
	}
	m.openGeneration.Add(1)
	cleanupOnError := true
	defer func() {
		if !cleanupOnError {
			m.mu.Unlock()
			return
		}
		retirement := m.detachSocketGenerationsLocked()
		m.mu.Unlock()
		if err := retirement.retire(); retErr == nil {
			retErr = err
		}
		m.readersWG.Wait()
		m.clearPerOpenStateAfterReaders()
	}()

	// (Re)build every bound peer's receive resequencer fresh for this bring-up. This
	// keeps Open symmetric with generation retirement, which clears every peer's
	// per-Open state: a concentrator peer bound before Open must get its OWN fresh
	// resequencer on each Close→Open cycle, and one peer's (re)creation must never
	// touch another peer's release point.
	for _, p := range m.peers {
		p.resequencer.Store(reseq.New(resequencerWindow, resequencerTimeout, m.clock))
	}

	// Resolve every path's interface up front and decide, per path, whether its
	// socket may be device-bound (SO_BINDTODEVICE + wildcard — survives a T16
	// re-roam) or must pin the specific source IP (the pre-T16 behaviour, required
	// when paths share an interface or the interface is multi-address, so distinct
	// specific-IP sockets coexist on one port without an EADDRINUSE collision). See
	// selectDeviceBinds.
	srcs := make([]netip.Addr, len(m.defs))
	modes := make([]config.BindMode, len(m.defs))
	for i := range m.defs {
		srcs[i] = m.defs[i].SourceAddr
		modes[i] = m.defs[i].Bind
	}
	bindDevs := planPathBinds(srcs, modes)

	// A path whose WELL-FORMED source_addr is merely NOT-YET-ASSIGNABLE at boot
	// (EADDRNOTAVAIL: no interface holds the address — a 5G modem without a DHCP
	// lease, Starlink mid-obstruction) is DEFERRED rather than treated as fatal, so
	// the bond comes up on the paths that DO bind (G2/W1, approach A): the deferred
	// path's prober stays StateDown and it has no socket, so the transport never
	// learns a lane on it. A MALFORMED source_addr never reaches here (config.validate
	// rejects it at load), and any OTHER bind error (EADDRINUSE, permission) is fatal.
	m.deferred = nil

	actualPort := port
	firstBound := true
	for i := range m.defs {
		def := m.defs[i]
		// Device-bind this path when selectDeviceBinds proved it safe (so a
		// mid-session source-address change / T16 re-roam does not break the socket),
		// otherwise pin the specific source IP. See selectDeviceBinds / listenPath.
		c, deviceErr, err := listenPath(def.SourceAddr, port, bindDevs[i])
		if err != nil {
			if errors.Is(err, syscall.EADDRNOTAVAIL) {
				// Defer this path: record its def + boot prober (kept Down) for the T55
				// background reconcile to retry, and leave the bond to come up on the rest.
				// Mirrors AddPath's rollback discipline — a failed path never disturbs the
				// tunnel — at the boot boundary. No socket materialized (the source-IP-pin
				// fallback failed too), so warnForcedDeviceStillDeferred — not
				// warnForcedDeviceUnresolvable — is the accurate, non-fallback-claiming
				// WARN here (D53 round 2 / FIX 2); it seeds the fresh deferredPath's dedup
				// latch so reconcileDeferred's first retry tick does not immediately
				// re-WARN the SAME condition (FIX 1).
				warned := m.warnForcedDeviceStillDeferred(def.Name, def.Bind, bindDevs[i], false)
				m.deferred = append(m.deferred, deferredPath{def: def, prober: m.probers[i], warnedUnresolvable: warned})
				continue
			}
			return nil, 0, fmt.Errorf("bind: open path %q on %s: %w", def.Name, def.SourceAddr, err)
		}
		// The listen succeeded: a working conn materialized. The D53 fallback-fact WARNs
		// are deferred past the peer fan-out below (round 3 / CRITICISM 1): a peer's
		// codec build or prober-fan-out desync in that loop aborts this ENTIRE Open call
		// (generation retirement + a returned error), so warning here — before the path is
		// actually installed into every peer's paths — would log an
		// outcome-false "falling back to source-IP pinning" claim for a bond that never
		// came up at all.
		//
		// Large SO_RCVBUF, best-effort: the kernel caps at net.core.rmem_max and
		// SetReadBuffer does not require privilege, so a returned error is rare;
		// treat it as non-fatal rather than refusing to bind in a restricted env.
		_ = c.SetReadBuffer(socketRecvBuffer)

		shared := &sharedPathState{
			name:        def.Name,
			id:          uint8(i),
			src:         def.SourceAddr,
			conn:        c,
			bindMode:    def.Bind,
			boundDevice: bindDevs[i],
		}
		// Own the socket before constructing any peer view so every later error
		// unwinds it through the same generation-retirement barrier.
		m.shared = append(m.shared, shared)
		// Build EVERY bound peer's view of this shared socket (T93): each peer decodes under its
		// OWN psk-derived Codec and probes with its OWN per-(peer,path) prober, so a concentrator
		// socket shared by several peers keeps each peer's authenticated stream isolated
		// (demuxInbound source-demuxes on the first authenticated PROBE). The primary (peers[0])
		// is built exactly as the pre-split single peer — byte-identical on the single-peer
		// edge/hub, where m.peers holds only the primary. m.paths (the primary's path slice,
		// reached through the embed) grows via the pi==0 append below; a concentrator peer's
		// paths grow on its own peerState.
		for pi, p := range m.peers {
			// The path binds to peer p; its receive codec is p's codec (derived from p's psk).
			codec, err := p.newCodec()
			if err != nil {
				return nil, 0, err
			}
			pp := &peerPathState{sharedPathState: shared, peer: p, codec: codec}
			// Fail fast rather than panic if a peer's prober slice ever falls short of the
			// shared m.defs membership: every runtime admission (bound OR deferred) fans a
			// per-peer prober out to EVERY peer (AddPath), so p.probers stays index-aligned
			// with m.defs. A divergence is a wiring defect — surface it as a bind error
			// instead of an index-out-of-range panic that would crash the daemon.
			if i >= len(p.probers) {
				return nil, 0, fmt.Errorf("bind: peer %q prober set (len %d) is shorter than the path membership at index %d — per-peer prober fan-out desync", p.name, len(p.probers), i)
			}
			pp.prober = p.probers[i]
			if pi == 0 {
				// Reconcile the SHARED path-id to the PRIMARY prober's IMMUTABLE stamp
				// rather than the slice index i: after a runtime RemovePath the survivor
				// keeps its original (higher) stamp, so index-based numbering would renumber a
				// live path AND diverge its lane id from its PROBE stamp. Every peer's
				// probers[i] carries the SAME stamp (the device stamps each peer's boot prober
				// for path i identically), so taking it from the primary is authoritative.
				shared.id = pp.prober.PathID()
			}
			switch {
			case def.DestAddr.IsValid():
				// A path-specific dest_addr (multi-address fronting of the peer's active
				// concentrator) wins over the peer/bind default.
				pp.setRemote(def.DestAddr)
			case p.hasConfiguredRemote:
				// This peer's OWN configured concentrator endpoint (multi-exit edge, T251) — so
				// each edge peer's paths reach ITS concentrator, not a single bind-global default
				// that would collapse every peer onto one hub (the last ParseEndpoint's address).
				pp.setRemote(p.configuredRemote)
			case len(m.edgePeerByRemote) == 0 && m.hasDefaultRemote:
				// Single-peer edge/hub: the bind-global default (ParseEndpoint's sole endpoint).
				// Deliberately NOT used in multi-exit edge mode (edgePeerByRemote non-empty): there
				// a peer without its own configuredRemote booted endpoint-less (tolerant boot) and
				// must stay remoteless until its endpoint is installed, not inherit another's hub.
				pp.setRemote(m.defaultRemote)
			}
			pp.pmtuProbe = m.buildPMTUProbe(pp)
			p.paths = append(p.paths, pp)
			// Publish this peer's view for the receive demux (T88). A single-view socket is the
			// edge/hub byte-identical fast path in demuxInbound; a socket with >1 view is
			// source-demuxed to its owning peer on each authenticated PROBE.
			shared.addViewLocked(pp)
		}
		// Every peer is now wired to this path (paths + receive-demux view) — the socket
		// is actually installed, so the fallback facts these log are backed by a real,
		// live, staying-up path, not a claim (D53 round 2 / FIX 2; ordering — round 3 /
		// CRITICISM 1).
		m.warnForcedDeviceUnresolvable(def.Name, def.Bind, def.SourceAddr, bindDevs[i])
		m.warnDeviceBindFallback(def.Name, def.Bind, bindDevs[i], deviceErr)
		if firstBound {
			actualPort = uint16(c.LocalAddr().(*net.UDPAddr).Port)
			firstBound = false
		}
	}

	// Hard guard (a): a bond with NO transport is impossible. If every configured path
	// deferred, tolerance must NOT degrade to a zero-path bind — fail fatally, exactly
	// as the pre-tolerance Open did when the sole path could not bind.
	if len(m.paths) == 0 {
		return nil, 0, fmt.Errorf("bind: no configured path could bind its source address (all %d deferred as not-yet-assignable)", len(m.deferred))
	}

	// nextPathID is a MONOTONIC HIGH-WATER carried ACROSS Open spans, never decreased.
	// Close does not reset it, and here it is only ever RAISED to at least max(existing
	// path stamp)+1. Resetting it to len(m.paths) (the pre-fix behaviour) re-minted an id
	// a survivor still held once a RemovePath had opened a gap in the stamp space below
	// that survivor's stamp: the next runtime AddPath then collided with the live path at
	// an identical (PathID, SessionID), and because the peer's Reflector keys anti-replay
	// AND the session challenge PER PathID, the strict-monotonic replay filter dropped one
	// of the two independent ProbeSeq streams -> probe loss / false-DOWN.
	m.openPort = port
	for _, ps := range m.paths {
		if uint16(ps.id)+1 > m.nextPathID {
			m.nextPathID = uint16(ps.id) + 1
		}
	}
	// A DEFERRED path's boot prober already OWNS its path-id stamp; raise the
	// high-water past it too, so a runtime AddPath arriving while the path is still
	// deferred cannot mint that reserved id and collide with it once T55 binds it.
	for _, dp := range m.deferred {
		if dp.prober != nil {
			if uint16(dp.prober.PathID())+1 > m.nextPathID {
				m.nextPathID = uint16(dp.prober.PathID()) + 1
			}
		}
	}

	// Stand up the receive fan-in: one Bind-owned reader per path feeding the shared
	// resequencer, and a single engine-facing drainer. Both channels are recreated
	// per Open so a Close→Open cycle starts clean.
	m.deliverSignal = make(chan struct{}, 1)
	m.recvClosed = make(chan struct{})
	for _, peer := range m.peers {
		if err := m.openAdaptivePeer(peer); err != nil {
			return nil, 0, err
		}
		if rq := peer.resequencer.Load(); rq != nil {
			rq.SetNotifier(resequencerNotifier(m.deliverSignal))
		}
	}
	for _, ps := range m.paths {
		m.readersWG.Add(1)
		go m.readLoop(ps, m.deliverSignal)
	}
	cleanupOnError = false
	return []ReceiveFunc{m.newReceiveFunc(m.deliverSignal, m.recvClosed)}, actualPort, nil
}

// ensurePeerReceiveInstantiated rebuilds a peer's receive resequencer on the first
// authenticated source->peer binding after a teardown reclaimed it (teardownPeerLocked),
// so a configured concentrator peer that is not reached carries none of that per-peer
// memory (Q26). It publishes through the same atomic.Pointer the receive fast path Loads
// and runs on the Bind-owned readLoop goroutine, which stays independent of transport
// mutation — so it takes ONLY the per-peer lifecycleMu, never m.mu. That lifecycleMu
// makes the build-and-publish mutually exclusive with teardownPeerLocked's clearing. It
// is idempotent (the lifecycleMu-guarded resequencer==nil double-check elects a single
// instantiator when two sockets bind the same peer concurrently; the loser returns
// without building).
func (m *Multipath) ensurePeerReceiveInstantiated(ps *peerState) {
	if ps.resequencer.Load() != nil {
		return // fast path: already instantiated (at Open, or by a prior binding)
	}
	ps.lifecycleMu.Lock()
	defer ps.lifecycleMu.Unlock()
	if ps.resequencer.Load() != nil {
		return // a concurrent bind instantiated it while we waited on lifecycleMu
	}
	rq := reseq.New(resequencerWindow, resequencerTimeout, m.clock)
	rq.SetNotifier(resequencerNotifier(m.deliverSignal))
	ps.resequencer.Store(rq)
}

func resequencerNotifier(deliver chan<- struct{}) func() {
	return func() {
		select {
		case deliver <- struct{}{}:
		default:
		}
	}
}

// readLoop is one Bind-OWNED per-path receive goroutine (T30). It reads the path
// socket and dispatches every datagram through demuxInbound — which hands CONTROL
// frames to the peer's transport and answers/consumes PROBEs — then pokes the delivery
// signal so the single engine-facing drainer wakes to release any newly in-order
// frame. The read buffer is private to this goroutine, so the per-path Codec's
// scratch is never shared.
//
// Owning the readers in the Bind (rather than returning one ReceiveFunc per path to
// the engine, as T12 did) is what makes a runtime path add/remove possible without
// disturbing the engine: the engine builds its receive goroutines ONCE from Open's
// return, so a path added later gets a reader HERE while the engine's fixed receive
// set — and thus the WG session — sees no churn. The goroutine exits when its socket
// is closed (RemovePath drains one path; Close drains all).
func (m *Multipath) readLoop(ps *peerPathState, deliver chan<- struct{}) {
	defer m.readersWG.Done()
	readBuf := make([]byte, maxDatagram)
	for {
		n, srcAP, err := ps.conn.ReadFromUDPAddrPort(readBuf)
		if err != nil {
			return // socket closed: this path was removed, or the bind was closed
		}
		m.demuxInbound(ps, readBuf[:n], srcAP)
		// Advance liveness off the receive path (throttled): a live signal on THIS path
		// is what lets a DIFFERENT, silent path be marked DOWN promptly even when the
		// probe-loop ticker is starved under load (D15). The peer probes every path
		// (and floods the survivor after it roams), so the surviving path's reader
		// supplies exactly that signal.
		m.tickLivenessFromReceive(time.Now())
		// Non-blocking poke: a single buffered slot coalesces bursts; the drainer
		// re-checks the resequencer on every wake, so a coalesced poke loses nothing.
		select {
		case deliver <- struct{}{}:
		default:
		}
	}
}

// tickLivenessFromReceive re-evaluates EVERY path's liveness from a receive
// goroutine, throttled to at most once per probe interval (T39, defect D15). It is
// the starvation-robust companion to StartProbeLoop's timer-driven Tick: the timer
// goroutine can be delayed ~1s under CPU saturation, but the receive goroutines are
// scheduled by the very traffic that must trigger failover, so liveness advances
// regardless. Ticking is monotone-safe: Tick only marks an UP path DOWN once its
// silence STRICTLY exceeds DownAfter and never brings a path UP (that needs
// RecordEcho), so a more frequent Tick can only make a genuine DOWN transition land
// sooner — never a premature/false one. No-op when the probe transport is absent
// (interval unset).
func (m *Multipath) tickLivenessFromReceive(now time.Time) {
	interval := m.sweepIntervalNanos.Load()
	if interval == 0 {
		return // no probe transport / loop not started: nothing to Tick
	}
	n := now.UnixNano()
	last := m.lastSweepNanos.Load()
	if n-last < interval {
		return // throttled: this interval's sweep was already taken
	}
	if !m.lastSweepNanos.CompareAndSwap(last, n) {
		return // another receive goroutine won this interval's sweep
	}
	// Snapshot the prober set under m.mu (a runtime AddPath/RemovePath mutates it),
	// then Tick OUTSIDE the lock so a transition's log write never runs under m.mu.
	// TryLock, NOT Lock: the sweep is opportunistic and throttled, so when the lock
	// is contended (a concurrent
	// Close/AddPath/RemovePath/Send) simply skipping this interval is harmless — the
	// probe-loop ticker and the next receive still advance liveness. This preserves
	// the receive hot path's non-blocking discipline. The lock, when taken, is held
	// at most once per interval (~5/s) for a bounded snapshot, so it adds negligible
	// contention to Send and does not disturb the lock-free receive fast path.
	if !m.mu.TryLock() {
		return
	}
	// Sweep EVERY bound peer's paths (T93): the receive-tick liveness signal must advance each
	// peer's own probers so a concentrator peer's silent path is marked DOWN promptly even when
	// the probe-loop ticker is starved. On the single-peer edge/hub m.peers holds only the
	// primary, so this is byte-identical to the pre-split single-peer sweep.
	probers := make([]*telemetry.Prober, 0, len(m.paths))
	for _, p := range m.peers {
		for _, ps := range p.paths {
			if ps.prober != nil {
				probers = append(probers, ps.prober)
			}
		}
	}
	m.mu.Unlock()
	for _, pr := range probers {
		pr.Tick()
	}
}

// newReceiveFunc returns the SINGLE engine-facing ReceiveFunc: it drains EACH bound peer's
// resequencer in that peer's own delivery order and hands each inner datagram up stamped
// with THAT peer's stable virtual endpoint (per-packet endpoint fill), so the engine
// attributes return traffic to the right peer and Send routes replies back via that peer's
// virt (invariant A1: one virtual endpoint per peer). A path's reader (handleInbound) has
// already routed each frame to its OWNING peer's transport via the peerPathState's
// ps.peer back-reference, so a shared socket serving many peers keeps each peer's stream
// isolated; this drainer just fans the in-order releases back in. Because every path's
// reader feeds one of these resequencers and a single drainer releases them, a path (or
// peer) added or removed at runtime needs no change to the engine's receive set.
//
// Fairness: the peers are scanned round-robin from a rotating cursor so a saturated peer
// cannot starve another peer's in-order releases (single-peer edge/hub: the cursor is a
// no-op). When nothing is ready across ANY peer it parks until a reader pokes deliver,
// until Close closes closed, or until a short poll elapses — the poll guarantees a
// head-of-line-blocked run still makes timeout progress even if the last live path fell
// silent right after buffering it. A single drainer delivers with ZERO added reorder
// (only it calls Pop).
// Bulk coalesces for at most adaptiveReceiveBatchDelay for TUN GRO;
// interactive arrivals flush immediately.
func (m *Multipath) newReceiveFunc(deliver <-chan struct{}, closed <-chan struct{}) ReceiveFunc {
	// One reusable injected-clock timer per drainer. Every empty scan resets it to
	// the earliest exact armed gap deadline, with T as the conservative poll
	// fallback when no gap is armed.
	timer := m.clock.NewTimerAt(m.clock.Now())
	timer.Stop()
	// rr is the round-robin cursor, advanced past a peer each time it yields a frame so
	// the next receive starts at the following peer. It is touched only by this single
	// engine goroutine, so it needs no synchronisation.
	var rr int
	return func(packets [][]byte, sizes []int, eps []Endpoint) (int, error) {
		count := 0
		var batchUntil time.Time
		for {
			select {
			case <-closed:
				timer.Stop()
				return 0, errClosed
			default:
			}
			// Scan every bound peer round-robin for an in-order resequenced datagram.
			// The item carries the outer source of the frame that produced it, so the
			// peer's virtual endpoint pins correctly even when the frame was buffered and
			// released out of arrival order. The peer view is read lock-free (peersView),
			// so a concurrent peer wiring/fan-out never contends m.mu here.
			peers := *m.peersView.Load()
			n := len(peers)
			progressed := false
			for i := 0; i < n; i++ {
				ps := peers[(rr+i)%n]
				rq := ps.resequencer.Load()
				if rq == nil {
					continue // a peer not yet Open on this span has no resequencer
				}
				var it reseq.Item
				var ok bool
				interactive := false
				adaptive := ps.adaptive.Load()
				if adaptive != nil {
					it, ok = adaptive.popInteractive()
					interactive = ok
				}
				if !ok {
					it, ok = rq.Pop()
				}
				if !ok {
					continue
				}
				rr = (rr + i + 1) % n // start the next scan after this peer (fairness)
				if len(it.Payload) > len(packets[count]) {
					// Oversize inner datagram: drop it, but a frame WAS dequeued this pass,
					// so keep draining (re-scan) rather than parking.
					progressed = true
					break
				}
				sizes[count] = copy(packets[count], it.Payload)
				eps[count] = m.virtualEndpoint(ps, it.Src)
				count++
				if count == len(packets) || interactive {
					return count, nil
				}
				if count == 1 && adaptive != nil {
					batchUntil = m.clock.Now().Add(adaptive.receiveBatchDelay(len(it.Payload)))
				}
				progressed = true
				break
			}
			if progressed {
				continue // re-scan after delivering or dropping a frame
			}
			wakeAt := earliestResequencerDeadline(peers, m.clock.Now().Add(resequencerTimeout))
			if count > 0 {
				if batchUntil.IsZero() || !m.clock.Now().Before(batchUntil) {
					return count, nil
				}
				if batchUntil.Before(wakeAt) {
					wakeAt = batchUntil
				}
			}
			timer.ResetAt(wakeAt)
			if m.beforeReceivePark != nil {
				m.beforeReceivePark(wakeAt)
			}
			select {
			case <-deliver:
				timer.Stop()
			case <-timer.C():
				// Poll fired; its channel is already drained by this receive.
			case <-closed:
				timer.Stop()
				return 0, errClosed
			}
		}
	}
}

func earliestResequencerDeadline(peers []*peerState, fallback time.Time) time.Time {
	earliest := fallback
	for _, peer := range peers {
		rq := peer.resequencer.Load()
		if rq == nil {
			continue
		}
		if deadline, armed := rq.ArmedDeadline(); armed && deadline.Before(earliest) {
			earliest = deadline
		}
	}
	return earliest
}

// handleInbound decodes one received outer datagram UNDER THIS VIEW'S CODEC and dispatches
// it by kind to the view's owning peer. It is the single per-frame receive action; the
// readLoop reaches it through demuxInbound, which first resolves the owning view on a shared
// concentrator socket (on the single-peer edge/hub the reader's own view is the owner, so
// demuxInbound's fast path calls this directly — byte-identical to the pre-concentrator
// behaviour). Delivery up the WG
// path is deferred to the resequencer (Pop, in the engine-facing drainer): a bulk
// datagram is not handed up here but pushed into the peer's resequencer to be released
// in sequence order.
//
//   - CONTROL: handed to the peer's transport, whose bulk deliveries are pushed
//     into the resequencer keyed by their sequence (delivered later, in order,
//     via Pop). The path's remote is NOT learned here — remote-learning is
//     PROBE-only (see below).
//   - PROBE, IsEcho=false: an authenticated peer probe. Its source is learned as
//     the path's remote (D11) and it is reflected straight back to that source via
//     this path's socket (T13 Reflector). Reflection writes independently of
//     getRemote so an echo returns even on a not-yet-selected path.
//   - PROBE, IsEcho=true: an authenticated echo of one of our own probes. Its
//     source is learned as the remote too, and the raw echo is fed into this
//     path's Prober (HandleEchoProbe) to update RTT/loss and drive liveness.
//   - anything else (malformed, or failing its MAC): dropped.
//
// Remote-learning and reflection touch only authenticated (MAC-verified) PROBE
// frames — Decode has already verified the tag — which is what resolves D9: an
// attacker without the psk cannot repoint a path's return endpoint.
func (m *Multipath) handleInbound(ps *peerPathState, raw []byte, srcAP netip.AddrPort) {
	fr, err := ps.codec.Decode(raw)
	if err != nil {
		return // drop malformed / PSK-mismatched outer frames
	}
	m.dispatchInbound(ps, fr, raw, srcAP)
}

// demuxInbound is the readLoop's per-frame entry on a (possibly shared) socket: it resolves
// which bound peer's view owns the datagram, then hands the frame to handleInbound on THAT
// view (T88). Because the readLoop holds only the PRIMARY's view of a shared socket (one
// reader per socket), the routing that a concentrator needs — a datagram from peer B decoded
// and resequenced under peer B's plane — happens HERE, not by trusting the reader's own view.
//
// Fast path: a socket with a single peer view (the edge/hub, or any socket not shared by a
// second peer) needs no demux — dispatch on the reader's own view, byte-identical to the
// pre-concentrator behaviour. The per-socket view snapshot is read lock-free; a nil/one-entry
// snapshot means "not a shared concentrator socket".
//
// Shared socket (>1 view):
//   - A source already bound by a prior authenticated PROBE routes straight to that peer's
//     view of THIS socket (one lock-free map Load). A frame that does not verify under that
//     peer's psk — a spoofed source it cannot forge the MAC for — is dropped by handleInbound.
//   - An unbound source is trial-decoded against each peer's psk-derived codec (O(peers),
//     bounded by the static peer count). Only an authenticated PROBE establishes a binding
//     (D9/D11: bindings, like remotes, are learned only from authenticated PROBEs), and a
//     PROBE's MAC verifies under EXACTLY ONE psk — so the FIRST psk whose codec yields a PROBE
//     identifies the peer, binds the source, dispatches, and the loop STOPS there. Every
//     kind carries a MAC, so a decode under another peer's psk fails and a non-PROBE decode
//     is a genuine CONTROL frame of that peer. It carries no binding authority and is
//     dropped — a CONTROL from a not-yet-bound source never dispatches or binds until that
//     peer's PROBE binds it. A forged/garbage frame verifies as a PROBE under NO psk, binds
//     nothing, and is dropped cheaply.
func (m *Multipath) demuxInbound(ps *peerPathState, raw []byte, srcAP netip.AddrPort) {
	views := ps.views.Load()
	if views == nil || len(*views) <= 1 {
		m.handleInbound(ps, raw, srcAP)
		return
	}
	if bound, ok := m.lookupPeerBySource(srcAP); ok {
		for _, v := range *views {
			if v.peer == bound {
				m.handleInbound(v, raw, srcAP)
				return
			}
		}
		// D62: the source is bound to a peer that holds NO view of this socket — the peer's
		// views were torn down (a bind racing unbindPeerSources, or a session-loss teardown)
		// while its stale demux binding survived. Dropping here would wedge the source
		// FOREVER (lookupPeerBySource keeps returning the dead peer, so it never re-binds).
		// Instead FALL THROUGH to the trial-decode loop below: it re-authenticates the PROBE
		// against the live peers' codecs and re-points the binding (bindSourceToPeer re-affirm)
		// to the peer that actually owns the source, self-healing the stale binding.
	}
	for _, v := range *views {
		fr, err := v.codec.Decode(raw)
		if err != nil {
			continue // not this peer's psk — try the next
		}
		if _, isProbe := fr.(frame.Probe); !isProbe {
			// Authenticated under this psk but not a PROBE: a CONTROL frame from a
			// source no PROBE has bound yet. No binding authority, so it never binds.
			continue
		}
		if !m.bindSourceToPeer(srcAP, v.peer) {
			// This peer is below its per-peer quota yet the GLOBAL demux cap is exhausted
			// (cross-peer drop-on-exhaustion): drop this new source's PROBE rather than grow the
			// map past the cap or steal another peer's headroom (Q26/Q27). WG retransmits re-drive
			// the bootstrap once a slot frees. (A peer AT its own quota is never dropped here — it
			// self-evicts its oldest binding, D49.)
			return
		}
		// The binding just resolved this peer: lazily materialise its heavy receive datapath
		// (resequencer ring) BEFORE dispatch, so a configured peer that
		// had never been reached — and a peer whose state was torn down on session loss — pays
		// that memory only from its first authenticated binding onward (Q26).
		m.ensurePeerReceiveInstantiated(v.peer)
		m.dispatchInbound(v, fr, raw, srcAP) // already decoded: dispatch without re-decoding
		return
	}
	// No peer's psk verified: a forged/garbage frame. No binding, drop.
}

// lookupPeerBySource resolves a learned source AddrPort to the peer bound to it, or nil when
// the source is not yet bound. It reads the copy-on-write binding map with a single lock-free
// Load, so the concentrator receive hot path never takes m.mu (see peerBySource).
func (m *Multipath) lookupPeerBySource(srcAP netip.AddrPort) (*peerState, bool) {
	mp := m.peerBySource.Load()
	if mp == nil {
		return nil, false
	}
	b, ok := (*mp)[srcAP]
	return b.peer, ok
}

// perPeerQuota is the per-peer share of maxDemuxSources (the GLOBAL cap) that any single peer
// may occupy in the demux map: maxDemuxSources/len(peers), floored at 1 (D49). len(peers) is
// read from the lock-free peersView snapshot (never nil after construction), so the quota is
// computed WITHOUT m.mu on the bind path. Caller guards on maxDemuxSources > 0.
func (m *Multipath) perPeerQuota() int {
	numPeers := 1
	if pv := m.peersView.Load(); pv != nil && len(*pv) > numPeers {
		numPeers = len(*pv)
	}
	quota := m.maxDemuxSources / numPeers
	if quota < 1 {
		quota = 1
	}
	return quota
}

// bindSourceToPeer records srcAP→p in the source-demux map, installed lock-free by a CAS
// republish of a copy with the entry added (T88). It takes NO lock: a reader must never block
// on m.mu, so the binding — written from a
// readLoop goroutine — must not acquire it. Idempotent: an already-present srcAP→p binding is a
// no-op, and a lost CAS (a concurrent bind on another socket) simply retries. Copy-on-write
// keeps every published map immutable, so a concurrent lookupPeerBySource over the old snapshot
// is never disturbed.
//
// The map is keyed by the full source AddrPort (D47): a peer that roams to a NEW port behind the
// same CGNAT address is a NEW key, and two peers behind one public IP occupy distinct keys.
//
// Cap/quota discipline for a NEW srcAP key (D49). The GLOBAL cap is maxDemuxSources; within it
// each peer's share is perPeerQuota (maxDemuxSources/len(peers), floor 1):
//   - SAME-peer roam churn: if p is ALREADY at its per-peer quota, admit the new AddrPort by
//     EVICTING p's OWN oldest binding in INSERTION ORDER (FIFO within p, by sourceBinding.seq).
//     NOTE (D63): seq is stamped once at first bind (below) and is NOT refreshed when an
//     already-present AddrPort is re-affirmed, so this is first-bound-first-evicted (FIFO), NOT
//     last-recently-used (LRU) — the insertion-order policy the T123 plan decision explicitly
//     sanctioned. p's footprint stays at quota, a live roaming peer is NEVER dropped, and p can
//     never evict ANOTHER peer's slot — so never-evict-live holds w.r.t. every other peer and
//     cross-peer isolation is total.
//   - CROSS-peer exhaustion: if p is BELOW its quota but the GLOBAL cap is full (only reachable
//     when the floor-1 quotas sum past the cap), drop-on-exhaustion — return false. p may not
//     evict another peer's binding to grow past the cap; bootstrap degrades, WG retransmits cover
//     the gap once a slot frees (e.g. a dead peer's teardown).
//
// Returns true when srcAP is bound to p on return (freshly installed — possibly after an own-LRU
// eviction — already present, or re-pointed from another peer, a roam T90) and false ONLY in the
// cross-peer exhaustion case above. Re-pointing or re-affirming an already-present AddrPort does
// not grow the map and is therefore never blocked.
func (m *Multipath) bindSourceToPeer(srcAP netip.AddrPort, p *peerState) bool {
	for {
		old := m.peerBySource.Load()
		var n int
		present := false
		if old != nil {
			existing, ok := (*old)[srcAP]
			if ok {
				present = true
				if existing.peer == p {
					return true // already bound to p: idempotent no-op
				}
			}
			n = len(*old)
		}

		// Cap/quota enforcement applies ONLY to a NEW key: a re-point of an existing AddrPort
		// does not grow the map (T90 roam re-affirm), so it is never blocked.
		evict := false
		var evictKey netip.AddrPort
		if !present && m.maxDemuxSources > 0 {
			quota := m.perPeerQuota()
			countP := 0
			var oldestSeq uint64
			if old != nil {
				for k, b := range *old {
					if b.peer != p {
						continue
					}
					if countP == 0 || b.seq < oldestSeq {
						oldestSeq = b.seq
						evictKey = k
					}
					countP++
				}
			}
			switch {
			case countP >= quota:
				// p is at its per-peer quota: admit by evicting p's OWN oldest binding in
				// insertion order (FIFO by sourceBinding.seq — see the bindSourceToPeer doc;
				// the T123-sanctioned policy, D63). countP >= quota >= 1, so an oldest binding
				// of p's exists.
				evict = true
			case n >= m.maxDemuxSources:
				// p is below its quota but the GLOBAL cap is exhausted: p may not steal another
				// peer's headroom. Drop-on-exhaustion (cross-peer isolation).
				return false
			}
		}

		size := n + 1
		if evict {
			size = n // one out, one in: net-zero growth
		}
		next := make(map[netip.AddrPort]sourceBinding, size)
		if old != nil {
			for k, b := range *old {
				if evict && k == evictKey {
					continue // drop p's own oldest binding to make room for the new AddrPort
				}
				next[k] = b
			}
		}
		next[srcAP] = sourceBinding{peer: p, seq: m.bindSeq.Add(1)}
		if m.peerBySource.CompareAndSwap(old, &next) {
			return true
		}
		// Lost the race with a concurrent bind: reload and retry.
	}
}

// unbindPeerSources removes every source->peer entry pointing at p from the demux map (a CAS
// republish of a copy with p's entries dropped), reclaiming their cap slots so a fresh
// authenticated PROBE can re-bind after the peer is re-instantiated. Like bindSourceToPeer it
// is lock-free copy-on-write (retry on a lost CAS), so it composes with a concurrent bind on a
// readLoop goroutine without either blocking on m.mu. A no-op when p holds no bindings.
func (m *Multipath) unbindPeerSources(p *peerState) {
	for {
		old := m.peerBySource.Load()
		if old == nil {
			return
		}
		found := false
		for _, b := range *old {
			if b.peer == p {
				found = true
				break
			}
		}
		if !found {
			return
		}
		next := make(map[netip.AddrPort]sourceBinding, len(*old))
		for k, b := range *old {
			if b.peer != p {
				next[k] = b
			}
		}
		if m.peerBySource.CompareAndSwap(old, &next) {
			return
		}
	}
}

// peerIsLiveLocked reports whether ANY of peer p's paths is currently StateUp — the liveness
// gate that makes teardown safe: a peer with a live path is actively carrying (or about to
// carry) traffic and must NEVER be torn down (Q26). It reads each path's own immutable prober
// State() (internally synchronized), so it is safe under m.mu. A peer
// with no prober-bearing path (a bind without the probe transport) reports not-live, matching
// the fact that such a bind has no liveness signal to protect.
func (m *Multipath) peerIsLiveLocked(p *peerState) bool {
	for _, pp := range p.paths {
		if pp.prober != nil && pp.prober.State() == telemetry.StateUp {
			return true
		}
	}
	return false
}

// teardownPeerLocked frees a dead peer's receive resequencer ring and releases its
// source->peer demux bindings, reclaiming both the memory and the demux-map cap slots
// (Q26). It is the lifecycle dual of ensurePeerReceiveInstantiated: after teardown the
// peer is dormant (its light state — psk, codec, reflector, per-(peer,path) views —
// survives so a trial-decode still authenticates it), and the next authenticated PROBE
// re-binds a source and re-instantiates the ring cleanly. It REFUSES to tear down a LIVE
// peer (any path StateUp) and the embedded primary (the edge/hub, whose lifecycle is
// Open/Close, not session teardown), returning false in both cases so a caller can
// distinguish "torn down" from "kept". The resequencer is an atomic.Pointer, so
// clearing it is safe against a concurrent readLoop (which nil-guards its Load); the
// drainer likewise skips a peer whose resequencer Loads nil. Caller holds m.mu and the
// peer's lifecycleMu; acquiring lifecycleMu happens before m.mu so no wait on
// the peer lifecycle barrier occurs while the bind lock is held.
func (m *Multipath) teardownPeerLocked(p *peerState) (*reseq.Resequencer, bool) {
	if p == m.peerState {
		return nil, false // the primary (edge/hub) is torn down only by Close, never by session loss
	}
	if m.peerIsLiveLocked(p) {
		return nil, false // a live (Up) peer is never torn down, whatever other peers' churn
	}
	return p.resequencer.Swap(nil), true
}

// TearDownPeer frees the heavy per-peer state of the named configured peer once its WireGuard
// session / liveness is gone — the device wires this from its per-peer session events (Q26). It
// is a no-op returning false when the peer is unknown, is the embedded primary, or is still
// LIVE (a live peer is never torn down); it returns true when the peer's resequencer ring
// was freed and its source bindings released. A torn-down configured peer
// re-instantiates cleanly on its next authenticated PROBE (ensurePeerReceiveInstantiated).
func (m *Multipath) TearDownPeer(name string) bool {
	m.mu.Lock()
	p, ok := m.peersByName[name]
	m.mu.Unlock()
	if !ok {
		return false
	}

	// lifecycleMu before m.mu: instantiation holds lifecycleMu without m.mu, so the
	// reverse order here would wait on it with the bind lock held.
	p.lifecycleMu.Lock()
	m.mu.Lock()
	current, stillBound := m.peersByName[name]
	var rq *reseq.Resequencer
	tornDown := false
	if stillBound && current == p {
		rq, tornDown = m.teardownPeerLocked(p)
	}
	m.mu.Unlock()
	if rq != nil {
		rq.Close()
	}
	p.lifecycleMu.Unlock()
	if tornDown {
		m.unbindPeerSources(p)
	}
	return tornDown
}

// EverHadLivePath reports whether ANY configured path, for ANY bound peer, has EVER
// reached liveness telemetry.StateUp since this Bind was constructed (I4). It is
// STICKY: once true it stays true for the Bind's lifetime, even if every path later
// goes down — a total outage AFTER connectivity was established is a genuine failure
// signal, not a startup warmup. The device-package engineLogger adapter consults it to
// gate the coalesced startup no-healthy-path INFO line (see ErrNoHealthyPath) to the
// warmup window only. Safe for concurrent use (backed by atomic.Bool); never blocks.
func (m *Multipath) EverHadLivePath() bool {
	return m.everUp.Load()
}

// SetOnFirstPathUp registers the D37 detection-seam callback: fn is invoked EXACTLY
// ONCE, off the receive hot path, the moment the everUp latch flips false->true (the
// same edge EverHadLivePath's result flips on) — never again, including across a
// later Down->Up->Down->Up cycle (everUp is sticky and never resets). Pass nil to
// clear a previously-set callback (dispatchInbound is nil-safe either way). Callable
// at any time — before or after Open, and safely from a goroutine concurrent with the
// receive path, since it only replaces a lock-free pointer; if the edge already fired
// before this call, fn is NOT retroactively invoked (it is a one-shot EDGE callback,
// not a level-triggered "call me if already up" registration).
func (m *Multipath) SetOnFirstPathUp(fn func()) {
	m.onFirstPathUp.Store(&fn)
}

// SetOnPeerRestart registers fn to be called with the peer's name each time a known
// peer is seen to have restarted. Pass nil to clear it.
func (m *Multipath) SetOnPeerRestart(fn func(peer string)) {
	m.onPeerRestart.Store(&fn)
}

// dispatchInbound handles one already-decoded inbound frame on the resolved peer's view (ps):
// it routes to that peer's transport / reflector. The source demux in
// demuxInbound has already selected ps so a shared socket serving many peers keeps each
// peer's stream on that peer's own state; on the single-peer edge/hub ps.peer is the
// embedded primary. raw is retained for
// the probe transport (HandleEcho / Reflect re-decode it under the peer's psk).
func (m *Multipath) dispatchInbound(ps *peerPathState, fr frame.Frame, raw []byte, srcAP netip.AddrPort) {
	ps.rxBytes.Add(uint64(len(raw)))
	pr := ps.peer
	switch f := fr.(type) {
	case frame.Control:
		if adaptive := pr.adaptive.Load(); adaptive != nil {
			adaptive.receive(ps, srcAP, f)
		}
	case frame.Probe:
		// Authenticated (the PROBE MAC verified in Decode): fold the frame into the
		// per-sender-path freshness table under its stamped path id (T246, defect D94) —
		// establishing/refreshing the return address for THAT sender path, below the
		// engine's virtual endpoint. It never moves the SELECTED destination (except
		// the R253 cold-start first-establishment and an in-place rebind of the
		// selected entry).
		ps.learnRemoteFromProbe(f.PathID, srcAP)
		if f.IsEcho {
			if ps.prober != nil {
				// A replay/forgery/wrong-path echo is rejected inside HandleEcho and
				// leaves liveness untouched; the error is a per-frame drop, not fatal.
				// ps.prober is the path's OWN immutable prober — never a lookup into a
				// dynamically-mutated slice — so runtime add/remove cannot race this.
				fresh, echoErr := ps.prober.HandleEchoProbe(raw)
				if echoErr == nil {
					if adaptive := pr.adaptive.Load(); adaptive != nil {
						adaptive.learn(ps, srcAP, fresh.Payload, false)
					}
				}
				// Release any PMTU search probe awaiting THIS echo (T227, defect D88),
				// matched by ProbeSeq and DECOUPLED from HandleEcho's anti-replay verdict
				// above: a slow padded echo must still complete its await even when a
				// faster liveness echo already advanced the guard high-water past its seq
				// (R245). A no-op for an ordinary liveness echo (no pending PMTU waiter).
				if ps.pmtuProbe != nil {
					ps.pmtuProbe.NotifyEcho(f.ProbeSeq)
				}
				// Sticky "ever had a live path" latch (I4): a fresh echo that just brought
				// this path to StateUp (or found it already Up) flips everUp permanently.
				// Checked here rather than only in Liveness.transition so the bind-level
				// predicate needs no wiring through NewProber/NewLiveness — it observes the
				// SAME state HandleEcho just updated, at the one call site that can ever
				// change Down->Up. The CAS (rather than a plain Store) is what makes the
				// false->true transition observable EXACTLY ONCE: concurrent per-path
				// receive goroutines (one per readLoop) can all reach this line around the
				// SAME moment their own path first goes Up, and only the goroutine whose CAS
				// actually flips false->true fires the D37 callback below — every other
				// goroutine's CAS fails (everUp is already true) and falls through silently,
				// including on a later Down->Up->Down->Up cycle (everUp never resets to
				// false, so CAS never succeeds a second time).
				if ps.prober.State() == telemetry.StateUp && m.everUp.CompareAndSwap(false, true) {
					// Fire off the receive hot path: this call site holds no lock, but a
					// dedicated goroutine keeps an arbitrarily slow or blocking callback from
					// ever stalling the readLoop that just brought the path Up, and keeps
					// every OTHER concurrent readLoop's dispatchInbound (on other paths/peers)
					// un-delayed by it too.
					if cb := m.onFirstPathUp.Load(); cb != nil && *cb != nil {
						go (*cb)()
					}
				}
			}
			return
		}
		accepted, rerr := pr.reflector.AcceptProbe(raw)
		if rerr != nil {
			return
		}
		// The echo of an ordinary probe carries this end's hello; a padded PMTU probe is
		// echoed as it came, so its size is the size that crossed the path.
		echoPayload := accepted.Probe.Payload
		if adaptive := pr.adaptive.Load(); adaptive != nil {
			if accepted.Acceptance != telemetry.ProbeBootstrap {
				adaptive.learn(ps, srcAP, accepted.Probe.Payload, accepted.Acceptance == telemetry.ProbeAdopted)
			}
			if !accepted.Probe.Padded {
				echoPayload = adaptive.hello(ps.id)
			}
		}
		echo, encodeErr := pr.reflector.EncodeAcceptedProbe(accepted, echoPayload)
		if encodeErr != nil {
			return
		}
		// UDP writes are goroutine-safe, so this receive-goroutine reflection
		// races no in-flight Send on the same socket.
		if _, werr := ps.writeToUDPAddrPort(echo, srcAP); werr == nil {
			// True-wire-volume accounting (D48): the echo we just sent back is
			// real egress traffic on this path — only on a nil write error.
			ps.recordOuterWrite(len(echo))
		}
	default:
		// Unreachable: Decode yields only Probe and Control.
	}
}

// virtualEndpoint returns the single stable endpoint the engine holds for the GIVEN
// peer (ps). Each peer owns its OWN virtual endpoint (invariant A1: one virtual endpoint
// per peer), so the drainer stamps a delivered inner datagram with the endpoint of the
// peer whose resequencer released it — the engine thus attributes return traffic to the
// right peer and Send routes replies back via that peer's virt. On a peer with no
// configured endpoint (the concentrator) its destination is pinned ONCE to the first
// learned source; thereafter every path returns the identical pointer so the engine sees
// one peer, never per-packet churn.
//
// Hot-path note: the destination, once pinned, never changes, and it is published
// through an atomic.Pointer (see udpEndpoint). So the common case takes a
// lock-free fast path — every received datagram would otherwise contend m.mu with
// in-flight Sends. The mutex is acquired only to pin the FIRST learned source.
func (m *Multipath) virtualEndpoint(ps *peerState, learned netip.AddrPort) Endpoint {
	if ps.virt.dstValid() {
		return ps.virt
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !ps.virt.dstValid() {
		ps.virt.setDst(learned)
	}
	return ps.virt
}

// Send hands the batch to the owning peer's adaptive transport, which paces each
// datagram over the lanes it has learned. Datagrams sent before the peer's first
// authenticated hello wait in the transport's queue.
func (m *Multipath) Send(bufs [][]byte, ep Endpoint) error {
	return m.send(bufs, nil, ep)
}

// PacketMetadataEnabled reports that the engine must supply each datagram's flow
// identity: the transport classifies and schedules by it.
func (m *Multipath) PacketMetadataEnabled() bool { return true }

// compile-time proof Multipath satisfies the engine's metadata-carrying send contract.
var _ BindPacketSender = (*Multipath)(nil)

// SendWithMetadata is Send with the engine's per-datagram flow metadata. The transport
// copies each datagram into its own queue and retains none of bufs.
func (m *Multipath) SendWithMetadata(bufs [][]byte, metadata []PacketMetadata, ep Endpoint) error {
	if len(metadata) != len(bufs) {
		return errors.New("bind: flow metadata does not match send batch")
	}
	return m.send(bufs, metadata, ep)
}

func (m *Multipath) send(bufs [][]byte, metadata []PacketMetadata, ep Endpoint) error {
	ue, ok := ep.(*udpEndpoint)
	if !ok {
		return conn.ErrWrongEndpointType
	}
	// Resolve the OWNING peer from the virtual endpoint the engine handed us (each
	// peer holds a DISTINCT virt, registered in peerByVirt). An endpoint not
	// registered here is an unknown peer: refuse it rather than misroute its traffic
	// onto another peer's paths.
	m.mu.Lock()
	peer, ok := m.peerByVirt[ue]
	m.mu.Unlock()
	if !ok {
		return ErrNoHealthyPath
	}
	adaptive := peer.adaptive.Load()
	if adaptive == nil {
		return errClosed
	}
	return adaptive.enqueue(bufs, metadata)
}

func (ps *peerPathState) recordOuterWrite(payloadBytes int) {
	ps.txBytes.Add(uint64(payloadBytes))
}

// ParseEndpoint records a peer's wireguard endpoint as its per-path remote and returns THAT
// peer's virtual endpoint. It may run before Open (the engine applies UAPI config before
// binding), so the parsed address is stashed and also applied to any already-open path lacking
// its own dest_addr.
//
// It resolves the OWNING peer from the configured endpoint (edgePeerByRemote, seeded by
// SeedEdgePeerRemotes for a multi-exit edge): each edge peer holds a DISTINCT virt, and Send
// routes on it (peerByVirt), so a second edge peer's endpoint MUST return that peer's virt — not
// the primary's — or its WG traffic would egress to the wrong concentrator (T251/Q68b). An
// endpoint not in the map (the single-peer edge/hub, or a runtime repoint) resolves to the
// primary and seeds the bind-global default, byte-identical to the pre-T251 behaviour.
func (m *Multipath) ParseEndpoint(s string) (Endpoint, error) {
	ap, err := netip.ParseAddrPort(s)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	peer := m.peerState
	if p, ok := m.edgePeerByRemote[ap]; ok {
		peer = p
	}
	m.defaultRemote, m.hasDefaultRemote = ap, true
	if !peer.virt.dstValid() {
		peer.virt.setDst(ap)
	}
	for _, ps := range peer.paths {
		if _, ok := ps.getRemote(); !ok {
			ps.setRemote(ap)
		}
	}
	return peer.virt, nil
}

// SeedEdgePeerRemotes records each BOUND peer's CONFIGURED concentrator endpoint (the edge role,
// T251/Q68b), index-aligned with the bound peers (primary first, then each AddConcentratorPeer in
// order — the same order as cfg.WireGuard.Peers / cfg.PeerIdentities). A zero/invalid AddrPort
// leaves that peer endpoint-less (tolerant boot — the re-resolution loop installs it later). It
// seeds two durable maps: the per-peer configuredRemote (Open seeds that peer's paths from it) and
// the endpoint→peer map ParseEndpoint resolves the OWNING peer's virt through — so a multi-exit
// edge routes each peer's frames to ITS OWN concentrator, not a single bind-global
// default. It touches NO per-Open path state (only the durable seeds), so it MUST run before
// dev.IpcSet/Open. The concentrator (peers learn remotes from inbound) and the single-peer edge
// (one endpoint, bind-global default) never call it.
func (m *Multipath) SeedEdgePeerRemotes(remotes []netip.AddrPort) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(remotes) != len(m.peers) {
		return fmt.Errorf("bind: SeedEdgePeerRemotes got %d remotes for %d bound peers", len(remotes), len(m.peers))
	}
	for i, ap := range remotes {
		if !ap.IsValid() {
			continue
		}
		p := m.peers[i]
		p.configuredRemote, p.hasConfiguredRemote = ap, true
		m.edgePeerByRemote[ap] = p
	}
	return nil
}

// SetPeerRemote repoints EVERY path's wire remote — and the default remote seeded onto
// paths without their own dest_addr — at ap, the concentrator endpoint the edge now
// sends to. It is the edge-side HUB-FAILOVER switch (T57): when every path's liveness to
// the active concentrator is DOWN (hub loss), the device advances to the next ordered
// peer endpoint (config.Peer.Endpoints) and calls this so every subsequent PROBE
// egresses toward the STANDBY concentrator on every path; the transport forgets its
// lanes and learns the standby's from the first echoed hello.
//
// It repoints UNIFORMLY — overriding a path's own configured dest_addr too — because a
// concentrator switch retargets the peer for the whole bond: the ordered endpoint list
// is a per-CONCENTRATOR list (one address per hub), so on failover every path must reach
// the same standby hub. (A per-path dest_addr addresses the ACTIVE concentrator's
// multi-address fronting; a hub switch supersedes it. All hubs in the ordered set share
// the peer's single WireGuard static key, so the same session identity re-handshakes
// against whichever hub is active.)
//
// It does NOT touch the engine's single virtual endpoint (invariant A1): the engine
// still sees one peer, and only the per-path fan-out BENEATH it is repointed — no
// per-packet endpoint churn reaches the engine. Fresh-session semantics (dropping the
// old hub's keypairs, no hub-to-hub state handoff) and the re-handshake initiation are
// the device's job, driven right after this call.
//
// Concurrency mirrors ParseEndpoint: it takes m.mu to publish the new default and
// repoint each path's remote (each ps.setRemote takes the path's own mutex). It is
// safe on a CLOSED bind (no paths) — the new default remote is still recorded, so the
// next Open seeds fresh paths with it.
//
// The edge fronts a SINGLE concentrator peer (hub failover is an edge-only event — the
// concentrator learns remotes and never switches hubs), so this operates on the primary
// peerState. The per-peer mechanics live in setPeerRemoteLocked, which touches ONLY that
// peer's paths and transport. The standby is a separate process: its hello carries a new
// boot epoch, on which the transport re-baselines this peer's resequencer (defect D32).
func (m *Multipath) SetPeerRemote(ap netip.AddrPort) {
	m.mu.Lock()
	m.setPeerRemoteLocked(m.peerState, ap)
	m.mu.Unlock()
}

// setPeerRemoteLocked repoints EVERY path bound to the given peer at ap — overriding an
// already-learned/configured remote — records ap as the bind's default remote seeded
// onto that peer's future paths, and makes the peer's transport forget its lanes. It
// writes ONLY the given peer's state, so a hub switch on one peer never disturbs another
// bound peer's wire remotes or release point. Caller holds m.mu.
//
// It is the SINGLE-CONTROLLER (primary-peer) path: writing the bind-global m.defaultRemote
// here is correct ONLY because exactly one hub-failover controller exists and it drives the
// primary peer. The MULTI-controller per-peer seam is setPeerRemoteForLocked, which repoints
// one peer WITHOUT touching m.defaultRemote (a per-peer hub switch has no bind-global meaning;
// see that function and the m.defaultRemote field doc for the reader audit).
func (m *Multipath) setPeerRemoteLocked(ps *peerState, ap netip.AddrPort) {
	if adaptive := ps.adaptive.Load(); adaptive != nil {
		adaptive.forgetRoutes()
	}
	m.defaultRemote, m.hasDefaultRemote = ap, true
	for _, pp := range ps.paths {
		pp.setRemote(ap)
	}
}

// SetPeerRemoteFor is the PER-PEER hub-failover repoint seam (T252/G28/M105): it repoints
// exactly the named peer's paths at ap, WITHOUT clobbering the bind-global m.defaultRemote or
// any OTHER peer's wire remotes and transport. It is the multi-exit-edge / N-controller dual
// of SetPeerRemote (which drives the primary and does write m.defaultRemote for single-peer-
// edge back-compat): with N independent hub-failover controllers, peer B's endpoint switch must
// not disturb the remote peer A relies on, so each controller repoints only ITS peer through
// this seam. The existing single-controller SetPeerRemote call sites are unchanged.
//
// It returns an error for an unknown peer name (a wiring defect — fail fast rather than
// silently repoint nothing).
//
// T253 will hand each per-peer controller an adapter that routes its hub switch — and its
// initial endpoint install for an endpoint-less (hostname-only) peer — through this seam.
func (m *Multipath) SetPeerRemoteFor(peerName string, ap netip.AddrPort) error {
	m.mu.Lock()
	p, ok := m.peersByName[peerName]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("bind: SetPeerRemoteFor unknown peer %q", peerName)
	}
	// A cross-peer remote collision leaves ALL state untouched (the guard runs before
	// any mutation) and surfaces the wiring defect.
	err := m.setPeerRemoteForLocked(p, ap)
	m.mu.Unlock()
	return err
}

// setPeerRemoteForLocked is the per-peer core of SetPeerRemoteFor: it repoints ONLY peer p's
// per-path remotes at ap and makes p's transport forget its lanes. Unlike setPeerRemoteLocked
// it does NOT write m.defaultRemote: that field is the SINGLE-PEER-EDGE bind-global fallback
// (its only readers, in attachPeerPathLocked / attachSharedPathLocked, are gated on
// len(m.edgePeerByRemote)==0), so a per-peer repoint has no business mutating it. Caller holds
// m.mu.
//
// It ALSO updates the two DURABLE per-peer seeds so the repoint survives an engine Close/Open
// and resolves correctly on the send-routing seam (D101):
//   - p.configuredRemote — Open/attachPeerPathLocked re-seed each multi-exit peer's fresh paths
//     from it, so without this a repointed peer would re-seed at its ORIGINAL (stale) hub after
//     any Close/Open cycle (configuredRemote was otherwise written only at boot by
//     SeedEdgePeerRemotes).
//   - m.edgePeerByRemote — the endpoint→owning-peer map ParseEndpoint resolves the send-side
//     virt through. The OLD remote's key is removed and the NEW remote keyed to p, so
//     ParseEndpoint(newAP) returns p's virt and ParseEndpoint(oldAP) no longer misresolves to p.
//
// Seeding these unconditionally ALSO INSTALLS a remote for a previously-unseeded peer (one that
// booted endpoint-less because its hostname had no address yet — D100): before this call such a
// peer had no configuredRemote and no edgePeerByRemote key, so ParseEndpoint(ap) would fall back
// to the primary's virt and its WG traffic would egress to the wrong concentrator; after it, the
// peer owns ap. (T253 routes each controller's initial install through this same seam.)
//
// It FAILS FAST — returning an error and mutating NO state — when ap is already owned by a
// DIFFERENT peer (edgePeerByRemote is keyed by remote and cannot represent two peers at one
// addr:port). Config load rejects duplicate LITERAL endpoints across peers, but a hostname-only
// peer carries no literal to compare, so once T253 drives two hostname peers that resolve to the
// same addr:port through this seam, keying ap to p unconditionally would (a) steal peer q's
// send-routing key now and (b) leave p's remote UNMAPPED when a later repoint of q away from ap
// deletes what is by then p's key — ParseEndpoint would then misresolve p to the primary's virt,
// the same silent cross-wiring class as D100. Keying ap to p when it ALREADY maps to p is fine:
// an idempotent self-repoint (or a repoint to p's own current remote) is a valid no-op path.
func (m *Multipath) setPeerRemoteForLocked(p *peerState, ap netip.AddrPort) error {
	if owner, ok := m.edgePeerByRemote[ap]; ok && owner != p {
		return fmt.Errorf("bind: SetPeerRemoteFor: remote %s is already owned by peer %q; refusing to repoint peer %q onto it (edgePeerByRemote cannot map two peers to one addr:port)", ap, owner.name, p.name)
	}
	if p.hasConfiguredRemote && p.configuredRemote != ap {
		delete(m.edgePeerByRemote, p.configuredRemote)
	}
	p.configuredRemote, p.hasConfiguredRemote = ap, true
	m.edgePeerByRemote[ap] = p
	if adaptive := p.adaptive.Load(); adaptive != nil {
		adaptive.forgetRoutes()
	}
	for _, pp := range p.paths {
		pp.setRemote(ap)
	}
	return nil
}

// Close tears down every per-path socket and CLEARS the bind's path state so a
// subsequent Open fully rebuilds it. The amneziawg engine drives exactly this
// lifecycle — device.upLocked → BindUpdate → closeBindLocked calls Close() before
// every Open(), and a Down/Up cycles Close after Open — so Close must leave the
// bind reopenable (matching conn.StdNetBind, whose closed state is simply "no
// sockets"). Outstanding receive calls return an error as their socket closes.
func (m *Multipath) Close() error {
	m.transitionMu.Lock()
	defer m.transitionMu.Unlock()

	m.mu.Lock()
	// Release the fan-in first so any reader parked on a delivery poke, and the
	// engine-facing drainer, unblocks. Detach every generation while m.mu still
	// serializes Send; its blocking writer and reader barriers run after unlock.
	if m.recvClosed != nil {
		close(m.recvClosed)
	}
	retirement := m.detachSocketGenerationsLocked()
	m.mu.Unlock()

	err := retirement.retire()
	m.readersWG.Wait()
	m.clearPerOpenStateAfterReaders()
	return err
}

func (m *Multipath) clearPerOpenStateAfterReaders() {
	// No generation reader or writer remains, and transitionMu prevents a new
	// Open until the old per-Open resequencers and channels are cleared. Snapshot the
	// durable peer registry under m.mu: AddConcentratorPeer may legally register
	// a fresh (already-empty) peer once path detachment makes the bind closed.
	m.mu.Lock()
	peers := append([]*peerState(nil), m.peers...)
	m.deliverSignal = nil
	m.recvClosed = nil
	m.mu.Unlock()
	for _, p := range peers {
		p.lifecycleMu.Lock()
		rq := p.resequencer.Swap(nil)
		p.lifecycleMu.Unlock()
		if rq != nil {
			rq.Close()
		}
	}
}

type socketGenerationRetirement struct {
	peerPaths []*peerPathState
	shared    []*sharedPathState
}

// preparePeerPathLocked stops generated PMTU work without waiting. Caller holds
// m.mu, so detachment and admission closure form one atomic transport-generation
// transition as observed by Send.
func (r *socketGenerationRetirement) preparePeerPathLocked(pp *peerPathState) {
	pp.closeGeneratedProbes()
	r.peerPaths = append(r.peerPaths, pp)
}

// prepareSharedLocked stops UDP-write admission on the shared socket and makes
// each peer's transport forget its lanes on it. Retirement then closes the socket
// to interrupt in-flight I/O before joining the in-flight writes.
func (r *socketGenerationRetirement) prepareSharedLocked(sp *sharedPathState) {
	sp.stopWrites()
	for _, view := range r.peerPaths {
		if view.sharedPathState != sp {
			continue
		}
		if adaptive := view.peer.adaptive.Load(); adaptive != nil {
			adaptive.forgetSocket(sp)
		}
	}
	r.shared = append(r.shared, sp)
}

// retire supplies the blocking half of generation teardown. It must run
// without m.mu.
func (r socketGenerationRetirement) retire() error {
	var firstErr error
	// Interrupt readers and any writer blocked in the kernel before joining
	// the writes. Admission was stopped while the generation was
	// detached, so no new writer can enter after this close.
	for _, sp := range r.shared {
		if err := sp.closeSocket(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	for _, sp := range r.shared {
		sp.waitWrites()
	}
	return firstErr
}

// detachSocketGenerationsLocked removes all live transport generations from
// admission and returns their blocking retirement work. Caller holds m.mu.
func (m *Multipath) detachSocketGenerationsLocked() socketGenerationRetirement {
	var retirement socketGenerationRetirement
	for _, p := range m.peers {
		for _, pp := range p.paths {
			retirement.preparePeerPathLocked(pp)
		}
	}
	for _, sp := range m.shared {
		retirement.prepareSharedLocked(sp)
	}
	m.shared = nil
	m.deferred = nil
	for _, p := range m.peers {
		p.paths = nil
	}
	return retirement
}

// AddPath admits a new path to the running bond at runtime (T30): it binds the
// path's source-addr'd socket, mints its prober (via the injected factory) joining
// the current probe session, seeds its remote from the config dest_addr or the
// learned default, and spawns its Bind-owned reader. The path slice mutates
// under m.mu; transitionMu keeps a concurrent transport generation change
// from overtaking any out-of-lock rollback retirement. The new path starts DOWN (its prober has no
// echoes yet); the transport learns a lane on it from its first echoed hello, so admission
// disturbs neither the lanes of the surviving paths nor the WG session:
// the single virtual endpoint is untouched, and the engine's receive set does not
// change (the reader is the Bind's, not the engine's).
//
// Path-ids are handed out monotonically from a high-water counter that persists
// ACROSS Open spans (a Close->Open cycle does NOT reset it -- only a process restart
// does), so a surviving path is never renumbered and a reopened bond never re-mints
// an id the peer still associates with old per-path reflector state; the uint8 id
// space (256 ids) is therefore consumed CUMULATIVELY over the process lifetime by
// the initial-Open admissions (the N configured paths take ids 0..N-1) PLUS every
// runtime AddPath admission combined, and is exhausted once 256 distinct ids have
// been minted over the daemon's life, which fails fast rather than reusing an id and
// colliding with the peer's per-path reflector state.
func (m *Multipath) AddPath(def config.Path) (retErr error) {
	m.transitionMu.Lock()
	defer m.transitionMu.Unlock()

	var retirement socketGenerationRetirement
	m.mu.Lock()
	defer func() {
		m.mu.Unlock()
		if err := retirement.retire(); retErr == nil {
			retErr = err
		}
	}()
	if len(m.paths) == 0 {
		return errClosed // only a running bind can take a runtime path
	}
	if m.newProber == nil {
		return errors.New("bind: cannot add a path at runtime without the probe transport")
	}
	if !def.SourceAddr.IsValid() {
		return fmt.Errorf("bind: add path %q: source_addr is required", def.Name)
	}
	// Reject a duplicate against the DURABLE membership (m.defs), not just the live
	// paths: a path that is present-but-DEFERRED is in m.defs but not m.paths, and
	// re-adding it must be refused rather than minting a second entry for the same name.
	for i := range m.defs {
		if m.defs[i].Name == def.Name {
			return fmt.Errorf("bind: add path %q: a path with that name is already configured", def.Name)
		}
	}
	if m.nextPathID > 255 {
		return fmt.Errorf("bind: add path %q: path-id space exhausted (256 cumulative admissions over the process lifetime; an interface Down/Up does not reset it -- restart the daemon)", def.Name)
	}
	id := uint8(m.nextPathID)

	// Honor a forced BindModeDevice the same way Open does (I5); BindModeSource always
	// source-IP-pins. BindModeAuto now (D30) gets Open's roam-surviving device-bind decision
	// via autoRuntimeDeviceBind — the runtime-added path device-binds when its source resolves
	// to a single-family interface no other configured path contends for, rather than the
	// pre-D30 unconditional source-IP-pin.
	dev := m.resolveDeviceBind(def.SourceAddr, def.Bind)
	if dev == "" {
		dev = m.autoRuntimeDeviceBind(def.SourceAddr, def.Bind)
	}
	c, deviceErr, err := m.addPathListen(def.SourceAddr, m.openPort, dev)
	if err != nil {
		if errors.Is(err, syscall.EADDRNOTAVAIL) {
			// Symmetric with Open's tolerant bind: a well-formed-but-not-yet-assignable
			// source_addr is DEFERRED, not fatal. Record it in the durable membership and
			// the deferred set (Down, without a socket) and return success, so a
			// reload that introduces such a path does not fail the entire reload. The prober is minted
			// here so the reserved id-stamp is consumed even while deferred; a later bind
			// (T55 / a Close→Open) reuses the SAME stamp. No socket materialized (the
			// source-IP-pin fallback failed too), so warnForcedDeviceStillDeferred — not
			// warnForcedDeviceUnresolvable — is the accurate, non-fallback-claiming WARN
			// here (D53 round 2 / FIX 2); it seeds the fresh deferredPath's dedup latch so
			// reconcileDeferred's first retry tick does not immediately re-WARN the SAME
			// condition (FIX 1).
			//
			// FAN the deferred admission out to EVERY bound peer, mirroring the bound-add
			// fan-out (attachSharedPathLocked): a still-deferred def is appended to the SHARED
			// membership m.defs, and Open rebuilds EVERY peer's per-(peer,path) view by indexing
			// p.probers[i] with i the def's index in m.defs. If only the primary's probers grew
			// here (the pre-fix behaviour), a concentrator peer's p.probers would fall SHORT of
			// m.defs, and the first Close->Open cycle that SUCCESSFULLY binds this deferred def
			// would panic with index-out-of-range at Open's `pp.prober = p.probers[i]`. So mint
			// each peer its OWN Down prober (keyed on that peer's psk, stamped with the shared
			// id), keeping every peer's prober slice index-aligned with m.defs. Each per-peer
			// prober stays Down until a later
			// bind admits it; the PRIMARY's is the durable deferredPath record (Open re-defers it
			// as m.probers[i]). Guard each peer's factory up front so a missing one fails the add
			// fast rather than nil-dereferencing mid fan-out.
			for _, p := range m.peers {
				if p.newProber == nil {
					return fmt.Errorf("bind: add path %q: peer %q cannot defer a path without the probe transport", def.Name, p.name)
				}
			}
			prober := m.newProber(def.Name, id, def.RideThrough) // the primary's; also the durable deferred record
			m.defs = append(m.defs, def)
			for pi, p := range m.peers {
				if pi == 0 {
					p.probers = append(p.probers, prober)
					continue
				}
				p.probers = append(p.probers, p.newProber(def.Name, id, def.RideThrough))
			}
			warned := m.warnForcedDeviceStillDeferred(def.Name, def.Bind, dev, false)
			m.deferred = append(m.deferred, deferredPath{def: def, prober: prober, warnedUnresolvable: warned})
			m.nextPathID++
			return nil
		}
		return fmt.Errorf("bind: add path %q on %s: %w", def.Name, def.SourceAddr, err)
	}
	// The listen succeeded: a working conn materialized. The D53 fallback-fact WARNs are
	// deferred past the peer fan-out below (round 3 / CRITICISM 1): attachSharedPathLocked
	// can still fail (a per-peer codec/prober build error) and roll the whole admission
	// back — closing this socket and returning an error to the caller, with the path
	// never added — so warning here, before the fan-out is known to succeed, would log an
	// outcome-false "falling back to source-IP pinning" claim for a path that never came up.
	_ = c.SetReadBuffer(socketRecvBuffer)
	shared := &sharedPathState{
		name:        def.Name,
		id:          id,
		src:         def.SourceAddr,
		conn:        c,
		bindMode:    def.Bind,
		boundDevice: dev,
	}

	// FAN-OUT (single owner): instantiate the per-(peer,path) state for EVERY currently-
	// bound peer, minting each peer's own Codec + prober. attached[k] is m.peers[k]'s view
	// of the new shared socket. A failure in any peer rolls back every peer already
	// attached, so a partial fan-out never leaks.
	attached, err := m.attachSharedPathLocked(shared, def, id, nil, &retirement)
	if err != nil {
		retirement.prepareSharedLocked(shared)
		return err
	}
	// The fan-out succeeded: every currently-bound peer now has a view
	// of this socket, so the fallback facts these log are backed by a real, live,
	// installed path, not a claim (D53 round 2 / FIX 2; ordering — round 3 / CRITICISM 1).
	m.warnForcedDeviceUnresolvable(def.Name, def.Bind, def.SourceAddr, dev)
	m.warnDeviceBindFallback(def.Name, def.Bind, dev, deviceErr)

	// Durable membership: the def is SHARED (one socket), recorded once; each peer records
	// its OWN prober so a subsequent Close→Open rebuilds THIS path. Kept index-aligned
	// with m.defs under m.mu, so the runtime add survives a reopen instead of vanishing.
	m.shared = append(m.shared, shared)
	m.defs = append(m.defs, def)
	for k, p := range m.peers {
		p.probers = append(p.probers, attached[k].prober)
	}
	m.nextPathID++

	// One reader per SHARED socket. The reader feeds demuxInbound, which source-demuxes the
	// socket's datagrams to their owning peer once more than one peer holds a view of it
	// (T88/T93 concentrator demux); a single-peer bind delivers straight through to that peer.
	// (D66: the prior "single-peer receive; N-peer demux is a later G4 task" note was stale —
	// the shared-socket demux shipped with T88/T93.)
	m.readersWG.Add(1)
	go m.readLoop(attached[0], m.deliverSignal)
	return nil
}

// autoRuntimeDeviceBind decides an AUTO-mode runtime-added or promoted path's device-bind (D30),
// applying Open's selectDeviceBinds heuristic over the current durable membership so an auto path
// device-binds only when its source resolves to a single-family interface that NO OTHER
// configured path contends for — the same roam-surviving decision Open makes, closing the pre-D30
// gap where a runtime-added/promoted auto path always source-IP-pinned (AddPath / reconcileDeferred
// went through resolveForcedDeviceBind, which returns "" for auto). It returns "" (source-IP-bind)
// for any non-auto mode (forced-device is decided by resolveDeviceBind; source never device-binds)
// or when the heuristic declines. Caller holds m.mu (reads m.defs).
func (m *Multipath) autoRuntimeDeviceBind(targetSrc netip.Addr, targetMode config.BindMode) string {
	if targetMode != config.BindModeAuto {
		return ""
	}
	// Contention set: the target at index 0, then every OTHER configured path by DISTINCT source
	// address — skipping entries sharing targetSrc avoids double-counting the target when it is
	// already in m.defs (the promotion path, where the deferred def is present). selectDeviceBinds'
	// device-uniqueness check then device-binds the target only when no other member resolves to
	// its interface.
	srcs := []netip.Addr{targetSrc}
	modes := []config.BindMode{targetMode}
	for i := range m.defs {
		if m.defs[i].SourceAddr == targetSrc {
			continue
		}
		srcs = append(srcs, m.defs[i].SourceAddr)
		modes = append(modes, m.defs[i].Bind)
	}
	return selectDeviceBinds(srcs, modes, m.resolveIface)[0]
}

// attachSharedPathLocked is the SINGLE OWNER of the runtime shared-path fan-out: for a
// freshly-bound shared socket it instantiates the per-(peer,path) state — codec, learned/
// configured remote, prober, and (implicitly) the tx/rx counters — for EVERY currently-bound
// peer. It returns the created views in peer order
// (attached[k] belongs to m.peers[k]). On any peer's failure it rolls back every peer already
// attached (popping the appended peerPathState), so a
// partial fan-out never leaks a half-admitted path. Caller holds m.mu and, on success, owns
// appending the shared socket + each peer's prober to the durable membership.
//
// probers, when non-nil, MUST hold exactly one entry per m.peers (peer order) — that peer's
// OWN, ALREADY m.defs-aligned prober to REUSE rather than mint fresh (the deferred-promote
// fan-out, promoteDeferredLocked: a promoted deferred path's probers already exist in every
// peer's p.probers from the original admission, so promotion must not mint a second, different
// prober per peer). nil mints a FRESH prober per peer via that peer's newProber factory (the
// runtime AddPath fan-out for a brand-new path).
func (m *Multipath) attachSharedPathLocked(
	shared *sharedPathState,
	def config.Path,
	id uint8,
	probers []*telemetry.Prober,
	retirement *socketGenerationRetirement,
) ([]*peerPathState, error) {
	attached := make([]*peerPathState, 0, len(m.peers))
	for pi, p := range m.peers {
		var prober *telemetry.Prober
		if probers != nil {
			prober = probers[pi]
		}
		pp, err := m.attachPeerPathLocked(p, shared, def, id, prober)
		if err != nil {
			for k := len(attached) - 1; k >= 0; k-- {
				if detached := m.detachPeerPathBoundLocked(m.peers[k], shared.name); detached != nil {
					retirement.preparePeerPathLocked(detached)
				}
			}
			return nil, err
		}
		attached = append(attached, pp)
	}
	return attached, nil
}

func (m *Multipath) attachPeerPathLocked(
	p *peerState,
	shared *sharedPathState,
	def config.Path,
	id uint8,
	prober *telemetry.Prober,
) (*peerPathState, error) {
	if prober == nil {
		if p.newProber == nil {
			return nil, errors.New("bind: cannot add a path at runtime without the probe transport")
		}
		prober = p.newProber(def.Name, id, def.RideThrough)
	}
	// This path binds to peer p; its receive codec is p's codec (derived from p's psk).
	codec, err := p.newCodec()
	if err != nil {
		return nil, err
	}
	pp := &peerPathState{sharedPathState: shared, peer: p, codec: codec, prober: prober}
	switch {
	case def.DestAddr.IsValid():
		// A path-specific dest_addr (multi-address fronting of the peer's active concentrator)
		// wins over the peer/bind default.
		pp.setRemote(def.DestAddr)
	case p.hasConfiguredRemote:
		// This peer's OWN configured concentrator endpoint (multi-exit edge, T251) — so each
		// edge peer's paths reach ITS concentrator, not a single bind-global default.
		pp.setRemote(p.configuredRemote)
	case len(m.edgePeerByRemote) == 0 && m.hasDefaultRemote:
		// Single-peer edge/hub: the bind-global default (ParseEndpoint's sole endpoint). It is
		// deliberately NOT used in multi-exit edge mode (edgePeerByRemote non-empty): there a
		// peer without its own configuredRemote booted endpoint-less (tolerant boot) and must
		// stay remoteless until its endpoint is installed, rather than inherit another peer's hub.
		pp.setRemote(m.defaultRemote)
	}
	pp.pmtuProbe = m.buildPMTUProbe(pp)
	p.paths = append(p.paths, pp)
	// Publish this peer's view of the shared socket for the receive demux (T88): once >1 peer
	// has a view, handleInbound source-demuxes the socket's datagrams to their owning peer.
	shared.addViewLocked(pp)
	return pp, nil
}

// detachPeerPathBoundLocked drops one peer's BOUND view of a shared path (matched by name)
// from that peer's paths slice and returns it. It does NOT touch the durable membership
// (m.defs / p.probers) — the caller (RemovePath, or the fan-out rollback) owns that. It
// returns nil when the peer holds no bound view of the path. Caller holds m.mu.
func (m *Multipath) detachPeerPathBoundLocked(p *peerState, name string) *peerPathState {
	for i, pp := range p.paths {
		if pp.name == name {
			p.paths = append(p.paths[:i], p.paths[i+1:]...)
			return pp
		}
	}
	return nil
}

// RemovePath closes the named path at runtime (T30). It unlinks the path from the
// path slice, makes each peer's transport forget its lanes on it (so no further
// datagram is scheduled onto it), and closes its socket — which retires its
// Bind-owned reader. Detachment is atomic under m.mu, then blocking writer/socket
// retirement runs under transitionMu alone, so the structures stay coherent for Send
// without holding the bind lock across a wait. In-flight state is preserved: frames
// the path already pushed into the resequencer stay queued and are delivered in
// order (resequencing is per peer, NOT per path, so a removal never resets it), the
// surviving paths are untouched, and the single virtual endpoint / WG session is
// undisturbed. The last remaining LIVE path cannot be removed (that would tear down
// the virtual endpoint the engine holds).
//
// A DEFERRED path (present in the durable membership but not yet bound, because its
// source_addr was not assignable at Open) has no socket or reader:
// removing it merely drops it from the durable membership + deferred set, so a reload
// that DROPS a still-deferred path retires it cleanly.
//
// Since a tolerant Open leaves m.defs/m.probers (durable, full length) LONGER than
// m.paths (bound only), the durable-membership splice is keyed by IDENTITY (name),
// NOT by the m.paths index — indexing m.defs by the m.paths position would splice the
// wrong entry once a deferred path precedes the removed one.
func (m *Multipath) RemovePath(name string) (retErr error) {
	m.transitionMu.Lock()
	defer m.transitionMu.Unlock()

	var retirement socketGenerationRetirement
	m.mu.Lock()
	defer func() {
		m.mu.Unlock()
		if err := retirement.retire(); retErr == nil {
			retErr = err
		}
	}()
	if len(m.paths) == 0 {
		return errClosed
	}
	// Locate the path in the DURABLE membership by identity. m.defs (and each peer's
	// probers) are full-length (bound + deferred); m.shared is the bound subset and may be
	// shorter.
	defIdx := -1
	for i := range m.defs {
		if m.defs[i].Name == name {
			defIdx = i
			break
		}
	}
	if defIdx < 0 {
		return fmt.Errorf("bind: remove path %q: no such configured path", name)
	}
	// Is it a LIVE (bound) shared socket? If so it owns a socket and per-peer views
	// to retire.
	sharedIdx := -1
	for i, sp := range m.shared {
		if sp.name == name {
			sharedIdx = i
			break
		}
	}
	if sharedIdx < 0 {
		// A DEFERRED path: no transport to tear down — just drop it from the durable
		// membership and the deferred set so it does not resurrect on the next Open.
		return m.removeDurableLocked(defIdx, name)
	}
	// Removing a bound path: refuse if it is the LAST live socket (that tears down the
	// virtual endpoint the engine holds). A deferred path carries no transport, so it
	// does not count toward "at least one live path must remain".
	if len(m.shared) == 1 {
		return fmt.Errorf("bind: refusing to remove path %q: at least one live path must remain", name)
	}
	sp := m.shared[sharedIdx]
	// FAN-OUT (single owner): drop this shared path's per-(peer,path) view from EVERY bound
	// peer, so no peer schedules onto the closing socket. Each peer's remaining paths are
	// untouched (the splice is by identity).
	for _, p := range m.peers {
		if detached := m.detachPeerPathBoundLocked(p, name); detached != nil {
			retirement.preparePeerPathLocked(detached)
		}
	}
	m.shared = append(m.shared[:sharedIdx], m.shared[sharedIdx+1:]...)
	retirement.prepareSharedLocked(sp)
	return m.removeDurableLocked(defIdx, name)
}

// removeDurableLocked drops the path named name from the durable membership: m.defs and
// EVERY peer's probers at defIdx (each peer's probers is kept index-aligned with m.defs),
// and the deferred set by name (a no-op when the path was bound rather than deferred).
// Caller holds m.mu. Keying the durable splice on defIdx (a name lookup) rather than a
// bound-path index is what keeps m.defs / each peer's probers correct once a tolerant Open
// has made them longer than the bound path list — so a subsequent Close→Open rebuilds
// exactly the surviving membership and neither resurrects the removed path nor loses a
// deferred one.
//
// Fail-fast alignment guard (D42): every runtime admission/promotion fan-out is supposed to
// keep each peer's p.probers EXACTLY length-aligned with m.defs, but a peer whose prober set
// has fallen out of alignment (a wiring defect in some other fan-out site) would otherwise
// either panic with an index-out-of-range slice operation (when the divergence leaves defIdx
// past the slice end) or, worse, silently splice the WRONG entry (when the divergence is
// in-range — e.g. probers longer than m.defs, or short at the TAIL with defIdx still valid —
// so the length check below is required in ADDITION to any index bound: an index check alone
// would miss every in-range divergence and let it through to corrupt the splice). Detect ANY
// length divergence BEFORE mutating anything and return a wiring-defect error instead, leaving
// m.defs/every peer's probers untouched so the caller can decide how to proceed rather than
// crashing the daemon or corrupting the membership.
func (m *Multipath) removeDurableLocked(defIdx int, name string) error {
	for _, p := range m.peers {
		if len(p.probers) != len(m.defs) {
			return fmt.Errorf("bind: remove path %q: peer %q prober set (len %d) is misaligned with the durable membership (len %d) — per-peer prober fan-out desync (wiring defect)", name, p.name, len(p.probers), len(m.defs))
		}
	}
	m.defs = append(m.defs[:defIdx], m.defs[defIdx+1:]...)
	for _, p := range m.peers {
		p.probers = append(p.probers[:defIdx], p.probers[defIdx+1:]...)
	}
	for i := range m.deferred {
		if m.deferred[i].def.Name == name {
			m.deferred = append(m.deferred[:i], m.deferred[i+1:]...)
			break
		}
	}
	return nil
}

// PathNames returns the names of the DURABLE configured membership — every path the
// bond is configured for, in configured order, INCLUDING a path that is currently
// DEFERRED because its source_addr is not yet assignable (it is in m.defs but not
// m.paths). The device's config reload diffs the desired path set against this to
// decide what to add or remove; returning the deferred paths here is what keeps a
// no-op reload a no-op — a still-configured deferred path is NOT seen as a new add
// (which AddPath would then reject on the same EADDRNOTAVAIL bind), so the
// SIGHUP-no-op invariant holds across the whole deferred window.
func (m *Multipath) PathNames() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	names := make([]string, len(m.defs))
	for i, def := range m.defs {
		names[i] = def.Name
	}
	return names
}

// BoundPeerNames returns the id/name of every bound peer in peer order — peers[0] is the
// embedded primary, which NewMultipath names "" and which stays "" for the single-peer
// edge/hub; a multi-peer concentrator's wiring (device.Up) calls SetPrimaryPeerName so the
// primary ALSO carries its configured name (D58) — followed by each peer registered via
// AddConcentratorPeer, in registration order, under its own configured name. It is an
// observability accessor over the bound peer set the concentrator wiring builds; it takes
// m.mu so it never races peer registration.
func (m *Multipath) BoundPeerNames() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	names := make([]string, len(m.peers))
	for i, p := range m.peers {
		names[i] = p.name
	}
	return names
}

// PeerVirtEndpoints returns each bound peer's STABLE virtual endpoint (invariant A1: one per
// peer), in peer order. Each is a DISTINCT pointer pinned for the peer's whole life, so the
// engine attributes return traffic to the right peer and Send routes replies back through it.
// Observability accessor; it takes m.mu so it never races peer registration.
func (m *Multipath) PeerVirtEndpoints() []Endpoint {
	m.mu.Lock()
	defer m.mu.Unlock()
	eps := make([]Endpoint, len(m.peers))
	for i, p := range m.peers {
		eps[i] = p.virt
	}
	return eps
}

// PeerBootProbe emits a freshly-encoded boot PROBE from bound peer peerIdx's boot prober for
// path pathIdx (both in bound-peer / durable-membership order). The returned bytes are MAC'd
// under THAT peer's prober psk, so a test can assert each concentrator peer's prober is keyed on
// its own configured psk — the bytes decode as a PROBE under it and under NO other peer's psk.
// Wiring-verification accessor over the per-peer prober set the concentrator wiring builds; it
// takes m.mu so it never races peer registration. It errors if the indices are out of range.
func (m *Multipath) PeerBootProbe(peerIdx, pathIdx int) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if peerIdx < 0 || peerIdx >= len(m.peers) {
		return nil, fmt.Errorf("bind: peer index %d out of range [0,%d)", peerIdx, len(m.peers))
	}
	p := m.peers[peerIdx]
	if pathIdx < 0 || pathIdx >= len(p.probers) {
		return nil, fmt.Errorf("bind: peer %q path index %d out of range [0,%d)", p.name, pathIdx, len(p.probers))
	}
	if p.probers[pathIdx] == nil {
		return nil, fmt.Errorf("bind: peer %q has no prober for path %d", p.name, pathIdx)
	}
	raw, _, err := p.probers[pathIdx].SendProbePayload(nil)
	return raw, err
}

// PeerReflect runs raw through bound peer peerIdx's probe Reflector (keyed on that peer's psk)
// and returns the authenticated echo, or an error if the peer's reflector does not authenticate
// raw. It lets a test assert each concentrator peer's reflector (and thus its codec derivation)
// is keyed on its own configured psk: a probe minted under the peer's psk reflects, one minted
// under another peer's psk does not. Wiring-verification accessor; it takes m.mu.
func (m *Multipath) PeerReflect(peerIdx int, raw []byte) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if peerIdx < 0 || peerIdx >= len(m.peers) {
		return nil, fmt.Errorf("bind: peer index %d out of range [0,%d)", peerIdx, len(m.peers))
	}
	echo, _, err := m.peers[peerIdx].reflector.Reflect(raw)
	return echo, err
}

// PathTraffic is a consistent per-path traffic+telemetry snapshot (T23): the OUTER-
// wire byte counters fused with the path's telemetry quality Estimate and liveness
// State (read verbatim from its Prober). It is the shape the metrics.Source adapter
// maps into a metrics.PathSnapshot; the Bind reports raw cumulative byte counters and
// the Estimate/State — it does NOT compute a rate here (the adapter derives throughput
// from the byte-counter delta across scrapes).
type PathTraffic struct {
	Name string
	// ID is the path's stable wire id; the high byte of each of its lanes.
	ID       uint8
	TxBytes  uint64
	RxBytes  uint64
	Estimate telemetry.Estimate
	State    telemetry.PathState
	// SocketWriteErrors is the cumulative count of the transport's datagram writes
	// this path's socket refused.
	SocketWriteErrors uint64
	// ProbeSendErrors is the cumulative count of unexpected locally-originated
	// ordinary/PMTU PROBE socket write failures for this path. Expected PMTU
	// EMSGSIZE verdicts are excluded. Read verbatim from peerPathState.probeSendErrors.
	ProbeSendErrors uint64
	// The following addressing fields surface this path's runtime networking
	// identity for the G21 monitoring UI (value-wiring into the monitor snapshot
	// is T220; the /metrics prometheus exposition ignores them). Source is the
	// bound local source address; LocalAddr the authoritative bound addr:port from
	// the socket; Remote the current wire remote the path points at (on the
	// concentrator, the edge's last observed source via roaming — Q64). BindMode is
	// the configured/effective bind mode; BoundDevice the resolved SO_BINDTODEVICE
	// interface ("" when source-IP-pinned). Zero-valued while a path is unbound/
	// remoteless.
	Source      netip.Addr
	LocalAddr   netip.AddrPort
	Remote      netip.AddrPort
	BindMode    config.BindMode
	BoundDevice string
}

// PeerSnapshot is a consistent per-BOUND-PEER snapshot of path traffic+telemetry, the
// transport's state, and resequencer counters (T94): the read side the per-peer /metrics
// exposition scrapes. It reports EVERY bound peer (not just the primary), so a
// multi-peer concentrator's metrics can attribute each series to the edge it came
// from. Name is BoundPeerNames()[i]: "" for the primary on the single-peer edge/hub only;
// the peer's configured name otherwise, including the concentrator's first-configured peer
// once SetPrimaryPeerName has run (D58). Adaptive is nil while the bind is closed.
type PeerSnapshot struct {
	Adaptive *bond.Snapshot
	Name     string
	Paths    []PathTraffic
	Reseq    reseq.Stats
}

// PeerSnapshots returns a consistent per-peer snapshot, in bound-peer order (matching
// BoundPeerNames), for every bound peer's path traffic+telemetry, transport state, and
// resequencer counters. Concurrency: grab each peer's name, per-path counters/prober
// pointers, and transport/resequencer pointers under m.mu in one bounded O(peers+paths) copy,
// then RELEASE m.mu before calling the independently-synchronized prober
// Estimate()/State(), transport Snapshot(), and resequencer Stats() — so the scrape never
// blocks an in-flight Send behind m.mu. len(result) >= 1: NewMultipath always binds at
// least the primary peer, so this is never empty — though the per-peer Paths slice can
// still be empty for a peer with a currently-empty path set.
func (m *Multipath) PeerSnapshots() []PeerSnapshot {
	type pathRef struct {
		tx, rx       uint64
		probeErrs    uint64
		socketErrors uint64
		pp           *peerPathState
	}
	type peerRef struct {
		adaptive *adaptivePeer
		name     string
		paths    []pathRef
		rq       *reseq.Resequencer
	}

	m.mu.Lock()
	refs := make([]peerRef, len(m.peers))
	for i, p := range m.peers {
		r := peerRef{name: p.name, rq: p.resequencer.Load(), adaptive: p.adaptive.Load()}
		r.paths = make([]pathRef, len(p.paths))
		for j, pp := range p.paths {
			r.paths[j] = pathRef{
				tx:           pp.txBytes.Load(),
				rx:           pp.rxBytes.Load(),
				probeErrs:    pp.probeSendErrors.Load(),
				socketErrors: pp.socketWriteErrors.Load(),
				pp:           pp,
			}
		}
		refs[i] = r
	}
	m.mu.Unlock()

	out := make([]PeerSnapshot, len(refs))
	for i, r := range refs {
		snap := PeerSnapshot{Name: r.name}
		if r.adaptive != nil {
			r.adaptive.mu.Lock()
			state := r.adaptive.transport.Snapshot(time.Now())
			r.adaptive.mu.Unlock()
			snap.Adaptive = &state
		}
		snap.Paths = make([]PathTraffic, len(r.paths))
		for j, pr := range r.paths {
			// name, src, bindMode, boundDevice and prober are immutable for the
			// path's life; conn.LocalAddr() and getRemote() are internally
			// synchronized. All are read here, after m.mu is released.
			pt := PathTraffic{
				Name:              pr.pp.name,
				ID:                pr.pp.id,
				TxBytes:           pr.tx,
				RxBytes:           pr.rx,
				ProbeSendErrors:   pr.probeErrs,
				SocketWriteErrors: pr.socketErrors,
				Estimate:          pr.pp.prober.Estimate(),
				State:             pr.pp.prober.State(),
				Source:            pr.pp.src,
				BindMode:          pr.pp.bindMode,
				BoundDevice:       pr.pp.boundDevice,
			}
			if pr.pp.conn != nil {
				if ua, ok := pr.pp.conn.LocalAddr().(*net.UDPAddr); ok {
					pt.LocalAddr = ua.AddrPort()
				}
			}
			if rem, ok := pr.pp.getRemote(); ok {
				pt.Remote = rem
			}
			snap.Paths[j] = pt
		}
		if r.rq != nil {
			snap.Reseq = r.rq.Stats()
		}
		out[i] = snap
	}
	return out
}

// SetMark is a no-op for T12: the engine only calls SetMark when a fwmark is
// configured, which wanbond does not set.
func (m *Multipath) SetMark(uint32) error { return nil }

// BatchSize is the max number of datagrams passed to a ReceiveFunc / Send.
func (m *Multipath) BatchSize() int { return multipathBatchSize }
