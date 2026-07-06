//go:build realhosts

package realhosts

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Environment variables that select the real hosts and credentials. Each has a
// default (below) so the tier runs against the standing worker machines with no
// configuration.
const (
	envEdgeHost  = "WANBOND_EDGE_HOST"
	envConcHost  = "WANBOND_CONC_HOST"
	envConcPubIP = "WANBOND_CONC_PUBLIP"
	envSSHKey    = "WANBOND_SSH_KEY"
)

// Defaults for the standing two-machine testbed:
//   - the edge sits behind a symmetric NAT and is amd64;
//   - the concentrator is aarch64 and has a public, inbound-reachable IP.
const (
	defaultEdgeHost  = "llm-ubuntu-0.pgtr.7mind.io"
	defaultConcHost  = "o3.7mind.io"
	defaultConcPubIP = "89.168.124.91"
	defaultSSHKey    = "/run/agenix/llm-ssh-key"

	// defaultUser is the login shared by both worker machines.
	defaultUser = "ubuntu"

	// defaultConnectTimeout bounds the TCP+auth handshake of a single ssh
	// invocation. The runner passes it as `-o ConnectTimeout=<n>` (seconds).
	defaultConnectTimeout = 10 * time.Second
)

// Host is one real machine reachable over SSH.
type Host struct {
	// Role is a human label for the machine's function ("edge"/"concentrator").
	Role string
	// Addr is the SSH target hostname (or IP) passed to ssh.
	Addr string
	// User is the SSH login user.
	User string
}

// target renders the user@host argument ssh expects.
func (h Host) target() string { return h.User + "@" + h.Addr }

// Config is the resolved real-host topology plus SSH credentials, populated from
// the environment (with defaults) by LoadConfig.
type Config struct {
	Edge Host
	Conc Host
	// ConcPubIP is the concentrator's public, inbound-reachable IP. The edge
	// (behind symmetric NAT) dials this; recorded here for later tasks.
	ConcPubIP string
	// SSHKey is the private-key path handed to `ssh -i`.
	SSHKey string
	// ConnectTimeout bounds each ssh connect.
	ConnectTimeout time.Duration
}

// envOr returns the value of env var key, or def when it is unset or empty.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// LoadConfig resolves the real-host topology from the environment, applying the
// standing-testbed defaults for any unset variable. Both hosts use the default
// SSH login user.
func LoadConfig() Config {
	return Config{
		Edge:           Host{Role: "edge", Addr: envOr(envEdgeHost, defaultEdgeHost), User: defaultUser},
		Conc:           Host{Role: "concentrator", Addr: envOr(envConcHost, defaultConcHost), User: defaultUser},
		ConcPubIP:      envOr(envConcPubIP, defaultConcPubIP),
		SSHKey:         envOr(envSSHKey, defaultSSHKey),
		ConnectTimeout: defaultConnectTimeout,
	}
}

// Result captures the outcome of one remote command.
type Result struct {
	Host   Host
	Cmd    string
	Stdout string
	Stderr string
}

// Runner executes commands on real hosts over SSH. It is safe for sequential
// use; the SSH invocation itself carries no shared state.
type Runner struct {
	key            string
	connectTimeout time.Duration
}

// NewRunner builds a Runner from a Config's key and connect timeout.
func NewRunner(cfg Config) *Runner {
	return &Runner{key: cfg.SSHKey, connectTimeout: cfg.ConnectTimeout}
}

// sshArgs assembles the argv for a single ssh invocation against host running
// remoteCmd. The system ssh_config is known-broken on these workers, so `-F
// none` forces ssh to ignore it; the remaining options pin the identity file,
// auto-accept the host key on first contact, and bound the connect.
func (r *Runner) sshArgs(host Host, remoteCmd string) []string {
	secs := int(r.connectTimeout / time.Second)
	if secs < 1 {
		secs = 1
	}
	return []string{
		"-F", "none",
		"-i", r.key,
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "ConnectTimeout=" + strconv.Itoa(secs),
		host.target(),
		remoteCmd,
	}
}

// Run executes remoteCmd on host over SSH, capturing stdout and stderr
// separately. A non-nil error wraps the exit status and includes the captured
// stderr for diagnosis; ctx bounds the whole invocation.
func (r *Runner) Run(ctx context.Context, host Host, remoteCmd string) (Result, error) {
	cmd := exec.CommandContext(ctx, "ssh", r.sshArgs(host, remoteCmd)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	res := Result{Host: host, Cmd: remoteCmd}
	err := cmd.Run()
	res.Stdout, res.Stderr = strings.TrimRight(stdout.String(), "\n"), strings.TrimRight(stderr.String(), "\n")
	if err != nil {
		return res, fmt.Errorf("ssh %s: %q: %w\nstderr: %s", host.target(), remoteCmd, err, res.Stderr)
	}
	return res, nil
}
