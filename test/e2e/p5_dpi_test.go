//go:build e2e

package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// Requirement-6 (DPI-resistance) EXTERNAL-TOOL non-classification check (M9).
//
// TestWireFormatAudit (T26) proves the obfuscation properties of the wanbond wire
// PROGRAMMATICALLY (entropy + no fixed-offset fingerprint). This test complements it
// by running the CAPTURED wire through the two production DPI engines an adversary on
// a hostile network would use — nDPI (ndpiReader) and Suricata — and asserting NEITHER
// classifies the obfuscated wanbond flow as WireGuard or any identified VPN protocol.
//
// NON-VACUITY / POSITIVE CONTROL. A bare "not detected" result is meaningless unless
// the detector demonstrably DETECTS WireGuard when it is present. wanbond ALWAYS
// obfuscates via its outer frame codec (amnezia + the FEC-framed Bind), so a plain-WG
// reference cannot be produced from wanbond itself. Option (a) of the task — bringing
// up plain amneziawg-go in-process via tun/netstack — is NOT tractable here: importing
// amneziawg-go/tun/netstack pulls gVisor, whose vendored tcpip/stack package fails to
// build under the go toolchain (bridge_test.go declares `package bridge_test`, a name
// mismatch the loader rejects); and two kernel-TUN WireGuard endpoints in ONE netns
// short-circuit (a packet to the peer's on-link /32 never traverses the WG socket), so
// real WG-on-the-wire needs two netns and a second process — disproportionate. So this
// test uses option (b): a COMMITTED genuine plain-WireGuard capture
// (testdata/plain-wireguard.pcap, real kernel-WG handshake+transport between two netns)
// as the positive control. nDPI classifies it `WireGuard`; if it does not, the tool or
// the parser is broken and the negative assertion is vacuous — so the positive control
// is asserted FIRST and hard.
//
// nDPI carries the WG-SPECIFIC positive control. Suricata 8.x's default configuration
// ships NO WireGuard app-layer parser or signature (it labels even a plain-WG flow
// app_proto="failed"), so it cannot serve as a WG positive control; its value here is
// the app-layer/anomaly NEGATIVE check. This is DOCUMENTED (docs/install.md Limitations
// and the log lines below) and the suricata positive run is informational only — a host
// that DOES provision a WG ruleset records bonus teeth, but the mandatory WG detection
// proof rests on nDPI.
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

	// plainWGPcap is the committed positive-control capture (real plain WireGuard).
	plainWGPcap = "testdata/plain-wireguard.pcap"

	// suricataConfigEnv optionally overrides the suricata.yaml path; defaultSuricataConfig
	// is the standard location a provisioned suricata install writes. When neither is
	// present, suricata falls back to its own built-in config search.
	suricataConfigEnv     = "WANBOND_SURICATA_CONFIG"
	defaultSuricataConfig = "/etc/suricata/suricata.yaml"

	// suricataEveFile is the JSON event log suricata writes into its -l directory.
	suricataEveFile = "eve.json"
)

// vpnLabelPatterns are the lowercase substrings that mark a DPI label as a WireGuard or
// otherwise-identified VPN classification. A hit on the OBFUSCATED wanbond flow is a
// requirement-6 defect; a hit on the plain-WG positive control is the expected teeth.
// A classification of Unknown/QUIC/DNS/etc. matches none of these and is fine.
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

	// (1) POSITIVE CONTROL FIRST — assert the detector has teeth before trusting a
	// "not classified" result. nDPI MUST classify the known plain-WireGuard capture as
	// WireGuard/VPN; if not, the tool or parser is broken and every negative below is
	// vacuous.
	plainPath := plainWGPcapPath(t)
	posLabels := runNdpi(t, plainPath)
	if hit, pat, ok := firstVPNLabel(posLabels); !ok {
		t.Fatalf("POSITIVE CONTROL FAILED: ndpiReader did not classify the known plain-WireGuard capture %s as WireGuard/VPN (labels: %s). "+
			"The detector or the output parser is broken — the negative assertions would be vacuous.", plainPath, formatLabels(posLabels))
	} else {
		t.Logf("positive control OK: ndpiReader classified plain WireGuard as %s (matched %q)", hit, pat)
	}

	// (2) Capture the OBFUSCATED wanbond flow (amnezia junk + FEC parity active) and run
	// both engines over it. NEITHER may classify it WireGuard/VPN.
	wbPcap := captureWanbondFlow(t, top, bin)

	negLabels := runNdpi(t, wbPcap)
	if hit, pat, ok := firstVPNLabel(negLabels); ok {
		t.Fatalf("REQUIREMENT-6 DEFECT: nDPI classified the OBFUSCATED wanbond flow as a WireGuard/VPN protocol: %s (matched %q). "+
			"All nDPI labels: %s. The amnezia+FEC obfuscation is leaking a DPI-identifiable fingerprint — fix the codec, do NOT weaken this matcher.",
			hit, pat, formatLabels(negLabels))
	}
	t.Logf("nDPI negative OK: obfuscated wanbond flow labelled %s — no WireGuard/VPN", formatLabels(negLabels))

	appProtos, signatures := runSuricata(t, wbPcap)
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
	t.Logf("suricata negative OK: app_protos=%v, %d alerts — none WireGuard/VPN", appProtos, len(signatures))

	// (3) Suricata positive is INFORMATIONAL (see the file doc): its default config has
	// no WG parser, so nDPI carries the WG-specific positive control. Record whichever
	// outcome this suricata build gives so a provisioned WG ruleset shows teeth.
	posAppProtos, posSignatures := runSuricata(t, plainPath)
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

// captureWanbondFlow brings up one fresh amnezia+FEC wanbond tunnel over auditPath,
// captures the outer UDP wire on the edge veth with tcpdump (T26's startPcap — no
// `-Z root`) while a short bulk transfer drives the full DATA/PARITY/PROBE/junk mix,
// and returns the path to the completed pcap savefile.
func captureWanbondFlow(t *testing.T, top *Topology, bin string) string {
	t.Helper()
	pcapFile := filepath.Join(t.TempDir(), "wanbond-obfuscated.pcap")
	cap := top.startPcap(t, auditPath.edgeVeth, listenPort, pcapFile)

	edge, conc := setupAuditTunnel(t, top, bin)
	if !top.pingUntil(concInner, 15*time.Second) {
		cap.stop(t)
		t.Fatalf("wanbond tunnel never came up\n--- edge ---\n%s\n--- conc ---\n%s", edge.log(), conc.log())
	}
	if mbps := top.iperf3Mbps(t, concInner, dpiLoadSecs); mbps <= 0 {
		cap.stop(t)
		t.Fatalf("wanbond capture: non-positive throughput %.2f Mbit/s", mbps)
	}
	cap.stop(t)

	if fi, err := os.Stat(pcapFile); err != nil || fi.Size() == 0 {
		t.Fatalf("wanbond capture %s is missing or empty (err=%v)\n--- tcpdump ---\n%s", pcapFile, err, cap.log())
	}
	return pcapFile
}

// ndpiLabel is one classification nDPI reported: a detected protocol name or a traffic
// category, tagged so a hit can name which.
type ndpiLabel struct {
	kind string // "protocol" | "category"
	name string
}

func (l ndpiLabel) String() string { return l.kind + "=" + l.name }

func formatLabels(labels []ndpiLabel) string {
	if len(labels) == 0 {
		return "(none)"
	}
	parts := make([]string, len(labels))
	for i, l := range labels {
		parts[i] = l.String()
	}
	return strings.Join(parts, ", ")
}

// firstVPNLabel returns the first label whose name matches a vpnLabelPattern.
func firstVPNLabel(labels []ndpiLabel) (label ndpiLabel, pattern string, ok bool) {
	for _, l := range labels {
		if pat, hit := matchVPNLabel(l.name); hit {
			return l, pat, true
		}
	}
	return ndpiLabel{}, "", false
}

// runNdpi runs `ndpiReader -i <pcap>` under a timeout and parses its detected-protocol
// and category breakdown into labels.
func runNdpi(t *testing.T, pcap string) []ndpiLabel {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), dpiToolTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, ndpiReaderBin, "-i", pcap).CombinedOutput()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("ndpiReader timed out after %s on %s\n%s", dpiToolTimeout, pcap, out)
	}
	if err != nil {
		t.Fatalf("ndpiReader on %s failed: %v\n%s", pcap, err, out)
	}
	labels := parseNdpiLabels(string(out))
	if len(labels) == 0 {
		t.Fatalf("ndpiReader produced no protocol/category breakdown for %s — output format may have changed:\n%s", pcap, out)
	}
	return labels
}

// ndpiSectionLineRE matches an indented "<Name> ... packets:" row in ndpiReader's
// "Detected protocols:" and "Category statistics:" sections; group 1 is the label name.
var ndpiSectionLineRE = regexp.MustCompile(`^\s+(\S+)\s+packets:`)

// parseNdpiLabels extracts the detected protocol names and traffic-category names from
// ndpiReader's textual report. It scopes to the "Detected protocols:" and
// "Category statistics:" sections (each an indented block terminated by a blank line)
// rather than substring-scanning the whole output, so unrelated banner text cannot
// produce a spurious hit.
func parseNdpiLabels(out string) []ndpiLabel {
	var labels []ndpiLabel
	sc := bufio.NewScanner(strings.NewReader(out))
	// Section headers ndpiReader prints; the value is the label kind emitted for rows
	// within that section.
	kind := ""
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.Contains(line, "Detected protocols:"):
			kind = "protocol"
			continue
		case strings.Contains(line, "Category statistics:"):
			kind = "category"
			continue
		}
		if kind == "" {
			continue
		}
		if strings.TrimSpace(line) == "" {
			kind = "" // end of the current indented section
			continue
		}
		if m := ndpiSectionLineRE.FindStringSubmatch(line); m != nil {
			labels = append(labels, ndpiLabel{kind: kind, name: m[1]})
		}
	}
	return labels
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

// runSuricata runs `suricata -r <pcap> -l <dir> -k none [-c <config>]` under a timeout
// and parses the resulting eve.json into the set of app-layer protocols and alert
// signatures it observed. `-k none` disables checksum validation so offloaded-checksum
// captures (veth/loopback) are not silently dropped.
func runSuricata(t *testing.T, pcap string) (appProtos, signatures []string) {
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

// suricataEvent is the subset of an eve.json record this check inspects: the app-layer
// protocol suricata assigned the flow and, for alert records, the rule that fired.
type suricataEvent struct {
	EventType string `json:"event_type"`
	AppProto  string `json:"app_proto"`
	Alert     struct {
		Signature string `json:"signature"`
		Category  string `json:"category"`
	} `json:"alert"`
}

// parseSuricataEve parses newline-delimited eve.json into the distinct non-empty
// app-layer protocol values and alert signatures/categories it contains. Robust to the
// mixed event stream (flow/stats/alert): it keys off event fields, not line position.
func parseSuricataEve(t *testing.T, data []byte) (appProtos, signatures []string) {
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
	return appProtos, signatures
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
