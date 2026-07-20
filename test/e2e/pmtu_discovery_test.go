//go:build e2e

package e2e

// TestE2EPMTUDiscovery (T212, defect D85) is the netns resolution gate for the
// AUTO-DISCOVERY half of D85 (decision 5: netns e2e is sufficient; a real-hardware run
// is the follow-up). Where TestE2EConstrainedPathMTUKnob (T210) proves the STATIC operator
// `mtu` knob resolves a constrained underlay, this test asserts the daemon resolves the
// SAME constrained path with NO knob declared, by DISCOVERING the path MTU and resizing
// wanbond0 to fit — then holds a full-MTU flow losslessly, regrows on a roam, and clamps
// edge-originated TCP.
//
// Topology (mirrors T210): two bonded veth paths — a constrained PRIMARY (outer link MTU
// 1400, the field's ~5G underlay) and a healthy 1500 secondary — with the SAME transit-netns
// IP-fragment-DROP rule (installFragmentDrop, R241): a lossless netns cannot reproduce the
// field loss, so the drop rule makes an oversize-then-fragmented outer datagram actually
// lossy, and under the T201 Don't-Fragment policy an over-MTU outer send instead surfaces
// EMSGSIZE at the edge. The ONLY difference from T210 is that NO `mtu` knob is declared, so
// the correct sizing must come from auto-discovery, not the operator.
//
// HARDWARE TIER — DO NOT RUN IN THE DEFAULT GATE. Like every `//go:build e2e` test here it
// needs root, /dev/net/tun, tc, and nftables inside a network namespace; it is compiled and
// `go vet -tags e2e` / `just lint` GREEN locally and executed ONLY on the privileged
// netns/real-host tier. Do NOT run it locally.
//
// The four asserts, per the D85 decision:
//  1. df-set-discovery-converges-and-shrinks: DF is set (T201) so oversize probes/data are
//     not locally fragmented; discovery converges and wanbond0 shrinks to InnerMTU(1400)
//     within a bounded time (the T209 resize driven by the T206 discovery).
//  2. full-mtu-flow-zero-fragmentation: a full-inner-MTU DF flow then flows with ZERO outer
//     fragmentation and ~0 loss under the drop rule (capture assert).
//  3. roam-regrows-wanbond0: raising the constrained link to 1500 and bouncing the path
//     (DOWN->UP re-probe) grows wanbond0 back to InnerMTU(1500) after the loosening dwell.
//  4. edge-syn-mss-clamped: an edge-originated TCP SYN carries MSS <= innerMTU-40 (T208).
//
// WIRING STATUS (as of T212). Asserts (1)/(2)/(3) depend on PMTU auto-discovery being
// INSTANTIATED in the live datapath — telemetry.PMTUDiscovery (T206) driving the metrics
// Source's PathSnapshot.PMTU, which the T209 resizer folds. That instantiation is NOT yet
// present: NewPMTUDiscovery is never constructed by the tunnel/Source, and
// internal/device.metricsSource.Paths() never populates PathSnapshot.PMTU (it is always 0),
// so the resizer sees only the configured-or-default path MTU. With NO `mtu` knob, wanbond0
// therefore stays at InnerMTU(1500) and never auto-shrinks/regrows. Those three subtests are
// written in full and gated on skipUntilPMTUDiscoveryWired so the hardware tier does NOT
// fake a pass; flip the skip once the datapath discovery-instantiation follow-up lands.
// Assert (4) — the T208 edge-originated MSS clamp — IS wired at device.Up (mangle/OUTPUT
// TCPMSS --clamp-mss-to-pmtu, derived from the LIVE wanbond0 MTU), independent of discovery,
// so it runs for real against whatever MTU wanbond0 currently carries.
//
// This file binds no /metrics listener (the asserts read the link MTU and the on-wire SYN
// directly), so it claims no port in netns.go's metricsPortRegistry. Per AGENTS.md the netns
// fixture validates FUNCTIONAL/counter-ratio outcomes, not throughput.

import (
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/bind"
)

const (
	// tcpIPv4MSSOverhead is the inner IPv4 (20) + TCP (20) header cost subtracted from
	// wanbond0's inner MTU to yield the largest MSS an edge-originated TCP SYN may carry
	// under the T208 clamp (mirrors internal/device.tcpV4MSSOverhead).
	tcpIPv4MSSOverhead = 40

	// pmtuConvergeTimeout bounds how long the auto-discovery convergence (assert 1) may take
	// to shrink wanbond0 from its assumed InnerMTU(1500) to the discovered InnerMTU(1400):
	// a few probe/liveness ticks plus the immediate (tightening) resize apply.
	pmtuConvergeTimeout = 20 * time.Second

	// pmtuRegrowTimeout bounds the post-roam regrow (assert 3): a re-probe plus the
	// LOOSENING resize dwell (mtuResizeDwell == the scheduler failback dwell, ~5s), so it is
	// deliberately larger than pmtuConvergeTimeout's tighten-now path.
	pmtuRegrowTimeout = 25 * time.Second
)

// TestE2EPMTUDiscovery runs the four D85 auto-discovery asserts as sequential subtests. They
// run sequentially (no t.Parallel) because the fixture's FIXED veth names forbid two live
// topologies — each subtest builds and tears down its own Setup, mirroring T210/T213.
func TestE2EPMTUDiscovery(t *testing.T) {
	bin := buildWanbond(t)

	// The constrained path is the PRIMARY (paths[0]) so the "first healthy path" scheduler
	// rides it; the healthy 1500 path is the real second path so the daemon's assumed-1500
	// ceiling is exercised. NO `mtu` knob is declared on either — auto-discovery, not the
	// operator, must resolve the constrained sizing. The veth names match T210's; the suite
	// is sequential (fixed names forbid parallel), and Setup idempotently pre-deletes them.
	constrained := pathSpec{name: "cellular", edgeIP: "10.100.2.1", concIP: "10.100.2.2", edgeVeth: "wbBe", concVeth: "wbBc", delayMs: 20, outerMTU: constrainedOuterMTU}
	healthy := pathSpec{name: "starlink", edgeIP: "10.100.1.1", concIP: "10.100.1.2", edgeVeth: "wbAe", concVeth: "wbAc", delayMs: 20}
	paths := []pathSpec{constrained, healthy}

	// Assert 1 — DF holds, discovery converges, wanbond0 shrinks to InnerMTU(1400).
	t.Run("df-set-discovery-converges-and-shrinks", func(t *testing.T) {
		skipUntilPMTUDiscoveryWired(t)
		top := SetupWithPaths(t, paths)
		installFragmentDrop(t, top)
		edge, conc := setupConstrainedTunnel(t, top, bin, paths, "", 0)
		if !top.pingUntil(concInner, 15*time.Second) {
			t.Fatalf("tunnel never came up\n--- edge ---\n%s\n--- conc ---\n%s", edge.log(), conc.log())
		}

		// Under the T201 Don't-Fragment policy an oversize outer probe/datagram surfaces
		// EMSGSIZE at the edge rather than IP-fragmenting; discovery must then converge on the
		// constrained path and the T209 resizer must shrink wanbond0 to InnerMTU(1400).
		want := bind.InnerMTU(constrainedOuterMTU, false)
		if !top.waitLinkMTU(t, tunDev, false, want, pmtuConvergeTimeout) {
			t.Fatalf("wanbond0 MTU = %d after %s, want auto-discovered InnerMTU(1400) = %d — PMTU discovery did not converge and shrink the TUN (no mtu knob declared)\n--- edge ---\n%s",
				top.linkMTU(t, tunDev, false), pmtuConvergeTimeout, want, edge.log())
		}
		t.Logf("assert-1: wanbond0 auto-shrank to InnerMTU(1400) = %d over the %d-MTU constrained path with NO mtu knob — discovery converged", want, constrainedOuterMTU)
	})

	// Assert 2 — a full-inner-MTU DF flow then flows with ZERO outer fragmentation and ~0 loss.
	t.Run("full-mtu-flow-zero-fragmentation", func(t *testing.T) {
		skipUntilPMTUDiscoveryWired(t)
		top := SetupWithPaths(t, paths)
		installFragmentDrop(t, top)
		edge, conc := setupConstrainedTunnel(t, top, bin, paths, "", 0)
		if !top.pingUntil(concInner, 15*time.Second) {
			t.Fatalf("tunnel never came up\n--- edge ---\n%s\n--- conc ---\n%s", edge.log(), conc.log())
		}

		want := bind.InnerMTU(constrainedOuterMTU, false)
		if !top.waitLinkMTU(t, tunDev, false, want, pmtuConvergeTimeout) {
			t.Fatalf("wanbond0 did not auto-shrink to InnerMTU(1400) = %d before the flow (got %d)\n--- edge ---\n%s",
				want, top.linkMTU(t, tunDev, false), edge.log())
		}

		top.Blackhole(healthy.name) // force the flow onto the constrained path
		payload := want - icmpIPv4Overhead
		// Capture the constrained path's egress while a correctly-sized full-MTU DF flow runs
		// and assert it produces ZERO fragmented outer datagrams under the drop rule.
		var loss float64
		frags := top.captureFragments(t, constrained.edgeVeth, mtuFlowCaptureWindow, func() {
			loss = top.tunnelDFPingLossPct(t, concInner, payload)
		})
		if frags != 0 {
			t.Fatalf("observed %d fragmented outer datagrams on %s during a full-MTU flow; the discovered wanbond0=%d must keep the outer datagram <= %d",
				frags, constrained.edgeVeth, want, constrainedOuterMTU)
		}
		if loss > knobMaxLossPct {
			t.Fatalf("full-MTU (%d-byte inner) DF flow over the %d-MTU path lost %.0f%%, want <= %.0f%% — the discovered wanbond0=%d must fit the constrained path\n--- edge ---\n%s",
				want, constrainedOuterMTU, loss, knobMaxLossPct, want, edge.log())
		}
		t.Logf("assert-2: full-MTU DF flow over the %d-MTU constrained path lost %.0f%% with %d fragmented datagrams — auto-discovered sizing resolves D85", constrainedOuterMTU, loss, frags)
	})

	// Assert 3 — a roam (constrained link rises to 1500) regrows wanbond0 after the dwell.
	t.Run("roam-regrows-wanbond0", func(t *testing.T) {
		skipUntilPMTUDiscoveryWired(t)
		top := SetupWithPaths(t, paths)
		installFragmentDrop(t, top)
		edge, conc := setupConstrainedTunnel(t, top, bin, paths, "", 0)
		if !top.pingUntil(concInner, 15*time.Second) {
			t.Fatalf("tunnel never came up\n--- edge ---\n%s\n--- conc ---\n%s", edge.log(), conc.log())
		}

		shrunk := bind.InnerMTU(constrainedOuterMTU, false)
		if !top.waitLinkMTU(t, tunDev, false, shrunk, pmtuConvergeTimeout) {
			t.Fatalf("wanbond0 did not auto-shrink to InnerMTU(1400) = %d before the roam (got %d)\n--- edge ---\n%s",
				shrunk, top.linkMTU(t, tunDev, false), edge.log())
		}

		// Simulate a roam: the constrained underlay's real MTU rises to 1500. Bounce the path
		// (DOWN->UP) so discovery re-probes (decideLocked's DOWN->UP re-trigger); the resizer
		// then GROWS wanbond0 back to InnerMTU(1500) after the loosening dwell (mtuResizeDwell).
		top.setPathMTU(constrained.name, bind.DefaultPathMTU)
		top.Blackhole(constrained.name)
		time.Sleep(500 * time.Millisecond)
		top.Restore(constrained.name)

		grown := bind.InnerMTU(bind.DefaultPathMTU, false)
		if !top.waitLinkMTU(t, tunDev, false, grown, pmtuRegrowTimeout) {
			t.Fatalf("wanbond0 MTU = %d after the roam+dwell, want regrown InnerMTU(1500) = %d — the re-probe/loosening resize did not grow the TUN back\n--- edge ---\n%s",
				top.linkMTU(t, tunDev, false), grown, edge.log())
		}
		t.Logf("assert-3: wanbond0 regrew to InnerMTU(1500) = %d after the constrained link rose to 1500 and the path bounced — discovery re-probe + loosening resize", grown)
	})

	// Assert 4 — an edge-originated TCP SYN carries MSS <= innerMTU-40 (T208 clamp). WIRED.
	t.Run("edge-syn-mss-clamped", func(t *testing.T) {
		top := SetupWithPaths(t, paths)
		installFragmentDrop(t, top)
		edge, conc := setupConstrainedTunnel(t, top, bin, paths, "", 0)
		if !top.pingUntil(concInner, 15*time.Second) {
			t.Fatalf("tunnel never came up\n--- edge ---\n%s\n--- conc ---\n%s", edge.log(), conc.log())
		}

		// The clamp derives MSS from the LIVE wanbond0 MTU (--clamp-mss-to-pmtu), so read the
		// interface MTU actually in force and assert against it — robust whether the
		// auto-discovery resize has run (InnerMTU(1400)) or not yet (InnerMTU(1500)).
		innerMTU := top.linkMTU(t, tunDev, false)

		// A one-shot iperf3 server on the concentrator gives the edge a real TCP peer; the
		// edge-originated control-connection SYN egresses wanbond0 and is captured there.
		top.startProc(t, "iperf3-server", "nsenter", "-t", strconv.Itoa(top.pid), "-n", "iperf3", "-s", "-1", "-B", concInner)
		top.waitIperfListen(t, iperfDefaultPort)

		mss := top.captureSYNMaxMSS(t, tunDev, mtuFlowCaptureWindow, func() {
			_ = top.tryRun("iperf3", "-c", concInner, "-t", "2")
		})
		if mss <= 0 {
			t.Fatalf("captured no edge-originated TCP SYN carrying an MSS option on %s during the flow — cannot verify the T208 clamp\n--- edge ---\n%s", tunDev, edge.log())
		}
		wantMax := innerMTU - tcpIPv4MSSOverhead
		if mss > wantMax {
			t.Fatalf("edge-originated SYN MSS = %d, want <= innerMTU-40 = %d (live wanbond0 MTU %d) — the T208 daemon MSS clamp did not hold\n--- edge ---\n%s", mss, wantMax, innerMTU, edge.log())
		}
		t.Logf("assert-4: edge-originated SYN MSS = %d <= innerMTU-40 = %d (live wanbond0 MTU %d) — T208 clamp holds", mss, wantMax, innerMTU)
	})
}

// skipUntilPMTUDiscoveryWired skips a subtest whose assertion depends on PMTU auto-discovery
// being instantiated in the live datapath — which it is NOT yet (see the file header). It
// skips (never fakes a pass) so the hardware tier records an honest SKIP; flip this to a
// no-op once the datapath PMTUDiscovery-instantiation follow-up lands.
func skipUntilPMTUDiscoveryWired(t *testing.T) {
	t.Helper()
	t.Skip("PMTU auto-discovery (telemetry.PMTUDiscovery, T206) is not yet instantiated in the live datapath: " +
		"NewPMTUDiscovery is never constructed by the tunnel/metrics Source, and internal/device.metricsSource.Paths() " +
		"never populates PathSnapshot.PMTU (always 0), so the T209 runtime resizer folds only the configured-or-default " +
		"path MTU — a bond with NO mtu knob keeps wanbond0 at InnerMTU(1500) and never auto-shrinks/regrows. " +
		"Un-skip once datapath PMTUDiscovery instantiation lands (D85 auto-discovery follow-up).")
}

// waitLinkMTU polls dev's link MTU (in the peer netns when ns is true) until it equals want
// or d elapses, returning whether it converged. It is the bounded-time observation the
// auto-discovery resize asserts (1)/(3) rest on.
func (top *Topology) waitLinkMTU(t *testing.T, dev string, ns bool, want int, d time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if top.linkMTU(t, dev, ns) == want {
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}

// setPathMTU raises (or lowers) the constrained link MTU on BOTH ends of the named path's
// veth pair at runtime, emulating an underlay path-MTU change (a roam onto a larger-MTU
// uplink) for the discovery re-probe assert (3).
func (top *Topology) setPathMTU(name string, mtu int) {
	p := top.path(name)
	top.run("ip", "link", "set", p.edgeVeth, "mtu", strconv.Itoa(mtu))
	top.nsenter("ip", "link", "set", p.concVeth, "mtu", strconv.Itoa(mtu))
}

// captureSYNMaxMSS runs tcpdump on dev for the duration of during(), capturing pure TCP SYNs
// (the only segment carrying the MSS option) and returning the largest MSS advertised, or 0
// when none was seen. It is the on-wire proof of the T208 edge-originated MSS clamp: the SYN
// of a flow leaving wanbond0 must carry MSS <= innerMTU-40.
func (top *Topology) captureSYNMaxMSS(t *testing.T, dev string, d time.Duration, during func()) int {
	t.Helper()
	secs := int(d.Seconds())
	if secs < 1 {
		secs = 1
	}
	// tcp[tcpflags] & (tcp-syn|tcp-ack) == tcp-syn matches a PURE SYN (SYN set, ACK clear),
	// skipping the SYN,ACK reply; -v renders the TCP options including "mss <N>".
	capture := exec.Command("timeout", strconv.Itoa(secs), "tcpdump", "-n", "-v", "-i", dev,
		"tcp[tcpflags] & (tcp-syn|tcp-ack) == tcp-syn")
	out := &lockedBuffer{}
	capture.Stdout, capture.Stderr = out, out
	if err := capture.Start(); err != nil {
		t.Fatalf("start tcpdump on %s: %v", dev, err)
	}
	time.Sleep(700 * time.Millisecond) // let tcpdump attach before generating traffic
	during()
	time.Sleep(300 * time.Millisecond) // let trailing packets flush to the capture
	_ = capture.Wait()                 // timeout terminates it
	return maxMSS(out.String())
}

// maxMSS parses the largest "mss <N>" value tcpdump -v printed for the captured SYNs, or 0
// when none is present. tcpdump renders the option as "options [mss 1460,sackOK,...]", so the
// brackets and commas are flattened to spaces before tokenising: "mss" then the number.
func maxMSS(dump string) int {
	repl := strings.NewReplacer(",", " ", "[", " ", "]", " ")
	fields := strings.Fields(repl.Replace(dump))
	max := 0
	for i, f := range fields {
		if f == "mss" && i+1 < len(fields) {
			if v, err := strconv.Atoi(fields[i+1]); err == nil && v > max {
				max = v
			}
		}
	}
	return max
}
