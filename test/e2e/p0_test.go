//go:build e2e

package e2e

import (
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"
)

// P0 overlay addressing: the inner /24 carried by the WireGuard TUN on both ends.
const (
	edgeInner  = "10.10.0.1"
	concInner  = "10.10.0.2"
	tunDev     = "wanbond0"
	listenPort = 51820
)

// TestP0PassThrough is the P0 acceptance gate: bring the wanbond tunnel up over a
// single path (no bonding) with the pass-through Bind, complete the WireGuard
// handshake, and verify both ICMP (ping) and a TCP bulk transfer (iperf3) flow
// through the tunnel between the edge and concentrator network namespaces.
func TestP0PassThrough(t *testing.T) {
	top := Setup(t)
	p := top.path("starlink")
	bin := buildWanbond(t)

	edgePriv, edgePub := genKey(t)
	concPriv, concPub := genKey(t)
	psk := randKey(t) // outer-control PSK: required by config validation, unused by P0 pass-through

	dir := t.TempDir()
	edgeCfg := writeConfig(t, filepath.Join(dir, "edge.toml"), fmt.Sprintf(`role = "edge"
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
level = "error"
`, psk, p.edgeIP, edgePriv, concPub, p.concIP, listenPort, concInner))

	concCfg := writeConfig(t, filepath.Join(dir, "conc.toml"), fmt.Sprintf(`role = "concentrator"
psk = "%s"

[[paths]]
name = "starlink"
source_addr = "%s"

[wireguard]
private_key = "%s"
listen_port = %d

[[wireguard.peers]]
public_key = "%s"
allowed_ips = ["%s/32"]

[log]
level = "error"
`, psk, p.concIP, concPriv, listenPort, edgePub, edgeInner))

	// Concentrator first (it must be listening before the edge initiates), then edge.
	conc := top.startProc(t, "concentrator", "nsenter", "-t", strconv.Itoa(top.pid), "-n", bin, "--config", concCfg)
	edge := top.startProc(t, "edge", bin, "--config", edgeCfg)

	// Wait for each side's TUN, then address it (wanbond owns the engine only;
	// addressing is the operator's job — here, the test's).
	if !top.waitLink(tunDev, false, 5*time.Second) {
		t.Fatalf("edge %s never appeared\n%s", tunDev, edge.log())
	}
	if !top.waitLink(tunDev, true, 5*time.Second) {
		t.Fatalf("concentrator %s never appeared\n%s", tunDev, conc.log())
	}
	top.run("ip", "addr", "add", edgeInner+"/24", "dev", tunDev)
	top.run("ip", "link", "set", tunDev, "up")
	top.nsenter("ip", "addr", "add", concInner+"/24", "dev", tunDev)
	top.nsenter("ip", "link", "set", tunDev, "up")

	// The first ping triggers the handshake; retry until the session is established.
	if !top.pingUntil(concInner, 15*time.Second) {
		t.Fatalf("tunnel never came up: %s unreachable through the tunnel\n--- edge ---\n%s\n--- concentrator ---\n%s",
			concInner, edge.log(), conc.log())
	}

	mbps := top.iperf3Mbps(t, concInner, 3)
	if mbps <= 0 {
		t.Fatalf("iperf3 through the tunnel measured non-positive throughput %.2f Mbit/s", mbps)
	}
	t.Logf("P0 tunnel up: handshake + ping OK, iperf3 = %.1f Mbit/s", mbps)
}

// buildWanbond compiles the daemon (plain build, no e2e tag) into a temp path and
// returns it. The build uses the module-qualified package path so it resolves
// regardless of the test's working directory.
func buildWanbond(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "wanbond")
	out, err := exec.Command("go", "build", "-o", bin, "github.com/7mind/wanbond/cmd/wanbond").CombinedOutput()
	if err != nil {
		t.Fatalf("build wanbond: %v\n%s", err, out)
	}
	return bin
}

// genKey generates an X25519 keypair (the WireGuard key format) using the
// standard library, returning both keys base64-encoded as the TOML config
// carries them.
func genKey(t *testing.T) (privB64, pubB64 string) {
	t.Helper()
	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate X25519 key: %v", err)
	}
	return base64.StdEncoding.EncodeToString(priv.Bytes()),
		base64.StdEncoding.EncodeToString(priv.PublicKey().Bytes())
}

// randKey returns 32 random bytes base64-encoded (a placeholder PSK).
func randKey(t *testing.T) string {
	t.Helper()
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("read random: %v", err)
	}
	return base64.StdEncoding.EncodeToString(b[:])
}

// writeConfig writes body to path with the exact 0600 mode config.Load requires.
func writeConfig(t *testing.T, path, body string) string {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config %s: %v", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil { // defeat umask widening
		t.Fatalf("chmod config %s: %v", path, err)
	}
	return path
}

// lockedBuffer is a bytes.Buffer safe for concurrent Write (by exec.Cmd's output
// copier goroutine) and String (by the test's diagnostic log() calls).
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// proc is a background process with captured combined output.
type proc struct {
	cmd    *exec.Cmd
	name   string
	output *lockedBuffer
}

func (p *proc) log() string { return p.output.String() }

// startProc launches argv in the background, capturing its combined output and
// registering a cleanup that terminates it (SIGTERM, then SIGKILL after a grace
// period). A concentrator process is launched via nsenter so it runs — and
// creates its TUN — inside the peer network namespace.
func (top *Topology) startProc(t *testing.T, name string, argv ...string) *proc {
	t.Helper()
	cmd := exec.Command(argv[0], argv[1:]...)
	out := &lockedBuffer{}
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s (%v): %v", name, argv, err)
	}
	p := &proc{cmd: cmd, name: name, output: out}
	t.Cleanup(func() {
		if cmd.Process == nil {
			return
		}
		_ = cmd.Process.Signal(syscall.SIGTERM)
		done := make(chan struct{})
		// cmd.Wait (not Process.Wait) reaps the child AND waits for the output
		// copier goroutines, so no write races the final log() read.
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	})
	return p
}

// waitLink polls until dev exists (in the peer netns when ns is true), up to d.
func (top *Topology) waitLink(dev string, ns bool, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		var err error
		if ns {
			err = top.tryRun("nsenter", "-t", strconv.Itoa(top.pid), "-n", "ip", "link", "show", dev)
		} else {
			err = top.tryRun("ip", "link", "show", dev)
		}
		if err == nil {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// pingUntil pings ip from the edge netns until it answers or d elapses. The first
// successful reply implies the WireGuard session is established.
func (top *Topology) pingUntil(ip string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if top.tryRun("ping", "-c", "1", "-W", "1", ip) == nil {
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}

// iperf3Mbps runs a one-shot iperf3 TCP transfer to serverIP (inside the peer
// netns) for secs seconds and returns the sender throughput in Mbit/s.
func (top *Topology) iperf3Mbps(t *testing.T, serverIP string, secs int) float64 {
	t.Helper()
	top.startProc(t, "iperf3-server", "nsenter", "-t", strconv.Itoa(top.pid), "-n", "iperf3", "-s", "-1", "-B", serverIP)
	time.Sleep(500 * time.Millisecond) // allow the server to bind and listen

	out := top.runOut("iperf3", "-c", serverIP, "-t", strconv.Itoa(secs), "-J")
	var r struct {
		End struct {
			SumSent struct {
				BitsPerSecond float64 `json:"bits_per_second"`
			} `json:"sum_sent"`
		} `json:"end"`
	}
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("parse iperf3 json: %v\n%s", err, out)
	}
	return r.End.SumSent.BitsPerSecond / 1e6
}
