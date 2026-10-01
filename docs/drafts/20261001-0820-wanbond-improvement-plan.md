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
- **The monitor UI and TUI show no lane state.** The `wanbond_adaptive_*`
  series exist on `/metrics` only, and have no test in `internal/metrics`.
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
