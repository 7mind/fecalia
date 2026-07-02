//go:build e2e

package e2e

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
