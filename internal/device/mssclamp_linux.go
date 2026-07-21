//go:build linux

package device

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// tcpV4MSSOverhead is the inner IPv4 (20) + TCP (20) header cost subtracted from
// wanbond0's inner MTU to yield the largest MSS an edge-originated TCP segment may
// carry without the wrapped datagram exceeding a path MTU (the arithmetic lives in
// docs/p1-mtu.md). The installed rule uses --clamp-mss-to-pmtu, which derives the same
// value from the LIVE interface MTU (so it tracks a runtime MTU resize for free rather
// than pinning a stale MSS); clampMSS reproduces the effective value for the boot log
// line and is pinned by TestClampMSS.
const tcpV4MSSOverhead = 40

// clampMSS returns the effective TCP MSS the OUTPUT-chain clamp enforces for an inner
// MTU of innerMTU: innerMTU minus the inner IPv4+TCP header overhead.
func clampMSS(innerMTU int) int { return innerMTU - tcpV4MSSOverhead }

// mssClampTable / mssClampChain name the netfilter location of the daemon-owned clamp:
// the mangle-table OUTPUT chain, matching LOCALLY ORIGINATED (edge-originated) TCP. This
// is DISJOINT from G14's operator-installed FORWARD-chain clamp for routed-LAN traffic
// (docs/p1-mtu.md) — a segment is either locally originated (OUTPUT) or forwarded
// (FORWARD), never both, so the two clamps are complementary, never redundant.
const (
	mssClampTable = "mangle"
	mssClampChain = "OUTPUT"
)

// maxMSSClampDeletes bounds the remove loop so a pathological iptables that keeps
// reporting the rule present can never spin forever. A crash-free install adds at most
// one rule per family; the loop only ever repeats to clear a duplicate a pre-idempotency
// crash might have stacked.
const maxMSSClampDeletes = 16

// mssClampBinaries pairs each IP family's iptables front-end with a human label used in
// error messages; both families are clamped so IPv4 and IPv6 edge-originated TCP alike
// stay inside the tunnel MTU.
var mssClampBinaries = []struct{ family, bin string }{
	{"IPv4", "iptables"},
	{"IPv6", "ip6tables"},
}

// The clamp is programmed by EXEC'ing the iptables/ip6tables front-ends rather than
// natively via netlink: the daemon links no nftables/libnftnl binding (its only netlink
// use — route programming in route_linux.go — hand-rolls the far simpler rtnetlink
// protocol, not nftables' expression graph), and adding one is out of this task's scope.
// The exec path fails fast with a clear error when the front-end binary is absent (see
// ensureRule), satisfying the "if exec is chosen, fail fast on a missing binary" contract.

// mssClampRule returns the iptables rule-spec (WITHOUT the -A/-C/-D verb and chain) for
// the edge-originated TCP MSS clamp egressing ifname: the SYN of every TCP flow leaving
// wanbond0 gets its MSS clamped down to the path MTU. --tcp-flags SYN,RST SYN matches a
// pure SYN (the only segment carrying the MSS option) and skips a SYN,ACK+RST. Using
// --clamp-mss-to-pmtu (rather than a pinned --set-mss) lets the kernel derive the MSS
// from the live TUN MTU, so a runtime MTU resize needs no rule re-install.
func mssClampRule(ifname string) []string {
	return []string{
		"-o", ifname,
		"-p", "tcp",
		"--tcp-flags", "SYN,RST", "SYN",
		"-j", "TCPMSS", "--clamp-mss-to-pmtu",
	}
}

// iptablesArgs prefixes the xtables lock wait (-w, so a concurrent operator iptables run
// cannot make ours fail with a transient lock error), the mangle table, the verb
// (-A/-C/-D), and the OUTPUT chain onto a rule-spec.
func iptablesArgs(verb string, rule []string) []string {
	args := []string{"-w", "-t", mssClampTable, verb, mssClampChain}
	return append(args, rule...)
}

// lookPath indirects exec.LookPath so a test can simulate a missing front-end binary
// without mutating the process PATH (T232, defect D92).
var lookPath = exec.LookPath

// ruleExists reports whether bin's chain already carries the rule (iptables -C exits 0).
// A non-zero exit means the rule is absent; it is NOT surfaced as an error here — the
// caller has already confirmed the binary exists via LookPath, so a non-zero -C is the
// "not present" signal, not a fault.
func ruleExists(bin string, rule []string) bool {
	return exec.Command(bin, iptablesArgs("-C", rule)...).Run() == nil
}

// ensureRule adds the rule to bin's chain if it is not already present, so re-running Up
// after a crash never stacks a duplicate (idempotent install). It fails fast with a clear,
// actionable error when the front-end binary is not installed.
func ensureRule(bin string, rule []string) error {
	if _, err := lookPath(bin); err != nil {
		// A missing front-end binary is BENIGN and NON-FATAL (D92): classify it with the
		// errMSSClampBinaryMissing sentinel so device.Up degrades to a WARN + continue rather
		// than aborting bring-up into a restart loop on an nft-only host.
		return fmt.Errorf("%s not found on PATH (install iptables/nft, or program the MSS clamp out of band): %w", bin, errMSSClampBinaryMissing)
	}
	if ruleExists(bin, rule) {
		return nil
	}
	if out, err := exec.Command(bin, iptablesArgs("-A", rule)...).CombinedOutput(); err != nil {
		return fmt.Errorf("%s -A: %w: %s", bin, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// deleteRule removes the rule from bin's chain, idempotently: an already-absent rule is a
// no-op, and a missing front-end binary is tolerated (nothing could have been installed
// through it, so there is nothing to withdraw). The bounded loop also clears any duplicate
// a pre-idempotency crash might have stacked.
func deleteRule(bin string, rule []string) error {
	if _, err := lookPath(bin); err != nil {
		return nil
	}
	for i := 0; i < maxMSSClampDeletes && ruleExists(bin, rule); i++ {
		if out, err := exec.Command(bin, iptablesArgs("-D", rule)...).CombinedOutput(); err != nil {
			return fmt.Errorf("%s -D: %w: %s", bin, err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// installMSSClamp installs the edge-originated TCP MSS clamp on ifname's OUTPUT chain for
// BOTH IPv4 and IPv6, idempotently. On any error it best-effort withdraws whatever partial
// state landed (mirroring installRoutes' removeRoutes-on-error), so a failed install never
// leaves a half-programmed clamp behind.
func installMSSClamp(ifname string) error {
	rule := mssClampRule(ifname)
	for _, b := range mssClampBinaries {
		if err := ensureRule(b.bin, rule); err != nil {
			_ = removeMSSClamp(ifname)
			return fmt.Errorf("%s MSS clamp on %s: %w", b.family, ifname, err)
		}
	}
	return nil
}

// removeMSSClamp withdraws the clamp for both families, idempotently (a double Close, or a
// family whose install never landed, is a no-op). Unlike a route — which the kernel drops
// when wanbond0 is destroyed — an iptables rule matching `-o <ifname>` stores the interface
// NAME and SURVIVES the interface, so Close MUST remove it explicitly or it leaks across a
// tun_persist restart. Errors are aggregated so Close can log them.
func removeMSSClamp(ifname string) error {
	rule := mssClampRule(ifname)
	var errs []error
	for _, b := range mssClampBinaries {
		if err := deleteRule(b.bin, rule); err != nil {
			errs = append(errs, fmt.Errorf("%s MSS clamp on %s: %w", b.family, ifname, err))
		}
	}
	return errors.Join(errs...)
}
