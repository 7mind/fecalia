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

// TestInterfaceUpWithoutExternalIPLinkSet is the I1 e2e acceptance: wanbond0
// reports the administrative UP flag immediately after device.Up brings the
// tunnel up on BOTH roles, with NO external `ip link set up` — closing the
// silent-dead-tunnel footgun where a write to a DOWN TUN yields EIO (D39/NM
// flush territory, but a distinct defect: nothing previously brought the
// interface up at all). It also asserts the daemon assigns NO address:
// addressing stays operator-owned (unlike TestP0PassThrough, which — being a
// data-path test — DOES address and `ip link set up` the interface itself,
// this test must NOT, or it could not tell the daemon's own IFF_UP apart from
// the test's).
func TestInterfaceUpWithoutExternalIPLinkSet(t *testing.T) {
	top := Setup(t)
	p := top.path("starlink")
	bin := buildWanbond(t)

	edgePriv, edgePub := genKey(t)
	concPriv, concPub := genKey(t)
	psk := randKey(t)

	dir := t.TempDir()
	edgeCfg := writeConfig(t, filepath.Join(dir, "edge.toml"), fmt.Sprintf(`role = "edge"
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

	// Concentrator first, then edge — same ordering as TestP0PassThrough. Neither
	// process is given an `ip link set up` at any point in this test.
	conc := top.startProc(t, "concentrator", "nsenter", "-t", strconv.Itoa(top.pid), "-n", bin, "--config", concCfg)
	edge := top.startProc(t, "edge", bin, "--config", edgeCfg)

	if !top.waitLink(tunDev, false, 5*time.Second) {
		t.Fatalf("edge %s never appeared\n%s", tunDev, edge.log())
	}
	if !top.waitLink(tunDev, true, 5*time.Second) {
		t.Fatalf("concentrator %s never appeared\n%s", tunDev, conc.log())
	}

	if !top.linkIsUp(t, tunDev, false) {
		t.Fatalf("edge %s not UP immediately after device.Up (no external `ip link set up` was run)\n%s", tunDev, edge.log())
	}
	if !top.linkIsUp(t, tunDev, true) {
		t.Fatalf("concentrator %s not UP immediately after device.Up (no external `ip link set up` was run)\n%s", tunDev, conc.log())
	}

	if top.hasInetAddr(t, tunDev, false) {
		t.Fatalf("edge %s carries an address the daemon must not have assigned", tunDev)
	}
	if top.hasInetAddr(t, tunDev, true) {
		t.Fatalf("concentrator %s carries an address the daemon must not have assigned", tunDev)
	}

	t.Logf("wanbond0 UP on both roles with no address, no external `ip link set up` (I1)")
}

// linkIsUp reports whether dev's administrative flags include UP (in the peer
// netns when ns is true), parsing the `<FLAG,FLAG,...>` field of `ip -o link
// show`. It requires an exact flag-token match (not a substring check) so it is
// not fooled by LOWER_UP, which always contains "UP" as a substring.
func (top *Topology) linkIsUp(t *testing.T, dev string, ns bool) bool {
	t.Helper()
	var out string
	if ns {
		out = top.runOut("nsenter", "-t", strconv.Itoa(top.pid), "-n", "ip", "-o", "link", "show", dev)
	} else {
		out = top.runOut("ip", "-o", "link", "show", dev)
	}
	open := strings.Index(out, "<")
	shut := strings.Index(out, ">")
	if open < 0 || shut < 0 || shut < open {
		t.Fatalf("no <FLAGS> field in %q", out)
	}
	for _, flag := range strings.Split(out[open+1:shut], ",") {
		if flag == "UP" {
			return true
		}
	}
	return false
}

// hasInetAddr reports whether dev carries an IPv4 address (in the peer netns
// when ns is true), i.e. whether something has run `ip addr add` on it.
func (top *Topology) hasInetAddr(t *testing.T, dev string, ns bool) bool {
	t.Helper()
	var out string
	if ns {
		out = top.runOut("nsenter", "-t", strconv.Itoa(top.pid), "-n", "ip", "-4", "-o", "addr", "show", "dev", dev)
	} else {
		out = top.runOut("ip", "-4", "-o", "addr", "show", "dev", dev)
	}
	return strings.Contains(out, "inet ")
}
