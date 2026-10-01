// Package monitor defines the JSON wire-format DTO the monitoring HTTP endpoint
// (W2) serves to the frontend, and BuildSnapshot, which encodes a metrics.Source
// read model into it. It depends ONLY on the metrics.Source interface — never on
// internal/device — so it can be exercised against a fake Source in tests with no
// live engine/bind wiring.
package monitor

import (
	"net/netip"
	"time"

	"github.com/7mind/wanbond/internal/metrics"
	"github.com/7mind/wanbond/internal/telemetry"
)

// AddressingSnapshot is the JSON encoding of one path's REDACTABLE addressing
// block (G21, Q61/Q62): the bound local source address and the current wire
// remote (on the concentrator role, the connected edge's observed source). It is
// present on a PathSnapshot ONLY when addressing is revealed (a loopback-bound
// monitor); on a non-loopback binding BuildSnapshot omits it entirely (the
// pointer is nil) and sets MonitorSnapshot.AddressingHidden — the redaction
// happens server-side, before serialization, so a non-loopback observer never
// receives these values. See BuildSnapshot.
type AddressingSnapshot struct {
	Source string `json:"source"`
	Remote string `json:"remote"`
}

// PathSnapshot is the JSON encoding of one per-(peer,path) entry
// (metrics.PathSnapshot): traffic counters fused with the telemetry plane's
// quality estimate and liveness verdict. Durations are rendered as float
// seconds, matching the Prometheus exposition's *_seconds series.
type PathSnapshot struct {
	Name          string  `json:"name"`
	Peer          string  `json:"peer"`
	TxBytes       uint64  `json:"txBytes"`
	RxBytes       uint64  `json:"rxBytes"`
	ThroughputBps float64 `json:"throughputBps"`
	RTTSeconds    float64 `json:"rttSeconds"`
	JitterSeconds float64 `json:"jitterSeconds"`
	Loss          float64 `json:"loss"`
	Up            bool    `json:"up"`
	// BindMode is the path's runtime-resolved bind mode ("source"|"device"|
	// "auto"), and BoundDevice the resolved SO_BINDTODEVICE interface name (empty
	// when source-pinned) — both runtime-resolved and shown on ANY binding (Q60).
	// They ride the metrics.PathSnapshot pass-through (T220 wires the values); we
	// collapse the separate "source interface" candidate into BoundDevice (R242).
	BindMode    string `json:"bindMode"`
	BoundDevice string `json:"boundDevice"`
	// Addressing is the per-path REDACTABLE addressing block (Q61/Q62): non-nil
	// only when addressing is revealed (loopback binding); nil (omitted) on a
	// non-loopback binding. See AddressingSnapshot.
	Addressing *AddressingSnapshot `json:"addressing,omitempty"`
}

// ReseqSnapshot is the JSON encoding of one per-peer resequencer counter set
// (metrics.ReseqSnapshot, which embeds reseq.Stats verbatim).
type ReseqSnapshot struct {
	Peer           string `json:"peer"`
	Released       uint64 `json:"released"`
	DroppedDup     uint64 `json:"droppedDup"`
	DroppedOld     uint64 `json:"droppedOld"`
	DroppedSuspect uint64 `json:"droppedSuspect"`
	Skipped        uint64 `json:"skipped"`
	Resyncs        uint64 `json:"resyncs"`
	Rebaselines    uint64 `json:"rebaselines"`
	// HoL-stall / hold accounting (T242), mirrored verbatim
	// from reseq.Stats.
	Holds                 uint64 `json:"holds"`
	HoldNanos             uint64 `json:"holdNanos"`
	ArmedDeadlineUnixNano int64  `json:"armedDeadlineUnixNano"`
	ArmedWindowNanos      int64  `json:"armedWindowNanos"`
	DeadlineWakeups       uint64 `json:"deadlineWakeups"`
	GapFills              uint64 `json:"gapFills"`
}

// LaneSnapshot is the JSON encoding of one transport lane (bond.PathStats): a
// local path paired with one of the peer's. Rates are in bits per second, as
// PathSnapshot.ThroughputBps is. CapacityBps is 0 until the lane has
// demonstrated a capacity; the target is held just below it afterwards, and
// QueueDelaySeconds above ThresholdSeconds is what the lane takes for a queue.
type LaneSnapshot struct {
	Peer string `json:"peer"`
	// Path is the local path's name, RemotePath the peer's path id the lane
	// pairs it with, and Lane the transport's lane id (as in the `lane` label
	// of the wanbond_adaptive_* series).
	Path              string  `json:"path"`
	RemotePath        uint8   `json:"remotePath"`
	Lane              uint16  `json:"lane"`
	Up                bool    `json:"up"`
	Discovering       bool    `json:"discovering"`
	TargetBps         float64 `json:"targetBps"`
	SendBps           float64 `json:"sendBps"`
	DeliveryBps       float64 `json:"deliveryBps"`
	CapacityBps       float64 `json:"capacityBps"`
	RTTSeconds        float64 `json:"rttSeconds"`
	QueueDelaySeconds float64 `json:"queueDelaySeconds"`
	ThresholdSeconds  float64 `json:"thresholdSeconds"`
	InFlightBytes     int     `json:"inFlightBytes"`
	WindowBytes       int     `json:"windowBytes"`
	SentBytes         uint64  `json:"sentBytes"`
	AckedBytes        uint64  `json:"ackedBytes"`
	Repairs           uint64  `json:"repairs"`
	// The control's decisions (bond.Decisions), cumulative.
	DelaySignals       uint64 `json:"delaySignals"`
	LossSignals        uint64 `json:"lossSignals"`
	StallSignals       uint64 `json:"stallSignals"`
	DiscoveryCongested uint64 `json:"discoveryCongested"`
	DiscoveryPlateau   uint64 `json:"discoveryPlateau"`
	CapacityRemeasured uint64 `json:"capacityRemeasured"`
	CapacityDecays     uint64 `json:"capacityDecays"`
	Pulses             uint64 `json:"pulses"`
	PulseWins          uint64 `json:"pulseWins"`
	PulseLosses        uint64 `json:"pulseLosses"`
	Rediscoveries      uint64 `json:"rediscoveries"`
}

// TransportSnapshot is the JSON encoding of one peer's transport queue
// counters (bond.Snapshot), cumulative except InteractiveQueued.
type TransportSnapshot struct {
	Peer              string `json:"peer"`
	QueueDrops        uint64 `json:"queueDrops"`
	AdmissionDrops    uint64 `json:"admissionDrops"`
	AQMDrops          uint64 `json:"aqmDrops"`
	InteractiveDrops  uint64 `json:"interactiveDrops"`
	InteractiveQueued int    `json:"interactiveQueued"`
	Expired           uint64 `json:"expired"`
	Duplicates        uint64 `json:"duplicates"`
	CoalescedACKs     uint64 `json:"coalescedAcks"`
}

// SessionSnapshot is the JSON encoding of the connection-scoped WG-session
// snapshot (metrics.SessionSnapshot). LastHandshakeSeconds is the elapsed time
// since the peer's most recent completed handshake, zero when none has ever
// completed (Established is then false).
type SessionSnapshot struct {
	Established          bool    `json:"established"`
	LastHandshakeSeconds float64 `json:"lastHandshakeSeconds"`
}

// PeerSessionSnapshot is the JSON encoding of one bound peer's OWN WG-session
// health (metrics.PeerSessionSnapshot, T256/T257, G28/M106), mirroring the
// package-level peer-label rule (D58): Peer is meaningful only once 2+ peers are
// bound; a single-bound-peer snapshot still carries exactly one entry, with Peer
// "".
type PeerSessionSnapshot struct {
	Peer                 string  `json:"peer"`
	Established          bool    `json:"established"`
	LastHandshakeSeconds float64 `json:"lastHandshakeSeconds"`
}

// DaemonSnapshot is the JSON encoding of the process-scoped identity fields
// (G21, Q60): the effective role, the daemon version/build string, and the
// process uptime in seconds. Sourced from monitor.Info, shown on any binding.
type DaemonSnapshot struct {
	Role          string  `json:"role"`
	Version       string  `json:"version"`
	UptimeSeconds float64 `json:"uptimeSeconds"`
}

// EndpointSnapshot is one entry of the ordered hub-endpoint list with its
// active-vs-standby failover state (G21, Q61c). Address is inside the REDACTABLE
// addressing surface: on a non-loopback binding BuildSnapshot blanks it while
// preserving the ordered active/standby shape. Empty on the concentrator (which
// has no configured endpoint list) — the frontend omits the section.
//
// Peer attributes this entry to the bound edge peer whose OWN endpoint list it
// belongs to (T257, G28/M106), grouping the flat list into per-peer sections on a
// multi-exit edge — one ordered active/standby group per peer with a hub-failover
// controller — while following the metrics-package peer-label rule (D58): "" on a
// single-bound-peer config, so that shape stays byte-compatible with the
// pre-T257 flat list.
type EndpointSnapshot struct {
	Peer    string `json:"peer"`
	Address string `json:"address"`
	Active  bool   `json:"active"`
}

// Info is the monitor-local read seam for the identity/config/failover data the
// prometheus-facing metrics.Source deliberately does NOT carry (keeping that
// interface narrow — no new Prometheus series). It is supplied by the device
// layer (wired in T222) and consumed by BuildSnapshot. The zero value is the
// FAIL-CLOSED default (no daemon identity, no endpoints, empty fingerprint) that
// server.go passes until the real Info is threaded in T219/T222.
type Info struct {
	// Role, Version, UptimeSeconds populate DaemonSnapshot.
	Role          string
	Version       string
	UptimeSeconds float64
	// Uptime, when non-nil, is a LIVE provider for the process uptime evaluated
	// INSIDE BuildSnapshot on every snapshot (freshness, R242): the server holds one
	// Info for its whole life, so a plain UptimeSeconds captured at construction would
	// freeze at its near-zero boot value and a long-lived client would never see uptime
	// advance. The device layer (T222) supplies this closure; when it is nil the plain
	// UptimeSeconds value is used (the zero-value / test path).
	Uptime func() float64
	// WGPublicKeyFingerprint is the truncated (~10 base64 chars) fingerprint of the
	// local WG public key (Q63 — fingerprint ONLY, never the full key). Shown on any
	// binding.
	WGPublicKeyFingerprint string
	// Endpoints is a LIVE provider evaluated INSIDE BuildSnapshot on every snapshot
	// (freshness, R242): it returns the ordered hub-endpoint list with the active
	// entry flagged, grouped per-peer since T257 (each entry's Peer names the
	// owning edge peer, "" throughout on a single-bound-peer config), so a hub
	// failover after startup is reflected. Nil (or a nil return) means no endpoint
	// list (e.g. the concentrator role).
	Endpoints func() []EndpointSnapshot
	// ActiveExit is a LIVE provider (T257, G28/M106), evaluated INSIDE BuildSnapshot on
	// every snapshot (freshness, R242), reporting the name of the exit-capable peer
	// currently carrying the default route (the exit selector's ActiveExit(), T254).
	// It is a peer NAME, not an address, so it is NOT part of the redactable
	// addressing surface (never blanked). Nil (or a nil return of "") means no
	// active-exit concept applies: the concentrator role, or an edge with fewer
	// than two exit-capable peers / no default-route peer.
	ActiveExit func() string
	// ExitMode is the live operator target, distinct from ActiveExit.
	ExitMode func() string
	// ExitCapablePeers is the configured, config-order set of peer names that may
	// own the default route. It is empty off the edge role.
	ExitCapablePeers []string
}

// MonitorSnapshot is the JSON wire-format contract the monitoring HTTP
// endpoint (W2) serves to the frontend: a full point-in-time mirror of a
// metrics.Source read model, plus the G21 identity/addressing/failover surface.
// PeerNames and MultiPeer mirror the metrics package's peer-label rule (see
// internal/metrics/metrics.go): MultiPeer is true, and per-(peer,path)/
// Reseq entries carry a meaningful Peer, only when 2+ peers are
// bound; on a single-bound-peer Source, Peer is "" throughout. See BuildSnapshot.
type MonitorSnapshot struct {
	Paths []PathSnapshot `json:"paths"`
	// Lanes and Transport mirror metrics.Source.Adaptive(): every lane of
	// every bound peer, and each peer's queue counters.
	Lanes     []LaneSnapshot      `json:"lanes"`
	Transport []TransportSnapshot `json:"transport"`
	Reseq     []ReseqSnapshot     `json:"reseq"`
	Session   SessionSnapshot     `json:"session"`
	PeerNames []string            `json:"peerNames"`
	MultiPeer bool                `json:"multiPeer"`
	// Daemon carries the process identity (role/version/uptime, Q60).
	Daemon DaemonSnapshot `json:"daemon"`
	// Endpoints is the ordered hub-endpoint list with active/standby state
	// (Q61c), grouped per-peer since T257 (Endpoints[i].Peer names the owning
	// edge peer); endpoint addresses are blanked on a non-loopback binding
	// (redacted), while the ordered active/standby shape is preserved. Empty on
	// the concentrator role.
	Endpoints []EndpointSnapshot `json:"endpoints"`
	// PeerSessions mirrors metrics.Source.PeerSessions() (T256/T257, G28/M106):
	// one entry per bound peer's OWN WG-session health. Peer is meaningful only
	// once 2+ peers are bound (D58 back-compat rule) — a single-bound-peer
	// snapshot still carries exactly one entry, with Peer "".
	PeerSessions []PeerSessionSnapshot `json:"peerSessions"`
	// ActiveExit is the name of the exit-capable peer currently carrying the
	// default route on a multi-exit edge (T254/T257, G28/M106); "" on the
	// concentrator role and on an edge with no default-route ownership to
	// report. It is a peer NAME, never an address, so it is NOT redacted.
	ActiveExit string `json:"activeExit"`
	// ExitMode is "auto" or the configured exit name selected by the operator.
	ExitMode string `json:"exitMode"`
	// ExitCapablePeers is the configured, config-order set of peers eligible to
	// own the default route. The control applies only when this set has 2+ names;
	// it is not inferred from endpoint or session telemetry.
	ExitCapablePeers []string `json:"exitCapablePeers"`
	// WGPublicKeyFingerprint is the truncated local WG public-key fingerprint
	// (Q63 — fingerprint ONLY; there is deliberately NO full-key field). Present
	// on any binding.
	WGPublicKeyFingerprint string `json:"wgPublicKeyFingerprint"`
	// AddressingHidden is true when addressing is NOT revealed and the per-path
	// addressing blocks + endpoint addresses have been redacted server-side (Q62).
	// It is the negation of revealAddressing (loopbackBound OR the reveal opt-in),
	// so it can be false even on a non-loopback bind when the operator opted in.
	// The frontend renders a "hidden" placeholder; it never reconstructs the values.
	AddressingHidden bool `json:"addressingHidden"`
	// ExitControlAvailable is true for a verified loopback binding or a
	// token-authenticated non-loopback binding. It is independent of the
	// addressing-reveal setting.
	ExitControlAvailable bool `json:"exitControlAvailable"`
}

// addrString renders a netip.Addr, "" when unset/invalid (so a not-yet-bound
// path yields an empty string rather than the "invalid IP" sentinel).
func addrString(a netip.Addr) string {
	if a.IsValid() {
		return a.String()
	}
	return ""
}

// addrPortString renders a netip.AddrPort, "" when unset/invalid.
func addrPortString(a netip.AddrPort) string {
	if a.IsValid() {
		return a.String()
	}
	return ""
}

// BuildSnapshot reads src exactly once — one call each to Paths/Reseq/
// Session/PeerSessions/PeerNames — folds in the monitor.Info
// identity/failover seam (info.Endpoints and info.ActiveExit are each evaluated
// here, once, so the active-hub/active-exit state is fresh per snapshot), and
// marshals the result into the MonitorSnapshot wire format. telemetry.Estimate's
// RTT/Jitter and the session's LastHandshakeAge are rendered as float seconds,
// and MultiPeer is derived from len(PeerNames())>1, matching the metrics
// package's peer-label rule.
//
// revealAddressing gates the Q62/Q64 addressing surface SERVER-SIDE: when false
// (a non-loopback binding with no reveal opt-in), every per-path
// AddressingSnapshot is omitted (nil) and every endpoint Address is blanked while
// its active/standby shape is kept, and AddressingHidden is set — so a redacted
// value provably never reaches the wire. The truncated WG fingerprint (Q63) is
// NOT part of the redactable set: it is present on any binding, and there is
// deliberately no full key to gate.
//
// controlAvailable reports the server's authorization surface independently of
// revealAddressing; the latter controls only addressing redaction.
func BuildSnapshot(src metrics.Source, info Info, revealAddressing, controlAvailable bool) MonitorSnapshot {
	paths := src.Paths()
	reseqSnapshots := src.Reseq()
	session := src.Session()
	peerSessions := src.PeerSessions()
	peerNames := src.PeerNames()
	transports := src.Adaptive()

	// UptimeSeconds is LIVE when a provider is supplied (R242): evaluated here on every
	// snapshot so a long-lived client sees uptime advance rather than frozen at the
	// server-construction value. Falls back to the plain float (zero-value / test path).
	uptimeSeconds := info.UptimeSeconds
	if info.Uptime != nil {
		uptimeSeconds = info.Uptime()
	}

	// ActiveExit is LIVE when a provider is supplied (R242/T257): evaluated here on
	// every snapshot so a manual switch or auto-promotion after startup is
	// reflected. Falls back to "" (no provider / concentrator / single-exit edge).
	var activeExit string
	if info.ActiveExit != nil {
		activeExit = info.ActiveExit()
	}
	var exitMode string
	if info.ExitMode != nil {
		exitMode = info.ExitMode()
	}

	out := MonitorSnapshot{
		Paths:            make([]PathSnapshot, len(paths)),
		Lanes:            []LaneSnapshot{},
		Transport:        make([]TransportSnapshot, len(transports)),
		Reseq:            make([]ReseqSnapshot, len(reseqSnapshots)),
		PeerSessions:     make([]PeerSessionSnapshot, len(peerSessions)),
		ActiveExit:       activeExit,
		ExitMode:         exitMode,
		ExitCapablePeers: append([]string{}, info.ExitCapablePeers...),
		Session: SessionSnapshot{
			Established:          session.Established,
			LastHandshakeSeconds: session.LastHandshakeAge.Seconds(),
		},
		PeerNames: peerNames,
		MultiPeer: len(peerNames) > 1,
		Daemon: DaemonSnapshot{
			Role:          info.Role,
			Version:       info.Version,
			UptimeSeconds: uptimeSeconds,
		},
		WGPublicKeyFingerprint: info.WGPublicKeyFingerprint,
		AddressingHidden:       !revealAddressing,
		ExitControlAvailable:   controlAvailable,
	}

	for i, p := range paths {
		ps := PathSnapshot{
			Name:          p.Name,
			Peer:          p.Peer,
			TxBytes:       p.TxBytes,
			RxBytes:       p.RxBytes,
			ThroughputBps: p.ThroughputBitsPerSecond,
			RTTSeconds:    p.Estimate.RTT.Seconds(),
			JitterSeconds: p.Estimate.Jitter.Seconds(),
			Loss:          p.Estimate.Loss,
			Up:            p.State == telemetry.StateUp,
			BindMode:      p.BindMode,
			BoundDevice:   p.BoundDevice,
		}
		if revealAddressing {
			ps.Addressing = &AddressingSnapshot{
				Source: addrString(p.Source),
				Remote: addrPortString(p.Remote),
			}
		}
		out.Paths[i] = ps
	}

	if info.Endpoints != nil {
		eps := info.Endpoints()
		out.Endpoints = make([]EndpointSnapshot, len(eps))
		for i, e := range eps {
			if revealAddressing {
				out.Endpoints[i] = e
			} else {
				// Redact the address; keep the peer grouping and active/standby shape.
				out.Endpoints[i] = EndpointSnapshot{Peer: e.Peer, Active: e.Active}
			}
		}
	}

	for i, r := range reseqSnapshots {
		out.Reseq[i] = ReseqSnapshot{
			Peer:           r.Peer,
			Released:       r.Released,
			DroppedDup:     r.DroppedDup,
			DroppedOld:     r.DroppedOld,
			DroppedSuspect: r.DroppedSuspect,
			Skipped:        r.Skipped,
			Resyncs:        r.Resyncs,
			Rebaselines:    r.Rebaselines,
			Holds:          r.Holds,
			HoldNanos:      r.HoldNanos,

			ArmedDeadlineUnixNano: unixNanoOrZero(r.ArmedDeadline),
			ArmedWindowNanos:      r.ArmedWindow.Nanoseconds(),
			DeadlineWakeups:       r.DeadlineWakeups,
			GapFills:              r.GapFills,
		}
	}

	const bitsPerByte = 8
	for i, transport := range transports {
		s := transport.State
		out.Transport[i] = TransportSnapshot{
			Peer:              transport.Peer,
			QueueDrops:        s.QueueDrops,
			AdmissionDrops:    s.AdmissionDrops,
			AQMDrops:          s.AQMDrops,
			InteractiveDrops:  s.InteractiveQueueDrops,
			InteractiveQueued: s.InteractiveQueued,
			Expired:           s.Expired,
			Duplicates:        s.Duplicates,
			CoalescedACKs:     s.CoalescedACKs,
		}
		for _, lane := range s.Paths {
			d := lane.Decisions
			out.Lanes = append(out.Lanes, LaneSnapshot{
				Peer:               transport.Peer,
				Path:               transport.LanePaths[lane.Path],
				RemotePath:         uint8(lane.Path),
				Lane:               uint16(lane.Path),
				Up:                 lane.Up,
				Discovering:        lane.Discovering,
				TargetBps:          lane.Rate * bitsPerByte,
				SendBps:            lane.SendRate * bitsPerByte,
				DeliveryBps:        lane.DeliveryRate * bitsPerByte,
				CapacityBps:        lane.Capacity * bitsPerByte,
				RTTSeconds:         lane.RTT.Seconds(),
				QueueDelaySeconds:  lane.QueueDelay.Seconds(),
				ThresholdSeconds:   lane.Threshold.Seconds(),
				InFlightBytes:      lane.InFlight,
				WindowBytes:        lane.Window,
				SentBytes:          lane.Sent,
				AckedBytes:         lane.ACKed,
				Repairs:            lane.Retransmits,
				DelaySignals:       d.DelaySignals,
				LossSignals:        d.LossSignals,
				StallSignals:       d.StallSignals,
				DiscoveryCongested: d.DiscoveryCongested,
				DiscoveryPlateau:   d.DiscoveryPlateau,
				CapacityRemeasured: d.CapacityRemeasured,
				CapacityDecays:     d.CapacityDecays,
				Pulses:             d.Pulses,
				PulseWins:          d.PulseWins,
				PulseLosses:        d.PulseLosses,
				Rediscoveries:      d.Rediscoveries,
			})
		}
	}

	for i, ps := range peerSessions {
		out.PeerSessions[i] = PeerSessionSnapshot{
			Peer:                 ps.Peer,
			Established:          ps.Established,
			LastHandshakeSeconds: ps.LastHandshakeSeconds,
		}
	}

	return out
}

func unixNanoOrZero(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.UnixNano()
}
