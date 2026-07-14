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
