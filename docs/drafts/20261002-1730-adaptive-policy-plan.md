# Adaptive policy plan — 2026-10-02

Original baseline: `main` = `f75668e`, deployed on the edge (`pi.mo`) and on `raspi5l`
(both daemons restarted 13:47, idle since). This plan follows
[the improvement plan](20261001-0820-wanbond-improvement-plan.md) and replaces
its item-by-item tuning as the way forward. Stage 0 measurement infrastructure
is partly implemented; stages 1–3 have not begun.

**Release checkpoint, 2026-10-04:** the operator approved committing and
tagging the tested C8 candidate for their own installation. Production code
is now `4a1cd54`, released as `v0.0.2`; the agent restored both production
hosts to their original deployed binaries. This approval is distinct from
the incomplete stage 0 proof and failed stage 1 gates. Stages 2–3 remain
unfinished; no scenario threshold is changed. See the
[release record](20261004-1105-adaptive-stage1-trial.md#operator-approved-c8-release--2026-10-04).

Provenance of each claim is marked: **observed** (run or read in this session),
**inferred** (from reading the code, not executed), **recorded** (taken from the
earlier plan or a commit message, not re-checked).

## 1. Review of the work since `14b4bf8`

| Commit | Change | Verdict |
|---|---|---|
| `480f94a` | The bind's flush reads the clock after it takes the transport | Correct. Observed: `go test ./internal/bind/` passes |
| `7b84ca5` | The resequencer lists buffered sequences in arrival order instead of scanning 32768 slots | Correct. Observed: equivalence test against a scan passes |
| `f75668e` | Design notes, runbook, plan | Docs only |

Observed on `main`: `go build ./...` succeeds, `go test ./internal/bond/
./internal/reseq/ ./internal/bind/` passes (80 s, 0.1 s, 4 s), `nix build`
succeeds. Not run: the web gate, `go vet` on everything, `gofmt`, the patched
dependency's tests, e2e, real hosts.

Residual defects in that work:

- **The precondition is documented, not enforced.** `bond.Transport` now says
  its owner gives each call a time no earlier than the last (`bond.go:314`).
  Nothing checks it; the defect it caused was invisible for a day. The
  transport should reject a regressing time through its error path.
- **A frame the transport rejects is still dropped without a count**
  (`bind/adaptive.go:178`). Same class of defect, still invisible. Already
  listed as open; it is a prerequisite here (stage 0).
- **`TestGapFillsBehindAnEarlyFrameDoNotScanTheWindow` asserts a wall-clock
  ratio** (20 times in-order). The measured ratio is about 1.4, so the margin
  is wide, but this host ran at a load average of 18-35 during the lab nights.
  Count ring-slot visits instead of timing them.

Loose ends of the lost session, **none merged, some not in git**:

| Where | What | State |
|---|---|---|
| branch `window-bound` (`a0ebec0`) | Window bounded against round trips measured under cross traffic | One field pair as candidate `w1`: uploads 3.7 and 6.6 Mbit/s against 7.9 and 6.1 deployed. Inconclusive |
| branch `cold-start` (`47806dc`) | A first discovery outlasts delay that is not the lane's limit | Model only |
| branch `lab-tooling` (`3a8695b`) | Lab links with buffer depth and varying rate, `cellular.json` | Needed by stage 0 |
| `/srv/nvme/tmp/wanbond-field/wip-*.patch` | Heavy loss acts without a backlog; duplicate bulk on unproven lanes; model lanes that police or lose rate | Uncommitted prototypes. Local scratch only |
| capture `prodfast-20261002-131024-cold-k1-a` | First download after a restart: 0.82 Mbit/s through the bond, 27.3 on the mobile link alone | Recorded in a WIP test comment: the policed satellite lane was sent 771 kB, 463 kB arrived, no loss signal |

Also observed today on the idle production pair: both downlink lanes of the
concentrator stand at the 16 kB/s floor target, still in first discovery, three
hours after the restart. A download therefore starts from the floor.

## 2. Why tuning does not converge

Every field run since 2026-10-01 found a regime in which a rule written for
another regime misfires, and each was answered with one more rule
(`control.go` was recorded as having 25 constants and the lane 90 fields).
Observed subsequently with Go's parser: `control.go` has 32 declared constant
names on `f75668e` and on the stage 0 branch, including 24 in its first block.
The recorded count of 25 is wrong; the full-file baseline for the required
reduction is 32 (`control-constant-baseline.jsonl` in the stage 0 evidence).
The common cause is
structural: **the lane's picture of its link is a set of values latched under
one condition and used under another.** Read in the code at `f75668e`:

| Latched value | When it is refreshed | Consequence (inferred unless marked) |
|---|---|---|
| Lane rank for real-time, `idleRTT + 2·idleRTTVariation` (`schedule.go:205`) | Only after 2 s without payload on the lane (`bond.go:1169`), or halfway by a floor test | The lane a call rides always has payload: its rank is frozen for the call. A latency rise on it is not followed (scenario 3) |
| Transit floor per size bucket (`bond.go:1091`) | Only downwards; upwards by a floor test, which needs bulk on the lane, or by the 10 s calibration, which needs a collapsed target | A latency rise reads as a queue. A voice-only lane has no bulk to pause, so no test |
| Capacity estimate (`control.capacity`) | Kept for ever while idle and across peer restarts; lowered 3% per repeated delay signal; raised only by pulses | A plan change in either direction meets a wrong estimate with full confidence |
| Pulses and the whole control interval | Only while the lane is backlogged (`bond.go:1264`), lane-limited and sending 75% of its target (`control.go:540`) | A lane below 1.2 Mbit/s that carries real-time traffic takes no bulk (`schedule.go:226`), so it never pulses and never learns it was upgraded (recorded: model lane held at 57 kB/s). A light sender leaves loss unanswered (recorded: the 0.82 Mbit/s download) |
| Unloaded variation, threshold, window | Only while idle; poisoned by traffic outside the tunnel | Recorded: threshold 722 ms, window 1.13 s of target. Each bounded by its own patch |
| `peakDelivery` (`bond.go:1205`) | Never lowered | Gates the baseline calibration against a peak the link may no longer have |
| `polices` | One minute after any material loss | A lane that stopped policing is treated as policing; one that starts is found by loss |
| Liveness: `stalled` at one RTO without an acknowledgement (`bond.go:816`), lease 1 s from the peer's hellos | A lane dead in one direction keeps its lease and stays schedulable (recorded, seen in a model run) | Scenario 1 depends on the RTO, itself an estimate from before the blackout |

The existing transport is already adaptive in the small (delay and loss
feedback per lane). What is missing is adaptivity of the *model*: no estimate
ages, and most are measured only in the state they are least needed in.

**Stage 0 finding, observed 2026-10-02.** The preliminary voice-only model
test of 3a passes its receive-latency gate on `f75668e` on both families.
The table's frozen-rank observation is inferred from code, but its claimed
receive-latency consequence is wrong in that case: existing real-time copies
can deliver on the better lane before the primary. The test is retained;
primary route changes require a separate observable check. Voice-only outage
passes are also observed: voice-only 1a on both lanes of both families; 1c
radio/gigaradio lane 0 direction 1 and gigaradio lane 1 in both directions.
The liveness mechanism alone does not establish caller-visible failure in
those cases. The full scenario and lab gates remain to be proved.

**Stage 0 finding, observed 2026-10-03.** After correcting the model's ACK
metadata to preserve SACK as the production classifier does, radio 2d passes
on `f75668e`: voice p99 is 136/141 ms in `[10,40)` seconds and both bulk
directions exceed 70% in `[20,40)` seconds
of the independent reference. All 52 model cases produced identical
measurements and verdicts in three repetitions. An inference that the old
model necessarily fails this radio grant pattern is wrong; the pass is
retained. Gigaradio 2d still fails. These observations do not prove the
unfinished lab or field gates.

Stage 0 subsequently stopped under the operator's lab/field disagreement
rule. The same voice-only 15-second mobile-egress outage produced zero lost
echoes in the field and 10/40 in the lab, with a longest lab gap of 880 ms.
Initial lane states and physical link behavior were not matched, so the cause
is unknown. The detailed [stage 0 record](../../test/vm/README.md#adaptive-policy-stage-0--2026-10-02-in-progress)
also records the incomplete calibration and gates. The operator subsequently
reported host CPU spikes to 100% during those measurements and directed that
the field be the behavioral reference (section 7). Stage 0 resumed with host
and guest scheduler observations; the earlier timing verdict is inconclusive.
Stages 1–3 have not begun.

**Stage 0 stopgap decisions, inferred from the retained evidence.** Leave
`window-bound` and `cold-start` unmerged for now. The measured field delay
rise collapsed targets and windows; it did not demonstrate the expanded-window
defect that would justify the window stopgap. Earlier `w1` throughput pairs
remain inconclusive. The cold-transfer model still fails its gate, but the
first-discovery special case has no measured field benefit here. Stages 2 and
3 remain responsible for replacing these mechanisms. This does not assert
that either stopgap is incorrect or waive their scenario gates.

**Stage 0 finding, observed 2026-10-03.** The corrected model 3a gate rejects
a fixture recovering at 2.5 seconds, which the old post-deadline measurement
accepted. With independent better-lane median references, the latency checks
pass on `f75668e` both with and without bulk, identically in three repetitions
on both families. Thus the table's caller-latency consequence is wrong in
these cases too. The bulk variants still fail because delivery falls by more
than a quarter for over two seconds. Neither observation proves that the
primary route moves; the tests retain those distinct outcomes.

**Stage 0 finding, observed 2026-10-03.** The scenario model had renewed
hello leases even through a dark incoming direction. After a failing
reproduction and that input correction, three repetitions on `f75668e`
give 1a five passes/three failures, 1b four failures, and 1c five
passes/eleven failures; the current stage 0 controller matches these results.
In particular, radio lane 0 with bulk passes 1a. The table's predicted
caller-visible failure is wrong for that case. Some earlier voice-only 1c
passes become failures and others become passes, so the newer evidence
supersedes those earlier outage verdicts without changing their gates.

**Stage 0 finding, observed 2026-10-04.** A separate failing reproduction
on `f75668e` exposed the TCP model forgetting retransmission history at a
timeout and accepting an ambiguous RTT sample. Removing RTT eligibility
when retransmitting corrects the model without changing production policy
or scenario thresholds. All 52 corrected-input baseline cases have identical
measurements and verdicts across three runs with that Karn-only correction. The outage verdicts above,
radio 2d pass and 3a latency findings remain; prior measurements retain their
input provenance. The [stage 1 trial record](20261004-1105-adaptive-stage1-trial.md)
records the reproduction and corrected candidate failures.

A subsequent pair of observed reproductions exposes previously SACKed data
being timed again on cumulative progress and fresh SACK timing being ignored.
Both models now share receipt-timing selection; no production rule or
acceptance threshold changes. The `c8` candidate still fails 1a–1c, with a
live gigaradio survivor's target below its independent required payload
goodput at the deadline. The explained failure stops that attempt; the code
is retained on `adaptive-stage1-c8-final` and `main` retains stage 0.
This establishes neither a field gain nor a field root cause; see the trial
record above for observations and the conditional bound.
The fresh-SACK-corrected baseline again produces identical measurements in
three runs of all 52 cases. Radio 2d now fails at 151 ms voice p99 with
both bulk bounds passing; this supersedes the older-input pass for current
acceptance. Outage verdicts remain 1a 5/3, 1b 0/4, and 1c 5/11. No gate
was weakened and the earlier observations retain their provenance.

**Stage 0 finding, observed 2026-10-04.** A further failing reproduction
shows the TCP models treating sequence distance as three selectively
received segments: one far-ahead receipt causes seven retransmissions.
Both models now use three distinct full-segment receipts, preserving the
positive loss case and excluding duplicate reports. Production policy and
scenario gates are unchanged. All 52 corrected-input baseline cases again
produce identical measurements and verdicts across three runs. Radio 1c,
lane 0, direction 0 with bulk now passes; section 2's predicted
caller-visible failure is wrong for that case too. The pass is retained.
Current baseline outage passes/failures are 1a 5/3, 1b 0/4, 1c 6/10;
radio 2d still fails at 151 ms. `c8` remains unaccepted: its three-run
outage results are 1a 5/3, 1b 1/3, 1c 9/7. Its live gigaradio survivor
still has a target below the required payload rate at the deadline.
The [trial record](20261004-1105-adaptive-stage1-trial.md) preserves the
older-input observations and the renewed failure bound.

## 3. Target: one link model per lane, continuously measured, with ages

A lane keeps a **link model**: a few estimates, each fed by every
acknowledgement whatever the load, each with the time of its last evidence.
Decisions (target, eligibility, rank) are functions of the model and of
demand. No estimate is trusted beyond its age; an old one is re-measured by a
probe, never assumed.

| Estimate | Estimator | Replaces |
|---|---|---|
| Transit floor | Lowest transit over a sliding window (about 10 s, BBR's RTprop), per size bucket, from all samples | All-time minimum, floor test, 10 s calibration, `floorMoved`, part of the stall rule |
| Path delay now | Smoothed transit of a lane while it queues nothing of its own (below its target and delivery keeping up), loaded or not | `idleRTT`, `idleRTTVariation`, `idleForwardVariation`, `wander`, the 2 s idleness condition, the threshold and window bounds |
| Capacity | Highest delivery over the last several round trips in which the lane was the limit; samples taken while the sender was the limit do not lower it but let it age | `capacity` with decay, re-measure, `raiseToDelivery`, `sustained`, plateau estimate |
| Loss character | Loss ratio over the ledger's horizon, kept as a number | `polices` and its one-minute memory, `droppedAt`, pulse back-off |
| Liveness | Time since the peer last reported progress on the lane against the cadence it reports at; three states: live, suspect, dead | `stalled`, the one-way-dead case, the silent-lane cut of 0.7 per interval |

Rules that follow from the model:

1. **Rank real-time by path delay now, with hysteresis.** A lane takes the call
   when its delay is lower by a margin (start with 10 ms or a quarter) for a
   dwell time (start with 1 s). The loaded round trip stays out of the rank, as
   today: the estimate counts only intervals in which the lane did not queue.
2. **Suspect before dead.** A lane with datagrams outstanding and no progress
   reported for twice the peer's acknowledgement cadence is suspect: real-time
   datagrams are copied to another lane without regard to the copy budget, bulk
   is assigned elsewhere, nothing is cut. Progress clears it. One RTO makes it
   dead; keepalives at 50 ms test its return, as today.
3. **An aged capacity estimate is probed, not trusted.** After an idle period or
   a peer restart the lane resumes at the estimate but re-enters probing at
   once. This is the cold start and the plan change in one mechanism.
4. **Probing does not depend on that lane's own backlog.** When bulk waits
   anywhere, every live lane probes upward on its schedule, including a lane
   bulk is kept off for a call. What a probe sends above the estimate is
   **redundant** (copies of bulk also sent on a proven lane), so a path that
   drops the excess costs the flow nothing. The WIP "unproven lane" prototype
   is this idea for the first discovery only. The copies travel on the lane
   being probed, so they cost data on that lane only. Redundancy is
   **conditional** (operator, 2026-10-02: acceptable if cheap): a probe is
   redundant only on a lane whose loss ratio says it drops rather than queues,
   or whose capacity estimate has aged out (cold start, plan change). A lane
   that queues its excess, as the mobile link does (recorded: 200-500 ms of
   buffer), is probed plainly at no extra data. See *Cost of redundant probes*.
5. **Loss acts whatever the backlog.** Material loss on a lane lowers its target
   whether or not the sender fills it.
6. **Every new estimator removes the rules it subsumes in the same change.** The
   constant count of `control.go` must fall. A change that only adds a rule is
   out of scope.

**What "probe" means here (clarified 2026-10-02).** No synthetic traffic is
sent to measure capacity. A probe is real traffic pushed above the estimate
for a bounded time, and only on demand: the trigger is the *tunnel's own*
queue growing (datagrams wait for a lane), the verdict is the *path's* delay
and loss. A fall in capacity shows passively. A rise cannot: a lane sending
at or below its capacity looks the same whatever the headroom, so something
must be sent above the estimate to find it. The push is short, not a
continuous increase, because a standing queue in the path delays voice, which
has no priority there (recorded: 40 ms standing in the model, 150-450 ms for
raw TCP on the mobile link). An idle lane is not pushed at all; its aged
estimate is re-tested by the first traffic that needs it.

Redundant copies in the pushed excess are a **fallback, not the default**:
adopt them only if stage 3 measures that pushes on a path that drops its
excess cost voice or TCP more than repair covers. Without them the cost
table below is zero on every lane.

Not decided here, to be settled by stage 0's measurements:

- Window lengths (floor 10 s, capacity 6-10 round trips) and the hysteresis
  figures.
- Whether hold-and-pulse stays as the probing schedule over the new estimators
  (preferred: smallest change) or gives way to a BBR-style gain cycle.

### Cost of redundant probes

Derived from the constants of `control.go`, not measured. A pulse raises the
target to 110% of the estimate for 15 times the delay threshold, 150-500 ms,
about once every 1-1.7 s while bulk waits. The redundant part is the tenth
above the estimate: 1-5% of what the lane carries during a saturating
transfer, and nothing otherwise.

| Lane | Per pulse | Per minute of saturating transfer |
|---|---|---|
| Mobile, 50 Mbit/s, if redundancy were unconditional | 94-313 kB | 4-14 MB beside 375 MB carried |
| Mobile, conditional (the rule above) | 0 in steady state | about 0.3-0.5 MB per cold or aged discovery |
| Satellite standby, 0.5 Mbit/s policed | 1-3 kB | 40-140 kB |

For scale, the copies of two voice streams already cost 30 kB/s on the other
lane (recorded in `docs/design.md`): 108 MB per hour of call. Stage 3 must
confirm these figures from the lane counters before the rule is kept.

The wire format is not expected to change. If a stage needs a field, it
becomes a versioned acknowledgement with a rollout order, as item 5 of the
earlier plan says.

## 4. Scenarios and acceptance criteria

The voice and bulk figures were confirmed by the operator on 2026-10-02; the
video row is a proposal. Each is checked in the
deterministic model, in the lab, and where affordable on the production pair.

**Operator amendment, 2026-10-02.** In scenarios carrying voice, a capacity
percentage applies to the available TCP payload goodput after the required
voice traffic and protocol overhead, not to the total link wire rate. The
reference is independent of the candidate's target, capacity estimate, losses,
repairs and redundant copies: use a deterministic wire budget in the model
and matched direct-link calibration in the lab, accounting for encapsulation,
feedback and the reverse TCP ACK stream. Record the reference for each
direction and phase before evaluating a candidate; use the same reference
for the baseline and candidate. A calibration that fails its own capacity
gate makes the throughput verdict inconclusive. Voice gates, progress rules
and adaptation deadlines are unchanged. This resolves the
[radio survivor budget conflict](../../debug/20261002-180053-adaptive-budget.md).

| # | Scenario | Voice (two 50 Hz streams) | Bulk (one TCP flow each way) |
|---|---|---|---|
| 1a | One link goes dark both ways for 15 s (tunnel), either link, with and without bulk | No receive gap of 150 ms; at most 3 consecutive datagrams lost; under 1% lost over the run; p99 round trip within the survivor's idle p99 + 50 ms from 1 s after | Delivery in every second after the first; 75% of the survivor's available TCP goodput within 3 s |
| 1b | The link returns | No gap, no latency rise above the gate | Lane carries bulk within 2 s; 75% of the pair's available TCP goodput within 5 s |
| 1c | One direction of one link goes dark | As 1a | As 1a |
| 2a | A link falls to 20-50% of its rate (deep buffer and shallow) | p99 round trip under 150 ms through the change | 75% of the new available TCP goodput within 5 s; no burst of expired datagrams |
| 2b | A link rises by 5 times or more | Unchanged latency | 75% of the new available TCP goodput within 10 s |
| 2c | Plan change on the low-latency link, 0.5 Mbit/s policed to 100/15 and back, while a call rides it | Call stays on the lowest-latency lane; p99 under 150 ms | Up: 75% of the pair's available TCP goodput within 20 s. Down: loss on that lane under 5% after 3 s |
| 2d | Rate varying ±60% every 100 ms (`cellular.json`) | p99 under 150 ms | 70% of the mean available TCP goodput |
| 3a | The lane a call rides gains 100 ms; the other is lower | Median round trip within 20 ms of the better lane within 2 s; voice-only and with bulk | Not cut by more than a quarter for more than 2 s |
| 3b | The other lane becomes the lower by 20 ms or more | Call moves within 5 s; at most one move per 5 s under jitter | Unchanged |
| 3c | Both lanes gain latency | No loss, no gap | As 3a |
| 4 | A video call (stretch goal): voice plus one 1.5 Mbit/s stream of 1200-byte UDP datagrams each way, through 1a and 3a | Voice as above | Video: not held for resequencing; no receive gap of 300 ms; under 2% lost; TCP bulk beside it keeps 75% of what is left |
| 0 | Cold: first transfer 30 s after both daemons start | — | 60% of the pair within 7 s (the night's goal, unmet at 42%) |

Existing gates (`continuity.py`, `benchmark.py`, `udp.py` on `radio` and
`gigaradio`) must not regress.

## 5. Stages

Each stage: failing model test first, then the change, then the lab on both
profile families interleaved with the baseline, then the field. Code and docs
in separate commits; `docs/design.md` updated with each behaviour change.

**Operator amendment, 2026-10-04.** An unsatisfactory lab result does not
veto a bounded temporary field trial. The field remains the behavioral
reference. Record the candidate's model and lab failures before testing it;
a field trial does not establish that those gates pass. This changes trial
eligibility, not the fixed acceptance gates, stage order, restoration timers,
management precautions or restriction against permanent deployment.

**Stage 0 — measure, no behaviour change.**

1. Bring the loose ends into git: merge `lab-tooling`; commit the WIP model
   extensions (policed lane, falling rate) as model infrastructure; decide
   `window-bound` and `cold-start` (see 5 below).
2. Count rejected frames by cause; make the transport reject a regressing
   time. Export the model's inputs per lane (floor, path delay, rank, liveness
   state) on `/metrics` and in `wanbond monitor`.
3. Model: give `modelLane` a rate, a delay and a dark interval as functions of
   time, per direction. One model test per row of section 4, expected to fail
   on `f75668e` where section 2 says so. A test that passes is a finding:
   section 2 is then wrong on that point.
4. Lab: `debug/20261002-171000-adapt-scenarios.py` drives the scenarios
   (`blackout`, `rate`, `upgrade`, `latency`, with voice-only variants) and
   records both guests at 10 Hz. **It has not completed a run**: one run got
   through all four changes and failed while collecting; that defect is
   corrected, the result not re-checked. Move it to `test/vm/adapt.py` with
   gates from section 4 once it works. Run each scenario three times on
   `f75668e` and record the baseline in `test/vm/README.md`.
5. Field baseline on the production pair, voice-only first (kilobytes):
   blackout and added delay by `tc` on the edge's WAN VLANs
   (`end0.231` satellite, `end0.232` mobile) with a `systemd-run` timer that
   removes the qdisc, as `candidate.sh` does for binaries. **Before the first
   blackout, establish which WAN the management address `192.168.222.15` is
   reached over**; observed only that `raspi5l` routes to it by its LAN
   gateway, not through `wanbond0`. Throughput scenarios in the field use rate
   caps well below the link's rate to save data (the earlier measurements used
   1.4 GB).

**Stage 1 — liveness and failover (scenario 1).** Liveness as a three-state
estimate; suspect copies real-time and diverts bulk; one-way death detected
from the peer's progress reports, not from the lease. Removes `stalled`, the
0.7 silent cut, and settles item 7 of the earlier plan by making the
continuity gates concrete. Smallest stage, most visible to a caller.

**Stage 2 — delay model and ranking (scenario 3).** Windowed floor and
path-delay-now from all samples; rank with hysteresis; threshold and window
derived from the same estimates. Removes the idle-only estimators, the wander
estimator, the floor test, the calibration, and the two bounds patched in on
2026-10-01/02. Makes `window-bound` unnecessary if it works; if stage 0 shows
the window defect costs the field now, merge `a0ebec0` first as a stopgap.

**Stage 3 — capacity model (scenarios 2 and 0).** Windowed capacity with age;
probing independent of the lane's backlog; redundant probes; loss acting
without a backlog. Removes decay, re-measure, `raiseToDelivery`, `sustained`,
the plateau bound, `polices`, and the cold-start special cases. Largest stage;
`cold-start` (`47806dc`) and the heavy-loss WIP are stopgaps it replaces.

**Stage 4 — real-time by flow, not by size (scenario 4, stretch).** Operator,
2026-10-02: video calls are in scope "ideally"; this stage must not delay
stages 1-3. Today a datagram over 384 bytes is bulk: resequenced with a hold
of up to 250 ms, subject to CoDel, never prioritised. A large-datagram
real-time class is delivered immediately like the small one (the delivery
sequence's small-packet bit already selects that path; invariant 3 and its
documentation change with it), is served after voice and before TCP, and
rides the lowest-latency lane with room for it. It is **not copied** in steady
state: copies of a 1.5 Mbit/s stream would cost about 0.7 GB per hour. It is
copied only while its lane is suspect (stage 1). Open design question: telling
a video call from a QUIC download, both large UDP. Candidates: the inner
DSCP mark, and a flow that is steady in every 200 ms and stays under a few
Mbit/s. Decide from captures of real calls in stage 0.

**Stage 5 — field.** Candidates through `candidate.sh` (restore timer, both
ends); binaries gzip-compressed for the edge. Cold round, warm round, then
the section 4 scenarios on the real links. Deploy when the lab gates and the
field rounds agree.

Order 1, 2, 3 is by size and risk, not by the scenario numbering: stage 2's
delay model is also what stage 3's congestion signal reads.

## 6. Risks

- **Rank oscillation.** A call that moves between lanes reorders and may gap.
  Hysteresis figures need the jittery profiles, not the clean ones.
- **A windowed floor follows a standing queue upward** once the window holds
  no unqueued sample. The control holds below capacity, so the queue drains
  between probes; this must be shown on the deep-buffer profile, and is the
  reason BBR has ProbeRTT. If it fails, a short periodic drain replaces the
  floor test, on a schedule and not on a signal.
- **Redundant probes cost metered data.** Quantify per stage 3 before choosing.
- **The model tests encode today's rules.** About fifty tests in
  `internal/bond` assert behaviours by mechanism (floor test, pulse wins). Each
  stage must restate the ones whose mechanism it removes as outcome tests
  first, in a separate commit, so that their passing means something.
- **The lab does not reproduce the field** (no stalls, per-datagram jitter).
  Stage 0's field baseline is the reference; lab passes alone do not justify
  a deployment.
- **`Poll` walks every attempt of the last two seconds** (recorded: 36% of the
  concentrator's CPU at 5 MB/s). Continuous estimation adds work per
  acknowledgement; fix the walk before stage 3 or the estimators will be
  blamed for it.

## 7. Operator decisions (2026-10-02) and what stays open

1. The gates of section 4 stand, with the approved available-goodput amendment
   above. It preserves the voice gates and all adaptation deadlines.
2. Probing uses real traffic pushed on demand; agreed. No synthetic probe
   traffic. Redundant copies in the pushed excess stay a fallback, adopted
   only if stage 3 measures a need, in the conditional form of rule 4.
3. Video calls: in scope as a stretch goal (stage 4).
4. Field is the behavioral reference. The operator reports that the lab host
   repeatedly reached 100% CPU during earlier measurements, although current
   load is 9%. That is operator evidence, not a historical CPU trace. Treat
   those lab timing verdicts as inconclusive and resume measurement with
   host/guest wake delays, CPU/steal counters and daemon scheduler records.
   Current load cannot establish the cause of an earlier failure. This does
   not waive the numeric gates or the required three-run lab series. The
   field links are 5G and Starlink and may vary widely (operator evidence):
   retain their contemporaneous conditions and interleave baseline/candidate
   rounds; a single field RTT or rate is not a stationary reference.
5. Management safety: the operator confirms ZeroTier access survives a
   blackout of either single wanbond VLAN (`end0.231` or `end0.232`). This is
   operator evidence for one-VLAN tests. Observe the current Linux reply
   route separately and verify an automatic removal timer before each change.
6. For variable field conditions, measure the direct uplinks immediately
   before the tunnel, following the ordering of the edge's `/home/pavel/wbtest`.
   Observed script order is Starlink, 5G, tunnel. Use bounded measurements
   and retain their spread; the operator cannot currently supply fixed
   numerical meanings for the qualitative gates. Those comparisons must
   expose uncertainty rather than inventing a tolerance. The existing
   numerical gates and deadlines remain unchanged.
7. Temporary field trials may proceed despite unsatisfactory lab tests
   (operator, 2026-10-04). A lab failure remains evidence to investigate;
   host contention and physical-link behavior must be distinguished rather
   than inferred from the overall verdict. Preserve the field trial's
   candidate revision, outstanding failures and bounded workload.
8. After the parked comparisons and their own `wbtest`, the operator requests
   committing and release-tagging the tested C8 candidate for installation
   (2026-10-04, 20:19 UTC). The agent restores the hosts; the operator handles
   installation. Preserve failed gates and measured limits. This release
   decision does not assert completion of stages 0–3.

Open: the video gates of row 4 (300 ms gap, 2% loss) are a proposal; and how a
video call is told from a QUIC download.

## 8. Research follow-up — 2026-10-04

This section proposes experiments, not an implemented policy or measured
improvement. The deployed policy is unchanged. The
[rejected stage 1 checkpoint](20261003-2245-adaptive-stage1-checkpoint.md)
retains the observed deterministic failures. The rejected stage 1 replacement
has never been tested in the field. Stage 0 field measurements are baseline
evidence; section 1 separately records the earlier
inconclusive `window-bound` prototype's field pair.

**Observed in code:** ranking still uses idle RTT and variation; lane choice
adds pacing wait to half that rank. It does not predict a datagram's
serialization or bulk-stream completion. The survivor's legacy voice rule
can cap bulk at 5% of its target. **Observed in the rejected attempt:** that
cap is below the independent required goodput in the recorded radio
direction; removing it regresses voice and does not restore TCP progress.
**Inference:** estimator replacement alone is insufficient. Allocation,
pacing and ordered delivery need to agree with the same lane model.

### Relevant primary sources

| Approach | What the source establishes | Implication for wanbond (inferred) |
|---|---|---|
| [BBRv3, IETF draft -06](https://datatracker.ietf.org/doc/html/draft-ietf-ccwg-bbr-06) | A transport-independent delivery sampler bounds the ACK rate by the corresponding send rate and identifies application-limited samples. Bandwidth and inflight models drive pacing. Section 3.8 explicitly warns that persistently application-limited audio/video can retain old bandwidth maxima. This is an experimental work in progress. | Use its sampling discipline, not an unmodified controller. A voice-only lane still needs capacity age and demand-triggered remeasurement. |
| [Earliest Completion First](https://api.repository.cam.ac.uk/server/api/core/bitstreams/3ec47f93-4360-4630-bd4a-9e1ed23605fa/content) | The MPTCP scheduler considers RTT, subflow capacity and queued work; waiting for the faster path can complete a transfer sooner than immediately using a slower one. The paper measures improved utilization under heterogeneous paths. | Add completion-aware bulk scheduling, including the receiver's ordering constraint. These MPTCP results do not prove a gain for wanbond. |
| [QUIC recovery, RFC 9002](https://datatracker.ietf.org/doc/html/rfc9002#section-6.2) | Probe-timeout expiry does not by itself establish packet loss. RTT variation and ACK delay enter recovery timing. | Preserve the separation between suspect liveness, capacity and confirmed loss. This supports removing the silent capacity cut; it does not prescribe wanbond's liveness thresholds. |
| [SCReAMv2, IETF draft -01](https://datatracker.ietf.org/doc/html/draft-ietf-ccwg-rfc8298bis-screamv2-01) | Combines pacing, delay/loss feedback and an inflight reference for multimedia, including variable mobile access. Its discussion acknowledges scheduling jitter, reverse-feedback congestion and policer difficulties on satellite links. It is an experimental work in progress. | A useful comparison for latency control, but its media-rate control cannot regulate an opaque encrypted TCP/UDP workload. A full adoption needs a separate feedback compatibility audit. |

[A 2026 Starlink TCP study](https://arxiv.org/html/2607.07133v1) reports a
favorable throughput/delay/loss tradeoff for BBRv3. Its setup has one
Australian Starlink terminal and six destination cities, not six independent
Starlink access sites. **Document evidence:** this supports testing a
model-based controller; it does not establish performance on this production
pair, for UDP bonding or during voice failover. Enabling kernel TCP BBR alone
would not control wanbond's outer UDP sender (inferred from the transport).

[Starlink queue measurements](https://arxiv.org/html/2605.27717v1) report
drop-front rather than drop-tail buffering and no per-flow fair queueing at
two terminals. The authors also note that configuration may change and do
not establish a causal explanation for TCP underutilization. **Inference:**
retain drop-tail and policer fixtures, but add drop-front and burst/grant
variants when validating the replacement. These observations do not
establish this field link's queue configuration.

### Recommended implementation experiments

1. **Stage 1:** retain ACK-progress liveness and its removal of the silent
   capacity cut, but test voice reservation and copy pacing as part of the
   same lane wire budget. A copy or repair consumes real capacity. Bulk may
   use measured residual service; it must not inherit a fixed 5% cap merely
   because voice occupies the only survivor. Packet serialization still
   limits voice latency, so removing the cap alone is already a rejected
   experiment, not the proposed correction.
2. **Stage 2:** keep separate aged estimates for transit floor and current
   delay. A sliding minimum must not silently turn a standing queue into
   propagation delay; test the plan's conditional drain before removing the
   floor-test mechanism. Rank from fresh evidence with hysteresis. Use
   completion estimates for bulk so the slower lane does not stall ordered
   delivery on the faster one. Avoid counting pacing wait twice as backlog.
3. **Stage 3:** sample corresponding send and receive flights, mark whether
   traffic was limited by application demand, scheduling or the path, and
   expire unsupported capacity confidence. An ACK with no new measurement
   must not refresh an estimate's evidence age. Trigger bounded upward pushes
   of queued real data from tunnel demand, including lanes kept off bulk for
   voice; retain the operator's conditional redundancy fallback. Confirm loss
   lowers the target even during a light workload.

**Wire compatibility, inferred from code:** ACK v1 already carries physical
lane receipts, cumulative received wire bytes, receiver elapsed time and ACK
delay; local attempts carry send time and wire size. These are sufficient
inputs to prototype a sender-side delivery sampler and relative transit
model without adding a field. This is not proof of an exact BBRv3 or
SCReAMv2 implementation. In particular, elapsed time anchors the highest
lane sequence whereas the byte count includes later-arriving older
attempts: sampling intervals must be aligned and tested under reordering.
Tests must also cover compressed ACKs, sparse feedback, receipt-bitmap
turnover and copies; global delivery alone cannot identify which physical
attempt arrived. Independent lane clock origins also prevent treating their
relative transit values as absolute one-way delays. Use RTT evidence for cross-lane timing
and retain the uncertainty; stop if the implementation requires new feedback.

For each experiment, reproduce its claimed defect first and remove the rules
it replaces in the same code change. Keep the required outcome-test
restatements separate. Do not combine stages to obtain a passing verdict.

### Field comparison

**Operator evidence, 2026-10-04:** current Starlink standby is capped at
0.5 Mbit/s symmetric; 5G at 100 Mbit/s down and 10 Mbit/s up. These maxima
do not establish sustainable service under current RF conditions. The
retained radio fixture (0.5/0.4 plus 100/1.25) and built-in `FIELD` fixture
(0.5/0.5 plus 50/10) do not reproduce those caps. Gigaradio remains a
300+300 Mbit/s stress requirement; its goodput bound is not a field bound.
Retain those historical conditions and measure the current direct links
before deriving field goodput references. This changes no scenario gate.

Run voice first, using temporary candidates with verified restoration and
single-WAN removal timers. Interleave baseline and candidate rounds; measure
Starlink, 5G and tunnel in that order immediately before each comparison.
Keep both directions and their contemporaneous variability. A rate-capped
measurement which reaches its offered ceiling is a capacity lower bound,
not an aggregation or full-capacity result.

Observe application-delivered TCP bytes and voice losses/gaps/latency, along
with physical lane receipt progress, ACK intervals, inflight wire bytes,
queue age, repairs, copies and actual metered MB. Where cap or RF variability
prevents a comparison, retain the uncertainty. Use captured behavior to
challenge model assumptions; neither a poor lab result nor a favorable
field round is sufficient to declare stages 1–3 proved.

### Approved next execution — 2026-10-04

**Operator instruction:** proceed with the following strategy as the goal.
Retain stages 0–3, their order and all section 4 gates; video and permanent
deployment remain excluded. The earlier goal is unfinished. The goal API
refused replacement and exposes no resume operation for its paused entry;
this approval authorizes the work, not a claim that the earlier goal passed.

1. Add a separate fixture with the current field caps. Keep radio and
   gigaradio unchanged. Delays, queue depth and policing in the new fixture
   are stated modeling assumptions until checked against current field data.
2. Compare deployed → candidate → deployed with matched startup age. Measure
   direct Starlink, direct 5G and tunnel immediately before each round. Run
   voice-only single-WAN outages first, then rate-capped TCP high enough to
   expose the survivor's residual service; record delivered bytes and MB.
3. Isolate stage 1's outstanding capacity bottleneck with a minimal model
   reproduction. Determine whether a correction belongs to liveness/pacing,
   or demonstrate its dependency on a later estimator. Do not combine stages
   to obtain a passing verdict.
4. Continue estimator replacement in stages 2 and 3 only in the approved
   order, with separate outcome-test commits and removal of replaced rules.
   Complete the lab series, regression gates, documentation and Nix build
   before claiming the implementation complete.

At that checkpoint the next deliverable was the matching field comparison
and a reproducible account of the next bottleneck. Existing failed gates
remain outstanding; section 9 gives the execution priorities after release.

**Observed checkpoint, parked field resumption, 2026-10-04:** three
deployed → `c8` → deployed comparisons complete with matched startup ages
and fresh Starlink/5G/tunnel references. Under the bounded 24 kB/s-per-direction
workload and 15-second mobile-egress blackout, candidate uplink delivery is
15.3–15.9 kB/s versus deployed rounds' 1.4–9.3. Loss/gap checks pass, but
candidate voice p99 reaches 131–144 ms in two rounds and downlink results
are mixed. **Inference:** an uplink gain for this workload is established;
an overall policy improvement and full section 4 verdict are not. Independent
survivor idle/residual-goodput references remain unmeasured. Deployed binaries
and cleanup were verified afterwards; the mobile VLAN interval increased
100.24 MB including background traffic. The unchanged model failures still
reject the retained stage 1 attempt. The [trial record](20261004-1105-adaptive-stage1-trial.md#parked-field-resumption--2026-10-04)
keeps all nine rounds and their limitations; no gate or stage order changes.

## 9. Further improvements after the C8 release — 2026-10-04

### Current checkpoint and evidence

**Observed:** `v0.0.2` contains the released C8 policy (`4a1cd54` plus
documentation). The following monitoring feature is implemented in
`ab4c8a1`: both monitor views expose the running daemon's source commit and
UTC commit time. **Operator decision:** use reproducible source commit time,
not compilation time. Go VCS metadata and explicit Nix stamps preserve dirty
markers; unavailable identity is displayed as unknown. Retain the executable
hash and any dirty patch alongside the commit. This feature adds diagnostics,
not an estimator or transport policy. The existing release tag precedes it.

**Recorded field evidence:** three matched deployed → C8 → deployed sets
establish the bounded outage uplink gain, with mixed downlink results and
voice RTT p99 reaching 131–144 ms in two candidate rounds. **Operator
evidence:** the subsequent sequential Speedtest reports 66.19 Mbit/s down
and 0.33 up through C8; direct 5G reports 44.58/2.42 at a different server.
These observations do not isolate an aggregation gain or the upload cause.
The supplied monitor records 2,633 expirations and 1,053 5G repairs at the
end, but cannot assign them to the download or upload phase. The
[release record](20261004-1105-adaptive-stage1-trial.md#operator-approved-c8-release--2026-10-04)
preserves measurements, restoration and mobile accounting.

**Recorded model evidence:** C8 still fails 1a–1c gates. The gigaradio
survivor's legacy target/estimate is already too low before the blackout;
ACK-progress liveness alone cannot establish the required goodput. This is
an explained failure for that model, not a demonstrated field upload cause.
The full non-privileged gate and tagged source vet checks pass after the
monitor feature; neither substitutes for the incomplete adaptive lab series.

### Next deliverable: reproduce and explain the upload limit

1. Observe both running executables before testing; do not infer installation
   from a tag or operator intent. Record daemon commit/time, hash, selected
   exit, uptime and lane states. If identity is dirty or unknown, retain
   source/patch provenance. Keep the original `f75668e` evidence and compare
   against the actual installed C8 baseline when it is observed installed.
2. Use the same controlled destination for direct Starlink, direct 5G and
   tunnel measurements, immediately before each round. Match startup ages
   and interleave baseline → candidate → baseline. Begin with voice-only;
   then bounded upload alone, download alone and bidirectional TCP. Preserve
   configured caps separately from measured RF service. A 1 Mbit/s offered
   ceiling can expose the reported 0.33 Mbit/s upload without an uncapped
   Speedtest; reaching that ceiling gives a lower bound, not capacity proof.
   Record a byte budget and mobile RX+TX MB for every interval.
3. Capture receiver bytes, actual TCP start times, sender backpressure and
   retransmissions, voice send/receive stamps, and per-lane progress, target,
   delivery, inflight, queue, repairs, expirations and rejected ACK counters.
   Use phase-aligned samples: final smoothed metrics cannot explain a whole
   transfer. Independently calibrate survivor idle voice latency and residual
   TCP goodput before making the section 4 percentage or idle-plus-50-ms claim.
4. Turn the observed limit into a minimal deterministic reproduction that
   fails for its actual cause before changing policy. Compare original and
   C8 outcomes to distinguish an existing defect from a regression. Check
   whether the limit is the pacing/window/class budget, reverse feedback,
   repair/expiry history or the link estimates. These are hypotheses, not
   findings. Preserve model corrections separately from production fixes;
   a model pass is not proof that it reproduces Linux TCP or the RF link.

**Delivery criterion:** one phase-aligned field reproduction, the matching
model failure and a causal account consistent with both. If they disagree,
report the disagreement and input/scheduler uncertainty before proceeding.
Do not infer a historical CPU cause from current host load. No new WAN
impairment is needed to investigate a healthy two-link upload; any later
single-WAN impairment retains management checks and verified removal timers.

### Implementation and proof remain in stages 1, 2, 3

| Stage | Remaining change | Required proof and removal |
|---|---|---|
| 1 — liveness/pacing | Resolve reproduced class/feedback/repair defects within the current stage, then prove 1a–1c. Released C8 already provides ACK-progress states and shared pacing. | Restate any remaining mechanism assertion as an outcome in a separate commit. Preserve loss/gap/deadline and returning-lane receipt checks. If a failed gate requires the later delay/capacity model, stop and report that dependency rather than adding a capacity heuristic here. |
| 2 — delay/rank | Sliding transit floor with age and fresh path-delay evidence from all eligible ACK samples; hysteresis for voice ranking and coherent threshold/window inputs. | Prove 3a–3c, including primary moves rather than faster-copy delivery and protection against following a standing queue upward. Remove idle-only estimates, wander, floor tests, calibration and superseded bounds in the same estimator change. |
| 3 — capacity/loss | Align corresponding send/receive flights, identify application/scheduler/path limits, age capacity confidence and keep loss as a measured ratio. Demand in the tunnel triggers bounded upward pushes of queued real traffic. | Prove 2a–2d and cold 0. Remove decay, remeasurement, delivery-raise/sustained/plateau rules, policing memory and cold-start special cases. Redundant pushed copies remain a measured fallback, with their MB cost reported. |

Do not fold later estimators into stage 1 to obtain a pass or mark a released
candidate as an accepted stage. `control.go` is now at 31 constant names,
against the original 32; every further estimator must remove its replaced
rules and reduce that inventory. Original passing-model findings remain
findings; do not bend them into failures. ACK v1 supplies the existing local
prototype inputs, but any required new feedback field still stops work for
a versioned ACK and rollout design.

The final acceptance remains all section 4 cases, three runs on each lab
family with contemporaneous calibration and scheduler observations, and
continuity/benchmark/UDP regression checks on radio and gigaradio. Field is
the behavioral reference; an explained failed gate or disagreement still
requires a report. Qualitative unchanged-latency/bulk comparisons retain
paired references and uncertainty rather than an invented tolerance.
Code/docs commits stay separate; affected design/runbook docs, the full
non-privileged gate and `nix build` remain part of completion. Stage 4 video
and agent-managed permanent deployment remain outside this goal.
