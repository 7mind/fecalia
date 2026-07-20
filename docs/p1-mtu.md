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

> Amnezia junk **prefixes** add further *variable* bytes on top of a real
> transport packet when obfuscation is configured. They are not subtracted here
> (they are per-packet variable and configurable); an amnezia deployment must
> lower the path MTU it feeds `InnerMTU` by its worst-case junk-prefix size, or
> accept that the largest junked packets may fragment. Wiring amnezia end-to-end
> is T19; this note is revisited there.

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

## MSS clamping (the operator action)

Setting the TUN MTU bounds what the *local* stack originates, but TCP endpoints
*behind* the tunnel (a LAN host routed through the edge, or the concentrator's
downstream) negotiate their MSS from their own link MTU and will happily send
1460-byte segments that no longer fit once wrapped. The fix is to clamp the TCP
MSS of forwarded SYNs to the tunnel's inner MTU minus the inner IP+TCP headers:

```
MSS = inner MTU − 40 (IPv4: 20 IP + 20 TCP) = 1400 − 40 = 1360 bytes
      inner MTU − 60 (IPv6: 40 IP + 20 TCP) = 1380 − 60 = 1320 bytes
```

Clamp on the forwarding node (edge and concentrator) with the standard
`TCPMSS --clamp-mss-to-pmtu` target on the tunnel interface, or an explicit value:

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

`--clamp-mss-to-pmtu` is preferred: it derives the MSS from the tunnel interface
MTU, so it stays correct if the inner MTU is retuned (e.g. amnezia enabled, or a
lower real path MTU). UDP and other non-TCP traffic cannot be MSS-clamped; those
flows must rely on the inner MTU and, for locally originated traffic, on the
sender honouring the TUN MTU.

## What T12 verifies

- `TestInnerMTUFixture` / `TestDataOverheadMatchesEncoding` — the arithmetic and
  its coupling to the codec.
- The e2e `TestMultipathNoFragmentation` fixture sends a max-inner-MTU payload
  with DF set and asserts, from a packet capture on the edge egress, that no
  outer datagram is IP-fragmented and that the inner packet fits the computed
  budget. (Runs on hardware with `/dev/net/tun`; compiled under `-tags e2e`.)
