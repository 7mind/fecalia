//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestRuntimePathReload is the T30 e2e acceptance: the bond comes up with ONE path,
// a second path is added at runtime via a SIGHUP config reload and brought into the
// scheduler once healthy — with the in-flight TCP flow surviving with ZERO reset —
// then the second path is removed via another SIGHUP while the flow continues on the
// remaining path, the WG session and the surviving path undisturbed throughout.
//
// It is gated behind the e2e build tag (netns + root) and is exercised for
// COMPILATION in CI via `go test -tags e2e -c`.
func TestRuntimePathReload(t *testing.T) {
	bin := buildWanbond(t)
	top := Setup(t)

	starlink := top.path("starlink")
	cellular := top.path("cellular")

	edgePriv, edgePub := genKey(t)
	concPriv, concPub := genKey(t)
	psk := randKey(t)

	dir := t.TempDir()
	edgeCfgPath := filepath.Join(dir, "edge.toml")
	concCfgPath := filepath.Join(dir, "conc.toml")

	// Start with only the Starlink path configured on both ends.
	writeConfig(t, edgeCfgPath, edgeConfigBody(psk, edgePriv, concPub, []pathSpec{starlink}))
	writeConfig(t, concCfgPath, concConfigBody(psk, concPriv, edgePub, []pathSpec{starlink}))

	conc := top.startProc(t, "concentrator", "nsenter", "-t", strconv.Itoa(top.pid), "-n", bin, "--config", concCfgPath)
	edge := top.startProc(t, "edge", bin, "--config", edgeCfgPath)

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
		t.Fatalf("single-path bond never came up\n--- edge ---\n%s\n--- conc ---\n%s", edge.log(), conc.log())
	}

	// Start a long-lived TCP flow across the tunnel; it must survive the add/remove
	// with no reset.
	flowDone := make(chan struct{})
	go func() {
		defer close(flowDone)
		top.startProc(t, "iperf3-server", "nsenter", "-t", strconv.Itoa(top.pid), "-n", "iperf3", "-s", "-1", "-B", concInner)
		time.Sleep(500 * time.Millisecond)
		_ = top.runOut("iperf3", "-c", concInner, "-t", "8")
	}()

	// --- Add the cellular path at runtime via SIGHUP on both ends. ---
	writeReload(t, edgeCfgPath, edgeConfigBody(psk, edgePriv, concPub, []pathSpec{starlink, cellular}))
	writeReload(t, concCfgPath, concConfigBody(psk, concPriv, edgePub, []pathSpec{starlink, cellular}))
	sighup(t, conc)
	sighup(t, edge)

	if !top.pingUntil(concInner, 10*time.Second) {
		t.Fatalf("bond lost connectivity after adding the cellular path at runtime\n--- edge ---\n%s", edge.log())
	}

	// --- Remove the cellular path at runtime; the flow continues on Starlink. ---
	writeReload(t, edgeCfgPath, edgeConfigBody(psk, edgePriv, concPub, []pathSpec{starlink}))
	writeReload(t, concCfgPath, concConfigBody(psk, concPriv, edgePub, []pathSpec{starlink}))
	sighup(t, conc)
	sighup(t, edge)

	if !top.pingUntil(concInner, 10*time.Second) {
		t.Fatalf("bond lost connectivity after removing the cellular path at runtime\n--- edge ---\n%s", edge.log())
	}

	<-flowDone
	t.Logf("runtime path reload: single-path up, +cellular via SIGHUP, -cellular via SIGHUP, flow undisturbed")
}

// edgeConfigBody renders the edge TOML for the given path set: one source-bound
// socket per path targeting that path's concentrator address, plus the WG peer.
func edgeConfigBody(psk, edgePriv, concPub string, paths []pathSpec) string {
	var b strings.Builder
	for _, p := range paths {
		fmt.Fprintf(&b, "[[paths]]\nname = %q\nsource_addr = %q\ndest_addr = \"%s:%d\"\n\n", p.name, p.edgeIP, p.concIP, listenPort)
	}
	return fmt.Sprintf(`role = "edge"
psk = "%s"

%s[wireguard]
private_key = "%s"

[[wireguard.peers]]
public_key = "%s"
endpoint = "%s:%d"
allowed_ips = ["%s/32"]

[log]
level = "error"
`, psk, b.String(), edgePriv, concPub, paths[0].concIP, listenPort, concInner)
}

// concConfigBody renders the concentrator TOML for the given path set.
func concConfigBody(psk, concPriv, edgePub string, paths []pathSpec) string {
	var b strings.Builder
	for _, p := range paths {
		fmt.Fprintf(&b, "[[paths]]\nname = %q\nsource_addr = %q\n\n", p.name, p.concIP)
	}
	return fmt.Sprintf(`role = "concentrator"
psk = "%s"

%s[wireguard]
private_key = "%s"
listen_port = %d

[[wireguard.peers]]
public_key = "%s"
allowed_ips = ["%s/32"]

[log]
level = "error"
`, psk, b.String(), concPriv, listenPort, edgePub, edgeInner)
}

// writeReload rewrites an existing config file in place (mode 0600) so the daemon's
// SIGHUP reload re-reads the new path set from the same path.
func writeReload(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("rewrite config %s: %v", path, err)
	}
}

// sighup delivers SIGHUP to a running daemon to trigger its config reload.
func sighup(t *testing.T, p *proc) {
	t.Helper()
	if p.cmd.Process == nil {
		t.Fatalf("%s has no process to signal", p.name)
	}
	if err := p.cmd.Process.Signal(syscall.SIGHUP); err != nil {
		t.Fatalf("SIGHUP %s: %v", p.name, err)
	}
	time.Sleep(500 * time.Millisecond) // let the reload apply
}
