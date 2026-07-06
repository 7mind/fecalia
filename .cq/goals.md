---
ledger: goals
counters:
  milestone: 0
  item: 1
archives: []
---

# goals

## M1

### G1 — clarifying

- createdAt: 2026-07-01T23:11:54.649Z
- updatedAt: 2026-07-06T21:28:03.596Z
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
    
    EVIDENCE / CONTEXT (established — planner need not re-ask): Two real remote hosts now exist in DIFFERENT networks, reachable over SSH with key /run/agenix/llm-ssh-key: (a) o3.7mind.io — aarch64, 1 vCPU, PUBLIC inbound-reachable UDP endpoint (89.168.124.91) → CONCENTRATOR; (b) llm-ubuntu-0.pgtr.7mind.io — amd64, 4 vCPU, behind a SYMMETRIC NAT (not inbound-reachable) → EDGE (initiates; concentrator learns the NAT'd endpoint — real CGNAT traversal). The P0 pass-through tunnel was validated between them over the real internet (WG handshake + NAT traversal + ping ~29ms + iperf3). MEASURED: tunnel carries ~150-170 Mbit/s (UDP 148, 8×TCP 169) ≈ raw path (171-313); NOT CPU-bound (o3 wanbond ~24% of one core); single-flow TCP collapses to ~18-48 Mbit/s from ~0.1-0.8% loss over the 29ms RTT (Mathis) — the long-fat-lossy-network problem FEC (P3/P4) fixes. Per-host provisioning: apt install iperf3 gcc + Go 1.26.4 tarball to /usr/local/go; concentrator MUST allow tunnel-interface traffic (OCI ships `-A INPUT -j REJECT --reject-with icmp-host-prohibited` that blocks TCP on the tunnel; ICMP slips through).
    
    NEW SCOPE (design decisions pre-answered, so planning need not clarify these):
    1. REAL cross-network two-host e2e tier: SSH-orchestrated from the repo, behind a dedicated `realhosts` build tag (separate from the netns `e2e` tag), OPT-IN / manual-run only (NOT default CI; run via a Justfile target). Host addresses/roles/SSH-key from env (WANBOND_EDGE_HOST / WANBOND_CONC_HOST / WANBOND_CONC_PUBLIP / WANBOND_SSH_KEY), defaulting to the two hosts above. COMPLEMENTS (not replaces) the netns fixture. Start with the P0 single-uplink smoke test (handshake + ping + iperf3: single-flow, 8×parallel, UDP goodput/loss); extend to multipath/failover/FEC as those phases land; provision hosts idempotently + set the concentrator firewall rule.
    2. CONTROLLED-LOSS / FEC BASELINE: induce known loss (netem) and record single-flow-TCP collapse as the quantitative baseline P3/P4 FEC recovery is measured against. UNIFY with the A7 checkpoint follow-up drafted in docs/p0-checkpoint.md (T10): the fixture (netns and/or a virtual-interface topology on the real edge host) must gain BOTH a bandwidth cap (netem rate / tbf/htb) AND a controlled-loss knob, so bufferbloat/pacing (T21/T23) AND FEC recovery (T25/T29) are measurable. Supersede/merge the A7 follow-up; do not duplicate it.
    3. MULTIPATH over the real hosts (the full picture): give the edge two paths to the one concentrator via virtual interfaces + policy routing (shared physical uplink, distinct source IPs/4-tuples) for FUNCTIONAL bonding/failover validation once T12+ land. Truly-independent asymmetric/intermittent links are the FINAL real-hardware step — explicitly OUT OF SCOPE for now.
    4. FOLD-IN scope notes on existing tasks (no new task): T12 (multipath Bind) should set a large SO_RCVBUF and adopt batched send/recv (GSO/GRO best-effort per-path) to match pure-WireGuard StdNetBind (confirmed P0 §2 efficiency gap — pass-through Bind uses default socket buffers); T22 (systemd/install doc) must document the concentrator tunnel-interface firewall requirement.
- sessionLogs: [".cq/logs/20260701-231505-aacec84bd6a7748f4.md",".cq/logs/20260701-234215-a533f3a14c0afe112.md",".cq/logs/20260701-234215-a2ee01f9272ece9de.md"]
- rawLogs: [".cq/logs/raw/20260701-231505-aacec84bd6a7748f4.jsonl",".cq/logs/raw/20260701-234215-a533f3a14c0afe112.jsonl",".cq/logs/raw/20260701-234215-a2ee01f9272ece9de.jsonl"]
- milestones: ["M2","M3","M4","M5","M6","M7","M8","M9"]
- grounding: Plan locked via decision K1 after a 3-round opus+fable review panel (R1/R2 revise → R3 go-ahead). 8 phase milestones M2-M9 (scaffolding S, harness H, P0 spike, P1 failover, P2 aggregation, P3 fixed FEC, P4 adaptive FEC, P5 DPI); 30 tasks T1-T30. All Q1-Q8 answers are binding constraints wired into the tasks.
