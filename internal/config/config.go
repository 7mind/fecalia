package config

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net"
	"net/netip"
	"strconv"
	"strings"
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
	DNS       DNS             `toml:"dns"`
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
	// WeightedCapacitySane is the Q52 WARN-arm capacity-sanity verdict (T144), computed
	// by normalize() and never read from TOML: nil when scheduler.policy is not
	// "weighted" (not applicable — the wanbond_weighted_capacity_sane metric family is
	// absent and no startup WARN is possible), otherwise a non-nil bool — true when
	// EVERY path declares link_bandwidth (SANE-VERIFIED: gauge=1, no WARN — by the time
	// Load returns, the T142 hard-fail guard has necessarily also passed, since a
	// declared-but-inconsistent path would have already aborted Load), false when at
	// least one path's link_bandwidth is undeclared (UNVERIFIABLE: gauge=0, one startup
	// WARN — covering BOTH "no path declares it" and a PARTIAL declaration, the latter
	// reachable whenever pacing is disabled since deriveWeightedPacingFromBDP then
	// no-ops and never rejects it). See weightedCapacitySane.
	WeightedCapacitySane *bool `toml:"-"`
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
	// (hysteresis). Must be >= 0. Parsed from CollapseDwellRaw in normalize.
	CollapseDwell time.Duration `toml:"-"`
	// CollapseDwellRaw is the TOML Go-duration string form of CollapseDwell, e.g.
	// "2s" (D43). Parsed in normalize; an unparseable value fails fast, mirroring
	// Path.LinkRTTRaw.
	CollapseDwellRaw string `toml:"collapse_dwell"`
	// LoadTau is the offered-load rate estimator's time constant. Must be > 0. Parsed
	// from LoadTauRaw in normalize.
	LoadTau time.Duration `toml:"-"`
	// LoadTauRaw is the TOML Go-duration string form of LoadTau, e.g. "200ms" (D43).
	// Parsed in normalize; an unparseable value fails fast.
	LoadTauRaw string `toml:"load_tau"`

	// PacingEnabled turns per-path send-pacing on. When false the token buckets are
	// bypassed (a documented no-op — P0 §7 could not empirically size the pace in the
	// unmetered fixture), but PerPathCapacityFPS still drives the aggregation gate.
	PacingEnabled bool `toml:"pacing_enabled"`
	// PacingBurstFrames is the per-path token-bucket burst in frame slots. Must be > 0
	// when PacingEnabled.
	PacingBurstFrames float64 `toml:"pacing_burst_frames"`

	// WeightRTTFloor floors RTT in the weight formula (must be > 0 under weighted).
	// Parsed from WeightRTTFloorRaw in normalize.
	WeightRTTFloor time.Duration `toml:"-"`
	// WeightRTTFloorRaw is the TOML Go-duration string form of WeightRTTFloor, e.g.
	// "1ms" (D43). Parsed in normalize; an unparseable value fails fast.
	WeightRTTFloorRaw string `toml:"weight_rtt_floor"`
	// WeightLossFloor floors the loss term under the square root (must be > 0 under
	// weighted).
	WeightLossFloor float64 `toml:"weight_loss_floor"`
}

// BDPSizing is a per-path pacing sizing derived from a MEASURED path, in the
// frame-selection-slot units the weighted scheduler pace uses (defect D22). It is the
// empirical alternative to the synthetic frame-count defaults: CapacityFPS replaces
// PerPathCapacityFPS and BurstFrames replaces PacingBurstFrames.
type BDPSizing struct {
	// CapacityFPS is the sustained per-path frame rate the measured bottleneck bandwidth
	// supports (the token-bucket refill rate and aggregation-gate denominator).
	CapacityFPS float64
	// BurstFrames is the bandwidth-delay product expressed in frames (bandwidth x RTT,
	// i.e. one RTT of in-flight frames): the token-bucket burst that absorbs one RTT of
	// jitter without building a standing queue.
	BurstFrames float64
}

// defaultAvgWireFrameBytes is the conservative average on-wire outer-frame size used
// when deriving the per-path pace from an operator-declared bandwidth at config load
// (T53): a full IPv4 path MTU, since a full-MTU DATA datagram occupies about one path
// MTU on the wire. It mirrors bind.DefaultPathMTU (1500) rather than importing it —
// internal/bind imports internal/config, so config cannot import bind without a cycle
// (the same mirror-with-cross-reference pattern as maxFECDeadline above). Sizing
// capacity with the full-MTU frame is the conservative floor: smaller average frames
// would yield a HIGHER frame rate, so this never over-paces a path.
const defaultAvgWireFrameBytes = 1500.0

// SizePacingFromBDP derives the weighted scheduler's per-path pacing parameters from a
// measured path instead of the synthetic frame-count default (defect D22). The shipped
// defaultPerPathCapacityFPS (10000, ~115 Mbit/s at full MTU) sits far above a realistic
// slow uplink, so the aggregation gate may never engage and — with pacing enabled — the
// pace never binds; deriving capacity from the measured bottleneck bandwidth fixes both.
//
// capacity_fps = bandwidth / (8 * avg wire frame bytes) — frames/s the link sustains.
// burst_frames = capacity_fps * RTT — one RTT of in-flight frames (equivalently
// BDP_bytes / avg wire frame bytes, since the bandwidth-delay product BDP = bandwidth * RTT).
//
// bandwidthBitsPerSec is the measured bottleneck bandwidth (bits/s), rtt the measured
// path RTT, and avgWireFrameBytes the average on-wire outer-frame size (a full-MTU
// datagram plus frame.DataOverhead is the conservative choice). It fails fast on a
// non-positive input rather than emitting a nonsensical (zero/negative/Inf) sizing.
func SizePacingFromBDP(bandwidthBitsPerSec float64, rtt time.Duration, avgWireFrameBytes float64) (BDPSizing, error) {
	if bandwidthBitsPerSec <= 0 {
		return BDPSizing{}, fmt.Errorf("config: BDP sizing bandwidth must be > 0 bit/s, got %g", bandwidthBitsPerSec)
	}
	if rtt <= 0 {
		return BDPSizing{}, fmt.Errorf("config: BDP sizing RTT must be > 0, got %s", rtt)
	}
	if avgWireFrameBytes <= 0 {
		return BDPSizing{}, fmt.Errorf("config: BDP sizing average wire frame size must be > 0 bytes, got %g", avgWireFrameBytes)
	}
	const bitsPerByte = 8.0
	capacityFPS := bandwidthBitsPerSec / (bitsPerByte * avgWireFrameBytes)
	burstFrames := capacityFPS * rtt.Seconds()
	return BDPSizing{CapacityFPS: capacityFPS, BurstFrames: burstFrames}, nil
}

// bandwidthUnit is a recognised link_bandwidth suffix and its bit/s multiplier.
type bandwidthUnit struct {
	suffix string
	mult   float64
}

// bandwidthUnits are the accepted operator-facing bandwidth suffixes, longest-first so
// "gbit" is matched before "bit". SI decimal multipliers (k/M/G = 1e3/1e6/1e9) over a
// bit/s base; "bps" is accepted as an alias of "bit" (and "kbps"/"mbps"/"gbps" likewise).
var bandwidthUnits = []bandwidthUnit{
	{"gbit", 1e9}, {"gbps", 1e9},
	{"mbit", 1e6}, {"mbps", 1e6},
	{"kbit", 1e3}, {"kbps", 1e3},
	{"bit", 1}, {"bps", 1},
}

// parseBandwidth parses an operator-declared link bandwidth such as "50Mbit", "1Gbit",
// or "500kbit" into bits per second. It requires an explicit bit/s unit suffix so a
// bare unitless number cannot be silently misread, and fails fast on an empty or
// otherwise unparseable value — a mistyped bandwidth is rejected at config load.
func parseBandwidth(s string) (float64, error) {
	lower := strings.ToLower(strings.TrimSpace(s))
	if lower == "" {
		return 0, errors.New("empty bandwidth")
	}
	for _, u := range bandwidthUnits {
		if !strings.HasSuffix(lower, u.suffix) {
			continue
		}
		num := strings.TrimSpace(lower[:len(lower)-len(u.suffix)])
		val, err := strconv.ParseFloat(num, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid number %q", num)
		}
		return val * u.mult, nil
	}
	return 0, fmt.Errorf("missing bit/s unit suffix (want e.g. %q)", "50Mbit")
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
	// > 0 after defaulting. Parsed from DeadlineRaw in normalize.
	Deadline time.Duration `toml:"-"`
	// DeadlineRaw is the TOML Go-duration string form of Deadline, e.g. "5ms" (D43).
	// Parsed in normalize; an unparseable value fails fast, mirroring Path.LinkRTTRaw.
	DeadlineRaw string `toml:"deadline"`
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
	// defaultAdaptiveSafetyFactor (the simulation-proven controller default) when left zero
	// AND target_residual is unset, and must be >= 1. Ignored (and must stay zero) in fixed
	// mode, and mutually exclusive with target_residual (set one, not both).
	SafetyFactor float64 `toml:"safety_factor"`
	// TargetResidual is the adaptive controller's target POST-RECOVERY residual-loss SLA
	// (a fraction in (0,1)) and the PRIMARY adaptive sizing surface (D26/T46): the
	// controller derives the per-group parity M by inverting the binomial residual model
	// E[max(0,D-M)]/K (D ~ Bin(K, smoothed loss)) to the smallest M meeting this target,
	// capped at the parity ceiling (parity_shards). It SUPERSEDES safety_factor — set
	// EITHER a residual SLA (target_residual, recommended: it maps an operator's loss
	// budget directly to redundancy) OR the bare headroom multiplier (safety_factor), never
	// both. Applies only in adaptive mode; unset (0) selects the safety_factor path. Must
	// be in (0,1) when set. Ignored (and must stay zero) in fixed mode.
	TargetResidual float64 `toml:"target_residual"`
}

// parseDurations parses DeadlineRaw into the typed Deadline field (D43), mirroring
// the Path.LinkRTTRaw precedent: go-toml/v2 cannot decode a TOML string into a bare
// time.Duration, so the documented `deadline = "5ms"` form would otherwise fail to
// load. An empty DeadlineRaw leaves Deadline at zero so applyDefaults' zero-check
// still fills defaultFECDeadline. Only the parse itself is fail-fast here (unparseable
// duration syntax); the >0/<=maxFECDeadline range checks stay in validate(), unchanged.
func (f *FEC) parseDurations() error {
	if f.DeadlineRaw != "" {
		d, err := time.ParseDuration(f.DeadlineRaw)
		if err != nil {
			return fmt.Errorf("fec.deadline: invalid duration %q: %w", f.DeadlineRaw, err)
		}
		f.Deadline = d
	}
	return nil
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
	// The SafetyFactor default is applied only in the legacy sizing mode: when
	// target_residual is set it is the primary surface and safety_factor stays inert
	// (0), so defaulting it would spuriously trip the mutual-exclusion check.
	if f.Adaptive && f.TargetResidual == 0 && f.SafetyFactor == 0 {
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
		if f.TargetResidual != 0 {
			// Residual-SLA sizing mode (primary): target_residual governs and safety_factor
			// stays inert, so setting both is an ambiguous mis-config — reject it. NaN is
			// caught explicitly because every ordered comparison against it is false.
			if math.IsNaN(f.TargetResidual) || f.TargetResidual <= 0 || f.TargetResidual >= 1 {
				return fmt.Errorf("fec.target_residual must be a finite value in (0,1), got %g", f.TargetResidual)
			}
			if f.SafetyFactor != 0 {
				return fmt.Errorf("fec.safety_factor and fec.target_residual are mutually exclusive; target_residual is the primary residual-SLA surface — set one, not both")
			}
		} else if f.SafetyFactor < 1 {
			// SafetyFactor is defaulted to defaultAdaptiveSafetyFactor when zero, so a value
			// below 1 here is an explicit mis-set: the controller requires >= 1 (masking less
			// than the mean loss is nonsensical), matching adaptivefec.Config.Validate.
			return fmt.Errorf("fec.safety_factor must be >= 1 in adaptive mode, got %g", f.SafetyFactor)
		}
	} else if f.SafetyFactor != 0 || f.TargetResidual != 0 {
		return fmt.Errorf("fec.safety_factor / fec.target_residual are only meaningful in adaptive mode (set fec.adaptive = true), got safety_factor=%g target_residual=%g", f.SafetyFactor, f.TargetResidual)
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
	// LinkBandwidthBitsPerSec is the OPERATOR-DECLARED bottleneck bandwidth of this
	// uplink in bits/s, parsed from LinkBandwidthRaw in normalize. It is used ONLY to
	// size the weighted scheduler's per-path pace from the bandwidth-delay product
	// (SizePacingFromBDP) when the weighted policy runs with pacing ENABLED (T53, Q20);
	// it is OPERATOR-DECLARED, not runtime-measured — wanbond never auto-tunes it live.
	// Zero means "not declared" (the synthetic default pace is kept).
	LinkBandwidthBitsPerSec float64 `toml:"-"`
	// LinkBandwidthRaw is the TOML string form of the declared bandwidth, e.g.
	// "50Mbit" / "1Gbit" / "500kbit" (SI bit/s units; the "bit" suffix may be written
	// "bps"). Parsed in normalize; a non-positive or unparseable value fails fast.
	LinkBandwidthRaw string `toml:"link_bandwidth"`
	// LinkRTT is the OPERATOR-DECLARED baseline RTT of this uplink, parsed from
	// LinkRTTRaw in normalize. It is the delay term of the bandwidth-delay-product pace
	// burst (one RTT of in-flight frames); required (> 0) when LinkBandwidth is set and
	// pacing is enabled under the weighted policy, ignored otherwise.
	LinkRTT time.Duration `toml:"-"`
	// LinkRTTRaw is the TOML Go-duration string form of LinkRTT, e.g. "45ms". Parsed in
	// normalize; an unparseable or non-positive value fails fast.
	LinkRTTRaw string `toml:"link_rtt"`
	// Bind selects this path's bind mode (I5, Q42): "source", "device", or "auto".
	// Left empty in TOML, it falls back to the top-level Config.Bind default (itself
	// defaulted to BindModeAuto); normalize() resolves this field to its EFFECTIVE
	// value, so after Load it always holds one of the three valid modes, never
	// empty. See BindMode.
	Bind BindMode `toml:"bind"`
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
		if p.LinkBandwidthRaw != "" {
			bw, err := parseBandwidth(p.LinkBandwidthRaw)
			if err != nil {
				return fmt.Errorf("path %q: invalid link_bandwidth %q: %w", p.Name, p.LinkBandwidthRaw, err)
			}
			if bw <= 0 {
				return fmt.Errorf("path %q: link_bandwidth must be > 0, got %q", p.Name, p.LinkBandwidthRaw)
			}
			p.LinkBandwidthBitsPerSec = bw
		}
		if p.LinkRTTRaw != "" {
			rtt, err := time.ParseDuration(p.LinkRTTRaw)
			if err != nil {
				return fmt.Errorf("path %q: invalid link_rtt %q: %w", p.Name, p.LinkRTTRaw, err)
			}
			if rtt <= 0 {
				return fmt.Errorf("path %q: link_rtt must be > 0, got %q", p.Name, p.LinkRTTRaw)
			}
			p.LinkRTT = rtt
		}
	}
	for i := range c.WireGuard.Peers {
		if err := c.WireGuard.Peers[i].resolveEndpoints(); err != nil {
			return fmt.Errorf("wireguard peer %d: %w", i, err)
		}
	}
	c.Amnezia.applyDefaults()
	// Derive the weighted pace from any operator-declared per-link bandwidth BEFORE
	// applyDefaults, so it can (a) see the raw zero PerPathCapacityFPS to distinguish an
	// explicit knob from the default and (b) set the derived capacity/burst that
	// applyDefaults then leaves intact (it only fills a knob left at zero). Under a
	// disabled or non-weighted pacing it is a no-op, so the synthetic default is kept.
	if err := c.deriveWeightedPacingFromBDP(); err != nil {
		return err
	}
	if err := c.Scheduler.parseDurations(); err != nil {
		return err
	}
	c.Scheduler.applyDefaults()
	if err := c.FEC.parseDurations(); err != nil {
		return err
	}
	c.FEC.applyDefaults()
	if err := c.DNS.applyDefaults(); err != nil {
		return err
	}
	// Computed LAST, after c.Scheduler.applyDefaults() has resolved an omitted
	// scheduler.policy to its default (PolicyActiveBackup) — see weightedCapacitySane's
	// doc for why this must run after every path's LinkBandwidthBitsPerSec is parsed.
	c.WeightedCapacitySane = c.weightedCapacitySane()
	return nil
}

// weightedCapacitySane computes the Q52 WARN-arm capacity-sanity verdict (T144, the
// complementary soft-verdict to T142's hard-fail guard): nil when the scheduler is not
// running the weighted policy (not applicable). Under the weighted policy it is true
// when EVERY path declares link_bandwidth — SANE-VERIFIED — and false when at least one
// does not — UNVERIFIABLE, covering both "no path declares it" and a PARTIAL
// declaration. It must run after normalize has parsed every path's LinkBandwidthRaw
// into LinkBandwidthBitsPerSec and after c.Scheduler.applyDefaults has resolved an
// omitted policy, so it sees the EFFECTIVE policy and EFFECTIVE per-path bandwidths.
func (c *Config) weightedCapacitySane() *bool {
	if c.Scheduler.Policy != PolicyWeighted {
		return nil
	}
	declared := 0
	for i := range c.Paths {
		if c.Paths[i].LinkBandwidthBitsPerSec > 0 {
			declared++
		}
	}
	sane := declared == len(c.Paths)
	return &sane
}

// deriveWeightedPacingFromBDP sizes the weighted scheduler's per-path pace from the
// operator-declared per-link bandwidth (T53, Q20) via SizePacingFromBDP instead of the
// synthetic defaultPerPathCapacityFPS. It runs only under the weighted policy with
// pacing ENABLED and at least one declared link_bandwidth; otherwise a declared
// bandwidth is inert (pacing ships DISABLED by default), so an unrelated config is
// untouched. The value is OPERATOR-DECLARED and fixed at load — NOT runtime auto-tuning
// (Q20 rejected a live control loop for the pilot).
//
// The scheduler carries a SINGLE reference per-path capacity applied to every path's
// token bucket, so a heterogeneous link set is sized to the BOTTLENECK: the slowest
// declared link governs the shared pace, because pacing any path faster than the
// slowest link's capacity would let that link build the very standing queue pacing
// exists to prevent. All paths must therefore declare a bandwidth (all-or-nothing) so
// the bottleneck is well-defined across the whole path set.
func (c *Config) deriveWeightedPacingFromBDP() error {
	s := &c.Scheduler
	if s.Policy != PolicyWeighted || !s.PacingEnabled {
		return nil
	}
	declared := 0
	for i := range c.Paths {
		if c.Paths[i].LinkBandwidthBitsPerSec > 0 {
			declared++
		}
	}
	if declared == 0 {
		return nil // no declared bandwidth: the synthetic default pace is preserved.
	}
	if declared != len(c.Paths) {
		return fmt.Errorf("scheduler pacing: link_bandwidth must be declared on ALL paths or none (got %d of %d) — the shared per-path pace is sized to the slowest declared link, which is undefined with a partial declaration", declared, len(c.Paths))
	}
	if s.PerPathCapacityFPS != 0 || s.PacingBurstFrames != 0 {
		return errors.New("scheduler.per_path_capacity_fps / pacing_burst_frames and per-path link_bandwidth are mutually exclusive: declare the link bandwidth (BDP-derived pace) OR set the raw frame-slot knobs, not both")
	}
	var bottleneck BDPSizing
	for i := range c.Paths {
		p := &c.Paths[i]
		if p.LinkRTT <= 0 {
			return fmt.Errorf("path %q: link_rtt is required (> 0) when link_bandwidth is set under weighted pacing — it is the delay term of the bandwidth-delay-product burst", p.Name)
		}
		sz, err := SizePacingFromBDP(p.LinkBandwidthBitsPerSec, p.LinkRTT, defaultAvgWireFrameBytes)
		if err != nil {
			return fmt.Errorf("path %q: %w", p.Name, err)
		}
		if i == 0 || sz.CapacityFPS < bottleneck.CapacityFPS {
			bottleneck = sz
		}
	}
	s.PerPathCapacityFPS = bottleneck.CapacityFPS
	s.PacingBurstFrames = bottleneck.BurstFrames
	return nil
}

// parseDurations parses the scheduler's Go-duration-string knobs (CollapseDwellRaw,
// LoadTauRaw, WeightRTTFloorRaw) into their typed time.Duration fields (D43), mirroring
// the Path.LinkRTTRaw precedent: go-toml/v2 cannot decode a TOML string into a bare
// time.Duration, so wanbond.example.toml's documented "2s"/"200ms"/"1ms" forms would
// otherwise fail to load. An empty Raw string leaves the typed field at its zero value,
// so applyDefaults' zero-check still fills the documented default. Only the parse
// itself is fail-fast here (unparseable duration syntax); the >=0/>0 range checks stay
// in validate(), unchanged — this runs unconditionally (regardless of Policy) exactly
// like the typed fields decoded unconditionally before this change.
func (s *SchedulerConfig) parseDurations() error {
	if s.CollapseDwellRaw != "" {
		d, err := time.ParseDuration(s.CollapseDwellRaw)
		if err != nil {
			return fmt.Errorf("scheduler.collapse_dwell: invalid duration %q: %w", s.CollapseDwellRaw, err)
		}
		s.CollapseDwell = d
	}
	if s.LoadTauRaw != "" {
		d, err := time.ParseDuration(s.LoadTauRaw)
		if err != nil {
			return fmt.Errorf("scheduler.load_tau: invalid duration %q: %w", s.LoadTauRaw, err)
		}
		s.LoadTau = d
	}
	if s.WeightRTTFloorRaw != "" {
		d, err := time.ParseDuration(s.WeightRTTFloorRaw)
		if err != nil {
			return fmt.Errorf("scheduler.weight_rtt_floor: invalid duration %q: %w", s.WeightRTTFloorRaw, err)
		}
		s.WeightRTTFloor = d
	}
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

// validateWeightedEngageAgainstBandwidth is the Q52/Q53 hard-fail guard (Option 3,
// scoped to the guard itself — per-path capacity auto-derive + BDP-sizing docs are
// G2/Q20's scope, see docs/install.md §3a, not restated here). Under the weighted
// policy, a path that declares link_bandwidth must be able to sustain the
// aggregation engage threshold, or weighted aggregation can mathematically never
// engage at line rate on that path — a misconfiguration that must fail fast at load
// rather than silently capping the path below its declared capacity forever. It runs
// AFTER normalize (deriveWeightedPacingFromBDP + SchedulerConfig.applyDefaults have
// already produced the EFFECTIVE EngageFraction/PerPathCapacityFPS), and computes the
// bandwidth-implied capacity with the SAME avg-wire-frame constant and math
// SizePacingFromBDP uses (capacity_fps = bandwidth / (8 * defaultAvgWireFrameBytes)),
// so the guard and the BDP derive can never disagree.
//
// With pacing ENABLED + declared bandwidth, deriveWeightedPacingFromBDP has already
// sized PerPathCapacityFPS to the BOTTLENECK link's implied capacity (the raw
// per_path_capacity_fps/pacing_burst_frames knobs are mutually exclusive with a
// declared bandwidth there, config.go's deriveWeightedPacingFromBDP), so
// EngageFraction <= 1 (enforced by SchedulerConfig.validate above) makes this guard
// structurally unable to fire. It therefore chiefly bites when pacing is DISABLED
// (the derive no-ops, leaving the synthetic defaultPerPathCapacityFPS=10000 standing
// against a much slower declared link) or when the raw knobs are set explicitly
// alongside a declared bandwidth.
func (c *Config) validateWeightedEngageAgainstBandwidth() error {
	if c.Scheduler.Policy != PolicyWeighted {
		return nil
	}
	threshold := c.Scheduler.EngageFraction * c.Scheduler.PerPathCapacityFPS
	for _, p := range c.Paths {
		if p.LinkBandwidthBitsPerSec <= 0 {
			continue
		}
		impliedCapacityFPS := p.LinkBandwidthBitsPerSec / (8 * defaultAvgWireFrameBytes)
		if threshold > impliedCapacityFPS {
			return fmt.Errorf("path %q: declared link_bandwidth %s implies a maximum sustained capacity of %.1f frames/s, "+
				"but scheduler.engage_fraction(%g) * per_path_capacity_fps(%.1f) = %.1f frames/s exceeds it — "+
				"weighted aggregation can mathematically never engage at line rate on this path; "+
				"lower scheduler.per_path_capacity_fps, enable scheduler.pacing_enabled to auto-derive it from "+
				"link_bandwidth, or correct link_bandwidth",
				p.Name, p.LinkBandwidthRaw, impliedCapacityFPS, c.Scheduler.EngageFraction, c.Scheduler.PerPathCapacityFPS, threshold)
		}
	}
	return nil
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
	}
	if !c.WireGuard.PrivateKey.IsSet() {
		return errors.New("wireguard.private_key is required")
	}
	if len(c.WireGuard.Peers) == 0 {
		return errors.New("at least one wireguard peer is required")
	}
	// D59: at most one peer may carry mode = "default-route" — WireGuard
	// cryptokey routing makes overlapping allowed_ips last-writer-wins, so two
	// full-tunnel peers would be a silent misconfig regardless of role. Checked
	// here, ahead of the edge single-peer cap and the concentrator per-peer mode
	// rejection below, both of which currently make a multi-default-route-peer
	// config unreachable via Load: the edge caps at one peer (next check) and the
	// concentrator rejects mode = "default-route" on every peer (per-peer loop
	// below), so today only a single edge peer can ever carry the mode at all.
	// This guard is enforced directly anyway so it stays correct if either cap is
	// ever relaxed later.
	defaultRoutePeer := -1
	for i, peer := range c.WireGuard.Peers {
		if peer.Mode != PeerModeDefaultRoute {
			continue
		}
		if defaultRoutePeer != -1 {
			return fmt.Errorf("wireguard peers %s and %s both carry mode = %q; at most one peer may be the full-tunnel default route",
				peerLabel(defaultRoutePeer, c.WireGuard.Peers[defaultRoutePeer].Name), peerLabel(i, peer.Name), PeerModeDefaultRoute)
		}
		defaultRoutePeer = i
	}
	// The edge dials exactly one concentrator peer per process (Q21); multi-peer
	// configs are concentrator-only scope. Check this before the per-peer loop
	// below so an edge config with >1 peer is rejected with this scope-explaining
	// message regardless of whether the peers are otherwise well-formed.
	if c.Role == RoleEdge && len(c.WireGuard.Peers) > 1 {
		return fmt.Errorf("edge role supports exactly one wireguard peer per process (got %d); the edge dials a single concentrator peer, multi-peer configs are concentrator-only", len(c.WireGuard.Peers))
	}
	// v4DefaultPeer/v6DefaultPeer track which peer (if any) has already claimed
	// the literal default route for that address family (D59): a second peer
	// duplicating 0.0.0.0/0 or ::/0 is rejected here at LOAD, rather than
	// producing a last-writer-wins UAPI allowed_ip= clash at the engine.
	v4DefaultPeer := -1
	v6DefaultPeer := -1
	for i, peer := range c.WireGuard.Peers {
		if !peer.PublicKey.IsSet() {
			return fmt.Errorf("wireguard peer %d: public_key is required", i)
		}
		// EndpointSpecs (not Endpoints) is the "any endpoint entry configured" check:
		// a hostname-only peer contributes nothing to Endpoints at load time (Q30 —
		// resolution is deferred to runtime) but must still count as configured.
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
		var v4SeenInPeer, v6SeenInPeer bool
		for _, raw := range peer.AllowedIPs {
			prefix, err := netip.ParsePrefix(raw)
			if err != nil {
				return fmt.Errorf("%s: invalid allowed_ips entry %q: %w", peerLabel(i, peer.Name), raw, err)
			}
			if prefix.Bits() != 0 {
				continue
			}
			// D59: a literal /0 entry is the full default route for its address
			// family — reject a second occurrence, both within this peer's own
			// allowed_ips (a redundant duplicate) and across peers (WireGuard
			// cryptokey routing makes overlapping allowed_ips last-writer-wins, a
			// silent misconfig).
			if prefix.Addr().Is4() {
				if v4SeenInPeer {
					return fmt.Errorf("%s: duplicate 0.0.0.0/0 entry in allowed_ips", peerLabel(i, peer.Name))
				}
				v4SeenInPeer = true
				if v4DefaultPeer != -1 {
					return fmt.Errorf("wireguard peers %s and %s both list 0.0.0.0/0 in allowed_ips; WireGuard cryptokey routing makes overlapping allowed_ips last-writer-wins",
						peerLabel(v4DefaultPeer, c.WireGuard.Peers[v4DefaultPeer].Name), peerLabel(i, peer.Name))
				}
				v4DefaultPeer = i
			} else {
				if v6SeenInPeer {
					return fmt.Errorf("%s: duplicate ::/0 entry in allowed_ips", peerLabel(i, peer.Name))
				}
				v6SeenInPeer = true
				if v6DefaultPeer != -1 {
					return fmt.Errorf("wireguard peers %s and %s both list ::/0 in allowed_ips; WireGuard cryptokey routing makes overlapping allowed_ips last-writer-wins",
						peerLabel(v6DefaultPeer, c.WireGuard.Peers[v6DefaultPeer].Name), peerLabel(i, peer.Name))
				}
				v6DefaultPeer = i
			}
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
	if err := c.validateWeightedEngageAgainstBandwidth(); err != nil {
		return err
	}
	if err := c.FEC.validate(); err != nil {
		return err
	}
	if err := c.DNS.validate(); err != nil {
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
