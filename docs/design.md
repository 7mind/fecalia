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
- owns the **per-path UDP sockets**, byte counters, the send-path FEC `Admit`, the
  adaptive-FEC tick loop, and the wiring that hands frames to the scheduler and
  the resequencer.
- classifies each outbound batch by inner WireGuard message type (parameterized by
  the configured AmneziaWG obfuscation profile — custom `h1`–`h4` magic headers
  and `s1`/`s2` junk prefixes) so control frames can be treated specially by the
  pacer.
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
(`config.Peer.Endpoints`, Q18/T54): index 0 is the active/primary hub, the rest
are ordered standbys. Endpoints are **IP:port only — no DNS resolution** (the T54
constraint). All hubs in the set share the peer's **single WireGuard static key**,
so the same peer identity re-handshakes against whichever hub is active.

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

### Supporting packages

- `internal/config` — loads the single TOML config, validates fail-fast at load
  (0600 perms, complete-or-absent amnezia block, FEC bounds, scheduler
  invariants, unique `source_addr`).
- `internal/device` — brings a tunnel up from a validated config (Up/Down/Reload),
  wires metrics, handles SIGHUP path add/remove without teardown.
- `internal/metrics` — a private-registry Prometheus `/metrics` endpoint that
  **refuses any non-loopback bind**.
- `internal/wireaudit` — the requirement-6 DPI wire-format audit (pcap parse +
  per-offset value-entropy + coverage checks) used by the P5 tests.
- `internal/log` — slog-based structured logging.
- `internal/dnsresolve` — the DNS resolution seam: a context-bounded `Resolver`
  interface, a system implementation over `net.Resolver`, and an in-memory
  `FakeResolver` for tests.

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
- **Outer data plane** (DATA, PARITY): **unauthenticated by design**. A network
  attacker can forge/replay these; forgeries are dropped by the inner AEAD (real
  payload) or discarded by the FEC decoder as inconsistent. The accepted residual
  risk is **DoS-grade only** (an attacker can waste decode/resequence work), never
  a confidentiality or integrity break. This trade buys ~0 per-packet overhead on
  the hot path.
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
  no TCP/TLS fallback for wholesale-UDP-block networks — and the endpoint list is
  IP:port only (no DNS resolution).

## References

- [p0-findings.md](p0-findings.md) — the P0 spike that fixed the single-endpoint,
  resequencing, and fixture-is-CPU-bound decisions.
- [p0-checkpoint.md](p0-checkpoint.md) — phase gate + deferred-work record.
- [install.md](install.md) — deployment and operation.
- [manual-checklist.md](manual-checklist.md) — manual per-phase + real-link
  verification.
