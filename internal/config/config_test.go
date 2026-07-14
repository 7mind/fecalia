package config

import (
	"encoding/base64"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// testKey returns a syntactically valid base64 32-byte key for fixtures.
func testKey(b byte) string {
	raw := make([]byte, keyLen)
	for i := range raw {
		raw[i] = b
	}
	return base64.StdEncoding.EncodeToString(raw)
}

// writeConfig writes body to a temp file with the given mode and returns its path.
func writeConfig(t *testing.T, mode os.FileMode, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "wanbond.toml")
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	// os.WriteFile is subject to umask; force the exact bits.
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod fixture: %v", err)
	}
	return path
}

// Root-level scalars (role, psk) must precede any TOML table header.
const edgeConfig = `
role = "edge"
psk = "%PSK%"

[[paths]]
name = "starlink"
source_addr = "192.0.2.10"

[[paths]]
name = "cellular"
source_addr = "192.0.2.20"

[wireguard]
private_key = "%PRIV%"
[[wireguard.peers]]
public_key = "%PUB%"
endpoint = "203.0.113.5:51820"
allowed_ips = ["0.0.0.0/0"]

[amnezia]
jc = 4
jmin = 8
jmax = 80
s1 = 15
s2 = 92

[metrics]
listen = "127.0.0.1:9095"

[log]
level = "info"
`

const concentratorConfig = `
role = "concentrator"
psk = "%PSK%"

[[paths]]
name = "wan"
source_addr = "203.0.113.5"

[wireguard]
private_key = "%PRIV%"
listen_port = 51820
[[wireguard.peers]]
public_key = "%PUB%"
allowed_ips = ["10.0.0.2/32"]
`

func fill(tmpl string) string {
	r := strings.NewReplacer("%PRIV%", testKey(1), "%PUB%", testKey(2), "%PSK%", testKey(3))
	return r.Replace(tmpl)
}

func TestLoadValidEdge(t *testing.T) {
	path := writeConfig(t, 0o600, fill(edgeConfig))
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Role != RoleEdge {
		t.Errorf("role = %q, want edge", c.Role)
	}
	if len(c.Paths) != 2 {
		t.Fatalf("paths = %d, want 2", len(c.Paths))
	}
	if got := c.Paths[0].SourceAddr.String(); got != "192.0.2.10" {
		t.Errorf("path0 source_addr = %q, want 192.0.2.10", got)
	}
	if !c.WireGuard.PrivateKey.IsSet() || !c.PSK.IsSet() {
		t.Error("private_key and psk must be set")
	}
	if c.Amnezia.Jmax != 80 {
		t.Errorf("amnezia jmax = %d, want 80", c.Amnezia.Jmax)
	}
	// The fixture omits h1-h4, so Load must default them to the standard 1..4
	// message-type headers (defect D1), never leave them at 0.
	if got := [4]uint32{c.Amnezia.H1, c.Amnezia.H2, c.Amnezia.H3, c.Amnezia.H4}; got != [4]uint32{1, 2, 3, 4} {
		t.Errorf("amnezia magic headers = %v, want defaulted [1 2 3 4]", got)
	}
}

func TestLoadValidConcentrator(t *testing.T) {
	path := writeConfig(t, 0o600, fill(concentratorConfig))
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Role != RoleConcentrator {
		t.Errorf("role = %q, want concentrator", c.Role)
	}
	if c.WireGuard.ListenPort != 51820 {
		t.Errorf("listen_port = %d, want 51820", c.WireGuard.ListenPort)
	}
}

// TestLoadEndpointSingleBackwardCompat is the T54 backward-compat case: a bare
// `endpoint = "..."` (the pre-T54 config surface) must normalize to a
// ONE-ELEMENT Endpoints list holding exactly that address — behavior-identical
// to the old single defaultRemote, and the shape T57's hub-failover switch
// consumes (Endpoints[0] is always the active concentrator).
func TestLoadEndpointSingleBackwardCompat(t *testing.T) {
	path := writeConfig(t, 0o600, fill(edgeConfig))
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	peer := c.WireGuard.Peers[0]
	if got := len(peer.Endpoints); got != 1 {
		t.Fatalf("Endpoints length = %d, want 1 (single endpoint form)", got)
	}
	if got := peer.Endpoints[0].String(); got != "203.0.113.5:51820" {
		t.Errorf("Endpoints[0] = %q, want %q", got, "203.0.113.5:51820")
	}
}

// TestLoadEndpointsOrderedList is the T54 core case (Q18 edge-side ordered-
// endpoint active-standby): a multi-entry `endpoints` list must parse to an
// Endpoints slice preserving TOML order, index 0 = the active/primary
// concentrator and the rest ordered standbys — the shape T57 consumes to pick
// the next endpoint on hub loss.
func TestLoadEndpointsOrderedList(t *testing.T) {
	body := fill(strings.Replace(edgeConfig, `endpoint = "203.0.113.5:51820"`,
		`endpoints = ["203.0.113.5:51820", "198.51.100.7:51820", "203.0.113.9:51821"]`, 1))
	path := writeConfig(t, 0o600, body)
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	peer := c.WireGuard.Peers[0]
	want := []string{"203.0.113.5:51820", "198.51.100.7:51820", "203.0.113.9:51821"}
	if got := len(peer.Endpoints); got != len(want) {
		t.Fatalf("Endpoints length = %d, want %d", got, len(want))
	}
	for i, w := range want {
		if got := peer.Endpoints[i].String(); got != w {
			t.Errorf("Endpoints[%d] = %q, want %q (order must be preserved: index 0 is active, the rest ordered standbys)", i, got, w)
		}
	}
	// The legacy single-endpoint field must stay unset when the ordered list form
	// is used, so a caller cannot accidentally read a stale single Endpoint.
	if peer.Endpoint != "" {
		t.Errorf("Endpoint = %q, want empty when endpoints list is used", peer.Endpoint)
	}
}

// mustKey decodes b's fixture base64 into a Key the same way TOML unmarshaling
// would, for building golden expected-struct values in tests.
func mustKey(t *testing.T, b byte) Key {
	t.Helper()
	var k Key
	if err := k.UnmarshalText([]byte(testKey(b))); err != nil {
		t.Fatalf("mustKey: %v", err)
	}
	return k
}

// twoPeerConcentratorConfig (T80) is a concentrator config with two peers, each
// carrying a distinct per-peer psk and name — the G4 multi-peer groundwork
// surface. The top-level psk is still required (validate) and stays the
// single-peer default; per-peer psk/name here are parsed and exposed only (no
// datapath change in T80).
const twoPeerConcentratorConfig = `
role = "concentrator"
psk = "%PSK%"

[[paths]]
name = "wan"
source_addr = "203.0.113.5"

[wireguard]
private_key = "%PRIV%"
listen_port = 51820

[[wireguard.peers]]
public_key = "%PUB1%"
psk = "%PEERPSK1%"
name = "edge-alpha"
allowed_ips = ["10.0.0.2/32"]

[[wireguard.peers]]
public_key = "%PUB2%"
psk = "%PEERPSK2%"
name = "edge-beta"
allowed_ips = ["10.0.0.3/32"]
`

// TestLoadPeerPSKName is the T80 core case: a 2-peer TOML with distinct
// per-peer psk+name values must expose each peer's own psk and name.
func TestLoadPeerPSKName(t *testing.T) {
	r := strings.NewReplacer(
		"%PRIV%", testKey(1),
		"%PUB1%", testKey(2),
		"%PSK%", testKey(3),
		"%PUB2%", testKey(4),
		"%PEERPSK1%", testKey(5),
		"%PEERPSK2%", testKey(6),
	)
	body := r.Replace(twoPeerConcentratorConfig)
	path := writeConfig(t, 0o600, body)
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := len(c.WireGuard.Peers); got != 2 {
		t.Fatalf("peers = %d, want 2", got)
	}
	p0, p1 := c.WireGuard.Peers[0], c.WireGuard.Peers[1]
	if p0.Name != "edge-alpha" {
		t.Errorf("peer 0 name = %q, want %q", p0.Name, "edge-alpha")
	}
	if p1.Name != "edge-beta" {
		t.Errorf("peer 1 name = %q, want %q", p1.Name, "edge-beta")
	}
	if !p0.PSK.IsSet() || p0.PSK.Bytes() != mustKey(t, 5).Bytes() {
		t.Errorf("peer 0 psk does not match fixture value")
	}
	if !p1.PSK.IsSet() || p1.PSK.Bytes() != mustKey(t, 6).Bytes() {
		t.Errorf("peer 1 psk does not match fixture value")
	}
	if p0.PSK.Bytes() == p1.PSK.Bytes() {
		t.Error("peer psks must be distinct per fixture, got equal")
	}
	// The top-level psk stays the single-peer default, unaffected by per-peer psk.
	if !c.PSK.IsSet() || c.PSK.Bytes() != mustKey(t, 3).Bytes() {
		t.Errorf("top-level psk does not match fixture value")
	}
}

// TestLoadSinglePeerLegacyPSKGoldenShape is the T80 backward-compat case: a
// legacy single-peer config carrying only the top-level psk (no per-peer
// psk/name) must parse to a Config byte-identical (golden struct compare via
// reflect.DeepEqual) to what a hand-built expected value predicts — in
// particular the new Peer.PSK/Peer.Name fields stay at their zero (unset)
// value and every pre-existing field keeps its pre-T80 shape.
func TestLoadSinglePeerLegacyPSKGoldenShape(t *testing.T) {
	path := writeConfig(t, 0o600, fill(concentratorConfig))
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	want := &Config{
		Role: RoleConcentrator,
		Paths: []Path{
			{
				Name:          "wan",
				SourceAddr:    netip.MustParseAddr("203.0.113.5"),
				SourceAddrRaw: "203.0.113.5",
				// Bind is always resolved by normalize() to the effective mode
				// (I5, Q42); the fixture sets neither the global nor per-path
				// bind, so it defaults all the way to BindModeAuto.
				Bind: BindModeAuto,
			},
		},
		WireGuard: WireGuard{
			PrivateKey: mustKey(t, 1),
			ListenPort: 51820,
			Peers: []Peer{
				{
					PublicKey:  mustKey(t, 2),
					AllowedIPs: []string{"10.0.0.2/32"},
					Endpoints:  []netip.AddrPort{},
					// resolveEndpoints (T67) always assigns EndpointSpecs to a
					// non-nil slice; a concentrator peer with no endpoint entries
					// parses to the empty (non-nil) slice, not the nil zero value.
					EndpointSpecs: []EndpointSpec{},
					// PSK and Name are intentionally left at their zero value:
					// the fixture sets neither, and normalize() must not
					// backfill Peer.PSK from the top-level PSK.
				},
			},
		},
		PSK: mustKey(t, 3),
		Scheduler: SchedulerConfig{
			Policy: PolicyActiveBackup, // applyDefaults always fills this in.
		},
		DNS: DNS{
			// applyDefaults always fills these in for an absent [dns] block.
			Resolver:     DNSResolverSystem,
			PollInterval: defaultDNSPollInterval,
			Timeout:      defaultDNSTimeout,
		},
		// Bind is always resolved by normalize() to the effective global default
		// (I5, Q42); the fixture omits it, so it defaults to BindModeAuto.
		Bind: BindModeAuto,
	}

	if !reflect.DeepEqual(c, want) {
		t.Errorf("parsed config does not match golden shape:\ngot:  %#v\nwant: %#v", c, want)
	}
	if c.WireGuard.Peers[0].PSK.IsSet() {
		t.Error("legacy single-peer config must not have per-peer psk set")
	}
	if c.WireGuard.Peers[0].Name != "" {
		t.Error("legacy single-peer config must not have per-peer name set")
	}
}

// twoPeerConcentratorTemplate is a parameterized variant of
// twoPeerConcentratorConfig for the T81 per-peer psk/name validation table:
// %NAME0%/%PSK0%/%NAME1%/%PSK1% let each case independently vary name and psk
// to exercise the presence/uniqueness/distinctness rules. An empty %PSK1%/
// %NAME0%/etc. renders an empty TOML string value, which the decoder leaves
// at the unset zero value — the same as omitting the key entirely.
const twoPeerConcentratorTemplate = `
role = "concentrator"
psk = "%TOPPSK%"

[[paths]]
name = "wan"
source_addr = "203.0.113.5"

[wireguard]
private_key = "%PRIV%"
listen_port = 51820

[[wireguard.peers]]
public_key = "%PUB1%"
name = "%NAME0%"
psk = "%PSK0%"
allowed_ips = ["10.0.0.2/32"]

[[wireguard.peers]]
public_key = "%PUB2%"
name = "%NAME1%"
psk = "%PSK1%"
allowed_ips = ["10.0.0.3/32"]
`

func fillTwoPeer(name0, psk0, name1, psk1 string) string {
	r := strings.NewReplacer(
		"%PRIV%", testKey(1),
		"%PUB1%", testKey(2),
		"%TOPPSK%", testKey(3),
		"%PUB2%", testKey(4),
		"%NAME0%", name0,
		"%PSK0%", psk0,
		"%NAME1%", name1,
		"%PSK1%", psk1,
	)
	return r.Replace(twoPeerConcentratorTemplate)
}

// edgeTwoPeersConfig is an edge config with two wireguard peers, used only to
// exercise the Q21 concentrator-only-scope rejection (the edge dials exactly
// one concentrator peer per process).
const edgeTwoPeersConfig = `
role = "edge"
psk = "%PSK%"

[[paths]]
name = "starlink"
source_addr = "192.0.2.10"

[wireguard]
private_key = "%PRIV%"
[[wireguard.peers]]
public_key = "%PUB%"
endpoint = "203.0.113.5:51820"
allowed_ips = ["0.0.0.0/0"]
[[wireguard.peers]]
public_key = "%PUB2%"
endpoint = "203.0.113.9:51820"
allowed_ips = ["0.0.0.0/0"]
`

// TestPeerPSKAndNameValidation is the T81 acceptance table for Q21 multi-peer
// concentrator validation: with more than one peer, each peer's psk must be
// present and pairwise-distinct (equal psks defeat authenticated demux) and
// each peer's name must be present and unique; with exactly one peer, the
// top-level psk is the default and a per-peer psk must be absent; an edge
// role config is rejected outright with more than one peer (concentrator-only
// scope, Q21).
func TestPeerPSKAndNameValidation(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantErr string // empty means Load must succeed
	}{
		{
			name:    "more than one peer with equal per-peer psks fails",
			body:    fillTwoPeer("peer-a", testKey(5), "peer-b", testKey(5)),
			wantErr: "pairwise distinct",
		},
		{
			name:    "more than one peer with a missing per-peer psk fails",
			body:    fillTwoPeer("peer-a", testKey(5), "peer-b", ""),
			wantErr: "psk is required",
		},
		{
			name:    "duplicate peer names fail",
			body:    fillTwoPeer("peer-a", testKey(5), "peer-a", testKey(6)),
			wantErr: "must be unique",
		},
		{
			name:    "edge role with 2 peers fails with a scope-explaining message",
			body:    strings.NewReplacer("%PRIV%", testKey(1), "%PUB%", testKey(2), "%PSK%", testKey(3), "%PUB2%", testKey(4)).Replace(edgeTwoPeersConfig),
			wantErr: "concentrator-only",
		},
		{
			name:    "single-peer top-level-only passes",
			body:    fill(concentratorConfig),
			wantErr: "",
		},
		{
			name:    "2 peers with distinct psks and names pass",
			body:    fillTwoPeer("peer-a", testKey(5), "peer-b", testKey(6)),
			wantErr: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeConfig(t, 0o600, tc.body)
			_, err := Load(path)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Load: unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tc.wantErr)
			}
		})
	}
}

// TestPeerIdentitiesSinglePeerUsesTopLevelPSK is the T82 single-peer back-compat
// case: a single-peer config's PeerIdentities must report the top-level
// Config.PSK as the effective PSK, and a stable fallback name/id since the
// legacy fixture sets no per-peer name.
func TestPeerIdentitiesSinglePeerUsesTopLevelPSK(t *testing.T) {
	path := writeConfig(t, 0o600, fill(concentratorConfig))
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	ids := c.PeerIdentities()
	if got := len(ids); got != 1 {
		t.Fatalf("PeerIdentities() len = %d, want 1", got)
	}
	if ids[0].PSK.Bytes() != mustKey(t, 3).Bytes() {
		t.Errorf("single-peer PeerIdentities()[0].PSK does not match top-level psk")
	}
	if ids[0].Name == "" {
		t.Error("single-peer PeerIdentities()[0].Name must be non-empty (fallback id) when peer.name is unset")
	}
}

// TestPeerIdentitiesSinglePeerTopLevelPSKWinsOverDistinctPeerPSK pins the
// distinguishing case of the single-peer back-compat rule: PeerIdentities'
// doc comment claims the top-level Config.PSK wins "regardless of whether a
// per-peer psk is also set". TestPeerIdentitiesSinglePeerUsesTopLevelPSK alone
// does not pin this — its fixture carries no per-peer psk, so an
// implementation that preferred p.PSK when set would still pass. Here the
// single peer carries a psk DISTINCT from the top-level one; PeerIdentities
// must still report the top-level value.
//
// validate() (owned by T81) now rejects a per-peer psk when exactly one peer
// is configured, so this shape is no longer loadable via config.Load. The
// helper is a pure function over Config, so the Config struct is built
// directly here, bypassing Load/validate, to pin the invariant defensively.
func TestPeerIdentitiesSinglePeerTopLevelPSKWinsOverDistinctPeerPSK(t *testing.T) {
	topLevelPSK := mustKey(t, 3)
	distinctPeerPSK := mustKey(t, 5)
	if topLevelPSK.Bytes() == distinctPeerPSK.Bytes() {
		t.Fatal("fixture setup: topLevelPSK and distinctPeerPSK must differ")
	}

	c := Config{
		PSK: topLevelPSK,
		WireGuard: WireGuard{
			Peers: []Peer{
				{
					PublicKey: mustKey(t, 2),
					PSK:       distinctPeerPSK,
				},
			},
		},
	}

	ids := c.PeerIdentities()
	if got := len(ids); got != 1 {
		t.Fatalf("PeerIdentities() len = %d, want 1", got)
	}
	if ids[0].PSK.Bytes() != topLevelPSK.Bytes() {
		t.Error("single-peer PeerIdentities()[0].PSK must be the top-level psk even when a distinct per-peer psk is also set")
	}
	if ids[0].PSK.Bytes() == distinctPeerPSK.Bytes() {
		t.Error("single-peer PeerIdentities()[0].PSK must NOT be the per-peer psk")
	}
}

// TestPeerIdentitiesMultiPeerUsesOwnPSKAndName is the T82 multi-peer case: each
// peer's PeerIdentities entry must report that peer's OWN psk and name, in the
// order matching cfg.WireGuard.Peers.
func TestPeerIdentitiesMultiPeerUsesOwnPSKAndName(t *testing.T) {
	r := strings.NewReplacer(
		"%PRIV%", testKey(1),
		"%PUB1%", testKey(2),
		"%PSK%", testKey(3),
		"%PUB2%", testKey(4),
		"%PEERPSK1%", testKey(5),
		"%PEERPSK2%", testKey(6),
	)
	body := r.Replace(twoPeerConcentratorConfig)
	path := writeConfig(t, 0o600, body)
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	ids := c.PeerIdentities()
	if got := len(ids); got != 2 {
		t.Fatalf("PeerIdentities() len = %d, want 2", got)
	}
	// Order matches cfg.WireGuard.Peers.
	if ids[0].Name != "edge-alpha" {
		t.Errorf("PeerIdentities()[0].Name = %q, want %q", ids[0].Name, "edge-alpha")
	}
	if ids[1].Name != "edge-beta" {
		t.Errorf("PeerIdentities()[1].Name = %q, want %q", ids[1].Name, "edge-beta")
	}
	if ids[0].PSK.Bytes() != mustKey(t, 5).Bytes() {
		t.Errorf("PeerIdentities()[0].PSK does not match peer 0's own psk fixture value")
	}
	if ids[1].PSK.Bytes() != mustKey(t, 6).Bytes() {
		t.Errorf("PeerIdentities()[1].PSK does not match peer 1's own psk fixture value")
	}
	if ids[0].PSK.Bytes() == ids[1].PSK.Bytes() {
		t.Error("multi-peer PeerIdentities PSKs must be distinct per fixture, got equal")
	}
	// The top-level psk must NOT leak into a multi-peer identity.
	if ids[0].PSK.Bytes() == mustKey(t, 3).Bytes() {
		t.Error("multi-peer PeerIdentities()[0].PSK must not equal the top-level psk")
	}
}

// TestLoadEndpointAllLiteralPopulatesEndpointSpecs is the T67 byte-for-byte
// invariant (Q29): an all-IP-literal config must take EXACTLY today's code
// path. EndpointSpecs must mirror Endpoints one-for-one (IsName=false, Addr
// set), on top of Endpoints staying populated exactly as before T67 (already
// covered by TestLoadEndpointsOrderedList).
func TestLoadEndpointAllLiteralPopulatesEndpointSpecs(t *testing.T) {
	body := fill(strings.Replace(edgeConfig, `endpoint = "203.0.113.5:51820"`,
		`endpoints = ["203.0.113.5:51820", "198.51.100.7:51820"]`, 1))
	path := writeConfig(t, 0o600, body)
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	peer := c.WireGuard.Peers[0]
	want := []string{"203.0.113.5:51820", "198.51.100.7:51820"}
	if got := len(peer.EndpointSpecs); got != len(want) {
		t.Fatalf("EndpointSpecs length = %d, want %d", got, len(want))
	}
	for i, w := range want {
		spec := peer.EndpointSpecs[i]
		if spec.IsName {
			t.Errorf("EndpointSpecs[%d].IsName = true, want false for literal %q", i, w)
		}
		if got := spec.Addr.String(); got != w {
			t.Errorf("EndpointSpecs[%d].Addr = %q, want %q", i, got, w)
		}
	}
}

// TestLoadEndpointHostnameWithDNSOptIn covers acceptance cases (1) and (3): a
// hostname entry with the peer's dns = true opt-in parses into an EndpointSpec
// with IsName=true, Host/Port set, and a mixed list of literals and names
// preserves TOML order in EndpointSpecs. Endpoints (the literal/resolved
// snapshot T57 consumes) holds only the literal entries — Q30 defers hostname
// resolution to runtime, so a name contributes nothing there at load time.
func TestLoadEndpointHostnameWithDNSOptIn(t *testing.T) {
	body := fill(strings.Replace(edgeConfig, `endpoint = "203.0.113.5:51820"`,
		"dns = true\n"+`endpoints = ["203.0.113.5:51820", "concentrator.example.com:51820", "198.51.100.7:51821"]`, 1))
	path := writeConfig(t, 0o600, body)
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	peer := c.WireGuard.Peers[0]
	if got := len(peer.EndpointSpecs); got != 3 {
		t.Fatalf("EndpointSpecs length = %d, want 3", got)
	}
	if spec := peer.EndpointSpecs[0]; spec.IsName || spec.Addr.String() != "203.0.113.5:51820" {
		t.Errorf("EndpointSpecs[0] = %+v, want literal 203.0.113.5:51820", spec)
	}
	if spec := peer.EndpointSpecs[1]; !spec.IsName || spec.Host != "concentrator.example.com" || spec.Port != 51820 {
		t.Errorf("EndpointSpecs[1] = %+v, want hostname concentrator.example.com:51820", spec)
	}
	if spec := peer.EndpointSpecs[2]; spec.IsName || spec.Addr.String() != "198.51.100.7:51821" {
		t.Errorf("EndpointSpecs[2] = %+v, want literal 198.51.100.7:51821", spec)
	}
	// The hostname entry contributes nothing to Endpoints at load time (Q30): only
	// the two literal entries, in order, show up there.
	want := []string{"203.0.113.5:51820", "198.51.100.7:51821"}
	if got := len(peer.Endpoints); got != len(want) {
		t.Fatalf("Endpoints length = %d, want %d", got, len(want))
	}
	for i, w := range want {
		if got := peer.Endpoints[i].String(); got != w {
			t.Errorf("Endpoints[%d] = %q, want %q", i, got, w)
		}
	}
}

func TestLoadRejects(t *testing.T) {
	cases := []struct {
		name string
		mode os.FileMode
		body string
		want string
	}{
		{
			name: "insecure mode",
			mode: 0o644,
			body: fill(edgeConfig),
			want: "insecure permissions",
		},
		{
			name: "missing role",
			mode: 0o600,
			body: fill(strings.Replace(edgeConfig, `role = "edge"`, "", 1)),
			want: "role must be",
		},
		{
			name: "unknown role",
			mode: 0o600,
			body: fill(strings.Replace(edgeConfig, `role = "edge"`, `role = "relay"`, 1)),
			want: "role must be",
		},
		{
			name: "empty paths",
			mode: 0o600,
			body: fill(concentratorConfig[:strings.Index(concentratorConfig, "[[paths]]")] +
				concentratorConfig[strings.Index(concentratorConfig, "[wireguard]"):]),
			want: "at least one path",
		},
		{
			name: "malformed key",
			mode: 0o600,
			body: fill(strings.Replace(edgeConfig, "%PRIV%", "not-base64!!", 1)),
			want: "base64",
		},
		{
			name: "wrong key length",
			mode: 0o600,
			body: fill(strings.Replace(edgeConfig, "%PRIV%", base64.StdEncoding.EncodeToString([]byte("short")), 1)),
			want: "decode to 32 bytes",
		},
		{
			name: "edge peer without endpoint",
			mode: 0o600,
			body: fill(strings.Replace(edgeConfig, `endpoint = "203.0.113.5:51820"`, "", 1)),
			want: "endpoint is required",
		},
		{
			name: "edge peer endpoint and endpoints both set",
			mode: 0o600,
			body: fill(strings.Replace(edgeConfig, `endpoint = "203.0.113.5:51820"`,
				"endpoint = \"203.0.113.5:51820\"\nendpoints = [\"198.51.100.1:51820\"]", 1)),
			want: "mutually exclusive",
		},
		{
			name: "edge peer endpoints unparseable entry",
			mode: 0o600,
			body: fill(strings.Replace(edgeConfig, `endpoint = "203.0.113.5:51820"`,
				`endpoints = ["203.0.113.5:51820", "not-a-host-port"]`, 1)),
			want: "invalid endpoint",
		},
		{
			name: "edge peer endpoints duplicate entry",
			mode: 0o600,
			body: fill(strings.Replace(edgeConfig, `endpoint = "203.0.113.5:51820"`,
				`endpoints = ["203.0.113.5:51820", "203.0.113.5:51820"]`, 1)),
			want: "duplicate endpoint",
		},
		{
			name: "edge peer endpoints empty list",
			mode: 0o600,
			body: fill(strings.Replace(edgeConfig, `endpoint = "203.0.113.5:51820"`,
				`endpoints = []`, 1)),
			want: "endpoint is required",
		},
		{
			name: "concentrator peer declares endpoints (edge-only surface)",
			mode: 0o600,
			body: fill(strings.Replace(concentratorConfig, `allowed_ips = ["10.0.0.2/32"]`,
				"allowed_ips = [\"10.0.0.2/32\"]\nendpoints = [\"203.0.113.5:51820\"]", 1)),
			want: "not meaningful for the concentrator role",
		},
		{
			// Acceptance case (2): a hostname entry without the peer's dns = true
			// opt-in fails Load with an error naming the flag (Q29 default-off
			// DPI posture).
			name: "edge peer hostname endpoint without dns opt-in",
			mode: 0o600,
			body: fill(strings.Replace(edgeConfig, `endpoint = "203.0.113.5:51820"`,
				`endpoint = "concentrator.example.com:51820"`, 1)),
			want: "dns = true",
		},
		{
			// Acceptance case (4): duplicate detection extends to hostname entries
			// — the same host:port twice is rejected, same error shape as a
			// duplicate IP literal.
			name: "edge peer duplicate hostname endpoint",
			mode: 0o600,
			body: fill(strings.Replace(edgeConfig, `endpoint = "203.0.113.5:51820"`,
				"dns = true\n"+`endpoints = ["concentrator.example.com:51820", "concentrator.example.com:51820"]`, 1)),
			want: "duplicate endpoint",
		},
		{
			// Acceptance case (5): the dns opt-in is edge-only, mirroring the
			// endpoints-not-meaningful-for-concentrator rule, even when the
			// concentrator declares no endpoint/endpoints entries at all.
			name: "concentrator peer declares dns opt-in (edge-only surface)",
			mode: 0o600,
			body: fill(strings.Replace(concentratorConfig, `allowed_ips = ["10.0.0.2/32"]`,
				"allowed_ips = [\"10.0.0.2/32\"]\ndns = true", 1)),
			want: "not meaningful for the concentrator role",
		},
		{
			name: "concentrator without listen_port",
			mode: 0o600,
			body: fill(strings.Replace(concentratorConfig, "listen_port = 51820", "", 1)),
			want: "listen_port is required",
		},
		{
			name: "missing psk",
			mode: 0o600,
			body: fill(strings.Replace(edgeConfig, "psk = \"%PSK%\"", "", 1)),
			want: "psk is required",
		},
		{
			name: "fec enabled without data_shards",
			mode: 0o600,
			body: fill(edgeConfig) + "\n[fec]\nenabled = true\nparity_shards = 2\n",
			want: "fec.data_shards must be >= 1",
		},
		{
			name: "fec enabled without parity_shards",
			mode: 0o600,
			body: fill(edgeConfig) + "\n[fec]\nenabled = true\ndata_shards = 8\n",
			want: "fec.parity_shards must be >= 1",
		},
		{
			name: "fec ratio exceeds field limit",
			mode: 0o600,
			body: fill(edgeConfig) + "\n[fec]\nenabled = true\ndata_shards = 250\nparity_shards = 10\n",
			want: "Reed-Solomon field limit",
		},
		{
			name: "fec deadline exceeds resequencer budget",
			mode: 0o600,
			// go-toml/v2 decodes a duration as integer nanoseconds; 500ms = 5e8 ns > 125ms.
			body: fill(edgeConfig) + "\n[fec]\nenabled = true\ndata_shards = 8\nparity_shards = 3\ndeadline = 500000000\n",
			want: "fec.deadline must be <=",
		},
		{
			name: "fec adaptive without enabled",
			mode: 0o600,
			body: fill(edgeConfig) + "\n[fec]\nadaptive = true\n",
			want: "fec.adaptive = true requires fec.enabled = true",
		},
		{
			name: "fec target_residual out of range (>= 1)",
			mode: 0o600,
			body: fill(edgeConfig) + "\n[fec]\nenabled = true\nadaptive = true\ndata_shards = 10\nparity_shards = 6\ntarget_residual = 1.5\n",
			want: "fec.target_residual must be a finite value in (0,1)",
		},
		{
			name: "fec target_residual out of range (<= 0)",
			mode: 0o600,
			body: fill(edgeConfig) + "\n[fec]\nenabled = true\nadaptive = true\ndata_shards = 10\nparity_shards = 6\ntarget_residual = -0.01\n",
			want: "fec.target_residual must be a finite value in (0,1)",
		},
		{
			name: "fec target_residual non-finite (nan)",
			mode: 0o600,
			body: fill(edgeConfig) + "\n[fec]\nenabled = true\nadaptive = true\ndata_shards = 10\nparity_shards = 6\ntarget_residual = nan\n",
			want: "fec.target_residual must be a finite value in (0,1)",
		},
		{
			name: "fec target_residual and safety_factor both set",
			mode: 0o600,
			body: fill(edgeConfig) + "\n[fec]\nenabled = true\nadaptive = true\ndata_shards = 10\nparity_shards = 6\ntarget_residual = 0.005\nsafety_factor = 2.0\n",
			want: "mutually exclusive",
		},
		{
			name: "fec target_residual in fixed (non-adaptive) mode",
			mode: 0o600,
			body: fill(edgeConfig) + "\n[fec]\nenabled = true\ndata_shards = 10\nparity_shards = 6\ntarget_residual = 0.005\n",
			want: "only meaningful in adaptive mode",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeConfig(t, tc.mode, tc.body)
			_, err := Load(path)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tc.want)
			}
		})
	}
}

// TestLoadRejectsDuplicateSourceAddr is the D10 regression. validate() enforced
// unique path NAMES but not unique source_addr values; the multipath bind Opens
// each path on (source_addr, port), so two paths sharing a source_addr collide
// EADDRINUSE at the second ListenUDP (and on every re-Open after Down/Up, since the
// engine passes the fixed listen port back). It must be rejected at config LOAD with
// an error naming BOTH conflicting paths — not deferred to bring-up. A distinct
// source_addr per path still loads (TestLoadValidEdge covers the .10/.20 fixture).
func TestLoadRejectsDuplicateSourceAddr(t *testing.T) {
	// Point the cellular path at starlink's source_addr so the two collide.
	body := fill(strings.Replace(edgeConfig,
		"name = \"cellular\"\nsource_addr = \"192.0.2.20\"",
		"name = \"cellular\"\nsource_addr = \"192.0.2.10\"", 1))
	path := writeConfig(t, 0o600, body)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected duplicate source_addr to be rejected at load, got nil")
	}
	msg := err.Error()
	for _, want := range []string{"starlink", "cellular", "192.0.2.10"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q must name %q (both conflicting paths and the shared address)", msg, want)
		}
	}
}

// TestLoadRejectsDuplicateSourceAddrV4MappedV6 closes the D10 residual gap: the
// duplicate guard must catch two textual forms of the SAME address. "192.0.2.10"
// parses to an Is4 address and "::ffff:192.0.2.10" to an Is4In6 one — distinct
// under netip ==, but they bind the identical v4 socket and so collide EADDRINUSE
// at runtime. The guard compares the UNMAPPED form so the pair is rejected at load.
func TestLoadRejectsDuplicateSourceAddrV4MappedV6(t *testing.T) {
	// cellular uses the v4-mapped-v6 spelling of starlink's v4 source_addr.
	body := fill(strings.Replace(edgeConfig,
		"name = \"cellular\"\nsource_addr = \"192.0.2.20\"",
		"name = \"cellular\"\nsource_addr = \"::ffff:192.0.2.10\"", 1))
	path := writeConfig(t, 0o600, body)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected v4/v4-mapped-v6 source_addr collision to be rejected at load, got nil")
	}
	msg := err.Error()
	for _, want := range []string{"starlink", "cellular"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q must name conflicting path %q", msg, want)
		}
	}
}

// TestFECDefaultOff: omitting [fec] leaves FEC disabled so an existing config runs
// the pre-T24 datapath unchanged, and every FEC knob stays at its zero value.
func TestFECDefaultOff(t *testing.T) {
	path := writeConfig(t, 0o600, fill(edgeConfig))
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.FEC.Enabled {
		t.Fatal("FEC must default to disabled when [fec] is omitted")
	}
	if c.FEC.DataShards != 0 || c.FEC.ParityShards != 0 || c.FEC.Deadline != 0 {
		t.Fatalf("FEC knobs must stay zero when disabled, got %+v", c.FEC)
	}
}

// TestFECAdaptiveLoads: an enabled [fec] block with adaptive = true loads, keeps the
// ratio (now the controller's K / parity-ceiling), and reports adaptive on; fixed configs
// leave it off (default).
func TestFECAdaptiveLoads(t *testing.T) {
	body := fill(edgeConfig) + "\n[fec]\nenabled = true\nadaptive = true\ndata_shards = 10\nparity_shards = 6\n"
	path := writeConfig(t, 0o600, body)
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !c.FEC.Enabled || !c.FEC.Adaptive {
		t.Fatalf("adaptive FEC not loaded: %+v", c.FEC)
	}
	if c.FEC.DataShards != 10 || c.FEC.ParityShards != 6 {
		t.Fatalf("adaptive ratio not loaded: %+v", c.FEC)
	}

	// A fixed block leaves adaptive off.
	fixed := writeConfig(t, 0o600, fill(edgeConfig)+"\n[fec]\nenabled = true\ndata_shards = 10\nparity_shards = 6\n")
	cf, err := Load(fixed)
	if err != nil {
		t.Fatalf("Load fixed: %v", err)
	}
	if cf.FEC.Adaptive {
		t.Fatal("adaptive must default to off for a fixed [fec] block")
	}
}

// TestFECTargetResidualLoads: an adaptive [fec] block with target_residual set parses
// the SLA, leaves safety_factor inert (0, NOT defaulted), so the residual-SLA sizing
// mode (D26/T46) is the one the controller runs. A block with neither field keeps the
// safety_factor default (the legacy path), proving the two are mutually exclusive at load.
func TestFECTargetResidualLoads(t *testing.T) {
	body := fill(edgeConfig) + "\n[fec]\nenabled = true\nadaptive = true\ndata_shards = 10\nparity_shards = 6\ntarget_residual = 0.005\n"
	path := writeConfig(t, 0o600, body)
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.FEC.TargetResidual != 0.005 {
		t.Fatalf("target_residual = %g, want 0.005", c.FEC.TargetResidual)
	}
	if c.FEC.SafetyFactor != 0 {
		t.Fatalf("safety_factor = %g, want 0 (inert when target_residual governs)", c.FEC.SafetyFactor)
	}

	// With neither field the legacy safety_factor default fills in (not target_residual).
	legacy := writeConfig(t, 0o600, fill(edgeConfig)+"\n[fec]\nenabled = true\nadaptive = true\ndata_shards = 10\nparity_shards = 6\n")
	lc, err := Load(legacy)
	if err != nil {
		t.Fatalf("Load legacy: %v", err)
	}
	if lc.FEC.TargetResidual != 0 {
		t.Fatalf("legacy target_residual = %g, want 0", lc.FEC.TargetResidual)
	}
	if lc.FEC.SafetyFactor != defaultAdaptiveSafetyFactor {
		t.Fatalf("legacy safety_factor = %g, want defaulted %g", lc.FEC.SafetyFactor, defaultAdaptiveSafetyFactor)
	}
}

// TestFECEnabledDefaults: a minimal enabled [fec] block loads, keeps the given ratio,
// and defaults the group-close deadline so `enabled = true` with just a ratio is
// usable without hand-tuning the deadline.
func TestFECEnabledDefaults(t *testing.T) {
	body := fill(edgeConfig) + "\n[fec]\nenabled = true\ndata_shards = 8\nparity_shards = 3\n"
	path := writeConfig(t, 0o600, body)
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !c.FEC.Enabled || c.FEC.DataShards != 8 || c.FEC.ParityShards != 3 {
		t.Fatalf("FEC ratio not loaded: %+v", c.FEC)
	}
	if c.FEC.Deadline != defaultFECDeadline {
		t.Fatalf("FEC deadline = %s, want defaulted %s", c.FEC.Deadline, defaultFECDeadline)
	}
}

// TestSchedulerPolicyDefault: omitting [scheduler] defaults the policy to
// active-backup (P1 preserved) and leaves the weighted knobs inert/zero.
func TestSchedulerPolicyDefault(t *testing.T) {
	path := writeConfig(t, 0o600, fill(edgeConfig))
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Scheduler.Policy != PolicyActiveBackup {
		t.Fatalf("default scheduler policy = %q, want %q", c.Scheduler.Policy, PolicyActiveBackup)
	}
	if c.Scheduler.PerPathCapacityFPS != 0 {
		t.Fatalf("weighted knobs must stay zero under active-backup, got capacity %g", c.Scheduler.PerPathCapacityFPS)
	}
}

// TestSchedulerPolicyWeightedDefaults: a minimal weighted block loads and every
// omitted weighted knob is filled with its default (so `policy = "weighted"` alone
// is usable), forming a valid hysteresis band.
func TestSchedulerPolicyWeightedDefaults(t *testing.T) {
	body := fill(edgeConfig) + "\n[scheduler]\npolicy = \"weighted\"\n"
	path := writeConfig(t, 0o600, body)
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Scheduler.Policy != PolicyWeighted {
		t.Fatalf("policy = %q, want weighted", c.Scheduler.Policy)
	}
	if c.Scheduler.PerPathCapacityFPS <= 0 || c.Scheduler.EngageFraction <= 0 ||
		c.Scheduler.DisengageFraction >= c.Scheduler.EngageFraction || c.Scheduler.LoadTau <= 0 ||
		c.Scheduler.WeightRTTFloor <= 0 || c.Scheduler.WeightLossFloor <= 0 {
		t.Fatalf("weighted defaults not applied coherently: %+v", c.Scheduler)
	}
}

// TestSchedulerPolicyRejects: the weighted policy fails fast at load on an unknown
// policy or a non-hysteretic threshold band.
func TestSchedulerPolicyRejects(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "unknown policy",
			body: fill(edgeConfig) + "\n[scheduler]\npolicy = \"round-robin\"\n",
			want: "scheduler.policy must be",
		},
		{
			name: "disengage not below engage",
			body: fill(edgeConfig) + "\n[scheduler]\npolicy = \"weighted\"\nengage_fraction = 0.5\ndisengage_fraction = 0.6\n",
			want: "hysteresis band",
		},
		{
			name: "pacing enabled without burst",
			body: fill(edgeConfig) + "\n[scheduler]\npolicy = \"weighted\"\npacing_enabled = true\npacing_burst_frames = -1\n",
			want: "pacing_burst_frames must be > 0",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeConfig(t, 0o600, tc.body)
			_, err := Load(path)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tc.want)
			}
		})
	}
}

// TestBindModeDefaultAuto: a config that never mentions `bind` anywhere (no
// top-level default, no per-path override) resolves every path's effective
// bind mode to BindModeAuto (I5, Q42) — today's selectDeviceBinds behavior —
// so an existing config keeps its current bind behaviour byte-for-byte.
func TestBindModeDefaultAuto(t *testing.T) {
	path := writeConfig(t, 0o600, fill(edgeConfig))
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Bind != BindModeAuto {
		t.Fatalf("top-level bind default = %q, want %q", c.Bind, BindModeAuto)
	}
	for _, p := range c.Paths {
		if p.Bind != BindModeAuto {
			t.Errorf("path %q bind = %q, want defaulted %q", p.Name, p.Bind, BindModeAuto)
		}
	}
}

// TestBindModePerPathOverridesGlobal: a per-path `bind` wins over the
// top-level global default, and a path that omits `bind` still falls back to
// the (non-auto) global default rather than BindModeAuto.
func TestBindModePerPathOverridesGlobal(t *testing.T) {
	body := fill(edgeConfig)
	body = "bind = \"device\"\n" + body
	body = strings.Replace(body,
		"name = \"starlink\"\nsource_addr = \"192.0.2.10\"",
		"name = \"starlink\"\nsource_addr = \"192.0.2.10\"\nbind = \"source\"", 1)
	path := writeConfig(t, 0o600, body)
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Bind != BindModeDevice {
		t.Fatalf("top-level bind = %q, want %q", c.Bind, BindModeDevice)
	}
	if len(c.Paths) != 2 {
		t.Fatalf("paths = %d, want 2", len(c.Paths))
	}
	if c.Paths[0].Name != "starlink" || c.Paths[0].Bind != BindModeSource {
		t.Errorf("path[0] (%q) bind = %q, want per-path override %q", c.Paths[0].Name, c.Paths[0].Bind, BindModeSource)
	}
	if c.Paths[1].Name != "cellular" || c.Paths[1].Bind != BindModeDevice {
		t.Errorf("path[1] (%q) bind = %q, want inherited global default %q", c.Paths[1].Name, c.Paths[1].Bind, BindModeDevice)
	}
}

// TestBindModeRejectsUnknownValue: an unrecognized per-path `bind` value fails
// fast at load with a message naming the offending path.
func TestBindModeRejectsUnknownValue(t *testing.T) {
	body := fill(edgeConfig)
	body = strings.Replace(body,
		"name = \"cellular\"\nsource_addr = \"192.0.2.20\"",
		"name = \"cellular\"\nsource_addr = \"192.0.2.20\"\nbind = \"bogus\"", 1)
	path := writeConfig(t, 0o600, body)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected unknown bind value to be rejected at load, got nil")
	}
	if !strings.Contains(err.Error(), `path "cellular"`) || !strings.Contains(err.Error(), "bind must be") {
		t.Fatalf("error = %q, want it to name path \"cellular\" and say \"bind must be\"", err.Error())
	}
}

// TestBindModeRejectsUnknownGlobalValue: an unrecognized top-level `bind`
// default also fails fast at load.
func TestBindModeRejectsUnknownGlobalValue(t *testing.T) {
	body := "bind = \"bogus\"\n" + fill(edgeConfig)
	path := writeConfig(t, 0o600, body)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected unknown top-level bind value to be rejected at load, got nil")
	}
	if !strings.Contains(err.Error(), "bind must be") {
		t.Fatalf("error = %q, want substring %q", err.Error(), "bind must be")
	}
}
