//go:build e2e

package e2e

import (
	"crypto/rand"
	"encoding/base64"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/log"
	"github.com/7mind/wanbond/internal/telemetry"
)

// Environment markers wiring the reflector helper subprocess (see
// TestProbeReflectorHelper). The helper runs in the concentrator network
// namespace via nsenter and echoes authenticated probes back to the edge.
const (
	reflectAddrEnv = "WANBOND_E2E_REFLECT" // "<ip>:<port>" the reflector binds
	reflectPSKEnv  = "WANBOND_E2E_PROBE_PSK"
	reflectPort    = 52820
)

// TestLivenessBlackhole is the T13 e2e acceptance gate: over a real netns/netem
// path, an authenticated per-path probe exchange brings the path up; blackholing
// the edge link then marks the path down within the configured detection
// threshold, and the transition is logged with the per-path field. Path liveness
// is entirely ours: the inner WireGuard keepalive is per-peer, so this blackhole
// is invisible to WireGuard and must be caught here. Requires CAP_NET_ADMIN +
// /dev/net/tun; the plain `go test` never compiles it (e2e build tag).
func TestLivenessBlackhole(t *testing.T) {
	top := Setup(t)
	p := top.path("starlink")

	psk, pskB64 := randProbePSK(t)
	top.startReflector(t, p.concIP, reflectPort, pskB64)

	// The edge prober: real wall clock, buffer-backed logger so the transition is
	// inspectable, thresholds read from the shared table.
	logs := &lockedBuffer{}
	logger, err := log.New("debug", logs)
	if err != nil {
		t.Fatalf("build logger: %v", err)
	}
	sessionID, err := telemetry.NewSessionID(rand.Reader)
	if err != nil {
		t.Fatalf("new session id: %v", err)
	}
	prober := telemetry.NewProber("starlink", 1, sessionID, psk, telemetry.ProberConfig{
		Liveness: telemetry.LivenessConfig{
			DownAfter:        PLivenessDownAfter,
			UpAfterSuccesses: PLivenessUpSuccesses,
		},
	}, telemetry.SystemClock{}, logger)

	// UDP socket pinned to the edge end of the path, targeting the reflector.
	conn, err := net.DialUDP("udp",
		&net.UDPAddr{IP: net.ParseIP(p.edgeIP)},
		&net.UDPAddr{IP: net.ParseIP(p.concIP), Port: reflectPort})
	if err != nil {
		t.Fatalf("dial probe socket: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	buf := make([]byte, 64*1024)
	// pump performs one probe cycle: send, await an echo for up to one probe
	// interval, then advance the liveness state machine. Write/echo errors during a
	// blackhole are expected and simply yield no heartbeat.
	pump := func() {
		if raw, err := prober.SendProbe(); err == nil {
			_ = conn.SetWriteDeadline(time.Now().Add(PLivenessProbeInterval))
			_, _ = conn.Write(raw)
		}
		_ = conn.SetReadDeadline(time.Now().Add(PLivenessProbeInterval))
		if n, err := conn.Read(buf); err == nil {
			_ = prober.HandleEcho(buf[:n])
		}
		prober.Tick()
	}

	// Phase 1: bring the path up.
	upDeadline := time.Now().Add(10 * time.Second)
	for prober.State() != telemetry.StateUp {
		if time.Now().After(upDeadline) {
			t.Fatalf("path never came up; probe exchange failed:\n%s", logs.String())
		}
		pump()
	}

	// Phase 2: blackhole the edge link and time the down transition.
	top.Blackhole("starlink")
	blackholedAt := time.Now()
	downDeadline := blackholedAt.Add(PLivenessDetectBudget)
	for prober.State() != telemetry.StateDown {
		if time.Now().After(downDeadline) {
			t.Fatalf("blackholed path not marked down within %v (per-path liveness failed)\n%s",
				PLivenessDetectBudget, logs.String())
		}
		pump()
	}
	detectLatency := time.Since(blackholedAt)
	if detectLatency > PLivenessDetectBudget {
		t.Fatalf("down detection took %v, budget %v", detectLatency, PLivenessDetectBudget)
	}

	// The transition must be logged with the per-path field.
	out := logs.String()
	if !strings.Contains(out, `"`+log.FieldPath+`":"starlink"`) {
		t.Fatalf("down transition missing per-path field %q:\n%s", log.FieldPath, out)
	}
	if !strings.Contains(out, `"to":"down"`) {
		t.Fatalf("down transition not logged:\n%s", out)
	}
	t.Logf("blackhole detected in %v (budget %v)", detectLatency, PLivenessDetectBudget)
}

// TestProbeReflectorHelper is not a standalone test: it is the reflector process
// the edge test spawns (via nsenter) inside the concentrator network namespace.
// It binds the probe port, echoes every authenticated probe, and runs until the
// parent kills it. Without the reflectAddrEnv marker it is a no-op skip so the
// normal suite run does not block on it.
func TestProbeReflectorHelper(t *testing.T) {
	spec := os.Getenv(reflectAddrEnv)
	if spec == "" {
		t.Skip("not invoked as the probe reflector helper")
	}
	psk := mustProbePSK(t, os.Getenv(reflectPSKEnv))

	addr, err := net.ResolveUDPAddr("udp", spec)
	if err != nil {
		t.Fatalf("resolve reflector addr %q: %v", spec, err)
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		t.Fatalf("bind reflector %s: %v", spec, err)
	}
	defer func() { _ = conn.Close() }()

	refl := telemetry.NewReflector(psk, rand.Reader)
	buf := make([]byte, 64*1024)
	for {
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			return // socket closed on parent kill
		}
		echo, _, err := refl.Reflect(buf[:n])
		if err != nil {
			continue // drop forged / replayed / non-probe frames
		}
		_, _ = conn.WriteToUDP(echo, from)
	}
}

// startReflector launches the reflector helper subprocess in the concentrator
// network namespace and registers its termination.
func (top *Topology) startReflector(t *testing.T, ip string, port int, pskB64 string) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("locate test binary: %v", err)
	}
	cmd := exec.Command("nsenter", "-t", strconv.Itoa(top.pid), "-n",
		self, "-test.run", "^TestProbeReflectorHelper$")
	cmd.Env = append(os.Environ(),
		nsEnvMarker+"=1", // the child is already in a namespace; skip the re-exec
		reflectAddrEnv+"="+net.JoinHostPort(ip, strconv.Itoa(port)),
		reflectPSKEnv+"="+pskB64,
	)
	out := &lockedBuffer{}
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		t.Fatalf("start reflector: %v", err)
	}
	t.Cleanup(func() {
		if cmd.Process == nil {
			return
		}
		_ = cmd.Process.Signal(syscall.SIGTERM)
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	})
	// Give the reflector a moment to bind before the edge starts probing.
	time.Sleep(300 * time.Millisecond)
}

// randProbePSK returns a fresh 32-byte outer PSK as both a config.Key and its
// base64 form (to hand to the reflector subprocess).
func randProbePSK(t *testing.T) (config.Key, string) {
	t.Helper()
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("read random psk: %v", err)
	}
	b64 := base64.StdEncoding.EncodeToString(b[:])
	return mustProbePSK(t, b64), b64
}

// mustProbePSK parses a base64 PSK into a config.Key.
func mustProbePSK(t *testing.T, b64 string) config.Key {
	t.Helper()
	var k config.Key
	if err := k.UnmarshalText([]byte(b64)); err != nil {
		t.Fatalf("parse probe psk: %v", err)
	}
	return k
}
