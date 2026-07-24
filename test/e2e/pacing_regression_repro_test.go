//go:build e2e && pacing_repro

package e2e

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// This opt-in test is executable RED evidence for D112 on
// main@040256256470eec5af976d5477c4deb24652d731.
//
// Disposition: T302 must convert or remove this file when the permanent GREEN
// counter-based netns regression lands. It must not remain an intentionally
// failing artifact when G35 completes.

const (
	reproPacingCapacityFPS = 100.0
	reproPacingBurstFrames = 1.0
	reproClusterGap        = 5 * time.Millisecond
	reproClusterPeriod     = 25 * time.Millisecond
	reproClusters          = 100
	reproPayloadBytes      = 400
	reproSinkPort          = 6012
)

func TestPacingLossPolicerNetnsRepro(t *testing.T) {
	bin := pacingReproWanbond(t)
	top := SetupWithPaths(t, []pathSpec{pacingPath})
	edge, conc := setupPacingReproTunnel(t, top, bin)
	if !top.pingUntil(concInner, 15*time.Second) {
		t.Fatalf("RED fixture: tunnel never came up\n--- edge ---\n%s\n--- concentrator ---\n%s", edge.log(), conc.log())
	}

	sinkAddr := net.JoinHostPort(concInner, strconv.Itoa(reproSinkPort))
	top.startUDPLoadSink(t, sinkAddr)
	dst, err := net.ResolveUDPAddr("udp", sinkAddr)
	if err != nil {
		t.Fatalf("resolve sink %q: %v", sinkAddr, err)
	}
	conn, err := net.DialUDP("udp", &net.UDPAddr{IP: net.ParseIP(edgeInner)}, dst)
	if err != nil {
		t.Fatalf("dial clustered load %s -> %s: %v", edgeInner, sinkAddr, err)
	}
	defer func() { _ = conn.Close() }()

	payload := make([]byte, reproPayloadBytes)
	start := time.Now()
	sentFrames := 0
	sentBytes := 0
	for i := 0; i < reproClusters; i++ {
		clusterStart := time.Now()
		n, err := conn.Write(payload)
		if err != nil {
			t.Fatalf("cluster %d first write: %v", i, err)
		}
		sentFrames++
		sentBytes += n
		time.Sleep(reproClusterGap)
		n, err = conn.Write(payload)
		if err != nil {
			t.Fatalf("cluster %d second write: %v", i, err)
		}
		sentFrames++
		sentBytes += n
		if remain := reproClusterPeriod - time.Since(clusterStart); remain > 0 {
			time.Sleep(remain)
		}
	}
	elapsed := time.Since(start)
	achievedFPS := float64(sentFrames) / elapsed.Seconds()
	if achievedFPS >= reproPacingCapacityFPS {
		t.Fatalf("fixture error: achieved offered rate %.1f fps must stay below declared capacity %.1f fps", achievedFPS, reproPacingCapacityFPS)
	}

	// Give the coalesced one-second scheduler diagnostic time to flush, then prove
	// probe/control traffic still keeps the path usable.
	time.Sleep(1100 * time.Millisecond)
	if !top.pingUntil(concInner, 5*time.Second) {
		t.Fatalf("ClassControl/PROBE control failed after clustered load\n--- edge ---\n%s", edge.log())
	}

	var shedFrames float64
	var shedRecords int
	for _, line := range ParseLogLines(edge.log()) {
		if line.Msg != "scheduler pacer shedding" {
			continue
		}
		shed, ok := line.FieldFloat("shed_frames")
		if !ok {
			t.Fatalf("pacer-shedding record lacks numeric shed_frames: %+v", line)
		}
		shedFrames += shed
		shedRecords++
	}
	t.Logf("RED D112 netns base=040256256470eec5af976d5477c4deb24652d731 offered_frames=%d offered_bytes=%d "+
		"elapsed=%s achieved_offered_fps=%.1f capacity_fps=%.1f pacing_burst_frames=%.1f "+
		"max_application_batch=1 cluster_gap=%s cluster_period=%s shed_records=%d shed_frames=%.0f",
		sentFrames, sentBytes, elapsed, achievedFPS, reproPacingCapacityFPS, reproPacingBurstFrames,
		reproClusterGap, reproClusterPeriod, shedRecords, shedFrames)
	if shedFrames != 0 {
		t.Fatalf("RED D112 netns: an ordinary clustered flow averaged %.1f fps below the declared %.1f-fps capacity, "+
			"and every application write was one frame (<= the %.0f-frame queue budget), yet the pacer shed %.0f frames "+
			"across %d counter records; want wait-not-shed with path control still live",
			achievedFPS, reproPacingCapacityFPS, reproPacingBurstFrames, shedFrames, shedRecords)
	}
}

func pacingReproWanbond(t *testing.T) string {
	t.Helper()
	if bin := os.Getenv("WANBOND_PACING_REPRO_BINARY"); bin != "" {
		return bin
	}
	return buildWanbond(t)
}

func setupPacingReproTunnel(t *testing.T, top *Topology, bin string) (edge, conc *proc) {
	t.Helper()
	edgePriv, edgePub := genKey(t)
	concPriv, concPub := genKey(t)
	psk := randKey(t)
	p := pacingPath
	schedBlock := fmt.Sprintf(`[scheduler]
policy = "active-backup"
per_path_capacity_fps = %.1f
pacing_burst_frames = %.1f
pacing_enabled = true

`, reproPacingCapacityFPS, reproPacingBurstFrames)
	dir := t.TempDir()
	edgeCfg := writeConfig(t, filepath.Join(dir, "edge.toml"), fmt.Sprintf(`role = "edge"
psk = "%s"

[[paths]]
name = %q
source_addr = "%s"
dest_addr = "%s:%d"

%s[wireguard]
private_key = "%s"

[[wireguard.peers]]
public_key = "%s"
endpoint = "%s:%d"
allowed_ips = ["%s/32"]

[log]
level = "info"
`, psk, p.name, p.edgeIP, p.concIP, listenPort, schedBlock, edgePriv, concPub, p.concIP, listenPort, concInner))
	concCfg := writeConfig(t, filepath.Join(dir, "conc.toml"), fmt.Sprintf(`role = "concentrator"
psk = "%s"

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
level = "info"
`, psk, p.name, p.concIP, schedBlock, concPriv, listenPort, edgePub, edgeInner))
	conc = top.startProc(t, "concentrator", "nsenter", "-t", strconv.Itoa(top.pid), "-n", bin, "--config", concCfg)
	edge = top.startProc(t, "edge", bin, "--config", edgeCfg)
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
	return edge, conc
}
