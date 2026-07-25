package bind

import (
	"sync"
	"time"

	"github.com/7mind/wanbond/internal/telemetry"
)

const conservativeRecoveryService = 250 * time.Millisecond

type sentRecoveryOffer struct {
	sessionID  uint64
	contractID uint64
	probeSeq   uint64
	challenge  uint64
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

	local       telemetry.RecoveryContractMessage
	localBytes  []byte
	localAt     time.Time
	pending     bool
	fallbackDue time.Time
	ackedUntil  time.Time
	outstanding map[uint8]sentRecoveryOffer

	haveReceived    bool
	receivedSession uint64
	received        receivedRecoveryContract
}

func newRecoveryContractCoordinator(session uint64, clock fecOwnerClock) *recoveryContractCoordinator {
	return &recoveryContractCoordinator{
		clock:       clock,
		session:     session,
		changed:     make(chan struct{}),
		outstanding: make(map[uint8]sentRecoveryOffer),
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
	c.nextID++
	if c.nextID == 0 {
		c.nextID++
	}
	message := telemetry.RecoveryContractMessage{
		Type:       telemetry.RecoveryContractOffer,
		Enabled:    enabled,
		Lifetime:   telemetry.RecoveryContractLifetime,
		ContractID: c.nextID,
	}
	if enabled {
		message.ServiceBound = serviceBound
	}
	payload, err := telemetry.EncodeRecoveryContract(message)
	if err != nil {
		return err
	}
	c.local = message
	c.localBytes = payload
	c.localAt = c.clock.Now()
	c.pending = true
	c.ackedUntil = time.Time{}
	c.outstanding = make(map[uint8]sentRecoveryOffer)
	c.fallbackDue = c.clock.Now().Add(conservativeRecoveryService)
	c.notifyLocked()
	return nil
}

func (c *recoveryContractCoordinator) disable() {
	c.mu.Lock()
	c.pending = false
	c.ackedUntil = time.Time{}
	c.localBytes = nil
	c.outstanding = make(map[uint8]sentRecoveryOffer)
	c.notifyLocked()
	c.mu.Unlock()
}

func (c *recoveryContractCoordinator) payload() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.localBytes...)
}

func (c *recoveryContractCoordinator) recordOffer(pathID uint8, probe telemetryProbeHeader) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.pending || probe.sessionID != c.session {
		return
	}
	c.outstanding[pathID] = sentRecoveryOffer{
		sessionID:  probe.sessionID,
		contractID: c.local.ContractID,
		probeSeq:   probe.probeSeq,
		challenge:  probe.challenge,
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
	sent, ok := c.outstanding[pathID]
	if !ok || sent.sessionID != probeSession || sent.probeSeq != probeSeq ||
		sent.contractID != message.ContractID || sent.challenge == 0 ||
		probeSession != c.session || !c.pending {
		return false
	}
	want := c.local
	want.Type = telemetry.RecoveryContractACK
	if message != want {
		return false
	}
	until := c.localAt.Add(message.Lifetime)
	if until.Sub(c.clock.Now()) < conservativeRecoveryService {
		return false
	}
	c.pending = false
	c.ackedUntil = until
	c.notifyLocked()
	return true
}

func (c *recoveryContractCoordinator) fastEligible() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pending || !c.local.Enabled || c.ackedUntil.IsZero() {
		return false
	}
	return c.ackedUntil.Sub(c.clock.Now()) >= conservativeRecoveryService
}

func (c *recoveryContractCoordinator) awaitDecision() error {
	for {
		c.mu.Lock()
		if !c.pending {
			c.mu.Unlock()
			return nil
		}
		due := c.fallbackDue
		changed := c.changed
		clock := c.clock
		c.mu.Unlock()

		timer := clock.NewTimerAt(due)
		select {
		case <-changed:
			timer.Stop()
		case <-timer.C():
			c.mu.Lock()
			if c.pending && !c.clock.Now().Before(c.fallbackDue) {
				c.pending = false
				c.ackedUntil = time.Time{}
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
	if m.fecCfg == nil || m.shaperConfigs == nil {
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
