//go:build realhosts

package realhosts

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// P0 smoke overlay addressing and wire parameters. The inner /24 mirrors the
// netns e2e fixture (test/e2e/p0_test.go): edge=10.10.0.1, concentrator=10.10.0.2.
const (
	smokeEdgeInner  = "10.10.0.1"
	smokeConcInner  = "10.10.0.2"
	smokeInnerCIDR  = "24"
	smokeListenPort = 51820

	// smokeRemoteDir is where the repo is synced and the native wanbond binary is
	// built on each host. Both hosts share the login (defaultUser) home layout.
	smokeRemoteDir = "/home/" + defaultUser + "/wanbond-smoke"
	smokeBin       = smokeRemoteDir + "/wanbond"

	// smokeUnit is the transient systemd unit the wanbond daemon runs under on
	// each host; iperfUnit is the concentrator's iperf3 server unit. Distinct
	// machines, so the daemon unit name may repeat across hosts.
	smokeUnit = "wanbond-smoke"
	iperfUnit = "wanbond-iperf3"
)

// Timeouts. The tier is report-only but drives real internet round-trips and a
// native Go build, so budgets are generous.
const (
	smokeSSHTimeout   = 60 * time.Second
	smokeBuildTimeout = 5 * time.Minute
	smokeSyncTimeout  = 3 * time.Minute
	linkAppearTimeout = 20 * time.Second
	handshakeTimeout  = 45 * time.Second
	iperfRunTimeout   = 60 * time.Second
	cleanupTimeout    = 30 * time.Second
)

// TestRealP0Smoke codifies the manually validated single-uplink P0 flow against
// the two standing worker machines: provision both hosts, natively build
// wanbond on each, bring a plain-WireGuard tunnel up over the real internet
// path (edge behind symmetric NAT -> concentrator public IP), and record the
// handshake, ping RTT, and three iperf3 measurements (single TCP, 8x-parallel
// TCP, UDP goodput/loss). REPORT-ONLY per Q12: executing and recording IS the
// acceptance; no threshold gates it. Every remote resource it starts is torn
// down on exit, including on failure, via t.Cleanup.
func TestRealP0Smoke(t *testing.T) {
	cfg := LoadConfig()
	r := NewRunner(cfg)

	t.Logf("realhosts config: edge=%s concentrator=%s conc-public-ip=%s ssh-key=%s",
		cfg.Edge.target(), cfg.Conc.target(), cfg.ConcPubIP, cfg.SSHKey)

	// 1. Provision both hosts (idempotent: a no-op on an already-provisioned host).
	//    The concentrator additionally gets the tunnel-interface INPUT ACCEPT rule.
	provision(t, r, cfg.Edge, ProvisionOpts{})
	provision(t, r, cfg.Conc, ProvisionOpts{TunnelIface: tunnelIface})

	// 2. Sync the repo and build wanbond natively on each host (arch matches host).
	//    Register removal of smokeRemoteDir immediately after each sync so the
	//    synced repo, the native binary, and the secret configs written into it in
	//    step 5 are torn down on exit — including on failure/panic. Key material
	//    must not outlive the tunnel it secures.
	root := repoRoot(t)
	syncAndBuild(t, r, cfg.Edge, root)
	t.Cleanup(func() { removeRemoteDir(t, r, cfg.Edge) })
	syncAndBuild(t, r, cfg.Conc, root)
	t.Cleanup(func() { removeRemoteDir(t, r, cfg.Conc) })

	// 3. Key material: an X25519 keypair per end plus a shared PSK (base64), same
	//    scheme as the netns e2e fixture.
	edgePriv, edgePub := genSmokeKey(t)
	concPriv, concPub := genSmokeKey(t)
	psk := randSmokeKey(t)

	// 4. Each path binds its socket to the host's primary source IP.
	edgeSrc := primaryIP(t, r, cfg.Edge)
	concSrc := primaryIP(t, r, cfg.Conc)
	t.Logf("path source addrs: edge=%s concentrator=%s", edgeSrc, concSrc)

	// 5. Write the 0600 configs (Amnezia omitted -> plain WireGuard). The
	//    concentrator listens; the edge dials the concentrator's PUBLIC IP.
	concCfg := fmt.Sprintf(`role = "concentrator"
psk = "%s"

[[paths]]
name = "wan0"
source_addr = "%s"

[wireguard]
private_key = "%s"
listen_port = %d

[[wireguard.peers]]
public_key = "%s"
allowed_ips = ["%s/32"]

[log]
level = "info"
`, psk, concSrc, concPriv, smokeListenPort, edgePub, smokeEdgeInner)

	edgeCfg := fmt.Sprintf(`role = "edge"
psk = "%s"

[[paths]]
name = "wan0"
source_addr = "%s"

[wireguard]
private_key = "%s"

[[wireguard.peers]]
public_key = "%s"
endpoint = "%s:%d"
allowed_ips = ["%s/32"]

[log]
level = "info"
`, psk, edgeSrc, edgePriv, concPub, cfg.ConcPubIP, smokeListenPort, smokeConcInner)

	concCfgPath := smokeRemoteDir + "/conc.toml"
	edgeCfgPath := smokeRemoteDir + "/edge.toml"
	writeRemoteFile(t, r, cfg.Conc, concCfgPath, concCfg)
	writeRemoteFile(t, r, cfg.Edge, edgeCfgPath, edgeCfg)

	// 6. Clear any leftover unit/interface from a prior interrupted run, then
	//    start the concentrator first (it must be listening before the edge
	//    initiates), then the edge, each as a transient systemd unit for clean
	//    detachment. Register teardown immediately so a later failure still stops
	//    the daemon and removes the interface.
	preClean(t, r, cfg.Conc)
	preClean(t, r, cfg.Edge)
	startDaemon(t, r, cfg.Conc, concCfgPath)
	startDaemon(t, r, cfg.Edge, edgeCfgPath)

	// 7. Wait for each TUN to appear, then address it (wanbond owns the engine
	//    only; addressing is the operator's job).
	if !waitRemoteLink(t, r, cfg.Conc, tunnelIface, linkAppearTimeout) {
		dumpDaemonLog(t, r, cfg.Conc)
		t.Fatalf("concentrator %s never appeared", tunnelIface)
	}
	if !waitRemoteLink(t, r, cfg.Edge, tunnelIface, linkAppearTimeout) {
		dumpDaemonLog(t, r, cfg.Edge)
		t.Fatalf("edge %s never appeared", tunnelIface)
	}
	addressLink(t, r, cfg.Conc, smokeConcInner)
	addressLink(t, r, cfg.Edge, smokeEdgeInner)

	// 8. From the edge, ping the concentrator's inner IP. The first packet drives
	//    the WireGuard handshake through the NAT; the first reply means the
	//    session is established.
	if !pingUntil(t, r, cfg.Edge, smokeConcInner, handshakeTimeout) {
		dumpDaemonLog(t, r, cfg.Conc)
		dumpDaemonLog(t, r, cfg.Edge)
		t.Fatalf("handshake never completed: %s unreachable from the edge through the tunnel", smokeConcInner)
	}
	rttMs := measureRTT(t, r, cfg.Edge, smokeConcInner)
	t.Logf("HANDSHAKE OK; ping %s from edge: avg RTT = %.3f ms", smokeConcInner, rttMs)

	// 9. iperf3 server on the concentrator (bound to its inner IP), then three
	//    client runs from the edge.
	startIperfServer(t, r, cfg.Conc, smokeConcInner)

	single := iperfTCP(t, r, cfg.Edge, smokeConcInner, 1)
	t.Logf("iperf3 single-flow TCP: %.2f Mbit/s (retransmits=%d)", single.mbps, single.retransmits)

	parallel := iperfTCP(t, r, cfg.Edge, smokeConcInner, 8)
	t.Logf("iperf3 8x-parallel TCP: %.2f Mbit/s (retransmits=%d)", parallel.mbps, parallel.retransmits)

	udp := iperfUDP(t, r, cfg.Edge, smokeConcInner, "100M")
	t.Logf("iperf3 UDP @100M: goodput %.2f Mbit/s, loss %.3f%%, jitter %.3f ms", udp.mbps, udp.lossPct, udp.jitterMs)

	// Final report block (report-only; nothing below gates the test).
	t.Logf("=== P0 SMOKE RESULTS ===\n"+
		"  handshake:      OK\n"+
		"  ping avg RTT:   %.3f ms\n"+
		"  TCP single:     %.2f Mbit/s (retx %d)\n"+
		"  TCP 8-parallel: %.2f Mbit/s (retx %d)\n"+
		"  UDP goodput:    %.2f Mbit/s (loss %.3f%%, jitter %.3f ms)",
		rttMs, single.mbps, single.retransmits, parallel.mbps, parallel.retransmits,
		udp.mbps, udp.lossPct, udp.jitterMs)
}

// provision runs the T32 idempotent provisioner against host, failing the test
// on error and logging the per-step outcome.
func provision(t *testing.T, r *Runner, host Host, opts ProvisionOpts) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), provisionTimeout)
	defer cancel()
	rep, err := Provision(ctx, r, host, opts)
	if err != nil {
		t.Fatalf("%s: provisioning failed: %v", host.Role, err)
	}
	t.Logf("provisioned %s", rep)
}

// repoRoot returns the module root by walking up from the test's working
// directory (the package dir) until it finds go.mod.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("go.mod not found walking up from working directory")
		}
		dir = parent
	}
}

// syncAndBuild tar-streams the repo (excluding VCS/ledger dirs) into
// smokeRemoteDir on host, then builds wanbond there with the pinned Go
// toolchain. The build arch matches the host's arch (native build).
func syncAndBuild(t *testing.T, r *Runner, host Host, root string) {
	t.Helper()

	// Fresh directory, then extract the streamed tar.
	syncCtx, cancelSync := context.WithTimeout(context.Background(), smokeSyncTimeout)
	defer cancelSync()
	remoteCmd := "rm -rf " + smokeRemoteDir + " && mkdir -p " + smokeRemoteDir + " && tar xzf - -C " + smokeRemoteDir
	ssh := exec.CommandContext(syncCtx, "ssh", r.sshArgs(host, remoteCmd)...)
	tar := exec.CommandContext(syncCtx, "tar", "czf", "-",
		"--exclude=./.git", "--exclude=./.cq", "--exclude=./.direnv",
		"--exclude=./.github", "--exclude=./wanbond",
		"--exclude=./.claude", "--exclude=./.codegraph",
		"--exclude=./.worktrees", "--exclude=./result", "-C", root, ".")

	tarOut, err := tar.StdoutPipe()
	if err != nil {
		t.Fatalf("%s: tar stdout pipe: %v", host.Role, err)
	}
	ssh.Stdin = tarOut
	var tarErr, sshErr strings.Builder
	tar.Stderr = &tarErr
	ssh.Stderr = &sshErr

	if err := ssh.Start(); err != nil {
		t.Fatalf("%s: start ssh (sync): %v", host.Role, err)
	}
	if err := tar.Run(); err != nil {
		t.Fatalf("%s: tar (sync): %v\n%s", host.Role, err, tarErr.String())
	}
	if err := ssh.Wait(); err != nil {
		t.Fatalf("%s: ssh extract (sync): %v\n%s", host.Role, err, sshErr.String())
	}
	t.Logf("%s: repo synced to %s", host.Role, smokeRemoteDir)

	// Native build with the provisioned Go toolchain (gcc kept on PATH for cgo).
	buildCtx, cancelBuild := context.WithTimeout(context.Background(), smokeBuildTimeout)
	defer cancelBuild()
	build := "cd " + smokeRemoteDir + " && PATH=" + goInstallDir + "/bin:$PATH go build -o wanbond ./cmd/wanbond"
	if _, err := r.Run(buildCtx, host, build); err != nil {
		t.Fatalf("%s: native wanbond build failed: %v", host.Role, err)
	}
	t.Logf("%s: built %s", host.Role, smokeBin)
}

// primaryIP returns host's primary IPv4 source address (the src of its default
// route), which each path's socket binds to.
func primaryIP(t *testing.T, r *Runner, host Host) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), smokeSSHTimeout)
	defer cancel()
	res, err := r.Run(ctx, host, `ip route get 1.1.1.1 | sed -n 's/.*src \([0-9.]*\).*/\1/p' | head -1`)
	if err != nil {
		t.Fatalf("%s: primary IP probe failed: %v", host.Role, err)
	}
	ip := strings.TrimSpace(res.Stdout)
	if ip == "" {
		t.Fatalf("%s: could not determine primary IP", host.Role)
	}
	return ip
}

// writeRemoteFile pipes content to path on host at mode 0600 (the exact mode
// config.Load requires), failing the test on error. `umask 077` narrows the
// creation mode BEFORE any secret bytes are written, so the file is never
// briefly world-readable; the trailing chmod is belt-and-braces.
func writeRemoteFile(t *testing.T, r *Runner, host Host, path, content string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), smokeSSHTimeout)
	defer cancel()
	remoteCmd := "umask 077 && cat > " + path + " && chmod 600 " + path
	cmd := exec.CommandContext(ctx, "ssh", r.sshArgs(host, remoteCmd)...)
	cmd.Stdin = strings.NewReader(content)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s: write %s failed: %v\n%s", host.Role, path, err, stderr.String())
	}
	t.Logf("%s: wrote %s (0600)", host.Role, path)
}

// startDaemon launches the wanbond daemon on host under a transient systemd unit
// (clean detachment; nohup-& would hang the SSH session) and registers teardown
// (stop the unit, remove the interface) that runs even on failure.
func startDaemon(t *testing.T, r *Runner, host Host, cfgPath string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), smokeSSHTimeout)
	defer cancel()

	// Register teardown BEFORE start so a failed start still cleans up.
	t.Cleanup(func() {
		stopUnit(t, r, host, smokeUnit)
		delLink(t, r, host, tunnelIface)
	})

	start := fmt.Sprintf("sudo systemd-run --unit=%s --service-type=simple %s --config %s",
		smokeUnit, smokeBin, cfgPath)
	if _, err := r.Run(ctx, host, start); err != nil {
		t.Fatalf("%s: start wanbond daemon failed: %v", host.Role, err)
	}
	t.Logf("%s: wanbond daemon started (unit=%s)", host.Role, smokeUnit)
}

// waitRemoteLink polls until dev exists on host or d elapses.
func waitRemoteLink(t *testing.T, r *Runner, host Host, dev string, d time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), smokeSSHTimeout)
		_, err := r.Run(ctx, host, "ip link show "+dev)
		cancel()
		if err == nil {
			return true
		}
		time.Sleep(300 * time.Millisecond)
	}
	return false
}

// addressLink assigns inner/smokeInnerCIDR to the tunnel interface on host and
// brings it up (via sudo over SSH).
func addressLink(t *testing.T, r *Runner, host Host, inner string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), smokeSSHTimeout)
	defer cancel()
	// `addr replace` is idempotent: it adds the address or updates it in place,
	// so a re-run against a still-addressed interface does not error.
	cmd := fmt.Sprintf("sudo ip addr replace %s/%s dev %s && sudo ip link set %s up",
		inner, smokeInnerCIDR, tunnelIface, tunnelIface)
	if _, err := r.Run(ctx, host, cmd); err != nil {
		t.Fatalf("%s: address %s failed: %v", host.Role, tunnelIface, err)
	}
	t.Logf("%s: %s addressed %s/%s up", host.Role, tunnelIface, inner, smokeInnerCIDR)
}

// pingUntil pings ip from host until it replies or d elapses; the first reply
// implies the WireGuard session is established.
func pingUntil(t *testing.T, r *Runner, host Host, ip string, d time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), smokeSSHTimeout)
		_, err := r.Run(ctx, host, "ping -c 1 -W 2 "+ip)
		cancel()
		if err == nil {
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}

// rttRe extracts the average from ping's "rtt min/avg/max/mdev = a/b/c/d ms"
// summary line.
var rttRe = regexp.MustCompile(`= [0-9.]+/([0-9.]+)/`)

// measureRTT runs a 10-packet ping from host to ip and returns the average RTT
// in milliseconds.
func measureRTT(t *testing.T, r *Runner, host Host, ip string) float64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), smokeSSHTimeout)
	defer cancel()
	res, err := r.Run(ctx, host, "ping -c 10 -W 2 "+ip)
	if err != nil {
		t.Fatalf("%s: measured ping to %s failed: %v", host.Role, ip, err)
	}
	m := rttRe.FindStringSubmatch(res.Stdout)
	if m == nil {
		t.Fatalf("%s: could not parse RTT from ping output:\n%s", host.Role, res.Stdout)
	}
	avg, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		t.Fatalf("%s: parse avg RTT %q: %v", host.Role, m[1], err)
	}
	return avg
}

// startIperfServer starts an iperf3 server on host bound to serverIP under a
// transient systemd unit, registering teardown (runs on failure too).
func startIperfServer(t *testing.T, r *Runner, host Host, serverIP string) {
	t.Helper()
	t.Cleanup(func() { stopUnit(t, r, host, iperfUnit) })

	ctx, cancel := context.WithTimeout(context.Background(), smokeSSHTimeout)
	defer cancel()
	start := fmt.Sprintf("sudo systemd-run --unit=%s --service-type=simple iperf3 -s -B %s",
		iperfUnit, serverIP)
	if _, err := r.Run(ctx, host, start); err != nil {
		t.Fatalf("%s: start iperf3 server failed: %v", host.Role, err)
	}
	// Give the server a moment to bind before the first client connects.
	time.Sleep(1 * time.Second)
	t.Logf("%s: iperf3 server up on %s (unit=%s)", host.Role, serverIP, iperfUnit)
}

// tcpMeasurement is one TCP iperf3 result.
type tcpMeasurement struct {
	mbps        float64
	retransmits int
}

// udpMeasurement is one UDP iperf3 result. mbps is the goodput derived from the
// sender rate and the measured loss (see iperfUDP), not the raw offered rate.
type udpMeasurement struct {
	mbps     float64
	lossPct  float64
	jitterMs float64
}

// iperfReport is the subset of iperf3 -J output this tier records. sum_sent
// carries the sender totals for TCP; sum carries the UDP summary, whose
// bits_per_second is the sender's offered rate (goodput is derived from
// lost_percent in iperfUDP), alongside jitter and loss.
type iperfReport struct {
	End struct {
		SumSent struct {
			BitsPerSecond float64 `json:"bits_per_second"`
			Retransmits   int     `json:"retransmits"`
		} `json:"sum_sent"`
		Sum struct {
			BitsPerSecond float64 `json:"bits_per_second"`
			JitterMs      float64 `json:"jitter_ms"`
			LostPercent   float64 `json:"lost_percent"`
		} `json:"sum"`
	} `json:"end"`
}

// runIperfJSON runs an iperf3 client on host and parses its -J report.
func runIperfJSON(t *testing.T, r *Runner, host Host, args string) iperfReport {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), iperfRunTimeout)
	defer cancel()
	res, err := r.Run(ctx, host, "iperf3 "+args+" -J")
	if err != nil {
		t.Fatalf("%s: iperf3 %s failed: %v", host.Role, args, err)
	}
	var rep iperfReport
	if err := json.Unmarshal([]byte(res.Stdout), &rep); err != nil {
		t.Fatalf("%s: parse iperf3 JSON: %v\n%s", host.Role, err, res.Stdout)
	}
	return rep
}

// iperfTCP runs a 10s TCP transfer from host to serverIP with parallel streams
// and returns the aggregate sender goodput and retransmit count.
func iperfTCP(t *testing.T, r *Runner, host Host, serverIP string, parallel int) tcpMeasurement {
	t.Helper()
	args := fmt.Sprintf("-c %s -t 10 -P %d", serverIP, parallel)
	rep := runIperfJSON(t, r, host, args)
	return tcpMeasurement{
		mbps:        rep.End.SumSent.BitsPerSecond / 1e6,
		retransmits: rep.End.SumSent.Retransmits,
	}
}

// iperfUDP runs a 10s UDP transfer from host to serverIP at the given target
// bitrate and returns the goodput, loss percentage, and jitter. In iperf3's UDP
// client JSON, end.sum.bits_per_second is the SENDER's offered rate (it tracks
// the -b target), not receiver goodput, so true goodput is derived by applying
// the measured loss: send_rate * (1 - lost_percent/100).
func iperfUDP(t *testing.T, r *Runner, host Host, serverIP, bitrate string) udpMeasurement {
	t.Helper()
	args := fmt.Sprintf("-c %s -t 10 -u -b %s", serverIP, bitrate)
	rep := runIperfJSON(t, r, host, args)
	sendMbps := rep.End.Sum.BitsPerSecond / 1e6
	lossPct := rep.End.Sum.LostPercent
	return udpMeasurement{
		mbps:     sendMbps * (1 - lossPct/100),
		lossPct:  lossPct,
		jitterMs: rep.End.Sum.JitterMs,
	}
}

// preClean removes any leftover wanbond/iperf3 units and tunnel interface from
// a prior interrupted run so this run starts from a clean slate. Best-effort:
// it logs but does not fail the test.
func preClean(t *testing.T, r *Runner, host Host) {
	t.Helper()
	stopUnit(t, r, host, smokeUnit)
	stopUnit(t, r, host, iperfUnit)
	delLink(t, r, host, tunnelIface)
}

// stopUnit stops (and clears any failed state of) a transient systemd unit on
// host. Best-effort: it logs but does not fail the test, so one host's cleanup
// error does not mask the real result.
func stopUnit(t *testing.T, r *Runner, host Host, unit string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	cmd := fmt.Sprintf("sudo systemctl stop %s 2>/dev/null; sudo systemctl reset-failed %s 2>/dev/null; true", unit, unit)
	if _, err := r.Run(ctx, host, cmd); err != nil {
		t.Logf("cleanup: %s: stop unit %s: %v", host.Role, unit, err)
	}
}

// delLink removes the tunnel interface on host. Best-effort (see stopUnit).
func delLink(t *testing.T, r *Runner, host Host, dev string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	if _, err := r.Run(ctx, host, "sudo ip link del "+dev+" 2>/dev/null; true"); err != nil {
		t.Logf("cleanup: %s: del link %s: %v", host.Role, dev, err)
	}
}

// removeRemoteDir deletes smokeRemoteDir on host, discarding the synced repo,
// the native binary, and the secret configs (WireGuard private keys + PSK) so
// no key material persists past the run. Best-effort (see stopUnit).
func removeRemoteDir(t *testing.T, r *Runner, host Host) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	if _, err := r.Run(ctx, host, "rm -rf "+smokeRemoteDir); err != nil {
		t.Logf("cleanup: %s: remove %s: %v", host.Role, smokeRemoteDir, err)
	}
}

// dumpDaemonLog logs the tail of the wanbond unit's journal on host for
// diagnosing a failed bring-up.
func dumpDaemonLog(t *testing.T, r *Runner, host Host) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), smokeSSHTimeout)
	defer cancel()
	res, err := r.Run(ctx, host, "sudo journalctl -u "+smokeUnit+" --no-pager -n 50 2>&1; true")
	if err != nil {
		t.Logf("%s: could not read daemon journal: %v", host.Role, err)
		return
	}
	t.Logf("%s: wanbond journal (last 50 lines):\n%s", host.Role, res.Stdout)
}

// genSmokeKey generates an X25519 keypair (WireGuard key format), base64-encoded
// as the TOML config carries it.
func genSmokeKey(t *testing.T) (privB64, pubB64 string) {
	t.Helper()
	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate X25519 key: %v", err)
	}
	return base64.StdEncoding.EncodeToString(priv.Bytes()),
		base64.StdEncoding.EncodeToString(priv.PublicKey().Bytes())
}

// randSmokeKey returns 32 random bytes base64-encoded (the outer-control PSK).
func randSmokeKey(t *testing.T) string {
	t.Helper()
	var b [32]byte
	if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
		t.Fatalf("read random: %v", err)
	}
	return base64.StdEncoding.EncodeToString(b[:])
}
