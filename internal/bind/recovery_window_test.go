package bind

import (
	"net/netip"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/fec"
	"github.com/7mind/wanbond/internal/reseq"
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
	if !m.contracts.recordReceivedACK(receiverContractSession, message, pathKey, receiverContractSource) {
		t.Fatal("exact ACK completion rejected")
	}
	standbyPathKey := reseqPathKey(m.paths[1].id, 10)
	if !m.contracts.recordReceivedACK(receiverContractSession, message, standbyPathKey, receiverStandbySource) {
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
				if !m.contracts.recordReceivedACK(receiverContractSession, message, pathKey+1, receiverContractSource) {
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
				if !m.contracts.recordReceivedACK(receiverContractSession, message, pathKey, receiverContractSource) {
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
				if !m.contracts.recordReceivedACK(receiverContractSession, message, pathKey, receiverContractSource) {
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
				if !m.contracts.recordReceivedACK(receiverContractSession, message, pathKey, receiverContractSource) {
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
				if !m.contracts.recordReceivedACK(receiverContractSession, message, pathKey, receiverContractSource) {
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
				if !m.contracts.recordReceivedACK(receiverContractSession, message, pathKey, receiverContractSource) {
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
				if !m.contracts.recordReceivedACK(receiverContractSession, message, pathKey, receiverContractSource) {
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
	if !coordinator.recordReceivedACK(100, first, 7, receiverContractSource) {
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
	rq.SetRecoveryWindow(reseq.RecoveryWindow{
		Enabled:    true,
		Revision:   1,
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
