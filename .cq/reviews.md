---
ledger: reviews
counters:
  milestone: 0
  item: 207
archives:
  - id: M11
    path: ./archive/reviews/M11.md
    summary: "Deferred-defect hardening round complete: 9 fix tasks T42-T50 delivered (each opus+fable-reviewed, gated, -race-clean, merged to main), resolving 12 defects (D3,D4,D10,D13,D14,D20,D22,D23,D24,D25,D26,D28). Highlights: T44 CONTROL-frame anti-replay (MAC-covered Seq + ControlGuard); T45 FEC prefix-stability invariant + quiescence-accurate unrecoverable counter; T46 target_residual adaptive-FEC SLA sizing (sanctioned new config surface per Q16); T47 AmneziaWG-profile-aware pacer control-frame exemption (caught+fixed a vanilla-only classifier blindness on re-review); T42 non-vacuous goroutine-leak gate; T43 duplicate source_addr + global-v6 device-bind fixes; T49 throughput-ceiling doc sweep to measured 4-vCPU numbers; T50 e2e/realhosts-tagged lint coverage; T48 reboot-persistent firewall provisioning (repo-side). D7 (live-apply) + D8 remain non-terminal pending the manual o3 iptables ops per Q14 (o3 is a test host)."
    title: Deferred-defect hardening round (D3/D4/D7/D8/D10/D13/D14/D20/D22/D23/D24/D25/D26/D28)
    status: done
  - id: M14
    path: ./archive/reviews/M14.md
    summary: "G2/W2 pacing empirical sizing + BDP config wiring COMPLETE (CORE SCOPE 1, Q20=both). T52 capped-fixture BDP measurement (report-only), T53 wired SizePacingFromBDP into config load from operator-declared per-link bandwidth (load-time only, NOT runtime auto-tuning; pacing default-DISABLED), T56 operator tuning procedure (docs/install.md §3a + design.md; 1540B/frame), T61 ENABLED-pacing bufferbloat + no-rekey-starvation e2e (relative gate). All 4 tasks done, 4 reviews go-ahead (opus), merged to main (c803cb5 T53, b9f5983 T56, 40205c1 T61). HARDWARE-VALIDATED on llm-ubuntu-0 (amd64 4-vCPU): bufferbloat 208.5ms(unpaced)→0.5ms(paced) at 4Mbit cap; BDP=33241B (21.6 frames @1540B), SizePacingFromBDP→capacityFPS=4179.9 burstFrames=21.6 @50Mbit/5.2ms. Numbers fed to the T65 pilot runbook."
    title: G2/W2 — Pacing empirical sizing + BDP config wiring (CORE SCOPE 1 + Q20 both)
    status: done
  - id: M16
    path: ./archive/reviews/M16.md
    summary: "G2/W4 real-link validation tier COMPLETE (CORE SCOPE 2, report-only). T58 aggregation-ratio + loaded-RTT/bufferbloat, T63 mid-transfer LINK + HUB failover, T64 short soak — all across llm-ubuntu-0 (amd64 NAT edge) <-> o3 (aarch64 public concentrator), all HARDWARE-VALIDATED. 3 tasks done, 3 reviews go-ahead (opus). Key real-link results: aggregation ratio ~0.25-0.46 (shared-physical-uplink topology, ratio<=1 EXPECTED — NOT a bandwidth-aggregation guarantee); bufferbloat 21-176ms under saturation (real-link variability); LINK failover ~1.4-1.5s, HUB failover ~1.7-2.1s with traffic RESUMED via standby (confirms the D32-fixed hub-failover data plane on a REAL cross-network link, 60-90 Mbit/s); short soak survived a WG rekey (0 path-down flaps). All o3-safe (reversible udp-scoped iptables, never deprovisioned; firewall fully restored each run)."
    title: "G2/W4 — Real-link validation tier (CORE SCOPE 2: aggregation + loaded-RTT + WAN-kill + short soak, report-only)"
    status: done
  - id: M17
    path: ./archive/reviews/M17.md
    summary: "G2/W5 pilot runbook + non-blocking exit criterion + full doc-sync COMPLETE (CORE SCOPE 3, Q19). T59 rollout runbook (docs/runbook.md — key/PSK gen, both-ends config, standby-concentrator via ordered endpoints + shared WG key, D7/D8 firewall persistence, /metrics health checks), T65 `just p0-baseline` automating the P0 real-link baseline (HARDWARE-VALIDATED: PASS 286s, report emitted), T66 recorded the non-blocking pilot exit criterion (runbook §7: capped-fixture W2 + report-only real-link W4 sufficient to enter a supervised pilot; soak runs DURING the pilot) + full doc-sync removing stale not-yet-built phrasing across README/design/install/manual-checklist/runbook. 3 tasks done, 3 reviews go-ahead. All metric/config claims verified against source; no overclaim (aggregation documented as report-only, single-uplink topology)."
    title: G2/W5 — Pilot runbook, non-blocking exit criterion + full doc sync (CORE SCOPE 3 + Q19)
    status: done
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

### R8 — go-ahead

- createdAt: 2026-07-06T21:49:25.265Z
- updatedAt: 2026-07-06T21:49:25.265Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- summary: "G1 additive follow-up plan (M10 'RH' + T31-T36 + T12/T22 fold-ins): reconciled go-ahead (opus+fable panel, UNANIMOUS, round 1). Both verified: DAG acyclic and correctly sequenced (intra-M10 T31->T32->T33->T34 and T35->T36; cross-plan advisory edges T33->T8/T9 done, T34->T12/T15/T20 correctly defers real-host multipath behind P1's landing via T20's transitive closure; T36 depends on T35 ONLY, correctly excluding T25/T29 since the pre-FEC baseline precedes and feeds the FEC e2e). Q12 report-only honored (all four real-host tasks T31-T34 gate nothing; T35/T36 are hard-gated netns-e2e fixture tasks). Q13 honored (all under one new cross-cutting M10). Additive-only (locked T11-T30 untouched; T12/T22 carry only appended FOLLOW-UP SCOPE notes with unchanged dependsOn). T35 supersedes (not duplicates) the A7/T10 checkpoint follow-up. T34's virtual-interface + policy-routing approach (two distinct 4-tuples through the symmetric NAT from one uplink) is technically sound, asymmetric/intermittent correctly deferred. Grounding verified against the repo (test/e2e/netns.go pathSpec, docs/p0-checkpoint.md A7 note, Justfile e2e targets, OCI REJECT caveat)."
- criticism: []
- new_questions: []
- ledgerRefs: ["goals:G1"]
- sessionLogs: [".cq/logs/20260706-214500-aa9cec28670132772.md",".cq/logs/20260706-214500-a9ccb92569816f8ec.md"]

### R39 — revise

- createdAt: 2026-07-08T21:12:20.921Z
- updatedAt: 2026-07-08T21:12:20.921Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- summary: "revise: hardening plan T42-T50 is well-grounded (every load-bearing citation verified against source: pathsock.go interfaceInfo/familyCount L115-141, baseline_test.go rttUnderLoad 800ms sleep L150, engine_test.go:99 unbuffered send, fixture_impairment_test.go:11-15,63 misattribution, reedsolomon v1.14.1, DefaultSafetyFactor=1.5, fecRetainGroups=512, evictStale L232), complete (14/14 defects mapped 1:1, no drop/overlap), and consistent with Q14/Q15/Q16. Two planner-fixable defects block go-ahead: (1) T42/D20 acceptance is vacuous - a leaked goroutine cannot fail `go test`; (2) T48 would mark D8/live-D7 resolved on a repo merge though they are host-only manual ops."
- new_questions: []
- criticism: ["T42 / D20 acceptance is not operational (vacuous gate). A producer goroutine blocked forever on the unbuffered `ctun.Outbound <-` send at internal/bind/engine_test.go:99 does NOT fail `go test -run TestMultipathEngineUpCanTransmit -count=20`: a passing Go test emits no end-of-run goroutine dump, the test binary exits and kills the leaked goroutine, and there is no deadlock panic because other (test-runtime) goroutines remain live. The stated gate ('shows no leaked/blocked goroutines in the end-of-run dump') therefore cannot observe the D20 leak, so the fix is not actually verified. Require an explicit leak assertion in the test - goleak.VerifyNone(t) (uber-go/goleak) or a runtime.NumGoroutine() before/after delta with a settle window - that FAILS on the pre-fix unbuffered send and PASSES on the buffered/select+done fix.","T48 / D8 (and the live-apply half of D7) resolution bookkeeping is inconsistent. The round's rule is 'each fix task drives its defects to resolved on merge', but D8 is explicitly 'o3 HOST STATE ONLY, not a repo change' and the live iptables dedup + apply + reboot-persist is a MANUAL report-only ops step (correctly carved out of the automated gate per Q14). A repo-only merge that lands the persistence-provisioning code + doc + TestRealProvision would auto-resolve D8 (and the live-apply portion of D7) while o3 still carries the duplicate / non-persistent INPUT rules - the ledger would show them resolved while the host remains in the defective state. Amend the T48 acceptance to state that only the repo-side artifacts resolve on merge, and that D8 and the live-apply portion of D7 are driven to resolved by the RECORDED manual ops action (the before/after `iptables -S INPUT` capture + post-reboot confirmation), not by the repo merge.","T45 <-> T46 ordering (minor). T45 installs the build-time klauspost generator-matrix prefix-stability assertion + the partial-m x partial-k byte-exact property test that make ANY varying-M group provably decodable against the fixed ceiling decoder; T46 (target_residual) newly widens the range of M the adaptive encoder emits. There is no dependsOn edge or note ensuring T45's guarantee is in place before T46's derived-M range is produced. Add an ordering note (or a dependsOn T45 on T46) so a widened M range is never emitted without the byte-exact decode guarantee proven. (The varying-M mechanism pre-exists per D25, so this is a safety/ordering clarification rather than a new coupling.)"]
- ledgerRefs: ["goals:G1"]

### R40 — go-ahead

- createdAt: 2026-07-08T21:19:13.764Z
- updatedAt: 2026-07-08T21:19:13.764Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- summary: "go-ahead: re-review confirms all three R39 criticisms resolved with no new defect. T42/D20 now has a non-vacuous operational leak gate (goleak VerifyTestMain/VerifyNone or NumGoroutine before/after delta that MUST fail on the unbuffered send at engine_test.go:99 and pass post-fix; -count run explicitly demoted to non-gate). T48/D7+D8 now splits an automated repo-side gate (provisioning+doc+TestRealProvision, the only portion resolving on merge) from a report-only manual ops step with exact operator commands; D8 + D7 live-apply are explicitly non-terminal on merge, resolved only by the recorded iptables -S INPUT captures + post-reboot confirmation, and M11's description matches. T45<->T46 advisory ordering recorded reciprocally on both tasks and M11, consistent with the advisory-not-hard-dependency model. Plan already found sound (14/14 defects 1:1, Q14-16 wired); scoped re-review found no regression."
- new_questions: []
- criticism: []
- ledgerRefs: ["goals:G1"]

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

## M5

### R7 — go-ahead

- createdAt: 2026-07-06T21:11:02.142Z
- updatedAt: 2026-07-06T21:11:02.142Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- summary: "T11 reconciled go-ahead (opus+fable panel, round 1 unanimous approve). Both independently re-ran the full gate (build/vet/gofmt/golangci-lint 0 issues, 143k-exec/15-20s FuzzDecode clean, nix build OK) and audited the crypto: HKDF-SHA256 domain-separated subkeys, Encrypt-then-MAC ordering with the nonce inside the MAC, HMAC-SHA256 truncated to 16B compared constant-time (hmac.Equal), fresh crypto/rand 24B XChaCha20 nonce per frame. All 4 acceptance clauses met with GENUINE (non-vacuous) tests: DeepEqual round-trip all kinds incl. empty payloads; exhaustive per-byte tamper test (ErrAuth across the MAC-covered region, no mutation accepted as authentic CONTROL/PROBE); 256-sample/kind byte-histogram (no constant offset); fuzz no-panic. Kind-byte-downgrade-to-forgeable-DATA adjudication verified sound. Filed 2 out-of-scope defects: D4 (no outer-layer anti-replay for CONTROL/PROBE -> T13 handler), D5 (per-frame HKDF/double-ChaCha20 hot-path cost -> cached Codec refactor at T12)."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T11","goals:G1"]
- sessionLogs: [".cq/logs/20260706-211500-ae614f805e5cb18d0.md",".cq/logs/20260706-211500-a8aeb19256ab53115.md",".cq/logs/20260706-211500-a28cc8d9376a6a85b.md"]

### R12 — go-ahead

- createdAt: 2026-07-06T22:49:02.209Z
- updatedAt: 2026-07-06T22:49:02.209Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- summary: "T13 (per-path probes + liveness state machine) reconciled go-ahead (opus+fable panel, multi-round). R1 fable disapprove — 5 valid criticisms: (1) liveness silence-hysteresis reset, (2) anti-replay must be PathID-keyed not global (ErrPathMismatch), (3) per-path loss must derive from the probe ProbeSeq stream NOT the connection-global outer-seq (striping/attach would read as loss; ConnLoss is the correctly-scoped connection metric), (4) concurrency contract (mutexes; Estimator holds no clock/does no I/O), (5) threshold-doc. All fixed in ea9e197. R2 fable disapprove — self-contradictory threshold invariant (comment claimed PLivenessDetectBudget<P1RecoverySeconds i.e. 3.5s<3s); fixed 9bef9cf by restating the invariant on the analytical detect term (DownAfter+ProbeInterval=2.2s < 3s) and labeling DetectBudget as the deliberately-larger e2e assertion deadline. R2 opus disapprove — stale Estimate/Loss/defaultLossWindow docs still said Loss derives from outer-seq DATA, contradicting the #3 redesign; fixed 792bf79. R3 fable approve — both doc fixes verified against code (ObserveProbeEcho/ProbeSeq wiring at probe.go:143; invariant matches constants). Opus r2 confirmed all 5 reworked defects correct + regression-free (-race -count=3 clean, 25 tests green). Merged be0da9f (fast-forward; rebased on main, build/vet/gofmt/test/lint green)."
- criticism: ["[r1 fable, resolved ea9e197] liveness silence-hysteresis not reset on recovery","[r1 fable, resolved ea9e197] anti-replay global not PathID-keyed — added guards map[uint8]*AntiReplay + ErrPathMismatch","[r1 fable, resolved ea9e197] per-path loss derived from connection-global outer-seq — moved to probe ProbeSeq stream (ObserveProbeEcho); ConnLoss for connection-scoped outer-seq","[r1 fable, resolved ea9e197] concurrency contract underspecified — mutexes + no-clock/no-IO Estimator doc","[r2 fable, resolved 9bef9cf] self-contradictory threshold invariant (3.5s<3s) — restated on analytical detect term (2.2s<3s); DetectBudget labeled as e2e assertion deadline","[r2 opus, resolved 792bf79] stale Estimate/Loss/defaultLossWindow docs claimed outer-seq DATA loss — corrected to probe-echo ProbeSeq gaps"]
- new_questions: []
- ledgerRefs: ["tasks:T13","goals:G1"]

### R15 — go-ahead

- createdAt: 2026-07-06T23:09:35.930Z
- updatedAt: 2026-07-06T23:09:35.930Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- summary: "T12 (multipath conn.Bind: per-path sockets behind one virtual endpoint + MTU accounting + D5 frame.Codec) reconciled go-ahead (opus+fable panel, 3 rounds). R1 opus disapprove — reproduced a data race on the shared virtual-endpoint dst (write under m.mu vs lockless engine Dst* reads); fixed 6937d7c via atomic.Pointer[netip.AddrPort]. R2 opus approve (race fix confirmed, regression test trips DATA RACE on revert), fable DISAPPROVE (CRITICAL) — reproduced a bind-lifecycle defect: amneziawg-go v1.0.4 upLocked->BindUpdate->closeBindLocked calls bind.Close() unconditionally BEFORE every Open(port); facet (a) Close set m.closed=true and Open never reset it -> Send returned net.ErrClosed forever after device.Up() (tunnel could never transmit a handshake); facet (b) m.paths never cleared -> re-Open returned ErrBindAlreadyOpen. Also hot-path lock contention (virtualEndpoint took m.mu per recv; Send held m.mu across the WriteToUDPAddrPort syscall). Fixed f15fe59: removed the sticky closed flag (closed == no bound sockets, mirroring conn.StdNetBind), Close clears m.paths+sendCodec and Open rebuilds sockets from retained defs on the engine-supplied port; lock-free virtualEndpoint fast path (atomic dstValid double-checked pin); WriteToUDPAddrPort moved out of m.mu with fresh per-datagram buffers (Encode copies out of codec scratch, no aliasing). R3 BOTH approve: all 3 new tests (TestMultipathEngineLifecycleCloseThenOpen facet-a, TestMultipathReopenAfterClose facet-b, unprivileged TestMultipathEngineUpCanTransmit via tuntest channel-TUN + real awgdevice.Device) demonstrated fail-before at 6937d7c / pass-after at f15fe59 under -race; no aliasing on the unlocked send path; TestMultipathVirtualEndpointDstRace still race-clean; no regression to virtual-endpoint identity, outer-seq/path-id framing, D5 single-keystream Codec, InnerMTU=1401, SO_RCVBUF ~7MiB. Full gate green (build/vet/gofmt/test -race/golangci-lint 0; e2e compiles). D5 auto-resolves (NewCodec landed). Out-of-scope defects filed: D9 (unauthenticated-DATA remote-learn DoS -> T15), D10 (duplicate path source_addr not validated -> EADDRINUSE). Merged 6675ead (rebased onto main)."
- criticism: ["[r1 opus, resolved 6937d7c] data race on shared virtual-endpoint dst — atomic.Pointer","[r2 fable, resolved f15fe59, CRITICAL] bind-lifecycle: engine Close-before-Open left sticky closed=true (Send->ErrClosed forever) + m.paths uncleared (re-Open->ErrBindAlreadyOpen); fixed to StdNetBind semantics + 2 lifecycle regressions + unprivileged engine-integration test","[r2 fable, resolved f15fe59] hot-path lock contention — lock-free virtualEndpoint fast path + WriteToUDPAddrPort moved out of m.mu with fresh per-datagram buffers"]
- new_questions: []
- ledgerRefs: ["tasks:T12","goals:G1"]

### R18 — go-ahead

- createdAt: 2026-07-06T23:43:22.065Z
- updatedAt: 2026-07-06T23:43:22.065Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- summary: "T15 (active-backup send scheduler with transparent failover: internal/sched Scheduler interface + ActiveBackup, wired into multipath Bind Send) reconciled go-ahead (opus+fable panel, 2 rounds). R1: BOTH confirmed the UNIT-level acceptance is met non-vacuously (all-egress-on-primary; failover within the detection window driven by a REAL telemetry.Liveness on a fake clock; no-thrash under a flapping trace incl. active-dies-mid-dwell) and the T12 concurrency model is preserved (Pick() under s.mu, lock order m.mu->s.mu acyclic, receive fast path + atomic dst untouched, -race clean). opus approve; fable DISAPPROVE with 2 autonomously-fixable doc/contract criticisms: (1) the PathHealth doc endorsed bare *telemetry.Liveness as a concurrent source, but Liveness.State() is an unguarded field read (only *telemetry.Prober's State() is mutex-guarded) — wiring bare Liveness live would be a data race; (2) the Send comment falsely claimed equivalence with pre-T15 remote handling (the removed pickPathLocked fell through remoteless paths; the new code fails the send on the single scheduler-chosen path). Fixed 406b007 (docs-only): PathHealth doc now requires an internally-synchronized source (*Prober); Send comment states the real behavioural narrowing + residual remoteless-Up window. R2 BOTH approve: both corrections verified accurate against source (Prober.State mutex probe.go:175-179; Liveness.State unguarded liveness.go:125; pre-T15 fall-through confirmed at c27d0e4~1); delta comment-only; gates green (build/vet/gofmt/test -race/golangci-lint 0/e2e-compile). Merged 9c4fe4e (rebased onto main). THREE out-of-scope items tracked separately: probe-transport wiring gap (both reviewers HIGH; sched.AlwaysUp placeholder; real on-wire failover inert until wired) -> NEW TASK T37 (blocks T20; added to T16/T20 dependsOn); concentrator-side failover remote-learning gap (opus MEDIUM) -> D11 (owned by T37); Liveness.State() unsynchronized (opus LOW / fable criticism) -> resolved by the doc fix (only *Prober used concurrently; T37 enforces)."
- criticism: ["[r1 fable, resolved 406b007] PathHealth doc endorsed unsynchronized bare *telemetry.Liveness as a concurrent source (race) — corrected to require *telemetry.Prober","[r1 fable, resolved 406b007] Send comment falsely claimed pre-T15 remote-handling equivalence — corrected to state the deliberate narrowing + residual remoteless-Up window"]
- new_questions: []
- ledgerRefs: ["tasks:T15","goals:G1","tasks:T37"]

### R20 — go-ahead

- createdAt: 2026-07-07T00:14:37.750Z
- updatedAt: 2026-07-07T00:14:37.750Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- summary: "T37 (per-path probe transport driving T13 liveness over the multipath Bind) reconciled go-ahead (opus+fable panel, UNANIMOUS round 1). Both independently verified against source + tests: (1) the new frame.Probe.IsEcho wire bit is INSIDE the MAC-covered region — TestTamperedRejected flips every post-kind byte to ErrAuth, so the probe/echo discriminant is unspoofable without the PSK and cannot be flipped to convert echo<->probe; (2) D9 CLOSED — removed the unauthenticated-DATA setRemote; per-path remote is now learned ONLY from MAC-verified probe/echo frames (forged DATA can no longer repoint return traffic), with DATA->virt-endpoint pinning retained as a deliberate safe identity split (Send routes by per-path authenticated getRemote, never the virt address); (3) D11 CLOSED — concentrator learns a backup path's remote from an authenticated probe before it becomes active (probe-only path getRemote() true; post-failover Send usable); (4) reflection is authenticated-only + 1:1 + anti-replayed + IsEcho-guarded against reflect-of-reflect (no amplification/loop, attacker without PSK triggers nothing); (5) NO bring-up deadlock — emitProbes iterates m.paths independently of liveness so a Down path bootstraps to Up (cold Down->Up + blackhole->Down->failover both tested on a fake clock; bring-up ~UpAfterSuccesses*interval~=750ms << WG 5s handshake retransmit); (6) T12 concurrency preserved (emitProbes snapshots under m.mu then lock-free I/O mirroring Send; probers/reflector set once in NewMultipath; lock-free virt fast path + atomic dst untouched; -race clean); (7) anti-replay D4 non-vacuous. AlwaysUp replaced with live per-path *telemetry.Prober in device.buildScheduler. Full gate green (build/vet/gofmt/test -race/golangci-lint 0/e2e-compile). Merged 03c8651. Resolves D9 + D11. opus filed ONE out-of-scope HIGH defect (D12): probe anti-replay has no session epoch, so a peer RESTART deadlocks liveness (survivor's stale high-water rejects the restarted peer's seq-from-0 probes as replay) until seq catches up — out of T37's acceptance (cold bootstrap + blackhole, both from empty high-waters, pass); fix is a wire change owned by NEW task T38."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T37","goals:G1","defects:D9","defects:D11","defects:D12"]

### R22 — go-ahead

- createdAt: 2026-07-07T11:54:50.397Z
- updatedAt: 2026-07-07T11:54:50.397Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- summary: "T38 (probe anti-replay session epoch, fixes D12 peer-restart liveness deadlock) reconciled go-ahead (opus+fable panel, 2 rounds; the v1 design was REPLACED). R1: opus approve, fable DISAPPROVE — fable REPRODUCED a session-seizure bypass in the v1 peer-chosen-random-SessionID design: the Reflector adopted ANY authenticated probe bearing a never-observed SessionID as current + reset the high-water, but UNPREDICTABILITY IS NOT FRESHNESS — a passive eavesdropper (no PSK) obtains never-seen authenticated probes (responder restart wipes in-memory state; probes lost-in-transit reach the attacker not the responder; prior boots) and replaying ONE seized current, permanently locking out the legit peer (a DoS worse than the D12 deadlock it fixes); v1 also grew guards[(pathID,SessionID)] unbounded. [opus's v1 approve MISSED this — it wrongly assumed never-seen => freshly-PSK-minted.] REDESIGN (667f568, merged c64d794): RESPONDER-CONTRIBUTED CHALLENGE. Session-epoch reset is gated on the peer echoing the responder's confidential, MAC-covered, per-adoption-ROTATED issuedChallenge (drawn non-zero, inside obf(body) — only a PSK holder can read it), so a replay (which cannot know the live challenge) can NEVER authorize a reset. acceptLocked: S==session -> D4 strict-monotonic; S!=session & C==issuedChallenge -> adopt+reset+rotate+reflect; S!=session & C!=issuedChallenge -> reflect-only (bootstrap), no reset. Genuine restart recovers via a bounded 2-round handshake (~2 probe intervals + RTT, within the T13 detection window / WG budget). Memory O(paths) (paths map[uint8]*reflectorPath ≤256, NO retired-set). R2 BOTH approve: opus traced every acceptLocked branch (only the challenge-gated branch mutates high-water; forgeries die at frame.Decode's MAC); fable RE-RAN its r1 seizure reproduction + 3 fresh attack tests (wrong-challenge/rotation/stale-echo-rollback) — all fail to seize/stall. Verified: challenge confidential+unforgeable; rotation blocks captured-adoption-probe re-adoption; prober updates learnedChallenge only AFTER guard.Accept (no backward-roll stall); non-zero draw excludes the Challenge=0 bootstrap sentinel; no new redraw-flood/amplification DoS; D4 + T37 (IsEcho/reflection/remote-learning) preserved; SessionID+Challenge MAC-covered (TestTamperedRejected extended); -race clean. Merged c64d794 (rebased onto main; multipath.go/probe_test.go conflict with T18 resolved preserving BOTH the resequencer wiring and the challenge/reflect handling; full suite + -race + e2e-compile green). Resolves D12."
- criticism: ["[r1 fable, REDESIGNED 667f568] v1 session-seizure bypass — peer-chosen-random SessionID: a replayed never-observed authenticated probe seized `current` and permanently locked out the legit peer (unpredictability != freshness). Replaced with a responder-contributed confidential rotating challenge gating the epoch reset.","[r1 fable, resolved 667f568] v1 unbounded guards[(pathID,SessionID)] retired-set — eliminated (O(paths), no retired-set) by the challenge design"]
- new_questions: []
- ledgerRefs: ["tasks:T38","goals:G1","defects:D12"]

### R23 — go-ahead

- createdAt: 2026-07-07T12:57:34.605Z
- updatedAt: 2026-07-07T12:57:34.605Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- summary: "T16 (edge public-IP change survival / per-path re-roaming) reconciled go-ahead (opus+fable panel, 2 rounds). T37 already relearns the concentrator's per-path remote from AUTHENTICATED probes; T16 found + fixed the EDGE-side gap: a source-IP-pinned socket fails ENETUNREACH when its own address changes, so re-roam device-binds each path socket to its interface (SO_BINDTODEVICE + wildcard source, best-effort with source-IP fallback for loopback/unprivileged/non-Linux). Re-roam vs peer-restart distinction verified (same SessionID -> within-session, no T38 epoch reset, anti-replay high-water persists; outer-seq monotonic -> no T18 resequencer resync; virt endpoint pinned once -> WG session preserved). R1: opus approve (all invariants hold; filed the same-interface EADDRINUSE as a LOW UNVERIFIED defect), fable DISAPPROVE — EMPIRICALLY reproduced (unshare -Urn) 2 regressions from UNCONDITIONAL device binding: (1) two paths' sources on ONE interface with a fixed port fail Open with EADDRINUSE (device-bind collides + source-IP fallback ALSO collides with the wildcard+device socket) — a legal config that worked pre-T16; (2) wildcard+device silently VOIDS the source_addr pin on a multi-address interface (kernel route-based source selection). Fixed 349065b: device binding is now CONDITIONAL — device-bind ONLY when the interface holds EXACTLY ONE address of the source family AND EXACTLY ONE path claims that interface (selectDeviceBinds/planPathBinds, pure + injected resolver); else source-IP-bind (pre-T16 behavior). The single-path-per-interface single-address uplink (the real mobile scenario) still device-binds so roam survives. R2 BOTH approve: opus verified the selection gates close both criticisms by construction; fable EMPIRICALLY re-ran the repro (fails at 4ab7681 / passes at 349065b) AND ran the acceptance e2e on real hardware (llm-ubuntu-0: primary re-roamed 10.100.1.1->.111 mid-transfer, recovered, transfer completed 15.1 Mbit/s NO reset, other path undisturbed; full -tags e2e suite green twice). Gates green (build/vet/gofmt/test -race/golangci-lint 0/e2e-compile). Merged 349065b (fast-forward). 2 low out-of-scope defects filed (D13 IPv6-link-local-never-device-binds coverage gap; D14 e2e-harness back-to-back teardown race)."
- criticism: ["[r1 fable, resolved 349065b] unconditional device binding regressed same-interface multi-path configs (EADDRINUSE, empirically reproduced) — made device-bind conditional on exactly-one-path-per-interface","[r1 fable, resolved 349065b] unconditional device binding voided source_addr pinning on multi-address interfaces — gated on familyCount==1"]
- new_questions: []
- ledgerRefs: ["tasks:T16","goals:G1"]

### R24 — go-ahead

- createdAt: 2026-07-07T13:30:00.904Z
- updatedAt: 2026-07-07T13:30:00.904Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- summary: "T30 (runtime path add/remove + SIGHUP config reload) reconciled go-ahead (opus+fable panel, 4 rounds — each caught a real reproduced fault). Architecture: a RECEIVE FAN-IN — the Bind owns one readLoop per path feeding the shared T18 resequencer, and Open returns a SINGLE engine-facing drainer ReceiveFunc, so a path added at runtime gets a Bind reader WITHOUT the engine spawning a goroutine (WG session/virtual endpoint untouched). AddPath/RemovePath + DynamicScheduler + SIGHUP reload; per-path prober moved to pathState (shared-slice race fix). R1: opus approve, fable DISAPPROVE — escalated opus's 'latent' defect to blocking: runtime mutations desync the scheduler across the bind's DOCUMENTED Close->Open cycle (a runtime-added path leaves a frozen-StateUp scheduler entry at index>=len(m.paths) -> Pick pins to it -> every Send errNoHealthyPath = TOTAL EGRESS OUTAGE; a removed boot path is resurrected with no scheduler entry -> silent failover loss); + race-test vacuity (silent break masks AddPath errors), cross-goroutine t.Fatalf in e2e, silent reload diff-drops. Fixed 8d81f2b: AddPath/RemovePath keep m.defs+m.probers index-aligned with m.paths, Open reconciles the scheduler via new DynamicScheduler.SetPaths; race-test Fatalf; e2e channel-assert + explicit zero-reset; reload logs a warning per ignored change. R2: fable approve, opus DISAPPROVE — reproduced a NEW defect the r2 fix EXPOSED: PathID collision after remove->reopen->add (Open reset nextPathID=len(paths) while survivors kept higher stamps -> a runtime add re-mints a colliding PathID -> two live paths at same (PathID,SessionID) corrupt the peer Reflector's per-PathID anti-replay/challenge -> probe loss/false-DOWN). Fixed 347df43: nextPathID is a monotonic high-water carried across Open (>= max live stamp+1, never lowered; uint8-exhaustion fail-fast), ps.id reconciled to the immutable prober stamp (survivor never renumbered, DATA/PROBE agree); + folded fable's low defect (NewMultipath rejects newProber!=nil && probers==nil). R3: opus approve, fable DISAPPROVE — stale per-Open-span exhaustion doc comment + error string contradict the new cross-span invariant + misdirect the operator remedy. Fixed c917310 (+ struct-field comment). R4: fable DISAPPROVE on one residual comment inaccuracy ('256 AddPath admissions' vs actual 256-minus-initial-N); addressed VERBATIM per fable's supplied wording at merge (comment-only; opus-approved logic byte-identical). Crux concurrency verified sound throughout (fan-in no lost-wakeup, Close teardown leak-free, dynamic-set under m.mu, race test -count up to 8). Merged c3fa6e2 — rebased onto main resolving the multipath.go conflict between T30's receive FAN-IN and T16's conditional device-binding in Open (kept T16's planPathBinds/bindDevs bind-plan + dropped the T30 fan-in's now-unused per-path fns accumulator); full race suite (bind/sched/device/telemetry/reseq/frame) + crux race x3 + golangci-lint 0 + e2e-compile all green post-merge. NOTE: AddPath source-IP-binds a runtime path (net.ListenUDP, no device-binding) — acceptable (device-bind roam-survival is a boot-path concern; runtime-add is a distinct feature)."
- criticism: ["[r1 fable, resolved 8d81f2b] runtime mutations desync scheduler across Close->Open -> total egress outage / silent failover loss; +race-test vacuity, e2e cross-goroutine Fatalf, silent reload diff-drops","[r2 opus, resolved 347df43] PathID collision after remove->reopen->add (nextPathID reset to len while survivors kept higher stamps) -> corrupts peer per-PathID anti-replay/challenge; fixed via monotonic high-water + prober-stamp reconcile","[r3 fable, resolved c917310] stale per-Open-span exhaustion doc/error-string contradicting the cross-span high-water + misdirecting the operator remedy","[r4 fable, resolved at merge verbatim] residual comment inaccuracy: '256 AddPath admissions' (actual capacity 256 minus initial-Open path count)"]
- new_questions: []
- ledgerRefs: ["tasks:T30","goals:G1"]

### R25 — go-ahead

- createdAt: 2026-07-07T15:46:58.773Z
- updatedAt: 2026-07-07T15:46:58.773Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- summary: "T39 (meet the P1 3s failover budget in BOTH directions; fixes D15 HIGH + D16) reconciled go-ahead (opus+fable panel, 2 rounds). ROOT CAUSE (found on hardware): bidirectional failover needs BOTH ends to independently mark the killed path DOWN via their probe-liveness Tick, which rode ONE wall-clock ticker goroutine (StartProbeLoop->emitProbes); on the concentrator (iperf3 server absorbing the forward flood on 4 vCPU) that goroutine was scheduled with ~1s jitter under GOMAXPROCS saturation, delaying the reply-direction switch past 3s in ~30% of runs. FIX: tickLivenessFromReceive — a throttled (atomic-CAS, <=once/interval) liveness sweep driven off the per-path RECEIVE goroutines (scheduled by the very traffic that must trigger failover), so DOWN-detection no longer waits on a starved timer; uses TryLock (NOT Lock) to preserve Close's 'a reader never blocks on m.mu' invariant (Close holds m.mu while waiting on readersWG — a Lock would deadlock); ticks OUTSIDE the lock. Monotone-safe: Tick only marks an UP path DOWN once silence STRICTLY exceeds DownAfter and never brings a path UP, so a more frequent tick only lands a GENUINE DOWN sooner, never a false one; failback hysteresis untouched. Plus a modest timing tighten (1500/250 -> 1200/200ms) keeping the identical 6:1 six-lost-echo false-down tolerance. D16 reconciled: device.go + thresholds.go now read one telemetry.Default* source of truth, and the composition analysis budgets BOTH directions (max(edge,conc)). New sound TestP1Failover (failover_test.go) measures per-direction recovery from each daemon's scheduler-transition log timestamp (sub-ms, un-confounded — an ICMP-gap probe is unusable: it shares the netem queue with the saturating flow and measures congestion, not failover) + a data-plane iperf-bidir survival cross-check, strict < P1RecoverySeconds. R1: fable disapprove (the new concurrent sweep had NO -race unit coverage — no-op in bind unit tests since sweepIntervalNanos==0 unless StartProbeLoop runs; exercised only by the non-race e2e; acceptance requires -race unit tests) + filed D17 (TestPSKMismatchRejected flake); opus disapprove (thresholds D16 budget prose ~1.4s contradicted the 1.6s constant). Fixed dda1ce9: 3 -race bind sweep tests (throttle-coalesces-to-one; starved-timer receive-driven DOWN; no-deadlock-vs-Close), each MUTATION-VERIFIED non-vacuous (throttle off -> 64 Ticks; TryLock->Lock -> Close hangs + 5s guard fires); thresholds prose reconciled to the 1.6s constant (1.4s detect + 1-interval headroom, 1.4s jitter margin). R2 BOTH approve (concurrency crux -race-clean, Prober.mu serializes Liveness, no lock-order inversion; prose consistent). HARDWARE: implementer 42/42 <3s (worst 2464ms), fable independent 16/16 (recovery max 2099ms, conc_switch max 1970ms) — D15 CLOSED (vs the prior 4/14 >3.1s). Merged c79a95b. Resolves D15 + D16. D17 (out-of-scope frame test flake) separately diagnosed as a test-assertion bug (crypto sound) + resolved af31005."
- criticism: ["[r1 fable, resolved dda1ce9] the new concurrent receive-tick sweep (tickLivenessFromReceive: CAS throttle + TryLock deadlock-avoidance) had NO -race unit coverage — added 3 mutation-verified non-vacuous -race bind tests","[r1 opus, resolved dda1ce9] thresholds.go D16 budget prose (~1.4s budget/~1.6s margin) contradicted the 1.6s constant — reconciled to 1.4s detect + headroom = 1.6s budget, 1.4s margin"]
- new_questions: []
- ledgerRefs: ["tasks:T39","goals:G1","defects:D15","defects:D16"]

### R26 — go-ahead

- createdAt: 2026-07-07T18:04:11.185Z
- updatedAt: 2026-07-07T18:04:11.185Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- summary: |
    T40 (fix D18 repeated-flap wedge + sound flap acceptance) reconciled GO-AHEAD (opus by-construction + fable validation-soundness panel; strictest-wins → unanimous, 0 criticisms / 0 questions / 0 defects). Merged d4047a7 (delta 00c1124..20b955c).
    
    ROOT CAUSE (D18): failover was PULL-BASED — scheduler.Pick() ran ONLY from the Send path, so a repeated-flap kill during an egress lull (both TCP dirs stalled, no Send) left the bond wedged on the dead path until the 25s WG keepalive (~1/6 per-kill wedge at load). FIX: nudgeSchedulerActive() calls Pick() from the receive-liveness tick (tickLivenessFromReceive) AND the probe emitter (emitProbes), making failover EAGER/liveness-driven — a DOWN detected off receive/probe immediately triggers the switch without an application Send. Also lands the repeated-flap e2e acceptance (TestP1FailoverRepeatedFlap), the D21 flap-test load-flow leak reap, a widened OBSERVATION window (flapFailoverPoll=P1RecoverySeconds+5s; 3s budget still asserted per cycle), and +147 lines of -race sweep unit tests.
    
    opus (by-construction): nudge is lock-order-safe — takes only the scheduler s.mu, both call sites run after m.mu.Unlock (multipath.go:498, probe.go:38), so the Send-path m.mu->s.mu order is never inverted and Close/readersWG cannot deadlock (T39 TryLock invariant untouched). Pick/setActiveLocked idempotent (no-op + no log when active unchanged), failback dwell preserved. -race sweep tests proven NON-VACUOUS by nudge-revert (TestSweepDrivesEagerFailover + throttle-count both fail).
    
    fable (validation-soundness): widened flapFailoverPoll is observation-only; 3s per-cycle budget still asserted (failover_test.go:286) from daemon scheduler-transition timestamps (real latency); measured 1027-1364ms at the eager-detect floor discriminates fixed-from-masked. The no-Send D18 condition is exercised DETERMINISTICALLY by TestSweepDrivesEagerFailover (zero Sends, hour FailbackAfter pins the tick as the only mover) — overlay-verified to FAIL on pre-fix c495839 ('the tick did not eagerly fail over', multipath_sweep_test.go:276) and pass fixed. Failback asserted per cycle both ends. Load 0.6-1.9 >= the 0.55 repro point. Re-ran build/vet/fmt/lint/test -race green.
    
    HARDWARE (llm-ubuntu-0, amd64, bidirectional saturating load): flap 22/22 PASS (0 wedged, 0 budget-exceeded, max 1364ms vs 3000ms); single-kill 10/10 PASS (1135-1320ms, all < 1600ms failover budget). Resolves D18 + D21; completes the T20 flap acceptance.
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T40","tasks:T20","defects:D18","defects:D21","goals:G1"]

### R27 — go-ahead

- createdAt: 2026-07-07T18:20:27.444Z
- updatedAt: 2026-07-07T18:20:27.444Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- summary: "T22 (systemd units + static cross-compile release + install doc + P1 checklist) go-ahead after 1 fix round. Merged 38c8756. opus reviewer verified by construction + by execution: `just release` produces dist/wanbond-linux-{amd64,arm64} both ELF statically-linked correct-arch stripped (CGO_ENABLED=0 clean — amneziawg-go/tun is pure-Go AF_NETLINK, no cgo); both systemd units pass `systemd-analyze verify` (only host-path artifacts on the NixOS build host); hardening (CAP_NET_ADMIN + DeviceAllow=/dev/net/tun rw + RestrictAddressFamilies AF_NETLINK + SystemCallFilter=@system-service, ProtectSystem=strict does not touch /dev) does NOT block TUN creation; SIGHUP ExecReload maps to a signal main.go handles; install-doc config keys all match internal/config, 0600-exact matches load.go requiredMode, tunnel-interface ACCEPT rule matches test/realhosts/provision.go byte-for-byte. R1 DISAPPROVE (1 criticism): docs/install.md §4 recommended `ExecStartPost=ip address add … dev wanbond0` which RACES daemon TUN creation under Type=exec (wanbond0 absent at execve → 'Cannot find device' → un-prefixed ExecStartPost fails the unit → Restart=on-failure crash-loop; no sd_notify readiness). FIXED 73f34e7: replaced with a race-free systemd-networkd .network file ([Match] Name=wanbond0 / [Network] Address=…) that applies addressing when the interface appears, plus an explicit warning against the racing ExecStartPost. Doc-only fix, gate unaffected (no .go touched); release re-verified static+correct-arch on the merged tree."
- criticism: ["[r1 opus, resolved 73f34e7] docs/install.md §4 ExecStartPost `ip address add dev wanbond0` raced daemon TUN creation under Type=exec → fail/crash-loop — replaced with race-free systemd-networkd .network addressing + a warning against the ExecStartPost approach"]
- new_questions: []
- ledgerRefs: ["tasks:T22","goals:G1"]

## M10

### R9 — go-ahead

- createdAt: 2026-07-06T22:08:37.223Z
- updatedAt: 2026-07-06T22:08:37.223Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- summary: "T31 (realhosts e2e tier) reconciled go-ahead (opus+fable panel). R1: opus approve, fable disapprove (2 valid criticisms — `just realhosts` replayed the go-test cache without -count=1, reproduced empirically; connectivityTimeout comment misstated its scope). Both fixed in ebf95d5 (added -count=1 to the realhosts recipe AND the pre-existing e2e/e2e-run recipes, closing the cache-replay defect; corrected the comment). R2: fable approve (confirmed via `just --dry-run` that all three recipes carry -count=1; comment matches behavior; checks green). Build-tag separation complete (every test/realhosts file //go:build realhosts; untagged + `-tags e2e` builds exclude it); SSH runner safe (`-F none` bypass, correct env defaults, no injection/leak/destructive ops); report-only + opt-in confirmed; real SSH connectivity run verified (edge=x86_64, concentrator=aarch64)."
- criticism: ["[r1 fable, resolved r2] `just realhosts` omitted -count=1 — go-test cache replayed a stale PASS; fixed on all three recipes","[r1 fable, resolved r2] connectivityTimeout comment misstated single-vs-shared ctx scope; corrected"]
- new_questions: []
- ledgerRefs: ["tasks:T31","goals:G1"]
- sessionLogs: [".cq/logs/20260706-220000-ab78416a54b9fd747.md",".cq/logs/20260706-220000-a9d08b86138121cb3.md",".cq/logs/20260706-220000-a297f9214a676fd4c.md"]

### R10 — go-ahead

- createdAt: 2026-07-06T22:19:45.944Z
- updatedAt: 2026-07-06T22:19:45.944Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- summary: "T35 (netns fixture bandwidth-cap + controlled-loss knobs) reconciled go-ahead (opus+fable panel, 2 rounds). R1: opus approve; fable disapprove (2 valid criticisms — lossy-path self-test flaky ~8% because iperf3's TCP control channel shared the 10%-lossy link -> backoff stalls distorted the loss; A7 doc note mislabeled 50 Mbit/s as a pathSpec default). Both fixed in 7383ca0: loss measurement moved to ping/ICMP (netem loss is edge-egress, so a dropped echo-request directly reflects the configured drop rate, no control channel to stall; 500 probes, 5-18% band ~+-3.7sigma escape 1e-4); doc reworded. HARDWARE-VERIFIED on o3: TestFixtureImpairment 5/5 PASS all ~10.07s (flake gone; earlier flaky runs took +26s). R2: fable approve (confirmed edge-egress reasoning, statistical soundness, doc fix, full check suite green). Backward-compat byte-identical (rateMbit/lossPct default 0), capped-path iperf3 assertion untouched, A7 follow-up superseded into T35 (no duplicate)."
- criticism: ["[r1 fable, resolved r2] TestFixtureImpairment lossy-path flaky ~8% (iperf3 TCP control channel on the lossy link) — switched to ping/ICMP loss (verified 5/5 on o3)","[r1 fable, resolved r2] docs/p0-checkpoint.md A7 note mislabeled 50 Mbit/s as a default — reworded (defaults are 0; 50 is the self-test cap)"]
- new_questions: []
- ledgerRefs: ["tasks:T35","goals:G1"]
- sessionLogs: [".cq/logs/20260706-220000-aa4ce7b7518ab1cfd.md",".cq/logs/20260706-220000-a5b2b9b863a40779d.md",".cq/logs/20260706-221500-a139ea4c25eeab49c.md",".cq/logs/20260706-221500-aacca56552aa07ae9.md"]

### R11 — go-ahead

- createdAt: 2026-07-06T22:27:40.143Z
- updatedAt: 2026-07-06T22:27:40.143Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- summary: "T32 (idempotent real-host provisioning + concentrator firewall) reconciled go-ahead (opus+fable panel, UNANIMOUS r1). Both independently RE-RAN TestRealProvision LIVE (opus corroborated; fable PASS 11.25s) and verified: genuine check-then-act idempotency (every mutation predicate-guarded via command -v / go-version-grep / iptables -C, post-install re-verify; second pass no-op, Changed()==false); SAFE (additive `iptables -I INPUT -i wanbond0 -j ACCEPT` before REJECT cannot sever SSH; scoped `rm -rf /usr/local/go` gated behind go-absent + curl-fails-closed; NO OCI lifecycle ops); no shell-injection (constants + validated uname-m arch enum); correct -C/-I insert-before-REJECT; build-tag hygiene (//go:build realhosts excluded from untagged/e2e). Acceptance met operationally. Filed 2 out-of-scope defects: D7 (firewall rule not reboot-persistent, medium -> T22 install doc + persistence step), D8 (pre-existing o3 INPUT-chain duplicates from this session's manual P0 bring-up, low -> host cleanup)."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T32","goals:G1"]
- sessionLogs: [".cq/logs/20260706-222500-a272f5360504eaf37.md",".cq/logs/20260706-222500-a2fa37e32b9886c2c.md",".cq/logs/20260706-222500-a097aa48cda8e782e.md"]

### R13 — go-ahead

- createdAt: 2026-07-06T22:52:47.344Z
- updatedAt: 2026-07-06T22:52:47.344Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- summary: "T36 (controlled-loss single-flow-TCP-collapse FEC baseline: netns fec_baseline_test.go + docs/fec-baseline.md) reconciled go-ahead (opus+fable panel, 2 rounds). R1 fable disapprove — 4 valid criticisms: (1) unpinned congestion control (BBR would not collapse under loss, defeating the Mathis baseline); (2) measured sum_sent not sum_received (sender-side overcounts goodput on a lossy link); (3) vacuity hole (sweep could pass trivially if loss never applied); (4) doc citation malformed. All fixed (rework, merged 6ebc288). R2 BOTH approve, independently verified: --congestion cubic pinned on the iperf3 client via fail-fast runOut (fecCongestionControl); fecIperf3RecvMbps reads end.sum_received; non-vacuity enforced by three runtime guards (fecLossSweep[0]!=0 Fatalf + positive-baseline Fatalf + collapse gate frac<0.5 that FAILS not passes if loss unapplied; InjectLoss itself Fatalf's on tc error — no silent no-op); citation now points to the G1 follow-up + HO5 carrying the exact figures. o3 re-run numbers 45.1 (0% loss) -> 7.3/5.4/3.7 Mbit/s (fractions 0.16/0.12/0.08) are internally consistent and Mathis 1/sqrt(p)-conformant, zero placeholders. Report-only honored (asserts the pre-FEC collapse problem manifests; does NOT gate FEC recovery — the baseline T25/T29 is measured against). Full check (build/vet/gofmt/e2e-compile/golangci-lint) green. Merged 6ebc288 (fast-forward, rebased on main)."
- criticism: ["[r1 fable, resolved] unpinned CC — BBR masks loss; pinned --congestion cubic on the sender","[r1 fable, resolved] measured sum_sent not sum_received — switched to end.sum_received (receiver goodput) via T36-local fecIperf3RecvMbps","[r1 fable, resolved] vacuity hole — added fecLossSweep[0]==0 anchor Fatalf + positive-baseline Fatalf + substantive frac<0.5 collapse gate","[r1 fable, resolved] doc citation malformed — cite G1 follow-up + HO5 with the exact live figures; Mathis stated as upper bound goodput<=MSS/(RTT*sqrt(p))"]
- new_questions: []
- ledgerRefs: ["tasks:T36","goals:G1"]

### R14 — go-ahead

- createdAt: 2026-07-06T23:01:30.519Z
- updatedAt: 2026-07-06T23:01:30.519Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- summary: "T33 (real-host P0 single-uplink smoke: test/realhosts/smoke_test.go, //go:build realhosts) reconciled go-ahead (opus+fable panel, 2 rounds). R1: opus approve (genuine through-tunnel verification — inner-IP ping for handshake + iperf3 -B bound to inner IP; safe teardown; report-only; no injection; live run passed RTT 31.6ms, TCP 24.3/91.0, UDP), fable disapprove with 4 objective criticisms: (1) teardown contract overclaimed — conc.toml/edge.toml (WG keys+PSK) + synced tree under smokeRemoteDir persisted after the run; (2) writeRemoteFile chmod-after-write left a world-readable window; (3) tar sync shipped untracked .claude/.codegraph/.worktrees/result (incl. other agents' worktrees) to the remote hosts; (4) UDP metric mislabeled the sender offered rate as goodput. All 4 fixed in b4ba329 (rebased to e34e0ac on merge). R2 BOTH approve: removeRemoteDir t.Cleanup registered on both hosts before every secret write — LIFO ordering tears daemons/iperf down (synchronous systemctl stop) BEFORE rm -rf, and fires on Fatalf/panic; umask 077 creates secret files at 0600 before any byte lands; tar excludes anchored/additive; UDP goodput=sendMbps*(1-lost_percent/100) with the correct JSON field, report-only. No injection (constant path). Static gates green (build/vet/vet-realhosts/gofmt/test/lint 0); live realhosts tier not runnable from the review workers (verified statically). Merged e34e0ac (rebased onto main)."
- criticism: ["[r1 fable, resolved b4ba329] teardown overclaim — secret configs + synced tree persisted; added removeRemoteDir t.Cleanup on both hosts (LIFO-safe vs daemon teardown)","[r1 fable, resolved b4ba329] chmod-after-write world-readable window — umask 077 && cat > path && chmod 600","[r1 fable, resolved b4ba329] tar shipped untracked agent state — excludes .claude/.codegraph/.worktrees/result","[r1 fable, resolved b4ba329] UDP metric mislabeled offered rate as goodput — compute sendMbps*(1-lost_percent/100)"]
- new_questions: []
- ledgerRefs: ["tasks:T33","goals:G1"]

### R28 — go-ahead

- createdAt: 2026-07-07T18:43:33.350Z
- updatedAt: 2026-07-07T18:43:33.350Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- summary: |
    T34 (real-host multipath/failover validation, realhosts tier) go-ahead after 1 fix round (2 review rounds). Merged 42caa3b. The test gives the NAT'd edge two source-IP paths (secondary uplink addr + per-source policy-routing tables) to the one concentrator endpoint, brings up the P1 multipath bond over the real internet, confirms both paths reach liveness up, blackholes the active path mid-flow, asserts a long-lived TCP transfer survives, records edge-side failover time. Report-only per Q12 (gates nothing).
    
    R1 opus DISAPPROVE (3 criticisms): (1) CRITICAL self-SSH-severance — the blackhole was a table-wide `default` in the primary path's table (5210), but path-0 binds the host's management-SSH source IP, so the blackhole would drop the SSH-reply channel → sever control SSH with NO in-band recovery (teardown runs over the severed SSH) → brick the standing shared host + leak routing state + force a false failure (the iperf3 -J report rides that SSH). (2) MEDIUM cross-host clock comparison (runner time.Now() vs edge slog timestamps → NTP-skew corruption + spurious failover<0 fail). (3) LOW default-only policy tables strip host-specific routes.
    
    FIXED (01810da): (a) scoped blackhole to `ip route replace blackhole <concPubIP>/32 table 5210` — only path-0's traffic TO THE CONCENTRATOR is dropped; management SSH (dest = control host ≠ concPubIP) keeps routing via the mirrored default; (b) copyMainRoutes makes tables 5210/5211 faithful mirrors of the main table (folds in #3); (2) single edge-clock domain via edgeClockNow (`date -u +%s%3N`) captured before the kill vs edge slog timestamps.
    
    R2 opus APPROVE (0 criticisms): full SSH-safety walk confirms `src=primaryIP,dst=controlHost` is never dropped in any of the four states (setup / blackholed / restored / teardown), copyMainRoutes faithful, teardown registered pre-mutation + idempotent + partial-state-safe, distinct-4-tuple property preserved, single-clock timing correct, vet+gofmt clean. Filed one LOW out-of-scope defect: the controlHost≠concPubIP precondition was documented but unguarded. ORCHESTRATOR HARDENING (1e3cea2): added assertControlHostNotConcentrator — a fail-fast guard that reads the edge's $SSH_CLIENT and aborts BEFORE any mutation if the edge sees the control SSH arriving from concPubIP (converts the precondition into a by-construction guard against the irreversible sever). Gate re-verified green with the guard.
- criticism: ["[r1 opus, resolved 01810da] CRITICAL self-SSH-severance: table-wide `blackhole default table 5210` behind `ip rule from primaryIP` dropped the management-SSH reply channel (path-0 binds the host primary IP) → severed control SSH, no in-band recovery, bricked host + leaked routing state + false failure — scoped to `blackhole concPubIP/32` + faithful main-table mirrors so management SSH survives every state","[r1 opus, resolved 01810da] MEDIUM cross-host clock comparison (runner time.Now() vs edge slog timestamps) — replaced with a single edge-clock-domain T0 marker (date -u +%s%3N) vs edge slog time","[r1 opus, resolved 01810da] LOW default-only policy-routing tables strip host-specific routes — tables now faithfully mirror the main table's unicast routes","[r2 opus low/out-of-scope, hardened 1e3cea2] controlHost≠concPubIP precondition documented but unguarded — added a fail-fast $SSH_CLIENT guard before any mutation"]
- new_questions: []
- ledgerRefs: ["tasks:T34","goals:G1","defects:D7"]

## M6

### R16 — go-ahead

- createdAt: 2026-07-06T23:16:48.230Z
- updatedAt: 2026-07-06T23:16:48.230Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- summary: "T17 (localhost Prometheus /metrics endpoint with per-path telemetry: internal/metrics) reconciled go-ahead (opus+fable panel, 2 rounds). R1: opus approve (loopback guard fail-closed across IP-literal/wildcard/empty-host/IPv6/hostname-to-routable; per-path RTT/jitter/loss/state read verbatim from telemetry.Estimate/PathState via a Source at scrape time; dedicated prometheus.NewRegistry, no globals; metric names/types Prometheus-valid; non-vacuous HTTP-scrape value assertions; FEC counters registered-now/zero honest; gates green incl. nix build/vendorHash), fable DISAPPROVE — 1 valid criticism: check-then-act TOCTOU in the loopback guard's hostname branch (requireLoopback validated via net.LookupIP but net.Listen re-resolved independently, so a divergent/attacker-influenced resolver could bind a routable interface despite the pre-check). Fixed c59851b: fail-closed act-then-verify — after net.Listen, verifyLoopbackBind(ln.Addr()) asserts the KERNEL-reported bound address IsLoopback, closes the listener + returns ErrNonLoopbackBind otherwise (resolver-independent); pre-Listen requireLoopback retained as belt-and-braces. R2 BOTH approve: verify runs inside NewServer BEFORE the Server exists / any Serve, so no bind-then-serve window; error path closes the listener (no fd leak); comma-ok assertion rejects non-*net.TCPAddr without panic (UnixAddr-tested); new tests (TestVerifyLoopbackBind 6 subcases + TestHostnameBindVerifiedLoopback via real localhost:0) non-vacuous; delta confined to server.go/server_test.go, r1 properties untouched. Full gate green (build/vet/gofmt/test -race/golangci-lint 0). Merged 429c760 (rebased onto main; prometheus dep vendorHash unchanged). Minor non-blocking: a test subcase mislabels 127.255.255.254 as 'low'."
- criticism: ["[r1 fable, resolved c59851b] loopback-guard TOCTOU — requireLoopback (LookupIP) then net.Listen re-resolved independently; fixed with fail-closed post-bind verifyLoopbackBind(ln.Addr()) on the kernel-bound address + listener cleanup"]
- new_questions: []
- ledgerRefs: ["tasks:T17","goals:G1"]

### R21 — go-ahead

- createdAt: 2026-07-07T01:18:31.605Z
- updatedAt: 2026-07-07T01:18:31.605Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- summary: "T18 (receive resequencing buffer, internal/reseq: bounded-window + timeout, wired into the multipath Bind before WG delivery) reconciled go-ahead (opus+fable panel, 3 rounds). R1: BOTH disapprove — the sound core (ordering/dedup/window/timeout/bounded-ring/-race/PROBE-isolation/T24-FEC-seam) was verified correct, but Observe unconditionally trusted the by-design-UNAUTHENTICATED outer-seq: (a) advanceTo was O(jump) under r.mu, so a forged/garbage far-future OuterSeq (DATA decodes as KindData p~1/256) spun ~2^62 iterations — reproduced PERMANENT hard-lock of all receive goroutines; (b) head-of-line timeout wasn't re-based across gaps (expire->arm with waiting=true) — dropped in-window reordered frames; (c) a wild-high seq OR a peer restart (outerSeq resets to 1) irreversibly advanced the release point — permanent tunnel blackhole (regression vs pre-T18). opus also filed the availability-amplification angle as a defect. Rework 978f763: advanceTo capped to O(window) + arithmetic gap-close; expire clears waiting before arm; a discontinuity/resync guard (K=resyncFactor=4, C=resyncCorroborate=3) re-pins ONLY after C corroborating suspect frames mutually spanning <window — junk seqs (independent in 2^64) don't corroborate, a genuine restart/long-outage does; subsumes the availability defect (a forged advance is bounded to (K-1)*window and self-heals). R2: opus approve; fable DISAPPROVE — the corroboration run counted CONSECUTIVE not DISTINCT seqs, so 3 copies of ONE junk frame (span 0) self-corroborated a spurious resync (reproduced Resyncs==1), defeating the documented junk-immunity invariant. Rework c73d197: require C DISTINCT suspect seqs (resyncSeqs + runContains); genuine restart (1,2,3) and outage (base,base+1,base+2) emit distinct seqs so both legit paths preserved; bounded at C, mutex-guarded. R3 BOTH approve: duplicate-seq hole closed with fail-first evidence (both new tests fail at 978f763 with Resyncs==1, pass at c73d197); resyncSeqs bounded + race-free; O(window) advance + per-gap timeout intact; full gate + -race green. Delivers strictly-monotonic outer-seq order so WG's RFC6479 inner anti-replay never sees multipath reorder. Merged c73d197. Residual active-forger disruption on the unauthenticated DATA channel is transient/self-healing and within the accepted P1 DoS-tolerant threat model (T11: DATA headers unauthenticated by design); the complete fix (outer-header auth) is out of scope."
- criticism: ["[r1 both, resolved 978f763] advanceTo O(jump) mutex spin — forged far-future OuterSeq permanent hard-lock; capped to O(window)+arithmetic","[r1 opus, resolved 978f763] head-of-line timeout not re-based across gaps — dropped in-window reordered frames; expire clears waiting before arm","[r1 fable + opus-defect, resolved 978f763] wild-seq / peer-restart release-point discontinuity — permanent blackhole; discontinuity/resync guard (K=4,C=3), bounded + self-healing","[r2 fable, resolved c73d197] corroboration run accepted DUPLICATE seqs — 3 copies of one junk frame forced a spurious resync; require C DISTINCT suspect seqs"]
- new_questions: []
- ledgerRefs: ["tasks:T18","goals:G1"]

### R29 — go-ahead

- createdAt: 2026-07-07T19:37:27.178Z
- updatedAt: 2026-07-07T19:37:27.178Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- summary: |
    T21 (weighted-aggregation scheduler + data-thrift policy) reconciled GO-AHEAD (opus by-construction + fable adversarial panel; 2 rework rounds). Merged 7cb0a86. WeightedScheduler (Scheduler+DynamicScheduler): Mathis-proxy 1/(RTT·√Loss) weights + nginx SWRR distribution under load; data-thrift collapse to primary at low load (metered 5G ~idle) with hysteretic engage/disengage band + dwell; per-path token-bucket pacing; FEC seam. Active-backup stays default; selected at device composition root; existing P1 configs load unchanged.
    
    T40 eager-failover PRESERVED for both policies: added a non-consuming Recompute() to the Scheduler interface (nudge sites repointed Pick()→Recompute() so a stateful weighted Pick is never spent on the liveness nudge); weighted routing reads live liveness fresh each Pick (never a stale active snapshot). opus verified byte-identical ActiveBackup.Recompute, lock order strictly m.mu→s.mu (scheduler never re-enters Bind), weight math + SWRR + pacing bound sound.
    
    R1: opus APPROVE (0 findings); fable DISAPPROVE (3 criticisms + D22 filed). fable REPRODUCED a data-thrift leak (requirement-2 regression): the aggregation gate only advanced inside Pick(), so an abruptly-ending overload kept the gate engaged across idle, striping the next low-rate burst onto the metered backup (20/40 frames). Strictest-wins → disapprove.
    
    FIXED (4b430a6): (1) updateGateLocked now credits the inter-Pick idle gap — gap>=CollapseDwell forces immediate collapse + belowSince backdated to now-gap + EWMA decays across the gap; new TestWeightedCollapsesAfterOverloadIdle asserts backup=0 (mutation-verified 13/40 leak without fix). (2) unwired/all-zero-Estimate path now gets the MEAN of measured weights (was the floored MAXIMUM → ~20:1 siphon), doc corrected, safe all-neutral fallback. (3) distinct PickPaced(-2) vs PickNone(-1) sentinel + errPacerShedding + coalesced 'pacer shedding' log so shedding is distinguishable from outage.
    
    R2 fable APPROVE (0 findings): probed the general case (backup frames 35→17→0 as gap grows 0→500ms, monotonic, zero at dwell — not just the one repro), verified neutral-weight edge cases (no div-by-zero/NaN), confirmed the sole Pick consumer (Multipath.Send) handles both sentinels distinctly, all 3 regression tests fail pre-fix for the right reason, no regression, vet/gofmt/lint/-race/full-suite green. Assumption surfaced (non-blocking): load/capacity in frames/sec not bytes (Pick carries no size; no measured BDP) — acceptable P2 approximation, byte-rate sizing deferred to T35/T23. D22 (pacer sheds WG control frames under overload) filed file-and-defer to T23/T35.
- criticism: ["[r1 fable, resolved 4b430a6] CRITICAL data-thrift leak (requirement-2 regression), reproduced: aggregation gate stayed engaged across idle after an abruptly-ending overload → next low-rate burst striped onto metered backup (20/40 frames) — fixed via idle-gap-forces-collapse + belowSince backdating + regression test (mutation-verified 13/40 without fix)","[r1 fable, resolved 4b430a6] unwired/all-zero-Estimate path got the floored MAXIMUM weight (~20:1 siphon) contradicting its 'neutral' doc — now mean-of-measured with safe all-neutral fallback, doc corrected","[r1 fable, resolved 4b430a6] paced-out frame surfaced as errNoHealthyPath (indistinguishable from total outage in engine logs) — added distinct PickPaced(-2) sentinel + errPacerShedding + coalesced shedding log"]
- new_questions: []
- ledgerRefs: ["tasks:T21","goals:G1","defects:D22"]

### R30 — go-ahead

- createdAt: 2026-07-07T20:20:22.920Z
- updatedAt: 2026-07-07T20:20:22.920Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- summary: |
    T23 (live /metrics surface + P2 aggregation e2e) reconciled GO-AHEAD (opus by-construction + fable measurement-validity panel; 1 rework round). Merged cbad91f. Closed the T17→T23 gap: T17 built the internal/metrics package but nothing wired it into the daemon or fed a Source over real traffic. T23 added lock-free atomic per-path tx/rx OUTER-wire byte counters in the Bind (Send tx / readLoop rx, off the m.mu-free path) + PathSnapshots(); a metrics.Source adapter deriving throughput from counter deltas; the metrics.Server wired into the daemon Tunnel lifecycle (loopback-only, started only when [metrics].listen set, closed first, rebound on reload); fixed an amnezia-guard double-release on the new Up error path (releaseOnce). TestP2Aggregation (weighted scheduler, per-path rate-capped) asserts bonded wire throughput >= P2BondedMinFraction(0.85) of the solo-sum + metered 5G tx < P2MeteredMaxByteFraction(0.01) while primary healthy, both from /metrics.
    
    opus (by-construction) APPROVE: byte counters lock-free/race-clean (Send counts only successful writes off the m.mu-free path; PathSnapshots reads Estimate/State outside m.mu — T39/T40 discipline preserved, no lock inversion); amnezia guard acquired-once/released-once across the new metrics-failure Up path (ok=true + releaseOnce, no leak, full teardown); metrics lifecycle loopback-enforced + Close-first-before-Bind-teardown + reload-rebind via a stable Source (no use-after-free); throughput derivation guarded (first-scrape 0, backward-counter 0, no div-by-zero). Full -race/vet/lint/e2e-compile green.
    
    fable (measurement-validity) DISAPPROVE r1 (2 criticisms + D23 filed): (1) the bonded>=0.85*(soloA+soloB) assertion is only well-defined when each SOLO run is LINK-bound, but nothing asserted it — on the recorded CPU-bound 1-vCPU host (12-46 Mbit/s in-fixture, p0-findings) the assertion is either vacuously passable (want=35.7 < 40 single-path cap) or a misdiagnosed failure. (2) the crypto-ceiling comment misattributed the real-internet cross-host 150-170 Mbit/s figure as the in-fixture 1-vCPU ceiling. Strictest-wins → disapprove.
    
    FIXED (d77731f, test-only — product wiring untouched): (1) runSoloSaturated now asserts each solo >= 0.9*p2RateMbit (link-bound) with a loud CPU-bound Fatalf diagnostic; this makes want = 0.85*(soloA+soloB) >= 0.85*2*0.9*cap = 1.53*cap > 1*cap (single-path), so NO single path can satisfy the bonded assertion — killing both the vacuous-pass and false-fail modes. (2) comment corrected to cite in-fixture 12-46 Mbit/s CPU-bound data + the executing-host requirement (2*cap below the host's measured in-fixture ceiling; 1-vCPU doesn't qualify at 40Mbit, 4-vCPU amd64 is the target). Arithmetic verified (1.53>1). D22 (pacer sheds WG control frames) + D23 (pre-existing 150-170 misattribution from T35, 4 locations) filed root-caused/deferred. Hardware P2 run pending on the 4-vCPU host.
- criticism: ["[r1 fable, resolved d77731f] bonded>=0.85*sum aggregation assertion lacked the solo-is-link-bound precondition it requires — vacuous-pass/misdiagnosed-fail on a CPU-bound host; added a >= 0.9*cap link-bound Fatalf guard that also forces want=1.53*cap > single-path cap","[r1 fable, resolved d77731f] crypto-ceiling comment misattributed the real-internet cross-host 150-170 Mbit/s figure as the in-fixture 1-vCPU ceiling — corrected to the recorded in-fixture 12-46 Mbit/s + the executing-host requirement"]
- new_questions: []
- ledgerRefs: ["tasks:T23","tasks:T17","goals:G1","defects:D22","defects:D23"]

### R31 — go-ahead

- createdAt: 2026-07-07T20:51:30.572Z
- updatedAt: 2026-07-07T20:51:30.572Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- summary: |
    T23 P2 e2e HARDWARE-DRIVEN follow-up chain (post-R30 merge), reconciled GO-AHEAD with hardware green. The P2 aggregation e2e went through 3 hardware-driven iterations on llm-ubuntu-0 (4-vCPU amd64) after the initial merge:
    
    (1) FIRST hardware run: the link-bound precondition (added in R30) FATAL'd — solo throughput 13.2/2.9 Mbit/s << the 40-cap. Investigated: single-flow measurement. Tried parallel streams (-P 8) → throughput got WORSE (13.2→4.2), REFUTING the single-flow-collapse hypothesis and proving the single-host netns fixture is PACKET-PROCESSING/CPU-bound (both userspace-WG daemons + load + netem share cores), NOT congestion-bound. Conclusion: bonding two paths on ONE shared-CPU host cannot exceed the single-path ceiling by their sum — the bonded-THROUGHPUT ratio is not measurable in-fixture.
    (2) RESTRUCTURE (53952f3): bonded-ratio subtest now SKIPS with measured evidence when not link-bound (environmental limit, not product defect), stays ENFORCED on any link-bound venue; data-thrift stays enforced. fable focused-review APPROVED the skip as honest (teeth intact, non-vacuity preserved) but found a COVERAGE GAP: the far-end both-paths cross-check lived inside the skipped subtest → nothing proved concurrent two-path socket carriage (unit tests cover only Pick-index distribution; P1 is active-backup).
    (3) STRIPING SUBTEST (6a3cb6c): added TestP2Aggregation/bonded-striping — fixture-scaled gate p2StripingCapacityFPS=40 (engage 36 fps < ~54 fps worst observed rate) so aggregation reliably engages on the CPU-bound fixture; asserts edge DATA tx>0 on BOTH sockets (DATA-only counter — airtight striping proof) + conc rx delta>=50KB on BOTH paths (floor above liveness-probe noise, closing a self-caught vacuity). No throughput-ratio assertion → robust to CPU-boundedness. Also fixed a fixture veth-reuse race (SetupWithPaths now idempotently pre-deletes the fixed-name edge veth) that the 4th sequential heavy topology exposed (`ip link add: File exists`).
    
    HARDWARE GREEN (full sequential run, llm-ubuntu-0): TestP2Aggregation PASS — solo-starlink/cellular PASS, bonded-aggregation SKIP (ratio, evidence), bonded-striping PASS (edge DATA tx starlink=6.5MB + cellular-backup=5.0MB → scheduler striped onto the 2nd socket; conc rx 203KB/158KB both>50KB → far-end reassembled DATA on both paths), data-thrift PASS (cellular tx=0 B, fraction 0.0000 < 0.01). P2 aggregation is now validated end-to-end: concurrent two-path carriage PROVEN in-fixture, 5G-idle PROVEN, proportionality by unit tests, throughput-ratio enforced-on-link-bound-venue + deferred to real independent-links hardware. D23 (pre-existing 150-170 misattribution) unchanged.
- criticism: ["[fable focused-review, resolved 6a3cb6c] the bonded-ratio skip removed the only e2e proof of concurrent two-path socket carriage (far-end cross-check was inside the skipped subtest) — added TestP2Aggregation/bonded-striping (fixture-scaled gate, edge-DATA-tx-both-sockets + conc-rx-both-paths>=50KB, no throughput ratio), hardware-green","[hardware, resolved 6a3cb6c] fixture veth-reuse race: the added 4th sequential topology hit `ip link add <fixed-veth>: File exists` when a prior subtest's async netns/veth reap lagged — SetupWithPaths now idempotently pre-deletes the fixed-name edge veth"]
- new_questions: []
- ledgerRefs: ["tasks:T23","goals:G1"]

## M7

### R17 — go-ahead

- createdAt: 2026-07-06T23:40:07.289Z
- updatedAt: 2026-07-06T23:40:07.289Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- summary: "T14 (RS FEC engine: internal/fec grouping + parity-emission deadline + recovery, klauspost/reedsolomon v1.14.1) reconciled go-ahead (opus+fable panel, 3 rounds). [Re-dispatched fresh after 2 earlier infra-deaths; module proxy confirmed reachable.] R1: opus approve (RS geometry, metadata-carrier invariant, deadline clock-purity, ≤K recovery, overhead ratio all verified non-vacuous; 1 out-of-scope defect: unbounded decoder group map), fable DISAPPROVE — reproduced a PANIC (observeParity accepted a parity payload shorter than the 4-byte length prefix -> encodeDataShard PutUint32 index-out-of-range; remotely triggerable once T24 wires input) + silent-truncate/fabricate on oversized data + unbounded group state. Rework 623e031: reject short parity + oversized data (errors not panic), markDone buffer release + Forget + wraparound-safe SetRetainWindow eviction. R2: opus approve (filed adjacent DataCount O(m)-loop DoS: DataCount~2^31 -> multi-billion-iteration scan/alloc), fable DISAPPROVE — the oversized-data test was VACUOUS (passed pre-fix via an incidental decodeDataShard error) + unvalidated shard Index upper bounds left single-group memory unbounded (~6.4MB probed) and let one out-of-range index PERMANENTLY POISON a recoverable group. Rework dd4118f: discriminate the oversized-data test on the specific maybeReconstruct guard error; reject data Index>=maxShards-K at Offer + parity Index outside [0,K); drop within-bound >=m shards instead of wedging; reject DataCount>maxShards-K before the O(m) scan (44.8s->instant). R3 BOTH approve: all fixes verified fail-before/pass-after by mutation (guard-removed decoder + grafting r3 tests onto 623e031); >=m drop loses no recoverable data (RS only addresses 0..m-1; m is the pinned wire authority); DataCount bound matches Config.validate; full 19-test fec suite + -race + golangci-lint 0 green. Pure library (no datapath wiring, that is T24). Merged 51af100 (rebased onto main; go.mod/go.sum/flake.nix reconciled to the union of reedsolomon+prometheus with a regenerated vendorHash sha256-Y48M+39z...)."
- criticism: ["[r1 fable, resolved 623e031] reproduced panic: short parity payload -> PutUint32 out-of-range; + silent-truncate/fabricate on oversized data; + unbounded group state","[r2 opus, resolved dd4118f] DataCount O(m)-loop DoS — no upper bound before the missing-scan/alloc; reject DataCount>maxShards-K early","[r2 fable, resolved dd4118f] vacuous oversized-data test (passed pre-fix incidentally) + unvalidated shard Index (unbounded per-group memory + one-index group poisoning) — discriminating test + Offer/observeParity index bounds + >=m drop-not-wedge"]
- new_questions: []
- ledgerRefs: ["tasks:T14","goals:G1"]

### R32 — go-ahead

- createdAt: 2026-07-07T22:12:06.078Z
- updatedAt: 2026-07-07T22:12:06.078Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- summary: |
    T24 (integrate fixed-ratio Reed-Solomon FEC into the datapath + FEC metrics) reconciled GO-AHEAD (opus by-construction + fable recovery-correctness panel; 1 rework round). Merged f61a008. FEC off by default: SEND admits each inner datagram to a per-Open encoder + emits KindParity on group close (size or a TryLock/readersWG deadline tick); RECEIVE offers DATA+PARITY to a per-Open decoder BEFORE the T18 resequencer, delivering reconstructed frames at their original outer-seq. DATA carries a fec-index byte, PARITY a data-count byte; coded shard = OuterSeq||inner (seq reconstructed both ends, self-describing recovery even if all data frames of a group are lost). FEC counters flow Bind->Source->/metrics. New [fec] config, fail-fast.
    
    ROUND 1 DISAPPROVE. opus found an MTU overflow; fable REPRODUCED two datapath faults + found two more (4 total):
    (1) [reproduced] decoder GroupID high-water POISONING: DATA/PARITY unauthenticated, junk decodes as KindData ~1/256; one junk frame with a random-high uint32 group evicted all live groups + refused subsequent as tooOld -> recovery dead; same on a sender Close->Open (encoder group resets to 0, decoder keeps high-water).
    (2) [reproduced] late-recovered frames DUMPED the resequencer buffer: recovered seqs >window below next corroborated a BACKWARD resync -> FEC recovery CAUSED burst loss (repro: 39 frames dumped).
    (3) PARITY frames exceed path MTU by 5 bytes at full inner MTU (InnerMTU budgeted only DATA) -> FEC inert on bulk full-size traffic.
    (4) fec.deadline unbounded + /metrics counted reconstructed-not-delivered -> recovery structurally late (dropped as late) while counter overstated.
    
    FIXED (a97836d), each mutation-verified + fable ROUND 2 APPROVE (0 findings, 3 scratch-copy mutations confirm the fix-witnesses fail pre-fix, -race green): (1) corroborate-before-trust GroupID discontinuity guard mirroring the resequencer suspect/corroborate (single junk never moves the frontier; 3 distinct mutually-close ids required; uniform-random junk essentially never corroborates; genuine forward-jump/reopen resyncs after <=2 groups; residual ~3.5e-7 admitted-jump window SELF-HEALS via backward corroboration -> no persistent poisoning, no frontier stall). (2) reseq.ObserveRecovered non-resyncing path (placement strictly [next,next+window), never touches next/resync run, ring-invariant preserved, dedup both directions). (3) FEC-aware inner-MTU budget FECParityMTUPenalty=5 (parity wire = exactly 1500; FEC-off unchanged; all callers updated v4+v6). (4) fec.deadline bounded to resequencerTimeout/2 at BOTH config-load and bind (NewMultipath); recovered counter counts DELIVERED-ahead-of-release-point, no double-count. Full -race suite + lint + e2e/realhosts compile green.
    
    Acceptance met: <=M loss reconstructs the full ordered payload + advances recovery; 0-loss transparent (overhead = ratio); >M unrecoverable (counted) without stalling. FEC operates on the UNAUTHENTICATED DATA/PARITY frames by design; the discontinuity guard is the robustness boundary against junk/reopen. NOTE: parity rides the same scheduler.Pick as its data (cross-path parity placement = future refinement, documented).
- criticism: ["[r1 fable, resolved a97836d, REPRODUCED] decoder GroupID high-water poisoning: one unauthenticated junk frame (~1/256) or a sender Close->Open disabled FEC recovery for the Open span -> corroborate-before-trust discontinuity guard (single frame can't move the frontier; self-heals; no stall)","[r1 fable, resolved a97836d, REPRODUCED] late-recovered frames (parity delayed >window under real skew) corroborated a backward resync that dumped the live resequencer buffer -> FEC caused the loss it should prevent -> reseq.ObserveRecovered non-resyncing path, recovered frames below release point never reach corroboration","[r1 opus+fable, resolved a97836d] PARITY frames exceed path MTU by 5 bytes at full inner MTU (InnerMTU budgeted only DATA) -> FEC inert on bulk full-size traffic -> FEC-aware inner-MTU budget (FECParityMTUPenalty=5)","[r1 fable, resolved a97836d] fec.deadline unbounded (>resequencerTimeout made recovery structurally late) + /metrics counted reconstructed not delivered -> deadline bounded to resequencerTimeout/2 (config+bind) + delivered-only recovered counter"]
- new_questions: []
- ledgerRefs: ["tasks:T24","tasks:T14","tasks:T18","goals:G1"]

### R33 — go-ahead

- createdAt: 2026-07-07T23:28:34.739Z
- updatedAt: 2026-07-07T23:28:34.739Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- summary: |
    T25 (P3 e2e: FEC recovery at injected loss + bounded overhead) reconciled GO-AHEAD (opus counter-wiring + fable measurement-validity panel; 1 rework round + a hardware-found scrape fix). Merged 0128cc4. TestP3FixedFEC asserts, at 5% + 15% uniform netem loss: recovered-fraction (recovered/(recovered+unrecoverable) from CONCENTRATOR /metrics) >= 0.95 + frame overhead (repair/data from EDGE /metrics) <= 2*(M/K), + data-plane survival. K=10/M=6 (binomial-proven ~0.983 @15%; corrected the spec's K=10/M=4 = 0.882). Added production counter wanbond_fec_data_packets_total (overhead denominator; opus approved: atomic off m.mu, mirrors T24 parity counter, FEC-off=0).
    
    fable R1 DISAPPROVE (2 criticisms + D24): the recovered-fraction DENOMINATOR was invalidated by decoder eviction lag — unrecoverable is accounted only when a group falls >fecRetainGroups=512 behind the high-water, and the after-scrape had no trailing traffic, so the loss window's last 512 groups' failures were invisible → structurally recovered/recovered=1.0 at the sample floor, ~64% blind at realistic rates (a 70%-masking FEC would pass). FIXED (4765456): a trailing LOSSLESS drain (>6400 data frames → >=640 group-advances > 512 eviction threshold) forces every loss-window group's failure into unrecoverable BEFORE the after-scrape; accounting floor tightened 0.5→0.8.
    
    HARDWARE root-cause (the critical step): the first hardware run showed conc recovered=0 unrecoverable=0 despite loss+parity. Investigation (instrumented on llm-ubuntu-0) REFUTED both the late-recovery and parity-not-decoding hypotheses: the concentrator decoder reconstructs AND delivers IN TIME (15%: 3685 delivered/164 unrecoverable/only 67 late = fraction 0.957; 5%: 16313/127) — the FEC PRODUCT IS CORRECT. The bug was in the TEST scrape: fetchMetricsInNetns read the EDGE not the concentrator — net/http dials on a BACKGROUND goroutine that escaped the setns'd calling thread back to the root netns, hitting the edge's identically-bound 127.0.0.1 /metrics. FIXED (07be52b): the netns switch moved into the custom DialContext where socket() runs (LockOSThread-pinned, thread discarded on exit). fable FINAL APPROVE: denominator complete + real teeth (0.957 vs 0.95 bar), DialContext fix correct (loopback literal → one socket, fresh client, edge scrape untouched), overhead+survival sound.
    
    HARDWARE GREEN: TestP3FixedFEC PASS both (5%: fraction 0.9923/overhead 0.6003; 15%: 0.9792/0.6066). 
    
    P2/T23 IMPLICATION: the same scrape fix corrected T23's P2 concentrator cross-check, which had been reading the EDGE's rx (its conc-rx 50KB floor was a probe-noise floor not edge-tuned, so it passed; T23's airtight edge-tx-both-sockets striping proof was ALWAYS valid). P2 now genuinely reads the concentrator (bonded-striping conc rx 6.06/4.86 MB) and still PASSES — T23's concurrent-two-path-carriage conclusion holds and is now doubly-confirmed. D24 (FEC unrecoverable under-reports at quiescence, pre-existing T24) filed root-caused/deferred.
- criticism: ["[r1 fable, resolved 4765456] recovered-fraction denominator invalidated by decoder eviction lag (unrecoverable only counted at 512-group eviction; no post-load high-water advance) → structurally 1.0 at the sample floor, can't fail near 0.95 — fixed by a trailing lossless drain (>512 group-advances) that forces eviction+accounting before the after-scrape + floor 0.5→0.8","[hardware root-cause, resolved 07be52b] the concentrator scrape read the EDGE daemon (net/http background-goroutine dial escaped the setns'd calling thread to the root netns, both daemons bind identical 127.0.0.1 /metrics) → conc recovered read as 0 — fixed by moving the netns switch into the DialContext where socket() runs; ALSO strengthened T23/P2's concentrator cross-check (was reading the edge)"]
- new_questions: []
- ledgerRefs: ["tasks:T25","tasks:T24","tasks:T23","goals:G1","defects:D24"]

## M9

### R19 — go-ahead

- createdAt: 2026-07-06T23:45:11.596Z
- updatedAt: 2026-07-06T23:45:11.596Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- summary: "T19 (expose amnezia obfuscation params end-to-end + fold in D1/D2) reconciled go-ahead (opus+fable panel, 2 rounds). R1: opus approve, fable DISAPPROVE (2 criticisms) — fable RAN the e2e on real hardware (llm-ubuntu-0: matched profile handshake + ping + iperf3 147.5 Mbit/s + junk soak stable; mismatched fails closed) confirming acceptance + D1 correct, but found the D2 amneziaGuard UNSOUND: verified against amneziawg-go v1.0.4 that (*Device).Close calls resetProtocol() UNCONDITIONALLY (device.go:440, resets the package-global message types to WireGuard defaults), so (1) the guard's same-profile refcount admission let closing the first engine revert globals under a live second same-profile engine, and (2) plain-WireGuard engines bypassed the guard entirely yet their Close also reset globals under a live amnezia tunnel. Fixed 17a909c: guard rewritten to PROCESS-EXCLUSIVITY tracking all live engines (plainLive int + configLive bool) — a configured engine admitted only when no other engine is live (same-profile refcount dropped); no engine may start while a configured engine is live; plain engines coexist freely (reset-to-default idempotent). Also folded opus's low out-of-scope defect: Amnezia.validate now rejects the 148+s1 == 92+s2 junk-size collision at config LOAD using the engine's exported MessageInitiationSize/MessageResponseSize constants (mirrors IpcSet's own reject). R2 BOTH approve: exclusivity invariant complete (every interleaving placing a second engine beside a live configured one refused — same-profile, plain->config, config->plain — all test-pinned); release exactly-once via sync.Once, deferred release registered only after acquire succeeds (no spurious slot-clear), plainLive decrement symmetric, no leak/double-release/deadlock; Close runs dev.Close (resetProtocol) BEFORE freeing the slot so a successor's IpcSet strictly follows; s1/s2 uses the fork's real 148/92 constants. Gates green (build/vet/gofmt/test -race/golangci-lint 0/e2e-compile). Merged ca5d638 (rebased onto main; device.go conflict with T15's buildScheduler resolved preserving both guard-acquire-before-IpcSet + scheduler wiring). Resolves D1 + D2."
- criticism: ["[r1 fable, resolved 17a909c] D2 guard unsound — same-profile refcount admission let Close->resetProtocol revert globals under a live second engine; tightened to process-exclusivity (same-profile second configured engine now refused)","[r1 fable, resolved 17a909c] plain-WireGuard engines bypassed the guard but their Close also resets globals under a live amnezia tunnel — guard now tracks all engines; both plain/configured orderings refused, plain+plain allowed"]
- new_questions: []
- ledgerRefs: ["tasks:T19","goals:G1","defects:D1","defects:D2"]

### R36 — go-ahead

- createdAt: 2026-07-08T01:38:29.665Z
- updatedAt: 2026-07-08T01:38:29.665Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- summary: |
    T26 (automated wire-format audit: entropy + fixed-offset check, requirement 6) reconciled GO-AHEAD (opus by-construction + fable adversarial-teeth panel; 2 rework rounds + hardware). Merged 7f052e2. New pure package internal/wireaudit: classic-libpcap+Eth/IPv4/UDP parser (no gopacket, IPv4-total-length padding strip, IPv4-fragment guard) + the audit; e2e wrapper captures >=5 fresh amnezia+FEC sessions via tcpdump.
    
    opus (by-construction) APPROVE: parser bounds-safe (truncated record errors, no panic), returns ONLY udp[8:] (wanbond payload, not the constant IP/UDP header fields), Shannon entropy correct, single-valued detector + determinism correct.
    
    fable (adversarial teeth) R1 DISAPPROVE (4 criticisms): DECISIVE — the constant-offset detector flagged only SINGLE-valued offsets + entropy skipped <1024B frames, so a plaintext-header regression (kind byte {1..4} at offset 24 across the DATA/PARITY/PROBE/junk mix + structured seq) passed BOTH checks (multi-valued → not flagged; large frame still ~7.8 entropy) — a green audit on a DPI-signaturable wire (false assurance). Plus: mean-only entropy diluted a leaking subset; under-sampled offsets passed silently; udpPayload ignored IPv4 fragments. FIXED (2337abc): (1) OffsetDistributionOK per-offset value-entropy check (>=6.5 bits/byte, 512-sample floor, ~1.1-bit margin vs MLE bias) — the {1..4} escape now measures 1.32 bits → caught with 5.18-bit margin; (2) per-frame floor (6.0) + p5 quantile (7.0); (3) coverage reporting (contiguous judged prefix + CoverageOK); (4) fragment guard. All 4 mutation-verified. R2 DISAPPROVE (1 hygiene): orphaned dead code plantAndAssertDetected (unused under -tags e2e, invisible to `just lint` — D28) → deleted (fb413eb). fable r2 verified all 4 blind-spot fixes SOUND (escape caught, judged region provably prefix-contiguous so CoverageOK(1024) closes the reopening, floors false-fail-free per frame.go, fragment masks RFC-791-correct).
    
    CAPTURE FIX (hardware): first run gave 0-byte pcaps — tcpdump 4.99.4 SEGFAULTS with -Z root (exit 139, core dump); dropped -Z root (default privilege drop works; savefile world-readable) → 1fc4f09.
    
    HARDWARE GREEN (llm-ubuntu-0): TestWireFormatAudit PASS — 5 sessions / 385201 frames, FULL coverage (1472 offsets judged, 0 under-sampled), NO single-valued offset, ALL 1472 offsets clear 6.5 bits/byte value entropy (NO false-fail on the real keystream-uniform wire), mean entropy 7.87 (min 7.81 / p5 7.85); both planted signatures caught on the real wire (constant offset 10=0x5a; low-cardinality 4-value 2.0 bits vs 6.5). Requirement-6 empirically validated with a teeth-verified audit. D28 (just lint omits -tags e2e, low) filed; D27 (pre-existing flaky TestCodecPSKMismatch, medium) fixed+resolved out-of-band (de-flaked the shared gate).
- criticism: ["[r1 fable, resolved 2337abc] DECISIVE: constant-offset detector caught only SINGLE-valued offsets + entropy skipped <1024B → a low-cardinality plaintext-header signature (kind byte {1..4} at offset 24) passed BOTH checks (false assurance on a DPI-signaturable wire) — added a per-offset value-distribution entropy check (>=6.5 bits, 512-sample floor); the escape now measures 1.32 bits, caught with 5.18-bit margin","[r1 fable, resolved 2337abc] mean-only entropy diluted a leaking subset (up to ~8% plaintext large frames) + under-sampled offsets passed silently + udpPayload ignored IPv4 fragments — added per-frame/p5 entropy floors, coverage reporting (contiguous judged prefix + CoverageOK), and a fragment guard; all mutation-verified","[r2 fable, resolved fb413eb] orphaned dead code plantAndAssertDetected (unused under -tags e2e, invisible to `just lint` — filed D28) — deleted","[hardware, resolved 1fc4f09] tcpdump -Z root segfaults on 4.99.x → 0-byte pcaps — dropped -Z root (default privilege drop; world-readable savefile)"]
- new_questions: []
- ledgerRefs: ["tasks:T26","tasks:T24","goals:G1","defects:D27","defects:D28"]

### R37 — go-ahead

- createdAt: 2026-07-08T02:23:17.584Z
- updatedAt: 2026-07-08T02:23:17.584Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- summary: |
    T28 (nDPI/Suricata non-classification check + UDP-block limitation, requirement 6) reconciled GO-AHEAD (opus by-construction + fable non-vacuity panel; 1 rework + hardware). Merged 2b6ac3d. TestP5DPI captures the obfuscated wanbond flow (amnezia junk + FEC) and asserts neither ndpiReader nor suricata classifies it WireGuard/VPN by PAYLOAD. Non-vacuity: a committed genuine plain-WireGuard pcap asserted FIRST to be nDPI-classified WireGuard by DPI confidence (same payloadVPNFlow predicate the negative leg must be free of).
    
    opus (by-construction) APPROVE: every tool-failure path fails loud (ndpiReader/suricata non-zero/timeout/empty; tool-absent → Fatalf never Skip); positive control asserted first + hard on a byte-verified genuine WG fixture (init+resp+25 transport frames), non-bypassable.
    
    fable (non-vacuity) R1 DISAPPROVE (1 reproduced criticism): the negative nDPI leg captured on UDP 51820 (WireGuard's IANA port), and nDPI PORT-GUESSES WireGuard/category-VPN for ANY UDP flow on 51820 independent of payload (reproduced: random payload on 51820 → WireGuard [Match by port]; on 40000 → Unknown). The parser read only the confidence-LESS summary sections → the test would spuriously t.Fatalf a requirement-6 defect on the host EVEN WITH perfect obfuscation, conflating PORT-based classification with PAYLOAD DPI-resistance. FIXED (919014d): (a) parse ndpiReader -v 2 PER-FLOW Confidence — fail only on a payload/DPI-confidence WireGuard match, never a `Match by port` guess; (b) capture on a NON-registered port (40000) so the port-guess never fires and any WG/VPN label is a genuine payload leak. Positive control strengthened to assert a DPI-confidence match (symmetric with the negative). fable R2 APPROVE: extracted the FULL nDPI 5.0 confidence taxonomy from the production binary — `Match by port` is the ONLY excludable non-payload class that can fire in the fixture (Match-by-IP needs a public-IP DB, custom-rule/nBPF need unloaded rules; all DPI* classes are payload-derived); predicate FAIL-CLOSED on unknown confidences; anchored regex FPC-bracket-immune; port-40000 isolation empirically confirmed.
    
    HARDWARE GREEN (llm-ubuntu-0, nDPI+suricata provisioned): TestP5DPI PASS — positive control nDPI classified plain WG as WireGuard by DPI (payload) confidence; NEGATIVE the obfuscated wanbond flow on port 40000 = proto Unknown / no payload WireGuard-VPN classification (payload DPI-resistance PROVEN); suricata 1 flow decoded (>=1 vacuity guard fired), app_proto=failed, 0 alerts. Requirement-6 empirically validated with real DPI engines, non-vacuous. Docs: UDP-block limitation (no TCP/TLS fallback, non-goal) + the 51820 port-guess deployment note in install.md; P5 checklist appended.
- criticism: ["[r1 fable, resolved 919014d, REPRODUCED] the negative nDPI leg captured on UDP 51820 where nDPI port-guesses WireGuard/VPN independent of payload (parser ignored confidence) → would spuriously fail requirement-6 on the host even with perfect obfuscation — fixed by parsing per-flow Confidence (fail only on payload/DPI match, not a port guess) + capturing on a non-registered port (40000) to isolate payload DPI-resistance"]
- new_questions: []
- ledgerRefs: ["tasks:T28","tasks:T26","goals:G1"]

## M8

### R34 — go-ahead

- createdAt: 2026-07-07T23:53:53.177Z
- updatedAt: 2026-07-07T23:53:53.177Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- summary: |
    T27 (adaptive FEC controller with hysteresis, simulation-tested) reconciled GO-AHEAD (opus by-construction + fable stability panel; 1 fix). Merged e9e578b. New PURE package internal/adaptivefec, isolated from the datapath (integration deferred). Control law: EWMA-smoothed loss (alpha 0.3); redundancy map M=ceil(K·e/(1-e)), e=SafetyFactor(1.5)·loss, K=10, MaxParity=10 (guarded: e>=1 short-circuits to MaxParity before the (1-e) division — no blow-up/negative-M at high loss); three-region hysteresis (raise >=5% monotone-up via max(m,map), deadband 2-5% hold, lower <=2% collapse-to-0); slew |ΔM|<=2 per 500ms; 3s lower-dwell (raise fast, lower slow). Zero-at-zero via the lower band (the worker self-caught that ceil pins the EWMA tail at M=1). Virtual-clock simulation harness; 5 mechanism-mutations each break exactly one assertion.
    
    opus (by-construction) APPROVE: high-loss (1-e)<=0 risk guarded by the e>=1->MaxParity early return; tolerance invariant M/(K+M)>=SafetyFactor·loss holds; RS bound DataShards+MaxParity<=256 validated; three regions partition the input (LowerThreshold<RaiseThreshold strict, no gap/overlap); EWMA first-sample seeding correct; determinism + import-isolation confirmed (no fec/telemetry/config/bind/device import); 11/11 tests, vet + -race clean.
    
    fable (stability) verified EMPIRICALLY stable: 0 changes over 2000-sample dither traces straddling BOTH band edges (no chatter); 0 changes after convergence over 20k samples × 5 loss levels (6/10/15/30/50% — exact convergence, NO limit cycle, slew short-circuits when target==m); dwell-bounded sawtooth (0.43-0.85 changes/s) under period~2×Dwell oscillation (per-spec raise-fast/lower-slow, not per-RateInterval flap); correct saturation at MaxParity for loss up to +Inf via the e>=1 guard. Mutation testing kills all mechanism removals + off-tunings (alpha 2x, dwell 0.5x, safety drop); only 2 tiny parameter drifts survive (tolerance slack, not vacuity). fable R1 DISAPPROVE (1 criticism): Validate rejected NaN but not +Inf for SafetyFactor → SafetyFactor=+Inf reached implementation-defined int(math.Ceil(NaN)) in redundancyMap(0) (e=0*Inf=NaN falls through both guards; worst case M=MaxParity at zero loss on a platform converting NaN positive). FIXED (076d865): reject non-finite SafetyFactor (math.IsInf) + a safetyInf reject test case. Documented law consequence (not a fault): loss dropping 30%→6% steady (above raise band, never <=2%) leaves M pinned — exactly the task-stated 'raise band → only increase' rule.
    
    NOTE: the datapath integration (feed telemetry.Estimate.Loss, apply M to the FEC encoder, optionally feed the scheduler) is a separate deferred task; SmoothedLoss()/Overhead() are exposed read-only for that future wiring but intentionally not connected. The T21 weighted scheduler already loss-weights paths, so the controller owns only the parity ratio.
- criticism: ["[r1 fable, resolved 076d865] Config.Validate rejected NaN but not +Inf for SafetyFactor → SafetyFactor=+Inf reached an implementation-defined int(math.Ceil(NaN)) conversion in redundancyMap(0) — added math.IsInf rejection (finite >= 1) + a safetyInf reject-invalid test case, matching the file's fail-fast contract"]
- new_questions: []
- ledgerRefs: ["tasks:T27","goals:G1"]

### R35 — go-ahead

- createdAt: 2026-07-08T00:47:52.770Z
- updatedAt: 2026-07-08T00:47:52.770Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- summary: |
    T29 (wire adaptive FEC controller into datapath + P4 e2e vs fixed baseline) reconciled GO-AHEAD (opus by-construction + fable measurement panel; 1 rework + hardware). Merged c7d6256. Adaptive FEC opt-in ([fec].adaptive; fixed default = byte-for-byte T24): FEC tick loop (single m.mu locus, probe-cadence throttle) drives the T27 controller from MAX raw probe loss across eligible paths, retargets encoder per-group parity via SetParity (M fixed once group opens). Decoder UNCHANGED — klauspost RS parity prefix-consistent (RS(K,m)==first m of RS(K,ceiling)). New [fec].safety_factor knob + byte-overhead + residual-loss /metrics.
    
    opus (by-construction) APPROVE: the load-bearing PREFIX-CONSISTENCY claim PROVEN against reedsolomon@v1.14.1 source (default buildMatrix = Vandermonde×top-inverse; coding-row data+j depends on (data,j) NOT total parity → parity shard j byte-identical for RS(m,k) and RS(m,ceiling)); M=0 short-circuits (no div-by-zero, decoder buffers data-only groups); single-locus controller concurrency (TryLock tick, readersWG, no m.mu inversion); bounded codec cache; residual estimator observes native+recovered seqs with NO control-loop feedback (controller uses raw probe loss); fixed/non-FEC preserved.
    
    fable (measurement) R1 DISAPPROVE (3 criticisms + D26): (1) acceptance names an e2e run that was compile-only — must hardware-run; (2) the residual-loss instrument (the whole equal-masking leg) had ZERO non-vacuity coverage — a dead-low gauge or unapplied netem passes vacuously (the T25-class hole; no P3-style loss-took-effect teeth); (3) misleading gating comment. FIXED (5eee851, test-only): (2a) TestMultipathFECResidualLossNonVacuous proves ResidualLoss measures residual (0.25 unmasked / 0.0 masked), TWO-SIDED mutation-verified against both Observe sites; (2b) runP4Phase asserts loss-took-effect per phase (edge probe loss ~= rate AND conc recovered-delta >= 20) so an unapplied netem fails loud + disambiguates a send-side M-stall; (3) comment corrected to match the parent-gates-masking-then-compares-overhead structure.
    
    HARDWARE GREEN (llm-ubuntu-0, 5% loss): TestP4AdaptiveFEC PASS — adaptive residual 0.0000 / overheadBytes 0.4011 (M≈4, edgeLoss 0.063, recovered 6525) vs fixed residual 0.0043 / overheadBytes 0.6026 (M=6); equal masking established (both <= 0.005) THEN adaptive overhead 0.40 <= fixed 0.60 = 67% of baseline. Adaptive masks BETTER for LESS overhead — the P4 thesis proven. The band-edge risk (5% == RaiseThreshold) did not bite (probe read 6.3%). safety_factor=4.0 in the test is a legitimate SLA lever (default 1.5 gives ~1% residual — D26). D25 (prefix-consistency pinning + partial-group test, medium) + D26 (adaptive default tuning vs 0.5% SLA, low) filed root-caused/deferred.
- criticism: ["[r1 fable, resolved 5eee851] the residual-loss instrument (the P4 equal-masking signal) had ZERO non-vacuity coverage — a dead-low gauge or silently-unapplied netem passed P4 vacuously (no P3-style loss-took-effect teeth) — added a two-sided-mutation-verified bind-level residual test (0.25 unmasked/0.0 masked) + a per-phase loss-took-effect guard (edge probe loss ~= rate AND conc recovered-delta >= 20)","[r1 fable, resolved 5eee851] misleading TestP4AdaptiveFEC gating comment (claimed phases gate masking; actually the parent asserts both residuals then compares overhead) — corrected to match behavior","[r1 fable, resolved via hardware run] acceptance names a passing e2e run but the gate was compile-only — hardware-ran on llm-ubuntu-0: PASS (adaptive 0.40 overhead / 0.0000 residual vs fixed 0.60 / 0.0043)"]
- new_questions: []
- ledgerRefs: ["tasks:T29","tasks:T27","tasks:T24","goals:G1","defects:D25","defects:D26"]

## M12

### R51 — go-ahead

- createdAt: 2026-07-13T13:48:19.737Z
- updatedAt: 2026-07-13T13:48:19.737Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- summary: "G2 plan-review ROUND 1: GO-AHEAD. Plan (M13-M17, 16 tasks) is fine-grained, correctly sequenced, testable, grounded, complete, and CONSISTENT with Q17-Q20. Coverage: CORE-1 pacing = T52(measure BDP)/T53(wire SizePacingFromBDP from operator-DECLARED bandwidth, NOT auto-tuning, pacing stays default-DISABLED)/T56(document)/T61(ENABLED pacing: relative bufferbloat reduction + WG-rekey/probe survives saturation, no absolute gate). CORE-2 real-link = T58(aggregation ratio + loaded-RTT, report-only)/T63(mid-transfer WAN-kill link AND hub failover)/T64(short soak), all report-only per M10/Q12, liveness-only, NO absolute-number gate. CORE-3 runbook = T59(rollout runbook)/T65(automate P0 baseline)/T66(non-blocking exit + doc-sync sweep) - distinct deliverables, not double-covered. CORE-4 startup = T51(tolerant bind: EADDRNOTAVAIL via errors.Is NOT string-match; EADDRINUSE/perm stay fatal; zero-bindable fatal; malformed stays config-load error)/T55(background reconcile, -race no-goroutine-leak, Close-clean)/T60(netns e2e: absent-then-added survivor+deferred-join, zero-bindable non-zero exit, malformed config-error, T16 no-regression). Q17 CONTROL DORMANT honored - NO control milestone/task. Q18 hub-failover = edge-side ordered-endpoint active-standby EXACTLY (T54 ordered list / T57 all-paths-to-active-hub-DOWN detection + switch remote + WG re-handshake, NO hub-to-hub state, single-endpoint no-op / T62 netns). Q19 exit NON-BLOCKING (T66). Q20 pacing BOTH+declared (T53+T56). Sequencing correct: M15->M13, M16->M14+M15, M17->M13-16; task edges sound (T55->T51, T60->T51/T55, T57->T54/T51 cross-ms, T61->T52/T53, T63->T58/T57, T66->all leaves). Doc-sync notes present on every behavior/config-changing task (T51,T55,T53,T54,T57). ADVERSARIAL CHECKS PASSED: (a) no split-brain - single-edge active-standby with fresh WG session at standby, no hub state handoff; (b) flapping bounded - liveness hysteresis (DownAfter~1200ms, UpAfterSuccesses=3), no failback specified (flap-safe), and T62 acceptance ('traffic resumes via hub#2') backstops any failure to reset per-path liveness against the new endpoint; (c) no runtime pacing auto-tuning (T53 declared-only); (d) no absolute-number gate on report-only realhosts tier. Minor non-blocking observations left to implementer latitude (each backstopped by an acceptance test): T53 RTT-input source for SizePacingFromBDP under-specified (operator-declared per Q20); T57 all-paths-to-active-hub-DOWN cannot distinguish hub loss from total edge-uplink loss, but that yields only bounded harmless endpoint churn (wrap/stop per config) and is validated positively by T62. OUT-OF-SCOPE PRE-EXISTING DEFECT (file-and-defer, medium): cq.toml has a malformed reviewer/alias token 'frontier' (missing ':') - get_reviewers errors ('token \"frontier\" is not \"<harness>:<model>\"'), forcing UNCONFIGURED single-reviewer fallback, and the tasks' suggestedModel aliases (frontier/standard/fast) may not resolve at implement dispatch until cq.toml maps 'frontier' to a full '<harness>:<model>' token. Independent of the G2 plan content. Verdict: go-ahead."
- new_questions: []
- criticism: []
- ledgerRefs: ["goals:G2"]

## M13

### R55 — go-ahead

- createdAt: 2026-07-13T14:26:02.129Z
- updatedAt: 2026-07-13T14:26:02.129Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- summary: "T51 (G2/W1 tolerant startup bind) implement-review ROUND 2: APPROVE (verdict=approve). Round-1 DISAPPROVE had 2 reproduced blockers from the half-integrated m.deferred set (base Open-tolerance + Send/Pick middle-defer index-alignment + errno discrimination + hard guards were already correct); both now RESOLVED at b0e35f9. C1 (reload regression): PathNames() now returns the durable bound+deferred membership (m.defs) so diffPaths no longer sees a deferred path as an ADD, and AddPath defers EADDRNOTAVAIL SYMMETRICALLY with Open — no-op reload of a still-deferred path succeeds; TestPathNamesIncludesDeferred + TestAddPathDefersUnassignable fail pre-fix, pass post. C2 (RemovePath corruption): removeDurableLocked splices m.defs/m.probers by IDENTITY (name) + drops from m.deferred, live m.paths spliced by liveIdx; [first,mid(deferred),third] RemovePath('third') preserves mid + doesn't resurrect third across Close->Open; TestRemovePathAfterDeferredPreservesMembership fails pre-fix, passes post. NO REGRESSION: Open tolerance non-vacuous, EADDRINUSE/other fatal, zero-bindable fatal, prober-stamp path-id (DATA==PROBE), nextPathID high-water past deferred stamps, scheduler bound-only Pick alignment, T55 deferred-reconcile substrate preserved. Full gate + go test -race ./internal/bind/... green under nix develop. 0 criticisms, 0 questions. Merged to main at ba9eb65."
- ledgerRefs: ["tasks:T51"]

### R58 — go-ahead

- createdAt: 2026-07-13T14:45:22.666Z
- updatedAt: 2026-07-13T14:45:22.666Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- summary: "T55 (G2/W1 deferred-path background reconcile) implement-review: APPROVE (verdict=approve). All acceptance clauses verified operationally. TestReconcilePromotesDeferredPathToLive drives EADDRNOTAVAIL->armed via injected deferredListen seam, promotes into m.paths+scheduler+reader, proves end-to-end failover (blackhole primary->Pick=1->Send) — NON-VACUOUS; stamp continuity holds (promoted.prober==probers[1], id==probers[1].PathID()), reserved nextPathID raised past deferred stamps. CONCURRENCY: reconcileDeferred/promoteDeferredLocked run entirely under m.mu (serialize w/ Send/Close/AddPath/RemovePath); loop goroutine has idempotent done-channel stopper wired into Tunnel.Close before dev.Close; promoted readers tracked by readersWG so Close joins them; TestReconcileLoopStopsCleanly starts the real 1ms ticker + asserts goleak.VerifyNone (non-vacuous). TestReconcileSkipsPathRemovedBeforeBind confirms RemovePath retires a deferred entry (no resurrection). Index-skew rollback on promote mirrors AddPath. Poll chosen over netlink (vishvananda/netlink not a dep). Full gate + go test -race ./internal/bind/... + ./internal/device/... green. doc-sync present. 0 criticisms, 0 questions. Merged to main at a955083. Reviewer filed 1 low-sev OUT-OF-SCOPE defect (promoted/runtime paths forgo SO_BINDTODEVICE, pre-existing w/ AddPath) — recorded separately."
- ledgerRefs: ["tasks:T55"]

### R59 — go-ahead

- createdAt: 2026-07-13T15:04:58.140Z
- updatedAt: 2026-07-13T15:04:58.140Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- summary: "T60 (G2/W1 startup-resilience netns e2e) implement-review: APPROVE (verdict=approve). Validates T51 tolerant-Open + T55 reconcile END-TO-END. Absent-then-added flow SOUND: deferEdgeAddr withholds the address on the correct edge namespace (same call path as Readdress), AddEdgeAddr adds it later; promotion waited via waitPathUp = bounded 100ms metrics-poll (NOT a fixed-sleep race) with an analytically-derived deadline matching the REAL bind.DefaultReconcileInterval + telemetry.DefaultUpSuccesses*DefaultProbeInterval constants; traffic-on-promoted-path proven by blackholing the survivor -> failover recovers only via the reconciled path (active-backup default). Zero-bindable fatal: exit!=0 + 'wanbond starting' present (proves Open() not config-load) + exact 'no configured path could bind' string (both confirmed in multipath.go/main.go). Malformed source_addr: rejected BEFORE 'wanbond starting' logs (matches main.go config.Load-before-log ordering) + 'invalid source_addr' string. Deterministic (bounded ctx/deadline, reuses pingUntil/iperf3Mbps/Blackhole/metrics.Fetch). SURGICAL: 2 files, netns.go additive (deferEdgeAddr zero-value-safe + AddEdgeAddr, no behavior change) + new tolerant_startup_test.go; NO production code touched; T16 roaming_test.go untouched. Non-privileged gate + e2e-tagged compile green; gofmt clean. Verified by compile + close code-reading (privileged netns run not executable in reviewer sandbox). 0 criticisms, 0 questions. Merged to main at 96504d4. HARDWARE VALIDATION (the privileged run) pending on llm-ubuntu-0."
- ledgerRefs: ["tasks:T60"]

## M15

### R60 — go-ahead

- createdAt: 2026-07-13T15:20:03.902Z
- updatedAt: 2026-07-13T15:20:03.902Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- summary: "T54 (edge-side ordered concentrator-endpoint config surface) implement-review: APPROVE (verdict=approve, 0 criticisms, 0 questions). Ordered `endpoints` list parses in TOML order into Peer.Endpoints []netip.AddrPort (order preserved index 0..2, verified by TestLoadEndpointsOrderedList). Legacy single `endpoint` normalizes to a one-element Endpoints list via resolveEndpoints() — behavior-identical, confirmed by TestLoadEndpointSingleBackwardCompat and device.go uapiConfig now reading Endpoints[0] uniformly for both forms. Fail-fast validation covers: endpoint/endpoints mutual exclusivity, unparseable host:port, duplicate entries, empty list on edge (falls through to existing 'endpoint is required'), and edge-only constraint rejecting endpoints on the concentrator role — each with a dedicated rejection-table case in config_test.go. Endpoints is IP:port-only (netip.ParseAddrPort, NO hostname resolution) — a documented constraint T57 must honor. Endpoints field exposed public for T57 (hub-loss switch). Docs (README/design/install) updated consistently with a correctly #-prefixed multi-line TOML example. Full non-privileged gate green: go build/vet/gofmt/test all clean. SURGICAL: config.go + device.go call site + tests + docs only; switch/re-handshake deferred to T57 as specified. Merged to main at e066524."
- ledgerRefs: ["tasks:T54"]

### R61 — go-ahead

- createdAt: 2026-07-13T15:45:24.829Z
- updatedAt: 2026-07-13T15:45:24.829Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- summary: "T57 (edge-side hub-loss detection + peer-remote switch + WG re-handshake) implement-review: APPROVE after round-1 DISAPPROVE resolved. Adversarial review (opus) verified ALL six core concerns CORRECT: (1) Re-handshake API sound — dev.LookupPeer/peer.ExpireCurrentKeypairs/SendHandshakeInitiation all exist+exported in amneziawg-go@v1.0.4; ExpireCurrentKeypairs invalidates keypairs, clears handshake state, deletes index-table entry, AND backdates lastSentHandshake by RekeyTimeout+1s so the following SendHandshakeInitiation(false) clears the RekeyTimeout guard and emits a fresh initiation (no wedge); LookupPeer==nil handled. (2) Invariant A1 preserved — SetPeerRemote (multipath.go:1329) takes m.mu, updates defaultRemote, calls ps.setRemote on every path under ps.mu; never touches the engine virtual endpoint; lock order m.mu->ps.mu matches seed paths (no inversion); -race clean. (3) No boot false-positive — lastSwitch seeded to construction time + 3s settle dwell gates first advance; reachable hub UP in ~600ms<<3s. (4) Goroutine lifecycle clean — done channel + sync.Once-guarded close wired into Close; single-endpoint/concentrator/no-prober -> no-op stopper, no goroutine; -race clean. (5) Single-endpoint GUARD non-vacuous (removing len<2 fires 5 switches -> test fails). (6) WRAP policy bounded to one switch/dwell, re-arms. Round-1 sole blocker (CRITICISM): no test exercised a MIXED liveness state (one path Up, one Down) for the load-bearing 'hub loss = ALL down, distinct from single-path failover' property -> an allDownLocked all-down->any-down regression would pass the whole suite. RESOLVED: added TestHubFailoverPartialDownNoFailover (settle dwell elapsed so an any-down detector WOULD fire), MUTATION-VERIFIED it FAILS under the any-down regression and passes with correct logic. Full gate + go test -race ./internal/bind/... ./internal/device/... + go vet -tags e2e all green on merged main. Merged at 40ba4d8 + 7d309bd. Real cross-network netns e2e deferred to T62."
- ledgerRefs: ["tasks:T57"]

### R62 — go-ahead

- createdAt: 2026-07-13T16:55:51.799Z
- updatedAt: 2026-07-13T16:55:51.799Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- summary: "D32 fix (reseq re-baseline on hub failover, commit c7f8421) review: APPROVE + HARDWARE-VALIDATED. Adversarial review (opus) verified: (1) anti-forgery guard NOT weakened from the wire — Rebaseline reachable ONLY via SetPeerRemote->hubFailover.check(), gated on allDownLocked (liveness plane) + >=2 endpoints + settle dwell, never a wire-frame path; an on-path attacker who forces all-paths-DOWN already holds the accepted DATA-plane DoS (invariant 4), and a forged post-switch anchor still fails WG inner Noise auth. (2) No lock inversion — SetPeerRemote takes m.mu, loads the atomic.Pointer, RELEASES m.mu, then calls Rebaseline (r.mu); reseq imports stdlib only so r.mu->m.mu is structurally impossible; nil-guard correct; -race clean. (3) Rebaseline correct — released FIFO preserved, no double-free, idempotent, no WG replay regression (standby is a fresh session). (4) Both new reseq tests mutation-sensitive (non-vacuous). Full gate + go test -race ./internal/reseq/./internal/bind/./internal/device green. HARDWARE (llm-ubuntu-0): TestHubFailoverStandbySwitch 13/13 PASS on successful setups (was 0/3), HUB_FAILOVER_RESUME_MS ~6.0-6.8s within the 10.2s window — traffic resumes via standby hub#2; single-endpoint guard 7/7 PASS. Reviewer filed 1 out-of-scope low-sev defect (D34: post-rebaseline straggler/FEC re-anchor race, self-healing) — did NOT trigger in 13 hardware runs. D32 RESOLVED."
- ledgerRefs: ["defects:D32"]

### R63 — go-ahead

- createdAt: 2026-07-13T17:09:02.831Z
- updatedAt: 2026-07-13T17:09:02.831Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- summary: "T62 (hub-failover netns e2e) FINAL: APPROVE + HARDWARE-VALIDATED. Static review (round 1) approved the test as non-vacuous (iperf3 client in edge netns, server only in hub#2 netns, hub#1 L2-blackholed -> a transfer proves the standby switch + fresh re-handshake; guard test load-bearing via the 'hub failover:' journal-absence assertion; port 9099 collision-free; bounded/analytical waits). The e2e then did its JOB on hardware: it caught TWO real defects the unit tests + code review missed -- D32 (hub-failover data-plane: resequencer dropped the standby's handshake-response, tunnel never re-established) and D33 (fixture netns setup race). BOTH FIXED + hardware-confirmed: D32 (reseq re-baseline, c7f8421) -> StandbySwitch 13/13 PASS, RESUME_MS ~6.0-6.9s within the 10.2s window, traffic resumes via hub#2; D33 (retry the in-netns addr-add, merged into the test) -> 26/26 setups clean (0 failures vs prior ~13%). Merged to main (1f1cd04 test + c4e10a7 D33 fix). Full gate + go vet -tags e2e green. This task delivered exactly the pilot value of the report-only hardware tier: an e2e that surfaced a data-plane bug unit tests could not. 1 residual low-sev hardening defect (D34) filed + deferred (did not trigger in 39 hardware runs)."
- ledgerRefs: ["tasks:T62"]

## M19

### R70 — revise

- createdAt: 2026-07-13T22:02:58.906Z
- updatedAt: 2026-07-13T22:03:38.920Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "Reconciled panel verdict (strictest-wins over 2 reviewers): [opus] go-ahead — plan is fine-grained, sequenced, testable, grounded, complete; [fable] revise — grounded and well-sequenced but two mechanism gaps need planner fixes (T74 first-resolve handshake path cannot work as written; T70 active-endpoint identity ambiguous under cross-spec AddrPort duplicates). Verdict: revise. No new questions; no out-of-scope defects from either reviewer."
- new_questions: []
- criticism: ["[fable] T74: the 'first successful resolve → SetPeerRemote + rehandshake kicks the first handshake' mechanism is insufficient after an endpoint-less tolerant boot. The engine peer's endpoint is populated exclusively by a UAPI endpoint= line routed through Multipath.ParseEndpoint (multipath.go:1324-1344) — SetPeerRemote (multipath.go:1371) only repoints bind path remotes and never hands the engine peer its (virtual) endpoint, so the rehandshake's SendHandshakeInitiation has no known endpoint to address and cannot transmit. Specify that the first resolve must also install the resolved endpoint on the engine peer via the UAPI/IpcSet path (or equivalent), and strengthen acceptance: the fake-rehandshake-counter check cannot detect this failure, and T77's e2e starts with a resolvable name, so the boot-unresolvable→first-resolve→handshake path is never proven — add it to T74's unit acceptance (assert the engine peer gains an endpoint / a handshake initiation actually egresses) or to T77's e2e scenario.","[fable] T70: tracking the ACTIVE endpoint 'by IDENTITY (its AddrPort value)' is ambiguous when two specs' expansions contain the same AddrPort — T67's duplicate detection only rejects textual host:port duplicates at load, so a hostname re-resolving to the same IP:port as a literal (or another hostname's record) elsewhere in the list yields duplicate values in the flattened failover order, and value-based idx re-mapping can silently match the wrong spec's entry. Specify the rule: dedup on flatten (documented precedence), or track the active entry as (specIdx, AddrPort), and add an acceptance case for a hostname resolving onto an existing literal standby."]
- ledgerRefs: ["goals:G5"]
- sessionLogs: [".cq/logs/20260713-220328-a45a9b222054d0d22.md",".cq/logs/20260713-220328-ab541f9aa587c0050.md"]
- rawLogs: [".cq/logs/raw/20260713-220328-a45a9b222054d0d22.jsonl",".cq/logs/raw/20260713-220328-ab541f9aa587c0050.jsonl"]

### R71 — go-ahead

- createdAt: 2026-07-13T22:10:28.589Z
- updatedAt: 2026-07-13T22:11:08.696Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "Reconciled panel verdict, round 2 (2 reviewers, unanimous): [opus] go-ahead — R70's two criticisms resolved with grounded, correct mechanisms and strengthened observable acceptance (T74 installs the engine endpoint via the UAPI/ParseEndpoint path before rehandshaking, asserting real egress; T70 tracks the active entry by spec-scoped (specIdx, AddrPort) identity with a cross-spec-duplicate acceptance case); [fable] go-ahead — both R70 criticisms confirmed resolved with code-verified mechanisms (device.go:706-726 + multipath.go:1327/1371), T77 now proves the boot-unresolvable path e2e; plan fine-grained, correctly sequenced, testable, grounded, complete against Q29-Q36. Verdict: go-ahead. No new questions, no criticism, no out-of-scope defects from either reviewer."
- new_questions: []
- criticism: []
- ledgerRefs: ["goals:G5"]
- sessionLogs: [".cq/logs/20260713-221057-abf3d4747c8c2d97a.md",".cq/logs/20260713-221057-ab6ff041b6c4fdf2b.md"]
- rawLogs: [".cq/logs/raw/20260713-221057-abf3d4747c8c2d97a.jsonl",".cq/logs/raw/20260713-221057-ab6ff041b6c4fdf2b.jsonl"]

## M18

### R72 — revise

- createdAt: 2026-07-13T22:33:53.545Z
- updatedAt: 2026-07-13T22:34:34.453Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "Reconciled panel verdict (strictest-wins over 2 reviewers): [opus] go-ahead — plan fine-grained, sequenced, testable, grounded, complete across Q21-Q28; [fable] revise — well-grounded but two planner-fixable gaps: M25's chain (T89/T90/T92) is missing a dependsOn edge to T86 (per-peer resequencer demux), and runtime path add/remove interacting with per-(peer,path) state has no task or acceptance. Verdict: revise. No new questions; no out-of-scope defects."
- new_questions: []
- criticism: ["[fable] Missing dependency edge: T89's acceptance ('DATA lands in B's resequencer only') — and transitively T90's and T92's cross-peer-resequencer assertions — require T86's per-peer resequencer demux, but T86 is not in T89's transitive dependsOn closure (T89→T88→{T84,T85}→T83; T86 is a sibling off T83). In DAG-parallel execution T89 can be picked before T86 merges, making its acceptance unimplementable. Add T86 to T89's dependsOn (or to T88's).","[fable] Runtime path add/remove is unaddressed in the multi-peer world: the repo has live dynamic-path machinery (internal/bind/runtime_path_test.go, tolerant_membership_test.go, bind.ProberFactory returned by buildScheduler at internal/device/device.go:577, the T30 deferred paths T83 itself names as shared socket state), and the ProberFactory today closes over the single cfg.PSK. No task or acceptance exercises adding/removing a path while >=2 peers are bound (each bound peer must gain/lose its per-(peer,path) prober/codec/remote). Add an acceptance clause to T83 or T93, or explicitly declare runtime path mutation out of scope for multi-peer."]
- ledgerRefs: ["goals:G4"]
- sessionLogs: [".cq/logs/20260713-223422-aac7874ad6597a588.md",".cq/logs/20260713-223422-a2693299e2689e645.md"]
- rawLogs: [".cq/logs/raw/20260713-223422-aac7874ad6597a588.jsonl",".cq/logs/raw/20260713-223422-a2693299e2689e645.jsonl"]

### R73 — go-ahead

- createdAt: 2026-07-13T22:38:38.547Z
- updatedAt: 2026-07-13T22:39:12.288Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "Reconciled panel verdict, round 2 (2 reviewers, unanimous): [opus] go-ahead — both R72 criticisms resolved and grounded (T89 now directly dependsOn T86 fixing the DAG-parallel unimplementable-acceptance hazard; T83 brings runtime path mutation in scope with a two-peer fan-out acceptance; T93 replaces the device.go:577/599 ProberFactory single-cfg.PSK closure with a per-peer factory); [fable] go-ahead — both criticisms verifiably resolved, all plan-cited repo facts match source, the 20-task DAG is fine-grained, correctly sequenced, operationally testable, grounded, and complete against Q21-Q28 and every named invariant (A1, D32, FEC prefix-stability, doc sync). Verdict: go-ahead. No new questions, no criticism, no out-of-scope defects."
- new_questions: []
- criticism: []
- ledgerRefs: ["goals:G4"]
- sessionLogs: [".cq/logs/20260713-223902-a91e747225a420952.md",".cq/logs/20260713-223902-ae3509c0ca48d6db5.md"]
- rawLogs: [".cq/logs/raw/20260713-223902-a91e747225a420952.jsonl",".cq/logs/raw/20260713-223902-ae3509c0ca48d6db5.jsonl"]

## M20

### R74 — go-ahead

- createdAt: 2026-07-13T22:54:59.681Z
- updatedAt: 2026-07-13T22:54:59.681Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T67 round 1 reconciled panel verdict (opus + fable, unanimous approve, check green): hostname endpoints behind per-peer dns=true opt-in implemented surgically in internal/config; all 6 acceptance cases test-covered and passing; all-IP-literal path byte-for-byte preserved; validate() retarget to EndpointSpecs verified safe; doc-sync legitimately deferred to T79 per plan."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T67","goals:G5"]
- sessionLogs: [".cq/logs/20260713-225437-a7e364965f1c96b59.md",".cq/logs/20260713-225437-a11bd428159e547c9.md"]
- rawLogs: [".cq/logs/raw/20260713-225437-a7e364965f1c96b59.jsonl",".cq/logs/raw/20260713-225437-a11bd428159e547c9.jsonl"]

### R76 — go-ahead

- createdAt: 2026-07-13T23:03:03.881Z
- updatedAt: 2026-07-13T23:03:03.881Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T68 terminal reconciled panel verdict after 2 rounds: round 1 [opus] approve / [fable] disapprove (strictest-wins revise) on ONE criticism — docs/design.md Supporting-packages inventory omitted internal/dnsresolve; round 2 (docs-only fix b596b6b) unanimous approve, gate green re-verified by both reviewers, all acceptance clauses evidenced operationally."
- criticism: ["[fable, round 1, RESOLVED in round 2] docs/design.md §'Supporting packages' was not updated to list the new internal/dnsresolve package while README.md's repo-layout list was — AGENTS.md's required docs-sync rule made this an objective, autonomously fixable omission."]
- new_questions: []
- ledgerRefs: ["tasks:T68","goals:G5"]
- sessionLogs: [".cq/logs/20260713-225437-ae9e21e85de4600a9.md",".cq/logs/20260713-225437-a99ae9caf87cc11a3.md",".cq/logs/20260713-230228-a0fb43fc933a1f307.md",".cq/logs/20260713-230228-a50626c69a5974410.md"]
- rawLogs: [".cq/logs/raw/20260713-225437-ae9e21e85de4600a9.jsonl",".cq/logs/raw/20260713-225437-a99ae9caf87cc11a3.jsonl",".cq/logs/raw/20260713-230228-a0fb43fc933a1f307.jsonl",".cq/logs/raw/20260713-230228-a50626c69a5974410.jsonl"]

### R78 — go-ahead

- createdAt: 2026-07-13T23:27:36.431Z
- updatedAt: 2026-07-13T23:27:36.431Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T69 terminal reconciled panel verdict after 2 rounds: round 1 [opus+fable] disapprove with 4 unioned criticisms (double-NODATA empty-addrs-with-nil-error diverging from SystemResolver; unbounded response-body read; stale doc.go; short-read-prone test helper); round 2 (e690011) unanimous approve — all 4 resolved with fail-first-verified tests (NoDataError, io.LimitReader 64KiB cap, doc.go, io.ReadAll), gate green re-verified by both reviewers."
- criticism: ["[opus+fable, round 1, RESOLVED] Lookup returned ([], nil) on double-NODATA, diverging from SystemResolver behind the same seam (unreachable guard at doh.go:127).","[fable, round 1, RESOLVED] unbounded io.ReadAll of the DoH response body → io.LimitReader cap with typed oversize rejection.","[fable, round 1, RESOLVED] stale doc.go still described DoH as a future transport.","[opus+fable, round 1, RESOLVED] readDoHQuestion single short-read-prone Read → io.ReadAll."]
- new_questions: []
- ledgerRefs: ["tasks:T69","goals:G5"]
- sessionLogs: [".cq/logs/20260713-231830-a5122d18a9a011585.md",".cq/logs/20260713-231830-a15aa232e07b17d44.md",".cq/logs/20260713-232716-a256ad0c7fae40b3a.md",".cq/logs/20260713-232716-a73363906e9351cad.md"]
- rawLogs: [".cq/logs/raw/20260713-231830-a5122d18a9a011585.jsonl",".cq/logs/raw/20260713-231830-a15aa232e07b17d44.jsonl",".cq/logs/raw/20260713-232716-a256ad0c7fae40b3a.jsonl",".cq/logs/raw/20260713-232716-a73363906e9351cad.jsonl"]

### R83 — go-ahead

- createdAt: 2026-07-13T23:41:16.798Z
- updatedAt: 2026-07-13T23:41:16.798Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T71 round 1 reconciled panel verdict (opus + fable, unanimous approve, check green incl. fresh -race runs): DoTResolver (RFC 7858) behind the seam with hermetic TLS-listener tests covering addrs+minTTL, wrong-server-name x509.HostnameError, timeout, truncated-frame; shared dnscodec extraction judged minimal and surgical; docs synced. Non-blocking notes: t.Fatal in a handler goroutine on an already-failing path; one-family-NXDOMAIN branch mirrors DoH's tested loop."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T71","goals:G5"]
- sessionLogs: [".cq/logs/20260713-234055-af856789b6ff0a960.md",".cq/logs/20260713-234055-a0b4991b5647ea04b.md"]
- rawLogs: [".cq/logs/raw/20260713-234055-af856789b6ff0a960.jsonl",".cq/logs/raw/20260713-234055-a0b4991b5647ea04b.jsonl"]

### R88 — go-ahead

- createdAt: 2026-07-14T00:32:34.350Z
- updatedAt: 2026-07-14T00:32:34.350Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T72 terminal reconciled panel verdict after 3 rounds: R1 disapprove ([fable] documented duration strings failed TOML decode; bootstrap_ip decorative) → R2 fixes accepted but [fable] found a NEW gap (unvalidated bootstrap_ip overriding an IP-literal host, contradicting the documented contract) → R3 (2417bb9) unanimous approve: fail-fast rejection of bootstrap_ip under an IP-literal host, non-vacuous negative tests both transports, docs fully consistent, gate green. D43 filed R1 (pre-existing scheduler/FEC string-duration docs desync)."
- criticism: ["[fable, R1, RESOLVED R2] documented \"30s\"/\"5s\" duration strings failed go-toml/v2 decode → PollIntervalRaw/TimeoutRaw + time.ParseDuration.","[fable, R1, RESOLVED R2] bootstrap_ip validated but not wired into the DoH/DoT dial target → NewDoTResolverWithBootstrap / NewDoHResolverWithBootstrap.","[fable, R2, RESOLVED R3] unvalidated bootstrap_ip could override an IP-literal host's dial target → fail-fast mode-mismatch rejection + matrix tests + docs alignment."]
- new_questions: []
- ledgerRefs: ["tasks:T72","goals:G5"]
- sessionLogs: [".cq/logs/20260714-001524-ab3c6a032f4c6441f.md",".cq/logs/20260714-001524-a3e48d89773e283be.md",".cq/logs/20260714-003019-a7c2512ac55807582.md",".cq/logs/20260714-003019-a7ba4d8dc5a6d3714.md",".cq/logs/20260714-003215-a51f0c5a70ae87666.md",".cq/logs/20260714-003215-a66e35c5e28239c28.md"]
- rawLogs: [".cq/logs/raw/20260714-001524-ab3c6a032f4c6441f.jsonl",".cq/logs/raw/20260714-001524-a3e48d89773e283be.jsonl",".cq/logs/raw/20260714-003019-a7c2512ac55807582.jsonl",".cq/logs/raw/20260714-003019-a7ba4d8dc5a6d3714.jsonl",".cq/logs/raw/20260714-003215-a51f0c5a70ae87666.jsonl",".cq/logs/raw/20260714-003215-a66e35c5e28239c28.jsonl"]

## M23

### R75 — go-ahead

- createdAt: 2026-07-13T22:55:05.322Z
- updatedAt: 2026-07-13T22:55:05.322Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T80 round 1 reconciled panel verdict (opus + fable, unanimous approve, check green): per-peer psk/name fields added surgically (12 lines + 138 test lines); 2-peer TOML exposure and legacy single-peer whole-Config DeepEqual golden both pass; gate independently re-run green by both reviewers. One out-of-scope pre-existing defect filed file-and-defer ([fable] non-strict TOML decode silently ignores unknown keys, load.go:34). Worker output json omitted the required `branch` field (contract breach logged; git state verified correct)."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T80","goals:G4"]
- sessionLogs: [".cq/logs/20260713-225437-a1850ae28a48e003a.md",".cq/logs/20260713-225437-ac6ebf9d1c27aa4bf.md"]
- rawLogs: [".cq/logs/raw/20260713-225437-a1850ae28a48e003a.jsonl",".cq/logs/raw/20260713-225437-ac6ebf9d1c27aa4bf.jsonl"]

### R77 — go-ahead

- createdAt: 2026-07-13T23:19:25.346Z
- updatedAt: 2026-07-13T23:19:25.346Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T81 round 1 reconciled panel verdict (opus + fable, unanimous approve, check green): per-peer psk presence/pairwise-distinctness + unique-name validation, edge-role >1-peer scope rejection, single-peer top-level back-compat — all 6 acceptance cases in a table-driven test; the single-peer per-peer-psk rejection judged a sound fail-fast, back-compat-preserving choice by both reviewers."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T81","goals:G4"]
- sessionLogs: [".cq/logs/20260713-231830-a8eef803232932fbf.md",".cq/logs/20260713-231830-a2082c0e624c73f95.md"]
- rawLogs: [".cq/logs/raw/20260713-231830-a8eef803232932fbf.jsonl",".cq/logs/raw/20260713-231830-a2082c0e624c73f95.jsonl"]

### R79 — go-ahead

- createdAt: 2026-07-13T23:27:42.196Z
- updatedAt: 2026-07-13T23:27:42.196Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T82 terminal reconciled panel verdict after 2 rounds: round 1 [opus] approve / [fable] disapprove (strictest-wins) on ONE criticism — the single-peer top-level-PSK-wins-over-set-per-peer-psk invariant was untested; round 2 (b69ed0d) unanimous approve — the invariant is pinned by a distinguishing, mutation-verified test (builds Config directly since T81's validate() rejects the shape at load), gate green."
- criticism: ["[fable, round 1, RESOLVED] the single-peer psk-shadowing case (top-level wins over a set per-peer psk) had no distinguishing test; an implementation preferring p.PSK would have passed the suite."]
- new_questions: []
- ledgerRefs: ["tasks:T82","goals:G4"]
- sessionLogs: [".cq/logs/20260713-231830-a91a4176a0efd8739.md",".cq/logs/20260713-231830-adb81d36d2064a300.md",".cq/logs/20260713-232716-acaab660e4b8c3626.md",".cq/logs/20260713-232716-a00c1b140b54c2482.md"]
- rawLogs: [".cq/logs/raw/20260713-231830-a91a4176a0efd8739.jsonl",".cq/logs/raw/20260713-231830-adb81d36d2064a300.jsonl",".cq/logs/raw/20260713-232716-acaab660e4b8c3626.jsonl",".cq/logs/raw/20260713-232716-a00c1b140b54c2482.jsonl"]

## M29

### R80 — revise

- createdAt: 2026-07-13T23:30:43.164Z
- updatedAt: 2026-07-13T23:31:10.027Z
- author: fable-5
- session: cac93b81-5292-42e3-b77e-962544c75e54
- summary: "G6 plan (M30-M33, T100-T115) round-1 reconciled verdict: REVISE (opus revise + fable revise, strictest-wins). Both reviewers verified every grounding citation against source and confirmed Q37-Q43 compliance, fine granularity, and I1-I8/C1-C6 completeness; the round's findings are two DAG/scope gaps on the T115 sync sweep plus one flaky-by-design acceptance assertion on T101. No new user questions; no out-of-scope defects filed."
- new_questions: []
- criticism: ["[opus+fable] T115 (reference-sync sweep) dependsOn is [T105, T107, T109], but its acceptance requires outputs of tasks it does NOT depend on: the wanbond_session_established / last-handshake-age metric names shipped by T101, and design.md's note on the default-route wiring as the one deliberate exception to 'the daemon never assigns routes' — behavior landed by T108. A DAG-ready T115 can be scheduled before T101/T108 complete and would document unshipped or renamed surfaces ('grep for each new metric name across docs/' cannot pass). Add T101 and T108 to T115.dependsOn.","[opus] T115's sync scope is explicitly enumerated as 'install.md §3z + wanbond.example.toml + design.md + README' but omits docs/runbook.md, which install.md designates as the end-to-end operator provisioning procedure. This goal introduces THE primary, previously-undocumented use case (full-tunnel via mode=default-route) plus new NM-drop-in and addressing-oneshot provisioning steps; leaving the runbook silent on them defeats the AGENTS.md docs-in-sync mandate T115 itself invokes. Either add runbook.md to T115's sweep (pointing at the new C1/C3/C4 sections) or record an explicit rationale for excluding it.","[fable] T101's e2e acceptance asserts 'wanbond_path_up=1 observable before the session gauge flips' via metrics scraping. The ordering is structurally guaranteed (the handshake cannot send until a path is healthy — errNoHealthyPath, internal/bind/multipath.go:64), but in the netns tier the handshake completes milliseconds after first path-up (the ~25 s window is a production/WAN artifact), so a scrape-cadence observer will nondeterministically miss the path_up=1/session=0 intermediate state — a flaky-by-design assertion. Reword the acceptance to assert the ordering from log/transition timestamps (or a fixture that gates handshake completion), keeping the 0→1 transition and exactly-once log assertions as-is."]
- ledgerRefs: ["goals:G6"]
- sessionLogs: [".cq/logs/20260713-233100-a55a4e128f6f54f3a.md",".cq/logs/20260713-233100-af3f0626f4832a9e5.md"]
- rawLogs: [".cq/logs/raw/20260713-233100-a55a4e128f6f54f3a.jsonl",".cq/logs/raw/20260713-233100-af3f0626f4832a9e5.jsonl"]

### R81 — revise

- createdAt: 2026-07-13T23:35:50.000Z
- updatedAt: 2026-07-13T23:36:15.771Z
- author: fable-5
- session: cac93b81-5292-42e3-b77e-962544c75e54
- summary: "G6 plan round-2 reconciled verdict: REVISE (opus revise + fable revise, strictest-wins). ALL THREE R80 criticisms verified resolved by both reviewers (T115.dependsOn now carries T101+T108; T115 scope+acceptance cover docs/runbook.md, confirmed to exist as the operator provisioning procedure; T101 ordering asserted from log transition timestamps). Two residual same-class sequencing gaps remain — both 'documenting unshipped surfaces' edges the R80 standard itself established."
- new_questions: []
- criticism: ["[opus] T115's newly-added runbook.md sweep must 'reference the new C1/C3/C4 provisioning steps,' but those sections and their shipped file paths are landed by T110 (C1 NetworkManager drop-in + install.md §4 NM subsection), T111 (C4 addressing oneshot + install.md C4 section), and T113 (C3 full-tunnel recipe) — none of which appear in T115.dependsOn ([T101,T105,T107,T108,T109]). T110 and T111 are dependency-free and T113 depends only on [T108,T111], so T115 becomes DAG-ready once T101/T105/T107/T108/T109 finish and can be dispatched before T110/T111/T113 exist, writing runbook cross-references to unwritten provisioning sections — the same missing-prerequisite class R80's criticism 1 flagged, re-introduced by the criticism-2 fix. Add T110, T111, and T113 to T115.dependsOn.","[fable] T111's acceptance requires the install.md C4 section to carry the tun_persist cross-link, but T111 has no dependsOn on T109 — the task that introduces the key and fixes its final name (T109's description gives it only as 'e.g. top-level tun_persist'). A DAG-ready T111 can be scheduled before T109 and would document an unshipped or subsequently-renamed key — the exact documenting-unshipped-surfaces failure mode R80 established on T115, and inconsistent with the plan's own pattern (T114←T101 for the metric name; T112←T106 for the bind field shape). Fix: add T109 to T111.dependsOn, OR drop the tun_persist cross-link from T111's acceptance and let the T115 sweep (which already depends on T109) add it."]
- ledgerRefs: ["goals:G6"]
- sessionLogs: [".cq/logs/20260713-233606-a6a7deec127907c4c.md",".cq/logs/20260713-233606-a00894726fc25d16c.md"]
- rawLogs: [".cq/logs/raw/20260713-233606-a6a7deec127907c4c.jsonl",".cq/logs/raw/20260713-233606-a00894726fc25d16c.jsonl"]

### R82 — go-ahead

- createdAt: 2026-07-13T23:39:03.366Z
- updatedAt: 2026-07-13T23:39:27.775Z
- author: fable-5
- session: cac93b81-5292-42e3-b77e-962544c75e54
- summary: "G6 plan round-3 reconciled verdict: GO-AHEAD (opus go-ahead + fable go-ahead, unanimous). Both R81 criticisms verified resolved in the current task fields: T115.dependsOn = [T101,T105,T107,T108,T109,T110,T111,T113] (every surface the sync sweep documents now maps to a direct dependency) and T111.dependsOn = [T109] (the tun_persist cross-link orders behind the task that fixes the key name). Full DAG re-verified acyclic with a valid topological order (roots T100/T101/T102/T103/T104/T105/T107/T109/T110 → T106/T108/T111 → T112/T113/T114 → T115); no residual documenting-unshipped-surfaces edge; rubric satisfied (fine-grained: 16 single-concern tasks with surface/wiring splits; sequenced; testable: named tests / grep assertions / systemd-analyze verify / byte-for-byte regression guards; grounded: every citation verified in rounds 1-2, untouched by the round-3 deltas; complete: I1-I8 → T100-T109, C1-C6 → T110-T115); Q37-Q43 bindings and the D35-D40 acceptance-only composition preserved. Review history: R80 (revise, 3 criticisms) → R81 (revise, 2 criticisms) → R82 unanimous go-ahead."
- new_questions: []
- criticism: []
- ledgerRefs: ["goals:G6"]
- sessionLogs: [".cq/logs/20260713-233917-a89c1670ebd3cd89d.md",".cq/logs/20260713-233917-a5034ee3e9ef63fd4.md"]
- rawLogs: [".cq/logs/raw/20260713-233917-a89c1670ebd3cd89d.jsonl",".cq/logs/raw/20260713-233917-a5034ee3e9ef63fd4.jsonl"]

## M24

### R84 — go-ahead

- createdAt: 2026-07-13T23:57:49.652Z
- updatedAt: 2026-07-13T23:57:49.652Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T83 round 1 reconciled panel verdict (opus + fable, unanimous approve, gate green incl. -race, zero existing-test edits): peerState/pathState split verified behavior-preserving — singleton fields genuinely relocated onto peerState (grep-clean on Multipath; handleInbound routes via ps.peer, not promotion); runtime add/remove fan-out single-owner with LIFO rollback; two-peer fan-out test proves per-(peer,path) create/teardown. One out-of-scope latent defect filed file-and-defer ([fable] D42: deferred AddPath probers/m.defs desync with >1 peer bound)."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T83","goals:G4"]
- sessionLogs: [".cq/logs/20260713-235735-a10456d6fb76f7f1c.md",".cq/logs/20260713-235735-aefa45ecf45cfffd3.md"]
- rawLogs: [".cq/logs/raw/20260713-235735-a10456d6fb76f7f1c.jsonl",".cq/logs/raw/20260713-235735-aefa45ecf45cfffd3.jsonl"]

### R85 — go-ahead

- createdAt: 2026-07-14T00:16:43.710Z
- updatedAt: 2026-07-14T00:16:43.710Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T84 round 1 reconciled panel verdict (opus + fable, unanimous approve, gate green uncached): per-peer PSK seam (peerState.psk, newPeerState, newCodec) correct and surgical; the cryptographic-invariant test reframing (never accepted as an authenticated probe; Reflector never reflects cross-psk) judged a deterministic strengthening over the flaky literal decode-failure wording; the ~0.8% cross-psk garble into unauthenticated DATA/PARITY is designed frame-format forgeability, pre-existing, and defended in reseq + inner WG auth — no defect."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T84","goals:G4"]
- sessionLogs: [".cq/logs/20260714-001524-a6a63e0616ed2fed9.md",".cq/logs/20260714-001524-ab4832a41c717ad90.md"]
- rawLogs: [".cq/logs/raw/20260714-001524-a6a63e0616ed2fed9.jsonl",".cq/logs/raw/20260714-001524-ab4832a41c717ad90.jsonl"]

### R86 — go-ahead

- createdAt: 2026-07-14T00:16:49.384Z
- updatedAt: 2026-07-14T00:16:49.384Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T85 round 1 reconciled panel verdict (opus + fable, unanimous approve, gate green + -race): Send routes via peerByVirt to the owning peerState (outerSeq/scheduler/sendCodec/fecSend/per-(peer,path) egress); acceptance test discriminates the fix and verifies wire-level egress at each stand-in remote; unknown endpoint errors with zero side effects; single-peer behavior preserved. One out-of-scope defect filed file-and-defer ([fable] D44: fecFlushDeadline primary-only — per-peer FEC from T91/T93 would silently lose straggler parity)."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T85","goals:G4"]
- sessionLogs: [".cq/logs/20260714-001524-a370601a73478ca92.md",".cq/logs/20260714-001524-a2b4d488f2a5615f5.md"]
- rawLogs: [".cq/logs/raw/20260714-001524-a370601a73478ca92.jsonl",".cq/logs/raw/20260714-001524-a2b4d488f2a5615f5.jsonl"]

### R87 — go-ahead

- createdAt: 2026-07-14T00:19:16.295Z
- updatedAt: 2026-07-14T00:19:16.295Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T86 round 1 reconciled panel verdict (opus + fable, unanimous approve, gate green fresh + -race): single ReceiveFunc drains each bound peer's resequencer round-robin with a lock-free peersView atomic snapshot and stamps each delivered datagram with the owning peer's virt (A1); acceptance test discriminates against base behavior (no cross-peer leak); rr cursor race-free by construction (single drainer goroutine); existing-test edits are mechanical signature plumbing."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T86","goals:G4"]
- sessionLogs: [".cq/logs/20260714-001524-a9eeeffca5db3fd43.md",".cq/logs/20260714-001524-ae44d2e7f23156dd5.md"]
- rawLogs: [".cq/logs/raw/20260714-001524-a9eeeffca5db3fd43.jsonl",".cq/logs/raw/20260714-001524-ae44d2e7f23156dd5.jsonl"]

### R89 — go-ahead

- createdAt: 2026-07-14T00:48:39.352Z
- updatedAt: 2026-07-14T00:48:39.352Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T87 round 1 reconciled panel verdict (opus + fable, unanimous approve, gate green, mutation-verified guard): per-peer Open datapath rebuild (openPeerDatapathLocked over m.peers, fixing a real Close→Open asymmetry vs closeSocketsLocked) + peer-scoped D32 rebaseline (setPeerRemoteLocked); all three acceptance clauses verified operationally; single-peer path semantics-preserving. [fable] noted the FEC deadline-tick primary-only gap — substance already tracked as D44, no duplicate filed."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T87","goals:G4"]
- sessionLogs: [".cq/logs/20260714-004825-a19e6e3ee6175b726.md",".cq/logs/20260714-004825-a260f3da7a78a9174.md"]
- rawLogs: [".cq/logs/raw/20260714-004825-a19e6e3ee6175b726.jsonl",".cq/logs/raw/20260714-004825-a260f3da7a78a9174.jsonl"]

## M21

### R90 — go-ahead

- createdAt: 2026-07-14T01:02:40.367Z
- updatedAt: 2026-07-14T01:02:40.367Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T70 terminal reconciled panel verdict after 2 rounds: R1 [opus] approve / [fable] disapprove (strictest-wins) on 2 criticisms — activeSpec==-1 absorbing boot state (single-hostname peer never receives an endpoint) and missing lastSwitch settle-dwell reset on repoint; R2 (276a6ea) unanimous approve — boot adoption with one SetPeerRemote+rehandshake+dwell-arm, lastSwitch reset in the repoint branch, both new tests verified failing pre-fix. Two out-of-scope defects filed file-and-defer: D45 (pre-existing lint red at base) and D46 (T73-contingent total<2 stranding corner)."
- criticism: ["[fable+opus, R1, RESOLVED R2] updateResolution could not activate from the all-empty boot state — single-hostname peer's bond never received an endpoint through the update API T73 must use.","[fable, R1, RESOLVED R2] the active-IP-change repoint skipped the lastSwitch settle-dwell reset, allowing an immediate second disruptive advance."]
- new_questions: []
- ledgerRefs: ["tasks:T70","goals:G5"]
- sessionLogs: [".cq/logs/20260714-005639-a4a5512b111f43aa0.md",".cq/logs/20260714-005639-a5c7bef556145b214.md",".cq/logs/20260714-010221-a67a4e5aa186a68a1.md",".cq/logs/20260714-010221-a7f9ccd45b1752e7f.md"]
- rawLogs: [".cq/logs/raw/20260714-005639-a4a5512b111f43aa0.jsonl",".cq/logs/raw/20260714-005639-a5c7bef556145b214.jsonl",".cq/logs/raw/20260714-010221-a67a4e5aa186a68a1.jsonl",".cq/logs/raw/20260714-010221-a7f9ccd45b1752e7f.jsonl"]

### R91 — go-ahead

- createdAt: 2026-07-14T01:43:04.490Z
- updatedAt: 2026-07-14T01:43:04.490Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T73 terminal reconciled panel verdict after 2 rounds: R1 [opus] approve / [fable] disapprove (strictest-wins) on 2 criticisms — family-filter clause unimplemented (v6-only answer on v4-only paths could strand the bond via the success path) and out-of-band re-arm dropping the TTL clamp; R2 (0d36a23) unanimous approve — pathFamilies filtering with D46-retention on all-filtered answers (fail-first verified), clampPollDelay on both re-arms, docs synced. D46 resolved by the never-publish-empty retention policy. [fable] re-noted the lint-red-at-base finding — duplicates D45, not re-filed."
- criticism: ["[fable, R1, RESOLVED R2] family-filter clause unimplemented — fixed via pathFamiliesFromPaths + orderAddrPorts filtering + D46 retention path.","[fable, R1, RESOLVED R2] checkLivenessLoss discarded the TTL clamp on the out-of-band re-arm — fixed via clampPollDelay on both re-arms."]
- new_questions: []
- ledgerRefs: ["tasks:T73","goals:G5","defects:D46"]
- sessionLogs: [".cq/logs/20260714-013526-aa5af95a3b03fde16.md",".cq/logs/20260714-013526-a655a1c595ce4648a.md",".cq/logs/20260714-014014-a6681895aad44fda0.md",".cq/logs/20260714-014014-ad33623fb263f9dc1.md"]
- rawLogs: [".cq/logs/raw/20260714-013526-aa5af95a3b03fde16.jsonl",".cq/logs/raw/20260714-013526-a655a1c595ce4648a.jsonl",".cq/logs/raw/20260714-014014-a6681895aad44fda0.jsonl",".cq/logs/raw/20260714-014014-ad33623fb263f9dc1.jsonl"]

### R94 — go-ahead

- createdAt: 2026-07-14T02:46:44.540Z
- updatedAt: 2026-07-14T02:46:44.540Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T74 terminal reconciled panel verdict after 2 rounds: R1 [opus] approve / [fable] disapprove (strictest-wins) on ONE criticism — the R70 first-resolve INSTALL path was never exercised through the PRODUCTION wiring (startFailoverAndResolution's ctrl.install assignment ran in no test; installActive's nil→SetPeerRemote fallback made a lost wiring line a silent R70 regression); acceptance 1/3/4 verified end-to-end. R2 (1df3f11) unanimous approve — worker did BOTH recommendations: added TestUpFirstResolveInstallsEndpointThroughProductionWiring (drives up()→startFailoverAndResolution with a boot-fail-then-succeed resolver, asserts engine peer endpoint via IpcGet) AND made install a REQUIRED newHubFailoverFromSpecs constructor param (removed the silent SetPeerRemote fallback; installActive deleted — lost wiring is now a compile error). BOTH reviewers independently mutation-verified the new test FAILS when the production install line is no-op'd. Rebased onto main (past T89) and ff-merged as 6ceee83; full go build/vet/test green on main."
- criticism: ["[fable, R1, RESOLVED R2] the R70 install path was untested through production wiring and the install-nil fallback made a lost wiring line a silent regression — fixed via an up()-driven production-wiring test and a required install collaborator."]
- new_questions: []
- ledgerRefs: ["tasks:T74","goals:G5"]
- sessionLogs: [".cq/logs/20260714-023944-a9fac7819e0cf8c3d.md",".cq/logs/20260714-023944-ac7d0d623c6361bff.md",".cq/logs/20260714-024545-a417b8e13ab58fa54.md",".cq/logs/20260714-024545-a534c16da5521baae.md"]
- rawLogs: [".cq/logs/raw/20260714-023944-a9fac7819e0cf8c3d.jsonl",".cq/logs/raw/20260714-023944-ac7d0d623c6361bff.jsonl",".cq/logs/raw/20260714-024545-a417b8e13ab58fa54.jsonl",".cq/logs/raw/20260714-024545-a534c16da5521baae.jsonl"]

### R97 — go-ahead

- createdAt: 2026-07-14T03:13:57.991Z
- updatedAt: 2026-07-14T03:13:57.991Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T75 unanimous panel approve (round 1): [opus]+[fable] both approve. Test-only (internal/device/interleave_race_test.go, +321): 3 cross-controller -race interleave tests for the two endpoint-set co-owners (re-resolve mid-advance; failover advance between lookup and apply; simultaneous liveness-loss stress 50×2×20) + a Q36 boot-defer seam unit test. The exactly-one-SetPeerRemote property was verified STRUCTURAL by both reviewers against failover.go/resolution.go (check() and updateResolution() each hold h.mu across their whole body; first repoint arms the settle dwell + spec-scoped/survival guards no-op any second). [fable] independently mutation-tested: removing h.mu from updateResolution failed BOTH the race detector AND the exactly-one assertion (2 calls at iteration 17) — proving schedule 3 is genuine goroutine contention and the assertion non-vacuous. Acceptance `go test -race ./internal/config/... ./internal/device/... ./internal/dnsresolve/...` green (stable -count=2/3, bounded by a 5s per-schedule deadlock guard)."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T75","goals:G5"]
- sessionLogs: [".cq/logs/20260714-031341-ad2d941ce63da2108.md",".cq/logs/20260714-031341-a2380182ab5d80508.md"]
- rawLogs: [".cq/logs/raw/20260714-031341-ad2d941ce63da2108.jsonl",".cq/logs/raw/20260714-031341-a2380182ab5d80508.jsonl"]

## M25

### R92 — go-ahead

- createdAt: 2026-07-14T01:56:30.515Z
- updatedAt: 2026-07-14T01:56:30.515Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T88 terminal reconciled panel verdict after 2 rounds: R1 [opus] approve / [fable] disapprove (strictest-wins) on 2 criticisms — trial loop stopped on first DECODE not first MAC-verify (unMAC'd cross-psk garble aborted the trial, dropping a genuine later-peer PROBE ~0.4%) and a non-discriminating stop-at-first-match test; R2 (babebc6) unanimous approve — non-PROBE decode continues, comment corrected, two discriminating tests, each independently mutation-verified by BOTH reviewers on clean exports; drop-unbound-DATA guarantee verified intact. D47 filed R1 (Addr-only binding key excludes a second peer behind one public IP — for the T90 design)."
- criticism: ["[fable, R1, RESOLVED R2] trial-decode stopped on first decode rather than first MAC verification — fixed via continue on non-PROBE decodes.","[fable, R1, RESOLVED R2] stop-at-first-match acceptance clause had no discriminating test — fixed via the shared-psk both-peers test."]
- new_questions: []
- ledgerRefs: ["tasks:T88","goals:G4"]
- sessionLogs: [".cq/logs/20260714-015014-ae4c878a9b8cdabcd.md",".cq/logs/20260714-015014-a76ead6a90c6f25fb.md"]
- rawLogs: [".cq/logs/raw/20260714-015014-ae4c878a9b8cdabcd.jsonl",".cq/logs/raw/20260714-015014-a76ead6a90c6f25fb.jsonl"]

### R93 — go-ahead

- createdAt: 2026-07-14T02:21:29.069Z
- updatedAt: 2026-07-14T02:21:29.069Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T89 terminal reconciled panel verdict after 2 rounds: R1 [opus] approve / [fable] disapprove (strictest-wins) on ONE criticism — the PARITY subtest was non-discriminating under the FEC-off fixture (a leaked PARITY dropped at nil fecRecv regardless of the gate); R2 (7900642) unanimous approve — the subtest now arms receive-side FEC on both peers via Open's exact seam and uses a DataCount=1 parity that reconstructs on a leak, mutation-verified discriminating by BOTH reviewers independently. Test-only deliverable pinning the T88 gate; the unbound-DATA/PARITY drop is production behavior from T88's continue-then-drop trial loop."
- criticism: ["[fable, R1, RESOLVED R2] the PARITY subtest was vacuous under the FEC-off fixture — fixed by arming fecRecv on both peers and observing FEC-decoder reconstruction on a leak."]
- new_questions: []
- ledgerRefs: ["tasks:T89","goals:G4"]
- sessionLogs: [".cq/logs/20260714-021726-a68d4cd936a9aeaec.md",".cq/logs/20260714-021726-a3a44fe24270c9538.md",".cq/logs/20260714-022112-a366f6435dd6b4681.md",".cq/logs/20260714-022112-a4a8424b945a1df11.md"]
- rawLogs: [".cq/logs/raw/20260714-021726-a68d4cd936a9aeaec.jsonl",".cq/logs/raw/20260714-021726-a3a44fe24270c9538.jsonl",".cq/logs/raw/20260714-022112-a366f6435dd6b4681.jsonl",".cq/logs/raw/20260714-022112-a4a8424b945a1df11.jsonl"]

### R95 — go-ahead

- createdAt: 2026-07-14T03:07:15.964Z
- updatedAt: 2026-07-14T03:07:15.964Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T90 unanimous panel approve (round 1): [opus]+[fable] both approve. Test-only (internal/bind/concentrator_roam_test.go) locking the per-peer NAT/roaming discipline provided by the T88/T89 unbound-source gate + PROBE-only bindSourceToPeer. BOTH reviewers independently ran the decisive roam-specific mutation (relearn an already-bound peer's roam from unauthenticated DATA): T90 FAILS while T89 PASSES — proving T90 discriminates the already-bound-peer/new-source roam property T89 does not cover. A-resequencer isolation + D11 authenticated re-learn (view remote repoints to the new source) asserted; -race green. Note: D47 (no unbind path / address-only keying) remains open — T90 is behavior-locking, did not settle it."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T90","goals:G4"]
- sessionLogs: [".cq/logs/20260714-030657-a4a3590b837613332.md",".cq/logs/20260714-030657-aa1362bb37a2ee5d5.md"]
- rawLogs: [".cq/logs/raw/20260714-030657-a4a3590b837613332.jsonl",".cq/logs/raw/20260714-030657-aa1362bb37a2ee5d5.jsonl"]

### R101 — go-ahead

- createdAt: 2026-07-14T03:52:53.997Z
- updatedAt: 2026-07-14T03:52:53.997Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T91 terminal reconciled panel verdict after 2 rounds. R1: [opus] disapprove (dispatchInbound nil-guards untested — mutation survived) + [fable] disapprove (FEC-plane lifecycle vacuously tested under the FEC-off fixture; AND a REAL production defect — fecSend freed on teardown but never re-instantiated on re-bind → silent parity loss; + a CAS ordering hole). 2 deferred defects filed (D49 insider cap-monopoly, D50 untracked TearDownPeer device wiring). R2 (2b9a0de) UNANIMOUS approve: worker (1) added TestDispatchInboundNilGuardsDropNotPanic (still-bound source + niled ring) + a -race teardown-vs-demux test; (2) added TestConcentratorFECReceivePlaneLifecycle (FEC-ENABLED, asserts real reconstruction absent→instantiated→freed→re-instantiated); (3) FIXED the production defect — ensurePeerReceiveInstantiated now rebuilds fecSend via newFECSender, a per-peer lifecycleMu serializes heavy-trio build vs teardown/close, fecSend made atomic.Pointer. BOTH R2 reviewers independently (isolated git-archive copies) mutation-verified: deleting fecRecv install → receive-lifecycle test RED; deleting fecSend rebuild → send-reinstantiation test RED (parityFrames counts post-WriteToUDPAddrPort egress, so a rebound peer provably emits parity on the wire); DATA nil-guard removal → nil-deref panic. Deadlock-free: strict m.mu ⊃ lifecycleMu, no cycle. go test -race ./internal/bind/... -count=2 green (incl. a 400-round concurrent teardown/rebind test). Rebased onto current main and ff-merged as a99c3ed."
- criticism: ["[opus, R1, RESOLVED R2] dispatchInbound DATA+PARITY nil-guards were untested (mutation survived) — added still-bound-source niled-ring guard tests + a -race teardown-vs-demux test.","[fable, R1, RESOLVED R2] FEC-plane lifecycle vacuously tested under the FEC-off fixture — added an FEC-enabled lifecycle test asserting real reconstruction across teardown+re-bind.","[fable, R1, RESOLVED R2] PRODUCTION DEFECT: fecSend freed on teardown but never re-instantiated on re-bind (silent parity loss) + CAS ordering hole — fixed by rebuilding fecSend on re-bind + a per-peer lifecycleMu ordering build vs teardown; fecSend made atomic.Pointer."]
- new_questions: []
- ledgerRefs: ["tasks:T91","goals:G4","defects:D49","defects:D50"]
- sessionLogs: [".cq/logs/20260714-032122-acd6bfff48ecc6611.md",".cq/logs/20260714-032122-a43969b0d13dec49c.md",".cq/logs/20260714-035218-a84c7434f6d908139.md",".cq/logs/20260714-035218-a6f8746b8e0351608.md"]
- rawLogs: [".cq/logs/raw/20260714-032122-acd6bfff48ecc6611.jsonl",".cq/logs/raw/20260714-032122-a43969b0d13dec49c.jsonl",".cq/logs/raw/20260714-035218-a84c7434f6d908139.jsonl",".cq/logs/raw/20260714-035218-a6f8746b8e0351608.jsonl"]

### R104 — go-ahead

- createdAt: 2026-07-14T04:21:56.054Z
- updatedAt: 2026-07-14T04:21:56.054Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T92 unanimous panel approve (round 1): [opus]+[fable] both approve. Test-only (internal/bind/threat_model_test.go, +297) codifying the Q27 cross-peer isolation threat model as adversary cases against the T83-T91 demux. Attacks target a source ALREADY BOUND to peer A (genuinely beyond T88/89/90's unbound coverage): foreign-psk PROBEs, wrong-psk PROBEs, replay, byte-mutation, forged DATA+outer-seq-storm, and a 300-source unauthenticated flood — A's binding, resequencer release point, FEC decoder, and liveness all asserted intact; the flood binds nothing, grows no demux state, evicts no live peer. Sentinel assertion cryptographically sound (DATA/PARITY are obfuscation-only/unauthenticated, so a wrong-psk decode can never reproduce a CHOSEN sentinel); release-point assertion made deterministic by the reseq discontinuity guard. BOTH reviewers independently mutation-verified BOTH isolation guards (demuxInbound bound-source early-return removal → binding re-pointed to B; isProbe D9/D11 gate removal → demux map grew 1→~204). go test -race ./internal/bind/... -count=2 + full gate green. No production isolation defect found. Rebased onto current main and ff-merged as e3c2655."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T92","goals:G4"]
- sessionLogs: [".cq/logs/20260714-042139-accf6c05ac3dfa0e1.md",".cq/logs/20260714-042139-a4291b0fb8fad1812.md"]
- rawLogs: [".cq/logs/raw/20260714-042139-accf6c05ac3dfa0e1.jsonl",".cq/logs/raw/20260714-042139-a4291b0fb8fad1812.jsonl"]

## M30

### R96 — go-ahead

- createdAt: 2026-07-14T03:08:42.637Z
- updatedAt: 2026-07-14T03:08:42.637Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T100 unanimous panel approve (round 1): [opus]+[fable] both approve. device.Up now sets IFF_UP on wanbond0 via SIOCGIFFLAGS→OR IFF_UP→SIOCSIFFLAGS read-modify-write (golang.org/x/sys/unix, no new dep; consts verified in x/sys v0.35.0), in the production-only Up() wrapper (device.go:210) BEFORE the up() unit-test seam — so fake-TUN unit tests are untouched but real runs get IFF_UP. Control socket closed via defer; TUN closed on ifUp failure; other IFF_ flags preserved; NO address assignment (operator-owned); teardown unchanged; INFO 'interface up' logged. New linkup_linux.go + !linux stub mirroring pathsock_{linux,other}.go; darwin cross-compiles. The -tags e2e netns test (test/e2e/link_up_test.go) compiles/vets and asserts UP-without-external-ip-link + no daemon address on both roles; privileged execution DEFERRED (G2 pattern). docs/install.md synced. Full non-privileged gate green. NOTE: the [fable] reviewer's filed defect (lint red at base: doh.go:206, dot.go:168, pathsock.go:166) is a verified DUPLICATE of open D45 — not re-filed."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T100","goals:G6"]
- sessionLogs: [".cq/logs/20260714-030756-aa7c68662525d4b3f.md",".cq/logs/20260714-030756-a9196a9bc3bed8ec8.md"]
- rawLogs: [".cq/logs/raw/20260714-030756-aa7c68662525d4b3f.jsonl",".cq/logs/raw/20260714-030756-a9196a9bc3bed8ec8.jsonl"]

### R98 — go-ahead

- createdAt: 2026-07-14T03:19:04.622Z
- updatedAt: 2026-07-14T03:19:04.622Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T104 unanimous panel approve (round 1): [opus]+[fable] both approve; both independently filed the SAME defect (deduped into D48). Q39 in-goal VERIFICATION task — acceptance is 'well-formed netns test that either proves bidirectional liveness OR fails-and-is-refiled as a repro'; both satisfied. New test/e2e/standby_liveness_test.go (TestStandbyLivenessBidirectional, 2 subtests) + Topology.BlockEgress/UnblockEgress (tc clsact matchall drop). [fable] EMPIRICALLY validated BlockEgress in an unshare -Urmn netns replica (outbound 100% loss, inbound intact, netem coexists+survives unblock, idempotent teardown). BOTH source-confirmed the emitProbes tx-omission (probe/echo writes bypass ps.txBytes; only Send/fecFlushDeadline count) — the real cause of the production path_up=1/tx=0 — and confirmed it is a METRICS fault, NOT a liveness hole (liveness genuinely bidirectional: only HandleEcho marks up). Non-privileged gate + golangci --build-tags e2e green; privileged netns execution DEFERRED to hardware (G2 pattern). Filed defect D48 (goals:G6) with subtest (a) as the kept repro."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T104","goals:G6","defects:D48"]
- sessionLogs: [".cq/logs/20260714-031845-a6d96b8f39ee0fc04.md",".cq/logs/20260714-031845-ae358e9b638958305.md"]
- rawLogs: [".cq/logs/raw/20260714-031845-a6d96b8f39ee0fc04.jsonl",".cq/logs/raw/20260714-031845-ae358e9b638958305.jsonl"]

### R102 — go-ahead

- createdAt: 2026-07-14T03:57:32.248Z
- updatedAt: 2026-07-14T03:57:32.248Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T101 terminal reconciled panel verdict after 2 rounds: R1 [opus] approve / [fable] disapprove (strictest-wins) on ONE criticism — the e2e i2MetricsListen port 9099 collided with hub_failover_test.go's hfMetricsListen and its 'unique port' comment was factually false (substance was otherwise sound: both reviewers verified the 0→1 edge detector mutation-kills in isolated copies, bind stays WG-unaware, UAPI last_handshake parse correct against amneziawg-go source). R2 (51b6df6) unanimous approve — worker surveyed all e2e *MetricsListen ports and moved i2 to the unused 9101 (both reviewers grep-verified uniqueness) and de-staled the comment; round-2 diff test-only. A PRE-EXISTING 9096 collision (pacing_test vs p3_fec_test) surfaced during the survey and was filed as D51 (out of scope). Rebased onto current main and ff-merged as 1957f21."
- criticism: ["[fable, R1, RESOLVED R2] e2e i2MetricsListen 9099 collided with hub_failover's hfMetricsListen + false unique-port comment — moved to unused 9101, comment de-staled, grep-verified unique."]
- new_questions: []
- ledgerRefs: ["tasks:T101","goals:G6","defects:D51"]
- sessionLogs: [".cq/logs/20260714-035711-af7fe1e7fcf138c03.md",".cq/logs/20260714-035711-ac7254fc378c0095a.md",".cq/logs/20260714-035711-a1a5495814cda5845.md",".cq/logs/20260714-035711-ac04c993630a8da43.md"]
- rawLogs: [".cq/logs/raw/20260714-035711-af7fe1e7fcf138c03.jsonl",".cq/logs/raw/20260714-035711-ac7254fc378c0095a.jsonl",".cq/logs/raw/20260714-035711-a1a5495814cda5845.jsonl",".cq/logs/raw/20260714-035711-ac04c993630a8da43.jsonl"]

### R112 — go-ahead

- createdAt: 2026-07-14T06:12:11.412Z
- updatedAt: 2026-07-14T06:12:11.412Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T102 unanimous panel approve (round 1): [opus]+[fable] both approve. Added diagnosingTUN — a tun.Device decorator wrapping the engine's TUN so every Write is diagnosed: on syscall.EIO (detected via errors.Is, not a fragile string match) it inspects the interface IFF_UP/MTU state via a new read-only ifState ioctl (mirroring T100's ifUp) and logs ONE rate-limited (30s sliding-window) actionable ERROR naming the state (DOWN/UP/unknown — probe-driven, NOT hardcoded) + pointing at install.md §4 + the raw numeric errno, while returning the original (n,err) UNCHANGED on every path (transparent). BOTH reviewers verified transparency by reading Write; [fable] confirmed the ioctl is GATED behind the rate limiter (early return precedes probeState — no ioctl-storm under a write storm) and killed 4/4 mutants (unthrottled, latch-once, always-DOWN, any-error-diagnosed); [opus] confirmed the sliding-window limiter via burst=1/post-window=2/strict-< boundary tests + that awgdevice consumes tun.Device purely by interface (decorator can't break the engine). Wired into up(); !linux stub builds; docs/install.md §4 synced. Rebased onto current main (device.go + install.md overlapped T107; clean 3-way merge, gate re-run green) and ff-merged as 890ab43."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T102","goals:G6"]
- sessionLogs: [".cq/logs/20260714-061135-a8a45233ae279d4cd.md",".cq/logs/20260714-061135-a0b85a40cc20e154c.md"]
- rawLogs: [".cq/logs/raw/20260714-061135-a8a45233ae279d4cd.jsonl",".cq/logs/raw/20260714-061135-a0b85a40cc20e154c.jsonl"]

### R116 — go-ahead

- createdAt: 2026-07-14T06:42:34.546Z
- updatedAt: 2026-07-14T06:42:34.546Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T103 unanimous panel approve (round 1): [opus]+[fable] both approve. Downgrades the startup no-healthy-path ERROR spam during the liveness warmup (I4). Exported bind.ErrNoHealthyPath + added a sticky, race-free Multipath.EverHadLivePath() latch (atomic.Bool, Store(true) only) set at the SOLE Down→Up transition site (dispatchInbound's HandleEcho echo branch → RecordEcho → transition(StateUp)). engineLogger now takes an everHadLivePath func() bool and, before the first path-up, COALESCES every ErrNoHealthyPath-wrapping Errorf record into exactly ONE INFO 'waiting for path liveness' via a warmupInfoLogged atomic CAS (detection is errors.Is on the Errorf args vs the exported sentinel — NOT string-match; the engine passes the raw bind error unwrapped through SendBuffers→Errorf, confirmed in amneziawg-go v1.0.4); after first path-up the same error logs at ERROR. Unrelated engine errors still log at their normal level. BOTH reviewers independently mutation-verified in isolated copies: gate removed → pre-up ERRORs; once-latch removed → INFO-per-record; everUp never set → sticky test fails. Both engineLogger callers updated (production plumbs the real mpBind.EverHadLivePath). -race + all-tags gate green. Rebased onto current main (gate re-run green) and ff-merged as 445c332. [fable]'s lint-at-base defect is a DUPLICATE of D45."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T103","goals:G6","defects:D45"]
- sessionLogs: [".cq/logs/20260714-064150-a384714613befbd9b.md",".cq/logs/20260714-064150-a6746d623c2e7e8c7.md"]
- rawLogs: [".cq/logs/raw/20260714-064150-a384714613befbd9b.jsonl",".cq/logs/raw/20260714-064150-a6746d623c2e7e8c7.jsonl"]

## M31

### R99 — go-ahead

- createdAt: 2026-07-14T03:41:06.708Z
- updatedAt: 2026-07-14T03:41:06.708Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T105 unanimous panel approve (round 1): [opus]+[fable] both approve. Config-surface-only: BindMode (\"source\"|\"device\"|\"auto\") added to config.Config (top-level `bind` default) + config.Path (per-path override). normalize() defaults an empty global to auto BEFORE resolving each path's empty bind to the global — so precedence is path>global>auto (both reviewers verified the ordering trap: empty global + set path, set global + empty path, both-set). validate() rejects unknown values for BOTH surfaces, naming the offending path. [fable] ran go test ./... green in the worktree and grep-confirmed selectDeviceBinds/planPathBinds are UNCHANGED (no behavior regression; default auto == today's behavior). Golden DeepEqual fixture updated. Doc-sync for the new key deliberately deferred to T115 (dependsOn T105) per the plan."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T105","goals:G6"]
- sessionLogs: [".cq/logs/20260714-034050-ad3878949437704f2.md",".cq/logs/20260714-034050-a88c682fa1f564cce.md"]
- rawLogs: [".cq/logs/raw/20260714-034050-ad3878949437704f2.jsonl",".cq/logs/raw/20260714-034050-a88c682fa1f564cce.jsonl"]

### R106 — go-ahead

- createdAt: 2026-07-14T04:44:33.251Z
- updatedAt: 2026-07-14T04:44:33.251Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T106 terminal reconciled panel verdict after 3 rounds. R1 [opus] approve / [fable] disapprove (strictest-wins): the AddPath/deferred-reconcile device-mode wiring was UNVERIFIED (neutering resolveForcedDeviceBind survived the full suite); + a silent device-fallback-WARN defect (→ D53, both reviewers). R2 (97968bb): worker split resolveForcedDeviceBind into a pure selectForcedDeviceBind + real-interfaces wrapper and added a resolveDeviceBind injection seam — but BOTH reviewers disapprove again: Mutation D (hardcode dev=\"\" in AddPath) still SURVIVED because AddPath called package-level listenPath directly (only the RECONCILE half got a seam test). R3 (f6ae3f1): worker added an m.addPathListen injection seam (mirroring deferredListen) + an env-independent TestAddPathThreadsForcedDeviceBind. UNANIMOUS approve — BOTH reviewers independently re-ran Mutation D in isolated copies and confirmed it now reddens deterministically ('threaded dev = \"\", want \"wan0\"'), no real-interface dependency/skip; the seam default is provably identical to the prior direct listenPath call (no behavior change); auto byte-for-byte; -race green. Rebased onto current main (3-commit replay, gate re-run green) and ff-merged as cb6547e. D53 (device-fallback WARN) deferred throughout."
- criticism: ["[fable, R1, RESOLVED R2/R3] the forced-device-bind wiring was unverified — split into a testable decision + injection seams; reconcile half covered R2, AddPath half covered R3 (m.addPathListen seam), both mutation-verified env-independent."]
- new_questions: []
- ledgerRefs: ["tasks:T106","goals:G6","defects:D53"]
- sessionLogs: [".cq/logs/20260714-041808-ae0780df317aa2c57.md",".cq/logs/20260714-041808-aa9c60cbe80cacf55.md",".cq/logs/20260714-043501-a9441e152c838dd6c.md",".cq/logs/20260714-043501-a5ef1e10bfec6b3bf.md",".cq/logs/20260714-044354-a039c99963c717f61.md",".cq/logs/20260714-044354-a439e530d4d66f5bc.md"]
- rawLogs: [".cq/logs/raw/20260714-041808-ae0780df317aa2c57.jsonl",".cq/logs/raw/20260714-041808-aa9c60cbe80cacf55.jsonl",".cq/logs/raw/20260714-043501-a9441e152c838dd6c.jsonl",".cq/logs/raw/20260714-043501-a5ef1e10bfec6b3bf.jsonl",".cq/logs/raw/20260714-044354-a039c99963c717f61.jsonl",".cq/logs/raw/20260714-044354-a439e530d4d66f5bc.jsonl"]

### R110 — go-ahead

- createdAt: 2026-07-14T06:07:51.891Z
- updatedAt: 2026-07-14T06:07:51.891Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T107 unanimous panel approve (round 1): [opus]+[fable] both approve. uapiConfig now UNCONDITIONALLY splits a literal 0.0.0.0/0 → 0.0.0.0/1 + 128.0.0.0/1 and ::/0 → ::/1 + 8000::/1 (via splitDefaultRoute) so the amneziawg engine NEVER receives the literal /0 that wedges the handshake (D35 deterministic sidestep). Added an edge-only Peer.Mode=\"default-route\" config surface (PeerMode type), validation-rejected on the concentrator + unknown-value-rejected, fail-fast at Load. BOTH reviewers mutation-verified the split (passthrough-mutating splitDefaultRoute leaks allowed_ip=0.0.0.0/0 + ::/0 and fails both new tests); confirmed the split prefixes are EXACTLY correct for v4+v6, non-/0 CIDRs pass through unchanged, and [fable] verified the UNCONDITIONAL split is a routing no-op vs /0 under cryptokey longest-prefix-match (strictly safer than mode-gating, which would leave the wedge live for a literal /0 written without the mode). No pre-existing uapiConfig test relied on a literal /0. docs (wanbond.example.toml + install.md) synced; full go test ./... green. ff-merged as e958035. [fable] filed a new low defect D55 (allowed_ips CIDR syntax unvalidated at load)."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T107","goals:G6","defects:D55"]
- sessionLogs: [".cq/logs/20260714-060726-a48e2a04e50fbf112.md",".cq/logs/20260714-060726-af9cfffd1ceb8b455.md"]
- rawLogs: [".cq/logs/raw/20260714-060726-a48e2a04e50fbf112.jsonl",".cq/logs/raw/20260714-060726-af9cfffd1ceb8b455.jsonl"]

### R120 — go-ahead

- createdAt: 2026-07-14T07:47:27.336Z
- updatedAt: 2026-07-14T07:47:27.336Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T108 unanimous panel approve at ROUND 2 ([opus]+[fable] both approve; round 1 was split — opus approve, fable disapprove). Edge default-route wiring under mode=default-route (G6/I6): new internal/device/route_linux.go programs the wg-quick-style split default route (the two /1s of the default-route peer's allowed_ips, reusing T107's splitDefaultRoute) into wanbond0 via hand-rolled rtnetlink (golang.org/x/sys/unix; route_other.go non-Linux stub), installed after dev.Up() and withdrawn on Close. STRICT Q41 boundary held: scope-link device routes ONLY — no policy routing, SNAT, ip_forward, or FORWARD programming. Round 1: opus verified rtnetlink message/attribute/ACK construction + socket lifecycle + Q41 + regression guard all correct; fable DISAPPROVED on 2 objective lifecycle defects — (1) NLM_F_EXCL made restart after an unclean death under tun_persist=true fail EEXIST forever (unrecoverable bring-up loop, since the persistent wanbond0 keeps its /1 routes), and (2) a partial-install leak because up() returns before the Tunnel is constructed so Close/removeRoutes never runs. Round 2 fixed BOTH: route add flags factored into a pure routeMsgFlags(add) helper using NLM_F_CREATE|NLM_F_REPLACE (never EXCL) — `ip route replace` semantics that ADOPT/normalize a leftover route on restart (matching the persist_linux.go TUN-adoption posture) and no-op on duplicate prefixes — pinned by TestRouteMsgFlags; and a best-effort removeRoutes(name,prefixes) on the up() install-error path (correctly ordered before dev.Close while the iface still exists; ESRCH/ENOENT-tolerant) plus a corrected installRoutes comment. Both reviewers re-verified against source; the REPLACE-overwrites-foreign-route hazard judged pre-existing in kind (teardown DELROUTE was never ownership-checked) and consistent with the daemon's converge-to-intended-state posture. Regression guard (no default-route peer → no socket, no route, byte-for-byte unchanged) intact; splitDefaultRoute reused. Full gate + -tags e2e compile/vet green; PRIVILEGED netns exec of test/e2e/default_route_test.go DEFERRED to the o3 + llm-ubuntu-0 hosts (G2 pattern). Rebased onto current main (over T79/T99 docs) cleanly and ff-merged as 8bb24a9. fable's config-validation finding (multiple mode=default-route peers) filed as D59."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T108","goals:G6","defects:D59"]
- sessionLogs: [".cq/logs/20260714-074804-a95d32452b71677e1.md",".cq/logs/20260714-074804-aaac034e84e494ebc.md"]
- rawLogs: [".cq/logs/raw/20260714-074804-a95d32452b71677e1.jsonl",".cq/logs/raw/20260714-074804-aaac034e84e494ebc.jsonl"]

## M32

### R100 — go-ahead

- createdAt: 2026-07-14T03:48:56.938Z
- updatedAt: 2026-07-14T03:48:56.938Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T110 terminal reconciled panel verdict after 2 rounds: R1 [opus] approve / [fable] disapprove (strictest-wins) on TWO criticisms — (1) install.md '(or alongside NetworkManager) you do not need this drop-in' was factually WRONG (NM active flushes the address regardless of networkd), (2) packaging_test.go's substring Contains assertion passed on a commented-out directive (vacuity hole). R2 (c3882a8) unanimous approve — worker restated the skip advice (drop-in required whenever NM is active, even alongside networkd) and replaced the substring check with exact trimmed-line equality (== 'unmanaged-devices=interface-name:wanbond0') + a '[keyfile]' section-line assertion. BOTH R2 reviewers independently mutation-verified the test now FAILS on a commented-out directive and a removed [keyfile]. Rebased onto current main and ff-merged as 63a3791; full gate green."
- criticism: ["[fable, R1, RESOLVED R2] install.md 'alongside NetworkManager' skip advice was factually wrong — restated to require the drop-in whenever NM is active.","[fable, R1, RESOLVED R2] packaging_test.go substring assertion passed on a commented-out directive — replaced with exact-line + [keyfile] checks, mutation-verified."]
- new_questions: []
- ledgerRefs: ["tasks:T110","goals:G6"]
- sessionLogs: [".cq/logs/20260714-034834-a38febff249b322f5.md",".cq/logs/20260714-034834-a8fe10de2e74c5ad8.md",".cq/logs/20260714-034834-a3b74d24cfbd0d5a7.md",".cq/logs/20260714-034834-a03fdf59bb32f668c.md"]
- rawLogs: [".cq/logs/raw/20260714-034834-a38febff249b322f5.jsonl",".cq/logs/raw/20260714-034834-a8fe10de2e74c5ad8.jsonl",".cq/logs/raw/20260714-034834-a3b74d24cfbd0d5a7.jsonl",".cq/logs/raw/20260714-034834-a03fdf59bb32f668c.jsonl"]

### R103 — go-ahead

- createdAt: 2026-07-14T04:07:07.864Z
- updatedAt: 2026-07-14T04:07:07.864Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T109 terminal reconciled panel verdict after 2 rounds (fresh re-implementation on correct base after a discarded stale-based attempt). R1 [opus] approve / [fable] disapprove (strictest-wins) on ONE criticism — reloadWarnings omitted tun_persist, so a SIGHUP flipping it was silently ignored, violating Reload's warn-on-every-ignored-change contract (which the task's 'must not break SIGHUP reload' clause covers). Mechanism was otherwise verified sound against amneziawg-go v1.0.4 source: NativeTun.Close never RTM_DELLINKs, CreateTUN re-adopts by name preserving ifindex; TUNSETPERSIST via SyscallConn().Control correct; unconditional apply clears on false; Close unchanged; e2e + docs sound. R2 (4175d1b) unanimous approve — reloadWarnings now emits a 'tun_persist X -> Y ... ignored until restart' warning (message follows the established convention) with a reload_test flip case; BOTH reviewers mutation-verified it FAILS ('got []') when the warning line is removed. 2 deferred defects filed at R1: D52 (reloadWarnings scheduler/fec/dns/bind gap, pre-existing) + a lint-at-base dup of D45. Rebased onto current main (clean 3-way merge into device.Up + install.md that T101/T110 also touched; full gate re-run green) and ff-merged as cf3f341."
- criticism: ["[fable, R1, RESOLVED R2] reloadWarnings omitted tun_persist — a SIGHUP flipping it was silently ignored; fixed with an explicit warning + a mutation-verified reload_test case."]
- new_questions: []
- ledgerRefs: ["tasks:T109","goals:G6","defects:D52"]
- sessionLogs: [".cq/logs/20260714-040623-a21407ed6e6df882a.md",".cq/logs/20260714-040623-a64c1d2ea3184af3e.md"]
- rawLogs: [".cq/logs/raw/20260714-040623-a21407ed6e6df882a.jsonl",".cq/logs/raw/20260714-040623-a64c1d2ea3184af3e.jsonl"]

### R107 — go-ahead

- createdAt: 2026-07-14T05:05:20.714Z
- updatedAt: 2026-07-14T05:05:20.714Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T111 unanimous panel approve (round 1): [opus]+[fable] both approve. Shipped packaging/systemd/wanbond-addressing@.service (templated oneshot, instance=role) + docs/install.md §4 C4 recipe + a CI-guarded internal/config/packaging_test.go shape test. BOTH reviewers independently RAN systemd-analyze verify (systemd 260) → exit 0 on a stub-path copy (the only finding on the verbatim unit is the absent operator-owned /etc/wanbond/addressing-%i.sh, covered by ConditionPathExists at runtime). The unit orders after INTERFACE EXISTENCE via a bounded ExecStartPre poll on /sys/class/net/wanbond0 (30s loop < TimeoutStartSec=45s, fails cleanly — no boot hang), not merely after execve, with the R27-race rationale documented. Templated syntax correct (%i, PartOf/After/WantedBy=wanbond-%i.service restart-coupling, Type=oneshot+RemainAfterExit). [fable] mutation-verified the packaging test non-vacuous (4/4 mutations kill it: drop ExecStartPre, drop After=, change poll path, reintroduce active ExecStartPost=). install.md C4 references the shipped file + carries the R27 race warning + the tun_persist cross-link. Full gate green. ff-merged as f3a59f8."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T111","goals:G6"]
- sessionLogs: [".cq/logs/20260714-050504-a680275f9573ffec1.md",".cq/logs/20260714-050504-af7c64d9b56c69fc6.md"]
- rawLogs: [".cq/logs/raw/20260714-050504-a680275f9573ffec1.jsonl",".cq/logs/raw/20260714-050504-af7c64d9b56c69fc6.jsonl"]

## M22

### R105 — go-ahead

- createdAt: 2026-07-14T04:24:10.337Z
- updatedAt: 2026-07-14T04:24:10.337Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T77 unanimous panel approve (round 1): [opus]+[fable] both approve. Test-only (test/e2e/dns_failover_test.go +459, TestDNSHubResolveAndReroute; + docs/design.md). The Q36 v1 DNS acceptance bar as a netns e2e: edge names the concentrator by hostname (dns opt-in, system resolver). BOTH reviewers verified against production source: all 3 grepped daemon log markers exist verbatim (boots deferred device.go:866; first endpoint resolution failover.go:302; active concentrator endpoint re-resolved failover.go:317). Hermeticity triple-sealed and verified: GODEBUG=netdns=go set on the EDGE DAEMON (the resolving process via SystemResolver), private bind-mounted resolv.conf in the confirmed mount namespace, in-netns dnsmessage responder as sole answer source, + a query-count growth probe; the unshared netns has no external route (defense in depth). All 5 phases staged: NXDOMAIN endpoint-less boot / R70 first-resolve install (not race-masked — responder NXDOMAIN before boot, pure-Go resolver has no cache, conc can't initiate) / mid-session hubA→hubB renumber with real concHubA flush / re-resolve repoint (updateResolution active-spec-changed branch, exactly one SetPeerRemote) / D32 rebaseline guard asserting real post-change iperf3 bytes over concHubB. Single-edge-path model correct for uniform SetPeerRemote. Compiles/vets under -tags e2e + full non-e2e gate green; docs synced. Privileged root -run DNS execution DEFERRED to the o3 + llm-ubuntu-0 hosts (G2 pattern; sandbox lacks /dev/net/tun). Rebased onto current main and ff-merged as 2afe674."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T77","goals:G5","defects:D54"]
- sessionLogs: [".cq/logs/20260714-042350-ab2788ec23f5df97d.md",".cq/logs/20260714-042350-a0e589c40f828c748.md"]
- rawLogs: [".cq/logs/raw/20260714-042350-ab2788ec23f5df97d.jsonl",".cq/logs/raw/20260714-042350-a0e589c40f828c748.jsonl"]

### R108 — go-ahead

- createdAt: 2026-07-14T05:20:16.174Z
- updatedAt: 2026-07-14T05:20:16.174Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T78 terminal reconciled panel verdict after 2 rounds: R1 [opus] approve / [fable] disapprove (strictest-wins) on ONE LOGIC-ERROR criticism — the valid_name RESOLUTION report read ONLY the R70 'first endpoint resolution' marker (fires only on the deferred-install path), but on the standing testbed the happy path resolves during the SILENT Q30 boot-resolve, so it would misreport 'did not resolve' alongside a successful handshake. R2 (521bbb6) unanimous approve — new resolveOutcome() checks the R70 marker first (authoritative), else infers resolution success from a COMPLETED HANDSHAKE (sound: an edge-initiated hostname-only peer's endpoint has only two sources, the Q30 seed or the R70 install, so a handshake requires the name resolved) reporting the standing cfg.ConcPubIP. BOTH R2 reviewers independently confirmed the boot-resolve happy path is genuinely unobservable over SSH (no success journal line, no endpoint metrics series, no UAPI/status subcommand — grep-verified), so handshake-inference is the correct report-only source. Report-only contract intact (bogus_name reports resolveOK=false and passes). Rebased onto current main and ff-merged as 66c1110; -tags realhosts vet/build + full gate green."
- criticism: ["[fable, R1, RESOLVED R2] valid_name RESOLUTION report misread the silent Q30 boot-resolve happy path as 'did not resolve' — fixed via resolveOutcome() sourcing from both the R70 marker and handshake inference."]
- new_questions: []
- ledgerRefs: ["tasks:T78","goals:G5"]
- sessionLogs: [".cq/logs/20260714-050548-a54ae7c80e1ab909f.md",".cq/logs/20260714-050548-a65d3da77adb4f5b4.md",".cq/logs/20260714-051954-a9e48eb98d4a79d0d.md",".cq/logs/20260714-051954-a01261db5c976ffab.md"]
- rawLogs: [".cq/logs/raw/20260714-050548-a54ae7c80e1ab909f.jsonl",".cq/logs/raw/20260714-050548-a65d3da77adb4f5b4.jsonl",".cq/logs/raw/20260714-051954-a9e48eb98d4a79d0d.jsonl",".cq/logs/raw/20260714-051954-a01261db5c976ffab.jsonl"]

### R111 — go-ahead

- createdAt: 2026-07-14T06:10:51.286Z
- updatedAt: 2026-07-14T06:10:51.286Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T76 unanimous panel approve (round 1): [opus]+[fable] both approve. Operationalizes the Q29/Q33 DPI-posture security target. (1) internal/device.TestUpAllLiteralTripwireNeverCallsLookup: a tripwireResolver whose Lookup calls t.Errorf the instant invoked, injected via the PRODUCTION up()/resolverFactory seam on an all-IP-literal config — BOTH reviewers independently mutation-verified it (opus: unconditional resolver construction trips the factory-count guard, forced literal Lookup trips the t.Errorf; fable: 3 mutation runs in a clean-HEAD scratchpad export incl. the async re-resolution goroutine under -race — the async path is TRIPLY gated so single-gate mutants are caught by the factory count). (2) A protocol-agnostic internal/wireaudit.CountPcapPackets helper (+ its own non-vacuous unit tests) + an e2e extension to test/e2e/p5_dpi_test.go: a concurrent tcpdump on 53/853/443 (TCP+UDP, both directions) over the DNS-off P5 session, assertZeroDNSEgress Fatalf-ing on any packet, capture starting pre-boot so the boot-resolve moment is in-window. Compiles/vets under -tags e2e; privileged P5 execution deferred (G2). Documented the per-mode leaked artifact (cleartext DNS query vs TLS SNI+timing). Full non-e2e gate + go vet -tags e2e green. Rebased onto current main and ff-merged as 9bd121a."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T76","goals:G5"]
- sessionLogs: [".cq/logs/20260714-061018-aabdf77e0c8e0f30f.md",".cq/logs/20260714-061018-a98cbbd2ae1d05ea6.md"]
- rawLogs: [".cq/logs/raw/20260714-061018-aabdf77e0c8e0f30f.jsonl",".cq/logs/raw/20260714-061018-a98cbbd2ae1d05ea6.jsonl"]

### R118 — go-ahead

- createdAt: 2026-07-14T07:20:26.312Z
- updatedAt: 2026-07-14T07:20:26.312Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T79 unanimous panel approve at ROUND 3 (3-round criticism loop): [opus]+[fable] both approve. G5 DNS/hostname-endpoint doc-sync across README.md, docs/design.md, docs/install.md, docs/runbook.md, wanbond.example.toml + a new config test (internal/config/config_test.go TestExampleConfigLoads) that READS the real wanbond.example.toml, extracts+uncomments each [dns] variant (doh/dot/system) and the hostname-peer example, and config.Load()s each (mutation-verified: injecting dot_server into the doh example fails dns_example_doh). Round 1 filed 7 criticisms (broken FULL-[dns] example that failed Load; test never read the real file; false 'dns=true requires [dns] block' in 3 files; wrong dedup/liveness/mixing prose; stale 'no DNS' text) — all fixed round 2. Round 2 filed 2 residual design.md false claims: (a) Mixing-rules rules 1-2 wrongly said the expansion is 'independent of the resolver's own record order' — orderAddrPorts (resolution.go:283-303) is a STABLE v4-before-v6 partition preserving within-family resolver encounter order (no sort), so byte-identical expansion requires a byte-identical answer; (b) boot-semantics 'repoints whenever the record changes' + Change-suppression understated updateResolution (failover.go:305-321) which is active-AddrPort-SURVIVAL-scoped (repoint only when the active AddrPort disappears from the spec's non-empty new expansion, re-point to addrs[0], one SetPeerRemote+rehandshake). Round 3 reworded both, changing ONLY docs/design.md; both reviewers verified verbatim against source. Rebased past T95/T96/T103/T97 and ff-merged as 167bed3."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T79","goals:G5"]
- sessionLogs: [".cq/logs/20260714-071939-a4cceb8d343f3d498.md",".cq/logs/20260714-071939-a193ca96634ef63ef.md"]
- rawLogs: [".cq/logs/raw/20260714-071939-a4cceb8d343f3d498.jsonl",".cq/logs/raw/20260714-071939-a193ca96634ef63ef.jsonl"]

## M26

### R109 — go-ahead

- createdAt: 2026-07-14T05:50:25.913Z
- updatedAt: 2026-07-14T05:50:25.913Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T93 terminal reconciled panel verdict after 2 rounds. R1 [opus] approve / [fable] disapprove (strictest-wins) with FOUR criticisms: (1) a REPRODUCED daemon panic — a deferred runtime AddPath on a 2-peer concentrator grew only the primary's probers while the new Open per-peer view loop indexes every peer's probers by the m.defs index, so the next Close→Open crashed with index-out-of-range at multipath.go:806; (2) DEVICE-level per-peer PSK keying was mutation-unproven (both wrong-PSK mutants at device.go:301/:306 survived the whole suite); (3) a new staticcheck QF1008; (4) an inaccurate BoundPeerNames doc comment. R2 (20ef56c) UNANIMOUS approve: worker (1) fanned the deferred-add prober record out to EVERY bound peer (per-peer index-aligned Down prober) + a fail-fast Open bounds guard, with a regression test; (2) added TestUpTwoPeerConcentratorKeysEachPeerOnItsOwnPSK proving each peer is keyed on its OWN psk on BOTH the prober AND reflector planes (via new PeerBootProbe/PeerReflect accessors); (3) fixed QF1008; (4) corrected the doc. BOTH R2 reviewers independently (isolated git-archive copies) reverted the fan-out to reproduce the exact panic then confirmed the fix, and independently killed both device PSK mutants on their respective planes. Single-peer uapiConfig BYTE-IDENTICAL (device.go untouched in R2); -race -count=2 bind+device green. Rebased onto current main (gate re-run green) and ff-merged as 55889b1. The re-filed pre-existing lint findings are D45 (line numbers shifted by T106's edits)."
- criticism: ["[fable, R1, RESOLVED R2] REPRODUCED panic — deferred AddPath on a 2-peer concentrator crashed on reopen (index-out-of-range at multipath.go:806); fixed by fanning the deferred prober to every peer (index-aligned) + a fail-fast Open guard + a regression test.","[fable, R1, RESOLVED R2] device-level per-peer PSK keying was mutation-unproven — added a device test that kills both device.go:301 (prober plane) and :306 (reflector plane) wrong-PSK mutants.","[fable, R1, RESOLVED R2] new staticcheck QF1008 at multipath.go:675 — fixed.","[fable, R1, RESOLVED R2] inaccurate BoundPeerNames doc comment — corrected to match the hardcoded empty primary name."]
- new_questions: []
- ledgerRefs: ["tasks:T93","goals:G4","defects:D45"]
- sessionLogs: [".cq/logs/20260714-054935-aeb82bf6766d4a909.md",".cq/logs/20260714-054935-a0b6f7290c160086e.md"]
- rawLogs: [".cq/logs/raw/20260714-054935-aeb82bf6766d4a909.jsonl",".cq/logs/raw/20260714-054935-a0b6f7290c160086e.jsonl"]

### R113 — go-ahead

- createdAt: 2026-07-14T06:21:24.075Z
- updatedAt: 2026-07-14T06:21:24.075Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T94 unanimous panel approve (round 1): [opus]+[fable] both approve. Added a conditionally-attached `peer` label (keyed on config peer name) to wanbond_path_*, wanbond_fec_*, and NEW wanbond_resequencer_* series, decided ONCE at NewCollector from Source.PeerNames(): 1 peer = OMIT the label (byte-compatible), 2+ = include. New bind.Multipath.PeerSnapshots() generalizes the primary-only PathSnapshots/FECSnapshot to per-peer path traffic + FEC + resequencer counters; the device adapter keys its throughput last-sample map by (peer,path). BOTH reviewers independently verified the SINGLE-PEER byte-compatibility as the load-bearing property: [opus] confirmed TestExpositionSinglePeerByteCompatible is a real raw-text assertion (no peer= leak + exact pre-T94 lines); [fable] additionally DIFFED the base-vs-HEAD exposition text with identical source data — every pre-existing series line byte-identical, only the additive resequencer families differ — and mutation-proofed it (forcing multiPeer=true fails the byte-compat test). Per-(peer,path) throughput rate isolation verified under same-named paths (both peers have a 'starlink' path; 800 vs 8000 bit/s, no clobber). decide-once sound (peer set frozen via AddConcentratorPeer before NewCollector; Reload keeps it static). reseq series map verbatim to reseq.Stats; back-compat rule documented in the package comment + runbook. Full gate + go vet -tags e2e green. Rebased onto current main (gate re-run green) and ff-merged as ed4b45c. [fable] filed low defect D56 (superseded PathSnapshots/FECSnapshot seams + duplicated FEC derivation)."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T94","goals:G4","defects:D56"]
- sessionLogs: [".cq/logs/20260714-062044-a7f4a8b610cf5aeda.md",".cq/logs/20260714-062044-a31be323657b7a7c5.md"]
- rawLogs: [".cq/logs/raw/20260714-062044-a7f4a8b610cf5aeda.jsonl",".cq/logs/raw/20260714-062044-a31be323657b7a7c5.jsonl"]

## M27

### R114 — go-ahead

- createdAt: 2026-07-14T06:39:32.009Z
- updatedAt: 2026-07-14T06:39:32.009Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T95 unanimous panel approve (round 1): [opus]+[fable] both approve. Test-only (internal/bind/per_peer_reseq_isolation_test.go, +120): TestPerPeerReseqIsolation binds two concentrator peers over one shared socket via the PRODUCTION demuxInbound/peerBySource path (authenticated PROBE → bindSourceToPeer → bound-source fast path → dispatchInbound → ps.peer.resequencer.Observe), then feeds each an out-of-order stream over the SAME overlapping numeric outer-seq (0..5) interleaved between peers, asserting each resequencer releases ONLY its own payloads in order with ZERO DroppedSuspect/DroppedOld/DroppedDup. The D32-class per-peer-isolation guarantee at unit level. BOTH reviewers independently mutation-verified in isolated copies (forcing both peers onto ONE shared resequencer in ensurePeerReceiveInstantiated → 'B peer released 0 frames' — A swallows B's overlapping seqs); confirmed the overlapping-seq design is the discriminator and the production binding path (not hand-wired). -race green (count=1 and count=10, no flake). No production defect. ff-merged as b38581f."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T95","goals:G4"]
- sessionLogs: [".cq/logs/20260714-063906-af7c84761e871fbd1.md",".cq/logs/20260714-063906-aaafa72796608ad29.md"]
- rawLogs: [".cq/logs/raw/20260714-063906-af7c84761e871fbd1.jsonl",".cq/logs/raw/20260714-063906-aaafa72796608ad29.jsonl"]

### R115 — go-ahead

- createdAt: 2026-07-14T06:40:33.128Z
- updatedAt: 2026-07-14T06:40:33.128Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T96 unanimous panel approve (round 1): [opus]+[fable] both approve. Test-only (appended TestConcentratorFECParityNeverCrossesPeers to internal/bind/peer_fec_lifecycle_test.go). Confirms the T91/T93 per-peer fecSend/fecRecv split preserves the Reed-Solomon invariants: TestKlauspostParityPrefixStableInvariant + the FEC datapath suite (go test ./internal/... -run FEC) pass UNCHANGED (no masked regression — test-only diff). The new cross-peer FEC-isolation test uses a GENUINE group-id-0 collision (both fresh per-peer fec.Encoders open group 0 since nextGroup is zero-valued) and asserts BOTH directions + payload-level isolation: peer A's parity recovers exactly 1 frame into A's OWN decoder/resequencer, B's Recovered stays 0 and B's group-0 stays undisturbed until B's own parity arrives (+ a reciprocal check). BOTH reviewers independently mutation-verified in isolated copies: routing the frame.Parity case to the primary and cross-feeding to the other peer BOTH redden the test. -race + full internal suite green. ff-merged as 010b7ec."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T96","goals:G4"]
- sessionLogs: [".cq/logs/20260714-064007-a0d31254057d2a3a1.md",".cq/logs/20260714-064007-a278df2d0fc736c97.md"]
- rawLogs: [".cq/logs/raw/20260714-064007-a0d31254057d2a3a1.jsonl",".cq/logs/raw/20260714-064007-a278df2d0fc736c97.jsonl"]

### R117 — go-ahead

- createdAt: 2026-07-14T06:55:13.693Z
- updatedAt: 2026-07-14T06:55:13.693Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T97 unanimous panel approve (round 1): [opus]+[fable] both approve. New netns e2e (test/e2e/multipeer_test.go, +777, TestMultiPeerConcentratorIsolation): one concentrator (2 uplinks, 2 peers, /metrics 9102) + two edges each in their own holder netns bonded across two netem-shaped uplinks over a two-bridge fabric. Stages ALL acceptance scenarios: independent per-edge inner streams (concurrent bulk TCP, both positive); edge-A kill+restart with an edge-B transfer SPANNING the outage (a reset fails the assertion, so a positive result proves non-interruption) + edge-B per-peer path_up; per-peer /metrics attribution; edge-A NAT-rebind recovery forced onto the rebound uplink (mirrors the T16 roaming proof) with B undisturbed; a spoofed unbound-source flood (real unenrolled source hitting the demux via net.DialUDP) asserting no live-peer eviction (ping + wanbond_path_up==1 for both peers, not just no-crash). BOTH reviewers verified the peer-label attribution against source (edge A → peer=\"\" since device.go binds ids[0] via NewMultipath unnamed + BoundPeerNames forces primary name=\"\"; edge B → peer=\"edge-beta\" via AddConcentratorPeer) — [fable] cross-checked 3 independent unit-level proofs (TestExpositionTwoPeerSeries empty-label scrape round-trip + concentrator_peer_test BoundPeerNames==[\"\",\"beta\"]). Isolation properties genuinely Fatalf; absolute counters report-only (M10/Q12). Port 9102 unique; netns.go/thresholds.go untouched. Compiles/vets under -tags e2e + full gate green; privileged execution DEFERRED to the o3 + llm-ubuntu-0 hosts (G2 pattern). Rebased onto current main and ff-merged as 4b912e5. [fable]'s 9096 pacing/p3 collision defect is a DUPLICATE of D51."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T97","goals:G4","defects:D51"]
- sessionLogs: [".cq/logs/20260714-065448-a43bd9a8d80ec37ce.md",".cq/logs/20260714-065448-a377c32726def949b.md"]
- rawLogs: [".cq/logs/raw/20260714-065448-a43bd9a8d80ec37ce.jsonl",".cq/logs/raw/20260714-065448-a377c32726def949b.jsonl"]

### R119 — go-ahead

- createdAt: 2026-07-14T07:23:03.617Z
- updatedAt: 2026-07-14T07:23:03.617Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T99 unanimous panel approve (round 1): [opus]+[fable] both approve. Verification + report-only-capture task. Mandatory half: `go test ./...` green (exit 0, all packages). Second half taken as a DOCUMENTED DEFERRAL (per M10/Q12 report-only discipline), adding only docs/drafts/20260714-0705-t99-2edge-realhosts-deferral.md (no code). Both reviewers independently GROUNDED the deferral against source and (fable) via read-only SSH to the live hosts: test/realhosts/runner.go Config carries exactly one Edge + one Conc (env WANBOND_EDGE_HOST/WANBOND_CONC_HOST, no second-edge notion); setupEdgeTwoPaths (multipath_failover_test.go) is a single-NIC secondary-source-IP fake-UPLINK trick, degenerate if repurposed for a second PEER; T97's TestMultiPeerConcentratorIsolation uses THREE independent vantage points (2 edge netns + base). The standing 2-host inventory (o3.7mind.io: single NIC enp0s6 + a LIVE standing concentrator PID 73612; llm-ubuntu-0: single NIC enp1s0) exposes only two vantage points, so a genuine 2-edge+concentrator isolation capture is infeasible without a degenerate single-NIC 2-edge run (observes multiplexing, not isolation — fails the 'per-peer isolation observed' bar even report-only) or mutating o3's live shared concentrator config. Required inventory correctly identified: a third independently-networked edge host (WANBOND_EDGE2_HOST, matching the existing env-var pattern). Deferral judged HONEST, not a dodge. Rebased onto current main and ff-merged as 6e41f4a."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T99","goals:G4"]
- sessionLogs: [".cq/logs/20260714-070917-abf23ed9fff393fef.md",".cq/logs/20260714-071939-a959ee680c6a5baab.md",".cq/logs/20260714-071939-a9dd699b4c0bc00c8.md"]
- rawLogs: [".cq/logs/raw/20260714-070917-abf23ed9fff393fef.jsonl",".cq/logs/raw/20260714-071939-a959ee680c6a5baab.jsonl",".cq/logs/raw/20260714-071939-a9dd699b4c0bc00c8.jsonl"]

### R121 — go-ahead

- createdAt: 2026-07-14T07:54:07.227Z
- updatedAt: 2026-07-14T07:54:07.227Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T98 unanimous panel approve at ROUND 3 (3-round criticism loop): [opus]+[fable] both approve. Docs-sync for the shipped G4 multi-peer concentrator across AGENTS.md, README.md, docs/design.md, docs/install.md, wanbond.example.toml + an extended config test. Round 1 (both disapprove): the multi-peer example was commented-out prose exercised by NO test (acceptance requires 'parses via the config test suite'), plus 4 doc-vs-source contradictions. Round 2: re-applied on current main (post T79) fixing all 4 — (a) single-peer per-peer psk is REJECTED at load not 'optional/defaults to top-level' (config.go:1132); (b) the top-level psk authenticates NO peer on a multi-peer concentrator though still required by validate (device.go feeds only per-peer PSKs); (c) only ADDITIONAL peers carry a named `peer` metrics label, the primary is peer=\"\" (BoundPeerNames; discrepancy filed D58); (d) 'virtual endpoint' terminology reserved for A1 — and EXTENDED TestExampleConfigLoads with an exampleMultiPeerConcentrator extractor + multi_peer_concentrator subtest that config.Load()s the real commented stanza with two distinct per-peer PSKs/names (mutation-verified). Round 2 approve/disapprove split caught a NEW decisive error: the added design.md multi-peer DATA/PARITY sub-bullet INVERTED the DoS condition. Round 3 corrected it against demuxInbound (an UNBOUND forged source is trial-decoded O(peers) and dropped pre-dispatch — the HMAC in frame.go makes a forged PROBE impossible; only a source spoofing a currently-BOUND peer reaches that peer's resequencer/FEC, dropped by the inner WG AEAD), plus scoped the README per-peer-PSK claim to 2+ edges and redirected a dangling example.toml [metrics] pointer to the runbook G4 note. Both reviewers re-read demuxInbound/Codec.Decode and confirmed the security prose is correct in both directions. Rebased onto current main (over T79/T99/T108); resolved one wanbond.example.toml conflict (kept T108's route-installing mode= description + T98's psk/name comment blocks), re-ran the gate (TestExampleConfigLoads green), ff-merged as d960979. The config.go stale Peer.PSK 'not yet consumed' comment is filed D57."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T98","goals:G4","defects:D57","defects:D58"]
- sessionLogs: [".cq/logs/20260714-075343-a64dedbc8a4d965d1.md",".cq/logs/20260714-075343-a4e53784a3d3e81cb.md"]
- rawLogs: [".cq/logs/raw/20260714-075343-a64dedbc8a4d965d1.jsonl",".cq/logs/raw/20260714-075343-a4e53784a3d3e81cb.jsonl"]

## M33

### R122 — go-ahead

- createdAt: 2026-07-14T08:18:02.473Z
- updatedAt: 2026-07-14T08:18:02.473Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T112 unanimous panel approve at ROUND 2 ([opus]+[fable] both approve). Docs (install.md §3b): the D38 source_addr/device-bind collision — per-WAN `ip rule from <source>` pinning silently defeated by the auto device-bind on one-address (VLAN-per-WAN) interfaces. Round 1 (both disapprove) caught a DECISIVE self-defeating recipe: Workaround 1 shipped `ip rule add from <source_ip> oif <dev> table N`, but fib-rule selectors AND together and a wildcard-source SO_BINDTODEVICE socket presents an unset source at route-lookup — so the `from` selector can never match (exactly the section's own root cause), leaving the outage unresolved; the D38 production recipe is OIF-ONLY. Round 2 fixed all 5 findings: (1) oif-only `ip rule add oif <dev> table <N> prio 100` (matches the D38 ledger record); (2) replaced a false 'persistent across policy-route changes' claim with an accurate not-reboot-persistent caveat pointing at the §4 wanbond-addressing@.service oneshot + §5 netfilter-persistent; (3) relocated §3b between §3a and §3z (monotonic order); (4) completed the 'auto' bind-mode description with selectDeviceBinds' third condition (exactly ONE configured path per device — devPaths[dev]==1, verified in internal/bind/pathsock.go); (5) documented the top-level `bind` default (config.go) as the simpler VLAN-per-WAN recommendation. Both reviewers re-verified the oif-only rule (oif set from SO_BINDTODEVICE matches at lookup) + the bind field shape against source. Full gate green. Rebased onto current main and ff-merged as e790a3c. The stale config.go BindMode 'config surface only' comment is filed D60."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T112","goals:G6","defects:D60"]
- sessionLogs: [".cq/logs/20260714-081736-a440fa701129dfa8e.md",".cq/logs/20260714-081736-a2b12f72790f5bc08.md"]
- rawLogs: [".cq/logs/raw/20260714-081736-a440fa701129dfa8e.jsonl",".cq/logs/raw/20260714-081736-a2b12f72790f5bc08.jsonl"]

### R123 — go-ahead

- createdAt: 2026-07-14T08:18:45.455Z
- updatedAt: 2026-07-14T08:18:45.455Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T114 unanimous panel approve at ROUND 2 ([opus]+[fable] both approve). Docs (install.md §6a): interim restart/reconverge guidance until D36 is fixed, using the T101 session metric. Round 1 (both disapprove) caught a FABRICATED timing figure: the section stated the session-validity/freshness window and the one-sided-restart convergence delay as '~2.5 hours' — ~50x too large. Round 2 fixed all 4 findings, each grounded against source: (1) both '~2.5 hours' → ~180s/~3min (RejectAfterTime = time.Second*180, amneziawg-go device/constants.go:22, consumed verbatim as the session expiry in internal/device/session.go:61); (2) the one-sided-restart outage rescaled to minutes-scale bounded by RekeyAfterTime(120s)/RejectAfterTime(180s), matching D36's 'down for MINUTES until a rekey timer fires'; (3) the 'Avoid' paragraph reframed — D36 is the INNER-WG whole-tunnel session outage, distinct from the already-resolved OUTER per-path liveness deadlock (D12); (4) added a converging-vs-wedged operational discriminator (gauge 1 within ~25s of a coordinated both-end restart; persistent 0 beyond ~25s/~3min = wedge) + a stale-end caveat (the non-restarted end's wanbond_session_established can read 1 for up to ~180s post-peer-restart despite no live traffic, since Established = age<=RejectAfterTime per session.go:84). Metric name wanbond_session_established (metrics.go:102) + log line 'session established' (session.go:166) kept verbatim; interim-until-D36 marking intact. Full gate green. Rebased onto current main and ff-merged as c71d26a."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T114","goals:G6","defects:D36"]
- sessionLogs: [".cq/logs/20260714-081736-a61c19bcaa76a8b17.md",".cq/logs/20260714-081736-a0bb9d3d0e0db7a83.md"]
- rawLogs: [".cq/logs/raw/20260714-081736-a61c19bcaa76a8b17.jsonl",".cq/logs/raw/20260714-081736-a0bb9d3d0e0db7a83.jsonl"]

### R124 — go-ahead

- createdAt: 2026-07-14T08:27:20.090Z
- updatedAt: 2026-07-14T08:27:20.090Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T113 unanimous panel approve at ROUND 3 (3-round criticism loop): [opus]+[fable] both approve. Docs (install.md §9 full-tunnel/client-LAN recipe + §5 C6 concentrator NAT/forwarding checklist) — THE primary G6 use case. The SNAT PRIMARY recipe was sound from round 1 and stayed untouched. The criticism loop hardened the surrounding material: R1 (both disapprove) — the §9 intro bold 'never write literal 0.0.0.0/0' contradicted §9.1's own TOML + §3 (the daemon ALWAYS splits a config-literal /0 to /1+/1 at UAPI render via splitDefaultRoute, so it is safe; the engine-boundary is the real invariant, D35 the engine defect), and the §9.2 widen-allowed_ips ALTERNATIVE broke the C6 MASQUERADE (un-SNAT'd client subnet not in <tunnel-net>). R2 fixed the intro + MASQUERADE-widening note + a misdirected SNAT-address pointer, but [fable]'s deeper end-to-end trace caught that the widen branch was STILL non-functional: the de-NATed RETURN packet to the client subnet had no concentrator kernel route toward wanbond0 (the daemon programs routes only for mode=default-route, rejected on the concentrator role; WireGuard allowed_ips is cryptokey routing only, no kernel route). R3 completed the widen branch with the operator-owned `ip route add <client-subnet> dev wanbond0` on the concentrator (persisted via wanbond-addressing@concentrator.service, §9.4 pattern) + corrected the failure-direction wording (client-OUTBOUND leg, not replies) + qualified the SNAT-pointer parenthetical for the /0-vs-/32 cases. Both reviewers re-traced the full data path (outbound + return) across all three widen-branch steps and confirmed it now closes; Q41 operator-owned boundary held; all code citations verified (config.go:1122 concentrator mode rejection, device.go defaultRoutePrefixes/installRoutes edge-only). Full gate green. Rebased over T112/T114 and ff-merged as 1a8c570."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T113","goals:G6","defects:D35"]
- sessionLogs: [".cq/logs/20260714-082657-a554e035011abf6ad.md",".cq/logs/20260714-082657-afee22f11f48ff9b8.md"]
- rawLogs: [".cq/logs/raw/20260714-082657-a554e035011abf6ad.jsonl",".cq/logs/raw/20260714-082657-afee22f11f48ff9b8.jsonl"]

### R125 — go-ahead

- createdAt: 2026-07-14T08:43:46.175Z
- updatedAt: 2026-07-14T08:43:46.175Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T115 unanimous panel approve (round 1): [opus]+[fable] both approve. The FINAL G6 doc-sync sweep (docs/design.md, docs/install.md, docs/runbook.md, wanbond.example.toml). The worker correctly inventoried what T101/T105-T114 already shipped and filled ONLY the genuine gaps: (1) the `bind` key (top-level + per-path, values source/device/auto, default auto) was absent from install.md §3z's toml block + wanbond.example.toml despite §3b prose — added; (2) docs/design.md had ZERO mentions of bind/BindMode, tun_persist/TUNSETPERSIST, mode=default-route/splitDefaultRoute, or wanbond_session_established (no G6 task had touched design.md) — added a new section covering tun_persist, the default-route wiring as the ONE deliberate exception to 'the daemon never assigns routes', and the session signal; (3) docs/runbook.md gained pointers to the C1 NetworkManager unmanaged-devices drop-in, the C4 addressing/persistence oneshot, and the C3 §9 full-tunnel recipe. Both reviewers GROUNDED every claim verbatim against source (config.go bind shape; persist_linux.go:31-47 TUNSETPERSIST called from device.Up:234; device.go:1071 splitDefaultRoute shared by route-install + uapiConfig; config.go:1122 concentrator mode rejection; metrics.go:102 wanbond_session_established byte-for-byte; session.go sessionMonitor/RejectAfterTime/one-INFO-per-0→-1-edge) AND independently re-derived all three hand-authored design.md GitHub anchor slugs, confirming each resolves to a real install.md heading; confirmed both surviving 'never assigns routes' statements (design.md:448, install.md:915) are inline-qualified with the default-route exception (no unqualified survivor in docs/ or README); cited packaging artifacts exist. README needed no change (its feature list doesn't enumerate keys/metrics). Full gate + TestExampleConfigLoads green. Base = current main; ff-merged as f2e3fc0. [fable]'s stale-config.go-bind-comment defect extends the already-filed D60."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T115","goals:G6","defects:D60"]
- sessionLogs: [".cq/logs/20260714-084317-a1960e942fe11ca6d.md",".cq/logs/20260714-084317-af168e7ecc9a2339a.md"]
- rawLogs: [".cq/logs/raw/20260714-084317-a1960e942fe11ca6d.jsonl",".cq/logs/raw/20260714-084317-af168e7ecc9a2339a.jsonl"]

## M34

### R126 — revise

- createdAt: 2026-07-14T09:32:37.660Z
- updatedAt: 2026-07-14T09:32:37.660Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "G7 plan review round 1 — RECONCILED verdict REVISE (strictest-wins): [opus] go-ahead, [fable] revise (2 criticisms). Both verified the root-cause→fix mapping sound + grounded (T119 reflect-site is the surviving peer's reflector; one dispatchInbound seam covers edge+concentrator + both directions; T38 adoption authenticated; restart-vs-bootstrap gate correct; deviceRehandshake backdating defeats RekeyTimeout; DAG acyclic, units before deferred e2e). [fable]'s 2 revise findings folded into T116+T119 (below)."
- criticism: ["[fable] T116/T119 API-form divergence: T116 offered callback OR return-flag; T119's wiring/lock-discipline only cohere with the RETURN-FLAG form. FIX: pin the return-flag form in T116 (Reflect returns (echo, epochChanged, err)); T119 consumes it. [ADDRESSED in the revised T116/T119.]","[fable] Post-Rebaseline re-pin RACE: Rebaseline clears `started` + trusts the NEXT frame; under the D36 saturation precondition a stale OLD-boot high-seq straggler can land between the Rebaseline and the wrapped low-seq init, re-pinning next high, and the once-per-epoch dedup then blocks recovery → silent degrade to the slow tryResync path. FIX: a LOW-ANCHOR-ONLY rebaseline variant (re-anchor only on a frame far below the pre-rebaseline release point; stale-high stragglers stay SUSPECT-dropped until the low init) + a stale-high-between regression test. [ADDRESSED in the revised T119.]"]
- new_questions: []
- ledgerRefs: ["goals:G7"]
- sessionLogs: [".cq/logs/20260714-093144-a63bb8cfc747aa8c7.md",".cq/logs/20260714-093144-a4220290e07861420.md"]
- rawLogs: [".cq/logs/raw/20260714-093144-a63bb8cfc747aa8c7.jsonl",".cq/logs/raw/20260714-093144-a4220290e07861420.jsonl"]

### R127 — go-ahead

- createdAt: 2026-07-14T09:38:23.713Z
- updatedAt: 2026-07-14T09:38:23.713Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "G7 plan review round 2 — UNANIMOUS GO-AHEAD ([opus]+[fable]). Both R126 revise criticisms verified resolved against source: (1) T116 pins the return-flag Reflect form (echo, epochChanged, err) with no callback — lock-coherent because acceptLocked releases r.mu before Reflect returns, so T119 consumes the flag lock-free at multipath.go:1690 (same atomic-Load+nil-check as the DATA branch), and dependsOn:T116 prevents an incompatible build; (2) T119's low-anchor rebaseline variant closes the post-Rebaseline re-pin race by reusing the resequencer's own one-window SUSPECT boundary (a stale old-boot high-seq straggler fails the 'more than one window below the pre-rebaseline release point' predicate and stays SUSPECT-dropped, while the restarted stream's seq~1 init trivially anchors), with the stale-high-between regression case pinned in acceptance and the D32 hub-failover path explicitly preserved. DAG acyclic; T116 signature change staged compile-green; residual >1-window deep-straggler case bounded (degrades only to the pre-existing tryResync fallback). Plan APPROVED — G7 locked to planned."
- criticism: []
- new_questions: []
- ledgerRefs: ["goals:G7"]
- sessionLogs: [".cq/logs/20260714-093902-a58e381892eb74d71.md",".cq/logs/20260714-093902-ac82af35219680402.md"]
- rawLogs: [".cq/logs/raw/20260714-093902-a58e381892eb74d71.jsonl",".cq/logs/raw/20260714-093902-ac82af35219680402.jsonl"]

## M35

### R128 — revise

- createdAt: 2026-07-14T09:53:07.303Z
- updatedAt: 2026-07-14T09:53:07.303Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "G8 plan review round 1 — RECONCILED REVISE (strictest-wins): [opus] go-ahead, [fable] revise (3 criticisms). Both verified every grounding claim + the DAG (acyclic, multipath.go serialized). [fable]'s 3 planner-fixable findings folded into T123/T126/T127 (below)."
- criticism: ["[fable] T126 (D50): EDGE-triggered detection (Established 1→0) MISSES the exact D50 leak — heavy state instantiates on the first authenticated PROBE (demuxInbound→ensurePeerReceiveInstantiated, multipath.go:1425-1435), NOT on WG handshake, so a valid-psk peer that never completes a handshake has last_handshake=0 forever, no 1→0 edge ever fires, and its ring/FEC/demux state leaks permanently. FIX: LEVEL-triggered detection (per poll: not-established-now ⇒ TearDownPeer(name); TearDownPeer is safe to call repeatedly — refuses live/primary, no-ops on absent) + add the never-handshaked-peer reclaim case to acceptance. [ADDRESSED in revised T126.]","[fable] T123 (D47+D49 interaction): the AddrPort re-key introduces a NEW failure mode — same-peer CGNAT port churn accumulates stale bindings counting against that peer's OWN quota with no unbind path short of teardown, and TearDownPeer refuses LIVE peers, so a live legitimately-roaming peer that churns past its quota drops its own re-bind PROBE forever. FIX: pin the decision — a same-peer bind AT quota re-points/evicts that peer's OWN oldest binding (preserving never-evict-live wrt OTHER peers + full cross-peer isolation) — and add a same-peer-port-churn-past-quota case to acceptance (currently only the cross-peer insider flood). [ADDRESSED in revised T123.]","[fable] T127 (D58): the fix changes user-visible documented metrics-label semantics (T98 shipped docs pinning primary peer=\"\"; T97 e2e pins it), but T127 updates only tests — per AGENTS.md same-change doc-sync, add updating the T98-touched docs (README/docs/design.md metrics-label description) to T127's scope + acceptance. [ADDRESSED in revised T127.]"]
- new_questions: []
- ledgerRefs: ["goals:G8"]
- sessionLogs: [".cq/logs/20260714-094948-adc9642f29210ebd5.md",".cq/logs/20260714-094948-a29368d245dc044ba.md"]
- rawLogs: [".cq/logs/raw/20260714-094948-adc9642f29210ebd5.jsonl",".cq/logs/raw/20260714-094948-a29368d245dc044ba.jsonl"]

### R129 — go-ahead

- createdAt: 2026-07-14T09:56:26.017Z
- updatedAt: 2026-07-14T09:56:26.017Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "G8 plan review round 2 — UNANIMOUS GO-AHEAD ([opus]+[fable]). All three R128 revise criticisms verified resolved against source: (1) T123 pins same-peer-own-oldest LRU eviction — a live roaming peer at quota evicts ITS OWN oldest binding (never another peer's), closing the self-starvation the AddrPort re-key introduced while preserving cross-peer drop-on-exhaustion + never-evict-live; implementable in the existing copy-on-write CAS loop (bindSourceToPeer :1469-1501). (2) T126 pins LEVEL-triggered per-poll detection + idempotent TearDownPeer, closing the never-handshaked-but-PROBE-instantiated leak (heavy state instantiates at demuxInbound→ensurePeerReceiveInstantiated :1425-1435, not on WG handshake, so an edge detector never fires; TearDownPeer refuses live/primary by identity :1563-1568, so repeated level calls are safe). (3) T127 adds the AGENTS.md doc-sync (docs/design.md + README.md + docs/runbook.md metrics-label + the T97 e2e). DAG acyclic; multipath.go serialization (T123→T124→T125→T127) intact; nothing regressed. Plan APPROVED — G8 locked to planned."
- criticism: []
- new_questions: []
- ledgerRefs: ["goals:G8"]
- sessionLogs: [".cq/logs/20260714-095536-a3a7f395cb395bd56.md",".cq/logs/20260714-095536-a5a35759bbc77ff8a.md"]
- rawLogs: [".cq/logs/raw/20260714-095536-a3a7f395cb395bd56.jsonl",".cq/logs/raw/20260714-095536-a5a35759bbc77ff8a.jsonl"]

## M36

### R130 — go-ahead

- createdAt: 2026-07-14T10:06:16.839Z
- updatedAt: 2026-07-14T10:06:16.839Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "G9 plan review round 1 — UNANIMOUS GO-AHEAD ([opus]+[fable]). Config loader/validation hardening DAG (M45 / T130→T131→T132 serial chain) verified fine-grained, correctly sequenced, testable, and fully grounded: T130 (D41) strict DisallowUnknownFields decode is safe against the toml:\"-\" derived fields (StrictMissingError per go-toml/v2 contract); T131 (D43) the LinkRTTRaw raw-string precedent + string→Duration gap verified, and CollapseDwell/LoadTau/WeightRTTFloor/FEC.Deadline are the COMPLETE remaining bare-Duration knob set ([dns]/link_rtt already use the pattern), with the mandated wanbond.example.toml/docs sync; T132 (D55+D59) netip.ParsePrefix + /0-exclusivity land at the correct validate() locus — the multi-peer-default-route shapes are Load-unreachable (edge one-peer cap + concentrator mode rejection) but the reachable single-peer-dup-/0 AND two-concentrator-bare-/0 cases are covered, and the guarded shapes are unit-tested via direct validate(). Serial chain justified (all touch config.go; T131's field re-keying must pass under T130's strict decoder). Plan APPROVED — G9 locked to planned."
- criticism: []
- new_questions: []
- ledgerRefs: ["goals:G9"]
- sessionLogs: [".cq/logs/20260714-100557-a19544394bc175b00.md",".cq/logs/20260714-100557-a35b26d6acaee7e2d.md"]
- rawLogs: [".cq/logs/raw/20260714-100557-a19544394bc175b00.jsonl",".cq/logs/raw/20260714-100557-a35b26d6acaee7e2d.jsonl"]

## M37

### R131 — revise

- createdAt: 2026-07-14T10:14:55.933Z
- updatedAt: 2026-07-14T10:14:55.933Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "G10 plan review round 1 — RECONCILED REVISE (both [opus]+[fable] revise). Both verified the D48/D53 fix sites are complete + hazard-free (only 2 uncounted tx writes, atomic counters; 9 NewMultipath callers; both fallback layers + CAP covered) and T133→T134 serialization is justified. Reconciled union of 3 planner-fixable findings, folded into T133/T135:"
- criticism: ["[opus+fable] T135 Bind either/or is a FALSE DICHOTOMY: config.Config has Bind at BOTH levels — top-level c.Bind global default (normalize, config.go:841-843) AND per-path Path.Bind with fallback (:847-849). FIX both/and: extend the same-name-path comparison with l.Bind != d.Bind (normalize resolves the default into every path, covering effective changes) AND explicitly handle top-level c.Bind (own DeepEqual case, OR deliberately zero it in the catch-all as covered-by-per-path) — else a top-level bind change fires only the generic catch-all instead of an actionable per-section warning. [ADDRESSED in revised T135.]","[opus] SEQUENCING: T134 (9 NewMultipath call sites in device.go) and T135 (reloadWarnings in device.go) BOTH edit internal/device/device.go, but T135 is parallel while T133→T134 were serialized on the same-file rationale. Apply consistently: T135 dependsOn T134. [ADDRESSED: T135 now dependsOn T134.]","[fable] T133 gaps: (a) the peerPathState txBytes counter-contract comment (multipath.go:157-167) states txBytes 'counts DATA-frame wire bytes on the Send hot path' + 'the backup path's Send count stays ~flat' — both FALSE once probe/echo bytes count; extend item (3) to update that comment. (b) T133 is the ONLY task without the AGENTS.md docs-sync clause despite changing an operator-visible metric — add: check/update README.md + docs/design.md wherever wanbond_path_tx_bytes_total / the idle-standby-tx symptom is documented. (c) Clarify 'flip the T104 subtest' = update its stale repro COMMENTARY (file doc-comment predicting failure + the refile-as-defect note); the subtest already asserts delta>0, no assertion logic inverts. [ADDRESSED in revised T133.]"]
- new_questions: []
- ledgerRefs: ["goals:G10"]
- sessionLogs: [".cq/logs/20260714-101133-a0a8f038fe804a40b.md",".cq/logs/20260714-101133-afc7473f1aa34cb37.md"]
- rawLogs: [".cq/logs/raw/20260714-101133-a0a8f038fe804a40b.jsonl",".cq/logs/raw/20260714-101133-afc7473f1aa34cb37.jsonl"]

### R133 — go-ahead

- createdAt: 2026-07-14T10:24:50.751Z
- updatedAt: 2026-07-14T10:24:50.751Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "G10 plan review round 2 — RECONCILED GO-AHEAD (unanimous: both [opus]+[fable] go-ahead, 0 criticisms). Both reviewers independently verified all THREE R131 findings are resolved and source-grounded, with no new problem introduced by the revision: (1) T135 Bind both/and — config.go normalize confirms Bind at BOTH levels (top-level default c.Bind→BindModeAuto :841-843, per-path Path.Bind fallback to c.Bind :847-849); the revised T135 correctly directs the per-path l.Bind!=d.Bind extension of the same-name-path comparison (device.go:589, SourceAddr/DestAddr-only today) PLUS explicit top-level c.Bind handling (own case or deliberate catch-all zeroing to avoid a generic double-warning), and correctly leaves Metrics un-warned (applied at Reload). (2) T135 dependsOn:[T134] is present — codegraph confirms NewMultipath (multipath.go:549) has exactly 9 callers in device.go and reloadWarnings lives in the same file, so the same-file serialization is sound and consistent across the linear T133→T134→T135 chain (every same-file overlap serialized: multipath.go T133+T134, device.go T134+T135). (3) T133 — the peerPathState txBytes comment at multipath.go:157-167 verbatim claims DATA-only/Send-only + 'backup Send count stays ~flat' (both false post-fix); revised item (3) rewrites it, item (4) adds the AGENTS.md docs-sync clause, item (5) correctly reframes the T104 'flip' as stale-repro-commentary-only (standby_liveness_test.go already asserts delta>0 via t.Errorf, no assertion inverts; emitProbes probe.go:50 counts on nil-error only; help string metrics.go:273 already accurate). Both reviewers additionally confirmed control frames have NO production egress site in internal/bind, so probe emission + echo reflection remain the only two uncounted tx writes (the round-1 completeness claim holds). Rubric clean across fine-grained/sequenced/testable/grounded/complete. No revision required."
- criticism: []
- new_questions: []
- ledgerRefs: ["goals:G10"]
- sessionLogs: [".cq/logs/20260714-102427-a1a2e5f635c8365c4.md",".cq/logs/20260714-102427-ae7b4d2f874ed1050.md"]
- rawLogs: [".cq/logs/raw/20260714-102427-a1a2e5f635c8365c4.jsonl",".cq/logs/raw/20260714-102427-ae7b4d2f874ed1050.jsonl"]

## M38

### R132 — go-ahead

- createdAt: 2026-07-14T10:21:03.533Z
- updatedAt: 2026-07-14T10:21:03.533Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "G11 plan review round 1 — RECONCILED GO-AHEAD (unanimous: both [opus]+[fable] go-ahead, 0 criticisms). Both reviewers independently ground-verified all 7 defect fixes against the working tree across the fine-grained/sequenced/testable/grounded/complete axes: T136 (.golangci.yml is v2 — the linters.exclusions.paths + formatters.exclusions.paths pivot from D54's stale v1 run.skip-dirs is correct, and the formatters half is load-bearing for the gofmt worktree-walk leak; doh.go:206 + dot.go:168 are genuine errcheck violations; the QF1001 site is now bind/pathsock.go:242 not the filed :166, which T136 anticipates by relocating via a lint run); T137 (9096 collision confirmed — pacing_test.go:75 + p3_fec_test.go:47, all other e2e ports unique across 9095-9102); T138 (all 4 config.go stale comments verbatim — BindMode :78-81, Path.Bind :492-493, PSK :577-578, Name :583-584 — with D60→delete / D57→replace correctly assigned and the claimed real consumers verified); T139 (zero external callers of Multipath.PathSnapshots/FECSnapshot — 3 traffic_test + 6 fec_test, none outside internal/bind; the metrics.FECSnapshot struct is a distinct type the grep's paren disambiguates); T140 (both shipped systemd units grant CAP_NET_ADMIN-only, so the empirical CAP_NET_ADMIN probe reproduces deployment reality, and the ≥5.7 unprivileged-SO_BINDTODEVICE floor + WebSearch verification is rigorous; don't-widen-unless-proven is the correct conservative posture). Sequencing: all-dependsOn-T136 is load-bearing (T138/T139 acceptances require `just lint` green, impossible before T136 lands on a RED base), a two-wave DAG with four file-disjoint parallel leaves. Completeness: all 7 defects map 1:1 to tasks with correct ledgerRefs; docs/install.md sync in T140; no orphaned scope. Only cosmetic non-blocking nits noted (stale 9101 hint self-corrected by T137's fresh-inventory mandate; two acceptance-grep case/history nits each redundantly covered by other clauses). No revision required."
- criticism: []
- new_questions: []
- ledgerRefs: ["goals:G11"]
- sessionLogs: [".cq/logs/20260714-102004-a2a60ff2d5102fd65.md",".cq/logs/20260714-102004-a2d056ba8c6f51a5a.md"]
- rawLogs: [".cq/logs/raw/20260714-102004-a2a60ff2d5102fd65.jsonl",".cq/logs/raw/20260714-102004-a2d056ba8c6f51a5a.jsonl"]

## M39

### R134 — go-ahead

- createdAt: 2026-07-14T10:56:20.435Z
- updatedAt: 2026-07-14T10:56:20.435Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T116 review — RECONCILED APPROVE (unanimous opus+fable go-ahead). Reflect changed to (echo []byte, epochChanged bool, err error) return-flag (no callback — satisfies T119's outside-r.mu lock discipline). Restart classification `restart := st.adopted && sessionID != st.session` captured BEFORE st.session mutation; per-epoch dedup on lastRestartSession/haveRestartSession under r.mu → exactly-once across concurrent paths of one boot. Acceptance (a)-(d) each covered by TestReflectEpochChangedOnPeerRestart + an exactly-once count through the real Prober handshake; all 33 call sites migrated. fable examined+dismissed 3 theoretical signal-swallow paths (frame.Encode/drawChallenge CSPRNG-unreachable; lagging-path extra-true matches the pinned last-restart-session design). go build/vet/test + -race (telemetry+bind) + e2e vet green. LANDED on main at 124c232 (integration fix 225f98d adapted T117's test to this new signature)."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T116","goals:G7","defects:D36"]
- sessionLogs: [".cq/logs/20260714-104922-afe04b49891ef2eb7.md",".cq/logs/20260714-104922-af4f6a49d0ebf98a6.md"]
- rawLogs: [".cq/logs/raw/20260714-104922-afe04b49891ef2eb7.jsonl",".cq/logs/raw/20260714-104922-af4f6a49d0ebf98a6.jsonl"]

### R146 — revise

- createdAt: 2026-07-14T11:30:31.355Z
- updatedAt: 2026-07-14T11:30:31.355Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T119 review round 1 — RECONCILED REVISE (BOTH [opus]+[fable] disapprove, independently, each EMPIRICALLY reproducing the defects). The saturated-restart D36 target scenario is correct and green: the epochChanged wiring in dispatchInbound (outside m.mu, resequencer's r.mu never nested — lock discipline sound), the RebaselineToLow low-anchor arithmetic (stale-high straggler SUSPECT-dropped, underflow guard seq<anchor before anchor-seq>window, threshold off-by-one-correct), per-peer scoping (primary + AddConcentratorPeer), and the discriminating tests (fail without wiring AND with plain Rebaseline) all check out; D32 test untouched + green; build/vet/test/-race green. BUT two pendingLow boundary interactions cause PERMANENT per-peer blackholes (both reproduced with throwaway tests):"
- criticism: ["[opus+fable] PERMANENT BLACKHOLE at small anchor: RebaselineToLow (reseq.go:~602) unconditionally enters pendingLow with anchor=next, but the re-anchor predicate `seq < pendingLowAnchor && pendingLowAnchor - seq > r.window` (reseq.go:305) is UNSATISFIABLE for any seq when anchor <= window+1, and pendingLow is cleared ONLY in that branch (tryResync is bypassed by the early return; repeated restart signals keep the original anchor; no timeout). The restarted sender's first DATA is outerSeq=1 (peer.outerSeq.Add(1)), so re-anchoring needs anchor >= window+2 = 2050 — a peer that released < ~2049 outer-seq on its prior boot then restarts (light traffic, early restart, crash-loop: first restart re-anchors next~1, a second restart within one window pins a tiny anchor) SUSPECT-drops ALL traffic forever. Reproduced: window=64, old boot 50 frames, RebaselineToLow, then 499 new-boot frames → 499/499 dropped, 0 delivered. This is a REGRESSION — pre-T119 the same case self-heals via resync corroboration within ~3 low frames. The acceptance tests only cover anchor>>window (restartHighSeq=9000). FIX: enter pendingLow ONLY when next > window+1; otherwise plain unpin (started=false) which self-heals; ADD a small-anchor (next<=window) restart regression test.","[fable] plain Rebaseline() BREAKS the D32 contract: Rebaseline() (reseq.go:~574) resets `started` but does NOT clear pendingLow, violating its documented 'next Observe re-anchors next' postcondition. After a RebaselineToLow (restart pending, low init not yet arrived), a D32 SetPeerRemote→Rebaseline hub failover leaves the stale pendingLow gate in force: the first frame re-pins next via !started, but every subsequent frame is re-classified against the STALE pendingLowAnchor. Reproduced: anchor=200/window=64, Rebaseline(), then fail-back seqs 300..399 → 0/100 delivered, all SUSPECT-dropped permanently. FIX: clear pendingLow (full unpin supersedes the pending low-anchor) inside Rebaseline(); ADD a RebaselineToLow→Rebaseline interleaving regression test."]
- new_questions: []
- ledgerRefs: ["tasks:T119","goals:G7","defects:D36"]
- sessionLogs: [".cq/logs/20260714-111900-ac937f4001db01ed3.md",".cq/logs/20260714-111900-a4fd5680adb345dca.md"]
- rawLogs: [".cq/logs/raw/20260714-111900-ac937f4001db01ed3.jsonl",".cq/logs/raw/20260714-111900-a4fd5680adb345dca.jsonl"]

### R150 — revise

- createdAt: 2026-07-14T11:51:16.998Z
- updatedAt: 2026-07-14T11:51:16.998Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T119 review round 2 — RECONCILED REVISE (strictest-wins: [fable] disapprove overrides [opus] approve). BOTH round-1 blackhole fixes ARE verified resolved (both reviewers: TestRebaselineToLowSmallAnchorSelfHeals + TestRebaselineToLowThenRebaselineDelivers fail on the round-1 branch and pass on round-2; saturated D36 case, stale-high race, per-peer scoping, lock discipline, D32 tests all intact; full suite + -race green). BUT [fable]'s executable adversarial probes against 82d9793 found TWO NEW confirmed faults in the round-2 pendingLow gate (each reproduced), so the gate is not yet robust:"
- criticism: ["[fable] RESIDUAL PERMANENT BLACKHOLE one seq past the round-2 guard (reproduced 0/499, DroppedSuspect=499): with anchor==window+2 the gate ARMS (next>window+1 holds), but the re-anchor budget is seq <= anchor-window-1 = seq 1 ALONE at that boundary (the restarted first DATA is outerSeq.Add(1)=1). If that single wrapped-init frame is LOST — and loss under saturation IS the D36 premise — every subsequent new-boot frame fails `anchor-seq > window` and is suspect-dropped FOREVER. The pendingLow branch of admit() (reseq.go:296-313) bypasses tryResync corroboration (the pre-T119 self-heal) with no drop-cap or timeout, and the idempotency branch (reseq.go:635) keeps the original anchor across repeated restart signals, so nothing clears the gate. Same failure class as round-1 FIX 1, moved one seq. FIX: BOUND the gate — after O(window) consecutive pendingLow suspect-drops (or a timeout) fall back to a plain unpin (started=false, which self-heals), OR feed gate-failing below-anchor frames into corroboration; ADD boundary tests at next==window+1 (plain unpin) and next==window+2 (armed, satisfiable by seq 1, PLUS the seq-1-lost recovery path).","[fable] FEC REPAIR BYPASSES the pendingLow gate AND the re-anchor does not clear the ring (reproduced next 2→210, Skipped:208): ObserveRecovered (reseq.go:241, production-wired to the SAME per-peer resequencer at multipath.go:1889) never consults pendingLow, so a parity-recovered OLD-boot frame in [anchor, anchor+window) is PLACED while the gate is armed; the admit() re-anchor branch (reseq.go:305-310) sets next=seq WITHOUT clearing the ring (unlike resync(), reseq.go:539, which clears it for exactly this reason). Repro: recovered old-boot seq 210 placed while armed → after the low init re-anchored next=1, the stale occupied cell kept the head-of-line timeout live and expire() jumped next 2→210 (Skipped:208), suspect-dropping new-boot frames 2-3 and delivering a STALE old-boot frame into the new stream (a bounded re-instance of the D36 re-pin the gate exists to prevent); a surviving stale cell can also corrupt buf accounting (occupied-cell overwrite, reseq.go:214-222). FIX: clear the ring/buf in the re-anchor branch (mirror resync()) AND drop recovered frames while pendingLow is armed (a recoverable frame while the gate is up is by definition pre-restart); ADD an ObserveRecovered-while-armed regression test."]
- new_questions: []
- ledgerRefs: ["tasks:T119","goals:G7","defects:D36"]
- sessionLogs: [".cq/logs/20260714-114500-a9bbd3ad9b477e347.md",".cq/logs/20260714-114500-a640aac378eddd264.md"]
- rawLogs: [".cq/logs/raw/20260714-114500-a9bbd3ad9b477e347.jsonl",".cq/logs/raw/20260714-114500-a640aac378eddd264.jsonl"]

### R152 — go-ahead

- createdAt: 2026-07-14T12:19:29.632Z
- updatedAt: 2026-07-14T12:19:29.632Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T119 review round 3 — RECONCILED APPROVE (unanimous opus+fable go-ahead) after two prior revise rounds (R146 round-1, R150 round-2). This was the run's hardest task: the adversarial panel caught FOUR distinct permanent-blackhole defects across three rounds, each empirically reproduced. Round 3 adds the final two fixes and both reviewers actively tried and FAILED to break the gate: FIX 3 (bounded gate) — a pendingLowDrops counter, after O(window) consecutive armed suspect-drops falls back to a plain unpin (started=false) that self-heals via resync corroboration, so a lost seq-1 init at anchor==window+2 can no longer permanently blackhole (arm boundary next>window+1 exactly matches re-anchor satisfiability); FIX 4a (ring clear) — the low re-anchor branch clears ring/buf/waiting mirroring resync()'s invariants, so no stale old-boot cell survives to jump next high; FIX 4b (gate FEC) — ObserveRecovered drops recovered frames while pendingLow is armed (no buf leak, loses no legitimate re-anchor). fable RE-RAN both round-2 executable probes: the lost-init blackhole now delivers 434/499 (was 0/499) and the ObserveRecovered bypass seats nothing (Skipped==0, no high re-pin); plus 4 NEW attack probes (threshold-edge init, straggler-flood counter-trip, FEC-init-while-armed, post-clear ObserveRecovered) ALL held. opus independently constructed a straggler-flood case and confirmed it degrades to bounded resync-corroboration recovery, never a blackhole. Non-regression confirmed: saturated D36 case, stale-high race, small-anchor self-heal (FIX-1), Rebaseline-clears-pendingLow (FIX-2), per-peer scoping (primary+concentrator, witness undisturbed), same-epoch no-op, lock discipline (RebaselineToLow outside m.mu, race clean), D32 hub-failover. Full suite + -race ./internal/bind/...+./internal/reseq/... + just lint all green. The analogous pre-existing Rebaseline()+ObserveRecovered !started re-pin is filed out-of-scope as D64. Two non-blocking nits noted (pendingLowDrops not reset on idempotent re-arm — zero functional impact; deep-bufferbloat degrades to corroboration — bounded, exceeds acceptance). LANDED on main at b786c25 (branch implement/T119-r3, ff18ccd; applied as the 2-commit range 7d0fc42+ff18ccd since the round-2 base lives in the parent commit)."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T119","goals:G7","defects:D36"]
- sessionLogs: [".cq/logs/20260714-120400-ae85588217ae02a05.md",".cq/logs/20260714-120400-a44db3894da9e4335.md"]
- rawLogs: [".cq/logs/raw/20260714-120400-ae85588217ae02a05.jsonl",".cq/logs/raw/20260714-120400-a44db3894da9e4335.jsonl"]

## M40

### R135 — go-ahead

- createdAt: 2026-07-14T10:56:28.492Z
- updatedAt: 2026-07-14T10:56:28.492Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T117 review — RECONCILED APPROVE (unanimous opus+fable go-ahead). Multipath.SetOnFirstPathUp(func()) — nil-safe (double guard cb!=nil && *cb!=nil), fired EXACTLY ONCE off the receive hot path via a dedicated goroutine on the everUp false→true edge. dispatchInbound's everUp.Store(true) became CompareAndSwap(false,true): the CAS is the sole everUp writer package-wide and nothing ever resets it, so at-most-once + no-refire-across-Down→Up→Down→Up hold STRUCTURALLY. 4 non-vacuous tests drive the production demuxInbound path; both reviewers ran uncached `go test -race ./internal/bind/...` green. Non-blocking design notes (for the dependent consumer T120): the seam is edge-triggered (register before Open or consult EverHadLivePath) and the fired goroutine may run after Close. LANDED on main at f5ace40."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T117","goals:G7","defects:D37"]
- sessionLogs: [".cq/logs/20260714-105323-a6bc513252cea77fc.md",".cq/logs/20260714-105323-a38fe4b9603f3e06e.md"]
- rawLogs: [".cq/logs/raw/20260714-105323-a6bc513252cea77fc.jsonl",".cq/logs/raw/20260714-105323-a38fe4b9603f3e06e.jsonl"]

### R145 — go-ahead

- createdAt: 2026-07-14T11:30:12.518Z
- updatedAt: 2026-07-14T11:30:12.518Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T120 review — RECONCILED APPROVE (unanimous opus+fable go-ahead). The forced-WG-handshake-on-first-path-up seam (startFirstPathUpHandshake in failover.go, wired in device.go up() at :329 immediately after NewDevice and BEFORE IpcSet/dev.Up/StartProbeLoop) is correctly ordered: the non-retroactive T117 everUp latch is neither missed (registration precedes any possible path-up edge — a path only reaches StateUp via echoes of self-sent probes, after StartProbeLoop) nor a nil-peer no-op (IpcSet adds the engine peer before probing). Fires EXACTLY ONCE for the edge (CAS-gated everUp false→true, sticky across Down→Up→Down→Up) and NEVER for the concentrator role (early-return on cfg.Role != RoleEdge). deviceRehandshake/ExpireCurrentKeypairs reuse is correct (backdates lastSentHandshake to defeat RekeyTimeout suppression; cold-boot no-keypairs is a no-op). Peers[0] is panic-safe (zero-peer configs rejected at config.go:1080). The startFailoverAndResolution concentrator noop is untouched. BOTH tests proven NON-VACUOUS by fable's mutation testing (disabling the wiring fails the edge test; unconditional wiring fails the concentrator test). go build/vet/test + -race -count=2 green; design.md synced. Non-blocking nit: the 'never disrupts an established session' doc-comment overlooks a ms-scale race whose worst case is one redundant boot-time handshake. LANDED on main at e8cbc55."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T120","goals:G7","defects:D37"]
- sessionLogs: [".cq/logs/20260714-111900-a6ced9e51bf1e4003.md",".cq/logs/20260714-111900-a0bf6c6aac53741e8.md"]
- rawLogs: [".cq/logs/raw/20260714-111900-a6ced9e51bf1e4003.jsonl",".cq/logs/raw/20260714-111900-a0bf6c6aac53741e8.jsonl"]

## M41

### R136 — go-ahead

- createdAt: 2026-07-14T10:56:36.491Z
- updatedAt: 2026-07-14T10:56:36.491Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T118 review — RECONCILED APPROVE (unanimous opus+fable go-ahead). Both reviewers independently VERIFIED the worker's 'already-wired-at-base' claim against base 735ece3: metrics.go declares reseqRebaselines/reseqDroppedSuspect Descs with peerScopedLabels (L292/295) and emits them unconditionally in the Reseq() Collect loop via peerLabelValues (L360/363) — BOTH single-peer (no peer label) AND per-peer (peer=<name>) forms present, the identical const-metric path proven for the reseq series by the pre-existing TestExpositionTwoPeerSeries; provenance multipath.go:2806 r.rq.Stats() → device/metrics.go:141 → exposition. So NO production wiring was needed. The added test (TestExpositionReseqRebaselineAndDropSuspect) drives a REAL reseq.New resequencer through Rebaseline() + the genuine ObserveRecovered SUSPECT branch (not synthetic Stats), scrapes /metrics, and asserts both counters read 1 in the zero-label (single-peer) exposition — the Value() assertion simultaneously proves the no-peer-label back-compat rule. Test-only diff, no datapath change. go build + go test ./internal/reseq/... ./internal/metrics/... green. LANDED on main at 4bf9c52."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T118","goals:G7","defects:D36"]
- sessionLogs: [".cq/logs/20260714-105323-acd25ba2452d0aed4.md",".cq/logs/20260714-105323-a9e8a6fd89060ba75.md"]
- rawLogs: [".cq/logs/raw/20260714-105323-acd25ba2452d0aed4.jsonl",".cq/logs/raw/20260714-105323-a9e8a6fd89060ba75.jsonl"]

### R158 — revise

- createdAt: 2026-07-14T12:56:52.772Z
- updatedAt: 2026-07-14T12:56:52.772Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T121 review round 1 — RECONCILED REVISE (strictest-wins: [fable] disapprove overrides [opus] approve). [fable] EXECUTED the deferred e2e (opus reviewed statically and missed both blocking defects) and found the test as written CANNOT pass. Verified-green parts: build/vet -tags e2e clean, just lint 0 issues, default just test green, //go:build e2e excludes it from the default build, port 9104 unique + registered in netns.go (no D51-style drift), counter constants resolve to the real T118 names, run-A/run-B saturation directions correct (edge→conc / iperf3 -R), relBefore>2048 precondition guards vacuity, restart discipline sound, D37 4s budget discriminates against the 5s retransmit multiple. TWO BLOCKING defects + one minor:"
- criticism: ["[fable, BLOCKING] The survivor's reseq counters are NEVER scraped: r121PeerCounter (test/e2e/restart_onesided_test.go:414-426) reads exp.PeerValue(name, \"\"), which matches only a series carrying a peer=\"\" LABEL — but both daemons bind exactly ONE peer, so metrics.NewCollector sets multiPeer=len(PeerNames())>1 = FALSE (internal/metrics/metrics.go:260-266) and the reseq series are emitted UNLABELED. PeerValue returns (0,false) unconditionally: in the privileged run the D36 precondition Fatalfs ('released_frames_total{peer=\"\"} absent after traffic flowed') on a HEALTHY system, and the core rebaselines/dropSuspect assertions would otherwise silently read 0/0. The repo's own TestExpositionReseqRebaselineAndDropSuspect documents+asserts the single-peer NO-label exposition via exp.Value(...). FIX: read the single-peer exposition via exp.Value(name) (or PeerValue-then-fallback-to-Value), and correct the inverted helper comment (peer=\"\" label values exist only in the MULTI-peer exposition's unnamed primary, not in a single-peer daemon).","[fable, BLOCKING] Acceptance clause 'skips (not fails) without privileges' is OPERATIONALLY FALSIFIED: `go test -tags e2e ./test/e2e -run TestOneSidedRestartRecovery` in an unprivileged sandbox does NOT skip — requireNetAdmin's `ip link add … dummy` probe SUCCEEDS inside TestMain's `unshare -Urmn` user namespace (which grants CAP_NET_ADMIN), then both subtests FAIL ~5s later on 'CreateTUN(\"wanbond0\") failed; /dev/net/tun does not exist' (observed: --- FAIL, 11.86s). The file's own doc comment lists /dev/net/tun as a fixture requirement. FIX: extend the skip-gate to ALSO probe /dev/net/tun availability so the unprivileged run SKIPS.","[fable, minor] The post-restart waitLink checks (restart_onesided_test.go:177, 240) are VACUOUS under tun_persist=true — the persisted link exists across the stop, so waitLink succeeds regardless of whether the restarted daemon re-adopted it, yet the failure message claims 'not re-adopted after restart'. Either assert actual re-adoption (e.g. the restarted process's TUN-adoption log record) or reword the check/message to what it actually verifies."]
- new_questions: []
- ledgerRefs: ["tasks:T121","goals:G7","defects:D36","defects:D37"]
- sessionLogs: [".cq/logs/20260714-124500-a490dba7f580b73b3.md",".cq/logs/20260714-124500-a6c1ec1e870de2511.md"]
- rawLogs: [".cq/logs/raw/20260714-124500-a490dba7f580b73b3.jsonl",".cq/logs/raw/20260714-124500-a6c1ec1e870de2511.jsonl"]

### R160 — go-ahead

- createdAt: 2026-07-14T13:14:50.632Z
- updatedAt: 2026-07-14T13:14:50.632Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T121 review round 2 — RECONCILED APPROVE (unanimous opus+fable go-ahead) after the round-1 revise (R158, where fable EXECUTED the deferred e2e and found two blocking defects). All THREE round-1 fixes verified, both reviewers re-running the test: FIX 1 — r121PeerCounter now reads the single-peer NO-LABEL reseq exposition via a PeerValue-then-Value fallback (traced against internal/metrics: single-peer daemon → multiPeer=len(PeerNames())>1=FALSE → peerLabelValues=nil → series emitted UNLABELED → Value(name) matches len(GetLabel())==0), so the D36 rebaselines>=1 / ~0-dropSuspect assertions can actually observe values; FIX 2 — the skip-gate now probes /dev/net/tun FIRST, and BOTH reviewers ran `go test -tags e2e -run TestOneSidedRestartRecovery -count=1` unprivileged and confirmed `--- SKIP` at 0.00s BEFORE any daemon bring-up (closing the round-1 userns-CAP_NET_ADMIN FAIL path); FIX 3 — the post-restart re-adoption check now polls the RESTARTED process's own log buffer for the 'tunnel interface up' record (cmd/wanbond/main.go:64, emitted only after CreateTUN/TUNSETIFF succeeds; fresh lockedBuffer per proc), non-vacuous under tun_persist. go build/vet -tags e2e clean, just lint 0 issues, just test green, port 9104 unique + registered in netns.go. The run-A/run-B restart matrix, D36 saturation-past-window precondition, D37 4s-budget (< one 5s WG retransmit), and the o3+llm-ubuntu-0 runbook are all intact; the PRIVILEGED run is deferred per the G2 pattern and NOT part of the merge gate. LANDED on main at 50ffe9b (branch implement/T121-r2, a9488e46)."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T121","goals:G7","defects:D36","defects:D37"]
- sessionLogs: [".cq/logs/20260714-130000-aa99e39f80a1a7b81.md",".cq/logs/20260714-130000-a7f14b9d3aa0ae11a.md"]
- rawLogs: [".cq/logs/raw/20260714-130000-aa99e39f80a1a7b81.jsonl",".cq/logs/raw/20260714-130000-a7f14b9d3aa0ae11a.jsonl"]

### R161 — revise

- createdAt: 2026-07-14T13:15:08.307Z
- updatedAt: 2026-07-14T13:15:08.307Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T122 review round 1 — RECONCILED REVISE (BOTH [opus]+[fable] disapprove). The docs-only design.md change is build/lint-green and MOSTLY traces to merged code (counter names match metrics.go:89/92; triggers 1-2 = SetPeerRemote→Rebaseline / epochChanged→RebaselineToLow verified; all four T119 boundary rules verified; D37/T120 first-path-up section retained; NO stale 'hub-failover-only' claim survives). But three technical-accuracy defects (superset of both reviewers):"
- criticism: ["[fable+opus, PRIMARY] Trigger-3 MISATTRIBUTION (design.md ~:430-435 preamble): it claims all three triggers are 'trusted control events, not forgeable', re-anchor 'via Resequencer.Rebaseline or RebaselineToLow', and are 'each tracked by wanbond_resequencer_rebaselines_total'. ALL THREE clauses are FALSE for trigger 3 (D12 resync corroboration): it is the UNAUTHENTICATED path driven by forgeable wire frames (the doc's own item-3 text says 'defense against non-trusted frames'), it goes through tryResync/resync (internal/reseq/reseq.go:533,591) NOT Rebaseline/RebaselineToLow, and it increments r.resyncs → wanbond_resequencer_resyncs_total (reseq.go:599, metrics.go:91), NEVER r.rebaselines (only reseq.go:645,692). REWORD: TWO trusted triggers (hub failover D32, peer restart D36) tracked by rebaselines_total, PLUS the unauthenticated corroboration fallback (D12) tracked by resyncs_total.","[opus+fable, related] The 'Frame rejection during rebaseline recovery' paragraph (design.md ~:481-485) wrongly attributes dropped_suspect_frames_total increments to D32 plain Rebaseline ('increases during both planned rebaselines (D32, D36)') — a plain Rebaseline() (reseq.go:626) unpins + re-anchors on the NEXT frame immediately and NEVER routes through the suspect branch (dead-hub frames drop as stale/old; HIGH stragglers re-pin — the race D36's low-anchor gate exists to prevent). Suspect drops are driven by D36 (low-anchor gate) + D12 (resync), NOT D32. ALSO the suspect classification '(outer-seq outside the acceptance window)' is imprecise: a frame within one window BELOW the release point is dropped as LATE (dropLate, reseq.go:379), not suspect; suspect = >1 window below next, or >= resyncFactor*window ahead, or any drop while the pendingLow gate is armed (reseq.go:363,375,389). Fix the attribution + classification.","[fable] MISSING task-required content: the task description mandates 'State the operational expectation: one-sided restart reconverges ~= the both-ends-fresh baseline, not on the WG rekey timer.' No such statement exists in the diff or design.md (grep reconverge/both-ends/'rekey timer' misses). ADD it."]
- new_questions: []
- ledgerRefs: ["tasks:T122","goals:G7","defects:D36","defects:D37"]
- sessionLogs: [".cq/logs/20260714-125200-abceb9ac1a325216c.md",".cq/logs/20260714-125200-a77a583ad721906d7.md"]
- rawLogs: [".cq/logs/raw/20260714-125200-abceb9ac1a325216c.jsonl",".cq/logs/raw/20260714-125200-a77a583ad721906d7.jsonl"]

### R165 — go-ahead

- createdAt: 2026-07-14T13:37:47.400Z
- updatedAt: 2026-07-14T13:37:47.400Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T122 review round 2 — RECONCILED APPROVE (unanimous opus+fable go-ahead) after the round-1 revise (R161). All THREE round-1 doc-accuracy criticisms are resolved, both reviewers verifying line-by-line against merged code: (1) the re-anchor section now cleanly separates TWO trusted, authenticated triggers — D32 Rebaseline via SetPeerRemote (multipath.go:2331→2345) and D36 RebaselineToLow via dispatchInbound on epochChanged (multipath.go:1837→1855), BOTH incrementing r.rebaselines (reseq.go:645/692) → wanbond_resequencer_rebaselines_total — from the THIRD, UNAUTHENTICATED corroboration fallback (D12 resync, which 'never calls Rebaseline/RebaselineToLow', runs tryResync/resync, increments r.resyncs (reseq.go:599) → wanbond_resequencer_resyncs_total (metrics.go:91)); (2) suspect drops are now attributed to D36 (low-anchor gate) + D12, NOT D32 (a plain Rebaseline re-anchors on the next frame and drops its buffered frames as dropLate, never suspect), with the suspect-vs-late classification matching admit() exactly (suspect: reseq.go:363/375/389, resyncFactor=4; late: :379); (3) the task-required operational-expectation sentence is present and grounded (reconverges ~= the ~25s both-ends-fresh baseline / ~10s predicted edge-restart, cited to test/e2e/restart_onesided_test.go, no invented causal claim). The D37/T120 first-path-up section (design.md:397-411) is retained + accurate; no stale 'hub-failover-only' claim survives; docs-only diff, reseq.go untouched (the stale reseq.go:760 comment is filed as out-of-scope D68). go build + just lint green. COMPLETES goal G7 (T116-T122 all done). LANDED on main at 26cf5c8 (branch implement/T122-r2, 6f40bd16)."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T122","goals:G7","defects:D36","defects:D37"]
- sessionLogs: [".cq/logs/20260714-133500-a507a4b42a8fa94c0.md",".cq/logs/20260714-133500-aa13ce82d5eeddc77.md"]
- rawLogs: [".cq/logs/raw/20260714-133500-a507a4b42a8fa94c0.jsonl",".cq/logs/raw/20260714-133500-aa13ce82d5eeddc77.jsonl"]

## M42

### R137 — go-ahead

- createdAt: 2026-07-14T10:56:48.065Z
- updatedAt: 2026-07-14T10:56:48.065Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T123 review — RECONCILED APPROVE (unanimous opus+fable go-ahead; 2 out-of-scope defects filed → D62/D63). Highest-risk change (lock-free copy-on-write peerBySource restructure). Both reviewers verified all 5 focus invariants: (1) CAS loop recomputes countP/oldestSeq/evictKey per iteration from the reloaded snapshot, lost CAS retries with nothing carried, CoW keeps published maps immutable (no torn/lost updates, no ABA); (2) the eviction scan filters `b.peer != p` (multipath.go:1553-1563) so a peer can STRUCTURALLY never evict another peer's slot — cross-peer isolation, asserted by 4 tests; (3) per-peer quota max(1, maxDemuxSources/len(peersView)) read lock-free, evict is net-zero growth so the global cap holds, maxDemuxSources==0 preserves documented no-cap; (4) T90 roam re-affirm bypasses quota; (5) both demuxInbound sites (:1437 lookup, :1458 bind) AddrPort-keyed. fable's MUTATION-counterfactual proved discrimination: addr-only key → test (a) fails with the exact cross-wiring mode; quota stripped → (b)/(c) fail. go build/vet/test + -race ./internal/bind/... green (verified again on merged main); design.md synced. Two OUT-OF-SCOPE defects filed (file-and-defer, K13): D62 (medium, PRE-EXISTING at base) teardown-vs-bind race installs a dead-peer binding that demuxInbound:1444 then blackholes + leaks a global-cap slot; D63 (low) the 'LRU' is first-bind FIFO (bound sources never refresh recency) — conforms to the pinned insertion-order decision, a refinement not a T123 defect. LANDED on main at ae2d111."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T123","goals:G8","defects:D47","defects:D49"]
- sessionLogs: [".cq/logs/20260714-105323-a84dc3152843ba3be.md",".cq/logs/20260714-105323-a26f9969b0d210376.md"]
- rawLogs: [".cq/logs/raw/20260714-105323-a84dc3152843ba3be.jsonl",".cq/logs/raw/20260714-105323-a26f9969b0d210376.jsonl"]

### R157 — revise

- createdAt: 2026-07-14T12:55:15.429Z
- updatedAt: 2026-07-14T12:55:15.429Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T124 review round 1 — RECONCILED REVISE (strictest-wins: [fable] disapprove overrides [opus] approve). Both reviewers verified the SUBSTANCE is sound and green: the promotion now fans a view + scheduler entry to EVERY bound peer (was primary-only), reusing each peer's already-m.defs-aligned prober via attachSharedPathLocked extended with an optional probers param (nil mints fresh for AddPath — byte-identical to before; non-nil reuses for promotion), shared.id=probers[0].PathID() keeps the wire path-id consistent while each peer decodes on its own PSK, rollback publishes `shared` only after full success (no live-state leak), and all 3 new regression tests fail pre-fix for the right reasons (test (a) reproduces the exact D42 slice-bounds [2:1] panic; (b)/(c) show the beta peer view-less on promote / promote+reopen) and pass post-fix. opus empirically confirmed the aligned 2-peer deferred RemovePath never panics at base (validating the worker's narrowing that the durable membership was already aligned). go build/vet/test + -race + just lint all green. The REVISE is one under-strength guard fable pinned:"
- criticism: ["[fable] removeDurableLocked's alignment guard (internal/bind/multipath.go ~:2769-2774) is WEAKER than the task specifies: the task requires a wiring-defect error 'when ANY p.probers length diverges from m.defs', but the implemented check only fires on `defIdx >= len(p.probers)` (index-out-of-range). A divergent-but-IN-RANGE case passes SILENTLY — the exact 'silently splice the WRONG entry' failure the function's own doc comment claims the guard prevents: (1) a peer whose probers fell short at the TAIL (len 2 vs m.defs len 3) with RemovePath at a low defIdx splices and leaves the desync undetected; (2) probers LONGER than m.defs is never detected; (3) a NON-TAIL desync splices the WRONG prober. FIX: in the guard loop (which runs BEFORE the m.defs splice) check `p.probers != nil && len(p.probers) != len(m.defs)` and return the wiring-defect error — test (a) still passes (beta len 1 vs defs len 2). ADD a regression test for the in-range divergence / wrong-entry case (a peer with a same-length-but-mis-mapped or longer/tail-short probers slice)."]
- new_questions: []
- ledgerRefs: ["tasks:T124","goals:G8","defects:D42"]
- sessionLogs: [".cq/logs/20260714-124000-a5092d8500e5d2467.md",".cq/logs/20260714-124000-a931ad26492d4a88b.md"]
- rawLogs: [".cq/logs/raw/20260714-124000-a5092d8500e5d2467.jsonl",".cq/logs/raw/20260714-124000-a931ad26492d4a88b.jsonl"]

### R159 — go-ahead

- createdAt: 2026-07-14T13:07:20.176Z
- updatedAt: 2026-07-14T13:07:20.176Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T124 review round 2 — RECONCILED APPROVE (unanimous opus+fable go-ahead) after the round-1 revise (R157). The sole round-1 criticism is resolved: removeDurableLocked's alignment guard now checks `len(p.probers) != len(m.defs)` per peer BEFORE any mutation (multipath.go:2773-2777), naming the peer + both lengths — covering tail-short in-range, probers-longer-than-m.defs, AND non-tail desync (the full 'any length divergence' the task specified, not just the round-1 index-out-of-range case). BOTH reviewers empirically re-verified by reverting the guard to the round-1 index-only form in a scratch worktree: the new TestRemoveDeferredPathInRangeMisalignedPeerProbersFailsFast then FAILS ('RemovePath succeeded, want a wiring-defect error' — the exact silent-mis-splice the guard prevents) while the round-1 tail-short test still passes, so the new test isolates precisely the in-range class. The round-1-approved core (promoteDeferredLocked fan-out to every peer, attachSharedPathLocked optional-probers reuse, rollback, the 3 original tests) is BYTE-IDENTICAL between rounds (reconcile.go unchanged); the deferred-AddPath-mints-per-peer-prober invariant keeps len(p.probers)==len(m.defs) so the guard cannot false-positive on legitimate states. go build/vet/test + -race ./internal/bind/... + just lint all green. LANDED on main at 11e67e6 (branch implement/T124-r2, 0ef749f6). Two out-of-scope pre-existing defects were filed during round 1: D66 (stale single-peer-receive comment), D67 (attachSharedPathLocked rollback swallows detach errors)."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T124","goals:G8","defects:D42"]
- sessionLogs: [".cq/logs/20260714-125200-a208a987d174ab1a6.md",".cq/logs/20260714-125200-a83001b621c7fea3d.md"]
- rawLogs: [".cq/logs/raw/20260714-125200-a208a987d174ab1a6.jsonl",".cq/logs/raw/20260714-125200-a83001b621c7fea3d.jsonl"]

### R166 — revise

- createdAt: 2026-07-14T13:46:57.812Z
- updatedAt: 2026-07-14T13:46:57.812Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T125 review round 1 — RECONCILED REVISE (strictest-wins: [fable] disapprove overrides [opus] approve). BOTH reviewers confirmed the PRODUCT fan-out is CORRECT and race-clean: fecFlushDeadline/driveAdaptiveControllerLocked/maxEligiblePathLossLocked now iterate m.peers (were embedded-primary-only), per-peer fecSend.Load nil-skip torn-down/uninstantiated, encodeParityLocked(peer,...), scheduler.Pick over that peer's paths, writes accumulated under the lock + egressed after unlock (no cross-peer buffer aliasing); the tick-gate widening (m.fecCfg!=nil) no-ops (not busy-spins) when no peer has fecSend; the new test is a valid D44 witness (fails pre-fix at peer-B i/o-timeout, passes post-fix 200/200 under -race); opus found ZERO DATA RACE across ~380 race runs. But TWO distinct flaky-test findings make the acceptance's `go test -race ./internal/bind/...` gate NON-DETERMINISTIC:"
- criticism: ["[fable, IN-SCOPE] T125's OWN new test TestFECFlushDeadlineFansOutAcrossPeers (fec_multipeer_flush_test.go:169) has a cross-psk assertion that false-fails ~1/256 per run (reproduced 2x in 60 runs): parity frames carry NO MAC by design, so codecA.Decode(rawBBytes) garbles the kind byte to a ~uniform value that lands on KindParity(2) with p≈1/256, whose 8-byte header parses within the 10-byte body with no further validation (internal/frame/frame.go:431-439), so Decode returns nil error and trips `if err==nil { t.Fatal }`. FIX per the perpsk_test.go:70 precedent: on err==nil, fail ONLY if the decoded frame actually matches peer B's parity SEMANTICS (a frame.Parity whose Payload/DataCount equal parB's) — an accidental garbage-decode into a NON-matching frame is the designed behaviour of unauthenticated kinds, not an isolation violation.","[opus, D69 — pre-existing but same gate] TestMultipathFECDeadlineEmitsPartialGroupParity (internal/bind/fec_test.go:585, a file T125 does NOT modify) reads FECSnapshot().ParityFrames the instant it has received both parity wires, RACING the async fecTickLoop goroutine's post-WriteToUDPAddrPort counter increment — ~2% flake post-fix (0% pre-fix, so marginally perturbed by the fan-out, not introduced). Filed out-of-scope as D69. SINCE IT SHARES the same `go test -race ./internal/bind/...` acceptance gate and is a trivial test-only synchronization fix, ALSO harden it in this round: poll FECSnapshot().ParityFrames with a short bounded retry (until it reaches parityShards or a ~200ms deadline) instead of a single immediate read, so the gate becomes deterministic."]
- new_questions: []
- ledgerRefs: ["tasks:T125","goals:G8","defects:D44"]
- sessionLogs: [".cq/logs/20260714-133500-a0d5661f211d7c783.md",".cq/logs/20260714-133500-a9fb280c853bdb098.md"]
- rawLogs: [".cq/logs/raw/20260714-133500-a0d5661f211d7c783.jsonl",".cq/logs/raw/20260714-133500-a9fb280c853bdb098.jsonl"]

### R167 — go-ahead

- createdAt: 2026-07-14T14:15:23.856Z
- updatedAt: 2026-07-14T14:15:23.856Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T125 review round 2 — RECONCILED APPROVE (unanimous opus+fable go-ahead) after the round-1 revise (R166). The PRODUCT fan-out (both reviewers approved it in round 1: fecFlushDeadline/driveAdaptiveControllerLocked/maxEligiblePathLossLocked fanned across m.peers with per-peer psk-codec parity + nil-skip, tick-gate m.fecCfg!=nil, race-clean across ~380 round-1 runs) is BYTE-IDENTICAL between rounds (diff 02eebafa..444455eb on multipath.go/adaptive_test.go is EMPTY). Round 2 hardened the TWO flaky test assertions and BOTH reviewers verified the gate is now DETERMINISTIC: FIX 1 [fable, in-scope] — TestFECFlushDeadlineFansOutAcrossPeers's cross-psk assertion now requires a SEMANTIC field+payload match (per perpsk_test.go) instead of any nil-err decode; since the parity body is XOR-obfuscated under a PSK-derived XChaCha20 keystream, a real isolation break de-obfuscates field-exact (fires t.Fatal) while the ~1/256 accidental KindParity garbage-decode yields non-matching fields — proven NON-VACUOUS via an overlay key-collapse probe (fires only on a simulated break), and count=100 -race now shows ZERO false-failures (was ~32% expected over 100). FIX 2 [opus, closes D69] — TestMultipathFECDeadlineEmitsPartialGroupParity now polls FECSnapshot().ParityFrames with a bounded 200ms retry then asserts strict equality, so it masks NEITHER an under- nor over-count (the ~2% counter-lag flake vs the async post-write increment is gone). `go test -race -count=50 -run 'FEC|Flush|Deadline|Adaptive'` passes all 50 iterations; full go test -race ./internal/bind/... + build/vet + just lint green. LANDED on main at 3eab82e (branch implement/T125-r2, 444455eb). Resolves D44; closes D69."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T125","goals:G8","defects:D44","defects:D69"]
- sessionLogs: [".cq/logs/20260714-135500-a47f4a34704774c02.md",".cq/logs/20260714-135500-a724702fe94bef9ec.md"]
- rawLogs: [".cq/logs/raw/20260714-135500-a47f4a34704774c02.jsonl",".cq/logs/raw/20260714-135500-a724702fe94bef9ec.jsonl"]

## M46

### R138 — go-ahead

- createdAt: 2026-07-14T10:56:58.074Z
- updatedAt: 2026-07-14T10:56:58.074Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T133 review — RECONCILED APPROVE (unanimous opus+fable go-ahead). Both reviewers confirmed ps.txBytes.Add is on the nil-error path ONLY at both sites (probe.go emitProbes after m.mu released; multipath.go dispatchInbound echo on the lock-free receive goroutine) — exactly 4 Add sites total, probe/echo writes bypass Send + fecFlushDeadline so there is NO double-count, and the atomic add needs no lock change. Both new fake-clock tests were EMPIRICALLY verified fail-first: reverting probe.go/multipath.go to base 735ece3 with the tests kept yields 'txBytes = 0, want 75' (the exact D48 gap, right reason), and the fixed tree passes fresh. The T104 standby-idle subtest's delta>0 assertion (standby_liveness_test.go:133 `if delta <= 0 { t.Errorf }`) is UNCHANGED — only the stale-repro doc-comment + t.Errorf prose were rewritten. The peerPathState counter-contract comment (multipath.go:157-170) now states true-wire-volume ('Neither counter is DATA-only or Send-only (D48)') and the metrics help string ('Total bytes transmitted on the path.') is accurate; README/design.md carry no stale DATA-only tx_bytes wording. go build/vet/test (bind+metrics) + -tags e2e build/vet green. LANDED on main at 0487f0a."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T133","goals:G10","defects:D48"]
- sessionLogs: [".cq/logs/20260714-105323-a22078d2020cbe7b9.md",".cq/logs/20260714-105323-a68872eff2c279acf.md"]
- rawLogs: [".cq/logs/raw/20260714-105323-a22078d2020cbe7b9.jsonl",".cq/logs/raw/20260714-105323-a68872eff2c279acf.jsonl"]

### R169 — revise

- createdAt: 2026-07-14T15:06:53.049Z
- updatedAt: 2026-07-14T15:06:53.049Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T134 review round 1 — RECONCILED REVISE (strictest-wins: [fable] disapprove overrides [opus] approve). Both reviewers confirmed the CORE plumbing is correct and green: NewMultipath's required log.Logger param (nil fail-fast, Component('bind')), all call sites updated, listenPath's 3-return restructure preserving a working source-IP fallback socket (pathsock.go stays logging-free), the two WARN layers mutually exclusive (unresolvable dev=='' vs deviceErr!=nil), the FORCED(WARN)/AUTO(INFO) distinction, and the 6 new tests mutation-verified to fail against the silent pre-fix code; build/vet/test + -race + just lint all green. But [fable] reproduced TWO log-correctness defects (opus independently flagged #1 as a defect):"
- criticism: ["[fable+opus] WARN SPAM at 1 Hz: warnForcedDeviceUnresolvable holds NO per-path state, and reconcileDeferred (internal/bind/reconcile.go:116) runs at 1 Hz in production (StartReconcileLoop(DefaultReconcileInterval), device.go:398). REPRODUCED: a deferred bind='device' path whose interface stays unresolvable emits 5 identical WARNs over 5 reconcileDeferred() calls — one WARN PER SECOND for the whole deferral window (a normal startup transient: mobile edge pre-DHCP, Starlink mid-obstruction). After the first record the information is zero, and sustained per-second WARN trains operators to IGNORE the very signal D53 adds. FIX: log ONCE per condition transition (a 'warned' flag on the deferred-path entry, cleared when the interface resolves or the path promotes), not per tick; ADD a test asserting a SECOND reconcileDeferred() with the same unresolved state emits 0 NEW WARNs.","[fable] FALSE-FALLBACK claim: both WARN sites fire regardless of the listen OUTCOME. In reconcileDeferred (and Open's tolerate-defer / AddPath's defer paths), when the subsequent listen FAILS (EADDRNOTAVAIL) the path stays DEFERRED — NO source-IP-pinned socket exists — yet the layer-(a) WARN text asserts 'falling back to source-IP pinning (roam survival ... is lost)'. Similarly warnDeviceBindFallback WARNs BEFORE err is checked, claiming a fallback that FAILED to bind. FIX: gate the fallback WARN on the fallback socket actually MATERIALIZING (err==nil), or reword the deferred-retry case so it does not assert a fallback that did not occur."]
- new_questions: []
- ledgerRefs: ["tasks:T134","goals:G10","defects:D53"]
- sessionLogs: [".cq/logs/20260714-140500-a24a0b2be975ad925.md",".cq/logs/20260714-140500-a83f17ef8179b04cf.md"]
- rawLogs: [".cq/logs/raw/20260714-140500-a24a0b2be975ad925.jsonl",".cq/logs/raw/20260714-140500-a83f17ef8179b04cf.jsonl"]

### R170 — go-ahead

- createdAt: 2026-07-14T15:29:43.468Z
- updatedAt: 2026-07-14T15:29:43.468Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T135 review round 1 — RECONCILED GO-AHEAD (opus claude:opus-4.8[1m] + fable claude:fable-5 panel, strictest-wins; BOTH approve). reloadWarnings extended to Scheduler/FEC/DNS/Bind + D52 future-proof catch-all. Merged to main as 82463bb (cherry-pick of 447dbca onto dfb5d71; device.go+reload_test.go only, clean apply — main was a768452+ledger-only). Full gate green on composed main: go build/vet/test ./internal/device/... ./internal/config/... ok; just lint 0 issues across default+e2e+realhosts. Both reviewers verified EMPIRICALLY: Scheduler/FEC/DNS/top-level-Bind/per-path-Bind each fire exactly one actionable warning (strengthened len(w)==1 assertions); Metrics + membership-only changes fire zero (catch-all zeroes Metrics+Paths, false-positive-free); the catch-all zeroes exactly Config's 12 fields {Role,Paths,WireGuard,Amnezia,PSK,Metrics,Log,Scheduler,FEC,DNS,Bind,TUNPersist} — tautologically DeepEqual-true today, and structurally cannot miss a future 13th field — with TestReloadWarningsCatchAll bidirectionally pinning the field set via reflection (fails on both an unknown field and a stale known-entry); reloadWarnings stays pure (shallow copies only). Non-blocking nuance both noted: the top-level-Bind test uses pre-normalize input (Path.Bind=='') to force exactly-one; on production-normalized configs a top-level default edit additionally fires per-path warnings (1+N total) — conforms to R131's sanctioned design (per-path covers effective changes; the prohibited double-warn was the generic catch-all, structurally unreachable for Bind since it is zeroed in the copies). Every emitted warning is individually true and actionable. BOTH reviewers independently filed ONE identical out-of-scope defect → D70 (per-path link_bandwidth/link_rtt silent acceptance on reload)."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T135","goals:G10","defects:D52","defects:D70"]

### R171 — revise

- createdAt: 2026-07-14T15:30:38.907Z
- updatedAt: 2026-07-14T15:30:38.907Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T134 review round 2 — RECONCILED REVISE (strictest-wins: [fable] disapprove overrides [opus] approve). Both round-1 criticisms VERIFIED FIXED and mutation-locked by BOTH reviewers: FIX1 dedup — deleting the !alreadyWarned check makes TestReconcileDeferredDedupesUnresolvableWarn fail (WARN count 1, want 0); FIX2 false-claim — restoring the false claim at Open's deferral makes TestOpenWarnsOnUnresolvableForcedDeviceBind fail the !strings.Contains('falling back') assertion. warnedUnresolvable latch is per-path, seeded/threaded/cleared correctly, leak-free (opus confirmed). build/vet/test/-race/just-lint all green at 8ce35eb; merge-base==a768452; 12 D53 tests pass non-cached. BUT [fable] adversarial mutation hunt found the round-1 DEFECT CLASS RELOCATED to the promote-failure edge, plus a mutation-vacuous test — REVISE for round 3."
- criticism: ["[fable] FALSE-FALLBACK + PER-TICK SPAM RELOCATED to the promote-failure edge: in reconcileDeferred (internal/bind/reconcile.go ~L128-135) the two success-path warns (warnForcedDeviceUnresolvable + warnDeviceBindFallback) fire after deferredListen succeeds but BEFORE promoteDeferredLocked. On promotion failure the fresh socket is closed (_ = c.Close()) and the path stays deferred — so 'falling back to source-IP pinning' is OUTCOME-FALSE (no socket persists, path never comes up), and because promotion is retried every 1 Hz tick with NO dedup on these two warns, a persistent promotion failure (defIdx/prober desync or attachSharedPathLocked error — the code's own 'wiring defect' path) spams the false claim once per tick. This is exactly the round-1 defect class (false claim + 1 Hz spam) relocated. FIX: emit both warns ONLY after promoteDeferredLocked returns nil (the fallback socket has actually materialized AND been installed).","[fable] TestReconcileDeferredReArmsAfterResolveThenUnresolve is MUTATION-VACUOUS for the latch-clear it documents: deleting `dp.warnedUnresolvable = false` from reconcileDeferred leaves the test PASSING (verified by mutation). It promotes the entry OUT of m.deferred then AddPath mints a FRESH deferredPath whose latch is trivially unset — the latch-clear line's only observable flow (listen-success → promote-FAILURE → SAME kept entry) is untested. FIX: strengthen to re-arm the SAME kept entry — inject a promoteDeferredLocked failure (e.g. rename m.deferred[0].def.Name so defIdx<0) so listen-success clears the latch on a KEPT entry, then drive a failing listen and assert a NEW WARN fires.","[fable] STALE COMMENT in TestAddPathWarnsOnUnresolvableForcedDeviceBind (internal/bind/devicebind_warn_test.go ~L136): 'the WARN under test fires BEFORE that bind attempt, independent of whether the fallback bind itself later succeeds' — FALSE after round 2: the warn fires INSIDE the EADDRNOTAVAIL deferral branch (AFTER the failed bind), and a successful fallback logs the OTHER (fallback-claiming) message instead. Correct the comment to the round-2/3 semantics."]
- new_questions: []
- ledgerRefs: ["tasks:T134","goals:G10","defects:D53","defects:D71"]

### R173 — go-ahead

- createdAt: 2026-07-14T15:50:37.346Z
- updatedAt: 2026-07-14T15:50:37.346Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T134 review round 3 — RECONCILED GO-AHEAD (opus + fable panel, strictest-wins; BOTH approve). Round-3 fixed the round-2 fable-caught defect (false-fallback + per-tick spam RELOCATED to the promote-failure edge) at ALL THREE listenPath call sites: reconcileDeferred (reconcile.go:154-155, warns after promoteDeferredLocked returns nil, continue at :148 on failure), Open (multipath.go:1049, after the peer fan-out loop), AddPath (multipath.go:2706, after attachSharedPathLocked) — the worker found Open/AddPath had the SAME before-install ordering defect and relocated those too. BOTH reviewers independently MUTATION-VERIFIED: (a) deleting `dp.warnedUnresolvable = false` fails the new TestReconcileDeferredKeptEntryClearsLatchOnPromoteFailureThenReArms at the latch assertion (round-2 vacuity CLOSED); (b) moving warns back before promote fails it at the zero-WARN assertion with the exact outcome-false 'falling back to source-IP pinning' record; fable additionally ran a fresh 5-tick persistent-promote-failure repro emitting ZERO fallback claims, and confirmed exactly three listenPath call paths exist (Open :960, addPathListen :2634, deferredListen :116) — no fourth relocation site, no per-peer N-multiplication. Genuine success-install still warns exactly once (no over-suppression). C3 stale comment + docs (design.md/install.md) corrected to 'materialized AND installed' semantics. Both noted a NON-blocking marginal Open cross-def-abort window (path i warns true-at-emission, then a later sibling def's fatal listen aborts Open — one-shot, no retry loop, not the spam-class defect). Merged to main as ac9274b (round-2 core) + 10f8a4c (round-3) via clean cherry-pick of a768452..f7b605f onto a4406e7 (docs/install.md + device.go 3-way auto-merged with T158/T135, no conflicts). Full gate green on composed main: go build/vet/test bind+device ok; just lint 0 issues default+e2e+realhosts. 3 rounds, defect class provably eliminated."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T134","goals:G10","defects:D53"]

## M47

### R139 — go-ahead

- createdAt: 2026-07-14T10:57:10.106Z
- updatedAt: 2026-07-14T10:57:10.106Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T136 review — RECONCILED APPROVE (unanimous opus+fable go-ahead; 1 low out-of-scope defect → D61). Clean-golangci-cache `just lint` exits 0 on the tracked tree; the doh.go:206/dot.go:168 errcheck + pathsock.go/metrics_test.go QF1001 findings are gone, with the Body.Close still bodyclose-recognizable. CRITICAL De Morgan check: both reviewers verified logical equivalence — opus algebraically, fable by EXHAUSTIVE executed truth tables (pathsock.go:242 !(A∧B)≡¬A∨¬B over 4 combos; metrics_test.go !((A∧B)∨(C∧D))≡(¬A∨¬B)∧(¬C∨¬D) over 16 combos, zero mismatches) plus the passing D13 TestFamilyBindCount regression pinning all 4 semantic quadrants. Hermeticity confirmed: a planted .claude/worktrees/x/bad.go leaves `just lint` at exit 0 and is never linted. The explicit package-list Justfile recipe (./cmd/... ./internal/... ./test/...) loses zero coverage (no tracked .go outside those trees). CAP_NET_RAW/D40 comment untouched. go build/vet/test green. ONE OUT-OF-SCOPE defect filed (D61, low): fable's probe shows a bare `golangci-lint run` ALSO exits 0 on the planted dot-dir file (Go package loading skips dot-directories), so D54's recorded 'walks .claude/worktrees' mechanism is unreproducible and the observed leak matches D45's own-tree findings (likely misattribution) — the fix remains sound as a by-construction hermeticity guarantee. LANDED on main at 4a38f8c."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T136","goals:G11","defects:D45","defects:D54"]
- sessionLogs: [".cq/logs/20260714-105323-a5498c8846b8634b0.md",".cq/logs/20260714-105323-afb3dce8711b40b84.md"]
- rawLogs: [".cq/logs/raw/20260714-105323-a5498c8846b8634b0.jsonl",".cq/logs/raw/20260714-105323-afb3dce8711b40b84.jsonl"]

### R142 — go-ahead

- createdAt: 2026-07-14T11:19:56.886Z
- updatedAt: 2026-07-14T11:19:56.886Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T138 review — RECONCILED APPROVE (unanimous opus+fable go-ahead). Comment-only refresh of the 4 stale config.go doc-comments (D57+D60). Acceptance grep returns nothing (all 4 stale phrases gone); both reviewers confirmed the diff is STRICTLY comment-only (declarations unchanged) and every replacement names a REAL wired consumer, grep-verified against source: cfg.PeerIdentities() (config.go:721) called by device.go:293; per-peer PSKs flow NewMultipath/AddConcentratorPeer → peerBySource PROBE-MAC-authenticated demux (multipath.go:440/1435); BoundPeerNames/PeerSnapshot.Name (multipath.go:2751/2877) → metrics 'peer' label (metrics.go:20); selectDeviceBinds (pathsock.go:128-136) switches on BindMode + AddPath honors forced BindModeDevice (multipath.go:2452) — confirming the deleted D60 sentences were false. go build + just lint green. LANDED on main at 0cd6d1c."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T138","goals:G11","defects:D57","defects:D60"]
- sessionLogs: [".cq/logs/20260714-110600-a49e94b205e22d0c4.md",".cq/logs/20260714-110600-af680b3f433ebeead.md"]
- rawLogs: [".cq/logs/raw/20260714-110600-a49e94b205e22d0c4.jsonl",".cq/logs/raw/20260714-110600-af680b3f433ebeead.jsonl"]

### R143 — revise

- createdAt: 2026-07-14T11:20:08.409Z
- updatedAt: 2026-07-14T11:20:08.409Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T137 review round 1 — RECONCILED REVISE (strictest-wins: [fable] disapprove overrides [opus] approve). Both reviewers confirmed the CORE fix is correct and green: the 9096 collision (pacing_test.go vs p3_fec_test.go) is resolved by moving pacing_test.go's metrics listener to :9103; a fresh grep shows all 9 e2e metrics ports unique (9095-9103); the netns.go port registry comment matches every constant; 9103 is unused elsewhere; e2e-test-only; go vet -tags e2e + golangci-lint(e2e) + go test ./... green. The REVISE is a doc-consistency gap fable pinned: the port move FALSIFIED two PRE-EXISTING neighbor port-inventory comments that the diff did not update."
- criticism: ["[fable] The pacing→9103 move left two now-FALSE port-inventory comments in the tree: test/e2e/tolerant_startup_test.go:30 still says '(see test/e2e/p2/p3/p4/pacing for 9095-97)' and test/e2e/hub_failover_test.go:81 still says '(p2/p3/p4/pacing/tolerant use 9095-9098)' — both were true when pacing bound 9096 and are false now that pacing binds 9103. Since the task's whole purpose is a drift-PROOF port registry, leaving two contradicting inventories defeats it. FIX: update both comments — either drop pacing from those ad-hoc lists or replace them with a pointer to the netns.go registry — then re-run `go vet -tags e2e ./test/e2e/...`."]
- new_questions: []
- ledgerRefs: ["tasks:T137","goals:G11","defects:D51"]
- sessionLogs: [".cq/logs/20260714-110600-ab0af2aadb32069b9.md",".cq/logs/20260714-110600-a188d13d342d1aff9.md"]
- rawLogs: [".cq/logs/raw/20260714-110600-ab0af2aadb32069b9.jsonl",".cq/logs/raw/20260714-110600-a188d13d342d1aff9.jsonl"]

### R144 — revise

- createdAt: 2026-07-14T11:20:20.652Z
- updatedAt: 2026-07-14T11:20:20.652Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T140 review round 1 — RECONCILED REVISE (strictest-wins: [fable] disapprove overrides [opus] approve). Both reviewers verified the CORE reconciliation is correct: on Linux ≥5.7 SO_BINDTODEVICE needs NO capability for a not-yet-bound socket (kernel commit c427bfec18f21, v5.7, WebSearch-confirmed by opus AND independently by fable via a local zero-caps probe on kernel 7.0.8 succeeding); the worker's o3 probe (uid nobody, zero caps, kernel 6.17) is STRONGER than the CAP_NET_ADMIN-only bar; the diff is comment/docs-only with CapabilityBoundingSet unchanged in both units; the >=5.7 rule is stated across pathsock_linux.go + both units + docs/install.md with the EPERM→source-IP fallback; build/vet/test/lint green. The REVISE is a residual in-scope inconsistency fable caught: one CAP_NET_RAW surface the task text explicitly names ('any other CAP_NET_RAW mention in internal/bind') was left self-contradicting."
- criticism: ["[fable] internal/bind/pathsock.go:31-33 — the diff RELOCATED the pre-existing parenthetical '(the daemon runs privileged, but the unit tests bind loopback unprivileged)' INSIDE the new 'on Linux <5.7, SO_BINDTODEVICE needs CAP_NET_RAW' clause, which now IMPLIES the daemon satisfies that pre-5.7 CAP_NET_RAW requirement. But the shipped units grant only CAP_NET_ADMIN (NOT CAP_NET_RAW), and pathsock_linux.go + docs/install.md:879 state the daemon itself EPERMs and falls back to source-IP binding on pre-5.7 kernels. This resurrects, in miniature, the exact pathsock-comment-vs-units disagreement D40 exists to eliminate. FIX: reword the parenthetical so it does NOT suggest the daemon holds CAP_NET_RAW pre-5.7 (e.g. note the units grant only CAP_NET_ADMIN, so a pre-5.7 daemon also takes the source-IP fallback; unit tests bind loopback unprivileged as well). Re-run `grep -rn CAP_NET_RAW internal/ packaging/ docs/` to confirm all surfaces agree."]
- new_questions: []
- ledgerRefs: ["tasks:T140","goals:G11","defects:D40"]
- sessionLogs: [".cq/logs/20260714-110600-aa89c285efbeaba6e.md",".cq/logs/20260714-110600-a1757d0d469487eb3.md"]
- rawLogs: [".cq/logs/raw/20260714-110600-aa89c285efbeaba6e.jsonl",".cq/logs/raw/20260714-110600-a1757d0d469487eb3.jsonl"]

### R147 — go-ahead

- createdAt: 2026-07-14T11:36:38.952Z
- updatedAt: 2026-07-14T11:36:38.952Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T140 review round 2 — RECONCILED APPROVE (unanimous opus+fable go-ahead) after the round-1 revise (R144). The sole round-1 criticism is resolved: pathsock.go:31-38 is reworded to 'on Linux <5.7, SO_BINDTODEVICE needs CAP_NET_RAW, which the shipped systemd units don't grant (CapabilityBoundingSet is CAP_NET_ADMIN only), so a pre-5.7 daemon also falls back to source-IP binding; on >=5.7 it needs no capability' — it NO LONGER implies the daemon holds CAP_NET_RAW pre-5.7. Both reviewers ran `grep -rn CAP_NET_RAW internal/ packaging/ docs/` (5 hits: pathsock.go, pathsock_linux.go, both units, docs/install.md) and confirmed ALL surfaces agree on the >=5.7-qualified rule with the EPERM→source-IP fallback, none implying daemon CAP_NET_RAW. Diff comment/docs-only; CapabilityBoundingSet=CAP_NET_ADMIN unchanged in both units. Kernel commit c427bfec18f21 (v5.7, 'enable SO_BINDTODEVICE for non-root users', not-yet-bound-socket) verified upstream by both; the o3 probe (uid nobody, zero caps, kernel 6.17, SUCCEEDED) is documented in the commit message. build/vet/test + just lint green. LANDED on main at 831c8d4 (branch implement/T140-r2, c1139e4)."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T140","goals:G11","defects:D40"]
- sessionLogs: [".cq/logs/20260714-113100-afad7cc70124eb736.md",".cq/logs/20260714-113100-abf8c449d30e7dc7a.md"]
- rawLogs: [".cq/logs/raw/20260714-113100-afad7cc70124eb736.jsonl",".cq/logs/raw/20260714-113100-abf8c449d30e7dc7a.jsonl"]

### R148 — revise

- createdAt: 2026-07-14T11:36:51.391Z
- updatedAt: 2026-07-14T11:36:51.391Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T137 review round 2 — RECONCILED REVISE (strictest-wins: [fable] disapprove overrides [opus] approve). The round-1 criticism IS resolved within the branch's own tree (tolerant_startup_test.go:30 + hub_failover_test.go:81 now reference the netns.go registry; vet/lint/test green). BUT [fable] caught a decisive INFRA fault: the branch implement/T137-r2 forked from STALE main 60138a6 (the G2-era tip) instead of current main — the recurring harness worktree-base bug — so its port inventory ran against a tree MISSING three e2e files present on the merge target. [opus] approved only because it reviewed against that same stale merge-base (saw 6 ports). Against CURRENT main the fix is incomplete:"
- criticism: ["[fable] The netns.go port registry the branch adds lists only 9095-9099 + 9103, but current main has THREE more claimed metrics ports the stale worktree never saw: 9100 (standby_liveness_test.go:54), 9101 (session_established_test.go:34 — the exact 'T101 already claimed 9101' the task text names), 9102 (multipeer_test.go:130). Post-merge the registry omits them, failing the acceptance clause 'the port-inventory comment lists them'. FIX: the port CHOICE 9103 is sound (current main's max is 9102, no collision) — keep it — but extend the registry to enumerate ALL of 9095-9103.","[fable] multipeer_test.go:129 on current main says 'the current max is 9101' — falsified once pacing moves to 9103 (the identical stale-neighbor-comment defect class as round 1, invisible to the stale-based worker). FIX: re-point that comment at the netns.go registry (or update the stated max)."]
- new_questions: []
- ledgerRefs: ["tasks:T137","goals:G11","defects:D51"]
- sessionLogs: [".cq/logs/20260714-113100-a31a4ac311f57014b.md",".cq/logs/20260714-113100-a48436a017bd022d0.md"]
- rawLogs: [".cq/logs/raw/20260714-113100-a31a4ac311f57014b.jsonl",".cq/logs/raw/20260714-113100-a48436a017bd022d0.jsonl"]

### R149 — go-ahead

- createdAt: 2026-07-14T11:39:47.159Z
- updatedAt: 2026-07-14T11:39:47.159Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T137 round 3 — GO-AHEAD (orchestrator-completed after two worker rounds hit the recurring stale-worktree-base infra fault). Round 1 + round 2 both had reviewer-approved SUBSTANCE (move pacing 9096→9103 to break the collision with p3_fec; re-point the tolerant_startup/hub_failover survey comments at a netns.go registry), but both worker worktrees were cut from STALE main 60138a6, so round 2's registry omitted 9100/9101/9102 and left multipeer_test.go:129's 'max is 9101' comment falsified on the merge target (R148 [fable]). Rather than risk a THIRD stale-based worker round on a purely mechanical e2e-port/comment change, the orchestrator applied the fix directly on CURRENT main (36f0bd3) and verified it against the full acceptance: (1) all NINE e2e metrics listeners now bind UNIQUE ports — 9095 p2_aggregation, 9096 p3_fec, 9097 p4_adaptive, 9098 tolerant_startup, 9099 hub_failover, 9100 standby_liveness, 9101 session_established, 9102 multipeer, 9103 pacing (grep-confirmed, no shared literal); (2) a COMPLETE metrics-port registry added to netns.go enumerates all nine; (3) ALL THREE ad-hoc port-survey comments (tolerant_startup:30, hub_failover:81, multipeer:129) re-pointed at the registry — no stale port inventory remains (grep for '9095-97'/'9095-9098'/'max is 9101' returns nothing); (4) e2e-test-only. Verified: `go vet -tags e2e ./test/e2e/...`, `golangci-lint run --build-tags e2e ./test/e2e/...` (0 issues), `just lint`, `just test` all green (gofmt-clean after detaching the registry comment). Resolves D51."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T137","goals:G11","defects:D51"]
- sessionLogs: [".cq/logs/20260714-113100-a48436a017bd022d0.md"]
- rawLogs: [".cq/logs/raw/20260714-113100-a48436a017bd022d0.jsonl"]

### R168 — go-ahead

- createdAt: 2026-07-14T15:06:38.274Z
- updatedAt: 2026-07-14T15:06:38.274Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T139 review — RECONCILED APPROVE (unanimous opus+fable go-ahead). Deleted the superseded primary-only bind read seams Multipath.PathSnapshots + Multipath.FECSnapshot (no production callers since T94 migrated the device metrics adapter to PeerSnapshots) and migrated the ~9 bind test call sites (fec_test.go, traffic_test.go) to PeerSnapshots()[0].Paths/[0].FEC. The migration is PROVABLY SEMANTICS-PRESERVING (structural, not merely test-observed): Multipath embeds *peerState as the primary and the constructor sets peers:[]*peerState{primary}, so peers[0] IS the same object the deleted seams read via promotion — PeerSnapshots()[0].FEC/.Paths reads the identical slice (same path/priority order) and the same atomic FEC pointers, with verbatim-identical field mapping (PathTraffic{Name,TxBytes,RxBytes,Estimate,State}; FEC.Recovered=deliveredRecovered.Load, Unrecoverable=stats().Unrecoverable, ResidualLoss=connLoss.Loss). The honest Recovered/Unrecoverable delivered-count derivation now lives in EXACTLY ONE place (PeerSnapshots, multipath.go:~2960) — the prior 'mirrors ... verbatim' duplicate is gone (D56 drift risk eliminated). grep confirms ZERO surviving callers of the deleted methods (only 2 honest historical comments in device/metrics.go + tolerant_startup_test.go; the metrics.FECSnapshot STRUCT is correctly kept). Fresh (uncached) go build/vet/test (bind, device, metrics) + just lint all green. Surgical 3-file diff. LANDED on main at a768452."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T139","goals:G11","defects:D56"]
- sessionLogs: [".cq/logs/20260714-140500-ab95271310335d76f.md",".cq/logs/20260714-140500-ad37fb093af989592.md"]
- rawLogs: [".cq/logs/raw/20260714-140500-ab95271310335d76f.jsonl",".cq/logs/raw/20260714-140500-ad37fb093af989592.jsonl"]

## M45

### R140 — revise

- createdAt: 2026-07-14T10:58:15.676Z
- updatedAt: 2026-07-14T10:58:15.676Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T130 review round 1 — RECONCILED REVISE (strictest-wins: [fable] revise overrides [opus] go-ahead). Both reviewers agree the IMPLEMENTATION is correct and the full gate is green: strict decode (toml.NewDecoder(...).DisallowUnknownFields().Decode) genuinely rejects unknown keys, the message renders single-line dotted ('config <path>: unknown key paths.link_bandwith' / 'wireguard.peers.nane'), the 8 toml:\"-\" derived fields are inert to strict decode, errors.As detection is correct, and every accept-table config incl. wanbond.example.toml still loads (opus empirically reverted only load.go to base to confirm the new cases fail-without-fix). The REVISE is a test-coverage gap fable pinned: the two new rejects-table cases (internal/config/config_test.go:139,147) assert only the generic substring 'unknown key', NOT the offending key name — so the diff's ONLY nontrivial new logic, the unknownKeys dotted-path rendering, is UNVERIFIED (a regression returning an empty/wrong key list would still pass). The acceptance clause requires an error IDENTIFYING the unknown key."
- criticism: ["[fable] Tighten the two new rejects-table assertions to pin the rendered key: change the `want` substrings at internal/config/config_test.go:139 and :147 from the generic 'unknown key' to the specific 'unknown key paths.link_bandwith' and 'unknown key wireguard.peers.nane' respectively (both empirically verified as the actual rendered dotted paths). This operationalizes the acceptance's 'error identifying the unknown key' clause and matches the suite's existing convention of pinning identifiers (cf. config_test.go:1211 `path \"cellular\"`). Re-run `go test ./internal/config/...` to confirm green."]
- new_questions: []
- ledgerRefs: ["tasks:T130","goals:G9","defects:D41"]
- sessionLogs: [".cq/logs/20260714-105323-aa06d4feec9b3da1c.md",".cq/logs/20260714-105323-a9ffe648dbfc97572.md"]
- rawLogs: [".cq/logs/raw/20260714-105323-aa06d4feec9b3da1c.jsonl",".cq/logs/raw/20260714-105323-a9ffe648dbfc97572.jsonl"]

### R141 — go-ahead

- createdAt: 2026-07-14T11:05:01.599Z
- updatedAt: 2026-07-14T11:05:01.599Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T130 review round 2 — RECONCILED APPROVE (unanimous opus+fable go-ahead) after the round-1 revise (R140). The sole round-1 criticism is resolved: both new rejects-table cases now assert the SPECIFIC rendered dotted key ('unknown key paths.link_bandwith' and 'unknown key wireguard.peers.nane') via strings.Contains, so the unknownKeys dotted-path rendering is operationalized — the generic 'unknown key' substring no longer satisfies the assertion. fable (who raised the criticism) EMPIRICALLY re-verified by scratchpad-mutating unknownKeys to return an empty list, which makes both cases fail on the substring check, proving the rendering is genuinely exercised; the asserted paths also confirm go-toml/v2 StrictMissingError.Key() renders array-of-tables without a numeric index (paths.link_bandwith, not paths.0.link_bandwith). The strict-decode implementation (load.go: toml.NewDecoder(...).DisallowUnknownFields().Decode, errors.As on *toml.StrictMissingError, other decode errors on the %w path) is unchanged from the round-1-approved form; all accept-table configs incl. wanbond.example.toml still load; go build/vet/test ./... green. LANDED on main at 2036bba (branch implement/T130-r2, commit c590052)."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T130","goals:G9","defects:D41"]
- sessionLogs: [".cq/logs/20260714-105323-a9f20c7eca65cd28d.md",".cq/logs/20260714-105323-acf604535befc5929.md"]
- rawLogs: [".cq/logs/raw/20260714-105323-a9f20c7eca65cd28d.jsonl",".cq/logs/raw/20260714-105323-acf604535befc5929.jsonl"]

### R151 — go-ahead

- createdAt: 2026-07-14T12:00:03.962Z
- updatedAt: 2026-07-14T12:00:03.962Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T131 review — RECONCILED APPROVE (unanimous opus+fable go-ahead). All FOUR operator-facing duration knobs (scheduler.collapse_dwell/load_tau/weight_rtt_floor + fec.deadline) now accept the documented Go-duration STRING forms via the LinkRTTRaw/DNS Raw-field precedent: typed fields moved to `toml:\"-\"` (inert to the T130 strict decoder), new *Raw string fields carry the real TOML keys, parsed by parseDurations() in normalize() BEFORE applyDefaults (so empty-Raw→zero→default-fill is preserved; deriveWeightedPacingFromBDP runs earlier but reads only Path.LinkRTT, so ordering is sound), with existing range validation unchanged. Verified: the string-form matrix test asserts 2s/200ms/1ms/5ms; the 8-case rejects-table covers unparseable ('5 parsecs') + non-positive ('-1s') per knob with field-naming errors; TestExampleConfigLoads confirms the uncommented wanbond.example.toml scheduler/fec blocks load; the dropped bare-integer-nanoseconds form is now cleanly REJECTED (empirical probe: type error naming the field). fable's tree-wide grep (literal + %d-format) found ZERO remaining bare-int duration writers for these keys — the 4 e2e fixtures the worker also fixed (p3_fec/p4_adaptive/p5_dpi/wireaudit, which generate deadline TOML for real tunnel bring-up) render exact ms equivalents (100/50ms ÷ 1e6). Docs already documented the string forms (that doc-vs-code mismatch WAS D43), so no doc edit needed. go build/vet/test + go vet -tags e2e/realhosts + just lint all green. Non-blocking nit: the bare-int reject error names the internal field 'DeadlineRaw' not the TOML key (go-toml/v2 library message). LANDED on main at e6beded."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T131","goals:G9","defects:D43"]
- sessionLogs: [".cq/logs/20260714-115400-a3ce1c99e0384583a.md",".cq/logs/20260714-115400-a212c96b9b4e7bbd0.md"]
- rawLogs: [".cq/logs/raw/20260714-115400-a3ce1c99e0384583a.jsonl",".cq/logs/raw/20260714-115400-a212c96b9b4e7bbd0.jsonl"]

### R153 — revise

- createdAt: 2026-07-14T12:21:36.547Z
- updatedAt: 2026-07-14T12:21:36.547Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T132 review round 1 — RECONCILED REVISE (strictest-wins: [fable] disapprove overrides [opus] approve). Both reviewers verified the SUBSTANCE is correct and well-tested: D55 (netip.ParsePrefix every allowed_ips entry, fail-fast at load naming the peer index/name + offending string) and D59 (at most one default-route peer; a 0.0.0.0/0 or ::/0 entry at most once per address family both within and across peers) are enforced in Config.validate() with reject-table + direct-validate() tests; the ordering is safe (edgeTwoPeersConfig carries no mode, so the default-route pre-pass is inert and the pre-existing edge single-peer-cap 'concentrator-only' message is unchanged); and fable's 11 adversarial over-rejection probes ALL pass (shared non-/0 CIDR across peers, dual-family /0 on one peer, cross-family /0 across peers, v6-only ::/0 edge peer, bare-IP correctly rejected, non-canonical 1.2.3.4/0 = v4 default) — only literal /0 exclusivity is enforced, not general overlap. build/vet/test + just lint green. The REVISE is a same-change docs-sync gap fable caught:"
- criticism: ["[fable] internal/device/device.go: the doc comments on splitDefaultRoute (:1076 'allowed_ips carries no syntax validation upstream') and defaultRoutePrefixes (:1097 'allowed_ips carries no upstream syntax validation, matching splitDefaultRoute's own tolerance') now assert a FALSE invariant — this change makes config.validate() parse-validate every allowed_ips entry at load, so an unparseable entry can no longer reach either function. Per the repo's docs-in-sync-in-the-same-change rule, update both comments to state the new invariant (entries are guaranteed parseable post-D55; the parse-failure branches are defensive-only). [RESOLVED inline — see R154.]"]
- new_questions: []
- ledgerRefs: ["tasks:T132","goals:G9","defects:D55","defects:D59"]
- sessionLogs: [".cq/logs/20260714-120400-adee6f071f63fe512.md",".cq/logs/20260714-120400-afcec1c5e8be9f846.md"]
- rawLogs: [".cq/logs/raw/20260714-120400-adee6f071f63fe512.jsonl",".cq/logs/raw/20260714-120400-afcec1c5e8be9f846.jsonl"]

### R154 — go-ahead

- createdAt: 2026-07-14T12:21:43.973Z
- updatedAt: 2026-07-14T12:21:43.973Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T132 round 2 — GO-AHEAD (orchestrator-resolved the single round-1 docs-sync criticism inline). The R153 [fable] criticism was a purely mechanical 2-comment doc-sync fix in a file T132 did not otherwise touch (internal/device/device.go). The orchestrator updated both stale comments on CURRENT main: splitDefaultRoute and defaultRoutePrefixes no longer claim 'allowed_ips carries no (upstream) syntax validation' — they now state that config.validate() parse-validates every allowed_ips entry at load (T132/D55), so an unparseable entry can no longer reach either function and the parse-failure branches are DEFENSIVE-only. Verified: `grep 'no (upstream) syntax validation' internal/device/device.go` returns nothing; go build/vet + go test ./internal/config/... ./internal/device/... + just lint all green. The T132 substance (D55 CIDR validation + D59 /0 exclusivity, unanimously approved) LANDED at 24714b3; the doc-sync fix at 74f38b7. Resolves D55+D59; completes goal G9 (T130+T131+T132 all done)."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T132","goals:G9","defects:D55","defects:D59"]
- sessionLogs: [".cq/logs/20260714-120400-afcec1c5e8be9f846.md"]
- rawLogs: [".cq/logs/raw/20260714-120400-afcec1c5e8be9f846.jsonl"]

## M50

### R155 — revise

- createdAt: 2026-07-14T12:48:57.476Z
- updatedAt: 2026-07-14T12:48:57.476Z
- author: "opus-4.8[1m]"
- session: 915ea040-10d3-4f13-9cf2-ed8e5149babb
- summary: "revise (strictest-wins over opus+fable panel): plan is well-grounded, acyclic, and complete for the Q51-Q55 scope without re-owning G2/Q20, but 4 planner-fixable faults survive — T143 rests on a false grounding claim (the aggregation-change log already exists) and T143/T147 assert a non-existent log string; T144 is missing a dependsOn edge and leaves the partial-declaration verdict unspecified. 0 out-of-scope defects filed by either reviewer."
- new_questions: []
- criticism: ["[opus] T143 grounding is factually WRONG against the repo: updateGateLocked (internal/sched/weighted.go:499-532) does NOT 'flip the gate silently' — it ALREADY emits s.log.Info(\"scheduler aggregation change\", \"to\", \"aggregating\"|\"collapsed\", \"load_fps\", s.loadRate) on every s.aggregating flip (lines 506/514/526). A literal implementation of T143 ('emit an INFO transition log on every flip' as if none exists) DOUBLE-LOGS every engage/disengage. Fix T143 to EXTEND the existing 'scheduler aggregation change' record (add the missing from + engage_threshold_fps + disengage_threshold_fps structured fields) rather than introduce a new log line.","[opus] T143 and T147 acceptance assert the exact log strings 'scheduler aggregation engaged' / 'scheduler aggregation disengaged', but the code's existing message is 'scheduler aggregation change' with to=\"aggregating\"|\"collapsed\". Pin the SINGLE canonical message string in T143 and align T143/T147's log-grep assertions to it (preserving the setActiveLocked coalesce-on-change parity).","[fable] T144's acceptance asserts via 'the log capturer' — a T141 deliverable — but its dependsOn lists only T142 and the grounding declares M52 an INDEPENDENT root; if T144 dispatches while T141 is in flight the acceptance is unimplementable as written. Add T141 to T144's dependsOn, OR reword the acceptance to assert on the existing startProc combined-output (proc.log()) so M52 stays independent.","[fable] T144's capacity-sanity verdict is unspecified for a reachable state: with pacing DISABLED, deriveWeightedPacingFromBDP no-ops (config.go:957), so a PARTIAL declaration (link_bandwidth on some paths, not all) reaches the guard, yet T144 defines the gauge only for 'every path declares'(1) and 'no bandwidth declared'(0). Pin the partial case (e.g. WARN + wanbond_weighted_capacity_sane=0 while T142's guard still checks the declared paths) so the T142 and T144 workers cannot resolve it inconsistently."]
- ledgerRefs: ["goals:G13"]
- sessionLogs: [".cq/logs/20260714-124714-a81dc426b159d4381.md",".cq/logs/20260714-124714-af7800def9e0eca35.md"]
- rawLogs: [".cq/logs/raw/20260714-124714-a81dc426b159d4381.jsonl",".cq/logs/raw/20260714-124714-af7800def9e0eca35.jsonl"]

### R156 — go-ahead

- createdAt: 2026-07-14T12:53:19.521Z
- updatedAt: 2026-07-14T12:53:19.521Z
- author: "opus-4.8[1m]"
- session: 915ea040-10d3-4f13-9cf2-ed8e5149babb
- summary: "GO-AHEAD (round 2, reconciled: opus go-ahead + fable go-ahead, unanimous). All 4 R155 criticisms verified resolved against the revised tasks AND live source: (C1) T143 now EXTENDS the existing weighted.go:506/514/526 'scheduler aggregation change' record with from/engage_threshold_fps/disengage_threshold_fps and forbids a second log line (no double-log); (C2) T143 & T147 acceptance assert the CANONICAL 'scheduler aggregation change' (to=aggregating|collapsed) string, not the fictional 'engaged/disengaged'; (C3) T144 reworded to assert on the daemon's own startProc proc.log() combined output, dependsOn stays [T142] so M52 remains an independent root; (C4) T144 pins the partial-link_bandwidth-declaration verdict (WARN + wanbond_weighted_capacity_sane=0 while T142 still hard-fails contradicting declared paths). No new faults; DAG re-verified acyclic (roots T141/T142 -> T143/T144/T145 -> T146 -> T147 -> T148) and complete for Q51-Q55 (harness T141, guard T142/T144, observability T143/T146/T147, probe T145, docs T148), Q53 G2-boundary preserved, Q55 e2e binding respected (T148 the one declared docs deviation). 0 out-of-scope defects. Review history: R155 (revise, 4 criticisms) -> R156 unanimous go-ahead."
- new_questions: []
- criticism: []
- ledgerRefs: ["goals:G13"]
- sessionLogs: [".cq/logs/20260714-125245-ae05184c487158ddc.md",".cq/logs/20260714-125245-a943cee6cd9b93e72.md"]
- rawLogs: [".cq/logs/raw/20260714-125245-ae05184c487158ddc.jsonl",".cq/logs/raw/20260714-125245-a943cee6cd9b93e72.jsonl"]

## M56

### R162 — revise

- createdAt: 2026-07-14T13:24:51.758Z
- updatedAt: 2026-07-14T13:24:51.758Z
- author: "opus-4.8[1m]"
- session: 7295f080-20fa-4cf9-afac-0357b4cf65cb
- summary: "Reconciled (opus+fable, strictest-wins): REVISE. Plan is well-grounded, fine-grained, correctly sequenced, testable, and covers both D65 halves — but 4 autonomously-fixable criticisms, including one real design defect (bottleneck BDP sizing is wrong for active-backup's one-active-path model)."
- new_questions: []
- criticism: ["[fable] DESIGN DEFECT — T152/T154 carry weighted's BOTTLENECK BDP sizing into active-backup. Weighted shares one PerPathCapacity scalar across simultaneously-striped paths (config.go:952-953); active-backup egresses on ONE path at a time and T150 gives it per-path buckets. Sizing every bucket at the SLOWEST declared link's rate over-throttles a faster active primary (e.g. a Starlink primary paced to the 5G backup's declared rate) — reimposing the exact artificial single-flow ceiling this goal removes. FIX: under active-backup, size each path's bucket from ITS OWN link_bandwidth/link_rtt (per-path capacities in sched.Config, plumbed through T153), or record an explicit justification for bottleneck sizing; change T154's weighted-parity table test to per-path parity accordingly.","[opus+fable] T150 under-scopes the stale doc-comment corrections its own change forces. Beyond the interface comment at internal/sched/scheduler.go:59-62, adding pacing to ActiveBackup ALSO falsifies (a) scheduler.go:16-20 — the PickPaced constant doc ('Only a pacing-enabled weighted scheduler ever returns it'); and (b) internal/sched/active_backup.go:176-179 — ActiveBackup.Pick's doc ('class is ignored: active-backup has no pacer'). `just lint` cannot catch these (they stay grammatically well-formed). Broaden T150's acceptance to correct ALL THREE comments.","[opus] T150 keeps per-path buckets consistent via AddPath/RemovePath but OMITS SetPaths (internal/sched/active_backup.go:143), the Close→Open / T30 durable-membership path that replaces s.health wholesale. If the bucket slice is not resized/reset in SetPaths (and initialized in NewActiveBackup, mirroring WeightedScheduler's fullBuckets init), a Close→Open that changes the path count leaves tokens[] mis-length and the next Pick indexes out of range (panic). Add SetPaths + the NewActiveBackup bucket init to T150's bucket-consistency scope, and add a Close→Open-with-different-path-count regression case to T151.","[fable] T152/T155 leave contradictory weighted-only claims un-enumerated: config.go SchedulerConfig doc (158-161), Path.LinkBandwidthBitsPerSec (496-501) and Path.LinkRTT (507-510), install.md §3z's [scheduler] block ('Every knob below applies ONLY to weighted; under active-backup they are inert') and its per-key comments, and wanbond.example.toml's mirrored comments — all assert weighted-only pacing. T155's grep acceptance checks only PRESENCE of new statements; add an ABSENCE check (no remaining 'only under weighted' pacing claims) and name these sites in T152/T155."]
- ledgerRefs: ["goals:G14"]
- sessionLogs: [".cq/logs/20260714-132348-a4817b0c9903be5f5.md",".cq/logs/20260714-132348-a2ee651df649727fc.md"]
- rawLogs: [".cq/logs/raw/20260714-132348-a4817b0c9903be5f5.jsonl",".cq/logs/raw/20260714-132348-a2ee651df649727fc.jsonl"]

### R163 — go-ahead

- createdAt: 2026-07-14T13:34:16.102Z
- updatedAt: 2026-07-14T13:34:16.102Z
- author: "opus-4.8[1m]"
- session: 7295f080-20fa-4cf9-afac-0357b4cf65cb
- summary: "Round 2 reconciled (opus+fable, both go-ahead): GO-AHEAD. All 4 R162 criticisms verified resolved with line-level repo grounding — per-path (not bottleneck) BDP sizing correct for active-backup's one-active-path model, all three stale sched doc comments enumerated, SetPaths+NewActiveBackup bucket init with a Close→Open regression case, and enumerated weighted-only doc sites plus a file-wide absence grep. Plan is fine-grained, sequenced, testable, grounded, and complete across both D65 halves. No new blocking issues."
- new_questions: []
- criticism: []
- ledgerRefs: ["goals:G14"]
- sessionLogs: [".cq/logs/20260714-133358-a65ef5ef060923e4e.md",".cq/logs/20260714-133358-a75a6833b6ede96b3.md"]
- rawLogs: [".cq/logs/raw/20260714-133358-a65ef5ef060923e4e.jsonl",".cq/logs/raw/20260714-133358-a75a6833b6ede96b3.jsonl"]

## M43

### R164 — go-ahead

- createdAt: 2026-07-14T13:37:34.038Z
- updatedAt: 2026-07-14T13:37:34.038Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T126 review — RECONCILED APPROVE (unanimous opus+fable go-ahead). The device wires a LEVEL-triggered concentrator per-peer teardown monitor (perPeerHandshakeNano parses the UAPI dump per-peer; peerTeardownMonitor level-checks each configured non-primary peer every poll: established := nano!=0 && age<=RejectAfterTime, and calls the idempotent Bind.TearDownPeer(name) on any not-established peer) to Bind.TearDownPeer (D50 — previously uncalled, leaking dead-peer resequencer rings/FEC buffers/demux slots). Both reviewers verified: (a) the NEVER-HANDSHAKED reclaim (last_handshake=0, instantiated via PROBE, no 1→0 edge ever) IS torn down by the level check (TestPeerTeardownNeverHandshaked); (b) a LIVE peer is untouched across repeated polls; (c) the pubkey→configured-name mapping is correct end-to-end (cfg.PeerIdentities() order matches cfg.WireGuard.Peers index-for-index; AddConcentratorPeer registers under id.Name; TearDownPeer keys peersByName) and idempotent-safe (refuses live+primary); (d) the SINGLE-PEER path is BYTE-IDENTICAL — concentratorMonitoredPeers returns nil for len(peers)<=1, startPeerTeardownMonitor spawns NO goroutine, sessionMonitor untouched; the RejectAfterTime threshold matches sessionMonitor; a mid-first-handshake sweep is safe (teardownPeerLocked refuses StateUp probers, the next authenticated PROBE re-instantiates — bind-tested); churn is bounded (bind-side live-refusal + T123 lifecycleMu ordering); the monitor is stopped in Close (sync.Once) BEFORE dev.Close, race-clean. Acceptance (a)-(c) covered by new device tests, (d) by the existing bind lifecycle test. go build/vet/test + -race ./internal/device/... ./internal/bind/... + just lint all green. LANDED on main at df4651c."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T126","goals:G8","defects:D50"]
- sessionLogs: [".cq/logs/20260714-133500-a76410870f2fdd4fd.md",".cq/logs/20260714-133500-a4f63c99c44d4ebb6.md"]
- rawLogs: [".cq/logs/raw/20260714-133500-a76410870f2fdd4fd.jsonl",".cq/logs/raw/20260714-133500-a4f63c99c44d4ebb6.jsonl"]

### R178 — revise

- createdAt: 2026-07-14T16:11:45.492Z
- updatedAt: 2026-07-14T16:11:45.492Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T127 review round 1 — RECONCILED REVISE (strictest-wins: [fable] disapprove overrides [opus] approve), REMEDIATED INLINE by orchestrator (both criticisms were trivial doc-sync textual fixes on a DUAL-APPROVED core; T137 escape-hatch precedent). BOTH reviewers verified the CORE fix is sound + green and INDEPENDENTLY reproduced the pre-fix D58 failure: SetPrimaryPeerName (multipath.go:901-918) re-keys peersByName from '' under m.mu, refuses ''/post-Open/already-registered; device.Up calls it before the AddConcentratorPeer loop gated on len(ids)>1, so collision checks (name==m.name :874 + peersByName dup :877) see the FINAL name (proven by TestSetPrimaryPeerNameRenamesPrimaryAndKeepsCollisionCheckCorrect, both orders); single-peer keeps peer='' (TestExpositionSinglePeerByteCompatible, T94); teardownPeerLocked refuses on IDENTITY p==m.peerState (:1871) not name; repro: excising the device.Up call makes TestUpTwoPeerConcentratorWiresPerPeerState fail 'primary bound peer name = \"\", want \"edge-0\"'; TestExpositionTwoPeerSeries asserts two distinct non-empty labels; build/vet/test + -race(bind/device/metrics) + just lint all green. [fable] DISAPPROVE for 2 DOC-SYNC misses the acceptance's repo-wide grep should have caught: (1) wanbond.example.toml:189-193 still documented the OLD peer='' behavior as 'documented current behavior; not fixed here' — a shipped user-facing example CONTRADICTING the merged code; (2) test/e2e/restart_onesided_test.go:410-422 r121PeerCounter comment claimed a peer='' multi-peer series exists (false post-D58). ORCHESTRATOR REMEDIATION (commit 316ab81): rewrote the wanbond.example.toml block to the corrected rule (every bound peer incl. first-configured carries its name once >1 peer; peer='' only in true single-peer exposition); corrected the r121PeerCounter comment AND dropped the now-dead PeerValue(name,'') first-try (behavior-preserving — this survivor is single-peer so it always fell through to Value(name); the probe can never match post-D58) + fixed its Fatalf message. Merged to main as 3ceef3d (T127 core, clean cherry-pick, docs/install.md+config.go 3-way auto-merged) + 316ab81 (doc-sync). Full gate re-run green on composed main: bind/device/metrics tests ok; e2e compiles; just lint 0 issues default+e2e+realhosts. 2 criticisms (both remediated inline) / 0 questions / 0 defects."
- criticism: ["[fable, REMEDIATED 316ab81] wanbond.example.toml:189-193 documented the pre-fix peer='' primary behavior as current ('not fixed here') — rewritten to the D58-fixed rule.","[fable, REMEDIATED 316ab81] test/e2e/restart_onesided_test.go r121PeerCounter comment asserted a peer='' multi-peer series that no longer exists post-D58 — comment corrected and the now-dead PeerValue(name,'') probe dropped (behavior-preserving for the single-peer survivor)."]
- new_questions: []
- ledgerRefs: ["tasks:T127","goals:G8","defects:D58"]

## M58

### R172 — go-ahead

- createdAt: 2026-07-14T15:43:15.179Z
- updatedAt: 2026-07-14T15:43:15.179Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T158 review round 1 — GO-AHEAD (single opus reviewer, proportionate for a docs-only change with grep-deterministic acceptance). Forwarded-TCP MSS-clamp operator recipe (D65 secondary fix). Merged to main as 0414854 (cherry-pick of ee3cca8 onto 972d84d; docs/install.md + docs/runbook.md only, clean). Reviewer verified EVERY acceptance clause: both clamp rules (iptables + ip6tables, `-t mangle -A FORWARD -o wanbond0 -p tcp --tcp-flags SYN,RST SYN -j TCPMSS --clamp-mss-to-pmtu`) present in install.md §9.2 AND runbook.md, byte-verbatim vs docs/p1-mtu.md canonical; --clamp-mss-to-pmtu-vs---set-mss rationale present+correct (tracks live inner MTU); D65 fragment/PMTU-blackhole risk stated in both; arithmetic (IPv4 1401-40=1361, IPv6 1381-60=1321) matches p1-mtu.md and p1-mtu.md UNCHANGED; both edge+concentrator named as clamp points; explicitly operator-owned (daemon installs no firewall/mangle rules); p1-mtu.md links + accurate persistence notes (edge→addressing oneshot, concentrator→netfilter-persistent save) in both files; docs-only diff, no production code; just lint green (0 issues all tag sets). 0 criticisms / 0 questions / 0 defects."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T158","goals:G14","defects:D65"]

### R176 — go-ahead

- createdAt: 2026-07-14T16:00:26.162Z
- updatedAt: 2026-07-14T16:00:26.162Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T159 — GO-AHEAD (orchestrator-applied inline, escape-hatch per the T137 precedent for a trivial docs task whose target file is NOT in any git worktree). wanbond-fixes.md is an UNTRACKED (but not gitignored) project deploy-notes/defect-analysis doc referenced by tracked tasks as the D65 root-cause notes; a worktree-isolated worker cut from HEAD cannot see it, so the orchestrator edited it directly in the main checkout and self-verified against acceptance. Added to C3 (Full-tunnel / route-a-client-LAN recipe) a 'Required step (closes the D65 compounding fault)' naming the TCPMSS --clamp-mss-to-pmtu mangle FORWARD rule (iptables+ip6tables) on BOTH forwarding nodes, pointing at the now-merged docs/install.md §9.2 (+ runbook.md) rather than duplicating rule syntax, with a docs/p1-mtu.md cross-ref for the MSS=1361/1321 arithmetic. Acceptance verified by grep: C3 names the --clamp-mss-to-pmtu FORWARD rule as a required step AND references install.md §9.2 + D65. just lint is Go-only in this repo (no markdown linter) so the markdown-only change trivially passes. Merged as 479a231. NOTE FOR USER: this commit NEWLY TRACKS the previously-untracked wanbond-fixes.md — if you intended it to stay a local scratch doc, `git rm --cached wanbond-fixes.md` reverts the tracking while keeping the file. 0 criticisms / 0 questions / 0 defects."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T159","goals:G14","defects:D65"]

## M57

### R174 — go-ahead

- createdAt: 2026-07-14T15:53:36.335Z
- updatedAt: 2026-07-14T15:53:36.335Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T149 review round 1 — RECONCILED GO-AHEAD (opus + fable panel, strictest-wins; BOTH approve). Pure behavior-preserving extraction of the WeightedScheduler token-bucket pacer into new internal/sched/pacer.go (caller-locked `pacer` type + pacerConfig). Merged to main as f387831 (cherry-pick of 88ba71c onto f387831-parent e569d48; internal/sched/{pacer.go,weighted.go} only, clean). BOTH reviewers verified ZERO behavioral change: weighted_test.go BYTE-IDENTICAL (empty diff) and green unchanged; pacer is caller-locked (no internal mutex; every call under s.mu at the identical pre-refactor site; -race clean); anonymous embedding shadows correctly (outer cfg/log win at depth 0 per Go shallowest-depth; no method collision — old refillLocked/tryConsumeLocked/shedLocked renamed, addPath/removePath lowercase ≠ exported AddPath/RemovePath; no exported-surface change); shed-log message/fields(shed_frames+load_fps)/1s cadence, refill seeding+burst-clamp, tryConsume order, fullBuckets, PickNone=-1/PickPaced=-2 sentinels, ClassControl D22 exemption, tokens[] index alignment ALL unchanged. FABLE ran a differential overload test at BOTH base 972d84d and head 88ba71c — BIT-IDENTICAL trajectories (11997/15000=0.800 sheds, 3 coalesced log emissions/3s, shedCount reset at each boundary, tokens never exceed burst). Snapshot-vs-live hazard (pacerConfig snapshots cfg at construction vs old live s.cfg reads) proven INERT (no post-construction cfg mutation in the package). pacer.go standalone-reusable for ActiveBackup (depends only on time/log/PickPaced; loadFPS is a param). Full gate green on composed main: go build/vet/test sched ok; just lint 0 issues default+e2e+realhosts. 0 criticisms / 0 questions / 0 defects."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T149","goals:G14","defects:D65"]

### R181 — revise

- createdAt: 2026-07-14T16:35:46.568Z
- updatedAt: 2026-07-14T16:35:46.568Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T150 review round 1 — RECONCILED REVISE (strictest-wins: [fable] disapprove overrides [opus] approve). CORE VERIFIED SOUND by both: per-path (NOT bottleneck) BDP sizing proven (primary admits ~1008 = own cap*T+burst, strictly above the 208 backup-rate bound — a fast primary is NOT capped at the slow backup); ClassControl exempt (admitted on empty bucket, D22); PickPaced(-2) distinct from PickNone(-1); middle-path RemovePath keeps the survivor on its OWN bucket (index-aligned, token state travels); pacing-off byte-compat; fail-fast validation; reproduce-first re-verified by surgically neutering Pick (5000/5000 admitted, no shedding, for the right reason); 3 enumerated doc comments corrected; build/vet/test + -race + just lint green. But [fable] DEMONSTRATED a PANIC in the task's EXPLICIT no-panic-across-membership-churn hazard — REVISE round 2."
- criticism: ["DEMONSTRATED PANIC (deterministic) in the required membership-churn hazard class: with Pacing=true, RemovePath MAY empty the path set (the DynamicScheduler contract only requires i in range; the pre-pacing ActiveBackup tolerated remove-to-empty — Pick returns -1 fine). AFTER emptying, (a) AddPath panics 'index out of range [-1]' at internal/sched/active_backup.go:181 (s.pacers[len(s.pacers)-1] on an EMPTY slice), and (b) SetPaths panics at internal/sched/active_backup.go:271 (old[len(old)-1] in resizeActiveBackupPacers). Exact repros: NewActiveBackup([1 path], Pacing=true) -> RemovePath(0) -> AddPath(h); and NewActiveBackup([1 path], Pacing=true) -> RemovePath(0) -> SetPaths([h,h]). The comments claiming 'pacing implies >=1 path is an invariant' are FALSIFIED by the type's own API. Production is unexposed today ONLY because device.go:887 never sets Pacing=true, but panic-free churn is this task's stated REQUIRED hazard. FIX: when the pacer slice is empty, seed the new bucket from cfg.PerPathCapacities/PacingBursts tail (guaranteed non-empty whenever Pacing is on — the constructor validates len==len(health)>=1), OR enforce the >=1-path invariant in RemovePath; ADD a remove-to-empty-then-AddPath/SetPaths regression test.","A FOURTH stale doc comment of the exact genre the task targeted was MISSED: internal/sched/scheduler.go:34-36 (ClassData const doc) still scopes data pacing to 'under a weighted scheduler with pacing enabled it is subject to the per-path token buckets and is shed (PickPaced)…' — active-backup with pacing now ALSO sheds ClassData; reword to 'any pacing-enabled scheduler'."]
- new_questions: []
- ledgerRefs: ["tasks:T150","goals:G14","defects:D65"]

### R183 — go-ahead

- createdAt: 2026-07-14T16:55:34.100Z
- updatedAt: 2026-07-14T16:55:34.100Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T150 review round 2 — GO-AHEAD (single fable reviewer — it demonstrated the round-1 panic, proportionate; round-1 core dual-verified). Both round-1 criticisms RESOLVED: C1 (PANIC) — the two regression tests encode fable's exact repros (RemovePath(0)-to-empty then AddPath / SetPaths), mutation-verified to panic 'index out of range [-1]' at AddPath and resizeActiveBackupPacers:271 against the round-1 tip, PASS with the fix (incl. post-regrow Pick burst). Config-tail seeding (new Config.tailPacerConfig) SCRUTINIZED: engages ONLY when the live pacer slice is EMPTY (all paths removed — no per-path correspondence survives by construction); 'remove path 0 of 3 then re-grow' never reaches the fallback (2 live pacers remain, round-1 live-tail inheritance applies); fallback read is safe (newActiveBackupPacers fails fast unless len(PerPathCapacities)==len(PacingBursts)==len(health)>=1 when Pacing on; NewActiveBackup rejects empty health; s.cfg stored by value, never reassigned) — capacity never zero/garbage. THIRD-SITE HUNT: Pick guards s.active<0; RemovePath compaction bounds-safe; a 50-seed × 400-op randomized churn (add/remove/setpaths/pick/recompute, repeatedly draining to empty + re-growing, heterogeneous caps) under -race found NO third index site (len(pacers)==len(health) + positive configs asserted after every op). C2 (4th doc comment) — scheduler.go PickPaced/ClassData/interface-class + active_backup.go Pick doc all corrected; no 5th stale comment. Merged to main as ee95d4c (round-1 core) + 8c86bc3 (round-2) via clean cherry-pick of 6e26127..11a6849 onto 9925b97 (internal/sched only, no conflicts). Full gate green on composed main: sched tests + -race ok; just lint 0 issues default+e2e+realhosts. 0 criticisms / 0 questions / 0 defects. NOTE: active-backup pacing is the SCHED FOUNDATION — device.go still constructs it with Pacing=false (config-wiring is a follow-up), so the churn-panic path is not production-reachable today but is now panic-safe for when it is wired."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T150","goals:G14","defects:D65"]

### R184 — go-ahead

- createdAt: 2026-07-14T17:20:12.706Z
- updatedAt: 2026-07-14T17:20:12.706Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T151 review round 1 — GO-AHEAD (single opus reviewer, proportionate for a TEST-ONLY additive-coverage task). 6 new active-backup pacing scenarios added to internal/sched/active_backup_test.go (+406 lines, ZERO production code). Merged to main as f9b2836 (clean cherry-pick of a56f608 onto b6f5ad7; sched test file only). Reviewer verified NON-VACUITY against the real token-bucket logic: (a) pacing-disabled asserts admitted==5000 & each Pick got==0 (would return PickPaced=-2 if pacing-off shed); (b) failover asserts primaryAdmitted==5, backupAdmitted==20 with window bound >primaryUpper(25) AND <=backupUpper(820) — the admitted-starts-at-1 off-by-one (failover Pick consumes the first token) is arithmetically correct; (c) PickPaced(-2)/PickNone(-1) distinct in the right situations; (d) ClassControl exemption asserts haveFill==false cold-start + tokens[0] unchanged across control Picks (catches a charged ClassControl); (e) burst absorption admitted==burst, shed==0 + refill-cap; (f) SetPaths membership-count-change asserts len(s.pacers)==membership count after grow(3)/shrink(1) AND admitted==tailBurst (count-bound 'paces on new membership', not mere no-panic). DISTINCT from T150's homogeneous FailoverUsesOwnBucket + SetPathsResizeNoPanic — strengthens both. Full gate green: build/vet/test + -race + just lint 0 issues default+e2e+realhosts. 0 criticisms / 0 questions / 0 defects."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T151","goals:G14","defects:D65"]

## M52

### R175 — go-ahead

- createdAt: 2026-07-14T15:54:28.347Z
- updatedAt: 2026-07-14T15:54:28.347Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T142 review round 1 — RECONCILED GO-AHEAD (opus + fable panel, strictest-wins; BOTH approve). Config-load hard-fail guard: under policy=weighted, a path declaring link_bandwidth whose impliedCapacityFPS = LinkBandwidthBitsPerSec/(8*defaultAvgWireFrameBytes) is below EngageFraction*PerPathCapacityFPS makes Load fail fast. Merged to main as 6f906b3 (cherry-pick of 3298e5d onto f387831; docs/install.md auto-merged with T134/T158 sections, no conflicts). BOTH reviewers verified: (1) CONSTANT CONSISTENCY — guard math byte-identical to SizePacingFromBDP with the SAME defaultAvgWireFrameBytes=1500 (config.go:1134 vs :262), so guard and BDP-derive cannot disagree; (2) ordering — Load→normalize(deriveWeightedPacingFromBDP+applyDefaults)→validate; guard in Config.validate after Scheduler.validate on EFFECTIVE values; (3) boundary strict `>` allows exact equality (proven binary-exact 0.5*1000==500 AND decimal-inexact fl(0.9*1000)==900); (4) NO FALSE TRIP in pacing-enabled mode — derive sizes PerPathCapacityFPS to the bottleneck so threshold=EngageFraction(≤1)*bottleneck ≤ implied for every declared path; FABLE empirically probed engage_fraction=1.0 + pacing-enabled and confirmed no fire; (5) error names path + declared bw + implied fps + engage-threshold fps + all three fixes; (6) FIXTURE BUMP 50/10→150/120 Mbit in TestPacingDisabledLeavesDerivationInert LEGITIMATE (old 10Mbit→833fps<9000 threshold now an intentionally-forbidden config; assertions/intent unchanged); (7) REPRO-FIRST confirmed — removing the guard call makes TestWeightedEngageAgainstBandwidthRefuses fail for the right reason; (8) docs/install.md config-error entry accurate. FABLE ran 7 edge probes (multi-path bottleneck naming, undeclared-path skip, float boundary, unit spellings 8mbit/8Mbit/8000kbit/8mbps all→666.7, extreme 100kbit vs 1e9fps no Inf/NaN/div0). Acceptance (i) refuses-to-start e2e RAN GREEN unprivileged (exit 1, all markers); (ii) tunnel-establish needs /dev/net/tun — compiles/vets/lints, DEFERRED to hardware. Full gate green on composed main: go build/vet/test config ok; e2e compiles; just lint 0 issues default+e2e+realhosts. NON-BLOCKING (opus): exactly-equal boundary not explicitly unit-tested (the `>` is correct by inspection; pass-when-lowered case is strictly-below so wouldn't catch a `>=` regression) — candidate hardening, not a defect. 0 criticisms / 0 questions / 0 defects."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T142","goals:G13"]

### R180 — go-ahead

- createdAt: 2026-07-14T16:35:22.734Z
- updatedAt: 2026-07-14T16:35:22.734Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T144 review round 1 — RECONCILED GO-AHEAD (opus + fable panel, strictest-wins; BOTH approve). Startup WARN + static unlabeled wanbond_weighted_capacity_sane gauge (Q52 soft verdict). Merged to main as c31e792 (cherry-pick of implement/T144 ab06cf9 onto 5e645e4; internal/metrics/metrics_test.go + device/config/docs 3-way auto-merged with T127, no conflicts). BOTH reviewers verified the FULL verdict matrix EMPIRICALLY: {active-backup}→nil/family-ABSENT; {weighted, all declared}→true/gauge1/no-WARN; {weighted, none declared}→false/gauge0/one-WARN; {weighted, PARTIAL, pacing disabled}→false/gauge0/one-WARN (R155 partial case pinned); {weighted, all declared, pacing ENABLED}→still true, no false WARN; {weighted, partial, pacing ENABLED}→hard-fails in deriveWeightedPacingFromBDP before the soft verdict. T142 ORDERING confirmed: Load→normalize()computes verdict→validate()runs the hard guard, so a declared-CONTRADICTING path still aborts Load (TestWeightedCapacitySaneDeclaredPathStillHardFails names the path). STARTUP NEVER BLOCKS: warnUnverifiableWeightedCapacity returns void, called EXACTLY ONCE in run() never in reloadTunnel (e2e asserts count==1). GAUGE static+unlabeled prometheus.NewGauge, does NOT touch NewCollector's multiPeer label decision, registered only when verdict non-nil (nil→absent); exposition byte-identical modulo the new unlabeled family for single AND two-peer sources (probed). All 3 NewServer(*bool) call sites updated. reloadWarnings catch-all correctly zeroes the derived field + reflection test adds it to known, still pinning every other field. Metric name exported MetricWeightedCapacitySane. All FOUR acceptance e2e cases PASS on llm-ubuntu-0 hardware (real /dev/net/tun, root). Full gate green on composed main: config/device/metrics tests ok; e2e compiles; just lint 0 issues default+e2e+realhosts. Docs synced (README + design.md + install.md §3a). Fable filed 2 out-of-scope defects: reload verdict staleness → D74 (new); load.go build-tags → already D73. 0 criticisms / 0 questions."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T144","goals:G13","defects:D74","defects:D73"]

## M51

### R177 — go-ahead

- createdAt: 2026-07-14T16:04:57.020Z
- updatedAt: 2026-07-14T16:04:57.020Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T141 review round 1 — GO-AHEAD (single opus reviewer, proportionate for additive test-harness helpers with NO production code; worker independently executed the privileged e2e self-test on llm-ubuntu-0 hardware). Added test/e2e/load.go (DriveUDPLoad rate-calibrated UDP sender+sink, MetricsSampler polling scraper, ParseLogLines/AwaitLogLine structured-log capturer) + load_self_test.go (TestLoadDriverSelfTest) + a one-line netns.go registry doc comment. Merged to main as b0f52e4 (cherry-pick of implement/T141 4cac05d onto 88a8ba2; test/e2e/ only, clean). Reviewer verified: (1) SCOPE clean — diff confined to test/e2e/, no internal/ or production code; (2) DefaultPaths + TestFixtureImpairment BYTE-IDENTICAL (extend-not-modify honored); (3) helpers sound + race-free — DriveUDPLoad paces via fixed-interval ticker (1s/TargetFPS), MetricsSampler retains under mutex + joins goroutine on idempotent Stop registered as t.Cleanup, AwaitLogLine bounded-deadline (no hang), sink subprocess SIGTERM→wait→Kill via t.Cleanup; (4) self-test NON-VACUOUS — asserts achieved fps AND wire tx_bytes delta within ±20% requiring ≥2 samples + present wanbond_path_up gauge, awaits the coalesced 'scheduler pacer shedding' record under 3000fps overload + bring-up 'path liveness transition'; (5) ±20% ARITHMETIC validated against REAL code — txBytes counts the OUTER wanbond frame (multipath.go:2062), ~88B/frame overhead at 1200B payload = ~7.3% << 20% band, 380fps target sits above 360fps engage yet below 400fps pacing capacity so nothing sheds and sent-vs-tx is well-defined; (6) netns.go 9105 loadSelfTestMetricsListen comment accurate. WORKER RAN IT ON HARDWARE (llm-ubuntu-0): both subtests pass (376.8fps vs 380 off 0.8%, tx_bytes delta 6.2%, shed_frames observed live). Full gate green on composed main: go build/vet ok; e2e -count=0 compiles; just lint 0 issues default+e2e+realhosts. This is the up-front fixture dependency for the downstream observability/probe-protection e2e tasks (G13). 0 criticisms / 0 questions / 0 defects."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T141","goals:G13"]

## M53

### R179 — revise

- createdAt: 2026-07-14T16:26:45.363Z
- updatedAt: 2026-07-14T16:26:45.363Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T143 review round 1 — RECONCILED REVISE (strictest-wins: [fable] disapprove overrides [opus] approve). CORE VERIFIED SOUND by both: AggregationSnapshot reads all 4 fields under s.mu with threshold formulas byte-identical to updateGateLocked's engage/disengage locals (no metrics lie); the 'scheduler aggregation change' record was EXTENDED in place (from/engage_threshold_fps/disengage_threshold_fps), exactly one log call per flip in mutually-exclusive returning branches, one-shot-on-change preserved under a 2000-Pick saturated loop; fable MUTATION-verified the no-double-log test bites (duplicating the engage log call fails it 'got 2 want 1') and ran an 8-goroutine snapshot-vs-Pick -race probe clean; build/vet/test + -race + just lint all green. [fable] DISAPPROVE for a docs-sync inaccuracy + correlated e2e flake, all autonomously fixable — REVISE round 2."
- criticism: ["docs/design.md's new T143 paragraph misdocuments the log schema: it claims EVERY 'scheduler aggregation change' record carries load_fps, but the IDLE-GAP collapse branch (internal/sched/weighted.go:537-538) emits NO load_fps and carries an UNDOCUMENTED 'gap' field. FIX (preferred): add `\"load_fps\", s.loadRate` to the idle-gap collapse record — the EWMA has already decayed across the gap in observeLoadLocked so the value is meaningful, and the schema becomes UNIFORM for the T146 metrics/log consumers; document the 'gap' field too. (Alternative: correct the doc to state load_fps is absent + gap present on idle-gap collapse — but uniformity is better for T146.)","TestAggregationGateLog idle-gap FLAKE hazard: between the engage drive's last frame and the low drive's first frame the harness does AwaitLogLine + log parse + spawns a second nsenter'd UDP sink; if that wall-clock span reaches CollapseDwell (only 800ms) the FIRST low-phase Pick collapses via the idle-gap branch (which lacks load_fps) and assertAggLogFields fails spuriously with a misleading 'missing load_fps'. HARDEN: adopt the load_fps-uniformity fix from criticism 1, AND assert reason=='sustained low load' explicitly, AND widen this test's CollapseDwell (e.g. 2s) so the inter-phase margin dominates harness jitter.","The e2e fixture configures an UNUSED [metrics] listen block (aggLogMetricsListen 127.0.0.1:9106) on both daemons; the test never queries it and the task EXPLICITLY defers all metrics wiring to T146 — REMOVE the block (T146's own e2e adds it). This also frees port 9106 back to the registry."]
- new_questions: []
- ledgerRefs: ["tasks:T143","goals:G13","defects:D72","defects:D73"]

### R182 — go-ahead

- createdAt: 2026-07-14T16:42:33.791Z
- updatedAt: 2026-07-14T16:42:33.791Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T143 review round 2 — GO-AHEAD (single fable reviewer — it filed the round-1 disapprove, proportionate for 3 localized fixes; round-1 core already dual-verified). All 3 round-1 criticisms RESOLVED with evidence: (1) idle-gap collapse record (weighted.go:538-540) now carries load_fps — all THREE 'scheduler aggregation change' sites schema-uniform; design.md's EWMA-decayed-across-gap claim verified against Pick's call order (gap captured before observeLoadLocked decays, then updateGateLocked logs); gap field doc matches gap.String(); NO new/second log call (no-double-log invariant re-verified, TestWeightedAggregationChangeLogFieldsAndNoDoubleLog asserts exactly 2 records total, passes under -race). (2) e2e aggLogCollapseDwell widened 800ms→2s + reason=='sustained low load' asserted explicitly so residual jitter fails loudly not silently; wait budget knob-derived (dwell+10*tau+1s, no magic sleep); low-phase inter-frame gap 50ms<<2s so idle-gap branch can't fire mid-drive. (3) [metrics] block + aggLogMetricsListen + port 9106 fully removed (grep clean); Metrics.Listen confirmed optional (applyMetricsLocked device.go:493), daemon startup unaffected — hardware-validated 9/9 on llm-ubuntu-0. Merged to main as 19bb873 (round-1 core) + d4ed216 (round-2) via clean cherry-pick of 6e26127..cec7ff1 onto a7dfd18 (docs/design.md 3-way auto-merged with T127/T144, no conflicts). Full gate green on composed main: sched tests + -race ok; e2e compiles; just lint 0 issues default+e2e+realhosts. Filed 1 low-severity out-of-scope defect: idle-gap record's fields untested → D75. 0 criticisms / 0 questions."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T143","goals:G13","defects:D75"]

### R186 — go-ahead

- createdAt: 2026-07-14T18:19:45.581Z
- updatedAt: 2026-07-14T18:19:45.581Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T146 review round 1 — RECONCILED GO-AHEAD (opus + fable panel, strictest-wins; BOTH approve). Four per-peer aggregation-gate gauges (wanbond_aggregation_engaged{peer}, wanbond_offered_load_fps{peer}, static engage/disengage_threshold_fps{peer}) plumbed through Bind PeerSnapshot (optional aggregationReporter type-assert) → metrics.Source.Aggregation() → collector. Merged to main as a082a9d (cherry-pick of ddbc10c onto 28e1c26; netns.go port CONFLICT resolved by reassigning T146's port 9107→9108 since T128 holds 9107, + aggregation_metrics_test.go updated to 9108; multipath.go/metrics.go/device/docs 3-way clean). BOTH reviewers verified EMPIRICALLY (HTTP scrapes): (1) THRESHOLD TRUTHFUL — gauge expr weighted.go:315-316 (EngageFraction*PerPathCapacity / Disengage*) BYTE-IDENTICAL to the live gate expr weighted.go:522-523, sourced live from immutable cfg each read; selectScheduler maps per_path_capacity_fps/fractions verbatim (defaults 0.9/0.5); e2e asserts 630/350 at 1e-6 tol; (2) T94 BACK-COMPAT EXACT — single-peer UNLABELED, two-peer peer-LABELED (multi-peer test rejects any unlabeled sample), via the shared peerLabelValues path; NewCollector's multiPeer decision unchanged (one-shot PeerNames()); every pre-existing exposition test passed UNMODIFIED (only fakeSource gained the method); (3) ACTIVE-BACKUP ABSENT at BOTH seams (nil PeerSnapshot.Aggregation skipped — no zero-value/empty-label leak; sampleless Descs emit nothing); (4) LOCK-SAFE — peerState.scheduler write-once, PeerSnapshots reads AggregationSnapshot (takes only sched.mu) AFTER releasing m.mu → m.mu→sched.mu ordering, no cycle; -race green; (5) 4 names exported constants, empty-subsystem offered_load_fps intentional. Full gate green on composed main: bind/metrics/device tests + -race ok; e2e compiles; just lint 0 issues default+e2e+realhosts. 0 criticisms / 0 questions / 0 defects."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T146","goals:G13"]

### R189 — go-ahead

- createdAt: 2026-07-14T18:49:56.003Z
- updatedAt: 2026-07-14T18:49:56.003Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T147 review round 1 — GO-AHEAD (single opus reviewer, proportionate for a TEST-ONLY e2e task; scenario A hardware-deterministic, scenario B validated modulo the pre-existing D78 harness race). Two -tags e2e aggregation-visibility scenarios (test/e2e/aggregation_visibility_test.go, +478) + netns.go port 9110. Merged to main as 9eb3274 (clean cherry-pick of e081f5c onto dea04b2; test-only, registry 9110 slots after 9109). Reviewer verified: (1) SCOPE test-only, no production code; (2) SKIP-NOT-FAIL via //go:build e2e + TestMain unshare -Urmn gating (every sibling e2e's mechanism); (3) build+vet under default AND -tags e2e clean, just lint 0 issues x3 tags; (4) SCENARIO A non-vacuous — asserts wanbond_aggregation_engaged 0->1->0 with wanbond_offered_load_fps crossing the engage(225)/disengage(125) threshold GAUGES directionally, EXACTLY TWO canonical T143 'scheduler aggregation change' records (to=aggregating then to=collapsed, verified vs weighted.go:546/555, NOT engaged/disengaged), log threshold fields asserted vs fraction*capacity, collapse wait DERIVED from collapse_dwell+load_tau (no magic sleep); (5) SCENARIO B non-vacuous — DEFAULT per_path_capacity_fps=10000 (engage-threshold gauge=9000), asserts engaged==0 across every retained sample for >=5s while 0<offered<9000, with a sawNonzeroOffered guard defeating a vacuous all-zero pass; (6) gauge names match metrics.go, port 9110 registered. D78 (pre-existing concentrator-netns bring-up race) manifests as a setup Fatalf not a false assertion; T147 adds no new flake. 0 criticisms / 0 questions / 0 defects."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T147","goals:G13","defects:D78"]

## M44

### R185 — go-ahead

- createdAt: 2026-07-14T18:03:08.314Z
- updatedAt: 2026-07-14T18:03:08.314Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T128 review round 1 — GO-AHEAD (single opus reviewer, proportionate for an e2e COMPILE+VET deliverable with privileged run DEFERRED to hardware). New test/e2e/multipeer_hardened_test.go (TestMultiPeerHardenedDatapath, 730 lines) encoding D47/D50/D58/D49/D42/D44 + netns.go port 9107 registration. Merged to main as f54888e (cherry-pick of fbc615e onto e0d6701; test/e2e only, registry merged cleanly — 9107 slots after T144's 9106). Reviewer verified OPERATIONALLY: (1) SCOPE clean — test/e2e only, no production code; (2) SKIP-NOT-FAIL — requireNetAdmin(t) is the first statement, running as uid 1000 SKIPPED at '/dev/net/tun unavailable' (--- SKIP, not FAIL); new requireIPTables mirrors the fail-soft t.Skipf contract; (3) compiles+vets under -tags e2e, just lint 0 issues x3 tag sets; (4) ASSERTIONS NON-VACUOUS — D47: both NATed edges measure positive iperf3 throughput AND per-peer wanbond_path_rx_bytes_total>0; D50: log-greps teardown INFO (peer field==hw-gamma) + wanbond_path_up{peer=hw-gamma}==0; D44: wanbond_fec_repair_packets_total strictly advances; D42: no 'panic:' + post-reload 3-peer reachability; D49: bootstrap within budget + 2 live peers reachable; (5) D58 asserts BOTH hw-alpha (first-configured) AND hw-beta carry their own peer label AND negatively asserts NO peer='' series (guards the T127 regression); (6) D50 budget hwTeardownBudget=RejectAfterTime+15s (real WireGuard keypair-validity budget, not a magic sleep); (7) port 9107 registered, no in-tree self-collision. Privileged execution legitimately deferred to the host-run task. 0 criticisms / 0 questions / 0 defects. NOTE: the worker de-risked by validating the D47 SNAT/conntrack demux in an unprivileged userns + round-tripping all rendered TOML through config.Load."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T128","goals:G8","defects:D47","defects:D49","defects:D50","defects:D58"]

### R197 — go-ahead

- createdAt: 2026-07-14T19:39:37.324Z
- updatedAt: 2026-07-14T19:39:37.324Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "D77 fix review — GO-AHEAD (single opus reviewer; dual-arch hardware pass is the ultimate corroboration). Fixed D77 (concentrator /metrics non-loopback bind) + D78 (fast-host lo-up/metrics-bind start race) + 5 further fixture defects that first-ever execution of T128's suite surfaced. Merged to main as 8b6a815 (cherry-pick of d2d094b onto 0145bfb; test/e2e/{multipeer_hardened_test.go,p2_aggregation_test.go,netns.go}; netns.go 3-way auto-merged with T147's port-9110 addition, no conflict). Reviewer verified: (1) SCOPE test/e2e ONLY — git diff internal/ EMPTY, server.go:requireLoopback (T17 invariant) UNTOUCHED (fixture adapts, does not weaken); (2) LOOPBACK FIX correct — metricsAddr()→127.0.0.1:9107 scraped via fetchMetricsInNetns/netnsMetricsClient into the peer netns (p2/p3/p4 mechanism); (3) rx→tx switch (d47) LEGITIMATE, verified vs real code — readLoop:1376 accrues shared-socket rx to the PRIMARY's peerPathState (attached[0], reconcile.go:220-224), so non-primary rx is structurally ~0; tx IS per-peer; NOT masking a bug (filed D81); (4) D50 teardown-match SOUND — baseline-anchored (index>=pre-kill) + peer==gamma filtered, verifies POST-KILL teardown; (5) bind=source (D30/D80 workaround) + start-retry (concStartAttempts=4, bounded, reaps hung daemon, still fails a genuine unbindable addr) are legitimate bounded fixture guards; (6) p2_aggregation_test.go = pure netnsMetricsClient extraction (behavior preserved); netns.go = registry-comment update. Default+e2e build/vet + just lint (default+e2e+realhosts) 0 issues. HARDWARE: BOTH o3 (aarch64, 205.4s) AND llm-ubuntu-0 (amd64, 208.6s) ran ALL subtests (d47/d58/d49/d44/d42/d50) GREEN, NO SKIP — satisfies T129. Reported 4 out-of-scope findings: D80 (r121 same loopback bind), D81 (T97 rx accounting) filed; D30 (production runtime-add gap, pre-existing) + per-peer-inbound-accounting design note. 0 criticisms / 0 questions / 0 new defects."
- criticism: []
- new_questions: []
- ledgerRefs: ["defects:D77","defects:D78","tasks:T129","tasks:T128","goals:G8","defects:D80","defects:D81"]

## M54

### R187 — go-ahead

- createdAt: 2026-07-14T18:22:19.505Z
- updatedAt: 2026-07-14T18:22:19.505Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T145 review round 1 — RECONCILED GO-AHEAD (opus + fable panel, strictest-wins; BOTH approve). sched.ProbeBudget{AccountProbe} on *WeightedScheduler: exempt-but-charged probe accounting — deducts one token per emitted probe / reflected echo WITHOUT shedding/delaying (strict priority; bucket may go negative), wired symmetrically into emitProbes + dispatchInbound echo via peerPathState.schedIdx. Three-tier priority model (ClassControl exempt-uncharged, KindProbe exempt-but-charged, ClassData fully paced) codified in scheduler.go + docs/design.md. Merged to main as 56f521a (cherry-pick of 36157e7 onto c028ca7; netns.go port CONFLICT resolved 9107→9109 + probe_headroom_test.go updated to 9109; multipath.go [T145 dispatchInbound vs merged T146 PeerSnapshot] + design.md [three-tier vs T146 metrics-ref] 3-way AUTO-MERGED clean). REPRODUCE-FIRST HANDLED HONESTLY: worker determined a base-fails/fix-passes -tags e2e would be a FLAKY KNIFE-EDGE (probe/echo ~10 frames/s ≈ 0.1-0.5% of a 450fps pace — ~2 ORDERS OF MAGNITUDE short of building a 1.2s standing queue in a 12s netns window; ran BOTH base+fixed binaries, both hold RTT ~0.06-0.08s), so the DISCRIMINATING proof lives in sched UNIT tests; the e2e (TestProbeHeadroomUnderOverload, port 9109) is an invariant guard, ran GREEN on llm-ubuntu-0 hardware (peak RTT 0.061s, overload proven real via 'scheduler pacer shedding'). BOTH reviewers MUTATION-VERIFIED: no-oping pacer.accountProbe's token deduction fails TestWeightedAccountProbeDeductsOneTokenWithoutShedding (bucket=4 want -2) AND TestWeightedAccountProbeReservesClassDataHeadroom (freed 0 want 3; base=7 charged=7) for the accounting reason; restore → pass. Verified: STRICT PRIORITY (charge inside if werr==nil AFTER the unconditional socket write, both sites); SYMMETRY (emitProbes charges snapshot idx i under m.mu, echo charges ps.schedIdx atomic); schedIdx STAMPED at all 3 p.paths splice sites (Open/attach/detach) with bounds-checked no-op fallback (-1/1/99 unit-tested); -race clean (AccountProbe takes only leaf s.mu, charged after m.mu.Unlock — no cycle with Pick); D22 ClassControl exempt-uncharged intact; pacing-off no-op. Full gate green on composed main: sched+bind tests + -race ok; e2e compiles; just lint 0 issues default+e2e+realhosts. BOTH filed ONE identical out-of-scope defect → D76 (active-backup+pacing lacks ProbeBudget — same latent starvation). 0 criticisms / 0 questions."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T145","goals:G13","defects:D65","defects:D76"]

## M59

### R188 — go-ahead

- createdAt: 2026-07-14T18:47:17.493Z
- updatedAt: 2026-07-14T18:47:17.493Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T152 review round 1 — RECONCILED GO-AHEAD (opus + fable panel, strictest-wins; BOTH approve). Generalized [scheduler] pacing to policy-independent (internal/config): split pacing knobs (PacingEnabled/PerPathCapacityFPS/PacingBurstFrames + derived PerPathCapacities/PacingBursts vectors, toml:'-', index-aligned to Config.Paths for T153) from weighted-only aggregation knobs; deriveWeightedPacingFromBDP→derivePacingFromBDP gated on PacingEnabled under BOTH policies. Merged to main as e093276 (clean cherry-pick of b698322 onto 7d0ffad; config + tests + test/e2e/pacing_test.go). BOTH reviewers verified EMPIRICALLY: (1) WEIGHTED BYTE-IDENTICAL (the load-bearing no-regression) — deriveWeightedBottleneckPacing body line-for-line identical to the old deriveWeightedPacingFromBDP (only the declared-count loop extracted verbatim); a 4-config base-vs-branch probe (hetero BDP, homo BDP, explicit knobs, defaults) loaded at 7d0ffad vs branch shows BYTE-IDENTICAL derived values (hetero bottleneck cap=833.33 from the 10Mbit link, burst=25, all aggregation knobs unchanged); full pre-existing weighted suite green; (2) PER-PATH ACTIVE-BACKUP — deriveActiveBackupPerPathPacing runs SizePacingFromBDP per path over its OWN link_bandwidth/link_rtt, NOT min-reduced; heterogeneous 50Mbit/10Mbit yields DISTINCT caps with strict caps[0]>caps[1] anti-bottleneck assertion; (3) FAIL-FAST non-vacuous — at base 7d0ffad the neither-source config LOADS with cap=0/burst=0 (enabled-but-unbinding pace = the exact D65 hazard); post-impl it fails with a NAMED error; applyDefaults early-return under active-backup + the derive fail-fast together close the synthetic-10000fps hole; (4) mutual-exclusion/all-or-nothing/link_rtt>0 preserved both policies (partial bw, both-sources, missing rtt, cap-without-burst each fail); (5) index alignment caps[i]/bursts[i] from c.Paths[i] declaration order, no Paths sorting; NewActiveBackup pacer validation gated on cfg.Pacing so pre-T153 device wiring stays valid; (6) 3 stale doc comments (SchedulerConfig/LinkBandwidth/LinkRTT) reworded for both policies. Full gate green on composed main: config tests ok; just lint 0 issues default+e2e+realhosts. 0 criticisms / 0 questions / 0 defects. UNBLOCKS T153 (device.selectScheduler plumbing of the per-path vectors)."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T152","goals:G14","defects:D65"]

### R191 — go-ahead

- createdAt: 2026-07-14T18:57:47.511Z
- updatedAt: 2026-07-14T18:57:47.511Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T154 review round 1 — GO-AHEAD (single opus reviewer, proportionate for a TEST-ONLY parity-coverage task). 3 config tests in internal/config/active_backup_pacing_parity_test.go (per-path BDP parity table; active-backup-vs-weighted heterogeneous distinction; no-[scheduler]-block P1 regression guard). Merged to main as c299f11 (clean cherry-pick of 287c793 onto 195130d; single new test file). Reviewer verified: (1) SCOPE — single test file, zero production code; (2) NON-DUPLICATIVE of T152's active_backup_pacing_test.go (table vs fixed fixture; AB-vs-weighted CROSS-compare which T152 never did; scheduler-block-OMITTED surface vs T152's declared-but-inert); (3) NON-VACUOUS — parity asserts PerPathCapacities[i] == SizePacingFromBDP(parseBandwidth(bw),ParseDuration(rtt)).CapacityFPS via math.Abs>1e-6, a genuine per-path BDP recomputation using the PRODUCTION parseBandwidth (catches cross-wiring/min-reduction/shared-scalar); (4) SUBTLE CORRECTNESS verified — since CapacityFPS depends only on bandwidth, weighted min-reduces to the SLOW link's cap so weighted-scalar==caps[1] numerically; the test asserts divergence on caps[0] (FAST) ONLY (caps[0]!=scalar, caps[0]>scalar) and correctly AVOIDS the false caps[1]!=scalar assertion + pins weighted PerPathCapacities==nil — correct on the right index, not accidentally passing; (5) no-pacing guard asserts policy=active-backup default, PacingEnabled false, scalar knobs zero, vectors nil, all 6 weighted knobs zero. Full gate green on composed main: config tests + Pacing -v ok; just lint 0 issues default+e2e+realhosts. 0 criticisms / 0 questions / 0 defects."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T154","goals:G14","defects:D65"]

### R192 — go-ahead

- createdAt: 2026-07-14T19:06:12.037Z
- updatedAt: 2026-07-14T19:06:12.037Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T153 review round 1 — RECONCILED GO-AHEAD (opus + fable panel, strictest-wins; BOTH approve). Wired cfg.Scheduler.PacingEnabled/PerPathCapacities/PacingBursts into sched.Config in selectScheduler's ACTIVE-BACKUP branch (device.go), weighted branch untouched — the final step making active-backup pacing (D65 primary fix) RUNTIME-EFFECTIVE. Merged to main as a39f39f (clean cherry-pick of 1490597 onto 1c446ea; device.go + scheduler_pacing_test.go[new] + config_test.go marker + docs install/design/example-toml). BOTH reviewers verified CONSTRUCTION-TIME INDEX ALIGNMENT is provably 1:1: buildScheduler (device.go:865-872) sets health[i]=probers[i] over cfg.Paths in declaration order (NO filter/reorder); deriveActiveBackupPerPathPacing (config.go:1132-1147) builds caps[i]/bursts[i] over cfg.Paths[i] in the SAME order; newActiveBackupPacers pairs pacers[i] with health[i] from PerPathCapacities[i]; NewActiveBackup fail-fasts on length mismatch. WEIGHTED byte-identical (diff touches only the default branch). PACING-OFF byte-compat (PacingEnabled=false → nil vectors, newActiveBackupPacers nil, Pick guards !cfg.Pacing; test compares to a reference unwired NewActiveBackup). REPRODUCE-FIRST empirically verified by BOTH: reverting the wiring hunk fails TestSelectSchedulerActiveBackupPacingEnabled at the exact D65-regression message ('no frames were paced out … wired pacing did not shed'); the test asserts per-path (NOT bottleneck) pacing in BOTH directions (admitted ≤ primaryCap*T+burst AND admitted > backupCap*T+backupBurst). Docs (install/design/wanbond.example.toml) accurately state policy-independent pacing + active-backup fail-fast; config_test.go scrape marker matches the new example header. Full gate green on composed main: device+sched+config tests + -race ok; just lint 0 issues default+e2e+realhosts. FABLE filed 1 HIGH-severity OUT-OF-SCOPE defect → D79 (per-path pacer configs carried POSITIONALLY not by identity across bind membership churn — a deferred path at Open / T55 promotion / reload AddPath misassigns the fast path to the slow path's rate, reintroducing D65; pre-existing in internal/sched+bind untouched by this diff, made REACHABLE by T153's wiring). 0 criticisms / 0 questions."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T153","goals:G14","defects:D65","defects:D79"]

## M48

### R190 — revise

- createdAt: 2026-07-14T18:55:12.748Z
- updatedAt: 2026-07-14T18:56:27.033Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "G12 PLAN review round 1 — RECONCILED REVISE (single configured plan-reviewer opus ran in Mode B and returned json; orchestrator writes this aggregated review). The emitted 13-task plan (T160-T172 across M61 backend / M62 Vite+TS frontend / M63 daemon-wiring+e2e+docs) for the live monitoring web UI is STRONG: FIDELITY to the answered Q45-Q50 is high (loopback-default + opt-in-non-loopback-requires-token, separate [monitor] listener, Host/Origin + static token + SameSite=Strict/HttpOnly cookie + constant-time compare, read-only v1, Vite+TS go:embed, 1s push + client-side ~5min sparklines + per-peer). GROUNDING verified accurate against source: the metrics.Source interface; the metricsSource.Paths() last-sample throughput-delta HAZARD (device/metrics.go:74-110 mutates a per-(peer,path) s.last map — two readers on the SAME instance semantically corrupt each other's cadence even under the mutex; newMetricsSource allocates a fresh map so the monitor MUST use its OWN Source instance — correctly captured in T161/T165/T169); the loopback-only T17 fail-fast invariant; single-binary role-from-config (edge/concentrator parity free via device.Up wiring); 0600 config load. DAG is ACYCLIC with correct build-ordering (Vite scaffold → //go:embed → go build/golangci typecheck → gate), every task has concrete verifiable acceptance, auth tasks (T162/T164) are frontier-tier and well-specified, sizing is right (no mega-tasks, auth isolated). coder/websocket server lib is a determinable default (Q49 bundled it; user did not pick hand-roll) — not a blocker. THE ONE GAP (→ revise): a USER-ONLY unresolved decision on TRANSPORT CONFIDENTIALITY for the opt-in non-loopback bind — the plan ships a bearer token over PLAINTEXT HTTP on a LAN-reachable listener (passive on-path capture), and TLS (flagged in Q45's context) was never adjudicated in Q45-Q50. This sits squarely in the goal's headline concern ('how to make such an API safe'). Filed as open question Q51 (options: accept+document / require-TLS-when-non-loopback / forbid-non-loopback+ssh-L). G12 stays in planning, BLOCKED on Q51 until the user answers; then a re-plan/lock round finalizes. 0 criticisms (no plan defect) / 1 new_question (Q51) / 0 out-of-scope defects."
- criticism: []
- new_questions: ["Q58"]
- ledgerRefs: ["goals:G12","questions:Q58"]

### R199 — go-ahead

- createdAt: 2026-07-14T22:41:45.469Z
- updatedAt: 2026-07-14T22:41:45.469Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "G12 revised-plan review (aggregated, multi-reviewer panel opus + fable, reconciled strictest-wins). VERDICT = go-ahead (both reviewers go-ahead; 0 criticisms / 0 new questions / 0 defects). The sole prior blocker of R190 (open Q58 — transport confidentiality for the opt-in non-loopback [monitor] bind) is RESOLVED by the user's answer (a): accept plaintext-token-over-LAN for the read-only v1 scope + document the residual passive-capture risk. The plan-advance revision applied this surgically and faithfully: no transport/auth task changed (answer (a) accepts the existing loopback-default + opt-in-non-loopback-requires-token + static-bearer-token-over-plaintext posture, so T160/T162/T164 stand as R190 already judged sound), and T171 (M63 docs-sync) now has BOTH description AND acceptance requiring an explicit residual-risk paragraph (token in cleartext over LAN; passive on-path observer can capture it -> read-only stats access; recommend loopback + `ssh -L` on untrusted networks) co-located with the existing metrics-loopback invariant in design.md, plus README/install.md/wanbond.example.toml [monitor] coverage mirroring the [metrics] doc pattern. Both reviewers confirmed no silent-unwarned path exists: T160+T162 fail-fast on a non-loopback bind without a token at both config-load and bind time; answer (a) requires DOCUMENTATION (not runtime TLS/log-warning), which T171 (a required, DoD-gated task) ships with the code. DAG T160-T172 intact, un-renumbered. MINOR NON-BLOCKING NOTE for the implementer (fable): T171's 'docs/design.md:740' anchor for the T17 metrics-loopback invariant is STALE — the invariant prose now lives at design.md:897-898 under '### Supporting packages' and there is no titled 'security-invariants section'; T171's acceptance is content-based (place the monitor invariant + residual-risk paragraph alongside the existing metrics-loopback prose), so resolve the location by content, not line number. Plan is ready to lock to `planned`."
- ledgerRefs: ["goals:G12","questions:Q58"]

## M60

### R193 — go-ahead

- createdAt: 2026-07-14T19:19:13.885Z
- updatedAt: 2026-07-14T19:19:13.885Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T155 review round 1 — GO-AHEAD (single opus reviewer, proportionate for DOCS-ONLY). Full doc-sync for D65 policy-independent pacing. Merged to main as 00e190f (clean cherry-pick of 975734c onto e12e620; README.md + docs/design.md + docs/runbook.md — install.md + wanbond.example.toml were already corrected by the merged T153). Reviewer verified all 7 acceptance clauses: (1) docs-only, no production code; (2) all FIVE files (README/design/install/runbook/example.toml) carry pacing_enabled/link_bandwidth/link_rtt + state pacing available under default active-backup; (3) design.md DECISION BLOCKS record BOTH the policy-independent-pacing choice AND the per-path-vs-bottleneck sizing distinction; (4) ABSENCE grep clean — no weighted-only claim attached to pacing/BDP keys (surviving weighted-only notes attach only to the aggregation knobs engage/disengage/collapse/load_tau/weight_*, permitted); (5) wanbond.example.toml loads (TestExampleConfigLoads PASS); (6) D65 motivation accurate vs rootCause + internal/sched/pacer.go (synchronous Send, head-shed pacer no internal enqueue, ~1s loaded vs ~40ms idle RTT, 3.67 vs >=6.9 Mbps); (7) runbook Starlink-style guidance consistent with the T152 all-paths fail-fast rule. Full gate green on composed main: config Example test ok; just lint 0 issues default+e2e+realhosts. NON-BLOCKING nit (not filed): design.md attributes a PickPaced return to tryConsume (which returns bool; PickPaced is the Pick result) — substantively correct (admit-or-shed-at-head, never enqueue), immaterial for a docs task. 0 criticisms / 0 questions / 0 defects."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T155","goals:G14","defects:D65"]

### R195 — revise

- createdAt: 2026-07-14T19:32:07.482Z
- updatedAt: 2026-07-14T19:32:07.482Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T156 review round 1 — DISAPPROVE (single opus reviewer), REMEDIATED INLINE by orchestrator (both criticisms trivial autonomously-fixable docs edits; T137 escape-hatch). D65 field-validation procedure in docs/manual-checklist.md (docs-only, +186). Reviewer confirmed scope/config-keys(pacing_enabled/link_bandwidth/link_rtt match schema, all-or-nothing satisfied)/observation-table/e2e-no-absolute-throughput-caveat/lint all correct, BUT found 2 real in-scope docs defects: (1) iperf3 DIRECTION inconsistency — leg 1a ('Direct WAN') used `-R` (reverse=edge measures DOWNLOAD) while legs 1b/1c use forward (edge SENDS=UPLOAD); pacing shapes edge EGRESS/upload and the D65 ~3.67→~6.9 Mbps figures are UPLOAD, so a `-R` download baseline on an asymmetric last-mile (Starlink down>>up) is NOT comparable — breaks the three-way attribution AND contradicts the table's own ~6.9 upload figure; plus leg 1a lacked the `iperf3 -s` server line 1b/1c have (not runnable verbatim); (2) BROKEN intra-repo anchor `design.md#pacing` (no such heading; pacing lives under '### Send-side scheduler — internal/sched'), uncaught by the Go-only just lint. ORCHESTRATOR REMEDIATION (commit 5451c54): rewrote leg 1a to FORWARD/upload mode (dropped `-R` on both sub-measurements, added the concentrator `iperf3 -s -p 5201` server line, reworded the record-notes to 'edge UPLOAD throughput — the direction the pacer shapes' so T_starlink_direct/T_5g_direct are valid upload baselines for legs 1b/1c); fixed the anchor to design.md#send-side-scheduler--internalsched (verified the target heading exists at design.md:241). Merged to main as afcc559 (T156 core) + 5451c54 (remediation). Verified: no `-R` remains in leg 1a, no `#pacing` anchor; just lint 0 issues default+e2e+realhosts. 2 criticisms (both remediated inline) / 0 questions / 0 defects."
- criticism: ["[REMEDIATED 5451c54] iperf3 leg-1a `-R` download baseline non-comparable to the forward/upload tunnel legs 1b/1c + missing `iperf3 -s` server line — rewritten to forward/upload with the server line added.","[REMEDIATED 5451c54] broken design.md#pacing anchor — repointed to the real #send-side-scheduler--internalsched heading."]
- new_questions: []
- ledgerRefs: ["tasks:T156","goals:G14","defects:D65"]

### R196 — go-ahead

- createdAt: 2026-07-14T19:34:36.926Z
- updatedAt: 2026-07-14T19:34:36.926Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T157 — GO-AHEAD (orchestrator-run terminal G14 integration gate; a verification task, not code-authoring, so run inline on the composed main rather than dispatched). Ran the full project definition-of-done on composed main 8685a5e (all G14 pacing+config+wiring+docs merged: T149/T150/T151/T152/T153/T154/T155/T156/T158/T159 + the G13 T143-T148): `nix develop -c just build` → go build ./... clean; `nix develop -c just test` → all 13 packages ok; `nix develop -c just lint` → golangci-lint + go vet across default+e2e+realhosts tags = 0 issues each; `gofmt -l cmd internal test` → EMPTY (exit 0). No lint-only regression in tag-guarded helpers referencing the changed SchedulerConfig/sched symbols, no unused symbol orphaned by the pacer extraction, no stale-doc-comment/misspell lint in the new docs. No code-vs-doc drift (README.md/docs/* in sync with the merged code; the only dirty tree state is the per-merge .cq ledger + pre-existing untracked cq.toml/wanbond-fixes.md). Acceptance fully met. 0 criticisms / 0 questions / 0 defects."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T157","goals:G14","defects:D65"]

## M55

### R194 — go-ahead

- createdAt: 2026-07-14T19:28:27.798Z
- updatedAt: 2026-07-14T19:28:27.798Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T148 review round 1 — GO-AHEAD (single opus reviewer, proportionate for DOCS-ONLY operability documentation). Added the pacing on/off tradeoff table, inner-ICMP-infeasibility note, and operability runbook stanza to docs/design.md + README.md (the three-tier priority model was already present from T145/T155, referenced not duplicated). Merged to main as a1c3937 (clean cherry-pick of e6838ea onto 1007707; design.md + README.md). Reviewer verified THE CRITICAL CHECK — every cited signal string matches source EXACTLY: all 5 metric names vs internal/metrics/metrics.go (wanbond_aggregation_engaged :136, wanbond_offered_load_fps :139 — the worker correctly used this NOT the task-prose typo wanbond_aggregation_offered_load_fps, wanbond_aggregation_engage_threshold_fps :143, wanbond_aggregation_disengage_threshold_fps :147, wanbond_weighted_capacity_sane :123); both log strings ('scheduler aggregation change' weighted.go:546/555/569, 'scheduler pacer shedding' pacer.go:142) + their field keys (to/from/load_fps/engage_threshold_fps/disengage_threshold_fps/reason; shed_frames/load_fps); the config hard-fail phrase (config.go:1357 validateWeightedEngageAgainstBandwidth); the inner-ICMP note cites the CORRECT internal/bind/classify.go (worker corrected the task's wrong sched/classify.go) reading only the outer WG type word; cross-doc anchors #3a/#6b resolve; BDP sizing REFERENCED to install.md §3a not duplicated; tradeoff table internally consistent (71/29 vs 50/50, 1083 vs 757ms, 6.93 vs 4.98 Mbps, ~33% shed). All 4 required sections present. Full gate green: build/vet/test + just lint 0 issues default+e2e+realhosts. 0 criticisms / 0 questions / 0 defects."
- criticism: []
- new_questions: []
- ledgerRefs: ["tasks:T148","goals:G13"]

## M61

### R200 — go-ahead

- createdAt: 2026-07-14T22:57:47.575Z
- updatedAt: 2026-07-14T22:57:47.575Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T161 implement review (aggregated, panel opus + fable, reconciled strictest-wins). VERDICT = approve/go-ahead (BOTH reviewers approve; 0 criticisms / 0 questions / 0 defects). internal/monitor MonitorSnapshot DTO + BuildSnapshot(metrics.Source) faithfully and completely encode the metrics.Source read model: field-by-field cross-check against all five Source DTOs (PathSnapshot incl State->up, FECSnapshot 8, ReseqSnapshot incl all 7 embedded reseq.Stats, AggregationSnapshot 5, SessionSnapshot 2) found NO dropped/mistyped field; RTT/Jitter/LastHandshakeAge rendered as float SECONDS via .Seconds() and PINNED by marshalled-JSON value assertions (50ms->0.05 catches any 1e9 error); multiPeer=len(PeerNames())>1 tested for 1 and 2 peers with per-entry peer labels; each Source method called EXACTLY ONCE (no rate-state-corrupting double read); NO device import (only metrics + telemetry); empty collections marshal as [] not null (TestBuildSnapshotEmptyIsNotNull); tests assert the MARSHALLED JSON shape (map[string]any) not struct equality; gofmt/go build/vet/test + just lint (default+e2e+realhosts) all green. Surgical diff (2 new files). Merged to main."
- ledgerRefs: ["tasks:T161","goals:G12"]

### R201 — revise

- createdAt: 2026-07-14T23:02:00.182Z
- updatedAt: 2026-07-14T23:02:00.182Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T160 implement review round 1 (aggregated, panel opus + fable, reconciled strictest-wins). VERDICT = revise (opus approve; fable disapprove -> strictest wins). Both confirm the CODE is correct and the gate is green: internal/netutil.IsLoopbackHost is byte-faithful to metrics/server.go requireLoopback and fail-closed on all 20 probed edge cases (0.0.0.0, empty host ':9096', [::], [::1], malformed -> Load error); no input lets a routable addr pass as loopback; ErrMonitorNonLoopbackWithoutAuth is package-level, %w-wrapped, asserted via errors.Is; DisallowUnknownFields genuinely exercised; all 6 acceptance cases present; device D52 reload catch-all extended minimally without pre-empting T169; go build/vet/test + just lint (default+e2e+realhosts) all green. DISAPPROVED on definition-of-done, two autonomously-fixable criticisms: (1) DOC-SYNC (AGENTS.md 'keep docs current'): the new operator-facing [monitor] config keys (monitor.listen/monitor.token) + fail-fast invariant appear in NO doc; add at minimum a commented-out [monitor] block to wanbond.example.toml (the deliberately-exhaustive example that documents [metrics]) noting the endpoint/full docs land with later monitor tasks — the comprehensive README/design/install sync remains T171's scope. (2) internal/netutil ships with NO test files: add a direct table test for IsLoopbackHost covering the fail-closed branches (empty host, [::1], [::], bare-port/missing-port error path) so a regression in the host=='' security branch cannot pass the suite. Filed low out-of-scope defect (classification duplication metrics/server.go vs netutil -> consolidate later). Re-dispatching worker with these two criticisms."
- ledgerRefs: ["tasks:T160","goals:G12"]

### R202 — go-ahead

- createdAt: 2026-07-14T23:11:25.647Z
- updatedAt: 2026-07-14T23:11:25.647Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T160 implement review round 2 (fable re-review of the revise). VERDICT = approve/go-ahead. Both round-1 criticisms verified RESOLVED with direct evidence: (1) doc-sync — a commented-out [monitor] block added to wanbond.example.toml (mirrors the [metrics] example) showing listen+token, stating the non-loopback-requires-token invariant with the real ErrMonitorNonLoopbackWithoutAuth identifier, and explicitly deferring the endpoint + full README/design/install docs to T171; (2) internal/netutil/loopback_test.go added directly covering all IsLoopbackHost fail-closed branches (empty host=>non-loopback, [::1]=>loopback, [::]/0.0.0.0=>non-loopback, public IP=>non-loopback, bare-port/missing-port/malformed=>error, localhost resolution) asserting bool AND error — a MUTATION CHECK confirmed the test fails if the host=='' branch regresses to return true. Round-1 code unchanged (round-2 delta = 2 files only). Gate re-run green in worktree AND re-gated on the composed main tree (T160+T161): gofmt clean, go build/vet/test green. Round-1 was opus-approve + fable-disapprove (R201 revise); round-2 both-approve. Merged to main (7ce4752, 2 commits). Low consolidation defect D83 filed+root-caused (ready-to-seed)."
- ledgerRefs: ["tasks:T160","goals:G12","defects:D83"]

### R203 — revise

- createdAt: 2026-07-14T23:22:34.107Z
- updatedAt: 2026-07-14T23:22:34.107Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T162 implement review round 1 (aggregated, panel opus + fable, reconciled strictest-wins). VERDICT = revise (opus approve; fable disapprove -> strictest wins; fable empirically REPRODUCED a real defect opus missed). Both confirm the bind guard is fail-closed (non-loopback+empty-token returns ErrNonLoopbackBind BEFORE net.Listen, opens no socket; wildcard/empty-host/routable covered), act-then-verify TOCTOU parity with metrics/server.go on the tokenless path, coder/websocket v1.8.15 Accept uses the SAFE same-origin default (authenticateOrigin on unless InsecureSkipVerify — verified accept.go:116; no CSWSH regression, T164 tightens), the /ws frame is the real BuildSnapshot(src) contract with exactly-one-frame-then-StatusNormalClosure, and the full gate is green (go test -race + just lint 0 issues all tags + go mod verify/tidy clean). DISAPPROVED on three autonomously-fixable criticisms: (1) LISTENER LEAK on Close-without-Start — Server.Close is only s.srv.Shutdown(ctx), but http.Server.Shutdown closes ONLY listeners registered via Serve; on the NewServer->Close-without-Start path (which TestLoopbackBindAccepted itself exercises) the bound socket is NEVER released (fable reproduced `bind: address already in use` on re-listen). Fails the 'Close clean' acceptance operationally. FIX: Close must also close s.ln tolerating net.ErrClosed (when Serve already closed it), and TestLoopbackBindAccepted must assert the port is re-bindable after Close. (2) The token-AUTHORIZED bind branch is UNTESTED: no test covers non-loopback + token!='' succeeding (NewServer('0.0.0.0:0','secret',...) must bind, nil error) — a regression inverting the token check would pass. Add it. (3) Stale comment in newWSHandler: says 'StatusInternalError on the deferred close is a no-op' but the code uses c.CloseNow() (sends no status) — fix the comment to match. Filed low out-of-scope defect: metrics.Server.Close has the IDENTICAL leak (pre-existing, T162 copied the pattern; fixing metrics is separate). Re-dispatching worker with the three criticisms."
- ledgerRefs: ["tasks:T162","goals:G12"]

### R204 — go-ahead

- createdAt: 2026-07-14T23:29:51.253Z
- updatedAt: 2026-07-14T23:29:51.253Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T162 implement review round 2 (fable re-review of the revise). VERDICT = approve/go-ahead. All THREE round-1 criticisms verified RESOLVED with evidence: (1) LISTENER LEAK fixed — Server.Close (internal/monitor/server.go) now runs srv.Shutdown then s.ln.Close() tolerating net.ErrClosed (Start->Close path where Serve already closed it), correct error precedence (Shutdown's error wins; ln-close error surfaces only if Shutdown succeeded); TestCloseReleasesPortWithoutStart re-binds the SAME OS-assigned port read from Addr() (not :0) — a REAL regression guard, FAIL-FIRST VERIFIED (reverting Close to Shutdown-only makes it fail EADDRINUSE, with the fix go test -race passes); the Start->Close path stays goleak/-race clean. (2) TestNonLoopbackWithTokenAccepted added locking the token-authorized non-loopback bind branch (0.0.0.0:0 + token => nil error + bound server). (3) newWSHandler comment corrected to accurately describe c.CloseNow (immediate close, no close frame/status). Round-1 skeleton code otherwise unchanged (opus + fable round-1 vetted the bind guard/WS same-origin default/lifecycle); internal/metrics/ UNTOUCHED (D84 separate). Gate green in worktree AND re-gated on composed main tree: gofmt clean, go build/vet/test (monitor/config/metrics) green. Round-1 = opus-approve + fable-disapprove (R203 revise, fable reproduced the leak); round-2 = approve. Merged to main (47c65af, 2 commits). Pre-existing metrics.Close leak filed as D84 (root-caused, ready-to-seed)."
- ledgerRefs: ["tasks:T162","goals:G12","defects:D84"]

### R205 — revise

- createdAt: 2026-07-15T00:00:03.434Z
- updatedAt: 2026-07-15T00:00:03.434Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T164 implement review round 1 (aggregated, panel opus + fable, reconciled strictest-wins). VERDICT = revise. T164 was implemented INLINE by the orchestrator (three dispatched workers stalled during exploration — an agent-infra hang, zero code written each time). BOTH reviewers INDEPENDENTLY found the SAME real security defect (strong signal): the middleware's Origin check reused hostAllowed(u.Host, allowed), whose IP-literal passthrough (a VALID DNS-rebinding defense for the attacker-uncontrolled HOST header) is WRONG for the ORIGIN header — the Origin is the client PAGE's origin, fully attacker-controlled and serveable from any bare public IP. Empirically (fable): 'Origin: http://203.0.113.7' -> 200, should be 403; opus confirmed from the passing TestHostAllowed assertions. This is a cross-origin/CSRF BYPASS, critical on /ws where the Origin header is the SOLE CSRF control (WebSocket upgrades are not gated by SOP/CORS): an attacker page at http://<any-ip>/ opens ws://127.0.0.1:PORT/ws, the upgrade is accepted, and the monitor snapshot is exfiltrated cross-origin. opus 2nd criticism: missing foreign-IP-Origin test (the suite only had a foreign-DOMAIN case). EVERYTHING ELSE both reviewers verified SOUND: token gate fail-closed + constant-time (subtle.ConstantTimeCompare is the only token compare; the token=='' sites are config-presence checks); ?token= bootstrap sets the cookie to the CONFIGURED token only on a match, HttpOnly+SameSite=Strict+Secure=false+Path=/ (Q58(a)), same-path relative redirect (no open-redirect); middleware wraps the mux so Host/Origin precede token on BOTH / and /ws; unauthenticated /ws with a token => 401; null/opaque/file:// Origin (u.Host=='') => 403; missing/foreign-domain Host => 403; goleak + -race + just lint (0 issues all tags) green. FIX APPLIED (round 2, fc59349): split the classifier — hostAllowed (Host) keeps the IP pass; new originAllowed (Origin) requires EXACT same-origin (Origin==Host, covering legit loopback/LAN direct-IP access) OR an allowlisted host, NO IP passthrough; added foreign-IP-Origin tests on /, /ws (the CSRF-critical path), and an originAllowed unit test. Re-review pending."
- ledgerRefs: ["tasks:T164","goals:G12"]

### R206 — go-ahead

- createdAt: 2026-07-15T00:03:57.391Z
- updatedAt: 2026-07-15T00:03:57.391Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T164 implement review round 2 (fable re-review of the Origin fix). VERDICT = approve/go-ahead. The round-1 cross-origin/CSRF bypass (both opus+fable flagged: Origin check reused hostAllowed's IP-literal pass) is CLOSED and verified DIFFERENTIALLY: the new HTTP-route regression test (Origin: http://198.51.100.7 on /) fails against round-1 code with '200, want 403' (the exact bypass) and passes on fc59349. The middleware Origin branch now routes through originAllowed, which grants ONLY exact same-origin (Origin==r.Host) OR an allowlisted host (loopback aliases + configured listen host) with NO IP-literal passthrough; hostAllowed (Host header) keeps the IP pass for its DNS-rebinding role. No legit access regressed: exact same-origin covers loopback AND the wildcard-bind LAN case (browser sends matching host:port in Origin and Host); the full monitor suite (token flow, no-Origin, WS one-shot, goleak) passes under -race; whole-repo go test -race green; just lint 0 issues all tags. No new bypass: cross-site pages cannot force Origin==Host (browser sets them independently); Origin: null fails closed (u.Host==''); hostOnly strips ports/v6-brackets correctly. Fair reviewer note: TestAuthForeignOriginRejectedOnWS also passes on old code (websocket.Accept(nil) retains the library default same-origin check), so it pins the end-to-end /ws invariant while the HTTP-route test is the discriminating guard — both retained. T164 (inline-implemented after 3 worker stalls) merged to main (fc59349). Round-1 = opus+fable disapprove (identical Origin finding); round-2 = approve."
- ledgerRefs: ["tasks:T164","goals:G12"]

### R207 — go-ahead

- createdAt: 2026-07-15T00:19:46.874Z
- updatedAt: 2026-07-15T00:19:46.874Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- summary: "T165 implement review (panel opus + fable). VERDICT = approve/go-ahead. OPUS: explicit approve, 0 criticisms — adversarial lifecycle analysis confirms the 1s WS push loop is provably LEAK-FREE on BOTH exits: client-close (CloseRead->connCtx->loopCtx; deferred CloseNow closes the conn so the CloseRead reader goroutine exits) and server-Close (srvCtx cancel fires the registered AfterFunc->stop->loopCtx.Done). The `defer context.AfterFunc(srvCtx, stop)()` idiom registers-on-entry/unregisters-on-exit and spawns NO per-conn goroutine (srvCtx is a WithCancel cancelCtx; the AfterFunc goroutine spawns only once at shutdown then exits). Close cancels BEFORE srv.Shutdown so Shutdown does not block on the hijacked-WS handler; Close-without-Start is no-op-safe (TestCloseReleasesPortWithoutStart passes). Stalled-client write unblocks by construction (writeCtx=WithTimeout(loopCtx,writeTimeout); a blocked c.Write cancels on Close via loopCtx). Cadence real (immediate first frame + 1s ticker, per-write 5s timeout). Graceful-vs-abrupt close logic sound (StatusNormalClosure only when srvCtx.Err()!=nil); no spurious error log on cancellation (loopCtx.Err()==nil guard). The dedicated-Source invariant comment is GROUNDED against internal/device/metrics.go's mutable last-sample delta map. go test -race -count=1 goleak-clean in BOTH TestServerWSPushesSnapshots and TestServerWSCloseStopsPush; just lint 0 issues. FABLE: verified via differential scratch probes that 'the real implementation passes both discriminating probes' (cadence + prompt stalled-client shutdown) — trending approve — then stalled (agent-infra hang, same pattern that hit the T164 workers) during a docs-sync check (T165 owns NO docs; the [monitor] docs are T171's) before emitting its final JSON; per the panel abstention rule fable is dropped and the panel proceeds on opus's approve. Opus's ONE non-blocking nit (TestServerWSCloseStopsPush comment overstated the 'block on write' mechanism — a single small frame won't fill the TCP buffer) was addressed by rewording the comment before merge. T165 implemented INLINE (monitor-package workers stalled). Merged to main (70ca59f). M61 backend complete."
- ledgerRefs: ["tasks:T165","goals:G12"]
