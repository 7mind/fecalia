# wanbond improvement plan — 2026-10-01

State: `main` = `db465b3`, deployed on the edge and `raspi5l` (store path
`i141lddc…`, 2026-09-30 23:19). Branch `level-shift2` holds unmerged transport
work. Evidence is in [the lab results](../../test/vm/README.md) and in the
production captures under `/srv/nvme/tmp/wanbond-awg3-20260929/prod-2026*`
(local scratch, not portable).

## Order of work

0. This plan.
1. **Cleanup first** (operator decision of 2026-10-01): retire every legacy
   policy and perform the cleanup and deduplication of
   [the code audit](20260928-2000-code-audit-amnezia-migration.md), work-order
   items 3 and 4. No transport behaviour changes in that work.
2. Return to the transport items below, in the order given.

## Field facts the items rest on

Measured on 2026-09-30/10-01, edge `pi` behind a satellite and a mobile link:

| Path | Speedtest down / up, Mbit/s | Loaded latency down / up, ms (iqm; high) |
|---|---|---|
| Mobile link alone | 54.0 / 6.73 | 152 / 476; 455 / 852 |
| Satellite link alone | 0.52 / 0.48 | 31 / 32; 296 / 316 |
| Bond via `raspi5l` | 30.8 / 6.05 | 82 / 84; 277 / 214 |

- The satellite link is a hard 0.5 Mbit/s cap that drops what exceeds it and
  does not delay it (loaded latency 31 ms at the cap): a policer. Its value to
  the bond is its 28 ms idle latency for voice and failover, not throughput.
- Its latency moves between levels: idle 15-second block means of 34-77 ms.
- The bond delivers 57% of what the mobile link alone does downstream, at
  half the loaded latency. Raw TCP reaches 54 Mbit/s by standing 150-450 ms
  of queue in the mobile link's buffer; the tunnel backs off at 20-30 ms.
- The mobile lane confirms 95-105% of its bytes in order; no reordering of
  the kind `wander.py` produces was seen at these rates.

## Transport items

1. **Queue allowance by what a lane carries.** Voice rides the lane with the
   lowest latency; the 150 ms voice gate constrains that lane only. The delay
   threshold that holds the mobile lane at 30-34 Mbit/s is the same on every
   lane. A lane that carries no real-time datagrams may hold a deeper queue
   (60-80 ms to start with); when real-time traffic moves onto it, after the
   other lane fails, it returns to the voice allowance. Failing test first: a
   two-lane model with voice on the low-latency lane and bulk on a
   deep-buffered one, asserting that bulk throughput rises and voice p99
   stays. Then both lab profiles with the continuity gates, then the field.
   Cost: the bond's own loaded latency for bulk rises towards 100 ms.
2. **Policed path** (`level-shift2`, commit "Hold a policed path at its
   capacity"). The satellite lane runs at 1.0-1.1 times its delivery under
   load; the loss is repaired over the mobile lane a round trip later and
   feeds the 214-277 ms latency highs. Lab: voice loss 0.06-0.49% on the
   policed profile, target at capacity. Open before a field trial: one
   300+300 UDP downlink sample of 237 Mbit/s against 366-423 otherwise, which
   fits a lane recovering slowly after its probes backed off.
3. **Loss from byte counts and the floor test** (`level-shift2`). Keep on the
   branch until a production capture shows a level shift read as a queue, or
   a loss cut on a lossless lane. The satellite lane's level shifts are real;
   whether they cost anything at 0.5 Mbit/s is not shown.
4. **Peer restart under load.** `1496e7d` keeps the capacity estimate across
   a peer restart. Verified in the field only for a lane without an estimate
   (cold discovery after the restart). To verify the failing case: restart
   one end after both lanes carried load, then load again.
5. **Acknowledgements that survive reordering.** The lane bitmap covers 64
   datagrams and the global one 256 sequences. Under `wander.py` a quarter of
   the wire bytes are repairs of datagrams that arrived. A cumulative
   per-lane receipt point beside a wider bitmap removes that; it is a wire
   format change and needs a versioned acknowledgement and a rollout order.
   Relevant once either link is fast enough to reorder (the 300+300 case).
6. **The 75% target on the mobile link.** Revisit after items 1 and 2: part of
   the 54 Mbit/s may exist only for a backlogged sender.
7. **Continuity gates.** The TCP-progress gate in the mobile outage and the
   voice gate with only the jittery WAN up fail some of the time with every
   build. Either the policy guarantees something concrete there (one bulk
   datagram a second; repairs of the dead lane's bulk do not consume the
   survivor's share) or the gates are restated.
8. **Lab fidelity.** Report the reordering each run ran under; emulate level
   shifts without reordering (netem reorders when it lowers the delay).

## Open observations

- A 305 ms voice gap in both directions at once in one lab run, with nothing
  in either daemon log.
- `o2` refuses the measurement key; nothing is known about its build or lanes.
- The satellite plan's 0.5/0.5 Mbit/s cap: confirm it is the plan and not a
  fault.

## Found during the cleanup (2026-10-01)

None of these was introduced by the removal; each was already true of the
adaptive transport and became visible when it was the only one left.

- **A handshake initiation issued before the hello completes is dropped.**
  Observed in `internal/device` (`TestFirstResolveInstallsEndpointAndInitiatesHandshake`):
  the initiation queued at endpoint install is counted as a queue drop
  (`QueueDrops` 1) and the handshake completes about 5.3 s later, on the
  engine's `RekeyTimeout` retry. The cause — the transport's 100 ms queue age
  expiring before the first hello at the 200 ms probe interval — is inferred
  from the code, not traced. At startup `startFirstPathUpHandshake` and
  `startPeerRestartHandshake` re-initiate. A failover between two endpoints of
  one concentrator process has neither trigger; whether it then waits 5 s is
  not reproduced. Reproduce in the lab before changing anything.
- **Reorder hold is 250 ms, not the 300 ms the transport asks for.**
  `bind/adaptive.go` requests `adaptiveReorderHold` (300 ms);
  `Resequencer.SetHoldBound` clamps to the construction timeout (250 ms).
  Decide which number is intended and make the two agree.
- **An IPv6 underlay path is sized with the IPv4 budget.** `bind.InnerMTU6` has
  no caller; `device.tunMTU` and the runtime resizer use `InnerMTU` for every
  path, so by arithmetic a full-size datagram exceeds an IPv6 path's MTU by 20
  bytes. Not tested at runtime; production paths are IPv4.
- **A path that is dead in one direction stays schedulable.** From reading
  `bind/multipath.go` and `bond/bond.go`: a lane's lease is refreshed by the
  peer's inbound probes regardless of this end's liveness verdict, so the lane
  is used until its acknowledgements stall. Not executed.
- **No end-to-end test bounds how fast traffic leaves a dead lane.** The netns
  and real-host failover tests measured it through the removed schedulers' log
  record; they now bound liveness detection only, and flow survival. The lab's
  continuity gates are the remaining measurement.
- **The monitor UI and TUI showed no lane state**, and the control's
  decisions were not exported at all. Both are added on branch
  `lane-observability`: capacity estimate, threshold and decision counters
  per lane on `/metrics`, in `wanbond monitor` and on the dashboard.
- **Four netns tests (`test/e2e`) fail on the worker VM for the baseline
  `db465b3` and for the cleanup tree alike**, each run alone as root on
  `llm-ubuntu-0` (Ubuntu, kernel 6.8), with the same message in both trees:
  `TestDNSHubResolveAndReroute` (the edge does not repoint to the renumbered
  concentrator within 15 s), `TestMultiPeerConcentratorIsolation`
  (`per-peer-metrics…`: edge A's iperf3 after its restart gets "Network is
  unreachable"), `TestE2ELossyPathPMTUConvergence` (wanbond0 stays at the
  1500-path size on the deterministic-loss path), `TestOneSidedRestartRecovery`
  (`ip addr add` on the persistent TUN: "Address already assigned"). Whether
  each is a product defect, a test defect or the host is not established. The
  rest of the tier passes on the cleanup tree when a test is run alone; in one
  whole-suite run three more failed from state left by earlier tests (a
  leftover `wanbond0`, a loopback bind refused).
- **The real-host tier (`test/realhosts`) was adapted but not run.**
- **`TestOneSidedRestartRecovery` still describes mechanisms that are gone**
  (a 2048-frame resequencer window, the low-anchor rebaseline); it cannot be
  judged until its fixture defect above is fixed.

## Found while modelling item 1 (2026-10-01)

Fixed on branch `bulk-beside-voice`, each with a failing-first model test and
lab figures in [the lab record](../../test/vm/README.md):

- **Bulk received nothing beside a call on the only lane up**, from a cold
  tunnel (lab: 0.0 Mbit/s of TCP for 20 seconds on a 6 Mbit/s WAN; 4.0 after).
- **The lane a call rides was kept free of bulk only by that defect.** A lane
  whose capacity was already known was shared, at 22-24 ms of serialization
  per bulk datagram on a 0.5 Mbit/s lane. Production voice latency therefore
  depended on whether the low-latency lane had carried bulk before the call.
  The rule is explicit now.
- **A bulk flood had voice refused at the tunnel's entrance** (lab: 35-61% of
  the voice datagrams lost beside 400 Mbit/s of UDP offered to a 32+96 Mbit/s
  bond; none after).

## Field run of 2026-10-01 (`main` = `b46f1c2`)

The first build with the lane control series. Edge `pi` behind the satellite
and the mobile link, exit `raspi5l`. Captures: all series from both ends once
a second (`prod-20261001-160301-field-obs1`, `…-180755-field-obs2`) and the
transport series about 17 times a second (`prodfast-20261001-181131-obs3`,
`…-182337-obs4`, `…-182539-obs5`), under `/srv/nvme/tmp/wanbond-awg3-20260929/`
(local scratch, not portable).

| Run | Method | Bond down / up, Mbit/s | Mobile link alone, same minutes |
|---|---|---|---|
| obs1, one minute after the restart | Speedtest | 24.2 / 1.5 | — |
| obs2, two hours later | Speedtest | 23.6 / 4.0 | — |
| obs3 | Speedtest | 24.7 / 4.9 | — |
| obs4, obs5 | one TCP flow for 7-8 s, alternating | down 21.4, 18.2, 23.1; up 3.6, 4.6, 3.7, 3.9 | down 48.9, 49.9, 48.8; up 1.4, 6.4, 6.0, 5.6 |

Measured the same way in the same minutes, the bond carries 43% of what the
mobile link alone does downstream and about two thirds upstream (the first
upload on the mobile link alone, 1.4 Mbit/s, is not explained).

What the counters and the fast captures show:

- **Nearly every repair is a duplicate.** In the obs1 download the
  concentrator sent 1119 repairs and wrote 1647 datagrams off as unconfirmed;
  the edge counted 1035 duplicates and its resequencer skipped 4 sequences.
  The concentrator's path sent 51.10 MB and the edge's paths received 50.98.
  The mobile lane took 21 loss signals, 4 reductions of its estimate and 3
  ends of discovery in those 18 seconds.
- **The mobile link stalls.** About once a second the bytes in flight on the
  downlink lane double for 0.2-0.4 s, with queue delays of 65-180 ms, and the
  lane then delivers as before. Repairs come in bursts of 319-354 within a
  second at those moments. Whether the path is silent throughout a stall or
  only slow is not resolved at 17 samples a second; both occur in the delivery
  figures.
- **The estimate is measured from delivery that a stall depressed.** In obs5
  the lane delivered 4.8-5.3 MB/s for a second and a half, ended discovery at
  5.06 MB/s, and 0.6 s later held an estimate of 3.22 MB/s, measured anew
  while delivery was at 3.0-3.4 MB/s with 91-184 ms of queue delay. The 7 s
  transfer ended before it was back. In obs3 a return to discovery after
  three won probes ended 0.3 s later at 3.47 MB/s; the estimate before it was
  4.64.
- **The target of a silent lane is cut by three tenths per control interval**
  (the rule for a lane whose datagrams time out with no acknowledgement
  since): 6.17 to 4.32 to 3.02 MB/s within 60 ms in obs5 and 4.55 to 3.19 in
  obs3, each by exactly 0.7 with no signal counted.
- **A cold uplink lane stays low.** One minute after the restart the mobile
  uplink ended its first discovery in the first second of the upload, at
  0.26 MB/s, on one delay signal at the threshold (20 ms), while the sender
  was still starting. Probes then raised and lowered it between 0.20 and
  0.30 MB/s for the nine seconds of the upload. Two hours and several
  transfers later it held 0.54-0.96 MB/s.
- **Delay signals on the uplink do not depend on the rate**: 2.0 a second at
  0.20-0.29 MB/s, 2.4 at 0.54-0.67, 3.4 at 0.60-0.97.
- **The satellite lane carries no bulk during a Speedtest.** Its traffic was
  small datagrams only (`interactive_sent_bytes` equal to `sent_bytes`), at a
  third to a half of its target. Inferred, not observed: Speedtest's own UDP
  loss probes are a real-time stream to the classifier, and the rule that
  keeps bulk off a slow lane beside a call applies. The cost is at most
  0.3 Mbit/s of the upload.
- **The lane bitmap confirms 56-58% of the mobile lane's bytes** at
  3-4.5 MB/s (`acked_bytes` against `sent_bytes`); the rest is confirmed by
  the global receipt only. Item 5 is therefore relevant at 30-40 Mbit/s, not
  only at 300.

Branch `field-stalls` (on `b46f1c2`, not merged) answers the first three, and
the fifth as far as the uplink's delay signals are stalls, which is not known.
A model lane that serves nothing for 100-300 ms about once a second and loses
nothing reproduces them (`internal/bond/stalling_lane_test.go`); the model's
stalls are complete silences of one mean length, which the field's are not.
None of it has run on a real link.

| Model lane, 50 Mbit/s, share of what it serves delivered | no stalls | 100 ms | 200 ms | 300 ms | half rate for 300 ms |
|---|---|---|---|---|---|
| `b46f1c2` | 85% | 78% | 24% | 1% | 73% |
| with `level-shift2` (loss from byte counts) | 85% | 76% | 55% | 49% | not run |
| `field-stalls` | 85% | 84% | 81% | 80% | 76% |

Means of eight schedules of stalls about once a second, the call on the other
lane; under 200 ms stalls `b46f1c2` ranges from 2% to 64%.

1. `level-shift2` is ported: its condition, a loss cut on a lane that lost
   nothing, is met by the first finding. Item 3 above is done on the branch;
   item 2 comes with it.
2. A datagram is not sent again on the lane it was last sent on until the
   peer has received a later one on that lane, or fewer than three followed
   it.
3. A lane that reported nothing new for 100 ms while it held datagrams, and
   then confirmed them, stalled. Delay within what is left of that backlog is
   not a congestion signal: discovery goes on, a probe is neither won nor
   lost, the estimate stands, and the target gives way as it does while a
   probe's queue drains.
4. An estimate taken at the end of discovery, measured anew after repeated
   cuts, or lowered by a delay signal does not go below the delivery the lane
   kept up over half a second within the last two and a half.

Not answered, and open:

- The silent-lane cut itself (three tenths per interval) is unchanged.
- In the model, a lane whose one direction stalls is not marked silent while
  another lane delivers: its target was never cut through a 400 ms stall.
  Inferred from the code, not traced: the receiver acknowledges on every lane
  whenever any receipt is unreported, and those acknowledgements count as the
  lane answering. This is the one-way-dead lane of the list above, seen now
  in a run.
- A lane whose estimate fell below what one full-size datagram needs in the
  queue allowance (1.2 Mbit/s) while it carries a call keeps bulk off, and
  nothing raises the estimate: probes need a sender the lane limits. In the
  model this held a 50 Mbit/s lane at 57 kB/s for the rest of the run.
- Slowdowns without silence gain little: 76% against 73% in the model.
- Whether the delay signals on the uplink are stalls is not known.
- Not run in the lab: the host was loaded (load average 18) when the branch
  was ready, and the lab has no WAN that stalls.

## After the deployment of `7351328` (2026-10-01, 20:55)

Same edge, same method, captures `prodfast-20261001-205813-post1` and
`…-210307-post2`. The mobile link alone was slower than in the afternoon.

| | Bond, Mbit/s | Mobile link alone, Mbit/s |
|---|---|---|
| Down, one TCP flow | 26.5, 28.6, 27.6 | 28.3, 38.1, 32.0 |
| Up, one TCP flow, alternating with the direct uploads | 5.1, 5.4 | 7.5, 6.7 |
| Up, bond only, ten seconds apart | 0.4, 0.4, 2.8 | — |
| Speedtest (operator) | 26.5 / 4.4 | — |

Downstream, per download, before and after: loss signals 7, 0, 3 against
0, 0, 1; estimate 2.9-5.1 MB/s with one re-measure and three ends of
discovery against 4.7-5.4 with none; lowerings of the estimate 4 against 2.
The bond carried 75-94% of what the mobile link alone did in the adjacent
runs, against 43%; the link itself delivered a third less, so the two
comparisons are not under equal conditions.

What did not work, or is new:

- **Repairs still arrive as duplicates**: 757 repairs on the mobile lane in
  three downloads, 760 duplicates at the edge, no sequence skipped. They are
  now sent when a stall ends, not during it, in groups of 70-150. Inferred
  from the code and their timing, not traced: what a stall held arrives at
  once, the first acknowledgement after it reports the newest lane sequence
  but can confirm 64 datagrams by lane and 256 by receipt, and every datagram
  it does not confirm counts as overtaken. This is item 5 again. A rule that
  waits for a second report before calling a datagram overtaken would cover
  it without a wire change; not built.
- **Stall recognition rarely applies**: 2 stall signals in three downloads,
  none on the uplink. The field's stalls are mostly not 100 ms silences of
  the acknowledgement stream.
- **A return to discovery left open at the end of a transfer was ended by
  the next transfer's first delay sample**, at the sender's rate: 1.16 MB/s
  became the 16 kB/s floor, and two uploads ran at 0.4 Mbit/s. The rule is
  as old as discovery; the ported probe settling made the recovery slower
  (next point). Fixed on branch `cold-estimate`
  (`TestSenderLimitedDiscoveryKeepsTheEstimate`).
- **Below about 170 kB/s a probe's win is never counted**: the next probe
  begins before thirty datagrams have confirmed the last and replaces it. The
  gain stays at a twentieth and the lane does not return to discovery. This
  came with `level-shift2`, and its policed-lane tests depend on it: with
  wins counted, a path that polices was returned to discovery and dropped a
  fifth of what it was offered. A lane this slow cannot tell a probe's loss
  from none in thirty datagrams; it needs a larger probe, not a faster count.
- **The delay threshold has no upper bound.** Traffic on the mobile link
  outside the tunnel (the direct uploads of this comparison) raised the idle
  forward variation of the uplink lane to 90 and 273 ms and its threshold to
  181 and 546 ms. The bond uploads that followed within two seconds ran
  without a delay signal, with 100-336 ms of queue, 282 and 830 datagrams
  expired and 14 and 65 sequences skipped at the concentrator. The estimate
  recovers within ten seconds of idleness. In production nothing but the
  tunnel uses that link; the comparison method must leave ten seconds after
  a direct transfer.

## After the deployment of `825b5ff` (2026-10-01, 21:31)

Operator's run, cold lanes: satellite link alone 0.49 / 0.49 Mbit/s; mobile
link alone 50.4 / 4.8 at 203 / 552 ms of loaded latency; bond 27.5 / 3.7 at
117 / 764 ms. Then three uploads through the bond, ten seconds apart, from
the state that gave 0.4 Mbit/s on `7351328` (the uplink lane returned to
discovery with 0.9 MB/s demonstrated): 3.1, 3.9, 4.5 Mbit/s, the estimate
kept, the threshold at 29 ms at most, mean queue delay 16 ms.

- **The bond's upload latency of 764 ms was the unbounded threshold**: the
  Speedtest on the mobile link ran immediately before and raised the uplink
  lane's threshold to 722 ms. Bounded at 100 ms on branch `threshold-bound`.
- **The downstream limit is no longer the lane control.** The mobile lane's
  estimate was 4.6 MB/s (37 Mbit/s on the wire) and its target above what it
  sent. Neither host is short of CPU: during a 23 Mbit/s download the edge's
  daemon used 0.47 of its four cores and the concentrator's 0.21. Inferred,
  not shown: TCP is the limit, answering the tunnel's queue drops (48 in that
  Speedtest) and the 250 ms reorder hold, where the mobile link alone gives
  it 200-500 ms of buffer and drops nothing. Raw: 50 Mbit/s at 203 ms of
  loaded latency; bond: 27.5 at 117 ms. This is items 1 and 6.
- **In a Speedtest most "repairs" on the lanes are copies of its own UDP
  probes** (about 20 a second each way, the real-time class): 874 on the
  uplink lane and 835 duplicates at the concentrator in one run. Repair and
  duplicate counts are comparable only between runs of the same tool.

## After the deployment of `2d2bd24` (2026-10-01, 22:29)

Operator's `wbtest` (Speedtest on the satellite link, on the mobile link,
then through the bond), twice:

| Run | Mobile alone, down / up, Mbit/s (latency, ms) | Bond, down / up, Mbit/s (latency, ms) |
|---|---|---|
| lanes cold, 30 s after the restart | 18.7 / 6.2 (109 / 555) | 1.2 / 5.9 (66 / 65) |
| two minutes later | 18.9 / 7.2 (96 / 920) | 22.9 / 5.3 (161 / 62) |

- **The threshold bound holds.** The uplink lane's threshold read 100 ms
  after the direct test where it read 722; the bond's loaded upload latency
  was 65 and 62 ms where it was 764, at 5.3-5.9 Mbit/s where it was 3.7.
  Upstream the bond carries 74-94% of the mobile link alone at a ninth to a
  fifteenth of its loaded latency.
- **A cold lane can end its first discovery low and stay there.** In the
  first run the concentrator's mobile lane held about 90 kB/s through the
  whole download (inferred from the edge's 1661 released datagrams in fifteen
  seconds; the concentrator was not captured during it). Probes below about
  170 kB/s were never counted won. Fixed on branch `cold-probes`
  (`TestProbeWinsCountOnASlowLane`); the first discovery still ends at the
  first delay signal once the lane limits the sender, so a low first
  estimate remains possible and now recovers in seconds.
- **In the second run the bond's download exceeded the mobile link alone**
  (22.9 against 18.9 Mbit/s) at 161 ms of loaded latency against 96: the
  mobile link was slow that minute, and the comparison is not a ceiling.
