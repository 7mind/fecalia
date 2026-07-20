//go:build e2e

package e2e

import (
	"fmt"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestE2EDaemonMSSClampLifecycle is the T208/D85 acceptance for the daemon-owned,
// edge-originated TCP MSS clamp on wanbond0: at device.Up (edge role) the daemon installs
// a mangle/OUTPUT-chain TCPMSS --clamp-mss-to-pmtu rule for BOTH IPv4 and IPv6, the install
// is idempotent across a crash+restart (a re-run of Up never stacks a duplicate), and the
// rule is WITHDRAWN on a clean Close. It is the privileged, HARDWARE-TIER half of the test
// split: the MSS-derivation arithmetic is pinned by the non-privileged
// internal/device.TestClampMSS; this subtest exercises the actual netfilter lifecycle and
// runs only under the netns+root e2e harness (compiled and vetted in CI, executed on the
// hardware suite — NOT in the local sandbox).
//
// tun_persist=true so wanbond0 SURVIVES both the simulated crash and the clean Close: the
// interface persisting across the crash lets the restart re-adopt it and re-run Up over the
// leaked rule (the idempotency case), and the interface persisting across Close proves the
// post-Close "rule is gone" assertion is the daemon's OWN removeMSSClamp, not a side effect
// of the interface (and its `-o wanbond0` rule) vanishing. No concentrator process is
// needed: the clamp is programmed at bring-up (tolerant boot, like the tun_persist and
// default-route tests) and observed directly in the edge netns's mangle table.
func TestE2EDaemonMSSClampLifecycle(t *testing.T) {
	top := Setup(t)
	p := top.path("starlink")
	bin := buildWanbond(t)

	edgePriv, _ := genKey(t)
	_, concPub := genKey(t)
	psk := randKey(t)

	dir := t.TempDir()
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

	// The rule and the persistent device both outlive a crash, so clean BOTH up so a wedged
	// run cannot leak clamp rules (or wanbond0) into later subtests sharing this netns.
	t.Cleanup(func() {
		_ = top.tryRun("ip", "link", "del", tunDev)
		top.removeMSSClampRules()
	})

	// --- Up: the clamp is present for v4 AND v6 after bring-up. ---
	edge := top.startProc(t, "edge", bin, "--config", cfg)
	if !top.waitLink(tunDev, false, 5*time.Second) {
		t.Fatalf("wanbond0 never appeared\n%s", edge.log())
	}
	if got := top.mssClampCount(t, "iptables"); got != 1 {
		t.Fatalf("IPv4 MSS clamp count after Up = %d, want 1\n%s", got, edge.log())
	}
	if got := top.mssClampCount(t, "ip6tables"); got != 1 {
		t.Fatalf("IPv6 MSS clamp count after Up = %d, want 1\n%s", got, edge.log())
	}

	// --- Idempotency across a double-Up: SIGKILL leaves the rule (and, under tun_persist,
	// wanbond0) behind; the restart re-runs Up over the leaked rule and must NOT stack a
	// duplicate — exactly one rule per family remains. ---
	top.crash(t, edge)
	if !top.waitLink(tunDev, false, 2*time.Second) {
		t.Fatalf("persistent wanbond0 disappeared after crash (tun_persist=true) — cannot exercise idempotent re-Up")
	}
	edge2 := top.startProc(t, "edge", bin, "--config", cfg)
	if !top.waitLink(tunDev, false, 5*time.Second) {
		t.Fatalf("wanbond0 never re-appeared after restart\n%s", edge2.log())
	}
	if got := top.mssClampCount(t, "iptables"); got != 1 {
		t.Fatalf("IPv4 MSS clamp count after crash+restart = %d, want 1 (idempotent, no duplicate)\n%s", got, edge2.log())
	}
	if got := top.mssClampCount(t, "ip6tables"); got != 1 {
		t.Fatalf("IPv6 MSS clamp count after crash+restart = %d, want 1 (idempotent, no duplicate)\n%s", got, edge2.log())
	}

	// --- Close: a clean stop withdraws the clamp for both families. wanbond0 survives
	// (tun_persist), so the rule's disappearance is the daemon's own removeMSSClamp. ---
	top.stopAndWait(t, edge2)
	if !top.waitLink(tunDev, false, 2*time.Second) {
		t.Fatalf("persistent wanbond0 disappeared after clean stop (tun_persist=true) — cannot attribute clamp removal to the daemon")
	}
	if got := top.mssClampCount(t, "iptables"); got != 0 {
		t.Fatalf("IPv4 MSS clamp survived Close: count = %d, want 0", got)
	}
	if got := top.mssClampCount(t, "ip6tables"); got != 0 {
		t.Fatalf("IPv6 MSS clamp survived Close: count = %d, want 0", got)
	}
	t.Logf("edge-originated MSS clamp: present (v4+v6) after Up, idempotent across crash+restart, withdrawn on Close (T208, D85)")
}

// mssClampRuleSpec is the OUTPUT-chain rule-spec the daemon installs, mirrored here for the
// test's own count/cleanup so the assertions match the daemon's exact match criteria.
func mssClampRuleSpec() []string {
	return []string{
		"-t", "mangle", "OUTPUT",
		"-o", tunDev,
		"-p", "tcp",
		"--tcp-flags", "SYN,RST", "SYN",
		"-j", "TCPMSS", "--clamp-mss-to-pmtu",
	}
}

// mssClampCount returns how many copies of the daemon's clamp rule bin's mangle OUTPUT chain
// carries (0, 1, or — on a stacking regression — more). `iptables -C` only reports presence,
// not multiplicity, so the count is derived by counting the `-A OUTPUT ... -o wanbond0 ...
// TCPMSS --clamp-mss-to-pmtu` lines that `-S` (list rules as commands) prints.
func (top *Topology) mssClampCount(t *testing.T, bin string) int {
	t.Helper()
	out := top.runOut(bin, "-t", "mangle", "-S", "OUTPUT")
	n := 0
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, tunDev) && strings.Contains(line, "TCPMSS") && strings.Contains(line, "clamp-mss-to-pmtu") {
			n++
		}
	}
	return n
}

// removeMSSClampRules best-effort deletes every copy of the clamp rule for both families,
// used by the test cleanup to guarantee no rule leaks into a later subtest.
func (top *Topology) removeMSSClampRules() {
	spec := mssClampRuleSpec()
	for _, bin := range []string{"iptables", "ip6tables"} {
		checkArgs := append([]string{spec[0], spec[1], "-C"}, spec[2:]...)
		delArgs := append([]string{spec[0], spec[1], "-D"}, spec[2:]...)
		for i := 0; i < 16; i++ {
			if top.tryRun(bin, checkArgs...) != nil {
				break
			}
			_ = top.tryRun(bin, delArgs...)
		}
	}
}

// crash SIGKILLs p and reaps it, disarming the startProc terminator. Unlike stopAndWait
// (SIGTERM → a clean Close that removes the clamp), SIGKILL denies the daemon its Close, so
// the clamp rule (and, under tun_persist, wanbond0) survives — the setup for the idempotent
// re-Up assertion.
func (top *Topology) crash(t *testing.T, p *proc) {
	t.Helper()
	if p.cmd.Process == nil {
		return
	}
	_ = p.cmd.Process.Signal(syscall.SIGKILL)
	done := make(chan struct{})
	go func() { _ = p.cmd.Wait(); close(done) }()
	<-done
	p.cmd.Process = nil // disarm the t.Cleanup terminator (no double Wait)
}
