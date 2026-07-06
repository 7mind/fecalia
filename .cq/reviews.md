---
ledger: reviews
counters:
  milestone: 0
  item: 6
archives: []
---

# reviews

## M1

### R1 — revise

- createdAt: 2026-07-01T23:53:32.525Z
- updatedAt: 2026-07-01T23:53:54.119Z
- author: fable-5
- session: 0047802a-1b44-4fcc-8198-d12359610ad6
- summary: "Round 1 (opus + fable panel, strictest-wins): revise. DAG is acyclic, faithful to the decided architecture, phase-gated, and testable against Q1 constants/Q7 metrics; 3 reconciled criticisms to fix (pacing/bufferbloat unowned; runtime path add/remove missing; one spurious dependency edge)."
- new_questions: []
- criticism: ["[opus, fable] Bufferbloat/send-pacing is a prompt-listed KEY RISK ('congestion/bufferbloat (need pacing?)', 'P0 targets most of these') but is unowned: T9's P0-findings scope enumerates six areas that omit it, and neither the active-backup (T15) nor weighted-aggregation (T21) scheduler tasks address pacing, though aggregation and FEC-grouping latency depend on it. Add pacing/bufferbloat to T9's P0 investigation scope AND give the scheduler an explicit pacing sub-goal + acceptance.","[opus] Runtime path add/remove is uncovered: the FUNCTIONAL list specifies 'Path up/down + add/remove' and 'design for N', but T13 implements only the up/down liveness state machine over a config-static path set. Add a task for adding/removing a path from the active set at runtime, with acceptance that a path can be added/removed without disturbing existing paths or the WG session.","[fable] T21 (weighted aggregation scheduler) dependsOn T17 (localhost /metrics HTTP endpoint) is not a genuine prerequisite — the scheduler derives weights from internal per-path telemetry (T13 via T15), not from the HTTP export. Remove the T21→T17 edge and add T17 directly to T23's (P2 e2e) dependsOn so the DAG reflects real prerequisites."]
- ledgerRefs: ["goals:G1"]
- sessionLogs: [".cq/logs/20260701-235345-a7740b6485fe5fb68.md",".cq/logs/20260701-235345-aa548e9af4732b445.md"]
- rawLogs: [".cq/logs/raw/20260701-235345-a7740b6485fe5fb68.jsonl",".cq/logs/raw/20260701-235345-aa548e9af4732b445.jsonl"]

### R2 — revise

- createdAt: 2026-07-02T00:05:08.405Z
- updatedAt: 2026-07-02T00:05:26.876Z
- author: fable-5
- session: 0047802a-1b44-4fcc-8198-d12359610ad6
- summary: "Round 2 (opus + fable panel, strictest-wins): revise. All three round-1 fixes confirmed correctly applied and the DAG is acyclic; both reviewers independently found ONE residual missing edge introduced by the T30 fix."
- new_questions: []
- criticism: ["[opus, fable] T30 (runtime path add/remove) dependsOn [T12,T13] but its acceptance requires scheduler behavior from T15 (active-backup) — 'brings it into the scheduler once probes report healthy and traffic begins using it', and on removal 'the flow continues on the remaining path'. T12/T13 provide sockets + config-static liveness but no send-side traffic steering; that is T15. Without a T30→T15 edge, DAG-ready dispatch schedules T30 concurrently with/before T15, leaving its acceptance unsatisfiable and both tasks editing the scheduler concurrently. Add T15 to T30.dependsOn (acyclic: T15 already depends on T12,T13). Same missing-edge class the round-1 T23→T17 fix corrected."]
- ledgerRefs: ["goals:G1"]
- sessionLogs: [".cq/logs/20260702-000518-a6e2847f4e4ea475d.md",".cq/logs/20260702-000518-a8090f5e41a8e7704.md"]
- rawLogs: [".cq/logs/raw/20260702-000518-a6e2847f4e4ea475d.jsonl",".cq/logs/raw/20260702-000518-a8090f5e41a8e7704.jsonl"]

### R3 — go-ahead

- createdAt: 2026-07-02T00:16:39.702Z
- updatedAt: 2026-07-02T00:17:08.784Z
- author: fable-5
- session: 0047802a-1b44-4fcc-8198-d12359610ad6
- summary: "Round 3 (opus + fable panel): go-ahead (unanimous). R2's sole criticism resolved — T30.dependsOn now [T12,T13,T15]; the T30→T15 edge is present, the DAG remains acyclic (numeric id order is a valid topological order), and no new defect was introduced. Plan approved."
- new_questions: []
- criticism: []
- ledgerRefs: ["goals:G1"]
- sessionLogs: [".cq/logs/20260702-001700-aebb6055cd61166dd.md",".cq/logs/20260702-001700-a89072ddab484d8b1.md"]
- rawLogs: [".cq/logs/raw/20260702-001700-aebb6055cd61166dd.jsonl",".cq/logs/raw/20260702-001700-a89072ddab484d8b1.jsonl"]

## M4

### R4 — go-ahead

- createdAt: 2026-07-06T20:02:43.002Z
- updatedAt: 2026-07-06T20:02:43.002Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- summary: "T8 reconciled go-ahead (opus+fable panel, strictest-wins). Round 1: both disapprove (union of 4 criticisms — daemon exits 0 on unexpected engine death; stale 'wanbond version' doc; factually wrong writeAmnezia comment; e2e log-buffer data race). All 4 fixed in commit 86b0749. Round 2: both approve unanimously, verified against amneziawg-go v1.0.4 source, no regression. Acceptance met on real hardware (o3.7mind.io): TestP0PassThrough handshake+ping+iperf3 through the tunnel, passing functionally and under -race."
- criticism: ["[resolved r2] cmd/wanbond exit 0 on unexpected tunnel death masked failure from supervision — now returns a non-nil error","[resolved r2] docs/manual-checklist.md 'wanbond version' broke when the subcommand was dropped — subcommand restored","[resolved r2] internal/device writeAmnezia comment falsely claimed all-zero keys break the handshake — corrected","[resolved r2] test/e2e/p0_test.go data race on the captured process buffer + cmd.Process.Wait misuse — mutex-guarded buffer + cmd.Wait"]
- new_questions: []
- ledgerRefs: ["tasks:T8","goals:G1"]
- sessionLogs: [".cq/logs/20260706-200109-a1fd7a439122cc6ad.md",".cq/logs/20260706-200109-aa8173f2778caf84c.md",".cq/logs/20260706-200109-ac0148457e0d74922.md",".cq/logs/20260706-200109-a61cae3e31e0f7460.md"]

### R5 — go-ahead

- createdAt: 2026-07-06T20:31:29.490Z
- updatedAt: 2026-07-06T20:31:29.490Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- summary: "T9 reconciled go-ahead (opus+fable panel, 2 rounds). Round 1: both disapprove — opus filed one citation criticism (awg.go magic headers 26-29→28-31) that was VERIFIED FALSE (grep -n confirms 26-29 exact; fable independently confirmed) and was REJECTED, no change; fable filed a valid section-4 D2-gloss criticism (fixed in d4781a6) plus an out-of-scope defect (fixed-sleep iperf3 readiness → D3). Round 2: both approve (opus withdrew its incorrect criticism; fable confirmed the gloss fix). All ~40 amneziawg-go source citations audited exact across the 7 pitfall areas; section-7 pacing verdict judged scientifically honest about the fixture's no-bandwidth-cap limitation. Verified on hardware (o3.7mind.io): TestP0Baseline passes both paths; full P0 e2e suite green."
- criticism: ["[round1 opus, REJECTED-as-false] awg.go magic-header citation 26-29→28-31 — 26-29 is exact, no change","[round1 fable, resolved] section-4 D2 cross-reference gloss was inaccurate — corrected in d4781a6"]
- new_questions: []
- ledgerRefs: ["tasks:T9","goals:G1"]
- sessionLogs: [".cq/logs/20260706-203000-a66924e3eb38ae28b.md",".cq/logs/20260706-203000-a6aa433786a823bc2.md",".cq/logs/20260706-203000-a555730d6a692a960.md",".cq/logs/20260706-203000-ae0c2d5f6a0994fb7.md"]

### R6 — go-ahead

- createdAt: 2026-07-06T20:49:16.281Z
- updatedAt: 2026-07-06T20:49:16.281Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- summary: "T10 reconciled go-ahead (opus+fable panel, 3 rounds). R1: opus approve, fable disapprove with 3 evidence-grounding criticisms (A6 MTU citation ungrounded; A7->T21 overclaimed a unit-test acceptance as fixture-gated; A4 implied hardware confirmed junk-opacity when P0 ran plain WireGuard) — all valid, fixed in 36a9f6e. R2: fable approve; opus disapprove (drafted follow-up still sequenced the fixture before T21, contradicting the corrected body) — fixed in 2ab0fdb. R3: opus approve. Checkpoint verdicts audited against the T11-T30 DAG, T12/T21/T23 acceptance, defects D1/D2, and bind/device source. Gate: GO-AHEAD for P1 (M5) + P3-P5 (M7-M9); GO-AHEAD-WITH-PREREQUISITE for P2 (M6: T23 e2e + T21 empirical pace-sizing need a bandwidth-capped fixture variant, drafted as a /cq:plan:follow-up)."
- criticism: ["[r1 fable, resolved] A6 (MTU) CONFIRMED on an ungrounded findings citation — restated as CARRIED FORWARD, verified by T12 acceptance","[r1 fable, resolved] A7->T21 impact overclaimed (T21 acceptance is unit-level) — rescoped so only T23 e2e + T21 empirical pace-sizing are fixture-gated","[r1 fable, resolved] A4 confirmation implied operational P0 evidence — restated as source-analysis level, soak deferred to T19","[r2 opus, resolved] drafted follow-up closing directive still sequenced the fixture before T21 — corrected to before T23 only"]
- new_questions: []
- ledgerRefs: ["tasks:T10","goals:G1"]
- sessionLogs: [".cq/logs/20260706-204500-a8e8aba6f76f5085b.md",".cq/logs/20260706-204500-a7e6b677426ce0802.md",".cq/logs/20260706-204500-a134692db4129bffa.md",".cq/logs/20260706-204500-ab8cf7484251a3d93.md",".cq/logs/20260706-204500-a9f5d5eb7770fd58d.md"]
