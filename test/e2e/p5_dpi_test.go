//go:build e2e

package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/wireaudit"
)

// Requirement-6 (DPI-resistance) EXTERNAL-TOOL non-classification check (M9).
//
// TestWireFormatAudit (T26) proves the obfuscation properties of the wanbond wire
// PROGRAMMATICALLY (entropy + no fixed-offset fingerprint). This test complements it
// by running the CAPTURED wire through the two production DPI engines an adversary on
// a hostile network would use — nDPI (ndpiReader) and Suricata — and asserting NEITHER
// classifies the obfuscated wanbond flow as WireGuard or any identified VPN by PAYLOAD.
//
// PAYLOAD vs PORT — the decisive distinction. Requirement 6 is about the WIRE FORMAT /
// PAYLOAD being indistinguishable from random, NOT about the UDP port (wanbond's listen
// port is operator-configurable). nDPI (and DPI engines generally) will PORT-GUESS
// "WireGuard"/VPN for ANY UDP flow on WireGuard's IANA-registered port 51820 regardless
// of payload — a benign [Confidence: Match by port] classification that says nothing
// about the wire. (Reproduced with nDPI 5.0: a random-payload UDP flow to :51820 is
// labelled WireGuard/VPN "Match by port"; the SAME payload to :40000 is "Unknown".) So
// this test isolates PAYLOAD DPI-resistance two ways, BOTH applied:
//
//	(a) it parses nDPI's PER-FLOW output and reads the [Confidence: …] field, failing
//	    ONLY on a WireGuard/VPN classification whose confidence is PAYLOAD/CONTENT-based
//	    (e.g. "DPI") — a "Match by port" guess is EXPECTED and never fails; and
//	(b) it captures the wanbond flow on a NON-registered UDP port (dpiListenPort), so
//	    nDPI's port guess does not fire at all and any WireGuard/VPN label on that
//	    capture is unambiguously a genuine payload-fingerprint leak.
//
// Symmetry: the positive control (plain WireGuard) is detected by exactly the
// payload/content confidence class ("DPI") that the negative leg must be FREE of, so the
// two legs are the same predicate — one asserted present, one asserted absent.
//
// NON-VACUITY / POSITIVE CONTROL. A bare "not detected" result is meaningless unless the
// detector demonstrably DETECTS WireGuard BY PAYLOAD when it is present. wanbond ALWAYS
// obfuscates via its outer frame codec, so a plain-WG reference cannot come from wanbond
// itself. Option (a) of the task — plain amneziawg-go in-process via tun/netstack — is
// NOT tractable: netstack pulls gVisor, whose vendored tcpip/stack fails to build under
// the go toolchain (bridge_test.go declares `package bridge_test`), and two kernel-TUN
// WireGuard endpoints in ONE netns short-circuit (a packet to the peer's on-link /32
// never traverses the WG socket). So this uses option (b): a COMMITTED genuine
// plain-WireGuard capture (testdata/plain-wireguard.pcap, real kernel-WG handshake +
// transport). nDPI classifies it WireGuard with [Confidence: DPI]; the positive control
// asserts that PAYLOAD-confidence classification FIRST — if it is absent, the tool or
// parser is broken and the negative assertion is vacuous.
//
// nDPI carries the WG-SPECIFIC positive control. Suricata 8.x's default configuration
// ships NO WireGuard app-layer parser or signature (it labels even a plain-WG flow
// app_proto="failed"), so it cannot serve as a WG positive control; its value here is
// the app-layer/anomaly NEGATIVE check (with a ">=1 flow decoded" sanity so a zero-decode
// run cannot pass silently). This is DOCUMENTED (docs/install.md Limitations and the log
// lines below); the suricata positive run is informational only.
//
// PRIVILEGE. The wanbond capture reuses T26's tcpdump plumbing (startPcap; note the
// deliberate absence of `-Z root`, which segfaults tcpdump 4.99.x) over the netns/netem
// fixture, so this test needs root/CAP_NET_ADMIN and runs only on the e2e host. It also
// needs ndpiReader and suricata ON PATH; if either is absent it FAILS LOUD (never
// t.Skip — a skipped DPI check is false assurance). The e2e host must have nDPI and
// Suricata provisioned (the orchestrator does this).
const (
	// ndpiReaderBin / suricataBin are the external DPI engines, resolved on PATH.
	ndpiReaderBin = "ndpiReader"
	suricataBin   = "suricata"

	// dpiToolTimeout bounds each external-tool invocation so a hung ndpiReader/suricata
	// fails the test rather than wedging the suite.
	dpiToolTimeout = 90 * time.Second

	// dpiLoadSecs is the bulk-transfer duration driving the wanbond capture: long enough
	// to put DATA/PARITY/PROBE frames + amnezia junk on the wire for the engines to
	// inspect, short enough to keep the test quick.
	dpiLoadSecs = 5

	// dpiListenPort is the wanbond WireGuard listen port used for the P5 capture. It is a
	// NON-registered high port ON PURPOSE: capturing on WireGuard's IANA-registered 51820
	// (the shared listenPort) would make nDPI PORT-GUESS "WireGuard"/VPN regardless of
	// payload, conflating a port guess with a genuine payload leak. On a non-registered
	// port nDPI's port guess never fires, so any WireGuard/VPN classification of this
	// capture is unambiguously a payload-fingerprint defect. wanbond's port is
	// operator-configurable, so this changes nothing about the acceptance semantics.
	dpiListenPort = 40000

	// plainWGPcap is the committed positive-control capture (real plain WireGuard).
	plainWGPcap = "testdata/plain-wireguard.pcap"

	// suricataConfigEnv optionally overrides the suricata.yaml path; defaultSuricataConfig
	// is the standard location a provisioned suricata install writes. When neither is
	// present, suricata falls back to its own built-in config search.
	suricataConfigEnv     = "WANBOND_SURICATA_CONFIG"
	defaultSuricataConfig = "/etc/suricata/suricata.yaml"

	// suricataEveFile is the JSON event log suricata writes into its -l directory.
	suricataEveFile = "eve.json"

	// dpiDNSGuardFilter is the tcpdump BPF filter for the zero-DNS-egress guard
	// (Q29/Q33): the well-known ports for cleartext system DNS (53), DNS-over-TLS (853),
	// and DNS-over-HTTPS (443 — shared with generic HTTPS, but this fixture's edge netns
	// makes no other outbound connection during the capture window, so any packet on
	// these ports here IS DNS-adjacent egress, in either direction). This capture runs on
	// a DNS-OFF config (no peer carries the `dns = true` opt-in; every endpoint is an
	// IP literal), so Q29 requires it to be PROVABLY inert: zero packets on any of these
	// ports for the whole session. Documents the exact artifact each mode would otherwise
	// leak to a passive observer, once DNS IS opted in: system mode leaks a CLEARTEXT DNS
	// query naming the concentrator's hostname; DoH/DoT mode leaks the TLS ClientHello
	// SNI plus connection timing to the resolver (the query payload itself is encrypted,
	// but the SNI and the timing correlate the edge to that resolver).
	dpiDNSGuardFilter = "port 53 or port 853 or port 443"
)

// vpnLabelPatterns are the lowercase substrings that mark a DPI label as a WireGuard or
// otherwise-identified VPN classification. A PAYLOAD-confidence hit on the OBFUSCATED
// wanbond flow is a requirement-6 defect; the same on the plain-WG positive control is
// the expected teeth. A classification of Unknown/QUIC/DNS/etc. matches none of these.
var vpnLabelPatterns = []string{"wireguard", "openvpn", "ipsec", "tunnel", "vpn"}

// matchVPNLabel reports the first vpnLabelPattern contained (case-insensitively) in s.
func matchVPNLabel(s string) (pattern string, ok bool) {
	l := strings.ToLower(s)
	for _, p := range vpnLabelPatterns {
		if strings.Contains(l, p) {
			return p, true
		}
	}
	return "", false
}

func TestP5DPI(t *testing.T) {
	requireDPITool(t, ndpiReaderBin)
	requireDPITool(t, suricataBin)

	bin := buildWanbond(t)
	top := SetupWithPaths(t, []pathSpec{auditPath})

	// (1) POSITIVE CONTROL FIRST — prove the detector has PAYLOAD teeth before trusting a
	// "not classified" result. nDPI must classify the known plain-WireGuard capture as
	// WireGuard/VPN with a PAYLOAD/CONTENT confidence (not a mere port guess); if it does
	// not, the tool or parser is broken and every negative below is vacuous.
	plainPath := plainWGPcapPath(t)
	posFlows := runNdpiFlows(t, plainPath)
	if f, label, pat, ok := payloadVPNFlow(posFlows); !ok {
		t.Fatalf("POSITIVE CONTROL FAILED: nDPI did not classify the known plain-WireGuard capture %s as WireGuard/VPN by PAYLOAD (flows: %s). "+
			"The detector or the per-flow parser is broken — the negative assertions would be vacuous.", plainPath, formatFlows(posFlows))
	} else {
		t.Logf("positive control OK: nDPI classified plain WireGuard as %q by payload (matched %q, %s)", label, pat, f.String())
	}

	// (2) Capture the OBFUSCATED wanbond flow (amnezia junk + FEC parity active) on a
	// NON-registered port and run both engines over it. Neither may classify it
	// WireGuard/VPN by payload. The SAME session also runs the zero-DNS-egress guard
	// (Q29/Q33): this config carries no `dns = true` opt-in (every endpoint is an IP
	// literal), so NO packet may appear on the system-DNS/DoT/DoH ports for the whole
	// capture window — proving DNS stays inert on the wire, not just in-process (see
	// the tripwire-resolver unit test in internal/device for the in-process half).
	wbPcap, dnsGuardPcap := captureWanbondFlow(t, top, bin)

	assertZeroDNSEgress(t, dnsGuardPcap)

	negFlows := runNdpiFlows(t, wbPcap)
	if f, label, pat, ok := payloadVPNFlow(negFlows); ok {
		t.Fatalf("REQUIREMENT-6 DEFECT: nDPI classified the OBFUSCATED wanbond flow as a WireGuard/VPN protocol BY PAYLOAD: label %q (matched %q), %s. "+
			"The flow was captured on non-registered port %d, so this is NOT a port guess — the amnezia+FEC obfuscation is leaking a DPI-identifiable payload fingerprint. "+
			"Fix the codec; do NOT weaken this matcher. All nDPI flows: %s",
			label, pat, f.String(), dpiListenPort, formatFlows(negFlows))
	}
	t.Logf("nDPI negative OK: obfuscated wanbond flow on port %d — no PAYLOAD WireGuard/VPN classification (flows: %s)", dpiListenPort, formatFlows(negFlows))

	appProtos, signatures, flows := runSuricata(t, wbPcap)
	if flows == 0 {
		t.Fatalf("suricata decoded ZERO flows from the wanbond capture %s — a zero-decode run cannot certify 'no WireGuard/VPN' (check the capture / suricata config), not a pass.", wbPcap)
	}
	for _, ap := range appProtos {
		if pat, ok := matchVPNLabel(ap); ok {
			t.Fatalf("REQUIREMENT-6 DEFECT: suricata labelled the obfuscated wanbond flow app_proto=%q (matched %q) — a DPI-identifiable VPN fingerprint leaked.", ap, pat)
		}
	}
	for _, sig := range signatures {
		if pat, ok := matchVPNLabel(sig); ok {
			t.Fatalf("REQUIREMENT-6 DEFECT: suricata raised a WireGuard/VPN alert on the obfuscated wanbond flow: %q (matched %q).", sig, pat)
		}
	}
	t.Logf("suricata negative OK: %d flow(s) decoded, app_protos=%v, %d alerts — none WireGuard/VPN", flows, appProtos, len(signatures))

	// (3) Suricata positive is INFORMATIONAL (see the file doc): its default config has
	// no WG parser, so nDPI carries the WG-specific positive control. Record whichever
	// outcome this suricata build gives so a provisioned WG ruleset shows teeth.
	posAppProtos, posSignatures, _ := runSuricata(t, plainPath)
	if labelsHitVPN(posAppProtos) || labelsHitVPN(posSignatures) {
		t.Logf("suricata positive (bonus teeth): classified the known plain-WireGuard capture as WireGuard/VPN (app_protos=%v, alerts=%v)", posAppProtos, posSignatures)
	} else {
		t.Logf("suricata has NO WireGuard app-layer parser/signature in this build (plain-WG app_protos=%v) — this is expected for the stock suricata config; "+
			"nDPI carries the WG-specific positive control, suricata provides the app-layer/anomaly negative check (documented in docs/install.md Limitations).", posAppProtos)
	}
}

// requireDPITool fails the test LOUD if name is not on PATH. A DPI check that silently
// skips when its engine is missing is false assurance, so this never t.Skip()s.
func requireDPITool(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Fatalf("%s not found on PATH: the P5 DPI non-classification check requires it. "+
			"Provision nDPI/Suricata on this host (the check is never skipped — a skipped DPI check is false assurance). lookup error: %v", name, err)
	}
}

// plainWGPcapPath resolves the committed positive-control capture to an absolute path
// and fails if it is missing.
func plainWGPcapPath(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs(plainWGPcap)
	if err != nil {
		t.Fatalf("resolve %s: %v", plainWGPcap, err)
	}
	if _, err := os.Stat(abs); err != nil {
		t.Fatalf("positive-control capture %s missing (regenerate with testdata/gen-plain-wireguard.sh): %v", abs, err)
	}
	return abs
}

// captureWanbondFlow brings up one fresh amnezia+FEC wanbond tunnel over auditPath on the
// NON-registered dpiListenPort, captures the outer UDP wire on the edge veth with tcpdump
// (T26's startPcap — no `-Z root`) while a short bulk transfer drives the full
// DATA/PARITY/PROBE/junk mix, and returns the path to the completed pcap savefile PLUS the
// path to a second, concurrent capture over the SAME veth/window restricted to the
// system-DNS/DoT/DoH ports (dpiDNSGuardFilter) — the zero-DNS-egress guard's evidence. The
// tunnel config carries no `dns = true` opt-in, so the second capture must be empty.
func captureWanbondFlow(t *testing.T, top *Topology, bin string) (wbPcap, dnsGuardPcap string) {
	t.Helper()
	pcapFile := filepath.Join(t.TempDir(), "wanbond-obfuscated.pcap")
	dnsPcapFile := filepath.Join(t.TempDir(), "wanbond-dns-guard.pcap")
	cap := top.startPcap(t, auditPath.edgeVeth, dpiListenPort, pcapFile)
	dnsCap := top.startPcapFilter(t, auditPath.edgeVeth, dpiDNSGuardFilter, dnsPcapFile)

	edge, conc := setupWanbondDPITunnel(t, top, bin, dpiListenPort)
	if !top.pingUntil(concInner, 15*time.Second) {
		cap.stop(t)
		dnsCap.stop(t)
		t.Fatalf("wanbond tunnel never came up\n--- edge ---\n%s\n--- conc ---\n%s", edge.log(), conc.log())
	}
	if mbps := top.iperf3Mbps(t, concInner, dpiLoadSecs); mbps <= 0 {
		cap.stop(t)
		dnsCap.stop(t)
		t.Fatalf("wanbond capture: non-positive throughput %.2f Mbit/s", mbps)
	}
	cap.stop(t)
	dnsCap.stop(t)

	if fi, err := os.Stat(pcapFile); err != nil || fi.Size() == 0 {
		t.Fatalf("wanbond capture %s is missing or empty (err=%v)\n--- tcpdump ---\n%s", pcapFile, err, cap.log())
	}
	if fi, err := os.Stat(dnsPcapFile); err != nil || fi.Size() == 0 {
		t.Fatalf("DNS-guard capture %s is missing or empty (err=%v) — the savefile must at least contain a global header even with zero packets\n--- tcpdump ---\n%s", dnsPcapFile, err, dnsCap.log())
	}
	return pcapFile, dnsPcapFile
}

// assertZeroDNSEgress reads the zero-DNS-egress guard's capture (dpiDNSGuardFilter,
// system-DNS/DoT/DoH ports) and fails the test if it holds ANY packet: with DNS opted
// off, the edge netns must emit and receive NOTHING on those ports (Q29). Once DNS IS
// opted in, this is exactly what would leak: system mode, a cleartext DNS query naming
// the concentrator's hostname; DoH/DoT mode, the TLS ClientHello SNI + timing to the
// resolver.
func assertZeroDNSEgress(t *testing.T, dnsGuardPcap string) {
	t.Helper()
	data, err := os.ReadFile(dnsGuardPcap)
	if err != nil {
		t.Fatalf("read DNS-guard capture %s: %v", dnsGuardPcap, err)
	}
	n, err := wireaudit.CountPcapPackets(data)
	if err != nil {
		t.Fatalf("parse DNS-guard capture %s: %v", dnsGuardPcap, err)
	}
	if n != 0 {
		t.Fatalf("REQUIREMENT Q29/Q33 DEFECT: %d packet(s) on the system-DNS/DoT/DoH ports (53/853/443) egressed the edge netns "+
			"on a DNS-off config (no peer opts in `dns = true`, every endpoint is an IP literal). DNS must stay PROVABLY inert on the "+
			"wire: with DNS opted in, this is exactly what leaks — system mode, a cleartext DNS query naming the concentrator's "+
			"hostname; DoH/DoT mode, the TLS ClientHello SNI + connection timing to the resolver.", n)
	}
	t.Logf("zero-DNS-egress guard OK: 0 packets on ports 53/853/443 over the wanbond capture window (DNS opt-in off)")
}

// setupWanbondDPITunnel brings the edge+concentrator tunnel up over auditPath with BOTH
// the amnezia obfuscation profile (junk active) AND the fixed-ratio FEC plane (parity
// active) on a caller-chosen WireGuard listen port. It mirrors setupAuditTunnel but
// parametrises the port (the P5 capture runs on a non-registered port to defeat nDPI's
// port guess — see dpiListenPort). Addressing/bring-up otherwise match the audit tunnel.
func setupWanbondDPITunnel(t *testing.T, top *Topology, bin string, port int) (edge, conc *proc) {
	t.Helper()

	edgePriv, edgePub := genKey(t)
	concPriv, concPub := genKey(t)
	psk := randKey(t)

	fecBlock := fmt.Sprintf("[fec]\nenabled = true\ndata_shards = %d\nparity_shards = %d\ndeadline = %d\n\n",
		auditFECData, auditFECParity, auditFECDeadlineNanos)

	dir := t.TempDir()
	edgeCfg := writeConfig(t, filepath.Join(dir, "edge.toml"), fmt.Sprintf(`role = "edge"
psk = "%s"

[[paths]]
name = "%s"
source_addr = "%s"

%s%s[wireguard]
private_key = "%s"

[[wireguard.peers]]
public_key = "%s"
endpoint = "%s:%d"
allowed_ips = ["%s/32"]

[log]
level = "error"
`, psk, auditPath.name, auditPath.edgeIP, amneziaProfileA, fecBlock, edgePriv, concPub, auditPath.concIP, port, concInner))

	concCfg := writeConfig(t, filepath.Join(dir, "conc.toml"), fmt.Sprintf(`role = "concentrator"
psk = "%s"

[[paths]]
name = "%s"
source_addr = "%s"

%s%s[wireguard]
private_key = "%s"
listen_port = %d

[[wireguard.peers]]
public_key = "%s"
allowed_ips = ["%s/32"]

[log]
level = "error"
`, psk, auditPath.name, auditPath.concIP, amneziaProfileA, fecBlock, concPriv, port, edgePub, edgeInner))

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

// ndpiFlow is one per-flow classification nDPI reported (from `ndpiReader -v 2`): the
// application protocol, its traffic category, and — decisively — the CONFIDENCE with
// which nDPI assigned it. A "Match by port" confidence is a PORT GUESS (it says nothing
// about the payload); a "DPI"/content confidence is a genuine payload/wire-format match.
type ndpiFlow struct {
	proto      string
	category   string
	confidence string
	raw        string // the trimmed source line, for diagnostics
}

func (f ndpiFlow) String() string {
	return fmt.Sprintf("proto=%q cat=%q confidence=%q", f.proto, f.category, f.confidence)
}

// portGuess reports whether nDPI classified this flow purely by its destination port
// (Confidence: "Match by port") rather than by inspecting the payload. Such a guess is
// EXPECTED for UDP on WireGuard's registered port and must never fail the negative leg.
func (f ndpiFlow) portGuess() bool {
	return strings.Contains(strings.ToLower(f.confidence), "match by port")
}

// payloadVPN reports whether this flow is classified as a WireGuard/VPN protocol OR
// category by a PAYLOAD/CONTENT confidence — i.e. a genuine wire-format fingerprint, not
// a port guess. This is the single predicate both legs use: the positive control asserts
// it PRESENT (the detector has payload teeth); the negative leg asserts it ABSENT.
func (f ndpiFlow) payloadVPN() (label, pattern string, ok bool) {
	if f.portGuess() {
		return "", "", false
	}
	for _, c := range []string{f.proto, f.category} {
		if pat, hit := matchVPNLabel(c); hit {
			return c, pat, true
		}
	}
	return "", "", false
}

// payloadVPNFlow returns the first flow that is a payload-confidence WireGuard/VPN
// classification, if any.
func payloadVPNFlow(flows []ndpiFlow) (flow ndpiFlow, label, pattern string, ok bool) {
	for _, f := range flows {
		if label, pat, hit := f.payloadVPN(); hit {
			return f, label, pat, true
		}
	}
	return ndpiFlow{}, "", "", false
}

func formatFlows(flows []ndpiFlow) string {
	if len(flows) == 0 {
		return "(none)"
	}
	parts := make([]string, len(flows))
	for i, f := range flows {
		parts[i] = f.String()
	}
	return strings.Join(parts, " | ")
}

// nDPI per-flow field extractors. Each per-flow line (from `-v 2`) looks like:
//
//	1  UDP a:p <-> b:q [proto: 206/WireGuard][Stack: WireGuard]...[Confidence: DPI][FPC: 0/Unknown, Confidence: Unknown][cat: VPN/2]...
//
// ndpiFlowConfRE deliberately anchors on `[Confidence:` so it matches the PRIMARY
// confidence bracket and NOT the secondary `[FPC: …, Confidence: …]` first-packet-
// classification bracket (whose text does not start with "Confidence:").
var (
	ndpiFlowProtoRE = regexp.MustCompile(`\[proto:\s*[^/\]]+/([^\]]+)\]`)
	ndpiFlowCatRE   = regexp.MustCompile(`\[cat:\s*([^/\]]+)`)
	ndpiFlowConfRE  = regexp.MustCompile(`\[Confidence:\s*([^\]]+)\]`)
)

// runNdpiFlows runs `ndpiReader -v 2 -i <pcap>` (very verbose = per-flow detail) under a
// timeout and parses the per-flow classification lines. It fails if no flow is found (an
// empty capture or a changed output format would otherwise make the negative leg vacuous).
func runNdpiFlows(t *testing.T, pcap string) []ndpiFlow {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), dpiToolTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, ndpiReaderBin, "-v", "2", "-i", pcap).CombinedOutput()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("ndpiReader timed out after %s on %s\n%s", dpiToolTimeout, pcap, out)
	}
	if err != nil {
		t.Fatalf("ndpiReader on %s failed: %v\n%s", pcap, err, out)
	}
	flows := parseNdpiFlows(string(out))
	if len(flows) == 0 {
		t.Fatalf("ndpiReader produced no per-flow classification for %s — empty capture or output format changed:\n%s", pcap, out)
	}
	return flows
}

// parseNdpiFlows extracts the per-flow classification lines from ndpiReader's very-verbose
// output. A per-flow line is identified by carrying both a `[proto: …]` and a
// `[Confidence: …]` field; the extractors pull the protocol name, category, and the
// primary confidence.
func parseNdpiFlows(out string) []ndpiFlow {
	var flows []ndpiFlow
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024) // per-flow lines can be long (histograms etc.)
	for sc.Scan() {
		line := sc.Text()
		if !strings.Contains(line, "[proto:") || !strings.Contains(line, "[Confidence:") {
			continue
		}
		f := ndpiFlow{raw: strings.TrimSpace(line)}
		if m := ndpiFlowProtoRE.FindStringSubmatch(line); m != nil {
			f.proto = strings.TrimSpace(m[1])
		}
		if m := ndpiFlowCatRE.FindStringSubmatch(line); m != nil {
			f.category = strings.TrimSpace(m[1])
		}
		if m := ndpiFlowConfRE.FindStringSubmatch(line); m != nil {
			f.confidence = strings.TrimSpace(m[1])
		}
		flows = append(flows, f)
	}
	return flows
}

// suricataConfigArgs returns the `-c <config>` args to use, if a config is locatable:
// the WANBOND_SURICATA_CONFIG override first, then the standard install path. When
// neither exists it returns nil so suricata uses its own built-in config search.
func suricataConfigArgs() []string {
	if c := os.Getenv(suricataConfigEnv); c != "" {
		return []string{"-c", c}
	}
	if _, err := os.Stat(defaultSuricataConfig); err == nil {
		return []string{"-c", defaultSuricataConfig}
	}
	return nil
}

// runSuricata runs `suricata -r <pcap> -l <dir> -k none [-c <config>]` under a timeout and
// parses the resulting eve.json into the distinct app-layer protocols, alert
// signatures/categories, and the number of flow records it emitted. `-k none` disables
// checksum validation so offloaded-checksum captures (veth/loopback) are not dropped.
func runSuricata(t *testing.T, pcap string) (appProtos, signatures []string, flows int) {
	t.Helper()
	dir := t.TempDir()
	args := []string{"-r", pcap, "-l", dir, "-k", "none"}
	args = append(args, suricataConfigArgs()...)

	ctx, cancel := context.WithTimeout(context.Background(), dpiToolTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, suricataBin, args...).CombinedOutput()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("suricata timed out after %s on %s\n%s", dpiToolTimeout, pcap, out)
	}
	if err != nil {
		t.Fatalf("suricata on %s failed: %v\n%s", pcap, err, out)
	}

	eve := filepath.Join(dir, suricataEveFile)
	data, rerr := os.ReadFile(eve)
	if rerr != nil {
		t.Fatalf("suricata wrote no %s at %s (a misconfigured suricata, not a pass): %v\n--- suricata output ---\n%s", suricataEveFile, eve, rerr, out)
	}
	return parseSuricataEve(t, data)
}

// suricataEvent is the subset of an eve.json record this check inspects: the event type
// (to count decoded flows), the app-layer protocol suricata assigned the flow, and, for
// alert records, the rule that fired.
type suricataEvent struct {
	EventType string `json:"event_type"`
	AppProto  string `json:"app_proto"`
	Alert     struct {
		Signature string `json:"signature"`
		Category  string `json:"category"`
	} `json:"alert"`
}

// parseSuricataEve parses newline-delimited eve.json into the distinct non-empty app-layer
// protocol values, alert signatures/categories, and the count of "flow" event records.
// Robust to the mixed event stream (flow/stats/alert): it keys off event fields.
func parseSuricataEve(t *testing.T, data []byte) (appProtos, signatures []string, flows int) {
	t.Helper()
	seenProto := map[string]struct{}{}
	seenSig := map[string]struct{}{}
	sc := bufio.NewScanner(strings.NewReader(string(data)))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024) // eve records can exceed the 64KB default
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var ev suricataEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			// A single malformed line is not a test failure by itself, but surfacing it
			// aids diagnosis if the format shifted.
			t.Logf("parseSuricataEve: skipping unparseable eve.json line: %v", err)
			continue
		}
		if ev.EventType == "flow" {
			flows++
		}
		if ev.AppProto != "" {
			if _, ok := seenProto[ev.AppProto]; !ok {
				seenProto[ev.AppProto] = struct{}{}
				appProtos = append(appProtos, ev.AppProto)
			}
		}
		for _, s := range []string{ev.Alert.Signature, ev.Alert.Category} {
			if s == "" {
				continue
			}
			if _, ok := seenSig[s]; !ok {
				seenSig[s] = struct{}{}
				signatures = append(signatures, s)
			}
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan eve.json: %v", err)
	}
	return appProtos, signatures, flows
}

// labelsHitVPN reports whether any string in ss matches a vpnLabelPattern.
func labelsHitVPN(ss []string) bool {
	for _, s := range ss {
		if _, ok := matchVPNLabel(s); ok {
			return true
		}
	}
	return false
}
