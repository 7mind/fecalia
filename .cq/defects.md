---
ledger: defects
counters:
  milestone: 0
  item: 33
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

### D32 — wip

- createdAt: 2026-07-13T16:18:50.224Z
- updatedAt: 2026-07-13T16:20:36.940Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: "Hub failover: switch decision + outer PROBE liveness recover to standby hub#2, but inner WG data traffic does NOT resume via the standby within the window (hardware e2e)"
- severity: high
- description: "Found by T62 hardware e2e on llm-ubuntu-0 (amd64), commit 5ffc411 (main + T62 test; T57 hub-failover feature merged). TestHubFailoverStandbySwitch FAILS deterministically 3/3 clean-setup runs at hub_failover_test.go:150 ('tunnel did not resume via standby hub#2 within 10.2s of the hub#1 outage'). The observed sequence after hub#1 is killed (its bridge port down = L2 blackhole): (1) edge detects all-paths-down (path liveness up->down, scheduler no eligible path); (2) edge LOGS the 'hub failover:' WARN naming hub#2 (from_index:0 to_index:1 to_endpoint:'10.100.0.3:51820' endpoints:2) — the T57 controller advances + repoints as designed; (3) OUTER path liveness RECOVERS to UP on hub#2 ~2s after the kill (path liveness uplink down->up; scheduler active path change reason:failover) and stays up, no flapping; (4) YET base.pingUntil(concInner) — inner tunnel traffic, which can ONLY be carried by hub#2 since hub#1 is blackholed — NEVER succeeds for the remaining ~8s of the 10.2s window. So the outer PROBE/liveness plane repoints to hub#2 and the T57 re-handshake fires, but INNER encrypted WG data does not traverse to hub#2. HUB_FAILOVER_RESUME_MS never emitted (success-path-only). The single-endpoint guard PASSES (no spurious switch). Test topology is correctly configured (verified statically): both hubs run full concentrator daemons with the IDENTICAL WG private key and the edge as an allowed peer (allowed_ips=concInner); edge peer has endpoints=[hub1,hub2], public_key=hubPub. So this is NOT a hub-misconfiguration; the defect is in the DATA PLANE after SetPeerRemote/ExpireCurrentKeypairs+SendHandshakeInitiation. Hypothesis to confirm on hardware: the WG handshake to hub#2 does not COMPLETE (hub#2 never establishes the session) OR completes but data does not forward — hub daemon logs are suppressed (config level='error') so this needs an instrumented hardware re-run (hub#2 at level info/debug + tcpdump/counters) to determine whether hub#2 receives+completes the edge's re-handshake. This validates that T57's unit tests (mocked liveness + recordingRemote) proved the CONTROLLER LOGIC but not the actual end-to-end data re-establishment; the e2e caught the gap. Q18 (multi-concentrator hub failover) is IN-SCOPE for the pilot, so this must work."
- ledgerRefs: ["tasks:T57","tasks:T62","goals:G2"]

### D33 — wip

- createdAt: 2026-07-13T16:19:04.477Z
- updatedAt: 2026-07-13T16:27:41.664Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: "T62 hub-failover fixture has an intermittent netns setup race: `ip link set wbHfcN netns <pid>` vs immediate `nsenter ... ip addr add` -> 'Cannot find device wbHfcN' (~50% of setups)"
- severity: medium
- description: "Found by T62 hardware e2e on llm-ubuntu-0. startHubHolder/setupHubFailover (test/e2e/hub_failover_test.go ~lines 127/188/362) fails roughly half of all test SETUPS (both TestHubFailoverStandbySwitch and TestHubFailoverSingleEndpointGuard, both hub veths) with: 'nsenter -t <pid> -n ip addr add <ip>/24 dev wbHfcN: exit status 1 / Cannot find device \"wbHfcN\"'. Root cause: a race between `ip link set wbHfcN netns <pid>` (moving the veth end into the hub netns) and the IMMEDIATELY-following `nsenter ... ip addr add` on that device — the device move into the target netns is not yet visible when the addr-add runs. Verified NOT cross-run contamination: no leftover netns/holders/links persist after runs. This aborts the setup before any failover logic runs, so it does not corrupt the D32 verdict (that was obtained from clean-setup runs after retrying past the race), but the fixture is flaky and must be hardened (e.g. poll/wait for the device to appear in the target netns before addr-add, mirroring waitForNetns / waitLink patterns) for the test to be reliable/CI-usable. This is a TEST-infrastructure defect, distinct from the D32 data-plane defect."
- ledgerRefs: ["tasks:T62","goals:G2"]
- suggestedFix: "FIX WRITTEN at 79c5ca4 (on the T62 branch worktree-agent-a2f01fe7cbbab5049): after `ip link set hubCeth netns <pid>`, poll (bounded 3s, 20ms interval) `nsenter -t <pid> -n ip link show hubCeth` until the moved veth end is visible in the hub netns BEFORE the addr-add. Compiles under -tags e2e, gofmt-clean. To be CONFIRMED on hardware in the combined re-validation once D32 is fixed (repeated runs must show zero 'Cannot find device' setup failures). Kept on the T62 branch to ride along when T62 merges."
