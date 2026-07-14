# wanbond design

This document explains wanbond's architecture and, specifically, **exactly what
we built on top of the amneziawg-go WireGuard engine**. For setup and operation
see [install.md](install.md); for the front-door overview see the
[README](../README.md).

## Thesis

> Keep the WireGuard engine **unmodified**. Put **all** bonding logic in a custom
> `conn.Bind` beneath it, operating only on opaque, already-encrypted datagrams.

We embed [amneziawg-go](https://github.com/amnezia-vpn/amneziawg-go) as a library
and use it exactly as intended — for TUN management, the Noise handshake, AEAD
encryption, key rotation (rekey), endpoint roaming, and keepalives. We add
**nothing** inside the engine. Everything wanbond does — multipath scheduling,
outer-frame obfuscation, forward error correction, receive resequencing, and
per-path telemetry — lives in an implementation of the engine's `conn.Bind`
transport interface, which the engine drives for every packet it sends and
receives.

This gives a clean separation: WireGuard owns confidentiality, integrity, and
authenticity of the *payload*; wanbond owns *delivery* across multiple lossy
paths, plus outer obfuscation. The Bind never inspects plaintext — it moves
opaque ciphertext datagrams.

## Why amneziawg-go (and not plain wireguard-go)

The architecture decision is closed: amneziawg-go over plain wireguard-go,
kcp-go, or quic-go. The reason is **DPI resistance** (requirement 6). AmneziaWG
adds configurable obfuscation to the WireGuard wire — junk packets (`jc`, `jmin`,
`jmax`), handshake junk prefixes (`s1`, `s2`), and custom magic headers
(`h1`–`h4`) — as *defense-in-depth* beneath our own outer obfuscation codec.
Using the engine as-is means WireGuard's battle-tested crypto and roaming come
for free while the obfuscation knobs are available when configured.

**Fork-lag hedge.** amneziawg-go is a fork of wireguard-go and can lag upstream
security/perf fixes. We contain that risk: the entire dependency on the engine's
`conn` package is isolated to **one file**, `internal/bind/bind.go`, via type
aliases (`Bind = conn.Bind`, `Endpoint = conn.Endpoint`,
`ReceiveFunc = conn.ReceiveFunc`). The `conn.Bind`/`conn.Endpoint` contracts are
byte-identical between the two forks, so swapping back to upstream wireguard-go
(dropping obfuscation) touches only that file.

## The data path

```
                          EDGE                                     CONCENTRATOR
   ┌───────────────────────────────────────┐      ┌───────────────────────────────────────┐
   │  applications / kernel routing         │      │       kernel routing / NAT onward      │
   │                 │                       │      │                 ▲                       │
   │           ┌─────▼──────┐  TUN           │      │           ┌─────┴──────┐  TUN           │
   │           │ WireGuard  │  (amneziawg-go)│      │           │ WireGuard  │                │
   │           │  engine    │  Noise/AEAD    │      │           │  engine    │                │
   │           └─────┬──────┘  rekey/roam    │      │           └─────▲──────┘                │
   │   opaque encrypted datagrams  │         │      │       opaque encrypted datagrams        │
   │           ┌─────▼──────────────────────┐│      │┌──────────────────────┴─────┐          │
   │           │   wanbond conn.Bind        ││      ││   wanbond conn.Bind        │          │
   │           │  ┌───────────────────────┐ ││      ││  ┌───────────────────────┐ │          │
   │  send ───►│  │ scheduler (sched)     │ ││      ││  │ resequencer (reseq)   │ │──► recv  │
   │           │  │ FEC encode (fec)      │ ││      ││  │ FEC decode (fec)      │ │          │
   │           │  │ frame codec (frame)   │ ││      ││  │ frame codec (frame)   │ │          │
   │           │  └──────────┬────────────┘ ││      ││  └──────────▲────────────┘ │          │
   │           │   per-path UDP sockets      ││      ││   per-path UDP sockets     │          │
   │           └──────┬───────────┬─────────┘│      │└──────▲───────────▲─────────┘          │
   └──────────────────┼───────────┼──────────┘      └───────┼───────────┼──────────────────┘
              starlink│    cellular│   ══════ real internet ══════│  path A    │ path B
```

**Send** (edge → concentrator): the engine hands the Bind an opaque encrypted
datagram → the **scheduler** picks the path(s) → **FEC** optionally emits parity
frames for the group → the **frame codec** wraps each datagram in an obfuscated
outer frame → it goes out the chosen per-path UDP socket.

**Receive** (concentrator side): frames arrive on the per-path sockets → the
**frame codec** de-obfuscates and classifies them → **FEC** reconstructs any lost
DATA frames from PARITY → the **resequencer** restores order across paths →
the in-order opaque datagram is delivered up to the engine as if from one
endpoint.

Both ends run the same Bind; the diagram shows the dominant direction per role.

## What we built — layer by layer

Each bullet names the package (`internal/…`) that owns it.

### Outer frame codec — `internal/frame`

Wraps every outbound datagram in an outer bonding frame and defines the wire
format. Layout: a fresh 24-byte XChaCha20 nonce, then the **obfuscated** body
(`kind` byte ‖ per-kind header ‖ opaque payload), then an optional HMAC-SHA256
tag. Frame kinds:

- **DATA** — carries an inner WireGuard datagram. Header has the outer sequence
  number, path id, and FEC group/index. **Unauthenticated** (no tag).
- **PARITY** — a Reed-Solomon parity shard for a FEC group. **Unauthenticated.**
- **PROBE** — telemetry/liveness. **PSK-HMAC authenticated**, carries a monotonic
  `ProbeSeq` + timestamp for anti-replay.
- **CONTROL** — reserved out-of-band control. **PSK-HMAC authenticated**, carries
  a MAC-covered monotonic `Seq`. *(See "Not yet built" — currently unwired.)*

DPI resistance comes from here: the nonce randomizes every frame, the body is
XChaCha20-obfuscated, and there are **no magic bytes or fixed offsets** — the
wire is high-entropy UDP indistinguishable from noise (verified by
`internal/wireaudit` and `TestP5DPI`/`TestWireFormatAudit`). Overhead is small
and fixed: **DATA ≈ 17 bytes**, **PARITY ≈ 18 bytes** of header on top of the
nonce; the daemon subtracts this from the TUN MTU so there is no fragmentation.

### The multipath Bind — `internal/bind`

The heart of wanbond: the `conn.Bind` implementation the engine drives. It:

- **presents one stable virtual endpoint per peer** to the engine while privately
  fanning out across the real per-path sockets beneath it (design rule **A1**, see
  [p0-findings.md §3](p0-findings.md)). The engine must never see per-packet
  endpoint churn, so every receive returns the *same* `*udpEndpoint`; the learned
  destination is held in an `atomic.Pointer` because the Bind writes it under its
  mutex while the engine reads it locklessly.
- **learns edge endpoints dynamically** — the concentrator needs no edge endpoint
  config; it discovers each path's (possibly NAT'd) source from inbound traffic,
  enabling real CGNAT traversal.
- **demuxes multiple peers by authenticated source binding (G4 multi-peer)** — on a
  concentrator with more than one configured peer, inbound datagrams are routed to
  the owning peer via `peerBySource`, an atomic-pointer map keyed by the full source
  **`AddrPort` (address+port)** and populated only from authenticated PROBE frames.
  Keying on the AddrPort — not the bare address — lets two peers behind ONE public IP
  (CGNAT, distinct source ports) bind and demux independently. Each peer authenticates
  with its own per-peer `psk`: the first PROBE from a source that MAC-verifies under a
  peer's psk binds that source to that peer; subsequent DATA/PARITY frames from
  the same source are routed without re-authentication, keeping the receive hot
  path fast. The map is bounded by a global cap and a **per-peer quota**
  (`maxDemuxSources/len(peers)`, floor 1): a party holding one valid psk that floods
  spoofed sources exhausts only its own quota and never starves another peer's
  bootstrap PROBE. A peer that roams across CGNAT source ports past its own quota
  evicts its OWN oldest binding (LRU) to admit the new one — it is never dropped and
  never disturbs another peer's slot. With a single configured peer, a per-peer `psk` is **rejected** at
  config load (`config.validate`) and the top-level `psk` is the sole
  authenticator, byte-identical to pre-G4 behavior. Once a second peer is
  configured, per-peer `psk` becomes **required and pairwise-distinct**, and the
  top-level `psk` — still required by validation — **authenticates no peer**
  (`device.Up` feeds only each peer's own PSK, from `Config.PeerIdentities`, into
  the bind). Binding is learned only from PROBE frames — unauthenticated
  DATA/PARITY cannot establish or hijack a source-to-peer binding (D9/D11).
  Per-peer `name` is required in multi-peer mode and exposed as the metrics `peer`
  label for **every** bound peer, including the first/primary one: `device.Up`
  plumbs the primary's configured name into the bind
  (`bind.Multipath.SetPrimaryPeerName`) whenever a second peer is configured, so
  `peer=""` appears only on a true single-peer edge/hub/concentrator (D58).
- owns the **per-path UDP sockets**, byte counters, the send-path FEC `Admit`, the
  adaptive-FEC tick loop, and the wiring that hands frames to the scheduler and
  the resequencer.
- classifies each outbound batch by inner WireGuard message type (parameterized by
  the configured AmneziaWG obfuscation profile — custom `h1`–`h4` magic headers
  and `s1`/`s2` junk prefixes) so control frames can be treated specially by the
  pacer.
- **selects, per path, HOW its socket binds to the network** (`bind`, I5,
  Q42/`internal/bind/pathsock.go`'s `selectDeviceBinds`). Three modes, resolved
  per path from the path's own `bind` or, when that is omitted, the top-level
  default (itself `"auto"` when also omitted, matching pre-T105 behavior
  byte-for-byte): `"auto"` reproduces the original heuristic — device-bind
  (`SO_BINDTODEVICE`, wildcard source) only when provably equivalent to
  pinning `source_addr` (the address is the *sole* owner of its interface, so
  a device bind and a source-IP bind reach the same place), source-IP-bind
  otherwise; `"source"` forces the pre-T16 source-IP pin unconditionally; and
  `"device"` forces a device bind unconditionally. `"source"` is the fix for a
  one-address-per-VLAN policy-routing edge (each VLAN sub-interface is the
  *sole* address on its own device, so `"auto"`'s equivalence heuristic
  device-binds it — losing the source-based `ip rule` selector the operator's
  routing depends on); see [install.md §3b](install.md#3b-policy-routing-edge-topologies-source-ip-pinning-with-bind--source).
  A `"device"` path whose device bind cannot be honored — its `source_addr`
  resolves to no live interface, or the resolved interface's `SO_BINDTODEVICE`
  setsockopt fails (pre-5.7 kernel, permission) — silently fell back to
  source-IP pinning pre-D53, dropping the operator's roam-survival choice with
  no signal. `NewMultipath` now takes a component-scoped `log.Logger`
  (`log.Component("bind")`) and WARNs at both fallback points, naming the path
  and the (resolved or empty) interface, so the operator sees the roam
  property was lost; the same setsockopt fallback for an `"auto"`-selected
  device bind — never an operator's explicit choice — logs at INFO instead
  (D53). The WARN fires **only once a source-IP-pinned socket has actually
  materialized AND been installed as a live path** — a claim of "falling back
  to source-IP pinning" while the fallback bind itself also failed (the path
  stays `DEFERRED`, no socket at all) would be false, and that case instead
  logs a distinct, non-fallback-claiming "still deferred" WARN. A THIRD case
  — the fallback bind succeeds but installing the resulting socket into the
  running bond then fails (a scheduler/peer-fan-out wiring defect) — logs
  NEITHER WARN: the T55 background reconciler closes that socket and keeps
  the path deferred for a clean retry next tick, so claiming a fallback that
  was never actually wired in would be equally false (round 3). The
  equivalent ordering applies at `Open()` and `AddPath()`, whose fallback
  WARNs likewise wait until the path is fully wired into every bound peer,
  not merely bound — a failure there aborts the whole call instead of
  retrying, so nothing is silently kept half-admitted either way. The
  still-deferred WARN is deduplicated
  **per condition-transition**, not per background-reconcile tick: the T55
  reconciler (`StartReconcileLoop`/`reconcileDeferred`) retries a deferred
  path every `DefaultReconcileInterval` (1s), and a persistently-unresolvable
  interface — a mobile edge before its DHCP lease, Starlink mid-obstruction —
  WARNs once for the whole deferral window rather than flooding the log at
  1 Hz; the latch clears (re-arming a fresh WARN) the moment the interface
  resolves or the fallback starts working, so a later re-roam that drops the
  interface again is reported too.
- **tolerates a not-yet-assignable `source_addr` at startup** (`Open()`). A path
  whose *well-formed* `source_addr` no interface holds yet — a mobile edge booting
  before its 5G modem has a DHCP lease, Starlink mid-obstruction — makes
  `net.ListenUDP` return `EADDRNOTAVAIL`. Rather than tear the whole bond down,
  `Open()` brings the tunnel up on the paths that **do** bind and *defers* the
  unbindable ones: a deferred path is recorded (with its boot prober) and left
  `Down` — its prober never echoes, so the scheduler excludes it, exactly as the
  runtime path-down model treats a live-but-silent path. Hard guards: if **zero**
  paths bind, `Open()` still fails fatally (no transport ⇒ no tunnel); a
  **malformed** `source_addr` remains a hard config-load error (`config.validate`
  rejects it at load); and any bind error that is **not** `EADDRNOTAVAIL`
  (`EADDRINUSE`, permission) stays fatal. Startup and the runtime model are
  symmetric: a SIGHUP reload that introduces a not-yet-assignable path *defers* it
  the same way (`AddPath`), a reload that keeps a deferred path is a no-op for it
  (`PathNames` reports the durable membership, deferred paths included), and a reload
  that drops one retires it (`RemovePath`) — so a deferred path never regresses the
  SIGHUP-no-op invariant.
- **reconciles a deferred path in the background** (`StartReconcileLoop`, T55). A
  device-lifecycle goroutine (started after the first `Open`, stopped before `Close`
  by the same `Tunnel.Close` that stops the probe loop — no goroutine leak) polls the
  deferred set at `DefaultReconcileInterval` (1 s) and re-attempts each deferred
  path's bind. When a path's `source_addr` **becomes assignable** (its interface/
  address appears — the 5G modem finally got its DHCP lease), the reconcile **binds
  and promotes** it to a live path: it enters `m.paths`, the scheduler (as a new
  lowest-priority path), and its own reader, **reusing the preserved boot prober** so
  the path keeps its reserved id-stamp (no renumber, no peer-reflector collision) —
  so the tunnel starts using it WITHOUT a `Close→Open` restart, and the scheduler
  promotes it to active by the SAME liveness path as any runtime `AddPath`. A path
  that still cannot bind stays deferred and is retried; a path REMOVED before it binds
  (`RemovePath`) is dropped from the deferred set and never promoted. Everything runs
  under `m.mu`, so it serializes with `Send`/`Close`/`AddPath`/`RemovePath` and is a
  no-op on a closed bind. **Mechanism:** a bounded periodic poll, chosen over
  event-driven netlink route/addr subscription (`vishvananda/netlink AddrSubscribe`)
  because netlink is not an existing dependency and the deferred set is normally empty
  — so the steady-state tick is a single mutex-guarded length check. The full
  absent-then-added path flow over a real interface is validated by a netns e2e (T60).

This package is also the **amnezia boundary** (`bind.go`, above).

### Send-side scheduler — `internal/sched`

Decides which path(s) a frame goes out. Two policies:

- **active-backup** (default) — one active path; instant failover to the backup on
  liveness loss; the metered link stays idle (data-thrift) until needed.
- **weighted aggregation** (opt-in) — striped/weighted send-weighted-round-robin
  across paths, with a Mathis-proxy path quality signal (`1/(RTT·√loss)`),
  three-region hysteresis to avoid flapping, and load-based engage/disengage.

**Pacing** (per-path token buckets) is a scheduler feature that is **off by
default** and, when enabled, exempts WireGuard control frames from shedding so
overload cannot starve rekey. When pacing is enabled the per-path pace can be
sized from an **operator-declared** per-link bandwidth (`link_bandwidth` +
`link_rtt` on each `[[paths]]`): at config load `SizePacingFromBDP` derives the
scheduler's `per_path_capacity_fps` and `pacing_burst_frames` from the
bandwidth-delay product, sized to the **slowest declared link** (the shared pace
must not exceed the bottleneck). This is operator-*declared*, not runtime
auto-tuning — the value is fixed at load. With pacing off (the default) a declared
bandwidth is inert and the synthetic default pace is kept. See "Not yet built" for
why pacing stays off by default.

**Sizing from the bandwidth-delay product.** The BDP algorithm (`SizePacingFromBDP`,
internal/config) sizes the pacing parameters as follows:

- **`capacity_fps`** (frames/second): `bandwidth_bits_per_sec / (8 * avg_wire_frame_bytes)`.
  The rate at which the link sustains datagrams; frames arrive at this rate or the
  token bucket drains.
- **`burst_frames`** (frame count): `capacity_fps * rtt_seconds` ≡ `bandwidth * rtt / (8 * frame_size)`.
  The maximum burst (number of frames) the bucket can hold — one RTT's worth of
  in-flight data. Equivalently, the bandwidth-delay product (in bytes) divided by
  the average wire-frame size.

The operator measures two values per link (see [install.md §3a](install.md#3a-tuning-per-link-bandwidth-and-pacing)):
**`link_bandwidth`** (bits/s, e.g. `"50Mbit"`) and **`link_rtt`** (latency in
milliseconds, e.g. `"21ms"`). The idle RTT is the baseline; pacing bounds the
queue so RTT under load stays near the idle value, preventing bufferbloat
(excessive delay inflation). If heterogeneous links are bonded (different
bandwidths), the operator declares all of them; the scheduler uses the bottleneck
(slowest link) to size the shared per-path pace, because any link can be the
path for a given packet.

**Conservative sizing.** The wire-frame size used in the denominator is the full
path MTU (1500 bytes), the conservative floor for frame size. This produces a
frame rate that never over-paces a path; smaller average frames (headers,
fragmentation) would permit higher rates, but taking the worst case (full MTU)
ensures the pacer does not let the link overfill. Measurement on real links is
essential to validate that the declared bandwidth and RTT reflect the actual
link properties; the netns fixture is CPU-bound and cannot build the standing
queues pacing is designed to control (see [manual-checklist.md §P0](manual-checklist.md#p0--spike--baseline)).

**Capacity-sanity guard and WARN (T142/T144).** A path that declares
`link_bandwidth` under the weighted policy must be able to sustain the
aggregation engage threshold (`engage_fraction * per_path_capacity_fps`), or
aggregation can mathematically never engage at line rate on it — a
misconfiguration `validateWeightedEngageAgainstBandwidth` (T142) refuses at
config load (hard fail) for every path that DOES declare a bandwidth and
contradicts the guard. That guard cannot check a path that declares no
bandwidth at all; T144 is the complementary SOFT verdict for exactly that gap.
At load, `Config.WeightedCapacitySane` (`internal/config`) records: nil when
the policy is not weighted (not applicable); `true` when every path declares
`link_bandwidth` (SANE-VERIFIED — the T142 guard has then necessarily also
passed, since Load would otherwise have already failed); `false` when at least
one path's `link_bandwidth` is undeclared — UNVERIFIABLE, covering both "no
path declares it" and a PARTIAL declaration (reachable whenever pacing is
disabled, the shipped default, since the derive above then no-ops and never
rejects a partial set). Unlike T142, this is never fatal: startup must not be
blocked on an unverifiable — as opposed to a contradicting — declaration. The
daemon instead logs ONE actionable startup WARN
(`cmd/wanbond`'s `warnUnverifiableWeightedCapacity`) and the `/metrics`
endpoint exposes a STATIC, unlabeled `wanbond_weighted_capacity_sane` gauge
(1 = verified sane, 0 = unverifiable) registered directly from the loaded
config alongside — not through — the Source-driven collector (it is
config-derived, not per-peer, so it carries no `peer` label and is exempt from
the collector's per-peer back-compat rule); the family is absent entirely
under the active-backup policy. See
[install.md §6b](install.md#6b-weighted-policy-capacity-sanity-check-t144) for
the operator-facing remedy.

### Concentrator hub failover — `internal/device` (`failover.go`, T57)

Two *different* failovers exist and must not be conflated:

- **Per-path failover** (the scheduler, above): one uplink to the *active*
  concentrator dies, egress moves to another uplink. Sub-second, transparent, the
  WG session is untouched. This is the common case.
- **Hub failover** (this section): the *concentrator itself* is unreachable —
  **every** path's liveness to the active concentrator endpoint is DOWN
  simultaneously (HUB LOSS). No surviving uplink can reach it, so switching
  uplinks cannot help; the edge must move to a *standby concentrator*.

An edge peer carries an **ordered** concentrator endpoint list
(`config.Peer.EndpointSpecs`, Q18/T54/Q35): index 0 is the active/primary hub, the
rest are ordered standbys. Each entry is either an **IP:port literal** or, behind
the peer's explicit `dns = true` opt-in (Q29), a **hostname:port** whose record set
is resolved at runtime (see *Re-resolution* below). All hubs in the set share the
peer's **single WireGuard static key**, so the same peer identity re-handshakes
against whichever hub is active.

The controller (`hubFailover`) runs a device-lifecycle monitor loop (started after
`dev.Up`, stopped before `dev.Close`, alongside the probe/reconcile loops):

1. **Detect** hub loss off the **existing per-path liveness plane** — the same
   `telemetry.Prober` `State()` the schedulers select on — as *every* path
   reporting `StateDown`. No second detector.
2. **Advance** to the next endpoint in the ordered list and **repoint every
   path's remote** at it via `bind.Multipath.SetPeerRemote` (a uniform override —
   a hub switch retargets the whole bond; it supersedes any per-path `dest_addr`).
   This changes only the per-path fan-out *beneath* the engine's single virtual
   endpoint — **invariant A1 holds** (the engine still sees one peer, no
   per-packet endpoint churn).
3. **Re-handshake**: expire the peer's current keypairs (a **fresh** session —
   **no hub-to-hub state handoff**) and send a fresh handshake initiation toward
   the just-repointed standby. This is the only engine-*peer* coupling the
   failover path takes; it lives in `internal/device` next to the rest of the
   engine wiring (the `conn`-seam isolation of `bind.go` is unaffected).
4. **Re-baseline the receive resequencer**: the standby is a *separate process*
   whose outer sequence restarts near 1 — far below the release point the prior
   hub's high-rate stream advanced the shared `reseq.Resequencer` to. Its first
   frame (the WG handshake *response*) would otherwise land in the resequencer's
   *suspect* branch and be dropped, because the unauthenticated-DATA resync guard
   needs several corroborating low seqs and a freshly re-handshaking standby emits
   only ~one DATA frame per `RekeyTimeout` — so corroboration falls outside the
   failover window and the tunnel never re-establishes. `SetPeerRemote` therefore
   calls `Resequencer.Rebaseline`: because a hub switch is a **trusted control
   event** (not a forgeable wire frame), the release point is re-anchored to the
   standby's *first* frame immediately, discarding the dead hub's buffered frames
   while leaving already-delivered frames untouched.
5. **Re-arm** against the new endpoint: probes now flow to it, so if it too is
   fully down the controller advances again.

**Settle dwell.** After a switch (and at boot for endpoint 0) the newly-selected
endpoint gets a fixed dwell (`hubFailoverSettle`, 3 s) to prove itself LIVE before
another advance is allowed. It comfortably exceeds the liveness UP-recovery latency
(~3 echoes × 200 ms ≈ 600 ms once probes reach a reachable hub), so a still-DOWN
reading caused merely by echoes not having returned yet cannot skip past a healthy
standby, and it bounds the re-advance cadence (one switch / one handshake per dwell)
while a whole hub fleet is down.

**End-of-list policy: WRAP** (round-robin modulo the list length). Once the last
standby is exhausted the controller cycles back to index 0 and keeps retrying every
endpoint in order. Wrap is chosen over *stop* to preserve availability — a hub that
recovers earlier in the list is retried and settled on within one cycle, whereas
stopping at the last endpoint would strand the edge on a dead hub even after
endpoint 0 came back. The settle dwell keeps the round-robin a slow, bounded retry,
not a storm.

**GUARD (must-hold invariant).** A **single-endpoint** list takes **no** failover
action — no advance, no remote repoint, no re-handshake. A one-concentrator
deployment (including the legacy single `endpoint` form, normalized to a
one-element list) is therefore byte-for-byte the pre-T57 behaviour. The switch and
this guard are validated by the real-network netns e2e (`TestHubFailoverStandbySwitch`
+ `TestHubFailoverSingleEndpointGuard`, T62) and, over the real internet, by the
realhosts mid-transfer WAN-kill tier (`TestRealMidTransferWANKill`, T63).

**Re-resolution** (`resolution`, T73). A hostname endpoint spec has no fixed
address; its expansion is a **mutable, spec-keyed record set** the failover set
carries (`failoverSpec.addrs`, updated in place by `hubFailover.updateResolution`
under the endpoint-set lock — the sole mutation point). The re-resolution
controller keeps those record sets fresh. It mirrors the `hubFailover` shape (a pure
constructor over an injected `dnsresolve.Resolver`, the failover controller it
drives, a `telemetry.Clock`, and the `[dns]` poll interval + per-lookup timeout) and
runs its own device-lifecycle loop **off the send hot path** — all lookups happen on
its goroutine; results are applied only through `updateResolution`. Each evaluation:

- **Poll** every hostname spec on the fixed `[dns]` cadence; on a **successful,
  non-empty** lookup the addrs are **family-filtered then ordered** — addrs of a
  family no local path can source (a path binds a socket whose family is fixed by its
  `source_addr`, so an AAAA answer on a v4-only edge is unreachable and is dropped),
  then IPv4 first, then IPv6, deduped — a deterministic order so an unchanged answer
  yields a byte-identical expansion. The result is handed to `updateResolution`, which
  **repoints only on an actual active-IP change** (D32 no-op suppression). When the
  transport exposes a TTL (DoH/DoT), the next poll is clamped to
  `min(pollInterval, minTTL)`.
- **Liveness-loss trigger**: the instant every path to the **active** endpoint reads
  `StateDown` — the *same* `allDown` sweep the failover loop advances on (Q34: the
  two controllers coordinate purely through the shared lock and the update API) — the
  active spec is re-resolved **out of band**, edge-triggered, without waiting for the
  next poll tick. This out-of-band re-arm is clamped to `min(pollInterval, minTTL)`
  too, so record freshness holds on exactly the hub-loss path where it matters most.
- **Retention invariant (D46)**: a lookup that **fails** (error/timeout/NXDOMAIN) or
  yields an **empty** usable set — including an answer that **filters down to empty**
  because it carries no family any local path can source — **never publishes**: the
  spec keeps its last-good expansion and the controller retries next tick. A transient
  resolver fault, or an answer for a family the edge cannot reach, therefore never
  tears down a working endpoint set, and `hubFailover` never sees a previously-resolved
  active spec collapse to empty (the condition its `total < 2` guard could otherwise
  strand the bond on).

This controller runs **even for a single-hostname peer** (to track a changing DDNS
address), independent of hub-failover's `>= 2` guard; the first successful resolution
of a hostname-only peer is what boot-adopts its active endpoint.

**Device lifecycle** (T74). At `Up` the device does one **bounded initial resolve**
of each hostname spec (the `[dns]` per-lookup timeout) and builds the engine/UAPI peer
endpoint **only from resolved entries** — the flattened head of the seeded specs. If a
name does not resolve in the boot window (single-hostname peer, resolver down), the
tunnel comes up **without a peer endpoint** (tolerant boot, Q30 defer-and-reconcile —
an unresolvable name never hard-fails bring-up); the concentrator already runs
endpoint-less, so the engine supports it. The resolver is constructed **once**, and
**only when some peer carries a hostname spec** — a zero-hostname config builds no
resolver and starts no loop (Q29 inertness). The **first-resolve install path (R70)**
is load-bearing: `SetPeerRemote` repoints the bind's per-path remotes but **never sets
the engine peer's endpoint**, which is populated **only** by a UAPI `endpoint=` line
routed through `Multipath.ParseEndpoint`. So after an endpoint-less boot, the first
successful resolve must **install** the resolved endpoint on the engine peer via the
UAPI/IpcSet path (`deviceInstallEndpoint`) **then** re-handshake — the initiation now
has an addressable endpoint. Subsequent re-resolves of an already-installed peer take
the normal `SetPeerRemote` repoint path (the engine's virtual endpoint stays stable per
A1; only the bind remotes move). The re-resolution loop's stopper is held on the
`Tunnel` and invoked by `Close` between the hub-failover stop and the engine teardown.
The whole flow — endpoint-less boot while the name is unresolvable, the R70
first-resolve install, a mid-session concentrator-IP change, and the re-resolve repoint
whose `SetPeerRemote` re-baselines the receive resequencer so post-change traffic
actually resumes (the D32 guard) — is validated end to end by the privileged netns e2e
`TestDNSHubResolveAndReroute` (Q36), with a hermetic in-namespace UDP DNS responder as
the sole answer source (no external DNS egress).

**Boot-time forced initiation (D37/T120).** A THIRD, unrelated mechanism also lives in
`failover.go`: `startFirstPathUpHandshake`, wired in `device.go`'s `up()` for the edge
role only (a no-op for the concentrator, which is the responder to every edge and
initiates nothing). It is **not** part of hub failover or re-resolution — it fires
**once**, at most, per tunnel lifetime, on the bind's `Multipath.SetOnFirstPathUp`
latch (T117), regardless of whether the peer's endpoint list has one entry or many.
The problem it addresses: the engine's own boot-time handshake initiation can race
`bind.Multipath.Open` — issued before any path telemetry exists, it may hit
`bind: no healthy path` and get dropped, yet the engine still stamps
`peer.lastSentHandshake`, so a bare retry moments later can be silently suppressed by
the engine's own `RekeyTimeout` guard, leaving the tunnel waiting out that ~5 s
retransmit timer instead of re-initiating the instant a path is actually usable. The
callback reuses the `deviceRehandshake` pattern (`ExpireCurrentKeypairs` backdates
`lastSentHandshake` so the immediately-following `SendHandshakeInitiation` is never
suppressed; a no-op on a cold boot with no keypairs yet). It **must** be registered
before the probe loop starts (the latch's edge is not retroactive), so `up()` wires it
right after the engine is constructed, well before `StartProbeLoop`.

### Per-path telemetry — `internal/telemetry`

Measures per-path quality (RTT, loss, jitter) by exchanging authenticated PROBE
frames, and drives liveness/failover decisions. Carries the anti-replay
primitives (`AntiReplay` high-water, `ControlGuard`) that protect PROBE and
CONTROL against replay of captured valid-MAC frames.

### Receive resequencer — `internal/reseq`

Bonding across paths of different latency reorders packets. The resequencer holds
a **bounded window** with a timeout and restores order **before** the inner
WireGuard anti-replay window sees the traffic — critical, because WG would
otherwise drop legitimately-reordered datagrams. It runs its **own outer sequence
space** and never touches the inner WireGuard counter (a core invariant).

**Two trusted re-anchor triggers, plus an unauthenticated corroboration fallback.**
A DATA frame is unauthenticated, so the release point is normally moved only by
the `resync` guard (several distinct low seqs within one window) — which a
*single* low frame cannot trip, protecting against forgeable wire frames. Two
trusted control events, not forgeable, force re-anchoring via
`Resequencer.Rebaseline` or `RebaselineToLow` and are each tracked by the metric
`wanbond_resequencer_rebaselines_total`:

1. **Hub failover (D32, T57)** — `SetPeerRemote` at the bind layer (when the
   edge switches to a standby concentrator). The standby is a separate process
   with outer-sequence restarted near 1; `Rebaseline` unpins and re-anchors on
   the *next* frame immediately, discarding the dead hub's buffered frames
   while leaving already-delivered frames untouched.

2. **Peer restart (D36, T119)** — a **peer/concentrator process restart**
   resets the sender's outer-seq near 1, far below the release point the prior
   boot's high-rate stream advanced `next` to, and the restarted peer's wrapped
   WG init is a *lone* low frame that a plain `Rebaseline` cannot safely rescue
   (see below). It is detected on the **authenticated** liveness plane: the
   per-peer probe reflector reports an `epochChanged` when a probe adopts an
   already-adopted path under a **new session id** (a genuine restart, deduped
   once per epoch), and `dispatchInbound` re-baselines *that* peer's
   resequencer via `Resequencer.RebaselineToLow`. Because this fires on the
   demux-resolved per-peer view, the one call site covers both the edge
   single-concentrator primary and every concentrator per-peer resequencer, in
   either restart direction. Unlike the hub-failover `Rebaseline` (trigger 1),
   the **low-anchor** variant re-anchors only on a frame more than one window
   *below* the pre-rebaseline release point — so a stale HIGH-seq straggler
   still draining from the old boot's queues is suspect-dropped and cannot
   re-pin `next` high and block recovery (the D36 re-pin race).

A third path, the **unauthenticated corroboration fallback (D12)**, is the
`resync` guard itself: several distinct low seqs arriving within one
resequence window, with no special trigger — it is the steady-state defense
against non-trusted, forgeable frames. Unlike the two trusted triggers above,
it never calls `Rebaseline` or `RebaselineToLow`; it runs through
`tryResync`/`resync` and re-pins `next` only once `resyncCorroborate` (3)
mutually-close, independent low seqs corroborate a discontinuity, and it is
tracked by the separate metric `wanbond_resequencer_resyncs_total`, never
`rebaselines_total`.

Two boundary rules keep the low-anchor gate (trigger 2) from becoming a
*blackhole*: (1) the gate is armed only when the release point is high enough
for it to be satisfiable (`next > window+1`) — the restarted sender's first
DATA is outer-seq ~1, so at a small anchor no low frame could ever satisfy
`anchor - seq > window` and every frame would be suspect-dropped forever; at a
small anchor (light traffic / an early restart / a crash-loop) it falls back
to the plain unpin, which self-heals; and (2) a subsequent plain `Rebaseline`
(trigger 1, hub failover) *clears* any still-pending low-anchor, so the
fail-back stream is not re-classified against a now-stale anchor. Both
`Rebaseline` and `RebaselineToLow` are sound because a hub switch and an
authenticated epoch change are **trusted control events**, not forgeable wire
frames — trigger 3 (D12) carries no such guarantee, which is why it requires
corroboration instead of re-anchoring on a single frame. Two further rules
keep the gate from blackholing under *loss* (D36's own premise): (3) the gate
is **bounded** — the sole in-budget re-anchor frame at the tightest armed
anchor (`window+2`) is outer-seq 1, and if that lone wrapped-init frame is
*lost* every later new-boot frame fails `anchor - seq > window` and would
suspect-drop forever, so after O(window) consecutive pending-low drops the
gate falls back to the plain unpin and self-heals via the unauthenticated
resync-corroboration fallback (trigger 3); and (4) FEC repair must not
subvert the gate — `ObserveRecovered` normally bypasses `admit`, so a
parity-recovered *old-boot* frame while the gate is armed is by definition
pre-restart and is **dropped** (never seated), and the low-anchor re-anchor
**clears the ring** (like `resync`) so no stale occupied cell survives to
keep a head-of-line timeout live and jump `next` high past the restarted
stream.

**Frame rejection during rebaseline recovery.** While recovery is in flight,
the resequencer counts frames dropped as *suspect* via the metric
`wanbond_resequencer_dropped_suspect_frames_total` (scoped per peer in
multi-peer mode). A plain `Rebaseline` (trigger 1, D32 hub failover) does
**not** drive this counter: it unpins and re-anchors on the very next frame
immediately, so the dead hub's buffered frames drop as ordinary *late*
(stale/old), never suspect, and a HIGH-seq straggler simply re-pins `next`.
Suspect drops during recovery are driven instead by (a) the `RebaselineToLow`
low-anchor gate (trigger 2, D36) — a stale HIGH-seq straggler from the old
boot lands at or near the old release point and is classified suspect so it
cannot re-pin `next` high (the D36 re-pin race) — and (b) the unauthenticated
`resync` corroboration path (trigger 3, D12) before it has accumulated enough
corroborating seqs. The classification is precise, not merely "outside the
acceptance window": a frame is *suspect* when the low-anchor gate is armed
(any drop while `pendingLow` is set), or when it lands more than one window
*below* the release point (`next - seq > window`), or when it lands
`>= resyncFactor * window` *ahead* of the release point. A frame within a
single window *below* the release point is classified *late* (`dropLate`),
not suspect, and does not contribute to this counter.

**Operational expectation.** Because a detected peer restart now re-anchors
via `RebaselineToLow` (trigger 2) instead of waiting on the unauthenticated
`resync` fallback, a one-sided restart reconverges approximately at the
both-ends-fresh baseline (~25 s observed), rather than waiting out
WireGuard's own rekey timer — static analysis predicts ~10 s specifically for
the edge-restart direction (T121, `test/e2e/restart_onesided_test.go`).

### FEC — `internal/fec` + `internal/adaptivefec`

- `fec` implements Reed-Solomon over groups of *K* data shards + *M* parity
  shards (via `klauspost/reedsolomon`). Lost DATA frames are reconstructed from
  PARITY without retransmission.
- `adaptivefec` is a pure, deterministic control loop that floats *M* in
  `[0, ceiling]` to track measured loss. It can be driven by a **`target_residual`
  SLA** (invert the binomial residual `E[max(0,D−M)]/K` to the smallest *M*
  meeting the target) or a legacy `safety_factor` multiplier (mutually
  exclusive). Both off by default; enable with `[fec] enabled = true`
  (+ `adaptive = true`).

> **Dependency invariant (pinned in `go.mod`):** the adaptive path codes each
> group `RS(m, k≤ceiling)` yet decodes against a single `RS(m, ceiling)` codec.
> That is byte-exact only because reedsolomon's default matrix makes parity shard
> *j* identical across total-parity counts — an *undocumented* property. Any
> reedsolomon bump must be re-verified against
> `TestKlauspostParityPrefixStableInvariant` (`internal/fec`) before landing.

### TUN lifecycle: persistence, the default-route exception, and the session signal — `internal/device`

Three device-lifecycle surfaces beyond tunnel bring-up/teardown itself, all owned
by `internal/device`:

- **TUN persistence** (`tun_persist`, I7/Q38, `persist_linux.go`). By default
  `wanbond0` is a non-persistent TUN: the kernel destroys it when the daemon's
  last file descriptor closes on `Close`, so every restart drops the
  operator-owned addresses/routes/rules attached to it. `tun_persist = true`
  makes `device.Up` issue `TUNSETPERSIST` unconditionally with the config's
  value — `persist=false` explicitly *clears* the flag too, so a device left
  persistent by a prior `true` run and re-adopted under `false` reverts to
  non-persistent teardown, and a fresh TUN's `TUNSETPERSIST(0)` is a harmless
  no-op. amneziawg-go's `NativeTun.Close` only closes the fd/netlink socket —
  it never issues `RTM_DELLINK` — so this flag alone makes the link outlive
  `Close`; the next `Up` re-adopts the **same** persistent device by name via
  `CreateTUN`'s `TUNSETIFF`, preserving its ifindex, so operator-owned
  addressing survives untouched. Persistence does **not** exempt the interface
  from NetworkManager (D39) — an NM host still needs the unmanaged-devices
  drop-in regardless of `tun_persist`.
- **The default-route routing exception** (`mode = "default-route"`, I6/Q41,
  `route_linux.go`/`splitDefaultRoute` in `device.go`). Elsewhere in this
  document and in [install.md](install.md), wanbond's interface-ownership
  posture is: the daemon creates and brings up `wanbond0` but otherwise **never
  assigns addresses and installs no routes** — addressing and routing stay
  operator-owned. `mode = "default-route"` is the **one deliberate exception**:
  an edge-only opt-in, rejected on the concentrator, that marks a peer as the
  edge's full-tunnel concentrator. When set, once the interface is up the
  daemon installs that peer's `allowed_ips` split — the wg-quick-style
  `/1`+`/1` pair for a literal `0.0.0.0/0`/`::/0` (the same split
  `uapiConfig`'s `splitDefaultRoute` already applies when rendering the
  engine's UAPI `allowed_ip=` lines, so the installed routes and the engine's
  notion of "what this peer owns" always agree) — as plain scope-link device
  routes via `wanbond0`, and withdraws them on stop. This is **routes only**:
  no policy-routing rules, SNAT, or concentrator `ip_forward`/`MASQUERADE`/
  `FORWARD` programming, which stay documented operator recipes (client-LAN
  full-tunnel: [install.md §9](install.md#9-full-tunnel--client-lan-recipe-c3);
  concentrator NAT/forwarding: [install.md §5](install.md#concentrator-natforwarding-prerequisites-for-routed-traffic-c6)).
  A literal `0.0.0.0/0`/`::/0` is never installed or handed to the engine —
  only its `/1`+`/1` split, so the encrypted underlay path to the concentrator
  endpoint itself is never captured by the tunnel's own default route.
- **WG-session liveness signal** (`wanbond_session_established`, T101,
  `session.go`). The per-path liveness plane (probes) tells you a path's
  **transport** is reachable; it says nothing about whether the **inner**
  WireGuard session has actually converged. A `sessionMonitor` polls the
  engine's UAPI `IpcGet` peer dump at probe cadence and resolves each peer's
  last-handshake age against WireGuard's `RejectAfterTime` (the point past
  which a completed handshake's keypair is dead) into a binary verdict exposed
  as the `wanbond_session_established` gauge, plus one INFO `session
  established` log record emitted on each `0→1` edge (never repeated while the
  session stays up, so a live poll loop doesn't spam the log). This
  distinguishes a tunnel that is **still converging** (no completed handshake
  yet) from one that is **wedged** (a path reads up but the handshake is
  absent or has aged out) — a distinction `wanbond_path_up` alone cannot make.
  The monitor is stateless (a pure function of engine state + clock), reads
  through the UAPI seam only (there is no public accessor for a peer's
  last-handshake instant), and takes no WG-session coupling anywhere else in
  the bind — the Bind stays WG-unaware.

### Supporting packages

- `internal/config` — loads the single TOML config, validates fail-fast at load
  (0600 perms, complete-or-absent amnezia block, FEC bounds, scheduler
  invariants, unique `source_addr`). The optional `[dns]` block selects the
  resolver transport (system default, DoH, or DoT) a peer's opt-in hostname
  endpoint is resolved through, enforcing the BOOTSTRAP-IP invariant (a
  hostname-form `doh_url`/`dot_server` requires an explicit `bootstrap_ip`;
  an IP-literal host rejects a non-empty `bootstrap_ip` as a mode mismatch)
  and constructing the matching `internal/dnsresolve` implementation — when
  `bootstrap_ip` is set, that implementation dials it directly instead of
  resolving the configured hostname through the system dialer.
- `internal/device` — brings a tunnel up from a validated config (Up/Down/Reload),
  wires metrics, handles SIGHUP path add/remove without teardown.
- `internal/metrics` — a private-registry Prometheus `/metrics` endpoint that
  **refuses any non-loopback bind**.
- `internal/wireaudit` — the requirement-6 DPI wire-format audit (pcap parse +
  per-offset value-entropy + coverage checks) used by the P5 tests.
- `internal/log` — slog-based structured logging.
- `internal/dnsresolve` — the DNS resolution seam: a context-bounded `Resolver`
  interface, a system implementation over `net.Resolver`, two private-resolver
  transports sharing one dnsmessage encode/decode/error taxonomy, and an
  in-memory `FakeResolver` for tests. `DoHResolver` is DNS-over-HTTPS (RFC
  8484): wire-encoded with `golang.org/x/net/dns/dnsmessage`, POSTed over a
  dedicated `http.Client` with standard system CA trust (no
  insecure-skip-verify knob). `DoTResolver` is DNS-over-TLS (RFC 7858): one
  `crypto/tls` connection per lookup per family, queries framed with the
  2-byte length prefix, same system CA trust and server-name verification.
  Both query A and AAAA and tolerate one family answering NXDOMAIN when the
  other resolves, and both treat an empty final addr set as a typed error
  (`NXDomainError`/`NoDataError`), never a silent `([], nil)`. Residual leak
  for both: TLS SNI/timing to the configured provider.

## DNS endpoints and resolver privacy trade-offs

The optional `[dns]` block selects which transport resolves a peer's hostname
endpoint when opted in with `dns = true` per-peer. It is **opt-in by default —
[dns] alone never enables hostname resolution**; every peer endpoint is always
an IP literal unless explicitly marked `dns = true`. Hostname endpoints are
resolved through the OS system resolver by default (when `[dns]` is absent).

### Why default-off: the DPI thesis

A **pre-tunnel hostname lookup** is an unencrypted (cleartext) signal: an
on-path adversary sees the edge asking for the public concentrator's hostname
*before* the tunnel is up, making the host blocklistable at the DNS level
without inspecting any encrypted traffic. This is true regardless of how the
lookup is done — system resolver, DoH/DoT — because the resolver *itself*
learns the query. The default posture (IP literals only; hostnames deferred
to an explicit opt-in with `dns = true`) keeps this leakage off by default and
surfaces it as an intentional choice. The `[dns]` block is **optional** even
once a peer opts in — it only selects the resolver *transport*; an absent
block still resolves hostnames, through the OS system resolver.

### Leaked artifacts per resolver mode

Once a peer opts into hostname resolution (`dns = true`), the transport choice
in `[dns].resolver` determines what information escapes to a passive on-path
observer — per Requirement-6 testing (test/e2e/p5_dpi_test.go, Q29/Q33):

- **system** (default, OS stub resolver): A **cleartext DNS query naming the
  concentrator's hostname** — the full QNAME in plaintext on port 53. This is
  the most visible artifact: a DPI engine observes the exact hostname being
  resolved and can block it pre-emptively at the network edge.

- **DoH** (DNS-over-HTTPS, RFC 8484): The **TLS ClientHello SNI** (Server Name
  Indication, naming the DoH provider's host) **plus timing/connection metadata**
  to the DoH provider. The query payload itself is encrypted within the HTTPS
  tunnel, but the SNI and the observed request/response cadence allow timing-based
  inference and correlation to the DoH provider — the concentrator hostname is
  *not* visible on the wire.

- **DoT** (DNS-over-TLS, RFC 7858): Identical to DoH: the **TLS ClientHello SNI**
  (naming the DoT server) **plus timing and connection metadata** to the DoT
  provider. The query is encrypted, but SNI + timing correlates the edge to that
  resolver.

In all three cases, **multi-record expansions** (a hostname resolving to multiple
A/AAAA records, e.g. in a concentrator failover `endpoints` list) feed back into
`hubFailover`: each address in the result set becomes a separate entry in that
spec's slot of the ORDERED, ACTIVE-STANDBY failover list, so the edge can
advance within that set on hub loss (one address down → try the next). This
selection is **always** active-standby — `[scheduler].policy = "weighted"`
never applies here; weighted only reweights the *per-path* scheduler across the
paths reaching whichever single endpoint hub-failover has currently selected.

### Opt-in defer-and-reconcile boot semantics

A hostname endpoint that cannot be resolved at startup (resolver down, DNS
outage, network unreachable) **never blocks tunnel bring-up**. The tunnel
boots without that endpoint, and a background re-resolution loop (at the
cadence `[dns].poll_interval`, default 30s) installs it and initiates the
Noise handshake on the first successful lookup. Steady-state re-resolution then
repoints the bond only when the **active** endpoint's own AddrPort no longer
appears in its spec's freshly re-resolved, non-empty expansion (TTL-driven or
faster, per the operational `poll_interval`) — see "Change suppression" below
for the exact scope of that check.

- **Re-resolution cadence**: `[dns].poll_interval` (default 30s, must be > 0).
  Governs how often an opted-in hostname endpoint is re-resolved; changes are
  reconciled immediately.

- **Liveness-loss trigger**: the instant every path to the peer's **currently
  active** endpoint reads `StateDown` (the same `allPathsDown` sweep
  hub-failover advances on), the re-resolution controller re-resolves the
  **active spec, once**, out of band — an edge-triggered kick, not a sustained
  faster-cadence retry mode. It then re-arms the next scheduled poll at the
  normal TTL-clamped `poll_interval` delay; a sustained outage is covered by
  that regular poll loop, not by a shortened cadence.

- **Change suppression**: suppression is **active-survival-scoped, not
  set-identity-scoped**. `updateResolution` repoints only when the currently
  active AddrPort is **absent** from its spec's freshly re-resolved,
  non-empty expansion. Any re-resolution where the active AddrPort still
  appears anywhere in the new expansion — including a genuinely changed set
  that merely *added* an address, reordered the standbys, or dropped a
  different (non-active) address — takes no repoint and no re-handshake; only
  the derived flattened index is re-mapped. A repoint (one `SetPeerRemote` +
  one re-handshake) fires only when the active AddrPort disappears from its
  spec's new non-empty expansion, and it re-points to that expansion's new
  first entry.

### Mixing rules with ordered endpoints

When a peer declares multiple endpoints via the `endpoints` list (hub-failover),
and one or more are hostnames (`dns = true`), the following rules apply:

1. **Each hostname expands to its full A+AAAA record set** at resolve time.
   `orderAddrPorts` imposes only a family partition — IPv4 records first, then
   IPv6 — and **preserves the resolver's own encounter order within each
   family**; it does not sort by value. It also drops any address of a family
   no local path can source (a v4-only edge drops AAAA answers). Consequently
   the expansion is byte-identical tick after tick only when the resolver
   returns the same records in the same within-family order; a same-set but
   differently-ordered answer (e.g. DNS round-robin rotating the record order)
   yields a differently-ordered expansion, which reorders the standby advance
   sequence for that spec. Endpoint *selection* is always ordered
   **ACTIVE-STANDBY** — `hubFailover` advances through the flattened, ordered
   list on hub loss; `[scheduler]`'s weighted policy is never consulted here
   (see above).

2. **Order preservation**: the `endpoints` list order is **strict** — index 0
   is the active concentrator, index N are ordered standbys, and hub-failover
   advances through them in that order on hub loss. When a hostname at index K
   resolves to multiple addresses, they are inserted *consecutively* in that
   order (first address fills index K, subsequent addresses shift later
   entries right in the flattened ordering) — in the family-partitioned,
   within-family-resolver-order form described in rule 1, not a value-sorted
   order.

3. **Deduplication is per-namespace and load-time only; resolved addresses are
   never deduplicated across specs.** At config load, `resolveEndpoints`
   rejects a duplicate **within** each of two disjoint namespaces — a literal
   duplicating another literal, or a hostname:port duplicating another
   hostname:port — never across the two, and never on a resolved address (no
   resolution happens at load, Q30). At runtime, `orderAddrPorts` dedupes
   addresses only **within a single hostname spec's own** A+AAAA answer. Two
   *different* specs (two distinct hostnames, or a hostname and a literal)
   that happen to resolve or point to the same address:port are **not**
   rejected or merged — both remain distinct entries in the flattened
   failover list.

4. **Resolver selection is global**: `[dns].resolver` applies to *all* opted-in
   hostname endpoints — you cannot mix system + DoH + DoT within one config.
   Choose one transport per tunnel.

## Load-bearing invariants

These are the rules that keep the design correct; break them and the tunnel
misbehaves subtly. Agents and contributors must preserve them.

1. **One virtual endpoint per peer (A1).** The engine sees a single stable
   `Endpoint`; the Bind fans out beneath it. Never surface per-path endpoint
   churn to the engine.
2. **Own outer sequence space.** The resequencer/FEC use wanbond's outer-seq;
   never reuse or perturb the inner WireGuard counter.
3. **Resequence before inner anti-replay.** Restore cross-path order in `reseq`
   before the engine's replay window validates.
4. **Inner fail-closed; outer control authenticated.** WireGuard authenticates the
   payload; PROBE/CONTROL are PSK-HMAC authenticated with monotonic anti-replay;
   DATA/PARITY are deliberately unauthenticated (see Security model).
5. **Amnezia is isolated to `bind.go`.** All engine coupling goes through the type
   aliases there.
6. **Amnezia is all-or-nothing** and **single-engine-per-process** (its magic-
   header logic uses package-level globals). Config validation enforces the
   former; deployments must honor the latter.
7. **Re-verify the reedsolomon prefix invariant** on any FEC-dependency bump.

## Security model

- **Payload**: confidentiality, integrity, authenticity provided by inner
  WireGuard (Noise + AEAD). wanbond never sees plaintext.
- **Outer control plane** (PROBE, CONTROL): PSK-HMAC authenticated + per-peer
  monotonic anti-replay — an attacker cannot forge or replay them.
  - **Per-peer PSK (multi-peer concentrator, G4):** on a concentrator with more
    than one configured peer, each edge authenticates PROBE frames with its OWN
    per-peer `psk` — this field is REQUIRED and must be pairwise-distinct across
    peers (config load rejects a duplicate). The concentrator uses PROBE
    MAC-verification to learn each source `AddrPort`'s owning peer (`peerBySource`
    binding in `internal/bind/multipath.go`, keyed by address+port so CGNAT-shared
    IPs demux per-port); a source that MAC-verifies under peer A's psk is bound to A,
    and subsequent DATA/PARITY frames from it route to A without re-authentication. The top-level `psk` remains REQUIRED by
    config validation in every configuration, but on a multi-peer concentrator it
    authenticates **no peer**: `device.Up` feeds only each peer's own PSK (from
    `Config.PeerIdentities`) into the bind, so an existing single-peer edge does
    NOT keep authenticating via the top-level psk once a second peer is added —
    it must be given its own per-peer psk at that point. With a single configured
    peer, a per-peer `psk` is instead REJECTED at config load (not merely
    defaulted) and the top-level `psk` is the sole authenticator, so existing
    single-peer deployments parse and run identically unchanged.
- **Outer data plane** (DATA, PARITY): **unauthenticated by design**. A network
  attacker can forge/replay these; forgeries are dropped by the inner AEAD (real
  payload) or discarded by the FEC decoder as inconsistent. The accepted residual
  risk is **DoS-grade only** (an attacker can waste decode/resequence work), never
  a confidentiality or integrity break. This trade buys ~0 per-packet overhead on
  the hot path.
  - **In multi-peer mode**, unauthenticated DATA/PARITY do not establish
    source-to-peer bindings (they route using existing bindings learned from
    PROBE frames). The two forgery cases cost differently: a forged source
    `AddrPort` with **no existing binding** is trial-decoded against each
    configured peer's codec (`O(peers)`, bounded by the static peer count,
    `demuxInbound`) and, carrying no PROBE MAC, is dropped there — it never
    dispatches to a peer and never reaches that peer's resequencer or FEC
    decoder. A forged source that spoofs an `AddrPort` **already bound** to a
    peer (learned from that peer's authenticated PROBE) routes straight into
    that peer's plane and wastes its resequence/FEC work — the same
    DoS-grade residual described above for the single-peer case — before the
    inner AEAD rejects the forged payload. Neither case lets an attacker
    impersonate an existing peer: bindings are established and re-pointed
    only by an authenticated PROBE.
- **Traffic analysis / DPI**: the outer wire has no fingerprint (random nonce,
  obfuscated body, no magic bytes); AmneziaWG junk params add defense-in-depth.
  Protocol *mimicry* (looking like HTTPS) is an explicit non-goal.

## Not yet built / deliberate boundaries

These are recorded design boundaries, not defects:

- **No live CONTROL protocol.** The CONTROL frame kind and its `ControlGuard`
  anti-replay exist and are tested, but inbound CONTROL is currently dropped at
  the Bind (`multipath.go` receive default case). It is the chokepoint a future
  out-of-band signalling layer (e.g. explicit rekey/state) must route through.
- **Pacing ships disabled by default; no live auto-tuning (Q20).** Empirical
  sizing *is* built: `SizePacingFromBDP` derives the per-path pace from an
  operator-declared per-link bandwidth (`link_bandwidth`/`link_rtt`) at config
  load (T53), and enabled-pacing was measured to eliminate bufferbloat both on the
  bandwidth-capped netns fixture (T56/T61) and on the real-link tier (T58). What
  stays a **deliberate boundary**: pacing is off unless `[scheduler] pacing_enabled
  = true`, and the declared bandwidth is fixed at load — wanbond never re-derives
  the pace live from runtime measurements (Q20 rejected a live control loop for the
  pilot). Absent a declared bandwidth (or with pacing off) the default per-path
  capacity is synthetic (~115 Mbit/s), well above realistic uplinks.
- **In-fixture throughput/bufferbloat measurement is CPU-bound (a fixture
  boundary).** The netns fixture proves *functional* bonding/FEC/failover/DPI but
  is CPU/PPS-bound, so absolute "bonded ≈ sum of links" throughput and bufferbloat
  are **not** measured there — they are measured on the **real-link tier**. The
  capped-fixture BDP sub-test (T52) and the realhosts tier (`just p0-baseline` →
  `TestRealAggregationBufferbloat` / `TestRealMidTransferWANKill`, T58/T63) record
  the aggregation ratio and loaded-vs-idle RTT **report-only**. Note the realhosts
  topology shares a single physical uplink, so the measured aggregation ratio is
  ~≤1 — this is an informational, report-only measurement, not a bandwidth-
  aggregation guarantee (see
  [manual-checklist.md §P0](manual-checklist.md#p0--automated-real-link-baseline-realhosts-tier)
  and [p0-findings.md](p0-findings.md)).
- **Multi-concentrator hub-failover: UDP-only remains a non-goal.** Q18 brought
  edge-side ORDERED-ENDPOINT ACTIVE-STANDBY hub failover into scope; the config
  surface (T54), the switch (T57), the netns e2e (T62), and the real-link
  mid-transfer WAN-kill tier (T63) are all built and validated (see *Concentrator
  hub failover* above). What stays out of scope: UDP-only is deliberate — there is
  no TCP/TLS fallback for wholesale-UDP-block networks. The endpoint list itself
  is no longer IP:port-only: an entry may also be a hostname behind the peer's
  `dns = true` opt-in (see *DNS endpoints and resolver privacy trade-offs* above).

## References

- [p0-findings.md](p0-findings.md) — the P0 spike that fixed the single-endpoint,
  resequencing, and fixture-is-CPU-bound decisions.
- [p0-checkpoint.md](p0-checkpoint.md) — phase gate + deferred-work record.
- [install.md](install.md) — deployment and operation.
- [manual-checklist.md](manual-checklist.md) — manual per-phase + real-link
  verification.
