---
ledger: hypothesis
counters:
  milestone: 0
  item: 3
archives: []
---

# hypothesis

## M28

### H1 — uncertain

- createdAt: 2026-07-14T08:49:21.616Z
- updatedAt: 2026-07-14T08:57:10.561Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: 0.0.0.0/0 wedge is an amneziawg-go allowed-ip trie zero-length-prefix defect; T107 splitDefaultRoute now shields the config path but a raw /0 to the engine still wedges
- description: "TRUE iff: (a) the receiving side never answers a handshake when the initiating peer carries a literal 0.0.0.0/0 allowed-ip (engine trie/lookup mishandles the zero-length prefix), AND (b) wanbond's shipped T107 splitDefaultRoute (internal/device/device.go uapiConfig) now UNCONDITIONALLY rewrites a config-literal /0 to the /1+/1 pair before the UAPI render, so the config surface no longer reaches the engine with a raw /0 — leaving the residual root cause purely upstream in amneziawg-go's allowed-ips trie for the zero-length prefix."
- ledgerRefs: ["defects:D35"]
- evidence: ["[correct] device.go:1071-1080 — splitDefaultRoute rewrites a literal 0.0.0.0/0 or ::/0 into the /1+/1 pair; any non-/0 passes through. The T107 config-path shield. VALIDATED.","[correct] device.go:1052-1060 — uapiConfig applies splitDefaultRoute UNCONDITIONALLY to every peer's every allowed_ip before the UAPI render (regardless of Mode), so the engine never receives a config-literal /0. Shield confirmed.","[correct] amneziawg-go@v1.0.4 device/allowedips.go:101-114 (insert) — cidr==0 yields a valid root node (bitAtByte=0, bitAtShift=7, maskSelf zeroes bits); no error/panic. RULES OUT the stated insert-defect.","[correct] amneziawg-go@v1.0.4 device/allowedips.go:191-205 (lookup) — a /0 root matches ALL addresses (commonBits>=0); a /0 ALLOWS all, does not wedge. RULES OUT the stated lookup-defect.","[correct] amneziawg-go@v1.0.4 device/receive.go:400-417 — the handshake-response path calls SendHandshakeResponse UNCONDITIONALLY after ConsumeMessageInitiation; the allowedips trie is NEVER consulted here. DECISIVELY RULES OUT the stated 'trie zero-length defect suppresses the handshake response' mechanism.","[correct] amneziawg-go@v1.0.4 device/receive.go:521-543 — the only allowedips.Lookup call sites are on the decrypted inner-packet source (post-handshake DATA path), where a /0 allows all sources. The stated locus is wrong.","[correct] test/e2e/default_route_test.go:65-93 — the only shipped e2e using 0.0.0.0/0 asserts ROUTE WIRING only (no concentrator, no handshake), so NO shipped test reproduces the D35 wedge or proves the split causally fixes the handshake."]
- sessionLogs: [".cq/logs/20260714-085530-a300a1198741fda4e.md"]
- rawLogs: [".cq/logs/raw/20260714-085530-a300a1198741fda4e.jsonl"]

### H2 — confirmed

- createdAt: 2026-07-14T08:49:27.070Z
- updatedAt: 2026-07-14T09:00:36.325Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: "One-sided-restart outage: the surviving peer's established WG keypair is not superseded by a fresh initiation until the rekey timer; wanbond's startup handshake fires pre-liveness without aggressive retry"
- description: "TRUE iff: (a) when one end restarts, the surviving end holding the old session does not treat a fresh handshake initiation from the known static key as immediately superseding (so the stale keypair persists for minutes until RekeyAfterTime/RejectAfterTime), AND/OR (b) wanbond's bind/virtual-endpoint layer delays or drops the restarted side's initiation retransmits, AND (c) the restarted side's first init fires before path liveness and is not re-driven off the first path-up edge. Distinct from the already-resolved OUTER-path D12 (T38 responder-challenge session epoch)."
- ledgerRefs: ["defects:D36"]
- evidence: ["[correct] amneziawg-go@v1.0.4 device/receive.go:399-417 + noise-protocol.go:330-340 — the surviving responder consumes+responds to a fresh init IMMEDIATELY (SendHandshakeResponse), gated only by a monotonic tai64n timestamp + a 20ms flood window. RULES OUT H2(a) (no WG-layer supersede failure) IF the init reaches the engine.","[correct] internal/reseq/reseq.go:285-297 — admit(): a frame more than one window below `next` is treated as SUSPECT ('a peer restart resets outerSeq to 1, so its frames land here') and dropped (dropSuspect++) unless tryResync corroborates. VALIDATED against source (exact).","[correct] internal/reseq/reseq.go:529-546 — Rebaseline() doc: a freshly re-handshaking sender emits ~1 DATA frame per RekeyTimeout so corroboration falls outside the window and 'the response is dropped as SUSPECT — the tunnel never re-establishes (defect D32)'. THE root-cause mechanism. VALIDATED against source (exact).","[correct] internal/bind/multipath.go:2167-2182 — Rebaseline() has exactly ONE caller (SetPeerRemote), whose only callers are the hub-failover controller; a one-sided restart with live paths never calls it, and the concentrator role runs no failover (failover.go:496 noop). So the surviving side's release point is never re-anchored on a plain restart.","[correct] internal/bind/multipath.go:1619-1650 — every inner WG datagram (incl. the opaque handshake init/response) is wrapped in an outer DATA frame and pushed through the peer's resequencer (rq.Observe); PROBE liveness bypasses it — which is why the OUTER paths recover (D12/T38) while the INNER init is dropped.","[correct] amneziawg-go device/timers.go:99-107 (RekeyTimeout=5s retransmit) + device.go:864-889 (first init pre-liveness, ErrNoHealthyPath, D37) — H2(c) compounds but is bounded to seconds, not the minutes-scale cause.","[correct] ledger: D36=wip, T101 (metric only)/T114 (docs interim) shipped no fix; D32.rootCause is the SAME reseq admit() mechanism, hardware-root-caused and fixed via Rebaseline for the failover path ONLY."]
- sessionLogs: [".cq/logs/20260714-090009-a8d87a208742a8de8.md"]
- rawLogs: [".cq/logs/raw/20260714-090009-a8d87a208742a8de8.jsonl"]

### H3 — confirmed

- createdAt: 2026-07-14T08:49:32.920Z
- updatedAt: 2026-07-14T08:56:54.496Z
- author: fable-5
- session: 671d5adc-7e2a-440e-b87d-6da40edeb7b7
- headline: Device-bind vs source-policy-routing collision is real in selectDeviceBinds; the shipped bind="source" toggle (T105/T106) + oif docs (T112) already fix it, leaving only optional auto-detection
- description: "TRUE iff: (a) selectDeviceBinds (internal/bind/pathsock.go) still picks SO_BINDTODEVICE (wildcard source) for a one-address/one-path interface, which cannot match `ip rule from <source>`, AND (b) config.BindMode `bind=\"source\"` (shipped T105/T106) now lets such topologies force source binding and is honored by selectDeviceBinds, AND (c) docs/install.md §3b (shipped T112) documents the collision + the oif recipe + the toggle — so the only remaining work is the OPTIONAL auto-detection of source-routed WANs (fixes-doc I5 stretch), a plan-flow decision, not a blocking defect."
- ledgerRefs: ["defects:D38"]
- evidence: ["[correct] pathsock.go:136-139 — auto heuristic device-binds (SO_BINDTODEVICE, wildcard src) exactly a one-address (familyCount==1) one-path (devPaths==1) interface: the VLAN-per-WAN collision with `ip rule from <source>` is real. VALIDATED against source.","[correct] pathsock.go:127-130 — BindModeSource leaves out[i]=\"\" unconditionally (\"the D38 escape hatch\"), so listenPath pins the source IP and never device-binds: the fix. VALIDATED against source (exact match).","[correct] multipath.go:746-752 — Open builds modes[] from m.defs[i].Bind and passes to planPathBinds→selectDeviceBinds: the toggle is consumed at runtime. VALIDATED against source (exact match).","[correct] multipath.go:613-627 — NewMultipath stores the normalize()-resolved config.Path (incl. Bind) into m.defs: closes config→runtime.","[correct] config.go:84-96,840-849 — BindMode source/device/auto validated; auto is the default; per-path override beats top-level default. Only the explicit toggle shipped (no auto-detection).","[correct] install.md §3b (:499-585, shipped T112) — documents the D38 collision + the `ip rule add oif <dev> table N` recipe + the bind toggle.","[correct] config.go:78-81,488-494 — stale 'not yet consumed by planPathBinds/selectDeviceBinds' comments (now false) = the already-filed D60."]
- sessionLogs: [".cq/logs/20260714-085530-a6ed9f2c043a11557.md"]
- rawLogs: [".cq/logs/raw/20260714-085530-a6ed9f2c043a11557.jsonl"]
