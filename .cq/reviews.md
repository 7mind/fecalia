---
ledger: reviews
counters:
  milestone: 0
  item: 3
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
