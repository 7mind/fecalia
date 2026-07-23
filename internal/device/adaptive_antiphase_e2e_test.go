//go:build e2e

// Package device (this file) — permanent D96 fix-(d) regression, sibling to the T266
// single-path fixture (adaptive_real_loss_e2e_test.go): a REAL two-path (active-backup)
// netns/netem tunnel proving the T271/T272 scheduler-aware signal selection actually
// reproduces (and stays fixed) the field's ANTI-PHASE symptom on a topology that has a
// data-idle path to be fooled by.
//
// D96's field incident was "over/under/over": the adaptive controller's pre-fix drive
// took a role-agnostic MAX over every StateUp prober's loss, including the metered
// STANDBY, which active-backup never sends data over. A lossy-but-idle standby could
// drive parity M up to protect data it never carried, while the ACTIVE path's own
// sustained ~4-5% loss — squarely inside the pre-fix RaiseThreshold-vs-quantization gap
// D96 also fixed (mechanism 1, T273) — read as "inert" (M stuck near 0). T272's fix
// reads the loss from the scheduler's DataPaths() seam (T271) instead: under
// active-backup that is EXACTLY the one path carrying data, never the standby. This
// file proves BOTH halves of that fix on a real two-path fixture:
//
//   - the ACTIVE path's sustained loss DOES ramp M (the field's "inert" half), and
//   - a noisy STANDBY's blip, while it stays liveness StateUp (so a MAX-over-StateUp
//     drive would have seen it), does NOT ramp M (the field's "over" half) — the
//     anti-phase pattern must not reproduce.
//
// It reuses TestMain's namespace re-exec (defined in adaptive_real_loss_e2e_test.go, this
// package) and several of that file's process/config helpers (h96BuildWanbond, h96GenKey,
// h96RandKey, h96WriteConfig, h96FetchMetrics, h96Proc, h96LockedBuffer) — this file adds
// only what T266's SINGLE-path topology cannot express: a two-veth-pair topology with
// independently loss-injectable paths.
package device

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/metrics"
)

// g30 addressing: two emulated WAN uplinks (active/standby), a distinct tunnel-inner
// subnet and metrics port from h96's (T266) so both files' fixtures never collide when
// the device e2e binary runs both tests in one process.
const (
	g30EdgeVethActive = "wbg30ae" // <=15 chars
	g30ConcVethActive = "wbg30ac"
	g30EdgeIPActive   = "10.198.1.1"
	g30ConcIPActive   = "10.198.1.2"

	g30EdgeVethStandby = "wbg30se"
	g30ConcVethStandby = "wbg30sc"
	g30EdgeIPStandby   = "10.198.2.1"
	g30ConcIPStandby   = "10.198.2.2"

	g30PathDelayMs = 20

	g30TunDev     = "wanbond0"
	g30ListenPort = 51822
	g30EdgeInner  = "10.98.0.1"
	g30ConcInner  = "10.98.0.2"

	g30MetricsListen = "127.0.0.1:9200"
	g30MetricsURL    = "http://" + g30MetricsListen + "/metrics"

	// g30ActiveLossPct is the injected active-path loss for the ramps-parity phase: the
	// 4-5% band the task/D96 field incident cites as the regime that read as "inert"
	// pre-fix (below the legacy 5% RaiseThreshold, but not below the target_residual-
	// derived gate this fixture's [fec] block uses — see effectiveThresholds).
	g30ActiveLossPct = 4.5

	// g30WindowFillWarmupSecs is the ramps-parity phase's warmup: long enough that, ADDED
	// to bring-up time and g30SettleSecs, the elapsed time since the active path's netem
	// loss took effect exceeds lossWindowSize (512, mirrored in adaptivefec/config.go from
	// telemetry.defaultLossWindow) probe intervals — 512*0.2s = 102.4s — so the trailing
	// loss window is FULLY saturated (not merely non-empty) at scrape time. This is the
	// T266 512-slot dilution lesson applied the other way around from h96LoadSecs: h96
	// widens its OWN window with real traffic; here, because the active path's netem loss
	// is configured at qdisc-CREATION time (g30Setup) rather than injected after a clean
	// bring-up, a PARTIALLY filled window is already unbiased (no pre-injection clean
	// prefix to dilute it) — but at a small sample count its VARIANCE is large enough to
	// make a tight [g30LossBandLow,g30LossBandHigh] band assertion flaky. Waiting out the
	// window to (near) full removes that variance the same way h96's 120s load window does.
	g30WindowFillWarmupSecs = 110

	// g30SettleSecs lets a couple more adaptiveControlInterval (200ms) drive ticks land
	// after warmup before the scrape, so the published gauges reflect a settled decision.
	g30SettleSecs = 2

	// g30LossBandLow/High bound the edge's steady per-path probe loss AND the
	// controller's eligible/smoothed-loss gauges around the injected g30ActiveLossPct/100.
	// At a (near-)full 512-sample window the binomial standard deviation of the reported
	// fraction is sqrt(p(1-p)/512) ~= 0.9 percentage points at p=4.5%, so this band's
	// ~1.5-3 pp margin on each side is several sigma, not the proportional h96-style
	// 0.5x-2x multiplier a smaller sample would need.
	g30LossBandLow  = 0.015
	g30LossBandHigh = 0.09

	// g30StandbyNoiseLossPct is the noisy standby's SUSTAINED injected loss, applied at
	// qdisc-CREATION time (like the active path's g30ActiveLossPct — see
	// g30WindowFillWarmupSecs) rather than injected mid-test: a windowed loss estimator's
	// fraction is diluted by whatever clean history preceded the injection (the same T266
	// dilution lesson), so a brief on/off "blip" late in an otherwise-clean window reads as
	// only a fraction of its true rate — observed directly on this fixture (a 1s/40%-loss
	// on/off cycle read back as ~5-7%, not >=15%, well below any useful anti-vacuity
	// floor). Sustaining the loss from the very first packet sidesteps dilution exactly as
	// the active path's phase does, so the standby's OWN probe-loss gauge reads close to
	// the true injected rate. 20% is still two orders of magnitude above the residual-SLA
	// raise gate (>= 2 loss quanta ~= 0.0039, effectiveThresholds), so a role-agnostic
	// MAX-over-StateUp-probers drive (the pre-D96 defect this replaces) would unmistakably
	// have crossed it and ramped M — a PASS below is not vacuous.
	g30StandbyNoiseLossPct = 20.0

	// g30StandbyNoiseWarmupSecs is the standby-noise phase's single warmup: long enough to
	// clear minAdaptiveLossSamples (32, internal/bind/fec.go; 6.4s at the 200ms probe
	// cadence, so the ACTIVE path — clean throughout this phase — is sample-eligible) and
	// to accumulate enough of the standby's OWN loss-window samples
	// (100 at the 200ms probe cadence) that its reported fraction's binomial standard
	// deviation (~4 pp at p=20%) sits comfortably (~3 sigma) above g30StandbyNoiseMinLoss.
	// At g30StandbyNoiseLossPct a 6-consecutive-loss run (the telemetry.DefaultDownAfter
	// liveness-down trigger) has probability 0.2^6 ~= 0.0064%, so across ~100 probe
	// attempts the standby staying StateUp the whole warmup is overwhelmingly likely — the
	// "noisy StateUp standby" the task specifies, not a liveness-down non-event.
	g30StandbyNoiseWarmupSecs = 20

	// g30StandbyNoiseMinLoss is the anti-vacuity floor on the standby's OWN per-path loss
	// gauge: proof the injected noise actually registered on that path's probe-loss
	// estimator (rather than netem silently not applying), so a "parity did not ramp" pass
	// below is not vacuously explained by "the noise never happened".
	g30StandbyNoiseMinLoss = 0.08

	// g30NoRampMaxLoss bounds the ACTIVE path's own (clean, uninjected) loss signal in the
	// standby-noise phase: the drive must read the active path's near-zero loss, never let
	// the standby's noise bleed into the published eligible/smoothed-loss gauges.
	g30NoRampMaxLoss = 0.01
)

// g30Path describes one emulated WAN uplink in the two-path anti-phase fixture: a veth
// pair with baseline delay, and an optional loss percentage applied AT qdisc-creation
// time (g30Setup) so the very first probe on the path is already under it — see
// g30WindowFillWarmupSecs/g30StandbyNoiseLossPct for why that matters.
type g30Path struct {
	name     string
	edgeVeth string
	concVeth string
	edgeIP   string
	concIP   string
	lossPct  float64 // applied at qdisc-add time; 0 = clean at bring-up
}

// g30ActivePath/g30StandbyPath are the two DefaultPaths-style uplinks: active is the
// priority-0 (first-configured) active-backup primary, standby the priority-1 backup.
// Loss is left 0 here — each phase below sets its own g30Path slice with whichever
// path(s) need config-time loss.
var (
	g30ActivePathSpec  = g30Path{name: "active", edgeVeth: g30EdgeVethActive, concVeth: g30ConcVethActive, edgeIP: g30EdgeIPActive, concIP: g30ConcIPActive}
	g30StandbyPathSpec = g30Path{name: "standby", edgeVeth: g30EdgeVethStandby, concVeth: g30ConcVethStandby, edgeIP: g30EdgeIPStandby, concIP: g30ConcIPStandby}
)

// g30Topology is the two-veth-pair analog of h96Topology (T266): the edge side is the
// current (TestMain re-exec'd) netns, the concentrator side a child process's netns
// addressed by PID. Unlike h96Topology it carries a slice of independently
// loss-injectable paths.
type g30Topology struct {
	t      *testing.T
	holder *exec.Cmd
	pid    int
	paths  []g30Path
}

// g30Setup builds the topology from paths: a veth pair per path, each with baseline
// g30PathDelayMs delay and — if the path's lossPct is nonzero — that loss applied AT
// qdisc-add time, so it is in effect from the very first packet.
func g30Setup(t *testing.T, paths []g30Path) *g30Topology {
	t.Helper()
	top := &g30Topology{t: t, paths: paths}

	top.holder = exec.Command("unshare", "-n", "sleep", "600")
	if err := top.holder.Start(); err != nil {
		t.Fatalf("start concentrator netns holder: %v", err)
	}
	top.pid = top.holder.Process.Pid
	top.waitForNetns()

	pid := strconv.Itoa(top.pid)
	top.run("ip", "link", "set", "lo", "up")
	top.nsenter("ip", "link", "set", "lo", "up")

	for _, p := range top.paths {
		_ = top.tryRun("ip", "link", "del", p.edgeVeth) // idempotent pre-delete (D3-style)
		top.run("ip", "link", "add", p.edgeVeth, "type", "veth", "peer", "name", p.concVeth)
		top.run("ip", "link", "set", p.concVeth, "netns", pid)
		top.run("ip", "addr", "add", p.edgeIP+"/24", "dev", p.edgeVeth)
		top.run("ip", "link", "set", p.edgeVeth, "up")
		top.nsenter("ip", "addr", "add", p.concIP+"/24", "dev", p.concVeth)
		top.nsenter("ip", "link", "set", p.concVeth, "up")
		qargs := append([]string{"qdisc", "add", "dev", p.edgeVeth, "root", "netem"}, top.netemArgs(p.lossPct)...)
		top.run("tc", qargs...)
	}

	t.Cleanup(top.teardown)
	return top
}

// netemArgs builds the netem parameter list at the given loss percentage (0 =
// delay-only), preserving g30PathDelayMs. Every g30 path shares the same baseline
// delay, so this needs no per-path parameter.
func (top *g30Topology) netemArgs(lossPct float64) []string {
	args := []string{"delay", fmt.Sprintf("%dms", g30PathDelayMs)}
	if lossPct > 0 {
		args = append(args, "loss", fmt.Sprintf("%g%%", lossPct))
	}
	return args
}

// path looks up a configured path by name, failing the test if absent.
func (top *g30Topology) path(name string) g30Path {
	for _, p := range top.paths {
		if p.name == name {
			return p
		}
	}
	top.t.Fatalf("g30: unknown path %q", name)
	return g30Path{}
}

func (top *g30Topology) waitForNetns() {
	path := fmt.Sprintf("/proc/%d/ns/net", top.pid)
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	top.t.Fatalf("concentrator netns %s never appeared", path)
}

func (top *g30Topology) teardown() {
	for _, p := range top.paths {
		_ = top.tryRun("ip", "link", "del", p.edgeVeth)
	}
	if top.holder != nil && top.holder.Process != nil {
		_ = top.holder.Process.Kill()
		_, _ = top.holder.Process.Wait()
		top.holder = nil
	}
}

func (top *g30Topology) run(name string, args ...string) {
	top.t.Helper()
	if out, err := exec.Command(name, args...).CombinedOutput(); err != nil {
		top.t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
}

func (top *g30Topology) tryRun(name string, args ...string) error {
	return exec.Command(name, args...).Run()
}

func (top *g30Topology) nsenter(args ...string) {
	top.t.Helper()
	full := append([]string{"-t", strconv.Itoa(top.pid), "-n"}, args...)
	top.run("nsenter", full...)
}

// waitLink polls until dev exists (in the peer netns when ns is true), up to d.
func (top *g30Topology) waitLink(dev string, ns bool, d time.Duration) bool {
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

// pingUntil pings ip from the edge netns until it answers or d elapses. It is used
// with a generous deadline in this fixture because the active path may already carry
// injected loss from the very first packet (g30Setup) — see the ramps-parity phase.
func (top *g30Topology) pingUntil(ip string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if top.tryRun("ping", "-c", "1", "-W", "1", ip) == nil {
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}

// g30StartProc launches argv in the background, capturing combined output into an
// h96Proc (T266's process/log type, reused here — see the file header) and registering
// a cleanup that terminates it (SIGTERM, then SIGKILL after a grace period).
func g30StartProc(t *testing.T, name string, argv ...string) *h96Proc {
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

// g30BuildTunnel brings up the two-path (active, standby) active-backup edge — no
// [scheduler] block, so active-backup is the default and config order sets priority
// (active first = primary) — against a matching two-path concentrator, with the FEC
// plane's field-exact [fec] block (h96DataShards/h96ParityCeiling/h96DeadlineMs,
// adaptive/target_residual = h96TargetResidual, reused from adaptive_real_loss_e2e_test.go,
// this package) on the edge; the concentrator carries the plain fixed block so it
// decodes at the same ceiling (mirrors h96's edge/conc FEC block split). Only the edge
// exposes /metrics — the assertions below read the drive's published decision, which
// lives entirely on the send (edge) side.
func g30BuildTunnel(t *testing.T, top *g30Topology, bin string) (edge, conc *h96Proc) {
	t.Helper()

	edgePriv, edgePub := h96GenKey(t)
	concPriv, concPub := h96GenKey(t)
	psk := h96RandKey(t)

	active := top.path("active")
	standby := top.path("standby")

	edgeFEC := fmt.Sprintf("[fec]\nenabled = true\ndata_shards = %d\nparity_shards = %d\ndeadline = \"%dms\"\nadaptive = true\ntarget_residual = %g\n\n",
		h96DataShards, h96ParityCeiling, h96DeadlineMs, h96TargetResidual)
	concFEC := fmt.Sprintf("[fec]\nenabled = true\ndata_shards = %d\nparity_shards = %d\ndeadline = \"%dms\"\n\n",
		h96DataShards, h96ParityCeiling, h96DeadlineMs)
	metricsBlock := fmt.Sprintf("[metrics]\nlisten = %q\n\n", g30MetricsListen)

	dir := t.TempDir()
	edgeCfg := h96WriteConfig(t, filepath.Join(dir, "edge.toml"), fmt.Sprintf(`role = "edge"
psk = "%s"

[[paths]]
name = %q
source_addr = "%s"
dest_addr = "%s:%d"

[[paths]]
name = %q
source_addr = "%s"
dest_addr = "%s:%d"

%s%s[wireguard]
private_key = "%s"

[[wireguard.peers]]
public_key = "%s"
endpoint = "%s:%d"
allowed_ips = ["%s/32"]

[log]
level = "error"
`, psk,
		active.name, active.edgeIP, active.concIP, g30ListenPort,
		standby.name, standby.edgeIP, standby.concIP, g30ListenPort,
		metricsBlock, edgeFEC, edgePriv, concPub, active.concIP, g30ListenPort, g30ConcInner))

	concCfg := h96WriteConfig(t, filepath.Join(dir, "conc.toml"), fmt.Sprintf(`role = "concentrator"
psk = "%s"

[[paths]]
name = %q
source_addr = "%s"

[[paths]]
name = %q
source_addr = "%s"

%s[wireguard]
private_key = "%s"
listen_port = %d

[[wireguard.peers]]
public_key = "%s"
allowed_ips = ["%s/32"]

[log]
level = "error"
`, psk,
		active.name, active.concIP,
		standby.name, standby.concIP,
		concFEC, concPriv, g30ListenPort, edgePub, g30EdgeInner))

	conc = g30StartProc(t, "concentrator", "nsenter", "-t", strconv.Itoa(top.pid), "-n", bin, "--config", concCfg)
	edge = g30StartProc(t, "edge", bin, "--config", edgeCfg)

	if !top.waitLink(g30TunDev, false, 5*time.Second) {
		t.Fatalf("edge %s never appeared\n%s", g30TunDev, edge.log())
	}
	if !top.waitLink(g30TunDev, true, 5*time.Second) {
		t.Fatalf("concentrator %s never appeared\n%s", g30TunDev, conc.log())
	}
	top.run("ip", "addr", "add", g30EdgeInner+"/24", "dev", g30TunDev)
	top.run("ip", "link", "set", g30TunDev, "up")
	top.nsenter("ip", "addr", "add", g30ConcInner+"/24", "dev", g30TunDev)
	top.nsenter("ip", "link", "set", g30TunDev, "up")
	return edge, conc
}

// TestAdaptiveFECAntiPhaseTwoPath is the permanent D96 fix-(d) regression: a real
// two-path active-backup tunnel proving the T271/T272 scheduler-aware signal selection
// both (1) ramps parity for the ACTIVE path's own sustained loss (the field's "inert"
// half) and (2) does NOT ramp parity for a noisy but data-idle STANDBY's blip (the
// field's "over" half) — the anti-phase pattern the D96 fix replaces must not reproduce.
func TestAdaptiveFECAntiPhaseTwoPath(t *testing.T) {
	bin := h96BuildWanbond(t)

	t.Run("active-loss-ramps-parity", func(t *testing.T) { testG30ActiveLossRampsParity(t, bin) })
	t.Run("standby-blip-does-not-ramp-parity", func(t *testing.T) { testG30StandbyBlipDoesNotRampParity(t, bin) })
}

// testG30ActiveLossRampsParity is acceptance half (1): sustained g30ActiveLossPct loss
// on the ACTIVE (data-carrying) path, injected from the very first packet, must ramp
// wanbond_fec_adaptive_parity above 0, with wanbond_fec_smoothed_loss and
// wanbond_fec_eligible_path_loss both tracking the injected rate and
// wanbond_fec_eligible_paths reading exactly 1 (the active path alone).
func testG30ActiveLossRampsParity(t *testing.T, bin string) {
	active := g30ActivePathSpec
	active.lossPct = g30ActiveLossPct
	standby := g30StandbyPathSpec // clean

	top := g30Setup(t, []g30Path{active, standby})
	edge, conc := g30BuildTunnel(t, top, bin)

	if !top.pingUntil(g30ConcInner, 20*time.Second) {
		t.Fatalf("tunnel never came up (active path carries %.1f%% loss from bring-up)\n--- edge ---\n%s\n--- conc ---\n%s",
			g30ActiveLossPct, edge.log(), conc.log())
	}

	time.Sleep(g30WindowFillWarmupSecs * time.Second)
	time.Sleep(g30SettleSecs * time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	exp := h96FetchMetrics(t, ctx, g30MetricsURL)

	activeLoss, ok := exp.PathValue(metrics.MetricLoss, "active")
	if !ok {
		t.Fatalf("edge /metrics missing %s{path=\"active\"}", metrics.MetricLoss)
	}
	standbyLoss, ok := exp.PathValue(metrics.MetricLoss, "standby")
	if !ok {
		t.Fatalf("edge /metrics missing %s{path=\"standby\"}", metrics.MetricLoss)
	}
	eligiblePaths, ok := exp.Value(metrics.MetricFECEligiblePaths)
	if !ok {
		t.Fatalf("edge /metrics missing %s", metrics.MetricFECEligiblePaths)
	}
	adaptiveParity, ok := exp.Value(metrics.MetricFECAdaptiveParity)
	if !ok {
		t.Fatalf("edge /metrics missing %s — the adaptive-parity gauge did not expose (controller not engaged, or the series is not wired)", metrics.MetricFECAdaptiveParity)
	}
	smoothedLoss, ok := exp.Value(metrics.MetricFECSmoothedLoss)
	if !ok {
		t.Fatalf("edge /metrics missing %s", metrics.MetricFECSmoothedLoss)
	}
	eligibleLoss, ok := exp.Value(metrics.MetricFECEligiblePathLoss)
	if !ok {
		t.Fatalf("edge /metrics missing %s", metrics.MetricFECEligiblePathLoss)
	}

	t.Logf("g30 active-loss: activeLoss=%.4f standbyLoss=%.4f eligiblePaths=%.0f adaptiveParity=%.0f smoothedLoss=%.4f eligibleLoss=%.4f",
		activeLoss, standbyLoss, eligiblePaths, adaptiveParity, smoothedLoss, eligibleLoss)

	// Loss-took-effect guard (anti-vacuity): the ACTIVE path's own probe loss must land
	// in the tolerance band around the injected rate, or a silently-unapplied netem
	// would let the parity assertion below fail cleanly rather than catch the D96
	// regression it exists for.
	if activeLoss < g30LossBandLow || activeLoss > g30LossBandHigh {
		t.Fatalf("active path probe loss %.4f outside [%.2f,%.2f] around the injected %.3f — the netem loss did not take effect as expected",
			activeLoss, g30LossBandLow, g30LossBandHigh, g30ActiveLossPct/100)
	}

	// (1) the field's "inert" half, directly: the active path's sustained loss ramps M.
	if adaptiveParity <= 0 {
		t.Errorf("%s = %.0f, want > 0 (the controller's target M stayed at 0 under the active path's sustained %.1f%% loss — the D96 field symptom)",
			metrics.MetricFECAdaptiveParity, adaptiveParity, g30ActiveLossPct)
	}
	if smoothedLoss < g30LossBandLow || smoothedLoss > g30LossBandHigh {
		t.Errorf("%s = %.4f, want in [%.2f,%.2f] (the controller's EWMA input did not track the active path's real injected loss)",
			metrics.MetricFECSmoothedLoss, smoothedLoss, g30LossBandLow, g30LossBandHigh)
	}
	if eligibleLoss < g30LossBandLow || eligibleLoss > g30LossBandHigh {
		t.Errorf("%s = %.4f, want in [%.2f,%.2f] (the drive's eligible-path signal did not track the active path's loss)",
			metrics.MetricFECEligiblePathLoss, eligibleLoss, g30LossBandLow, g30LossBandHigh)
	}
	// DataPaths() under active-backup carries exactly the one active path — proof the
	// signal came from the data-carrying path, not a role-agnostic scan over both.
	if eligiblePaths != 1 {
		t.Errorf("%s = %.0f, want exactly 1 (only the ACTIVE path carries data under active-backup)",
			metrics.MetricFECEligiblePaths, eligiblePaths)
	}
}

// testG30StandbyBlipDoesNotRampParity is acceptance half (2): a noisy but StateUp,
// data-idle STANDBY — sustained g30StandbyNoiseLossPct loss from the very first packet
// (g30Setup; see g30StandbyNoiseLossPct for why it is sustained rather than a late
// on/off blip) — while the ACTIVE path stays clean, must NOT ramp
// wanbond_fec_adaptive_parity — the field's "over" anti-phase half must not reproduce.
// The final scrape's own standby loss/liveness readings double as the anti-vacuity
// proof that the noise actually registered (and the standby stayed StateUp) — no
// separate mid-test scrape is needed since, unlike a late blip, sustained
// qdisc-creation-time noise is never diluted by a clean prefix.
func testG30StandbyBlipDoesNotRampParity(t *testing.T, bin string) {
	standby := g30StandbyPathSpec
	standby.lossPct = g30StandbyNoiseLossPct
	active := g30ActivePathSpec // clean

	top := g30Setup(t, []g30Path{active, standby})
	edge, conc := g30BuildTunnel(t, top, bin)

	if !top.pingUntil(g30ConcInner, 15*time.Second) {
		t.Fatalf("tunnel never came up\n--- edge ---\n%s\n--- conc ---\n%s", edge.log(), conc.log())
	}

	time.Sleep(g30StandbyNoiseWarmupSecs * time.Second)
	time.Sleep(g30SettleSecs * time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	exp := h96FetchMetrics(t, ctx, g30MetricsURL)

	// Anti-vacuity: the sustained noise actually registered on the standby's OWN
	// per-path estimator, and the standby was liveness StateUp throughout (a "noisy
	// StateUp standby", exactly the task's premise — not a liveness-down non-event).
	standbyLoss, ok := exp.PathValue(metrics.MetricLoss, "standby")
	if !ok {
		t.Fatalf("edge /metrics missing %s{path=\"standby\"}", metrics.MetricLoss)
	}
	standbyUp, ok := exp.PathValue(metrics.MetricUp, "standby")
	if !ok {
		t.Fatalf("edge /metrics missing %s{path=\"standby\"}", metrics.MetricUp)
	}
	t.Logf("g30 standby-noise: standbyLoss=%.4f standbyUp=%.0f", standbyLoss, standbyUp)
	if standbyLoss < g30StandbyNoiseMinLoss {
		t.Fatalf("standby probe loss %.4f < floor %.2f — the injected %.0f%% noise did not register; the 'no ramp' result below would be vacuous",
			standbyLoss, g30StandbyNoiseMinLoss, g30StandbyNoiseLossPct)
	}
	if standbyUp != 1 {
		t.Fatalf("standby liveness = %.0f, want StateUp (1) — the noise knocked the standby DOWN instead of merely noising it; this is not the 'noisy StateUp standby' scenario the task specifies",
			standbyUp)
	}

	eligiblePaths, ok := exp.Value(metrics.MetricFECEligiblePaths)
	if !ok {
		t.Fatalf("edge /metrics missing %s", metrics.MetricFECEligiblePaths)
	}
	adaptiveParity, ok := exp.Value(metrics.MetricFECAdaptiveParity)
	if !ok {
		t.Fatalf("edge /metrics missing %s", metrics.MetricFECAdaptiveParity)
	}
	smoothedLoss, ok := exp.Value(metrics.MetricFECSmoothedLoss)
	if !ok {
		t.Fatalf("edge /metrics missing %s", metrics.MetricFECSmoothedLoss)
	}
	eligibleLoss, ok := exp.Value(metrics.MetricFECEligiblePathLoss)
	if !ok {
		t.Fatalf("edge /metrics missing %s", metrics.MetricFECEligiblePathLoss)
	}
	t.Logf("g30 standby-noise: final eligiblePaths=%.0f adaptiveParity=%.0f smoothedLoss=%.4f eligibleLoss=%.4f",
		eligiblePaths, adaptiveParity, smoothedLoss, eligibleLoss)

	// (2) the field's "over" half, directly: the noisy data-idle standby must NOT ramp M.
	if adaptiveParity != 0 {
		t.Errorf("%s = %.0f, want exactly 0 (the noisy but data-IDLE standby's sustained loss ramped parity — the D96 anti-phase symptom this fix replaces)",
			metrics.MetricFECAdaptiveParity, adaptiveParity)
	}
	if smoothedLoss > g30NoRampMaxLoss {
		t.Errorf("%s = %.4f, want <= %.2f (the controller's EWMA input picked up the standby's blip instead of the clean active path's own loss)",
			metrics.MetricFECSmoothedLoss, smoothedLoss, g30NoRampMaxLoss)
	}
	if eligibleLoss > g30NoRampMaxLoss {
		t.Errorf("%s = %.4f, want <= %.2f (the drive's eligible-path signal picked up the standby's blip instead of the active path's own loss)",
			metrics.MetricFECEligiblePathLoss, eligibleLoss, g30NoRampMaxLoss)
	}
	// DataPaths() under active-backup excludes the standby by construction — the count
	// stays 1 (the clean active path alone) even while the standby is noisy.
	if eligiblePaths != 1 {
		t.Errorf("%s = %.0f, want exactly 1 (only the ACTIVE path carries data under active-backup, regardless of the standby's noise)",
			metrics.MetricFECEligiblePaths, eligiblePaths)
	}
}
