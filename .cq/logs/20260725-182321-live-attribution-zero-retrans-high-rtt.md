# Live attribution: zero retransmissions with high loaded RTT

Captured read-only from the standing Pi edge and o3 concentrator. No service,
configuration, or network state changed; no traffic was generated.

## Exact deployed build

Both running processes use the same binary:

- VCS revision: `4c377b56e3e1516b267fe18ea2b83237c14da3f7`
- Module: `github.com/7mind/wanbond v0.0.0-20260725131952-4c377b56e3e1`
- Embedded version: `4c377b5-mo`
- Go: `go1.26.4`, `linux/arm64`, `CGO_ENABLED=0`
- `vcs.modified=false`
- Installed/running binary SHA-256:
  `c9f7b268960ea20903287c4db9c0771a68a2321edd7bead75a80a63a832370b0`

This build predates the T308 fail-first series and the unmerged T309 candidate
`6994ecb95c5ffb1143c46db4b18b3d688fb7b779`.

- Pi `wanbond-edge.service`: PID 12475, started
  `2026-07-25 16:32:17 IST` (`15:32:17 UTC`).
- o3 `wanbond-concentrator.service`: PID 372701, started
  `2026-07-25 15:32:12 UTC`.

## Relevant configuration

Pi:

- edge role, default `active-backup`;
- pacing enabled;
- Starlink `8Mbit`, declared RTT `45ms`;
- 5G `6Mbit`, declared RTT `60ms`;
- adaptive FEC enabled, `K=8`, `Mmax=4`,
  `target_residual=0.001`, deadline `5ms`.

o3:

- concentrator role, one `wan0` path;
- default active-backup, pacing disabled;
- same adaptive FEC settings.

Neither endpoint configures a resequencer override; the built-in revision
behavior applies.

## Metrics evidence

Pi at `2026-07-25T18:23:21+01:00`:

- derived shaper rates: Starlink 1,000,000 B/s; 5G 750,000 B/s;
- current queue, in-flight bytes, and scheduled delay: zero;
- accepted equals emitted on both paths;
- zero shaper write, async, EMSGSIZE, and admission-cancellation errors;
- Starlink accumulated 0.091849 s over 251 admission waits, about
  0.366 ms/wait;
- current Starlink RTT/loss 40.4 ms / 0.195%; 5G 67.6 ms / 0%;
- FEC 187,806 DATA and 91,902 repair packets (48.9% packet overhead;
  repair/data bytes 31.7%); current adaptive parity 0; residual loss 0.879%;
- resequencer 233 HOL holds totaling 62.484 s, mean approximately 268 ms;
  426 skipped sequences.

o3:

- no shaper series, consistent with pacing disabled;
- current RTT/loss 53.6 ms / 0%;
- FEC repair/data packet ratio 7.89%; residual loss 0.183%;
- resequencer 87 HOL holds totaling 18.812 s, mean approximately 216 ms;
  91 skipped sequences.

## Interpretation

The current scrape contains no local shaper backlog, shedding, or write-error
evidence and cannot reconstruct the exact loaded-RTT interval. It does,
however, separate the mechanisms:

- the zero-retransmission improvement runs on the exact-byte shaper revision,
  before T309;
- cumulative resequencer hold durations cluster near the fixed 250 ms hold
  path and provide an independent mechanism for the remaining latency spikes;
- substantial FEC wire overhead consumes shaped capacity and can reduce TCP
  goodput, but does not alone explain the full loaded-RTT spike.

Logs contain no pacing/FEC/resequencer errors. They show repeated Pi MTU
oscillation, generally `1395 -> approximately 1320 -> 1395` around every five
minutes, plus shorter o3 MTU drops. This may add transient disruption but does
not establish the loaded-RTT mechanism.

## Read-only operations

Evidence came from `systemctl show`, `sha256sum` of installed and running
binaries, `go version -m`, strict non-secret configuration filters, local
Prometheus scrapes, and filtered `journalctl` output.
