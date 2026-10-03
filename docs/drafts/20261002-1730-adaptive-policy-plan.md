# Adaptive policy plan — 2026-10-02

State: `main` = `f75668e`, deployed on the edge (`pi.mo`) and on `raspi5l`
(both daemons restarted 13:47, idle since). This plan follows
[the improvement plan](20261001-0820-wanbond-improvement-plan.md) and replaces
its item-by-item tuning as the way forward. Nothing here is implemented.

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
(`control.go` has 25 constants and the lane 90 fields). The common cause is
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
on `f75668e`: voice p99 is 136/141 ms and both bulk directions exceed 70%
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

Open: the video gates of row 4 (300 ms gap, 2% loss) are a proposal; and how a
video call is told from a QUIC download.
