---
ledger: milestones
counters:
  milestone: 0
  item: 81
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
  - id: M14
    path: ./archive/milestones/M14.md
    summary: "G2/W2 pacing empirical sizing + BDP config wiring COMPLETE (CORE SCOPE 1, Q20=both). T52 capped-fixture BDP measurement (report-only), T53 wired SizePacingFromBDP into config load from operator-declared per-link bandwidth (load-time only, NOT runtime auto-tuning; pacing default-DISABLED), T56 operator tuning procedure (docs/install.md §3a + design.md; 1540B/frame), T61 ENABLED-pacing bufferbloat + no-rekey-starvation e2e (relative gate). All 4 tasks done, 4 reviews go-ahead (opus), merged to main (c803cb5 T53, b9f5983 T56, 40205c1 T61). HARDWARE-VALIDATED on llm-ubuntu-0 (amd64 4-vCPU): bufferbloat 208.5ms(unpaced)→0.5ms(paced) at 4Mbit cap; BDP=33241B (21.6 frames @1540B), SizePacingFromBDP→capacityFPS=4179.9 burstFrames=21.6 @50Mbit/5.2ms. Numbers fed to the T65 pilot runbook."
    title: G2/W2 — Pacing empirical sizing + BDP config wiring (CORE SCOPE 1 + Q20 both)
    status: done
  - id: M16
    path: ./archive/milestones/M16.md
    summary: "G2/W4 real-link validation tier COMPLETE (CORE SCOPE 2, report-only). T58 aggregation-ratio + loaded-RTT/bufferbloat, T63 mid-transfer LINK + HUB failover, T64 short soak — all across llm-ubuntu-0 (amd64 NAT edge) <-> o3 (aarch64 public concentrator), all HARDWARE-VALIDATED. 3 tasks done, 3 reviews go-ahead (opus). Key real-link results: aggregation ratio ~0.25-0.46 (shared-physical-uplink topology, ratio<=1 EXPECTED — NOT a bandwidth-aggregation guarantee); bufferbloat 21-176ms under saturation (real-link variability); LINK failover ~1.4-1.5s, HUB failover ~1.7-2.1s with traffic RESUMED via standby (confirms the D32-fixed hub-failover data plane on a REAL cross-network link, 60-90 Mbit/s); short soak survived a WG rekey (0 path-down flaps). All o3-safe (reversible udp-scoped iptables, never deprovisioned; firewall fully restored each run)."
    title: "G2/W4 — Real-link validation tier (CORE SCOPE 2: aggregation + loaded-RTT + WAN-kill + short soak, report-only)"
    status: done
  - id: M17
    path: ./archive/milestones/M17.md
    summary: "G2/W5 pilot runbook + non-blocking exit criterion + full doc-sync COMPLETE (CORE SCOPE 3, Q19). T59 rollout runbook (docs/runbook.md — key/PSK gen, both-ends config, standby-concentrator via ordered endpoints + shared WG key, D7/D8 firewall persistence, /metrics health checks), T65 `just p0-baseline` automating the P0 real-link baseline (HARDWARE-VALIDATED: PASS 286s, report emitted), T66 recorded the non-blocking pilot exit criterion (runbook §7: capped-fixture W2 + report-only real-link W4 sufficient to enter a supervised pilot; soak runs DURING the pilot) + full doc-sync removing stale not-yet-built phrasing across README/design/install/manual-checklist/runbook. 3 tasks done, 3 reviews go-ahead. All metric/config claims verified against source; no overclaim (aggregation documented as report-only, single-uplink topology)."
    title: G2/W5 — Pilot runbook, non-blocking exit criterion + full doc sync (CORE SCOPE 3 + Q19)
    status: done
  - id: M61
    path: ./archive/milestones/M61.md
    summary: "G12 W1 — Monitor backend COMPLETE. New internal/monitor package: dedicated loopback-default listener (non-loopback fail-fast requires token; act-then-verify verifyLoopbackBind), Host/Origin validation + static-token auth (?token=→wanbond_monitor_token SameSite=Strict HttpOnly cookie→302), 1Hz WebSocket push of MonitorSnapshot built from a DEDICATED metrics.Source; /metrics untouched (Q46). 5 tasks + 8 reviews, all terminal. Review panel caught+fixed real defects: listener leak on Close-without-Start (D84 filed for the identical metrics.Server bug), Origin CSRF bypass (foreign-IP Origin allowed), config/bind loopback invariant."
    title: "G12 W1 — Monitor backend: [monitor] config, dedicated listener, auth, WS snapshot feed"
    status: done
  - id: M62
    path: ./archive/milestones/M62.md
    summary: "G12 W2 — Frontend COMPLETE. Vite+TypeScript (Q49) read-only dashboard go:embed-served by the W1 monitor: ResilientWsClient (connecting/live/reconnecting/offline, exp backoff+jitter, clean-vs-abnormal close), per-path/FEC/reseq/session cards with per-peer vs flat grouping, client-side-only ~5min rolling SVG sparklines (Q48/Q50), TS MonitorSnapshot mirror. web-build wired into the Justfile before go build/lint/release; //go:embed all:dist with committed dist/.gitkeep. 4 tasks + 4 reviews, all terminal."
    title: "G12 W2 — Frontend: Vite+TypeScript resilient dashboard, go:embed + build wiring"
    status: done
  - id: M63
    path: ./archive/milestones/M63.md
    summary: "G12 W3 — Daemon wiring + e2e + docs + gate COMPLETE. Monitor wired into device.Up with a DEDICATED 2nd metrics.Source (≠ /metrics scraper's) + applyMonitorLocked idempotent SIGHUP-reload reconciler (edge+concentrator parity); rebind-order fix (T169 r2, defc990) stop-old-before-start-new on same-address token rotation (fable differentially reproduced the EADDRINUSE + confirmed the guard). Live-WS e2e (T170) drives the real adapter reflecting single+multi-peer state. Docs sync (T171) incl. the Q58(a) cleartext-token residual-risk paragraph. Full DoD gate GREEN (T172): just fmt-check + lint (0 issues all tags) + test + build (real Vite bundle embedded). 4 tasks + 5 reviews, all terminal. G12 DONE."
    title: G12 W3 — Daemon wiring (edge+concentrator parity), e2e, docs & gate
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

### M15 — open

- createdAt: 2026-07-13T13:36:51.219Z
- updatedAt: 2026-07-13T13:36:51.219Z
- title: "G2/W3 — Multi-concentrator hub-failover (Q18: edge-side ordered-endpoint active-standby)"
- description: "Q18 IN-SCOPE. Bring hub-termination redundancy into the pilot via edge-side ORDERED-ENDPOINT ACTIVE-STANDBY: the edge holds an ordered list of concentrator (Peer) endpoints, detects hub loss (ALL paths to the active concentrator DOWN via the PROBE/liveness plane), switches the peer remote and triggers a WireGuard re-handshake to the next endpoint. NO hub-to-hub state handoff (fresh WG session at the standby); mesh/anycast ruled out by the SD-WAN non-goal. Netns e2e + report-only realhosts validation. Depends on W1 (shares internal/bind/multipath.go). Work milestone for goal G2 (coordination milestone M12)."
- dependsOn: ["M13"]

### M18 — open

- createdAt: 2026-07-13T20:55:43.837Z
- updatedAt: 2026-07-13T20:55:43.837Z
- title: "Plan: multi-peer (hub-and-spoke) concentrator"
- description: "Coordination milestone for the plan-flow goal G3: let one wanbond concentrator process terminate MANY edges concurrently (per-peer isolation of resequencing/FEC/scheduling + authenticated path->peer demux). Groups the goal, its clarifying questions, reviews, and approval decision. Successor to G2 (production-readiness, complete)."

### M19 — open

- createdAt: 2026-07-13T21:16:32.637Z
- updatedAt: 2026-07-13T21:16:32.637Z
- title: "Plan: DNS (hostname) concentrator endpoints"
- description: "Coordination milestone for the plan-flow goal G5: optional DNS/hostname resolution for the edge's concentrator endpoint/endpoints (dial a concentrator by name; support a changing/DDNS concentrator IP via re-resolution + SetPeerRemote repoint), opt-in, default IP-only. Groups the goal, clarifying questions, reviews, approval decision. Edge-side; orthogonal to G4 (multi-peer concentrator)."

### M20 — open

- createdAt: 2026-07-13T21:53:32.463Z
- updatedAt: 2026-07-13T21:53:32.463Z
- title: "G5 DNS endpoints: config and resolver foundations"
- description: "Work milestone 1/3 for goal G5 (optional DNS concentrator endpoints): the config surface (overloaded endpoint/endpoints behind an explicit per-peer DNS opt-in, Q35/Q29) and the resolver subsystem (injectable seam + system resolver + first-class DoH/DoT per Q33, plus the [dns] selection block)."

### M21 — open

- createdAt: 2026-07-13T21:53:41.434Z
- updatedAt: 2026-07-13T21:53:41.434Z
- title: G5 deferred resolution and re-resolution runtime
- description: "Work milestone 2/3 for goal G5: make hubFailover's endpoint set mutable/spec-keyed under its lock (Q34), implement the re-resolution controller (poll + liveness-loss, change-suppressed SetPeerRemote repoint, Q31/Q32), and wire deferred boot-time resolution + the loop into the device lifecycle (Q30) keeping Multipath.ParseEndpoint IP-only."
- dependsOn: ["M20"]

### M22 — open

- createdAt: 2026-07-13T21:53:50.514Z
- updatedAt: 2026-07-13T21:53:50.514Z
- title: G5 DNS verification, DPI posture, and docs
- description: "Work milestone 3/3 for goal G5: the Q36 acceptance bar — DPI-posture guard tests (opt-in OFF ⇒ zero DNS + unchanged wire audit, Q29/Q33), the netns dial-by-name / mid-session-IP-change e2e, the cross-controller race coverage, the report-only realhosts stretch, and the AGENTS.md docs/example-config sync."
- dependsOn: ["M21"]

### M23 — open

- createdAt: 2026-07-13T22:26:21.605Z
- updatedAt: 2026-07-13T22:26:21.605Z
- title: Per-peer PSK and peer-identity config surface
- description: "Work milestone 1/5 for goal G4 (multi-peer concentrator): per-peer psk + name fields on [[wireguard.peers]] with top-level-psk single-peer back-compat, pairwise-distinctness validation, edge-role rejection, and the effective-PSK resolution helper (Q22/Q21/Q28)."

### M24 — open

- createdAt: 2026-07-13T22:26:28.802Z
- updatedAt: 2026-07-13T22:26:28.802Z
- title: "Bind: de-singleton the multipath datapath into per-peer state"
- description: "Work milestone 2/5 for goal G4: structural, behavior-preserving de-singletoning of internal/bind/multipath.go — peerState (virt/outerSeq/scheduler/fec/resequencer/reflector/codec), the shared-socket vs per-(peer,path) pathState split, per-peer Send routing, single ReceiveFunc draining all peers under per-peer virtual endpoints (A1 literal), per-peer resequencer lifecycle + D32 rebaseline isolation."
- dependsOn: ["M23"]

### M25 — open

- createdAt: 2026-07-13T22:26:38.032Z
- updatedAt: 2026-07-13T22:26:38.032Z
- title: Authenticated path->peer binding, bootstrap, roaming, and limits
- description: "Work milestone 3/5 for goal G4: the security crux — source->peer binding established ONLY by an authenticated PROBE (trial-decoded across per-peer PSKs), DATA/PARITY gated on unbound sources, PROBE-only roam re-bind, capped provisional demux state with lazy per-peer instantiation + dead-peer teardown, and executable cross-peer isolation threat-model tests (Q23–Q27). No wire change."
- dependsOn: ["M24"]

### M26 — open

- createdAt: 2026-07-13T22:26:45.887Z
- updatedAt: 2026-07-13T22:26:45.887Z
- title: Device wiring and per-peer metrics
- description: "Work milestone 4/5 for goal G4: composition root — device.Up builds per-peer prober sets/schedulers/virtual endpoints from the effective-PSK helper and programs the Bind demux; /metrics gains a peer label keyed on the config peer name with single-peer back-compat (Q28). Edge role byte-identical (Q21)."
- dependsOn: ["M25"]

### M27 — open

- createdAt: 2026-07-13T22:26:52.579Z
- updatedAt: 2026-07-13T22:26:52.579Z
- title: Multi-peer isolation tests, invariants, and docs
- description: "Work milestone 5/5 for goal G4: verification at all altitudes — per-peer resequencer isolation unit test, 2+ edge netns e2e, FEC prefix-stability invariant re-check after the per-peer FEC split, docs/example-config sync per AGENTS.md, and the report-only 2-edge real-link check (M10/Q12 discipline)."
- dependsOn: ["M26"]

### M28 — open

- createdAt: 2026-07-13T22:47:58.249Z
- updatedAt: 2026-07-13T22:47:58.249Z
- title: Production-deploy defect intake (wanbond-fixes.md, RPi/NM edge → o3)
- description: "Intake milestone for the defects observed during the first REAL production-style deploy (edge = Raspberry Pi 4 / Debian / NetworkManager, two WAN uplinks as VLAN sub-interfaces eth0.231 Starlink / eth0.232 5G pinned by `ip rule from <source_addr>`; concentrator = o3 aarch64 OCI, public 89.168.124.91 NAT'd from private enp0s6 10.0.0.92; client LAN eth0.223 → bonded tunnel 10.77.0.0/24 → NAT out o3). All observed on real hardware/WANs; unit + realhosts-P0 tiers were green — every gap sits at the restart/re-handshake and operator/edge-plumbing boundary the testbed (2 source IPs on ONE interface, both ends always restarted together) does not exercise. Source: wanbond-fixes.md (repo root). Improvements/doc work from the same document is goal-tracked separately (see the wanbond-fixes goal milestone)."

### M29 — open

- createdAt: 2026-07-13T22:49:37.801Z
- updatedAt: 2026-07-13T22:49:37.801Z
- title: "Plan: production-edge operability & full-tunnel hardening (wanbond-fixes.md I1-I8 + C1-C6)"
- description: Coordination milestone for the plan-flow goal covering the IMPROVEMENTS (I1-I8) and DOCUMENTATION updates (C1-C6) from wanbond-fixes.md — the production lessons from the first real deploy (RPi/NM edge + o3 concentrator). The six companion defects from the same document are filed as D35-D40 under milestone M28 (defect intake); investigate-flow owns their root-causing, and their eventual fix tasks should compose with this goal's plan. Groups the goal, its clarifying questions, reviews, and approval decision.

### M30 — open

- createdAt: 2026-07-13T23:21:45.217Z
- updatedAt: 2026-07-13T23:21:45.217Z
- title: "G6-A Operability: link-up, session signal, diagnostics (I1-I4, I8)"
- description: "Work milestone for goal G6 (synthesized opus+fable plan). Low-risk operability code with existing test hooks: wanbond0 link-up (I1), wanbond_session_established metric + session-up log (I2), actionable TUN-EIO diagnostics (I3), warmup no-healthy-path log downgrade (I4), and the Q39 bidirectional-standby-liveness netns verification (I8). All independently plannable per Q37."

### M31 — open

- createdAt: 2026-07-13T23:21:51.657Z
- updatedAt: 2026-07-13T23:21:51.657Z
- title: "G6-B Config: bind-mode toggle + thin full-tunnel (I5, I6)"
- description: "Work milestone for goal G6 (synthesized opus+fable plan). The two config-surface features, each split surface-then-wiring so the TOML/validation contract is reviewable before behavior changes: per-path bind = source|device|auto with global default (I5, Q42), and thin full-tunnel mode=default-route — internal /1+/1 allowed_ips split at UAPI render + edge default-route wiring only (I6, Q41); literal-/0 pass-through gated on D35 by acceptance text only (Q37)."

### M32 — open

- createdAt: 2026-07-13T23:21:58.028Z
- updatedAt: 2026-07-13T23:21:58.028Z
- title: G6-C Persistence & packaged artifacts (I7, C1, C4)
- description: "Work milestone for goal G6 (synthesized opus+fable plan). The Q38 belt-and-suspenders pair: opt-in persistent-TUN code (I7, tun_persist default false) plus the two Q40 packaged artifacts — NM unmanaged-devices conf.d drop-in and templated wanbond-addressing@.service oneshot — each with its coupled C1/C4 install.md section (AGENTS.md docs-with-code rule)."

### M33 — open

- createdAt: 2026-07-13T23:22:11.294Z
- updatedAt: 2026-07-13T23:22:11.294Z
- title: G6-D Full-tunnel & operations docs (C2, C3, C5, C6) + reference sync
- description: "Work milestone for goal G6 (synthesized opus+fable plan). Standalone docs sequenced after the code they must reference: C2 (source_addr/device-bind collision + oif recipe + bind-mode pointer), C3+C6 (full-tunnel client-LAN recipe + concentrator NAT/forwarding checklist), C5 (reconverge window + restart guidance naming the I2 session metric), closing with the reference/example/design sync sweep so no new key or metric ships undocumented."
- dependsOn: ["M30","M31","M32"]

### M34 — open

- createdAt: 2026-07-14T09:00:22.044Z
- updatedAt: 2026-07-14T09:00:22.044Z
- title: "Plan: fix D36 one-sided-restart resequencer stall"

### M35 — open

- createdAt: 2026-07-14T09:06:46.120Z
- updatedAt: 2026-07-14T09:06:46.120Z
- title: "Plan: multi-peer concentrator datapath hardening (D42/D44/D47/D49/D50/D58)"

### M36 — open

- createdAt: 2026-07-14T09:06:46.602Z
- updatedAt: 2026-07-14T09:06:46.602Z
- title: "Plan: config loader/validation hardening (D41/D43/D55/D59)"

### M37 — open

- createdAt: 2026-07-14T09:06:47.518Z
- updatedAt: 2026-07-14T09:06:47.518Z
- title: "Plan: observability & metrics accuracy (D48/D52/D53)"

### M38 — open

- createdAt: 2026-07-14T09:06:47.767Z
- updatedAt: 2026-07-14T09:06:47.767Z
- title: "Plan: code/test/doc-comment hygiene (D40/D45/D51/D54/D56/D57/D60)"

### M39 — open

- createdAt: 2026-07-14T09:20:42.638Z
- updatedAt: 2026-07-14T09:20:42.638Z
- title: "G7-W1: D36 restart re-anchor (Reflector epoch-change detection + resequencer Rebaseline wiring)"

### M40 — open

- createdAt: 2026-07-14T09:20:42.702Z
- updatedAt: 2026-07-14T09:20:42.702Z
- title: "G7-W2: D37 first-path-up handshake (re)initiation"

### M41 — open

- createdAt: 2026-07-14T09:20:42.843Z
- updatedAt: 2026-07-14T09:20:42.843Z
- title: "G7-W3: one-sided-restart netns e2e, restart-recovery counters, docs sync"

### M42 — open

- createdAt: 2026-07-14T09:46:00.066Z
- updatedAt: 2026-07-14T09:46:00.066Z
- title: "G8-A: bind demux (AddrPort+quota), deferred-path, FEC per-peer hardening (serialized multipath.go)"

### M43 — open

- createdAt: 2026-07-14T09:46:00.112Z
- updatedAt: 2026-07-14T09:46:00.112Z
- title: "G8-B: device per-peer teardown wiring + primary-peer name attribution"

### M44 — open

- createdAt: 2026-07-14T09:46:00.976Z
- updatedAt: 2026-07-14T09:46:00.976Z
- title: "G8-C: multi-peer netns e2e extension + deferred privileged run (o3 + llm-ubuntu-0)"

### M45 — open

- createdAt: 2026-07-14T10:00:09.529Z
- updatedAt: 2026-07-14T10:00:09.529Z
- title: "G9-W: config loader/validation hardening (serialized: strict-decode → durations → allowed_ips/default-route)"

### M46 — open

- createdAt: 2026-07-14T10:07:03.072Z
- updatedAt: 2026-07-14T10:07:03.072Z
- title: "G10-W: observability & metrics accuracy (bind tx-byte + fallback WARN serialized; reloadWarnings parallel)"

### M47 — open

- createdAt: 2026-07-14T10:08:49.641Z
- updatedAt: 2026-07-14T10:08:49.641Z
- title: "G11-W: hygiene sweep (lint-gate first; then e2e-ports / config-comments / bind-seams / capability)"

### M48 — open

- createdAt: 2026-07-14T11:41:36.409Z
- updatedAt: 2026-07-14T11:41:36.409Z
- title: "Plan: live monitoring web UI (edge + concentrator)"
- description: "Coordination milestone for the plan-flow goal: embedded live-monitoring webpage with WebSocket status updates (link quality/peer/loss/RTT/FEC) on both edge and concentrator, plus local-API authorization hardening."

### M49 — open

- createdAt: 2026-07-14T12:11:13.644Z
- updatedAt: 2026-07-14T12:11:13.644Z
- title: "Investigate: low-throughput-loss-bufferbloat"

### M50 — open

- createdAt: 2026-07-14T12:13:11.545Z
- updatedAt: 2026-07-14T12:13:11.545Z
- title: "Plan: weighted-mode operability & pacing safety"
- description: "Coordination milestone for the plan-flow goal covering operability gaps found while testing policy=\"weighted\" with pacing on/off: aggregation-engagement observability, capacity-sizing safety (warn-loud/auto-derive), and the pacing on/off tradeoff (docs + a latency/priority class for control frames). Groups the goal, its clarifying questions, reviews, and the approval decision."

### M51 — open

- createdAt: 2026-07-14T12:39:47.365Z
- updatedAt: 2026-07-14T12:39:47.365Z
- title: "G13: E2E overload+sampling harness"
- description: Work milestone for G13. Extends the -tags e2e netns fixture (test/e2e) with a sustained calibrated tunnel-load driver plus concurrent /metrics and structured-log sampling helpers — the shared capability Q55 flagged as missing. Prerequisite for the observability and probe-protection e2e acceptances.

### M52 — open

- createdAt: 2026-07-14T12:39:49.619Z
- updatedAt: 2026-07-14T12:39:49.619Z
- title: "G13: Weighted capacity-sanity guard"
- description: "Work milestone for G13 item 2 (Q52 Option 3, scoped to the GUARD ONLY per Q53). Config-load capacity-sanity check for policy=\"weighted\": hard-fail when declared link_bandwidth proves aggregation can never engage; startup WARN + wanbond_weighted_capacity_sane gauge when bandwidth is undeclared/unverifiable. Does NOT own the G2/Q20 per_path_capacity_fps auto-derive or BDP-sizing docs."

### M53 — open

- createdAt: 2026-07-14T12:39:55.737Z
- updatedAt: 2026-07-14T12:39:55.737Z
- title: "G13: Aggregation-engagement observability"
- description: Work milestone for G13 item 1 (Q54). Exposes the WeightedScheduler aggregation-gate state (s.aggregating, EWMA loadRate, engage/disengage thresholds) as per-peer Prometheus gauges (wanbond_aggregation_engaged / wanbond_offered_load_fps / engage+disengage threshold gauges), adds an engage↔disengage INFO transition log, and validates the "configured but inert" blind spot via -tags e2e scenarios.
- dependsOn: ["M51"]

### M54 — open

- createdAt: 2026-07-14T12:39:58.494Z
- updatedAt: 2026-07-14T12:39:58.494Z
- title: "G13: Probe protection under pacer overload"
- description: Work milestone for G13 item 3(ii) (Q51). Protects wanbond's own telemetry PROBE frames (frame.KindProbe) so pacer-era DATA overload cannot induce spurious path-DOWN / failover flaps, via exempt-but-charged probe accounting in the weighted pacer. Inner-tunnel ICMP prioritization is explicitly OUT of scope (opaque ClassData).
- dependsOn: ["M51"]

### M55 — open

- createdAt: 2026-07-14T12:40:04.019Z
- updatedAt: 2026-07-14T12:40:04.019Z
- title: "G13: Operability documentation"
- description: Work milestone for G13 item 3(a) docs + the Q51 infeasibility note. Documents the measured pacing on/off tradeoff, the codified frame-class priority model (ClassControl exempt-uncharged, KindProbe exempt-but-charged, ClassData fully paced), the new operability signals, and the explicit architectural note that inner-tunnel ICMP prioritization is infeasible — referencing (not restating) G2/Q20's BDP-sizing docs.
- dependsOn: ["M53","M52","M54"]

### M56 — open

- createdAt: 2026-07-14T12:57:12.928Z
- updatedAt: 2026-07-14T12:57:12.928Z
- title: "Plan: fix D65 (tunnel single-flow throughput / bufferbloat)"

### M57 — open

- createdAt: 2026-07-14T13:14:52.912Z
- updatedAt: 2026-07-14T13:14:52.912Z
- title: "wanbond D65: BDP-sized egress pacing on the default active-backup scheduler"

### M58 — open

- createdAt: 2026-07-14T13:14:53.131Z
- updatedAt: 2026-07-14T13:14:53.131Z
- title: "wanbond D65: forwarded-TCP MSS clamp operator recipe (docs)"

### M59 — open

- createdAt: 2026-07-14T13:14:58.930Z
- updatedAt: 2026-07-14T13:14:58.930Z
- title: "wanbond D65: policy-independent pacing config + composition wiring"
- dependsOn: ["M57"]

### M60 — open

- createdAt: 2026-07-14T13:15:03.407Z
- updatedAt: 2026-07-14T13:15:03.407Z
- title: "wanbond D65: docs sync + green definition-of-done gate"
- dependsOn: ["M59"]

### M64 — open

- createdAt: 2026-07-15T06:03:27.624Z
- updatedAt: 2026-07-15T06:03:27.624Z
- title: "Plan: fix D79+D76 active-backup pacing correctness"
- description: Coordination milestone for the defect-seeded fix goal covering D79 (per-path pacer capacities misassigned across bind membership churn — reintroduces the D65 fast-path-throttled-to-slow-rate fault) and D76 (ActiveBackup lacks ProbeBudget — probe/echo egress unaccounted under active-backup+pacing → spurious primary path-DOWN failover flap). Both are churn/coverage holes in the T150–T153 active-backup pacing feature, filed out-of-scope by reviewers, root-caused ready-to-seed. Manually bridged into a fix goal (cq lacks an autonomous root-caused-defect sweep — see the filed cq bug report).

### M65 — open

- createdAt: 2026-07-15T06:08:23.852Z
- updatedAt: 2026-07-15T06:08:23.852Z
- title: "G15/D76: ActiveBackup implements sched.ProbeBudget (probe/echo pacing headroom)"
- description: "Fix defect D76 (MEDIUM): *ActiveBackup does not implement sched.ProbeBudget, so the emitProbes + echo-reflection charge sites no-op and the out-of-band probe/echo stream reserves zero pacing headroom -> spurious active-primary path-DOWN flap under active_backup + pacing. Independent of the D79 track. Links goals:G15, defects:D76."

### M66 — open

- createdAt: 2026-07-15T06:08:26.453Z
- updatedAt: 2026-07-15T06:08:26.453Z
- title: "G15/D79: identity-keyed active-backup per-path pacer config across membership churn"
- description: "Fix defect D79 (HIGH): active-backup per-path pacer config is carried by slice position / tail-inheritance (AddPath, SetPaths/resizeActiveBackupPacers), never re-sourced from the path's own identity-keyed cfg.Scheduler.PerPathCapacities, so a T55 deferral/promotion/reload churn mis-paces a bound path at another cfg.Paths entry's capacity (reintroduces D65). Extend the DynamicScheduler membership calls to carry per-path pacer config by identity and wire the bind to source it from cfg by m.defs index. Independent of the D76 track. Links goals:G15, defects:D79, defects:D65."

### M67 — open

- createdAt: 2026-07-15T06:08:28.604Z
- updatedAt: 2026-07-15T06:08:36.103Z
- title: "G15: definition-of-done gate (build + test + lint + race, active-backup pacing fix)"
- description: "Final green gate for both D76 and D79 fixes: nix develop -c just build && just test && just lint (default+e2e+realhosts tags) + go test -race ./internal/sched/... ./internal/bind/.... Placeholder dependsOn tokens replaced with the real W_D76/W_D79 milestone ids after creation. Links goals:G15."
- dependsOn: ["M65","M66"]

### M68 — open

- createdAt: 2026-07-15T06:21:43.330Z
- updatedAt: 2026-07-15T06:21:43.330Z
- title: "Plan: fix reseq re-anchor correctness residuals (D34/D64/D68)"
- description: "Defect-seeded fix goal cluster: internal/reseq re-anchor correctness. D34 (Rebaseline lacks source-identity gate on the hub-failover path), D64 (ObserveRecovered re-pins next to a recovered frame's seq after plain Rebaseline — pendingLow guard only covers RebaselineToLow), D68 (stale rebaselines doc-comment scoping to hub-failover only, now also peer-restart). Manual bridge for the cq root-caused-defect sweep gap."

### M69 — open

- createdAt: 2026-07-15T06:21:46.512Z
- updatedAt: 2026-07-15T06:21:46.512Z
- title: "Plan: fix bind path-lifecycle & demux hardening (D30/D62/D63/D66/D67/D71)"
- description: "Defect-seeded fix goal cluster: internal/bind path-lifecycle + source-binding demux correctness. D67 (attachSharedPathLocked rollback swallows detach error), D62 (teardown-vs-bind race installs dead-peer binding), D71 (reconcile promote-failure unlogged), D30 (auto-mode runtime-added paths never device-bind), D66 (stale single-peer-receive comment), D63 (demux eviction FIFO not LRU — design refinement per pinned T123). Manual bridge for the cq root-caused-defect sweep gap."

### M70 — open

- createdAt: 2026-07-15T06:21:50.312Z
- updatedAt: 2026-07-15T06:21:50.312Z
- title: "Plan: fix metrics/server + reload observability (D70/D72/D74/D75/D83/D84)"
- description: "Defect-seeded fix goal cluster: observability + server hygiene. D84 (metrics.Server.Close leaks listener when never Started — mirror the T162 monitor fix), D83 (loopback classification duplicated between metrics/server.go and netutil.IsLoopbackHost — dedup), D70 (reload same-name path link_bandwidth/link_rtt change unwarned), D74 (reload doesn't recompute the weighted-capacity gauge + reloadWarnings gap — overlaps D70), D72 (WeightedScheduler.SetPaths aggregation collapse unlogged), D75 (idle-gap aggregation-change log-field test missing). Manual bridge for the cq root-caused-defect sweep gap."

### M71 — open

- createdAt: 2026-07-15T06:21:53.328Z
- updatedAt: 2026-07-15T06:21:53.328Z
- title: "Plan: fix e2e test-correctness (D80/D81)"
- description: "Defect-seeded fix goal cluster: test/e2e correctness. D81 (multipeer_test.go non-primary peer INBOUND assertion uses MetricRxBytes which is structurally 0 for a shared readLoop — switch to MetricTxBytes, mirror the fixed multipeer_hardened_test.go), D80 (restart_onesided_test.go binds concentrator /metrics to a non-loopback uplink IP which NewServer refuses — apply the D77 netns-loopback fetchMetricsInNetns remediation). Manual bridge for the cq root-caused-defect sweep gap."

### M72 — open

- createdAt: 2026-07-15T06:21:56.416Z
- updatedAt: 2026-07-15T06:21:56.416Z
- title: "Plan: fix docs/operator-guidance residuals (D35/D61)"
- description: "Defect-seeded fix goal cluster: docs/operator-guidance. D35 (full-tunnel operator guidance — warn that a literal 0.0.0.0/0 default route installed OUTSIDE wanbond must exclude the concentrator underlay endpoint or use the /1+/1 split; production already mitigated by T107+T108, residual is docs-only), D61 (re-adjudicate D54's root-cause mechanism attribution and reword the Justfile lint comment if the 'walks the repo root' claim is a misattribution). Manual bridge for the cq root-caused-defect sweep gap."

### M73 — open

- createdAt: 2026-07-15T06:26:37.568Z
- updatedAt: 2026-07-15T06:26:37.568Z
- title: "reseq re-anchor correctness fixes (G16: D34/D64/D68)"
- description: "Work milestone for plan-flow goal G16. Closes the internal/reseq re-anchor correctness residuals left by T119/G7: D34 (source-identity gate on the plain Rebaseline hub-failover path), D64 (recovered frames must not re-anchor an unstarted ring on the Rebaseline path), D68 (rebaselines-counter doc-comment reword). Reproduce-first regression tests bundled with each fix; DoD gate task closes the milestone."

### M74 — open

- createdAt: 2026-07-15T06:27:20.358Z
- updatedAt: 2026-07-15T06:27:20.358Z
- title: "internal/bind path-lifecycle & demux correctness (G17: D67, D62, D30)"
- description: "Reproduce-first fixes for the three substantive correctness residuals in internal/bind from the G4/G6 multi-peer work: D67 (attach rollback swallows detach error + stale peerPathState on detach failure), D62 (bind-vs-unbind race installs a binding to a torn-down peer with no self-heal), D30 (auto/source runtime-added paths never device-bind, unlike Open). Part of goal G17."

### M75 — open

- createdAt: 2026-07-15T06:27:26.188Z
- updatedAt: 2026-07-15T06:27:26.188Z
- title: "internal/bind diagnostics, docs & DoD gate (G17: D71, D66, D63)"
- description: "Low-risk diagnostics + documentation fixes and the goal's definition-of-done gate: D71 (add a deduped WARN on the reconcileDeferred promote-failure branch), D66 (reword the stale 'demux is a later G4 task' comment), D63 (confirm T123 pinned FIFO as intended, correct the mislabelled 'LRU' comment, recommend wontfix), and a final full-gate task. Part of goal G17; depends on the correctness milestone."
- dependsOn: ["M74"]

### M76 — open

- createdAt: 2026-07-15T06:27:27.666Z
- updatedAt: 2026-07-15T06:27:27.666Z
- title: "G18 fix: metrics/reload/weighted observability residuals (D70,D72,D74,D75,D83,D84)"
- description: "Work milestone for defect-seeded goal G18. Three disjoint work areas, parallelizable: (A) internal/metrics/server.go [D83,D84]; (B) internal/device/device.go + internal/metrics/metrics.go [D70,D74 overlap]; (C) internal/sched/weighted.go [D72,D75]. Same-file tasks are dependsOn-sequenced to avoid merge conflict; a DoD gate task closes the set."
- dependsOn: ["M70"]

### M77 — open

- createdAt: 2026-07-15T06:27:40.775Z
- updatedAt: 2026-07-15T06:27:40.775Z
- title: G20 — D35/D61 docs & ledger-record residual fixes

### M78 — open

- createdAt: 2026-07-15T06:27:59.473Z
- updatedAt: 2026-07-15T06:27:59.473Z
- title: Fix e2e test-correctness (D80/D81 metrics-scrape test bugs)

### M79 — open

- createdAt: 2026-07-20T15:49:17.959Z
- updatedAt: 2026-07-20T15:49:17.959Z
- title: "Plan: extend monitoring-UI stats page (addressing + more)"
- description: Coordination milestone for the plan-flow goal to EXTEND the G12 live monitoring web UI (internal/monitor MonitorSnapshot + the web/ Vite dashboard) with additional operator-useful fields — starting with edge/concentrator IP addressing, and inviting the planner to propose what else is worth surfacing. Greenfield feature extension of the completed G12 (done).

### M80 — open

- createdAt: 2026-07-20T17:26:16.511Z
- updatedAt: 2026-07-20T17:26:16.511Z
- title: "Investigate: field MTU-shred + liveness flap"
- description: "Coordination milestone for two real-hardware field-hardening findings from a Starlink+5G edge deployment: H1 (per-path MTU mismatch shreds large packets on the smaller-MTU 5G path — single hardcoded DefaultPathMTU, no per-path PMTU discovery, no mtu config knob, overhead constant possibly under-counted) and H2 (liveness DownAfter not tunable, so a flappy-but-usable primary thrashes the bond onto metered 5G). Investigate + confirm/refute each with file:line + repro, then seed fix goal(s)."

### M81 — open

- createdAt: 2026-07-20T17:37:32.821Z
- updatedAt: 2026-07-20T17:37:32.821Z
- title: "Plan: field-hardening (per-path MTU + tunable liveness)"
