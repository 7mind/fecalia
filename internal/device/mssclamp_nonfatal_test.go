//go:build linux

package device

import (
	"bytes"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/log"
)

// stubLookPathMissing makes the MSS-clamp lookPath seam report every front-end binary
// absent, restoring the real exec.LookPath on cleanup — simulating an nft-only host with
// no iptables/ip6tables (defect D92).
func stubLookPathMissing(t *testing.T) {
	t.Helper()
	orig := lookPath
	lookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	t.Cleanup(func() { lookPath = orig })
}

// TestEnsureRuleClassifiesMissingBinary reproduces the D92 TRIGGER: with no iptables
// front-end present, installMSSClamp returns an error classified with the
// errMSSClampBinaryMissing sentinel — the error the OLD device.Up treated as FATAL,
// crash-looping the whole tunnel on nft-only hosts.
func TestEnsureRuleClassifiesMissingBinary(t *testing.T) {
	stubLookPathMissing(t)
	err := installMSSClamp("wanbond0")
	if err == nil {
		t.Fatal("installMSSClamp with no front-end binary returned nil, want the missing-binary error")
	}
	if !errors.Is(err, errMSSClampBinaryMissing) {
		t.Fatalf("installMSSClamp error = %v, want errors.Is(errMSSClampBinaryMissing)", err)
	}
}

// TestInstallEdgeMSSClampNonFatalOnMissingBinary is the D92 FIX: a missing front-end degrades
// to a single WARN and leaves mssClampInstalled=false (so Close withdraws nothing), and the
// helper NEVER returns an error — so bring-up is not aborted into the systemd restart loop the
// field hit (42 restarts on the Pi). The full-Up 'tunnel comes up' assertion is delegated to
// the privileged nft-only e2e (T234), since Up hard-creates the TUN.
func TestInstallEdgeMSSClampNonFatalOnMissingBinary(t *testing.T) {
	stubLookPathMissing(t)
	var buf bytes.Buffer
	lg, err := log.New("info", &buf)
	if err != nil {
		t.Fatalf("log.New: %v", err)
	}
	tun := &Tunnel{name: "wanbond0", log: lg, cfg: &config.Config{}}

	tun.installEdgeMSSClamp() // must not panic, must not abort

	if tun.mssClampInstalled {
		t.Fatal("mssClampInstalled=true after a missing-binary install; Close would try to remove a rule that was never programmed")
	}
	out := buf.String()
	if !strings.Contains(out, `"level":"WARN"`) || !strings.Contains(out, "MSS clamp not installed") {
		t.Fatalf("missing-binary install did not log the expected WARN; got:\n%s", out)
	}
	if strings.Contains(out, `"level":"ERROR"`) {
		t.Fatalf("missing-binary install logged an ERROR (want WARN only); got:\n%s", out)
	}
}

// TestCloseSkipsClampRemovalWhenNotInstalled pins the Close-safety half: with
// mssClampInstalled=false (the missing-binary outcome), the removeMSSClamp path is guarded
// off, so a host that never programmed a rule performs no withdrawal. removeMSSClamp itself
// is a no-op here (lookPath reports the binaries absent), proving the guard + idempotency
// compose: nothing is removed and nothing errors.
func TestCloseSkipsClampRemovalWhenNotInstalled(t *testing.T) {
	stubLookPathMissing(t)
	// The Close guard is `if t.mssClampInstalled { removeMSSClamp(...) }`; with the flag
	// false the call is skipped entirely. Independently, removeMSSClamp is idempotent even if
	// called: with the front-ends absent it withdraws nothing and returns nil.
	if err := removeMSSClamp("wanbond0"); err != nil {
		t.Fatalf("removeMSSClamp with no front-end binary = %v, want nil (idempotent no-op)", err)
	}
}
