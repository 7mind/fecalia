package device

import (
	"strings"
	"testing"

	"github.com/7mind/wanbond/internal/config"
)

// TestUapiConfigSplitsDefaultRoute pins D35/I6: uapiConfig must NEVER emit the
// literal all-routes /0 prefix to the engine (amneziawg-go wedges the
// handshake on it) — a configured 0.0.0.0/0 or ::/0 allowed_ip renders as
// exactly the equivalent /1+/1 pair instead. An ordinary CIDR passes through
// unchanged. If the split were removed, the literal-/0 lines this test
// asserts are ABSENT would reappear and the two-/1-line assertions would fail
// (mutation-verified).
func TestUapiConfigSplitsDefaultRoute(t *testing.T) {
	privRaw, _ := genX25519(t)
	_, pubRaw := genX25519(t)

	cfg := &config.Config{
		Role: config.RoleEdge,
		WireGuard: config.WireGuard{
			PrivateKey: keyFromRaw(t, privRaw),
			Peers: []config.Peer{
				{
					PublicKey:  keyFromRaw(t, pubRaw),
					AllowedIPs: []string{"0.0.0.0/0", "::/0", "10.0.0.0/24"},
					Mode:       config.PeerModeDefaultRoute,
				},
			},
		},
	}
	boot := []bootEndpoint{{}}

	got, err := uapiConfig(cfg, boot)
	if err != nil {
		t.Fatalf("uapiConfig: %v", err)
	}

	// The literal /0 forms must NEVER appear in the rendered set string.
	for _, literal := range []string{"allowed_ip=0.0.0.0/0\n", "allowed_ip=::/0\n"} {
		if strings.Contains(got, literal) {
			t.Fatalf("uapiConfig leaked the literal /0 prefix %q into the UAPI set string:\n%s", literal, got)
		}
	}

	// Exactly the split /1+/1 pairs must appear instead, plus the untouched
	// ordinary CIDR.
	for _, want := range []string{
		"allowed_ip=0.0.0.0/1\n",
		"allowed_ip=128.0.0.0/1\n",
		"allowed_ip=::/1\n",
		"allowed_ip=8000::/1\n",
		"allowed_ip=10.0.0.0/24\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("uapiConfig missing expected line %q in:\n%s", want, got)
		}
	}
}

// TestUapiConfigDefaultRouteSplitUnconditional pins that the /0 split applies
// regardless of the peer's mode (T107 is the render-only surface; mode is a
// separate, edge-only config concern — see config.PeerMode).
func TestUapiConfigDefaultRouteSplitUnconditional(t *testing.T) {
	privRaw, _ := genX25519(t)
	_, pubRaw := genX25519(t)

	cfg := &config.Config{
		Role: config.RoleEdge,
		WireGuard: config.WireGuard{
			PrivateKey: keyFromRaw(t, privRaw),
			Peers: []config.Peer{
				{
					PublicKey:  keyFromRaw(t, pubRaw),
					AllowedIPs: []string{"0.0.0.0/0"},
				},
			},
		},
	}
	boot := []bootEndpoint{{}}

	got, err := uapiConfig(cfg, boot)
	if err != nil {
		t.Fatalf("uapiConfig: %v", err)
	}
	if strings.Contains(got, "allowed_ip=0.0.0.0/0\n") {
		t.Fatalf("uapiConfig leaked the literal /0 prefix for a peer with mode unset:\n%s", got)
	}
	if !strings.Contains(got, "allowed_ip=0.0.0.0/1\n") || !strings.Contains(got, "allowed_ip=128.0.0.0/1\n") {
		t.Fatalf("uapiConfig did not split the /0 for a peer with mode unset:\n%s", got)
	}
}
