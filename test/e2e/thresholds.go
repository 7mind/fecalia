//go:build e2e

package e2e

import (
	"time"

	"github.com/7mind/wanbond/internal/telemetry"
)

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

// Per-path liveness detection (T13/T39). Path liveness is entirely ours — the inner
// WireGuard keepalive is per-peer, not per-path — so a blackholed uplink under a
// live peer is detected only by these probes. These are NOT independent constants:
// they are ALIASES of the daemon's telemetry.Default* timing, so the e2e asserts
// against the exact cadence the daemon runs. This is the D16 reconciliation — there
// is one source of truth (internal/telemetry), not two tables that drift apart.
// PLivenessDownAfter is the silence (no authenticated probe echo) after which an up
// path is marked down; PLivenessProbeInterval is the probe/Tick cadence; detection
// latency is therefore bounded by DownAfter plus roughly one interval, and the e2e
// asserts a blackholed path is marked down within PLivenessDetectBudget.
//
// Relationship to P1RecoverySeconds (the 3s failover-recovery budget). Killing the
// ACTIVE WAN must restore the surviving flow within P1RecoverySeconds in BOTH
// directions. Recovery in EACH direction is (that side marks the path down) +
// (its scheduler reroutes, a sub-ms data-structure update). Crucially, failover is
// BIDIRECTIONAL and the two ends detect INDEPENDENTLY: the edge must mark the path
// down to reroute egress, AND the concentrator must mark it down to reroute its
// replies (D15/D16 — the earlier note budgeted only the edge term). Both detection
// clocks start at the kill and run CONCURRENTLY, so end-to-end bidirectional
// recovery is governed by the SLOWER of the two ends, not their sum:
//
//	recovery ≈ max(edgeDetect, concDetect) + reroute
//	         ≈ (DownAfter + one interval) + reroute        [the two ends are symmetric]
//	         = PLivenessFailoverBudget
//
// PLivenessFailoverBudget ≈ 1.4s is the NO-LOAD analytical floor (detect + one
// interval + reroute). The remaining ~1.6s up to the 3s deadline is the margin for
// CPU-scheduling jitter under a saturating flow — the D15 tail. Two T39 changes
// bought that margin back after the tail was found at ~3.1s: DownAfter was tightened
// 1500ms→1200ms and the interval 250ms→200ms (floor 1.75s→1.4s) WITHOUT reducing the
// six-lost-echo false-down tolerance (the ratio is unchanged); and the bind now
// advances liveness off the always-scheduled receive path too (D15), so a starved
// probe-loop timer no longer delays the concentrator-side detect. The correctness
// invariant when retuning is PLivenessFailoverBudget < P1RecoverySeconds, and the
// operational target is that MEASURED recovery under saturating load stays below
// P1RecoverySeconds every run — TestP1Failover is the guard.
//
// NOTE PLivenessDetectBudget is deliberately LARGER than PLivenessFailoverBudget: it
// is the SINGLE-PATH blackhole ASSERTION DEADLINE for TestLivenessBlackhole (detect
// worst-case + harness slack so that pump-loop e2e is not flaky), NOT the
// failover-composition term.
const (
	PLivenessProbeInterval = telemetry.DefaultProbeInterval
	PLivenessDownAfter     = telemetry.DefaultDownAfter
	PLivenessUpSuccesses   = telemetry.DefaultUpSuccesses
	// PLivenessFailoverBudget is the analytical per-direction failover-recovery
	// bound (detect + one interval + reroute headroom). Both directions are symmetric
	// and detect concurrently, so it bounds end-to-end BIDIRECTIONAL recovery. It MUST
	// stay below P1RecoverySeconds; TestP1Failover asserts measured recovery against
	// P1RecoverySeconds and reports its margin against this budget.
	PLivenessFailoverBudget = PLivenessDownAfter + 2*PLivenessProbeInterval
	// PLivenessDetectBudget is the single-path blackhole assertion deadline (harness
	// slack added on top of the analytical detect), NOT the failover budget.
	PLivenessDetectBudget = PLivenessDownAfter + 1500*time.Millisecond
)
