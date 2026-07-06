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

// Two DISTINCT, complete, valid amnezia obfuscation profiles. Both satisfy the D1
// all-or-nothing validation (full junk/size set + a distinct magic-header set with
// every header > 4 so the engine actually rewrites the message-type headers). They
// differ in every field, so an edge running amneziaProfileA cannot handshake with a
// concentrator running amneziaProfileB.
const amneziaProfileA = `[amnezia]
jc = 6
jmin = 24
jmax = 120
s1 = 44
s2 = 68
h1 = 1148981903
h2 = 2019910323
h3 = 3225276945
h4 = 4058030826
`

const amneziaProfileB = `[amnezia]
jc = 8
jmin = 30
jmax = 150
s1 = 50
s2 = 80
h1 = 1234567890
h2 = 2345678901
h3 = 3456789012
h4 = 987654321
`

// TestAmneziaMatchedHandshake is the T19 positive acceptance: with a non-default
// amnezia obfuscation profile set IDENTICALLY on both ends, the tunnel handshakes
// and passes traffic over the multi-path Bind. It then runs a short soak to confirm
// the junk packets amneziawg prepends do not destabilise the Bind (no wedge: ICMP
// keeps flowing and a bulk TCP transfer completes).
func TestAmneziaMatchedHandshake(t *testing.T) {
	bin := buildWanbond(t)
	top := Setup(t)
	edge, conc := setupAmneziaTunnel(t, top, bin, DefaultPaths, amneziaProfileA, amneziaProfileA)

	if !top.pingUntil(concInner, 15*time.Second) {
		t.Fatalf("amnezia tunnel never came up with matched params\n--- edge ---\n%s\n--- conc ---\n%s",
			edge.log(), conc.log())
	}

	// Bulk transfer: exercises the junk-prefixed transport frames end to end.
	mbps := top.iperf3Mbps(t, concInner, 3)
	if mbps <= 0 {
		t.Fatalf("amnezia tunnel: non-positive throughput %.2f Mbit/s", mbps)
	}

	// Short soak: keep pinging; the Bind must stay stable while junk arrives.
	for i := 0; i < 5; i++ {
		if !top.pingUntil(concInner, 3*time.Second) {
			t.Fatalf("amnezia tunnel wedged during soak (iteration %d)\n--- edge ---\n%s", i, edge.log())
		}
	}
	if l := edge.log(); strings.Contains(l, "panic") {
		t.Fatalf("edge daemon reported a panic during the junk soak:\n%s", l)
	}
	t.Logf("amnezia matched: handshake + ping OK over %d paths, iperf3 = %.1f Mbit/s, soak stable",
		len(DefaultPaths), mbps)
}

// TestAmneziaMismatchedFailsClosed is the T19 negative acceptance: with DIFFERENT
// amnezia profiles on the two ends, the WireGuard handshake never completes and no
// traffic flows — the tunnel fails CLOSED rather than falling back to an
// unobfuscated or partially-obfuscated session.
func TestAmneziaMismatchedFailsClosed(t *testing.T) {
	bin := buildWanbond(t)
	top := Setup(t)
	edge, conc := setupAmneziaTunnel(t, top, bin, DefaultPaths, amneziaProfileA, amneziaProfileB)

	if top.pingUntil(concInner, 8*time.Second) {
		t.Fatalf("mismatched amnezia params still handshook — must fail closed\n--- edge ---\n%s\n--- conc ---\n%s",
			edge.log(), conc.log())
	}
	t.Logf("amnezia mismatched: handshake correctly failed closed, no traffic")
}

// setupAmneziaTunnel brings the multipath tunnel up over the given paths with an
// amnezia obfuscation block on each end. It mirrors setupMultipathTunnel but
// injects edgeAmnezia / concAmnezia (each a complete "[amnezia]..." TOML block)
// into the respective config, so the two ends can be driven with matched or
// mismatched profiles.
func setupAmneziaTunnel(t *testing.T, top *Topology, bin string, paths []pathSpec, edgeAmnezia, concAmnezia string) (edge, conc *proc) {
	t.Helper()

	edgePriv, edgePub := genKey(t)
	concPriv, concPub := genKey(t)
	psk := randKey(t)

	var edgePaths, concPaths strings.Builder
	for _, p := range paths {
		fmt.Fprintf(&edgePaths, "[[paths]]\nname = %q\nsource_addr = %q\ndest_addr = \"%s:%d\"\n\n", p.name, p.edgeIP, p.concIP, listenPort)
		fmt.Fprintf(&concPaths, "[[paths]]\nname = %q\nsource_addr = %q\n\n", p.name, p.concIP)
	}
	primary := paths[0]

	dir := t.TempDir()
	edgeCfg := writeConfig(t, filepath.Join(dir, "edge.toml"), fmt.Sprintf(`role = "edge"
psk = "%s"

%s%s
[wireguard]
private_key = "%s"

[[wireguard.peers]]
public_key = "%s"
endpoint = "%s:%d"
allowed_ips = ["%s/32"]

[log]
level = "error"
`, psk, edgePaths.String(), edgeAmnezia, edgePriv, concPub, primary.concIP, listenPort, concInner))

	concCfg := writeConfig(t, filepath.Join(dir, "conc.toml"), fmt.Sprintf(`role = "concentrator"
psk = "%s"

%s%s
[wireguard]
private_key = "%s"
listen_port = %d

[[wireguard.peers]]
public_key = "%s"
allowed_ips = ["%s/32"]

[log]
level = "error"
`, psk, concPaths.String(), concAmnezia, concPriv, listenPort, edgePub, edgeInner))

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
