---
ledger: defects
counters:
  milestone: 0
  item: 5
archives: []
---

# defects

## M4

### D1 — root-caused

- createdAt: 2026-07-06T20:02:54.250Z
- updatedAt: 2026-07-06T20:06:52.806Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: Partial amnezia config emits zeroed h1..h4 and would silently misconfigure obfuscation
- description: "Filed by the T8 implement-review panel (opus), file-and-defer. internal/device/device.go writeAmnezia() emits ALL nine amnezia UAPI keys whenever ANY single field is non-zero. config.validate()/Amnezia.validate() enforce only the jmin<=jmax ordering, not an all-or-nothing / non-zero-magic-header invariant. So a partial amnezia block (e.g. jc/jmin/jmax set but s1/s2/h1..h4 left at 0) produces a configured-but-inconsistent obfuscation profile: the two ends can silently disagree on junk vs magic-header settings. P0 runs plain WireGuard (amnezia all-zero, unexercised), so this is latent. Belongs with the T19 amnezia end-to-end wiring. Severity low."
- severity: low
- suggestedFix: "In T19, add an Amnezia validation rule: when the block is configured, require the full obfuscation set to be internally consistent (and default magic headers to 1..4 when omitted rather than 0), so a partial config fails fast at load."
- ledgerRefs: ["tasks:T8","goals:G1","tasks:T19"]
- rootCause: "Confirmed against source (in-tree, no explorer needed). internal/device/device.go writeAmnezia() gates on `configured := any-field-non-zero` and then emits ALL nine keys (jc/jmin/jmax/s1/s2/h1..h4), so a partial block emits zeros for the unset fields. internal/config/config.go Amnezia.validate() checks ONLY `0 <= jmin <= jmax` — no all-or-nothing / non-zero-magic-header invariant. Net: a partial amnezia config is accepted and emitted inconsistently. Unexercised at P0 (amnezia all-zero → writeAmnezia early-returns → plain WireGuard). Fix folded into task T19 (amnezia end-to-end); D1 ledgerRefs tasks:T19 so it auto-resolves on T19 merge-back per implement §7.4."

### D2 — root-caused

- createdAt: 2026-07-06T20:03:00.651Z
- updatedAt: 2026-07-06T20:06:57.704Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: amneziawg-go stores amnezia message-type magic headers in package-level globals
- description: "Filed by the T8 implement-review panel (fable), file-and-defer. amneziawg-go v1.0.4 device.handlePostConfig assigns MessageInitiationType/MessageResponseType/MessageCookieReplyType/MessageTransportType — PACKAGE-LEVEL vars (noise-protocol.go:62-67) — on every configured IpcSet apply. Two Device instances in one process therefore cannot carry different obfuscation magic headers; the last apply wins process-wide. wanbond currently runs exactly one engine per process (one binary per role), so this is unexercised, but it constrains any future in-process multi-device usage (e.g. an in-process edge+concentrator test with distinct amnezia configs would silently share header types). Upstream dependency property, not fixable inside T8. Relevant to T19. Severity low."
- severity: low
- suggestedFix: "Before/at T19: document + assert the single-engine-per-process invariant in internal/device, or evaluate vendoring/patching the fork to move the message-type magic headers into per-Device state."
- ledgerRefs: ["tasks:T8","goals:G1","tasks:T19"]
- rootCause: "Confirmed against vendored engine source (I read amneziawg-go@v1.0.4 device/device.go handlePostConfig, lines 585-720). The message-type magic headers MessageInitiationType/MessageResponseType/MessageCookieReplyType/MessageTransportType are package-level vars (noise-protocol.go) reassigned by handlePostConfig on every configured IpcSet apply (device.go:685/688 etc.), and resetProtocol() (device.go:585) also mutates them process-wide. So obfuscation magic headers are process-global, not per-Device: two engines in one process share the last-applied values. Upstream dependency property; not fixable in T8. Mitigation (document/assert single-engine-per-process, or vendor-patch to per-Device state) folded into T19; D2 ledgerRefs tasks:T19 so it auto-resolves on T19 merge-back per implement §7.4."

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

### D5 — root-caused

- createdAt: 2026-07-06T21:10:30.046Z
- updatedAt: 2026-07-06T21:32:47.546Z
- author: "opus-4.8[1m]"
- session: 45fdce95-2af6-42cd-8ddd-0c9faabc56ef
- headline: frame codec re-derives HKDF subkeys and double-inits ChaCha20 per call (per-datagram hot path)
- description: Filed by the T11 review panel (fable), file-and-defer. internal/frame Encode/Decode call subkeys(psk) (two HKDF-SHA256 derivations) on EVERY invocation, and Decode constructs two XChaCha20 cipher instances per frame (peek kind byte + full-body obfuscate), plus per-frame allocations. Correct but wasteful in the per-datagram path of a WAN bonder (~microsecond-scale key-derivation per packet per direction; double-digit % of a core at 100k pps). Out of scope for T11 (codec correctness); the internal API is free to change when T12 consumes it. Severity medium.
- severity: medium
- suggestedFix: "At T12 integration, introduce a codec state built once from the PSK (e.g. type Codec struct{obfKey, authKey []byte} with NewCodec(psk) + methods): derive subkeys once, reuse one cipher/keystream per Decode (de-obfuscate kind byte and body from a single keystream), and consider a dst-append buffer-reuse API once T12 defines the datapath throughput target."
- ledgerRefs: ["tasks:T11","goals:G1","tasks:T12"]
- rootCause: "Confirmed by the T11 review (source-cited): internal/frame Encode/Decode call subkeys(psk) (two HKDF-SHA256 derivations) per invocation and Decode double-inits XChaCha20 (peek + full-body) per frame + per-frame allocations — wasteful in the per-datagram hot path. Correct output, but not built for the datapath. Fix deferred to T12 (where the codec is first wired into the datapath): introduce a Codec state built once from the PSK (NewCodec(psk), derive subkeys once, single keystream per Decode, dst-append buffer reuse). D5 ledgerRefs tasks:T12 so it auto-resolves on T12 merge-back. Reinforced by this session's real-host finding that the pass-through path is efficiency-sensitive (though not the current bottleneck)."
