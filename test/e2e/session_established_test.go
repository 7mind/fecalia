//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/metrics"
)

// I2 is the netns e2e for the WG-session observability signal: the
// wanbond_session_established gauge, the wanbond_session_last_handshake_seconds age, and
// the ONE 'session established' INFO log record emitted on the 0->1 edge. Together they
// distinguish a tunnel that is STILL CONVERGING from one that is WEDGED — the signal
// D35/D36/D37 all presented identically without.
//
// EXECUTION IS DEFERRED (G2 pattern): the netns tier needs the privileged two-namespace
// fixture and is not run in the unit environment. This file must COMPILE and vet
// (`go vet -tags e2e ./test/e2e` + `go build -tags e2e ./test/e2e`); the metric
// registration and the 0->1 edge are validated by the runnable unit tests in
// internal/metrics and internal/device.
const (
	// i2MetricsListen is the edge's /metrics endpoint for this file's test, on a port none
	// of the other e2e files use (p2/p3/p4/pacing use 9095-97; tolerant_startup uses 9098).
	i2MetricsListen = "127.0.0.1:9099"
	i2MetricsURL    = "http://" + i2MetricsListen + "/metrics"

	// i2SessionUpDeadline bounds how long the WG session may take to establish after the
	// tunnel is up and carrying traffic. Generous: it must cover the edge's initial
	// handshake plus the session monitor's next poll tick. It comfortably exceeds the edge
	// keepalive so a keepalive-driven handshake also lands within it.
	i2SessionUpDeadline = 30 * time.Second
)

// TestSessionEstablishedTransitions is the I2 acceptance. It brings the bond up on a single
// path, drives inner traffic to force the WG handshake, and asserts:
//
//   - wanbond_session_established reaches 1 (scraped via metrics.Fetch) — the "->1" half of
//     the 0->1 transition. The "0->" half is asserted by the 'session established' log
//     record's existence: the device-side edge detector emits it ONLY on a false->true
//     transition, starting from false, so exactly one record proves a genuine 0->1 edge.
//     (Catching session=0 in an intermediate scrape is deliberately avoided — like the
//     path_up=1/session=0 window, the netns tier crosses it within ms and a scrape observer
//     would nondeterministically miss it.)
//   - wanbond_session_last_handshake_seconds is present and within the session-validity
//     window (a fresh handshake).
//   - the 'session established' record appears EXACTLY ONCE per session.
//   - the path-up-before-session-established ordering holds, by comparing the path-liveness
//     up transition and session-established transition TIMESTAMPS recorded in the logs (NOT
//     by observing an intermediate scrape): wanbond's own probe plane brings the path up
//     independent of the WG handshake, which only fires on inner traffic / keepalive, so the
//     path-up record must precede the session-established record.
func TestSessionEstablishedTransitions(t *testing.T) {
	bin := buildWanbond(t)
	path := DefaultPaths[0] // starlink
	top := SetupWithPaths(t, []pathSpec{path})
	edge, conc := setupI2Tunnel(t, top, bin, []pathSpec{path}, i2MetricsListen)

	// Force the WG handshake by driving inner traffic through the tunnel; without traffic the
	// edge would only handshake on its 25s keepalive.
	if !top.pingUntil(concInner, 15*time.Second) {
		t.Fatalf("bond never came up on path %q\n--- edge ---\n%s\n--- conc ---\n%s", path.name, edge.log(), conc.log())
	}

	// (1) wanbond_session_established reaches 1.
	waitSessionEstablished(t, i2MetricsURL, i2SessionUpDeadline)

	// (2) The last-handshake age is present and fresh (within the validity window).
	exp := scrapeMetrics(t, i2MetricsURL)
	age, ok := exp.Value(metrics.MetricSessionLastHandshake)
	if !ok {
		t.Fatalf("%s series absent after the session established", metrics.MetricSessionLastHandshake)
	}
	if age < 0 || age > 180 {
		t.Errorf("%s = %.1fs, want a fresh handshake within the 180s validity window", metrics.MetricSessionLastHandshake, age)
	}

	// (3) The 'session established' record appears exactly once per session.
	logText := edge.log()
	if got := countLogRecords(logText, "session established"); got != 1 {
		t.Fatalf("'session established' logged %d times, want exactly 1 per session\n%s", got, logText)
	}

	// (4) path-up-before-session-established ordering, by log timestamps.
	pathUpAt, ok := pathLivenessUpTime(logText)
	if !ok {
		t.Fatalf("no 'path liveness transition' to=up record in the edge log\n%s", logText)
	}
	sessionAt, ok := logRecordTime(logText, "session established")
	if !ok {
		t.Fatalf("no 'session established' record in the edge log\n%s", logText)
	}
	if sessionAt.Before(pathUpAt) {
		t.Errorf("session established (%s) preceded path up (%s); expected path up FIRST — the probe plane brings the path up before the WG handshake fires on traffic/keepalive",
			sessionAt.Format(time.RFC3339Nano), pathUpAt.Format(time.RFC3339Nano))
	}

	t.Logf("I2: path up at %s, session established at %s (once); last-handshake age %.1fs",
		pathUpAt.Format(time.RFC3339Nano), sessionAt.Format(time.RFC3339Nano), age)
}

// waitSessionEstablished polls url's /metrics until wanbond_session_established reads 1, or
// fails at deadline. A mid-poll scrape error is tolerated (transient), since the assertion
// is about the gauge VALUE reaching 1, not the endpoint's own availability.
func waitSessionEstablished(t *testing.T, url string, deadline time.Duration) {
	t.Helper()
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		exp, err := metrics.Fetch(ctx, http.DefaultClient, url)
		cancel()
		if err == nil {
			if v, ok := exp.Value(metrics.MetricSessionEstablished); ok && v == 1 {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("wanbond_session_established never reached 1 within %s", deadline)
}

// i2LogRecord is the subset of a daemon slog JSON record this file asserts on: the emission
// time, the message, and the liveness transition's target state.
type i2LogRecord struct {
	Time time.Time `json:"time"`
	Msg  string    `json:"msg"`
	To   string    `json:"to"`
}

// logRecordTime returns the emission time of the FIRST log record whose msg equals the
// given message, and whether one was found.
func logRecordTime(logText, msg string) (time.Time, bool) {
	for _, line := range strings.Split(logText, "\n") {
		if !strings.Contains(line, msg) {
			continue
		}
		var rec i2LogRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		if rec.Msg == msg {
			return rec.Time, true
		}
	}
	return time.Time{}, false
}

// pathLivenessUpTime returns the emission time of the FIRST 'path liveness transition'
// record whose target state is up, and whether one was found.
func pathLivenessUpTime(logText string) (time.Time, bool) {
	for _, line := range strings.Split(logText, "\n") {
		if !strings.Contains(line, "path liveness transition") {
			continue
		}
		var rec i2LogRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		if rec.Msg == "path liveness transition" && rec.To == "up" {
			return rec.Time, true
		}
	}
	return time.Time{}, false
}

// countLogRecords counts the log records whose msg equals the given message.
func countLogRecords(logText, msg string) int {
	n := 0
	for _, line := range strings.Split(logText, "\n") {
		if !strings.Contains(line, msg) {
			continue
		}
		var rec i2LogRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		if rec.Msg == msg {
			n++
		}
	}
	return n
}

// setupI2Tunnel mirrors setupT60Tunnel's config shape and bring-up sequence, adding an edge
// [metrics] listen block AND running the edge at [log] level "info": I2 asserts on the INFO
// 'session established' and 'path liveness transition' records, which an "error" level would
// suppress.
func setupI2Tunnel(t *testing.T, top *Topology, bin string, paths []pathSpec, metricsListen string) (edge, conc *proc) {
	t.Helper()

	edgePriv, edgePub := genKey(t)
	concPriv, concPub := genKey(t)
	psk := randKey(t)

	var edgePaths, concPaths strings.Builder
	for _, p := range paths {
		edgePaths.WriteString(pathBlock(p.name, p.edgeIP, p.concIP+":"+strconv.Itoa(listenPort)))
		concPaths.WriteString(pathBlock(p.name, p.concIP, ""))
	}
	primary := paths[0]
	metricsBlock := "[metrics]\nlisten = \"" + metricsListen + "\"\n\n"

	dir := t.TempDir()
	edgeCfg := writeConfig(t, filepath.Join(dir, "edge.toml"),
		"role = \"edge\"\npsk = \""+psk+"\"\n\n"+
			edgePaths.String()+metricsBlock+
			"[wireguard]\nprivate_key = \""+edgePriv+"\"\n\n"+
			"[[wireguard.peers]]\npublic_key = \""+concPub+"\"\n"+
			"endpoint = \""+primary.concIP+":"+strconv.Itoa(listenPort)+"\"\n"+
			"allowed_ips = [\""+concInner+"/32\"]\n\n"+
			"[log]\nlevel = \"info\"\n")

	concCfg := writeConfig(t, filepath.Join(dir, "conc.toml"),
		"role = \"concentrator\"\npsk = \""+psk+"\"\n\n"+
			concPaths.String()+
			"[wireguard]\nprivate_key = \""+concPriv+"\"\nlisten_port = "+strconv.Itoa(listenPort)+"\n\n"+
			"[[wireguard.peers]]\npublic_key = \""+edgePub+"\"\n"+
			"allowed_ips = [\""+edgeInner+"/32\"]\n\n"+
			"[log]\nlevel = \"info\"\n")

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

// pathBlock renders one [[paths]] TOML block. destAddr is empty for the concentrator side
// (it has no dest_addr — it roams edges).
func pathBlock(name, sourceAddr, destAddr string) string {
	b := "[[paths]]\nname = \"" + name + "\"\nsource_addr = \"" + sourceAddr + "\"\n"
	if destAddr != "" {
		b += "dest_addr = \"" + destAddr + "\"\n"
	}
	return b + "\n"
}
