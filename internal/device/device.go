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

	scheduler, err := buildScheduler(cfg, clg)
	if err != nil {
		_ = tunDev.Close()
		return nil, fmt.Errorf("device: build scheduler: %w", err)
	}
	mpBind, err := bind.NewMultipath(cfg.Paths, cfg.PSK, scheduler)
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

	clg.Info("tunnel up", "interface", name, "role", string(cfg.Role))
	return &Tunnel{dev: dev, tun: tunDev, name: name}, nil
}

// defaultFailbackDwell is how long a recovered higher-priority path must stay up
// before egress fails BACK to it, damping flap-induced thrash (T15 hysteresis).
const defaultFailbackDwell = 5 * time.Second

// buildScheduler constructs the P1 active-backup send scheduler over cfg.Paths in
// their configured priority order (index 0 = the preferred primary). Real
// per-path liveness arrives once the probe-transport wiring lands (a follow-up
// task drives *telemetry.Prober per path); until then every path is reported
// statically Up, so the active-backup scheduler keeps all egress on the primary —
// the data-thrift bring-up behaviour — and swapping the AlwaysUp sources for live
// Probers activates failover with no further Bind or scheduler change.
func buildScheduler(cfg *config.Config, lg log.Logger) (sched.Scheduler, error) {
	health := make([]sched.PathHealth, len(cfg.Paths))
	for i := range health {
		health[i] = sched.AlwaysUp{}
	}
	return sched.NewActiveBackup(
		health,
		sched.Config{FailbackAfter: defaultFailbackDwell},
		telemetry.SystemClock{},
		lg,
	)
}

// Name is the created TUN interface name (for external addressing/routing).
func (t *Tunnel) Name() string { return t.name }

// Wait blocks until the device is torn down (its own Close, or an unrecoverable
// engine error).
func (t *Tunnel) Wait() { <-t.dev.Wait() }

// Close brings the device down and releases the TUN and Bind. Idempotent.
func (t *Tunnel) Close() { t.dev.Close() }

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
// base64 form the TOML carries. Amnezia obfuscation keys are emitted only when at
// least one is configured; an all-zero Amnezia block leaves the engine in plain
// WireGuard mode (amnezia parameters are wired end-to-end in a later phase).
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

// writeAmnezia emits the amneziawg obfuscation UAPI keys, but only when the block
// is configured (any non-zero field). Emitting an all-zero block would NOT break
// the handshake — the engine treats magic-header values <= 4 as "use the default
// message types" and leaves obfuscation off when no junk/size field is set. The
// guard's purpose is to avoid needlessly driving the engine's UAPI amnezia path,
// which assigns amneziawg's PROCESS-GLOBAL message-type state on every configured
// apply (a single-engine-per-process constraint tracked for the T19 amnezia
// wiring); when amnezia is unused, wanbond leaves that global state untouched.
func writeAmnezia(b *strings.Builder, a config.Amnezia) {
	configured := a.Jc != 0 || a.Jmin != 0 || a.Jmax != 0 || a.S1 != 0 || a.S2 != 0 ||
		a.H1 != 0 || a.H2 != 0 || a.H3 != 0 || a.H4 != 0
	if !configured {
		return
	}
	fmt.Fprintf(b, "jc=%d\njmin=%d\njmax=%d\ns1=%d\ns2=%d\nh1=%d\nh2=%d\nh3=%d\nh4=%d\n",
		a.Jc, a.Jmin, a.Jmax, a.S1, a.S2, a.H1, a.H2, a.H3, a.H4)
}
