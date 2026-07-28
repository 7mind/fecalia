package bind

import (
	"testing"
	"time"
)

func TestReceiverRecoveryStatsUseResequencerFreshnessBoundary(t *testing.T) {
	m, probers, clock := openRecoveryWindowPeer(t, 1)
	bringProberUpClean(t, probers[0], m.psk, clock, testProbeUpSucc)

	message := receiverContractMessage(1, 80*time.Millisecond)
	if _, ok := m.contracts.acceptOffer(receiverContractSession, message, func() {}); !ok {
		t.Fatal("fresh offer rejected")
	}
	pathKey := reseqPathKey(m.paths[0].id, 9)
	if !completeReceiverACK(m.contracts, receiverContractSession, message, pathKey, receiverContractSource) {
		t.Fatal("exact ACK completion rejected")
	}
	m.refreshPeerRecoveryWindow(m.peerState)

	initial := m.contracts.stats().Receiver
	if !initial.FastEligible || initial.Window >= conservativeRecoveryService {
		t.Fatalf("initial receiver stats = %+v, want fast W < T", initial)
	}
	untilBoundary := initial.FreshUntil.Sub(clock.Now()) - conservativeRecoveryService
	if untilBoundary <= 0 {
		t.Fatalf("receiver freshness headroom = %v, want > 0", untilBoundary)
	}
	clock.advance(untilBoundary)

	atBoundary := m.contracts.stats().Receiver
	if !atBoundary.FastEligible || atBoundary.Window != initial.Window ||
		atBoundary.FallbackReason != "" {
		t.Fatalf("receiver stats at exact T boundary = %+v, want fast W=%v", atBoundary, initial.Window)
	}
	armedAt := clock.Now()
	if deadline := armRecoveryWindowGap(m, receiverContractSource, pathKey); deadline != armedAt.Add(initial.Window) {
		t.Fatalf("resequence deadline at exact T boundary = %v, want %v", deadline, armedAt.Add(initial.Window))
	}

	rq := m.resequencer.Load()
	rq.ObserveFromPath(1, []byte("one"), receiverContractSource, pathKey)
	if _, ok := rq.Pop(); !ok {
		t.Fatal("gap fill did not release sequence 1")
	}
	if _, ok := rq.Pop(); !ok {
		t.Fatal("gap fill did not release buffered sequence 2")
	}

	clock.advance(time.Nanosecond)
	staleAt := clock.Now()
	rq.ObserveFromPath(4, []byte("four"), receiverContractSource, pathKey)
	deadline, armed := rq.ArmedDeadline()
	if !armed || deadline != staleAt.Add(conservativeRecoveryService) {
		t.Fatalf("stale resequence deadline = %v armed=%v, want %v", deadline, armed, staleAt.Add(conservativeRecoveryService))
	}
	stale := m.contracts.stats().Receiver
	if stale.FastEligible || stale.Window != conservativeRecoveryService ||
		stale.FallbackReason != "stale" {
		t.Fatalf("stale receiver stats = %+v, want FastEligible=false Window=T FallbackReason=stale", stale)
	}

	rq.ObserveFromPath(3, []byte("three"), receiverContractSource, pathKey)
	if _, ok := rq.Pop(); !ok {
		t.Fatal("stale gap fill did not release sequence 3")
	}
	if _, ok := rq.Pop(); !ok {
		t.Fatal("stale gap fill did not release buffered sequence 4")
	}
	recordRecoveryRTTSample(t, probers[0], m.psk, clock, testProbeRTT)
	m.refreshPeerRecoveryWindow(m.peerState)
	restored := m.contracts.stats().Receiver
	evidence := probers[0].RecoveryRTT()
	wantWindow := recoveryWindow(
		message.ServiceBound,
		recoveryRTTHeadroom(evidence.RTT, evidence.RTTVariation),
	)
	if !restored.FastEligible || restored.Window != wantWindow || restored.FallbackReason != "" {
		t.Fatalf("fresh receiver commit did not restore fast stats: %+v", restored)
	}
	restoredAt := clock.Now()
	rq.ObserveFromPath(6, []byte("six"), receiverContractSource, pathKey)
	deadline, armed = rq.ArmedDeadline()
	if !armed || deadline != restoredAt.Add(wantWindow) {
		t.Fatalf("restored resequence deadline = %v armed=%v, want %v", deadline, armed, restoredAt.Add(wantWindow))
	}
}
