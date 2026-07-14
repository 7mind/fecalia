package device

import (
	"net/netip"
	"testing"

	"github.com/7mind/wanbond/internal/config"
)

// TestDefaultRoutePrefixes pins the I6/Q41 route-set derivation: a peer marked
// mode=default-route contributes the wg-quick split of its allowed_ips (0.0.0.0/0 →
// the two /1s, ::/0 → its two /1s, an ordinary CIDR unchanged), REUSING the same
// splitDefaultRoute helper uapiConfig renders the engine allowed_ips with — while a
// peer WITHOUT the mode contributes nothing (the regression guard: no mode ⇒ no
// route). If defaultRoutePrefixes stopped gating on Mode, the no-mode cases below
// would gain prefixes; if it stopped splitting, the /0 cases would carry the literal
// /0 instead of the /1 pair (mutation-verified both ways).
func TestDefaultRoutePrefixes(t *testing.T) {
	pfx := func(ss ...string) []netip.Prefix {
		out := make([]netip.Prefix, len(ss))
		for i, s := range ss {
			out[i] = netip.MustParsePrefix(s)
		}
		return out
	}

	cases := []struct {
		name  string
		peers []config.Peer
		want  []netip.Prefix
	}{
		{
			name:  "default-route v4 /0 splits into the two /1s",
			peers: []config.Peer{{AllowedIPs: []string{"0.0.0.0/0"}, Mode: config.PeerModeDefaultRoute}},
			want:  pfx("0.0.0.0/1", "128.0.0.0/1"),
		},
		{
			name: "default-route dual-stack + ordinary CIDR: each allowed_ip split, in order",
			peers: []config.Peer{{
				AllowedIPs: []string{"0.0.0.0/0", "::/0", "10.0.0.0/24"},
				Mode:       config.PeerModeDefaultRoute,
			}},
			want: pfx("0.0.0.0/1", "128.0.0.0/1", "::/1", "8000::/1", "10.0.0.0/24"),
		},
		{
			name:  "no mode: nothing installed even with a 0.0.0.0/0 allowed_ip (regression guard)",
			peers: []config.Peer{{AllowedIPs: []string{"0.0.0.0/0"}}},
			want:  nil,
		},
		{
			name: "only the default-route peer contributes; a plain peer alongside is ignored",
			peers: []config.Peer{
				{AllowedIPs: []string{"10.1.0.0/24"}},
				{AllowedIPs: []string{"0.0.0.0/0"}, Mode: config.PeerModeDefaultRoute},
			},
			want: pfx("0.0.0.0/1", "128.0.0.0/1"),
		},
		{
			name:  "no peers at all: no routes",
			peers: nil,
			want:  nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{WireGuard: config.WireGuard{Peers: tc.peers}}
			got := defaultRoutePrefixes(cfg)
			if len(got) != len(tc.want) {
				t.Fatalf("defaultRoutePrefixes = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("prefix[%d] = %s, want %s (full: %v)", i, got[i], tc.want[i], got)
				}
			}
		})
	}
}
