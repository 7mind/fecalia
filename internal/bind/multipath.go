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

	"github.com/amnezia-vpn/amneziawg-go/conn"

	"github.com/7mind/wanbond/internal/adaptivefec"
	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/fec"
	"github.com/7mind/wanbond/internal/frame"
	"github.com/7mind/wanbond/internal/log"
	"github.com/7mind/wanbond/internal/reseq"
	"github.com/7mind/wanbond/internal/sched"
	"github.com/7mind/wanbond/internal/telemetry"
)

// socketRecvBuffer is the SO_RCVBUF requested on every per-path socket, matching
// wireguard-go's StdNetBind (~7 MiB). A large receive buffer absorbs the bursts a
// bonded, reordered flow delivers while the userspace engine catches up; the
// pass-through P0 Bind used the OS default and lost datagrams under load (P0
// findings §1/§2). The kernel silently caps the request at net.core.rmem_max, so
// the effective size may be smaller on an untuned host — best-effort by design.
const socketRecvBuffer = 7 << 20

// multipathBatchSize is the number of datagrams a ReceiveFunc / Send handles per
// call. T12 keeps it at 1 (one syscall per datagram, per path); GSO/GRO batching
// is best-effort future work (P0 findings §2) tracked separately and does not
// change the wire format.
const multipathBatchSize = 1

// maxDatagram bounds a single received outer datagram. It comfortably exceeds a
// full-MTU inner packet plus every outer/WG/amnezia-junk overhead.
const maxDatagram = 65535

// Receive-resequencer tuning (T18), sized against P0 findings §6.
//
// resequencerWindow is the maximum span of outer-seq positions the receive
// resequencer buffers while it waits for an out-of-order frame. WG's inner RFC
// 6479 anti-replay window is 8128 messages (docs/p0-findings.md §6); the window
// here sits ~4x below that, so even a worst-case burst of reordered/held frames
// stays comfortably inside the inner filter's tolerance while spanning far more
// packets than the measured ~19 ms emulated cross-path skew (and the larger,
// more variable real Starlink/5G skew) needs at realistic per-path rates.
//
// resequencerTimeout bounds how long a head-of-line-blocked run is held for a
// missing (presumed-lost) lower frame before that gap is skipped and the run
// released. It caps the added latency — and the stall a slow/lossy path can
// impose on the whole bond (P0 §7 head-of-line concern) — to a few multiples of
// a Starlink RTT (~45 ms) rather than holding a gap forever.
const (
	resequencerWindow  = 2048
	resequencerTimeout = 250 * time.Millisecond
)

// holdBoundRTTMultiple scales the delivering paths' smoothed RTT into the
// resequencer's dynamic per-gap hold (T241, D93): hold = multiple x max SRTT,
// clamped by the resequencer to [its floor, resequencerTimeout]. Four matches the
// intent documented on resequencerTimeout — the fixed 250 ms cap was chosen as "a
// few multiples of a Starlink RTT (~45 ms)" — so a genuinely low-RTT bond pays a
// proportionally small reorder hold per multi-path gap while the 250 ms worst-case
// bound is preserved for slow paths.
const holdBoundRTTMultiple = 4

// reseqPathKey composes the OPAQUE delivering-path discriminator the resequencer's
// single-path immediate release keys on (T240/T241, D93; reviews R249/R250): the
// LOCAL receiving-path id in the high byte discriminates the edge's downlink paths
// (which share the single-homed concentrator's src address), and the SENDER-stamped
// frame PathID in the low byte discriminates the concentrator's uplink view (one
// local socket, but the edge stamps a distinct ps.id per WAN). Both operands are
// uint8, so the packing is injective; a spoofed/garbled sender PathID can only add
// distinct keys, which only ever forces the conservative full hold (DoS-neutral).
func reseqPathKey(localID, framePathID uint8) uint32 {
	return uint32(localID)<<8 | uint32(framePathID)
}

// markMultiPathExpected suppresses the resequencer's single-path immediate release when
// the peer runs an AGGREGATING (weighted) scheduler (D93 follow-up; the o3
// TestP2Aggregation regression — see reseq.SetMultiPathExpected). Immediate release is a
// single-path optimization aimed at active-backup (the D93 field case); on a weighted
// bond it is RETAINED PENDING a link-bound-venue A/B — unmeasured, default-under-
// uncertainty (defect D95, decisions:K35, tasks:T293 branch 4). Whether the
// resequencer's reordering buffer is load-bearing for genuine two-path striping (as
// opposed to the earlier burstiness-coupling theory, superseded by the frame-accurate
// offered-load fix) was NOT settled: the only available venue could not be caught
// link-bound in both arms of the A/B, so the comparison was not like-for-like. What
// would revisit this: a link-bound-venue A/B where BOTH arms are link-bound (a
// beefier host, or the real two-host setup).
func markMultiPathExpected(rq *reseq.Resequencer, scheduler sched.Scheduler) {
	if rq == nil {
		return
	}
	if _, weighted := scheduler.(*sched.WeightedScheduler); weighted {
		rq.SetMultiPathExpected(true)
	}
}

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
	// errPacerShedding is returned by Send when the scheduler shed the datagram for
	// pacing (PickPaced) while paths are healthy — deliberate rate limiting, NOT an
	// outage. It is DISTINCT from ErrNoHealthyPath so operator logs and the e2e
	// log-grep harness can tell shedding from total path failure (the drop behavior is
	// identical to the pre-existing no-path case; only the diagnostic differs). The
	// coalesced, rate-limited "pacer shedding" record is emitted by the scheduler.
	errPacerShedding = errors.New("bind: datagram shed by send pacer (paths healthy, rate limited)")
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
// exactly that peer's resequencer/reflector/FEC decoder.
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

	// txBytes/rxBytes are cumulative OUTER-wire byte counters for this (peer,path), the
	// per-path traffic accounting the /metrics exposition reports (T23). Both are
	// TRUE-WIRE-VOLUME counters: txBytes counts every outer datagram this path actually
	// writes to its socket — DATA/PARITY on the Send hot path (T23/T24), PROBE frames
	// emitted by emitProbes, and PROBE echoes reflected back by dispatchInbound — each
	// counted only once the write returns a nil error; rxBytes counts every outer
	// datagram this path's readLoop receives (DATA, PROBE, and echo alike). Neither
	// counter is DATA-only or Send-only (D48): a healthy idle standby path still emits
	// and echoes probes, so its txBytes keeps advancing even while active-backup
	// collapses all DATA onto the primary. They are atomics so the send/receive/probe
	// hot paths increment them WITHOUT taking m.mu (lock-free) from whichever goroutine
	// performs the write (Send's caller, the probe-loop goroutine, or this path's
	// readLoop), and the scrape/snapshot path reads them with a plain atomic Load.
	txBytes atomic.Uint64
	rxBytes atomic.Uint64

	// emsgsizeDrops counts DATA/PARITY datagrams this (peer,path) failed to send with
	// EMSGSIZE — the explicit "exceeds path MTU, DF set" the T201 DF policy surfaces in
	// place of silent fragmentation (accountSendError). It is the per-path metric the
	// fail-fast invariant requires: the datagram is dropped and the error still
	// propagates to the caller, but the loss is COUNTED rather than swallowed, so a
	// misconfigured over-MTU tunnel is observable at /metrics instead of appearing as an
	// inexplicable throughput hole. lastEMSGSIZEWarnNanos rate-limits the accompanying
	// WARN (warnEMSGSIZE) to one line per path per emsgsizeWarnInterval — under a
	// persistent over-MTU flow the drop is per-datagram (potentially thousands/s), so the
	// log is coalesced while the counter still advances on every occurrence. Both are
	// atomics written lock-free from the Send hot path (no m.mu), like txBytes.
	emsgsizeDrops         atomic.Uint64
	lastEMSGSIZEWarnNanos atomic.Int64

	// probeSendErrors counts PROBE-frame socket write errors emitProbes currently drops
	// (defect D96 item 4, composes with D90's emsgsizeDrops): a path whose probe writes
	// fail (e.g. a Close racing the probe-loop goroutine, or a transient socket error)
	// was previously indistinguishable from a path with 100% probe loss. Incremented
	// lock-free (no m.mu) from the probe-loop goroutine at the exact point the write
	// error is dropped; count-and-continue, no behaviour change to probing.
	probeSendErrors atomic.Uint64

	// schedIdx is this path's index in its peer's scheduler (== its position in
	// peer.paths, the invariant attachPeerPathLocked enforces). It is the pathIdx the
	// bind passes to sched.ProbeBudget.AccountProbe so a directly-written PROBE frame /
	// reflected echo charges the RIGHT per-path token bucket (T145). It is maintained
	// under m.mu at every peer.paths (re)build and splice (Open, attachPeerPathLocked,
	// detachPeerPathBoundLocked) and read lock-free (via atomic) from the receive
	// goroutine's dispatchInbound, which must not take m.mu. A momentarily-stale value
	// during a concurrent runtime membership change is benign: AccountProbe bounds-checks
	// and probe accounting is best-effort headroom, never correctness-critical.
	schedIdx atomic.Int32

	mu sync.Mutex
	// remotes is the per-SENDER-PATH return-address table (T246, defect D94): one entry
	// per sender-stamped path id seen in this view's authenticated probe plane, replacing
	// the pre-D94 single scalar whose last-prober-wins overwrite flapped the concentrator's
	// downlink destination across the edge's WANs at probe cadence. FRESHNESS is owned by
	// the authenticated probe plane exclusively — a probe request (concentrator side,
	// stamped with the edge's path id) or an echo (edge side, stamped with this path's own
	// id) establishes/refreshes an entry — while forgeable-by-design DATA may only SELECT
	// among established entries (confirmDataRemote's exact address+PathID match gate) and
	// can never introduce or move an address.
	remotes map[uint8]*remoteEntry
	// selKey/selValid name the SELECTED entry — the downlink destination getRemote()
	// returns (feeding Send, emitProbes, and the PMTU probes alike). Selection is STICKY:
	// established by the FIRST probe-learned entry (the R253 cold-start rule, so the
	// destination is valid and stable before any DATA arrives), moved only by an
	// address-match-gated DATA sample naming a DIFFERENT established entry (the edge's
	// active WAN changed), by a one-time DEAD fallback when the selected entry's probes go
	// silent (checkRemoteDead), or by an explicit SetPeerRemote override.
	selKey   uint8
	selValid bool
	// onRoam, when set (device.Up registers it on the PRIMARY peer's path via
	// Multipath.OnPathRoam), fires whenever the SELECTED downlink destination's ADDRESS
	// changes — initial establishment, a DATA-driven selection move, a DEAD fallback, an
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
// lastData is stamped by every address-match-gated DATA confirmation and gates the
// DATA-driven selection MOVE (remoteDataFreshHorizon): under weighted STRIPING both
// entries carry DATA continuously, so the selection must not chase every foreign frame.
type remoteEntry struct {
	addr      netip.AddrPort
	lastProbe time.Time
	lastData  time.Time
}

// peerState holds the per-PEER datapath state that was a process-global singleton before
// the multi-peer concentrator split: the SINGLE virtual endpoint the engine holds for this
// peer, the peer's send scheduler, its own outer-seq space, its shared send Codec, its
// probe reflector, its receive resequencer and FEC send/receive planes, its per-path probe
// initiators, and its per-(peer,path) views over the shared sockets. The single-peer
// edge/hub constructs EXACTLY ONE peerState (so behaviour is byte-identical to the pre-split
// singleton — Multipath embeds it as the primary and the datapath reaches its fields through
// that embed); the concentrator constructs one peerState per bound peer. resequencer and
// fecRecv stay atomic.Pointer, and outerSeq atomic, so the lock-free receive/send fast paths
// read them WITHOUT m.mu.
type peerState struct {
	// name is the peer id/name, the key under which Multipath.peersByName holds this peer.
	// Empty on the single-peer edge/hub (there is only one peer to key) and on the
	// concentrator's primary UNTIL SetPrimaryPeerName re-keys it to its configured name
	// (D58) — device.Up calls that whenever more than one peer is configured, so in
	// practice this is empty only for the true single-peer case.
	name string

	// psk is THIS peer's effective pre-shared key: the sole seam from which every frame
	// Codec this peer derives (its sendCodec and each per-(peer,path) receive codec, via
	// newCodec) and its probe Reflector authenticate. The single-peer edge/hub sets it to
	// the one configured psk (so behaviour is byte-identical to the pre-split singleton);
	// the concentrator sets a DIFFERENT psk per bound peer, so one peer's codec/reflector
	// rejects another peer's frames (T84). NewCodec/NewReflector PSK-derivation itself is
	// unchanged — only WHICH psk each peer feeds them differs.
	psk config.Key

	// Immutable per-peer collaborators, built at construction and persisting across the
	// Open→Close socket lifecycle (the concentrator pins virt's destination once for the
	// process life, so virt in particular must NOT be recreated per Open).
	virt      *udpEndpoint
	scheduler sched.Scheduler
	reflector *telemetry.Reflector
	// newProber mints a prober for a path admitted to THIS peer at runtime (T30); nil
	// disables runtime path addition for the peer (a bind without the probe transport).
	newProber ProberFactory
	// probers is this peer's BOOT-TIME per-path probe initiator set, in durable-membership
	// (m.defs) order — bound AND deferred — shared with this peer's scheduler. At Open each
	// bound entry is bound onto its peerPathState (pp.prober), and thereafter the hot paths
	// reach a path's prober through the peerPathState — never by indexing this slice — so a
	// runtime path add/remove cannot race echo handling. Nil when the peer runs without the
	// probe transport; a nil here also gates the whole probe loop off.
	probers []*telemetry.Prober

	// configuredRemote is THIS peer's CONFIGURED wire remote — the concentrator endpoint an EDGE
	// peer statically targets (T251/Q68b). It seeds every one of this peer's paths at Open (and any
	// path added at runtime) so a MULTI-EXIT edge sends each peer's DATA/PROBE frames to ITS OWN
	// concentrator, never a single bind-global default that would conflate two peers onto one hub.
	// Unset on a concentrator peer (which learns its edge remote dynamically from authenticated
	// inbound) and on a single-peer edge/hub (which keeps the bind-global defaultRemote, byte-
	// identical to pre-T251). Seeded by SeedEdgePeerRemotes; durable across the Open/Close cycle.
	configuredRemote    netip.AddrPort
	hasConfiguredRemote bool

	// Per-Open state, (re)built by Open and cleared by Close.
	sendCodec *frame.Codec
	paths     []*peerPathState
	outerSeq  atomic.Uint64
	// resequencer is this peer's shared T18 receive resequencing buffer. Published
	// atomically so the per-path readLoop goroutines read it WITHOUT m.mu.
	resequencer atomic.Pointer[reseq.Resequencer]
	// FEC datapath (T24), per peer. Both published atomically like resequencer so the
	// lock-free receive fast path Loads fecRecv, and the lazy re-instantiation on re-bind
	// (ensurePeerReceiveInstantiated, on a readLoop that must not take m.mu) can Store
	// fecSend without racing Send's under-m.mu Load. The ENCODER inside fecSend is still
	// mutated (Admit/Tick) only under m.mu — the atomic pointer governs publication of the
	// fecSender object, not its encoder's single-writer discipline. Both nil when FEC is off.
	fecSend atomic.Pointer[fecSender]
	fecRecv atomic.Pointer[fecReceiver]

	// parityCarry is the count of FEC PARITY frames this peer has written to a path
	// socket but not yet reported to its scheduler as offered load (defect D95,
	// decisions:K35 §3c). Parity shards consume the SAME per-path wire capacity that
	// PerPathCapacity denominates, so they must be metered as offered frames — but a
	// batch's parity count is not known until the encoder's Admit runs inside Send's
	// per-buffer loop, which is AFTER Pick has already stamped the chosen path into
	// every frame. The count is therefore carried to the NEXT Send for this peer, which
	// consumes it with Swap(0) and adds it to len(bufs). It is incremented only where a
	// parity frame actually reached the socket, so it counts each parity frame exactly
	// once and never counts one that was dropped in framing.
	//
	// It is ATOMIC because both writers run WITHOUT m.mu: Send's egress loop runs after
	// m.mu.Unlock(), and fecFlushDeadline's write loop likewise. A plain field would be
	// a data race (the same reason ps.txBytes and peer.outerSeq are atomics). With FEC
	// off it is never incremented, so the datapath reads a constant 0 and Send's offered
	// count is exactly len(bufs).
	parityCarry atomic.Uint64

	// lifecycleMu serializes lazy (re)instantiation of the heavy trio (resequencer, fecRecv,
	// fecSend) on a readLoop goroutine (ensurePeerReceiveInstantiated) against teardown's
	// clearing of that trio (teardownPeerLocked), so a teardown interleaving mid-instantiation
	// can never leave a half-published plane (a fecRecv/fecSend without its resequencer) nor
	// resurrect a plane on a torn-down peer that the next re-bind then reuses stale. It is a
	// LEAF lock taken either ALONE (instantiation — which must never take m.mu, so Close's
	// readersWG.Wait under m.mu cannot deadlock on it) or UNDER m.mu (teardown), giving the
	// single fixed order m.mu -> lifecycleMu and no cycle (instantiation never reaches for m.mu).
	lifecycleMu sync.Mutex
}

// newPeerState builds the durable per-peer datapath state whose probe Reflector — and,
// through ps.psk, every frame Codec this peer later derives in Open/AddPath (its sendCodec
// and each per-(peer,path) receive codec, via newCodec) — authenticate under THIS peer's
// psk. It is the seam that replaces the pre-split single-psk assumption (T84): the
// single-peer edge/hub mints exactly one (the primary) from the sole configured psk, while
// the concentrator mints one per bound peer from that peer's DIFFERENT effective psk, so a
// peerState built from a different psk gets its own codec/reflector and cross-psk frames are
// rejected. virt is created here (not per Open) because the concentrator pins its destination
// once for the process life. The Reflector draws its per-path challenges from crypto/rand,
// exactly as before. The per-Open fields (sendCodec, paths, resequencer, FEC planes) are
// left zero for Open to (re)build.
func newPeerState(name string, psk config.Key, scheduler sched.Scheduler, newProber ProberFactory, probers []*telemetry.Prober) *peerState {
	return &peerState{
		name:      name,
		psk:       psk,
		virt:      &udpEndpoint{},
		scheduler: scheduler,
		reflector: telemetry.NewReflector(psk, rand.Reader),
		newProber: newProber,
		probers:   probers,
	}
}

// newCodec derives a fresh frame Codec bound to THIS peer's psk — the send Codec at Open and
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
// echo, reports StateDown, which the scheduler excludes from Pick, exactly as the
// runtime path-down model treats a live-but-silent path. It carries the boot-time
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
	// reconcileDeferred: true once this path has BOUND but FAILED promotion (a scheduler/path
	// index skew or codec build error), so a persistently un-promotable deferred path WARNs
	// once for the whole failure window rather than once per 1 Hz reconcile tick. It is NOT
	// cleared on a later listen success (that would re-spam every tick, since the listen
	// re-succeeds each tick while promotion keeps failing); a path that finally promotes
	// leaves m.deferred, discarding the latch with it.
	warnedPromoteFail bool
}

// remoteDeadAfter is the probe-silence bound on the SELECTED remote entry after which
// checkRemoteDead performs the one-time sticky fallback to the freshest probe-learned
// entry (T246, defect D94). It is deliberately ABOVE the liveness DownAfter (1200 ms):
// path liveness declares the path down first (and the scheduler moves egress where it
// can); this table-level fallback is the concentrator's belt-and-suspenders — its single
// path gives the scheduler nothing to switch, so the DESTINATION itself must follow the
// edge's surviving WAN. 2x DownAfter keeps ordinary probe jitter from ever tripping it
// while still bounding downlink-failover latency to a couple of liveness windows.
const remoteDeadAfter = 2 * telemetry.DefaultDownAfter

// remoteDataFreshHorizon is the DATA-activity horizon gating a DATA-driven selection
// MOVE (T246, defect D94; the o3 P2Aggregation regression): a foreign address-match-gated
// DATA sample moves the selection ONLY when the currently-SELECTED entry's own DATA has
// been silent for at least this long. Under weighted STRIPING both entries carry DATA
// continuously (alternating per frame), so without this horizon the selection would chase
// every foreign frame — flapping the downlink destination at FRAME rate (worse than the
// probe-cadence flap D94 fixed) and shredding the return path. Under active-backup the
// edge's genuine WAN switch silences the old entry's DATA, so the downlink follows within
// this horizon. One DefaultDownAfter matches the liveness DOWN detection scale, keeping
// the downlink-follow latency inside the existing failover budget.
const remoteDataFreshHorizon = telemetry.DefaultDownAfter

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
// itself (the R253 cold-start rule — the destination must be valid before any DATA so
// the concentrator's own probe/liveness plane works), and an in-place ADDRESS change of
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

// confirmDataRemote lets a DATA frame SELECT — never establish — the downlink
// destination (T246, defect D94): a frame whose (sender path id, source address) EXACTLY
// matches an entry the authenticated probe plane already established stamps that entry's
// DATA freshness, and may mark it the active one. A mismatched or unknown (srcAP, PathID)
// changes NOTHING — DATA is forgeable by design, so it can only ever pick among
// probe-vouched addresses — and a same-entry confirmation just refreshes lastData (no
// roam callback). Selection MOVES (one roam callback) only when the match names a
// DIFFERENT established entry AND the currently-selected entry's own DATA has been silent
// past remoteDataFreshHorizon: under active-backup a genuine edge WAN switch silences the
// old entry so the move follows promptly, while under weighted STRIPING both entries stay
// DATA-fresh and the selection never chases the per-frame alternation (the o3
// P2Aggregation regression — a frame-rate destination flap on the return path).
func (ps *peerPathState) confirmDataRemote(senderPathID uint8, src netip.AddrPort) {
	ps.confirmDataRemoteAt(senderPathID, src, time.Now())
}

// confirmDataRemoteAt is confirmDataRemote with an injectable clock for tests.
func (ps *peerPathState) confirmDataRemoteAt(senderPathID uint8, src netip.AddrPort, now time.Time) {
	ps.mu.Lock()
	e, ok := ps.remotes[senderPathID]
	if !ok || e.addr != src {
		ps.mu.Unlock()
		return
	}
	e.lastData = now
	if ps.selValid && ps.selKey == senderPathID {
		ps.mu.Unlock()
		return
	}
	if ps.selValid {
		if sel := ps.remotes[ps.selKey]; sel != nil && !sel.lastData.IsZero() && now.Sub(sel.lastData) < remoteDataFreshHorizon {
			// The selected entry itself carries fresh DATA (weighted striping, or a
			// transient duplicate source): stay sticky, never chase per-frame alternation.
			ps.mu.Unlock()
			return
		}
	}
	prev, hadPrev := ps.selectedAddrLocked()
	ps.selKey, ps.selValid = senderPathID, true
	next, _ := ps.selectedAddrLocked()
	cb := ps.onRoam
	ps.mu.Unlock()
	if (!hadPrev || prev != next) && cb != nil {
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

// getRemote returns the SELECTED downlink destination — the sticky, DATA-confirmed
// active-path address (T246, defect D94) — feeding Send, emitProbes, and the PMTU
// probes alike.
func (ps *peerPathState) getRemote() (netip.AddrPort, bool) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return ps.selectedAddrLocked()
}

// errNoPathRemote is returned by the PMTU send seam when a probe is attempted before
// this path has learned a remote — a transient startup condition, so the search stays
// unconverged and retries on a later tick rather than treating it as a size verdict.
var errNoPathRemote = errors.New("bind: path has no remote for PMTU probe")

// buildPMTUProbe constructs this path's PMTU echo-await backend once its prober and
// shared socket are set (T227, defect D88). The send func writes the padded probe on
// this path's OUTER socket to its learned/configured remote and maps a DF EMSGSIZE
// (the T201 over-PMTU signal) to telemetry.ErrProbeTooLarge, so the binary search reads
// an oversize probe as "too large" (echoed=false) rather than a transport fault. It
// returns nil when the path has no prober (the probe transport is disabled), matching
// pmtuProbe's nil-safe contract at the NotifyEcho call site.
func (ps *peerPathState) buildPMTUProbe() *telemetry.EchoAwaitProbe {
	if ps.prober == nil {
		return nil
	}
	send := func(raw []byte) error {
		remote, ok := ps.getRemote()
		if !ok {
			return errNoPathRemote
		}
		if _, err := ps.conn.WriteToUDPAddrPort(raw, remote); err != nil {
			if errors.Is(err, syscall.EMSGSIZE) {
				return telemetry.ErrProbeTooLarge
			}
			return err
		}
		return nil
	}
	return telemetry.NewEchoAwaitProbe(ps.prober, send, 0, nil)
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

// Multipath is the P1 bonding conn.Bind: one UDP socket per configured path
// (bound to the path's source address), all fronted by a SINGLE stable virtual
// endpoint the engine holds per peer. Send wraps each opaque WireGuard datagram
// in an outer DATA frame (own outer-seq + path-id) and picks a path; one
// Bind-owned reader per path unwraps DATA frames into the shared resequencer, and
// a SINGLE engine-facing ReceiveFunc drains it and hands the inner WG datagram up
// under the shared virtual endpoint (the fan-in that lets paths be added/removed at
// runtime without the engine spawning receive goroutines — T30). It replaces
// bind.Passthrough behind the same conn.Bind seam (device wiring is unchanged).
//
// Lifecycle: the sockets live for the duration of an Open→Close span, NOT the
// Multipath's whole life. The amneziawg engine calls Close() before every Open()
// (device.upLocked → BindUpdate → closeBindLocked, and on IpcSet listen_port /
// route-change events) and cycles Close↔Open on Down/Up, so — exactly like
// conn.StdNetBind — Open creates the per-path sockets and Close tears them down
// AND clears the path state so the next Open rebuilds from scratch. The "closed"
// state is simply "no bound sockets" (len(paths)==0); there is no separate sticky
// flag that a later Open would have to reset.
//
// Path policy: an injected sched.Scheduler (T15) chooses the egress path per
// datagram. The P1 MVP wires an active-backup scheduler (all traffic on the
// preferred primary, transparent failover to a backup on a T13 path-DOWN signal
// with failback hysteresis); the weighted/FEC-aware policy (T21) is a different
// Scheduler swapped in here with no Bind change. No FEC/resequencing yet
// (P2/P3), but every DATA frame carries its outer-seq and path-id so T18/T24 can
// consume them.
// ProberFactory mints a *telemetry.Prober for a path admitted at runtime (T30),
// stamped with the given stable path-id and the path's OWN ride_through dwell (D86/T207)
// so a runtime-added path measures liveness with the same configured hysteresis as a
// boot-time path. The device wires it capturing the SAME per-boot session id, global
// down_after threshold, clock, PSK, and logger the boot-time probers were built with, so a
// runtime path's liveness is measured identically to a boot-time path with the same
// ride_through (its probes join the same session so the peer's reflector adopts them without
// a challenge reset). It is nil on a bind built without the probe transport (the T12 unit
// tests), which therefore cannot add paths at runtime.
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

type Multipath struct {
	defs []config.Path

	// classify maps each outbound datagram to its pacer traffic class from the inner
	// WireGuard message type, parameterized by the tunnel's Amnezia obfuscation profile
	// so a WireGuard control frame (handshake/keepalive) is recognised — and pacing-
	// exempted — under advanced security too, not only in vanilla mode (defect D22). It
	// is immutable after construction and holds no lock, so Send reads it off m.mu.
	classify wgClassifier

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

	// clock is the bind-level injectable time source the adaptive drive's self-throttle
	// reads (driveAdaptiveControllerLocked). It is an injection seam mirroring
	// deferredListen / resolveDeviceBind / addPathListen: the default is the real
	// telemetry.SystemClock (set in NewMultipath), and a test overrides it PRE-OPEN with
	// the same hand-advanced fake clock its probers/scheduler read, so the throttle's
	// interval arithmetic advances in lockstep with liveness transitions rather than on the
	// wall clock (D97). Multipath held no injected clock before — the fake clock reached
	// only probers/scheduler because tests constructed THOSE with it — so the throttle read
	// bare time.Now() and could not be driven deterministically. Immutable after Open (the
	// fecTickLoop goroutine reads it concurrently), never nil.
	clock telemetry.Clock

	mu sync.Mutex

	// The PRIMARY peer, embedded so the single-peer datapath (Send, the receive drainer,
	// the probe loop) and the existing single-peer tests reach its fields — virt,
	// scheduler, reflector, newProber, probers, sendCodec, paths, outerSeq, resequencer,
	// fecSend/fecRecv — transparently through promotion, keeping behaviour byte-identical
	// to the pre-split singleton. It is peers[0]; the concentrator's additional peers live
	// in peers/peersByName. The former process-global resequencer/outerSeq/scheduler now
	// live on this peerState, NOT on Multipath.
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
	// receive hot path — the same lock-free-publish discipline resequencer/fecRecv use. It
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
	// a lock-free Load (no m.mu — a reader must never block on m.mu, since Close waits on the
	// readers WHILE holding it), and a new binding is installed lock-free by a CAS republish of a
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
	// that pointer to the peer's datapath state (outerSeq, scheduler, sendCodec, fecSend,
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
	// assignable at the last Open (EADDRNOTAVAIL). They are NOT in shared or the
	// scheduler — the tunnel runs on the paths that bound — but are recorded here,
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

	// fecCfg / adaptiveCfg are the FEC configuration (T24/T29), shared by every peer; the
	// per-peer fecSend/fecRecv runtime state Open builds from them lives on peerState. nil
	// when FEC (respectively adaptive FEC) is disabled — the datapath is then byte-for-byte
	// the pre-T24 behaviour. See fec.go.
	fecCfg      *fec.Config
	adaptiveCfg *adaptivefec.Config

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
}

// compile-time proof that Multipath satisfies the engine's Bind contract.
var _ Bind = (*Multipath)(nil)

// NewMultipath returns a closed multipath Bind over the configured paths; call
// Open to bind the per-path sockets. The PSK keys the outer framing and must be
// set (config validation guarantees it). The scheduler is the injected send-side
// path-selection policy (T15) whose priority order MUST match the paths slice
// order (index 0 = the preferred primary); it is a required collaborator so the
// send path is never without a policy.
//
// probers is the per-path probe initiator that drives on-wire liveness (T13/T37).
// When non-nil it MUST hold exactly one *telemetry.Prober per path, in path order,
// and those SAME values MUST be the scheduler's PathHealth sources so the liveness
// the probe loop measures is the liveness the scheduler selects on. Pass nil to
// run the bind without the probe transport (the T12 unit tests drive selection via
// sched.AlwaysUp instead). A Reflector is built from the PSK to answer peer probes.
//
// newProber is the factory the runtime path-add path (AddPath, T30) uses to mint a
// prober for a newly-admitted path; it must be non-nil to allow AddPath and MUST be
// paired with a non-nil probers (the boot-time set) — a factory without a boot-time
// slice would let AddPath append to a nil m.probers, breaking the m.paths/m.probers
// alignment invariant and panicking on the next Open at m.probers[i]. Pass nil to
// forbid runtime path addition.
//
// fecCfg is the fixed-ratio Reed-Solomon FEC configuration (T24). Pass nil to run the
// datapath with FEC disabled (the pre-T24 behaviour, byte-for-byte); a non-nil,
// pre-validated config turns the send-side parity encoder and receive-side recovery
// decoder on. It is validated here (fail fast) so an invalid ratio is rejected at
// construction rather than at the first Open.
// adaptiveCfg is the adaptive-FEC controller configuration (T29). Pass nil for the
// fixed-ratio behaviour (T24); a non-nil config REQUIRES a non-nil fecCfg (the
// controller resizes the FEC encoder's parity — it is meaningless with the plane off)
// and reinterprets fecCfg.ParityShards as the controller's parity ceiling. It is
// validated here (fail fast) so a mis-tuned control law is rejected at construction.
//
// amnezia is the tunnel's AmneziaWG obfuscation profile (config.Amnezia). It
// parameterizes the send-path frame-type classifier so a WireGuard control frame is
// recognised and pacing-exempted under advanced security — custom magic headers and
// handshake junk prefixes — as well as in vanilla mode (defect D22). Pass the zero value
// for a vanilla (unobfuscated) tunnel; the classifier then uses the default type words
// and no junk prefix. It does NOT need to match the config's validated state — an
// all-zero profile is exactly the vanilla classifier.
//
// lg is the structured logger (internal/log); it is component-scoped to "bind"
// (log.Logger.Component) and stored so the SO_BINDTODEVICE→source-IP fallback a
// forced bind="device" path can silently take is surfaced at WARN instead of the
// pre-D53 silence (see warnForcedDeviceUnresolvable / warnDeviceBindFallback). It is
// a required collaborator, like scheduler — fail fast on nil rather than let the bind
// run logging-blind.
func NewMultipath(paths []config.Path, psk config.Key, scheduler sched.Scheduler, probers []*telemetry.Prober, newProber ProberFactory, fecCfg *fec.Config, adaptiveCfg *adaptivefec.Config, amnezia config.Amnezia, lg log.Logger) (*Multipath, error) {
	if len(paths) == 0 {
		return nil, errors.New("bind: at least one path is required")
	}
	if !psk.IsSet() {
		return nil, errors.New("bind: PSK is required for outer framing")
	}
	if len(paths) > 256 {
		// path-id is a uint8 in the DATA frame header.
		return nil, fmt.Errorf("bind: at most 256 paths supported, got %d", len(paths))
	}
	if scheduler == nil {
		return nil, errors.New("bind: a send scheduler is required")
	}
	if lg == nil {
		return nil, errors.New("bind: a logger is required")
	}
	if newProber != nil && probers == nil {
		// A runtime-path factory without a boot-time prober slice would let AddPath append
		// to a nil m.probers, desyncing m.paths from m.probers and panicking on the next
		// Open at m.probers[i]. The two are paired by construction; enforce it here.
		return nil, errors.New("bind: newProber requires a non-nil probers (boot-time set)")
	}
	if probers != nil && len(probers) != len(paths) {
		return nil, fmt.Errorf("bind: probers must have one entry per path (got %d, want %d)", len(probers), len(paths))
	}
	for i, pr := range probers {
		if pr == nil {
			return nil, fmt.Errorf("bind: prober %d is nil", i)
		}
	}
	if fecCfg != nil {
		if err := fecCfg.Validate(); err != nil {
			return nil, fmt.Errorf("bind: invalid FEC configuration: %w", err)
		}
		if fecCfg.Deadline > maxFECDeadline {
			// A deadline this large makes every deadline-flushed group's recovery
			// structurally late: the resequencer skips the gap (resequencerTimeout) before
			// the parity-derived frames land, so recovery never delivers. Reject it rather
			// than ship a bond whose FEC silently cannot help (defect #4).
			return nil, fmt.Errorf("bind: fec deadline %s exceeds the max %s (must stay safely below the resequencer's %s per-gap timeout so deadline-flushed recovery lands before the gap is skipped)", fecCfg.Deadline, maxFECDeadline, resequencerTimeout)
		}
	}
	if adaptiveCfg != nil {
		if fecCfg == nil {
			return nil, errors.New("bind: adaptive FEC requires a FEC configuration (the controller resizes the FEC encoder's parity)")
		}
		if err := adaptiveCfg.Validate(); err != nil {
			return nil, fmt.Errorf("bind: invalid adaptive FEC configuration: %w", err)
		}
		// The controller's parity ceiling (MaxParity) is the fixed FEC ParityShards both
		// ends agree on: the receiver's decoder is built at fecCfg.ParityShards, and the
		// encoder must never emit more parity than that ceiling (SetParity clamps to it).
		// A mismatch would let the controller target a parity index the decoder rejects,
		// so bind the two together at construction rather than trusting the caller.
		if adaptiveCfg.MaxParity != fecCfg.ParityShards {
			return nil, fmt.Errorf("bind: adaptive FEC parity ceiling (MaxParity=%d) must equal the FEC parity_shards (%d), which the receiver's decoder is built at", adaptiveCfg.MaxParity, fecCfg.ParityShards)
		}
		if adaptiveCfg.DataShards != fecCfg.DataShards {
			return nil, fmt.Errorf("bind: adaptive FEC DataShards (%d) must equal the FEC data_shards (%d)", adaptiveCfg.DataShards, fecCfg.DataShards)
		}
	}
	// The single-peer edge/hub constructs EXACTLY ONE peerState (the primary). virt is
	// created here — NOT per Open — because the concentrator pins its destination once for
	// the process life and every existing test relies on the virtual-endpoint pointer being
	// stable across Open/Close/add/remove.
	primary := newPeerState("", psk, scheduler, newProber, probers)
	m := &Multipath{
		defs:              append([]config.Path(nil), paths...),
		log:               lg.Component("bind"),
		classify:          newWGClassifier(amnezia),
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
		clock:            telemetry.SystemClock{},
		peerState:        primary,
		peers:            []*peerState{primary},
		peersByName:      map[string]*peerState{primary.name: primary},
		peerByEndpoint:   map[netip.AddrPort]*peerState{},
		peerByVirt:       map[*udpEndpoint]*peerState{primary.virt: primary},
		edgePeerByRemote: map[netip.AddrPort]*peerState{},
		maxDemuxSources:  defaultMaxDemuxSources,
		fecCfg:           fecCfg,
		adaptiveCfg:      adaptiveCfg,
	}
	// Publish the initial (primary-only) peer view the receive drainer iterates. No
	// concurrency yet — the bind is not open — so this runs without m.mu.
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
// send scheduler, boot-time per-path prober set, and runtime prober factory (all keyed on that
// peer's psk, so one peer's codec/reflector reject another's frames — T84/R72). The peer's
// STABLE virtual endpoint is minted here (newPeerState) and registered in peerByVirt so an
// outbound Send routes replies back to THIS peer; Open then builds this peer's per-(peer,path)
// view of every bound socket, reconciles its scheduler, and (via newReceiveFunc) reports its
// virt to the engine on the first inbound frame (invariant A1: one virtual endpoint per peer).
//
// It MUST be called BEFORE Open (while the bind is closed): the per-(peer,path) views are
// rebuilt by Open from the registered peer set on every Open span (including each Close→Open
// cycle the engine drives on Down/Up and route changes), so a peer registered after the sockets
// are bound would be view-less and its DATA/PROBE never routed. probers, when supplied, must be
// index-aligned with the configured path membership (m.defs), exactly as the primary's are.
func (m *Multipath) AddConcentratorPeer(name string, psk config.Key, scheduler sched.Scheduler, probers []*telemetry.Prober, newProber ProberFactory) error {
	if name == "" {
		return errors.New("bind: concentrator peer name is required")
	}
	if !psk.IsSet() {
		return errors.New("bind: concentrator peer psk is required")
	}
	if scheduler == nil {
		return errors.New("bind: concentrator peer requires a send scheduler")
	}
	if newProber != nil && probers == nil {
		return errors.New("bind: newProber requires a non-nil probers (boot-time set)")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.paths) != 0 {
		return errors.New("bind: concentrator peers must be registered before Open")
	}
	if probers != nil && len(probers) != len(m.defs) {
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
	p := newPeerState(name, psk, scheduler, newProber, probers)
	m.peers = append(m.peers, p)
	m.peersByName[name] = p
	m.peerByVirt[p.virt] = p
	// Publish the grown peer set so the engine-facing receive drainer (newReceiveFunc) will
	// enumerate this peer's resequencer once Open builds it.
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
func (m *Multipath) Open(port uint16) ([]ReceiveFunc, uint16, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.paths) != 0 {
		return nil, 0, conn.ErrBindAlreadyOpen
	}

	// (Re)build the per-Open datapath planes — send Codec, receive resequencer, and FEC
	// send/receive state — fresh for this bring-up, for EVERY bound peer (not just the
	// primary via promotion). This keeps Open symmetric with closeSocketsLocked, which
	// clears every peer's per-Open state: a concentrator peer bound before Open must get
	// its OWN fresh planes on each Close→Open cycle, and one peer's (re)creation must never
	// touch another peer's release point or FEC group state. On the single-peer edge/hub
	// m.peers holds only the primary, so this is byte-identical to the pre-split rebuild.
	// See openPeerDatapathLocked.
	for _, p := range m.peers {
		if err := m.openPeerDatapathLocked(p); err != nil {
			_ = m.closeSocketsLocked()
			return nil, 0, err
		}
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
	// the bond comes up on the paths that DO bind (G2/W1, approach A). Tolerating it
	// requires the runtime path-down machinery: the deferred path's prober stays
	// StateDown so the scheduler excludes it, and a DynamicScheduler whose membership
	// can be reconciled to only the bound paths. Absent the probe transport (the T12
	// no-prober unit binds) or a dynamic scheduler there is no such Down model, so —
	// exactly as AddPath refuses a runtime add without probers — every bind error
	// stays fatal there. A MALFORMED source_addr never reaches here (config.validate
	// rejects it at load), and any OTHER bind error (EADDRINUSE, permission) is fatal.
	_, dynOK := m.scheduler.(sched.DynamicScheduler)
	tolerateDefer := m.probers != nil && dynOK
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
			if tolerateDefer && errors.Is(err, syscall.EADDRNOTAVAIL) {
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
			_ = m.closeSocketsLocked()
			return nil, 0, fmt.Errorf("bind: open path %q on %s: %w", def.Name, def.SourceAddr, err)
		}
		// The listen succeeded: a working conn materialized. The D53 fallback-fact WARNs
		// are deferred past the peer fan-out below (round 3 / CRITICISM 1): a peer's
		// codec build or prober-fan-out desync in that loop aborts this ENTIRE Open call
		// (closeSocketsLocked + a returned error), so warning here — before the path is
		// actually installed into every peer's paths/scheduler — would log an
		// outcome-false "falling back to source-IP pinning" claim for a bond that never
		// came up at all.
		//
		// Large SO_RCVBUF, best-effort: the kernel caps at net.core.rmem_max and
		// SetReadBuffer does not require privilege, so a returned error is rare;
		// treat it as non-fatal rather than refusing to bind in a restricted env.
		_ = c.SetReadBuffer(socketRecvBuffer)

		shared := &sharedPathState{name: def.Name, id: uint8(i), src: def.SourceAddr, conn: c, bindMode: def.Bind, boundDevice: bindDevs[i]}
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
				_ = c.Close()
				_ = m.closeSocketsLocked()
				return nil, 0, err
			}
			pp := &peerPathState{sharedPathState: shared, peer: p, codec: codec}
			if p.probers != nil {
				// Fail fast rather than panic if a peer's prober slice ever falls short of the
				// shared m.defs membership: every runtime admission (bound OR deferred) fans a
				// per-peer prober out to EVERY peer (AddPath), so p.probers stays index-aligned
				// with m.defs. A divergence is a wiring defect — surface it as a bind error
				// instead of an index-out-of-range panic that would crash the daemon.
				if i >= len(p.probers) {
					_ = c.Close()
					_ = m.closeSocketsLocked()
					return nil, 0, fmt.Errorf("bind: peer %q prober set (len %d) is shorter than the path membership at index %d — per-peer prober fan-out desync", p.name, len(p.probers), i)
				}
				pp.prober = p.probers[i]
				pp.pmtuProbe = pp.buildPMTUProbe()
				if pi == 0 {
					// Reconcile the SHARED DATA-frame path-id to the PRIMARY prober's IMMUTABLE
					// stamp rather than the slice index i: after a runtime RemovePath the survivor
					// keeps its original (higher) stamp, so index-based numbering would renumber a
					// live path AND diverge its DATA id from its PROBE stamp. Every peer's
					// probers[i] carries the SAME stamp (the device stamps each peer's boot prober
					// for path i identically), so taking it from the primary is authoritative and
					// keeps DATA and every peer's PROBE agreeing on the wire.
					shared.id = pp.prober.PathID()
				}
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
			// Stamp the scheduler index (== this path's position in p.paths) BEFORE the append,
			// so a directly-written probe/echo on this path charges the right token bucket (T145).
			pp.schedIdx.Store(int32(len(p.paths)))
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
		m.shared = append(m.shared, shared)
		if firstBound {
			actualPort = uint16(c.LocalAddr().(*net.UDPAddr).Port)
			firstBound = false
		}
	}

	// Hard guard (a): a bond with NO transport is impossible. If every configured path
	// deferred, tolerance must NOT degrade to a zero-path bind — fail fatally, exactly
	// as the pre-tolerance Open did when the sole path could not bind.
	if len(m.paths) == 0 {
		_ = m.closeSocketsLocked()
		return nil, 0, fmt.Errorf("bind: no configured path could bind its source address (all %d deferred as not-yet-assignable)", len(m.deferred))
	}

	// nextPathID is a MONOTONIC HIGH-WATER carried ACROSS Open spans, never decreased.
	// Close does not reset it, and here it is only ever RAISED to at least max(existing
	// path stamp)+1. Resetting it to len(m.paths) (the pre-fix behaviour) re-minted an id
	// a survivor still held once a RemovePath had opened a gap in the stamp space below
	// that survivor's stamp: the next runtime AddPath then collided with the live path at
	// an identical (PathID, SessionID), and because the peer's Reflector keys anti-replay
	// AND the session challenge PER PathID, the strict-monotonic replay filter dropped one
	// of the two independent ProbeSeq streams -> probe loss / false-DOWN. Deriving the
	// high-water from the (prober-stamped) ps.id covers both the probe-transport case and
	// the T12 no-prober case (where ps.id == i, so the high-water is len(m.paths)).
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

	// Reconcile the scheduler's membership with the path slice just rebuilt from
	// m.defs. A runtime AddPath/RemovePath (T30) keeps m.defs, m.probers, AND the
	// live scheduler in lockstep during an Open span; re-pinning the scheduler's
	// health list to the BOUND probers HERE — index-aligned with m.paths, since each
	// bound path appended its ps.prober to boundProbers in order above — is the single
	// reconciliation point that makes that runtime membership survive this
	// Close→Open cycle without a scheduler/path desync (no frozen zombie health
	// entry, no resurrected removed path). A DEFERRED path (not-yet-assignable
	// source_addr) is deliberately EXCLUDED here: its prober is absent from the
	// scheduler, so Pick can never select it, and Send's Pick->m.paths[idx] mapping
	// stays index-aligned with the bound-only path slice. Only meaningful with the
	// probe transport: a bind without probers cannot change membership at runtime
	// (AddPath is refused) and never defers, so its scheduler is left exactly as built.
	// Reconcile EACH bound peer's scheduler membership with the path slice just rebuilt from
	// m.defs (T93): every peer's per-(peer,path) views were appended in m.defs order above, so a
	// peer's scheduler health list is that peer's BOUND probers in order (a DEFERRED path
	// contributed no view and is deliberately excluded, exactly as for the primary). On the
	// single-peer edge/hub m.peers holds only the primary, so this reconciles exactly the one
	// scheduler — byte-identical to the pre-split single reconcile. A peer without the probe
	// transport or with a non-dynamic scheduler is left as built (the T12 no-prober unit binds
	// never change membership at runtime and never defer).
	for _, p := range m.peers {
		pdyn, pdynOK := p.scheduler.(sched.DynamicScheduler)
		if p.probers == nil || !pdynOK {
			continue
		}
		admissions := make([]sched.PathAdmission, 0, len(p.paths))
		for _, pp := range p.paths {
			if pp.prober != nil {
				admissions = append(admissions, admissionFor(p.scheduler, pp.prober))
			}
		}
		if err := pdyn.SetPaths(admissions); err != nil {
			_ = m.closeSocketsLocked()
			return nil, 0, fmt.Errorf("bind: reconcile scheduler on open: %w", err)
		}
	}

	// Stand up the receive fan-in: one Bind-owned reader per path feeding the shared
	// resequencer, and a single engine-facing drainer. Both channels are recreated
	// per Open so a Close→Open cycle starts clean.
	m.deliverSignal = make(chan struct{}, 1)
	m.recvClosed = make(chan struct{})
	for _, ps := range m.paths {
		m.readersWG.Add(1)
		go m.readLoop(ps, m.deliverSignal)
	}
	// Deadline-tick goroutine for FEC group close (T24): it flushes a partial group's
	// parity on time under low load so a group is never stranded waiting for the size
	// threshold. Tracked by readersWG (like the readers) so Close waits for it; it
	// exits on recvClosed. It TryLocks m.mu, so it never blocks Close's readersWG.Wait
	// held under m.mu.
	// Started whenever FEC is configured at all (m.fecCfg != nil), NOT merely when the
	// PRIMARY's fecSend has already materialized: a concentrator peer's fecSend can still
	// be nil here and only Store lazily on its first authenticated bind
	// (ensurePeerReceiveInstantiated), yet it must receive deadline flushes as soon as it
	// does. Gating on m.fecSend.Load() (the primary's plane, via promotion) would leave
	// such a peer's straggler parity stranded until the next size-triggered close (D44).
	if m.fecCfg != nil {
		m.readersWG.Add(1)
		go m.fecTickLoop(m.fecCfg.Deadline, m.recvClosed)
	}
	return []ReceiveFunc{m.newReceiveFunc(m.deliverSignal, m.recvClosed)}, actualPort, nil
}

// openPeerDatapathLocked (re)builds ONE peer's per-Open datapath planes: its send Codec
// (derived from THIS peer's psk), its receive resequencer, and — when FEC is configured —
// its FEC send/receive planes. Each is fresh per Open so a Close→Open cycle (or reconnect)
// re-pins the peer's release point and re-anchors its FEC grouping to this bring-up rather
// than a stale prior span. It writes ONLY the given peer's fields (from the peer's own psk
// and the bind-wide fecCfg/adaptiveCfg), so one peer's (re)creation never touches another
// peer's resequencer or FEC group state — the per-peer lifecycle boundary that keeps a
// reconnect on one peer from disturbing another's. Caller holds m.mu; on error the caller
// unwinds the whole Open via closeSocketsLocked (which clears every peer's per-Open state),
// so a partial build here is cleaned up.
func (m *Multipath) openPeerDatapathLocked(ps *peerState) error {
	sendCodec, err := ps.newCodec()
	if err != nil {
		return err
	}

	// A fresh resequencer per Open: its release point re-pins to the first frame THIS peer
	// receives after this bring-up, so a Close→Open cycle (or reconnect) never wedges on a
	// stale high-water outer-seq. Published atomically so the peer's per-path readLoop
	// goroutines read it WITHOUT m.mu.
	ps.resequencer.Store(reseq.New(resequencerWindow, resequencerTimeout, reseq.SystemClock{}))
	markMultiPathExpected(ps.resequencer.Load(), ps.scheduler)

	// Fresh FEC send/receive state per Open, when FEC is enabled (T24). The encoder
	// group state and the decoder's per-group buffers re-pin with the sockets, so a
	// Close→Open cycle never reconstructs against a stale group. Both are torn down (per
	// peer) in closeSocketsLocked. A build error here is a programmer error (the ratio was
	// validated in NewMultipath), so it fails the Open.
	if m.fecCfg != nil {
		fs, err := m.newFECSender()
		if err != nil {
			return err
		}
		ps.fecSend.Store(fs)
		fr, err := m.newFECReceiver()
		if err != nil {
			return err
		}
		ps.fecRecv.Store(fr)
		// D93/T241: an active FEC decoder can still repair a head-of-line gap, so the
		// resequencer (stored above) must keep its full hold — no single-path
		// immediate release and no RTT-shortened bound — while FEC is on.
		ps.resequencer.Load().SetFECActive(true)
	}
	ps.sendCodec = sendCodec
	return nil
}

// newFECSender builds a fresh FEC send plane (group encoder + optional adaptive controller)
// from the bind-wide fecCfg/adaptiveCfg, or returns (nil, nil) when FEC is disabled. Like
// newFECReceiver it reads only immutable post-construction config, so it is safe to call
// WITHOUT m.mu — which the lock-free re-instantiation on re-bind (ensurePeerReceiveInstantiated)
// relies on to rebuild a torn-down concentrator peer's send plane symmetric to the receive
// plane. Open calls it too, so a lazily-rebuilt fecSend is byte-identical to an eagerly-opened
// one: a fresh encoder re-pinned to this bring-up, and in adaptive mode a fresh controller
// starting the control law from M=0 (no standing redundancy until loss is observed). A build
// error is a programmer error (the ratio/controller cfg were validated in NewMultipath).
func (m *Multipath) newFECSender() (*fecSender, error) {
	if m.fecCfg == nil {
		return nil, nil
	}
	enc, err := fec.NewEncoder(*m.fecCfg, fec.SystemClock{})
	if err != nil {
		return nil, fmt.Errorf("bind: build FEC encoder: %w", err)
	}
	fs := &fecSender{enc: enc}
	if m.adaptiveCfg != nil {
		// m.clock (telemetry.Clock) satisfies adaptivefec.Clock — both are the identical
		// Now() time.Time shape — so the controller's own slew/dwell timing rides the SAME
		// injectable clock seam T276 threaded through the drive throttle (m.clock), rather
		// than a separately hardcoded adaptivefec.SystemClock{}. Immutable-post-construction
		// (like fecCfg/adaptiveCfg), so reading it here without m.mu is safe; NewMultipath
		// defaults it to telemetry.SystemClock{} (identical Now() = time.Now() as
		// adaptivefec.SystemClock{}), so production behavior is byte-for-byte unchanged.
		ctrl, err := adaptivefec.NewController(*m.adaptiveCfg, m.clock)
		if err != nil {
			return nil, fmt.Errorf("bind: build adaptive FEC controller: %w", err)
		}
		fs.ctrl = ctrl
		// Adopt the controller's starting parity so encoder and controller agree from t=0; the
		// tick loop sizes it to measured loss within the first control interval. Fixed mode
		// leaves the encoder at its cfg.ParityShards default instead.
		enc.SetParity(ctrl.Parity())
	}
	return fs, nil
}

// newFECReceiver builds a fresh FEC receive plane (decoder + residual-loss estimator) from
// the bind-wide fecCfg, or returns (nil, nil) when FEC is disabled. It reads only immutable
// post-construction config (m.fecCfg), so it is safe to call WITHOUT m.mu — which the
// lock-free lazy receive-side instantiation on first authenticated binding
// (ensurePeerReceiveInstantiated) relies on. The decoder tuning matches what Open pins for
// the primary: a retain window, and a recovery deadline past which a doomed group is folded
// into the unrecoverable counter (D24), so a lazily-instantiated concentrator peer recovers
// identically to an eagerly-opened one.
func (m *Multipath) newFECReceiver() (*fecReceiver, error) {
	if m.fecCfg == nil {
		return nil, nil
	}
	dec, err := fec.NewDecoder(*m.fecCfg)
	if err != nil {
		return nil, fmt.Errorf("bind: build FEC decoder: %w", err)
	}
	dec.SetRetainWindow(fecRetainGroups)
	dec.SetClock(fec.SystemClock{})
	dec.SetRecoveryDeadline(m.fecCfg.Deadline + resequencerTimeout)
	return &fecReceiver{dec: dec, connLoss: telemetry.NewConnLoss(fecResidualLossWindow)}, nil
}

// ensurePeerReceiveInstantiated lazily builds a peer's HEAVY receive-side datapath — the
// ~2048-frame resequencer ring and (when FEC is configured) the decoder's per-group buffers —
// on the FIRST authenticated source->peer binding, rather than eagerly at Open (Q26). A
// configured concentrator peer that has never been reached therefore carries none of that
// per-peer memory; it materialises only once an authenticated PROBE has bound a source to it,
// and is reclaimed on teardown (teardownPeerLocked), re-materialising cleanly on the next
// re-bind. It publishes through the same atomic.Pointer the receive fast path Loads and runs
// on the Bind-owned readLoop goroutine, which must never take m.mu (Close waits on the readers
// WHILE holding it) — so it takes ONLY the per-peer lifecycleMu, never m.mu. That lifecycleMu
// makes the whole build-and-publish of the heavy trio (resequencer + FEC receive AND send
// planes) mutually exclusive with teardownPeerLocked's clearing of the same trio: a teardown
// can no longer interleave between the resequencer publish and the FEC publish and leave a
// half-published plane, nor resurrect a plane on a torn-down peer. It is idempotent (the
// lifecycleMu-guarded resequencer==nil double-check elects a single instantiator when two
// sockets bind the same peer concurrently; the loser returns without building). It re-instates
// the SEND plane symmetric to the receive plane, so a torn-down-then-rebound FEC peer sends
// parity again rather than silently sending unprotected. A build error on either FEC plane is a
// programmer error (the ratios were validated in NewMultipath) — the resequencer is still
// installed so DATA flows, only FEC is absent, which is the safe degradation.
func (m *Multipath) ensurePeerReceiveInstantiated(ps *peerState) {
	if ps.resequencer.Load() != nil {
		return // fast path: already instantiated (the eager primary, or a prior binding)
	}
	ps.lifecycleMu.Lock()
	defer ps.lifecycleMu.Unlock()
	if ps.resequencer.Load() != nil {
		return // a concurrent bind instantiated it while we waited on lifecycleMu
	}
	// Build both FEC planes BEFORE publishing anything; on a build error degrade to no-FEC (the
	// resequencer alone still carries DATA). Store the resequencer LAST: it is the election
	// sentinel the fast-path check above and the receive fast path key on, so a concurrent
	// reader never observes a live resequencer whose FEC planes are not yet published.
	fr, ferr := m.newFECReceiver()
	if ferr != nil {
		fr = nil
	}
	fs, serr := m.newFECSender()
	if serr != nil {
		fs = nil
	}
	if fr != nil {
		ps.fecRecv.Store(fr)
	}
	if fs != nil {
		ps.fecSend.Store(fs)
	}
	ps.resequencer.Store(reseq.New(resequencerWindow, resequencerTimeout, reseq.SystemClock{}))
	markMultiPathExpected(ps.resequencer.Load(), ps.scheduler)
	if fr != nil {
		// D93/T241: FEC is repairing this stream (fecRecv stored above), so the fresh
		// resequencer must keep its full hold — no single-path immediate release and
		// no RTT-shortened bound — while FEC is on.
		ps.resequencer.Load().SetFECActive(true)
	}
}

// readLoop is one Bind-OWNED per-path receive goroutine (T30). It reads the path
// socket and dispatches every datagram through handleInbound — which pushes DATA
// into the shared resequencer and answers/consumes PROBEs — then pokes the delivery
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
		// Per-path received-wire accounting (T23): count the OUTER datagram this path
		// pulled off its socket before dispatch, lock-free. This goroutine is the sole
		// writer of ps.rxBytes, so the atomic Add is uncontended.
		ps.rxBytes.Add(uint64(n))
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
// sooner — never a premature/false one — and the failback hysteresis (owned by the
// scheduler) is untouched. No-op when the probe transport is absent (interval unset).
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
	// TryLock, NOT Lock: Close holds m.mu WHILE it waits on readersWG for the readers to
	// exit, so a reader that BLOCKED here on m.mu would deadlock that shutdown. The sweep
	// is opportunistic and throttled, so when the lock is contended (a concurrent
	// Close/AddPath/RemovePath/Send) simply skipping this interval is harmless — the
	// probe-loop ticker and the next receive still advance liveness. This preserves
	// Close's invariant that a reader never blocks on m.mu. The lock, when taken, is held
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
	// Eager failover nudge (defect D18, the repeated-flap wedge). The active-backup
	// selection is otherwise recomputed ONLY inside the scheduler's Pick, which the
	// Bind calls ONLY from Send — so failover is pull-based: it happens only when the
	// engine hands down an egress datagram. When the ACTIVE path dies during an egress
	// LULL that is fatal: a repeated-flap kill landing on the just-restored primary
	// before the saturating flow re-fills it (the failback and the next kill overlap)
	// leaves both TCP directions stalled on the now-dead path, so NO Send occurs, so
	// Pick is never called, so egress is never switched to the healthy backup — the
	// bond wedges on the dead path until the 25s WG keepalive finally drives a Send.
	// The receive tick is the starvation-robust signal that ALREADY detected the DOWN
	// (it just Ticked the path down above); recomputing the scheduler from it makes the
	// switch EAGER — bounded by the detection window (~DownAfter + one interval) rather
	// than by the next application Send. Pick recomputes purely against current liveness
	// and the clock and the failback dwell is time-based, so a more frequent recompute
	// can only make a genuine transition land sooner — never a premature/false failover,
	// and never a shortened anti-thrash hysteresis. It takes ONLY the scheduler's own
	// lock (never m.mu), so it adds no receive-path m.mu contention and cannot invert
	// the Send-path m.mu→scheduler lock order.
	m.nudgeSchedulerActive()
}

// nudgeSchedulerActive forces the scheduler to recompute its active egress path
// against current liveness, independent of any application Send. It is the eager-
// failover companion to the Send-driven Pick: driven from the liveness-detection
// paths (the receive tick and the probe loop) it switches egress to a healthy backup
// the moment the active path is detected DOWN, so a dead active path with stalled
// egress does not wedge the bond until the next Send (defect D18). The returned index
// is intentionally discarded — only the recompute (and its logged transition) is
// wanted here; Send remains the sole reader of the selection for actual routing. Pick
// is internally synchronized and never calls back into the Bind, so this is safe to
// call from a receive goroutine or the probe loop without m.mu.
//
// It calls Recompute, NOT Pick: the nudge wants only the liveness-driven active-set
// recompute (and its logged transition), and a weighted/aggregating scheduler's Pick
// is STATEFUL — a spurious Pick here would consume a distribution slot and skew the
// per-path weight split. Recompute is the non-consuming half that refreshes the
// eligible/active set without touching distribution/pacing/load state, so the T40
// eager-failover guarantee holds for BOTH the active-backup and the weighted policy
// (defect D18).
func (m *Multipath) nudgeSchedulerActive() {
	// Recompute EVERY bound peer's active egress set (T93): each peer schedules over its OWN
	// paths, so a liveness DOWN on one peer's path must nudge THAT peer's scheduler. The peer
	// set is read lock-free through peersView (published under m.mu at construction / peer
	// registration and immutable during an Open span), so this stays off m.mu exactly as the
	// pre-split single Recompute did. On the single-peer edge/hub peersView holds only the
	// primary (p.scheduler == m.scheduler), so this is byte-identical to the pre-split nudge.
	if peers := m.peersView.Load(); peers != nil {
		for _, p := range *peers {
			p.scheduler.Recompute()
		}
		return
	}
	m.scheduler.Recompute()
}

// newReceiveFunc returns the SINGLE engine-facing ReceiveFunc: it drains EACH bound peer's
// resequencer in that peer's own outer-seq order and hands each inner datagram up stamped
// with THAT peer's stable virtual endpoint (per-packet endpoint fill), so the engine
// attributes return traffic to the right peer and Send routes replies back via that peer's
// virt (invariant A1: one virtual endpoint per peer). A path's reader (handleInbound) has
// already routed each DATA frame to its OWNING peer's resequencer via the peerPathState's
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
// (only it calls Pop), which is stricter than T12's per-path receivers.
func (m *Multipath) newReceiveFunc(deliver <-chan struct{}, closed <-chan struct{}) ReceiveFunc {
	// One reusable poll timer per drainer, NOT a fresh time.After timer per park: the
	// drainer parks on every empty receive, and a per-park 250 ms timer — retained by
	// the runtime timer heap until it fires even after the park is over — added
	// avoidable allocation and GC pressure on the receive hot path (opus low defect).
	// The ReceiveFunc is driven by a single engine goroutine, so the timer has no
	// concurrent user; Stop+drain then Reset per park reuses the one allocation.
	timer := time.NewTimer(resequencerTimeout)
	stopTimer := func() {
		timer.Stop()
		select {
		case <-timer.C:
		default:
		}
	}
	stopTimer() // start inert; armed only while parked
	// rr is the round-robin cursor, advanced past a peer each time it yields a frame so
	// the next receive starts at the following peer. It is touched only by this single
	// engine goroutine, so it needs no synchronisation.
	var rr int
	return func(packets [][]byte, sizes []int, eps []Endpoint) (int, error) {
		for {
			// Scan every bound peer round-robin for an in-order resequenced DATA frame.
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
				it, ok := rq.Pop()
				if !ok {
					continue
				}
				rr = (rr + i + 1) % n // start the next scan after this peer (fairness)
				if len(it.Payload) > len(packets[0]) {
					// Oversize inner datagram: drop it, but a frame WAS dequeued this pass,
					// so keep draining (re-scan) rather than parking.
					progressed = true
					break
				}
				sizes[0] = copy(packets[0], it.Payload)
				eps[0] = m.virtualEndpoint(ps, it.Src)
				return 1, nil
			}
			if progressed {
				continue // dropped an oversize frame; re-scan before parking
			}
			timer.Reset(resequencerTimeout)
			select {
			case <-deliver:
				stopTimer()
			case <-timer.C:
				// Poll fired; its channel is already drained by this receive.
			case <-closed:
				stopTimer()
				return 0, errClosed
			}
		}
	}
}

// handleInbound decodes one received outer datagram UNDER THIS VIEW'S CODEC and dispatches
// it by kind to the view's owning peer. It is the single per-frame receive action; the
// readLoop reaches it through demuxInbound, which first resolves the owning view on a shared
// concentrator socket (on the single-peer edge/hub the reader's own view is the owner, so
// demuxInbound's fast path calls this directly — byte-identical to the pre-concentrator
// behaviour). Delivery up the WG
// path is deferred to the resequencer (Pop, in the engine-facing drainer): a DATA
// frame is not handed up here but pushed into the shared resequencer to be released in
// outer-seq order.
//
//   - DATA: the decoded inner datagram is pushed into the resequencer keyed by
//     the frame's outer-seq (delivered later, in order, via Pop). The path's
//     remote is NOT learned here — remote-learning is authenticated-only (see
//     below), so a forged DATA frame cannot steer a path's return endpoint.
//   - PROBE, IsEcho=false: an authenticated peer probe. Its source is learned as
//     the path's remote (D11: a probe-only backup path gets a usable return remote
//     before any DATA flows) and it is reflected straight back to that source via
//     this path's socket (T13 Reflector). Reflection writes independently of the
//     scheduler/getRemote so an echo returns even on a not-yet-active path.
//   - PROBE, IsEcho=true: an authenticated echo of one of our own probes. Its
//     source is learned as the remote too, and the raw echo is fed into this
//     path's Prober (HandleEcho) to update RTT/loss and drive liveness.
//   - anything else (PARITY/CONTROL/malformed): dropped.
//
// Remote-learning and reflection touch only authenticated (MAC-verified) PROBE
// frames — Decode has already verified the tag for the PROBE kind — which is what
// resolves D9: an attacker who forges an unauthenticated DATA frame can no longer
// repoint a path's return endpoint.
//
// FEC seam (T24): DATA ingestion via resequencer.Observe is keyed purely on
// outer-seq, so an FEC decoder can slot in BEFORE the resequencer with no change
// here — PARITY (dropped today) will feed the decoder, which reconstructs missing
// DATA frames and calls Observe with their ORIGINAL outer-seq, identical to a
// natively-received frame.
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
//     identifies the peer, binds the source, dispatches, and the loop STOPS there. A non-PROBE
//     decode does NOT stop the trial: DATA/PARITY carry no MAC and are forgeable by design, so
//     a genuine PROBE from a later peer can cross-psk-garble into a DATA/PARITY kind under an
//     earlier peer's codec (~0.4%); the loop must therefore `continue` past a non-PROBE decode
//     to give every remaining psk its chance to authenticate a PROBE. A non-PROBE decode
//     carries no binding authority and is dropped either way — a genuine DATA/PARITY from a
//     not-yet-bound source never dispatches or binds until that peer's PROBE binds it, so
//     continuing past it changes nothing for genuine frames. A forged/garbage frame verifies
//     as a PROBE under NO psk, binds nothing, and is dropped cheaply.
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
			// Decoded under this psk but not a PROBE (DATA/PARITY carry no MAC, so
			// this may be a genuine PROBE from a later peer that cross-garbled into an
			// unauthenticated kind here). No binding authority: try the remaining psks
			// rather than aborting — a genuine unbound DATA/PARITY still never binds.
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
		// (resequencer ring + FEC decoder buffers) BEFORE dispatch, so a configured peer that
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
// on m.mu (Close waits on the readers WHILE holding m.mu), so the binding — written from a
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
// State() (atomic internally, per the PathHealth contract), so it is safe under m.mu. A peer
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

// teardownPeerLocked frees a dead peer's HEAVY per-peer state — the ~2048-frame resequencer
// ring and the FEC send/receive buffers — and releases its source->peer demux bindings,
// reclaiming both the memory and the demux-map cap slots (Q26). It is the lifecycle dual of
// ensurePeerReceiveInstantiated: after teardown the peer is dormant (its light state — psk,
// codec, reflector, per-(peer,path) views — survives so a trial-decode still authenticates it),
// and the next authenticated PROBE re-binds a source and re-instantiates the ring cleanly. It
// REFUSES to tear down a LIVE peer (any path StateUp) and the embedded primary (the edge/hub,
// whose lifecycle is Open/Close, not session teardown), returning false in both cases so a
// caller can distinguish "torn down" from "kept". The heavy fields are atomic.Pointer, so
// Store(nil) is safe against a concurrent readLoop (which nil-guards its Load); the drainer
// likewise skips a peer whose resequencer Loads nil. Caller holds m.mu.
func (m *Multipath) teardownPeerLocked(p *peerState) bool {
	if p == m.peerState {
		return false // the primary (edge/hub) is torn down only by Close, never by session loss
	}
	if m.peerIsLiveLocked(p) {
		return false // a live (Up) peer is never torn down, whatever other peers' churn
	}
	// Clear the heavy trio under lifecycleMu so a concurrent ensurePeerReceiveInstantiated on a
	// readLoop (which also holds lifecycleMu across its whole build-and-publish) cannot interleave:
	// it either runs wholly before this clear (and is then wholly undone here) or wholly after
	// (and rebuilds all three cleanly). This closes the resurrection/half-published hole where a
	// teardown landing mid-instantiation left a fecRecv/fecSend without its resequencer, which the
	// next re-bind then reused stale. lifecycleMu is a leaf lock (see its field doc); we hold m.mu
	// here, giving the fixed order m.mu -> lifecycleMu with no cycle. The unbind runs after the
	// clear (and outside lifecycleMu): it is its own lock-free CAS loop and needs no ordering here.
	p.lifecycleMu.Lock()
	p.resequencer.Store(nil)
	p.fecRecv.Store(nil)
	p.fecSend.Store(nil)
	p.lifecycleMu.Unlock()
	m.unbindPeerSources(p)
	return true
}

// TearDownPeer frees the heavy per-peer state of the named configured peer once its WireGuard
// session / liveness is gone — the device wires this from its per-peer session events (Q26). It
// is a no-op returning false when the peer is unknown, is the embedded primary, or is still
// LIVE (a live peer is never torn down); it returns true when the peer's resequencer ring and
// FEC buffers were freed and its source bindings released. A torn-down configured peer
// re-instantiates cleanly on its next authenticated PROBE (ensurePeerReceiveInstantiated).
func (m *Multipath) TearDownPeer(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.peersByName[name]
	if !ok {
		return false
	}
	return m.teardownPeerLocked(p)
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

// dispatchInbound handles one already-decoded inbound frame on the resolved peer's view (ps):
// it routes to that peer's resequencer / FEC decoder / reflector. The source demux in
// demuxInbound has already selected ps so a shared socket serving many peers resequences each
// peer's stream against that peer's own buffer; on the single-peer edge/hub ps.peer is the
// embedded primary, so this is byte-identical to the pre-split singleton. raw is retained for
// the probe transport (HandleEcho / Reflect re-decode it under the peer's psk).
func (m *Multipath) dispatchInbound(ps *peerPathState, fr frame.Frame, raw []byte, srcAP netip.AddrPort) {
	pr := ps.peer
	switch f := fr.(type) {
	case frame.Data:
		// The edge's uplink DATA rides only its ACTIVE WAN, so an address-match-gated
		// DATA sample selects (never establishes) the downlink destination among the
		// probe-established entries (T246, defect D94).
		ps.confirmDataRemote(f.PathID, srcAP)
		// Decode already returned a fresh copy of the payload (it aliases nothing
		// else), so the resequencer may take ownership of it directly.
		rq := pr.resequencer.Load()
		if rq == nil {
			// The peer's heavy receive state is absent — not yet instantiated, or torn down on
			// session/liveness loss while a DATA frame was in flight on this readLoop. Drop it;
			// a fresh authenticated PROBE re-instantiates the ring before the next DATA lands.
			return
		}
		if fr := pr.fecRecv.Load(); fr != nil {
			// FEC on (T24): offer the data shard to the decoder BEFORE resequencing so a
			// later parity frame can reconstruct any group-mate lost in transit, then
			// deliver THIS received frame in its own right (the decoder never echoes a
			// directly-received data shard back). The shard's coded bytes are
			// OuterSeq || Payload — the same bytes the sender coded parity over.
			shard := fec.DataShard{Group: fec.GroupID(f.FECGroup), Index: int(f.FECIndex), Payload: fecShardPayload(f.OuterSeq, f.Payload)}
			recovered, _ := fr.offer(shard)
			rq.ObserveFromPath(f.OuterSeq, f.Payload, srcAP, reseqPathKey(ps.id, f.PathID))
			// Residual-loss accounting (T29): this outer-seq was natively delivered, so mark
			// it present in the post-recovery loss estimator. A seq never marked here nor via
			// a reconstruction below is loss that FEC did not mask.
			if fr.connLoss != nil {
				fr.connLoss.Observe(f.OuterSeq)
			}
			m.observeRecovered(fr, rq, recovered, srcAP)
		} else {
			rq.ObserveFromPath(f.OuterSeq, f.Payload, srcAP, reseqPathKey(ps.id, f.PathID))
		}
	case frame.Parity:
		// PARITY feeds the FEC decoder (T24); a group that has now accumulated enough
		// shards reconstructs its missing data frames, each resequenced at its ORIGINAL
		// outer-seq (carried in the recovered shard's coded bytes) so recovery composes
		// with T18 exactly like a natively-received frame. With FEC off, PARITY is
		// dropped (the pre-T24 behaviour).
		if fr := pr.fecRecv.Load(); fr != nil {
			rq := pr.resequencer.Load()
			if rq == nil {
				return // heavy receive state torn down mid-flight (see the DATA case)
			}
			shard := fec.ParityShard{Group: fec.GroupID(f.FECGroup), Index: int(f.ParityIndex), DataCount: int(f.DataCount), Payload: f.Payload}
			recovered, _ := fr.offer(shard)
			m.observeRecovered(fr, rq, recovered, srcAP)
		}
	case frame.Probe:
		// Authenticated (the PROBE MAC verified in Decode): fold the frame into the
		// per-sender-path freshness table under its stamped path id (T246, defect D94) —
		// establishing/refreshing the return address for THAT sender path, below the
		// engine's virtual endpoint. Unlike the pre-D94 unconditional overwrite, this
		// never moves the SELECTED downlink destination (except the R253 cold-start
		// first-establishment and an in-place rebind of the selected entry), so the
		// standby WAN's probes no longer flap the concentrator's downlink at cadence.
		ps.learnRemoteFromProbe(f.PathID, srcAP)
		if f.IsEcho {
			if ps.prober != nil {
				// A replay/forgery/wrong-path echo is rejected inside HandleEcho and
				// leaves liveness untouched; the error is a per-frame drop, not fatal.
				// ps.prober is the path's OWN immutable prober — never a lookup into a
				// dynamically-mutated slice — so runtime add/remove cannot race this.
				_ = ps.prober.HandleEcho(raw)
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
		if pr.reflector != nil {
			if echo, epochChanged, rerr := pr.reflector.Reflect(raw); rerr == nil {
				// UDP writes are goroutine-safe, so this receive-goroutine reflection
				// races no in-flight Send on the same socket.
				if _, werr := ps.conn.WriteToUDPAddrPort(echo, srcAP); werr == nil {
					// True-wire-volume accounting (D48): the echo we just sent back is
					// real egress traffic on this path, so it counts toward txBytes
					// exactly like a DATA/PARITY write — only on a nil write error.
					ps.txBytes.Add(uint64(len(echo)))
					// Exempt-but-charged probe accounting (T145): a reflected echo is
					// out-of-band egress on this path (it never traverses Send->Pick), so
					// SYMMETRICALLY with emitProbes it is never shed/delayed but IS charged
					// against the path's token bucket, so paced ClassData yields the headroom
					// the reflected-echo stream consumes. ps.schedIdx addresses this path's
					// bucket; AccountProbe locks only the scheduler's own mutex (never m.mu,
					// which this receive goroutine must not take) and bounds-checks the index.
					if budget, ok := pr.scheduler.(sched.ProbeBudget); ok {
						budget.AccountProbe(int(ps.schedIdx.Load()))
					}
				}
				if epochChanged {
					// Authenticated PEER RESTART (T116/T119): the reflector reports THIS
					// peer's epoch changed — a new-sessionID adoption over an already-adopted
					// path. The restarted peer's outer-seq resets near 1, far below the
					// release point the pre-restart stream advanced THIS peer's resequencer's
					// `next` to, so its wrapped WG init (outer-seq ~1) would be SUSPECT-dropped
					// and the tunnel never re-establishes. Re-anchor via a LOW-ANCHOR
					// re-baseline so the restarted low-seq init re-pins the release point while
					// a stale HIGH-seq old-boot straggler still draining cannot re-pin it high
					// (defect D36 re-pin race). Because pr is the demux-resolved per-peer view,
					// this ONE site covers the edge single-concentrator primary AND every
					// concentrator per-peer resequencer, both restart directions. Load the
					// resequencer atomically and nil-check it — absent mid-teardown, like the
					// DATA branch; a torn-down peer re-instantiates an UNSTARTED ring that needs
					// no re-anchor. Done OUTSIDE m.mu with no other lock held (dispatchInbound
					// runs on a readLoop that never takes m.mu; the resequencer keeps its own
					// mutex — never nest it), mirroring the SetPeerRemote->Rebaseline discipline.
					if rq := pr.resequencer.Load(); rq != nil {
						rq.RebaselineToLow()
					}
				}
			}
		}
	default:
		// CONTROL (and any unhandled kind) is not delivered up this path.
	}
}

// observeRecovered feeds every FEC-reconstructed data frame into the resequencer at
// its ORIGINAL outer-seq (recovered from the shard's coded bytes) via the NON-resyncing
// ObserveRecovered path, so a frame rebuilt from parity resequences identically to a
// natively-received frame — filling the exact gap the resequencer would otherwise time
// out on — WITHOUT a late batch of recovered seqs being able to move the release point
// or dump the live buffer (recovery must never cause loss). Only frames actually placed
// AHEAD of the release point advance the honest delivered-recovery counter; a frame
// rebuilt after the resequencer already skipped its gap is dropped as late and not
// counted, so /metrics reflects delivered recovery, not mere reconstruction. A
// malformed recovered shard (too short to hold the outer-seq prefix) is dropped: it
// signals an encoder/decoder mismatch, not a deliverable frame.
func (m *Multipath) observeRecovered(fr *fecReceiver, rq *reseq.Resequencer, recovered []fec.Recovered, srcAP netip.AddrPort) {
	for _, rec := range recovered {
		seq, inner, err := splitFECShardPayload(rec.Payload)
		if err != nil {
			continue
		}
		// Residual-loss accounting (T29): FEC reconstructed this outer-seq, so it is NOT
		// residual loss — mark it present in the post-recovery estimator even if the
		// resequencer later drops it as late (that is a latency outcome, not a masking
		// failure; the P4 residual bound measures loss FEC failed to mask).
		if fr.connLoss != nil {
			fr.connLoss.Observe(seq)
		}
		if rq.ObserveRecovered(seq, inner, srcAP) {
			fr.deliveredRecovered.Add(1)
		}
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

// Send wraps each buffer in an outer DATA frame (fresh outer-seq + the chosen
// path's id) and writes it to that path's remote. The egress path is chosen by
// the injected scheduler (T15): the active-backup policy returns the preferred
// primary while it is up and the failover backup otherwise. The scheduler
// selects by path priority/liveness only; the Bind additionally requires THAT
// ONE chosen path to have a known remote, failing the send otherwise rather
// than silently dropping.
//
// Behavioural change from pre-T15: this is a deliberate NARROWING. The removed
// pickPathLocked iterated the paths and skipped any healthy-but-remoteless one,
// falling through to the next healthy path WITH a known remote, so a send failed
// only when NO path had a remote. Now the scheduler owns selection and returns a
// single index; the Bind does NOT fall through, because a Bind-level fall-through
// would bypass the scheduler's hysteresis (failover/failback). The residual
// window this opens: if the scheduler's chosen path is reported Up but its remote
// is not yet learned — e.g. a concentrator/hub restart, or a T16 NAT-rebind
// before the first inbound packet re-teaches the remote — the send fails until
// that path's remote is learned, even if another path has a known remote.
//
// Critical-section discipline: path selection and framing run under m.mu (the
// send Codec is shared and stateful — D5's single keystream requires sequential,
// mutex-guarded Encode), but each datagram is encoded into its OWN fresh buffer
// so it outlives the lock, and the WriteToUDPAddrPort syscalls run WITHOUT m.mu
// held. The scheduler is internally synchronized and never calls back into the
// Bind, so calling Pick under m.mu cannot deadlock and adds no receive-path
// contention (the lock-free virtual-endpoint fast path is untouched). A receive
// goroutine pinning the virtual endpoint therefore never blocks behind an
// in-flight transmit syscall.
func (m *Multipath) Send(bufs [][]byte, ep Endpoint) error {
	ue, ok := ep.(*udpEndpoint)
	if !ok {
		return conn.ErrWrongEndpointType
	}

	// Classify the batch by inner WireGuard message type (defect D22): a handshake or
	// keepalive is passed to the scheduler as ClassControl so a pacing scheduler exempts
	// it from the per-path data token buckets and bulk overload cannot shed it and starve
	// rekey. The classifier is parameterized by the tunnel's Amnezia profile, so it works
	// under advanced-security obfuscation (custom magic headers + handshake junk), not
	// only vanilla WireGuard. It reads only the (possibly junk-shifted) type word off the
	// lock. It is peer-agnostic (inner WireGuard type, not routing), so it runs before the
	// peer resolution below.
	class := m.classify.classifyBatch(bufs)

	m.mu.Lock()
	// Resolve the OWNING peer from the engine-facing virtual endpoint: each peer holds a
	// DISTINCT virt, so this datagram egresses on THAT peer's outerSeq, scheduler,
	// sendCodec, fecSend, and per-(peer,path) set — never the embedded primary's by
	// promotion. An endpoint of the right type but unknown to the demux (no bound peer)
	// cannot be routed, so it returns the no-path error rather than misrouting onto some
	// other peer's paths. On the single-peer edge/hub the primary's virt resolves to the
	// primary peerState, so behaviour is byte-identical to the pre-split singleton.
	peer, ok := m.peerByVirt[ue]
	if !ok {
		m.mu.Unlock()
		return ErrNoHealthyPath
	}
	if len(peer.paths) == 0 {
		m.mu.Unlock()
		return errClosed
	}
	// OFFERED WIRE FRAMES FOR THIS ONE SELECTION DECISION (defect D95, decisions:K35
	// §3a/§3c). Pick runs ONCE per batch — it must, because the path it selects is
	// stamped into every frame this batch produces (PathID at the Encode below, and into
	// each parity shard) — so it is told how many wire frames that decision covers:
	//
	//   len(bufs)                  the batch's own DATA frames, one per buffer; before
	//                              D95 the scheduler was told "1" here regardless, which
	//                              made its offered-load meter count Send BATCHES/s while
	//                              PerPathCapacity denominates WIRE FRAMES/s;
	//   peer.parityCarry.Swap(0)   the FEC parity frames THIS peer actually wrote to a
	//                              socket since its last Pick. Parity egresses on the
	//                              same chosen path and consumes the same wire capacity,
	//                              so excluding it would put a demand numerator over a
	//                              wire denominator (at 4+2 a ~3400 fps path would meter
	//                              only ~2267 fps, below engage 2700 — D95's failure mode
	//                              restored for every FEC-enabled deployment).
	//
	// WHY THE CARRY RATHER THAN AN EXACT AT-PICK COUNT: the batch's parity count is not
	// known until the encoder's Admit calls run INSIDE the loop below, and under adaptive
	// FEC the per-group parity M itself varies at runtime, so an exact at-selection count
	// would require inverting select-then-encode. The carry is exact IN AGGREGATE at O(1)
	// per batch and lags by at most ONE batch — sub-millisecond at any rate where the gate
	// can matter, against LoadTau = 200 ms (K35 §9.4).
	//
	// EMPTY BATCH: with no buffers AND no pending carry there is nothing to offer, so
	// Send returns without calling Pick. That also removes a pre-existing spurious
	// offered event (an empty Send used to meter one frame). A batch that is empty but
	// has parity pending DOES pick, so no parity is silently lost.
	frames := len(bufs) + int(peer.parityCarry.Swap(0))
	if frames == 0 {
		m.mu.Unlock()
		return nil
	}
	idx := peer.scheduler.Pick(class, frames)
	if idx == sched.PickPaced {
		// The scheduler shed this datagram for pacing while paths are healthy: drop it
		// (same as no-path), but surface a DISTINCT error so the diagnostic is not
		// conflated with a total outage. The rate-limited log is emitted at the source
		// (the scheduler), so no per-drop logging happens here on the hot path.
		m.mu.Unlock()
		return errPacerShedding
	}
	if idx < 0 || idx >= len(peer.paths) {
		m.mu.Unlock()
		return ErrNoHealthyPath
	}
	ps := peer.paths[idx]
	remote, ok := ps.getRemote()
	if !ok {
		m.mu.Unlock()
		return ErrNoHealthyPath
	}
	c := ps.conn
	// Snapshot this peer's send-FEC plane once under m.mu: a torn-down peer reads nil (sends
	// unprotected until re-bind rebuilds it), a re-bound peer reads its freshly re-instantiated
	// sender. The encoder is mutated (Admit) only here under m.mu, so this single Load pins a
	// stable sender for the whole batch.
	sendFEC := peer.fecSend.Load()
	wires := make([]fecWire, 0, len(bufs))
	for _, b := range bufs {
		seq := peer.outerSeq.Add(1)
		if sendFEC != nil {
			// FEC on (T24): admit the inner datagram (coded as seq || inner) to the group
			// encoder. The returned data shard rides a normal DATA frame carrying its FEC
			// group + shard index; when this admission FILLS the group the encoder returns
			// the group's parity shards, emitted here as KindParity frames on the SAME
			// chosen path. Spreading parity onto a DIFFERENT path than its data (so one
			// path outage cannot lose both) is a documented future refinement, deliberately
			// NOT implemented here (see the T24 design notes).
			ds, parity, err := sendFEC.enc.Admit(fecShardPayload(seq, b))
			if err != nil {
				m.mu.Unlock()
				return err
			}
			wire, err := peer.sendCodec.Encode(nil, frame.Data{OuterSeq: seq, PathID: ps.id, FECGroup: uint32(ds.Group), FECIndex: uint8(ds.Index), Payload: b})
			if err != nil {
				m.mu.Unlock()
				return err
			}
			wires = append(wires, fecWire{b: wire})
			for _, par := range parity {
				pw, err := m.encodeParityLocked(peer, par, ps.id)
				if err != nil {
					m.mu.Unlock()
					return err
				}
				wires = append(wires, fecWire{b: pw, parity: true})
			}
			continue
		}
		wire, err := peer.sendCodec.Encode(nil, frame.Data{OuterSeq: seq, PathID: ps.id, Payload: b})
		if err != nil {
			m.mu.Unlock()
			return err
		}
		wires = append(wires, fecWire{b: wire})
	}
	fs := sendFEC
	m.mu.Unlock()

	for _, w := range wires {
		if _, err := c.WriteToUDPAddrPort(w.b, remote); err != nil {
			return m.accountSendError(ps, err)
		}
		// Per-path egress-wire accounting (T23): count the OUTER frame bytes just written
		// to this path (DATA and any FEC PARITY alike), lock-free and only for a datagram
		// that actually reached the socket. Send serialized the path choice under m.mu, so
		// ps is fixed here and this is the sole writer of ps.txBytes for this frame.
		ps.txBytes.Add(uint64(len(w.b)))
		if fs != nil {
			// FEC frame accounting (T24/T25), counted only once the frame reached the socket
			// so the /metrics overhead ratio reflects wire cost actually spent. Parity and
			// DATA are charged to disjoint counters; their ratio ParityFrames/DataFrames is
			// the fixed-ratio overhead the P3 e2e asserts against M/K. fs != nil only when FEC
			// is enabled, so a plain (FEC-off) datapath increments neither counter.
			if w.parity {
				fs.parityFrames.Add(1)
				fs.parityBytes.Add(uint64(len(w.b)))
				// OFFERED-LOAD CARRY (defect D95, K35 §3c): this parity frame has just
				// consumed one wire frame of the chosen path's capacity, so it is owed to
				// the scheduler's offered-load meter. It is counted HERE — where w.parity
				// is already discriminated and the frame has provably reached the socket —
				// so it is counted exactly once and never for a frame that failed framing
				// or was never written. The next Send for this peer consumes the carry.
				peer.parityCarry.Add(1)
			} else {
				fs.dataFrames.Add(1)
				// DATA-frame wire bytes: the overhead-BYTES denominator (T29). The P4
				// acceptance compares parity BYTES / data BYTES, so both are counted on the
				// same only-once-it-reached-the-socket basis as the frame counters.
				fs.dataBytes.Add(uint64(len(w.b)))
			}
		}
	}
	return nil
}

// fecWire is one framed outgoing datagram tagged with whether it is an FEC parity
// frame, so the write loop can charge parity-overhead accounting (T24) without a
// second pass.
type fecWire struct {
	b      []byte
	parity bool
}

// emsgsizeWarnInterval bounds how often a path's EMSGSIZE (over-PMTU send, DF set)
// diagnostic is emitted, so a persistent over-MTU flow — whose drop is per-datagram —
// logs a coalesced record per path rather than one line per dropped datagram. The
// counter (emsgsizeDrops) still advances on every occurrence.
const emsgsizeWarnInterval = 1 * time.Second

// accountSendError classifies a path write failure from the Send hot path. An EMSGSIZE
// is the explicit "datagram exceeds the path MTU with DF set" that the T201 DF policy
// (setDontFragment) surfaces in place of the kernel's old silent fragmentation: it is
// COUNTED per (peer,path) and WARNed (rate-limited per path), then still RETURNED so the
// engine observes the drop — the fail-fast invariant means the loss is surfaced, never
// swallowed. Any other write error is returned unchanged (no counter, no log — those are
// genuine send failures the caller already handles). It runs WITHOUT m.mu, off the Send
// write loop, so it touches only the path's own lock-free atomics and the (safe-for-
// concurrent-use) logger.
func (m *Multipath) accountSendError(ps *peerPathState, err error) error {
	if errors.Is(err, syscall.EMSGSIZE) {
		ps.emsgsizeDrops.Add(1)
		m.warnEMSGSIZE(ps)
	}
	return err
}

// warnEMSGSIZE emits the coalesced per-path over-PMTU WARN at most once per
// emsgsizeWarnInterval. It gates on ps.lastEMSGSIZEWarnNanos with a load-then-CAS so
// concurrent Send goroutines on the same path collapse to a single log line per window
// (a lost CAS means another goroutine already claimed this window). The reported drop
// count is the running total, so a reader sees the cumulative loss even across coalesced
// windows.
func (m *Multipath) warnEMSGSIZE(ps *peerPathState) {
	now := time.Now().UnixNano()
	last := ps.lastEMSGSIZEWarnNanos.Load()
	if last != 0 && now-last < int64(emsgsizeWarnInterval) {
		return
	}
	if !ps.lastEMSGSIZEWarnNanos.CompareAndSwap(last, now) {
		return
	}
	m.log.Warn("bind: path send exceeded PMTU with DF set, datagram dropped (EMSGSIZE)",
		"path", ps.name,
		"peer", ps.peer.name,
		"emsgsize_drops", ps.emsgsizeDrops.Load(),
	)
}

// encodeParityLocked encodes one parity shard as a KindParity frame on the given
// path, using the owning peer's send Codec. Caller holds m.mu (the send Codec is
// shared and stateful per peer).
func (m *Multipath) encodeParityLocked(peer *peerState, par fec.ParityShard, pathID uint8) ([]byte, error) {
	return peer.sendCodec.Encode(nil, frame.Parity{
		FECGroup:    uint32(par.Group),
		ParityIndex: uint16(par.Index),
		DataCount:   uint8(par.DataCount),
		PathID:      pathID,
		Payload:     par.Payload,
	})
}

// fecTickLoop drives the FEC encoder's grouping deadline (T24): a partially-filled
// group whose size threshold has not been reached is closed on time so its parity is
// emitted rather than stranded until the next data frame. It ticks at the configured
// deadline period and exits on recvClosed. It is tracked by readersWG so Close waits
// for it. Like tickLivenessFromReceive it only ever TryLocks m.mu, so it can never
// deadlock Close's readersWG.Wait (which runs while Close holds m.mu).
func (m *Multipath) fecTickLoop(period time.Duration, closed <-chan struct{}) {
	defer m.readersWG.Done()
	ticker := time.NewTicker(period)
	defer ticker.Stop()
	for {
		select {
		case <-closed:
			return
		case <-ticker.C:
			m.fecFlushDeadline()
		}
	}
}

// fecFlushPeerWrite is one peer's accumulated deadline-flush parity, framed under m.mu and
// egressed after it is released (see fecFlushDeadline). Each peer contributes at most one of
// these per tick, so a torn-down/never-instantiated peer or a peer with nothing to flush
// simply contributes none — it never disturbs any other peer's flush.
type fecFlushPeerWrite struct {
	conn   *net.UDPConn
	remote netip.AddrPort
	ps     *peerPathState
	fs     *fecSender
	wires  [][]byte
}

// fecFlushDeadline closes any FEC group whose grouping deadline has elapsed, for EVERY bound
// peer, and emits each peer's parity on that peer's OWN scheduler-chosen path (D44 — a
// concentrator peer's straggler parity must flush on the deadline exactly like the primary's,
// not only on the next size-triggered close). It TryLocks m.mu: when the lock is contended (a
// concurrent Send/Close/AddPath) it simply skips this tick — the next tick, or the next
// size-triggered close, still emits the group's parity, and skipping preserves Close's
// invariant that no readersWG goroutine blocks on m.mu. Framing (and the adaptive drive) run
// under the lock (each peer's send Codec/controller/encoder are shared, stateful, per-peer
// state); the socket writes run without it, mirroring Send. A peer whose fecSend Loads nil —
// torn down, or never lazily instantiated (ensurePeerReceiveInstantiated) — is nil-skipped
// without affecting any other peer's flush; likewise a per-peer framing/Pick/write failure
// only drops THAT peer's group for this tick, exactly as it did (whole-flush) before the
// multi-peer fan-out.
func (m *Multipath) fecFlushDeadline() {
	if !m.mu.TryLock() {
		return
	}
	var writes []fecFlushPeerWrite
	for _, peer := range m.peers {
		fs := peer.fecSend.Load()
		if fs == nil || len(peer.paths) == 0 {
			continue
		}
		// Adaptive drive (T29): fold a fresh loss sample into THIS peer's controller and
		// retarget THIS peer's encoder's per-group parity BEFORE flushing, so a group closed
		// by this tick already carries the current target. It is a no-op in fixed mode and
		// self-throttles to the probe cadence. Runs under the same m.mu the flush holds — the
		// single serialized FEC locus — so the controller (a state machine) is never touched
		// concurrently.
		m.driveAdaptiveControllerLocked(peer)
		parity, err := fs.enc.Tick()
		if err != nil || len(parity) == 0 {
			continue
		}
		// Route the parity like data through THIS peer's scheduler (a first-integration
		// choice; see Send). A shed/no-path verdict drops this group's parity — a degraded
		// path is exactly when the datapath is under pressure, and a stranded partial group's
		// parity is best-effort — so the group simply goes unprotected rather than blocking.
		// FEC parity is bulk redundancy, so it is paced as ClassData (defect D22): only
		// WireGuard control frames earn the pacing exemption.
		// ONE selection decision for this peer's flush, offering ONE frame — the
		// deliberately unchanged pre-D95 arity at this call site (K35 §3c). The parity
		// this flush actually writes is metered through the SAME carry Send uses (see
		// the write loop below), so it reaches the offered-load meter on the peer's next
		// Send rather than being counted speculatively here, before framing and the
		// socket writes have had a chance to fail.
		idx := peer.scheduler.Pick(sched.ClassData, 1)
		if idx < 0 || idx >= len(peer.paths) {
			continue
		}
		ps := peer.paths[idx]
		remote, ok := ps.getRemote()
		if !ok {
			continue
		}
		wires := make([][]byte, 0, len(parity))
		framingFailed := false
		for _, par := range parity {
			// Frame with THIS peer's own psk-derived send Codec (encodeParityLocked), so each
			// peer's parity is decodable only under its own peer's codec — never the primary's
			// by promotion.
			pw, err := m.encodeParityLocked(peer, par, ps.id)
			if err != nil {
				framingFailed = true
				break
			}
			wires = append(wires, pw)
		}
		if framingFailed {
			continue
		}
		writes = append(writes, fecFlushPeerWrite{conn: ps.conn, remote: remote, ps: ps, fs: fs, wires: wires})
	}
	m.mu.Unlock()

	for _, w := range writes {
		for _, wire := range w.wires {
			if _, err := w.conn.WriteToUDPAddrPort(wire, w.remote); err != nil {
				break // this peer's remaining shards for this tick are dropped; other peers are unaffected
			}
			w.ps.txBytes.Add(uint64(len(wire)))
			w.fs.parityFrames.Add(1)
			w.fs.parityBytes.Add(uint64(len(wire)))
			// Deadline-closed parity consumes the path's wire capacity exactly as a
			// size-closed group's does, so it is owed to the offered-load meter through
			// the same one-batch-late carry Send uses (defect D95, K35 §3c/§9.4). The
			// owning peer is reached through the path's own back-reference, so a
			// multi-peer flush credits each peer's carry to that peer alone.
			w.ps.peer.parityCarry.Add(1)
		}
	}
}

// driveAdaptiveControllerLocked folds one measured loss sample into peer's adaptive FEC
// controller and retargets peer's encoder's per-group parity (T29). Caller holds m.mu, so
// the controller — a state machine that is NOT safe for concurrent use — is driven from
// this single serialized locus (the FEC tick loop, per peer), exactly as the encoder is. It
// is a no-op in fixed-ratio mode (no controller) and self-throttles to
// adaptiveControlInterval so the controller's EWMA sees ~one sample per probe interval
// regardless of the tick rate. peer is any bound peer (the embedded primary or a
// concentrator peer) — the drive is entirely peer-scoped so one peer's control loop never
// reads or perturbs another peer's controller/encoder/paths.
//
// WHICH LOSS drives the controller (design decision 1, revised D96 mechanisms 2+3): the loss
// on the path(s) that ACTUALLY CARRY DATA, consulted through the scheduler's DataPaths seam
// (T271) rather than a role-agnostic MAX over every StateUp prober. Parity must mask the loss
// the DATA actually experiences: under active-backup only the active path carries data (so the
// signal is that one path's raw probe loss), and under the weighted scheduler data is striped
// across the aggregating set (so the signal is the WEIGHT-WEIGHTED MIX of those paths' losses,
// per each path's distribution share). A role-agnostic MAX let a lossy but data-idle STANDBY
// drive M up even though it carried no data to protect — the D96 defect this replaces. A
// MIN-SAMPLE FLOOR (minAdaptiveLossSamples) additionally excludes any data path still in its
// early loss-window regime, where a single dropped probe reads as a large fraction against a
// tiny denominator (D96 mechanism 3); when the weighted mix's eligible subset is a strict
// subset of the data paths the mix is RENORMALIZED over that subset, and when NO data path is
// sample-eligible the drive takes the count==0 HOLD branch (below). It is deliberately the RAW
// per-path loss, NOT the post-recovery ConnLoss: feeding the masked residual back would form a
// control loop that under-provisions precisely because it is succeeding. A down/probeless path
// carries no data and is never in DataPaths, so it is excluded by construction.
func (m *Multipath) driveAdaptiveControllerLocked(peer *peerState) {
	fs := peer.fecSend.Load()
	if fs == nil || fs.ctrl == nil {
		return // FEC off for this peer, or fixed-ratio mode
	}
	now := m.clock.Now()
	if fs.haveControlTick && now.Sub(fs.lastControlTick) < adaptiveControlInterval {
		return
	}

	// Refresh the scheduler's liveness-derived selection before reading its data-path set: this
	// drive runs on the FEC flush timer, decoupled from the send-path Pick that otherwise warms
	// the cached active/eligible set, so without this the data-path signal could lag liveness on
	// an idle-but-lossy peer. Recompute is non-consuming (advances no distribution/pacing/load
	// state), takes only the scheduler lock, and never calls back into the Bind — the same
	// m.mu->scheduler order the eager-failover nudge already relies on — so it composes with the
	// DataPaths read below (which the T271 seam documents as NOT itself refreshing liveness).
	peer.scheduler.Recompute()

	loss, count := dataPathLossLocked(peer)
	if count == 0 {
		// No sample-eligible DATA path this interval — either the scheduler reports no path
		// carrying data (all down/collapsed) or every data path is still below the min-sample
		// floor (early regime). HOLD the current parity/smoothed-loss target (the controller
		// does not Observe), but still publish the eligible signal (count 0) — it is the only
		// way an operator sees this hold branch — WITHOUT clobbering the held decision. The
		// throttle stamp deliberately stays UNtouched on this branch (it moved below, past the
		// eligibility check): the hold does not Observe, so it must not consume the interval —
		// the drive stays admitted every tick until a data path becomes sample-eligible, at the
		// minor cost of a per-tick dataPathLossLocked scan (D97). Because the stamp lives ONLY on
		// the Observe branch (G31/T276), a floor-induced early-regime HOLD leaves the throttle
		// UNSTAMPED, so selection work runs each tick during the early regime — accepted
		// consciously (D96). publishAdaptiveEligible is idempotent/non-clobbering, so the
		// repeated hold-branch publishes are safe.
		fs.publishAdaptiveEligible(loss, count)
		return
	}
	// Stamp the throttle only now that the controller will actually Observe — the interval
	// is consumed by a real sample, never by a hold (D97).
	fs.lastControlTick = now
	fs.haveControlTick = true
	fs.ctrl.Observe(loss)
	fs.enc.SetParity(fs.ctrl.Parity())
	fs.publishAdaptiveDrive(fs.ctrl.Parity(), fs.ctrl.SmoothedLoss(), loss, count)
}

// dataPathLossLocked computes the adaptive controller's loss input from the path(s) that
// ACTUALLY CARRY DATA (D96 mechanisms 2+3), consulting the scheduler's DataPaths seam (T271)
// instead of a role-agnostic MAX over every StateUp prober. It returns the weight-weighted mix
// of the data paths' raw probe-measured loss and the COUNT of sample-eligible data paths (0 —
// the caller's HOLD condition — when the scheduler reports no data path, or every reported data
// path is still below the min-sample floor). Caller holds m.mu.
//
// It calls peer.scheduler.DataPaths() (which takes the scheduler's own lock and returns a
// caller-owned copy, never calling back into the Bind — the documented m.mu->scheduler order)
// and then reads each named data path's prober (Estimate) — the prober is a LEAF lock that
// never calls back into the Bind, so the whole read respects the m.mu->scheduler->prober order
// the rest of the Bind takes. DataPath.Index is the priority-ordered path index, which by the
// schedIdx invariant (attachPeerPathLocked) equals the position in peer.paths.
func dataPathLossLocked(peer *peerState) (float64, int) {
	dps := peer.scheduler.DataPaths()
	return weightedDataPathLoss(dps, func(idx int) (telemetry.Estimate, bool) {
		if idx < 0 || idx >= len(peer.paths) {
			return telemetry.Estimate{}, false // stale index during a concurrent membership change
		}
		pr := peer.paths[idx].prober
		if pr == nil {
			return telemetry.Estimate{}, false // no probe transport: no per-path loss to read
		}
		return pr.Estimate(), true
	})
}

// weightedDataPathLoss folds the per-data-path loss estimates into one controller input: the
// weight-weighted mix of the RAW loss over the SAMPLE-ELIGIBLE data paths, renormalized over
// that eligible subset. estimate maps a DataPath.Index to that path's telemetry.Estimate (ok
// false when the index is unreadable). A data path whose Estimate().LossSamples is below
// minAdaptiveLossSamples (T270) is excluded — its denominator is too small to trust (D96
// mechanism 3) — so a sub-threshold single-drop spike cannot cross the raise gate. The returned
// weight is renormalized over the eligible subset: when the floor excludes a strict subset of a
// weighted bond's data paths the mix is taken over the survivors (dividing by their weight sum);
// when the eligible subset is EMPTY it returns (0, 0), the HOLD signal. For the single-carrier
// active-backup result ([{active, 1.0}]) the mix collapses to the active path's own loss.
//
// AGGREGATION-GATE DISCONTINUITY: when a WeightedScheduler stops aggregating mid-stream its
// DataPaths steps from the striped mix to the primary-only ([{primary, 1.0}]) signal — a step
// change in the controller input smoothed downstream by the controller's EWMA, not here.
func weightedDataPathLoss(dps []sched.DataPath, estimate func(idx int) (telemetry.Estimate, bool)) (float64, int) {
	var weightedSum, weightTotal float64
	count := 0
	for _, dp := range dps {
		est, ok := estimate(dp.Index)
		if !ok {
			continue
		}
		if est.LossSamples < minAdaptiveLossSamples {
			continue // early-regime: denominator too small to be a trustworthy loss fraction
		}
		weightedSum += dp.Weight * est.Loss
		weightTotal += dp.Weight
		count++
	}
	if count == 0 {
		return 0, 0
	}
	return weightedSum / weightTotal, count
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
// edge routes each peer's DATA/PROBE frames to ITS OWN concentrator, not a single bind-global
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
// peer endpoint (config.Peer.Endpoints) and calls this so every subsequent DATA and
// PROBE frame egresses toward the STANDBY concentrator on every path.
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
// peer's paths and resequencer, so the D32 re-baseline is scoped to the peer whose remote
// changed and can never disturb another bound peer's release point.
func (m *Multipath) SetPeerRemote(ap netip.AddrPort) {
	m.mu.Lock()
	rq := m.setPeerRemoteLocked(m.peerState, ap)
	m.mu.Unlock()

	// A hub switch changes the DATA-frame SENDER identity: the standby concentrator is a
	// separate process whose outer-seq restarts near 1, far below the release point the
	// prior hub's stream advanced THIS peer's resequencer's `next` to. Re-baseline it so the
	// standby's FIRST frame (the WG handshake response) re-anchors the release point instead
	// of being dropped as a suspect low seq — without this the tunnel never re-establishes
	// after failover (defect D32). Done OUTSIDE m.mu (the resequencer has its own mutex;
	// never nest it under m.mu). Nil on a closed bind (a resequencer is Stored per Open) —
	// the next Open seeds a fresh one whose release point re-pins to its first frame anyway.
	if rq != nil {
		rq.Rebaseline(ap)
	}
}

// setPeerRemoteLocked repoints EVERY path bound to the given peer at ap — overriding an
// already-learned/configured remote — and records ap as the bind's default remote seeded
// onto that peer's future paths, returning the peer's receive resequencer so the caller
// re-baselines it OUTSIDE m.mu (the resequencer keeps its own mutex; never nest it under
// m.mu). It writes ONLY the given peer's per-path remotes and reads ONLY that peer's
// resequencer, so a hub switch on one peer never disturbs another bound peer's wire remotes
// or release point — the per-peer D32 boundary. Returns nil on a closed bind (no
// resequencer Stored yet). Caller holds m.mu.
//
// It is the SINGLE-CONTROLLER (primary-peer) path: writing the bind-global m.defaultRemote
// here is correct ONLY because exactly one hub-failover controller exists and it drives the
// primary peer. The MULTI-controller per-peer seam is setPeerRemoteForLocked, which repoints
// one peer WITHOUT touching m.defaultRemote (a per-peer hub switch has no bind-global meaning;
// see that function and the m.defaultRemote field doc for the reader audit).
func (m *Multipath) setPeerRemoteLocked(ps *peerState, ap netip.AddrPort) *reseq.Resequencer {
	m.defaultRemote, m.hasDefaultRemote = ap, true
	for _, pp := range ps.paths {
		pp.setRemote(ap)
	}
	return ps.resequencer.Load()
}

// SetPeerRemoteFor is the PER-PEER hub-failover repoint seam (T252/G28/M105): it repoints
// exactly the named peer's paths at ap, WITHOUT clobbering the bind-global m.defaultRemote or
// any OTHER peer's wire remotes and resequencer. It is the multi-exit-edge / N-controller dual
// of SetPeerRemote (which drives the primary and does write m.defaultRemote for single-peer-
// edge back-compat): with N independent hub-failover controllers, peer B's endpoint switch must
// not disturb the remote peer A relies on, so each controller repoints only ITS peer through
// this seam. The existing single-controller SetPeerRemote call sites are unchanged.
//
// It returns an error for an unknown peer name (a wiring defect — fail fast rather than
// silently repoint nothing). The resequencer re-baseline (D32) runs OUTSIDE m.mu, exactly as
// SetPeerRemote does.
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
	rq, err := m.setPeerRemoteForLocked(p, ap)
	m.mu.Unlock()
	if err != nil {
		// A cross-peer remote collision left ALL state untouched (the guard runs before any
		// mutation), so there is nothing to re-baseline — surface the wiring defect.
		return err
	}

	// Re-baseline OUTSIDE m.mu (the resequencer owns its own mutex; never nest it under m.mu),
	// mirroring SetPeerRemote — the standby hub's outer-seq restarts low and must re-anchor the
	// release point instead of being SUSPECT-dropped (D32).
	if rq != nil {
		rq.Rebaseline(ap)
	}
	return nil
}

// setPeerRemoteForLocked is the per-peer core of SetPeerRemoteFor: it repoints ONLY peer p's
// per-path remotes at ap and returns p's receive resequencer for the caller to re-baseline
// outside m.mu (nil on a closed bind — no resequencer Stored yet). Unlike setPeerRemoteLocked
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
func (m *Multipath) setPeerRemoteForLocked(p *peerState, ap netip.AddrPort) (*reseq.Resequencer, error) {
	if owner, ok := m.edgePeerByRemote[ap]; ok && owner != p {
		return nil, fmt.Errorf("bind: SetPeerRemoteFor: remote %s is already owned by peer %q; refusing to repoint peer %q onto it (edgePeerByRemote cannot map two peers to one addr:port)", ap, owner.name, p.name)
	}
	if p.hasConfiguredRemote && p.configuredRemote != ap {
		delete(m.edgePeerByRemote, p.configuredRemote)
	}
	p.configuredRemote, p.hasConfiguredRemote = ap, true
	m.edgePeerByRemote[ap] = p
	for _, pp := range p.paths {
		pp.setRemote(ap)
	}
	return p.resequencer.Load(), nil
}

// Close tears down every per-path socket and CLEARS the bind's path state so a
// subsequent Open fully rebuilds it. The amneziawg engine drives exactly this
// lifecycle — device.upLocked → BindUpdate → closeBindLocked calls Close() before
// every Open(), and a Down/Up cycles Close after Open — so Close must leave the
// bind reopenable (matching conn.StdNetBind, whose closed state is simply "no
// sockets"). Outstanding receive calls return an error as their socket closes.
func (m *Multipath) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Release the fan-in first so any reader parked on a delivery poke, and the
	// engine-facing drainer, unblock; then close the sockets so every readLoop
	// returns; then wait for them all to exit before clearing state so no reader
	// outlives this Close (and no reader touches the resequencer a later Open
	// replaces). readLoop needs no lock, so waiting under m.mu cannot deadlock.
	if m.recvClosed != nil {
		close(m.recvClosed)
	}
	err := m.closeSocketsLocked()
	m.readersWG.Wait()
	m.deliverSignal = nil
	m.recvClosed = nil
	return err
}

// closeSocketsLocked closes all path sockets and resets the bind to the unopened
// state (paths and send Codec cleared), returning the first close error. Caller
// holds m.mu. Idempotent: safe on an already-closed or never-opened bind — which
// is exactly what the engine's pre-open Close relies on.
func (m *Multipath) closeSocketsLocked() error {
	var firstErr error
	// Sockets are SHARED (one per shared path), so close them once from the shared list —
	// NOT once per (peer,path) view, which would double-close a concentrator socket.
	for _, sp := range m.shared {
		if sp.conn != nil {
			if err := sp.conn.Close(); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	m.shared = nil
	m.deferred = nil
	// Clear every peer's per-Open state so the next Open rebuilds it fresh (paths, send
	// Codec, FEC send/receive). The deadline-tick goroutine that reads a peer's fecSend is
	// stopped by Close (recvClosed) and joined via readersWG before state is cleared, and it
	// captured its own fecSender pointer, so niling here never races it. The per-path readLoops
	// are joined by readersWG.Wait AFTER this returns, so one may still be mid-instantiation
	// (ensurePeerReceiveInstantiated) here; clear the FEC planes under lifecycleMu so that build
	// cannot interleave and resurrect a half-published plane past this Close.
	for _, p := range m.peers {
		p.paths = nil
		p.sendCodec = nil
		p.lifecycleMu.Lock()
		p.fecSend.Store(nil)
		p.fecRecv.Store(nil)
		p.lifecycleMu.Unlock()
	}
	return firstErr
}

// AddPath admits a new path to the running bond at runtime (T30): it binds the
// path's source-addr'd socket, mints its prober (via the injected factory) joining
// the current probe session, seeds its remote from the config dest_addr or the
// learned default, admits it to the scheduler as a NEW LOWEST-PRIORITY path, and
// spawns its Bind-owned reader. Everything runs under m.mu, so the path slice and
// the scheduler's path list are mutated together and stay index-aligned as Send
// observes them (Send also holds m.mu). The new path starts DOWN (its prober has no
// echoes yet) and is only selected once its probes report healthy, so admission
// disturbs neither the active selection of the surviving paths nor the WG session:
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
func (m *Multipath) AddPath(def config.Path) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.paths) == 0 {
		return errClosed // only a running bind can take a runtime path
	}
	if m.newProber == nil {
		return errors.New("bind: cannot add a path at runtime without the probe transport")
	}
	if _, ok := m.scheduler.(sched.DynamicScheduler); !ok {
		return errors.New("bind: scheduler does not support runtime path membership")
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
			// the deferred set (Down, absent from the scheduler) and return success, so a
			// reload that introduces such a path does not fail the entire reload. AddPath
			// already requires the probe transport + a DynamicScheduler (checked above),
			// which is exactly the Down model Open's tolerance needs. The prober is minted
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
			// id so DATA and PROBE agree), keeping every peer's prober slice index-aligned with
			// m.defs. Each per-peer prober stays Down and absent from the scheduler until a later
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
	shared := &sharedPathState{name: def.Name, id: id, src: def.SourceAddr, conn: c, bindMode: def.Bind, boundDevice: dev}

	// FAN-OUT (single owner): instantiate the per-(peer,path) state for EVERY currently-
	// bound peer, minting each peer's own Codec + prober and admitting it to that peer's
	// scheduler. attached[k] is m.peers[k]'s view of the new shared socket. A failure in any
	// peer rolls back every peer already attached, so a partial fan-out never leaks.
	attached, err := m.attachSharedPathLocked(shared, def, id, nil)
	if err != nil {
		_ = c.Close()
		return err
	}
	// The fan-out succeeded: every currently-bound peer now has a view + scheduler entry
	// for this socket, so the fallback facts these log are backed by a real, live,
	// installed path, not a claim (D53 round 2 / FIX 2; ordering — round 3 / CRITICISM 1).
	m.warnForcedDeviceUnresolvable(def.Name, def.Bind, def.SourceAddr, dev)
	m.warnDeviceBindFallback(def.Name, def.Bind, dev, deviceErr)

	// Durable membership: the def is SHARED (one socket), recorded once; each peer records
	// its OWN prober so a subsequent Close→Open rebuilds THIS path and re-pins each peer's
	// scheduler. Kept index-aligned with m.defs under m.mu, so the runtime add survives a
	// reopen instead of vanishing — or leaving a frozen scheduler health entry with no path
	// to Tick it (total-egress-outage defect).
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
// peer and admits each to that peer's scheduler. It returns the created views in peer order
// (attached[k] belongs to m.peers[k]). On any peer's failure it rolls back every peer already
// attached (dropping the scheduler entry and popping the appended peerPathState), so a
// partial fan-out never leaks a half-admitted path. Caller holds m.mu and, on success, owns
// appending the shared socket + each peer's prober to the durable membership.
//
// probers, when non-nil, MUST hold exactly one entry per m.peers (peer order) — that peer's
// OWN, ALREADY m.defs-aligned prober to REUSE rather than mint fresh (the deferred-promote
// fan-out, promoteDeferredLocked: a promoted deferred path's probers already exist in every
// peer's p.probers from the original admission, so promotion must not mint a second, different
// prober per peer). nil mints a FRESH prober per peer via that peer's newProber factory (the
// runtime AddPath fan-out for a brand-new path).
func (m *Multipath) attachSharedPathLocked(shared *sharedPathState, def config.Path, id uint8, probers []*telemetry.Prober) ([]*peerPathState, error) {
	attached := make([]*peerPathState, 0, len(m.peers))
	for pi, p := range m.peers {
		var prober *telemetry.Prober
		if probers != nil {
			prober = probers[pi]
		}
		pp, err := m.attachPeerPathLocked(p, shared, def, id, prober)
		if err != nil {
			for k := len(attached) - 1; k >= 0; k-- {
				if derr := m.detachPeerPathBoundLocked(m.peers[k], shared.name); derr != nil {
					// D67: a rollback detach must not be silent — surface the failure. The
					// path was still force-spliced from p.paths, so no stale view survives.
					m.log.Error("bind: rollback detach failed during shared-path fan-out",
						"path", shared.name, "err", derr.Error())
				}
			}
			return nil, err
		}
		attached = append(attached, pp)
	}
	return attached, nil
}

// attachPeerPathLocked builds ONE peer's view of a shared path: its own decode Codec, the
// given prober (or a freshly-minted one, stamped with the shared path-id so DATA and PROBE
// agree on the wire, when prober is nil), the seeded return remote, and admission to that
// peer's scheduler as a NEW LOWEST-PRIORITY path (so it never steals a healthy survivor's
// active selection). It does NOT touch the durable membership (m.defs / p.probers) —
// attachSharedPathLocked's caller owns that after the whole fan-out succeeds. Caller holds
// m.mu.
// admissionFor pairs a path's prober (its scheduler health source) with the path's OWN
// identity-sourced per-path pacing (defect D79). The pacing is read from the scheduler's
// configured per-path sizing by the prober's ORIGINAL definition index (PathID), so a
// bound/promoted path always carries its own declared token-bucket rate regardless of its
// position in the bound-path slice — the fix for a deferred path shifting the health indices and
// letting the sole bound path inherit the deferred path's slower pace. When the scheduler does
// not pace per-path (weighted / pacing-off, i.e. not a PerPathPacingConfig), or the path is
// outside the configured set, the zero Pacing is inert.
func admissionFor(scheduler sched.Scheduler, prober *telemetry.Prober) sched.PathAdmission {
	adm := sched.PathAdmission{Health: prober}
	if ppc, ok := scheduler.(sched.PerPathPacingConfig); ok && prober != nil {
		if pacing, ok := ppc.ConfiguredPacing(int(prober.PathID())); ok {
			adm.Pacing = pacing
		}
	}
	return adm
}

func (m *Multipath) attachPeerPathLocked(p *peerState, shared *sharedPathState, def config.Path, id uint8, prober *telemetry.Prober) (*peerPathState, error) {
	dyn, ok := p.scheduler.(sched.DynamicScheduler)
	if !ok {
		return nil, errors.New("bind: scheduler does not support runtime path membership")
	}
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
	pp.pmtuProbe = pp.buildPMTUProbe()
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
	// Append to the peer's path slice, then admit the prober to that peer's scheduler as the
	// new tail; both are index-aligned, so the scheduler's returned index must equal the new
	// path's slice index. A mismatch would mis-route datagrams, so fail loudly and roll back.
	p.paths = append(p.paths, pp)
	schedIdx, err := dyn.AddPath(admissionFor(p.scheduler, pp.prober))
	if err != nil {
		p.paths = p.paths[:len(p.paths)-1]
		return nil, err
	}
	if schedIdx != len(p.paths)-1 {
		bindIdx := len(p.paths) - 1
		_ = dyn.RemovePath(schedIdx)
		p.paths = p.paths[:len(p.paths)-1]
		return nil, fmt.Errorf("bind: scheduler/path index skew after add: sched=%d bind=%d", schedIdx, bindIdx)
	}
	// Stamp the scheduler index so a directly-written probe/echo on this path charges the
	// right token bucket (T145); the skew guard above just proved schedIdx == its position.
	pp.schedIdx.Store(int32(schedIdx))
	// Publish this peer's view of the shared socket for the receive demux (T88): once >1 peer
	// has a view, handleInbound source-demuxes the socket's datagrams to their owning peer.
	shared.addViewLocked(pp)
	return pp, nil
}

// detachPeerPathBoundLocked drops one peer's BOUND view of a shared path (matched by name)
// from that peer's scheduler and paths slice. It does NOT touch the durable membership
// (m.defs / p.probers) — the caller (RemovePath, or the fan-out rollback) owns that. It is a
// no-op when the peer holds no bound view of the path. Caller holds m.mu.
func (m *Multipath) detachPeerPathBoundLocked(p *peerState, name string) error {
	dyn, ok := p.scheduler.(sched.DynamicScheduler)
	if !ok {
		return errors.New("bind: scheduler does not support runtime path membership")
	}
	idx := -1
	for i, pp := range p.paths {
		if pp.name == name {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil
	}
	// D67: capture the RemovePath outcome but ALWAYS splice p.paths (and re-stamp survivors)
	// regardless of it, so a RemovePath failure never leaves a stale peerPathState in p.paths
	// that the receive demux could still route to. The error is returned (the caller logs it),
	// not short-circuited before the splice.
	removeErr := dyn.RemovePath(idx)
	p.paths = append(p.paths[:idx], p.paths[idx+1:]...)
	// The scheduler shifted every path above idx down by one; re-stamp the survivors so
	// their schedIdx keeps addressing the right token bucket for probe accounting (T145).
	for k := idx; k < len(p.paths); k++ {
		p.paths[k].schedIdx.Store(int32(k))
	}
	return removeErr
}

// RemovePath drains and closes the named path at runtime (T30). It drops the path
// from the scheduler FIRST (so no further datagram is scheduled onto it), unlinks it
// from the path slice, and closes its socket — which retires its Bind-owned reader.
// All under m.mu, so the structures stay coherent for Send. In-flight state is
// preserved: frames the path already pushed into the resequencer stay queued and are
// delivered in outer-seq order (T18 resequencing is connection-global, keyed on
// outer-seq, NOT per-path, so a removal never resets it), the surviving paths and
// their scheduling are untouched, and the single virtual endpoint / WG session is
// undisturbed. The last remaining LIVE path cannot be removed (that would tear down
// the virtual endpoint the engine holds).
//
// A DEFERRED path (present in the durable membership but not yet bound, because its
// source_addr was not assignable at Open) has no socket, reader, or scheduler entry:
// removing it merely drops it from the durable membership + deferred set, so a reload
// that DROPS a still-deferred path retires it cleanly.
//
// Since a tolerant Open leaves m.defs/m.probers (durable, full length) LONGER than
// m.paths (bound only), the durable-membership splice is keyed by IDENTITY (name),
// NOT by the m.paths index — indexing m.defs by the m.paths position would splice the
// wrong entry once a deferred path precedes the removed one.
func (m *Multipath) RemovePath(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.paths) == 0 {
		return errClosed
	}
	if _, ok := m.scheduler.(sched.DynamicScheduler); !ok {
		return errors.New("bind: scheduler does not support runtime path membership")
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
	// Is it a LIVE (bound) shared socket? If so it owns a socket + per-peer scheduler
	// entries to retire.
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
	// peer — its scheduler entry and its peerPathState — so no peer schedules onto the
	// closing socket. Each peer's remaining paths are untouched (the splice is by identity).
	for _, p := range m.peers {
		if err := m.detachPeerPathBoundLocked(p, name); err != nil {
			return err
		}
	}
	m.shared = append(m.shared[:sharedIdx], m.shared[sharedIdx+1:]...)
	if err := m.removeDurableLocked(defIdx, name); err != nil {
		// The shared socket + every peer's bound view/scheduler entry are already detached
		// above; only the durable-membership splice failed (a wiring defect, never expected
		// in practice — see removeDurableLocked). Close the socket before surfacing the error
		// rather than leaking it.
		_ = sp.conn.Close()
		return err
	}
	// Closing the socket unblocks and retires the path's reader; it is NOT waited on
	// here (it never touches the path slice, and any last in-flight frame it Observes
	// is delivered normally), so a removal never blocks the caller behind a read.
	return sp.conn.Close()
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
		if p.probers != nil && len(p.probers) != len(m.defs) {
			return fmt.Errorf("bind: remove path %q: peer %q prober set (len %d) is misaligned with the durable membership (len %d) — per-peer prober fan-out desync (wiring defect)", name, p.name, len(p.probers), len(m.defs))
		}
	}
	m.defs = append(m.defs[:defIdx], m.defs[defIdx+1:]...)
	for _, p := range m.peers {
		if p.probers != nil {
			p.probers = append(p.probers[:defIdx], p.probers[defIdx+1:]...)
		}
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
// bond is configured for, in priority order, INCLUDING a path that is currently
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
// takes m.mu so it never races peer registration. It errors if the indices are out of range or
// the peer carries no prober for that path (a bind without the probe transport).
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
	return p.probers[pathIdx].SendProbe()
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
// from the byte-counter delta across scrapes). Estimate/State are the telemetry
// zero-values on a bind without the probe transport (no prober).
type PathTraffic struct {
	Name     string
	TxBytes  uint64
	RxBytes  uint64
	Estimate telemetry.Estimate
	State    telemetry.PathState
	// ProbeSendErrors is the cumulative count of PROBE-frame socket write errors
	// emitProbes has dropped for this path (defect D96 item 4), read verbatim from
	// peerPathState.probeSendErrors.
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

// PeerSnapshot is a consistent per-BOUND-PEER snapshot of path traffic+telemetry, FEC
// counters, and resequencer counters (T94): the read side the per-peer /metrics
// exposition scrapes. It reports EVERY bound peer (not just the primary), so a
// multi-peer concentrator's metrics can attribute each series to the edge it came
// from. Name is BoundPeerNames()[i]: "" for the primary on the single-peer edge/hub only;
// the peer's configured name otherwise, including the concentrator's first-configured peer
// once SetPrimaryPeerName has run (D58).
type PeerSnapshot struct {
	Name  string
	Paths []PathTraffic
	FEC   FECStats
	Reseq reseq.Stats
	// Aggregation is the weighted scheduler's aggregation-gate snapshot (T146),
	// present ONLY for a peer whose scheduler exposes it (the weighted policy, via
	// *sched.WeightedScheduler's AggregationSnapshot(), T143). It is nil for an
	// active-backup peer — which has no aggregation gate — so the four Q54 aggregation
	// series are ABSENT for that peer, rather than fabricating a gate reading the way a
	// zero-valued struct would.
	Aggregation *sched.AggregationSnapshot
}

// aggregationReporter is the small OPTIONAL seam a scheduler implements to expose its
// aggregation-gate state to the /metrics plumbing (T146). Only *sched.WeightedScheduler
// satisfies it (its AggregationSnapshot() reads the gate under the scheduler's OWN mutex,
// T143); active-backup does not, so type-asserting a peer's scheduler against it is how
// PeerSnapshots decides per-peer whether the aggregation series exist. AggregationSnapshot
// advances no per-frame distribution state, so polling it at scrape time never perturbs
// selection — and, like the prober Estimate()/decoder stats() reads, it is called AFTER
// m.mu is released so the scrape never blocks an in-flight Send.
type aggregationReporter interface {
	AggregationSnapshot() sched.AggregationSnapshot
}

// PeerSnapshots returns a consistent per-peer snapshot, in bound-peer order (matching
// BoundPeerNames), for every bound peer's path traffic+telemetry, FEC counters, and
// resequencer counters. Concurrency: grab each peer's name, per-path counters/prober
// pointers, and FEC/resequencer pointers under m.mu in one bounded O(peers+paths) copy,
// then RELEASE m.mu before calling the independently-synchronized prober
// Estimate()/State(), decoder stats(), and resequencer Stats() — so the scrape never
// blocks an in-flight Send behind m.mu. len(result) >= 1: NewMultipath always binds at
// least the primary peer, so this is never empty — though the per-peer Paths slice can
// still be empty for a peer with a currently-empty path set.
func (m *Multipath) PeerSnapshots() []PeerSnapshot {
	type pathRef struct {
		name      string
		tx, rx    uint64
		probeErrs uint64
		prober    *telemetry.Prober
		// pp is captured under m.mu; its src/conn/bindMode/boundDevice are immutable
		// and its remote is ps.mu-guarded (getRemote), so the addressing fields are
		// read AFTER m.mu is released, exactly like the prober Estimate()/State() reads.
		pp *peerPathState
	}
	type peerRef struct {
		name  string
		paths []pathRef
		fs    *fecSender
		fr    *fecReceiver
		rq    *reseq.Resequencer
		sched sched.Scheduler
	}

	m.mu.Lock()
	refs := make([]peerRef, len(m.peers))
	for i, p := range m.peers {
		r := peerRef{name: p.name, fs: p.fecSend.Load(), fr: p.fecRecv.Load(), rq: p.resequencer.Load(), sched: p.scheduler}
		r.paths = make([]pathRef, len(p.paths))
		for j, pp := range p.paths {
			r.paths[j] = pathRef{name: pp.name, tx: pp.txBytes.Load(), rx: pp.rxBytes.Load(), probeErrs: pp.probeSendErrors.Load(), prober: pp.prober, pp: pp}
		}
		refs[i] = r
	}
	m.mu.Unlock()

	out := make([]PeerSnapshot, len(refs))
	for i, r := range refs {
		snap := PeerSnapshot{Name: r.name}
		snap.Paths = make([]PathTraffic, len(r.paths))
		for j, pr := range r.paths {
			pt := PathTraffic{Name: pr.name, TxBytes: pr.tx, RxBytes: pr.rx, ProbeSendErrors: pr.probeErrs}
			if pr.prober != nil {
				pt.Estimate = pr.prober.Estimate()
				pt.State = pr.prober.State()
			}
			// Addressing (G21): src/bindMode/boundDevice are immutable; LocalAddr comes
			// from the socket; Remote is read under ps.mu via getRemote — all AFTER m.mu
			// is released, so the scrape never blocks an in-flight Send.
			if pr.pp != nil {
				pt.Source = pr.pp.src
				pt.BindMode = pr.pp.bindMode
				pt.BoundDevice = pr.pp.boundDevice
				if pr.pp.conn != nil {
					if ua, ok := pr.pp.conn.LocalAddr().(*net.UDPAddr); ok {
						pt.LocalAddr = ua.AddrPort()
					}
				}
				if rem, ok := pr.pp.getRemote(); ok {
					pt.Remote = rem
				}
			}
			snap.Paths[j] = pt
		}
		if r.fs != nil {
			snap.FEC.DataFrames = r.fs.dataFrames.Load()
			snap.FEC.DataBytes = r.fs.dataBytes.Load()
			snap.FEC.ParityFrames = r.fs.parityFrames.Load()
			snap.FEC.ParityBytes = r.fs.parityBytes.Load()
			// The adaptive controller's decision is present only in adaptive mode (ctrl is
			// set once at construction and never mutated, so this nil-check is race-free
			// after the fecSend atomic Load). A fixed-ratio peer leaves Adaptive nil so no
			// adaptive series is fabricated (the Aggregation nil-precedent, T146). The read
			// is lock-free — the atomics are published at the m.mu-held drive locus (T263).
			if r.fs.ctrl != nil {
				adaptive := r.fs.adaptiveSnapshot()
				snap.FEC.Adaptive = &adaptive
			}
		}
		if r.fr != nil {
			// Recovered is the HONEST delivered count (frames placed ahead of the release
			// point), NOT the decoder's raw reconstruction count — a frame rebuilt after the
			// resequencer skipped its gap is reconstructed but never delivered, so counting it
			// would overstate recovery on /metrics. Unrecoverable is the decoder's repair-
			// failure count (groups evicted still incomplete).
			if r.fr.connLoss != nil {
				snap.FEC.ResidualLoss = r.fr.connLoss.Loss()
			}
			snap.FEC.Recovered = r.fr.deliveredRecovered.Load()
			snap.FEC.Unrecoverable = r.fr.stats().Unrecoverable
		}
		if r.rq != nil {
			snap.Reseq = r.rq.Stats()
		}
		// Poll the aggregation gate only for a scheduler that reports one (weighted policy);
		// active-backup does not satisfy aggregationReporter, so its peers leave Aggregation
		// nil and the Q54 series are absent (T146). Like the prober/decoder reads above, this
		// runs after m.mu is released — AggregationSnapshot takes the scheduler's own lock, not
		// the send lock, so it never blocks Send across a Pick.
		if rep, ok := r.sched.(aggregationReporter); ok {
			agg := rep.AggregationSnapshot()
			snap.Aggregation = &agg
		}
		out[i] = snap
	}
	return out
}

// SetMark is a no-op for T12: per-path SO_MARK is a scheduler concern (T15), and
// the engine only calls SetMark when a fwmark is configured, which wanbond does
// not set.
func (m *Multipath) SetMark(uint32) error { return nil }

// BatchSize is the max number of datagrams passed to a ReceiveFunc / Send.
func (m *Multipath) BatchSize() int { return multipathBatchSize }
