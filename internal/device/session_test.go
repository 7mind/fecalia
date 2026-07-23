package device

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	awgdevice "github.com/amnezia-vpn/amneziawg-go/device"

	"github.com/7mind/wanbond/internal/log"
	"github.com/7mind/wanbond/internal/metrics"
)

// fakeEngine is an ipcGetter whose UAPI GET dump (or error) the test controls, standing in
// for the amneziawg engine so the session snapshot parsing runs without a live device.
type fakeEngine struct {
	dump string
	err  error
}

func (f fakeEngine) IpcGet() (string, error) { return f.dump, f.err }

// uapiPeer renders a minimal UAPI GET peer block carrying a last-handshake instant, mirroring
// the amneziawg device's IpcGetOperation output the monitor parses (sec + nsec lines). A zero
// instant renders the never-handshaked 0/0 pair.
func uapiPeer(handshake time.Time) string {
	var sec, nsec int64
	if !handshake.IsZero() {
		nano := handshake.UnixNano()
		sec = nano / int64(time.Second)
		nsec = nano % int64(time.Second)
	}
	return fmt.Sprintf("public_key=abcd\nlast_handshake_time_sec=%d\nlast_handshake_time_nsec=%d\ntx_bytes=1\nrx_bytes=1\n", sec, nsec)
}

// TestSessionMonitorEstablished asserts a fresh completed handshake resolves to
// Established=true with the correct age.
func TestSessionMonitorEstablished(t *testing.T) {
	now := time.Unix(10_000, 0)
	handshake := now.Add(-12 * time.Second)
	mon := newSessionMonitor(fakeEngine{dump: uapiPeer(handshake)}, &fakeClock{now: now})

	got := mon.SessionSnapshot()
	if !got.Established {
		t.Errorf("Established = false, want true (handshake 12s ago is within the %s window)", awgdevice.RejectAfterTime)
	}
	if got.LastHandshakeAge != 12*time.Second {
		t.Errorf("LastHandshakeAge = %s, want 12s", got.LastHandshakeAge)
	}
}

// TestSessionMonitorNeverHandshaked asserts a peer with the 0/0 last-handshake pair (never
// completed a handshake) resolves to the zero snapshot — the "still converging" reading.
func TestSessionMonitorNeverHandshaked(t *testing.T) {
	mon := newSessionMonitor(fakeEngine{dump: uapiPeer(time.Time{})}, &fakeClock{now: time.Unix(10_000, 0)})

	got := mon.SessionSnapshot()
	if got.Established {
		t.Errorf("Established = true, want false (no handshake has completed)")
	}
	if got.LastHandshakeAge != 0 {
		t.Errorf("LastHandshakeAge = %s, want 0", got.LastHandshakeAge)
	}
}

// TestSessionMonitorAgedOut asserts a completed-but-stale handshake (older than the validity
// window) resolves to Established=false — the "wedged" reading — while still reporting the age.
func TestSessionMonitorAgedOut(t *testing.T) {
	now := time.Unix(10_000, 0)
	handshake := now.Add(-(awgdevice.RejectAfterTime + time.Second))
	mon := newSessionMonitor(fakeEngine{dump: uapiPeer(handshake)}, &fakeClock{now: now})

	got := mon.SessionSnapshot()
	if got.Established {
		t.Errorf("Established = true, want false (handshake older than %s is wedged)", awgdevice.RejectAfterTime)
	}
	if got.LastHandshakeAge != awgdevice.RejectAfterTime+time.Second {
		t.Errorf("LastHandshakeAge = %s, want %s", got.LastHandshakeAge, awgdevice.RejectAfterTime+time.Second)
	}
}

// TestSessionMonitorMostRecentPeer asserts a multi-peer dump reports the MOST RECENT
// handshake across peers (the connection-level "at least one session live" signal).
func TestSessionMonitorMostRecentPeer(t *testing.T) {
	now := time.Unix(10_000, 0)
	stale := now.Add(-100 * time.Second)
	fresh := now.Add(-3 * time.Second)
	mon := newSessionMonitor(fakeEngine{dump: uapiPeer(stale) + uapiPeer(fresh)}, &fakeClock{now: now})

	got := mon.SessionSnapshot()
	if !got.Established {
		t.Errorf("Established = false, want true (the fresher peer is within the window)")
	}
	if got.LastHandshakeAge != 3*time.Second {
		t.Errorf("LastHandshakeAge = %s, want 3s (the most recent peer)", got.LastHandshakeAge)
	}
}

// TestSessionMonitorEngineError asserts an engine read error yields the zero snapshot rather
// than a spurious established verdict.
func TestSessionMonitorEngineError(t *testing.T) {
	mon := newSessionMonitor(fakeEngine{err: errors.New("device closed")}, &fakeClock{now: time.Unix(10_000, 0)})

	if got := mon.SessionSnapshot(); got.Established || got.LastHandshakeAge != 0 {
		t.Errorf("snapshot on engine error = %+v, want zero value", got)
	}
}

// TestPeerSessionMonitorTwoPeers is the T256/G28/M106 acceptance check: given a dump
// carrying two distinct peers, PeerSessionSnapshots() resolves EACH peer's OWN
// established verdict and handshake age from THAT peer's own last-handshake pair,
// independent of the other peer's — unlike sessionMonitor's connection-wide max.
func TestPeerSessionMonitorTwoPeers(t *testing.T) {
	now := time.Unix(10_000, 0)
	fresh := now.Add(-3 * time.Second)
	aged := now.Add(-(awgdevice.RejectAfterTime + time.Second))
	dump := uapiPeerKey("aaaa", fresh) + uapiPeerKey("bbbb", aged)
	mon := newPeerSessionMonitor(fakeEngine{dump: dump}, &fakeClock{now: now}, []monitoredPeer{
		{name: "edge1", publicKey: "aaaa"},
		{name: "edge2", publicKey: "bbbb"},
	})

	got := mon.PeerSessionSnapshots()
	if len(got) != 2 {
		t.Fatalf("PeerSessionSnapshots len = %d, want 2", len(got))
	}
	if got[0].Peer != "edge1" || !got[0].Established || got[0].LastHandshakeSeconds != 3 {
		t.Errorf("edge1 = %+v, want Peer=edge1 Established=true age=3s", got[0])
	}
	wantAge := (awgdevice.RejectAfterTime + time.Second).Seconds()
	if got[1].Peer != "edge2" || got[1].Established || got[1].LastHandshakeSeconds != wantAge {
		t.Errorf("edge2 = %+v, want Peer=edge2 Established=false age=%gs (aged out)", got[1], wantAge)
	}
}

// TestPeerSessionMonitorNeverHandshaked asserts a peer with the 0/0 never-handshaked
// pair resolves to the zero snapshot for THAT peer (Established=false, age=0), even
// while another peer in the same dump is established — proving the per-peer keying,
// not a global fallback.
func TestPeerSessionMonitorNeverHandshaked(t *testing.T) {
	now := time.Unix(10_000, 0)
	fresh := now.Add(-3 * time.Second)
	dump := uapiPeerKey("aaaa", fresh) + uapiPeerKey("bbbb", time.Time{})
	mon := newPeerSessionMonitor(fakeEngine{dump: dump}, &fakeClock{now: now}, []monitoredPeer{
		{name: "edge1", publicKey: "aaaa"},
		{name: "edge2", publicKey: "bbbb"},
	})

	got := mon.PeerSessionSnapshots()
	if len(got) != 2 {
		t.Fatalf("PeerSessionSnapshots len = %d, want 2", len(got))
	}
	if got[1].Peer != "edge2" || got[1].Established || got[1].LastHandshakeSeconds != 0 {
		t.Errorf("edge2 = %+v, want Peer=edge2 Established=false age=0 (never handshaked)", got[1])
	}
}

// TestPeerSessionMonitorEngineError asserts an engine read error yields the zero
// snapshot for EVERY monitored peer (by name), matching sessionMonitor's error handling.
func TestPeerSessionMonitorEngineError(t *testing.T) {
	mon := newPeerSessionMonitor(fakeEngine{err: errors.New("device closed")}, &fakeClock{now: time.Unix(10_000, 0)}, []monitoredPeer{
		{name: "edge1", publicKey: "aaaa"},
		{name: "edge2", publicKey: "bbbb"},
	})

	got := mon.PeerSessionSnapshots()
	if len(got) != 2 {
		t.Fatalf("PeerSessionSnapshots len = %d, want 2", len(got))
	}
	for _, p := range got {
		if p.Established || p.LastHandshakeSeconds != 0 {
			t.Errorf("peer %+v on engine error, want zero value", p)
		}
	}
}

// TestPeerSessionMonitorSinglePeerBackCompat asserts a single-peer monitored set (Peer
// "", the D58 primary-naming rule via allMonitoredPeers) still resolves that one peer's
// established verdict correctly — the back-compat shape PeerSessions() must preserve.
func TestPeerSessionMonitorSinglePeerBackCompat(t *testing.T) {
	now := time.Unix(10_000, 0)
	fresh := now.Add(-3 * time.Second)
	mon := newPeerSessionMonitor(fakeEngine{dump: uapiPeerKey("aaaa", fresh)}, &fakeClock{now: now}, []monitoredPeer{
		{name: "", publicKey: "aaaa"},
	})

	got := mon.PeerSessionSnapshots()
	if len(got) != 1 || got[0].Peer != "" {
		t.Fatalf("PeerSessionSnapshots = %+v, want one entry with Peer \"\"", got)
	}
	if !got[0].Established || got[0].LastHandshakeSeconds != 3 {
		t.Errorf("PeerSessionSnapshots[0] = %+v, want Established=true age=3s", got[0])
	}
}

// TestSessionEdge0to1 is the core acceptance check: the edge detector fires EXACTLY once on
// the false->true transition and NOT on steady-state true polls. The steady-state assertion
// is the mutation discriminator — an implementation that returned true on every established
// poll (e.g. `return established`) would log the 'session established' record repeatedly and
// fail here, so this test kills that mutant.
func TestSessionEdge0to1(t *testing.T) {
	var e sessionEdge

	// Still converging: repeated false polls never fire.
	if e.observe(false) {
		t.Fatal("edge fired on the initial false poll")
	}
	if e.observe(false) {
		t.Fatal("edge fired on a second false poll")
	}
	// The 0->1 rise fires exactly once.
	if !e.observe(true) {
		t.Fatal("edge did NOT fire on the 0->1 transition")
	}
	// Steady-state true polls must NOT re-fire (exactly-once-per-session). This is the
	// mutation discriminator.
	if e.observe(true) {
		t.Fatal("edge re-fired on a steady-state true poll (would duplicate the log)")
	}
	if e.observe(true) {
		t.Fatal("edge re-fired on a second steady-state true poll")
	}
}

// TestSessionEdgeReestablish asserts a lost-then-re-established session (1->0->1) fires again
// on the fresh rise — a NEW session warrants a new record.
func TestSessionEdgeReestablish(t *testing.T) {
	var e sessionEdge
	if !e.observe(true) {
		t.Fatal("edge did not fire on the first 0->1 rise")
	}
	if e.observe(false) {
		t.Fatal("edge fired on the 1->0 fall (only 0->1 rises fire)")
	}
	if !e.observe(true) {
		t.Fatal("edge did not fire on the second (re-established) 0->1 rise")
	}
}

// TestStartSessionMonitorLogsOncePerSession drives the poll loop against a snapshotter that
// flips false->true->false->true and asserts the 'session established' record is emitted once
// per rise (twice total), exercising the loop end to end with the real ticker.
func TestStartSessionMonitorLogsOncePerSession(t *testing.T) {
	snap := &scriptedSession{seq: []bool{false, false, true, true, true, false, true, true}}
	buf := &syncBuffer{}
	lg, err := log.New("info", buf)
	if err != nil {
		t.Fatalf("log.New: %v", err)
	}
	stop := startSessionMonitor(snap, time.Millisecond, lg)

	// Wait until the scripted sequence has been fully consumed (or a generous deadline).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && snap.remaining() > 0 {
		time.Sleep(2 * time.Millisecond)
	}
	stop()
	stop() // idempotent

	if got := strings.Count(buf.String(), "session established"); got != 2 {
		t.Errorf("'session established' logged %d times, want 2 (one per 0->1 rise)\n%s", got, buf.String())
	}
}

// scriptedSession is a sessionSnapshotter that returns a scripted sequence of established
// verdicts, one per SessionSnapshot call, then repeats the LAST verdict once exhausted (so
// no spurious edge fires after the script ends). It is concurrency-safe: the poll-loop
// goroutine reads while the test polls remaining().
type scriptedSession struct {
	mu  sync.Mutex
	seq []bool
	i   int
}

func (s *scriptedSession) SessionSnapshot() metrics.SessionSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	var established bool
	switch {
	case s.i < len(s.seq):
		established = s.seq[s.i]
		s.i++
	case len(s.seq) > 0:
		established = s.seq[len(s.seq)-1]
	}
	return metrics.SessionSnapshot{Established: established}
}

func (s *scriptedSession) remaining() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.seq) - s.i
}

// syncBuffer is a concurrency-safe bytes.Buffer so the poll-loop goroutine's log writes do
// not race the test's read.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
