---
ledger: goals
counters:
  milestone: 0
  item: 13
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

## M12

### G2 — planned

- createdAt: 2026-07-13T12:27:36.017Z
- updatedAt: 2026-07-13T13:49:31.728Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- title: "Production-readiness: real-link validation, pacing sizing, and pilot hardening"
- description: |
    ADDITIVE goal following the completed P0-P5 build and the 2026-07-08 deferred-defect hardening round (G1). Turns the deliberate boundaries surfaced in the production-readiness assessment into a scoped PRE-PILOT hardening plan. Do NOT invalidate or reorder G1's completed work.
    
    CONTEXT (established this session; planner need not re-ask): the P0-P5 tunnel is functionally complete, opus+fable-reviewed, hardened; the ledger is drained (0 open defects/questions). Two standing REAL hosts exist in different networks, reachable via the llm SSH key (/run/agenix/llm-ssh-key): (a) llm-ubuntu-0.pgtr.7mind.io = amd64 4-vCPU behind SYMMETRIC NAT = EDGE; (b) o3.7mind.io = aarch64 1-vCPU PUBLIC 89.168.124.91 = CONCENTRATOR (o3 is a TEST host; live iptables/reboots OK; NEVER deprovision it; its firewall is already deduped + reboot-persistent per D7/D8). The netns e2e fixture is CPU/PPS-bound (~12-46 Mbit/s 1-vCPU, ~13 single-path / ~47-87 FEC 4-vCPU), so real-link THROUGHPUT AGGREGATION and BUFFERBLOAT are UNMEASURED. A bandwidth-capped impairment fixture exists (TestFixtureImpairment / T35). Pacing ships DISABLED and un-sized: SizePacingFromBDP (internal/config) is a helper, NOT auto-wired; default per-path capacity is synthetic (~115 Mbit/s).
    
    CORE SCOPE (the genuinely plannable 'open issues'):
    1. PACING empirical sizing (relates D22): use the bandwidth-capped fixture to create standing queues; measure per-path BDP; either wire SizePacingFromBDP into config load/auto-tuning OR ship a documented per-link tuning procedure; validate that ENABLED pacing eliminates bufferbloat under sustained load and does NOT starve WireGuard control frames (rekey survives overload).
    2. REAL-LINK VALIDATION (relates D23): extend the -tags realhosts tier with a throughput-aggregation + loaded-RTT (bufferbloat) + short soak test across the two standing hosts; record the bonded-vs-sum ratio and loaded RTT under load, and a deliberate mid-transfer WAN kill for failover under real conditions. These are the measurements the CPU-bound netns fixture cannot produce (report-only, per M10/Q12 discipline).
    3. PILOT RUNBOOK: automate/streamline the manual-checklist section-P0 real-link baseline into a repeatable pre-pilot procedure, plus a rollout runbook (config/key/PSK generation, the already-done concentrator firewall persistence, /metrics monitoring, health checks). Keep README/docs/design.md/install.md in sync (per AGENTS.md).
    4. STARTUP PATH-AVAILABILITY RESILIENCE (approach A; user-selected 2026-07-13 after a design-review of the startup bind path): TODAY the Bind's startup Open() (internal/bind/multipath.go:474-483) FAILS FATALLY if ANY configured path's source_addr cannot be bound at boot -- no interface holds that IP, so net.ListenUDP returns EADDRNOTAVAIL ('cannot assign requested address') and the whole tunnel bring-up is torn down (device.Up fails, the daemon crash-loops under systemd Restart=on-failure) EVEN WHEN other uplinks are healthy. A mobile edge rebooting while its 5G modem has no DHCP lease yet, or Starlink mid-obstruction, thus refuses to start ENTIRELY. This contradicts requirement 1 (transparent failover) at the BOOT boundary and is ASYMMETRIC with the runtime model, where a SIGHUP-added bad path (multipath.go AddPath) errors without affecting the tunnel and a live path whose interface disappears is handled by liveness/failover. FIX (approach A): make startup TOLERANT -- bring the tunnel up on whatever paths DO bind, mark the unbindable ones DOWN (reusing the runtime 'path down' model so the scheduler/liveness treat them uniformly), and RECONCILE/retry binding them in the background as their interfaces/addresses appear (event-driven via netlink route/addr updates, or a bounded poll -- planner's call). HARD GUARDS: (a) the ZERO-paths-bindable case stays FATAL (no tunnel is possible without any transport); (b) a MALFORMED source_addr remains a hard CONFIG-LOAD failure (config.validate already rejects unparseable addresses) -- approach A only tolerates a WELL-FORMED address that is merely NOT-YET-ASSIGNABLE, it does not paper over a typo; (c) do not regress the T16 device-bind / re-roam behaviour or the source_addr pin. VALIDATE with a netns e2e: start with a path whose source_addr's interface is ABSENT, assert the tunnel comes up on the survivor path, then ADD the missing address and assert the deferred path joins and carries traffic (and, separately, that zero-bindable still fails fast).
    
    SCOPE DECISIONS TO CLARIFY (these gate the plan; answer before /cq:plan:advance):
    - CONTROL protocol: wire a LIVE out-of-band CONTROL protocol (e.g. explicit rekey / tunnel-state signalling)? The CONTROL frame kind + MAC-covered Seq + telemetry.ControlGuard anti-replay already exist and are tested, but inbound CONTROL is currently DROPPED at the Bind (reserved chokepoint). Currently a non-goal - include, or keep dormant/reserved?
    - MULTI-CONCENTRATOR failover: bring tunnel-termination redundancy (>1 concentrator, failover at the hub) into scope, or keep the single-concentrator model as a standing non-goal?
    - PILOT GATING: must a REAL-LINK SOAK gate the pilot go/no-go, or is the bandwidth-capped-fixture aggregation measurement + a report-only real-link smoke sufficient to proceed?
    
    NON-GOALS (keep EXCLUDED unless the answers above explicitly override): TCP/TLS fallback for wholesale-UDP-block networks (standing G1 non-goal); protocol mimicry; >3 links; a general SD-WAN product; GUI.
    
    DEFERRED-WORK PROVENANCE: this goal collects the deliberate boundaries recorded in docs/design.md 'Not yet built' + the production-readiness assessment (pacing not empirically sized; throughput aggregation unmeasured in-fixture; CONTROL unwired; single concentrator), PLUS the startup all-or-nothing path-bind resilience gap surfaced by the 2026-07-13 design review (CORE SCOPE 4, approach A). It does not re-open any resolved G1 defect.
- grounding: "Grounded 2026-07-13 (opus-4.8[1m]). STARTUP FATAL-BIND (CORE SCOPE 4): internal/bind/multipath.go Open() loop L467-516 binds each path via listenPath L479; ANY error -> closeSocketsLocked+fatal return (EADDRNOTAVAIL included). The TOLERANT model to reuse is runtime AddPath L1336-1416 (binds, on failure rolls back path WITHOUT tearing down the tunnel). Path-down model = telemetry Liveness StateDown/StateUp (internal/telemetry/liveness.go, DownAfter~1200ms, UpAfterSuccesses=3); sched/weighted.go excludes Down paths from Pick. CONTROL dropped at bind default case (~L854) -> stays dormant (Q17). CONFIG: internal/config/config.go Path{SourceAddr netip.Addr, DestAddr netip.AddrPort} (per-path dest) + single defaultRemote (ParseEndpoint ~L1244); validate L579-644 rejects missing/duplicate/malformed source_addr at load (malformed stays fatal per guard b). PACING: SizePacingFromBDP(bandwidthBitsPerSec float64, rtt time.Duration, avgWireFrameBytes float64)->BDPSizing{CapacityFPS,BurstFrames} L182-196 is a HELPER, NOT wired into config load; synthetic default defaultPerPathCapacityFPS=10000 (~115Mbit/s); WeightedConfig.Pacing / config PacingEnabled default FALSE; token bucket in sched/weighted.go tryConsumeLocked. PROBE plane: telemetry Prober/Reflector/Estimator drives per-path RTT/loss/jitter + failover. TESTS: test/e2e netns fixture (netns.go SetupWithPaths, tc/netem) + capped fixture TestFixtureImpairment (test/e2e/fixture_impairment_test.go, rateMbit/lossPct, T35); test/realhosts (-tags realhosts) runner.go SSH + provision.go against llm-ubuntu-0 (amd64 symmetric-NAT EDGE) + o3.7mind.io (aarch64 PUBLIC concentrator 89.168.124.91, live iptables OK, never deprovision). GATE: go build/vet/gofmt/test; just e2e (sudo netns); just realhosts (report-only, M10/Q12). DOCS to keep in sync (AGENTS.md): README.md, docs/design.md ('Not yet built' L232-251), docs/install.md, docs/manual-checklist.md. DECISIONS: Q17 CONTROL dormant (no milestone). Q18 multi-concentrator IN: model = edge-side ORDERED-ENDPOINT ACTIVE-STANDBY (edge holds N concentrator endpoints, detects all-paths-to-hub DOWN via PROBE/liveness, switches remote + WG re-handshake to next; NO hub-to-hub state handoff; mesh/anycast ruled out by SD-WAN non-goal). Q19 exit criterion NON-BLOCKING on soak. Q20 pacing = BOTH (wire SizePacingFromBDP from operator-declared per-link bandwidth + document measurement)."
- milestones: ["M13","M14","M15","M16","M17"]

## M18

### G4 — planned

- createdAt: 2026-07-13T20:56:20.857Z
- updatedAt: 2026-07-13T22:40:39.203Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- title: Multi-peer (hub-and-spoke) concentrator support
- description: |
    GOAL: let one wanbond concentrator process terminate MANY edges concurrently — each edge bonded across its own uplinks, with per-peer isolation of resequencing/FEC/scheduling and correct per-peer return routing — while preserving all load-bearing invariants.
    
    MOTIVATION. Today a concentrator is single-peer-per-process. The config schema and the WireGuard engine already accept multiple [[wireguard.peers]] (config.validate only requires >=1; device.go uapiConfig ranges over all peers), but wanbond's multipath Bind is a single-tunnel bonding instance: internal/bind/multipath.go holds ONE virtual endpoint (m.virt, pinned to the first source it learns), ONE receive resequencer (m.resequencer, keyed purely by outer-seq with no peer separation), ONE send counter (m.outerSeq), ONE scheduler, ONE fecSend/fecRecv, ONE flat m.paths list, and ONE defaultRemote. So with two edges, their independent outer-seq spaces interleave into one resequencer release window (D32-class drops), the single virtual endpoint + per-path endpoint-learning clobber, and return traffic misroutes. Design rule A1 already SAYS 'one virtual endpoint per peer' — the code took the single-peer shortcut. The plural [[wireguard.peers]] schema is therefore misleading and should either work or be documented as unsupported.
    
    CHOSEN DIRECTION (to confirm/refine in planning): approach (2) — AUTHENTICATED path->peer binding, NOT a forgeable outer-frame tunnel id. The crux is that the Bind demuxes frames BELOW the crypto layer (it resequences/FEC-recovers before the engine decrypts), so it cannot use the engine's Noise peer identity, and DATA frames carry no peer id (frame.go: OuterSeq/PathID/FEC only) and are unauthenticated by design (invariant 4) — an outer tunnel id would be spoofable across edges (cross-peer resequencer-injection DoS). The recommended enabler is a PER-PEER PSK (move psk from top-level into [[wireguard.peers]]), so the PSK-HMAC PROBE/CONTROL plane authenticates the SPECIFIC peer and can establish a path->peer binding; DATA frames on that path then route to that peer's resequencer. Then de-singleton the Bind: make virt / resequencer / outerSeq / scheduler / fecSend / fecRecv / paths / defaultRemote / probers / reflector per-peer (a map[peer]peerState), with the engine-facing ReceiveFunc demuxing each frame to the right peerState and Send(bufs, endpoint) routing via an endpoint->peerState map.
    
    KNOWN REFACTOR SURFACE: internal/bind/multipath.go (the de-singleton + demux), internal/bind/bind.go (keep the conn-seam isolation), internal/reseq (per-peer instances), internal/sched + internal/fec + internal/telemetry (per-peer), internal/frame (only if a wire change proves necessary), internal/device/device.go (program the path->peer demux table from authenticated peer events; per-peer virtual endpoints to the engine), internal/config (per-peer psk + validation).
    
    INVARIANTS TO PRESERVE (do not break): A1 one-virtual-endpoint-per-peer (now literally per-peer); own outer-seq space per sender (never the inner WG counter); resequence before inner anti-replay; DATA/PARITY unauthenticated by design (do not add a forgeable demux that weakens this); PROBE/CONTROL PSK-HMAC authenticated with monotonic anti-replay; conn coupling stays isolated to internal/bind/bind.go; amnezia all-or-nothing + single-engine-per-process; reedsolomon prefix-stability (TestKlauspostParityPrefixStableInvariant) on any FEC touch.
    
    OPEN QUESTIONS FOR CLARIFICATION (planner should ask before designing):
    - Scope: concentrator-side multi-peer only, or also edge talking to multiple DISTINCT concentrators simultaneously (different from the existing Q18 single-active-hub failover)? Recommend concentrator-only for this goal; edge-multi-hub is a separate feature.
    - PSK model: per-peer PSK (recommended, enables authenticated demux) vs keep deployment-wide PSK + some other demux? What's the migration/back-compat story for existing single-peer configs (top-level psk)?
    - Demux bootstrapping: how is a path attributed to a peer for the very FIRST frames, before any authenticated PROBE / before the WG handshake completes? (provisional/quarantine resequencer? gate DATA until an authenticated PROBE binds the path?)
    - Roaming: when an edge's source rebinds (NAT), how does the path re-bind to the same peer without a window where frames misroute?
    - Resource limits: max peers per concentrator; per-peer memory (a ~2048-frame resequencer ring + FEC state each); backpressure / eviction of idle peers; is there a configured cap?
    - Security: can a malicious edge (that knows ITS psk) disrupt ANOTHER edge's tunnel? Threat-model the path->peer binding.
    - Metrics: per-peer label on the /metrics series (wanbond_path_* etc.).
    
    NON-GOALS: mesh/anycast; edge-side simultaneous multi-concentrator aggregation; changing the single-engine-per-process model; any DPI/obfuscation change.
    
    TESTING DIRECTION: netns e2e with 2+ edges to one concentrator proving per-peer isolation (each edge's traffic resequences independently; one edge's loss/reorder/restart does not corrupt another's stream; return traffic routes to the correct edge); a per-peer resequencer unit test (two interleaved outer-seq streams stay separated); realhosts extension if feasible. Report-only real-link where absolute numbers apply, per the existing M10/Q12 discipline.
- sessionLogs: [".cq/logs/20260713-210054-acde8de5f9cf22718.md",".cq/logs/20260713-223042-a2b09d2ae330ca1d8.md",".cq/logs/20260713-223042-a2c165322dcecdcd0.md",".cq/logs/20260713-223626-a79ddfeb2486cc79e.md",".cq/logs/20260713-224024-ac5e7e9c498fab1f8.md"]
- rawLogs: [".cq/logs/raw/20260713-210054-acde8de5f9cf22718.jsonl",".cq/logs/raw/20260713-223042-a2b09d2ae330ca1d8.jsonl",".cq/logs/raw/20260713-223042-a2c165322dcecdcd0.jsonl",".cq/logs/raw/20260713-223626-a79ddfeb2486cc79e.jsonl",".cq/logs/raw/20260713-224024-ac5e7e9c498fab1f8.jsonl"]
- milestones: ["M23","M24","M25","M26","M27"]
- grounding: "Synthesized from 2 configured candidate planners (opus, fable; generate-N-then-judge per Q100/Q101; base = opus's 5-milestone/20-task candidate for its finer per-task testability, D32-per-peer isolation task, and the FEC prefix-stability invariant re-check the goal mandates; folded in from fable: the explicit pathState split into SHARED socket state vs per-(peer,path) codec/remote/prober/tx/rx — concentrator sockets are shared across peers — plus edge-role-with->1-peer rejection (Q21), uapiConfig golden byte-identity acceptance, (peer,path)-keyed metrics rate maps, and lazy per-peer state instantiation). Repo facts shaping the plan: internal/bind/multipath.go holds the process singletons (virt, resequencer atomic.Pointer, outerSeq, scheduler, fecSend/fecRecv, reflector, sendCodec, paths, defaultRemote, probers); Send(bufs, ep) picks path/outerSeq/fecSend under m.mu; handleInbound learns a path's return remote ONLY from an authenticated frame.Probe (ps.setRemote — the D9/D11 discipline); ONE engine-facing ReceiveFunc drains the shared resequencer. device.go buildScheduler builds one Prober per path and one NewMultipath; uapiConfig already ranges over all cfg.WireGuard.Peers (the plural schema exists but the Bind is single-peer). config.Config.PSK is top-level-only; config.Peer has no psk/name. metricsSource has no peer dimension. frame Codec / telemetry Reflector / Prober are all PSK-derived, so per-peer PSK naturally yields per-peer codec/reflector/probers, enabling authenticated trial-decode demux (Q24) with no wire change (Q23). Design keeps a SINGLE ReceiveFunc that stamps each delivered inner datagram with its peer's virtual endpoint (A1 literally per-peer) and demuxes Send via an endpoint->peerState map — no per-peer receive goroutines. Sharpest design point: the shared-socket vs per-(peer,path) path/remote model, isolated in M24/M25 where it is unit-testable before the e2e locks it in."

## M19

### G5 — planned

- createdAt: 2026-07-13T21:17:03.895Z
- updatedAt: 2026-07-13T22:12:41.925Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- title: Optional DNS (hostname) concentrator endpoints
- description: |
    GOAL: optional DNS (hostname) resolution for the edge's concentrator endpoint / ordered endpoints list, so an edge can dial a concentrator BY NAME — principally to support a concentrator with NO static IP (DDNS / a cloud instance whose public IP changes).
    
    MOTIVATION. Today endpoint/endpoints are IP:port literals ONLY — enforced by netip.ParseAddrPort in config.Peer.resolveEndpoints (internal/config/config.go:495) and Multipath.ParseEndpoint (internal/bind/multipath.go:1327), both of which reject hostnames. This is a DELIBERATE boundary, not a capability gap: the codebase already resolves DNS elsewhere (internal/metrics/server.go:132,151 resolves a hostname listen address to enforce loopback). It is purely EDGE-side (the concentrator has no endpoint — it learns edges dynamically), and largely orthogonal to the multi-peer concentrator goal (G4).
    
    CHOSEN DIRECTION (to confirm/refine in planning): OPT-IN DNS endpoints, default IP-only. Three things must be solved to make it useful and safe:
    - (a) RESOLUTION TIMING: the datapath sends to a concrete netip.AddrPort (per-path remote), so a hostname must resolve to an IP before any packet egresses. A one-time resolve at config load handles a static IP behind a name — but that is no better than putting the IP in the config. So resolution should DEFER/RECONCILE (like the W1 tolerant-startup model) rather than hard-fail config.Load when the resolver/network is not ready at boot.
    - (b) RE-RESOLUTION (the actual value): WireGuard resolves an endpoint ONCE and never re-resolves (wg-quick re-resolves externally via a timer). To support a CHANGING concentrator IP, add a re-resolution loop that, on TTL / liveness-loss, re-resolves the hostname and — if the IP changed — repoints the bond via the EXISTING bind.Multipath.SetPeerRemote path (the same machinery T57 hub-failover uses). Without this loop, DNS is strictly worse than an IP literal (a startup dependency for no benefit).
    - (c) DPI METADATA LEAK: wanbond's thesis is a high-entropy, unfingerprintable wire (amnezia obfuscation, no fixed offsets). A plaintext DNS query for the concentrator hostname is a cleartext, on-path-observable, PRE-tunnel signal that reveals a nameable/blocklistable host + timing over an unprotected channel — a real DPI-resistance regression. So DNS must be opt-in (default off), and the plan should decide DoH/DoT support vs system resolver vs a documented DPI trade-off.
    
    KNOWN REFACTOR SURFACE: internal/config (resolveEndpoints: accept + resolve or defer hostnames; keep the resolved netip.AddrPort shape T57 consumes; a raw-hostname field alongside; validation; keep IP-literal endpoints byte-for-byte behavior-identical), internal/bind/multipath.go (ParseEndpoint; re-resolution repoints via SetPeerRemote), internal/device/device.go (a re-resolution loop wired like startHubFailover / the T55 reconcile loop; startup tolerance for a not-yet-resolvable name), possibly internal/telemetry (trigger re-resolve on liveness loss). Compose with T57 hub-failover (each ordered endpoint could itself be a hostname).
    
    INVARIANTS / CONSTRAINTS TO PRESERVE: A1 one-virtual-endpoint-per-peer; the tolerant-startup model (do not hard-fail boot on a transient resolver outage); the DPI-resistance thesis (default IP-only; DNS opt-in); IP-literal endpoints stay byte-for-byte behavior-identical (no regression for existing configs); resolution never blocks the send hot path (resolve off-path, cache the result).
    
    OPEN QUESTIONS FOR CLARIFICATION (planner should ask before designing):
    - Default posture: DNS opt-in with default IP-only (recommended), or on by default?
    - Load-time behavior: hard-resolve-at-config-load, or defer-and-reconcile (recommended, mirrors W1 tolerant startup) when a name is not yet resolvable?
    - Re-resolution trigger + cadence: honor DNS TTL, a fixed poll interval, on-liveness-loss, or a combination? What repoints the bond — reuse SetPeerRemote?
    - Multi-record: a name can resolve to several A/AAAA records — pick first, expand into extra ordered endpoints (feeding hub-failover), or happy-eyeballs? IPv4/IPv6 preference?
    - Resolver privacy: system resolver (leaks plaintext DNS), DoH/DoT (more machinery, still leaks SNI/timing), or document the DPI trade-off and leave it to the operator? What is the security acceptance target?
    - Hub-failover interaction: may the ordered endpoints list mix hostnames and IP literals? How does re-resolution compose with an in-progress hub-failover switch (which endpoint is re-resolved, and does a re-resolve override a failover or vice-versa)?
    - Config surface: new raw-hostname field vs overloading endpoint/endpoints to accept either form; how validation distinguishes and reports.
    
    NON-GOALS: SRV records / service discovery; DNS on the concentrator side (it has no endpoint); any change to the obfuscation wire format; the edge-side simultaneous multi-concentrator aggregation feature (separate).
    
    TESTING DIRECTION: unit — resolveEndpoints resolves a hostname via an injected resolver seam and DEFERS (not hard-fails) on lookup failure; a re-resolution unit test repoints via SetPeerRemote when the injected resolver returns a changed IP (injected resolver + fake clock). netns e2e — an edge dials the concentrator BY NAME (a hosts-file / local resolver entry), the concentrator's IP changes mid-session, and the edge re-resolves and reconnects with the tunnel surviving. Report-only realhosts extension if feasible, per the M10/Q12 discipline.
- sessionLogs: [".cq/logs/20260713-212207-a0e65d160c67b7983.md",".cq/logs/20260713-215726-ad0c63b3749a28ff8.md",".cq/logs/20260713-215726-a3a1678fe42741a52.md",".cq/logs/20260713-220647-a5fce4a23176a911e.md",".cq/logs/20260713-221230-a1d2bf0c3d369b9bf.md"]
- rawLogs: [".cq/logs/raw/20260713-212207-a0e65d160c67b7983.jsonl",".cq/logs/raw/20260713-215726-ad0c63b3749a28ff8.jsonl",".cq/logs/raw/20260713-215726-a3a1678fe42741a52.jsonl",".cq/logs/raw/20260713-220647-a5fce4a23176a911e.jsonl",".cq/logs/raw/20260713-221230-a1d2bf0c3d369b9bf.jsonl"]
- milestones: ["M20","M21","M22"]
- grounding: "Synthesized from 2 configured candidate planners (opus, fable; generate-N-then-judge per Q100/Q101; base = fable's candidate, with opus's bootstrap-IP fail-fast for DoH/DoT, a dedicated cross-controller -race interleave task, and active-endpoint-by-identity folded in). Repo facts shaping the plan: resolveEndpoints (internal/config/config.go:484) and Multipath.ParseEndpoint (internal/bind/multipath.go:1327) are IP-only today; the plan keeps ParseEndpoint IP-only — the device layer hands only resolved netip.AddrPort to the engine, so no hostname ever reaches the bind/datapath. hubFailover (internal/device/failover.go:72-97) holds an immutable []netip.AddrPort snapshot with an active idx; Q34's answer makes it a mutable spec-keyed set updated under its existing h.mu, with the active entry tracked by identity. SetPeerRemote (multipath.go:1371) unconditionally Rebaselines the resequencer (D32) — hence strict change-suppression on re-resolve. The T55 tolerant-startup template (internal/bind/multipath.go:531-537 deferred paths + internal/bind/reconcile.go:60 loop) is the defer-and-reconcile model for unresolvable-at-boot names; startHubFailover currently skips len(Endpoints)<2 peers, so wiring changes for single-hostname peers while single-IP-literal peers stay controller-less. Go's net.Resolver discards TTL — the seam exposes (minTTL, ttlOk) so DoH/DoT can honor TTL while system mode polls. Netns e2e caveat: /etc/hosts is not netns-scoped for the Go resolver, so the e2e runs an in-test UDP DNS responder inside the edge namespace. Q32 expands multi-record A/AAAA into the ordered failover list; Q33 puts DoH (RFC 8484) + DoT (RFC 7858) in scope as first-class private-resolver options with a bootstrap-IP fail-fast (no plaintext lookup of the resolver's own name); Q29/Q35 gate everything behind a per-peer opt-in with all-IP-literal configs byte-for-byte identical."

## M29

### G6 — planned

- createdAt: 2026-07-13T22:50:11.848Z
- updatedAt: 2026-07-13T23:40:28.704Z
- author: fable-5
- session: cac93b81-5292-42e3-b77e-962544c75e54
- title: "Production-edge operability & full-tunnel hardening: improvements + docs from the first real deploy (wanbond-fixes.md)"
- description: |
    GOAL: adopt the improvement and documentation lessons from the first REAL production-style deploy (wanbond-fixes.md, repo root — authoritative source): edge = Raspberry Pi 4 / Debian / NetworkManager, two WANs as VLAN sub-interfaces (eth0.231 Starlink / eth0.232 5G) pinned by `ip rule from <source_addr>`; concentrator = o3 (aarch64 OCI, public 89.168.124.91 NAT'd from private enp0s6); client LAN eth0.223 routed through the bonded tunnel (inner 10.77.0.0/24) and NAT'd out o3. The deploy REACHED the goal (0% loss, ~18-37 ms over the Starlink+5G bond, clients egressing via the concentrator's public IP) — every gap sits at the restart/re-handshake and operator/edge-plumbing boundary that the netns/realhosts testbeds do not exercise.
    
    COMPANION DEFECTS (filed separately under M28, investigate-flow owns root-causing; this goal must COMPOSE with their fixes, not duplicate them): D35 allowed_ips 0.0.0.0/0 wedges the handshake; D36 one-sided restart → multi-minute outage; D37 startup handshake not re-driven off path-up; D38 auto device-bind defeats source-policy routing; D39 wanbond0 left DOWN + NM address flush; D40 CAP_NET_RAW/CAP_NET_ADMIN mismatch.
    
    SCOPE — IMPROVEMENTS (fixes-doc I1-I8):
    - I1 Daemon brings the wanbond0 link UP after creating it (low-risk; addressing stays operator-owned; relates D39).
    - I2 Session/handshake state signal — the single biggest debugging obstacle: path_up=1 long before the WG session exists; add a `wanbond_session_established` (and/or last-handshake-age) metric + a 'session up' log line so 'still converging' is distinguishable from 'wedged' (D35/D36/D37 all presented identically).
    - I3 Actionable error instead of raw `TUN write: input/output error` — on EIO check link state/MTU and name the remedy (relates D39).
    - I4 Downgrade the startup `no healthy path` ERROR spam during the ~1 s liveness warmup (DEBUG/INFO or a single 'waiting for path liveness' line; relates D37).
    - I5 Config toggle to force source-IP binding (opt out of device-bind), per-path or global `bind = "source"` — avoids the oif-rule workaround entirely (relates D38).
    - I6 Native full-tunnel support — once D35 is fixed accept 0.0.0.0/0; better, apply the /1+/1 split internally or a `mode = "default-route"` that wires client routing (relates C3).
    - I7 Interface/route persistence across daemon restarts — recreating wanbond0 drops every address/route/rule referencing it, forcing an external oneshot to rebuild them; keep the tun persistent across restarts or ship the oneshot (relates C4).
    - I8 Verify standby-path liveness is BIDIRECTIONAL — observed path_up{5g}=1 with tx{5g}=0; confirm an 'up' standby can actually transmit before failover selects it (possible latent defect: probes/echoes may only prove one direction for an idle standby — if investigation confirms, refile as a defect).
    
    SCOPE — DOCUMENTATION (fixes-doc C1-C6; per AGENTS.md docs land with the code changes where coupled, standalone otherwise):
    - C1 NetworkManager section in install.md §4 (`unmanaged-devices=interface-name:wanbond0`), today networkd-only.
    - C2 source_addr + device-bind collision warning + the `ip rule add oif <dev> table N` recipe (until I5/D38 lands).
    - C3 Full-tunnel / route-a-client-LAN recipe — THE primary use case, entirely undocumented: split allowed_ips (not 0.0.0.0/0 until D35); edge policy-route + SNAT-to-tunnel-IP (or widen concentrator allowed_ips); concentrator ip_forward + MASQUERADE + FORWARD conntrack-ESTABLISHED accept (the shipped `-i wanbond0 ACCEPT` covers only the forward direction).
    - C4 Persistence recipe for non-networkd hosts — bless the `PartOf=` oneshot pattern (address + link-up + policy rules + per-table routes + nft SNAT after daemon start); warn that a plain ExecStartPost races tun creation (R27 already fixed one instance of that race in the networkd docs).
    - C5 Expected reconverge window + restart guidance until D36 is fixed (restart both ends ~together; use the I2 session metric as the 'is it up yet' check).
    - C6 Concentrator NAT/forwarding prerequisites checklist (ip_forward, MASQUERADE, FORWARD ESTABLISHED accept).
    
    WHAT WENT RIGHT (keep; do not regress): static single binary; one-binary-two-roles; 0600-config enforcement; per-path liveness/loss/RTT metrics; netns + realhosts test tiers; virtual-endpoint multipath design; the steady-state datapath.
    
    OPEN QUESTIONS FOR CLARIFICATION (planner should ask before designing): sequencing vs the D35-D40 investigations (which improvements are gated on a root cause — e.g. I6 on D35 — vs immediately plannable); whether I7 (tun persistence) is in scope for code or docs-only this round (C4); whether I8 belongs here or as a defect after a quick investigation; whether the NM/oneshot recipes should also ship as packaged unit files vs docs-only; how much of C3's client-LAN wiring should become the I6 `mode="default-route"` automation vs stay documented manual steps.
    
    NON-GOALS: TCP/TLS fallback; protocol mimicry; >3 links; general SD-WAN; GUI; re-opening resolved G1/G2 defects; duplicating the D35-D40 fixes themselves (compose with them).
    
    TESTING DIRECTION: unit tests for the bind-mode toggle (I5) and metrics (I2); netns e2e for link-up (I1), session-established signal timing, and full-tunnel mode (I6, once D35 unwedges); realhosts/report-only where absolute behavior needs real WANs, per the M10/Q12 discipline. Doc recipes validated against a NetworkManager host where practical (the production Pi validated the current workarounds).
- sourceRefs: ["wanbond-fixes.md","docs/install.md","docs/design.md"]
- tags: ["production-deploy","operability","docs"]
- sessionLogs: [".cq/logs/20260713-225618-a6491a1ae0266d482.md",".cq/logs/20260713-232548-a382332a889496d5d.md",".cq/logs/20260713-232548-a795489b23fb6f794.md",".cq/logs/20260713-233226-a31df891879aba85e.md",".cq/logs/20260713-233719-a1999a2a1c65132fa.md",".cq/logs/20260713-234017-aa1ce2b42795fdf8a.md"]
- rawLogs: [".cq/logs/raw/20260713-225618-a6491a1ae0266d482.jsonl",".cq/logs/raw/20260713-232548-a382332a889496d5d.jsonl",".cq/logs/raw/20260713-232548-a795489b23fb6f794.jsonl",".cq/logs/raw/20260713-233226-a31df891879aba85e.jsonl",".cq/logs/raw/20260713-233719-a1999a2a1c65132fa.jsonl",".cq/logs/raw/20260713-234017-aa1ce2b42795fdf8a.jsonl"]
- milestones: ["M30","M31","M32","M33"]
- grounding: "Synthesized from 2 configured candidate planners (opus, fable; generate-N-then-judge per Q100/Q101; base = fable's 4-milestone/16-task candidate for its surface-then-wiring splits of I5/I6, the opt-in tun_persist semantics, docs-coupled-to-packaging per AGENTS.md, the two-sided I8 test, and the closing reference-sync sweep; folded in from opus: I7 acceptance = stable ifindex across restart + persistent device stays NM-unmanaged, and the packaging presence-check assertion for the NM drop-in). Repo facts shaping the plan: wanbond0 is created via tun.CreateTUN in internal/device/device.go and never LinkSetUp'd nor TUNSETPERSIST'd (comes up DOWN, non-persistent); the no-healthy-path spam is errNoHealthyPath (internal/bind/multipath.go:64) surfacing at ERROR through the engineLogger adapter (device.go:687) — I4 needs a warmup seam there, not a blanket level change; no session/handshake metric exists (only probe-plane wanbond_path_up) — I2 sources handshake state from the engine (IpcGet last_handshake_time_sec / peer lookup as deviceRehandshake in internal/device/failover.go:181 does) through the metrics.Source seam so the bind stays WG-unaware; the bind decision is planPathBinds/selectDeviceBinds (internal/bind/pathsock.go) and I5's mode must apply to Open, AddPath, AND the deferred-path reconcile; config is plain TOML (no versioning cost); install.md §4 today states the daemon never assigns routes — T108 is the one deliberate, mode-gated exception and design.md must say so (T115). Binding answers: Q37 gate only I6's literal-/0 acceptance on D35 (all D35-D40 references are acceptance-text only, no dependsOn — those defects are investigate-flow-owned under M28 and deliberately unlinked); Q38 I7 = BOTH tun_persist code (T109) AND the C4 oneshot (T111); Q39 I8 = in-goal verification test (T104), refile-as-defect if it exposes a fault; Q40 packaged NM drop-in (T110) + templated oneshot (T111); Q41 thin I6 (internal /1+/1 split at UAPI render T107 + edge default-route wiring T108, NO SNAT/NAT programming); Q42 per-path bind=source|device|auto + global default (T105/T106); Q43 all C1-C6 + I1-I4 + thin I5/I6 in this goal."

## M34

### G7 — planned

- createdAt: 2026-07-14T09:02:04.443Z
- updatedAt: 2026-07-14T09:39:13.730Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- title: "Fix D36: re-anchor the resequencer on a detected peer restart (one-sided-restart stall)"
- description: "DEFECT-SEEDED (skip clarifying — root cause confirmed; refs defects:D36, defects:D32, defects:D37). Fix the multi-minute one-sided-restart outage (defects:D36). CONFIRMED ROOT CAUSE: the outer-plane per-peer resequencer drops a restarted peer's low-outer-seq frames (which wrap the inner WG handshake init/response) as SUSPECT — the same mechanism as defects:D32 — because the Rebaseline() trusted re-anchor is wired ONLY to hub-failover (SetPeerRemote), so a plain one-sided edge restart (live paths, no endpoint change) and any concentrator-role restart (no failover) never re-anchor `next`; the wrapped init is SUSPECT-dropped until unauthenticated tryResync eventually corroborates or a WG rekey timer fires. The WG engine itself supersedes a fresh init immediately (ruled out as the cause) — the init just never reaches it. SUGGESTED FIX: trigger Resequencer.Rebaseline() on a DETECTED PEER RESTART via the T38 responder-challenge session-epoch change (an authenticated trusted control event), wired into BOTH the edge single-concentrator path and the concentrator per-peer path (both currently lack any Rebaseline trigger). VALIDATE with a netns one-sided-restart e2e (deferred to the o3 + llm-ubuntu-0 hosts, G2 pattern): saturate, restart ONLY edge (run A) then ONLY concentrator (run B), assert reconvergence ~= the ~25s both-ends-fresh baseline, capture reseq dropSuspect/rebaseline counters + wanbond_session_established 0→1 per direction. Grounding: internal/reseq/reseq.go (admit SUSPECT branch :285-297, Rebaseline :529-546), internal/bind/multipath.go (rq.Observe :1619-1650, SetPeerRemote→Rebaseline :2167-2182), internal/device/failover.go (concentrator noop :496), the T38 session-epoch machinery (see defects:D12 fix). Compounding defects:D37 (pre-liveness first init, still open) is tracked separately and may be folded in if cheap. On merge, drive defects:D36 to resolved."
- sourceRefs: ["defects:D36","defects:D32","defects:D37","wanbond-fixes.md §A D2"]
- tags: ["production-deploy","restart","re-handshake","defect-seeded"]
- milestones: ["M34","M39","M40","M41"]
- sessionLogs: [".cq/logs/20260714-092040-a0ca3fd027bd1507c.md",".cq/logs/20260714-092040-a24993ebb08900c82.md"]
- rawLogs: [".cq/logs/raw/20260714-092040-a0ca3fd027bd1507c.jsonl",".cq/logs/raw/20260714-092040-a24993ebb08900c82.jsonl"]

## M35

### G8 — planned

- createdAt: 2026-07-14T09:07:10.028Z
- updatedAt: 2026-07-14T09:56:41.736Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- title: Multi-peer concentrator datapath hardening (G4 follow-up)
- description: "DEFECT-SEEDED (skip clarifying — each defect is reviewer-pinned with a code-grounded root cause + suggestedFix this run). Harden the multi-peer (hub-and-spoke) concentrator datapath that G4 landed. Fix, each per its ledger detail (read the defect item): defects:D42 — deferred AddPath desyncs per-peer probers from m.defs when >1 peer is bound (latent out-of-range/mis-attribution); defects:D44 — fecFlushDeadline drives only the primary peer's FEC group, so additional peers' partial FEC groups miss the deadline flush; defects:D47 — source→peer binding keyed by address only, so two peers behind ONE public IP (same NAT) can never both bind (a real CGNAT topology gap); defects:D49 — the global demux cap is monopolizable by one authenticated insider, starving other peers' bootstrap (per-peer fairness/quota); defects:D50 — device wiring of TearDownPeer (peer session/liveness loss) is untracked by any task (a peer that goes away is never torn down — leak/stale state); defects:D58 — the multi-peer concentrator drops the FIRST configured peer's required name from metrics labels (primary peer always peer=\"\" despite config requiring a unique name). PLANNER: group into coherent fix tasks by subsystem (bind/multipath demux+prober+FEC lifecycle, config/metrics label), each ledgerRef its defects:<D> and drive it resolved on merge; add per-peer unit tests + extend the multi-peer netns e2e (privileged run deferred to the o3 + llm-ubuntu-0 hosts, G2 pattern). Grounding lives on each defect item and in internal/bind/multipath.go, internal/reseq, internal/fec, internal/metrics."
- sourceRefs: ["defects:D42","defects:D44","defects:D47","defects:D49","defects:D50","defects:D58","goals:G4"]
- tags: ["multi-peer","concentrator","defect-seeded"]
- milestones: ["M35","M42","M43","M44"]
- sessionLogs: [".cq/logs/20260714-094557-a1b53c2cc830f3a68.md",".cq/logs/20260714-094557-a127c117697460cae.md"]
- rawLogs: [".cq/logs/raw/20260714-094557-a1b53c2cc830f3a68.jsonl",".cq/logs/raw/20260714-094557-a127c117697460cae.jsonl"]

## M36

### G9 — planned

- createdAt: 2026-07-14T09:07:19.087Z
- updatedAt: 2026-07-14T10:06:34.935Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- title: Config loader/validation hardening
- description: "DEFECT-SEEDED (skip clarifying — reviewer-pinned causes + suggestedFixes on each defect). Make the TOML config loader fail-fast and honest at load time. Fix per each defect item: defects:D41 — the loader SILENTLY IGNORES unknown/misspelled TOML keys (a typo'd key is dropped, not rejected — against the fail-fast posture; enable strict/DisallowUnknownFields decoding); defects:D43 — pre-existing docs advertise string-duration config forms ([scheduler]/[...]) that the loader REJECTS (doc-vs-loader mismatch — either accept the string-duration form or correct the docs, planner's call, prefer accepting to match documented UX); defects:D55 — allowed_ips CIDR syntax is NOT validated at config load, so a malformed CIDR fails LATE at daemon start instead of at Load (validate each allowed_ips entry with netip.ParsePrefix in config.validate); defects:D59 — config validation ACCEPTS multiple mode=default-route peers on the edge with overlapping 0.0.0.0/0 (last-writer-wins at the engine — silent misconfig; reject >1 default-route peer / overlapping /0 across peers). PLANNER: one or two coherent config-validation fix tasks; each ledgerRef its defects:<D>, add table-driven config.Load rejection tests, drive resolved on merge. Grounding: internal/config/config.go (Load/normalize/validate)."
- sourceRefs: ["defects:D41","defects:D43","defects:D55","defects:D59"]
- tags: ["config","validation","defect-seeded"]
- milestones: ["M36","M45"]
- sessionLogs: [".cq/logs/20260714-100007-a8d1d787e3f65897c.md",".cq/logs/20260714-100007-ad2d48fde7414a99f.md"]
- rawLogs: [".cq/logs/raw/20260714-100007-a8d1d787e3f65897c.jsonl",".cq/logs/raw/20260714-100007-ad2d48fde7414a99f.jsonl"]

## M37

### G10 — planned

- createdAt: 2026-07-14T09:07:27.096Z
- updatedAt: 2026-07-14T10:25:10.924Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- title: Observability & metrics accuracy
- description: "DEFECT-SEEDED (skip clarifying — reviewer-pinned). Fix per each defect item: defects:D48 — wanbond_path_tx_bytes_total omits probe and echo-reflection wire bytes, so per-path tx/rx byte accounting under-reports (count probe/echo frames in the wire-byte counters, or document the exclusion — prefer counting for accurate bufferbloat/BDP math); defects:D52 — reloadWarnings omits the scheduler/fec/dns/bind non-path config sections, so a SIGHUP that changes those sections is silently ignored with no warning (extend reloadWarnings to cover all reload-immutable sections); defects:D53 — the device-bind→source-IP-pinning fallback is SILENT (no WARN) in internal/bind, so an operator never learns the requested device bind was denied/unsupported and fell back (emit a WARN on the listenPath SO_BINDTODEVICE fallback). PLANNER: coherent fix tasks with metrics/log unit tests; each ledgerRef its defects:<D>, drive resolved on merge. Grounding: internal/metrics, internal/device/reload path, internal/bind/pathsock.go listenPath."
- sourceRefs: ["defects:D48","defects:D52","defects:D53"]
- tags: ["observability","metrics","defect-seeded"]
- milestones: ["M37","M46"]
- sessionLogs: [".cq/logs/20260714-100701-a4b1145f72fc338bd.md",".cq/logs/20260714-100701-a8f084815c088a9b9.md"]
- rawLogs: [".cq/logs/raw/20260714-100701-a4b1145f72fc338bd.jsonl",".cq/logs/raw/20260714-100701-a8f084815c088a9b9.jsonl"]

## M38

### G11 — planned

- createdAt: 2026-07-14T09:07:37.085Z
- updatedAt: 2026-07-14T10:21:26.815Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- title: Code, test, and doc-comment hygiene cleanup
- description: "DEFECT-SEEDED (skip clarifying — all reviewer-pinned, mostly one-line/low-risk). A single coherent hygiene sweep (can be one or a few tasks). Fix per each defect item: defects:D40 — CAP_NET_RAW (pathsock comment) vs CAP_NET_ADMIN (shipped systemd units) mismatch for SO_BINDTODEVICE — reconcile the comment to the actually-required capability; defects:D45 — `just lint` is RED at base (3 pre-existing golangci-lint findings in dnsresolve/doh.go, dot.go, bind/pathsock.go, device/metrics_test.go) — fix the findings so lint is green; defects:D51 — e2e /metrics port collision: pacing_test.go and p3_fec_test.go both bind 127.0.0.1:9096 — give each e2e test a unique port; defects:D54 — golangci-lint scans nested .claude/worktrees, leaking sibling agents' in-progress code into lint — exclude .claude/worktrees in .golangci config; defects:D56 — superseded primary-only bind read seams (PathSnapshots/FECSnapshot) retained with duplicated FEC-stat derivation — migrate bind tests to PeerSnapshots and delete them, or make them thin wrappers so the honest-delivered-count derivation lives once; defects:D57 — stale config.go doc-comments (Peer.PSK/Name 'not yet consumed by any datapath') now false since T93; defects:D60 — stale config.go BindMode + Path.Bind 'config surface only / not yet consumed' comments now false since T106. PLANNER: group into a hygiene task (or split lint/test-hygiene vs stale-comments); each ledgerRef its defects:<D>; keep the gate green; drive resolved on merge. Grounding on each defect item."
- sourceRefs: ["defects:D40","defects:D45","defects:D51","defects:D54","defects:D56","defects:D57","defects:D60"]
- tags: ["hygiene","lint","defect-seeded"]
- milestones: ["M38","M47"]
- sessionLogs: [".cq/logs/20260714-100847-a682af370a1cff1d9.md",".cq/logs/20260714-100847-aae9f3a68abcdc6ba.md"]
- rawLogs: [".cq/logs/raw/20260714-100847-a682af370a1cff1d9.jsonl",".cq/logs/raw/20260714-100847-aae9f3a68abcdc6ba.jsonl"]

## M48

### G12 — clarifying

- createdAt: 2026-07-14T11:41:52.076Z
- updatedAt: 2026-07-14T11:45:46.121Z
- author: "opus-4.8[1m]"
- session: be1a85fd-55c8-4654-ae42-672792fc0238
- title: Live monitoring web UI on edge and concentrator (WebSocket status, local-API auth)
- description: "Add live monitoring capabilities to both edge and concentrator instances. We already have some sort of a metrics API; embed a simple webpage which would receive dynamic status updates over WebSocket and display live link quality/peer/loss/RTT/FEC statistics. The /resilient-ws-ui skill (guidelines for reliable WebSocket connections to a backend with clear connection-health surfacing) could help with the frontend. Important open question: how to make such an API safe against unauthorized local calls."
- sessionLogs: [".cq/logs/20260714-114510-a2014552ac2ffb804.md"]
- rawLogs: [".cq/logs/raw/20260714-114510-a2014552ac2ffb804.jsonl"]

## M50

### G13 — planned

- createdAt: 2026-07-14T12:13:29.796Z
- updatedAt: 2026-07-14T12:53:50.586Z
- author: "opus-4.8[1m]"
- session: 915ea040-10d3-4f13-9cf2-ed8e5149babb
- title: Operability & safety for weighted aggregation + pacing
- description: |
    Plan (task DAG + acceptance) the operability gaps found while testing policy="weighted" with pacing on and off. Both functionally worked — the single flow striped across both paths — but they are hard to operate safely.
    
    1. **Aggregation-engagement observability.** Weighted mode sits silently in primary-only until offered load exceeds `engage_fraction × per_path_capacity_fps`. With the default `per_path_capacity_fps = 10000` (~100+ Mbps/path) aggregation NEVER engages on a low-throughput / CPU-bound edge — the operator sees `policy="weighted"` but single-path behavior and no signal why. Add a metric + log: `wanbond_aggregation_engaged` (bool) and the current EWMA offered-load vs the engage/disengage thresholds, so "configured but inert" is directly visible. (Empirically: had to hand-set `per_path_capacity_fps = 250` to trigger aggregation at all.)
    2. **Capacity-sizing safety.** Either auto-derive the default `per_path_capacity_fps` from measured/declared `link_bandwidth`, or warn/fail-loud when `weighted` is selected but capacity is left at a default the link cannot approach (so aggregation is silently dead). Tie the docs' BDP sizing (`install.md §3a`) to a runtime sanity check.
    3. **Pacing tradeoff — expose & document.** Measured on/off at the same 8 Mbps offered load:
       - pacing changed the path split — RTT-weighted 71/29 (off) → capacity-capped ~50/50 (on);
       - pacing bounded worst-case loaded RTT to 757 ms vs 1083 ms unpaced, but cut throughput to 4.98 vs 6.93 Mbps and dropped 33% excess;
       - under overload the pacer dropped frames indiscriminately — a concurrent ICMP flow saw ~38% loss.
       Plan: (a) document the on/off tradeoff and how to size `per_path_capacity_fps` / `pacing_burst_frames` from BDP; (b) consider a latency/priority class so small control frames (handshake, probes, ICMP) aren't starved when the pace saturates.
    
    Deliverable: acceptance criteria per task; each reproducible on a bandwidth-capped netns fixture (no metered WAN needed).
    
    RELATIONSHIP TO G2 (planner: dedup at task level, do NOT re-plan G2's locked scope): the already-`planned` goal G2 covers broad pacing empirical BDP sizing (Q20 = wire SizePacingFromBDP from declared per-link bandwidth + document) and validates that enabled pacing does NOT starve WireGuard control frames. This goal is OPERABILITY-specific and adds three things G2 does not: (i) the `wanbond_aggregation_engaged` metric + EWMA-offered-load-vs-threshold observability; (ii) a weighted-mode capacity-sanity WARN/FAIL-LOUD guard tied to install.md §3a BDP sizing; (iii) a latency/priority class mechanism so control/probe/ICMP frames aren't dropped indiscriminately under pacer overload. If item 2's auto-derive or item 3(a)'s sizing docs would duplicate a G2/M13-M17 task, reference the G2 task instead of restating it.
- sessionLogs: [".cq/logs/20260714-121742-a7376892a4d9f68f5.md",".cq/logs/20260714-123907-ac5d6e2c7eba4f406.md",".cq/logs/20260714-123907-a525c21bb0638ebf4.md"]
- rawLogs: [".cq/logs/raw/20260714-121742-a7376892a4d9f68f5.jsonl",".cq/logs/raw/20260714-123907-ac5d6e2c7eba4f406.jsonl",".cq/logs/raw/20260714-123907-a525c21bb0638ebf4.jsonl"]
- milestones: ["M51","M52","M53","M54","M55"]
- grounding: "Synthesized 2026-07-14 (opus-4.8[1m] orchestrator) from a 2-planner candidate panel (opus 5M/6T + fable 5M/8T; generate-N-then-JUDGE+SYNTHESIS). Both candidates converged on the same DAG shape; fable's 8-task decomposition taken as base, folding in opus's reproduce-first probe acceptance. DAG: M51 harness (root) -> {M53 observability, M54 probe} ; M52 guard (independent root) ; M55 docs (leaf, deps M52+M53+M54). 8 tasks T141-T148. LOAD-BEARING GROUNDING (both planners, independently confirmed): (1) PROBE frames (frame.KindProbe) do NOT traverse scheduler.Pick/token-bucket — emitProbes (internal/bind/probe.go) writes them directly to the path socket; so Q51 protection is NOT a ClassControl-style exemption but exempt-but-charged probe accounting that reserves link headroom against the pacer (T145, reproduce-first). (2) No scheduler->metrics seam exists today (metrics.Source = Paths/FEC/Reseq/Session/PeerNames); item 1 needs a new WeightedScheduler.AggregationSnapshot() accessor (T143) -> Bind PeerSnapshots -> device metricsSource -> collector four per-peer gauges (T146), honoring the T94 single-peer-omits-label back-compat. (3) deriveWeightedPacingFromBDP/SizePacingFromBDP already exist and are G2/Q20-owned — the Q52 capacity guard (T142 hard-fail / T144 WARN) reuses their avg-wire-frame math for consistency but does NOT re-own auto-derive or BDP docs (Q53 Option A). SCOPE per binding answers: item 1 observability (Q54 per-peer gauges + engage/disengage transition log + configured-but-inert e2e scenario), item 2 capacity-sanity guard (Q52 Option 3: hard-fail declared-contradiction / WARN+wanbond_weighted_capacity_sane gauge undeclared), item 3(ii) probe protection (Q51, inner-ICMP explicitly infeasible/out-of-scope), item 3(a) tradeoff+priority-model+infeasibility docs. All behavioral acceptance is -tags e2e via an up-front fixture-extension (Q55); the pure-docs task T148 is the one deliberate Q55 deviation (lint + reviewer prose-check, no runtime surface)."
