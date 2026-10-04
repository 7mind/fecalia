package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/sys/unix"

	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/monitor"
)

const (
	monitorDialTimeout  = 5 * time.Second
	monitorFrameTimeout = 5 * time.Second
	monitorHeadingColor = "1;36"
	monitorUpColor      = "32"
	monitorDownColor    = "31"
	monitorIdleColor    = "33"
)

func runMonitor(args []string, out *os.File) error {
	fs := flag.NewFlagSet("wanbond monitor", flag.ContinueOnError)
	configPath := fs.String("config", "", "path to the running daemon's TOML config")
	once := fs.Bool("once", false, "print one snapshot and exit")
	noColor := fs.Bool("no-color", false, "disable ANSI colors")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("monitor: unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	endpoint, err := resolveMonitorEndpoint(*configPath)
	if err != nil {
		return err
	}
	_, termErr := unix.IoctlGetTermios(int(out.Fd()), unix.TCGETS)
	terminal := termErr == nil
	if !*once {
		if !terminal {
			return fmt.Errorf("monitor: terminal required; use --once for redirected output")
		}
	}
	_, noColorEnv := os.LookupEnv("NO_COLOR")
	color := monitorColorEnabled(terminal, os.Getenv("TERM"), *noColor, noColorEnv)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return streamMonitor(ctx, endpoint.addr, endpoint.token, *once, color, out)
}

// monitorEndpoint is the local address and bearer token of a running daemon's
// [monitor] endpoint, as read from its config.
type monitorEndpoint struct {
	addr  string
	token string
}

// defaultMonitorConfigs are the config locations probed when --config is absent.
var defaultMonitorConfigs = []string{
	"/run/wanbond/edge.toml",
	"/run/wanbond/concentrator.toml",
	"/etc/wanbond/config.toml",
}

// resolveMonitorEndpoint loads configPath (or the single discovered default
// config when it is empty) and returns the daemon's local monitor endpoint.
func resolveMonitorEndpoint(configPath string) (monitorEndpoint, error) {
	path := configPath
	if path == "" {
		var err error
		path, err = findMonitorConfig(defaultMonitorConfigs)
		if err != nil {
			return monitorEndpoint{}, err
		}
	}
	cfg, err := config.Load(path)
	if err != nil {
		return monitorEndpoint{}, fmt.Errorf("monitor: load %s: %w", path, err)
	}
	if cfg.Monitor.Listen == "" {
		return monitorEndpoint{}, fmt.Errorf("monitor: [monitor].listen is disabled in %s", path)
	}
	addr, err := localMonitorAddress(cfg.Monitor.Listen)
	if err != nil {
		return monitorEndpoint{}, err
	}
	return monitorEndpoint{addr: addr, token: cfg.Monitor.Token}, nil
}

func monitorColorEnabled(terminal bool, term string, noColorFlag, noColorEnv bool) bool {
	return terminal && term != "" && term != "dumb" && !noColorFlag && !noColorEnv
}

func streamMonitor(ctx context.Context, addr, token string, once, color bool, out io.Writer) error {
	dialCtx, cancel := context.WithTimeout(ctx, monitorDialTimeout)
	defer cancel()
	options := &websocket.DialOptions{HTTPHeader: make(http.Header)}
	if token != "" {
		options.HTTPHeader.Set("Authorization", "Bearer "+token)
	}
	conn, response, err := websocket.Dial(dialCtx, "ws://"+addr+"/ws", options)
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if err != nil {
		return fmt.Errorf("monitor: connect to %s: %w", addr, err)
	}
	defer func() { _ = conn.CloseNow() }()
	if !once {
		fmt.Fprint(out, "\x1b[?1049h\x1b[?25l")
		defer fmt.Fprint(out, "\x1b[?25h\x1b[?1049l")
	}
	for {
		readCtx, cancel := context.WithTimeout(ctx, monitorFrameTimeout)
		_, data, err := conn.Read(readCtx)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("monitor: read snapshot: %w", err)
		}
		var snapshot monitor.MonitorSnapshot
		if err := json.Unmarshal(data, &snapshot); err != nil {
			return fmt.Errorf("monitor: decode snapshot: %w", err)
		}
		if once {
			_, err = io.WriteString(out, renderMonitor(snapshot, time.Now(), false, color)+"\n")
			return err
		}
		if _, err := io.WriteString(out, "\x1b[H\x1b[2J"+renderMonitor(snapshot, time.Now(), true, color)+"\n"); err != nil {
			return err
		}
	}
}

func findMonitorConfig(paths []string) (string, error) {
	var found string
	for _, path := range paths {
		_, err := os.Stat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("monitor: inspect %s: %w", path, err)
		}
		if found != "" {
			return "", fmt.Errorf("monitor: multiple configs found (%s, %s); pass --config", found, path)
		}
		found = path
	}
	if found == "" {
		return "", fmt.Errorf("monitor: no config found in %s; pass --config", strings.Join(paths, ", "))
	}
	return found, nil
}

func localMonitorAddress(listen string) (string, error) {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "", fmt.Errorf("monitor: invalid listen address %q: %w", listen, err)
	}
	if host == "::" {
		host = "::1"
	} else if host == "" || host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port), nil
}

func renderMonitor(s monitor.MonitorSnapshot, now time.Time, interactive, color bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s  %s  v%s  up %s\n", monitorStyle("wanbond", monitorHeadingColor, color), s.Daemon.Role, s.Daemon.Version, (time.Duration(s.Daemon.UptimeSeconds) * time.Second).Truncate(time.Second))
	fmt.Fprintf(&b, "Updated %s", now.Format("15:04:05"))
	if interactive {
		fmt.Fprint(&b, "    Ctrl+C to quit")
	}
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, strings.Repeat("─", 76))
	session := "DOWN"
	if s.Session.Established {
		session = fmt.Sprintf("UP · handshake %.0fs ago", s.Session.LastHandshakeSeconds)
	}
	session = monitorStatus(session, s.Session.Established, color)
	fmt.Fprintf(&b, "WireGuard  %s    Key %s\n", session, s.WGPublicKeyFingerprint)
	if s.Daemon.Role == "edge" && len(s.ExitCapablePeers) > 0 {
		fmt.Fprintf(&b, "Exit       %s    Policy %s\n", s.ActiveExit, s.ExitMode)
	}
	if len(s.PeerSessions) > 0 {
		fmt.Fprintln(&b, "\n"+monitorStyle("PEERS", monitorHeadingColor, color))
		for _, p := range s.PeerSessions {
			state := "DOWN"
			if p.Established {
				state = fmt.Sprintf("UP · handshake %.0fs ago", p.LastHandshakeSeconds)
			}
			fmt.Fprintf(&b, "  %-18s %s\n", peerName(p.Peer), monitorStatus(state, p.Established, color))
		}
	}
	fmt.Fprintln(&b, "\n"+monitorStyle("PATHS", monitorHeadingColor, color))
	fmt.Fprintln(&b, "  PEER / PATH                 STATE    RTT      LOSS     JITTER")
	for _, p := range s.Paths {
		state := "DOWN"
		if p.Up {
			state = "UP"
		}
		name := p.Name
		if p.Peer != "" {
			name = p.Peer + " / " + name
		}
		fmt.Fprintf(&b, "  %-27.27s %s %6.1fms  %5.1f%%  %6.1fms\n",
			name, monitorStatus(fmt.Sprintf("%-7s", state), p.Up, color), p.RTTSeconds*1000, p.Loss*100, p.JitterSeconds*1000)
		fmt.Fprintf(&b, "    rate %-11s tx %-11s rx %s\n",
			formatRate(p.ThroughputBps/8), formatBytes(p.TxBytes), formatBytes(p.RxBytes))
		if p.Addressing != nil {
			fmt.Fprintf(&b, "    source %s  remote %s\n", p.Addressing.Source, p.Addressing.Remote)
		}
	}
	if len(s.Lanes) > 0 {
		fmt.Fprintln(&b, "\n"+monitorStyle("LANES", monitorHeadingColor, color))
		fmt.Fprintln(&b, "  PEER / PATH #REMOTE         STATE    TARGET      SENT        CAPACITY")
		for _, l := range s.Lanes {
			name := fmt.Sprintf("%s #%d", l.Path, l.RemotePath)
			if l.Peer != "" {
				name = l.Peer + " / " + name
			}
			state := "HOLD"
			switch {
			case !l.Up:
				state = "DOWN"
			case l.Discovering:
				state = "PROBING"
			}
			capacity := "unknown"
			if l.CapacityBps > 0 {
				capacity = formatRate(l.CapacityBps / 8)
			}
			fmt.Fprintf(&b, "  %-27.27s %s %-11s %-11s %s\n",
				name, monitorStatus(fmt.Sprintf("%-7s", state), l.Up, color), formatRate(l.TargetBps/8), formatRate(l.SendBps/8), capacity)
			fmt.Fprintf(&b, "    delivered %-11s queue %.0fms of %.0fms  rtt %.0fms  in flight %s of %s\n",
				formatRate(l.DeliveryBps/8), l.QueueDelaySeconds*1000, l.ThresholdSeconds*1000, l.RTTSeconds*1000, formatBytes(uint64(l.InFlightBytes)), formatBytes(uint64(l.WindowBytes)))
			fmt.Fprintf(&b, "    model %s  floor %.1fms (known %t, age %.1fs)  path delay %.1fms  rank %.1fms  ACK progress known %t, age %.1fs\n", l.Liveness, l.TransitFloorSeconds*1000, l.TransitFloorKnown, l.TransitFloorAgeSeconds, l.PathDelaySeconds*1000, l.RankSeconds*1000, l.ACKProgressKnown, l.LivenessAgeSeconds)
			fmt.Fprintf(&b, "    signals delay %d loss %d stall %d  probes %d won %d lost %d  estimate remeasured %d decayed %d  repairs %d\n",
				l.DelaySignals, l.LossSignals, l.StallSignals, l.Pulses, l.PulseWins, l.PulseLosses, l.CapacityRemeasured, l.CapacityDecays, l.Repairs)
		}
	}
	if len(s.Transport) > 0 {
		fmt.Fprintln(&b, "\n"+monitorStyle("TRANSPORT QUEUE", monitorHeadingColor, color))
		for _, q := range s.Transport {
			fmt.Fprintf(&b, "  %-18s dropped %d (full %d, aqm %d, small %d)  expired %d  duplicates %d\n",
				peerName(q.Peer), q.QueueDrops, q.AdmissionDrops, q.AQMDrops, q.InteractiveDrops, q.Expired, q.Duplicates)
			for _, rejected := range q.RejectedFrames {
				fmt.Fprintf(&b, "    rejected %-10s %d\n", rejected.Cause, rejected.Count)
			}
		}
	}
	if len(s.Reseq) > 0 {
		fmt.Fprintln(&b, "\n"+monitorStyle("RESEQUENCER", monitorHeadingColor, color))
		for _, r := range s.Reseq {
			fmt.Fprintf(&b, "  %-18s released %d  skipped %d  dup %d  old %d  resync %d\n",
				peerName(r.Peer), r.Released, r.Skipped, r.DroppedDup, r.DroppedOld, r.Resyncs)
		}
	}
	if len(s.Endpoints) > 0 {
		fmt.Fprintln(&b, "\n"+monitorStyle("ENDPOINTS", monitorHeadingColor, color))
		for _, e := range s.Endpoints {
			state := "standby"
			if e.Active {
				state = "active"
			}
			address := e.Address
			if s.AddressingHidden {
				address = "hidden"
			}
			state = fmt.Sprintf("%-7s", state)
			if e.Active {
				state = monitorStyle(state, monitorUpColor, color)
			} else {
				state = monitorStyle(state, monitorIdleColor, color)
			}
			fmt.Fprintf(&b, "  %-18s %s %s\n", peerName(e.Peer), state, address)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func monitorStatus(state string, up, color bool) string {
	if up {
		return monitorStyle(state, monitorUpColor, color)
	}
	return monitorStyle(state, monitorDownColor, color)
}

func monitorStyle(value, code string, color bool) string {
	if !color {
		return value
	}
	return "\x1b[" + code + "m" + value + "\x1b[0m"
}

func peerName(name string) string {
	if name == "" {
		return "default"
	}
	return name
}

func formatRate(bytesPerSecond float64) string {
	return formatBytes(uint64(bytesPerSecond)) + "/s"
}

func formatBytes(bytes uint64) string {
	units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	amount := float64(bytes)
	unit := 0
	for amount >= 1024 && unit < len(units)-1 {
		amount /= 1024
		unit++
	}
	if unit == 0 {
		return fmt.Sprintf("%d%s", bytes, units[unit])
	}
	return fmt.Sprintf("%.1f%s", amount, units[unit])
}
