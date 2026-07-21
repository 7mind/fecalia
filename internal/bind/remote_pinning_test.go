package bind

import (
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/frame"
)

// TestConcentratorDownlinkPinsToActivePath is the D94/T246 fail-first reproduction and
// acceptance: a single-socket concentrator receives authenticated probes from BOTH of an
// edge's WAN sources (distinct srcAPs, distinct sender-stamped PathIDs), while uplink
// DATA rides only the ACTIVE WAN. The downlink destination (getRemote) must pin to the
// active WAN's address and NOT flap to whichever WAN probed last.
//
// RED on the pre-T246 code: the Probe branch calls setRemote(srcAP) unconditionally, so
// the last prober wins — after the srcB probe, getRemote() returns srcB even though all
// DATA arrives from srcA (the confirmed D94 mechanism: ~50% of downlink DATA egressing
// the metered standby WAN at the 200 ms probe cadence).
func TestConcentratorDownlinkPinsToActivePath(t *testing.T) {
	psk := testKey(t, 0x77)
	clk := newFakeClock()
	m, _, _ := newProbingMultipath(t, loopbackPaths(1), psk, clk)
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	ps := m.paths[0]

	var roams atomic.Int32
	m.OnPathRoam(ps.name, func() { roams.Add(1) })

	srcA := netip.MustParseAddrPort("203.0.113.1:40000")  // edge WAN A (active: carries DATA)
	srcB := netip.MustParseAddrPort("198.51.100.2:40001") // edge WAN B (standby: probes only)

	codec, err := frame.NewCodec(psk)
	if err != nil {
		t.Fatalf("build codec: %v", err)
	}

	// First authenticated probe (edge path 1, WAN A) establishes AND selects srcA — the
	// R253 cold-start rule: getRemote() is valid and stable from the first probe, before
	// any DATA arrives, and the initial establishment fires exactly one roam callback.
	m.demuxInbound(ps, authProbe(t, psk, 1, 1, clk), srcA)
	if got, ok := ps.getRemote(); !ok || got != srcA {
		t.Fatalf("after first probe getRemote = %v ok=%v, want %v (first-probe-established selection)", got, ok, srcA)
	}

	// Uplink DATA from WAN A confirms the selection (address-match gate; no extra roam).
	m.demuxInbound(ps, mustEncodeData(t, codec, 0, 1, "d0"), srcA)

	// An interleaved probe from the STANDBY WAN (edge path 2, srcB) refreshes B's entry
	// for failover readiness but MUST NOT move the selected downlink destination.
	m.demuxInbound(ps, authProbe(t, psk, 2, 2, clk), srcB)
	if got, ok := ps.getRemote(); !ok || got != srcA {
		t.Fatalf("after standby probe getRemote = %v ok=%v, want %v STICKY (D94: last-prober flap)", got, ok, srcA)
	}

	// More interleaved standby probes at cadence; DATA keeps riding WAN A. Selection and
	// the roam count stay pinned (no per-probe churn).
	for seq := uint64(3); seq < 8; seq++ {
		m.demuxInbound(ps, authProbe(t, psk, 2, seq, clk), srcB)
		m.demuxInbound(ps, mustEncodeData(t, codec, seq, 1, "d"), srcA)
		if got, _ := ps.getRemote(); got != srcA {
			t.Fatalf("seq %d: getRemote = %v, want %v (flap at probe cadence)", seq, got, srcA)
		}
	}
	if n := roams.Load(); n > 1 {
		t.Fatalf("roam callback fired %d times under stable DATA on A; want <= 1 (initial establishment only)", n)
	}

	// A genuine active-path change: DATA arrives address-match-gated from WAN B (its entry
	// was probe-established above). The selection MOVES once.
	m.demuxInbound(ps, mustEncodeData(t, codec, 8, 2, "d8"), srcB)
	if got, _ := ps.getRemote(); got != srcB {
		t.Fatalf("after DATA on WAN B getRemote = %v, want %v (address-match-gated selection move)", got, srcB)
	}

	// Forged DATA: WAN A's address under WAN B's PathID (mismatch with B's established
	// entry) and an entirely unknown address — neither may move the selection nor
	// establish anything.
	m.demuxInbound(ps, mustEncodeData(t, codec, 9, 2, "forged"), srcA)
	if got, _ := ps.getRemote(); got != srcB {
		t.Fatalf("forged DATA (addr/PathID mismatch) moved getRemote to %v; must stay %v", got, srcB)
	}
	evil := netip.MustParseAddrPort("192.0.2.66:6666")
	m.demuxInbound(ps, mustEncodeData(t, codec, 10, 3, "spoof"), evil)
	if got, _ := ps.getRemote(); got != srcB {
		t.Fatalf("spoofed-source DATA moved getRemote to %v; must stay %v (DATA never introduces an address)", got, srcB)
	}
}

// TestRemoteRebindDeadFallbackAndOverride covers the remaining T246 selection
// transitions: an in-place NAT rebind of the selected entry (same sender path id, new
// address) follows with one roam callback; probe-silence on the selected entry beyond
// remoteDeadAfter triggers the ONE-time sticky fallback to the freshest probe-learned
// entry; and the SetPeerRemote override repoints the whole bond, clearing learned state.
func TestRemoteRebindDeadFallbackAndOverride(t *testing.T) {
	psk := testKey(t, 0x78)
	clk := newFakeClock()
	m, _, _ := newProbingMultipath(t, loopbackPaths(1), psk, clk)
	if _, _, err := m.Open(0); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	ps := m.paths[0]

	var roams atomic.Int32
	m.OnPathRoam(ps.name, func() { roams.Add(1) })

	srcA := netip.MustParseAddrPort("203.0.113.1:40000")
	srcA2 := netip.MustParseAddrPort("203.0.113.99:41000") // WAN A after a NAT rebind
	srcB := netip.MustParseAddrPort("198.51.100.2:40001")

	// Establish + select WAN A, then rebind it in place: the selection follows the NEW
	// address under the unchanged sender path id (D9/D11), with exactly one roam.
	m.demuxInbound(ps, authProbe(t, psk, 1, 1, clk), srcA)
	before := roams.Load()
	m.demuxInbound(ps, authProbe(t, psk, 1, 2, clk), srcA2)
	if got, _ := ps.getRemote(); got != srcA2 {
		t.Fatalf("after same-PathID rebind getRemote = %v, want %v (in-place repoint)", got, srcA2)
	}
	if d := roams.Load() - before; d != 1 {
		t.Fatalf("rebind of the selected entry fired %d roams, want exactly 1", d)
	}

	// Establish WAN B (selection stays sticky on A2), then let A's probes go silent past
	// remoteDeadAfter: the selection falls back ONCE to the freshest entry (B).
	m.demuxInbound(ps, authProbe(t, psk, 2, 3, clk), srcB)
	if got, _ := ps.getRemote(); got != srcA2 {
		t.Fatalf("standby establishment moved getRemote to %v; want sticky %v", got, srcA2)
	}
	before = roams.Load()
	ps.checkRemoteDead(time.Now().Add(remoteDeadAfter + time.Second))
	if got, _ := ps.getRemote(); got != srcB {
		t.Fatalf("after selected-entry probe silence getRemote = %v, want DEAD fallback to %v", got, srcB)
	}
	if d := roams.Load() - before; d != 1 {
		t.Fatalf("DEAD fallback fired %d roams, want exactly 1", d)
	}

	// SetPeerRemote is the deliberate whole-bond override: it repoints and clears the
	// learned table so stale entries cannot win a later fallback.
	ovr := netip.MustParseAddrPort("192.0.2.200:51820")
	m.SetPeerRemote(ovr)
	if got, _ := ps.getRemote(); got != ovr {
		t.Fatalf("after SetPeerRemote getRemote = %v, want the override %v", got, ovr)
	}
}
