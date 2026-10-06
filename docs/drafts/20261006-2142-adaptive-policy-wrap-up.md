# Adaptive policy release wrap-up — 2026-10-06

## Release decision and scope

**Operator decision:** release `ad7fea9` as `v0.0.4` and stop improvement work
for now. The annotated tag identifies exactly
`ad7fea930a58066c6ef613e7046df6b014bcda1c`, source commit time
`2026-10-06T19:36:15Z`. This wrap-up is a subsequent documentation-only change;
it does not move the release tag.

**Observed publication:** the operator publishes the tag and documentation.
At 2026-10-06 21:48 UTC, remote `v0.0.4` resolves to the selected `ad7fea9`,
and remote main contains wrap-up commit `58d1e2a`. Earlier agent publication
attempts failed: the available HTTPS account received HTTP 403 and the field
SSH key received `Permission denied (publickey)`; those attempts moved no refs.

**Observed source comparison:** production files in `cmd`, `internal`, `web`,
the flake and module definitions are identical between released `ad7fea9`
and the field's restored `2f3187e`, excluding Go test files. Subsequent changes
correct the TCP model and record measurements. Build identity distinguishes
the revisions. No field activation or deployment is part of this handover.

**Observed changes since `v0.0.3`:** main adds small-queue drop attribution by
protocol class/cause and pre-transmission queue residence metrics. Receipt
inference requires a complete sent-byte prefix and retains the facts needed
to recognize late complete receipts. Tests restate removed mechanism assertions
as transport outcomes and protect settled service after propagation changes,
including earlier physical loss. The shared test sender now includes
Reno-friendly recovery. Monitor/web source commit and UTC source time remain
available from the preceding release.

**Release boundary:** experimental queue-period correction `07731c7` and the
delay/capacity estimator prototypes remain on their branches. The release
retains the existing queue/controller policy. Authentication, wire format,
outer sequence space, inner replay validation and MTU invariants are unchanged.
Stage 4/video and permanent deployment remain outside the work.

## Metrics retained for further improvement

These are the operator's targets from [plan section 11](20261002-1730-adaptive-policy-plan.md#11-revised-execution-goal--operator-2026-10-04),
not claims that this release meets them all.

| Metric | Operational target |
|---|---|
| TCP download | Receiving-edge payload goodput relative to immediate downlink reference; retain 75% steady and 70% cellular targets |
| TCP upload | Receiving-hub payload goodput relative to immediate uplink reference; retain the same targets independently |
| Concurrent TCP download/upload | Both receiving directions, including reverse ACK demand and voice |
| Voice RTT | Lower median/p95/p99; p99 under 150 ms through rate changes; survivor idle p99 +50 ms after failover |
| Voice jitter, gaps and loss | No 150 ms receive gap; under 1% loss and at most three consecutive losses; preserve zero-loss cases |
| Bulk continuity and recovery | Delivery each second after the first outage second; survivor service within 3 s, returning-lane bulk within 2 s, pair service within 5 s |
| Rate adaptation | Fall within 5 s, rise within 10 s, plan upgrade within 20 s; no expiration burst |
| Delay adaptation/ranking | Follow improved delay within the stated 2/5 s gates; at most one move per 5 s under jitter |
| Cold transfer | 60% of available pair goodput within 7 s |
| Efficiency | Repairs, copies and expired datagrams per delivered byte; mobile RX+TX megabytes |

**Operator/environment evidence:** these are variable 5G/Starlink links. Earlier
configured caps were Starlink standby 0.5 Mbit/s symmetric and 5G 100 down/10
up; caps do not establish contemporaneous service. Local host CPU spikes can
distort lab wall-clock measurements. The field remains the performance
reference; virtual-clock models reproduce outcomes and lab failures remain
recorded. Immediate capped UDP references establish delivered UDP service,
not maximum capacity or available TCP goodput.

## Latest field evidence and its attribution

**Observed, retained logs:** the latest directional comparison tests deployed
`2f3187e`, temporary queue-period experiment `07731c7`, then restored
`2f3187e`. The baseline columns describe the release's production source;
there is no separate field trial of the test-model correction in `ad7fea9`.
Every TCP transfer lasts six seconds. Voice-only preflights precede physical
references and tunnel workloads; no WAN shaping or blackout is used.

| Receiving-side measurement | `2f3187e` before | `07731c7` experiment | `2f3187e` after |
|---|---:|---:|---:|
| TCP download, 6 Mbit/s offer | 4.105 Mbit/s | 6.002 Mbit/s | 3.507 Mbit/s |
| TCP upload, 3 Mbit/s offer | 2.413 Mbit/s | 2.222 Mbit/s | 1.508 Mbit/s |
| Concurrent download, 3 Mbit/s offer | 2.820 Mbit/s | 2.978 Mbit/s | 2.956 Mbit/s |
| Concurrent upload, 3 Mbit/s offer | 2.944 Mbit/s | 2.980 Mbit/s | 2.975 Mbit/s |
| Download-active voice RTT p99, edge/hub | 45.262/46.249 ms | 43.462/40.914 ms | 66.156/79.914 ms |
| Upload-active voice RTT p99, edge/hub | 51.315/49.673 ms | 68.941/69.431 ms | 43.272/42.593 ms |
| Concurrent-active voice RTT p99, edge/hub | 84.384/97.836 ms | 51.292/52.666 ms | 47.755/47.235 ms |

All nine cases receive 1,000/1,000 whole-run voice echoes on each host;
preflights receive 250/250. Latency uses guarded TCP-active windows on each
host's own clock. Download comes from the edge's local reverse-stream receiver
report, upload from the hub's local normal-stream receiver report. Actual
loopback receiver contracts on both hosts establish that report mapping.
Physical forward receipts and returned confirmations are counted separately.

**Inference:** the experiment reaches its download cap in this set, but its
upload voice tails exceed both baselines. Concurrent service is close to its
offer for both experiment and final baseline. The first baseline and experiment
downloads have no AQM drops, so their rate difference does not establish the
queue correction's causal effect. Neither repeatable improvement across all
metrics nor an uncapped download gain follows. The 6.002 Mbit/s experimental
result is not a measurement of released `ad7fea9`.

The budget guard stops the initial comparison after seven cases. The last
upload/concurrent baselines come from a separate unchanged-baseline run after
a pause without another restart. This is one field set, not three repetitions.
Four disjoint staging/comparison/completion/cleanup intervals total
**82.373319 MB mobile RX+TX**, including background traffic. Gaps, contract
preparation and other checks are excluded; this is not a whole-session total.
Exact method, accounting and limits are in the
[directional comparison record](20261002-1730-adaptive-policy-plan.md#directional-tcp-comparison-and-queue-period-correction--2026-10-06).

**Historical operator measurement:** the earlier C8 test reached 66.19 Mbit/s
download and 0.33 Mbit/s upload. Sequential Speedtests used different servers;
that observation is neither a matched aggregation comparison nor the current
release's directional measurement. See the
[C8 release record](20261004-1105-adaptive-stage1-trial.md#operator-approved-c8-release--2026-10-04).

## Verification and unfinished acceptance

**Observed:** the release revision passes the full non-privileged gate from
`AGENTS.md` and `nix build`. Frontend checks include 44 passing tests. Evidence
is retained under `/srv/nvme/tmp/wanbond-adaptive-evidence/` as
`release-v0.0.4-full-nonprivileged-gate.txt` and
`release-v0.0.4-ad7fea9-nix-build.txt`. The documentation handover receives its
own final Nix build.

**Observed model results:** after correcting Reno-friendly recovery, all 52
section 4 cases give identical measurements/verdicts in three virtual-clock
repetitions per controller, using the separate `adaptivepolicy` build tag.
Original `f75668e` passes 13 and fails 39;
current main passes 17 and fails 35. Original passes remain findings against
section 2's predicted failures. The current radio lane-0 blackout with bulk
fails where the original passes. The total pass count therefore does not
establish improvement across all outcomes.

The test sender approximates CUBIC rather than reproducing the full Linux
implementation; the unresolved sparse-ACK slow-start approximation remains
documented. **Inferred from `transmit`:** `repair_packets_total` includes
redundant copies, so it cannot alone quantify retransmission overhead.

Stage 1's complete acceptance remains unsatisfied; stages 2–3 estimator
replacements remain unfinished. Complete triplicate lab acceptance on both
profile families and fresh non-regression proof for `continuity.py`,
`benchmark.py` and `udp.py` are not established by these checks. Operator
acceptance of this release is separate from completion of the original plan.

**Observed restoration:** independent checks after the directional trial verify
deployed hashes, unchanged boots, empty service overrides, original `auto`
exit policy/qdiscs, absent owned firewall rules/timers, stopped helpers,
removed private keys and removed staged candidate binaries. The last read-only
service identity check, 2026-10-06 19:42 UTC, finds both hosts on `2f3187e`
with SHA-256 `a53dbded1b699e48bd2b235e000fa3d7665e360d34340a125176911bc0e13c5a`.
Retained measurement archives are not blanket-deleted.

## Resume point

**Intended, deferred:** reproduce cold/startup lane assignment and qualification
of physical delivery before another policy change. Retained field startup
samples motivate that hypothesis, but their setup-relative timing and
background counters do not establish causality. Both one-datagram unknown-
capacity flight replacements are rejected: they starve healthy sole-lane bulk
beside voice and leave other gates failing. They receive no field deployment.

Any later candidate must preserve all three TCP workloads, voice outcomes,
adaptation and efficiency, with immediate directional references and paired
baseline/candidate/baseline field sets. Preserve failures and measurement
provenance; remove replaced rules in the same estimator change. The operator's
2026-10-06 release-and-wrap-up instruction pauses further improvement work.
