# Wanbond handover — 2026-09-29

## Current state

Application checkpoint: **`d64e956`**, branch `main` in
`/home/pavel/work/safe/flakes/fecalia`. The working tree was clean before this
handover. The latest runtime change is `ee61de2`; subsequent commits add
reproductions and documentation.

**The adaptive policy is still experimental. The requested radio saturation
and voice-continuity goals have not been achieved.** Several independently
reproduced defects have been corrected, and a reusable autonomous VM lab exists.
Passing the default tests or the basic VM profile does not establish success
under the radio profile.

The user performs production deployments. At the original handover, the adjacent
`nix-config` repository was at `c49dfa66`; its `fecalia` input pinned
`af2d54c4fcdcda89fb74842d951c687e02e43a27`. That pin does **not** include the later
corrections listed below. The user subsequently reported deploying the latest
candidate and a substantially improved Speedtest result, recorded below.
The deployed revision was not independently checked with that report.

## Goals and outcome

| Goal | Achieved | Outstanding |
| --- | --- | --- |
| Autonomous, reproducible testing | Persistent KVM guests, independent WAN impairments, calibration, directional throughput and bidirectional continuity scenarios, retained raw results | Broader coverage of multiple exits/peers, NAT changes and production RF conditions |
| Use both WANs in both directions | Adaptive transport discovers authenticated return paths and paces each direction independently; both WAN counters advance under benchmark load | Radio throughput thresholds and consistently fast capacity discovery |
| Preserve connections and voice through WAN loss | Basic VM continuity passes; latest retained radio run maintained TCP progress through both outages | Radio voice loss and latency thresholds still fail |
| Audit and cleanup | Audit documented; obsolete `.cq/` ledger removed | Decide supported legacy features, then remove their implementations and deduplicate shared code |
| AmneziaWG 3 migration | Pinned candidate investigated, compile incompatibilities identified, staged migration documented | No engine upgrade or mixed-version runtime validation implemented |

## What was implemented and retained

| Commit | Change |
| --- | --- |
| `8ff0c5e` | Adaptive bidirectional transport and reproducible KVM network lab |
| `623d36f` | Intermediate feedback/batching corrections for asymmetric, jittered links |
| `33889c7` | Attribute shared-socket receive bytes to the resolved peer |
| `39dd04c` | Correct receipt handling under reordering, cross-path window release, attempt retirement, idle keepalive behavior and full delivery-confirmation timing |
| `ce565cd` | Carry local flow metadata through encryption, isolate small flows, and coalesce eligible unsent cumulative TCP ACKs |
| `4d3d0da` | Bound startup windows, separate forward jitter from return-path jitter, prevent bulk starvation behind small packets, and retain current advertised windows during ACK coalescing |
| `ee61de2` | Start the fixed bulk repair lifetime at first transmission, preserving the enqueue-relative small-packet deadline |
| `be86c42` | Commit opt-in failing reproductions for discovery and voice contention |
| `d64e956` | Record validation results and remaining failures |

Important behavior to preserve:

- Bulk packets are striped; the policy does not replicate every packet onto
  every link. Small encrypted datagrams may receive budgeted replication and
  cross-path recovery. The extra-copy budget is 10% of aggregate healthy pacing
  targets, capped at 64 kB/s.
- Small datagrams, up to 384 encrypted bytes, receive priority and bypass bulk
  resequencing after authentication/deduplication. This is a size heuristic,
  not a voice application classifier. Inner WireGuard replay validation remains.
- Small flows rotate through a per-flow queue. A waiting bulk datagram gets a
  reserved turn after 8 KiB of small-packet service.
- ACK coalescing retains control information, duplicate/backward ACKs, SACKs,
  window-only updates and zero-window transitions. Local metadata does not
  change the wire format. Engine coupling for the metadata interface stays in
  `internal/bind/bind.go`.
- Queued packets wait at most 100 ms before first transmission. Bulk repair
  lasts at most 250 ms from first send; small-packet expiry remains 250 ms from
  enqueue. Retransmission never extends either deadline.
- Adaptive requires `[scheduler] policy = "adaptive"` and `[fec] enabled = false`
  on both ends. It does not compose with the legacy FEC/shaper plane.

Earlier repository history also contains automatic RTT-based exit selection,
remote token-authenticated monitor control, the persistent exit selector/UI
redesign, and the read-only TUI for both roles. Router routing/FastTrack and
priority-script corrections were handled earlier through exchange scripts and
reported working by the user; they were not re-audited during this handover.

## Evidence and validation

### Production observation

The previous live capture verified matching Pi/concentrator binaries, SHA256
`22c2ac182a1bbfcaba0bf1fb64747e7060a86bfe5d87053bb64e0a046aafced9`.
During the user's rerun, the concentrator LTE pacing target stayed near its
16 kB/s floor despite healthy probes. Lost idle keepalives also caused exact
30% rate cuts. Matching binaries and removal of the old HTB cap did not resolve
the captured throughput collapse. These are observations from that capture,
not assertions about a subsequent user deployment.

The stronger VM radio profile reproduced approximately 0.417/0.472 Mbps
upload/download before the later corrections. The newer VM results establish
improvement in that emulator. The subsequent user report also establishes
improved observed production throughput, with slow discovery still evident.

### Subsequent user-reported production result

After deploying what the user described as the latest candidate, Speedtest
against **Blacknight, Dublin (server 4604)**, with Blacknight as the reported
ISP, returned:

| Measurement | Result |
| --- | --- |
| Download | 44.57 Mbps; 55.1 MB transferred |
| Upload | 2.25 Mbps; 3.4 MB transferred |
| Idle latency | 32.00 ms; jitter 2.07 ms; range 31.86–39.99 ms |
| Download loaded latency | 46.63 ms; jitter 15.65 ms; maximum 373.01 ms |
| Upload loaded latency | 74.52 ms; jitter 27.07 ms; maximum 390.49 ms |
| Reported packet loss | 0.0% |

The preceding reported test against the same server measured 0.49 Mbps download
and 0.48 Mbps upload. RF conditions and raw-link capacity were not measured
alongside both runs, so this is an observed improvement rather than a controlled
estimate of the code's effect. Binary hashes and an exact deployed revision
were not supplied with the new result.

The user observed peak throughput only after approximately **60% of the test's
allocated time**. This is consistent with the committed capacity-discovery
reproduction and makes faster convergence the first controller investigation.
Record time to reach the sustained rate as well as whole-test average goodput;
do not hide the ramp by lengthening warmup. The zero-loss Speedtest result does
not exercise a WAN outage or establish the separate voice-continuity gates.

### Latest retained runtime candidate

| Scenario | Measured result | Acceptance result | Artifact directory |
| --- | --- | --- | --- |
| Radio throughput | 1.183128 / 71.897991 Mbps upload/download | Both fail; required 1.2375 / 75.375 Mbps | `20260929-011714-adaptive` |
| Radio continuity | Hub/edge loss 0.984615% / 1.323077%; p99 RTT 198.17 / 197.22 ms; gaps 120.50 / 146.14 ms | TCP progress and gaps pass; edge loss and both latency bounds fail | `20260929-011557-continuity` |
| Fast throughput | 97.356924 / 95.997262 Mbps | Upload passes; download fails the 96 Mbps bound, even though close | `20260929-011948-adaptive` |
| Basic continuity | Zero loss; p99 RTT 117.10 / 113.58 ms; gaps 77.92 / 80.68 ms | All gates pass | `20260929-014420-continuity` |

Radio benchmarks use 60 seconds idle, 10 seconds warmup and 30 seconds measured
per direction. Fast benchmarks use 15 seconds warmup and 20 seconds measured.
Do not compare these aggregates with earlier five-second-warmup runs as though
they measured the same convergence interval.

The default frontend/Go/vendor gate, focused race checks, ARM64 cross-build and
Nix build passed for the retained runtime code. The final checkpoint Nix build
also passed. ARM64 compilation is not a Pi performance measurement. Tagged
real-host/namespace suites were not part of that final default gate.

### What remains reproducibly wrong

Two committed tests under the `progression` build tag intentionally fail:

- `TestCapacityDiscoveryAtCellularRTT`: after five seconds on a 100 Mbps,
  80 ms RTT path, the final second reaches only 15.91 Mbps from cold startup,
  or 24.68 Mbps following a 2-to-100 Mbps capacity increase. Required: at least
  75% of wire capacity. This uses virtual time, not host execution speed.
- `TestSparseSmallFlowsSurviveSustainedACKBacklog`: a loss-free symmetric
  0.4 Mbps model with two 50 Hz voice-sized flows, continuous pure ACKs and bulk
  delivered 142/200 voice packets in the measured interval; one-way p99 was
  158 ms. Required: all 200 and p99 below 75 ms. This offered-load model isolates
  contention; it is not a complete TCP feedback model or a production trace.

Radio continuity loss in an earlier retained run clustered during the LTE
outage, with small-packet drops continuing after the transition. Earlier
voice-only testing on standby Starlink delivered both echo streams without
loss and with about 59–60 ms p99 RTT. Contention under load remains an
investigation target; do not assume faster outage detection alone fixes it.

## Rejected approaches and investigation limits

- Faster multiplicative discovery reached 94–97 Mbps in the deterministic
  discovery model but regressed the full radio benchmark to 59 Mbps download
  and failed TCP outage progress. It was removed.
- A common deficit scheduler for all flows caused startup voice loss. It was
  removed; the retained design uses the small-flow ring and bulk reservation.
- Prepending newly active small flows did not solve the contention reproduction.
  Preferring non-ACK flow heads delivered all 200 voice packets but still had
  91 ms one-way p99 and did not establish bounded ACK/TCP service. It was removed.
- Doubling the bulk-service interval failed the memory/UDP starvation contract.
  Doubling the feedback interval worsened startup voice delivery. Neither remains.
- Removing the send/delivery-ratio guard from the queue-delay rate cap reproduced
  severe jitter collapse: only 78,000 payload bytes in the final five seconds
  of a 10 Mbps model, with target near 16.6 kB/s. That overlay experiment was
  rejected; do not repeat it as an unqualified fix.

A temporary CPU profiling build of the preceding checkpoint measured only
7.19 hub and 10.13 edge CPU-seconds over 25 seconds; aggregate guest CPU stayed
89.7%/86.2% idle. That run does not support CPU saturation as its bottleneck.
A warmed run achieved 96.50 Mbps TCP with 237 retransmissions; UDP received
99.39 Mbps from 110 Mbps offered, with 8.71% loss. Queue drops and repair expiry
still increased. These measurements do not establish a single remaining cause
or rule out CPU limits on different hardware. No permanent profiler was added.

## Next steps, in order

1. **Resume from the committed baseline.** Run the two progression tests and
   retain their failure output before changing code. Keep candidate binaries,
   source revision/diff, profile, warmup, SHA256 and raw results together.
2. **Prioritize the slow ramp reported in production.** Separate discovery
   from steady congestion response and instrument one
   capacity transition: pacing target, actual send/delivery rates, window versus
   bytes in flight, queue-delay samples and feedback timing. Establish why each
   increase or decrease happens. Any faster discovery candidate must preserve
   the startup voice, shallow-buffer and busy-jitter regressions and then pass
   the radio VM checks; the isolated 100 Mbps model is insufficient.
3. **Resolve loaded voice contention.** Account separately for voice-sized
   data, native transport feedback, TCP ACKs, bulk service and repair/duplicate
   traffic during the LTE outage. Measure queue residence as well as network
   delay. Verify the response to ACK backlog and preserve TCP progress while
   improving voice service. Do not infer application capacity from pacing
   targets or aggregate interface counters alone.
4. **Run matched VM acceptance scenarios.** Require both throughput directions,
   both WAN counters and the full simultaneous TCP/voice outage scenario. Keep
   all failures. Do not relax thresholds, hide startup with longer warmup, or
   round a near miss into a pass. Repeat a candidate after a material correction
   or to resolve observed variability, rather than selecting a favorable run.
5. **Prepare an intermediate production candidate only with explicit limits.**
   Document whether wire/config compatibility requires both ends to change.
   The user deploys. Subsequent live validation should verify binary identity
   and collect both controllers during the same working Speedtest invocation.
   Root-run Speedtest previously failed with `Cannot write`; a tunnel-local
   iperf attempt timed out, so those were not valid throughput measurements.
6. **Then perform cleanup and engine migration** in the order below. Avoid
   mixing a controller change, feature retirement and engine upgrade in one
   candidate whose regressions cannot be attributed.

## Reproduction commands and lab state

Run from the `fecalia` repository. Go is supplied by `nix develop` (Go 1.26.4).
The opt-in tests below are expected to exit nonzero at this checkpoint:

```sh
nix develop --command go test -tags progression ./internal/bond \
  -run 'TestCapacityDiscoveryAtCellularRTT|TestSparseSmallFlowsSurviveSustainedACKBacklog' \
  -count=1 -v
```

The lab guests are **stopped**, with disks and results retained. They are two
Alpine 3.24.2 KVM guests, each with four vCPUs and 2 GiB RAM, private WAN links
and loopback-only management SSH. No host filesystem/device mounts or production
network changes are needed. The scripts serialize lab mutations with a lock;
do not run competing scenario writers against the same lab.

```sh
nix build --cores 4
python3 test/vm/lab.py up
python3 test/vm/calibrate.py --profile test/vm/profiles/fast.json
python3 test/vm/benchmark.py result/bin/wanbond --policy adaptive \
  --profile test/vm/profiles/fast.json --warmup 15 --seconds 20
python3 test/vm/benchmark.py result/bin/wanbond --policy adaptive \
  --profile test/vm/profiles/radio.json --idle-seconds 60 --warmup 10 --seconds 30
python3 test/vm/continuity.py result/bin/wanbond --profile test/vm/profiles/radio.json
python3 test/vm/continuity.py result/bin/wanbond --profile test/vm/profiles/basic.json
python3 test/vm/lab.py stop
```

Known failing scenarios exit nonzero; run subsequent scenarios separately and
preserve each result. Calibration failure makes a throughput conclusion
inconclusive. The lab requires the documented sandbox/KVM capabilities; use
the local-test-vms skill when resuming it.

The radio profile has uplink capacities 0.4+1.25 Mbps and downlink capacities
0.5+100 Mbps; Starlink delay is 20±10 ms with 0.4% loss, LTE is 40±30 ms, per
direction. It is a stress model, not an exact modem/RF trace. Continuity runs
65 seconds with simultaneous bidirectional TCP and two 50 Hz, 160-byte UDP
echo streams, including independent five-second WAN outages. Gates are <1%
echo loss, <150 ms p99 RTT, <150 ms maximum receive gap, and TCP receiver progress
in each measured one-second outage interval. TCP uses 1 KiB application blocks
and receiver reports; the old 128 KiB block check produced false stalls.

Use the full default gate in [AGENTS.md](../../AGENTS.md), focused race tests
and `nix build` before handing over runtime changes. The Nix build does not run
the Go tests. The current vendor hash is
`sha256-xo0u/qHy4EzDccrGVMWXcEwT7damVNg0Lbeb/JiGh1Y=`; changing the locally
replaced engine can require refreshing it even without a root `go.mod` change.

## Cleanup and AmneziaWG migration backlog

The detailed source audit is
[Code audit and AmneziaWG migration](20260928-2000-code-audit-amnezia-migration.md).
Its upstream/version observations are dated to that audit; recheck before an
actual upgrade.

- **Done:** `.cq/` removed in `7d23ed7`, about 110 MB across 826 files. History
  retains it. Do not recreate it or rewrite Git history.
- **Not approved as retirements yet:** weighted scheduling, active-backup/data
  thrift, Reed–Solomon FEC and their old control/shaper plane. Decide which
  behaviors remain supported before deleting implementation/config/tests.
- **Shared code must survive:** path health and membership, authenticated
  peer/source demux, replay protection, bulk resequencing, routing, DNS, MTU
  handling, exits and lifecycle. Adaptive still uses legacy scheduler membership
  plumbing; extract a path registry before deleting that package.
- **Deduplication candidates:** DNS A/AAAA result aggregation, duplicate legacy
  carrier-selection loops if retained, and repeated recovery documentation.
  Consolidate tests by preserved behavioral contracts, not filename or size.
- **Migration candidate investigated:** AmneziaWG Go `v3.1.20260828`, commit
  `b5928efb6ca19f0153958460c3d141f04abc5c2e`, using the `/v3` module path.
  The compile probe failed on local completion/admission/statistics APIs absent
  upstream. The newer local encrypted-flow metadata API must also be ported or
  deliberately replaced. No migrated engine was run.
- **Migration sequence:** stabilize adaptive; settle legacy scope; port required
  engine contracts with existing wire settings; verify old/new and new/new,
  rekey/restart/NAT/MTU/multiple peers; enable new obfuscation profiles separately.
  Preserve device isolation/race tests, completion semantics and inner replay
  validation. S3/S4 padding/header protection change framing and MTU assumptions.

## Where to resume reading and find evidence

| Location | Purpose |
| --- | --- |
| [test/vm/README.md](../../test/vm/README.md) | Lab operation, acceptance gates and historical measurements |
| [docs/design.md](../design.md#adaptive-transport--internalbond) | Transport invariants and current controller behavior |
| [docs/manual-checklist.md](../manual-checklist.md) | Verification checklist |
| `internal/bond/bond.go`, `queue.go` | Deterministic transport, feedback, timers and scheduling |
| `internal/bond/*progression_test.go` | Two known failing models |
| `internal/bind/adaptive.go`, `adaptive_contract_test.go` | UDP adapter and memory/UDP delivery contracts |
| `third_party/amneziawg-go/conn`, `device` | Local engine metadata and completion patches |

Local persistent artifacts:

- Lab: `/srv/nvme/tmp/agent-vms/wanbond-bonding/`; result directories above contain
  raw iperf/voice JSON, metrics, daemon logs and binary identity. Private lab keys
  remain there; do not copy credentials into documentation or commits.
- Investigation: `/srv/nvme/tmp/wanbond-audit-20260928/`.
- Production capture: `pi-user-load-metrics.txt`, `raspi5l-user-load-metrics.txt`.
- Validation: `repair-final-gate.log`, `checkpoint-final-nix.log`,
  `radio-v21-gate.log`, `retained-progression.log`, `checkpoint-lab-stop.log`.
- Packet/CPU evidence: `bidir-capture/`, `profile-v20/`.
- Rejected experiments: `discovery-v22-rejected.patch`, `radio-v22-vm.log`,
  `sparse-flow-*.log`, `queue-cap-overlay.json`, `queue-cap-experiment.log`.

Root SSH to the Pi was authorized earlier. Router access to `mo.zt.7mind.io`
and `home.zt.7mind.io` must use the user's sandbox exchange workflow when needed;
read the environment skill then. Do not put production PSKs or monitor tokens
in a handover. No router access is needed to resume the local reproductions.
