package device

import (
	"sync"

	"github.com/7mind/wanbond/internal/bind"
	"github.com/7mind/wanbond/internal/metrics"
	"github.com/7mind/wanbond/internal/telemetry"
)

// trafficProvider is the read seam the metrics adapter consumes: the multipath Bind's
// per-path traffic+telemetry snapshot (T23) and its connection-scoped FEC counters
// (T24). *bind.Multipath satisfies it; a fake satisfies it in the adapter's unit test,
// so the mapping and rate derivation are testable without a running engine.
type trafficProvider interface {
	PathSnapshots() []bind.PathTraffic
	FECSnapshot() bind.FECStats
}

// metricsSource adapts the multipath Bind onto metrics.Source: at every scrape it
// reads the Bind's cumulative per-path byte counters + telemetry, and DERIVES the
// per-path throughput as the rate of the (tx+rx) byte-counter delta since the previous
// scrape. Deriving the rate here (rather than in the Bind) keeps the Bind's hot path a
// pair of lock-free atomic Adds with no timekeeping, and confines the only stateful,
// scrape-cadence-dependent piece to this adapter. The first scrape of a path reports
// zero throughput (no prior sample); a counter that appears to move backwards (only
// possible across a Close→Open counter reset) also yields zero for that interval rather
// than a spurious negative.
type metricsSource struct {
	provider trafficProvider
	// session yields the connection-scoped WG-session snapshot (I2), read from the
	// engine at scrape time. It is separate from provider because the bind stays
	// WG-unaware — the session signal is sourced by the device layer's engine seam.
	session sessionSnapshotter
	clock   telemetry.Clock

	mu   sync.Mutex
	last map[string]byteSample
}

// byteSample is the previous scrape's cumulative (tx+rx) total for one path and the
// instant it was taken, so the next scrape can divide the delta by the elapsed time.
type byteSample struct {
	total   uint64
	atNanos int64
}

// newMetricsSource builds a metrics.Source over the Bind (per-path traffic + FEC) and the
// engine session seam (WG-session snapshot). The clock is injected so the throughput
// derivation is deterministic under test.
func newMetricsSource(provider trafficProvider, session sessionSnapshotter, clock telemetry.Clock) *metricsSource {
	return &metricsSource{provider: provider, session: session, clock: clock, last: make(map[string]byteSample)}
}

// Paths implements metrics.Source. It is called on the scrape goroutine (the /metrics
// HTTP handler), which the prometheus registry may run concurrently, so the per-path
// last-sample map is guarded. The underlying provider.PathSnapshots() reads atomics and
// the prober's synchronized Estimate without holding the Bind's send lock across a
// prober call, so scraping never blocks Send.
func (s *metricsSource) Paths() []metrics.PathSnapshot {
	tr := s.provider.PathSnapshots()
	nowNanos := s.clock.Now().UnixNano()

	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]metrics.PathSnapshot, len(tr))
	for i, t := range tr {
		total := t.TxBytes + t.RxBytes
		var tput float64
		if prev, ok := s.last[t.Name]; ok {
			dtSeconds := float64(nowNanos-prev.atNanos) / 1e9
			if dtSeconds > 0 && total >= prev.total {
				tput = float64(total-prev.total) * 8 / dtSeconds
			}
		}
		s.last[t.Name] = byteSample{total: total, atNanos: nowNanos}
		out[i] = metrics.PathSnapshot{
			Name:                    t.Name,
			TxBytes:                 t.TxBytes,
			RxBytes:                 t.RxBytes,
			ThroughputBitsPerSecond: tput,
			Estimate:                t.Estimate,
			State:                   t.State,
		}
	}
	return out
}

// FEC implements metrics.Source: it reads the Bind's connection-scoped FEC counters
// (T24) verbatim at scrape time. Unlike the per-path throughput, the FEC counters are
// cumulative and need no rate derivation or prior-sample state, so this is a direct
// pass-through of the Bind's lock-free snapshot.
func (s *metricsSource) FEC() metrics.FECSnapshot {
	f := s.provider.FECSnapshot()
	return metrics.FECSnapshot{
		DataPackets:          f.DataFrames,
		RepairPackets:        f.ParityFrames,
		RecoveredPackets:     f.Recovered,
		UnrecoverablePackets: f.Unrecoverable,
		DataBytes:            f.DataBytes,
		RepairBytes:          f.ParityBytes,
		ResidualLossRatio:    f.ResidualLoss,
	}
}

// Session implements metrics.Source: it reads the connection-scoped WG-session snapshot
// (I2) from the engine seam at scrape time. Like FEC it is a direct pass-through of a
// snapshot the underlying monitor computes on demand — no per-scrape state is kept here
// (the 0->1 edge log is driven by the separate session-monitor poll loop, not by scrapes).
func (s *metricsSource) Session() metrics.SessionSnapshot {
	return s.session.SessionSnapshot()
}
