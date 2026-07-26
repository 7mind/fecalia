package bind

import (
	"crypto/rand"
	"errors"
	"net"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/fec"
	"github.com/7mind/wanbond/internal/frame"
	"github.com/7mind/wanbond/internal/reseq"
	"github.com/7mind/wanbond/internal/telemetry"
)

func openNegotiatingPeer(t *testing.T, psk config.Key) *Multipath {
	t.Helper()
	clk := newFakeClock()
	paths := loopbackPaths(1)
	m, _ := newProbingMultipathFEC(t, paths, psk, &fec.Config{
		DataShards:   3,
		ParityShards: 1,
		Deadline:     20 * time.Millisecond,
	}, clk)
	cfg := recoveryReviewShaperConfig()
	cfg.RecoveryBound = 125 * time.Millisecond
	m.shaperConfigs = []config.PathShaperConfig{cfg}
	if _, _, err := m.Open(0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m
}

func joinNegotiatingPeers(t *testing.T, left, right *Multipath) {
	t.Helper()
	left.paths[0].setRemote(leftAddrPort(t, right.paths[0].conn))
	right.paths[0].setRemote(leftAddrPort(t, left.paths[0].conn))
	for range 4 {
		left.emitProbes()
		right.emitProbes()
		time.Sleep(10 * time.Millisecond)
	}
}

func leftAddrPort(t testing.TB, conn interface{ LocalAddr() net.Addr }) netip.AddrPort {
	t.Helper()
	addr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok {
		t.Fatalf("local address is %T, want *net.UDPAddr", conn.LocalAddr())
	}
	return addr.AddrPort()
}

func TestSameSessionPathAdditionInvalidatesAcknowledgedRecoveryBound(t *testing.T) {
	psk := testKey(t, 0x71)
	left := openNegotiatingPeer(t, psk)
	right := openNegotiatingPeer(t, psk)
	joinNegotiatingPeers(t, left, right)

	if !left.paths[0].recoveryContract().Enabled {
		t.Fatal("new↔new peer exchange did not acknowledge the initial fast recovery contract")
	}
	oldOuter := left.outerSeq.Load()
	cfg := recoveryReviewShaperConfig()
	cfg.RecoveryBound = 140 * time.Millisecond
	added := config.Path{Name: "added", SourceAddr: netip.MustParseAddr("127.0.0.1")}
	if err := left.AddPathWithShaper(added, cfg); err != nil {
		t.Fatal(err)
	}
	if left.paths[0].recoveryContract().Enabled {
		t.Fatal("same-session service change left cached old A eligible before a new ACK")
	}
	if left.outerSeq.Load() != oldOuter {
		t.Fatalf("ordinary contract rotation changed OuterSeq from %d to %d", oldOuter, left.outerSeq.Load())
	}
}

func TestRestartRebaselineCompletesBeforeLegacyAdoptionEchoWrite(t *testing.T) {
	psk := testKey(t, 0x72)
	m, _, _ := newProbingMultipath(t, loopbackPaths(1), psk, newFakeClock())
	if _, _, err := m.Open(0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	view := m.paths[0]
	peer, peerAP := rawPeer(t)

	challenge := reflectProbeIssuedChallenge(t, m, view, psk, peer, peerAP, preRestartSession, 0, 0)
	_ = reflectProbeIssuedChallenge(t, m, view, psk, peer, peerAP, preRestartSession, 1, challenge)
	deliverDATA(t, m, view, psk, restartHighSeq, []byte("old"), peerAP)
	if _, ok := m.resequencer.Load().Pop(); !ok {
		t.Fatal("old-session high DATA did not advance the resequencer")
	}
	restartChallenge := reflectProbeIssuedChallenge(t, m, view, psk, peer, peerAP, postRestartSession, 0, 0)

	writeEntered := make(chan struct{})
	releaseWrite := make(chan struct{})
	view.writeUDP = func(payload []byte, _ netip.AddrPort) (int, error) {
		close(writeEntered)
		<-releaseWrite
		return len(payload), nil
	}
	raw, err := frame.Encode(psk, frame.Probe{
		PathID:         view.id,
		ProbeSeq:       1,
		TimestampNanos: time.Now().UnixNano(),
		SessionID:      postRestartSession,
		Challenge:      restartChallenge,
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		m.handleInbound(view, raw, peerAP)
		close(done)
	}()
	select {
	case <-writeEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("adoption echo write did not start")
	}
	if got := m.resequencer.Load().Stats().Rebaselines; got != 1 {
		close(releaseWrite)
		<-done
		t.Fatalf("adoption echo write became visible before RebaselineToLow: Rebaselines=%d, want 1", got)
	}
	close(releaseWrite)
	<-done
}

func TestPathAdditionFreezesDATAUntilACKOrConservativeFallback(t *testing.T) {
	psk := testKey(t, 0x73)
	left := openNegotiatingPeer(t, psk)
	right := openNegotiatingPeer(t, psk)
	joinNegotiatingPeers(t, left, right)

	cfg := recoveryReviewShaperConfig()
	cfg.RecoveryBound = 140 * time.Millisecond
	if err := left.AddPathWithShaper(config.Path{Name: "added", SourceAddr: netip.MustParseAddr("127.0.0.1")}, cfg); err != nil {
		t.Fatal(err)
	}
	before := left.paths[0].txBytes.Load()
	sendDone := make(chan error, 1)
	go func() {
		sendDone <- left.Send([][]byte{[]byte("must-wait")}, left.virt)
	}()
	time.Sleep(30 * time.Millisecond)
	if got := left.paths[0].txBytes.Load(); got != before {
		t.Fatalf("DATA crossed service barrier before ACK/T fallback: txBytes %d -> %d", before, got)
	}
	select {
	case err := <-sendDone:
		t.Fatalf("Send crossed service barrier before ACK/T fallback: %v", err)
	default:
	}

	joinNegotiatingPeers(t, left, right)
	select {
	case err := <-sendDone:
		if err != nil {
			t.Fatalf("Send after ACK: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("matching ACK did not release frozen DATA")
	}
}

func TestRecoveryContractProbeUsesExactPriorityReservation(t *testing.T) {
	psk := testKey(t, 0x74)
	m := openNegotiatingPeer(t, psk)
	peer, peerAP := rawPeer(t)
	m.paths[0].setRemote(peerAP)
	beforeWire := m.paths[0].txBytes.Load()
	beforePriority := m.paths[0].shaper.(pathShaperReporter).Snapshot().OuterPriorityBytes

	m.emitProbes()
	codec, err := frame.NewCodec(psk)
	if err != nil {
		t.Fatal(err)
	}
	probe := readProbe(t, peer, codec)
	if got, want := len(probe.Payload), 27; got != want {
		t.Fatalf("contract payload length = %d, want %d", got, want)
	}
	if _, recognized, err := telemetry.DecodeRecoveryContract(probe.Payload); err != nil || !recognized {
		t.Fatalf("contract payload decode = recognized %v err %v", recognized, err)
	}
	wantWire := uint64(frame.UnpaddedProbeOnWire + len(probe.Payload))
	deadline := time.Now().Add(2 * time.Second)
	for m.paths[0].txBytes.Load() == beforeWire && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := m.paths[0].txBytes.Load() - beforeWire; got != wantWire {
		t.Fatalf("probe wire accounting = %d, want exact %d", got, wantWire)
	}
	afterPriority := m.paths[0].shaper.(pathShaperReporter).Snapshot().OuterPriorityBytes
	if got := afterPriority - beforePriority; got != wantWire {
		t.Fatalf("priority reservation/accounting = %d, want exact %d", got, wantWire)
	}
}

func TestRecoveryContractACKRequiresLatestSuccessfulLiveOffer(t *testing.T) {
	clock := newFakeClock()
	coordinator := newRecoveryContractCoordinator(0x6001, clock)
	if err := coordinator.begin(true, 125*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	offered := coordinator.offerSnapshot()
	offer, recognized, err := telemetry.DecodeRecoveryContract(offered.payload)
	if err != nil || !recognized {
		t.Fatalf("decode local offer = recognized %v err %v", recognized, err)
	}
	ack := offer
	ack.Type = telemetry.RecoveryContractACK
	ackPayload, err := telemetry.EncodeRecoveryContract(ack)
	if err != nil {
		t.Fatal(err)
	}

	if coordinator.acceptACK(0, 0x6001, 7, ackPayload) {
		t.Fatal("ACK accepted before its offer completed a successful write")
	}
	coordinator.recordOffer(0, telemetryProbeHeader{sessionID: 0x6001, probeSeq: 7}, offered)
	if coordinator.acceptACK(0, 0x6001, 7, ackPayload) {
		t.Fatal("ACK accepted for a bootstrap offer without a live peer challenge")
	}
	coordinator.recordOffer(0, telemetryProbeHeader{sessionID: 0x6001, probeSeq: 8, challenge: 9}, offered)
	if coordinator.acceptACK(0, 0x6001, 7, ackPayload) {
		t.Fatal("stale echo accepted after a newer offer became outstanding")
	}
	if coordinator.acceptACK(0, 0x6001, 8, coordinator.payload()) {
		t.Fatal("legacy verbatim OFFER echo treated as an ACK")
	}
	if !coordinator.acceptACK(0, 0x6001, 8, ackPayload) {
		t.Fatal("fresh authenticated ACK for the latest emitted live offer was rejected")
	}
}

func TestRecoveryContractACKDoesNotRefreshOriginalOfferValidity(t *testing.T) {
	clock := newFakeClock()
	coordinator := newRecoveryContractCoordinator(0x6101, clock)
	if err := coordinator.begin(true, 125*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	offered := coordinator.offerSnapshot()
	offer, _, err := telemetry.DecodeRecoveryContract(offered.payload)
	if err != nil {
		t.Fatal(err)
	}
	ack := offer
	ack.Type = telemetry.RecoveryContractACK
	ackPayload, err := telemetry.EncodeRecoveryContract(ack)
	if err != nil {
		t.Fatal(err)
	}
	coordinator.recordOffer(0, telemetryProbeHeader{sessionID: 0x6101, probeSeq: 9, challenge: 10}, offered)
	clock.advance(telemetry.RecoveryContractLifetime - conservativeRecoveryService + time.Millisecond)
	if coordinator.acceptACK(0, 0x6101, 9, ackPayload) {
		t.Fatal("late ACK refreshed validity beyond the original OFFER interval")
	}
}

func TestRecoveryContractRepeatedOfferDoesNotRefreshAndExpiredOfferStaysStale(t *testing.T) {
	clock := newFakeClock()
	coordinator := newRecoveryContractCoordinator(0x7001, clock)
	offer := telemetry.RecoveryContractMessage{
		Type:         telemetry.RecoveryContractOffer,
		Enabled:      true,
		ServiceBound: 125 * time.Millisecond,
		Lifetime:     telemetry.RecoveryContractLifetime,
		ContractID:   4,
	}
	installed := 0
	install := func() { installed++ }
	if _, ok := coordinator.acceptOffer(0x7002, offer, install); !ok {
		t.Fatal("initial OFFER rejected")
	}
	clock.advance(951 * time.Millisecond)
	if _, ok := coordinator.acceptOffer(0x7002, offer, install); ok {
		t.Fatal("repeated OFFER refreshed acceptedAt inside the final T window")
	}
	clock.advance(250 * time.Millisecond)
	if _, ok := coordinator.acceptOffer(0x7002, offer, install); ok {
		t.Fatal("expired old OFFER identity was adopted again")
	}
	if installed != 1 {
		t.Fatalf("contract install count = %d, want 1", installed)
	}
}

func TestRecoveryContractLegacyFallbackReleasesAtT(t *testing.T) {
	clock := newFakeClock()
	coordinator := newRecoveryContractCoordinator(0x8001, clock)
	if err := coordinator.begin(true, 125*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- coordinator.awaitDecision() }()
	deadline := time.Now().Add(time.Second)
	for {
		clock.mu.Lock()
		timerCount := len(clock.timers)
		clock.mu.Unlock()
		if timerCount > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fallback waiter did not arm its clock timer")
		}
		time.Sleep(time.Millisecond)
	}
	clock.advance(conservativeRecoveryService - time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("legacy fallback released before T: %v", err)
	default:
	}
	clock.advance(time.Millisecond)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("legacy fallback did not release at T")
	}
	if coordinator.fastEligible() {
		t.Fatal("legacy fallback enabled fast recovery without an ACK")
	}
}

func TestNoOfferProbeCadenceCannotPostponeConservativeGapDeadline(t *testing.T) {
	m, _, clock := openRecoveryWindowPeer(t, 1)
	peer, source := rawPeer(t)
	const session = uint64(0x8002)

	echo := dispatchLegacyProbe(t, m, peer, source, session, 0, 0)
	echo = dispatchLegacyProbe(t, m, peer, source, session, 1, echo.Challenge)
	beforeGeneration := m.contracts.receivedSnapshot().generation

	deliverDATA(t, m, m.paths[0], m.psk, 0, []byte("before-gap"), source)
	if item, ok := m.resequencer.Load().Pop(); !ok || string(item.Payload) != "before-gap" {
		t.Fatalf("pre-gap release = payload %q ok=%v", item.Payload, ok)
	}
	deliverDATA(t, m, m.paths[0], m.psk, 2, []byte("after-gap"), source)
	rq := m.resequencer.Load()
	originalDeadline, armed := rq.ArmedDeadline()
	if !armed || originalDeadline != clock.Now().Add(conservativeRecoveryService) {
		t.Fatalf("initial fallback gap = %v,%v, want now+T,true", originalDeadline, armed)
	}

	for sequence := uint64(2); sequence < 5; sequence++ {
		clock.advance(100 * time.Millisecond)
		echo = dispatchLegacyProbe(t, m, peer, source, session, sequence, echo.Challenge)
	}
	if clock.Now().Before(originalDeadline) {
		t.Fatalf("probe cadence stopped before original deadline: now=%v deadline=%v", clock.Now(), originalDeadline)
	}
	item, ok := rq.Pop()
	if !ok || string(item.Payload) != "after-gap" {
		t.Fatalf("no-offer probes postponed fallback release: payload %q ok=%v stats=%+v",
			item.Payload, ok, rq.Stats())
	}
	if got := m.contracts.receivedSnapshot().generation; got != beforeGeneration {
		t.Fatalf("no-offer probes advanced recovery generation from %d to %d", beforeGeneration, got)
	}
}

func TestLegacyProbeRevokesAcknowledgedRecoveryEvidenceOnce(t *testing.T) {
	m, _, _ := openRecoveryWindowPeer(t, 1)
	peer, source := rawPeer(t)
	const session = uint64(0x8003)

	echo := dispatchLegacyProbe(t, m, peer, source, session, 0, 0)
	echo = dispatchLegacyProbe(t, m, peer, source, session, 1, echo.Challenge)
	message := receiverContractMessage(1, 80*time.Millisecond)
	if _, ok := m.contracts.acceptOffer(session, message, func() {}); !ok {
		t.Fatal("recovery offer rejected")
	}
	pathKey := reseqPathKey(m.paths[0].id, m.paths[0].id)
	if !completeReceiverACK(m.contracts, session, message, pathKey, source) {
		t.Fatal("recovery ACK completion rejected")
	}
	before := m.contracts.receivedSnapshot()
	if len(before.venues) != 1 {
		t.Fatalf("acknowledged venues = %d, want 1", len(before.venues))
	}

	echo = dispatchLegacyProbe(t, m, peer, source, session, 2, echo.Challenge)
	afterFirst := m.contracts.receivedSnapshot()
	if afterFirst.generation != before.generation+1 || len(afterFirst.venues) != 0 {
		t.Fatalf("first legacy probe revocation = generation %d venues %d, want %d/0",
			afterFirst.generation, len(afterFirst.venues), before.generation+1)
	}
	dispatchLegacyProbe(t, m, peer, source, session, 3, echo.Challenge)
	afterSecond := m.contracts.receivedSnapshot()
	if afterSecond.generation != afterFirst.generation {
		t.Fatalf("already-revoked evidence advanced generation from %d to %d",
			afterFirst.generation, afterSecond.generation)
	}
}

func TestNoACKProbeInvalidatesBlockedACKAdmissionOnce(t *testing.T) {
	m, _, clock := openRecoveryWindowPeer(t, 1)
	peer, source := rawPeer(t)
	view := m.paths[0]
	const session = uint64(0x8004)

	echo := dispatchLegacyProbe(t, m, peer, source, session, 0, 0)
	echo = dispatchLegacyProbe(t, m, peer, source, session, 1, echo.Challenge)
	deliverDATA(t, m, view, m.psk, 0, []byte("before-gap"), source)
	if item, ok := m.resequencer.Load().Pop(); !ok || string(item.Payload) != "before-gap" {
		t.Fatalf("pre-gap release = payload %q ok=%v", item.Payload, ok)
	}
	deliverDATA(t, m, view, m.psk, 2, []byte("after-gap"), source)

	message := receiverContractMessage(1, 80*time.Millisecond)
	payload, err := telemetry.EncodeRecoveryContract(message)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := frame.Encode(m.psk, frame.Probe{
		PathID:         view.id,
		ProbeSeq:       2,
		TimestampNanos: clock.Now().UnixNano(),
		SessionID:      session,
		Challenge:      echo.Challenge,
		Payload:        payload,
	})
	if err != nil {
		t.Fatal(err)
	}

	originalWrite := view.writeUDP
	blockedWire := make(chan []byte, 1)
	releaseWrite := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-releaseWrite:
		default:
			close(releaseWrite)
		}
	})
	var writes atomic.Uint32
	view.writeUDP = func(payload []byte, destination netip.AddrPort) (int, error) {
		if writes.Add(1) == 1 {
			blockedWire <- append([]byte(nil), payload...)
			<-releaseWrite
		}
		if originalWrite == nil {
			return view.conn.WriteToUDPAddrPort(payload, destination)
		}
		return originalWrite(payload, destination)
	}
	firstDone := make(chan struct{})
	go func() {
		m.handleInbound(view, raw, source)
		close(firstDone)
	}()

	var blockedEcho frame.Probe
	select {
	case wire := <-blockedWire:
		decoded, decodeErr := frame.Decode(m.psk, wire)
		if decodeErr != nil {
			t.Fatal(decodeErr)
		}
		var ok bool
		blockedEcho, ok = decoded.(frame.Probe)
		if !ok {
			t.Fatalf("blocked recovery echo = %T, want PROBE", decoded)
		}
	case <-time.After(time.Second):
		t.Fatal("recovery ACK write did not block")
	}
	if snapshot := m.contracts.receivedSnapshot(); len(snapshot.venues) != 0 {
		t.Fatalf("blocked ACK published %d venues before write completion", len(snapshot.venues))
	}

	clock.advance(100 * time.Millisecond)
	legacyEcho := dispatchLegacyProbe(t, m, peer, source, session, 3, blockedEcho.Challenge)
	afterFirst := m.contracts.receivedSnapshot()
	firstDeadline, armed := m.resequencer.Load().ArmedDeadline()
	if !armed || firstDeadline != clock.Now().Add(conservativeRecoveryService) {
		t.Fatalf("pending-ACK invalidation deadline = %v,%v, want now+T,true", firstDeadline, armed)
	}

	close(releaseWrite)
	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("blocked recovery ACK write did not finish")
	}
	staleEcho := readProbe(t, peer, mustFrameCodec(t, m.psk))
	if len(staleEcho.Payload) == 0 {
		t.Fatal("blocked recovery ACK lost its encoded contract payload")
	}
	if snapshot := m.contracts.receivedSnapshot(); len(snapshot.venues) != 0 {
		t.Fatalf("stale blocked ACK restored %d venues", len(snapshot.venues))
	}

	clock.advance(100 * time.Millisecond)
	dispatchLegacyProbe(t, m, peer, source, session, 4, legacyEcho.Challenge)
	afterSecond := m.contracts.receivedSnapshot()
	if afterSecond.generation != afterFirst.generation {
		t.Fatalf("post-invalidation legacy probe advanced generation from %d to %d",
			afterFirst.generation, afterSecond.generation)
	}
	if deadline, armed := m.resequencer.Load().ArmedDeadline(); !armed || deadline != firstDeadline {
		t.Fatalf("post-invalidation legacy probe moved deadline from %v to %v,%v",
			firstDeadline, deadline, armed)
	}

	clock.advance(firstDeadline.Sub(clock.Now()))
	item, ok := m.resequencer.Load().Pop()
	if !ok || string(item.Payload) != "after-gap" {
		t.Fatalf("pending-ACK fallback release = payload %q ok=%v stats=%+v",
			item.Payload, ok, m.resequencer.Load().Stats())
	}
}

func TestFailedACKWriteLeavesNoEvidenceForLegacyProbeToInvalidate(t *testing.T) {
	m, _, clock := openRecoveryWindowPeer(t, 1)
	peer, source := rawPeer(t)
	view := m.paths[0]
	const session = uint64(0x8005)

	echo := dispatchLegacyProbe(t, m, peer, source, session, 0, 0)
	echo = dispatchLegacyProbe(t, m, peer, source, session, 1, echo.Challenge)
	deliverDATA(t, m, view, m.psk, 0, []byte("before-gap"), source)
	if item, ok := m.resequencer.Load().Pop(); !ok || string(item.Payload) != "before-gap" {
		t.Fatalf("pre-gap release = payload %q ok=%v", item.Payload, ok)
	}
	deliverDATA(t, m, view, m.psk, 2, []byte("after-gap"), source)

	message := receiverContractMessage(1, 80*time.Millisecond)
	payload, err := telemetry.EncodeRecoveryContract(message)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := frame.Encode(m.psk, frame.Probe{
		PathID:         view.id,
		ProbeSeq:       2,
		TimestampNanos: clock.Now().UnixNano(),
		SessionID:      session,
		Challenge:      echo.Challenge,
		Payload:        payload,
	})
	if err != nil {
		t.Fatal(err)
	}

	originalWrite := view.writeUDP
	var failedWire []byte
	view.writeUDP = func(payload []byte, _ netip.AddrPort) (int, error) {
		failedWire = append([]byte(nil), payload...)
		return 0, errors.New("injected ACK write failure")
	}
	m.handleInbound(view, raw, source)
	view.writeUDP = originalWrite
	decoded, err := frame.Decode(m.psk, failedWire)
	if err != nil {
		t.Fatal(err)
	}
	failedEcho, ok := decoded.(frame.Probe)
	if !ok {
		t.Fatalf("failed recovery echo = %T, want PROBE", decoded)
	}

	before := m.contracts.receivedSnapshot()
	if len(before.venues) != 0 {
		t.Fatalf("failed ACK write published %d venues", len(before.venues))
	}
	originalDeadline, armed := m.resequencer.Load().ArmedDeadline()
	if !armed {
		t.Fatal("failed ACK write left no conservative gap armed")
	}

	clock.advance(100 * time.Millisecond)
	dispatchLegacyProbe(t, m, peer, source, session, 3, failedEcho.Challenge)
	after := m.contracts.receivedSnapshot()
	if after.generation != before.generation {
		t.Fatalf("legacy probe invalidated failed ACK generation %d -> %d",
			before.generation, after.generation)
	}
	if deadline, armed := m.resequencer.Load().ArmedDeadline(); !armed || deadline != originalDeadline {
		t.Fatalf("legacy probe moved failed-ACK deadline from %v to %v,%v",
			originalDeadline, deadline, armed)
	}

	clock.advance(originalDeadline.Sub(clock.Now()))
	item, ok := m.resequencer.Load().Pop()
	if !ok || string(item.Payload) != "after-gap" {
		t.Fatalf("failed-ACK fallback release = payload %q ok=%v stats=%+v",
			item.Payload, ok, m.resequencer.Load().Stats())
	}
}

func TestReceivedACKCancellationPreservesOtherPendingAdmissions(t *testing.T) {
	coordinator := newRecoveryContractCoordinator(0x8006, newFakeClock())
	const remoteSession = uint64(0x8007)
	message := receiverContractMessage(1, 80*time.Millisecond)
	if _, ok := coordinator.acceptOffer(remoteSession, message, func() {}); !ok {
		t.Fatal("recovery offer rejected")
	}
	firstKey, secondKey := uint32(1), uint32(2)
	coordinator.observeReceivedSource(firstKey, receiverContractSource)
	coordinator.observeReceivedSource(secondKey, receiverStandbySource)
	first, ok := coordinator.admitReceivedACK(
		remoteSession, message, firstKey, receiverContractSource,
	)
	if !ok {
		t.Fatal("first ACK admission rejected")
	}
	second, ok := coordinator.admitReceivedACK(
		remoteSession, message, secondKey, receiverStandbySource,
	)
	if !ok {
		t.Fatal("second ACK admission rejected")
	}
	if !coordinator.cancelReceivedACK(first) {
		t.Fatal("first ACK cancellation rejected")
	}
	before := coordinator.receivedSnapshot().generation
	generation, changed := coordinator.invalidateReceivedFastEvidence()
	if !changed || generation != before+1 {
		t.Fatalf("surviving ACK invalidation = generation %d changed %v, want %d/true",
			generation, changed, before+1)
	}
	if coordinator.completeReceivedACK(second) {
		t.Fatal("invalidated surviving ACK admission completed")
	}

	next := receiverContractMessage(2, 80*time.Millisecond)
	if _, ok := coordinator.acceptOffer(remoteSession, next, func() {}); !ok {
		t.Fatal("replacement recovery offer rejected")
	}
	coordinator.observeReceivedSource(firstKey, receiverContractSource)
	coordinator.observeReceivedSource(secondKey, receiverStandbySource)
	first, ok = coordinator.admitReceivedACK(
		remoteSession, next, firstKey, receiverContractSource,
	)
	if !ok {
		t.Fatal("replacement first ACK admission rejected")
	}
	second, ok = coordinator.admitReceivedACK(
		remoteSession, next, secondKey, receiverStandbySource,
	)
	if !ok {
		t.Fatal("replacement second ACK admission rejected")
	}
	if !coordinator.cancelReceivedACK(first) || !coordinator.cancelReceivedACK(second) {
		t.Fatal("replacement ACK cancellation rejected")
	}
	before = coordinator.receivedSnapshot().generation
	if generation, changed = coordinator.invalidateReceivedFastEvidence(); changed || generation != before {
		t.Fatalf("fully cancelled ACK invalidation = generation %d changed %v, want %d/false",
			generation, changed, before)
	}
}

func TestRecoveryContractStateIsPeerScoped(t *testing.T) {
	clock := newFakeClock()
	first := newRecoveryContractCoordinator(0x9001, clock)
	second := newRecoveryContractCoordinator(0x9001, clock)
	firstInstalls := 0
	offer := telemetry.RecoveryContractMessage{
		Type:         telemetry.RecoveryContractOffer,
		Enabled:      true,
		ServiceBound: 125 * time.Millisecond,
		Lifetime:     telemetry.RecoveryContractLifetime,
		ContractID:   1,
	}
	if _, ok := first.acceptOffer(0x9002, offer, func() { firstInstalls++ }); !ok {
		t.Fatal("first peer rejected OFFER")
	}
	if _, ok := second.acceptOffer(0x9002, offer, func() {}); !ok {
		t.Fatal("second peer rejected identical identity in its independent scope")
	}
	inconsistent := offer
	inconsistent.ServiceBound = 140 * time.Millisecond
	if _, ok := first.acceptOffer(0x9002, inconsistent, func() { firstInstalls++ }); ok {
		t.Fatal("first peer accepted inconsistent immutable identity")
	}
	if firstInstalls != 2 {
		t.Fatalf("inconsistent immutable identity installs = %d, want initial install plus fail-closed clear", firstInstalls)
	}
	if _, ok := first.acceptOffer(0x9002, inconsistent, func() { firstInstalls++ }); ok || firstInstalls != 2 {
		t.Fatalf("repeated invalid identity = accepted %v installs %d, want rejected without another clear", ok, firstInstalls)
	}
	if _, ok := second.acceptOffer(0x9002, offer, func() {}); !ok {
		t.Fatal("first peer's inconsistency invalidated the second peer")
	}
}

func TestRecoveryGenerationFailureInvalidatesNegotiatedContract(t *testing.T) {
	psk := testKey(t, 0x75)
	left := openNegotiatingPeer(t, psk)
	right := openNegotiatingPeer(t, psk)
	joinNegotiatingPeers(t, left, right)
	if !left.contracts.fastEligible() {
		t.Fatal("precondition: recovery contract was not acknowledged")
	}

	left.abortRecoveryGeneration(left.paths[0], errors.New("forced recovery failure"))
	if left.contracts.fastEligible() {
		t.Fatal("failed writer generation retained its acknowledged recovery contract")
	}
}

func TestCloseReleasesRecoveryContractBarrier(t *testing.T) {
	psk := testKey(t, 0x76)
	m := openNegotiatingPeer(t, psk)
	sendDone := make(chan error, 1)
	go func() {
		sendDone <- m.Send([][]byte{[]byte("waiting-for-contract")}, m.virt)
	}()
	time.Sleep(10 * time.Millisecond)
	select {
	case err := <-sendDone:
		t.Fatalf("Send did not wait for ACK/T before Close: %v", err)
	default:
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-sendDone:
	case <-time.After(time.Second):
		t.Fatal("Close left Send blocked on recovery-contract negotiation")
	}
}

func TestRecoveryContractAdvertisesMaximumPathServiceBound(t *testing.T) {
	psk := testKey(t, 0x77)
	clock := newFakeClock()
	m, _ := newProbingMultipathFEC(t, loopbackPaths(2), psk, &fec.Config{
		DataShards:   3,
		ParityShards: 1,
		Deadline:     20 * time.Millisecond,
	}, clock)
	first := recoveryReviewShaperConfig()
	first.RecoveryBound = 125 * time.Millisecond
	second := recoveryReviewShaperConfig()
	second.RecoveryBound = 140 * time.Millisecond
	m.shaperConfigs = []config.PathShaperConfig{first, second}
	if _, _, err := m.Open(0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })

	message, recognized, err := telemetry.DecodeRecoveryContract(m.contracts.payload())
	if err != nil || !recognized {
		t.Fatalf("decode local contract = recognized %v err %v", recognized, err)
	}
	if !message.Enabled || message.ServiceBound != 140*time.Millisecond {
		t.Fatalf("local contract = %+v, want enabled max Sdevice 140ms", message)
	}
}

func TestSharedConcentratorSocketAdvertisesDisabledPerPeerContracts(t *testing.T) {
	pskA := testKey(t, 0x78)
	pskB := testKey(t, 0x79)
	clock := newFakeClock()
	paths := loopbackPaths(1)
	m, _ := newProbingMultipathFEC(t, paths, pskA, &fec.Config{
		DataShards:   3,
		ParityShards: 1,
		Deadline:     20 * time.Millisecond,
	}, clock)
	betaScheduler, betaProbers, betaFactory := concPeerWiring(t, paths, pskB, 0xC002, clock)
	if err := m.AddConcentratorPeer("beta", pskB, betaScheduler, betaProbers, betaFactory); err != nil {
		t.Fatal(err)
	}
	cfg := recoveryReviewShaperConfig()
	cfg.RecoveryBound = 125 * time.Millisecond
	m.shaperConfigs = []config.PathShaperConfig{cfg}
	if _, _, err := m.Open(0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })

	for peerIndex, psk := range []config.Key{pskA, pskB} {
		raw, err := m.PeerBootProbe(peerIndex, 0)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := frame.Decode(psk, raw)
		if err != nil {
			t.Fatal(err)
		}
		probe := decoded.(frame.Probe)
		message, recognized, err := telemetry.DecodeRecoveryContract(probe.Payload)
		if err != nil || !recognized {
			t.Fatalf("peer %d contract decode = recognized %v err %v", peerIndex, recognized, err)
		}
		if message.Enabled {
			t.Fatalf("peer %d shared-socket contract = %+v, want disabled", peerIndex, message)
		}
		other := pskA
		if peerIndex == 0 {
			other = pskB
		}
		if decoded, err := frame.Decode(other, raw); err == nil && decoded.Kind() == frame.KindProbe {
			t.Fatalf("peer %d contract probe authenticated under another peer's PSK", peerIndex)
		}
	}
}

func TestLegacyReflectorEchoCannotAcknowledgeRecoveryContract(t *testing.T) {
	psk := testKey(t, 0x7A)
	m := openNegotiatingPeer(t, psk)
	peer, peerAP := rawPeer(t)
	m.paths[0].setRemote(peerAP)
	legacy := telemetry.NewReflector(psk, rand.Reader)
	buffer := make([]byte, maxDatagram)

	for range 2 {
		m.emitProbes()
		if err := peer.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatal(err)
		}
		n, err := peer.Read(buffer)
		if err != nil {
			t.Fatal(err)
		}
		echo, _, err := legacy.Reflect(buffer[:n])
		if err != nil {
			t.Fatal(err)
		}
		m.handleInbound(m.paths[0], echo, peerAP)
	}
	if m.contracts.fastEligible() {
		t.Fatal("legacy verbatim OFFER reflection enabled fast recovery")
	}
}

func TestProductionEmptyProbeEchoDoesNotCountACKRejection(t *testing.T) {
	psk := testKey(t, 0x7B)
	m := openNegotiatingPeer(t, psk)
	peer, peerAP := rawPeer(t)
	m.paths[0].setRemote(peerAP)
	buffer := make([]byte, maxDatagram)

	m.emitProbes()
	if err := peer.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	n, err := peer.Read(buffer)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := frame.Decode(psk, buffer[:n])
	if err != nil {
		t.Fatal(err)
	}
	probe, ok := decoded.(frame.Probe)
	if !ok {
		t.Fatalf("outbound frame = %T, want PROBE", decoded)
	}
	probe.IsEcho = true
	probe.Payload = nil
	emptyEcho, err := frame.Encode(psk, probe)
	if err != nil {
		t.Fatal(err)
	}
	before := m.contracts.stats().Sender
	m.handleInbound(m.paths[0], emptyEcho, peerAP)
	after := m.contracts.stats().Sender
	if after.WrongRejections != before.WrongRejections ||
		after.FallbackReason != before.FallbackReason {
		t.Fatalf("empty legacy echo changed sender rejection telemetry: before=%+v after=%+v", before, after)
	}
}

func readRecoveryProbeWire(t testing.TB, peer *net.UDPConn, psk config.Key) ([]byte, frame.Probe) {
	t.Helper()
	raw, decoded := readRecoveryWire(t, peer, psk)
	probe, ok := decoded.(frame.Probe)
	if !ok {
		t.Fatalf("wire frame = %T, want PROBE", decoded)
	}
	return raw, probe
}

func readRecoveryWire(t testing.TB, peer *net.UDPConn, psk config.Key) ([]byte, frame.Frame) {
	t.Helper()
	if err := peer.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	raw := make([]byte, maxDatagram)
	n, err := peer.Read(raw)
	if err != nil {
		t.Fatal(err)
	}
	raw = raw[:n]
	decoded, err := frame.Decode(psk, raw)
	if err != nil {
		t.Fatal(err)
	}
	return raw, decoded
}

func dispatchLegacyProbe(
	t testing.TB,
	m *Multipath,
	peer *net.UDPConn,
	source netip.AddrPort,
	session, sequence, challenge uint64,
) frame.Probe {
	t.Helper()
	dispatchTestProbe(t, m, m.paths[0], source, frame.Probe{
		PathID:         m.paths[0].id,
		ProbeSeq:       sequence,
		TimestampNanos: m.clock.Now().UnixNano(),
		SessionID:      session,
		Challenge:      challenge,
	})
	echo := readProbe(t, peer, mustFrameCodec(t, m.psk))
	if !echo.IsEcho || len(echo.Payload) != 0 {
		t.Fatalf("legacy echo = %+v, want authenticated extension-free echo", echo)
	}
	return echo
}

func reflectLegacyRecoveryOffer(
	t testing.TB,
	m *Multipath,
	peer *net.UDPConn,
	source netip.AddrPort,
	legacy *telemetry.Reflector,
) telemetry.RecoveryContractMessage {
	t.Helper()
	beforeWrites := m.contracts.stats().Sender.OfferWrites
	m.emitProbes()
	raw, probe := readRecoveryProbeWire(t, peer, m.psk)
	message, recognized, err := telemetry.DecodeRecoveryContract(probe.Payload)
	if err != nil || !recognized || message.Type != telemetry.RecoveryContractOffer {
		t.Fatalf("new sender wire OFFER = recognized %v err %v message %+v", recognized, err, message)
	}
	echo, _, err := legacy.Reflect(raw)
	if err != nil {
		t.Fatal(err)
	}
	m.handleInbound(m.paths[0], echo, source)
	waitForRecoveryCondition(t, func() bool {
		return m.contracts.stats().Sender.OfferWrites > beforeWrites
	})
	return message
}

type compatibilityContinuity struct {
	outerSeq           uint64
	nextGroup          uint32
	receivedGeneration uint64
	reseq              reseq.Stats
	fec                FECStats
}

func compatibilitySnapshot(m *Multipath) compatibilityContinuity {
	snapshot := m.PeerSnapshots()[0]
	return compatibilityContinuity{
		outerSeq:           m.outerSeq.Load(),
		nextGroup:          m.fecNextGroup.Load(),
		receivedGeneration: m.contracts.receivedSnapshot().generation,
		reseq:              snapshot.Reseq,
		fec:                snapshot.FEC,
	}
}

func assertCompatibilityContinuity(
	t testing.TB,
	baseline, current compatibilityContinuity,
	wantOuter uint64,
	wantNextGroup uint32,
	wantRebaselineDelta uint64,
) {
	t.Helper()
	if current.outerSeq != wantOuter || current.nextGroup != wantNextGroup {
		t.Fatalf("sender continuity = OuterSeq %d GroupID high-water %d, want %d/%d (baseline %d/%d)",
			current.outerSeq, current.nextGroup, wantOuter, wantNextGroup, baseline.outerSeq, baseline.nextGroup)
	}
	if current.reseq.Rebaselines != baseline.reseq.Rebaselines+wantRebaselineDelta ||
		current.reseq.Resyncs != baseline.reseq.Resyncs {
		t.Fatalf("receiver continuity reset: baseline=%+v current=%+v want Rebaselines delta=%d",
			baseline.reseq, current.reseq, wantRebaselineDelta)
	}
	if current.fec.Recovered != baseline.fec.Recovered ||
		current.fec.Unrecoverable != baseline.fec.Unrecoverable {
		t.Fatalf("decoder outcome counters changed across ordinary compatibility traffic: baseline=%+v current=%+v",
			baseline.fec, current.fec)
	}
}

type compatibilityFECGroup struct {
	id       uint32
	firstSeq uint64
	lastSeq  uint64
	payloads [][]byte
	frames   []capturedFrame
}

func sendCompatibilityFECGroup(
	t testing.TB,
	m *Multipath,
	peer *net.UDPConn,
	label string,
) compatibilityFECGroup {
	t.Helper()
	const dataShards = 3
	beforeOuter := m.outerSeq.Load()
	beforeGroup := m.fecNextGroup.Load()
	payloads := make([][]byte, dataShards)
	for index := range payloads {
		payloads[index] = []byte(label + "-" + string(rune('a'+index)))
	}
	if err := m.Send(payloads, m.virt); err != nil {
		t.Fatal(err)
	}

	group := compatibilityFECGroup{
		id:       beforeGroup,
		firstSeq: beforeOuter + 1,
		lastSeq:  beforeOuter + dataShards,
		payloads: payloads,
	}
	dataIndices := make(map[uint8]uint64, dataShards)
	parityIndices := make(map[uint16]struct{}, 1)
	for len(dataIndices) < dataShards || len(parityIndices) < 1 {
		raw, decoded := readRecoveryWire(t, peer, m.psk)
		switch wire := decoded.(type) {
		case frame.Data:
			if wire.FECGroup != beforeGroup {
				t.Fatalf("DATA GroupID = %d, want process high-water %d", wire.FECGroup, beforeGroup)
			}
			if _, duplicate := dataIndices[wire.FECIndex]; duplicate {
				t.Fatalf("group %d duplicated DATA index %d", beforeGroup, wire.FECIndex)
			}
			dataIndices[wire.FECIndex] = wire.OuterSeq
			group.frames = append(group.frames, capturedFrame{raw: raw, fr: wire})
		case frame.Parity:
			if wire.FECGroup != beforeGroup || wire.DataCount != dataShards {
				t.Fatalf("PARITY = group %d data-count %d, want %d/%d",
					wire.FECGroup, wire.DataCount, beforeGroup, dataShards)
			}
			if _, duplicate := parityIndices[wire.ParityIndex]; duplicate {
				t.Fatalf("group %d duplicated PARITY index %d", beforeGroup, wire.ParityIndex)
			}
			parityIndices[wire.ParityIndex] = struct{}{}
			group.frames = append(group.frames, capturedFrame{raw: raw, fr: wire})
		default:
			t.Fatalf("complete FEC group emitted unexpected %T", decoded)
		}
	}
	for index := uint8(0); index < dataShards; index++ {
		wantSeq := group.firstSeq + uint64(index)
		if got := dataIndices[index]; got != wantSeq {
			t.Fatalf("group %d DATA[%d] OuterSeq = %d, want monotonic %d", group.id, index, got, wantSeq)
		}
	}
	if got := m.outerSeq.Load(); got != group.lastSeq {
		t.Fatalf("group %d sender OuterSeq = %d, want %d", group.id, got, group.lastSeq)
	}
	if got := m.fecNextGroup.Load(); got != beforeGroup+1 {
		t.Fatalf("group %d sender GroupID high-water = %d, want %d", group.id, got, beforeGroup+1)
	}
	return group
}

func dispatchCompatibilityFECGroup(
	t testing.TB,
	m *Multipath,
	source netip.AddrPort,
	group compatibilityFECGroup,
) {
	t.Helper()
	for _, captured := range group.frames {
		m.handleInbound(m.paths[0], captured.raw, source)
	}
	rq := m.resequencer.Load()
	for _, want := range group.payloads {
		item, ok := rq.Pop()
		if !ok || string(item.Payload) != string(want) {
			t.Fatalf("group %d receiver release = payload %q ok=%v, want %q",
				group.id, item.Payload, ok, want)
		}
	}
}

func assertProductionGapRelease(
	t testing.TB,
	m *Multipath,
	clock *fakeClock,
	source netip.AddrPort,
	missing uint64,
	hold time.Duration,
) {
	t.Helper()
	rq := m.resequencer.Load()

	deliverDATA(t, m, m.paths[0], m.psk, missing+1, []byte("after-gap"), source)
	if item, ok := rq.Pop(); ok {
		t.Fatalf("gap released early at arm: payload %q", item.Payload)
	}
	stats := rq.Stats()
	if stats.ArmedWindow != hold {
		t.Fatalf("armed production gap = %+v, want window %v", stats, hold)
	}

	clock.advance(hold - time.Nanosecond)
	if item, ok := rq.Pop(); ok {
		t.Fatalf("gap released at W-1ns: payload %q", item.Payload)
	}
	clock.advance(time.Nanosecond)
	item, ok := rq.Pop()
	if !ok || string(item.Payload) != "after-gap" {
		t.Fatalf("production gap release at W = payload %q ok=%v", item.Payload, ok)
	}
}

func addCompatibilityPathAfterDrain(
	t testing.TB,
	m *Multipath,
	clock *fakeClock,
	def config.Path,
	shaperConfig config.PathShaperConfig,
) {
	t.Helper()
	drainDue := clock.Now().Add(conservativeRecoveryService)
	done := make(chan error, 1)
	go func() {
		done <- m.AddPathWithShaper(def, shaperConfig)
	}()
	waitForRecoveryCondition(t, func() bool {
		clock.mu.Lock()
		defer clock.mu.Unlock()
		for timer := range clock.timers {
			if timer.armed && timer.due.Equal(drainDue) {
				return true
			}
		}
		return false
	})
	clock.advance(conservativeRecoveryService)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("path addition did not finish after exact service-drain bound")
	}
}

func TestRecoveryContractCompatibilityMatrix(t *testing.T) {
	t.Run("old sender to old receiver remains extension-free", func(t *testing.T) {
		psk := testKey(t, 0x7C)
		clock := newFakeClock()
		m, probers, _ := newProbingMultipath(t, loopbackPaths(1), psk, clock)
		if _, _, err := m.Open(0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = m.Close() })
		bringProberUpClean(t, probers[0], psk, clock, testProbeUpSucc)
		peer, source := rawPeer(t)
		m.paths[0].setRemote(source)
		legacy := telemetry.NewReflector(psk, rand.Reader)
		rq := m.resequencer.Load()
		baseline := compatibilitySnapshot(m)

		if err := m.Send([][]byte{[]byte("legacy-data")}, m.virt); err != nil {
			t.Fatal(err)
		}
		rawData, decoded := readRecoveryWire(t, peer, psk)
		data, ok := decoded.(frame.Data)
		if !ok || data.OuterSeq != baseline.outerSeq+1 || string(data.Payload) != "legacy-data" {
			t.Fatalf("old↔old outbound DATA = %+v (%T), want OuterSeq %d payload legacy-data",
				decoded, decoded, baseline.outerSeq+1)
		}
		m.handleInbound(m.paths[0], rawData, source)
		item, ok := rq.Pop()
		if !ok || string(item.Payload) != "legacy-data" {
			t.Fatalf("old↔old inbound DATA release = payload %q ok=%v", item.Payload, ok)
		}

		for range 2 {
			m.emitProbes()
			raw, probe := readRecoveryProbeWire(t, peer, psk)
			if len(probe.Payload) != 0 {
				t.Fatalf("legacy outbound PROBE carried %d extension bytes", len(probe.Payload))
			}
			echo, _, err := legacy.Reflect(raw)
			if err != nil {
				t.Fatal(err)
			}
			m.handleInbound(m.paths[0], echo, source)
		}
		stats := m.contracts.stats()
		if stats.Sender.OfferPresent || stats.Receiver.OfferPresent ||
			stats.Sender.FastEligible || stats.Receiver.FastEligible {
			t.Fatalf("old↔old ordinary probes created recovery state: %+v", stats)
		}
		assertCompatibilityContinuity(
			t, baseline, compatibilitySnapshot(m),
			baseline.outerSeq+1, baseline.nextGroup, 0,
		)
	})

	t.Run("old sender to new receiver retains T fallback", func(t *testing.T) {
		m, _, clock := openShapedRecoveryWindowPeer(t, 1)
		peer, source := rawPeer(t)
		baseline := compatibilitySnapshot(m)

		deliverDATA(t, m, m.paths[0], m.psk, 0, []byte("legacy-before"), source)
		item, ok := m.resequencer.Load().Pop()
		if !ok || string(item.Payload) != "legacy-before" {
			t.Fatalf("pre-probe legacy DATA release = payload %q ok=%v", item.Payload, ok)
		}
		const session = uint64(0xB002)
		first := dispatchLegacyProbe(t, m, peer, source, session, 0, 0)
		dispatchLegacyProbe(t, m, peer, source, session, 1, first.Challenge)

		afterNegotiation := compatibilitySnapshot(m)
		stats := m.contracts.stats()
		if stats.Receiver.OfferPresent || stats.Receiver.FastEligible ||
			stats.Receiver.Window != conservativeRecoveryService ||
			stats.Receiver.FallbackReason != "no_offer" {
			t.Fatalf("old→new production observability = %+v", stats)
		}
		if afterNegotiation.receivedGeneration != baseline.receivedGeneration+1 {
			t.Fatalf("legacy adoption generation = %d, want baseline %d + 1",
				afterNegotiation.receivedGeneration, baseline.receivedGeneration)
		}
		assertCompatibilityContinuity(
			t, baseline, afterNegotiation,
			baseline.outerSeq, baseline.nextGroup, 0,
		)

		deliverDATA(t, m, m.paths[0], m.psk, 1, []byte("legacy-after"), source)
		item, ok = m.resequencer.Load().Pop()
		if !ok || string(item.Payload) != "legacy-after" {
			t.Fatalf("post-probe legacy DATA release = payload %q ok=%v", item.Payload, ok)
		}
		assertProductionGapRelease(t, m, clock, source, 2, conservativeRecoveryService)
		assertCompatibilityContinuity(
			t, baseline, compatibilitySnapshot(m),
			baseline.outerSeq, baseline.nextGroup, 0,
		)
	})

	t.Run("new sender to old receiver retains T fallback", func(t *testing.T) {
		m, probers, clock := openShapedRecoveryWindowPeer(t, 1)
		bringProberUpClean(t, probers[0], m.psk, clock, testProbeUpSucc)
		psk := m.psk
		peer, source := rawPeer(t)
		m.paths[0].setRemote(source)
		legacy := telemetry.NewReflector(psk, rand.Reader)
		legacyReceiver, _ := newProbingMultipathFEC(t, loopbackPaths(1), psk, &fec.Config{
			DataShards:   3,
			ParityShards: 1,
			Deadline:     20 * time.Millisecond,
		}, clock)
		legacyReceiver.clock = clock
		if _, _, err := legacyReceiver.Open(0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = legacyReceiver.Close() })

		senderBaseline := compatibilitySnapshot(m)
		receiverBaseline := compatibilitySnapshot(legacyReceiver)
		clock.advance(conservativeRecoveryService)
		if err := m.contracts.awaitDecision(); err != nil {
			t.Fatal(err)
		}
		if sender := m.contracts.stats().Sender; sender.TransitionFrozen {
			t.Fatalf("initial sender transition did not resolve conservatively: %+v", sender)
		}
		firstGroup := sendCompatibilityFECGroup(t, m, peer, "before-legacy-offer")
		dispatchCompatibilityFECGroup(t, legacyReceiver, source, firstGroup)
		assertCompatibilityContinuity(
			t, senderBaseline, compatibilitySnapshot(m),
			senderBaseline.outerSeq+3, senderBaseline.nextGroup+1, 0,
		)
		assertCompatibilityContinuity(
			t, receiverBaseline, compatibilitySnapshot(legacyReceiver),
			receiverBaseline.outerSeq, receiverBaseline.nextGroup, 0,
		)

		firstOffer := reflectLegacyRecoveryOffer(t, m, peer, source, legacy)
		_ = reflectLegacyRecoveryOffer(t, m, peer, source, legacy)
		if m.contracts.fastEligible() {
			t.Fatal("legacy verbatim OFFER reflection enabled fast recovery")
		}
		afterNegotiation := compatibilitySnapshot(m)
		assertCompatibilityContinuity(
			t, senderBaseline, afterNegotiation,
			senderBaseline.outerSeq+3, senderBaseline.nextGroup+1, 0,
		)

		rotatedConfig := recoveryReviewShaperConfig()
		rotatedConfig.RecoveryBound = 140 * time.Millisecond
		addCompatibilityPathAfterDrain(t, m, clock, config.Path{
			Name:       "rotated",
			SourceAddr: netip.MustParseAddr("127.0.0.1"),
		}, rotatedConfig)
		var rotated telemetry.RecoveryContractMessage
		waitForRecoveryCondition(t, func() bool {
			rotated = m.contracts.offerSnapshot().message
			return rotated.ContractID > firstOffer.ContractID
		})
		if rotated.ServiceBound != rotatedConfig.RecoveryBound ||
			m.probers[0].SessionID() != testProbeSessionID {
			t.Fatalf("same-session rotation = %+v session=%x", rotated, m.probers[0].SessionID())
		}
		reflectedRotation := reflectLegacyRecoveryOffer(t, m, peer, source, legacy)
		if reflectedRotation.ContractID != rotated.ContractID ||
			reflectedRotation.ServiceBound != rotatedConfig.RecoveryBound {
			t.Fatalf("legacy reflector saw rotation %+v, want %+v", reflectedRotation, rotated)
		}
		remaining := m.contracts.barrierDue.Sub(clock.Now())
		if remaining <= 0 {
			t.Fatalf("sender transition had no positive conservative remainder: %v", remaining)
		}
		resolved := make(chan error, 1)
		go func() { resolved <- m.contracts.awaitDecision() }()
		clock.advance(remaining - time.Nanosecond)
		select {
		case err := <-resolved:
			t.Fatalf("sender transition resolved before T: %v", err)
		default:
		}
		clock.advance(time.Nanosecond)
		select {
		case err := <-resolved:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("sender transition did not resolve conservatively at T")
		}
		sender := m.contracts.stats().Sender
		if sender.FastEligible || sender.TransitionFrozen ||
			sender.ACKAccepts != 0 || sender.FallbackReason != "wrong" {
			t.Fatalf("new→old production observability after T = %+v", sender)
		}
		secondGroup := sendCompatibilityFECGroup(t, m, peer, "after-legacy-offer")
		if secondGroup.id <= firstGroup.id || secondGroup.firstSeq <= firstGroup.lastSeq {
			t.Fatalf("rotation reset sender identity: first group=%+v second group=%+v", firstGroup, secondGroup)
		}
		dispatchCompatibilityFECGroup(t, legacyReceiver, source, secondGroup)
		assertProductionGapRelease(
			t, legacyReceiver, clock, source, secondGroup.lastSeq+1, conservativeRecoveryService,
		)
		assertCompatibilityContinuity(
			t, senderBaseline, compatibilitySnapshot(m),
			senderBaseline.outerSeq+6, senderBaseline.nextGroup+2, 0,
		)
		assertCompatibilityContinuity(
			t, receiverBaseline, compatibilitySnapshot(legacyReceiver),
			receiverBaseline.outerSeq, receiverBaseline.nextGroup, 0,
		)
	})

	t.Run("new sender to new receiver becomes fast eligible", func(t *testing.T) {
		m, probers, clock := openShapedRecoveryWindowPeer(t, 1)
		bringProberUpClean(t, probers[0], m.psk, clock, testProbeUpSucc)
		peer, source := rawPeer(t)
		m.paths[0].setRemote(source)
		const session = uint64(0xB003)
		baseline := compatibilitySnapshot(m)

		deliverDATA(t, m, m.paths[0], m.psk, 0, []byte("pre-adoption"), source)
		item, ok := m.resequencer.Load().Pop()
		if !ok || string(item.Payload) != "pre-adoption" {
			t.Fatalf("pre-adoption DATA release = payload %q ok=%v", item.Payload, ok)
		}
		message := receiverContractMessage(1, 80*time.Millisecond)
		payload, err := telemetry.EncodeRecoveryContract(message)
		if err != nil {
			t.Fatal(err)
		}

		dispatchTestProbe(t, m, m.paths[0], source, frame.Probe{
			PathID:         m.paths[0].id,
			ProbeSeq:       0,
			TimestampNanos: clock.Now().UnixNano(),
			SessionID:      session,
			Payload:        payload,
		})
		bootstrap := readProbe(t, peer, mustFrameCodec(t, m.psk))
		afterBootstrap := compatibilitySnapshot(m)
		if afterBootstrap.receivedGeneration != baseline.receivedGeneration {
			t.Fatalf("no-evidence bootstrap generation = %d, want unchanged baseline %d",
				afterBootstrap.receivedGeneration, baseline.receivedGeneration)
		}
		assertCompatibilityContinuity(
			t, baseline, afterBootstrap,
			baseline.outerSeq, baseline.nextGroup, 0,
		)
		dispatchTestProbe(t, m, m.paths[0], source, frame.Probe{
			PathID:         m.paths[0].id,
			ProbeSeq:       1,
			TimestampNanos: clock.Now().UnixNano(),
			SessionID:      session,
			Challenge:      bootstrap.Challenge,
			Payload:        payload,
		})
		beforeInitialACK := compatibilitySnapshot(m)
		if beforeInitialACK.receivedGeneration != baseline.receivedGeneration+2 {
			t.Fatalf("initial adoption/offer generation = %d, want baseline %d + 2",
				beforeInitialACK.receivedGeneration, baseline.receivedGeneration)
		}
		assertCompatibilityContinuity(
			t, baseline, beforeInitialACK,
			baseline.outerSeq, baseline.nextGroup, 0,
		)
		ackEcho := readProbe(t, peer, mustFrameCodec(t, m.psk))
		ack, recognized, err := telemetry.DecodeRecoveryContract(ackEcho.Payload)
		if err != nil || !recognized || ack.Type != telemetry.RecoveryContractACK ||
			ack.ContractID != message.ContractID {
			t.Fatalf("new receiver wire ACK = recognized %v err %v message %+v", recognized, err, ack)
		}
		waitForRecoveryCondition(t, func() bool {
			return m.contracts.receivedSnapshot().acked
		})

		remote := telemetry.NewReflector(m.psk, rand.Reader)
		for index := 0; index < 2; index++ {
			beforeWrites := m.contracts.stats().Sender.OfferWrites
			m.emitProbes()
			raw, offeredProbe := readRecoveryProbeWire(t, peer, m.psk)
			waitForRecoveryCondition(t, func() bool {
				return m.contracts.stats().Sender.OfferWrites > beforeWrites
			})
			accepted, err := remote.AcceptProbe(raw)
			if err != nil {
				t.Fatal(err)
			}
			echoPayload := offeredProbe.Payload
			if accepted.Acceptance != telemetry.ProbeBootstrap {
				offered, recognized, err := telemetry.DecodeRecoveryContract(offeredProbe.Payload)
				if err != nil || !recognized || offered.Type != telemetry.RecoveryContractOffer {
					t.Fatalf("new sender wire OFFER = recognized %v err %v message %+v", recognized, err, offered)
				}
				offered.Type = telemetry.RecoveryContractACK
				echoPayload, err = telemetry.EncodeRecoveryContract(offered)
				if err != nil {
					t.Fatal(err)
				}
			}
			echo, err := remote.EncodeAcceptedProbe(accepted, echoPayload)
			if err != nil {
				t.Fatal(err)
			}
			clock.advance(testProbeRTT)
			m.handleInbound(m.paths[0], echo, source)
		}

		stats := m.contracts.stats()
		if !stats.Sender.FastEligible || stats.Sender.ACKAccepts != 1 ||
			stats.Sender.FallbackReason != "" {
			t.Fatalf("new↔new sender observability = %+v", stats)
		}
		if !stats.Receiver.FastEligible || stats.Receiver.Window >= conservativeRecoveryService ||
			stats.Receiver.FallbackReason != "" {
			t.Fatalf("new↔new receiver observability = %+v", stats)
		}

		firstGroup := sendCompatibilityFECGroup(t, m, peer, "before-current-rotation")
		dispatchCompatibilityFECGroup(t, m, source, firstGroup)
		afterFirstGroup := compatibilitySnapshot(m)
		assertCompatibilityContinuity(
			t, baseline, afterFirstGroup,
			baseline.outerSeq+3, baseline.nextGroup+1, 0,
		)

		rotated := receiverContractMessage(2, 90*time.Millisecond)
		rotatedPayload, err := telemetry.EncodeRecoveryContract(rotated)
		if err != nil {
			t.Fatal(err)
		}
		dispatchTestProbe(t, m, m.paths[0], source, frame.Probe{
			PathID:         m.paths[0].id,
			ProbeSeq:       2,
			TimestampNanos: clock.Now().UnixNano(),
			SessionID:      session,
			Challenge:      ackEcho.Challenge,
			Payload:        rotatedPayload,
		})
		rotatedACK := readProbe(t, peer, mustFrameCodec(t, m.psk))
		rotatedMessage, recognized, err := telemetry.DecodeRecoveryContract(rotatedACK.Payload)
		if err != nil || !recognized || rotatedMessage.Type != telemetry.RecoveryContractACK ||
			rotatedMessage.ContractID != rotated.ContractID {
			t.Fatalf("same-session rotation ACK = recognized %v err %v message %+v",
				recognized, err, rotatedMessage)
		}
		waitForRecoveryCondition(t, func() bool {
			snapshot := m.contracts.receivedSnapshot()
			return snapshot.acked && snapshot.message.ContractID == rotated.ContractID
		})
		afterRotation := compatibilitySnapshot(m)
		if afterRotation.receivedGeneration != baseline.receivedGeneration+3 {
			t.Fatalf("same-session rotation generation = %d, want baseline %d + 3",
				afterRotation.receivedGeneration, baseline.receivedGeneration)
		}
		assertCompatibilityContinuity(
			t, baseline, afterRotation,
			baseline.outerSeq+3, baseline.nextGroup+1, 0,
		)

		secondGroup := sendCompatibilityFECGroup(t, m, peer, "after-current-rotation")
		if secondGroup.id <= firstGroup.id || secondGroup.firstSeq <= firstGroup.lastSeq {
			t.Fatalf("same-session ACK reset sender identity: first group=%+v second group=%+v",
				firstGroup, secondGroup)
		}
		dispatchCompatibilityFECGroup(t, m, source, secondGroup)
		stats = m.contracts.stats()
		if !stats.Receiver.FastEligible || stats.Receiver.ServiceBound != rotated.ServiceBound ||
			stats.Receiver.Window >= conservativeRecoveryService {
			t.Fatalf("rotated receiver observability = %+v", stats)
		}
		assertProductionGapRelease(
			t, m, clock, source, secondGroup.lastSeq+1, stats.Receiver.Window,
		)
		assertCompatibilityContinuity(
			t, baseline, compatibilitySnapshot(m),
			baseline.outerSeq+6, baseline.nextGroup+2, 0,
		)
	})
}

func TestRecoveryContractRotationAndSessionRestartStayDistinct(t *testing.T) {
	coordinator := newRecoveryContractCoordinator(0xB004, newFakeClock())
	if err := coordinator.begin(true, 80*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	first := coordinator.offerSnapshot().message.ContractID
	if err := coordinator.beginGeneration(true, 90*time.Millisecond, 1); err != nil {
		t.Fatal(err)
	}
	if second := coordinator.offerSnapshot().message.ContractID; second <= first {
		t.Fatalf("same-process ContractID = %d, want > %d", second, first)
	}
	coordinator.adoptReceivedSession(0xCAFE)
	coordinator.adoptReceivedSession(0xBABE)
	if stats := coordinator.stats(); stats.Sender.Rotations != 1 ||
		stats.Receiver.SessionRestarts != 1 {
		t.Fatalf("rotation/restart observability = %+v", stats)
	}
}

func TestRecoveryContractWireExtensionIsAbsentWhenFECOff(t *testing.T) {
	for _, test := range []struct {
		name string
		open func(*testing.T, config.Key, *fakeClock) *Multipath
	}{
		{
			name: "FEC off",
			open: func(t *testing.T, psk config.Key, clock *fakeClock) *Multipath {
				m, _, _ := newProbingMultipath(t, loopbackPaths(1), psk, clock)
				m.shaperConfigs = []config.PathShaperConfig{priorityTestShaperConfig()}
				if _, _, err := m.Open(0); err != nil {
					t.Fatal(err)
				}
				return m
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			psk := testKey(t, 0x7B)
			m := test.open(t, psk, newFakeClock())
			t.Cleanup(func() { _ = m.Close() })
			peer, peerAP := rawPeer(t)
			m.paths[0].setRemote(peerAP)
			m.emitProbes()
			if err := peer.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
				t.Fatal(err)
			}
			raw := make([]byte, maxDatagram)
			n, err := peer.Read(raw)
			if err != nil {
				t.Fatal(err)
			}
			if n != frame.UnpaddedProbeOnWire {
				t.Fatalf("ordinary probe wire size = %d, want legacy %d", n, frame.UnpaddedProbeOnWire)
			}
			decoded, err := frame.Decode(psk, raw[:n])
			if err != nil {
				t.Fatal(err)
			}
			if payload := decoded.(frame.Probe).Payload; len(payload) != 0 {
				t.Fatalf("legacy-mode probe carries %d recovery-contract bytes", len(payload))
			}
		})
	}
}
