---
ledger: questions
counters:
  milestone: 0
  item: 13
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
