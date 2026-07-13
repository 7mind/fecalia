---
ledger: tasks
counters:
  milestone: 0
  item: 66
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

## M16

### T58 — planned

- createdAt: 2026-07-13T13:42:19.742Z
- updatedAt: 2026-07-13T13:42:19.742Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: "Realhosts tier: throughput-aggregation ratio + loaded-RTT (bufferbloat) across the two standing hosts"
- description: "Extend the -tags realhosts tier (test/realhosts runner.go SSH + provision.go) to run across llm-ubuntu-0 (amd64 symmetric-NAT EDGE) <-> o3.7mind.io (aarch64 PUBLIC concentrator 89.168.124.91) and record: (1) per-path throughput and BONDED-vs-SUM aggregation ratio; (2) LOADED RTT vs idle RTT (bufferbloat) under sustained transfer, with pacing enabled using the W2 per-link bandwidth config (exercises T53). Report-only per M10/Q12 -- print/emit the numbers, assert only liveness (tunnel came up, transfer completed), NO absolute-number gate. Reuses the standing hosts' llm SSH key path; must not deprovision o3. This is the measurement the CPU-bound netns fixture cannot produce."
- acceptance: "`just realhosts` (go test -tags realhosts) runs against the two standing hosts, brings up the bonded tunnel, and emits per-path throughput, bonded-vs-sum ratio, and idle-vs-loaded RTT to the test log; the test passes on liveness (no absolute-throughput assertion). Hardware-validated: attach a captured run log. `go vet -tags realhosts ./test/realhosts` clean."
- suggestedModel: standard
- ledgerRefs: ["goals:G2"]
- dependsOn: ["T53"]

### T63 — planned

- createdAt: 2026-07-13T13:43:06.136Z
- updatedAt: 2026-07-13T13:43:06.136Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: "Realhosts tier: mid-transfer WAN kill for link AND hub failover under real conditions"
- description: "Extend the realhosts tier (building on T58) with a deliberate MID-TRANSFER kill: (1) LINK failover -- drop one WAN path (iptables on the edge host) mid-transfer and confirm the transfer continues over the survivor path; (2) HUB failover -- exercise the Q18 active-standby switch (T57) by making the active concentrator endpoint unreachable (iptables on o3, live-firewall OK, never deprovision) and confirm the edge fails over to a standby endpoint and the transfer resumes. Report-only (M10/Q12): assert transfer resumes/completes, emit failover timing, NO absolute-number gate. Uses the llm SSH key; must not deprovision o3."
- acceptance: "`just realhosts` runs the WAN-kill sub-tests against the standing hosts: transfer survives a mid-transfer single-link kill; transfer survives a mid-transfer active-concentrator kill by switching to a standby endpoint; failover timings emitted to the log. Passes on liveness (transfer completes). Hardware-validated: attach the captured run log."
- suggestedModel: standard
- ledgerRefs: ["goals:G2"]
- dependsOn: ["T58","T57"]

### T64 — planned

- createdAt: 2026-07-13T13:43:11.499Z
- updatedAt: 2026-07-13T13:43:11.499Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: "Realhosts tier: short report-only soak across the two standing hosts"
- description: "Add a SHORT report-only soak to the realhosts tier (building on T58): sustained transfer over the bonded tunnel across llm-ubuntu-0 <-> o3 for a bounded duration (minutes, not hours -- the long soak runs DURING the supervised pilot per Q19, not as a pre-gate), sampling throughput, RTT, loss, and rekey/liveness health over the window. Report-only per M10/Q12: emit the time series / summary, assert only that the tunnel stayed up and the transfer completed (no absolute-number gate). Must not deprovision o3."
- acceptance: "`just realhosts` runs the bounded soak: tunnel stays up for the full window, transfer completes, and per-sample throughput/RTT/loss/health are emitted to the log. Passes on liveness only. Hardware-validated: attach the captured soak log."
- suggestedModel: standard
- ledgerRefs: ["goals:G2"]
- dependsOn: ["T58"]

## M17

### T59 — planned

- createdAt: 2026-07-13T13:42:26.004Z
- updatedAt: 2026-07-13T13:42:26.004Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: "Rollout runbook: config/key/PSK generation, firewall persistence, monitoring, standby-concentrator setup"
- description: "Write the pre-pilot ROLLOUT RUNBOOK (docs, per CORE SCOPE 3): concentrator + edge config generation, WireGuard key + PSK generation, the already-done concentrator firewall persistence (D7/D8, reboot-persistent deduped iptables on o3), /metrics monitoring + health checks, and the STANDBY-CONCENTRATOR setup that the Q18 ordered-endpoint list (T53/T54) now enables. Documents the shipped pacing config key (T53) and concentrator-endpoint list (T54). Doc task; keep README/docs/install.md consistent."
- acceptance: docs/install.md (or a new docs/runbook) contains a complete rollout runbook covering key/PSK generation, both-ends config, firewall persistence, monitoring, and standby-concentrator configuration; an operator can provision a fresh edge+hub pair by following it. Markdown links resolve; README indexes it.
- suggestedModel: standard
- ledgerRefs: ["goals:G2"]
- dependsOn: ["T53","T54"]

### T65 — planned

- createdAt: 2026-07-13T13:43:27.916Z
- updatedAt: 2026-07-13T13:43:27.916Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: Automate the manual-checklist section-P0 real-link baseline into a repeatable pre-pilot procedure
- description: Turn the docs/manual-checklist.md section-P0 real-link baseline (currently manual steps) into a repeatable, mostly-automated pre-pilot procedure, reusing the realhosts harness/tooling extended in T58/T63 (runner.go SSH + provision.go against llm-ubuntu-0 <-> o3). Provide a single invocation (a just target or script) that provisions both ends, brings up the tunnel, runs the aggregation + loaded-RTT + link/hub failover smoke, and emits a baseline report. Keep it report-only (Q19 non-blocking). Update docs/manual-checklist.md so the P0 section points at the automated procedure (documenting any steps that remain manual).
- acceptance: "A single documented command runs the P0 baseline end-to-end against the standing hosts and emits a baseline report (aggregation ratio, loaded RTT, failover smoke); docs/manual-checklist.md P0 section is updated to reference it. Hardware-validated: attach the captured baseline report. `go vet -tags realhosts ./test/realhosts` clean."
- suggestedModel: standard
- ledgerRefs: ["goals:G2"]
- dependsOn: ["T58","T63"]

### T66 — planned

- createdAt: 2026-07-13T13:43:45.246Z
- updatedAt: 2026-07-13T13:43:45.246Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: Record non-blocking pilot exit criterion + full README/design.md/install.md/manual-checklist.md doc-sync
- description: "Final documentation sweep for G2 (CORE SCOPE 3 + Q19). (1) Record the NON-BLOCKING pilot exit criterion explicitly: capped-fixture aggregation/bufferbloat measurement (W2) + report-only real-link smoke (W4) are SUFFICIENT to proceed to a supervised pilot; the longer soak runs DURING the pilot, not as a pre-gate. (2) Full doc-sync per AGENTS.md across README.md, docs/design.md ('Not yet built' L232-251 -- move the now-built items: startup resilience, pacing sizing, hub failover, real-link tier), docs/install.md, docs/manual-checklist.md, so no doc still describes these as unbuilt/deferred and the pacing config key + concentrator-endpoint list + tolerant-startup behavior are all documented consistently. This is the last task; it reconciles the docs touched piecemeal by W1-W4 into a coherent whole."
- acceptance: docs/design.md 'Not yet built' no longer lists the G2-delivered items; README/design.md/install.md/manual-checklist.md consistently document startup tolerance, pacing sizing+config key, hub failover+endpoint list, and the real-link tier; the non-blocking exit criterion is stated in the pilot section. `grep` for stale 'not yet built'/'unmeasured'/'single concentrator' phrasing returns nothing describing delivered work. Markdown links resolve.
- suggestedModel: standard
- ledgerRefs: ["goals:G2"]
- dependsOn: ["T56","T59","T65","T60","T61","T62","T63","T64"]
