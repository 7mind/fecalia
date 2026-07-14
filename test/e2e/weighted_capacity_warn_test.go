//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/metrics"
)

// T144 (G13, Q52 WARN arm): the complementary soft-verdict to T142's hard-fail guard.
// When policy = "weighted" and link_bandwidth is NOT fully declared across every path,
// startup must NEVER be blocked — the daemon logs ONE actionable startup WARN and
// exposes wanbond_weighted_capacity_sane = 0. This file asserts on the daemon's OWN
// startProc combined log output (proc.log(), via ParseLogLines — NOT the T141
// MetricsSampler), so M52 stays an INDEPENDENT root; this task's dependsOn remains
// [T142] only.
const (
	// t144MetricsListen is the edge's /metrics endpoint for this file's tests, on a
	// port none of the other e2e files use (see the metrics-port registry in netns.go).
	t144MetricsListen = "127.0.0.1:9106"
	t144MetricsURL    = "http://" + t144MetricsListen + "/metrics"

	// t144WarnMsg is the exact "msg" field cmd/wanbond/main.go's
	// warnUnverifiableWeightedCapacity logs (T144) — pinned here so a regression in
	// either the daemon or this test's expectation surfaces as a clear mismatch
	// rather than a silently-never-matching substring.
	t144WarnMsg = "weighted policy: link_bandwidth capacity is unverifiable"

	// t144GuardConsistentSched is the [scheduler] block every weighted-policy subtest
	// below uses: per_path_capacity_fps lowered to 700 keeps the T142 hard-fail guard
	// PASSING for an 8mbit declared path (engage_fraction(0.9)*700=630 fps sits
	// at/below the ~666.7 fps 8mbit-implied ceiling — the same sizing
	// weighted_engage_guard_test.go and internal/config/weighted_capacity_sane_test.go
	// use), so this file exercises ONLY the T144 verdict, never T142's guard.
	t144GuardConsistentSched = "\n[scheduler]\npolicy = \"weighted\"\nper_path_capacity_fps = 700\n"

	// t144MetricsWaitTimeout bounds how long the edge's /metrics endpoint may take to
	// come up after the process starts (TUN creation, engine bring-up, THEN the
	// metrics listener — see internal/device.up) and the WARN wait after that.
	t144MetricsWaitTimeout = 15 * time.Second
)

// setupT144WeightedEdge starts a SINGLE edge daemon (no concentrator process is
// needed — T144 is purely a config-load-time / startup-log accounting concern, not an
// established tunnel; the peer endpoint below is deliberately unreachable, mirroring
// tolerant_startup_test.go's writeT60EdgeConfig precedent) over the two DefaultPaths
// uplinks (real, netns-assigned source addrs, so Open() genuinely succeeds and the
// daemon keeps running). bandwidths[i] == "" leaves that path's link_bandwidth
// undeclared; a non-empty value declares it (with a fixed link_rtt). schedulerBlock is
// spliced in verbatim ("" leaves the scheduler at its default, active-backup). [log]
// level is "info" (not "error") so the Warn-level T144 diagnostic is captured.
func setupT144WeightedEdge(t *testing.T, top *Topology, bin string, paths []pathSpec, bandwidths []string, schedulerBlock string) *proc {
	t.Helper()
	edgePriv, _ := genKey(t)
	_, concPub := genKey(t)
	psk := randKey(t)

	var edgePaths strings.Builder
	for i, p := range paths {
		fmt.Fprintf(&edgePaths, "[[paths]]\nname = %q\nsource_addr = %q\n", p.name, p.edgeIP)
		if i < len(bandwidths) && bandwidths[i] != "" {
			fmt.Fprintf(&edgePaths, "link_bandwidth = %q\nlink_rtt = \"20ms\"\n", bandwidths[i])
		}
		edgePaths.WriteString("\n")
	}

	dir := t.TempDir()
	edgeCfg := writeConfig(t, filepath.Join(dir, "edge.toml"), fmt.Sprintf(`role = "edge"
psk = "%s"

%s[metrics]
listen = "%s"

[wireguard]
private_key = "%s"

[[wireguard.peers]]
public_key = "%s"
endpoint = "192.0.2.53:51820"
allowed_ips = ["10.10.0.2/32"]

[log]
level = "info"
%s`, psk, edgePaths.String(), t144MetricsListen, edgePriv, concPub, schedulerBlock))

	return top.startProc(t, "edge", bin, "--config", edgeCfg)
}

// waitT144MetricsReady polls url until a scrape succeeds (the metrics endpoint binds
// only near the END of device.up, after TUN/engine bring-up), returning the first
// successful Exposition, or fails the test at deadline.
func waitT144MetricsReady(t *testing.T, url string, deadline time.Duration) metrics.Exposition {
	t.Helper()
	end := time.Now().Add(deadline)
	var lastErr error
	for time.Now().Before(end) {
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		exp, err := metrics.Fetch(ctx, http.DefaultClient, url)
		cancel()
		if err == nil {
			return exp
		}
		lastErr = err
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("metrics endpoint %s never became scrapeable within %s (last error: %v)", url, deadline, lastErr)
	return metrics.Exposition{}
}

// countT144WarnLines counts the log records in logText whose msg equals t144WarnMsg.
func countT144WarnLines(logText string) int {
	n := 0
	for _, l := range ParseLogLines(logText) {
		if l.Msg == t144WarnMsg {
			n++
		}
	}
	return n
}

// TestWeightedCapacityWarnUndeclared is T144 acceptance (a): a weighted daemon with
// NO link_bandwidth on any path starts on the fixture, its combined output contains
// EXACTLY ONE capacity-sanity WARN line, and metrics.Fetch reads
// wanbond_weighted_capacity_sane == 0.
func TestWeightedCapacityWarnUndeclared(t *testing.T) {
	bin := buildWanbond(t)
	top := Setup(t)

	edge := setupT144WeightedEdge(t, top, bin, DefaultPaths, nil, t144GuardConsistentSched)
	if !top.waitLink(tunDev, false, 5*time.Second) {
		t.Fatalf("edge %s never appeared\n%s", tunDev, edge.log())
	}

	exp := waitT144MetricsReady(t, t144MetricsURL, t144MetricsWaitTimeout)
	if got := countT144WarnLines(edge.log()); got != 1 {
		t.Fatalf("capacity-sanity WARN logged %d times, want exactly 1 (no path declared link_bandwidth)\n%s", got, edge.log())
	}
	v, ok := exp.Value(metrics.MetricWeightedCapacitySane)
	if !ok {
		t.Fatalf("%s series absent, want present (UNVERIFIABLE = 0)", metrics.MetricWeightedCapacitySane)
	}
	if v != 0 {
		t.Fatalf("%s = %v, want 0 (UNVERIFIABLE — no path declared link_bandwidth)", metrics.MetricWeightedCapacitySane, v)
	}
	t.Logf("T144 undeclared: exactly one WARN, %s = 0", metrics.MetricWeightedCapacitySane)
}

// TestWeightedCapacityWarnPartiallyDeclared is T144 acceptance (b) — the R155-pinned
// reachable state: a PARTIAL declaration (link_bandwidth on the first path, not the
// second; pacing disabled, the shipped default; the declared path is
// guard-consistent) also starts, emits EXACTLY ONE WARN, and reads gauge == 0. The
// DECLARED path being guard-consistent proves this WARN is about the UNDECLARED
// path, not a T142 guard failure masquerading as one.
func TestWeightedCapacityWarnPartiallyDeclared(t *testing.T) {
	bin := buildWanbond(t)
	top := Setup(t)

	edge := setupT144WeightedEdge(t, top, bin, DefaultPaths, []string{"8mbit", ""}, t144GuardConsistentSched)
	if !top.waitLink(tunDev, false, 5*time.Second) {
		t.Fatalf("edge %s never appeared\n%s", tunDev, edge.log())
	}

	exp := waitT144MetricsReady(t, t144MetricsURL, t144MetricsWaitTimeout)
	if got := countT144WarnLines(edge.log()); got != 1 {
		t.Fatalf("capacity-sanity WARN logged %d times, want exactly 1 (a partial link_bandwidth declaration)\n%s", got, edge.log())
	}
	v, ok := exp.Value(metrics.MetricWeightedCapacitySane)
	if !ok {
		t.Fatalf("%s series absent, want present (UNVERIFIABLE = 0)", metrics.MetricWeightedCapacitySane)
	}
	if v != 0 {
		t.Fatalf("%s = %v, want 0 (UNVERIFIABLE — a partial declaration)", metrics.MetricWeightedCapacitySane, v)
	}
	t.Logf("T144 partial: exactly one WARN, %s = 0", metrics.MetricWeightedCapacitySane)
}

// TestWeightedCapacitySaneNoWarnWhenAllDeclared is T144 acceptance (c): link_bandwidth
// declared on ALL paths (guard-consistent capacity) starts with NO WARN and gauge == 1.
func TestWeightedCapacitySaneNoWarnWhenAllDeclared(t *testing.T) {
	bin := buildWanbond(t)
	top := Setup(t)

	edge := setupT144WeightedEdge(t, top, bin, DefaultPaths, []string{"8mbit", "8mbit"}, t144GuardConsistentSched)
	if !top.waitLink(tunDev, false, 5*time.Second) {
		t.Fatalf("edge %s never appeared\n%s", tunDev, edge.log())
	}

	exp := waitT144MetricsReady(t, t144MetricsURL, t144MetricsWaitTimeout)
	if got := countT144WarnLines(edge.log()); got != 0 {
		t.Fatalf("capacity-sanity WARN logged %d times, want 0 (every path declared link_bandwidth)\n%s", got, edge.log())
	}
	v, ok := exp.Value(metrics.MetricWeightedCapacitySane)
	if !ok {
		t.Fatalf("%s series absent, want present (SANE-VERIFIED = 1)", metrics.MetricWeightedCapacitySane)
	}
	if v != 1 {
		t.Fatalf("%s = %v, want 1 (SANE-VERIFIED — every path declared link_bandwidth)", metrics.MetricWeightedCapacitySane, v)
	}
	t.Logf("T144 all-declared: no WARN, %s = 1", metrics.MetricWeightedCapacitySane)
}

// TestWeightedCapacitySaneAbsentUnderActiveBackup is T144 acceptance (d): under the
// active-backup policy (the default — no [scheduler] block at all) the
// wanbond_weighted_capacity_sane family is absent ENTIRELY, not present at 0.
func TestWeightedCapacitySaneAbsentUnderActiveBackup(t *testing.T) {
	bin := buildWanbond(t)
	top := Setup(t)

	edge := setupT144WeightedEdge(t, top, bin, DefaultPaths, nil, "")
	if !top.waitLink(tunDev, false, 5*time.Second) {
		t.Fatalf("edge %s never appeared\n%s", tunDev, edge.log())
	}

	exp := waitT144MetricsReady(t, t144MetricsURL, t144MetricsWaitTimeout)
	if exp.Has(metrics.MetricWeightedCapacitySane) {
		t.Fatalf("%s registered under the active-backup policy, want absent entirely\n%s", metrics.MetricWeightedCapacitySane, edge.log())
	}
	if got := countT144WarnLines(edge.log()); got != 0 {
		t.Fatalf("capacity-sanity WARN logged %d times under active-backup, want 0 (not applicable)\n%s", got, edge.log())
	}
	t.Logf("T144 active-backup: %s absent, no WARN", metrics.MetricWeightedCapacitySane)
}
