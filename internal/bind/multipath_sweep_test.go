package bind

import (
	"crypto/rand"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/frame"
	"github.com/7mind/wanbond/internal/sched"
	"github.com/7mind/wanbond/internal/telemetry"
)

// These tests give the receive-path liveness sweep (tickLivenessFromReceive,
// T39/D15) the -race unit coverage the e2e cannot: the e2e runs WITHOUT -race,
// and every OTHER bind test leaves sweepIntervalNanos at 0 (only StartProbeLoop
// arms it), so the sweep is a no-op there. Each test ARMS the sweep by storing
// sweepIntervalNanos directly (the test is package bind), which is exactly what
// StartProbeLoop does, minus the wall-clock ticker goroutine — so the sweep runs
// deterministically off injected time with no background Tick to perturb counts.

// countingClock is a fixed-time telemetry.Clock that COUNTS Now() calls. It backs BOTH
// the probers AND the scheduler in newProbingMultipath, so one sweep consumes exactly
// len(probers)+1 Now() reads: one per Prober.Tick (Liveness.Tick's first line) plus one
// for the eager-failover nudge's single scheduler recompute (ActiveBackup.Pick →
// recomputeLocked reads the clock once; D18). Nothing else reads it in the measured
// window, so the Now()-call delta is that fixed per-sweep quantum times the number of
// sweeps — the observable the throttle assertion needs. The time is immutable after
// construction (the throttle is driven by the receive timestamp passed to
// tickLivenessFromReceive, not by this clock), so concurrent sweep goroutines read it
// race-free; only the atomic counter is written concurrently.
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
// would Tick both probers AND nudge the scheduler, so the Now()-call delta would be
// (len(probers)+1)*N, not one per-sweep quantum. A second batch one interval later
// performs exactly one MORE sweep, proving the throttle releases rather than latching.
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

	// One sweep = one Tick per prober + one scheduler-nudge recompute (D18).
	perSweep := int64(len(probers)) + 1
	if got := runBatch(base); got != perSweep {
		t.Fatalf("first interval: %d Now() calls across %d racers, want exactly %d (one sweep = %d Ticks + 1 nudge; throttle coalesces the burst)",
			got, racers, perSweep, len(probers))
	}
	if got := runBatch(base.Add(testProbeInterval)); got != perSweep {
		t.Fatalf("next interval: %d Now() calls, want exactly %d (one fresh sweep — throttle released)",
			got, perSweep)
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

// TestSweepDrivesEagerFailover asserts the D18 fix: a receive-path tick does not
// just mark the dead ACTIVE path DOWN — it EAGERLY fails egress over to the healthy
// backup, WITHOUT any application Send driving the scheduler's Pick. This closes the
// repeated-flap wedge, where the active path dies during an egress lull (a kill
// landing on the just-restored primary) so no Send ever calls Pick and a purely
// Send-driven failover never switches off the dead path.
//
// The switch is isolated to the TICK — not to a later external Pick — by exploiting
// the scheduler's failback hysteresis. newProbingMultipath sets FailbackAfter to an
// hour, so ONCE egress is on the backup the scheduler stays there even after the
// primary recovers. The test re-ups the primary AFTER the failover tick and then
// asserts Pick()==backup: that can hold only if the tick had already moved egress to
// the backup (a fresh recompute with both paths up would otherwise pick the primary).
// Non-vacuous: no Send is ever issued and no Pick is called between arming and the
// tick, so nothing but the tick's nudge can perform the failover.
func TestSweepDrivesEagerFailover(t *testing.T) {
	const (
		primaryIdx = 0
		backupIdx  = 1
	)
	psk := testKey(t, 0x34)
	clk := newFakeClock()
	m, probers, scheduler := newProbingMultipath(t, loopbackPaths(2), psk, clk)
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	codec, err := frame.NewCodec(psk)
	if err != nil {
		t.Fatalf("new codec: %v", err)
	}
	reflector := telemetry.NewReflector(psk, rand.Reader)
	peers := make([]*net.UDPConn, 2)
	peerAPs := make([]netip.AddrPort, 2)
	for i := 0; i < 2; i++ {
		peers[i], peerAPs[i] = rawPeer(t)
		m.paths[i].setRemote(peerAPs[i])
	}
	// pump injects one authenticated probe/echo exchange for path i through
	// handleInbound (NOT the socket, so the parked readers never participate and never
	// tick), advancing the fake clock by one probe interval. It is a heartbeat: it
	// updates path i's liveness via RecordEcho without itself running a sweep tick.
	pump := func(i int) {
		raw, err := probers[i].SendProbe()
		if err != nil {
			t.Fatalf("path %d SendProbe: %v", i, err)
		}
		if _, err := m.paths[i].conn.WriteToUDPAddrPort(raw, peerAPs[i]); err != nil {
			t.Fatalf("path %d emit probe: %v", i, err)
		}
		probe := readProbe(t, peers[i], codec)
		reraw, err := frame.Encode(psk, probe)
		if err != nil {
			t.Fatalf("path %d re-encode: %v", i, err)
		}
		echo, err := reflector.Reflect(reraw)
		if err != nil {
			t.Fatalf("path %d reflect: %v", i, err)
		}
		clk.advance(testProbeRTT)
		m.handleInbound(m.paths[i], echo, peerAPs[i])
		clk.advance(testProbeInterval - testProbeRTT)
	}

	// Bring BOTH paths up, then let the scheduler cold-start onto the primary.
	for i := 0; i < 2; i++ {
		for j := 0; j < testProbeUpSucc; j++ {
			pump(i)
		}
		if probers[i].State() != telemetry.StateUp {
			t.Fatalf("path %d after healthy exchange = %v, want up", i, probers[i].State())
		}
	}
	if got := scheduler.Pick(sched.ClassData); got != primaryIdx {
		t.Fatalf("both paths up: active=%d, want the primary %d", got, primaryIdx)
	}

	// Arm the sweep (no probe-loop timer). Silence the PRIMARY past DownAfter while
	// keeping the BACKUP fresh with a single heartbeat, so a tick marks only the
	// primary down. Crucially, NO Send is issued and NO Pick is called from here until
	// the tick — so the scheduler still believes the primary is active.
	m.sweepIntervalNanos.Store(int64(testProbeInterval))
	clk.advance(testProbeDownAfter + time.Millisecond)
	pump(backupIdx)

	// A single receive-driven tick (timestamp far past the 0 high-water clears the
	// throttle): it marks the primary DOWN and — the behaviour under test — nudges the
	// scheduler to fail egress over to the backup, with no Send in sight.
	m.tickLivenessFromReceive(time.Unix(2_000_000_000, 0))
	if probers[primaryIdx].State() != telemetry.StateDown {
		t.Fatalf("primary after tick = %v, want down", probers[primaryIdx].State())
	}
	if probers[backupIdx].State() != telemetry.StateUp {
		t.Fatalf("backup after tick = %v, want still up", probers[backupIdx].State())
	}

	// Re-up the primary. A fresh recompute with both paths up would pick the primary,
	// EXCEPT that the hour-long failback dwell pins egress to the backup once it is
	// active. So Pick()==backup proves the tick already failed over; ==primary would
	// prove the tick did NOT nudge the scheduler (the wedge).
	for j := 0; j < testProbeUpSucc; j++ {
		pump(primaryIdx)
	}
	if got := scheduler.Pick(sched.ClassData); got != backupIdx {
		t.Fatalf("after receive tick: active=%d, want backup %d — the tick did not eagerly fail over (D18 wedge)", got, backupIdx)
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
