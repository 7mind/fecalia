//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/metrics"
)

// This file is the T141 harness-contract extension: three reusable helpers that
// let an e2e test drive and observe a SUSTAINED tunnel load, rather than the
// fixed-duration bulk transfers (iperf3Mbps/rttUnderLoad) and single before/after
// scrapes (fetchMetrics) the earlier phases used.
//
//   - DriveUDPLoad: a rate-calibrated UDP load generator (sender+sink across the
//     tunnel) that sustains a target frames/sec offered load, including loads
//     above an exact-byte shaper's declared wire rate.
//   - MetricsSampler: a polling /metrics scraper that retains every sample across
//     a load window, so a caller can inspect the Exposition DURING the load, not
//     only at its two ends.
//   - ParseLogLines/AwaitLogLine: a structured (JSON) log-stream capturer that
//     finds expected transition records (for example "path liveness transition")
//     while a load runs.
//
// See load_self_test.go for the operational helper self-test (Q55/T141).

// UDPLoadSpec configures a sustained, rate-calibrated UDP offered-load run driven
// by DriveUDPLoad: TargetFPS frames/sec, each carrying PayloadBytes of payload,
// sustained for Duration wall-clock time.
type UDPLoadSpec struct {
	TargetFPS    float64
	PayloadBytes int
	Duration     time.Duration
}

// UDPLoadResult reports what DriveUDPLoad's SENDER side actually achieved: the
// frames/bytes it managed to WRITE to its UDP socket. This is the OFFERED load,
// not necessarily what the exact-byte shaper transmitted; under backpressure,
// wanbond_path_tx_bytes_total can fall short of this application-side count.
type UDPLoadResult struct {
	SentFrames  int64
	SentBytes   int64
	Elapsed     time.Duration
	AchievedFPS float64
}

// udpLoadPayloadByte fills every load-generator payload; its value is arbitrary
// (the sink discards every datagram unread), a fixed byte keeps DriveUDPLoad
// allocation-only per call (no per-frame RNG cost skews the achieved rate).
const udpLoadPayloadByte = 0xA5

// DriveUDPLoad sustains a target-fps UDP offered load from srcIP to sinkAddr
// ("<ip>:<port>") for spec.Duration — the sender+sink pair across the tunnel
// (T141) that lets a test drive a SUSTAINED, rate-calibrated offered load through
// a running tunnel. It first launches the UDP sink helper subprocess
// (startUDPLoadSink) inside the concentrator network namespace bound to
// sinkAddr, then paces spec.TargetFPS datagrams of spec.PayloadBytes from a UDP
// socket bound to srcIP — through the wanbond TUN once the tunnel is up, exactly
// as an application socket would use it. Pacing uses a fixed-interval ticker (not
// a tight send loop), so the ACHIEVED rate tracks the TARGET rather than blasting
// as fast as the kernel allows. A caller may drive above the configured wire rate
// to exercise exact-byte capacity backpressure.
func (top *Topology) DriveUDPLoad(t *testing.T, srcIP, sinkAddr string, spec UDPLoadSpec) UDPLoadResult {
	t.Helper()
	if spec.TargetFPS <= 0 {
		t.Fatalf("DriveUDPLoad: TargetFPS must be > 0, got %g", spec.TargetFPS)
	}
	if spec.PayloadBytes <= 0 {
		t.Fatalf("DriveUDPLoad: PayloadBytes must be > 0, got %d", spec.PayloadBytes)
	}
	if spec.Duration <= 0 {
		t.Fatalf("DriveUDPLoad: Duration must be > 0, got %s", spec.Duration)
	}

	top.startUDPLoadSink(t, sinkAddr)

	sinkUDPAddr, err := net.ResolveUDPAddr("udp", sinkAddr)
	if err != nil {
		t.Fatalf("DriveUDPLoad: resolve sink %q: %v", sinkAddr, err)
	}
	conn, err := net.DialUDP("udp", &net.UDPAddr{IP: net.ParseIP(srcIP)}, sinkUDPAddr)
	if err != nil {
		t.Fatalf("DriveUDPLoad: dial %s -> %s: %v", srcIP, sinkAddr, err)
	}
	defer func() { _ = conn.Close() }()

	payload := bytes.Repeat([]byte{udpLoadPayloadByte}, spec.PayloadBytes)
	interval := time.Duration(float64(time.Second) / spec.TargetFPS)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var result UDPLoadResult
	start := time.Now()
	deadline := start.Add(spec.Duration)
	for now := range ticker.C {
		if !now.Before(deadline) {
			break
		}
		n, err := conn.Write(payload)
		if err != nil {
			continue // transient send error (e.g. a momentary EAGAIN); keep pacing, don't abort the run
		}
		result.SentFrames++
		result.SentBytes += int64(n)
	}
	result.Elapsed = time.Since(start)
	result.AchievedFPS = float64(result.SentFrames) / result.Elapsed.Seconds()
	return result
}

// udpLoadSinkAddrEnv marks the UDP-load sink helper subprocess, mirroring
// reflectAddrEnv's marker-env-var pattern (liveness_test.go/T13).
const udpLoadSinkAddrEnv = "WANBOND_E2E_UDPLOAD_SINK"

// TestUDPLoadSinkHelper is not a standalone test: like TestProbeReflectorHelper,
// it is spawned as a subprocess (startUDPLoadSink) to receive and discard the
// datagrams DriveUDPLoad sends through the tunnel. It reports nothing back to the
// parent process — a caller reads the OFFERED load from DriveUDPLoad's own
// UDPLoadResult and the ACTUALLY-TRANSMITTED load from a MetricsSampler's
// wanbond_path_tx_bytes_total delta, never from anything the sink itself counts.
// Its only job is to exist: a CONNECTED UDP socket's next Write fails once an
// ICMP port-unreachable comes back from an absent peer, so the sink must be bound
// before DriveUDPLoad starts sending. Without the marker env var it is a no-op
// skip so the normal suite run does not block on it.
func TestUDPLoadSinkHelper(t *testing.T) {
	spec := os.Getenv(udpLoadSinkAddrEnv)
	if spec == "" {
		t.Skip("not invoked as the UDP load sink helper")
	}
	addr, err := net.ResolveUDPAddr("udp", spec)
	if err != nil {
		t.Fatalf("resolve UDP load sink addr %q: %v", spec, err)
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		t.Fatalf("bind UDP load sink %s: %v", spec, err)
	}
	defer func() { _ = conn.Close() }()

	buf := make([]byte, 64*1024)
	for {
		if _, _, err := conn.ReadFromUDP(buf); err != nil {
			return // socket closed on parent kill
		}
	}
}

// startUDPLoadSink launches the UDP-load sink helper subprocess inside the
// concentrator network namespace, bound to sinkAddr ("<ip>:<port>"), and blocks
// briefly so the bind has landed before the caller starts sending. It mirrors
// startReflector's self-exec pattern (liveness_test.go/T13).
func (top *Topology) startUDPLoadSink(t *testing.T, sinkAddr string) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("locate test binary: %v", err)
	}
	cmd := exec.Command("nsenter", "-t", strconv.Itoa(top.pid), "-n",
		self, "-test.run", "^TestUDPLoadSinkHelper$")
	cmd.Env = append(os.Environ(),
		nsEnvMarker+"=1", // the child is already in a namespace; skip TestMain's re-exec
		udpLoadSinkAddrEnv+"="+sinkAddr,
	)
	out := &lockedBuffer{}
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		t.Fatalf("start UDP load sink: %v", err)
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
	time.Sleep(100 * time.Millisecond) // let the bind land before the caller sends
}

// MetricsSample is one polled scrape from a MetricsSampler run, timestamped at
// the poll tick that triggered it.
type MetricsSample struct {
	At  time.Time
	Exp metrics.Exposition
}

// metricsSamplerScrapeTimeout bounds each individual poll's HTTP round trip, so a
// stalled endpoint skips that tick instead of wedging the whole poll loop.
const metricsSamplerScrapeTimeout = 2 * time.Second

// MetricsSampler periodically scrapes a /metrics endpoint via metrics.Fetch and
// retains every successful sample, so a caller can inspect the Exposition series
// at MULTIPLE points across a sustained-load window (T141) — unlike the single
// before/after fetchMetrics pattern (p2_aggregation_test.go), which cannot tell
// whether the endpoint stayed live and scrapable THROUGHOUT the window, only at
// its two ends.
type MetricsSampler struct {
	mu      sync.Mutex
	samples []MetricsSample
	stop    chan struct{}
	done    chan struct{}
}

// StartMetricsSampler launches a background poller against url, scraping every
// interval until the returned sampler's Stop is called. Stop is also registered
// as a t.Cleanup, so a caller that never calls it explicitly still has the poller
// torn down at test end.
func StartMetricsSampler(t *testing.T, url string, interval time.Duration) *MetricsSampler {
	t.Helper()
	if interval <= 0 {
		t.Fatalf("StartMetricsSampler: interval must be > 0, got %s", interval)
	}
	s := &MetricsSampler{stop: make(chan struct{}), done: make(chan struct{})}
	go s.run(url, interval)
	t.Cleanup(s.Stop)
	return s
}

func (s *MetricsSampler) run(url string, interval time.Duration) {
	defer close(s.done)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			return
		case now := <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), metricsSamplerScrapeTimeout)
			exp, err := metrics.Fetch(ctx, http.DefaultClient, url)
			cancel()
			if err != nil {
				continue // transient scrape failure (e.g. daemon mid-restart); skip this tick
			}
			s.mu.Lock()
			s.samples = append(s.samples, MetricsSample{At: now, Exp: exp})
			s.mu.Unlock()
		}
	}
}

// Stop halts the poller and blocks until its goroutine has exited. Idempotent —
// safe to call explicitly and again via the registered t.Cleanup.
func (s *MetricsSampler) Stop() {
	select {
	case <-s.stop:
	default:
		close(s.stop)
	}
	<-s.done
}

// Samples returns a snapshot of every sample retained so far, in poll order.
func (s *MetricsSampler) Samples() []MetricsSample {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]MetricsSample, len(s.samples))
	copy(out, s.samples)
	return out
}

// PathValueDelta returns the LAST retained sample's value minus the FIRST
// retained sample's value for a per-path series, failing the test if fewer than
// two samples were retained (no delta is well-defined) or either endpoint's
// scrape lacked the series — a missing series is a wiring defect, not a zero,
// mirroring deltaPathValue's convention in p2_aggregation_test.go.
func (s *MetricsSampler) PathValueDelta(t *testing.T, name, path string) float64 {
	t.Helper()
	samples := s.Samples()
	if len(samples) < 2 {
		t.Fatalf("metrics sampler retained %d sample(s), need >= 2 to form a delta for %s{path=%q}", len(samples), name, path)
	}
	first, ok := samples[0].Exp.PathValue(name, path)
	if !ok {
		t.Fatalf("first sample missing %s{path=%q}", name, path)
	}
	last, ok := samples[len(samples)-1].Exp.PathValue(name, path)
	if !ok {
		t.Fatalf("last sample missing %s{path=%q}", name, path)
	}
	return last - first
}

func TestWaitPathShaperDrainedRejectsLateTailAfterEmptyScrape(t *testing.T) {
	const path = "tail"
	if out, err := exec.Command("ip", "link", "set", "lo", "up").CombinedOutput(); err != nil {
		t.Fatalf("bring up loopback for metrics fixture: %v\n%s", err, out)
	}
	var mu sync.Mutex
	scrapes := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		scrapes++
		scrape := scrapes
		mu.Unlock()
		value := 0
		if scrape > 1 {
			value = 100
		}
		for _, name := range []string{
			metrics.MetricShaperAcceptedBytes,
			metrics.MetricShaperEmittedBytes,
			metrics.MetricShaperAcceptedDatagrams,
			metrics.MetricShaperEmittedDatagrams,
		} {
			_, _ = fmt.Fprintf(w, "%s{path=%q} %d\n", name, path, value)
		}
		for _, name := range []string{
			metrics.MetricShaperQueueBytes,
			metrics.MetricShaperInFlightBytes,
			metrics.MetricProbeSendErrors,
			metrics.MetricSocketWriteErrors,
			metrics.MetricShaperAdmissionCanceledDatagrams,
			metrics.MetricShaperAsyncWriteErrors,
			metrics.MetricShaperAsyncWriteErrorBytes,
			metrics.MetricShaperAsyncWriteEMSGSIZEErrors,
			metrics.MetricShaperAsyncWriteEMSGSIZEBytes,
		} {
			_, _ = fmt.Fprintf(w, "%s{path=%q} 0\n", name, path)
		}
		writeErrors := 0
		if scrape > 4 {
			writeErrors = 1
		}
		_, _ = fmt.Fprintf(w, "%s{path=%q} %d\n", metrics.MetricShaperWriteErrors, path, writeErrors)
	}))
	defer server.Close()

	exp := waitPathShaperDrained(t, server.URL, path)
	accepted, ok := exp.PathValue(metrics.MetricShaperAcceptedBytes, path)
	if !ok || accepted != 100 {
		t.Fatalf("terminal accepted bytes = %.0f (ok=%v), want late tail value 100 rather than the first empty scrape", accepted, ok)
	}
	if writeErrors, ok := exp.PathValue(metrics.MetricShaperWriteErrors, path); !ok || writeErrors != 1 {
		t.Fatalf("terminal write errors = %.0f (ok=%v), want late counter value 1 after quiescence reset", writeErrors, ok)
	}
}

const shaperDrainQuiescence = 250 * time.Millisecond

type shaperDrainFingerprint struct {
	acceptedBytes              float64
	emittedBytes               float64
	acceptedDatagrams          float64
	emittedDatagrams           float64
	queueBytes                 float64
	inFlightBytes              float64
	probeSendErrors            float64
	shaperWriteErrors          float64
	socketWriteErrors          float64
	admissionCanceledDatagrams float64
	asyncWriteErrors           float64
	asyncWriteErrorBytes       float64
	asyncWriteEMSGSIZEErrors   float64
	asyncWriteEMSGSIZEBytes    float64
}

// waitPathShaperDrained returns a terminal scrape only after accepted/emitted
// counters reconcile, the queue and writer stay empty, and all admission and
// terminal counters remain unchanged across a quiescence interval. The stable
// interval prevents an empty scrape from preceding later kernel/TUN-tail
// admissions.
func waitPathShaperDrained(t *testing.T, url, path string) metrics.Exposition {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var stable shaperDrainFingerprint
	var stableSince time.Time
	haveStable := false
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), metricsSamplerScrapeTimeout)
		exp := fetchMetrics(t, ctx, url)
		cancel()
		value := func(name string) float64 {
			got, ok := exp.PathValue(name, path)
			if !ok {
				t.Fatalf("drain scrape missing %s{path=%q}", name, path)
			}
			return got
		}
		current := shaperDrainFingerprint{
			acceptedBytes:              value(metrics.MetricShaperAcceptedBytes),
			emittedBytes:               value(metrics.MetricShaperEmittedBytes),
			acceptedDatagrams:          value(metrics.MetricShaperAcceptedDatagrams),
			emittedDatagrams:           value(metrics.MetricShaperEmittedDatagrams),
			queueBytes:                 value(metrics.MetricShaperQueueBytes),
			inFlightBytes:              value(metrics.MetricShaperInFlightBytes),
			probeSendErrors:            value(metrics.MetricProbeSendErrors),
			shaperWriteErrors:          value(metrics.MetricShaperWriteErrors),
			socketWriteErrors:          value(metrics.MetricSocketWriteErrors),
			admissionCanceledDatagrams: value(metrics.MetricShaperAdmissionCanceledDatagrams),
			asyncWriteErrors:           value(metrics.MetricShaperAsyncWriteErrors),
			asyncWriteErrorBytes:       value(metrics.MetricShaperAsyncWriteErrorBytes),
			asyncWriteEMSGSIZEErrors:   value(metrics.MetricShaperAsyncWriteEMSGSIZEErrors),
			asyncWriteEMSGSIZEBytes:    value(metrics.MetricShaperAsyncWriteEMSGSIZEBytes),
		}
		drained := current.acceptedBytes == current.emittedBytes &&
			current.acceptedDatagrams == current.emittedDatagrams &&
			current.queueBytes == 0 &&
			current.inFlightBytes == 0
		now := time.Now()
		if drained {
			if !haveStable || current != stable {
				stable = current
				stableSince = now
				haveStable = true
			} else if now.Sub(stableSince) >= shaperDrainQuiescence {
				return exp
			}
		} else {
			haveStable = false
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("exact-byte shaper path %q did not remain drained for %s within 10s", path, shaperDrainQuiescence)
	return metrics.Exposition{}
}

// LogLine is one parsed structured (JSON) daemon log record: the "msg" field
// plus every field the record carries (slog's fixed time/level/msg keys and any
// caller-supplied attributes, e.g. "from"/"to"/"silence_ms" on a liveness
// transition). Different messages
// carry different attribute sets by design (that is what structured logging
// means), so Fields stays a generic map rather than a per-message type; the
// FieldString/FieldFloat accessors give typed access at the call site.
type LogLine struct {
	Msg    string
	Fields map[string]any
}

// FieldString returns Fields[name] as a string, and whether it was present and a
// string.
func (l LogLine) FieldString(name string) (string, bool) {
	v, ok := l.Fields[name].(string)
	return v, ok
}

// FieldFloat returns Fields[name] as a float64, and whether it was present and
// numeric. encoding/json decodes every JSON number into float64 when the target
// is map[string]any, so this covers every numeric attribute (e.g. silence_ms)
// regardless of whether the daemon logged it as an int or a float.
func (l LogLine) FieldFloat(name string) (float64, bool) {
	v, ok := l.Fields[name].(float64)
	return v, ok
}

// ParseLogLines parses every well-formed single-line JSON object in log — the
// shape internal/log.New's slog JSON handler emits, one object per line — into a
// LogLine, silently skipping any line that is not a JSON object with a "msg"
// field (non-JSON noise, e.g. a non-structured stderr print from a dependency, or
// a partial line if the capture raced process termination). It is the parsing
// half of the structured-log capturer (T141): a test asserts on daemon records
// such as "path liveness transition" instead of grepping raw text.
func ParseLogLines(log string) []LogLine {
	var out []LogLine
	for _, line := range strings.Split(log, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var raw map[string]any
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			continue
		}
		msg, ok := raw["msg"].(string)
		if !ok || msg == "" {
			continue
		}
		out = append(out, LogLine{Msg: msg, Fields: raw})
	}
	return out
}

// logLinePollInterval bounds how often AwaitLogLine re-parses the captured
// output while waiting for a line to appear.
const logLinePollInterval = 50 * time.Millisecond

// AwaitLogLine polls p's captured combined output (proc.log) until a structured
// record whose "msg" field equals msg appears, returning it with ok=true, or
// until timeout elapses (returning ok=false). It is the poll-until-observed
// counterpart to pingUntil/waitLink for a daemon's structured logs: a test can
// find a transition line while a load is running, rather than only after the
// process has exited.
func AwaitLogLine(t *testing.T, p *proc, msg string, timeout time.Duration) (LogLine, bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		for _, l := range ParseLogLines(p.log()) {
			if l.Msg == msg {
				return l, true
			}
		}
		if time.Now().After(deadline) {
			return LogLine{}, false
		}
		time.Sleep(logLinePollInterval)
	}
}
