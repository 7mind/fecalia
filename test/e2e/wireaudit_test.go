//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/wireaudit"
)

// Requirement-6 (DPI-resistance) automated wire-format audit.
//
// TestWireFormatAudit captures the outer tunnel UDP payloads (the wanbond wire
// frames) across >= wireaudit.MinSessions FRESH tunnel sessions — each with amnezia
// junk AND fixed-ratio FEC parity ACTIVE, so the capture spans the full traffic mix
// (DATA / PARITY / PROBE / CONTROL + amnezia junk) — and then asserts the
// requirement-6 obfuscation properties PROGRAMMATICALLY via internal/wireaudit:
//
//  1. no byte offset holds a constant value across sessions/packets (a DPI
//     fingerprint), and
//  2. the mean per-packet payload entropy of the large frames exceeds
//     wireaudit.MeanEntropyThreshold bits/byte.
//
// The analysis is a pure, non-privileged helper (internal/wireaudit) with its own
// unit tests (planted-constant, entropy-teeth, variable-length, pcap-parse); THIS
// test is only the privileged capture+drive wrapper. It also re-proves the
// constant-byte detector's TEETH on the REAL captured wire: it plants a constant
// byte at a known offset into a copy of the captured frames and asserts the audit
// then FAILS and pinpoints that offset.
//
// Fresh sessions: each session runs in its own t.Run subtest, which stands up a NEW
// topology and NEW daemon pair (fresh X25519 keys, fresh PSK => fresh HKDF
// obfuscation key => fresh per-frame nonces and keystream) and tears it down before
// the next — the fixed veth names forbid two live topologies, so sessions are
// strictly sequential. A byte constant WITHIN one session but varying ACROSS
// sessions is not a fingerprint; the detector requires cross-session constancy.
const (
	// auditFECData / auditFECParity size the FEC group for the audit. Small K with a
	// short deadline so parity frames actually appear at the fixture's low frame rate
	// (the audit needs PARITY on the wire, not a specific recovery ratio).
	auditFECData   = 4
	auditFECParity = 2
	// auditFECDeadlineNanos closes FEC groups after 50ms so parity flushes promptly
	// even when a group never fills to K at the fixture's low packet rate.
	auditFECDeadlineNanos = 50 * 1000 * 1000

	// auditLoadSecs is the per-session bulk-transfer duration: long enough to emit
	// thousands of MTU-sized DATA frames (so the large-frame entropy sample and the
	// per-offset sample counts clear their floors) yet short enough for five
	// sessions to run in reasonable wall time.
	auditLoadSecs = 5

	// auditCaptureAttachDelay lets tcpdump open its capture socket and savefile
	// before any tunnel traffic flows, so the handshake + junk are captured too.
	auditCaptureAttachDelay = 800 * time.Millisecond
	// auditCaptureFlushDelay lets trailing packets reach the (packet-buffered)
	// savefile before tcpdump is signalled.
	auditCaptureFlushDelay = 400 * time.Millisecond

	// auditPlantOffset is the byte offset at which the real-wire teeth check plants a
	// constant. It sits inside the 24-byte nonce region, present in EVERY frame, so
	// it is maximally sampled and unambiguously flagged.
	auditPlantOffset = 10
	auditPlantValue  = 0x5A
)

// auditPath is the single emulated uplink for the wire-format audit: a low fixed
// delay, no jitter, no loss — the audit inspects the OBFUSCATION of the wire, not
// its loss behaviour, so a clean path maximises the frame rate and sample size. It
// reuses DefaultPaths' veth names/IPs; safe because the test owns its topology and
// tears it down between sessions.
var auditPath = pathSpec{name: "wan", edgeIP: "10.100.1.1", concIP: "10.100.1.2", edgeVeth: "wbAe", concVeth: "wbAc", delayMs: 10}

func TestWireFormatAudit(t *testing.T) {
	bin := buildWanbond(t)

	var (
		mu       sync.Mutex
		sessions [][]wireaudit.Frame
	)

	for s := 0; s < wireaudit.MinSessions; s++ {
		s := s
		t.Run(fmt.Sprintf("session-%d", s), func(t *testing.T) {
			frames := captureAuditSession(t, bin, s)
			if len(frames) == 0 {
				t.Fatalf("session %d captured zero wanbond frames", s)
			}
			t.Logf("session %d: captured %d frames", s, len(frames))
			mu.Lock()
			sessions = append(sessions, frames)
			mu.Unlock()
		})
	}

	// Guard: every session must have contributed frames.
	if len(sessions) != wireaudit.MinSessions {
		t.Fatalf("collected %d sessions, want %d — a session subtest failed to capture", len(sessions), wireaudit.MinSessions)
	}

	rep := wireaudit.Audit(sessions)
	t.Logf("audit over %d sessions / %d frames (max frame %d bytes): mean entropy %.4f over %d large frames",
		rep.Sessions, rep.TotalFrames, rep.MaxFrameLen, rep.MeanEntropy, rep.EntropyFrameCount)

	// Requirement-6 assertions. A failure here that is NOT the planted teeth below is
	// a GENUINE obfuscation defect (a real constant offset or low entropy on the
	// wire) — it must be reported and fixed at the codec, NOT masked by retuning.
	if ok, msg := rep.SessionsOK(); !ok {
		t.Fatalf("wire audit: %s", msg)
	}
	if ok, msg := rep.ConstantByteOK(); !ok {
		t.Fatalf("wire audit: REQUIREMENT-6 DEFECT — %s", msg)
	} else {
		t.Log(msg)
	}
	if ok, msg := rep.EntropyOK(); !ok {
		t.Fatalf("wire audit: REQUIREMENT-6 DEFECT — %s", msg)
	} else {
		t.Log(msg)
	}

	// Teeth on the REAL captured wire: plant a constant byte at auditPlantOffset into
	// a copy of every captured frame and confirm the audit now FAILS and pinpoints
	// that offset. Proves the constant-byte detector is non-vacuous against real data.
	plantAndAssertDetected(t, sessions)
}

// plantAndAssertDetected deep-copies the captured sessions, forces a constant byte
// at auditPlantOffset across every frame, and asserts the audit then reports that
// exact offset as constant — the non-vacuity (teeth) proof on real captured frames.
func plantAndAssertDetected(t *testing.T, sessions [][]wireaudit.Frame) {
	t.Helper()
	planted := make([][]wireaudit.Frame, len(sessions))
	for i, sess := range sessions {
		cp := make([]wireaudit.Frame, len(sess))
		for j, f := range sess {
			nf := append([]byte(nil), f...)
			if len(nf) > auditPlantOffset {
				nf[auditPlantOffset] = auditPlantValue
			}
			cp[j] = nf
		}
		planted[i] = cp
	}

	rep := wireaudit.Audit(planted)
	ok, msg := rep.ConstantByteOK()
	if ok {
		t.Fatalf("teeth: planted constant at offset %d NOT detected — the audit is vacuous", auditPlantOffset)
	}
	found := false
	for _, c := range rep.ConstantOffsets {
		if c.Offset == auditPlantOffset {
			found = true
			if c.Value != auditPlantValue {
				t.Errorf("teeth: offset %d reported value 0x%02x, want 0x%02x", auditPlantOffset, c.Value, auditPlantValue)
			}
		}
	}
	if !found {
		t.Fatalf("teeth: audit failed but did not pinpoint the planted offset %d; report: %s", auditPlantOffset, msg)
	}
	t.Logf("teeth OK: planted constant detected — %s", msg)
}

// captureAuditSession brings up one fresh amnezia+FEC tunnel session over auditPath,
// captures the outer UDP payloads on the edge veth with tcpdump while a short bulk
// transfer drives DATA/PARITY/PROBE/junk traffic, and returns the parsed wanbond
// frames.
func captureAuditSession(t *testing.T, bin string, session int) []wireaudit.Frame {
	t.Helper()
	top := SetupWithPaths(t, []pathSpec{auditPath})

	pcapFile := filepath.Join(t.TempDir(), fmt.Sprintf("session-%d.pcap", session))
	cap := top.startPcap(t, auditPath.edgeVeth, listenPort, pcapFile)

	edge, conc := setupAuditTunnel(t, top, bin)
	if !top.pingUntil(concInner, 15*time.Second) {
		cap.stop(t)
		t.Fatalf("session %d: tunnel never came up\n--- edge ---\n%s\n--- conc ---\n%s",
			session, edge.log(), conc.log())
	}

	// Drive representative traffic: a saturating upload (DATA + FEC PARITY), with
	// PROBE/CONTROL + amnezia junk flowing continuously alongside.
	if mbps := top.iperf3Mbps(t, concInner, auditLoadSecs); mbps <= 0 {
		cap.stop(t)
		t.Fatalf("session %d: non-positive throughput %.2f Mbit/s", session, mbps)
	}

	cap.stop(t)

	data, err := os.ReadFile(pcapFile)
	if err != nil {
		t.Fatalf("session %d: read pcap %s: %v\n--- tcpdump ---\n%s", session, pcapFile, err, cap.log())
	}
	frames, err := wireaudit.ParsePcap(data, listenPort)
	if err != nil {
		t.Fatalf("session %d: parse pcap: %v\n--- tcpdump ---\n%s", session, err, cap.log())
	}
	return frames
}

// pcapCapture is a running tcpdump savefile capture. It manages its own lifetime
// (rather than using startProc, whose cleanup fires only at test end) so the caller
// can stop tcpdump and read a complete savefile mid-subtest.
type pcapCapture struct {
	cmd    *exec.Cmd
	output *lockedBuffer
	once   sync.Once
}

func (c *pcapCapture) log() string { return c.output.String() }

// stop flushes trailing packets, signals tcpdump, and waits for it to exit so the
// savefile is complete and closed before the caller reads it. Idempotent.
func (c *pcapCapture) stop(t *testing.T) {
	t.Helper()
	c.once.Do(func() {
		time.Sleep(auditCaptureFlushDelay)
		_ = c.cmd.Process.Signal(syscall.SIGTERM)
		done := make(chan struct{})
		go func() { _ = c.cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			_ = c.cmd.Process.Kill()
			<-done
		}
	})
}

// startPcap launches tcpdump writing a classic-pcap savefile of the UDP traffic on
// the given port over veth (in the current/edge network namespace), packet-buffered
// so the file is complete on stop. It stays root (-Z root) so no privilege-drop user
// is required, and waits auditCaptureAttachDelay for the capture to attach before
// returning. Requires CAP_NET_RAW (the e2e TestMain namespace / sudo provides it).
func (top *Topology) startPcap(t *testing.T, veth string, port int, file string) *pcapCapture {
	t.Helper()
	out := &lockedBuffer{}
	cmd := exec.Command("tcpdump", "-i", veth, "-n", "-p", "-U", "-Z", "root", "-w", file, "udp", "port", strconv.Itoa(port))
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		t.Fatalf("start tcpdump on %s: %v", veth, err)
	}
	cap := &pcapCapture{cmd: cmd, output: out}
	// Terminate the capture at test end too, in case a fatal path skips stop().
	t.Cleanup(func() { cap.stop(t) })
	time.Sleep(auditCaptureAttachDelay)
	return cap
}

// setupAuditTunnel brings up the edge+concentrator tunnel over auditPath with BOTH
// the amnezia obfuscation profile (junk active) AND the fixed-ratio FEC plane
// (parity active) enabled on both ends, so the captured wire carries the full
// traffic mix. It mirrors setupP3Tunnel's addressing/bring-up with the [amnezia]
// block added.
func setupAuditTunnel(t *testing.T, top *Topology, bin string) (edge, conc *proc) {
	t.Helper()

	edgePriv, edgePub := genKey(t)
	concPriv, concPub := genKey(t)
	psk := randKey(t)

	fecBlock := fmt.Sprintf("[fec]\nenabled = true\ndata_shards = %d\nparity_shards = %d\ndeadline = %d\n\n",
		auditFECData, auditFECParity, auditFECDeadlineNanos)

	dir := t.TempDir()
	edgeCfg := writeConfig(t, filepath.Join(dir, "edge.toml"), fmt.Sprintf(`role = "edge"
psk = "%s"

[[paths]]
name = "%s"
source_addr = "%s"

%s%s[wireguard]
private_key = "%s"

[[wireguard.peers]]
public_key = "%s"
endpoint = "%s:%d"
allowed_ips = ["%s/32"]

[log]
level = "error"
`, psk, auditPath.name, auditPath.edgeIP, amneziaProfileA, fecBlock, edgePriv, concPub, auditPath.concIP, listenPort, concInner))

	concCfg := writeConfig(t, filepath.Join(dir, "conc.toml"), fmt.Sprintf(`role = "concentrator"
psk = "%s"

[[paths]]
name = "%s"
source_addr = "%s"

%s%s[wireguard]
private_key = "%s"
listen_port = %d

[[wireguard.peers]]
public_key = "%s"
allowed_ips = ["%s/32"]

[log]
level = "error"
`, psk, auditPath.name, auditPath.concIP, amneziaProfileA, fecBlock, concPriv, listenPort, edgePub, edgeInner))

	conc = top.startProc(t, "concentrator", "nsenter", "-t", strconv.Itoa(top.pid), "-n", bin, "--config", concCfg)
	edge = top.startProc(t, "edge", bin, "--config", edgeCfg)

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
	return edge, conc
}
