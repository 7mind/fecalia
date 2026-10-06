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

The earlier goal-tool attempt to replace a paused goal was rejected because
it was unfinished; the status API could not resume or amend its objective.
This section records the revised authorized objective without claiming that
goal complete. **Observed 2026-10-05, 23:01 UTC:** the tracker now reports the
original goal active. Work continues; no further tracker approval is needed.

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

### Post-push correction and sampling follow-up — 2026-10-05

**Observed:** candidate `ef36799` excludes drain-phase samples from unloaded
path-delay evidence after the failing reproduction above. Its executable
SHA-256 is `7ed29ceb9a5496545481bd302f262158c64a79ed307b103a8733c9ccd33fd5f0`.
Another temporary cold baseline → candidate → baseline set retains the same
voice-first ordering, immediate capped references, 400 kbit/s → 2 Mbit/s
mobile-UDP shaping, three-Mbit/s TCP offer and restoration timers.

| Observed measurement | Baseline before | Candidate | Baseline after |
|---|---:|---:|---:|
| Approximate TCP payload in receiver seconds [5,15), Mbit/s | 0.154 | 0.239 | 0.148 |
| Whole-report bounds for [5,15), Mbit/s | 0.139–0.170 | 0.218–0.264 | 0.132–0.180 |
| Approximate TCP payload in receiver seconds [25,30), Mbit/s | 1.412 | 1.564 | 1.392 |
| Whole-report bounds for [25,30), Mbit/s | 1.133–1.672 | 1.246–1.866 | 1.116–1.678 |
| Guarded voice p99 edge/hub RTT, ms | 49.5 / 50.6 | 99.9 / 118.2 | 41.7 / 70.0 |
| Guarded voice losses, edge/hub | 0 / 0 | 0 / 0 | 0 / 0 |

The low-rate payload increase survives report bounds; the late-window bounds
overlap, so a recovery gain is not established. Voice p99 and receive gaps
still regress against both surrounding baselines. The lower candidate p99
than the preceding experiment does not isolate the correction's effect:
baseline recovery also changes from about 0.28 to 1.4 Mbit/s. Direct mobile
references are 3.015 / 2.938 / 2.960 Mbit/s at a 3 Mbit/s offer; Starlink is
0.518 / 0.519 / 0.516 Mbit/s. These remain capped references, not RF capacity
ceilings. No all-metric improvement or three-run acceptance is established.

Both hosts are independently verified restored to `b444920`, with empty
overrides, inactive trial timers, cleared test firewall rules, `noqueue` WANs
and exit policy `auto`. The comparison uses 27.367 mobile RX+TX MB and the
enclosing interval through cleanup 32.391 MB. The interval enclosing both
estimator comparisons through this cleanup uses 64.383 MB, including
management/background and intervening work; these overlapping intervals must
not be added. Three owned `/run` reference directories are verified against
their archive before removal; both trial executables are removed. The edge
has only active `/run/wanbond` configuration and an empty candidate directory
among the inspected wanbond/reference paths; `/run` is 4% used. Archive
`/var/tmp/wanbond-estimator-drain-reference-archive-20261005.tar.gz` has SHA-256
`3de250d9db316ff06de2f49302de99015250aa7a556dee4a17bb370423076dc3`.
Commands, source identities, bounded analysis and postconditions are retained
in `/srv/nvme/tmp/wanbond-adaptive-evidence/estimator-drain-field-upgrade-20261005/`.

**Observed isolated model follow-up, not field source:** a fresh physical ACK
whose highest attempt has retired fails to advance the experimental sampler's
byte anchor. The next flight counts old delivery again: the failing test gives
15,000 B/s for 1000 new bytes in 200 ms, rather than 5000 B/s. Updating the
anchor for every fresh ACK corrects it. An unmatched ACK advances accounting
without fabricating a rate sample. Propagation-only capacity aging also fails
small-backlog discovery at 189,600 bytes in five seconds; including the peer's
ACK cadence in the aging round makes that existing outcome pass. Together,
the corrected clock and aging pass small-backlog discovery, batched receipts,
directional-noise service and the two-lane upgrade three times. Upgrade
deadline/sustained payload is 186,000/168,960 B/s with zero voice loss and
36 ms p99. Slow-lane voice, ACK backlog and 1.25 Mbit/s utilization still fail.
The candidate branch records these later corrections as separate code/docs
commits. Gentler probe trials improve some voice outcomes but fail discovery,
service or deadlines; their sources and outputs are retained as rejected
experiments. Main still has no production policy change. Next work must
resolve application-limited sampling and excessive queued probe service,
then compare delivery, latency and repair/expiry cost together in the field.

**Remaining target assertion restated, 2026-10-05:**
`TestUnderusedLossyLaneKeepsItsTarget` becomes
`TestUnderusedLossyLaneDeliversBurstyBulk` in a separate code commit before
bringing in the estimator replacement. Its physical link and offered bursts
are unchanged. Unique payload created from 10 to 30 s is measured after
500 ms of drain; admission must include the full 239,200,000 offered bytes,
delivery must reach 99%, and the existing zero-AQM-drop condition remains.
C8 delivers 99.98% with zero AQM drops identically three times. Evidence is
`underused-lossy-outcome-c8-three.txt`; this is an observed baseline outcome,
not a candidate improvement or a completed inventory of mechanism tests.


### Holding-source model and field follow-up — 2026-10-05

The unmerged estimator branch also corrects two fail-first sampler findings:
application-limited delivery remains tagged until its outstanding delivered
boundary passes, and expired capacity cannot undo a fresh congestion cut.
The reproductions pass three times; they do not independently establish better
service. Subsequent pacing experiments remain isolated from the installed C8
baseline. The following holding checkpoint and field comparison retain both
gains and failures. Its source/code and documentation are separate commits;
no replacement is accepted by these observations.

**Holding lower-bound delivery, 2026-10-05:** removing plateau-dependent
long pushes alone fails healthy steady utilization (1.2% on 100 Mbit/s) and
batched-receipt throughput (113,040 B/s). A temporary controller trace observes
pacing falling from 125,000 to about 53,000 B/s despite zero queue delay and
no loss/delay cuts. Delivery measured under the controller's own lower pace
then acts as a ceiling; the unmeasured 85% drain suppresses further discovery.
That causal interpretation is inferred from the trace, not a field finding.
Temporary instrumentation is removed. The standalone plateau removal is not
retained as an accepted policy.

The next isolated source holds the current or pre-push pace while awaiting
physical delay feedback, raises it when fresh delivery proves greater service,
and still cuts on measured congestion. It replaces the plateau-growth rule
with capacity known/unknown state, removes the `growing` estimator, and removes
the duplicate probe interval constant. `control.go` has 10 constant names
against C8's 31. Pushes remain bounded queued real traffic; ACK v1 is unchanged.
The preceding receipt-guard restatement is committed separately.

**Observed models:** the selected sampler/aging/receipt guards, small backlog,
batched receipts, directional noise, ACK backlog, steady utilization and
bursty underused delivery pass three times. Batched payload is 4,982,720 B/s;
ACK-backlog voice is 200/200 at 80 ms one-way p99. Steady utilization is
98.2%/97.4%, with 12.4/19.1 ms queue p90. Directional-noise voice loses none
at 136/149 ms RTT p99, with 854,520/839,040 B/s payload. The fivefold upgrade
passes three times at 189,600/172,560 B/s deadline/sustained payload and zero
voice loss at 36/40 ms RTT p99. Build/vet pass. The full default bond gate
still has eighteen top-level failures, including cold discovery, policer loss
(6.3%), slow voice (497/500 at 103 ms), takeover and mixed-load cases.
Cold scenario 0 still fails both families. These remain failed acceptance
outcomes, not overridden thresholds. A bounded field comparison is authorized
despite them to test the actual throughput/voice tradeoff.

A separate measured-capacity flight-ceiling trial is rejected: it improves
steady queue utilization but collapses upgrade payload to 2400/1560 B/s.
Measured delivery is a lower bound, so using it as a flight ceiling obstructs
probes. Its sources/records remain in `stage23-rejected-confirmed-flight-budget-*`
and `stage23-confirmed-flight-budget-*`. Holding-source records are
`stage23-feedback-holding-*`, the rejected plateau-only records are
`stage23-aged-startup-*`, and its trace is
`stage23-aged-startup-sampler-trace.txt`. No field improvement or accepted
replacement is established by these model observations.


**Holding-source field comparison, 2026-10-05:** clean temporary source
`1d272f95bd81d4222bfa96d264830b7e5a2e028b`, binary SHA-256
`56d327f89e7003918c33a29b54721758a39fb0afaa5f34ded15a600bbef2f90f`,
passes Nix build and build/vet, while its full default bond run retains the
eighteen failures above. A bounded baseline → candidate → baseline native
TCP upload comparison completes all phases with no cleanup errors. The
400 kbit/s → 2 Mbit/s change classifies only the concentrator-bound tunnel
UDP on 5G; management and the standby Starlink remain outside that class.
Two 50 Hz voice streams and their echoes run through it. Each phase first
passes voice-only checks and measures direct uploads to the same endpoint.

| Observed field metric | Baseline before | Holding candidate | Baseline after |
|---|---:|---:|---:|
| Low-rate payload, [5,15) s, approximate Mbit/s | 0.145 | 0.202 | 0.282 |
| Late payload, [25,30) s, approximate Mbit/s | 1.409 | 1.511 | 0.300 |
| Late whole-report payload bounds, Mbit/s | 1.129–1.690 | 1.212–1.812 | 0.238–0.357 |
| Edge voice RTT p99, ms | 82.6 | 62.9 | 55.0 |
| Hub voice RTT p99, ms | 98.9 | 74.7 | 53.7 |
| Edge/hub voice loss, % | 0/0 | 0/0 | 0/0 |
| Edge/hub maximum receive gap, ms | 83.7/104.7 | 130.7/133.6 | 53.8/57.9 |
| Edge peer AQM drops in guarded active window | 51 | 20 | 79 |
| Edge peer repair packets in that window | 2502 | 2649 | 1960 |
| Edge peer expired datagrams in that window | 4 | 5 | 2 |

**Inference and limits:** candidate late service is above the final baseline
with separated bounds, but overlaps the initial baseline. Voice p99 sits
between the two baselines and receive gaps worsen. This is not repeatable
improvement across metrics. Direct Starlink payload is 0.527/0.526/0.531
Mbit/s; direct 5G reaches 2.960/2.927/3.006 Mbit/s under its 3 Mbit/s offer,
which is a service lower bound. Whole-report throughput bounds do not certify
an exact adaptation deadline; local-clock guarded voice/counter windows do
not assume cross-host synchronization. Peer counters include voice and
feedback; the raw repair/drop counts are not unique-payload efficiency ratios.
No download, blackout or three-run lab series is claimed from this set.

**Observed restoration:** both active binaries match baseline SHA-256
`dce5c7e4dae9a13565e04c69aa2ac36a6ab388a51418282a027e14e18f4e5dd9`;
overrides and owned timers/firewall rules are absent, WANs have their original
noqueue qdiscs, and exit policy is `auto`. Owned reference directories and
temporary binaries are archived, content-compared and removed from `/run`;
the edge retains only its active config and an empty candidate directory.
Mobile RX+TX is 22.601287 MB for the comparison and 24.533616 MB through
cleanup, including background/management; these intervals are nested.
Evidence is `estimator-feedback-field-upgrade-20261005/`, including source,
raw phase logs, bounded/counter analyses, archive hashes and postconditions.
Continue with cold-service discovery, current-loss decisions and voice gaps;
this comparison does not accept the prototype or finish the objective.


**Subsequent isolated diagnoses:** current-loss holding reduces policer loss
from 6.3% to 2.5% but collapses upgrade payload to 24,000/23,760 B/s; reject it.
Full-ACK-round pushes restore healthy cold/increase discovery to 87.27/87.08
Mbit/s but produce 285/287 ms directional-noise voice p99, standing queues
of 54/96 ms and only 104/200 ACK-backlog voice receipts; reject them. Keeping
discovery open until first congestion reaches 99.99 Mbit/s cold but retains
only 5.39 Mbit/s after a rate increase and fails upgrade service. That is a
coupled diagnostic of push duration and AQM, not an accepted latched state.
An AQM-only disabled diagnostic leaves healthy discovery unchanged at
2.75/5.68 Mbit/s, rejecting AQM as the principal cause of those failures.

Removing the additional fixed probe cooldown in favor of physical receipt
coverage and measured queue eligibility raises discovery only to 3.31/6.86
Mbit/s; ACK-backlog voice regresses from 80 to 97 ms and fails its 90 ms gate.
A one-round delivery-maximum experiment passes upgrade at 225,600/176,040
B/s but directional-noise voice rises to 171/175 ms and policer loss is 6.5%.
Both are discarded. These are observed fixed-model outcomes, not field
results or reasons to end the improvement objective. Sources and records are
`stage23-{loss-aware-holding,holding-full-round-push,holding-first-congestion,
holding-no-aqm,feedback-eligible-push,round-maximum-delivery}-*`, with rejected
sources under `stage23-rejected-*`. The field-tested holding source remains
the experimental checkpoint; every diagnostic edit is restored.

**Research:** the July 2026 [BBR draft, section 5.5.10.3](https://www.ietf.org/archive/id/draft-ietf-ccwg-bbr-06.html#section-5.5.10.3)
uses a delivery maximum over a round and distinguishes loss response by probing
phase. This supports investigating feedback-cohort alignment; the isolated
maximum trial above does not validate an imported BBR policy. Next reproduce
which acknowledgement cohorts cause underestimation or congestion cuts, then
replace the corresponding sampling/phase rules together. Cold discovery,
policer efficiency, slow-link voice and loaded delay transitions remain the
measured outstanding requirements. Revisit native download and combined traffic
once a source preserves the passing voice/service outcomes; use immediate
direct references and bounded offers, and record mobile bytes. Retain model
failures while field comparisons guide suitability.

### Receiver-cohort field rejection and congestion-history reproduction — 2026-10-05

**Observed field source:** `a8fe4a974dd325f7c7622eb048c5ef0701033a98`
(code `098edbe`), binary SHA-256
`f1d011fd3c0ee130285e96fdd2a2b4a7001c0d1fb205d1be5224e82498ff0994`.
Baseline remains installed/tagged `b444920` / `v0.0.3`. This is one completed
baseline → candidate → baseline set, not three independent repetitions.

| Guarded measurement | Baseline before | Candidate | Baseline after |
|---|---:|---:|---:|
| Low-rate upload, estimated Mbit/s | 0.150 | 0.172 | 0.153 |
| Late upload, estimated Mbit/s | 1.432 | 1.561 | 1.395 |
| Late upload whole-report bounds, Mbit/s | 1.140–1.676 | 1.244–1.814 | 1.110–1.667 |
| Edge voice RTT p99, ms | 46.6 | 186.1 | 49.7 |
| Hub voice RTT p99, ms | 47.1 | 191.7 | 51.2 |
| Edge maximum receive gap, ms | 50.1 | 177.7 | 43.8 |
| Hub maximum receive gap, ms | 75.8 | 180.3 | 61.1 |
| Edge voice loss, % | 0 | 0.281 (4/1424) | 0 |
| Hub voice loss, % | 0 | 0.638 (9/1411) | 0 |
| Raw peer repair datagrams | 2353 | 1665 | 2461 |
| Raw peer expirations | 4 | 20 | 3 |
| Raw peer AQM drops | 54 | 10 | 60 |

Voice uses guarded TCP-active windows on each host's local clock; throughput
estimates assume uniform arrivals inside receiver reports, while the bounds
use whole certainly-contained/possibly-overlapping reports. Raw peer counters
include other peer traffic and do not establish efficiency per unique TCP byte.
Immediate direct Starlink uploads measure 0.526/0.526/0.516 Mbit/s; 5G
measures 3.005/2.969/2.990 Mbit/s at a 3 Mbit/s offer, which is a service
lower bound. Both voice-only preflights receive 500/500 in every phase.

**Decision from those observations:** reject this combined policy for
promotion. Upload bounds overlap, while voice violates the 150 ms p99/gap
requirements and loses the zero-loss baseline property. No download or
combined-direction improvement is measured. Nix/build/vet, frontend (44 tests),
patched engine and formatting pass; the full default Go run retains 20 bond
failures (`stage23-receiver-cohort-root-gate.txt`). The tagged scenario outcomes
are retained separately. Passing ACK accounting tests do not accept the policy.

**Observed restoration:** both running executables again have baseline SHA
`dce5c7e4dae9a13565e04c69aa2ac36a6ab388a51418282a027e14e18f4e5dd9`,
empty runtime overrides, no owned restore/reference/rate timers or hub firewall
rules, and both edge WAN qdiscs are `noqueue`. The initial `raspi5l` operator
policy is retained. Owned edge `/run` reference directories are archived,
content-compared and removed; the verified inactive candidate binary is
removed from both hosts. The active edge config remains present at mode 0600.
Evidence: `receiver-cohort-field-upgrade-20261005/` and immediate
`field-direct-before-tunnel-receiver-cohort-*` folders. Mobile RX+TX is
24.679 MB for the comparison, 26.459 MB through runtime cleanup; these are
nested intervals, including background traffic, not additive charges.

**Next reproduced defect:** congestion cuts pacing to 38,250 B/s using a
45,000 B/s delivery observation, then holding restores 95,000 B/s from the
earlier 100,000 B/s capacity sample without a new delivery measurement.
`TestCongestionDoesNotRestoreEarlierCapacityWithoutNewDelivery` fails
identically three times on this source (`stage23-congestion-capacity-red.txt`)
and is committed before replacement. The field trace also shows overshoots
and later cuts; attributing the field regression to this precise mechanism
remains an inference. Replace reuse of contradicted capacity history with
revision of the existing model, then rerun service/voice/queue outcomes before
another capped field comparison. This rejects an experiment, not the authorized
improvement objective; installed `v0.0.3` remains the reference.

### Congestion-history field comparison and per-lane demand reproduction — 2026-10-05

**Observed temporary source:** `55407336fd88e17822971648093204ea9573a13f`
(code `99b9c25`), SHA-256
`439a5b26a294d375ca885b42468b43ebb9514217fc152cefa6cf57bd75247281`.
This is one new paired set, not a repetition of the preceding source.

| Guarded measurement | Baseline before | Candidate | Baseline after |
|---|---:|---:|---:|
| Low-rate upload, estimated Mbit/s | 0.276 | 0.175 | 0.172 |
| Late upload, estimated Mbit/s | 0.232 | 1.511 | 1.388 |
| Late whole-report bounds, Mbit/s | 0.188–0.292 | 1.185–1.809 | 1.114–1.651 |
| Edge voice RTT p99, ms | 54.9 | 156.3 | 65.3 |
| Hub voice RTT p99, ms | 53.7 | 123.3 | 62.7 |
| Edge maximum receive gap, ms | 51.0 | 85.9 | 75.3 |
| Hub maximum receive gap, ms | 49.3 | 109.0 | 69.3 |
| Edge voice loss, % | 0 | 0.142 (2/1410) | 0 |
| Hub voice loss, % | 0 | 0 | 0 |
| Raw peer repairs / expirations / AQM drops | 1879 / 3 / 77 | 2070 / 8 / 22 | 2222 / 3 / 65 |

The first baseline does not adapt to the rate increase; the last does.
Immediate direct Starlink service is 0.521/0.522/0.510 Mbit/s, and 5G reaches
the 3 Mbit/s offered cap (3.003/2.987/3.038). This shows variation in the
transport result even with similar capped references; it does not prove RF
conditions were identical. All voice-only preflights receive 500/500.
Throughput estimates/bounds and guarded local voice windows use the preceding
set's method. The raw peer counters are not efficiency ratios per unique TCP byte.

**Decision:** no promotion or all-metric gain. The candidate improves late
service against the first baseline and has better voice tails than the
preceding experimental source, but candidate service overlaps the returning
baseline, edge p99 exceeds 150 ms, and it loses the zero-loss baseline property.
No download/combined measurement or three-run field acceptance is established.
The model now passes tagged bidirectional-noise service three times at 148 ms
RTT p99, but the upgrade deadline and 17 default bond tests remain failed.

Both exact deployed baseline binaries, empty overrides, absence of owned
timers/firewall rules, `noqueue` WAN qdiscs and the initial `raspi5l` policy are
independently verified restored. Owned edge `/run` references are archived,
content-compared and removed; verified inactive candidate binaries are removed
from both hosts. Evidence is `congestion-history-field-upgrade-20261005/` and
`field-direct-before-tunnel-congestion-history-*`. Mobile RX+TX is 23.517 MB
for the comparison and 28.902 MB through cleanup. The enclosing interval of
both new field sets through final cleanup is 63.778 MB, including their gap
and background traffic; these nested intervals must not be added.

**New observed sampler reproduction:** bulk is queued for an alternate lane
while this lane reserves voice and admits no bulk. Two sparse voice receipts
nevertheless certify 14,450 B/s as this lane's capacity after its earlier
50,000 B/s estimate expires. Global backlog marks this lane's unsaturated
flight as non-application-limited. The new outcome fails three times for that
reason (`stage23-lane-sampling-demand-red.txt`) and is committed before
replacement. Its first draft failed a fixture age precondition instead; that
output is retained as `stage23-lane-sampling-demand-fixture-red.txt` and is not
claimed as reproduction evidence. Per-lane eligible demand must qualify rate
samples; a peer-wide queue cannot establish saturation of every lane. This
is a code/model finding; its contribution to field tails remains inferred.
Replace that qualification using existing class demand/reservations, rerun
voice, service, aging and upgrade outcomes, and hold further metered trials
until this narrower correction is reviewed by those outcomes. Installed
`v0.0.3` remains the best accepted candidate and the improvement objective
remains unfinished.

### Eligible-backlog field comparison — 2026-10-05

**Observed temporary source:** `fd088cc590001d9e0efe9fe0b901337dc8aa8181`
(code `7bc31b5`), SHA-256
`f9aa0ce857cd5486233466c4775b798f3006432c59f015db84581babad376c96`.
This is one paired set, using the same bounded direct references, cold restarts,
voice-first preflights, 400 kbit/s → 2 Mbit/s selective uplink shape and
baseline/candidate/baseline method as the preceding sets.

| Guarded measurement | Baseline before | Candidate | Baseline after |
|---|---:|---:|---:|
| Low-rate upload, estimated Mbit/s | 0.158 | 0.186 | 0.151 |
| Late upload, estimated Mbit/s | 1.231 | 1.425 | 1.385 |
| Late whole-report bounds, Mbit/s | 1.002–1.513 | 1.162–1.734 | 1.121–1.661 |
| Edge voice RTT p99, ms | 71.6 | 188.2 | 51.1 |
| Hub voice RTT p99, ms | 77.3 | 175.0 | 47.5 |
| Edge maximum receive gap, ms | 63.1 | 85.0 | 73.3 |
| Hub maximum receive gap, ms | 326.7 | 92.1 | 76.1 |
| Edge voice loss, % | 0 | 0.283 (4/1412) | 0 |
| Hub voice loss, % | 0 | 0.071 (1/1402) | 0 |
| Raw peer repairs / expirations / AQM drops | 2242 / 0 / 31 | 1839 / 14 / 27 | 2494 / 1 / 48 |

The baseline's isolated 326.7 ms hub gap is retained; its cause is unknown.
Throughput bounds overlap, while candidate voice violates 150 ms p99 and
loses the zero-loss baseline property. **Decision:** reject promotion; no
repeatable all-metric gain is established. The corrected sampling outcomes
pass three times, including tagged upgrade and fast shared-lane service, but
16 default bond outcomes remain failed. Model progress does not accept this
policy. No download, combined-direction or three-run acceptance is claimed.

Both exact deployed baseline binaries, empty overrides, no owned
timers/firewall rules, `noqueue` WAN qdiscs and the initial `raspi5l` policy
are independently verified restored. The three owned edge `/run` reference
directories are archived, content-compared and removed; the verified inactive
candidate binary is removed from both hosts. Evidence is
`lane-demand-field-upgrade-20261005/` and immediate
`field-direct-before-tunnel-lane-demand-*` folders. Mobile RX+TX is 26.179 MB
for comparison and 28.218 MB through cleanup. The enclosing interval of all
three new sets, their gaps and background is 108.600 MB; these intervals overlap.

**Next observed reproductions:** congestion handling replaces a fresh
100,000 B/s capacity estimate with sparse application-limited voice at
14,450 B/s, or with expired delivery at 20,000 B/s. Each fails three times
before replacement (`stage23-congestion-sample-quality-red.txt`). Preserving
sample qualification and age fixes these outcomes and improves tagged
bidirectional noise to 123 ms RTT p99, but regresses sole slow-lane voice
from 500/500 at 75 ms to 497/500 at 97 ms. This uncommitted variant receives
no field trial. Investigate that service regression before another metered
comparison; the accepted `v0.0.3` reference remains unchanged.

### Fresh-evidence pacing field comparison — 2026-10-05

Experimental `897004cc0a73b902610df99df27535003a5e6f15` combines the reproduced
ACK-generation clock, sample qualification, acknowledged drain and aged physical
feedback corrections. Holding recovers only from qualified delivery newer than
the congestion response; retained capacity evaluates bounded real backlog
pushes rather than restoring the target unconditionally. Its controller has
8 constants against the baseline's 31. Selected model outcomes pass three
times: bidirectional noise at 134 ms RTT p99, steady utilization 94.4%/99.0%,
and upgrade payload 180,000/181,800 B/s with zero voice loss. The complete
non-privileged gate still has 20 bond failures; frontend's 44 tests, build,
vet, patched-engine tests, formatting and all other Go packages pass. Nix
build passes. This source remains experimental.

**Observed field set:** deployed → candidate → deployed, fresh starts,
voice-only preflights, immediate direct references and simultaneous loaded
voice around the same 400 kbit/s → 2 Mbit/s uplink trial. Both hosts run the
same pinned executable per round. Candidate SHA-256 is
`0de5355c6c502f26c6986e7de5c3da51d7be9833d8f35623efd5564c1edf2b6c`.

| Guarded measurement | Baseline before | Candidate | Baseline after |
|---|---:|---:|---:|
| Low-rate upload, estimated Mbit/s | 0.165 | 0.275 | 0.174 |
| Low-rate whole-report bounds, Mbit/s | 0.148–0.181 | 0.230–0.282 | 0.148–0.188 |
| Late upload, estimated Mbit/s | 1.311 | 1.522 | 0.255 |
| Late whole-report bounds, Mbit/s | 1.077–1.588 | 1.219–1.832 | 0.202–0.301 |
| Edge voice RTT p99, ms | 63.9 | 143.6 | 119.5 |
| Hub voice RTT p99, ms | 64.1 | 100.9 | 138.7 |
| Edge/hub maximum receive gap, ms | 65.4/60.3 | 74.1/89.5 | 314.5/313.7 |
| Edge voice loss, % | 0 | 1.766 (25/1416) | 0.417 (6/1438) |
| Hub voice loss, % | 0 | 1.134 (16/1411) | 0.142 (2/1407) |
| Raw edge peer repairs / expirations / AQM drops | 2176 / 0 / 53 | 2060 / 11 / 19 | 2164 / 36 / 26 |
| Raw edge small-queue drops | 0 | 41 | 6 |

The low-rate candidate bounds exceed both baseline upper bounds: an observed
gain in this paired set, not a repeatability claim. Late bounds overlap the
first baseline, while the returning baseline is substantially different; no
exact recovery deadline or general improvement follows. Direct Starlink
upload is 0.524/0.524/0.513 Mbit/s. 5G reaches its 3 Mbit/s offered ceiling
at 2.935/2.990/2.938 Mbit/s, establishing service lower bounds only.

**Decision:** reject promotion because voice loss exceeds 1% and worsens
relative to both baselines. Retain the returning baseline's six consecutive
losses and 314 ms gaps. They also fail their gates; their cause is unknown.
No download, combined-traffic gain or completed policy is claimed.

Unlike preceding sets, both hosts now have CPU/observer wake records during
guarded local TCP activity. Candidate observer maximum wake delay is 0.929 ms
edge / 0.194 ms hub, and maximum aggregate busy CPU is 17.8%/16.7% over
roughly 100 ms intervals. The returning hub has a 32.8 ms observer delay.
Per-thread scheduler statistics are disabled. These observations neither
measure wanbond's own scheduler delay nor exclude a busy individual core.

Both baseline binary hashes, empty overrides, inactive owned timers, original
WAN qdiscs, removed owned firewall rules and operator `raspi5l` policy are
independently verified. Owned runtime reference directories are archived and
content-compared before removal; the verified inactive candidate binaries are
removed on both hosts. Comparison uses 25.767 mobile RX+TX MB, 34.153 through
cleanup including background/management. The enclosing interval of four sets,
their gaps and background is 197.309 MB. These intervals overlap; do not add
them. Evidence is `fresh-pacing-field-upgrade-20261005/`.

**Next diagnosis:** queue drops concentrate in the first four seconds after
bulk starts. The new voice-first model passes its absolute gates three times
on both the candidate and `b444920` using the same updated test driver. It
does not reproduce the field loss and must not be adjusted to claim it does.
Constant-delay loaded voice p99 is 101/130 ms candidate versus 41/44 ms
baseline; varying delay gives 2/500 losses in one candidate direction versus
zero on the baseline. These are separate model observations. Preserve the
field trace and classify queue drops before changing scheduling behavior.

The counter-analysis script initially treated the concentrator as unlabelled
and silently emitted empty results. Actual collected series carry `peer=""`.
The corrected selector uses that label and rejects empty windows; the original
output is retained. Corrected concentrator small-queue drops are 0/0/1 and
expirations 0/0/19. These are peer counters, not useful TCP efficiency ratios.

On unchanged main `15d37f2`, a new public transport/metrics test reproduces
missing class/cause attribution three times for admission, residence deadline
and real-time backlog shedding. Each fixture observes two aggregate drops,
but no detailed counter exists (`stage23-small-queue-classification-red.txt`).
Add classification counters without changing the queue rules; the current
field data cannot establish which small-packet class produced its 41 drops.

The classification replacement passes all three fixtures three times, including
zero-valued series and the aggregate sum. `wanbond_adaptive_small_queue_drops_total`
has `class={realtime,small_tcp}` and `cause={admission,deadline,stale}`. Only
counter storage/export changes; scheduling, deadlines and wire fields are
unchanged. Their first bounded field attribution is recorded below; no
policy improvement follows from adding counters.


### Small-queue attribution in the field — 2026-10-05

**Observed diagnostic source:** `260ae9fcc1bd85e6af28ced014cb487791547b95`,
SHA-256 `45d4f3e3f52c570b69c7493afa277e0cb589758944d52b991e1d376dabd662b0`.
This is the preceding candidate policy with class/cause counters, one
candidate-only round, voice-only preflights and immediate capped direct
references before the same 400 kbit/s → 2 Mbit/s upload. It is an attribution
run, not a new paired performance comparison.

| Guarded local measurement | Diagnostic candidate |
|---|---:|
| Low-rate upload estimate / whole-report bounds, Mbit/s | 0.265 / 0.252–0.280 |
| Late upload estimate / whole-report bounds, Mbit/s | 1.548 / 1.215–1.935 |
| Edge/hub voice RTT p99, ms | 75.4 / 161.3 |
| Edge/hub maximum receive gap, ms | 66.5 / 179.9 |
| Edge/hub voice losses | 2/1411 / 1/1380 |
| Edge/hub consecutive losses | 2 / 1 |
| Edge/hub real-time stale queue drops | 3 / 0 |
| All small TCP, admission and deadline queue drops | 0 |

Every collected peer snapshot has six class/cause series whose sum equals
its aggregate small-queue drops. In the complete edge trace, four real-time
backlog-shedding drops occur 0.866–0.974 seconds after TCP starts, outside
the guarded active window; three more occur at 15.176–15.284 seconds,
shortly after the rate upgrade. The two guarded missing edge echoes were
sent at 15.236/15.256 seconds, inside that latter local snapshot interval.
The hub loses one echo and has no local queue drops. This establishes
class/cause and a local timing correlation; it does not identify the lost
leg or prove that all seven drops were voice. `realtime` also includes other
small non-TCP traffic. It cannot retrospectively classify the preceding
candidate's 41 drops.

Immediate direct uploads are 0.521 Mbit/s Starlink and 2.988 Mbit/s 5G at a
3 Mbit/s offered ceiling; the latter is a lower bound. The same policy now
has much lower voice loss than the preceding trial but still exceeds
150 ms hub p99 and gap gates. Retain that variation; no repeatability,
download gain, all-metric improvement or promotion is established.
Observer maximum wake delay is 0.974/0.200 ms and maximum aggregate busy CPU
18.2%/20.5% during guarded local activity. Scheduler statistics are disabled;
these observations do not measure wanbond's own scheduler delay.

Both deployed binary hashes, empty runtime overrides, absence of owned
timers/rules, original WAN qdiscs and the initial `raspi5l` exit policy are
independently verified restored. Owned edge runtime references are archived,
content-compared and removed; both verified inactive candidate binaries are
removed. Mobile RX+TX is 9.665 MB for the diagnostic and 10.678 MB through
cleanup. The enclosing interval of five sets, gaps and background is
237.797 MB. These intervals overlap and must not be added. Evidence is
`queue-cause-field-diagnostic-20261005/`.

**Separate model observations:** the existing standby startup outcome fails
three times on the diagnostic source at 88/100 voice delivery. Added
failure-only snapshots show 12 real-time stale drops, pacing near 25,284 B/s
and measured capacity 41,530 B/s after queue delay returns to zero. That
model does not prove the field's cause. A narrower reproduction fails three
times: after congestion drains, a new qualified 60,000 B/s capacity
observation followed by a sparse receipt leaves pacing at 18,050 B/s.
The current recovery rule reads only the latest receipt and discards the
usable qualified observation. This is an observed estimator/control defect;
its contribution to field loss remains inferred. Replace that recovery rule
with fresh qualified capacity newer than the congestion response, preserving
age and the completed-drain precondition, then rerun service and voice gates.

The complete-ACK-round push experiment is also retained as rejected:
100 Mbit/s cellular discovery improves from 2.09/3.40 to 89.20/82.73 Mbit/s,
but bidirectional noise rises to 158 ms p99, steady utilization falls to
68.8%/78.8%, and upgrade service is 0/600 B/s. Extending every push is not
an accepted correction (`stage23-fresh-pacing-complete-round-push-repeat.txt`).


### Recovery cohort checkpoint — 2026-10-05

Two recovery reproductions fail three times before correction: a sparse
receipt hides a fresh qualified delivery sample, and a receipt arriving after
a congestion response describes traffic sent before that response. The latter
raises pacing from 18,050 to 57,000 B/s without observing service under the
new target (`stage23-pre-response-cohort-recovery-red.txt`). Recovery beneath
an older, larger capacity maximum is also covered using actual delivery
observations (`stage23-qualified-recovery-old-maximum-red.txt`).

The experimental replacement keeps the latest raw sample for congestion
assessment and retains the latest qualified sample for recovery inside the
same capacity model. Each keeps its observation timestamp; the qualified
sample also carries the existing physical attempt's send time. Holding can
recover only from fresh qualified traffic sent after the last response and
only after the acknowledged drain. This replaces the raw-latest-receipt
recovery rule; no synthetic traffic, wire field or constant is added. The
controller still has 8 constants against baseline 31.

All narrow recovery, sample-quality, clock and aging checks pass three times.
Bidirectional-noise service remains at 134 ms RTT p99 with zero losses in
900 sends per direction, and steady utilization remains 94.4%/99.0% with
9.2/16.4 ms queue p90. The formerly slow lane improves to 280,320 B/s but
still fails its 75% service gate against 417,458 B/s available service.
Voice-first constant-delay p99 rises to 83/175 ms, exceeding its gate in one
direction; standby startup remains 88/100 and sole slow-lane voice 77 ms.
These are model observations, not a field improvement or accepted candidate.
Evidence is `stage23-post-response-qualified-recovery-repeat.txt`.

Rejected alternatives are retained: recovery from the largest retained
capacity produces only 36,360 B/s upgrade service because the old maximum's
age hides newer lower service. Replacing raw latest delivery with qualified
latest delivery changes congestion assessment as well; it gives 345,600 B/s
upgrade service but 151 ms noise p99 and 5/500 voice losses in the varying-delay
startup model. Separating the uses fixes the narrow defect, but does not
complete the voice or utilization gates. The relationship between these
model defects and the field's stale real-time drops remains unproven.


### Qualified-cohort field comparison — 2026-10-05

**Observed experimental source:** `0e5de63c219f4f890301370dbb2a891f03e8f808`
(code `568b5a8`), SHA-256
`f14f738dbfe430bd603b7e070358801164ff415ff93ba6c3672987d44806baa1`.
The completed non-privileged gate has 19 bond failures. Frontend's 44 tests,
build, vet, patched-engine tests, formatting and all other Go packages pass;
Nix build passes. This source remains experimental.

The paired field set uses fresh starts, identical pinned host executables,
voice-only preflights (500/500 on both hosts in all three rounds), immediate
capped direct references and the same selective 400 kbit/s → 2 Mbit/s
uplink change. No WAN is blacked out.

| Guarded local measurement | Baseline before | Candidate | Baseline after |
|---|---:|---:|---:|
| Low-rate upload estimate, Mbit/s | 0.240 | 0.267 | 0.152 |
| Low-rate whole-report bounds, Mbit/s | 0.217–0.264 | 0.243–0.275 | 0.137–0.181 |
| Late upload estimate, Mbit/s | 0.282 | 1.338 | 1.395 |
| Late whole-report bounds, Mbit/s | 0.213–0.346 | 1.091–1.651 | 1.114–1.678 |
| Edge voice RTT p99, ms | 45.2 | 79.4 | 56.5 |
| Hub voice RTT p99, ms | 63.5 | 172.9 | 57.4 |
| Edge/hub maximum receive gap, ms | 64.3/73.1 | 69.5/72.8 | 58.0/57.1 |
| Edge/hub voice losses | 0/1424 / 0/1405 | 0/1417 / 1/1398 | 0/1418 / 0/1414 |
| Raw edge peer repairs / expirations / AQM drops | 1792 / 7 / 81 | 1870 / 1 / 21 | 2386 / 3 / 60 |
| Raw edge/hub small-queue drops | 0/0 | 0/0 | 0/0 |

**Decision:** reject promotion. Low-rate bounds overlap the first baseline,
and late bounds overlap the returning baseline. Hub voice p99 exceeds
150 ms and both baseline tails; one hub echo is lost where both baselines
lose none. The first baseline's low late throughput is retained rather than
selected as sole comparison. No repeatable all-metric gain, download gain or
completed stage is established.

Direct receiver first-to-last upload rates are 0.503/0.525/0.527 Mbit/s
Starlink and 2.942/2.938/2.934 Mbit/s 5G at a 3 Mbit/s offered ceiling.
They are contemporaneous references, not proof of identical RF conditions
or uncapped 5G capacity. The candidate's complete local trace has no small
queue drops, and every six-series sum matches its aggregate. Therefore the
remaining large voice tail cannot be assigned to the queue-shedding cause
observed in the preceding diagnostic; its component and the lost echo's
cause remain unknown. Queue residence and path transit need separate
measurements before another scheduling correction.

Candidate observer maximum wake delay is 1.737 ms edge / 0.241 ms hub;
maximum aggregate busy CPU is 21.1%/18.6% over guarded local intervals.
Disabled scheduler statistics and the observer's own wakes do not establish
wanbond scheduling latency or exclude a short busy-core interval. Peer
repair/copy/expiry counters include voice, feedback and other peer traffic;
they are not useful TCP byte efficiency ratios.

Both exact deployed baseline hashes, empty overrides, absence of owned
timers/firewall rules, original WAN qdiscs and the initial `raspi5l` policy
are independently verified restored. Owned edge runtime references are
archived, content-compared and removed; both verified inactive candidate
binaries are removed. Mobile RX+TX is 25.009 MB during comparison and
26.618 MB through cleanup. The enclosing interval of six sets, gaps and
background is 281.832 MB. These intervals overlap; do not add them. Evidence
is `qualified-cohort-field-upgrade-20261005/`.


### Queue-residence measurement preparation — 2026-10-05

The qualified-cohort field set has a 173 ms voice tail with no small-queue
drops. The next component measurement adds passive local queue-residence
histograms to the unchanged baseline policy. Before implementation, the
public transport/collector reproduction fails three times on `a13f6eb`:
two actual first submissions occur after 50/25 ms queue waits, but no
residence metric is exported (`stage23-small-queue-residence-red.txt`).

Counting then passes three times. A real retransmission and a separately
expired queued datagram leave the original two histogram observations
unchanged; six finite buckets retain the correct cumulative counts.
Metrics, monitor and bind tests pass. This is an observation feature, not a
policy estimator or throughput correction. No controller constant or wire
field changes. The full gate and Nix build must pass before handover;
main's full non-privileged gate and Nix build subsequently pass on `d939587`.

### Queue-residence field diagnostic — 2026-10-05

**Observed:** source `dacf50a` is C20 policy with passive residence histograms,
not a promoted policy. After voice-only preflight and immediate direct
references (Starlink 0.528 Mbit/s, 5G 2.091 Mbit/s, receiver first-to-last
timing), one bounded 400 kbit/s-to-2 Mbit/s uplink diagnostic gives:

| Measurement | Edge client | Hub client |
|---|---:|---:|
| Voice RTT p50 / p95 / p99 | 36.7 / 59.1 / 140.8 ms | 35.5 / 81.0 / 163.2 ms |
| Voice loss | 2/1415 (0.141%) | 1/1377 (0.073%) |
| Maximum arrival gap | 92.9 ms | 81.7 ms |
| Local real-time queue-wait p99 upper bound | 5 ms | 1 ms |
| Local real-time queue-wait mean | 0.212 ms | 0.011 ms |
| Opposite echo server handling maximum | 0.313 ms | 0.268 ms |

Histograms are class-level increments in guarded local windows, not
per-echo attribution. The echo measurement is `recvfrom` return to `sendto`
return, joined using the client's unchanged sequence and monotonic stamp;
it excludes pre-receive socket waiting. Neither establishes one-way transit.
The edge has three guarded `realtime/stale` drops, none from other small
classes/causes; the hub has none. Queued drops do not enter residence histograms.

The local kernel shaper observer sees 1,412 samples, 729 nonempty, with up to
8,972 bytes queued during the low-rate period and 17,895 after the upgrade;
the guarded shaper drop delta is 12. Its sample duration p99/max is
4.36/5.26 ms. The lane's congestion threshold reaches 208.3 ms before the
upgrade and 209.4 ms after it. This observes backlog and an inflated allowance;
it does not prove the cause or location of each lost/late echo. CPU observer
wake maxima are 0.639/0.443 ms and aggregate busy maxima 20.9%/18.6%; these
do not measure wanbond's own scheduling delay or exclude a busy individual core.

Low upload is estimated at 0.319 Mbit/s, bounds 0.289–0.330; late upload at
1.272 Mbit/s, bounds 1.116–1.574. This candidate-only diagnostic cannot establish
a gain over a matched baseline. The changing direct 5G reference remains
part of the evidence; the previous field set's reference must not be reused.

The workload completes, but local archive extraction rejects the 22.36 MB
metrics log against the harness's 20 MB member limit. The raw failure and
incomplete comparison flag are preserved. The already captured archive is
recovered locally after validating its exact known basename set and a 25 MB
bound for that sampler file; no workload is rerun. Independent checks verify
both deployed baseline hashes, empty runtime overrides, removed network
changes/timers and operator exit policy `raspi5l`. Owned runtime references
are archived/content-verified/removed and inactive candidate binaries removed.
Mobile RX+TX is 9.506 MB during the run and 10.802 through cleanup, including
background; the enclosing seven-set interval is 344.858 MB. These intervals
overlap and must not be added. Evidence is `queue-residence-field-diagnostic-20261005/`.

### Receipt-period delay qualification reproduction — 2026-10-05

**Inferred from code, then reproduced:** an attempt's `unqueued` flag is
recorded before a capacity push/congestion response but still certifies path
delay and variation when its ACK arrives during a later control period.
Actual submission/physical-ACK fixtures fail three times on `dacf50a`: in
each of active push, draining and a completed newer response, a late receipt
changes unloaded RTT from 40 to 50 ms and threshold from 10 to 85 ms.
Physical queue evidence remains 120 ms. The fresh quiet-cohort control passes.
This proves an experimental estimator defect, not causation of the field tail.

Four existing qualification tests are first restated as submission/ACK
outcomes and pass three times before their mechanism is removed. The first
replacement removes the latched flag, its rate/excess heuristic and one
constant, reusing application-limited delivery cohorts and current control
state. Narrow reproductions pass, and voice-first constant-delay p99 changes
from 83/175 to 89/136 ms, but noisy-link bulk falls from 881,520 to 380,040 B/s
against an 867,845 B/s reference. That sampling choice is rejected. Reclassifying quality at receipt time
also regresses noisy bulk (330–383 kB/s across directions/runs) and introduces
map-iteration dependence while the ACK batch is being released. A separate
100-batch reproduction fails three times because qualification runs before
all physical releases; moving observation after the release loop passes it
three times and produces identical model outcomes. That variant still loses
10/500 constant-delay voice echoes in one direction, keeps a 25.2 ms steady
queue and delivers only 381,960 B/s under noise. It is also rejected. No field
improvement or complete gate is claimed. Evidence is `stage23-delay-receipt-cohort-{red,green}.txt`,
`stage23-delay-qualification-restated.txt` and
`stage23-current-delay-cohort-model-repeat.txt`.

Additional evidence is `stage23-current-receipt-delay-model-repeat.txt`,
`stage23-delay-receipt-release-order-red.txt` and
`stage23-after-receipt-release-delay-model-repeat.txt`. These are defects and
tradeoffs in unaccepted sampling choices, not an accepted policy correction.
The next field control uses C8 behavior plus passive telemetry to measure the
same components on the accepted policy before further estimator changes.

### Baseline component control and overdue originals — 2026-10-05

**Observed:** temporary `38a58f7` runs the accepted C8 policy with passive
telemetry. Voice-only preflight precedes immediate direct references: Starlink
0.523 Mbit/s and 5G 1.117 Mbit/s by receiver first-to-last timing. This changed
5G reference prevents a raw-throughput comparison with the earlier 2.091 Mbit/s
C20 diagnostic. The same bounded shaper procedure gives:

| Measurement | Edge client | Hub client |
|---|---:|---:|
| Guarded voice RTT p99 | 64.1 ms | 59.3 ms |
| Guarded voice loss | 0/1468 | 0/1460 |
| Local real-time queue-wait p99 upper bound | 1 ms | 1 ms |
| Local real-time queue-wait mean | 0.011 ms | 0.017 ms |
| Opposite echo handling maximum | 0.244 ms | 0.152 ms |

There are no guarded local small-queue drops. Shaper backlog peaks at 4,336
bytes, 473/1,462 samples are nonempty and its guarded drop delta is zero.
Sample duration p99/max is 4.36/5.38 ms. The baseline's fixed lane thresholds
are 15.7/18.1 ms, against the C20 diagnostic's maximum above 200 ms. This
controls the component observation without proving a throughput gain or
per-echo path cause. Low upload bounds are 0.156–0.187 Mbit/s (estimate 0.173);
late bounds 0.547–0.833 (estimate 0.731). The runner completes, including
extraction under an explicit 40 MB sampler bound. Both exact deployed baseline
hashes, operator exit, runtime overrides, timers and network restoration are
independently verified. Owned references are archived/content-verified/removed
and inactive instrumentation binaries removed. Mobile RX+TX is 8.375 MB during
the run / 11.701 through cleanup; the enclosing eight-set interval is 428.677
MB including gaps/background. These are overlapping intervals, not additive.
Evidence is `baseline-residence-field-diagnostic-20261005/`.

**Inferred, then tested:** shortening the fixed ranking waits does not repair
C20's 175 ms voice-first model tail. That variant preserves noisy bulk and
steady utilization but remains rejected; source snapshots and three-run
results are retained as `stage23-rejected-feedback-round-ranking-*` and
`stage23-feedback-round-ranking-{red,repeat}.txt`.

A separate public two-transport reproduction drops one real-time original
while delivering later small-TCP traffic and authenticated feedback. The lane
stays live, yet no alternate delivers that original within two ACK cadences.
It fails three times on unchanged C20 and on `b444920`; the first attempt to
compile the fixture is retained separately and is not counted as reproduction.
The prototype replaces the lane-wide suspect/dead predicate for the first
real-time recovery copy with an overdue-original predicate using the existing
two-ACK-cadence interval. It adds no estimator, constant, synthetic probe or
wire field, and preserves one-copy recovery and the bulk repair rules.

The public reproduction then passes three times. Voice-first constant-delay
p99 changes from 83/175 to 120/133 ms with zero loss during the measured early
window; later p99 changes from 41/41 to 59/62 ms. Varying-delay loss remains
2/500 in one direction and zero in the other. Noisy bulk remains 881,520 B/s
with 134 ms voice p99; steady utilization/queue outcomes are unchanged.
These observations improve one failed gate and disclose the other-direction
tail tradeoff. They do not establish all-metric improvement. Field comparison
and the complete gate remain to be measured for this recovery choice.
Evidence is `stage23-late-realtime-{live-lane-red,live-lane-green,b444920-red,fallback-model-repeat}.txt`.

The separately rejected current-receipt delay branch `15f8a01` completes the
non-privileged gate with 22 bond failures; frontend's 44 tests, build/vet,
patched engine, formatting and every other Go package pass. Its Nix build
passes. Main's `38a58f7` Nix build passes; its code is unchanged from the earlier
fully passing main gate. No failed estimator choice is merged into main.

### Overdue-original field comparison rejected — 2026-10-05

**Observed:** temporary source `24e0318` replaces C20's first real-time
recovery predicate with the age of the original. The B/C/B runner completes
all three phases with no cleanup errors, using voice-only preflight and
immediate direct references before each capped uplink run. During the guarded
TCP-active windows:

| Measurement | Baseline before | Overdue-original candidate | Baseline after |
|---|---:|---:|---:|
| Edge-client voice loss | 0/1424 | 17/1413 (1.20%) | 0/1420 |
| Hub-client voice loss | 0/1414 | 15/1395 (1.08%) | 0/1421 |
| Edge-client voice RTT p99 | 56.8 ms | 205.9 ms | 50.1 ms |
| Hub-client voice RTT p99 | 56.6 ms | 204.7 ms | 55.3 ms |
| Late upload payload bounds | 0.140–0.227 Mbit/s | 0.595–0.991 Mbit/s | 0.674–1.056 Mbit/s |
| Immediate direct 5G reference | 2.014 Mbit/s | 1.154 Mbit/s | 1.612 Mbit/s |

The late candidate/returning-baseline throughput bounds overlap, and direct
5G service varies. This does not establish a throughput improvement. The
candidate is rejected for the observed voice result. Its edge records 32
real-time/stale local queue drops and first-submission queue-wait p99 bounded
by 50 ms (mean 1.552 ms); the hub records none and has p99 bounded by 1 ms.
Edge aggregate repair counts are 1875/2113/2346 across B/C/B; these include
voice and other traffic and are not a TCP efficiency measurement. Per-echo
attribution and the cause of local starvation remain unknown.

The complete candidate non-privileged gate has 20 bond failures; frontend's
44 tests, build/vet, patched engine, formatting and other Go packages pass.
Its Nix build passes. Selected outage-model failures are also retained without
claiming regression against C20, which has not received that matched check.
The overdue-original functional reproduction and selected positive models do
not supersede these failed gates or the field observation.

Both exact deployed `b444920` hashes, the `raspi5l` exit, empty runtime service
overrides and removal of owned timers/network changes are independently
verified after cleanup. Owned reference logs are archived/content-verified
before removal, and inactive candidate binaries are removed. Mobile RX+TX is
21.783 MB during the comparison / 37.282 MB through runtime cleanup; the
enclosing nine-set interval is 483.837 MB including gaps/background. These
intervals overlap and must not be added. Evidence is
`late-original-field-upgrade-20261005/`,
`stage23-late-realtime-full-nonprivileged-gate.txt` and
`stage23-late-realtime-candidate-nix-build.txt`. Accepted policy remains
`b444920` / `v0.0.3`; main adds passive telemetry only.

### C8 isolation and fresh-packet priority — 2026-10-05

**Observed:** adding a 4,000-byte service-time queue allowance to the existing
voice-before-bulk model passes three times on the rejected overdue-original
C20 choice: early voice p99 is 49/140 ms with zero loss, and later p99 is
41/41 ms with zero loss. This sensitivity check does not reproduce field
loss. It extends queue allowance only; it does not implement a kernel token
bucket or replay RF conditions. Evidence is
`stage23-field-queue-allowance-finding.txt`.

Isolating the age-based recovery choice on C8 fixes the missing-original
reproduction but fails the existing buffered slow-link rate gate: after five
seconds at 62,500 B/s, both targets remain 170,161 B/s. The unchanged C8 gate
passes three times. Requiring an older physical receipt gap, with or without
the lane-liveness predicate, still fails that rate gate; reject those choices.
The bidirectional-noise model's 65,520 B/s failure occurs identically on
unchanged C8 and is not attributed to these trials. Evidence is
`stage23-c8-{late-realtime-bond-gate,receipt-gap-recovery-repeat,gap-only-recovery-repeat,unchanged-recovery-control}.txt`.

**Inferred, then reproduced:** scheduling pending recovery copies before fresh
small datagrams can consume the surviving lane's slots. A public two-transport
model submits 19 small originals to a lane that loses them, disables that lane,
establishes the alternate and admits a fresh real-time datagram. All 19 older
originals recover, but the fresh datagram waits 26 ms against the documented
20 ms local queue target. This fails identically three times on unchanged C8
and `b444920`. Earlier fixture failures exceed the initial congestion window
and are retained separately; they are not the defect reproduction.

Isolated source `388a6d6` replaces the repair-first scheduling order with fresh
small datagrams, pending repairs, then fresh bulk. It changes no estimator,
constant, copy trigger, wire field or synthetic traffic. Fresh wait becomes
zero in this scheduler-only model, with all 19 older originals recovered,
three times. The complete bond suite passes, as do three repetitions of the
buffered rate/outage, standby startup and voice-lane failure guards. This
establishes a scheduler correction, not an end-to-end latency or throughput
gain. The full non-privileged gate and Nix build pass on the isolated branch;
frontend has 44 passing tests and all root/patched-engine checks pass. Main's
documentation-only Nix build also passes. Field verification remains pending;
accepted policy remains `b444920` / `v0.0.3`.
Evidence is `stage23-{c8,b444920}-original-priority-red.txt`,
`stage23-c8-original-priority-{green,green-measured,bond-gate,full-nonprivileged-gate,nix-build}.txt`.

### C8 fresh-first field comparison inconclusive — 2026-10-05

**Observed:** source `d7f9a01` completes the bounded B/C/B uplink comparison
with no phase cleanup errors. Both voice-only preflights deliver 500/500
packets in every phase; direct uplink references precede the tunnel workloads.

| Measurement | Baseline before | Fresh-first candidate | Baseline after |
|---|---:|---:|---:|
| Direct 5G reference | 1.877 Mbit/s | 1.483 Mbit/s | 1.703 Mbit/s |
| Low upload payload bounds | 0.131–0.162 Mbit/s | 0.138–0.167 Mbit/s | 0.132–0.160 Mbit/s |
| Late upload payload bounds | 0.829–1.296 Mbit/s | 0.795–1.202 Mbit/s | 0.948–1.419 Mbit/s |
| Edge-client voice loss | 0/1430 | 1/1424 (0.070%) | 0/1419 |
| Hub-client voice loss | 0/1395 | 0/1381 | 0/1378 |
| Edge-client RTT p99 | 53.3 ms | 54.7 ms | 57.0 ms |
| Hub-client RTT p99 | 55.6 ms | 50.5 ms | 57.5 ms |

Throughput bounds overlap; the one lower hub-client tail is not a repeatable
field improvement. The missing edge-client echo is not attributed to a local
queue drop: all six small-queue cause/class counters remain zero on both
hosts. Candidate real-time local wait p99 is bounded by 1 ms on both hosts,
with mean 0.011/0.012 ms. The healthy, rate-change workload does not establish
the WAN-failure scheduling benefit reproduced in the model. The candidate
remains unaccepted pending that measurement and remaining gates.

Both exact deployed baseline hashes, operator exit, runtime overrides, timers
and network restoration are independently verified. Owned direct-reference
logs are archived/content-verified/removed and inactive candidate binaries
removed. Mobile-interface RX+TX is 24.512 MB during comparison / 25.356 MB
through cleanup. The enclosing ten-set interval is 532.634 MB including
gaps/background; these intervals overlap and must not be added. Evidence is
`c8-original-priority-field-upgrade-20261005/`. No new best candidate or tag
is claimed.

### Fresh-first WAN-failure measurements remain incomplete — 2026-10-05

**Observed:** three candidate radio blackout collections complete. Their
section 4 checker reports failure on every run: the whole-run arrival-gap
check fails each time, and the second run also records no physical bulk
receipt on the returning satellite lane within the bounded two-second
deadline at the edge. Each run has two failed checks and 19–20 inconclusive
checks; independent idle-latency and payload references were not supplied.
The raw phase latency summaries do not replace those missing references.
Satellite-dark voice has zero missing echoes in all three candidate runs,
but the paired baseline results vary substantially; this does not establish
a repeatable overall improvement or a three-out-of-three scenario pass.

The first paired baseline's host CPU busy median is 85.1%, with a maximum
99.6%; the edge/hub observer wake delays reach 1256/1285 ms, and guest CPU
steal p99 is 51.1/54.7%. The candidate and returning baseline have host busy
medians 83.0/84.3% and guest wake maxima 84/99 and 79/72 ms respectively.
Scheduler statistics are disabled. **Inference:** CPU scheduling interferes
with the comparison; these aggregates do not attribute individual gaps.
Evidence is `stage23-c8-original-priority-lab-radio-{comparison,outage-counters}.json`
and `stage23-c8-original-priority-lab-radio-candidate*-gates.txt`.

A voice-only field baseline then completes ten-second, single-WAN egress
blocks of only UDP to `45.11.171.73:51820`, with verified removal timers and
successful phase cleanup. Both clients deliver 2748/2750 echoes: maximum
arrival gaps are 62/95 ms and whole-stream RTT p99 is 111/96 ms. Immediate
direct upload references are Starlink 0.523 Mbit/s and 5G 1.458 Mbit/s.
These observations establish neither loaded service nor a candidate result.

The subsequent compressed candidate transfer times out before activation,
and the edge is briefly unreachable. On return, its recorded uptime confirms
a reboot during the transfer, and its exact deployed `b444920` hash, empty
runtime overrides, both unshaped WAN queues and both UP paths are observed.
The reboot cause is unknown. No candidate phase ran. Interface counters reset,
so total mobile use for this interrupted set is unknown; the last pre-reboot
sample records 5.468 MB of interface RX+TX since collection began, excluding
later transfer/archive/background traffic. Preserve the incomplete comparison
and its failure log. Evidence is `c8-original-priority-field-blackout-20261005/`.
Accepted baseline remains `b444920` / `v0.0.3`.

### Fresh-first single-WAN field measurement — 2026-10-05

**Observed:** the next `d7f9a01` B/C/B set captures all three measurement
phases: two simultaneous 50 Hz, 160-byte echo streams, 2750 echoes per
direction per phase, with ten-second mobile and satellite egress blocks
separately. Only edge UDP to `45.11.171.73:51820` is blocked; verified removal
timers precede every network mutation. There is no bulk workload in this set.

| Measurement | Baseline before | Fresh-first candidate | Baseline after |
|---|---:|---:|---:|
| Direct Starlink upload, Mbit/s | 0.516 | 0.517 | 0.528 |
| Direct 5G upload, Mbit/s | 1.810 | 2.267 | 1.585 |
| Edge-client lost / 2750 | 17 | 6 | 10 |
| Hub-client lost / 2750 | 19 | 6 | 14 |
| Edge-client maximum consecutive missing | 10 | 4 | 3 |
| Hub-client maximum consecutive missing | 9 | 4 | 2 |
| Edge-client maximum arrival gap, ms | 370.6 | 138.3 | 74.3 |
| Hub-client maximum arrival gap, ms | 339.1 | 80.0 | 82.4 |
| Edge-client RTT p50 / p95 / p99, ms | 37.4 / 53.6 / 68.9 | 34.8 / 52.1 / 73.4 | 37.0 / 56.3 / 96.3 |
| Hub-client RTT p50 / p95 / p99, ms | 34.6 / 55.7 / 102.7 | 36.4 / 50.7 / 69.4 | 38.6 / 55.4 / 101.1 |

Candidate loss and hub p99 are lower than both surrounding baselines, but
other metrics are mixed and the maximum consecutive-loss gate still fails.
The candidate's direct 5G service is higher than both baselines. **Inference:**
this set is promising for recovery scheduling, but neither isolates a policy
gain from varying physical service nor establishes repeatability. No new
accepted candidate or tag is justified.

Guarded local queue drops at the edge are 14/12/16 and at the hub 21/0/8.
The candidate edge has one real-time deadline drop and eleven stale drops;
all other cause/class counters are zero. Candidate first-submission residence
has mean 0.318/0.078 ms at edge/hub, with p99 bounded by 10/5 ms. These
aggregates do not identify individual missing echoes. Repair totals include
voice and feedback and are not a TCP efficiency measurement.

Each phase records successful cleanup. The original runner exits with a
final SSH restoration-verification error during another observed edge reboot;
do not label it a completed run. Phase manifests recover the returning
baseline into `recovered-comparison-state.json`, retaining the original
error. Subsequent independent checks verify both deployed source/hash pairs,
empty runtime overrides, unshaped WAN queues, restored `auto` policy and
absence of the inactive candidate and owned reference directories.
**Operator report:** these were maintenance events during the router update;
power is stable now. Their cause is not independently established. Interface
counter reset prevents total mobile-use accounting; 17.884 MB is observed
only through the last pre-reboot sample, excluding later cleanup/background.
Evidence is `c8-original-priority-field-blackout-locked-20261005/`.

An intervening retry is discarded: a second runner was incorrectly launched
before the first exited. Its existing-timer guard rejects it before mutations;
the first is stopped and owned cleanup verified. Subsequent runners take an
exclusive local lock before SSH; rejection of a second acquisition is tested.

**Observed:** three gigaradio candidate collections have 5/0/3 failed checks
and 20/16/16 inconclusive checks. The middle collection remains inconclusive,
not a pass. Independent idle/payload references were not supplied. Together
with the three failed radio collections, these do not establish either
profile family's three-out-of-three gate. Evidence is
`stage23-c8-original-priority-lab-gigaradio-gate-summary.json`.

**Observed:** source `54053eb` combines fresh-small-first scheduling and the
age-based recovery predicate. The missing-original reproduction passes three
times, but the existing buffered rate-reduction guard fails three times at
170161 B/s after five seconds at 62500 B/s; unchanged fresh-first passes
three times. Reject this combination before field activation. Its Nix build
passes. Traces show capacity 226541 B/s with no remeasurement at the deadline,
against 58276 B/s with one remeasurement on the control. **Inference:** the
changed recovery timing exposes dependence on the inherited reduction and
capacity rules; an additional stage-1 heuristic would not implement the
specified estimator replacement. Evidence is
`stage23-c8-priority-recovery-{red,green-attempt,rate-fall-timeline}.txt` and
`stage23-c8-original-priority-rate-fall-control-timeline.txt`.

### Repeat and physical rate-fall reproduction — 2026-10-05

**Observed:** the next fresh-first C8 B/C/B set completes without reboot or
cleanup errors. Candidate edge/hub losses are 22/21 of 2750, against 12/4
before and 31/42 after. Candidate RTT p99 is 181/167 ms against 149/139 and
198/197 ms, and consecutive missing echoes are 7/8. Immediate direct 5G
references are 3.007/2.207/1.933 Mbit/s at a 3 Mbit/s offer; the first is a
service lower bound. Starlink is 0.499/0.507/0.478 Mbit/s. **Inference:** the
drifting comparison does not isolate or repeat the earlier lower loss and hub
tail. Fresh-first C8 remains unaccepted. Both exact deployed hashes, `auto`
policy, unshaped WAN queues, empty overrides and owned-artifact cleanup are
independently verified. Mobile RX+TX is 0.946 MB during staging, 16.517 MB
during comparison / 17.640 MB through cleanup; the last two intervals overlap.
Evidence is `c8-original-priority-field-blackout-repeat2-20261005/`.

**Observed:** diagnostic instrumentation in the old rate-target test shows
similar delivered bulk and voice one-way delay with and without age-based
recovery. That test is not a TCP-and-echo performance measurement. A separate
physical reproduction retains real modeled TCP and two echo streams: the
low-latency 250000 B/s lane falls to 62500 B/s at 12 s, with a 100 ms buffer,
beside an unchanged 750000 B/s lane. The independent available-payload
reference is 546092 B/s in each direction. The checked deadline window is
[16,17), not the following second used by the initial diagnostics.

Main fails three identical runs at 226800 B/s against the required 409569 B/s,
with voice p99 65 ms and no missing echoes. The existing expired-burst gate
also fails on four datagrams at 12.3 s. This reproduction is committed under
the `adaptivepolicy` progression tag; the old target guard and all existing
gates are preserved.

The unmerged estimator prototype passes the delivery and voice parts in this
case. It independently reproduces repair-first scheduling delay at 26 ms
three times. Source `0c5cf65` preserves its class-pacing update and applies
fresh small → repairs → bulk; wait becomes zero with all 19 originals
recovered, three times. With the new outcome test (`c2d9976`), deadline TCP
delivery is 511200 B/s, voice p99 90 ms, and no echoes are missing. Four
expirations at 12.2 s still fail the burst check. **Inference:** this is a
useful model improvement within a prototype, not a completed scenario or a
field improvement. No estimator rule, wire field or probe traffic is added
by the scheduler correction.

The complete candidate non-privileged gate retains 22 bond failures, including
standby voice startup and slow-lane service. Frontend's 44 tests, build/vet,
patched engine, formatting and all other packages pass. Candidate Nix and
main's full non-privileged gate pass. Evidence is
`stage23-c8-buffered-low-latency-rate-fall-red.txt` and
`stage23-estimator-fresh-priority-{red,green,buffered-rate-fall,full-nonprivileged-gate,nix-build}.txt`.
The operator's bounded-field-trial amendment permits measurement of this
unaccepted prototype while these failures remain recorded. Accepted baseline
and tag remain `b444920` / `v0.0.3`; main's policy is unchanged.

### Estimator plus fresh-priority: three field sets — 2026-10-05

**Observed:** unaccepted source `72fcc9d` (executable SHA-256
`b8e4e971dc51ca9c0689a751ba2b7ba824fa2e03ddfdfa4a0b55610faf9baa73`)
completes three B/C/B field sets. Each phase sends 2750 160-byte echoes per
direction at 50 Hz. Separate ten-second edge-egress blocks affect only
wanbond UDP traffic on 5G and Starlink; independent access remains available.
Voice-only preflight precedes immediate capped direct references and the
tunnel measurement. No bulk transfer runs in these sets.

| Set | Before losses edge/hub | Candidate losses edge/hub | After losses edge/hub | Candidate RTT p99 edge/hub, ms | Candidate maximum arrival gap edge/hub, ms |
|---|---|---|---|---|---|
| 1 | 19/18 | 0/0 | 30/56 | 106.4/98.5 | 118.1/151.1 |
| 2 | 10/5 | 0/0 | 7/7 | 154.2/154.9 | 236.1/232.2 |
| 3 | 52/71 | 0/0 | 8/8 | 138.1/138.2 | 160.1/158.4 |

Zero loss repeats over 8250 candidate echoes per direction. In set 1 all
candidate RTT percentiles improve against both surrounding baselines; in
set 2 candidate RTT p99 worsens against both (edge 131.4/153.0 ms, hub
136.5/144.5 ms). Set 3 candidate p99 improves against both. Every candidate
set fails at least one unchanged 150 ms arrival-gap check. This is a repeated
bounded-workload loss observation, not improvement on all metrics or a
completed scenario. Goodput, cold transfer and efficiency remain unmeasured.

Immediate direct 5G references in B/C/B order are 2.229/2.836/2.319,
3.035/2.471/2.715 and 2.374/2.897/2.047 Mbit/s. These are first-to-last
receiver payload rates under a 3 Mbit/s offer; reaching the offer establishes
a lower bound, not path capacity. Starlink references remain 0.492–0.513
Mbit/s. **Inference:** changing physical service is a confound. Candidate
loss stays zero with both higher and lower immediate 5G references, but the
measurements still do not hold RF conditions constant or localize each stall.

Guarded candidate small-queue cause/class drops and total interactive drops
are zero on both hosts in every set. Local real-time residence p99 is bounded
by 5 ms. Reconstruction of receiver arrivals from local monotonic send
stamps plus measured RTT confirms the maximum-gap failures; candidate
wall/monotonic gap differences are below 0.1 ms. These are aggregate checks,
not packet-level attribution to the WAN or transport. **Next measurement:**
record exact-route direct delay alongside voice, and matched sender/echo
handling for the largest gaps; retain independent clocks and scheduler
uncertainty. Assess capped TCP separately before claiming a service gain.

All three runs retain unchanged edge/hub boot identities and no cleanup
errors. Independent postconditions verify both deployed source/hash pairs,
empty candidate overrides, absent restore/removal timers, unshaped queues,
the operator's `auto` policy and removal of owned inactive candidate binaries
and archived reference directories. Mobile RX+TX including background is
53.631 MB during comparisons, 58.193 MB from their respective comparison
starts through runtime cleanup; those intervals overlap. Staging separately
uses 2.643 MB. Evidence:
`estimator-fresh-priority-field-blackout{,-repeat2,-repeat3}-20261005/` and
`stage23-estimator-fresh-priority-monotonic-gap-audit.json`.

The prototype's 22 default bond failures remain unresolved. Its native Nix
and ARM candidate builds pass; main's policy, accepted baseline and tag are
unchanged. These field observations justify continuing investigation, not
promoting a partially validated source.

### Expiration provenance and recovery research — 2026-10-05

**Observed:** detached instrumentation of the buffered rate-fall model finds
206 expired originals during [12,18), all never observed at the receiver,
each with one physical attempt and RTO at least the 250 ms repair lifetime.
The first captured example has a 510 ms RTO. These expirations are distinct
from local queue drops. The exact traces are retained as
`stage23-c8-expiration-{delivery,timeout}-provenance.{txt,json}`.

A public outcome reproduction loses one original on a 150 ms RTT lane,
then establishes a 20 ms RTT alternate with physical ACK progress before
expiry. Main delivers 0/1 and expires one, three identical runs. An
API-adapted fixture on `f75668e` fails identically three times; compile-error
attempts are retained separately and are not evidence of the defect.
`TestAdaptivePolicyBulkRecoveryUsesAlternateBeforeRepairExpires` remains
under the progression tag. The default gate is not weakened.

An isolated deadline-repair change (`4c86ff5`) recovers the original in
215 ms without expiration, three times, by budgeting alternate RTT and
ACK cadence. Its full default gate instead fails the existing radio wire
utilization requirement at 0.844 against 0.925. Adding raw receipt-gap
evidence reaches only 0.893; filtering with existing reordering allowances
restores utilization but fails the original recovery or rate-reduction
guard. These alternatives are rejected, not field activated. The cadence
variant also still fails the buffered expired-burst check. No deadline rule
is added to accepted policy. Evidence is
`stage23-deadline-recovery-{original-red,green-measured,cadence-green,full-nonprivileged-gate,wire-utilization-red,receipt-gap-green-attempt,reordering-green-attempt,observed-reorder-green-attempt,cadence-buffered-rate-fall}.txt`;
the main reproduction is `stage23-main-repair-deadline-red.txt`.

**Documented research:** [RFC 9002 section 6](https://www.rfc-editor.org/rfc/rfc9002.html#section-6)
separates ACK-based loss detection from a probe timeout. Its time threshold
uses the larger of latest and smoothed RTT after a later packet is
acknowledged; timeout expiry alone does not declare loss. The
[BBR draft, application-limited sampling](https://datatracker.ietf.org/doc/html/draft-ietf-ccwg-bbr-06#section-4.1.1.3)
tracks application limitation through the send pipeline rather than
equating every low delivery sample with low capacity.
**Inference and intended work:** replace the stale delay/loss and capacity
rules with coherent aged evidence, and evaluate loss detection separately
from liveness timeout. These references suggest a model boundary, not proof
that QUIC constants or a congestion controller can be copied into wanbond.
Keep public recovery, jitter, utilization and rate-change counterexamples
together; remove each superseded rule in its estimator change. Preserve the
wire format, demand-driven real-traffic probing and all existing gates.

### Capped field upload and qualified receipt sampling — 2026-10-05

**Observed:** two separate baseline/candidate/baseline upload sets complete
with unchanged boot identities. Voice-only preflight and immediate capped
physical references precede each phase. During two 55-second voice streams,
30-second TCP upload is offered at 3 Mbit/s. Only wanbond UDP egress on 5G
is shaped to 400 kbit/s, then 2 Mbit/s at TCP +15 seconds. Starlink remains
unshaped. Every change has a removal timer; independent checks verify both
original deployed source/hash pairs, `auto` policy, empty candidate overrides,
absent temporary timers/firewall rules and unshaped queues afterwards.

Receiver-relative goodput bounds use certainly contained and possibly
overlapping one-second reports. They do not prove exact adaptation deadlines.
Voice p99 below uses each host's guarded TCP-active interval and local clock.

| Source / phase | Low upload [5,15), Mbit/s bounds | Late upload [25,30), Mbit/s bounds | Voice RTT p99 edge/hub, ms | Voice losses edge/hub in active interval |
|---|---|---|---|---|
| `72fcc9d` set / before | 0.144–0.162 | 1.066–1.580 | 78.2/79.4 | 0/0 |
| `72fcc9d` set / candidate | 0.216–0.268 | 1.146–1.657 | 158.1/153.9 | 0/0 |
| `72fcc9d` set / after | 0.137–0.167 | 1.081–1.540 | 60.7/57.1 | 0/0 |
| `fcb25f1` set / before | 0.137–0.179 | 1.127–1.676 | 62.8/69.1 | 0/0 |
| `fcb25f1` set / candidate | 0.207–0.243 | 1.096–1.551 | 134.7/147.5 | 2/1 |
| `fcb25f1` set / after | 0.225–0.272 | 0.219–0.298 | 134.7/124.3 | 0/0 |

The first source improves low-phase upload against both surrounding baselines,
but worsens voice tails. The second overlaps the returning baseline's low
upload and the preceding baseline's late upload; neither improves all metrics.
Immediate 5G references are 2.965/3.057/2.951 and 2.969/2.835/3.006 Mbit/s,
respectively, under a 3 Mbit/s offer; near-offer results are lower bounds,
not capacity. Starlink references are 0.481–0.519 Mbit/s. Returning baseline
in the second set sends zero 5G bulk originals in both guarded windows;
its 5G target stays 57,787 B/s and its probe count stays one. **Inference:**
scheduling or rediscovery contributes to its low upload; these aggregates
cannot exclude changes in physical service during the tunnel phase.

Candidate low-phase TBF backlog/rate p99 is 145 ms in the first set and
174 ms in the second. These are service-time proxies, not packet wait times:
[TBF's documented latency limit accounts for burst tokens](https://man7.org/linux/man-pages/man8/tc-tbf.8.html).
A quiet 100 ms FIFO model is not an exact replica of that shaper. The second
candidate has three real-time stale-queue drops at the edge, one small-TCP
deadline drop, and local real-time residence p99 bounded by 50 ms; the first
has zero real-time queue drops. Exact echo attribution remains unknown.

**Observed model evidence:** the second source removes the probe-wait age
from queued-demand sampling, retaining it for real-traffic probe initiation.
It uses receipt elapsed time only when cumulative received bytes identify
exactly the new timed highest packet. Ambiguous cohorts retain generation
time so late receipts cannot inflate the estimate. A corrected physically
possible ACK-holding fixture fails three times at 5717 B/s, then passes three
times at 10290 B/s. The earlier factor-nine fixture omitted a packet already
received when its first ACK was generated; that reproduction and claim are
withdrawn, with original logs retained. Unconditionally using receipt time
fails the existing late-receipt guard and is rejected.

The qualified source passes the quiet buffered 2a model three times at
507600 B/s, 119 ms voice p99, no missing echoes and no expiration burst above
three per 100 ms. Its full default gate still fails 23 bond checks; frontend,
build/vet, patched engine, formatting and other packages pass. Native Nix and
ARM candidate builds pass. Source `fcb25f1` executable SHA-256 is
`9d3afa3812514a22782fb91eb6cbd59a2ab0967ffdd4dee7d9d0abb700adfa90`.
No wire format, synthetic probe or additional copy trigger is introduced.

A separate voice-primed upload progression model drops edge 5G service to
400 kbit/s when TCP starts beside standby Starlink. At five seconds main
receives 28,800 B/s against a 36,563 B/s requirement, while the two prototypes
receive 1200 B/s; each result repeats three times. All pass that model's voice
gates. It reproduces modeled upload starvation, not the field's tail-latency
failure. **Intended next work:** investigate lane exclusion, demand sampling
and probe flight budgets using both counterexamples, then judge a revised
policy by paired field measurements. Host-loaded lab timing remains diagnostic.

Mobile RX+TX including background is 28.188 MB for the first comparison,
28.703 MB through runtime cleanup, plus 0.887 MB separate staging; the second
uses 26.321 MB for comparison, 27.892 MB through cleanup, plus 0.843 MB staging.
Comparison and cleanup intervals overlap; these are interface counters, not
SIM billing. Evidence is `estimator-fresh-priority-field-upload-20261005/`,
`estimator-qualified-arrival-field-upload-20261005/`,
`stage23-estimator-ack-holding-physical-{red,green}.txt`,
`stage23-estimator-ack-holding-qualified-{full-nonprivileged-gate,buffered-rate-fall}.txt`
and `stage23-main-primed-standby-upload-final-red.txt`,
`stage23-estimator-primed-standby-upload-red.txt` and
`stage23-estimator-fresh-priority-primed-standby-upload-control.txt`.
No performance source is promoted; b444920 remains the field baseline.

### Receipt endpoint field result and feedback timing — 2026-10-05

**Observed:** unaccepted source `1bc29ef`, executable SHA-256
`837ccef088155942dd8f2063d3d62e747cb6f8c56c13a982c578c4611d26beae`,
completes another 400 kbit/s → 2 Mbit/s B/C/B field upload set. Each phase
sends 2750 echoes per direction. Receiver low-window bounds are
0.137–0.157 / 0.186–0.213 / 0.130–0.166 Mbit/s; the candidate's lower bound
exceeds both baseline upper bounds. Late-window bounds overlap:
1.096–1.622 / 1.223–1.816 / 1.125–1.665 Mbit/s. Guarded TCP-active voice p99
is 47.6/50.8, 154.2/156.7 and 64.9/62.8 ms edge/hub. Whole-run echo losses
are 0/0, 8/8 and 0/1. Candidate hub loses four consecutively and has a 152 ms
arrival gap. Low-rate upload improves in this bounded workload; voice
regresses and the source is not promoted.

New source-bound ICMP samples to the concentrator public IP precede each
phase. Starlink RTT p99 is 40.9/42.3/49.9 ms and 5G 164/98.4/129 ms.
These describe immediate idle conditions, not service during the tunnel
measurement. Capped upload references to the OCI worker take a different
route: Starlink 0.497/0.507/0.507 and 5G 2.967/3.005/2.861 Mbit/s under
1/3 Mbit/s offers. Near-offer rates remain service lower bounds. **Inference:**
the candidate's preceding idle 5G tail does not explain its loaded regression;
physical fluctuations during load still prevent packet-level attribution.

Candidate edge counters show 15 real-time stale-queue drops, 27 expired
originals and real-time local residence p99 bounded by 50 ms; hub real-time
drops are zero, residence p99 at most 1 ms. TBF backlog/rate p99 is 175 ms
in the candidate low window, a service-time proxy, not measured packet delay.
All phases retain both boot identities and complete cleanup. Independent
checks verify deployed b444920 hashes, `auto` policy, removed overrides,
absence of temporary timers/firewall rules and unshaped queues. Owned inactive
binaries and archived reference directories are removed. Mobile RX+TX with
background is 27.743 MB during comparison, 28.834 MB through cleanup,
plus 0.877 MB separate staging; comparison and cleanup intervals overlap.
Evidence is `estimator-cohort-endpoints-field-upload-20261005/`.

This endpoint source passes its new clock-accounting guards three times and
builds natively and for ARM, but fails 24 default bond tests. Correcting the
clock also removes the preceding source's complete quiet 2a pass: payload
is 458400 B/s and voice p99 65 ms with zero loss, but four originals expire
together. Voice-primed upload remains 1200 B/s and one model direction now
misses the 1% loss check. These failures are retained. Untouched `f75668e`
transport, with the current TCP model and API-adapted fixture, fails the quiet
case three times at 81600 B/s and the same four-expiration burst; it fails
primed upload three times at 25200 B/s. Only test harness files differ.
Evidence is `stage23-original-buffered-and-primed-{red.txt,fixture.patch}` and
`stage23-estimator-cohort-endpoints-{full-nonprivileged-gate,buffered-and-primed}.txt`.

A narrower public reproduction suspends bulk at 60 ms despite a known 100 ms
path round trip, available pacing and window room, before feedback could
return. It fails three times. Replacing suspicion based solely on ACK cadence
with aged path-delay/jitter plus cadence fixes that case three times, while
preserving stale physical-feedback expiry. This later liveness source has
no field result yet. The quiet model reaches 504000 B/s but still fails the
expired burst; primed starvation remains. **Intended work:** verify broader
liveness guards, then measure any further proposal in the field. No new
constant, estimator, synthetic probe, copy trigger or wire field is added.
Evidence is `stage23-feedback-due-liveness-{red,green-attempt}.txt`.

Two C8-only diagnostics are rejected: qualifying the quiet alternate's
service leaves primed upload at 28800 B/s; additionally disabling optional
copies reduces it to 13200 B/s. Neither is field activated; their patches and
three-run logs are retained as `stage23-c8-qualified-quiet-lane-*` and
`stage23-c8-no-optional-copy-diagnostic.*`. Copies alone do not explain that
model deficit. Main policy and release remain unchanged; host-loaded lab
latency/goodput is diagnostic and field comparisons determine gains.

### Current-delay field comparison — 2026-10-05

**Observed source:** unaccepted `684b1939dcdb012c9cb046d591cda45c11826386`,
SHA-256 `164f8a7e894d44987813cde5511e944a9674d5f6cc1afbe979fefd8de8c0e7aa`.
The liveness-only revision fails 25 default bond checks; consistent packet-flight
capacity sampling changes that to 22. A public sparse-ACK reproduction then
fails three times: 100/200 ms physical RTTs leave current delay at the 100 ms
minimum, unknown. Five window/congestion guards are restated in a preceding
commit and pass three times before and after replacement. Sampling every timed
physical ACK fixes that reproduction; native/ARM builds and Nix pass, but 25
default bond checks still fail. Quiet upload reaches 508800 B/s with a retained
four-expiration burst; primed upload is zero at the deadline. Evidence is
`stage23-current-path-delay-{red,green-attempt,full-nonprivileged-gate,buffered-and-primed,nix-build,arm-build}.txt`
and `stage23-current-delay-all-mechanisms-restated.txt`.

Its timer-backed 400 kbit/s → 2 Mbit/s B/C/B field comparison completes:

| Observed metric | Baseline before | Candidate | Baseline after |
|---|---:|---:|---:|
| Low-window upload bounds, Mbit/s | 0.135–0.170 | 0.197–0.241 | 0.149–0.188 |
| Late-window upload bounds, Mbit/s | 1.046–1.446 | 1.154–1.759 | 1.069–1.668 |
| TCP-active voice RTT p99, edge/hub ms | 72.3/66.0 | 110.6/157.3 | 64.6/88.2 |
| Whole-phase voice median RTT, edge/hub ms | 37.0/37.7 | 31.7/33.0 | 36.5/34.3 |
| Missing echoes of 2750, edge/hub | 0/0 | 0/0 | 0/0 |
| Whole-phase maximum receive gap, edge/hub ms | 73.7/77.2 | 99.9/83.6 | 94.1/105.9 |

The candidate lower low-upload bound exceeds both baseline upper bounds;
late-window bounds overlap. Median delay improves, loaded p99 regresses, and
promotion is rejected. Bounds use whole receiver reports, not exact deadline
measurements. Immediate direct upload references to the different OCI route
are Starlink 0.493/0.513/0.512 and 5G 2.842/2.980/2.994 Mbit/s under 1/3 Mbit/s
offers; near-offer rates are service lower bounds, not capacity. Source-bound
idle ICMP to the concentrator itself gives Starlink p99 49.8/59.8/42.5 ms,
5G 134/70.6/97.9 ms, with 100 replies per sample. These measurements cannot
establish UDP service throughout the later loaded interval.

The candidate has no stale real-time queue drops and local real-time residence
p99 bounds of 5/1 ms. Guarded expired-original deltas are 8/0 edge/hub, versus
24/1 before; expiry does not alone prove non-delivery. Candidate low-rate TBF
backlog/rate p99 is 150 ms, a service-time proxy rather than packet delay.
Both boot identities remain unchanged. Deployed b444920 hashes, original
`auto` policy, empty overrides, unshaped queues and absence of temporary
firewall rules/timers are independently verified before and after owned runtime
cleanup. Mobile RX+TX including background is 26.980 MB during comparison,
27.833 MB through cleanup (overlapping), plus 0.874 MB separate staging.
Evidence is `estimator-current-delay-field-upload-20261005/`.

**Inferred from code:** the raw current-RTT experiment puts self-queue delay
into rank, departing from the intended unloaded-delay model. Its contribution
to the field tail is unproved. **Intended next correction:** retain unloaded
qualification, removing the settled delivery-rate prerequisite that prevents
the first sparse physical ACKs from refreshing delay. That targeted variant
passes the unchanged sparse-ACK values and window/congestion guards three
times; broader checks and field performance remain pending. No new estimate,
constant, synthetic probe, optional-copy trigger or wire field is added.
The installed baseline remains `b444920` / `v0.0.3`. Host CPU spikes can distort
lab timings; performance acceptance rests on field comparisons.

### Unloaded-delay field result — 2026-10-05

**Observed source:** unaccepted `97f5509df1ce2c567ce201dfe949855a191aca14`,
SHA-256 `057e9f0f58f006cbc8f7c2c111faa4c3576a745ff5bc930483a1f5844d8db0fd`.
It retains unloaded qualification while removing the settled delivery-rate
prerequisite. The sparse-ACK reproduction and window/congestion guards pass
three times. Quiet rate fall passes completely three times at 512400 B/s per
direction, zero voice loss, 66 ms RTT p99 and no expired-burst failure. Primed
upload is 34800 B/s against 36563 required, still failing. The full
non-privileged gate has 24 bond failures; frontend's 44 tests, build/vet,
patched engine, formatting and all other packages pass. Nix/ARM builds pass.
Evidence is `stage23-unloaded-current-delay-*.txt`.

Its timer-backed 400 kbit/s → 2 Mbit/s field comparison completes:

| Observed metric | Baseline before | Candidate | Baseline after |
|---|---:|---:|---:|
| Low-window upload bounds, Mbit/s | 0.144–0.183 | 0.268–0.302 | 0.149–0.184 |
| Late-window upload bounds, Mbit/s | 1.104–1.622 | 1.044–1.572 | 1.100–1.596 |
| TCP-active voice RTT p99, edge/hub ms | 95.3/99.8 | 112.1/95.4 | 57.8/62.4 |
| Whole-phase voice RTT p99, edge/hub ms | 175.4/169.9 | 110.2/85.5 | 58.2/59.4 |
| Missing echoes of 2750, edge/hub | 5/5 | 0/0 | 0/0 |
| Whole-phase maximum receive gap, edge/hub ms | 161.9/153.6 | 89.8/82.8 | 66.0/66.3 |

Low-upload bounds improve against both baselines; late bounds overlap.
Candidate whole-phase loss/tail improve against the first baseline, but the
return baseline has a lower tail, and loaded edge p99 worsens against both.
An improvement across all metrics is not established; no promotion occurs.
Each host's loaded guard uses local TCP timestamps; bounds use whole receiver
reports and do not prove an exact adaptation deadline.

Immediate physical upload references to the different OCI route are Starlink
0.510/0.514/0.504 Mbit/s and 5G 3.010/3.023/3.084 under 1/3 Mbit/s offers;
near-offer values are lower bounds, not capacity. Idle ICMP to the concentrator
has Starlink RTT p99 69.6/59.2/53.8 ms and 5G 180/92.7/198 ms. These adjacent
samples do not establish UDP service throughout the loaded measurement.
Candidate local real-time residence p99 is bounded by 5/1 ms and no stale
real-time drops occur. Guarded expired-original deltas are 8/0 edge/hub,
against 8/2 before and 12/0 after; expiry alone does not prove non-delivery.

Both boot identities remain stable. Deployed b444920 hashes, original `auto`
policy, removed overrides, unshaped queues, absence of temporary firewall
rules/timers and owned inactive runtime-file cleanup are independently
verified. Mobile RX+TX including background is 27.715 MB during comparison,
28.572 MB through cleanup (overlapping), plus 0.773 MB separate staging.
Evidence is `estimator-unloaded-delay-field-upload-20261005/`.

**Intended next work:** check class allocation and optional-copy cost while
preserving voice delivery/deadline outcomes, then measure any resulting
proposal in the field. Existing model failures remain visible; host-loaded
lab elapsed-time metrics cannot establish performance improvement.

### Feedback-round push field result — 2026-10-05

**Observed source:** unaccepted `78127ff3e6d1ffa7c8ed28137aa4cfdb5ac929fc`,
SHA-256 `c6258a390e8d9b58a2857b9dfc7345393d24a3286bcaf97c8beb2f5e10e8a7de`.
One timer-backed 400 kbit/s to 2 Mbit/s B/C/B upload comparison completes:

| Observed metric | Baseline before | Candidate | Baseline after |
|---|---:|---:|---:|
| Low-window upload bounds, Mbit/s | 0.133–0.161 | 0.200–0.252 | 0.149–0.179 |
| Late-window upload bounds, Mbit/s | 1.092–1.603 | 1.133–1.751 | 0.290–0.434 |
| TCP-active voice RTT p99, edge/hub ms | 74.3/81.1 | 127.5/134.6 | 58.2/68.4 |
| Whole-phase voice RTT p99, edge/hub ms | 76.6/80.2 | 119.2/111.8 | 58.6/66.0 |
| Missing echoes of 2750, edge/hub | 0/1 | 0/0 | 0/0 |
| Whole-phase maximum receive gap, edge/hub ms | 80.0/107.3 | 112.2/101.2 | 89.5/95.4 |

Candidate low-upload bounds exceed both baseline bounds. Late bounds overlap
the first baseline and exceed the collapsed return baseline. Loaded and
whole-phase p99 worsen in both directions against both baselines; the source
is not promoted. Bounds use whole receiver reports, not an exact adaptation
deadline. The return baseline still submits 163 bulk originals on 5G in a
guarded late 4.8-second window, compared with 615 before and 628 in the
candidate; its cause is unknown, not a claim of complete lane abandonment.

Immediate direct references on the different OCI route are Starlink
0.493/0.503/0.505 Mbit/s and 5G 2.897/2.077/2.950 under 1/3 Mbit/s offers.
Near-offer samples are service lower bounds; the candidate's 5G reference is
below its offer, so physical conditions are not matched. Idle ICMP to the
concentrator itself has Starlink p99 41.6/45.9/49.0 ms and 5G
137.0/54.6/103.0 ms, with 100 replies per sample. These adjacent samples
do not establish UDP service throughout the loaded phase or isolate the
policy's contribution to its worse field tail.

Candidate local real-time queue residence p99 is at most 1 ms on each host,
with no stale drops; guarded expired-original deltas are 14/0, versus 4/0
before and 1/0 after. Expiry alone does not prove non-delivery. Candidate
low-window TBF backlog/rate p99 is 165 ms, a service-time proxy rather than
measured packet wait, with five drops; the high window has four drops.
Both boot identities stay unchanged and all phase cleanup errors are empty.
Independent checks before and after owned runtime cleanup verify deployed
b444920 source/hashes, original `auto` policy, empty overrides, unshaped
queues and absence of temporary firewall rules/timers. Mobile RX+TX including
background is 25.919 MB during comparison, 26.622 MB through cleanup
(overlapping), plus 0.779 MB separate staging. Evidence is
`estimator-round-push-field-upload-20261005/` under the adaptive evidence root.

Two further local diagnostics are rejected and reverted, never field
activated. Limiting the extended push to its preceding excess-volume budget
regresses cold discovery to 1.85 Mbit/s and leaves primed upload at 2400 B/s.
Removing optional copies with earlier real-time gap repair and class-preserving
repair delivers 5500/5500 but 18 late beyond 150 ms; buffered rate-fall voice
p99 is 369 ms and primed upload 25200 B/s. Each repeats three times.
Evidence is `stage23-round-length-volume-budget-{diagnostic.txt,rejected.patch}`
and `stage23-realtime-gap-no-optional-copy-{diagnostic.txt,rejected.patch}`.

**Intended next check:** derive lower-class flight allowance from the same
queue budget used for voice, replacing the special pre-push class-window
rule, and preserve both delivery and capacity-discovery outcomes before any
new field comparison. This is a hypothesis about the field tail, not its
established cause. `b444920` / `v0.0.3` remains the baseline; all outstanding
model/lab gates stay visible. Field measurements determine performance gains;
local host CPU load cannot establish or refute one.

### Rejected lower-class flight diagnostics — 2026-10-05

**Observed in deterministic virtual time:** the feedback-round source fails
slow-lane voice isolation (38 ms one-way p99 versus the existing 25 ms gate)
and cold standby delivery (90/100 versus at least 97), identically three times.
A queue-only lower-class flight cap fixes buffered rate-fall expiry and gives
52/57 ms primed-upload voice RTT p99, but single-lane bulk discovery falls to
12000 B/s. It fails 24 default bond cases against 18 before; build/vet,
formatting and other Go packages pass. This is rejected, not a field candidate.

Retaining propagation and ACK cadence restores fast two-lane sharing to
802880 B/s but leaves single-lane service at 108000 B/s. Removing the pre-push
holding cap then restores single-lane bulk to 467120/5440800 B/s while raising
voice one-way p99 to 99/94 ms, still failing its unchanged gates. Limiting push
excess while voice reserves capacity gives 5085040 B/s at 34 ms on the fast
single lane, but the slow case stays at 120000 B/s and buffered expiry still
bursts by four. Each diagnostic repeats three times; all are archived and
reverted, with no field activation. None proves an improvement across metrics.

**Inference from code and these counterexamples:** propagation flight cannot
be treated as queue occupancy, while retaining the previous holding flight
allowance can prevent a capacity push from demonstrating more service. Removing
that allowance alone sacrifices voice delay. This does not establish which
mechanism caused the measured field tail or justify another scalar cap.
**Intended next investigation:** reproduce capacity qualification and record
per-lane rejection of fresh real-time submissions, preserving existing outcome
gates, before choosing another policy replacement. Field comparisons remain
the performance reference and installed `b444920` remains the baseline.
Evidence under `/srv/nvme/tmp/wanbond-adaptive-evidence` is
`stage23-realtime-flight-budget-{red,green-attempt,sharing-and-upgrade,go-gate}.txt`,
`stage23-realtime-flight-budget-rejected.patch`,
`stage23-propagation-and-queue-flight-budget-{diagnostic.txt,rejected.patch}`,
`stage23-propagation-flight-without-holding-clamp-{diagnostic.txt,rejected.patch}`,
and `stage23-voice-queue-budget-push-{diagnostic.txt,rejected.patch}`.

### Delivery-qualified delay field rejection — 2026-10-05

**Observed:** source `2d48b75` (code `07368d4`, binary SHA-256
`e06c1d5a9e1a452b847f400472577f27578e643c98d49d3ca12964de03c663dd`)
completed C8/candidate/C8 on stable boots. Low-upload bounds overlap:
0.139–0.171 / 0.140–0.207 / 0.156–0.190 Mbit/s. Late bounds overlap:
1.081–1.597 / 0.914–1.402 / 0.910–1.221 Mbit/s. No clear throughput
improvement is established. Loaded voice p99 is 55.76/55.30 →
119.43/144.91 → 65.66/63.44 ms (edge/hub); all phases receive 2750/2750
echoes per host. Promotion is rejected.

Immediate direct offered-rate tests give Starlink 0.505/0.500/0.512 Mbit/s
and 5G 2.976/2.950/2.973 Mbit/s. Exact-route idle ICMP p99 is
61.0/59.5/42.2 ms on Starlink and 182/67/77 ms on 5G, over 100/101
observed replies; raw packet summaries retain missing replies. These do not
establish equal loaded physical service. Candidate late first-submission
counters place 478 voice originals on 5G versus zero before and three after;
that association does not prove the tail's cause. Local real-time residence
p99 is bounded by 10/1 ms, with no stale drops.

The supplied-rate qualification reproduction passes after the replacement,
and primed upload improves in the model, but the default gate has 20 bond
failures versus 18 before. A separate traced model shows a new ranking
regression: sender and receiver averages differ by 0.0012 B/s at 13 s,
keeping the better lane's delay unchanged for over eight seconds.
**Inferred:** comparing unmatched intervals can preserve a false delivery
deficit after a real delay improvement. Replacing those interval observations
with coherent, aged evidence remains necessary; this trial is not accepted.

Evidence: `estimator-delivery-qualified-field-upload-20261005`,
`stage23-delivery-qualified-delay-full-nonprivileged-gate.txt`, and
`stage23-delivery-qualification-rank-trace.{txt,json}` under
`/srv/nvme/tmp/wanbond-adaptive-evidence`. Native Nix and ARM builds pass;
full scenario and VM profile gates are not proved. Both deployed C8 hashes,
original `raspi5l` policy, empty runtime overrides/timers and clean qdiscs
are independently verified before and after owned artifact removal. Mobile
RX+TX is 26.226 MB for comparison, 26.814 MB through cleanup (nested), plus
1.011 MB staging, including background traffic rather than SIM billing.

**Intended next field isolation:** put only fresh-small-before-repair
scheduling onto C8, including main's passive queue telemetry. This avoids
combining the scheduling correction with the unaccepted estimator rewrite.

### C8 fresh-small priority repeat and TCP tradeoff — 2026-10-05, 23:05 UTC

**Observed:** source `a712bbc` (code `2e88765`, executable SHA-256
`fcf037e91d352c8ed58c33463190fe784ba92e9bf0627a3cceb6310590d80f76`)
repeats the scheduling change from `388a6d6`; those production diffs are
identical. This is another field observation of that policy, not a new
algorithm. The full non-privileged gate and native/ARM Nix builds pass.

The voice-only C8/candidate/C8 sequence completes on stable boots. Each host
sends 2750 echoes per phase, with separate ten-second edge-egress blocks
of only wanbond UDP on 5G and Starlink. Whole-stream measurements are:

| Measurement, edge/hub | C8 before | Candidate | C8 after |
|---|---:|---:|---:|
| Missing echoes | 48/45 | 5/7 | 6/6 |
| RTT p99, ms | 139.14/143.60 | 114.50/130.19 | 145.48/137.31 |
| Maximum arrival gap, ms | 189.08/132.65 | 137.95/128.25 | 133.30/234.53 |
| Maximum consecutive missing | 4/4 | 3/5 | 5/2 |

Candidate p99 is lower than both baselines, but loss is similar to the
return baseline and five consecutive missing echoes still fail continuity.
Immediate direct upload references are Starlink 0.489/0.519/0.506 Mbit/s
at a 1 Mbit/s offer and 5G 2.871/2.946/2.956 Mbit/s at a 3 Mbit/s offer.
Exact-route idle ICMP p99 is 57.8/48.6/57.8 ms on Starlink and
97.6/184.0/174.0 ms on 5G. Every sample has 100 observed replies; raw
transmit/receive summaries retain loss. These references do not prove equal
loaded service or isolate the policy's contribution. There is no bulk
workload in this set. Candidate local voice residence p99 is bounded by
50 ms on both hosts; guarded edge drops are three deadline and nine stale,
and hub drops are zero. Aggregate drops do not identify missing echoes.

**Observed:** matched virtual-time scenarios 1a–1c repeat three times with
identical verdicts. Fresh-small priority improves radio lane-0, direction-0
one-way blackout from failure to pass, but changes two baseline passes to
failures: bidirectional blackout and direction-1 one-way blackout, both with
bulk. The former has zero TCP delivery in seconds 22 and 28, the latter in
second 21. Voice improves, while some bulk progress worsens. A diagnostic
that moves only real-time originals first also fails the selected bulk
gates. TCP-state traces show holes persisting until retransmission timeouts;
**inferred:** whole-class priority alone does not resolve the recovery tradeoff.
No scenario threshold is changed and this policy is not promoted.

Both deployed C8 hashes, original `raspi5l` policy, empty runtime overrides
and timers, and clean queues/firewall are independently verified before
and after owned artifact removal. Mobile RX+TX is 16.292 MB for comparison,
16.965 MB through cleanup (nested), plus 0.772 MB staging, including
background traffic rather than SIM billing. Evidence under
`/srv/nvme/tmp/wanbond-adaptive-evidence` is
`c8-fresh-voice-field-blackout-20261005/`,
`c8-fresh-voice-outage-{baseline.txt,candidate.txt,comparison.json}`,
`c8-realtime-only-selected-outages-diagnostic.txt`, and
`c8-priority-tcp-trace/`. Installed `b444920` remains the accepted baseline.

### Matching-interval delay diagnostic rejected — 2026-10-05, 23:16 UTC

**Observed:** the delivered-rate qualification in prototype `2d48b75` fails
the existing persistent lane-improvement outcome three times: neither sender
places any of its 100 checked originals on the better lane. A Go overlay
replaces its unmatched sender/receiver rate averages with intervals ending
at the same physical acknowledgement, using acknowledged sender bytes and
send times, receiver bytes and receipt elapsed time. It evaluates the current
interval before qualifying delay and adds no threshold, constant or wire field.

That reproduction passes three times, as do the late-receipt, delivery-deficit
and busy-radio checks. Primed upload delivers 50400 B/s against the 48751 B/s
requirement. Buffered rate-fall delivery is 520800 B/s against 546092 B/s
available service, with voice RTT p99 122 ms and no missing echoes, but five
expirations still fail the burst gate. Slow-lane rate increase remains
38760 B/s against 417458 B/s available service. These are deterministic
model measurements, not field performance.

The complete bond diagnostic has 22 failed tests against 20 on its unchanged
predecessor. Three previous failures pass; five previously passing outcomes
fail, including sparse delay, forward/reverse noise service, single-lane
voice, fast-pair voice and voice's preferred lane under load. The choice is
rejected and never field-activated. **Inference:** correcting interval
provenance alone does not establish sound unloaded-delay qualification.
Next, reproduce that qualification with physically paced traffic and
packet-time evidence before choosing another estimator replacement.
The preceding per-ACK and quiet-at-send diagnostics are also unaccepted.
Evidence is `stage23-coherent-rate-window-{delay-diagnostic.txt,
capacity-and-voice-diagnostic.txt,bond-diagnostic-gate.txt,
bond-diagnostic-comparison.json,diagnostic.patch}` under
`/srv/nvme/tmp/wanbond-adaptive-evidence`. No production policy changes.

### C8 demand-step rule removal: measured tradeoff — 2026-10-06, 00:49 IST

**Observed reproduction:** public transports with virtual time, loss-free
375,000 B/s service and 224-byte real-time datagrams increase from 50 to
100 datagrams/s after four seconds. C8 loses ten of the next 500 datagrams
per direction even at 20 ms one-way propagation without jitter. Five
propagation/jitter cases fail identically three times. Test commit `39fb343`
precedes candidate code `8e22dbb`; removal of `boundByDelivery`, its callers
and unused steady-stream predicate delivers all 500 in every case, three
runs, with no queue drops. No estimator, wire or new rule is added;
`control.go` remains at 31 constants versus original `f75668e`'s 32.
**Inferred from code:** recent sparse delivery is used as a startup ceiling
under higher demand. This substantiates that failure mode with actual paced
input, without mutating private estimator state.

**Observed field:** source `f2ea16b36decbb1727a3351e5e1bc7ab552021cc`, binary
SHA-256 `c191ef4562d0ce30651edd6600bcb810f9848fecaca849a00f283d91061cee73`,
was measured between two deployed C8 phases. Each sends 2,750 voice echoes
per host over 55 seconds, with timed ten-second 5G and Starlink UDP-only
interruptions on the edge, separately. No tunnel bulk is offered.

| Metric, edge / concentrator | C8 before | Candidate | C8 after |
|---|---|---|---|
| Lost echoes | 5 / 4 | 0 / 0 | 7 / 3 |
| Median RTT, ms | 36.88 / 38.63 | 31.43 / 31.53 | 35.13 / 33.85 |
| p95 RTT, ms | 70.48 / 61.23 | 49.34 / 48.95 | 54.13 / 55.07 |
| p99 RTT, ms | 124.32 / 107.95 | 121.25 / 111.23 | 121.76 / 116.90 |
| Maximum receive gap, ms | 137.36 / 154.04 | 171.99 / 155.90 | 156.51 / 155.70 |
| Local interactive queue drops | 0 / 9 | 0 / 0 | 10 / 0 |
| Repair/copy submissions, 53.8 s window | 1,805 / 1,740 | 3,390 / 3,385 | 1,760 / 1,684 |
| Mobile RX+TX in that window, MB | 2.846 | 3.961 | 2.800 |

Immediate direct uploads measure Starlink 0.509/0.505/0.504 Mbps and 5G
2.971/3.087/2.981 Mbps at capped 1/3 Mbps offers. These are contemporaneous
service references, not configured capacities; reaching a capped offer is
only a lower bound. All phases complete without cleanup errors or reboots.
The comparison uses 17.685 MB mobile RX+TX, 18.144 MB through cleanup
(nested), plus 0.866 MB staging (separate); background traffic is included.
Independent checks verify restored C8 commit/hash, operator exit policy,
empty overrides, absent timers, original qdiscs and firewall state. Exact
owned inactive binaries/reference directories are removed after verification;
reference contents are archived on the edge. Evidence and analysis scripts:
`/srv/nvme/tmp/wanbond-adaptive-evidence/c8-demand-step-field-blackout-20261006`.

**Observed limitation:** zero loss and lower median/p95 cost 39–41% more
mobile bytes and do not improve all latency tails or receive gaps. No
throughput gain or three-set repeatability is established. The full
non-privileged candidate gate has one failure: jitter-only voice p99 137 ms
against unchanged 130 ms; frontend, build/vet and patched-engine checks pass.
The candidate Nix build passes. The candidate remains isolated and unaccepted;
C8 `b444920` / `v0.0.3` remains the accepted baseline.

**Inferred follow-up:** removing the sparse-demand ceiling also raises the
ordinary-copy budget because that budget uses the pacing target, including
unknown-capacity startup assumptions. The counters show increased copies and
repairs together, not their separate causes. Retain the demand-step outcome
and the failed jitter outcome while separating real queued-traffic service
from redundancy allowance. Fallback copies must follow measured need;
capacity replacement must remove superseded rules and reduce its constants.
Do not promote this deletion by itself or repeat its field set as acceptance.

**Observed rejected diagnostic:** matching sender-prefix and receipt intervals
and dividing delivered bytes by their longer interval, without rate smoothing,
passes selected demand-step and voice checks three times (jitter p99 127 ms),
but the complete bond suite has eleven failing tests across rate drops,
radio service, delay noise, policing, stalls, startup and voice/bulk sharing.
Retaining smoothing still fails low-latency-lane voice at 77 ms against 70 ms.
Neither diagnostic is in the field binary. Logs and overlays:
`/srv/nvme/tmp/wanbond-adaptive-evidence/c8-voice-demand-step-repro`.

**Observed follow-up:** direct ICMP idle p99 in the demand-step field set is
Starlink 57.6/37.6/42.0 ms and 5G 170/71/96.5 ms, before/during/after the
candidate. Service-rate references alone do not establish stable latency;
the lower candidate median/p95 cannot be attributed solely to policy. Raw
ping reports retain transmitted/received counts and loss. Field scheduler
observer maxima are 1.61/3.41/24.68 ms on the edge and 0.85/4.32/1.32 ms on
the concentrator; these are observations, not estimates of network delay.

Before changing ordinary-copy policy, `TestVoiceCopiesHaveRoomOnTheOtherLane`
is restated as `TestVoiceLossDoesNotExceedLatencyBudget` in a separate test
commit. It retains at least 99.9% delivery and at most 0.1% later than
150 ms; only the requirement for at least 95% copied datagrams is removed.
Before and after, three virtual-time runs deliver 5,500/5,500, none later
than 150 ms, maximum 110 ms. Copy counts remain diagnostics. No production
copy behavior changes in this commit.

### Ordinary-copy removal remains insufficient — 2026-10-06, 01:00 IST

**Observed diagnostic:** with the demand-step ceiling removed, disabling the
ordinary copy budget while retaining suspect-lane fallback delivers
5,499/5,500 in the existing 0.4%-loss voice/bulk model, but 19 arrive later
than 150 ms (maximum 247 ms). The unchanged outcome gate allows at most
0.1% late. Giving normal retries their original traffic priority produces
5,500/5,500 but 20 late, maximum 279 ms. Sending an alternate first attempt
when a later physical packet is confirmed and this packet has waited one
measured RTT produces 5,500/5,500 but 16 late, maximum 232 ms. Every result
repeats three times. Demand-step, suspect-fallback and eight-seed takeover
checks pass in these selected runs. None of these diagnostics is shipped or
field-tested. Logs/overlays and `diagnostic-summary.json` are under
`/srv/nvme/tmp/wanbond-adaptive-evidence/c8-voice-demand-step-repro/fallback-only`.

**Inferred next constraint:** ordinary-copy removal needs a better bounded
fallback decision; neither liveness alone, retry priority, nor later-packet
confirmation plus mean RTT meets the retained voice outcome. Preserve these
counterexamples when replacing acknowledgement timing and capacity policy.
Main's non-privileged gate and Nix build pass at `19738ec`; production policy
remains C8, field hosts restored, and the improvement goal remains active.

### Conservative cohort sampler: field tradeoff — 2026-10-06, 01:40 IST

**Observed:** isolated source `b70cb58453823ee20dffaba0da0153d91e28d756`
(code `5e9403b`, ARM SHA-256
`a52c671b263e588d7c4cfef7630ddc00ab83593b5d45fee24340656d4d70cfdb`)
completed capped C8/candidate/C8 upload on stable boots. Each phase first
received all 500 voice preflight echoes per host, then measured each physical
uplink immediately before the tunnel. Upload offered 3 Mbit/s for 30 seconds;
timed shaping affected only edge wanbond UDP on 5G, with a 400 kbit/s
allowance rising to 2 Mbit/s after 15 seconds. No WAN blackout was involved.

| Observed metric | C8 before | Candidate | C8 after |
|---|---:|---:|---:|
| Early receiver goodput bounds, Mbit/s | 0.134–0.159 | 0.183–0.220 | 0.154–0.170 |
| Late receiver goodput bounds, Mbit/s | 1.012–1.476 | 1.083–1.601 | 1.058–1.580 |
| Loaded voice p99, edge/hub ms | 88.82/59.98 | 105.55/119.07 | 60.40/57.40 |
| All voice echoes received, each host | 2750/2750 | 2750/2750 | 2750/2750 |

The early goodput bounds exceed both baseline brackets; the late bounds
overlap. Bounds retain whole receiver reports inside/overlapping the window;
interpolated point estimates are not adaptation-deadline proof. Promotion is
rejected because voice tails increase and default/scenario gates remain
incomplete. Candidate local real-time residence p99 is at most 1 ms on both
hosts, with zero interactive queue drops. Observed field wake delay over
the guarded voice interval is at most 1.70 ms on edge and 3.44 ms on hub;
these observations do not establish the cause of network latency.

Immediate physical references are Starlink 0.507/0.501/0.512 Mbit/s and 5G
2.886/2.975/2.921 Mbit/s at 1/3 Mbit/s offers. They are service lower bounds.
Idle ICMP p99 is 74.3/40.9/46.8 ms on Starlink and 120/71.9/106 ms on 5G;
raw transmitted/received counts are retained, including missing replies.
**Inference:** these idle measurements do not establish equal loaded RF
conditions; the candidate has not established improvement on all metrics.

The new deterministic held-batch reproduction reports 73,500 B/s on
102,900 B/s steady physical service before replacement, three times. A
multi-cohort conservative sampler passes its 90–100% bound and existing
clock/holding, sender-limited, congestion and service checks three times.
The longest-cohort-only diagnostic slows discovery (30 default bond
failures versus 20), so it is rejected. Matching ACK-generation clocks
instead overestimates capacity by 25–43% in two existing physically
consistent tests. A new exact-rate test therefore expresses an unsupported
requirement, not proof that the conservative clock rule is defective.

Final full non-privileged checks fail on the same 20 bond tests as predecessor
`2d48b75`; frontend 44 tests, build/vet, patched-engine checks, formatting and
all other Go packages pass. Native Nix and ARM builds pass. Code and docs
are separate commits on the isolated branch; no policy is merged. A follow-up
using only the immediate cohort for recent congestion evidence reduces one
radio expiry burst from 186 to 45, but voice p99 remains 187/166 ms and the
burst gate still fails, three times; that diagnostic is rejected.

Evidence is `capacity-cohort-window-field-20261006`,
`capacity-cohort-immutable-window-*`, `capacity-cohort-window-full-nonprivileged-gate.txt`,
`capacity-cohort-recent-service-red.txt`, `capacity-cohort-current-service-rate-fall.txt`
and `matched-cohort-clocks/rejected-paired-clock.json` under
`/srv/nvme/tmp/wanbond-adaptive-evidence`. Both deployed C8 hashes, original
`raspi5l` policy, empty runtime overrides/timers and clean qdiscs/firewall are
independently verified after restoration and owned cleanup. Mobile RX+TX is
26.989 MB for comparison, 27.154 MB through cleanup (nested), plus 0.874 MB
staging; totals include background traffic, not SIM billing. Exact owned
reference directories are archived and byte-verified before removal.

**Intended next isolation at this checkpoint:** derive push gain from a bounded queue budget
and the measured feedback round, removing the fixed twofold gain and its
loss-backoff rule; material loss still lowers pacing through the link model.
Reproduce standing queue and discovery outcomes before choosing a field
candidate. All stage/profile gates and repeatability remain required;
installed `b444920` remains the accepted baseline.

### Held-service flight window: incomplete field comparison — 2026-10-06, 02:05 IST

**Observed source:** unaccepted `440676150a0acdabafe6f108fc8dd1b67311869e`,
code `b66aae1`, ARM SHA-256
`a82f5d57dc791c598e968b0a71b8a8c59ce479880f5b4dc65827481e7c6af93b`.
The preceding comment-only test commit retains the steady-path utilization
and queue limits. The production change sizes the flight window from held
service rather than the temporary commanded push rate; it adds no rule,
constant or wire field. The slow steady-path result improves from 91.7%
utilization/69.1 ms queue p90 to 95.8%/15.8 ms, three times. The fast result
is 94.5%/29.8 ms and still fails the unchanged 20 ms queue gate.

The full non-privileged gate fails on 20 bond tests. Relative to the preceding
sampler, five failures resolve and five new service/jitter failures appear;
equal counts do not establish non-regression. Frontend's 44 tests, build/vet,
patched engine, formatting and other Go packages pass. Native Nix and ARM
builds pass. No model, lab or field acceptance is claimed.

The capped field runner completes C8 and candidate phases on unchanged boots,
with 500/500 voice preflight echoes on each host and immediate direct uplink
references before each tunnel workload. Shaping affects only wanbond UDP on
5G: 400 kbit/s rising to 2 Mbit/s after 15 seconds during a 30-second upload
offered at 3 Mbit/s. Each network mutation has its removal timer; binary
restoration is armed before activation. No WAN blackout occurs.

| Observed metric | C8 before | Candidate |
|---|---:|---:|
| Early receiver goodput bounds, Mbit/s | 0.153–0.175 | 0.230–0.298 |
| Late receiver goodput bounds, Mbit/s | 1.116–1.597 | 1.094–1.701 |
| Loaded voice p99, edge/hub ms | 66.22/86.71 | 141.10/136.81 |
| All voice echoes received, each host | 2750/2750 | 2750/2750 |
| Low-rate shaper backlog service time median/p99, ms | 22.02/131.70 | 63.78/156.50 |
| High-rate shaper backlog service time median/p99, ms | 6.06/81.67 | 1.47/55.15 |
| Guarded low/high-rate edge voice p99, ms | 45.43/87.81 | 165.19/42.65 |

Early goodput bounds exceed the first baseline's; late bounds overlap.
Low-rate voice tails worsen, while high-rate tails improve. Backlog/rate is
a service-time proxy, not a measured packet delay. This correlation does not
prove the cause of the voice tail. Candidate local real-time residence p99 is
bounded by 1 ms on both hosts, with zero interactive queue drops. Guarded
expired-original deltas are 3/0 edge/hub versus 0/0 before; expiry alone
does not establish non-delivery. Existing repair counters include optional
copies, so their increases cannot be attributed to timeout repair alone.

**Measurement limitation:** after the first baseline, the runner fails because
the source-matched model result file is not yet available; no candidate has
activated at that point. Independent restoration is verified before resuming
only the remaining phases, retaining the initial mobile counters. After the
candidate completes and C8 is restored, the conservative traffic guard holds
the final baseline at 21.57 MB consumed. The comparison stays incomplete;
there is no return-baseline bracket or three-run field proof. Promotion is
rejected, not inferred from the improved subperiods.

Immediate direct references are Starlink 0.493/0.491 and 5G 3.040/2.834 Mbit/s
at 1/3 Mbit/s offers. Their first-to-last receiver intervals give service
lower bounds, not ceilings. Idle ICMP p99 is Starlink 74.6/48.0 and 5G
134.0/88.5 ms; transmitted/received counts are 101/100 for both baseline
links, 101/100 and 101/101 for the candidate. **Inference:** adjacent direct
measurements cannot establish equal loaded RF conditions throughout upload.

Both deployed C8 hashes, original `raspi5l` exit policy, empty runtime overrides
and timers, clean qdiscs/firewall and removal of the exact inactive candidate
are independently verified. Two owned reference directories are archived and
byte-verified before removal. Mobile RX+TX is 21.570 MB through comparison,
22.168 MB through cleanup (nested), plus 0.779 MB staging (separate), including
background traffic. Evidence is `observed-service-window-field-20261006`,
`observed-service-window-{bond-gate,full-nonprivileged-gate,nix-build,arm-build}.txt`
and `observed-service-window-outcomes.txt` under
`/srv/nvme/tmp/wanbond-adaptive-evidence`.

Further model diagnostics are retained rather than promoted. Removing the
fixed wait after ACK-proven drainage raises cold discovery from 5.17 to
52.18 Mbit/s (still below 75 required), while recovered capacity reaches
98.36 Mbit/s. It leaves a 29.04 ms fast standing queue and fails radio-jitter
and voice-isolation checks, three times. Substituting measured feedback delay
minus queue delay in the flight window collapses fast utilization to 1.1%
and slow utilization to 85.1%, three times. Evidence is
`ack-drained-probe-{outcomes.txt,rejection.json,rejected.patch}` and
`physical-feedback-window-{outcomes.txt,rejection.json,rejected.patch}`.

**Observed isolation finding:** fixing the minima leaves the fast standing
queue unchanged at 29.835 ms/94.5% utilization, three times; slow utilization
instead falls to 85.1%. Floor aging is therefore not necessary for this
reproduction. This does not disprove section 6's separate risk on other inputs
or justify adding a periodic drain. Evidence is
`fixed-floor-isolation-{outcomes.txt,finding.json,rejected.patch}`.

**Intended next isolation:** compare the flight window's minimum 25 ms ACK
allowance with measured packet-count-triggered feedback cadence, and distinguish
capacity/control effects from copy allocation before another bounded field trial. Preserve service,
voice and queue counterexamples together. Installed `b444920` remains the
accepted baseline; the improvement goal remains active.


### Observed ACK cadence: field gain with voice-tail cost — 2026-10-06, 02:31 IST

**Observed source:** unaccepted `d1b3f74c7c8ec60b7dc1c6388d45908277ba0ce4`,
code `9c2ba94`, ARM SHA-256
`5572859873325ab3e8cb19a782546e895bf978665f8aec5dd6a536933a516ee6`.
It removes the ACK timer minimum from the held-service flight window and
uses measured cadence. The existing steady-path outcomes pass three times:
fast utilization 96.2%, queue p90 17.856 ms; slow 95.8%/15.774 ms.
The queued-receipt guard is first restated in separate commit `8f6c1d6`,
retaining low service and adding larger-flight coverage with submitted packets.
It passes three times before production changes and fails three times under
the queued-delay qualification mutant, proving sensitivity.

The full non-privileged gate fails on 22 bond tests, versus the predecessor's
20: steady queue and moderate-jitter service resolve; unmatched queued receipt,
random-loss discovery, single-slow-lane voice and ACK-stream sharing newly fail.
All other components pass, including frontend 44 tests; native Nix and ARM
builds pass. Radio buffered rate fall still fails three times at 173/179 ms
voice p99, a zero-delivery deadline second and six expirations in a burst.
No stage/profile acceptance is established.

One C8/candidate/C8 field set completes on unchanged boots, with immediate
physical references and 500/500 preflight echoes per host before each workload.
The bounded source-matched gate is recorded before staging starts, avoiding
the previous missing-file interruption. The workload is unchanged: 55 seconds
bidirectional voice and 30 seconds upload offered at 3 Mbit/s, with only
wanbond's 5G UDP shaped from 400 kbit/s to 2 Mbit/s after 15 seconds. Removal
and binary-restoration timers are verified before their respective changes.
The comparison's admission budget is 45 MB, allowing the return baseline;
no WAN blackout or permanent deployment occurs.

| Observed metric | C8 before | Candidate | C8 after |
|---|---:|---:|---:|
| Early receiver goodput bounds, Mbit/s | 0.130–0.177 | 0.283–0.335 | 0.149–0.184 |
| Late receiver goodput bounds, Mbit/s | 1.014–1.517 | 1.175–1.734 | 1.060–1.592 |
| Loaded voice p99, edge/hub ms | 53.01/50.48 | 84.07/75.31 | 50.72/44.43 |
| All voice echoes received, each host | 2750/2750 | 2750/2750 | 2750/2750 |
| Low-rate shaper backlog service time median/p99, ms | 26.16/115.66 | 49.10/115.66 | 4.14/56.44 |
| Guarded low-rate edge voice p99, ms | 55.84 | 130.03 | 50.91 |

Early goodput bounds exceed both baseline brackets. Late bounds overlap;
within-report interpolation does not prove an adaptation deadline. Loaded
voice tails worsen against both baselines; promotion is rejected. All echoes
arrive, and candidate local real-time residence p99 is bounded by 1 ms on both
hosts, with zero interactive queue drops. Guarded expiry deltas are 5/2
edge/hub, versus 5/0 and 0/0; expiry alone is not proof of non-delivery.
Additional-submission counters include optional copies and cannot establish
timeout-repair cost alone. This is one field set, not three-run repeatability.

Immediate physical references are Starlink 0.510/0.503/0.499 and 5G
2.999/2.985/2.892 Mbit/s at 1/3 Mbit/s offers; these are service lower bounds.
Idle ICMP p99 is Starlink 32.0/40.0/78.6 and 5G 75.6/118.0/51.0 ms. Reply
counts retain missing packets: Starlink 101/100, 101/100, 102/101;
5G 100/100, 101/100, 101/100. **Inference:** adjacent physical references
cannot establish equivalent loaded RF behavior or prove that the policy
causes the tail difference. The field remains the performance reference;
local test elapsed time is not a throughput observation.

Both deployed C8 hashes, original `raspi5l` exit, empty runtime overrides and
timers, clean qdiscs/firewall, and exact inactive-candidate removal are
independently verified. Three owned reference directories are archived and
byte-verified before removal. Mobile RX+TX is 27.260 MB for comparison,
27.769 MB through cleanup (nested), plus 0.777 MB separate staging, including
background traffic. Evidence is `observed-ack-window-field-20261006` and
`observed-ack-window-{full-nonprivileged-gate,nix-build,arm-build,outcomes,implementation-checks,radio-rate-fall}.txt`
under `/srv/nvme/tmp/wanbond-adaptive-evidence`.

**Observed follow-up, not field-tested:** the raw-cadence revision treats one
ACK after idle as fresh evidence while retaining the older cadence value.
A separate reproduction in `9ca6e72` produces windows 12,000/12,250 bytes
for identical fresh paths after 11 seconds idle, three times. Timestamping
actual cadence evidence, expiring it against the existing horizon, and
retaining the protocol maximum ACK allowance fixes that reproduction and
selected queued-receipt/restart/slow-voice outcomes three times. The production
correction is `23f1c45`; complete checks remain pending. Steady-path gains
remain, random-loss discovery still fails. Evidence is `ack-cadence-age-*.txt`.
This corrects an unaccepted experiment, not an observed installed-policy defect.

Reapplying matched sender/receiver intervals on this newer sampler still
fixes persistent lane-improvement selection but regresses sparse delay,
forward/reverse noise service and the slow steady-path queue gate, three times.
That overlay is rejected again; evidence is
`ack-window-coherent-delay-{red,outcomes}.txt`.

**Intended next work:** finish cadence-confidence checks, then correct the
remaining delay-qualification/control defects with the recorded counterexamples
before selecting another field candidate. Installed `b444920` remains the
accepted baseline; the improvement goal remains active.


### Fresh ACK cadence: completed field rejection — 2026-10-06, 02:53 IST

**Observed source:** unaccepted `13661368605118b06b1c41b85bb11508761d0820`,
ARM SHA-256 `3d6998b7ce621a3308729e4b7a259e1fab7904b80e801f36409f651fb8c3d255`.
The cadence-age correction removes stale/unknown feedback spacing from flight
calculation, uses the existing protocol maximum when unknown/aged, and stamps
actual interval evidence. Its fail-first idle-history check passes three
times. Full non-privileged gate has 19 bond failures versus 22 on `d1b3f74`,
with no new failing names; unmatched queued receipt, slow-lane voice and
ACK-sharing outcomes return to passing. Frontend 44, build/vet, patched engine,
formatting and other Go packages pass; native Nix and ARM builds pass.
No wire, synthetic-probe, copy-policy or constant change is introduced.

One complete timer-backed 400 kbit/s → 2 Mbit/s B/C/B upload set records:

| Observed metric | C8 before | Cadence-age candidate | C8 after |
|---|---:|---:|---:|
| Early receiver upload bounds, Mbit/s | 0.220–0.264 | 0.204–0.268 | 0.140–0.176 |
| Late receiver upload bounds, Mbit/s | 0.171–0.267 | 0.876–1.438 | 1.106–1.655 |
| Loaded voice RTT p99, edge/hub ms | 97.5/122.3 | 130.8/152.0 | 59.3/59.0 |
| Missing echoes of 2750, edge/hub | 0/0 | 0/0 | 0/0 |
| Whole-phase receive gap, edge/hub ms | 102.6/103.1 | 95.9/149.9 | 67.8/106.1 |
| Low-rate TBF backlog/service median/p99 ms | 0/0 | 22.0/114.7 | 33.1/85.4 |

Candidate early bounds exceed only the returning baseline, and overlap the
preceding baseline. Late bounds exceed only the preceding baseline and overlap
the returning baseline. Loaded voice p99 worsens against both; one direction
exceeds 150 ms. This result does not establish improvement across metrics, and
the source is not promoted. Bounds use receiver-local reporting intervals and
do not prove exact adaptation deadlines; voice uses each client's own clock.
This is one B/C/B comparison, not three repeats of either profile family.

The preceding C8 phase submits zero 5G bulk originals in its guarded late
window, while 5G carries 478/480 real-time originals. Candidate submits 458
5G bulk originals while 5G carries 464/470 real-time originals. Returning C8
submits 644 5G bulk originals, while Starlink carries 476/480 real-time
originals. These are first-submission counters, not physical TCP receipts.
**Inference:** stale ranking/allocation may contribute to the first baseline's
low upload; these aggregates cannot exclude RF or other service changes.

Direct upload references on the different OCI route are Starlink
0.510/0.518/0.512 Mbit/s under a 1 Mbit/s offer and 5G
2.959/2.929/2.960 Mbit/s under a 3 Mbit/s offer. Near-offer rates are service
lower bounds. Idle ICMP to the concentrator has Starlink p99 37.1/41.9/44.1 ms
and 5G 96.4/133.0/98.6 ms. All samples have 100 replies; transmitted counts
are 101/101/101 for Starlink and 101/100/100 for 5G. The candidate's preceding
5G tail is higher, and adjacent references do not establish service throughout
the loaded interval or identify the cause of its worse voice tail.

Candidate own real-time queue residence p99 is bounded by 5/1 ms, with zero
interactive queue drops. Guarded expired-original deltas are 4/0, versus 9/0
before and 5/0 after. TBF backlog/rate is a service-time proxy, not a measured
packet wait. Both boot identities remain stable. Independent checks verify
deployed C8 hashes, original `raspi5l` policy, removed overrides, unshaped
queues and absence of temporary timers/firewall rules, before and after
archiving/removing only the owned runtime files. Mobile RX+TX with background
is 26.317 MB during comparison, 26.558 MB through cleanup (overlapping), plus
0.775 MB separate staging: 27.333 MB over disjoint measured intervals.
Evidence is `fresh-ack-cadence-field-20261006/` and
`ack-cadence-age-{red,full-nonprivileged-gate,field-source-nix-build,arm-build}.txt`
under `/srv/nvme/tmp/wanbond-adaptive-evidence`.

Two local replacements are rejected without field activation. Advancing pushes
after qualified service gains raises cold discovery from 5.17 to 46.36 Mbit/s
(still below 75), but slow-lane voice falls from 500/500 at 60 ms to 490/500
at 89 ms. Restricting unloaded delay qualification to transit minima fixes
persistent lane choice but loses roughly half the forward/reverse noise-case
voice, and still fails sparse-delay and busy-radio outcomes. Each repeats
three times; source overlays, results and rejected patches are retained as
`measured-progress-probe-*` and `low-transit-delay-*`.

A separate idle-only reproduction cuts prototype pacing from 125,000 to
87,292 B/s without application traffic. Replacing keepalive timing as
delay-driven capacity-cut evidence with timed application receipt passes that
unchanged outcome and twelve selected queue/service/voice outcomes three times.
It is isolated on `adaptive-application-delay-control`, code `e4f1adc`, with
separate documentation; broader checks are running and field gain is unknown.
This change was absent from the field source. No policy is promoted.

**Intended next work:** preserve that idle outcome, reproduce the effect of ACK
batching on productive flight, and replace allowances only if the reproduction
fails for the claimed reason. The [BBR draft-06 §5.5.9](https://www.ietf.org/archive/id/draft-ietf-ccwg-bbr-06.html#section-5.5.9)
measures excess acknowledged volume separately from bandwidth and ages it over
delivery rounds. **Inference:** that separation may replace fixed flight
allowances without confusing radio/ACK variation with standing self-queue;
it is a research direction, not an implemented wanbond estimator or observed
field gain. A revised source still needs paired field measurements against
installed `b444920` / `v0.0.3`; local host elapsed-time metrics remain diagnostic.
