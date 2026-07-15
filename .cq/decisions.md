---
ledger: decisions
counters:
  milestone: 0
  item: 22
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

## M34

### K8 — locked

- createdAt: 2026-07-14T09:39:07.375Z
- updatedAt: 2026-07-14T09:39:07.375Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: "G7 plan approved: D36/D37 restart-handshake fix DAG locked (M39-M41 / T116-T122)"
- rationale: "Unanimous reviewer go-ahead (R127, round 2, opus+fable) after a round-1 revise (R126) whose two criticisms were resolved. The confirmed root cause: the outer-plane resequencer SUSPECT-drops a restarted peer's low-outer-seq frames (wrapping the WG init) because Rebaseline() is wired only to hub-failover. The locked plan: T116 surface the authenticated T38 peer-restart epoch-change as a Reflect return-flag (restart-vs-bootstrap gated; per-epoch deduped) → T119 consume it at the single dispatchInbound seam (covers edge single-concentrator + every concentrator per-peer resequencer, both directions) with a LOW-ANCHOR re-anchor variant that closes the stale-high re-pin race by reusing the resequencer's own one-window SUSPECT boundary; independently T117 (bind one-shot first-path-up) → T120 (device forced WG (re)init via deviceRehandshake backdating) fixes the compounding D37 pre-liveness startup init; T118 rebaseline/dropSuspect counters, T121 deferred netns one-sided-restart e2e (o3 + llm-ubuntu-0, G2 pattern), T122 docs. Fixes defects:D36, folds defects:D37. Synthesized from opus+fable candidate planners (fable base for the sharper restart-vs-bootstrap distinction + surface→wiring splits; opus's observability-counters task folded in)."
- landsIn: ["M39","M40","M41"]
- sourceRefs: ["goals:G7","reviews:R126","reviews:R127"]
- ledgerRefs: ["goals:G7","defects:D36","defects:D37"]

## M35

### K9 — locked

- createdAt: 2026-07-14T09:56:34.511Z
- updatedAt: 2026-07-14T09:56:34.511Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: "G8 plan approved: multi-peer datapath hardening DAG locked (M42-M44 / T123-T129)"
- rationale: "Unanimous reviewer go-ahead (R129, round 2, opus+fable) after a round-1 revise (R128) whose 3 criticisms were resolved. Six multi-peer defects grouped by code-locality: T123 re-keys the source→peer demux by AddrPort + per-peer quota with same-peer-own-oldest LRU eviction (D47+D49); T124 completes the deferred-path lifecycle (promoteDeferredLocked fan-out + removeDurableLocked alignment guard, D42); T125 fans the FEC deadline flush + tick-loop-start across peers (D44); T126 wires LEVEL-triggered device per-peer teardown→Bind.TearDownPeer (D50, closing the never-handshaked leak); T127 plumbs the primary peer name into metrics + doc-sync (D58); T128 extends the multi-peer netns e2e; T129 the deferred privileged run on o3+llm-ubuntu-0. The multipath.go editors (T123→T124→T125→T127) are serialized to avoid worktree merge conflicts in the 2820-line file. Synthesized from opus+fable candidates (fable base: sharper D42-already-partially-fixed + D44-tickloop grounding, D47+D49 fusion, multipath.go serialization; opus's separate deferred-privileged-run task folded in). Fixes defects:D42/D44/D47/D49/D50/D58."
- landsIn: ["M42","M43","M44"]
- sourceRefs: ["goals:G8","reviews:R128","reviews:R129"]
- ledgerRefs: ["goals:G8","defects:D42","defects:D44","defects:D47","defects:D49","defects:D50","defects:D58"]

## M36

### K10 — locked

- createdAt: 2026-07-14T10:06:22.734Z
- updatedAt: 2026-07-14T10:06:22.734Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: "G9 plan approved: config loader/validation hardening DAG locked (M45 / T130-T132)"
- rationale: "Unanimous round-1 reviewer go-ahead (R130, opus+fable). Serial 3-task chain in internal/config: T130 strict DisallowUnknownFields decode rejecting unknown/misspelled TOML keys (D41); T131 accept Go duration STRINGS for CollapseDwell/LoadTau/WeightRTTFloor/FEC.Deadline via the LinkRTTRaw raw-string mirror + wanbond.example.toml/docs sync (D43); T132 netip.ParsePrefix allowed_ips at load + default-route/-overlapping-/0 exclusivity in validate() (D55+D59). Serialized (all share config.go; T131's re-keyed fields must pass under T130's strict decoder). Synthesized from convergent opus+fable candidates (fable base: cleaner D41/D43 split so field re-keying is strict-decode-covered; D55+D59 folded since /0-detection consumes D55's parsed prefixes). Fixes defects:D41/D43/D55/D59."
- landsIn: ["M45"]
- sourceRefs: ["goals:G9","reviews:R130"]
- ledgerRefs: ["goals:G9","defects:D41","defects:D43","defects:D55","defects:D59"]

## M38

### K11 — locked

- createdAt: 2026-07-14T10:21:21.908Z
- updatedAt: 2026-07-14T10:21:21.908Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: "G11 plan approved: code/test/doc-comment hygiene sweep DAG locked (M47 / T136-T140)"
- rationale: "Unanimous round-1 reviewer go-ahead (R132, opus+fable, 0 criticisms). Two-wave DAG: T136 (root) fixes the 3 pre-existing golangci findings (doh.go:206 + dot.go:168 errcheck; the QF1001 site — now bind/pathsock.go:242, relocated via a lint run) AND installs the v2 .golangci.yml exclusions (linters.exclusions.paths + formatters.exclusions.paths for .claude/worktrees, D54) to make `just lint` green + hermetic — a genuine prerequisite because T138/T139 acceptances require a green lint gate (D45+D54). Then four file-disjoint parallel leaves each dependsOn T136: T137 (e2e /metrics port collision — pacing_test.go+p3_fec_test.go both 9096 — fresh port inventory + unique assignment, D51); T138 (stale config.go comments — D60 delete BindMode/Path.Bind 'config surface only', D57 replace PSK/Name 'not yet consumed' with real consumers); T139 (delete superseded primary-only Multipath.PathSnapshots/FECSnapshot seams — zero external callers — migrating bind tests to PeerSnapshots so the delivered-count derivation lives once, D56); T140 (reconcile SO_BINDTODEVICE capability — pathsock_linux.go CAP_NET_RAW comment vs CAP_NET_ADMIN-only units — via an empirical CAP_NET_ADMIN worker probe + ≥5.7 kernel-floor verification, widening the unit only if proven required, D40). All 7 defects map 1:1 with correct ledgerRefs; docs/install.md sync in T140. Synthesized from convergent opus+fable candidate plans. Fixes defects:D40/D45/D51/D54/D56/D57/D60."
- landsIn: ["M47"]
- sourceRefs: ["goals:G11","reviews:R132"]
- ledgerRefs: ["goals:G11","defects:D40","defects:D45","defects:D51","defects:D54","defects:D56","defects:D57","defects:D60"]

## M37

### K12 — locked

- createdAt: 2026-07-14T10:25:05.566Z
- updatedAt: 2026-07-14T10:25:05.566Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: "G10 plan approved: metrics-accuracy + reload/bind observability DAG locked (M46 / T133-T135)"
- rationale: "Unanimous round-2 reviewer go-ahead (R133, opus+fable, 0 criticisms) after a clean round-1 REVISE (R131) that both reviewers confirmed fully addressed. Serialized chain T133→T134→T135: T133 counts probe-emission + echo-reflection wire bytes into ps.txBytes (emitProbes probe.go:50 + dispatchInbound echo multipath.go:~1693, both on nil-error only), rewrites the now-false peerPathState txBytes counter-contract comment (multipath.go:157-167) to true-wire-volume semantics, syncs README/design.md, and updates the T104 standby-idle subtest's stale repro commentary (no assertion inverts — already asserts delta>0), fixing D48. T134 threads a log.Logger through NewMultipath (9 device.go call sites) + WARNs on device-bind fallback, fixing D53. T135 extends reloadWarnings (device.go:549) over Scheduler/FEC/DNS + Bind at BOTH config levels (per-path Path.Bind l.Bind!=d.Bind AND top-level c.Bind default, config.go:841-849) + a zeroed-copy catch-all future-proof, fixing D52. Serialized because T133+T134 both edit multipath.go and T134+T135 both edit device.go (same-file worktree-conflict avoidance, applied consistently). Control frames have no production egress in internal/bind, so probe+echo are the only two uncounted tx writes (completeness verified). Synthesized from convergent opus+fable candidate plans. Fixes defects:D48/D52/D53."
- landsIn: ["M46"]
- sourceRefs: ["goals:G10","reviews:R131","reviews:R133"]
- ledgerRefs: ["goals:G10","defects:D48","defects:D52","defects:D53"]

## M50

### K13 — locked

- createdAt: 2026-07-14T12:53:33.053Z
- updatedAt: 2026-07-14T12:53:33.053Z
- author: "opus-4.8[1m]"
- session: 915ea040-10d3-4f13-9cf2-ed8e5149babb
- headline: "plan review: approved"
- rationale: "Reviewer go-ahead per R156 (unanimous opus+fable panel, empty new_questions/criticism). Plan G13 (work milestones M51-M55 / tasks T141-T148) approved; DAG verified acyclic and complete for the Q51-Q55 scope (R155 revise[4 criticisms] -> R156 go-ahead). Synthesized from a 2-planner candidate panel (opus+fable, generate-N-then-JUDGE+SYNTHESIS). Locking to permit planning->planned."
- ledgerRefs: ["goals:G13"]

## M56

### K14 — locked

- createdAt: 2026-07-14T13:34:44.340Z
- updatedAt: 2026-07-14T13:34:44.340Z
- author: "opus-4.8[1m]"
- session: 7295f080-20fa-4cf9-afac-0357b4cf65cb
- headline: "plan review: approved"
- rationale: "Reconciled opus+fable plan review R163 both go-ahead: all 4 R162 criticisms verified resolved, plan fine-grained/sequenced/testable/grounded/complete across both D65 halves."
- ledgerRefs: ["goals:G14"]

## M48

### K15 — locked

- createdAt: 2026-07-14T22:42:34.113Z
- updatedAt: 2026-07-14T22:42:34.113Z
- author: "opus-4.8[1m]"
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: "plan review: approved"
- rationale: "Reviewer go-ahead (ref review R199, aggregated opus+fable panel: 0 criticisms / 0 new questions / 0 defects). Plan DAG M61/M62/M63 tasks T160-T172 approved; prior blocker Q58 answered (a) and reflected in T171."
- ledgerRefs: ["goals:G12"]

## M64

### K16 — locked

- createdAt: 2026-07-15T06:19:22.283Z
- updatedAt: 2026-07-15T06:19:22.283Z
- author: "opus-4.8[1m]"
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: "G15 plan approved: active-backup pacing correctness fix DAG locked (M65/M66/M67, T173-T178)"
- rationale: "Reviewer go-ahead (R218, single opus, 0 criticisms/0 questions), root-cause->fix mapping verified against source. D79 (HIGH): active-backup per-path pacer config carried by slice position / tail-inheritance across churn (AddPath tail-seed active_backup.go:184-188; resizeActiveBackupPacers index-carry :275-288) while newActiveBackupPacers is identity-keyed only at construction; FIX = extend DynamicScheduler.SetPaths/AddPath (scheduler.go:153-175) to carry per-path pacer config by identity, seed *ActiveBackup from it (drop tail/positional carry), confirm *WeightedScheduler (shared-scalar pacer) has no analogous hole, and source each bound/promoted path's config from cfg.Scheduler.PerPathCapacities (index-aligned to m.defs) at the bind Open-reconcile/promotion/reload sites. D76 (MEDIUM): *ActiveBackup implements no sched.ProbeBudget so emitProbes/echo-reflection type-asserts no-op -> zero probe headroom -> spurious failover flap; FIX = AccountProbe(pathIdx) on *ActiveBackup (s.pacers[pathIdx].accountProbe(0)) + compile-proof, mirroring the T145 weighted impl+3 tests. Reproduce-first for both. DAG: T173->T175, T174->T176->T177, T178 DoD gate. On merge, defects:D79 and D76 -> resolved. EXECUTION NOTE: T173/T174 are reproduce-first tests that do not build/pass on HEAD and both defects edit internal/sched/active_backup.go, so the six tasks are implemented as ONE coherent worktree diff (reproduce-first verified in-worktree) to keep main green."
- landsIn: ["M65","M66","M67"]
- sourceRefs: ["goals:G15","reviews:R218"]
- ledgerRefs: ["goals:G15","defects:D79","defects:D76"]

## M71

### K17 — locked

- createdAt: 2026-07-15T06:29:34.509Z
- updatedAt: 2026-07-15T06:29:34.509Z
- author: "opus-4.8[1m]"
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: "G19 plan approved: e2e test-correctness fix DAG locked (M78, T187/T192)"
- rationale: "Orchestrator inline go-ahead (R219). D81 (T187): rx->tx metric swap in multipeer_test.go non-primary INBOUND assertion. D80 (T192): loopback-in-netns /metrics bind + fetchMetricsInNetns scrape in restart_onesided_test.go (mirror the D77 remediation). Both test-edit-only, reuse proven patterns, independent. Privileged -tags e2e hardware validation deferred to the o3+llm-ubuntu-0 tier. On merge, D80/D81 -> resolved."
- landsIn: ["M78"]
- sourceRefs: ["goals:G19","reviews:R219"]
- ledgerRefs: ["goals:G19","defects:D80","defects:D81"]

## M72

### K18 — locked

- createdAt: 2026-07-15T06:29:47.507Z
- updatedAt: 2026-07-15T06:29:47.507Z
- author: "opus-4.8[1m]"
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: "G20 plan approved: docs/operator-guidance fix DAG locked (M77, T185/T189)"
- rationale: "Orchestrator inline go-ahead (R220). D35 (T185): docs-only external-/0-route operator warning in install.md §9 + runbook pointer (production already mitigated by T107+T108). D61 (T189): Justfile D54-comment reword + D54.rootCause re-adjudication note. Both docs/comment/ledger-only, no code change, independent. On merge, D35/D61 -> resolved."
- landsIn: ["M77"]
- sourceRefs: ["goals:G20","reviews:R220"]
- ledgerRefs: ["goals:G20","defects:D35","defects:D61"]

## M69

### K19 — locked

- createdAt: 2026-07-15T06:37:21.497Z
- updatedAt: 2026-07-15T06:37:21.497Z
- author: "opus-4.8[1m]"
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: "G17 plan approved: bind path-lifecycle & demux hardening DAG locked (M74/M75, T183-T199)"
- rationale: "Reviewer go-ahead (R223, single opus, 0 criticisms). All six root causes verified at exact locations. M74 (linear, same-file multipath.go edit-serialization): T183 (D67 rollback detach-error propagate + force-splice) -> T186 (D62 post-CAS peer-liveness recheck) -> T191 (D30 route auto-mode runtime-added paths through selectDeviceBinds). M75: T195 (D71 promote-failure WARN), T197 (D66 stale comment reword) -> T198 (D63 correct the FIFO-as-'LRU' mislabel at BOTH multipath.go:1744-1746 AND :1794 + wontfix RECOMMENDATION for the user, since autonomous flow can't self-wontfix an intended-per-T123 behavior), T199 (DoD gate). Reproduce-first tests mandatory (codegraph: no covering tests for attachSharedPathLocked/detachPeerPathBoundLocked/bindSourceToPeer/promoteDeferredLocked/AddPath). EXECUTION NOTE: implement as one coherent worktree diff AND serialize AFTER G15 merges (both edit internal/bind/multipath.go). On merge, D30/D62/D66/D67/D71 -> resolved; D63 -> comment fixed, FIFO-vs-LRU behavior flagged to the user for a wontfix decision."
- landsIn: ["M74","M75"]
- sourceRefs: ["goals:G17","reviews:R223"]
- ledgerRefs: ["goals:G17","defects:D30","defects:D62","defects:D63","defects:D66","defects:D67","defects:D71"]

## M70

### K20 — locked

- createdAt: 2026-07-15T06:52:04.463Z
- updatedAt: 2026-07-15T06:52:04.463Z
- author: "opus-4.8[1m]"
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: "G18 plan approved (round 2): metrics/server + reload observability DAG locked (M76, T184-T196)"
- rationale: "Orchestrator go-ahead R235 after the R232 revise corrections landed. DAG: T184 (D84 metrics.Server.Close listener leak, mirror monitor.Server.Close) -> T193 (D83 requireLoopback -> netutil.IsLoopbackHost, generic ErrNonLoopbackBind) -> T188 (D70+D74 reloadWarnings link_bw/rtt + weighted-capacity gauge retain-on-Server + recompute-on-Reload + doc-sync), all serialized on internal/metrics/server.go; T190 (D72 WeightedScheduler.SetPaths aggregation-change record) -> T194 (D75 idle-gap log-field test) on weighted.go; T196 DoD gate. Reproduce-first where observable. EXECUTION NOTE: implement as one coherent worktree diff; worker-safe (internal/metrics + internal/device + internal/sched/weighted, NOT the stall-prone multipath.go/monitor); disjoint from G16/G17 (multipath.go/reseq) so parallel-safe. On merge, D70/D72/D74/D75/D83/D84 -> resolved."
- landsIn: ["M76"]
- sourceRefs: ["goals:G18","reviews:R235","reviews:R232"]
- ledgerRefs: ["goals:G18","defects:D70","defects:D72","defects:D74","defects:D75","defects:D83","defects:D84"]

## M68

### K21 — locked

- createdAt: 2026-07-15T06:52:19.529Z
- updatedAt: 2026-07-15T06:52:19.529Z
- author: "opus-4.8[1m]"
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: "G16 plan approved (round 2): reseq re-anchor correctness DAG locked (M73, T179-T182)"
- rationale: "Orchestrator go-ahead R236 after the R222 revise (T179 caller-inventory) correction. DAG: T179 (D34 source-identity gate on plain Rebaseline, carry `ap`; updates the 1 prod caller SetPeerRemote:2559 + 4 test callers) -> T181 (D64 drop-recovered-while-!started, shared ObserveRecovered/Observe seam), T180 (D68 comment reword, parallel), T182 DoD gate. Reproduce-first regression tests (fail on HEAD, pass after). EXECUTION NOTE: T179/T181 edit internal/reseq/reseq.go AND T179 touches internal/bind/multipath.go (SetPeerRemote Rebaseline caller) — so implement INLINE (multipath.go stall-prone in workers) and SERIALIZE after G17 (also multipath.go). On merge, D34/D64/D68 -> resolved."
- landsIn: ["M73"]
- sourceRefs: ["goals:G16","reviews:R236","reviews:R222"]
- ledgerRefs: ["goals:G16","defects:D34","defects:D64","defects:D68"]
