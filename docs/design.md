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
  (`EADDRINUSE`, permission) stays fatal. This makes startup symmetric with the
  runtime model, where a SIGHUP-added bad path (`AddPath`) errors without disturbing
  the tunnel. (The background reconcile that retries a deferred path as its address
  appears is **not yet built** — see "Not yet built".)

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
overload cannot starve rekey. See "Not yet built" for why pacing is not
empirically sized.

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
- **Pacing not empirically sized.** `SizePacingFromBDP` derives per-path pacing
  from a measured bandwidth-delay product, but it is a helper, not auto-wired; the
  default per-path capacity is synthetic (~115 Mbit/s), well above realistic
  uplinks. The netns fixture is CPU/PPS-bound and cannot produce the standing
  queues needed to tune pacing — so it is disabled by default and must be tuned
  from real-link measurement.
- **Throughput aggregation unmeasured in-fixture.** The fixture proves *functional*
  bonding/FEC/failover/DPI; "bonded ≈ sum of links" and bufferbloat require real
  uplinks (see [manual-checklist.md](manual-checklist.md) §P0 and
  [p0-findings.md](p0-findings.md)).
- **No background reconcile of a deferred path.** `Open()` tolerates a
  not-yet-assignable `source_addr` by deferring the path (above), but the loop that
  retries binding it as its interface/address appears — event-driven via netlink or
  a bounded poll — is not yet wired. A deferred path therefore stays `Down` until
  the next full `Close→Open` cycle re-attempts every path's bind.
- **Single concentrator; UDP-only.** No tunnel-termination failover, no TCP/TLS
  fallback for wholesale-UDP-block networks. Both are explicit non-goals.

## References

- [p0-findings.md](p0-findings.md) — the P0 spike that fixed the single-endpoint,
  resequencing, and fixture-is-CPU-bound decisions.
- [p0-checkpoint.md](p0-checkpoint.md) — phase gate + deferred-work record.
- [install.md](install.md) — deployment and operation.
- [manual-checklist.md](manual-checklist.md) — manual per-phase + real-link
  verification.
