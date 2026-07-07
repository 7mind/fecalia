// Package device brings a wanbond tunnel up from a validated configuration: it
// creates the TUN interface, drives the embedded amneziawg-go engine over the
// multipath Bind, and applies the WireGuard (and, when configured, amnezia
// obfuscation) parameters via the engine's UAPI. It owns ONLY the tunnel engine
// — interface addressing and routing are left to the operator (systemd/wg-quick
// style), so the daemon stays free of privileged shell-outs. The interface name
// is exposed via Tunnel.Name for that external configuration step.
package device

import (
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	awgdevice "github.com/amnezia-vpn/amneziawg-go/device"
	"github.com/amnezia-vpn/amneziawg-go/tun"

	"github.com/7mind/wanbond/internal/bind"
	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/log"
	"github.com/7mind/wanbond/internal/sched"
	"github.com/7mind/wanbond/internal/telemetry"
)

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
	// stopProbes halts the per-path probe cadence loop (T37). Close calls it before
	// tearing the engine down so emission stops before the sockets close. It is
	// idempotent and never nil (a no-op when the bind runs without probers).
	stopProbes func()
	// amnezia is the obfuscation profile this tunnel holds against the
	// process-global amnezia guard (see amneziaGuard); Close releases it.
	amnezia     config.Amnezia
	releaseOnce sync.Once
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
	scheduler, probers, err := buildScheduler(cfg, clg)
	if err != nil {
		_ = tunDev.Close()
		return nil, fmt.Errorf("device: build scheduler: %w", err)
	}
	mpBind, err := bind.NewMultipath(cfg.Paths, cfg.PSK, scheduler, probers)
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
	stopProbes := mpBind.StartProbeLoop(defaultProbeInterval)

	ok = true
	clg.Info("tunnel up", "interface", name, "role", string(cfg.Role))
	return &Tunnel{dev: dev, tun: tunDev, name: name, stopProbes: stopProbes, amnezia: cfg.Amnezia}, nil
}

// defaultFailbackDwell is how long a recovered higher-priority path must stay up
// before egress fails BACK to it, damping flap-induced thrash (T15 hysteresis).
const defaultFailbackDwell = 5 * time.Second

// Probe cadence and liveness detection defaults (T13/T37). The interval is the
// PROBE emission period; DownAfter is the silence that marks an up path down and
// UpAfterSuccesses the consecutive echoes that bring a down path up. DownAfter is
// several intervals so a single lost echo never flaps a path, and detection
// latency stays within DownAfter plus one interval. LossWindow=0 takes the
// estimator's default trailing window.
const (
	defaultProbeInterval    = 250 * time.Millisecond
	defaultProbeDownAfter   = 1500 * time.Millisecond
	defaultProbeUpSuccesses = 3
	defaultProbeLossWindow  = 0
)

// buildScheduler constructs one live *telemetry.Prober per path and the P1
// active-backup send scheduler over them, in cfg.Paths' configured priority order
// (index 0 = the preferred primary). The returned probers ARE the scheduler's
// PathHealth sources (a *Prober is internally synchronized, satisfying the
// PathHealth concurrency contract — a bare *Liveness would not) and are handed to
// the bind so the probe transport drives the very same liveness the scheduler
// selects on. This replaces the T15 sched.AlwaysUp placeholder with real on-wire
// failover (T37).
func buildScheduler(cfg *config.Config, lg log.Logger) (sched.Scheduler, []*telemetry.Prober, error) {
	clock := telemetry.SystemClock{}
	proberCfg := telemetry.ProberConfig{
		LossWindow: defaultProbeLossWindow,
		Liveness: telemetry.LivenessConfig{
			DownAfter:        defaultProbeDownAfter,
			UpAfterSuccesses: defaultProbeUpSuccesses,
		},
	}
	probers := make([]*telemetry.Prober, len(cfg.Paths))
	health := make([]sched.PathHealth, len(cfg.Paths))
	for i := range cfg.Paths {
		probers[i] = telemetry.NewProber(cfg.Paths[i].Name, uint8(i), cfg.PSK, proberCfg, clock, lg)
		health[i] = probers[i]
	}
	scheduler, err := sched.NewActiveBackup(
		health,
		sched.Config{FailbackAfter: defaultFailbackDwell},
		clock,
		lg,
	)
	if err != nil {
		return nil, nil, err
	}
	return scheduler, probers, nil
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
