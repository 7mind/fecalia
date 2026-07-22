//go:build e2e

// Package device (this file) — permanent regression coverage for D96 (T266): a
// REAL netns/netem two-host tunnel driving the production adaptive-FEC controller
// under sustained real packet loss, reconstructing the throwaway field_h96_test.go
// written during the D96 investigation (H96), which is not in the tree.
//
// D96's H96 hypothesis asked whether the field's zero-parity symptom reproduces on
// a real (kernel-forwarded, netem-lossy) path rather than a synthetic in-process
// loss injector; it did NOT reproduce — signal, controller, and encoder all behaved
// correctly end-to-end (parity=13584, recovered=2542, residual=0.0018 on the o3
// run) — but that repro was a throwaway. This file makes it permanent so a future
// regression (signal/controller/encoder wiring silently breaking again) is caught
// automatically rather than requiring another field incident.
//
// Isolation: TestMain re-execs the whole `device` e2e test binary inside a fresh
// network namespace (mirroring test/e2e/main_test.go) so this fixture's `wanbond0`
// TUN and veth pair never collide with a host's own tunnel — critically including
// the persistent root-netns concentrator some hosts (e.g. o3) run outside the test.
package device

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/7mind/wanbond/internal/metrics"
)

// h96NSEnvMarker guards TestMain's one-shot re-exec into a fresh network namespace.
const h96NSEnvMarker = "WANBOND_DEVICE_E2E_NS"

// TestMain re-execs this test binary inside a FRESH network namespace, exactly as
// test/e2e/main_test.go does for the same reason: under real root (the o3 sudo
// target) a plain mount+net namespace; unprivileged, an unprivileged user+net
// namespace (still grants CAP_NET_ADMIN for veth/netem). Without this, running as
// root directly in the HOST's root netns would create wanbond0 there and collide
// with a persistent root-netns concentrator (the documented o3 collision).
func TestMain(m *testing.M) {
	if os.Getenv(h96NSEnvMarker) == "" {
		self, err := os.Executable()
		if err != nil {
			fmt.Fprintln(os.Stderr, "device e2e: cannot find test binary:", err)
			os.Exit(1)
		}
		unshareArgs := []string{"-Urmn"}
		if os.Geteuid() == 0 {
			unshareArgs = []string{"-mn"}
		}
		args := append([]string{}, unshareArgs...)
		args = append(args, "--", self)
		args = append(args, os.Args[1:]...)

		cmd := exec.Command("unshare", args...)
		cmd.Env = append(os.Environ(), h96NSEnvMarker+"=1")
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		err = cmd.Run()
		if err == nil {
			os.Exit(0)
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			os.Exit(ee.ExitCode())
		}
		fmt.Fprintln(os.Stderr, "device e2e: namespace re-exec failed:", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// h96 constants: the single emulated WAN path's addressing/impairment profile and
// the field-exact [fec] block from D96 (target_residual=0.001, deadline=5ms, K=8,
// max parity M=4).
const (
	h96EdgeVeth    = "wbh96e" // <=15 chars
	h96ConcVeth    = "wbh96c"
	h96EdgeIP      = "10.196.1.1"
	h96ConcIP      = "10.196.1.2"
	h96PathDelayMs = 20

	h96TunDev     = "wanbond0"
	h96ListenPort = 51820
	h96EdgeInner  = "10.96.0.1"
	h96ConcInner  = "10.96.0.2"

	h96MetricsListen = "127.0.0.1:9199"
	h96MetricsURL    = "http://" + h96MetricsListen + "/metrics"

	h96DataShards     = 8     // K
	h96ParityCeiling  = 4     // M ceiling (field-exact)
	h96TargetResidual = 0.001 // field-exact adaptive target residual
	h96DeadlineMs     = 5     // field-exact deadline

	h96LossPct = 8.0 // field-exact injected outer loss

	// h96WarmupSecs settles the per-path probe-loss estimate and lets the adaptive
	// controller slew to steady M before the measurement window starts (mirrors
	// p4WarmupSecs: RateInterval 500ms, EWMA over a few 200ms probe intervals).
	h96WarmupSecs = 5

	// h96LoadSecs is the saturating single-flow TCP window. It must be long enough
	// for TWO independent reasons: (1) accumulate thousands of DATA/parity frames
	// so the repair/recovered assertions are not vacuous (mirrors p3LoadSecs/
	// p4LoadSecs), and (2) — the dominant constraint here — telemetry.lossWindow
	// (internal/telemetry/estimator.go) is a SEQUENCE-COUNT ring of
	// defaultLossWindow=512 probe-echo slots; UNTIL the total probe population
	// since Prober creation exceeds 512, fraction() averages the ENTIRE cumulative
	// history from the first probe (including this fixture's brief pre-injection,
	// zero-loss bring-up), diluting the reported loss well below the true injected
	// rate. At DefaultProbeInterval=200ms, flushing the window with fully-lossy
	// samples needs >= 512*0.2s = 102.4s of CONTINUOUS loss before the final
	// scrape. h96WarmupSecs+h96LoadSecs+h96SettleSecs must clear that with margin
	// regardless of how long the pre-injection bring-up took (observed to vary
	// under host contention) — hence the load window, not the warmup, carries
	// this budget: it is productive (drives the repair/recovered deltas) rather
	// than a pure sleep.
	h96LoadSecs = 120

	// h96SettleSecs lets in-flight recovery land, loss still injected, before the
	// residual/parity/smoothed-loss gauges are scraped (mirrors p4SettleSecs).
	h96SettleSecs = 3

	// h96LossBandLow/High is the acceptance's tolerance band around the injected 8%
	// for BOTH the edge per-path probe loss and the controller's smoothed-loss gauge
	// (T266 acceptance (c) and (d)).
	h96LossBandLow  = 0.04
	h96LossBandHigh = 0.16

	// h96CongestionControl pins the sender-side TCP congestion-control algorithm
	// (mirrors fecCongestionControl in test/e2e/fec_baseline_test.go).
	h96CongestionControl = "cubic"
)

// TestAdaptiveFECUnderRealLoss is the permanent D96/H96 regression: edge and
// concentrator, real veth+netns, netem 8% loss on the single path, the field-exact
// adaptive [fec] block. It pushes a saturating TCP flow through the tunnel while
// loss is injected and then asserts, entirely from the /metrics exposition:
//
//	(a) edge wanbond_fec_repair_packets_total delta > 0 — adaptive parity was
//	    actually EMITTED under loss (the D96 field symptom was exactly this stuck
//	    at 0);
//	(b) concentrator wanbond_fec_recovered_packets_total delta > 0 — the emitted
//	    parity actually reconstructed lost data at the decoder;
//	(c) edge per-path probe loss (wanbond_path_loss_ratio{path="wan"}) lands in
//	    [0.04,0.16] around the injected 8% — the loss actually reached the
//	    datapath as the estimator's raw signal (anti-vacuity: a silently-unapplied
//	    netem would otherwise let (a)/(b) fail cleanly rather than pass vacuously);
//	(d) the NEW T264 gauges, scraped end-to-end from the SAME exposition:
//	    wanbond_fec_adaptive_parity > 0 (the controller's current target M) and
//	    wanbond_fec_smoothed_loss within the same [0.04,0.16] band (the
//	    controller's EWMA input tracked the real loss, not stuck at 0).
func TestAdaptiveFECUnderRealLoss(t *testing.T) {
	top := h96Setup(t)
	bin := h96BuildWanbond(t)

	edgePriv, edgePub := h96GenKey(t)
	concPriv, concPub := h96GenKey(t)
	psk := h96RandKey(t)

	fecBlock := fmt.Sprintf("[fec]\nenabled = true\ndata_shards = %d\nparity_shards = %d\ndeadline = \"%dms\"\n",
		h96DataShards, h96ParityCeiling, h96DeadlineMs)
	edgeFEC := fecBlock + fmt.Sprintf("adaptive = true\ntarget_residual = %g\n\n", h96TargetResidual)
	concFEC := fecBlock + "\n"
	metricsBlock := fmt.Sprintf("[metrics]\nlisten = %q\n\n", h96MetricsListen)

	dir := t.TempDir()
	edgeCfg := h96WriteConfig(t, filepath.Join(dir, "edge.toml"), fmt.Sprintf(`role = "edge"
psk = "%s"

[[paths]]
name = "wan"
source_addr = "%s"

%s%s[wireguard]
private_key = "%s"

[[wireguard.peers]]
public_key = "%s"
endpoint = "%s:%d"
allowed_ips = ["%s/32"]

[log]
level = "error"
`, psk, h96EdgeIP, metricsBlock, edgeFEC, edgePriv, concPub, h96ConcIP, h96ListenPort, h96ConcInner))

	concCfg := h96WriteConfig(t, filepath.Join(dir, "conc.toml"), fmt.Sprintf(`role = "concentrator"
psk = "%s"

[[paths]]
name = "wan"
source_addr = "%s"

%s%s[wireguard]
private_key = "%s"
listen_port = %d

[[wireguard.peers]]
public_key = "%s"
allowed_ips = ["%s/32"]

[log]
level = "error"
`, psk, h96ConcIP, metricsBlock, concFEC, concPriv, h96ListenPort, edgePub, h96EdgeInner))

	// Concentrator first (must be listening before the edge initiates), then edge.
	conc := top.startProc(t, "concentrator", "nsenter", "-t", strconv.Itoa(top.pid), "-n", bin, "--config", concCfg)
	edge := top.startProc(t, "edge", bin, "--config", edgeCfg)

	if !top.waitLink(h96TunDev, false, 5*time.Second) {
		t.Fatalf("edge %s never appeared\n%s", h96TunDev, edge.log())
	}
	if !top.waitLink(h96TunDev, true, 5*time.Second) {
		t.Fatalf("concentrator %s never appeared\n%s", h96TunDev, conc.log())
	}
	top.run("ip", "addr", "add", h96EdgeInner+"/24", "dev", h96TunDev)
	top.run("ip", "link", "set", h96TunDev, "up")
	top.nsenter("ip", "addr", "add", h96ConcInner+"/24", "dev", h96TunDev)
	top.nsenter("ip", "link", "set", h96TunDev, "up")

	if !top.pingUntil(h96ConcInner, 15*time.Second) {
		t.Fatalf("tunnel never came up: %s unreachable\n--- edge ---\n%s\n--- conc ---\n%s",
			h96ConcInner, edge.log(), conc.log())
	}

	// Inject the field-exact 8% outer loss on the edge->conc egress AFTER the clean
	// bring-up handshake, then warm up so the probe-loss estimate and the adaptive
	// controller settle to steady state under it.
	top.injectLoss(h96LossPct)
	time.Sleep(h96WarmupSecs * time.Second)

	ctxB, cancelB := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelB()
	edgeBefore := h96FetchMetrics(t, ctxB, h96MetricsURL)
	concBefore := h96FetchMetricsInNetns(t, top.pid, h96MetricsURL)

	// Push a saturating single-flow TCP transfer through the lossy tunnel.
	top.startProc(t, "iperf3-server", "nsenter", "-t", strconv.Itoa(top.pid), "-n", "iperf3", "-s", "-1", "-B", h96ConcInner)
	time.Sleep(500 * time.Millisecond) // allow the server to bind and listen
	if out, err := exec.Command("iperf3", "-c", h96ConcInner, "-t", strconv.Itoa(h96LoadSecs), "--congestion", h96CongestionControl).CombinedOutput(); err != nil {
		t.Fatalf("iperf3 load transfer failed: %v\n%s", err, out)
	}

	// Let in-flight recovery land, loss still injected, before scraping the gauges.
	time.Sleep(h96SettleSecs * time.Second)

	ctxA, cancelA := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelA()
	edgeAfter := h96FetchMetrics(t, ctxA, h96MetricsURL)
	concAfter := h96FetchMetricsInNetns(t, top.pid, h96MetricsURL)

	repairDelta := h96DeltaValue(t, edgeBefore, edgeAfter, metrics.MetricFECRepair)
	recoveredDelta := h96DeltaValue(t, concBefore, concAfter, metrics.MetricFECRecovered)
	edgeLoss, okLoss := edgeAfter.PathValue(metrics.MetricLoss, "wan")
	if !okLoss {
		t.Fatalf("edge /metrics missing %s{path=\"wan\"}", metrics.MetricLoss)
	}
	adaptiveParity, okParity := edgeAfter.Value(metrics.MetricFECAdaptiveParity)
	if !okParity {
		t.Fatalf("edge /metrics missing %s — the T264 adaptive-parity gauge did not expose (controller not engaged, or the series is not wired)", metrics.MetricFECAdaptiveParity)
	}
	smoothedLoss, okSmoothed := edgeAfter.Value(metrics.MetricFECSmoothedLoss)
	if !okSmoothed {
		t.Fatalf("edge /metrics missing %s — the T264 smoothed-loss gauge did not expose", metrics.MetricFECSmoothedLoss)
	}

	t.Logf("h96: edge repairDelta=%.0f edgeLoss=%.4f adaptiveParity=%.0f smoothedLoss=%.4f | conc recoveredDelta=%.0f",
		repairDelta, edgeLoss, adaptiveParity, smoothedLoss, recoveredDelta)

	// (c) loss-took-effect guard: the edge's per-path probe loss must land in the
	// tolerance band around the injected 8% — otherwise a silently-unapplied netem
	// would let a zero-repair/zero-recovered result pass VACUOUSLY as "adaptive is
	// inert because there's no loss", rather than the D96 regression it must catch.
	if edgeLoss < h96LossBandLow || edgeLoss > h96LossBandHigh {
		t.Fatalf("edge probe loss %.4f outside [%.2f,%.2f] around the injected %.2f — the netem loss did not take effect as expected; the repair/recovered/parity assertions below would be measured on the wrong loss",
			edgeLoss, h96LossBandLow, h96LossBandHigh, h96LossPct/100)
	}

	// (a) the D96 field symptom, directly: adaptive parity actually emitted.
	if repairDelta <= 0 {
		t.Errorf("edge %s delta = %.0f, want > 0 (D96: adaptive FEC emitted ZERO parity under sustained real loss)", metrics.MetricFECRepair, repairDelta)
	}
	// (b) the emitted parity actually reconstructed lost data at the decoder.
	if recoveredDelta <= 0 {
		t.Errorf("concentrator %s delta = %.0f, want > 0 (parity emitted but nothing recovered — decoder-side gap)", metrics.MetricFECRecovered, recoveredDelta)
	}
	// (d) the T264 observability gauges, end-to-end from the exposition.
	if adaptiveParity <= 0 {
		t.Errorf("%s = %.0f, want > 0 (the controller's target M stayed at 0 under real loss — exactly the D96 symptom the S2 gauge exists to surface)", metrics.MetricFECAdaptiveParity, adaptiveParity)
	}
	if smoothedLoss < h96LossBandLow || smoothedLoss > h96LossBandHigh {
		t.Errorf("%s = %.4f, want in [%.2f,%.2f] (the controller's EWMA input did not track the real injected loss)", metrics.MetricFECSmoothedLoss, smoothedLoss, h96LossBandLow, h96LossBandHigh)
	}
}

// h96Topology is a minimal single-path netns/netem fixture, scoped to this test:
// the edge side is the current (TestMain re-exec'd) network namespace; the
// concentrator side is a child process's namespace, addressed by PID (mirrors
// test/e2e's Topology, reduced to one path).
type h96Topology struct {
	t      *testing.T
	holder *exec.Cmd
	pid    int
}

// h96Setup builds the two-namespace topology: a veth pair (h96EdgeVeth/h96ConcVeth)
// addressed h96EdgeIP/h96ConcIP, with netem delay on the edge egress (loss is
// injected later, at runtime, via injectLoss).
func h96Setup(t *testing.T) *h96Topology {
	t.Helper()
	top := &h96Topology{t: t}

	top.holder = exec.Command("unshare", "-n", "sleep", "600")
	if err := top.holder.Start(); err != nil {
		t.Fatalf("start concentrator netns holder: %v", err)
	}
	top.pid = top.holder.Process.Pid
	top.waitForNetns()

	pid := strconv.Itoa(top.pid)
	top.run("ip", "link", "set", "lo", "up")
	top.nsenter("ip", "link", "set", "lo", "up")

	_ = top.tryRun("ip", "link", "del", h96EdgeVeth) // idempotent pre-delete (D3-style)
	top.run("ip", "link", "add", h96EdgeVeth, "type", "veth", "peer", "name", h96ConcVeth)
	top.run("ip", "link", "set", h96ConcVeth, "netns", pid)
	top.run("ip", "addr", "add", h96EdgeIP+"/24", "dev", h96EdgeVeth)
	top.run("ip", "link", "set", h96EdgeVeth, "up")
	top.nsenter("ip", "addr", "add", h96ConcIP+"/24", "dev", h96ConcVeth)
	top.nsenter("ip", "link", "set", h96ConcVeth, "up")
	top.run("tc", "qdisc", "add", "dev", h96EdgeVeth, "root", "netem", "delay", fmt.Sprintf("%dms", h96PathDelayMs))

	t.Cleanup(top.teardown)
	return top
}

func (top *h96Topology) waitForNetns() {
	path := fmt.Sprintf("/proc/%d/ns/net", top.pid)
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	top.t.Fatalf("concentrator netns %s never appeared", path)
}

// injectLoss sets uniform egress loss (percent) on the edge veth at runtime,
// preserving the baseline delay.
func (top *h96Topology) injectLoss(pct float64) {
	top.run("tc", "qdisc", "change", "dev", h96EdgeVeth, "root", "netem",
		"delay", fmt.Sprintf("%dms", h96PathDelayMs), "loss", fmt.Sprintf("%g%%", pct))
}

func (top *h96Topology) teardown() {
	_ = top.tryRun("ip", "link", "del", h96EdgeVeth)
	if top.holder != nil && top.holder.Process != nil {
		_ = top.holder.Process.Kill()
		_, _ = top.holder.Process.Wait()
		top.holder = nil
	}
}

func (top *h96Topology) run(name string, args ...string) {
	top.t.Helper()
	if out, err := exec.Command(name, args...).CombinedOutput(); err != nil {
		top.t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
}

func (top *h96Topology) tryRun(name string, args ...string) error {
	return exec.Command(name, args...).Run()
}

func (top *h96Topology) nsenter(args ...string) {
	top.t.Helper()
	full := append([]string{"-t", strconv.Itoa(top.pid), "-n"}, args...)
	top.run("nsenter", full...)
}

// waitLink polls until dev exists (in the peer netns when ns is true), up to d.
func (top *h96Topology) waitLink(dev string, ns bool, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		var err error
		if ns {
			err = top.tryRun("nsenter", "-t", strconv.Itoa(top.pid), "-n", "ip", "link", "show", dev)
		} else {
			err = top.tryRun("ip", "link", "show", dev)
		}
		if err == nil {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// pingUntil pings ip from the edge netns until it answers or d elapses.
func (top *h96Topology) pingUntil(ip string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if top.tryRun("ping", "-c", "1", "-W", "1", ip) == nil {
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}

// h96LockedBuffer is a bytes.Buffer safe for concurrent Write (the output copier
// goroutine) and String (the test's diagnostic log() reads).
type h96LockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *h96LockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *h96LockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// h96Proc is a background process with captured combined output.
type h96Proc struct {
	cmd    *exec.Cmd
	output *h96LockedBuffer
}

func (p *h96Proc) log() string { return p.output.String() }

// startProc launches argv in the background, capturing its combined output and
// registering a cleanup that terminates it (SIGTERM, then SIGKILL after a grace
// period).
func (top *h96Topology) startProc(t *testing.T, name string, argv ...string) *h96Proc {
	t.Helper()
	cmd := exec.Command(argv[0], argv[1:]...)
	out := &h96LockedBuffer{}
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s (%v): %v", name, argv, err)
	}
	p := &h96Proc{cmd: cmd, output: out}
	t.Cleanup(func() {
		if cmd.Process == nil {
			return
		}
		_ = cmd.Process.Signal(syscall.SIGTERM)
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	})
	return p
}

// h96BuildWanbond compiles the daemon (plain build, no e2e tag) into a temp path.
func h96BuildWanbond(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "wanbond")
	out, err := exec.Command("go", "build", "-o", bin, "github.com/7mind/wanbond/cmd/wanbond").CombinedOutput()
	if err != nil {
		t.Fatalf("build wanbond: %v\n%s", err, out)
	}
	return bin
}

// h96GenKey generates an X25519 keypair, base64-encoded as the TOML config carries it.
func h96GenKey(t *testing.T) (privB64, pubB64 string) {
	t.Helper()
	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate X25519 key: %v", err)
	}
	return base64.StdEncoding.EncodeToString(priv.Bytes()),
		base64.StdEncoding.EncodeToString(priv.PublicKey().Bytes())
}

// h96RandKey returns 32 random bytes base64-encoded (a placeholder PSK).
func h96RandKey(t *testing.T) string {
	t.Helper()
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("read random: %v", err)
	}
	return base64.StdEncoding.EncodeToString(b[:])
}

// h96WriteConfig writes body to path with the exact 0600 mode config.Load requires.
func h96WriteConfig(t *testing.T, path, body string) string {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config %s: %v", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil { // defeat umask widening
		t.Fatalf("chmod config %s: %v", path, err)
	}
	return path
}

// h96FetchMetrics scrapes an in-namespace /metrics endpoint (the edge, which lives
// in the current/test netns) via metrics.Fetch.
func h96FetchMetrics(t *testing.T, ctx context.Context, url string) metrics.Exposition {
	t.Helper()
	exp, err := metrics.Fetch(ctx, http.DefaultClient, url)
	if err != nil {
		t.Fatalf("scrape %s: %v", url, err)
	}
	return exp
}

// h96FetchMetricsInNetns scrapes a loopback /metrics endpoint that lives inside the
// concentrator's network namespace (pid), via h96NetnsMetricsClient.
func h96FetchMetricsInNetns(t *testing.T, pid int, url string) metrics.Exposition {
	t.Helper()
	client := h96NetnsMetricsClient(pid)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	exp, err := metrics.Fetch(ctx, client, url)
	if err != nil {
		t.Fatalf("scrape concentrator %s in netns: %v", url, err)
	}
	return exp
}

// h96NetnsMetricsClient builds an http.Client whose dial opens its socket INSIDE
// the network namespace of pid (mirrors test/e2e's netnsMetricsClient): the
// concentrator's loopback /metrics endpoint lives in a namespace this test process
// is not a member of, so the connecting socket() call itself must run there.
func h96NetnsMetricsClient(pid int) *http.Client {
	nsPath := fmt.Sprintf("/proc/%d/ns/net", pid)

	dialInNetns := func(ctx context.Context, network, addr string) (net.Conn, error) {
		type result struct {
			conn net.Conn
			err  error
		}
		done := make(chan result, 1)
		go func() {
			runtime.LockOSThread() // deliberately never unlocked: goroutine exit kills the thread

			f, err := os.Open(nsPath)
			if err != nil {
				done <- result{err: fmt.Errorf("open %s: %w", nsPath, err)}
				return
			}
			defer f.Close()
			if err := unix.Setns(int(f.Fd()), unix.CLONE_NEWNET); err != nil {
				done <- result{err: fmt.Errorf("setns into concentrator netns: %w", err)}
				return
			}
			var d net.Dialer
			c, err := d.DialContext(ctx, network, addr)
			done <- result{conn: c, err: err}
		}()
		r := <-done
		return r.conn, r.err
	}

	return &http.Client{Transport: &http.Transport{DisableKeepAlives: true, DialContext: dialInNetns}}
}

// h96DeltaValue returns after-before for an unlabeled (connection-scoped) series —
// the shape of the FEC repair/recovered counters — failing if either scrape lacked
// it (a missing series is a wiring defect, not a zero).
func h96DeltaValue(t *testing.T, before, after metrics.Exposition, name string) float64 {
	t.Helper()
	b, ok := before.Value(name)
	if !ok {
		t.Fatalf("first scrape missing unlabeled series %s", name)
	}
	a, ok := after.Value(name)
	if !ok {
		t.Fatalf("second scrape missing unlabeled series %s", name)
	}
	return a - b
}
