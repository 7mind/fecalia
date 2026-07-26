package bind

import (
	"crypto/rand"
	"errors"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/fec"
	"github.com/7mind/wanbond/internal/frame"
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

func TestRecoveryContractCompatibilityMatrix(t *testing.T) {
	for _, test := range []struct {
		name string
		run  func(*testing.T)
	}{
		{
			name: "old sender to old receiver remains extension-free",
			run: func(t *testing.T) {
				if _, recognized, err := telemetry.DecodeRecoveryContract(nil); err != nil || recognized {
					t.Fatalf("legacy empty payload = recognized %v err %v", recognized, err)
				}
			},
		},
		{
			name: "new sender to old receiver retains T fallback",
			run: func(t *testing.T) {
				clock := newFakeClock()
				sender := newRecoveryContractCoordinator(0xB001, clock)
				if err := sender.begin(true, 80*time.Millisecond); err != nil {
					t.Fatal(err)
				}
				offered := sender.offerSnapshot()
				sender.recordOffer(0, telemetryProbeHeader{
					sessionID: 0xB001,
					probeSeq:  1,
					challenge: 2,
				}, offered)
				if sender.acceptACK(0, 0xB001, 1, offered.payload) {
					t.Fatal("legacy verbatim OFFER echo satisfied exact ACK")
				}
				if sender.fastEligible() {
					t.Fatal("new sender enabled fast recovery against old receiver")
				}
				if stats := sender.stats(); stats.Sender.OfferWrites != 1 ||
					stats.Sender.ACKAccepts != 0 ||
					stats.Sender.WrongRejections != 1 ||
					!stats.Sender.TransitionFrozen ||
					stats.Sender.FallbackReason != "transition" {
					t.Fatalf("new-to-old observability = %+v", stats)
				}
			},
		},
		{
			name: "old sender to new receiver retains T fallback",
			run: func(t *testing.T) {
				receiver := newRecoveryContractCoordinator(0xB002, newFakeClock())
				if _, recognized, err := telemetry.DecodeRecoveryContract(nil); err != nil || recognized {
					t.Fatalf("legacy payload = recognized %v err %v", recognized, err)
				}
				if receiver.receivedSnapshot().present {
					t.Fatal("new receiver fabricated a contract for an old sender")
				}
				if stats := receiver.stats(); stats.Receiver.OfferPresent ||
					stats.Receiver.FastEligible ||
					stats.Receiver.FallbackReason != "no_offer" {
					t.Fatalf("old-to-new observability = %+v", stats)
				}
			},
		},
		{
			name: "new sender to new receiver becomes fast eligible",
			run: func(t *testing.T) {
				sender := newRecoveryContractCoordinator(0xB003, newFakeClock())
				if err := sender.begin(true, 80*time.Millisecond); err != nil {
					t.Fatal(err)
				}
				acknowledgeCurrentContract(t, sender, 0, 1)
				if !sender.fastEligible() {
					t.Fatal("exact current-process ACK did not enable fast recovery")
				}
				if stats := sender.stats(); !stats.Sender.OfferPresent ||
					!stats.Sender.FastEligible ||
					!stats.Sender.WriterExclusive ||
					stats.Sender.OfferWrites != 1 ||
					stats.Sender.ACKAccepts != 1 ||
					stats.Sender.FallbackReason != "" {
					t.Fatalf("new-to-new observability = %+v", stats)
				}
			},
		},
		{
			name: "same-process rotation and session restart stay distinct",
			run: func(t *testing.T) {
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
			},
		},
	} {
		t.Run(test.name, test.run)
	}
}

func TestRecoveryContractWireExtensionIsAbsentWhenPacingOrFECOff(t *testing.T) {
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
		{
			name: "pacing off",
			open: func(t *testing.T, psk config.Key, clock *fakeClock) *Multipath {
				m, _ := newProbingMultipathFEC(t, loopbackPaths(1), psk, &fec.Config{
					DataShards:   3,
					ParityShards: 1,
					Deadline:     20 * time.Millisecond,
				}, clock)
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
