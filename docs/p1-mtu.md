# P1 MTU accounting and MSS-clamping guidance (T12)

The multipath Bind (`internal/bind`, T12) wraps every opaque WireGuard datagram
in an outer DATA frame before it leaves a per-path socket. That outer frame is
pure overhead the P0 pass-through Bind did not add, so the tunnel MTU must shrink
to keep a full-size inner packet from fragmenting on the wire. This document
records the arithmetic (the source of truth is `internal/bind/mtu.go`, pinned by
`TestInnerMTUFixture`) and the MSS-clamping the operator must apply so TCP stays
inside the budget.

## Why fragmentation must be avoided

- **Loss amplification.** An IP-fragmented outer datagram is only delivered if
  *every* fragment arrives. On a lossy WAN (Starlink/5G — the target links) the
  probability that all fragments survive is strictly worse than for a single
  datagram, so fragmentation inflates the effective loss rate exactly where we
  can least afford it.
- **PMTUD black holes.** Path-MTU discovery relies on ICMP "fragmentation
  needed" (IPv4 DF) / "packet too big" (IPv6). Consumer CPE and carrier NAT
  routinely filter that ICMP, so an oversized packet is silently dropped and the
  sender never learns why — the tunnel stalls for large flows while small packets
  pass. Sizing the inner MTU conservatively side-steps PMTUD entirely.

## The overhead stack

A tunnelled inner packet is wrapped in four nested layers before the wire:

```
[ outer IP | outer UDP | outer DATA frame | WG transport | inner IP payload ]
  \_______ 28 (IPv4) _______/ \___ 40 ___/ \____ 32 ____/ \___ inner MTU ___/
```

| Layer | Bytes | Constant |
| ----- | ----- | -------- |
| Outer IPv4 header | 20 | `IPv4UDPOverhead` (with UDP) |
| Outer UDP header | 8 | " |
| Outer DATA frame | **40** | `frame.DataOverhead` |
| WireGuard transport | **32** | `WGTransportOverhead` |
| Amnezia junk prefix (obfuscation only) | **`max(s1, s2)`** | `config.Amnezia.MaxJunkPrefix()` |

The DATA-frame overhead of **40 bytes** decomposes as: XChaCha20 nonce (24) +
kind discriminant (1) + outer-seq (8) + path-id (1) + fec-group (4) + fec-index
(1, T24) + flags (1). DATA frames are unauthenticated (the inner WireGuard layer
authenticates the real payload), so they carry **no** MAC tag — the 40-byte
figure is exact. `TestDataOverheadMatchesEncoding` pins it to the real encoded
length so the MTU budget can never silently drift from the codec.

With FEC enabled (T24), the inner MTU is reduced by a further
`FECParityMTUPenalty` (5 bytes) so a full-size PARITY frame — which carries
more framing than a DATA frame — also fits the path MTU rather than
fragmenting; see `bind.InnerMTU`'s `fecEnabled` parameter and
`internal/bind/mtu.go`.

The WireGuard transport overhead of **32 bytes** is the 16-byte data-message
header (message type + reserved + receiver index + counter) plus the 16-byte
Poly1305 tag.

> Amnezia junk **prefixes** add further bytes on top of a real transport packet
> when obfuscation is configured. Since T225 (D85 fix-direction 4) the sizing path
> reserves the worst-case junk prefix — `config.Amnezia.MaxJunkPrefix()`, i.e.
> `max(s1, s2)` — automatically: the **static** inner-MTU derivation
> (`device.tunMTU`) subtracts it from each path's effective MTU before `InnerMTU`,
> and the **dynamic** per-path PMTU discovery
> (`telemetry.PMTUDiscovery.UsablePathMTU`) subtracts it from the discovered outer
> PMTU. So an amnezia deployment is sized for the true obfuscated data-frame
> envelope with no manual adjustment; the largest junked DATA packet still fits the
> path MTU. With obfuscation off (`s1 == s2 == 0`) the reserve is 0 and the derived
> MTU is byte-identical to plain WireGuard. The `jc`/`jmin`/`jmax` knobs size
> SEPARATE junk *packets*, not a per-datagram prefix, so they do not enter this
> budget.

## Computed inner MTU

For the default 1500-byte IPv4 path MTU, FEC off:

```
inner MTU = 1500 − 28 (IP+UDP) − 40 (DATA frame) − 32 (WG) = 1400 bytes
```

`internal/bind.InnerMTU(1500, false) == 1400`, asserted by `TestInnerMTUFixture`.
With FEC enabled the same path MTU yields `InnerMTU(1500, true) == 1395` (a
further 5-byte `FECParityMTUPenalty`). An IPv6 underlay costs 20 more header
bytes → `InnerMTU6(1500, false) == 1380`.

## Per-path MTU and min-across-paths TUN sizing (T200, T205, D85)

Each `[[paths]]` entry MAY declare an operator-known outer path MTU via the
`mtu` key (bytes; `minPathMTU..maxPathMTU` = 1280..9000, or 0/omitted for
"unset" — falls back to `bind.DefaultPathMTU` = 1500):

```toml
[[paths]]
name = "starlink"
mtu  = 1500

[[paths]]
name = "lte"
mtu  = 1400   # a PPPoE/CGNAT/cellular uplink with a smaller underlay MTU
```

`internal/device.tunMTU` computes the TUN's MTU as the **minimum**, across all
configured paths, of each path's inner MTU (`bind.InnerMTU(pathMTU,
fec.Enabled)`) — since the single virtual `wanbond0` interface carries one MTU
for the whole bond, a full-size inner packet must fit whichever path the
scheduler happens to send it over. Concretely, for a two-path config with `mtu
= 1500` and `mtu = 1400` and FEC off: `InnerMTU(1500, false) = 1400` and
`InnerMTU(1400, false) = 1300`, so the TUN is sized to **1300**, not 1400 — see
`TestTunMTUMinAcrossPaths`. A path that omits `mtu` (or sets it to 0)
contributes `InnerMTU(bind.DefaultPathMTU, fec.Enabled)` to that minimum, so an
all-default config reproduces pre-T200 sizing exactly. `validate()` separately
rejects a declared per-path `mtu` whose OWN derived inner MTU would fall below
`minInnerMTU` (576), independent of what any other path contributes to the
bond-wide minimum.

### Runtime PMTU auto-discovery (D88 / T206, T226–T229)

Omitting `mtu` on a path no longer merely pins the 1500 default — it enables
**per-path PMTU auto-discovery**. At `device.Up` the daemon starts one
`telemetry.PMTUDiscovery` per non-pinned path on its **own dedicated goroutine**
(so a search never stalls liveness probing) and binary-searches the largest
DF-padded probe that still echoes, between a floor of **1280** (the IPv6 minimum
link MTU) and a ceiling of `bind.DefaultPathMTU` (1500). The discovered value is
published as `PathSnapshot.PMTU` and the **T209 runtime resizer** folds it into
`wanbond0`'s live link MTU — so a bond that rides a constrained underlay (e.g. a
1400-MTU 5G path) **auto-shrinks** `wanbond0` to fit, and **regrows** when the
constraint lifts, with **no operator `mtu` knob required**.

- **Pinned override.** A path with an explicit `mtu` is PINNED: discovery never
  probes it and its configured value is authoritative — the static knob and
  auto-discovery compose (declare `mtu` when you know the underlay; omit it to
  measure).
- **Re-probe triggers.** A path `DOWN→UP` transition, an endpoint roam (the
  concentrator learning a new edge endpoint, or an edge hub-failover repoint),
  and a slow periodic refresh each re-run the search.
- **Reliability-aware acceptance (D91).** The search accepts a candidate size
  only after **N consecutive** echoing probes (`Confirmations`, default **3**),
  and short-circuits a candidate on its **first** non-echo. Single-echo
  acceptance was the D91 defect: on a partially-lossy carrier (a 5G path
  dropping ~30 % of packets) a size *above* the reliably-carried MTU still
  echoes on the ~70 % of probes that pass, so a lone echo accepted it and the
  search converged tens of bytes too high (field: inner 1331 vs a reliable
  ~1268–1300) — full-MTU DATA then black-holed (TCP 0 bytes rx). Requiring N
  consecutive successes rejects such an intermittently-echoing size, so the
  search settles at/below the size that echoes *reliably*. The short-circuit
  bounds a candidate to N probes and keeps the failing candidates — the only
  ones that wait a probe deadline — at ≤ log2(window), so worst-case search time
  still fits the e2e 20 s window. The end-to-end confirmation is
  `TestE2ELossyPathPMTUConvergence` (`-tags e2e`, hardware-tier): over a real
  socket path that deterministically drops every 3rd *oversize* outer datagram
  (nft `ip length > T` + `numgen inc mod 3`), the N-consecutive search converges
  at/below the reliably-carried threshold `T`, never running away to the ceiling.
- **Optional safety margin.** `SafetyMargin` (bytes, default **0**) is
  subtracted from the *reported* path MTU (`PathMTU` / `PathMTUOrZero` and the
  usable envelope that composes on them) as an extra cushion below the
  reliably-carried size, clamped to the 1280 floor. It does **not** change the
  raw discovered value or the `wanbond_path_mtu` gauge; with the default 0 the
  reported value is byte-identical to the discovered value.
- **Boot behaviour (no dip).** A non-pinned path reports **no** discovered PMTU
  until its first search converges, so `wanbond0` holds `InnerMTU(1500)` at boot
  and shrinks only once a real smaller PMTU is measured — never a boot-time
  shrink-then-grow.
- **Loosening is debounced.** A regrow (looser MTU) waits out the T209 dwell so a
  flapping path does not thrash the link; a tighten applies immediately.
- **Obfuscation headroom** is reserved once (see “The overhead stack”): the
  discovered outer PMTU has the amnezia junk-prefix subtracted a single time in
  the resizer’s sizing, never doubly.
- **Metric.** The per-path discovered PMTU is exposed as the `wanbond_path_mtu`
  gauge (0 until convergence).
- The T208 MSS clamp derives its MSS from the **live** `wanbond0` MTU
  (`--clamp-mss-to-pmtu`), so it re-clamps for free after a discovery-driven
  resize — no re-install.

## MSS clamping — two disjoint chains (T208, D85)

Setting the TUN MTU bounds what the *local* stack originates, but a TCP endpoint
whose SYN carries an MSS negotiated from a LARGER link MTU (1460 bytes for a
1500-byte LAN) will send segments that no longer fit once wrapped. The fix is to
clamp the TCP MSS of those SYNs to the tunnel's inner MTU minus the inner IP+TCP
headers:

```
MSS = inner MTU − 40 (IPv4: 20 IP + 20 TCP) = 1400 − 40 = 1360 bytes
      inner MTU − 60 (IPv6: 40 IP + 20 TCP) = 1380 − 60 = 1320 bytes
```

Two DISJOINT netfilter chains carry this clamp, split by ownership — a given SYN
is either locally originated (OUTPUT) or forwarded (FORWARD), never both, so the
two are complementary, never redundant:

### Edge-originated TCP → OUTPUT chain, DAEMON-owned (T208)

TCP that the **edge host itself originates** over `wanbond0` (the daemon, a local
service, an admin ssh out the tunnel) is clamped by the **daemon**: at
`device.Up` on the **edge role** it installs, and on `Close` it withdraws, an
`OUTPUT`-chain `TCPMSS --clamp-mss-to-pmtu` rule for BOTH IPv4 and IPv6 —
equivalent to:

```sh
iptables  -t mangle -A OUTPUT -o wanbond0 -p tcp --tcp-flags SYN,RST SYN \
          -j TCPMSS --clamp-mss-to-pmtu
ip6tables -t mangle -A OUTPUT -o wanbond0 -p tcp --tcp-flags SYN,RST SYN \
          -j TCPMSS --clamp-mss-to-pmtu
```

The operator installs NOTHING for this case — it is programmed automatically. The
install is idempotent (a re-run of `Up` after a crash never stacks a duplicate);
because an `-o wanbond0` rule stores the interface NAME and survives the
interface, the daemon removes it explicitly on `Close` (it does not vanish with a
`tun_persist` device).

**The clamp front-end is OPTIONAL — a missing one NEVER fails bring-up (T232/T233,
D92).** The daemon programs the OUTPUT clamp by exec'ing a front-end CLI (it links
no native netfilter binding). It tries `iptables`/`ip6tables` first; when they are
absent — an nft-only host such as Raspberry Pi OS or a modern Debian/Ubuntu — it
FALLS BACK to the `nft` CLI, programming the equivalent clamp in a daemon-owned
`table inet wanbond_mssclamp` (an `output`-hook chain at mangle priority whose
`tcp option maxseg size set rt mtu` mirrors `--clamp-mss-to-pmtu`, withdrawn as a
whole table on `Close`). Only when NEITHER `iptables` nor `nft` resolves does the
clamp go uninstalled — and even then bring-up merely logs a single WARN and
continues (the old behaviour, a fatal exit-1 systemd restart loop on nft-only
hosts, was defect D92). Edge-originated TCP then relies on the TUN MTU / operator
FORWARD clamp; the tunnel itself stays up.

### Forwarded (routed-LAN) TCP → FORWARD chain, OPERATOR-owned (G14)

TCP that the node **forwards** — a LAN host routed through the edge, or the
concentrator's downstream — traverses the `FORWARD` chain, which the daemon does
NOT touch. The operator installs the G14 recipe on each forwarding node:

```sh
# Clamp to the discovered PMTU (preferred — tracks the tunnel MTU automatically):
iptables  -t mangle -A FORWARD -o wanbond0 -p tcp --tcp-flags SYN,RST SYN \
          -j TCPMSS --clamp-mss-to-pmtu
ip6tables -t mangle -A FORWARD -o wanbond0 -p tcp --tcp-flags SYN,RST SYN \
          -j TCPMSS --clamp-mss-to-pmtu

# Or pin an explicit MSS if a fixed lower path MTU is known:
iptables  -t mangle -A FORWARD -o wanbond0 -p tcp --tcp-flags SYN,RST SYN \
          -j TCPMSS --set-mss 1360
```

`--clamp-mss-to-pmtu` is preferred in both chains: it derives the MSS from the
tunnel interface MTU, so it stays correct if the inner MTU is retuned (e.g.
amnezia enabled, a lower real path MTU, or a runtime resize). UDP and other
non-TCP traffic cannot be MSS-clamped; those flows must rely on the inner MTU and,
for locally originated traffic, on the sender honouring the TUN MTU.

## What T12 verifies

- `TestInnerMTUFixture` / `TestDataOverheadMatchesEncoding` — the arithmetic and
  its coupling to the codec.
- The e2e `TestMultipathNoFragmentation` fixture sends a max-inner-MTU payload
  with DF set and asserts, from a packet capture on the edge egress, that no
  outer datagram is IP-fragmented and that the inner packet fits the computed
  budget. (Runs on hardware with `/dev/net/tun`; compiled under `-tags e2e`.)
- `internal/device.TestClampMSS` pins the daemon-owned OUTPUT clamp's
  MSS-derivation (`inner MTU − 40`) in the non-privileged gate; the privileged
  `TestE2EDaemonMSSClampLifecycle` (`-tags e2e`, hardware-tier) asserts the rule
  is present after `Up` (v4+v6), idempotent across a crash+restart, and withdrawn
  on `Close` (T208, D85).
