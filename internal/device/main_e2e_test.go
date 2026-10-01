//go:build e2e

package device

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"testing"
)

// e2eNSEnvMarker guards TestMain's one-shot re-exec into a fresh network namespace.
const e2eNSEnvMarker = "WANBOND_DEVICE_E2E_NS"

// TestMain re-execs this test binary inside a FRESH network namespace, exactly as
// test/e2e/main_test.go does for the same reason: under real root (the o3 sudo
// target) a plain mount+net namespace; unprivileged, an unprivileged user+net
// namespace (still grants CAP_NET_ADMIN for veth/netem). Without this, running as
// root directly in the HOST's root netns would create the fixtures' links there and
// collide with a persistent root-netns concentrator (the documented o3 collision).
func TestMain(m *testing.M) {
	if os.Getenv(e2eNSEnvMarker) == "" {
		self, err := os.Executable()
		if err != nil {
			fmt.Fprintln(os.Stderr, "device e2e: cannot find test binary:", err)
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
		cmd.Env = append(os.Environ(), e2eNSEnvMarker+"=1")
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		err = cmd.Run()
		if err == nil {
			os.Exit(0)
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			os.Exit(ee.ExitCode())
		}
		fmt.Fprintln(os.Stderr, "device e2e: namespace re-exec failed:", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}
