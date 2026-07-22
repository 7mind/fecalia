// Package adaptivefec implements a pure, deterministic control loop that adjusts
// the Reed-Solomon parity count M for a fixed group of K data shards from a
// measured per-path loss fraction. It is DELIBERATELY decoupled from the
// datapath: it imports neither internal/fec, internal/telemetry, nor
// internal/config. Its sole input is a raw loss fraction in [0,1]; its sole
// output is a target parity count M. The datapath integration (feeding
// telemetry.Estimate.Loss in, applying M to the encoder) is a separate task.
//
// # Control model
//
// The controller is a state machine driven by (loss sample, time). It composes
// four independent, individually-necessary mechanisms — each guards a distinct
// failure mode of a naive loss->parity map:
//
//  1. SMOOTHING (EWMA). Raw telemetry loss is noisy. A per-sample exponentially
//     weighted moving average s <- alpha*loss + (1-alpha)*s rejects that noise
//     so M tracks the underlying loss rate, not individual samples. alpha is
//     defined PER SAMPLE and assumes a roughly regular probe cadence (the same
//     assumption internal/telemetry's estimator makes).
//
//  2. REDUNDANCY MAP with a SAFETY FACTOR. A group of K+M shards tolerates a
//     loss fraction up to M/(K+M). To reliably MASK a uniform loss p a single
//     group must tolerate MORE than p, because per-group loss fluctuates around
//     the mean (binomial variance) — occasionally a group loses more than p*N
//     shards. We therefore size M so the group tolerates safety*p, not just p:
//
//     M/(K+M) >= safety*p   =>   M = ceil( K * e / (1-e) ),  e = safety*p
//
//     with e saturating to MaxParity as e->1. At p=0 the map yields exactly 0.
//     SafetyFactor is the documented headroom over the mean.
//
//     Alternatively (and preferably) the map is parameterized by a TARGET RESIDUAL
//     rather than a bare multiplier: when Config.TargetResidual is set the map picks
//     the smallest M whose MODELED post-recovery residual E[max(0,D-M)]/K
//     (D ~ Bin(K, p), see residual.go) is at/below the target, capped at MaxParity.
//     This maps an operator's residual-loss SLA directly to redundancy; it
//     SUPERSEDES SafetyFactor (the two are mutually exclusive). Both variants are
//     zero-at-zero and monotone non-decreasing, so the mechanisms below are
//     independent of which one is active.
//
//  3. HYSTERESIS (deadband). Two DISTINCT loss thresholds bracket a deadband:
//     the controller only RAISES M when smoothed loss >= RaiseThreshold and only
//     LOWERS M when smoothed loss <= LowerThreshold; inside the band it HOLDS.
//     A loss signal oscillating inside the band cannot make M oscillate — the
//     deadband is the primary anti-flap mechanism. In the raise region M only
//     ever increases; in the lower region it only ever decreases; so a moderate
//     over-band excursion cannot trigger a spurious decrease and vice versa.
//     In the RESIDUAL-SLA mode these two gates are DERIVED from TargetResidual
//     quantization-aware (max(TargetResidual, 2 loss quanta) for the raise gate,
//     the configured deadband shape scaled below it for the lower gate) rather
//     than taken from the fixed RaiseThreshold/LowerThreshold, so a sub-5% loss
//     that misses the SLA still raises M while a single estimator quantum cannot
//     flap parity (D96); see Config.effectiveThresholds.
//
//  4. RATE LIMIT + DWELL (slew control, asymmetric). M changes by at most
//     MaxStep per RateInterval (a min-interval slew limiter), so a single loss
//     spike cannot swing redundancy wildly — the overshoot is bounded to
//     MaxStep. Lowering additionally requires the smoothed loss to have stayed
//     at/below LowerThreshold continuously for Dwell before the first decrease.
//     This makes the loop RAISE QUICKLY (react to rising loss within a
//     RateInterval) but LOWER CONSERVATIVELY (hold redundancy through a Dwell
//     window after loss appears to clear), the classic FEC asymmetry.
//
// # Determinism
//
// The controller holds an injected Clock (mirroring internal/fec.Clock and
// internal/telemetry.Clock) and does no I/O and touches no globals. With a
// hand-advanced fake Clock every trajectory is exactly reproducible and
// -race clean, so the simulation harness in the tests can assert the stability
// properties without a network.
//
// # Read-only scheduler feed
//
// SmoothedLoss and Overhead expose the controller's internal signals as a clean
// read-only surface. A future integration MAY also feed SmoothedLoss to the
// weighted scheduler (T21), but this package does NOT wire anything — its scope
// is the parity-ratio control law and its simulation tests.
package adaptivefec
