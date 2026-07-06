package config

import (
	"encoding/base64"
	"os"
	"path/filepath"
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
