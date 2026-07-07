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
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	awgdevice "github.com/amnezia-vpn/amneziawg-go/device"
	"github.com/amnezia-vpn/amneziawg-go/tun"

	"github.com/7mind/wanbond/internal/bind"
	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/log"
	"github.com/7mind/wanbond/internal/metrics"
	"github.com/7mind/wanbond/internal/sched"
	"github.com/7mind/wanbond/internal/telemetry"
)

// metricsShutdownTimeout bounds the graceful shutdown of the /metrics endpoint on
// Close (and on a reload that rebinds it). The endpoint is loopback-only with no
// long-lived scrapes, so this is comfortably generous.
const metricsShutdownTimeout = 2 * time.Second

// defaultTUNName is the requested interface name; the kernel honours it unless it
// collides (it never does across the edge and concentrator network namespaces).
const defaultTUNName = "wanbond0"

// defaultMTU is the tunnel (TUN) MTU. It is the P1 bonded figure: the default
// path MTU minus the IP+UDP, outer DATA-frame, and WireGuard transport overheads,
// so a full-MTU inner packet never fragments on the wire (see internal/bind
// mtu.go and docs/p1-mtu.md). This is smaller than the plain-WireGuard 1420
// because the bonding layer adds its own outer DATA frame per datagram.
var defaultMTU = bind.InnerMTU(bind.DefaultPathMTU)

// Tunnel is a running wanbond tunnel: the amneziawg engine, its TUN, and the
// multipath Bind. Close tears all three down.
type Tunnel struct {
	dev  *awgdevice.Device
	tun  tun.Device
	name string
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

	tunDev, err := tun.CreateTUN(defaultTUNName, defaultMTU)
	if err != nil {
		return nil, fmt.Errorf("device: create TUN: %w", err)
	}
	name, err := tunDev.Name()
	if err != nil {
		_ = tunDev.Close()
		return nil, fmt.Errorf("device: read TUN name: %w", err)
	}

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

	// One live *telemetry.Prober per path drives on-wire liveness. The SAME prober
	// values back the scheduler's PathHealth AND the bind's probe transport, so the
	// liveness the probe loop measures is exactly the liveness the scheduler selects
	// on (T37 replaces the sched.AlwaysUp placeholder here).
	scheduler, probers, newProber, err := buildScheduler(cfg, clg)
	if err != nil {
		_ = tunDev.Close()
		return nil, fmt.Errorf("device: build scheduler: %w", err)
	}
	mpBind, err := bind.NewMultipath(cfg.Paths, cfg.PSK, scheduler, probers, newProber)
	if err != nil {
		_ = tunDev.Close()
		return nil, fmt.Errorf("device: build multipath bind: %w", err)
	}
	dev := awgdevice.NewDevice(tunDev, mpBind, engineLogger(clg, cfg.Log.Level))

	uapi, err := uapiConfig(cfg)
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

	// The engine has opened the bind (dev.Up → BindUpdate → Open), so the per-path
	// sockets exist: start the probe cadence now. Close stops it before dev.Close.
	// The interval is the SINGLE-SOURCE-OF-TRUTH telemetry default, which also arms
	// the bind's receive-path liveness sweep throttle (D15).
	stopProbes := mpBind.StartProbeLoop(telemetry.DefaultProbeInterval)

	t := &Tunnel{
		dev: dev, tun: tunDev, name: name, bind: mpBind, cfg: cfg, log: clg,
		stopProbes: stopProbes, amnezia: cfg.Amnezia,
		// The Source reads live per-path counters/telemetry from the Bind and derives
		// throughput from the byte-counter delta between scrapes (see metricsSource). It is
		// built unconditionally (cheap) so a reload that later turns [metrics].listen ON has
		// a Source ready; the endpoint itself is started only when a listen is configured.
		metricsSrc: newMetricsSource(mpBind, telemetry.SystemClock{}),
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

	ok = true
	clg.Info("tunnel up", "interface", name, "role", string(cfg.Role))
	return t, nil
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
	srv, err := metrics.NewServer(listen, t.metricsSrc, t.log)
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
	// Advance the running config to the membership now in service. Survivors keep their
	// ORIGINAL parameters and all non-path fields stay as booted (the ignored changes
	// were not applied), so a subsequent identical reload re-warns about a still-diverged
	// file rather than silently accepting it. Metrics.Listen is carried to the applied
	// value so a subsequent reload diffs against the endpoint actually running.
	t.cfg = runningConfig(t.cfg, add, remove)
	t.cfg.Metrics.Listen = t.metricsListen
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
	// NOTE: a Metrics change is NOT warned here — unlike the other non-path fields, the
	// reload APPLIES it by rebinding the /metrics endpoint (see Reload). Warning about a
	// change that is honoured would misinform the operator.

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
	}

	if reordered(live.Paths, desired.Paths) {
		w = append(w, "path priority order changed (reorder) — the running order is unchanged")
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

// buildScheduler constructs one live *telemetry.Prober per path and the P1
// active-backup send scheduler over them, in cfg.Paths' configured priority order
// (index 0 = the preferred primary). The returned probers ARE the scheduler's
// PathHealth sources (a *Prober is internally synchronized, satisfying the
// PathHealth concurrency contract — a bare *Liveness would not) and are handed to
// the bind so the probe transport drives the very same liveness the scheduler
// selects on. This replaces the T15 sched.AlwaysUp placeholder with real on-wire
// failover (T37).
func buildScheduler(cfg *config.Config, lg log.Logger) (sched.Scheduler, []*telemetry.Prober, bind.ProberFactory, error) {
	clock := telemetry.SystemClock{}
	proberCfg := telemetry.ProberConfig{
		LossWindow: telemetry.DefaultLossWindow,
		Liveness: telemetry.LivenessConfig{
			DownAfter:        telemetry.DefaultDownAfter,
			UpAfterSuccesses: telemetry.DefaultUpSuccesses,
		},
	}
	// One random per-boot session id shared by every path's Prober (it identifies
	// this boot, not the path): a peer restart presents a new session id that resets
	// the surviving responder's anti-replay high-water so liveness recovers (T38, D12).
	// A runtime-added path (T30) reuses the SAME session id, so its probes join this
	// boot's stream and the peer's reflector adopts them without a challenge reset.
	sessionID, err := telemetry.NewSessionID(rand.Reader)
	if err != nil {
		return nil, nil, nil, err
	}
	// newProber mints one path's Prober with the shared session/config/clock. It is
	// the single construction point for boot-time AND runtime paths, so both measure
	// liveness identically.
	newProber := func(name string, id uint8) *telemetry.Prober {
		return telemetry.NewProber(name, id, sessionID, cfg.PSK, proberCfg, clock, lg)
	}
	probers := make([]*telemetry.Prober, len(cfg.Paths))
	health := make([]sched.PathHealth, len(cfg.Paths))
	quality := make([]sched.PathQuality, len(cfg.Paths))
	for i := range cfg.Paths {
		probers[i] = newProber(cfg.Paths[i].Name, uint8(i))
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
		return sched.NewActiveBackup(
			health,
			sched.Config{FailbackAfter: defaultFailbackDwell},
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
	// Shut the scrape endpoint FIRST so no in-flight /metrics scrape reads the Bind while
	// the engine tears its sockets down. reloadMu serializes this against a concurrent
	// SIGHUP-driven rebind.
	t.reloadMu.Lock()
	t.stopMetricsLocked()
	t.reloadMu.Unlock()
	if t.stopProbes != nil {
		t.stopProbes()
	}
	t.dev.Close()
	t.releaseOnce.Do(func() { globalAmneziaGuard.release(t.amnezia) })
}

// engineLogger adapts the amneziawg engine's logger onto wanbond's structured
// logger under a "wg" component. The engine is verbose only when the daemon runs
// at debug level; otherwise only its errors surface.
func engineLogger(lg log.Logger, level string) *awgdevice.Logger {
	wg := lg.Component("wg")
	verbosef := func(string, ...any) {}
	if strings.EqualFold(strings.TrimSpace(level), "debug") {
		verbosef = func(format string, args ...any) { wg.Debug(fmt.Sprintf(format, args...)) }
	}
	return &awgdevice.Logger{
		Verbosef: verbosef,
		Errorf:   func(format string, args ...any) { wg.Error(fmt.Sprintf(format, args...)) },
	}
}

// uapiConfig renders cfg into the newline-delimited UAPI set string the engine's
// IpcSet consumes. Keys are lowercase hex (UAPI's on-the-wire encoding), NOT the
// base64 form the TOML carries. Amnezia obfuscation keys are emitted only when the
// block is configured; an all-zero Amnezia block leaves the engine in plain
// WireGuard mode. The same amnezia parameters are applied on BOTH roles (edge and
// concentrator) as defense-in-depth — they must match end to end for the handshake
// to succeed (config validation makes each end specify a complete profile, D1).
func uapiConfig(cfg *config.Config) (string, error) {
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
		if peer.Endpoint != "" {
			fmt.Fprintf(&b, "endpoint=%s\n", peer.Endpoint)
			// A keepalive keeps the edge->concentrator session warm and lets the
			// concentrator relearn the edge endpoint after a NAT rebind; only the
			// initiating (edge) side sets it.
			fmt.Fprintf(&b, "persistent_keepalive_interval=%d\n", keepaliveSeconds)
		}
		if len(peer.AllowedIPs) == 0 {
			return "", fmt.Errorf("peer %d (%s): at least one allowed_ip is required", i, hex.EncodeToString(pub[:8]))
		}
		for _, cidr := range peer.AllowedIPs {
			fmt.Fprintf(&b, "allowed_ip=%s\n", cidr)
		}
	}
	return b.String(), nil
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
