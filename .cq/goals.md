---
ledger: goals
counters:
  milestone: 0
  item: 1
archives: []
---

# goals

## M1

### G1 — planned

- createdAt: 2026-07-01T23:11:54.649Z
- updatedAt: 2026-07-08T21:20:05.256Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- title: "wanbond: 2-WAN bonding tunnel with adaptive FEC on amneziawg-go + custom conn.Bind — implementation and test harness"
- description: |
    Plan the implementation and test harness for `wanbond`, per the full project prompt in `fec-prompt.md` (repo root — authoritative source; summarized here).
    
    GOAL: Build `wanbond` — a single self-contained Go binary (runs on both edge and concentrator) that bonds two unreliable, heterogeneous WANs (Starlink: low-latency, jittery, intermittent obstruction loss; 4G/5G: metered, stable, variable latency) into one resilient, DPI-resistant tunnel for GENERAL IP traffic, with adaptive FEC.
    
    ARCHITECTURE (decided — do NOT re-evaluate): embed amneziawg-go as a library (unmodified WG engine: TUN, Noise, AEAD, rekey, roaming, keepalive); ALL bonding logic (multipath scheduling, RS-FEC, resequencing, per-path telemetry) in a custom `conn.Bind` under the engine, operating on opaque encrypted WG datagrams. Outer bonding frame (outer-seq, path-id, fec-group, flags) + parity/probe/control frame types. Inner layer fail-closed; outer control/probe frames PSK-authenticated; outer data headers unauthenticated (DoS-grade risk accepted). Outer wire format = unidentifiable high-entropy UDP (no magic bytes).
    
    PRIORITIZED REQUIREMENTS (earlier must not regress for later): 1. transparent failover (TCP flow survives WAN death, no reset); 2. data-thrift (metered 5G ~idle until needed); 3. bandwidth aggregation on demand; 4. RS-FEC masking loss without duplication; 5. adaptive FEC tracking measured per-path loss; 6. DPI resistance (no WG fingerprint; amnezia junk params as defense-in-depth; protocol mimicry out of scope).
    
    PHASES: P0 spike (embed amneziawg-go + trivial pass-through Bind, document conn.Bind contract pitfalls, baseline throughput) → P1 transparent failover MVP → P2 aggregation + data-thrift → P3 fixed-ratio FEC → P4 adaptive FEC → P5 DPI hardening (capture + entropy check; nDPI/Suricata must not classify as WireGuard). Each phase independently shippable + verifiable.
    
    TEST HARNESS is an explicit first-class deliverable: per-phase verification criteria are in the prompt (kill-active-WAN-mid-SSH survival; bonded throughput ≈ sum; ≥Y% recovery at X% injected loss with ≤Z% overhead; adaptive overhead ≤ fixed-FEC baseline; DPI classification checks). Expect network emulation (loss/jitter/reorder injection, e.g. netns + netem) to make these reproducible.
    
    KEY RISKS (investigate first, P0 targets most): conn.Bind API impedance (batched send/recv, GSO/GRO, Endpoint identity when hiding N paths behind one virtual endpoint); amneziawg-go fork lag vs upstream (keep Bind portable to wireguard-go); WG anti-replay window vs multipath reorder (own outer-seq, never reuse inner counter); reorder-buffer tuning vs Starlink jitter; adaptive control-loop STABILITY (the crux); FEC grouping latency vs recovery; bufferbloat/pacing; hotels blocking UDP wholesale (documented limitation, no TCP fallback).
    
    CONTEXT: edge = Linux box behind router that already pins source IPs to WANs (path selection external); concentrator = small public-IP VPS that NATs tunnel traffic; Starlink ~45ms jittery, 5G ~64ms stable; start with 2 paths, design for N.
    
    NON-GOALS: not a general SD-WAN product; no GUI; not >3 links initially; path pinning external; no TCP/TLS fallback transport; no protocol mimicry; base-library decision closed (kcp-go, quic-go, plain wireguard-go rejected in favor of amneziawg-go + custom Bind).
    
    LIBRARIES: amneziawg-go (base, decided); klauspost/reedsolomon (RS FEC).
    
    ## Follow-up (2026-07-06): Real cross-network two-host e2e tier + controlled-loss/FEC baseline
    
    ADDITIVE scope (do NOT invalidate or reorder existing P1-P5 tasks T11-T30). Grounded in P0 hardware validation this session.
    
    EVIDENCE / CONTEXT (established — planner need not re-ask): Two real remote hosts now exist in DIFFERENT networks, reachable over SSH with key /run/agenix/llm-ssh-key: (a) o3.7mind.io — aarch64, 1 vCPU, PUBLIC inbound-reachable UDP endpoint (89.168.124.91) → CONCENTRATOR; (b) llm-ubuntu-0.pgtr.7mind.io — amd64, 4 vCPU, behind a SYMMETRIC NAT (not inbound-reachable) → EDGE (initiates; concentrator learns the NAT'd endpoint — real CGNAT traversal). The P0 pass-through tunnel was validated between them over the real internet (WG handshake + NAT traversal + ping ~29ms + iperf3). MEASURED: tunnel carries ~150-170 Mbit/s (UDP 148, 8×TCP 169) ≈ raw path (171-313); NOT CPU-bound (o3 wanbond ~24% of one core); single-flow TCP collapses to ~18-48 Mbit/s from ~0.1-0.8% loss over the 29ms RTT (Mathis). Per-host provisioning: apt install iperf3 gcc + Go 1.26.4 tarball to /usr/local/go; concentrator MUST allow tunnel-interface traffic (OCI ships `-A INPUT -j REJECT --reject-with icmp-host-prohibited`).
    
    NEW SCOPE (design decisions pre-answered): 1. REAL cross-network two-host e2e tier behind a `realhosts` build tag, opt-in/manual (Justfile target), env-configured hosts, complementing netns — P0 smoke then multipath/failover/FEC as phases land; idempotent provisioning + concentrator firewall rule. 2. CONTROLLED-LOSS/FEC BASELINE: fixture gains a bandwidth cap + controlled-loss knob (unify with the A7/T10 checkpoint follow-up). 3. MULTIPATH over real hosts via virtual interfaces + policy routing (shared uplink, distinct source IPs) for functional bonding/failover; truly-independent links OUT OF SCOPE. 4. FOLD-IN notes: T12 large SO_RCVBUF + batched send/recv; T22 document the concentrator firewall requirement. [This follow-up is DONE: realhosts tier T33/T34, impairment fixture T35, and the fold-ins all landed.]
    
    ## Follow-up (2026-07-08): Deferred-defect hardening round
    
    ADDITIVE — do NOT invalidate the completed P1-P5 / T1-T34 work. SCOPE = resolve the 14 root-caused, file-and-defer defects accumulated across the P1-P5 build (each on the defects ledger with a confirmed rootCause + suggestedFix). Turn them into REVIEWED FIX TASKS (opus+fable adversarial review; hardware-validate any that touch the netns fixture or the real hosts). Each fix task MUST link its defect(s) via `defects:<D>` and drive them to `resolved` on merge. Authoritative fix detail lives on each defect item — read it; the summaries below are the grouping.
    
    GROUP A — bind/config/e2e-harness correctness (mostly test-only, low sev): D3 e2e iperf3 readiness fixed-sleeps → shared LISTEN-poll helper (poll ss/​/proc/net/tcp until LISTENING; do NOT probe-connect a `-s -1` server). D14 e2e Setup races prior teardown (RTNETLINK 'File exists' on fixed veths) → idempotent Setup / synchronous Teardown (generalize T26's SetupWithPaths pre-delete). D20 goroutine leak in TestMultipathEngineUpCanTransmit (engine_test.go ~L99 unbuffered send) → buffered chan / select+done. D10 config.validate accepts duplicate source_addr → EADDRINUSE → reject dup SourceAddr. D13 IPv6 global sources never device-bind (fe80::/10 counted in familyCount) → exclude link-local for a global-v6 source. D4 outer CONTROL/PROBE no codec-layer anti-replay → per-peer ProbeSeq high-water + stale-Timestamp rejection in the T13 layer.
    
    GROUP B — FEC hardening (M7/M8): D25 (MEDIUM) adaptive varying-M rests on an UNDOCUMENTED klauspost prefix-stability default + partial groups untested → partial-m×partial-k byte-exact property test + PIN the guarantee (build-time prefix assert and/or version-pin note). D24 (low) FEC unrecoverable counter under-reports at quiescence → account retained-past-deadline groups at Stats() or time-based eviction. D26 (low) adaptive DEFAULT tuning can't meet sub-1% residual → derive redundancy from a TARGET-RESIDUAL param, or a documented SafetyFactor/threshold-per-SLA table.
    
    GROUP C — pacing (M6): D22 (MEDIUM) pacer sheds WG control frames under overload → classify + exempt/priority WG control frames at the Bind (frame-type plumbing) + size per_path_capacity from measured BDP.
    
    GROUP D — real-host/deploy (M10): D7 (MEDIUM) concentrator ACCEPT rule not reboot-persistent → idempotent persistence provisioning + install-doc + TestRealProvision assertion. D8 (low) o3 INPUT chain has duplicate rules → one-time host dedup (folds with D7).
    
    GROUP E — docs/tooling (M6/M9): D23 (MEDIUM) fixture comments misattribute the real-internet 150-170 Mbit/s figure as the in-fixture ceiling (4 locations) → sweep to the measured per-host in-fixture ceilings (12-46 Mbit/s 1-vCPU; measure the 4-vCPU host) + the '2*cap below host ceiling' rule. D28 (low) `just lint` omits -tags e2e → add --build-tags e2e (+ go vet -tags e2e ./test/e2e) to the Justfile lint target.
    
    SEQUENCING/PRIORITY: the four MEDIUMs (D25, D22, D7, D23) first; rest are low-sev correctness/hygiene. Structure as a small number of coherent fix tasks (~one per group A-E) under a NEW hardening milestone OR folded onto M5-M10 by area — planner's call. D8 is a one-time host action (not a repo change); D23 is a doc/comment sweep. NON-GOAL: no new product capability — purely resolving recorded technical-debt/hardening defects.
- sessionLogs: [".cq/logs/20260701-231505-aacec84bd6a7748f4.md",".cq/logs/20260701-234215-a533f3a14c0afe112.md",".cq/logs/20260701-234215-a2ee01f9272ece9de.md",".cq/logs/20260706-214500-ae9330abac00e2f49.md",".cq/logs/20260706-214500-a325701e6205544bb.md"]
- rawLogs: [".cq/logs/raw/20260701-231505-aacec84bd6a7748f4.jsonl",".cq/logs/raw/20260701-234215-a533f3a14c0afe112.jsonl",".cq/logs/raw/20260701-234215-a2ee01f9272ece9de.jsonl"]
- milestones: ["M2","M3","M4","M5","M6","M7","M8","M9","M10","M11"]
- grounding: "Plan locked via decision K1 after a 3-round opus+fable review panel (R1/R2 revise → R3 go-ahead). 8 phase milestones M2-M9 (scaffolding S, harness H, P0 spike, P1 failover, P2 aggregation, P3 fixed FEC, P4 adaptive FEC, P5 DPI); 30 tasks T1-T30. All Q1-Q8 answers are binding constraints wired into the tasks. ## Hardening round (2026-07-08): the 14 root-caused file-and-defer defects (D3,D4,D7,D8,D10,D13,D14,D20,D22,D23,D24,D25,D26,D28) folded into 9 fix tasks under a NEW hardening milestone. Binding answers: Q14 -> o3 is a TEST host (live iptables edits/reboots OK; NEVER deprovision it; implement-workers cannot reach it from sandbox, so D7/D8 live-apply is a manual report-only ops step, not an automated gate). Q15 -> D23 measure-then-sweep (record real 4-vCPU in-fixture ceiling on llm-ubuntu-0 BEFORE the sweep). Q16 -> D26 adds a new target_residual config parameter (option i; new config surface explicitly approved despite the round's general no-new-capability non-goal). Fix detail is authoritative on each defect item; each fix task ledgerRefs its defects:<D> and drives them to resolved on merge."
