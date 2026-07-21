//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestE2EEdgeBringUpNftOnly is the T234/D92 acceptance for an nft-only host: an EDGE
// tunnel must BRING UP and STAY UP when the iptables/ip6tables front-ends are ABSENT
// from the daemon's PATH — no fatal, no exit-1 systemd restart loop — and, when `nft`
// is present, the edge MSS clamp is programmed via the daemon-owned
// `table inet wanbond_mssclamp` instead.
//
// REPRODUCE-FIRST / RED-on-pre-fix: before T232 (non-fatal missing front-end) and T233
// (nft fallback), device.Up treated a missing iptables front-end as FATAL, so on an
// nft-only host device.Up returned an error, cmd/wanbond exited non-zero BEFORE logging
// "tunnel interface up", and systemd restart-looped the whole tunnel down (defect D92:
// 42 restarts). Against that pre-fix tree the core assertion below —
// waitLogContains(edge, "tunnel interface up") — would RED (the daemon exits; the line
// never appears). On the post-T232/T233 tree it passes: bring-up completes and the clamp
// degrades gracefully (nft fallback when nft is present, a single WARN when neither
// front-end resolves). The pre-fix tree cannot be run here; this pins the post-fix
// invariant.
//
// PATH is controlled per-daemon via startProcEnv (Go's exec dedups the env, so the
// appended PATH= wins): each case runs the daemon with a PATH pointing at a temp bin dir
// that mirrors the host PATH MINUS iptables/ip6tables (and, for the neither-present case,
// minus nft too) — the faithful emulation of an nft-only / no-netfilter-CLI host. The
// daemon programs the interface and routes via netlink (no `ip` binary) and this
// topology uses IP endpoints (no DNS exec), so the only external binaries it ever execs
// are the MSS-clamp front-ends; stripping them from PATH exercises exactly the D92 seam.
//
// The daemon runs in the current (edge/test) network namespace like the T208 MSS-clamp
// e2e, so the nft table it programs is observed directly from the test process (no
// nsenter). It deliberately needs NO root netns and no concentrator process — bring-up is
// tolerant-boot, and the clamp is installed at device.Up — so it does not collide with
// the o3 host's persistent root-netns concentrator.
func TestE2EEdgeBringUpNftOnly(t *testing.T) {
	bin := buildWanbond(t)

	// --- nft present: bring-up succeeds AND the clamp lands via the nft fallback. ---
	// Gate on the executing host actually having `nft`; a CI host without it cannot
	// exercise the fallback, so skip that sub-assertion gracefully rather than fail.
	t.Run("nft_present", func(t *testing.T) {
		if _, err := exec.LookPath("nft"); err != nil {
			t.Skipf("host has no nft binary (%v); cannot exercise the nft fallback", err)
		}
		top := Setup(t)
		// PATH mirrors the host MINUS the iptables front-ends, so LookPath("iptables")
		// fails in the daemon and installMSSClamp falls back to nft (which IS present).
		binDir := mirrorPathExcluding(t, "iptables", "ip6tables", "iptables-legacy", "ip6tables-legacy")
		// Clean up the daemon-owned nft table (edge netns) even if teardown races a wedged run.
		t.Cleanup(func() { _ = exec.Command("nft", "delete", "table", "inet", "wanbond_mssclamp").Run() })

		edge := startEdgeNftOnly(t, top, bin, binDir)

		// Bring-up completed past the clamp install (the pre-fix RED signal) and the
		// process did not exit.
		if !waitLogContains(edge, "tunnel interface up", 8*time.Second) {
			t.Fatalf("edge never brought up on an nft-only PATH (no \"tunnel interface up\"); pre-T232 fatal regression?\n%s", edge.log())
		}
		assertEdgeAliveNoFatal(t, edge)

		// The clamp took the nft fallback: the daemon logged the install AND the
		// daemon-owned inet table carries the oifname/maxseg rule.
		if !waitLogContains(edge, "MSS clamp installed", 3*time.Second) {
			t.Fatalf("nft present but the daemon never logged \"MSS clamp installed\"\n%s", edge.log())
		}
		out, err := exec.Command("nft", "list", "table", "inet", "wanbond_mssclamp").CombinedOutput()
		if err != nil {
			t.Fatalf("nft list table inet wanbond_mssclamp (edge netns) failed: %v\n%s\n--- daemon log ---\n%s", err, out, edge.log())
		}
		ruleset := string(out)
		if !strings.Contains(ruleset, tunDev) || !strings.Contains(ruleset, "maxseg") {
			t.Fatalf("nft table wanbond_mssclamp lacks the oifname %q / maxseg clamp rule:\n%s", tunDev, ruleset)
		}
		t.Logf("nft-only bring-up: tunnel up, clamp programmed via table inet wanbond_mssclamp (T234, D92)")
	})

	// --- neither present: bring-up STILL succeeds; the clamp degrades to a single WARN. ---
	t.Run("neither_present", func(t *testing.T) {
		top := Setup(t)
		// PATH mirrors the host MINUS every netfilter CLI, so installMSSClamp finds no
		// front-end at all and reports errMSSClampBinaryMissing -> WARN + continue.
		binDir := mirrorPathExcluding(t, "iptables", "ip6tables", "iptables-legacy", "ip6tables-legacy", "nft")

		edge := startEdgeNftOnly(t, top, bin, binDir)

		if !waitLogContains(edge, "tunnel interface up", 8*time.Second) {
			t.Fatalf("edge never brought up with no netfilter CLI on PATH (no \"tunnel interface up\"); pre-T232 fatal regression?\n%s", edge.log())
		}
		assertEdgeAliveNoFatal(t, edge)

		// A missing front-end is non-fatal: exactly the WARN, never an install log line.
		if !waitLogContains(edge, "MSS clamp not installed", 3*time.Second) {
			t.Fatalf("no front-end present but the daemon never logged the WARN \"MSS clamp not installed\"\n%s", edge.log())
		}
		if strings.Contains(edge.log(), "MSS clamp installed") {
			t.Fatalf("no front-end present but the daemon logged \"MSS clamp installed\"\n%s", edge.log())
		}
		t.Logf("no-netfilter-CLI bring-up: tunnel up, clamp degraded to a single WARN (T234, D92)")
	})
}

// startEdgeNftOnly writes an edge config for the "starlink" path and launches the daemon
// with PATH pinned to binDir (via startProcEnv, whose appended PATH wins under exec's env
// dedup). The config mirrors the T208 MSS-clamp e2e's edge config — the minimal edge role
// that brings wanbond0 up and installs the edge clamp at device.Up — with no metrics/
// monitor listeners (so nothing binds a shared 127.0.0.1 port across the two sub-tests).
func startEdgeNftOnly(t *testing.T, top *Topology, bin, binDir string) *proc {
	t.Helper()
	p := top.path("starlink")

	edgePriv, _ := genKey(t)
	_, concPub := genKey(t)
	psk := randKey(t)

	dir := t.TempDir()
	cfg := writeConfig(t, filepath.Join(dir, "edge.toml"), fmt.Sprintf(`role = "edge"
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
level = "info"
`, psk, p.edgeIP, edgePriv, concPub, p.concIP, listenPort, concInner))

	return top.startProcEnv(t, "edge", []string{"PATH=" + binDir}, bin, "--config", cfg)
}

// assertEdgeAliveNoFatal proves the daemon did NOT exit after bring-up: it settles
// briefly, then checks the OS process is still running (its /proc state is not zombie/
// dead — a self-exit before the t.Cleanup reap would leave a zombie) and that the log
// carries neither the runtime-fatal "tunnel device stopped unexpectedly" marker nor a
// self-initiated shutdown. This is the "stays up, no restart loop" half of the D92
// invariant.
func assertEdgeAliveNoFatal(t *testing.T, p *proc) {
	t.Helper()
	time.Sleep(500 * time.Millisecond)
	if p.cmd.Process == nil {
		t.Fatalf("edge process handle went nil after bring-up\n%s", p.log())
	}
	if !procRunning(p.cmd.Process.Pid) {
		t.Fatalf("edge process exited after bring-up (nft-only fatal regression?)\n%s", p.log())
	}
	if strings.Contains(p.log(), "tunnel device stopped unexpectedly") {
		t.Fatalf("edge logged the runtime-fatal teardown after bring-up\n%s", p.log())
	}
	if strings.Contains(p.log(), "shutting down") {
		t.Fatalf("edge initiated shutdown unexpectedly after bring-up\n%s", p.log())
	}
}

// procRunning reports whether pid names a live (non-zombie) process. It reads
// /proc/<pid>/stat's state field rather than kill(pid, 0): an un-reaped self-exit stays a
// zombie whose pid still answers signal 0, so signal 0 cannot distinguish "still serving"
// from "exited but not yet reaped". The stat state field DOES ('Z' zombie / 'X' dead). The
// comm field can contain spaces and parens, so the state char is parsed as the character
// two positions after the LAST ')'.
func procRunning(pid int) bool {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	s := string(data)
	i := strings.LastIndex(s, ")")
	if i < 0 || i+2 >= len(s) {
		return false
	}
	state := s[i+2]
	return state != 'Z' && state != 'X'
}

// mirrorPathExcluding builds a temp bin dir populated with symlinks to every executable
// currently resolvable on the test process's PATH, EXCEPT those whose basename appears in
// exclude, and returns the dir (used verbatim as the daemon's single-entry PATH). It is
// the faithful emulation of a host on which the excluded binaries are simply not
// installed: the daemon's exec.LookPath sees everything the real host offers minus the
// excluded front-ends. First occurrence wins, preserving PATH precedence; unreadable dirs
// and non-executables are skipped.
func mirrorPathExcluding(t *testing.T, exclude ...string) string {
	t.Helper()
	binDir := t.TempDir()
	ex := make(map[string]bool, len(exclude))
	for _, e := range exclude {
		ex[e] = true
	}
	seen := map[string]bool{}
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" {
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := e.Name()
			if ex[name] || seen[name] {
				continue
			}
			src := filepath.Join(dir, name)
			// Stat (follows symlinks) so a symlinked tool resolves to a real executable file.
			info, serr := os.Stat(src)
			if serr != nil || info.IsDir() || info.Mode()&0o111 == 0 {
				continue
			}
			if err := os.Symlink(src, filepath.Join(binDir, name)); err != nil {
				continue
			}
			seen[name] = true
		}
	}
	return binDir
}
