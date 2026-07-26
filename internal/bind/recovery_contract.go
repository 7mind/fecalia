package bind

import (
	"net/netip"
	"sort"
	"sync"
	"time"

	"github.com/7mind/wanbond/internal/reseq"
	"github.com/7mind/wanbond/internal/sched"
	"github.com/7mind/wanbond/internal/telemetry"
)

const (
	conservativeRecoveryService = 250 * time.Millisecond
	recoveryRenewBefore         = 2 * conservativeRecoveryService
	recoveryRTTFloor            = 10 * time.Millisecond
	recoveryRTTMultiple         = 4
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
	venues     map[recoveryVenueKey]uint64
}

type recoveryVenueKey struct {
	pathKey uint32
	source  netip.AddrPort
}

type receivedRecoveryVenue struct {
	recoveryVenueKey
	revision uint64
}

type receivedRecoverySnapshot struct {
	session    uint64
	message    telemetry.RecoveryContractMessage
	validUntil time.Time
	venues     []receivedRecoveryVenue
	acked      bool
	invalid    bool
}

func (s receivedRecoverySnapshot) hasRoamedVenue(pathKey uint32, source netip.AddrPort) bool {
	for _, venue := range s.venues {
		if venue.pathKey == pathKey && venue.source != source {
			return true
		}
	}
	return false
}

// recoveryContractCoordinator is the peer-scoped owner of both directions of
// recovery-contract negotiation. It deliberately does not live on Prober:
// every path of one peer advertises and acknowledges one immutable generation.
type recoveryContractCoordinator struct {
	mu         sync.Mutex
	clock      fecOwnerClock
	session    uint64
	nextID     uint64
	changed    chan struct{}
	generation uint64

	offer *localRecoveryOffer

	barrierPending bool
	barrierDue     time.Time

	haveLease   bool
	lease       telemetry.RecoveryContractMessage
	leaseUntil  time.Time
	invalidated bool

	haveReceived       bool
	receivedSession    uint64
	received           receivedRecoveryContract
	receivedRevision   uint64
	haveAdoptedSession bool
	adoptedSession     uint64
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
	generation := c.generation
	c.mu.Unlock()
	return c.beginGeneration(enabled, serviceBound, generation)
}

func (c *recoveryContractCoordinator) beginGeneration(enabled bool, serviceBound time.Duration, generation uint64) error {
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
	c.generation = generation
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
	c.haveReceived = false
	c.receivedSession = 0
	c.received = receivedRecoveryContract{}
	c.haveAdoptedSession = false
	c.adoptedSession = 0
	c.bumpReceivedRevisionLocked()
	c.notifyLocked()
	c.mu.Unlock()
}

// adoptReceivedSession pins the peer-wide process epoch after the reflector's
// live-challenge adoption. A path that still accepts an old per-path session
// cannot subsequently restore old receiver evidence.
func (c *recoveryContractCoordinator) adoptReceivedSession(session uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.haveAdoptedSession && c.adoptedSession == session {
		return
	}
	c.invalidateReceivedEvidenceLocked()
	c.haveAdoptedSession = true
	c.adoptedSession = session
	c.bumpReceivedRevisionLocked()
}

func (c *recoveryContractCoordinator) bumpReceivedRevisionLocked() {
	c.receivedRevision++
	if c.receivedRevision == 0 {
		c.receivedRevision++
	}
}

// invalidateReceivedEvidence revokes only the fast receiver evidence. The
// accepted ContractID high-water remains intact, so an ordinary exact re-ACK
// restores evidence without permitting a lower or inconsistent identity.
func (c *recoveryContractCoordinator) invalidateReceivedEvidence() {
	c.mu.Lock()
	c.invalidateReceivedEvidenceLocked()
	c.mu.Unlock()
}

func (c *recoveryContractCoordinator) invalidateReceivedEvidenceLocked() {
	if !c.haveReceived || len(c.received.venues) == 0 {
		return
	}
	c.received.venues = nil
	c.bumpReceivedRevisionLocked()
}

func (c *recoveryContractCoordinator) receivedSnapshot() receivedRecoverySnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.haveReceived {
		return receivedRecoverySnapshot{}
	}
	snapshot := receivedRecoverySnapshot{
		session:    c.receivedSession,
		message:    c.received.message,
		validUntil: c.received.acceptedAt.Add(c.received.message.Lifetime),
		acked:      len(c.received.venues) > 0,
		invalid:    c.received.invalid,
	}
	for venue, revision := range c.received.venues {
		snapshot.venues = append(snapshot.venues, receivedRecoveryVenue{
			recoveryVenueKey: venue,
			revision:         revision,
		})
	}
	sort.Slice(snapshot.venues, func(i, j int) bool {
		if snapshot.venues[i].pathKey != snapshot.venues[j].pathKey {
			return snapshot.venues[i].pathKey < snapshot.venues[j].pathKey
		}
		return snapshot.venues[i].source.String() < snapshot.venues[j].source.String()
	})
	return snapshot
}

// recordReceivedACK makes receiver evidence usable only after the exact typed
// ACK has completed successfully on the exact composite delivery venue.
func (c *recoveryContractCoordinator) recordReceivedACK(
	session uint64,
	message telemetry.RecoveryContractMessage,
	pathKey uint32,
	source netip.AddrPort,
) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.haveReceived || c.received.invalid ||
		c.receivedSession != session || c.received.message != message ||
		!source.IsValid() {
		return false
	}
	venue := recoveryVenueKey{pathKey: pathKey, source: source}
	if _, exists := c.received.venues[venue]; !exists {
		if c.received.venues == nil {
			c.received.venues = make(map[recoveryVenueKey]uint64)
		}
		c.bumpReceivedRevisionLocked()
		c.received.venues[venue] = c.receivedRevision
	}
	return true
}

// invalidateForTransition immediately revokes fast recovery and blocks new DATA
// while an asynchronous service-transition worker drains and rotates the offer.
// A zero barrierDue deliberately has no autonomous fallback: begin installs the
// new service snapshot and starts the explicit T fallback.
func (c *recoveryContractCoordinator) invalidateForTransition() {
	c.mu.Lock()
	generation := c.generation
	c.mu.Unlock()
	c.invalidateGeneration(generation)
}

func (c *recoveryContractCoordinator) invalidateGeneration(generation uint64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.generation != generation {
		return false
	}
	c.haveLease = false
	c.leaseUntil = time.Time{}
	c.invalidated = true
	c.barrierPending = true
	c.barrierDue = time.Time{}
	c.notifyLocked()
	return true
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
	if c.haveAdoptedSession && c.adoptedSession != session {
		return telemetry.RecoveryContractMessage{}, false
	}
	if c.haveReceived && c.receivedSession == session {
		existing := c.received
		if message.ContractID < existing.message.ContractID {
			return telemetry.RecoveryContractMessage{}, false
		}
		if message.ContractID == existing.message.ContractID {
			if existing.invalid {
				return telemetry.RecoveryContractMessage{}, false
			}
			if existing.message != message {
				c.invalidateReceivedEvidenceLocked()
				install()
				existing.invalid = true
				existing.venues = nil
				c.received = existing
				c.bumpReceivedRevisionLocked()
				return telemetry.RecoveryContractMessage{}, false
			}
			if existing.acceptedAt.Add(existing.message.Lifetime).Sub(now) < conservativeRecoveryService {
				return telemetry.RecoveryContractMessage{}, false
			}
			ack := message
			ack.Type = telemetry.RecoveryContractACK
			return ack, true
		}
		if sameRecoveryService(existing.message, message) {
			c.received = receivedRecoveryContract{message: message, acceptedAt: now}
			c.bumpReceivedRevisionLocked()
			ack := message
			ack.Type = telemetry.RecoveryContractACK
			return ack, true
		}
	}

	if message.Lifetime < conservativeRecoveryService {
		return telemetry.RecoveryContractMessage{}, false
	}
	c.invalidateReceivedEvidenceLocked()
	install()
	c.haveReceived = true
	c.receivedSession = session
	c.received = receivedRecoveryContract{message: message, acceptedAt: now}
	c.bumpReceivedRevisionLocked()
	ack := message
	ack.Type = telemetry.RecoveryContractACK
	return ack, true
}

func sameRecoveryService(left, right telemetry.RecoveryContractMessage) bool {
	left.Type = telemetry.RecoveryContractOffer
	right.Type = telemetry.RecoveryContractOffer
	left.ContractID = 0
	right.ContractID = 0
	return left == right
}

func recoveryRTTHeadroom(maxRTT time.Duration) time.Duration {
	if maxRTT <= recoveryRTTFloor/recoveryRTTMultiple {
		return recoveryRTTFloor
	}
	if maxRTT >= conservativeRecoveryService/recoveryRTTMultiple {
		return conservativeRecoveryService
	}
	return recoveryRTTMultiple * maxRTT
}

func recoveryWindow(service, headroom time.Duration) time.Duration {
	if service >= conservativeRecoveryService-headroom {
		return conservativeRecoveryService
	}
	return service + headroom
}

// refreshPeerRecoveryWindow derives the receiver hold from one coherent set of
// authenticated contract, delivery-venue, liveness, and RTT snapshots. It takes
// no bind lock while entering the resequencer.
func (m *Multipath) refreshPeerRecoveryWindow(peer *peerState) {
	if m.fecCfg == nil || peer == nil || peer.contracts == nil {
		return
	}
	m.mu.Lock()
	found := false
	for _, candidate := range m.peers {
		if candidate == peer {
			found = true
			break
		}
	}
	rq := peer.resequencer.Load()
	paths := append([]*peerPathState(nil), peer.paths...)
	scheduler := peer.scheduler
	m.mu.Unlock()
	if !found || rq == nil {
		return
	}

	contract := peer.contracts.receivedSnapshot()
	now := m.clock.Now()
	if !contract.acked || contract.invalid ||
		!contract.message.Enabled ||
		contract.message.Lifetime != telemetry.RecoveryContractLifetime ||
		contract.message.ServiceBound <= 0 ||
		contract.message.ServiceBound >= conservativeRecoveryService ||
		contract.validUntil.Sub(now) < conservativeRecoveryService {
		rq.SetRecoveryWindows(nil)
		return
	}
	if _, weighted := scheduler.(*sched.WeightedScheduler); weighted {
		rq.SetRecoveryWindows(nil)
		return
	}

	haveQualified := false
	qualifiedPathIDs := make(map[uint8]struct{}, len(paths))
	maxRTT := time.Duration(0)
	validUntil := contract.validUntil
	for _, path := range paths {
		if path.prober == nil {
			rq.SetRecoveryWindows(nil)
			return
		}
		evidence := path.prober.RecoveryRTT()
		if evidence.State != telemetry.StateUp {
			continue
		}
		if !evidence.Present ||
			evidence.FreshUntil.Sub(now) < conservativeRecoveryService {
			rq.SetRecoveryWindows(nil)
			return
		}
		haveQualified = true
		qualifiedPathIDs[path.id] = struct{}{}
		if evidence.RTT > maxRTT {
			maxRTT = evidence.RTT
		}
		if evidence.FreshUntil.Before(validUntil) {
			validUntil = evidence.FreshUntil
		}
	}
	if !haveQualified {
		rq.SetRecoveryWindows(nil)
		return
	}

	hold := recoveryWindow(contract.message.ServiceBound, recoveryRTTHeadroom(maxRTT))
	if hold >= conservativeRecoveryService {
		rq.SetRecoveryWindows(nil)
		return
	}
	windows := make([]reseq.RecoveryWindow, 0, len(contract.venues))
	for _, venue := range contract.venues {
		if !venue.source.IsValid() {
			continue
		}
		if _, qualified := qualifiedPathIDs[uint8(venue.pathKey>>8)]; !qualified {
			continue
		}
		windows = append(windows, reseq.RecoveryWindow{
			Enabled:    true,
			Revision:   venue.revision,
			PathKey:    venue.pathKey,
			Source:     venue.source,
			Hold:       hold,
			ValidUntil: validUntil,
		})
	}
	rq.SetRecoveryWindows(windows)
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
	return peer.contracts.beginGeneration(enabled, bound, m.openGeneration.Load())
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
		for _, peer := range peers {
			peer.contracts.invalidateReceivedEvidence()
		}
	}
	for index := len(peers) - 1; index >= 0; index-- {
		peers[index].serviceGate.Unlock()
	}
	if rotate {
		for _, peer := range peers {
			m.refreshPeerRecoveryWindow(peer)
		}
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

func (m *Multipath) captureOpenGeneration(generation uint64) (chan struct{}, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.openGeneration.Load() != generation || len(m.paths) == 0 || m.recvClosed == nil {
		return nil, false
	}
	select {
	case <-m.recvClosed:
		return nil, false
	default:
		return m.recvClosed, true
	}
}

func (m *Multipath) openGenerationCurrent(generation uint64, token <-chan struct{}) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.openGenerationCurrentLocked(generation, token)
}

func (m *Multipath) openGenerationCurrentLocked(generation uint64, token <-chan struct{}) bool {
	if m.openGeneration.Load() != generation || len(m.paths) == 0 || m.recvClosed != token {
		return false
	}
	select {
	case <-token:
		return false
	default:
		return true
	}
}

func (m *Multipath) invalidateAndRotatePeerRecoveryContract(peer *peerState, generation uint64) {
	if peer == nil || peer.contracts == nil {
		return
	}
	cancelled, current := m.captureOpenGeneration(generation)
	if !current || !peer.contracts.invalidateGeneration(generation) {
		return
	}
	peer.serviceTransitionGeneration.Store(generation)
	requested := peer.serviceTransitionRequested.Add(1)
	if !peer.serviceTransitionPending.CompareAndSwap(false, true) {
		return
	}
	go m.rotateInvalidatedPeerRecoveryContract(peer, generation, requested, cancelled)
}

func (m *Multipath) finishInvalidatedPeerRecoveryWorker(peer *peerState, handled uint64) {
	// Publish-release-recheck is the lossless single-consumer handoff: a producer
	// either claims the released latch itself or leaves a counter delta that this
	// owner observes and reacquires. A closed-generation request is handled by
	// dropping it once; only a newer request can drive another capture attempt.
	peer.serviceTransitionHandled.Store(handled)
	for {
		peer.serviceTransitionPending.Store(false)
		if peer.serviceTransitionRequested.Load() == handled {
			return
		}
		if !peer.serviceTransitionPending.CompareAndSwap(false, true) {
			return
		}
		requested := peer.serviceTransitionRequested.Load()
		generation := peer.serviceTransitionGeneration.Load()
		cancelled, current := m.captureOpenGeneration(generation)
		if current {
			go m.rotateInvalidatedPeerRecoveryContract(peer, generation, requested, cancelled)
			return
		}
		peer.serviceTransitionHandled.Store(requested)
		if m.afterRecoveryTransitionCaptureMiss != nil {
			m.afterRecoveryTransitionCaptureMiss(peer, generation, requested)
		}
		handled = requested
	}
}

func (m *Multipath) rotateInvalidatedPeerRecoveryContract(peer *peerState, generation, requested uint64, cancelled <-chan struct{}) {
	if peer.serviceTransitionGeneration.Load() != generation {
		m.finishInvalidatedPeerRecoveryWorker(peer, requested)
		return
	}
	peer.serviceGate.Lock()
	lastWrite := peer.lastWrite.Load()
	peer.serviceGate.Unlock()
	if !m.waitPeerRecoveryDrain(lastWrite, cancelled) ||
		!m.openGenerationCurrent(generation, cancelled) {
		m.finishInvalidatedPeerRecoveryWorker(peer, requested)
		return
	}

	m.transitionMu.Lock()
	if !m.openGenerationCurrent(generation, cancelled) {
		m.transitionMu.Unlock()
		m.finishInvalidatedPeerRecoveryWorker(peer, requested)
		return
	}
	peer.serviceGate.Lock()
	m.mu.Lock()
	active := m.openGenerationCurrentLocked(generation, cancelled)
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
	}
	m.mu.Unlock()
	peer.serviceGate.Unlock()
	m.transitionMu.Unlock()
	if err != nil {
		m.log.Error("bind: recovery contract rotation failed", "error", err)
	}
	if active {
		peer.contracts.invalidateReceivedEvidence()
		m.refreshPeerRecoveryWindow(peer)
	}
	m.finishInvalidatedPeerRecoveryWorker(peer, requested)
}
