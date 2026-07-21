package bind

import (
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/config"
	"github.com/7mind/wanbond/internal/frame"
)

// authEcho builds a MAC'd probe ECHO stamped with the given path id — the frame an edge
// path receives back from the concentrator's reflector (T247 seam g: on the edge, an idle
// standby WAN's only inbound is echoes, and they must keep that path's own return-address
// entry fresh).
func authEcho(t *testing.T, psk config.Key, pathID uint8, seq uint64, clk *fakeClock) []byte {
	t.Helper()
	raw, err := frame.Encode(psk, frame.Probe{PathID: pathID, ProbeSeq: seq, TimestampNanos: clk.Now().UnixNano(), IsEcho: true})
	if err != nil {
		t.Fatalf("encode echo: %v", err)
	}
	return raw
}

// TestRemoteTableMultiPathNoBleed locks T247 seam (b): a bind with MULTIPLE paths keeps a
// per-sender-path table on EACH peerPathState independently — learning on one path's view
// never moves or pollutes another path's selection.
func TestRemoteTableMultiPathNoBleed(t *testing.T) {
	psk := testKey(t, 0x81)
	clk := newFakeClock()
	m, _, _ := newProbingMultipath(t, loopbackPaths(2), psk, clk)
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	ps0, ps1 := m.paths[0], m.paths[1]

	src0 := netip.MustParseAddrPort("203.0.113.10:40000")
	src1 := netip.MustParseAddrPort("198.51.100.11:40001")

	m.demuxInbound(ps0, authProbe(t, psk, 1, 1, clk), src0)
	m.demuxInbound(ps1, authProbe(t, psk, 2, 2, clk), src1)

	if got, _ := ps0.getRemote(); got != src0 {
		t.Fatalf("path 0 getRemote = %v, want %v", got, src0)
	}
	if got, _ := ps1.getRemote(); got != src1 {
		t.Fatalf("path 1 getRemote = %v, want %v (cross-path bleed)", got, src1)
	}

	// Learning a new sender on path 1 leaves path 0's selection untouched.
	other := netip.MustParseAddrPort("192.0.2.33:40002")
	m.demuxInbound(ps1, authProbe(t, psk, 3, 3, clk), other)
	if got, _ := ps0.getRemote(); got != src0 {
		t.Fatalf("path 0 selection moved to %v after path-1 learning (bleed)", got)
	}
	if got, _ := ps1.getRemote(); got != src1 {
		t.Fatalf("path 1 selection moved to %v on a non-selected establishment (must stay sticky)", got)
	}
}

// TestRemoteRoamQuietUnderProbeCadence locks T247 seam (d): across K probe cadences with
// interleaved standby probes and stable DATA on the active WAN, the roam callback fires
// exactly once (initial establishment) — the pre-D94 per-cadence NotifyRoam churn is gone
// — and the concentrator's own probe egress (emitProbes reads getRemote) keeps targeting
// the SELECTED destination throughout. A forced DEAD fallback then adds exactly one more.
func TestRemoteRoamQuietUnderProbeCadence(t *testing.T) {
	psk := testKey(t, 0x82)
	clk := newFakeClock()
	m, _, _ := newProbingMultipath(t, loopbackPaths(1), psk, clk)
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	ps := m.paths[0]

	var roams atomic.Int32
	m.OnPathRoam(ps.name, func() { roams.Add(1) })

	srcA := netip.MustParseAddrPort("203.0.113.20:40000")
	srcB := netip.MustParseAddrPort("198.51.100.21:40001")

	codec, err := frame.NewCodec(psk)
	if err != nil {
		t.Fatalf("build codec: %v", err)
	}

	m.demuxInbound(ps, authProbe(t, psk, 1, 1, clk), srcA)
	for k := 0; k < 6; k++ { // K >= 5 cadences
		m.demuxInbound(ps, authProbe(t, psk, 2, uint64(10+k), clk), srcB)
		m.demuxInbound(ps, authProbe(t, psk, 1, uint64(20+k), clk), srcA)
		m.demuxInbound(ps, mustEncodeData(t, codec, uint64(k), 1, "d"), srcA)
		m.emitProbes() // the concentrator's own probe cadence: egress reads getRemote
		if got, _ := ps.getRemote(); got != srcA {
			t.Fatalf("cadence %d: selected destination = %v, want %v (probe egress would follow the flap)", k, got, srcA)
		}
	}
	if n := roams.Load(); n != 1 {
		t.Fatalf("roam callback fired %d times over 6 cadences, want exactly 1 (initial establishment)", n)
	}

	// One forced failover: A goes probe-silent while B keeps probing (B's entry becomes
	// the fresher one — the realistic silence pattern), then the DEAD bound elapses.
	// Exactly one more roam.
	m.demuxInbound(ps, authProbe(t, psk, 2, 99, clk), srcB)
	ps.checkRemoteDead(time.Now().Add(remoteDeadAfter + time.Second))
	if got, _ := ps.getRemote(); got != srcB {
		t.Fatalf("after DEAD fallback getRemote = %v, want %v", got, srcB)
	}
	if n := roams.Load(); n != 2 {
		t.Fatalf("roam callback fired %d times after one failover, want exactly 2", n)
	}
}

// TestEchoRefreshesOwnPathEntryAndOverrideSticky locks T247 seams (g) and (a): an
// authenticated probe ECHO — stamped with the path's OWN id, the only inbound an idle
// edge standby WAN sees — establishes and refreshes that path's return-address entry, and
// a later echo from a NEW source (the concentrator's public address changed mid-session)
// repoints the entry in place so a subsequent failover reaches the NEW address. After a
// SetPeerRemote override, fresh non-selected learning does not move the selection.
func TestEchoRefreshesOwnPathEntryAndOverrideSticky(t *testing.T) {
	psk := testKey(t, 0x83)
	clk := newFakeClock()
	m, _, _ := newProbingMultipath(t, loopbackPaths(1), psk, clk)
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	ps := m.paths[0]

	var roams atomic.Int32
	m.OnPathRoam(ps.name, func() { roams.Add(1) })

	conc := netip.MustParseAddrPort("203.0.113.50:51820")
	concNew := netip.MustParseAddrPort("203.0.113.51:51820") // after a public-address change

	// Cold start via an echo (edge-side view: this path's own id): establishes + selects.
	m.demuxInbound(ps, authEcho(t, psk, ps.id, 1, clk), conc)
	if got, _ := ps.getRemote(); got != conc {
		t.Fatalf("after first echo getRemote = %v, want %v (echo-established entry)", got, conc)
	}

	// The concentrator's address changes; the next echo repoints the entry in place and
	// the selection follows (one roam) — the post-rebind failover reaches the NEW address.
	before := roams.Load()
	m.demuxInbound(ps, authEcho(t, psk, ps.id, 2, clk), concNew)
	if got, _ := ps.getRemote(); got != concNew {
		t.Fatalf("after concentrator rebind getRemote = %v, want %v (echo must refresh the entry)", got, concNew)
	}
	if d := roams.Load() - before; d != 1 {
		t.Fatalf("selected-entry echo rebind fired %d roams, want exactly 1", d)
	}

	// SetPeerRemote override: selection honours the override; fresh NON-selected learning
	// (a request under a different sender id) does not move it.
	ovr := netip.MustParseAddrPort("192.0.2.99:51820")
	m.SetPeerRemote(ovr)
	m.demuxInbound(ps, authProbe(t, psk, 7, 3, clk), concNew)
	if got, _ := ps.getRemote(); got != ovr {
		t.Fatalf("post-override learning moved getRemote to %v; must honour the override %v until it goes DEAD or DATA names another entry", got, ovr)
	}
}
