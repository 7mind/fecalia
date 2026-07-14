---
ledger: questions
counters:
  milestone: 0
  item: 56
archives:
  - id: M2
    path: ./archive/questions/M2.md
    summary: "wanbond S (scaffolding) complete: git repo + Go module github.com/7mind/wanbond, package layout, Nix flake (dev shell + static binary), golangci-lint + GitHub Actions CI, TOML config loader (0600 + fail-fast), structured logging. T1-T5 done and verified in-sandbox; Q9 answered."
    title: "wanbond S: repo scaffolding &amp; toolchain"
    status: done
---

# questions

## M1

### Q1 — answered

- createdAt: 2026-07-01T23:13:36.195Z
- updatedAt: 2026-07-01T23:20:36.753Z
- author: user
- session: 0047802a-1b44-4fcc-8198-d12359610ad6
- question: "Concrete verification thresholds: the prompt leaves placeholders — P1 failover recovery within N seconds; P3 at X% injected loss ≥Y% recovered with ≤Z% overhead; P2 'bonded throughput ~ sum' (within what tolerance?); P4 'overhead tracks loss' (what bound vs the fixed-FEC baseline?). What numbers should the test harness assert?"
- context: These become the `acceptance` fields of the per-phase verification tasks, so they block writing a testable plan. They can be named constants in the harness (easy to retune later), but the plan needs initial values to assert against.
- suggestions: ["Approve proposed defaults: P1 recovery N=3s (TCP session survives, throughput restored within 3s of killing the active WAN); P2 bonded throughput ≥85% of the sum of individual path throughputs, and 5G bytes <1% of total while Starlink is healthy; P3 at 5% and 15% uniform injected loss, ≥95% of lost data frames recovered without retransmit, FEC overhead ≤2× the configured parity ratio; P4 adaptive total overhead ≤ fixed-FEC baseline bytes for equal masking, and post-recovery residual loss ≤0.5% at steady 5% path loss","Supply your own numbers per phase","Defer exact numbers: plan tasks assert 'threshold from a constants table' and you fill the table before P1 implementation starts"]
- recommendation: Option (a) — accept the proposed defaults as initial acceptance thresholds, kept as named constants in the harness so retuning after real-link measurements is a one-line change.
- ledgerRefs: ["goals:G1"]
- answer: as recommended

### Q2 — answered

- createdAt: 2026-07-01T23:13:45.132Z
- updatedAt: 2026-07-01T23:21:12.648Z
- author: user
- session: 0047802a-1b44-4fcc-8198-d12359610ad6
- question: "Test environment: can the automated harness assume Linux network namespaces + netem/tc with root (or CAP_NET_ADMIN/CAP_NET_RAW) on the dev machine? And what real hardware exists for end-to-end verification — is the actual Starlink+5G edge box and a concentrator VPS available during development, or is all phase verification to run in netns emulation only?"
- context: The per-phase verify criteria (WAN kill mid-SSH, loss/jitter injection, throughput aggregation) need either netem emulation or real links. TUN device creation also needs CAP_NET_ADMIN, so even the P0 spike's end-to-end check is privileged. Whether real hardware exists decides if the plan includes deploy-and-measure-on-real-links tasks or stops at emulated verification with a manual checklist.
- suggestions: ["netns+netem with root available locally; real edge+VPS also available for manual end-to-end runs per phase","netns+netem only — all verification emulated, real deployment validated later outside this plan","No root locally — harness must run inside a VM (e.g. qemu/nixos-test-style)"]
- recommendation: "Assume (a): netns+netem as the reproducible automated harness (two namespaces + veth pairs emulating Starlink-like 45ms jittery and 5G-like 64ms stable paths), plus a short scripted manual checklist per phase for the real hardware."
- ledgerRefs: ["goals:G1"]
- answer: "Assume (a): netns+netem as the reproducible automated harness (two namespaces + veth pairs emulating Starlink-like 45ms jittery and 5G-like 64ms stable paths), plus a short scripted manual checklist per phase for the real hardware.; I do have starlink+5g for real production tests later"

### Q3 — answered

- createdAt: 2026-07-01T23:13:51.712Z
- updatedAt: 2026-07-01T23:21:30.933Z
- author: user
- session: 0047802a-1b44-4fcc-8198-d12359610ad6
- question: "CI expectations: is there a CI system for this repo (GitHub Actions, self-hosted runner, none/local-only)? If CI exists, should the privileged netns/TUN end-to-end tests run there (privileged runner) or stay local-only behind a build tag, with CI running only unprivileged unit/property tests?"
- context: Privileged tests cannot run on stock shared CI runners. This decides whether the plan includes CI-pipeline tasks and how the test suite is layered (e.g. plain `go test ./...` unprivileged; `-tags e2e` + sudo for the netns harness).
- suggestions: ["No CI for now — local `go test` plus a sudo-run e2e target; structure so CI can be added later","GitHub Actions for lint+unit; e2e local-only behind a build tag","Full CI including privileged e2e (self-hosted or privileged container runner)"]
- recommendation: "Option (a) or (b): layer the suite as unprivileged unit/property tests (`go test ./...`) vs a tagged privileged e2e suite invoked via a make/just target with sudo; add CI wiring only if you want it."
- ledgerRefs: ["goals:G1"]
- answer: GitHub Actions for lint+unit; e2e local-only behind a build tag

### Q4 — answered

- createdAt: 2026-07-01T23:13:59.455Z
- updatedAt: 2026-07-01T23:21:41.709Z
- author: user
- session: 0047802a-1b44-4fcc-8198-d12359610ad6
- question: "Repo and toolchain conventions: (1) Go module path (e.g. github.com/<org>/wanbond — which org/name)? (2) Minimum Go version (latest stable, currently 1.24.x line, or pin to what amneziawg-go requires)? (3) Linting: golangci-lint with a standard config? (4) Should the repo ship a Nix flake (dev shell with Go toolchain + netem tools, package for the binary)? (5) The working directory /home/pavel/work/safe/fecalia is not yet a git repo — initialize the project here?"
- context: "Pure greenfield: the directory contains only fec-prompt.md and the ledger. Scaffolding (module init, lint, dev shell) is the first work milestone, so these conventions block the first tasks. Your environment appears Nix-centric, hence the flake question."
- suggestions: ["Module github.com/7mind/wanbond, latest stable Go, golangci-lint, Nix flake with dev shell + package, git init in place","Same but no Nix — plain Go toolchain + Makefile","Different module path / layout — specify"]
- recommendation: Option (a), adjusted to whatever module path you actually want to publish under; a flake dev shell pinning Go + iproute2/netem test tools makes the privileged e2e harness reproducible.
- ledgerRefs: ["goals:G1"]
- answer: as recommended

### Q5 — answered

- createdAt: 2026-07-01T23:14:06.066Z
- updatedAt: 2026-07-01T23:21:53.426Z
- author: user
- session: 0047802a-1b44-4fcc-8198-d12359610ad6
- question: "Config format and shape: the prompt says 'simple config (WG keys/peers + amnezia params + path list + PSK)'. Which format — TOML, YAML, or wg-quick-style INI extended with wanbond sections? One schema for both roles with an explicit role=edge|concentrator field, or role inferred (e.g. presence of a path list)? Keys/PSK inline in the file (0600) or file references?"
- context: The config loader and its validation are early tasks (P0 needs a working config to bring the tunnel up), and the format decision ripples into docs, systemd units, and the test harness fixtures.
- suggestions: ["Single TOML file, explicit role field, keys inline, file mode 0600 enforced","wg-quick INI dialect ([Interface]/[Peer] plus [Path]/[Bond] sections) for WG-user familiarity","YAML"]
- recommendation: "Option (a): TOML — unambiguous, good Go library support, comfortable for hand-editing; explicit role field (fail-fast validation over inference); inline secrets with enforced 0600."
- ledgerRefs: ["goals:G1"]
- answer: as recommended

### Q6 — answered

- createdAt: 2026-07-01T23:14:12.197Z
- updatedAt: 2026-07-01T23:22:04.287Z
- author: user
- session: 0047802a-1b44-4fcc-8198-d12359610ad6
- question: "Deployment deliverable scope: are systemd unit files + install/ops documentation in-scope plan deliverables (and at which phase — P1 MVP or later)? Which target platforms/arches must the static binary support (linux/amd64 only, or also linux/arm64 for the edge box)? Is packaging beyond the binary (nix package/module, deb) in scope?"
- context: The prompt's non-functional list names 'single static binary + systemd + simple config', but not when these land or which arches the edge/concentrator actually run. This adds or removes concrete tasks (cross-compile matrix, unit files, install docs).
- suggestions: ["linux/amd64 + linux/arm64, systemd units + install doc at P1 (first deployable phase), no packaging beyond the binary","linux/amd64 only, systemd at the end (P5)","Include nix packaging too"]
- recommendation: "Option (a): both arches cost nothing with CGO_ENABLED=0 cross-compilation; systemd units belong with P1 since that is the first phase worth running unattended on real hardware."
- ledgerRefs: ["goals:G1"]
- answer: as recommended

### Q7 — answered

- createdAt: 2026-07-01T23:14:20.680Z
- updatedAt: 2026-07-01T23:22:16.032Z
- author: user
- session: 0047802a-1b44-4fcc-8198-d12359610ad6
- question: "Observability mechanism: 'metrics/logging per path (loss/RTT/throughput/FEC-overhead)' — via what? (a) structured logs only; (b) a Prometheus /metrics HTTP endpoint (localhost-bound); (c) a local status socket / CLI subcommand (wg-show analogue) for ad-hoc inspection; or a combination? And when — minimal logs from P0 with the metrics surface added when the scheduler/FEC need tuning visibility (P2+)?"
- context: Per-path telemetry already exists internally (the scheduler and adaptive-FEC loop consume it), so this question is only about the export surface. The choice adds distinct tasks (HTTP endpoint + metric registry vs status-socket protocol) and matters for verifying P2-P4 acceptance (the harness could read the metrics endpoint instead of scraping logs).
- suggestions: ["Structured logs from P0 + Prometheus localhost endpoint from P2 (harness asserts against it)","Logs only, keep it minimal","Logs + status CLI subcommand, no Prometheus"]
- recommendation: "Option (a): the P2-P4 acceptance checks (5G bytes ~0, FEC overhead %, recovery counters) are much cleaner asserted against a metrics endpoint than parsed from logs, and it doubles as the ops surface."
- ledgerRefs: ["goals:G1"]
- answer: as recommended

### Q8 — answered

- createdAt: 2026-07-01T23:14:28.200Z
- updatedAt: 2026-07-01T23:22:31.993Z
- author: user
- session: 0047802a-1b44-4fcc-8198-d12359610ad6
- question: "Planning depth vs the P0 spike: should this goal's plan lay out the full P0-P5 task DAG now, or plan P0 in fine grain and only sketch P1-P5 (re-planning them after the spike's findings)? Also: what timebox do you want on the P0 spike?"
- context: P0 exists precisely to discover conn.Bind contract pitfalls (batched send/recv, GSO/GRO, Endpoint identity, amnezia junk packets at the Bind) that shape the P1+ design. A full upfront DAG is reviewable end-to-end but P1+ tasks may need revision after P0; a P0-only plan defers that risk but means another planning round. This changes the shape and count of work milestones emitted now.
- suggestions: ["Full P0-P5 DAG now, with an explicit P0-findings checkpoint task gating P1; revise later phases via /cq:plan:follow-up if the spike invalidates assumptions","Fine-grained P0 only; P1-P5 as placeholder milestones planned after the spike","Fine-grained P0+P1 (through the failover MVP), sketch P2-P5"]
- recommendation: "Option (a) with a ~2-3 day timebox on P0: the architecture is already decided, so most P1+ tasks are stable regardless of spike findings, and a full DAG gives the reviewer the whole picture; the checkpoint task makes the revision point explicit."
- ledgerRefs: ["goals:G1"]
- answer: as recommended

### Q12 — answered

- createdAt: 2026-07-06T21:31:13.293Z
- updatedAt: 2026-07-06T21:36:03.601Z
- author: user
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- question: "Real-host e2e tier — gating vs report-only: when the SSH-orchestrated `realhosts` tier runs a phase's assertions (P0 single-uplink smoke now; multipath/failover/FEC as those phases land), should a PASS be REQUIRED for that phase's task/milestone to be considered done (a hard completion gate that holds the phase milestone un-archivable until real hardware validates), or is it REPORT-ONLY supplementary verification — with the netns `e2e` tier remaining the sole automated completion gate and each real-host run recorded as a separate confirmation?"
- context: The `realhosts` tier is opt-in/manual (explicitly NOT CI) and depends on intermittently-available real hardware (o3 + llm-ubuntu-0). Whether a real-host pass gates phase completion determines each phase task's `acceptance` wording and whether the phase milestones M4-M9 can archive on netns e2e alone. Coupling completion to intermittent hardware would stall the DAG; decoupling keeps every phase shippable on the reproducible emulation while real-host runs confirm separately. This is orthogonal to the already-settled 'not in CI' decision — it is about task/milestone completion semantics, not CI.
- suggestions: ["Report-only: netns `e2e` remains the sole automated completion gate; real-host `realhosts` runs are supplementary confirmations recorded in a report/checklist, never blocking a task or milestone","Hard gate: a phase is not done until its real-host assertions pass on the two hosts","Hybrid: report-only during development, but a single final 'real-host acceptance' task must pass before the whole goal is closed"]
- recommendation: Option (a) report-only — matches the opt-in/manual, hardware-dependent nature; phases stay verifiable and archivable on the reproducible netns fixture, while the real-host tier provides real-network confirmation without stalling the DAG.
- ledgerRefs: ["goals:G1"]
- answer: as recommended

### Q13 — answered

- createdAt: 2026-07-06T21:31:20.997Z
- updatedAt: 2026-07-06T21:36:24.605Z
- author: user
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- question: "Structural placement of the additive verification scope: should the real-host `realhosts` e2e tier PLUS the fixture loss/rate-limit extension (controlled-loss knob + bandwidth cap, unifying/superseding the A7-T10 checkpoint follow-up) live under a NEW dedicated cross-cutting 'real-host + impairment validation' work milestone, whose tasks depend on the phase milestones they validate (real-host smoke after P0/M4; real-host multipath/failover after P1/M5; loss/FEC baseline with P3/M7) — or be folded task-by-task into the existing active phase milestones P0-P5 (M4-M9)?"
- context: The original netns-harness milestone H (M3) that owns the fixture task T7 is already ARCHIVED, so the fixture loss/rate-limit extension cannot be added there — it needs a new task under an active milestone regardless of this choice. A separate cross-cutting milestone keeps each phase milestone archivable on its own netns e2e (real-host + impairment work tracked independently); folding in keeps everything phase-local but couples each phase milestone's archival to the new real-host/impairment work. This decides how many work milestones I append to the goal's `fields.milestones` and where each new task attaches. Existing P1-P5 tasks T11-T30 are untouched (additive only) either way. This pairs with the gating question above (report-only leans naturally toward a separate milestone).
- suggestions: ["New dedicated cross-cutting milestone (e.g. 'RH' — real-host + impairment validation), with tasks depending on the phase milestones they validate","Fold each new task into its corresponding existing phase milestone (M4-M9)","New milestone for the real-host tier, but place the loss/rate-limit fixture extension into the FEC phase (P3/M7), where the baseline is consumed"]
- recommendation: Option (a) new cross-cutting milestone — the scope is inherently cross-phase (smoke -> multipath -> FEC baseline) and is mostly test-infrastructure; isolating it keeps the phase milestones archivable on netns e2e (consistent with a report-only default) while giving the additive real-host/impairment work its own home.
- ledgerRefs: ["goals:G1"]
- answer: as recommended

### Q14 — answered

- createdAt: 2026-07-08T08:25:57.082Z
- updatedAt: 2026-07-08T20:50:52.992Z
- author: user
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- question: "Group D (D7/D8) live-host scope: does resolving D7/D8 include performing the one-time mutations on the LIVE o3 concentrator host (dedup its INPUT chain per D8; apply + reboot-persist the wanbond0 ACCEPT rule per D7), or do the fix task(s) deliver only the REPO-SIDE artifacts — idempotent reboot-persistence provisioning code, the T22 install-doc update, and a TestRealProvision assertion — with the actual live-o3 mutation left as a separate manual op you run?"
- context: "This sets the acceptance field of the Group D fix task(s). D8 is explicitly 'o3 HOST STATE ONLY, not a code defect' (a one-time iptables dedup on the running host); D7 has a repo part (persistence provisioning + doc + test) AND a live-host apply/persist part. Two constraints push toward repo-only automated acceptance: (1) implement-workers run in sandboxed local git worktrees and may not hold /run/agenix/llm-ssh-key or be permitted to mutate a production host; (2) the standing rule that o3 must NEVER be deprovisioned or rebooted into an unrecoverable state, so live iptables edits on it warrant human oversight. M10's realhosts tier is already Q12 report-only / opt-in / manual and never an automated completion gate — the hardening round would inherit that posture."
- suggestions: ["Repo-side only: fix tasks deliver idempotent persistence provisioning + install-doc + a TestRealProvision that asserts the persisted set WHEN run against a host; the live-o3 dedup+persist (D8, and applying D7 on o3) is a separately-tracked one-time manual ops step you execute (recorded in the ledger), NOT an implement-worker acceptance gate","Include the live-o3 mutation in task acceptance (requires granting the validation step SSH access to o3 and an explicit exception to the no-touch-o3 posture)","Repo-side now; defer the live-o3 apply entirely to a later deploy, tracking D8 as ops-only / resolved-on-repo"]
- recommendation: "Option (a): fix tasks are repo-side only and their acceptance is netns/unit-testable (idempotent provisioning + doc + TestRealProvision assertion); the one-time live-o3 dedup + reboot-persist is an ops step you run manually under the never-deprovision-o3 constraint, recorded but not gating an implement-worker. Preserves M10's report-only real-host posture."
- ledgerRefs: ["goals:G1"]
- answer: as recommended, but apparently you forgot that o3 is a TEST machine, it's not under production use!

### Q15 — answered

- createdAt: 2026-07-08T08:26:06.097Z
- updatedAt: 2026-07-08T20:51:28.936Z
- author: user
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- question: "D23 doc/comment sweep (Group E): should the D23 fix task FIRST run an in-fixture (both-daemons-sharing-one-core) netns throughput measurement on the 4-vCPU amd64 host (llm-ubuntu-0) and record that empirical ceiling — making the sweep depend on a real remote measurement — or sweep the four comment/doc locations NOW using the already-measured 1-vCPU figure (12-46 Mbit/s) plus an explicit 'TBD: measure' note for the 4-vCPU host, recording that number opportunistically later?"
- context: "D23's authoritative suggestedFix says to replace the mis-copied 150-170 Mbit/s figure with per-host MEASURED in-fixture ceilings and to 'measure once on the 4-vCPU amd64 host and record it.' The 1-vCPU number (12-46 Mbit/s, docs/p0-findings.md:216-225) already exists; the 4-vCPU number does not. Obtaining it requires provisioning llm-ubuntu-0 and running the netns fixture there — a real-host step outside the implement-worker's local sandbox, and per M10/Q12 real-host runs are opt-in/manual/report-only. This decides whether D23 carries a blocking hardware-validation dependency or ships as a pure in-repo doc edit."
- suggestions: ["Measure-then-sweep: provision llm-ubuntu-0, run the in-fixture measurement once, record the real 4-vCPU ceiling, THEN sweep all four locations with both real numbers (the measurement is a prerequisite step of the D23 task, hardware-validated, executed manually per the report-only posture)","Sweep-now: replace the figure with the 1-vCPU 12-46 Mbit/s number + the '2*cap below the executing host's measured in-fixture ceiling' rule now, leaving an explicit 'TBD-measure' marker for the 4-vCPU host to fill opportunistically","Sweep-now and omit the per-host 4-vCPU number entirely (state only the 1-vCPU measured ceiling + the general 2*cap rule)"]
- recommendation: "Option (a) measure-then-sweep: the whole point of D23 is to stop propagating an unmeasured figure, so record the real 4-vCPU in-fixture ceiling before writing it into the docs. Execute the measurement manually on llm-ubuntu-0 (report-only, not an automated gate) and make '4-vCPU ceiling recorded' a hardware-validation acceptance of the D23 task."
- ledgerRefs: ["goals:G1"]
- answer: as recommended

### Q16 — answered

- createdAt: 2026-07-08T08:26:14.295Z
- updatedAt: 2026-07-08T20:52:19.807Z
- author: user
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- question: "Group B / D26 resolution surface: the round's stated NON-GOAL is 'no new product capability — purely resolving hardening defects.' D26 offers two fixes — (i) derive the adaptive-FEC redundancy map from a NEW target-residual config parameter (inverting the binomial residual for M), which adds a runtime/config surface, or (ii) ship a DOCUMENTED SafetyFactor/RaiseThreshold-per-residual-SLA table (no code surface change). Which is in scope for this hardening round?"
- context: "This sets D26's task type (code+test vs doc-only) and acceptance. Option (i) adds a `target_residual` knob + inversion logic — arguably a new product capability, in tension with the round's non-goal. Option (ii) closes the defect (operators can pick SafetyFactor for a target SLA) without new runtime surface. For reference, I plan to resolve the other Group-B defects per their suggestedFix without asking: D25 = extend the property test over partial-m x partial-k with byte-exact recovery through the single ceiling decoder AND pin the klauspost prefix-stability guarantee (build-time generator-matrix prefix assertion + a reedsolomon version-pin doc note); D24 = account retained-incomplete-past-deadline groups at Stats()/snapshot time so quiescence no longer overstates recovery. Flag if you want either scoped differently."
- suggestions: ["Docs-table only (option ii): a documented SafetyFactor/RaiseThreshold-per-SLA table in the ops/install docs, staying within the 'no new product capability' non-goal","Add the target-residual config parameter (option i): accept a new config surface this round as the more principled fix","Docs-table now, and file a SEPARATE (non-hardening) feature goal for the target-residual config surface if you want it later"]
- recommendation: "Option (a) docs-table only: resolves D26 within the round's explicit 'no new product capability' non-goal with no new runtime surface; if you later want the target-residual knob, that is a separate feature goal (option c)."
- ledgerRefs: ["goals:G1"]
- answer: "Add the target-residual config parameter (option i): accept a new config surface this round as the more principled fix"

## M4

### Q10 — answered

- createdAt: 2026-07-02T22:11:25.112Z
- updatedAt: 2026-07-06T15:37:03.395Z
- author: user
- session: 0047802a-1b44-4fcc-8198-d12359610ad6
- question: "P0 (embed amneziawg-go) approach decision — the sandbox boundary is reached. T8's acceptance is the actual amneziawg tunnel bring-up (WG handshake + ping + iperf3 through the tunnel), which requires /dev/net/tun + CAP_NET_ADMIN. /dev/net/tun is ABSENT in this sandbox (verified), so P0's acceptance and everything gated behind the P0 checkpoint (T10) cannot be verified here. Two ways to proceed, please pick: (a) I implement the SANDBOX-VERIFIABLE portion of P0 now — add amneziawg-go as a dependency, write the pass-through conn.Bind, and unit-test a datagram round-trip through Send + the receive callback on UDP loopback (no TUN) — leaving ONLY the tunnel e2e (T8 acceptance) + baseline (T9) + checkpoint (T10) for the hardware phase; OR (b) defer ALL of P0 to the hardware phase. STRONG RECOMMENDATION regardless: run P0+ from a FRESH Claude Code session on/with host access — a fresh session also restores the proper worktree-isolated multi-agent implement flow (worker + independent review + tiered models), which is unavailable in THIS session (startup-snapshot limitation, per Q9), and is the right vehicle for the substantial P0-P5 work. Answer (a) or (b), and confirm when host root + /dev/net/tun are available, to re-open T8 and resume."
- context: "Completed & verified in-sandbox this run: entire S milestone (T1-T5: module/layout, Nix flake, golangci-lint+CI, TOML config with 0600, structured logging) and H milestone (T6 e2e layering + Q1 constants + manual checklist; T7 netns/netem two-path fixture — both archived). All built/vetted/tested/linted green via nix-provided Go; the netns fixture verified via userns (no real root). Remaining P0-P5 (T8-T30, 23 tasks) is the tunnel/scheduler/FEC/DPI work whose acceptance ultimately needs the real tunnel (TUN) and the hosts. Plan locked (decision K1)."
- ledgerRefs: ["tasks:T8","goals:G1"]
- answer: "Proceed with option (a): re-invoked /cq:advance under the standing 'implement what we can in the sandbox' directive. Implement the sandbox-verifiable slice of P0 now — embed amneziawg-go, write the pass-through conn.Bind behind the portable interface, and unit-test the datagram round-trip on UDP loopback — leaving ONLY the actual tunnel e2e (WG handshake + ping + iperf3 through TUN) for the hardware phase."

### Q11 — answered

- createdAt: 2026-07-06T15:46:12.658Z
- updatedAt: 2026-07-06T19:25:20.042Z
- author: user
- session: 0047802a-1b44-4fcc-8198-d12359610ad6
- question: "Hardware gate for the P0-P5 tunnel e2e. The sandbox-verifiable slice of P0 (T8) is DONE and committed: amneziawg-go v1.0.4 embedded, pass-through conn.Bind over net.UDPConn, loopback round-trip unit test green. What REMAINS for T8 (and is gated here) is the actual tunnel: wire the amneziawg-go device to a TUN + the Bind, drive both roles from config, and run TestP0PassThrough (WG handshake + ping + iperf3 through the tunnel edge<->concentrator). That requires host root + /dev/net/tun, which are absent in this sandbox. Provide host access (your Starlink+5G edge + concentrator VPS, or any Linux host with root + /dev/net/tun) — ideally from a FRESH Claude Code session, which also restores the proper worktree-isolated multi-agent implement flow (per Q9) — then answer this to re-open T8 and drive P0-P5 to completion."
- context: "Landed this run: T8 partial (commit bbdf04a) — internal/bind isolates the engine behind type aliases; Passthrough is implemented directly over net.UDPConn because the engine's StdNetBind recvmmsg/GSO fast path misbehaves under sandbox socket restrictions (a genuine P0 conn.Bind finding that seeds T9's findings doc). go build/vet/test/golangci-lint green; nix build produces the static binary. Everything downstream (T9 baseline, T10 checkpoint, and all of P1-P5) is gated behind the working tunnel, hence behind this hardware gate."
- ledgerRefs: ["tasks:T8","goals:G1"]
- answer: you have ubuntu machine available at ubuntu@o3.7mind.io, that should work for first out-of-sandbox tests

## M12

### Q17 — answered

- createdAt: 2026-07-13T12:28:44.292Z
- updatedAt: 2026-07-13T13:28:33.895Z
- author: user
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- question: "CONTROL protocol: for the pilot, do we wire a LIVE out-of-band CONTROL protocol (explicit rekey / tunnel-state signalling) through the existing reserved Bind chokepoint, or keep the CONTROL primitive dormant/reserved?"
- context: |
    WHAT 'CONTROL' IS TODAY (a built, tested ENVELOPE with no messages and no handler — a primitive, not a protocol): frame.Control{ ControlType uint8; Seq uint64; Payload []byte }. Exists: the wire encode/decode (internal/frame/frame.go), PSK-HMAC authentication, and telemetry.ControlGuard — a per-(peer,ControlType) monotonic anti-replay state machine (built in D4/T44). Does NOT exist: ANY ControlType constant, any sender, any receiver — inbound CONTROL is deliberately DROPPED at the Bind (internal/bind/multipath.go receive default). So it is an authenticated, replay-protected channel that currently carries nothing.
    
    WHAT A CONTROL PROTOCOL WOULD COVER (out-of-band tunnel-level signalling that isn't user payload): (1) coordinated/explicit rekey or session control (the example the ControlGuard comment itself cites); (2) DYNAMIC PATH/POLICY PUSH — one end (typically the concentrator) telling the other to add/remove/reweight a path instead of each side reading its own static config; (3) cross-end PARAMETER NEGOTIATION — FEC ratio, MTU/PMTU, pacing, scheduler policy agreed dynamically rather than set consistently per side; (4) GRACEFUL DRAIN / maintenance / state notifications ('stop sending on this path' ahead of a liveness timeout); (5) explicit flow-control/backpressure between ends.
    
    WHY EACH IS ALREADY COVERED / NOT NEEDED FOR THE PILOT: rekey/session -> WireGuard's INNER protocol already rekeys automatically (~2 min), authenticated + battle-tested; an outer control-plane rekey duplicates it. Liveness / path up-down / failover -> this is the signalling that actually matters for a bonding tunnel and it is ALREADY WIRED, as the PROBE plane: authenticated PROBE frames (Prober/Reflector request-response, high-water anti-replay, session challenge) measure per-path RTT/loss/jitter and drive failover. The live out-of-band plane exists; it just isn't called CONTROL. Endpoint learning / roaming / NAT -> handled by the Bind's dynamic endpoint learning + WG roaming, no messages needed. Path config -> static per-side config + SIGHUP reload suffices for a 2-link known-topology pilot; DYNAMIC path push is SD-WAN territory, and 'not a general SD-WAN product' is an explicit G1 non-goal. FEC/MTU/pacing -> the pilot runs a known fixed config set consistently by the operator; negotiation is an efficiency nicety, not a correctness requirement.
    
    WHY DORMANT IS THE RIGHT DEFAULT: (a) no concrete signalling requirement exists for a supervised pilot; (b) a live control plane is a SECURITY-SENSITIVE surface DROPPED ON PURPOSE — it is exactly where parsing bugs, replay/downgrade attacks, and state-machine complexity enter on an authenticated-but-outer channel; wiring it with no need buys attack surface + versioning burden for no payoff (this is WHY D4/T44 pre-built the anti-replay guard: the defense is ready the day a protocol is defined, and the protocol can wait for a reason); (c) DEFERRING IS FREE — the envelope + guard already exist and are tested, so adding it later is small and well-scoped (define ControlType constants + a handler, route through the existing ControlGuard chokepoint) with NO wire-format change and NO re-architecture.
    
    Net: YAGNI on a security-sensitive surface whose plausible jobs are already done by WireGuard's inner protocol and the wired PROBE plane — with the pleasant property that it is pre-built, so there is no cost to waiting. Wiring a live protocol means designing the actual signalling semantics (which messages, which state transitions, how hub and edge react) — a substantial additive design+build task with its own review surface, and a whole CONTROL-protocol work milestone in the plan. This decision determines whether the plan contains that milestone or none.
- suggestions: ["Keep CONTROL dormant/reserved for the pilot (primitive stays ready, no live protocol shipped)","Wire a live out-of-band rekey/state-signalling CONTROL protocol into scope now"]
- recommendation: "Keep CONTROL dormant/reserved for the pilot. Every job a control protocol would do is already covered (WireGuard's own inner rekey; the wired PROBE plane for liveness/failover; the Bind's endpoint learning for roaming/NAT) or is a static-config concern for a 2-link known-topology pilot (dynamic path push is the SD-WAN non-goal). The primitive is intentionally pre-built and anti-replay-guarded, so activating it later is cheap and non-breaking. FLIP TO 'IN SCOPE' ONLY IF a concrete near-term need exists: you want to add/remove/reweight paths CENTRALLY (from the concentrator) without editing edge config; OR a COORDINATED MAINTENANCE DRAIN (graceful 'stop using this path/tunnel' ahead of the liveness timeout); OR ends that NEGOTIATE FEC/pacing/MTU dynamically instead of by matched static config. If any of those is a pilot goal, answer 'in scope' and it becomes a clean milestone (define the message types + handler on top of the existing guard); otherwise 'keep dormant' loses nothing."
- ledgerRefs: ["goals:G2"]
- answer: as recommended

### Q18 — answered

- createdAt: 2026-07-13T12:28:52.771Z
- updatedAt: 2026-07-13T13:27:39.187Z
- author: user
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- question: "MULTI-CONCENTRATOR failover: does the pilot bring tunnel-termination redundancy (>1 concentrator, failover at the hub) into scope, or keep the single-concentrator model as a standing non-goal?"
- context: Today the concentrator (o3, the public hub) is a single tunnel-termination point. Edge-side multipathing already provides link-level redundancy (multiple WAN paths bonded), but the hub itself is a single point of termination. Adding hub redundancy means concentrator discovery/selection, failover semantics, and state handoff between concentrators — a large additive design that touches the core data path. This decision determines whether the plan contains a multi-concentrator work milestone. It is currently a stated non-goal.
- suggestions: ["Keep the single-concentrator model (hub redundancy remains a non-goal for the pilot)","Bring multi-concentrator / hub-failover redundancy into pilot scope"]
- recommendation: Out of scope for the pilot. Edge multipathing already delivers the link redundancy wanbond exists to provide; concentrator HA is better handled as an operational/deployment concern (DNS/anycast/standby host in front of the hub) than as a wanbond protocol feature. Bring it into scope only if the pilot specifically requires surviving loss of the hub host itself.
- ledgerRefs: ["goals:G2"]
- answer: Bring multi-concentrator / hub-failover redundancy into pilot scope

### Q19 — answered

- createdAt: 2026-07-13T12:29:01.762Z
- updatedAt: 2026-07-13T13:28:16.529Z
- author: user
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- question: "PILOT GATING: must a REAL-LINK SOAK test gate the pilot go/no-go, or is the bandwidth-capped-fixture aggregation measurement + a report-only real-link smoke sufficient to proceed to a supervised pilot?"
- context: This sets the exit criterion of the whole plan and shapes how much validation work is blocking vs. informational. A blocking soak gate (e.g. hours of sustained real-link transfer that must pass before pilot) adds a long-running, environment-sensitive step to the critical path and couples go/no-go to shared test-host availability. The alternative treats the capped-fixture aggregation/bufferbloat measurement (deterministic, in-repo) plus a short report-only real-link smoke as sufficient to PROCEED, and runs any longer soak DURING the supervised pilot rather than as a pre-gate. Per M10/Q12 the real-link tier is report-only discipline, which leans toward the non-blocking interpretation.
- suggestions: ["Real-link smoke (report-only) + capped-fixture aggregation/bufferbloat measurement are sufficient to proceed; soak runs during the pilot","A real-link SOAK must pass as a blocking pre-pilot gate before go/no-go"]
- recommendation: Real-link smoke + capped-fixture aggregation measurement are sufficient to PROCEED to a supervised pilot; run the soak DURING the pilot, not as a blocking pre-gate. The capped fixture gives deterministic, repeatable bufferbloat/pacing evidence; the real-link smoke confirms the two standing hosts bond and fail over; a soak's value is in sustained real traffic, which the supervised pilot itself provides while remaining observable and reversible.
- ledgerRefs: ["goals:G2"]
- answer: as recommended

### Q20 — answered

- createdAt: 2026-07-13T12:29:12.695Z
- updatedAt: 2026-07-13T13:03:36.985Z
- author: user
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- question: "PACING delivery shape (CORE SCOPE 1): once per-path BDP is measured from the capped fixture, do we AUTO-WIRE SizePacingFromBDP into config load so pacing is derived from a declared/measured per-link bandwidth, or ship a DOCUMENTED per-link manual tuning procedure (operator sets pacing by hand), or both?"
- context: This is a delivery-shape choice WITHIN the already-agreed pacing scope (not a whether-to-do question) — the description itself frames item 1 as 'wire SizePacingFromBDP into config load/auto-tuning OR ship a documented per-link tuning procedure', and the answer changes the task DAG. Auto-wiring means new config-load code + tests that turn a per-link bandwidth number into BDP-sized pacing (correct-by-construction, but adds runtime surface and requires the operator to supply a measured link rate). A documented procedure means a docs/runbook task plus keeping pacing operator-driven (less code, but relies on manual tuning per deployment). Pacing currently ships disabled with a synthetic ~115 Mbit/s default and SizePacingFromBDP is an un-wired helper.
- suggestions: ["Auto-wire SizePacingFromBDP into config load: operator supplies a measured per-link bandwidth, config derives BDP-sized pacing; ship with tests","Ship a documented per-link manual tuning procedure only (pacing stays operator-set by hand)","Both: wire the helper into config from a declared per-link bandwidth AND document how to measure that bandwidth"]
- recommendation: Both (option c). Wire SizePacingFromBDP so config derives pacing from a declared per-link bandwidth the operator supplies (making correct pacing reachable without hand-computing BDP), and document the fixture/real-link measurement procedure that produces that bandwidth number. Prefer operator-declared bandwidth over fully automatic runtime auto-tuning for the pilot — auto-tuning adds control-loop risk that a supervised pilot does not yet justify.
- ledgerRefs: ["goals:G2"]
- answer: as recommended

## M18

### Q21 — answered

- createdAt: 2026-07-13T20:58:52.969Z
- updatedAt: 2026-07-13T21:07:54.714Z
- author: user
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- question: "Confirm the scope boundary: is this goal STRICTLY concentrator-side multi-peer (one concentrator process terminating N distinct edges, each a distinct WireGuard peer/pubkey), with NO change to the edge side? Specifically, (a) is edge-side simultaneous aggregation across multiple DISTINCT concentrators explicitly out of scope (it is different from the already-shipped T57/Q18 single-active-hub ordered-endpoint failover), and (b) is the existing single-edge NAT-roaming case (one peer whose source rebinds) considered ALREADY handled, so 'multi-peer' means genuinely distinct peers, not the roaming of one?"
- context: "Grounded: device.Up builds ONE bind.NewMultipath(cfg.Paths, cfg.PSK, ...) for the whole process; the edge already has T57 hub-failover (config.Peer.Endpoints ordered list, reseq.Rebaseline, startHubFailover in device.go) which is single-ACTIVE, not simultaneous aggregation. config.validate already accepts >=1 [[wireguard.peers]] and device.uapiConfig ranges over all peers, but the Bind/reseq/scheduler are singletons. The scope answer decides whether ANY edge-side work is in the plan or the plan is concentrator-only de-singletoning."
- suggestions: ["Concentrator-only multi-peer; edge unchanged; edge-multi-hub and single-edge-roaming both out of scope","Concentrator multi-peer PLUS edge-side simultaneous multi-concentrator aggregation","Something else (describe)"]
- recommendation: Concentrator-only multi-peer for this goal; edge-side simultaneous multi-concentrator aggregation is a separate feature; single-edge roaming is already handled and is not what 'multi-peer' means here.
- ledgerRefs: ["goals:G4"]
- answer: as recommended

### Q22 — answered

- createdAt: 2026-07-13T20:59:05.594Z
- updatedAt: 2026-07-13T21:08:21.925Z
- author: user
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- question: "Adopt a PER-PEER PSK (move psk into [[wireguard.peers]]) as the authenticated path->peer demux enabler? If yes, pin the exact config schema and back-compat rule: (a) does the top-level `psk` REMAIN fully supported as the single-peer default (so every existing single-peer config keeps working byte-for-byte), with a per-peer `psk` under [[wireguard.peers]] only REQUIRED once >1 peer is configured? (b) does validate REQUIRE the per-peer psks to be pairwise DISTINCT when >1 peer (equal psks would defeat authenticated demux)? (c) is the model symmetric on the edge (each edge configured with the single psk that matches its concentrator-side peer entry), i.e. no edge schema change beyond the value it already sets?"
- context: "Grounded: config.Config.PSK is a single top-level Key (config.go:42), config.validate requires it (line 844), config.Peer has PublicKey/Endpoint(s)/AllowedIPs and NO psk field. multipath.NewMultipath takes one psk and builds ONE frame.Codec + telemetry.NewReflector(psk) + per-path telemetry.NewProber(...,cfg.PSK,...). The outer PSK-HMAC PROBE/CONTROL plane is the only authenticated signal below the crypto layer, so a per-peer PSK is what lets an authenticated PROBE identify WHICH peer a path belongs to. This decision drives internal/config schema+validation AND how the Bind is de-singletoned (map keyed by peer, each peerState with its own psk-derived codec/reflector)."
- suggestions: ["Per-peer psk under [[wireguard.peers]]; top-level psk stays the single-peer default; per-peer required + pairwise-distinct only when >1 peer; edge unchanged","Per-peer psk ALWAYS required (drop top-level psk) — a clean break, no single-peer back-compat","Keep one deployment-wide psk + some non-PSK demux (describe how it stays unforgeable)"]
- recommendation: "Per-peer psk under [[wireguard.peers]] as the enabler; keep top-level psk as the single-peer back-compat default; require per-peer psks present and pairwise-distinct when >1 peer; no edge schema change (the edge already sets exactly the psk matching its concentrator peer)."
- ledgerRefs: ["goals:G4"]
- answer: as recommended

### Q23 — answered

- createdAt: 2026-07-13T20:59:15.592Z
- updatedAt: 2026-07-13T21:08:35.653Z
- author: user
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- question: "Is a DATA/PARITY outer-frame WIRE-FORMAT change acceptable for this goal, or is 'no wire change' a hard requirement? The chosen direction (authenticated path->peer binding) should need NO wire change: DATA/PARITY stay unauthenticated with no peer id, and demux is purely by the authenticated path->peer binding table. Confirm that (a) adding a peer-id field to the DATA header is explicitly REJECTED (it would be spoofable and reintroduce the cross-peer resequencer-injection DoS invariant-4 forbids), and (b) the plan must preserve byte-for-byte wire compatibility with already-deployed single-peer edges."
- context: "Grounded: frame.Data carries OuterSeq/PathID/FECGroup/FECIndex/Flags and NO peer id; DATA/PARITY are unauthenticated by design (frame.go wire-model comment, invariant 4); only PROBE/CONTROL carry a PSK-HMAC tag. reseq's whole discontinuity/resync guard assumes DATA is forgeable. A wire change would ripple into frame.DataOverhead, mtu.go InnerMTU sizing, FEC shard coding (OuterSeq||Payload), and cross-version compat. Answering this fixes whether internal/frame is in the refactor surface at all."
- suggestions: ["No wire change; DATA/PARITY unchanged; demux purely via authenticated path->peer binding (peer-id-in-DATA rejected); keep single-peer wire compat","A wire change IS acceptable if planning shows it is necessary (describe the compat story)"]
- recommendation: No wire change. DATA/PARITY stay unauthenticated and peer-id-free; the peer is resolved by the authenticated path->peer binding, not by any outer-frame field. This preserves invariant 4 and wire compat with deployed edges.
- ledgerRefs: ["goals:G4"]
- answer: as recommended

### Q24 — answered

- createdAt: 2026-07-13T20:59:27.135Z
- updatedAt: 2026-07-13T21:09:05.872Z
- author: user
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- question: "How is an inbound path/source attributed to a peer for the VERY FIRST frames, before any authenticated PROBE has bound it (and before the WG handshake completes)? Two sub-decisions: (1) Bootstrap policy for early DATA on an unbound source — GATE (drop DATA until an authenticated PROBE under some peer's psk binds the source->peer, relying on WG handshake/keepalive retransmit), or QUARANTINE it in a provisional per-source resequencer that is adopted once the binding resolves? (2) Peer identification for an unbound authenticated frame — is it acceptable to TRIAL-DECODE an unbound PROBE against each configured peer's psk (O(peers) HMAC verifies) to discover which peer it belongs to, or is a cheaper binding hint required?"
- context: "Grounded: multipath.handleInbound today learns a path's return remote ONLY from an authenticated PROBE (Probe case: ps.setRemote(srcAP)), never from DATA (the D9/D11 fix), and reflects peer probes via telemetry.Reflector. With per-peer psk each peerState has its own psk-derived Codec, so an unbound source's frame must be trial-decoded across peers' codecs to identify the peer. Trial-decode is an O(peers) cost + a potential CPU-DoS surface on spoofed unbound sources (ties to the resource-limit and threat-model questions). This decision shapes the demux table programming in device.go and the Bind receive path."
- suggestions: ["Gate DATA until an authenticated PROBE binds source->peer; identify the peer by trial-decoding the PROBE across peer psks (bounded by max-peers)","Provisional quarantine resequencer per unbound source, adopted on first authenticated PROBE","A cheaper binding hint is required (describe)"]
- recommendation: Gate DATA on an unbound source until an authenticated PROBE binds source->peer (WG retransmits cover the brief gap); identify the peer by trial-decoding the unbound PROBE across configured peer psks, with the cost bounded by the max-peers cap and unauthenticated floods dropped cheaply.
- ledgerRefs: ["goals:G4"]
- answer: as recommended

### Q25 — answered

- createdAt: 2026-07-13T20:59:38.682Z
- updatedAt: 2026-07-13T21:09:25.514Z
- author: user
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- question: When an edge's source rebinds (NAT/roaming) so a path arrives from a NEW source address, how must the path re-bind to the SAME peer without a window where its frames misroute into another peer's resequencer? Is it acceptable that re-binding a moved source to its peer happens ONLY on a fresh authenticated PROBE (under that peer's psk) from the new source — accepting a brief drop/no-route window for that path's DATA until the PROBE re-binds it (covered by WG retransmit and the other still-bound paths of the same bonded edge) — or is a stronger continuity guarantee required?
- context: "Grounded: today a single virt is pinned once to the first learned source (virtualEndpoint), and per-path remote is re-learned on each authenticated PROBE (handleInbound Probe case). The existing T16 re-roam and D11 machinery already re-learn a path's remote from authenticated probes. In the multi-peer world the RISK is that DATA from a moved source, arriving before its re-binding PROBE, could be attributed to the wrong peer's resequencer (cross-peer contamination). Gating re-bind on an authenticated PROBE keeps the binding unforgeable but opens a small window. This decides the roaming/re-bind logic in the demux table."
- suggestions: ["Re-bind a moved source to its peer ONLY on a fresh authenticated PROBE from the new source; unbound/other DATA from that source is dropped (not misrouted) until then","Require a stronger zero-window continuity guarantee (describe the mechanism)"]
- recommendation: Re-bind on the authenticated PROBE only; until the moved source re-binds, its DATA is dropped rather than attributed to any peer (never misrouted). WG retransmit and the edge's other still-bound paths cover the brief window, mirroring the existing D11/T16 re-learn discipline.
- ledgerRefs: ["goals:G4"]
- answer: as recommended

### Q26 — answered

- createdAt: 2026-07-13T20:59:51.158Z
- updatedAt: 2026-07-13T21:09:53.940Z
- author: user
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- question: "Pin the concentrator resource-limit model: (a) is there a CONFIGURED max-peers cap, and what default (the per-peer footprint is a ~2048-frame resequencer ring PLUS per-peer FEC send/recv state PLUS per-peer scheduler/probers/reflector, so N peers multiply memory)? (b) what is the eviction policy for idle/dead peers — evict when the WG session is torn down / liveness has been DOWN past a timeout, or never (static peer set only)? (c) on cap exhaustion, REJECT a new peer's bootstrap, or evict the idlest? Note: is the concentrator peer set STATIC (only the configured [[wireguard.peers]] ever bind) or can peers appear dynamically within that configured set?"
- context: "Grounded: resequencerWindow=2048 (multipath.go), and each peer needs its own resequencer (atomic.Pointer today), fecSend/fecRecv, scheduler, prober set, and reflector — all currently process-singletons. The configured peer set is bounded by [[wireguard.peers]] (config), so 'max peers' may simply be len(peers); but the demux/provisional state for UNbound sources needs its own bound (DoS). This decision sets the peerState map sizing, the eviction lifecycle wired from device.go peer events, and the backpressure branch."
- suggestions: ["Static configured peer set only (cap = number of [[wireguard.peers]]); per-peer state torn down when that peer's WG session/liveness goes away; provisional unbound-source state separately capped","Configured max_peers cap with a default (state the number) + idle-eviction timeout","No cap / no eviction (reject if unsure)"]
- recommendation: "Peers are the STATIC configured [[wireguard.peers]] set, so the steady-state cap is that count; size per-peer state lazily and tear it down when a peer's WG session/liveness is gone; cap the PROVISIONAL unbound-source demux state separately (small, drop-on-exhaustion) to bound the bootstrap DoS."
- ledgerRefs: ["goals:G4"]
- answer: as recommended

### Q27 — answered

- createdAt: 2026-07-13T21:00:02.247Z
- updatedAt: 2026-07-13T21:10:12.162Z
- author: user
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- question: "State the target THREAT MODEL for the path->peer binding so acceptance can test it: (1) With distinct per-peer PSKs, is the required guarantee 'a malicious edge that knows ITS OWN psk can disrupt ONLY its own tunnel, never another peer's resequencing/FEC/scheduling' (i.e. full cross-peer isolation, since it cannot forge an authenticated PROBE under a victim's psk)? (2) For an attacker with NO valid psk who floods spoofed/unbound source addresses, is the accepted bound 'unauthenticated frames are dropped cheaply and provisional demux state is capped, so the flood cannot exhaust memory/CPU or evict a live peer' — accepting only degraded bootstrap latency, never cross-peer corruption?"
- context: "Grounded: DATA/PARITY are unforgeable-DATA by design (invariant 4); only an authenticated PROBE (PSK-HMAC, with monotonic anti-replay + the T38/D12 session challenge in telemetry) can bind a source to a peer. So cross-peer injection requires the victim's psk. The residual surfaces are (i) trial-decode CPU on unbound floods and (ii) provisional-state exhaustion / live-peer eviction — both tie to the demux-bootstrap and resource-limit questions. A written threat model turns the e2e 'one edge's loss/restart does not corrupt another' test into concrete adversary cases."
- suggestions: ["Full cross-peer isolation under distinct psks; no-psk floods bounded to degraded bootstrap only (never corruption or live-peer eviction)","A weaker/different guarantee (describe)"]
- recommendation: "Target full cross-peer isolation: with distinct per-peer psks a peer can disrupt only itself; an attacker without a valid psk is limited to (bounded, capped) bootstrap-latency degradation and can neither corrupt another peer's stream nor evict a live peer."
- ledgerRefs: ["goals:G4"]
- answer: as recommended

### Q28 — answered

- createdAt: 2026-07-13T21:00:13.528Z
- updatedAt: 2026-07-13T21:10:23.809Z
- author: user
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- question: "For /metrics: add a per-peer label to the wanbond_path_* (and the per-peer resequencer/FEC) series so an operator can attribute traffic/loss/recovery to a specific edge? If yes, what is the label KEYED on — a stable config-assigned peer name (recommended, human-readable, bounded), or the WG public key (globally stable but opaque and higher-cardinality-looking)? Confirm the cardinality increase (series multiply by peer count) is acceptable given it is bounded by the max-peers/static peer set, and whether existing single-peer series must stay label-compatible (e.g. omit the peer label, or emit a default, when only one peer)."
- context: "Grounded: metrics.Source (newMetricsSource) reads per-PATH counters (txBytes/rxBytes atomics on pathState, prober RTT/loss, resequencer Stats) off the single Bind; there is no peer dimension today. Per-peer isolation makes per-peer scrape data the operator's primary signal ('is edge A's tunnel healthy independently of edge B'). config.Peer has no name field today, so keying on a peer name may require a small schema add (peer name/id). This decides the metrics.Source shape and any config surface for a peer identifier."
- suggestions: ["Add a `peer` label keyed on a config-assigned peer name (add a name/id field to [[wireguard.peers]]); single-peer configs keep back-compatible series","Key the `peer` label on the WG public key (no new config field)","No per-peer labels for this goal (aggregate only)"]
- recommendation: "Add a per-peer `peer` label keyed on a config-assigned peer name (small [[wireguard.peers]] name field); cardinality is bounded by the static peer set; keep single-peer series back-compatible (omit the label or emit a stable default when only one peer)."
- ledgerRefs: ["goals:G4"]
- answer: as recommended

## M19

### Q29 — answered

- createdAt: 2026-07-13T21:20:01.422Z
- updatedAt: 2026-07-13T21:31:52.450Z
- author: user
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- question: "Default posture: is DNS/hostname endpoint resolution strictly OPT-IN with default IP-only, and does an existing IP-literal config stay byte-for-byte behavior-identical (no new code path taken when no hostname is configured)?"
- context: "The goal's core DPI-resistance constraint is that a plaintext DNS query is a cleartext, on-path, pre-tunnel signal that names a blocklistable host — a regression against the thesis (design.md 'DPI resistance', requirement 6). This codebase already has a firm idiom for exactly this shape: FEC, pacing, adaptive-FEC, and the amnezia block are all default-off and leave the existing datapath byte-for-byte unchanged when unconfigured (config.go FEC L288, Amnezia L517). Confirming the posture pins whether resolution is gated behind an explicit opt-in and whether the IP-literal parse path (netip.ParseAddrPort at config.go:495, multipath.go:1327) must be preserved untouched for non-hostname configs."
- suggestions: ["Opt-in, default IP-only; IP-literal path untouched (matches FEC/amnezia idiom)","On by default (any endpoint may be a hostname), IP still parsed fast-path","Opt-in AND require an explicit acknowledgement flag for the DPI trade-off"]
- recommendation: Opt-in, default IP-only, IP-literal configs byte-for-byte identical — matches the goal's stated direction and the codebase's established default-off feature-block idiom.
- ledgerRefs: ["goals:G5"]
- answer: as recommended

### Q30 — answered

- createdAt: 2026-07-13T21:20:12.483Z
- updatedAt: 2026-07-13T21:32:04.021Z
- author: user
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- question: "Load-time behavior when a configured hostname is NOT yet resolvable at boot (resolver/network not ready): hard-fail config.Load, or DEFER-and-reconcile mirroring the T55 tolerant-startup model — bring the tunnel up on any resolvable/IP endpoints and let a background loop resolve the name later?"
- context: "The T55 tolerant-startup path is a precise, existing template: internal/bind/multipath.go:531-537 defers a path on EADDRNOTAVAIL into m.deferred, and StartReconcileLoop (internal/bind/reconcile.go:60-124, ~1s cadence) retries ListenUDP and promotes on success. An unresolvable hostname at boot is structurally the same situation. The datapath sends to a concrete netip.AddrPort (multipath ParseEndpoint/SetPeerRemote), so a name MUST resolve before any packet egresses on that endpoint — but hard-failing Load would violate the invariant 'do not hard-fail boot on a transient resolver outage'. The decision determines whether resolveEndpoints (config.go:484) may return a config with an as-yet-unresolved hostname placeholder, and whether a deferred-resolution reconcile loop is in scope."
- suggestions: ["Defer-and-reconcile: boot succeeds, background loop resolves later (mirror T55)","Hard-resolve at config.Load, fail boot if any hostname does not resolve","Hybrid: hard-resolve if it is the ONLY/primary endpoint, defer if standbys exist"]
- recommendation: "Defer-and-reconcile, mirroring T55: boot tolerantly, resolve off-path, and only start sending on that endpoint once resolved — preserves the tolerant-startup invariant and makes a single-hostname edge boot even before its resolver is up."
- ledgerRefs: ["goals:G5"]
- answer: as recommended

### Q31 — answered

- createdAt: 2026-07-13T21:20:24.274Z
- updatedAt: 2026-07-13T21:32:16.075Z
- author: user
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- question: "Re-resolution loop — what TRIGGERS a re-resolve, at what CADENCE, and how does a changed IP repoint the bond? Specifically: (a) honor DNS record TTL, a fixed poll interval, on-liveness-loss, or a combination; (b) reuse bind.Multipath.SetPeerRemote to repoint; (c) suppress a no-op repoint when the resolved IP is UNCHANGED?"
- context: "Re-resolution is the goal's actual value (a static IP behind a name is no better than the IP literal). The repoint machinery already exists: SetPeerRemote (multipath.go:1371) repoints every path's remote AND calls resequencer.Rebaseline() plus (via the hub-failover caller) a fresh WG re-handshake — this is deliberately DISRUPTIVE (defect D32: a hub switch restarts the sender's outer-seq, so the resequencer must re-baseline). Therefore repointing to the SAME IP would needlessly drop the session, so a change-detection guard is essential. Go's net.Resolver does not surface TTL directly (net.LookupIP discards it), so honoring TTL needs a resolver seam that exposes it or a fixed interval. The loop would wire in exactly like startHubFailover (device.go:260) with a stopResolution closure (device.go:664-682). The cadence + trigger choice sizes the reconnect latency after a DDNS IP change versus steady-state resolver load."
- suggestions: ["Fixed poll interval + re-resolve on liveness-loss; reuse SetPeerRemote; skip if IP unchanged","Honor DNS TTL (needs a TTL-exposing resolver seam); re-resolve on expiry","On-liveness-loss ONLY (re-resolve when all paths to the name go DOWN), no timer"]
- recommendation: Fixed poll interval (bounded, e.g. reconcile-cadence-scale) PLUS an immediate re-resolve on liveness-loss, repoint via SetPeerRemote ONLY when the resolved AddrPort actually changed. TTL-honoring is a nice-to-have gated on whether we add a TTL-exposing resolver seam.
- ledgerRefs: ["goals:G5"]
- answer: as recommended

### Q32 — answered

- createdAt: 2026-07-13T21:20:34.322Z
- updatedAt: 2026-07-13T21:32:49.555Z
- author: user
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- question: "Multi-record handling: when a hostname resolves to SEVERAL A/AAAA records, do we pick the first, expand them into extra ordered endpoints (feeding hub-failover), or do happy-eyeballs? And what is the IPv4/IPv6 preference?"
- context: "config.Peer.Endpoints is an ordered []netip.AddrPort that T57 hub-failover consumes as active(0)/standbys(1:) with WRAP round-robin (failover.go). A multi-record name maps naturally onto that list — but expanding one name into N ordered endpoints changes failover semantics (a re-resolve could add/remove standbys, and hubFailover holds an IMMUTABLE snapshot of endpoints with an active idx, so a changing record set races that snapshot — see the hub-failover-interaction question). Picking one address is simplest and keeps a 1:1 name->endpoint mapping. IPv4/IPv6 preference matters because a path's local bind family must match the remote family; the send path uses a single netip.AddrPort per path."
- suggestions: ["Pick one (first, or first matching the path's address family); 1:1 name->endpoint","Expand all records into ordered endpoints feeding hub-failover","Happy-eyeballs (race v4/v6, keep the winner)"]
- recommendation: "Pick a single address per name (prefer the family that matches available local paths; document the tie-break) for a clean 1:1 name->endpoint mapping. Expanding into the failover list is a larger, separable change with real interaction complexity — defer it unless explicitly wanted."
- ledgerRefs: ["goals:G5"]
- answer: Expand all records into ordered endpoints feeding hub-failover

### Q33 — answered

- createdAt: 2026-07-13T21:20:50.526Z
- updatedAt: 2026-07-13T21:33:10.820Z
- author: user
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- question: "Resolver privacy: use the SYSTEM resolver (leaks a plaintext DNS query naming the concentrator), DoH/DoT (more machinery; still leaks SNI + timing to the DoH provider), or the system resolver with a DOCUMENTED DPI trade-off left to the operator? And what is the concrete SECURITY ACCEPTANCE TARGET this must meet?"
- context: "This is the crux of the DPI-resistance tension (goal point c). wanbond's thesis is a high-entropy, unfingerprintable wire; a plaintext DNS lookup for the concentrator name is a pre-tunnel cleartext signal that names a blocklistable host and reveals timing over an unprotected channel. DoH/DoT reduce the on-path plaintext but add a TLS client dependency and still leak SNI/timing to (and trust in) the DoH resolver — and the existing p5_dpi_test.go wire audit only governs the TUNNEL wire, not a pre-tunnel resolver. Per this project's operationalism norm the plan needs a TESTABLE acceptance criterion, not 'is private': e.g. 'with DNS opt-in OFF, zero DNS traffic and the wire audit is unchanged'; 'with system resolver, the operator-facing docs state the exact leaked artifact (hostname in cleartext DNS)'. The choice sizes scope enormously (system resolver = small; DoH = a new subsystem)."
- suggestions: ["System resolver + explicit documented DPI trade-off (smallest); default-off already contains the leak","System resolver now, design the seam so DoH/DoT can be added later without a datapath change","DoH/DoT in-scope now as a first-class private-resolver option"]
- recommendation: System resolver via an injectable resolver SEAM, with a documented DPI trade-off and a testable acceptance target (opt-in OFF => zero DNS + wire audit unchanged; opt-in ON => operator docs state the exact cleartext artifact). Design the seam so DoH/DoT is a later drop-in, but do not build it now unless required.
- ledgerRefs: ["goals:G5"]
- answer: DoH/DoT in-scope now as a first-class private-resolver option

### Q34 — answered

- createdAt: 2026-07-13T21:21:03.589Z
- updatedAt: 2026-07-13T21:33:53.876Z
- author: user
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- question: "Hub-failover interaction: may the ordered endpoints list MIX hostnames and IP literals? And how do re-resolution and an in-progress hub-failover switch COORDINATE — which endpoint does re-resolution target, and does a re-resolve override a failover selection or vice-versa?"
- context: "This is the hardest composition concern and it is concrete, not speculative. hubFailover (internal/device/failover.go:72-97) holds endpoints []netip.AddrPort as an IMMUTABLE snapshot taken at construction, tracks the active idx, and BOTH controllers would call the same bind.Multipath.SetPeerRemote. If a re-resolution loop repoints endpoint[idx]'s AddrPort out from under hubFailover, its snapshot goes stale and the two can fight over the bond's remote (each SetPeerRemote also triggers a resequencer Rebaseline + re-handshake, so a fight = repeated session drops). Options: (a) re-resolution only ever repoints the CURRENTLY-ACTIVE endpoint and defers to failover for selection; (b) make the endpoint list mutable/owned by one coordinator; (c) forbid mixing (a hostname endpoint list is single-entry, so failover and DNS are mutually exclusive per peer). The answer decides whether failover.go's endpoints field must become resolvable/mutable or stays IP-only with DNS handled by a separate single-endpoint path."
- suggestions: ["Allow mixing; ONE coordinator owns endpoint selection, re-resolution only rewrites the active entry's IP and never advances idx","Forbid mixing for v1: a hostname endpoint is single-entry only (DNS XOR multi-endpoint failover per peer)","Allow mixing; make hubFailover's endpoint list mutable and re-resolution updates entries in place under a shared lock"]
- recommendation: "For v1, keep them decoupled: re-resolution only ever rewrites the ACTIVE endpoint's resolved IP (never advances the failover idx), and failover continues to own selection. Whether to also FORBID mixing hostnames into a multi-entry list for v1 (simplest, smallest blast radius) is the key sub-decision I need your call on."
- ledgerRefs: ["goals:G5"]
- answer: Allow mixing; make hubFailover's endpoint list mutable and re-resolution updates entries in place under a shared lock

### Q35 — answered

- createdAt: 2026-07-13T21:21:15.533Z
- updatedAt: 2026-07-13T21:34:05.655Z
- author: user
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- question: "Config surface: introduce a NEW field for hostname endpoints (e.g. an explicit endpoint_hostname / endpoints_dns), or OVERLOAD the existing endpoint/endpoints fields to accept either an IP:port literal or a host:port name? How should validation distinguish the two forms and report errors?"
- context: "Today Peer has Endpoint (legacy single) and EndpointsRaw (ordered []string), mutually exclusive, both normalized by resolveEndpoints (config.go:452-507) via netip.ParseAddrPort which REJECTS hostnames. Overloading keeps the surface small and lets a hostname sit anywhere in the ordered list, but it silently changes the semantics of the existing fields and couples the opt-in DPI gate to per-value parsing (harder to make DNS explicitly opt-in). A separate field makes the opt-in explicit and greppable and keeps the IP path's parse untouched, at the cost of a third mutually-exclusive input form. Validation must cleanly separate 'is this an IP:port or a host:port' (a bare ':port' split then ParseAddr-vs-treat-as-name) and give a clear error (e.g. reject a hostname when DNS is not opted in)."
- suggestions: ["New explicit field/flag (opt-in is greppable; IP path untouched; hostname clearly gated)","Overload endpoint/endpoints to accept either form; a per-entry parse decides IP vs name","Overload, but require a peer-level dns=true flag before any hostname entry is accepted"]
- recommendation: Overload endpoint/endpoints to accept either form BUT gate hostnames behind an explicit per-peer opt-in flag (so the default-off DPI posture stays explicit and greppable, the IP-literal path is unchanged, and a hostname without the flag is a clear validation error). Confirm you prefer this over a wholly separate field.
- ledgerRefs: ["goals:G5"]
- answer: as recommended

### Q36 — answered

- createdAt: 2026-07-13T21:21:27.222Z
- updatedAt: 2026-07-13T21:34:12.988Z
- author: user
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- question: "Acceptance bar for 'done': is the required e2e proof a NETNS test where an edge dials the concentrator BY NAME (local hosts-file/resolver entry), the concentrator's IP CHANGES mid-session, and the edge re-resolves and reconnects with the tunnel surviving — plus injected-resolver unit tests for defer-on-lookup-failure and repoint-on-changed-IP? And is a report-only realhosts extension in or out of scope for v1?"
- context: "The goal's testing direction names these, and the harness exists: test/e2e/failover_test.go already exercises a mid-session concentrator switch over netns, and p5_dpi_test.go audits the tunnel wire — so a DNS e2e is an incremental addition, not new infrastructure. The unit seams needed are an injected resolver (like the injected clock telemetry.Clock already used across failover/reconcile tests) and a fake clock for cadence. Pinning the acceptance bar is what makes the plan's tasks testable (operationalism): each task's 'done' should map to one of these observable checks. The realhosts (M10/Q12 report-only) discipline is explicitly 'if feasible' in the goal — I need to know whether to plan it as a task or leave it out of v1."
- suggestions: ["Netns IP-change-survival e2e + injected-resolver/fake-clock unit tests are the v1 bar; realhosts report-only is optional/out","Same, PLUS a realhosts report-only DNS check as an in-scope task","Unit tests only for v1; netns e2e deferred"]
- recommendation: Make the netns 'dial-by-name, IP changes mid-session, tunnel survives' e2e plus injected-resolver unit tests (defer-on-failure; repoint-on-change via SetPeerRemote with a fake clock) the v1 acceptance bar, and treat realhosts report-only as a stretch task included only if it adds signal cheaply. Confirm this bar.
- ledgerRefs: ["goals:G5"]
- answer: as recommended

## M29

### Q37 — answered

- createdAt: 2026-07-13T22:54:57.160Z
- updatedAt: 2026-07-13T23:07:56.630Z
- author: user
- session: cac93b81-5292-42e3-b77e-962544c75e54
- question: "Sequencing vs the D35-D40 investigations: which improvements do you consider HARD-GATED on a companion defect's root-cause/fix (so the plan must order them behind it), vs immediately plannable now assuming the D35-D40 fixes land independently? Specifically, should I6 (accept 0.0.0.0/0) be gated on D35, and should I3/I4's acceptance reference D37/D39?"
- context: G6 must COMPOSE with D35-D40 (owned by investigate-flow under M28), not duplicate them. I6's '0.0.0.0/0' acceptance is the one clear hard dependency (D35 wedges the handshake on 0.0.0.0/0). I1 (link-up), I2 (session metric), I4 (log downgrade) and I5 (bind toggle) read as independently plannable now. Getting the gating right determines task dependsOn edges and whether this goal can reach `planned` before D35-D40 resolve.
- suggestions: ["Gate ONLY I6's 0.0.0.0/0 acceptance on D35; keep the split-allowed_ips path and everything else (I1/I2/I3/I4/I5) unblocked and plannable now","Gate all defect-related improvements (I3->D39, I4->D37, I6->D35) behind their defects; plan only the fully-independent items this round","No gating: plan everything now, assume D35-D40 fixes are in place, express no cross-goal ordering"]
- recommendation: Gate only I6's 0.0.0.0/0-acceptance on D35 (the split-allowed_ips full-tunnel path stays unblocked); plan I1/I2/I3/I4/I5 now as independent. I3/I4 IMPROVE the diagnostics around D37/D39 but do not require their fixes, so reference them in acceptance without a hard dependsOn.
- ledgerRefs: ["goals:G6"]
- answer: as recommended

### Q38 — answered

- createdAt: 2026-07-13T22:55:06.138Z
- updatedAt: 2026-07-13T23:08:19.229Z
- author: user
- session: cac93b81-5292-42e3-b77e-962544c75e54
- question: "I7 (interface/route persistence across daemon restarts): in scope as a CODE change this round, or docs-only (bless the C4 oneshot pattern) with persistent-tun deferred to a follow-up goal?"
- context: Recreating wanbond0 on every restart drops every address/route/rule referencing it. The code fix (persistent TUN via TUNSETPERSIST or a keep-device lifecycle) is a substantial change to device creation/teardown and interacts with D39 (NM flush) and the addressing model. The docs-only path just blesses the `PartOf=` oneshot (C4) that rebuilds state after daemon start. These are very different effort/risk profiles and shape whether a device-lifecycle task exists in the DAG.
- suggestions: ["Docs-only this round (ship/bless the C4 oneshot pattern); file a follow-up idea for persistent-tun code","Code this round: make wanbond0 persistent across restarts (persistent TUN), plus C4 docs as fallback for non-networkd hosts","Both: persistent-tun code AND the oneshot recipe documented as the belt-and-suspenders answer"]
- recommendation: Docs-only this round via C4 (bless the `PartOf=` oneshot), and file a follow-up idea for persistent-TUN code. Persistent-tun touches device lifecycle + D39/NM interactions and is better done as its own investigated change than bundled into this operability/docs round.
- ledgerRefs: ["goals:G6"]
- answer: "Both: persistent-tun code AND the oneshot recipe documented as the belt-and-suspenders answer"

### Q39 — answered

- createdAt: 2026-07-13T22:55:14.638Z
- updatedAt: 2026-07-13T23:08:41.836Z
- author: user
- session: cac93b81-5292-42e3-b77e-962544c75e54
- question: "I8 (verify standby-path liveness is BIDIRECTIONAL — observed path_up{5g}=1 with tx{5g}=0): handle as a verification task INSIDE this goal, or hand to investigate-flow first as a suspected latent defect (refile/fix only if confirmed)?"
- context: "The goal text itself flags 'possible latent defect: probes/echoes may only prove one direction for an idle standby — if investigation confirms, refile as a defect.' If failover can select a path that only proved RECEIVE, that is a correctness defect in path selection, not an improvement. Whether this becomes a G6 task or a separate defect determines if any code lands here."
- suggestions: ["Hand to investigate-flow now (file a defect linked to G6); keep I8 out of this plan's code scope until a root cause is confirmed","Plan a verification task in G6 (add a bidirectional-liveness check/test); if it surfaces a real defect, refile then","Defer entirely: note as a known observation, no action this round"]
- recommendation: File it now as an investigate-flow defect (it is a potential correctness fault in failover selection, tx=0 on an 'up' standby). Keep I8's fix out of this goal's code scope; this goal composes with the outcome. A cheap netns bidirectional-liveness test can live here regardless.
- ledgerRefs: ["goals:G6"]
- answer: Plan a verification task in G6 (add a bidirectional-liveness check/test); if it surfaces a real defect, refile then

### Q40 — answered

- createdAt: 2026-07-13T22:55:23.705Z
- updatedAt: 2026-07-13T23:08:50.519Z
- author: user
- session: cac93b81-5292-42e3-b77e-962544c75e54
- question: "C1 (NetworkManager unmanaged-devices) and C4 (addressing/persistence oneshot): docs-only, or ALSO ship packaged artifacts in the repo — a NM `unmanaged-devices` conf.d drop-in and/or a templated `wanbond-addressing@.service` oneshot unit alongside the existing systemd units?"
- context: "Every NM edge box (RPi OS/Debian/Ubuntu desktop) needs the `unmanaged-devices=interface-name:wanbond0` drop-in or NM flushes the address (D39/D5). The oneshot that re-applies address+link-up+policy rules+routes+SNAT is load-bearing and a plain ExecStartPost races tun creation (R27 fixed one such race). Shipping these as real files vs prose changes the deliverable shape (packaging tasks + tests vs docs edits)."
- suggestions: ["Docs-only: recipes in install.md, operator hand-assembles the drop-in and oneshot","Ship both: a NM conf.d drop-in file AND a templated oneshot unit in the repo/package, with docs pointing at them","Ship the NM drop-in only (cheap, high value); keep the oneshot documented because it is host-specific"]
- recommendation: Ship both as packaged, version-controlled artifacts (NM conf.d drop-in + templated `wanbond-addressing@.service` oneshot with correct ordering to avoid the R27 tun-creation race), with C1/C4 docs referencing them. Docs alone repeat the exact footgun the doc is warning about.
- ledgerRefs: ["goals:G6"]
- answer: as recommended

### Q41 — answered

- createdAt: 2026-07-13T22:55:32.642Z
- updatedAt: 2026-07-13T23:09:10.401Z
- author: user
- session: cac93b81-5292-42e3-b77e-962544c75e54
- question: "I6 automation boundary vs C3/C6 documented manual steps: how much of the full-tunnel client-LAN plumbing should `mode=\"default-route\"` AUTOMATE from the daemon, vs stay documented recipes? Specifically, should the daemon program edge policy-route + SNAT-to-tunnel-IP and/or concentrator ip_forward + MASQUERADE + FORWARD-ESTABLISHED accept, or only the edge-side default-route into wanbond0 with the /1+/1 split?"
- context: C3 is the primary, entirely-undocumented use case. Programming host nftables/forwarding and policy routing FROM the daemon is high-blast-radius and host-specific (nft vs iptables, table numbering, existing operator rules), whereas wiring the edge default route + internal allowed_ips split is contained. This decides whether I6 is a thin, contained feature or a large host-network-programming feature.
- suggestions: ["Thin I6: daemon applies only the internal /1+/1 allowed_ips split and wires the edge default-route into wanbond0; client-LAN SNAT + concentrator NAT stay documented, validated recipes (C3/C6)","Full I6: daemon also programs edge policy-route + SNAT and concentrator MASQUERADE/forward/conntrack rules","Middle: daemon automates the edge side (default-route + SNAT) but leaves concentrator NAT/forwarding documented"]
- recommendation: "Thin I6 (option 1): automate only the edge default-route + internal /1+/1 split; keep client-LAN SNAT and concentrator NAT/forwarding as documented, validated C3/C6 recipes. Daemon-programmed host nftables/policy-routing is high-blast-radius, host-specific, and likely to collide with operator-owned rules."
- ledgerRefs: ["goals:G6"]
- answer: as recommended

### Q42 — answered

- createdAt: 2026-07-13T22:55:39.648Z
- updatedAt: 2026-07-13T23:09:18.339Z
- author: user
- session: cac93b81-5292-42e3-b77e-962544c75e54
- question: "I5 config surface for forcing source-IP binding (opt out of device-bind): a per-path `bind = \"source\"` field on each `[[paths]]`, a single global toggle, or both (global default + per-path override)?"
- context: "Config is plain TOML (internal/config/config.go — no baboon versioning cost to add a field). The existing per-path shape already carries `source_addr` on `[[paths]]`, and the device-bind decision (selectDeviceBinds) is per-source. A per-path field composes naturally with source_addr; a global toggle is simpler but coarser. This sets the config schema and the unit-test surface for I5."
- suggestions: ["Per-path `bind = \"source\"|\"device\"|\"auto\"` on each [[paths]], defaulting to today's auto behavior","Single global toggle (e.g. [scheduler] or top-level bind_mode)","Both: a global default with an optional per-path override"]
- recommendation: Per-path field with an optional global default (option 3, defaulting to today's `auto`). It matches the existing per-source `source_addr` shape, lets a mixed edge pin only the VLAN-per-WAN paths, and keeps the common case zero-config.
- ledgerRefs: ["goals:G6"]
- answer: as recommended

### Q43 — answered

- createdAt: 2026-07-13T22:55:47.434Z
- updatedAt: 2026-07-13T23:09:24.223Z
- author: user
- session: cac93b81-5292-42e3-b77e-962544c75e54
- question: "Batch scope: deliver ALL of I1-I8 + C1-C6 in this single goal, or cut a must-have first round (observability + link-up + core docs) and defer the rest to a follow-up goal?"
- context: This is a large, mixed batch (code + packaging + docs). Splitting reduces review/merge blast radius and lets the highest-value operability wins (link-up, session metric, log downgrade, the undocumented full-tunnel recipe) land fast, independent of the I5/I6/I7 design decisions above. This is a priority axis distinct from the defect-gating in the sequencing question.
- suggestions: ["All-in-one: I1-I8 + C1-C6 in G6 (subject to the gating/scoping answers above)","Must-have first round in G6: I1, I2, I3, I4 + C1, C2, C3, C5, C6 (docs); defer I5, I6, I7, I8 to a follow-up goal","Something else — specify which items are must-have this round"]
- recommendation: Keep all docs (C1-C6) + the low-risk code (I1, I2, I3, I4) plus I5 (thin, per your I5 answer) in this goal; land I6 as the thin edge-side variant if D35 timing allows, and defer I7-code and I8-fix to follow-ups. This front-loads the operability + full-tunnel-docs wins with minimal blast radius.
- ledgerRefs: ["goals:G6"]
- answer: as recommended

## M28

### Q44 — answered

- createdAt: 2026-07-14T08:57:18.103Z
- updatedAt: 2026-07-14T12:01:26.625Z
- author: user
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- question: "D35 (0.0.0.0/0 handshake wedge): read-only investigation is exhausted and the production path is already mitigated (T107 split shield). Localizing the residual upstream-engine wedge requires a PRIVILEGED 3-arm handshake repro that the sandbox cannot run (no CAP_NET_ADMIN for netns) — it must run on the e2e hosts (o3.7mind.io + llm-ubuntu-0, the G2 pattern). Authorize/queue that privileged repro (drive a real WG handshake with a LITERAL /0 in the UAPI set bypassing the split, vs /32, vs the /1+/1 split; amneziawg-go verbose + pcap on the receiver; also pin o3's actual amneziawg-go commit vs go.mod v1.0.4), OR confirm the split shield is a sufficient disposition and D35 can be closed against the shield with the upstream engine bug tracked separately."
- context: "H1 adjudged uncertain: the T107 shield is CONFIRMED (device.go:1071-1080/1052-1060, unconditional split before UAPI render — config /0 can no longer reach the engine). The originally-stated trie-zero-length mechanism is RULED OUT against amneziawg-go v1.0.4 (allowedips insert/lookup handle cidr==0 cleanly; the handshake-response path never consults the trie — receive.go:400-417/521-543). The runtime wedge is real but unlocalized read-only; it needs the privileged repro above. No shipped e2e reproduces it (default_route_test.go asserts route wiring only)."
- ledgerRefs: ["defects:D35"]
- answer: This is not a question - you have access to the hosts in question and can run privileged tests on them. Keep in mind that o3 has a production instance, use another port/device to run your tests.

## M48

### Q45 — answered

- createdAt: 2026-07-14T11:44:03.256Z
- updatedAt: 2026-07-14T11:54:43.888Z
- author: user
- session: be1a85fd-55c8-4654-ae42-672792fc0238
- question: "Network exposure: must the monitoring UI stay loopback-only (like the existing /metrics endpoint), or should it be reachable off-host (e.g. from a phone/laptop on the LAN behind the edge)?"
- context: The existing metrics server (internal/metrics/server.go) enforces loopback-only binding as a hard fail-fast invariant (ErrNonLoopbackBind, plus an act-then-verify check on the kernel-bound address) because it exposes per-path operational data. A live-monitoring webpage is most useful viewed from a browser, which for loopback-only means either a browser on the box itself or an SSH port-forward. Allowing a non-loopback bind materially changes the security design (auth becomes mandatory, TLS becomes a question). This decision drives the whole plan shape.
- suggestions: ["Loopback-only, same invariant as /metrics; remote viewing via `ssh -L` port-forward","Loopback default, but permit an explicit opt-in non-loopback bind gated on auth being configured (fail-fast if non-loopback without auth)","LAN-reachable by default with mandatory auth"]
- recommendation: "Option 2: keep loopback as the default and the documented posture, but allow an explicit non-loopback bind that hard-requires the auth mechanism (fail-fast at config load otherwise). This keeps the safe default while making the UI actually usable from the LAN behind an edge box."
- ledgerRefs: ["goals:G12"]
- answer: as recommended

### Q46 — answered

- createdAt: 2026-07-14T11:44:10.340Z
- updatedAt: 2026-07-14T11:54:56.583Z
- author: user
- session: be1a85fd-55c8-4654-ae42-672792fc0238
- question: "Listener topology: should the UI + WebSocket share the existing metrics listener/mux (new routes next to /metrics on the same port), or get a dedicated listener with its own [monitor] config block and port?"
- context: "internal/metrics.Server currently serves exactly one route (/metrics, Prometheus text format) on the [metrics] listen address. Adding / (page) and /ws (live updates) to that mux couples the scrape surface to the UI surface: auth added for the UI would either also gate Prometheus scrapes (breaking scrapers) or need per-route exemptions. A separate listener keeps /metrics a bare unauthenticated Prometheus surface and lets the monitor listener carry auth uniformly, at the cost of a second port and config block."
- suggestions: ["Separate [monitor] listener/port; [metrics] unchanged","Same listener: add / and /ws to the metrics mux, auth-exempt /metrics","Same listener and auth everything including /metrics"]
- recommendation: "Separate [monitor] listener. Uniform auth on the new surface, zero behaviour change to the existing scrape endpoint, and the exposure decision (previous question) can then differ between the two surfaces."
- ledgerRefs: ["goals:G12"]
- answer: as recommended

### Q47 — answered

- createdAt: 2026-07-14T11:44:21.470Z
- updatedAt: 2026-07-14T11:55:21.663Z
- author: user
- session: be1a85fd-55c8-4654-ae42-672792fc0238
- question: "Auth threat model and mechanism: which adversary must the local API resist, and which authentication mechanism do you want? (a) other unprivileged local users/processes on the same host calling the loopback API, (b) the operator's own browser being lured to a malicious page that attacks the localhost API cross-origin (DNS rebinding / CSRF), or (c) both?"
- context: "This is the goal's stated open question. The mechanisms differ per adversary. Against (b), strict Host/Origin validation on every request and on the WebSocket upgrade suffices and needs no secret. Against (a), a secret is required: a static bearer token in the TOML config (already enforced mode 0600, so readable only by the daemon user/root), or a per-boot random token written to a 0600 file and logged, either presented once via URL and then held in a cookie/localStorage. A Unix-domain socket with SO_PEERCRED gives the strongest local-caller identity but browsers cannot speak to unix sockets, so it fits a CLI, not an embedded webpage. Note the daemon runs privileged (TUN device), so on a single-admin box adversary (a) may be out of scope by definition."
- suggestions: ["Origin/Host validation only (defends the browser vector; any local process can still read stats)","Static token in the 0600 TOML config + Origin/Host validation","Per-boot random token (0600 file, path logged at startup) + Origin/Host validation","Unix socket + SO_PEERCRED (CLI-only, incompatible with a browser UI)"]
- recommendation: "Option 2: static token in the config (generated by the operator or documented `openssl rand` one-liner), presented as ?token= once and set as a SameSite=Strict cookie, plus unconditional Host/Origin validation on the WS upgrade and API routes. Covers both adversaries with no new files or state; the 0600 config already protects the secret exactly as it protects the WG private key."
- ledgerRefs: ["goals:G12"]
- answer: as recommended

### Q48 — answered

- createdAt: 2026-07-14T11:44:27.540Z
- updatedAt: 2026-07-14T11:55:42.760Z
- author: user
- session: be1a85fd-55c8-4654-ae42-672792fc0238
- question: Is the monitoring UI strictly read-only (a dashboard), or should v1 also expose control actions (e.g. trigger failover, SIGHUP-equivalent config reload, enable/disable a path)?
- context: Read-only vs control changes the stakes of the auth question, the API surface, and the test burden substantially. The daemon already has SIGHUP for config reload, so a reload button would duplicate an existing control path. Everything the goal text names (link quality/peer/loss/RTT/FEC statistics) is read-only and already available via the metrics Source interface (Paths/FEC/Reseq/Session/PeerNames snapshots).
- suggestions: ["Read-only dashboard only","Read-only plus a config-reload button","Full control surface (failover, path enable/disable)"]
- recommendation: Read-only for v1. It matches the goal text, keeps the unauthorized-local-call risk to information disclosure rather than tunnel manipulation, and control actions can be a follow-up goal.
- ledgerRefs: ["goals:G12"]
- answer: as recommended

### Q49 — answered

- createdAt: 2026-07-14T11:44:36.327Z
- updatedAt: 2026-07-14T11:57:06.799Z
- author: user
- session: be1a85fd-55c8-4654-ae42-672792fc0238
- question: "Frontend toolchain constraint: hand-written vanilla HTML/CSS/JS embedded via go:embed (no Node/npm build step in the repo), or is introducing a JS build toolchain (e.g. Vite + a framework) acceptable? Relatedly: OK to add one Go WebSocket dependency (github.com/coder/websocket), or hand-roll the server side?"
- context: The repo is currently pure Go (go.mod has no web/JS anything; golang.org/x/net is present but its websocket package is deprecated and unsuitable). A single embedded vanilla page keeps the build `go build`-only and the binary self-contained, at the cost of hand-writing the reconnect/health logic per the /resilient-ws-ui skill guidelines (which are framework-agnostic and fit vanilla JS fine). A toolchain buys niceties (TypeScript, components) but adds a second build system to CI and the release process. On the server side, coder/websocket is the maintained, minimal, widely-used choice; hand-rolling RFC 6455 is possible but not a good use of effort.
- suggestions: ["Vanilla single-page, go:embed, zero JS toolchain; add coder/websocket for the server","Vanilla page but hand-rolled WS server (zero new deps)","Vite+TypeScript toolchain, build output go:embed-ed"]
- recommendation: "Option 1: vanilla embedded page plus coder/websocket. Keeps the toolchain Go-only and the binary self-contained; one small, well-maintained dependency is the right trade against hand-rolling the WS protocol."
- ledgerRefs: ["goals:G12"]
- answer: "Vite+TypeScript toolchain, build output go:embed-ed"

### Q50 — answered

- createdAt: 2026-07-14T11:44:45.693Z
- updatedAt: 2026-07-14T11:57:52.410Z
- author: user
- session: be1a85fd-55c8-4654-ae42-672792fc0238
- question: "Data scope and cadence for v1: which statistics, at what push interval, and with how much history? Specifically: (a) is the existing metrics Source snapshot set (per-(peer,path) RTT/jitter/loss/throughput/up, FEC counters, resequencer counters, WG session state) the right v1 payload, or is anything missing? (b) what update interval (e.g. 1s)? (c) instantaneous values only, or client-side rolling history with sparklines/graphs (last N minutes, browser memory only)? (d) on the concentrator, per-peer sections for all bound peers?"
- context: The metrics Source interface (internal/metrics/metrics.go) already snapshots exactly the categories the goal names, so the cheapest design pushes those same snapshots as JSON over the WebSocket on a timer — no new instrumentation. Anything beyond it (e.g. per-path event log, handshake history, packet captures) means new plumbing in internal/telemetry/device and grows the plan. History kept server-side (ring buffers) vs client-side (browser only, lost on reload) is a real scope fork.
- suggestions: ["Source snapshots as-is, 1s push, client-side-only rolling ~5min sparklines, per-peer sections on the concentrator","Source snapshots, 1s push, instantaneous values only (no graphs)","Snapshots + server-side ring-buffer history so a freshly opened page shows the recent past"]
- recommendation: Option 1. It reuses the existing snapshot plumbing verbatim, gives the 'live link quality' feel via client-side sparklines, and loses nothing that matters on page reload; server-side history can be a follow-up if wanted.
- ledgerRefs: ["goals:G12"]
- answer: as recommended

## M50

### Q51 — answered

- createdAt: 2026-07-14T12:16:25.034Z
- updatedAt: 2026-07-14T12:22:59.940Z
- author: user
- session: 915ea040-10d3-4f13-9cf2-ed8e5149babb
- question: "Item 3(ii) 'latency/priority class so control/probe/ICMP frames aren't dropped under pacer overload': what is the ACTUAL in-scope traffic, given wanbond cannot see inside the encrypted WG tunnel? Confirm the scope split so I can write feasible tasks."
- context: "The pacer's only frame-type signal is FrameClass = ClassData | ClassControl (internal/sched/scheduler.go). ClassControl (WG handshake/keepalive) is ALREADY pacing-exempt (D22), which is also what G2 CORE-SCOPE-1 validates ('pacing does not starve WireGuard control frames'). Your empirical '~38% ICMP loss under overload' almost certainly refers to ICMP carried INSIDE the tunnel: to wanbond that is opaque ClassData (encrypted WG transport) indistinguishable from bulk data, so wanbond CANNOT give inner ICMP a priority lane without plaintext DPI before encryption (out of architecture). What IS addressable and wanbond-visible: (a) wanbond's OWN telemetry PROBE frames (frame.KindProbe) being shed by the pacer under overload, which would cause spurious path-DOWN / failover flaps; (b) a generic small-frame / low-rate priority lane for ClassData (heuristic, since inner type is invisible). Which of these does item 3(ii) mean, and is inner-ICMP prioritization explicitly OUT of scope?"
- suggestions: ["Scope item 3(ii) to protecting wanbond-generated PROBE frames from pacer shedding (add a probe/priority class so overload can't induce spurious path-down), and EXCLUDE inner-ICMP prioritization as architecturally infeasible (document why).","Add BOTH a probe-protection class AND a generic small-frame/low-rate ClassData priority lane (best-effort, since inner type is opaque), excluding inner-ICMP as out of scope.","Something else / inner-ICMP really was OUTER host traffic competing on the netns link (then wanbond cannot prioritize it either) - clarify the empirical setup."]
- recommendation: "Option 1: protect PROBE frames (wanbond-visible, real operability impact - prevents pacer overload from faking path-down), and explicitly document inner-ICMP prioritization as out of scope because the tunnel payload is opaque to the pacer."
- ledgerRefs: ["goals:G13"]
- answer: as recommended

### Q52 — answered

- createdAt: 2026-07-14T12:16:37.613Z
- updatedAt: 2026-07-14T12:23:22.238Z
- author: user
- session: 915ea040-10d3-4f13-9cf2-ed8e5149babb
- question: "Item 2 capacity-sanity guard: should selecting policy=\"weighted\" with a per_path_capacity_fps the declared link can never approach be a HARD config-load FAILURE (fail-fast, refuse to start) or a startup WARN (log + metric, still runs)? And what is the exact trip criterion?"
- context: "Today per_path_capacity_fps defaults to a synthetic 10000 (~115 Mbit/s); on a low-throughput/CPU-bound edge, EngageFraction*PerPathCapacity is never reached so aggregation is silently inert (you had to hand-set 250). A precise, testable trip rule is needed. Candidate criterion using the existing SizePacingFromBDP helper: when weighted is selected AND link_bandwidth is declared, compute impliedCapacityFPS = link_bandwidth/(8*avgWireFrameBytes); if the configured EngageFraction*per_path_capacity_fps exceeds impliedCapacityFPS (aggregation mathematically cannot engage at line rate), trip the guard. Open sub-question: if weighted is selected but link_bandwidth is NOT declared, is that itself a WARN/FAIL (no way to sanity-check), or allowed? 'Fail-loud' in your text is ambiguous between a hard fail and a loud warning."
- suggestions: ["Hard config-load FAILURE (fail-fast per project guidelines): weighted + a capacity the declared link cannot approach refuses to start with an actionable error; weighted + undeclared link_bandwidth also fails (cannot verify).","Startup WARN only (log line + a wanbond_weighted_capacity_sane gauge=0): never blocks startup, surfaces the misconfig for the operator/dashboard to catch.","WARN when link_bandwidth is undeclared, but FAIL when it IS declared and the numbers prove aggregation can never engage."]
- recommendation: "Option 3: a declared-bandwidth contradiction is a determinable misconfiguration -> fail fast (matches the repo's fail-fast/validate-at-boundary discipline); an undeclared bandwidth can only be WARNed since there is nothing to check against. This also dovetails with the metric from item 1."
- ledgerRefs: ["goals:G13"]
- answer: as recommended

### Q53 — answered

- createdAt: 2026-07-14T12:16:52.104Z
- updatedAt: 2026-07-14T12:24:07.462Z
- author: user
- session: 915ea040-10d3-4f13-9cf2-ed8e5149babb
- question: "Boundary with G2/Q20: G2 already OWNS (by its locked Q20 decision) auto-wiring SizePacingFromBDP from operator-declared per-link bandwidth PLUS documenting BDP sizing - but that work is NOT yet materialized as tasks (G2's M14/M16/M17 are empty). Does G13 restrict item 2 to only the WARN/FAIL capacity-sanity guard + item 1 observability (deferring the actual auto-derive/auto-wire AND the item 3(a) BDP-sizing docs to G2), or should G13 own the auto-derive/docs too since G2 hasn't decomposed them?"
- context: "G2 is `planned`; its grounding records 'Q20 pacing = BOTH (wire SizePacingFromBDP from operator-declared per-link bandwidth + document measurement)'. G2 milestones M13 (tolerant startup) and M15 (hub failover) are done, but M14/M16/M17 hold no tasks - so the pacing-sizing + real-link + runbook scope exists as INTENT only. Your goal text says 'if item 2's auto-derive or item 3(a)'s sizing docs would duplicate a G2 task, reference the G2 task instead of restating it' - but there is no G2 task to reference yet. So the dedup must be resolved at the goal-ownership level: either (A) G13 stays operability-only (guard + metric + probe class) and G2 later owns the auto-derive + BDP docs, or (B) G13 absorbs the auto-derive + docs because they are prerequisite to G13's guard and G2 has not claimed them concretely."
- suggestions: ["A - G13 stays operability-only: capacity-sanity WARN/FAIL guard (item 2), wanbond_aggregation_engaged + offered-load metrics (item 1), and the probe/priority class (item 3(ii)). The per_path_capacity_fps auto-derive from link_bandwidth and the BDP-sizing docs (item 3(a)) remain G2/Q20 and are only REFERENCED here.","B - G13 owns auto-derive + BDP docs too (fold Q20's pacing-sizing into G13 since G2 left it undecomposed), and G2's Q20 pacing-sizing scope is retired/redirected to avoid duplication.","C - G13 owns the runtime guard AND the auto-derive wiring, but the BDP-sizing DOCUMENTATION (install.md §3a / item 3(a)) stays with G2's doc task."]
- recommendation: "A: keep G13 crisply operability-scoped (guard + observability + probe class); leave the auto-derive wiring and BDP docs to G2's Q20 owner. If G2's pacing-sizing is later dropped, raise a follow-up rather than silently double-owning it now. This keeps the two goals' scopes disjoint and avoids re-planning G2's locked decision."
- ledgerRefs: ["goals:G13"]
- answer: as recommended

### Q54 — answered

- createdAt: 2026-07-14T12:17:05.130Z
- updatedAt: 2026-07-14T12:24:22.467Z
- author: user
- session: 915ea040-10d3-4f13-9cf2-ed8e5149babb
- question: Item 1 observability - metric SHAPE and cardinality. The weighted scheduler is per-PEER (one scheduler per bound peer). For wanbond_aggregation_engaged (bool) and the offered-load-vs-threshold series, what label cardinality, and should engage/disengage ALSO emit a transition log line?
- context: "The scheduler already holds all the state (s.aggregating, loadRate EWMA, EngageFraction/DisengageFraction*PerPathCapacity) but exposes NONE to the metrics Source/Collector (internal/metrics). Existing Prometheus series are labeled per-path (labelPath) or per-peer (labelPeer, used by FEC/resequencer). Aggregation is a per-peer-scheduler property, not per-path. Options for the numeric 'offered load vs threshold' visibility: expose (a) wanbond_offered_load_fps gauge, (b) the engage + disengage threshold gauges (fps), so a dashboard can plot load against both bands directly. Separately, liveness/failover already log transitions (path up/down, active-path change); an engage/disengage transition log line would give the same operability for the aggregation gate ('configured but inert' becomes visible in logs too, not only /metrics)."
- suggestions: ["Per-peer label (labelPeer), matching the FEC/resequencer per-peer series: wanbond_aggregation_engaged{peer} plus wanbond_offered_load_fps{peer} and static wanbond_aggregation_engage_threshold_fps{peer}/..._disengage_threshold_fps{peer}; AND log an INFO transition on every engage<->disengage flip (parity with liveness transitions).","Same metrics but UNLABELED/global (assume the common single-peer edge) to keep cardinality minimal; still log transitions.","Bool gauge only (wanbond_aggregation_engaged{peer}) + transition log, WITHOUT the extra offered-load/threshold gauges (leave load visibility to logs)."]
- recommendation: "Option 1: per-peer labels (consistent with existing per-peer series and correct on a multi-peer concentrator), expose offered-load + both threshold gauges so 'inert because load never crosses the band' is directly graphable, and add the engage/disengage transition log for parity with liveness. This most directly kills the 'configured but silently inert' blind spot you hit."
- ledgerRefs: ["goals:G13"]
- answer: as recommended

### Q55 — answered

- createdAt: 2026-07-14T12:17:19.822Z
- updatedAt: 2026-07-14T12:24:40.993Z
- author: user
- session: 915ea040-10d3-4f13-9cf2-ed8e5149babb
- question: "Acceptance-harness constraints: each task must be 'reproducible on a bandwidth-capped netns fixture'. Should per-task acceptance be netns e2e tests under `-tags e2e` (privileged, `just e2e`), or default-tag unprivileged unit/integration tests where the behavior allows? And does TestFixtureImpairment (T35) already support the three things these acceptances need - bandwidth cap, /metrics scrape of the new gauges, and injecting a competing/probe-starvation flow - or is extending the fixture itself a task in this plan?"
- context: "The definition-of-done here is `go test` GREEN plus `just lint` across default+e2e+realhosts tags (per project memory: a test-only view once reddened main's lint gate). Much of item 1 (metric wiring) and item 2 (config-load guard) is testable UNPRIVILEGED at the unit level (metrics.Exposition scrape helper exists; config.validate is pure); only the empirical engage/starvation behavior (aggregation actually engaging under load; the probe/pacer-overload starvation from item 3(ii)) needs the capped netns fixture. TestFixtureImpairment (test/e2e/fixture_impairment_test.go) exposes rateMbit/lossPct capping, and metrics.Fetch scrapes /metrics, but it is unclear whether it can drive a sustained overload that flips wanbond_aggregation_engaged AND concurrently observe probe/small-frame starvation - if not, a fixture-extension task is needed as a dependency."
- suggestions: ["Prefer unprivileged default-tag unit/integration acceptance wherever the behavior permits (metric registration+values, config-load guard), and use `-tags e2e` netns fixture acceptance ONLY for the empirical engage-under-load and pacer-starvation tasks; add an explicit fixture-extension task if TestFixtureImpairment can't yet drive sustained overload + observe probe starvation.","All acceptance via `-tags e2e` netns fixture for uniformity (heavier, privileged, but end-to-end).","Unit-only wherever possible and treat the empirical netns validations as report-only (M10/Q12-style, non-gating), mirroring G2's real-link tier."]
- recommendation: "Option 1: push acceptance to the cheapest level that still verifies the behavior (unit for metric/guard, e2e only for the load-dependent effects), and make any needed TestFixtureImpairment extension an explicit up-front task other tasks depend on - so no task's acceptance is blocked on fixture capability that doesn't exist yet."
- ledgerRefs: ["goals:G13"]
- answer: All acceptance via `-tags e2e` netns fixture for uniformity (heavier, privileged, but end-to-end).

## M49

### Q56 — open

- createdAt: 2026-07-14T12:36:15.358Z
- updatedAt: 2026-07-14T12:36:15.358Z
- author: "opus-4.8[1m]"
- session: 7295f080-20fa-4cf9-afac-0357b4cf65cb
- question: |
    D65 investigation has ruled out the tunnel-internal candidates and isolated the ceiling to an EXTERNAL loss/bufferbloat mechanism the repo cannot reproduce. To CONFIRM attribution and quantify the fix gain, please run these on the actual Pi4/Starlink/o3 deployment and paste the outputs, then re-run `/cq:investigate:advance D65`:
    (1) THREE-WAY iperf3 to attribute the gap: (a) direct over the WAN, no tunnel (edge WAN IP → o3 WAN IP) = WAN ceiling; (b) single-flow TCP through the tunnel = end-to-end; (c) a loopback/netns tunnel with no WAN (two wanbond instances on one host) = pure CPU/codec ceiling. Report Mbps for each.
    (2) LOCALIZE the ~1s queue: during the 8 Mbps UDP run, `tc -s qdisc show dev <edge-WAN-iface>` (before/after) and a concurrent `ping <o3>` to show the loaded RTT climb; also `ip -s link show wanbond0`.
    (3) FLOW ORIGIN: is the failing single-flow TCP Pi-originated (iperf3 Pi→o3 tunnel IP) or VLAN-223-client-FORWARDED through wanbond0? (Determines whether the missing MSS clamp is in play.)
    (4) A/B (single-flow TCP, report plateau + cwnd + retransmits each): baseline; then WITH `iptables -t mangle -A FORWARD -o wanbond0 -p tcp --tcp-flags SYN,RST SYN -j TCPMSS --clamp-mss-to-pmtu` (add OUTPUT chain if Pi-originated); then WITH scheduler `policy="weighted"` + pacing enabled (BDP-sized). 
    Alternatively, if you'd rather not measure first: authorize implementing the two candidate fixes directly (default disposition is FIX) and we'll validate on the hardware afterward.
- context: |
    DIAGNOSIS (round 1, defect D65 — tunnel single-flow TCP ~3.67 Mbps, cwnd ~30KB, 13 retx/10s; UDP 8Mbps→6.9 delivered, 13% loss, ~1s bufferbloat; Pi4/Starlink/o3). Hypothesis tree (milestone M49): H4/H-A (MSS) uncertain; H5/H-B (reorder) WRONG; H6/H-C (CPU) WRONG; H7/H-D (internal queue) WRONG; H8/H-E (FEC) WRONG; H9/H-F (no egress pacing → external bufferbloat) uncertain — LEADING.
    
    RULED OUT (validated repo evidence): (a) FEC — off by default; reported inner MTU=1400 equals InnerMTU(1500,fec=false) exactly (FEC-on=1395), proving FEC off. (b) Scheduler reorder — default active-backup sends single-path; resequencer (window 2048/250ms) reorders BEFORE WireGuard, so TCP never sees reorder; striping is opt-in weighted only. (c) Internal oversized queue/tail-drop — none exists: Send writes synchronously to the socket; the pacer sheds at the head (not tail-drop) and is BDP-bounded; reseq bounded in memory+latency (skips, not tail-drops). (d) CPU-bound encode — inline benchmark: DATA codec encode = 4610 ns/op = 303 MB/s ≈ 2429 Mbps/core (x86_64; pprof: chacha20-dominated, generic non-SIMD path); Pi4 aarch64 extrapolation ~160-300 Mbps/core, 40-80x above 3.67 Mbps. 3.67 Mbps = ~300 pps, trivially within syscall/lock budget.
    
    LEADING CAUSE (H-F, code-grounded but needs field confirmation): the loss + ~1s bufferbloat is EXTERNAL to wanbond (Starlink last-mile buffer). Under the DEFAULT active-backup scheduler wanbond applies NO egress pacing/AQM (a weighted-only feature: config.go:99-108, 218-223) — it offers packets unshaped, the Starlink buffer bloats to ~1s and tail-drops ~13%, collapsing TCP cwnd. KEY DATA POINT supporting this: UDP achieved 6.9 Mbps on the SAME path while TCP got only 3.67 — so the WAN carries ≥6.9 Mbps and TCP's shortfall is loss-induced cwnd collapse, NOT a raw WAN rate cap or a tunnel CPU limit.
    
    TWO TUNNEL-SIDE DEFICIENCIES (both real, code-confirmed): (1) no egress pacing/AQM on the active-backup path — FIX: add BDP-sized send pacing (+optional CoDel-style AQM) to active-backup (the weighted pacer already exists), or document enabling weighted+pacing on single-uplink Starlink; expected: eliminates the ~1s standing queue, restores single-flow TCP toward the ≥6.9 Mbps the WAN demonstrably carries. (2) no TCP MSS clamp installed anywhere (daemon installs none; install.md/wanbond-fixes.md deploy recipes omit it even for the forwarded client-LAN case) — FIX: install `TCPMSS --clamp-mss-to-pmtu` on wanbond0 for FORWARDED TCP; bites only VLAN-223-forwarded flows (Pi-originated TCP is already MSS-bounded by the TUN route MTU).
    
    WHY BLOCKED: attributing the 3.67 Mbps between raw-WAN-rate and fixable-tunnel-deficiency, and proving the pacing fix recovers throughput, requires running the tunnel over the real bufferbloated Starlink link — external hardware the repo cannot reproduce. On receiving the measurements, H-F/H-A can be confirmed and the fix file-and-deferred to plan-flow.
- ledgerRefs: ["defects:D65"]
