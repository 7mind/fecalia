package bind

import (
	"crypto/rand"
	"fmt"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/telemetry"
)

// driveConcurrentPathUp is the concurrency-safe twin of driveEdgePathUp
// (everhadlive_test.go): it drives ONE path Up through the SAME production
// dispatch entry point (m.demuxInbound), but returns errors instead of calling
// t.Fatalf so it is safe to run on a non-test goroutine (calling FailNow off the
// test's own goroutine only stops that goroutine, not the test — see the
// testing.T docs). Callers run several of these concurrently, one per path, to
// exercise the D37 callback's at-most-once guarantee under `go test -race`.
func driveConcurrentPathUp(m *Multipath, idx int, psk config.Key, src netip.AddrPort) error {
	reflector := telemetry.NewReflector(psk, rand.Reader)
	pp := m.paths[idx]
	for i := 0; i < testProbeUpSucc; i++ {
		raw, err := pp.prober.SendProbe()
		if err != nil {
			return fmt.Errorf("path %q: SendProbe: %w", pp.name, err)
		}
		echo, err := reflector.Reflect(raw)
		if err != nil {
			return fmt.Errorf("path %q: reflect probe: %w", pp.name, err)
		}
		m.demuxInbound(pp, echo, src)
	}
	if got := pp.prober.State(); got != telemetry.StateUp {
		return fmt.Errorf("path %q not Up after %d demuxInbound echoes: %v", pp.name, testProbeUpSucc, got)
	}
	return nil
}

// TestOnFirstPathUpFiresExactlyOnceUnderConcurrentPaths is the T117 race
// acceptance: two paths reach StateUp on fresh echoes CONCURRENTLY (each driven
// from its own goroutine, mirroring the two real per-path receive goroutines
// dispatchInbound normally runs under), and the injected callback must fire
// EXACTLY ONCE regardless of which goroutine's CAS wins the false->true everUp
// edge. telemetry.SystemClock (real wall-clock, safe for concurrent Now())
// stands in for the fakeClock the sequential bind tests use, which is NOT
// goroutine-safe and would itself trip -race here.
func TestOnFirstPathUpFiresExactlyOnceUnderConcurrentPaths(t *testing.T) {
	psk := testKey(t, 0x37)
	m, _, _ := newProbingMultipath(t, loopbackPaths(2), psk, telemetry.SystemClock{})
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	var calls atomic.Int32
	fired := make(chan struct{})
	m.SetOnFirstPathUp(func() {
		if calls.Add(1) == 1 {
			close(fired)
		}
	})

	src := netip.MustParseAddrPort("127.0.0.1:9")
	const paths = 2
	errs := make(chan error, paths)
	var wg sync.WaitGroup
	for idx := 0; idx < paths; idx++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			errs <- driveConcurrentPathUp(m, idx, psk, src)
		}(idx)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("drive path up: %v", err)
		}
	}

	select {
	case <-fired:
	case <-time.After(2 * time.Second):
		t.Fatalf("onFirstPathUp callback never fired within 2s of both paths reaching StateUp")
	}

	// The callback fires on a dedicated goroutine (off the hot path); give a
	// buggy duplicate invocation time to land before asserting exclusivity.
	time.Sleep(50 * time.Millisecond)
	if got := calls.Load(); got != 1 {
		t.Fatalf("onFirstPathUp fired %d times across 2 concurrently-driven paths, want exactly 1", got)
	}
	if !m.EverHadLivePath() {
		t.Fatalf("EverHadLivePath = false after both paths reached StateUp")
	}
}

// TestOnFirstPathUpNilSafeWithoutCallback covers the "no callback set" half of
// the T117 acceptance: bringing a path Up without ever calling SetOnFirstPathUp
// must not panic (dispatchInbound's onFirstPathUp.Load() is nil-checked before
// the call).
func TestOnFirstPathUpNilSafeWithoutCallback(t *testing.T) {
	psk := testKey(t, 0x38)
	clk := newFakeClock()
	m, _, _ := newProbingMultipath(t, loopbackPaths(1), psk, clk)
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	src := netip.MustParseAddrPort("127.0.0.1:9")
	// No SetOnFirstPathUp call: onFirstPathUp stays at its zero value (nil).
	driveEdgePathUp(t, m, 0, psk, src, clk)

	if !m.EverHadLivePath() {
		t.Fatalf("EverHadLivePath = false after the path reached StateUp")
	}
}

// TestOnFirstPathUpNoFireWhileAllPathsDown covers the "no fire while all paths
// stay Down" half of the T117 acceptance: a bind that never receives any echo
// never reaches the false->true everUp edge, so the callback must never run.
func TestOnFirstPathUpNoFireWhileAllPathsDown(t *testing.T) {
	psk := testKey(t, 0x39)
	clk := newFakeClock()
	m, _, _ := newProbingMultipath(t, loopbackPaths(2), psk, clk)
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	var calls atomic.Int32
	m.SetOnFirstPathUp(func() { calls.Add(1) })

	// Tick liveness a few times with no echoes ever having been observed: every
	// path stays Down, so the false->true edge never occurs.
	for i := 0; i < 3; i++ {
		clk.advance(testProbeInterval)
		for _, pp := range m.paths {
			pp.prober.Tick()
		}
	}
	for _, pp := range m.paths {
		if pp.prober.State() != telemetry.StateDown {
			t.Fatalf("path %q unexpectedly Up with no echoes ever observed", pp.name)
		}
	}

	if got := calls.Load(); got != 0 {
		t.Fatalf("onFirstPathUp fired %d times while every path stayed Down, want 0", got)
	}
	if m.EverHadLivePath() {
		t.Fatalf("EverHadLivePath = true with every path Down")
	}
}

// TestOnFirstPathUpNoRefireAcrossDownUpDownUpCycle covers the "no re-fire across
// a Down->Up->Down->Up cycle" half of the T117 acceptance: everUp is sticky (I4)
// once it latches true, so a path that goes Up, then Down, then Up again must
// fire the callback on the FIRST transition only.
func TestOnFirstPathUpNoRefireAcrossDownUpDownUpCycle(t *testing.T) {
	psk := testKey(t, 0x3A)
	clk := newFakeClock()
	m, _, _ := newProbingMultipath(t, loopbackPaths(1), psk, clk)
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	var calls atomic.Int32
	fired := make(chan struct{}, 1)
	m.SetOnFirstPathUp(func() {
		calls.Add(1)
		fired <- struct{}{}
	})

	src := netip.MustParseAddrPort("127.0.0.1:9")

	// First Up: the false->true edge, must fire once.
	driveEdgePathUp(t, m, 0, psk, src, clk)
	select {
	case <-fired:
	case <-time.After(2 * time.Second):
		t.Fatalf("onFirstPathUp did not fire on the first Down->Up transition")
	}

	// Silence the path past DownAfter and Tick it back Down.
	clk.advance(testProbeDownAfter + testProbeInterval)
	m.paths[0].prober.Tick()
	if m.paths[0].prober.State() != telemetry.StateDown {
		t.Fatalf("path did not go Down after silence past DownAfter: %v", m.paths[0].prober.State())
	}

	// Bring it back Up a second time: everUp is already true, so this MUST NOT
	// re-fire the callback.
	driveEdgePathUp(t, m, 0, psk, src, clk)

	// Allow a buggy re-fire (asynchronous, off the hot path) time to land.
	time.Sleep(50 * time.Millisecond)
	if got := calls.Load(); got != 1 {
		t.Fatalf("onFirstPathUp fired %d times across a Down->Up->Down->Up cycle, want exactly 1", got)
	}
}
