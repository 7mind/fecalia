---
ledger: defects
counters:
  milestone: 0
  item: 12
archives: []
---

# defects

## M4

### D1 — resolved

- createdAt: 2026-07-06T20:02:54.250Z
- updatedAt: 2026-07-06T23:45:16.073Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: Partial amnezia config emits zeroed h1..h4 and would silently misconfigure obfuscation
- description: "Filed by the T8 implement-review panel (opus), file-and-defer. internal/device/device.go writeAmnezia() emits ALL nine amnezia UAPI keys whenever ANY single field is non-zero. config.validate()/Amnezia.validate() enforce only the jmin<=jmax ordering, not an all-or-nothing / non-zero-magic-header invariant. So a partial amnezia block (e.g. jc/jmin/jmax set but s1/s2/h1..h4 left at 0) produces a configured-but-inconsistent obfuscation profile: the two ends can silently disagree on junk vs magic-header settings. P0 runs plain WireGuard (amnezia all-zero, unexercised), so this is latent. Belongs with the T19 amnezia end-to-end wiring. Severity low."
- severity: low
- suggestedFix: "In T19, add an Amnezia validation rule: when the block is configured, require the full obfuscation set to be internally consistent (and default magic headers to 1..4 when omitted rather than 0), so a partial config fails fast at load."
- ledgerRefs: ["tasks:T8","goals:G1","tasks:T19"]
- rootCause: "Confirmed against source (in-tree, no explorer needed). internal/device/device.go writeAmnezia() gates on `configured := any-field-non-zero` and then emits ALL nine keys (jc/jmin/jmax/s1/s2/h1..h4), so a partial block emits zeros for the unset fields. internal/config/config.go Amnezia.validate() checks ONLY `0 <= jmin <= jmax` — no all-or-nothing / non-zero-magic-header invariant. Net: a partial amnezia config is accepted and emitted inconsistently. Unexercised at P0 (amnezia all-zero → writeAmnezia early-returns → plain WireGuard). Fix folded into task T19 (amnezia end-to-end); D1 ledgerRefs tasks:T19 so it auto-resolves on T19 merge-back per implement §7.4."
- fix: "Resolved by T19 (merged ca5d638): Amnezia.validate now enforces all-or-nothing (jc/jmin/jmax/s1/s2 all >0 when the block is configured) + distinct magic headers, and applyDefaults (called from normalize) fills H1-H4 with the standard 1..4 message-type headers when omitted (amneziawg-go treats headers <=4 as 'use the standard type'). A partial/inconsistent amnezia profile now FAILS FAST at config load instead of the two ends silently deriving different profiles. Verified faithful to amneziawg-go v1.0.4 semantics by both T19 reviewers and by the matched/mismatched e2e on real hardware."

### D2 — resolved

- createdAt: 2026-07-06T20:03:00.651Z
- updatedAt: 2026-07-06T23:45:20.405Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: amneziawg-go stores amnezia message-type magic headers in package-level globals
- description: "Filed by the T8 implement-review panel (fable), file-and-defer. amneziawg-go v1.0.4 device.handlePostConfig assigns MessageInitiationType/MessageResponseType/MessageCookieReplyType/MessageTransportType — PACKAGE-LEVEL vars (noise-protocol.go:62-67) — on every configured IpcSet apply. Two Device instances in one process therefore cannot carry different obfuscation magic headers; the last apply wins process-wide. wanbond currently runs exactly one engine per process (one binary per role), so this is unexercised, but it constrains any future in-process multi-device usage (e.g. an in-process edge+concentrator test with distinct amnezia configs would silently share header types). Upstream dependency property, not fixable inside T8. Relevant to T19. Severity low."
- severity: low
- suggestedFix: "Before/at T19: document + assert the single-engine-per-process invariant in internal/device, or evaluate vendoring/patching the fork to move the message-type magic headers into per-Device state."
- ledgerRefs: ["tasks:T8","goals:G1","tasks:T19"]
- rootCause: "Confirmed against vendored engine source (I read amneziawg-go@v1.0.4 device/device.go handlePostConfig, lines 585-720). The message-type magic headers MessageInitiationType/MessageResponseType/MessageCookieReplyType/MessageTransportType are package-level vars (noise-protocol.go) reassigned by handlePostConfig on every configured IpcSet apply (device.go:685/688 etc.), and resetProtocol() (device.go:585) also mutates them process-wide. So obfuscation magic headers are process-global, not per-Device: two engines in one process share the last-applied values. Upstream dependency property; not fixable in T8. Mitigation (document/assert single-engine-per-process, or vendor-patch to per-Device state) folded into T19; D2 ledgerRefs tasks:T19 so it auto-resolves on T19 merge-back per implement §7.4."
- fix: "Resolved by T19 (merged ca5d638): documented the amneziawg-go single-engine-per-process constraint (message-type magic headers are package-global vars, reset unconditionally by (*Device).Close->resetProtocol) and added globalAmneziaGuard enforcing PROCESS-EXCLUSIVITY — a configured (amnezia) engine is admitted only when no other engine is live; no engine may start while a configured engine is live; plain engines coexist. Acquire-before-IpcSet, release exactly-once via sync.Once (deferred release only after acquire succeeds), leak-free. The initial T19 delivery had an unsound same-profile-refcount + plain-bypass gap (found by the fable review, verified against the fork's unconditional resetProtocol); the merged rework tightened it to sound exclusivity with both orderings test-pinned. Verified by both reviewers."

### D3 — root-caused

- createdAt: 2026-07-06T20:28:18.949Z
- updatedAt: 2026-07-06T20:33:26.289Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: e2e iperf3 server readiness uses fixed sleeps instead of polling for listen
- description: Filed by the T9 implement-review panel (fable), file-and-defer. test/e2e/p0_test.go iperf3Mbps sleeps 500ms and test/e2e/baseline_test.go rttUnderLoad sleeps 800ms after starting the one-shot iperf3 server before the client connects; on a loaded host a slow server bind can yield 'connection refused' and a spurious failure (this class already bit the T9 bufferbloat measurement once, fixed there by moving to a distinct port — but the fixed-sleep readiness gap remains suite-wide). Pre-existing convention shared with the existing helper, so fixing it suite-wide is out of scope for T9. Severity low.
- severity: low
- suggestedFix: Add a shared helper that polls a bounded TCP connect to the iperf3 server port until it accepts (with a deadline) and use it in both iperf3Mbps and rttUnderLoad instead of the fixed sleeps.
- ledgerRefs: ["tasks:T9","goals:G1"]
- rootCause: "Confirmed (in-tree). test/e2e/p0_test.go iperf3Mbps and test/e2e/baseline_test.go rttUnderLoad start a one-shot (`iperf3 -s -1`) server then sleep a FIXED interval (500ms/800ms) before the client connects; there is no readiness check, so a slow bind under load races the client into 'connection refused'. Note the suggestedFix's naive 'poll a TCP connect to the server port' is UNSAFE here: a probe connect would consume the `-1` server's single accept and make the real client fail. Correct fix: poll for the LISTEN socket without connecting — `nsenter -t <pid> -n ss -ltn 'sport = :<port>'` (or read /proc/net/tcp in the netns) until the port is LISTENING, in a shared helper used by both call sites. DEFERRED as out-of-scope test-hardening (does not affect the P0 acceptance, which passes; the T9 bufferbloat instance was already de-flaked via a distinct port). Standalone test-robustness item, not tied to a product task; to be picked up by a future test-hardening pass or a direct /cq:investigate follow-up."

## M5

### D4 — root-caused

- createdAt: 2026-07-06T21:10:23.780Z
- updatedAt: 2026-07-06T21:32:42.684Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: Outer CONTROL/PROBE frames have no anti-replay at the codec layer
- description: Filed by the T11 review panel (opus), file-and-defer. internal/frame.Decode verifies the HMAC of CONTROL/PROBE frames but is STATELESS, so a passively-captured valid authenticated frame replays with a passing MAC (e.g. a replayed CONTROL rekey or a PROBE). This is CORRECT for the T11 codec (which is deliberately stateless and already exposes the enabling fields Probe.ProbeSeq, Probe.TimestampNanos, Control.ControlType); replay defense belongs to the downstream CONTROL/PROBE handling state machine. Severity low.
- severity: low
- suggestedFix: In the probe/liveness + control handling layer (T13), track a per-peer ProbeSeq high-water mark and/or reject stale TimestampNanos, and apply replay rejection to security-relevant ControlType messages.
- ledgerRefs: ["tasks:T11","goals:G1","tasks:T13"]
- rootCause: "Confirmed by the T11 review (source-cited): internal/frame.Decode verifies the CONTROL/PROBE HMAC but keeps NO per-peer state, so a captured valid frame replays with a passing MAC. Correct by design for a stateless codec (T11 exposes Probe.ProbeSeq / Probe.TimestampNanos / Control.ControlType as the freshness material). Fix deferred to T13 (probe/liveness + control state machine): track a per-peer ProbeSeq high-water mark and/or reject stale TimestampNanos. D4 ledgerRefs tasks:T13 so it auto-resolves on T13 merge-back."

### D5 — resolved

- createdAt: 2026-07-06T21:10:30.046Z
- updatedAt: 2026-07-06T23:12:48.163Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: frame codec re-derives HKDF subkeys and double-inits ChaCha20 per call (per-datagram hot path)
- description: Filed by the T11 review panel (fable), file-and-defer. internal/frame Encode/Decode call subkeys(psk) (two HKDF-SHA256 derivations) on EVERY invocation, and Decode constructs two XChaCha20 cipher instances per frame (peek kind byte + full-body obfuscate), plus per-frame allocations. Correct but wasteful in the per-datagram path of a WAN bonder (~microsecond-scale key-derivation per packet per direction; double-digit % of a core at 100k pps). Out of scope for T11 (codec correctness); the internal API is free to change when T12 consumes it. Severity medium.
- severity: medium
- suggestedFix: "At T12 integration, introduce a codec state built once from the PSK (e.g. type Codec struct{obfKey, authKey []byte} with NewCodec(psk) + methods): derive subkeys once, reuse one cipher/keystream per Decode (de-obfuscate kind byte and body from a single keystream), and consider a dst-append buffer-reuse API once T12 defines the datapath throughput target."
- ledgerRefs: ["tasks:T11","goals:G1","tasks:T12"]
- rootCause: "Confirmed by the T11 review (source-cited): internal/frame Encode/Decode call subkeys(psk) (two HKDF-SHA256 derivations) per invocation and Decode double-inits XChaCha20 (peek + full-body) per frame + per-frame allocations — wasteful in the per-datagram hot path. Correct output, but not built for the datapath. Fix deferred to T12 (where the codec is first wired into the datapath): introduce a Codec state built once from the PSK (NewCodec(psk), derive subkeys once, single keystream per Decode, dst-append buffer reuse). D5 ledgerRefs tasks:T12 so it auto-resolves on T12 merge-back. Reinforced by this session's real-host finding that the pass-through path is efficiency-sensitive (though not the current bottleneck)."
- fix: "Resolved by T12 (merged 6675ead): internal/frame gained NewCodec(psk) building HKDF subkeys ONCE, with Codec.Encode/Decode using a single keystream per operation and a dst-append API; the multipath Bind constructs the Codec once and reuses it on the per-datagram hot path instead of re-deriving subkeys + double-initing ChaCha20 per frame. Verified intact (single-keystream) by the T12 r3 review panel."

### D6 — open

- createdAt: 2026-07-06T22:26:02.141Z
- updatedAt: 2026-07-06T22:26:02.141Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: Probe frame has no direction/role bit — a bounced outbound probe is a valid echo
- description: "Filed by the T13 review panel (fable), file-and-defer. frame.Probe (internal/frame/frame.go) has no echo/direction discriminant and Reflector.Reflect re-encodes the probe verbatim, so an echo is content-identical to the probe. An on-path adversary that BLACKHOLES a path but bounces the prober's own outbound probe bytes back (no PSK knowledge needed) produces an authenticated, replay-fresh 'echo'; liveness stays Up and RTT reads only the attacker hop while the remote endpoint is unreachable — defeating exactly the blackhole detection T13 delivers. OUT OF SCOPE for T13: the fix changes the outer frame format (owned by the frame/codec layer). Severity medium."
- severity: medium
- suggestedFix: Add a direction/role bit to frame.Probe (or a distinct KindProbeEcho) COVERED BY THE HMAC; the prober accepts only echo-role frames, the reflector only probe-role frames. Do this in the frame codec (adjacent to D5/T12) or a dedicated follow-up; then T13's liveness/anti-replay consumes the role.
- ledgerRefs: ["tasks:T13","goals:G1","tasks:T18"]

### D9 — root-caused

- createdAt: 2026-07-06T22:50:59.274Z
- updatedAt: 2026-07-06T23:36:53.291Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: Per-path remote learned from unauthenticated DATA frames enables blind traffic-redirect DoS
- description: "Filed by the T12 review panel (fable), file-and-defer. internal/bind/multipath.go receiver() calls ps.setRemote(srcAP) for every decoded DATA frame. DATA frames are unauthenticated by design (frame.go wire model); for a blind attacker spraying random datagrams at a path socket's public port, a random payload decodes to a valid KindData frame with probability ~1/256 (uniform kind byte after keystream XOR, header length permitting) — each success redirects that path's return traffic to the attacker's source address until the next legitimate packet re-learns it. Inner WireGuard keeps confidentiality/integrity, so impact is DoS-grade (per-path traffic blackholing on the concentrator). OUT OF SCOPE for T12: the accepted P1 threat model explicitly tolerates DoS-grade DATA forgery, and the authenticated path-probe machinery that can gate remote-learning arrives with T15. Severity medium."
- severity: medium
- rootCause: "Confirmed by the T12 review (source-cited): multipath.go receiver() unconditionally calls ps.setRemote(srcAP) on every decoded DATA frame, and DATA frames carry no authentication (T11 codec authenticates only CONTROL/PROBE). Correct for the T12 datapath (P1 threat model tolerates DoS-grade DATA forgery); the gating fix depends on T15's authenticated PROBE frames. Deferred to T15."
- suggestedFix: Gate per-path remote learning on AUTHENTICATED traffic when the probe transport lands (T37) — or at least remote CHANGES away from a configured/confirmed address — mirroring wireguard-go (updates a peer endpoint only from crypto-verified packets). T37 introduces authenticated inbound probe/echo frames and the per-path remote-learning from them (see D11), which is the correct gating point; the unauthenticated-DATA setRemote should then be removed or restricted. (Originally scoped to T15; the authenticated-probe transport is T37.)
- ledgerRefs: ["tasks:T12","goals:G1","tasks:T37"]

### D10 — root-caused

- createdAt: 2026-07-06T23:09:43.617Z
- updatedAt: 2026-07-06T23:09:43.617Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: Config validation accepts duplicate path source_addr, causing EADDRINUSE at bind Open
- description: Filed by the T12 review panel (opus+fable, both independently) — file-and-defer. internal/config/config.go validate() enforces unique path NAMES but not unique SourceAddr values. internal/bind/multipath.go Open binds each path to (SourceAddr, port); the concentrator's Open(listen_port) and every re-Open after Down/Up (the engine passes the previously-bound port back, so ALL paths rebind that fixed port) then fail with EADDRINUSE on the second ListenUDP for two paths sharing a source_addr. Fail-fast and diagnosable (clear bind error, no silent corruption), but a misconfiguration that should be rejected at config LOAD, not at bring-up. Pre-existing validation gap, NOT introduced by T12's diff. Severity low.
- severity: low
- rootCause: "Confirmed against source by both T12 reviewers: config.validate() tracks a seen-set for path names only (not SourceAddr); bind Open binds (SourceAddr, port) per path, so duplicate source_addr with a fixed listen port collides at the second ListenUDP (EADDRINUSE). Fails loudly at bring-up rather than at config load."
- suggestedFix: In config validate(), track seen SourceAddr values alongside names and reject duplicates with a per-path error naming both conflicting paths. Small, self-contained; can fold into T15 (scheduler, next to touch the path set) or a direct config-hardening follow-up.
- ledgerRefs: ["tasks:T12","goals:G1"]

### D11 — root-caused

- createdAt: 2026-07-06T23:36:49.481Z
- updatedAt: 2026-07-06T23:36:49.481Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: "Concentrator-side failover drops: PROBE frames do not populate per-path learned remotes"
- description: Filed by the T15 review panel (opus). internal/bind/multipath.go receiver() calls ps.setRemote(srcAP) ONLY for DATA frames (PROBE/CONTROL/PARITY are dropped before setRemote). A backup path on the concentrator therefore learns its return remote only from inbound DATA, not from probes. Once the probe transport (T37) lands, a backup path can be reported StateUp purely from probe echoes while getRemote() is still false; when the scheduler fails egress over to it, multipath Send returns errNoHealthyPath and return traffic drops until the peer happens to send DATA on that path. Client/edge side is unaffected (ParseEndpoint seeds every path's remote from the configured peer endpoint). Out of scope for T15 (unit tests configure remotes; AlwaysUp keeps egress on the primary), but it defeats concentrator-side transparent failover in T20. Severity medium.
- severity: medium
- rootCause: "Confirmed by the T15 review against source: multipath.go receiver() gates ps.setRemote(srcAP) on KindData only, so a concentrator backup path learns its remote solely from inbound DATA. A probe-only StateUp path has getRemote()==false, so scheduler failover to it yields errNoHealthyPath. Fix is co-located with the probe-transport wiring (T37), which introduces authenticated inbound probe/echo frames that CAN safely populate the remote."
- suggestedFix: In T37, learn ps.setRemote(srcAP) from AUTHENTICATED probe/echo frames too (or seed backup remotes from config), so a StateUp-via-probe path always has a usable remote before it becomes active. This authenticated remote-learning is the same mechanism that gates D9's unauthenticated-DATA remote-learn DoS.
- ledgerRefs: ["tasks:T15","tasks:T37","goals:G1"]

## M10

### D7 — open

- createdAt: 2026-07-06T22:27:16.368Z
- updatedAt: 2026-07-06T22:27:16.368Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: Concentrator tunnel-interface ACCEPT rule is not reboot-persistent
- description: Filed by the T32 review panel (opus+fable), file-and-defer. T32's provision inserts `iptables -I INPUT -i wanbond0 -j ACCEPT` into the RUNTIME chain only. The concentrator (o3, OCI Ubuntu) restores its INPUT chain from /etc/iptables/rules.v4 at boot, so a reboot silently drops the rule and inbound tunnel TCP hits OCI's default REJECT again — the exact fault T32 fixes reappears with no signal until re-provisioned. Out of scope for T32 (its acceptance asserts only the runtime chain state, report-only per Q12), but a standing testbed / real deployment needs the rule to survive reboots. Severity medium.
- severity: medium
- suggestedFix: Add a provisioning step (and document in T22's install doc) that persists the concentrator INPUT rule across reboots — `netfilter-persistent save` after insertion, or an idempotent edit of /etc/iptables/rules.v4, or a small systemd unit that re-applies on boot — guarded by a state check so re-runs stay no-ops; extend TestRealProvision to assert the persisted set.
- ledgerRefs: ["tasks:T32","goals:G1","tasks:T22"]

### D8 — open

- createdAt: 2026-07-06T22:27:25.373Z
- updatedAt: 2026-07-06T22:27:25.373Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: Pre-existing duplicate rules in the o3 concentrator INPUT chain
- description: Filed by the T32 review panel (fable), file-and-defer. Live `iptables -S INPUT` on o3 shows the OCI default rule block DUPLICATED (two `-j REJECT --reject-with icmp-host-prohibited` with a full unreachable copy of ESTABLISHED/icmp/lo/ntp/ssh after the first REJECT) and three identical `-p udp --dport 51820 -j ACCEPT` rules. This PREDATES T32 (its -C-guarded insert cannot duplicate) — it is residue of earlier NON-idempotent rule insertion during this session's manual P0 real-host bring-up (the repeated `iptables -I INPUT ... 51820` probes). Dead/duplicate rules add audit noise and can mask future misconfiguration. Severity low; o3 host state only (not a code defect).
- severity: low
- suggestedFix: In the reboot-persistence follow-up, deduplicate the o3 INPUT chain to one canonical rule set (single 51820 ACCEPT, single OCI default block) before persisting, with a before/after `iptables -S INPUT` capture. This is a one-time host cleanup on o3, not a repo change.
- ledgerRefs: ["tasks:T32","goals:G1"]
