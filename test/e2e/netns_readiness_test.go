//go:build e2e

package e2e

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

const (
	netnsHolderHelperPIDPathEnv = "WANBOND_E2E_HOLDER_HELPER_PID_PATH"
	setupReadinessProbeEnv      = "WANBOND_E2E_SETUP_READINESS_PROBE"
	setupReadinessResultEnv     = "WANBOND_E2E_SETUP_READINESS_RESULT"
)

// Regression: D126 — the holder's /proc namespace entry exists before unshare
// necessarily publishes the child's distinct network namespace.
func TestWaitForNetnsRequiresDistinctChildNamespaceIdentity(t *testing.T) {
	const (
		currentPath = "/proc/self/ns/net"
		childPath   = "/proc/42/ns/net"
		currentID   = "net:[100]"
		childID     = "net:[200]"
	)
	childReads := 0
	readIdentity := func(path string) (string, error) {
		switch path {
		case currentPath:
			return currentID, nil
		case childPath:
			childReads++
			if childReads == 1 {
				return currentID, nil
			}
			return childID, nil
		default:
			t.Fatalf("unexpected identity path %q", path)
			return "", nil
		}
	}

	got, err := waitForNetnsIdentity(readIdentity, childPath, 3, func() {})
	if err != nil {
		t.Fatalf("waitForNetnsIdentity: %v", err)
	}
	if got.current == got.child {
		t.Fatalf(
			"readiness returned shared namespace identity %q after %d child read; want wait for %q",
			got.child,
			childReads,
			childID,
		)
	}
	if got.child != childID || childReads != 2 {
		t.Fatalf(
			"readiness = %+v after %d child reads, want child %q after 2",
			got,
			childReads,
			childID,
		)
	}
}

func TestWaitForNetnsReportsPersistentSharedIdentity(t *testing.T) {
	const (
		childPath = "/proc/42/ns/net"
		currentID = "net:[100]"
	)
	readIdentity := func(string) (string, error) {
		return currentID, nil
	}

	got, err := waitForNetnsIdentity(readIdentity, childPath, 2, func() {})
	if err == nil {
		t.Fatalf("readiness accepted persistent shared identities: %+v", got)
	}
	for _, want := range []string{childPath, currentID, "after 2 attempts"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("readiness error %q missing %q", err, want)
		}
	}
}

func TestWaitForNetnsReportsMissingChild(t *testing.T) {
	const (
		currentPath = "/proc/self/ns/net"
		childPath   = "/proc/42/ns/net"
		currentID   = "net:[100]"
	)
	readIdentity := func(path string) (string, error) {
		if path == currentPath {
			return currentID, nil
		}
		return "", errors.New("holder exited")
	}

	_, err := waitForNetnsIdentity(readIdentity, childPath, 2, func() {})
	if err == nil {
		t.Fatal("readiness accepted a missing child namespace")
	}
	for _, want := range []string{childPath, currentID, "holder exited"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("readiness error %q missing %q", err, want)
		}
	}
}

// Regression: D128 — SetupWithPaths must register holder cleanup before
// readiness can fail. The subprocess drives the production SetupWithPaths
// lifecycle with the test binary standing in for unshare; the probe cleanup
// observes the holder before the subprocess exits, so "reaped" proves that
// Teardown both terminated and waited for it.
func TestSetupWithPathsReadinessFailureReapsHolder(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("find e2e test executable: %v", err)
	}
	dir := t.TempDir()
	fakeUnshare := filepath.Join(dir, "unshare")
	if err := os.Symlink(self, fakeUnshare); err != nil {
		t.Fatalf("create fake unshare: %v", err)
	}
	pidPath := filepath.Join(dir, "holder.pid")
	resultPath := filepath.Join(dir, "cleanup.result")

	cmd := exec.Command(self, "-test.run=^TestSetupWithPathsReadinessFailureProbe$", "-test.v")
	cmd.Env = append(
		os.Environ(),
		nsEnvMarker+"=1",
		setupReadinessProbeEnv+"=1",
		setupReadinessResultEnv+"="+resultPath,
		netnsHolderHelperPIDPathEnv+"="+pidPath,
		"PATH="+dir,
	)
	output, runErr := cmd.CombinedOutput()
	if runErr == nil {
		t.Fatalf("readiness-failure probe unexpectedly passed:\n%s", output)
	}
	result, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatalf("read readiness cleanup result: %v\nprobe output:\n%s", err, output)
	}
	if got := strings.TrimSpace(string(result)); got != "reaped" {
		t.Fatalf("holder state after readiness failure = %q, want %q\nprobe output:\n%s", got, "reaped", output)
	}
}

func TestSetupWithPathsReadinessFailureProbe(t *testing.T) {
	if os.Getenv(setupReadinessProbeEnv) == "" {
		t.Skip("subprocess probe")
	}
	pidPath := os.Getenv(netnsHolderHelperPIDPathEnv)
	resultPath := os.Getenv(setupReadinessResultEnv)
	t.Cleanup(func() {
		pidBytes, err := os.ReadFile(pidPath)
		if err != nil {
			_ = os.WriteFile(resultPath, []byte(fmt.Sprintf("read holder PID: %v", err)), 0o600)
			return
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
		if err != nil {
			_ = os.WriteFile(resultPath, []byte(fmt.Sprintf("parse holder PID: %v", err)), 0o600)
			return
		}
		result := "reaped"
		if err := syscall.Kill(pid, 0); err == nil {
			result = "alive"
			_ = syscall.Kill(pid, syscall.SIGKILL)
			var status syscall.WaitStatus
			_, _ = syscall.Wait4(pid, &status, 0, nil)
		} else if !errors.Is(err, syscall.ESRCH) {
			result = fmt.Sprintf("inspect holder PID %d: %v", pid, err)
		}
		if err := os.WriteFile(resultPath, []byte(result), 0o600); err != nil {
			t.Errorf("write readiness cleanup result: %v", err)
		}
	})

	SetupWithPaths(t, nil)
	t.Fatal("SetupWithPaths returned after a forced shared-namespace readiness failure")
}

// Regression: D129 — every auxiliary holder must be an explicit argument at
// its waitForNetns call site, so a readiness failure can report that holder's
// PID, argv, and state rather than "holder: <nil>".
func TestWaitForNetnsCallSitesSupplyHolderDiagnostics(t *testing.T) {
	files := []string{
		"hub_failover_test.go",
		"multipeer_test.go",
		"multipeer_hardened_test.go",
		"multi_concentrator_warm_standby_test.go",
	}
	for _, filename := range files {
		t.Run(filename, func(t *testing.T) {
			parsed, err := parser.ParseFile(token.NewFileSet(), filename, nil, 0)
			if err != nil {
				t.Fatalf("parse %s: %v", filename, err)
			}
			calls := 0
			ast.Inspect(parsed, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || selector.Sel.Name != "waitForNetns" {
					return true
				}
				calls++
				if len(call.Args) != 1 {
					t.Errorf("waitForNetns call has %d holder arguments, want 1", len(call.Args))
					return true
				}
				if ident, ok := call.Args[0].(*ast.Ident); ok && ident.Name == "nil" {
					t.Error("waitForNetns holder argument is nil")
				}
				return true
			})
			if calls != 1 {
				t.Errorf("found %d waitForNetns calls, want 1", calls)
			}
		})
	}
}

func TestHolderDiagnosticsIncludesPIDArgvAndState(t *testing.T) {
	holder := exec.Command("unshare", "-n", "sleep", "600")
	holder.Process = &os.Process{Pid: 2_000_000_000}
	top := &Topology{pid: holder.Process.Pid}

	got := top.holderDiagnostics(holder)
	for _, want := range []string{
		"pid=2000000000",
		`argv=["unshare" "-n" "sleep" "600"]`,
		"process_state=",
		"proc_state=",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("holder diagnostics %q missing %q", got, want)
		}
	}
}
