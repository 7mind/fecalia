// Package telemetry measures per-path quality (RTT, loss, jitter) from
// PSK-authenticated probes and drives the per-path up/down liveness state
// machine. Path liveness is entirely this codec's concern: the inner WireGuard
// keepalive is per-peer, not per-path, so a dead uplink under a live peer is
// invisible to WireGuard and must be detected here.
//
// Scoping of loss. Per-path loss is derived from the ACTIVE probe stream
// (gaps in received probe-echo ProbeSeq; see Prober/Estimator), NOT from the
// transport's data sequence. That sequence is connection-global — one sequence the
// transport spreads across all lanes and the receiver resequences
// into one global order (T18) — so per-path gaps in it would misread
// lane spreading and mid-stream path attach as loss.
//
// Concurrency. The composed entry types — Prober and Reflector — are safe
// for concurrent use (they take internal mutexes), matching T12's model of
// per-path receive goroutines plus a timer goroutine. The lower-level Estimator,
// Liveness, and AntiReplay are not independently synchronized and must be reached
// only through their owning Prober.
package telemetry
