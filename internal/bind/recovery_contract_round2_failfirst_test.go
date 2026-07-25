package bind

import (
	"net"
	"net/netip"
	"syscall"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/fec"
	"github.com/7mind/wanbond/internal/frame"
	"github.com/7mind/wanbond/internal/shaper"
	"github.com/7mind/wanbond/internal/telemetry"
)

func acknowledgeCurrentContract(t testing.TB, coordinator *recoveryContractCoordinator, pathID uint8, probeSeq uint64) telemetry.RecoveryContractMessage {
	t.Helper()
	offered := coordinator.offerSnapshot()
	offer, recognized, err := telemetry.DecodeRecoveryContract(offered.payload)
	if err != nil || !recognized {
		t.Fatalf("decode current recovery contract = recognized %v err %v", recognized, err)
	}
	ack := offer
	ack.Type = telemetry.RecoveryContractACK
	payload, err := telemetry.EncodeRecoveryContract(ack)
	if err != nil {
		t.Fatal(err)
	}
	coordinator.recordOffer(pathID, telemetryProbeHeader{
		sessionID: coordinator.session,
		probeSeq:  probeSeq,
		challenge: 1,
	}, offered)
	if !coordinator.acceptACK(pathID, coordinator.session, probeSeq, payload) {
		t.Fatal("current recovery contract ACK rejected")
	}
	return offer
}

func TestFallbackKeepsLiveOfferACKEligibleAtProductionCadence(t *testing.T) {
	clock := newFakeClock()
	coordinator := newRecoveryContractCoordinator(0xCA01, clock)
	if err := coordinator.begin(true, 125*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	offered := coordinator.offerSnapshot()
	offer, recognized, err := telemetry.DecodeRecoveryContract(offered.payload)
	if err != nil || !recognized {
		t.Fatal("initial OFFER unavailable")
	}

	waitDone := make(chan error, 1)
	go func() { waitDone <- coordinator.awaitDecision() }()
	time.Sleep(time.Millisecond)
	// Probe 0 at t=0 bootstraps the responder. Probe 1 at the real 200ms cadence
	// is lost. The DATA barrier reaches its conservative T fallback at 250ms.
	coordinator.recordOffer(0, telemetryProbeHeader{sessionID: 0xCA01, probeSeq: 0}, offered)
	clock.advance(telemetry.DefaultProbeInterval)
	clock.advance(conservativeRecoveryService - telemetry.DefaultProbeInterval)
	select {
	case err := <-waitDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("DATA barrier did not release conservatively at T")
	}
	if coordinator.fastEligible() {
		t.Fatal("fallback enabled fast recovery without ACK")
	}

	// Probe 2 at t=400ms carries the live challenge. Its exact ACK remains
	// admissible even though DATA already resumed conservatively at t=250ms.
	clock.advance(telemetry.DefaultProbeInterval - (conservativeRecoveryService - telemetry.DefaultProbeInterval))
	const lateSeq = 2
	coordinator.recordOffer(0, telemetryProbeHeader{sessionID: 0xCA01, probeSeq: lateSeq, challenge: 2}, offered)
	ack := offer
	ack.Type = telemetry.RecoveryContractACK
	ackPayload, err := telemetry.EncodeRecoveryContract(ack)
	if err != nil {
		t.Fatal(err)
	}
	if !coordinator.acceptACK(0, 0xCA01, lateSeq, ackPayload) {
		t.Fatal("exact live ACK after conservative DATA fallback was rejected")
	}
	if !coordinator.fastEligible() {
		t.Fatal("late exact ACK did not enable still-valid fast recovery")
	}
}

func TestAcknowledgedLeaseRenewsBeforeUnsafeWindow(t *testing.T) {
	clock := newFakeClock()
	coordinator := newRecoveryContractCoordinator(0xCA02, clock)
	if err := coordinator.begin(true, 125*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	first := acknowledgeCurrentContract(t, coordinator, 0, 1)

	clock.advance(telemetry.RecoveryContractLifetime - 2*conservativeRecoveryService + time.Millisecond)
	next, recognized, err := telemetry.DecodeRecoveryContract(coordinator.payload())
	if err != nil || !recognized {
		t.Fatalf("renewal OFFER decode = recognized %v err %v", recognized, err)
	}
	if next.ContractID <= first.ContractID {
		t.Fatalf("lease did not renew before unsafe window: ContractID %d -> %d", first.ContractID, next.ContractID)
	}
	if next.Enabled != first.Enabled || next.ServiceBound != first.ServiceBound {
		t.Fatalf("renewal changed immutable service value: first=%+v next=%+v", first, next)
	}
	if !coordinator.fastEligible() {
		t.Fatal("starting renewal disabled the still-safe acknowledged lease")
	}
}

func TestLostInitialAndRenewalOffersKeepRotatingUntilExactLiveACK(t *testing.T) {
	clock := newFakeClock()
	coordinator := newRecoveryContractCoordinator(0xCA12, clock)
	if err := coordinator.begin(true, 110*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	first := coordinator.offerSnapshot()

	waitDone := make(chan error, 1)
	go func() { waitDone <- coordinator.awaitDecision() }()
	clock.advance(conservativeRecoveryService)
	select {
	case err := <-waitDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("initial lost OFFER did not release DATA conservatively")
	}

	clock.advance(telemetry.RecoveryContractLifetime - recoveryRenewBefore - conservativeRecoveryService + time.Millisecond)
	second := coordinator.offerSnapshot()
	if second.message.ContractID <= first.message.ContractID {
		t.Fatal("lost initial OFFER did not rotate before its validity ended")
	}
	clock.advance(telemetry.RecoveryContractLifetime - recoveryRenewBefore + time.Millisecond)
	third := coordinator.offerSnapshot()
	if third.message.ContractID <= second.message.ContractID {
		t.Fatal("lost second OFFER did not rotate before its validity ended")
	}

	const seq = 9
	coordinator.recordOffer(0, telemetryProbeHeader{sessionID: 0xCA12, probeSeq: seq, challenge: 4}, third)
	ack := messageWithType(third.message, telemetry.RecoveryContractACK)
	ackPayload, err := telemetry.EncodeRecoveryContract(ack)
	if err != nil {
		t.Fatal(err)
	}
	if !coordinator.acceptACK(0, 0xCA12, seq, ackPayload) {
		t.Fatal("exact live ACK after two lost OFFER generations was rejected")
	}
	if !coordinator.fastEligible() {
		t.Fatal("exact live ACK after offer loss did not enable fast recovery")
	}
}

func TestLostRenewalFallsBackBeforeOldLeaseBecomesUnsafe(t *testing.T) {
	clock := newFakeClock()
	coordinator := newRecoveryContractCoordinator(0xCA13, clock)
	if err := coordinator.begin(true, 115*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	first := acknowledgeCurrentContract(t, coordinator, 0, 1)

	clock.advance(telemetry.RecoveryContractLifetime - recoveryRenewBefore + time.Millisecond)
	second := coordinator.offerSnapshot()
	if second.message.ContractID <= first.ContractID {
		t.Fatal("first lease renewal did not rotate")
	}
	if !coordinator.fastEligible() {
		t.Fatal("safe old lease was dropped when its renewal started")
	}

	clock.advance(conservativeRecoveryService)
	if coordinator.fastEligible() {
		t.Fatal("lost renewal retained fast recovery inside the old lease's unsafe window")
	}
	const lateSeq = 2
	coordinator.recordOffer(0, telemetryProbeHeader{sessionID: 0xCA13, probeSeq: lateSeq, challenge: 5}, second)
	lateACK := messageWithType(second.message, telemetry.RecoveryContractACK)
	latePayload, err := telemetry.EncodeRecoveryContract(lateACK)
	if err != nil {
		t.Fatal(err)
	}
	if !coordinator.acceptACK(0, 0xCA13, lateSeq, latePayload) {
		t.Fatal("exact renewal ACK was rejected even though the renewal itself retained more than T validity")
	}
	if !coordinator.fastEligible() {
		t.Fatal("exact live renewal ACK did not restore fast recovery")
	}
}

func TestUnacknowledgedRenewalKeepsRotatingAfterOldLeaseExpires(t *testing.T) {
	clock := newFakeClock()
	coordinator := newRecoveryContractCoordinator(0xCA16, clock)
	if err := coordinator.begin(true, 115*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	first := acknowledgeCurrentContract(t, coordinator, 0, 1)
	clock.advance(telemetry.RecoveryContractLifetime - recoveryRenewBefore + time.Millisecond)
	second := coordinator.offerSnapshot().message
	if second.ContractID <= first.ContractID {
		t.Fatal("renewal did not start")
	}

	clock.advance(telemetry.RecoveryContractLifetime - recoveryRenewBefore + time.Millisecond)
	third := coordinator.offerSnapshot().message
	if third.ContractID <= second.ContractID {
		t.Fatal("unacknowledged renewal stopped rotating after the old lease expired")
	}
	if third.Enabled != first.Enabled || third.ServiceBound != first.ServiceBound {
		t.Fatalf("unacknowledged renewal retry changed service: first=%+v third=%+v", first, third)
	}
	if coordinator.fastEligible() {
		t.Fatal("unacknowledged renewal retry retained an expired fast lease")
	}
}

func TestSecondLeaseRenewalRotatesAgainWithoutChangingService(t *testing.T) {
	clock := newFakeClock()
	coordinator := newRecoveryContractCoordinator(0xCA14, clock)
	if err := coordinator.begin(true, 120*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	first := acknowledgeCurrentContract(t, coordinator, 0, 1)
	clock.advance(telemetry.RecoveryContractLifetime - recoveryRenewBefore + time.Millisecond)
	second := acknowledgeCurrentContract(t, coordinator, 0, 2)
	clock.advance(telemetry.RecoveryContractLifetime - recoveryRenewBefore + time.Millisecond)
	third := coordinator.offerSnapshot().message
	if first.ContractID >= second.ContractID || second.ContractID >= third.ContractID {
		t.Fatalf("renewal identity sequence = %d, %d, %d", first.ContractID, second.ContractID, third.ContractID)
	}
	if third.Enabled != first.Enabled || third.ServiceBound != first.ServiceBound {
		t.Fatalf("second renewal changed service: first=%+v third=%+v", first, third)
	}
	if !coordinator.fastEligible() {
		t.Fatal("second renewal disabled the still-safe acknowledged lease")
	}
}

func TestDisableWakesIndefiniteTransitionBarrier(t *testing.T) {
	coordinator := newRecoveryContractCoordinator(0xCA15, newFakeClock())
	if err := coordinator.begin(true, 125*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	coordinator.invalidateForTransition()
	done := make(chan error, 1)
	go func() { done <- coordinator.awaitDecision() }()
	coordinator.disable()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("disable did not wake transition-barrier waiter")
	}
}

func TestCloseCancelsPendingAsynchronousContractRotation(t *testing.T) {
	psk := testKey(t, 0x8B)
	clock := newFakeClock()
	m, _ := newProbingMultipathFEC(t, loopbackPaths(1), psk, &fec.Config{
		DataShards:   3,
		ParityShards: 1,
		Deadline:     20 * time.Millisecond,
	}, clock)
	cfg := recoveryReviewShaperConfig()
	cfg.RecoveryBound = 125 * time.Millisecond
	m.shaperConfigs = []config.PathShaperConfig{cfg}
	if _, _, err := m.Open(0); err != nil {
		t.Fatal(err)
	}
	m.lastWrite.Store(clock.Now().UnixNano())
	m.invalidateAndRotatePeerRecoveryContract(m.peerState)
	if !m.serviceTransitionPending.Load() {
		t.Fatal("asynchronous contract rotation was not scheduled")
	}
	closed := make(chan error, 1)
	go func() { closed <- m.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not cancel the pending fake-clock rotation wait")
	}
	deadline := time.Now().Add(time.Second)
	for m.serviceTransitionPending.Load() {
		if time.Now().After(deadline) {
			t.Fatal("cancelled rotation worker retained its pending latch")
		}
		time.Sleep(time.Millisecond)
	}
	m.mu.Lock()
	err := m.beginPeerRecoveryContractLocked(m.peerState)
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if payload := m.contracts.payload(); len(payload) != 0 {
		t.Fatal("late post-Close transition resurrected a recovery OFFER")
	}
}

func TestOldOfferCompletionCannotAuthorizeNewContractACK(t *testing.T) {
	clock := newFakeClock()
	coordinator := newRecoveryContractCoordinator(0xCA03, clock)
	if err := coordinator.begin(true, 125*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	oldSnapshot := coordinator.offerSnapshot()
	oldOffer, _, err := telemetry.DecodeRecoveryContract(oldSnapshot.payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.begin(true, 140*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	newOffer, _, err := telemetry.DecodeRecoveryContract(coordinator.payload())
	if err != nil {
		t.Fatal(err)
	}
	if newOffer.ContractID == oldOffer.ContractID {
		t.Fatal("precondition: contract did not rotate")
	}

	// Simulate completion of a priority write whose generator captured oldOffer
	// before the rotation. The completion must not be rebound to newOffer.
	const oldProbeSeq = 7
	coordinator.recordOffer(0, telemetryProbeHeader{sessionID: 0xCA03, probeSeq: oldProbeSeq, challenge: 3}, oldSnapshot)
	newACK := newOffer
	newACK.Type = telemetry.RecoveryContractACK
	newACKPayload, err := telemetry.EncodeRecoveryContract(newACK)
	if err != nil {
		t.Fatal(err)
	}
	if coordinator.acceptACK(0, 0xCA03, oldProbeSeq, newACKPayload) {
		t.Fatal("old OFFER completion authorized an ACK for the new contract identity")
	}
}

func TestBlockedProbeCompletionCannotRebindToRotatedOffer(t *testing.T) {
	psk := testKey(t, 0x7F)
	clock := newFakeClock()
	m, _ := newProbingMultipathFEC(t, loopbackPaths(1), psk, &fec.Config{
		DataShards:   3,
		ParityShards: 1,
		Deadline:     20 * time.Millisecond,
	}, clock)
	cfg := recoveryReviewShaperConfig()
	cfg.RecoveryBound = 125 * time.Millisecond
	m.shaperConfigs = []config.PathShaperConfig{cfg}
	if _, _, err := m.Open(0); err != nil {
		t.Fatal(err)
	}
	peer, peerAP := rawPeer(t)
	t.Cleanup(func() {
		_ = m.Close()
		_ = peer.Close()
	})
	bringProberUpClean(t, m.probers[0], psk, clock, testProbeUpSucc)
	path := m.paths[0]
	path.setRemote(peerAP)

	writeStarted := make(chan []byte, 1)
	releaseWrite := make(chan struct{})
	path.setWriteDeadline = func(time.Time) error { return nil }
	path.writeUDP = func(payload []byte, _ netip.AddrPort) (int, error) {
		writeStarted <- append([]byte(nil), payload...)
		<-releaseWrite
		return len(payload), nil
	}
	beforeBytes := path.txBytes.Load()
	m.emitProbes()
	var oldRaw []byte
	select {
	case oldRaw = <-writeStarted:
	case <-time.After(time.Second):
		t.Fatal("old OFFER probe did not enter the blocked production writer")
	}
	decoded, err := frame.Decode(psk, oldRaw)
	if err != nil {
		t.Fatal(err)
	}
	oldProbe, ok := decoded.(frame.Probe)
	if !ok || oldProbe.Challenge == 0 {
		t.Fatalf("blocked old OFFER probe = %T challenge=%d", decoded, oldProbe.Challenge)
	}
	oldOffer, recognized, err := telemetry.DecodeRecoveryContract(oldProbe.Payload)
	if err != nil || !recognized {
		t.Fatalf("blocked OFFER decode = recognized %v err %v", recognized, err)
	}

	if err := m.contracts.begin(true, 120*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	newSnapshot := m.contracts.offerSnapshot()
	if newSnapshot.message.ContractID <= oldOffer.ContractID {
		t.Fatal("test rotation did not advance ContractID")
	}
	newACK := messageWithType(newSnapshot.message, telemetry.RecoveryContractACK)
	newACKPayload, err := telemetry.EncodeRecoveryContract(newACK)
	if err != nil {
		t.Fatal(err)
	}
	close(releaseWrite)
	deadline := time.Now().Add(time.Second)
	for path.txBytes.Load() == beforeBytes {
		if time.Now().After(deadline) {
			t.Fatal("blocked old OFFER write did not complete")
		}
		time.Sleep(time.Millisecond)
	}
	if m.contracts.acceptACK(path.id, oldProbe.SessionID, oldProbe.ProbeSeq, newACKPayload) {
		t.Fatal("old emitted OFFER completion rebound to the rotated contract identity")
	}
}

func TestDeferredPromotionRotatesAcknowledgedServiceContract(t *testing.T) {
	psk := testKey(t, 0x7C)
	clock := newFakeClock()
	paths := []config.Path{
		{Name: "live", SourceAddr: netip.MustParseAddr("127.0.0.1")},
		{Name: "deferred", SourceAddr: netip.MustParseAddr("192.0.2.1")},
	}
	m, _ := newProbingMultipathFEC(t, paths, psk, &fec.Config{
		DataShards:   3,
		ParityShards: 1,
		Deadline:     20 * time.Millisecond,
	}, clock)
	cfg := recoveryReviewShaperConfig()
	cfg.RecoveryBound = 125 * time.Millisecond
	m.shaperConfigs = []config.PathShaperConfig{cfg, cfg}
	if _, _, err := m.Open(0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	if len(m.deferred) != 1 {
		t.Fatalf("deferred paths = %d, want 1", len(m.deferred))
	}
	old := acknowledgeCurrentContract(t, m.contracts, m.paths[0].id, 1)

	m.deferredListen = func(netip.Addr, uint16, string) (*net.UDPConn, error, error) {
		conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		return conn, nil, err
	}
	m.reconcileDeferred()
	if len(m.deferred) != 0 || len(m.paths) != 2 {
		t.Fatalf("promotion result = deferred %d paths %d, want 0/2", len(m.deferred), len(m.paths))
	}
	next, recognized, err := telemetry.DecodeRecoveryContract(m.contracts.payload())
	if err != nil || !recognized {
		t.Fatalf("post-promotion contract decode = recognized %v err %v", recognized, err)
	}
	if next.ContractID <= old.ContractID {
		t.Fatalf("deferred promotion retained old ContractID %d", old.ContractID)
	}
	if m.contracts.fastEligible() {
		t.Fatal("deferred promotion retained old fast-recovery eligibility before ACK")
	}
}

func TestUnresolvedDeferredRetryDoesNotFreezeOrRotateService(t *testing.T) {
	psk := testKey(t, 0x8D)
	clock := newFakeClock()
	paths := []config.Path{
		{Name: "live", SourceAddr: netip.MustParseAddr("127.0.0.1")},
		{Name: "deferred", SourceAddr: netip.MustParseAddr("192.0.2.1")},
	}
	m, _ := newProbingMultipathFEC(t, paths, psk, &fec.Config{
		DataShards:   3,
		ParityShards: 1,
		Deadline:     20 * time.Millisecond,
	}, clock)
	cfg := recoveryReviewShaperConfig()
	cfg.RecoveryBound = 125 * time.Millisecond
	m.shaperConfigs = []config.PathShaperConfig{cfg, cfg}
	if _, _, err := m.Open(0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	acknowledged := acknowledgeCurrentContract(t, m.contracts, m.paths[0].id, 1)
	m.lastWrite.Store(clock.Now().UnixNano())
	m.deferredListen = func(netip.Addr, uint16, string) (*net.UDPConn, error, error) {
		return nil, nil, syscall.EADDRNOTAVAIL
	}
	done := make(chan struct{})
	go func() {
		m.reconcileDeferred()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("unresolved deferred retry entered the service drain")
	}
	current := m.contracts.offerSnapshot().message
	if current.ContractID != acknowledged.ContractID || !m.contracts.fastEligible() {
		t.Fatalf("unresolved deferred retry changed acknowledged service: old=%+v current=%+v", acknowledged, current)
	}
}

func TestRemoveAndReaddRotateServiceWithoutChangingSession(t *testing.T) {
	psk := testKey(t, 0x8A)
	clock := newFakeClock()
	m, _ := newProbingMultipathFEC(t, loopbackPaths(2), psk, &fec.Config{
		DataShards:   3,
		ParityShards: 1,
		Deadline:     20 * time.Millisecond,
	}, clock)
	firstCfg := recoveryReviewShaperConfig()
	firstCfg.RecoveryBound = 110 * time.Millisecond
	secondCfg := recoveryReviewShaperConfig()
	secondCfg.RecoveryBound = 120 * time.Millisecond
	m.shaperConfigs = []config.PathShaperConfig{firstCfg, secondCfg}
	if _, _, err := m.Open(0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	session := m.probers[0].SessionID()
	first := acknowledgeCurrentContract(t, m.contracts, m.paths[0].id, 1)

	if err := m.RemovePath("b"); err != nil {
		t.Fatal(err)
	}
	removed := m.contracts.offerSnapshot().message
	if removed.ContractID <= first.ContractID || removed.ServiceBound != firstCfg.RecoveryBound {
		t.Fatalf("post-remove contract = %+v, first=%+v", removed, first)
	}
	if m.contracts.fastEligible() {
		t.Fatal("path removal retained the acknowledged old service")
	}
	_ = acknowledgeCurrentContract(t, m.contracts, m.paths[0].id, 2)

	readdedCfg := recoveryReviewShaperConfig()
	readdedCfg.RecoveryBound = 130 * time.Millisecond
	if err := m.AddPathWithShaper(config.Path{
		Name:       "b",
		SourceAddr: netip.MustParseAddr("127.0.0.1"),
	}, readdedCfg); err != nil {
		t.Fatal(err)
	}
	readded := m.contracts.offerSnapshot().message
	if readded.ContractID <= removed.ContractID || readded.ServiceBound != readdedCfg.RecoveryBound {
		t.Fatalf("post-readd contract = %+v, removed=%+v", readded, removed)
	}
	if m.probers[0].SessionID() != session {
		t.Fatal("ordinary remove/readd changed the probe SessionID")
	}
	beforeRejected := readded.ContractID
	if err := m.RemovePath("a"); err != nil {
		t.Fatal(err)
	}
	if err := m.RemovePath("b"); err == nil {
		t.Fatal("last live path removal unexpectedly succeeded")
	}
	if got := m.contracts.offerSnapshot().message.ContractID; got != beforeRejected+1 {
		t.Fatalf("rejected mutation rotated contract: ContractID=%d, want %d", got, beforeRejected+1)
	}
}

func readNextFECData(t testing.TB, peer *net.UDPConn, psk config.Key) frame.Data {
	t.Helper()
	if err := peer.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, maxDatagram)
	for {
		n, err := peer.Read(buffer)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := frame.Decode(psk, buffer[:n])
		if err != nil {
			t.Fatal(err)
		}
		if data, ok := decoded.(frame.Data); ok {
			return data
		}
	}
}

func TestCloseOpenPreservesFECGroupIDInSameBoot(t *testing.T) {
	psk := testKey(t, 0x7D)
	clock := newFakeClock()
	m, _ := newProbingMultipathFEC(t, loopbackPaths(1), psk, &fec.Config{
		DataShards:   3,
		ParityShards: 1,
		Deadline:     20 * time.Millisecond,
	}, clock)
	if _, _, err := m.Open(0); err != nil {
		t.Fatal(err)
	}
	peer, peerAP := rawPeer(t)
	t.Cleanup(func() {
		_ = m.Close()
		_ = peer.Close()
	})
	bringProberUpClean(t, m.probers[0], psk, clock, testProbeUpSucc)
	m.paths[0].setRemote(peerAP)
	session := m.probers[0].SessionID()
	if err := m.Send([][]byte{[]byte("a"), []byte("b"), []byte("c")}, m.virt); err != nil {
		t.Fatal(err)
	}
	first := readNextFECData(t, peer, psk)
	// Drain the remaining DATA from the first closed group so the post-Open read
	// cannot observe a datagram buffered by the old socket generation.
	_ = readNextFECData(t, peer, psk)
	_ = readNextFECData(t, peer, psk)
	outerBefore := m.outerSeq.Load()

	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.Open(0); err != nil {
		t.Fatal(err)
	}
	bringProberUpClean(t, m.probers[0], psk, clock, testProbeUpSucc)
	m.paths[0].setRemote(peerAP)
	if err := m.Send([][]byte{[]byte("d"), []byte("e"), []byte("f")}, m.virt); err != nil {
		t.Fatal(err)
	}
	second := readNextFECData(t, peer, psk)
	if m.probers[0].SessionID() != session {
		t.Fatal("same-process Close/Open changed probe SessionID")
	}
	if m.outerSeq.Load() <= outerBefore {
		t.Fatalf("OuterSeq did not continue: before=%d after=%d", outerBefore, m.outerSeq.Load())
	}
	if second.FECGroup <= first.FECGroup {
		t.Fatalf("FEC GroupID reset across same-boot Close/Open: %d -> %d", first.FECGroup, second.FECGroup)
	}
}

func TestCloseOpenAppliesRestartRequiredServiceConfigUnderNewContract(t *testing.T) {
	psk := testKey(t, 0x8C)
	clock := newFakeClock()
	m, _ := newProbingMultipathFEC(t, loopbackPaths(1), psk, &fec.Config{
		DataShards:   3,
		ParityShards: 1,
		Deadline:     20 * time.Millisecond,
	}, clock)
	oldCfg := recoveryReviewShaperConfig()
	oldCfg.RecoveryBound = 110 * time.Millisecond
	m.shaperConfigs = []config.PathShaperConfig{oldCfg}
	if _, _, err := m.Open(0); err != nil {
		t.Fatal(err)
	}
	first := acknowledgeCurrentContract(t, m.contracts, m.paths[0].id, 1)
	session := m.probers[0].SessionID()
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}

	newCfg := oldCfg
	newCfg.RateBytesPerSecond += 100_000
	newCfg.ProbeRateBytesPerSecond += 100
	newCfg.DataBurstBytes += 1000
	newCfg.ProbeBurstBytes += 1
	newCfg.PriorityReserveBytes += 1
	newCfg.FECGroupReserveBytes += 1000
	newCfg.RecoveryWriteSlack += time.Millisecond
	newCfg.RecoveryBound = 130 * time.Millisecond
	m.shaperConfigs = []config.PathShaperConfig{newCfg}
	if _, _, err := m.Open(0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	next := m.contracts.offerSnapshot().message
	if next.ContractID <= first.ContractID {
		t.Fatalf("restart-required service config reused ContractID %d", first.ContractID)
	}
	if next.ServiceBound != newCfg.RecoveryBound {
		t.Fatalf("post-restart service bound = %s, want %s", next.ServiceBound, newCfg.RecoveryBound)
	}
	if m.contracts.fastEligible() {
		t.Fatal("post-restart service config inherited the old ACK")
	}
	if m.probers[0].SessionID() != session {
		t.Fatal("same-process config restart changed probe SessionID")
	}
}

type blockedRecoveryACKShaper struct {
	recoveryPathShaper
	entered chan []byte
	release chan struct{}
}

func (s *blockedRecoveryACKShaper) TryWritePriority(payload []byte, write shaper.WriteFunc) (bool, <-chan error, error) {
	done := make(chan error, 1)
	s.entered <- append([]byte(nil), payload...)
	go func() {
		<-s.release
		done <- write(payload)
	}()
	return true, done, nil
}

func offerRecoveryContractBlocked(
	t testing.TB,
	m *Multipath,
	view *peerPathState,
	base recoveryPathShaper,
	psk config.Key,
	source netip.AddrPort,
	session, sequence, challenge uint64,
	message telemetry.RecoveryContractMessage,
) (*blockedRecoveryACKShaper, frame.Probe) {
	t.Helper()
	payload, err := telemetry.EncodeRecoveryContract(message)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := frame.Encode(psk, frame.Probe{
		PathID:         view.id,
		ProbeSeq:       sequence,
		TimestampNanos: time.Now().UnixNano(),
		SessionID:      session,
		Challenge:      challenge,
		Payload:        payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	blocked := &blockedRecoveryACKShaper{
		recoveryPathShaper: base,
		entered:            make(chan []byte, 1),
		release:            make(chan struct{}),
	}
	view.shaper = blocked
	m.handleInbound(view, raw, source)
	var echoedRaw []byte
	select {
	case echoedRaw = <-blocked.entered:
	case <-time.After(time.Second):
		t.Fatal("recovery ACK did not reach the blocked priority write")
	}
	decoded, err := frame.Decode(psk, echoedRaw)
	if err != nil {
		t.Fatal(err)
	}
	echo, ok := decoded.(frame.Probe)
	if !ok || !echo.IsEcho {
		t.Fatalf("blocked priority payload = %T, want PROBE echo", decoded)
	}
	ack, recognized, err := telemetry.DecodeRecoveryContract(echo.Payload)
	if err != nil || !recognized || ack.Type != telemetry.RecoveryContractACK ||
		ack.ContractID != message.ContractID {
		t.Fatalf("blocked echo payload = recognized %v err %v message %+v", recognized, err, ack)
	}
	return blocked, echo
}

func makeFECDecision(t testing.TB, cfg fec.Config, group fec.GroupID, payloads ...[]byte) *fec.GroupDecision {
	t.Helper()
	encoder, err := fec.NewEncoder(cfg, fec.SystemClock{})
	if err != nil {
		t.Fatal(err)
	}
	if err := encoder.SetNextGroup(group); err != nil {
		t.Fatal(err)
	}
	var decision *fec.GroupDecision
	for _, payload := range payloads {
		_, decision, err = encoder.AdmitOwned(append([]byte(nil), payload...))
		if err != nil {
			t.Fatal(err)
		}
	}
	if decision == nil {
		t.Fatal("test FEC group did not close")
	}
	return decision
}

func TestAuthenticatedOfferInstallsReceiverStateBeforeBlockedACK(t *testing.T) {
	psk := testKey(t, 0x7E)
	clock := newFakeClock()
	fecCfg := fec.Config{DataShards: 3, ParityShards: 1, Deadline: 20 * time.Millisecond}
	m, _ := newProbingMultipathFEC(t, loopbackPaths(1), psk, &fecCfg, clock)
	cfg := recoveryReviewShaperConfig()
	cfg.RecoveryBound = 125 * time.Millisecond
	m.shaperConfigs = []config.PathShaperConfig{cfg}
	if _, _, err := m.Open(0); err != nil {
		t.Fatal(err)
	}
	peer, peerAP := rawPeer(t)
	t.Cleanup(func() {
		_ = m.Close()
		_ = peer.Close()
	})
	view := m.paths[0]
	base, ok := view.shaper.(recoveryPathShaper)
	if !ok {
		t.Fatal("test path lacks recovery shaper")
	}
	codec, err := frame.NewCodec(psk)
	if err != nil {
		t.Fatal(err)
	}

	const firstSession = 0xCAFE01
	challenge := reflectProbeIssuedChallenge(t, m, view, psk, peer, peerAP, firstSession, 0, 0)
	firstOffer := telemetry.RecoveryContractMessage{
		Type:         telemetry.RecoveryContractOffer,
		Enabled:      true,
		ServiceBound: 125 * time.Millisecond,
		Lifetime:     telemetry.RecoveryContractLifetime,
		ContractID:   1,
	}
	firstBlocked, firstEcho := offerRecoveryContractBlocked(
		t, m, view, base, psk, peerAP, firstSession, 1, challenge, firstOffer,
	)
	close(firstBlocked.release)
	_ = readProbe(t, peer, codec)

	receiver := m.fecRecv.Load()
	if receiver == nil {
		t.Fatal("FEC receiver not instantiated")
	}
	receiver.dec.SetRetainWindow(2)
	completed := makeFECDecision(t, fecCfg, 10, []byte("c0"), []byte("c1"), []byte("c2"))
	if _, err := receiver.offer(completed.Data[1]); err != nil {
		t.Fatal(err)
	}
	if _, err := receiver.offer(completed.Data[2]); err != nil {
		t.Fatal(err)
	}
	recovered, err := receiver.offer(completed.Parity[0])
	if err != nil || len(recovered) != 1 {
		t.Fatalf("completed-group seed recovery = %d err %v", len(recovered), err)
	}
	incomplete := makeFECDecision(t, fecCfg, 11, []byte("i0"), []byte("i1"), []byte("i2"))
	if _, err := receiver.offer(incomplete.Data[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := receiver.offer(incomplete.Parity[0]); err != nil {
		t.Fatal(err)
	}
	recoveredBefore := receiver.stats().Recovered

	secondOffer := firstOffer
	secondOffer.ContractID = 2
	secondBlocked, _ := offerRecoveryContractBlocked(
		t, m, view, base, psk, peerAP, firstSession, 2, firstEcho.Challenge, secondOffer,
	)
	if got := m.resequencer.Load().Stats().Rebaselines; got != 0 {
		t.Fatalf("same-session contract rotation rebaselined resequencer: %d", got)
	}
	if got := receiver.stats().Recovered; got != recoveredBefore {
		t.Fatalf("completed decoder counters changed before ACK write: %d -> %d", recoveredBefore, got)
	}
	if got, err := receiver.offer(incomplete.Data[1]); err != nil || len(got) != 0 {
		t.Fatalf("incomplete old-contract state survived before ACK write: recovered=%d err=%v", len(got), err)
	}
	if got, err := receiver.offer(incomplete.Parity[0]); err != nil || len(got) != 0 {
		t.Fatalf("cleared group reconstructed without its discarded shard: recovered=%d err=%v", len(got), err)
	}
	old := makeFECDecision(t, fecCfg, 0, []byte("o0"), []byte("o1"), []byte("o2"))
	if got, err := receiver.offer(old.Data[1]); err != nil || len(got) != 0 {
		t.Fatalf("old high-water DATA handling = recovered %d err %v", len(got), err)
	}
	if got, err := receiver.offer(old.Data[2]); err != nil || len(got) != 0 {
		t.Fatalf("old high-water DATA handling = recovered %d err %v", len(got), err)
	}
	if got, err := receiver.offer(old.Parity[0]); err != nil || len(got) != 0 {
		t.Fatalf("receiver high-water reset before ACK write: recovered=%d err=%v", len(got), err)
	}
	close(secondBlocked.release)
	_ = readProbe(t, peer, codec)

	restartIncomplete := makeFECDecision(t, fecCfg, 12, []byte("r0"), []byte("r1"), []byte("r2"))
	if _, err := receiver.offer(restartIncomplete.Data[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := receiver.offer(restartIncomplete.Parity[0]); err != nil {
		t.Fatal(err)
	}
	const restartSession = 0xCAFE02
	view.shaper = base
	restartChallenge := reflectProbeIssuedChallenge(t, m, view, psk, peer, peerAP, restartSession, 0, 0)
	restartOffer := firstOffer
	restartBlocked, _ := offerRecoveryContractBlocked(
		t, m, view, base, psk, peerAP, restartSession, 1, restartChallenge, restartOffer,
	)
	if got := m.resequencer.Load().Stats().Rebaselines; got != 1 {
		t.Fatalf("authenticated restart did not rebaseline before ACK write: %d", got)
	}
	if got, err := receiver.offer(restartIncomplete.Data[1]); err != nil || len(got) != 0 {
		t.Fatalf("restart retained incomplete decoder state before ACK write: recovered=%d err=%v", len(got), err)
	}
	close(restartBlocked.release)
	_ = readProbe(t, peer, codec)
}
