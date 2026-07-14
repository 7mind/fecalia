//go:build e2e

package e2e

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// T142 (G13, Q52/Q53 hard-fail guard): a path that declares link_bandwidth under the
// weighted policy must be able to sustain the aggregation engage threshold
// (engage_fraction * per_path_capacity_fps), or aggregation can mathematically never
// engage at line rate — config.Load now refuses such a config outright. These are the
// two DAEMON-level acceptance cases; the config-load semantics themselves (all three
// numbers named, the lowered-capacity negative case) are unit-tested in
// internal/config/weighted_engage_guard_test.go.

// TestWeightedEngageGuardRefusesAtStartup is acceptance (i): a daemon launched with
// policy = "weighted", link_bandwidth = "8mbit" (+ link_rtt), pacing DISABLED, and the
// default per_path_capacity_fps (10000) REFUSES to start — config.Load's guard
// rejects it before the daemon ever logs "wanbond starting" (the malformed-source_addr
// precedent in TestTolerantStartupFastFailModes uses the same marker to distinguish a
// config-load failure from a fatal Open), exiting non-zero with an actionable error
// naming the path, the declared bandwidth, the implied capacity fps, and the
// engage-threshold fps.
func TestWeightedEngageGuardRefusesAtStartup(t *testing.T) {
	bin := buildWanbond(t)
	cfg := writeT60EdgeConfig(t, `[[paths]]
name = "starlink"
source_addr = "192.0.2.10"
link_bandwidth = "8mbit"
link_rtt = "20ms"

[scheduler]
policy = "weighted"
`)

	code, out := runWanbondOnce(t, bin, cfg, 10*time.Second)
	if code == 0 {
		t.Fatalf("wanbond exited 0 with an engage threshold that can never be reached at the declared link_bandwidth (8mbit); want a fatal config-load error\n%s", out)
	}
	if strings.Contains(out, "wanbond starting") {
		t.Fatalf("the engage-vs-bandwidth guard is a config LOAD error and must reject before the daemon ever starts; output:\n%s", out)
	}
	// 8mbit implies ~666.7 frames/s; the default engage_fraction(0.9) *
	// per_path_capacity_fps(10000) = 9000.0 frames/s exceeds it — see
	// internal/config/config.go's validateWeightedEngageAgainstBandwidth.
	for _, want := range []string{"starlink", "8mbit", "666.7", "9000.0"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected the actionable error to name %q (path / declared bandwidth / implied capacity fps / engage-threshold fps); output:\n%s", want, out)
		}
	}
	t.Logf("weighted engage guard refused as expected: exit %d\n%s", code, out)
}

// TestWeightedEngageGuardPassesWhenLowered is acceptance (ii): the SAME declared
// link_bandwidth as above, but with per_path_capacity_fps lowered so
// engage_fraction * per_path_capacity_fps <= the bandwidth-implied capacity, starts
// normally and establishes the tunnel end to end (handshake + ping), mirroring
// TestP0PassThrough's single-path bring-up sequence.
func TestWeightedEngageGuardPassesWhenLowered(t *testing.T) {
	top := Setup(t)
	p := top.path("starlink")
	bin := buildWanbond(t)

	edgePriv, edgePub := genKey(t)
	concPriv, concPub := genKey(t)
	psk := randKey(t)

	dir := t.TempDir()
	// per_path_capacity_fps = 700: engage_fraction defaults to 0.9, so the threshold
	// (630 fps) stays at/below the 8mbit-implied ~666.7 fps ceiling — the same lowered
	// value the config-load unit test (TestWeightedEngageAgainstBandwidthPassesWhenLowered)
	// uses.
	edgeCfg := writeConfig(t, filepath.Join(dir, "edge.toml"), fmt.Sprintf(`role = "edge"
psk = "%s"

[[paths]]
name = "starlink"
source_addr = "%s"
link_bandwidth = "8mbit"
link_rtt = "20ms"

[wireguard]
private_key = "%s"

[[wireguard.peers]]
public_key = "%s"
endpoint = "%s:%d"
allowed_ips = ["%s/32"]

[scheduler]
policy = "weighted"
per_path_capacity_fps = 700

[log]
level = "error"
`, psk, p.edgeIP, edgePriv, concPub, p.concIP, listenPort, concInner))

	concCfg := writeConfig(t, filepath.Join(dir, "conc.toml"), fmt.Sprintf(`role = "concentrator"
psk = "%s"

[[paths]]
name = "starlink"
source_addr = "%s"

[wireguard]
private_key = "%s"
listen_port = %d

[[wireguard.peers]]
public_key = "%s"
allowed_ips = ["%s/32"]

[log]
level = "error"
`, psk, p.concIP, concPriv, listenPort, edgePub, edgeInner))

	conc := top.startProc(t, "concentrator", "nsenter", "-t", strconv.Itoa(top.pid), "-n", bin, "--config", concCfg)
	edge := top.startProc(t, "edge", bin, "--config", edgeCfg)

	if !top.waitLink(tunDev, false, 5*time.Second) {
		t.Fatalf("edge %s never appeared\n%s", tunDev, edge.log())
	}
	if !top.waitLink(tunDev, true, 5*time.Second) {
		t.Fatalf("concentrator %s never appeared\n%s", tunDev, conc.log())
	}
	top.run("ip", "addr", "add", edgeInner+"/24", "dev", tunDev)
	top.run("ip", "link", "set", tunDev, "up")
	top.nsenter("ip", "addr", "add", concInner+"/24", "dev", tunDev)
	top.nsenter("ip", "link", "set", tunDev, "up")

	if !top.pingUntil(concInner, 15*time.Second) {
		t.Fatalf("tunnel never came up with the lowered per_path_capacity_fps=700: %s unreachable through the tunnel\n--- edge ---\n%s\n--- concentrator ---\n%s",
			concInner, edge.log(), conc.log())
	}
	t.Logf("weighted engage guard: lowered per_path_capacity_fps started and established the tunnel (handshake + ping OK)")
}
