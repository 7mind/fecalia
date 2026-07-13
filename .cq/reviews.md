---
ledger: reviews
counters:
  milestone: 0
  item: 61
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
