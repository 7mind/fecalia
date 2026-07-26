package bind

import (
	"crypto/rand"
	"errors"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/fec"
	"github.com/7mind/wanbond/internal/frame"
	"github.com/7mind/wanbond/internal/reseq"
	"github.com/7mind/wanbond/internal/sched"
	"github.com/7mind/wanbond/internal/shaper"
	"github.com/7mind/wanbond/internal/telemetry"
)

const receiverContractSession = uint64(0xA314)

var receiverContractSource = netip.MustParseAddrPort("192.0.2.44:51820")
var receiverStandbySource = netip.MustParseAddrPort("198.51.100.44:51820")

func receiverContractMessage(id uint64, service time.Duration) telemetry.RecoveryContractMessage {
	return telemetry.RecoveryContractMessage{
		Type:         telemetry.RecoveryContractOffer,
		Enabled:      true,
		ServiceBound: service,
		Lifetime:     telemetry.RecoveryContractLifetime,
		ContractID:   id,
	}
}

func completeReceiverACK(
	coordinator *recoveryContractCoordinator,
	session uint64,
	message telemetry.RecoveryContractMessage,
	pathKey uint32,
	source netip.AddrPort,
) bool {
	coordinator.observeReceivedSource(pathKey, source)
	admission, ok := coordinator.admitReceivedACK(session, message, pathKey, source)
	return ok && coordinator.completeReceivedACK(admission)
}

func bringProberUpAtRTT(
	t testing.TB,
	prober *telemetry.Prober,
	psk config.Key,
	clock *fakeClock,
	rtt time.Duration,
) {
	t.Helper()
	reflector := telemetry.NewReflector(psk, rand.Reader)
	for range testProbeUpSucc {
		raw, err := prober.SendProbe()
		if err != nil {
			t.Fatal(err)
		}
		echo, _, err := reflector.Reflect(raw)
		if err != nil {
			t.Fatal(err)
		}
		clock.advance(rtt)
		if err := prober.HandleEcho(echo); err != nil {
			t.Fatal(err)
		}
	}
	prober.Tick()
	if prober.State() != telemetry.StateUp {
		t.Fatal("prober did not reach Up")
	}
}

func recordRecoveryRTTSample(
	t testing.TB,
	prober *telemetry.Prober,
	psk config.Key,
	clock *fakeClock,
	rtt time.Duration,
) {
	t.Helper()
	raw, err := prober.SendProbe()
	if err != nil {
		t.Fatal(err)
	}
	echo, _, err := telemetry.NewReflector(psk, rand.Reader).Reflect(raw)
	if err != nil {
		t.Fatal(err)
	}
	clock.advance(rtt)
	if err := prober.HandleEcho(echo); err != nil {
		t.Fatal(err)
	}
	prober.Tick()
}

func openRecoveryWindowPeer(t *testing.T, pathCount int) (*Multipath, []*telemetry.Prober, *fakeClock) {
	t.Helper()
	clock := newFakeClock()
	psk := testKey(t, 0xA3)
	m, probers := newProbingMultipathFEC(t, loopbackPaths(pathCount), psk, &fec.Config{
		DataShards:   3,
		ParityShards: 1,
		Deadline:     20 * time.Millisecond,
	}, clock)
	m.clock = clock
	if _, _, err := m.Open(0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m, probers, clock
}

func openShapedRecoveryWindowPeer(t *testing.T, pathCount int) (*Multipath, []*telemetry.Prober, *fakeClock) {
	t.Helper()
	clock := newFakeClock()
	psk := testKey(t, 0xA4)
	m, probers := newProbingMultipathFEC(t, loopbackPaths(pathCount), psk, &fec.Config{
		DataShards:   3,
		ParityShards: 1,
		Deadline:     20 * time.Millisecond,
	}, clock)
	m.clock = clock
	cfg := recoveryReviewShaperConfig()
	cfg.RecoveryBound = 125 * time.Millisecond
	m.shaperConfigs = make([]config.PathShaperConfig, pathCount)
	for i := range m.shaperConfigs {
		m.shaperConfigs[i] = cfg
	}
	if _, _, err := m.Open(0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m, probers, clock
}

func armRecoveryWindowGap(m *Multipath, source netip.AddrPort, pathKey uint32) time.Time {
	rq := m.resequencer.Load()
	rq.ObserveFromPath(0, []byte("zero"), source, pathKey)
	rq.Pop()
	rq.ObserveFromPath(2, []byte("two"), source, pathKey)
	deadline, _ := rq.ArmedDeadline()
	return deadline
}

func makeArmedRecoveryPeer(clock *fakeClock, pathKey uint32, hold time.Duration) *peerState {
	rq := reseq.New(16, conservativeRecoveryService, clock)
	rq.SetFECActive(true)
	rq.SetRecoveryWindow(reseq.RecoveryWindow{
		Enabled:    true,
		Revision:   uint64(pathKey),
		PathKey:    pathKey,
		Source:     receiverContractSource,
		Hold:       hold,
		ValidUntil: clock.Now().Add(conservativeRecoveryService),
	})
	rq.ObserveFromPath(0, []byte("zero"), receiverContractSource, pathKey)
	rq.Pop()
	rq.ObserveFromPath(2, []byte("two"), receiverContractSource, pathKey)
	peer := &peerState{virt: &udpEndpoint{}}
	peer.resequencer.Store(rq)
	return peer
}

func TestReceiverRecoveryWindowUsesExactACKAndFreshMaxRTT(t *testing.T) {
	m, probers, clock := openRecoveryWindowPeer(t, 2)
	for _, prober := range probers {
		bringProberUpClean(t, prober, m.psk, clock, testProbeUpSucc)
	}
	message := receiverContractMessage(1, 80*time.Millisecond)
	if _, ok := m.contracts.acceptOffer(receiverContractSession, message, func() {}); !ok {
		t.Fatal("fresh offer rejected")
	}
	pathKey := reseqPathKey(m.paths[0].id, 9)
	if !completeReceiverACK(m.contracts, receiverContractSession, message, pathKey, receiverContractSource) {
		t.Fatal("exact ACK completion rejected")
	}
	standbyPathKey := reseqPathKey(m.paths[1].id, 10)
	if !completeReceiverACK(m.contracts, receiverContractSession, message, standbyPathKey, receiverStandbySource) {
		t.Fatal("standby ACK completion rejected")
	}
	snapshot := m.contracts.receivedSnapshot()
	if len(snapshot.venues) != 2 {
		t.Fatalf("successful ACK venues = %d, want both primary and standby", len(snapshot.venues))
	}
	m.refreshPeerRecoveryWindow(m.peerState)

	armedAt := clock.Now()
	deadline := armRecoveryWindowGap(m, receiverContractSource, pathKey)
	want := armedAt.Add(80*time.Millisecond + recoveryRTTHeadroom(testProbeRTT))
	if deadline != want {
		t.Fatalf("fast deadline = %v, want A+H %v", deadline, want)
	}
}

func TestReceiverRecoveryWindowUsesMaximumUnequalUpRTT(t *testing.T) {
	m, probers, clock := openRecoveryWindowPeer(t, 2)
	const (
		fastRTT = 5 * time.Millisecond
		slowRTT = 20 * time.Millisecond
	)
	bringProberUpAtRTT(t, probers[0], m.psk, clock, fastRTT)
	bringProberUpAtRTT(t, probers[1], m.psk, clock, slowRTT)
	bringProberUpAtRTT(t, probers[0], m.psk, clock, fastRTT)
	message := receiverContractMessage(1, 80*time.Millisecond)
	if _, ok := m.contracts.acceptOffer(receiverContractSession, message, func() {}); !ok {
		t.Fatal("offer rejected")
	}
	pathKey := reseqPathKey(m.paths[0].id, 9)
	if !completeReceiverACK(m.contracts, receiverContractSession, message, pathKey, receiverContractSource) {
		t.Fatal("ACK completion rejected")
	}
	m.refreshPeerRecoveryWindow(m.peerState)

	armedAt := clock.Now()
	deadline := armRecoveryWindowGap(m, receiverContractSource, pathKey)
	want := armedAt.Add(message.ServiceBound + 4*slowRTT)
	if deadline != want {
		t.Fatalf("unequal-RTT deadline = %v, want max-RTT deadline %v", deadline, want)
	}
}

func TestReceiverRecoveryWindowFallsBackForWeightedOrDownDelivery(t *testing.T) {
	t.Run("weighted", func(t *testing.T) {
		m, probers, clock := openRecoveryWindowPeer(t, 1)
		bringProberUpClean(t, probers[0], m.psk, clock, testProbeUpSucc)
		message := receiverContractMessage(1, 80*time.Millisecond)
		if _, ok := m.contracts.acceptOffer(receiverContractSession, message, func() {}); !ok {
			t.Fatal("offer rejected")
		}
		pathKey := reseqPathKey(m.paths[0].id, 9)
		if !completeReceiverACK(m.contracts, receiverContractSession, message, pathKey, receiverContractSource) {
			t.Fatal("ACK completion rejected")
		}
		m.scheduler = &sched.WeightedScheduler{}
		m.refreshPeerRecoveryWindow(m.peerState)
		armedAt := clock.Now()
		if got := armRecoveryWindowGap(m, receiverContractSource, pathKey); got != armedAt.Add(conservativeRecoveryService) {
			t.Fatalf("weighted deadline = %v, want T", got)
		}
	})

	t.Run("delivery path Down", func(t *testing.T) {
		m, probers, clock := openRecoveryWindowPeer(t, 1)
		bringProberUpClean(t, probers[0], m.psk, clock, testProbeUpSucc)
		message := receiverContractMessage(1, 80*time.Millisecond)
		if _, ok := m.contracts.acceptOffer(receiverContractSession, message, func() {}); !ok {
			t.Fatal("offer rejected")
		}
		pathKey := reseqPathKey(m.paths[0].id, 9)
		if !completeReceiverACK(m.contracts, receiverContractSession, message, pathKey, receiverContractSource) {
			t.Fatal("ACK completion rejected")
		}
		clock.advance(testProbeDownAfter + time.Nanosecond)
		probers[0].Tick()
		if probers[0].State() != telemetry.StateDown {
			t.Fatal("delivery path did not transition Down")
		}
		m.refreshPeerRecoveryWindow(m.peerState)
		armedAt := clock.Now()
		if got := armRecoveryWindowGap(m, receiverContractSource, pathKey); got != armedAt.Add(conservativeRecoveryService) {
			t.Fatalf("Down-path deadline = %v, want T", got)
		}
	})
}

func TestReceiverRecoveryWindowFallsBackWithoutExactEvidence(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, *Multipath, []*telemetry.Prober, *fakeClock, uint32)
	}{
		{name: "no contract"},
		{
			name: "offer only",
			setup: func(t *testing.T, m *Multipath, _ []*telemetry.Prober, _ *fakeClock, _ uint32) {
				if _, ok := m.contracts.acceptOffer(receiverContractSession, receiverContractMessage(1, 80*time.Millisecond), func() {}); !ok {
					t.Fatal("offer rejected")
				}
			},
		},
		{
			name: "wrong composite",
			setup: func(t *testing.T, m *Multipath, _ []*telemetry.Prober, _ *fakeClock, pathKey uint32) {
				message := receiverContractMessage(1, 80*time.Millisecond)
				if _, ok := m.contracts.acceptOffer(receiverContractSession, message, func() {}); !ok {
					t.Fatal("offer rejected")
				}
				if !completeReceiverACK(m.contracts, receiverContractSession, message, pathKey+1, receiverContractSource) {
					t.Fatal("ACK completion rejected")
				}
			},
		},
		{
			name: "RTT absent",
			setup: func(t *testing.T, m *Multipath, _ []*telemetry.Prober, _ *fakeClock, pathKey uint32) {
				message := receiverContractMessage(1, 80*time.Millisecond)
				if _, ok := m.contracts.acceptOffer(receiverContractSession, message, func() {}); !ok {
					t.Fatal("offer rejected")
				}
				if !completeReceiverACK(m.contracts, receiverContractSession, message, pathKey, receiverContractSource) {
					t.Fatal("ACK completion rejected")
				}
			},
		},
		{
			name: "RTT stale below T",
			setup: func(t *testing.T, m *Multipath, probers []*telemetry.Prober, clock *fakeClock, pathKey uint32) {
				bringProberUpClean(t, probers[0], m.psk, clock, testProbeUpSucc)
				message := receiverContractMessage(1, 80*time.Millisecond)
				if _, ok := m.contracts.acceptOffer(receiverContractSession, message, func() {}); !ok {
					t.Fatal("offer rejected")
				}
				if !completeReceiverACK(m.contracts, receiverContractSession, message, pathKey, receiverContractSource) {
					t.Fatal("ACK completion rejected")
				}
				clock.advance(testProbeDownAfter - conservativeRecoveryService + time.Nanosecond)
			},
		},
		{
			name: "contract freshness below T",
			setup: func(t *testing.T, m *Multipath, probers []*telemetry.Prober, clock *fakeClock, pathKey uint32) {
				message := receiverContractMessage(1, 80*time.Millisecond)
				if _, ok := m.contracts.acceptOffer(receiverContractSession, message, func() {}); !ok {
					t.Fatal("offer rejected")
				}
				if !completeReceiverACK(m.contracts, receiverContractSession, message, pathKey, receiverContractSource) {
					t.Fatal("ACK completion rejected")
				}
				clock.advance(telemetry.RecoveryContractLifetime - conservativeRecoveryService + time.Nanosecond)
				sendCleanProbes(t, probers[0], m.psk, clock, 1)
			},
		},
		{
			name: "disabled service",
			setup: func(t *testing.T, m *Multipath, _ []*telemetry.Prober, _ *fakeClock, pathKey uint32) {
				message := receiverContractMessage(1, 80*time.Millisecond)
				message.Enabled = false
				message.ServiceBound = 0
				if _, ok := m.contracts.acceptOffer(receiverContractSession, message, func() {}); !ok {
					t.Fatal("disabled offer rejected before conservative policy evaluation")
				}
				if !completeReceiverACK(m.contracts, receiverContractSession, message, pathKey, receiverContractSource) {
					t.Fatal("disabled ACK completion rejected")
				}
			},
		},
		{
			name: "lifetime not F",
			setup: func(t *testing.T, m *Multipath, _ []*telemetry.Prober, _ *fakeClock, pathKey uint32) {
				message := receiverContractMessage(1, 80*time.Millisecond)
				message.Lifetime = telemetry.RecoveryContractLifetime + time.Millisecond
				if _, ok := m.contracts.acceptOffer(receiverContractSession, message, func() {}); !ok {
					t.Fatal("alternate-lifetime offer rejected before conservative policy evaluation")
				}
				if !completeReceiverACK(m.contracts, receiverContractSession, message, pathKey, receiverContractSource) {
					t.Fatal("alternate-lifetime ACK completion rejected")
				}
			},
		},
		{
			name: "service A at T",
			setup: func(t *testing.T, m *Multipath, _ []*telemetry.Prober, _ *fakeClock, pathKey uint32) {
				message := receiverContractMessage(1, conservativeRecoveryService)
				if _, ok := m.contracts.acceptOffer(receiverContractSession, message, func() {}); !ok {
					t.Fatal("T-bound offer rejected before conservative policy evaluation")
				}
				if !completeReceiverACK(m.contracts, receiverContractSession, message, pathKey, receiverContractSource) {
					t.Fatal("T-bound ACK completion rejected")
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			m, probers, clock := openRecoveryWindowPeer(t, 1)
			pathKey := reseqPathKey(m.paths[0].id, 9)
			if test.name != "RTT absent" && test.name != "RTT stale below T" {
				bringProberUpClean(t, probers[0], m.psk, clock, testProbeUpSucc)
			}
			if test.setup != nil {
				test.setup(t, m, probers, clock, pathKey)
			}
			m.refreshPeerRecoveryWindow(m.peerState)
			armedAt := clock.Now()
			deadline := armRecoveryWindowGap(m, receiverContractSource, pathKey)
			if want := armedAt.Add(conservativeRecoveryService); deadline != want {
				t.Fatalf("deadline = %v, want conservative %v", deadline, want)
			}
		})
	}
}

func TestRecoveryHeadroomFactorFloorAndSaturation(t *testing.T) {
	tests := []struct {
		rtt  time.Duration
		want time.Duration
	}{
		{rtt: 0, want: recoveryRTTFloor},
		{rtt: time.Millisecond, want: recoveryRTTFloor},
		{rtt: 20 * time.Millisecond, want: 80 * time.Millisecond},
		{rtt: 100 * time.Millisecond, want: conservativeRecoveryService},
	}
	for _, test := range tests {
		if got := recoveryRTTHeadroom(test.rtt); got != test.want {
			t.Fatalf("H(%v) = %v, want %v", test.rtt, got, test.want)
		}
	}
	if got := recoveryWindow(200*time.Millisecond, 80*time.Millisecond); got != conservativeRecoveryService {
		t.Fatalf("saturated W = %v, want %v", got, conservativeRecoveryService)
	}
}

func TestReceivedContractRotationClearsEvidencePreservingHighWater(t *testing.T) {
	clock := newFakeClock()
	coordinator := newRecoveryContractCoordinator(1, clock)
	first := receiverContractMessage(1, 80*time.Millisecond)
	if _, ok := coordinator.acceptOffer(100, first, func() {}); !ok {
		t.Fatal("first offer rejected")
	}
	if !completeReceiverACK(coordinator, 100, first, 7, receiverContractSource) {
		t.Fatal("first ACK rejected")
	}
	second := receiverContractMessage(2, 80*time.Millisecond)
	if _, ok := coordinator.acceptOffer(100, second, func() {}); !ok {
		t.Fatal("renewal offer rejected")
	}
	snapshot := coordinator.receivedSnapshot()
	if snapshot.acked || snapshot.message.ContractID != 2 {
		t.Fatalf("renewal snapshot = %+v, want ID 2 unacked", snapshot)
	}
	if _, ok := coordinator.acceptOffer(100, first, func() {}); ok {
		t.Fatal("lower ContractID accepted after renewal")
	}
	if got := coordinator.receivedSnapshot().message.ContractID; got != 2 {
		t.Fatalf("lower ContractID changed high-water to %d", got)
	}

	restarted := receiverContractMessage(1, 80*time.Millisecond)
	installs := 0
	if _, ok := coordinator.acceptOffer(101, restarted, func() { installs++ }); !ok {
		t.Fatal("new SessionID offer rejected")
	}
	snapshot = coordinator.receivedSnapshot()
	if installs != 1 || snapshot.session != 101 || snapshot.acked {
		t.Fatalf("restart snapshot = %+v installs=%d, want session 101 unacked", snapshot, installs)
	}
}

func TestAdoptedSessionCannotRevertToOldPerPathSession(t *testing.T) {
	coordinator := newRecoveryContractCoordinator(1, newFakeClock())
	old := receiverContractMessage(9, 80*time.Millisecond)
	if _, ok := coordinator.acceptOffer(100, old, func() {}); !ok {
		t.Fatal("old session setup rejected")
	}
	coordinator.adoptReceivedSession(101)
	current := receiverContractMessage(1, 80*time.Millisecond)
	if _, ok := coordinator.acceptOffer(101, current, func() {}); !ok {
		t.Fatal("adopted session offer rejected")
	}
	if _, ok := coordinator.acceptOffer(100, old, func() {}); ok {
		t.Fatal("old per-path session reverted the adopted peer process epoch")
	}
	if got := coordinator.receivedSnapshot().session; got != 101 {
		t.Fatalf("received session = %d, want adopted 101", got)
	}
}

func TestOldACKCompletionCannotRestoreInvalidatedEvidence(t *testing.T) {
	coordinator := newRecoveryContractCoordinator(1, newFakeClock())
	message := receiverContractMessage(1, 80*time.Millisecond)
	if _, ok := coordinator.acceptOffer(receiverContractSession, message, func() {}); !ok {
		t.Fatal("offer rejected")
	}
	coordinator.observeReceivedSource(7, receiverContractSource)
	oldAdmission, ok := coordinator.admitReceivedACK(
		receiverContractSession,
		message,
		7,
		receiverContractSource,
	)
	if !ok {
		t.Fatal("old ACK admission rejected")
	}

	// Model an ACK write admitted before a same-key source roam. The roam advances
	// the peer generation before the held write completes.
	roamedSource := netip.MustParseAddrPort("192.0.2.99:51820")
	if _, changed := coordinator.observeReceivedSource(7, roamedSource); !changed {
		t.Fatal("same-key source roam did not advance the receiver generation")
	}
	// An independent standby ACK admitted in the current generation remains
	// usable on its own exact venue.
	coordinator.observeReceivedSource(8, receiverStandbySource)
	standbyAdmission, ok := coordinator.admitReceivedACK(
		receiverContractSession,
		message,
		8,
		receiverStandbySource,
	)
	if !ok || !coordinator.completeReceivedACK(standbyAdmission) {
		t.Fatal("current-generation standby ACK was not recorded")
	}
	if coordinator.completeReceivedACK(oldAdmission) {
		t.Fatal("old ACK completion restored evidence after invalidation")
	}
	snapshot := coordinator.receivedSnapshot()
	if !snapshot.acked || len(snapshot.venues) != 1 ||
		snapshot.venues[0].pathKey != 8 || snapshot.venues[0].source != receiverStandbySource {
		t.Fatalf("current standby venue after old completion = %+v", snapshot.venues)
	}
}

func TestStaleRecoveryPublicationCannotRestoreOlderGeneration(t *testing.T) {
	clock := newFakeClock()
	rq := reseq.New(16, conservativeRecoveryService, clock)
	rq.SetFECActive(true)
	stale := reseq.RecoveryWindow{
		Enabled:    true,
		Revision:   1,
		PathKey:    7,
		Source:     receiverContractSource,
		Hold:       60 * time.Millisecond,
		ValidUntil: clock.Now().Add(time.Second),
	}
	rq.SetRecoveryWindow(stale)
	// A transition publishes a newer disabled generation before a paused old
	// refresh resumes and attempts to republish generation 1.
	rq.SetRecoveryWindow(reseq.RecoveryWindow{Revision: 2})
	rq.SetRecoveryWindow(stale)

	rq.ObserveFromPath(0, []byte("zero"), receiverContractSource, 7)
	rq.Pop()
	rq.ObserveFromPath(2, []byte("two"), receiverContractSource, 7)
	if deadline, armed := rq.ArmedDeadline(); !armed || deadline != clock.Now().Add(conservativeRecoveryService) {
		t.Fatalf("stale generation restored fast deadline: %v,%v", deadline, armed)
	}
}

func TestPausedRecoveryRefreshCannotCrossReceiverGeneration(t *testing.T) {
	type transition func(*testing.T, *Multipath, *reseq.Resequencer, uint32)
	transitions := []struct {
		name string
		run  transition
	}{
		{
			name: "ContractID renewal",
			run: func(t *testing.T, m *Multipath, _ *reseq.Resequencer, _ uint32) {
				second := receiverContractMessage(2, 80*time.Millisecond)
				if _, ok := m.contracts.acceptOffer(receiverContractSession, second, func() {}); !ok {
					t.Fatal("renewal rejected")
				}
				m.publishPeerRecoveryGeneration(m.peerState, m.contracts.receivedSnapshot().generation)
			},
		},
		{
			name: "SessionID adoption",
			run: func(t *testing.T, m *Multipath, _ *reseq.Resequencer, _ uint32) {
				generation, changed := m.contracts.adoptReceivedSession(receiverContractSession + 1)
				if !changed {
					t.Fatal("session adoption did not advance generation")
				}
				m.publishPeerRecoveryGeneration(m.peerState, generation)
			},
		},
		{
			name: "membership add",
			run: func(_ *testing.T, m *Multipath, _ *reseq.Resequencer, _ uint32) {
				m.invalidatePeerRecoveryEvidence(m.peerState)
			},
		},
		{
			name: "membership remove",
			run: func(_ *testing.T, m *Multipath, _ *reseq.Resequencer, _ uint32) {
				m.invalidatePeerRecoveryEvidence(m.peerState)
			},
		},
		{
			name: "same-key roam",
			run: func(t *testing.T, m *Multipath, _ *reseq.Resequencer, pathKey uint32) {
				if generation, changed := m.contracts.observeReceivedSource(pathKey, receiverStandbySource); changed {
					m.publishPeerRecoveryGeneration(m.peerState, generation)
				} else {
					t.Fatal("source roam did not advance generation")
				}
			},
		},
		{
			name: "rebaseline",
			run: func(_ *testing.T, m *Multipath, rq *reseq.Resequencer, _ uint32) {
				generation := m.invalidatePeerRecoveryEvidence(m.peerState)
				rq.RebaselineGeneration(netip.AddrPort{}, generation)
			},
		},
		{
			name: "resequencer replacement",
			run: func(_ *testing.T, m *Multipath, _ *reseq.Resequencer, _ uint32) {
				generation := m.contracts.invalidateReceivedEvidence()
				replacement := reseq.New(16, conservativeRecoveryService, m.clock)
				replacement.SetRecoveryAuthority(m.contracts.recoveryAuthority())
				replacement.SetFECActive(true)
				replacement.SetRecoveryPublication(generation, 0, nil)
				m.resequencer.Store(replacement)
			},
		},
		{
			name: "teardown",
			run: func(_ *testing.T, m *Multipath, rq *reseq.Resequencer, _ uint32) {
				m.invalidatePeerRecoveryEvidence(m.peerState)
				rq.Close()
			},
		},
	}

	for _, test := range transitions {
		t.Run(test.name, func(t *testing.T) {
			m, probers, clock := openRecoveryWindowPeer(t, 1)
			bringProberUpClean(t, probers[0], m.psk, clock, testProbeUpSucc)
			message := receiverContractMessage(1, 80*time.Millisecond)
			if _, ok := m.contracts.acceptOffer(receiverContractSession, message, func() {}); !ok {
				t.Fatal("offer rejected")
			}
			pathKey := reseqPathKey(m.paths[0].id, 9)
			if !completeReceiverACK(m.contracts, receiverContractSession, message, pathKey, receiverContractSource) {
				t.Fatal("ACK completion rejected")
			}

			paused := make(chan struct{}, 1)
			release := make(chan struct{})
			var once sync.Once
			m.beforeRecoveryPublish = func(_ *peerState, _ *reseq.Resequencer, _ uint64) {
				once.Do(func() { paused <- struct{}{} })
				<-release
			}
			refreshDone := make(chan struct{})
			go func() {
				m.refreshPeerRecoveryWindow(m.peerState)
				close(refreshDone)
			}()
			<-paused
			oldRQ := m.resequencer.Load()
			test.run(t, m, oldRQ, pathKey)
			close(release)
			<-refreshDone

			current := m.resequencer.Load()
			armedAt := clock.Now()
			current.ObserveFromPath(0, []byte("zero"), receiverContractSource, pathKey)
			current.Pop()
			current.ObserveFromPath(2, []byte("two"), receiverContractSource, pathKey)
			if deadline, armed := current.ArmedDeadline(); !armed ||
				deadline != armedAt.Add(conservativeRecoveryService) {
				t.Fatalf("stale refresh crossed %s: deadline=%v armed=%v", test.name, deadline, armed)
			}
		})
	}
}

func TestLiveFastGapRearmsFreshTOnContractOrSessionGeneration(t *testing.T) {
	tests := []struct {
		name       string
		transition func(*testing.T, *Multipath)
	}{
		{
			name: "ContractID",
			transition: func(t *testing.T, m *Multipath) {
				next := receiverContractMessage(2, 80*time.Millisecond)
				if _, ok := m.contracts.acceptOffer(receiverContractSession, next, func() {}); !ok {
					t.Fatal("renewal rejected")
				}
				m.publishPeerRecoveryGeneration(m.peerState, m.contracts.receivedSnapshot().generation)
			},
		},
		{
			name: "SessionID",
			transition: func(t *testing.T, m *Multipath) {
				generation, changed := m.contracts.adoptReceivedSession(receiverContractSession + 1)
				if !changed {
					t.Fatal("session adoption did not advance generation")
				}
				m.publishPeerRecoveryGeneration(m.peerState, generation)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			m, probers, clock := openRecoveryWindowPeer(t, 1)
			bringProberUpClean(t, probers[0], m.psk, clock, testProbeUpSucc)
			message := receiverContractMessage(1, 80*time.Millisecond)
			if _, ok := m.contracts.acceptOffer(receiverContractSession, message, func() {}); !ok {
				t.Fatal("offer rejected")
			}
			pathKey := reseqPathKey(m.paths[0].id, 9)
			if !completeReceiverACK(m.contracts, receiverContractSession, message, pathKey, receiverContractSource) {
				t.Fatal("ACK completion rejected")
			}
			m.refreshPeerRecoveryWindow(m.peerState)
			fastDeadline := armRecoveryWindowGap(m, receiverContractSource, pathKey)
			if fastDeadline.Equal(clock.Now().Add(conservativeRecoveryService)) {
				t.Fatal("precondition: gap did not arm fast")
			}
			clock.advance(time.Millisecond)
			rearmedAt := clock.Now()
			test.transition(t, m)
			if deadline, armed := m.resequencer.Load().ArmedDeadline(); !armed ||
				deadline != rearmedAt.Add(conservativeRecoveryService) {
				t.Fatalf("%s rearm = %v,%v, want fresh T", test.name, deadline, armed)
			}
		})
	}
}

func TestSameTopologyEvidenceUpdateDoesNotChangeLiveGap(t *testing.T) {
	m, probers, clock := openRecoveryWindowPeer(t, 1)
	bringProberUpClean(t, probers[0], m.psk, clock, testProbeUpSucc)
	message := receiverContractMessage(1, 80*time.Millisecond)
	if _, ok := m.contracts.acceptOffer(receiverContractSession, message, func() {}); !ok {
		t.Fatal("offer rejected")
	}
	pathKey := reseqPathKey(m.paths[0].id, 9)
	if !completeReceiverACK(m.contracts, receiverContractSession, message, pathKey, receiverContractSource) {
		t.Fatal("ACK completion rejected")
	}
	m.refreshPeerRecoveryWindow(m.peerState)
	rq := m.resequencer.Load()
	oldDeadline := armRecoveryWindowGap(m, receiverContractSource, pathKey)
	clock.advance(time.Millisecond)

	recordRecoveryRTTSample(t, probers[0], m.psk, clock, 80*time.Millisecond)
	newRTT := probers[0].RecoveryRTT().RTT
	m.refreshPeerRecoveryWindow(m.peerState)
	if deadline, armed := rq.ArmedDeadline(); !armed || !deadline.Equal(oldDeadline) {
		t.Fatalf("same-topology RTT update changed live gap: %v,%v want %v,true", deadline, armed, oldDeadline)
	}

	rq.ObserveFromPath(1, []byte("one"), receiverContractSource, pathKey)
	for range 2 {
		if _, ok := rq.Pop(); !ok {
			t.Fatal("gap fill did not release the old contiguous run")
		}
	}
	rq.ObserveFromPath(4, []byte("four"), receiverContractSource, pathKey)
	want := clock.Now().Add(recoveryWindow(message.ServiceBound, recoveryRTTHeadroom(newRTT)))
	if deadline, armed := rq.ArmedDeadline(); !armed || !deadline.Equal(want) {
		t.Fatalf("future gap did not use current evidence: %v,%v want %v,true", deadline, armed, want)
	}
}

func TestTopologyAuthorityClosesEveryPrepublicationTransitionInterval(t *testing.T) {
	type transition func(*testing.T, *Multipath, *reseq.Resequencer, uint32) *reseq.Resequencer
	transitions := []struct {
		name string
		run  transition
	}{
		{
			name: "ContractID renewal",
			run: func(t *testing.T, m *Multipath, rq *reseq.Resequencer, _ uint32) *reseq.Resequencer {
				if _, ok := m.contracts.acceptOffer(
					receiverContractSession,
					receiverContractMessage(2, 80*time.Millisecond),
					func() {},
				); !ok {
					t.Fatal("renewal rejected")
				}
				return rq
			},
		},
		{
			name: "contract service change",
			run: func(t *testing.T, m *Multipath, rq *reseq.Resequencer, _ uint32) *reseq.Resequencer {
				if _, ok := m.contracts.acceptOffer(
					receiverContractSession,
					receiverContractMessage(2, 90*time.Millisecond),
					func() {},
				); !ok {
					t.Fatal("service change rejected")
				}
				return rq
			},
		},
		{
			name: "SessionID adoption",
			run: func(t *testing.T, m *Multipath, rq *reseq.Resequencer, _ uint32) *reseq.Resequencer {
				if _, changed := m.contracts.adoptReceivedSession(receiverContractSession + 1); !changed {
					t.Fatal("session adoption did not advance topology")
				}
				return rq
			},
		},
		{
			name: "membership add",
			run: func(_ *testing.T, m *Multipath, rq *reseq.Resequencer, _ uint32) *reseq.Resequencer {
				m.contracts.invalidateReceivedEvidence()
				return rq
			},
		},
		{
			name: "membership remove",
			run: func(_ *testing.T, m *Multipath, rq *reseq.Resequencer, _ uint32) *reseq.Resequencer {
				m.contracts.invalidateReceivedEvidence()
				return rq
			},
		},
		{
			name: "same-key roam",
			run: func(t *testing.T, m *Multipath, rq *reseq.Resequencer, pathKey uint32) *reseq.Resequencer {
				if _, changed := m.contracts.observeReceivedSource(pathKey, receiverStandbySource); !changed {
					t.Fatal("source roam did not advance topology")
				}
				return rq
			},
		},
		{
			name: "rebaseline",
			run: func(_ *testing.T, m *Multipath, rq *reseq.Resequencer, _ uint32) *reseq.Resequencer {
				m.contracts.invalidateReceivedEvidence()
				return rq
			},
		},
		{
			name: "resequencer replacement",
			run: func(_ *testing.T, m *Multipath, _ *reseq.Resequencer, pathKey uint32) *reseq.Resequencer {
				generation := m.contracts.invalidateReceivedEvidence()
				replacement := reseq.New(16, conservativeRecoveryService, m.clock)
				replacement.SetRecoveryAuthority(m.contracts.recoveryAuthority())
				replacement.SetFECActive(true)
				replacement.SetRecoveryPublication(generation, 0, nil)
				m.resequencer.Store(replacement)
				replacement.ObserveFromPath(0, []byte("zero"), receiverContractSource, pathKey)
				replacement.Pop()
				replacement.ObserveFromPath(2, []byte("two"), receiverContractSource, pathKey)
				return replacement
			},
		},
		{
			name: "teardown",
			run: func(_ *testing.T, m *Multipath, rq *reseq.Resequencer, _ uint32) *reseq.Resequencer {
				m.contracts.invalidateReceivedEvidence()
				return rq
			},
		},
	}

	for _, test := range transitions {
		t.Run(test.name, func(t *testing.T) {
			m, probers, clock := openRecoveryWindowPeer(t, 1)
			bringProberUpClean(t, probers[0], m.psk, clock, testProbeUpSucc)
			message := receiverContractMessage(1, 80*time.Millisecond)
			if _, ok := m.contracts.acceptOffer(receiverContractSession, message, func() {}); !ok {
				t.Fatal("offer rejected")
			}
			pathKey := reseqPathKey(m.paths[0].id, 9)
			if !completeReceiverACK(m.contracts, receiverContractSession, message, pathKey, receiverContractSource) {
				t.Fatal("ACK completion rejected")
			}
			m.refreshPeerRecoveryWindow(m.peerState)
			rq := m.resequencer.Load()
			oldDeadline := armRecoveryWindowGap(m, receiverContractSource, pathKey)
			clock.advance(oldDeadline.Sub(clock.Now()))

			current := test.run(t, m, rq, pathKey)
			if current == rq {
				if item, ok := current.Pop(); ok {
					t.Fatalf("old W released during %s interval: %q", test.name, item.Payload)
				}
			}
			want := clock.Now().Add(conservativeRecoveryService)
			if deadline, armed := current.ArmedDeadline(); !armed || !deadline.Equal(want) {
				t.Fatalf("%s interval deadline=%v,%v want=%v,true", test.name, deadline, armed, want)
			}
		})
	}
}

func TestOlderSameGenerationRefreshCannotEraseNewACKVenue(t *testing.T) {
	m, probers, clock := openRecoveryWindowPeer(t, 2)
	for _, prober := range probers {
		bringProberUpClean(t, prober, m.psk, clock, testProbeUpSucc)
	}
	message := receiverContractMessage(1, 80*time.Millisecond)
	if _, ok := m.contracts.acceptOffer(receiverContractSession, message, func() {}); !ok {
		t.Fatal("offer rejected")
	}
	primaryKey := reseqPathKey(m.paths[0].id, 9)
	if !completeReceiverACK(m.contracts, receiverContractSession, message, primaryKey, receiverContractSource) {
		t.Fatal("primary ACK completion rejected")
	}

	paused := make(chan struct{}, 1)
	release := make(chan struct{})
	var hookMu sync.Mutex
	first := true
	m.beforeRecoveryPublish = func(_ *peerState, _ *reseq.Resequencer, _ uint64) {
		hookMu.Lock()
		pause := first
		first = false
		hookMu.Unlock()
		if pause {
			paused <- struct{}{}
			<-release
		}
	}
	oldDone := make(chan struct{})
	go func() {
		m.refreshPeerRecoveryWindow(m.peerState)
		close(oldDone)
	}()
	<-paused

	standbyKey := reseqPathKey(m.paths[1].id, 10)
	if !completeReceiverACK(m.contracts, receiverContractSession, message, standbyKey, receiverStandbySource) {
		t.Fatal("standby ACK completion rejected")
	}
	m.refreshPeerRecoveryWindow(m.peerState)
	close(release)
	<-oldDone

	armedAt := clock.Now()
	deadline := armRecoveryWindowGap(m, receiverStandbySource, standbyKey)
	want := armedAt.Add(recoveryWindow(message.ServiceBound, recoveryRTTHeadroom(testProbeRTT)))
	if !deadline.Equal(want) {
		t.Fatalf("older publication erased the new ACK venue: deadline=%v want=%v", deadline, want)
	}
}

func TestReservedOlderPublicationCannotEraseNewACKVenue(t *testing.T) {
	m, probers, clock := openRecoveryWindowPeer(t, 2)
	for _, prober := range probers {
		bringProberUpClean(t, prober, m.psk, clock, testProbeUpSucc)
	}
	message := receiverContractMessage(1, 80*time.Millisecond)
	if _, ok := m.contracts.acceptOffer(receiverContractSession, message, func() {}); !ok {
		t.Fatal("offer rejected")
	}
	primaryKey := reseqPathKey(m.paths[0].id, 9)
	if !completeReceiverACK(m.contracts, receiverContractSession, message, primaryKey, receiverContractSource) {
		t.Fatal("primary ACK completion rejected")
	}

	paused := make(chan struct{}, 1)
	release := make(chan struct{})
	var hookMu sync.Mutex
	first := true
	m.afterRecoveryPublicationReserve = func(
		_ *peerState,
		_ *reseq.Resequencer,
		_, _ uint64,
	) {
		hookMu.Lock()
		pause := first
		first = false
		hookMu.Unlock()
		if pause {
			paused <- struct{}{}
			<-release
		}
	}
	oldDone := make(chan struct{})
	go func() {
		m.refreshPeerRecoveryWindow(m.peerState)
		close(oldDone)
	}()
	<-paused

	standbyKey := reseqPathKey(m.paths[1].id, 10)
	if !completeReceiverACK(m.contracts, receiverContractSession, message, standbyKey, receiverStandbySource) {
		t.Fatal("standby ACK completion rejected")
	}
	m.refreshPeerRecoveryWindow(m.peerState)
	close(release)
	<-oldDone

	armedAt := clock.Now()
	deadline := armRecoveryWindowGap(m, receiverStandbySource, standbyKey)
	want := armedAt.Add(recoveryWindow(message.ServiceBound, recoveryRTTHeadroom(testProbeRTT)))
	if !deadline.Equal(want) {
		t.Fatalf("older reserved publication erased new ACK venue: deadline=%v want=%v", deadline, want)
	}
}

func TestOlderSameGenerationRefreshCannotRestoreLowerRTTHeadroom(t *testing.T) {
	m, probers, clock := openRecoveryWindowPeer(t, 1)
	bringProberUpClean(t, probers[0], m.psk, clock, testProbeUpSucc)
	message := receiverContractMessage(1, 80*time.Millisecond)
	if _, ok := m.contracts.acceptOffer(receiverContractSession, message, func() {}); !ok {
		t.Fatal("offer rejected")
	}
	pathKey := reseqPathKey(m.paths[0].id, 9)
	if !completeReceiverACK(m.contracts, receiverContractSession, message, pathKey, receiverContractSource) {
		t.Fatal("ACK completion rejected")
	}

	paused := make(chan struct{}, 1)
	release := make(chan struct{})
	var hookMu sync.Mutex
	first := true
	m.afterRecoveryPublicationReserve = func(
		_ *peerState,
		_ *reseq.Resequencer,
		_, _ uint64,
	) {
		hookMu.Lock()
		pause := first
		first = false
		hookMu.Unlock()
		if pause {
			paused <- struct{}{}
			<-release
		}
	}
	oldDone := make(chan struct{})
	go func() {
		m.refreshPeerRecoveryWindow(m.peerState)
		close(oldDone)
	}()
	<-paused

	recordRecoveryRTTSample(t, probers[0], m.psk, clock, 80*time.Millisecond)
	newRTT := probers[0].RecoveryRTT().RTT
	if newRTT <= testProbeRTT {
		t.Fatalf("new RTT = %v, want greater than old %v", newRTT, testProbeRTT)
	}
	m.refreshPeerRecoveryWindow(m.peerState)
	close(release)
	<-oldDone

	armedAt := clock.Now()
	deadline := armRecoveryWindowGap(m, receiverContractSource, pathKey)
	want := armedAt.Add(recoveryWindow(message.ServiceBound, recoveryRTTHeadroom(newRTT)))
	if !deadline.Equal(want) {
		t.Fatalf("older publication restored lower RTT headroom: deadline=%v want=%v", deadline, want)
	}
	stats := m.contracts.stats().Receiver
	if wantHeadroom := recoveryRTTHeadroom(newRTT); stats.Headroom != wantHeadroom {
		t.Fatalf("older rejected publication restored observed H=%v, want newer H=%v", stats.Headroom, wantHeadroom)
	}
}

func TestRefreshRevalidatesRTTFreshnessAtPublication(t *testing.T) {
	m, probers, clock := openRecoveryWindowPeer(t, 1)
	bringProberUpClean(t, probers[0], m.psk, clock, testProbeUpSucc)
	message := receiverContractMessage(1, 80*time.Millisecond)
	if _, ok := m.contracts.acceptOffer(receiverContractSession, message, func() {}); !ok {
		t.Fatal("offer rejected")
	}
	pathKey := reseqPathKey(m.paths[0].id, 9)
	if !completeReceiverACK(m.contracts, receiverContractSession, message, pathKey, receiverContractSource) {
		t.Fatal("ACK completion rejected")
	}

	paused := make(chan struct{}, 1)
	release := make(chan struct{})
	m.beforeRecoveryPublish = func(_ *peerState, _ *reseq.Resequencer, _ uint64) {
		paused <- struct{}{}
		<-release
	}
	done := make(chan struct{})
	go func() {
		m.refreshPeerRecoveryWindow(m.peerState)
		close(done)
	}()
	<-paused
	evidence := probers[0].RecoveryRTT()
	advance := evidence.FreshUntil.Sub(clock.Now()) - conservativeRecoveryService + time.Nanosecond
	if advance <= 0 {
		t.Fatalf("precondition: initial RTT freshness = %v", evidence.FreshUntil.Sub(clock.Now()))
	}
	clock.advance(advance)
	close(release)
	<-done

	armedAt := clock.Now()
	deadline := armRecoveryWindowGap(m, receiverContractSource, pathKey)
	want := armedAt.Add(conservativeRecoveryService)
	if !deadline.Equal(want) {
		t.Fatalf("publication froze stale wall-clock evidence: deadline=%v want=%v", deadline, want)
	}
}

func TestTopologyAdvanceBeforePublicationForcesConservativeNewGap(t *testing.T) {
	m, probers, clock := openRecoveryWindowPeer(t, 1)
	bringProberUpClean(t, probers[0], m.psk, clock, testProbeUpSucc)
	message := receiverContractMessage(1, 80*time.Millisecond)
	if _, ok := m.contracts.acceptOffer(receiverContractSession, message, func() {}); !ok {
		t.Fatal("offer rejected")
	}
	pathKey := reseqPathKey(m.paths[0].id, 9)
	if !completeReceiverACK(m.contracts, receiverContractSession, message, pathKey, receiverContractSource) {
		t.Fatal("ACK completion rejected")
	}
	m.refreshPeerRecoveryWindow(m.peerState)

	renewal := receiverContractMessage(2, message.ServiceBound)
	if _, ok := m.contracts.acceptOffer(receiverContractSession, renewal, func() {}); !ok {
		t.Fatal("renewal rejected")
	}
	armedAt := clock.Now()
	deadline := armRecoveryWindowGap(m, receiverContractSource, pathKey)
	want := armedAt.Add(conservativeRecoveryService)
	if !deadline.Equal(want) {
		t.Fatalf("gap armed during unpublished topology transition at %v, want %v", deadline, want)
	}
}

func TestTopologyAdvanceBeforePublicationPreventsOldFastExpiry(t *testing.T) {
	m, probers, clock := openRecoveryWindowPeer(t, 1)
	bringProberUpClean(t, probers[0], m.psk, clock, testProbeUpSucc)
	message := receiverContractMessage(1, 80*time.Millisecond)
	if _, ok := m.contracts.acceptOffer(receiverContractSession, message, func() {}); !ok {
		t.Fatal("offer rejected")
	}
	pathKey := reseqPathKey(m.paths[0].id, 9)
	if !completeReceiverACK(m.contracts, receiverContractSession, message, pathKey, receiverContractSource) {
		t.Fatal("ACK completion rejected")
	}
	m.refreshPeerRecoveryWindow(m.peerState)
	oldDeadline := armRecoveryWindowGap(m, receiverContractSource, pathKey)
	clock.advance(oldDeadline.Sub(clock.Now()) - time.Nanosecond)

	renewal := receiverContractMessage(2, message.ServiceBound)
	transitionAt := clock.Now()
	if _, ok := m.contracts.acceptOffer(receiverContractSession, renewal, func() {}); !ok {
		t.Fatal("renewal rejected")
	}
	clock.advance(time.Nanosecond)
	if item, ok := m.resequencer.Load().Pop(); ok {
		t.Fatalf("old fast expiry released during unpublished topology transition: %q", item.Payload)
	}
	want := transitionAt.Add(conservativeRecoveryService)
	if deadline, armed := m.resequencer.Load().ArmedDeadline(); !armed || !deadline.Equal(want) {
		t.Fatalf("transition interval deadline=%v,%v want=%v,true", deadline, armed, want)
	}
}

func TestDispatchAuthenticatedACKCompletionRejectsSameKeyRoam(t *testing.T) {
	m, probers, clock := openShapedRecoveryWindowPeer(t, 1)
	bringProberUpClean(t, probers[0], m.psk, clock, testProbeUpSucc)
	view := m.paths[0]
	base := view.shaper.(recoveryPathShaper)
	firstPeer, firstSource := rawPeer(t)
	secondPeer, secondSource := rawPeer(t)
	t.Cleanup(func() {
		_ = firstPeer.Close()
		_ = secondPeer.Close()
	})

	const session = uint64(0xA31401)
	challenge := reflectProbeIssuedChallenge(t, m, view, m.psk, firstPeer, firstSource, session, 0, 0)
	message := receiverContractMessage(1, 80*time.Millisecond)
	oldBlocked, oldEcho := offerRecoveryContractBlocked(
		t, m, view, base, m.psk, firstSource, session, 1, challenge, message,
	)
	if m.contracts.receivedSnapshot().acked {
		t.Fatal("venue became usable before the blocked ACK write completed")
	}

	// A padded authenticated probe carries no contract, but its exact same
	// composite path arriving from a new source advances the topology generation.
	view.shaper = base
	roamRaw, err := frame.Encode(m.psk, frame.Probe{
		PathID:         view.id,
		ProbeSeq:       2,
		TimestampNanos: clock.Now().UnixNano(),
		SessionID:      session,
		Challenge:      oldEcho.Challenge,
		Padded:         true,
		PadLen:         8,
	})
	if err != nil {
		t.Fatal(err)
	}
	m.handleInbound(view, roamRaw, secondSource)
	roamEcho := readProbe(t, secondPeer, mustFrameCodec(t, m.psk))

	close(oldBlocked.release)
	_ = readProbe(t, firstPeer, mustFrameCodec(t, m.psk))
	if m.contracts.receivedSnapshot().acked {
		t.Fatal("old ACK completion restored a venue after same-key roam")
	}

	currentBlocked, _ := offerRecoveryContractBlocked(
		t, m, view, base, m.psk, secondSource, session, 3, roamEcho.Challenge, message,
	)
	close(currentBlocked.release)
	_ = readProbe(t, secondPeer, mustFrameCodec(t, m.psk))
	deadline := time.Now().Add(time.Second)
	for !m.contracts.receivedSnapshot().acked && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !m.contracts.receivedSnapshot().acked {
		t.Fatal("current-generation ACK completion did not publish its venue")
	}

	clock.advance(2*conservativeRecoveryService + time.Nanosecond)
	sendCleanProbes(t, probers[0], m.psk, clock, 1)
	m.refreshPeerRecoveryWindow(m.peerState)
	pathKey := reseqPathKey(view.id, view.id)
	armedAt := clock.Now()
	deadlineAt := armRecoveryWindowGap(m, secondSource, pathKey)
	want := armedAt.Add(recoveryWindow(message.ServiceBound, recoveryRTTHeadroom(testProbeRTT)))
	if deadlineAt != want {
		t.Fatalf("current dispatch venue deadline = %v, want %v", deadlineAt, want)
	}
}

func TestDispatchFailedAuthenticatedACKPublishesNoVenue(t *testing.T) {
	m, _, _ := openShapedRecoveryWindowPeer(t, 1)
	view := m.paths[0]
	base := view.shaper.(recoveryPathShaper)
	peer, source := rawPeer(t)
	t.Cleanup(func() { _ = peer.Close() })

	const session = uint64(0xA31402)
	challenge := reflectProbeIssuedChallenge(t, m, view, m.psk, peer, source, session, 0, 0)
	message := receiverContractMessage(1, 80*time.Millisecond)
	payload, err := telemetry.EncodeRecoveryContract(message)
	if err != nil {
		t.Fatal(err)
	}
	failed := &failedRecoveryACKShaper{
		recoveryPathShaper: base,
		entered:            make(chan struct{}, 1),
		release:            make(chan struct{}),
		completed:          make(chan struct{}),
	}
	view.shaper = failed
	dispatchTestProbe(t, m, view, source, frame.Probe{
		PathID:         view.id,
		ProbeSeq:       1,
		TimestampNanos: time.Now().UnixNano(),
		SessionID:      session,
		Challenge:      challenge,
		Payload:        payload,
	})
	<-failed.entered
	close(failed.release)
	<-failed.completed
	if snapshot := m.contracts.receivedSnapshot(); snapshot.acked || len(snapshot.venues) != 0 {
		t.Fatalf("failed ACK published receiver venues: %+v", snapshot.venues)
	}
}

type failedRecoveryACKShaper struct {
	recoveryPathShaper
	entered   chan struct{}
	release   chan struct{}
	completed chan struct{}
}

func (s *failedRecoveryACKShaper) TryWritePriority([]byte, shaper.WriteFunc) (bool, <-chan error, error) {
	done := make(chan error, 1)
	s.entered <- struct{}{}
	go func() {
		<-s.release
		done <- errors.New("forced ACK write failure")
		close(s.completed)
	}()
	return true, done, nil
}

func mustFrameCodec(t testing.TB, psk config.Key) *frame.Codec {
	t.Helper()
	codec, err := frame.NewCodec(psk)
	if err != nil {
		t.Fatal(err)
	}
	return codec
}

func dispatchTestProbe(
	t testing.TB,
	m *Multipath,
	view *peerPathState,
	source netip.AddrPort,
	probe frame.Probe,
) []byte {
	t.Helper()
	raw, err := frame.Encode(m.psk, probe)
	if err != nil {
		t.Fatal(err)
	}
	m.handleInbound(view, raw, source)
	return raw
}

func TestDispatchRecoveryControlEvidenceMatrix(t *testing.T) {
	t.Run("bootstrap verbatim OFFER echo", func(t *testing.T) {
		m, _, clock := openShapedRecoveryWindowPeer(t, 1)
		peer, source := rawPeer(t)
		t.Cleanup(func() { _ = peer.Close() })
		message := receiverContractMessage(1, 80*time.Millisecond)
		payload, err := telemetry.EncodeRecoveryContract(message)
		if err != nil {
			t.Fatal(err)
		}
		dispatchTestProbe(t, m, m.paths[0], source, frame.Probe{
			PathID:         m.paths[0].id,
			ProbeSeq:       0,
			TimestampNanos: clock.Now().UnixNano(),
			SessionID:      0xA31410,
			Payload:        payload,
		})
		echo := readProbe(t, peer, mustFrameCodec(t, m.psk))
		if string(echo.Payload) != string(payload) {
			t.Fatal("bootstrap did not echo OFFER payload verbatim")
		}
		if m.contracts.receivedSnapshot().acked {
			t.Fatal("bootstrap OFFER created usable receiver evidence")
		}
	})

	t.Run("padded probe", func(t *testing.T) {
		m, _, clock := openShapedRecoveryWindowPeer(t, 1)
		peer, source := rawPeer(t)
		t.Cleanup(func() { _ = peer.Close() })
		const session = uint64(0xA31411)
		before := m.contracts.stats()
		challenge := reflectProbeIssuedChallenge(t, m, m.paths[0], m.psk, peer, source, session, 0, 0)
		dispatchTestProbe(t, m, m.paths[0], source, frame.Probe{
			PathID:         m.paths[0].id,
			ProbeSeq:       1,
			TimestampNanos: clock.Now().UnixNano(),
			SessionID:      session,
			Challenge:      challenge,
			Padded:         true,
			PadLen:         8,
		})
		_ = readProbe(t, peer, mustFrameCodec(t, m.psk))
		if m.contracts.receivedSnapshot().acked {
			t.Fatal("padded probe created usable receiver evidence")
		}
		after := m.contracts.stats()
		if after.Sender.WrongRejections != before.Sender.WrongRejections ||
			after.Sender.FallbackReason != before.Sender.FallbackReason {
			t.Fatalf("padded non-contract echo changed telemetry: before=%+v after=%+v", before, after)
		}
	})

	t.Run("malformed recognized payload", func(t *testing.T) {
		m, _, clock := openShapedRecoveryWindowPeer(t, 1)
		peer, source := rawPeer(t)
		t.Cleanup(func() { _ = peer.Close() })
		const session = uint64(0xA31412)
		challenge := reflectProbeIssuedChallenge(t, m, m.paths[0], m.psk, peer, source, session, 0, 0)
		malformed := []byte{'W', 'B', 'R', 'C', 1}
		dispatchTestProbe(t, m, m.paths[0], source, frame.Probe{
			PathID:         m.paths[0].id,
			ProbeSeq:       1,
			TimestampNanos: clock.Now().UnixNano(),
			SessionID:      session,
			Challenge:      challenge,
			Payload:        malformed,
		})
		echo := readProbe(t, peer, mustFrameCodec(t, m.psk))
		if string(echo.Payload) != string(malformed) {
			t.Fatal("malformed recognized payload was rewritten")
		}
		if m.contracts.receivedSnapshot().acked {
			t.Fatal("malformed payload created usable receiver evidence")
		}
	})

	t.Run("replay and inconsistent identity", func(t *testing.T) {
		m, _, clock := openShapedRecoveryWindowPeer(t, 1)
		peer, source := rawPeer(t)
		t.Cleanup(func() { _ = peer.Close() })
		view := m.paths[0]
		const session = uint64(0xA31413)
		challenge := reflectProbeIssuedChallenge(t, m, view, m.psk, peer, source, session, 0, 0)
		message := receiverContractMessage(1, 80*time.Millisecond)
		payload, err := telemetry.EncodeRecoveryContract(message)
		if err != nil {
			t.Fatal(err)
		}
		raw := dispatchTestProbe(t, m, view, source, frame.Probe{
			PathID:         view.id,
			ProbeSeq:       1,
			TimestampNanos: clock.Now().UnixNano(),
			SessionID:      session,
			Challenge:      challenge,
			Payload:        payload,
		})
		ackEcho := readProbe(t, peer, mustFrameCodec(t, m.psk))
		deadline := time.Now().Add(time.Second)
		for !m.contracts.receivedSnapshot().acked && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		before := m.contracts.receivedSnapshot()
		if !before.acked {
			t.Fatal("precondition: exact ACK completion did not publish venue")
		}
		m.handleInbound(view, raw, source)
		afterReplay := m.contracts.receivedSnapshot()
		if afterReplay.generation != before.generation || len(afterReplay.venues) != len(before.venues) {
			t.Fatal("replayed OFFER changed receiver evidence")
		}

		inconsistent := message
		inconsistent.ServiceBound = 90 * time.Millisecond
		inconsistentPayload, err := telemetry.EncodeRecoveryContract(inconsistent)
		if err != nil {
			t.Fatal(err)
		}
		dispatchTestProbe(t, m, view, source, frame.Probe{
			PathID:         view.id,
			ProbeSeq:       2,
			TimestampNanos: clock.Now().UnixNano(),
			SessionID:      session,
			Challenge:      ackEcho.Challenge,
			Payload:        inconsistentPayload,
		})
		echo := readProbe(t, peer, mustFrameCodec(t, m.psk))
		echoMessage, recognized, err := telemetry.DecodeRecoveryContract(echo.Payload)
		if err != nil || !recognized || echoMessage.Type != telemetry.RecoveryContractOffer {
			t.Fatalf("inconsistent identity echo = %+v recognized=%v err=%v", echoMessage, recognized, err)
		}
		if m.contracts.receivedSnapshot().acked {
			t.Fatal("inconsistent identity retained usable receiver evidence")
		}
	})
}

func TestEarliestResequencerDeadlineAcrossPeers(t *testing.T) {
	clock := newFakeClock()
	peers := []*peerState{
		makeArmedRecoveryPeer(clock, 1, 80*time.Millisecond),
		makeArmedRecoveryPeer(clock, 2, 60*time.Millisecond),
	}
	if got, want := earliestResequencerDeadline(peers, clock.Now().Add(conservativeRecoveryService)), clock.Now().Add(60*time.Millisecond); got != want {
		t.Fatalf("earliest deadline = %v, want %v", got, want)
	}
}

func TestReceiveFuncUsesEarliestArmedDeadlineAcrossPeers(t *testing.T) {
	clock := newFakeClock()
	m := &Multipath{
		clock: clock,
		peers: []*peerState{
			makeArmedRecoveryPeer(clock, 1, 80*time.Millisecond),
			makeArmedRecoveryPeer(clock, 2, 60*time.Millisecond),
		},
	}
	view := append([]*peerState(nil), m.peers...)
	m.peersView.Store(&view)
	parked := make(chan time.Time, 1)
	m.beforeReceivePark = func(deadline time.Time) {
		select {
		case parked <- deadline:
		default:
		}
	}
	receive := m.newReceiveFunc(make(chan struct{}, 1), make(chan struct{}))
	resultCh := make(chan int, 1)
	go func() {
		packets := [][]byte{make([]byte, 64)}
		sizes := make([]int, 1)
		endpoints := make([]Endpoint, 1)
		n, err := receive(packets, sizes, endpoints)
		if err != nil || n != 1 {
			resultCh <- -1
			return
		}
		resultCh <- sizes[0]
	}()

	want := clock.Now().Add(60 * time.Millisecond)
	if got := <-parked; got != want {
		t.Fatalf("receive parked until %v, want earliest peer deadline %v", got, want)
	}
	clock.advance(60 * time.Millisecond)
	if got := <-resultCh; got != len("two") {
		t.Fatalf("receive result size = %d, want %d", got, len("two"))
	}
}

func TestReceiveFuncWakesAtExactArmedRecoveryDeadline(t *testing.T) {
	m, _, clock := openRecoveryWindowPeer(t, 1)
	const hold = 60 * time.Millisecond
	pathKey := reseqPathKey(m.paths[0].id, 9)
	rq := m.resequencer.Load()
	generation := m.contracts.receivedSnapshot().generation
	rq.SetRecoveryWindow(reseq.RecoveryWindow{
		Enabled:    true,
		Revision:   generation,
		PathKey:    pathKey,
		Source:     receiverContractSource,
		Hold:       hold,
		ValidUntil: clock.Now().Add(conservativeRecoveryService),
	})
	rq.ObserveFromPath(0, []byte("zero"), receiverContractSource, pathKey)
	rq.Pop()
	rq.ObserveFromPath(2, []byte("two"), receiverContractSource, pathKey)

	parked := make(chan time.Time, 1)
	m.beforeReceivePark = func(deadline time.Time) {
		select {
		case parked <- deadline:
		default:
		}
	}
	receive := m.newReceiveFunc(m.deliverSignal, m.recvClosed)
	type result struct {
		n    int
		size int
		err  error
	}
	resultCh := make(chan result, 1)
	go func() {
		packets := [][]byte{make([]byte, 64)}
		sizes := make([]int, 1)
		endpoints := make([]Endpoint, 1)
		n, err := receive(packets, sizes, endpoints)
		resultCh <- result{n: n, size: sizes[0], err: err}
	}()

	wantDeadline := clock.Now().Add(hold)
	if got := <-parked; got != wantDeadline {
		t.Fatalf("receive parked until %v, want exact gap deadline %v", got, wantDeadline)
	}
	clock.advance(hold - time.Nanosecond)
	select {
	case got := <-resultCh:
		t.Fatalf("receive returned before W: %+v", got)
	default:
	}
	clock.advance(time.Nanosecond)
	got := <-resultCh
	if got.err != nil || got.n != 1 || got.size != len("two") {
		t.Fatalf("receive at W = %+v, want one successor", got)
	}
}

const recoveryReceiveService = 80 * time.Millisecond

type recoveryReceiveResult struct {
	n    int
	size int
	err  error
}

func openArmedRecoveryReceiveGap(t *testing.T) (*Multipath, *fakeClock, time.Time) {
	t.Helper()
	m, probers, clock := openRecoveryWindowPeer(t, 1)
	bringProberUpClean(t, probers[0], m.psk, clock, testProbeUpSucc)
	message := receiverContractMessage(1, recoveryReceiveService)
	if _, ok := m.contracts.acceptOffer(receiverContractSession, message, func() {}); !ok {
		t.Fatal("offer rejected")
	}
	pathKey := reseqPathKey(m.paths[0].id, 9)
	if !completeReceiverACK(m.contracts, receiverContractSession, message, pathKey, receiverContractSource) {
		t.Fatal("ACK completion rejected")
	}
	m.refreshPeerRecoveryWindow(m.peerState)
	return m, clock, armRecoveryWindowGap(m, receiverContractSource, pathKey)
}

func drainRecoveryReceiveSignals(m *Multipath) {
	for {
		select {
		case <-m.deliverSignal:
		case <-m.recoveryAuthoritySignal:
		default:
			return
		}
	}
}

func startRecoveryReceive(m *Multipath) <-chan recoveryReceiveResult {
	resultCh := make(chan recoveryReceiveResult, 1)
	receive := m.newReceiveFunc(m.deliverSignal, m.recvClosed)
	go func() {
		packets := [][]byte{make([]byte, 64)}
		sizes := make([]int, 1)
		endpoints := make([]Endpoint, 1)
		n, err := receive(packets, sizes, endpoints)
		resultCh <- recoveryReceiveResult{n: n, size: sizes[0], err: err}
	}()
	return resultCh
}

func TestReceiveFuncReleasesByAuthorityTransitionDeadlineWhenPublicationStalls(t *testing.T) {
	m, clock, oldDeadline := openArmedRecoveryReceiveGap(t)
	drainRecoveryReceiveSignals(m)
	parked := make(chan time.Time, 4)
	m.beforeReceivePark = func(deadline time.Time) {
		parked <- deadline
	}
	resultCh := startRecoveryReceive(m)
	if firstPark := <-parked; !firstPark.Equal(oldDeadline) {
		t.Fatalf("initial receive park = %v, want old W %v", firstPark, oldDeadline)
	}

	clock.advance(time.Nanosecond)
	transitionAt := clock.Now()
	renewal := receiverContractMessage(2, recoveryReceiveService)
	if _, ok := m.contracts.acceptOffer(receiverContractSession, renewal, func() {}); !ok {
		t.Fatal("renewal rejected")
	}
	// Deliberately do not call publishPeerRecoveryGeneration: this holds the
	// production interval between coordinator advance and explicit publication.
	clock.advance(oldDeadline.Sub(clock.Now()))
	rearmedDeadline := <-parked
	transitionDeadline := transitionAt.Add(conservativeRecoveryService)
	clock.advance(transitionDeadline.Sub(clock.Now()))

	select {
	case got := <-resultCh:
		if got.err != nil || got.n != 1 || got.size != len("two") {
			t.Fatalf("receive at transitionAt+T = %+v, want one successor", got)
		}
	case <-time.After(100 * time.Millisecond):
		if rearmedDeadline.After(clock.Now()) {
			clock.advance(rearmedDeadline.Sub(clock.Now()))
		}
		got := <-resultCh
		t.Fatalf(
			"receive exceeded transitionAt+T: transition=%v bound=%v lateDeadline=%v result=%+v",
			transitionAt,
			transitionDeadline,
			rearmedDeadline,
			got,
		)
	}
}

func TestReceiveFuncExpiresImmediatelyWhenAuthorityObservedPastTransitionDeadline(t *testing.T) {
	m, clock, _ := openArmedRecoveryReceiveGap(t)
	drainRecoveryReceiveSignals(m)
	clock.advance(time.Nanosecond)
	transitionAt := clock.Now()
	renewal := receiverContractMessage(2, recoveryReceiveService)
	if _, ok := m.contracts.acceptOffer(receiverContractSession, renewal, func() {}); !ok {
		t.Fatal("renewal rejected")
	}
	clock.advance(conservativeRecoveryService + time.Nanosecond)

	parked := make(chan time.Time, 4)
	m.beforeReceivePark = func(deadline time.Time) {
		parked <- deadline
	}
	resultCh := startRecoveryReceive(m)

	select {
	case got := <-resultCh:
		if got.err != nil || got.n != 1 || got.size != len("two") {
			t.Fatalf("late authority observation result = %+v, want one successor", got)
		}
	case lateDeadline := <-parked:
		clock.advance(lateDeadline.Sub(clock.Now()))
		got := <-resultCh
		t.Fatalf(
			"authority observed after transitionAt+T rearmed late: transition=%v observed=%v deadline=%v result=%+v",
			transitionAt,
			clock.Now(),
			lateDeadline,
			got,
		)
	case <-time.After(time.Second):
		t.Fatal("late authority observation neither released nor parked")
	}
}

func TestReceiveFuncCoalescesTopologyAdvancesAtLatestTransitionDeadline(t *testing.T) {
	m, clock, _ := openArmedRecoveryReceiveGap(t)
	drainRecoveryReceiveSignals(m)
	clock.mu.Lock()
	timersBefore := len(clock.timers)
	clock.mu.Unlock()
	firstPark := make(chan struct{}, 1)
	releaseFirstPark := make(chan struct{})
	laterParks := make(chan time.Time, 4)
	var hookMu sync.Mutex
	first := true
	m.beforeReceivePark = func(deadline time.Time) {
		hookMu.Lock()
		block := first
		first = false
		hookMu.Unlock()
		if block {
			firstPark <- struct{}{}
			<-releaseFirstPark
			return
		}
		laterParks <- deadline
	}
	resultCh := startRecoveryReceive(m)
	clock.mu.Lock()
	timersAfterCreate := len(clock.timers)
	clock.mu.Unlock()
	if timersAfterCreate != timersBefore+1 {
		t.Fatalf("receive timers after construction = %d, want %d", timersAfterCreate, timersBefore+1)
	}
	<-firstPark

	clock.advance(time.Millisecond)
	if _, ok := m.contracts.acceptOffer(
		receiverContractSession,
		receiverContractMessage(2, recoveryReceiveService),
		func() {},
	); !ok {
		t.Fatal("renewal rejected")
	}
	clock.advance(time.Millisecond)
	if _, changed := m.contracts.adoptReceivedSession(receiverContractSession + 1); !changed {
		t.Fatal("session adoption did not advance topology")
	}
	clock.advance(time.Millisecond)
	latestTransitionAt := clock.Now()
	m.contracts.invalidateReceivedEvidence()
	if len(m.recoveryAuthoritySignal) != 1 {
		t.Fatalf("coalesced bind authority notifications = %d, want 1", len(m.recoveryAuthoritySignal))
	}
	close(releaseFirstPark)

	wantDeadline := latestTransitionAt.Add(conservativeRecoveryService)
	if deadline := <-laterParks; !deadline.Equal(wantDeadline) {
		t.Fatalf("coalesced transition deadline = %v, want latest transition %v", deadline, wantDeadline)
	}
	select {
	case deadline := <-laterParks:
		if !deadline.Equal(wantDeadline) {
			t.Fatalf("notification rescan changed transition deadline to %v", deadline)
		}
	case <-time.After(5 * time.Millisecond):
	}
	select {
	case deadline := <-laterParks:
		t.Fatalf("authority wake spun after its one coalesced resequencer notification; extra park %v", deadline)
	case <-time.After(5 * time.Millisecond):
	}
	clock.mu.Lock()
	timersAfterWake := len(clock.timers)
	clock.mu.Unlock()
	if timersAfterWake != timersAfterCreate {
		t.Fatalf("authority wake leaked timers: before=%d after=%d", timersAfterCreate, timersAfterWake)
	}

	clock.advance(wantDeadline.Sub(clock.Now()) - time.Nanosecond)
	select {
	case got := <-resultCh:
		t.Fatalf("coalesced transition released before transitionAt+T: %+v", got)
	default:
	}
	clock.advance(time.Nanosecond)
	got := <-resultCh
	if got.err != nil || got.n != 1 || got.size != len("two") {
		t.Fatalf("coalesced transition release = %+v, want one successor", got)
	}
}

func TestReceiveFuncAuthorityWakeCannotDeliverAfterClose(t *testing.T) {
	m, _, _ := openArmedRecoveryReceiveGap(t)
	drainRecoveryReceiveSignals(m)
	parked := make(chan struct{}, 1)
	m.beforeReceivePark = func(time.Time) {
		select {
		case parked <- struct{}{}:
		default:
		}
	}
	resultCh := startRecoveryReceive(m)
	<-parked
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	got := <-resultCh
	if got.n != 0 || !errors.Is(got.err, errClosed) {
		t.Fatalf("receive after Close = %+v, want 0/errClosed", got)
	}
}
