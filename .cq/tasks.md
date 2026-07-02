---
ledger: tasks
counters:
  milestone: 0
  item: 30
archives: []
---

# tasks

## M2

### T1 — done

- createdAt: 2026-07-01T23:38:18.474Z
- updatedAt: 2026-07-02T21:47:32.806Z
- author: fable-5
- session: 0047802a-1b44-4fcc-8198-d12359610ad6
- headline: Initialize git repo, Go module, and package layout
- description: "In /home/pavel/work/safe/fecalia: `git init`; `go mod init github.com/7mind/wanbond` pinned to the latest stable Go; skeleton package layout `cmd/wanbond/` plus `internal/{config,frame,bind,sched,fec,reseq,telemetry,metrics,log}` and `test/e2e/`; one binary serves both roles (edge|concentrator selected by config). Reasonable `.gitignore` (Go artifacts, nix result/, pcaps)."
- acceptance: "`go build ./...` and `go vet ./...` succeed on the committed skeleton; `go mod edit -json` shows module github.com/7mind/wanbond and a stable (non-rc/beta) Go version; the directory is a git repo with an initial commit and a .gitignore."
- suggestedModel: fast
- ledgerRefs: ["goals:G1"]
- tags: ["direct-impl-sandbox"]
- resultCommit: 9b3dc47
- completion: "Go module github.com/7mind/wanbond (go 1.26.4) + cmd/wanbond stub and internal/{config,frame,bind,sched,fec,reseq,telemetry,metrics,log} package stubs; test/e2e behind the e2e build tag. Verified: go build/vet green, e2e excluded from default build and compiles under -tags e2e. Implemented directly in the main checkout (worktree-isolated workers unavailable this session)."

### T2 — done

- createdAt: 2026-07-01T23:38:27.297Z
- updatedAt: 2026-07-02T22:00:34.224Z
- author: fable-5
- session: 0047802a-1b44-4fcc-8198-d12359610ad6
- headline: "Add Nix flake: dev shell and static binary package"
- description: "Author flake.nix: (a) dev shell providing the pinned Go toolchain plus the privileged-harness and DPI tooling — iproute2/tc (netem), iperf3, tcpdump, golangci-lint, and the nDPI + Suricata CLIs used by P5; (b) a package output building the static wanbond binary with CGO_ENABLED=0."
- acceptance: "`nix develop -c go version` prints the pinned Go; `nix develop -c sh -c 'command -v tc && command -v ip && command -v iperf3 && command -v tcpdump'` all resolve; `nix build` produces a runnable ./result/bin/wanbond (a stub main is acceptable at this stage)."
- suggestedModel: standard
- dependsOn: ["T1"]
- ledgerRefs: ["goals:G1"]
- resultCommit: ad8416c
- completion: "Nix flake: devShells.default (go 1.26.4, gopls, golangci-lint, just, gnumake, iproute2/tc, iperf3, tcpdump, ndpi, suricata) + packages.default static buildGoModule binary (CGO_ENABLED=0, vendorHash pinned). Verified in sandbox: nix build produces a runnable statically-linked ./result/bin/wanbond; nix develop resolves go + all harness/DPI tools (tc, ip, iperf3, tcpdump, ndpiReader, suricata)."
- tags: ["direct-impl-sandbox"]

### T3 — done

- createdAt: 2026-07-01T23:38:31.570Z
- updatedAt: 2026-07-02T21:57:16.257Z
- author: fable-5
- session: 0047802a-1b44-4fcc-8198-d12359610ad6
- headline: Configure golangci-lint and GitHub Actions CI (lint + unit)
- description: Add a standard-strict `.golangci.yml`; a Justfile/Makefile with thin targets (build, lint, test unprivileged, e2e = sudo + `-tags e2e`); and `.github/workflows/ci.yml` running golangci-lint and `go test ./...` (unprivileged only — the `e2e` build tag is never set in CI, per Q3). No privileged runners.
- acceptance: "`golangci-lint run ./...` exits 0 on the scaffold; `just lint` and `just test` exit 0; the workflow YAML passes actionlint and its steps invoke golangci-lint and `go test ./...` with no `-tags e2e`."
- suggestedModel: standard
- dependsOn: ["T1"]
- ledgerRefs: ["goals:G1"]
- resultCommit: 6cc34cb
- completion: "golangci-lint v2 config (.golangci.yml: standard + bodyclose/errorlint/misspell/unconvert + gofmt) and .github/workflows/ci.yml (build/vet/test + golangci-lint, ubuntu-latest, unprivileged; e2e tag never set). Verified in sandbox: golangci-lint run ./... = 0 issues over T1/T4/T5/T6 code; actionlint on the workflow = 0 issues; go test ./... green. Justfile lint/test targets wrap these."
- tags: ["direct-impl-sandbox"]

### T4 — done

- createdAt: 2026-07-01T23:38:36.294Z
- updatedAt: 2026-07-02T21:51:48.656Z
- author: fable-5
- session: 0047802a-1b44-4fcc-8198-d12359610ad6
- headline: Implement TOML config loader (role, paths, WG keys, amnezia params, PSK; 0600)
- description: Single TOML schema for both roles with an explicit `role = "edge"|"concentrator"` field (fail-fast validation, no inference); per-path source address list; WG private key/peers; amnezia obfuscation params (Jc/Jmin/Jmax, S1/S2, H1-H4); outer-control PSK inline. Parse into typed structs; enforce file mode 0600 at load (per Q5).
- acceptance: "Unit tests pass: a valid edge and a valid concentrator config parse into typed structs; missing role, empty path list, malformed key, and file mode != 0600 are each rejected with a descriptive error."
- suggestedModel: standard
- dependsOn: ["T1"]
- ledgerRefs: ["goals:G1"]
- resultCommit: dc4c090
- completion: "TOML config loader (internal/config): single schema for both roles, explicit role, base64 Curve25519 keys, per-path source addrs, amnezia params, outer-control PSK; Load() enforces exact 0600 + fail-fast validation. Fully unit-tested and green (go test ./internal/config green; build/vet/gofmt clean). Uses github.com/pelletier/go-toml/v2. Verified in sandbox."
- tags: ["direct-impl-sandbox"]

### T5 — done

- createdAt: 2026-07-01T23:38:40.169Z
- updatedAt: 2026-07-02T21:53:00.730Z
- author: fable-5
- session: 0047802a-1b44-4fcc-8198-d12359610ad6
- headline: Structured logging foundation with per-path fields
- description: slog-based structured logging (JSON handler) used from P0 onward (per Q7), behind a thin package wrapper so the rest of the code depends on an interface, not slog directly. Conventions for `component` and `path` fields so per-path events (up/down, roam, loss) are machine-filterable; level is config-driven.
- acceptance: Unit test asserts a record emitted through the wrapper is valid JSON and carries `component` and `path` attributes; the skeleton binary logs startup/shutdown through it.
- suggestedModel: fast
- dependsOn: ["T1"]
- ledgerRefs: ["goals:G1"]
- resultCommit: 7995a54
- completion: "Structured logging (internal/log): Logger interface over slog JSON handler so no other package imports slog directly; config-driven level (fail-fast on unknown); Component()/Path() child loggers for per-path fields; skeleton binary logs startup/shutdown through it. Unit-tested green (field attachment, level filtering, unknown-level rejection, empty-level default). Verified in sandbox."
- tags: ["direct-impl-sandbox"]

## M3

### T6 — done

- createdAt: 2026-07-01T23:38:45.236Z
- updatedAt: 2026-07-02T21:55:14.075Z
- author: fable-5
- session: 0047802a-1b44-4fcc-8198-d12359610ad6
- headline: e2e suite layering, sudo target, and acceptance-threshold constants table
- description: "Layer the suite per Q3: unprivileged `go test ./...` vs a privileged `-tags e2e` suite behind a just/make target invoking sudo. Define the Q1 thresholds as named constants in one file: P1RecoverySeconds=3; P2BondedMinFraction=0.85; P2MeteredMaxByteFraction=0.01; P3InjectedLossRates={0.05,0.15}; P3MinRecoveredFraction=0.95; P3MaxOverheadFactor=2 (× configured parity ratio); P4ResidualLossMax=0.005 at steady 5% loss; P4 adaptive-overhead <= fixed-FEC baseline. Commit the per-phase manual real-link checklist template (docs/manual-checklist.md, per Q2)."
- acceptance: "`go test ./...` runs zero privileged tests; the sudo e2e target compiles and runs the tagged suite; the constants file contains exactly the Q1 values above and the harness imports it (no magic literals in tests); checklist template committed."
- suggestedModel: fast
- dependsOn: ["T1"]
- ledgerRefs: ["goals:G1"]
- resultCommit: 307f1f8
- completion: "e2e suite layering + Q1 constants + manual checklist. test/e2e/thresholds.go (e2e-tagged) is the single source of Q1 acceptance constants; TestThresholds proves import with no magic literals. Justfile targets build/lint/fmt-check/test (unprivileged) + e2e/e2e-run (sudo -tags e2e). docs/manual-checklist.md per-phase template. Verified in sandbox: default go test excludes e2e (0 e2e pkgs), e2e-tagged threshold test passes, e2e build compiles, Justfile lists targets. NOTE: the sudo e2e RUN is deferred to hardware (no /dev/net/tun in sandbox); compile verified."
- tags: ["direct-impl-sandbox"]

### T7 — planned

- createdAt: 2026-07-01T23:38:53.166Z
- updatedAt: 2026-07-01T23:38:53.166Z
- author: fable-5
- session: 0047802a-1b44-4fcc-8198-d12359610ad6
- headline: netns/netem two-path fixture library (Starlink-like + 5G-like)
- description: "Go fixture (root-required, `e2e` build tag) creating edge and concentrator network namespaces joined by two veth paths: path A netem 45ms with jitter (Starlink-like), path B netem 64ms stable (5G-like). Runtime knobs: inject uniform loss %, blackhole/restore a path, re-address a veth (for the IP-change test), rate limits. Idempotent teardown (per Q2)."
- acceptance: "`sudo go test -tags e2e -run TestFixture ./test/e2e` brings the topology up, measures path RTTs of ~45ms (with jitter) and ~64ms (stable) within tolerance (verified via `tc qdisc show`), injects and removes 5% loss on path A, passes ICMP between namespaces, and tears down leaving no residual netns/veth."
- suggestedModel: standard
- dependsOn: ["T2"]
- ledgerRefs: ["goals:G1"]

## M4

### T8 — planned

- createdAt: 2026-07-01T23:39:01.889Z
- updatedAt: 2026-07-01T23:39:01.889Z
- author: fable-5
- session: 0047802a-1b44-4fcc-8198-d12359610ad6
- headline: Embed amneziawg-go with pass-through Bind; bring tunnel up edge↔concentrator
- description: Import github.com/amnezia-vpn/amneziawg-go as a library; bring up the device engine with TUN and a trivial single-UDP-socket pass-through conn.Bind (Open/Close/Send/ParseEndpoint/BatchSize/receive funcs); wire keys/peers/amnezia params from the TOML config; one binary drives both roles. Keep the Bind behind a small internal interface so swapping to upstream wireguard-go stays cheap (API-drift hedge). No bonding logic yet.
- acceptance: Package compiles against the pinned amneziawg-go version; a unit test round-trips a datagram through Send + the receive callback on loopback; `sudo go test -tags e2e ./test/e2e -run TestP0PassThrough` completes the WG handshake and passes ping + an iperf3 TCP transfer between the edge and concentrator namespaces through the tunnel.
- suggestedModel: frontier
- dependsOn: ["T4","T5","T6","T7"]
- ledgerRefs: ["goals:G1"]

### T9 — planned

- createdAt: 2026-07-01T23:39:12.755Z
- updatedAt: 2026-07-01T23:54:00.282Z
- author: fable-5
- session: 0047802a-1b44-4fcc-8198-d12359610ad6
- headline: Baseline throughput measurement + conn.Bind pitfalls findings doc
- description: "Measure baseline single-path tunnel throughput/latency in the fixture (iperf3). Author docs/p0-findings.md documenting, with citations into the amneziawg-go source, the pitfalls that shape P1+: (1) batched Send/ReceiveFunc semantics and BatchSize; (2) GSO/GRO fast paths; (3) the Endpoint identity model (how N real paths can hide behind one virtual endpoint); (4) amnezia junk packets arriving at the Bind; (5) fork lag / API drift vs upstream wireguard-go; (6) the WG anti-replay-window-vs-multipath-reorder margin (own outer-seq, never reuse the inner counter); (7) congestion/bufferbloat and whether send-pacing is needed (measure standing queue / latency-under-load on the emulated paths; note whether the scheduler must pace egress). Record the P0 manual real-link checklist section."
- acceptance: e2e prints a baseline throughput number for the single path; docs/p0-findings.md exists and contains a concrete finding (not a placeholder), citing specific amneziawg-go files/symbols where applicable, for each of the SEVEN named pitfall areas including the pacing/bufferbloat measurement and its verdict on whether the scheduler must pace.
- suggestedModel: frontier
- dependsOn: ["T8"]
- ledgerRefs: ["goals:G1"]

### T10 — planned

- createdAt: 2026-07-01T23:39:23.370Z
- updatedAt: 2026-07-01T23:39:23.370Z
- author: fable-5
- session: 0047802a-1b44-4fcc-8198-d12359610ad6
- headline: "P0 findings checkpoint gating P1: confirm or revise P1-P5 assumptions"
- description: "Explicit gate (per Q8): review docs/p0-findings.md against every planned P1-P5 task; enumerate each design assumption (virtual-endpoint identity, batched I/O shape, reorder margins, junk-packet handling) as confirmed or revised. If any P1+ task is invalidated, draft the /cq:plan:follow-up request describing the needed re-plan; otherwise record explicit go-ahead. P0 total is timeboxed to ~2-3 days."
- acceptance: A committed docs/p0-checkpoint.md lists each assumption with a confirmed/revised verdict and either a go-ahead statement or a drafted follow-up request; no P1 task starts before this note exists.
- suggestedModel: frontier
- dependsOn: ["T9"]
- ledgerRefs: ["goals:G1"]

## M5

### T11 — planned

- createdAt: 2026-07-01T23:39:30.788Z
- updatedAt: 2026-07-01T23:39:30.788Z
- author: fable-5
- session: 0047802a-1b44-4fcc-8198-d12359610ad6
- headline: Outer bonding frame codec + PSK-authenticated control/probe frames
- description: "Wire codec for the outer frame types: DATA (outer-seq, path-id, fec-group, flags) wrapping opaque WG datagrams; PARITY; PROBE; CONTROL. No plaintext magic constants or fixed offsets (requirement 6 groundwork). CONTROL/PROBE authenticated with the config PSK via a vetted AEAD/HMAC library (not hand-rolled); DATA headers unauthenticated by design (DoS-grade risk accepted). Own outer-seq space — never reuse the inner WG counter."
- acceptance: Unit round-trip tests for all four frame types preserve fields; tampered or PSK-mismatched CONTROL/PROBE frames are rejected; a decoder fuzz/property test runs clean without panic; a byte-histogram test asserts no byte position is constant across encodings of random payloads.
- suggestedModel: frontier
- dependsOn: ["T10"]
- ledgerRefs: ["goals:G1"]

### T12 — planned

- createdAt: 2026-07-01T23:39:43.724Z
- updatedAt: 2026-07-01T23:39:43.724Z
- author: fable-5
- session: 0047802a-1b44-4fcc-8198-d12359610ad6
- headline: "Multi-path conn.Bind: per-path sockets behind one virtual endpoint + MTU accounting"
- description: "Replace the pass-through Bind: one UDP socket per configured path bound to its source address; WG sees a single stable virtual endpoint per peer while the Bind privately maps real per-path endpoints; honor the batched send/recv + GSO/GRO findings from P0. MTU accounting for outer header + WG overhead (no fragmentation / ICMP black holes); write the MSS-clamping guidance doc section."
- acceptance: "Unit tests for virtual-endpoint identity and per-path endpoint bookkeeping; e2e: traffic flows over each path individually when the other is disabled; a max-MTU-sized transfer shows no IP fragmentation in a fixture capture; computed inner MTU = path MTU - (outer header + WG overhead) asserted against a fixture; MSS guidance committed."
- suggestedModel: frontier
- dependsOn: ["T11"]
- ledgerRefs: ["goals:G1"]

### T13 — planned

- createdAt: 2026-07-01T23:39:47.454Z
- updatedAt: 2026-07-01T23:39:47.454Z
- author: fable-5
- session: 0047802a-1b44-4fcc-8198-d12359610ad6
- headline: Per-path quality probes and liveness state machine
- description: PSK-authenticated probe frames measuring per-path RTT/loss/jitter, plus outer-seq gap accounting for passive loss estimation; a path up/down state machine with configurable detection thresholds. Path liveness is entirely ours (WG keepalive is per-peer, not per-path).
- acceptance: "Estimator unit tests on synthetic traces converge to injected RTT/loss/jitter within tolerance; a forged/tampered probe is rejected; e2e: a blackholed path is marked down within the configured detection threshold and the transition is logged with per-path fields."
- suggestedModel: standard
- dependsOn: ["T11"]
- ledgerRefs: ["goals:G1"]

### T15 — planned

- createdAt: 2026-07-01T23:40:01.204Z
- updatedAt: 2026-07-01T23:40:01.204Z
- author: fable-5
- session: 0047802a-1b44-4fcc-8198-d12359610ad6
- headline: Active-backup scheduler with transparent failover
- description: "Send-side scheduler v1: a single active path (Starlink-preferred) carries traffic; on a path-down signal from telemetry, instant switch to the backup path; switch-back with hysteresis on recovery (no thrash). Data-thrift by construction (backup idle until needed). Expose hooks the later weighted/FEC-aware scheduler extends. This is the P1 MVP core."
- acceptance: "Unit test: with two paths up all data egresses the active path; a path-down event switches egress to the backup within the configured detection window; recovery does not thrash the selection."
- suggestedModel: frontier
- dependsOn: ["T12","T13"]
- ledgerRefs: ["goals:G1"]

### T16 — planned

- createdAt: 2026-07-01T23:40:04.937Z
- updatedAt: 2026-07-01T23:40:04.937Z
- author: fable-5
- session: 0047802a-1b44-4fcc-8198-d12359610ad6
- headline: Edge public-IP change survival (per-path re-roaming)
- description: "Handle the mobile case: the edge's public IP changes on a path (NAT rebinding / carrier CGNAT churn). The concentrator's Bind re-learns that path's real endpoint from authenticated probe/control traffic without disturbing the other path or the WG session."
- acceptance: "e2e: re-address the edge-side veth of one path mid-transfer — that path recovers and the TCP transfer completes without reset; the other path is unaffected."
- suggestedModel: standard
- dependsOn: ["T12","T13"]
- ledgerRefs: ["goals:G1"]

### T20 — planned

- createdAt: 2026-07-01T23:40:28.766Z
- updatedAt: 2026-07-01T23:40:28.766Z
- author: fable-5
- session: 0047802a-1b44-4fcc-8198-d12359610ad6
- headline: "P1 e2e: failover survives WAN death within 3s"
- description: "e2e test driving the active-backup path: start a long-lived TCP flow (SSH-like / iperf3) through the tunnel, then kill the active WAN namespace mid-transfer and assert the flow survives with no connection reset and throughput recovers. Uses the P1RecoverySeconds constant from the harness table."
- acceptance: "`sudo go test -tags e2e ./test/e2e -run TestP1Failover` kills the active path mid-iperf3; the TCP connection is NOT reset and throughput is restored within P1RecoverySeconds (3s), asserted against the harness constants; repeated flap does not wedge the tunnel."
- suggestedModel: standard
- dependsOn: ["T15","T16"]
- ledgerRefs: ["goals:G1"]

### T22 — planned

- createdAt: 2026-07-01T23:40:41.392Z
- updatedAt: 2026-07-01T23:40:41.392Z
- author: fable-5
- session: 0047802a-1b44-4fcc-8198-d12359610ad6
- headline: systemd units, cross-compile matrix, install doc + P1 manual checklist
- description: "Per Q6: systemd unit files for the edge and concentrator roles; a CI/release step cross-compiling CGO_ENABLED=0 for linux/amd64 and linux/arm64; an install/ops doc referencing the 0600 config path; and the scripted P1 manual checklist for the real Starlink+5G+VPS setup appended to docs/manual-checklist.md. No packaging beyond the binary + nix."
- acceptance: "`just release` (or make) produces static binaries for linux/amd64 and linux/arm64 (`file` reports statically linked, correct arch); `systemd-analyze verify` passes on both unit files; install doc and P1 checklist committed."
- suggestedModel: fast
- dependsOn: ["T20"]
- ledgerRefs: ["goals:G1"]

### T30 — planned

- createdAt: 2026-07-01T23:54:17.561Z
- updatedAt: 2026-07-02T00:05:13.417Z
- author: fable-5
- session: 0047802a-1b44-4fcc-8198-d12359610ad6
- headline: Runtime path add/remove (dynamic path set + config reload)
- description: "Per the FUNCTIONAL requirement 'Path up/down + add/remove' and 'design for N': allow adding or removing a path from the active bonded set at runtime (e.g. SIGHUP config reload or a control-socket command), beyond the up/down liveness of T13. Adding a path opens its per-path socket, begins probing, and admits it to the scheduler once healthy; removing a path drains and closes it. Must not disturb existing paths, in-flight resequencing, or the WG session (single virtual endpoint preserved)."
- acceptance: "Unit + e2e tests: starting with one path, adding a second at runtime brings it into the scheduler once its probes report healthy and traffic begins using it, with zero reset of an in-flight TCP flow; removing a path drains and closes it while the flow continues on the remaining path; the WG session and the other path are undisturbed throughout."
- suggestedModel: frontier
- dependsOn: ["T12","T13","T15"]
- ledgerRefs: ["goals:G1"]

## M7

### T14 — planned

- createdAt: 2026-07-01T23:39:51.257Z
- updatedAt: 2026-07-01T23:39:51.257Z
- author: fable-5
- session: 0047802a-1b44-4fcc-8198-d12359610ad6
- headline: "RS FEC engine: grouping, parity-emission deadline, recovery"
- description: "Reed-Solomon over opaque outer DATA frames using klauspost/reedsolomon: group frames by fec-group, emit K parity frames within a configurable grouping deadline (bounding grouping latency), receiver recovers up to K losses per group. Content-agnostic (operates on ciphertext). Pure library layer with a fake clock — no datapath wiring yet."
- acceptance: "Unit tests: for random drop patterns of <=K frames per group, all data frames are recovered; a property test shows parity is emitted within the configured deadline even for partially filled groups (asserted with a fake clock); measured overhead equals the configured parity ratio."
- suggestedModel: frontier
- dependsOn: ["T11"]
- ledgerRefs: ["goals:G1"]

### T24 — planned

- createdAt: 2026-07-01T23:40:49.927Z
- updatedAt: 2026-07-01T23:40:49.927Z
- author: fable-5
- session: 0047802a-1b44-4fcc-8198-d12359610ad6
- headline: Integrate fixed-ratio FEC into the datapath + FEC metrics
- description: "Wire the RS engine into the datapath: send-side parity emission at the configured fixed ratio; receive-side recovery integrated BEFORE the resequencing buffer (reconstruct lost data frames from parity within a group, then hand recovered+received frames to resequencing and on to WG). Populate FEC overhead/recovered/unrecoverable counters on /metrics. Parity ratio from config."
- acceptance: "Unit/integration test: a receive stream with <=K dropped frames per group reconstructs the missing frames and delivers the full ordered payload to WG; recovery counter and FEC-overhead gauge update on /metrics."
- suggestedModel: frontier
- dependsOn: ["T14","T18","T21"]
- ledgerRefs: ["goals:G1"]

### T25 — planned

- createdAt: 2026-07-01T23:41:00.459Z
- updatedAt: 2026-07-01T23:41:00.459Z
- author: fable-5
- session: 0047802a-1b44-4fcc-8198-d12359610ad6
- headline: "P3 e2e: recovery at injected loss with bounded overhead"
- description: e2e test injecting uniform netem loss and asserting FEC recovery and overhead against /metrics, using the harness constants (P3InjectedLossRates, P3MinRecoveredFraction, P3MaxOverheadFactor). Appends the P3 manual checklist.
- acceptance: "`sudo go test -tags e2e ./test/e2e -run TestP3FixedFEC`: at 5% and at 15% uniform injected loss, >=95% of lost data frames recovered without retransmit, and FEC overhead <= 2x the configured parity ratio; both read from /metrics; P3 manual checklist appended."
- suggestedModel: standard
- dependsOn: ["T24"]
- ledgerRefs: ["goals:G1"]

## M6

### T17 — planned

- createdAt: 2026-07-01T23:40:09.142Z
- updatedAt: 2026-07-01T23:40:09.142Z
- author: fable-5
- session: 0047802a-1b44-4fcc-8198-d12359610ad6
- headline: Prometheus /metrics endpoint (localhost) with per-path telemetry
- description: "Per Q7: a localhost-bound HTTP /metrics endpoint with a per-path registry — tx/rx bytes, loss, RTT, jitter, throughput, path state, plus FEC counters (registered now, populated in P3). This is the assertion surface for P2-P4 e2e acceptance. Binding to a non-loopback address is refused by default."
- acceptance: "Integration test: GET http://127.0.0.1:<port>/metrics returns per-path gauges/counters for bytes, loss, RTT and throughput matching fixture traffic; a non-loopback bind is refused; a harness scrape helper is committed."
- suggestedModel: standard
- dependsOn: ["T13"]
- ledgerRefs: ["goals:G1"]

### T18 — planned

- createdAt: 2026-07-01T23:40:12.907Z
- updatedAt: 2026-07-01T23:40:12.907Z
- author: fable-5
- session: 0047802a-1b44-4fcc-8198-d12359610ad6
- headline: Receive resequencing buffer (bounded window + timeout)
- description: Bounded-window + timeout resequencer on the receive side, applied after unwrap (and later after FEC recovery) and BEFORE delivery to the WG engine, so WG's anti-replay window never sees pathological multipath reorder. Tune the initial window against the P0-measured Starlink jitter; verify the WG anti-replay window still has margin.
- acceptance: "Unit/property tests: frames arriving out of order within the window are delivered in outer-seq order under synthetic reorder/duplication/loss traces; frames beyond window/timeout are released (not held forever); bounded memory; e2e: with both paths active, WG anti-replay drop count stays 0 under fixture jitter."
- suggestedModel: frontier
- dependsOn: ["T12"]
- ledgerRefs: ["goals:G1"]

### T21 — planned

- createdAt: 2026-07-01T23:40:32.685Z
- updatedAt: 2026-07-01T23:54:06.453Z
- author: fable-5
- session: 0047802a-1b44-4fcc-8198-d12359610ad6
- headline: Weighted aggregation scheduler + data-thrift policy
- description: "Extend the scheduler from active-backup to weighted aggregation: under load a single flow uses both paths distributed by per-path weight derived from internal telemetry (capacity/RTT/loss/backlog from T13); at low load it collapses to Starlink-preferred so metered 5G stays ~idle (requirement 2 must not regress), engaging 5G only on demand with hysteresis. Include send-pacing / bufferbloat control per the P0 findings (T9): pace egress per path so aggregation does not build standing queues that inflate latency-under-load (make pacing a no-op if T9 concludes it is unnecessary). FEC-aware hooks for P3+. Policy thresholds in config."
- acceptance: "Unit tests: under offered load exceeding one path, frames are distributed across both paths proportional to weights (within tolerance); with load below one path's capacity, distribution collapses to the primary (5G ~idle); a path-down event still fails over correctly (P1 preserved); with pacing enabled, per-path egress rate does not exceed the configured/derived pace and no unbounded send backlog accumulates under sustained overload."
- suggestedModel: frontier
- dependsOn: ["T15","T18"]
- ledgerRefs: ["goals:G1"]

### T23 — planned

- createdAt: 2026-07-01T23:40:45.709Z
- updatedAt: 2026-07-01T23:54:07.640Z
- author: fable-5
- session: 0047802a-1b44-4fcc-8198-d12359610ad6
- headline: "P2 e2e: bonded throughput and 5G-idle assertions via /metrics"
- description: e2e test asserting aggregation and data-thrift against the /metrics endpoint under the netns profiles, using the harness constants (P2BondedMinFraction, P2MeteredMaxByteFraction). Appends the P2 manual checklist.
- acceptance: "`sudo go test -tags e2e ./test/e2e -run TestP2Aggregation`: under saturating load, bonded throughput >= 85% of the sum of the two paths' individual throughputs; while Starlink is healthy, 5G bytes < 1% of total; both read from /metrics; P2 manual checklist appended."
- suggestedModel: standard
- dependsOn: ["T21","T17"]
- ledgerRefs: ["goals:G1"]

## M9

### T19 — planned

- createdAt: 2026-07-01T23:40:16.740Z
- updatedAt: 2026-07-01T23:40:16.740Z
- author: fable-5
- session: 0047802a-1b44-4fcc-8198-d12359610ad6
- headline: Expose amnezia obfuscation params (Jc/Jmin/Jmax, S1/S2, H1-H4) end-to-end
- description: Plumb the amnezia junk/obfuscation params from TOML config into the embedded engine on both roles as defense-in-depth; confirm junk packets arriving at the multi-path Bind are tolerated (P0 finding). Protocol mimicry stays out of scope (non-goal).
- acceptance: "e2e with non-default params set identically on both ends: the tunnel handshakes and passes traffic; with mismatched params the handshake fails closed; junk packets do not destabilize the Bind (no errors/wedge in a soak run)."
- suggestedModel: standard
- dependsOn: ["T8","T12"]
- ledgerRefs: ["goals:G1"]

### T26 — planned

- createdAt: 2026-07-01T23:41:04.532Z
- updatedAt: 2026-07-01T23:41:04.532Z
- author: fable-5
- session: 0047802a-1b44-4fcc-8198-d12359610ad6
- headline: "Automated wire-format audit: entropy + fixed-offset check"
- description: "Harness tool capturing tunnel pcaps in the fixture across multiple sessions (with FEC/parity and amnezia junk active), then asserting the requirement-6 properties programmatically: no byte position holds a constant value across sessions/packets, and mean per-packet payload entropy exceeds a named threshold constant. Failure output pinpoints the offending offset."
- acceptance: A tagged e2e test captures >=5 fresh sessions and the audit reports zero constant byte positions and mean per-packet payload entropy above the named constant; a deliberately-planted constant byte makes the test fail with the offset reported.
- suggestedModel: standard
- dependsOn: ["T24","T19"]
- ledgerRefs: ["goals:G1"]

### T28 — planned

- createdAt: 2026-07-01T23:41:20.541Z
- updatedAt: 2026-07-01T23:41:20.541Z
- author: fable-5
- session: 0047802a-1b44-4fcc-8198-d12359610ad6
- headline: nDPI/Suricata non-classification check + document UDP-block limitation
- description: "Run the captured fixture pcaps through nDPI and Suricata (provided by the dev shell) and assert neither classifies the flow as WireGuard or any identified VPN protocol. Document the known limitation: hostile networks blocking UDP wholesale have no in-scope mitigation (no TCP/TLS fallback — non-goal). Append the P5 real-link checklist."
- acceptance: "`sudo go test -tags e2e ./test/e2e -run TestP5DPI`: the pcaps are analyzed by nDPI and Suricata; neither labels the flow as WireGuard/VPN (asserted on their output); the UDP-block limitation and the P5 manual checklist are documented."
- suggestedModel: standard
- dependsOn: ["T26"]
- ledgerRefs: ["goals:G1"]

## M8

### T27 — planned

- createdAt: 2026-07-01T23:41:16.490Z
- updatedAt: 2026-07-01T23:41:16.490Z
- author: fable-5
- session: 0047802a-1b44-4fcc-8198-d12359610ad6
- headline: Adaptive FEC controller with hysteresis (simulation-tested)
- description: "Control loop adjusting FEC parity ratio (and scheduler weights) from measured per-path loss, with hysteresis and rate limiting — stability is the crux risk. Build a deterministic simulation harness (synthetic loss traces, no network) and test the controller in ISOLATION before touching the datapath: redundancy low when links are clean, scaling up under sustained loss, without thrash under noisy telemetry."
- acceptance: "Simulation tests: parity ratio rises with sustained loss and falls when loss clears; under a loss signal oscillating around a threshold the change rate is bounded by the hysteresis/rate-limit (no flap); converges to a steady ratio for steady loss; at 0% loss steady-state parity overhead is ~0."
- suggestedModel: frontier
- dependsOn: ["T25"]
- ledgerRefs: ["goals:G1"]

### T29 — planned

- createdAt: 2026-07-01T23:41:29.469Z
- updatedAt: 2026-07-01T23:41:29.469Z
- author: fable-5
- session: 0047802a-1b44-4fcc-8198-d12359610ad6
- headline: Wire adaptive controller into datapath + P4 e2e vs fixed-FEC baseline
- description: "Wire the simulation-proven adaptive controller into the live datapath and verify against the P3 fixed-ratio baseline in the fixture, per Q1: equal loss masking for less total overhead, plus a steady-state residual-loss check. Uses harness constants (P4ResidualLossMax; adaptive-overhead <= fixed-FEC baseline)."
- acceptance: "`sudo go test -tags e2e ./test/e2e -run TestP4AdaptiveFEC`: for equal masking, adaptive total overhead bytes <= the P3 fixed-FEC baseline run; post-recovery residual loss <= 0.5% at steady 5% path loss; both read from /metrics; P4 manual checklist appended."
- suggestedModel: standard
- dependsOn: ["T27"]
- ledgerRefs: ["goals:G1"]
