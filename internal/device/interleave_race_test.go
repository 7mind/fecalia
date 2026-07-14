package device

import (
	"context"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/telemetry"
)

// T75 (Q34/Q36): the consolidated cross-controller concurrency proof for the two controllers that
// co-own hubFailover's mutable endpoint set — the re-resolution controller (T73, resolution.go)
// and the hub-failover controller itself (T57/failover.go). Q34 established that they coordinate
// PURELY through h.mu and the updateResolution/check API, never a second, competing repoint path;
// this file drives BOTH concurrently under `go test -race`, matching the actual production
// concurrency model (startFailoverAndResolution starts exactly two goroutines: the failover loop
// ticking check(), and the resolution loop ticking step()), and proves three contested schedules:
//
//  1. TestInterleaveReResolveWhileCheckMidAdvance — a re-resolve (updateResolution) landing while
//     check() is mid-advance (still holding h.mu, past its own SetPeerRemote call).
//  2. TestInterleaveFailoverAdvanceBetweenLookupAndApply — a failover advance (check()) landing in
//     the window between a re-resolve's DNS lookup completing (unlocked) and its updateResolution
//     apply.
//  3. TestInterleaveSimultaneousLivenessLossExactlyOneWinner — both controllers observing the SAME
//     liveness-loss event under genuine goroutine-scheduler concurrency (no artificial gating):
//     exactly ONE SetPeerRemote must win, with no double-repoint, no lost update, and no deadlock
//     on h.mu.
//
// (1) and (2) force their schedule deterministically via blockingRemote/gatedResolver rather than
// hoping the scheduler stumbles into it — a flake-free repro of the exact contested window named
// in the acceptance criterion. (3) lets the real scheduler interleave, repeated across many
// iterations, and relies on -race to catch any unsynchronized access.
//
// This file also consolidates the Q36 seam-contract unit coverage in one place:
//   - TestBootResolveHostnameDefersOnLookupFailure (below): the bounded boot resolve DEFERS
//     (never hard-fails) on a lookup failure — the direct seam-level unit test; the same property
//     end to end through up() is TestUpTolerantBootEndpointless (lifecycle_test.go).
//   - the loop repoints via SetPeerRemote only on a CHANGED active IP: TestResolutionChangedActiveIPRepointsOnce
//     (resolution_test.go).
//   - an UNCHANGED IP suppresses the repoint (no Rebaseline): TestResolutionUnchangedIPNoRepoint
//     (resolution_test.go).

// runWithTimeout runs fn in a goroutine and fails the test if it does not complete within d —
// wall-clock, independent of the fake telemetry.Clock the controllers use internally — the
// bounded-test-time proof that no contested schedule between the two controllers can deadlock on
// h.mu.
func runWithTimeout(t *testing.T, d time.Duration, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("test body did not complete within %v — suspected deadlock", d)
	}
}

// blockingRemote wraps recordingRemote and, on its first SetPeerRemote call, signals mid (closed
// once) and then blocks until the test closes release — used to hold hubFailover.check() deep
// inside its critical section (h.mu still held, past the point its active-identity mutation has
// already applied) so a concurrent updateResolution call is forced to queue on h.mu. All calls to
// SetPeerRemote happen with h.mu held by the caller (check/updateResolution), so the embedded
// recordingRemote's unsynchronized fields are safe to read from the test goroutine once both
// controller goroutines have been joined (sync.WaitGroup establishes the happens-before).
type blockingRemote struct {
	rem *recordingRemote

	mid     chan struct{}
	release chan struct{}
	once    sync.Once
}

func newBlockingRemote() *blockingRemote {
	return &blockingRemote{rem: &recordingRemote{}, mid: make(chan struct{}), release: make(chan struct{})}
}

func (b *blockingRemote) SetPeerRemote(ap netip.AddrPort) {
	b.rem.SetPeerRemote(ap)
	b.once.Do(func() { close(b.mid) })
	<-b.release
}

// TestInterleaveReResolveWhileCheckMidAdvance is contested schedule 1: check() advances the active
// endpoint to the standby (mutating activeSpec/activeAddr/idx/lastSwitch) and then blocks INSIDE
// its critical section, still holding h.mu. A concurrent re-resolve apply (updateResolution) for
// the now-superseded hostname spec must queue on h.mu without deadlocking, then — once check()
// releases — observe the POST-advance state and correctly no-op the repoint (the expansion is
// still applied, not lost; only the bond-facing SetPeerRemote is suppressed).
func TestInterleaveReResolveWhileCheckMidAdvance(t *testing.T) {
	oldA := mustAP(t, "203.0.113.1:51820")
	standby := mustAP(t, "198.51.100.7:51820")
	newA := mustAP(t, "203.0.113.2:51820")

	specs := []failoverSpec{
		nameSpec("hub.example.com", 51820, oldA), // spec 0: active hostname
		litSpec(t, standby.String()),             // spec 1: literal standby
	}
	hp := []hubHealth{&fakeHealth{telemetry.StateDown}, &fakeHealth{telemetry.StateDown}}
	brem := newBlockingRemote()
	clk := &fakeClock{now: time.Unix(1000, 0)}
	fo := newHubFailoverFromSpecs(specs, hp, brem, func() {}, discardInstall, clk, testSettle, discardLogger(t))
	clk.advance(testSettle + time.Second) // settle elapsed: check() is immediately eligible

	runWithTimeout(t, 5*time.Second, func() {
		var wg sync.WaitGroup
		wg.Add(2)

		go func() { // check() advances then blocks mid-critical-section.
			defer wg.Done()
			fo.check()
		}()

		go func() { // the re-resolve's apply — queues on h.mu until check() releases it.
			defer wg.Done()
			<-brem.mid
			fo.updateResolution(0, []netip.AddrPort{newA})
		}()

		time.Sleep(50 * time.Millisecond) // let the re-resolve goroutine queue on h.mu
		close(brem.release)
		wg.Wait()
	})

	if brem.rem.calls != 1 || brem.rem.last != standby {
		t.Fatalf("check() mid-advance interleave: SetPeerRemote calls=%d last=%v, want exactly (1, %v) — check() must win", brem.rem.calls, brem.rem.last, standby)
	}
	if fo.activeSpec != 1 || fo.activeAddr != standby {
		t.Fatalf("active identity after the interleave = (%d,%v), want (1,%v)", fo.activeSpec, fo.activeAddr, standby)
	}
	if got := fo.specs[0].addrs; len(got) != 1 || got[0] != newA {
		t.Fatalf("the queued re-resolve's expansion was lost: spec0.addrs=%v, want [%v] applied (not dropped — just correctly suppressed since spec0 is no longer active)", got, newA)
	}
}

// gatedResolver wraps scriptedResolver and, right before Lookup returns, signals gate (closed
// once) and then blocks until the test closes release — so a test can force a concurrent state
// change (a failover advance) to land in the window between the lookup completing and its caller
// (resolveTarget) applying the result via fo.updateResolution — contested schedule 2.
type gatedResolver struct {
	*scriptedResolver
	gate    chan struct{}
	release chan struct{}
	once    sync.Once
}

func newGatedResolver() *gatedResolver {
	return &gatedResolver{scriptedResolver: newScriptedResolver(), gate: make(chan struct{}), release: make(chan struct{})}
}

func (g *gatedResolver) Lookup(ctx context.Context, host string) ([]netip.Addr, time.Duration, bool, error) {
	addrs, ttl, ttlOk, err := g.scriptedResolver.Lookup(ctx, host)
	g.once.Do(func() { close(g.gate) })
	<-g.release
	return addrs, ttl, ttlOk, err
}

// TestInterleaveFailoverAdvanceBetweenLookupAndApply is contested schedule 2: the re-resolve's DNS
// lookup completes (unlocked — the network call never holds h.mu), then, in the window BEFORE the
// caller applies it via fo.updateResolution, a concurrent hub-loss check() fully advances the
// active endpoint to the standby. The queued apply must then observe the post-advance state and
// no-op the repoint (spec-scoped identity, not a stale flattened index) rather than double-repoint
// or corrupt the active pointer.
func TestInterleaveFailoverAdvanceBetweenLookupAndApply(t *testing.T) {
	host := "hub.example.com"
	oldA := mustAP(t, "203.0.113.1:51820")
	standby := mustAP(t, "198.51.100.7:51820")
	newA := mustAP(t, "203.0.113.2:51820")

	specs := []failoverSpec{
		nameSpec(host, 51820, oldA), // spec 0: active hostname
		litSpec(t, standby.String()),
	}
	hp := []hubHealth{&fakeHealth{telemetry.StateDown}, &fakeHealth{telemetry.StateDown}}
	rem := &recordingRemote{}
	clk := &fakeClock{now: time.Unix(1000, 0)}
	fo := newHubFailoverFromSpecs(specs, hp, rem, func() {}, discardInstall, clk, testSettle, discardLogger(t))
	clk.advance(testSettle + time.Second)

	rslv := newGatedResolver()
	rslv.set(host, newA.Addr())
	res := newResolution(rslv, fo, nameTargetsFromSpecs([]config.EndpointSpec{
		{Host: host, Port: 51820, IsName: true},
		{Addr: standby},
	}), pathFamilies{v4: true, v6: true}, clk, testPollInterval, testDNSTimeout, discardLogger(t))

	runWithTimeout(t, 5*time.Second, func() {
		var wg sync.WaitGroup
		wg.Add(2)

		go func() { // the re-resolve: its lookup completes, then pauses before applying.
			defer wg.Done()
			res.step()
		}()

		go func() { // once the lookup has completed (but not yet applied), the failover
			// controller independently observes hub loss and advances — landing squarely in the
			// gap between the lookup and its apply.
			defer wg.Done()
			<-rslv.gate
			fo.check()
		}()

		time.Sleep(50 * time.Millisecond) // let check() fully run before releasing the lookup
		close(rslv.release)
		wg.Wait()
	})

	if rem.calls != 1 || rem.last != standby {
		t.Fatalf("advance-between-lookup-and-apply: SetPeerRemote calls=%d last=%v, want exactly (1, %v) — check() must win", rem.calls, rem.last, standby)
	}
	if fo.activeSpec != 1 || fo.activeAddr != standby {
		t.Fatalf("active identity = (%d,%v), want (1,%v)", fo.activeSpec, fo.activeAddr, standby)
	}
	if got := fo.specs[0].addrs; len(got) != 1 || got[0] != newA {
		t.Fatalf("the queued re-resolve's expansion was lost: spec0.addrs=%v, want [%v] (applied, not dropped — just no repoint since spec0 is no longer active)", got, newA)
	}
}

// TestInterleaveSimultaneousLivenessLossExactlyOneWinner is contested schedule 3, the core
// acceptance assertion: both controllers observe the SAME liveness-loss event and race, under
// genuine goroutine-scheduler concurrency (no artificial gating — two goroutines hammer res.step()
// and fo.check() concurrently, matching production's exact two-goroutine model), for exactly one
// SetPeerRemote — no double-repoint, no lost update, no deadlock on h.mu. The h.mu-serialized
// settle-dwell reset (whichever action lands first resets lastSwitch/moves activeSpec, making the
// other action's own precondition false by the time it acquires the lock) makes "exactly one
// winner" a structural, scheduling-order-independent guarantee — repeated across many iterations
// (with a fresh harness each time) for -race coverage, not because the outcome is expected to
// vary.
func TestInterleaveSimultaneousLivenessLossExactlyOneWinner(t *testing.T) {
	const iterations = 50
	const stepsPerGoroutine = 20

	host := "hub.example.com"
	oldA := mustAP(t, "203.0.113.1:51820")
	standby := mustAP(t, "198.51.100.7:51820")
	newAddr := mustAddr(t, "203.0.113.2")
	newAP := netip.AddrPortFrom(newAddr, 51820)

	for i := 0; i < iterations; i++ {
		specs := []failoverSpec{
			nameSpec(host, 51820, oldA),
			litSpec(t, standby.String()),
		}
		hp := []hubHealth{&fakeHealth{telemetry.StateDown}, &fakeHealth{telemetry.StateDown}}
		rem := &recordingRemote{}
		clk := &fakeClock{now: time.Unix(1000, 0)}
		fo := newHubFailoverFromSpecs(specs, hp, rem, func() {}, discardInstall, clk, testSettle, discardLogger(t))
		clk.advance(testSettle + time.Second) // settle already elapsed: check() is eligible from the first tick

		rslv := newScriptedResolver()
		rslv.set(host, newAddr)
		res := newResolution(rslv, fo, nameTargetsFromSpecs([]config.EndpointSpec{
			{Host: host, Port: 51820, IsName: true},
			{Addr: standby},
		}), pathFamilies{v4: true, v6: true}, clk, testPollInterval, testDNSTimeout, discardLogger(t))

		runWithTimeout(t, 5*time.Second, func() {
			start := make(chan struct{})
			var wg sync.WaitGroup
			wg.Add(2)
			go func() {
				defer wg.Done()
				<-start
				for j := 0; j < stepsPerGoroutine; j++ {
					res.step()
				}
			}()
			go func() {
				defer wg.Done()
				<-start
				for j := 0; j < stepsPerGoroutine; j++ {
					fo.check()
				}
			}()
			close(start)
			wg.Wait()
		})

		if rem.calls != 1 {
			t.Fatalf("iteration %d: simultaneous liveness-loss produced %d SetPeerRemote calls, want exactly 1 (no double-repoint, no lost update)", i, rem.calls)
		}
		// Exactly two valid outcomes: the re-resolution controller repointed the active hostname
		// spec onto the fresh IP, or the failover controller advanced to the standby.
		switch rem.last {
		case newAP:
			if fo.activeSpec != 0 || fo.activeAddr != newAP {
				t.Fatalf("iteration %d: re-resolution won but active identity = (%d,%v), want (0,%v)", i, fo.activeSpec, fo.activeAddr, newAP)
			}
		case standby:
			if fo.activeSpec != 1 || fo.activeAddr != standby {
				t.Fatalf("iteration %d: failover won but active identity = (%d,%v), want (1,%v)", i, fo.activeSpec, fo.activeAddr, standby)
			}
		default:
			t.Fatalf("iteration %d: SetPeerRemote target = %v, want either %v (re-resolve win) or %v (failover win)", i, rem.last, newAP, standby)
		}
	}
}

// TestBootResolveHostnameDefersOnLookupFailure is the direct seam-level unit test for the first
// Q36 acceptance item: a lookup failure at the bounded boot resolve must DEFER — return an EMPTY
// expansion so the peer boots endpoint-less and the re-resolution loop completes it later — and
// must NEVER hard-fail (no error is even plumbed out of bootResolveHostname's signature).
// TestUpTolerantBootEndpointless (lifecycle_test.go) proves the same property end to end through
// up(); this isolates the seam itself.
func TestBootResolveHostnameDefersOnLookupFailure(t *testing.T) {
	rslv := newScriptedResolver()
	rslv.fail(context.DeadlineExceeded)
	spec := config.EndpointSpec{Host: "hub.example.com", Port: 51820, IsName: true}

	got := bootResolveHostname(rslv, spec, pathFamilies{v4: true, v6: true}, testDNSTimeout, discardLogger(t))

	if got != nil {
		t.Fatalf("bootResolveHostname on a lookup failure = %v, want nil (defer, never hard-fail)", got)
	}
}
