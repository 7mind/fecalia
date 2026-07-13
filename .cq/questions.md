---
ledger: questions
counters:
  milestone: 0
  item: 28
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

### Q21 — open

- createdAt: 2026-07-13T20:58:52.969Z
- updatedAt: 2026-07-13T20:58:52.969Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- question: "Confirm the scope boundary: is this goal STRICTLY concentrator-side multi-peer (one concentrator process terminating N distinct edges, each a distinct WireGuard peer/pubkey), with NO change to the edge side? Specifically, (a) is edge-side simultaneous aggregation across multiple DISTINCT concentrators explicitly out of scope (it is different from the already-shipped T57/Q18 single-active-hub ordered-endpoint failover), and (b) is the existing single-edge NAT-roaming case (one peer whose source rebinds) considered ALREADY handled, so 'multi-peer' means genuinely distinct peers, not the roaming of one?"
- context: "Grounded: device.Up builds ONE bind.NewMultipath(cfg.Paths, cfg.PSK, ...) for the whole process; the edge already has T57 hub-failover (config.Peer.Endpoints ordered list, reseq.Rebaseline, startHubFailover in device.go) which is single-ACTIVE, not simultaneous aggregation. config.validate already accepts >=1 [[wireguard.peers]] and device.uapiConfig ranges over all peers, but the Bind/reseq/scheduler are singletons. The scope answer decides whether ANY edge-side work is in the plan or the plan is concentrator-only de-singletoning."
- suggestions: ["Concentrator-only multi-peer; edge unchanged; edge-multi-hub and single-edge-roaming both out of scope","Concentrator multi-peer PLUS edge-side simultaneous multi-concentrator aggregation","Something else (describe)"]
- recommendation: Concentrator-only multi-peer for this goal; edge-side simultaneous multi-concentrator aggregation is a separate feature; single-edge roaming is already handled and is not what 'multi-peer' means here.
- ledgerRefs: ["goals:G4"]

### Q22 — open

- createdAt: 2026-07-13T20:59:05.594Z
- updatedAt: 2026-07-13T20:59:05.594Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- question: "Adopt a PER-PEER PSK (move psk into [[wireguard.peers]]) as the authenticated path->peer demux enabler? If yes, pin the exact config schema and back-compat rule: (a) does the top-level `psk` REMAIN fully supported as the single-peer default (so every existing single-peer config keeps working byte-for-byte), with a per-peer `psk` under [[wireguard.peers]] only REQUIRED once >1 peer is configured? (b) does validate REQUIRE the per-peer psks to be pairwise DISTINCT when >1 peer (equal psks would defeat authenticated demux)? (c) is the model symmetric on the edge (each edge configured with the single psk that matches its concentrator-side peer entry), i.e. no edge schema change beyond the value it already sets?"
- context: "Grounded: config.Config.PSK is a single top-level Key (config.go:42), config.validate requires it (line 844), config.Peer has PublicKey/Endpoint(s)/AllowedIPs and NO psk field. multipath.NewMultipath takes one psk and builds ONE frame.Codec + telemetry.NewReflector(psk) + per-path telemetry.NewProber(...,cfg.PSK,...). The outer PSK-HMAC PROBE/CONTROL plane is the only authenticated signal below the crypto layer, so a per-peer PSK is what lets an authenticated PROBE identify WHICH peer a path belongs to. This decision drives internal/config schema+validation AND how the Bind is de-singletoned (map keyed by peer, each peerState with its own psk-derived codec/reflector)."
- suggestions: ["Per-peer psk under [[wireguard.peers]]; top-level psk stays the single-peer default; per-peer required + pairwise-distinct only when >1 peer; edge unchanged","Per-peer psk ALWAYS required (drop top-level psk) — a clean break, no single-peer back-compat","Keep one deployment-wide psk + some non-PSK demux (describe how it stays unforgeable)"]
- recommendation: "Per-peer psk under [[wireguard.peers]] as the enabler; keep top-level psk as the single-peer back-compat default; require per-peer psks present and pairwise-distinct when >1 peer; no edge schema change (the edge already sets exactly the psk matching its concentrator peer)."
- ledgerRefs: ["goals:G4"]

### Q23 — open

- createdAt: 2026-07-13T20:59:15.592Z
- updatedAt: 2026-07-13T20:59:15.592Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- question: "Is a DATA/PARITY outer-frame WIRE-FORMAT change acceptable for this goal, or is 'no wire change' a hard requirement? The chosen direction (authenticated path->peer binding) should need NO wire change: DATA/PARITY stay unauthenticated with no peer id, and demux is purely by the authenticated path->peer binding table. Confirm that (a) adding a peer-id field to the DATA header is explicitly REJECTED (it would be spoofable and reintroduce the cross-peer resequencer-injection DoS invariant-4 forbids), and (b) the plan must preserve byte-for-byte wire compatibility with already-deployed single-peer edges."
- context: "Grounded: frame.Data carries OuterSeq/PathID/FECGroup/FECIndex/Flags and NO peer id; DATA/PARITY are unauthenticated by design (frame.go wire-model comment, invariant 4); only PROBE/CONTROL carry a PSK-HMAC tag. reseq's whole discontinuity/resync guard assumes DATA is forgeable. A wire change would ripple into frame.DataOverhead, mtu.go InnerMTU sizing, FEC shard coding (OuterSeq||Payload), and cross-version compat. Answering this fixes whether internal/frame is in the refactor surface at all."
- suggestions: ["No wire change; DATA/PARITY unchanged; demux purely via authenticated path->peer binding (peer-id-in-DATA rejected); keep single-peer wire compat","A wire change IS acceptable if planning shows it is necessary (describe the compat story)"]
- recommendation: No wire change. DATA/PARITY stay unauthenticated and peer-id-free; the peer is resolved by the authenticated path->peer binding, not by any outer-frame field. This preserves invariant 4 and wire compat with deployed edges.
- ledgerRefs: ["goals:G4"]

### Q24 — open

- createdAt: 2026-07-13T20:59:27.135Z
- updatedAt: 2026-07-13T20:59:27.135Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- question: "How is an inbound path/source attributed to a peer for the VERY FIRST frames, before any authenticated PROBE has bound it (and before the WG handshake completes)? Two sub-decisions: (1) Bootstrap policy for early DATA on an unbound source — GATE (drop DATA until an authenticated PROBE under some peer's psk binds the source->peer, relying on WG handshake/keepalive retransmit), or QUARANTINE it in a provisional per-source resequencer that is adopted once the binding resolves? (2) Peer identification for an unbound authenticated frame — is it acceptable to TRIAL-DECODE an unbound PROBE against each configured peer's psk (O(peers) HMAC verifies) to discover which peer it belongs to, or is a cheaper binding hint required?"
- context: "Grounded: multipath.handleInbound today learns a path's return remote ONLY from an authenticated PROBE (Probe case: ps.setRemote(srcAP)), never from DATA (the D9/D11 fix), and reflects peer probes via telemetry.Reflector. With per-peer psk each peerState has its own psk-derived Codec, so an unbound source's frame must be trial-decoded across peers' codecs to identify the peer. Trial-decode is an O(peers) cost + a potential CPU-DoS surface on spoofed unbound sources (ties to the resource-limit and threat-model questions). This decision shapes the demux table programming in device.go and the Bind receive path."
- suggestions: ["Gate DATA until an authenticated PROBE binds source->peer; identify the peer by trial-decoding the PROBE across peer psks (bounded by max-peers)","Provisional quarantine resequencer per unbound source, adopted on first authenticated PROBE","A cheaper binding hint is required (describe)"]
- recommendation: Gate DATA on an unbound source until an authenticated PROBE binds source->peer (WG retransmits cover the brief gap); identify the peer by trial-decoding the unbound PROBE across configured peer psks, with the cost bounded by the max-peers cap and unauthenticated floods dropped cheaply.
- ledgerRefs: ["goals:G4"]

### Q25 — open

- createdAt: 2026-07-13T20:59:38.682Z
- updatedAt: 2026-07-13T20:59:38.682Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- question: When an edge's source rebinds (NAT/roaming) so a path arrives from a NEW source address, how must the path re-bind to the SAME peer without a window where its frames misroute into another peer's resequencer? Is it acceptable that re-binding a moved source to its peer happens ONLY on a fresh authenticated PROBE (under that peer's psk) from the new source — accepting a brief drop/no-route window for that path's DATA until the PROBE re-binds it (covered by WG retransmit and the other still-bound paths of the same bonded edge) — or is a stronger continuity guarantee required?
- context: "Grounded: today a single virt is pinned once to the first learned source (virtualEndpoint), and per-path remote is re-learned on each authenticated PROBE (handleInbound Probe case). The existing T16 re-roam and D11 machinery already re-learn a path's remote from authenticated probes. In the multi-peer world the RISK is that DATA from a moved source, arriving before its re-binding PROBE, could be attributed to the wrong peer's resequencer (cross-peer contamination). Gating re-bind on an authenticated PROBE keeps the binding unforgeable but opens a small window. This decides the roaming/re-bind logic in the demux table."
- suggestions: ["Re-bind a moved source to its peer ONLY on a fresh authenticated PROBE from the new source; unbound/other DATA from that source is dropped (not misrouted) until then","Require a stronger zero-window continuity guarantee (describe the mechanism)"]
- recommendation: Re-bind on the authenticated PROBE only; until the moved source re-binds, its DATA is dropped rather than attributed to any peer (never misrouted). WG retransmit and the edge's other still-bound paths cover the brief window, mirroring the existing D11/T16 re-learn discipline.
- ledgerRefs: ["goals:G4"]

### Q26 — open

- createdAt: 2026-07-13T20:59:51.158Z
- updatedAt: 2026-07-13T20:59:51.158Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- question: "Pin the concentrator resource-limit model: (a) is there a CONFIGURED max-peers cap, and what default (the per-peer footprint is a ~2048-frame resequencer ring PLUS per-peer FEC send/recv state PLUS per-peer scheduler/probers/reflector, so N peers multiply memory)? (b) what is the eviction policy for idle/dead peers — evict when the WG session is torn down / liveness has been DOWN past a timeout, or never (static peer set only)? (c) on cap exhaustion, REJECT a new peer's bootstrap, or evict the idlest? Note: is the concentrator peer set STATIC (only the configured [[wireguard.peers]] ever bind) or can peers appear dynamically within that configured set?"
- context: "Grounded: resequencerWindow=2048 (multipath.go), and each peer needs its own resequencer (atomic.Pointer today), fecSend/fecRecv, scheduler, prober set, and reflector — all currently process-singletons. The configured peer set is bounded by [[wireguard.peers]] (config), so 'max peers' may simply be len(peers); but the demux/provisional state for UNbound sources needs its own bound (DoS). This decision sets the peerState map sizing, the eviction lifecycle wired from device.go peer events, and the backpressure branch."
- suggestions: ["Static configured peer set only (cap = number of [[wireguard.peers]]); per-peer state torn down when that peer's WG session/liveness goes away; provisional unbound-source state separately capped","Configured max_peers cap with a default (state the number) + idle-eviction timeout","No cap / no eviction (reject if unsure)"]
- recommendation: "Peers are the STATIC configured [[wireguard.peers]] set, so the steady-state cap is that count; size per-peer state lazily and tear it down when a peer's WG session/liveness is gone; cap the PROVISIONAL unbound-source demux state separately (small, drop-on-exhaustion) to bound the bootstrap DoS."
- ledgerRefs: ["goals:G4"]

### Q27 — open

- createdAt: 2026-07-13T21:00:02.247Z
- updatedAt: 2026-07-13T21:00:02.247Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- question: "State the target THREAT MODEL for the path->peer binding so acceptance can test it: (1) With distinct per-peer PSKs, is the required guarantee 'a malicious edge that knows ITS OWN psk can disrupt ONLY its own tunnel, never another peer's resequencing/FEC/scheduling' (i.e. full cross-peer isolation, since it cannot forge an authenticated PROBE under a victim's psk)? (2) For an attacker with NO valid psk who floods spoofed/unbound source addresses, is the accepted bound 'unauthenticated frames are dropped cheaply and provisional demux state is capped, so the flood cannot exhaust memory/CPU or evict a live peer' — accepting only degraded bootstrap latency, never cross-peer corruption?"
- context: "Grounded: DATA/PARITY are unforgeable-DATA by design (invariant 4); only an authenticated PROBE (PSK-HMAC, with monotonic anti-replay + the T38/D12 session challenge in telemetry) can bind a source to a peer. So cross-peer injection requires the victim's psk. The residual surfaces are (i) trial-decode CPU on unbound floods and (ii) provisional-state exhaustion / live-peer eviction — both tie to the demux-bootstrap and resource-limit questions. A written threat model turns the e2e 'one edge's loss/restart does not corrupt another' test into concrete adversary cases."
- suggestions: ["Full cross-peer isolation under distinct psks; no-psk floods bounded to degraded bootstrap only (never corruption or live-peer eviction)","A weaker/different guarantee (describe)"]
- recommendation: "Target full cross-peer isolation: with distinct per-peer psks a peer can disrupt only itself; an attacker without a valid psk is limited to (bounded, capped) bootstrap-latency degradation and can neither corrupt another peer's stream nor evict a live peer."
- ledgerRefs: ["goals:G4"]

### Q28 — open

- createdAt: 2026-07-13T21:00:13.528Z
- updatedAt: 2026-07-13T21:00:13.528Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- question: "For /metrics: add a per-peer label to the wanbond_path_* (and the per-peer resequencer/FEC) series so an operator can attribute traffic/loss/recovery to a specific edge? If yes, what is the label KEYED on — a stable config-assigned peer name (recommended, human-readable, bounded), or the WG public key (globally stable but opaque and higher-cardinality-looking)? Confirm the cardinality increase (series multiply by peer count) is acceptable given it is bounded by the max-peers/static peer set, and whether existing single-peer series must stay label-compatible (e.g. omit the peer label, or emit a default, when only one peer)."
- context: "Grounded: metrics.Source (newMetricsSource) reads per-PATH counters (txBytes/rxBytes atomics on pathState, prober RTT/loss, resequencer Stats) off the single Bind; there is no peer dimension today. Per-peer isolation makes per-peer scrape data the operator's primary signal ('is edge A's tunnel healthy independently of edge B'). config.Peer has no name field today, so keying on a peer name may require a small schema add (peer name/id). This decides the metrics.Source shape and any config surface for a peer identifier."
- suggestions: ["Add a `peer` label keyed on a config-assigned peer name (add a name/id field to [[wireguard.peers]]); single-peer configs keep back-compatible series","Key the `peer` label on the WG public key (no new config field)","No per-peer labels for this goal (aggregate only)"]
- recommendation: "Add a per-peer `peer` label keyed on a config-assigned peer name (small [[wireguard.peers]] name field); cardinality is bounded by the static peer set; keep single-peer series back-compatible (omit the label or emit a stable default when only one peer)."
- ledgerRefs: ["goals:G4"]
