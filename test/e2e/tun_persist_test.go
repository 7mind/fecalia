//go:build e2e

package e2e

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestTUNPersistSurvivesDaemonRestart is the I7/Q38 acceptance: with
// tun_persist=true an operator-assigned address on wanbond0 survives a FULL
// daemon stop/start, and the interface keeps the SAME ifindex across the
// restart — the kernel keeps the persistent device (TUNSETPERSIST) across Close
// (amneziawg-go's NativeTun.Close never issues RTM_DELLINK), and the next Up
// re-adopts it BY NAME via CreateTUN's TUNSETIFF rather than recreating it.
//
// Gated behind the e2e build tag (netns + root); exercised for COMPILATION and
// vet in CI, execution deferred to the privileged hardware suite (G2 pattern).
func TestTUNPersistSurvivesDaemonRestart(t *testing.T) {
	top := Setup(t)
	p := top.path("starlink")
	bin := buildWanbond(t)

	edgePriv, _ := genKey(t)
	_, concPub := genKey(t)
	psk := randKey(t)

	dir := t.TempDir()
	// tun_persist is a top-level scalar (before any table header). The peer is
	// unreachable in this test — the edge boots tolerantly and still creates and
	// persists wanbond0, which is all this device-lifecycle test needs.
	cfg := writeConfig(t, filepath.Join(dir, "edge.toml"), fmt.Sprintf(`role = "edge"
psk = "%s"
tun_persist = true

[[paths]]
name = "starlink"
source_addr = "%s"

[wireguard]
private_key = "%s"

[[wireguard.peers]]
public_key = "%s"
endpoint = "%s:%d"
allowed_ips = ["%s/32"]

[log]
level = "error"
`, psk, p.edgeIP, edgePriv, concPub, p.concIP, listenPort, concInner))

	// First incarnation: bring wanbond0 up, then the operator assigns an address
	// (addressing stays operator-owned — the daemon never assigns one itself).
	edge1 := top.startProc(t, "edge-1", bin, "--config", cfg)
	if !top.waitLink(tunDev, false, 5*time.Second) {
		t.Fatalf("wanbond0 never appeared\n%s", edge1.log())
	}
	const addr = "10.77.0.1"
	top.run("ip", "addr", "add", addr+"/24", "dev", tunDev)
	if !top.hasHostAddr(t, tunDev, false, addr) {
		t.Fatalf("address %s not present after assignment", addr)
	}
	idxBefore := top.ifindex(t, tunDev, false)

	// Stop the daemon fully. With tun_persist=true the kernel keeps wanbond0
	// (TUNSETPERSIST) even though Close dropped the last fd.
	top.stopAndWait(t, edge1)

	if !top.waitLink(tunDev, false, 2*time.Second) {
		t.Fatalf("persistent wanbond0 disappeared after daemon stop (tun_persist=true)")
	}
	if !top.hasHostAddr(t, tunDev, false, addr) {
		t.Fatalf("address %s on persistent wanbond0 was dropped across daemon stop", addr)
	}

	// Second incarnation re-adopts the SAME persistent device by name, so ifindex
	// and the operator-owned address are both preserved across the restart.
	edge2 := top.startProc(t, "edge-2", bin, "--config", cfg)
	if !top.waitLink(tunDev, false, 5*time.Second) {
		t.Fatalf("wanbond0 not present after restart\n%s", edge2.log())
	}
	idxAfter := top.ifindex(t, tunDev, false)
	if idxBefore != idxAfter {
		t.Fatalf("ifindex changed across restart: %d -> %d (persistent device was recreated, not re-adopted)", idxBefore, idxAfter)
	}
	if !top.hasHostAddr(t, tunDev, false, addr) {
		t.Fatalf("address %s lost across the full restart cycle", addr)
	}

	// D39/NM invariant (documented; asserted out of band): a persistent device
	// STILL needs the unmanaged-devices drop-in on NetworkManager hosts. The netns
	// fixture runs no NetworkManager, so there is nothing here to become managed —
	// the invariant lives in docs/install.md and wanbond.example.toml and is
	// asserted on NM hardware, not in this namespace-only fixture.
	t.Logf("wanbond0 survived a full daemon stop/start with a stable ifindex (%d) and address %s intact (I7)", idxBefore, addr)
}

// TestTUNNonPersistDisappearsOnStop is the I7/Q38 default-unchanged case: with
// tun_persist omitted (false) the device is destroyed on Close exactly as before,
// so wanbond0 is GONE after the daemon stops.
func TestTUNNonPersistDisappearsOnStop(t *testing.T) {
	top := Setup(t)
	p := top.path("starlink")
	bin := buildWanbond(t)

	edgePriv, _ := genKey(t)
	_, concPub := genKey(t)
	psk := randKey(t)

	dir := t.TempDir()
	cfg := writeConfig(t, filepath.Join(dir, "edge.toml"), fmt.Sprintf(`role = "edge"
psk = "%s"

[[paths]]
name = "starlink"
source_addr = "%s"

[wireguard]
private_key = "%s"

[[wireguard.peers]]
public_key = "%s"
endpoint = "%s:%d"
allowed_ips = ["%s/32"]

[log]
level = "error"
`, psk, p.edgeIP, edgePriv, concPub, p.concIP, listenPort, concInner))

	edge := top.startProc(t, "edge", bin, "--config", cfg)
	if !top.waitLink(tunDev, false, 5*time.Second) {
		t.Fatalf("wanbond0 never appeared\n%s", edge.log())
	}
	top.stopAndWait(t, edge)

	if !top.waitLinkGone(tunDev, false, 3*time.Second) {
		t.Fatalf("wanbond0 outlived the daemon with tun_persist unset (default teardown regressed)")
	}
	t.Logf("wanbond0 removed on Close with default tun_persist=false (unchanged teardown)")
}

// stopAndWait sends SIGTERM to p and reaps it (SIGKILL after a grace period),
// then disarms the startProc-registered terminator so the deferred cleanup does
// not double-Wait. Used by tests that must observe device state AFTER a full,
// completed daemon stop rather than at test teardown.
func (top *Topology) stopAndWait(t *testing.T, p *proc) {
	t.Helper()
	if p.cmd.Process == nil {
		return
	}
	_ = p.cmd.Process.Signal(syscall.SIGTERM)
	done := make(chan struct{})
	go func() { _ = p.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		_ = p.cmd.Process.Kill()
		<-done
	}
	p.cmd.Process = nil // disarm the t.Cleanup terminator (no double Wait)
}

// waitLinkGone polls until dev is absent (in the peer netns when ns is true), up
// to d. It is the inverse of waitLink.
func (top *Topology) waitLinkGone(dev string, ns bool, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		var err error
		if ns {
			err = top.tryRun("nsenter", "-t", strconv.Itoa(top.pid), "-n", "ip", "link", "show", dev)
		} else {
			err = top.tryRun("ip", "link", "show", dev)
		}
		if err != nil {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// ifindex returns dev's kernel interface index (in the peer netns when ns is
// true), parsed from the leading `<index>:` field of `ip -o link show`.
func (top *Topology) ifindex(t *testing.T, dev string, ns bool) int {
	t.Helper()
	var out string
	if ns {
		out = top.runOut("nsenter", "-t", strconv.Itoa(top.pid), "-n", "ip", "-o", "link", "show", dev)
	} else {
		out = top.runOut("ip", "-o", "link", "show", dev)
	}
	colon := strings.Index(out, ":")
	if colon < 0 {
		t.Fatalf("no ifindex field in %q", out)
	}
	idx, err := strconv.Atoi(strings.TrimSpace(out[:colon]))
	if err != nil {
		t.Fatalf("parse ifindex from %q: %v", out, err)
	}
	return idx
}

// hasHostAddr reports whether dev carries the exact IPv4 host address addr (in
// the peer netns when ns is true). Stricter than hasInetAddr (any inet): it
// matches the specific address assigned so a survival assertion cannot be
// satisfied by an unrelated address.
func (top *Topology) hasHostAddr(t *testing.T, dev string, ns bool, addr string) bool {
	t.Helper()
	var out string
	if ns {
		out = top.runOut("nsenter", "-t", strconv.Itoa(top.pid), "-n", "ip", "-4", "-o", "addr", "show", "dev", dev)
	} else {
		out = top.runOut("ip", "-4", "-o", "addr", "show", "dev", dev)
	}
	return strings.Contains(out, "inet "+addr+"/")
}
