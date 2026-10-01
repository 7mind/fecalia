package monitor

import (
	"encoding/json"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/7mind/wanbond/internal/bond"
	"github.com/7mind/wanbond/internal/metrics"
	"github.com/7mind/wanbond/internal/reseq"
	"github.com/7mind/wanbond/internal/telemetry"
)

// fakeSource is a static metrics.Source that returns a fixed set of
// snapshots, mirroring internal/metrics's own test fakeSource so BuildSnapshot
// can be exercised with no live engine/bind wiring.
type fakeSource struct {
	paths        []metrics.PathSnapshot
	reseq        []metrics.ReseqSnapshot
	session      metrics.SessionSnapshot
	peerSessions []metrics.PeerSessionSnapshot
	peerNames    []string
	adaptive     []metrics.AdaptiveSnapshot
}

func (f fakeSource) Paths() []metrics.PathSnapshot               { return f.paths }
func (f fakeSource) Reseq() []metrics.ReseqSnapshot              { return f.reseq }
func (f fakeSource) Session() metrics.SessionSnapshot            { return f.session }
func (f fakeSource) PeerSessions() []metrics.PeerSessionSnapshot { return f.peerSessions }
func (f fakeSource) PeerNames() []string                         { return f.peerNames }
func (f fakeSource) Adaptive() []metrics.AdaptiveSnapshot        { return f.adaptive }

// TestBuildSnapshot_ExtendedFields exercises the G21 contract extension (T214):
// the daemon identity, per-path bind metadata + the
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
		Endpoints: func() []EndpointSnapshot {
			return []EndpointSnapshot{
				{Address: "203.0.113.7:51820", Active: true},
				{Address: "198.51.100.7:51820", Active: false},
			}
		},
	}

	// revealAddressing = true (loopback binding): every new field populated.
	snap := BuildSnapshot(src, info, true, false)
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
	if p.Addressing == nil || p.Addressing.Source != "192.168.1.10" || p.Addressing.Remote != "203.0.113.7:51820" {
		t.Fatalf("addressing = %+v", p.Addressing)
	}
	if len(snap.Endpoints) != 2 || snap.Endpoints[0].Address != "203.0.113.7:51820" || !snap.Endpoints[0].Active {
		t.Fatalf("endpoints = %+v", snap.Endpoints)
	}

	// revealAddressing = false (non-loopback binding): server-side redaction.
	red := BuildSnapshot(src, info, false, false)
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
	red, err := json.Marshal(BuildSnapshot(src, info, false, false))
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
	full, err := json.Marshal(BuildSnapshot(src, info, true, false))
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
		session: metrics.SessionSnapshot{
			Established:      true,
			LastHandshakeAge: 30 * time.Second,
		},
		peerNames: []string{""},
	}

	snap := BuildSnapshot(src, Info{}, true, false)

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

	reseqArr, ok := decoded["reseq"].([]any)
	if !ok || len(reseqArr) != 1 {
		t.Fatalf("json reseq = %#v, want a 1-element array", decoded["reseq"])
	}
	r := reseqArr[0].(map[string]any)
	if r["released"] != float64(500) || r["rebaselines"] != float64(7) {
		t.Errorf("reseq counters = %#v, want released=500 rebaselines=7", r)
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
// MultiPeer is true and each per-(peer,path)/Reseq entry
// carries its bound peer's name.
func TestBuildSnapshotMultiPeer(t *testing.T) {
	src := fakeSource{
		paths: []metrics.PathSnapshot{
			{Peer: "east", Name: "starlink", State: telemetry.StateUp},
			{Peer: "west", Name: "starlink", State: telemetry.StateDown},
		},
		reseq: []metrics.ReseqSnapshot{
			{Peer: "east", Stats: reseq.Stats{Released: 1}},
			{Peer: "west", Stats: reseq.Stats{Released: 2}},
		},
		session:   metrics.SessionSnapshot{Established: false, LastHandshakeAge: 0},
		peerNames: []string{"east", "west"},
	}

	snap := BuildSnapshot(src, Info{}, true, false)

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

	reseqArr := decoded["reseq"].([]any)
	if len(reseqArr) != 2 || reseqArr[0].(map[string]any)["peer"] != "east" || reseqArr[1].(map[string]any)["peer"] != "west" {
		t.Errorf("json reseq = %#v, want peers east then west", decoded["reseq"])
	}

	session := decoded["session"].(map[string]any)
	if session["established"] != false || session["lastHandshakeSeconds"] != float64(0) {
		t.Errorf("session = %#v, want established=false lastHandshakeSeconds=0", session)
	}
}

// TestBuildSnapshotReseqMirrorsStats pins the resequencer wire object: every
// reseq.Stats field is carried under its JSON name with its value, durations and
// the armed deadline as integer nanoseconds, and the object has no other key.
func TestBuildSnapshotReseqMirrorsStats(t *testing.T) {
	armedDeadline := time.Unix(1_700_000_000, 123)
	snap := BuildSnapshot(fakeSource{
		reseq: []metrics.ReseqSnapshot{
			{Peer: "east", Stats: reseq.Stats{
				Released:        1,
				DroppedDup:      2,
				DroppedOld:      3,
				DroppedSuspect:  4,
				Skipped:         5,
				Resyncs:         6,
				Rebaselines:     7,
				Holds:           8,
				HoldNanos:       9,
				ArmedDeadline:   armedDeadline,
				ArmedWindow:     60 * time.Millisecond,
				DeadlineWakeups: 11,
				GapFills:        12,
			}},
			{Peer: "west"},
		},
		peerNames: []string{"east", "west"},
	}, Info{}, true, false)

	wire, err := json.Marshal(snap.Reseq)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var got []map[string]any
	if err := json.Unmarshal(wire, &got); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	want := []map[string]any{
		{
			"peer": "east", "released": float64(1), "droppedDup": float64(2), "droppedOld": float64(3),
			"droppedSuspect": float64(4), "skipped": float64(5), "resyncs": float64(6),
			"rebaselines": float64(7), "holds": float64(8), "holdNanos": float64(9),
			"armedDeadlineUnixNano": float64(armedDeadline.UnixNano()),
			"armedWindowNanos":      float64(60 * time.Millisecond),
			"deadlineWakeups":       float64(11), "gapFills": float64(12),
		},
		{
			"peer": "west", "released": float64(0), "droppedDup": float64(0), "droppedOld": float64(0),
			"droppedSuspect": float64(0), "skipped": float64(0), "resyncs": float64(0),
			"rebaselines": float64(0), "holds": float64(0), "holdNanos": float64(0),
			"armedDeadlineUnixNano": float64(0),
			"armedWindowNanos":      float64(0),
			"deadlineWakeups":       float64(0), "gapFills": float64(0),
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reseq wire objects:\n got=%s\nwant=%v", wire, want)
	}
}

func TestBuildSnapshotExitCapablePeersUsesAuthoritativeInfo(t *testing.T) {
	info := Info{ExitCapablePeers: []string{"tokyo", "osaka"}}
	snap := BuildSnapshot(fakeSource{peerNames: []string{"tokyo", "osaka"}}, info, true, true)

	if got := strings.Join(snap.ExitCapablePeers, ","); got != "tokyo,osaka" {
		t.Fatalf("exitCapablePeers = %q, want config-order %q", got, "tokyo,osaka")
	}
	info.ExitCapablePeers[0] = "mutated"
	if snap.ExitCapablePeers[0] != "tokyo" {
		t.Fatalf("snapshot aliases Info.ExitCapablePeers: got %q after source mutation", snap.ExitCapablePeers[0])
	}
}

func TestBuildSnapshotSeparatesExitModeFromActiveExit(t *testing.T) {
	mode := "auto"
	active := "osaka"
	info := Info{ExitMode: func() string { return mode }, ActiveExit: func() string { return active }}
	src := fakeSource{peerNames: []string{"tokyo", "osaka"}}
	first := BuildSnapshot(src, info, true, true)
	if first.ExitMode != "auto" || first.ActiveExit != "osaka" {
		t.Fatalf("first selection = %q/%q", first.ExitMode, first.ActiveExit)
	}
	mode, active = "tokyo", "tokyo"
	second := BuildSnapshot(src, info, true, true)
	if second.ExitMode != "tokyo" || second.ActiveExit != "tokyo" {
		t.Fatalf("second selection = %q/%q", second.ExitMode, second.ActiveExit)
	}
}

// TestBuildSnapshotEmptyIsNotNull asserts that empty per-(peer,path)/
// Reseq sets marshal as `[]`, not `null` — a nil slice would force
// the frontend to null-check every field before iterating.
func TestBuildSnapshotEmptyIsNotNull(t *testing.T) {
	snap := BuildSnapshot(fakeSource{peerNames: []string{""}}, Info{}, true, false)

	b, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	for _, field := range []string{`"paths":[]`, `"lanes":[]`, `"transport":[]`, `"reseq":[]`, `"peerSessions":[]`, `"exitCapablePeers":[]`} {
		if !strings.Contains(string(b), field) {
			t.Errorf("marshalled JSON %s does not contain %q, want an empty array not null", b, field)
		}
	}
}

// TestBuildSnapshotSinglePeerByteCompatibleExceptAdditiveFields is the T257 back-compat
// acceptance: for a single-bound-peer edge, the marshalled snapshot is JSON-identical to the
// pre-T257 wire shape once the additive fields (peerSessions, activeExit,
// exitCapablePeers, and each endpoint's peer) are stripped. The "want" shape below
// is the literal pre-T257 BuildSnapshot/EndpointSnapshot behaviour for this fixture,
// less the fields of the removed static transports; the comparison is exact, so it
// also proves none of those fields is still served.
func TestBuildSnapshotSinglePeerByteCompatibleExceptAdditiveFields(t *testing.T) {
	src := fakeSource{
		paths: []metrics.PathSnapshot{
			{Peer: "", Name: "solo", TxBytes: 100, RxBytes: 200,
				Estimate: telemetry.Estimate{RTT: 10 * time.Millisecond}, State: telemetry.StateUp},
		},
		peerSessions: []metrics.PeerSessionSnapshot{{Peer: "", Established: false, LastHandshakeSeconds: 0}},
		peerNames:    []string{""},
	}
	info := Info{
		Endpoints: func() []EndpointSnapshot {
			return []EndpointSnapshot{{Address: "9.9.9.9:1", Active: true}}
		},
	}

	snap := BuildSnapshot(src, info, true, false)

	b, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	// The additive fields must be present...
	if _, ok := got["peerSessions"]; !ok {
		t.Fatalf("peerSessions field missing from %s", b)
	}
	if _, ok := got["activeExit"]; !ok {
		t.Fatalf("activeExit field missing from %s", b)
	}
	if _, ok := got["exitControlAvailable"]; !ok {
		t.Fatalf("exitControlAvailable field missing from %s", b)
	}
	if _, ok := got["exitCapablePeers"]; !ok {
		t.Fatalf("exitCapablePeers field missing from %s", b)
	}
	eps, ok := got["endpoints"].([]any)
	if !ok || len(eps) != 1 {
		t.Fatalf("endpoints = %#v, want a 1-element array", got["endpoints"])
	}
	ep0 := eps[0].(map[string]any)
	if peer, ok := ep0["peer"]; !ok || peer != "" {
		t.Fatalf("endpoints[0].peer = %#v, want present and \"\"", peer)
	}

	// ...and, once stripped, the remainder must equal the pre-T257 shape exactly.
	delete(got, "peerSessions")
	delete(got, "activeExit")
	delete(got, "exitMode")
	delete(got, "exitControlAvailable")
	delete(got, "exitCapablePeers")
	delete(ep0, "peer")
	// The transport's lanes and queue counters are additive as well; this
	// source has none.
	for _, field := range []string{"lanes", "transport"} {
		if list, ok := got[field].([]any); !ok || len(list) != 0 {
			t.Fatalf("%s = %#v, want an empty array", field, got[field])
		}
		delete(got, field)
	}

	want := map[string]any{
		"paths": []any{
			map[string]any{
				"name": "solo", "peer": "", "txBytes": float64(100), "rxBytes": float64(200),
				"throughputBps": float64(0), "rttSeconds": 0.01, "jitterSeconds": float64(0),
				"loss": float64(0), "up": true, "bindMode": "", "boundDevice": "",
				"addressing": map[string]any{"source": "", "remote": ""},
			},
		},
		"reseq":     []any{},
		"session":   map[string]any{"established": false, "lastHandshakeSeconds": float64(0)},
		"peerNames": []any{""},
		"multiPeer": false,
		"daemon":    map[string]any{"role": "", "version": "", "uptimeSeconds": float64(0)},
		"endpoints": []any{
			map[string]any{"address": "9.9.9.9:1", "active": true},
		},
		"wgPublicKeyFingerprint": "",
		"addressingHidden":       false,
	}

	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("re-marshal stripped got: %v", err)
	}
	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal want: %v", err)
	}
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("stripped snapshot != pre-T257 shape:\n got=%s\nwant=%s", gotJSON, wantJSON)
	}
}

// TestBuildSnapshotExitControlAvailableTracksRawLoopbackVerdict pins the T280
// split: ExitControlAvailable follows the RAW loopbackBound verdict, NOT
// revealAddressing, while AddressingHidden follows revealAddressing. The critical
// row is the reveal-override non-loopback bind (revealAddressing=true,
// loopbackBound=false): addressing is unhidden yet exit control stays UNAVAILABLE,
// so the two verdicts are provably independent on the wire.
func TestBuildSnapshotExitControlAvailableTracksRawLoopbackVerdict(t *testing.T) {
	src := fakeSource{peerNames: []string{""}}
	cases := []struct {
		name             string
		revealAddressing bool
		loopbackBound    bool
		wantExit         bool
		wantHidden       bool
	}{
		{"loopback bind", true, true, true, false},
		{"non-loopback redacted", false, false, false, true},
		{"reveal-override non-loopback", true, false, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			snap := BuildSnapshot(src, Info{}, tc.revealAddressing, tc.loopbackBound)
			if snap.ExitControlAvailable != tc.wantExit {
				t.Errorf("ExitControlAvailable = %v, want %v", snap.ExitControlAvailable, tc.wantExit)
			}
			if snap.AddressingHidden != tc.wantHidden {
				t.Errorf("AddressingHidden = %v, want %v", snap.AddressingHidden, tc.wantHidden)
			}
		})
	}
}

// TestBuildSnapshotLanesMirrorTransport pins the lane and transport objects:
// rates in bits per second, the local path's name and the peer's path id taken
// from the lane id, every decision counter, and the exact key sets.
func TestBuildSnapshotLanesMirrorTransport(t *testing.T) {
	src := fakeSource{
		peerNames: []string{""},
		adaptive: []metrics.AdaptiveSnapshot{{
			Peer:      "hub",
			LanePaths: map[bond.PathID]string{256: "5g"},
			State: bond.Snapshot{
				QueueDrops: 9, AdmissionDrops: 1, AQMDrops: 2, InteractiveQueueDrops: 3, InteractiveQueued: 4, Expired: 5, Duplicates: 6, CoalescedACKs: 7,
				Paths: []bond.PathStats{{
					Path: 256, Capacity: 130000, Rate: 125000, SendRate: 100000, DeliveryRate: 90000,
					RTT: 60 * time.Millisecond, QueueDelay: 37 * time.Millisecond, Threshold: 30 * time.Millisecond,
					InFlight: 5000, Window: 30000, Sent: 11, ACKed: 10, Retransmits: 12, Up: true, Discovering: true,
					Decisions: bond.Decisions{DelaySignals: 21, LossSignals: 22, DiscoveryCongested: 23, DiscoveryPlateau: 24,
						CapacityRemeasured: 25, CapacityDecays: 26, Pulses: 27, PulseWins: 28, PulseLosses: 29, Rediscoveries: 30, StallSignals: 31},
				}},
			},
		}},
	}
	b, err := json.Marshal(BuildSnapshot(src, Info{}, true, false))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Lanes     []map[string]any `json:"lanes"`
		Transport []map[string]any `json:"transport"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	wantLane := map[string]any{
		"peer": "hub", "path": "5g", "remotePath": float64(0), "lane": float64(256), "up": true, "discovering": true,
		"targetBps": float64(1000000), "sendBps": float64(800000), "deliveryBps": float64(720000), "capacityBps": float64(1040000),
		"rttSeconds": 0.06, "queueDelaySeconds": 0.037, "thresholdSeconds": 0.03,
		"inFlightBytes": float64(5000), "windowBytes": float64(30000), "sentBytes": float64(11), "ackedBytes": float64(10), "repairs": float64(12),
		"delaySignals": float64(21), "lossSignals": float64(22), "discoveryCongested": float64(23), "discoveryPlateau": float64(24),
		"capacityRemeasured": float64(25), "capacityDecays": float64(26), "pulses": float64(27), "pulseWins": float64(28),
		"pulseLosses": float64(29), "rediscoveries": float64(30), "stallSignals": float64(31),
	}
	if len(got.Lanes) != 1 || !reflect.DeepEqual(got.Lanes[0], wantLane) {
		t.Errorf("lanes = %v\nwant   [%v]", got.Lanes, wantLane)
	}
	wantTransport := map[string]any{
		"peer": "hub", "queueDrops": float64(9), "admissionDrops": float64(1), "aqmDrops": float64(2), "interactiveDrops": float64(3),
		"interactiveQueued": float64(4), "expired": float64(5), "duplicates": float64(6), "coalescedAcks": float64(7),
	}
	if len(got.Transport) != 1 || !reflect.DeepEqual(got.Transport[0], wantTransport) {
		t.Errorf("transport = %v\nwant        [%v]", got.Transport, wantTransport)
	}
}
