//go:build e2e

package e2e

// TestE2EConstrainedPathMTUKnob (T210, D85) is the netns reproduction-and-fix gate
// for the STATIC-KNOB half of D85: a bonded path whose real outer link MTU (1400)
// is below the 1500 the daemon assumes shreds full-size inner traffic, and the
// per-path `mtu` knob (T200) + min-across-paths TUN sizing (T205) resolves it.
//
// HARDWARE TIER — DO NOT RUN IN THE DEFAULT GATE. It needs root, /dev/net/tun, tc,
// and nftables inside a network namespace, so it is compiled under //go:build e2e
// and executed ONLY on the privileged netns/real-host tier (`just e2e` / the
// standing worker machines), never by `go test ./...`. The non-privileged gate only
// COMPILES + `go vet -tags e2e`s + lints it.
//
// Mechanism (R241 — why a lossless netns needs a middlebox, reconciled with T201):
// the field loss came from the 5G network DROPPING the IP fragments a too-large
// outer datagram produced. On THIS Linux netns tier that in-network fragment drop
// is NOT the operative mechanism, because the T201 Don't-Fragment policy
// (setDontFragment → IP_PMTUDISC_DO on every outer path socket) makes an over-MTU
// outer send fail with EMSGSIZE at the EDGE — the datagram is dropped and counted
// (internal/bind.accountSendError) before it ever reaches the wire, so no fragments
// are produced to drop. The test therefore reproduces the SAME functional outcome
// (full-size inner traffic over the constrained path is lost) via that edge-side
// EMSGSIZE drop, AND still installs the field's fragment-drop middlebox
// (installFragmentDrop) in the transit netns: it faithfully mirrors the field, and
// it becomes the operative loss mechanism on any platform where the DF sockopt is a
// no-op (pathsock_other.go), keeping the negative control valid across DF policies.
//
// Two phases, both over the constrained path:
//   - repro_no_knob (negative control): NO `mtu` knob → wanbond0 is mis-sized to
//     InnerMTU(1500); a full-wanbond0-MTU DF flow encapsulates to ~1500 outer,
//     exceeds the 1400 link, and is lost > reproMinLossPct.
//   - knob_1400 (fix): `mtu = 1400` on the constrained path → wanbond0 is sized to
//     InnerMTU(1400) (the min across paths, T205); the same full-MTU flow keeps
//     every outer datagram <= 1400, produces ZERO fragmented outer datagrams, and
//     completes with <= knobMaxLossPct loss.
//
// Per AGENTS.md the netns fixture validates FUNCTIONAL bonding, not throughput, so
// the assertions are loss FRACTIONS and the fragment count, never Mbit/s.

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/bind"
)

const (
	// constrainedOuterMTU is the emulated 5G-like outer link MTU: below the 1500 the
	// daemon assumes by default, so a full-1500-budget outer datagram overflows it.
	constrainedOuterMTU = 1400

	// icmpIPv4Overhead is the outer IPv4 (20) + ICMP (8) header cost added to a ping's
	// -s payload, used to size a payload that exactly fills a given TUN MTU.
	icmpIPv4Overhead = 20 + 8

	// mtuProbePings is the DF ping count per measured flow; loss is the fraction lost.
	mtuProbePings = 10

	// mtuFlowCaptureWindow bounds the fragment capture that runs concurrently with the
	// knob-phase flow.
	mtuFlowCaptureWindow = 8 * time.Second

	// reproMinLossPct is the loss floor the negative control must exceed: a mis-sized
	// wanbond0 over the constrained path loses (nearly all) full-MTU traffic.
	reproMinLossPct = 50.0

	// knobMaxLossPct is the loss ceiling the knob phase must stay under: a correctly
	// sized wanbond0 completes the full-MTU flow (a small allowance absorbs a lost
	// warmup/reroute datagram in the netns).
	knobMaxLossPct = 20.0

	// fragDropTable / fragDropChain name the nftables table+chain installed in the
	// transit (concentrator) netns that drops IP fragments, mirroring the field 5G
	// middlebox (R241).
	fragDropTable = "wbmtu"
	fragDropChain = "fragdrop"
)

func TestE2EConstrainedPathMTUKnob(t *testing.T) {
	bin := buildWanbond(t)

	// The constrained path is the PRIMARY (paths[0]) so the "first healthy path"
	// scheduler rides it; the healthy 1500 path exists so the daemon "assumes 1500"
	// for a real second path (exercising the min-across-paths TUN sizing, T205). The
	// measured flow is forced onto the constrained path by blackholing the healthy one.
	constrained := pathSpec{name: "cellular", edgeIP: "10.100.2.1", concIP: "10.100.2.2", edgeVeth: "wbBe", concVeth: "wbBc", delayMs: 20, outerMTU: constrainedOuterMTU}
	healthy := pathSpec{name: "starlink", edgeIP: "10.100.1.1", concIP: "10.100.1.2", edgeVeth: "wbAe", concVeth: "wbAc", delayMs: 20}
	paths := []pathSpec{constrained, healthy}

	// Phase 1 — reproduction / negative control: no mtu knob.
	t.Run("repro_no_knob", func(t *testing.T) {
		top := SetupWithPaths(t, paths)
		installFragmentDrop(t, top)
		edge, conc := setupConstrainedTunnel(t, top, bin, paths, "", 0)
		if !top.pingUntil(concInner, 15*time.Second) {
			t.Fatalf("tunnel never came up\n--- edge ---\n%s\n--- conc ---\n%s", edge.log(), conc.log())
		}

		// With no declared MTU the daemon assumes 1500 for both paths, so wanbond0 is
		// mis-sized to InnerMTU(1500) — larger than the constrained path can carry.
		wantTun := bind.InnerMTU(bind.DefaultPathMTU, false)
		if got := top.linkMTU(t, tunDev, false); got != wantTun {
			t.Fatalf("phase-1 %s MTU = %d, want mis-sized InnerMTU(1500) = %d", tunDev, got, wantTun)
		}

		top.Blackhole(healthy.name) // force the flow onto the constrained path
		payload := wantTun - icmpIPv4Overhead
		loss := top.tunnelDFPingLossPct(t, concInner, payload)
		if loss <= reproMinLossPct {
			t.Fatalf("phase-1 negative control did NOT reproduce D85: a full-MTU (%d-byte inner) DF flow over the %d-MTU path lost %.0f%%, want > %.0f%% — the mis-sized wanbond0=%d must overflow the constrained path\n--- edge ---\n%s",
				wantTun, constrainedOuterMTU, loss, reproMinLossPct, wantTun, edge.log())
		}
		t.Logf("phase-1 (no mtu knob): wanbond0 = %d (InnerMTU(1500)); full-MTU DF flow over the %d-MTU constrained path lost %.0f%% — D85 reproduced", wantTun, constrainedOuterMTU, loss)
	})

	// Phase 2 — fix: mtu = 1400 declared on the constrained path.
	t.Run("knob_1400", func(t *testing.T) {
		top := SetupWithPaths(t, paths)
		installFragmentDrop(t, top)
		edge, conc := setupConstrainedTunnel(t, top, bin, paths, constrained.name, constrainedOuterMTU)
		if !top.pingUntil(concInner, 15*time.Second) {
			t.Fatalf("tunnel never came up\n--- edge ---\n%s\n--- conc ---\n%s", edge.log(), conc.log())
		}

		// The declared mtu=1400 pulls the min-across-paths TUN size down to
		// InnerMTU(1400) (T205), so a full-wanbond0-MTU inner packet fits the path.
		wantTun := bind.InnerMTU(constrainedOuterMTU, false)
		if got := top.linkMTU(t, tunDev, false); got != wantTun {
			t.Fatalf("phase-2 %s MTU = %d, want InnerMTU(1400) = %d (min across paths, T205)", tunDev, got, wantTun)
		}

		top.Blackhole(healthy.name) // same forcing as phase 1: the flow rides the constrained path
		payload := wantTun - icmpIPv4Overhead
		// The SAME constrained path under the SAME fragment-drop rule: capture the
		// constrained path's egress while the correctly-sized flow runs and assert it
		// produces ZERO fragmented outer datagrams.
		var loss float64
		frags := top.captureFragments(t, constrained.edgeVeth, mtuFlowCaptureWindow, func() {
			loss = top.tunnelDFPingLossPct(t, concInner, payload)
		})
		if frags != 0 {
			t.Fatalf("phase-2 observed %d fragmented outer datagrams on %s during a full-MTU flow; wanbond0=%d must keep the outer datagram <= %d", frags, constrained.edgeVeth, wantTun, constrainedOuterMTU)
		}
		if loss > knobMaxLossPct {
			t.Fatalf("phase-2 full-MTU (%d-byte inner) DF flow over the %d-MTU path lost %.0f%%, want <= %.0f%% — wanbond0=%d must fit the constrained path\n--- edge ---\n%s",
				wantTun, constrainedOuterMTU, loss, knobMaxLossPct, wantTun, edge.log())
		}
		t.Logf("phase-2 (mtu=%d): wanbond0 = %d (InnerMTU(1400)); full-MTU DF flow over the %d-MTU constrained path lost %.0f%% with %d fragmented datagrams — the knob resolves D85", constrainedOuterMTU, wantTun, constrainedOuterMTU, loss, frags)
	})
}

// setupConstrainedTunnel brings the bonded tunnel up over paths, optionally emitting a
// per-path `mtu = declaredMTU` on the path named declareMTUOn in BOTH the edge and the
// concentrator config (declareMTUOn == "" / declaredMTU == 0 declares none). It mirrors
// setupMultipathTunnelLevel but adds the T200 `mtu` knob; it lives in this file (not the
// shared helper) so the T213 ride-through e2e merges cleanly.
func setupConstrainedTunnel(t *testing.T, top *Topology, bin string, paths []pathSpec, declareMTUOn string, declaredMTU int) (edge, conc *proc) {
	t.Helper()

	edgePriv, edgePub := genKey(t)
	concPriv, concPub := genKey(t)
	psk := randKey(t)

	var edgePaths, concPaths strings.Builder
	for _, p := range paths {
		fmt.Fprintf(&edgePaths, "[[paths]]\nname = %q\nsource_addr = %q\ndest_addr = \"%s:%d\"\n", p.name, p.edgeIP, p.concIP, listenPort)
		if p.name == declareMTUOn && declaredMTU > 0 {
			fmt.Fprintf(&edgePaths, "mtu = %d\n", declaredMTU)
		}
		edgePaths.WriteString("\n")

		fmt.Fprintf(&concPaths, "[[paths]]\nname = %q\nsource_addr = %q\n", p.name, p.concIP)
		if p.name == declareMTUOn && declaredMTU > 0 {
			fmt.Fprintf(&concPaths, "mtu = %d\n", declaredMTU)
		}
		concPaths.WriteString("\n")
	}
	primary := paths[0]

	dir := t.TempDir()
	edgeCfg := writeConfig(t, filepath.Join(dir, "edge.toml"), fmt.Sprintf(`role = "edge"
psk = "%s"

%s[wireguard]
private_key = "%s"

[[wireguard.peers]]
public_key = "%s"
endpoint = "%s:%d"
allowed_ips = ["%s/32"]

[log]
level = "error"
`, psk, edgePaths.String(), edgePriv, concPub, primary.concIP, listenPort, concInner))

	concCfg := writeConfig(t, filepath.Join(dir, "conc.toml"), fmt.Sprintf(`role = "concentrator"
psk = "%s"

%s[wireguard]
private_key = "%s"
listen_port = %d

[[wireguard.peers]]
public_key = "%s"
allowed_ips = ["%s/32"]

[log]
level = "error"
`, psk, concPaths.String(), concPriv, listenPort, edgePub, edgeInner))

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

// installFragmentDrop mirrors the field 5G middlebox that DROPPED IP fragments (R241):
// it installs an nftables input-hook rule in the concentrator (far-side/transit) netns
// dropping fragmented outer datagrams as they arrive. Under the T201 Don't-Fragment
// policy the wanbond outer sockets never PRODUCE fragments on Linux — an over-MTU send
// is EMSGSIZE-dropped at the edge — so on this Linux netns tier the rule is a faithful
// middlebox backstop, not the operative loss mechanism (see the file header). It becomes
// the operative mechanism wherever the DF sockopt is a no-op (pathsock_other.go), keeping
// the negative control valid across DF policies. `ip frag-off & 0x1fff != 0` matches any
// non-first fragment, which is enough to break reassembly (the field behaviour).
func installFragmentDrop(t *testing.T, top *Topology) {
	t.Helper()
	top.nsenter("nft", "add", "table", "ip", fragDropTable)
	top.nsenter("nft", "add", "chain", "ip", fragDropTable, fragDropChain,
		"{", "type", "filter", "hook", "input", "priority", "0", ";", "}")
	top.nsenter("nft", "add", "rule", "ip", fragDropTable, fragDropChain,
		"ip", "frag-off", "&", "0x1fff", "!=", "0", "drop")
}

// tunnelDFPingLossPct sends mtuProbePings DF-set pings of the given -s payload from the
// edge to ip THROUGH THE TUNNEL and returns the packet-loss percentage parsed from ping's
// summary line (distinct from the path-direct pingLossPct in fixture_impairment_test.go).
// A non-zero ping exit (expected at 100% loss) is ignored — the summary is emitted
// regardless — so the caller reads the loss fraction, never an exec error.
func (top *Topology) tunnelDFPingLossPct(t *testing.T, ip string, payload int) float64 {
	t.Helper()
	out, _ := exec.Command("ping", "-c", strconv.Itoa(mtuProbePings), "-s", strconv.Itoa(payload),
		"-M", "do", "-i", "0.3", "-W", "2", ip).CombinedOutput()
	s := string(out)
	// summary: "10 packets transmitted, 0 received, 100% packet loss, time 2050ms"
	idx := strings.Index(s, "% packet loss")
	if idx < 0 {
		t.Fatalf("no packet-loss summary in ping output (payload %d):\n%s", payload, s)
	}
	start := idx
	for start > 0 && ((s[start-1] >= '0' && s[start-1] <= '9') || s[start-1] == '.') {
		start--
	}
	pct, err := strconv.ParseFloat(s[start:idx], 64)
	if err != nil {
		t.Fatalf("parse packet loss %q: %v\n%s", s[start:idx], err, s)
	}
	return pct
}
