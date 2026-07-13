---
ledger: decisions
counters:
  milestone: 0
  item: 7
archives: []
---

# decisions

## M1

### K1 — locked

- createdAt: 2026-07-02T00:17:25.063Z
- updatedAt: 2026-07-02T00:17:44.996Z
- author: fable-5
- session: 0047802a-1b44-4fcc-8198-d12359610ad6
- headline: "wanbond implementation + test-harness plan locked: 8 phase milestones (S,H,P0-P5), 30 tasks"
- rationale: "Multi-planner synthesis (opus + fable candidates) folded into one DAG, approved by the opus+fable reviewer panel after 3 rounds (3→1→0 criticisms; R1/R2 revise → R3 go-ahead). Structure: milestone S (M2) scaffolding/toolchain (git+Go module github.com/7mind/wanbond, Nix flake, golangci-lint+GitHub Actions lint/unit CI, TOML config with role+0600, structured logging) → H (M3) netns/netem two-path harness + Q1 acceptance-threshold constants table + manual-checklist template → P0 (M4) amneziawg-go embed spike behind a portable conn.Bind + baseline + 7-area conn.Bind pitfalls findings (incl. pacing/bufferbloat) + explicit findings checkpoint gating P1 (Q8) → P1 (M5) transparent failover: outer frame codec + PSK control/probe frames, multi-path Bind (per-path sockets, one virtual endpoint, MTU/MSS), probes+liveness, active-backup scheduler, IP-change roaming, runtime path add/remove, systemd+cross-compile, P1 e2e (≤3s recovery) → P2 (M6) /metrics endpoint + resequencing buffer + weighted aggregation with send-pacing/data-thrift, P2 e2e (≥85% bonded, 5G <1%) → P3 (M7) RS FEC engine + datapath integration, P3 e2e (5%/15% loss, ≥95% recovery, ≤2× overhead) → P4 (M8) simulation-tested adaptive controller + live P4 e2e (≤ fixed-FEC baseline, ≤0.5% residual) → P5 (M9) amnezia params end-to-end + entropy/fixed-offset audit + nDPI/Suricata non-classification. Every acceptance is operational (concrete `sudo go test -tags e2e` invocations / Q1 constants / Q7 metrics). Architecture (amneziawg-go + custom conn.Bind) was decided upstream and not re-litigated; test harness is a first-class deliverable per the prompt."
- alternatives: "Two candidate DAGs were generated: opus (7 milestones, W0-combined scaffolding+harness, 29 tasks) and fable (8 milestones, split S/H, simulation-first adaptive controller, 3-task P5, 26 tasks). Synthesis took fable's base (cleaner S/H split, correct frontier/standard/fast tiers, simulation-first P4, fuller dev shell) and folded in opus's explicit per-phase e2e verification tasks and FEC encoder/integration split. Base-library alternatives (kcp-go, quic-go, plain wireguard-go) were rejected upstream in fec-prompt.md."
- landsIn: ["M2","M3","M4","M5","M6","M7","M8","M9"]
- sourceRefs: ["goals:G1","reviews:R1","reviews:R2","reviews:R3"]
- ledgerRefs: ["goals:G1"]

### K2 — locked

- createdAt: 2026-07-06T21:50:17.132Z
- updatedAt: 2026-07-06T21:50:17.132Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: "G1 additive follow-up plan locked: milestone M10 'RH — real-host + impairment validation' (6 tasks T31-T36) + T12/T22 fold-ins"
- rationale: "Multi-planner synthesis (opus + fable candidates) folded into one additive DAG, approved UNANIMOUSLY by the opus+fable plan-reviewer panel round 1 (R8 go-ahead, 0 criticisms). Leg 1 (real-host tier, REPORT-ONLY per answered Q12): T31 realhosts scaffolding (dedicated `realhosts` build tag + SSH runner + env-var host config + Justfile target + connectivity test) -> T32 idempotent provisioning (go/iperf3/gcc) + concentrator tunnel-interface firewall rule -> T33 real-host P0 single-uplink smoke (handshake + ping + iperf3 single/8x/UDP over the real internet) -> T34 real-host multipath/failover via virtual interfaces + policy routing (two 4-tuples through the symmetric NAT; gated behind P1 T12/T15/T20; asymmetric/intermittent links out of scope). Leg 2 (netns fixture, hard-gated): T35 per-path bandwidth-cap + controlled-loss knobs (supersedes the A7/T10 checkpoint follow-up) -> T36 single-flow-TCP-collapse FEC baseline (docs/fec-baseline.md, the pre-FEC reference T25/T29 measure recovery against). Fold-in scope notes: T12 += large SO_RCVBUF + batched send/recv (GSO/GRO best-effort) + the D5 hot-path frame.Codec refactor; T22 += concentrator firewall doc requirement. Synthesis took fable's finer 6-task decomposition (cleaner scaffolding/provisioning split, correct baseline dependency) and folded in opus's advisory cross-plan dependsOn onto phase tasks for correct DAG sequencing."
- alternatives: "Opus candidate: 5 tasks (harness combined scaffolding+provisioning+firewall; baseline dependsOn T25 — INCORRECT, baseline precedes FEC). Fable candidate: 6 tasks (scaffolding/provisioning split; baseline deps fixture only — correct). Synthesis used fable's base + opus's advisory phase-task dependsOn (T33->T8/T9, T34->T12/T15/T20), corrected the baseline to depend on T35 only."
- landsIn: ["M10"]
- sourceRefs: ["goals:G1","reviews:R8"]
- ledgerRefs: ["goals:G1"]

### K3 — locked

- createdAt: 2026-07-08T21:20:01.918Z
- updatedAt: 2026-07-08T21:20:01.918Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: "plan review: approved — hardening round (M11 / T42-T50)"
- rationale: "Reviewer go-ahead R40: re-review confirms all three R39 criticisms resolved with no new defect; 14/14 deferred defects mapped 1:1 to fix tasks, Q14-16 wired. Locks the M11 hardening milestone and its fix-task DAG T42-T50 as the accepted plan."
- ledgerRefs: ["goals:G1"]

## M12

### K4 — locked

- createdAt: 2026-07-13T13:49:25.344Z
- updatedAt: 2026-07-13T13:49:25.344Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: "plan review: approved"
- rationale: "R51 go-ahead (round 1, 0 criticism / 0 new_questions): the M13-M17 / T51-T66 production-readiness DAG is fine-grained, correctly sequenced, testable, grounded, and consistent with the Q17-Q20 answers (CONTROL dormant, multi-concentrator edge-side ordered-endpoint active-standby, non-blocking soak exit, pacing wired-from-declared-bandwidth + documented). This locks that DAG as the accepted plan for G2."
- ledgerRefs: ["goals:G2"]

## M19

### K5 — locked

- createdAt: 2026-07-13T22:11:45.922Z
- updatedAt: 2026-07-13T22:11:45.922Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: "plan review: approved"
- rationale: "Unanimous reviewer go-ahead (R71, 2-reviewer panel round 2): plan for optional DNS concentrator endpoints judged fine-grained, correctly sequenced, testable, grounded, and complete against Q29-Q36."
- ledgerRefs: ["goals:G5"]

## M18

### K6 — locked

- createdAt: 2026-07-13T22:39:38.046Z
- updatedAt: 2026-07-13T22:39:38.046Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: "plan review: approved"
- rationale: "Unanimous reviewer go-ahead (R73, round 2, opus+fable): both R72 criticisms resolved; 20-task DAG across M23-M27 judged fine-grained, sequenced, testable, grounded, complete."
- ledgerRefs: ["goals:G4"]

## M29

### K7 — locked

- createdAt: 2026-07-13T23:39:53.160Z
- updatedAt: 2026-07-13T23:39:53.160Z
- author: "opus-4.8[1m]"
- session: cac93b81-5292-42e3-b77e-962544c75e54
- headline: "plan review: approved"
- rationale: Reviewer go-ahead per R82 (unanimous opus+fable panel, empty new_questions/criticism). Plan M30-M33 / T100-T115 approved; DAG verified acyclic (R80 revise -> R81 revise -> R82 go-ahead). Locking to permit planning->planned.
- ledgerRefs: ["goals:G6"]
