//go:build realhosts

package realhosts

import (
	"context"
	"strings"
	"testing"
	"time"
)

// provisionTimeout budgets one Provision call. Generous: a fresh host downloads
// and unpacks the ~100 MB Go tarball and pulls apt packages. On an
// already-provisioned host every step short-circuits on its check and the call
// completes in a few SSH round-trips.
const provisionTimeout = 10 * time.Minute

// versionProbes are the tool-version commands the acceptance requires to succeed
// over SSH on both hosts. Go is probed at its install path (/usr/local/go/bin);
// PATH export is out of this task's scope.
var versionProbes = []string{
	goBin + " version",
	"iperf3 --version",
	"gcc --version",
}

// TestRealProvision is the report-only provisioning entry point: it provisions
// both real hosts, asserts each tool-version command succeeds over SSH, runs
// provisioning a SECOND time and asserts the report shows no changes
// (idempotency), and checks the concentrator carries exactly one tunnel ACCEPT
// rule ordered before the OCI REJECT AND persisted to the boot rules file so it
// survives a reboot (defect D7). Executing and recording IS the acceptance (per
// Q12); it gates nothing.
func TestRealProvision(t *testing.T) {
	cfg := LoadConfig()
	r := NewRunner(cfg)

	t.Logf("realhosts config: edge=%s concentrator=%s ssh-key=%s",
		cfg.Edge.target(), cfg.Conc.target(), cfg.SSHKey)

	targets := []struct {
		host Host
		opts ProvisionOpts
	}{
		{cfg.Edge, ProvisionOpts{}},
		{cfg.Conc, ProvisionOpts{TunnelIface: tunnelIface}},
	}

	// First provisioning pass on both hosts.
	for _, tg := range targets {
		ctx, cancel := context.WithTimeout(context.Background(), provisionTimeout)
		rep, err := Provision(ctx, r, tg.host, tg.opts)
		cancel()
		if err != nil {
			t.Fatalf("%s: first provisioning pass failed: %v", tg.host.Role, err)
		}
		t.Logf("first pass %s", rep)
	}

	// Tool versions must succeed over SSH on both hosts.
	for _, tg := range targets {
		for _, probe := range versionProbes {
			ctx, cancel := context.WithTimeout(context.Background(), connectivityTimeout)
			res, err := r.Run(ctx, tg.host, probe)
			cancel()
			if err != nil {
				t.Errorf("%s (%s): version probe %q failed: %v", tg.host.Role, tg.host.target(), probe, err)
				continue
			}
			first := res.Stdout
			if i := strings.IndexByte(first, '\n'); i >= 0 {
				first = first[:i]
			}
			t.Logf("%s: %q -> %s", tg.host.Role, probe, first)
		}
	}

	// Second pass must be a no-op on every host (idempotency).
	for _, tg := range targets {
		ctx, cancel := context.WithTimeout(context.Background(), provisionTimeout)
		rep, err := Provision(ctx, r, tg.host, tg.opts)
		cancel()
		if err != nil {
			t.Fatalf("%s: second provisioning pass failed: %v", tg.host.Role, err)
		}
		t.Logf("second pass %s", rep)
		if rep.Changed() {
			t.Errorf("%s: second provisioning pass was not idempotent (reported changes): %s", tg.host.Role, rep)
		}
	}

	// Concentrator firewall: exactly one tunnel ACCEPT rule, before the REJECT.
	ctx, cancel := context.WithTimeout(context.Background(), connectivityTimeout)
	state, err := InspectTunnelFirewall(ctx, r, cfg.Conc, tunnelIface)
	cancel()
	if err != nil {
		t.Fatalf("concentrator: firewall inspection failed: %v", err)
	}
	t.Logf("concentrator INPUT chain:\n%s", strings.Join(state.Rules, "\n"))
	if state.AcceptCount != 1 {
		t.Errorf("concentrator: expected exactly one %s ACCEPT rule, found %d", tunnelIface, state.AcceptCount)
	}
	if !state.BeforeReject {
		t.Errorf("concentrator: %s ACCEPT rule is not ordered before the REJECT rule", tunnelIface)
	}
	if !state.Persisted {
		t.Errorf("concentrator: %s ACCEPT rule is not persisted to %s — it will not survive a reboot (D7)", tunnelIface, persistedRulesFile)
	}
}
