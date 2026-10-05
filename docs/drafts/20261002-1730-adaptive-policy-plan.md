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
9. The operator selects current main `b444920`, including build identity, as
   the new operational baseline (2026-10-04). Preserve `f75668e` as the
   original reproduction reference. This does not waive the section 4 gates
   or accept an unfinished stage.
10. The operator replaces the failed-gate sequencing stop with an improvement
    goal: continue advancing until the metrics improve over the installed
    baseline (2026-10-04). Stages 2 and 3 may proceed with stage 1 gates still
    outstanding. See section 11; no numeric gate is waived.

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

## 10. Installed baseline and upload investigation — 2026-10-04

### Installation and bounded field observations

**Observed:** both hosts run `b444920c53f689393ea084c349ca496fbd7abd42`,
source time `2026-10-04T21:13:41Z`, executable SHA-256
`dce5c7e4dae9a13565e04c69aa2ac36a6ab388a51418282a027e14e18f4e5dd9`.
Installation was observed during the operator's deployment; the later
completed rounds preserve edge PID 124929 and concentrator PID 468806.
The exit is pinned to `raspi5l`. No candidate replacement, daemon restart,
WAN impairment or Nix deployment change was made in this investigation.

**Observed:** voice-only preflights precede bounded TCP transfers to the same
OCI endpoint, `89.168.124.91`. Direct sockets are bound to the WAN device;
tunnel traffic uses `wanbond0` through `raspi5l`. Receivers record actual
payload arrivals; upload size and digest and download size are checked.
Rates below are payload Mbit/s over the receiver interval, including stalls.
SSH transport is used without compression; these are not Ookla measurements.

| Sequence | Offered rate / payload per transfer | Direct references and tunnel result |
|---|---|---|
| Starlink up/down → 5G up/down → tunnel up/down | 1 Mbit/s / 1.25 MB each | Starlink 0.522/0.534, 5G 0.995/1.001, tunnel 1.001/1.001 |
| Starlink upload → 5G upload → tunnel upload → 5G upload | Starlink 1 Mbit/s / 1.25 MB; others 3 Mbit/s / 3.75 MB | Starlink 0.513; 5G before/after 2.989/2.993; tunnel 3.001 |
| Starlink upload → 5G upload → tunnel download → tunnel upload → 5G upload | Same upload caps; download 10 Mbit/s / 12.5 MB | Starlink 0.523; 5G before/after 3.000/2.989; tunnel down/up 9.996/2.999 |

**Inference:** the reported 0.33 Mbit/s upload limit is not reproduced in
these bounded workloads, including after the 10 Mbit/s download. Reaching
an offered ceiling establishes that service lower bound, not link capacity,
aggregation, a policy gain or behavior after a 66 Mbit/s download. RF
conditions, TCP implementation, destination and startup history differ from
the operator's Speedtest. There is no justified production upload fix yet.
These are baseline rounds, not baseline/candidate comparisons. Warm transport
state is retained. No independent survivor idle calibration or loaded-voice
comparison was made, so the section 4 idle-plus-50-ms gates are not claimed.

**Observed accounting:** completed collection intervals plus one failed setup
advance mobile VLAN RX+TX by 53.504 MB. The first interval omits its initial
setup, and a separate space-failure interval lacks a complete counter pair.
The continuous 21:21:40–21:59:46 UTC counter interval is 217.118 MB, including
operator deployment, management if routed over that VLAN, and background
traffic. Nested reference intervals must not be added to collector intervals;
neither figure measures provider billing or test-only mobile usage.

Collection defects are retained separately: embedded-NUL cleanup failed after
the first payload sequence (bounded jobs expired and logs were recovered);
a helper incorrectly restored `auto` after rejecting an operator-pinned
exit (restored to `raspi5l`, then changed to preserve the observed policy);
and `/run` exhaustion interrupted another setup before TCP payload. Subsequent
collectors use disk-backed `/var/tmp`, persist setup counters immediately and
attempt cleanup on both hosts before reporting cleanup errors. Failed setup
runs carry no policy verdict.

At the operator's request, 62 obsolete test directories under `/run` on the
edge are archived with verified file hashes to
`/var/tmp/wanbond-run-archive-20261004-221448.tar.gz` (50.979 MB compressed),
then removed: 453.339 MB of files. **Observed:** no process or active timer
references those paths; daemon PID, executable hash and runtime config hash
are identical before/after. `/run/wanbond/edge.toml` is retained and `/run`
usage falls to 19.644 MB (4%). This cleanup interval advances mobile RX+TX by
0.058 MB including background traffic; no archive is transferred over mobile.

### Model findings and the remaining stage dependency

**Built:** `2395c92` adds independent upload/download selection to the existing
TCP model and four healthy standby cases (each direction, with/without voice).
The inactive direction offers no bulk but still carries genuine reverse TCP
ACKs and transport feedback; its independent reference likewise reserves
that reverse demand. No production policy or constant changes.

**Observed:** all four cases pass with identical measurements in three runs
on both `f75668e` and C8. They are findings, not newly fixed failures. The
fixture uses the documented 0.5 Mbit/s symmetric Starlink and 100 down/10 up
5G caps, fixed propagation delays and no jitter/loss. It is not an RF trace
or a Linux TCP reproduction.

| Healthy case, last five seconds | Original payload B/s | C8 payload B/s | Independent reference B/s |
|---|---:|---:|---:|
| Download only | 9,692,640 | 9,971,280 | 6,412,847 |
| Download with voice | 9,908,640 | 9,954,720 | 6,231,040 |
| Upload only | 1,033,200 | 1,012,560 | 1,091,646 |
| Upload with voice | 985,680 | 987,600 | 1,060,697 |

The reference is the existing conservative model wire-budget calculation,
not a measured field ceiling. Passing both revisions does not establish a C8
gain or reproduce the operator's upload observation.

**Observed:** all 52 section 4 model cases now have three completed repetitions
with identical normalized output and verdicts on C8. The initial `-count=3`
invocation hit Go's default ten-minute timeout during the third repetition;
a separate `-count=1 -timeout=20m` run completed the missing 1c/2/3/0 cases.
Only completed subtests are counted. This is not a three-run lab series.

| Scenario | Passing / failing cases |
|---|---:|
| 1a blackout | 5 / 3 |
| 1b recovery | 1 / 3 |
| 1c one-way blackout | 9 / 7 |
| 2a rate falls | 0 / 4 |
| 2b rate rises | 0 / 2 |
| 2c plan changes | 0 / 2 |
| 2d cellular grants | 1 / 1 |
| 3a call lane gains delay | 2 / 2 |
| 3b other lane becomes better | 0 / 4 |
| 3c both lanes gain delay | 1 / 3 |
| 0 cold transfer | 0 / 2 |

A narrower gigaradio diagnostic records the 1a lane-0 blackout deadline.
At 22.999 s the failed lane is dead; the survivor is live with ACK-progress
age 33 ms and queue delay zero, but its legacy pacing target is only
653,342 B/s. Its capacity is 737,189 B/s after 49 delay signals and nine
capacity decays. Required payload service is 20,242,721 B/s (75% of the
26,990,294 B/s independent reference); actual `[22,23)` payload is
84,000 B/s in that direction and 6,868,800 B/s in reverse.
**Inference from snapshots and pacing code:** liveness/allocation alone cannot
meet that deadline while retaining this target. The failing stage 1 gate
depends on the later delay/capacity replacement. This explains a model
bottleneck, not the field Speedtest cause or every failing case.

The original stop/report condition and section 9 stage boundary therefore
remain material: proceeding to the later estimators before proving stage 1
requires an explicit sequencing amendment. Preserve every failed gate and
deadline; do not add a capacity heuristic to stage 1 or label it accepted.
Stages 2–3 have not started, and no new lab/regression series is claimed.
The full non-privileged gate passes for the directional test change; Nix
build and packaged source-identity checks remain required at handover.

Evidence under `/srv/nvme/tmp/wanbond-adaptive-evidence/`:
`new-baseline-upload-20261004-212139/`,
`new-baseline-upload-3m-disk-20261004-214921/`,
`new-baseline-post-download-20261004-215337/`, their referenced
`field-direct-before-tunnel-c8-new-baseline*` directories,
`edge-run-cleanup-20261004/`,
`{c8-new-baseline,original-baseline}-directional-reference-three.jsonl`,
`c8-new-baseline-three-run-provenance.json`,
`c8-new-baseline-stage1-deadline-diagnostic.txt`, and retained diagnostic
source/patches. Reproduction commands:

```sh
nix develop --command go test -tags adaptivepolicy ./internal/bond \
  -run '^TestAdaptiveFieldStandbyDirectionalService$' -count=3 -timeout=20m
nix develop --command go test -tags adaptivepolicy ./internal/bond \
  -run '^TestAdaptivePolicy' -count=3 -timeout=30m
```

## 11. Revised execution goal — operator, 2026-10-04

Improve the adaptive transport against observed installed baseline `b444920`
across the metrics below. Advance through the delay and capacity replacements
despite explained model/lab failures, retaining those failures as evidence
and rerunning their scenarios. This supersedes the failed-gate stop and
stage-acceptance prerequisite in sections 5, 9 and 10. Implementation order
remains delay, then capacity; acceptance is assessed across the completed
policy, without calling an incomplete intermediate stage accepted.

| Metric | Desired change and retained targets |
|---|---|
| TCP payload goodput, upload/download and together | Higher relative to contemporaneous available service; 75% in steady changed conditions, 70% under cellular variation |
| Voice round-trip latency | Lower median/p95/p99; p99 under 150 ms during rate changes, survivor idle p99 +50 ms after failover |
| Voice jitter and receive gaps | Lower latency spread; no gap of 150 ms |
| Voice loss | Under 1%, at most three consecutive losses; preserve zero-loss cases |
| Bulk continuity | Delivery in every second after the first outage second |
| Outage/recovery adaptation | Survivor goodput within 3 s; returning lane carries bulk within 2 s and pair goodput within 5 s |
| Rate adaptation | Fall within 5 s, rise within 10 s, plan upgrade within 20 s; no expiration burst |
| Delay adaptation and ranking stability | Follow better delay within 2 s after a delay rise, switch within 5 s when another lane improves, at most one move per 5 s under jitter |
| Cold transfer | 60% of available pair goodput within 7 s |
| Bandwidth efficiency | Lower repairs/copies/expired datagrams per delivered byte; retain mobile RX+TX MB |

A zero-loss baseline cannot be strictly improved numerically: preserve it
while improving latency, service, adaptation and efficiency. Capped flows
already reaching their offer establish lower bounds; increase a bounded
offer only where necessary to expose headroom, recording its byte budget.
Assess repeatable gains in paired baseline/candidate/baseline field sets
with immediate direct-link references, phase-aligned counters and startup
ages. Compare distributions and the observed RF/scheduler spread; do not
invent fixed tolerances for the previously qualitative gates. Field remains
the behavioral reference. Lab/model disagreement triggers investigation and
further measurements rather than terminating implementation automatically.
The operator reiterates this objective on 2026-10-05: a failed experiment
is rejected and investigated, rather than ending the improvement goal.

First restate the remaining tests of removed mechanisms as outcome tests in
a separate code commit; then replace the stage 2 delay model and remove its
superseded rules together. Capacity replacement follows, with real queued
traffic for demand-triggered bounded pushes and no synthetic probe traffic.
Every estimator change must reduce the `control.go` constant inventory.

All invariants, reproduction discipline, separate code/docs commits, full
non-privileged checks and Nix handover build remain required. Temporary field
candidates retain restore timers, compressed edge binaries, voice-first
ordering and metered workloads. Unsafe access changes and a required wire
format revision still require resolution before that specific operation.
Stage 4/video and permanent deployment remain outside this goal.

The goal-tool attempt to replace the earlier paused goal is rejected because
that goal is unfinished; the available status API cannot resume or amend its
objective. This section records the revised authorized objective without
claiming the earlier goal complete. Work continues under this instruction.

### Delay-test restatement before replacement

**Observed:** tests of direct `rebaseline` state, exact congestion-threshold
constants and direct unloaded-window field mutation are replaced with public
transport outcomes before removing those mechanisms. Transient and persistent
idle delay changes check which lane carries voice originals; independent
forward/reverse delay variation checks voice loss/p99 and TCP payload service.
All four outcome cases pass three identical runs on unchanged C8 policy.

The simultaneous two-direction variation adds a stronger progression case:
both directions deliver 65,520 B/s against an independent 867,845 B/s reference
(75% required), despite zero voice loss and 63 ms p99. It fails identically
three times before the replacement. This is not relabeled as a fix or weakened
to match the baseline. A second public reproduction shows fresh ACK traffic
retaining transit-floor evidence aged 19.956 s in both directions, again three
identical failures against the ten-second freshness contract. These are model
observations, not field root causes. Evidence:
`stage2-outcome-restatement-three.txt` and
`stage2-delay-reproductions-red.txt` under the existing evidence directory.

Mapping: `TestOneUnloadedSampleDoesNotReorderLanes` becomes
`TestUnloadedDelayChangesChooseTheBetterVoiceLane`;
`TestThresholdIsBounded`, `TestLoadedRTTVariationDoesNotExpandWindow` and
`TestLoadedBaseRTTDoesNotExpandWindow` become the directional cases of
`TestDelayNoisePreservesVoiceAndBulkService`. The additional bidirectional
service and aged-floor reproductions remain tagged `adaptivepolicy` until
their corrections are verified. Production estimators are unchanged by this
test commit; stage 3 mechanism restatements remain to do.

### Capacity-test restatement and delay prototype observations

**Observed:** before changing capacity control, `422870f` replaces private
sender-limited discovery, slow-lane pulse wins and plateau-state checks with
public transport service checks. Sparse first and resumed transfers over a
queued 1.25 MB/s lane deliver 831,600 and 856,560 B/s against the independent
867,845 B/s reference. Batched receipts deliver 5,388,560 B/s with all 750
measured voice datagrams received and 90 ms one-way p99. These pass three
identical runs. Restart budget, repeated slowdown service and traffic-counter
outcomes also pass three runs on unchanged C8 policy.

**Observed failures:** the same sparse and resumed fixtures on a policed
lane deliver 393,360 B/s and 465,120/215,760 B/s respectively. A 90 kB/s lane
raised to 625 kB/s delivers only 10,560 B/s against a 417,458 B/s available
reference. All fail their unchanged 75% service gate three times. The private
probe-win assertions passing did not establish useful TCP service; retain
these stronger cases under `adaptivepolicy`, rather than weakening them.
This is a model finding, not a reproduced field cause.

Mapping: `TestSenderLimitedDiscoveryKeepsTheEstimate` becomes
`TestSparseAndResumedSendersKeepBulkProductive` and
`TestAdaptivePolicedSparseAndResumedSenders`; `TestProbeWinsCountOnASlowLane`
becomes `TestAdaptiveSlowLaneRateIncreaseMakesBulkProgress` and the existing
`TestPolicedLaneIsNotOverdriven`; `TestPlateauEstimateIsWhatTheLaneSustained`
becomes `TestBatchedReceiptsKeepBulkProductive` together with the already
restated repeated-stall service cases. Capacity-value assertions in catch-up
and restart tests become bulk service and physical traffic-budget outcomes.
Controller-consequence counter assertions become useful service accompanied
by actual sent/acknowledged/original counters. Evidence is
`stage3-capacity-outcome-baseline-complete-three.txt` and
`stage3-restatement-default-complete-three.txt` in the evidence directory.

**Observed prototype results, not accepted behavior:** the separate delay
experiment refreshes the formerly 19.956 s old transit floor and maintains
bulk through a 15 ms propagation-level change. Under two-direction delay
noise it increases bulk from 65,520 to 889,800 B/s but raises voice p99 from
63 to 154 ms, failing the 150 ms gate. Standing link-queue p90 is 46 ms on
100 Mbit/s and 26 ms on 1.25 Mbit/s, both exceeding the existing 20 ms gate.
It is not a field candidate. An initial ranking defect reset residence time
on a suspect lane; preserving preference while the existing suspect-copy
fallback operates passes the transient and persistent delay outcomes.

**Inferred from the traces:** short receiver-clock delivery samples inflate
the old capacity estimate, while an earlier queue-quality predicate admits
self-queued transit as propagation. Test the corresponding send/receive
flight sampler next, retaining the current delay experiment in an isolated
worktree. The installed `b444920` baseline remains the field reference.

### Physical-service restatements and capacity recovery trace — 2026-10-05

**Observed:** `5122feb` finishes two remaining command-target restatements
before replacing their mechanisms. Restart now checks actual slow-lane payload
service (at least 75% of the independent 56,858 B/s budget), retaining the 15%
physical-drop ceiling. The unchanged controller delivers 54,080 B/s with 6%
dropped in three identical runs. The lightly loaded voice case now checks
actual offered bytes, physical drops and voice delivery/p99: 41,646 B/s on
62,500 B/s, zero physical drops and 5,400/5,400 voice datagrams at 43 ms one-way
p99, again three identical runs. Evidence:
`physical-budget-restatement-baseline-three.txt`.

**Observed isolated prototype:** `stage23-slow-recovery-tcp-trace.txt` shows
zero physical WAN drops throughout the upgrade fixture, despite repeated TCP
timeouts, tunnel queue drops and near-zero bulk progress. Therefore the
previous physical-loss hypothesis is not supported for this case. Reading the
scheduler exposes a one-bulk-datagram flight restriction while voice uses a
slow lane, even when it is the only lane. Removing that restriction changes
post-upgrade service to 335,640 B/s against a 417,458 B/s reference, passing
75%; the source/measurements are retained in
`stage23-remove-bulk-flight-cap.txt`. This is a model result, not a field cause.
The same experiment still fails policed sparse/resumed service and 1.25 Mbit/s
steady utilization (86.6% against the unchanged 93% gate). Two-direction noise
bulk reaches 823,680 B/s with zero voice loss and 117 ms p99, compared with the
baseline's 65,520 B/s and 63 ms p99: service improves while latency worsens.
It does not establish improvement on all metrics.

**Researched:** the [BBR draft's delivery-rate sampler](https://datatracker.ietf.org/doc/html/draft-ietf-ccwg-bbr-06#section-4.1.2)
snapshots delivery state per packet and tracks application-limited flight
phases; low application-limited samples must not lower a path-capacity model.
This is a reference for the next sampler review, not evidence that copying
BBR or the current prototype meets wanbond's gates. The field baseline remains
`b444920`; no candidate is started by these tests.

### Isolated flight-allowance field comparison — 2026-10-05

**Observed:** an isolated C8 experiment removes the one-bulk-datagram flight
restriction, without the estimator replacement. Source `7860b97`, executable
SHA-256 `585ef93ba6f292ad69dcf766396f8916d0eed6c404117d944f25ef639a9e3d6c`,
is tested temporarily on both hosts with verified 15-minute restoration
timers. The full non-privileged gate fails three existing voice outcomes:
single slow lane, sustained ACK backlog and takeover catch-up. These failures
are retained; this field trial does not approve the experiment for merging.

All three field phases start after a daemon restart and voice-only preflight.
Direct Starlink/5G uploads precede loaded tunnel download/upload; a direct 5G
upload follows. All transfers use the same destination and bounded payloads.
Loaded voice runs during tunnel transfers, independently of raw references.
The following are measured payload rates, with 10 Mbit/s down and 3 Mbit/s up
offers; reaching an offer establishes a service lower bound.

| Measurement | Baseline before | Experiment | Baseline after |
|---|---:|---:|---:|
| Direct Starlink upload, Mbit/s | 0.523 | 0.516 | 0.517 |
| Direct 5G upload before, Mbit/s | 2.994 | 2.989 | 2.993 |
| Tunnel download, Mbit/s | 8.233 | 8.153 | 10.006 |
| Tunnel upload, Mbit/s | 2.994 | 2.994 | 2.862 |
| Direct 5G upload after, Mbit/s | 2.993 | 2.998 | 2.993 |
| Voice p99 during download, edge/hub RTT ms | 42.4 / 46.9 | 42.2 / 44.0 | 39.3 / 40.3 |
| Voice p99 during upload, edge/hub RTT ms | 40.8 / 43.0 | 37.3 / 38.3 | 36.7 / 38.0 |

Voice loses no datagrams in the one-second-trimmed transfer windows; the
largest observed gap is 50.3 ms. RTT uses each client's monotonic clock.
Start-of-phase clock exchanges bound the relative host clocks to less than
0.51 s, inside the trimming margin; drift during a phase is not measured.
Raw downlink capacity is not calibrated. **Inference:** these observations
do not establish an improvement: candidate throughput/latency is within the
baseline spread, and most upload measurements reach the offered ceiling.

The first completed setup ran voice only before bulk and cannot establish
loaded voice performance. The next setup incorrectly overlapped tunnel voice
with the raw standby reference and timed out before candidate activation.
Both attempts are retained separately. The corrected three-phase comparison
uses 99.479 mobile RX+TX MB. Including the two earlier collector intervals
gives 136.080 MB; the enclosing interval, including gaps, management and
background, is 157.720 MB. These are VLAN counters, not provider billing;
the nested totals must not be added.

**Observed restoration:** both hosts run `b444920` again, with empty runtime
overrides and inactive restoration timers. No WAN impairment ran. Edge WAN
qdiscs remain `noqueue`. The five experiment-owned `/run/wb-reference-*`
directories are archived and removed after checking the archive listing and
digest; active configuration is retained. `/run` is 4% used. Evidence,
commands, identities, failed gates and cleanup logs are under
`/srv/nvme/tmp/wanbond-adaptive-evidence/flight-cap-field-20261005/`;
the failed non-privileged run is `c8-no-bulk-cap-go-tests.txt` in its parent.

The scratch `candidate.sh` build action now explicitly stamps the source
commit/time: its initial build had no Go VCS metadata. This changes build
identity, not candidate activation/restoration. Record and verify the daemon
identity after activation as well as the executable hash.

**Rejected hypothesis:** accepting only capacity samples associated with
physical congestion worsens the replacement model. A pacing-limited sample
measures a lower bound, not a proven capacity ceiling; its presence alone is
not evidence of a defect. The stronger private test imposed an unsupported
interpretation of “known” and is retired, with source and failed experiment
retained as `stage23-physical-confidence*`. The sampler review continues
against public service outcomes; no production estimator change is accepted.

### Controlled uplink upgrade and two-lane reproduction — 2026-10-05

**Observed:** the same isolated flight-allowance binary receives a second
baseline → experiment → baseline field comparison, with cold restarts and
voice-only preflights. Direct Starlink/5G uploads to the same destination
immediately precede each shaped trial. Their payload rates are respectively
0.528/3.017, 0.503/2.938 and 0.511/2.920 Mbit/s; the 5G offers are capped at
3 Mbit/s and establish lower bounds.

Only edge IPv4 UDP to `45.11.171.73:51820` on `end0.232` is shaped:
400 kbit/s rises to 2 Mbit/s fifteen seconds into a thirty-second uplink TCP
flow. The TBF burst is 4,000 bytes and its latency bound 100 ms; other traffic
uses an unshaped prio band. Observed management replies use `end0`, table 100,
via `192.168.222.1`; this route does not identify a WAN provider. The classifier
excludes SSH/ZeroTier regardless of provider. Verified 120-second cleanup
timers precede every network mutation; the paired candidate has its existing
15-minute binary restoration timers. The temporary concentrator firewall
rule permits only the test TCP port and source on `wanbond0`. No blackout or
deployment configuration change runs.

| Measurement | Baseline before | Experiment | Baseline after |
|---|---:|---:|---:|
| Approximate TCP payload in receiver seconds [5,15), Mbit/s | 0.146 | 0.263 | 0.166 |
| Approximate TCP payload in receiver seconds [25,30), Mbit/s | 1.358 | 0.244 | 1.546 |
| Whole-report bounds for [25,30), Mbit/s | 1.098–1.644 | 0.192–0.305 | 1.269–1.849 |
| Voice p99 during locally confirmed TCP activity, edge/hub RTT ms | 62.4 / 57.2 | 61.0 / 55.3 | 72.6 / 71.2 |

Approximate window rates interpolate partial receiver reports; they cannot
prove exact adaptation deadlines. The lower bound counts reports wholly
inside the window; the upper counts every overlapping report. Candidate
service is lower even under those bounds. This single paired set establishes
no universal RF result or repeated gain. All guarded TCP-active voice windows
have zero loss; the largest receive gap is 82.6 ms. Each host uses its own
clock, guarded TCP timestamps and archived result-file metadata; no exact
cross-host phase alignment is claimed. Full-stream trailing hub misses were
collector cleanup artifacts: the edge echo server stopped before the hub
sender. The uncorrected analysis remains beside the corrected evidence.

At approximately ten seconds after the upgrade, edge 5G capacity estimates
are 266,772 / 54,862 / 262,235 B/s, with live ACK progress in all phases.
**Inference:** voice-lane bulk isolation and backlog-dependent probing are
plausible contributors to the experiment's failure to learn the upgrade.
These snapshots do not establish a field root cause.

**Collection corrections:** default 128 KiB iperf writes produced coarse
zero/burst receiver reports in an earlier baseline attempt; the corrected
set uses `-l 1200`. That earlier attempt activated no candidate and is retained
separately. Gzip log archives replace the uncompressed collection that timed
out after restoration. The corrected comparison uses 23.262 mobile RX+TX MB;
the earlier collector interval uses 9.381 MB. The enclosing interval through
cleanup is 42.996 MB, including management/background. These overlap and must
not be added. Both hosts are independently verified on `b444920`, with empty
overrides, inactive test timers, no test firewall rules and `noqueue` WANs.
Four owned `/run` reference directories are archived and removed; active
configuration remains. Raw records, executable identities, commands,
`analyze.py`, bounded analysis and restoration checks are in
`/srv/nvme/tmp/wanbond-adaptive-evidence/flight-rate-upgrade-20261005/`.

**Observed model reproduction:** test-only `1bfd266` adds a lower-latency
50 kB/s uplink raised to 250 kB/s, beside a 62.5 kB/s policed alternate,
with voice and one uplink TCP flow. Fixed delays and FIFO buffering are
representative inputs, not an RF replay or an exact TBF model. At the
ten-second deadline original `f75668e`, C8 and the isolated flight-cap removal
all deliver 27,600 B/s against the independent 217,408 B/s payload reference,
identically across three runs. Voice has zero loss and 24 ms p99. Original
and C8 sustained service is 21,240 and 25,920 B/s respectively. This supports
the structural model failure; because the field baselines recover, it does
not explain the field baseline/candidate contrast. No threshold is weakened.
The tagged progression case remains outside the default correctness gate.

**Observed isolated replacement experiment:** moving demand-triggered bounded
pushes from ACK processing to `Poll` makes them respond to waiting real TCP
traffic even when that lane's feedback arrives between bursts. Deadline and
sustained uplink delivery become 180,000 and 165,120 B/s, passing 75%.
Bidirectional delay noise delivers 837,960 B/s at 124 ms voice p99, compared
with C8's 65,520 B/s and 63 ms. Directional noise, sparse queued senders,
batched receipts and steady queue/utilization outcomes pass this run. Slow
survivor voice, ACK backlog and takeover still fail, so neither an accepted
policy nor improvement on all metrics follows. This source is retained in
`stage23-poll-probing-source.tar.gz`, with its digest/provenance and
`stage23-poll-probing-outcomes.txt`; it has not run in the field. Go AST
inventory counts 12 constants in its `control.go`, versus C8's 31; this includes
the function-local `judged` constant. Main retains C8 production behavior.

**Rejected experimental requirement:** a private test required a congestion
cut to persist after fresh feedback showed the queue below threshold, despite
no new bandwidth peak. That is not sufficient evidence that the pace remains
unsafe; forced drain retention worsened public service outcomes. The test
and change are retired, with `stage23-forced-drain-rejected*` retained. Further
probe-gain and flight-limit trials retain their failures separately. Continue
against public delivery, latency and efficiency outcomes, rather than adding
constraints to make private state tests pass.

### Estimator replacement field tradeoff — 2026-10-05

**Observed:** the unmerged `adaptive-estimator-candidate` branch applies the
delay replacement before the capacity replacement, in separate code/docs
commits. Field source `b5948c5` uses aged delay evidence, sender/ACK-clock
delivery samples and bounded pushes of queued real traffic from `Poll`.
It removes the superseded controller; `control.go` contains 12 constants,
against C8's 31. ACK v1 and the wire format are unchanged. Its cross-built
arm64 executable SHA-256 is
`d022ab214c5c4bf140be7e31e0be0a6aa93d93d92ab2c301e04c50bfdb3b8b61`.
Its Nix build passes. This is an experiment, not an accepted replacement.

One cold baseline → candidate → baseline field set repeats the preceding
400 kbit/s → 2 Mbit/s mobile-UDP shaping, thirty-second TCP upload offered
at 3 Mbit/s, 1,200-byte writes and simultaneous voice. Voice-only preflights
and immediate direct uplink references precede each phase. Starlink upload
references are 0.518 / 0.527 / 0.530 Mbit/s; mobile references are
2.985 / 2.981 / 2.965 Mbit/s at the 3 Mbit/s offer, hence lower bounds.
Verified 120-second network cleanup and 15-minute candidate restoration
timers protect the trial. The classifier excludes management traffic.

| Observed measurement | Baseline before | Candidate | Baseline after |
|---|---:|---:|---:|
| Approximate TCP payload in receiver seconds [5,15), Mbit/s | 0.287 | 0.150 | 0.273 |
| Approximate TCP payload in receiver seconds [25,30), Mbit/s | 0.284 | 1.415 | 0.286 |
| Whole-report bounds for [25,30), Mbit/s | 0.217–0.334 | 1.135–1.692 | 0.234–0.332 |
| Voice p99 during guarded local TCP activity, edge/hub RTT ms | 50.3 / 48.5 | 167.9 / 139.2 | 49.2 / 48.3 |
| Edge voice lost/sent | 0/1429 | 2/1411 | 0/1421 |
| Hub voice lost/sent | 0/1417 | 0/1403 | 0/1395 |

The receiver-report bounds establish a late-window service increase within
this paired set. The low-rate window and voice distributions regress.
Neither repeatability, an exact adaptation deadline nor improvement on all
metrics follows. The earlier baseline field set recovered at 1.36–1.55
Mbit/s; the current baseline does not. Direct capped references alone cannot
explain that variation. Preserve both sets rather than choosing the more
favorable baseline. Sender-first voice cleanup now prevents the earlier
trailing-miss artifact; analysis still uses each host's guarded local clock.

**Observed model limits:** the two-lane upgrade case passes identically
three times on this source: deadline/sustained payload 192,000/175,680 B/s
against the 217,408 B/s independent reference, zero voice loss and 36 ms p99
in both directions. Baseline voice p99 is 24 ms. A full tagged run of
scenarios 2a–2d, 3a–3c and 0 still fails every top-level case. All unloaded
3a–3c variants pass; every bulk-loaded variant fails. Only gigaradio passes
2d. Other variants fail on the retained delivery, voice, ranking or expiry
checks. The preceding bulk-demand-only version also fails 18 default bond
tests; this source's selected slow-survivor voice, ACK-backlog and small-flow
discovery outcomes remain failed. A successful build is not a passing policy
gate. These deterministic model results are distinct from host CPU effects.

**Inference for the next experiment:** field samples show post-push excess
delay alongside increasing path-delay estimates and larger flight allowance.
A reproduction confirms that a draining lane with 50 ms measured excess
delay can qualify as unqueued using pre-push rate averages. That qualification
defect is reproduced before its correction; whether correcting it improves
field voice remains unmeasured. Continue by bounding queued probe service
and testing voice and throughput together, rather than promoting this source.

**Observed restoration/accounting:** both hosts are independently verified
on `b444920`, with empty runtime overrides, inactive trial timers, no test
firewall rules, original `noqueue` WANs and exit policy `auto`. This comparison
uses 21.805 mobile RX+TX MB including management/background; the enclosing
interval through cleanup uses 26.516 MB. They overlap and must not be added.
Three owned edge `/run` reference directories are archived, their contents
compared with the originals, then removed; the trial executable is removed
from both hosts. Active configuration remains. Archive
`/var/tmp/wanbond-estimator-reference-archive-20261005.tar.gz` has SHA-256
`4dbf8b18822850938a82b661666fe9d2ceb6cda92d1da497ce4977b169aade3f`.
Raw records, source identity, bounded analysis, timers and cleanup checks are
in `/srv/nvme/tmp/wanbond-adaptive-evidence/estimator-field-upgrade-20261005/`.
Main retains the installed baseline policy. Failed experiments guide further
work; the revised improvement objective in section 11 remains unfinished.
