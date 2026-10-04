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
