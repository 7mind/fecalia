package device

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	awgdevice "github.com/amnezia-vpn/amneziawg-go/device"

	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/log"
)

// uapiPeerKey renders a minimal UAPI GET peer block for a chosen public key (lowercase hex) and
// last-handshake instant — the per-peer shape perPeerHandshakeNano parses and the teardown
// monitor level-checks. A zero instant renders the never-handshaked 0/0 pair.
func uapiPeerKey(pubHex string, handshake time.Time) string {
	var sec, nsec int64
	if !handshake.IsZero() {
		nano := handshake.UnixNano()
		sec = nano / int64(time.Second)
		nsec = nano % int64(time.Second)
	}
	return fmt.Sprintf("public_key=%s\nlast_handshake_time_sec=%d\nlast_handshake_time_nsec=%d\ntx_bytes=1\nrx_bytes=1\n", pubHex, sec, nsec)
}

// errIpc is a stand-in engine read error.
var errIpc = fmt.Errorf("device closed")

// mutableEngine is an ipcGetter whose UAPI dump the test swaps between polls, so a single
// monitor can be driven across a dead -> live -> dead sequence.
type mutableEngine struct {
	mu   sync.Mutex
	dump string
}

func (e *mutableEngine) set(dump string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.dump = dump
}

func (e *mutableEngine) IpcGet() (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.dump, nil
}

// recordingTearer is a peerTearer that records every TearDownPeer(name) call in order so a test
// can assert WHICH peers the level check tore down and how many times (the idempotent call is
// repeated every poll while a peer stays dead). It reports true (state reclaimed) by default,
// modelling a bind that had heavy state to free; it is concurrency-safe for the loop test.
type recordingTearer struct {
	mu    sync.Mutex
	calls []string
}

func (r *recordingTearer) TearDownPeer(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, name)
	return true
}

func (r *recordingTearer) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.calls))
	copy(out, r.calls)
	return out
}

func (r *recordingTearer) countOf(name string) int {
	n := 0
	for _, c := range r.snapshot() {
		if c == name {
			n++
		}
	}
	return n
}

// discardInfo is a logger writing to a discarded buffer, for tests that only assert calls.
func discardInfo(t *testing.T) log.Logger {
	t.Helper()
	lg, err := log.New("info", &syncBuffer{})
	if err != nil {
		t.Fatalf("log.New: %v", err)
	}
	return lg
}

// TestPerPeerHandshakeNano asserts the per-peer parser keeps peers DISTINCT (unlike the global
// latestHandshakeNano flatten): each public_key block maps to its own handshake instant, a
// never-handshaked block maps to 0 (present but not established), and interface preamble lines
// are ignored.
func TestPerPeerHandshakeNano(t *testing.T) {
	fresh := time.Unix(10_000, 500)
	dump := "private_key=deadbeef\nlisten_port=51820\n" +
		uapiPeerKey("aaaa", fresh) +
		uapiPeerKey("bbbb", time.Time{})

	got := perPeerHandshakeNano(dump)
	if len(got) != 2 {
		t.Fatalf("parsed %d peers, want 2: %v", len(got), got)
	}
	if got["aaaa"] != fresh.UnixNano() {
		t.Errorf("peer aaaa handshake = %d, want %d", got["aaaa"], fresh.UnixNano())
	}
	if v, ok := got["bbbb"]; !ok || v != 0 {
		t.Errorf("never-handshaked peer bbbb = (%d, present=%v), want (0, present=true)", v, ok)
	}
}

// TestPeerTeardownAgedOut is acceptance (a): a non-primary peer whose handshake has aged past
// RejectAfterTime is torn down — TearDownPeer invoked with its configured name — and ONE INFO
// logs the transition.
func TestPeerTeardownAgedOut(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	aged := now.Add(-(awgdevice.RejectAfterTime + time.Second))
	eng := fakeEngine{dump: uapiPeerKey("bbbb", aged)}
	tearer := &recordingTearer{}
	buf := &syncBuffer{}
	lg, err := log.New("info", buf)
	if err != nil {
		t.Fatalf("log.New: %v", err)
	}
	mon := newPeerTeardownMonitor(eng, tearer, []monitoredPeer{{name: "peer-b", publicKey: "bbbb"}}, &fakeClock{now: now})

	mon.poll(lg)

	if got := tearer.snapshot(); len(got) != 1 || got[0] != "peer-b" {
		t.Fatalf("TearDownPeer calls = %v, want exactly [peer-b]", got)
	}
	if n := strings.Count(buf.String(), "concentrator peer session lost"); n != 1 {
		t.Errorf("teardown INFO logged %d times, want 1\n%s", n, buf.String())
	}
}

// TestPeerTeardownNeverHandshaked is acceptance (b) — the D50 core: a peer that instantiated
// heavy state via an authenticated PROBE but has last_handshake=0 (NO handshake ever, hence NO
// Established 1->0 edge) is STILL torn down by the LEVEL check, not skipped for lack of an edge.
func TestPeerTeardownNeverHandshaked(t *testing.T) {
	eng := fakeEngine{dump: uapiPeerKey("bbbb", time.Time{})}
	tearer := &recordingTearer{}
	mon := newPeerTeardownMonitor(eng, tearer, []monitoredPeer{{name: "peer-b", publicKey: "bbbb"}}, &fakeClock{now: time.Unix(1_000_000, 0)})

	mon.poll(discardInfo(t))

	if got := tearer.snapshot(); len(got) != 1 || got[0] != "peer-b" {
		t.Fatalf("never-handshaked peer: TearDownPeer calls = %v, want [peer-b] (the level check must fire without a 1->0 edge)", got)
	}
}

// TestPeerTeardownLivePeerUntouched is acceptance (c): a peer with a fresh handshake is
// established, so the level check leaves it alone — even across repeated polls (no teardown, no
// spurious log). The primary is never even a monitored peer.
func TestPeerTeardownLivePeerUntouched(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	fresh := now.Add(-3 * time.Second)
	eng := fakeEngine{dump: uapiPeerKey("bbbb", fresh)}
	tearer := &recordingTearer{}
	buf := &syncBuffer{}
	lg, err := log.New("info", buf)
	if err != nil {
		t.Fatalf("log.New: %v", err)
	}
	mon := newPeerTeardownMonitor(eng, tearer, []monitoredPeer{{name: "peer-b", publicKey: "bbbb"}}, &fakeClock{now: now})

	for i := 0; i < 3; i++ {
		mon.poll(lg)
	}

	if got := tearer.snapshot(); len(got) != 0 {
		t.Fatalf("live peer torn down: TearDownPeer calls = %v, want none", got)
	}
	if buf.String() != "" {
		t.Errorf("live peer produced a teardown log: %s", buf.String())
	}
}

// TestPeerTeardownRepeatedLevelCheckDedupesLog pins the "dedupe the LOG, not the call" rule: a
// persistently-dead peer is torn down on EVERY poll (the idempotent call repeats — that is what
// survives a daemon-reload loss of edge memory), but only ONE INFO logs the transition.
func TestPeerTeardownRepeatedLevelCheckDedupesLog(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	aged := now.Add(-(awgdevice.RejectAfterTime + time.Second))
	eng := fakeEngine{dump: uapiPeerKey("bbbb", aged)}
	tearer := &recordingTearer{}
	buf := &syncBuffer{}
	lg, err := log.New("info", buf)
	if err != nil {
		t.Fatalf("log.New: %v", err)
	}
	mon := newPeerTeardownMonitor(eng, tearer, []monitoredPeer{{name: "peer-b", publicKey: "bbbb"}}, &fakeClock{now: now})

	const polls = 4
	for i := 0; i < polls; i++ {
		mon.poll(lg)
	}

	if got := tearer.countOf("peer-b"); got != polls {
		t.Errorf("TearDownPeer(peer-b) called %d times, want %d (the level-triggered call must repeat every poll)", got, polls)
	}
	if n := strings.Count(buf.String(), "concentrator peer session lost"); n != 1 {
		t.Errorf("teardown INFO logged %d times, want 1 (the LOG is deduped, not the call)\n%s", n, buf.String())
	}
}

// TestPeerTeardownReestablishRelogs is the acceptance (d) analog at the monitor level: a peer
// torn down, then RE-ESTABLISHED (a fresh handshake — the same signal a re-bind PROBE + relayed
// traffic ultimately produces), stops being torn down; and a SUBSEQUENT loss tears it down and
// logs again (the dedupe resets on re-establishment).
func TestPeerTeardownReestablishRelogs(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	aged := now.Add(-(awgdevice.RejectAfterTime + time.Second))
	fresh := now.Add(-2 * time.Second)
	eng := &mutableEngine{}
	tearer := &recordingTearer{}
	buf := &syncBuffer{}
	lg, err := log.New("info", buf)
	if err != nil {
		t.Fatalf("log.New: %v", err)
	}
	mon := newPeerTeardownMonitor(eng, tearer, []monitoredPeer{{name: "peer-b", publicKey: "bbbb"}}, &fakeClock{now: now})

	// (1) dead -> torn down + logged once.
	eng.set(uapiPeerKey("bbbb", aged))
	mon.poll(lg)
	// (2) re-established -> not torn down; dedupe cleared.
	eng.set(uapiPeerKey("bbbb", fresh))
	mon.poll(lg)
	// (3) dead again -> torn down + logged AGAIN (a new loss warrants a new record).
	eng.set(uapiPeerKey("bbbb", aged))
	mon.poll(lg)

	if got := tearer.snapshot(); len(got) != 2 || got[0] != "peer-b" || got[1] != "peer-b" {
		t.Fatalf("TearDownPeer calls = %v, want [peer-b peer-b] (torn, kept while live, torn again)", got)
	}
	if n := strings.Count(buf.String(), "concentrator peer session lost"); n != 2 {
		t.Errorf("teardown INFO logged %d times, want 2 (one per loss; the dedupe resets on re-establishment)\n%s", n, buf.String())
	}
}

// TestPeerTeardownEngineErrorSkips asserts an engine read error skips the sweep entirely — no
// spurious teardown on a transiently unreadable engine.
func TestPeerTeardownEngineErrorSkips(t *testing.T) {
	eng := fakeEngine{err: errIpc}
	tearer := &recordingTearer{}
	mon := newPeerTeardownMonitor(eng, tearer, []monitoredPeer{{name: "peer-b", publicKey: "bbbb"}}, &fakeClock{now: time.Unix(1_000_000, 0)})

	mon.poll(discardInfo(t))

	if got := tearer.snapshot(); len(got) != 0 {
		t.Fatalf("engine error caused teardown: calls = %v, want none", got)
	}
}

// TestPeerTeardownOnlyMonitoredPeers asserts a dump peer NOT in the monitored set (e.g. the
// primary, which is excluded) is never torn down, and the monitored peers are checked
// independently: one aged (torn), one fresh (kept).
func TestPeerTeardownOnlyMonitoredPeers(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	aged := now.Add(-(awgdevice.RejectAfterTime + time.Second))
	fresh := now.Add(-1 * time.Second)
	dump := uapiPeerKey("aaaa", aged) + // primary key — present in dump but NOT monitored
		uapiPeerKey("bbbb", aged) + // monitored, dead -> torn
		uapiPeerKey("cccc", fresh) // monitored, live -> kept
	eng := fakeEngine{dump: dump}
	tearer := &recordingTearer{}
	mon := newPeerTeardownMonitor(eng, tearer, []monitoredPeer{
		{name: "peer-b", publicKey: "bbbb"},
		{name: "peer-c", publicKey: "cccc"},
	}, &fakeClock{now: now})

	mon.poll(discardInfo(t))

	if got := tearer.snapshot(); len(got) != 1 || got[0] != "peer-b" {
		t.Fatalf("TearDownPeer calls = %v, want exactly [peer-b] (primary excluded, live peer-c kept)", got)
	}
}

// TestConcentratorMonitoredPeers asserts the device-side peer set builder: a single-peer config
// yields NO monitored peers (the teardown loop stays inert, single-peer path unchanged), while a
// multi-peer config yields every NON-primary peer paired with its configured name and the
// lowercase-hex public key the UAPI dump identifies it by.
func TestConcentratorMonitoredPeers(t *testing.T) {
	t.Run("single-peer config monitors nothing", func(t *testing.T) {
		cfg := &config.Config{}
		cfg.WireGuard.Peers = []config.Peer{{PublicKey: testPubKey(t, 0x11), Name: "solo"}}
		if got := concentratorMonitoredPeers(cfg, cfg.PeerIdentities()); len(got) != 0 {
			t.Fatalf("single-peer config produced %v monitored peers, want none", got)
		}
	})

	t.Run("multi-peer concentrator config monitors every non-primary peer", func(t *testing.T) {
		k0 := testPubKey(t, 0x11)
		k1 := testPubKey(t, 0x22)
		k2 := testPubKey(t, 0x33)
		cfg := &config.Config{Role: config.RoleConcentrator}
		cfg.WireGuard.Peers = []config.Peer{
			{PublicKey: k0, Name: "primary"},
			{PublicKey: k1, Name: "peer-b"},
			{PublicKey: k2, Name: "peer-c"},
		}
		got := concentratorMonitoredPeers(cfg, cfg.PeerIdentities())
		b1, b2 := k1.Bytes(), k2.Bytes()
		want := []monitoredPeer{
			{name: "peer-b", publicKey: hex.EncodeToString(b1[:])},
			{name: "peer-c", publicKey: hex.EncodeToString(b2[:])},
		}
		if len(got) != len(want) {
			t.Fatalf("monitored peers = %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("monitored peer %d = %+v, want %+v", i, got[i], want[i])
			}
		}
	})

	// D50 guard (T251/Q68b): a MULTI-PEER EDGE config monitors NOTHING. The additional edge peers
	// are warm-standby concentrators — healthy by design even carrying no data — so the level-check
	// teardown must never engage on the edge, where it would tear a warm standby down the moment its
	// session momentarily aged. This is the exact shape that, before the role gate, wrongly returned
	// the non-primary set (identical to the concentrator subtest above but for the role).
	t.Run("multi-peer edge config monitors nothing", func(t *testing.T) {
		k0 := testPubKey(t, 0x11)
		k1 := testPubKey(t, 0x22)
		k2 := testPubKey(t, 0x33)
		cfg := &config.Config{Role: config.RoleEdge}
		cfg.WireGuard.Peers = []config.Peer{
			{PublicKey: k0, Name: "primary"},
			{PublicKey: k1, Name: "peer-b"},
			{PublicKey: k2, Name: "peer-c"},
		}
		if got := concentratorMonitoredPeers(cfg, cfg.PeerIdentities()); len(got) != 0 {
			t.Fatalf("multi-peer EDGE config produced %v monitored peers, want none (warm standbys must never be torn down)", got)
		}
	})
}

// TestStartPeerTeardownMonitorLoop drives the background loop end to end against a dead peer and
// asserts the ticker-driven poll tears it down; the empty-peer-set constructor is a no-op.
func TestStartPeerTeardownMonitorLoop(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	aged := now.Add(-(awgdevice.RejectAfterTime + time.Second))
	eng := fakeEngine{dump: uapiPeerKey("bbbb", aged)}
	tearer := &recordingTearer{}
	mon := newPeerTeardownMonitor(eng, tearer, []monitoredPeer{{name: "peer-b", publicKey: "bbbb"}}, &fakeClock{now: now})

	stop := startPeerTeardownMonitor(mon, time.Millisecond, discardInfo(t))
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && tearer.countOf("peer-b") == 0 {
		time.Sleep(2 * time.Millisecond)
	}
	stop()
	stop() // idempotent

	if tearer.countOf("peer-b") == 0 {
		t.Fatal("background teardown loop never tore down the dead peer")
	}

	// An empty peer set (single-peer config) starts no goroutine and stops cleanly.
	noop := startPeerTeardownMonitor(newPeerTeardownMonitor(eng, tearer, nil, &fakeClock{now: now}), time.Millisecond, discardInfo(t))
	noop()
}

// testPubKey builds a config.Key from a fully-filled 32-byte pattern via its exported
// base64 UnmarshalText seam, so a test can construct configured peers with distinct public keys.
func testPubKey(t *testing.T, fill byte) config.Key {
	t.Helper()
	var raw [32]byte
	for i := range raw {
		raw[i] = fill
	}
	var k config.Key
	if err := k.UnmarshalText([]byte(base64.StdEncoding.EncodeToString(raw[:]))); err != nil {
		t.Fatalf("build key: %v", err)
	}
	return k
}
