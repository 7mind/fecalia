//go:build realhosts

package realhosts

import (
	"context"
	"fmt"
	"strings"
)

// Provisioning targets. Go is pinned to the module's toolchain family; the
// family check (go1.26) tolerates patch drift while still installing the exact
// pinned patch when a fresh install is required.
const (
	goVersion       = "1.26.4"
	goVersionFamily = "go1.26"
	goInstallDir    = "/usr/local/go"
	goBin           = goInstallDir + "/bin/go"

	// tunnelIface is the TUN device name the wanbond daemon creates
	// (internal/device.defaultTUNName). The concentrator firewall step ACCEPTs
	// INPUT traffic arriving on it.
	tunnelIface = "wanbond0"
)

// Sentinels a remote predicate echoes so a genuine SSH-transport failure (which
// makes Runner.Run return an error) is distinguishable from a predicate that
// simply evaluated false (which prints sentinelMissing and exits 0).
const (
	sentinelPresent = "WANBOND_PRESENT"
	sentinelMissing = "WANBOND_MISSING"
)

// StepStatus is the outcome of one provisioning step.
type StepStatus string

const (
	// StatusAlreadyPresent means the step's precondition already held; nothing
	// was mutated.
	StatusAlreadyPresent StepStatus = "already-present"
	// StatusChanged means the step performed an install/insert to satisfy its
	// precondition.
	StatusChanged StepStatus = "changed"
)

// StepReport records what one provisioning step did.
type StepReport struct {
	Name   string
	Status StepStatus
}

// ProvisionReport aggregates every step's outcome for one host. A report with no
// Changed steps means the host was already fully provisioned (a re-run
// signalling idempotency).
type ProvisionReport struct {
	Host  Host
	Arch  string
	Steps []StepReport
}

// Changed reports whether any step mutated the host.
func (r ProvisionReport) Changed() bool {
	for _, s := range r.Steps {
		if s.Status == StatusChanged {
			return true
		}
	}
	return false
}

// String renders the per-step outcome for test logs.
func (r ProvisionReport) String() string {
	parts := make([]string, 0, len(r.Steps))
	for _, s := range r.Steps {
		parts = append(parts, fmt.Sprintf("%s=%s", s.Name, s.Status))
	}
	return fmt.Sprintf("%s (arch=%s): [%s]", r.Host.Role, r.Arch, strings.Join(parts, " "))
}

// ProvisionOpts selects host-specific provisioning behaviour.
type ProvisionOpts struct {
	// TunnelIface, when non-empty, ensures an iptables INPUT ACCEPT rule for the
	// named tunnel interface (concentrator only). Empty skips the firewall step
	// (the edge needs no such rule).
	TunnelIface string
}

// step is a single idempotent provisioning action: predicate is a shell
// expression that exits 0 iff the desired state already holds; install is run
// only when predicate is false, and predicate must hold afterwards.
type step struct {
	name      string
	predicate string
	install   string
}

// checkCmd wraps a predicate so the remote command always exits 0 and prints a
// sentinel, letting runStep tell "predicate false" apart from "SSH failed".
func checkCmd(predicate string) string {
	return fmt.Sprintf("if %s; then echo %s; else echo %s; fi", predicate, sentinelPresent, sentinelMissing)
}

// aptInstall is the install command for an apt package: refresh the index (an
// idempotent no-op on an up-to-date host) then install non-interactively.
func aptInstall(pkg string) string {
	return "sudo apt-get update -qq && sudo DEBIAN_FRONTEND=noninteractive apt-get install -y " + pkg
}

// goInstallCmd downloads the pinned Go tarball for goArch and unpacks it into
// /usr/local, replacing any prior install atomically enough for this tier.
func goInstallCmd(goArch string) string {
	tarball := fmt.Sprintf("go%s.linux-%s.tar.gz", goVersion, goArch)
	return strings.Join([]string{
		fmt.Sprintf("curl -fsSL https://go.dev/dl/%s -o /tmp/%s", tarball, tarball),
		fmt.Sprintf("sudo rm -rf %s", goInstallDir),
		fmt.Sprintf("sudo tar -C /usr/local -xzf /tmp/%s", tarball),
		fmt.Sprintf("rm -f /tmp/%s", tarball),
	}, " && ")
}

// detectGoArch maps `uname -m` to the GOARCH token Go's download naming uses.
func detectGoArch(ctx context.Context, r *Runner, host Host) (string, error) {
	res, err := r.Run(ctx, host, "uname -m")
	if err != nil {
		return "", err
	}
	switch m := strings.TrimSpace(res.Stdout); m {
	case "x86_64":
		return "amd64", nil
	case "aarch64", "arm64":
		return "arm64", nil
	default:
		return "", fmt.Errorf("%s (%s): unsupported machine arch %q", host.Role, host.target(), m)
	}
}

// runStep evaluates a step's predicate, installs only when it is unmet, and
// verifies the predicate holds afterwards. A false predicate yields
// StatusAlreadyPresent with no mutation; an install yields StatusChanged.
func runStep(ctx context.Context, r *Runner, host Host, s step) (StepReport, error) {
	present, err := r.Run(ctx, host, checkCmd(s.predicate))
	if err != nil {
		return StepReport{}, fmt.Errorf("%s: precondition check failed on %s: %w", s.name, host.target(), err)
	}
	if strings.Contains(present.Stdout, sentinelPresent) {
		return StepReport{Name: s.name, Status: StatusAlreadyPresent}, nil
	}

	if _, err := r.Run(ctx, host, s.install); err != nil {
		return StepReport{}, fmt.Errorf("%s: install failed on %s: %w", s.name, host.target(), err)
	}

	verify, err := r.Run(ctx, host, checkCmd(s.predicate))
	if err != nil {
		return StepReport{}, fmt.Errorf("%s: post-install check failed on %s: %w", s.name, host.target(), err)
	}
	if !strings.Contains(verify.Stdout, sentinelPresent) {
		return StepReport{}, fmt.Errorf("%s: precondition still unmet after install on %s", s.name, host.target())
	}
	return StepReport{Name: s.name, Status: StatusChanged}, nil
}

// Provision brings host to the wanbond real-host baseline idempotently: iperf3 +
// gcc installed (apt) and Go goVersionFamily at /usr/local/go, plus — when
// opts.TunnelIface is set (concentrator) — an INPUT ACCEPT rule for that tunnel
// interface, inserted at the head of the chain so it precedes OCI's default
// REJECT. Every step checks current state before mutating, so a second call on
// an already-provisioned host returns a report with Changed()==false.
func Provision(ctx context.Context, r *Runner, host Host, opts ProvisionOpts) (ProvisionReport, error) {
	report := ProvisionReport{Host: host}

	goArch, err := detectGoArch(ctx, r, host)
	if err != nil {
		return report, err
	}
	report.Arch = goArch

	steps := []step{
		{name: "iperf3", predicate: "command -v iperf3 >/dev/null 2>&1", install: aptInstall("iperf3")},
		{name: "gcc", predicate: "command -v gcc >/dev/null 2>&1", install: aptInstall("gcc")},
		{
			name:      "go",
			predicate: fmt.Sprintf("test -x %s && %s version | grep -q %s", goBin, goBin, goVersionFamily),
			install:   goInstallCmd(goArch),
		},
	}
	if opts.TunnelIface != "" {
		iface := opts.TunnelIface
		steps = append(steps, step{
			name: "tunnel-firewall",
			// -C exits 0 iff the exact rule already exists.
			predicate: fmt.Sprintf("sudo iptables -C INPUT -i %s -j ACCEPT 2>/dev/null", iface),
			// -I inserts at position 1 (chain head), guaranteeing the ACCEPT
			// precedes OCI's appended (-A) REJECT rule.
			install: fmt.Sprintf("sudo iptables -I INPUT -i %s -j ACCEPT", iface),
		})
	}

	for _, s := range steps {
		sr, err := runStep(ctx, r, host, s)
		if err != nil {
			return report, err
		}
		report.Steps = append(report.Steps, sr)
	}
	return report, nil
}

// TunnelFirewallState is the concentrator INPUT-chain state relevant to the
// tunnel ACCEPT rule, as parsed from `iptables -S INPUT`.
type TunnelFirewallState struct {
	// AcceptCount is the number of `-i <iface> -j ACCEPT` rules in INPUT.
	AcceptCount int
	// BeforeReject is true iff the first such ACCEPT rule precedes the first
	// `-j REJECT` rule in the chain.
	BeforeReject bool
	// Rules is the raw `iptables -S INPUT` output for diagnostics.
	Rules []string
}

// InspectTunnelFirewall reads host's INPUT chain and reports how many tunnel
// ACCEPT rules exist and whether the first precedes the REJECT rule.
func InspectTunnelFirewall(ctx context.Context, r *Runner, host Host, iface string) (TunnelFirewallState, error) {
	res, err := r.Run(ctx, host, "sudo iptables -S INPUT")
	if err != nil {
		return TunnelFirewallState{}, fmt.Errorf("%s (%s): iptables -S INPUT failed: %w", host.Role, host.target(), err)
	}

	lines := strings.Split(res.Stdout, "\n")
	state := TunnelFirewallState{Rules: lines}
	firstAccept, firstReject := -1, -1
	acceptNeedle := fmt.Sprintf("-i %s ", iface)
	for i, line := range lines {
		l := strings.TrimSpace(line)
		if strings.Contains(l, acceptNeedle) && strings.HasSuffix(l, "-j ACCEPT") {
			state.AcceptCount++
			if firstAccept == -1 {
				firstAccept = i
			}
		}
		if strings.Contains(l, "-j REJECT") && firstReject == -1 {
			firstReject = i
		}
	}
	state.BeforeReject = firstAccept != -1 && firstReject != -1 && firstAccept < firstReject
	return state, nil
}
