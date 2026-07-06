package bind

import (
	"net"
	"net/netip"
	"sync"

	"github.com/amnezia-vpn/amneziawg-go/conn"
)

// The type aliases below isolate the embedded WireGuard engine's conn package to
// this one file. Swapping amneziawg-go for upstream wireguard-go (the API-drift
// hedge) touches only this import and these aliases — the conn.Bind / conn.Endpoint
// contracts are byte-identical between the two forks.
type (
	// Bind is the transport the WireGuard device drives; wanbond's bonding logic
	// lives in implementations of it.
	Bind = conn.Bind
	// Endpoint identifies a peer's transport address.
	Endpoint = conn.Endpoint
	// ReceiveFunc is a packet-receive callback returned by Bind.Open.
	ReceiveFunc = conn.ReceiveFunc
)

// batchSize is the number of packets a ReceiveFunc / Send handles per call. The
// pass-through bind is deliberately one-at-a-time; the multipath bind (P1) may
// raise this.
const batchSize = 1

// Passthrough is the P0 trivial single-socket Bind: one UDP socket, no batching,
// no GSO, no bonding logic. It is implemented directly over net.UDPConn rather
// than delegating to the engine's default bind, whose recvmmsg/GSO fast path is
// unnecessary here and brittle in restricted environments. The multipath Bind
// (P1) replaces it behind this same interface while the device wiring stays put.
type Passthrough struct {
	mu   sync.Mutex
	conn *net.UDPConn
}

// compile-time proof that Passthrough satisfies the engine's Bind contract.
var _ Bind = (*Passthrough)(nil)

// NewPassthrough returns a closed pass-through Bind; call Open to bind a socket.
func NewPassthrough() *Passthrough { return &Passthrough{} }

// Open binds a dual-stack UDP socket on port (0 = random) and returns a single
// receive callback plus the actual bound port.
func (p *Passthrough) Open(port uint16) ([]ReceiveFunc, uint16, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.conn != nil {
		return nil, 0, conn.ErrBindAlreadyOpen
	}
	c, err := net.ListenUDP("udp", &net.UDPAddr{Port: int(port)})
	if err != nil {
		return nil, 0, err
	}
	p.conn = c
	actual := uint16(c.LocalAddr().(*net.UDPAddr).Port)
	return []ReceiveFunc{p.receive}, actual, nil
}

// receive reads one datagram into packets[0], recording its size and source
// endpoint. It blocks until a packet arrives, the deadline elapses, or the bind
// is closed (returning net.ErrClosed via the underlying conn).
func (p *Passthrough) receive(packets [][]byte, sizes []int, eps []Endpoint) (int, error) {
	p.mu.Lock()
	c := p.conn
	p.mu.Unlock()
	if c == nil {
		return 0, net.ErrClosed
	}
	n, src, err := c.ReadFromUDPAddrPort(packets[0])
	if err != nil {
		return 0, err
	}
	sizes[0] = n
	eps[0] = &udpEndpoint{dst: src}
	return 1, nil
}

// Close closes the underlying socket; outstanding receive calls return an error.
func (p *Passthrough) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.conn == nil {
		return nil
	}
	err := p.conn.Close()
	p.conn = nil
	return err
}

// SetMark is a no-op for the pass-through bind (SO_MARK is a per-path concern the
// multipath bind will own).
func (p *Passthrough) SetMark(uint32) error { return nil }

// Send writes each buffer in bufs to ep's destination address.
func (p *Passthrough) Send(bufs [][]byte, ep Endpoint) error {
	e, ok := ep.(*udpEndpoint)
	if !ok {
		return conn.ErrWrongEndpointType
	}
	p.mu.Lock()
	c := p.conn
	p.mu.Unlock()
	if c == nil {
		return net.ErrClosed
	}
	for _, b := range bufs {
		if _, err := c.WriteToUDPAddrPort(b, e.dst); err != nil {
			return err
		}
	}
	return nil
}

// ParseEndpoint builds an Endpoint from an "ip:port" string.
func (p *Passthrough) ParseEndpoint(s string) (Endpoint, error) {
	ap, err := netip.ParseAddrPort(s)
	if err != nil {
		return nil, err
	}
	return &udpEndpoint{dst: ap}, nil
}

// BatchSize is the max number of packets passed to a ReceiveFunc / Send.
func (p *Passthrough) BatchSize() int { return batchSize }

// udpEndpoint is a conn.Endpoint over a destination AddrPort with an optional
// learned source IP (roaming). It is the single endpoint type the pass-through
// bind produces and accepts.
type udpEndpoint struct {
	dst netip.AddrPort
	src netip.Addr
}

func (e *udpEndpoint) ClearSrc()           { e.src = netip.Addr{} }
func (e *udpEndpoint) DstToString() string { return e.dst.String() }
func (e *udpEndpoint) DstIP() netip.Addr   { return e.dst.Addr() }
func (e *udpEndpoint) SrcIP() netip.Addr   { return e.src }

func (e *udpEndpoint) SrcToString() string {
	if e.src.IsValid() {
		return e.src.String()
	}
	return ""
}

// DstToBytes serializes the destination as address bytes followed by the
// little-endian port, matching the engine's expectation for mac2 cookies.
func (e *udpEndpoint) DstToBytes() []byte {
	b, _ := e.dst.Addr().MarshalBinary()
	port := e.dst.Port()
	return append(b, byte(port), byte(port>>8))
}
