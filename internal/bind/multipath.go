package bind

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"

	"github.com/amnezia-vpn/amneziawg-go/conn"

	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/frame"
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

	mu        sync.Mutex
	remote    netip.AddrPort
	hasRemote bool
	healthy   bool
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
// in an outer DATA frame (own outer-seq + path-id) and picks a path; each
// per-path ReceiveFunc unwraps DATA frames and hands the inner WG datagram up
// under the shared virtual endpoint. It replaces bind.Passthrough behind the
// same conn.Bind seam (device wiring is unchanged).
//
// T12 scope: the path policy is "first healthy path with a known remote" — the
// real weighted scheduler is T15. No FEC/resequencing yet (P2/P3), but every
// DATA frame carries its outer-seq and path-id so T18/T24 can consume them.
type Multipath struct {
	psk  config.Key
	defs []config.Path

	mu        sync.Mutex
	paths     []*pathState
	sendCodec *frame.Codec
	sendBuf   []byte // reusable Encode buffer, guarded by mu
	virt      *udpEndpoint
	// defaultRemote is the fallback per-path remote (the peer's wireguard
	// endpoint) applied to any path without its own dest_addr. It may be set by
	// ParseEndpoint BEFORE Open, so it is stored here and applied at Open time.
	defaultRemote    netip.AddrPort
	hasDefaultRemote bool
	closed           bool

	outerSeq atomic.Uint64
}

// compile-time proof that Multipath satisfies the engine's Bind contract.
var _ Bind = (*Multipath)(nil)

// NewMultipath returns a closed multipath Bind over the configured paths; call
// Open to bind the per-path sockets. The PSK keys the outer DATA framing and must
// be set (config validation guarantees it).
func NewMultipath(paths []config.Path, psk config.Key) (*Multipath, error) {
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
	return &Multipath{
		psk:  psk,
		defs: append([]config.Path(nil), paths...),
		virt: &udpEndpoint{},
	}, nil
}

// Open binds one UDP socket per configured path to that path's source address on
// port (0 = random per socket), sets a large SO_RCVBUF on each, and returns one
// ReceiveFunc per path plus the first path's bound port. Each path whose config
// carries a dest_addr — or, failing that, the peer's wireguard endpoint learned
// via ParseEndpoint — starts with a known remote; the rest are learned from
// inbound traffic.
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

	fns := make([]ReceiveFunc, 0, len(m.defs))
	actualPort := port
	for i := range m.defs {
		def := m.defs[i]
		laddr := &net.UDPAddr{IP: net.IP(def.SourceAddr.AsSlice()), Port: int(port)}
		c, err := net.ListenUDP("udp", laddr)
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

		ps := &pathState{name: def.Name, id: uint8(i), src: def.SourceAddr, conn: c, codec: codec, healthy: true}
		switch {
		case def.DestAddr.IsValid():
			ps.setRemote(def.DestAddr)
		case m.hasDefaultRemote:
			ps.setRemote(m.defaultRemote)
		}
		m.paths = append(m.paths, ps)
		fns = append(fns, m.receiver(ps))
		if i == 0 {
			actualPort = uint16(c.LocalAddr().(*net.UDPAddr).Port)
		}
	}

	m.sendCodec = sendCodec
	return fns, actualPort, nil
}

// receiver returns the ReceiveFunc for one path socket. It reads one outer
// datagram, decodes it, and — for a DATA frame — learns the path's remote from
// the datagram source and delivers the inner WG datagram under the shared virtual
// endpoint. Non-DATA frames (PROBE/CONTROL/PARITY) and malformed inputs are
// dropped for T12 (their handling is P2/P3), so the loop reads again rather than
// returning a zero-length batch. The read buffer is private to this closure, so
// the per-path Codec's scratch is never shared across goroutines.
func (m *Multipath) receiver(ps *pathState) ReceiveFunc {
	readBuf := make([]byte, maxDatagram)
	return func(packets [][]byte, sizes []int, eps []Endpoint) (int, error) {
		for {
			n, srcAP, err := ps.conn.ReadFromUDPAddrPort(readBuf)
			if err != nil {
				return 0, err
			}
			fr, derr := ps.codec.Decode(readBuf[:n])
			if derr != nil {
				continue // drop malformed / PSK-mismatched outer frames
			}
			data, ok := fr.(frame.Data)
			if !ok {
				continue // T12 delivers only DATA up the stack
			}
			// Per-path endpoint bookkeeping: learn where this path's peer is, so
			// return traffic on this path goes back whence it came. This stays
			// strictly below the engine's virtual endpoint (no roaming churn).
			ps.setRemote(srcAP)

			inner := data.Payload
			if len(inner) > len(packets[0]) {
				continue // cannot fit; drop (should not happen within MTU budget)
			}
			copy(packets[0], inner)
			sizes[0] = len(inner)
			eps[0] = m.virtualEndpoint(srcAP)
			return 1, nil
		}
	}
}

// virtualEndpoint returns the single stable endpoint the engine holds for the
// peer. On the concentrator (which has no configured endpoint) its destination
// is pinned ONCE to the first learned source; thereafter every path returns the
// identical pointer so the engine sees one peer, never per-packet churn.
func (m *Multipath) virtualEndpoint(learned netip.AddrPort) Endpoint {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.virt.dstValid() {
		m.virt.setDst(learned)
	}
	return m.virt
}

// Send wraps each buffer in an outer DATA frame (fresh outer-seq + the chosen
// path's id) and writes it to that path's remote. The path policy is T12's simple
// "first healthy path with a known remote"; encoding uses the shared send Codec
// guarded by the Bind mutex.
func (m *Multipath) Send(bufs [][]byte, ep Endpoint) error {
	if _, ok := ep.(*udpEndpoint); !ok {
		return conn.ErrWrongEndpointType
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return errClosed
	}
	ps := m.pickPathLocked()
	if ps == nil {
		return errNoHealthyPath
	}
	remote, ok := ps.getRemote()
	if !ok {
		return errNoHealthyPath
	}
	for _, b := range bufs {
		seq := m.outerSeq.Add(1)
		wire, err := m.sendCodec.Encode(m.sendBuf[:0], frame.Data{OuterSeq: seq, PathID: ps.id, Payload: b})
		if err != nil {
			return err
		}
		m.sendBuf = wire
		if _, err := ps.conn.WriteToUDPAddrPort(wire, remote); err != nil {
			return err
		}
	}
	return nil
}

// pickPathLocked returns the first healthy path that has a known remote, or nil.
// Caller holds m.mu.
func (m *Multipath) pickPathLocked() *pathState {
	for _, ps := range m.paths {
		if !ps.healthy {
			continue
		}
		if _, ok := ps.getRemote(); ok {
			return ps
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

// Close closes every per-path socket; outstanding receive calls return an error.
func (m *Multipath) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return m.closeSocketsLocked()
}

// closeSocketsLocked closes all path sockets, returning the first error. Caller
// holds m.mu. Idempotent.
func (m *Multipath) closeSocketsLocked() error {
	var firstErr error
	for _, ps := range m.paths {
		if ps.conn != nil {
			if err := ps.conn.Close(); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// SetMark is a no-op for T12: per-path SO_MARK is a scheduler concern (T15), and
// the engine only calls SetMark when a fwmark is configured, which wanbond does
// not set.
func (m *Multipath) SetMark(uint32) error { return nil }

// BatchSize is the max number of datagrams passed to a ReceiveFunc / Send.
func (m *Multipath) BatchSize() int { return multipathBatchSize }
