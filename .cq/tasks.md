---
ledger: tasks
counters:
  milestone: 0
  item: 30
archives:
  - id: M2
    path: ./archive/tasks/M2.md
    summary: "wanbond S (scaffolding) complete: git repo + Go module github.com/7mind/wanbond, package layout, Nix flake (dev shell + static binary), golangci-lint + GitHub Actions CI, TOML config loader (0600 + fail-fast), structured logging. T1-T5 done and verified in-sandbox; Q9 answered."
    title: "wanbond S: repo scaffolding &amp; toolchain"
    status: done
  - id: M3
    path: ./archive/tasks/M3.md
    summary: "wanbond H (test harness) complete: netns/netem two-path fixture (Starlink-like 45ms+jitter / 5G-like 64ms stable; loss/blackhole/readdress knobs; PID-addressed peer ns, no /run needed) verified in-sandbox via userns; e2e suite layering behind the e2e build tag with sudo Justfile targets; Q1 acceptance-threshold constants table; per-phase manual checklist. T6-T7 done and verified."
    title: "wanbond H: netns/netem test harness"
    status: done
---

# tasks

## M4

### T8 — done

- createdAt: 2026-07-01T23:39:01.889Z
- updatedAt: 2026-07-06T20:03:39.446Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: Embed amneziawg-go with pass-through Bind; bring tunnel up edge↔concentrator
- description: Import github.com/amnezia-vpn/amneziawg-go as a library; bring up the device engine with TUN and a trivial single-UDP-socket pass-through conn.Bind (Open/Close/Send/ParseEndpoint/BatchSize/receive funcs); wire keys/peers/amnezia params from the TOML config; one binary drives both roles. Keep the Bind behind a small internal interface so swapping to upstream wireguard-go stays cheap (API-drift hedge). No bonding logic yet.
- acceptance: Package compiles against the pinned amneziawg-go version; a unit test round-trips a datagram through Send + the receive callback on loopback; `sudo go test -tags e2e ./test/e2e -run TestP0PassThrough` completes the WG handshake and passes ping + an iperf3 TCP transfer between the edge and concentrator namespaces through the tunnel.
- suggestedModel: frontier
- dependsOn: ["T4","T5","T6","T7"]
- ledgerRefs: ["goals:G1"]
- tags: ["blocked-on-hardware","partially-done"]
- completion: "DONE (commits 99a102a + 86b0749). internal/device brings the tunnel up (create TUN, wire the pass-through Bind into the embedded amneziawg-go engine, apply WireGuard/amnezia params via UAPI, both roles from one config); cmd/wanbond does config-driven role dispatch + signal-driven shutdown (fail-loud on unexpected engine death); test/e2e/TestP0PassThrough builds the binary, generates X25519 keypairs (stdlib crypto/ecdh), runs concentrator (peer netns via nsenter) + edge, addresses both TUNs, and verifies WG handshake + ping + iperf3 through the tunnel. amnezia UAPI keys emitted only when configured → P0 runs plain WireGuard (amnezia e2e deferred to T19). Verified on ubuntu@o3.7mind.io (real /dev/net/tun + root, aarch64): full e2e suite green (TestFixture, TestP0PassThrough handshake+ping+iperf3, TestThresholds), passing under -race; local go build/vet/test/golangci-lint/gofmt green; nix build produces the static binary (vendorHash updated for new amneziawg-go transitive deps). Reviewed by opus+fable panel: R4 go-ahead after fixing 4 round-1 criticisms. Filed 2 out-of-scope amnezia defects (D1, D2) deferred to T19."
- sessionLogs: [".cq/logs/20260706-200109-a1fd7a439122cc6ad.md",".cq/logs/20260706-200109-aa8173f2778caf84c.md",".cq/logs/20260706-200109-ac0148457e0d74922.md",".cq/logs/20260706-200109-a61cae3e31e0f7460.md"]
- resultCommit: 86b0749

### T9 — done

- createdAt: 2026-07-01T23:39:12.755Z
- updatedAt: 2026-07-06T20:31:42.557Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: Baseline throughput measurement + conn.Bind pitfalls findings doc
- description: "Measure baseline single-path tunnel throughput/latency in the fixture (iperf3). Author docs/p0-findings.md documenting, with citations into the amneziawg-go source, the pitfalls that shape P1+: (1) batched Send/ReceiveFunc semantics and BatchSize; (2) GSO/GRO fast paths; (3) the Endpoint identity model (how N real paths can hide behind one virtual endpoint); (4) amnezia junk packets arriving at the Bind; (5) fork lag / API drift vs upstream wireguard-go; (6) the WG anti-replay-window-vs-multipath-reorder margin (own outer-seq, never reuse the inner counter); (7) congestion/bufferbloat and whether send-pacing is needed (measure standing queue / latency-under-load on the emulated paths; note whether the scheduler must pace egress). Record the P0 manual real-link checklist section."
- acceptance: e2e prints a baseline throughput number for the single path; docs/p0-findings.md exists and contains a concrete finding (not a placeholder), citing specific amneziawg-go files/symbols where applicable, for each of the SEVEN named pitfall areas including the pacing/bufferbloat measurement and its verdict on whether the scheduler must pace.
- suggestedModel: frontier
- dependsOn: ["T8"]
- ledgerRefs: ["goals:G1"]
- completion: "DONE (merged d4781a6; worker commit cb77a83 + orchestrator fixes 1531911/d4781a6). test/e2e/baseline_test.go TestP0Baseline measures per-uplink idle RTT, saturated tunnel throughput, and RTT-under-load (bufferbloat) — verified on o3.7mind.io: starlink 44.8ms idle / 18.9-88.8 Mbit/s (CPU-bound, noisy), cellular 64.3ms / 43.5 Mbit/s; bufferbloat Δ negligible. docs/p0-findings.md documents all 7 pitfall areas with amneziawg-go@v1.0.4 source citations (audited exact by the review panel): BatchSize/batched I/O, GSO/GRO, Endpoint identity (N-paths-behind-one-virtual-endpoint), amnezia junk at the Bind, fork lag/API drift, anti-replay-window-vs-reorder (own outer-seq, never reuse inner counter), and congestion/bufferbloat. KEY FINDING for T10: the netns fixture emulates delay/jitter/loss but NO bandwidth cap, and throughput is CPU-bound, so it cannot exercise bufferbloat/pacing in-fixture — a REVISED assumption (needs a rate-limited fixture variant for P2). Reviewed opus+fable, R5 go-ahead. Filed D3 (fixed-sleep iperf3 readiness race, out-of-scope)."
- sessionLogs: [".cq/logs/20260706-203000-a28d62545377df7ef.md",".cq/logs/20260706-203000-a66924e3eb38ae28b.md",".cq/logs/20260706-203000-a6aa433786a823bc2.md",".cq/logs/20260706-203000-a555730d6a692a960.md",".cq/logs/20260706-203000-ae0c2d5f6a0994fb7.md"]
- resultCommit: d4781a6

### T10 — done

- createdAt: 2026-07-01T23:39:23.370Z
- updatedAt: 2026-07-06T20:49:29.185Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: "P0 findings checkpoint gating P1: confirm or revise P1-P5 assumptions"
- description: "Explicit gate (per Q8): review docs/p0-findings.md against every planned P1-P5 task; enumerate each design assumption (virtual-endpoint identity, batched I/O shape, reorder margins, junk-packet handling) as confirmed or revised. If any P1+ task is invalidated, draft the /cq:plan:follow-up request describing the needed re-plan; otherwise record explicit go-ahead. P0 total is timeboxed to ~2-3 days."
- acceptance: A committed docs/p0-checkpoint.md lists each assumption with a confirmed/revised verdict and either a go-ahead statement or a drafted follow-up request; no P1 task starts before this note exists.
- suggestedModel: frontier
- dependsOn: ["T9"]
- ledgerRefs: ["goals:G1"]
- completion: "DONE (commit 3f55920 + review fixes 36a9f6e/2ab0fdb). docs/p0-checkpoint.md is the Q8 P0->P1 gate: assumption ledger A1-A7 each with a confirmed/revised verdict grounded in docs/p0-findings.md and cross-checked against the T11-T30 DAG. CONFIRMED: A1 virtual-endpoint identity (T12/T16/T30), A2 batched I/O+GSO/GRO (T12), A3 anti-replay-vs-reorder/own-outer-seq (T11/T18), A4 junk-at-Bind opacity (source analysis; T11/T19), A5 fork isolation; CARRIED-FORWARD: A6 MTU accounting (unmeasured at P0, verified by T12 acceptance); REVISED: A7 bufferbloat/pacing — the netns fixture has no bandwidth cap and P0 throughput is CPU-bound, so T23's aggregation e2e + T21's empirical BDP pace-sizing need a bandwidth-capped fixture variant (drafted /cq:plan:follow-up). VERDICT: GO-AHEAD for P1 (M5) and P3-P5 (M7-M9); GO-AHEAD-WITH-PREREQUISITE for P2 (M6). Reviewed opus+fable, R6 go-ahead (3 rounds). T11 may start."
- sessionLogs: [".cq/logs/20260706-204500-a8e8aba6f76f5085b.md",".cq/logs/20260706-204500-a7e6b677426ce0802.md",".cq/logs/20260706-204500-a134692db4129bffa.md",".cq/logs/20260706-204500-ab8cf7484251a3d93.md",".cq/logs/20260706-204500-a9f5d5eb7770fd58d.md"]
- resultCommit: 2ab0fdb

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
