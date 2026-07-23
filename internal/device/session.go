package device

import (
	"strconv"
	"strings"
	"sync"
	"time"

	awgdevice "github.com/amnezia-vpn/amneziawg-go/device"

	"github.com/7mind/wanbond/internal/log"
	"github.com/7mind/wanbond/internal/metrics"
	"github.com/7mind/wanbond/internal/telemetry"
)

// sessionPollInterval is the cadence at which the session monitor reads the engine's
// last-handshake state to drive the 0->1 'session established' edge log. It matches the
// probe cadence (the same rate the liveness plane runs at) so the session-established
// transition is timestamped close to when it happens — the /metrics gauge itself is
// read independently at scrape time and needs no polling.
const sessionPollInterval = telemetry.DefaultProbeInterval

// ipcGetter is the read seam onto the amneziawg engine's UAPI GET: it yields the
// newline-delimited peer state string that carries each peer's last_handshake_time_sec /
// _nsec lines. *awgdevice.Device satisfies it; a fake satisfies it in unit tests so the
// snapshot parsing and the 0->1 edge run without a live engine.
type ipcGetter interface {
	IpcGet() (string, error)
}

// sessionSnapshotter yields the current WG-session snapshot the metrics adapter exposes
// and the edge detector polls. *sessionMonitor satisfies it against the live engine; a
// fake satisfies it in unit tests.
type sessionSnapshotter interface {
	SessionSnapshot() metrics.SessionSnapshot
}

// sessionMonitor reads the amneziawg engine's peer last-handshake state at scrape/poll
// time and resolves it into a metrics.SessionSnapshot. It is the ONLY WG-session coupling
// the metrics plane takes; the bind stays WG-unaware. The engine is read through the UAPI
// GET seam (there is no public accessor for a peer's last-handshake instant), so the read
// is a parse of the peer dump rather than a field load — acceptable at scrape/probe
// cadence for the edge's single peer. It is stateless (no edge memory): the current
// verdict is a pure function of the engine state and the clock, so it is safe for the
// concurrent scrape goroutine and the poll loop to share one instance.
type sessionMonitor struct {
	engine ipcGetter
	clock  telemetry.Clock
	// expiry is the session-validity window: a completed handshake older than this no
	// longer counts as established (the tunnel is wedged, not converged). It is WireGuard's
	// RejectAfterTime — the point past which the current keypair is dead — so a healthy
	// tunnel (which rekeys well within it, and keeps the edge session warm via keepalive)
	// never flaps to 0, while a tunnel whose handshake stops advancing does.
	expiry time.Duration
}

// newSessionMonitor builds a session monitor over the engine. The clock is injected so the
// age/freshness derivation is deterministic under test.
func newSessionMonitor(engine ipcGetter, clock telemetry.Clock) *sessionMonitor {
	return &sessionMonitor{engine: engine, clock: clock, expiry: awgdevice.RejectAfterTime}
}

// SessionSnapshot reads the engine and resolves the connection-scoped WG-session verdict:
// the MOST RECENT completed handshake across all configured peers (for the edge's single
// concentrator peer this is simply that peer's handshake; for a multi-peer concentrator it
// is "at least one edge session is live"). Established is true only when such a handshake
// exists AND its age is within the validity window. An engine read error, or no completed
// handshake on any peer, yields the zero snapshot (Established=false, age=0) — the "still
// converging" reading. A negative age (clock skew) is clamped to zero.
func (m *sessionMonitor) SessionSnapshot() metrics.SessionSnapshot {
	dump, err := m.engine.IpcGet()
	if err != nil {
		return metrics.SessionSnapshot{}
	}
	latest := latestHandshakeNano(dump)
	if latest == 0 {
		return metrics.SessionSnapshot{}
	}
	age := m.clock.Now().Sub(time.Unix(0, latest))
	if age < 0 {
		age = 0
	}
	return metrics.SessionSnapshot{
		Established:      age <= m.expiry,
		LastHandshakeAge: age,
	}
}

// latestHandshakeNano parses an amneziawg UAPI GET dump and returns the most recent peer
// handshake instant as Unix nanoseconds, or 0 when no peer carries a completed handshake.
// Each peer block emits `last_handshake_time_sec=<s>` immediately followed by
// `last_handshake_time_nsec=<ns>`; a never-handshaked peer emits 0/0. The two lines are
// recombined into a single nanosecond instant, and the maximum across peers is returned.
func latestHandshakeNano(dump string) int64 {
	var latest int64
	var sec int64
	var haveSec bool
	for _, line := range strings.Split(dump, "\n") {
		key, val, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch key {
		case "last_handshake_time_sec":
			s, err := strconv.ParseInt(val, 10, 64)
			if err != nil {
				haveSec = false
				continue
			}
			sec, haveSec = s, true
		case "last_handshake_time_nsec":
			if !haveSec {
				continue
			}
			haveSec = false
			ns, err := strconv.ParseInt(val, 10, 64)
			if err != nil {
				continue
			}
			nano := sec*int64(time.Second) + ns
			if nano > latest {
				latest = nano
			}
		}
	}
	return latest
}

// perPeerHandshakeNano parses an amneziawg UAPI GET dump into a PER-PEER snapshot: each
// configured peer's most recent handshake instant as Unix nanoseconds, keyed by the peer's
// lowercase-hex public key (the same `public_key=<hex>` form uapiConfig renders and the
// engine's IpcGet dump emits). Unlike latestHandshakeNano — which flattens the whole dump to
// one global max for the connection-level session gauge — this keeps peers distinct so the
// concentrator teardown loop can level-check EACH non-primary peer independently. A peer block
// begins at its `public_key=` line and runs until the next one; a never-handshaked peer emits
// the 0/0 pair and is recorded with instant 0 (present, but not established). A block with no
// last-handshake lines at all also maps to 0. Keys are taken verbatim from the dump (already
// lowercase hex), so a caller compares against hex.EncodeToString(pub[:]).
func perPeerHandshakeNano(dump string) map[string]int64 {
	out := make(map[string]int64)
	var curKey string
	var sec int64
	var haveSec bool
	for _, line := range strings.Split(dump, "\n") {
		key, val, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch key {
		case "public_key":
			curKey = val
			haveSec = false
			if _, seen := out[curKey]; !seen {
				// Record presence even for a never-handshaked peer so the level check sees
				// it as "not established" (instant 0) rather than "absent from the dump".
				out[curKey] = 0
			}
		case "last_handshake_time_sec":
			if curKey == "" {
				continue
			}
			s, err := strconv.ParseInt(val, 10, 64)
			if err != nil {
				haveSec = false
				continue
			}
			sec, haveSec = s, true
		case "last_handshake_time_nsec":
			if curKey == "" || !haveSec {
				continue
			}
			haveSec = false
			ns, err := strconv.ParseInt(val, 10, 64)
			if err != nil {
				continue
			}
			out[curKey] = sec*int64(time.Second) + ns
		}
	}
	return out
}

// peerSessionSnapshotter yields the current per-peer WG-session snapshot list that
// metrics.Source.PeerSessions() exposes (T256, G28, M106). *peerSessionMonitor satisfies
// it against the live engine; a fake satisfies it in unit tests.
type peerSessionSnapshotter interface {
	PeerSessionSnapshots() []metrics.PeerSessionSnapshot
}

// peerSessionMonitor reads the amneziawg engine's per-peer last-handshake state at
// scrape time and resolves it into ONE metrics.PeerSessionSnapshot per configured peer
// (T256, G28, M106). Where sessionMonitor collapses every peer into a single
// connection-scoped "is SOME session live" verdict, this keeps peers distinct so a
// warm-standby promotion decision can ask "is THIS candidate peer's session live" — the
// per-concentrator proof of session health M106 needs. It reuses perPeerHandshakeNano —
// the SAME hex-pubkey dump parse deviceExitHealth.healthy (T269) already drives — rather
// than duplicating the parse. Stateless like sessionMonitor: safe for the concurrent
// scrape goroutine.
type peerSessionMonitor struct {
	engine ipcGetter
	clock  telemetry.Clock
	// expiry is the session-validity window, matching sessionMonitor/deviceExitHealth
	// (WireGuard's RejectAfterTime).
	expiry time.Duration
	// peers is the STATIC ordered set of every configured peer (primary included), each
	// paired with the stable name metrics/exposition attributes it to (the D58
	// primary-naming rule: "" for a true single-peer config, else its own configured
	// name — see allMonitoredPeers) and the lowercase-hex public key the engine's UAPI
	// dump identifies it by.
	peers []monitoredPeer
}

// newPeerSessionMonitor builds the per-peer session monitor over the engine for the
// given peer set (see allMonitoredPeers). The clock is injected so the age derivation is
// deterministic under test; expiry is WireGuard's RejectAfterTime, matching
// sessionMonitor/deviceExitHealth.
func newPeerSessionMonitor(engine ipcGetter, clock telemetry.Clock, peers []monitoredPeer) *peerSessionMonitor {
	return &peerSessionMonitor{engine: engine, clock: clock, expiry: awgdevice.RejectAfterTime, peers: peers}
}

// PeerSessionSnapshots reads the engine ONCE and resolves each configured peer's OWN
// session verdict from that single dump. An engine read error yields every peer at the
// zero snapshot (Established=false, age=0) — the "still converging" reading, matching
// sessionMonitor.SessionSnapshot's error handling. A negative age (clock skew) is
// clamped to zero, like sessionMonitor.
func (m *peerSessionMonitor) PeerSessionSnapshots() []metrics.PeerSessionSnapshot {
	out := make([]metrics.PeerSessionSnapshot, len(m.peers))
	dump, err := m.engine.IpcGet()
	if err != nil {
		for i, p := range m.peers {
			out[i] = metrics.PeerSessionSnapshot{Peer: p.name}
		}
		return out
	}
	handshakes := perPeerHandshakeNano(dump)
	now := m.clock.Now()
	for i, p := range m.peers {
		nano := handshakes[p.publicKey]
		if nano == 0 {
			out[i] = metrics.PeerSessionSnapshot{Peer: p.name}
			continue
		}
		age := now.Sub(time.Unix(0, nano))
		if age < 0 {
			age = 0
		}
		out[i] = metrics.PeerSessionSnapshot{
			Peer:                 p.name,
			Established:          age <= m.expiry,
			LastHandshakeSeconds: age.Seconds(),
		}
	}
	return out
}

// exitPeerHealth is one exit-capable peer's health-probe inputs (T269): the lowercase-hex public
// key its engine-session handshake is keyed by, and its OWN per-path liveness plane (that peer's
// prober set). Both are needed to answer the auto-promotion health question — session established
// AND at least one path up — for a candidate warm standby.
type exitPeerHealth struct {
	publicKeyHex string
	health       []hubHealth
}

// deviceExitHealth is the PRODUCTION exitHealth (T269): it answers whether a candidate exit-capable
// peer is a HEALTHY warm standby fit for auto-promotion — its WG session is established (the
// engine's last-handshake for that peer is within RejectAfterTime, the same gate sessionMonitor
// uses) AND at least one of its paths is up (its own liveness plane, mirroring hubFailover's
// allDownLocked: up = some path not Down). A peer not in peers, an engine read error, an
// unestablished session, or an all-down liveness plane all read UNHEALTHY, so promotion never
// moves egress onto a peer that could not carry it. A fake drives the selector's promotion logic
// in unit tests, so this concrete path stays engine-coupled.
type deviceExitHealth struct {
	engine ipcGetter
	clock  telemetry.Clock
	expiry time.Duration
	peers  map[string]exitPeerHealth
}

func (d *deviceExitHealth) healthy(name string) bool {
	ph, ok := d.peers[name]
	if !ok {
		return false
	}
	// At least one path up (the liveness plane). Checked first: it is a cheap in-memory read and
	// avoids an engine IpcGet for an obviously-down candidate.
	anyUp := false
	for _, hp := range ph.health {
		if hp.State() != telemetry.StateDown {
			anyUp = true
			break
		}
	}
	if !anyUp {
		return false
	}
	// Session established: the engine's last-handshake for this peer is within the validity window.
	dump, err := d.engine.IpcGet()
	if err != nil {
		return false
	}
	nano := perPeerHandshakeNano(dump)[ph.publicKeyHex]
	return nano != 0 && d.clock.Now().Sub(time.Unix(0, nano)) <= d.expiry
}

// peerTearer is the seam onto the bind's per-peer teardown: Bind.TearDownPeer frees a dead
// configured peer's heavy state (resequencer ring, FEC buffers, demux source bindings) and
// returns true when it actually reclaimed that state, false on a no-op (peer unknown, the
// embedded primary, still LIVE, or already torn down). *bind.Multipath satisfies it; a fake
// records calls in unit tests so the level-triggered wiring runs without a live bind.
type peerTearer interface {
	TearDownPeer(name string) bool
}

// monitoredPeer pairs a non-primary configured peer's stable name (the id TearDownPeer is
// keyed on) with the lowercase-hex public key the engine's UAPI dump identifies it by, so the
// teardown loop can map a dump entry back to the peer to tear down.
type monitoredPeer struct {
	name      string
	publicKey string // lowercase hex of the 32-byte WG public key (hex.EncodeToString(pub[:]))
}

// peerTeardownMonitor drives the LEVEL-TRIGGERED reclaim of dead concentrator peers (D50).
// A peer's heavy receive state is instantiated on its FIRST authenticated PROBE (not on WG
// handshake), so a valid-psk peer that BINDS via PROBE but never completes a handshake has
// last_handshake=0 forever and never produces the Established 1->0 EDGE an edge-triggered
// monitor would need — its state would leak permanently. So this monitor is level-triggered:
// on each poll it reads the engine's per-peer handshake snapshot and, for EVERY configured
// non-primary peer that is NOT currently established (no handshake, OR a handshake aged past
// RejectAfterTime), it calls Bind.TearDownPeer(name). TearDownPeer is idempotent-safe (it
// refuses live peers and the primary and no-ops on an already-torn/absent name), so repeating
// the call every poll is harmless and survives a daemon-reload loss of prior edge memory. Only
// the LOG is deduped (via torn): ONE INFO on the transition to torn-down, reset when the peer
// re-establishes. It is engaged ONLY in multi-peer (concentrator) mode; the single-peer
// edge/hub keeps the global sessionMonitor path byte-identical.
type peerTeardownMonitor struct {
	engine ipcGetter
	tearer peerTearer
	clock  telemetry.Clock
	// expiry is the session-validity window (WireGuard's RejectAfterTime): a handshake older
	// than this no longer counts as established, matching sessionMonitor's gate.
	expiry time.Duration
	// peers is the ORDERED set of configured non-primary peers to level-check (the primary is
	// never torn down by session loss — TearDownPeer refuses it — so it is excluded here).
	peers []monitoredPeer
	// torn dedupes the transition LOG, NOT the idempotent call: name -> already logged as
	// torn-down. Cleared when a peer re-establishes so a later loss logs afresh.
	torn map[string]bool
}

// newPeerTeardownMonitor builds the level-triggered teardown monitor over the engine and bind
// for the given non-primary peers. The clock is injected so the handshake-age derivation is
// deterministic under test; expiry is WireGuard's RejectAfterTime, matching sessionMonitor.
func newPeerTeardownMonitor(engine ipcGetter, tearer peerTearer, peers []monitoredPeer, clock telemetry.Clock) *peerTeardownMonitor {
	return &peerTeardownMonitor{
		engine: engine,
		tearer: tearer,
		clock:  clock,
		expiry: awgdevice.RejectAfterTime,
		peers:  peers,
		torn:   make(map[string]bool),
	}
}

// poll performs ONE level-triggered sweep: it reads the engine's per-peer handshake snapshot
// and, for each monitored non-primary peer that is NOT currently established, calls
// Bind.TearDownPeer(name). The call is repeated every poll while the peer stays dead (idempotent
// by contract); only the LOG is deduped — one INFO fires on the poll where TearDownPeer actually
// reclaims state (its true return), and the dedupe clears when the peer re-establishes so a
// subsequent loss logs again. An engine read error skips the sweep (no spurious teardown). The
// poll loop is single-goroutine, so the torn map needs no lock.
func (m *peerTeardownMonitor) poll(lg log.Logger) {
	dump, err := m.engine.IpcGet()
	if err != nil {
		return
	}
	handshakes := perPeerHandshakeNano(dump)
	now := m.clock.Now()
	for _, p := range m.peers {
		nano := handshakes[p.publicKey]
		established := nano != 0 && now.Sub(time.Unix(0, nano)) <= m.expiry
		if established {
			// Re-established: allow a future loss to log its teardown afresh.
			delete(m.torn, p.name)
			continue
		}
		reclaimed := m.tearer.TearDownPeer(p.name)
		if reclaimed && !m.torn[p.name] {
			m.torn[p.name] = true
			lg.Info("concentrator peer session lost; heavy state torn down", "peer", p.name)
		}
	}
}

// startPeerTeardownMonitor starts the background poll loop that level-checks each configured
// non-primary peer's WG session and tears down any that is not established. Like the session
// monitor it uses a wall-clock ticker; the age derivation runs through the injected clock the
// monitor holds. The returned stopper is idempotent and must be invoked BEFORE the engine is
// torn down so no IpcGet races the close. It is a no-op (no goroutine) when interval <= 0 or
// no non-primary peer is configured (the single-peer edge/hub).
func startPeerTeardownMonitor(mon *peerTeardownMonitor, interval time.Duration, lg log.Logger) (stop func()) {
	if interval <= 0 || len(mon.peers) == 0 {
		return func() {}
	}
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				mon.poll(lg)
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(done) }) }
}

// sessionEdge detects the 0->1 (false->true) WG-session-established transition. observe is
// called on each poll with the current established verdict; it returns true EXACTLY on a
// false->true edge, so the caller emits the 'session established' record once per newly
// (re)established session. A steady-state 1->1 poll returns false (no duplicate log); a
// 1->0->1 sequence (a session lost and re-established) fires again on the fresh rise.
type sessionEdge struct {
	established bool
}

func (e *sessionEdge) observe(established bool) (rose bool) {
	rose = established && !e.established
	e.established = established
	return rose
}

// startSessionMonitor starts the background poll loop that watches the WG-session verdict
// and emits ONE INFO 'session established' record on each 0->1 edge. Like the probe loop it
// uses a wall-clock ticker (production timing glue); the verdict it reads runs through the
// injected clock the monitor holds. The returned stopper is idempotent and must be invoked
// BEFORE the engine is torn down so no IpcGet races the close. It is a no-op (no goroutine)
// when interval <= 0.
func startSessionMonitor(snap sessionSnapshotter, interval time.Duration, lg log.Logger) (stop func()) {
	if interval <= 0 {
		return func() {}
	}
	done := make(chan struct{})
	go func() {
		edge := &sessionEdge{}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				s := snap.SessionSnapshot()
				if edge.observe(s.Established) {
					lg.Info("session established", "last_handshake_age_ms", s.LastHandshakeAge.Milliseconds())
				}
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(done) }) }
}
