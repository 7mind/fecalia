---
ledger: milestones
counters:
  milestone: 0
  item: 9
archives: []
---

# milestones

## active

### M-AMBIENT — open

- createdAt: 2026-07-01T22:11:55.570Z
- updatedAt: 2026-07-01T22:11:55.570Z
- title: ambient

### M1 — open

- createdAt: 2026-07-01T23:11:32.541Z
- updatedAt: 2026-07-01T23:11:32.541Z
- title: "Plan: wanbond — WAN-bonding tunnel (amneziawg-go + custom Bind)"
- description: "Coordination milestone for the plan-flow goal: implementation + test harness plan for wanbond, a 2-WAN bonding tunnel with adaptive FEC built on amneziawg-go with a custom conn.Bind. Groups the goal, clarifying questions, reviews, and approval decision."

### M2 — open

- createdAt: 2026-07-01T23:36:54.706Z
- updatedAt: 2026-07-01T23:36:54.706Z
- title: "wanbond S: repo scaffolding &amp; toolchain"
- description: "Greenfield foundations for wanbond (goal G1): git init, Go module github.com/7mind/wanbond, package layout, Nix flake dev shell + static binary package, golangci-lint + GitHub Actions CI (lint+unit only), TOML config loader (role/paths/WG keys/amnezia params/PSK, 0600), structured per-path logging. Feeds every later phase."

### M3 — open

- createdAt: 2026-07-01T23:36:59.458Z
- updatedAt: 2026-07-01T23:36:59.458Z
- title: "wanbond H: netns/netem test harness"
- description: "First-class test harness for wanbond (goal G1): netns/netem two-path fixture (Starlink-like 45ms jitter + 5G-like 64ms stable; loss injection, path blackhole, veth re-address, idempotent teardown), e2e suite layering behind an `e2e` build tag with a sudo target, Q1 acceptance-threshold constants table, and the per-phase manual real-link checklist template. Every phase asserts against this."
- dependsOn: ["M2"]

### M4 — open

- createdAt: 2026-07-01T23:37:04.743Z
- updatedAt: 2026-07-01T23:37:04.743Z
- title: "wanbond P0: amneziawg-go embedding spike (timeboxed ~2-3 days)"
- description: "Spike (goal G1): embed amneziawg-go as a library behind a portable conn.Bind interface with a trivial single-socket pass-through Bind; bring the tunnel up edge↔concentrator in the netns fixture (handshake + ping + TCP); measure baseline throughput; document conn.Bind contract pitfalls (batched send/recv, GSO/GRO, Endpoint identity, amnezia junk packets at the Bind, fork lag vs upstream, anti-replay-vs-reorder margin). Ends with the Q8-mandated findings checkpoint gating P1."
- dependsOn: ["M2","M3"]

### M5 — open

- createdAt: 2026-07-01T23:37:09.866Z
- updatedAt: 2026-07-01T23:37:09.866Z
- title: "wanbond P1: transparent failover MVP"
- description: "Requirement 1 (goal G1): outer bonding frame codec + PSK-authenticated control/probe frames; multi-path conn.Bind (per-path sockets behind one virtual endpoint, MTU accounting/MSS guidance); per-path quality probes + liveness state machine; active-backup scheduler with transparent failover; edge public-IP-change survival (per-path re-roaming). Verify: kill the active WAN mid-TCP → session survives with no reset, throughput restored within 3s. Includes systemd units + amd64/arm64 cross-compile + install doc."
- dependsOn: ["M4"]

### M6 — open

- createdAt: 2026-07-01T23:37:14.975Z
- updatedAt: 2026-07-01T23:37:14.975Z
- title: "wanbond P2: aggregation, data-thrift &amp; metrics"
- description: "Requirements 2+3 (goal G1): localhost Prometheus /metrics endpoint with per-path telemetry (the P2-P4 assertion surface, Q7); receive resequencing buffer (bounded window + timeout, before WG so anti-replay never sees pathological reorder); weighted aggregation scheduler + data-thrift policy. Verify (via /metrics): bonded throughput ≥85% of the sum of individual path throughputs; 5G bytes <1% of total while Starlink healthy. Must not regress P1 failover."
- dependsOn: ["M5"]

### M7 — open

- createdAt: 2026-07-01T23:37:20.740Z
- updatedAt: 2026-07-01T23:37:20.740Z
- title: "wanbond P3: fixed-ratio Reed-Solomon FEC"
- description: "Requirement 4 (goal G1): RS FEC engine (klauspost/reedsolomon) over opaque outer DATA frames — group by fec-group, emit parity within a grouping deadline, recover ≤K losses/group, content-agnostic on ciphertext; integrate into the datapath (send parity at a fixed ratio + receive recovery before the resequencer) with FEC counters on /metrics. Verify (via /metrics): at 5% and 15% injected loss, ≥95% of lost data frames recovered without retransmit, FEC overhead ≤2× the configured parity ratio."
- dependsOn: ["M6"]

### M8 — open

- createdAt: 2026-07-01T23:37:24.784Z
- updatedAt: 2026-07-01T23:37:24.784Z
- title: "wanbond P4: adaptive FEC"
- description: "Requirement 5 (goal G1): adaptive control loop adjusting FEC parity ratio (and scheduler weights) from measured per-path loss, with hysteresis + rate limiting — control-loop stability is the crux risk, so the controller is built and proven in a deterministic simulation harness (synthetic loss traces, no network) before live wiring. Verify (via /metrics): for equal masking, adaptive total overhead ≤ the P3 fixed-FEC baseline; post-recovery residual loss ≤0.5% at steady 5% path loss."
- dependsOn: ["M7"]

### M9 — open

- createdAt: 2026-07-01T23:37:32.458Z
- updatedAt: 2026-07-01T23:37:32.458Z
- title: "wanbond P5: DPI hardening"
- description: "Requirement 6 (goal G1): expose amnezia obfuscation params (Jc/Jmin/Jmax, S1/S2, H1-H4) end-to-end as defense-in-depth; automated wire-format audit (entropy + fixed-offset check over captured pcaps); nDPI/Suricata non-classification check. Verify: outer frames show no constant byte at any fixed offset and high per-position entropy; nDPI and Suricata do not classify the flow as WireGuard or any identified VPN. Protocol mimicry out of scope; document the UDP-wholesale-block limitation (no TCP/TLS fallback)."
- dependsOn: ["M8"]
