//go:build e2e

package e2e

import "time"

// Acceptance thresholds (Q1). These are the single source of truth for the
// per-phase verification criteria; every e2e assertion reads them from here so
// retuning after real-link measurements is a one-line change. Values are the
// approved Q1 defaults.
const (
	// P1RecoverySeconds bounds transparent-failover recovery: after the active
	// WAN is killed, the surviving TCP flow must have throughput restored within
	// this many seconds, with no connection reset.
	P1RecoverySeconds = 3

	// P2BondedMinFraction is the minimum bonded throughput as a fraction of the
	// sum of the two paths' individual throughputs, under saturating load.
	P2BondedMinFraction = 0.85
	// P2MeteredMaxByteFraction caps the metered (5G) path's share of total bytes
	// while the primary (Starlink) path is healthy — data-thrift.
	P2MeteredMaxByteFraction = 0.01

	// P3MinRecoveredFraction is the minimum fraction of lost DATA frames the RS
	// FEC must recover without retransmit, at each injected loss rate.
	P3MinRecoveredFraction = 0.95
	// P3MaxOverheadFactor bounds measured FEC overhead as a multiple of the
	// configured parity ratio.
	P3MaxOverheadFactor = 2.0

	// P4ResidualLossMax is the maximum post-recovery residual loss under steady
	// P4SteadyLossRate path loss with adaptive FEC engaged.
	P4ResidualLossMax = 0.005
	// P4SteadyLossRate is the steady per-path loss rate at which the P4 residual
	// and overhead-vs-baseline comparisons are made.
	P4SteadyLossRate = 0.05
)

// P3InjectedLossRates are the uniform loss rates at which fixed-ratio FEC
// recovery and overhead are asserted (P3).
var P3InjectedLossRates = []float64{0.05, 0.15}

// P4 adaptive-overhead acceptance is a comparison, not a scalar: for equal loss
// masking, adaptive total overhead bytes must be <= the P3 fixed-FEC baseline
// bytes. The comparison is performed in the P4 e2e test against a recorded
// baseline run rather than a constant here.

// Per-path liveness detection (T13). Path liveness is entirely ours — the inner
// WireGuard keepalive is per-peer, not per-path — so a blackholed uplink under a
// live peer is detected only by these probes. PLivenessDownAfter is the silence
// (no authenticated probe echo) after which an up path is marked down;
// PLivenessProbeInterval is the probe cadence; detection latency is therefore
// bounded by DownAfter plus roughly one interval, and the e2e asserts the
// blackholed path is marked down within PLivenessDetectBudget.
//
// Relationship to P1RecoverySeconds (the 3s failover-recovery budget). The two
// budgets compose: killing a WAN must restore the surviving flow within
// P1RecoverySeconds, and that clock is (path marked down) + (scheduler fails the
// path out and reroutes). This liveness machine owns only the first term; marking
// a path down is what TRIGGERS failover (the T12 scheduler drops a down path from
// its active set). Worst-case detection is DownAfter + one probe interval ≈ 2.0s +
// 0.2s = 2.2s, leaving ~0.8s of the 3s budget for the scheduler's reroute — the
// send-side switch is a data-structure update (sub-millisecond), so 2s DownAfter
// is comfortably compatible with the 3s P1 recovery budget. The invariant to
// preserve when retuning is on the ANALYTICAL detect term, not the assertion
// deadline: PLivenessDownAfter + PLivenessProbeInterval (+ the reroute headroom)
// < P1RecoverySeconds. NOTE PLivenessDetectBudget below is deliberately LARGER
// than P1RecoverySeconds (3.5s vs 3s): it is the e2e ASSERTION DEADLINE — the
// analytical worst-case detect (~2.2s) plus ~1.3s of harness slack so the
// blackhole e2e is not flaky — NOT the failover-composition term. Tighten
// DownAfter (at the cost of more probe traffic and a higher false-down risk on a
// jittery link) if the reroute term ever grows.
const (
	PLivenessProbeInterval = 200 * time.Millisecond
	PLivenessDownAfter     = 2 * time.Second
	PLivenessUpSuccesses   = 3
	PLivenessDetectBudget  = PLivenessDownAfter + 1500*time.Millisecond
)
