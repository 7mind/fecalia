package adaptivefec

import "math"

// binomialResidual returns the MODELED post-recovery residual-loss fraction for a
// coding group of k data shards, each independently lost with probability p,
// protected by m parity shards that recover up to m of the group's losses:
//
//	E[max(0, D-m)] / k,   D ~ Bin(k, p)
//
// i.e. the expected fraction of the k data shards that remain unrecovered after
// correcting up to m losses. It is the residual-loss SLA the target_residual
// surface inverts (see (*Controller).residualTargetParity): D counts per-group
// losses, and any losses beyond the m the parity can repair are the residual. At
// p=0 the residual is 0 (no loss); at p=1 every shard is lost so the residual is
// max(0, k-m)/k.
//
// The binomial PMF is accumulated iteratively (pmf_0 = (1-p)^k, then the standard
// pmf_d = pmf_{d-1} * (k-d+1)/d * p/(1-p) recurrence) so no binomial coefficient is
// materialized, keeping it numerically stable for k up to the field limit.
func binomialResidual(k int, p float64, m int) float64 {
	if k <= 0 || p <= 0 {
		return 0
	}
	if p >= 1 {
		// Every data shard lost (D = k with probability 1); m parity recover m of them.
		if k > m {
			return float64(k-m) / float64(k)
		}
		return 0
	}
	q := 1 - p
	ratio := p / q
	pmf := math.Pow(q, float64(k)) // P(D = 0)
	var expected float64           // E[max(0, D-m)]
	for d := 1; d <= k; d++ {
		pmf *= float64(k-d+1) / float64(d) * ratio // advance to P(D = d)
		if d > m {
			expected += float64(d-m) * pmf
		}
	}
	return expected / float64(k)
}

// residualTargetParity derives the parity count M for the residual-SLA sizing mode:
// the SMALLEST m in [0, MaxParity] whose modeled binomial residual at the current
// smoothed loss is at/below the configured TargetResidual. It is used in place of
// the SafetyFactor redundancy map when TargetResidual is set.
//
// It is monotone non-decreasing in loss (a higher loss needs at least as much
// parity to hold the same residual), returns 0 at zero loss (zero overhead when
// clean), and saturates at MaxParity when even the parity ceiling cannot meet the
// target — the same shape the control law requires of the SafetyFactor map, so the
// hysteresis/slew machinery is unaffected by which mode is active.
func (c *Controller) residualTargetParity(loss float64) int {
	if loss <= 0 {
		return 0
	}
	for m := 0; m <= c.cfg.MaxParity; m++ {
		if binomialResidual(c.cfg.DataShards, loss, m) <= c.cfg.TargetResidual {
			return m
		}
	}
	return c.cfg.MaxParity
}
