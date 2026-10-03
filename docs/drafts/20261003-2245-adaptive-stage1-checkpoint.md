# Adaptive policy: rejected stage 1 attempt — 2026-10-03

Stage 1 is not proved or installed. The repository retains the stage 0
controller. Stages 2–3 remain unstarted. This checkpoint follows the
[adaptive-policy plan](20261002-1730-adaptive-policy-plan.md); its gates and
operator decisions are unchanged.

## Attempt and provenance

Observed source checkpoint: `0e47629`. The attempt introduced live/suspect/dead
liveness from reported ACK progress, copied suspect real-time traffic without
the copy budget, diverted bulk and removed `stalled` and the silent 0.7 cut.
It changed no wire field. No candidate was installed in the lab or field;
this attempt used zero field-test megabytes.

The attempt is retained under
`/srv/nvme/tmp/wanbond-adaptive-evidence/stage1-attempt-20261003-2245/`:
`stage1-attempt.patch`, the three source files, `manifest.json` and
`evidence-hashes.json`. Observed: the patch applies cleanly to its source
checkpoint. It includes the unsuccessful survivor-sharing experiment below,
not a validated implementation. The preceding scheduler variant is separately
retained as `stage1-schedule-before-survivor-sharing.go` in the evidence root.

The two committed public-transport reproductions in
`internal/bond/liveness_policy_test.go` failed on exact `f75668e` production
source for the expected rate-cut and missing-copy reasons, as recorded in
the [lab record](../../test/vm/README.md). They pass in the attempted
implementation (`stage1-copy-slot-outcomes.txt`). They remain progression
tests behind `-tags adaptivepolicy` in the restored repository.

## Observed outcomes

All model runs below use simulated time and deterministic link inputs.
Host scheduling can change execution duration, but cannot account for their
simulated latency regressions. This differs from the contested VM timing
evidence and makes no claim about the field's RF conditions.

The initial attempt regressed `TestVoiceKeepsLowLatencyLaneUnderLoad`:
500/500 delivered, one-way p99 98 ms, against the unchanged 70 ms gate.
The original controller delivers 500/500 at 52 ms. Traces show some originals
using the jittery lane while the preferred lane is busy; their alternate
copies cannot obtain its pacing slot. Removing the extra restriction against
suspect copy destinations reduced p99 to 93 ms. Allowing a real-time copy to
borrow one lower-class datagram's pacing slot, while charging its bytes to
the pacing clock, brought p99 to 59 ms. The single slow-lane and jittery-lane
checks then also passed at 69 and 119 ms respectively.

The complete bond suite still failed `TestCatchUpRaisesTheEstimate`, schedule
2: bulk 3,031,500 B/s, below 75% of the link's 4,562,500 B/s mean service
(3,421,875 B/s). Its capacity assertion passed. This is an observed goodput
regression, not grounds to weaken or rename its outcome assertion
(`stage1-bond-regression-copy-slot.txt`).

One complete stage 1 model run gave:

| Row | Pass / fail cases | Passing variants |
| --- | --- | --- |
| 1a | 4 / 4 | all voice-only; no bulk variant |
| 1b | 0 / 4 | none |
| 1c | 8 / 8 | all voice-only; no bulk variant |

These are one-run results, not the required three-run proof
(`stage1-model-outage-copy-slot.jsonl`). In particular, radio lane 0 with
bulk now fails 1a, where the corrected original baseline passes. All baseline
passes remain findings; none has been converted into a failing test by
changing its gate.

## Why this attempt stops

Observed in the radio lane 1 outage trace: the surviving lane's targets are
70,514.7 and 47,075.2 B/s in the two directions. The lane remains live. Its
voice reservation is large enough to activate the legacy single-lane
`realtimeLane` restriction, which limits bulk to 5% of its target. Inferred
from `allowed(classBulk)`: in direction 0 that allows about 3,525.7 wire B/s
at the recorded held target, below the required 6,385.5 TCP payload B/s
(75% of the independent 8,514 B/s outage reference), before encapsulation.
This arithmetic uses direction 0; the direction 1 reference is 2,181 B/s.
It is a bound while that restriction is active, not a proof about every
possible controller or probe interval.

A diagnostic experiment removed the single-lane restriction, retaining the
multi-lane voice isolation rule. It still failed the radio outage's progress
and goodput checks. It also regressed `TestVoiceSurvivesOnSingleSlowLane`:
494/500 delivered and one-way p99 116 ms against the unchanged 99% / 75 ms
gate. `TestVoiceCatchesUpAfterTakeover` failed too
(`stage1-survivor-sharing-regression.txt`,
`stage1-survivor-sharing-radio-model.jsonl`). The removal is not retained in
the repository.

Observed separately: in the radio trace, direction 1 TCP delivery stalls
before the blackout, and both lanes are live after return while TCP delivery
remains zero for several seconds. Liveness alone does not establish goodput.
The remaining capacity, reservation and delay rules affect this gate.
Replacing those rules in later stages may address it; that is an inference,
not an observed fix or a reason to skip the required stage 1 proof.

Under the operator's instruction to stop when a section 4 gate cannot be
met with an explained cause, this attempt stops here. No threshold was
relaxed, no extra capacity rule was added, and no stage 2 or 3 implementation
was folded into stage 1. The failed code is preserved for review; production
policy files were restored to `0e47629` before verification.

## Restored checkpoint verification

Observed: the full AGENTS.md non-privileged gate passes, and
`nix build --cores 2 --max-jobs 1` succeeds. Verification is recorded in
`stage1-restored-nonprivileged-v1.txt` and
`stage1-restored-nix-build-v1.txt` in the evidence root. These checks validate
the restored stage 0 source, not the rejected stage 1 implementation.
