package device

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	awgdevice "github.com/amnezia-vpn/amneziawg-go/v3/device"

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

// scriptedEngine is an ipcGetter whose engine state (a UAPI dump or a read error) the test
// swaps between polls, so a single monitor can be driven across a dead -> live -> dead sequence.
type scriptedEngine struct {
	mu    sync.Mutex
	state fakeEngine
}

func (e *scriptedEngine) set(state fakeEngine) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.state = state
}

func (e *scriptedEngine) IpcGet() (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.state.IpcGet()
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

// TestPeerTeardownPoll drives the level check over a scripted engine: one poll per entry of
// polls, each against that entry's engine state. It asserts WHICH peers were torn down, in call
// order, and how many teardown INFO records were logged.
func TestPeerTeardownPoll(t *testing.T) {
	const teardownInfo = "concentrator peer session lost"
	now := time.Unix(1_000_000, 0)
	aged := now.Add(-(awgdevice.RejectAfterTime + time.Second))
	fresh := now.Add(-3 * time.Second)
	peerB := []monitoredPeer{{name: "peer-b", publicKey: "bbbb"}}
	agedB := fakeEngine{dump: uapiPeerKey("bbbb", aged)}
	freshB := fakeEngine{dump: uapiPeerKey("bbbb", fresh)}

	cases := []struct {
		name      string
		peers     []monitoredPeer
		polls     []fakeEngine
		wantCalls []string
		wantInfos int
		// wantSilent additionally requires that the polls logged nothing at all.
		wantSilent bool
	}{
		{
			// Acceptance (a): a non-primary peer whose handshake has aged past RejectAfterTime
			// is torn down — TearDownPeer invoked with its configured name — and ONE INFO logs
			// the transition.
			name:      "aged out",
			peers:     peerB,
			polls:     []fakeEngine{agedB},
			wantCalls: []string{"peer-b"},
			wantInfos: 1,
		},
		{
			// Acceptance (b) — the D50 core: a peer that instantiated heavy state via an
			// authenticated PROBE but has last_handshake=0 (NO handshake ever, hence NO
			// Established 1->0 edge) is STILL torn down by the LEVEL check, not skipped for
			// lack of an edge.
			name:      "never handshaked",
			peers:     peerB,
			polls:     []fakeEngine{{dump: uapiPeerKey("bbbb", time.Time{})}},
			wantCalls: []string{"peer-b"},
			wantInfos: 1,
		},
		{
			// Acceptance (c): a peer with a fresh handshake is established, so the level check
			// leaves it alone — even across repeated polls (no teardown, no spurious log). The
			// primary is never even a monitored peer.
			name:       "live peer untouched",
			peers:      peerB,
			polls:      []fakeEngine{freshB, freshB, freshB},
			wantCalls:  nil,
			wantInfos:  0,
			wantSilent: true,
		},
		{
			// "Dedupe the LOG, not the call": a persistently-dead peer is torn down on EVERY
			// poll (the idempotent call repeats — that is what survives a daemon-reload loss of
			// edge memory), but only ONE INFO logs the transition.
			name:      "repeated level check dedupes log",
			peers:     peerB,
			polls:     []fakeEngine{agedB, agedB, agedB, agedB},
			wantCalls: []string{"peer-b", "peer-b", "peer-b", "peer-b"},
			wantInfos: 1,
		},
		{
			// The acceptance (d) analog at the monitor level: a peer torn down, then
			// RE-ESTABLISHED (a fresh handshake — the same signal a re-bind PROBE + relayed
			// traffic ultimately produces), stops being torn down; and a SUBSEQUENT loss tears
			// it down and logs again (the dedupe resets on re-establishment).
			name:      "re-establish relogs",
			peers:     peerB,
			polls:     []fakeEngine{agedB, freshB, agedB},
			wantCalls: []string{"peer-b", "peer-b"},
			wantInfos: 2,
		},
		{
			// An engine read error skips the sweep entirely — no spurious teardown on a
			// transiently unreadable engine.
			name:      "engine error skips",
			peers:     peerB,
			polls:     []fakeEngine{{err: errIpc}},
			wantCalls: nil,
			wantInfos: 0,
		},
		{
			// A dump peer NOT in the monitored set (e.g. the primary, which is excluded) is
			// never torn down, and the monitored peers are checked independently: one aged
			// (torn), one fresh (kept).
			name: "only monitored peers",
			peers: []monitoredPeer{
				{name: "peer-b", publicKey: "bbbb"},
				{name: "peer-c", publicKey: "cccc"},
			},
			polls: []fakeEngine{{dump: uapiPeerKey("aaaa", aged) + // primary key — present in dump but NOT monitored
				uapiPeerKey("bbbb", aged) + // monitored, dead -> torn
				uapiPeerKey("cccc", fresh)}}, // monitored, live -> kept
			wantCalls: []string{"peer-b"},
			wantInfos: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eng := &scriptedEngine{}
			tearer := &recordingTearer{}
			buf := &syncBuffer{}
			lg, err := log.New("info", buf)
			if err != nil {
				t.Fatalf("log.New: %v", err)
			}
			mon := newPeerTeardownMonitor(eng, tearer, tc.peers, &fakeClock{now: now})

			for _, state := range tc.polls {
				eng.set(state)
				mon.poll(lg)
			}

			if got := tearer.snapshot(); !equalStrings(got, tc.wantCalls) {
				t.Fatalf("TearDownPeer calls = %v, want %v", got, tc.wantCalls)
			}
			if n := strings.Count(buf.String(), teardownInfo); n != tc.wantInfos {
				t.Errorf("teardown INFO logged %d times, want %d\n%s", n, tc.wantInfos, buf.String())
			}
			if tc.wantSilent && buf.String() != "" {
				t.Errorf("polls produced a log: %s", buf.String())
			}
		})
	}
}

// configuredPeer is one configured WireGuard peer of a monitored-peer-set case: its name and
// the byte its 32-byte public key is filled with.
type configuredPeer struct {
	name    string
	keyFill byte
}

// monitoredPeersCase is one config shape handed to a monitored-peer-set builder, with the
// peers the builder must return for it — each as the name the monitor reports and the
// keyFill of the configured peer whose lowercase-hex public key identifies it in the UAPI dump.
type monitoredPeersCase struct {
	name  string
	role  config.Role
	peers []configuredPeer
	want  []configuredPeer
}

// runMonitoredPeersCases builds each case's config and asserts build returns exactly the
// wanted monitored peers, in order.
func runMonitoredPeersCases(t *testing.T, build func(*config.Config, []config.PeerIdentity) []monitoredPeer, cases []monitoredPeersCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{Role: tc.role}
			for _, p := range tc.peers {
				cfg.WireGuard.Peers = append(cfg.WireGuard.Peers, config.Peer{PublicKey: testPubKey(t, p.keyFill), Name: p.name})
			}
			want := make([]monitoredPeer, len(tc.want))
			for i, p := range tc.want {
				raw := testPubKey(t, p.keyFill).Bytes()
				want[i] = monitoredPeer{name: p.name, publicKey: hex.EncodeToString(raw[:])}
			}

			got := build(cfg, cfg.PeerIdentities())

			if len(got) != len(want) {
				t.Fatalf("monitored peers = %+v, want %+v", got, want)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Errorf("monitored peer %d = %+v, want %+v", i, got[i], want[i])
				}
			}
		})
	}
}

// TestConcentratorMonitoredPeers asserts the device-side peer set builder: a single-peer config
// yields NO monitored peers (the teardown loop stays inert, single-peer path unchanged), while a
// multi-peer config yields every NON-primary peer paired with its configured name and the
// lowercase-hex public key the UAPI dump identifies it by.
func TestConcentratorMonitoredPeers(t *testing.T) {
	threePeers := []configuredPeer{{"primary", 0x11}, {"peer-b", 0x22}, {"peer-c", 0x33}}
	runMonitoredPeersCases(t, concentratorMonitoredPeers, []monitoredPeersCase{
		{
			name:  "single-peer config monitors nothing",
			peers: []configuredPeer{{"solo", 0x11}},
			want:  nil,
		},
		{
			name:  "multi-peer concentrator config monitors every non-primary peer",
			role:  config.RoleConcentrator,
			peers: threePeers,
			want:  []configuredPeer{{"peer-b", 0x22}, {"peer-c", 0x33}},
		},
		{
			// D50 guard (T251/Q68b): a MULTI-PEER EDGE config monitors NOTHING. The additional
			// edge peers are warm-standby concentrators — healthy by design even carrying no
			// data — so the level-check teardown must never engage on the edge, where it would
			// tear a warm standby down the moment its session momentarily aged. This is the
			// exact shape that, before the role gate, wrongly returned the non-primary set
			// (identical to the concentrator case above but for the role).
			name:  "multi-peer edge config monitors nothing",
			role:  config.RoleEdge,
			peers: threePeers,
			want:  nil,
		},
	})
}

// TestAllMonitoredPeers asserts the T256/G28/M106 generalization of
// concentratorMonitoredPeers to EVERY role: a single-peer config yields ONE entry with
// Peer "" (the D58 primary-naming rule), while a multi-peer config — of ANY role,
// including the edge (T251/Q68b warm-standby concentrators) — yields EVERY configured
// peer, PRIMARY INCLUDED, each paired with its own configured name and the lowercase-hex
// public key the UAPI dump identifies it by.
func TestAllMonitoredPeers(t *testing.T) {
	runMonitoredPeersCases(t, allMonitoredPeers, []monitoredPeersCase{
		{
			name:  "single-peer config yields one entry named \"\"",
			peers: []configuredPeer{{"solo", 0x11}},
			want:  []configuredPeer{{"", 0x11}},
		},
		{
			name:  "multi-peer concentrator config yields every peer including the primary",
			role:  config.RoleConcentrator,
			peers: []configuredPeer{{"primary", 0x11}, {"peer-b", 0x22}},
			want:  []configuredPeer{{"primary", 0x11}, {"peer-b", 0x22}},
		},
		{
			// Generalized to the EDGE role (T256): a multi-peer edge's additional peers are
			// warm-standby concentrators, not concentrator-side edges, but PeerSessions() still
			// needs a session verdict for EACH — unlike concentratorMonitoredPeers' D50 teardown
			// set, which is deliberately EMPTY on the edge.
			name:  "multi-peer edge config yields every peer including the primary",
			role:  config.RoleEdge,
			peers: []configuredPeer{{"primary", 0x11}, {"standby", 0x22}},
			want:  []configuredPeer{{"primary", 0x11}, {"standby", 0x22}},
		},
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
