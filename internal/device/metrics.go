package device

import (
	"sync"

	"github.com/7mind/wanbond/internal/bind"
	"github.com/7mind/wanbond/internal/metrics"
	"github.com/7mind/wanbond/internal/telemetry"
)

// trafficProvider is the read seam the metrics adapter consumes: the multipath Bind's
// per-PEER traffic+telemetry, FEC, and resequencer snapshot (T93/T94). *bind.Multipath
// satisfies it; a fake satisfies it in the adapter's unit test, so the mapping and rate
// derivation are testable without a running engine. PeerSnapshots generalizes the
// pre-T94 PathSnapshots/FECSnapshot (primary-peer-only) to every bound peer, so a
// single-peer edge/hub still maps 1:1 onto today's series (len(PeerSnapshots())==1) and
// a multi-peer concentrator's additional peers surface as additional entries.
type trafficProvider interface {
	PeerSnapshots() []bind.PeerSnapshot
}

// sampleKey identifies one (peer,path) pair in the throughput last-sample map (T94): a
// path name alone is only unique WITHIN one peer, not across peers (each peer's path
// set is independently configured/named in principle), so keying by path name alone
// would let two peers' samples for a same-named path clobber each other's rate
// derivation. peer is "" for the single-peer edge/hub, exactly matching PeerSnapshot.Name
// — so a single-peer config's map degenerates to the pre-T94 keying (peer is always "",
// so the key varies only by path). A concentrator's primary carries its configured name
// like every other bound peer (D58), so a multi-peer config's map is keyed by that name.
type sampleKey struct {
	peer, path string
}

// metricsSource adapts the multipath Bind onto metrics.Source: at every scrape it
// reads the Bind's cumulative per-(peer,path) byte counters + telemetry, and DERIVES
// the per-(peer,path) throughput as the rate of the (tx+rx) byte-counter delta since
// the previous scrape. Deriving the rate here (rather than in the Bind) keeps the
// Bind's hot path a pair of lock-free atomic Adds with no timekeeping, and confines the
// only stateful, scrape-cadence-dependent piece to this adapter. The first scrape of a
// (peer,path) reports zero throughput (no prior sample); a counter that appears to move
// backwards (only possible across a Close→Open counter reset) also yields zero for that
// interval rather than a spurious negative.
type metricsSource struct {
	provider trafficProvider
	// session yields the connection-scoped WG-session snapshot (I2), read from the
	// engine at scrape time. It is separate from provider because the bind stays
	// WG-unaware — the session signal is sourced by the device layer's engine seam.
	session sessionSnapshotter
	// peerSessions yields the per-peer WG-session snapshot (T256, G28, M106), read from
	// the engine at scrape time. Like session, it is separate from provider because the
	// bind stays WG-unaware.
	peerSessions peerSessionSnapshotter
	clock        telemetry.Clock

	mu   sync.Mutex
	last map[sampleKey]byteSample
	// pmtu, when set (device.Up wires it AFTER PMTU discovery is constructed, T229/D88),
	// returns a path's discovered outer PMTU via the T226 converged accessor: 0 until the
	// first search converges, so the T209 resizer keeps the configured-or-default fallback
	// and wanbond0 does not dip at boot. nil before it is wired -> PMTU reported as 0.
	pmtu func(pathName string) int
}

// byteSample is the previous scrape's cumulative (tx+rx) total for one (peer,path) and
// the instant it was taken, so the next scrape can divide the delta by the elapsed time.
type byteSample struct {
	total   uint64
	atNanos int64
}

// newMetricsSource builds a metrics.Source over the Bind (per-peer path traffic, FEC,
// and resequencer counters) and the engine session seams (the connection-scoped and the
// per-peer WG-session snapshots, T256/G28/M106). The clock is injected so the
// throughput derivation is deterministic under test.
func newMetricsSource(provider trafficProvider, session sessionSnapshotter, peerSessions peerSessionSnapshotter, clock telemetry.Clock) *metricsSource {
	return &metricsSource{provider: provider, session: session, peerSessions: peerSessions, clock: clock, last: make(map[sampleKey]byteSample)}
}

// setPMTULookup wires the per-path discovered-PMTU accessor (device.Up, T229/D88). It is
// set ONCE after PMTU discovery is constructed (which happens after this Source is built,
// so it cannot be passed at construction), guarded by mu because Paths reads it on the
// scrape goroutine. A nil fn (never wired) leaves PMTU reported as 0.
func (s *metricsSource) setPMTULookup(fn func(pathName string) int) {
	s.mu.Lock()
	s.pmtu = fn
	s.mu.Unlock()
}

// Paths implements metrics.Source. It is called on the scrape goroutine (the /metrics
// HTTP handler), which the prometheus registry may run concurrently, so the per-
// (peer,path) last-sample map is guarded. The underlying provider.PeerSnapshots() reads
// atomics and each prober's synchronized Estimate without holding the Bind's send lock
// across a prober call, so scraping never blocks Send. The BindMode/BoundDevice/Source/
// Remote addressing fields (T220) are copied verbatim from bind.PathTraffic — this is the
// single authority for runtime-resolved addressing; the Prometheus collector ignores them.
func (s *metricsSource) Paths() []metrics.PathSnapshot {
	peers := s.provider.PeerSnapshots()
	nowNanos := s.clock.Now().UnixNano()

	total := 0
	for _, peer := range peers {
		total += len(peer.Paths)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]metrics.PathSnapshot, 0, total)
	for _, peer := range peers {
		for _, t := range peer.Paths {
			key := sampleKey{peer: peer.Name, path: t.Name}
			byteTotal := t.TxBytes + t.RxBytes
			var tput float64
			if prev, ok := s.last[key]; ok {
				dtSeconds := float64(nowNanos-prev.atNanos) / 1e9
				if dtSeconds > 0 && byteTotal >= prev.total {
					tput = float64(byteTotal-prev.total) * 8 / dtSeconds
				}
			}
			s.last[key] = byteSample{total: byteTotal, atNanos: nowNanos}
			// Discovered outer PMTU (T229/D88): the RAW measured value (0 until converged);
			// the amnezia junk headroom is subtracted ONCE downstream in sampleMTU (T225), so
			// it must NOT be subtracted here (no double-subtract). Pinned paths report their
			// configured value. A per-path property, so it is the same across every peer's
			// snapshot of this path.
			pmtu := 0
			if s.pmtu != nil {
				pmtu = s.pmtu(t.Name)
			}
			out = append(out, metrics.PathSnapshot{
				Peer:                    peer.Name,
				Name:                    t.Name,
				TxBytes:                 t.TxBytes,
				RxBytes:                 t.RxBytes,
				ThroughputBitsPerSecond: tput,
				Estimate:                t.Estimate,
				State:                   t.State,
				BindMode:                string(t.BindMode),
				BoundDevice:             t.BoundDevice,
				Source:                  t.Source,
				Remote:                  t.Remote,
				PMTU:                    pmtu,
				ProbeSendErrors:         t.ProbeSendErrors,
			})
		}
	}
	return out
}

// FEC implements metrics.Source: it reads the Bind's per-peer connection-scoped FEC
// counters (T24, T94) verbatim at scrape time, one entry per bound peer. Unlike the
// per-path throughput, the FEC counters are cumulative and need no rate derivation or
// prior-sample state, so this is a direct pass-through of the Bind's lock-free snapshot.
func (s *metricsSource) FEC() []metrics.FECSnapshot {
	peers := s.provider.PeerSnapshots()
	out := make([]metrics.FECSnapshot, len(peers))
	for i, peer := range peers {
		f := peer.FEC
		snap := metrics.FECSnapshot{
			Peer:                 peer.Name,
			DataPackets:          f.DataFrames,
			RepairPackets:        f.ParityFrames,
			RecoveredPackets:     f.Recovered,
			UnrecoverablePackets: f.Unrecoverable,
			DataBytes:            f.DataBytes,
			RepairBytes:          f.ParityBytes,
			ResidualLossRatio:    f.ResidualLoss,
		}
		if f.Adaptive != nil {
			snap.Adaptive = &metrics.AdaptiveFECStats{
				Parity:        f.Adaptive.Parity,
				SmoothedLoss:  f.Adaptive.SmoothedLoss,
				EligibleLoss:  f.Adaptive.EligibleLoss,
				EligiblePaths: f.Adaptive.EligiblePaths,
			}
		}
		out[i] = snap
	}
	return out
}

// Reseq implements metrics.Source: it reads the Bind's per-peer resequencer counters
// (T94) verbatim at scrape time, one entry per bound peer — a direct pass-through of
// the resequencer's own cumulative Stats(), like FEC needing no rate derivation.
func (s *metricsSource) Reseq() []metrics.ReseqSnapshot {
	peers := s.provider.PeerSnapshots()
	out := make([]metrics.ReseqSnapshot, len(peers))
	for i, peer := range peers {
		out[i] = metrics.ReseqSnapshot{Peer: peer.Name, Stats: peer.Reseq}
	}
	return out
}

// Aggregation implements metrics.Source: it reads each peer's weighted-scheduler
// aggregation-gate snapshot (T146) from the Bind's per-peer snapshot at scrape time,
// emitting ONE entry per peer whose scheduler exposes a gate. A peer without one
// (active-backup) carries a nil PeerSnapshot.Aggregation and is skipped, so the four
// aggregation series are absent for it — like FEC/Reseq, a direct pass-through of a
// lock-free snapshot the Bind already computed (the gate read happens in PeerSnapshots,
// off the send lock), needing no rate derivation or prior-sample state here.
func (s *metricsSource) Aggregation() []metrics.AggregationSnapshot {
	peers := s.provider.PeerSnapshots()
	out := make([]metrics.AggregationSnapshot, 0, len(peers))
	for _, peer := range peers {
		if peer.Aggregation == nil {
			continue
		}
		a := peer.Aggregation
		out = append(out, metrics.AggregationSnapshot{
			Peer:                  peer.Name,
			Aggregating:           a.Aggregating,
			OfferedLoadFPS:        a.OfferedLoadFPS,
			EngageThresholdFPS:    a.EngageThresholdFPS,
			DisengageThresholdFPS: a.DisengageThresholdFPS,
		})
	}
	return out
}

// PeerNames implements metrics.Source: it returns the current bound-peer name set,
// queried once by metrics.NewCollector to fix the `peer` label's presence for the
// collector's whole life (T94) — see the metrics package doc for the back-compat rule.
func (s *metricsSource) PeerNames() []string {
	peers := s.provider.PeerSnapshots()
	names := make([]string, len(peers))
	for i, peer := range peers {
		names[i] = peer.Name
	}
	return names
}

// Session implements metrics.Source: it reads the connection-scoped WG-session snapshot
// (I2) from the engine seam at scrape time. Like FEC it is a direct pass-through of a
// snapshot the underlying monitor computes on demand — no per-scrape state is kept here
// (the 0->1 edge log is driven by the separate session-monitor poll loop, not by scrapes).
func (s *metricsSource) Session() metrics.SessionSnapshot {
	return s.session.SessionSnapshot()
}

// PeerSessions implements metrics.Source: it reads the per-peer WG-session snapshot
// (T256, G28, M106) from the engine seam at scrape time — a direct pass-through, like
// Session(), of a snapshot the underlying per-peer session monitor computes on demand.
func (s *metricsSource) PeerSessions() []metrics.PeerSessionSnapshot {
	return s.peerSessions.PeerSessionSnapshots()
}
