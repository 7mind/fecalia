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
