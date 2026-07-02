---
ledger: decisions
counters:
  milestone: 0
  item: 1
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
