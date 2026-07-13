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

var (
	errNoHealthyPath = errors.New("bind: no healthy path with a known remote endpoint")
	// errPacerShedding is returned by Send when the scheduler shed the datagram for
	// pacing (PickPaced) while paths are healthy — deliberate rate limiting, NOT an
	// outage. It is DISTINCT from errNoHealthyPath so operator logs and the e2e
	// log-grep harness can tell shedding from total path failure (the drop behavior is
	// identical to the pre-existing no-path case; only the diagnostic differs). The
	// coalesced, rate-limited "pacer shedding" record is emitted by the scheduler.
	errPacerShedding = errors.New("bind: datagram shed by send pacer (paths healthy, rate limited)")
	errClosed        = net.ErrClosed
)

// pathState is one configured uplink: its source-bound UDP socket, stable
// path-id, its own decode Codec (each path receives on its own goroutine, so the
// Codec's scratch is never shared), and the per-path remote endpoint. The remote
// is either configured (edge dest_addr / peer endpoint) or LEARNED from inbound
// traffic (concentrator) — but it lives strictly BELOW the engine's single
// virtual endpoint, so the engine never sees this per-path bookkeeping churn.
type pathState struct {
	name  string
	id    uint8
	src   netip.Addr
	conn  *net.UDPConn
	codec *frame.Codec
	// prober is this path's own probe initiator (nil when the bind runs without the
	// probe transport). It is set at path creation and immutable for the path's life,
	// so the Bind-owned receive goroutine reaches it via the pathState it already
	// holds — NOT through a shared, dynamically-mutated m.probers slice — which is
	// what keeps echo handling race-free while paths are added/removed at runtime (T30).
	prober *telemetry.Prober

	// txBytes/rxBytes are cumulative OUTER-wire byte counters for this path, the
	// per-path traffic accounting the /metrics exposition reports (T23). txBytes counts
	// the DATA-frame wire bytes this path egresses on the Send hot path; rxBytes counts
	// every outer datagram this path's readLoop receives (DATA, PROBE, and echo alike —
	// the true received wire volume). They are atomics so the send/receive hot paths
	// increment them WITHOUT taking m.mu (lock-free), and the scrape/snapshot path reads
	// them with a plain atomic Load; a single writer per counter (Send holds m.mu while
	// choosing the path, and each path has exactly one readLoop) means the Add is
	// uncontended in the common case. "Bytes on Starlink vs 5G" — the data-thrift signal
	// (requirement 2) — is exactly txBytes: when the weighted scheduler collapses to the
	// primary under sub-capacity load, the backup path's Send count stays ~flat.
	txBytes atomic.Uint64
	rxBytes atomic.Uint64

	mu        sync.Mutex
	remote    netip.AddrPort
	hasRemote bool
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
}

func (ps *pathState) setRemote(ap netip.AddrPort) {
	ps.mu.Lock()
	ps.remote, ps.hasRemote = ap, true
	ps.mu.Unlock()
}

func (ps *pathState) getRemote() (netip.AddrPort, bool) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return ps.remote, ps.hasRemote
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
// stamped with the given stable path-id. The device wires it capturing the SAME
// per-boot session id, prober config, clock, PSK, and logger the boot-time probers
// were built with, so a runtime path's liveness is measured identically to a
// boot-time path (its probes join the same session so the peer's reflector adopts
// them without a challenge reset). It is nil on a bind built without the probe
// transport (the T12 unit tests), which therefore cannot add paths at runtime.
type ProberFactory func(name string, id uint8) *telemetry.Prober

type Multipath struct {
	psk       config.Key
	defs      []config.Path
	scheduler sched.Scheduler

	// classify maps each outbound datagram to its pacer traffic class from the inner
	// WireGuard message type, parameterized by the tunnel's Amnezia obfuscation profile
	// so a WireGuard control frame (handshake/keepalive) is recognised — and pacing-
	// exempted — under advanced security too, not only in vanilla mode (defect D22). It
	// is immutable after construction and holds no lock, so Send reads it off m.mu.
	classify wgClassifier

	// probers is the BOOT-TIME per-path probe initiator set, in path order, shared
	// with the scheduler (the SAME *telemetry.Prober values back its PathHealth). At
	// Open each is bound onto its pathState (ps.prober), and thereafter the hot paths
	// reach a path's prober through the pathState — never by indexing this slice — so
	// a runtime path add/remove cannot race echo handling. It is nil when the bind
	// runs without the probe transport (the T12 unit tests, which drive selection via
	// AlwaysUp); a nil here also gates the whole probe loop off.
	probers []*telemetry.Prober
	// newProber mints a prober for a path admitted at runtime (T30); nil disables
	// runtime path addition (a bind without the probe transport).
	newProber ProberFactory
	// deferredListen binds a reconciled deferred path's socket (T55 background
	// reconcile). It is an injection seam: the default pins the source IP (net.ListenUDP,
	// matching AddPath's runtime bind), and a test overrides it to drive the
	// deferred→bound transition deterministically — a source_addr "becoming assignable"
	// — without a real interface address having to appear on the host. Immutable after
	// construction, never nil.
	deferredListen func(src netip.Addr, port uint16) (*net.UDPConn, error)
	// reflector answers inbound peer probes (IsEcho=false) with an authenticated
	// echo. A single Reflector serves every path (its anti-replay is PathID-keyed)
	// and it is internally synchronized, so the per-path receive goroutines share it.
	reflector *telemetry.Reflector

	mu        sync.Mutex
	paths     []*pathState
	sendCodec *frame.Codec
	virt      *udpEndpoint
	// deferred holds the configured paths whose well-formed source_addr was not yet
	// assignable at the last Open (EADDRNOTAVAIL). They are NOT in m.paths or the
	// scheduler — the tunnel runs on the paths that bound — but are recorded here,
	// index-independent of m.paths, for the T55 background reconcile to retry as their
	// addresses appear. Rebuilt from scratch on every Open; guarded by m.mu.
	deferred []deferredPath
	// defaultRemote is the fallback per-path remote (the peer's wireguard
	// endpoint) applied to any path without its own dest_addr. It may be set by
	// ParseEndpoint BEFORE Open, so it is stored here and applied at Open time.
	defaultRemote    netip.AddrPort
	hasDefaultRemote bool

	outerSeq atomic.Uint64

	// resequencer is the shared T18 receive resequencing buffer. Every path's
	// decoded DATA frame is pushed through it (by outer-seq) and delivered up the
	// WG path IN ORDER, so WG's inner anti-replay window never sees multipath
	// reorder. It is (re)created per Open — matching the socket lifecycle, and
	// re-pinning its release point after a reconnect — and published atomically so
	// the per-path receive goroutines read it WITHOUT m.mu, preserving T12's
	// lock-free receive fast path. Its own mutex is disjoint from m.mu, so it adds
	// no contention to Send and is never held across a syscall. PROBE handling
	// (T37) never touches it.
	resequencer atomic.Pointer[reseq.Resequencer]

	// FEC datapath (T24). fecCfg is the fixed-ratio Reed-Solomon configuration, nil
	// when FEC is disabled — in which case the whole plane is inert and the datapath
	// is byte-for-byte the pre-T24 behaviour. fecSend (the send-side encoder + parity
	// counters) is (re)built per Open and accessed only under m.mu — on the Send path
	// and the single deadline-tick goroutine (which TryLocks). fecRecv (the receive-
	// side recovery decoder, self-guarded) is published atomically like resequencer so
	// the per-path readLoop goroutines read it WITHOUT m.mu, preserving the lock-free
	// receive fast path. Both are nil/empty when FEC is off. See fec.go.
	fecCfg  *fec.Config
	fecSend *fecSender
	fecRecv atomic.Pointer[fecReceiver]

	// adaptiveCfg is the adaptive-FEC controller configuration (T29), nil in fixed-ratio
	// mode. When both fecCfg and adaptiveCfg are non-nil, Open builds one adaptivefec
	// Controller per span and attaches it to fecSend; the FEC tick loop then drives it
	// from the measured per-path loss and resizes the encoder's per-group parity. It is
	// immutable after construction, so the send/receive hot paths read it without a lock.
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
func NewMultipath(paths []config.Path, psk config.Key, scheduler sched.Scheduler, probers []*telemetry.Prober, newProber ProberFactory, fecCfg *fec.Config, adaptiveCfg *adaptivefec.Config, amnezia config.Amnezia) (*Multipath, error) {
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
	return &Multipath{
		psk:            psk,
		defs:           append([]config.Path(nil), paths...),
		scheduler:      scheduler,
		classify:       newWGClassifier(amnezia),
		probers:        probers,
		newProber:      newProber,
		deferredListen: defaultDeferredListen,
		reflector:      telemetry.NewReflector(psk, rand.Reader),
		virt:           &udpEndpoint{},
		fecCfg:         fecCfg,
		adaptiveCfg:    adaptiveCfg,
	}, nil
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

	sendCodec, err := frame.NewCodec(m.psk)
	if err != nil {
		return nil, 0, err
	}

	// A fresh resequencer per Open: its release point re-pins to the first frame
	// received after this bring-up, so a Close→Open cycle (or reconnect) never
	// wedges on a stale high-water outer-seq.
	m.resequencer.Store(reseq.New(resequencerWindow, resequencerTimeout, reseq.SystemClock{}))

	// Fresh FEC send/receive state per Open, when FEC is enabled (T24). The encoder
	// group state and the decoder's per-group buffers re-pin with the sockets, so a
	// Close→Open cycle never reconstructs against a stale group. Both are torn down in
	// closeSocketsLocked. A build error here is a programmer error (the ratio was
	// validated in NewMultipath), so it fails the Open.
	if m.fecCfg != nil {
		enc, err := fec.NewEncoder(*m.fecCfg, fec.SystemClock{})
		if err != nil {
			_ = m.closeSocketsLocked()
			return nil, 0, fmt.Errorf("bind: build FEC encoder: %w", err)
		}
		dec, err := fec.NewDecoder(*m.fecCfg)
		if err != nil {
			_ = m.closeSocketsLocked()
			return nil, 0, fmt.Errorf("bind: build FEC decoder: %w", err)
		}
		dec.SetRetainWindow(fecRetainGroups)
		// Quiescence-accurate unrecoverable accounting (D24): fold a doomed group into
		// the unrecoverable counter once its recovery could no longer have helped, even
		// when no newer group arrives to advance the retain-window eviction (a stalled
		// link, or traffic that simply stops after an incident). Past fecRecoveryDeadline
		// from a group's first-seen instant, its deadline-flushed parity has certainly
		// arrived (encoder grouping deadline) AND the resequencer has certainly skipped
		// its gap (resequencerTimeout), so any later reconstruction is delivered too late
		// to matter — the group is definitively unrecoverable and safe to count.
		dec.SetClock(fec.SystemClock{})
		dec.SetRecoveryDeadline(m.fecCfg.Deadline + resequencerTimeout)
		m.fecSend = &fecSender{enc: enc}
		// Adaptive mode (T29): a fresh controller per Open, re-pinned with the encoder so a
		// Close→Open cycle starts the control law from M=0 (no standing redundancy until
		// loss is observed) rather than inheriting a stale target. The controller cfg was
		// validated in NewMultipath, so a build error here is a programmer error and fails
		// the Open. The receive-side residual-loss estimator (post-FEC-recovery loss, the
		// P4 signal) is likewise fresh per span.
		if m.adaptiveCfg != nil {
			ctrl, err := adaptivefec.NewController(*m.adaptiveCfg, adaptivefec.SystemClock{})
			if err != nil {
				_ = m.closeSocketsLocked()
				return nil, 0, fmt.Errorf("bind: build adaptive FEC controller: %w", err)
			}
			m.fecSend.ctrl = ctrl
			// Adopt the controller's starting parity (0 — no standing redundancy until loss
			// is observed) so the encoder and controller agree from t=0; the tick loop then
			// sizes it to measured loss within the first control interval. Fixed mode leaves
			// the encoder at its cfg.ParityShards default instead.
			enc.SetParity(ctrl.Parity())
		}
		m.fecRecv.Store(&fecReceiver{dec: dec, connLoss: telemetry.NewConnLoss(fecResidualLossWindow)})
	}

	// Resolve every path's interface up front and decide, per path, whether its
	// socket may be device-bound (SO_BINDTODEVICE + wildcard — survives a T16
	// re-roam) or must pin the specific source IP (the pre-T16 behaviour, required
	// when paths share an interface or the interface is multi-address, so distinct
	// specific-IP sockets coexist on one port without an EADDRINUSE collision). See
	// selectDeviceBinds.
	srcs := make([]netip.Addr, len(m.defs))
	for i := range m.defs {
		srcs[i] = m.defs[i].SourceAddr
	}
	bindDevs := planPathBinds(srcs)

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
	dyn, dynOK := m.scheduler.(sched.DynamicScheduler)
	tolerateDefer := m.probers != nil && dynOK
	m.deferred = nil

	actualPort := port
	firstBound := true
	// boundProbers is index-aligned with m.paths (the paths that actually bound), so
	// the scheduler reconcile below and Send's Pick->m.paths[idx] mapping stay aligned
	// even when a middle path deferred. nil in the no-prober case (nothing to reconcile).
	var boundProbers []*telemetry.Prober
	for i := range m.defs {
		def := m.defs[i]
		// Device-bind this path when selectDeviceBinds proved it safe (so a
		// mid-session source-address change / T16 re-roam does not break the socket),
		// otherwise pin the specific source IP. See selectDeviceBinds / listenPath.
		c, err := listenPath(def.SourceAddr, port, bindDevs[i])
		if err != nil {
			if tolerateDefer && errors.Is(err, syscall.EADDRNOTAVAIL) {
				// Defer this path: record its def + boot prober (kept Down) for the T55
				// background reconcile to retry, and leave the bond to come up on the rest.
				// Mirrors AddPath's rollback discipline — a failed path never disturbs the
				// tunnel — at the boot boundary.
				m.deferred = append(m.deferred, deferredPath{def: def, prober: m.probers[i]})
				continue
			}
			_ = m.closeSocketsLocked()
			return nil, 0, fmt.Errorf("bind: open path %q on %s: %w", def.Name, def.SourceAddr, err)
		}
		// Large SO_RCVBUF, best-effort: the kernel caps at net.core.rmem_max and
		// SetReadBuffer does not require privilege, so a returned error is rare;
		// treat it as non-fatal rather than refusing to bind in a restricted env.
		_ = c.SetReadBuffer(socketRecvBuffer)

		codec, err := frame.NewCodec(m.psk)
		if err != nil {
			_ = c.Close()
			_ = m.closeSocketsLocked()
			return nil, 0, err
		}

		ps := &pathState{name: def.Name, id: uint8(i), src: def.SourceAddr, conn: c, codec: codec}
		if m.probers != nil {
			ps.prober = m.probers[i]
			// Reconcile the DATA-frame path-id to the prober's IMMUTABLE stamp rather than
			// to the slice index i: after a runtime RemovePath the survivor keeps its
			// original (higher) stamp, so index-based numbering would renumber a live path
			// AND diverge its DATA id from its PROBE stamp. Taking id from the prober keeps
			// DATA and PROBE agreeing on the wire and a survivor's id stable across a reopen.
			ps.id = ps.prober.PathID()
			boundProbers = append(boundProbers, ps.prober)
		}
		switch {
		case def.DestAddr.IsValid():
			ps.setRemote(def.DestAddr)
		case m.hasDefaultRemote:
			ps.setRemote(m.defaultRemote)
		}
		m.paths = append(m.paths, ps)
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
	m.sendCodec = sendCodec

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
	if m.probers != nil && dynOK {
		health := make([]sched.PathHealth, len(boundProbers))
		for i, pr := range boundProbers {
			health[i] = pr
		}
		if err := dyn.SetPaths(health); err != nil {
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
	if m.fecSend != nil {
		m.readersWG.Add(1)
		go m.fecTickLoop(m.fecCfg.Deadline, m.recvClosed)
	}
	return []ReceiveFunc{m.newReceiveFunc(m.deliverSignal, m.recvClosed)}, actualPort, nil
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
func (m *Multipath) readLoop(ps *pathState, deliver chan<- struct{}) {
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
		m.handleInbound(ps, readBuf[:n], srcAP)
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
	probers := make([]*telemetry.Prober, 0, len(m.paths))
	for _, ps := range m.paths {
		if ps.prober != nil {
			probers = append(probers, ps.prober)
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
	m.scheduler.Recompute()
}

// newReceiveFunc returns the SINGLE engine-facing ReceiveFunc: it drains the shared
// resequencer in outer-seq order and hands each inner datagram up under the one
// virtual endpoint. Because every path's reader feeds this one drainer, a path added
// or removed at runtime needs no change to the engine's receive set. When nothing is
// ready it parks until a reader pokes deliver, until Close closes closed, or until a
// short poll elapses — the poll guarantees a head-of-line-blocked run still makes
// timeout progress even if the last live path fell silent right after buffering it.
// A single drainer also delivers with ZERO added reorder (only it calls Pop), which
// is stricter than T12's per-path receivers.
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
	return func(packets [][]byte, sizes []int, eps []Endpoint) (int, error) {
		for {
			// Deliver any in-order resequenced DATA. The item carries the outer source
			// of the frame that produced it, so the virtual endpoint pins correctly even
			// when the frame was buffered and released out of arrival order.
			if it, ok := m.resequencer.Load().Pop(); ok {
				if len(it.Payload) > len(packets[0]) {
					continue // oversize inner datagram: drop, keep draining
				}
				sizes[0] = copy(packets[0], it.Payload)
				eps[0] = m.virtualEndpoint(it.Src)
				return 1, nil
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

// handleInbound decodes one received outer datagram and dispatches it by kind. It
// is the single per-frame receive action, factored out of readLoop so the probe
// transport is exercisable without spinning receive goroutines. Delivery up the WG
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
func (m *Multipath) handleInbound(ps *pathState, raw []byte, srcAP netip.AddrPort) {
	fr, err := ps.codec.Decode(raw)
	if err != nil {
		return // drop malformed / PSK-mismatched outer frames
	}
	switch f := fr.(type) {
	case frame.Data:
		// Decode already returned a fresh copy of the payload (it aliases nothing
		// else), so the resequencer may take ownership of it directly.
		rq := m.resequencer.Load()
		if fr := m.fecRecv.Load(); fr != nil {
			// FEC on (T24): offer the data shard to the decoder BEFORE resequencing so a
			// later parity frame can reconstruct any group-mate lost in transit, then
			// deliver THIS received frame in its own right (the decoder never echoes a
			// directly-received data shard back). The shard's coded bytes are
			// OuterSeq || Payload — the same bytes the sender coded parity over.
			shard := fec.DataShard{Group: fec.GroupID(f.FECGroup), Index: int(f.FECIndex), Payload: fecShardPayload(f.OuterSeq, f.Payload)}
			recovered, _ := fr.offer(shard)
			rq.Observe(f.OuterSeq, f.Payload, srcAP)
			// Residual-loss accounting (T29): this outer-seq was natively delivered, so mark
			// it present in the post-recovery loss estimator. A seq never marked here nor via
			// a reconstruction below is loss that FEC did not mask.
			if fr.connLoss != nil {
				fr.connLoss.Observe(f.OuterSeq)
			}
			m.observeRecovered(fr, rq, recovered, srcAP)
		} else {
			rq.Observe(f.OuterSeq, f.Payload, srcAP)
		}
	case frame.Parity:
		// PARITY feeds the FEC decoder (T24); a group that has now accumulated enough
		// shards reconstructs its missing data frames, each resequenced at its ORIGINAL
		// outer-seq (carried in the recovered shard's coded bytes) so recovery composes
		// with T18 exactly like a natively-received frame. With FEC off, PARITY is
		// dropped (the pre-T24 behaviour).
		if fr := m.fecRecv.Load(); fr != nil {
			shard := fec.ParityShard{Group: fec.GroupID(f.FECGroup), Index: int(f.ParityIndex), DataCount: int(f.DataCount), Payload: f.Payload}
			recovered, _ := fr.offer(shard)
			m.observeRecovered(fr, m.resequencer.Load(), recovered, srcAP)
		}
	case frame.Probe:
		// Authenticated (the PROBE MAC verified in Decode): learn the return remote
		// from it, below the engine's virtual endpoint (no roaming churn).
		ps.setRemote(srcAP)
		if f.IsEcho {
			if ps.prober != nil {
				// A replay/forgery/wrong-path echo is rejected inside HandleEcho and
				// leaves liveness untouched; the error is a per-frame drop, not fatal.
				// ps.prober is the path's OWN immutable prober — never a lookup into a
				// dynamically-mutated slice — so runtime add/remove cannot race this.
				_ = ps.prober.HandleEcho(raw)
			}
			return
		}
		if m.reflector != nil {
			if echo, rerr := m.reflector.Reflect(raw); rerr == nil {
				// UDP writes are goroutine-safe, so this receive-goroutine reflection
				// races no in-flight Send on the same socket.
				_, _ = ps.conn.WriteToUDPAddrPort(echo, srcAP)
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

// virtualEndpoint returns the single stable endpoint the engine holds for the
// peer. On the concentrator (which has no configured endpoint) its destination
// is pinned ONCE to the first learned source; thereafter every path returns the
// identical pointer so the engine sees one peer, never per-packet churn.
//
// Hot-path note: the destination, once pinned, never changes, and it is published
// through an atomic.Pointer (see udpEndpoint). So the common case takes a
// lock-free fast path — every received datagram would otherwise contend m.mu with
// in-flight Sends. The mutex is acquired only to pin the FIRST learned source.
func (m *Multipath) virtualEndpoint(learned netip.AddrPort) Endpoint {
	if m.virt.dstValid() {
		return m.virt
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.virt.dstValid() {
		m.virt.setDst(learned)
	}
	return m.virt
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
	if _, ok := ep.(*udpEndpoint); !ok {
		return conn.ErrWrongEndpointType
	}

	// Classify the batch by inner WireGuard message type (defect D22): a handshake or
	// keepalive is passed to the scheduler as ClassControl so a pacing scheduler exempts
	// it from the per-path data token buckets and bulk overload cannot shed it and starve
	// rekey. The classifier is parameterized by the tunnel's Amnezia profile, so it works
	// under advanced-security obfuscation (custom magic headers + handshake junk), not
	// only vanilla WireGuard. It reads only the (possibly junk-shifted) type word off the
	// lock.
	class := m.classify.classifyBatch(bufs)

	m.mu.Lock()
	if len(m.paths) == 0 {
		m.mu.Unlock()
		return errClosed
	}
	idx := m.scheduler.Pick(class)
	if idx == sched.PickPaced {
		// The scheduler shed this datagram for pacing while paths are healthy: drop it
		// (same as no-path), but surface a DISTINCT error so the diagnostic is not
		// conflated with a total outage. The rate-limited log is emitted at the source
		// (the scheduler), so no per-drop logging happens here on the hot path.
		m.mu.Unlock()
		return errPacerShedding
	}
	if idx < 0 || idx >= len(m.paths) {
		m.mu.Unlock()
		return errNoHealthyPath
	}
	ps := m.paths[idx]
	remote, ok := ps.getRemote()
	if !ok {
		m.mu.Unlock()
		return errNoHealthyPath
	}
	c := ps.conn
	wires := make([]fecWire, 0, len(bufs))
	for _, b := range bufs {
		seq := m.outerSeq.Add(1)
		if m.fecSend != nil {
			// FEC on (T24): admit the inner datagram (coded as seq || inner) to the group
			// encoder. The returned data shard rides a normal DATA frame carrying its FEC
			// group + shard index; when this admission FILLS the group the encoder returns
			// the group's parity shards, emitted here as KindParity frames on the SAME
			// chosen path. Spreading parity onto a DIFFERENT path than its data (so one
			// path outage cannot lose both) is a documented future refinement, deliberately
			// NOT implemented here (see the T24 design notes).
			ds, parity, err := m.fecSend.enc.Admit(fecShardPayload(seq, b))
			if err != nil {
				m.mu.Unlock()
				return err
			}
			wire, err := m.sendCodec.Encode(nil, frame.Data{OuterSeq: seq, PathID: ps.id, FECGroup: uint32(ds.Group), FECIndex: uint8(ds.Index), Payload: b})
			if err != nil {
				m.mu.Unlock()
				return err
			}
			wires = append(wires, fecWire{b: wire})
			for _, par := range parity {
				pw, err := m.encodeParityLocked(par, ps.id)
				if err != nil {
					m.mu.Unlock()
					return err
				}
				wires = append(wires, fecWire{b: pw, parity: true})
			}
			continue
		}
		wire, err := m.sendCodec.Encode(nil, frame.Data{OuterSeq: seq, PathID: ps.id, Payload: b})
		if err != nil {
			m.mu.Unlock()
			return err
		}
		wires = append(wires, fecWire{b: wire})
	}
	fs := m.fecSend
	m.mu.Unlock()

	for _, w := range wires {
		if _, err := c.WriteToUDPAddrPort(w.b, remote); err != nil {
			return err
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

// encodeParityLocked encodes one parity shard as a KindParity frame on the given
// path. Caller holds m.mu (the send Codec is shared and stateful).
func (m *Multipath) encodeParityLocked(par fec.ParityShard, pathID uint8) ([]byte, error) {
	return m.sendCodec.Encode(nil, frame.Parity{
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

// fecFlushDeadline closes any FEC group whose grouping deadline has elapsed and emits
// its parity on a scheduler-chosen path. It TryLocks m.mu: when the lock is contended
// (a concurrent Send/Close/AddPath) it simply skips this tick — the next tick, or the
// next size-triggered close, still emits the group's parity, and skipping preserves
// Close's invariant that no readersWG goroutine blocks on m.mu. Framing runs under the
// lock (the shared stateful send Codec); the socket writes run without it, mirroring
// Send.
func (m *Multipath) fecFlushDeadline() {
	if !m.mu.TryLock() {
		return
	}
	if m.fecSend == nil || len(m.paths) == 0 {
		m.mu.Unlock()
		return
	}
	// Adaptive drive (T29): fold a fresh loss sample into the controller and retarget the
	// encoder's per-group parity BEFORE flushing, so a group closed by this tick already
	// carries the current target. It is a no-op in fixed mode and self-throttles to the
	// probe cadence. Runs under the same m.mu the flush holds — the single serialized FEC
	// locus — so the controller (a state machine) is never touched concurrently.
	m.driveAdaptiveControllerLocked()
	parity, err := m.fecSend.enc.Tick()
	if err != nil || len(parity) == 0 {
		m.mu.Unlock()
		return
	}
	// Route the parity like data through the scheduler (a first-integration choice; see
	// Send). A shed/no-path verdict drops this group's parity — a degraded path is
	// exactly when the datapath is under pressure, and a stranded partial group's parity
	// is best-effort — so the group simply goes unprotected rather than blocking. FEC
	// parity is bulk redundancy, so it is paced as ClassData (defect D22): only WireGuard
	// control frames earn the pacing exemption.
	idx := m.scheduler.Pick(sched.ClassData)
	if idx < 0 || idx >= len(m.paths) {
		m.mu.Unlock()
		return
	}
	ps := m.paths[idx]
	remote, ok := ps.getRemote()
	if !ok {
		m.mu.Unlock()
		return
	}
	c := ps.conn
	fs := m.fecSend
	wires := make([][]byte, 0, len(parity))
	for _, par := range parity {
		pw, err := m.encodeParityLocked(par, ps.id)
		if err != nil {
			m.mu.Unlock()
			return
		}
		wires = append(wires, pw)
	}
	m.mu.Unlock()

	for _, wire := range wires {
		if _, err := c.WriteToUDPAddrPort(wire, remote); err != nil {
			return
		}
		ps.txBytes.Add(uint64(len(wire)))
		fs.parityFrames.Add(1)
		fs.parityBytes.Add(uint64(len(wire)))
	}
}

// driveAdaptiveControllerLocked folds one measured loss sample into the adaptive FEC
// controller and retargets the encoder's per-group parity (T29). Caller holds m.mu, so
// the controller — a state machine that is NOT safe for concurrent use — is driven from
// this single serialized locus (the FEC tick loop), exactly as the encoder is. It is a
// no-op in fixed-ratio mode (no controller) and self-throttles to adaptiveControlInterval
// so the controller's EWMA sees ~one sample per probe interval regardless of the tick rate.
//
// WHICH LOSS drives the controller (design decision 1): the MAX raw probe-measured loss
// (Estimate().Loss) across the currently-eligible (StateUp) paths. Parity must mask the
// loss the DATA actually experiences; under active-backup only the primary carries data
// and the max collapses to it, and under the weighted scheduler data is striped across the
// eligible set, so sizing to the worst active path is the defensible, policy-agnostic
// choice. It is deliberately the RAW per-path loss, NOT the post-recovery ConnLoss: feeding
// the masked residual back would form a control loop that under-provisions precisely because
// it is succeeding. A down/probeless path is excluded — it carries no data to protect.
func (m *Multipath) driveAdaptiveControllerLocked() {
	fs := m.fecSend
	if fs.ctrl == nil {
		return // fixed-ratio mode
	}
	now := time.Now()
	if fs.haveControlTick && now.Sub(fs.lastControlTick) < adaptiveControlInterval {
		return
	}
	fs.lastControlTick = now
	fs.haveControlTick = true

	loss, have := m.maxEligiblePathLossLocked()
	if !have {
		return // no eligible probed path this interval: hold the current target
	}
	fs.ctrl.Observe(loss)
	fs.enc.SetParity(fs.ctrl.Parity())
}

// maxEligiblePathLossLocked returns the maximum raw probe-measured loss across the paths
// that are currently StateUp and carry a prober, plus whether any such path exists. Caller
// holds m.mu. It reads each prober's own mutex (State/Estimate) under m.mu; the prober is a
// LEAF lock (it never calls back into the Bind), so this cannot invert the send-path
// m.mu→scheduler→prober order the rest of the Bind takes.
func (m *Multipath) maxEligiblePathLossLocked() (float64, bool) {
	maxLoss := 0.0
	have := false
	for _, ps := range m.paths {
		if ps.prober == nil || ps.prober.State() != telemetry.StateUp {
			continue
		}
		if l := ps.prober.Estimate().Loss; !have || l > maxLoss {
			maxLoss = l
			have = true
		}
	}
	return maxLoss, have
}

// FECSnapshot returns a consistent snapshot of the connection-scoped FEC counters
// (T24): the send-side parity overhead and the receive-side recovery outcome. It
// follows the PathSnapshots discipline — grab the send/receive state pointers under
// m.mu, then read the decoder's stats OUTSIDE m.mu (the decoder has its own mutex) —
// so the scrape never blocks an in-flight Send. All zero when FEC is disabled or the
// bind is closed.
func (m *Multipath) FECSnapshot() FECStats {
	m.mu.Lock()
	fs := m.fecSend
	fr := m.fecRecv.Load()
	m.mu.Unlock()

	var out FECStats
	if fs != nil {
		out.DataFrames = fs.dataFrames.Load()
		out.DataBytes = fs.dataBytes.Load()
		out.ParityFrames = fs.parityFrames.Load()
		out.ParityBytes = fs.parityBytes.Load()
	}
	if fr != nil {
		if fr.connLoss != nil {
			out.ResidualLoss = fr.connLoss.Loss()
		}
		// Recovered is the HONEST delivered count (frames placed ahead of the release
		// point), NOT the decoder's raw reconstruction count — a frame rebuilt after the
		// resequencer skipped its gap is reconstructed but never delivered, so counting it
		// would overstate recovery on /metrics. Unrecoverable is the decoder's repair-
		// failure count (groups evicted still incomplete).
		out.Recovered = fr.deliveredRecovered.Load()
		out.Unrecoverable = fr.stats().Unrecoverable
	}
	return out
}

// ParseEndpoint records the peer's wireguard endpoint as the default per-path
// remote and returns the single virtual endpoint. It may run before Open (the
// engine applies UAPI config before binding), so the parsed address is stashed
// and also applied to any already-open path lacking its own dest_addr.
func (m *Multipath) ParseEndpoint(s string) (Endpoint, error) {
	ap, err := netip.ParseAddrPort(s)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.defaultRemote, m.hasDefaultRemote = ap, true
	if !m.virt.dstValid() {
		m.virt.setDst(ap)
	}
	for _, ps := range m.paths {
		if _, ok := ps.getRemote(); !ok {
			ps.setRemote(ap)
		}
	}
	return m.virt, nil
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
	for _, ps := range m.paths {
		if ps.conn != nil {
			if err := ps.conn.Close(); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	m.paths = nil
	m.deferred = nil
	m.sendCodec = nil
	// Drop the FEC send/receive state (T24) so the next Open rebuilds fresh group state,
	// mirroring the send Codec and resequencer. The deadline-tick goroutine that reads
	// m.fecSend is stopped by Close (recvClosed) and joined via readersWG before state is
	// cleared, and it captured its own fecSender pointer, so niling here never races it.
	m.fecSend = nil
	m.fecRecv.Store(nil)
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
	dyn, ok := m.scheduler.(sched.DynamicScheduler)
	if !ok {
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
	// Mint the prober up front so its id-stamp is consumed whether the path binds now
	// or is deferred: a deferred path keeps its prober (Down, excluded from the
	// scheduler) so a later bind (T55 / a Close→Open) reuses the SAME stamp.
	prober := m.newProber(def.Name, id)

	laddr := &net.UDPAddr{IP: net.IP(def.SourceAddr.AsSlice()), Port: int(m.openPort)}
	c, err := net.ListenUDP("udp", laddr)
	if err != nil {
		if errors.Is(err, syscall.EADDRNOTAVAIL) {
			// Symmetric with Open's tolerant bind: a well-formed-but-not-yet-assignable
			// source_addr is DEFERRED, not fatal. Record it in the durable membership and
			// the deferred set (Down, absent from the scheduler) and return success, so a
			// reload that introduces such a path does not fail the entire reload. AddPath
			// already requires the probe transport + a DynamicScheduler (checked above),
			// which is exactly the Down model Open's tolerance needs.
			m.defs = append(m.defs, def)
			m.probers = append(m.probers, prober)
			m.deferred = append(m.deferred, deferredPath{def: def, prober: prober})
			m.nextPathID++
			return nil
		}
		return fmt.Errorf("bind: add path %q on %s: %w", def.Name, def.SourceAddr, err)
	}
	_ = c.SetReadBuffer(socketRecvBuffer)
	codec, err := frame.NewCodec(m.psk)
	if err != nil {
		_ = c.Close()
		return err
	}

	ps := &pathState{name: def.Name, id: id, src: def.SourceAddr, conn: c, codec: codec, prober: prober}
	switch {
	case def.DestAddr.IsValid():
		ps.setRemote(def.DestAddr)
	case m.hasDefaultRemote:
		ps.setRemote(m.defaultRemote)
	}

	// Append to the path slice, then admit the prober to the scheduler as the new
	// tail; both are index-aligned, so the scheduler's returned index must equal the
	// new path's slice index. A mismatch would mis-route datagrams, so fail loudly.
	m.paths = append(m.paths, ps)
	schedIdx, err := dyn.AddPath(ps.prober)
	if err != nil {
		m.paths = m.paths[:len(m.paths)-1]
		_ = c.Close()
		return err
	}
	if schedIdx != len(m.paths)-1 {
		// Defensive, currently unreachable (AddPath appends at the scheduler tail):
		// roll the admission back COMPLETELY — drop the just-added scheduler entry,
		// pop the appended path, and close its socket — mirroring the sibling error
		// branch above so a skew never leaks a half-admitted path or an orphaned
		// scheduler entry.
		bindIdx := len(m.paths) - 1
		_ = dyn.RemovePath(schedIdx)
		m.paths = m.paths[:len(m.paths)-1]
		_ = c.Close()
		return fmt.Errorf("bind: scheduler/path index skew after add: sched=%d bind=%d", schedIdx, bindIdx)
	}
	// Durable membership: record the def and prober so a subsequent Close→Open
	// rebuilds THIS path (Open reconstructs m.paths from m.defs and re-pins the
	// scheduler from m.probers). Kept index-aligned with m.paths under m.mu, so the
	// runtime add survives a reopen instead of vanishing — or leaving a frozen
	// scheduler health entry with no path to Tick it (total-egress-outage defect).
	m.defs = append(m.defs, def)
	m.probers = append(m.probers, ps.prober)
	m.nextPathID++

	m.readersWG.Add(1)
	go m.readLoop(ps, m.deliverSignal)
	return nil
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
	dyn, ok := m.scheduler.(sched.DynamicScheduler)
	if !ok {
		return errors.New("bind: scheduler does not support runtime path membership")
	}
	// Locate the path in the DURABLE membership by identity. m.defs/m.probers are
	// full-length (bound + deferred); m.paths is the bound subset and may be shorter.
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
	// Is it a LIVE (bound) path? If so it owns a socket + scheduler entry to retire.
	liveIdx := -1
	for i, ps := range m.paths {
		if ps.name == name {
			liveIdx = i
			break
		}
	}
	if liveIdx < 0 {
		// A DEFERRED path: no transport to tear down — just drop it from the durable
		// membership and the deferred set so it does not resurrect on the next Open.
		m.removeDurableLocked(defIdx, name)
		return nil
	}
	// Removing a bound path: refuse if it is the LAST live path (that tears down the
	// virtual endpoint the engine holds). A deferred path carries no transport, so it
	// does not count toward "at least one live path must remain".
	if len(m.paths) == 1 {
		return fmt.Errorf("bind: refusing to remove path %q: at least one live path must remain", name)
	}
	ps := m.paths[liveIdx]
	if err := dyn.RemovePath(liveIdx); err != nil {
		return err
	}
	m.paths = append(m.paths[:liveIdx], m.paths[liveIdx+1:]...)
	m.removeDurableLocked(defIdx, name)
	// Closing the socket unblocks and retires the path's reader; it is NOT waited on
	// here (it never touches the path slice, and any last in-flight frame it Observes
	// is delivered normally), so a removal never blocks the caller behind a read.
	return ps.conn.Close()
}

// removeDurableLocked drops the path named name from the durable membership: m.defs
// and m.probers at defIdx (kept index-aligned with each other), and the deferred set
// by name (a no-op when the path was bound rather than deferred). Caller holds m.mu.
// Keying the durable splice on defIdx (a name lookup) rather than the m.paths index is
// what keeps m.defs/m.probers correct once a tolerant Open has made them longer than
// m.paths — so a subsequent Close→Open rebuilds exactly the surviving membership and
// neither resurrects the removed path nor loses a deferred one.
func (m *Multipath) removeDurableLocked(defIdx int, name string) {
	m.defs = append(m.defs[:defIdx], m.defs[defIdx+1:]...)
	if m.probers != nil {
		m.probers = append(m.probers[:defIdx], m.probers[defIdx+1:]...)
	}
	for i := range m.deferred {
		if m.deferred[i].def.Name == name {
			m.deferred = append(m.deferred[:i], m.deferred[i+1:]...)
			break
		}
	}
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
}

// PathSnapshots returns a consistent per-path traffic+telemetry snapshot for the
// currently-active paths, in priority order — the read side the metrics exposition
// (T23) scrapes. Concurrency: it acquires m.mu ONLY to copy out each path's name, its
// two atomic byte counters, and its prober pointer, then RELEASES m.mu BEFORE calling
// the (independently-synchronized) prober's Estimate()/State(). This mirrors
// tickLivenessFromReceive's discipline — the lock is held for a bounded, syscall-free,
// O(paths) copy and never across a prober call or a log write — so the scrape never
// blocks an in-flight Send behind the prober's own mutex, and the lock-free send/
// receive byte-counter Adds are undisturbed (they take no lock at all). The counters
// are read as a point-in-time atomic Load: tx and rx of one path may reflect Adds a
// nanosecond apart, which is immaterial to a cumulative-counter exposition.
func (m *Multipath) PathSnapshots() []PathTraffic {
	type ref struct {
		name   string
		tx, rx uint64
		prober *telemetry.Prober
	}
	m.mu.Lock()
	refs := make([]ref, len(m.paths))
	for i, ps := range m.paths {
		refs[i] = ref{name: ps.name, tx: ps.txBytes.Load(), rx: ps.rxBytes.Load(), prober: ps.prober}
	}
	m.mu.Unlock()

	out := make([]PathTraffic, len(refs))
	for i, r := range refs {
		pt := PathTraffic{Name: r.name, TxBytes: r.tx, RxBytes: r.rx}
		if r.prober != nil {
			pt.Estimate = r.prober.Estimate()
			pt.State = r.prober.State()
		}
		out[i] = pt
	}
	return out
}

// SetMark is a no-op for T12: per-path SO_MARK is a scheduler concern (T15), and
// the engine only calls SetMark when a fwmark is configured, which wanbond does
// not set.
func (m *Multipath) SetMark(uint32) error { return nil }

// BatchSize is the max number of datagrams passed to a ReceiveFunc / Send.
func (m *Multipath) BatchSize() int { return multipathBatchSize }
