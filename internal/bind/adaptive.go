package bind

import (
	"errors"
	"net/netip"
	"sync"
	"time"

	"github.com/7mind/wanbond/internal/bond"
	"github.com/7mind/wanbond/internal/frame"
	"github.com/7mind/wanbond/internal/reseq"
)

const adaptiveReorderHold = 300 * time.Millisecond

type adaptiveRoute struct {
	path   *peerPathState
	remote netip.AddrPort
}

type adaptivePeer struct {
	mu          sync.Mutex
	owner       *peerState
	transport   *bond.Transport
	codec       *frame.Codec
	routes      map[bond.PathID]adaptiveRoute
	wake        chan struct{}
	closed      bool
	interactive chan reseq.Item
	notify      func()
}

// EnableAdaptive must be called before Open. The transport owns its pacing and
// recovery; composing it with the legacy FEC owner or shaper is an invariant error.
func (m *Multipath) EnableAdaptive() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.paths) != 0 || m.fecCfg != nil || m.shaperConfigs != nil {
		return errors.New("bind: adaptive requires a closed bind without legacy FEC or shapers")
	}
	m.adaptiveEnabled = true
	return nil
}

func (m *Multipath) openAdaptivePeer(peer *peerState) error {
	codec, err := peer.newCodec()
	if err != nil {
		return err
	}
	a := &adaptivePeer{
		owner:       peer,
		transport:   bond.New(bond.Epoch{Boot: peer.probers[0].SessionID(), Generation: m.openGeneration.Load()}),
		codec:       codec,
		routes:      make(map[bond.PathID]adaptiveRoute),
		wake:        make(chan struct{}, 1),
		interactive: make(chan reseq.Item, 256),
		notify:      resequencerNotifier(m.deliverSignal),
	}
	peer.adaptive.Store(a)
	if rq := peer.resequencer.Load(); rq != nil {
		rq.SetMultiPathExpected(true)
		rq.SetHoldBound(adaptiveReorderHold)
	}
	done := m.recvClosed
	m.readersWG.Add(1)
	go func() {
		defer m.readersWG.Done()
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		defer func() {
			a.mu.Lock()
			a.closed = true
			a.mu.Unlock()
			peer.adaptive.CompareAndSwap(a, nil)
		}()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
			case <-a.wake:
			}
			a.flush(time.Now())
		}
	}()
	return nil
}

func (a *adaptivePeer) signal() {
	select {
	case a.wake <- struct{}{}:
	default:
	}
}

func (a *adaptivePeer) hello(path uint8) []byte {
	return bond.Hello(a.transport.Epoch(), path)
}

func (a *adaptivePeer) learn(ps *peerPathState, remote netip.AddrPort, payload []byte, adopted bool) {
	epoch, remotePath, ok := bond.ParseHello(payload)
	if !ok || epoch.Boot == a.transport.Epoch().Boot {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return
	}
	ps.writeMu.Lock()
	retired := ps.writesClosed
	ps.writeMu.Unlock()
	if retired {
		return
	}
	changed := a.transport.SetRemote(epoch, adopted)
	if a.transport.Remote() != epoch {
		return
	}
	if changed {
		a.routes = make(map[bond.PathID]adaptiveRoute)
		for len(a.interactive) > 0 {
			select {
			case <-a.interactive:
			default:
			}
		}
		if rq := a.owner.resequencer.Load(); rq != nil {
			rq.RebaselineAt(1)
		}
	}
	id := bond.PathID(uint16(ps.id)<<8 | uint16(remotePath))
	a.routes[id] = adaptiveRoute{ps, remote}
	rtt := time.Duration(0)
	if ps.prober != nil {
		rtt = ps.prober.Estimate().RTT
	}
	a.transport.Path(id, bond.PathID(uint16(remotePath)<<8|uint16(ps.id)), rtt, time.Now())
	a.signal()
}

func (a *adaptivePeer) enqueue(bufs [][]byte) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return errClosed
	}
	now := time.Now()
	for _, b := range bufs {
		if err := a.transport.Enqueue(b, now); err != nil {
			return err
		}
	}
	a.signal()
	return nil
}

func (a *adaptivePeer) receive(ps *peerPathState, remote netip.AddrPort, f frame.Control) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return
	}
	var id bond.PathID
	found := false
	for candidate, route := range a.routes {
		if route.path == ps && route.remote == remote {
			id, found = candidate, true
			break
		}
	}
	if !found {
		return
	}
	deliveries, err := a.transport.Receive(id, f, time.Now())
	if err != nil {
		return
	}
	if rq := a.owner.resequencer.Load(); rq != nil {
		rq.SetMultiPathExpected(true)
		rq.SetHoldBound(adaptiveReorderHold)
		for _, d := range deliveries {
			if d.Interactive {
				select {
				case a.interactive <- reseq.Item{Payload: append([]byte(nil), d.Payload...), Src: remote}:
					a.notify()
				default:
				}
			} else {
				rq.ObserveFromPath(d.Sequence, d.Payload, remote, uint32(id))
			}
		}
	}
	a.signal()
}

func (a *adaptivePeer) popInteractive() (reseq.Item, bool) {
	select {
	case item := <-a.interactive:
		return item, true
	default:
		return reseq.Item{}, false
	}
}

func (a *adaptivePeer) flush(now time.Time) {
	type planned struct {
		route adaptiveRoute
		frame frame.Control
	}
	a.mu.Lock()
	var sends []planned
	for _, tx := range a.transport.Poll(now) {
		route, ok := a.routes[tx.Path]
		if !ok {
			continue
		}
		sends = append(sends, planned{route, tx.Frame})
	}
	a.mu.Unlock()
	for _, tx := range sends {
		route := tx.route
		raw, err := a.codec.Encode(nil, tx.frame)
		if err != nil {
			panic(err)
		}
		if _, err = route.path.writeToUDPAddrPort(raw, route.remote); err != nil {
			route.path.socketWriteErrors.Add(1)
			continue
		}
		route.path.recordOuterWrite(len(raw))
	}
}

func (a *adaptivePeer) forgetSocket(socket *sharedPathState) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for id, route := range a.routes {
		if route.path.sharedPathState == socket {
			a.transport.Disable(id)
			delete(a.routes, id)
		}
	}
}

func (a *adaptivePeer) forgetRoutes() {
	a.mu.Lock()
	defer a.mu.Unlock()
	for id := range a.routes {
		a.transport.Disable(id)
		delete(a.routes, id)
	}
}
