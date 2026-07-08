package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/netip"
	"time"

	awgdevice "github.com/amnezia-vpn/amneziawg-go/device"
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
	Paths     []Path          `toml:"paths"`
	WireGuard WireGuard       `toml:"wireguard"`
	Amnezia   Amnezia         `toml:"amnezia"`
	PSK       Key             `toml:"psk"`
	Metrics   Metrics         `toml:"metrics"`
	Log       Log             `toml:"log"`
	Scheduler SchedulerConfig `toml:"scheduler"`
	FEC       FEC             `toml:"fec"`
}

// SchedulerPolicy selects the send-side path-selection policy the multipath Bind
// runs. It is a bounded enum: the P1 active-backup failover (default) or the T21
// weighted-aggregation policy. The policy is an explicit config choice — active-
// backup stays selectable so the P1 behaviour is never removed, only extended.
type SchedulerPolicy string

const (
	// PolicyActiveBackup is the P1 single-active-path failover scheduler: all egress
	// on the highest-priority live path, every other path idle (data-thrift by
	// construction). It is the default when [scheduler] is omitted.
	PolicyActiveBackup SchedulerPolicy = "active-backup"
	// PolicyWeighted is the T21 weighted-aggregation scheduler: under load a single
	// flow is striped across both paths in proportion to per-path RTT/loss-derived
	// weight, collapsing to the primary at low load (5G stays ~idle) with hysteresis,
	// and per-path send-pacing to bound bufferbloat.
	PolicyWeighted SchedulerPolicy = "weighted"
)

func (p SchedulerPolicy) valid() bool {
	return p == PolicyActiveBackup || p == PolicyWeighted
}

// Weighted-aggregation policy defaults (T21). They are applied when [scheduler]
// selects the weighted policy but leaves a knob at its zero value, so a minimal
// `policy = "weighted"` block is usable without hand-tuning every threshold. They
// are conservative bring-up values; the empirical per-path pace is sized from a
// measured BDP in a bandwidth-capped fixture (P0 findings §7, deferred to T35/T23),
// so the shipped default leaves pacing DISABLED and only bounds distribution.
const (
	// defaultPerPathCapacityFPS is the reference per-path capacity, in frame-selection
	// slots per second, that the aggregation load-gate compares offered load against
	// AND (when pacing is enabled) the per-path token-bucket refill rate. It is a
	// synthetic proxy for path capacity: there is no measured BDP (P0 §7), so capacity
	// is expressed in the only unit the scheduler sees — Pick() invocations per second.
	defaultPerPathCapacityFPS = 10000.0
	// defaultEngageFraction engages aggregation once offered load exceeds this
	// fraction of one path's capacity — i.e. a single path is nearly saturated.
	defaultEngageFraction = 0.9
	// defaultDisengageFraction collapses back to primary-only once offered load falls
	// below this fraction of one path's capacity. It is strictly below the engage
	// fraction: the gap is the hysteresis band that stops the 5G path from dribbling
	// on/off around a single threshold (requirement 2, data-thrift).
	defaultDisengageFraction = 0.5
	// defaultCollapseDwell is how long offered load must stay continuously below the
	// disengage fraction before aggregation collapses to primary-only — the temporal
	// half of the hysteresis, mirroring the active-backup failback dwell so a brief
	// lull does not repeatedly park then re-engage the metered path.
	defaultCollapseDwell = 2 * time.Second
	// defaultLoadTau is the time constant of the exponentially-weighted offered-load
	// rate estimator. It smooths the instantaneous Pick() arrival rate so the gate
	// reacts to sustained load, not single-frame bursts.
	defaultLoadTau = 200 * time.Millisecond
	// defaultPacingBurstFrames is the per-path token-bucket burst (in frame slots)
	// when pacing is enabled: the bucket admits up to this many frames instantaneously
	// before the refill rate binds, absorbing normal jitter without inflating a queue.
	defaultPacingBurstFrames = 64.0
	// defaultWeightRTTFloor floors the RTT in the weight formula so a path reporting a
	// near-zero RTT (cold estimator, no samples yet) cannot be handed unbounded weight.
	defaultWeightRTTFloor = 1 * time.Millisecond
	// defaultWeightLossFloor floors the loss term under the square root so a zero-loss
	// path gets a large-but-finite weight and two zero-loss paths split by inverse-RTT
	// alone rather than both diverging.
	defaultWeightLossFloor = 1e-3
)

// SchedulerConfig selects and tunes the send scheduler. When the [scheduler] block
// is omitted the policy defaults to active-backup and every weighted knob is
// ignored, so existing configs keep the P1 behaviour unchanged. The weighted knobs
// are validated (and defaulted) only when the weighted policy is selected.
type SchedulerConfig struct {
	// Policy selects the scheduler; defaults to active-backup when empty.
	Policy SchedulerPolicy `toml:"policy"`

	// PerPathCapacityFPS is the reference per-path capacity in frame-selection slots
	// per second: the denominator the aggregation load-gate compares offered load
	// against, and the per-path pacing refill rate when PacingEnabled. Must be > 0
	// under the weighted policy.
	PerPathCapacityFPS float64 `toml:"per_path_capacity_fps"`
	// EngageFraction engages aggregation when offered load exceeds
	// EngageFraction*PerPathCapacityFPS. Must be in (0,1].
	EngageFraction float64 `toml:"engage_fraction"`
	// DisengageFraction collapses aggregation to primary-only once offered load stays
	// below DisengageFraction*PerPathCapacityFPS for CollapseDwell. Must be in
	// [0,EngageFraction) — strictly below EngageFraction so the two form a hysteresis
	// band.
	DisengageFraction float64 `toml:"disengage_fraction"`
	// CollapseDwell is the sustained-low-load dwell before collapsing to primary-only
	// (hysteresis). Must be >= 0.
	CollapseDwell time.Duration `toml:"collapse_dwell"`
	// LoadTau is the offered-load rate estimator's time constant. Must be > 0.
	LoadTau time.Duration `toml:"load_tau"`

	// PacingEnabled turns per-path send-pacing on. When false the token buckets are
	// bypassed (a documented no-op — P0 §7 could not empirically size the pace in the
	// unmetered fixture), but PerPathCapacityFPS still drives the aggregation gate.
	PacingEnabled bool `toml:"pacing_enabled"`
	// PacingBurstFrames is the per-path token-bucket burst in frame slots. Must be > 0
	// when PacingEnabled.
	PacingBurstFrames float64 `toml:"pacing_burst_frames"`

	// WeightRTTFloor floors RTT in the weight formula (must be > 0 under weighted).
	WeightRTTFloor time.Duration `toml:"weight_rtt_floor"`
	// WeightLossFloor floors the loss term under the square root (must be > 0 under
	// weighted).
	WeightLossFloor float64 `toml:"weight_loss_floor"`
}

// FEC group-close deadline default (T24). Applied only when [fec] is enabled but
// leaves the deadline at its zero value, so a minimal `enabled = true` block emits
// parity for a partially-filled group promptly under low load rather than stranding
// it until the size threshold fills.
const defaultFECDeadline = 5 * time.Millisecond

// defaultAdaptiveSafetyFactor mirrors adaptivefec.DefaultSafetyFactor (1.5), the
// simulation-proven controller default, restated here (with this cross-reference rather
// than an import) so a minimal `adaptive = true` block gets the proven tuning without
// coupling config to the controller package. NOTE: 1.5 sizes M for the mean loss with
// modest variance headroom; a tight residual SLA under a specific loss/geometry may need a
// higher factor (e.g. at 5% loss with K=10 the 1.5 default sizes M=1, ~1% residual — raise
// this to lift M and drive the residual down).
const defaultAdaptiveSafetyFactor = 1.5

// maxFECDeadline bounds the FEC group-close deadline at load time (T24, defect #4). It
// MUST stay at or below the multipath Bind's authoritative bound (bind.maxFECDeadline =
// resequencerTimeout/2 = 125ms): a group flushed by the deadline emits its parity
// `deadline` after opening, and the reconstructed frames must reach the receive
// resequencer BEFORE it skips the gap (its 250ms per-gap timeout) — otherwise recovery
// is structurally too late (the gap is skipped, the recovered frame dropped as past the
// release point) while /metrics would still count it reconstructed. Rejecting an
// over-large deadline at load makes that coupling fail-fast and explicit rather than
// silently defeating FEC. Kept in lockstep with bind.maxFECDeadline (the packages
// cannot import each other, so the value is mirrored with this cross-reference).
const maxFECDeadline = 125 * time.Millisecond

// maxFECShards mirrors the Reed-Solomon field limit enforced in internal/fec: a
// coding group carries at most 256 shards (data + parity) total over GF(2^8). It
// is restated here so config load fails fast on an over-large ratio at the right
// locus, with the same bound the codec enforces internally.
const maxFECShards = 256

// FEC configures the fixed-ratio Reed-Solomon forward-error-correction plane (T24).
// It is DISABLED by default (like pacing), so an existing config with no [fec] block
// runs the datapath exactly as before — FEC transparent, no parity on the wire. When
// enabled, each group of DataShards (K) inner datagrams is protected by ParityShards
// (M) parity frames, letting the receiver reconstruct up to M lost data frames per
// group at a fixed M/K parity overhead.
type FEC struct {
	// Enabled turns the FEC plane on. When false every other field is ignored and the
	// datapath carries no parity.
	Enabled bool `toml:"enabled"`
	// DataShards is K: the number of inner datagrams grouped before parity is emitted.
	// Must be >= 1 when enabled.
	DataShards int `toml:"data_shards"`
	// ParityShards is M: the parity frames emitted per group and the maximum number of
	// per-group data losses the receiver can recover. Must be >= 1 when enabled. In
	// adaptive mode it is the CEILING (and the receiver's decoder cardinality) — the
	// controller drives the per-group parity in [0,ParityShards] to track measured loss.
	ParityShards int `toml:"parity_shards"`
	// Deadline bounds grouping latency: a partially-filled group is flushed (parity
	// emitted over its current data frames) once this much time has elapsed since its
	// first frame. Defaults to defaultFECDeadline when enabled and left zero; must be
	// > 0 after defaulting.
	Deadline time.Duration `toml:"deadline"`
	// Adaptive opts the send-side FEC into the closed-loop controller (T27/T29): the
	// per-group parity count tracks the measured per-path loss instead of standing at
	// the fixed ParityShards ratio, so a clean path spends near-zero overhead while a
	// lossy one is masked. It is OFF by default, so an existing [fec] block keeps the
	// fixed-ratio behaviour (T24) byte-for-byte. When true, ParityShards is reinterpreted
	// as the controller's parity ceiling (see above); the receiver is unchanged because a
	// group coded with fewer parity shards decodes against the ParityShards-ceiling codec
	// unchanged (klauspost's parity is prefix-consistent).
	Adaptive bool `toml:"adaptive"`
	// SafetyFactor is the adaptive controller's headroom multiplier over the measured mean
	// loss the per-group parity is sized to mask (adaptivefec.Config.SafetyFactor). It is
	// the residual-loss LEVER: the controller sizes M so M/(K+M) >= SafetyFactor*loss, so a
	// higher factor spends more parity to keep the post-recovery residual under a tighter
	// bound against binomial per-group variance. Applies only in adaptive mode; defaults to
	// defaultAdaptiveSafetyFactor (the simulation-proven controller default) when left zero,
	// and must be >= 1. Ignored (and must stay zero) in fixed mode.
	SafetyFactor float64 `toml:"safety_factor"`
}

// applyDefaults fills the group-close deadline when FEC is enabled and the deadline
// was left at zero. It is a no-op for a disabled block, so a config that never turns
// FEC on keeps an empty [fec] surface.
func (f *FEC) applyDefaults() {
	if !f.Enabled {
		return
	}
	if f.Deadline == 0 {
		f.Deadline = defaultFECDeadline
	}
	if f.Adaptive && f.SafetyFactor == 0 {
		f.SafetyFactor = defaultAdaptiveSafetyFactor
	}
}

// validate enforces the FEC ratio invariants, failing fast so a mis-tuned parity
// ratio is rejected at load rather than panicking the codec at runtime. A disabled
// block needs no tuning. The bounds mirror internal/fec's own Config.validate.
func (f FEC) validate() error {
	if !f.Enabled {
		if f.Adaptive {
			return errors.New("fec.adaptive = true requires fec.enabled = true (adaptive FEC is meaningless with the plane off)")
		}
		return nil
	}
	if f.DataShards < 1 {
		return fmt.Errorf("fec.data_shards must be >= 1 when FEC is enabled, got %d", f.DataShards)
	}
	if f.ParityShards < 1 {
		return fmt.Errorf("fec.parity_shards must be >= 1 when FEC is enabled, got %d", f.ParityShards)
	}
	if f.DataShards+f.ParityShards > maxFECShards {
		return fmt.Errorf("fec.data_shards + fec.parity_shards must be <= %d (Reed-Solomon field limit), got %d", maxFECShards, f.DataShards+f.ParityShards)
	}
	if f.Deadline <= 0 {
		return fmt.Errorf("fec.deadline must be > 0 when FEC is enabled, got %s", f.Deadline)
	}
	if f.Deadline > maxFECDeadline {
		return fmt.Errorf("fec.deadline must be <= %s (safely below the receive resequencer's per-gap timeout so deadline-flushed recovery lands before the gap is skipped), got %s", maxFECDeadline, f.Deadline)
	}
	if f.Adaptive {
		// SafetyFactor is defaulted to defaultAdaptiveSafetyFactor when zero, so a value
		// below 1 here is an explicit mis-set: the controller requires >= 1 (masking less
		// than the mean loss is nonsensical), matching adaptivefec.Config.Validate.
		if f.SafetyFactor < 1 {
			return fmt.Errorf("fec.safety_factor must be >= 1 in adaptive mode, got %g", f.SafetyFactor)
		}
	} else if f.SafetyFactor != 0 {
		return fmt.Errorf("fec.safety_factor is only meaningful in adaptive mode (set fec.adaptive = true), got %g", f.SafetyFactor)
	}
	return nil
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
	// DestAddr is the OPTIONAL per-path concentrator endpoint ("ip:port") the
	// edge sends this path's datagrams to. It is unset on the concentrator (which
	// learns edge endpoints from traffic) and optional on the edge: when a single
	// public concentrator IP fronts all uplinks (the common source-routed case)
	// the peer's wireguard endpoint is reused for every path; when the paths reach
	// the concentrator on distinct addresses, set dest_addr per path.
	DestAddr netip.AddrPort `toml:"-"`
	// DestAddrRaw is the TOML string form of DestAddr; parsed in normalize.
	DestAddrRaw string `toml:"dest_addr"`
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
	}
	c.Amnezia.applyDefaults()
	c.Scheduler.applyDefaults()
	c.FEC.applyDefaults()
	return nil
}

// applyDefaults selects active-backup when no policy is given and, only under the
// weighted policy, fills any weighted knob left at its zero value with its default.
// It is a no-op for the active-backup policy: those weighted knobs are inert there,
// so leaving them zero keeps the config surface for a P1 deployment empty.
func (s *SchedulerConfig) applyDefaults() {
	if s.Policy == "" {
		s.Policy = PolicyActiveBackup
	}
	if s.Policy != PolicyWeighted {
		return
	}
	if s.PerPathCapacityFPS == 0 {
		s.PerPathCapacityFPS = defaultPerPathCapacityFPS
	}
	if s.EngageFraction == 0 {
		s.EngageFraction = defaultEngageFraction
	}
	if s.DisengageFraction == 0 {
		s.DisengageFraction = defaultDisengageFraction
	}
	if s.CollapseDwell == 0 {
		s.CollapseDwell = defaultCollapseDwell
	}
	if s.LoadTau == 0 {
		s.LoadTau = defaultLoadTau
	}
	if s.PacingBurstFrames == 0 {
		s.PacingBurstFrames = defaultPacingBurstFrames
	}
	if s.WeightRTTFloor == 0 {
		s.WeightRTTFloor = defaultWeightRTTFloor
	}
	if s.WeightLossFloor == 0 {
		s.WeightLossFloor = defaultWeightLossFloor
	}
}

// validate enforces the scheduler policy invariants. active-backup needs no tuning.
// The weighted policy fails fast on any out-of-range knob so a mis-tuned
// aggregation policy is rejected at load rather than misbehaving at runtime (the
// hysteresis band, in particular, is only a band when disengage < engage).
func (s SchedulerConfig) validate() error {
	if !s.Policy.valid() {
		return fmt.Errorf("scheduler.policy must be %q or %q, got %q", PolicyActiveBackup, PolicyWeighted, s.Policy)
	}
	if s.Policy != PolicyWeighted {
		return nil
	}
	if s.PerPathCapacityFPS <= 0 {
		return fmt.Errorf("scheduler.per_path_capacity_fps must be > 0 under the weighted policy, got %g", s.PerPathCapacityFPS)
	}
	if s.EngageFraction <= 0 || s.EngageFraction > 1 {
		return fmt.Errorf("scheduler.engage_fraction must be in (0,1], got %g", s.EngageFraction)
	}
	if s.DisengageFraction < 0 || s.DisengageFraction >= s.EngageFraction {
		return fmt.Errorf("scheduler.disengage_fraction must be in [0,engage_fraction=%g) to form a hysteresis band, got %g", s.EngageFraction, s.DisengageFraction)
	}
	if s.CollapseDwell < 0 {
		return fmt.Errorf("scheduler.collapse_dwell must be >= 0, got %s", s.CollapseDwell)
	}
	if s.LoadTau <= 0 {
		return fmt.Errorf("scheduler.load_tau must be > 0, got %s", s.LoadTau)
	}
	if s.PacingEnabled && s.PacingBurstFrames <= 0 {
		return fmt.Errorf("scheduler.pacing_burst_frames must be > 0 when pacing is enabled, got %g", s.PacingBurstFrames)
	}
	if s.WeightRTTFloor <= 0 {
		return fmt.Errorf("scheduler.weight_rtt_floor must be > 0 under the weighted policy, got %s", s.WeightRTTFloor)
	}
	if s.WeightLossFloor <= 0 {
		return fmt.Errorf("scheduler.weight_loss_floor must be > 0 under the weighted policy, got %g", s.WeightLossFloor)
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
	if err := c.Scheduler.validate(); err != nil {
		return err
	}
	if err := c.FEC.validate(); err != nil {
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
