package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/netip"
)

// Role selects which end of the tunnel this process runs as. It is an explicit,
// required field; the role is never inferred from other configuration.
type Role string

const (
	// RoleEdge is the mobile Linux box that bonds the WAN uplinks.
	RoleEdge Role = "edge"
	// RoleConcentrator is the public-IP VPS that terminates the tunnel.
	RoleConcentrator Role = "concentrator"
)

func (r Role) valid() bool {
	return r == RoleEdge || r == RoleConcentrator
}

// keyLen is the byte length of a Curve25519 WireGuard key and of a raw
// outer-control PSK, before base64 encoding.
const keyLen = 32

// Config is the whole wanbond configuration, shared by both roles and parsed
// from a single TOML file.
type Config struct {
	Role      Role      `toml:"role"`
	Paths     []Path    `toml:"paths"`
	WireGuard WireGuard `toml:"wireguard"`
	Amnezia   Amnezia   `toml:"amnezia"`
	PSK       Key       `toml:"psk"`
	Metrics   Metrics   `toml:"metrics"`
	Log       Log       `toml:"log"`
}

// Path is one physical WAN uplink. The edge binds each path's UDP socket to
// SourceAddr so the upstream router pins it to the intended WAN; the
// concentrator learns the real per-path endpoints from authenticated traffic.
type Path struct {
	// Name is a stable human-readable identifier for the path (e.g. "starlink").
	Name string `toml:"name"`
	// SourceAddr is the local source IP the path's socket binds to.
	SourceAddr netip.Addr `toml:"-"`
	// SourceAddrRaw is the TOML string form of SourceAddr; parsed in normalize.
	SourceAddrRaw string `toml:"source_addr"`
}

// WireGuard holds the inner tunnel's key material.
type WireGuard struct {
	PrivateKey Key    `toml:"private_key"`
	Peers      []Peer `toml:"peers"`
	// ListenPort is the UDP port the concentrator listens on; 0 on the edge.
	ListenPort uint16 `toml:"listen_port"`
}

// Peer is one WireGuard peer.
type Peer struct {
	PublicKey Key `toml:"public_key"`
	// Endpoint is the peer's tunnel address (edge -> concentrator); empty on the
	// concentrator, which roams the edge's endpoint dynamically.
	Endpoint string `toml:"endpoint"`
	// AllowedIPs are the CIDR ranges routed to this peer.
	AllowedIPs []string `toml:"allowed_ips"`
}

// Amnezia holds the amneziawg-go obfuscation parameters. They must match on both
// ends for the handshake to succeed; they are defense-in-depth only.
type Amnezia struct {
	Jc   int    `toml:"jc"`
	Jmin int    `toml:"jmin"`
	Jmax int    `toml:"jmax"`
	S1   int    `toml:"s1"`
	S2   int    `toml:"s2"`
	H1   uint32 `toml:"h1"`
	H2   uint32 `toml:"h2"`
	H3   uint32 `toml:"h3"`
	H4   uint32 `toml:"h4"`
}

// Metrics configures the localhost Prometheus endpoint.
type Metrics struct {
	// Listen is the address the /metrics endpoint binds to; must be loopback.
	Listen string `toml:"listen"`
}

// Log configures structured logging.
type Log struct {
	Level string `toml:"level"`
}

// Key is a 32-byte Curve25519 key or PSK, carried in TOML as standard base64.
type Key struct {
	bytes [keyLen]byte
	set   bool
}

// UnmarshalText decodes a base64 key. An empty string leaves the Key unset so
// optional keys can be distinguished from present-but-invalid ones.
func (k *Key) UnmarshalText(text []byte) error {
	if len(text) == 0 {
		return nil
	}
	raw, err := base64.StdEncoding.DecodeString(string(text))
	if err != nil {
		return fmt.Errorf("key is not valid base64: %w", err)
	}
	if len(raw) != keyLen {
		return fmt.Errorf("key must decode to %d bytes, got %d", keyLen, len(raw))
	}
	copy(k.bytes[:], raw)
	k.set = true
	return nil
}

// IsSet reports whether the key was present and valid.
func (k Key) IsSet() bool { return k.set }

// Bytes returns the raw key material.
func (k Key) Bytes() [keyLen]byte { return k.bytes }

// normalize parses the string-typed fields (addresses) into their typed forms.
func (c *Config) normalize() error {
	for i := range c.Paths {
		p := &c.Paths[i]
		if p.SourceAddrRaw == "" {
			continue
		}
		addr, err := netip.ParseAddr(p.SourceAddrRaw)
		if err != nil {
			return fmt.Errorf("path %q: invalid source_addr %q: %w", p.Name, p.SourceAddrRaw, err)
		}
		p.SourceAddr = addr
	}
	return nil
}

// validate enforces the required-field invariants, failing on the first problem.
func (c *Config) validate() error {
	if !c.Role.valid() {
		return fmt.Errorf("role must be %q or %q, got %q", RoleEdge, RoleConcentrator, c.Role)
	}
	if len(c.Paths) == 0 {
		return errors.New("at least one path is required")
	}
	seen := make(map[string]struct{}, len(c.Paths))
	for i, p := range c.Paths {
		if p.Name == "" {
			return fmt.Errorf("path %d: name is required", i)
		}
		if _, dup := seen[p.Name]; dup {
			return fmt.Errorf("duplicate path name %q", p.Name)
		}
		seen[p.Name] = struct{}{}
		if !p.SourceAddr.IsValid() {
			return fmt.Errorf("path %q: source_addr is required", p.Name)
		}
	}
	if !c.WireGuard.PrivateKey.IsSet() {
		return errors.New("wireguard.private_key is required")
	}
	if len(c.WireGuard.Peers) == 0 {
		return errors.New("at least one wireguard peer is required")
	}
	for i, peer := range c.WireGuard.Peers {
		if !peer.PublicKey.IsSet() {
			return fmt.Errorf("wireguard peer %d: public_key is required", i)
		}
		if c.Role == RoleEdge && peer.Endpoint == "" {
			return fmt.Errorf("wireguard peer %d: endpoint is required for the edge role", i)
		}
	}
	if c.Role == RoleConcentrator && c.WireGuard.ListenPort == 0 {
		return errors.New("wireguard.listen_port is required for the concentrator role")
	}
	if !c.PSK.IsSet() {
		return errors.New("psk is required (authenticates outer control/probe frames)")
	}
	if err := c.Amnezia.validate(); err != nil {
		return err
	}
	return nil
}

// validate checks the amnezia junk-size ordering invariant.
func (a Amnezia) validate() error {
	if a.Jmin < 0 || a.Jmax < 0 || a.Jmin > a.Jmax {
		return fmt.Errorf("amnezia: require 0 <= jmin <= jmax, got jmin=%d jmax=%d", a.Jmin, a.Jmax)
	}
	return nil
}
