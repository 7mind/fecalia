//go:build realhosts

package realhosts

import (
	"context"
	"testing"
	"time"
)

// connectivityTimeout is the shared budget for one host's connectivity check
// (both `uname` round-trips run under the same context). Generous relative to
// ConnectTimeout to tolerate a cold SSH session.
const connectivityTimeout = 30 * time.Second

// TestRealConnectivity is the report-only entry point of the real-host tier: it
// runs a trivial command (`uname -a`) on BOTH the edge and the concentrator via
// the SSH runner and records each host's kernel/arch line. It asserts only that
// the command succeeds — executing and recording IS the acceptance (per Q12);
// it gates nothing.
func TestRealConnectivity(t *testing.T) {
	cfg := LoadConfig()
	runner := NewRunner(cfg)

	t.Logf("realhosts config: edge=%s concentrator=%s conc-public-ip=%s ssh-key=%s",
		cfg.Edge.target(), cfg.Conc.target(), cfg.ConcPubIP, cfg.SSHKey)

	for _, host := range []Host{cfg.Edge, cfg.Conc} {
		host := host
		t.Run(host.Role, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), connectivityTimeout)
			defer cancel()

			res, err := runner.Run(ctx, host, "uname -a")
			if err != nil {
				t.Fatalf("%s (%s): connectivity check failed: %v", host.Role, host.target(), err)
			}
			t.Logf("%s (%s) uname: %s", host.Role, host.target(), res.Stdout)

			arch, err := runner.Run(ctx, host, "uname -m")
			if err != nil {
				t.Fatalf("%s (%s): arch probe failed: %v", host.Role, host.target(), err)
			}
			t.Logf("%s (%s) arch: %s", host.Role, host.target(), arch.Stdout)
		})
	}
}
