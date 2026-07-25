package bind

import (
	"sync"
	"time"

	"github.com/7mind/wanbond/internal/telemetry"
)

const (
	conservativeRecoveryService = 250 * time.Millisecond
	recoveryRenewBefore         = 2 * conservativeRecoveryService
)

type sentRecoveryOffer struct {
	sessionID uint64
	message   telemetry.RecoveryContractMessage
	probeSeq  uint64
	challenge uint64
}

type recoveryOfferSnapshot struct {
	message telemetry.RecoveryContractMessage
	payload []byte
}

type localRecoveryOffer struct {
	recoveryOfferSnapshot
	startedAt   time.Time
	outstanding map[uint8]sentRecoveryOffer
}

type receivedRecoveryContract struct {
	message    telemetry.RecoveryContractMessage
	acceptedAt time.Time
	invalid    bool
}

// recoveryContractCoordinator is the peer-scoped owner of both directions of
// recovery-contract negotiation. It deliberately does not live on Prober:
// every path of one peer advertises and acknowledges one immutable generation.
type recoveryContractCoordinator struct {
	mu      sync.Mutex
	clock   fecOwnerClock
	session uint64
	nextID  uint64
	changed chan struct{}

	offer *localRecoveryOffer

	barrierPending bool
	barrierDue     time.Time

	haveLease   bool
	lease       telemetry.RecoveryContractMessage
	leaseUntil  time.Time
	invalidated bool

	haveReceived    bool
	receivedSession uint64
	received        receivedRecoveryContract
}

func newRecoveryContractCoordinator(session uint64, clock fecOwnerClock) *recoveryContractCoordinator {
	return &recoveryContractCoordinator{
		clock:   clock,
		session: session,
		changed: make(chan struct{}),
	}
}

func (c *recoveryContractCoordinator) setClock(clock fecOwnerClock) {
	c.mu.Lock()
	c.clock = clock
	c.mu.Unlock()
}

func (c *recoveryContractCoordinator) notifyLocked() {
	close(c.changed)
	c.changed = make(chan struct{})
}

func (c *recoveryContractCoordinator) begin(enabled bool, serviceBound time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	message := telemetry.RecoveryContractMessage{
		Type:     telemetry.RecoveryContractOffer,
		Enabled:  enabled,
		Lifetime: telemetry.RecoveryContractLifetime,
	}
	if enabled {
		message.ServiceBound = serviceBound
	}
	c.haveLease = false
	c.leaseUntil = time.Time{}
	c.invalidated = false
	return c.startOfferLocked(message, true)
}

func (c *recoveryContractCoordinator) startOfferLocked(message telemetry.RecoveryContractMessage, barrier bool) error {
	c.nextID++
	if c.nextID == 0 {
		c.nextID++
	}
	message.Type = telemetry.RecoveryContractOffer
	message.ContractID = c.nextID
	payload, err := telemetry.EncodeRecoveryContract(message)
	if err != nil {
		return err
	}
	now := c.clock.Now()
	c.offer = &localRecoveryOffer{
		recoveryOfferSnapshot: recoveryOfferSnapshot{
			message: message,
			payload: payload,
		},
		startedAt:   now,
		outstanding: make(map[uint8]sentRecoveryOffer),
	}
	if barrier {
		c.barrierPending = true
		c.barrierDue = now.Add(conservativeRecoveryService)
	}
	c.notifyLocked()
	return nil
}

func (c *recoveryContractCoordinator) disable() {
	c.mu.Lock()
	c.barrierPending = false
	c.barrierDue = time.Time{}
	c.haveLease = false
	c.leaseUntil = time.Time{}
	c.invalidated = false
	c.offer = nil
	c.notifyLocked()
	c.mu.Unlock()
}

// invalidateForTransition immediately revokes fast recovery and blocks new DATA
// while an asynchronous service-transition worker drains and rotates the offer.
// A zero barrierDue deliberately has no autonomous fallback: begin installs the
// new service snapshot and starts the explicit T fallback.
func (c *recoveryContractCoordinator) invalidateForTransition() {
	c.mu.Lock()
	c.haveLease = false
	c.leaseUntil = time.Time{}
	c.invalidated = true
	c.barrierPending = true
	c.barrierDue = time.Time{}
	c.notifyLocked()
	c.mu.Unlock()
}

func (c *recoveryContractCoordinator) refreshLocked(now time.Time) {
	if c.invalidated || c.offer == nil {
		return
	}
	if c.haveLease && !now.Before(c.leaseUntil) {
		c.haveLease = false
		c.leaseUntil = time.Time{}
	}
	if c.haveLease && c.offer.message.ContractID == c.lease.ContractID &&
		c.leaseUntil.Sub(now) <= recoveryRenewBefore {
		message := c.lease
		if err := c.startOfferLocked(message, false); err != nil {
			panic(err)
		}
		return
	}
	if !c.haveLease && !c.barrierPending &&
		c.offer.startedAt.Add(c.offer.message.Lifetime).Sub(now) <= recoveryRenewBefore {
		message := c.offer.message
		if err := c.startOfferLocked(message, false); err != nil {
			panic(err)
		}
	}
}

func (c *recoveryContractCoordinator) offerSnapshot() recoveryOfferSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.refreshLocked(c.clock.Now())
	if c.offer == nil {
		return recoveryOfferSnapshot{}
	}
	return recoveryOfferSnapshot{
		message: c.offer.message,
		payload: append([]byte(nil), c.offer.payload...),
	}
}

func (c *recoveryContractCoordinator) payload() []byte {
	return c.offerSnapshot().payload
}

func (c *recoveryContractCoordinator) recordOffer(pathID uint8, probe telemetryProbeHeader, offered recoveryOfferSnapshot) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.offer == nil || probe.sessionID != c.session ||
		offered.message != c.offer.message {
		return
	}
	c.offer.outstanding[pathID] = sentRecoveryOffer{
		sessionID: probe.sessionID,
		message:   offered.message,
		probeSeq:  probe.probeSeq,
		challenge: probe.challenge,
	}
}

type telemetryProbeHeader struct {
	sessionID uint64
	probeSeq  uint64
	challenge uint64
}

func (c *recoveryContractCoordinator) acceptACK(pathID uint8, probeSession, probeSeq uint64, payload []byte) bool {
	message, recognized, err := telemetry.DecodeRecoveryContract(payload)
	if err != nil || !recognized || message.Type != telemetry.RecoveryContractACK {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.refreshLocked(c.clock.Now())
	if c.invalidated || c.offer == nil {
		return false
	}
	sent, ok := c.offer.outstanding[pathID]
	if !ok || sent.sessionID != probeSession || sent.probeSeq != probeSeq ||
		sent.message != messageWithType(message, telemetry.RecoveryContractOffer) ||
		sent.message != c.offer.message || sent.challenge == 0 ||
		probeSession != c.session {
		return false
	}
	want := c.offer.message
	want.Type = telemetry.RecoveryContractACK
	if message != want {
		return false
	}
	until := c.offer.startedAt.Add(message.Lifetime)
	if until.Sub(c.clock.Now()) < conservativeRecoveryService {
		return false
	}
	c.haveLease = true
	c.lease = c.offer.message
	c.leaseUntil = until
	c.barrierPending = false
	c.barrierDue = time.Time{}
	c.notifyLocked()
	return true
}

func messageWithType(message telemetry.RecoveryContractMessage, messageType telemetry.RecoveryContractMessageType) telemetry.RecoveryContractMessage {
	message.Type = messageType
	return message
}

func (c *recoveryContractCoordinator) fastEligible() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.clock.Now()
	c.refreshLocked(now)
	if c.invalidated || c.barrierPending || !c.haveLease || !c.lease.Enabled {
		return false
	}
	return c.leaseUntil.Sub(now) >= conservativeRecoveryService
}

func (c *recoveryContractCoordinator) barrierActive() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.refreshLocked(c.clock.Now())
	return c.barrierPending
}

func (c *recoveryContractCoordinator) awaitDecision() error {
	for {
		c.mu.Lock()
		c.refreshLocked(c.clock.Now())
		if !c.barrierPending {
			c.mu.Unlock()
			return nil
		}
		due := c.barrierDue
		changed := c.changed
		clock := c.clock
		c.mu.Unlock()

		if due.IsZero() {
			<-changed
			continue
		}
		timer := clock.NewTimerAt(due)
		select {
		case <-changed:
			timer.Stop()
		case <-timer.C():
			c.mu.Lock()
			if c.barrierPending && !c.barrierDue.IsZero() && !c.clock.Now().Before(c.barrierDue) {
				c.barrierPending = false
				c.barrierDue = time.Time{}
				c.notifyLocked()
			}
			c.mu.Unlock()
		}
	}
}

func (c *recoveryContractCoordinator) acceptOffer(
	session uint64,
	message telemetry.RecoveryContractMessage,
	install func(),
) (telemetry.RecoveryContractMessage, bool) {
	if message.Type != telemetry.RecoveryContractOffer {
		return telemetry.RecoveryContractMessage{}, false
	}
	now := c.clock.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.haveReceived && c.receivedSession == session {
		existing := c.received
		if message.ContractID < existing.message.ContractID {
			return telemetry.RecoveryContractMessage{}, false
		}
		if message.ContractID == existing.message.ContractID {
			if existing.invalid || existing.message != message {
				existing.invalid = true
				c.received = existing
				return telemetry.RecoveryContractMessage{}, false
			}
			if existing.acceptedAt.Add(existing.message.Lifetime).Sub(now) < conservativeRecoveryService {
				return telemetry.RecoveryContractMessage{}, false
			}
			ack := message
			ack.Type = telemetry.RecoveryContractACK
			return ack, true
		}
	}

	if message.Lifetime < conservativeRecoveryService {
		return telemetry.RecoveryContractMessage{}, false
	}
	install()
	c.haveReceived = true
	c.receivedSession = session
	c.received = receivedRecoveryContract{message: message, acceptedAt: now}
	ack := message
	ack.Type = telemetry.RecoveryContractACK
	return ack, true
}

// beginPeerRecoveryContractLocked snapshots the complete selectable writer
// service after a transport transition. Caller holds m.mu and the peer's
// serviceGate write side.
func (m *Multipath) beginPeerRecoveryContractLocked(peer *peerState) error {
	if peer.contracts == nil {
		return nil
	}
	peer.contracts.setClock(m.clock)
	if len(m.paths) == 0 || m.fecCfg == nil || m.shaperConfigs == nil {
		peer.contracts.disable()
		return nil
	}
	enabled := len(peer.paths) > 0
	var bound time.Duration
	if enabled {
		for _, path := range peer.paths {
			if !path.localRecoveryContract().Enabled ||
				path.recoveryBound <= 0 ||
				path.recoveryBound >= conservativeRecoveryService {
				enabled = false
				bound = 0
				break
			}
			if path.recoveryBound > bound {
				bound = path.recoveryBound
			}
		}
	}
	return peer.contracts.begin(enabled, bound)
}

func (m *Multipath) freezePeerServices() []*peerState {
	m.mu.Lock()
	peers := make([]*peerState, 0, len(m.peers))
	for _, peer := range m.peers {
		if peer.contracts != nil {
			peers = append(peers, peer)
		}
	}
	m.mu.Unlock()
	for _, peer := range peers {
		peer.serviceGate.Lock()
	}
	var last time.Time
	for _, peer := range peers {
		if nanos := peer.lastWrite.Load(); nanos > 0 {
			written := time.Unix(0, nanos)
			if written.After(last) {
				last = written
			}
		}
	}
	if !last.IsZero() {
		due := last.Add(conservativeRecoveryService)
		if m.clock.Now().Before(due) {
			timer := m.clock.NewTimerAt(due)
			<-timer.C()
			timer.Stop()
		}
	}
	return peers
}

func (m *Multipath) finishPeerServiceTransition(peers []*peerState, rotate bool) error {
	var firstErr error
	if rotate {
		m.mu.Lock()
		for _, peer := range peers {
			if err := m.beginPeerRecoveryContractLocked(peer); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		m.mu.Unlock()
	}
	for index := len(peers) - 1; index >= 0; index-- {
		peers[index].serviceGate.Unlock()
	}
	return firstErr
}

func (m *Multipath) enterPeerRecoveryService(peer *peerState) error {
	for {
		if err := peer.contracts.awaitDecision(); err != nil {
			return err
		}
		peer.serviceGate.RLock()
		if !peer.contracts.barrierActive() {
			return nil
		}
		peer.serviceGate.RUnlock()
	}
}

func (m *Multipath) waitPeerRecoveryDrain(nanos int64, cancelled <-chan struct{}) bool {
	if nanos == 0 {
		return true
	}
	due := time.Unix(0, nanos).Add(conservativeRecoveryService)
	if !m.clock.Now().Before(due) {
		return true
	}
	timer := m.clock.NewTimerAt(due)
	select {
	case <-timer.C():
		return true
	case <-cancelled:
		timer.Stop()
		return false
	}
}

func (m *Multipath) invalidateAndRotatePeerRecoveryContract(peer *peerState) {
	if peer == nil || peer.contracts == nil {
		return
	}
	peer.contracts.invalidateForTransition()
	peer.serviceTransitionRequested.Add(1)
	if !peer.serviceTransitionPending.CompareAndSwap(false, true) {
		return
	}
	m.mu.Lock()
	cancelled := m.recvClosed
	m.mu.Unlock()
	go m.rotateInvalidatedPeerRecoveryContract(peer, cancelled)
}

func (m *Multipath) rotateInvalidatedPeerRecoveryContract(peer *peerState, cancelled <-chan struct{}) {
	for {
		requested := peer.serviceTransitionRequested.Load()
		peer.serviceGate.Lock()
		lastWrite := peer.lastWrite.Load()
		peer.serviceGate.Unlock()
		if !m.waitPeerRecoveryDrain(lastWrite, cancelled) {
			peer.serviceTransitionPending.Store(false)
			if peer.serviceTransitionRequested.Load() != requested &&
				peer.serviceTransitionPending.CompareAndSwap(false, true) {
				m.mu.Lock()
				nextCancelled := m.recvClosed
				active := len(m.paths) > 0
				m.mu.Unlock()
				if active {
					go m.rotateInvalidatedPeerRecoveryContract(peer, nextCancelled)
				} else {
					peer.serviceTransitionPending.Store(false)
				}
			}
			return
		}
		m.transitionMu.Lock()
		peer.serviceGate.Lock()
		m.mu.Lock()
		active := len(m.paths) > 0
		if active {
			found := false
			for _, candidate := range m.peers {
				if candidate == peer {
					found = true
					break
				}
			}
			active = found
		}
		var err error
		if active {
			err = m.beginPeerRecoveryContractLocked(peer)
		} else {
			peer.contracts.disable()
		}
		m.mu.Unlock()
		peer.serviceGate.Unlock()
		m.transitionMu.Unlock()
		if err != nil {
			m.log.Error("bind: recovery contract rotation failed", "error", err)
		}
		if peer.serviceTransitionRequested.Load() != requested {
			continue
		}
		peer.serviceTransitionPending.Store(false)
		if peer.serviceTransitionRequested.Load() == requested ||
			!peer.serviceTransitionPending.CompareAndSwap(false, true) {
			return
		}
	}
}
