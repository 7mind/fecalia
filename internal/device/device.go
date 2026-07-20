// Package device brings a wanbond tunnel up from a validated configuration: it
// creates the TUN interface, drives the embedded amneziawg-go engine over the
// multipath Bind, and applies the WireGuard (and, when configured, amnezia
// obfuscation) parameters via the engine's UAPI. It owns ONLY the tunnel engine
// — interface addressing and routing are left to the operator (systemd/wg-quick
// style), so the daemon stays free of privileged shell-outs. The interface name
// is exposed via Tunnel.Name for that external configuration step.
package device

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	awgdevice "github.com/amnezia-vpn/amneziawg-go/device"
	"github.com/amnezia-vpn/amneziawg-go/tun"

	"github.com/7mind/wanbond/internal/adaptivefec"
	"github.com/7mind/wanbond/internal/bind"
	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/dnsresolve"
	"github.com/7mind/wanbond/internal/fec"
	"github.com/7mind/wanbond/internal/log"
	"github.com/7mind/wanbond/internal/metrics"
	"github.com/7mind/wanbond/internal/monitor"
	"github.com/7mind/wanbond/internal/sched"
	"github.com/7mind/wanbond/internal/telemetry"
)

// metricsShutdownTimeout bounds the graceful shutdown of the /metrics endpoint on
// Close (and on a reload that rebinds it). The endpoint is loopback-only with no
// long-lived scrapes, so this is comfortably generous.
const metricsShutdownTimeout = 2 * time.Second

// monitorShutdownTimeout bounds the graceful shutdown of the [monitor] endpoint on
// Close (and on a reload that reconciles it). It mirrors metricsShutdownTimeout;
// the monitor's /ws push handlers observe the server-context cancellation Close
// issues, so a stalled client cannot stretch teardown past this bound.
const monitorShutdownTimeout = 2 * time.Second

// defaultTUNName is the requested interface name; the kernel honours it unless it
// collides (it never does across the edge and concentrator network namespaces).
const defaultTUNName = "wanbond0"

// tunMTU is the tunnel (TUN) MTU for a config: the MINIMUM, across all configured
// paths, of each path's inner MTU — that path's declared MTU (T200, D85; 0 means
// unset, falling back to bind.DefaultPathMTU) mapped through bind.InnerMTU. Sizing
// to the smallest path keeps a full-MTU inner packet fragmentation-free no matter
// which path the scheduler sends it over (see internal/bind mtu.go and
// docs/p1-mtu.md). This is smaller than the plain-WireGuard 1420 because the
// bonding layer adds its own outer DATA frame per datagram; with FEC enabled it is
// a further bind.FECParityMTUPenalty smaller so a full-size PARITY frame also fits
// the path MTU (T24) rather than fragmenting. A config with no paths (defensive;
// validate() normally requires at least one) falls back to bind.DefaultPathMTU.
func tunMTU(cfg *config.Config) int {
	if len(cfg.Paths) == 0 {
		return bind.InnerMTU(bind.DefaultPathMTU, cfg.FEC.Enabled)
	}
	min := 0
	for _, p := range cfg.Paths {
		pathMTU := p.MTU
		if pathMTU == 0 {
			pathMTU = bind.DefaultPathMTU
		}
		inner := bind.InnerMTU(pathMTU, cfg.FEC.Enabled)
		if min == 0 || inner < min {
			min = inner
		}
	}
	return min
}

// Tunnel is a running wanbond tunnel: the amneziawg engine, its TUN, and the
// multipath Bind. Close tears all three down.
type Tunnel struct {
	dev  *awgdevice.Device
	tun  tun.Device
	name string
	// routePrefixes is the default-route wiring installed into wanbond0 for a
	// mode=default-route peer (I6, Q41): the wg-quick-style split of that peer's
	// allowed_ips (the two /1s for a /0), installed after dev.Up and withdrawn on
	// Close. Empty (nil) for a config with no default-route peer, so a plain tunnel
	// programs — and tears down — no routes at all.
	routePrefixes []netip.Prefix
	// bind is the multipath Bind the engine drives; Reload calls its runtime
	// AddPath/RemovePath to apply a config-reload path diff (T30).
	bind *bind.Multipath
	// cfg is the configuration the RUNNING tunnel reflects: the boot config, its path
	// set updated on each successful Reload to the current membership (survivors keep
	// their original parameters — a membership-only reload does not re-apply modified
	// path parameters or non-path fields). Reload diffs the next desired config against
	// it to warn about changes it cannot apply. Guarded by reloadMu.
	cfg *config.Config
	// reloadMu serializes Reload against itself (SIGHUP handlers), guarding cfg.
	reloadMu sync.Mutex
	// log is the device-component logger, retained for Reload diagnostics.
	log log.Logger
	// stopProbes halts the per-path probe cadence loop (T37). Close calls it before
	// tearing the engine down so emission stops before the sockets close. It is
	// idempotent and never nil (a no-op when the bind runs without probers).
	stopProbes func()
	// stopReconcile halts the background deferred-path reconcile loop (T55): it retries
	// binding the paths a tolerant Open left DOWN as their addresses appear. Close calls
	// it before tearing the engine down so no reconcile races the socket close. It is
	// idempotent and never nil (a no-op when the bind runs without probers / never defers).
	stopReconcile func()
	// stopHubFailover halts the edge-side hub-failover monitor loop (T57): it watches the
	// per-path liveness plane and, on hub loss (all paths to the active concentrator DOWN),
	// advances to the next ordered peer endpoint, repoints the bond's remote, and initiates
	// a fresh WG re-handshake. Close calls it before tearing the engine down. It is
	// idempotent and never nil (a no-op on the concentrator, or an edge with a single
	// concentrator endpoint, or a bind without the probe transport).
	stopHubFailover func()
	// stopResolution halts the edge-side DNS re-resolution loop (T74): it re-resolves each
	// opted-in hostname peer-endpoint spec on the [dns] poll cadence and out-of-band on hub loss,
	// feeding fresh records to the hub-failover controller. Close calls it between stopHubFailover
	// and dev.Close. It is idempotent and never nil (a no-op on the concentrator, an all-literal
	// edge, or when no hostname spec is configured — Q29 inertness).
	stopResolution func()
	// stopSession halts the WG-session monitor loop (I2): it polls the engine's last-handshake
	// state at probe cadence and emits ONE INFO 'session established' record on each 0->1 edge.
	// Close calls it before dev.Close so no IpcGet races the engine teardown. It is idempotent
	// and never nil.
	stopSession func()
	// stopPeerTeardown halts the concentrator per-peer teardown monitor loop (D50/T126): in
	// multi-peer mode it level-checks each configured non-primary peer's WG session and calls the
	// bind's idempotent TearDownPeer on any peer whose session is gone, reclaiming its heavy
	// receive state. Close calls it before dev.Close so no IpcGet races the engine teardown. It is
	// idempotent and never nil (a no-op for a single-peer config, which monitors no non-primary peer).
	stopPeerTeardown func()
	// amnezia is the obfuscation profile this tunnel holds against the
	// process-global amnezia guard (see amneziaGuard); Close releases it.
	amnezia     config.Amnezia
	releaseOnce sync.Once

	// metricsSrc is the live metrics.Source over the Bind; it is stable for the tunnel's
	// life (the Bind pointer never changes), so a reload that rebinds the endpoint reuses
	// the SAME Source — its derived-throughput last-sample state survives the rebind.
	metricsSrc metrics.Source
	// metricsSrv is the running /metrics endpoint, nil when [metrics].listen is empty.
	// It is (re)assigned by applyMetricsLocked and read by Close; both hold reloadMu, so
	// a SIGHUP-driven rebind never races shutdown. metricsListen mirrors the address it
	// is bound to so a reload can detect a listen change without inspecting the server.
	metricsSrv    *metrics.Server
	metricsListen string

	// monitorSrc is the DEDICATED metrics.Source feeding the [monitor] endpoint. It is a
	// SECOND Source over the SAME Bind — separate from metricsSrc — so the monitor's
	// derived-throughput last-sample state is not shared with the Prometheus scraper: two
	// independent scrape cadences reading one shared last-sample map would corrupt each
	// other's rate derivation (T165 invariant). Like metricsSrc it is built unconditionally
	// (cheap) so a reload that later turns [monitor].listen ON has a Source ready.
	monitorSrc metrics.Source
	// monitorSrv is the running [monitor] endpoint, nil when [monitor].listen is empty. It is
	// (re)assigned by applyMonitorLocked and read by Close; both hold reloadMu, so a
	// SIGHUP-driven reconcile never races shutdown. monitorListen/monitorToken mirror the
	// address+token it is bound to so a reload can detect a listen OR token change (either
	// forces a rebind) without inspecting the server.
	monitorSrv    *monitor.Server
	monitorListen string
	monitorToken  string
}

// SINGLE-ENGINE-PER-PROCESS INVARIANT (defect D2).
//
// amneziawg-go stores the amnezia magic-header message types in PACKAGE-GLOBAL
// variables — device.MessageInitiationType, MessageResponseType,
// MessageCookieReplyType, MessageTransportType. (*device.Device).IpcSet assigns
// them (in handlePostConfig) from a configured engine's profile, and
// (*device.Device).Close restores them to the WireGuard defaults (in
// resetProtocol) UNCONDITIONALLY — closing ANY engine, plain or configured,
// reverts the process-global message types to their defaults.
//
// Two consequences make a CONFIGURED (amnezia) engine require PROCESS
// EXCLUSIVITY:
//   - a second configured engine would overwrite the first engine's message-type
//     framing at IpcSet; and even with the SAME profile, closing the first engine
//     runs resetProtocol and reverts the globals to defaults under the second,
//     still-live engine, silently dropping its tunnel to plain-WireGuard framing.
//   - closing ANY other engine — a PLAIN (unconfigured) one included — runs
//     resetProtocol and resets the globals out from under a live configured engine.
//
// So the rule wanbond ASSERTS (rather than vendor-patching the fork) is:
//   - a configured engine may come up only when NO other engine is live, and
//   - no engine (plain or configured) may come up while a configured engine is live.
//
// PLAIN engines may coexist with each other: they never set the globals, and
// resetProtocol on their Close only restores the defaults they already use, so it
// is idempotent among them. wanbond runs exactly one tunnel per process, so the
// guard only ever trips on genuine misuse.
type amneziaGuard struct {
	mu         sync.Mutex
	plainLive  int  // number of live plain-WireGuard (unconfigured) engines
	configLive bool // whether a configured (amnezia) engine is live (at most one)
}

// globalAmneziaGuard enforces the single-amnezia-engine-per-process invariant for
// every Tunnel brought up in this process.
var globalAmneziaGuard amneziaGuard

// acquire registers an about-to-start engine against the process-exclusivity rule
// (see amneziaGuard). A configured (amnezia) engine is admitted only when NO other
// engine is live. A plain engine is admitted only when no configured engine is
// live; plain engines may coexist with one another. The caller must release the
// SAME profile exactly once when the engine is torn down (plain engines included —
// release is no longer a no-op for them).
func (g *amneziaGuard) acquire(a config.Amnezia) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if a.Configured() {
		if g.configLive || g.plainLive > 0 {
			return fmt.Errorf("device: refusing to start an amnezia-configured engine while another engine is live: " +
				"amneziawg-go keeps the magic-header message types in process-global state and resets them to defaults on ANY engine's Close, " +
				"so a configured engine requires process exclusivity — at most one, with no other engine alongside it (D2)")
		}
		g.configLive = true
		return nil
	}
	if g.configLive {
		return fmt.Errorf("device: refusing to start a second engine while an amnezia-configured engine is live: " +
			"closing this engine would reset amneziawg-go's process-global message types to defaults under the live amnezia tunnel (D2)")
	}
	g.plainLive++
	return nil
}

// release drops an engine's hold on the guard. A configured engine clears the
// single configured slot; a plain engine decrements the live plain count.
func (g *amneziaGuard) release(a config.Amnezia) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if a.Configured() {
		g.configLive = false
		return
	}
	if g.plainLive > 0 {
		g.plainLive--
	}
}

// Up creates the TUN, wires the multipath Bind into the amneziawg engine,
// applies the crypto configuration from cfg, and brings the device up. The same
// path drives both roles; the role only changes which UAPI fields cfg carries
// (the concentrator sets listen_port; the edge sets each peer's endpoint).
func Up(cfg *config.Config, lg log.Logger) (*Tunnel, error) {
	clg := lg.Component("device")

	tunDev, err := tun.CreateTUN(defaultTUNName, tunMTU(cfg))
	if err != nil {
		return nil, fmt.Errorf("device: create TUN: %w", err)
	}
	name, err := tunDev.Name()
	if err != nil {
		_ = tunDev.Close()
		return nil, fmt.Errorf("device: read TUN name: %w", err)
	}
	// Bring the interface administratively UP now (I1): amneziawg-go never does this
	// itself, and a write to a DOWN TUN yields EIO — a silent-dead-tunnel footgun the
	// operator previously had to work around with an out-of-band `ip link set up`.
	// Addressing stays operator-owned; this sets ONLY the IFF_UP flag (see ifUp).
	if err := ifUp(name); err != nil {
		_ = tunDev.Close()
		return nil, fmt.Errorf("device: bring interface up: %w", err)
	}
	// Apply the TUN persistence policy (I7, Q38) BEFORE the amnezia single-engine
	// guard in up(): with tun_persist=true the kernel keeps wanbond0 (and its
	// operator-owned addresses/routes) across a daemon restart; with the default
	// false it explicitly CLEARS the flag so the device disappears on Close exactly
	// as before. This composes with the ifUp above and does not touch the reload or
	// restart-on-failure paths — a reload never re-creates the TUN, and a persistent
	// device is simply re-adopted by name on the next Up (see setTUNPersist).
	if err := setTUNPersist(tunDev, cfg.TUNPersist); err != nil {
		_ = tunDev.Close()
		return nil, fmt.Errorf("device: set TUN persistence: %w", err)
	}
	clg.Info("interface up", "interface", name, "tun_persist", cfg.TUNPersist)
	// The DNS re-resolution transport is the one the (validated) [dns] block selects. It is passed
	// as a factory (not eagerly constructed) so up() builds it AT MOST ONCE and ONLY when some peer
	// carries a hostname endpoint spec — a config with zero hostname specs never constructs a
	// resolver (Q29 inertness). The injected TUN and factory are also the seams device tests drive.
	return up(cfg, clg, tunDev, name, cfg.DNS.NewResolver)
}

// resolverFactory builds the DNS resolver used for hostname endpoint re-resolution. It is injected
// so a device test can supply a fake; production passes cfg.DNS.NewResolver. up() invokes it AT
// MOST ONCE, and ONLY when some peer carries a hostname endpoint spec.
type resolverFactory func() (dnsresolve.Resolver, error)

// up drives the tunnel bring-up over an already-created TUN and an injected resolver factory: it
// wires the multipath Bind into the amneziawg engine, performs the bounded initial hostname
// resolve (Q30), applies the crypto/endpoint UAPI config built ONLY from resolved entries, brings
// the device up, and starts the probe / reconcile / hub-failover / re-resolution loops. Splitting
// it out of Up gives device tests a seam to inject a channel TUN and a fake resolver without the
// privileged tun.CreateTUN. The same path drives both roles; the role only changes which UAPI
// fields cfg carries (the concentrator sets listen_port; the edge sets each peer's endpoint).
func up(cfg *config.Config, clg log.Logger, tunDev tun.Device, name string, newResolver resolverFactory) (*Tunnel, error) {
	// Wrap the TUN so a write that fails with EIO gets an actionable, rate-limited diagnostic
	// (naming the interface's link state/MTU and the remedy) alongside the raw errno, instead
	// of surfacing only as the engine's generic "input/output error" (D39, I3). Every other
	// Device method is unaffected.
	tunDev = newDiagnosingTUN(tunDev, name, clg)

	// Claim the single-amnezia-engine-per-process invariant (D2) BEFORE IpcSet
	// assigns amneziawg-go's process-global message-type state. On any failure
	// below, ok stays false and the deferred release returns the hold; the
	// successful path transfers the hold to the returned Tunnel.
	if err := globalAmneziaGuard.acquire(cfg.Amnezia); err != nil {
		_ = tunDev.Close()
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			globalAmneziaGuard.release(cfg.Amnezia)
		}
	}()

	// One random per-boot probe session id, shared by every path AND every configured peer (it
	// identifies THIS boot, not a path or peer): a peer restart presents a new id that resets
	// the surviving responder's anti-replay high-water so liveness recovers (T38, D12), and a
	// runtime-added path (T30) reuses it so its probes join this boot's stream.
	sessionID, err := telemetry.NewSessionID(rand.Reader)
	if err != nil {
		_ = tunDev.Close()
		return nil, fmt.Errorf("device: new probe session id: %w", err)
	}
	// PeerIdentities is the SINGLE place the single-peer/multi-peer effective-PSK decision is
	// made (G4): for a single-peer config every identity's PSK is the top-level psk; for a
	// multi-peer concentrator each is that peer's own psk. Its order matches cfg.WireGuard.Peers
	// (and thus uapiConfig's peer render), so the engine peer set and the Bind peer set agree.
	ids := cfg.PeerIdentities()
	// The primary peer (peers[0]) drives on-wire liveness through the SAME prober values that
	// back its scheduler's PathHealth (T37). It is keyed on the FIRST identity's effective psk:
	// the top-level psk for a single-peer config (byte-identical to pre-G4), or peer 0's own psk
	// for a multi-peer concentrator.
	scheduler, probers, newProber, err := buildScheduler(cfg, ids[0].PSK, sessionID, clg)
	if err != nil {
		_ = tunDev.Close()
		return nil, fmt.Errorf("device: build scheduler: %w", err)
	}
	mpBind, err := bind.NewMultipath(cfg.Paths, ids[0].PSK, scheduler, probers, newProber, fecConfig(cfg.FEC), adaptiveFECConfig(cfg.FEC), cfg.Amnezia, clg)
	if err != nil {
		_ = tunDev.Close()
		return nil, fmt.Errorf("device: build multipath bind: %w", err)
	}
	// A multi-peer concentrator names its primary too (D58): NewMultipath always mints the
	// primary as peers[0] with name="" (a single-peer edge/hub needs no name — there is only
	// one peer to key, and this keeps that exposition byte-identical to pre-T94). Once a
	// SECOND identity is configured, every bound peer — including the primary — must carry its
	// configured name so /metrics attributes series to the right edge instead of leaking the
	// primary as peer="". This runs BEFORE the AddConcentratorPeer loop below so that loop's
	// name-collision checks (name == m.name, the peersByName duplicate check) compare against
	// the primary's FINAL name.
	if len(ids) > 1 {
		if err := mpBind.SetPrimaryPeerName(ids[0].Name); err != nil {
			_ = tunDev.Close()
			return nil, fmt.Errorf("device: set primary peer name %q: %w", ids[0].Name, err)
		}
	}
	// Concentrator per-peer wiring (G4/T93): register each ADDITIONAL configured peer with its
	// OWN prober set, scheduler, and prober factory (all keyed on that peer's effective psk),
	// BEFORE dev.Up drives the bind's Open — which builds each registered peer's per-(peer,path)
	// view of every bound socket, reconciles its scheduler, and reports its stable virtual
	// endpoint to the engine (A1). A single-peer config has exactly one identity, so this loop
	// is empty and the wiring stays byte-identical to the pre-G4 single-peer path.
	for _, id := range ids[1:] {
		psched, pprobers, pfactory, perr := buildScheduler(cfg, id.PSK, sessionID, clg)
		if perr != nil {
			_ = tunDev.Close()
			return nil, fmt.Errorf("device: build scheduler for peer %q: %w", id.Name, perr)
		}
		if perr := mpBind.AddConcentratorPeer(id.Name, id.PSK, psched, pprobers, pfactory); perr != nil {
			_ = tunDev.Close()
			return nil, fmt.Errorf("device: wire concentrator peer %q: %w", id.Name, perr)
		}
	}
	dev := awgdevice.NewDevice(tunDev, mpBind, engineLogger(clg, cfg.Log.Level, mpBind.EverHadLivePath))

	// Drive a forced WG handshake initiation off the bind's first-path-up latch (T117/D37/T120):
	// registered HERE, before StartProbeLoop below can flip any path Up — see
	// startFirstPathUpHandshake's doc comment for why the ordering matters and why it is inert for
	// the concentrator role (peers[0] is the edge's sole peer per config validation; unused, and
	// harmless, when cfg.Role is concentrator).
	startFirstPathUpHandshake(cfg, mpBind, deviceRehandshake(dev, cfg.WireGuard.Peers[0].PublicKey))

	// Bounded initial hostname resolution (Q30): construct the resolver ONCE (only when some peer
	// carries a hostname spec — Q29 inertness), resolve each hostname spec under a short timeout,
	// and seed its expansion. The per-peer boot endpoint — the flattened head of the seeded specs —
	// is what the UAPI render installs on the engine peer; a hostname that does not resolve in the
	// boot window (or a resolver that fails to construct) leaves its spec EMPTY and the peer boots
	// WITHOUT a peer endpoint (tolerant boot). The re-resolution loop then completes the wiring on
	// first success via the FIRST-RESOLVE INSTALL PATH (R70). Literal specs are seeded verbatim, so
	// an all-literal peer boots byte-for-byte as before.
	boot := resolveBootEndpoints(cfg, newResolver, clg)

	uapi, err := uapiConfig(cfg, boot.endpoints)
	if err != nil {
		dev.Close()
		return nil, fmt.Errorf("device: build UAPI config: %w", err)
	}
	if err := dev.IpcSet(uapi); err != nil {
		dev.Close()
		return nil, fmt.Errorf("device: apply UAPI config: %w", err)
	}
	if err := dev.Up(); err != nil {
		dev.Close()
		return nil, fmt.Errorf("device: bring up: %w", err)
	}

	// Default-route wiring (I6, Q41): with a mode=default-route peer, install the
	// wg-quick-style split default route (the two /1s of that peer's allowed_ips) INTO
	// wanbond0 now that the interface is up and the engine is running, so an arbitrary
	// destination egresses through the tunnel. This is the daemon's FIRST route
	// programming — confined to the mode being explicitly enabled: a config with no
	// default-route peer computes an empty set, opens no netlink socket, and installs
	// nothing, so default behaviour is byte-for-byte unchanged. STRICT Q41 boundary —
	// routes ONLY (no policy routing, no SNAT, no concentrator forwarding). Fail fast:
	// a route error aborts bring-up. Before returning we best-effort withdraw whatever
	// partial set installRoutes landed: dev.Close tears down the engine/TUN, but under
	// tun_persist=true the interface (and its routes) SURVIVES Close, and up() aborts
	// before the Tunnel exists so Close/removeRoutes never runs — the already-installed
	// prefixes would otherwise leak on the surviving interface. removeRoutes errors are
	// ignored (the install error is the one that matters; a leftover is overwritten by
	// the next successful start's NLM_F_REPLACE re-install anyway). Removed on Close.
	routePrefixes := defaultRoutePrefixes(cfg)
	if len(routePrefixes) > 0 {
		if err := installRoutes(name, routePrefixes); err != nil {
			_ = removeRoutes(name, routePrefixes)
			dev.Close()
			return nil, fmt.Errorf("device: install default-route wiring: %w", err)
		}
		clg.Info("default-route wiring installed", "interface", name, "routes", len(routePrefixes))
	}

	// The engine has opened the bind (dev.Up → BindUpdate → Open), so the per-path
	// sockets exist: start the probe cadence now. Close stops it before dev.Close.
	// The interval is the SINGLE-SOURCE-OF-TRUTH telemetry default, which also arms
	// the bind's receive-path liveness sweep throttle (D15).
	//
	// UNLIKE the up/down thresholds (down_after + per-path ride_through, now config-driven
	// via buildScheduler/proberConfigForPath — D86/T207), the probe cadence stays the fixed
	// telemetry.DefaultProbeInterval this pass: it is a global loop period shared by every
	// path AND every peer and it also gates the D15 receive-path sweep throttle, so making it
	// configurable is a broader change deferred beyond T207. Detection latency is therefore
	// bounded by down_after (+ ride_through for an UP path) plus this one interval.
	stopProbes := mpBind.StartProbeLoop(telemetry.DefaultProbeInterval)
	// Start the background deferred-path reconciler alongside probing (T55): a path the
	// tolerant Open left DOWN (its source_addr not yet assignable — a 5G modem without a
	// lease at boot) is retried and promoted to a live path as its address appears,
	// WITHOUT a tunnel restart. Close stops it before dev.Close, like the probe loop.
	stopReconcile := mpBind.StartReconcileLoop(bind.DefaultReconcileInterval)
	// Start the edge-side hub-failover monitor (T57) AND the DNS re-resolution loop (T74): on hub
	// loss (every path to the active concentrator DOWN) failover advances to the next ordered peer
	// endpoint, repoints the bond's remote, and re-handshakes; re-resolution re-resolves opted-in
	// hostname specs and installs/repoints on change. Both a no-op on the concentrator, a
	// single-IP-literal edge, or a bind without the probe transport. Started AFTER dev.Up so the
	// engine peer the re-handshake / endpoint-install look up exists (IpcSet added it above). Close
	// stops both before dev.Close, like the probe/reconcile loops.
	stopHubFailover, stopResolution := startFailoverAndResolution(cfg, mpBind, probers, dev, boot, clg)

	// The session monitor reads the engine's peer last-handshake state (I2). It backs BOTH
	// the /metrics session snapshot (read at scrape time) and the 0->1 'session established'
	// edge log (driven by its own poll loop). One instance is shared: it is stateless, so the
	// concurrent scrape goroutine and the poll loop reading the same engine is safe.
	sessMon := newSessionMonitor(dev, telemetry.SystemClock{})
	// Start the WG-session monitor poll loop AFTER dev.Up so the engine peer it reads exists.
	// Close stops it before dev.Close, like the probe/reconcile loops.
	stopSession := startSessionMonitor(sessMon, sessionPollInterval, clg)
	// Start the concentrator per-peer teardown monitor (D50/T126): in MULTI-PEER mode it
	// level-checks each configured non-primary peer's WG session every poll and calls the bind's
	// idempotent TearDownPeer on any peer whose session is gone (no handshake, or aged past
	// RejectAfterTime), reclaiming its resequencer ring, FEC buffers, and demux cap slots — the
	// leak the edge-triggered global monitor cannot catch (a valid-psk peer that binds via PROBE
	// but never completes a handshake produces no 1->0 edge). concentratorMonitoredPeers returns
	// an EMPTY peer set for a single-peer config, so the loop is a no-op there and the single-peer
	// monitor path is byte-identical to pre-T126. Started after dev.Up (the engine peers it reads
	// exist); Close stops it before dev.Close.
	teardownMon := newPeerTeardownMonitor(dev, mpBind, concentratorMonitoredPeers(cfg, ids), telemetry.SystemClock{})
	stopPeerTeardown := startPeerTeardownMonitor(teardownMon, sessionPollInterval, clg)

	t := &Tunnel{
		dev: dev, tun: tunDev, name: name, bind: mpBind, cfg: cfg, log: clg,
		routePrefixes: routePrefixes,
		stopProbes:    stopProbes, stopReconcile: stopReconcile,
		stopHubFailover: stopHubFailover, stopResolution: stopResolution,
		stopSession: stopSession, stopPeerTeardown: stopPeerTeardown, amnezia: cfg.Amnezia,
		// The Source reads live per-path counters/telemetry from the Bind and derives
		// throughput from the byte-counter delta between scrapes (see metricsSource). The
		// WG-session snapshot is read from the engine via sessMon. It is built unconditionally
		// (cheap) so a reload that later turns [metrics].listen ON has a Source ready; the
		// endpoint itself is started only when a listen is configured.
		metricsSrc: newMetricsSource(mpBind, sessMon, telemetry.SystemClock{}),
		// A SECOND, DEDICATED Source for the [monitor] endpoint over the SAME Bind/session
		// seam — NOT metricsSrc. The two endpoints scrape on independent cadences and each
		// Source derives throughput from its OWN last-sample state, so sharing one Source
		// between them would let each scrape reset the other's rate baseline (T165). Built
		// unconditionally so a reload can turn [monitor].listen ON with a Source ready.
		monitorSrc: newMetricsSource(mpBind, sessMon, telemetry.SystemClock{}),
	}

	// Stand up the /metrics endpoint when configured. A non-loopback listen is refused
	// by metrics.NewServer (fail fast) — surface it as an Up failure rather than booting
	// a tunnel that silently exposes per-path operational data off-host.
	t.reloadMu.Lock()
	err = t.applyMetricsLocked(cfg.Metrics.Listen)
	t.reloadMu.Unlock()
	if err != nil {
		// The tunnel is fully constructed and holds the amnezia guard, so transfer that
		// ownership to it (ok=true) BEFORE tearing it down: t.Close releases the guard via
		// releaseOnce, and suppressing the !ok defer avoids a double release. t.Close also
		// stops the probe loop and closes the engine/TUN.
		ok = true
		t.Close()
		return nil, fmt.Errorf("device: start metrics endpoint: %w", err)
	}

	// Stand up the [monitor] endpoint when configured, mirroring the /metrics endpoint
	// above but fed by the DEDICATED t.monitorSrc. A non-loopback listen without a token
	// is refused by monitor.NewServer (fail fast) — surface it as an Up failure rather than
	// booting a tunnel that silently exposes operational data off-host. Because role is a
	// config-only field and BOTH RoleEdge and RoleConcentrator flow through up(), this one
	// wiring gives edge+concentrator monitor parity.
	t.reloadMu.Lock()
	err = t.applyMonitorLocked(cfg.Monitor.Listen, cfg.Monitor.Token)
	t.reloadMu.Unlock()
	if err != nil {
		ok = true
		t.Close()
		return nil, fmt.Errorf("device: start monitor endpoint: %w", err)
	}

	ok = true
	clg.Info("tunnel up", "interface", name, "role", string(cfg.Role))
	return t, nil
}

// concentratorMonitoredPeers returns the set of NON-PRIMARY configured peers the per-peer
// teardown monitor (D50/T126) level-checks: every peer after peers[0], paired with the stable
// name TearDownPeer is keyed on (from PeerIdentities, so the single-peer/multi-peer name
// derivation is shared) and the lowercase-hex public key the engine's UAPI dump identifies it
// by (hex.EncodeToString(pub[:]), the same form uapiConfig renders). ids MUST be
// cfg.PeerIdentities() — its order matches cfg.WireGuard.Peers, so index i pairs peer i's
// public key with identity i's name. A single-peer config (edge/hub, or a one-peer
// concentrator) yields an EMPTY set, so the teardown loop is inert and the pre-T126 single-peer
// monitor path is unchanged. The primary (peers[0]) is excluded: TearDownPeer refuses it, and
// its lifecycle is Open/Close, not session teardown.
func concentratorMonitoredPeers(cfg *config.Config, ids []config.PeerIdentity) []monitoredPeer {
	peers := cfg.WireGuard.Peers
	if len(peers) <= 1 {
		return nil
	}
	mon := make([]monitoredPeer, 0, len(peers)-1)
	for i := 1; i < len(peers); i++ {
		pub := peers[i].PublicKey.Bytes()
		mon = append(mon, monitoredPeer{name: ids[i].Name, publicKey: hex.EncodeToString(pub[:])})
	}
	return mon
}

// applyMetricsLocked reconciles the running /metrics endpoint to listen: it starts the
// endpoint when one is desired and none runs, stops it when listen is empty, and rebinds
// (stop old, start new) when the address changed. It is idempotent for an unchanged
// address. The Source is reused across a rebind so its derived-throughput state is not
// reset. The caller MUST hold reloadMu (Up and Reload both do), which also serializes it
// against Close reading metricsSrv. On a NewServer/refuse error the previous server is
// left running untouched, so a bad reload never drops a working endpoint.
func (t *Tunnel) applyMetricsLocked(listen string) error {
	if listen == t.metricsListen {
		return nil
	}
	if listen == "" {
		t.stopMetricsLocked()
		return nil
	}
	srv, err := metrics.NewServer(listen, t.metricsSrc, t.cfg.WeightedCapacitySane, t.log)
	if err != nil {
		return err
	}
	t.stopMetricsLocked()
	srv.Start()
	t.metricsSrv = srv
	t.metricsListen = listen
	return nil
}

// stopMetricsLocked gracefully shuts the running endpoint down (if any) and clears the
// bookkeeping. Caller holds reloadMu.
func (t *Tunnel) stopMetricsLocked() {
	if t.metricsSrv == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), metricsShutdownTimeout)
	defer cancel()
	if err := t.metricsSrv.Close(ctx); err != nil {
		t.log.Warn("metrics endpoint shutdown error", "error", err.Error())
	}
	t.metricsSrv = nil
	t.metricsListen = ""
}

// applyMonitorLocked reconciles the running [monitor] endpoint to (listen, token): it
// starts the endpoint when one is desired and none runs, stops it when listen is empty,
// and rebinds when EITHER the address OR the token changed — a token change alters the
// auth layer, so it must rebind even at an unchanged address. It is idempotent for an
// unchanged (listen, token) pair. The dedicated t.monitorSrc is reused across a rebind so
// its derived-throughput state is not reset. The caller MUST hold reloadMu (Up and Reload
// both do), which also serializes it against Close reading monitorSrv. The rebind ORDER is
// address-dependent (see the body): a same-address (token-only) rebind stops the old
// server before binding the new to avoid EADDRINUSE at a fixed port; an address-changing
// rebind binds the new server first and only stops the old on success, so a bad
// address-changing reload never drops a working endpoint. Mirrors applyMetricsLocked.
func (t *Tunnel) applyMonitorLocked(listen, token string) error {
	if listen == t.monitorListen && token == t.monitorToken {
		return nil
	}
	if listen == "" {
		t.stopMonitorLocked()
		return nil
	}
	// Rebind ordering depends on whether the LISTEN ADDRESS changes:
	//   - SAME address (e.g. a token-only rotation): the old listener still
	//     holds the port, so the new bind would collide (EADDRINUSE). Stop the
	//     old server FIRST, then bind the new. A NewServer failure here leaves
	//     the endpoint DOWN, but the address is unchanged and a bad token was
	//     already rejected by config.validate at load, so this is unexpected.
	//   - DIFFERENT address: the new bind is on a different port, so bind the
	//     new server FIRST and only stop the old on success — preserving the
	//     "a bad reload never drops a working endpoint" property.
	sameAddr := listen == t.monitorListen
	if sameAddr {
		t.stopMonitorLocked()
	}
	srv, err := monitor.NewServer(listen, token, t.monitorSrc, t.log)
	if err != nil {
		return err
	}
	if !sameAddr {
		t.stopMonitorLocked()
	}
	srv.Start()
	t.monitorSrv = srv
	t.monitorListen = listen
	t.monitorToken = token
	return nil
}

// stopMonitorLocked gracefully shuts the running [monitor] endpoint down (if any) and
// clears the bookkeeping. Caller holds reloadMu. Mirrors stopMetricsLocked.
func (t *Tunnel) stopMonitorLocked() {
	if t.monitorSrv == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), monitorShutdownTimeout)
	defer cancel()
	if err := t.monitorSrv.Close(ctx); err != nil {
		t.log.Warn("monitor endpoint shutdown error", "error", err.Error())
	}
	t.monitorSrv = nil
	t.monitorListen = ""
	t.monitorToken = ""
}

// Reload applies a reloaded configuration to the RUNNING tunnel by diffing its
// path set against the live one and adding/removing paths at runtime (T30), WITHOUT
// tearing the tunnel down: the WG session, the surviving paths, and in-flight
// resequencing are all preserved. cfg is assumed already validated (config.Load
// fails fast on a bad file, so a bad reload never reaches here and never disturbs
// the running tunnel). Only the path set is diffed — the crypto/amnezia/role fields
// are fixed for the life of the process and a reload does not re-key the engine.
//
// New paths are added BEFORE absent ones are removed, so a failed addition aborts
// with every existing path still in service. Diffing is by name: a path present in
// both sets is left untouched (its source/dest parameters are not re-read).
//
// A membership-only reload CANNOT apply every kind of change: a same-name path whose
// source/dest changed, a path REORDER (index 0 is the preferred primary, so a reorder
// is a priority change), and every non-path field (psk, wireguard, amnezia, role, log,
// metrics) are all out of scope for T30. Rather than silently diverge from the file,
// Reload logs an EXPLICIT WARNING per ignored change (reloadWarnings) so the operator
// knows exactly what was dropped; only the path-membership add/remove is applied.
func (t *Tunnel) Reload(cfg *config.Config) error {
	t.reloadMu.Lock()
	defer t.reloadMu.Unlock()

	for _, w := range reloadWarnings(t.cfg, cfg) {
		t.log.Warn("reload: ignored config change (path-membership reload only)", "change", w)
	}

	// Rebind the /metrics endpoint if its listen address changed (T23). This is one of
	// the few non-path fields a reload DOES apply: the endpoint is external to the WG
	// session and the Bind, so restarting it disturbs neither. applyMetricsLocked reuses
	// the tunnel's stable Source (its derived-throughput state survives), and on a refuse
	// (e.g. a newly non-loopback address) leaves the previous endpoint running and fails
	// the reload — the running tunnel is never disturbed. Applied BEFORE the path diff so
	// a metrics-refuse aborts before any membership change.
	if cfg.Metrics.Listen != t.metricsListen {
		if err := t.applyMetricsLocked(cfg.Metrics.Listen); err != nil {
			return fmt.Errorf("device: reload metrics endpoint: %w", err)
		}
		t.log.Info("reload: metrics endpoint rebound", "listen", cfg.Metrics.Listen)
	}

	// Reconcile the [monitor] endpoint (T169) — the SECOND non-path field a reload applies,
	// mirroring /metrics. A listen OR token change reconciles the endpoint (start/stop/rebind)
	// via the dedicated t.monitorSrc without disturbing the WG session or the Bind. On a refuse
	// (e.g. a newly non-loopback address without a token) the previous endpoint is left running
	// and the reload fails. Applied BEFORE the path diff so a monitor-refuse aborts before any
	// membership change.
	if cfg.Monitor.Listen != t.monitorListen || cfg.Monitor.Token != t.monitorToken {
		if err := t.applyMonitorLocked(cfg.Monitor.Listen, cfg.Monitor.Token); err != nil {
			return fmt.Errorf("device: reload monitor endpoint: %w", err)
		}
		t.log.Info("reload: monitor endpoint reconciled", "listen", cfg.Monitor.Listen)
	}

	add, remove := diffPaths(t.bind.PathNames(), cfg.Paths)
	for _, def := range add {
		if err := t.bind.AddPath(def); err != nil {
			return fmt.Errorf("device: reload add path %q: %w", def.Name, err)
		}
		t.log.Info("reload: path added", "path", def.Name)
	}
	for _, name := range remove {
		if err := t.bind.RemovePath(name); err != nil {
			return fmt.Errorf("device: reload remove path %q: %w", name, err)
		}
		t.log.Info("reload: path removed", "path", name)
	}
	// D74: recompute the config-derived weighted-capacity-sanity verdict from the reloaded
	// config and re-set the retained gauge on the running /metrics endpoint, so a reload
	// that changed the path set (add/remove) updates wanbond_weighted_capacity_sane instead
	// of leaving it frozen at the boot value. WARN when the verdict diverges from the
	// running one so the operator sees the sanity flip. Skipped when no endpoint runs, or
	// under the active-backup policy (a nil verdict — the series is absent and the scheduler
	// policy is fixed for the process, so a reload never introduces it).
	if t.metricsSrv != nil && cfg.WeightedCapacitySane != nil {
		newSane := *cfg.WeightedCapacitySane
		if t.cfg.WeightedCapacitySane == nil || *t.cfg.WeightedCapacitySane != newSane {
			t.log.Warn("reload: weighted-capacity-sanity verdict changed", "sane", newSane)
		}
		t.metricsSrv.SetWeightedCapacitySane(newSane)
	}

	// Advance the running config to the membership now in service. Survivors keep their
	// ORIGINAL parameters and all non-path fields stay as booted (the ignored changes
	// were not applied), so a subsequent identical reload re-warns about a still-diverged
	// file rather than silently accepting it. Metrics.Listen is carried to the applied
	// value so a subsequent reload diffs against the endpoint actually running.
	t.cfg = runningConfig(t.cfg, add, remove)
	t.cfg.Metrics.Listen = t.metricsListen
	// Carry the applied [monitor] state so a subsequent reload diffs against the endpoint
	// actually running (mirroring Metrics.Listen above).
	t.cfg.Monitor.Listen = t.monitorListen
	t.cfg.Monitor.Token = t.monitorToken
	// Carry the recomputed weighted-capacity verdict so a subsequent reload's divergence
	// check compares against the value the gauge now holds (D74).
	t.cfg.WeightedCapacitySane = cfg.WeightedCapacitySane
	return nil
}

// reloadWarnings compares the desired config against the currently-running one and
// returns one human-readable warning per change a path-membership-only reload (T30)
// cannot apply: a modified same-name path (source/dest), a path reorder, and any
// non-path field. Membership add/remove is NOT reported here — it is applied. Full
// modify-support is out of scope; SILENCE is not — the operator must be told what was
// ignored. It is a pure function so the warning set is unit-testable.
func reloadWarnings(live, desired *config.Config) []string {
	var w []string

	if live.Role != desired.Role {
		w = append(w, fmt.Sprintf("role %q -> %q", live.Role, desired.Role))
	}
	if !reflect.DeepEqual(live.PSK, desired.PSK) {
		w = append(w, "psk changed")
	}
	if !reflect.DeepEqual(live.WireGuard, desired.WireGuard) {
		w = append(w, "wireguard section changed")
	}
	if !reflect.DeepEqual(live.Amnezia, desired.Amnezia) {
		w = append(w, "amnezia section changed")
	}
	if !reflect.DeepEqual(live.Log, desired.Log) {
		w = append(w, "log section changed")
	}
	// tun_persist is applied only once, at device.Up (TUNSETPERSIST); a reload never
	// re-issues the ioctl, so a SIGHUP that flips it must NOT be silently accepted —
	// otherwise an operator who sets tun_persist=true and reloads believes persistence
	// is armed while the next stop still destroys wanbond0 and drops its addressing.
	if live.TUNPersist != desired.TUNPersist {
		w = append(w, fmt.Sprintf("tun_persist %v -> %v — the running interface keeps its original persistence; ignored until restart", live.TUNPersist, desired.TUNPersist))
	}
	// NOTE: neither a Metrics nor a Monitor change is warned here — unlike the other
	// non-path fields, the reload APPLIES both by rebinding their endpoints (see Reload,
	// T169). Warning about a change that is honoured would misinform the operator.
	if !reflect.DeepEqual(live.Scheduler, desired.Scheduler) {
		w = append(w, "scheduler section changed — the running scheduler keeps its original policy/parameters until restart")
	}
	if !reflect.DeepEqual(live.FEC, desired.FEC) {
		w = append(w, "fec section changed — the running FEC parameters are unchanged until restart")
	}
	if !reflect.DeepEqual(live.DNS, desired.DNS) {
		w = append(w, "dns section changed — the running resolver configuration is unchanged until restart")
	}
	if !reflect.DeepEqual(live.Liveness, desired.Liveness) {
		w = append(w, "liveness section changed — the running liveness thresholds are unchanged until restart")
	}
	// The top-level Bind default (I5, Q42) is resolved by normalize() into every path
	// that omits its own `bind`, so a change here has already propagated into
	// Path.Bind by the time reloadWarnings runs — but the RUNNING sockets keep the
	// binding they were opened with, so it still needs its own actionable warning
	// (kept separate from the generic catch-all below to avoid double-warning).
	if live.Bind != desired.Bind {
		w = append(w, "default bind mode changed — running sockets keep their original binding")
	}

	// Same-name paths whose parameters changed: diffPaths matches by name only, so a
	// modified source/dest on an existing path is otherwise silently dropped.
	liveByName := make(map[string]config.Path, len(live.Paths))
	for _, p := range live.Paths {
		liveByName[p.Name] = p
	}
	for _, d := range desired.Paths {
		l, ok := liveByName[d.Name]
		if !ok {
			continue // a genuinely new path is added, not ignored
		}
		if l.SourceAddr != d.SourceAddr || l.DestAddr != d.DestAddr {
			w = append(w, fmt.Sprintf("path %q source/dest changed — the running path keeps its original binding", d.Name))
		}
		if l.Bind != d.Bind {
			w = append(w, fmt.Sprintf("path %q bind mode changed — the running socket keeps its original binding", d.Name))
		}
		// D70: a same-name path's declared link capacity (link_bandwidth/link_rtt) is NOT
		// applied by a membership reload — the running path keeps its original pacing/weight
		// — and the D52 catch-all zeroes Paths before its DeepEqual, so a change here is
		// otherwise silent. Warn so the operator knows the new declaration is deferred.
		if l.LinkBandwidthBitsPerSec != d.LinkBandwidthBitsPerSec || l.LinkRTT != d.LinkRTT {
			w = append(w, fmt.Sprintf("path %q link_bandwidth/link_rtt changed — the running path keeps its original capacity declaration; restart required", d.Name))
		}
	}

	if reordered(live.Paths, desired.Paths) {
		w = append(w, "path priority order changed (reorder) — the running order is unchanged")
	}

	// D52 option B — future-proof catch-all: copy live/desired with every
	// INDIVIDUALLY handled field zeroed (including Paths and Metrics — Metrics IS
	// applied by Reload, see the NOTE above, so it must not warn here either) and
	// DeepEqual the copies. A Config field added later that nobody has taught this
	// function to warn about falls through to this generic warning instead of being
	// silently accepted, so the "SILENCE is not acceptable" invariant (D52) cannot
	// regress by omission.
	lc, dc := *live, *desired
	lc.Role, dc.Role = "", ""
	lc.PSK, dc.PSK = config.Key{}, config.Key{}
	lc.WireGuard, dc.WireGuard = config.WireGuard{}, config.WireGuard{}
	lc.Amnezia, dc.Amnezia = config.Amnezia{}, config.Amnezia{}
	lc.Log, dc.Log = config.Log{}, config.Log{}
	lc.TUNPersist, dc.TUNPersist = false, false
	lc.Scheduler, dc.Scheduler = config.SchedulerConfig{}, config.SchedulerConfig{}
	lc.FEC, dc.FEC = config.FEC{}, config.FEC{}
	lc.DNS, dc.DNS = config.DNS{}, config.DNS{}
	lc.Liveness, dc.Liveness = config.Liveness{}, config.Liveness{}
	lc.Bind, dc.Bind = "", ""
	lc.Paths, dc.Paths = nil, nil
	lc.Metrics, dc.Metrics = config.Metrics{}, config.Metrics{}
	lc.Monitor, dc.Monitor = config.Monitor{}, config.Monitor{}
	// WeightedCapacitySane (T144) is a value COMPUTED from Scheduler+Paths, never an
	// independent operator knob (toml:"-") — a change to it is always a symptom of a
	// Scheduler or Paths change, both already compared above (or, for a same-name
	// path's link_bandwidth specifically, a pre-existing gap outside T144's scope).
	// Comparing it directly here would be redundant at best and could double-warn.
	lc.WeightedCapacitySane, dc.WeightedCapacitySane = nil, nil
	if !reflect.DeepEqual(lc, dc) {
		w = append(w, "other config section changed — reload does not apply it; restart required")
	}
	return w
}

// reordered reports whether the paths common to BOTH slices appear in a different
// relative order in desired than in live. The common subsequence has the same length
// on both sides (it is the name intersection), so a positional mismatch is a reorder.
func reordered(live, desired []config.Path) bool {
	desiredNames := make(map[string]struct{}, len(desired))
	for _, p := range desired {
		desiredNames[p.Name] = struct{}{}
	}
	liveNames := make(map[string]struct{}, len(live))
	for _, p := range live {
		liveNames[p.Name] = struct{}{}
	}
	var liveCommon, desiredCommon []string
	for _, p := range live {
		if _, ok := desiredNames[p.Name]; ok {
			liveCommon = append(liveCommon, p.Name)
		}
	}
	for _, p := range desired {
		if _, ok := liveNames[p.Name]; ok {
			desiredCommon = append(desiredCommon, p.Name)
		}
	}
	for i := range liveCommon {
		if liveCommon[i] != desiredCommon[i] {
			return true
		}
	}
	return false
}

// runningConfig advances the running config to the membership the bind now serves:
// the survivors (live paths not removed, keeping their ORIGINAL parameters and order)
// followed by the added paths (their new parameters). Non-path fields are carried over
// unchanged — a membership-only reload does not re-apply them. It mirrors the m.defs
// order the bind rebuilds on a Close→Open (survivors-in-order, then appended adds).
func runningConfig(live *config.Config, add []config.Path, remove []string) *config.Config {
	removeSet := make(map[string]struct{}, len(remove))
	for _, n := range remove {
		removeSet[n] = struct{}{}
	}
	paths := make([]config.Path, 0, len(live.Paths)+len(add))
	for _, p := range live.Paths {
		if _, gone := removeSet[p.Name]; !gone {
			paths = append(paths, p)
		}
	}
	paths = append(paths, add...)
	running := *live
	running.Paths = paths
	return &running
}

// diffPaths computes, by name, which desired paths are not yet live (to add) and
// which live paths are no longer desired (to remove). It is a pure function so the
// reload diff is unit-testable without a running engine.
func diffPaths(live []string, desired []config.Path) (add []config.Path, remove []string) {
	liveSet := make(map[string]struct{}, len(live))
	for _, n := range live {
		liveSet[n] = struct{}{}
	}
	desiredSet := make(map[string]struct{}, len(desired))
	for _, p := range desired {
		desiredSet[p.Name] = struct{}{}
		if _, ok := liveSet[p.Name]; !ok {
			add = append(add, p)
		}
	}
	for _, n := range live {
		if _, ok := desiredSet[n]; !ok {
			remove = append(remove, n)
		}
	}
	return add, remove
}

// defaultFailbackDwell is how long a recovered higher-priority path must stay up
// before egress fails BACK to it, damping flap-induced thrash (T15 hysteresis).
// Unlike the probe-cadence/liveness thresholds (which are the shared
// telemetry.Default* single source of truth, D16), the failback dwell is not part
// of the failover-recovery budget — failover to a backup is instant — so it stays a
// device-local constant.
const defaultFailbackDwell = 5 * time.Second

// fecConfig maps the validated [fec] config block onto the fec.Config the multipath
// Bind consumes (T24), or returns nil when FEC is disabled so the Bind runs the
// datapath with no parity plane. The ratio was already range-checked in config
// validation; the Bind re-validates defensively.
func fecConfig(f config.FEC) *fec.Config {
	if !f.Enabled {
		return nil
	}
	return &fec.Config{
		DataShards:   f.DataShards,
		ParityShards: f.ParityShards,
		Deadline:     f.Deadline,
	}
}

// adaptiveFECConfig maps the [fec] block onto the adaptive controller config the multipath
// Bind drives (T29), or returns nil when FEC is disabled or the block did not opt into
// adaptive mode — in which case the Bind runs the fixed-ratio plane (T24) unchanged. The
// controller uses the simulation-proven default control law (internal/adaptivefec,
// DefaultConfig), with only the group geometry pinned to the configured ratio: DataShards
// (K) is the FEC data_shards and MaxParity (the parity CEILING) is the FEC parity_shards,
// which the receiver's decoder is built at. The Bind re-validates and cross-checks these
// against the FEC config defensively.
func adaptiveFECConfig(f config.FEC) *adaptivefec.Config {
	if !f.Enabled || !f.Adaptive {
		return nil
	}
	c := adaptivefec.DefaultConfig()
	c.DataShards = f.DataShards
	c.MaxParity = f.ParityShards
	// Exactly one sizing mode is active (config load enforces mutual exclusion): in the
	// residual-SLA mode f.SafetyFactor is 0 and f.TargetResidual drives M; in the legacy
	// mode f.TargetResidual is 0 and f.SafetyFactor (defaulted at load) drives M.
	c.SafetyFactor = f.SafetyFactor
	c.TargetResidual = f.TargetResidual
	return &c
}

// proberConfigForPath builds ONE path's telemetry.ProberConfig from the loaded config
// (D86/T207). DownAfter comes from the top-level cfg.Liveness.DownAfter (a single global
// threshold, DEFAULTED at load by T203 to telemetry.DefaultDownAfter when the [liveness]
// block is absent), and RideThrough is THIS path's own dwell (cfg.Paths[i].RideThrough,
// default 0). LossWindow and UpAfterSuccesses stay the fixed telemetry defaults: the probe
// cadence and up-side hysteresis are NOT yet configurable this pass (StartProbeLoop keeps
// telemetry.DefaultProbeInterval), so those knobs remain the single-source-of-truth
// constants. On a fully-defaulted config the result is byte-identical to the pre-T207
// hardcoded literal — DownAfter=DefaultDownAfter, RideThrough=0 — an exact-behaviour
// identity guard (see TestBuildSchedulerLivenessFromConfig).
func proberConfigForPath(cfg *config.Config, rideThrough time.Duration) telemetry.ProberConfig {
	return telemetry.ProberConfig{
		LossWindow: telemetry.DefaultLossWindow,
		Liveness: telemetry.LivenessConfig{
			DownAfter:        cfg.Liveness.DownAfter,
			UpAfterSuccesses: telemetry.DefaultUpSuccesses,
			RideThrough:      rideThrough,
		},
	}
}

// buildScheduler constructs ONE peer's boot-time per-path prober set, its runtime prober
// factory, and the send scheduler over them — ALL keyed on that peer's effective psk (R72) —
// in cfg.Paths' configured priority order (index 0 = the preferred primary path). The returned
// probers ARE the scheduler's PathHealth sources (a *Prober is internally synchronized,
// satisfying the PathHealth concurrency contract — a bare *Liveness would not) and are handed
// to the bind so the probe transport drives the very same liveness the scheduler selects on
// (T37 replaces the T15 sched.AlwaysUp placeholder with real on-wire failover).
//
// The single-peer edge/hub/concentrator calls this once (psk = the top-level effective psk, so
// the wiring is byte-identical to pre-G4); a multi-peer concentrator calls it once per
// configured peer, each with that peer's OWN effective psk, so one peer's probers/reflector
// authenticate under a DIFFERENT key and reject another peer's frames (T84). sessionID is the
// per-boot probe session id — it identifies THIS boot, not a path or peer — shared by every
// path AND every peer: each peer's reflector keys anti-replay under its own psk, so a shared
// session id never conflates two peers' probe streams, and a runtime-added path (T30) reuses it
// so its probes join this boot's stream and the peer's reflector adopts them without a
// challenge reset.
func buildScheduler(cfg *config.Config, psk config.Key, sessionID uint64, lg log.Logger) (sched.Scheduler, []*telemetry.Prober, bind.ProberFactory, error) {
	clock := telemetry.SystemClock{}
	// newProber mints one path's Prober with the shared session/clock, keyed on THIS peer's
	// psk, and a PER-PATH ProberConfig (D86/T207): the global down_after threshold plus this
	// path's own ride-through dwell. It is the single construction point for this peer's
	// boot-time AND runtime (T30) paths, so both measure liveness identically. The runtime
	// bind.ProberFactory carries the path's ride_through so a runtime-added path gets its
	// configured dwell too — see proberConfigForPath.
	newProber := func(name string, id uint8, rideThrough time.Duration) *telemetry.Prober {
		return telemetry.NewProber(name, id, sessionID, psk, proberConfigForPath(cfg, rideThrough), clock, lg)
	}
	probers := make([]*telemetry.Prober, len(cfg.Paths))
	health := make([]sched.PathHealth, len(cfg.Paths))
	quality := make([]sched.PathQuality, len(cfg.Paths))
	for i := range cfg.Paths {
		probers[i] = newProber(cfg.Paths[i].Name, uint8(i), cfg.Paths[i].RideThrough)
		health[i] = probers[i]
		quality[i] = probers[i]
	}
	// Policy is a config choice: active-backup (P1, default) or the T21 weighted-
	// aggregation policy. Both consume the SAME per-path *Prober set — a *Prober
	// satisfies BOTH PathHealth (liveness) and PathQuality (RTT/loss Estimate) — so the
	// probe transport drives the very liveness/quality the scheduler selects on, and the
	// swap is behind config with no Bind change.
	scheduler, err := selectScheduler(cfg, health, quality, clock, lg)
	if err != nil {
		return nil, nil, nil, err
	}
	return scheduler, probers, newProber, nil
}

// selectScheduler builds the send scheduler the configured policy names, over the
// per-path health (and, for the weighted policy, quality) sources. active-backup is
// the P1 default; weighted is T21. The weighted knobs are validated at config load,
// so translating them here cannot fail on range — only NewWeighted's structural
// checks (which the wiring satisfies) apply.
func selectScheduler(cfg *config.Config, health []sched.PathHealth, quality []sched.PathQuality, clock telemetry.Clock, lg log.Logger) (sched.Scheduler, error) {
	switch cfg.Scheduler.Policy {
	case config.PolicyWeighted:
		sc := cfg.Scheduler
		return sched.NewWeighted(health, quality, sched.WeightedConfig{
			PerPathCapacity:   sc.PerPathCapacityFPS,
			EngageFraction:    sc.EngageFraction,
			DisengageFraction: sc.DisengageFraction,
			CollapseDwell:     sc.CollapseDwell,
			LoadTau:           sc.LoadTau,
			Pacing:            sc.PacingEnabled,
			PacingBurst:       sc.PacingBurstFrames,
			WeightRTTFloor:    sc.WeightRTTFloor,
			WeightLossFloor:   sc.WeightLossFloor,
		}, clock, lg)
	default:
		// active-backup (and the empty default, normalized to it at config load).
		// PerPathCapacities/PacingBursts are derived by config.derivePacingFromBDP
		// (T152) index-aligned to cfg.Paths; buildScheduler builds health over
		// cfg.Paths in that SAME order (no reorder/filter happens between there and
		// here), so the pacing vectors line up with health index-for-index.
		sc := cfg.Scheduler
		return sched.NewActiveBackup(
			health,
			sched.Config{
				FailbackAfter:     defaultFailbackDwell,
				Pacing:            sc.PacingEnabled,
				PerPathCapacities: sc.PerPathCapacities,
				PacingBursts:      sc.PacingBursts,
			},
			clock,
			lg,
		)
	}
}

// Name is the created TUN interface name (for external addressing/routing).
func (t *Tunnel) Name() string { return t.name }

// Wait blocks until the device is torn down (its own Close, or an unrecoverable
// engine error).
func (t *Tunnel) Wait() { <-t.dev.Wait() }

// Close stops the probe loop, brings the device down, and releases the TUN and
// Bind. Idempotent. It also releases this tunnel's hold on the single-amnezia-
// engine-per-process guard exactly once, so a later Up may reconfigure the
// process-global amnezia state. Probing is stopped BEFORE the engine tears the
// bind's sockets down so no emission races the close.
func (t *Tunnel) Close() {
	// Shut the /metrics scrape endpoint AND the [monitor] endpoint FIRST so no in-flight
	// scrape (or /ws push) reads the Bind while the engine tears its sockets down. reloadMu
	// serializes this against a concurrent SIGHUP-driven rebind.
	t.reloadMu.Lock()
	t.stopMetricsLocked()
	t.stopMonitorLocked()
	t.reloadMu.Unlock()
	if t.stopProbes != nil {
		t.stopProbes()
	}
	if t.stopReconcile != nil {
		t.stopReconcile()
	}
	if t.stopHubFailover != nil {
		t.stopHubFailover()
	}
	// Stop the DNS re-resolution loop between hub-failover and the engine teardown (T74): no
	// re-resolve may race the engine peer's install/repoint or the socket close.
	if t.stopResolution != nil {
		t.stopResolution()
	}
	// Stop the WG-session monitor before the engine teardown (I2): no IpcGet may race the
	// engine's Close.
	if t.stopSession != nil {
		t.stopSession()
	}
	// Stop the concentrator per-peer teardown monitor before the engine teardown (D50/T126): no
	// IpcGet — nor a TearDownPeer on the bind — may race the engine's Close. Inert for a
	// single-peer config, whose stopper is a no-op.
	if t.stopPeerTeardown != nil {
		t.stopPeerTeardown()
	}
	// Withdraw the default-route wiring (I6) BEFORE the engine teardown, while wanbond0
	// still exists: with tun_persist=true the interface survives Close, so its routes
	// must be explicitly removed here; with the default teardown dev.Close destroys the
	// interface (and the kernel drops its routes with it), so removeRoutes is idempotent
	// either way (it tolerates an already-gone device/route). A tunnel with no
	// default-route peer has an empty set and this is a no-op.
	if len(t.routePrefixes) > 0 {
		if err := removeRoutes(t.name, t.routePrefixes); err != nil {
			t.log.Warn("default-route teardown: could not remove routes", "error", err.Error())
		}
	}
	t.dev.Close()
	t.releaseOnce.Do(func() { globalAmneziaGuard.release(t.amnezia) })
}

// engineLogger adapts the amneziawg engine's logger onto wanbond's structured
// logger under a "wg" component. The engine is verbose only when the daemon runs
// at debug level; otherwise only its errors surface.
//
// everHadLivePath is the bind-level "ever had a live path" predicate (I4,
// bind.Multipath.EverHadLivePath): until the FIRST configured path reaches
// liveness up, the engine's "Failed to send handshake initiation: %w"
// (bind.ErrNoHealthyPath) is expected — every path starts Down and the boot-time
// probe race hasn't settled yet — so it would otherwise spam ERROR on every normal
// start. During that warmup window this adapter coalesces every such record into
// exactly ONE "waiting for path liveness" INFO line (not one per failed init) and
// emits none at ERROR; once everHadLivePath reports true it stops intercepting —
// the SAME error after a path has been up is a genuine outage and stays ERROR.
// Any OTHER Errorf record (not wrapping ErrNoHealthyPath) is unaffected and always
// logs at ERROR, warmup or not. Relates D37 (the wasted-first-init defect itself is
// investigate-flow-owned; this only fixes log severity).
func engineLogger(lg log.Logger, level string, everHadLivePath func() bool) *awgdevice.Logger {
	wg := lg.Component("wg")
	verbosef := func(string, ...any) {}
	if strings.EqualFold(strings.TrimSpace(level), "debug") {
		verbosef = func(format string, args ...any) { wg.Debug(fmt.Sprintf(format, args...)) }
	}
	var warmupInfoLogged atomic.Bool
	errorf := func(format string, args ...any) {
		if !everHadLivePath() && argsHaveNoHealthyPath(args) {
			if warmupInfoLogged.CompareAndSwap(false, true) {
				wg.Info("waiting for path liveness")
			}
			return
		}
		wg.Error(fmt.Sprintf(format, args...))
	}
	return &awgdevice.Logger{
		Verbosef: verbosef,
		Errorf:   errorf,
	}
}

// argsHaveNoHealthyPath reports whether any of an Errorf call's variadic args is (or
// wraps) bind.ErrNoHealthyPath. The engine passes the error value itself as a %v/%w
// arg (e.g. peer.go's "%v - Failed to send handshake initiation: %v", peer, err), so
// this checks the args directly with errors.Is rather than string-matching the
// formatted message, which would be fragile against wording changes.
func argsHaveNoHealthyPath(args []any) bool {
	for _, a := range args {
		if err, ok := a.(error); ok && errors.Is(err, bind.ErrNoHealthyPath) {
			return true
		}
	}
	return false
}

// bootEndpoint is a peer's initial engine endpoint derived from the bounded boot resolve: the
// flattened head of its seeded failoverSpecs, valid only when at least one spec has an expansion.
// An invalid boot endpoint means the peer boots endpoint-less (tolerant boot).
type bootEndpoint struct {
	ap    netip.AddrPort
	valid bool
}

// bootEndpoints is the outcome of the bounded initial resolve: the resolver constructed for
// hostname re-resolution (nil when no peer carries a hostname spec — the wiring is then provably
// inert, Q29), the per-peer seeded failoverSpecs (hostname expansions filled from the boot resolve,
// literals fixed), and the per-peer boot endpoint the UAPI render installs. specs and endpoints are
// indexed 1:1 with cfg.WireGuard.Peers.
type bootEndpoints struct {
	resolver  dnsresolve.Resolver
	specs     [][]failoverSpec
	endpoints []bootEndpoint
}

// resolveBootEndpoints performs the bounded initial hostname resolve (Q30) and derives, per peer,
// the seeded failoverSpecs and the boot endpoint the UAPI render installs. It constructs the
// resolver AT MOST ONCE, and ONLY when some peer carries a hostname endpoint spec — a config with
// zero hostname specs never invokes newResolver, so its wiring is provably inert (Q29). A literal
// spec is seeded verbatim (byte-for-byte the pre-G5 behaviour); a hostname spec is resolved under
// the [dns] per-lookup timeout and family-filtered. A lookup that fails, times out, or yields no
// usable record leaves that spec EMPTY — the peer boots endpoint-less and the re-resolution loop
// installs it on first success (R70). A resolver that fails to construct is logged and treated as
// absent (every hostname spec then boots empty), since re-resolution must never fail bring-up.
func resolveBootEndpoints(cfg *config.Config, newResolver resolverFactory, lg log.Logger) bootEndpoints {
	peers := cfg.WireGuard.Peers
	b := bootEndpoints{
		specs:     make([][]failoverSpec, len(peers)),
		endpoints: make([]bootEndpoint, len(peers)),
	}

	hasName := false
	for _, p := range peers {
		for _, s := range p.EndpointSpecs {
			if s.IsName {
				hasName = true
			}
		}
	}
	if hasName {
		if r, err := newResolver(); err != nil {
			lg.Warn("dns: could not construct resolver; hostname endpoints boot endpoint-less and will not re-resolve",
				"error", err.Error())
		} else {
			b.resolver = r
		}
	}

	fams := pathFamiliesFromPaths(cfg.Paths)
	for i, p := range peers {
		specs := make([]failoverSpec, len(p.EndpointSpecs))
		for j, s := range p.EndpointSpecs {
			if !s.IsName {
				specs[j] = failoverSpec{spec: s, addrs: []netip.AddrPort{s.Addr}}
				continue
			}
			var addrs []netip.AddrPort
			if b.resolver != nil {
				addrs = bootResolveHostname(b.resolver, s, fams, cfg.DNS.Timeout, lg)
			}
			specs[j] = failoverSpec{spec: s, addrs: addrs}
		}
		b.specs[i] = specs
		// The boot endpoint is the FLATTENED head — the first spec (in TOML order) with a
		// non-empty expansion. None ⇒ endpoint-less boot.
		for _, sp := range specs {
			if len(sp.addrs) > 0 {
				b.endpoints[i] = bootEndpoint{ap: sp.addrs[0], valid: true}
				break
			}
		}
	}
	return b
}

// bootResolveHostname does one context-bounded lookup for a hostname spec at boot and returns the
// ordered, family-filtered []netip.AddrPort (empty on any failure/timeout/empty-result — the caller
// then boots that spec endpoint-less). The timeout is the [dns] per-lookup timeout (kept > 0 by
// config validation); a non-positive value falls back to an unbounded context, matching
// resolution.lookupContext, but Up should never pass one.
func bootResolveHostname(resolver dnsresolve.Resolver, s config.EndpointSpec, fams pathFamilies, timeout time.Duration, lg log.Logger) []netip.AddrPort {
	ctx := context.Background()
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	addrs, _, _, err := resolver.Lookup(ctx, s.Host)
	if err != nil {
		lg.Info("dns: initial resolve failed; endpoint boots deferred to the re-resolution loop",
			"host", s.Host, "error", err.Error())
		return nil
	}
	eps := orderAddrPorts(addrs, s.Port, fams)
	if len(eps) == 0 {
		lg.Info("dns: initial resolve yielded no usable record for local path families; endpoint boots deferred",
			"host", s.Host)
		return nil
	}
	return eps
}

// uapiConfig renders cfg into the newline-delimited UAPI set string the engine's
// IpcSet consumes. Keys are lowercase hex (UAPI's on-the-wire encoding), NOT the
// base64 form the TOML carries. Amnezia obfuscation keys are emitted only when the
// block is configured; an all-zero Amnezia block leaves the engine in plain
// WireGuard mode. The same amnezia parameters are applied on BOTH roles (edge and
// concentrator) as defense-in-depth — they must match end to end for the handshake
// to succeed (config validation makes each end specify a complete profile, D1).
func uapiConfig(cfg *config.Config, bootEndpoints []bootEndpoint) (string, error) {
	var b strings.Builder

	priv := cfg.WireGuard.PrivateKey.Bytes()
	fmt.Fprintf(&b, "private_key=%s\n", hex.EncodeToString(priv[:]))
	if cfg.Role == config.RoleConcentrator {
		fmt.Fprintf(&b, "listen_port=%d\n", cfg.WireGuard.ListenPort)
	}
	writeAmnezia(&b, cfg.Amnezia)

	for i, peer := range cfg.WireGuard.Peers {
		pub := peer.PublicKey.Bytes()
		fmt.Fprintf(&b, "public_key=%s\n", hex.EncodeToString(pub[:]))
		// The engine peer's endpoint is built ONLY from a resolved flattened entry (Q30): the
		// boot endpoint is the head of the peer's seeded failoverSpecs — a literal's fixed address
		// for an all-literal peer (byte-for-byte the pre-G5 render, Endpoints[0]), or a hostname's
		// boot-resolved head. When nothing resolved (a hostname peer whose name did not resolve in
		// the boot window, or a resolver-less concentrator peer with no endpoint at all) the boot
		// endpoint is INVALID and no endpoint line is emitted — the peer boots endpoint-less
		// (tolerant boot), and for a hostname peer the re-resolution loop installs it on first
		// success (R70). Switching to a standby endpoint on hub loss is the failover controller's
		// job, not this initial render.
		if ep := bootEndpoints[i]; ep.valid {
			fmt.Fprintf(&b, "endpoint=%s\n", ep.ap.String())
			// A keepalive keeps the edge->concentrator session warm and lets the
			// concentrator relearn the edge endpoint after a NAT rebind; only the
			// initiating (edge) side sets it.
			fmt.Fprintf(&b, "persistent_keepalive_interval=%d\n", keepaliveSeconds)
		}
		if len(peer.AllowedIPs) == 0 {
			return "", fmt.Errorf("peer %d (%s): at least one allowed_ip is required", i, hex.EncodeToString(pub[:8]))
		}
		for _, cidr := range peer.AllowedIPs {
			for _, split := range splitDefaultRoute(cidr) {
				fmt.Fprintf(&b, "allowed_ip=%s\n", split)
			}
		}
	}
	return b.String(), nil
}

// splitDefaultRoute expands a literal 0.0.0.0/0 or ::/0 allowed_ip entry into
// its equivalent pair of /1 prefixes (D35, I6): amneziawg-go's engine wedges
// the handshake when handed the literal all-routes /0 prefix, so uapiConfig
// always renders the split form here and the engine never receives a literal
// /0 — regardless of the peer's mode. Any other prefix passes through
// unchanged; the parse-failure branch is defensive only, since config.validate()
// now parse-validates every allowed_ips entry at load (T132/D55), so an
// unparseable entry can no longer reach here.
func splitDefaultRoute(cidr string) []string {
	p, err := netip.ParsePrefix(cidr)
	if err != nil || p.Bits() != 0 {
		return []string{cidr}
	}
	if p.Addr().Is4() {
		return []string{"0.0.0.0/1", "128.0.0.0/1"}
	}
	return []string{"::/1", "8000::/1"}
}

// defaultRoutePrefixes computes the route set to install into wanbond0 for
// mode=default-route (I6, Q41): the wg-quick-style split of every default-route
// peer's allowed_ips (0.0.0.0/0 → the two /1s, ::/0 → its two /1s), REUSING the
// same splitDefaultRoute helper uapiConfig renders the engine allowed_ips with, so
// the installed routes match the internal split exactly. It returns nil unless some
// peer explicitly opts into default-route mode — the regression guard: a config
// without the mode installs no route at all. mode=default-route is edge-only (config
// validation rejects it on the concentrator), so this is naturally empty there. An
// allowed_ip that fails to parse is skipped — defensive only, since config.validate()
// now parse-validates every allowed_ips entry at load (T132/D55), so an unparseable
// entry can no longer reach here; the split entries always parse.
func defaultRoutePrefixes(cfg *config.Config) []netip.Prefix {
	var prefixes []netip.Prefix
	for _, peer := range cfg.WireGuard.Peers {
		if peer.Mode != config.PeerModeDefaultRoute {
			continue
		}
		for _, cidr := range peer.AllowedIPs {
			for _, split := range splitDefaultRoute(cidr) {
				p, err := netip.ParsePrefix(split)
				if err != nil {
					continue
				}
				prefixes = append(prefixes, p.Masked())
			}
		}
	}
	return prefixes
}

// keepaliveSeconds is the edge's persistent-keepalive interval.
const keepaliveSeconds = 25

// writeAmnezia emits the nine amneziawg obfuscation UAPI keys, but only when the
// block is configured. When amnezia is unused, wanbond leaves the engine's
// PROCESS-GLOBAL message-type state untouched (see the amneziaGuard invariant).
// Config validation guarantees a configured block is complete and self-consistent
// (D1), and applyDefaults has already filled the standard magic headers (1..4)
// when they were omitted, so the emitted set never carries an h*=0 sentinel.
func writeAmnezia(b *strings.Builder, a config.Amnezia) {
	if !a.Configured() {
		return
	}
	fmt.Fprintf(b, "jc=%d\njmin=%d\njmax=%d\ns1=%d\ns2=%d\nh1=%d\nh2=%d\nh3=%d\nh4=%d\n",
		a.Jc, a.Jmin, a.Jmax, a.S1, a.S2, a.H1, a.H2, a.H3, a.H4)
}
