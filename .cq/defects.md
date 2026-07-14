---
ledger: defects
counters:
  milestone: 0
  item: 76
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

### D3 — resolved

- createdAt: 2026-07-06T20:28:18.949Z
- updatedAt: 2026-07-08T21:39:15.464Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: e2e iperf3 server readiness uses fixed sleeps instead of polling for listen
- description: Filed by the T9 implement-review panel (fable), file-and-defer. test/e2e/p0_test.go iperf3Mbps sleeps 500ms and test/e2e/baseline_test.go rttUnderLoad sleeps 800ms after starting the one-shot iperf3 server before the client connects; on a loaded host a slow server bind can yield 'connection refused' and a spurious failure (this class already bit the T9 bufferbloat measurement once, fixed there by moving to a distinct port — but the fixed-sleep readiness gap remains suite-wide). Pre-existing convention shared with the existing helper, so fixing it suite-wide is out of scope for T9. Severity low.
- severity: low
- suggestedFix: Add a shared helper that polls a bounded TCP connect to the iperf3 server port until it accepts (with a deadline) and use it in both iperf3Mbps and rttUnderLoad instead of the fixed sleeps.
- ledgerRefs: ["tasks:T9","goals:G1"]
- rootCause: "Confirmed (in-tree). test/e2e/p0_test.go iperf3Mbps and test/e2e/baseline_test.go rttUnderLoad start a one-shot (`iperf3 -s -1`) server then sleep a FIXED interval (500ms/800ms) before the client connects; there is no readiness check, so a slow bind under load races the client into 'connection refused'. Note the suggestedFix's naive 'poll a TCP connect to the server port' is UNSAFE here: a probe connect would consume the `-1` server's single accept and make the real client fail. Correct fix: poll for the LISTEN socket without connecting — `nsenter -t <pid> -n ss -ltn 'sport = :<port>'` (or read /proc/net/tcp in the netns) until the port is LISTENING, in a shared helper used by both call sites. DEFERRED as out-of-scope test-hardening (does not affect the P0 acceptance, which passes; the T9 bufferbloat instance was already de-flaked via a distinct port). Standalone test-robustness item, not tied to a product task; to be picked up by a future test-hardening pass or a direct /cq:investigate follow-up."
- dependsOn: ["T42"]

## M5

### D4 — resolved

- createdAt: 2026-07-06T21:10:23.780Z
- updatedAt: 2026-07-08T21:39:19.680Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: Outer CONTROL/PROBE frames have no anti-replay at the codec layer
- description: Filed by the T11 review panel (opus), file-and-defer. internal/frame.Decode verifies the HMAC of CONTROL/PROBE frames but is STATELESS, so a passively-captured valid authenticated frame replays with a passing MAC (e.g. a replayed CONTROL rekey or a PROBE). This is CORRECT for the T11 codec (which is deliberately stateless and already exposes the enabling fields Probe.ProbeSeq, Probe.TimestampNanos, Control.ControlType); replay defense belongs to the downstream CONTROL/PROBE handling state machine. Severity low.
- severity: low
- suggestedFix: In the probe/liveness + control handling layer (T13), track a per-peer ProbeSeq high-water mark and/or reject stale TimestampNanos, and apply replay rejection to security-relevant ControlType messages.
- ledgerRefs: ["tasks:T11","goals:G1","tasks:T13"]
- rootCause: "Confirmed by the T11 review (source-cited): internal/frame.Decode verifies the CONTROL/PROBE HMAC but keeps NO per-peer state, so a captured valid frame replays with a passing MAC. Correct by design for a stateless codec (T11 exposes Probe.ProbeSeq / Probe.TimestampNanos / Control.ControlType as the freshness material). Fix deferred to T13 (probe/liveness + control state machine): track a per-peer ProbeSeq high-water mark and/or reject stale TimestampNanos. D4 ledgerRefs tasks:T13 so it auto-resolves on T13 merge-back."
- dependsOn: ["T44"]

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

### D6 — resolved

- createdAt: 2026-07-06T22:26:02.141Z
- updatedAt: 2026-07-07T11:56:52.680Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: Probe frame has no direction/role bit — a bounced outbound probe is a valid echo
- description: "Filed by the T13 review panel (fable), file-and-defer. frame.Probe (internal/frame/frame.go) has no echo/direction discriminant and Reflector.Reflect re-encodes the probe verbatim, so an echo is content-identical to the probe. An on-path adversary that BLACKHOLES a path but bounces the prober's own outbound probe bytes back (no PSK knowledge needed) produces an authenticated, replay-fresh 'echo'; liveness stays Up and RTT reads only the attacker hop while the remote endpoint is unreachable — defeating exactly the blackhole detection T13 delivers. OUT OF SCOPE for T13: the fix changes the outer frame format (owned by the frame/codec layer). Severity medium."
- severity: medium
- suggestedFix: Add a direction/role bit to frame.Probe (or a distinct KindProbeEcho) COVERED BY THE HMAC; the prober accepts only echo-role frames, the reflector only probe-role frames. Do this in the frame codec (adjacent to D5/T12) or a dedicated follow-up; then T13's liveness/anti-replay consumes the role.
- ledgerRefs: ["tasks:T13","goals:G1","tasks:T18"]
- fix: "Resolved by T37 (merged 03c8651): frame.Probe gained an IsEcho direction/role bit INSIDE the MAC-covered body. A prober emits PROBE (IsEcho=false); the reflector reflects it as ECHO (IsEcho=true); the originator's HandleEcho accepts ONLY IsEcho=true frames as echoes. An on-path adversary that bounces the prober's own outbound probe bytes back verbatim leaves IsEcho=false, so it is NOT accepted as an echo and liveness does not falsely stay Up; flipping IsEcho requires the PSK (MAC-covered) so it is unforgeable. The T37 review panel confirmed the direction discriminant is unspoofable and reflect-of-reflect is broken by the IsEcho guard — exactly D6's proposed fix (a HMAC-covered direction/role bit). Anti-replay freshness of echoes is further hardened by D4 (per-path ProbeSeq) and T38 (session epoch)."

### D9 — resolved

- createdAt: 2026-07-06T22:50:59.274Z
- updatedAt: 2026-07-07T00:14:42.208Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: Per-path remote learned from unauthenticated DATA frames enables blind traffic-redirect DoS
- description: "Filed by the T12 review panel (fable), file-and-defer. internal/bind/multipath.go receiver() calls ps.setRemote(srcAP) for every decoded DATA frame. DATA frames are unauthenticated by design (frame.go wire model); for a blind attacker spraying random datagrams at a path socket's public port, a random payload decodes to a valid KindData frame with probability ~1/256 (uniform kind byte after keystream XOR, header length permitting) — each success redirects that path's return traffic to the attacker's source address until the next legitimate packet re-learns it. Inner WireGuard keeps confidentiality/integrity, so impact is DoS-grade (per-path traffic blackholing on the concentrator). OUT OF SCOPE for T12: the accepted P1 threat model explicitly tolerates DoS-grade DATA forgery, and the authenticated path-probe machinery that can gate remote-learning arrives with T15. Severity medium."
- severity: medium
- rootCause: "Confirmed by the T12 review (source-cited): multipath.go receiver() unconditionally calls ps.setRemote(srcAP) on every decoded DATA frame, and DATA frames carry no authentication (T11 codec authenticates only CONTROL/PROBE). Correct for the T12 datapath (P1 threat model tolerates DoS-grade DATA forgery); the gating fix depends on T15's authenticated PROBE frames. Deferred to T15."
- suggestedFix: Gate per-path remote learning on AUTHENTICATED traffic when the probe transport lands (T37) — or at least remote CHANGES away from a configured/confirmed address — mirroring wireguard-go (updates a peer endpoint only from crypto-verified packets). T37 introduces authenticated inbound probe/echo frames and the per-path remote-learning from them (see D11), which is the correct gating point; the unauthenticated-DATA setRemote should then be removed or restricted. (Originally scoped to T15; the authenticated-probe transport is T37.)
- ledgerRefs: ["tasks:T12","goals:G1","tasks:T37"]
- fix: "Resolved by T37 (merged 03c8651): the unauthenticated-DATA ps.setRemote was REMOVED. Per-path return-remote learning now happens ONLY from MAC-verified probe/echo frames (frame.Decode authenticates PROBE). A blind attacker spraying forged DATA can no longer repoint a path's return traffic. DATA->virtual-endpoint pinning is retained (that is roaming identity, T16) but Send routes return traffic by the per-path authenticated getRemote, never the virt address, so DATA forgery cannot redirect traffic. Verified by both T37 reviewers + TestMultipathRemoteLearnedFromProbeNotData."

### D10 — resolved

- createdAt: 2026-07-06T23:09:43.617Z
- updatedAt: 2026-07-08T21:36:58.422Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: Config validation accepts duplicate path source_addr, causing EADDRINUSE at bind Open
- description: Filed by the T12 review panel (opus+fable, both independently) — file-and-defer. internal/config/config.go validate() enforces unique path NAMES but not unique SourceAddr values. internal/bind/multipath.go Open binds each path to (SourceAddr, port); the concentrator's Open(listen_port) and every re-Open after Down/Up (the engine passes the previously-bound port back, so ALL paths rebind that fixed port) then fail with EADDRINUSE on the second ListenUDP for two paths sharing a source_addr. Fail-fast and diagnosable (clear bind error, no silent corruption), but a misconfiguration that should be rejected at config LOAD, not at bring-up. Pre-existing validation gap, NOT introduced by T12's diff. Severity low.
- severity: low
- rootCause: "Confirmed against source by both T12 reviewers: config.validate() tracks a seen-set for path names only (not SourceAddr); bind Open binds (SourceAddr, port) per path, so duplicate source_addr with a fixed listen port collides at the second ListenUDP (EADDRINUSE). Fails loudly at bring-up rather than at config load."
- suggestedFix: In config validate(), track seen SourceAddr values alongside names and reject duplicates with a per-path error naming both conflicting paths. Small, self-contained; can fold into T15 (scheduler, next to touch the path set) or a direct config-hardening follow-up.
- ledgerRefs: ["tasks:T12","goals:G1"]
- dependsOn: ["T43"]

### D11 — resolved

- createdAt: 2026-07-06T23:36:49.481Z
- updatedAt: 2026-07-07T00:14:45.660Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: "Concentrator-side failover drops: PROBE frames do not populate per-path learned remotes"
- description: Filed by the T15 review panel (opus). internal/bind/multipath.go receiver() calls ps.setRemote(srcAP) ONLY for DATA frames (PROBE/CONTROL/PARITY are dropped before setRemote). A backup path on the concentrator therefore learns its return remote only from inbound DATA, not from probes. Once the probe transport (T37) lands, a backup path can be reported StateUp purely from probe echoes while getRemote() is still false; when the scheduler fails egress over to it, multipath Send returns errNoHealthyPath and return traffic drops until the peer happens to send DATA on that path. Client/edge side is unaffected (ParseEndpoint seeds every path's remote from the configured peer endpoint). Out of scope for T15 (unit tests configure remotes; AlwaysUp keeps egress on the primary), but it defeats concentrator-side transparent failover in T20. Severity medium.
- severity: medium
- rootCause: "Confirmed by the T15 review against source: multipath.go receiver() gates ps.setRemote(srcAP) on KindData only, so a concentrator backup path learns its remote solely from inbound DATA. A probe-only StateUp path has getRemote()==false, so scheduler failover to it yields errNoHealthyPath. Fix is co-located with the probe-transport wiring (T37), which introduces authenticated inbound probe/echo frames that CAN safely populate the remote."
- suggestedFix: In T37, learn ps.setRemote(srcAP) from AUTHENTICATED probe/echo frames too (or seed backup remotes from config), so a StateUp-via-probe path always has a usable remote before it becomes active. This authenticated remote-learning is the same mechanism that gates D9's unauthenticated-DATA remote-learn DoS.
- ledgerRefs: ["tasks:T15","tasks:T37","goals:G1"]
- fix: "Resolved by T37 (merged 03c8651): the Bind receiver now learns ps.setRemote(srcAP) from AUTHENTICATED probe/echo frames, and reflection runs independently of getRemote/scheduler, so a concentrator backup path acquires a usable return remote from probe traffic BEFORE it becomes active. A probe-only StateUp path has getRemote()==true, so scheduler failover to it no longer returns errNoHealthyPath. Verified by both T37 reviewers + the blackhole->failover test's post-failover Send."

### D12 — resolved

- createdAt: 2026-07-07T00:15:10.403Z
- updatedAt: 2026-07-07T11:54:56.190Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: Probe anti-replay has no session epoch — peer restart deadlocks liveness until seq catches up
- description: "Filed by the T37 review panel (opus). internal/telemetry/probe.go: Prober.nextSeq and the Reflector/AntiReplay high-water (guards[pathID]) are in-memory with NO per-session identity. When one peer RESTARTS, its nextSeq resets to 0 while the surviving peer's Reflector still holds the previous session's high-water N; every fresh probe (seq <= N) is rejected as ErrReplay, so no echoes are reflected, the restarted side's paths never reach StateUp, the scheduler's Pick() returns none, and no WireGuard handshake can be sent. Recovery only happens once nextSeq climbs past N (N probe intervals) — for a long prior session that is minutes-to-hours of full tunnel outage, precisely the failure a WAN-bonding VPN must not have. T37's live probe wiring EXPOSES this; it is outside T37's stated acceptance (cold bootstrap + blackhole failover, both starting from EMPTY high-waters, pass). Severity HIGH."
- severity: high
- rootCause: "Confirmed by the T37 review against source: the D4 anti-replay high-water is a strict-monotonic in-memory counter with no session/boot identity, so a peer that restarts (seq from 0) is indistinguishable from a replay attacker to the surviving peer, which rejects the entire fresh probe stream until the counter organically exceeds the stale high-water. The fix is a wire/protocol change (session epoch), hence a separate task, not a T37 rework."
- suggestedFix: Carry a random per-boot session id in the Probe frame (INSIDE the MAC-covered body) and key the Reflector's anti-replay by (sessionId, pathID), resetting the high-water when a NEW sessionId is first observed on a path; the originator's HandleEcho guard resets likewise on its own boot. Preserves strict-monotonic replay protection WITHIN a session while accepting a restarted peer's seq-from-0 stream. Owned by task T38.
- ledgerRefs: ["tasks:T37","tasks:T38","goals:G1"]
- fix: "Resolved by T38 (merged c64d794). A responder-contributed challenge establishes peer freshness: the Reflector issues a confidential, MAC-covered, per-adoption-rotated non-zero issuedChallenge (inside obf(body), readable only with the PSK); a session-epoch RESET is authorized ONLY when a probe echoes the current issuedChallenge — which a replay attacker cannot know. A genuinely restarted peer bootstraps in a bounded 2-round handshake (round 1 challenge-0 reflected -> learns challenge; round 2 echoes it -> adopted, high-water reset), recovering within the T13 detection window instead of the minutes-to-hours D12 deadlock. Memory is O(paths) (no retired-session set). NOTE: the FIRST T38 design (peer-chosen random SessionID) was itself unsound — the fable review reproduced a session-seizure bypass (unpredictability != freshness: a replayed never-observed probe seized the session and locked out the legit peer); the merged responder-challenge redesign closes it, verified by both reviewers re-running the seizure reproduction (now fails to seize)."

### D13 — resolved

- createdAt: 2026-07-07T12:57:43.704Z
- updatedAt: 2026-07-08T21:36:59.940Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: IPv6 path sources never qualify for device binding (link-local counts toward familyCount)
- description: "Filed by the T16 review panel (fable), file-and-defer. internal/bind/pathsock.go interfaceInfo counts EVERY same-family address, and an up interface virtually always carries a kernel fe80::/10 link-local — so a GLOBAL v6 source sees familyCount>=2 and selectDeviceBinds always falls back to source-IP binding (verified empirically in a netns). Consequence: v6 uplink paths never get T16 re-roam survival (they degrade to the pin-preserving pre-T16 source-IP bind, which is CORRECT but does not survive a same-address readdress). T16's acceptance (v4 fixture) is met; this is a v6 coverage limitation, low severity."
- severity: low
- rootCause: "Confirmed empirically by the T16 review (netns): interfaceInfo's family-count includes link-local, so any global-v6 source on a normal interface (global + fe80:: link-local) has familyCount>=2 and never device-binds."
- suggestedFix: For a GLOBAL (non-link-local) v6 source, EXCLUDE link-local addresses from familyCount — the kernel never source-selects a link-local for a global destination, so device binding still provably preserves the source_addr pin and v6 re-roam survival is restored.
- ledgerRefs: ["tasks:T16","goals:G1"]
- dependsOn: ["T43"]

### D14 — resolved

- createdAt: 2026-07-07T12:58:05.119Z
- updatedAt: 2026-07-08T21:39:16.963Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: e2e harness Setup races prior invocation teardown (transient RTNETLINK 'File exists' on veth create)
- description: "Filed by the T16 review panel (fable), file-and-defer. Pre-existing harness behavior in test/e2e/netns.go (NOT introduced by any recent diff): when `go test -tags e2e ./test/e2e` invocations run BACK-TO-BACK on the same host, Setup can fail with `ip link add wbAe type veth peer name wbAc: RTNETLINK answers: File exists`, cascading to mass instant failures. Observed twice consecutively on llm-ubuntu-0 immediately after a prior run; unreproducible on a quiescent host (two subsequent full-suite runs green). Suggests asynchronous teardown (holder-process kill / namespace reaping) racing the next Setup. Low severity (test-infra flake, not a product defect)."
- severity: low
- rootCause: "Confirmed by the T16 review: e2e Setup creates fixed-name veths (wbAe/wbBe) but Teardown returns before the prior namespace/holder-process is fully reaped, so a back-to-back run finds the leftover link (RTNETLINK File exists). Pre-existing (adjacent to D3's fixed-sleep test-hardening class)."
- suggestedFix: Make Setup idempotent (delete any leftover wbAe/wbBe links before creating) OR make Teardown synchronously wait for the holder process + link removal before returning. Pick up with the D3 test-hardening pass.
- ledgerRefs: ["goals:G1"]
- dependsOn: ["T42"]

### D15 — resolved

- createdAt: 2026-07-07T14:02:24.568Z
- updatedAt: 2026-07-07T15:47:04.765Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: End-to-end failover recovery intermittently exceeds the 3s P1 budget (reply-direction detection not in the budget composition)
- description: "Filed by the T20 review (fable), REPRODUCED on hardware (llm-ubuntu-0 amd64/4vCPU): TestP1Failover cycle-0 recovery exceeded ~3.1s in 4 of 14 runs — the P1 milestone acceptance ('throughput restored within P1RecoverySeconds=3s after killing the active WAN') is operationally UNMET ~30% of the time. Instrumented (info log): the EDGE daemon marks the killed path down at kill+~1.7s (silence_ms 1696-1711) and switches egress at kill+~1.9s, yet no end-to-end reply arrives within 3s even with a 0.4s measurement quantum (2/6 still >3.1s) — so the residual >1.2s sits between the edge egress switch and BIDIRECTIONAL traffic resumption, most plausibly the CONCENTRATOR-side (reply-direction) path-down detection composing with edge-side recovery. thresholds.go's composition analysis budgets ONLY the edge-side detect term (DownAfter + one interval + sub-ms reroute) and ignores the reply direction; with the daemon's DownAfter=1500ms + interval=250ms giving ~1.75s per-side detection, the margin to 3s is too thin once BOTH directions must fail over (and under iperf3 CPU load the probe/Tick timing jitters). This is a PRODUCT defect in the failover machinery (T13 liveness timing / T15 scheduler / T37 probe transport), NOT the T20 test. Severity HIGH — blocks the P1 milestone acceptance."
- severity: high
- rootCause: "Partially root-caused on hardware by the T20 review: bidirectional failover = max(edge-side detect, concentrator-side detect) + reroute; each side's detect is DownAfter(1500ms)+up-to-one-interval(250ms) ≈ 1.75s measured from the last echo, leaving only ~1.25s margin to the 3s budget. Under CPU-loaded netns conditions the two sides' detection composes and the timing jitters past 3s in a minority of runs. Owned by T39 (investigate the exact reply-direction tail + tighten timing)."
- suggestedFix: "In T39: reproduce with BOTH daemons at info level and synchronized timestamps; measure the concentrator's down-detection + reply-path switch latency relative to the kill; then either tighten DownAfter/interval so max(edge,conc) detection + reroute fits 3s with comfortable margin, and/or make the reply-path switch piggyback on the edge's roam (the first authenticated packet on the new path should immediately redirect replies rather than waiting a full independent DownAfter). Reconcile thresholds.go's composition comment to budget BOTH directions."
- ledgerRefs: ["tasks:T20","tasks:T39","goals:G1"]
- fix: "Resolved by T39 (merged c79a95b). Root cause: the concentrator's single probe-loop Tick goroutine was starved under iperf3 CPU load (GOMAXPROCS saturation, ~1s scheduling jitter), delaying its independent reply-direction path-DOWN detection past 3s. Fix: tickLivenessFromReceive advances liveness off the always-scheduled per-path RECEIVE goroutines (throttled <=once/interval via atomic CAS, TryLock-safe against Close/readersWG), so detection no longer depends on the starved timer; plus a timing tighten 1500/250->1200/200ms (same 6:1 false-down tolerance). Independently HARDWARE-VALIDATED on llm-ubuntu-0 under saturating bidir load: implementer 42/42 recoveries <3s (worst 2464ms), fable 16/16 (recovery max 2099ms, reply-direction conc_switch max 1970ms) — vs the prior 4/14 >3.1s. Bidirectional failover now reliably meets the P1 3s budget with ~900ms margin."

### D16 — resolved

- createdAt: 2026-07-07T14:02:31.334Z
- updatedAt: 2026-07-07T15:47:09.100Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: thresholds.go PLiveness constants + composition comment drifted from the daemon's actual defaults
- description: "Filed by the T20 review (fable). test/e2e/thresholds.go declares PLivenessProbeInterval=200ms / PLivenessDownAfter=2s and its P1 composition analysis reasons from those values ('2.0s + 0.2s = 2.2s'), but the daemon the failover e2e actually runs uses internal/device/device.go defaults: defaultProbeInterval=250ms, defaultProbeDownAfter=1500ms, defaultProbeUpSuccesses=3, defaultFailbackDwell=5s. The 'single source of truth' claim in thresholds.go does not hold for the daemon path, risking a wrong retune. Pre-existing (untouched by the T20 diff). Severity low."
- severity: low
- rootCause: "Confirmed by the T20 review: the telemetry-component e2e constants (PLiveness*) and the daemon's device.go probe-timing defaults are two independent sets; the standalone liveness test uses the former, the failover daemon uses the latter, and thresholds.go's composition comment reasons from the former while claiming to be the single source of truth for the failover budget."
- suggestedFix: "In T39: either have the e2e daemon config set its probe timings FROM the thresholds constants (make thresholds.go authoritative), or split the constants into 'prober-component e2e' vs 'daemon defaults' and have the composition comment reference the daemon's actual values (and both failover directions)."
- ledgerRefs: ["tasks:T20","tasks:T39","goals:G1"]
- fix: "Resolved by T39 (merged c79a95b): the daemon (internal/device/device.go) and test/e2e/thresholds.go now BOTH read one telemetry.Default* set (DefaultProbeInterval=200ms, DefaultDownAfter=1200ms, DefaultUpSuccesses=3) — single source of truth, no drift. thresholds.go's P1 composition analysis now budgets BOTH failover directions (recovery ≈ max(edgeDetect, concDetect) + reroute) rather than only the edge-side term, and its prose is internally consistent with the constants (worst-case detect = DownAfter + one interval = 1.4s; PLivenessFailoverBudget = 1.6s with one interval of headroom; ~1.4s jitter margin to the 3s deadline). Verified against liveness.go's strict-'>' Tick by both T39 reviewers."

### D17 — resolved

- createdAt: 2026-07-07T15:19:14.078Z
- updatedAt: 2026-07-07T15:22:56.706Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: TestPSKMismatchRejected intermittently accepts a wrong-PSK frame under -race (~1/40) — auth false-accept or non-deterministic test PSKs
- description: "Filed by the T39 review (fable), OUT OF SCOPE for T39 (frame package / T11, untouched by the T39 diff). During `go test -race ./internal/...`, internal/frame TestPSKMismatchRejected failed ONCE at frame_test.go:278 — 'kind 4: PSK-mismatched frame accepted' (a Control frame encoded under PSK-A decoded successfully under PSK-B) — then passed 40 consecutive re-runs. A wrong-key frame must ALWAYS fail the MAC; even a rare accept is an authentication concern. NOTE: the frame codec uses HMAC-SHA256 truncated to 16 bytes (128-bit) compared constant-time (verified in the T11 review R7), so a random tag COLLISION would be ~2^-128, NOT 1/40 — the ~1/40 rate strongly implies the TEST occasionally derives EQUAL/colliding PSKs (non-deterministic key generation) rather than a real crypto bypass, but this MUST be confirmed to rule out an actual auth false-accept path in Decode's MAC verification. Severity HIGH until disproven."
- severity: low
- suggestedFix: "In TestPSKMismatchRejected, assert the wrong-PSK decode does not yield a frame of the SAME AUTHENTICATED KIND it was encoded as: `if f2, err := Decode(pskB, raw); err == nil && f2.Kind() == f.Kind() { t.Fatalf(...) }` (a wrong-PSK Control/Probe must never decode as a valid Control/Probe; a garbage decode landing on the unauthenticated DATA/PARITY kind is EXPECTED and correct, not an auth failure). Document the ~2/256 rationale in a comment."
- ledgerRefs: ["tasks:T11","goals:G1"]
- rootCause: "ROOT-CAUSED (opus, in-session diagnosis, evidence): NOT a security bug — a TEST-ASSERTION bug. testPSK is deterministic (pskA seed 0x11, pskB 0x22, distinct); Encode uses a fresh random NONCE per frame. Over 20000 Decode(pskB, Encode(pskA, Control)) trials, 152 (0.76% ≈ 2/256) returned err==nil, and ALL 152 decoded to KindData(1) or KindParity(2) — the UNAUTHENTICATED kinds; ZERO decoded as the authenticated Probe(3)/Control(4). Mechanism: under the wrong PSK the body de-obfuscates to garbage with a uniformly-random kind byte; ~2/256 of the time it is DATA/PARITY, which Decode legitimately returns WITHOUT a MAC check (DATA/PARITY are unauthenticated by design; D9 threat model tolerates DoS-grade DATA forgery; inner WireGuard authenticates real DATA). The genuine auth property — a wrong-PSK frame can NEVER be accepted as a valid AUTHENTICATED Control/Probe — HELD in all 20000 trials (the 16-byte HMAC is sound). So TestPSKMismatchRejected's assertion (err==nil => fail) is too strong: it also fires on the expected ~2/256 garbage-decodes-to-unauthenticated-kind case. Fix is a one-line test correction."
- fix: "Resolved directly (test-only fix, merged af31005 on main). NOT a security bug — the crypto is sound (0/20000 wrong-PSK decodes ever yielded a valid AUTHENTICATED Control/Probe; the 16-byte HMAC holds). TestPSKMismatchRejected's assertion was too strong (err==nil => fail): a wrong-PSK decode legitimately returns err==nil ~2/256 of the time when the garbage-deobfuscated random kind byte lands on the UNAUTHENTICATED KindData/KindParity (which carry no MAC by design; D9 threat model). Fixed the assertion to require err==nil AND decoded-kind == encoded-authenticated-kind before failing, with a comment documenting the ~2/256 garbage-to-unauthenticated-kind rationale. Verified stable: 500 -race runs pass. This also keeps T39's -race acceptance gate reliable (the flake no longer intermittently breaks `go test -race ./...`)."

### D18 — resolved

- createdAt: 2026-07-07T16:40:13.519Z
- updatedAt: 2026-07-07T18:04:58.995Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: P1 failover recovery tail intermittently exceeds 3s under REPEATED flap on hardware (~7% per kill)
- description: "Filed by the T20 flap review (fable), REPRODUCED on llm-ubuntu-0 (4 vCPU, the T39 evidence host): 3 of 15 TestP1FailoverRepeatedFlap runs at c495839 failed on GENUINE budget exceedances — cycle-1 recovery 3476ms (both ends), cycle-2 recovery 3151ms (edge), and one cycle-2 failover not observed within the 4s window (magnitude lost). ~6.7% per-kill (3/45 kills) vs 0/46 for SINGLE-kill TestP1Failover (T39's 42/42+16/16 + 2/2 this session all <3s). The cycle-1 fix + non-wedge + no-reset held 15/15; this is purely a per-cycle recovery-latency tail specific to the REPEATED-flap-under-sustained-saturating-bidirectional-load scenario. The core single-WAN-death P1 acceptance (the actual requirement) is MET; this is a stringency tail under a pathological 3-kills-in-65s stress. Severity high (fable) — but note the core P1 is met and the tunnel always recovers/fails-back."
- severity: high
- rootCause: "CONFIRMED real PRODUCT defect (not shared-VM noise; reproduced on a quiet exclusive host): failover was PULL-BASED — scheduler.Pick() ran ONLY from the Send path. On a repeated flap the kill lands during an egress lull (both TCP dirs stalled mid-reroute, no application Send), so nothing called Pick() and the bond stayed wedged on the dead path until the 25s WG keepalive (~1/6 per-kill at load, 2984-4000ms+). T39's receive-tick advanced LIVENESS but did not TRIGGER the switch — the switch itself was still Send-gated. RESOLVED by T40/merge d4047a7: nudgeSchedulerActive() calls Pick() from tickLivenessFromReceive + emitProbes (eager liveness-driven failover). Deterministic repro TestSweepDrivesEagerFailover fails pre-fix / passes fixed (fable overlay-verified). Hardware: flap 22/22 PASS (0 wedged, max 1364ms) under saturating load. opus+fable go-ahead (R26). See reviews:R26."
- suggestedFix: "In T40: (1) run TestP1FailoverRepeatedFlap MANY times with HOST LOAD recorded to separate a product tail from shared-VM noise; instrument per-kill probe-loop-tick + receive-tick latency across consecutive cycles. (2) If product: bound the transition-window gap — e.g. emit probes more aggressively on a detected active-path change, have the scheduler nudge liveness on Pick, or ensure the receive-path tick fires from the OUTBOUND/Send path too (Send is scheduled during the reroute). (3) Validate the flap passes RELIABLY (>=19/20) on hardware."
- ledgerRefs: ["tasks:T20","tasks:T39","tasks:T40","goals:G1"]

### D19 — resolved

- createdAt: 2026-07-07T16:45:28.384Z
- updatedAt: 2026-07-07T17:07:04.706Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: "Flaky HANG: TestMultipathVirtualEndpointIdentity blocks until the package test timeout (lost-wakeup in the T30 receive fan-in)"
- description: "Filed by the T20 review (fable), OUT OF SCOPE for T20 (its diff is e2e-only). internal/bind's TestMultipathVirtualEndpointIdentity (bind_test.go:~133) INTERMITTENTLY blocks FOREVER in Multipath.newReceiveFunc's drainer select (multipath.go:~543) waiting for a packet that never arrives, while the readLoop goroutine sits in UDP ReadFromUDPAddrPort (multipath.go:~436). Reproduced TWICE: once on a plain `just test` (hit the 600s package timeout -> unit gate RED) and once within 3 of 30 `-count=30` iterations. Pre-existing, in the T30 receive fan-in (readLoop -> resequencer -> single drainer). Intermittently REDS the unit gate and costs the full package timeout per hit — a real robustness/CI hazard. Severity medium."
- severity: medium
- suggestedFix: "INVESTIGATE (hypothesis, not confirmed): a lost-wakeup / ordering race between the virtual-endpoint send and the receive-func subscription — a packet SENT before the reader goroutine is registered/looping is dropped by the UDP socket, and nothing retries, so the drainer waits forever for a frame that will never come. Reproduce with `go test ./internal/bind -run TestMultipathVirtualEndpointIdentity -count=100 -timeout 120s -v`. Fix: add a bounded wait/retry on the send side (or fix the subscription-before-send ordering in the test/harness), and give the test its OWN short deadline so a hang fails fast instead of consuming the package timeout. Route via /cq:investigate if the root cause is in production readLoop/drainer wiring rather than the test."
- ledgerRefs: ["tasks:T30","goals:G1"]
- rootCause: "ROOT-CAUSED (opus, in-session, reproduced): a TEST bug, NOT a production lost-wakeup. TestMultipathVirtualEndpointIdentity sent OuterSeq 0 to path-0's socket and OuterSeq 1 to path-1's socket UP FRONT (sendDataTo encodes frame.Data{OuterSeq: uint64(pathID), PathID: pathID}), then received both. The two path sockets are read by INDEPENDENT readLoop goroutines with NO cross-path arrival ordering. The T18 resequencer pins its release point (next) to the FIRST-observed outer-seq; when path-1's reader won the race and Observed OuterSeq 1 first, next pinned to 1, OuterSeq 1 was delivered, and the later-Observed OuterSeq 0 (< next) was dropped as dropLate. The test's SECOND fn() call then blocked forever (250ms poll finds nothing; no third frame ever arrives) -> the whole package hit its timeout. The drainer/poke mechanism is sound (the poll backstop self-heals a lost poke); the frame was genuinely DROPPED, not stuck. Benign in production: continuous traffic + WG/TCP retransmit recover an early reorder-drop, and the per-Open first-seq re-pin stabilizes quickly."
- fix: "Resolved directly (test-only, merged 5488f42 on main): INTERLEAVE send+receive per path in TestMultipathVirtualEndpointIdentity — send OuterSeq i, receive it (pinning next deterministically to 0, then advancing to 1), then send OuterSeq i+1. Removes the cross-path arrival race so both frames are delivered in order regardless of reader scheduling. Verified: 300 -race runs pass in ~1.9s (previously an intermittent 90s package timeout). No production code changed. D20 (goroutine leak in TestMultipathEngineUpCanTransmit) remains a separate low deferred test-hardening item."

### D20 — resolved

- createdAt: 2026-07-07T16:45:35.495Z
- updatedAt: 2026-07-08T21:39:18.476Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: "Goroutine leak: TestMultipathEngineUpCanTransmit helper blocked on chan send outlives the test"
- description: "Filed by the T20 review (fable), OUT OF SCOPE for T20. The internal/bind package-timeout goroutine dumps show multiple goroutines from EARLIER-completed TestMultipathEngineUpCanTransmit runs stuck >1 minute on a channel send at engine_test.go:~99 — the receiver is gone after the test ends, so each run LEAKS the producer goroutine. Pre-existing, harmless per run, but it accumulates under -count stress and pollutes hang diagnostics (it co-appeared in the D19 timeout dumps). Severity low (test-only)."
- severity: low
- rootCause: "Confirmed by the T20 review from the bind-package timeout goroutine dump: TestMultipathEngineUpCanTransmit's helper does an unbuffered channel send (engine_test.go:~99) with no done-channel escape, so after the test returns and the receiver is gone the producer goroutine blocks forever on the send."
- suggestedFix: "Use a buffered channel (cap 1) or a `select { case ch<-v: case <-done: }` at engine_test.go:~99 so the producer can always exit when the test ends. Test-only change; fold into the D19 fix or a test-hardening pass."
- ledgerRefs: ["tasks:T30","goals:G1"]
- dependsOn: ["T42"]

### D21 — resolved

- createdAt: 2026-07-07T17:23:27.568Z
- updatedAt: 2026-07-07T18:05:01.984Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: Failover e2e tests leak a saturating iperf3 load flow on every early t.Fatalf path (contaminates subsequent/co-tenant runs)
- description: "Filed by the T40 review (fable). In BOTH TestP1Failover (MERGED on main, from T39) and TestP1FailoverRepeatedFlap (T40/T20 branch), the load client is started with `load := exec.Command(\"iperf3\", ..., \"-t\", loadSecs, \"--bidir\"); load.Start()` and is reaped ONLY by `load.Wait()` on the SUCCESS path — no t.Cleanup registers load.Process.Kill(). Any early Fatalf (bond-never-up, failover non-observation, or a failback wedge) returns WITHOUT calling Wait(), leaking an uncapped ~70s bidirectional iperf3 that keeps SATURATING the shared 4-vCPU host and contaminates subsequent runs AND concurrent tenants. Directly observed mid-campaign (a 101s-old orphaned `iperf3 -c ... -t 70 --bidir`). THIS is the source of the multi-tenant contention that confounded the D15/D18 hardware measurements throughout this session. Severity medium (test-hygiene, but it materially undermines every multi-run flap/failover hardware validation and pollutes the shared host)."
- severity: medium
- rootCause: "RESOLVED. Both copies now reap the load iperf3 on every exit path via t.Cleanup(load.Process.Kill()): TestP1Failover (single-kill) fixed on main (df6a18a); TestP1FailoverRepeatedFlap (flap) fixed in T40 and merged (d4047a7). A failed run can no longer leak a saturating ~70s --bidir flow. Confirmed clean across the T40 campaign: host load did NOT accumulate across 22 flap + 10 single-kill consecutive runs. See reviews:R26."
- suggestedFix: "Immediately after load.Start() in BOTH tests, register `t.Cleanup(func(){ if load.Process != nil { _ = load.Process.Kill() } })` so a failed run cannot leak a saturating flow. Fix the MERGED TestP1Failover copy on main directly (de-contaminates future hardware runs) and the flap-test copy in the T40 rework."
- ledgerRefs: ["tasks:T39","tasks:T40","goals:G1"]

## M10

### D7 — resolved

- createdAt: 2026-07-06T22:27:16.368Z
- updatedAt: 2026-07-09T19:29:40.915Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: Concentrator tunnel-interface ACCEPT rule is not reboot-persistent
- description: |
    Filed by the T32 review panel (opus+fable), file-and-defer. T32's provision inserts `iptables -I INPUT -i wanbond0 -j ACCEPT` into the RUNTIME chain only. The concentrator (o3, OCI Ubuntu) restores its INPUT chain from /etc/iptables/rules.v4 at boot, so a reboot silently drops the rule and inbound tunnel TCP hits OCI's default REJECT again — the exact fault T32 fixes reappears with no signal until re-provisioned. Out of scope for T32 (its acceptance asserts only the runtime chain state, report-only per Q12), but a standing testbed / real deployment needs the rule to survive reboots. Severity medium.
    
    RESOLVED 2026-07-08 (live-o3 manual ops, executed by the orchestrator under explicit user authorization — the agent DOES have o3 SSH access via the llm key; the earlier 'cannot reach o3' handoff claim was an error). Repo-side (T48) already merged. Live: installed iptables-persistent, persisted the deduped INPUT chain via `netfilter-persistent save` → /etc/iptables/rules.v4, netfilter-persistent service `enabled`. Reboot-survival EMPIRICALLY CONFIRMED: `sudo systemctl reboot` (boot_id 5d97988a-c298-4934-9f19-31e36fc4ada0 → d01b9ff4-2dfe-42b9-87d7-18072c598ab2, uptime 9wk→0min = genuine reboot, back in ~15s); post-reboot `iptables -S INPUT` retains a SINGLE `-A INPUT -i wanbond0 -j ACCEPT` before the terminal REJECT, so inbound tunnel TCP is no longer REJECTed across reboots. SSH access preserved throughout (deadman-guarded, policy -P INPUT ACCEPT cushion). o3 NOT deprovisioned — clean in-OS reboot only.
- severity: medium
- suggestedFix: Add a provisioning step (and document in T22's install doc) that persists the concentrator INPUT rule across reboots — `netfilter-persistent save` after insertion, or an idempotent edit of /etc/iptables/rules.v4, or a small systemd unit that re-applies on boot — guarded by a state check so re-runs stay no-ops; extend TestRealProvision to assert the persisted set.
- ledgerRefs: ["tasks:T32","goals:G1","tasks:T22"]
- rootCause: "Confirmed by the T32 review against the live o3 host: T32's provision inserts `iptables -I INPUT -i wanbond0 -j ACCEPT` into the RUNTIME chain only; OCI Ubuntu restores /etc/iptables/rules.v4 at boot, so a reboot drops the rule and inbound tunnel TCP hits the default REJECT again. Fix DEFERRED to T22 (install doc + reboot-persistence provisioning step) per ledgerRefs tasks:T22 — documented and ready-to-implement, not separately investigable."
- dependsOn: ["T48"]

### D8 — resolved

- createdAt: 2026-07-06T22:27:25.373Z
- updatedAt: 2026-07-09T19:29:49.541Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: Pre-existing duplicate rules in the o3 concentrator INPUT chain
- description: |
    Filed by the T32 review panel (fable), file-and-defer. Live `iptables -S INPUT` on o3 shows the OCI default rule block DUPLICATED (two `-j REJECT --reject-with icmp-host-prohibited` with a full unreachable copy of ESTABLISHED/icmp/lo/ntp/ssh after the first REJECT) and three identical `-p udp --dport 51820 -j ACCEPT` rules. This PREDATES T32 (its -C-guarded insert cannot duplicate) — it is residue of earlier NON-idempotent rule insertion during this session's manual P0 real-host bring-up. Dead/duplicate rules add audit noise and can mask future misconfiguration. Severity low; o3 host state only (not a code defect).
    
    RESOLVED 2026-07-08 (live-o3 manual ops, orchestrator under user authorization). BEFORE `iptables -S INPUT`: 3× `-A INPUT -p udp --dport 51820 -j ACCEPT` + a dead duplicate OCI block (second RELATED,ESTABLISHED/icmp/lo/ntp(sport 123)/ssh(dport 22)/REJECT sitting AFTER the first terminal REJECT, hence unreachable). ACTION: deduped to one canonical set via surgical `iptables -D` (collapse 51820 to one; delete every rule after the first terminal REJECT), then `netfilter-persistent save`. AFTER (and post-reboot): single `--dport 51820 ACCEPT`, single `-i wanbond0 ACCEPT`, single terminal REJECT, dead block removed (wanbond0=1, 51820=1, INPUT REJECT=1). Survived the reboot (see D7). No functional change to reachable rules (SSH/wanbond0/wireguard/iperf3 preserved); pure removal of redundant + unreachable rules.
- severity: low
- suggestedFix: In the reboot-persistence follow-up, deduplicate the o3 INPUT chain to one canonical rule set (single 51820 ACCEPT, single OCI default block) before persisting, with a before/after `iptables -S INPUT` capture. This is a one-time host cleanup on o3, not a repo change.
- ledgerRefs: ["tasks:T32","goals:G1"]
- rootCause: "Confirmed by the T32 review: the duplicate rules in o3's INPUT chain PREDATE T32 (whose -C-guarded insert cannot duplicate) — residue of this session's earlier NON-idempotent manual iptables inserts during P0 real-host bring-up. o3 HOST STATE ONLY, not a code defect (low). One-time dedup deferred to the reboot-persistence follow-up (with D7/T22) — a host cleanup action, not separately investigable."
- dependsOn: ["T48"]

## M6

### D22 — resolved

- createdAt: 2026-07-07T19:17:17.204Z
- updatedAt: 2026-07-08T21:55:51.037Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: Weighted-scheduler pacer sheds WireGuard control frames (handshakes/keepalives) indiscriminately under overload
- severity: medium
- description: "Filed by the T21 review (fable), file-and-defer (out of scope for T21). With pacing enabled, EVERY datagram — data, handshake init/response, keepalive — shares the same per-path Pick() token buckets, so under sustained overload control frames are dropped with the same probability as bulk data (~80% at 5x overload), delaying rekey (mitigated only by WG's 5s REKEY_TIMEOUT retries within the 90s attempt window). Pick() has NO frame-type visibility — a control-frame bypass/priority class needs Bind/interface plumbing, which belongs with the empirical pacing-sizing work (T23 bufferbloat/pacing, T35 load-cap), NOT T21. Pacing ships DISABLED by default so there is no default-config exposure. RELATED sizing note for the same follow-up: the T21 default per_path_capacity_fps=10000 (~115 Mbit/s at full MTU) means the aggregation gate may never engage on realistic slow uplinks until sized from measured BDP."
- suggestedFix: "In the pacing follow-up (T23/T35): classify WG control frames (handshake/keepalive) at the Bind and exempt or priority-class them (a small reserved per-path token budget or a control-frame bypass), and size per_path_capacity from measured BDP rather than a frame-count default. Requires Bind/interface frame-type plumbing that Pick() alone cannot provide."
- ledgerRefs: ["tasks:T21","tasks:T23","goals:G1"]
- rootCause: "Established by the T21 review (fable): the weighted pacer's per-path Pick() token buckets are frame-type-blind, so under sustained overload WG control frames (handshake/keepalive) are shed at the same probability as bulk data. Pick() has no frame-type visibility — a control-frame bypass/priority class requires Bind/interface frame-type plumbing that does not exist yet. DEFERRED: pacing ships DISABLED by default (no default exposure), and the fix belongs with future pacing-hardening/sizing work (needs the Bind to classify frame types + BDP-based capacity sizing, related to T35 load-cap). No owning task fixes it today; re-seed a pacing-hardening task when pacing is enabled by default or empirically sized."
- dependsOn: ["T47"]

### D23 — resolved

- createdAt: 2026-07-07T20:14:07.686Z
- updatedAt: 2026-07-08T21:42:04.626Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: Fixture comments misattribute the real-internet 150-170 Mbit/s figure as the in-fixture 1-vCPU crypto ceiling
- severity: medium
- description: "Found by the T23 review (fable), file-and-defer, PRE-EXISTING (introduced by T35 commit 83aa799, propagated since; NOT introduced by T23 which only repeats it in one new comment). test/e2e/netns.go:21, test/e2e/fixture_impairment_test.go:11-12,63, test/e2e/fec_baseline_test.go:18, and docs/p0-checkpoint.md:72 all state the netns fixture's CPU-bound tunnel throughput is '~150-170 Mbit/s on a 1-vCPU host'. That figure was actually measured over the REAL INTERNET between two hosts with ONE daemon each (.cq/goals.md G1 evidence: 'NOT CPU-bound, o3 wanbond ~24% of one core'), whereas the recorded IN-FIXTURE measurement on the 1-vCPU host is 12-46 Mbit/s CPU-bound (docs/p0-findings.md:216-225) — both daemons sharing one core. Every capped-fixture sizing decision derived from the 150-170 premise (T35's 50 Mbit default, the FEC baseline cap, T23's 40 Mbit P2 caps) inherits an unsupported margin claim for the 1-vCPU host."
- rootCause: "Provenance traced by fable: the 150-170 Mbit/s number is a cross-host real-internet single-daemon-per-host measurement (G1 evidence) mis-copied into netns-fixture comments as if it were the in-fixture (both-daemons-one-core) CPU-bound ceiling. The real in-fixture 1-vCPU ceiling is 12-46 Mbit/s (p0-findings). Introduced by T35 (83aa799)."
- suggestedFix: "Sweep the four locations: replace the figure with the per-host MEASURED in-fixture ceilings (12-46 Mbit/s on the 1-vCPU aarch64 host per p0-findings; measure once on the 4-vCPU amd64 host and record it), and state that capped-fixture tests require 2*cap (aggregation) or cap (single-path) below the EXECUTING host's measured in-fixture ceiling. Pairs naturally with T35/T23 capped-fixture work."
- ledgerRefs: ["tasks:T23","tasks:T35","goals:G1"]
- dependsOn: ["T49"]

## M7

### D24 — resolved

- createdAt: 2026-07-07T22:37:34.260Z
- updatedAt: 2026-07-08T21:43:29.112Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: FEC unrecoverable /metrics counter under-reports at traffic quiescence (recovery overstated after an incident)
- severity: low
- description: Found by the T25 review (fable), file-and-defer, PRE-EXISTING (T24 decoder design, not introduced by T25). The FEC decoder accounts a failed group as `unrecoverable` ONLY when the high-water advances past the retain window (fecRetainGroups=512; internal/fec/decoder.go evictStale ~232-242), and the high-water advances only when NEW groups are offered. When traffic stops or a link stalls, the last ~512 groups' repair failures are NEVER folded into wanbond_fec_unrecoverable_packets_total, so an operator scraping /metrics after an incident sees recovery OVERSTATED exactly when it matters (the recovered fraction reads high because the denominator's failures are still retained, not counted). Also affected the T25 e2e measurement (fixed test-side there by a trailing lossless drain that advances the high-water before the after-scrape).
- rootCause: "Group-count-only eviction: unrecoverable is counted at 512-group eviction, triggered solely by high-water advance on newly-offered groups. At quiescence the retained-but-doomed tail groups are never evicted → never counted. No time-based eviction and no snapshot-time accounting of retained-incomplete-past-deadline groups."
- suggestedFix: "Account retained-incomplete groups whose deadline/window has definitively passed at Stats()/snapshot time (without evicting them from the reconstruction buffer), OR add time-based eviction alongside the 512-group window so a stalled tail is folded into unrecoverable after its recovery deadline. Care: only count a group once it is definitively unrecoverable (past the point more parity could arrive), to avoid premature/double counting. Pairs with the adaptive-FEC observability work (T29) or a dedicated FEC-metrics hardening task."
- ledgerRefs: ["tasks:T24","tasks:T25","goals:G1"]
- dependsOn: ["T45"]

## M8

### D25 — resolved

- createdAt: 2026-07-08T00:36:55.771Z
- updatedAt: 2026-07-08T21:43:28.074Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: Adaptive-FEC varying-M correctness rests on an undocumented klauspost prefix-stability default; partial groups untested
- severity: medium
- description: "Found by the T29 opus review, file-and-defer (current code CORRECT + proven, latent fragility). The adaptive datapath (T29) emits groups coded RS(K,m) with a varying m and decodes them against a FIXED RS(K,ceiling) decoder. This is byte-exact ONLY because reedsolomon@v1.14.1's DEFAULT buildMatrix (Vandermonde × top-inverse) makes coding-matrix row data+j depend on (data,j) but NOT on total parity — so parity shard j is identical for RS(m,k) and RS(m,ceiling). opus PROVED this against the pinned library source. BUT: (a) klauspost does NOT document parity-prefix-stability as a public API guarantee — a future minor-version bump flipping the default to Cauchy/PAR1/Jerasure/leopard, or enabling fastOneParity for k=1, would SILENTLY corrupt every reconstructed payload (wrong inner datagram delivered to WireGuard) with NO test catching it; (b) TestVaryingParityDecodesAtCeiling only round-trips FULL groups (m=DataShards=10) with varying k — partial (deadline-flushed) groups where m<DataShards AND k<ceiling simultaneously (which the adaptive datapath DOES produce) are never round-tripped through the ceiling decoder."
- rootCause: The varying-M-decodes-at-ceiling invariant is an implementation detail of reedsolomon's default matrix, not a documented API guarantee, and the property test under-covers the (partial-m × partial-k) space the adaptive encoder actually generates.
- suggestedFix: "Hardening task: (1) extend the property test to cover partial m in [1,DataShards] × k in [0,ceiling] with byte-exact recovery through the single ceiling decoder; (2) PIN the guarantee — either assert at build time that the constructed generator-matrix parity rows are a stable prefix as total-parity varies, or add a go.mod version-pin note + doc comment that reedsolomon must stay on a version whose default New() uses the Vandermonde buildMatrix, and re-verify on any reedsolomon upgrade."
- ledgerRefs: ["tasks:T29","tasks:T24","goals:G1"]
- dependsOn: ["T45"]

### D26 — resolved

- createdAt: 2026-07-08T00:37:04.757Z
- updatedAt: 2026-07-08T21:41:05.546Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: Adaptive-FEC DEFAULT tuning (SafetyFactor 1.5, RaiseThreshold 5%) cannot meet a sub-1% residual SLA
- severity: low
- description: "Found by the T29 fable review + flagged by the T29 implementer, file-and-defer, PRE-EXISTING (T27 control-law design). adaptivefec DefaultSafetyFactor=1.5 sizes M=1 at 5% loss with K=10, giving ~1% post-recovery residual (E[max(0,D-1)]/K, D~Bin(10,0.05)) — 2x the P4ResidualLossMax=0.5% bound. And DefaultRaiseThreshold=0.05 means steady loss anywhere below 5% (e.g. 3-4%) raises NO parity at all, so residual equals raw path loss. A deployment enabling `[fec] adaptive = true` with DEFAULTS and expecting P4-grade masking silently gets a much weaker SLA. The T29 config knob [fec].safety_factor works around it (the P4 e2e sets 4.0 → M~3 → residual <<0.5%), but nothing maps (target residual, K, loss) → SafetyFactor/M for an operator."
- rootCause: The redundancy map is parameterized by a bare SafetyFactor multiplier + fixed hysteresis bands, none derived from a target-residual SLA; the defaults were chosen for stability (T27), not to hit a specific residual bound.
- suggestedFix: Derive the redundancy map from a TARGET-RESIDUAL parameter (invert the binomial residual for M given K and smoothed loss), OR ship a documented SafetyFactor/RaiseThreshold table per residual SLA in the ops/install docs. Consider making the residual target (not the bare multiplier) the config surface. Pairs with the adaptive-FEC ops documentation.
- ledgerRefs: ["tasks:T29","tasks:T27","goals:G1"]
- dependsOn: ["T46"]

## M9

### D27 — resolved

- createdAt: 2026-07-08T01:11:04.386Z
- updatedAt: 2026-07-08T01:37:13.415Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: "TestCodecPSKMismatch flaky (~0.8%): cross-PSK frame accepted when deobfuscated kind lands on unauthenticated DATA/PARITY"
- severity: medium
- description: "Found by the T26 review (fable), file-and-defer, PRE-EXISTING on main (reproduced 8/1000 on the base tree; NOT caused by T26). internal/frame/frame_test.go:167 TestCodecPSKMismatch: Codec.Decode under a WRONG PSK deobfuscates the kind byte to a random value; with p~2/256 it lands on KindData/KindParity, which skip tag verification BY DESIGN (unauthenticated DATA/PARITY — accepted DoS-grade forgery, the inner WG layer authenticates), so Decode returns a garbage frame with NIL error. The test unconditionally asserts a non-nil error, contradicting the documented wire model, so `go test ./...` (`just test`, the gate every implement-worker runs) is INTERMITTENTLY red at ~0.8%/run. Same class as D17 (TestPSKMismatchRejected, fixed af31005) but a DIFFERENT test that still has the vacuous assertion. Because it makes the shared gate flaky, it warrants a near-term fix even though it is out of T26 scope."
- rootCause: "Same as D17: DATA/PARITY are unauthenticated by design, so a wrong-PSK decode legitimately returns err==nil ~2/256 of the time when the garbage kind byte lands on an unauthenticated kind. TestCodecPSKMismatch asserts err!=nil unconditionally, which is false ~0.8% of the time."
- suggestedFix: "Mirror the D17 fix (af31005): on err==nil, assert the decoded frame is an UNAUTHENTICATED kind (KindData/KindParity) with garbage payload (never the original CONTROL/PROBE), OR drive Decode with fixed nonces covering both branches deterministically. Crypto is sound; this is a test-assertion bug that flakes the shared gate."
- ledgerRefs: ["tasks:T26","defects:D17","goals:G1"]
- fix: RESOLVED. Reproduced the ~0.8% flake (several failures in 1000 runs), then fixed TestCodecPSKMismatch (internal/frame/frame_test.go) to assert on the decoded KIND rather than err — a cross-PSK decode must never yield a valid frame of the same AUTHENTICATED kind (mirrors the D17 fix af31005; err==nil ~2/256 is legitimate when the garbage kind byte lands on unauthenticated DATA/PARITY). Verified 0/5000 failures (was flaky). Crypto sound; test-assertion bug only. Committed on main.

### D28 — resolved

- createdAt: 2026-07-08T01:33:40.193Z
- updatedAt: 2026-07-08T21:31:47.426Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: "`just lint` never lints e2e-tagged test sources (no --build-tags e2e)"
- severity: low
- description: Found by the T26 review (fable), PRE-EXISTING harness gap. The Justfile lint target runs `go vet ./...` + `golangci-lint run` WITHOUT `--build-tags e2e`, so test/e2e/*.go (all -tags e2e sources) is excluded from vet+lint entirely. A T26 unused-function was invisible to `just lint` and only surfaced under `golangci-lint run --build-tags e2e ./test/e2e/`; any future e2e-only lint defect (unused symbol, shadowing, etc.) will be equally invisible to the standard gate. This is why implement-workers' green `golangci-lint run` gate can miss e2e-tagged issues.
- rootCause: The lint/vet targets omit the e2e build tag, so the Go toolchain never compiles the e2e-tagged files during lint (build-tag-gated files are skipped unless the tag is set).
- suggestedFix: Add `--build-tags e2e` to the golangci-lint invocation (and `go vet -tags e2e ./test/e2e/...`) in the Justfile lint target, OR add a dedicated `lint-e2e` target invoked by `just lint`, so e2e-tagged sources are vetted+linted. Also fold `-tags realhosts` similarly if those sources need coverage.
- ledgerRefs: ["tasks:T26","goals:G1"]
- dependsOn: ["T50"]

## M13

### D30 — root-caused

- createdAt: 2026-07-13T14:45:31.520Z
- updatedAt: 2026-07-13T14:45:31.520Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: Promoted/runtime-added paths forgo SO_BINDTODEVICE, losing T16 re-roam survival until Close->Open
- severity: low
- description: "Filed by the T55 implement-review (opus), file-and-defer, PRE-EXISTING (shared with runtime AddPath, NOT introduced by T55). internal/bind/reconcile.go defaultDeferredListen (and AddPath) bind a reconciled/runtime-added path via plain net.ListenUDP pinning the SPECIFIC source IP, unlike Open's listenPath which may device-bind (SO_BINDTODEVICE + wildcard) a path that selectDeviceBinds proves safe. Consequence: a deferred path promoted by the background reconcile — like any AddPath-added path — does NOT survive a mid-session source-address change / T16 re-roam; its socket breaks and the path goes Down until the next full Close->Open re-binds it via listenPath."
- rootCause: The runtime/reconcile bind path (reconcile.go defaultDeferredListen + AddPath) uses net.ListenUDP with a source-IP pin and does not route through the planPathBinds/selectDeviceBinds/listenPath device-bind decision that Open uses at boot. So runtime-added and reconcile-promoted paths never get SO_BINDTODEVICE, and thus never get T16 re-roam survival, unlike boot-bound paths.
- suggestedFix: "In a separate task (a device-bind unification), route the runtime/reconcile bind through the same planPathBinds/listenPath device-bind decision Open uses (recompute bindDevs for the promoted/added path's source), so a promoted or runtime-added path gets the same re-roam resilience as a boot-bound one. Care: preserve the source_addr pin and the no-contended-device guard selectDeviceBinds enforces."
- ledgerRefs: ["tasks:T55","goals:G2"]

### D31 — resolved

- createdAt: 2026-07-13T15:09:47.360Z
- updatedAt: 2026-07-13T15:40:00.436Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: "T60 zero-bindable e2e assertion is environment-fragile: TEST-NET-1 source_addr is not reliably unbindable on real hosts (ip_nonlocal_bind/root)"
- severity: low
- description: "Found by hardware validation on llm-ubuntu-0 (amd64). The FIRST fix (commit 2173a12/8004d01: pin ip_nonlocal_bind=0 via disableNonlocalBind) does NOT achieve determinism -- hardware re-run: TestTolerantStartupFastFailModes FAIL 6/6 (~10.9s each); zero_bindable_paths_is_fatal FAIL every run; malformed_source_addr PASS 6/6; TestTolerantStartupDeferredPathPromotes PASS (16.7s, requires /usr/sbin in PATH for tc). Host ip_nonlocal_bind=0. The subtest is broken in TWO independent ways (see rootCause). The daemon behavior is CORRECT in all cases; this is purely a test-correctness defect. W1 feature itself is validated by the passing deferred-promote test."
- rootCause: "The first fix mis-identified the variable. TWO independent root causes: (A) The gating factor for whether a non-local bind returns EADDRNOTAVAIL is INTERFACE PRESENCE/STATE, not the ip_nonlocal_bind sysctl. Kernel probe on host: lo-UP+pin=0 -> EADDRNOTAVAIL; lo-UP+pin=1 -> BIND_OK; lo-DOWN+pin=0 -> BIND_OK. The zero_bindable_paths_is_fatal subtest builds NO topology and never brings lo up, so lo is DOWN -> the non-local 192.0.2.x binds SUCCEED -> daemon comes up -> handshake fails -> 10s timeout (line 159). Ordering-dependent: run after a topology test (lo up) -> binds fail; run alone (lo down) -> binds succeed. The sysctl pin is irrelevant. (B) PRE-EXISTING contradiction in the ORIGINAL T60 commit 4f3b1f1: the subtest asserts output contains 'wanbond starting' (logged at Info in main.go:56), but writeT60EdgeConfig sets [log] level='error' -> slog.LevelError suppresses Info -> even on the fast-fatal (lo up) path the subtest fails at line 164. Empirically confirmed: 10s-timeout runs emitted zero Info lines at level=error. So the subtest cannot pass in EITHER ordering: lo-down -> line 159 timeout; lo-up -> line 164 missing-startup."
- suggestedFix: "RESOLVED at 62b0d97 (test/e2e/tolerant_startup_test.go + netns.go). Fix (A): bringLoopbackUp(t) brings lo UP in the subtest's netns so the non-local bind deterministically fails EADDRNOTAVAIL (interface presence, not the sysctl, was the gating variable); disableNonlocalBind pin kept as the second necessary condition. Fix (B): writeT60EdgeConfig [log] level='error'->'info' so the 'wanbond starting' Info marker is emitted (the assertion could never hold at error level). HARDWARE-CONFIRMED on llm-ubuntu-0 (amd64) pass 2: zero_bindable_paths_is_fatal PASS 5/5 across BOTH orderings (solo -count=3 'lo down' + combined 'lo up' + deferred-promote), INFO 'wanbond starting' present, exit 1 with 'no configured path could bind'; malformed_source_addr PASS 4/4; DeferredPathPromotes PASS 3/3 (~15-16s). No FAIL in any run. Daemon behavior was correct throughout; this was purely a test-correctness defect."
- ledgerRefs: ["tasks:T60","goals:G2"]

## M15

### D32 — resolved

- createdAt: 2026-07-13T16:18:50.224Z
- updatedAt: 2026-07-13T16:56:00.721Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: "Hub failover: switch decision + outer PROBE liveness recover to standby hub#2, but inner WG data traffic does NOT resume via the standby within the window (hardware e2e)"
- severity: high
- description: "Found by T62 hardware e2e on llm-ubuntu-0 (amd64), commit 5ffc411 (main + T62 test; T57 hub-failover feature merged). TestHubFailoverStandbySwitch FAILS deterministically 3/3 clean-setup runs at hub_failover_test.go:150 ('tunnel did not resume via standby hub#2 within 10.2s of the hub#1 outage'). The observed sequence after hub#1 is killed (its bridge port down = L2 blackhole): (1) edge detects all-paths-down (path liveness up->down, scheduler no eligible path); (2) edge LOGS the 'hub failover:' WARN naming hub#2 (from_index:0 to_index:1 to_endpoint:'10.100.0.3:51820' endpoints:2) — the T57 controller advances + repoints as designed; (3) OUTER path liveness RECOVERS to UP on hub#2 ~2s after the kill (path liveness uplink down->up; scheduler active path change reason:failover) and stays up, no flapping; (4) YET base.pingUntil(concInner) — inner tunnel traffic, which can ONLY be carried by hub#2 since hub#1 is blackholed — NEVER succeeds for the remaining ~8s of the 10.2s window. So the outer PROBE/liveness plane repoints to hub#2 and the T57 re-handshake fires, but INNER encrypted WG data does not traverse to hub#2. HUB_FAILOVER_RESUME_MS never emitted (success-path-only). The single-endpoint guard PASSES (no spurious switch). Test topology is correctly configured (verified statically): both hubs run full concentrator daemons with the IDENTICAL WG private key and the edge as an allowed peer (allowed_ips=concInner); edge peer has endpoints=[hub1,hub2], public_key=hubPub. So this is NOT a hub-misconfiguration; the defect is in the DATA PLANE after SetPeerRemote/ExpireCurrentKeypairs+SendHandshakeInitiation. Hypothesis to confirm on hardware: the WG handshake to hub#2 does not COMPLETE (hub#2 never establishes the session) OR completes but data does not forward — hub daemon logs are suppressed (config level='error') so this needs an instrumented hardware re-run (hub#2 at level info/debug + tcpdump/counters) to determine whether hub#2 receives+completes the edge's re-handshake. This validates that T57's unit tests (mocked liveness + recordingRemote) proved the CONTROLLER LOGIC but not the actual end-to-end data re-establishment; the e2e caught the gap. Q18 (multi-concentrator hub failover) is IN-SCOPE for the pilot, so this must work."
- ledgerRefs: ["tasks:T57","tasks:T62","goals:G2"]
- rootCause: "CONFIRMED by instrumented hardware reproduction (llm-ubuntu-0, edge+hub debug logs + tcpdump). Verdict (d): neither edge re-handshake timing (H-A) nor concentrator session establishment (H-B). The WG re-handshake COMPLETES ON THE WIRE IN BOTH DIRECTIONS: at T0+6.89s the edge's WG retransmit (try 2) egresses an initiation that REACHES hub#2 (wire: 10.100.0.1>10.100.0.3 len188), hub#2 logs 'Received handshake initiation' and 'Sending handshake response', and the response EGRESSES (wire: 10.100.0.3>10.100.0.1 len132). But the edge NEVER logs 'Received handshake response' -> keypair never established -> inner ping never succeeds. BREAKING STAGE: the edge's shared receive RESEQUENCER (internal/reseq/reseq.go admit()) DROPS hub#2's handshake-response DATA frame. WG payloads ride wanbond DATA frames tagged with the SENDER's per-bond outer-seq and are reordered by ONE shared reseq.Resequencer before the engine (PROBES are a different frame kind and bypass the resequencer -- which is exactly why OUTER liveness recovers while inner data does not). During the pre-kill iperf3 baseline hub#1 sent >>2048 DATA frames, advancing the resequencer release point `next` far above resequencerWindow (2048). Hub#2 is a SEPARATE process; its outer-seq starts near 1, so its response frame carries seq~1. In admit(): seq<next AND next-seq>window -> the SUSPECT branch; tryResync needs resyncCorroborate=3 distinct low seqs within one window to re-pin `next` backward, but only ONE hub#2 DATA frame arrives within the 10.2s window -> a lone frame only anchors the run, returns false -> dropSuspect++, dropped. Corroboration would need >=3 WG retransmits ~15s apart (past the window) -> deterministic permanent drop. The reseq.go comment names this exact case ('a peer restart resets outerSeq to 1, so its frames land here'). SetPeerRemote (multipath.go:1371) repoints path remotes ONLY -- it does NOT reset the resequencer, and NO Close/Open occurs on failover, so hub#1's high-water `next` persists across the concentrator switch. H-A (drop-at-switch, T0+1.6s 'Failed to send handshake initiation: no healthy path') is REAL but non-fatal (WG retries and egresses at +6.9s). H-B refuted (hub#2 both receives and responds)."
- suggestedFix: "The defect: a hub failover changes the DATA-frame SENDER IDENTITY (hub#1->hub#2, whose outer-seq restarts near 1) WITHOUT re-baselining the shared receive resequencer's release point `next`. Fix in internal/reseq + the failover path. Preferred (minimal): on the SetPeerRemote hub switch, RESET/re-pin the reseq.Resequencer so the standby's fresh low outer-seq is accepted as the new baseline (treat a concentrator switch as an explicit resync trigger -- we KNOW the sender changed, so re-anchor `next` proactively instead of waiting for the 3-frame tryResync corroboration that cannot complete within the failover window). Wire it: the hubFailover controller (device/failover.go) already calls mp.SetPeerRemote(next) on switch; give Multipath a way to reset its resequencer (m.resequencer.Load().Reset() or equivalent) invoked from the same switch action. Alternative (more invasive): key resequencing per remote/sender rather than one persistent per-bond `next`. Add a unit test in internal/reseq: with `next` advanced past the window, a Reset (or switch signal) followed by a low-seq frame must ADMIT it (not dropSuspect). Then combined hardware re-validation with D33 fix -> TestHubFailoverStandbySwitch passes. Host artifacts: /tmp/d32-edge.log /tmp/d32-hub1.log /tmp/d32-hub2.log on llm-ubuntu-0."

### D33 — resolved

- createdAt: 2026-07-13T16:19:04.477Z
- updatedAt: 2026-07-13T17:08:05.957Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: "T62 hub-failover fixture has an intermittent netns setup race: `ip link set wbHfcN netns <pid>` vs immediate `nsenter ... ip addr add` -> 'Cannot find device wbHfcN' (~50% of setups)"
- severity: medium
- description: "Found by T62 hardware e2e on llm-ubuntu-0. startHubHolder/setupHubFailover (test/e2e/hub_failover_test.go ~lines 127/188/362) fails roughly half of all test SETUPS (both TestHubFailoverStandbySwitch and TestHubFailoverSingleEndpointGuard, both hub veths) with: 'nsenter -t <pid> -n ip addr add <ip>/24 dev wbHfcN: exit status 1 / Cannot find device \"wbHfcN\"'. Root cause: a race between `ip link set wbHfcN netns <pid>` (moving the veth end into the hub netns) and the IMMEDIATELY-following `nsenter ... ip addr add` on that device — the device move into the target netns is not yet visible when the addr-add runs. Verified NOT cross-run contamination: no leftover netns/holders/links persist after runs. This aborts the setup before any failover logic runs, so it does not corrupt the D32 verdict (that was obtained from clean-setup runs after retrying past the race), but the fixture is flaky and must be hardened (e.g. poll/wait for the device to appear in the target netns before addr-add, mirroring waitForNetns / waitLink patterns) for the test to be reliable/CI-usable. This is a TEST-infrastructure defect, distinct from the D32 data-plane defect."
- ledgerRefs: ["tasks:T62","goals:G2"]
- suggestedFix: "RESOLVED at dadb69c (on the T62 branch). The first fix (poll `ip link show`) was insufficient -- hardware showed the device can be VISIBLE to link-show yet the immediately-following `ip addr add` still fail 'Cannot find device' (netns attribute propagation lags visibility). Final fix: RETRY the ACTUAL `nsenter ... ip addr add hubIP/24 dev hubCeth` in a bounded 5s loop (50ms interval), breaking on first success (no duplicate-address). HARDWARE-CONFIRMED on llm-ubuntu-0: 13 runs / 26 setups (incl. a cold start) with ZERO 'Cannot find device'/addr-add-timeout failures (vs prior ~13% cold-start rate; expected ~3-4 failures at that rate). All 26 test executions PASS; TestHubFailoverStandbySwitch RESUME_MS 6056-6903ms within the 10200ms window; single-endpoint guard PASS every run."

### D34 — root-caused

- createdAt: 2026-07-13T16:49:15.745Z
- updatedAt: 2026-07-13T17:10:45.110Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: Post-Rebaseline release point can re-anchor to a prior-hub straggler (native or FEC-recovered) frame, briefly reintroducing the D32 drop
- severity: low
- description: "Filed by the D32-fix adversarial review (opus), file-and-defer, out-of-scope for D32 (which eliminates the DETERMINISTIC drop). internal/reseq/reseq.go Rebaseline() sets started=false, so the NEXT ingest -- Observe OR ObserveRecovered (reseq.go:187 and reseq.go:236, both re-pin next when !started) -- anchors `next` to whatever outer-seq arrives first, SOURCE-AGNOSTICALLY. If a late prior-hub DATA straggler or an in-flight FEC reconstruction from the dead hub reaches the shared resequencer BETWEEN multipath.go SetPeerRemote's Rebaseline() call and the standby's handshake-response frame, `next` re-pins to the stale HIGH prior-hub seq and the standby's low seq is again routed to admit()'s SUSPECT branch and dropped -- transiently reintroducing the D32 symptom. Bounded + self-healing: (i) failover fires only after the prior hub is declared fully DOWN (silent for the DOWN-detection interval), so stragglers are improbable; (ii) worst case degrades to the PRE-FIX slow path -- the standby's retransmitted handshake responses carry increasing outer-seqs (1,2,3...), so 3 within one window corroborate the ordinary tryResync in ~3*RekeyTimeout (~15s). NOTE: ~15s self-heal exceeds a 10.2s failover window, so IF the straggler race triggers, a bounded-window e2e could see a transient failure -- the hardware re-validation of the D32 fix is the operational check on how often this actually triggers."
- suggestedFix: "Gate the post-rebaseline re-anchor on SOURCE IDENTITY: only a frame whose outer source matches the newly-configured standby endpoint may re-pin `next` after a Rebaseline (carry the expected standby AddrPort into the resequencer, or have the bind drop pre-switch-source frames for a brief window after SetPeerRemote). Alternatively reset/ignore the FEC decoder across a hub switch so no dead-hub reconstruction can re-anchor. Keep the trusted-control-event property (no wire-frame path may trigger a rebaseline)."
- ledgerRefs: ["defects:D32","tasks:T57","goals:G2"]
- rootCause: "Fully root-caused by the D32-fix adversarial review (opus). Rebaseline() sets started=false, and BOTH ingest paths (Observe reseq.go:187, ObserveRecovered reseq.go:236) re-pin `next` to the first arriving frame SOURCE-AGNOSTICALLY. So a prior-hub straggler (late native DATA or an in-flight FEC reconstruction from the dead hub) arriving between SetPeerRemote's Rebaseline() and the standby's handshake-response would re-anchor `next` to the stale HIGH prior-hub seq, re-routing the standby's low seq to admit()'s suspect branch (transient D32 recurrence). Bounded + self-healing (failover fires only after the prior hub is fully DOWN + silent, so stragglers are improbable; worst case degrades to the pre-fix slow tryResync path that self-heals in ~3*RekeyTimeout via the standby's increasing retransmit seqs). DID NOT trigger in 39 hardware runs (13 D32-validation + 26 D33-validation), so it is not pilot-blocking; the deterministic D32 drop is eliminated. Fix when scheduled: gate the post-rebaseline re-anchor on SOURCE IDENTITY (only a frame from the newly-configured standby endpoint may re-pin `next`), or reset/ignore the FEC decoder across a hub switch. Keep the trusted-control-event property (no wire path may trigger a rebaseline)."

## M28

### D35 — wip

- createdAt: 2026-07-13T22:48:19.299Z
- updatedAt: 2026-07-14T12:02:29.786Z
- author: "opus-4.8[1m]"
- session: be1a85fd-55c8-4654-ae42-672792fc0238
- headline: allowed_ips = 0.0.0.0/0 wedges the WG handshake — full-tunnel config never establishes
- severity: high
- description: "[fixes-doc D1, S1 — production deploy, real hardware, bisected and confirmed.] With the concentrator peer's `allowed_ips = [\"0.0.0.0/0\"]` on the edge, the WG handshake NEVER completes — even fresh-restarting BOTH ends and waiting minutes (edge tx≈0; o3 rx floods to 2.3 MB with tx 9 KB, i.e. o3 receives but never answers; no handshake ever logged). Reverting the SAME peer to a concrete prefix (`10.77.0.1/32`) establishes in ~25 s, deterministically. The split default `[\"0.0.0.0/1\",\"128.0.0.0/1\"]` also works. The virtual-endpoint design (the engine never holds the real 89.168.124.91) rules out an endpoint routing loop, so the cause is a `0.0.0.0/0`-specific path — amneziawg-go allowed-ip trie or a wanbond special-case. This silently breaks the single most common full-tunnel config. Observed asymmetry suggests the RECEIVING side (o3) drops/never responds when the initiating peer carries the /0 allowed-ip — investigate the allowed-ips trie insert/lookup for the zero-length prefix and any wanbond handling of the peer allowed_ips."
- suggestedFix: "PRODUCTION PATH ALREADY MITIGATED by the T107 split shield (a config 0.0.0.0/0 is safe). The residual upstream-engine root-cause needs a privileged 3-arm handshake repro DEFERRED to the e2e hosts (o3.7mind.io + llm-ubuntu-0, the G2 pattern — the sandbox lacks CAP_NET_ADMIN for netns): drive a real WG handshake with (i) a LITERAL 0.0.0.0/0 in the UAPI set (bypassing splitDefaultRoute), (ii) 10.x/32, (iii) the /1+/1 split, with amneziawg-go verbose logging + pcap on the receiver, recording ConsumeMessageInitiation / SendHandshakeResponse firing + rx/tx bytes per arm; and pin o3's running amneziawg-go commit vs v1.0.4. If confirmed engine-side, the fix is upstream in amneziawg-go (or a documented wanbond guard that already exists via the shield)."
- sourceRefs: ["wanbond-fixes.md §A D1","wanbond-fixes.md §B I6","wanbond-fixes.md §C C3"]
- tags: ["production-deploy","full-tunnel"]
- rootCause: "PARTIALLY established (H1 uncertain). CONFIRMED: the T107 config-path shield is real and unconditional — splitDefaultRoute (internal/device/device.go:1071-1080) rewrites a config-literal 0.0.0.0/0 (and ::/0) into the /1+/1 pair for EVERY peer in uapiConfig (device.go:1052-1060) before the UAPI render, so wanbond's config surface can no longer hand the engine a raw /0 (production impact MITIGATED). RULED OUT: the originally-stated mechanism (an amneziawg-go allowed-ips trie zero-length-prefix defect suppressing the handshake response) is CONTRADICTED by amneziawg-go v1.0.4 source — insert/lookup handle cidr==0 cleanly (a /0 ALLOWS all, does not wedge), and the handshake-response path (receive.go:400-417 SendHandshakeResponse after ConsumeMessageInitiation) NEVER consults the allowedips trie; the trie is touched only on the decrypted DATA path (receive.go:521-543). The observed hardware wedge (o3 rx floods, tx≈0 with a literal /0; /32 or the split establish in ~25s) therefore remains UNLOCALIZED by read-only analysis — it is NOT the stated trie mechanism, and needs a privileged runtime repro to pin (possibly a version skew: o3's actual amneziawg-go commit vs go.mod v1.0.4, or a sender/route-side effect)."
- sessionLogs: [".cq/logs/20260714-085530-a300a1198741fda4e.md"]
- rawLogs: [".cq/logs/raw/20260714-085530-a300a1198741fda4e.jsonl"]

### D36 — root-caused

- createdAt: 2026-07-13T22:48:32.358Z
- updatedAt: 2026-07-14T09:00:55.833Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: "One-sided restart → multi-minute outage: peer holding the old WG session does not promptly re-handshake"
- severity: high
- description: "[fixes-doc D2, S1 — production deploy, confirmed repeatedly.] When EITHER end restarts, the peer still holding the old session does not promptly re-handshake; the tunnel stays down for MINUTES until a rekey timer fires. Restart edge → o3 stale → down for minutes; restart o3 → edge stale → down for minutes; only restarting BOTH ~together (both re-initiate from scratch) converges in ~25 s. For a WAN-failover product this is severe — any reboot of the Pi or the concentrator is a multi-minute client outage. NOTE the layer: this is the INNER WG session going stale, distinct from the already-resolved OUTER probe/liveness restart deadlock (defects:D12, fixed by the T38 responder-challenge session epoch) — outer paths recover, but the stale WG keypair on the surviving side is not superseded. Compounded by fixes-doc D3 (see the startup-handshake defect): the restarted side's first init fires before path liveness and is not aggressively retried."
- suggestedFix: "Extend the trusted resequencer re-anchor (Rebaseline) from the hub-failover-only trigger to a detected PEER RESTART, on BOTH the edge non-failover path AND the concentrator role. The T38 responder-challenge session-epoch (which already authenticates and detects a peer's new session on the OUTER plane, fixing D12) is the natural trusted control event: when a peer's authenticated session epoch changes (restart), call the peer's Resequencer.Rebaseline() so the next inbound DATA frame (the wrapped WG init/response) re-anchors `next` instead of being SUSPECT-dropped. Wire it in the concentrator per-peer path and the edge single-concentrator path (both currently lack any Rebaseline trigger). VALIDATE with a netns one-sided-restart e2e (deferred to the o3 + llm-ubuntu-0 hosts, G2 pattern): saturate the bond so the surviving resequencer's `next` advances past the window, then restart ONLY the edge (run A) and ONLY the concentrator (run B); assert reconvergence well under the WG rekey timeout (target ~= the ~25s both-ends-fresh baseline) and capture reseq dropSuspect/rebaseline counters + the wanbond_session_established 0→1 timestamp per direction. NOTE: static analysis predicts the edge-restart direction should recover in ~10s absent the drop, so the e2e also quantifies the true per-direction magnitude (field-reported as minutes)."
- sourceRefs: ["wanbond-fixes.md §A D2","wanbond-fixes.md §C C5"]
- ledgerRefs: ["defects:D12"]
- tags: ["production-deploy","restart","re-handshake"]
- rootCause: "CONFIRMED (H2). The multi-minute one-sided-restart outage is the OUTER-plane per-peer resequencer dropping the restarted peer's low-outer-seq frames as SUSPECT — the SAME mechanism as the hardware-root-caused D32, but on the one-sided-restart path which has no rescue trigger. Every inner WG datagram (including the opaque handshake initiation/response) is wrapped in an outer DATA frame and pushed through the peer's resequencer (internal/bind/multipath.go:1619-1650 rq.Observe). After a peer restarts, its outer-seq resets to ~1, far below the stale-high release point `next` the pre-restart high-rate stream advanced the surviving side's resequencer to; admit() (internal/reseq/reseq.go:285-297) treats any frame >1 window below `next` as SUSPECT and drops it (dropSuspect++) unless the unauthenticated tryResync corroborates 3 distinct low seqs within one window — which a freshly re-handshaking peer emitting ~1 DATA frame per RekeyTimeout cannot achieve in time (reseq.go:529-546, the Rebaseline doc naming exactly this as D32). The Rebaseline() trusted re-anchor that defeats this is wired to EXACTLY ONE caller, SetPeerRemote (multipath.go:2167-2182), whose only callers are the edge hub-failover controller — so a plain one-sided EDGE restart (live paths, no endpoint change) never triggers it, and the CONCENTRATOR role runs no failover at all (failover.go:496 noop), so neither side re-anchors on a plain restart. RULED OUT: a WG-layer supersede failure (H2(a)) — amneziawg-go's responder consumes+responds to a fresh init immediately (receive.go:399-417, gated only by tai64n + a 20ms flood window), so the stall is NOT the engine holding the old keypair; the init simply never reaches the engine because the resequencer drops it. COMPOUNDING: the restarted side's first init fires pre-liveness (D37) and is not re-driven off the first path-up edge, but WG's 5s retransmit bounds that to seconds."
- sessionLogs: [".cq/logs/20260714-090009-a8d87a208742a8de8.md"]
- rawLogs: [".cq/logs/raw/20260714-090009-a8d87a208742a8de8.jsonl"]

### D37 — root-caused

- createdAt: 2026-07-13T22:48:44.390Z
- updatedAt: 2026-07-14T09:05:20.532Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Startup handshake fires before path liveness and is not re-driven off the first path-up edge
- severity: medium
- description: "[fixes-doc D3, S2 — production deploy.] On every start the edge logs one `Failed to send handshake initiation: bind: no healthy path with a known remote endpoint` (paths not up yet); paths transition Up ~0.6 s later — and then nothing: the first init is wasted and re-initiation is not visibly driven off the path-up edge, leaving the tunnel waiting on WG's own retransmit timers. Compounds the one-sided-restart outage (defects:D36). WG should (re)initiate the moment the first path becomes live. Related observability: the same startup window logs `no healthy path` at ERROR during the normal ~1 s liveness warmup (fixes-doc I4 — downgrade/rate-limit it; tracked in the wanbond-fixes goal)."
- suggestedFix: "Drive a handshake (re)initiation off the FIRST path StateUp transition (the telemetry liveness edge → poke the engine to SendHandshakeInitiation / expire-and-retry), rather than relying on the engine's own back-off. FOLDED INTO the D36 fix goal G7 (same startup/restart handshake-robustness surface — both wire a trusted control event into the handshake/resequencer path). Validate in the same netns e2e: from cold start, time-to-first-handshake tracks path-up (+~1 RTT), not a 5s retransmit timer. The related ERROR-spam downgrade (I4) shipped separately via the engineLogger warmup coalescing (T103)."
- sourceRefs: ["wanbond-fixes.md §A D3","wanbond-fixes.md §B I4"]
- ledgerRefs: ["defects:D36"]
- tags: ["production-deploy","startup","re-handshake"]
- rootCause: "CONFIRMED (established by the D36/H2 investigation, evidence item 9). The edge's first WG handshake initiation fires before path liveness (every path starts Down; the bind returns ErrNoHealthyPath, 'no healthy path with a known remote endpoint') and is NOT re-driven when the first path reaches StateUp ~0.6s later: internal/device/device.go up() (:258-437) issues NO boot-time forced SendHandshakeInitiation, and startFailoverAndResolution returns a noop for a single-concentrator edge (internal/device/failover.go), so nothing pokes the engine off the liveness edge — the tunnel then waits on amneziawg-go's own RekeyTimeout (5s) retransmit back-off (timers.go:99-107). Confirmed no wanbond hook re-initiates on path-up. This is the compounding startup half of the D36 one-sided-restart outage (the restart half being the resequencer SUSPECT-drop, defects:D36)."
- sessionLogs: [".cq/logs/20260714-090009-a8d87a208742a8de8.md"]
- rawLogs: [".cq/logs/raw/20260714-090009-a8d87a208742a8de8.jsonl"]

### D38 — resolved

- createdAt: 2026-07-13T22:48:59.152Z
- updatedAt: 2026-07-14T08:56:52.560Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Auto device-bind silently defeats `ip rule from <source_addr>` policy routing on single-address VLAN-per-WAN edges
- severity: high
- description: "[fixes-doc D4, S1 — production deploy; workaround applied on the Pi.] selectDeviceBinds (internal/bind/pathsock.go) picks SO_BINDTODEVICE (wildcard source) for a one-address/one-path interface — EXACTLY the VLAN-per-WAN edge topology (eth0.231/eth0.232, one address each, per-WAN pinning via `ip rule from <source_addr>`). A wildcard-source socket never matches `ip rule from <source_addr>`, so the route lookup falls through to `main` (which has no route to the concentrator via that VLAN) → ENETUNREACH → silent path-down, ZERO packets on the wire, while `ping -I <source_ip>` proves the WAN works. Worked around in production with `ip rule add oif <dev> table N`. The T16/R23 conditional device-bind gating (single-address + single-path interface) was designed for roam survival but is precisely the wrong choice under source-based policy routing — the two selection criteria collide. Related: defects:D30 (runtime-added paths never device-bind — the mirror-image gap)."
- suggestedFix: FIX SHIPPED (bind="source" toggle T105/T106 + docs T112) — defect resolved. The suggestedFix's OPTIONAL leg (auto-detect a source-routed WAN / a `from <source>` rule and default to source-bind) did NOT ship and is a non-required enhancement, not a defect; open a separate goal only if the user wants it. The silent no-WARN device-bind fallback is tracked separately as D53. The stale config.go bind doc-comments are D60.
- sourceRefs: ["wanbond-fixes.md §A D4","wanbond-fixes.md §B I5","wanbond-fixes.md §C C2"]
- ledgerRefs: ["defects:D30","tasks:T16"]
- tags: ["production-deploy","device-bind","policy-routing"]
- rootCause: "CONFIRMED (H3). The collision is real: selectDeviceBinds (internal/bind/pathsock.go:136-139) auto-mode picks SO_BINDTODEVICE (wildcard source) for a one-address/one-path interface (the VLAN-per-WAN edge), and a wildcard-source socket never matches `ip rule from <source_addr>` → route falls to main → ENETUNREACH → silent path-down. THE FIX ALREADY SHIPPED and is verified working end-to-end: the `bind = \"source\"` toggle (config.BindMode, T105/T106) forces out[i]=\"\" (pathsock.go:127-130, \"the D38 escape hatch\") so the path pins the source IP; Open feeds each path's resolved Bind through planPathBinds→selectDeviceBinds (multipath.go:746-752, 613-627); the config surface is source/device/auto with a per-path + top-level default (config.go:84-96, 840-849); and docs/install.md §3b (T112) documents the collision + the `ip rule add oif <dev> table N` recipe + the toggle."
- sessionLogs: [".cq/logs/20260714-085530-a6ed9f2c043a11557.md"]
- rawLogs: [".cq/logs/raw/20260714-085530-a6ed9f2c043a11557.jsonl"]

### D39 — resolved

- createdAt: 2026-07-13T22:49:13.912Z
- updatedAt: 2026-07-14T09:11:14.336Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: "wanbond0 left DOWN + unaddressed; NetworkManager flushes the operator's address → cryptic `TUN write: input/output error`"
- severity: medium
- description: "[fixes-doc D5, S2 — production deploy; workaround applied on the Pi.] The daemon creates the tun but never brings the link UP or addresses it. On a NetworkManager host (RPi OS / Debian / Ubuntu desktop — the common edge) the tun is auto-managed: NM FLUSHES the operator's address on link-up and the interface stays DOWN across daemon restarts. Writing into it then yields the cryptic `TUN write: input/output error`. Worked around with NM `unmanaged-devices=interface-name:wanbond0` + an addressing oneshot unit. Note the shipped docs (T22/R27, docs/install.md §4) cover only systemd-networkd addressing — the NM case is undocumented (fixes-doc C1). Fix direction split across: daemon brings the link UP itself (fixes-doc I1, low-risk; addressing stays operator-owned), actionable EIO diagnostics (fixes-doc I3), interface/route persistence across restarts (fixes-doc I7), and the NM + oneshot doc recipes (fixes-doc C1/C4) — the improvements/doc side is tracked in the wanbond-fixes goal; this defect item covers the DOWN-link/NM failure mode itself."
- suggestedFix: "Minimum code fix: bring wanbond0 UP after creating it (link-up only; addressing remains operator-owned), and on TUN write EIO check link state/MTU and emit an actionable error (e.g. 'wanbond0 is DOWN — address & bring it up (install.md §4)'). Document NM unmanaged-devices + the PartOf oneshot pattern. Consider keeping the tun persistent across daemon restarts so addresses/routes/rules referencing it survive (I7)."
- sourceRefs: ["wanbond-fixes.md §A D5","wanbond-fixes.md §B I1","wanbond-fixes.md §B I3","wanbond-fixes.md §B I7","wanbond-fixes.md §C C1","wanbond-fixes.md §C C4"]
- ledgerRefs: ["tasks:T22"]
- tags: ["production-deploy","networkmanager","tun"]
- rootCause: "CONFIRMED as filed (wanbond-fixes D5, production deploy). The daemon created wanbond0 but never brought the link UP nor addressed it; on a NetworkManager host NM flushes the operator's address and the interface stays DOWN across restarts, so a TUN write yields the cryptic 'input/output error'. THE FIX SHIPPED across the G6 goal: the daemon now brings the link UP (I1, T100 SIOCSIFFLAGS IFF_UP), EIO writes emit an actionable diagnostic (I3, T102 diagnosingTUN), the tun can persist across restarts (I7, T109 tun_persist), the NetworkManager unmanaged-devices drop-in ships (C1, T110), and the addressing/persistence oneshot ships (C4, T111) — all documented (T112-T115). The DOWN-link/NM failure mode is addressed end-to-end."

### D40 — root-caused

- createdAt: 2026-07-13T22:49:25.742Z
- updatedAt: 2026-07-14T09:10:32.042Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: CAP_NET_RAW (pathsock comment) vs CAP_NET_ADMIN (shipped systemd units) mismatch for SO_BINDTODEVICE
- severity: low
- description: "[fixes-doc D6, S3 — production deploy.] internal/bind/pathsock.go says SO_BINDTODEVICE needs CAP_NET_RAW; the shipped systemd units (T22) grant only CAP_NET_ADMIN. Per the comment, device-bind should therefore always fall back — yet it bound successfully on o3, so the actual kernel requirement is version/context-dependent and the comment and the unit disagree. Reconcile: either grant CAP_NET_RAW in the units (if the requirement is real on any supported kernel) or fix the comment/fallback logic. (Historically SO_BINDTODEVICE required CAP_NET_RAW; kernels ≥ 5.7 allow it unprivileged — verify against the supported kernel range and encode the finding.)"
- suggestedFix: "Determine the precise capability requirement across supported kernels (test as an unprivileged/capability-restricted service on Debian stable + Ubuntu LTS kernels), then align: unit AmbientCapabilities/CapabilityBoundingSet, the pathsock comment, and the fallback path. Update docs/install.md accordingly."
- sourceRefs: ["wanbond-fixes.md §A D6"]
- ledgerRefs: ["tasks:T22"]
- tags: ["production-deploy","capabilities","systemd"]
- rootCause: CONFIRMED as filed (wanbond-fixes D6, production deploy). internal/bind/pathsock.go's comment says SO_BINDTODEVICE needs CAP_NET_RAW; the shipped systemd units (T22) grant only CAP_NET_ADMIN — yet device-bind succeeded on o3, so the actual kernel requirement is version/context-dependent (historically CAP_NET_RAW; kernels ≥5.7 allow it unprivileged) and the comment and unit disagree. File-and-deferred to goal G11 (determine the precise capability across supported kernels; align the unit CapabilityBoundingSet, the pathsock comment, the fallback, and docs/install.md).

## M23

### D41 — root-caused

- createdAt: 2026-07-13T22:55:09.949Z
- updatedAt: 2026-07-14T09:09:55.845Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Config loader silently ignores unknown/misspelled TOML keys
- description: "internal/config/load.go:34 decodes with non-strict toml.Unmarshal (go-toml/v2), so an unknown or misspelled key (e.g. `link_bandwith`, `nane`) is silently dropped even though Load's doc comment and docs/install.md describe the loader as fail-fast. Required keys are backstopped by validate(), but misspelled OPTIONAL keys become silently inert configuration. Pre-existing behavior, untouched by T80's diff. Filed from implement review of T80 round 1 ([fable] reviewer, file-and-defer per K13) as out-of-scope/pre-existing."
- severity: low
- suggestedFix: Use toml.NewDecoder(...).DisallowUnknownFields() (handling *toml.StrictMissingError for a precise message) so an unrecognized key fails config load; add a rejects-table case for a misspelled key.
- ledgerRefs: ["tasks:T80","goals:G4"]
- rootCause: "CONFIRMED as filed ([fable] T80 review; internal/config/load.go:34). Load decodes with non-strict toml.Unmarshal (go-toml/v2), so an unknown/misspelled key (link_bandwith, nane) is silently dropped though Load's doc + docs/install.md describe fail-fast; misspelled OPTIONAL keys become silently inert config. File-and-deferred to goal G9 (DisallowUnknownFields + a rejects-table case)."

## M24

### D42 — root-caused

- createdAt: 2026-07-13T23:57:19.238Z
- updatedAt: 2026-07-14T09:09:32.494Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Deferred AddPath desyncs per-peer probers from m.defs when >1 peer is bound (latent out-of-range panic in removeDurableLocked)
- description: "internal/bind/multipath.go: AddPath's EADDRNOTAVAIL deferred branch appends the minted prober only to the primary peer's probers (via the peerState embed) while growing m.defs, but removeDurableLocked splices EVERY peer's probers at defIdx assuming each is index-aligned with m.defs. With >=2 bound peers and a deferred path, RemovePath of that path evaluates p.probers[defIdx+1:] past the shorter secondary slice (slice-bounds panic), and removing an earlier path splices the wrong prober silently. Unreachable today — no production code constructs a second peerState — and the omission is a documented later-G4 placeholder, so it was out of scope for T83. Filed from implement review of T83 round 1 ([fable] reviewer, file-and-defer per K13)."
- severity: medium
- suggestedFix: In the G4 task that implements the concentrator deferred-path fan-out (T85-T88 area), mint per-peer probers for deferred paths so every peer's probers stays m.defs-aligned; until then, fail fast by refusing deferral (returning the bind error) when len(m.peers) > 1, or assert the alignment invariant in removeDurableLocked.
- ledgerRefs: ["tasks:T83","goals:G4"]
- rootCause: "CONFIRMED as filed (reviewer-pinned this run with source citations in the description; [fable] T83 review). Deferred AddPath's EADDRNOTAVAIL branch appends the minted prober only to the primary peer's probers while growing m.defs, but removeDurableLocked splices every peer's probers at defIdx assuming m.defs-alignment — with ≥2 bound peers + a deferred path this is a latent slice-bounds panic / mis-splice (internal/bind/multipath.go). Unreachable today (no second peerState constructed). File-and-deferred to goal G8 (multi-peer datapath hardening)."

### D44 — root-caused

- createdAt: 2026-07-14T00:14:32.635Z
- updatedAt: 2026-07-14T09:09:35.772Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: fecFlushDeadline drives only the primary peer's FEC group; per-peer fecSend added later (T91/T93) would never receive deadline parity flushes
- description: "internal/bind/multipath.go:1271 fecFlushDeadline reaches m.fecSend/m.scheduler/m.paths through the embedded-primary promotion, so only the primary's straggler FEC groups get deadline parity (likewise driveAdaptiveControllerLocked). No fault today — no non-primary peerState is ever given a fecSend — but T91 (lazy per-peer FEC instantiation) and T93 (per-peer device wiring) will populate per-peer fecSend, and neither task's text mentions fanning the deadline flush across bound peers; a non-primary peer's partially filled groups would then only close on fill, silently losing straggler parity. Out of scope for T85 (Send-side routing map only). Filed from implement review of T85 round 1 ([fable] reviewer, file-and-defer per K13)."
- severity: low
- suggestedFix: When per-peer fecSend is wired (T91/T93), make the flush timer iterate m.peers, ticking each peer's encoder and framing parity with that peer's sendCodec via encodeParityLocked(peer, ...), and drive the adaptive controller per peer.
- ledgerRefs: ["tasks:T85","goals:G4"]
- rootCause: "CONFIRMED as filed ([fable] T85 review; citation multipath.go:1271). fecFlushDeadline (and driveAdaptiveControllerLocked) reach m.fecSend/scheduler/paths through the embedded-primary promotion, so only the primary peer's straggler FEC groups get deadline parity; once per-peer fecSend is populated (T91/T93) a non-primary peer's partial groups would only close on fill, silently losing straggler parity. File-and-deferred to goal G8."

## M20

### D43 — root-caused

- createdAt: 2026-07-14T00:14:26.934Z
- updatedAt: 2026-07-14T09:10:00.043Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: "Pre-existing docs advertise string-duration config forms the loader rejects ([scheduler]/[fec])"
- description: "wanbond.example.toml documents collapse_dwell = \"2s\", load_tau = \"200ms\", weight_rtt_floor = \"1ms\" (~L115-123) and [fec] deadline = \"5ms\" (~L136) for fields typed time.Duration and decoded by go-toml/v2, which cannot decode a TOML string into time.Duration. Probe test confirmed: fec.deadline = \"5ms\" fails config.Load with 'toml: cannot decode TOML string into struct field config.FEC.Deadline of type time.Duration'. An operator uncommenting the documented example gets a load failure. Pre-existing (fields and doc lines pre-date T72). Filed from implement review of T72 round 1 ([fable] reviewer, file-and-defer per K13)."
- severity: medium
- suggestedFix: Accept Go duration strings uniformly for all operator-facing duration knobs (LinkRTTRaw-style raw string + time.ParseDuration in normalize, or a shared TOML-text-unmarshaling duration wrapper), and add a config test matrix loading every documented string form; alternatively correct the docs to integer nanoseconds (worse operator UX, inconsistent with link_rtt).
- ledgerRefs: ["tasks:T72","goals:G5"]
- rootCause: "CONFIRMED as filed ([fable] T72 review; probe-verified). wanbond.example.toml documents string-duration forms (collapse_dwell=\"2s\", [fec] deadline=\"5ms\", etc.) for time.Duration fields that go-toml/v2 cannot decode from a TOML string — config.Load fails with 'cannot decode TOML string into ... time.Duration'; an operator uncommenting the documented example gets a load failure. File-and-deferred to goal G9 (accept Go duration strings uniformly for operator-facing duration knobs + a documented-string-form test matrix)."

## M21

### D45 — root-caused

- createdAt: 2026-07-14T01:02:00.302Z
- updatedAt: 2026-07-14T09:10:36.059Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: "just lint red at base: 3 pre-existing findings in dnsresolve and bind"
- description: "golangci-lint (clean cache, nix develop shell) exits 1 at 276a6ea AND at base 817dac4: errcheck on unchecked deferred Close at internal/dnsresolve/doh.go:206 (resp.Body.Close) and internal/dnsresolve/dot.go:168 (conn.Close), plus staticcheck QF1001 at internal/bind/pathsock.go:166. All three files byte-identical between base and the T70 branch, so the breakage pre-exists T70. A red lint gate on the base masks new lint regressions in every in-flight task. Filed from implement review of T70 round 2 ([fable] reviewer, file-and-defer per K13)."
- severity: medium
- suggestedFix: Fix the two unchecked Close returns (assign or //nolint with justification per repo convention) and apply the QF1001 De Morgan rewrite, or reconcile the golangci-lint version/config drift in the dev shell if these linters were not previously enabled.
- ledgerRefs: ["tasks:T70","goals:G5"]
- rootCause: "CONFIRMED as filed ([fable] T70 review). golangci-lint exits 1 at base: errcheck on unchecked deferred Close (internal/dnsresolve/doh.go:206 resp.Body.Close, dot.go:168 conn.Close) + staticcheck QF1001 at internal/bind/pathsock.go:166. All three files byte-identical between base and the T70 branch, so the breakage pre-exists T70; a red base masks new lint regressions. File-and-deferred to goal G11 (fix the two unchecked Close + the QF1001 rewrite, or reconcile the lint version/config drift)."

### D46 — resolved

- createdAt: 2026-07-14T01:02:05.420Z
- updatedAt: 2026-07-14T01:43:20.545Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: "hubFailover: stale active with flattened total < 2 strands the bond despite one live standby record"
- description: "internal/device/failover.go: updateResolution's contract says a change that empties the active spec leaves the identity stale and 'the next check on hub loss advances off it' — but check's total<2 guard blocks that advance when the emptying leaves exactly one record in another spec (e.g. two hostname specs, active re-resolves empty, standby holds one record). The bond stays pointed at the gone address under full hub loss until an expansion grows. Reachable only if the T73 resolver ever publishes an empty expansion for a previously-resolved hostname. Filed from implement review of T70 round 2 ([fable] reviewer, file-and-defer per K13); resolve alongside/within T73."
- severity: low
- suggestedFix: In T73, either never publish an empty expansion (retain last-good records on NXDOMAIN/timeout), or exempt the stale-active case (flatIndexLocked == -1 with total >= 1) from check's total<2 guard.
- ledgerRefs: ["tasks:T70","tasks:T73","goals:G5"]
- fix: "Resolved within T73 (0d36a23): the re-resolution controller never publishes an empty expansion — resolveTarget retains the last-good records on lookup failure/NXDOMAIN/empty AND on all-family-filtered answers — so the total<2 stranding precondition is unreachable. Verified by TestResolutionEmptyResultRetainsLastGood and TestResolutionFamilyFilterRetainsLastGood; confirmed by both round-2 reviewers."
- dependsOn: ["T73"]

## M25

### D47 — root-caused

- createdAt: 2026-07-14T01:43:26.215Z
- updatedAt: 2026-07-14T09:09:39.788Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: "Source->peer binding keyed by address only: two peers behind one public IP can never both bind"
- description: "peerBySource is keyed by netip.Addr (address, not AddrPort) — a granularity inherited from the pre-existing placeholder map. Once peer A's PROBE binds a shared public IP (CGNAT / two edge sites behind one NAT), every frame from that IP — including peer B's PROBEs from a different port — is routed to A's view via lookupPeerBySource, fails A's codec, and is dropped; because bound sources bypass trial-decode entirely, B can never bind or carry traffic through the concentrator. Bindings are also never removed or re-keyed (no unbind path), so the exclusion is permanent. Filed from implement review of T88 round 1 ([fable] reviewer, file-and-defer per K13); settle in the T90 roaming design."
- severity: medium
- suggestedFix: "Settle key granularity in the T90 roaming design: either key bindings by AddrPort, or let an authenticated PROBE that fails the bound peer's codec re-enter trial-decode so a MAC-verified PROBE from another peer at the same address can establish/steal the binding."
- ledgerRefs: ["tasks:T88","tasks:T90","goals:G4"]
- rootCause: "CONFIRMED as filed ([fable] T88 review). peerBySource is keyed by netip.Addr (address, not AddrPort), so once peer A's PROBE binds a shared public IP (CGNAT), every frame from that IP — including peer B's PROBEs from a different port — routes to A's view via lookupPeerBySource, fails A's codec, and is dropped; bound sources bypass trial-decode, so B can never bind. No unbind/re-key path makes the exclusion permanent. File-and-deferred to goal G8 (settle key granularity: AddrPort keying, or let a MAC-verified PROBE that fails the bound peer's codec re-enter trial-decode)."

### D49 — root-caused

- createdAt: 2026-07-14T03:21:29.110Z
- updatedAt: 2026-07-14T09:09:44.293Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Global demux cap is monopolizable by one authenticated insider, starving other peers' bootstrap
- description: "Filed from T91 round-1 review ([fable], file-and-defer per K13). peerBySource has a SINGLE global cap (default 1024) with drop-on-exhaustion and a never-evict-live guard. A party holding ONE valid peer psk can send authenticated PROBEs from ~1024 distinct spoofed source addresses, binding every slot to its OWN peer, then keep that peer live — TearDownPeer refuses live peers, so the slots are never reclaimed and every OTHER configured peer's first PROBE is dropped forever (bootstrap denial). Violates the Q27(1) isolation intent ('a psk holder can disrupt ONLY its own tunnel'). Outside T91's acceptance (which requires only cap + no-evict) and not covered by T92's current acceptance text."
- severity: medium
- suggestedFix: Enforce a PER-PEER quota on source bindings inside bindSourceToPeer (small k per configured peer, summing under the global cap), and add the insider-cap-monopoly adversary case to T92's threat-model tests.
- ledgerRefs: ["tasks:T91","tasks:T92","goals:G4"]
- rootCause: "CONFIRMED as filed ([fable] T91 review). peerBySource has a SINGLE global cap (default 1024) with drop-on-exhaustion + never-evict-live; one valid-psk insider can send authenticated PROBEs from ~1024 spoofed sources, bind every slot to its own peer, and keep it live (TearDownPeer refuses live peers) — permanently starving every other peer's bootstrap PROBE. Violates the Q27(1) 'a psk holder disrupts ONLY its own tunnel' isolation intent. File-and-deferred to goal G8 (per-peer quota in bindSourceToPeer + the insider-monopoly threat test)."

### D50 — root-caused

- createdAt: 2026-07-14T03:21:33.691Z
- updatedAt: 2026-07-14T09:09:48.334Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Device wiring of TearDownPeer (peer session/liveness loss) is not tracked by any planned task
- description: "Filed from T91 round-1 review ([fable], file-and-defer per K13). T91's description says teardown is 'wired from device peer events', deferred by the worker as future G4 work — acceptable for T91 since its acceptance is bind-test-only and the M25/M26 DAG defers device wiring. But T93's text covers per-peer CONSTRUCTION and runtime path add/remove only; NO planned task wires WG session-teardown / liveness-loss events to Bind.TearDownPeer. Until wired, dead-peer heavy state (resequencer ring, FEC buffers) and demux cap slots are never reclaimed in production, leaving the T91 machinery inert."
- severity: medium
- suggestedFix: Extend T93's description/acceptance (or add an M26 task) to wire device per-peer session-teardown / liveness-loss events to Bind.TearDownPeer, with a device-level test that a dead peer's ring is freed and a re-handshake re-binds.
- ledgerRefs: ["tasks:T91","tasks:T93","goals:G4"]
- rootCause: "CONFIRMED as filed ([fable] T91 review). The T91 TearDownPeer machinery exists but NO planned task wires device WG-session-teardown / liveness-loss events to Bind.TearDownPeer (T93 covers per-peer construction + runtime path add/remove only). Until wired, a dead peer's heavy state (resequencer ring, FEC buffers, demux cap slots) is never reclaimed in production, leaving the T91 machinery inert. File-and-deferred to goal G8 (wire device per-peer teardown to Bind.TearDownPeer + a device test that a dead peer's ring is freed and re-handshake re-binds)."

## M30

### D48 — root-caused

- createdAt: 2026-07-14T03:18:22.478Z
- updatedAt: 2026-07-14T09:10:19.050Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: wanbond_path_tx_bytes_total omits probe and echo-reflection wire bytes (tx/rx accounting asymmetry behind path_up=1/tx=0)
- description: "Both T104 reviewers ([opus]+[fable]) independently source-confirmed: internal/bind/probe.go emitProbes (probe.go:44) writes each PROBE frame via conn.WriteToUDPAddrPort WITHOUT incrementing ps.txBytes, and dispatchInbound's echo reflection (multipath.go ~1284) is likewise uncounted. ps.txBytes.Add exists at exactly two sites — Send (multipath.go:1477) and fecFlushDeadline (:1603) — both DATA/PARITY paths. Meanwhile rxBytes counts EVERY inbound datagram (DATA, PROBE, echo — multipath.go:881) and the metric help string claims 'Total bytes transmitted on the path.' The asymmetry makes a healthy idle standby (active-backup collapses all DATA onto the primary) read path_up=1 with tx=0 while rx grows — exactly the G6/I8-motivating production observation. [fable] additionally empirically validated the T104 repro fixture (BlockEgress one-way tc-clsact block coexisting with netem). This is a METRICS-ACCOUNTING fault, NOT a liveness hole: liveness is genuinely bidirectional (only HandleEcho on our own probe's authenticated echo marks up; an egress-dead standby goes DOWN within DownAfter). The T104 subtest 'standby-transmits-when-idle' (test/e2e/standby_liveness_test.go) is the ready-made repro, predicted (source-consistent) to FAIL against current code until fixed."
- severity: medium
- suggestedFix: "In the fix task decide the counter contract: either count probe emission (emitProbes) + echo reflection into ps.txBytes so tx matches rx's true-wire-volume semantics (optionally add a separate DATA-only series for the data-thrift signal the doc at multipath.go:140-151 describes), or re-document/rename the metric so its help string stops claiming total transmitted bytes. Then flip T104's standby-transmits-when-idle subtest from repro-failure to the green acceptance check."
- ledgerRefs: ["tasks:T104","goals:G6"]
- rootCause: "CONFIRMED as filed (both T104 reviewers source-confirmed). ps.txBytes.Add fires only at the two DATA/PARITY sites (Send multipath.go:1477, fecFlushDeadline :1603); PROBE emission (probe.go:44 emitProbes) and echo reflection (multipath.go ~1284) are uncounted, while rxBytes counts every inbound datagram — so a healthy idle standby reads path_up=1 with tx=0 while rx grows (the I8-motivating observation). A metrics-accounting fault, NOT a liveness hole. File-and-deferred to goal G10 (count probe/echo into txBytes or re-document the metric; flip T104's standby-transmits-when-idle subtest to green)."

### D51 — root-caused

- createdAt: 2026-07-14T03:53:40.693Z
- updatedAt: 2026-07-14T09:10:39.600Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: "Pre-existing e2e /metrics port collision: pacing_test.go and p3_fec_test.go both bind 127.0.0.1:9096"
- description: Surfaced during T101 round-2 review (port-inventory survey). Two -tags e2e test files declare the SAME metrics-listener port 9096 (pacing_test.go and p3_fec_test.go), breaking the per-file-unique-port convention. Latent under the current SEQUENTIAL netns runner (startProc cleanup waits for process exit), but becomes an active bind conflict under test shuffle/parallelism or a wedged teardown. Pre-existing (NOT introduced by T101, which was fixed to use 9101); out of scope for T101 so filed-and-deferred.
- severity: low
- suggestedFix: "Move one of the two (pacing_test.go or p3_fec_test.go) to an unused port and, ideally, centralize e2e metrics-port allocation (a shared registry or per-test ephemeral :0 bind) so the per-file-unique convention can't silently drift again."
- ledgerRefs: ["goals:G6"]
- rootCause: "CONFIRMED as filed (T101 round-2 port-inventory). Two -tags e2e files bind the same metrics port 127.0.0.1:9096 (pacing_test.go and p3_fec_test.go), breaking the per-file-unique-port convention — latent under the sequential netns runner, an active bind conflict under shuffle/parallelism. File-and-deferred to goal G11 (move one to an unused port; ideally centralize e2e metrics-port allocation)."

## M32

### D52 — root-caused

- createdAt: 2026-07-14T04:01:04.560Z
- updatedAt: 2026-07-14T09:10:23.050Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: reloadWarnings omits scheduler/fec/dns/bind non-path config sections (SIGHUP silently ignores them)
- description: "Filed from T109 round-1 review ([fable], file-and-defer per K13). internal/device/device.go reloadWarnings compares role/psk/wireguard/amnezia/log + path params/order, but Config also carries Scheduler, FEC, DNS, and Bind sections — a SIGHUP changing any of those is silently ignored with NO warning, contradicting Reload's documented invariant that every ignored non-path change must produce an explicit warning ('SILENCE is not acceptable'). Pre-existing at base 1fd915f (those fields were added after T30 without extending reloadWarnings). NOTE: the [bind] section here is T105's new BindMode; [dns] is G5's DNS block. Out of scope for T109 (which only adds the analogous warning for its own tun_persist field)."
- severity: medium
- suggestedFix: Extend reloadWarnings with reflect.DeepEqual comparisons for Scheduler, FEC, DNS, and Bind (mirroring the existing wireguard/amnezia/log cases) + unit-test cases; OR compare a struct copy with the path/metrics fields zeroed so the warning set is future-proof against new Config fields.
- ledgerRefs: ["tasks:T109","goals:G6"]
- rootCause: "CONFIRMED as filed ([fable] T109 review). internal/device/device.go reloadWarnings compares role/psk/wireguard/amnezia/log + path params but omits the Scheduler, FEC, DNS, and Bind sections — a SIGHUP changing any of those is silently ignored, contradicting Reload's documented 'SILENCE is not acceptable' invariant. File-and-deferred to goal G10 (extend reloadWarnings with DeepEqual over Scheduler/FEC/DNS/Bind, or a zeroed-copy comparison future-proof against new Config fields)."

### D54 — root-caused

- createdAt: 2026-07-14T04:23:35.572Z
- updatedAt: 2026-07-14T09:10:43.052Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: golangci-lint scans nested .claude/worktrees, leaking sibling agents' in-progress code into every lint run
- description: "Surfaced during T77 review ([fable]). `just lint` (golangci-lint) walks the nested implement-worker worktrees under .claude/worktrees/, so in-progress code from OTHER concurrent tasks fails the lint gate of every checkout, including main (observed: errcheck hits from a sibling agent's internal/dnsresolve/{doh,dot}.go leaking into an unrelated task's lint run). The lint gate is NON-HERMETIC with respect to concurrent agent worktrees — it makes `just lint` results depend on what other agents happen to be running, which is both noisy and non-reproducible. (Distinct from D45, which is the pre-existing real lint findings on the tracked tree.)"
- severity: medium
- suggestedFix: "Exclude the .claude directory from linting: set run.skip-dirs / issues.exclude-dirs to include `.claude` in .golangci.yml, OR lint an explicit package list in the justfile (`golangci-lint run ./cmd/... ./internal/... ./test/...`) instead of the implicit recursive walk."
- ledgerRefs: ["tasks:T77","goals:G6"]
- rootCause: "CONFIRMED as filed ([fable] T77 review). `just lint` (golangci-lint) walks the nested .claude/worktrees/, so in-progress code from OTHER concurrent implement-worker worktrees fails the lint gate of every checkout (non-hermetic, non-reproducible). Distinct from D45 (the real findings on the tracked tree). File-and-deferred to goal G11 (set run.skip-dirs/issues.exclude-dirs to include .claude in .golangci.yml, or lint an explicit package list)."

## M31

### D53 — root-caused

- createdAt: 2026-07-14T04:18:15.521Z
- updatedAt: 2026-07-14T09:10:27.605Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Device-bind fallback to source-IP pinning is silent (no WARN) in internal/bind
- description: "Both T106 reviewers ([opus]+[fable]) independently filed this. An operator who explicitly sets bind=\"device\" can silently end up source-IP-pinned — either an unresolvable interface in selectDeviceBinds/resolveForcedDeviceBind, OR a failed SO_BINDTODEVICE setsockopt in listenPath — losing the roam-survival property the mode was chosen for, with NO log signal. internal/bind holds no logger (NewMultipath, multipath.go:514, takes no logger param) and the PRE-EXISTING CAP/setsockopt fallback in listenPath is itself silent, so T106's 'matching the existing CAP fallback' is factually satisfied by silence. Fixing requires threading a logger through NewMultipath — broader than T106's selection-logic scope, so filed-and-deferred."
- severity: medium
- suggestedFix: Thread the repo's internal/log logger through NewMultipath into listenPath/planPathBinds/resolveForcedDeviceBind and WARN on every forced-device fallback to source-IP binding (both the unresolvable-interface and the setsockopt-failure layers; covers the pre-existing CAP fallback too), naming the path + interface.
- ledgerRefs: ["tasks:T106","goals:G6"]
- rootCause: CONFIRMED as filed (both T106 reviewers). An operator setting bind="device" can silently end up source-IP-pinned (unresolvable interface in selectDeviceBinds/resolveForcedDeviceBind, OR a failed SO_BINDTODEVICE setsockopt in listenPath) with NO log signal — internal/bind holds no logger (NewMultipath takes none) and the pre-existing CAP/setsockopt fallback is itself silent. File-and-deferred to goal G10 (thread internal/log through NewMultipath into listenPath/planPathBinds/resolveForcedDeviceBind and WARN on every forced-device→source fallback, naming path+interface).

### D55 — root-caused

- createdAt: 2026-07-14T06:07:16.662Z
- updatedAt: 2026-07-14T09:10:03.342Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: allowed_ips CIDR syntax is not validated at config load (fails late at daemon start)
- description: "Surfaced during T107 review ([fable]). config.validate() checks allowed_ips presence + role rules but NOT CIDR syntax: a malformed entry (e.g. '10.0.0.0/33' or a typo) passes config.Load and only fails at DAEMON START when the amneziawg engine's IpcSet rejects the rendered UAPI allowed_ip= line — a late, less-specific error instead of a fail-fast config error naming the offending peer + string. Pre-existing (T107's splitDefaultRoute comment notes 'allowed_ips carries no syntax validation upstream'); out of scope for T107, which only renders. Violates the repo's fail-fast-at-boundary (config load) discipline."
- severity: low
- suggestedFix: netip.ParsePrefix each allowed_ips entry in config.validate() and reject at Load time with the peer index + the offending string (mirroring how endpoint/source_addr are already parsed-and-validated at load).
- ledgerRefs: ["tasks:T107","goals:G6"]
- rootCause: "CONFIRMED as filed ([fable] T107 review). config.validate() checks allowed_ips presence + role rules but NOT CIDR syntax, so a malformed entry (10.0.0.0/33, a typo) passes Load and only fails at daemon start when the engine's IpcSet rejects the UAPI allowed_ip= line — a late, less-specific error, violating fail-fast-at-config-load. File-and-deferred to goal G9 (netip.ParsePrefix each entry in validate() with peer index + offending string)."

### D59 — root-caused

- createdAt: 2026-07-14T07:35:20.455Z
- updatedAt: 2026-07-14T09:10:06.349Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Config validation accepts multiple mode=default-route peers on the edge (overlapping /0)
- description: "Surfaced during T108 review ([fable]). internal/config/config.go validates only that Peer.Mode is empty-or-'default-route' and rejects mode on the concentrator; nothing rejects TWO OR MORE edge peers both marked mode=default-route with overlapping 0.0.0.0/0 allowed_ips. WireGuard cryptokey routing makes overlapping allowed_ips last-writer-wins at the engine, so such a config is silently misconfigured regardless of route programming — contrary to the project's fail-fast validation posture. Pre-existing (T107 config surface), not introduced by or fixable within T108 (which programs routes, not config validation)."
- severity: low
- suggestedFix: Reject >1 peer with mode=default-route (and/or overlapping /0 allowed_ips across peers) at config validation with a clear error.
- ledgerRefs: ["tasks:T108","goals:G6"]
- rootCause: "CONFIRMED as filed ([fable] T108 review). config validation accepts ≥2 edge peers both mode=default-route with overlapping 0.0.0.0/0 — WireGuard cryptokey routing makes overlapping allowed_ips last-writer-wins, a silent misconfig, against fail-fast. File-and-deferred to goal G9 (reject >1 default-route peer / overlapping /0 across peers)."

## M26

### D56 — root-caused

- createdAt: 2026-07-14T06:20:31.860Z
- updatedAt: 2026-07-14T09:10:47.067Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Superseded primary-only bind read seams (PathSnapshots/FECSnapshot) retained with duplicated FEC-stat derivation
- description: "Surfaced during T94 review ([fable]). After T94 migrated the device metrics adapter to Multipath.PeerSnapshots(), the older primary-only seams Multipath.PathSnapshots (internal/bind/multipath.go:2669) and Multipath.FECSnapshot (multipath.go:2053) have NO remaining production callers — only bind's own tests use them. Worse, PeerSnapshots COPY-PASTES FECSnapshot's honest Recovered/Unrecoverable 'delivered count' derivation (the code comment admits 'mirrors ... verbatim'), creating a two-copy DRIFT RISK on that non-trivial rule. Out of scope for T94 (removing them requires migrating ~9 bind test call sites in fec_test.go/traffic_test.go, an unrelated consolidation)."
- severity: low
- suggestedFix: "Either migrate bind's fec_test.go/traffic_test.go to PeerSnapshots and delete PathSnapshots/FECSnapshot, OR reimplement both as thin wrappers over PeerSnapshots()[0] so the honest-delivered-count FEC derivation lives in exactly one place."
- ledgerRefs: ["tasks:T94","goals:G4"]
- rootCause: "CONFIRMED as filed ([fable] T94 review). After T94 migrated the device metrics adapter to Multipath.PeerSnapshots(), the primary-only seams PathSnapshots (multipath.go:2669) + FECSnapshot (:2053) have no remaining production callers (only bind tests), and PeerSnapshots COPY-PASTES FECSnapshot's honest delivered-count derivation (a two-copy drift risk). File-and-deferred to goal G11 (migrate bind fec_test.go/traffic_test.go to PeerSnapshots and delete the seams, or make them thin wrappers over PeerSnapshots()[0])."

## M27

### D57 — root-caused

- createdAt: 2026-07-14T07:23:55.639Z
- updatedAt: 2026-07-14T09:10:51.146Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: "Stale doc-comments in internal/config/config.go: Peer.PSK/Name marked 'not yet consumed by any datapath code path'"
- description: "Surfaced during T98 review ([opus]). config.go Peer.PSK/Name doc-comments state 'No datapath code path consumes PSK yet; it is parsed, validated, and exposed only' and Name is 'Not yet consumed by any datapath code path'. As of the shipped G4 wiring these are FALSE: device.go calls cfg.PeerIdentities() which derives each peer's effective PSK from Peer.PSK, and internal/bind/multipath.go consumes those per-peer PSKs for peerBySource PROBE-authenticated demux; Peer.Name surfaces (for additional peers) as the metrics 'peer' label. Pre-existing groundwork remnant from T80/T81; out of scope for T98 (docs-sync task that edits AGENTS.md/README/docs/example, not config.go source comments)."
- severity: low
- suggestedFix: Update the Peer.PSK and Peer.Name doc-comments in internal/config/config.go to state they are now consumed via PeerIdentities() by device.Up and the Bind's peerBySource demux (PSK) and, for additional concentrator peers, the metrics peer label (Name).
- ledgerRefs: ["tasks:T98","goals:G4"]
- rootCause: "CONFIRMED as filed ([opus] T98 review). config.go Peer.PSK/Name doc-comments say 'not yet consumed by any datapath code path' — FALSE since T93: device.go PeerIdentities() derives each peer's effective PSK from Peer.PSK, multipath.go consumes them for the peerBySource PROBE-authenticated demux, and Peer.Name surfaces as the metrics 'peer' label for additional peers. File-and-deferred to goal G11 (update the comments to name the real consumers)."

### D58 — root-caused

- createdAt: 2026-07-14T07:24:03.514Z
- updatedAt: 2026-07-14T09:09:52.350Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Multi-peer concentrator drops the first configured peer's required name from metrics labels
- description: "Surfaced during T98 review ([fable]). Config validation REQUIRES a unique name per peer in multi-peer mode (internal/config/config.go, 'for metrics/logging clarity'), but device.Up never passes ids[0].Name to bind.NewMultipath, and Multipath.BoundPeerNames (internal/bind/multipath.go ~L2608-2622) hard-codes the embedded primary peer's name to \"\" — so on a two-edge concentrator, one edge's metrics series are labeled with its configured name while the primary edge's series carry peer=\"\", defeating the per-peer attribution the name requirement exists for. This is a KNOWN design property that T97's e2e asserts as current behavior (edge A → peer=\"\"), but it contradicts the config-level name requirement. Pre-existing T93/T94 wiring; NOT fixable within T98 (docs-sync). Distinct from [[D56]] (which concerns superseded PathSnapshots/FECSnapshot read seams, not the primary-name label)."
- severity: low
- suggestedFix: "Plumb ids[0].Name into NewMultipath (or set the primary peerState's name during concentrator wiring) so BoundPeerNames reports the configured name whenever >1 peer is bound, keeping \"\" only for the single-peer byte-compat exposition; update TestExpositionTwoPeerSeries and the T94 back-compat test accordingly. OR relax the config name-required rule for the primary peer and document peer=\"\" as its canonical label."
- ledgerRefs: ["tasks:T98","goals:G4","defects:D56"]
- rootCause: "CONFIRMED as filed ([fable] T98 review). Config requires a unique name per peer in multi-peer mode, but device.Up never passes ids[0].Name to bind.NewMultipath and BoundPeerNames (multipath.go ~L2608-2622) hard-codes the primary peer's name to \"\" — so the first edge's metrics series carry peer=\"\", defeating the per-peer attribution the name requirement exists for. File-and-deferred to goal G8 (plumb ids[0].Name into NewMultipath so BoundPeerNames reports it whenever >1 peer is bound; keep \"\" only for single-peer byte-compat)."

## M33

### D60 — root-caused

- createdAt: 2026-07-14T08:08:15.604Z
- updatedAt: 2026-07-14T09:10:55.053Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Stale 'config surface only' doc-comment on config.BindMode contradicts the shipped T106 wiring
- description: "Surfaced during T112 review ([fable]), re-confirmed + extended during T115 review ([fable]). TWO stale bind-mode doc-comments in internal/config/config.go claim the resolved bind mode is 'CONFIG SURFACE only' / 'not yet consumed': (a) the BindMode type comment (~L78-81): 'Wiring planPathBinds/selectDeviceBinds to consume the resolved mode is a later task — today every path is bound exactly as before, regardless of this field's value'; (b) the parallel Path.Bind field note (~L492-493). Both are FALSE since T106: internal/bind/pathsock.go selectDeviceBinds (~L115-136) switches on config.BindModeSource/Device/Auto and internal/bind/multipath.go (~L2309) AddPath honors a forced BindModeDevice. Groundwork-then-wire remnants, same class as [[D57]] (stale Peer.PSK/Name 'not yet consumed'). Out of scope for the docs-only tasks that surfaced it."
- severity: low
- suggestedFix: Delete the two 'later task / not yet consumed' sentences from BOTH the BindMode type comment (config.go ~L78-81) and the Path.Bind field note (config.go ~L492-493), leaving the now-accurate semantic description; selectDeviceBinds/multipath.AddPath honor the resolved mode since T106.
- ledgerRefs: ["tasks:T112","goals:G6","defects:D57"]
- rootCause: "CONFIRMED as filed ([fable] T112/T115 review). Two config.go comments — the BindMode type comment (~L78-81) and the Path.Bind field note (~L492-493) — claim the resolved bind mode is 'config surface only / not yet consumed', FALSE since T106: selectDeviceBinds (~L115-136) switches on the mode and multipath.AddPath (~L2309) honors a forced BindModeDevice. Same class as [[D57]]. File-and-deferred to goal G11 (delete the two stale sentences)."

### D61 — open

- createdAt: 2026-07-14T10:57:50.319Z
- updatedAt: 2026-07-14T10:57:50.319Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: "D54 root-cause mechanism does not reproduce: bare golangci-lint run never walked .claude/worktrees (dot-dir skipped); observed leak matched D45's own-tree findings"
- description: "Filed during T136 review ([fable], out-of-scope). Reviewer experiment in the T136 worktree: with a planted errcheck violation at .claude/worktrees/x/bad.go, the OLD bare `golangci-lint run` (current dev-shell toolchain) exits 0 with '0 issues' — Go package loading skips dot-directories, so the recorded D54 mechanism ('golangci-lint walks nested .claude/worktrees and leaks sibling code') is UNREPRODUCIBLE today. D54's observed evidence (errcheck hits at internal/dnsresolve/{doh,dot}.go) names exactly D45's pre-existing own-tree findings, whose relative paths in lint output are indistinguishable from a sibling-worktree leak — consistent with misattribution. NOTE: T136 (the D54 fix) already LANDED and its explicit-package-list Justfile change remains a sound BY-CONSTRUCTION hermeticity guarantee that meets its acceptance; this defect concerns only the accuracy of the D54 RECORD and the Justfile comment's 'which walks the repo root' clause, which assert an unverified mechanism."
- severity: low
- suggestedFix: Re-adjudicate D54's rootCause note against the original T77-era evidence (exact golangci-lint version + output paths at the time). If misattribution is confirmed, amend the D54 record and reword the Justfile lint-recipe comment to state the defense-in-depth rationale without the 'walks the repo root/.claude/worktrees' mechanism claim.
- ledgerRefs: ["tasks:T136","defects:D54"]

### D62 — open

- createdAt: 2026-07-14T10:57:57.911Z
- updatedAt: 2026-07-14T10:57:57.911Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Teardown-vs-bind race can install a dead-peer source binding that demuxInbound then permanently blackholes
- description: "Filed during T123 review ([fable], out-of-scope; PRE-EXISTING at base 735ece3). bindSourceToPeer (internal/bind/multipath.go) is lock-free by design and never rechecks peer liveness/views: a PROBE trial-decoded against a peer's view snapshot CONCURRENT with that peer's teardown re-installs a binding to the torn-down peerState AFTER unbindPeerSources completes (a lost CAS retries against the post-unbind map and installs anyway). demuxInbound then hits the ':1444 return' (bound peer holds no view → drop) for every subsequent frame from that AddrPort — the trial-decode/re-bind loop is unreachable for a bound key — so the AddrPort is blackholed until process restart and the stale entry consumes a global-cap slot forever (nothing reclaims it; a re-instantiated peer is a new pointer, so a repeat TearDownPeer does not match). T123 (AddrPort re-key) neither introduced nor widened it — the same structure exists at base with the netip.Addr key."
- severity: medium
- suggestedFix: "After a successful CAS in bindSourceToPeer, revalidate the peer is still wired (peersView membership or a peer.dead flag) and unbind on failure; OR make the ':1444 no-view' drop branch fall through to the trial-decode loop so an authenticated PROBE can re-point a stale binding."
- ledgerRefs: ["tasks:T123"]

### D63 — open

- createdAt: 2026-07-14T10:58:05.222Z
- updatedAt: 2026-07-14T10:58:05.222Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Per-peer demux eviction is first-bind order (FIFO), not LRU — a peer's ACTIVE binding can be evicted by its own churn
- description: "Filed during T123 review ([fable], out-of-scope refinement). sourceBinding.seq is stamped only when a key is inserted/re-pointed in bindSourceToPeer; bound sources short-circuit to handleInbound and NEVER refresh recency, so 'oldest' means first-bound, not least-recently-used. A peer with more concurrently-ACTIVE AddrPorts than its per-peer quota (many paths, or heavy CGNAT port churn alongside a stable path) evicts its own OLDEST — possibly actively-used — binding, blackholing that path's DATA until its next periodic PROBE re-binds it (which evicts the next-oldest: bounded rotating thrash). This CONFORMS to the pinned T123 plan decision (insertion order was explicitly sanctioned) and the default quota (1024/len(peers)) makes it unrealistic for sane deployments — so it is a design refinement for a separate task, not a T123 defect."
- severity: low
- suggestedFix: Refresh the binding's seq (recency) when an authenticated PROBE arrives from an already-bound AddrPort (handleInbound path), making eviction true LRU; OR floor the per-peer quota at the peer's configured path count so a peer's active paths are never self-evicted.
- ledgerRefs: ["tasks:T123","defects:D49"]

### D64 — open

- createdAt: 2026-07-14T11:51:23.954Z
- updatedAt: 2026-07-14T11:51:23.954Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: "Rebaseline()+ObserveRecovered: an FEC-recovered old-hub frame re-pins the unpinned release point, re-opening D32 when FEC is active"
- description: "Filed during T119 round-2 review ([fable], out-of-scope; PRE-EXISTING D32/T57-era, untouched by T119). Rebaseline() sets started=false (reseq.go:581), and ObserveRecovered's !started branch (reseq.go:246-249) then pins next to the FIRST recovered frame's seq — contradicting its OWN documented contract that a recovered frame 'NEVER moves or re-pins the release point' (reseq.go:232-240). After a hub failover, a parity-recovered OLD-hub HIGH-seq frame landing in the failover window re-pins next HIGH before the standby's low stream arrives; per Rebaseline's own doc the standby emits ~1 DATA frame per RekeyTimeout, so tryResync corroboration (3 distinct seqs) falls OUTSIDE the failover window and the D32 blackhole RE-OPENS for FEC-enabled deployments. Lives on the Rebaseline/FEC seam, NOT the new T119 RebaselineToLow path."
- severity: medium
- suggestedFix: Make ObserveRecovered refuse to anchor an unstarted ring (drop the recovered frame and return false when !started) so only a natively-received frame can pin the release point after any unpin; add a Rebaseline-then-ObserveRecovered regression test. Coordinate with T119's fix for the analogous RebaselineToLow-armed ObserveRecovered bypass (same seam).
- ledgerRefs: ["tasks:T119","defects:D36"]

### D66 — open

- createdAt: 2026-07-14T12:55:20.809Z
- updatedAt: 2026-07-14T12:55:20.809Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: "Stale single-peer-receive comment on AddPath's readLoop spawn (multipath.go:2548-2549)"
- description: "Filed during T124 review ([fable], out-of-scope; PRE-EXISTING on main, not touched by T124). internal/bind/multipath.go ~:2548-2549 claims 'single-peer receive; the concentrator's shared-socket demux to N peers is a later G4 task', but the multi-view source demux (demuxInbound, T88/T93) is SHIPPED and is exactly what T124's TestReconcilePromotionFansViewAndSchedulerToEveryPeer exercises; the new promoteDeferredLocked comment correctly states the opposite. The stale comment could steer a future change wrong."
- severity: low
- suggestedFix: "Reword to match promoteDeferredLocked's spawn-site comment: one reader per shared socket fed by the primary's view; demuxInbound resolves the owning peer per-datagram once the socket has >1 view."
- ledgerRefs: ["tasks:T124"]

### D67 — open

- createdAt: 2026-07-14T12:55:24.802Z
- updatedAt: 2026-07-14T12:55:24.802Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: attachSharedPathLocked rollback swallows detachPeerPathBoundLocked errors, leaving a stale peerPathState
- description: "Filed during T124 review ([fable], out-of-scope; PRE-EXISTING from the T123 AddPath fan-out — T124 only threads the probers param through). internal/bind/multipath.go ~:2580: on a mid-fan-out failure the rollback loop does `_ = m.detachPeerPathBoundLocked(...)`; if dyn.RemovePath fails inside detach, p.paths is NOT spliced, leaving a stale peerPathState (referencing a socket the caller then closes) in that peer's live path slice until the next Close→Open — a scheduler/paths coherence violation surviving an error path that claims 'a partial fan-out never leaks'."
- severity: low
- suggestedFix: Propagate or at least log the detach error; on detach failure, force-splice the tail peerPathState so p.paths never retains a view of a closed socket.
- ledgerRefs: ["tasks:T124","defects:D42"]

### D68 — open

- createdAt: 2026-07-14T13:15:12.210Z
- updatedAt: 2026-07-14T13:15:12.210Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Stale reseq.go Rebaselines-snapshot comment attributes the counter to hub failover only
- description: "Filed during T122 review ([fable], out-of-scope; PRE-EXISTING, introduced/left by T119). internal/reseq/reseq.go ~:760 comments the snapshot field as 'release-point re-baselines forced by a trusted control event (hub failover)', but since T119 the counter ALSO increments in RebaselineToLow (peer restart, reseq.go:692). Contradicts the metrics.go:295 help string which correctly covers both."
- severity: low
- suggestedFix: "Change the parenthetical to '(hub failover, peer restart)' or '(e.g. hub failover)' to match the metrics.go:295 help string."
- ledgerRefs: ["tasks:T122","defects:D36"]

### D69 — resolved

- createdAt: 2026-07-14T13:47:04.153Z
- updatedAt: 2026-07-14T14:33:35.867Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: TestMultipathFECDeadlineEmitsPartialGroupParity flakes ~2% under -race (counter read races async post-write increment)
- description: "Filed during T125 review ([opus], out-of-scope; PRE-EXISTING in fec_test.go, which T125 does not modify — T125's fan-out marginally raised the rate from 0% to ~2%). In internal/bind/multipath.go fecFlushDeadline, the parity counters (fs.parityFrames/parityBytes, ps.txBytes) are incremented AFTER WriteToUDPAddrPort inside the async fecTickLoop goroutine. TestMultipathFECDeadlineEmitsPartialGroupParity (fec_test.go:585) stops reading once it has received both parity wires and IMMEDIATELY asserts FECSnapshot().ParityFrames == parityShards, racing the tick goroutine's counter increment for the last wire. Reproduced ~3/140 (~2%) under `go test -race` combined runs; the race detector reports NO data race across ~380 runs — it is a test-synchronization defect, not a memory race. Makes the `go test -race ./internal/bind/...` gate non-deterministic. NOTE: T125 round 2 is instructed to also harden this test so the gate is deterministic post-merge; if that lands, resolve this on T125's merge."
- severity: medium
- suggestedFix: "Make the test synchronize against the async flush: poll FECSnapshot().ParityFrames with a short bounded retry (until it reaches parityShards or a ~200ms deadline) instead of reading it once immediately after the last wire arrives."
- ledgerRefs: ["tasks:T125","defects:D44"]
- rootCause: "Test-synchronization defect: TestMultipathFECDeadlineEmitsPartialGroupParity (fec_test.go) read FECSnapshot().ParityFrames immediately after receiving both parity wires, racing the async fecTickLoop goroutine's post-WriteToUDPAddrPort counter increment (~2% flake under -race; NO memory race). RESOLVED by T125 round 2 (landed 3eab82e): the test now polls ParityFrames with a bounded 200ms retry then asserts strict equality — masks neither under- nor over-count. Verified via 50x/100x -race runs (0 failures)."

### D70 — open

- createdAt: 2026-07-14T15:29:16.572Z
- updatedAt: 2026-07-14T15:29:16.572Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Same-name path link_bandwidth/link_rtt changes are silently accepted on reload (D52 gap at path-sub-field level)
- description: "Filed by BOTH T135 reviewers (opus+fable) independently as out-of-scope-for-T135. reloadWarnings' same-name-path comparison (internal/device/device.go ~:648-659) checks only SourceAddr/DestAddr/Bind for a surviving same-name path. Path.LinkBandwidthBitsPerSec and Path.LinkRTT (operator-declared BDP pace-sizing params, internal/config/config.go Path struct ~:496-514, inputs to the weighted scheduler's pacing) are (a) NOT applied on reload — Reload applies only path-membership add/remove; runningConfig keeps survivors' ORIGINAL params until restart; (b) NOT warned by the per-path comparison; and (c) NOT caught by the D52 future-proof catch-all — it zeroes Paths entirely (lc.Paths, dc.Paths = nil). So an operator who changes a path's link_bandwidth/link_rtt and SIGHUPs gets NO warning while the running weighted-scheduler pace keeps the booted value — the exact 'SILENCE is not acceptable' violation D52 targets, at the path-sub-field level. Pre-existing (those fields were never compared) and outside T135's stated scope (Scheduler/FEC/DNS/Bind top-level sections + per-path Bind), hence filed rather than blocking T135's go-ahead."
- severity: medium
- suggestedFix: Extend the same-name-path comparison to warn on l.LinkBandwidthBitsPerSec/l.LinkRTT != d's (mirroring the source/dest/bind checks with actionable messages), OR generalize the per-path comparison to a whole-struct reflect.DeepEqual per name-matched pair with the already-individually-warned/applied fields (Name) zeroed — so any future Path field is covered symmetrically to the top-level catch-all. Add table cases asserting exactly one warning each.
- ledgerRefs: ["tasks:T135","defects:D52"]

### D71 — open

- createdAt: 2026-07-14T15:30:45.086Z
- updatedAt: 2026-07-14T15:30:45.086Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: reconcileDeferred silently swallows promoteDeferredLocked's error
- description: "Filed by the T134-r2 fable reviewer as pre-existing/out-of-scope. internal/bind/reconcile.go: on promoteDeferredLocked failure the error is discarded (`_ = c.Close(); kept = append(kept, dp)`) with no log, so a wiring-defect/attach failure leaves the path permanently deferred with ZERO diagnostic signal. Pre-existing at base a768452 (the bind held no logger then); out of scope for T134's D53 fallback-warning surface, but now that Multipath holds a component-scoped logger (added by T134) the swallow is fixable. Note: T134 round 3's criticism #1 fix (moving the fallback warns to AFTER promoteDeferredLocked succeeds) is adjacent but distinct — that stops the FALSE success claim; this defect is about the MISSING failure diagnostic."
- severity: medium
- suggestedFix: Log the promotion error at WARN/ERROR in reconcileDeferred's promote-failure branch, deduplicated per deferral window like warnedUnresolvable to avoid 1 Hz spam.
- ledgerRefs: ["tasks:T134","defects:D53"]

### D72 — open

- createdAt: 2026-07-14T16:26:20.289Z
- updatedAt: 2026-07-14T16:26:20.289Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: WeightedScheduler.SetPaths silently collapses the aggregation gate without the canonical log record
- description: "Filed by the T143 fable reviewer (out-of-scope for T143). internal/sched/weighted.go SetPaths (T30-era, ~:659) sets s.aggregating=false with NO 'scheduler aggregation change' record, so on every Bind reopen (Close->Open durable-membership swap) a log consumer reconstructing gate state sees two consecutive to='aggregating' records with no intervening collapse. Pre-existing behavior untouched by T143's diff. Low severity."
- severity: low
- suggestedFix: In SetPaths, when s.aggregating was true before the reset, emit the canonical 'scheduler aggregation change' record (to=collapsed, from=aggregating, reason='paths replaced', plus the threshold fields from T143) under the already-held s.mu.
- ledgerRefs: ["tasks:T143","goals:G13"]

### D73 — resolved

- createdAt: 2026-07-14T16:26:28.223Z
- updatedAt: 2026-07-14T16:39:14.424Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: test/e2e/load.go (non-test file) references _test.go-only symbols, so `go build -tags e2e ./test/e2e/...` fails
- description: "Filed independently by BOTH T143 reviewers (opus noted, fable filed); pre-existing from T141 (present on base 6e26127, now on main via b0f52e4). test/e2e/load.go is a NON-_test.go file carrying the e2e build tag but references proc, lockedBuffer, and nsEnvMarker which are defined only in *_test.go files. The package therefore compiles under `go test` / `go vet -tags e2e` / golangci (all of which include test files) but `go build -tags e2e ./test/e2e/...` errors with undefined symbols. Latent: nothing in the gate does a plain `go build -tags e2e` (the gate uses `go test -tags e2e -count=0` which compiles via the test binary), so it went uncaught, but any future tooling that plain-builds the tag reddens. In Go, a non-test file cannot legally reference _test.go symbols outside the test binary — load.go is mis-structured."
- severity: low
- suggestedFix: Rename test/e2e/load.go to test/e2e/load_test.go (it is test-support code, belongs in the test binary), OR move proc/lockedBuffer/nsEnvMarker into a non-test e2e-tagged support file. Then add `go build -tags e2e ./test/e2e/...` to the gate so it cannot regress.
- ledgerRefs: ["tasks:T141","tasks:T143","goals:G13"]
- rootCause: "test/e2e/load.go (added by T141) was a NON-_test.go file carrying the e2e build tag but referencing proc/lockedBuffer/nsEnvMarker which are defined only in *_test.go files; in Go a non-test file cannot see _test.go symbols outside the test binary, so `go build -tags e2e ./test/e2e/...` failed with 3 undefined symbols (uncaught because the gate used `go test`/`go vet -tags e2e`/golangci, all of which include test files). RESOLVED inline by orchestrator (commit c5335a0): `git mv test/e2e/load.go test/e2e/load_test.go` — load helpers are test-support consumed only by e2e _test.go files, so moving load.go into the test binary makes the symbols resolve while DriveUDPLoad/MetricsSampler/ParseLogLines stay visible to the e2e tests. Verified: `go build -tags e2e ./test/e2e/...` now clean; e2e compiles; just lint 0 issues default+e2e+realhosts. This also unblocked T128 whose acceptance requires `go build -tags e2e` clean."

### D74 — open

- createdAt: 2026-07-14T16:35:30.090Z
- updatedAt: 2026-07-14T16:35:30.090Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Reload silently staleing the wanbond_weighted_capacity_sane gauge (membership add of an undeclared path leaves gauge=1)
- description: "Filed by the T144 fable reviewer (out-of-scope for T144, which pins a STATIC startup gauge). Two coupled reload gaps: (1) a SIGHUP that ADDS a path WITHOUT link_bandwidth under weighted policy is applied to membership but neither re-warns nor recomputes wanbond_weighted_capacity_sane — the gauge can read 1 while the running path set is no longer capacity-verifiable; (2) [overlaps D70] reloadWarnings' same-name path comparison covers only SourceAddr/DestAddr/Bind, so a SIGHUP changing an existing path's link_bandwidth/link_rtt is silently accepted, violating the D52 'SILENCE is not acceptable' invariant. See [[D70]] for the per-path link_bw/rtt reload-warning half; THIS defect adds the gauge-recompute-on-reload angle specific to T144's static gauge."
- severity: medium
- suggestedFix: Recompute the capacity-sanity verdict in runningConfig()/reloadTunnel and re-set the gauge (and emit a WARN) whenever the live vs desired WeightedCapacitySane differ; extend reloadWarnings' same-name comparison to LinkBandwidthBitsPerSec/LinkRTT (coordinate with D70's fix to avoid double-work).
- ledgerRefs: ["tasks:T144","defects:D70","defects:D52","goals:G13"]

### D75 — open

- createdAt: 2026-07-14T16:41:27.769Z
- updatedAt: 2026-07-14T16:41:27.769Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Idle-gap collapse 'scheduler aggregation change' record's structured fields have no test assertion
- description: "Filed by the T143-r2 fable reviewer (out-of-scope; pre-existing coverage gap). No test asserts the 'scheduler aggregation change' record emitted by the IDLE-GAP collapse branch (internal/sched/weighted.go ~:535-542). TestWeightedCollapsesAfterOverloadIdle exercises the branch behaviorally (path distribution + gate state) but never inspects the log record, and TestAggregationGateLog deliberately avoids the branch (its widened dwell forces the sustained-low-load branch). So the load_fps-uniformity invariant T143 added + documented in docs/design.md is enforced by tests on only 2 of the 3 record sites (engage + dwell-collapse); the idle-gap record's fields (reason='idle gap', gap, and the newly-added load_fps) are untested. Low severity — the field is present and manually verified, just not regression-locked."
- severity: low
- suggestedFix: Extend TestWeightedCollapsesAfterOverloadIdle (or the log-fields unit test) with the existing capturing-logger infra to assert the idle-gap collapse record carries reason='idle gap', gap, load_fps, from, and both threshold fields — locking the schema-uniformity invariant on all three record sites.
- ledgerRefs: ["tasks:T143","goals:G13"]

## M49

### D65 — root-caused

- createdAt: 2026-07-14T12:11:27.104Z
- updatedAt: 2026-07-14T13:17:40.979Z
- author: "opus-4.8[1m]"
- session: 7295f080-20fa-4cf9-afac-0357b4cf65cb
- headline: Tunnel single-flow throughput plateaus at ~3.67 Mbps with loss/reorder-limited TCP and ~1s bufferbloat under load
- severity: high
- description: |
    Root-cause the tunnel's low single-flow throughput and its loss/bufferbloat under load.
    
    **Observations (Pi 4 edge, Starlink active path ~40 ms RTT, o3 concentrator):**
    - Single-stream **TCP** through the tunnel plateaus at **~3.67 Mbps**, with `cwnd` stuck at ~30 KB and **13 retransmits / 10 s** — i.e. loss/reordering-limited, not a clean rate cap.
    - **UDP** offered at 8 Mbps delivers ~6.9 Mbps at **13% datagram loss** with the queue building to **~1 s loaded RTT** (idle ~40 ms).
    - Plain WireGuard on aarch64 normally does far more than 3.67 Mbps, so the ceiling and the retransmit pattern warrant explanation.
    
    **Investigate — isolate the ceiling's source and any fixable inefficiency:**
    1. Separate the three candidate limiters with measurements: (a) direct iperf3 over the same WAN (no tunnel) = WAN ceiling; (b) tunnel over the WAN = end-to-end; (c) a loopback / netns tunnel (no WAN) = pure CPU/codec ceiling. Attribute the gap.
    2. If CPU-bound: profile the send path — WG crypto vs reed-solomon FEC encode vs the bonding DATA-frame codec (`internal/bind/`) vs the userspace TUN read/write copy. Look for per-frame allocation, absence of send batching / GSO, single-goroutine serialization, or an XChaCha20/FEC hot loop.
    3. If loss/reorder-bound: is the loss introduced by the tunnel (framing/FEC/scheduler) or by the WAN? Check MSS clamping on `wanbond0` (inner MTU 1400 → is TCP fragmenting/PMTU black-holing?), and whether active-backup ever briefly reorders at path selection.
    
    Deliverable: a profile + the dominant cost, whether ~3.67 Mbps is the true CPU/WAN ceiling or an inefficiency, and a concrete fix candidate (batching, buffer reuse, MSS clamp) with the expected gain.
- rootCause: |
    The single-flow throughput collapse is LOSS-INDUCED, not a CPU or raw-WAN rate cap, and the loss originates in the EXTERNAL Starlink last-mile buffer that wanbond does nothing to shape.
    
    (1) The WAN demonstrably carries ≥6.9 Mbps: UDP offered at 8 Mbps delivered 6.9 through the same tunnel/path while single-flow TCP got only 3.67, so TCP's shortfall is loss-induced cwnd collapse (cwnd stuck ~30KB over ~40ms RTT), not a rate cap [D65 measurements].
    (2) The tunnel's own processing is not the ceiling: measured DATA-codec encode = 4610 ns/op ≈ 2429 Mbps/core on x86_64 (pprof: chacha20-dominated, generic non-SIMD path), ~160-300 Mbps/core extrapolated to Pi4 aarch64 — 40-80x above 3.67 Mbps; and 3.67 Mbps ≈ 300 pps is trivially within the syscall/lock budget [internal/frame inline benchmark].
    (3) There is no wanbond-internal queue: Send writes each frame synchronously to the UDP socket [internal/bind/multipath.go:2027-2035], the pacer sheds at the head and is BDP-bounded [internal/config/config.go:218-223], and the resequencer is bounded in memory+latency [internal/reseq/reseq.go:90-96] — so the observed ~1s loaded RTT is the EXTERNAL Starlink buffer, and its build-to-~1s-then-~13%-drop is the classic buffer-overflow signature of an unshaped sender (random medium loss would not grow the queue).
    (4) Under the DEFAULT active-backup scheduler wanbond applies NO egress pacing/AQM (pacing is a weighted-only feature) [internal/config/config.go:99-108], so it offers packets to the bloated last-mile unshaped and cannot prevent the overflow. This is the primary controllable root cause.
    (5) Compounding, for FORWARDED (behind-tunnel) TCP no MSS clamp is installed anywhere — the daemon installs none [internal/device/device.go:52-55] and the deploy recipes omit it even for the client-LAN forwarding case [docs/p1-mtu.md:76-99, docs/install.md, wanbond-fixes.md] — so forwarded segments can fragment/PMTUD-blackhole (Pi-originated TCP is already MSS-bounded by the TUN route MTU; only forwarded flows are affected).
    
    Candidates H-B (scheduler reorder), H-C (CPU-bound encode), H-D (internal oversized queue), H-E (FEC overhead) were each ruled out with validated evidence (H5/H6/H7/H8 = wrong). The user reviewed this diagnosis (Q56) and authorized implementing the fixes directly, waiving prior field-measurement confirmation; exact quantified gain is deferred to on-hardware validation.
- suggestedFix: |
    Two independent fixes (user authorized direct implementation via Q56):
    (1) PRIMARY — add BDP-sized egress send-pacing (optionally a CoDel-style AQM drop) to the DEFAULT active-backup scheduler, reusing the existing weighted-scheduler pacer + BDP-sizing machinery (internal/sched/weighted.go token bucket + internal/config SizePacingFromBDP / BurstFrames), so a single uplink is shaped to its drain rate and cannot self-inflict the ~1s Starlink bufferbloat. Expected: eliminate the standing queue and restore single-flow TCP toward the ≥6.9 Mbps the WAN demonstrably carries. (Design choice for plan-flow: wire pacing into active-backup directly vs expose a pacing knob usable independent of policy=weighted.)
    (2) SECONDARY — install a TCP MSS clamp on wanbond0 for forwarded traffic (iptables/ip6tables mangle FORWARD -o wanbond0 -p tcp --tcp-flags SYN,RST SYN -j TCPMSS --clamp-mss-to-pmtu) and document it in the deploy recipes (docs/install.md §9.2, wanbond-fixes.md), so behind-tunnel TCP cannot emit oversize segments that fragment/PMTUD-blackhole. Decide in plan-flow whether the daemon should install this itself for peers it forwards vs document it as an operator step.
    
    Separately worth noting (NOT the D65 ceiling, low priority): the send path has un-taken optimizations — unused dst-reuse seam in frame.Codec.Encode, no GSO/sendmmsg batching (multipathBatchSize=1), generic (non-SIMD) chacha20 — which matter only at far higher rates.
    
    On-hardware validation (three-way iperf3 + loaded-RTT A/B on Pi4/Starlink/o3) is deferred to verify/implement.
- sessionLogs: [".cq/logs/20260714-122159-ac772045578314808.md",".cq/logs/20260714-122159-aa7a4cc064596f222.md",".cq/logs/20260714-122159-ae52507d7fc1a55b7.md",".cq/logs/20260714-122159-a7875c1b02b7ec340.md",".cq/logs/20260714-122159-a971f55e45232ce3d.md",".cq/logs/20260714-123426-H6-inline-probe.md"]
- rawLogs: [".cq/logs/raw/20260714-122159-ac772045578314808.jsonl",".cq/logs/raw/20260714-122159-aa7a4cc064596f222.jsonl",".cq/logs/raw/20260714-122159-ae52507d7fc1a55b7.jsonl",".cq/logs/raw/20260714-122159-a7875c1b02b7ec340.jsonl",".cq/logs/raw/20260714-122159-a971f55e45232ce3d.jsonl"]
- dependsOn: ["T149","T150","T151","T152","T153","T154","T155","T156","T157","T158","T159"]
