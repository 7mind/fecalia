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
	venues     map[recoveryVenueKey]struct{}
}

type recoveryVenueKey struct {
	pathKey uint32
	source  netip.AddrPort
}

type receivedRecoverySnapshot struct {
	present          bool
	session          uint64
	message          telemetry.RecoveryContractMessage
	validUntil       time.Time
	generation       uint64
	evidenceRevision uint64
	venues           []recoveryVenueKey
	acked            bool
	invalid          bool
}

// recoveryContractCoordinator is the peer-scoped owner of both directions of
// recovery-contract negotiation. It deliberately does not live on Prober:
// every path of one peer advertises and acknowledges one contract while the
// receiver/topology generation invalidates stale evidence across transitions.
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

	haveReceived                bool
	receivedSession             uint64
	received                    receivedRecoveryContract
	receivedGeneration          uint64
	receivedEvidenceRevision    uint64
	receivedPublicationRevision uint64
	receivedAuthority           *reseq.RecoveryAuthority
	observedSources             map[uint32]netip.AddrPort
	haveAdoptedSession          bool
	adoptedSession              uint64
}

func newRecoveryContractCoordinator(session uint64, clock fecOwnerClock) *recoveryContractCoordinator {
	return &recoveryContractCoordinator{
		clock:             clock,
		session:           session,
		changed:           make(chan struct{}),
		receivedAuthority: &reseq.RecoveryAuthority{},
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
	c.advanceReceivedGenerationLocked()
	c.barrierPending = false
	c.barrierDue = time.Time{}
	c.haveLease = false
	c.leaseUntil = time.Time{}
	c.invalidated = false
	c.offer = nil
	c.haveReceived = false
	c.receivedSession = 0
	c.received = receivedRecoveryContract{}
	c.observedSources = nil
	c.haveAdoptedSession = false
	c.adoptedSession = 0
	c.notifyLocked()
	c.mu.Unlock()
}

// adoptReceivedSession pins the peer-wide process epoch after the reflector's
// live-challenge adoption. A path that still accepts an old per-path session
// cannot subsequently restore old receiver evidence.
func (c *recoveryContractCoordinator) adoptReceivedSession(session uint64) (uint64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.haveAdoptedSession && c.adoptedSession == session {
		return c.receivedGeneration, false
	}
	c.advanceReceivedGenerationLocked()
	c.haveAdoptedSession = true
	c.adoptedSession = session
	return c.receivedGeneration, true
}

func (c *recoveryContractCoordinator) bumpReceivedGenerationLocked() {
	c.receivedGeneration++
	if c.receivedGeneration == 0 {
		c.receivedGeneration++
	}
	c.receivedAuthority.AdvanceTo(c.receivedGeneration, c.clock.Now())
}

func (c *recoveryContractCoordinator) bumpReceivedEvidenceLocked() {
	c.receivedEvidenceRevision++
	if c.receivedEvidenceRevision == 0 {
		c.receivedEvidenceRevision++
	}
}

// invalidateReceivedEvidence revokes only the fast receiver evidence. The
// accepted ContractID high-water remains intact, so an ordinary exact re-ACK
// restores evidence without permitting a lower or inconsistent identity.
func (c *recoveryContractCoordinator) invalidateReceivedEvidence() uint64 {
	c.mu.Lock()
	c.advanceReceivedGenerationLocked()
	generation := c.receivedGeneration
	c.mu.Unlock()
	return generation
}

func (c *recoveryContractCoordinator) advanceReceivedGenerationLocked() {
	c.bumpReceivedGenerationLocked()
	c.bumpReceivedEvidenceLocked()
	c.received.venues = nil
	c.observedSources = nil
}

// observeReceivedSource records the authenticated source for one exact
// composite path. A same-key source change advances the peer generation before
// clearing every old ACK venue.
func (c *recoveryContractCoordinator) observeReceivedSource(pathKey uint32, source netip.AddrPort) (uint64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !source.IsValid() {
		return c.receivedGeneration, false
	}
	if previous, exists := c.observedSources[pathKey]; exists && previous != source {
		c.advanceReceivedGenerationLocked()
		if c.observedSources == nil {
			c.observedSources = make(map[uint32]netip.AddrPort)
		}
		c.observedSources[pathKey] = source
		return c.receivedGeneration, true
	}
	if c.observedSources == nil {
		c.observedSources = make(map[uint32]netip.AddrPort)
	}
	if previous, exists := c.observedSources[pathKey]; !exists || previous != source {
		c.observedSources[pathKey] = source
		c.bumpReceivedEvidenceLocked()
	}
	return c.receivedGeneration, false
}

func (c *recoveryContractCoordinator) receivedSnapshot() receivedRecoverySnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.haveReceived {
		return receivedRecoverySnapshot{
			generation:       c.receivedGeneration,
			evidenceRevision: c.receivedEvidenceRevision,
		}
	}
	snapshot := receivedRecoverySnapshot{
		present:          true,
		session:          c.receivedSession,
		message:          c.received.message,
		validUntil:       c.received.acceptedAt.Add(c.received.message.Lifetime),
		generation:       c.receivedGeneration,
		evidenceRevision: c.receivedEvidenceRevision,
		acked:            len(c.received.venues) > 0,
		invalid:          c.received.invalid,
	}
	for venue := range c.received.venues {
		snapshot.venues = append(snapshot.venues, venue)
	}
	sort.Slice(snapshot.venues, func(i, j int) bool {
		if snapshot.venues[i].pathKey != snapshot.venues[j].pathKey {
			return snapshot.venues[i].pathKey < snapshot.venues[j].pathKey
		}
		return snapshot.venues[i].source.String() < snapshot.venues[j].source.String()
	})
	return snapshot
}

func (c *recoveryContractCoordinator) reserveReceivedPublication(
	snapshot receivedRecoverySnapshot,
	validate func() bool,
) (uint64, uint64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.receivedGeneration != snapshot.generation ||
		c.receivedEvidenceRevision != snapshot.evidenceRevision ||
		c.haveReceived != snapshot.present {
		return 0, 0, false
	}
	if snapshot.present &&
		(c.receivedSession != snapshot.session ||
			c.received.message != snapshot.message ||
			!c.received.acceptedAt.Add(c.received.message.Lifetime).Equal(snapshot.validUntil) ||
			c.received.invalid != snapshot.invalid ||
			len(c.received.venues) != len(snapshot.venues)) {
		return 0, 0, false
	}
	for _, venue := range snapshot.venues {
		if _, exists := c.received.venues[venue]; !exists {
			return 0, 0, false
		}
	}
	if !validate() {
		return 0, 0, false
	}
	c.receivedPublicationRevision++
	if c.receivedPublicationRevision == 0 {
		c.receivedPublicationRevision++
	}
	return c.receivedGeneration, c.receivedPublicationRevision, true
}

func (c *recoveryContractCoordinator) recoveryAuthority() *reseq.RecoveryAuthority {
	return c.receivedAuthority
}

type receivedACKAdmission struct {
	generation uint64
	session    uint64
	message    telemetry.RecoveryContractMessage
	venue      recoveryVenueKey
}

// admitReceivedACK snapshots the exact receiver generation and source
// observation at ACK admission. Completion may publish only this token.
func (c *recoveryContractCoordinator) admitReceivedACK(
	session uint64,
	message telemetry.RecoveryContractMessage,
	pathKey uint32,
	source netip.AddrPort,
) (receivedACKAdmission, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.haveReceived || c.received.invalid ||
		c.receivedSession != session || c.received.message != message ||
		c.observedSources[pathKey] != source || !source.IsValid() {
		return receivedACKAdmission{}, false
	}
	return receivedACKAdmission{
		generation: c.receivedGeneration,
		session:    session,
		message:    message,
		venue:      recoveryVenueKey{pathKey: pathKey, source: source},
	}, true
}

// completeReceivedACK publishes an exact venue only when contract, generation,
// and current authenticated source observation still match the admission.
func (c *recoveryContractCoordinator) completeReceivedACK(admission receivedACKAdmission) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.haveReceived || c.received.invalid ||
		c.receivedGeneration != admission.generation ||
		c.receivedSession != admission.session ||
		c.received.message != admission.message ||
		c.observedSources[admission.venue.pathKey] != admission.venue.source {
		return false
	}
	venue := admission.venue
	if _, exists := c.received.venues[venue]; !exists {
		if c.received.venues == nil {
			c.received.venues = make(map[recoveryVenueKey]struct{})
		}
		c.received.venues[venue] = struct{}{}
		c.bumpReceivedEvidenceLocked()
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
				c.advanceReceivedGenerationLocked()
				install()
				existing.invalid = true
				existing.venues = nil
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
		if sameRecoveryService(existing.message, message) {
			c.advanceReceivedGenerationLocked()
			c.received = receivedRecoveryContract{message: message, acceptedAt: now}
			ack := message
			ack.Type = telemetry.RecoveryContractACK
			return ack, true
		}
	}

	if message.Lifetime < conservativeRecoveryService {
		return telemetry.RecoveryContractMessage{}, false
	}
	c.advanceReceivedGenerationLocked()
	install()
	c.haveReceived = true
	c.receivedSession = session
	c.received = receivedRecoveryContract{message: message, acceptedAt: now}
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

type recoveryRefreshPath struct {
	path     *peerPathState
	id       uint8
	prober   *telemetry.Prober
	evidence telemetry.RecoveryRTTSnapshot
}

type recoveryRefreshSnapshot struct {
	resequencer *reseq.Resequencer
	scheduler   sched.Scheduler
	paths       []recoveryRefreshPath
	contract    receivedRecoverySnapshot
}

// refreshPeerRecoveryWindow derives the receiver hold from one coherent set of
// authenticated contract, delivery-venue, membership, liveness, and RTT
// snapshots. The final publication revalidates every captured revision and
// reserves its publication order before entering the resequencer.
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
	paths := make([]recoveryRefreshPath, 0, len(peer.paths))
	for _, path := range peer.paths {
		paths = append(paths, recoveryRefreshPath{
			path:   path,
			id:     path.id,
			prober: path.prober,
		})
	}
	scheduler := peer.scheduler
	m.mu.Unlock()
	if !found || rq == nil {
		return
	}

	for i := range paths {
		if paths[i].prober != nil {
			paths[i].evidence = paths[i].prober.RecoveryRTT()
		}
	}
	snapshot := recoveryRefreshSnapshot{
		resequencer: rq,
		scheduler:   scheduler,
		paths:       paths,
		contract:    peer.contracts.receivedSnapshot(),
	}
	now := m.clock.Now()
	windows := deriveRecoveryWindows(snapshot, now)
	if m.beforeRecoveryPublish != nil {
		m.beforeRecoveryPublish(peer, rq, snapshot.contract.generation)
	}
	m.commitPeerRecoveryPublication(peer, snapshot, windows)
}

func deriveRecoveryWindows(snapshot recoveryRefreshSnapshot, now time.Time) []reseq.RecoveryWindow {
	contract := snapshot.contract
	if !contract.acked || contract.invalid ||
		!contract.message.Enabled ||
		contract.message.Lifetime != telemetry.RecoveryContractLifetime ||
		contract.message.ServiceBound <= 0 ||
		contract.message.ServiceBound >= conservativeRecoveryService ||
		contract.validUntil.Sub(now) < conservativeRecoveryService {
		return nil
	}
	if _, weighted := snapshot.scheduler.(*sched.WeightedScheduler); weighted {
		return nil
	}

	haveQualified := false
	qualifiedPathIDs := make(map[uint8]struct{}, len(snapshot.paths))
	maxRTT := time.Duration(0)
	validUntil := contract.validUntil
	for _, path := range snapshot.paths {
		if path.prober == nil {
			return nil
		}
		evidence := path.evidence
		if evidence.State != telemetry.StateUp {
			continue
		}
		if !evidence.Present ||
			evidence.FreshUntil.Sub(now) < conservativeRecoveryService {
			return nil
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
		return nil
	}

	hold := recoveryWindow(contract.message.ServiceBound, recoveryRTTHeadroom(maxRTT))
	if hold >= conservativeRecoveryService {
		return nil
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
			Revision:   contract.generation,
			PathKey:    venue.pathKey,
			Source:     venue.source,
			Hold:       hold,
			ValidUntil: validUntil,
		})
	}
	return windows
}

func (m *Multipath) commitPeerRecoveryPublication(
	peer *peerState,
	snapshot recoveryRefreshSnapshot,
	windows []reseq.RecoveryWindow,
) {
	m.mu.Lock()
	found := false
	for _, candidate := range m.peers {
		if candidate == peer {
			found = true
			break
		}
	}
	if !found || peer.resequencer.Load() != snapshot.resequencer ||
		peer.scheduler != snapshot.scheduler ||
		len(peer.paths) != len(snapshot.paths) {
		m.mu.Unlock()
		return
	}
	for i, path := range peer.paths {
		captured := snapshot.paths[i]
		if path != captured.path || path.id != captured.id ||
			path.prober != captured.prober {
			m.mu.Unlock()
			return
		}
	}

	publishWindows := windows
	generation, publicationRevision, ok := peer.contracts.reserveReceivedPublication(
		snapshot.contract,
		func() bool {
			for _, path := range snapshot.paths {
				if path.prober != nil && path.prober.RecoveryRTT() != path.evidence {
					return false
				}
			}
			if len(publishWindows) > 0 {
				publishWindows = deriveRecoveryWindows(snapshot, m.clock.Now())
			}
			return true
		},
	)
	m.mu.Unlock()
	if !ok {
		return
	}
	if m.afterRecoveryPublicationReserve != nil {
		m.afterRecoveryPublicationReserve(
			peer,
			snapshot.resequencer,
			generation,
			publicationRevision,
		)
	}
	snapshot.resequencer.SetRecoveryPublication(
		generation,
		publicationRevision,
		publishWindows,
	)
}

func (m *Multipath) publishPeerRecoveryGeneration(peer *peerState, generation uint64) {
	if peer == nil {
		return
	}
	if rq := peer.resequencer.Load(); rq != nil {
		rq.SetRecoveryPublication(generation, 0, nil)
	}
}

func (m *Multipath) invalidatePeerRecoveryEvidence(peer *peerState) uint64 {
	if peer == nil || peer.contracts == nil {
		return 0
	}
	generation := peer.contracts.invalidateReceivedEvidence()
	m.publishPeerRecoveryGeneration(peer, generation)
	return generation
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
			m.invalidatePeerRecoveryEvidence(peer)
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
		m.invalidatePeerRecoveryEvidence(peer)
		m.refreshPeerRecoveryWindow(peer)
	}
	m.finishInvalidatedPeerRecoveryWorker(peer, requested)
}
