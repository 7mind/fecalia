---
ledger: milestones
counters:
  milestone: 0
  item: 17
archives:
  - id: M2
    path: ./archive/milestones/M2.md
    summary: "wanbond S (scaffolding) complete: git repo + Go module github.com/7mind/wanbond, package layout, Nix flake (dev shell + static binary), golangci-lint + GitHub Actions CI, TOML config loader (0600 + fail-fast), structured logging. T1-T5 done and verified in-sandbox; Q9 answered."
    title: "wanbond S: repo scaffolding &amp; toolchain"
    status: done
  - id: M3
    path: ./archive/milestones/M3.md
    summary: "wanbond H (test harness) complete: netns/netem two-path fixture (Starlink-like 45ms+jitter / 5G-like 64ms stable; loss/blackhole/readdress knobs; PID-addressed peer ns, no /run needed) verified in-sandbox via userns; e2e suite layering behind the e2e build tag with sudo Justfile targets; Q1 acceptance-threshold constants table; per-phase manual checklist. T6-T7 done and verified."
    title: "wanbond H: netns/netem test harness"
    status: done
  - id: M11
    path: ./archive/milestones/M11.md
    summary: "Deferred-defect hardening round complete: 9 fix tasks T42-T50 delivered (each opus+fable-reviewed, gated, -race-clean, merged to main), resolving 12 defects (D3,D4,D10,D13,D14,D20,D22,D23,D24,D25,D26,D28). Highlights: T44 CONTROL-frame anti-replay (MAC-covered Seq + ControlGuard); T45 FEC prefix-stability invariant + quiescence-accurate unrecoverable counter; T46 target_residual adaptive-FEC SLA sizing (sanctioned new config surface per Q16); T47 AmneziaWG-profile-aware pacer control-frame exemption (caught+fixed a vanilla-only classifier blindness on re-review); T42 non-vacuous goroutine-leak gate; T43 duplicate source_addr + global-v6 device-bind fixes; T49 throughput-ceiling doc sweep to measured 4-vCPU numbers; T50 e2e/realhosts-tagged lint coverage; T48 reboot-persistent firewall provisioning (repo-side). D7 (live-apply) + D8 remain non-terminal pending the manual o3 iptables ops per Q14 (o3 is a test host)."
    title: Deferred-defect hardening round (D3/D4/D7/D8/D10/D13/D14/D20/D22/D23/D24/D25/D26/D28)
    status: done
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

### M10 — open

- createdAt: 2026-07-06T21:43:06.588Z
- updatedAt: 2026-07-06T21:43:06.588Z
- title: RH — real-host + impairment validation
- description: "Cross-cutting additive milestone (goal G1, 2026-07-06 follow-up; answered Q12 report-only + Q13 new-milestone). Two independent legs: (1) a REAL cross-network two-host e2e tier (SSH-orchestrated, `realhosts` build tag, opt-in/manual) validating the tunnel over the real internet between o3.7mind.io (concentrator, public IP) and llm-ubuntu-0 (edge, NAT) — REPORT-ONLY per Q12 (netns `e2e` stays the sole automated completion gate; real-host runs execute-and-record, never blocking a phase task/milestone); (2) a netns-fixture impairment extension (bandwidth cap + controlled-loss knobs, superseding the A7/T10 checkpoint follow-up) + the single-flow-TCP-collapse FEC baseline. Additive only — the locked P1-P5 DAG (T11-T30, M2-M9, K1) is untouched; cross-phase relationships are advisory task dependsOn/ledgerRefs."

### M12 — open

- createdAt: 2026-07-13T12:27:04.130Z
- updatedAt: 2026-07-13T12:27:04.130Z
- title: "G2 coordination: production-readiness — real-link validation, pacing, pilot hardening"
- description: Coordination milestone for goal G2 (production-readiness / pre-pilot hardening). Follows G1 (P0-P5 build + 2026-07-08 deferred-defect hardening round, both complete; ledger drained). Holds G2 and its clarifying questions until planned.
- dependsOn: ["M1"]

### M13 — open

- createdAt: 2026-07-13T13:36:41.213Z
- updatedAt: 2026-07-13T13:36:41.213Z
- title: "G2/W1 — Startup path-availability resilience (approach A: tolerant bind + background reconcile)"
- description: "CORE SCOPE 4. Make internal/bind/multipath.go Open() tolerant of a well-formed-but-not-yet-assignable source_addr (EADDRNOTAVAIL): bring the tunnel up on paths that DO bind, mark unbindable ones DOWN (reuse runtime path-down model), and reconcile/retry in background as addresses appear. Hard guards: zero-bindable stays FATAL; malformed source_addr stays a config-load error; no T16 device-bind/re-roam regression. Validated by a netns e2e. Work milestone for goal G2 (coordination milestone M12)."

### M14 — open

- createdAt: 2026-07-13T13:36:44.185Z
- updatedAt: 2026-07-13T13:36:44.185Z
- title: G2/W2 — Pacing empirical sizing + BDP config wiring (CORE SCOPE 1 + Q20 both)
- description: CORE SCOPE 1, Q20=both. Measure per-path BDP with the bandwidth-capped fixture (T35); wire SizePacingFromBDP into config load from an operator-declared per-link bandwidth (not runtime auto-tuning); validate ENABLED pacing eliminates bufferbloat under sustained load and does not starve WireGuard rekey; document the measurement/tuning procedure. Pacing continues to ship DISABLED by default. Work milestone for goal G2 (coordination milestone M12).

### M15 — open

- createdAt: 2026-07-13T13:36:51.219Z
- updatedAt: 2026-07-13T13:36:51.219Z
- title: "G2/W3 — Multi-concentrator hub-failover (Q18: edge-side ordered-endpoint active-standby)"
- description: "Q18 IN-SCOPE. Bring hub-termination redundancy into the pilot via edge-side ORDERED-ENDPOINT ACTIVE-STANDBY: the edge holds an ordered list of concentrator (Peer) endpoints, detects hub loss (ALL paths to the active concentrator DOWN via the PROBE/liveness plane), switches the peer remote and triggers a WireGuard re-handshake to the next endpoint. NO hub-to-hub state handoff (fresh WG session at the standby); mesh/anycast ruled out by the SD-WAN non-goal. Netns e2e + report-only realhosts validation. Depends on W1 (shares internal/bind/multipath.go). Work milestone for goal G2 (coordination milestone M12)."
- dependsOn: ["M13"]

### M16 — open

- createdAt: 2026-07-13T13:36:58.845Z
- updatedAt: 2026-07-13T13:36:58.845Z
- title: "G2/W4 — Real-link validation tier (CORE SCOPE 2: aggregation + loaded-RTT + WAN-kill + short soak, report-only)"
- description: "CORE SCOPE 2. Extend the -tags realhosts tier (runner.go SSH + provision.go) across the two standing hosts (llm-ubuntu-0 amd64 symmetric-NAT edge <-> o3 aarch64 public concentrator) with: throughput-aggregation (bonded-vs-sum ratio) + loaded-RTT/bufferbloat under load; a deliberate mid-transfer WAN kill for link AND hub failover under real conditions; and a SHORT report-only soak. All report-only per M10/Q12 (no absolute-number gate). Hardware-validated on the standing hosts; exercises W2 pacing and W3 hub-failover. Depends on W2 + W3. Work milestone for goal G2 (coordination milestone M12)."
- dependsOn: ["M14","M15"]

### M17 — open

- createdAt: 2026-07-13T13:37:05.088Z
- updatedAt: 2026-07-13T13:37:05.088Z
- title: G2/W5 — Pilot runbook, non-blocking exit criterion + full doc sync (CORE SCOPE 3 + Q19)
- description: CORE SCOPE 3 + Q19. Automate the manual-checklist section-P0 real-link baseline into a repeatable pre-pilot procedure; write a rollout runbook (config/key/PSK generation, concentrator firewall persistence D7/D8, /metrics monitoring, health checks, standby-concentrator setup); record the NON-BLOCKING pilot exit criterion (capped-fixture aggregation/bufferbloat + report-only real-link smoke are SUFFICIENT to proceed; soak runs DURING the supervised pilot, not as a pre-gate); and do a full README/design.md/install.md/manual-checklist.md doc-sync sweep per AGENTS.md. Depends on W1-W4. Work milestone for goal G2 (coordination milestone M12).
- dependsOn: ["M13","M14","M15","M16"]
