# Stage 1 shared-pacing trial — 2026-10-04

State: experimental draft; stages 1–3 are not accepted. The deployed build
was unchanged at the baseline checks below. No permanent deployment is
part of this work.

## Provenance

**Observed:** the repaired lone-survivor reproduction passes in the targeted
model, with bulk 9,600–10,800 B/s and 500/500 voice datagrams at one-way
p99 65 ms. `f75668e` delivers 2,400–3,600 B/s with p99 55 ms for the same
fixture and independent 6,302 B/s payload reference. The improvement is
model bulk service; latency rises by 10 ms in this fixture.

**Observed:** `stage1-shared-pacing-known-or-dominant-window.txt` passes the
targeted voice, isolation, cold discovery, wandering-delay and catch-up
checks. `stage1-draft-bond-regression-v2.txt` has one full-suite failure:
`TestLightlyLoadedLaneTargetStaysWithinCapacity` observes a target of
78,778 B/s on a 62,500 B/s lane, above its unchanged 75,000 B/s bound.
Three repetitions reproduce the same failure. That assertion is retained.
The progress-bounded stall-flight check subsequently passes the actual
stall and slowdown bulk gates but retains this target failure.

**Observed:** `stage1-shared-pacing-outage-model-v2.jsonl`, before the later
stall-flight change, gives 1a 5 passes/3 failures, 1b 0/4, 1c 9/7.
Neither this run nor the targeted passes establish stage 1 acceptance.
The final draft still needs a complete scenario run, both-family lab series,
existing continuity/benchmark/UDP gates, the full non-privileged gate and
`nix build` before a completed handover.

**Inferred from code:** ACK progress can replace the lane's boolean stall
state and silent capacity cut without a new wire field. The shared pacer
must distinguish the DATA budget represented by ACK v1 from locally sent
feedback. Physical receipt progress beyond recovery's outstanding flight
can replace its fixed clearance timeout. These inferences motivate the
draft; they are not evidence of field benefit.

**Document evidence:** BBRv3 sampling, completion-aware scheduling and QUIC
recovery support the experiments in the plan's research follow-up. No source
establishes that this candidate improves these 5G/Starlink links.

## Source and executable

Source base `04a745d`; retained full patch `stage1-c4-source.patch`, SHA256
`5799be7e16dc6279d3101512a419d6980d34289b5062d0921cad2162012c880f`.
ARM64 executable `c4-s1-share`, SHA256
`45b28c75b6e66bc2a65c566e7e50c2bbb12d07d5da1ae9df52ff7dd41999c5b4`.
The evidence root is `/srv/nvme/tmp/wanbond-adaptive-evidence`.
No wire format, inner anti-replay or authentication invariant changes.
The Go AST count of `control.go` is 31, down from 32: the old stall timeout
and shrinking time envelope are replaced by a receipt-progress boundary.

## Field baseline and collection defects

**Operator evidence, 2026-10-04:** the current field configuration is
Starlink standby, capped at 0.5 Mbit/s in both directions, and 5G capped at
100 Mbit/s downlink and 10 Mbit/s uplink. These are configured maxima,
not observed sustainable service under current RF conditions. The gross
pair ceilings are therefore 100.5 Mbit/s down and 10.5 Mbit/s up before
voice and protocol overhead; direct-before-tunnel measurements still define
the contemporaneous field reference.

**Observed in source:** `radio.json` and the deterministic radio fixture
instead use Starlink 0.5 down/0.4 up and mobile 100 down/1.25 up. The
collector's built-in `FIELD` uses Starlink 0.5 symmetric and mobile 50 down/10
up. Those retained fixtures do not reproduce the newly stated field caps.
The 300+300 Mbit/s `gigaradio` failure below is a required stress-test failure,
not the field's required goodput or an observation of its controller state.
The earlier capped field rounds remain bounded observations; they do not
establish either a gain or a failure at the current full-capacity reference.

**Observed:** the deployed executable on both hosts has SHA256
`f0cb62b2e221b413436c76a428e58dc177110deace3d70d87ee7db08eeec1375`.
The first mobile-blackout preflight stopped before any impairment because
5G was down and two direct ICMP destinations gave no replies. The native
voice round on the available Starlink path delivered 3,000/3,000 echoes per
direction, maximum gaps 101/73 ms, and RTT p99 62/60 ms. Its mobile VLAN
counter increased 0.155 MB including helpers; this is not a modem billing
total and does not establish two-WAN failover.

The first bounded TCP collector failed on a missing observer declaration
during setup; its two task-specific voice listeners were removed, verified
by PID pattern and closed ports. It installed no candidate or WAN impairment.
The repaired collector then delivered all voice echoes but TCP connection
setup timed out both ways. Read firewall rules allow UDP 60000–61000 and
omit the chosen TCP port. That run consumed 5.998 MB on the mobile VLAN,
including collection. It is not a bulk policy verdict. A subsequent trial
uses a temporary exact-peer, TUN-only TCP rule with a verified 180-second
removal timer and TCP preflight before traffic.

The operator reported a router update and requested a five-minute wait.
Field activity stopped for that interval. Subsequent observations show 5G
up/Starlink down, one SSH timeout, then both uplinks up. Their cause is not
established. Later comparisons must remeasure both direct paths and retain
this variability. Direct ICMP is a latency observation, not TCP goodput
calibration. Reaching an 8,000 B/s offered ceiling establishes a service
lower bound, not full capacity or aggregation.

## Pending measurements

Candidate trials must verify both executable hashes and restoration timers,
begin with voice, and restore the deployed build after each comparison.
Each impairment requires live paths immediately beforehand and its own
verified removal timer. Native bounded TCP and single-WAN voice measurements
will be recorded here with current direct-link observations, actual payload
receipt and mobile VLAN MB. Stages 2 and 3 remain outstanding.

## Subsequent field observations

**Observed:** both binaries were run temporarily through `candidate.sh`,
with ten-minute restoration timers verified before replacement. Every
blackout below affects only mobile egress (`end0.232`, 100% loss for about
15 seconds). Both lanes were observed up immediately before impairment;
each removal timer was verified before `tc`. These are one-direction
outages, not two-direction 1a trials. Direct Starlink and 5G ICMP measurements
precede each tunnel round. Their variation and the differing startup states
prevent attribution of a single difference to the policy.

| Voice round | Lost echoes, edge/hub, of 3,000 each | Maximum gaps, ms | RTT p99, ms | Mobile VLAN MB |
|---|---|---|---|---|
| Warm `c4`, mobile outage | 0 / 0 | 80 / 84 | 55 / 59 | 3.036497 |
| Restored baseline, mobile outage | 34 / 37 | 81 / 82 | 85 / 89 | 2.761980 |
| Fresh `c4`, mobile outage | 0 / 0 | 125 / 109 | 75 / 81 | 2.887876 |
| Fresh baseline, mobile outage | 0 / 0 | 113 / 77 | 68 / 70 | 2.963275 |
| Fresh `c8`, mobile outage | 0 / 0 | 93 / 73 | 67 / 63 | 2.916792 |

The baseline's first loss difference did not repeat consistently. No field
improvement is established. These observations do not provide a matched
survivor voice-idle reference for the p99 gate or a three-run lab series.

**Observed:** native TCP beside voice delivered the complete, digest-checked
240,000-byte payload in each direction on the baseline, `c4`, `c8`, and the
restored baseline after `c8`. Each reaches the offered 8,000 B/s ceiling;
none measures capacity or aggregation. All these rounds delivered 3,000/3,000
voice echoes each way. RTT p99 edge/hub was 63/60 ms on the initial working
baseline, 72/63 on `c4`, 73/56 on `c8`, and 86/90 after restoration. The
temporary exact-peer TCP rules were removed on both hosts after every round.

The recorded stage 1 rounds total **45.135906 MB** in mobile VLAN RX+TX
counter deltas, including helpers and the failed firewall collection.
This is not a modem billing total or a continuous task-wide counter interval.
The two edge binaries were gzip-compressed: 4,663,222 bytes for `c4` and
4,695,943 for `c8`. The management transfer's provider was not established;
those bytes must not be silently attributed to, or added to, the VLAN total.

**Observed:** final restoration checks verify the deployed SHA256 on both
hosts, empty runtime service drop-ins, inactive candidate restoration timers,
removed task TCP rules, closed test ports, and `noqueue` on both WAN VLANs.
No nix-config deployment was edited. Evidence:
`stage1-c8-restoration-verified.json` and the individually named
`field-voice-stage1-*` directories under the evidence root.

## Revised pacing draft and remaining failures

`c8-s1-share` was built from `2e3c261` plus `stage1-c8-source.patch`, SHA256
`2bd5e76d960e1806b2c7515daeb4c87619f1f10c822796d4f277029f358c1bf8`.
Its executable SHA256 is
`c35834c70f466b55986113c3c9df45075f8189d3f31a033dd218be0a62af33a1`.
It retains `c4`'s progress liveness and shared pacing, but limits an original
voice datagram's extra pacing allowance to its own size when other leased
lanes exist and no lower class occupies the lane. A sole leased lane,
lower-class flight, or reserved copy permits one full-datagram allowance.

**Observed:** unlimited allowance in `c4` failed the unchanged 75,000 B/s
target bound. Removing the allowance entirely in that case passed the bound
but regressed cold-stall bulk. Using only the voice size passed those checks
but produced 76 ms against the unchanged 75 ms sole-lane voice bound.
The final `c8` targeted run passes all three: highest target 66,496 B/s,
cold-stall service above its existing bounds, and sole-lane voice p99 72 ms.
The full non-privileged gate and `nix build` pass on this source before the
subsequent TCP-model correction below.

**Observed:** the complete original-input `c8` outage model still gives
1a 4 passes/4 failures, 1b 0/4, and 1c 8/8. A diagnostic removal of the
multi-lane voice-isolation rule raises voice p99 to 35 ms against 25 ms and
to 73 ms against 70 ms in two existing outcome tests, while all four recovery
cases still fail. That diagnostic was reverted; the restored production
patch has the same SHA256 as the tested `c8` source. These results rule out
unconditional isolation removal as the correction, not every possible
allocation policy.

## TCP-model RTT correction

**Observed:** tracing radio 1a found the model TCP sender's RTO reaching
13.334881677 seconds while its survivor still carried wire traffic. The
model cleared its retransmission scoreboard at an RTO, then allowed an
original send timestamp of a previously retransmitted segment to become an
RTT sample. A separate reproduction fails on `f75668e`: the resulting
1.20625-second RTO prevents the required retry. Removing a segment's
RTT-eligible send timestamp at every retransmission makes it pass without
changing production code or scenario thresholds. Both TCP models receive
the same correction.

**Document evidence:** [RFC 6298 section 3](https://www.rfc-editor.org/rfc/rfc6298.html#section-3)
requires Karn's exclusion of retransmitted segments without timestamp
disambiguation. This reproduction establishes that specific model defect;
it does not prove every RTT sample in the approximation matches Linux TCP.

**Observed:** one Karn-corrected-input `c8` run gives 1a 4/4, 1b 1/3, and 1c 8/8
passes/failures. The Karn-corrected-input `f75668e` series completed three runs of
all 52 scenario cases, with identical measurements and verdicts in each run.
Its outage verdicts remain 1a 5/3, 1b 0/4, and 1c 5/11 passes/failures;
radio 2d and the 3a voice-latency findings remain passes. Evidence:
`adaptive-policy-f75668e-karn-three.jsonl` and its `-summary.json`.
Earlier model measurements retain their input provenance. The full non-privileged
gate also passes after this test-infrastructure correction
(`stage1-c8-karn-nonprivileged-gate.txt`). Stage 1 is still unaccepted;
no stage 2 or 3 replacement has been implemented.

## Fresh SACK timing and explained stage 1 stop

**Observed:** a subsequent trace exposed another RTT-model defect: a segment
already selectively acknowledged was sampled again when a cumulative ACK
advanced past it. A reproduction on original-controller source delays the
next required retry with an inflated 1.15-second RTO. A second reproduction
shows fresh selective feedback being ignored, leaving the initial one-second
RTO. Both pass after the two models share receipt-timing selection: exclude
previously SACKed segments, exclude cumulative measurements covering a
retransmission, and use fresh, unretransmitted SACK timing when needed.
Duplicate SACK reports contribute no new sample. Production code and the
scenario gates are unchanged.

**Observed:** the fresh-SACK-corrected `f75668e` series completed all 52 cases
three times, with identical measurements and verdicts. Outage passes/failures
remain 1a 5/3, 1b 0/4, and 1c 5/11. Radio 2d now fails: direction 0 voice
p99 is 151 ms against the unchanged under-150-ms gate; bulk still passes
both directional bounds. Its earlier pass remains a finding from the older
input model, superseded for current verdicts by this correction. The 3a
voice-only passes remain. Evidence: `adaptive-policy-f75668e-sack-three.jsonl`
and its `-summary.json`. No model test was bent to recover the earlier pass.

**Document evidence:** Linux v6.18's
[`tcp_clean_rtx_queue` and `tcp_ack_update_rtt`](https://github.com/torvalds/linux/blob/v6.18/net/ipv4/tcp_input.c#L3034)
exclude previously SACKed data from cumulative timing, reject ambiguous
cumulative timing covering retransmissions, and use fresh SACK timing as a
fallback. This aligns that part of the approximation; it does not establish
complete Linux TCP equivalence. Evidence: `model-sack-f75668e-red.txt`,
`model-sack-timing-green.txt`, and test-only commit `b210aaf`.

**Observed:** with those corrected inputs, `c8` still gives 1a 4/4, 1b 1/3,
and 1c 8/8 passes/failures in one complete run
(`stage1-c8-outage-model-sack-v1.jsonl`). In gigaradio 1a lane 0 with bulk,
the surviving direction-0 lane remains live. At 22.999 seconds its target is
676,154 wire B/s; the gate requires 20,242,721 TCP payload B/s, three seconds
after the blackout. Its capacity estimate is 802,783 B/s despite physical
service of 37,500,000 B/s. Before the blackout, its target was already
356,020 B/s with repeated delay cuts. The trace retains the lane's delivery,
window, liveness age and controller decisions
(`stage1-c8-giga0-sack-diagnostic.txt`).

**Inference:** even spending the entire recorded target on payload cannot
meet that deadline; liveness eligibility alone cannot correct this capacity
and delay failure. This is a bound for this candidate and measured phase,
not a proof that every policy fails. Under the operator's explained-gate-failure
rule, this attempt stops. Stage 2 or 3 mechanisms are not folded into stage 1,
and no acceptance bound is weakened.

The production draft is retained as code-only commit `94b15c4` on
`adaptive-stage1-c8-final`. Its patch SHA256 remains
`2bd5e76d960e1806b2c7515daeb4c87619f1f10c822796d4f277029f358c1bf8`;
this is the source already tested temporarily in the field. `main` retains
the stage 0 controller and the committed outcome tests/model corrections.
Observed again: both production daemons run the deployed store executable;
the earlier full restoration verification remains recorded separately.
No new field payload trials followed this model correction.

**Observed:** the full non-privileged gate and `nix build` pass after restoring
the stage 0 controller (`stage1-c8-final-restored-nonprivileged-gate.txt` and
`stage1-c8-final-restored-nix-build.txt`). The candidate also passed the full
non-privileged gate before restoration
(`stage1-c8-sack-nonprivileged-gate.txt`). These are default-suite checks,
not passes of the tagged progression scenarios or the missing candidate lab
series and existing lab gates.

## Approved matching-cap comparison — 2026-10-04

The operator approved the next strategy in the adaptive-policy plan. This
resumes bounded diagnosis of the retained candidate; it does not accept
stage 1 or combine later stages with it. The goal API refused a replacement
because its earlier unfinished goal is paused, and supplies no resume
operation. The approved strategy is recorded in the plan.

**Observed model evidence, receipt-timing-corrected input before the
loss-evidence correction below:** `field-standby.json` supplies the same current
rate caps to the lab and deterministic model. Its fixed 14/28 ms delays,
zero random loss, satellite policer and 300 ms mobile buffer are assumptions,
not a current RF trace. Three baseline voice-only reference runs give
41/41 ms satellite p99 and 59/59 ms mobile p99, with all 700 echoes per
direction delivered in `[5,19)` seconds. Baseline and `c8` both pass all six
voice-only outage variants and fail all six combined bulk/outage/recovery
cases across three repetitions. No existing radio/gigaradio input or gate
was changed. For the bidirectional mobile blackout, `[23,35)` TCP delivery
is 1,200/3,500 B/s on baseline and 8,700/8,100 B/s on `c8`. The independent
reference is 12,110 B/s, so the unchanged 75% gate still fails.

**Observed diagnostic evidence with that earlier input:** on the same fixture with only satellite
from startup, `c8` delivers 13,800 B/s each way in `[15,25)`; five consecutive
early voice losses nevertheless fail continuity. During mobile failover,
the satellite target at 22.999 seconds exceeds its physical rate in both
directions, while the TCP sender retains 812/37 outstanding segments and
has entered timeout recovery. At 34.999 seconds these counts are 725/5.
The large direction-0 flight predates blackout: 882 segments at 19.999
seconds. **Inference:** a low survivor target is not sufficient to explain
this matching-cap failure; the transfer history and recovery differ from
the stationary case. This is distinct from the earlier gigaradio target
bound, not proof of a field root cause or a proposed estimator correction.
Evidence: `field-standby-c8-{survivor-diagnostic,tcp-handoff-trace}.txt`.
The trace instrumentation was test-only and restored after collection.

**Observed field evidence:** the voice series uses deployed → `c8` →
deployed, with both daemon ages between 180 and 195 seconds when collection
begins. Each round measures direct Starlink, direct 5G and tunnel TCP in
that order first, at a 1 Mbit/s offered ceiling, and then runs two 50 Hz,
160-byte echo streams for 60 seconds. A verified timer removes the single
5G-egress blackout after 15 seconds; the current management reply route is
checked separately. Candidate SHA and executable are verified on both hosts.

| Warm round | Lost echoes, edge/hub | Maximum receive gap, ms | Whole-run RTT p99, ms | Outage RTT p99, ms |
|---|---:|---:|---:|---:|
| Deployed A | 3 / 0 | 81 / 119 | 59 / 58 | 132 / 74 |
| `c8` | 0 / 0 | 77 / 82 | 54 / 55 | 60 / 61 |
| Deployed B | 0 / 0 | 97 / 91 | 71 / 68 | 84 / 71 |

All three pass the loss, consecutive-loss and receive-gap checks. The
outage RTT measurements exclude the first second, using the recorded guest
application/removal times. They are measurements, not independent
survivor-idle + 50 ms gate verdicts. **Inference:** this series does not
establish a repeatable voice gain. Direct Starlink upload is 0.506–0.517
Mbit/s and download 0.530–0.534; 5G and tunnel reach approximately the
1 Mbit/s offered ceiling, establishing lower bounds only. Both deployed
executable hashes and empty runtime drop-ins were verified after restoration.
Evidence: `field-matched-series-standby-voice-20261004-143324/manifest.json`
and each round's `summary.json`.

**Observed collection interruptions:** the first voice reference attempt
failed before impairment because the field SSH wrapper overrode the worker
host-key file; selecting the SSH executable with the correct worker file
corrected the collection. A later 24,000 B/s bulk series stopped before
impairment on an SSH connection timeout after four direct-link transfers.
Restoration succeeded. The following edge observation reports Starlink DOWN,
and source-bound ICMP to the concentrator receives no replies. The cause is
unknown; the 5G blackout is held until both lanes are observed working.
No bulk policy verdict follows from that interrupted collection.

**Observed mobile VLAN accounting:** the completed voice series consumes
26.449579 MB continuously across restarts, direct references, voice and
cleanup. Its three voice collectors account for 12.186687 MB within that
larger interval. The failed host-key attempt adds 1.234752 MB and the
interrupted bulk series 8.257803 MB in disjoint continuous intervals.
These counters include background traffic and are not modem billing totals.
No binary was transferred again.

## Three selective receipts and current verdicts

**Observed reproduction:** with ten outstanding full segments, receipt of
only segment 10 caused the model to retransmit 1–7 immediately. The
production-source `f75668e` worktree reproduces this through its test-only
adapter. Both models used sequence distance from the highest SACK rather
than the three later receipts their model contract describes. The new
test covers one receipt, its duplicate, a second distinct receipt and the
third receipt's positive loss case. It fails for the stated reason before
the correction and passes afterwards. Evidence:
`model-sack-loss-evidence-{exact-f75668e-red,green}.txt`; code-only commit
`75ebc1f`.

**Document evidence:** [RFC 6675 section 4](https://www.rfc-editor.org/rfc/rfc6675.html#section-4)
defines loss using SACKed sequences or SACKed bytes above a hole. Sequence
distance alone is not that evidence. The models now retain the three
highest distinct full-segment receipts and use the third as the loss
boundary in both callers. This does not implement complete RFC 6675 or
Linux RACK recovery. **Observed in the field:** both kernels use CUBIC,
SACK and `tcp_recovery=1`; the edge kernel is 6.18.52 and concentrator
6.18.42. The model remains a documented approximation, not a Linux kernel
equivalence claim.

**Observed current model results:** all 52 original-controller cases have
identical measurements and verdicts across three runs. The first command
ran 50 cases; cold row 0's two cases completed separately, three times.
The combined summary validates all 52. Baseline passes/failures are 1a 5/3,
1b 0/4, 1c 6/10, 2a 0/4, 2b 0/2, 2c 0/2, 2d 0/2, 3a 2/2, 3b 0/4,
3c 0/4 and 0 0/2. Radio 1c lane 0 direction 0 with bulk now passes;
the plan's predicted caller-visible failure is wrong in that case and the
pass is retained. Radio 2d still fails at 151 ms p99. `c8` gives 1a 5/3,
1b 1/3, 1c 9/7, identically across three runs. No acceptance threshold or
production controller changed. Evidence:
`adaptive-policy-f75668e-sack-loss-full-summary.json` and
`stage1-c8-outage-sack-loss-three-summary.json`.

The additional field-cap fixture still gives six voice-only passes and
six combined bulk/outage/recovery failures on both controllers, identically
across three runs. Current mobile-blackout `[23,35)` TCP delivery is
700/1,800 B/s on baseline and 1,600/3,600 on `c8`, against the unchanged
12,110 B/s reference. These supersede the earlier model numbers for current
verdicts; they are not field measurements.

**Observed renewed failure bound:** after this correction, gigaradio's
direction-0 survivor at 22.999 seconds is live, with a 653,342 wire B/s
target and 737,189 B/s capacity estimate. Its deadline requires 20,242,721
TCP payload B/s; actual final-second payload delivery is 84,000 B/s.
The target was already 356,020 B/s before blackout. **Inference:** even
spending the entire target on payload cannot satisfy the deadline. This
renews the explained failure for the retained stage 1 attempt; liveness
eligibility alone cannot meet that phase's gate. It does not prove a field
root cause or impossibility for every policy. Evidence:
`stage1-c8-giga0-sack-loss-bound.txt`.

On the matching-cap diagnostic with corrected input, the satellite-only
stationary transfer delivers 11,040 B/s each way; five early consecutive
voice losses still fail continuity. During mobile failover, direction 0
retains 824 outstanding TCP segments at 22.999 seconds and 808 at 34.999;
its target at 22.999 is 85,991 B/s. Direction 1's target is 61,083 B/s,
near the configured 62,500 B/s service. **Inference:** this different
failure needs a recovery diagnosis, not an assumption that the gigaradio
target bound explains it. Evidence:
`field-standby-c8-sack-loss-handoff-trace.txt`; test-only instrumentation
was restored after capture.

**Observed additional lab evidence:** the new fixture's UDP calibration
passes above 85% on every WAN/direction. The host's trace includes one
99% busy 100 ms sample; guest wake maxima are 5.8/10.3 ms with observed
steal. A static `c8` blackout diagnostic completes, with voice continuity
checks passing. TCP receiver delivery after mobile recovery's five-second
deadline averages 68,587 B/s down and 989,290 up. The run lacks independent
phase goodput/idle-latency references for a full gate verdict, and its
startup differs from the warm field series. This is not the required
radio/gigaradio three-run series. Evidence:
`field-standby-{lab-calibration,calibration-timing-summary,c8-lab-receiver-diagnostic}`
under the evidence root, with their `.txt`/`.json` suffixes. The first
diagnostic build omitted `CGO_ENABLED=0`, required an unavailable Nix
loader in Alpine and failed before traffic; the corrected static build's
SHA is `928afcde34837cecbcd31fb20d9928081ccf1c8f3626f6b12feeff15c43445ec`.

**Observed field interruption and restoration:** another bulk attempt
stops on an SSH timeout during the startup wait, before direct calibration
or impairment. It adds 1.194921 MB in its continuous mobile counter interval.
A fresh read at 14:19 UTC verifies both deployed executable hashes, empty
candidate overrides and inactive restore timers. The edge has no test WAN
qdiscs or task TCP/voice listener. Starlink lanes are DOWN on both hosts
and 5G lanes are UP. The field bulk comparison is held; no verdict is inferred from
these incomplete collections. The continuous mobile VLAN increase since
the first matching-series attempt is 64.076884 MB, including background
traffic and idle waits, not a billing total. Evidence:
`field-matched-restoration-current-20261004-151943/manifest.json`.

**Observed verification:** the full non-privileged gate and `nix build`
pass with the corrected models. `main` retains stage 0 production behavior;
the approved stages 1–3, their three-run lab gates and candidate regression
series remain incomplete. The stage 1 attempt is still stopped at an
explained gate failure; later estimators are not folded into it.
