package config

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"

	awgdevice "github.com/amnezia-vpn/amneziawg-go/v3/device"

	"github.com/7mind/wanbond/internal/netutil"
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
	Role      Role            `toml:"role"`
	Exit      string          `toml:"exit"` // edge boot policy: "auto" (default) or an exit peer name
	Paths     []Path          `toml:"paths"`
	WireGuard WireGuard       `toml:"wireguard"`
	Amnezia   Amnezia         `toml:"amnezia"`
	PSK       Key             `toml:"psk"`
	Metrics   Metrics         `toml:"metrics"`
	Monitor   Monitor         `toml:"monitor"`
	Log       Log             `toml:"log"`
	Scheduler SchedulerConfig `toml:"scheduler"`
	DNS       DNS             `toml:"dns"`
	// Liveness configures the D86 top-level up/down detection down_after
	// threshold (T203). An absent [liveness] block is inert: DownAfter defaults
	// to defaultLivenessDownAfter, byte-identical to today's fixed
	// telemetry.DefaultDownAfter. See liveness.go.
	Liveness Liveness `toml:"liveness"`
	// Bind is the OPTIONAL top-level default bind mode (I5, Q42) applied to every
	// path that omits its own `bind`. Left empty it defaults to BindModeAuto
	// (today's selectDeviceBinds heuristic) in normalize(), so an existing config
	// with no `bind` anywhere keeps exactly today's per-path bind behaviour.
	Bind BindMode `toml:"bind"`
	// TUNPersist opts wanbond0 into surviving daemon restarts (I7, Q38). Default
	// false keeps today's teardown semantics exactly: the TUN is non-persistent
	// and the kernel destroys it when the daemon's last fd closes on Close, so
	// addresses/routes/rules referencing it are dropped on every restart. Set true
	// and the daemon issues TUNSETPERSIST on start (device.Up) — the link then
	// outlives Close (amneziawg-go's NativeTun.Close only closes the fd/netlink
	// socket; it never issues RTM_DELLINK), and the next start re-adopts the same
	// persistent device BY NAME via CreateTUN's TUNSETIFF, preserving its ifindex
	// so operator-owned addressing survives untouched. On an NM host a persistent
	// device STILL needs the unmanaged-devices drop-in (D39) — persistence keeps
	// the link across restarts but does not exempt it from NetworkManager.
	TUNPersist bool `toml:"tun_persist"`
	// LivenessBudgetSane is the D86-decision-4 WARN-arm failover-budget verdict (T211),
	// computed by normalize() and never read from TOML: ALWAYS non-nil (the failover
	// budget applies to every config). true when the analytical failover budget for the worst-case path
	// — livenessFailoverBudget(Liveness.DownAfter, max path ride_through,
	// livenessProbeInterval) — fits within livenessRecoveryBudget (the 3s P1 deadline);
	// false when it does NOT. It is WARN-AND-ALLOW: normalize NEVER rejects an over-budget
	// down_after/ride_through, it only records the verdict so the daemon can log one
	// startup WARN and export the wanbond_liveness_budget_sane gauge. The lower-side floor
	// (a too-SMALL down_after that permanently flaps) stays a hard REJECT in validate
	// (minLivenessDownAfter) — only the UPPER side is soft. See livenessBudgetSane.
	LivenessBudgetSane *bool `toml:"-"`
}

// BindMode selects, per path, how that path's UDP socket is bound to the
// network at Open time (I5, Q42):
//
//   - BindModeAuto reproduces today's selectDeviceBinds heuristic: device-bind
//     (SO_BINDTODEVICE) only when provably equivalent to pinning source_addr,
//     source-IP-bind otherwise.
//   - BindModeSource forces the pre-T16 source-IP pin unconditionally.
//   - BindModeDevice forces a device bind unconditionally.
type BindMode string

const (
	// BindModeSource forces the source-IP pin (pre-T16 behaviour).
	BindModeSource BindMode = "source"
	// BindModeDevice forces a device bind (SO_BINDTODEVICE).
	BindModeDevice BindMode = "device"
	// BindModeAuto reproduces today's selectDeviceBinds heuristic. It is the
	// default when a path (and the top-level default) both omit `bind`.
	BindModeAuto BindMode = "auto"
)

func (b BindMode) valid() bool {
	return b == BindModeSource || b == BindModeDevice || b == BindModeAuto
}

// SchedulerPolicy names the transport the multipath Bind runs. The adaptive
// transport (internal/bond) is the only one; the key is retained as a
// single-valued enum so a configuration that names it explicitly stays valid.
type SchedulerPolicy string

// PolicyAdaptive is the adaptive bonding transport: per-lane capacity discovery,
// bounded retransmission and small-packet replication.
const PolicyAdaptive SchedulerPolicy = "adaptive"

// SchedulerConfig selects the transport. An omitted [scheduler] block, or an
// omitted policy, means PolicyAdaptive.
type SchedulerConfig struct {
	// Policy selects the transport; defaults to PolicyAdaptive when empty.
	Policy SchedulerPolicy `toml:"policy"`
}

func (s *SchedulerConfig) applyDefaults() {
	if s.Policy == "" {
		s.Policy = PolicyAdaptive
	}
}

// validate rejects any policy other than PolicyAdaptive. The static weighted and
// active-backup schedulers were removed; naming one fails the load rather than
// silently running a different transport.
func (s SchedulerConfig) validate() error {
	if s.Policy != PolicyAdaptive {
		return fmt.Errorf("scheduler.policy must be %q (the only supported transport), got %q", PolicyAdaptive, s.Policy)
	}
	return nil
}

// outerPathOverheadBytes is the fixed per-datagram overhead a full-size inner
// (TUN) packet grows by once wrapped in the outer IPv4/UDP + data-frame +
// WireGuard-transport layers (T200, D85): bind.IPv4UDPOverhead (28) +
// bond.Overhead (101) + bind.WGTransportOverhead (32) = 161. It mirrors
// bind.InnerMTU's IPv4 budget rather than importing it — internal/bind
// imports internal/config, so config cannot import bind without a cycle.
// Path.validate uses it to reject an operator-declared
// `mtu` too small to leave a sane inner MTU; kept honest against drift in
// bind's actual overhead figures by TestPathMTUOverheadMatchesConfigMirror in
// internal/bind (that package CAN import config, so the cross-check lives
// there instead).
const outerPathOverheadBytes = 161

// minPathMTU is the lower bound accepted for an operator-declared per-path
// `mtu` (T200, D85): the IPv6 minimum-MTU floor (RFC 8200 §5), chosen as a
// conservative universal lower bound regardless of whether the underlay runs
// IPv4 or IPv6.
const minPathMTU = 1280

// maxPathMTU is the upper bound accepted for an operator-declared per-path
// `mtu` (T200, D85): the conventional jumbo-frame ceiling: values above this
// are not a real underlay path MTU on any WAN uplink this project targets and
// are almost certainly a config typo.
const maxPathMTU = 9000

// minInnerMTU is the smallest derived inner (TUN) MTU validate() accepts for
// an operator-declared `mtu` (T200, D85): the classic IPv4 minimum-reassembly
// MTU (RFC 791), the conventional floor below which an inner tunnel MTU is
// considered too degenerate to carry real traffic usefully. Given
// minPathMTU (1280) and outerPathOverheadBytes (161), this bound is currently
// subsumed by minPathMTU (1280-161 = 1119 > 576) — it stays an explicit,
// independent check so a future increase in outerPathOverheadBytes (e.g. an
// IPv6 budget) cannot silently let the derived inner MTU sink
// below a sane floor without validate() catching it.
const minInnerMTU = 576

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
	// DestAddr is the OPTIONAL per-path concentrator endpoint ("ip:port") the
	// edge sends this path's datagrams to. It is unset on the concentrator (which
	// learns edge endpoints from traffic) and optional on the edge: when a single
	// public concentrator IP fronts all uplinks (the common source-routed case)
	// the peer's wireguard endpoint is reused for every path; when the paths reach
	// the concentrator on distinct addresses, set dest_addr per path.
	DestAddr netip.AddrPort `toml:"-"`
	// DestAddrRaw is the TOML string form of DestAddr; parsed in normalize.
	DestAddrRaw string `toml:"dest_addr"`
	// Bind selects this path's bind mode (I5, Q42): "source", "device", or "auto".
	// Left empty in TOML, it falls back to the top-level Config.Bind default (itself
	// defaulted to BindModeAuto); normalize() resolves this field to its EFFECTIVE
	// value, so after Load it always holds one of the three valid modes, never
	// empty. See BindMode.
	Bind BindMode `toml:"bind"`
	// MTU is the OPERATOR-DECLARED outer path MTU in bytes: the underlay's IP-level
	// MTU this path's datagrams must fit without IP fragmentation (T200, D85). Zero
	// (the default — omitted key) means "unset": the datapath falls back to
	// bind.DefaultPathMTU / runtime auto-discovery, unchanged from pre-T200
	// behavior. When set, validate() requires it in [minPathMTU, maxPathMTU] AND
	// that the derived inner (TUN) MTU stays >= minInnerMTU — see
	// outerPathOverheadBytes below for why that derivation is mirrored rather than
	// imported from internal/bind. This field only carries and validates the
	// operator's declaration; wiring it into the TUN's actual MTU is T205.
	MTU int `toml:"mtu"`
	// RideThrough is the OPTIONAL per-path liveness ride-through duration (D86
	// decision 3, T203): DEFAULT 0, parsed from RideThroughRaw in normalize. An
	// unset (zero) value reproduces today's behavior byte-for-byte.
	// device.proberConfigForPath wires it into the path's Prober (T207).
	// Must be >= 0.
	RideThrough time.Duration `toml:"-"`
	// RideThroughRaw is the TOML Go-duration string form of RideThrough, e.g.
	// "5s" (go-toml/v2 cannot decode a TOML string
	// directly into a bare time.Duration field). Parsed in normalize; an
	// unparseable value fails fast.
	RideThroughRaw string `toml:"ride_through"`
}

// WireGuard holds the inner tunnel's key material.
type WireGuard struct {
	PrivateKey Key    `toml:"private_key"`
	Peers      []Peer `toml:"peers"`
	// ListenPort is the UDP port the concentrator listens on; 0 on the edge.
	ListenPort uint16 `toml:"listen_port"`
}

// PeerMode selects a peer's full-tunnel intent (I6, Q41 — thin surface):
// PeerModeDefaultRoute marks a peer as the edge's full-tunnel concentrator,
// permitting a 0.0.0.0/0 (and/or ::/0) entry in that peer's allowed_ips. The
// UAPI renderer (uapiConfig) ALWAYS splits a literal 0.0.0.0/0 or ::/0 into
// the equivalent /1+/1 pair regardless of mode (D35 — the engine wedges on
// the literal /0), so this field is a config-surface/validation concern only:
// it does not itself drive the split, and it does not yet wire any OS-level
// default-route/policy-routing behavior on the edge (that is a later task).
type PeerMode string

const (
	// PeerModeDefaultRoute marks this peer as the edge's full-tunnel
	// concentrator. Edge-only: a concentrator-role config declaring it on any
	// peer is a config error, mirroring the endpoint/dns edge-only rules.
	PeerModeDefaultRoute PeerMode = "default-route"
)

func (m PeerMode) valid() bool {
	return m == "" || m == PeerModeDefaultRoute
}

// Peer is one WireGuard peer.
type Peer struct {
	PublicKey Key `toml:"public_key"`
	// Endpoint is the peer's SINGLE tunnel address (edge -> concentrator); the
	// legacy single-endpoint config form, mutually exclusive with EndpointsRaw.
	// Empty on the concentrator, which roams the edge's endpoint dynamically.
	// A bare `endpoint = "..."` is normalized in resolveEndpoints to a one-
	// element Endpoints list, so it stays behavior-identical to the pre-T54
	// config surface.
	Endpoint string `toml:"endpoint"`
	// EndpointsRaw is the ORDERED list of concentrator (peer) endpoint address:port
	// strings for the edge-side hub-failover config surface (Q18): index 0 is the
	// active/primary concentrator, the rest are ordered standbys. Edge-only; a
	// concentrator declaring it is a config error (validate). Mutually exclusive
	// with the legacy Endpoint field.
	EndpointsRaw []string `toml:"endpoints"`
	// Endpoints is EndpointsRaw (or the single legacy Endpoint, as a one-element
	// list) parsed to netip.AddrPort and de-duplicated; populated by
	// resolveEndpoints in normalize(). This is the shape T57's hub-failover
	// switch consumes: Endpoints[0] is the active concentrator, Endpoints[1:]
	// are the ordered standbys to fail over to in order when all paths to the
	// active one are down. A hostname entry (Q35) contributes NO element here at
	// load time — resolution happens at runtime, not at config load (Q30) — so
	// Endpoints only ever holds the literal-address entries, in order.
	Endpoints []netip.AddrPort `toml:"-"`
	// DNS is the explicit per-peer opt-in (Q29) for hostname endpoint entries: a
	// hostname entry in endpoint/endpoints without dns = true is a config load
	// error naming this flag. Default-off, so an existing IP-literal config never
	// takes a new code path. Edge-only; a concentrator declaring dns = true is a
	// config error, mirroring the endpoints-not-meaningful-for-concentrator rule.
	DNS bool `toml:"dns"`
	// EndpointSpecs is the ordered, typed per-entry parse of the endpoint/
	// endpoints entries (Q35): each entry is either a literal address:port
	// (IsName=false, Addr set — parsed with EXACTLY today's netip.ParseAddrPort
	// path) or, when DNS opts in, a hostname:port (IsName=true, Host/Port set).
	// Populated by resolveEndpoints in normalize(); order matches the TOML list.
	EndpointSpecs []EndpointSpec `toml:"-"`
	// AllowedIPs are the CIDR ranges routed to this peer.
	AllowedIPs []string `toml:"allowed_ips"`
	// Mode is this peer's full-tunnel intent (I6, Q41): unset, or
	// "default-route" to mark it as the edge's full-tunnel concentrator. See
	// PeerMode.
	Mode PeerMode `toml:"mode"`
	// PSK is this peer's per-peer override of the outer-control PSK (G4
	// multi-peer concentrator groundwork). With a single configured peer it
	// must be left UNSET: the top-level Config.PSK remains the single-peer
	// default, so a legacy single-peer config carrying only the top-level
	// `psk` parses and behaves byte-identically to before this field existed.
	// With more than one peer, validate requires it to be present and
	// pairwise-distinct across peers (T81, Q21) — the top-level psk alone
	// cannot discriminate which peer authenticated an inbound frame, and equal
	// per-peer psks would defeat that authenticated demux. device.go calls
	// cfg.PeerIdentities() to derive each peer's effective PSK, and
	// bind/multipath.go consumes those per-peer PSKs for the peerBySource
	// PROBE-authenticated demux.
	PSK Key `toml:"psk"`
	// Name is this peer's human-readable identifier (G4 multi-peer
	// concentrator groundwork), analogous to Path.Name. Unused and optional
	// with a single peer; required and must be unique across peers when more
	// than one is configured (T81, Q21). Surfaces as the metrics 'peer' label
	// via BoundPeerNames/PeerSnapshot.Name for EVERY bound peer once a second
	// peer is configured — including the first-configured one (D58).
	Name string `toml:"name"`
}

// EndpointSpec is one parsed peer endpoint entry (Q35). A literal address:port
// entry parses via netip.ParseAddrPort and sets Addr (IsName=false); a
// hostname:port entry (accepted only when the owning Peer opts in with
// dns = true) sets Host/Port and IsName=true instead — no resolution happens at
// config load (Q30 defers it to runtime).
type EndpointSpec struct {
	// Host is the hostname, set only when IsName is true.
	Host string
	// Port is the port for a hostname entry, set only when IsName is true.
	Port uint16
	// Addr is the parsed literal address:port, set only when IsName is false.
	Addr netip.AddrPort
	// IsName reports whether this entry is a hostname (true) or an IP literal
	// (false).
	IsName bool
}

// resolveEndpoints canonicalizes the peer's endpoint config into the ordered
// Endpoints list (Q18) and the ordered, typed EndpointSpecs list (Q35): the
// legacy single Endpoint field and the new ordered EndpointsRaw list are
// mutually exclusive input forms, but a bare `endpoint = "..."` is normalized
// here to a ONE-ELEMENT list — so it stays behavior-identical to the pre-T54
// single-defaultRemote config surface. Each entry is tried FIRST as a literal
// netip.AddrPort ("host:port"), the same format bind.Multipath.ParseEndpoint
// requires of the UAPI endpoint string at runtime — an all-literal config
// takes EXACTLY this path, unchanged, with the same errors and the same
// duplicate detection as before T67 (Q29). Only when that parse fails, AND the
// host portion is not itself a malformed IP literal, is the entry split as
// host:port and treated as a hostname, gated behind the peer's explicit DNS
// opt-in (Q29); a hostname entry without the opt-in is a config error naming
// the flag. No resolution happens here (Q30 defers it to runtime). Duplicates
// are rejected within each of the two namespaces: a literal duplicating a
// literal, or a hostname:port duplicating another hostname:port.
func (p *Peer) resolveEndpoints() error {
	if p.Endpoint != "" && len(p.EndpointsRaw) > 0 {
		return errors.New("endpoint and endpoints are mutually exclusive; endpoint is the single-entry legacy form of endpoints")
	}
	raw := p.EndpointsRaw
	if p.Endpoint != "" {
		raw = []string{p.Endpoint}
	}
	seen := make(map[netip.AddrPort]struct{}, len(raw))
	seenNames := make(map[string]struct{}, len(raw))
	endpoints := make([]netip.AddrPort, 0, len(raw))
	specs := make([]EndpointSpec, 0, len(raw))
	for _, s := range raw {
		ap, err := netip.ParseAddrPort(s)
		if err == nil {
			if _, dup := seen[ap]; dup {
				return fmt.Errorf("duplicate endpoint %q", s)
			}
			seen[ap] = struct{}{}
			endpoints = append(endpoints, ap)
			specs = append(specs, EndpointSpec{Addr: ap})
			continue
		}
		host, portStr, splitErr := net.SplitHostPort(s)
		if splitErr != nil {
			return fmt.Errorf("invalid endpoint %q: %w", s, err)
		}
		// The host portion parses as an IP: this is a malformed IP-literal entry
		// (e.g. a bad port), not a hostname — report the ORIGINAL ParseAddrPort
		// error rather than diverting it into the hostname path, so every
		// IP-shaped entry keeps today's exact error.
		if _, ipErr := netip.ParseAddr(host); ipErr == nil {
			return fmt.Errorf("invalid endpoint %q: %w", s, err)
		}
		if !p.DNS {
			return fmt.Errorf("invalid endpoint %q: hostname endpoints require the peer's dns = true opt-in flag", s)
		}
		port, portErr := strconv.ParseUint(portStr, 10, 16)
		if portErr != nil || port == 0 {
			return fmt.Errorf("invalid endpoint %q: invalid port %q", s, portStr)
		}
		if hostErr := validateHostname(host); hostErr != nil {
			return fmt.Errorf("invalid endpoint %q: %w", s, hostErr)
		}
		nameKey := host + ":" + portStr
		if _, dup := seenNames[nameKey]; dup {
			return fmt.Errorf("duplicate endpoint %q", s)
		}
		seenNames[nameKey] = struct{}{}
		specs = append(specs, EndpointSpec{Host: host, Port: uint16(port), IsName: true})
	}
	p.Endpoints = endpoints
	p.EndpointSpecs = specs
	return nil
}

// validateHostname reports whether host is a syntactically valid DNS hostname
// (RFC 1123 label rules): 1-253 characters total, each dot-separated label
// 1-63 characters of letters/digits/hyphens, no leading or trailing hyphen.
func validateHostname(host string) error {
	if len(host) == 0 || len(host) > 253 {
		return fmt.Errorf("hostname %q must be 1-253 characters", host)
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 {
			return fmt.Errorf("hostname label %q must be 1-63 characters", label)
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return fmt.Errorf("hostname label %q must not start or end with a hyphen", label)
		}
		for _, r := range label {
			alnum := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
			if !alnum && r != '-' {
				return fmt.Errorf("hostname label %q contains invalid character %q", label, r)
			}
		}
	}
	return nil
}

// PeerIdentity is one configured WireGuard peer's effective outer-control PSK
// and stable name/id (G4 multi-peer concentrator groundwork).
type PeerIdentity struct {
	// PSK is this peer's effective outer-control PSK: for a single-peer config
	// it is the top-level Config.PSK (the pre-G4 back-compat default, unaffected
	// by any per-peer psk); for a multi-peer config it is the peer's own PSK
	// field. validate() rejects a per-peer psk when exactly one peer is
	// configured, so the single-peer-with-distinct-per-peer-psk shape is not
	// loadable via config.Load; this field still defends the invariant
	// defensively for any Config value built directly.
	PSK Key
	// Name is this peer's stable identifier: its configured Name when set,
	// otherwise a fallback derived from its public key (the first 8 bytes,
	// lowercase hex — the same short-key form uapiConfig uses to name a peer
	// in error messages), so every peer has a non-empty, stable id even when
	// name is omitted.
	Name string
}

// PeerIdentities returns, for each configured WireGuard peer in order, its
// effective PSK and stable name/id (G4). This is the SINGLE place the
// single-peer/multi-peer PSK back-compat decision is made: device.Up and the
// Bind consume PeerIdentities instead of each re-deriving the effective PSK
// from Config.PSK vs Peer.PSK. Order matches c.WireGuard.Peers.
func (c Config) PeerIdentities() []PeerIdentity {
	peers := c.WireGuard.Peers
	ids := make([]PeerIdentity, len(peers))
	for i, p := range peers {
		psk := p.PSK
		if len(peers) == 1 {
			// Single-peer back-compat: the top-level psk remains the effective
			// PSK regardless of whether a per-peer psk is also set.
			psk = c.PSK
		}
		name := p.Name
		if name == "" {
			pub := p.PublicKey.Bytes()
			name = hex.EncodeToString(pub[:8])
		}
		ids[i] = PeerIdentity{PSK: psk, Name: name}
	}
	return ids
}

// Amnezia holds the amneziawg-go obfuscation parameters. They must match on both
// ends for the handshake to succeed; they are defense-in-depth only.
//
// The block is "all-or-nothing": either every junk/size knob is left zero (plain
// WireGuard) or the whole obfuscation set (jc, jmin, jmax, s1, s2) is specified.
// A PARTIAL block silently produces an obfuscation profile the two ends cannot
// agree on, so validate rejects it at load (defect D1). The four magic headers
// (h1-h4) default to the standard message-type values 1..4 when omitted.
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

// defaultMagicHeaders are the standard WireGuard message-type headers: initiation,
// response, cookie-reply, and transport. amneziawg-go treats any magic header <= 4
// as "use the standard type", so 1..4 is the canonical "headers not obfuscated"
// profile. wanbond emits them explicitly (rather than 0) so a configured amnezia
// block always carries a complete, self-consistent set of magic headers.
var defaultMagicHeaders = [4]uint32{1, 2, 3, 4}

// Configured reports whether the amnezia block carries any obfuscation parameter.
// An all-zero block leaves the engine in plain WireGuard mode.
func (a Amnezia) Configured() bool {
	return a.Jc != 0 || a.Jmin != 0 || a.Jmax != 0 || a.S1 != 0 || a.S2 != 0 ||
		a.H1 != 0 || a.H2 != 0 || a.H3 != 0 || a.H4 != 0
}

// MaxJunkPrefix returns the maximum number of junk bytes AmneziaWG may PREPEND to a
// datagram under this obfuscation profile: max(S1, S2), the larger of the
// initiation/response junk-prefix lengths (defect D85, fix-direction 4). These are the
// only size-bearing prefixes in the profile — the s1/s2 bytes prepended ahead of a
// packet's type word; jc/jmin/jmax size SEPARATE junk PACKETS, not a per-datagram
// prefix, so they do not enter the DATA-frame MTU envelope. MTU sizing reserves this many
// bytes on top of the fixed outer overhead so a full-size DATA datagram plus a worst-case
// junk prefix still fits the path MTU without fragmentation/EMSGSIZE. An unconfigured
// (all-zero) block returns 0, leaving sizing byte-identical to plain WireGuard.
func (a Amnezia) MaxJunkPrefix() int {
	m := a.S1
	if a.S2 > m {
		m = a.S2
	}
	if m < 0 {
		m = 0
	}
	return m
}

// applyDefaults fills in the standard magic headers (1..4) when the block is
// configured but no header was given, so the UAPI renderer emits an explicit,
// complete header set instead of h1=0..h4=0. It is a no-op for an unconfigured
// block and for one that already sets any header (a partial header set is left
// intact so validate can reject it).
func (a *Amnezia) applyDefaults() {
	if !a.Configured() {
		return
	}
	if a.H1 == 0 && a.H2 == 0 && a.H3 == 0 && a.H4 == 0 {
		a.H1, a.H2, a.H3, a.H4 = defaultMagicHeaders[0], defaultMagicHeaders[1], defaultMagicHeaders[2], defaultMagicHeaders[3]
	}
}

// Metrics configures the localhost Prometheus endpoint.
type Metrics struct {
	// Listen is the address the /metrics endpoint binds to; must be loopback.
	Listen string `toml:"listen"`
}

// ErrMonitorNonLoopbackWithoutAuth is returned by validate() when monitor.listen
// is set to a non-loopback address without a monitor.token (Q45): the
// monitoring-UI endpoint exposes operational state and must not be reachable
// off-host without an explicit auth token, so an opt-in non-loopback bind
// without a token fails fast at config load rather than serving unauthenticated
// off-host.
var ErrMonitorNonLoopbackWithoutAuth = errors.New("config: monitor.listen is non-loopback and requires monitor.token to be set")

// Monitor configures the OPTIONAL monitoring-UI endpoint (Q45). Empty Listen
// mirrors Metrics: the block is disabled and Token is ignored. A loopback
// Listen needs no Token — operator access is host-local, matching the Metrics
// posture. A non-loopback Listen is an explicit off-host exposure opt-in and
// REQUIRES a non-empty Token; validate() enforces this invariant and fails
// fast with ErrMonitorNonLoopbackWithoutAuth otherwise.
type Monitor struct {
	// Listen is the address the monitoring-UI endpoint binds to. Empty (the
	// default) disables the endpoint entirely, mirroring Metrics.Listen.
	Listen string `toml:"listen"`
	// Token is the bearer credential the monitoring-UI endpoint requires when
	// Listen is non-loopback. Optional when Listen is loopback; ignored when
	// Listen is empty.
	Token string `toml:"token"`
	// AllowedHosts adds exact DNS names accepted in the HTTP Host header when
	// Listen binds a wildcard address.
	AllowedHosts []string `toml:"allowed_hosts"`
	// RevealAddressing controls whether the monitoring-UI endpoint reveals
	// addressing detail that is otherwise redacted. Default false preserves
	// today's non-loopback redaction; the flag composes with — never
	// weakens — the Token requirement enforced by validate(), so a
	// non-loopback Listen still REQUIRES a non-empty Token regardless of
	// this setting. On a loopback Listen it is a harmless no-op.
	RevealAddressing bool `toml:"reveal_addressing"`
}

// validate enforces the Q45 opt-in-non-loopback-requires-auth invariant: a
// disabled block (empty Listen) needs no token; a loopback Listen needs no
// token either (host-local access); a non-loopback Listen REQUIRES a non-empty
// Token, failing fast with ErrMonitorNonLoopbackWithoutAuth rather than
// serving an unauthenticated endpoint off-host. The loopback classification
// mirrors internal/metrics/server.go's requireLoopback via the shared
// internal/netutil helper (duplicated rather than imported, so config does not
// depend on the metrics package's internals).
func (m Monitor) validate() error {
	for _, host := range m.AllowedHosts {
		if host == "" || strings.TrimSpace(host) != host || strings.ContainsAny(host, "/:@") {
			return fmt.Errorf("monitor.allowed_hosts: invalid host %q", host)
		}
	}
	if m.Listen == "" {
		return nil
	}
	loopback, err := netutil.IsLoopbackHost(m.Listen)
	if err != nil {
		return fmt.Errorf("monitor.listen: %w", err)
	}
	if !loopback && m.Token == "" {
		return fmt.Errorf("%w: monitor.listen %q is not loopback", ErrMonitorNonLoopbackWithoutAuth, m.Listen)
	}
	return nil
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
	if c.Exit == "" && c.Role == RoleEdge {
		c.Exit = "auto"
	}
	// Resolve the global bind default BEFORE the per-path loop so an omitted
	// per-path `bind` can fall back to it: an empty top-level Bind defaults to
	// BindModeAuto (today's selectDeviceBinds behavior), matching an existing
	// config that never mentions `bind` anywhere. An invalid (non-empty,
	// unrecognized) value is left untouched here and rejected by validate().
	if c.Bind == "" {
		c.Bind = BindModeAuto
	}
	for i := range c.Paths {
		p := &c.Paths[i]
		// Per-path override beats the global default; empty falls back to it.
		if p.Bind == "" {
			p.Bind = c.Bind
		}
		if p.SourceAddrRaw != "" {
			addr, err := netip.ParseAddr(p.SourceAddrRaw)
			if err != nil {
				return fmt.Errorf("path %q: invalid source_addr %q: %w", p.Name, p.SourceAddrRaw, err)
			}
			p.SourceAddr = addr
		}
		if p.DestAddrRaw != "" {
			dst, err := netip.ParseAddrPort(p.DestAddrRaw)
			if err != nil {
				return fmt.Errorf("path %q: invalid dest_addr %q: %w", p.Name, p.DestAddrRaw, err)
			}
			p.DestAddr = dst
		}
		if p.RideThroughRaw != "" {
			rt, err := time.ParseDuration(p.RideThroughRaw)
			if err != nil {
				return fmt.Errorf("path %q: invalid ride_through %q: %w", p.Name, p.RideThroughRaw, err)
			}
			p.RideThrough = rt
		}
	}
	for i := range c.WireGuard.Peers {
		if err := c.WireGuard.Peers[i].resolveEndpoints(); err != nil {
			return fmt.Errorf("wireguard peer %d: %w", i, err)
		}
	}
	c.Amnezia.applyDefaults()
	c.Scheduler.applyDefaults()
	if err := c.DNS.applyDefaults(); err != nil {
		return err
	}
	if err := c.Liveness.applyDefaults(); err != nil {
		return err
	}
	// Computed after Liveness.applyDefaults has resolved DownAfter and every path's
	// RideThrough has been parsed, so the verdict sees the EFFECTIVE timing (T211).
	c.LivenessBudgetSane = c.livenessBudgetSane()
	return nil
}

// livenessBudgetSane computes the D86-decision-4 WARN-arm failover-budget verdict
// (T211). It returns
// a non-nil *bool for EVERY config (the budget always applies): true when the
// worst-case-path analytical failover budget fits within the 3s P1 recovery deadline,
// false when it exceeds it. The worst-case path is the one with the LARGEST ride_through
// (its dwell adds directly to the detect term), so the max over paths is the tightest
// single verdict. It NEVER rejects — an over-budget value is allowed and surfaced only
// as a startup WARN + gauge; only the lower-side flap floor is a hard reject (validate).
func (c *Config) livenessBudgetSane() *bool {
	maxRideThrough := time.Duration(0)
	for i := range c.Paths {
		if c.Paths[i].RideThrough > maxRideThrough {
			maxRideThrough = c.Paths[i].RideThrough
		}
	}
	budget := livenessFailoverBudget(c.Liveness.DownAfter, maxRideThrough, livenessProbeInterval)
	sane := budget <= livenessRecoveryBudget
	return &sane
}

// peerLabel formats a wireguard peer identifier for a validation error: the
// index always, plus the configured name in parens when set (a single-peer
// config legitimately leaves Name empty; a multi-peer one requires it).
func peerLabel(i int, name string) string {
	if name == "" {
		return fmt.Sprintf("wireguard peer %d", i)
	}
	return fmt.Sprintf("wireguard peer %d (%q)", i, name)
}

// maxProbeFanout bounds an edge's total per-(peer,path) probe fan-out (Q74). The
// edge runs ONE liveness/RTT prober per (concentrator peer, uplink) pair, so the
// fan-out is N concentrator peers × U uplinks. The uplink SOCKETS do not multiply
// by N — every peer shares each uplink's socket (bind's attachSharedPathLocked) —
// but the PROBERS do: each emits one PROBE per fixed livenessProbeInterval
// (200ms) plus its reflected echo. Those generated frames bypass Send and the
// transport's pacing. The ceiling independently
// keeps aggregate emission bounded (32 probers × 5 PROBE/s = 160 PROBE/s across
// the shared uplink sockets). It admits realistic multi-concentrator
// edges (e.g. 8 concentrators × 4 uplinks) and rejects a pathological fan-out at
// config LOAD with a computed-vs-available message, rather than deferring the
// overload to runtime.
const maxProbeFanout = 32

// defaultRouteMask records which address families' full default route
// (0.0.0.0/0, ::/0) a peer's allowed_ips carry (T250 rule 2).
type defaultRouteMask struct {
	v4 bool
	v6 bool
}

// defaultRouteMaskOf returns which families' full default route the prefixes
// carry. Exit-capable peers are alternates for the SAME egress role, so with
// more than one their default-route entry sets must match (T250 rule 2).
func defaultRouteMaskOf(prefixes []netip.Prefix) defaultRouteMask {
	var m defaultRouteMask
	for _, p := range prefixes {
		if p.Bits() != 0 {
			continue
		}
		if p.Addr().Is4() {
			m.v4 = true
		} else {
			m.v6 = true
		}
	}
	return m
}

// hasNonDefaultAllowedIP reports whether prefixes carry at least one non-default
// entry (a prefix narrower than /0 — e.g. a concentrator's inner /32). T250 rule
// 8 (R255): every exit-capable peer in an N>1-exit config must carry one, so
// T254's standby render (which strips the /1+/1 default-route splits) still leaves
// a non-empty allowed_ips and an inner address for the T261 warm-session ping.
func hasNonDefaultAllowedIP(prefixes []netip.Prefix) bool {
	for _, p := range prefixes {
		if p.Bits() != 0 {
			return true
		}
	}
	return false
}

// validateConcentratorDefaultRoutes preserves the pre-T250 D59 cross-peer
// default-route guard for the concentrator role: a literal /0 entry may appear on
// at most ONE peer per address family (WireGuard cryptokey routing makes
// overlapping allowed_ips last-writer-wins). The concentrator rejects
// mode = "default-route" entirely (checked in validate's per-peer loop), so this
// is its only default-route surface; the within-peer duplicate /0 is likewise
// already rejected there.
func (c *Config) validateConcentratorDefaultRoutes(peerPrefixes [][]netip.Prefix) error {
	v4DefaultPeer := -1
	v6DefaultPeer := -1
	for i, prefixes := range peerPrefixes {
		name := c.WireGuard.Peers[i].Name
		for _, prefix := range prefixes {
			if prefix.Bits() != 0 {
				continue
			}
			if prefix.Addr().Is4() {
				if v4DefaultPeer != -1 {
					return fmt.Errorf("wireguard peers %s and %s both list 0.0.0.0/0 in allowed_ips; WireGuard cryptokey routing makes overlapping allowed_ips last-writer-wins",
						peerLabel(v4DefaultPeer, c.WireGuard.Peers[v4DefaultPeer].Name), peerLabel(i, name))
				}
				v4DefaultPeer = i
			} else {
				if v6DefaultPeer != -1 {
					return fmt.Errorf("wireguard peers %s and %s both list ::/0 in allowed_ips; WireGuard cryptokey routing makes overlapping allowed_ips last-writer-wins",
						peerLabel(v6DefaultPeer, c.WireGuard.Peers[v6DefaultPeer].Name), peerLabel(i, name))
				}
				v6DefaultPeer = i
			}
		}
	}
	return nil
}

// validateEdgePeerSet enforces the T250 multi-exit edge peer semantics
// (Q68b/Q74). The edge admits N concentrator peers over the SAME set of uplinks;
// each mode = "default-route" peer is an exit-capable alternate for the shared
// egress role. A single-peer edge config keeps the pre-T250 behavior
// byte-identically — the multi-exit rules below bind only at N>1.
func (c *Config) validateEdgePeerSet(peerPrefixes [][]netip.Prefix) error {
	peers := c.WireGuard.Peers
	// Rule 7 (Q74 probe budget): reject a config whose per-(peer,path) probe
	// fan-out exceeds the documented budget before validating the arrangement —
	// the fan-out is unsupportable regardless of how the peers are configured.
	if fanout := len(peers) * len(c.Paths); fanout > maxProbeFanout {
		return fmt.Errorf("edge probe fan-out of %d probers (%d concentrator peers × %d uplinks) exceeds the available probe budget of %d; reduce the concentrator or uplink count (one liveness prober runs per peer-uplink pair)",
			fanout, len(peers), len(c.Paths), maxProbeFanout)
	}
	if len(peers) == 1 {
		// Single-peer legacy edge: the pre-T250 full-tunnel form
		// (allowed_ips = ["0.0.0.0/0"], with or without mode = "default-route")
		// loads byte-identically. Every multi-exit rule below binds only at N>1.
		return nil
	}
	// Rule 2 (part 1): a literal /0 entry is the full default route and is
	// permitted ONLY on a mode = "default-route" (exit-capable) peer. Collect the
	// exit-capable peers while rejecting a /0 on any non-exit peer.
	exitPeers := make([]int, 0, len(peers))
	for i := range peers {
		exitCapable := peers[i].Mode == PeerModeDefaultRoute
		if exitCapable {
			exitPeers = append(exitPeers, i)
		}
		for _, prefix := range peerPrefixes[i] {
			if prefix.Bits() == 0 && !exitCapable {
				return fmt.Errorf("%s: allowed_ips entry %s is the full default route and is permitted only on a mode = %q peer", peerLabel(i, peers[i].Name), prefix, PeerModeDefaultRoute)
			}
		}
	}
	// Rules 2 (part 2) and 8 (R255): with more than one exit-capable peer, they
	// are alternates for the same egress role, so each must carry (a) the SAME
	// default-route entry set and (b) at least one NON-default allowed_ip (its
	// concentrator inner address, e.g. its inner /32). A peer carrying SOLELY
	// 0.0.0.0/0 / ::/0 would render ZERO allowed_ips once T254 strips the
	// default-route splits on a standby, tripping the boot-time uapiConfig
	// >=1-allowed_ip invariant.
	if len(exitPeers) > 1 {
		first := exitPeers[0]
		firstMask := defaultRouteMaskOf(peerPrefixes[first])
		for _, i := range exitPeers {
			if mask := defaultRouteMaskOf(peerPrefixes[i]); mask != firstMask {
				return fmt.Errorf("exit-capable wireguard peers %s and %s carry different default-route entry sets; with more than one mode = %q peer every exit-capable peer must list the same default-route entries (they are alternates for the same egress role)",
					peerLabel(first, peers[first].Name), peerLabel(i, peers[i].Name), PeerModeDefaultRoute)
			}
			if !hasNonDefaultAllowedIP(peerPrefixes[i]) {
				return fmt.Errorf("exit-capable %s carries only the full default route; with more than one mode = %q peer each must ALSO carry its concentrator inner address (e.g. its inner /32) so a standby exit still renders a non-empty allowed_ips",
					peerLabel(i, peers[i].Name), PeerModeDefaultRoute)
			}
		}
	}
	// Rule 5: non-default allowed_ips must not overlap across peers, so each
	// concentrator's inner address stays unambiguous under WireGuard cryptokey
	// routing (the shared /0 default route is excluded — it is intentionally
	// identical across exit-capable peers per rule 2).
	for a := range peers {
		for _, pa := range peerPrefixes[a] {
			if pa.Bits() == 0 {
				continue
			}
			for b := a + 1; b < len(peers); b++ {
				for _, pb := range peerPrefixes[b] {
					if pb.Bits() == 0 {
						continue
					}
					if pa.Overlaps(pb) {
						return fmt.Errorf("wireguard peers %s and %s have overlapping non-default allowed_ips %s and %s; each concentrator's inner address must stay unambiguous",
							peerLabel(a, peers[a].Name), peerLabel(b, peers[b].Name), pa, pb)
					}
				}
			}
		}
	}
	// Rule 3: reject an exact duplicate literal endpoint address:port across two
	// peers (the per-peer T57 failover list is retained; within-peer duplicates
	// are already rejected in resolveEndpoints). A hostname entry (Q35) carries no
	// literal Addr at load, so only literal endpoints are compared here.
	seenEndpoint := make(map[netip.AddrPort]int, len(peers))
	for i := range peers {
		for _, spec := range peers[i].EndpointSpecs {
			if spec.IsName {
				continue
			}
			if prev, dup := seenEndpoint[spec.Addr]; dup {
				return fmt.Errorf("wireguard peers %s and %s share endpoint %s; each concentrator peer must be reachable at a distinct endpoint",
					peerLabel(prev, peers[prev].Name), peerLabel(i, peers[i].Name), spec.Addr)
			}
			seenEndpoint[spec.Addr] = i
		}
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
	if !c.Bind.valid() {
		return fmt.Errorf("bind must be %q, %q or %q, got %q", BindModeSource, BindModeDevice, BindModeAuto, c.Bind)
	}
	seen := make(map[string]struct{}, len(c.Paths))
	// seenSrc maps an already-claimed source_addr to the path that claimed it, so a
	// second path reusing it can be rejected at LOAD naming both conflicting paths.
	// The multipath bind Opens each path on (source_addr, listen_port), so two paths
	// sharing a source_addr collide EADDRINUSE at the second ListenUDP (and on every
	// re-Open after Down/Up, since the engine passes the fixed port back) — a
	// misconfiguration that must fail fast at load, not at bring-up (defect D10).
	seenSrc := make(map[netip.Addr]string, len(c.Paths))
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
		// Compare the UNMAPPED form: "192.0.2.10" (Is4) and "::ffff:192.0.2.10"
		// (Is4In6) are distinct under netip == yet bind the identical v4 socket, so
		// the two spellings must collide in the guard exactly as they do at bind.
		src := p.SourceAddr.Unmap()
		if prev, dup := seenSrc[src]; dup {
			return fmt.Errorf("paths %q and %q share source_addr %s; each path must bind a distinct source address (a shared source collides EADDRINUSE at the second bind)", prev, p.Name, p.SourceAddr)
		}
		seenSrc[src] = p.Name
		if !p.Bind.valid() {
			return fmt.Errorf("path %q: bind must be %q, %q or %q, got %q", p.Name, BindModeSource, BindModeDevice, BindModeAuto, p.Bind)
		}
		// mtu = 0 means "unset" (auto): the datapath falls back to bind.DefaultPathMTU /
		// auto-discovery, so only a nonzero declaration is range-checked (T200, D85).
		if p.MTU != 0 {
			if p.MTU < minPathMTU || p.MTU > maxPathMTU {
				return fmt.Errorf("path %q: mtu must be %d..%d (or 0 for auto), got %d", p.Name, minPathMTU, maxPathMTU, p.MTU)
			}
			if innerMTU := p.MTU - outerPathOverheadBytes; innerMTU < minInnerMTU {
				return fmt.Errorf("path %q: mtu %d leaves an inner MTU of %d after the fixed %d-byte outer overhead, below the required minimum %d", p.Name, p.MTU, innerMTU, outerPathOverheadBytes, minInnerMTU)
			}
		}
		if p.RideThrough < 0 {
			return fmt.Errorf("path %q: ride_through must be >= 0, got %s", p.Name, p.RideThrough)
		}
	}
	if !c.WireGuard.PrivateKey.IsSet() {
		return errors.New("wireguard.private_key is required")
	}
	if len(c.WireGuard.Peers) == 0 {
		return errors.New("at least one wireguard peer is required")
	}
	// Per-peer field invariants + allowed_ips parse (T250). Every peer's
	// public_key, edge/concentrator endpoint+dns+mode rules, D55 malformed-CIDR
	// parse, and the within-peer duplicate-default-route guard are enforced here,
	// for BOTH roles and any peer count, unchanged from pre-T250. The parsed
	// prefixes feed the role-specific cross-peer default-route / overlap /
	// endpoint / probe-budget checks below.
	peerPrefixes := make([][]netip.Prefix, len(c.WireGuard.Peers))
	for i, peer := range c.WireGuard.Peers {
		if !peer.PublicKey.IsSet() {
			return fmt.Errorf("wireguard peer %d: public_key is required", i)
		}
		// EndpointSpecs (not Endpoints) is the "any endpoint entry configured" check:
		// a hostname-only peer contributes nothing to Endpoints at load time (Q30 —
		// resolution is deferred to runtime) but must still count as configured. Every
		// edge peer carries its own endpoint/endpoints list (T57 per-concentrator
		// failover is retained per peer, Q72 — T250).
		if c.Role == RoleEdge && len(peer.EndpointSpecs) == 0 {
			return fmt.Errorf("wireguard peer %d: endpoint is required for the edge role", i)
		}
		// The concentrator learns the edge's endpoint dynamically (roaming across
		// NAT rebinds); an ordered failover endpoint list is meaningless for it, so
		// reject rather than silently ignore a mis-targeted config (Q18 is edge-side
		// only).
		if c.Role == RoleConcentrator && len(peer.EndpointSpecs) != 0 {
			return fmt.Errorf("wireguard peer %d: endpoint/endpoints is not meaningful for the concentrator role (it learns the edge's endpoint dynamically)", i)
		}
		// The DNS opt-in (Q29) is edge-only, mirroring the endpoints rule above: a
		// concentrator learns the edge's endpoint dynamically, so a hostname
		// endpoint config (and its dns = true opt-in) is meaningless there even
		// when no endpoint/endpoints entries are present.
		if c.Role == RoleConcentrator && peer.DNS {
			return fmt.Errorf("wireguard peer %d: dns is not meaningful for the concentrator role (it learns the edge's endpoint dynamically)", i)
		}
		if !peer.Mode.valid() {
			return fmt.Errorf("wireguard peer %d: mode must be empty or %q, got %q", i, PeerModeDefaultRoute, peer.Mode)
		}
		// The full-tunnel/default-route surface is edge-only (I6, Q41), mirroring
		// the endpoints/dns edge-only rules above: a concentrator is the full-tunnel
		// TARGET, not the peer that opts a route into it, so mode = "default-route"
		// on a concentrator-role peer is a config error rather than a silent no-op.
		if c.Role == RoleConcentrator && peer.Mode == PeerModeDefaultRoute {
			return fmt.Errorf("wireguard peer %d: mode = %q is not meaningful for the concentrator role (it is an edge-only full-tunnel opt-in)", i, PeerModeDefaultRoute)
		}
		// D55: allowed_ips entries are parsed here — not just carried as opaque
		// strings — so a malformed CIDR (a typo, or an out-of-range prefix length
		// like /33) fails fast at LOAD naming the peer and offending entry, instead
		// of surfacing LATE and opaquely when the engine's UAPI allowed_ip= line
		// fails to parse at daemon start. This mirrors the source_addr/endpoint
		// parse-at-load discipline elsewhere in this function.
		prefixes := make([]netip.Prefix, 0, len(peer.AllowedIPs))
		var v4SeenInPeer, v6SeenInPeer bool
		for _, raw := range peer.AllowedIPs {
			prefix, err := netip.ParsePrefix(raw)
			if err != nil {
				return fmt.Errorf("%s: invalid allowed_ips entry %q: %w", peerLabel(i, peer.Name), raw, err)
			}
			// D59: a literal /0 entry is the full default route for its address
			// family — reject a REDUNDANT duplicate within this peer's own
			// allowed_ips. The cross-peer default-route rules are role-specific and
			// enforced below (validateEdgePeerSet / validateConcentratorDefaultRoutes).
			if prefix.Bits() == 0 {
				if prefix.Addr().Is4() {
					if v4SeenInPeer {
						return fmt.Errorf("%s: duplicate 0.0.0.0/0 entry in allowed_ips", peerLabel(i, peer.Name))
					}
					v4SeenInPeer = true
				} else {
					if v6SeenInPeer {
						return fmt.Errorf("%s: duplicate ::/0 entry in allowed_ips", peerLabel(i, peer.Name))
					}
					v6SeenInPeer = true
				}
			}
			prefixes = append(prefixes, prefix)
		}
		peerPrefixes[i] = prefixes
	}
	// Cross-peer default-route / overlap / endpoint / probe-budget validation is
	// role-specific (T250). The edge admits N concentrator peers with multi-exit
	// semantics (Q68b/Q74 — arbitrary N, static set); the concentrator keeps the
	// pre-T250 D59 single-default-route-per-family guard.
	if c.Role == RoleEdge {
		if err := c.validateEdgePeerSet(peerPrefixes); err != nil {
			return err
		}
	} else if err := c.validateConcentratorDefaultRoutes(peerPrefixes); err != nil {
		return err
	}
	if c.Role != RoleEdge && c.Exit != "" {
		return fmt.Errorf("exit %q requires edge role", c.Exit)
	}
	for i, peer := range c.WireGuard.Peers {
		if peer.Mode == PeerModeDefaultRoute && peer.Name == "auto" {
			return fmt.Errorf("wireguard peer %d: name %q is reserved for exit selection", i, peer.Name)
		}
	}
	if c.Exit != "" && c.Exit != "auto" {
		found := false
		for _, peer := range c.WireGuard.Peers {
			if peer.Name == c.Exit && peer.Mode == PeerModeDefaultRoute {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("exit %q is not a configured default-route peer", c.Exit)
		}
	}
	// Per-peer name/psk (Q21 multi-peer concentrator): with a single peer the
	// top-level Config.PSK remains the sole authenticator (T80) and a per-peer
	// psk is redundant, so reject one if given. With more than one peer, the
	// single top-level psk can no longer discriminate which peer authenticated
	// an inbound frame, so each peer must carry its own name and psk, both
	// required and pairwise-distinct — equal per-peer psks would defeat that
	// authenticated demux.
	if len(c.WireGuard.Peers) == 1 {
		if c.WireGuard.Peers[0].PSK.IsSet() {
			return errors.New("wireguard peer 0: psk is not meaningful with a single peer; the top-level psk is used as the default")
		}
	} else {
		seenNames := make(map[string]int, len(c.WireGuard.Peers))
		seenPSKs := make(map[[keyLen]byte]int, len(c.WireGuard.Peers))
		for i, peer := range c.WireGuard.Peers {
			if peer.Name == "" {
				return fmt.Errorf("wireguard peer %d: name is required when more than one peer is configured", i)
			}
			if prev, dup := seenNames[peer.Name]; dup {
				return fmt.Errorf("wireguard peers %d and %d share name %q; peer names must be unique", prev, i, peer.Name)
			}
			seenNames[peer.Name] = i
			if !peer.PSK.IsSet() {
				return fmt.Errorf("wireguard peer %d (%q): psk is required when more than one peer is configured", i, peer.Name)
			}
			if prev, dup := seenPSKs[peer.PSK.Bytes()]; dup {
				return fmt.Errorf("wireguard peers %d (%q) and %d (%q) share the same psk; per-peer psks must be pairwise distinct (equal psks defeat authenticated demux)", prev, c.WireGuard.Peers[prev].Name, i, peer.Name)
			}
			seenPSKs[peer.PSK.Bytes()] = i
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
	if err := c.Scheduler.validate(); err != nil {
		return err
	}
	if err := c.DNS.validate(); err != nil {
		return err
	}
	if err := c.Liveness.validate(); err != nil {
		return err
	}
	if err := c.Monitor.validate(); err != nil {
		return err
	}
	return nil
}

// validate enforces the amnezia obfuscation invariants (defect D1). An
// unconfigured block is valid (plain WireGuard). A configured block must specify
// the WHOLE junk/size set and carry a consistent magic-header set, so a partial
// or inconsistent obfuscation profile FAILS FAST at load rather than producing a
// silently mismatched tunnel that never handshakes.
//
// This runs after applyDefaults, so an omitted magic-header set has already been
// filled with the standard 1..4 values; the header check below therefore only
// fires for a genuinely partial header set (some given, some left zero).
func (a Amnezia) validate() error {
	if !a.Configured() {
		return nil
	}
	// All-or-nothing: enabling amnezia at all requires the full junk/size set, so
	// both ends are forced to specify the same complete profile. A partial block
	// (e.g. jc/jmin/jmax set but s1/s2 omitted) would leave the ends deriving
	// different profiles and the handshake would fail closed with no diagnostic.
	if a.Jc <= 0 || a.Jmin <= 0 || a.Jmax <= 0 || a.S1 <= 0 || a.S2 <= 0 {
		return fmt.Errorf("amnezia: incomplete obfuscation set — when configured, jc, jmin, jmax, s1 and s2 must all be > 0 (got jc=%d jmin=%d jmax=%d s1=%d s2=%d)",
			a.Jc, a.Jmin, a.Jmax, a.S1, a.S2)
	}
	if a.Jmin > a.Jmax {
		return fmt.Errorf("amnezia: require jmin <= jmax, got jmin=%d jmax=%d", a.Jmin, a.Jmax)
	}
	// Junk sizes must not collide: amneziawg-go classifies an incoming datagram's
	// message type by its on-the-wire length, so the obfuscated initiation packet
	// (MessageInitiationSize + s1) and response packet (MessageResponseSize + s2)
	// must differ in length. A colliding s1/s2 pair passes config load but is
	// rejected later by the engine's IpcSet ("new init size == new response size");
	// reject it here so the collision fails fast at load, at the right locus. The
	// engine's own MessageInitiationSize (148) and MessageResponseSize (92)
	// constants are used so this check tracks the vendored fork if it ever changes.
	if awgdevice.MessageInitiationSize+a.S1 == awgdevice.MessageResponseSize+a.S2 {
		return fmt.Errorf("amnezia: junk sizes collide — obfuscated init size %d (%d+s1) must differ from response size %d (%d+s2); adjust s1/s2",
			awgdevice.MessageInitiationSize+a.S1, awgdevice.MessageInitiationSize,
			awgdevice.MessageResponseSize+a.S2, awgdevice.MessageResponseSize)
	}
	// Magic headers must be distinct: the receive path classifies a datagram's
	// message type by its header value, so two equal headers make two message
	// types indistinguishable. An all-zero header set is left for applyDefaults;
	// any non-zero header requires a complete, distinct set (a partial set leaves
	// zeros here and is caught as a duplicate).
	if a.H1 != 0 || a.H2 != 0 || a.H3 != 0 || a.H4 != 0 {
		if a.H1 == a.H2 || a.H1 == a.H3 || a.H1 == a.H4 ||
			a.H2 == a.H3 || a.H2 == a.H4 || a.H3 == a.H4 {
			return fmt.Errorf("amnezia: magic headers must be a complete, distinct set, got h1=%d h2=%d h3=%d h4=%d",
				a.H1, a.H2, a.H3, a.H4)
		}
	}
	return nil
}
