//go:build linux

package device

import (
	"os/exec"
	"strings"
	"testing"
)

// stubLookPathNftOnly makes the lookPath seam report the iptables/ip6tables front-ends absent
// but `nft` present — an nft-only host (Raspberry Pi OS / modern Debian/Ubuntu), the exact class
// D92 bricked before T233's fallback. Restores the real exec.LookPath on cleanup.
func stubLookPathNftOnly(t *testing.T) {
	t.Helper()
	orig := lookPath
	lookPath = func(bin string) (string, error) {
		if bin == "nft" {
			return "/usr/sbin/nft", nil
		}
		return "", exec.ErrNotFound
	}
	t.Cleanup(func() { lookPath = orig })
}

// nftCall records one captured invocation of the runCmd/runCmdStdin seam.
type nftCall struct {
	name  string
	stdin string
	args  []string
}

// stubNftRunners replaces the runCmd/runCmdStdin exec seams with capturing no-ops (both report
// success), so the nft fallback can be exercised with no real nftables backend. Returns a
// pointer to the ordered capture slice.
func stubNftRunners(t *testing.T) *[]nftCall {
	t.Helper()
	origRun, origStdin := runCmd, runCmdStdin
	var calls []nftCall
	runCmd = func(name string, args ...string) ([]byte, error) {
		calls = append(calls, nftCall{name: name, args: args})
		return nil, nil
	}
	runCmdStdin = func(name, stdin string, args ...string) ([]byte, error) {
		calls = append(calls, nftCall{name: name, stdin: stdin, args: args})
		return nil, nil
	}
	t.Cleanup(func() { runCmd, runCmdStdin = origRun, origStdin })
	return &calls
}

// TestInstallMSSClampFallsBackToNft is the D92 FIX (T233): on an nft-only host installMSSClamp
// does NOT fail — it programs the clamp via `nft -f -`, and the rendered ruleset matches the
// iptables rule's semantics (inet table, output hook at mangle priority, SYN-only, clamp MSS to
// the route MTU).
func TestInstallMSSClampFallsBackToNft(t *testing.T) {
	stubLookPathNftOnly(t)
	calls := stubNftRunners(t)

	if err := installMSSClamp("wanbond0"); err != nil {
		t.Fatalf("installMSSClamp on an nft-only host = %v, want nil (nft fallback)", err)
	}

	var add *nftCall
	for i := range *calls {
		if c := &(*calls)[i]; c.stdin != "" {
			add = c
		}
	}
	if add == nil {
		t.Fatalf("no `nft -f -` add call captured; calls=%+v", *calls)
	}
	if len(add.args) != 2 || add.args[0] != "-f" || add.args[1] != "-" {
		t.Fatalf("nft add args = %v, want [-f -]", add.args)
	}
	for _, want := range []string{
		"table inet " + nftMSSClampTable,
		`oifname "wanbond0"`,
		"priority -150",
		"tcp flags & (syn|rst) == syn",
		"tcp option maxseg size set rt mtu",
	} {
		if !strings.Contains(add.stdin, want) {
			t.Fatalf("nft program missing %q; got:\n%s", want, add.stdin)
		}
	}
}

// TestNftInstallIsIdempotentDeleteThenAdd pins the idempotent install: a stale daemon-owned
// table (a pre-crash remnant) is deleted BEFORE the recreate, so re-running Up never stacks
// duplicate state and never errors on an already-present table.
func TestNftInstallIsIdempotentDeleteThenAdd(t *testing.T) {
	stubLookPathNftOnly(t)
	calls := stubNftRunners(t)

	if err := nftInstallMSSClamp("wanbond0"); err != nil {
		t.Fatalf("nftInstallMSSClamp = %v", err)
	}
	if len(*calls) < 2 {
		t.Fatalf("want >=2 nft calls (delete then add), got %d: %+v", len(*calls), *calls)
	}
	del := (*calls)[0]
	if del.stdin != "" || len(del.args) < 2 || del.args[0] != "delete" || del.args[1] != "table" {
		t.Fatalf("first nft call = %+v, want a `delete table` pre-clean", del)
	}
	if add := (*calls)[1]; add.stdin == "" {
		t.Fatalf("second nft call = %+v, want the `-f -` add", add)
	}
}

// TestRemoveMSSClampDeletesNftTable pins Close-side withdrawal of the nft backend: removeMSSClamp
// issues `nft delete table inet wanbond_mssclamp` so the daemon-owned table never leaks across a
// tun_persist restart (an nft table, like an `-o <ifname>` iptables rule, survives the interface).
func TestRemoveMSSClampDeletesNftTable(t *testing.T) {
	stubLookPathNftOnly(t)
	calls := stubNftRunners(t)

	if err := removeMSSClamp("wanbond0"); err != nil {
		t.Fatalf("removeMSSClamp = %v, want nil", err)
	}
	var sawDelete bool
	for _, c := range *calls {
		if c.name == "nft" && len(c.args) >= 4 &&
			c.args[0] == "delete" && c.args[1] == "table" && c.args[2] == "inet" && c.args[3] == nftMSSClampTable {
			sawDelete = true
		}
	}
	if !sawDelete {
		t.Fatalf("removeMSSClamp did not issue `nft delete table inet %s`; calls=%+v", nftMSSClampTable, *calls)
	}
}
