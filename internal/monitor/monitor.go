// Package monitor defines the JSON wire-format DTO the monitoring HTTP endpoint
// (W2) serves to the frontend, and BuildSnapshot, which encodes a metrics.Source
// read model into it. It depends ONLY on the metrics.Source interface — never on
// internal/device — so it can be exercised against a fake Source in tests with no
// live engine/bind wiring.
package monitor

import (
	"github.com/7mind/wanbond/internal/metrics"
	"github.com/7mind/wanbond/internal/telemetry"
)

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
}

// FECSnapshot is the JSON encoding of one per-peer connection-scoped FEC
// counter set (metrics.FECSnapshot).
type FECSnapshot struct {
	Peer                 string  `json:"peer"`
	DataPackets          uint64  `json:"dataPackets"`
	RepairPackets        uint64  `json:"repairPackets"`
	RecoveredPackets     uint64  `json:"recoveredPackets"`
	UnrecoverablePackets uint64  `json:"unrecoverablePackets"`
	DataBytes            uint64  `json:"dataBytes"`
	RepairBytes          uint64  `json:"repairBytes"`
	ResidualLossRatio    float64 `json:"residualLossRatio"`
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
}

// AggregationSnapshot is the JSON encoding of one per-peer weighted-scheduler
// aggregation-gate snapshot (metrics.AggregationSnapshot). Absent for a peer
// whose scheduler exposes no gate (active-backup).
type AggregationSnapshot struct {
	Peer                  string  `json:"peer"`
	Aggregating           bool    `json:"aggregating"`
	OfferedLoadFPS        float64 `json:"offeredLoadFps"`
	EngageThresholdFPS    float64 `json:"engageThresholdFps"`
	DisengageThresholdFPS float64 `json:"disengageThresholdFps"`
}

// SessionSnapshot is the JSON encoding of the connection-scoped WG-session
// snapshot (metrics.SessionSnapshot). LastHandshakeSeconds is the elapsed time
// since the peer's most recent completed handshake, zero when none has ever
// completed (Established is then false).
type SessionSnapshot struct {
	Established          bool    `json:"established"`
	LastHandshakeSeconds float64 `json:"lastHandshakeSeconds"`
}

// MonitorSnapshot is the JSON wire-format contract the monitoring HTTP
// endpoint (W2) serves to the frontend: a full point-in-time mirror of a
// metrics.Source read model. PeerNames and MultiPeer mirror the metrics
// package's peer-label rule (see internal/metrics/metrics.go): MultiPeer is
// true, and per-(peer,path)/FEC/Reseq/Aggregation entries carry a meaningful
// Peer, only when 2+ peers are bound; on a single-bound-peer Source, Peer is
// "" throughout. See BuildSnapshot.
type MonitorSnapshot struct {
	Paths       []PathSnapshot        `json:"paths"`
	FEC         []FECSnapshot         `json:"fec"`
	Reseq       []ReseqSnapshot       `json:"reseq"`
	Aggregation []AggregationSnapshot `json:"aggregation"`
	Session     SessionSnapshot       `json:"session"`
	PeerNames   []string              `json:"peerNames"`
	MultiPeer   bool                  `json:"multiPeer"`
}

// BuildSnapshot reads src exactly once — one call each to Paths/FEC/Reseq/
// Aggregation/Session/PeerNames — and marshals the result into the
// MonitorSnapshot wire format: telemetry.Estimate's RTT/Jitter and the
// session's LastHandshakeAge are rendered as float seconds, and MultiPeer is
// derived from len(PeerNames())>1, matching the metrics package's peer-label
// rule (see internal/metrics/metrics.go).
func BuildSnapshot(src metrics.Source) MonitorSnapshot {
	paths := src.Paths()
	fec := src.FEC()
	reseqSnapshots := src.Reseq()
	aggregation := src.Aggregation()
	session := src.Session()
	peerNames := src.PeerNames()

	out := MonitorSnapshot{
		Paths:       make([]PathSnapshot, len(paths)),
		FEC:         make([]FECSnapshot, len(fec)),
		Reseq:       make([]ReseqSnapshot, len(reseqSnapshots)),
		Aggregation: make([]AggregationSnapshot, len(aggregation)),
		Session: SessionSnapshot{
			Established:          session.Established,
			LastHandshakeSeconds: session.LastHandshakeAge.Seconds(),
		},
		PeerNames: peerNames,
		MultiPeer: len(peerNames) > 1,
	}

	for i, p := range paths {
		out.Paths[i] = PathSnapshot{
			Name:          p.Name,
			Peer:          p.Peer,
			TxBytes:       p.TxBytes,
			RxBytes:       p.RxBytes,
			ThroughputBps: p.ThroughputBitsPerSecond,
			RTTSeconds:    p.Estimate.RTT.Seconds(),
			JitterSeconds: p.Estimate.Jitter.Seconds(),
			Loss:          p.Estimate.Loss,
			Up:            p.State == telemetry.StateUp,
		}
	}

	for i, f := range fec {
		out.FEC[i] = FECSnapshot{
			Peer:                 f.Peer,
			DataPackets:          f.DataPackets,
			RepairPackets:        f.RepairPackets,
			RecoveredPackets:     f.RecoveredPackets,
			UnrecoverablePackets: f.UnrecoverablePackets,
			DataBytes:            f.DataBytes,
			RepairBytes:          f.RepairBytes,
			ResidualLossRatio:    f.ResidualLossRatio,
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
		}
	}

	for i, a := range aggregation {
		out.Aggregation[i] = AggregationSnapshot{
			Peer:                  a.Peer,
			Aggregating:           a.Aggregating,
			OfferedLoadFPS:        a.OfferedLoadFPS,
			EngageThresholdFPS:    a.EngageThresholdFPS,
			DisengageThresholdFPS: a.DisengageThresholdFPS,
		}
	}

	return out
}
