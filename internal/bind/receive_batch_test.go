package bind

import (
	"net/netip"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/reseq"
)

func TestReceiveDrainsReadyBatch(t *testing.T) {
	m, err := newMultipath(t, loopbackPaths(1), testKey(t, 0x45))
	if err != nil {
		t.Fatal(err)
	}
	receivers, _, err := m.Open(0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	const count = 8
	packets := make([][]byte, count)
	sizes := make([]int, count)
	endpoints := make([]Endpoint, count)
	src := netip.MustParseAddrPort("192.0.2.1:51820")
	for i := range packets {
		packets[i] = make([]byte, 1500)
		m.resequencer.Load().Observe(uint64(i), []byte{byte(i)}, src)
	}
	n, err := receivers[0](packets, sizes, endpoints)
	if err != nil || n != count {
		t.Fatalf("ready receive batch returned %d packets, want %d: %v", n, count, err)
	}
	for i := range packets {
		if sizes[i] != 1 || packets[i][0] != byte(i) || endpoints[i] != m.virt {
			t.Errorf("packet %d lost order or peer identity: size=%d data=%v endpoint=%v", i, sizes[i], packets[i][:sizes[i]], endpoints[i])
		}
	}
}

func TestAdaptiveReceiveCoalescesBulkAndFlushesInteractive(t *testing.T) {
	for _, interactive := range []bool{false, true} {
		t.Run(map[bool]string{false: "bulk", true: "interactive"}[interactive], func(t *testing.T) {
			clock := newFakeClock()
			m, _ := newProbingMultipath(t, loopbackPaths(1), testKey(t, 0x46), clock)
			receivers, _, err := m.Open(0)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = m.Close() })
			start := clock.Now()
			src := netip.MustParseAddrPort("192.0.2.1:51820")
			rq := m.resequencer.Load()
			rq.Observe(1, []byte{1}, src)
			injected := false
			m.beforeReceivePark = func(deadline time.Time) {
				if injected || deadline != start.Add(4*time.Millisecond) {
					t.Fatalf("unexpected receive deadline: %v, injected=%v", deadline, injected)
				}
				injected = true
				if interactive {
					a := m.adaptive.Load()
					a.interactive <- reseq.Item{Payload: []byte{2}, Src: src}
					a.notify()
				} else {
					rq.Observe(2, []byte{2}, src)
					clock.advance(4 * time.Millisecond)
				}
			}
			packets := [][]byte{make([]byte, 1500), make([]byte, 1500), make([]byte, 1500)}
			sizes, endpoints := make([]int, len(packets)), make([]Endpoint, len(packets))
			n, err := receivers[0](packets, sizes, endpoints)
			if err != nil || n != 2 || !injected {
				t.Fatalf("burst split before coalescing: packets=%d injected=%v error=%v", n, injected, err)
			}
			if interactive && clock.Now() != start {
				t.Fatal("interactive delivery waited for bulk timer")
			}
		})
	}
}

// receiveGapSource is the outer source of the datagrams the armed-gap tests buffer.
var receiveGapSource = netip.MustParseAddrPort("192.0.2.1:51820")

// armReceiveGap leaves rq holding one datagram behind a missing predecessor and returns
// the instant its hold expires.
func armReceiveGap(t testing.TB, rq *reseq.Resequencer, hold time.Duration) time.Time {
	t.Helper()
	rq.SetHoldBound(hold)
	rq.Observe(1, []byte("one"), receiveGapSource)
	rq.Pop()
	rq.Observe(3, []byte("two"), receiveGapSource)
	deadline, armed := rq.ArmedDeadline()
	if !armed {
		t.Fatal("a datagram behind a missing predecessor armed no hold")
	}
	return deadline
}

// makeArmedPeer builds a bare peer whose resequencer holds a gap for hold.
func makeArmedPeer(t testing.TB, clock *fakeClock, hold time.Duration) *peerState {
	t.Helper()
	rq := reseq.New(16, resequencerTimeout, clock)
	armReceiveGap(t, rq, hold)
	peer := &peerState{virt: &udpEndpoint{}}
	peer.resequencer.Store(rq)
	return peer
}

func TestEarliestResequencerDeadlineAcrossPeers(t *testing.T) {
	clock := newFakeClock()
	peers := []*peerState{
		makeArmedPeer(t, clock, 80*time.Millisecond),
		makeArmedPeer(t, clock, 60*time.Millisecond),
	}
	if got, want := earliestResequencerDeadline(peers, clock.Now().Add(resequencerTimeout)), clock.Now().Add(60*time.Millisecond); got != want {
		t.Fatalf("earliest deadline = %v, want %v", got, want)
	}
}

func TestReceiveFuncUsesEarliestArmedDeadlineAcrossPeers(t *testing.T) {
	clock := newFakeClock()
	m := &Multipath{
		clock: clock,
		peers: []*peerState{
			makeArmedPeer(t, clock, 80*time.Millisecond),
			makeArmedPeer(t, clock, 60*time.Millisecond),
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

func TestReceiveFuncWakesAtExactArmedDeadline(t *testing.T) {
	clock := newFakeClock()
	m, _ := newProbingMultipath(t, loopbackPaths(1), testKey(t, 0x47), clock)
	if _, _, err := m.Open(0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	const hold = 60 * time.Millisecond
	wantDeadline := clock.Now().Add(hold)
	if got := armReceiveGap(t, m.resequencer.Load(), hold); got != wantDeadline {
		t.Fatalf("gap deadline = %v, want %v", got, wantDeadline)
	}

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

	if got := <-parked; got != wantDeadline {
		t.Fatalf("receive parked until %v, want exact gap deadline %v", got, wantDeadline)
	}
	clock.advance(hold - time.Nanosecond)
	select {
	case got := <-resultCh:
		t.Fatalf("receive returned before the gap deadline: %+v", got)
	default:
	}
	clock.advance(time.Nanosecond)
	got := <-resultCh
	if got.err != nil || got.n != 1 || got.size != len("two") {
		t.Fatalf("receive at the gap deadline = %+v, want one successor", got)
	}
}
