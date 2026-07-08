package config

import (
	"math"
	"testing"
	"time"
)

// TestSizePacingFromBDPSlowUplinkEngagesGate documents defect D22's sizing note: the
// synthetic default per_path_capacity_fps (10000) sits far above a realistic slow uplink,
// so the aggregation gate never engages; BDP-derived sizing yields a capacity that is
// well below the default and thus lets the gate engage. It also checks burst == one RTT
// of in-flight frames (the bandwidth-delay product in frame units).
func TestSizePacingFromBDPSlowUplinkEngagesGate(t *testing.T) {
	// A realistic slow mobile uplink: 10 Mbit/s bottleneck, ~45 ms Starlink-class RTT,
	// full-MTU 1400-byte outer frames.
	const (
		bandwidthBitsPerSec = 10e6
		wireFrameBytes      = 1400.0
	)
	rtt := 45 * time.Millisecond

	sz, err := SizePacingFromBDP(bandwidthBitsPerSec, rtt, wireFrameBytes)
	if err != nil {
		t.Fatalf("SizePacingFromBDP: %v", err)
	}

	// capacity = 10e6 / (8 * 1400) ~= 892.9 fps.
	wantCapacity := bandwidthBitsPerSec / (8 * wireFrameBytes)
	if math.Abs(sz.CapacityFPS-wantCapacity) > 1e-6 {
		t.Fatalf("CapacityFPS = %g, want %g", sz.CapacityFPS, wantCapacity)
	}
	// The whole point (D22): the measured capacity is FAR below the frame-count default,
	// so the aggregation gate — which compares offered load against capacity — can engage
	// on this uplink, whereas the 10000-fps default keeps it dormant.
	if sz.CapacityFPS >= defaultPerPathCapacityFPS {
		t.Fatalf("BDP capacity %g is not below the frame-count default %g — the gate would still never engage", sz.CapacityFPS, defaultPerPathCapacityFPS)
	}
	// burst = capacity * RTT = one RTT of in-flight frames (== BDP_bytes / frame_bytes).
	wantBurst := wantCapacity * rtt.Seconds()
	if math.Abs(sz.BurstFrames-wantBurst) > 1e-6 {
		t.Fatalf("BurstFrames = %g, want %g (one RTT of in-flight frames)", sz.BurstFrames, wantBurst)
	}
	// Cross-check burst against the bandwidth-delay product computed independently in bytes.
	bdpBytes := (bandwidthBitsPerSec / 8) * rtt.Seconds()
	if math.Abs(sz.BurstFrames-bdpBytes/wireFrameBytes) > 1e-6 {
		t.Fatalf("BurstFrames %g != BDP_bytes/frame %g", sz.BurstFrames, bdpBytes/wireFrameBytes)
	}
}

// TestSizePacingFromBDPFailFast covers the fail-fast guards on non-positive inputs.
func TestSizePacingFromBDPFailFast(t *testing.T) {
	if _, err := SizePacingFromBDP(0, time.Second, 1400); err == nil {
		t.Fatal("zero bandwidth accepted, want error")
	}
	if _, err := SizePacingFromBDP(1e6, 0, 1400); err == nil {
		t.Fatal("zero RTT accepted, want error")
	}
	if _, err := SizePacingFromBDP(1e6, time.Second, 0); err == nil {
		t.Fatal("zero frame size accepted, want error")
	}
}
