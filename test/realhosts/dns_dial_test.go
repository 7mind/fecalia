//go:build realhosts

package realhosts

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// DNS dial-by-name overlay addressing/wire parameters (Q36, stretch). Distinct inner
// addresses, port, and systemd units from the smoke/soak/failover tiers so a run of
// several tiers back to back never collides on a leftover interface or unit.
const (
	dnsEdgeInner  = "10.10.1.1"
	dnsConcInner  = "10.10.1.2"
	dnsListenPort = 51840

	dnsConcUnit = "wanbond-dns-conc"
	dnsEdgeUnit = "wanbond-dns-edge"

	// dnsBogusHost is a syntactically valid (passes config.validateHostname) but
	// deliberately unresolvable hostname under the IANA-reserved "example" TLD
	// (RFC 2606 guarantees it is never delegated), so the induced failure this
	// subtest demonstrates is deterministic and touches no real DNS
	// infrastructure.
	dnsBogusHost = "this-host-does-not-exist.invalid.wanbond-dns-test.example"
)

// Timeouts specific to this tier. dnsHandshakeTimeout is generous (a real resolver
// round trip plus the real NAT path) but the tier waits at most this long before
// REPORTING — never failing — a stalled resolution/handshake.
const (
	dnsHandshakeTimeout = 45 * time.Second
	dnsTrafficTimeout   = 20 * time.Second
)

// TestRealDNSDialByName is the report-only dial-by-name stretch tier (Q36, M10/Q12
// discipline): it brings up the standing two-host testbed with the EDGE dialing the
// concentrator by its real DNS name (cfg.Conc.Addr — defaultConcHost, o3.7mind.io,
// which resolves to the standing ConcPubIP) with the peer's `dns = true` opt-in
// enabled, then repeats the same bring-up against a deliberately bogus hostname.
// Both scenarios REPORT their outcome (resolved address, time-to-first-handshake,
// and a steady-traffic sample) and NEVER fail the suite on that outcome — only the
// SSH/provisioning/build plumbing (a precondition, not the thing under test) can
// fail the test, mirroring every other tier in this package. There is no mid-session
// IP change: the concentrator's public IP is the fixed, standing ConcPubIP; this
// tier only proves the resolve-then-dial path against a real resolver and real NAT.
func TestRealDNSDialByName(t *testing.T) {
	cfg := LoadConfig()
	r := NewRunner(cfg)

	t.Logf("realhosts config: edge=%s concentrator=%s conc-public-ip=%s ssh-key=%s",
		cfg.Edge.target(), cfg.Conc.target(), cfg.ConcPubIP, cfg.SSHKey)

	provision(t, r, cfg.Edge, ProvisionOpts{})
	provision(t, r, cfg.Conc, ProvisionOpts{TunnelIface: tunnelIface})

	root := repoRoot(t)
	syncAndBuild(t, r, cfg.Edge, root)
	t.Cleanup(func() { removeRemoteDir(t, r, cfg.Edge) })
	syncAndBuild(t, r, cfg.Conc, root)
	t.Cleanup(func() { removeRemoteDir(t, r, cfg.Conc) })

	edgePriv, edgePub := genSmokeKey(t)
	concPriv, concPub := genSmokeKey(t)
	psk := randSmokeKey(t)

	edgeSrc := primaryIP(t, r, cfg.Edge)
	concSrc := primaryIP(t, r, cfg.Conc)
	t.Logf("path source addrs: edge=%s concentrator=%s", edgeSrc, concSrc)

	// The concentrator listens on the real public endpoint (ConcPubIP:dnsListenPort)
	// throughout BOTH subtests; only the edge's configured peer HOSTNAME changes
	// between them, so one concentrator bring-up and one iperf3 server suffice.
	startDNSConcentrator(t, r, cfg.Conc, concSrc, concPriv, edgePub, psk)
	startIperfServer(t, r, cfg.Conc, dnsConcInner)

	t.Run("valid_name", func(t *testing.T) {
		runDNSDialScenario(t, r, cfg.Edge, cfg.Conc.Addr, edgeSrc, edgePriv, concPub, psk, cfg.ConcPubIP)
	})
	t.Run("bogus_name", func(t *testing.T) {
		runDNSDialScenario(t, r, cfg.Edge, dnsBogusHost, edgeSrc, edgePriv, concPub, psk, cfg.ConcPubIP)
	})
}

// startDNSConcentrator writes and starts the concentrator's dial-by-name-tier
// daemon (own config/unit/port, distinct from the smoke tier's), then waits for its
// TUN and addresses it. This is INFRASTRUCTURE the scenario is built on, not the
// dial-by-name outcome under test, so — matching the rest of this package — it
// Fatals on failure.
func startDNSConcentrator(t *testing.T, r *Runner, conc Host, concSrc, concPriv, edgePub, psk string) {
	t.Helper()

	concCfg := fmt.Sprintf(`role = "concentrator"
psk = "%s"

[[paths]]
name = "wan0"
source_addr = "%s"

[wireguard]
private_key = "%s"
listen_port = %d

[[wireguard.peers]]
public_key = "%s"
allowed_ips = ["%s/32"]

[log]
level = "info"
`, psk, concSrc, concPriv, dnsListenPort, edgePub, dnsEdgeInner)

	concCfgPath := smokeRemoteDir + "/dns-conc.toml"
	writeRemoteFile(t, r, conc, concCfgPath, concCfg)

	stopUnit(t, r, conc, dnsConcUnit)
	delLink(t, r, conc, tunnelIface)
	t.Cleanup(func() {
		stopUnit(t, r, conc, dnsConcUnit)
		delLink(t, r, conc, tunnelIface)
	})

	ctx, cancel := context.WithTimeout(context.Background(), smokeSSHTimeout)
	defer cancel()
	start := fmt.Sprintf("sudo systemd-run --unit=%s --service-type=simple %s --config %s",
		dnsConcUnit, smokeBin, concCfgPath)
	if _, err := r.Run(ctx, conc, start); err != nil {
		t.Fatalf("concentrator: start wanbond daemon failed: %v", err)
	}
	t.Logf("concentrator: wanbond daemon started (unit=%s)", dnsConcUnit)

	if !waitRemoteLink(t, r, conc, tunnelIface, linkAppearTimeout) {
		dumpUnitLog(t, r, conc, dnsConcUnit)
		t.Fatalf("concentrator: %s never appeared", tunnelIface)
	}
	addressLink(t, r, conc, dnsConcInner)
}

// runDNSDialScenario writes and starts the EDGE daemon dialing dialHost:dnsListenPort
// (the peer's `dns = true` opt-in enabled) against the already-running concentrator,
// then observes — but does NOT gate on — the resolve-then-dial outcome: the resolved
// address (sourced from daemon-observable evidence, not a client-side lookup, so this
// proves wanbond's own resolver path rather than the test host's — see resolveOutcome),
// the time-to-first-handshake, and a short best-effort traffic sample taken only once a
// handshake completed. concPubIP is the standing testbed's known concentrator public IP
// (cfg.ConcPubIP), used only as the reported address on the silent boot-resolve path
// (see resolveOutcome); it is never itself gated on.
//
// Standing up the daemon and its TUN is a PRECONDITION (Fatal on failure, matching the
// rest of this package); the RESOLUTION / HANDSHAKE / TRAFFIC outcome itself never
// calls t.Fatalf/t.Errorf — only t.Logf. This is the acceptance property the
// "bogus_name" subtest exists to demonstrate: an unresolvable hostname reports a
// failed resolution/handshake/traffic outcome and the subtest still PASSES.
func runDNSDialScenario(t *testing.T, r *Runner, edge Host, dialHost, edgeSrc, edgePriv, concPub, psk, concPubIP string) {
	t.Helper()

	edgeCfg := fmt.Sprintf(`role = "edge"
psk = "%s"

[[paths]]
name = "wan0"
source_addr = "%s"

[wireguard]
private_key = "%s"

[[wireguard.peers]]
public_key = "%s"
endpoint = "%s:%d"
dns = true
allowed_ips = ["%s/32"]

[log]
level = "info"
`, psk, edgeSrc, edgePriv, concPub, dialHost, dnsListenPort, dnsConcInner)

	edgeCfgPath := smokeRemoteDir + "/dns-edge.toml"
	writeRemoteFile(t, r, edge, edgeCfgPath, edgeCfg)

	stopUnit(t, r, edge, dnsEdgeUnit)
	delLink(t, r, edge, tunnelIface)
	t.Cleanup(func() {
		stopUnit(t, r, edge, dnsEdgeUnit)
		delLink(t, r, edge, tunnelIface)
	})

	startAt := edgeClockNow(t, r, edge)

	ctx, cancel := context.WithTimeout(context.Background(), smokeSSHTimeout)
	start := fmt.Sprintf("sudo systemd-run --unit=%s --service-type=simple %s --config %s",
		dnsEdgeUnit, smokeBin, edgeCfgPath)
	_, err := r.Run(ctx, edge, start)
	cancel()
	if err != nil {
		t.Fatalf("edge: start wanbond daemon (dial %q) failed: %v", dialHost, err)
	}
	t.Logf("edge: wanbond daemon started dialing %q (unit=%s)", dialHost, dnsEdgeUnit)

	if !waitRemoteLink(t, r, edge, tunnelIface, linkAppearTimeout) {
		dumpUnitLog(t, r, edge, dnsEdgeUnit)
		t.Fatalf("edge: %s never appeared", tunnelIface)
	}
	addressLink(t, r, edge, dnsEdgeInner)

	// --- Resolution + handshake (REPORT-ONLY: never fails the suite) -------------------
	handshakeOK := pingUntil(t, r, edge, dnsConcInner, dnsHandshakeTimeout)
	var handshakeElapsed time.Duration
	if handshakeOK {
		handshakeElapsed = edgeClockNow(t, r, edge).Sub(startAt)
	}

	journal := readDaemonJournal(t, r, edge, dnsEdgeUnit)
	resolvedAddr, resolveElapsed, resolveOK, viaBootResolve := resolveOutcome(journal, startAt, handshakeOK, concPubIP)

	switch {
	case resolveOK && viaBootResolve:
		t.Logf("RESOLUTION: %q -> %s (inferred: silent Q30 boot-resolve succeeded, evidenced by a completed handshake; no journal marker on this path, see resolveOutcome)",
			dialHost, resolvedAddr)
	case resolveOK:
		t.Logf("RESOLUTION: %q -> %s (%s after daemon start)", dialHost, resolvedAddr, resolveElapsed)
	default:
		t.Logf("RESOLUTION: %q did not resolve to a usable endpoint within the window", dialHost)
	}
	if handshakeOK {
		t.Logf("HANDSHAKE: OK (time-to-first-handshake ~ %s)", handshakeElapsed.Round(time.Millisecond))
	} else {
		t.Logf("HANDSHAKE: did not complete within %s", dnsHandshakeTimeout)
	}

	// --- Steady traffic (REPORT-ONLY; skipped entirely when there was no handshake) ----
	var mbps float64
	trafficOK := false
	if handshakeOK {
		mbps, trafficOK = dnsTrafficSample(t, r, edge, dnsConcInner)
	}
	if trafficOK {
		t.Logf("TRAFFIC: %.2f Mbit/s over the dial-by-name tunnel", mbps)
	} else {
		t.Logf("TRAFFIC: no sample (handshake=%t)", handshakeOK)
	}

	// Final report block (report-only; nothing above or below gates the subtest).
	t.Logf("=== DNS DIAL-BY-NAME RESULT ===\n"+
		"  dial host:       %s\n"+
		"  resolved:        %t (%s)\n"+
		"  handshake:       %t (%s)\n"+
		"  traffic sample:  %t (%.2f Mbit/s)",
		dialHost, resolveOK, resolvedAddr, handshakeOK, handshakeElapsed.Round(time.Millisecond), trafficOK, mbps)
}

// dumpUnitLog logs the tail of unit's journal on host for diagnosing a failed
// bring-up. A parameterized sibling of dumpDaemonLog (which hardcodes smokeUnit),
// needed here because this tier's daemons run under their own unit names.
func dumpUnitLog(t *testing.T, r *Runner, host Host, unit string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), smokeSSHTimeout)
	defer cancel()
	res, err := r.Run(ctx, host, "sudo journalctl -u "+unit+" --no-pager -n 50 2>&1; true")
	if err != nil {
		t.Logf("%s: could not read %s journal: %v", host.Role, unit, err)
		return
	}
	t.Logf("%s: %s journal (last 50 lines):\n%s", host.Role, unit, res.Stdout)
}

// dnsResolutionRecord is the subset of the "hub failover: first endpoint resolution"
// slog line (internal/device/failover.go) this tier reads: it is logged exactly once,
// the moment the re-resolution controller installs the FIRST successfully resolved
// address on the engine peer. This fires ONLY on the R70 DEFERRED-install path — see
// resolveOutcome for why a hostname can also resolve WITHOUT ever emitting this marker.
type dnsResolutionRecord struct {
	Time       time.Time `json:"time"`
	Msg        string    `json:"msg"`
	ToEndpoint string    `json:"to_endpoint"`
}

// resolveOutcome derives the resolution verdict from the TWO disjoint paths a hostname
// endpoint can resolve through (docs/design.md "Device lifecycle"):
//
//   - DEFERRED install (R70): the Q30 bounded boot resolve failed for every spec, so the
//     peer boots endpoint-less; the re-resolution controller's first successful lookup
//     then INSTALLS the endpoint via the engine's UAPI path and logs the "first endpoint
//     resolution" journal marker firstResolutionAfter reads. This is the only path that
//     is DIRECTLY journal-observable, so it is checked first and, when present, is
//     authoritative for both the resolved address and the elapsed time.
//   - SILENT boot resolve (Q30 happy path): the hostname resolves within the boot
//     window, so resolveBootEndpoints/bootResolveHostname (internal/device/device.go)
//     seed the endpoint directly into the UAPI render at Up — that path logs NOTHING on
//     success (only on failure/empty-result), and newHubFailoverFromSpecs seeds
//     activeSpec from the already-resolved specs, so updateResolution's R70 branch
//     (activeSpec == -1) is never reached either. There is therefore no daemon-observable
//     resolved address on this path; a completed handshake against a name-dialed peer is
//     itself the proof the boot resolve succeeded (the engine can only handshake against
//     an addressable endpoint), so a handshake with no R70 marker is reported as resolved
//     using the testbed's known standing concentrator public IP (concPubIP) as the
//     address — the same static fact the "valid_name" scenario's own dialHost resolves
//     to (see TestRealDNSDialByName's doc comment), not a fresh client-side lookup.
//
// Neither path succeeding (bogus_name: no marker, no handshake) reports ok=false, exactly
// what that subtest expects to observe.
func resolveOutcome(journal string, after time.Time, handshakeOK bool, concPubIP string) (endpoint string, elapsed time.Duration, ok bool, viaBootResolve bool) {
	if endpoint, elapsed, ok := firstResolutionAfter(journal, after); ok {
		return endpoint, elapsed, true, false
	}
	if handshakeOK {
		return concPubIP, 0, true, true
	}
	return "", 0, false, false
}

// firstResolutionAfter scans journal for the "first endpoint resolution" transition
// logged strictly after `after` and returns the address it installed and the elapsed
// time from `after` to that transition. ok is false when no such transition appears
// — i.e. either the hostname never resolved to a usable address in the window (the
// bogus_name case), or it resolved via the silent boot-resolve path (see
// resolveOutcome, which is what callers should use instead of this function directly).
func firstResolutionAfter(journal string, after time.Time) (endpoint string, elapsed time.Duration, ok bool) {
	for _, line := range strings.Split(journal, "\n") {
		if !strings.Contains(line, "first endpoint resolution") {
			continue
		}
		var rec dnsResolutionRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		if rec.ToEndpoint == "" || !rec.Time.After(after) {
			continue
		}
		return rec.ToEndpoint, rec.Time.Sub(after), true
	}
	return "", 0, false
}

// dnsTrafficSample runs a short best-effort iperf3 TCP sample from edge to the
// concentrator's inner IP and reports the achieved throughput. Unlike iperfTCP
// (smoke tier), every failure is LOGGED and returns ok=false rather than
// t.Fatalf — the whole point of this tier is that a data-plane sample taken after a
// bogus-name dial REPORTS, it never fails the suite.
func dnsTrafficSample(t *testing.T, r *Runner, edge Host, serverIP string) (mbps float64, ok bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), dnsTrafficTimeout)
	defer cancel()
	res, err := r.Run(ctx, edge, fmt.Sprintf("iperf3 -c %s -t 5 -J", serverIP))
	if err != nil {
		t.Logf("edge: dial-by-name traffic sample failed: %v", err)
		return 0, false
	}
	var rep iperfReport
	if err := json.Unmarshal([]byte(res.Stdout), &rep); err != nil {
		t.Logf("edge: parse dial-by-name iperf3 JSON failed: %v\n%s", err, res.Stdout)
		return 0, false
	}
	return rep.End.SumSent.BitsPerSecond / 1e6, true
}
