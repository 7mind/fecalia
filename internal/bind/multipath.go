package bind

import (
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/amnezia-vpn/amneziawg-go/conn"

	"github.com/7mind/wanbond/internal/config"
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

	mu        sync.Mutex
	remote    netip.AddrPort
	hasRemote bool
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
	// reflector answers inbound peer probes (IsEcho=false) with an authenticated
	// echo. A single Reflector serves every path (its anti-replay is PathID-keyed)
	// and it is internally synchronized, so the per-path receive goroutines share it.
	reflector *telemetry.Reflector

	mu        sync.Mutex
	paths     []*pathState
	sendCodec *frame.Codec
	virt      *udpEndpoint
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

	// Receive fan-in (T30). To let a path be added at runtime WITHOUT the engine
	// spawning a new receive goroutine (it only builds its receive goroutines once,
	// from the ReceiveFuncs Open returns), the per-path socket reads are decoupled
	// from the engine-facing delivery: the Bind owns one readLoop goroutine PER path
	// (tracked by readersWG) that reads its socket and feeds the shared resequencer,
	// and Open returns a SINGLE engine-facing ReceiveFunc that drains the resequencer
	// in order. A reader pokes deliverSignal after each datagram so the drainer wakes;
	// recvClosed is closed by Close to release both the drainer and any reader parked
	// on a delivery. openPort mirrors Open's bind port so a runtime-added path binds
	// consistently; nextPathID hands out stable, monotonically-increasing path-ids
	// (never reused within an Open span, so a surviving path is never renumbered and a
	// freed id can never collide with the peer's per-path reflector state).
	deliverSignal chan struct{}
	recvClosed    chan struct{}
	readersWG     sync.WaitGroup
	openPort      uint16
	nextPathID    uint16
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
// prober for a newly-admitted path; it must be non-nil to allow AddPath and, for
// consistency, should be paired with a non-nil probers (boot-time set). Pass nil to
// forbid runtime path addition.
func NewMultipath(paths []config.Path, psk config.Key, scheduler sched.Scheduler, probers []*telemetry.Prober, newProber ProberFactory) (*Multipath, error) {
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
	if probers != nil && len(probers) != len(paths) {
		return nil, fmt.Errorf("bind: probers must have one entry per path (got %d, want %d)", len(probers), len(paths))
	}
	for i, pr := range probers {
		if pr == nil {
			return nil, fmt.Errorf("bind: prober %d is nil", i)
		}
	}
	return &Multipath{
		psk:       psk,
		defs:      append([]config.Path(nil), paths...),
		scheduler: scheduler,
		probers:   probers,
		newProber: newProber,
		reflector: telemetry.NewReflector(psk, rand.Reader),
		virt:      &udpEndpoint{},
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

	actualPort := port
	for i := range m.defs {
		def := m.defs[i]
		// Device-bind this path when selectDeviceBinds proved it safe (so a
		// mid-session source-address change / T16 re-roam does not break the socket),
		// otherwise pin the specific source IP. See selectDeviceBinds / listenPath.
		c, err := listenPath(def.SourceAddr, port, bindDevs[i])
		if err != nil {
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
		}
		switch {
		case def.DestAddr.IsValid():
			ps.setRemote(def.DestAddr)
		case m.hasDefaultRemote:
			ps.setRemote(m.defaultRemote)
		}
		m.paths = append(m.paths, ps)
		if i == 0 {
			actualPort = uint16(c.LocalAddr().(*net.UDPAddr).Port)
		}
	}

	// Path-ids 0..N-1 are assigned above; runtime-added paths continue from N and
	// are never reused within this Open span, so a surviving path is never renumbered.
	m.openPort = port
	m.nextPathID = uint16(len(m.paths))
	m.sendCodec = sendCodec

	// Stand up the receive fan-in: one Bind-owned reader per path feeding the shared
	// resequencer, and a single engine-facing drainer. Both channels are recreated
	// per Open so a Close→Open cycle starts clean.
	m.deliverSignal = make(chan struct{}, 1)
	m.recvClosed = make(chan struct{})
	for _, ps := range m.paths {
		m.readersWG.Add(1)
		go m.readLoop(ps, m.deliverSignal)
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
		m.handleInbound(ps, readBuf[:n], srcAP)
		// Non-blocking poke: a single buffered slot coalesces bursts; the drainer
		// re-checks the resequencer on every wake, so a coalesced poke loses nothing.
		select {
		case deliver <- struct{}{}:
		default:
		}
	}
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
			select {
			case <-deliver:
			case <-time.After(resequencerTimeout):
			case <-closed:
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
		m.resequencer.Load().Observe(f.OuterSeq, f.Payload, srcAP)
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
		// PARITY/CONTROL are not delivered up this path (P2/P3).
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

	m.mu.Lock()
	if len(m.paths) == 0 {
		m.mu.Unlock()
		return errClosed
	}
	idx := m.scheduler.Pick()
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
	wires := make([][]byte, 0, len(bufs))
	for _, b := range bufs {
		seq := m.outerSeq.Add(1)
		wire, err := m.sendCodec.Encode(nil, frame.Data{OuterSeq: seq, PathID: ps.id, Payload: b})
		if err != nil {
			m.mu.Unlock()
			return err
		}
		wires = append(wires, wire)
	}
	m.mu.Unlock()

	for _, wire := range wires {
		if _, err := c.WriteToUDPAddrPort(wire, remote); err != nil {
			return err
		}
	}
	return nil
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
	m.sendCodec = nil
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
// Path-ids are handed out monotonically and never reused within an Open span, so a
// surviving path is never renumbered; the id space (uint8) is exhausted only after
// 256 admissions in one Open span, which fails fast rather than reusing an id and
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
	for _, ps := range m.paths {
		if ps.name == def.Name {
			return fmt.Errorf("bind: add path %q: a path with that name is already active", def.Name)
		}
	}
	if m.nextPathID > 255 {
		return fmt.Errorf("bind: add path %q: path-id space exhausted (256 admissions this Open span)", def.Name)
	}
	id := uint8(m.nextPathID)

	laddr := &net.UDPAddr{IP: net.IP(def.SourceAddr.AsSlice()), Port: int(m.openPort)}
	c, err := net.ListenUDP("udp", laddr)
	if err != nil {
		return fmt.Errorf("bind: add path %q on %s: %w", def.Name, def.SourceAddr, err)
	}
	_ = c.SetReadBuffer(socketRecvBuffer)
	codec, err := frame.NewCodec(m.psk)
	if err != nil {
		_ = c.Close()
		return err
	}

	ps := &pathState{name: def.Name, id: id, src: def.SourceAddr, conn: c, codec: codec, prober: m.newProber(def.Name, id)}
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
		return fmt.Errorf("bind: scheduler/path index skew after add: sched=%d bind=%d", schedIdx, len(m.paths)-1)
	}
	m.nextPathID++

	m.readersWG.Add(1)
	go m.readLoop(ps, m.deliverSignal)
	return nil
}

// RemovePath drains and closes the named path at runtime (T30). It drops the path
// from the scheduler FIRST (so no further datagram is scheduled onto it), unlinks it
// from the path slice, and closes its socket — which retires its Bind-owned reader.
// All under m.mu, so the two structures stay index-aligned for Send. In-flight state
// is preserved: frames the path already pushed into the resequencer stay queued and
// are delivered in outer-seq order (T18 resequencing is connection-global, keyed on
// outer-seq, NOT per-path, so a removal never resets it), the surviving paths and
// their scheduling are untouched, and the single virtual endpoint / WG session is
// undisturbed. The last remaining path cannot be removed (that would tear down the
// virtual endpoint the engine holds).
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
	if len(m.paths) == 1 {
		return fmt.Errorf("bind: refusing to remove path %q: at least one path must remain", name)
	}
	idx := -1
	for i, ps := range m.paths {
		if ps.name == name {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("bind: remove path %q: no such active path", name)
	}
	ps := m.paths[idx]
	if err := dyn.RemovePath(idx); err != nil {
		return err
	}
	m.paths = append(m.paths[:idx], m.paths[idx+1:]...)
	// Closing the socket unblocks and retires the path's reader; it is NOT waited on
	// here (it never touches the path slice, and any last in-flight frame it Observes
	// is delivered normally), so a removal never blocks the caller behind a read.
	return ps.conn.Close()
}

// PathNames returns the names of the currently-active paths, in priority order. The
// device's config reload diffs the desired path set against this to decide what to
// add or remove.
func (m *Multipath) PathNames() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	names := make([]string, len(m.paths))
	for i, ps := range m.paths {
		names[i] = ps.name
	}
	return names
}

// SetMark is a no-op for T12: per-path SO_MARK is a scheduler concern (T15), and
// the engine only calls SetMark when a fwmark is configured, which wanbond does
// not set.
func (m *Multipath) SetMark(uint32) error { return nil }

// BatchSize is the max number of datagrams passed to a ReceiveFunc / Send.
func (m *Multipath) BatchSize() int { return multipathBatchSize }
