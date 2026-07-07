package bind

import (
	"crypto/rand"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/frame"
	"github.com/7mind/wanbond/internal/telemetry"
)

// These tests give the receive-path liveness sweep (tickLivenessFromReceive,
// T39/D15) the -race unit coverage the e2e cannot: the e2e runs WITHOUT -race,
// and every OTHER bind test leaves sweepIntervalNanos at 0 (only StartProbeLoop
// arms it), so the sweep is a no-op there. Each test ARMS the sweep by storing
// sweepIntervalNanos directly (the test is package bind), which is exactly what
// StartProbeLoop does, minus the wall-clock ticker goroutine — so the sweep runs
// deterministically off injected time with no background Tick to perturb counts.

// countingClock is a fixed-time telemetry.Clock that COUNTS Now() calls. Prober.Tick
// reads the clock exactly once per call (Liveness.Tick's first line), and nothing
// else calls Now() during the measured window, so the Now()-call delta is the number
// of Prober.Tick invocations — the observable the throttle assertion needs. The time
// is immutable after construction (the throttle is driven by the receive timestamp
// passed to tickLivenessFromReceive, not by this clock), so concurrent sweep
// goroutines read it race-free; only the atomic counter is written concurrently.
type countingClock struct {
	now   time.Time
	calls atomic.Int64
}

func newCountingClock() *countingClock {
	return &countingClock{now: time.Unix(1_700_000_000, 0)}
}

func (c *countingClock) Now() time.Time {
	c.calls.Add(1)
	return c.now
}

// TestSweepThrottleAtMostOncePerInterval asserts assertion (a): across CONCURRENT
// tickLivenessFromReceive calls carrying the same receive timestamp, AT MOST ONE
// sweep runs per probe interval — the atomic CAS on lastSweepNanos coalesces the
// burst. Non-vacuous: without the CAS throttle every one of the N racing callers
// would Tick both probers, so the Now()-call delta would be 2*N, not 2. A second
// batch one interval later performs exactly one MORE sweep, proving the throttle
// releases rather than latching.
func TestSweepThrottleAtMostOncePerInterval(t *testing.T) {
	psk := testKey(t, 0x31)
	clk := newCountingClock()
	m, probers, _ := newProbingMultipath(t, loopbackPaths(2), psk, clk)
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	// Arm the sweep at the probe cadence WITHOUT starting the wall-clock ticker, so
	// the only Ticks are the ones our concurrent callers drive. No traffic reaches
	// the loopback sockets, so the Bind-owned readers stay parked and never Tick.
	m.sweepIntervalNanos.Store(int64(testProbeInterval))

	const racers = 32
	// A receive timestamp far past 0 (the initial lastSweepNanos high-water) so the
	// first batch clears the throttle; the second batch is exactly one interval later.
	base := time.Unix(2_000_000_000, 0)

	runBatch := func(now time.Time) int64 {
		before := clk.calls.Load()
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := 0; i < racers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				m.tickLivenessFromReceive(now)
			}()
		}
		close(start)
		wg.Wait()
		return clk.calls.Load() - before
	}

	if got := runBatch(base); got != int64(len(probers)) {
		t.Fatalf("first interval: %d Prober.Tick calls across %d racers, want exactly %d (one sweep, throttle coalesces the burst)",
			got, racers, len(probers))
	}
	if got := runBatch(base.Add(testProbeInterval)); got != int64(len(probers)) {
		t.Fatalf("next interval: %d Prober.Tick calls, want exactly %d (one fresh sweep — throttle released)",
			got, len(probers))
	}
}

// TestSweepDetectsDownWithStarvedTimer asserts assertion (b): with NO probe-loop
// timer running (StartProbeLoop is never called), a single receive-driven tick
// ALONE marks a silent UP path DOWN once its silence exceeds DownAfter. This is
// the D15 property: under CPU saturation the timer goroutine can be starved, but
// the receive goroutines — scheduled by the very traffic that must trigger
// failover — keep liveness advancing. Non-vacuous: the pre-tick assertion proves
// nothing else moves the path; only tickLivenessFromReceive drives the transition.
func TestSweepDetectsDownWithStarvedTimer(t *testing.T) {
	psk := testKey(t, 0x32)
	clk := newFakeClock()
	m, probers, _ := newProbingMultipath(t, loopbackPaths(2), psk, clk)
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	// Bring path 0 UP via a real authenticated probe/echo exchange, injected through
	// handleInbound (not the socket), so the parked readers do not participate.
	peer, peerAP := rawPeer(t)
	m.paths[0].setRemote(peerAP)
	reflector := telemetry.NewReflector(psk, rand.Reader)
	codec, err := frame.NewCodec(psk)
	if err != nil {
		t.Fatalf("new codec: %v", err)
	}
	for i := 0; i < testProbeUpSucc; i++ {
		raw, err := probers[0].SendProbe()
		if err != nil {
			t.Fatalf("SendProbe: %v", err)
		}
		if _, err := m.paths[0].conn.WriteToUDPAddrPort(raw, peerAP); err != nil {
			t.Fatalf("emit probe: %v", err)
		}
		probe := readProbe(t, peer, codec)
		reraw, err := frame.Encode(psk, probe)
		if err != nil {
			t.Fatalf("re-encode probe: %v", err)
		}
		echo, err := reflector.Reflect(reraw)
		if err != nil {
			t.Fatalf("reflect: %v", err)
		}
		clk.advance(testProbeRTT)
		m.handleInbound(m.paths[0], echo, peerAP)
		clk.advance(testProbeInterval - testProbeRTT)
	}
	if probers[0].State() != telemetry.StateUp {
		t.Fatalf("path 0 after healthy exchange = %v, want up", probers[0].State())
	}

	// Arm the sweep but start NO probe loop: the timer is "starved" (never ticks).
	m.sweepIntervalNanos.Store(int64(testProbeInterval))

	// Silence the path past DownAfter. Nothing has Ticked, so it is still UP — the
	// pre-tick assertion pins that only the receive tick can move it.
	clk.advance(testProbeDownAfter + time.Millisecond)
	if probers[0].State() != telemetry.StateUp {
		t.Fatalf("path 0 before receive tick = %v, want still up (no timer, no other Tick)", probers[0].State())
	}

	// A single receive-driven sweep (timestamp far past the 0 high-water clears the
	// throttle) marks the silent path DOWN — with no probe-loop timer involved.
	m.tickLivenessFromReceive(time.Unix(2_000_000_000, 0))
	if probers[0].State() != telemetry.StateDown {
		t.Fatalf("path 0 after receive tick = %v, want down (receive path detected the silence)", probers[0].State())
	}
}

// TestSweepNoDeadlockAgainstClose asserts assertion (c): the sweep's TryLock (NOT
// Lock) against m.mu is what lets Close — which holds m.mu WHILE it waits on the
// reader WaitGroup — complete while readers are hammering the sweep. Arming the
// interval to 1ns makes EVERY received datagram clear the throttle and reach
// m.mu.TryLock, so the Bind-owned readers contend m.mu continuously under load.
// With Lock instead of TryLock a reader would block on m.mu while Close holds it
// and waits for that same reader to exit — a deadlock that hangs Close forever.
// The bounded-timeout guard turns that hang into a failure; with TryLock, Close
// returns promptly and the test is -race-clean.
func TestSweepNoDeadlockAgainstClose(t *testing.T) {
	psk := testKey(t, 0x33)
	// SystemClock: this test asserts shutdown liveness, not a timing verdict, and a
	// real concurrency-safe clock avoids serializing the racing readers on a fake.
	m, _, _ := newProbingMultipath(t, loopbackPaths(2), psk, telemetry.SystemClock{})
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Capture the reader sockets' addresses BEFORE any concurrency (Close nils
	// m.paths under m.mu; reading it from a sender goroutine would race).
	dsts := make([]*net.UDPAddr, len(m.paths))
	for i := range m.paths {
		dsts[i] = m.paths[i].conn.LocalAddr().(*net.UDPAddr)
	}

	// 1ns interval: every datagram clears the receive-sweep throttle and reaches
	// m.mu.TryLock, maximising contention with Close's m.mu hold.
	m.sweepIntervalNanos.Store(1)

	stop := make(chan struct{})
	var senders sync.WaitGroup
	for _, dst := range dsts {
		senders.Add(1)
		go func(dst *net.UDPAddr) {
			defer senders.Done()
			cl, err := net.DialUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")}, dst)
			if err != nil {
				return
			}
			defer cl.Close()
			// Undecodable payload: handleInbound drops it, then readLoop still runs the
			// sweep — exactly the reader / m.mu contention this test needs.
			junk := []byte("keep-the-readers-cycling")
			for {
				select {
				case <-stop:
					return
				default:
					_, _ = cl.Write(junk)
				}
			}
		}(dst)
	}

	// Let the readers warm up so at least one is reliably inside the sweep when Close
	// grabs m.mu.
	time.Sleep(50 * time.Millisecond)

	done := make(chan error, 1)
	go func() { done <- m.Close() }()
	select {
	case <-done:
		// Close returned: no reader deadlocked on m.mu.
	case <-time.After(5 * time.Second):
		close(stop)
		senders.Wait()
		t.Fatal("Close hung: a reader deadlocked on m.mu — the sweep must TryLock, not Lock")
	}
	close(stop)
	senders.Wait()
}
