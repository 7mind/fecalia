//go:build e2e

package e2e

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"testing"
)

// nsEnvMarker guards the one-shot re-exec below.
const nsEnvMarker = "WANBOND_E2E_NS"

// TestMain re-execs the e2e test binary inside a FRESH network namespace so the
// fixtures never touch the host's networking. Under real root (the sudo target)
// it uses a plain mount+net namespace; unprivileged (the sandbox) it uses an
// unprivileged user+net namespace, which still grants CAP_NET_ADMIN for veth and
// netem. The PID-addressed peer namespace in netns.go needs no writable
// /run/netns, so this works in both environments.
func TestMain(m *testing.M) {
	if os.Getenv(nsEnvMarker) == "" {
		self, err := os.Executable()
		if err != nil {
			fmt.Fprintln(os.Stderr, "e2e: cannot find test binary:", err)
			os.Exit(1)
		}
		unshareArgs := []string{"-Urmn"}
		if os.Geteuid() == 0 {
			unshareArgs = []string{"-mn"}
		}
		args := append([]string{}, unshareArgs...)
		args = append(args, "--", self)
		args = append(args, os.Args[1:]...)

		cmd := exec.Command("unshare", args...)
		cmd.Env = append(os.Environ(), nsEnvMarker+"=1")
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		err = cmd.Run()
		if err == nil {
			os.Exit(0)
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			os.Exit(ee.ExitCode())
		}
		fmt.Fprintln(os.Stderr, "e2e: namespace re-exec failed:", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}
