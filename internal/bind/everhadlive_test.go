package bind

import (
	"crypto/rand"
	"net/netip"
	"testing"

	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/telemetry"
)

// driveEdgePathUp brings the EDGE's own prober (m.paths[idx]) to StateUp by sending
// testProbeUpSucc real probes and feeding back their reflected echoes through the
// Multipath's OWN inbound dispatch (m.demuxInbound), NOT by calling prober.HandleEcho
// directly — the I4 everUp latch lives in dispatchInbound's echo branch, so a test that
// bypassed demuxInbound would exercise the prober but never the Bind-level predicate.
func driveEdgePathUp(t *testing.T, m *Multipath, idx int, psk config.Key, src netip.AddrPort, clk *fakeClock) {
	t.Helper()
	reflector := telemetry.NewReflector(psk, rand.Reader)
	pp := m.paths[idx]
	for i := 0; i < testProbeUpSucc; i++ {
		raw, err := pp.prober.SendProbe()
		if err != nil {
			t.Fatalf("SendProbe: %v", err)
		}
		echo, _, err := reflector.Reflect(raw)
		if err != nil {
			t.Fatalf("reflect probe: %v", err)
		}
		clk.advance(testProbeRTT)
		m.demuxInbound(pp, echo, src)
		clk.advance(testProbeInterval - testProbeRTT)
	}
	if pp.prober.State() != telemetry.StateUp {
		t.Fatalf("path %q not Up after %d demuxInbound echoes: %v", pp.name, testProbeUpSucc, pp.prober.State())
	}
}

// TestEverHadLivePathLatchesOnFirstUpAndStaysStickyAfterOutage is the bind-level half of
// the I4 acceptance: EverHadLivePath is false before any path has reached liveness up,
// becomes true the moment the first path does (driven through the SAME dispatchInbound
// echo path production traffic uses), and stays true even after that path later goes back
// Down — a later outage must not un-latch the predicate the engineLogger warmup gate relies
// on to distinguish "startup warmup" from "genuine outage after connectivity".
func TestEverHadLivePathLatchesOnFirstUpAndStaysStickyAfterOutage(t *testing.T) {
	psk := testKey(t, 0x71)
	clk := newFakeClock()
	m, _, _ := newProbingMultipath(t, loopbackPaths(1), psk, clk)
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	if m.EverHadLivePath() {
		t.Fatalf("EverHadLivePath = true before any echo was ever handled")
	}

	src := netip.MustParseAddrPort("127.0.0.1:9")
	driveEdgePathUp(t, m, 0, psk, src, clk)

	if !m.EverHadLivePath() {
		t.Fatalf("EverHadLivePath = false immediately after the path reached StateUp")
	}

	// Silence the path past DownAfter and Tick it back Down: the CURRENT liveness verdict
	// flips, but the STICKY ever-up latch must not.
	clk.advance(testProbeDownAfter + testProbeInterval)
	m.paths[0].prober.Tick()
	if m.paths[0].prober.State() != telemetry.StateDown {
		t.Fatalf("path did not go Down after silence past DownAfter: %v", m.paths[0].prober.State())
	}
	if !m.EverHadLivePath() {
		t.Fatalf("EverHadLivePath flipped back to false after the path went Down; the I4 latch must be sticky")
	}
}
