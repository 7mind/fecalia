//go:build e2e

package e2e

import (
	"encoding/json"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestMultipathReRoamSurvivesEdgeIPChange is the T16 acceptance: while a bulk TCP
// transfer runs over the bond, the edge's public IP on ONE path changes (a NAT
// rebind / CGNAT churn, emulated by re-addressing that path's edge veth). That
// path must RECOVER — the concentrator's Bind re-learns the path's new return
// endpoint from the next authenticated probe (T37) — the TCP transfer must
// complete with NO reset (the single virtual endpoint, hence the WireGuard
// session, is preserved), and the OTHER path must be undisturbed.
//
// The path chosen for re-addressing is the PRIMARY: the active-backup scheduler
// runs all traffic over it, so re-roaming it is the strongest test — the flow can
// only survive if that path comes back on its new address (or fails over and then
// back). Recovery is then proven UNAMBIGUOUSLY by blackholing the OTHER path and
// confirming the tunnel still carries traffic: that is possible only if the
// re-roamed primary is itself forwarding again on its new source address.
//
// Requires CAP_NET_ADMIN + /dev/net/tun; the plain `go test` never compiles it
// (e2e build tag), and it is run under the privileged netns harness.
func TestMultipathReRoamSurvivesEdgeIPChange(t *testing.T) {
	bin := buildWanbond(t)
	top := Setup(t)
	edge, conc := setupMultipathTunnel(t, top, bin, DefaultPaths)

	if !top.pingUntil(concInner, 15*time.Second) {
		t.Fatalf("bond never came up\n--- edge ---\n%s\n--- conc ---\n%s", edge.log(), conc.log())
	}

	primary := DefaultPaths[0] // starlink — the active-backup primary
	secondary := DefaultPaths[1]

	// A bulk TCP transfer that spans the re-roam. If the WG session resets, this
	// single connection breaks and iperf3 reports an error / zero sent bytes.
	xfer := top.startTransfer(t, concInner, 14)

	// Let the flow establish and ramp, then re-roam the primary path: the edge's
	// public IP on that path changes to a new host in the same /24. The edge's
	// device-bound path socket keeps sending, now sourced from the new address; the
	// concentrator re-learns it from the next authenticated probe.
	time.Sleep(2 * time.Second)
	newEdgeIP := reHost(primary.edgeIP, 111)
	t.Logf("re-roaming primary path %q: edge %s -> %s (mid-transfer)", primary.name, primary.edgeIP, newEdgeIP)
	top.Readdress(primary.name, newEdgeIP)

	// The tunnel must recover within the failover/recovery budget — the flow is
	// carried again end to end.
	if !top.pingUntil(concInner, time.Duration(P1RecoverySeconds)*time.Second+2*time.Second) {
		t.Fatalf("tunnel did not recover after re-roaming primary path %q to %s\n--- edge ---\n%s\n--- conc ---\n%s",
			primary.name, newEdgeIP, edge.log(), conc.log())
	}

	// The OTHER path is undisturbed by the re-roam (still reachable at L3).
	if !top.Reachable(secondary.name, 3) {
		t.Fatalf("secondary path %q became unreachable after re-roaming the primary", secondary.name)
	}

	// Prove the RE-ROAMED path itself recovered (not merely that the flow survived on
	// the backup): blackhole the secondary, so only the re-roamed primary can carry
	// traffic. The tunnel staying up means the primary is forwarding on its NEW
	// address — the concentrator re-learned the roamed endpoint.
	top.Blackhole(secondary.name)
	if !top.pingUntil(concInner, time.Duration(P1RecoverySeconds)*time.Second+2*time.Second) {
		t.Fatalf("re-roamed primary path %q did not recover: tunnel down once the backup %q was blackholed\n--- edge ---\n%s\n--- conc ---\n%s",
			primary.name, secondary.name, edge.log(), conc.log())
	}
	top.Restore(secondary.name)

	// The transfer must have completed across the whole window with NO reset: a
	// preserved single virtual endpoint keeps one WireGuard session, so the one TCP
	// connection survives the source-address change.
	mbps := xfer.result(t)
	if mbps <= 0 {
		t.Fatalf("TCP transfer across the re-roam measured non-positive throughput %.2f Mbit/s — the flow was reset", mbps)
	}

	// Both edge veths still exist: the re-roam re-addressed one link, it did not tear
	// the topology down.
	if !top.LinkExists(primary.name) || !top.LinkExists(secondary.name) {
		t.Fatalf("edge veth missing after re-roam: primary exists=%v secondary exists=%v",
			top.LinkExists(primary.name), top.LinkExists(secondary.name))
	}
	t.Logf("re-roam survived: primary %q recovered on %s, transfer completed at %.1f Mbit/s with no reset",
		primary.name, newEdgeIP, mbps)
}

// transfer is a background iperf3 TCP client whose completion (and reported
// throughput) is awaited via result(). It carries a single TCP connection so a WG
// session reset during the run surfaces as an error or zero sent bytes.
type transfer struct {
	cmd *exec.Cmd
	out *lockedBuffer
}

// startTransfer starts an iperf3 server in the concentrator netns and a client on
// the edge that transfers for secs seconds, returning immediately. The transfer
// spans a mid-run re-roam; result() awaits it and returns the sender throughput.
func (top *Topology) startTransfer(t *testing.T, serverIP string, secs int) *transfer {
	t.Helper()
	top.startProc(t, "iperf3-server", "nsenter", "-t", strconv.Itoa(top.pid), "-n", "iperf3", "-s", "-1", "-B", serverIP)
	time.Sleep(500 * time.Millisecond) // allow the server to bind and listen

	cmd := exec.Command("iperf3", "-c", serverIP, "-t", strconv.Itoa(secs), "-J")
	out := &lockedBuffer{}
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		t.Fatalf("start iperf3 client: %v", err)
	}
	return &transfer{cmd: cmd, out: out}
}

// result waits for the transfer to finish and returns the sender throughput in
// Mbit/s. A non-nil exit error (a reset connection) fails the test.
func (x *transfer) result(t *testing.T) float64 {
	t.Helper()
	if err := x.cmd.Wait(); err != nil {
		t.Fatalf("iperf3 transfer failed (connection reset during re-roam?): %v\n%s", err, x.out.String())
	}
	var r struct {
		End struct {
			SumSent struct {
				BitsPerSecond float64 `json:"bits_per_second"`
			} `json:"sum_sent"`
		} `json:"end"`
	}
	if err := json.Unmarshal([]byte(x.out.String()), &r); err != nil {
		t.Fatalf("parse iperf3 json: %v\n%s", err, x.out.String())
	}
	return r.End.SumSent.BitsPerSecond / 1e6
}

// reHost replaces the last octet of a dotted-quad IPv4 address with host, used to
// pick a fresh edge address in the same /24 for the re-roam.
func reHost(ip string, host int) string {
	i := strings.LastIndex(ip, ".")
	if i < 0 {
		return ip
	}
	return ip[:i+1] + strconv.Itoa(host)
}
