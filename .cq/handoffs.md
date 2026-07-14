---
ledger: handoffs
counters:
  milestone: 0
  item: 18
archives: []
---

# handoffs

## M1

### HO1 — answers-required

- createdAt: 2026-07-01T23:15:52.667Z
- updatedAt: 2026-07-01T23:15:52.667Z
- author: fable-5
- session: 0047802a-1b44-4fcc-8198-d12359610ad6
- summary: "/cq:plan bootstrap: goal G1 (wanbond — 2-WAN bonding tunnel with adaptive FEC on amneziawg-go + custom conn.Bind, implementation + test harness) created under M1 from fec-prompt.md. plan-advance planner filed first clarifying batch Q1–Q8 (verification thresholds, test environment, CI layering, repo/toolchain conventions, config format, deployment scope, observability surface, planning depth). Goal parked in clarifying awaiting user answers; then run /cq:plan:advance G1."
- flow: plan
- ledgerRefs: ["goals:G1"]
- blockingQuestions: ["Q1","Q2","Q3","Q4","Q5","Q6","Q7","Q8"]
- handoffReasons: ["first clarifying round filed; all 8 questions open and gating the goal"]
- sessionLogs: [".cq/logs/20260701-231505-aacec84bd6a7748f4.md"]
- rawLogs: [".cq/logs/raw/20260701-231505-aacec84bd6a7748f4.jsonl"]

### HO2 — mixed

- createdAt: 2026-07-02T00:27:09.306Z
- updatedAt: 2026-07-02T00:27:09.306Z
- author: fable-5
- session: 0047802a-1b44-4fcc-8198-d12359610ad6
- summary: "/cq:advance: investigate (no actionable defects) → plan (G1 fully planned: multi-planner opus+fable synthesis, 3-round opus+fable review 3→1→0 criticisms → go-ahead; decision K1 locked; 8 milestones M2-M9, 30 tasks T1-T30) → implement STOPPED before any task landed. The implement flow cannot execute in this sandbox: dispatching an implement-worker fails with 'Cannot create agent worktree: not in a git repository and no WorktreeCreate hooks are configured' (worktree isolation unavailable, verified twice), and /dev/net/tun is absent so P0-P5 e2e acceptance can't be verified in-sandbox. Repo was bootstrapped (git init + commits). T1 set blocked on Q9 (environment-readiness gate). MIXED: plan landed; implementation blocked on a user environment action + Q9."
- flow: advance
- ledgerRefs: ["goals:G1","tasks:T1"]
- blockingQuestions: ["Q9"]
- handoffReasons: ["drained: investigate stage found no actionable defects; plan stage drove G1 to planned/approved (decision K1; milestones M2-M9; tasks T1-T30)","answers-required: T1 blocked on open question Q9 (environment-readiness gate for implement-flow execution)","user-action-required: implement-worker worktree isolation is unavailable in this sandbox (configure WorktreeCreate/WorktreeRemove hooks in settings.json, OR run /cq:advance from a non-sandboxed git checkout); P0+ e2e acceptance needs real hardware with root + /dev/net/tun (Starlink+5G edge + concentrator VPS)"]
- sessionLogs: [".cq/logs/20260701-234215-a533f3a14c0afe112.md",".cq/logs/20260701-234215-a2ee01f9272ece9de.md",".cq/logs/20260701-235345-a7740b6485fe5fb68.md",".cq/logs/20260701-235345-aa548e9af4732b445.md",".cq/logs/20260702-000518-a6e2847f4e4ea475d.md",".cq/logs/20260702-000518-a8090f5e41a8e7704.md",".cq/logs/20260702-001700-aebb6055cd61166dd.md",".cq/logs/20260702-001700-a89072ddab484d8b1.md"]
- rawLogs: [".cq/logs/raw/20260701-234215-a533f3a14c0afe112.jsonl",".cq/logs/raw/20260701-234215-a2ee01f9272ece9de.jsonl",".cq/logs/raw/20260701-235345-a7740b6485fe5fb68.jsonl",".cq/logs/raw/20260701-235345-aa548e9af4732b445.jsonl",".cq/logs/raw/20260702-000518-a6e2847f4e4ea475d.jsonl",".cq/logs/raw/20260702-000518-a8090f5e41a8e7704.jsonl",".cq/logs/raw/20260702-001700-aebb6055cd61166dd.jsonl",".cq/logs/raw/20260702-001700-a89072ddab484d8b1.jsonl"]

### HO3 — mixed

- createdAt: 2026-07-02T22:11:52.280Z
- updatedAt: 2026-07-02T22:11:52.280Z
- author: fable-5
- session: 0047802a-1b44-4fcc-8198-d12359610ad6
- summary: "/cq:advance (sandbox-implementation run): landed the ENTIRE sandbox-verifiable foundation for wanbond and stopped at the P0/hardware boundary. Because worktree-isolated worker subagents are unavailable this session (Q9), implemented directly in the main checkout, verifying every task with the nix-provided Go toolchain. DONE + VERIFIED + ARCHIVED: S milestone (M2) T1 module/layout, T2 Nix flake (dev shell + static binary), T3 golangci-lint + GitHub Actions CI, T4 TOML config loader (0600 + fail-fast, unit-tested), T5 structured logging (unit-tested); H milestone (M3) T6 e2e layering + Q1 constants + manual checklist, T7 netns/netem two-path fixture (verified via userns, TestFixture green). 7 tasks done, 2 milestones archived, all go build/vet/test/golangci-lint green. STOPPED: T8 (P0 embed amneziawg-go) blocked on Q10 — its acceptance is the real amneziawg tunnel bring-up, which needs /dev/net/tun + root (absent in sandbox, deferred to the user's hardware). MIXED: major work landed; P0+ blocked on Q10 (approach choice) and on host/TUN access."
- flow: advance
- ledgerRefs: ["goals:G1","tasks:T8"]
- blockingQuestions: ["Q10"]
- handoffReasons: ["drained: investigate + plan drained earlier; this run implemented and verified the full sandbox-doable foundation (S+H milestones, 7 tasks, both archived)","answers-required: T8/P0 blocked on open question Q10 (implement sandbox-verifiable P0 code now vs defer all P0 to hardware; + strong recommendation to resume in a fresh session for the proper worker flow)","user-action-required: P0-P5 e2e acceptance needs host root + /dev/net/tun (Starlink+5G edge + concentrator VPS); provide host access to verify the tunnel/scheduler/FEC/DPI phases"]

### HO4 — mixed

- createdAt: 2026-07-06T15:46:44.822Z
- updatedAt: 2026-07-06T15:46:44.822Z
- author: fable-5
- session: 0047802a-1b44-4fcc-8198-d12359610ad6
- summary: "/cq:advance (resume): Q10 resolved toward option (a) by the re-invocation under the standing 'implement what we can in the sandbox' directive; implemented and verified the sandbox slice of P0. LANDED (commit bbdf04a): amneziawg-go v1.0.4 embedded; internal/bind isolates the engine behind type aliases (portability hedge); pass-through conn.Bind implemented over net.UDPConn (NOT the engine's StdNetBind, whose recvmmsg/GSO fast path is brittle under sandbox socket restrictions — a genuine P0 finding for T9); TestPassthroughLoopback round-trips a datagram through Send + receive callback on loopback. go build/vet/test/golangci-lint green; nix build produces the static binary (vendorHash updated). STOPPED: T8's REMAINING acceptance (device+TUN wiring + tunnel e2e TestP0PassThrough) needs host root + /dev/net/tun (absent) — blocked on Q11. Everything downstream (T9-T10 and P1-P5) is gated behind the working tunnel. MIXED: P0 bind slice landed; P0 completion blocked on Q11/hardware."
- flow: advance
- ledgerRefs: ["goals:G1","tasks:T8"]
- blockingQuestions: ["Q11"]
- handoffReasons: ["drained: this run landed the sandbox-verifiable P0 slice (T8 partial: amneziawg-go embed + pass-through Bind + loopback test, all verified)","answers-required: T8 blocked on open question Q11 (hardware gate for the P0-P5 tunnel e2e)","user-action-required: the remaining P0 acceptance (device+TUN wiring + tunnel e2e) and all downstream P1-P5 e2e need host root + /dev/net/tun (Starlink+5G edge + concentrator VPS); a fresh session also restores the proper worktree-isolated multi-agent flow"]

### HO5 — mixed

- createdAt: 2026-07-06T21:34:03.165Z
- updatedAt: 2026-07-06T21:34:03.165Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- summary: "MIXED: major work landed AND now blocked on Q12/Q13. LANDED (done+merged, all opus+fable reviewed, hardware-verified): T8 P0 tunnel bring-up (internal/device + config-driven cmd/wanbond + TestP0PassThrough; verified on o3 incl -race; R4), T9 TestP0Baseline + docs/p0-findings.md (7 source-cited pitfall areas; R5), T10 docs/p0-checkpoint.md P0->P1 gate (A1-A6 confirmed/carried-forward, A7 revised -> P2 needs a bandwidth-capped fixture; R6), T11 internal/frame outer bonding codec (XChaCha20-obfuscated 4 frame kinds, PSK-auth CONTROL/PROBE, Encrypt-then-MAC; R7). Defects D1-D5 all root-caused (amnezia D1/D2->T19; iperf readiness D3; anti-replay D4->T13; hot-path Codec D5->T12). REAL-HOST VALIDATION (user-directed): the P0 tunnel runs over the REAL internet between o3 (concentrator, public 89.168.124.91) and llm-ubuntu-0 (edge, symmetric NAT) -- WG handshake + NAT traversal + ping 29ms + iperf3. Throughput investigation: tunnel carries ~150-170 Mbit/s (=raw path 171-313), NOT CPU-bound (o3 wanbond ~24%); single-flow TCP is loss-x-RTT limited (Mathis) -- exactly the long-fat-lossy-network problem the FEC phases fix. FOLLOW-UP (user-directed): appended a real cross-network two-host e2e tier + controlled-loss/FEC baseline to G1, re-opened to clarifying; planner filed Q12 + Q13. BLOCKED: G1 in clarifying gates ALL P1-P5 implementation (T12+) until Q12/Q13 are answered and the additive scope is planned. NEXT: answer Q12 (real-host tier assertions GATE vs REPORT-ONLY; recommendation report-only) and Q13 (NEW cross-cutting testing milestone vs fold-in; recommendation new milestone -- note H/M3 harness milestone is archived so the fixture extension needs an active milestone regardless) -- both carry recommendations so you can answer 'as recommended' -- then run /cq:plan:advance G1 to emit the additive plan, then /cq:advance resumes P1 (T12 multipath Bind, where the D5 hot-path Codec refactor + SO_RCVBUF/batched-I/O fold-in land)."
- flow: advance
- ledgerRefs: ["goals:G1","tasks:T8","tasks:T9","tasks:T10","tasks:T11","tasks:T12","questions:Q12","questions:Q13"]
- blockingQuestions: ["Q12","Q13"]
- handoffReasons: ["drained","answers-required"]
- sessionLogs: [".cq/logs/20260706-213000-ab3af77c823b6030b.md",".cq/logs/20260706-211500-ae614f805e5cb18d0.md",".cq/logs/20260706-204500-a8e8aba6f76f5085b.md",".cq/logs/20260706-203000-a28d62545377df7ef.md",".cq/logs/20260706-200109-a1fd7a439122cc6ad.md"]

### HO6 — drained

- createdAt: 2026-07-08T02:26:13.230Z
- updatedAt: 2026-07-08T02:26:13.230Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- flow: advance
- summary: |
    DRAINED — /cq:advance drove goal G1's ENTIRE task DAG to quiescence. All THREE predicates FALSE (P-investigate/P-plan/P-implement) and no open questions. ALL 33 TASKS DONE, each adversarially reviewed (opus by-construction + fable, strictest-wins) and hardware-validated on the real testbed where applicable.
    
    SHIPPED THIS RUN (P1→P5 + real-host, all merged to main @ 0d6293e, final full gate GREEN — build/vet/gofmt/`go test ./...`/golangci-lint 0 issues/e2e+realhosts compile):
    • P1 transparent-failover MVP: T40 eager (liveness-driven) failover resolving D18 pull-based-Pick wedge (hardware 42/42 flap + 16/16 single-kill, <3s recovery); T20 failover e2e; T22 packaging (systemd units + static amd64/arm64 cross-compile + install doc + firewall doc); T34 real-host cross-network multipath over the internet through symmetric NAT (1573ms failover, flow survived, host clean).
    • P2 aggregation: T21 weighted-aggregation scheduler + data-thrift (metered 5G idle) with hysteresis + pacing (fable reproduced+fixed a data-thrift idle leak); T23 P2 e2e + live /metrics surface (hardware: concurrent two-path striping + 5G-idle proven; bonded-throughput ratio skip-with-evidence on the CPU-bound single-host fixture, enforced on link-bound venues).
    • P3 fixed FEC: T24 Reed-Solomon datapath integration (fable reproduced 2 datapath faults — decoder GroupID poisoning + late-recovery buffer-dump — + opus MTU overflow, all fixed); T25 P3 loss-recovery e2e (hardware 0.99/0.98 recovery at 5/15% loss; root-caused a cross-netns scrape bug that also strengthened T23).
    • P4 adaptive FEC: T27 hysteretic controller (simulation-tested, fable-verified stable — no chatter/limit-cycle); T29 datapath integration + P4 e2e (hardware: adaptive 0.40 overhead / 0.0000 residual beats fixed 0.60 / 0.0043 at equal masking — the adaptive thesis proven; prefix-consistency of RS parity proven against the reedsolomon source).
    • M9 obfuscation/DPI-resistance (requirement 6): T26 automated wire-format audit (hardware 385k frames, 0 constant offsets, per-offset entropy teeth — fable caught+closed a false-assurance blind spot); T28 nDPI/Suricata non-classification (hardware: obfuscated payload = proto Unknown, positive-control detects real WG — fable reproduced+fixed an nDPI port-guess confound).
    
    The dual-panel strictest-wins discipline caught a real defect in nearly every task before merge (a self-defeating FEC, several vacuous/misdirected measurements, a self-severing e2e, a false-assurance security audit) — all reproduced and fixed. D27 (a pre-existing ~0.8% flaky shared-gate test) was reproduced + fixed out-of-band, de-flaking the gate.
    
    DEFECTS: 14 resolved. 14 root-caused + DEFERRED (file-and-defer; NONE actionable by the autonomous flow without a new goal/plan directive): D3, D4, D7, D8, D10, D13, D14, D20 (earlier-phase deferrals) and D22 (weighted pacer sheds WG control frames), D23 (fixture 150-170 Mbit/s misattribution from T35), D24 (FEC unrecoverable under-reports at quiescence), D25 (adaptive-FEC prefix-consistency rests on an undocumented klauspost default; partial groups untested — MEDIUM, worth pinning), D26 (adaptive default tuning can't meet a sub-1% residual SLA), D28 (just lint omits -tags e2e). These are future hardening; they need a user-created goal or /cq:plan:follow-up to become actionable.
    
    GOAL STATE: G1 is `planned` with every task done; the flow does not auto-complete goals. NOTE: completed milestones M5-M9 cannot be archived while their deferred root-caused defects remain non-terminal (expected). NO further autonomous action is possible — to continue, EITHER create a goal / run /cq:plan:follow-up G1 to action the deferred hardening defects (D25/D22/D14 are the most substantive), OR mark G1 done if the P1-P5 scope is complete as delivered.
- ledgerRefs: ["goals:G1","reviews:R26","reviews:R29","reviews:R30","reviews:R31","reviews:R32","reviews:R33","reviews:R34","reviews:R35","reviews:R36","reviews:R37","defects:D25","defects:D22","defects:D14"]

### HO7 — drained

- createdAt: 2026-07-08T08:03:58.958Z
- updatedAt: 2026-07-08T08:03:58.958Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- flow: advance
- summary: |
    DRAINED (re-confirmed) — this /cq:advance invocation re-derived the ledger and found it unchanged since HO6: all three predicates FALSE (P-investigate/P-plan/P-implement) with no open questions. The intervening /cq:plan:follow-up G1 added NO scope (invoked with an empty remainder → stopped and asked the user, no mutation), so the goal DAG remains fully drained: all 33 G1 tasks done (P1 failover MVP + real-host multipath, P2 aggregation, P3 fixed FEC, P4 adaptive FEC, M9 obfuscation/DPI-resistance) — each opus+fable-reviewed and hardware-validated where applicable; main @ bb0e26e, final full gate GREEN (build/vet/gofmt/go test ./.../golangci-lint/e2e+realhosts compile).
    
    End-of-run sweeps this invocation: (1) MILESTONE archive sweep — nothing eligible: M2/M3 already archived; the coordination milestone M1 is guarded open (goal G1 is `planned`, non-terminal — goals never auto-close); work milestones M4-M10 each still carry non-terminal ROOT-CAUSED deferred defects, which legitimately keep them open pending a fix round. (2) WORKTREE/branch cleanup — removed one merged stale branch (worktree-agent-ae638531e8d8d815d @ be0da9f, T13 review, fully merged); preserved worktree-agent-af77aeeacf7eb7555 @ 6221f9f per the hard safety guard (an unmerged worktree-agent branch with no terminal-task binding is never force-removed — it is the pre-consolidation T40 branch whose work was superseded by the merged eager-failover chain, harmless to keep).
    
    DEFECTS unchanged: 14 resolved, 14 root-caused + DEFERRED (D3/D4/D7/D8/D10/D13/D14/D20 + D22/D23/D24/D25/D26/D28) — none actionable by the autonomous flow (root-caused is excluded from P-investigate; they need a user-created goal or /cq:plan:follow-up to become fix tasks). The most substantive hardening candidates remain D25 (pin the undocumented klauspost RS prefix-consistency property + partial-group test), D22 (pacer sheds WG control frames under overload), D14 (fixture teardown race).
    
    NO further autonomous action is possible. To continue: EITHER mark G1 `done` if the delivered P1-P5 scope is complete; OR add new capability scope via /cq:plan:follow-up G1 <scope text>; OR turn the deferred hardening defects into fix tasks (a defect-driven plan round). Nothing is blocked on an open question or a user environment action — this is a clean drained terminus.
- ledgerRefs: ["goals:G1","defects:D25","defects:D22","defects:D14"]

### HO8 — answers-required

- createdAt: 2026-07-08T08:29:11.944Z
- updatedAt: 2026-07-08T08:29:11.944Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- flow: plan
- summary: |
    ANSWERS-REQUIRED — /cq:plan:follow-up G1 re-opened goal G1 with a NEW 'Deferred-defect hardening round' scope (the 14 root-caused, file-and-defer defects from the P1-P5 build: D3, D4, D7, D8, D10, D13, D14, D20, D22, D23, D24, D25, D26, D28, grouped A-E), transitioned G1 planned→planning→clarifying, and drove the plan-advance planner once. The planner read all 14 defects + the scope and filed THREE clarifying questions that genuinely gate a sound fix-task DAG (it deliberately did NOT ask milestone-placement/task-granularity — the scope delegates those — nor any whether-to-fix/disposition question, which the re-open settles):
    
    - Q14 (Group D, D7/D8 live-o3 scope): fix tasks deliver REPO-SIDE artifacts only — idempotent reboot-persistence provisioning + install-doc + a TestRealProvision assertion — with the one-time live-o3 INPUT-chain dedup + ACCEPT-rule persist tracked as a manual ops step you run (recommended, preserves the never-deprovision-o3 posture + M10 report-only semantics); vs including the live-host mutation in task acceptance.
    - Q15 (D23 doc-sweep): measure-then-sweep — record the 4-vCPU in-fixture ceiling on llm-ubuntu-0 first, then sweep the four comment/doc locations with both real numbers (recommended, so D23 stops propagating an unmeasured figure); vs sweep-now with the 1-vCPU figure + a TBD marker.
    - Q16 (D26 resolution surface): a DOCUMENTED SafetyFactor/target-residual-SLA table (recommended — closes the defect with NO new runtime surface, honoring the round's 'no new product capability' non-goal); vs a new target_residual config knob (a separate feature goal if you want it). The planner pre-committed (subject to override) D25 = partial-m×partial-k byte-exact property test + pin the klauspost prefix-stability guarantee, and D24 = account retained-past-deadline groups at Stats() so quiescence stops overstating recovery.
    
    All three carry a recommendation, so 'as recommended' answers each. NEXT: answer Q14/Q15/Q16 in the TUI/web, then run /cq:plan:advance G1 — the planner will ground, decide milestone placement + granularity, transition to planning, and emit the fix-task DAG (each task linking defects:<D>); then /cq:advance builds them.
    
    TOOLING NOTE (not a wanbond defect): `cq log put` is currently BROKEN in this environment — the installed `cq` binary rejects the repo's cq.toml with 'tiers key "opus" is not a valid tier' (the stale binary expects the older tier-name→tokens [tiers] shape; the committed cq.toml uses the current token→tier-class classifier schema). So this round's planner session logs could NOT be persisted via `cq log put` (the command forbids a direct .cq/logs write). The planner's raw transcript remains at its native path ~/.claude/projects/-home-pavel-work-safe-fecalia/45fdce95-2af6-42cd-8ddd-0c9faabc56ef/subagents/agent-a114cc91b4029009d.jsonl. Recommend updating the `cq` CLI to match the current cq.toml schema (or vice-versa) to restore session-logging + the multi-agent panel config resolution.
- blockingQuestions: ["Q14","Q15","Q16"]
- ledgerRefs: ["goals:G1","defects:D25","defects:D22","defects:D7","defects:D23","defects:D26","defects:D24"]

### HO10 — user-action-required

- createdAt: 2026-07-08T21:59:39.985Z
- updatedAt: 2026-07-08T21:59:39.985Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- summary: |
    /cq:advance run stop — BLOCKED-ON-USER-ACTION (hardening round landed in full; one manual o3 ops step remains).
    
    LANDED THIS RUN (deferred-defect hardening round, goal G1 / milestone M11 archived): planned + emitted the fix-task DAG (M11, T42-T50; decision K3 locked after opus plan-review R39 revise -> R40 go-ahead), then built all 9 tasks — each in an isolated worktree, adversarially reviewed (R42-R50; T47 caught a real AmneziaWG-classifier blindness on round 1, fixed + re-reviewed round 2), gated (build/vet/gofmt/go test) and -race-clean on the composed main, and merged. 12 defects RESOLVED: D3, D4, D10, D13, D14, D20, D22, D23, D24, D25, D26, D28. T49's measure-then-sweep used a real 4-vCPU in-fixture measurement on llm-ubuntu-0.
    
    BLOCKING USER ACTION (the ONLY remaining step; D7 live-apply + D8 are non-terminal by design per Q14 — o3 is a TEST host the agent cannot reach from its sandbox, and every repo-side step (T48: reboot-persistent provisioning + install-doc + TestRealProvision) is already merged). Operator runs ON o3 (89.168.124.91), in order: (1) `sudo iptables -S INPUT`  [capture BEFORE]; (2) dedup the INPUT chain to one canonical rule set (single `-p udp --dport 51820 -j ACCEPT`, single OCI default REJECT block; remove the duplicate/unreachable copies) [D8]; (3) apply+persist the tunnel ACCEPT rule via `sudo netfilter-persistent save` (or the idempotent /etc/iptables/rules.v4 edit) [D7 live-apply]; (4) `sudo reboot`; (5) after reboot `sudo iptables -S INPUT` [capture AFTER] and confirm the `-i wanbond0 -j ACCEPT` rule SURVIVED and inbound tunnel TCP is no longer REJECTed. NEVER deprovision/terminate o3. Then paste both captures + the post-reboot confirmation into the ledger to drive D7/D8 -> resolved, and re-run /cq:advance.
- flow: advance
- ledgerRefs: ["goals:G1","defects:D7","defects:D8","tasks:T48","tasks:T42","tasks:T43","tasks:T44","tasks:T45","tasks:T46","tasks:T47","tasks:T49","tasks:T50"]
- handoffReasons: ["drained: the deferred-defect hardening round is otherwise complete — 12 defects resolved (D3,D4,D10,D13,D14,D20,D22,D23,D24,D25,D26,D28); T42-T50 built, reviewed (R42-R50), gated, -race-clean, merged; milestone M11 archived; all three P-predicates FALSE","user-action-required: D7 (live-apply portion) + D8 blocked solely on the manual o3 iptables dedup/persist/reboot ops (exact commands in this summary); o3 is a test host unreachable from the agent sandbox (Q14); every autonomous/repo-side step (T48) is merged"]

## M12

### HO11 — drained

- createdAt: 2026-07-13T18:21:51.773Z
- updatedAt: 2026-07-13T18:21:51.773Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- summary: "DRAINED. Goal G2 (production-readiness / pre-pilot hardening) — ALL 16 tasks T51-T66 done + hardware-validated; P-investigate/P-plan/P-implement all FALSE. Delivered: W1 startup path-availability resilience (M13: tolerant bind + background reconcile, T51/T55/T60); W2 pacing empirical sizing + BDP config wiring (M14 archived: T52/T53/T56/T61, bufferbloat 208->0.5ms hardware); W3 multi-concentrator hub failover (M15: ordered-endpoint active-standby T54/T57 + netns e2e T62); W4 real-link validation tier (M16 archived: aggregation+bufferbloat T58, mid-transfer LINK+HUB WAN-kill T63, short soak T64 — all across llm-ubuntu-0<->o3); W5 pilot runbook + non-blocking exit criterion + full doc-sync (M17 archived: T59/T65/T66). HEADLINE: the T62 hardware e2e caught a HIGH-SEV data-plane defect (D32) that unit tests + code review missed — after a concentrator failover the switch+re-handshake completed on the wire but the edge's receive resequencer dropped the standby hub's handshake-response (its outer-seq restarts near 1, below the release point the prior hub's traffic advanced to). Root-caused via instrumented hardware capture, fixed (Resequencer.Rebaseline() on the hub switch, c7f8421), and confirmed: hub-failover now resumes traffic via the standby in ~1.7-2.1s over a REAL cross-network link (13/13 + real-link passes). A fixture setup race (D33) was also found+fixed (26/26 clean). NON-BLOCKING PILOT EXIT CRITERION recorded (runbook §7): capped-fixture aggregation/bufferbloat (W2) + report-only real-link baseline (`just p0-baseline`, W4) are SUFFICIENT to enter a supervised pilot; the longer soak runs DURING the pilot. REMAINING (non-blocking, for the user): (1) two DEFERRED root-caused low-sev hardening defects — D30 (promoted/runtime paths forgo SO_BINDTODEVICE, losing T16 re-roam survival; pre-existing) and D34 (post-rebaseline resequencer could re-anchor to a prior-hub straggler; self-healing, did NOT trigger in 39 hardware runs) — these need a post-pilot fix/defer decision and BLOCK archival of M13/M15 (M14/M16/M17 archived). (2) Goal G2 stays `building` — goals never auto-close; the user closes it when satisfied. No open questions; no blocking user action for the autonomous flow."
- flow: advance
- ledgerRefs: ["goals:G2","defects:D30","defects:D34","tasks:T66"]
- handoffReasons: ["drained"]

## M18

### HO12 — answers-required

- createdAt: 2026-07-13T21:01:45.458Z
- updatedAt: 2026-07-13T21:01:45.458Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- summary: "/cq:plan bootstrapped goal G4 'Multi-peer (hub-and-spoke) concentrator support' (milestone M18) into `clarifying`. The plan-advance planner grounded read-only against the cited code (verified all premises: config.Config.PSK is a single top-level key with no per-peer field; device.Up builds exactly ONE bind.NewMultipath so the datapath is structurally single-peer even though [[wireguard.peers]]/uapiConfig iterate all peers; found two reusable hooks — handleInbound already learns the return remote only from an authenticated PROBE, and reseq.Rebaseline exists) and filed 8 clarifying questions Q21-Q28 (open, linked goals:G4) covering: scope boundary (concentrator-only vs edge-multi-hub); per-peer-PSK model + config schema + single-peer back-compat; wire-format-change acceptability; demux bootstrapping (gate-vs-quarantine + trial-decode-across-peer-PSKs); roaming re-bind; concentrator resource limits/eviction/backpressure + per-peer memory; threat model for the path->peer binding; per-peer /metrics labels. Q24/Q26/Q27 (demux/limits/threat) form one cluster; Q22/Q28 (per-peer-PSK/metrics) may share a new per-peer config field — read the answers together. NEXT: user answers Q21-Q28 in the TUI/web, then runs /cq:plan:advance G4 to plan the task DAG. Blocked on user answers."
- flow: plan
- ledgerRefs: ["goals:G4"]
- blockingQuestions: ["Q21","Q22","Q23","Q24","Q25","Q26","Q27","Q28"]
- sessionLogs: [".cq/logs/20260713-210054-acde8de5f9cf22718.md"]
- rawLogs: [".cq/logs/raw/20260713-210054-acde8de5f9cf22718.jsonl"]

## M19

### HO13 — answers-required

- createdAt: 2026-07-13T21:22:33.569Z
- updatedAt: 2026-07-13T21:22:33.569Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- summary: "/cq:plan bootstrapped goal G5 'Optional DNS (hostname) concentrator endpoints' (milestone M19) into `clarifying`. The plan-advance planner grounded read-only (verified: netip.ParseAddrPort rejects hostnames at config.go:495 + multipath.go:1327; metrics/server.go:133 proves DNS capability via net.LookupIP) and filed 8 clarifying questions Q29-Q36 (open, linked goals:G5) covering: opt-in/default-off posture + IP-literal byte-identity; defer-and-reconcile vs hard-resolve at load; re-resolution trigger/cadence/repoint + no-op suppression; multi-record A/AAAA + v4/v6 preference; resolver privacy + a testable security acceptance target; hub-failover interaction/coordination; config surface (overload vs new field); e2e/unit acceptance bar + realhosts scope. Two grounded findings sharpen the plan: (1) SetPeerRemote (multipath.go:1371) is deliberately disruptive (Rebaseline + re-handshake) so re-resolution MUST suppress no-op unchanged-IP repoints; (2) hubFailover holds an immutable endpoints snapshot with active idx and also calls SetPeerRemote, so an independent re-resolution loop repointing the active endpoint RACES the failover controller (Q34). Q34 (failover coordination) + Q35 (config surface) most shape the DAG blast radius. NEXT: user answers Q29-Q36 in the TUI/web, then runs /cq:plan:advance G5 to plan the task DAG. Blocked on user answers."
- flow: plan
- ledgerRefs: ["goals:G5"]
- blockingQuestions: ["Q29","Q30","Q31","Q32","Q33","Q34","Q35","Q36"]
- sessionLogs: [".cq/logs/20260713-212207-a0e65d160c67b7983.md"]
- rawLogs: [".cq/logs/raw/20260713-212207-a0e65d160c67b7983.jsonl"]

### HO14 — drained

- createdAt: 2026-07-13T22:13:12.471Z
- updatedAt: 2026-07-13T22:13:12.471Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "Standalone /cq:plan:advance G5 run stop: DRAINED. G5 (optional DNS concentrator endpoints) advanced clarifying → planning → planned in one run. Multi-planner round (configured panel opus+fable, generate-N-then-judge): both candidates returned; fable's candidate chosen as base with opus fold-ins (DoH/DoT bootstrap-IP fail-fast, cross-controller -race interleave task, active-endpoint-by-identity); orchestrator persisted the synthesized plan as work milestones M20/M21/M22 + tasks T67-T79. Review round 1 (panel opus+fable, strictest-wins): R70 revise with 2 [fable] criticisms (T74 first-resolve handshake mechanism; T70 active-identity ambiguity under cross-spec AddrPort duplicates); planner revised T70/T74/T77 in place. Review round 2: R71 unanimous go-ahead; decision K5 locked; G5 reached planned. Auto-investigate phase: ledger query found ZERO actionable defects linked goals:G5 (no reviewer filed any) — no-op. Next: /cq:implement:start for M20/M21/M22, and the user closes building→done later."
- flow: plan
- ledgerRefs: ["goals:G5"]
- sessionLogs: [".cq/logs/20260713-215726-ad0c63b3749a28ff8.md",".cq/logs/20260713-215726-a3a1678fe42741a52.md",".cq/logs/20260713-220328-a45a9b222054d0d22.md",".cq/logs/20260713-220328-ab541f9aa587c0050.md",".cq/logs/20260713-220647-a5fce4a23176a911e.md",".cq/logs/20260713-221057-abf3d4747c8c2d97a.md",".cq/logs/20260713-221057-ab6ff041b6c4fdf2b.md",".cq/logs/20260713-221230-a1d2bf0c3d369b9bf.md"]
- rawLogs: [".cq/logs/raw/20260713-215726-ad0c63b3749a28ff8.jsonl",".cq/logs/raw/20260713-215726-a3a1678fe42741a52.jsonl",".cq/logs/raw/20260713-220328-a45a9b222054d0d22.jsonl",".cq/logs/raw/20260713-220328-ab541f9aa587c0050.jsonl",".cq/logs/raw/20260713-220647-a5fce4a23176a911e.jsonl",".cq/logs/raw/20260713-221057-abf3d4747c8c2d97a.jsonl",".cq/logs/raw/20260713-221057-ab6ff041b6c4fdf2b.jsonl",".cq/logs/raw/20260713-221230-a1d2bf0c3d369b9bf.jsonl"]

## M29

### HO15 — answers-required

- createdAt: 2026-07-13T22:57:09.533Z
- updatedAt: 2026-07-13T22:57:09.533Z
- author: fable-5
- session: cac93b81-5292-42e3-b77e-962544c75e54
- summary: "/cq:plan:advance G6 (standalone) stopped at awaiting-answers. First planner pass on the fresh clarifying goal G6 (wanbond-fixes.md production lessons: improvements I1-I8 + docs C1-C6) filed the initial clarifying batch Q37-Q43 under M29: (Q37) defect-gating/sequencing vs D35-D40, (Q38) I7 tun-persistence code vs docs-only, (Q39) I8 bidirectional-standby-liveness here vs investigate-flow, (Q40) NM drop-in + oneshot packaged vs docs-only, (Q41) I6 default-route automation boundary, (Q42) I5 bind-mode config surface, (Q43) batch scope all-in-one vs must-have-first. The configured planner panel (opus+fable) was not fanned out: a clarifying goal with unanswered scope questions yields no candidates, so the question-filing step ran as the default single-planner pass (opus). Auto-investigate worklist EMPTY by ledger query: no actionable defect carries goals:G6 (the companion intake defects D35-D40 under M28 are deliberately unlinked and owned by investigate-flow; D41 belongs to G4's implement round). NEXT: answer Q37-Q43 in the TUI/web, then re-run /cq:plan:advance G6; run /cq:investigate (or /cq:advance) separately to start root-causing D35-D40."
- flow: plan
- ledgerRefs: ["goals:G6"]
- blockingQuestions: ["Q37","Q38","Q39","Q40","Q41","Q42","Q43"]
- sessionLogs: [".cq/logs/20260713-225618-a6491a1ae0266d482.md"]
- rawLogs: [".cq/logs/raw/20260713-225618-a6491a1ae0266d482.jsonl"]

### HO16 — drained

- createdAt: 2026-07-13T23:41:02.296Z
- updatedAt: 2026-07-13T23:41:02.296Z
- author: fable-5
- session: cac93b81-5292-42e3-b77e-962544c75e54
- summary: "/cq:plan:advance G6 (standalone) run to completion: G6 reached `planned`. Q37-Q43 were all answered, so the configured multi-planner path ran: 2 candidate planners (opus 7-milestone/16-task, fable 4-milestone/16-task) fanned out in CANDIDATE mode; synthesis took fable's candidate as base (surface-then-wiring splits of I5/I6, opt-in tun_persist, docs-coupled-to-packaging, two-sided I8 test, reference-sync sweep) and folded in opus's I7 acceptance (stable ifindex + NM-unmanaged) and NM-drop-in presence check; the orchestrator persisted work milestones M30-M33 + tasks T100-T115 and transitioned G6 to planning. Review loop (configured opus+fable panel, strictest-wins): R80 revise (3 criticisms: T115 dependsOn missing T101/T108; T115 scope missing runbook.md; T101 flaky scrape-ordering acceptance) → revision → R81 revise (2 residual same-class sequencing gaps: T115 needs T110/T111/T113; T111 needs T109) → revision → R82 unanimous go-ahead. Decision K7 locked; G6 `planned`; planning-lock commit dbf54ec. Auto-investigate worklist EMPTY by ledger query (no actionable defect carries goals:G6; the reviews filed zero defects; D35-D40 remain M28/investigate-flow-owned by design). NEXT: /cq:implement:start (or /cq:advance) to build M30-M33; separately /cq:investigate for D35-D40."
- flow: plan
- ledgerRefs: ["goals:G6","reviews:R80","reviews:R81","reviews:R82","decisions:K7"]
- sessionLogs: [".cq/logs/20260713-232548-a382332a889496d5d.md",".cq/logs/20260713-232548-a795489b23fb6f794.md",".cq/logs/20260713-233100-a55a4e128f6f54f3a.md",".cq/logs/20260713-233100-af3f0626f4832a9e5.md",".cq/logs/20260713-233226-a31df891879aba85e.md",".cq/logs/20260713-233606-a6a7deec127907c4c.md",".cq/logs/20260713-233606-a00894726fc25d16c.md",".cq/logs/20260713-233719-a1999a2a1c65132fa.md",".cq/logs/20260713-233917-a89c1670ebd3cd89d.md",".cq/logs/20260713-233917-a5034ee3e9ef63fd4.md",".cq/logs/20260713-234017-aa1ce2b42795fdf8a.md"]
- rawLogs: [".cq/logs/raw/20260713-232548-a382332a889496d5d.jsonl",".cq/logs/raw/20260713-232548-a795489b23fb6f794.jsonl",".cq/logs/raw/20260713-233100-a55a4e128f6f54f3a.jsonl",".cq/logs/raw/20260713-233100-af3f0626f4832a9e5.jsonl",".cq/logs/raw/20260713-233226-a31df891879aba85e.jsonl",".cq/logs/raw/20260713-233606-a6a7deec127907c4c.jsonl",".cq/logs/raw/20260713-233606-a00894726fc25d16c.jsonl",".cq/logs/raw/20260713-233719-a1999a2a1c65132fa.jsonl",".cq/logs/raw/20260713-233917-a89c1670ebd3cd89d.jsonl",".cq/logs/raw/20260713-233917-a5034ee3e9ef63fd4.jsonl",".cq/logs/raw/20260713-234017-aa1ce2b42795fdf8a.jsonl"]

## M48

### HO17 — answers-required

- createdAt: 2026-07-14T11:46:26.489Z
- updatedAt: 2026-07-14T11:46:26.489Z
- author: "opus-4.8[1m]"
- session: be1a85fd-55c8-4654-ae42-672792fc0238
- summary: "/cq:plan bootstrapped goal G12 'Live monitoring web UI on edge and concentrator (WebSocket status, local-API auth)' (milestone M48) into `clarifying`. The plan-advance planner grounded against the repo (verified: internal/metrics.Source already snapshots exactly the statistics the goal names — Paths/FEC/Reseq/Session/PeerNames; internal/metrics/server.go enforces loopback-only binding fail-fast with act-then-verify on the kernel-bound address; repo is pure Go with no websocket library and no JS toolchain; single binary serves both roles from 0600-enforced TOML config with SIGHUP reload) and filed 6 clarifying questions Q45-Q50 (open, linked goals:G12) covering: (Q45) network exposure — loopback-only vs LAN against the existing ErrNonLoopbackBind invariant; (Q46) shared metrics listener vs dedicated [monitor] listener; (Q47) auth threat model + mechanism for unauthorized local calls (token / Origin-validation / unix-socket options) — the goal's stated open question; (Q48) read-only vs control actions; (Q49) vanilla go:embed vs JS toolchain + coder/websocket dependency; (Q50) data scope/cadence/history, per-peer on concentrator. Auto-investigate worklist EMPTY by ledger query (no open defect links goals:G12). NEXT: user answers Q45-Q50 in the TUI/web, then runs /cq:plan:advance G12 to plan the task DAG. Blocked on user answers."
- flow: plan
- ledgerRefs: ["goals:G12"]
- blockingQuestions: ["Q45","Q46","Q47","Q48","Q49","Q50"]
- sessionLogs: [".cq/logs/20260714-114510-a2014552ac2ffb804.md"]
- rawLogs: [".cq/logs/raw/20260714-114510-a2014552ac2ffb804.jsonl"]

## M50

### HO18 — answers-required

- createdAt: 2026-07-14T12:18:55.230Z
- updatedAt: 2026-07-14T12:18:55.230Z
- author: "opus-4.8[1m]"
- session: 915ea040-10d3-4f13-9cf2-ed8e5149babb
- summary: "/cq:plan bootstrapped goal G13 'Operability & safety for weighted aggregation + pacing' (milestone M50) into `clarifying` from empirical findings testing policy=\"weighted\" with pacing on/off. Three operability gaps: (1) aggregation-engagement observability — add `wanbond_aggregation_engaged` metric + EWMA-offered-load-vs-threshold signal so \"configured but inert\" (default per_path_capacity_fps=10000 never engages on a CPU-bound edge) is visible; (2) capacity-sizing safety — auto-derive per_path_capacity_fps from declared link_bandwidth OR warn/fail-loud, tied to install.md §3a BDP sizing; (3) pacing on/off tradeoff docs + a latency/priority class so small control frames aren't starved (measured ~38% loss on a concurrent ICMP flow under pacer overload). The plan-advance planner grounded against the repo and filed 5 clarifying questions Q51-Q55 (open, linked goals:G13). Key findings surfaced: the '38% ICMP loss' is likely INNER-tunnel ICMP (opaque ClassData to the pacer — no priority lane possible); the wanbond-addressable target is PROBE-frame (KindProbe) starvation (Q51); G2's Q20 pacing-sizing decision is recorded but UNMATERIALIZED (M14/M16/M17 empty) so G2/G13 ownership must be settled at goal level (Q53); the weighted scheduler already computes aggregation-gate state (s.aggregating, EWMA loadRate, thresholds) but is per-PEER, making `wanbond_aggregation_engaged` cardinality a real design decision (Q54). Auto-investigate worklist EMPTY by ledger query (no open defect links goals:G13). NEXT: user answers Q51-Q55 in the TUI/web (set each to answered with a non-empty answer), then runs /cq:plan:advance G13 to plan the task DAG. Blocked on user answers."
- flow: plan
- ledgerRefs: ["goals:G13"]
- blockingQuestions: ["Q51","Q52","Q53","Q54","Q55"]
- sessionLogs: [".cq/logs/20260714-121742-a7376892a4d9f68f5.md"]
- rawLogs: [".cq/logs/raw/20260714-121742-a7376892a4d9f68f5.jsonl"]
