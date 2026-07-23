package monitor

import (
	"encoding/json"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/metrics"
	"github.com/7mind/wanbond/internal/reseq"
	"github.com/7mind/wanbond/internal/telemetry"
)

// fakeSource is a static metrics.Source that returns a fixed set of
// snapshots, mirroring internal/metrics's own test fakeSource so BuildSnapshot
// can be exercised with no live engine/bind wiring.
type fakeSource struct {
	paths        []metrics.PathSnapshot
	fec          []metrics.FECSnapshot
	reseq        []metrics.ReseqSnapshot
	aggregation  []metrics.AggregationSnapshot
	session      metrics.SessionSnapshot
	peerSessions []metrics.PeerSessionSnapshot
	peerNames    []string
}

func (f fakeSource) Paths() []metrics.PathSnapshot               { return f.paths }
func (f fakeSource) FEC() []metrics.FECSnapshot                  { return f.fec }
func (f fakeSource) Reseq() []metrics.ReseqSnapshot              { return f.reseq }
func (f fakeSource) Aggregation() []metrics.AggregationSnapshot  { return f.aggregation }
func (f fakeSource) Session() metrics.SessionSnapshot            { return f.session }
func (f fakeSource) PeerSessions() []metrics.PeerSessionSnapshot { return f.peerSessions }
func (f fakeSource) PeerNames() []string                         { return f.peerNames }

// TestBuildSnapshot_ExtendedFields exercises the G21 contract extension (T214):
// the daemon identity, per-path bind metadata + declared link params + the
// redactable addressing block, the ordered endpoint list from the LIVE Info
// provider, and the truncated WG fingerprint. It also asserts the server-side
// redaction gate: with revealAddressing=false the per-path addressing block is
// nil, endpoint addresses are blanked (active/standby shape kept), the
// fingerprint survives (Q63 — no full key), AddressingHidden is set, and the
// redacted source address is absent from the marshaled frame.
func TestBuildSnapshot_ExtendedFields(t *testing.T) {
	src := fakeSource{
		paths: []metrics.PathSnapshot{
			{
				Peer:        "",
				Name:        "starlink",
				State:       telemetry.StateUp,
				BindMode:    "device",
				BoundDevice: "eth0",
				Source:      netip.MustParseAddr("192.168.1.10"),
				Remote:      netip.MustParseAddrPort("203.0.113.7:51820"),
			},
		},
		peerNames: []string{""},
	}
	info := Info{
		Role:                   "edge",
		Version:                "v1.2.3",
		UptimeSeconds:          42,
		WGPublicKeyFingerprint: "AbCdEfGhIj",
		PathLinks: map[PathKey]PathLink{
			{Peer: "", Name: "starlink"}: {LinkBandwidthBps: 50e6, LinkRttSeconds: 0.045},
		},
		Endpoints: func() []EndpointSnapshot {
			return []EndpointSnapshot{
				{Address: "203.0.113.7:51820", Active: true},
				{Address: "198.51.100.7:51820", Active: false},
			}
		},
	}

	// revealAddressing = true (loopback binding): every new field populated.
	snap := BuildSnapshot(src, info, true)
	if snap.Daemon.Role != "edge" || snap.Daemon.Version != "v1.2.3" || snap.Daemon.UptimeSeconds != 42 {
		t.Fatalf("daemon = %+v", snap.Daemon)
	}
	if snap.WGPublicKeyFingerprint != "AbCdEfGhIj" {
		t.Fatalf("fingerprint = %q", snap.WGPublicKeyFingerprint)
	}
	if snap.AddressingHidden {
		t.Fatalf("AddressingHidden must be false when revealed")
	}
	if len(snap.Paths) != 1 {
		t.Fatalf("paths len = %d", len(snap.Paths))
	}
	p := snap.Paths[0]
	if p.BindMode != "device" || p.BoundDevice != "eth0" {
		t.Fatalf("bind metadata = %q/%q", p.BindMode, p.BoundDevice)
	}
	if p.LinkBandwidthBps != 50e6 || p.LinkRttSeconds != 0.045 {
		t.Fatalf("link metadata = %v/%v", p.LinkBandwidthBps, p.LinkRttSeconds)
	}
	if p.Addressing == nil || p.Addressing.Source != "192.168.1.10" || p.Addressing.Remote != "203.0.113.7:51820" {
		t.Fatalf("addressing = %+v", p.Addressing)
	}
	if len(snap.Endpoints) != 2 || snap.Endpoints[0].Address != "203.0.113.7:51820" || !snap.Endpoints[0].Active {
		t.Fatalf("endpoints = %+v", snap.Endpoints)
	}

	// revealAddressing = false (non-loopback binding): server-side redaction.
	red := BuildSnapshot(src, info, false)
	if !red.AddressingHidden {
		t.Fatalf("AddressingHidden must be true when not revealed")
	}
	if red.Paths[0].Addressing != nil {
		t.Fatalf("addressing must be nil when redacted, got %+v", red.Paths[0].Addressing)
	}
	if red.WGPublicKeyFingerprint != "AbCdEfGhIj" {
		t.Fatalf("fingerprint must survive redaction (Q63 fingerprint-only), got %q", red.WGPublicKeyFingerprint)
	}
	if len(red.Endpoints) != 2 || red.Endpoints[0].Address != "" || !red.Endpoints[0].Active {
		t.Fatalf("endpoint addresses must be blanked but active/standby kept, got %+v", red.Endpoints)
	}
	b, err := json.Marshal(red)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), "192.168.1.10") || strings.Contains(string(b), "203.0.113.7") {
		t.Fatalf("redacted frame leaked an address: %s", b)
	}
}

// TestBuildSnapshot_RedactsAddressingWhenNotRevealed is the dedicated
// regression guard for the Q62/Q64 server-side redaction gate (T215). It feeds
// BuildSnapshot a Source + Info carrying multiple DISTINCT addresses (per-path
// source/remote across two paths + two hub-endpoint addresses), then asserts on
// the fully MARSHALED JSON bytes that with revealAddressing=false NONE of those
// address strings appear anywhere in the frame — the strongest operational form
// of "redacted server-side, not merely hidden client-side" — while the
// active/standby endpoint shape and the truncated WG fingerprint (Q63; no full
// key exists to leak) survive. The revealAddressing=true arm proves the same
// addresses ARE present when a loopback binding reveals them, so the test cannot
// pass vacuously.
func TestBuildSnapshot_RedactsAddressingWhenNotRevealed(t *testing.T) {
	// Distinct, unmistakable address literals so a substring scan is unambiguous.
	const (
		srcA  = "192.0.2.11"
		remA  = "203.0.113.21:51820"
		srcB  = "192.0.2.12"
		remB  = "203.0.113.22:51820"
		hubA  = "198.51.100.31:51820"
		hubB  = "198.51.100.32:51820"
		print = "Fp0aBcDeFg"
	)
	secretAddrs := []string{srcA, remA, srcB, remB, hubA, hubB}

	src := fakeSource{
		paths: []metrics.PathSnapshot{
			{Peer: "", Name: "starlink", State: telemetry.StateUp,
				Source: netip.MustParseAddr(srcA), Remote: netip.MustParseAddrPort(remA)},
			{Peer: "", Name: "cellular", State: telemetry.StateUp,
				Source: netip.MustParseAddr(srcB), Remote: netip.MustParseAddrPort(remB)},
		},
		peerNames: []string{""},
	}
	info := Info{
		Role: "edge", Version: "v9", UptimeSeconds: 1,
		WGPublicKeyFingerprint: print,
		Endpoints: func() []EndpointSnapshot {
			return []EndpointSnapshot{
				{Address: hubA, Active: true},
				{Address: hubB, Active: false},
			}
		},
	}

	// revealAddressing = false: the redacted frame must leak nothing.
	red, err := json.Marshal(BuildSnapshot(src, info, false))
	if err != nil {
		t.Fatalf("marshal redacted: %v", err)
	}
	for _, a := range secretAddrs {
		if strings.Contains(string(red), a) {
			t.Fatalf("redacted frame leaked address %q: %s", a, red)
		}
	}
	var decoded MonitorSnapshot
	if err := json.Unmarshal(red, &decoded); err != nil {
		t.Fatalf("unmarshal redacted: %v", err)
	}
	if !decoded.AddressingHidden {
		t.Fatalf("addressingHidden must be true in the redacted frame")
	}
	if decoded.WGPublicKeyFingerprint != print {
		t.Fatalf("fingerprint must survive redaction (Q63), got %q", decoded.WGPublicKeyFingerprint)
	}
	for i, p := range decoded.Paths {
		if p.Addressing != nil {
			t.Fatalf("path %d addressing must be nil when redacted, got %+v", i, p.Addressing)
		}
	}
	// The ordered active/standby endpoint shape is preserved (addresses blanked).
	if len(decoded.Endpoints) != 2 || !decoded.Endpoints[0].Active || decoded.Endpoints[1].Active {
		t.Fatalf("endpoint active/standby shape not preserved: %+v", decoded.Endpoints)
	}
	if decoded.Endpoints[0].Address != "" || decoded.Endpoints[1].Address != "" {
		t.Fatalf("endpoint addresses must be blanked when redacted: %+v", decoded.Endpoints)
	}

	// revealAddressing = true: the SAME addresses must now be present (non-vacuity).
	full, err := json.Marshal(BuildSnapshot(src, info, true))
	if err != nil {
		t.Fatalf("marshal revealed: %v", err)
	}
	for _, a := range secretAddrs {
		if !strings.Contains(string(full), a) {
			t.Fatalf("revealed frame missing address %q: %s", a, full)
		}
	}
}

// TestBuildSnapshotSinglePeer feeds BuildSnapshot a single-bound-peer Source
// (PeerNames() reporting exactly one name, "" per the metrics package's
// back-compat rule) and asserts the marshalled JSON's fields and shape,
// including that MultiPeer is false and durations render as float seconds.
func TestBuildSnapshotSinglePeer(t *testing.T) {
	src := fakeSource{
		paths: []metrics.PathSnapshot{
			{
				Peer:                    "",
				Name:                    "starlink",
				TxBytes:                 1000,
				RxBytes:                 2000,
				ThroughputBitsPerSecond: 12345.5,
				Estimate: telemetry.Estimate{
					RTT:    50 * time.Millisecond,
					Jitter: 5 * time.Millisecond,
					Loss:   0.01,
				},
				State: telemetry.StateUp,
			},
		},
		fec: []metrics.FECSnapshot{
			{
				Peer:                 "",
				DataPackets:          100,
				RepairPackets:        10,
				RecoveredPackets:     5,
				UnrecoverablePackets: 1,
				DataBytes:            140000,
				RepairBytes:          14000,
				ResidualLossRatio:    0.02,
			},
		},
		reseq: []metrics.ReseqSnapshot{
			{
				Peer: "",
				Stats: reseq.Stats{
					Released:       500,
					DroppedDup:     3,
					DroppedOld:     2,
					DroppedSuspect: 1,
					Skipped:        4,
					Resyncs:        6,
					Rebaselines:    7,
				},
			},
		},
		aggregation: []metrics.AggregationSnapshot{
			{
				Peer:                  "",
				Aggregating:           true,
				OfferedLoadFPS:        123.4,
				EngageThresholdFPS:    200,
				DisengageThresholdFPS: 100,
			},
		},
		session: metrics.SessionSnapshot{
			Established:      true,
			LastHandshakeAge: 30 * time.Second,
		},
		peerNames: []string{""},
	}

	snap := BuildSnapshot(src, Info{}, true)

	if snap.MultiPeer {
		t.Fatalf("MultiPeer = true, want false for a single-bound-peer Source")
	}
	if len(snap.PeerNames) != 1 || snap.PeerNames[0] != "" {
		t.Fatalf("PeerNames = %#v, want [\"\"]", snap.PeerNames)
	}

	b, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	if decoded["multiPeer"] != false {
		t.Errorf("json multiPeer = %v, want false", decoded["multiPeer"])
	}

	paths, ok := decoded["paths"].([]any)
	if !ok || len(paths) != 1 {
		t.Fatalf("json paths = %#v, want a 1-element array", decoded["paths"])
	}
	p := paths[0].(map[string]any)
	if p["name"] != "starlink" {
		t.Errorf("path name = %v, want starlink", p["name"])
	}
	if p["peer"] != "" {
		t.Errorf("path peer = %v, want \"\"", p["peer"])
	}
	if p["txBytes"] != float64(1000) {
		t.Errorf("path txBytes = %v, want 1000", p["txBytes"])
	}
	if p["rxBytes"] != float64(2000) {
		t.Errorf("path rxBytes = %v, want 2000", p["rxBytes"])
	}
	if p["throughputBps"] != 12345.5 {
		t.Errorf("path throughputBps = %v, want 12345.5", p["throughputBps"])
	}
	if p["rttSeconds"] != 0.05 {
		t.Errorf("path rttSeconds = %v, want 0.05 (50ms as seconds)", p["rttSeconds"])
	}
	if p["jitterSeconds"] != 0.005 {
		t.Errorf("path jitterSeconds = %v, want 0.005 (5ms as seconds)", p["jitterSeconds"])
	}
	if p["loss"] != 0.01 {
		t.Errorf("path loss = %v, want 0.01", p["loss"])
	}
	if p["up"] != true {
		t.Errorf("path up = %v, want true", p["up"])
	}

	fec, ok := decoded["fec"].([]any)
	if !ok || len(fec) != 1 {
		t.Fatalf("json fec = %#v, want a 1-element array", decoded["fec"])
	}
	f := fec[0].(map[string]any)
	if f["dataPackets"] != float64(100) || f["repairPackets"] != float64(10) {
		t.Errorf("fec counters = %#v, want dataPackets=100 repairPackets=10", f)
	}
	if f["residualLossRatio"] != 0.02 {
		t.Errorf("fec residualLossRatio = %v, want 0.02", f["residualLossRatio"])
	}

	reseqArr, ok := decoded["reseq"].([]any)
	if !ok || len(reseqArr) != 1 {
		t.Fatalf("json reseq = %#v, want a 1-element array", decoded["reseq"])
	}
	r := reseqArr[0].(map[string]any)
	if r["released"] != float64(500) || r["rebaselines"] != float64(7) {
		t.Errorf("reseq counters = %#v, want released=500 rebaselines=7", r)
	}

	agg, ok := decoded["aggregation"].([]any)
	if !ok || len(agg) != 1 {
		t.Fatalf("json aggregation = %#v, want a 1-element array", decoded["aggregation"])
	}
	a := agg[0].(map[string]any)
	if a["aggregating"] != true {
		t.Errorf("aggregation aggregating = %v, want true", a["aggregating"])
	}
	if a["offeredLoadFps"] != 123.4 {
		t.Errorf("aggregation offeredLoadFps = %v, want 123.4", a["offeredLoadFps"])
	}

	session, ok := decoded["session"].(map[string]any)
	if !ok {
		t.Fatalf("json session = %#v, want an object", decoded["session"])
	}
	if session["established"] != true {
		t.Errorf("session established = %v, want true", session["established"])
	}
	if session["lastHandshakeSeconds"] != float64(30) {
		t.Errorf("session lastHandshakeSeconds = %v, want 30 (30s as seconds)", session["lastHandshakeSeconds"])
	}
}

// TestBuildSnapshotMultiPeer feeds BuildSnapshot a 2-peer Source and asserts
// MultiPeer is true and each per-(peer,path)/FEC/Reseq/Aggregation entry
// carries its bound peer's name.
func TestBuildSnapshotMultiPeer(t *testing.T) {
	src := fakeSource{
		paths: []metrics.PathSnapshot{
			{Peer: "east", Name: "starlink", State: telemetry.StateUp},
			{Peer: "west", Name: "starlink", State: telemetry.StateDown},
		},
		fec: []metrics.FECSnapshot{
			{Peer: "east", DataPackets: 10},
			{Peer: "west", DataPackets: 20},
		},
		reseq: []metrics.ReseqSnapshot{
			{Peer: "east", Stats: reseq.Stats{Released: 1}},
			{Peer: "west", Stats: reseq.Stats{Released: 2}},
		},
		aggregation: []metrics.AggregationSnapshot{
			{Peer: "east", Aggregating: true},
		},
		session:   metrics.SessionSnapshot{Established: false, LastHandshakeAge: 0},
		peerNames: []string{"east", "west"},
	}

	snap := BuildSnapshot(src, Info{}, true)

	if !snap.MultiPeer {
		t.Fatalf("MultiPeer = false, want true for a 2-peer Source")
	}
	if len(snap.PeerNames) != 2 || snap.PeerNames[0] != "east" || snap.PeerNames[1] != "west" {
		t.Fatalf("PeerNames = %#v, want [east west]", snap.PeerNames)
	}

	b, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	if decoded["multiPeer"] != true {
		t.Errorf("json multiPeer = %v, want true", decoded["multiPeer"])
	}

	peerNames, ok := decoded["peerNames"].([]any)
	if !ok || len(peerNames) != 2 || peerNames[0] != "east" || peerNames[1] != "west" {
		t.Fatalf("json peerNames = %#v, want [east west]", decoded["peerNames"])
	}

	paths := decoded["paths"].([]any)
	if len(paths) != 2 {
		t.Fatalf("json paths = %#v, want a 2-element array", decoded["paths"])
	}
	p0 := paths[0].(map[string]any)
	p1 := paths[1].(map[string]any)
	if p0["peer"] != "east" || p0["up"] != true {
		t.Errorf("path[0] = %#v, want peer=east up=true", p0)
	}
	if p1["peer"] != "west" || p1["up"] != false {
		t.Errorf("path[1] = %#v, want peer=west up=false", p1)
	}

	fec := decoded["fec"].([]any)
	if len(fec) != 2 || fec[0].(map[string]any)["peer"] != "east" || fec[1].(map[string]any)["peer"] != "west" {
		t.Errorf("json fec = %#v, want peers east then west", decoded["fec"])
	}

	reseqArr := decoded["reseq"].([]any)
	if len(reseqArr) != 2 || reseqArr[0].(map[string]any)["peer"] != "east" || reseqArr[1].(map[string]any)["peer"] != "west" {
		t.Errorf("json reseq = %#v, want peers east then west", decoded["reseq"])
	}

	agg := decoded["aggregation"].([]any)
	if len(agg) != 1 || agg[0].(map[string]any)["peer"] != "east" {
		t.Errorf("json aggregation = %#v, want a single east entry (west has no gate)", decoded["aggregation"])
	}

	session := decoded["session"].(map[string]any)
	if session["established"] != false || session["lastHandshakeSeconds"] != float64(0) {
		t.Errorf("session = %#v, want established=false lastHandshakeSeconds=0", session)
	}
}

// TestBuildSnapshotEmptyIsNotNull asserts that empty per-(peer,path)/FEC/
// Reseq/Aggregation sets marshal as `[]`, not `null` — a nil slice would force
// the frontend to null-check every field before iterating.
func TestBuildSnapshotEmptyIsNotNull(t *testing.T) {
	snap := BuildSnapshot(fakeSource{peerNames: []string{""}}, Info{}, true)

	b, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	for _, field := range []string{`"paths":[]`, `"fec":[]`, `"reseq":[]`, `"aggregation":[]`} {
		if !strings.Contains(string(b), field) {
			t.Errorf("marshalled JSON %s does not contain %q, want an empty array not null", b, field)
		}
	}
}
