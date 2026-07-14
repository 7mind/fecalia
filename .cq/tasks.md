---
ledger: tasks
counters:
  milestone: 0
  item: 159
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
  - id: M11
    path: ./archive/tasks/M11.md
    summary: "Deferred-defect hardening round complete: 9 fix tasks T42-T50 delivered (each opus+fable-reviewed, gated, -race-clean, merged to main), resolving 12 defects (D3,D4,D10,D13,D14,D20,D22,D23,D24,D25,D26,D28). Highlights: T44 CONTROL-frame anti-replay (MAC-covered Seq + ControlGuard); T45 FEC prefix-stability invariant + quiescence-accurate unrecoverable counter; T46 target_residual adaptive-FEC SLA sizing (sanctioned new config surface per Q16); T47 AmneziaWG-profile-aware pacer control-frame exemption (caught+fixed a vanilla-only classifier blindness on re-review); T42 non-vacuous goroutine-leak gate; T43 duplicate source_addr + global-v6 device-bind fixes; T49 throughput-ceiling doc sweep to measured 4-vCPU numbers; T50 e2e/realhosts-tagged lint coverage; T48 reboot-persistent firewall provisioning (repo-side). D7 (live-apply) + D8 remain non-terminal pending the manual o3 iptables ops per Q14 (o3 is a test host)."
    title: Deferred-defect hardening round (D3/D4/D7/D8/D10/D13/D14/D20/D22/D23/D24/D25/D26/D28)
    status: done
  - id: M14
    path: ./archive/tasks/M14.md
    summary: "G2/W2 pacing empirical sizing + BDP config wiring COMPLETE (CORE SCOPE 1, Q20=both). T52 capped-fixture BDP measurement (report-only), T53 wired SizePacingFromBDP into config load from operator-declared per-link bandwidth (load-time only, NOT runtime auto-tuning; pacing default-DISABLED), T56 operator tuning procedure (docs/install.md §3a + design.md; 1540B/frame), T61 ENABLED-pacing bufferbloat + no-rekey-starvation e2e (relative gate). All 4 tasks done, 4 reviews go-ahead (opus), merged to main (c803cb5 T53, b9f5983 T56, 40205c1 T61). HARDWARE-VALIDATED on llm-ubuntu-0 (amd64 4-vCPU): bufferbloat 208.5ms(unpaced)→0.5ms(paced) at 4Mbit cap; BDP=33241B (21.6 frames @1540B), SizePacingFromBDP→capacityFPS=4179.9 burstFrames=21.6 @50Mbit/5.2ms. Numbers fed to the T65 pilot runbook."
    title: G2/W2 — Pacing empirical sizing + BDP config wiring (CORE SCOPE 1 + Q20 both)
    status: done
  - id: M16
    path: ./archive/tasks/M16.md
    summary: "G2/W4 real-link validation tier COMPLETE (CORE SCOPE 2, report-only). T58 aggregation-ratio + loaded-RTT/bufferbloat, T63 mid-transfer LINK + HUB failover, T64 short soak — all across llm-ubuntu-0 (amd64 NAT edge) <-> o3 (aarch64 public concentrator), all HARDWARE-VALIDATED. 3 tasks done, 3 reviews go-ahead (opus). Key real-link results: aggregation ratio ~0.25-0.46 (shared-physical-uplink topology, ratio<=1 EXPECTED — NOT a bandwidth-aggregation guarantee); bufferbloat 21-176ms under saturation (real-link variability); LINK failover ~1.4-1.5s, HUB failover ~1.7-2.1s with traffic RESUMED via standby (confirms the D32-fixed hub-failover data plane on a REAL cross-network link, 60-90 Mbit/s); short soak survived a WG rekey (0 path-down flaps). All o3-safe (reversible udp-scoped iptables, never deprovisioned; firewall fully restored each run)."
    title: "G2/W4 — Real-link validation tier (CORE SCOPE 2: aggregation + loaded-RTT + WAN-kill + short soak, report-only)"
    status: done
  - id: M17
    path: ./archive/tasks/M17.md
    summary: "G2/W5 pilot runbook + non-blocking exit criterion + full doc-sync COMPLETE (CORE SCOPE 3, Q19). T59 rollout runbook (docs/runbook.md — key/PSK gen, both-ends config, standby-concentrator via ordered endpoints + shared WG key, D7/D8 firewall persistence, /metrics health checks), T65 `just p0-baseline` automating the P0 real-link baseline (HARDWARE-VALIDATED: PASS 286s, report emitted), T66 recorded the non-blocking pilot exit criterion (runbook §7: capped-fixture W2 + report-only real-link W4 sufficient to enter a supervised pilot; soak runs DURING the pilot) + full doc-sync removing stale not-yet-built phrasing across README/design/install/manual-checklist/runbook. 3 tasks done, 3 reviews go-ahead. All metric/config claims verified against source; no overclaim (aggregation documented as report-only, single-uplink topology)."
    title: G2/W5 — Pilot runbook, non-blocking exit criterion + full doc sync (CORE SCOPE 3 + Q19)
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

### T11 — done

- createdAt: 2026-07-01T23:39:30.788Z
- updatedAt: 2026-07-06T21:11:15.883Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: Outer bonding frame codec + PSK-authenticated control/probe frames
- description: "Wire codec for the outer frame types: DATA (outer-seq, path-id, fec-group, flags) wrapping opaque WG datagrams; PARITY; PROBE; CONTROL. No plaintext magic constants or fixed offsets (requirement 6 groundwork). CONTROL/PROBE authenticated with the config PSK via a vetted AEAD/HMAC library (not hand-rolled); DATA headers unauthenticated by design (DoS-grade risk accepted). Own outer-seq space — never reuse the inner WG counter."
- acceptance: Unit round-trip tests for all four frame types preserve fields; tampered or PSK-mismatched CONTROL/PROBE frames are rejected; a decoder fuzz/property test runs clean without panic; a byte-histogram test asserts no byte position is constant across encodings of random payloads.
- suggestedModel: frontier
- dependsOn: ["T10"]
- ledgerRefs: ["goals:G1"]
- completion: "DONE (merged 9464e91). internal/frame: typed closed-sum codec for the 4 outer frame kinds (Data/Parity/Probe/Control). Wire format `nonce(24B XChaCha20) || obf(body) [|| HMAC-SHA256/16 tag]`: fresh crypto/rand nonce per frame, body XOR'd with a PSK-derived (HKDF-SHA256, domain-separated) keystream so no byte position is constant (requirement-6 groundwork); CONTROL/PROBE Encrypt-then-MAC authenticated (tamper/PSK-mismatch -> ErrAuth), DATA/PARITY unauthenticated by design (inner WG authenticates). DATA carries own outer-seq/path-id/fec-group/flags; payload opaque. Deps: crypto/hkdf (stdlib 1.26) + x/crypto/chacha20 (promoted to direct; go.sum + vendorHash unchanged). Full gate green + 20s fuzz clean + nix build OK. Reviewed opus+fable, R7 go-ahead (unanimous r1). Filed D4 (anti-replay -> T13) + D5 (hot-path Codec refactor -> T12)."
- sessionLogs: [".cq/logs/20260706-211500-ae614f805e5cb18d0.md",".cq/logs/20260706-211500-a8aeb19256ab53115.md",".cq/logs/20260706-211500-a28cc8d9376a6a85b.md"]
- resultCommit: "9464e91"

### T12 — done

- createdAt: 2026-07-01T23:39:43.724Z
- updatedAt: 2026-07-06T23:09:18.418Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: "Multi-path conn.Bind: per-path sockets behind one virtual endpoint + MTU accounting"
- description: |
    Replace the pass-through Bind: one UDP socket per configured path bound to its source address; WG sees a single stable virtual endpoint per peer while the Bind privately maps real per-path endpoints; honor the batched send/recv + GSO/GRO findings from P0. MTU accounting for outer header + WG overhead (no fragmentation / ICMP black holes); write the MSS-clamping guidance doc section.
    
    FOLLOW-UP SCOPE (2026-07-06, from the G1 follow-up + P0 real-host throughput investigation): (a) set a LARGE SO_RCVBUF per path (like pure-WireGuard StdNetBind's ~7MB via SetReadBuffer) and adopt batched send/recv (GSO/GRO best-effort per-path with the engine's runtime-disable discipline) to close the confirmed P0 §2 efficiency gap — the pass-through Bind used DEFAULT socket buffers, which the real-host run showed adds loss under load (though single-flow TCP over the lossy WAN, not the Bind, was the primary throughput limiter). (b) ADDRESS DEFECT D5 (hot-path Codec) here, since this is where internal/frame is first wired into the per-datagram datapath: build a frame.Codec state ONCE from the PSK (NewCodec: derive HKDF subkeys once, single keystream per Decode, dst-append buffer reuse) instead of re-deriving subkeys + double-initing ChaCha20 per frame.
- acceptance: "Unit tests for virtual-endpoint identity and per-path endpoint bookkeeping; e2e: traffic flows over each path individually when the other is disabled; a max-MTU-sized transfer shows no IP fragmentation in a fixture capture; computed inner MTU = path MTU - (outer header + WG overhead) asserted against a fixture; MSS guidance committed; each per-path socket sets a large SO_RCVBUF; the datapath uses a once-constructed frame.Codec (resolves D5)."
- suggestedModel: frontier
- dependsOn: ["T11"]
- ledgerRefs: ["goals:G1","defects:D5"]

### T13 — done

- createdAt: 2026-07-01T23:39:47.454Z
- updatedAt: 2026-07-06T22:48:38.578Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: Per-path quality probes and liveness state machine
- description: PSK-authenticated probe frames measuring per-path RTT/loss/jitter, plus outer-seq gap accounting for passive loss estimation; a path up/down state machine with configurable detection thresholds. Path liveness is entirely ours (WG keepalive is per-peer, not per-path).
- acceptance: "Estimator unit tests on synthetic traces converge to injected RTT/loss/jitter within tolerance; a forged/tampered probe is rejected; e2e: a blackholed path is marked down within the configured detection threshold and the transition is logged with per-path fields."
- suggestedModel: standard
- dependsOn: ["T11"]
- ledgerRefs: ["goals:G1"]

### T15 — done

- createdAt: 2026-07-01T23:40:01.204Z
- updatedAt: 2026-07-06T23:43:08.288Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: Active-backup scheduler with transparent failover
- description: "Send-side scheduler v1: a single active path (Starlink-preferred) carries traffic; on a path-down signal from telemetry, instant switch to the backup path; switch-back with hysteresis on recovery (no thrash). Data-thrift by construction (backup idle until needed). Expose hooks the later weighted/FEC-aware scheduler extends. This is the P1 MVP core."
- acceptance: "Unit test: with two paths up all data egresses the active path; a path-down event switches egress to the backup within the configured detection window; recovery does not thrash the selection."
- suggestedModel: frontier
- dependsOn: ["T12","T13"]
- ledgerRefs: ["goals:G1"]

### T16 — done

- createdAt: 2026-07-01T23:40:04.937Z
- updatedAt: 2026-07-07T12:57:18.675Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: Edge public-IP change survival (per-path re-roaming)
- description: "Handle the mobile case: the edge's public IP changes on a path (NAT rebinding / carrier CGNAT churn). The concentrator's Bind re-learns that path's real endpoint from authenticated probe/control traffic without disturbing the other path or the WG session."
- acceptance: "e2e: re-address the edge-side veth of one path mid-transfer — that path recovers and the TCP transfer completes without reset; the other path is unaffected."
- suggestedModel: standard
- dependsOn: ["T12","T13","T37"]
- ledgerRefs: ["goals:G1"]

### T20 — done

- createdAt: 2026-07-01T23:40:28.766Z
- updatedAt: 2026-07-07T18:04:52.526Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: "P1 e2e: failover survives WAN death within 3s"
- description: "e2e test driving the active-backup path: start a long-lived TCP flow (SSH-like / iperf3) through the tunnel, then kill the active WAN namespace mid-transfer and assert the flow survives with no connection reset and throughput recovers. Uses the P1RecoverySeconds constant from the harness table."
- acceptance: "`sudo go test -tags e2e ./test/e2e -run TestP1Failover` kills the active path mid-iperf3; the TCP connection is NOT reset and throughput is restored within P1RecoverySeconds (3s), asserted against the harness constants; repeated flap does not wedge the tunnel."
- suggestedModel: standard
- dependsOn: ["T15","T16","T37","T39","T40"]
- ledgerRefs: ["goals:G1","tasks:T40","reviews:R26"]

### T22 — done

- createdAt: 2026-07-01T23:40:41.392Z
- updatedAt: 2026-07-07T18:20:29.107Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: systemd units, cross-compile matrix, install doc + P1 manual checklist
- description: |
    Per Q6: systemd unit files for the edge and concentrator roles; a CI/release step cross-compiling CGO_ENABLED=0 for linux/amd64 and linux/arm64; an install/ops doc referencing the 0600 config path; and the scripted P1 manual checklist for the real Starlink+5G+VPS setup appended to docs/manual-checklist.md. No packaging beyond the binary + nix.
    
    FOLLOW-UP SCOPE (2026-07-06, from P0 real-host validation): the install/ops doc MUST document the CONCENTRATOR tunnel-interface firewall requirement — the concentrator must ACCEPT traffic on the wanbond tunnel interface (e.g. `iptables -I INPUT -i <tun> -j ACCEPT`) ahead of any default REJECT. OCI images ship `-A INPUT -j REJECT --reject-with icmp-host-prohibited`, which blocks tunnel TCP while ICMP (ping) slips through — producing a confusing 'No route to host' on TCP-through-the-tunnel (hit during P0 real-host testing). Document this as a required concentrator deployment step.
- acceptance: "`just release` (or make) produces static binaries for linux/amd64 and linux/arm64 (`file` reports statically linked, correct arch); `systemd-analyze verify` passes on both unit files; install doc and P1 checklist committed; the install doc documents the concentrator tunnel-interface firewall ACCEPT requirement (OCI default REJECT caveat)."
- suggestedModel: fast
- dependsOn: ["T20"]
- ledgerRefs: ["goals:G1","reviews:R27","defects:D7"]

### T30 — done

- createdAt: 2026-07-01T23:54:17.561Z
- updatedAt: 2026-07-07T13:29:38.170Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: Runtime path add/remove (dynamic path set + config reload)
- description: "Per the FUNCTIONAL requirement 'Path up/down + add/remove' and 'design for N': allow adding or removing a path from the active bonded set at runtime (e.g. SIGHUP config reload or a control-socket command), beyond the up/down liveness of T13. Adding a path opens its per-path socket, begins probing, and admits it to the scheduler once healthy; removing a path drains and closes it. Must not disturb existing paths, in-flight resequencing, or the WG session (single virtual endpoint preserved)."
- acceptance: "Unit + e2e tests: starting with one path, adding a second at runtime brings it into the scheduler once its probes report healthy and traffic begins using it, with zero reset of an in-flight TCP flow; removing a path drains and closes it while the flow continues on the remaining path; the WG session and the other path are undisturbed throughout."
- suggestedModel: frontier
- dependsOn: ["T12","T13","T15"]
- ledgerRefs: ["goals:G1"]

### T37 — done

- createdAt: 2026-07-06T23:36:14.588Z
- updatedAt: 2026-07-07T00:14:23.242Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: "Per-path probe transport: drive T13 Prober/Liveness over the multipath Bind"
- description: "DISCOVERED PREREQUISITE (filed during T15 implement-review by both opus+fable, 2026-07-06). T13 built the probe/liveness LOGIC (Prober, Liveness, Reflector, estimator) and T15 built the active-backup scheduler consuming per-path liveness, but NOTHING drives probes over the wire: no send/receive loop emits PROBE frames on the per-path sockets, reflects inbound probes on the peer, or feeds echoes into per-path *telemetry.Prober (HandleEcho/Tick). Consequently device.buildScheduler injects sched.AlwaysUp for every path (documented placeholder) and real on-wire failover is INERT. This task wires the transport: a per-path, timer-driven probe loop over the multipath Bind (emit frame KindProbe per path via the Prober; Reflect inbound KindProbe in the Bind receiver; HandleEcho on echoes; Tick per path), then replace the AlwaysUp slice in device.buildScheduler with the live per-path *telemetry.Prober instances (the mutex-guarded PathHealth sources). MUST ALSO close the concentrator-side failover gap (D-concentrator-remote-learn): the Bind receiver currently learns a path's return remote ONLY from DATA frames, so a backup path can be StateUp via probe echoes yet getRemote() is false -> Send returns errNoHealthyPath on failover; learn ps.setRemote(srcAP) from AUTHENTICATED probe/echo frames too (this also provides the authenticated-remote-learning that gates D9's unauthenticated-DATA remote-learn DoS). Preserve the T12 concurrency model (lock-free receive fast path, atomic dst, syscall-outside-mutex) and use only synchronized PathHealth sources (*Prober, not bare Liveness)."
- acceptance: "Unit/integration: a fake-clock per-path probe loop emits PROBE frames at the configured cadence, reflects inbound probes, and feeds echoes into each path's Prober so Liveness transitions Up/Down drive the scheduler; a blackholed path (probes stop echoing) is marked Down within the T13 detection window and the scheduler fails egress over; the concentrator learns a backup path's remote from an authenticated probe BEFORE that path becomes active (getRemote() true on a probe-only path). Wired into device.Up replacing AlwaysUp. e2e wiring readiness for T20 (compiles under -tags e2e). No data race under -race (only *Prober used as concurrent PathHealth)."
- suggestedModel: frontier
- dependsOn: ["T13","T15"]
- ledgerRefs: ["goals:G1","tasks:T20","tasks:T16"]

### T38 — done

- createdAt: 2026-07-07T00:15:22.349Z
- updatedAt: 2026-07-07T11:54:30.968Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: "Probe anti-replay session epoch: survive peer restart without a liveness deadlock"
- description: "DISCOVERED ROBUSTNESS DEFECT (D12, filed by the T37 review, HIGH). The per-path probe anti-replay (T13 D4: Prober.nextSeq + Reflector/AntiReplay high-water) is a strict-monotonic in-memory counter with no session/boot identity. After a peer RESTART, the restarted side's nextSeq resets to 0 while the surviving peer's Reflector retains the prior session's high-water N, so every fresh probe (seq<=N) is rejected as ErrReplay -> no echoes -> restarted side's paths never come Up -> scheduler Pick() returns none -> no WG handshake, for minutes-to-hours until the counter organically passes N. Fix: carry a random per-boot session id in the Probe frame INSIDE the MAC-covered body, and key the Reflector's anti-replay by (sessionId, pathID), resetting the high-water when a NEW authenticated sessionId is first observed on a path (with a monotonic-tiebreak / anti-rollback guard so an attacker cannot force-reset with an OLD replayed sessionId+low-seq); the originator's HandleEcho guard resets likewise on its own boot. Preserve strict-monotonic replay protection WITHIN a session. This is a wire-format change to frame.Probe (adjacent to T37's IsEcho bit) + the telemetry anti-replay keying."
- acceptance: "Unit tests: within a session, replays are still rejected (D4 preserved); a peer-restart simulated by a NEW sessionId + seq-from-0 stream is ACCEPTED (high-water resets) so the restarted peer's paths come Up and failover/bring-up recovers within the T13 detection window (fake clock, no real sleeps); an OLD/replayed sessionId or a rollback attempt is REJECTED (no attacker-forced reset). The session id is authenticated (inside the MAC); the frame codec tamper/round-trip tests still pass. No data race under -race. Compiles under -tags e2e."
- suggestedModel: frontier
- dependsOn: ["T37"]
- ledgerRefs: ["goals:G1","defects:D12"]

### T39 — done

- createdAt: 2026-07-07T14:02:51.949Z
- updatedAt: 2026-07-07T15:46:37.653Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: Meet the P1 3s failover budget in BOTH directions (fix the reply-direction recovery tail)
- description: "DISCOVERED by the T20 hardware review (D15, HIGH): the merged failover machinery does NOT reliably meet the P1 acceptance (throughput restored within P1RecoverySeconds=3s after killing the active WAN) — 4/14 hardware runs of TestP1Failover exceeded ~3.1s end-to-end recovery. The edge detects DOWN at kill+~1.7s and switches egress at kill+~1.9s, but BIDIRECTIONAL traffic (the reply direction) doesn't resume within 3s: the concentrator-side path-down detection composes with the edge-side, and the daemon's DownAfter=1500ms + interval=250ms (~1.75s per-side detection) leaves too thin a margin, jittering past 3s under CPU load. thresholds.go budgets only the edge-side term. FIX: (1) root-cause the exact reply-direction tail on hardware (both daemons at info log, synchronized timestamps, measure concentrator down-detection + reply-path switch vs the kill); (2) make failover reliably < 3s with comfortable margin — tighten DownAfter/interval for faster detection AND/OR make the reply-path switch piggyback on the edge's roam (the first authenticated packet arriving on the new path should immediately redirect the concentrator's replies to it, rather than waiting a full independent concentrator-side DownAfter); (3) reconcile thresholds.go (D16) so the composition analysis budgets BOTH directions and the daemon's actual probe timings are the single source of truth. Preserve no-thrash (failback hysteresis) and the false-down safety on jittery links. This is the PRODUCT work that makes P1 meet its core acceptance; T20 (the e2e) is reworked separately and depends on this."
- acceptance: On the reference hardware host (llm-ubuntu-0), a sound-measurement failover e2e (sub-100ms recovery measurement, strict < budget) passes RELIABLY (e.g. >=15/15 or >=19/20 runs) with end-to-end recovery < P1RecoverySeconds in BOTH directions after killing the active WAN; the root cause of the prior >3s tail is documented; thresholds.go's composition analysis budgets both directions and matches the daemon's actual timings; unit tests for any changed timing/scheduler logic pass under -race; no failover thrash regression.
- suggestedModel: frontier
- dependsOn: ["T13","T15","T37"]
- ledgerRefs: ["goals:G1","tasks:T20","defects:D15","defects:D16"]

### T40 — done

- createdAt: 2026-07-07T16:41:13.102Z
- updatedAt: 2026-07-07T18:04:50.518Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: Make repeated-flap failover reliably meet the 3s budget (fix D18 tail + sound flap test)
- description: "DISCOVERED by the T20 flap hardware review (D18): TestP1FailoverRepeatedFlap passes only ~12/15 on llm-ubuntu-0 — 3 GENUINE per-cycle recovery tails >3s (3476ms, 3151ms, one >4s lost) under REPEATED flap + sustained saturating bidir load, vs 0/46 for single-kill TestP1Failover. The cycle-1 fix, non-wedge, and no-reset all held 15/15; this is a per-cycle recovery-latency tail specific to the repeated-flap-under-saturation scenario. Builds on the T20 flap test (commit c495839, branch worktree-agent-a7db8aae02129b7c4 — the flap test LOGIC is opus-approved: cycle-1 current-active check, cycles>=2 windowed, non-vacuous). This task makes repeated-flap failover RELIABLE: (1) INVESTIGATE on hardware — run the flap MANY times with host load recorded to separate a real product tail from shared-VM (4-vCPU) contention noise; instrument per-kill probe-loop-tick vs receive-tick latency across consecutive cycles. (2) If PRODUCT (hypothesis: during a failover transition the surviving path's inbound flow momentarily pauses, so NEITHER the starved probe timer NOR T39's receive-path tick advances liveness for a window): bound that gap — e.g. advance liveness from the OUTBOUND/Send path too (Send IS scheduled during the reroute), emit probes more aggressively on a detected active-path change, or a scheduler nudge on Pick — preserving false-down safety + no-thrash. (3) Fix the T20 test measurement (fable criticism): widen flapFailoverPoll from P1RecoverySeconds+1s (4s) to ~8-10s so an over-budget failover is MEASURED (fails through the per-cycle budget Errorf) rather than lost to an unmeasured non-observation Fatalf. (4) If the tail is confirmed pure shared-VM noise, document it and make the test robust (record host load; the core single-kill P1 budget stands). Resolves D18 and completes the T20 flap acceptance."
- acceptance: On llm-ubuntu-0, TestP1FailoverRepeatedFlap passes RELIABLY (>=19/20 runs) with every cycle's recovery < P1RecoverySeconds in both directions (or, if the residual tail is proven to be shared-VM noise, documented as such with host-load evidence and the test made robust to it); the flapFailoverPoll window measures over-budget tails rather than losing them; the D18 root cause (product-vs-noise) is documented; non-wedge + no-reset + cycle-1 non-vacuity preserved; unit tests for any changed liveness/scheduler logic pass under -race; no single-kill/thrash regression (TestP1Failover still 60/60-class).
- suggestedModel: frontier
- dependsOn: ["T15","T37","T39"]
- ledgerRefs: ["goals:G1","tasks:T20","defects:D18","defects:D21","reviews:R26"]

## M7

### T14 — done

- createdAt: 2026-07-01T23:39:51.257Z
- updatedAt: 2026-07-06T23:39:51.172Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: "RS FEC engine: grouping, parity-emission deadline, recovery"
- description: "Reed-Solomon over opaque outer DATA frames using klauspost/reedsolomon: group frames by fec-group, emit K parity frames within a configurable grouping deadline (bounding grouping latency), receiver recovers up to K losses per group. Content-agnostic (operates on ciphertext). Pure library layer with a fake clock — no datapath wiring yet."
- acceptance: "Unit tests: for random drop patterns of <=K frames per group, all data frames are recovered; a property test shows parity is emitted within the configured deadline even for partially filled groups (asserted with a fake clock); measured overhead equals the configured parity ratio."
- suggestedModel: frontier
- dependsOn: ["T11"]
- ledgerRefs: ["goals:G1"]
- completion: "NOT STARTED — blocked on an INFRASTRUCTURE issue, not a task problem. Two consecutive implement-worker dispatches for T14 (agents a9855678ac296e2f9, a79b995486bf06b3d) DIED on startup with 0 tool-uses (~5-10s), each returning spurious injected-context/system-reminder fragments instead of doing any work. This is a subagent-dispatch/API failure specific to this run, not a fault in the task. Per the implement-flow ill-loop rule (two dead rounds) I stopped re-dispatching. RE-DISPATCH T14 fresh on the next /cq:advance run (a fresh session/context). The task itself is unchanged and ready (deps T11 done): implement internal/fec RS FEC engine (klauspost/reedsolomon) over opaque frames with a fake-clock grouping deadline + recovery, pure library + unit tests."

### T24 — done

- createdAt: 2026-07-01T23:40:49.927Z
- updatedAt: 2026-07-07T22:12:11.550Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: Integrate fixed-ratio FEC into the datapath + FEC metrics
- description: "Wire the RS engine into the datapath: send-side parity emission at the configured fixed ratio; receive-side recovery integrated BEFORE the resequencing buffer (reconstruct lost data frames from parity within a group, then hand recovered+received frames to resequencing and on to WG). Populate FEC overhead/recovered/unrecoverable counters on /metrics. Parity ratio from config."
- acceptance: "Unit/integration test: a receive stream with <=K dropped frames per group reconstructs the missing frames and delivers the full ordered payload to WG; recovery counter and FEC-overhead gauge update on /metrics."
- suggestedModel: frontier
- dependsOn: ["T14","T18","T21"]
- ledgerRefs: ["goals:G1","tasks:T14","tasks:T18","reviews:R32"]

### T25 — done

- createdAt: 2026-07-01T23:41:00.459Z
- updatedAt: 2026-07-07T23:28:39.484Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: "P3 e2e: recovery at injected loss with bounded overhead"
- description: e2e test injecting uniform netem loss and asserting FEC recovery and overhead against /metrics, using the harness constants (P3InjectedLossRates, P3MinRecoveredFraction, P3MaxOverheadFactor). Appends the P3 manual checklist.
- acceptance: "`sudo go test -tags e2e ./test/e2e -run TestP3FixedFEC`: at 5% and at 15% uniform injected loss, >=95% of lost data frames recovered without retransmit, and FEC overhead <= 2x the configured parity ratio; both read from /metrics; P3 manual checklist appended."
- suggestedModel: standard
- dependsOn: ["T24"]
- ledgerRefs: ["goals:G1","tasks:T24","reviews:R33","defects:D24"]

## M6

### T17 — done

- createdAt: 2026-07-01T23:40:09.142Z
- updatedAt: 2026-07-06T23:16:35.684Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: Prometheus /metrics endpoint (localhost) with per-path telemetry
- description: "Per Q7: a localhost-bound HTTP /metrics endpoint with a per-path registry — tx/rx bytes, loss, RTT, jitter, throughput, path state, plus FEC counters (registered now, populated in P3). This is the assertion surface for P2-P4 e2e acceptance. Binding to a non-loopback address is refused by default."
- acceptance: "Integration test: GET http://127.0.0.1:<port>/metrics returns per-path gauges/counters for bytes, loss, RTT and throughput matching fixture traffic; a non-loopback bind is refused; a harness scrape helper is committed."
- suggestedModel: standard
- dependsOn: ["T13"]
- ledgerRefs: ["goals:G1"]

### T18 — done

- createdAt: 2026-07-01T23:40:12.907Z
- updatedAt: 2026-07-07T01:18:13.278Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: Receive resequencing buffer (bounded window + timeout)
- description: Bounded-window + timeout resequencer on the receive side, applied after unwrap (and later after FEC recovery) and BEFORE delivery to the WG engine, so WG's anti-replay window never sees pathological multipath reorder. Tune the initial window against the P0-measured Starlink jitter; verify the WG anti-replay window still has margin.
- acceptance: "Unit/property tests: frames arriving out of order within the window are delivered in outer-seq order under synthetic reorder/duplication/loss traces; frames beyond window/timeout are released (not held forever); bounded memory; e2e: with both paths active, WG anti-replay drop count stays 0 under fixture jitter."
- suggestedModel: frontier
- dependsOn: ["T12"]
- ledgerRefs: ["goals:G1"]

### T21 — done

- createdAt: 2026-07-01T23:40:32.685Z
- updatedAt: 2026-07-07T19:37:31.732Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: Weighted aggregation scheduler + data-thrift policy
- description: "Extend the scheduler from active-backup to weighted aggregation: under load a single flow uses both paths distributed by per-path weight derived from internal telemetry (capacity/RTT/loss/backlog from T13); at low load it collapses to Starlink-preferred so metered 5G stays ~idle (requirement 2 must not regress), engaging 5G only on demand with hysteresis. Include send-pacing / bufferbloat control per the P0 findings (T9): pace egress per path so aggregation does not build standing queues that inflate latency-under-load (make pacing a no-op if T9 concludes it is unnecessary). FEC-aware hooks for P3+. Policy thresholds in config."
- acceptance: "Unit tests: under offered load exceeding one path, frames are distributed across both paths proportional to weights (within tolerance); with load below one path's capacity, distribution collapses to the primary (5G ~idle); a path-down event still fails over correctly (P1 preserved); with pacing enabled, per-path egress rate does not exceed the configured/derived pace and no unbounded send backlog accumulates under sustained overload."
- suggestedModel: frontier
- dependsOn: ["T15","T18"]
- ledgerRefs: ["goals:G1","reviews:R29","defects:D22"]

### T23 — done

- createdAt: 2026-07-01T23:40:45.709Z
- updatedAt: 2026-07-07T20:51:37.087Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: "P2 e2e: bonded throughput and 5G-idle assertions via /metrics"
- description: e2e test asserting aggregation and data-thrift against the /metrics endpoint under the netns profiles, using the harness constants (P2BondedMinFraction, P2MeteredMaxByteFraction). Appends the P2 manual checklist.
- acceptance: "`sudo go test -tags e2e ./test/e2e -run TestP2Aggregation`: under saturating load, bonded throughput >= 85% of the sum of the two paths' individual throughputs; while Starlink is healthy, 5G bytes < 1% of total; both read from /metrics; P2 manual checklist appended."
- suggestedModel: standard
- dependsOn: ["T21","T17"]
- ledgerRefs: ["goals:G1","tasks:T17","reviews:R30","reviews:R31","defects:D22","defects:D23"]

## M9

### T19 — done

- createdAt: 2026-07-01T23:40:16.740Z
- updatedAt: 2026-07-06T23:44:54.463Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: Expose amnezia obfuscation params (Jc/Jmin/Jmax, S1/S2, H1-H4) end-to-end
- description: Plumb the amnezia junk/obfuscation params from TOML config into the embedded engine on both roles as defense-in-depth; confirm junk packets arriving at the multi-path Bind are tolerated (P0 finding). Protocol mimicry stays out of scope (non-goal).
- acceptance: "e2e with non-default params set identically on both ends: the tunnel handshakes and passes traffic; with mismatched params the handshake fails closed; junk packets do not destabilize the Bind (no errors/wedge in a soak run)."
- suggestedModel: standard
- dependsOn: ["T8","T12"]
- ledgerRefs: ["goals:G1"]

### T26 — done

- createdAt: 2026-07-01T23:41:04.532Z
- updatedAt: 2026-07-08T01:38:34.638Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: "Automated wire-format audit: entropy + fixed-offset check"
- description: "Harness tool capturing tunnel pcaps in the fixture across multiple sessions (with FEC/parity and amnezia junk active), then asserting the requirement-6 properties programmatically: no byte position holds a constant value across sessions/packets, and mean per-packet payload entropy exceeds a named threshold constant. Failure output pinpoints the offending offset."
- acceptance: A tagged e2e test captures >=5 fresh sessions and the audit reports zero constant byte positions and mean per-packet payload entropy above the named constant; a deliberately-planted constant byte makes the test fail with the offset reported.
- suggestedModel: standard
- dependsOn: ["T24","T19"]
- ledgerRefs: ["goals:G1","tasks:T24","reviews:R36","defects:D28"]

### T28 — done

- createdAt: 2026-07-01T23:41:20.541Z
- updatedAt: 2026-07-08T02:23:30.157Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: nDPI/Suricata non-classification check + document UDP-block limitation
- description: "Run the captured fixture pcaps through nDPI and Suricata (provided by the dev shell) and assert neither classifies the flow as WireGuard or any identified VPN protocol. Document the known limitation: hostile networks blocking UDP wholesale have no in-scope mitigation (no TCP/TLS fallback — non-goal). Append the P5 real-link checklist."
- acceptance: "`sudo go test -tags e2e ./test/e2e -run TestP5DPI`: the pcaps are analyzed by nDPI and Suricata; neither labels the flow as WireGuard/VPN (asserted on their output); the UDP-block limitation and the P5 manual checklist are documented."
- suggestedModel: standard
- dependsOn: ["T26"]
- ledgerRefs: ["goals:G1","tasks:T26","reviews:R37"]

## M8

### T27 — done

- createdAt: 2026-07-01T23:41:16.490Z
- updatedAt: 2026-07-07T23:53:57.901Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: Adaptive FEC controller with hysteresis (simulation-tested)
- description: "Control loop adjusting FEC parity ratio (and scheduler weights) from measured per-path loss, with hysteresis and rate limiting — stability is the crux risk. Build a deterministic simulation harness (synthetic loss traces, no network) and test the controller in ISOLATION before touching the datapath: redundancy low when links are clean, scaling up under sustained loss, without thrash under noisy telemetry."
- acceptance: "Simulation tests: parity ratio rises with sustained loss and falls when loss clears; under a loss signal oscillating around a threshold the change rate is bounded by the hysteresis/rate-limit (no flap); converges to a steady ratio for steady loss; at 0% loss steady-state parity overhead is ~0."
- suggestedModel: frontier
- dependsOn: ["T25"]
- ledgerRefs: ["goals:G1","reviews:R34"]

### T29 — done

- createdAt: 2026-07-01T23:41:29.469Z
- updatedAt: 2026-07-08T00:47:57.541Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: Wire adaptive controller into datapath + P4 e2e vs fixed-FEC baseline
- description: "Wire the simulation-proven adaptive controller into the live datapath and verify against the P3 fixed-ratio baseline in the fixture, per Q1: equal loss masking for less total overhead, plus a steady-state residual-loss check. Uses harness constants (P4ResidualLossMax; adaptive-overhead <= fixed-FEC baseline)."
- acceptance: "`sudo go test -tags e2e ./test/e2e -run TestP4AdaptiveFEC`: for equal masking, adaptive total overhead bytes <= the P3 fixed-FEC baseline run; post-recovery residual loss <= 0.5% at steady 5% path loss; both read from /metrics; P4 manual checklist appended."
- suggestedModel: standard
- dependsOn: ["T27"]
- ledgerRefs: ["goals:G1","tasks:T27","tasks:T24","reviews:R35","defects:D25","defects:D26"]

## M10

### T31 — done

- createdAt: 2026-07-06T21:43:33.259Z
- updatedAt: 2026-07-06T22:09:01.471Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: "Add realhosts e2e tier: build tag, SSH runner, env-var host config, Justfile target"
- description: "Create test/realhosts/ behind a dedicated `realhosts` build tag, fully SEPARATE from the netns `e2e` tag (`go test ./...` and `-tags e2e` must compile none of it). Provide an SSH orchestration helper (exec-over-ssh with captured stdout/stderr, and sync of a per-arch-built wanbond binary — GOARCH=arm64 for the concentrator, amd64 for the edge, or build natively on each host) driven by env vars WANBOND_EDGE_HOST / WANBOND_CONC_HOST / WANBOND_CONC_PUBLIP / WANBOND_SSH_KEY, defaulting to llm-ubuntu-0.pgtr.7mind.io (edge, behind symmetric NAT), o3.7mind.io (concentrator), 89.168.124.91, and /run/agenix/llm-ssh-key. SSH must bypass the broken system ssh_config (use `-F none`). Add an opt-in Justfile target (e.g. `just realhosts [TEST]`) mirroring the existing `e2e`/`e2e-run` style; NEVER part of default `just test`/CI. Include a minimal connectivity test (run a trivial command on both hosts via the runner) so the tier is exercisable before any tunnel test exists. COMPLEMENTS the netns fixture; replaces nothing. Per Q12 the whole tier is REPORT-ONLY."
- acceptance: "`go build ./...` and `go test ./...` (no tag) compile nothing under test/realhosts/; `go vet -tags realhosts ./test/realhosts/...` passes; `just realhosts TestRealConnectivity` executes the SSH connectivity check against the default hosts and records both hosts' uname/arch. Report-only per Q12: the run executing and recording results IS the acceptance; it gates nothing."
- suggestedModel: standard
- ledgerRefs: ["goals:G1"]
- completion: "DONE (merged ebf95d5). test/realhosts/ behind a dedicated `realhosts` build tag (fully separate from netns `e2e`): runner.go = env-driven Host/Config (WANBOND_EDGE_HOST/CONC_HOST/CONC_PUBLIP/SSH_KEY defaults) + SSH Runner (`ssh -F none -i <key> -o StrictHostKeyChecking=accept-new -o ConnectTimeout ubuntu@host`); connectivity_test.go = report-only TestRealConnectivity (uname both hosts); opt-in `just realhosts [TEST]` Justfile target. Real SSH run verified: edge=x86_64, concentrator=aarch64. Reviewed opus+fable (R9 go-ahead) after fixing fable's 2 r1 criticisms (added -count=1 to all 3 test recipes to defeat go-test cache replay; fixed a comment). Also closed the pre-existing e2e/e2e-run cache-replay hazard in the same fix."
- sessionLogs: [".cq/logs/20260706-220000-ab78416a54b9fd747.md",".cq/logs/20260706-220000-a9d08b86138121cb3.md",".cq/logs/20260706-220000-a297f9214a676fd4c.md",".cq/logs/20260706-214500-a81aea62fc7e86149.md"]
- resultCommit: ebf95d5

### T32 — done

- createdAt: 2026-07-06T21:43:40.746Z
- updatedAt: 2026-07-06T22:27:51.213Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: Idempotent real-host provisioning + concentrator tunnel-interface firewall rule
- description: "Extend the realhosts tier with an idempotent provisioning step (from the harness and/or a `just realhosts-provision` target) that ensures on both hosts: iperf3 + gcc installed (apt), Go 1.26.x at /usr/local/go. On the CONCENTRATOR, ensure an iptables rule ACCEPTing traffic on the wanbond tunnel interface, inserted BEFORE OCI's default `-A INPUT -j REJECT --reject-with icmp-host-prohibited` (which blocks TCP over the tunnel while ICMP slips through — this exact symptom was hit at P0), and NOT duplicated on re-run. Every step checks current state before mutating (obey the remote-worker caution rules: no destructive ops, never sever own SSH access, NEVER deprovision the o3 OCI instance)."
- acceptance: Running provisioning twice succeeds and the second run reports no changes (idempotent); afterwards `go version`/`iperf3 --version`/`gcc --version` succeed over SSH on both hosts, and the concentrator's iptables shows exactly one tunnel-interface ACCEPT rule ordered before the OCI REJECT. Report-only per Q12.
- suggestedModel: standard
- dependsOn: ["T31"]
- ledgerRefs: ["goals:G1"]
- completion: "DONE (merged 9694c36). test/realhosts/provision.go: idempotent Provision (check-then-act, sentinel-guarded runStep w/ post-install re-verify) ensuring iperf3+gcc+Go 1.26.x per host (arch-detected via uname -m) + a concentrator-only `iptables -I INPUT -i wanbond0 -j ACCEPT` inserted before OCI's REJECT (-C-guarded, no dup). `just realhosts-provision` target. TestRealProvision provisions both hosts, asserts version probes + idempotent second pass (no changes) + exactly one wanbond0 ACCEPT before REJECT — RAN LIVE on o3+llm (PASS ~10-11s, both passes already-present). Reviewed opus+fable (R11 go-ahead, unanimous, both re-ran live). Filed D7 (rule not reboot-persistent -> T22) + D8 (pre-existing o3 INPUT-chain duplicates from manual P0 bring-up)."
- sessionLogs: [".cq/logs/20260706-222500-a272f5360504eaf37.md",".cq/logs/20260706-222500-a2fa37e32b9886c2c.md",".cq/logs/20260706-222500-a097aa48cda8e782e.md"]
- resultCommit: 9694c36

### T33 — done

- createdAt: 2026-07-06T21:43:47.637Z
- updatedAt: 2026-07-06T23:01:17.104Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: "Real-host P0 single-uplink smoke: handshake, ping, iperf3 single/8-parallel/UDP"
- description: "First real tunnel test in the realhosts tier, re-validating the P0 pass-through (T8) + baseline (T9) over the REAL internet: deploy wanbond to both hosts, concentrator listening on the public IP, NAT'd edge initiates (real CGNAT traversal — concentrator learns the NAT'd endpoint); verify WG handshake, ping the peer inner address recording RTT (~29ms observed), then iperf3 through the tunnel in three modes: single-flow TCP, 8x-parallel TCP, UDP (goodput + loss). Record all measurements (+ raw iperf3 JSON) to a results artifact for comparison against the session-measured ~150-170 Mbit/s tunnel figures. Clean up remote processes on exit incl. on failure."
- acceptance: "`just realhosts TestRealP0Smoke` executes end-to-end against the default hosts: handshake completes, ping RTT recorded, and all three iperf3 measurements (single TCP, 8x-parallel TCP, UDP goodput/loss) recorded. Per Q12 the acceptance is that the run executes and records results — no throughput threshold gates it and it blocks NO phase task (T8/T9) or milestone (M4)."
- suggestedModel: standard
- dependsOn: ["T32","T8","T9"]
- ledgerRefs: ["goals:G1","tasks:T8","tasks:T9"]

### T34 — done

- createdAt: 2026-07-06T21:44:02.745Z
- updatedAt: 2026-07-07T18:44:58.311Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: Real-host multipath/failover validation via virtual interfaces + policy routing
- description: "Give the NAT'd edge host TWO paths to the ONE concentrator over its single physical uplink: two virtual interfaces (macvlan or secondary addresses) with distinct source IPs + `ip rule`/policy routing, so wanbond's two configured paths use distinct 4-tuples through the NAT (concentrator sees two real per-path endpoints behind one virtual endpoint). Run FUNCTIONAL bonding/failover validation over the real internet once the P1 multipath stack lands (validates T12 multipath Bind, T15 active-backup scheduler, T20 failover e2e): both paths establish probes/handshake, traffic observed on both (telemetry recorded), and blackholing the active path (drop its ip rule / down its interface) mid-flow keeps a long-lived TCP transfer alive; record failover timing. Truly-independent asymmetric/intermittent physical links are EXPLICITLY OUT OF SCOPE (final real-hardware step, later)."
- acceptance: "`just realhosts TestRealMultipathFailover` executes against a wanbond build with P1 multipath support: two distinct-source-IP paths establish, traffic observed on both (telemetry recorded), and after disabling the active path the in-flight TCP session survives with failover time recorded. Report-only per Q12: executing and recording IS the acceptance; it never gates P1/M5 completion or archiving."
- suggestedModel: frontier
- dependsOn: ["T33","T12","T15","T20"]
- ledgerRefs: ["goals:G1","tasks:T12","tasks:T15","tasks:T20","reviews:R28"]

### T35 — done

- createdAt: 2026-07-06T21:44:12.747Z
- updatedAt: 2026-07-06T22:20:00.486Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: Extend netns fixture with per-path bandwidth cap and controlled-loss knobs
- description: "Extend test/e2e/netns.go (netns `e2e` tag): pathSpec gains an optional per-path bandwidth cap (netem rate, or a tbf/htb bottleneck qdisc on the edge veth) AND a controlled-loss knob (netem loss X%), both optional so existing P0/P1 tests run unchanged on the uncapped/lossless default topology. Size caps so the LINK, not the single-core userspace-WG crypto, is the bottleneck (P0 measured throughput CPU-bound; a cap well below the CPU-bound rate leaves headroom) — this lets a standing queue form (bufferbloat/pacing, T21/T23) and known loss be injected (FEC recovery, T25/T29) at a link-bound rate. This UNIFIES and SUPERSEDES the A7/T10 drafted follow-up in docs/p0-checkpoint.md (single rate+loss knob, no duplicate/parallel follow-up filed): update that drafted-follow-up note to point at THIS task so T23's aggregation e2e and T21's empirical BDP pace-sizing run against this capped variant when P2 begins."
- acceptance: "A self-test under `just e2e-run` demonstrates both knobs operationally: (a) with a per-path cap well below the CPU-bound throughput, iperf3 through that path measures within a stated tolerance of the cap (link-bound, not CPU-bound); (b) with X% configured loss, measured UDP loss falls in the expected band around X%. Existing e2e tests still pass unmodified on the default (uncapped, lossless) topology; docs/p0-checkpoint.md's A7 drafted-follow-up note is updated to reference this task (superseded/merged, not duplicated)."
- suggestedModel: frontier
- ledgerRefs: ["goals:G1","tasks:T10","tasks:T21","tasks:T23","tasks:T25","tasks:T29"]
- completion: "DONE (merged 2a6e61d; worker cb05397 + review-fix 7383ca0). test/e2e/netns.go pathSpec gains OPTIONAL per-path rateMbit (netem rate cap) + lossPct (netem loss) knobs, both defaulting to 0 so DefaultPaths + all existing P0/P1 e2e tests run UNCHANGED (netemArgs/netemArgsWithLoss refactor byte-identical for the zero case; SetupWithPaths; InjectLoss preserves the cap). New TestFixtureImpairment (e2e) proves both knobs over raw veth links: capped-path TCP ~link-bound near 50 Mbit/s cap, lossy-path ICMP loss ~configured %. HARDWARE-VERIFIED on o3: 5/5 PASS (cap 52.9 Mbit/s; loss ~10%). docs/p0-checkpoint.md A7 note superseded/unified into T35 (single rate+loss knob, no duplicate follow-up). Reviewed opus+fable (R10 go-ahead) after fixing fable's r1 criticisms (flaky iperf3-UDP loss -> robust ping/ICMP loss; doc default mislabel). This delivers the bandwidth-capped + controlled-loss fixture that T21/T23 (bufferbloat/pacing) and T25/T29 (FEC recovery) + T36 (FEC baseline) need."
- sessionLogs: [".cq/logs/20260706-220000-aa4ce7b7518ab1cfd.md",".cq/logs/20260706-220000-a5b2b9b863a40779d.md",".cq/logs/20260706-221500-a139ea4c25eeab49c.md",".cq/logs/20260706-221500-aacca56552aa07ae9.md"]
- resultCommit: 2a6e61d

### T36 — done

- createdAt: 2026-07-06T21:44:19.623Z
- updatedAt: 2026-07-06T22:52:34.024Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: Record controlled-loss single-flow TCP collapse baseline for P3/P4 FEC comparison
- description: "Using the capped+lossy fixture (T35), quantify the long-fat-lossy-network problem FEC must fix (real-internet evidence: single-flow TCP collapsed to ~18-48 Mbit/s from ~0.1-0.8% loss over 29ms RTT, per Mathis): at a fixed bandwidth cap and RTT, sweep configured netem loss (e.g. 0 / 0.5 / 1 / 2 %) and record single-flow TCP goodput through the tunnel at each point. Persist the table (loss rate -> absolute throughput + fraction of the 0%-loss figure) in a committed doc (docs/fec-baseline.md) with the exact fixture parameters, so P3 fixed-FEC (T25) and P4 adaptive-FEC (T29) recovery e2e are measured against the SAME topology and these numbers. This baseline PRECEDES and feeds T25/T29 (it is the pre-FEC reference); it does not depend on them."
- acceptance: "`just e2e-run TestFECBaselineCollapse` executes the sweep and writes/updates docs/fec-baseline.md; the test asserts the collapse is actually observed (single-flow TCP throughput at >=1% configured loss falls materially below the 0%-loss capped figure, e.g. <50%), proving the fixture reproduces the phenomenon P3/P4 FEC recovery is later measured against."
- suggestedModel: standard
- dependsOn: ["T35"]
- ledgerRefs: ["goals:G1","tasks:T25","tasks:T29"]

## M13

### T51 — done

- createdAt: 2026-07-13T13:41:17.887Z
- updatedAt: 2026-07-13T14:26:03.397Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: Make Bind Open() tolerant of not-yet-assignable source_addr (EADDRNOTAVAIL)
- description: "internal/bind/multipath.go Open() loop (L467-516, binds each path via listenPath L479) currently returns fatally on ANY per-path bind error, tearing down bring-up. Refactor so a WELL-FORMED source_addr that is merely not-yet-assignable (net.ListenUDP -> EADDRNOTAVAIL / 'cannot assign requested address') does NOT abort: bring the tunnel up on the paths that DO bind, and mark each unbindable path DOWN by reusing the runtime path-down model (telemetry Liveness StateDown, internal/telemetry/liveness.go) so sched/weighted.go Pick already excludes it. Mirror the rollback discipline of runtime AddPath (L1336-1416). HARD GUARDS enforced here: (a) if ZERO paths bind, Open() STILL fails fatally (no transport => no tunnel); (b) a MALFORMED source_addr stays a hard config-load error (config.validate L579-644 already rejects it -- do not move that check); (c) distinguish EADDRNOTAVAIL from other bind errors (EADDRINUSE / permission) which remain fatal. Detect EADDRNOTAVAIL via errors.Is on the syscall errno, not string matching. Behavior change => update docs/design.md startup/bind section + docs/manual-checklist.md."
- acceptance: "New unit test in internal/bind exercises Open() with (i) one bindable + one EADDRNOTAVAIL path -> Open succeeds, unbindable path present and marked Down; (ii) all paths EADDRNOTAVAIL -> Open returns error; (iii) EADDRINUSE -> Open returns error. `go build ./... && go vet ./... && gofmt -l internal/bind` clean; `go test ./internal/bind/...` passes. Doc-sync: docs/design.md + docs/manual-checklist.md updated to describe tolerant startup bind."
- suggestedModel: frontier
- ledgerRefs: ["goals:G2"]

### T55 — done

- createdAt: 2026-07-13T13:41:52.923Z
- updatedAt: 2026-07-13T14:45:32.641Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: "Background reconcile: retry binding deferred-DOWN paths as their addresses appear"
- description: Add a background reconciler that retries listenPath for paths left DOWN by the tolerant Open() (T51) once their well-formed source_addr becomes assignable. Planner's call between event-driven (subscribe to netlink route/addr updates, e.g. via vishvananda/netlink AddrSubscribe) and a bounded poll; prefer netlink if it stays within existing deps, else a bounded-interval poll with backoff. On successful (re)bind, promote the path using the SAME runtime path-up transition as AddPath/liveness (StateUp after UpAfterSuccesses) so the scheduler picks it up uniformly; on continued EADDRNOTAVAIL, stay DOWN and retry. Must not busy-loop, must stop cleanly on Close(), and must not touch paths that bound at startup. Behavior change => update docs/design.md startup/reconcile section.
- acceptance: "Unit test: a path that starts DOWN (EADDRNOTAVAIL) transitions Up once a fake bind succeeds, exercised via an injected listen/clock seam; reconciler terminates on Close with no goroutine leak (go test -race). `go build ./... && go vet ./... && gofmt -l internal/bind` clean; `go test -race ./internal/bind/...` passes. Doc-sync: docs/design.md documents background path reconcile."
- suggestedModel: frontier
- ledgerRefs: ["goals:G2"]
- dependsOn: ["T51"]

### T60 — done

- createdAt: 2026-07-13T13:42:43.548Z
- updatedAt: 2026-07-13T15:04:59.410Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: "netns e2e: absent-then-added source_addr; zero-bindable fatal; malformed config-load error; no T16 regression"
- description: "Add a netns e2e (test/e2e, netns.go SetupWithPaths + tc/netem harness) validating the tolerant-startup feature (T51) + reconcile (T55): (1) start with a path whose source_addr's interface/address is ABSENT -> assert the tunnel comes up on the SURVIVOR path and carries traffic, the absent path is DOWN; (2) then ADD the missing address -> assert the deferred path is reconciled/joined and now carries traffic; (3) separately, ZERO bindable paths -> daemon exits fatally (no crash-loop-masking); (4) MALFORMED source_addr -> config-load error (fails before bind). Also assert the existing T16 device-bind / re-roam + source_addr-pin e2e still passes (no regression)."
- acceptance: "`just e2e` (sudo netns) runs the new test: survivor-up-then-deferred-join passes; zero-bindable exits non-zero; malformed is rejected at config load; the pre-existing T16 e2e still passes in the same run. Test is deterministic across repeats."
- suggestedModel: standard
- ledgerRefs: ["goals:G2"]
- dependsOn: ["T51","T55"]

## M15

### T54 — done

- createdAt: 2026-07-13T13:41:37.377Z
- updatedAt: 2026-07-13T15:20:07.621Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: "Config: edge-side ordered list of concentrator (peer) endpoints"
- description: "Extend config (internal/config/config.go -- today a single defaultRemote via ParseEndpoint ~L1244) to hold an ORDERED list of concentrator/peer endpoints for the edge, first = active, rest = standby, per Q18 (edge-side ordered-endpoint active-standby). Parse + validate the list (each a well-formed host:port; reject empty/duplicate; a single-entry list preserves today's behavior exactly). No hub-to-hub state; the list is purely edge-side selection order. This task is config surface only -- the failover switch logic is the hub-loss-detection task. Config change => update docs/install.md config reference + docs/design.md concentrator section."
- acceptance: "Unit test: multi-endpoint list parses in order; single-entry list is behavior-identical to the old single defaultRemote; empty/duplicate rejected by validate. `go build ./... && go vet ./... && gofmt -l internal/config` clean; `go test ./internal/config/...` passes. Doc-sync: docs/install.md + docs/design.md document the ordered concentrator-endpoint list."
- suggestedModel: standard
- ledgerRefs: ["goals:G2"]

### T57 — done

- createdAt: 2026-07-13T13:42:10.041Z
- updatedAt: 2026-07-13T15:45:31.768Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: Hub-loss detection + switch peer remote + WG re-handshake to next concentrator
- description: "Implement edge-side active-standby hub failover (Q18). Using the PROBE/liveness plane, detect HUB LOSS = ALL paths to the ACTIVE concentrator endpoint DOWN (distinct from single-path loss, which existing per-path failover already handles). On hub loss, advance to the next endpoint in the ordered list (T54), switch the peer remote (WireGuard endpoint) for every path to point at the standby concentrator, and trigger a WG re-handshake (fresh session; NO hub-to-hub state handoff). Re-arm detection against the new active endpoint; if it also fails, advance again (wrap/stop per config). Shares internal/bind/multipath.go with the startup-resilience change, so it is sequenced after T51 to avoid conflicting edits to the bind/path model. Guard: a single-endpoint list must behave exactly as today (no failover path taken). Behavior change => update docs/design.md concentrator-failover section + docs/manual-checklist.md."
- acceptance: "Unit/component test: with a 2-endpoint list, forcing all paths to endpoint#1 DOWN switches the peer remote to endpoint#2 and initiates a re-handshake; single-endpoint list takes no failover action; detection re-arms on the new endpoint. `go build ./... && go vet ./... && gofmt -l internal/bind internal/config` clean; `go test ./internal/...` passes. Doc-sync: docs/design.md + docs/manual-checklist.md describe hub failover."
- suggestedModel: frontier
- ledgerRefs: ["goals:G2"]
- dependsOn: ["T54","T51"]

### T62 — done

- createdAt: 2026-07-13T13:42:58.597Z
- updatedAt: 2026-07-13T17:09:12.144Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: "netns e2e: hub failover switches to standby concentrator and re-establishes the tunnel"
- description: "Add a netns e2e (test/e2e) for Q18 hub failover (T57): stand up TWO concentrator endpoints and an edge with an ordered 2-endpoint list; establish the tunnel to endpoint#1; then force ALL paths to endpoint#1 DOWN (tc/netem drop or address removal at the hub side) and assert the edge switches its peer remote to endpoint#2, completes a WG re-handshake, and resumes carrying traffic through the standby. Assert a single-endpoint list takes NO failover action (control case). Deterministic."
- acceptance: "`just e2e` (sudo netns) runs the hub-failover test: after all-paths-to-hub#1 down, traffic resumes via hub#2 within the liveness/failover window; single-endpoint control case shows no switch. Passes on repeat."
- suggestedModel: standard
- ledgerRefs: ["goals:G2"]
- dependsOn: ["T57"]

## M20

### T67 — done

- createdAt: 2026-07-13T21:54:04.076Z
- updatedAt: 2026-07-13T22:56:09.596Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Accept hostname endpoints in config behind an explicit per-peer DNS opt-in
- description: "Overload the existing `endpoint`/`endpoints` fields (Q35): in Peer.resolveEndpoints (internal/config/config.go:484) parse each entry first with netip.ParseAddrPort; on failure, split host:port and treat as a hostname (validate port range and hostname syntax). Introduce a typed per-entry representation — e.g. `EndpointSpec { Host string; Port uint16; Addr netip.AddrPort; IsName bool }` — stored in order on Peer (e.g. `Peer.EndpointSpecs`), while `Peer.Endpoints []netip.AddrPort` keeps holding the resolved/literal snapshot T57 consumes. Add an explicit per-peer opt-in flag (e.g. `dns = true`, greppable): a hostname entry without the flag is a clear validation error naming the flag (Q29 default-off DPI posture); the flag is edge-only (a concentrator declaring it is a config error, mirroring the existing endpoints rule). CRITICAL invariant: an all-IP-literal config MUST take exactly today's code path — netip.ParseAddrPort per entry, same errors, same duplicate detection, byte-for-byte behavior-identical (Q29). Duplicate detection extends to hostname entries (same host:port twice rejected). No resolution happens at config load (Q30 defers it to runtime)."
- acceptance: "go test ./internal/config/... passes with new cases: (1) hostname entry + dns=true parses into an EndpointSpec with IsName=true; (2) hostname entry without dns=true fails Load with an error naming the opt-in flag; (3) mixed list of literals and names preserves order; (4) duplicate host:port rejected; (5) concentrator with dns=true rejected; (6) every pre-existing config test passes unchanged and an all-literal config populates Peer.Endpoints exactly as before."
- suggestedModel: standard
- ledgerRefs: ["goals:G5"]
- resultCommit: c6f9235b8381b3db1bfa3b2aed0a7d538bcac0f8
- completion: "Hostname endpoints accepted behind per-peer dns=true opt-in: EndpointSpec typed parse + Peer.EndpointSpecs/Peer.DNS; all-literal path byte-identical; 6 acceptance cases; merged ff to main."
- sessionLogs: [".cq/logs/20260713-224948-a89a978a513c962f4.md",".cq/logs/20260713-225437-a7e364965f1c96b59.md",".cq/logs/20260713-225437-a11bd428159e547c9.md"]
- rawLogs: [".cq/logs/raw/20260713-224948-a89a978a513c962f4.jsonl",".cq/logs/raw/20260713-225437-a7e364965f1c96b59.jsonl",".cq/logs/raw/20260713-225437-a11bd428159e547c9.jsonl"]

### T68 — done

- createdAt: 2026-07-13T21:54:10.871Z
- updatedAt: 2026-07-13T23:03:09.963Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Create the resolver seam package with a system-resolver implementation
- description: "New package (e.g. internal/dnsresolve): a small `Resolver` interface, context-bounded, returning the full A+AAAA record set plus a minimum TTL when the transport exposes it — e.g. `Lookup(ctx, host) (addrs []netip.Addr, minTTL time.Duration, ttlOk bool, err error)` (ttlOk=false when the transport discards TTL). Provide the system-resolver implementation over net.Resolver (net.LookupNetIP-shape; TTL not exposed — return ttlOk=false, Q31 makes TTL a nice-to-have). The seam is the injection point every runtime and test consumer uses (Q33: designed so DoH/DoT drop in; Q36: unit tests inject a fake). Resolution ordering: return records deterministically (v4 and v6 both; consumers filter/order by local path family). Keep the package free of any device/bind dependency so it is import-cycle-safe from config, device, and tests. Provide a hand-written in-memory fake Resolver backed by a static host->addrs map (dual-tests dummy) for unit use across packages."
- acceptance: "go test ./internal/dnsresolve/... passes: the fake Resolver satisfies the interface (compile-time var _ Resolver assertion) and resolves a mapped name to the expected ordered addrs while an unmapped name returns a non-nil error; the system implementation resolves localhost to loopback addrs in a hermetic test; context cancellation propagates (lookup returns promptly with ctx.Err())."
- suggestedModel: standard
- ledgerRefs: ["goals:G5"]
- resultCommit: 9f8e13d
- completion: "internal/dnsresolve resolver seam landed: context-bounded Resolver interface (addrs, minTTL, ttlOk), SystemResolver over net.Resolver, FakeResolver dual-tests dummy; README + design.md package inventories synced (round-2 criticism fix); merged ff to main."
- sessionLogs: [".cq/logs/20260713-224948-acf8c70e1855d17a7.md",".cq/logs/20260713-230051-a5fe60c169d59c898.md",".cq/logs/20260713-225437-ae9e21e85de4600a9.md",".cq/logs/20260713-225437-a99ae9caf87cc11a3.md",".cq/logs/20260713-230228-a0fb43fc933a1f307.md",".cq/logs/20260713-230228-a50626c69a5974410.md"]
- rawLogs: [".cq/logs/raw/20260713-224948-acf8c70e1855d17a7.jsonl",".cq/logs/raw/20260713-230051-a5fe60c169d59c898.jsonl",".cq/logs/raw/20260713-225437-ae9e21e85de4600a9.jsonl",".cq/logs/raw/20260713-225437-a99ae9caf87cc11a3.jsonl",".cq/logs/raw/20260713-230228-a0fb43fc933a1f307.jsonl",".cq/logs/raw/20260713-230228-a50626c69a5974410.jsonl"]

### T69 — done

- createdAt: 2026-07-13T21:54:23.234Z
- updatedAt: 2026-07-13T23:28:28.113Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Implement a DoH (RFC 8484) resolver behind the seam
- description: "First-class private-resolver option (Q33 answer). Implement DNS-over-HTTPS in internal/dnsresolve: encode A and AAAA queries with golang.org/x/net/dns/dnsmessage, POST (application/dns-message) to the configured DoH URL over net/http with a dedicated http.Client (bounded timeout, no proxy surprise, HTTP/2 ok), parse answers, extract per-record TTL and return minTTL with ttlOk=true. Certificate trust: standard system roots by default plus an injectable root-CA pool ONLY via an unexported constructor seam for tests — no production insecure-skip knob. Document (in the package) the residual leak: TLS SNI/timing to the DoH provider. Query both families; tolerate one family NXDOMAIN when the other answers."
- acceptance: "go test ./internal/dnsresolve/... passes: a hermetic httptest.NewTLSServer DoH responder (dnsmessage-encoded) yields the expected A+AAAA set and minTTL; a malformed response, non-200, and timeout each surface a typed error; the test CA is injected via the test-only seam (no InsecureSkipVerify anywhere in the package)."
- suggestedModel: standard
- dependsOn: ["T68"]
- ledgerRefs: ["goals:G5"]
- resultCommit: 9323a16
- completion: "DoHResolver (RFC 8484) landed behind the seam: dnsmessage A/AAAA POSTs, dedicated http.Client, minTTL+ttlOk, typed errors (NoDataError/NXDomain/Malformed/Status/Timeout), io.LimitReader response cap, test-only CA seam (no InsecureSkipVerify); 2 review rounds; merged ff to main."
- sessionLogs: [".cq/logs/20260713-231437-a07657609e4ceabbd.md",".cq/logs/20260713-232253-a4f0126c89564ecfd.md",".cq/logs/20260713-231830-a5122d18a9a011585.md",".cq/logs/20260713-231830-a15aa232e07b17d44.md",".cq/logs/20260713-232716-a256ad0c7fae40b3a.md",".cq/logs/20260713-232716-a73363906e9351cad.md"]
- rawLogs: [".cq/logs/raw/20260713-231437-a07657609e4ceabbd.jsonl",".cq/logs/raw/20260713-232253-a4f0126c89564ecfd.jsonl",".cq/logs/raw/20260713-231830-a5122d18a9a011585.jsonl",".cq/logs/raw/20260713-231830-a15aa232e07b17d44.jsonl",".cq/logs/raw/20260713-232716-a256ad0c7fae40b3a.jsonl",".cq/logs/raw/20260713-232716-a73363906e9351cad.jsonl"]

### T71 — done

- createdAt: 2026-07-13T21:54:44.129Z
- updatedAt: 2026-07-13T23:41:21.379Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Implement a DoT (RFC 7858) resolver behind the seam
- description: "Second private-resolver option (Q33 answer). Implement DNS-over-TLS in internal/dnsresolve: dial the configured server:853 with crypto/tls (server-name verification, system roots + the same injectable test-only CA seam as DoH), frame dnsmessage-encoded A/AAAA queries with the RFC 7858 2-byte length prefix, parse answers with per-record TTL (ttlOk=true). Reuse the shared dnsmessage encode/decode helpers introduced by the DoH task (extract them if needed — DRY across the two transports). Bounded per-lookup timeout via context; one connection per lookup is acceptable for v1 (lookups are seconds-cadence, not hot-path)."
- acceptance: "go test ./internal/dnsresolve/... passes: a hermetic in-process TLS listener speaking length-prefixed DNS answers the query and the resolver returns the expected addrs+minTTL; a wrong-server-name cert fails verification; timeout and truncated-frame paths surface typed errors."
- suggestedModel: standard
- dependsOn: ["T69"]
- ledgerRefs: ["goals:G5"]
- resultCommit: c331261
- completion: "DoTResolver (RFC 7858) landed behind the seam: tls :853 with server-name verification + test-CA seam, 2-byte length-prefix framing, addrs+minTTL (ttlOk=true), typed errors; shared dnscodec.go extracted from DoH (DRY); merged ff to main."
- sessionLogs: [".cq/logs/20260713-234055-a4b1141d2bedfcafe.md",".cq/logs/20260713-234055-af856789b6ff0a960.md",".cq/logs/20260713-234055-a0b4991b5647ea04b.md"]
- rawLogs: [".cq/logs/raw/20260713-234055-a4b1141d2bedfcafe.jsonl",".cq/logs/raw/20260713-234055-af856789b6ff0a960.jsonl",".cq/logs/raw/20260713-234055-a0b4991b5647ea04b.jsonl"]

### T72 — done

- createdAt: 2026-07-13T21:54:56.902Z
- updatedAt: 2026-07-14T00:32:42.211Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: "Add the [dns] config block selecting resolver mode, cadence, and timeouts"
- description: "New top-level `[dns]` block in internal/config: `resolver = \"system\" | \"doh\" | \"dot\"` (default system), `doh_url` (required iff doh), `dot_server` (host:port, required iff dot), `poll_interval` (re-resolution cadence, duration string, sane default on the reconcile-cadence scale per Q31, validated > 0), `timeout` (per-lookup bound, default a few seconds). BOOTSTRAP-IP invariant (chicken-and-egg): the DoH/DoT server address must itself be reachable WITHOUT a DNS lookup — require the doh_url host / dot_server to be an IP literal, or require an explicit `bootstrap_ip` field when a hostname is used; validation FAILS FAST otherwise (a plaintext lookup of the private resolver's own name would defeat Q33's purpose). Validation: mode-specific required fields; reject DoH/DoT settings when resolver=system; an absent block is inert (system defaults) — the per-peer opt-in flag remains the sole gate, [dns] merely selects transport. Provide a constructor mapping the validated block onto the internal/dnsresolve implementations. Zero-value behavior: absent block == system resolver defaults, still gated by the per-peer flag."
- acceptance: "go test ./internal/config/... passes a validation matrix: absent block yields system defaults; doh without doh_url fails; dot without dot_server fails; a hostname-form doh_url/dot_server without bootstrap_ip fails fast with a clear error; poll_interval <= 0 fails; a full valid doh and dot block each construct the matching dnsresolve implementation in a unit test."
- suggestedModel: standard
- dependsOn: ["T67","T71"]
- ledgerRefs: ["goals:G5"]
- resultCommit: d0b28ab
- completion: "[dns] config block landed over 3 review rounds: resolver mode system/doh/dot, duration-string poll_interval/timeout (LinkRTTRaw pattern), bootstrap_ip wired into the DoH/DoT dial target (WithBootstrap constructors) with fail-fast rejection under IP-literal hosts, full validation matrix, docs/example synced; merged ff to main."
- sessionLogs: [".cq/logs/20260714-001002-adff709a56135e73f.md",".cq/logs/20260714-002323-a4c50f2ae82785a2f.md",".cq/logs/20260714-003019-a31a3fb1eb6004083.md",".cq/logs/20260714-001524-ab3c6a032f4c6441f.md",".cq/logs/20260714-001524-a3e48d89773e283be.md",".cq/logs/20260714-003019-a7c2512ac55807582.md",".cq/logs/20260714-003019-a7ba4d8dc5a6d3714.md",".cq/logs/20260714-003215-a51f0c5a70ae87666.md",".cq/logs/20260714-003215-a66e35c5e28239c28.md"]
- rawLogs: [".cq/logs/raw/20260714-001002-adff709a56135e73f.jsonl",".cq/logs/raw/20260714-002323-a4c50f2ae82785a2f.jsonl",".cq/logs/raw/20260714-003019-a31a3fb1eb6004083.jsonl",".cq/logs/raw/20260714-001524-ab3c6a032f4c6441f.jsonl",".cq/logs/raw/20260714-001524-a3e48d89773e283be.jsonl",".cq/logs/raw/20260714-003019-a7c2512ac55807582.jsonl",".cq/logs/raw/20260714-003019-a7ba4d8dc5a6d3714.jsonl",".cq/logs/raw/20260714-003215-a51f0c5a70ae87666.jsonl",".cq/logs/raw/20260714-003215-a66e35c5e28239c28.jsonl"]

## M21

### T70 — done

- createdAt: 2026-07-13T21:54:32.763Z
- updatedAt: 2026-07-14T01:02:46.864Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Refactor hubFailover to a mutable, spec-keyed endpoint set updated under its lock
- description: "Q34 answer: allow mixing hostnames and IP literals in the ordered endpoints list; make hubFailover's endpoint list MUTABLE with re-resolution updating entries in place under the shared lock. Refactor internal/device/failover.go: replace the immutable `endpoints []netip.AddrPort` snapshot (failover.go:72-97) with a spec-keyed structure — the ordered []config.EndpointSpec where each spec owns its current expansion ([]netip.AddrPort: a literal is a fixed single entry; a hostname is its latest resolved record set per Q32 multi-record expansion, family-filtered/ordered against the local paths' bind families with a documented deterministic tie-break). The flattened concatenation is the failover order; `idx` addresses the flattened list. Track the ACTIVE endpoint by SPEC-SCOPED identity — the pair (specIdx, AddrPort) — never a raw flattened index and never a bare AddrPort value (R70 fix): T67's load-time duplicate detection is textual host:port only, so at runtime a hostname may legitimately re-resolve onto the same AddrPort as another spec's literal or record, yielding duplicate values in the flattened order, and a bare value-based re-map could silently match the wrong spec's entry. Duplicate AddrPort values across DIFFERENT specs are therefore permitted in the flattened list (check() walks it unchanged); re-mapping after an in-place swap resolves the active pointer WITHIN its owning spec (its AddrPort survived that spec's swap → same entry, flattened idx re-computed), and only a change in the active spec's OWN expansion can move or repoint it. Add an update method, e.g. `updateResolution(specIdx int, addrs []netip.AddrPort)`, taking h.mu: it swaps that spec's expansion; the ACTIVE entry stays stable if its AddrPort survives the swap within that spec (re-map idx); if the active entry's AddrPort changed, repoint via ONE SetPeerRemote (multipath.go:1371, disruptive — Rebaseline + rehandshake per D32) and rehandshake; if unchanged, strictly no repoint (Q31 no-op suppression); standby-only changes never touch the bond. check() (failover.go:136) keeps its exact semantics over the flattened list. Also update the startHubFailover wiring (failover.go:205): construct a controller when the peer has ANY hostname spec OR >= 2 flattened endpoints; a single-IP-literal peer still constructs NO controller (byte-for-byte pre-G5 behavior, Q29). Do NOT wire the resolver here — this task only makes the set mutable and exposes the update API; keep every existing failover unit test passing."
- acceptance: "go test ./internal/device/... -race passes: all existing failover_test.go tests unchanged; new fake-clock/fake-health/fake-remote cases prove (1) a standby-record swap causes zero SetPeerRemote calls; (2) an active-entry IP change causes exactly one SetPeerRemote + one rehandshake; (3) an unchanged active IP causes zero calls; (4) idx re-maps correctly when a spec's expansion grows/shrinks; (5) a single-IP-literal peer constructs no controller; (6) a hostname spec re-resolving onto the SAME AddrPort as another spec's literal standby leaves the active pointer on its own spec's entry (no spurious re-map, zero SetPeerRemote) and a subsequent failover advance still walks the flattened order correctly (R70 cross-spec duplicate case)."
- suggestedModel: frontier
- dependsOn: ["T67"]
- ledgerRefs: ["goals:G5"]
- resultCommit: 1fc6042
- completion: "hubFailover refactored to a mutable spec-keyed endpoint set: []failoverSpec with per-spec expansions, (specIdx,AddrPort) active identity, updateResolution with D32 no-op suppression + boot adoption + dwell re-arm, peerNeedsHubFailover gating; 8 new acceptance tests incl. R70 cross-spec duplicate; 2 review rounds; merged ff to main. D45/D46 filed for follow-ups."
- sessionLogs: [".cq/logs/20260714-004559-a5e1fb1cab9a92f66.md",".cq/logs/20260714-005639-a5279f6a07039286a.md",".cq/logs/20260714-005639-a4a5512b111f43aa0.md",".cq/logs/20260714-005639-a5c7bef556145b214.md",".cq/logs/20260714-010221-a67a4e5aa186a68a1.md",".cq/logs/20260714-010221-a7f9ccd45b1752e7f.md"]
- rawLogs: [".cq/logs/raw/20260714-004559-a5e1fb1cab9a92f66.jsonl",".cq/logs/raw/20260714-005639-a5279f6a07039286a.jsonl",".cq/logs/raw/20260714-005639-a4a5512b111f43aa0.jsonl",".cq/logs/raw/20260714-005639-a5c7bef556145b214.jsonl",".cq/logs/raw/20260714-010221-a67a4e5aa186a68a1.jsonl",".cq/logs/raw/20260714-010221-a7f9ccd45b1752e7f.jsonl"]

### T73 — done

- createdAt: 2026-07-13T21:55:14.470Z
- updatedAt: 2026-07-14T01:43:10.282Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: "Implement the re-resolution controller: poll plus liveness-loss triggers, change-suppressed repoint"
- description: "Q31 answer: fixed poll interval PLUS an immediate re-resolve on liveness loss; repoint only on actual change (suppression lives in T70's update method). New controller in internal/device (e.g. resolution.go), mirroring the hubFailover shape: pure constructor over injected collaborators (dnsresolve.Resolver, the mutable endpoint set's update API, the same hubHealth liveness plane, telemetry.Clock, poll interval from [dns]) so it is unit-testable with a fake resolver and fake clock (Q36). One evaluation step: for each hostname spec, Lookup with the configured timeout; on SUCCESS, family-filter/order and call updateResolution (change detection inside); on FAILURE, keep the previous expansion and retry next tick — a lookup failure NEVER tears down a working endpoint set and never hard-fails anything (Q30 tolerant model). Liveness-loss trigger: when every path to the ACTIVE endpoint is DOWN (same allDown sweep the failover controller uses), trigger an immediate out-of-band re-resolve of the active spec rather than waiting for the next tick; coordinate with hubFailover purely through the shared lock and the update API (Q34) so the two controllers cannot fight over the bond (each repoint is a single guarded SetPeerRemote). Resolution runs entirely off the send hot path (its own goroutine; results applied under the endpoint-set lock only). If minTTL is available (DoH/DoT, ttlOk=true), clamp the next poll to min(pollInterval, TTL) — the Q31 TTL nice-to-have, trivially available behind the seam. This controller runs even for a single-hostname peer (to track a changing IP), independent of hub-failover's >=2 guard."
- acceptance: "go test ./internal/device/... -race passes with injected resolver + fake clock: (1) lookup failure at every tick leaves the endpoint set untouched and keeps retrying (no hard fail); (2) a changed active IP produces exactly one SetPeerRemote (observed via fake remote); (3) an unchanged IP produces none over many ticks; (4) all-paths-down triggers a re-resolve before the next poll tick; (5) TTL below poll interval shortens the next resolve delay."
- suggestedModel: frontier
- dependsOn: ["T70","T72"]
- ledgerRefs: ["goals:G5"]
- resultCommit: 0d36a23
- completion: "Re-resolution controller landed (internal/device/resolution.go): poll + edge-triggered liveness-loss out-of-band re-resolve, family-filtered/ordered records via updateResolution (D32 suppression), TTL clamp on both re-arms, never-publish-empty retention (resolves D46); wired into startHubFailover; 2 review rounds; merged ff to main."
- sessionLogs: [".cq/logs/20260714-011407-ac373e4ef09d0a464.md",".cq/logs/20260714-013526-adf90d11d594f0391.md",".cq/logs/20260714-013526-aa5af95a3b03fde16.md",".cq/logs/20260714-013526-a655a1c595ce4648a.md",".cq/logs/20260714-014014-a6681895aad44fda0.md",".cq/logs/20260714-014014-ad33623fb263f9dc1.md"]
- rawLogs: [".cq/logs/raw/20260714-011407-ac373e4ef09d0a464.jsonl",".cq/logs/raw/20260714-013526-adf90d11d594f0391.jsonl",".cq/logs/raw/20260714-013526-aa5af95a3b03fde16.jsonl",".cq/logs/raw/20260714-013526-a655a1c595ce4648a.jsonl",".cq/logs/raw/20260714-014014-a6681895aad44fda0.jsonl",".cq/logs/raw/20260714-014014-ad33623fb263f9dc1.jsonl"]

### T74 — done

- createdAt: 2026-07-13T21:55:31.799Z
- updatedAt: 2026-07-14T02:46:57.215Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Wire deferred boot-time resolution and the resolution loop into the device lifecycle
- description: "Q30 answer: defer-and-reconcile, never hard-fail boot on an unresolvable name. In internal/device/device.go: (1) at Up, attempt one bounded initial resolve of each hostname spec (short timeout); build the engine/UAPI peer endpoint ONLY from a resolved flattened list — if NOTHING is resolved (single-hostname peer, resolver down), bring the tunnel up WITHOUT a peer endpoint (the concentrator side already runs endpoint-less, so the engine supports it) and let the loop complete the wiring on first success. FIRST-RESOLVE INSTALL PATH (R70 fix): SetPeerRemote (multipath.go:1371) only repoints bind path remotes — it NEVER populates the engine peer's endpoint, which is set exclusively by a UAPI `endpoint=` line routed through Multipath.ParseEndpoint (multipath.go:1324-1344); after an endpoint-less boot a bare rehandshake has no known endpoint, so SendHandshakeInitiation cannot transmit. Therefore on the FIRST successful resolve the device must (a) INSTALL the resolved endpoint on the engine peer via the UAPI/IpcSet path (an `endpoint=` line through the existing IpcSet machinery, or an equivalent device-level install that reaches ParseEndpoint with a resolved IP:port string), THEN (b) rehandshake — the initiation now has an addressable endpoint. Subsequent re-resolves of an already-installed peer take the normal SetPeerRemote repoint path (the engine's virtual endpoint stays stable per A1; only bind remotes move). (2) Start the re-resolution controller's loop like startHubFailover (device.go wiring at failover.go:205 / stopHubFailover at device.go:677): a device-lifecycle goroutine with an idempotent stopper stored on Tunnel (e.g. t.stopResolution) invoked in Close between stopHubFailover and dev.Close. (3) Multipath.ParseEndpoint (multipath.go:1327) stays IP-only — the device hands ONLY resolved netip.AddrPort strings to the engine, so no hostname ever reaches the bind and the datapath is untouched. (4) When DNS is not configured (no hostname specs), construct NO resolver and start NO loop — the wiring is provably inert for existing configs (Q29). Update Reload semantics only as far as correctness requires (a reload that changes endpoint specs restarts the loop); note anything larger as follow-up rather than expanding scope."
- acceptance: "go test ./internal/device/... -race passes: (1) Up succeeds with a never-resolving fake resolver on a single-hostname peer (tolerant boot; UAPI get shows NO peer endpoint); (2) first successful resolve INSTALLS the engine peer endpoint and initiates a handshake — assert BOTH that the engine peer gains the endpoint (UAPI get reports endpoint=<resolved ip:port>) AND that a handshake initiation actually egresses toward the resolved address (initiation packet observed at the test bind); a fake-rehandshake-counter increment alone is NOT sufficient evidence (R70); (3) Close stops the loop with no goroutine leak under -race; (4) a config with zero hostname specs constructs no resolver and starts no loop (asserted via the wiring seam). Full go test ./... and go vet ./... pass."
- suggestedModel: frontier
- dependsOn: ["T73"]
- ledgerRefs: ["goals:G5"]
- resultCommit: 6ceee83
- completion: "Boot-time DNS resolution lifecycle wired into internal/device: bounded initial resolve at Up builds the engine/UAPI endpoint only from resolved entries (endpoint-less tolerant boot when nothing resolves); the R70 first-resolve INSTALL path (deviceInstallEndpoint via UAPI endpoint= then rehandshake — SetPeerRemote never sets the engine peer endpoint) is wired as a REQUIRED newHubFailoverFromSpecs collaborator (no silent fallback); separate Tunnel.stopResolution in Close (leak-free under goleak); resolver built only when a hostname spec exists (zero-hostname inertness); injectable up() seam. Acceptance verified via go test ./internal/device/... -race incl. an up()-driven production-wiring test that mutation-fails if the install line is lost. 2 review rounds; rebased past T89 and ff-merged to main as 6ceee83; docs/design.md + docs/install.md synced."
- sessionLogs: [".cq/logs/20260714-022249-afa803909d4c484b6.md",".cq/logs/20260714-023944-a9fac7819e0cf8c3d.md",".cq/logs/20260714-023944-ac7d0d623c6361bff.md",".cq/logs/20260714-023944-a9e9d35db0d94988e.md",".cq/logs/20260714-024545-a417b8e13ab58fa54.md",".cq/logs/20260714-024545-a534c16da5521baae.md"]
- rawLogs: [".cq/logs/raw/20260714-022249-afa803909d4c484b6.jsonl",".cq/logs/raw/20260714-023944-a9fac7819e0cf8c3d.jsonl",".cq/logs/raw/20260714-023944-ac7d0d623c6361bff.jsonl",".cq/logs/raw/20260714-023944-a9e9d35db0d94988e.jsonl",".cq/logs/raw/20260714-024545-a417b8e13ab58fa54.jsonl",".cq/logs/raw/20260714-024545-a534c16da5521baae.jsonl"]

### T75 — done

- createdAt: 2026-07-13T21:55:46.889Z
- updatedAt: 2026-07-14T03:14:04.069Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: "Add cross-controller -race interleave tests: re-resolution vs hub-failover coordination"
- description: "Consolidated concurrency proof for the two controllers that co-own the mutable endpoint set (Q34): a test that INTERLEAVES re-resolution updates (updateResolution swapping expansions / repointing the active entry) with hubFailover.check() advances (all-paths-down failover) under `go test -race`, driven by the injected fake resolver, fake clock, and fake health/remote collaborators. Cover the contested schedules: a re-resolve landing while check() is mid-advance; a failover advance landing between a lookup and its updateResolution apply; and both controllers observing the same liveness-loss event (exactly ONE SetPeerRemote must win — no double-repoint, no lost update, no deadlock on h.mu). Also consolidate the seam-contract unit coverage Q36 names in one place: resolveEndpoints/boot defers (never hard-fails) on lookup failure; the loop repoints via SetPeerRemote only on a changed IP; an unchanged IP suppresses the repoint (no Rebaseline)."
- acceptance: go test -race ./internal/config/... ./internal/device/... ./internal/dnsresolve/... passes, including a test that interleaves re-resolution and failover advance under -race with no reported race, no deadlock (bounded test time), and an assertion that a simultaneous liveness-loss event produces exactly one SetPeerRemote.
- suggestedModel: standard
- dependsOn: ["T73","T74"]
- ledgerRefs: ["goals:G5"]
- resultCommit: 9798ff7
- completion: "Cross-controller concurrency proof pinned (test-only, internal/device/interleave_race_test.go): 3 -race interleave tests covering the three contested schedules between the re-resolution and hub-failover controllers (co-owners of the mutable endpoint set) + a Q36 boot-defer seam unit test. Proves no data race, no deadlock (5s per-schedule guard), and exactly-one-SetPeerRemote on a simultaneous liveness-loss event — a STRUCTURAL guarantee (both check() and updateResolution() hold h.mu across their whole body), reviewer-verified non-vacuous by a lock-removal mutation that failed both -race and the assertion. No production change needed; the controllers already coordinate correctly. Unanimous 1-round panel approve; rebased past T90/T100 and ff-merged to main as 9798ff7."
- sessionLogs: [".cq/logs/20260714-030903-abb91a4582d82ce09.md",".cq/logs/20260714-031341-ad2d941ce63da2108.md",".cq/logs/20260714-031341-a2380182ab5d80508.md"]
- rawLogs: [".cq/logs/raw/20260714-031341-ad2d941ce63da2108.jsonl",".cq/logs/raw/20260714-031341-a2380182ab5d80508.jsonl"]

## M22

### T76 — done

- createdAt: 2026-07-13T21:55:53.025Z
- updatedAt: 2026-07-14T06:11:00.880Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: "Add DPI-posture guard tests: opt-in OFF means zero DNS and an unchanged wire audit"
- description: "Operationalize the Q29/Q33 security acceptance target. (1) Unit-level guard: with any all-IP-literal config, assert no dnsresolve.Resolver is constructed and no resolution goroutine starts (a tripwire fake resolver that fails the test if Lookup is ever called, injected at the wiring seam). (2) Wire-audit guard: the existing p5 DPI test (test/e2e p5_dpi_test.go) must pass unchanged on a DNS-disabled config — assert the tunnel wire is byte-identical in shape to pre-G5 (the audit already encodes this; the task is running and, if needed, extending it to assert zero port-53/DoH/DoT egress from the edge netns while DNS is off). (3) Documentation hook: the guard test names the exact leaked artifact per mode (system: cleartext DNS query naming the concentrator; DoH/DoT: TLS SNI + timing to the resolver) so the docs task can cite a tested statement."
- acceptance: go test ./internal/device/... passes the tripwire-resolver case; the p5 DPI e2e (go test -tags e2e -run P5 on the e2e hosts) passes unchanged on a DNS-off config, extended with a zero-DNS-egress assertion for the edge namespace.
- suggestedModel: standard
- dependsOn: ["T74"]
- ledgerRefs: ["goals:G5"]
- resultCommit: 9bd121a
- completion: "DPI-posture guard tests (Q29/Q33): (1) internal/device tripwire unit test — an all-IP-literal config with a tripwireResolver whose Lookup t.Errorf's the instant invoked (injected via the production up()/resolverFactory seam) proves NO resolver Lookup ever fires on a DNS-off config (Q29 zero-DNS inertness), mutation-verified by BOTH reviewers on both the sync boot-resolve and async re-resolution paths (the async path is triply gated). (2) internal/wireaudit.CountPcapPackets (protocol-agnostic pcap record walk + unit tests) + a test/e2e/p5_dpi_test.go extension: a concurrent tcpdump on ports 53/853/443 (TCP+UDP) over the DNS-off P5 session asserting ZERO egress from the edge namespace (capture starts pre-boot). Compiles/vets under -tags e2e; privileged P5 execution deferred (G2). Documented the exact per-mode leaked artifact for the docs task to cite. Unanimous 1-round panel approve; rebased onto current main and ff-merged as 9bd121a."
- sessionLogs: [".cq/logs/20260714-060333-ab14171494ebe3742.md",".cq/logs/20260714-061018-aabdf77e0c8e0f30f.md",".cq/logs/20260714-061018-a98cbbd2ae1d05ea6.md"]
- rawLogs: [".cq/logs/raw/20260714-060333-ab14171494ebe3742.jsonl",".cq/logs/raw/20260714-061018-aabdf77e0c8e0f30f.jsonl",".cq/logs/raw/20260714-061018-a98cbbd2ae1d05ea6.jsonl"]

### T77 — done

- createdAt: 2026-07-13T21:56:00.076Z
- updatedAt: 2026-07-14T04:24:18.197Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: "Add the netns e2e: dial-by-name, mid-session concentrator IP change, tunnel survives"
- description: "The v1 acceptance bar (Q36). Extend the privileged netns e2e suite (build on test/e2e/failover_test.go's mid-session-switch harness): the edge config names the concentrator by HOSTNAME (dns opt-in on, system resolver). Name resolution inside the netns needs a netns-local answer source — /etc/hosts is not netns-scoped for the Go resolver, so run a minimal in-test UDP DNS responder inside the edge namespace (dnsmessage-based, reusing the package helpers) and point the edge at it (resolv.conf in a mount namespace, or the [dns] dot/doh server override if simpler — choose and document). Scenario: (0) the edge BOOTS while the name is UNRESOLVABLE (the in-test responder initially down/NXDOMAIN): the tunnel comes up endpoint-less (tolerant boot, no crash); (1) the responder starts answering, the edge's FIRST successful resolve installs the engine peer endpoint and the first handshake goes through — tunnel up, traffic flows (proves the boot-unresolvable → first-resolve → handshake path end-to-end, R70); (2) the concentrator's IP CHANGES mid-session (move the address in the conc namespace and update the DNS answer); (3) the edge re-resolves (poll or liveness-loss trigger) and repoints; (4) the tunnel SURVIVES — post-change traffic flows within a bounded window. Also assert the D32 regression guard: the resequencer re-baselines (traffic actually resumes, not just a handshake)."
- acceptance: "go test -tags e2e -run DNS (or the chosen test name) passes as root on the e2e hosts (o3.7mind.io aarch64 + llm-ubuntu-0 amd64): edge boots with the name unresolvable and reaches tunnel-up only after the responder starts answering (first-resolve handshake proven, R70), then IP change mid-session, post-change ping/iperf traffic resumes within the poll+settle bound; the test is hermetic to the netns sandbox (no external DNS egress)."
- suggestedModel: frontier
- dependsOn: ["T74"]
- ledgerRefs: ["goals:G5"]
- resultCommit: 2afe674
- completion: "Netns e2e for the Q36 v1 DNS acceptance bar (test-only, test/e2e/dns_failover_test.go: TestDNSHubResolveAndReroute). Edge names the concentrator by hostname (dns opt-in, system resolver); hermetic in-namespace dnsmessage UDP responder (mount-ns-private resolv.conf + GODEBUG=netdns=go on the edge daemon + query-count probe; no external DNS egress). Stages all 5 phases: NXDOMAIN endpoint-less boot → R70 first-resolve engine-endpoint install + first handshake → mid-session concentrator IP change (multi-homed conc + DNS cutover + real old-address flush) → re-resolve repoint (exactly one SetPeerRemote) → D32 resequencer-rebaseline guard (real post-change iperf3 transfer, not a handshake counter). Unanimous 1-round panel approve; daemon markers/semantics verified against source by both reviewers. Compiles/vets under -tags e2e; PRIVILEGED root execution DEFERRED to the o3.7mind.io + llm-ubuntu-0 hosts (G2 pattern — sandbox lacks /dev/net/tun). docs/design.md synced. Rebased onto current main and ff-merged as 2afe674. Filed D54 (golangci scans nested worktrees)."
- sessionLogs: [".cq/logs/20260714-041618-aa6460b0564523c9f.md",".cq/logs/20260714-042350-ab2788ec23f5df97d.md",".cq/logs/20260714-042350-a0e589c40f828c748.md"]
- rawLogs: [".cq/logs/raw/20260714-041618-aa6460b0564523c9f.jsonl",".cq/logs/raw/20260714-042350-ab2788ec23f5df97d.jsonl",".cq/logs/raw/20260714-042350-a0e589c40f828c748.jsonl"]

### T78 — done

- createdAt: 2026-07-13T21:56:10.379Z
- updatedAt: 2026-07-14T05:20:26.654Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Realhosts report-only dial-by-name check (stretch)
- description: "Q36: in scope only as a report-only stretch per the M10/Q12 discipline. Extend test/realhosts (runner.go harness): bring the standing two-host testbed up with the edge dialing the concentrator by its real DNS name (o3.7mind.io resolves to the standing ConcPubIP) with the dns opt-in enabled, and REPORT (never fail the suite on) the outcome: resolved address, time-to-first-handshake, and steady traffic. No mid-session IP change on real hosts (the public IP is fixed); this tier only proves the resolve-then-dial path against real resolvers and real NAT. Keep it strictly report-only: any failure logs a report line, exit status stays green, matching the existing realhosts discipline."
- acceptance: go test -tags realhosts ./test/realhosts/... runs against the standing testbed and emits the dial-by-name report (resolution result + handshake + traffic outcome); an induced failure (bogus name) demonstrably does NOT fail the suite — it reports and passes.
- suggestedModel: standard
- dependsOn: ["T77"]
- ledgerRefs: ["goals:G5"]
- resultCommit: 66c1110
- completion: "Report-only realhosts dial-by-name stretch tier (test-only, test/realhosts/dns_dial_test.go: TestRealDNSDialByName). Brings up the standing two-host testbed with the edge dialing the concentrator via its real DNS name (peer dns=true opt-in) + a bogus-name subtest; reports resolved address / time-to-first-handshake / traffic sample purely via t.Logf (resolution/handshake/traffic outcomes NEVER fail the suite — report-only discipline; only infra bring-up is fatal, matching sibling tiers). R2 fixed a logic error: resolveOutcome() now sources RESOLUTION from BOTH the R70 'first endpoint resolution' marker (deferred-install path) AND handshake-implies-resolution (the silent Q30 boot-resolve happy path), reporting the standing cfg.ConcPubIP — correct on both paths. Bogus name uses the RFC-2606 .example TLD (passes validateHostname so the daemon starts, deterministic NXDOMAIN). Compiles/vets under -tags realhosts; EXECUTION deferred to the realhosts run. 2 review rounds; rebased onto current main and ff-merged as 66c1110."
- sessionLogs: [".cq/logs/20260714-045408-a6ff3c7de32c86767.md",".cq/logs/20260714-051113-abaaea20708e03b76.md",".cq/logs/20260714-050548-a54ae7c80e1ab909f.md",".cq/logs/20260714-050548-a65d3da77adb4f5b4.md",".cq/logs/20260714-051954-a9e48eb98d4a79d0d.md",".cq/logs/20260714-051954-a01261db5c976ffab.md"]
- rawLogs: [".cq/logs/raw/20260714-045408-a6ff3c7de32c86767.jsonl",".cq/logs/raw/20260714-051113-abaaea20708e03b76.jsonl",".cq/logs/raw/20260714-050548-a54ae7c80e1ab909f.jsonl",".cq/logs/raw/20260714-050548-a65d3da77adb4f5b4.jsonl",".cq/logs/raw/20260714-051954-a9e48eb98d4a79d0d.jsonl",".cq/logs/raw/20260714-051954-a01261db5c976ffab.jsonl"]

### T79 — done

- createdAt: 2026-07-13T21:56:16.623Z
- updatedAt: 2026-07-14T07:22:28.482Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: "Sync docs and example config: DNS endpoints, resolver privacy trade-offs"
- description: "Per the repo rule (AGENTS.md: docs in sync with code in the same change-set) close the goal with a doc sweep: README.md and docs/design.md gain the DNS-endpoints section — opt-in posture and WHY default-off (the DPI thesis: a pre-tunnel cleartext signal naming a blocklistable host), the exact leaked artifact per resolver mode (system: plaintext query; DoH/DoT: SNI + timing to the provider — cite the tested statements from the DPI-posture task), defer-and-reconcile boot semantics, re-resolution cadence + liveness-loss trigger + change suppression, multi-record expansion feeding hub-failover, and the mixing rules with ordered endpoints. wanbond.example.toml gains a commented hostname-endpoint peer plus a full [dns] block (system, doh, dot variants, incl. the bootstrap-IP requirement). docs/install.md if it documents config fields. Verify the example still loads."
- acceptance: "The example config parses: the existing example-config test (or a one-line addition to it) round-trips wanbond.example.toml through config.Load with the new block commented-in variant covered; README/design.md sections exist and name the per-mode leaked artifacts; go test ./... passes."
- suggestedModel: fast
- dependsOn: ["T76","T69","T71"]
- ledgerRefs: ["goals:G5"]
- resultCommit: 167bed3
- completion: "G5 DNS/hostname-endpoint doc-sync (docs-only + config test). README.md/docs/design.md/docs/install.md/docs/runbook.md/wanbond.example.toml describe the shipped DNS feature; new TestExampleConfigLoads (internal/config/config_test.go) READS the real wanbond.example.toml and config.Load()s its extracted doh/dot/system [dns] variants + the hostname-peer example (mutation-verified). 3-round criticism loop: R1 fixed 7 findings (broken FULL-[dns] example, test-didn't-read-file, false '[dns] required' claims, wrong prose, stale text); R2→R3 fixed 2 residual design.md false claims grounded against orderAddrPorts (stable v4-then-v6 partition preserving within-family resolver order) and updateResolution (active-AddrPort-survival-scoped suppression). Unanimous round-3 panel approve. Rebased onto current main and ff-merged as 167bed3."
- sessionLogs: [".cq/logs/20260714-070000-aed9d426a93a8fb36.md",".cq/logs/20260714-070917-ad04d044c371aebd8.md",".cq/logs/20260714-071939-a4cceb8d343f3d498.md",".cq/logs/20260714-071939-a193ca96634ef63ef.md"]
- rawLogs: [".cq/logs/raw/20260714-070000-aed9d426a93a8fb36.jsonl",".cq/logs/raw/20260714-070917-ad04d044c371aebd8.jsonl",".cq/logs/raw/20260714-071939-a4cceb8d343f3d498.jsonl",".cq/logs/raw/20260714-071939-a193ca96634ef63ef.jsonl"]

## M23

### T80 — done

- createdAt: 2026-07-13T22:27:04.600Z
- updatedAt: 2026-07-13T23:01:08.347Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Add per-peer psk and name fields to config.Peer
- description: In internal/config, add `psk config.Key` (toml `psk`) and `name string` (toml `name`) to config.Peer. Keep top-level Config.PSK as the single-peer default (unchanged for existing configs). Normalize in normalize()/resolveEndpoints-adjacent code so a single-peer config with only top-level psk keeps its exact current shape. Do NOT touch the edge-side schema beyond the value it already sets. No datapath change in this task.
- acceptance: A new config unit test parses a 2-peer TOML with distinct per-peer `psk`+`name` values and a legacy single-peer TOML carrying only top-level `psk`; the single-peer parse is byte-identical to today (golden struct compare), and the multi-peer parse exposes each peer's psk/name. `go test ./internal/config/...` passes.
- suggestedModel: standard
- ledgerRefs: ["goals:G4"]
- resultCommit: 33e8e3f
- completion: Per-peer psk/name fields added to config.Peer with legacy single-peer golden-shape guard; semantic rebase conflict vs T67 (EndpointSpecs non-nil empty slice in the golden) resolved by conflict-resolver; merged ff to main.
- sessionLogs: [".cq/logs/20260713-224948-a49f5813151f3f0bf.md",".cq/logs/20260713-225437-a1850ae28a48e003a.md",".cq/logs/20260713-225437-ac6ebf9d1c27aa4bf.md",".cq/logs/20260713-230051-a21afb20872f222c8.md"]
- rawLogs: [".cq/logs/raw/20260713-224948-a49f5813151f3f0bf.jsonl",".cq/logs/raw/20260713-225437-a1850ae28a48e003a.jsonl",".cq/logs/raw/20260713-225437-ac6ebf9d1c27aa4bf.jsonl",".cq/logs/raw/20260713-230051-a21afb20872f222c8.jsonl"]

### T81 — done

- createdAt: 2026-07-13T22:27:14.700Z
- updatedAt: 2026-07-13T23:19:39.587Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Validate per-peer psk presence, distinctness, and single-peer back-compat
- description: "Extend config.validate: when len(peers) > 1, require each peer's `psk` present and pairwise-distinct (equal psks defeat authenticated demux) and each peer `name` present and unique; when len(peers) == 1, accept top-level psk as the default and require no per-peer psk. Reject a per-peer psk that duplicates another peer's. ALSO reject an EDGE role config with >1 peer (concentrator-only scope, Q21 — the edge dials exactly one concentrator peer per process). Keep the rest of edge validation unchanged. Fail fast with precise messages."
- acceptance: "Table-driven config.validate test: >1 peer with equal per-peer psks fails; >1 peer with a missing per-peer psk fails; duplicate peer names fail; edge role with 2 peers fails with a scope-explaining message; single-peer top-level-only passes; 2 peers with distinct psks+names pass. `go test ./internal/config/...` passes."
- suggestedModel: standard
- dependsOn: ["T80"]
- ledgerRefs: ["goals:G4"]
- resultCommit: 4d886cd1119dd99a48931fc973d3f43bc9c2a34d
- completion: "validate() extended for multi-peer: per-peer psk required + pairwise-distinct, unique names, edge-role >1-peer scope rejection, single-peer top-level back-compat (per-peer psk rejected as redundant); table-driven 6-case test; merged ff to main."
- sessionLogs: [".cq/logs/20260713-231437-aa2524bb4945f774b.md",".cq/logs/20260713-231830-a8eef803232932fbf.md",".cq/logs/20260713-231830-a2082c0e624c73f95.md"]
- rawLogs: [".cq/logs/raw/20260713-231437-aa2524bb4945f774b.jsonl",".cq/logs/raw/20260713-231830-a8eef803232932fbf.jsonl",".cq/logs/raw/20260713-231830-a2082c0e624c73f95.jsonl"]

### T82 — done

- createdAt: 2026-07-13T22:27:18.375Z
- updatedAt: 2026-07-13T23:30:31.892Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Add config helper resolving each peer's effective PSK and identity
- description: Add a config method/function returning, per configured peer, its effective PSK (top-level Config.PSK for the single-peer back-compat case; the peer's own psk when >1 peer) plus its stable name/id. This is the single source device.Up and the Bind consume so back-compat lives in exactly one place.
- acceptance: "Unit test: for a single-peer config the helper returns the top-level psk; for a multi-peer config it returns each peer's own psk and its name/id; ordering is stable and matches cfg.WireGuard.Peers. `go test ./internal/config/...` passes."
- suggestedModel: standard
- dependsOn: ["T80"]
- ledgerRefs: ["goals:G4"]
- resultCommit: fe7da10
- completion: "Config.PeerIdentity{PSK,Name} + PeerIdentities() landed: single-peer resolves top-level PSK (shadowing invariant mutation-verified), multi-peer uses per-peer psk/name in Peers order, hex-pubkey-prefix name fallback; additive test conflict vs T81 resolved by conflict-resolver; merged ff to main."
- sessionLogs: [".cq/logs/20260713-231437-ac3e53f3aafa4fc7e.md",".cq/logs/20260713-232253-a21e287f36bef9788.md",".cq/logs/20260713-231830-a91a4176a0efd8739.md",".cq/logs/20260713-231830-adb81d36d2064a300.md",".cq/logs/20260713-232716-acaab660e4b8c3626.md",".cq/logs/20260713-232716-a00c1b140b54c2482.md",".cq/logs/20260713-232951-a0223153774c695c8.md"]
- rawLogs: [".cq/logs/raw/20260713-231437-ac3e53f3aafa4fc7e.jsonl",".cq/logs/raw/20260713-232253-a21e287f36bef9788.jsonl",".cq/logs/raw/20260713-231830-a91a4176a0efd8739.jsonl",".cq/logs/raw/20260713-231830-adb81d36d2064a300.jsonl",".cq/logs/raw/20260713-232716-acaab660e4b8c3626.jsonl",".cq/logs/raw/20260713-232716-a00c1b140b54c2482.jsonl",".cq/logs/raw/20260713-232951-a0223153774c695c8.jsonl"]

## M24

### T83 — done

- createdAt: 2026-07-13T22:27:30.808Z
- updatedAt: 2026-07-13T23:57:54.818Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Introduce peerState and key Multipath state by peer
- description: "In internal/bind, introduce a peerState type holding the fields that are singletons today: virt (*udpEndpoint), outerSeq, scheduler, fecSend/fecRecv, resequencer (atomic.Pointer), reflector, sendCodec, the peer's path/remote view, and probers. ALSO split pathState (multipath.go:81) into SHARED per-socket state (name, id, src, conn, readLoop, deferred-path machinery) and per-(peer,path) state (codec, remote/hasRemote, prober, txBytes/rxBytes) — concentrator sockets are SHARED across peers while each peer owns its return-path/remote view, so the decomposition must be explicit. RUNTIME PATH MUTATION IS IN SCOPE (R72): the live dynamic-path machinery (deferred paths, internal/bind/runtime_path_test.go, tolerant_membership_test.go) operates on the SHARED socket state, so adding or removing a shared path at runtime must instantiate/tear down the per-(peer,path) state (codec, remote/hasRemote, prober, txBytes/rxBytes) for EVERY currently-bound peer — design the split so this fan-out has a single owner and is exercised while >=2 peerStates exist. Change Multipath to hold the shared path list plus a map keyed by peer id/name plus the lookup maps needed later (endpoint->peer, source->peer placeholders). Construct EXACTLY ONE peerState on the single-peer path so behavior is byte-identical. Preserve the m.mu discipline and the lock-free receive fast path (resequencer/fecRecv stay atomic.Pointer per peer). Keep the conn seam isolated to internal/bind/bind.go."
- acceptance: "`go build ./...` succeeds and the full existing internal/bind test suite passes unchanged (single-peer path proven behavior-preserving) — including the runtime-path suites (runtime_path_test.go, tolerant_membership_test.go). The former singleton fields are now reached through peerState; a grep shows no remaining process-global resequencer/outerSeq/scheduler on Multipath; per-(peer,path) state (remote/prober/tx/rx) is held off the shared socket state. A new unit test asserts that with two peerStates bound, adding a shared path at runtime creates per-(peer,path) state for BOTH peers and removing it tears down both peers' per-(peer,path) state, leaving each peer's remaining paths untouched."
- suggestedModel: frontier
- dependsOn: ["T82"]
- ledgerRefs: ["goals:G4"]
- resultCommit: "55041e3"
- completion: peerState introduced + pathState split into sharedPathState/peerPathState; Multipath embeds the primary peerState (promotion keeps single-peer datapath byte-identical, zero test edits); runtime shared-path add/remove fans per-(peer,path) state to every bound peer via a single owner with rollback; two-peer fan-out test; merged ff to main. Latent deferred-path multi-peer alignment gap filed as D42.
- sessionLogs: [".cq/logs/20260713-235250-a01fcd30d435bc669.md",".cq/logs/20260713-235735-a10456d6fb76f7f1c.md",".cq/logs/20260713-235735-aefa45ecf45cfffd3.md"]
- rawLogs: [".cq/logs/raw/20260713-235250-a01fcd30d435bc669.jsonl",".cq/logs/raw/20260713-235735-a10456d6fb76f7f1c.jsonl",".cq/logs/raw/20260713-235735-aefa45ecf45cfffd3.jsonl"]

### T84 — done

- createdAt: 2026-07-13T22:27:42.661Z
- updatedAt: 2026-07-14T00:17:00.236Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Derive per-peer frame Codec and Reflector from each peer PSK
- description: Replace the single m.reflector/m.sendCodec with a per-peerState frame.Codec and telemetry.Reflector derived from that peer's effective PSK. Each path's receive Codec is the codec of the peer the path is bound to (a still-unbound path has no peer codec yet — that is the demux task's concern). Keep NewCodec/NewReflector PSK-derivation unchanged.
- acceptance: "Unit test: two peerStates built from distinct psks produce codecs where one peer's Encode output fails the other's Decode (cross-psk frames are rejected) and each Reflector only authenticates probes under its own psk. `go test ./internal/bind/... ./internal/telemetry/...` passes."
- suggestedModel: frontier
- dependsOn: ["T83"]
- ledgerRefs: ["goals:G4"]
- resultCommit: b61f152
- completion: Per-peer PSK codec/reflector derivation landed (peerState.psk + newPeerState/newCodec seams; dead Multipath.psk removed); cross-psk rejection pinned by the deterministic cryptographic invariant; merged ff to main.
- sessionLogs: [".cq/logs/20260714-001002-a37d00249a2a18a46.md",".cq/logs/20260714-001524-a6a63e0616ed2fed9.md",".cq/logs/20260714-001524-ab4832a41c717ad90.md"]
- rawLogs: [".cq/logs/raw/20260714-001002-a37d00249a2a18a46.jsonl",".cq/logs/raw/20260714-001524-a6a63e0616ed2fed9.jsonl",".cq/logs/raw/20260714-001524-ab4832a41c717ad90.jsonl"]

### T85 — done

- createdAt: 2026-07-13T22:27:47.208Z
- updatedAt: 2026-07-14T00:17:04.962Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Route Send to a peerState via a per-peer virtual endpoint map
- description: Give each peer a distinct virtual endpoint (*udpEndpoint) and build an endpoint->peerState map. Change Send(bufs, ep) to resolve the peerState from ep and use THAT peer's outerSeq, scheduler, fecSend, sendCodec, and path/remote set — instead of the process-global singletons. Preserve the classifier and the m.mu-held path pick + lock-free txBytes accounting (now per-(peer,path)). An unknown endpoint returns the existing wrong-endpoint/no-path error rather than misrouting.
- acceptance: "Bind test with two peers each holding a distinct virt endpoint: Send to peer A's endpoint advances ONLY peer A's outerSeq and egresses on A's path set; Send to peer B's endpoint is fully independent; a Send to an unknown endpoint errors and touches no peer. `go test ./internal/bind/...` passes."
- suggestedModel: frontier
- dependsOn: ["T83"]
- ledgerRefs: ["goals:G4"]
- resultCommit: 7e02c85
- completion: "Send routing via peerByVirt landed: Send resolves the owning peerState from the endpoint and drives that peer's outerSeq/scheduler/sendCodec/fecSend/per-(peer,path) egress; unknown endpoint errors safely; merged ff to main. D44 filed for the primary-only fecFlushDeadline follow-up."
- sessionLogs: [".cq/logs/20260714-001002-a9f753cb838945145.md",".cq/logs/20260714-001524-a370601a73478ca92.md",".cq/logs/20260714-001524-a2b4d488f2a5615f5.md"]
- rawLogs: [".cq/logs/raw/20260714-001002-a9f753cb838945145.jsonl",".cq/logs/raw/20260714-001524-a370601a73478ca92.jsonl",".cq/logs/raw/20260714-001524-a2b4d488f2a5615f5.jsonl"]

### T86 — done

- createdAt: 2026-07-13T22:27:51.291Z
- updatedAt: 2026-07-14T00:19:21.011Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Demux receive delivery to per-peer resequencer under per-peer virt endpoint
- description: "Keep a SINGLE engine-facing ReceiveFunc but make it drain EACH peer's resequencer and stamp each delivered inner datagram with that peer's stable virtual endpoint (per-packet endpoint fill), so the engine attributes return traffic to the right peer and Send routes replies back via that peer's virt (A1: one virtual endpoint per peer). handleInbound feeds a decoded DATA/PARITY frame into the resequencer/fecRecv of the peer the arriving path is bound to."
- acceptance: "Bind test: interleaved DATA for two bound peers is delivered up with per-packet endpoints matching each peer, and each peer's resequencer orders its own outer-seq stream independently (no cross-peer frames observed in either resequencer). `go test ./internal/bind/...` passes."
- suggestedModel: frontier
- dependsOn: ["T83"]
- ledgerRefs: ["goals:G4"]
- resultCommit: d6816b1
- completion: "Receive demux landed: single ReceiveFunc drains every bound peer's resequencer round-robin (lock-free peersView snapshot) and stamps per-packet endpoints with the owning peer's virt (A1); three-sibling test-helper conflict resolved by conflict-resolver; merged ff to main."
- sessionLogs: [".cq/logs/20260714-001002-a51702a0c24f1dd5d.md",".cq/logs/20260714-001524-a9eeeffca5db3fd43.md",".cq/logs/20260714-001524-ae44d2e7f23156dd5.md",".cq/logs/20260714-001851-a02773d07a098daaf.md"]
- rawLogs: [".cq/logs/raw/20260714-001002-a51702a0c24f1dd5d.jsonl",".cq/logs/raw/20260714-001524-a9eeeffca5db3fd43.jsonl",".cq/logs/raw/20260714-001524-ae44d2e7f23156dd5.jsonl",".cq/logs/raw/20260714-001851-a02773d07a098daaf.jsonl"]

### T87 — done

- createdAt: 2026-07-13T22:27:59.938Z
- updatedAt: 2026-07-14T00:48:46.604Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Preserve per-peer resequencer lifecycle and D32 rebaseline
- description: "Make the per-Open (re)creation of the resequencer and fecSend/fecRecv, and the SetPeerRemote/Rebaseline D32 fix, operate per peerState rather than process-globally: a rebaseline or reconnect on one peer must never touch another peer's release point. Keep the disjoint-mutex, never-held-across-syscall discipline per peer."
- acceptance: "Per-peer resequencer unit test: two interleaved outer-seq streams stay separated across an Open cycle; a Rebaseline() triggered on peer A leaves peer B's `next`/release point untouched (the D32-class regression, now per-peer). `go test ./internal/bind/... ./internal/reseq/...` passes."
- suggestedModel: frontier
- dependsOn: ["T86"]
- ledgerRefs: ["goals:G4"]
- resultCommit: 603a136
- completion: Per-peer resequencer/FEC Open lifecycle + peer-scoped D32 rebaseline landed (openPeerDatapathLocked over m.peers; setPeerRemoteLocked(ps,ap)); Close→Open asymmetry fixed; mutation-verified regression guard; merged ff to main.
- sessionLogs: [".cq/logs/20260714-004427-a78b9fe5eb3d9a9ee.md",".cq/logs/20260714-004825-a19e6e3ee6175b726.md",".cq/logs/20260714-004825-a260f3da7a78a9174.md"]
- rawLogs: [".cq/logs/raw/20260714-004427-a78b9fe5eb3d9a9ee.jsonl",".cq/logs/raw/20260714-004825-a19e6e3ee6175b726.jsonl",".cq/logs/raw/20260714-004825-a260f3da7a78a9174.jsonl"]

## M25

### T88 — done

- createdAt: 2026-07-13T22:28:09.005Z
- updatedAt: 2026-07-14T01:56:35.943Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Bind source->peer only via authenticated PROBE with trial-decode across peer PSKs
- description: "Build the authenticated path->peer binding: an inbound PROBE from an unbound source is trial-decoded against each configured peer's psk-derived codec/reflector (O(peers), bounded by the static peer count), and on the first successful MAC verification the source address is bound to that peer (source->peer map). Only an authenticated PROBE ever establishes a binding; unauthenticated frames verify under no psk and are dropped cheaply. No DATA/PARITY wire change; no peer id in DATA."
- acceptance: "Unit test: a PROBE encoded under peer B's psk from a fresh source binds that source to B (and reflects an echo); a forged/garbage frame verifies under no peer psk and establishes no binding; trial-decode stops at the first matching psk. `go test ./internal/bind/...` passes."
- suggestedModel: frontier
- dependsOn: ["T84","T85"]
- ledgerRefs: ["goals:G4"]
- resultCommit: 7a5a0e6
- completion: "Authenticated source->peer binding landed: readLoop-only demuxInbound with lock-free CoW peerBySource (atomic.Pointer+CAS), trial-decode across peer psks binding ONLY on the first MAC-verified PROBE (continues past non-PROBE decodes), per-socket views copy-on-write, single-peer fast path byte-identical; 2 review rounds, mutation-verified tests; merged ff to main. D47 filed for the T90 binding-key design."
- sessionLogs: [".cq/logs/20260714-013631-a8451cdfaa8267fa3.md",".cq/logs/20260714-015014-a6b4e576272a3aa1a.md",".cq/logs/20260714-015014-ae4c878a9b8cdabcd.md",".cq/logs/20260714-015014-a76ead6a90c6f25fb.md"]
- rawLogs: [".cq/logs/raw/20260714-013631-a8451cdfaa8267fa3.jsonl",".cq/logs/raw/20260714-015014-a6b4e576272a3aa1a.jsonl",".cq/logs/raw/20260714-015014-ae4c878a9b8cdabcd.jsonl",".cq/logs/raw/20260714-015014-a76ead6a90c6f25fb.jsonl"]

### T89 — done

- createdAt: 2026-07-13T22:28:18.062Z
- updatedAt: 2026-07-14T02:21:39.612Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Gate DATA/PARITY from unbound sources
- description: In handleInbound, drop DATA/PARITY arriving from a source with no established source->peer binding (never attribute it to any peer's resequencer/fecRecv). Once an authenticated PROBE binds the source, subsequent DATA/PARITY from it route to that peer. Rely on WG handshake/keepalive retransmit to cover the brief pre-binding gap.
- acceptance: "Test: DATA from an unbound source reaches NO resequencer (dropped); after an authenticated PROBE binds that source to peer B, subsequent DATA lands in B's resequencer only. `go test ./internal/bind/...` passes."
- suggestedModel: frontier
- dependsOn: ["T88","T86"]
- ledgerRefs: ["goals:G4"]
- resultCommit: "7900642"
- completion: "Unbound-source DATA/PARITY gate pinned (test-only, TestSharedSocketGatesUnboundDataParity): on a shared multi-view socket, validly-decoding DATA/PARITY from an unbound source reaches NO resequencer/fecRecv and mints no binding; post-PROBE DATA routes to the bound peer only. Production gate is T88's continue-then-drop; PARITY subtest arms fecRecv + observes reconstruction (mutation-verified both rounds). 2 review rounds; merged ff to main."
- sessionLogs: [".cq/logs/20260714-020133-a60e7a44eaa038405.md",".cq/logs/20260714-021726-a68d4cd936a9aeaec.md",".cq/logs/20260714-021726-a3a44fe24270c9538.md",".cq/logs/20260714-022112-a366f6435dd6b4681.md",".cq/logs/20260714-022112-a4a8424b945a1df11.md"]
- rawLogs: [".cq/logs/raw/20260714-020133-a60e7a44eaa038405.jsonl",".cq/logs/raw/20260714-021726-a68d4cd936a9aeaec.jsonl",".cq/logs/raw/20260714-021726-a3a44fe24270c9538.jsonl",".cq/logs/raw/20260714-022112-a366f6435dd6b4681.jsonl",".cq/logs/raw/20260714-022112-a4a8424b945a1df11.jsonl"]

### T90 — done

- createdAt: 2026-07-13T22:28:29.503Z
- updatedAt: 2026-07-14T03:07:21.650Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Re-bind a roamed source to its peer on a fresh authenticated PROBE
- description: "Handle NAT/roaming: when a bound peer's traffic appears from a NEW source address, the new source re-binds to the SAME peer only on a fresh authenticated PROBE from it. Until then, DATA from the new source is dropped (never misrouted into another peer's resequencer). Mirror the existing D11/T16 authenticated-re-learn discipline, now per peer."
- acceptance: "Roam test: peer B's source changes; B's DATA from the new source is dropped until an authenticated PROBE under B's psk re-binds it, after which it routes to B; throughout, peer A's resequencer never observes any of B's frames. `go test ./internal/bind/...` passes."
- suggestedModel: frontier
- dependsOn: ["T89"]
- ledgerRefs: ["goals:G4"]
- resultCommit: 8c92b95
- completion: "Per-peer roam re-bind pinned (test-only, TestConcentratorRoamRebindsPeerOnAuthenticatedProbe in internal/bind): a bound peer B whose traffic appears from a NEW source has that source's DATA dropped until a fresh authenticated PROBE under B's psk re-binds it to the SAME peer (view remote repoints to the new source); peer A's resequencer never observes B's frames. The behavior is provided by the T88/T89 unbound-source gate + PROBE-only binding; T90 locks it. Unanimous 1-round panel approve, mutation-verified roam-specific (fails where T89 passes). ff-merged to main."
- sessionLogs: [".cq/logs/20260714-025518-ab151251d328f46db.md",".cq/logs/20260714-030657-a4a3590b837613332.md",".cq/logs/20260714-030657-aa1362bb37a2ee5d5.md"]
- rawLogs: [".cq/logs/raw/20260714-025518-ab151251d328f46db.jsonl",".cq/logs/raw/20260714-030657-a4a3590b837613332.jsonl",".cq/logs/raw/20260714-030657-aa1362bb37a2ee5d5.jsonl"]

### T91 — done

- createdAt: 2026-07-13T22:28:36.021Z
- updatedAt: 2026-07-14T03:53:04.093Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Cap provisional unbound-source demux state; lazy peerState instantiation and dead-peer teardown
- description: "Bound the bootstrap DoS surface and pin the per-peer lifecycle (Q26): cap the provisional/unbound-source tracking state (small, drop-on-exhaustion) separately from the steady-state peerState map, which is sized to the static configured peer set. Instantiate the HEAVY per-peer state (the ~2048-frame resequencer ring, FEC encoder/decoder, scheduler, per-(peer,path) probers) LAZILY on first authenticated source->peer binding rather than at Open; tear it down when that peer's WG session/liveness is gone (wired from device peer events), freeing the ring and FEC buffers; a torn-down configured peer re-instantiates cleanly on its next authenticated PROBE. On cap exhaustion, drop new unbound-source state; NOTHING ever evicts a LIVE peer."
- acceptance: "Tests (fake clock where needed): a flood of many distinct spoofed unbound source addresses cannot grow demux state past the configured cap and cannot evict or disturb a live bound peer; peerState heavy fields are absent before first authenticated binding, instantiated on binding, torn down after session/liveness loss, and re-instantiate + pass traffic on re-bind; a live (Up) peer is never torn down regardless of other peers' churn. `go test ./internal/bind/...` passes."
- suggestedModel: frontier
- dependsOn: ["T89"]
- ledgerRefs: ["goals:G4"]
- resultCommit: a99c3ed
- completion: "Bounded the bootstrap DoS surface + pinned the per-peer lifecycle (internal/bind/multipath.go + peer_lifecycle_test.go + peer_fec_lifecycle_test.go): capped source→peer demux map (default 1024, drop-on-exhaustion, never evicts a live binding); heavy per-peer receive datapath (2048-frame resequencer ring + FEC decoder) instantiates LAZILY on first authenticated binding via lock-free atomic-pointer CAS; TearDownPeer (device-facing seam) frees ring/FEC + releases a dead peer's bindings, refusing any live (Up) peer + the primary; dispatchInbound nil-guards a torn-down ring. R2 additionally FIXED a production parity-loss defect (fecSend freed on teardown was never re-instantiated on re-bind → a rebound FEC peer silently sent without parity) by rebuilding fecSend on re-bind, and closed the CAS ordering hole with a per-peer lifecycleMu serializing heavy-trio build vs teardown/close (fecSend made atomic.Pointer). All mechanisms mutation-verified (2 rounds); go test -race ./internal/bind/... -count=2 green incl. a 400-round concurrent teardown/rebind test. Deadlock-free (strict m.mu ⊃ lifecycleMu). Filed 2 deferred defects: D49 (insider cap-monopoly), D50 (untracked TearDownPeer device wiring). ff-merged as a99c3ed."
- sessionLogs: [".cq/logs/20260714-030903-aa2065422bfcb3fa2.md",".cq/logs/20260714-032122-acd6bfff48ecc6611.md",".cq/logs/20260714-032122-a43969b0d13dec49c.md",".cq/logs/20260714-035218-a84c7434f6d908139.md",".cq/logs/20260714-035218-a6f8746b8e0351608.md"]
- rawLogs: [".cq/logs/raw/20260714-030903-aa2065422bfcb3fa2.jsonl",".cq/logs/raw/20260714-032122-acd6bfff48ecc6611.jsonl",".cq/logs/raw/20260714-032122-a43969b0d13dec49c.jsonl",".cq/logs/raw/20260714-035218-a84c7434f6d908139.jsonl",".cq/logs/raw/20260714-035218-a6f8746b8e0351608.jsonl"]

### T92 — done

- createdAt: 2026-07-13T22:28:47.652Z
- updatedAt: 2026-07-14T04:22:03.037Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Enforce and test cross-peer isolation threat model
- description: "Codify Q27: with distinct per-peer psks, a party knowing only peer B's psk can bind/disturb ONLY peer B and can never alter peer A's binding, resequencer, FEC, or scheduling; a party with no valid psk is limited to bounded, capped bootstrap-latency degradation and can neither corrupt a peer's stream nor evict a live peer. Add the adversary-case tests that make the e2e isolation claim concrete at the unit level (forged DATA floods on a bound source, replayed/mutated PROBEs, outer-seq discontinuity storms, FEC garbage — victim-peer stream integrity asserted before/during/after each attack)."
- acceptance: "Threat-model test: frames authenticated under B's psk cannot move A's source->peer binding or inject into A's resequencer; unauthenticated floods cause no cross-peer corruption and no live-peer eviction; a forged PROBE under a WRONG psk from a bound source neither re-binds nor unbinds it. `go test ./internal/bind/...` passes."
- suggestedModel: frontier
- dependsOn: ["T90","T91"]
- ledgerRefs: ["goals:G4"]
- resultCommit: e3c2655
- completion: "Cross-peer isolation threat model (Q27) codified as unit-level adversary cases (test-only, internal/bind/threat_model_test.go): against a source already bound+live on peer A, foreign/wrong-psk PROBEs, replay, mutation, a forged DATA+seq-storm, and a 300-source unauthenticated flood all leave A's binding/resequencer/FEC/liveness intact; unauthenticated floods bind nothing, grow no demux state, and never evict a live peer; a wrong-psk PROBE from a bound source neither re-binds nor unbinds it. Isolation rests on two production guards (demuxInbound bound-source early-return; isProbe D9/D11 trial-decode gate) — both mutation-verified discriminating by BOTH reviewers independently. Sentinel/release-point assertions proven deterministic (DATA/PARITY unauthenticated; reseq discontinuity guard). No production defect found. Unanimous 1-round panel approve; ff-merged as e3c2655."
- sessionLogs: [".cq/logs/20260714-041618-a1036c3748de6eaed.md",".cq/logs/20260714-042139-accf6c05ac3dfa0e1.md",".cq/logs/20260714-042139-a4291b0fb8fad1812.md"]
- rawLogs: [".cq/logs/raw/20260714-041618-a1036c3748de6eaed.jsonl",".cq/logs/raw/20260714-042139-accf6c05ac3dfa0e1.jsonl",".cq/logs/raw/20260714-042139-a4291b0fb8fad1812.jsonl"]

## M26

### T93 — done

- createdAt: 2026-07-13T22:28:58.271Z
- updatedAt: 2026-07-14T05:50:37.108Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Wire per-peer prober sets, schedulers, and virtual endpoints in device.Up
- description: "In internal/device, extend buildScheduler/Up so the concentrator builds per-peer wiring: one prober set + scheduler + stable virtual endpoint per configured peer (each peer's probers/reflector keyed on that peer's effective PSK from the config helper), and programs the Bind's receive demux from authenticated peer bindings. Make bind.ProberFactory per-peer (R72): today the factory returned by buildScheduler (internal/device/device.go:577) closes over the single cfg.PSK; replace it so a prober created for a (peer,path) pair keys on THAT peer's effective PSK, and so a RUNTIME-added path gains a prober per bound peer (and a removed path tears each bound peer's per-(peer,path) prober down) — the runtime path add/remove flow must work while >=2 peers are bound. Report each peer's stable virt endpoint to the engine (A1); map WG peer public keys to bind peer identities so uapiConfig (device.go:706) and the Bind agree on the peer set. Keep the single-peer path structurally identical to today. Keep startHubFailover edge-only and unchanged."
- acceptance: "`go build ./...`; a device-level test brings up a 2-peer concentrator config yielding two peerStates each with its own prober set/scheduler/virt endpoint; a single-peer concentrator config produces exactly one peerState and unchanged wiring; uapiConfig golden output for existing single-peer fixtures is byte-identical. A runtime-path test adds a path while 2 peers are bound and asserts each bound peer gains a prober keyed on its OWN PSK for the new path, and path removal tears down each peer's per-(peer,path) prober; the existing runtime-path suites (internal/bind/runtime_path_test.go, tolerant_membership_test.go) still pass. `go test ./internal/device/...` passes."
- suggestedModel: frontier
- dependsOn: ["T85","T86","T88"]
- ledgerRefs: ["goals:G4"]
- resultCommit: 55889b1
- completion: "Wired per-peer prober sets, schedulers, and stable virtual endpoints into device.Up for the concentrator: device.Up builds per-peer wiring (prober set + scheduler + prober factory keyed on each peer's effective PSK from config.PeerIdentities) and registers each additional concentrator peer via a new bind.Multipath.AddConcentratorPeer before dev.Up; Open builds a per-(peer,path) view of every bound socket for every peer and reconciles each peer's scheduler; the probe/liveness loop drives every peer. Single-peer wiring + uapiConfig golden output are BYTE-IDENTICAL (primary keyed on ids[0].PSK == cfg.PSK). R2 fixed a daemon panic the multi-peer split introduced (deferred AddPath on a 2-peer concentrator crashed on reopen — index-out-of-range) by fanning the deferred prober out to every peer index-aligned with m.defs + a fail-fast Open guard, and added a device-level test proving per-peer PSK isolation on both the prober and reflector planes (kills both wrong-PSK mutants). All mechanisms mutation-verified across 2 rounds; go test -race ./internal/bind/... ./internal/device/... green. startHubFailover edge-only/unchanged; deferred-path fan-out beyond the per-peer prober record remains a later G4 task. Rebased onto current main (gate re-run green) and ff-merged as 55889b1."
- sessionLogs: [".cq/logs/20260714-050646-ab3db2ecbe75a0a06.md",".cq/logs/20260714-053851-aef05f0eb9f10ec63.md",".cq/logs/20260714-052053-ab385aae86467f7e3.md",".cq/logs/20260714-052053-a70da160c8c02fa61.md",".cq/logs/20260714-054935-aeb82bf6766d4a909.md",".cq/logs/20260714-054935-a0b6f7290c160086e.md"]
- rawLogs: [".cq/logs/raw/20260714-050646-ab3db2ecbe75a0a06.jsonl",".cq/logs/raw/20260714-053851-aef05f0eb9f10ec63.jsonl",".cq/logs/raw/20260714-052053-ab385aae86467f7e3.jsonl",".cq/logs/raw/20260714-052053-a70da160c8c02fa61.jsonl",".cq/logs/raw/20260714-054935-aeb82bf6766d4a909.jsonl",".cq/logs/raw/20260714-054935-a0b6f7290c160086e.jsonl"]

### T94 — done

- createdAt: 2026-07-13T22:29:07.891Z
- updatedAt: 2026-07-14T06:21:31.043Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Add per-peer label to /metrics path, resequencer, and FEC series
- description: In internal/device/metrics.go + internal/metrics, add a `peer` label (keyed on the config peer name) to wanbond_path_* and the per-peer resequencer/FEC series, sourced from the per-peer snapshots. Keep single-peer exposition back-compatible (omit the label or emit a stable default) so existing single-peer scrapes/series are unchanged — pick ONE rule and document it in the metrics package comment. Per-path throughput derivation keys its last-sample map by (peer,path) so rates stay correct. Cardinality is bounded by the static peer set.
- acceptance: "metrics adapter unit test: a 2-peer exposition carries distinct `peer` labels on path/resequencer/FEC series attributable to each edge with independent counters and correct per-(peer,path) rates; a single-peer exposition is byte-compatible with today's series. `go test ./internal/device/... ./internal/metrics/...` passes."
- suggestedModel: standard
- dependsOn: ["T93"]
- ledgerRefs: ["goals:G4"]
- resultCommit: ed4b45c
- completion: "Per-peer /metrics label (internal/metrics + internal/device/metrics.go + bind.Multipath.PeerSnapshots): a conditionally-attached `peer` label on wanbond_path_*, wanbond_fec_*, and NEW wanbond_resequencer_* series, decided ONCE at NewCollector from Source.PeerNames() — single-peer OMITS the label (byte-compatible with today's series), 2+ peers include it (cardinality bounded by the static peer set). PeerSnapshots() generalizes the primary-only PathSnapshots/FECSnapshot to per-peer path+FEC+resequencer snapshots; the throughput last-sample map is keyed by (peer,path) so per-edge rates stay correct even with same-named paths. Back-compat rule documented in the metrics package comment; runbook synced. Single-peer byte-compatibility independently verified (base-vs-HEAD exposition diff) + mutation-proofed by BOTH reviewers. Unanimous 1-round panel approve; rebased onto current main (gate re-run green) and ff-merged as ed4b45c. Filed D56 (superseded PathSnapshots/FECSnapshot seams; low)."
- sessionLogs: [".cq/logs/20260714-061244-a1470f0ba95b346dc.md",".cq/logs/20260714-062044-a7f4a8b610cf5aeda.md",".cq/logs/20260714-062044-a31be323657b7a7c5.md"]
- rawLogs: [".cq/logs/raw/20260714-061244-a1470f0ba95b346dc.jsonl",".cq/logs/raw/20260714-062044-a7f4a8b610cf5aeda.jsonl",".cq/logs/raw/20260714-062044-a31be323657b7a7c5.jsonl"]

## M27

### T95 — done

- createdAt: 2026-07-13T22:29:18.595Z
- updatedAt: 2026-07-14T06:39:37.389Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Add per-peer resequencer unit test for interleaved outer-seq isolation
- description: "Add the focused unit test asserting two peers' independent outer-seq spaces never interleave into one release window (the core D32-class guarantee at unit level): feed two interleaved outer-seq streams and assert each peer's resequencer releases only its own stream in order, with no cross-peer drops/reorders."
- acceptance: "`go test ./internal/bind/... -run PerPeerReseqIsolation` (or equivalent) passes: two interleaved streams stay fully separated; neither peer's resequencer records suspect/late drops caused by the other."
- suggestedModel: standard
- dependsOn: ["T87"]
- ledgerRefs: ["goals:G4"]
- resultCommit: b38581f
- completion: "Per-peer resequencer interleaved-outer-seq isolation pinned (test-only, internal/bind/per_peer_reseq_isolation_test.go): two concentrator peers bound over one shared socket via the production demuxInbound/peerBySource path each receive an out-of-order stream over the SAME overlapping numeric outer-seq (0..5) interleaved between them; each resequencer releases ONLY its own payloads in order with zero cross-peer suspect/late/dup drops — the D32-class guarantee at unit level. Mutation-verified by BOTH reviewers (a shared resequencer swallows peer B's stream). No production defect. Unanimous 1-round panel approve; ff-merged as b38581f."
- sessionLogs: [".cq/logs/20260714-063350-ac9d99da59f5190b5.md",".cq/logs/20260714-063906-af7c84761e871fbd1.md",".cq/logs/20260714-063906-aaafa72796608ad29.md"]
- rawLogs: [".cq/logs/raw/20260714-063350-ac9d99da59f5190b5.jsonl",".cq/logs/raw/20260714-063906-af7c84761e871fbd1.jsonl",".cq/logs/raw/20260714-063906-aaafa72796608ad29.jsonl"]

### T96 — done

- createdAt: 2026-07-13T22:29:22.171Z
- updatedAt: 2026-07-14T06:40:41.004Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Re-run FEC prefix-stability and FEC suite after per-peer FEC split
- description: "Confirm the per-peer fecSend/fecRecv split preserves the Reed-Solomon invariants: run TestKlauspostParityPrefixStableInvariant and the FEC datapath suite, and add a per-peer FEC recovery assertion (one peer's parity never reconstructs into another peer's decoder). Fix any regression surfaced."
- acceptance: "`go test ./internal/... -run FEC` and TestKlauspostParityPrefixStableInvariant pass; a new assertion shows peer A's parity shards never feed peer B's decoder."
- suggestedModel: standard
- dependsOn: ["T87"]
- ledgerRefs: ["goals:G4"]
- resultCommit: 010b7ec
- completion: "Confirmed the per-peer fecSend/fecRecv split (T91/T93) preserves the Reed-Solomon invariants (TestKlauspostParityPrefixStableInvariant + the FEC datapath suite pass unchanged) and added a cross-peer FEC-isolation assertion (test-only, internal/bind/peer_fec_lifecycle_test.go: TestConcentratorFECParityNeverCrossesPeers): two concentrator peers each run an independent fec.Encoder that lands group id 0 (a genuine numeric collision) — peer A's parity recovers ONLY into A's own decoder/resequencer (Recovered +1) while peer B's Recovered stays 0 and B's group-0 stays undisturbed until B's own parity arrives (+ reciprocal). Mutation-verified by BOTH reviewers (parity→primary and parity→cross-peer both redden). No regression. Unanimous 1-round panel approve; rebased past T95 and ff-merged as 010b7ec."
- sessionLogs: [".cq/logs/20260714-063350-a5cce87613f1ea68b.md",".cq/logs/20260714-064007-a0d31254057d2a3a1.md",".cq/logs/20260714-064007-a278df2d0fc736c97.md"]
- rawLogs: [".cq/logs/raw/20260714-063350-a5cce87613f1ea68b.jsonl",".cq/logs/raw/20260714-064007-a0d31254057d2a3a1.jsonl",".cq/logs/raw/20260714-064007-a278df2d0fc736c97.jsonl"]

### T97 — done

- createdAt: 2026-07-13T22:29:36.024Z
- updatedAt: 2026-07-14T06:55:21.178Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Add netns e2e proving 2+ edges to one concentrator stay isolated
- description: "Add a netns e2e (test/e2e, appropriate build tag) with 2+ edges each bonded across its own uplinks to one concentrator: assert each edge's traffic resequences independently; one edge's loss/reorder/restart does not corrupt another edge's stream; return traffic routes to the correct edge; edge A NAT-rebind (source move) recovers via PROBE re-bind while B is unaffected; and a spoofed unbound-source flood degrades only bootstrap latency without cross-peer corruption or live-peer eviction (the threat model, end to end). Scrape the concentrator /metrics and assert per-peer-labeled series for both names. Follow the existing thresholds.go discipline; report-only where absolute numbers apply (M10/Q12)."
- acceptance: "`go test -tags e2e ./test/e2e -run MultiPeer` passes on both e2e hosts (aarch64 + amd64): two edges' inner streams verify independently; killing+restarting edge A leaves edge B's tunnel uninterrupted; per-peer /metrics attribute traffic to the correct edge; the existing single-peer e2e tests still pass unchanged."
- suggestedModel: frontier
- dependsOn: ["T92","T93","T94"]
- ledgerRefs: ["goals:G4"]
- resultCommit: 4b912e5
- completion: "Multi-peer concentrator isolation netns e2e (test-only, test/e2e/multipeer_test.go: TestMultiPeerConcentratorIsolation): one concentrator + two edges each bonded across its own uplinks. Proves end-to-end the G4 threat model: independent per-edge inner streams; edge-A kill+restart leaves edge-B's tunnel uninterrupted (asserted via a transfer spanning the outage); per-peer /metrics attribution (edge A under peer=\"\", edge B under peer=\"edge-beta\" — label mapping verified against T93/T94 wiring); edge-A NAT-rebind recovery via PROBE re-bind with B undisturbed; a spoofed unbound-source flood evicts no live peer. Isolation properties Fatalf; absolute numbers report-only. Compiles/vets under -tags e2e; PRIVILEGED execution DEFERRED to the o3.7mind.io + llm-ubuntu-0 hosts (G2 pattern). netns.go/thresholds.go untouched; unique port 9102. Unanimous 1-round panel approve; rebased past T95/T96/T103 and ff-merged as 4b912e5."
- sessionLogs: [".cq/logs/20260714-064317-af9870e926cd7a191.md",".cq/logs/20260714-065448-a43bd9a8d80ec37ce.md",".cq/logs/20260714-065448-a377c32726def949b.md"]
- rawLogs: [".cq/logs/raw/20260714-064317-af9870e926cd7a191.jsonl",".cq/logs/raw/20260714-065448-a43bd9a8d80ec37ce.jsonl",".cq/logs/raw/20260714-065448-a377c32726def949b.jsonl"]

### T98 — done

- createdAt: 2026-07-13T22:29:48.172Z
- updatedAt: 2026-07-14T07:54:17.544Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Sync AGENTS.md, design/install/README docs and example config for multi-peer
- description: "Update docs in the same feature change (per AGENTS.md doc-sync rule): AGENTS.md invariants (A1 now literally one-virtual-endpoint-per-peer), docs/design.md (per-peer PSK enabler, authenticated path->peer demux, bootstrap/roaming/limits, threat model), docs/install.md + README.md (multi-peer concentrator operation, per-peer psk/name, single-peer back-compat), and wanbond.example.toml (a commented multi-peer stanza with per-peer psk+name). Explicitly document that the plural [[wireguard.peers]] concentrator schema is now supported."
- acceptance: Docs describe the per-peer model, the demux/threat model, and the single-peer back-compat rule; wanbond.example.toml contains a working multi-peer example that parses via the config test suite; `go build ./...` and the docs/link checks are unaffected; grep finds no stale claim that the concentrator supports only one peer.
- suggestedModel: fast
- dependsOn: ["T97"]
- ledgerRefs: ["goals:G4"]
- resultCommit: d960979
- completion: "Docs-sync for the shipped G4 multi-peer concentrator (AGENTS.md, README.md, docs/design.md, docs/install.md, wanbond.example.toml) + an extended config test. Documents the per-peer model, authenticated peerBySource demux + threat model, single-peer back-compat, and the plural [[wireguard.peers]] concentrator schema — every claim grounded in shipped code. TestExampleConfigLoads extended with a multi_peer_concentrator subtest that extracts+config.Load()s the real commented multi-peer stanza from wanbond.example.toml (two distinct per-peer PSKs/names; mutation-verified), satisfying the 'parses via the config test suite' acceptance. 3-round criticism loop resolved: R1 missing test coverage + 4 doc-vs-source contradictions (single-peer psk rejected-not-defaulted; top-level psk authenticates no peer in multi-peer; only additional peers named in metrics; 'virtual endpoint' A1-reserved); R2→R3 corrected an inverted multi-peer DATA-spam DoS claim against demuxInbound (unbound source dropped pre-dispatch; only a spoofed BOUND source reaches reseq/FEC). Unanimous R3 approve. Rebased over T79/T99/T108 (resolved a wanbond.example.toml conflict, gate re-run green) and ff-merged as d960979. Filed D57 (stale config.go Peer.PSK comment) + D58 (primary peer name dropped from metrics label)."
- sessionLogs: [".cq/logs/20260714-070917-a436e61b729c26ad8.md",".cq/logs/20260714-073515-aeb3c04557caa5a8c.md",".cq/logs/20260714-074711-a1f5adaf73e092e1c.md",".cq/logs/20260714-075343-a64dedbc8a4d965d1.md",".cq/logs/20260714-075343-a4e53784a3d3e81cb.md"]
- rawLogs: [".cq/logs/raw/20260714-070917-a436e61b729c26ad8.jsonl",".cq/logs/raw/20260714-073515-aeb3c04557caa5a8c.jsonl",".cq/logs/raw/20260714-074711-a1f5adaf73e092e1c.jsonl",".cq/logs/raw/20260714-075343-a64dedbc8a4d965d1.jsonl",".cq/logs/raw/20260714-075343-a4e53784a3d3e81cb.jsonl"]

### T99 — done

- createdAt: 2026-07-13T22:29:52.474Z
- updatedAt: 2026-07-14T07:23:09.968Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Run full suite and capture report-only 2-edge real-link check
- description: Run the full go test suite green, and (per the M10/Q12 report-only discipline) capture a 2-edge realhosts run against the o3.7mind.io + llm-ubuntu-0 hosts if infra is available — absolute numbers report-only, asserting only per-peer isolation qualitatively (each edge healthy independently). If the two-host inventory cannot realize two genuinely distinct edges plus a concentrator, document precisely WHY (host/network topology constraint) and what inventory would suffice, instead of forcing a degenerate setup.
- acceptance: "`go test ./...` is green; EITHER a report-only 2-edge real-link run is captured (per-peer isolation observed; numbers report-only) OR the deferral/infeasibility is documented with the concrete topology constraint and required inventory."
- suggestedModel: standard
- dependsOn: ["T97"]
- ledgerRefs: ["goals:G4"]
- resultCommit: 6e41f4a
- completion: "Full go test ./... suite green (mandatory acceptance half). The 2-edge report-only realhosts capture taken as a DOCUMENTED DEFERRAL per M10/Q12: the standing 2-host inventory (o3.7mind.io + llm-ubuntu-0, each single-NIC; o3 runs a live shared concentrator) exposes only two network vantage points, but a genuine 2-edge+concentrator isolation capture needs three (per T97's netns topology). docs/drafts/20260714-0705-t99-2edge-realhosts-deferral.md records the precise topology constraint and the required inventory (a third independently-networked edge host, WANBOND_EDGE2_HOST). Grounded against test/realhosts/runner.go + multipath_failover_test.go + test/e2e/multipeer_test.go and independently reproduced by a reviewer via read-only SSH. Unanimous panel approve; rebased and ff-merged as 6e41f4a."
- sessionLogs: [".cq/logs/20260714-070917-abf23ed9fff393fef.md",".cq/logs/20260714-071939-a959ee680c6a5baab.md",".cq/logs/20260714-071939-a9dd699b4c0bc00c8.md"]
- rawLogs: [".cq/logs/raw/20260714-070917-abf23ed9fff393fef.jsonl",".cq/logs/raw/20260714-071939-a959ee680c6a5baab.jsonl",".cq/logs/raw/20260714-071939-a9dd699b4c0bc00c8.jsonl"]

## M30

### T100 — done

- createdAt: 2026-07-13T23:22:25.158Z
- updatedAt: 2026-07-14T03:08:22.199Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Bring the wanbond0 link UP after TUN creation (I1)
- description: In device.Up (internal/device/device.go), after the TUN is created, set IFF_UP on the interface (SIOCSIFFLAGS ioctl via golang.org/x/sys — no new dependency; the repo already targets Linux, see internal/bind/pathsock_linux.go). Addressing stays operator-owned (do NOT assign addresses). Teardown behavior unchanged. Log an INFO 'interface up' with the interface name. Removes the silent-dead-tunnel footgun where writes to a DOWN tun yield EIO (relates D39/NM flush; not a duplicate of the D39 fix).
- acceptance: New netns e2e test (go test -tags e2e ./test/e2e) asserts wanbond0 reports UP immediately after device.Up on both roles with NO external `ip link set up`, and that the daemon assigns no address. go test ./... green; no regression in existing device/reload tests.
- suggestedModel: standard
- ledgerRefs: ["goals:G6"]
- resultCommit: f3b6a6f
- completion: "device.Up sets IFF_UP on wanbond0 after TUN creation via SIOCGIFFLAGS/SIOCSIFFLAGS read-modify-write (golang.org/x/sys/unix; new linkup_linux.go + !linux stub mirroring pathsock_{linux,other}.go), in the production-only Up() wrapper (not the fake-TUN up() unit seam), logging INFO 'interface up'. Addressing stays operator-owned; teardown unchanged. New -tags e2e test test/e2e/link_up_test.go asserts UP-without-external-ip-link + no daemon address on both roles; compiles/vets under -tags e2e but privileged netns execution DEFERRED (hardware, G2 pattern) — must be hardware-validated before the e2e acceptance clause is fully closed. Unanimous 1-round panel approve; rebased past T90 and ff-merged to main as f3b6a6f; docs/install.md synced. [fable]'s lint-at-base defect is a duplicate of open D45."
- sessionLogs: [".cq/logs/20260714-025553-a3d95aaa6b922d19a.md",".cq/logs/20260714-030756-aa7c68662525d4b3f.md",".cq/logs/20260714-030756-a9196a9bc3bed8ec8.md"]
- rawLogs: [".cq/logs/raw/20260714-025553-a3d95aaa6b922d19a.jsonl",".cq/logs/raw/20260714-030756-aa7c68662525d4b3f.jsonl",".cq/logs/raw/20260714-030756-a9196a9bc3bed8ec8.jsonl"]

### T101 — done

- createdAt: 2026-07-13T23:22:37.650Z
- updatedAt: 2026-07-14T03:57:42.202Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Add wanbond_session_established metric, last-handshake age, and a 'session established' log line (I2)
- description: "Add a WG-session signal to the metrics plane: a wanbond_session_established gauge (0/1) and wanbond_session_last_handshake_seconds (age), sourced at scrape time from the amneziawg engine (IpcGet last_handshake_time_sec, or peer lookup as deviceRehandshake in internal/device/failover.go:181 already does). Extend the metrics.Source seam (internal/metrics/metrics.go) with a session snapshot supplied by the device layer; the bind stays WG-unaware. Emit ONE INFO 'session established' log record on the 0→1 transition (poll at probe cadence or scrape-driven with a device-side edge detector). This is the signal that distinguishes 'still converging' from 'wedged' — D35/D36/D37 all presented identically without it."
- acceptance: Unit tests cover metric registration and the 0→1 edge; netns e2e asserts wanbond_session_established transitions 0→1 after tunnel up (scraped via metrics.Fetch) and that the path-up-before-session-established ordering holds by comparing the path-up and session-established transition timestamps recorded in the logs — NOT by observing a path_up=1/session=0 intermediate scrape, which the netns tier reaches within milliseconds and a scrape-cadence observer would nondeterministically miss (the ~25 s gap is a production/WAN artifact); the 'session established' record appears exactly once per session; go test ./... and -tags e2e suite green.
- suggestedModel: frontier
- ledgerRefs: ["goals:G6"]
- resultCommit: 1957f21
- completion: "Added a WG-session signal to the metrics plane: wanbond_session_established gauge (0/1) + wanbond_session_last_handshake_seconds age, via a new metrics.Source SessionSnapshot seam supplied by the device layer (internal/device/session.go sessionMonitor reads the amneziawg peer last-handshake via IpcGet UAPI-text at scrape time — bind stays WG-unaware). A probe-cadence poll emits ONE INFO 'session established' log per 0→1 edge (mutation-verified once-per-session; re-arms on a new session). Distinguishes 'still converging' from 'wedged' (the D35/D36/D37 blind spot). Unit tests cover registration, exposition, UAPI parse, never-handshaked/aged-out, and the mutation-killed edge; deferred netns e2e asserts scrape 0→1 + path-up-before-session log-timestamp ordering (compiles/vets under -tags e2e, execution deferred G2). R2 fixed an e2e metrics-port collision. docs/runbook.md synced. Rebased onto current main and ff-merged as 1957f21. Filed D51 (pre-existing pacing/p3 9096 collision)."
- sessionLogs: [".cq/logs/20260714-034520-a0d7a77e637809a31.md",".cq/logs/20260714-035334-a5d713271e5cc337d.md",".cq/logs/20260714-035711-af7fe1e7fcf138c03.md",".cq/logs/20260714-035711-ac7254fc378c0095a.md",".cq/logs/20260714-035711-a1a5495814cda5845.md",".cq/logs/20260714-035711-ac04c993630a8da43.md"]
- rawLogs: [".cq/logs/raw/20260714-034520-a0d7a77e637809a31.jsonl",".cq/logs/raw/20260714-035334-a5d713271e5cc337d.jsonl",".cq/logs/raw/20260714-035711-af7fe1e7fcf138c03.jsonl",".cq/logs/raw/20260714-035711-ac7254fc378c0095a.jsonl",".cq/logs/raw/20260714-035711-a1a5495814cda5845.jsonl",".cq/logs/raw/20260714-035711-ac04c993630a8da43.jsonl"]

### T102 — done

- createdAt: 2026-07-13T23:22:48.514Z
- updatedAt: 2026-07-14T06:12:19.229Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Emit an actionable diagnostic on TUN write EIO instead of the raw input/output error (I3)
- description: Where the TUN write error surfaces (the engine's tun read/write loop errors reach engineLogger in internal/device/device.go ~L687-697, and/or wrap tun.Device with a thin decorator), detect EIO, inspect the interface state (IFF_UP flags, MTU) and emit an actionable ERROR naming the probable cause and remedy, e.g. 'wanbond0 is DOWN — address & bring it up (install.md §4)'. Rate-limit so a write storm produces one diagnostic, not a flood. Keep the raw errno in the record for debugging.
- acceptance: Unit test injecting EIO against a fake/DOWN TUN asserts the log record names the interface state and points at install.md §4, with the raw errno included, and that a burst of EIOs yields one rate-limited diagnostic. Relates D39 (diagnoses the D39 symptom) with no dependsOn on its fix. go test ./... green.
- suggestedModel: standard
- ledgerRefs: ["goals:G6"]
- resultCommit: 890ab43
- completion: "Actionable TUN-write-EIO diagnostic (I3): a diagnosingTUN tun.Device decorator (internal/device/tundiag.go) wraps the engine's TUN in up() so every Write is diagnosed — on syscall.EIO (errors.Is) it inspects the interface IFF_UP/MTU via a new read-only ifState ioctl (linkup_linux.go, mirroring T100's ifUp; !linux stub) and logs ONE rate-limited (30s sliding window) actionable ERROR naming the interface state (DOWN/UP/unknown, probe-driven) + pointing at install.md §4 + the raw numeric errno, while returning the original (n,err) UNCHANGED (transparent). The ioctl is gated behind the rate limiter (no ioctl-storm). Non-EIO errors pass through undiagnosed. Diagnoses the D39 symptom (silent EIO on a DOWN/unaddressed wanbond0) without depending on its fix. Unit tests mutation-verified by BOTH reviewers (4/4 mutants killed: unthrottled, latch-once, always-DOWN, any-error). docs/install.md §4 synced. Unanimous 1-round panel approve; rebased onto current main (device.go/install.md overlap with T107 resolved, gate re-run green) and ff-merged as 890ab43."
- sessionLogs: [".cq/logs/20260714-060518-a116fca10b59541c0.md",".cq/logs/20260714-061135-a8a45233ae279d4cd.md",".cq/logs/20260714-061135-a0b85a40cc20e154c.md"]
- rawLogs: [".cq/logs/raw/20260714-060518-a116fca10b59541c0.jsonl",".cq/logs/raw/20260714-061135-a8a45233ae279d4cd.jsonl",".cq/logs/raw/20260714-061135-a0b85a40cc20e154c.jsonl"]

### T103 — done

- createdAt: 2026-07-13T23:22:58.901Z
- updatedAt: 2026-07-14T06:42:42.084Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Downgrade the startup no-healthy-path ERROR spam during the liveness warmup (I4)
- description: "The engine's 'Failed to send handshake initiation: bind: no healthy path with a known remote endpoint' reaches the operator as ERROR via engineLogger (internal/device/device.go:687) wrapping errNoHealthyPath (internal/bind/multipath.go:64). Add a warmup-aware seam: until the FIRST path reaches liveness UP, surface these as a single coalesced INFO 'waiting for path liveness' line; after first path-up they stay ERROR (a genuine outage signal). Implementation choice: expose a bind-level 'ever had a live path' predicate the engine-logger adapter consults, or filter on the errNoHealthyPath sentinel during the warmup window."
- acceptance: "Unit test: no-healthy-path records before first path-up yield exactly one INFO 'waiting for path liveness' and zero ERRORs; the same record after a path has been up logs at ERROR. Relates D37 (the wasted-first-init defect stays investigate-flow-owned; this only fixes log severity). go test ./... green; no spurious ERROR on a normal start in the netns e2e logs."
- suggestedModel: standard
- ledgerRefs: ["goals:G6"]
- resultCommit: 445c332
- completion: "Startup no-healthy-path ERROR spam downgraded to a coalesced warmup INFO (I4): exported bind.ErrNoHealthyPath + a sticky race-free Multipath.EverHadLivePath() latch (atomic.Bool set at the sole Down→Up site — dispatchInbound's HandleEcho echo branch). engineLogger consults it: before the FIRST path-up it coalesces every ErrNoHealthyPath-wrapping Errorf into exactly ONE INFO 'waiting for path liveness' (via a warmupInfoLogged atomic CAS; detection is errors.Is on the Errorf args vs the sentinel, robust to wording); after first path-up the same error logs ERROR. Unrelated engine errors still log at their normal level. Diagnoses the D37 symptom (wasted first-init spam) at log-severity level only — the wasted-first-init defect stays investigate-flow-owned. All mechanisms mutation-verified by BOTH reviewers (gate/once-latch/never-set-latch). -race + all-tags gate green. Unanimous 1-round panel approve; rebased past T95/T96 and ff-merged as 445c332."
- sessionLogs: [".cq/logs/20260714-063350-ac4b4116995048189.md",".cq/logs/20260714-064150-a384714613befbd9b.md",".cq/logs/20260714-064150-a6746d623c2e7e8c7.md"]
- rawLogs: [".cq/logs/raw/20260714-063350-ac4b4116995048189.jsonl",".cq/logs/raw/20260714-064150-a384714613befbd9b.jsonl",".cq/logs/raw/20260714-064150-a6746d623c2e7e8c7.jsonl"]

### T104 — done

- createdAt: 2026-07-13T23:23:09.581Z
- updatedAt: 2026-07-14T03:19:43.063Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: "Netns verification: standby-path liveness is bidirectional (I8)"
- description: "Per Q39, an in-goal verification task (not a refactor): add a netns e2e test that (a) asserts an idle 'up' standby path actually TRANSMITS — its tx byte counter (wanbond_path_tx_bytes_total / PathSnapshots) grows from probe emission while the primary carries data (the production observation was path_up{5g}=1 with tx{5g}=0); and (b) with the standby's EGRESS direction blocked one-way (nft/iptables drop in the netns fixture), asserts the standby transitions DOWN and is not selected by failover. If either check exposes a real fault (liveness proving only receive), commit the failing test as the repro and refile the finding as a defect linked to G6 — the fix is then out of this goal's scope."
- acceptance: "New -tags e2e test exists and runs in the netns tier: passes proving bidirectional liveness, OR fails with the failure documented and refiled as a defects item linked goals:G6 capturing the reproduction (test kept as repro). Either outcome satisfies the task per Q39."
- suggestedModel: standard
- ledgerRefs: ["goals:G6"]
- resultCommit: f9b2adb
- completion: "Netns standby-liveness verification landed (test-only, test/e2e/standby_liveness_test.go: TestStandbyLivenessBidirectional, 2 subtests; + Topology.BlockEgress/UnblockEgress one-way tc-clsact helper in netns.go). Per Q39's either-outcome acceptance the task is satisfied: the test is well-formed (compiles/vets under -tags e2e, matches harness idioms, BlockEgress empirically validated by a reviewer in a netns replica) and both reviewers source-confirmed subtest (a) will FAIL against current code — that failure is the reproduction of the tx-accounting defect, filed as D48 (goals:G6) with this subtest as the kept repro. Subtest (b) (egress-dead standby transitions DOWN, not selected) is source-consistent to PASS (liveness genuinely bidirectional). Privileged netns EXECUTION deferred to hardware (G2 pattern) — the hardware run will bind subtest (a) to D48 and, once D48 is fixed, flip it green. Unanimous 1-round panel approve; rebased past T75 and ff-merged as f9b2adb."
- sessionLogs: [".cq/logs/20260714-031845-accde2d95d6ea72dc.md",".cq/logs/20260714-031845-a6d96b8f39ee0fc04.md",".cq/logs/20260714-031845-ae358e9b638958305.md"]
- rawLogs: [".cq/logs/raw/20260714-031845-accde2d95d6ea72dc.jsonl",".cq/logs/raw/20260714-031845-a6d96b8f39ee0fc04.jsonl",".cq/logs/raw/20260714-031845-ae358e9b638958305.jsonl"]

## M31

### T105 — done

- createdAt: 2026-07-13T23:23:19.962Z
- updatedAt: 2026-07-14T03:41:12.354Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: "Config surface: per-path bind mode with optional global default (I5 config)"
- description: "Per Q42: add `bind = \"source\"|\"device\"|\"auto\"` to each [[paths]] block (internal/config/config.go Path struct) plus an optional top-level global default; per-path overrides global; default is `auto` (today's selectDeviceBinds behavior). config.validate rejects unknown values and normalizes empty to the global/auto default. Plain TOML — no versioning cost."
- acceptance: "internal/config/config_test.go covers: default auto when omitted, per-path override beats global, unknown value fails fast at load with a message naming the path. go test ./... green."
- suggestedModel: standard
- ledgerRefs: ["goals:G6"]
- resultCommit: 3ba47e0
- completion: "Per-path bind-mode config surface landed (internal/config/{config.go,config_test.go}): BindMode enum (\"source\"|\"device\"|\"auto\") on config.Config (top-level `bind` global default) and config.Path (per-path override). normalize() resolves precedence path>global>auto (empty global → auto first, then empty path → global); validate() rejects unknown values on both surfaces and names the offending path; fail-fast at Load. Surface-only — selectDeviceBinds/planPathBinds consumption UNCHANGED (default auto == today's behavior), verified by grep. Unanimous 1-round panel approve; ff-merged to main as 3ba47e0. Doc-sync deferred to T115 (dependsOn T105)."
- sessionLogs: [".cq/logs/20260714-033617-a8066d8a362952bd2.md",".cq/logs/20260714-034050-ad3878949437704f2.md",".cq/logs/20260714-034050-a88c682fa1f564cce.md"]
- rawLogs: [".cq/logs/raw/20260714-033617-a8066d8a362952bd2.jsonl",".cq/logs/raw/20260714-034050-ad3878949437704f2.jsonl",".cq/logs/raw/20260714-034050-a88c682fa1f564cce.jsonl"]

### T106 — done

- createdAt: 2026-07-13T23:23:31.897Z
- updatedAt: 2026-07-14T04:44:46.736Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Honor the bind mode in path-socket planning (I5 wiring)
- description: "Thread the resolved per-path bind mode into planPathBinds/selectDeviceBinds (internal/bind/pathsock.go): `source` forces source-IP pinning (never SO_BINDTODEVICE — the D38 topology's escape hatch), `device` forces device-bind for the path's interface (falling back to source with a WARN when the interface cannot be resolved or the setsockopt fails, matching the existing CAP fallback), `auto` keeps the current one-address-one-path heuristic byte-for-byte. Applies to Open, AddPath, and the deferred-path reconcile alike."
- acceptance: Unit tests on selectDeviceBinds/planPathBinds cover all three modes including the forced-source case on a one-address interface (which auto would device-bind — the exact D38 trap) and the device-mode fallback path; auto mode reproduces the current planPathBinds output on the existing fixtures. Relates D38 without depending on its fix. go test ./... green.
- suggestedModel: standard
- dependsOn: ["T105"]
- ledgerRefs: ["goals:G6"]
- resultCommit: cb6547e
- completion: Threaded the resolved per-path config.BindMode into internal/bind/pathsock.go's selectDeviceBinds/planPathBinds (source forces source-IP pin, device forces device-bind with fallback-to-source on an unresolvable interface, auto reproduces the pre-I5 heuristic byte-for-byte) and wired it into Open, AddPath, and the T55 deferred-path reconcile. To make the runtime paths (AddPath + reconcile) mutation-provable, refactored resolveForcedDeviceBind into a pure selectForcedDeviceBind decision + a thin real-interfaces wrapper, and added TWO injection seams on Multipath — resolveDeviceBind and addPathListen — so both AddPath and reconcileDeferred thread the resolved dev through env-independently testable seams (TestReconcileThreadsForcedDeviceBind + TestAddPathThreadsForcedDeviceBind, both mutation-verified). Also opportunistically activated forced-device on the runtime AddPath/reconcile paths (relates D30; source/auto behavior there unchanged). 3 review rounds (R1/R2 disapprove drove the coverage-completion of the reconcile then AddPath wiring). auto verified byte-for-byte; go test -race ./internal/bind/... green; rebased onto current main (gate re-run green) and ff-merged as cb6547e. Device-fallback WARN observability deferred to D53.
- sessionLogs: [".cq/logs/20260714-041243-a45dbb6e85fe5dcff.md",".cq/logs/20260714-042958-a1e4feeb5771405f2.md",".cq/logs/20260714-043959-aeea22edfc42b0b3e.md",".cq/logs/20260714-041808-ae0780df317aa2c57.md",".cq/logs/20260714-041808-aa9c60cbe80cacf55.md",".cq/logs/20260714-043501-a9441e152c838dd6c.md",".cq/logs/20260714-043501-a5ef1e10bfec6b3bf.md",".cq/logs/20260714-044354-a039c99963c717f61.md",".cq/logs/20260714-044354-a439e530d4d66f5bc.md"]
- rawLogs: [".cq/logs/raw/20260714-041243-a45dbb6e85fe5dcff.jsonl",".cq/logs/raw/20260714-042958-a1e4feeb5771405f2.jsonl",".cq/logs/raw/20260714-043959-aeea22edfc42b0b3e.jsonl",".cq/logs/raw/20260714-041808-ae0780df317aa2c57.jsonl",".cq/logs/raw/20260714-041808-aa9c60cbe80cacf55.jsonl",".cq/logs/raw/20260714-043501-a9441e152c838dd6c.jsonl",".cq/logs/raw/20260714-043501-a5ef1e10bfec6b3bf.jsonl",".cq/logs/raw/20260714-044354-a039c99963c717f61.jsonl",".cq/logs/raw/20260714-044354-a439e530d4d66f5bc.jsonl"]

### T107 — done

- createdAt: 2026-07-13T23:23:41.640Z
- updatedAt: 2026-07-14T06:07:59.866Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: "Full-tunnel config: accept 0.0.0.0/0 allowed_ips via internal /1+/1 split at UAPI render (I6 config)"
- description: "Per Q41 (thin I6): add an edge-only peer/full-tunnel surface — `mode = \"default-route\"` on the peer (or equivalent) — and make the UAPI renderer (uapiConfig, internal/device/device.go) translate a configured `0.0.0.0/0` (and `::/0`) allowed_ips into the split `0.0.0.0/1 + 128.0.0.0/1` (`::/1 + 8000::/1`) internally, so the engine NEVER receives the literal /0 prefix that wedges the handshake. Config validation: mode is edge-only (rejected on the concentrator, mirroring the existing endpoint rules), and `mode = \"default-route\"` implies/permits the full-tunnel allowed_ips."
- acceptance: "Unit test on the UAPI set string: a 0.0.0.0/0 config renders exactly the two /1 prefixes and never the literal /0; concentrator-role configs with the mode are rejected at load. Passing the literal /0 THROUGH to the engine unsplit remains gated on defect D35's root cause (acceptance reference only — no dependsOn; the split sidesteps the D35 wedge deterministically per the production bisect). go test ./... green."
- suggestedModel: standard
- ledgerRefs: ["goals:G6"]
- resultCommit: e958035
- completion: "Full-tunnel /1+/1 split at UAPI render (I6, D35 sidestep): uapiConfig (internal/device/device.go) now unconditionally translates a configured literal 0.0.0.0/0 into 0.0.0.0/1 + 128.0.0.0/1 (and ::/0 into ::/1 + 8000::/1) via splitDefaultRoute, so the engine never receives the literal /0 that wedges the handshake per the D35 production bisect. Non-/0 prefixes pass through unchanged. Added an edge-only Peer.Mode=\"default-route\" config marker (PeerMode type + validation), rejected on the concentrator role and for unknown values, fail-fast at Load. Split mutation-verified by BOTH reviewers; unconditional split is routing-equivalent to /0 under longest-prefix-match (strictly safer than mode-gating). docs synced (wanbond.example.toml + install.md); go test ./... green. Unanimous 1-round panel approve; ff-merged as e958035. OS-level default-route/SNAT wiring for the mode is a separate task (T108). Filed D55 (allowed_ips CIDR syntax unvalidated at load)."
- sessionLogs: [".cq/logs/20260714-060333-a41c6d5823c2af08c.md",".cq/logs/20260714-060726-a48e2a04e50fbf112.md",".cq/logs/20260714-060726-af9cfffd1ceb8b455.md"]
- rawLogs: [".cq/logs/raw/20260714-060333-a41c6d5823c2af08c.jsonl",".cq/logs/raw/20260714-060726-a48e2a04e50fbf112.jsonl",".cq/logs/raw/20260714-060726-af9cfffd1ceb8b455.jsonl"]

### T108 — done

- createdAt: 2026-07-13T23:23:50.937Z
- updatedAt: 2026-07-14T07:47:35.089Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Edge default-route wiring under mode=default-route (I6 routes)
- description: "When `mode = \"default-route\"` is active on the edge, after the interface is UP the daemon installs the default-route wiring into wanbond0 — the two /1 routes (wg-quick style, matching the internal allowed_ips split) — and removes them on Close. STRICT Q41 boundary: NO client-LAN policy routing, NO SNAT, NO concentrator ip_forward/MASQUERADE/FORWARD programming — those stay documented C3/C6 recipes. This is the daemon's first route programming (install.md §4 today states it never assigns routes) — keep it minimal, fail-fast, and confined to the mode being explicitly enabled; default behavior without the mode is byte-for-byte unchanged."
- acceptance: "Netns e2e: with mode=default-route the edge's two /1 routes via wanbond0 exist while the daemon runs and are gone after Close; traffic to an arbitrary destination egresses through the tunnel; WITHOUT the mode, no route is ever installed (regression guard). go test -tags e2e ./test/e2e green."
- suggestedModel: frontier
- dependsOn: ["T107","T100"]
- ledgerRefs: ["goals:G6"]
- resultCommit: 8bb24a9
- completion: "Edge default-route wiring under mode=default-route (G6/I6). internal/device/route_linux.go programs the wg-quick-style split default route (two /1s of the peer's allowed_ips, reusing splitDefaultRoute) into wanbond0 via rtnetlink after dev.Up(), withdraws on Close; route_other.go non-Linux stub. Idempotent (NLM_F_CREATE|NLM_F_REPLACE — adopts a leftover route on restart-after-unclean-death under tun_persist rather than wedging EEXIST) with best-effort partial-install cleanup on the up() error path. STRICT Q41: scope-link device routes only, no policy routing/SNAT/forwarding; no default-route peer → no socket, no route, byte-for-byte unchanged. TestDefaultRoutePrefixes + TestRouteMsgFlags unit tests; test/e2e/default_route_test.go netns e2e (compile+vet only in sandbox; PRIVILEGED exec DEFERRED to o3+llm-ubuntu-0). 2-round panel (R1 split → R2 unanimous approve after fixing an EEXIST bring-up-loop + a partial-install leak). Rebased over T79/T99 and ff-merged as 8bb24a9. Config gap (multiple default-route peers) filed D59."
- sessionLogs: [".cq/logs/20260714-073515-aa43ce28a2ab2fa2f.md",".cq/logs/20260714-074804-a95d32452b71677e1.md",".cq/logs/20260714-074804-aaac034e84e494ebc.md"]
- rawLogs: [".cq/logs/raw/20260714-073515-aa43ce28a2ab2fa2f.jsonl",".cq/logs/raw/20260714-074804-a95d32452b71677e1.jsonl",".cq/logs/raw/20260714-074804-aaac034e84e494ebc.jsonl"]

## M32

### T109 — done

- createdAt: 2026-07-13T23:24:00.782Z
- updatedAt: 2026-07-14T04:07:18.983Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Persistent wanbond0 TUN across daemon restarts (I7 code)
- description: "Per Q38 (belt-and-suspenders, code half): make wanbond0 survive daemon restarts so addresses/routes/rules referencing it are not dropped on every restart. Opt-in config key (e.g. top-level `tun_persist = true`, default false so existing teardown semantics are unchanged): on start, set TUNSETPERSIST (or adopt an already-existing persistent device by name); on Close, leave the persistent device in place (link stays, session state torn down). Document the interaction with D39/NM (a persistent device still needs the unmanaged-devices drop-in on NM hosts) in the key's reference entry. Beware the single-engine guard and reload paths (internal/device/device.go) — persistence must not break SIGHUP reload or the restart-on-failure supervisor flow."
- acceptance: "Netns e2e: with tun_persist=true, an address assigned to wanbond0 survives a full daemon stop/start (the D5/I7 production failure mode) and the interface keeps the SAME ifindex across the restart; the persistent device does not become NM-managed (documented invariant; asserted where the fixture permits). With the default false, behavior is unchanged (device disappears on Close, existing e2e suite green). Relates D39 in acceptance only. go test ./... and -tags e2e green."
- suggestedModel: frontier
- ledgerRefs: ["goals:G6"]
- resultCommit: cf3f341
- completion: "Opt-in persistent wanbond0 TUN across daemon restarts (top-level tun_persist, default false). On device.Up (after T100's ifUp, before the amnezia single-engine guard) the daemon issues TUNSETPERSIST via setTUNPersist (tun.Device.File().SyscallConn().Control — avoids racing netpoll; persist_linux.go + !linux stub), called UNCONDITIONALLY so false clears the flag. Close unchanged (amneziawg-go v1.0.4 NativeTun.Close never RTM_DELLINKs) → a persistent wanbond0 outlives Close and the next Up re-adopts it by name preserving ifindex, so addresses/routes/rules survive a full daemon restart (D5/I7). R2 fixed a SIGHUP-reload gap: reloadWarnings now warns on a tun_persist flip (mutation-verified). Config units cover default-false + opt-in; deferred netns e2e (tun_persist_test.go) asserts address+ifindex survival + default-false teardown (compiles/vets under -tags e2e, execution deferred G2). Docs synced (install.md + wanbond.example.toml with the D39/NM caveat). Discarded a stale-based first attempt; re-implemented on correct base; rebased onto current main (clean, full gate re-run green) and ff-merged as cf3f341. Filed D52 (reloadWarnings scheduler/fec/dns/bind gap)."
- sessionLogs: [".cq/logs/20260714-035334-a499fad4ac5e70f46.md",".cq/logs/20260714-040340-a807331bce82874fa.md",".cq/logs/20260714-040058-aa28850d7ae9798c8.md",".cq/logs/20260714-040058-a4363a876d3b56773.md",".cq/logs/20260714-040623-a21407ed6e6df882a.md",".cq/logs/20260714-040623-a64c1d2ea3184af3e.md"]
- rawLogs: [".cq/logs/raw/20260714-035334-a499fad4ac5e70f46.jsonl",".cq/logs/raw/20260714-040340-a807331bce82874fa.jsonl",".cq/logs/raw/20260714-040058-aa28850d7ae9798c8.jsonl",".cq/logs/raw/20260714-040058-a4363a876d3b56773.jsonl",".cq/logs/raw/20260714-040623-a21407ed6e6df882a.jsonl",".cq/logs/raw/20260714-040623-a64c1d2ea3184af3e.jsonl"]

### T110 — done

- createdAt: 2026-07-13T23:24:11.417Z
- updatedAt: 2026-07-14T03:49:03.933Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Ship the NetworkManager unmanaged-devices drop-in + install.md NM section (C1 + Q40 artifact)
- description: "Add packaging/networkmanager/99-wanbond-unmanaged.conf containing `[keyfile] unmanaged-devices=interface-name:wanbond0`, and (coupled, per AGENTS.md docs-with-code) the C1 install.md §4 NetworkManager subsection: today the docs are networkd-only, but most edge boxes (RPi OS/Debian/Ubuntu desktop) run NM, which flushes the operator's address on link-up without this drop-in (the D39/D5 production failure). Docs state the copy destination (/etc/NetworkManager/conf.d/) and the `nmcli device set`/reload verification step."
- acceptance: The drop-in file exists under packaging/ with valid NM keyfile syntax, and a lightweight packaging test/CI check asserts the file's presence and the unmanaged-devices key; install.md §4 gains the NM subsection referencing the shipped file (not a hand-typed inline recipe); validated against an NM host where practical (the production Pi validated the setting itself). Relates D39 in acceptance only.
- suggestedModel: fast
- ledgerRefs: ["goals:G6"]
- resultCommit: 63a3791
- completion: "Shipped packaging/networkmanager/99-wanbond-unmanaged.conf ([keyfile] unmanaged-devices=interface-name:wanbond0) + docs/install.md §4 NM subsection (copy to /etc/NetworkManager/conf.d/, nmcli reload/verify) + a build-time Go check (internal/config/packaging_test.go) asserting the file and the exact directive. Prevents NetworkManager flushing wanbond0's operator address on link-up (D39/D5). 2 review rounds: R2 corrected the install.md skip advice (drop-in required whenever NM is active, even alongside systemd-networkd) and hardened the packaging test from a vacuous substring match to exact-line + [keyfile] assertions (mutation-verified to reject a commented-out directive). Rebased onto current main and ff-merged as 63a3791."
- sessionLogs: [".cq/logs/20260714-033617-a553d90c5a5d0afc1.md",".cq/logs/20260714-034834-a38febff249b322f5.md",".cq/logs/20260714-034834-a8fe10de2e74c5ad8.md",".cq/logs/20260714-034834-a3b74d24cfbd0d5a7.md",".cq/logs/20260714-034834-a03fdf59bb32f668c.md"]
- rawLogs: [".cq/logs/raw/20260714-033617-a553d90c5a5d0afc1.jsonl",".cq/logs/raw/20260714-034834-a38febff249b322f5.jsonl",".cq/logs/raw/20260714-034834-a8fe10de2e74c5ad8.jsonl",".cq/logs/raw/20260714-034834-a3b74d24cfbd0d5a7.jsonl",".cq/logs/raw/20260714-034834-a03fdf59bb32f668c.jsonl"]

### T111 — done

- createdAt: 2026-07-13T23:24:22.830Z
- updatedAt: 2026-07-14T05:05:28.286Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Ship the templated wanbond-addressing oneshot unit + C4 persistence recipe (Q40 artifact + C4)
- description: "Add packaging/systemd/wanbond-addressing@.service: a templated oneshot (instance = role) with `PartOf=wanbond-%i.service`, `After=wanbond-%i.service`, that re-applies address + link-up + policy rules + per-table routes + nft SNAT from an operator-owned environment/script file after the daemon (re)starts. It MUST NOT race tun creation (the R27 lesson: a plain ExecStartPost under Type=exec runs before wanbond0 exists) — wait for the interface (ExecStartPre poll loop or BindsTo/After=sys-subsystem-net-devices-wanbond0.device). Coupled C4 doc section in install.md: the persistence recipe for non-networkd hosts, blessing this unit, explicitly warning that a plain ExecStartPost races tun creation, and noting the oneshot becomes optional-but-harmless once tun_persist is enabled."
- acceptance: systemd-analyze verify passes on the unit (where available in CI/dev shell); the unit orders after interface existence, not just after execve (documented rationale referencing the R27 race); install.md C4 section references the shipped file and carries the race warning + the tun_persist cross-link. go test ./... unaffected.
- suggestedModel: standard
- ledgerRefs: ["goals:G6"]
- dependsOn: ["T109"]
- resultCommit: f3a59f8
- completion: "Shipped packaging/systemd/wanbond-addressing@.service: a templated oneshot (instance=role, Type=oneshot+RemainAfterExit) that is PartOf=/After=/WantedBy=wanbond-%i.service and re-applies the operator's address/link-up/policy-rules/routes/nft-SNAT script after the daemon (re)starts. It orders after wanbond0's actual EXISTENCE via a bounded ExecStartPre poll on /sys/class/net/wanbond0 (30s loop under TimeoutStartSec=45s), NOT merely after execve — avoiding the R27 ExecStartPost-races-tun-creation failure. C4 recipe added to docs/install.md §4 (shipped-file reference + R27 race warning + tun_persist cross-link noting the unit becomes optional-but-harmless once tun_persist is on) + a CI-guarded shape test in internal/config/packaging_test.go (mutation-verified non-vacuous: 4/4 mutations fail). systemd-analyze verify exit 0 (stub-path copy). Unanimous 1-round panel approve; ff-merged as f3a59f8."
- sessionLogs: [".cq/logs/20260714-045305-a0f56065089a0c206.md",".cq/logs/20260714-050504-a680275f9573ffec1.md",".cq/logs/20260714-050504-af7c64d9b56c69fc6.md"]
- rawLogs: [".cq/logs/raw/20260714-045305-a0f56065089a0c206.jsonl",".cq/logs/raw/20260714-050504-a680275f9573ffec1.jsonl",".cq/logs/raw/20260714-050504-af7c64d9b56c69fc6.jsonl"]

## M33

### T112 — done

- createdAt: 2026-07-13T23:24:31.926Z
- updatedAt: 2026-07-14T08:18:09.310Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: "Docs C2: source_addr + device-bind collision warning with the oif-rule recipe and bind-mode pointer"
- description: "install.md: document that per-WAN pinning via `ip rule from <source_addr>` is silently defeated by the auto device-bind on one-address interfaces (the D38 production failure: wildcard-source socket falls to the main table, ENETUNREACH, silent path-down while `ping -I <source_ip>` works), give the `ip rule add oif <dev> table N` workaround recipe, and point at the new per-path `bind = \"source\"` toggle as the clean fix. This is the most common real edge topology (VLAN-per-WAN) and is currently undocumented."
- acceptance: install.md section exists covering the symptom, the ip-rule recipe verbatim from the production deploy, and the bind= toggle cross-reference matching the shipped I5 field shape. Relates D38 in acceptance only. Docs lint clean.
- suggestedModel: fast
- dependsOn: ["T106"]
- ledgerRefs: ["goals:G6"]
- resultCommit: e790a3c
- completion: "Docs (install.md §3b) for the D38 production failure: per-WAN `ip rule from <source>` pinning silently defeated by the auto device-bind on one-address VLAN-per-WAN interfaces (wildcard-source socket → main table → ENETUNREACH silent path-down while `ping -I <source_ip>` works). Section covers the symptom, the OIF-ONLY workaround `ip rule add oif <dev> table N` (verbatim from the D38 production deploy), the three-condition 'auto' bind-mode behavior, and cross-references the per-path + top-level `bind = \"source\"` toggle (T105/T106) as the clean fix. 2-round criticism loop: round 1 caught a self-defeating `from <source_ip>` selector in the recipe (can't match a wildcard-source device-bound socket) + 4 more; round 2 fixed all, gate green. Unanimous R2 approve; rebased and ff-merged as e790a3c. Filed D60 (stale config.go BindMode comment)."
- sessionLogs: [".cq/logs/20260714-081229-a2e40c9c076c7fd70.md",".cq/logs/20260714-081736-a440fa701129dfa8e.md",".cq/logs/20260714-081736-a2b12f72790f5bc08.md"]
- rawLogs: [".cq/logs/raw/20260714-081229-a2e40c9c076c7fd70.jsonl",".cq/logs/raw/20260714-081736-a440fa701129dfa8e.jsonl",".cq/logs/raw/20260714-081736-a2b12f72790f5bc08.jsonl"]

### T113 — done

- createdAt: 2026-07-13T23:24:43.681Z
- updatedAt: 2026-07-14T08:27:29.314Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: "Docs C3+C6: full-tunnel client-LAN recipe + concentrator NAT/forwarding checklist"
- description: "install.md: the end-to-end full-tunnel / route-a-client-LAN recipe — THE primary use case, entirely undocumented. Edge side: mode=default-route (or the manual /1+/1 split allowed_ips — never literal 0.0.0.0/0 until D35 is resolved), policy-route the client subnet to wanbond0 and SNAT to the tunnel IP (so the concentrator's `allowed_ips = <edge>/32` still matches) OR widen concentrator allowed_ips to the client subnet. Concentrator side, as an explicit C6 checklist subsection: ip_forward=1, `MASQUERADE -s <tunnel-net> -o <wan>`, and the FORWARD conntrack-ESTABLISHED accept (the shipped `-i wanbond0 ACCEPT` covers only the forward direction — default FORWARD REJECT drops return traffic without it), plus persistence via netfilter-persistent (existing §5 pattern). Reference the addressing oneshot for rule persistence across restarts."
- acceptance: install.md gains the full-tunnel recipe section and the C6 checklist subsection (copy-pasteable, internally consistent); every command was validated on the production Pi/o3 deploy per wanbond-fixes.md; the recipe cross-references mode=default-route for the edge-automated part and marks the rest operator-owned (Q41 boundary). Relates D35 in acceptance only. Docs lint clean.
- suggestedModel: standard
- dependsOn: ["T108","T111"]
- ledgerRefs: ["goals:G6"]
- resultCommit: 1a8c570
- completion: "Docs (install.md §9 full-tunnel/client-LAN recipe + §5 C6 concentrator NAT/forwarding checklist) — the primary G6 production use case, previously undocumented. Edge side: mode=default-route (T108-automated /1+/1 split, cross-referenced) + operator-owned policy-route the client subnet to wanbond0 + SNAT to the edge's tunnel IP (validated primary path), OR the widen-concentrator-allowed_ips alternative. Concentrator C6 checklist (operator-owned per Q41): ip_forward=1, MASQUERADE -s <tunnel-net> -o <wan>, the two-directional FORWARD accept (incl. the ESTABLISHED,RELATED return-leg the shipped -i wanbond0 ACCEPT omits), persisted via netfilter-persistent. 3-round loop hardened the material: fixed a self-contradictory D35 /0 warning, a MASQUERADE-source mismatch in the widen alternative, and (R3) completed the widen branch's return path with the required operator `ip route add <client-subnet> dev wanbond0` on the concentrator (the daemon programs no routes there). Never documents a literal 0.0.0.0/0 as unsafe (the daemon splits it). Unanimous R3 approve after full end-to-end data-path re-trace; rebased over T112/T114 and ff-merged as 1a8c570."
- sessionLogs: [".cq/logs/20260714-082229-ab6bc3476090aaa34.md",".cq/logs/20260714-082657-a554e035011abf6ad.md",".cq/logs/20260714-082657-afee22f11f48ff9b8.md"]
- rawLogs: [".cq/logs/raw/20260714-082229-ab6bc3476090aaa34.jsonl",".cq/logs/raw/20260714-082657-a554e035011abf6ad.jsonl",".cq/logs/raw/20260714-082657-afee22f11f48ff9b8.jsonl"]

### T114 — done

- createdAt: 2026-07-13T23:24:53.330Z
- updatedAt: 2026-07-14T08:18:52.930Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: "Docs C5: reconverge window + restart guidance using the session metric"
- description: "install.md (operations/observability sections): document that until D36 is fixed, restarting ONE end can leave the tunnel down for minutes (stale-session peer does not promptly re-handshake) and that restarting BOTH ends ~together is the fast reconverge path (~25 s observed); present wanbond_session_established / the 'session established' log line as the operational 'is it up yet' check, distinguishing converging from wedged."
- acceptance: install.md section exists, explicitly marked as interim-until-D36 (acceptance reference only, no dependsOn on the D36 fix) and names the exact metric/log line shipped by the I2 task. Docs lint clean.
- suggestedModel: fast
- dependsOn: ["T101"]
- ledgerRefs: ["goals:G6"]
- resultCommit: c71d26a
- completion: "Docs (install.md §6a): interim-until-D36 restart/reconverge guidance. Documents that restarting ONE end can leave the tunnel down for minutes (up to ~3min, bounded by RekeyAfterTime 120s/RejectAfterTime 180s — the stale-session end does not promptly re-handshake) while restarting BOTH ends ~together reconverges in ~25s, and presents wanbond_session_established / the 'session established' log line (T101/I2, named verbatim) as the operational 'is it up yet' check with a converging-vs-wedged discriminator + a stale-end freshness caveat. 2-round loop: round 1 caught a fabricated '~2.5 hours' figure (actual RejectAfterTime=180s); round 2 corrected both sites + aligned with D36 (inner-WG whole-tunnel outage, distinct from resolved outer-path D12) + added the discriminator. Unanimous R2 approve; rebased and ff-merged as c71d26a."
- sessionLogs: [".cq/logs/20260714-081229-a850365d641c82d66.md",".cq/logs/20260714-081736-a61c19bcaa76a8b17.md",".cq/logs/20260714-081736-a0bb9d3d0e0db7a83.md"]
- rawLogs: [".cq/logs/raw/20260714-081229-a850365d641c82d66.jsonl",".cq/logs/raw/20260714-081736-a61c19bcaa76a8b17.jsonl",".cq/logs/raw/20260714-081736-a0bb9d3d0e0db7a83.jsonl"]

### T115 — done

- createdAt: 2026-07-13T23:25:03.491Z
- updatedAt: 2026-07-14T08:43:53.043Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Sync the config reference, wanbond.example.toml, design.md, and README for all new surfaces
- description: "Per the AGENTS.md docs-in-sync rule, one sweep after the config-surface and behavior tasks land: install.md §3z exhaustive key reference + wanbond.example.toml gain `bind` (per-path + global default, default auto), the full-tunnel `mode = \"default-route\"` + 0.0.0.0/0-split semantics, and `tun_persist`; docs/design.md notes the new session-signal metric names (shipped by T101) and the default-route wiring (landed by T108) as the one deliberate exception to 'the daemon never assigns routes'; docs/runbook.md — install.md's designated end-to-end operator provisioning procedure — updated to cover the new provisioning steps this goal introduces: the C1 NetworkManager unmanaged-devices drop-in, the C3 full-tunnel client-LAN recipe, and the C4 addressing/persistence oneshot; README feature list updated if it enumerates config keys or metrics."
- acceptance: Every new config key and metric introduced by this goal appears in §3z, wanbond.example.toml (commented-out with default, matching the existing style), and design.md; docs/runbook.md references the new C1/C3/C4 provisioning steps; grep for each new key/metric name across docs/ (including runbook.md) and README returns the expected hits; no stale 'never assigns routes' claim survives unqualified.
- suggestedModel: standard
- dependsOn: ["T101","T105","T107","T108","T109","T110","T111","T113"]
- ledgerRefs: ["goals:G6"]
- resultCommit: f2e3fc0
- completion: "The final G6 doc-sync consistency sweep (docs/design.md, docs/install.md §3z, docs/runbook.md, wanbond.example.toml). Inventoried what T101/T105-T114 already documented and filled the genuine gaps: added the `bind` key (top-level + per-path, source/device/auto, default auto) to §3z + wanbond.example.toml; added a design.md section covering tun_persist/TUNSETPERSIST, the mode=default-route routing wiring as the ONE deliberate exception to 'the daemon never assigns routes' (both surviving occurrences now inline-qualified), and the wanbond_session_established session signal; added runbook.md pointers to the C1 NetworkManager drop-in, C4 addressing/persistence oneshot, and C3 full-tunnel recipe. All claims grounded verbatim in source; all cross-reference anchors verified against real headings; TestExampleConfigLoads still green. Unanimous round-1 approve; ff-merged as f2e3fc0. Closes the G6 docs surface. (README needed no change; stale config.go bind comments deferred as D60.)"
- sessionLogs: [".cq/logs/20260714-083759-ace22219f4e9e9b4e.md",".cq/logs/20260714-084317-a1960e942fe11ca6d.md",".cq/logs/20260714-084317-af168e7ecc9a2339a.md"]
- rawLogs: [".cq/logs/raw/20260714-083759-ace22219f4e9e9b4e.jsonl",".cq/logs/raw/20260714-084317-a1960e942fe11ca6d.jsonl",".cq/logs/raw/20260714-084317-af168e7ecc9a2339a.jsonl"]

## M39

### T116 — done

- createdAt: 2026-07-14T09:21:14.157Z
- updatedAt: 2026-07-14T10:57:21.988Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Surface an authenticated peer-restart adoption event from telemetry.Reflector
- description: "In internal/telemetry/probe.go, expose the session-epoch adoption that acceptLocked already performs (the `echoedChallenge == st.challenge` branch) as a peer-restart signal the bind can consume. A RESTART is specifically an adoption where the path was ALREADY adopted under a DIFFERENT sessionID (st.adopted && sessionID != st.session) — a first-ever bootstrap adoption (!st.adopted) is NOT a restart and must fire nothing. Because all probers of one boot share one sessionID (probe.go ~:130), DEDUP at the Reflector (per-peer) level: track the last restart-adopted sessionID and report the restart ONCE per new epoch, not once per path. SURFACING FORM — PINNED (plan review R126, [fable]): surface it as an explicit RETURN FLAG from Reflect — change Reflect's signature to `(echo []byte, epochChanged bool, err error)` (or a small typed result). Do NOT use a callback on NewReflector: T119 consumes the flag at the dispatchInbound Reflect call site OUTSIDE the reflector's r.mu, whereas a callback would necessarily fire INSIDE acceptLocked under r.mu and violate T119's lock discipline. Keep the Reflector WG-/resequencer-unaware (it returns a boolean; it knows nothing about reseq). Update ALL current Reflect call sites (dispatchInbound + the ~31 probe/perpsk test sites) to compile green, IGNORING the new flag for now (the wiring lands in T119 — no behavioural change yet). Grounding: internal/telemetry/probe.go acceptLocked adoption branch (:378 same-session swallow, :384 adoption), per-boot sessionID; the surviving-side Reflector is exactly the one whose resequencer SUSPECT-drops the restarted peer's frames."
- acceptance: "Reflect's signature returns an explicit epochChanged bool (no callback). New unit tests in internal/telemetry/probe_test.go (extend TestProbeSessionEpochSurvivesPeerRestart): (a) restart adoption (already-adopted path, NEW sessionID carrying the live challenge) returns epochChanged=true EXACTLY ONCE even across multiple paths of the same boot; (b) first-ever bootstrap adoption returns epochChanged=false; (c) a cross-session probe WITHOUT the live challenge (replay/forgery) returns false; (d) within-session probes / per-path duplicates return false. `go test ./internal/telemetry/...` passes and `go build ./...` is clean (all call sites green, flag ignored)."
- suggestedModel: frontier
- ledgerRefs: ["goals:G7","defects:D36"]

### T119 — done

- createdAt: 2026-07-14T09:21:51.992Z
- updatedAt: 2026-07-14T12:19:30.701Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Wire the peer-restart event to the per-peer Resequencer.Rebaseline in dispatchInbound
- description: |
    In internal/bind/multipath.go dispatchInbound's non-echo Probe branch (around the pr.reflector.Reflect(raw) call, ~:1689), consume T116's epochChanged return flag: when Reflect reports an authenticated peer-restart epoch change, re-anchor THAT peer's resequencer so the restarted peer's low-outer-seq frames (the wrapped WG init/response, outer-seq ~1) admit instead of being SUSPECT-dropped. Atomically Load the resequencer and nil-check it (absent mid-teardown, like the DATA branch; a torn-down peer re-instantiates an UNSTARTED ring that needs no rebaseline). Perform the re-anchor OUTSIDE m.mu with no other lock held — the resequencer keeps its own mutex; NEVER nest it — mirroring the SetPeerRemote→Rebaseline discipline (multipath.go ~:2167-2183). Because `pr` is the demux-resolved per-peer view, this SINGLE site covers BOTH the edge single-concentrator primary resequencer AND every concentrator per-peer resequencer, and both restart directions. Leave the existing SetPeerRemote→Rebaseline hub-failover trigger (D32) untouched.
    
    RE-PIN RACE HANDLING — REQUIRED (plan review R126, [fable]): the plain Rebaseline() clears `started` and trusts the NEXT frame to re-anchor, but under the D36 saturation precondition a stale OLD-boot HIGH-seq straggler can still be draining from carrier/modem queues and land between the rebaseline and the wrapped low-seq init, re-pinning `next` HIGH and (with once-per-epoch dedup) blocking recovery. Use a LOW-ANCHOR re-anchor instead: extend internal/reseq with a variant (e.g. Resequencer.RebaselineToLow, or a pending-low-re-anchor mode on Rebaseline) that, after unpinning, re-anchors `next` ONLY on a frame whose outer-seq is MORE THAN one window BELOW the pre-rebaseline release point (the genuine restarted-stream low-seq) — treating stale-high stragglers as ordinary SUSPECT-drops until the low init arrives, and idempotent across repeated restart signals. Keep the existing hub-failover Rebaseline() call (D32) as-is (there the old sender has been dark the whole detection window, so no stale-high race exists) OR migrate it to the same low-anchor variant if that is cleaner — planner-neutral, but do not regress the D32 test.
- acceptance: "New bind regression test (internal/bind, following concentrator_peer_test.go/probe_test.go fixtures) whose comment documents it fails without this wiring (the D36 repro): advance a peer's resequencer `next` far past resequencerWindow (2048) with high-seq DATA, simulate that peer restarting (new-sessionID probes: learn challenge then adopt → epochChanged=true), then deliver a LOW outer-seq DATA frame (the wrapped init) — assert it is DELIVERED (re-anchored), Stats().Rebaselines increments, and no dropSuspect increment from it. MUST INCLUDE the STALE-HIGH race case: after the restart re-anchor, inject a stale OLD-boot HIGH-seq straggler BEFORE the low-seq init and assert it does NOT re-pin `next` high (it is SUSPECT-dropped) and the subsequent low-seq init still admits. Assert for BOTH the primary peerState AND an AddConcentratorPeer peer, and that another bound peer's resequencer is undisturbed; a same-epoch (non-restart) probe must NOT re-anchor. The existing D32 hub-failover Rebaseline test still passes. `go test ./internal/bind/... ./internal/reseq/... && go test ./...` pass; `go test -race ./internal/bind/...` clean."
- suggestedModel: frontier
- dependsOn: ["T116"]
- ledgerRefs: ["goals:G7","defects:D36"]

## M40

### T117 — done

- createdAt: 2026-07-14T09:21:21.139Z
- updatedAt: 2026-07-14T10:57:23.457Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Expose a one-shot first-path-up notification from the bind
- description: "In internal/bind/multipath.go, the sticky everUp latch (dispatchInbound ~:1677-1685) already observes the exact Down→Up moment on a fresh echo. Add an injectable one-shot callback (Multipath.SetOnFirstPathUp(func()) or a constructor option) invoked EXACTLY ONCE on the false→true everUp edge, keeping the bind WG-unaware (it invokes an opaque func). Fire OFF the receive hot path (dedicated goroutine, or after releasing any locks) and guarantee at-most-once across concurrent per-path receive goroutines (CAS on the atomic everUp latch, or a dedicated sync.Once). This is the D37 detection seam; the device-layer consumer lands in the dependent task."
- acceptance: "Bind unit test (internal/bind): with two paths receiving fresh echoes concurrently, the callback fires EXACTLY ONCE (race-checked: `go test -race ./internal/bind/...`); no callback set ⇒ no panic (nil-safe); no fire while all paths stay Down; no re-fire across a Down→Up→Down→Up cycle. `go build ./...` clean."
- suggestedModel: standard
- ledgerRefs: ["goals:G7","defects:D37"]

### T120 — done

- createdAt: 2026-07-14T09:21:59.881Z
- updatedAt: 2026-07-14T11:30:13.842Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Drive a forced WG handshake initiation off first-path-up in the device layer
- description: "In internal/device/device.go up() (~:258-437), wire the bind's T117 first-path-up callback to a forced WG handshake initiation against the edge's peer. Reuse the deviceRehandshake pattern (internal/device/failover.go ~:426-439): ExpireCurrentKeypairs backdates lastSentHandshake — which MATTERS here because the wasted pre-liveness boot init (the D37 symptom 'Failed to send handshake initiation: bind: no healthy path...') may have stamped lastSentHandshake, so a bare SendHandshakeInitiation ~0.6s later can be silently RekeyTimeout-suppressed by the engine; on a cold boot with no keypairs the expire is a no-op. Wire it for the EDGE role (single-concentrator peer); the concentrator is the responder and initiates nothing (leave startFailoverAndResolution's concentrator noop untouched). Keep the seam fake-able (ipcGetter-style, per lifecycle_test.go) so the unit test runs without a live engine. This replaces reliance on the engine's own 5s retransmit timer with a liveness-edge-driven initiation."
- acceptance: "Device unit test (internal/device, lifecycle_test.go fake-engine pattern): on the injected first-path-up edge, the initiation func is invoked EXACTLY ONCE for the edge peer (inject a counter as failover_test does for rehandshake); a concentrator-role construction wires NO initiation. `go test ./internal/device/... ./internal/bind/...` passes; `go build ./...` clean; no regression in `go test ./internal/...`."
- suggestedModel: standard
- dependsOn: ["T117"]
- ledgerRefs: ["goals:G7","defects:D37"]

## M41

### T118 — done

- createdAt: 2026-07-14T09:21:27.432Z
- updatedAt: 2026-07-14T10:57:25.048Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Expose resequencer rebaseline and drop-suspect counters for restart-recovery observability
- description: The e2e and field triage must quantify the restart-recovery path via counters. Verify internal/reseq's rebaselines and dropSuspect counters (r.rebaselines / r.dropSuspect, already in Stats) are surfaced through the metrics ReseqSnapshot (internal/metrics) and out the /metrics exposition; if either is not already exposed, add it (e.g. wanbond_reseq_rebaselines_total, wanbond_reseq_drop_suspect_total) following the existing ReseqSnapshot counter wiring, in BOTH the single-peer (no peer label) and per-peer (peer=<name>) exposition forms, preserving the metrics back-compat rules. Observability only — NO datapath behaviour change.
- acceptance: Both counters are readable at /metrics for the single-peer and multi-peer expositions; a metrics unit test asserts that a Rebaseline() and a dropSuspect increment are reflected in the scraped snapshot. `go test ./internal/reseq/... ./internal/metrics/...` passes; single-peer exposition still omits the peer label.
- suggestedModel: standard
- ledgerRefs: ["goals:G7","defects:D36"]

### T121 — done

- createdAt: 2026-07-14T09:22:18.781Z
- updatedAt: 2026-07-14T13:14:51.924Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Author the netns one-sided-restart e2e (restart-only-edge and restart-only-concentrator; privileged run deferred to o3 + llm-ubuntu-0)
- description: "Add a `-tags e2e` netns scenario to test/e2e (G2 pattern, following netns.go / standby_liveness_test.go fixtures; pick a unique metrics port not colliding with existing e2e ports). Bring up edge↔concentrator across network namespaces, SATURATE the bond (iperf3-style) so the surviving side's resequencer `next` advances well past resequencerWindow (2048) — the D36 precondition; then RUN A: restart ONLY the edge process (live paths, no endpoint change); RUN B (separate run): restart ONLY the concentrator process (no failover). For each direction assert: (1) wanbond_session_established transitions 0→1 within a documented budget WELL under the WG rekey timer, targeting ~= the ~25s both-ends-fresh baseline (static analysis predicts ~10s for the edge-restart direction — record the true per-direction magnitude); (2) the SURVIVING side's reseq counters show rebaselines>=1 and ~0 post-restart dropSuspect delta (the wrapped init was NOT suspect-dropped); (3) D37: from cold start, time-to-first-handshake tracks the first path-up edge (+~1 RTT), not a 5s retransmit-timer multiple. Capture the counters + 0→1 timestamps in test output for the D36 record. The PRIVILEGED run is DEFERRED to the o3 (aarch64) + llm-ubuntu-0 (amd64) hosts per the G2 remote-validation pattern (see the o3-hardware-e2e memory for exact SSH/sudo/PATH invocations); locally the test compiles + vets + skips/gates cleanly."
- acceptance: "`go build -tags e2e ./test/e2e/... && go vet -tags e2e ./test/e2e/...` clean locally; the test is excluded from the default `go test ./...` and skips (not fails) without privileges. It encodes the run-A/run-B restart matrix and the reconvergence + rebaselines>=1 + ~0-dropSuspect-delta + session-established-timing + D37-first-handshake assertions, with a short runbook (exact `ssh -i /run/agenix/llm-ssh-key ...` + sudo + `-tags e2e` invocation for both hosts). Privileged execution is explicitly deferred and NOT part of the merge gate."
- suggestedModel: frontier
- dependsOn: ["T119","T120","T118"]
- ledgerRefs: ["goals:G7","defects:D36","defects:D37"]

### T122 — done

- createdAt: 2026-07-14T09:22:27.663Z
- updatedAt: 2026-07-14T13:37:48.579Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Sync docs/design.md with the peer-restart rebaseline trigger and first-path-up rehandshake
- description: "Per AGENTS.md's docs-in-sync rule, update docs/design.md (and README.md only if it describes restart/failover behaviour) to document: (1) the outer-plane resequencer now re-baselines on a DETECTED, authenticated peer restart (the T38 responder-challenge session-epoch adoption) — extending the D32 hub-failover-only trigger to plain one-sided restarts on BOTH the edge single-concentrator path and the concentrator per-peer path; so there are now THREE trusted re-anchor triggers: hub failover (D32/SetPeerRemote), detected peer restart (D36), plus the unauthenticated tryResync corroboration fallback; (2) the WG handshake is (re)initiated off the first path StateUp edge (D37) rather than only on the engine's retransmit timer, and its interaction with the RekeyTimeout guard (the deviceRehandshake backdating). Cross-reference defects D36/D37/D32/D12; name the rebaselines/dropSuspect counters. State the operational expectation: one-sided restart reconverges ~= the both-ends-fresh baseline, not on the WG rekey timer. Surgical edit — no behavioural claims beyond the merged code; remove any stale claim that Rebaseline is hub-failover-only."
- acceptance: docs/design.md describes both mechanisms with their trigger conditions and names the rebaselines/dropSuspect counters, with D36/D37/D32/D12 cross-references; `grep -i rebaseline docs/design.md` and a first-path-up/handshake grep each hit the new sections; no stale 'rebaseline is hub-failover-only' / 'only ... failover' claim survives (grep docs/ + README.md). A reviewer can trace each documented behaviour to the merged code.
- suggestedModel: fast
- dependsOn: ["T119","T120"]
- ledgerRefs: ["goals:G7","defects:D36","defects:D37"]

## M42

### T123 — done

- createdAt: 2026-07-14T09:46:25.389Z
- updatedAt: 2026-07-14T10:57:26.430Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Re-key the source→peer demux by AddrPort and enforce a per-peer binding quota (D47+D49)
- description: |
    Rework peerBySource (internal/bind/multipath.go ~:419, 1445-1534) in ONE pass — D47 and D49 edit the same lock-free copy-on-write structure. (1) D47: change the key from netip.Addr to netip.AddrPort so two peers behind ONE public IP (CGNAT) bind independently — thread srcAP through lookupPeerBySource/bindSourceToPeer/unbindPeerSources and the demuxInbound lookup site (~:1404 currently passes srcAP.Addr()); preserve T90 roam semantics + never-evict-live. (2) D49: inside the same CAS loop, enforce a PER-PEER quota (e.g. maxDemuxSources/len(peers), floor 1, against the copied map) summing under the retained global cap, so one valid-psk insider flooding spoofed sources exhausts only ITS OWN quota and never starves another peer's bootstrap PROBE (Q27(1) isolation).
    
    SAME-PEER ROAM-CHURN DECISION — PINNED (plan review R128, [fable]): the AddrPort re-key means a legitimately-roaming peer's CGNAT PORT CHURN accumulates stale bindings against its OWN quota with no unbind short of teardown, and TearDownPeer refuses LIVE peers — so 'leave stale bindings to teardown reclaim' would let a live roaming peer past its quota drop its own re-bind PROBE forever. PIN the behaviour: when a peer already at its per-peer quota authenticates a NEW AddrPort binding for ITSELF, EVICT that same peer's OWN OLDEST binding (LRU within the peer) to admit the new one — this preserves never-evict-live with respect to OTHER peers and full cross-peer isolation (a peer can never evict another peer's slot), while a live peer can always re-bind after a roam. (Track a per-peer insertion order / small ring of that peer's AddrPorts for the LRU choice.) Keep drop-on-exhaustion ONLY for the CROSS-peer case (a peer at its quota cannot steal another peer's headroom). Update the defaultMaxDemuxSources doc comment. FIRST multipath.go task — T124/T125/T127 serialize after it.
- acceptance: "`go test -race ./internal/bind/...` passes incl. NEW tests: (a) two peers behind ONE IP (same netip.Addr, distinct ports) each bind and each peer's DATA reaches its OWN resequencer; (b) a CROSS-peer insider flooding k>=quota spoofed sources to peer A leaves peer B's first authenticated PROBE able to bind (bootstrap not starved) and A refused past its quota; (c) SAME-PEER port-churn: a single live peer that authenticates MORE than its quota of distinct AddrPorts (roam churn) keeps binding — its OWN oldest binding is evicted (LRU), it is NEVER dropped, and NO other peer's slot is disturbed; (d) existing concentrator_roam_test.go + cap tests updated for the AddrPort key and green. `go vet ./...` clean."
- suggestedModel: frontier
- ledgerRefs: ["goals:G8","defects:D47","defects:D49"]

### T124 — done

- createdAt: 2026-07-14T09:46:45.633Z
- updatedAt: 2026-07-14T13:07:21.610Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Complete the deferred-path multi-peer lifecycle (promoteDeferredLocked fan-out + alignment guard) (D42)
- description: "D42's originally-filed AddPath desync is ALREADY partially mitigated (internal/bind/multipath.go ~:2339-2355 mints per-peer probers, keeping every p.probers m.defs-aligned; its comment names D42's panic as 'the pre-fix behaviour'). The LIVE residual is the deferred-path PROMOTION: promoteDeferredLocked (internal/bind/reconcile.go ~:138-188) attaches ONLY the primary's view ('single-peer today' comments), so on a concentrator a promoted deferred path leaves every non-primary peer view-less (its frames on that socket never demux to it) and absent from its scheduler until the next Close→Open. Fix: fan the promotion out to EVERY bound peer, reusing each peer's m.defs-aligned prober (locate by def name/index in p.probers) rather than minting fresh ones, with full rollback on partial failure (mirror attachSharedPathLocked); publish each view via addViewLocked. Add a fail-fast alignment assertion in removeDurableLocked (multipath.go ~:2575) that returns/logs a wiring-defect error instead of silently mis-splicing when any p.probers length diverges from m.defs. Serialized after T123 (shares multipath.go)."
- acceptance: "`go test -race ./internal/bind/...` passes incl. three new multi-peer deferred-lifecycle regression tests: (a) RemovePath of a deferred path with >=2 bound peers splices every peer's probers correctly (no slice-bounds panic — the D42 scenario), (b) reconcile promotion gives EVERY peer a view + scheduler entry and both peers' DATA flows on the promoted path, (c) Close→Open after a deferred AddPath rebuilds without the out-of-range panic. A test constructing 2 peers + 1 deferred path + RemovePath panics on the pre-fix behaviour and passes after."
- suggestedModel: standard
- dependsOn: ["T123"]
- ledgerRefs: ["goals:G8","defects:D42"]

### T125 — done

- createdAt: 2026-07-14T09:46:54.469Z
- updatedAt: 2026-07-14T14:15:32.944Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Fan the FEC deadline flush + adaptive-controller drive across all bound peers (D44)
- description: "fecFlushDeadline (internal/bind/multipath.go ~:1962-2022) and driveAdaptiveControllerLocked (~:2039-2057) reach fecSend/scheduler/paths through the embedded-primary promotion, and encodeParityLocked is called with m.peerState hard-coded (~:2005) — so only the PRIMARY peer's straggler FEC groups get deadline parity; a non-primary peer's partial groups close only on fill, silently losing straggler parity (D44). Fix: make the flush iterate m.peers under the same TryLock, per peer Load fecSend (nil-skip a torn-down/uninstantiated peer), drive that peer's adaptive controller, Tick that peer's encoder, Pick on that peer's scheduler over that peer's paths, and frame parity with encodeParityLocked(peer, ...); accumulate wire writes outside the lock as today. ALSO fix the tick-loop START condition in Open (~:941 `m.fecSend.Load() != nil`, primary-only): start fecTickLoop whenever m.fecCfg != nil, so a concentrator peer whose fecSend materializes lazily on re-bind (ensurePeerReceiveInstantiated) still receives deadline flushes. Serialized after T124 (shares multipath.go)."
- acceptance: "`go test -race ./internal/bind/...` passes incl. a new two-peer deadline-flush test that FAILS on current code (peer 2's partial group emits no parity on tick) and passes after: two bound peers each with a partial FEC group; one deadline tick emits parity for BOTH, each decodable ONLY under its own peer's psk-derived codec and egressed via its own scheduler; a torn-down peer is skipped without disturbing others. Existing fec_test.go / peer_fec_lifecycle_test.go stay green."
- suggestedModel: standard
- dependsOn: ["T124"]
- ledgerRefs: ["goals:G8","defects:D44"]

## M43

### T126 — done

- createdAt: 2026-07-14T09:47:03.275Z
- updatedAt: 2026-07-14T13:37:36.196Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Wire device per-peer session/liveness-loss events to Bind.TearDownPeer (D50)
- description: |
    Bind.TearDownPeer (internal/bind/multipath.go ~:1592) exists and is bind-tested but NO production code calls it (D50), so a dead peer's resequencer ring, FEC buffers, and demux cap slots are never reclaimed. Wire it from the device: extend internal/device/session.go — whose latestHandshakeNano currently flattens the UAPI dump to one global max — to parse a PER-PEER snapshot (public_key → last_handshake instant per peer block) and map pubkey → configured peer name via config.PeerIdentities.
    
    DETECTION IS LEVEL-TRIGGERED — PINNED (plan review R128, [fable]): do NOT use edge-triggered (Established 1→0) detection — a peer's heavy state is instantiated on its FIRST authenticated PROBE (demuxInbound→ensurePeerReceiveInstantiated, multipath.go:1425-1435), NOT on WG handshake, so a valid-psk peer that BINDS via PROBE but NEVER completes a handshake (wrong WG keys, or edge dies pre-handshake) has last_handshake=0 forever and no 1→0 edge ever fires, leaking its state permanently — the exact D50 symptom. Instead, in the concentrator poll loop, for EACH configured non-primary peer, LEVEL-check 'not established now' (last_handshake absent OR aged past RejectAfterTime) and call Bind.TearDownPeer(name) every poll while that holds. TearDownPeer is idempotent-safe (refuses live peers + the primary, no-ops on an absent/already-torn name), so a repeated level-triggered call is harmless and also survives a daemon-reload loss of prior edge memory; log one INFO on the transition to torn-down (dedupe the log, not the call). Engage the per-peer path ONLY in multi-peer (concentrator) mode; the single-peer edge/hub keeps the existing global monitor byte-identical. Edits device/session.go (parallel to the M42 multipath.go chain); dependsOn T123 for the AddrPort teardown-reclaim semantics.
- acceptance: "`go test -race ./internal/device/... ./internal/bind/...` passes incl. new device tests (fake ipcGetter/UAPI dump): (a) a peer whose handshake ages past RejectAfterTime is torn down (TearDownPeer invoked with its configured name; its resequencer.Load() is nil + source bindings gone); (b) the NEVER-HANDSHAKED reclaim case — a peer that instantiated state via authenticated PROBE but has last_handshake=0 (no handshake ever) is ALSO torn down by the level check (not skipped for lack of a 1→0 edge); (c) a LIVE peer (fresh handshake) is untouched and its repeated level-check does not tear it down; (d) a fresh authenticated PROBE re-binds + re-instantiates a torn-down peer and DATA flows again."
- suggestedModel: frontier
- dependsOn: ["T123"]
- ledgerRefs: ["goals:G8","defects:D50"]

### T127 — planned

- createdAt: 2026-07-14T09:47:20.098Z
- updatedAt: 2026-07-14T09:53:57.886Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Plumb the primary peer's configured name into the bind so metrics label every peer (D58)
- description: |
    Config validation requires a unique name per peer in multi-peer mode, yet device.Up never passes ids[0].Name to bind.NewMultipath — newPeerState("", ...) at internal/bind/multipath.go ~:612 — so BoundPeerNames (~:2614) and every per-peer metrics series label the primary edge peer="" (D58). Fix: in the concentrator wiring, set the primary peerState's name to ids[0].Name whenever >1 peer is configured (an extra NewMultipath parameter, or a pre-Open SetPrimaryPeerName that re-keys peersByName from "" and keeps the AddConcentratorPeer name-collision check correct), keeping name="" ONLY for the single-peer edge/hub so the exposition stays byte-compatible (T94). Verify TearDownPeer's primary-refusal still keys on IDENTITY (p == m.peerState ~:1563), not the empty name. Update BoundPeerNames consumers + device.Up/up.
    
    DOC-SYNC — REQUIRED (plan review R128, [fable], AGENTS.md same-change rule): this changes USER-VISIBLE, DOCUMENTED metrics-label semantics — T98 shipped docs (docs/design.md/README.md/docs/runbook.md) pinning the current primary peer="" behaviour, and T97's e2e asserts it. In the SAME change, update those docs so the multi-peer metrics-label description states that EVERY configured peer (including the first) carries its configured `name` as the `peer` label when >1 peer is bound, and peer="" remains ONLY for the single-peer exposition. Update the tests: TestExpositionTwoPeerSeries (both peers carry non-empty names), the T94 back-compat test (single-peer still peer=""), and the T97 e2e label expectation (edge A now its configured name, no longer "").
- acceptance: "`go test -race ./internal/bind/... ./internal/device/... ./internal/metrics/...` passes; TestExpositionTwoPeerSeries (updated) asserts BOTH peers' series carry their configured non-empty names on a two-peer concentrator; the single-peer back-compat test still asserts peer=\"\" unchanged; the T97 e2e label expectation updated to the configured name. DOCS: docs/design.md + README.md + docs/runbook.md metrics-label descriptions updated to the corrected multi-peer behaviour (grep for the old 'primary ... peer=\"\"' / 'first-configured peer ... \"\"' framing and correct it); a reviewer can trace the documented label rule to the merged code. Serialized after T125 (shares multipath.go NewMultipath/BoundPeerNames)."
- suggestedModel: standard
- dependsOn: ["T125"]
- ledgerRefs: ["goals:G8","defects:D58"]

## M44

### T128 — planned

- createdAt: 2026-07-14T09:47:30.122Z
- updatedAt: 2026-07-14T09:47:30.122Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Extend the multi-peer netns e2e for the hardened datapath (shared-NAT demux, teardown/re-bind, quota, per-peer labels)
- description: "Extend the existing multi-peer concentrator netns e2e (test/e2e, -tags e2e, G2 pattern, following multipeer_test.go/netns.go; unique metrics port) with the externally-observable hardening behaviours: (a) D47 — two edge peers egressing from ONE public IP (same-netns SNAT, distinct ports) both establish sessions and carry traffic through the concentrator simultaneously; (b) D50 — kill one edge (drop links / stop its daemon), assert the concentrator tears its peer state down (log-grep the teardown INFO + /metrics reflects the dead peer), then restart the edge and assert re-handshake re-binds and traffic resumes; (c) D58 — scrape the concentrator /metrics and assert EVERY peer's series (incl. the FIRST configured peer) carries its configured non-empty name; (d) D49 best-effort — where feasible in netns, a spoofed-source flood from one edge does not block the other edge's bootstrap; (e) where reachable, D42 (deferred-path add/remove with >1 peer no panic) + D44 (non-primary peer receives deadline FEC parity). Local deliverable is compiling, vet-clean test code + harness plumbing; the PRIVILEGED run is DEFERRED to the separate host-run task."
- acceptance: "`go build -tags e2e ./test/e2e/... && go vet -tags e2e ./test/e2e/...` clean; the test is excluded from the default `go test ./...` and skips (not fails) without CAP_NET_ADMIN (gated on the harness's root/netns capability check exactly as existing e2e tests). It encodes the D47/D49/D50/D58 (+ reachable D42/D44) scenarios with log-grep + /metrics-scrape assertions."
- suggestedModel: standard
- dependsOn: ["T125","T126","T127"]
- ledgerRefs: ["goals:G8","defects:D47","defects:D49","defects:D50","defects:D58"]

### T129 — planned

- createdAt: 2026-07-14T09:47:37.696Z
- updatedAt: 2026-07-14T09:47:37.696Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Run the multi-peer privileged netns e2e on o3 + llm-ubuntu-0 (deferred hardware validation)
- description: "DEFERRED privileged execution of the extended multi-peer netns e2e (G2 remote-hardware pattern): run the `-tags e2e` multi-peer suite requiring CAP_NET_ADMIN / real netns on BOTH hosts — o3.7mind.io (aarch64) and llm-ubuntu-0.pgtr.7mind.io (amd64) — using the documented `ssh -i /run/agenix/llm-ssh-key <user>@<host>` + passwordless sudo + Go PATH invocations (see the o3-hardware-e2e project memory for the exact provisioning + SSH/sudo/PATH commands). Capture pass/fail output for the D47 (two-peers-behind-one-NAT bind), D49 (insider-quota fairness), D50 (dead-peer teardown + re-bind), D58 (per-peer metrics name), and reachable D42/D44 scenarios on EACH architecture. This task performs NO source changes; it depends on the e2e-authoring task having merged."
- acceptance: The `-tags e2e` multi-peer suite is executed on both o3 (aarch64) and llm-ubuntu-0 (amd64) and passes on each; captured logs show the two-peers-behind-one-NAT bind, insider-quota fairness, dead-peer teardown+re-bind, and per-peer metrics-name assertions succeeding. Results recorded on the goal/milestone.
- suggestedModel: standard
- dependsOn: ["T128"]
- ledgerRefs: ["goals:G8","defects:D47","defects:D49","defects:D50","defects:D58"]

## M45

### T130 — done

- createdAt: 2026-07-14T10:00:28.040Z
- updatedAt: 2026-07-14T11:05:02.662Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Reject unknown TOML keys at config load via strict decoding (D41)
- description: "internal/config/load.go:34 decodes with non-strict toml.Unmarshal (go-toml/v2), so a misspelled key (link_bandwith, nane) is silently dropped despite Load's documented fail-fast posture (D41). Replace the decode site with `toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields()` + Decode(&c). On failure, detect *toml.StrictMissingError (errors.As) and render its row list into a precise `config %s: unknown key ...` error instead of the raw multiline dump; leave all other decode errors on today's `config %s: %w` path. Confirm every existing loadable config shape still loads (the `toml:\"-\"` derived fields are struct-side only and do not affect strict decoding of INPUT keys). FIRST task in the G9 chain — T131/T132 serialize after it (all touch internal/config) so the D43 field re-keying is itself covered by strict-decode tests."
- acceptance: "New rejects-table cases in internal/config tests: a config containing a misspelled key (e.g. `link_bandwith` on a path, `nane` on a peer) fails Load with an error identifying the unknown key; all existing accept-table configs still load (incl. wanbond.example.toml via TestExampleConfigLoads). `go test ./internal/config/... && go test ./...` pass."
- suggestedModel: standard
- ledgerRefs: ["goals:G9","defects:D41"]

### T131 — done

- createdAt: 2026-07-14T10:00:43.797Z
- updatedAt: 2026-07-14T12:00:05.416Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Accept documented string-duration forms for operator-facing duration knobs (D43)
- description: "wanbond.example.toml documents collapse_dwell=\"2s\", load_tau=\"200ms\", weight_rtt_floor=\"1ms\" and [fec] deadline=\"5ms\", but those fields are bare time.Duration and go-toml/v2 CANNOT decode a TOML string into time.Duration — an operator uncommenting the documented example gets a load failure (D43, probe-confirmed). Accept Go duration STRINGS uniformly for all operator-facing duration knobs: SchedulerConfig.CollapseDwell/LoadTau/WeightRTTFloor and FEC.Deadline. Use the in-repo LinkRTTRaw precedent (config.go ~:874-883): a Raw string field carrying the TOML key + the typed field moved to `toml:\"-\"`, parsed via time.ParseDuration in normalize() with fail-fast on unparseable/non-positive values. (A shared TextUnmarshaler duration wrapper is acceptable ONLY if it keeps ONE mechanism across all knobs.) Decide + document whether the bare-integer-nanoseconds form remains accepted (docs use strings exclusively; dropping it is fine if the error says so). Preserve each knob's applyDefaults defaulting + existing range validation. SAME-CHANGE docs sync (AGENTS.md): keep wanbond.example.toml consistent + audit README.md/docs/design.md/docs/install.md for duration-form mentions. Serialized after T130 (shares config.go; its field re-keying must pass under the T130 strict decoder)."
- acceptance: "A config-test matrix loads EVERY documented string-duration form (collapse_dwell=\"2s\", load_tau=\"200ms\", weight_rtt_floor=\"1ms\" under [scheduler] policy=\"weighted\", deadline=\"5ms\" under [fec] enabled=true) and asserts the parsed time.Duration values; rejects-table entries cover an unparseable duration (\"5 parsecs\") and a non-positive one (\"-1s\") with errors naming the field; and TestExampleConfigLoads (or an added check) confirms the uncommented wanbond.example.toml duration lines load. `go test ./internal/config/...` passes."
- suggestedModel: standard
- dependsOn: ["T130"]
- ledgerRefs: ["goals:G9","defects:D43"]

### T132 — done

- createdAt: 2026-07-14T10:00:55.164Z
- updatedAt: 2026-07-14T12:21:44.804Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Validate allowed_ips CIDR syntax + default-route /0 exclusivity at load (D55+D59)
- description: "Two folded validate()-only fixes in internal/config/config.go's per-peer loop (~:1090-1124). (D55) config.validate() never parses allowed_ips entries, so a malformed CIDR (10.0.0.0/33, a typo) passes Load and fails LATE at daemon start when the engine's IpcSet rejects the rendered UAPI allowed_ip= line: netip.ParsePrefix each entry and fail fast naming the peer index (+ name when set) + the offending string, mirroring the source_addr/endpoint parse-at-load discipline. (D59) using the parsed prefixes, enforce (a) at most ONE peer carries mode=default-route, (b) a 0.0.0.0/0 or ::/0 entry appears at most once per address family ACROSS peers and never duplicated WITHIN a peer — WireGuard cryptokey routing makes overlapping allowed_ips last-writer-wins, a silent misconfig. NUANCE (both candidate planners confirmed): the edge single-peer rule (config.go ~:1087) + the concentrator default-route rejection (~:1121) make the multi-peer default-route shapes UNREACHABLE via Load today — the reachable case is a single edge peer listing /0 twice; enforce the cross-peer invariant DIRECTLY anyway (future-proofs any relaxation of the one-peer cap) and unit-test the guarded-but-unreachable combinations by calling validate() on a constructed Config (package config tests). D59 folds into D55's task because its /0 detection consumes D55's parsed prefixes. Serialized after T131 (shares config.go)."
- acceptance: "Rejects-table cases: a peer with allowed_ips=[\"10.0.0.0/33\"] (and a non-CIDR typo) fails Load with the peer index + offending entry; a single edge default-route peer with [\"0.0.0.0/0\",\"0.0.0.0/0\"] fails Load as a duplicate /0. Direct Config.validate() unit tests reject a constructed two-default-route-peer Config and a /0-on-two-peers Config (naming the conflicting peers). All existing accept-table configs (incl. the normal single default-route peer with one 0.0.0.0/0, and valid CIDRs \"10.0.0.0/24\"/\"::/0\") still load. `go test ./internal/config/... && go test ./...` pass."
- suggestedModel: standard
- dependsOn: ["T131"]
- ledgerRefs: ["goals:G9","defects:D55","defects:D59"]

## M46

### T133 — done

- createdAt: 2026-07-14T10:07:27.505Z
- updatedAt: 2026-07-14T10:57:27.939Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Count probe emission + echo-reflection wire bytes into ps.txBytes and flip the T104 standby-idle subtest green (D48)
- description: "wanbond_path_tx_bytes_total under-reports because ps.txBytes is charged only on the DATA/PARITY send paths (Send + fecFlushDeadline), while rxBytes counts EVERY inbound datagram — so a healthy idle standby (active-backup collapses DATA onto the primary) reads path_up=1 with tx=0 while rx grows (the I8-motivating observation). Adopt the true-wire-volume contract (D48's preferred option): (1) internal/bind/probe.go emitProbes (~:50): after a successful ps.conn.WriteToUDPAddrPort(raw, remote), add ps.txBytes.Add(uint64(len(raw))) — ONLY on a nil error, mirroring how rxBytes counts bytes actually pulled off the socket. (2) internal/bind/multipath.go dispatchInbound echo reflection (~:1693, line refs drifted — locate the echo WriteToUDPAddrPort): after a successful write add ps.txBytes.Add(uint64(len(echo))). Both sites use the atomic counter outside m.mu — no locking change. (3) COUNTER-CONTRACT SYNC (R131 [fable]): the peerPathState txBytes field comment (internal/bind/multipath.go ~:157-167) currently states txBytes 'counts the DATA-frame wire bytes this path egresses on the Send hot path' and defines the data-thrift signal as 'the backup path's Send count stays ~flat' — BOTH become false once probe/echo bytes count (the standby's counter growing IS the fix). Rewrite that comment to the true-wire-volume semantics (all egressed wire bytes: DATA/PARITY + probes + echoes), and verify the wanbond_path_tx_bytes_total help string (internal/metrics) is ACCURATE ('Total bytes transmitted on the path'; adjust only if it still implies DATA-only). Do NOT add a separate DATA-only series (out of scope). (4) DOCS-SYNC (R131 [fable], AGENTS.md clause — T133 changes an operator-visible metric's semantics): check and update README.md + docs/design.md wherever wanbond_path_tx_bytes_total OR the idle-standby-tx symptom is documented, so operator docs match the new wire-volume meaning. (5) T104 SUBTEST (R131 [fable] clarification): 'flip the T104 standby-idle subtest green' means UPDATE ITS STALE REPRO COMMENTARY — the test's file/subtest doc-comment that predicts failure and the t.Errorf 'refile-as-defect' instruction — NOT invert any assertion: the subtest already asserts delta>0 via t.Errorf, so the green acceptance (idle standby tx_bytes>0 while path_up=1) holds once the fix lands; only the surrounding stale-repro prose changes. (6) NOTE: throughput derivation (internal/device/metrics.go) sums tx+rx deltas, so counted probe bytes slightly raise reported idle throughput — intended wire-volume semantics. FIRST bind task — T134 (D53) serializes after it (both edit multipath.go)."
- acceptance: "`go build ./... && go vet ./... && go test ./internal/bind/... ./internal/metrics/...` pass; new unit tests (probe_test.go fake-clock pattern) assert ps.txBytes increments by the emitted frame length for (a) emitProbes probe emission and (b) an inbound PROBE's echo reflection, and FAIL against the unpatched code. The peerPathState txBytes comment (multipath.go ~:157-167) no longer claims DATA-only/Send-only semantics. README.md + docs/design.md carry no stale DATA-only tx_bytes wording (grep for wanbond_path_tx_bytes_total documentation). T104's 'standby-transmits-when-idle' subtest (test/e2e/standby_liveness_test.go) has its stale-repro commentary updated to the green expectation (idle standby shows tx_bytes>0 while path_up=1) with no assertion logic inverted; compiles under -tags e2e (privileged run per the o3/llm-ubuntu-0 procedure)."
- suggestedModel: standard
- ledgerRefs: ["goals:G10","defects:D48"]

### T134 — done

- createdAt: 2026-07-14T10:07:46.968Z
- updatedAt: 2026-07-14T15:50:38.983Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Thread internal/log through NewMultipath and WARN on every forced-device-bind fallback to source-IP pinning (D53)
- description: "internal/bind holds no logger, so the SO_BINDTODEVICE→source-IP fallback is SILENT — an operator setting bind=\"device\" can end up source-IP-pinned (losing roam survival) with no signal (D53). (1) Add a log.Logger parameter to NewMultipath (internal/bind/multipath.go, ~:549/612) and store it component-scoped (logger.Component(\"bind\")); update ALL call sites: the ~9 in internal/device/device.go (device already holds t.log) + the ~6 internal/bind test files (a discard-writer log for tests). Fail fast on a nil logger, consistent with the constructor's existing nil-checks. (2) WARN at BOTH fallback layers, naming path + interface: (a) unresolvable-interface layer — where planPathBinds/resolveForcedDeviceBind (internal/bind/pathsock.go) yields dev==\"\" for a path whose resolved mode is BindModeDevice; (b) setsockopt-failure layer — listenPath (pathsock.go:35) currently SWALLOWS the listenOnDevice error: restructure so the caller can log it — PREFERRED (keeps pathsock.go logging-free) is to RETURN the fallback fact + underlying error alongside the conn and WARN at the call sites (Open, AddPath via the addPathListen seam, reconcile.go). This covers the PRE-EXISTING silent CAP/setsockopt fallback too; distinguish FORCED (bind=\"device\", operator-chosen roam property lost — WARN) from AUTO (informational). Keep the m.resolveDeviceBind/m.addPathListen function-field seams working for tests. Docs-sync per AGENTS.md if bind-fallback behaviour is documented. Serialized after T133 (both edit multipath.go; T134 changes the NewMultipath signature + struct)."
- acceptance: "`go build ./... && go vet ./... && go test ./internal/bind/... ./internal/device/...` pass (all NewMultipath call sites updated); new unit tests inject a capturing log.Logger and assert ONE WARN naming path+interface for (a) an unresolvable forced device (non-existent interface) and (b) a failing setsockopt fallback (driven via the addPathListen/deferredListen seams), and NO WARN on a successful device bind or a source-mode path; the WARN-on-fallback tests FAIL against the unpatched (silent) code; the fallback still returns a working source-IP-bound socket."
- suggestedModel: standard
- dependsOn: ["T133"]
- ledgerRefs: ["goals:G10","defects:D53"]
- resultCommit: 10f8a4c

### T135 — done

- createdAt: 2026-07-14T10:07:57.865Z
- updatedAt: 2026-07-14T15:29:44.819Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Extend reloadWarnings to cover the Scheduler/FEC/DNS/Bind reload-immutable config sections (D52)
- description: "reloadWarnings (internal/device/device.go ~:549) reports a change for Role/PSK/WireGuard/Amnezia/Log/TUNPersist + same-name path source/dest + reorder, but OMITS the Scheduler, FEC, DNS, and Bind sections — a SIGHUP changing any of those is silently accepted while the running tunnel keeps the booted values, contradicting Reload's documented 'SILENCE is not acceptable' invariant (D52). Fix: (1) add reflect.DeepEqual comparisons with actionable per-section messages for the top-level Scheduler, FEC, and DNS sections (mirroring the WireGuard/Amnezia/Log cases). (2) BIND — Bind lives at BOTH levels (R131 [opus+fable], NOT either/or): config.Config has a TOP-LEVEL default c.Bind (normalize resolves it, internal/config/config.go ~:841-843) AND a per-path config.Path.Bind with fallback to that default (~:847-849). Handle BOTH: (a) extend the existing same-name-path comparison (currently SourceAddr/DestAddr only) to also warn when l.Bind != d.Bind ('path %q bind mode changed — the running socket keeps its original binding') — since normalize resolves the top-level default into every path, this catches effective per-path changes; AND (b) explicitly handle the top-level c.Bind — either its OWN DeepEqual case with an actionable message ('default bind mode changed — running sockets keep their original binding'), OR deliberately zero it in the (3) catch-all as covered-by-per-path — so a top-level bind change fires ONE actionable per-section warning, NOT the generic catch-all double-warning. (3) FUTURE-PROOF (D52 suggestedFix option B): add a final catch-all comparing struct copies with ALL handled fields zeroed (Paths, Metrics — which IS applied, so must NOT warn — and every individually-compared/warned field including the Bind fields handled above) via reflect.DeepEqual, warning generically that other config sections changed — so a future Config field can never silently regress this invariant. Do NOT warn on Metrics (Reload applies it) or on path membership add/remove (applied). Keep reloadWarnings a pure function. Docs-sync design.md reload section if it enumerates warned sections. Edits device.go (reloadWarnings ~:549); serialized AFTER T134 (R131 [opus]: T134 rewrites the 9 NewMultipath call sites in device.go, so both tasks edit device.go — same-file serialization, consistent with the T133->T134 multipath.go rule)."
- acceptance: "`go build ./... && go test ./internal/device/...` pass; new table-driven internal/device/reload_test.go cases each mutate exactly one of Scheduler/FEC/DNS between live and desired and assert reloadWarnings returns exactly one corresponding warning; a case mutating the TOP-LEVEL c.Bind default asserts exactly one bind-default warning (NOT the generic catch-all), and a case mutating a single path's Path.Bind asserts exactly one per-path bind warning; a case mutating Metrics asserts NO warning (still applied); a synthetic added-but-unhandled field is caught by the zeroed-copy catch-all (or its coverage documented); existing warning cases stay green."
- suggestedModel: standard
- ledgerRefs: ["goals:G10","defects:D52"]
- dependsOn: ["T134"]
- resultCommit: 82463bb

## M47

### T136 — done

- createdAt: 2026-07-14T10:09:10.921Z
- updatedAt: 2026-07-14T10:57:29.453Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: "Make `just lint` green and hermetic: fix the 3 base findings (D45) + stop golangci-lint walking .claude/worktrees (D54)"
- description: "Two coupled lint-gate fixes (one hides the other: sibling-worktree noise masks real findings). (D54 — hermeticity) The Justfile `lint` recipe runs bare `golangci-lint run` from the repo root, walking .claude/worktrees/ and leaking sibling agents' in-progress code into every lint run. NOTE (fable grounding): `.golangci.yml` is a v2-format config (`version: \"2\"`), so the defect's suggested v1 `run.skip-dirs`/`issues.exclude-dirs` keys DO NOT apply — the v2 mechanism is `linters.exclusions.paths` (+ `formatters.exclusions.paths`) with a pattern like `^\\.claude/`, OR switch the Justfile lint recipe to an explicit package list (`golangci-lint run ./cmd/... ./internal/... ./test/...`); pick one and document why in a one-line comment. (D45 — the 3 tracked-tree findings) errcheck on the unchecked deferred Close at internal/dnsresolve/doh.go:206 (`defer resp.Body.Close()`) and dot.go:168 (`defer conn.Close()`) — fix per repo convention (e.g. `defer func() { _ = x.Close() }()`; keep the Body.Close recognizable to bodyclose); plus the staticcheck QF1001 De Morgan rewrite in internal/bind/pathsock.go — the filed line :166 is STALE (T106 shifted the file), so run `golangci-lint run ./internal/bind/...` to locate the current site + apply the suggested rewrite. Also fix any other finding lint reports on the tracked tree (goal notes device/metrics_test.go). Do NOT touch the pathsock CAP_NET_RAW comment (that is the dependent D40 task). FIRST task — all other G11 tasks dependsOn it so each verifies against a GREEN lint, and it serializes the two potential pathsock.go editors (this QF1001 rewrite vs D40's capability comment)."
- acceptance: "From a clean checkout in the `nix develop` dev shell with a clean golangci-lint cache, `just lint` exits 0 on the tracked tree; the doh.go/dot.go errcheck + pathsock.go QF1001 findings are gone. Hermeticity: a throwaway Go file with an obvious lint violation placed under `.claude/worktrees/x/` does NOT change `just lint`'s (still-0) exit status. `just test` stays green."
- suggestedModel: standard
- ledgerRefs: ["goals:G11","defects:D45","defects:D54"]

### T137 — done

- createdAt: 2026-07-14T10:09:29.500Z
- updatedAt: 2026-07-14T11:39:48.359Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Give each e2e metrics listener a unique port (resolve the 9096 collision) (D51)
- description: "test/e2e/pacing_test.go and test/e2e/p3_fec_test.go both bind the metrics listener to 127.0.0.1:9096, breaking the per-file-unique-port convention (latent under the sequential netns runner; an active EADDRINUSE under shuffle/parallelism or a wedged teardown). Inventory every metrics-port constant across test/e2e/*.go (T101 already claimed 9101), then move ONE of the two colliding files to an unused port. Add a short comment at the chosen constant (or in the shared fixture, e.g. netns.go) enumerating the claimed ports so the convention can't silently drift again — the minimal registry the defect asks for; do NOT build ephemeral :0 allocation unless it's a trivial drop-in. e2e test-only; no production source. dependsOn T136 so it verifies against a green/hermetic lint."
- acceptance: A grep for metrics-port constants across test/e2e/ shows every file's port unique (no two files share a port literal); the port-inventory comment lists them. `go vet -tags e2e ./test/e2e/... && golangci-lint run --build-tags e2e ./test/e2e/...` pass; unprivileged `just test` stays green.
- suggestedModel: fast
- dependsOn: ["T136"]
- ledgerRefs: ["goals:G11","defects:D51"]

### T138 — done

- createdAt: 2026-07-14T10:09:39.058Z
- updatedAt: 2026-07-14T11:19:58.376Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Refresh the four stale 'not yet consumed' doc-comments in internal/config/config.go (D57, D60)
- description: "Single-file comment sweep — D57 and D60 are co-located in internal/config/config.go so ONE task owns the file (avoids intra-file conflict). D60 (delete two stale sentences, keep the accurate description): (a) the BindMode type comment (~:78-81) — remove 'This is the CONFIG SURFACE only ... a later task — today every path is bound exactly as before'; (b) the Path.Bind field note (~:488-494) — remove 'this is the config surface only, not yet consumed by planPathBinds/selectDeviceBinds'. Both FALSE since T106: pathsock.go selectDeviceBinds switches on config.BindModeSource/Device/Auto and multipath.AddPath honors a forced BindModeDevice. D57 (REPLACE, not just delete): (c) Peer.PSK (~:569-579) — replace 'No datapath code path consumes PSK yet; it is parsed, validated, and exposed only' with the real consumers: device.go calls cfg.PeerIdentities() to derive each peer's effective PSK, and bind/multipath.go consumes those per-peer PSKs for the peerBySource PROBE-authenticated demux; (d) Peer.Name (~:580-585) — replace 'Not yet consumed by any datapath code path' with: surfaces (for additional concentrator peers) as the metrics 'peer' label via BoundPeerNames/PeerSnapshot.Name. Comment-only — touch no code. dependsOn T136."
- acceptance: "`grep -nE 'not yet consumed|No datapath code path|CONFIG SURFACE only|config surface only' internal/config/config.go` returns nothing; the replacement comments name PeerIdentities()/peerBySource demux (PSK) + the metrics peer label (Name), and selectDeviceBinds/multipath.AddPath (BindMode). `go build ./... && just lint` stay green (comment-only diff)."
- suggestedModel: fast
- dependsOn: ["T136"]
- ledgerRefs: ["goals:G11","defects:D57","defects:D60"]

### T139 — done

- createdAt: 2026-07-14T10:09:48.135Z
- updatedAt: 2026-07-14T15:06:39.783Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Consolidate the superseded PathSnapshots/FECSnapshot bind read seams onto PeerSnapshots (D56)
- description: "After T94 migrated the device metrics adapter to Multipath.PeerSnapshots(), the primary-only seams Multipath.PathSnapshots (internal/bind/multipath.go ~:2701) and Multipath.FECSnapshot (~:2085) have NO remaining production callers — only bind's own tests (fec_test.go, traffic_test.go, ~9 call sites) — and PeerSnapshots COPY-PASTES FECSnapshot's honest Recovered/Unrecoverable delivered-count derivation (the comment admits it 'mirrors ... verbatim'), a two-copy DRIFT RISK on a non-trivial rule. Preferred fix: migrate the bind test call sites to PeerSnapshots() (single-peer tests read PeerSnapshots()[0].Paths / [0].FEC) and DELETE PathSnapshots + FECSnapshot outright, so the delivered-count derivation lives EXACTLY ONCE; fall back to thin wrappers over PeerSnapshots()[0] ONLY if a test genuinely needs the old shape. Keep test assertions semantically identical (seam migration, not a behavior change); preserve the correctness of the delivered-count rule (the load-bearing part). Verify nothing outside internal/bind references the deleted seams (the device metrics adapter already consumes PeerSnapshots). Isolated to internal/bind. dependsOn T136."
- acceptance: The honest delivered-count FEC derivation exists in EXACTLY ONE place (no 'mirrors ... verbatim' duplicate — verify by grep); `grep -rn 'PathSnapshots|FECSnapshot(' internal/ cmd/ test/` shows no surviving callers of the deleted seams (or only the thin-wrapper fallback definitions). `go test ./internal/bind/... ./internal/device/... && just lint` pass.
- suggestedModel: standard
- dependsOn: ["T136"]
- ledgerRefs: ["goals:G11","defects:D56"]

### T140 — done

- createdAt: 2026-07-14T10:09:59.824Z
- updatedAt: 2026-07-14T11:36:40.387Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: "Reconcile the SO_BINDTODEVICE capability story: pathsock comment vs CAP_NET_ADMIN units vs docs (D40)"
- description: "internal/bind/pathsock_linux.go (~:9-10) claims bindToDevice 'requires CAP_NET_RAW', yet the shipped systemd units (packaging/systemd/wanbond-edge.service ~:24 + the concentrator twin) grant only CapabilityBoundingSet=CAP_NET_ADMIN — and device-bind SUCCEEDED on o3, so the comment and the unit disagree (D40). (1) Determine the real requirement: historically SO_BINDTODEVICE needed CAP_NET_RAW; Linux ≥5.7 allows it with no capability (verify the kernel commit/version via WebSearch and against the supported floor — Debian bookworm 6.1+, Ubuntu 22.04 5.15+, both ≥5.7). (2) EMPIRICALLY confirm on at least one standing worker (o3 aarch64 or llm-ubuntu-0 amd64, via `ssh -i /run/agenix/llm-ssh-key`): run a minimal setsockopt(SO_BINDTODEVICE) probe (or the daemon) under a CAP_NET_ADMIN-only bounding set and observe device-bind succeed; capture the command + output + `uname -r`. (3) Align all surfaces to the finding: the pathsock_linux.go bindToDevice comment (state the ≥5.7 rule + that the EPERM fallback covers pre-5.7 kernels), any other CAP_NET_RAW mention in internal/bind, the capability comment in BOTH packaging/systemd units, and docs/install.md's capability text. Do NOT widen the unit CapabilityBoundingSet unless the probe proves CAP_NET_RAW is actually required on a supported kernel. Keep the permission-error fallback intact. dependsOn T136 (serializes the pathsock.go editors: this comment vs the D45 QF1001 rewrite)."
- acceptance: A documented probe (command + output + `uname -r`) shows SO_BINDTODEVICE succeeding under a CAP_NET_ADMIN-only bounding set on a supported kernel. `grep -rn CAP_NET_RAW internal/ packaging/ docs/` shows no remaining claim contradicting the finding; the pathsock_linux.go comment, both unit files' comments, and docs/install.md state the same kernel-version-qualified rule with the source-IP fallback documented. `just lint && just test` stay green; unit CapabilityBoundingSet unchanged unless the probe proves CAP_NET_RAW is actually required.
- suggestedModel: standard
- dependsOn: ["T136"]
- ledgerRefs: ["goals:G11","defects:D40"]

## M51

### T141 — done

- createdAt: 2026-07-14T12:40:24.246Z
- updatedAt: 2026-07-14T16:04:57.958Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Extend the netns e2e fixture with a sustained-load driver and metrics/log sampling helpers
- description: "Q55 binds ALL per-task acceptance to the -tags e2e netns fixture, and flags that the fixture cannot yet (a) drive a SUSTAINED, rate-calibrated offered load through a running weighted-policy tunnel (frames/sec targeted above engage_fraction*per_path_capacity_fps and against the pacing capacity), (b) periodically sample the Exposition DURING that load, or (c) capture and grep the daemon's structured log stream for transition lines while the load runs. test/e2e/netns.go (SetupWithPaths/netemArgs) already provides per-path rateMbit caps and lossPct; metrics.Fetch (internal/metrics/scrape.go) scrapes /metrics; TestFixtureImpairment (test/e2e/fixture_impairment_test.go, T35) exposes rateMbit/lossPct. Build the shared capability as an up-front dependency for the observability and probe-protection e2e tasks. Add three reusable helpers to the e2e package: (1) a UDP load generator (target fps, payload size, duration; sender+sink across the tunnel); (2) a polling metrics sampler (poll Fetch every ~100-200ms, retain samples); (3) a structured-log capturer that asserts on daemon JSON log lines (liveness 'path liveness transition', 'scheduler pacer shedding', and the upcoming aggregation-transition line). Do NOT change production code. Keep DefaultPaths and existing TestFixtureImpairment behavior byte-identical; extend, do not modify, existing helpers. Update the test/e2e harness-contract doc comments."
- acceptance: "A new -tags e2e harness self-test under `just e2e`: with a weighted-policy daemon and a rate-capped path (~5 Mbit), the driver sustains a target offered load for >=5s within +/-20% (verified via wanbond_path_tx_bytes_total deltas from the sampler), the metrics sampler returns >=1 gauge sample scraped via metrics.Fetch during that window, and the log capturer returns >=1 expected structured line (e.g. the coalesced 'scheduler pacer shedding' record under deliberate overload). Existing e2e tests unaffected (DefaultPaths byte-identical). `go test` GREEN, `go test -tags e2e` (just e2e) GREEN, `just lint` across default+e2e+realhosts tags GREEN."
- suggestedModel: standard
- ledgerRefs: ["goals:G13"]
- resultCommit: b0f52e4

## M52

### T142 — done

- createdAt: 2026-07-14T12:40:34.697Z
- updatedAt: 2026-07-14T15:54:29.801Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Hard-fail config load when declared link_bandwidth proves weighted aggregation can never engage
- description: "Q52 hard-fail arm (Option 3), scoped by Q53 to the GUARD ONLY (G2/Q20 owns per_path_capacity_fps auto-derive + BDP-sizing docs — reference install.md 3a, do NOT restate; note deriveWeightedPacingFromBDP already exists in internal/config/config.go:955). In internal/config, AFTER normalize (so deriveWeightedPacingFromBDP and applyDefaults have produced the EFFECTIVE EngageFraction/PerPathCapacityFPS): when scheduler.policy=\"weighted\" and a path declares link_bandwidth (Path.LinkBandwidthBitsPerSec), compute impliedCapacityFPS = LinkBandwidthBitsPerSec / (8 * defaultAvgWireFrameBytes) using the SAME avg-wire-frame constant and math SizePacingFromBDP uses (so the guard and the BDP derive can never disagree). If EngageFraction*PerPathCapacityFPS > impliedCapacityFPS for any declared path (aggregation can mathematically never engage at line rate), Load fails fast with an actionable error naming the path and all three numbers (declared bandwidth, implied capacity fps, engage-threshold fps) and the fixes (lower per_path_capacity_fps, enable pacing to auto-derive it, or correct link_bandwidth). Interaction to respect: with pacing ENABLED + declared bandwidth the capacity is auto-derived (raw knobs mutually exclusive, config.go:972), so the guard chiefly bites when pacing is DISABLED (derive no-ops, synthetic 10000 default stands) or knobs are explicit. Document the new failure mode in docs/install.md's config-error list in the same change (AGENTS.md docs-sync)."
- acceptance: "-tags e2e under `just e2e` (fixture builds+runs the binary): (i) a daemon launched with policy=\"weighted\", link_bandwidth=\"8mbit\" (+link_rtt), pacing disabled, and default per_path_capacity_fps REFUSES to start, exiting non-zero with the actionable error naming the implied capacity fps and engage-threshold fps; (ii) the same config with per_path_capacity_fps lowered so EngageFraction*capacity <= impliedCapacityFPS starts and establishes the tunnel. `go test` GREEN, `just lint` (default+e2e+realhosts) GREEN."
- suggestedModel: standard
- ledgerRefs: ["goals:G13"]
- resultCommit: 6f906b3

### T144 — planned

- createdAt: 2026-07-14T12:41:01.308Z
- updatedAt: 2026-07-14T12:50:17.810Z
- author: "opus-4.8[1m]"
- session: 915ea040-10d3-4f13-9cf2-ed8e5149babb
- headline: Startup WARN and wanbond_weighted_capacity_sane gauge when weighted capacity is unverifiable
- description: "Q52 WARN arm. When policy=\"weighted\" and link_bandwidth is NOT FULLY declared, startup must NEVER be blocked. Config load computes a capacity-sanity verdict: SANE-VERIFIED (gauge=1, no WARN) ONLY when EVERY path declares link_bandwidth AND the T142 hard-fail guard passed; UNVERIFIABLE (gauge=0 + one WARN) otherwise. REVISION (R155 [fable]): the UNVERIFIABLE case covers BOTH 'no path declares bandwidth' AND a PARTIAL declaration (some paths declare, some don't) — a reachable state when pacing is DISABLED (deriveWeightedPacingFromBDP no-ops, internal/config/config.go:957). Pin it explicitly: partial declaration => WARN + wanbond_weighted_capacity_sane=0, WHILE T142's hard-fail guard STILL independently checks each DECLARED path (a declared path that contradicts still hard-fails at T142; this WARN concerns the UNdeclared paths that cannot be checked). In the unverifiable case the daemon logs ONE actionable startup WARN (message: declare link_bandwidth on ALL paths so capacity can be checked, or verify per_path_capacity_fps against the BDP sizing in install.md 3a — REFERENCE, do not restate, the G2-owned sizing docs). The metrics endpoint exposes a STATIC unlabeled gauge wanbond_weighted_capacity_sane: 1 when verified sane, 0 when unverifiable; the family is ABSENT entirely under non-weighted policy. Plumb the verdict from the loaded config through internal/device to collector registration as a static registered gauge alongside the Source-driven collector (config-derived, NOT per-peer, hence unlabeled and exempt from the labelPeer back-compat rule). Export the metric name as a constant. Update the metrics reference (README.md/docs/design.md) and docs/install.md in the same change."
- acceptance: "-tags e2e under `just e2e`, asserting on the daemon's OWN startProc combined log output (proc.log(), NOT the T141 sampler — so M52 stays an INDEPENDENT root; dependsOn remains [T142]): (a) weighted daemon with NO link_bandwidth on any path starts on the fixture, its combined output contains EXACTLY ONE capacity-sanity WARN line, and metrics.Fetch reads wanbond_weighted_capacity_sane == 0; (b) a PARTIAL declaration (link_bandwidth on some paths not all; pacing disabled; declared paths guard-consistent) also starts, emits EXACTLY ONE WARN, and reads gauge == 0; (c) link_bandwidth declared on ALL paths (guard-consistent capacity) starts with NO WARN and gauge == 1; (d) under active-backup policy the family is absent. `go test` GREEN, `just lint` (default+e2e+realhosts) GREEN."
- suggestedModel: standard
- dependsOn: ["T142"]
- ledgerRefs: ["goals:G13"]

## M53

### T143 — planned

- createdAt: 2026-07-14T12:40:52.205Z
- updatedAt: 2026-07-14T12:50:04.123Z
- author: "opus-4.8[1m]"
- session: 915ea040-10d3-4f13-9cf2-ed8e5149babb
- headline: Expose WeightedScheduler aggregation-gate snapshot and log engage/disengage transitions
- description: "Item 1 + Q54, sched-package half. internal/sched/weighted.go holds the gate state under s.mu — s.aggregating, the EWMA loadRate (fps), and the thresholds EngageFraction*PerPathCapacity / DisengageFraction*PerPathCapacity — but exposes NONE of it to any accessor. (1) Add a mutex-guarded snapshot accessor on *WeightedScheduler, e.g. AggregationSnapshot() returning {Aggregating bool, OfferedLoadFPS, EngageThresholdFPS, DisengageThresholdFPS}, as the read seam the metrics plumbing (T146) consumes. (2) REVISION (R155 [opus]): updateGateLocked (weighted.go:499-532) does NOT 'flip the gate silently' — it ALREADY emits s.log.Info(\"scheduler aggregation change\", \"to\", \"aggregating\"|\"collapsed\", \"load_fps\", s.loadRate [+ reason on collapse]) on every s.aggregating flip (lines 506/514/526). Do NOT add a second log line (that would DOUBLE-LOG every engage/disengage). Instead EXTEND that existing 'scheduler aggregation change' record with the MISSING structured fields — from (the prior gate state), engage_threshold_fps, disengage_threshold_fps — keeping the CANONICAL message string 'scheduler aggregation change', its existing to/load_fps/reason fields, and the one-shot-on-change semantics (parity with setActiveLocked's 'scheduler active path change', so a saturated Pick path does NOT log per-frame). Active-backup has no gate and is untouched. Pure sched-package change; NO metrics wiring here (T146). Document the extended log fields in docs/design.md's scheduler section in the same change (AGENTS.md docs-sync)."
- acceptance: "-tags e2e under `just e2e`: weighted-policy daemon with per_path_capacity_fps=250 (the empirical repro value) on the netns fixture; using the harness overload driver, offered load pushed above engage_fraction*250 makes the log capturer observe a 'scheduler aggregation change' record with to=\"aggregating\" carrying the NEW from + engage_threshold_fps + disengage_threshold_fps fields (alongside the existing load_fps); stopping the load yields a 'scheduler aggregation change' to=\"collapsed\" record within the collapse-dwell + EWMA-tau budget (wait derived from configured knobs, NO magic sleeps). Assert on the CANONICAL existing message string 'scheduler aggregation change' (NOT a new 'engaged/disengaged' string) and assert EXACTLY ONE record per flip (no double-log regression). `go test` GREEN, `just lint` (default+e2e+realhosts) GREEN."
- suggestedModel: standard
- dependsOn: ["T141"]
- ledgerRefs: ["goals:G13"]

### T146 — planned

- createdAt: 2026-07-14T12:41:29.073Z
- updatedAt: 2026-07-14T12:41:29.073Z
- author: "opus-4.8[1m]"
- session: 915ea040-10d3-4f13-9cf2-ed8e5149babb
- headline: Plumb per-peer aggregation gauges through the Bind snapshot, metrics.Source, and collector
- description: "Item 1 + Q54 (per-peer labels, labelPeer), metrics-plumbing half. Expose the four Q54 series to /metrics via the existing seam layers: wanbond_aggregation_engaged{peer} (bool gauge), wanbond_offered_load_fps{peer} (gauge), and the STATIC wanbond_aggregation_engage_threshold_fps{peer} / wanbond_aggregation_disengage_threshold_fps{peer} (gauges). (1) internal/bind: the Multipath per-peer snapshot (PeerSnapshots, consumed by internal/device/metrics.go metricsSource) gains the aggregation-gate snapshot for peers whose scheduler exposes it — type-assert peer.scheduler against a small optional reporter interface satisfied by *WeightedScheduler's AggregationSnapshot() (from T143); active-backup peers report nothing so the series are ABSENT. Read the snapshot without holding the send lock across Pick (consistent with how Estimate/FEC snapshots are read). (2) internal/metrics/metrics.go: add an AggregationSnapshot type and a Source.Aggregation() []AggregationSnapshot method (mirroring FEC()/Reseq()), emit the four gauges in collector.Collect honoring the EXISTING single-peer-omits-label back-compat rule (T94) already applied to FEC/reseq, and export the four metric names as constants next to MetricLoss/MetricRTT. Update the metrics reference in README.md/docs/design.md in the same change (AGENTS.md docs-sync)."
- acceptance: "-tags e2e under `just e2e`: (i) single-peer weighted daemon on the fixture — metrics.Fetch shows all four families; both threshold gauges equal the configured engage/disengage_fraction*per_path_capacity_fps within a small relative tolerance; wanbond_aggregation_engaged reads 0 at idle; (ii) under active-backup policy NONE of the four families is present; (iii) on the existing multi-peer concentrator fixture the series carry the peer label (Exposition.PeerValue resolves them). `go test` GREEN, `just lint` (default+e2e+realhosts) GREEN."
- suggestedModel: frontier
- dependsOn: ["T143"]
- ledgerRefs: ["goals:G13"]

### T147 — planned

- createdAt: 2026-07-14T12:41:42.007Z
- updatedAt: 2026-07-14T12:50:07.678Z
- author: "opus-4.8[1m]"
- session: 915ea040-10d3-4f13-9cf2-ed8e5149babb
- headline: "e2e: aggregation engage/disengage flip and configured-but-inert visibility scenarios"
- description: "The empirical acceptance for item 1: two -tags e2e scenarios on the netns fixture proving the operator blind spot is now directly observable. Scenario A (flip): weighted policy, per_path_capacity_fps=250; the harness sustained-load driver pushes offered load above the engage threshold; poll the metrics sampler until wanbond_aggregation_engaged==1 AND wanbond_offered_load_fps exceeds the engage-threshold gauge; stop the load and observe engaged return to 0 within the collapse-dwell + EWMA-tau budget (derive the wait from the configured knobs, NO magic sleeps); assert BOTH the engage and disengage transition log lines (from T143) for parity with the metric flips. Scenario B (configured-but-inert — the exact blind spot from the goal): DEFAULT per_path_capacity_fps (10000) with a modest sustained load — assert wanbond_aggregation_engaged stays 0 for the whole window WHILE wanbond_offered_load_fps reports a clearly non-zero value far below the engage-threshold gauge, i.e. 'policy=weighted but single-path behavior' is now measurable from /metrics instead of invisible. This task adds ONLY test code (relies on T143 log + T146 gauges); no production change."
- acceptance: "Both scenarios pass deterministically under `just e2e` (privileged netns fixture): scenario A observes wanbond_aggregation_engaged 0->1->0 with wanbond_offered_load_fps crossing the threshold gauges in the expected direction each time AND both 'scheduler aggregation change' log records captured (to=\"aggregating\" then to=\"collapsed\" — the CANONICAL message string from T143, NOT an 'engaged/disengaged' string); scenario B holds wanbond_aggregation_engaged==0 with 0 < wanbond_offered_load_fps < engage-threshold gauge for >=5s of sustained load. `go test` GREEN, `just lint` (default+e2e+realhosts) GREEN."
- suggestedModel: standard
- dependsOn: ["T141","T146"]
- ledgerRefs: ["goals:G13"]

## M54

### T145 — planned

- createdAt: 2026-07-14T12:41:14.250Z
- updatedAt: 2026-07-14T12:41:14.250Z
- author: "opus-4.8[1m]"
- session: 915ea040-10d3-4f13-9cf2-ed8e5149babb
- headline: "Reserve probe headroom in the weighted pacer: exempt-but-charged probe accounting"
- description: "Item 3(ii) + Q51 (PROBE-frame protection only; inner-ICMP explicitly OUT of scope). GROUNDING (load-bearing, confirmed by BOTH candidate planners): wanbond's own PROBE frames (frame.KindProbe) do NOT traverse scheduler.Pick — emitProbes (internal/bind/probe.go) writes them directly to each path socket, bypassing Send->Pick->token-bucket. So a ClassControl-style pacer EXEMPTION does not apply; the failure mode is that the pacer budgets ZERO headroom for probes, so a pace sized at ~link rate lets paced DATA + the probe stream (plus reflected echoes) oversubscribe the link, building the standing qdisc queue that delays/drops probes past DownAfter (1200ms, internal/telemetry/liveness.go) -> spurious path-DOWN / failover flap. REPRODUCE-FIRST: land a failing -tags e2e that observes the spurious path-down under sustained overload BEFORE the fix (confirm it fails for THIS reason). Then implement exempt-but-charged probe accounting: add a small optional interface (e.g. ProbeBudget{AccountProbe(pathIdx int)}) implemented by *WeightedScheduler — deduct one token from the path's pacing bucket per emitted probe WITHOUT ever shedding or delaying the probe (strict priority: bucket may briefly go negative / pre-drain so subsequent ClassData Picks yield) — and call it from the bind's probe emission AND the echo-reflection write in dispatchInbound (symmetric). ClassControl semantics stay EXACTLY as D22 (exempt AND uncharged) — do not re-plan. Codify the three-tier invariant in the FrameClass/Pick contract comments (internal/sched/scheduler.go) and docs/design.md priority model: ClassControl exempt-uncharged, KindProbe exempt-but-charged, ClassData fully paced."
- acceptance: "-tags e2e under `just e2e` (reproduce-first): weighted + pacing daemon on a rate-capped netns path with pace sized at ~the link rate; the harness driver sustains ClassData overload >= 2x pacing capacity for >= 10s (> 8x DownAfter). The regression test FAILS (observes a 'path liveness transition' to=down and/or wanbond_path_up->0 for the loaded path) with the probe accounting disabled/absent, and PASSES with it enabled: ZERO to=down transitions during the overload window, 'scheduler pacer shedding' lines ARE present (overload proven real), and wanbond_path_rtt_seconds for the loaded path stays below the DownAfter threshold throughout. `go test` GREEN (race detector per repo default), `just lint` (default+e2e+realhosts) GREEN."
- suggestedModel: frontier
- dependsOn: ["T141"]
- ledgerRefs: ["goals:G13"]

## M55

### T148 — planned

- createdAt: 2026-07-14T12:41:59.713Z
- updatedAt: 2026-07-14T12:41:59.713Z
- author: "opus-4.8[1m]"
- session: 915ea040-10d3-4f13-9cf2-ed8e5149babb
- headline: Document the pacing on/off tradeoff, the frame-class priority model, and inner-ICMP infeasibility
- description: "Item 3(a) docs + the Q51 infeasibility note, scoped by Q53 (reference — do NOT restate — G2/Q20's per_path_capacity_fps auto-derive/BDP-sizing docs). In docs/design.md (+ README.md operator section where appropriate): (a) the measured pacing ON/OFF tradeoff from the goal's empirical data — path split RTT-weighted ~71/29 (off) vs capacity-capped ~50/50 (on); bounded worst-case loaded RTT (757ms vs 1083ms) bought with reduced throughput (4.98 vs 6.93 Mbps) and deliberate shedding of ~33% excess; liveness stability under overload (pacing on + probe headroom keeps probes healthy; pacing off + sustained overload saturates the link queue and can flap liveness) — framed as an operability tradeoff with guidance on when to enable pacing; (b) the codified priority model: ClassControl exempt-uncharged (D22), KindProbe exempt-but-charged headroom (T145), ClassData fully paced/shed; (c) an EXPLICIT architectural note that inner-tunnel ICMP (or any inner flow) prioritization is INFEASIBLE — the WG tunnel payload is opaque ClassData to the pacer (classify.go reads only the inner WireGuard message TYPE word; plaintext DPI before encryption is out of architecture); (d) a short operability runbook stanza tying the new signals together: the four aggregation gauges, wanbond_weighted_capacity_sane, the engage/disengage and pacer-shedding log lines, and the hard-fail guard error. Do NOT write BDP/per_path_capacity_fps sizing guidance — REFERENCE install.md 3a (G2/Q20 owns it). NOTE (deliberate Q55 deviation): this is a PURE-DOCS task with no runtime surface to exercise via the netns fixture, so it is gated on `just lint` + reviewer prose-check rather than an -tags e2e test; the behavioral tasks (T143/T144/T146/T145) already carry the e2e acceptance and update their own operator docs in-change."
- acceptance: Every metric name, log-message string, and config-error phrase cited in the docs matches the exported constants and the exact strings the -tags e2e suite asserts (cross-checked by grep against the source constants and the e2e assertions in review); docs/design.md, README.md, and docs/install.md render consistently and contain the four sections (pacing on/off tradeoff, three-tier priority model, inner-ICMP-infeasible note, operability-signals runbook stanza) with a REFERENCE to G2's BDP sizing rather than a duplicate; `go test` GREEN and `just lint` (misspell/format across default+e2e+realhosts tags — covers doc fixtures per the T130 incident) GREEN.
- suggestedModel: standard
- dependsOn: ["T147","T144","T145"]
- ledgerRefs: ["goals:G13"]

## M57

### T149 — done

- createdAt: 2026-07-14T13:15:57.936Z
- updatedAt: 2026-07-14T15:53:37.851Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Extract the per-path token-bucket pacer into a shared internal/sched component
- description: "Behavior-preserving refactor to enable reuse by ActiveBackup. Move the pacing state + logic currently embedded in WeightedScheduler (internal/sched/weighted.go: tokens[]float64, lastFill/haveFill, refillLocked, tryConsumeLocked, shedLocked with the coalesced 'pacer shedding' rate-limited log, fullBuckets, and the PickPaced/PickNone sentinel semantics + ClassControl exemption per defect D22) into a standalone caller-locked helper type in a NEW internal/sched/pacer.go (e.g. a `pacer` holding tokens[], lastFill, haveFill, shedCount, lastShedLog and a config of CapacityFPS + BurstFrames). Refactor WeightedScheduler to delegate to it with ZERO behavioral change (it still guards the pacer under s.mu). Pure refactor — no config change, no ActiveBackup change."
- acceptance: "`nix develop -c just test` passes with internal/sched/weighted_test.go UNCHANGED — every existing weighted pacing test green against the delegating impl (token refill, burst bound, TestWeightedPacingBoundsEgressAndBacklog, ClassControl exemption, PickPaced shed, shed-log coalescing, sentinel distinctness); internal/sched/pacer.go exists and weighted.go delegates refill/consume/shed to it; `nix develop -c go vet ./internal/sched/...` clean."
- suggestedModel: frontier
- ledgerRefs: ["goals:G14","defects:D65"]
- resultCommit: f387831

### T150 — planned

- createdAt: 2026-07-14T13:16:09.195Z
- updatedAt: 2026-07-14T13:27:58.739Z
- author: "opus-4.8[1m]"
- session: 7295f080-20fa-4cf9-afac-0357b4cf65cb
- headline: Add BDP-sized send-pacing to the ActiveBackup scheduler
- description: |
    Give ActiveBackup an optional PER-PATH token-bucket pace using the shared pacer, so the DEFAULT scheduler shapes a single uplink to its drain rate and cannot self-inflict the ~1s Starlink bufferbloat (D65 primary fix). Extend sched.Config (active-backup config) with pacing fields: Pacing bool, plus PER-PATH capacity/burst SLICES — PerPathCapacities []float64 and PacingBursts []float64, index-aligned to the health/priority slice — NOT the single shared scalar the weighted scheduler carries.
    
    CRITICAL DESIGN CONSTRAINT (R162 criticism 1): active-backup egresses on exactly ONE path at a time, so each path's bucket MUST be sized from ITS OWN link_bandwidth/link_rtt BDP. Do NOT reuse weighted's shared-scalar BOTTLENECK sizing (config.go deriveWeightedPacingFromBDP: min CapacityFPS across all paths applied to every bucket) — that would cap a faster active primary at the slowest backup's declared rate (e.g. a Starlink primary paced to a 5G backup's rate), reimposing the exact artificial single-flow ceiling this goal exists to remove.
    
    In Pick(class): when pacing enabled, consume one token from the ACTIVE path's OWN bucket for a ClassData frame, return PickPaced (not the active index) when that bucket is empty, and EXEMPT ClassControl (spend no token, never shed) exactly as weighted does (D22); refill on each Pick. Per-path buckets so a failover to a different active path draws from that path's own (full) bucket at that path's own rate.
    
    Bucket-consistency across membership changes (T30) — REQUIRED to avoid a Close→Open panic: initialize the per-path bucket slice in NewActiveBackup (mirroring WeightedScheduler.fullBuckets init), and resize/reset it in AddPath, RemovePath, AND SetPaths (internal/sched/active_backup.go:143 — the Close→Open durable-membership path that replaces s.health wholesale). If SetPaths swaps in a different path count without resizing the bucket/capacity slices, the next Pick indexes tokens[] out of range and panics. Recompute stays strictly non-consuming. When pacing disabled the buckets are inert and Pick is byte-for-byte today's behavior.
    
    Validate the new fields in NewActiveBackup (when Pacing on: each PerPathCapacity>0 and each PacingBurst>0, and len(PerPathCapacities)==len(PacingBursts)==len(health)), failing fast. Pacer stays under the existing selection lock — no new deadlock path into the Bind (PickPaced is already handled at internal/bind/multipath.go:1981-1989 as errPacerShedding, so NO Bind change).
    
    ALSO correct ALL THREE now-stale doc comments the change falsifies (none catchable by `just lint` — they stay grammatically well-formed): (a) internal/sched/scheduler.go:16-20 — the PickPaced constant doc ('Only a pacing-enabled weighted scheduler ever returns it'); (b) internal/sched/scheduler.go:59-62 — the Scheduler interface class doc ('A non-pacing scheduler (active-backup) ignores class'); (c) internal/sched/active_backup.go:176-179 — ActiveBackup.Pick's doc ('class is ignored: active-backup has no pacer'). All three must be reworded to state that active-backup paces (and honors ClassControl exemption) when configured.
- acceptance: "Reproduction-first: a NEW test in internal/sched/active_backup_test.go asserting that with Pacing=true and HETEROGENEOUS per-path capacities (e.g. path0=1000fps, path1=200fps, PacingBurst=8 each), offering ~5000 ClassData Picks over an advancing fakeClock, the ACTIVE path's admits are bounded by THAT path's OWN PerPathCapacity*T + PacingBurst — and a fast active primary is explicitly NOT capped at the slow backup's rate — while the rest return PickPaced (distinct from PickNone), and a ClassControl frame is admitted even with an empty bucket; written and observed to FAIL for the right reason before the impl (no pacing today), PASS after. All THREE stale doc comments (scheduler.go:16-20 PickPaced const, scheduler.go:59-62 interface class doc, active_backup.go:176-179 Pick doc) are corrected to state active-backup paces when configured (verify by reading each). `nix develop -c just test` green; `nix develop -c go vet ./internal/sched/...` clean."
- suggestedModel: frontier
- ledgerRefs: ["goals:G14","defects:D65"]
- dependsOn: ["T149"]

### T151 — planned

- createdAt: 2026-07-14T13:16:15.348Z
- updatedAt: 2026-07-14T13:28:06.752Z
- author: "opus-4.8[1m]"
- session: 7295f080-20fa-4cf9-afac-0357b4cf65cb
- headline: Unit-test ActiveBackup pacing edge cases
- description: "Add the remaining active-backup pacing coverage beyond the primary bound-test bundled with the impl, reusing newFakeClock()/advance and the synthetic PathHealth drivers already in the sched test package: (a) pacing DISABLED is a pure no-op — Pick admits every frame on the active path exactly as pre-change (regression guard); (b) FAILOVER — the active path changes and pacing then draws from the NEW active path's OWN bucket at that path's own rate (a saturated old primary does not starve the backup, and a fast backup is not throttled to the old primary's rate); (c) sentinel distinctness — PickPaced (healthy-but-paced) vs PickNone (no eligible path) returned in the correct distinct situations; (d) ClassControl exemption holds both at cold start and after sustained shedding; (e) burst absorption — a burst <= PacingBurst after idle is admitted without shed; (f) Close→Open MEMBERSHIP CHANGE (T30 pacer regression, R162 criticism 3) — a SetPaths that CHANGES the path count resizes/reinitializes the per-path bucket+capacity slices so the next Pick indexes in range and does NOT panic, and then paces correctly against the new membership (proves NewActiveBackup init + SetPaths resize keep tokens[] length == len(health)). Assertions must be non-vacuous (verify by the count bound / observed pace, not by absence of error)."
- acceptance: "`nix develop -c just test` green with the new cases present; `nix develop -c go test ./internal/sched/ -run ActiveBackup -v` lists and passes all scenarios including the Close→Open-with-different-path-count case (asserted to complete without panic and to pace on the new membership); a coverage check shows the pacing branches in active_backup.go Pick and the SetPaths/NewActiveBackup bucket-init paths are exercised."
- suggestedModel: standard
- ledgerRefs: ["goals:G14","defects:D65"]
- dependsOn: ["T150"]

## M59

### T152 — planned

- createdAt: 2026-07-14T13:16:26.891Z
- updatedAt: 2026-07-14T13:28:25.307Z
- author: "opus-4.8[1m]"
- session: 7295f080-20fa-4cf9-afac-0357b4cf65cb
- headline: "Generalize the [scheduler] pacing config to be policy-independent (with fail-fast)"
- description: |
    Make egress pacing a policy-independent config surface usable under the DEFAULT active-backup policy, not only weighted (D65 design decision: policy-independent knob). internal/config/config.go: (1) split the pacing knobs (PerPathCapacityFPS, PacingBurstFrames, PacingEnabled) from the weighted-only aggregation/weight knobs (engage/disengage/collapse/load_tau/weight_*). (2) Rename deriveWeightedPacingFromBDP → derivePacingFromBDP and change its gate from `Policy==PolicyWeighted && PacingEnabled` to `PacingEnabled` for BOTH policies.
    
    CRITICAL — PER-PATH sizing under active-backup (R162 criticism 1): derivePacingFromBDP must branch on policy for HOW it sizes. Under WEIGHTED keep the existing shared BOTTLENECK scalar (min CapacityFPS across paths → single PerPathCapacityFPS/PacingBurstFrames) byte-identical, because weighted stripes all paths simultaneously and a faster path outrunning the bottleneck would build the standing queue. Under ACTIVE-BACKUP produce PER-PATH capacities: run SizePacingFromBDP once per path over ITS OWN link_bandwidth/link_rtt and fill a per-path capacity/burst vector (surfaced to sched.Config as PerPathCapacities/PacingBursts, plumbed through T153) — do NOT min-reduce to the bottleneck, since only one path egresses at a time and a fast active primary must pace at its OWN drain rate (bottleneck sizing here reimposes the D65 ceiling). Keep the all-paths-or-none link_bandwidth rule, the raw-knobs-vs-link_bandwidth mutual-exclusion, and the per-path link_rtt>0 requirement under BOTH policies. (3) In applyDefaults, when policy is active-backup AND pacing_enabled, default the pacing knobs (today applyDefaults early-returns for non-weighted). (4) validate: enforce pacing_burst_frames>0 and per_path_capacity_fps>0 when pacing_enabled under active-backup, keeping weighted-only knobs inert/unvalidated under active-backup. FAIL-FAST (critical, from D65 mechanism): pacing_enabled under active-backup with NEITHER all-paths link_bandwidth+link_rtt NOR explicit per_path_capacity_fps+pacing_burst_frames is a LOAD ERROR — the weighted synthetic default (~10000 fps) must NOT silently apply under active-backup, because a nominally-enabled-but-UNBINDING pace would reproduce D65 while claiming to shape. Weighted-policy behavior stays byte-identical.
    
    ALSO (R162 criticism 4) correct the now-stale weighted-only claims in the code doc-comments this change falsifies: config.go:158-161 (SchedulerConfig doc: 'every weighted knob is ignored'/pacing weighted-only framing), config.go:496-501 (Path.LinkBandwidthBitsPerSec: 'size the weighted scheduler's per-path pace ... when the weighted policy runs'), and config.go:507-510 (Path.LinkRTT: 'under the weighted policy'). Reword each to state pacing/link_bandwidth/link_rtt now size the pace under BOTH the weighted and the default active-backup policy.
- acceptance: "`nix develop -c just test` green with new config cases: (i) active-backup + pacing_enabled + explicit per_path_capacity_fps/pacing_burst_frames loads+validates; (ii) active-backup + pacing_enabled + link_bandwidth+link_rtt on ALL paths sizes PER-PATH capacities from EACH path's OWN BDP via SizePacingFromBDP — asserted a HETEROGENEOUS link set yields DISTINCT per-path capacities (the faster link gets the higher CapacityFPS, NOT min-reduced to the bottleneck); (iii) active-backup + pacing_enabled with NO bandwidth and NO explicit knobs FAILS at load with a named error (no silent 10000fps default); (iv) partial link_bandwidth and setting both raw knobs+link_bandwidth each fail fast; (v) active-backup WITHOUT pacing keeps every weighted knob zero/inert; (vi) all pre-existing weighted config tests pass unchanged (weighted still bottleneck-sized). The three stale weighted-only doc comments (config.go:158-161, 496-501, 507-510) are reworded to cover active-backup (verify by reading each)."
- suggestedModel: frontier
- ledgerRefs: ["goals:G14","defects:D65"]
- dependsOn: ["T150"]

### T153 — planned

- createdAt: 2026-07-14T13:16:32.388Z
- updatedAt: 2026-07-14T13:28:32.311Z
- author: "opus-4.8[1m]"
- session: 7295f080-20fa-4cf9-afac-0357b4cf65cb
- headline: Wire pacing config into selectScheduler's active-backup branch
- description: "internal/device/device.go selectScheduler (the default/active-backup branch, ~device.go:803): pass the validated pacing configuration into sched.Config so a configured pace actually reaches ActiveBackup — set Pacing=cfg.Scheduler.PacingEnabled and the PER-PATH capacity/burst vectors (PerPathCapacities / PacingBursts, index-aligned to the path/health priority order the scheduler is built over) derived by T152's derivePacingFromBDP, alongside the existing FailbackAfter. Per-path (NOT a single shared scalar) so each path's bucket paces at its OWN drain rate under active-backup (R162 criticism 1); ensure the capacity/burst vector order matches the health-slice order handed to NewActiveBackup. Leave the weighted branch unchanged (it keeps its shared bottleneck scalar). The existing health prober set suffices (active-backup needs only health). No other composition-root change."
- acceptance: "`nix develop -c go build ./...` succeeds; a device/config-to-scheduler test asserts a config with policy=active-backup + pacing_enabled=true builds an ActiveBackup whose per-path buckets carry the per-path capacities (a heterogeneous link set → distinct per-path pace, fast path not throttled to the bottleneck) and whose Pick sheds (PickPaced) under sustained overload, and pacing_enabled=false builds the byte-for-byte pre-change behavior. `nix develop -c just test` (incl. ./internal/device/...) green."
- suggestedModel: standard
- ledgerRefs: ["goals:G14","defects:D65"]
- dependsOn: ["T150","T152"]

### T154 — planned

- createdAt: 2026-07-14T13:16:37.344Z
- updatedAt: 2026-07-14T13:28:39.766Z
- author: "opus-4.8[1m]"
- session: 7295f080-20fa-4cf9-afac-0357b4cf65cb
- headline: Config unit tests for active-backup pacing and BDP sizing
- description: "Focused config-layer tests locking the new surface (complementary to those bundled with the generalization task): policy=active-backup + pacing round-trips through Load/normalize/validate; PER-PATH BDP sizing under active-backup (R162 criticism 1) produces a per-path capacity for EACH path equal to SizePacingFromBDP of THAT path's OWN link_bandwidth/link_rtt — a PER-PATH parity table test (NOT the weighted shared-bottleneck parity): for identical inputs, each active-backup path's PerPathCapacities[i]/PacingBursts[i] matches SizePacingFromBDP(path[i]), and a HETEROGENEOUS link set yields DISTINCT per-path capacities under active-backup (the faster link keeps its higher CapacityFPS) whereas the SAME inputs under weighted collapse to the single min-bottleneck scalar — the two sizings are asserted to DIFFER for a heterogeneous set. Plus: an existing active-backup config with NO pacing continues to load+validate with all weighted knobs zero (regression guard that the P1 empty-config surface is unchanged)."
- acceptance: "`nix develop -c go test ./internal/config/ -run Pacing` passes with the active-backup cases; a table test asserts each active-backup PerPathCapacities[i] equals SizePacingFromBDP of path[i]'s own link_bandwidth/link_rtt (per-path parity), and that a heterogeneous link set produces DISTINCT active-backup per-path capacities while the same inputs under weighted collapse to the single bottleneck scalar (the two are asserted unequal); the no-pacing active-backup regression case is green; `nix develop -c just test` green overall."
- suggestedModel: standard
- ledgerRefs: ["goals:G14","defects:D65"]
- dependsOn: ["T152"]

## M60

### T155 — planned

- createdAt: 2026-07-14T13:16:44.340Z
- updatedAt: 2026-07-14T13:28:50.880Z
- author: "opus-4.8[1m]"
- session: 7295f080-20fa-4cf9-afac-0357b4cf65cb
- headline: Sync docs + example config for default-path pacing
- description: |
    Per AGENTS.md docs-are-definition-of-done, update in the SAME change: README.md (config surface — pacing_enabled/link_bandwidth/link_rtt now meaningful under the DEFAULT active-backup policy); docs/design.md (scheduler section: pacing is a policy-independent egress shaper; its D65 motivation — unshaped default sender overruns a bloated last-mile buffer; the drop-at-head/no-internal-queue invariant; the PER-PATH (not bottleneck) sizing under active-backup vs the shared-bottleneck sizing under weighted; and a decision block recording the policy-independent-pacing choice); docs/install.md + docs/runbook.md (operator guidance: on a bufferbloated uplink like Starlink, declare link_bandwidth+link_rtt on ALL paths and set pacing_enabled=true under the default policy, with example values); wanbond.example.toml (a commented [scheduler] pacing block valid under active-backup). Config key names must match across all files. Reference D65 as the motivating defect.
    
    ALSO (R162 criticism 4) CORRECT the now-contradictory weighted-only pacing claims in the existing docs this change falsifies: docs/install.md §3z's [scheduler] block (the statement 'Every knob below applies ONLY to weighted; under active-backup they are inert') AND its per-key comments for pacing_enabled/per_path_capacity_fps/pacing_burst_frames/link_bandwidth/link_rtt, and wanbond.example.toml's MIRRORED per-key comments — reword so the pacing/BDP keys are documented as applying under active-backup too, while the genuinely weighted-only aggregation knobs (engage/disengage/collapse/load_tau/weight_*) stay marked weighted-only. Keep the two files' comments consistent with each other and with the code doc-comments corrected in T152.
- acceptance: grep confirms README.md, docs/design.md, docs/install.md, docs/runbook.md, wanbond.example.toml each state pacing is available under active-backup with identical key names; the design.md decision block records the policy-independent-pacing choice AND the per-path-vs-bottleneck sizing distinction; wanbond.example.toml still loads via the config tests; an ABSENCE grep across docs/install.md and wanbond.example.toml finds NO remaining 'only under weighted' / 'ONLY to weighted' / weighted-only claims attached to the pacing/link_bandwidth/link_rtt keys (aggregation-knob weighted-only notes may remain); `nix develop -c just lint` (misspell/doc checks) passes on the changed files.
- suggestedModel: standard
- ledgerRefs: ["goals:G14","defects:D65"]
- dependsOn: ["T152","T153"]

### T156 — planned

- createdAt: 2026-07-14T13:16:51.364Z
- updatedAt: 2026-07-14T13:17:31.107Z
- author: "opus-4.8[1m]"
- session: 7295f080-20fa-4cf9-afac-0357b4cf65cb
- headline: Add the D65 field-validation procedure to docs/manual-checklist.md
- description: "The on-hardware validation was waived pre-fix (Q56) and deferred to the field pass — script it so it is executable verbatim: three-way iperf3 attribution (direct-WAN no-tunnel / through-tunnel / loopback-netns tunnel) plus a loaded-RTT A/B with pacing OFF vs ON, on the Pi4-edge/Starlink/o3 topology. Record the expected observations: single-flow TCP through the paced tunnel approaches the UDP-goodput ceiling (~6.9 Mbps measured pre-fix) instead of ~3.67 Mbps; loaded RTT no longer builds toward ~1s; retransmits drop from ~13/10s. Note the AGENTS.md rule that netns/e2e fixtures must NOT assert absolute throughput — this belongs to the manual/real-host tier only."
- acceptance: docs/manual-checklist.md contains the exact iperf3 command lines for all three legs, the pacing on/off A/B toggle steps (config diff), and an expected-observation table; no netns e2e test asserts absolute throughput (unchanged); `nix develop -c just lint` doc checks pass.
- suggestedModel: fast
- ledgerRefs: ["goals:G14","defects:D65"]
- dependsOn: ["T155"]

### T157 — planned

- createdAt: 2026-07-14T13:16:57.835Z
- updatedAt: 2026-07-14T13:17:33.128Z
- author: "opus-4.8[1m]"
- session: 7295f080-20fa-4cf9-afac-0357b4cf65cb
- headline: "Green definition-of-done gate: nix develop -c just build && just test && just lint"
- description: "On the COMPOSED tree (all pacing + config + wiring + docs + MSS-clamp-docs tasks merged), run the full project definition-of-done and fix any residual fallout. Per project discipline the gate is `nix develop -c just build`, `just test`, AND `just lint` — golangci-lint + go vet across the default, e2e, AND realhosts build tags (not `go test` alone): a lint-only regression in a tag-guarded test helper referencing changed SchedulerConfig/sched symbols, an unused symbol orphaned by the pacer extraction, a stale doc-comment lint, or a misspell in the new docs MUST be caught here. Terminal integration node — depends on every code and doc task across both tracks."
- acceptance: "`nix develop -c just build` && `nix develop -c just test` && `nix develop -c just lint` all exit 0 on the composed tree; `gofmt -l cmd internal test` is empty; `git status` shows README.md/docs/* updated in sync with the code (no doc drift)."
- suggestedModel: standard
- ledgerRefs: ["goals:G14","defects:D65"]
- dependsOn: ["T151","T153","T154","T155","T156","T158","T159"]

## M58

### T158 — done

- createdAt: 2026-07-14T13:17:07.335Z
- updatedAt: 2026-07-14T15:43:16.750Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Document the forwarded-TCP MSS clamp operator recipe
- description: "Deliver the SECONDARY D65 fix as a documented OPERATOR step (design decision: the daemon owns only the tunnel engine and stays free of privileged shell-outs per the internal/device invariant, and the repo already treats ALL firewall/routing plumbing as operator-owned via the oneshot-unit pattern — so the clamp is an operator recipe, NOT a daemon-installed rule). Add to docs/install.md §9.2 (the full-tunnel / route-a-client-LAN forwarding recipe) and docs/runbook.md's firewall-persistence step the two rules verbatim from docs/p1-mtu.md: `iptables -t mangle -A FORWARD -o wanbond0 -p tcp --tcp-flags SYN,RST SYN -j TCPMSS --clamp-mss-to-pmtu` plus the ip6tables equivalent — on BOTH forwarding nodes (edge AND concentrator), scoped to FORWARDED traffic, with persistence guidance matching the recipe's existing pattern, a cross-reference to docs/p1-mtu.md's MSS-clamping section for the arithmetic, one line on why --clamp-mss-to-pmtu is preferred over --set-mss (tracks inner-MTU retuning), and an explicit statement that omitting it lets forwarded TCP emit segments that fragment/PMTU-blackhole (the D65 compounding fault). Keep the existing MSS=1361 accounting in p1-mtu.md consistent. Docs only."
- acceptance: grep confirms docs/install.md §9.2 and docs/runbook.md each contain BOTH clamp rules (iptables AND ip6tables) with -o wanbond0 and --clamp-mss-to-pmtu, a persistence note, a link to docs/p1-mtu.md, and name both edge and concentrator as clamp points; the recipe is an operator step (no daemon shell-out); `nix develop -c just lint` doc checks pass.
- suggestedModel: standard
- ledgerRefs: ["goals:G14","defects:D65"]
- resultCommit: "0414854"

### T159 — done

- createdAt: 2026-07-14T13:17:11.561Z
- updatedAt: 2026-07-14T16:00:27.279Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Record the MSS-clamp gap closure in wanbond-fixes.md
- description: wanbond-fixes.md's C3 full-tunnel / route-a-client-LAN recipe (the deploy notes the D65 root cause cites as omitting the clamp) gains the TCPMSS --clamp-mss-to-pmtu FORWARD rule as a REQUIRED step of the routed-client-LAN setup, marked as closing the D65 compounding fault, pointing at the now-updated docs/install.md §9.2 recipe rather than duplicating the rule syntax at length.
- acceptance: wanbond-fixes.md C3 (or an adjacent entry) names the TCPMSS --clamp-mss-to-pmtu FORWARD rule as a required recipe step and references docs/install.md §9.2 and D65; `nix develop -c just lint` doc checks pass.
- suggestedModel: fast
- ledgerRefs: ["goals:G14","defects:D65"]
- dependsOn: ["T158"]
- resultCommit: 479a231
