# P0 findings — the seven pitfall areas for bonding over amneziawg-go

This document records what the P0 spike established about the embedded
amneziawg-go engine (fork of `golang.zx2c4.com/wireguard-go`, pinned at
`v1.0.4` in `go.mod`) and the constraints it places on the later multipath
bonding work (P1+). It is grounded in the vendored engine source at
`github.com/amnezia-vpn/amneziawg-go@v1.0.4` and in wanbond's own
`internal/bind` pass-through Bind. Each section cites concrete files/symbols.

The engine talks to the transport exclusively through the `conn.Bind`
interface (`conn/conn.go:34`), and to the OS network stack through the `tun`
device. wanbond's whole bonding surface therefore lives behind ONE seam: the
`conn.Bind` implementation. P0 ships the trivial single-socket
`bind.Passthrough` (`internal/bind/bind.go:35`); the bonding logic (P1/T12)
replaces it behind the same interface. Everything below is about what that
seam does and does not let us do.

---

## 1. Batched Send/ReceiveFunc + BatchSize

The `conn.Bind` contract is batch-oriented. `ReceiveFunc` takes SLICES of
packets, sizes, and endpoints and returns a count `n`
(`conn/conn.go:28` — `type ReceiveFunc func(packets [][]byte, sizes []int,
eps []Endpoint) (n int, err error)`); `Send(bufs [][]byte, ep Endpoint)`
(`conn/conn.go:50`) likewise takes a batch; and `BatchSize() int`
(`conn/conn.go:57`) tells the engine how many buffers to preallocate per
call. The engine's own default caps this at `IdealBatchSize = 128`
(`conn/conn.go:19`).

wanbond's P0 Bind opts out of batching: `Passthrough.BatchSize()` returns the
`batchSize = 1` constant (`internal/bind/bind.go:28,128`). Its `receive`
reads exactly ONE datagram into `packets[0]` per call
(`internal/bind/bind.go:66-80`, `c.ReadFromUDPAddrPort(packets[0])` then
`return 1, nil`), and `Send` loops writing each buffer with one syscall each
(`internal/bind/bind.go:110-115`). So P0 pays one `recvfrom`/`sendto` syscall
per packet — no amortization.

Implication for bonding: the P1 multipath Bind MAY raise `BatchSize()` toward
`IdealBatchSize` to amortize syscall cost across a batch, but it must then
honor the contract precisely — fill `sizes[0..n)` and `eps[0..n)`, never
exceed the advertised `BatchSize()` (`conn/conn.go:55-57`), and be prepared
for the engine to hand `Send` up to that many buffers. The per-path fan-out
(which packet goes to which socket) is a Bind-internal decision the engine
never sees. Note the current single-packet cost as the throughput floor the
baseline (section 7) measures.

---

## 2. GSO/GRO fast paths

The engine's default Bind, `StdNetBind` (`conn/bind_std.go:32`), is heavily
optimized on Linux: it probes for UDP segmentation/receive offload at Open
time — `supportsUDPOffload(v4conn)` / `supportsUDPOffload(v6conn)`
(`conn/bind_std.go:177,186`), implemented in `conn/features_linux.go:14` by
setting `UDP_SEGMENT` (GSO, transmit) and `UDP_GRO`
(`conn/features_linux.go:24`, receive). On send it COALESCES many datagrams
into one GSO "super-packet" (`coalesceMessages`, `conn/bind_std.go:450`); on
receive it uses `recvmmsg` and SPLITS coalesced GRO batches
(`splitCoalescedMessages`, `conn/bind_std.go:502`, driven from `receiveIP`
at `conn/bind_std.go:226,248`). That is how `StdNetBind.BatchSize()` can
return `IdealBatchSize = 128` (`conn/bind_std.go:294-296`).

wanbond's `Passthrough` DELIBERATELY avoids this path. The type doc says so
directly (`internal/bind/bind.go:31-34`): it is "implemented directly over
net.UDPConn rather than delegating to the engine's default bind, whose
recvmmsg/GSO fast path is unnecessary here and brittle in restricted
environments." The brittleness is real in the engine itself: GSO can fail at
runtime and the engine carries a dedicated `ErrUDPGSODisabled`
(`conn/bind_std.go:328-337`) plus a runtime-disable retry
(`conn/bind_std.go:386` — `errShouldDisableUDPGSO`), because offload depends
on NIC checksum-offload support and on peer MTU vs path MTU.

Tradeoff, verified at P0: `Passthrough` trades peak single-path throughput
(no offload, one syscall per datagram) for robustness under restricted
sockets (namespaces, filtered egress, GSO-hostile NICs). P1 MAY reintroduce
per-path batched I/O in the multipath Bind, but should treat GSO as
best-effort with the same runtime-disable discipline the engine uses, and
must keep it per-path (each uplink socket has its own offload capability).

---

## 3. Endpoint identity — N paths behind one virtual endpoint

The engine identifies a peer's transport address through the opaque
`conn.Endpoint` interface (`conn/conn.go:78`; aliased into wanbond at
`internal/bind/bind.go:22`). Critically, a `Peer` holds exactly ONE current
endpoint at a time: the `endpoint` struct field on `Peer`
(`device/peer.go:29`), guarded by a mutex, with a single `val conn.Endpoint`.
The engine UPDATES that single value from received traffic — WireGuard
roaming — via `SetEndpointFromPacket` (`device/peer.go:290`, called from
`device/receive.go:412,440,499,561` on validated packets), unless
`disableRoaming` is set (`device/peer.go:33,293`).

wanbond's P0 endpoint is `udpEndpoint` (`internal/bind/bind.go:133`), carrying
a destination `AddrPort` plus an optional learned source IP
(`internal/bind/bind.go:133-136`); the `Passthrough.receive` sets it from the
datagram source (`internal/bind/bind.go:78`).

Design rule for bonding (P1/T12): the multipath Bind MUST present ONE stable
virtual `Endpoint` to the engine per peer, and internally stripe / fail over
across the per-path sockets underneath it. If the Bind instead surfaced a
different endpoint per path, the engine's single-endpoint field plus
`SetEndpointFromPacket` roaming would thrash — every packet arriving from a
different path would look like the peer roamed, rewriting `Peer.endpoint`
and pinning all egress to whichever path delivered last. The engine must
NEVER see per-packet endpoint churn; the bonding fan-out lives strictly below
the `Endpoint` the engine holds.

---

## 4. Amnezia junk at the Bind

amneziawg's obfuscation makes the byte stream at the Bind NOT a clean sequence
of WireGuard messages. Two kinds of extra bytes appear:

- **Junk packets** — standalone datagrams of random bytes. On the first
  handshake message the engine emits `JunkPacketCount` junk packets before the
  real handshake: `send.go` calls `JunkCreator.CreateJunkPackets`
  (`device/send.go:149`) and sends them as their own buffers
  (`device/send.go:157-158`, `peer.SendBuffers(junks)`), plus
  special/controlled junk from the handshake handler
  (`device/send.go:135-138`, `GenerateSpecialJunk` / `GenerateControlledJunk`).
- **Junk PREFIXES + magic headers** — real handshake/transport packets get a
  random junk prefix (`ASecCfg.InitPacketJunkSize` /
  `ResponsePacketJunkSize`, `device/send.go:167,217`) and a configurable
  type/"magic" header instead of the fixed WireGuard message-type bytes
  (`InitPacketMagicHeader` … `TransportPacketMagicHeader`,
  `device/awg/awg.go:26-29`).

All of this is configured through the amnezia security config, applied in
`Device.handlePostConfig` (`device/device.go:593`, populating
`device.awg.ASecCfg` from `device/awg/awg.go:13,21-29`). From the Bind's
vantage point these are ORDINARY UDP datagrams arriving at `ReceiveFunc` and
leaving through `Send`. The P0 `Passthrough` already treats them correctly:
it is transport-only and passes every datagram opaquely, never inspecting the
WireGuard payload (`internal/bind/bind.go:66-80,99-116`).

Design rule for bonding: the outer bonding frame codec (T11) MUST coexist with
amnezia without misclassifying junk. Because junk datagrams are
indistinguishable from real WG traffic by design, the bonding layer cannot
sniff the inner WG type to decide what to forward — it must wrap/route by its
OWN outer framing and pass the entire opaque payload (junk included) through.
See defect **D1** for complete-block validation. The original **D2** upstream
v1.0.4 package-global magic-header constraint has since been removed by the
local per-`Device` state patch (upstream #155).

---

## 5. Fork lag / API drift vs upstream

amneziawg-go is a fork of `golang.zx2c4.com/wireguard-go`. The transport-seam
types wanbond depends on — `conn.Bind`, `conn.Endpoint`, `conn.ReceiveFunc`
(`conn/conn.go:34,78,28`) — are byte-identical to upstream wireguard-go; the
fork's changes are concentrated in the amnezia obfuscation layer
(`device/awg/`, the junk logic in `device/send.go`), not in the `conn`
contract.

wanbond hedges the drift risk structurally: the entire amneziawg import is
isolated to ONE file behind three type aliases
(`internal/bind/bind.go:15-23` — `Bind = conn.Bind`, `Endpoint =
conn.Endpoint`, `ReceiveFunc = conn.ReceiveFunc`), with the rationale spelled
out in the comment there. Swapping to upstream wireguard-go (to pick up an
upstream fix the fork lags, or to drop amnezia) touches only that import and
those aliases; nothing else in the tree names the amneziawg package for the
Bind contract.

Risk, stated plainly: the fork can lag upstream security/perf fixes. It is
pinned at `v1.0.4` (`go.mod:6`). Mitigation is the single-file isolation above
plus tracking upstream `conn` for contract changes; if the two `conn`
interfaces ever diverge, the break surfaces at exactly one file.

---

## 6. Anti-replay window vs multipath reorder

WireGuard defends each transport keypair with an RFC 6479 sliding-window
anti-replay filter. The engine keeps a `replay.Filter` per keypair
(`device/keypair.go:28` — `replayFilter replay.Filter`) and checks every
decrypted transport packet's counter against it:
`replayFilter.ValidateCounter(elem.counter, RejectAfterMessages)` in the
receive path (`device/receive.go:493`). A counter that falls OUTSIDE the
window (too old) is silently dropped — `ValidateCounter` returns false and the
loop `continue`s (`device/receive.go:493-495`).

The window is finite: `replay/replay.go` uses `ringBlocks = 1<<7` blocks of
`blockBits = 64` each, giving `windowSize = (ringBlocks-1)*blockBits = 8128`
messages (`replay/replay.go:11-16`). Counters `>= RejectAfterMessages`
(`(1<<64)-(1<<13)-1`, `device/constants.go:16`) are rejected outright. Within
8128 of the high-water mark, out-of-order is fine; beyond it, dropped.

Why this is THE central bonding hazard: bonding splits one flow across paths of
DIFFERENT RTT, which REORDERS packets at the receiver. The emulated fixture
alone skews starlink ~45ms vs cellular ~64ms (`test/e2e/netns.go:30-33`) →
~19ms of cross-path skew, and real links are worse and more variable. If the
bonding layer let per-path reordering reach WG's inner counter stream, wide
reorder would trip the replay window and SILENTLY drop packets.

CRITICAL design rule (T18 resequencing): the bonding layer carries its OWN
outer sequence number and RESEQUENCES packets back into order BEFORE handing
them to the WG engine, and it NEVER reuses or perturbs WG's inner counter.
WG's counter must advance monotonically as the engine itself assigns it; the
bonding reorder is absorbed entirely in the outer layer so the inner
`ValidateCounter` check (`device/receive.go:493`) only ever sees in-window
counters.

---

## 7. Congestion / bufferbloat + pacing verdict

Measured by `TestP0Baseline` (`test/e2e/baseline_test.go`), which for each
uplink records idle RTT, saturated tunnel throughput, and RTT sampled WHILE a
saturating iperf3 runs (the standing-queue latency inflation). Measured on the
netns/netem fixture on real hardware (aarch64, 1 vCPU); a representative run:

| Path     | Idle RTT (tunnel) | RTT under saturating load | Bufferbloat Δ | Saturated throughput |
| -------- | ----------------- | ------------------------- | ------------- | -------------------- |
| starlink | 44.8 ms | 46.6 ms | +1.8 ms | 18.9 Mbit/s |
| cellular | 64.3 ms | 64.2 ms | −0.1 ms | 43.5 Mbit/s |

(Underlay idle RTT — netem delay only, no tunnel — was 42.7 ms starlink /
64.1 ms cellular, so the pass-through tunnel itself adds only ~0–2 ms. Tunnel
throughput is noisy run-to-run, 12–46 Mbit/s across runs, because it is
CPU-bound: single userspace-WG core on one vCPU, one syscall per datagram
per section 1 — not link-bound.)

Reading — **the in-fixture bufferbloat Δ is negligible (≈ 0–2 ms), NOT the large
inflation one might expect.** This is a genuine and important finding about the
FIXTURE, not evidence that pacing is unnecessary: the emulated paths impose
delay, jitter, and loss (`test/e2e/netns.go:69` — netem `delay`/`jitter`, and
`InjectLoss` at `:135`) but **no bandwidth cap**. With no rate-limited
bottleneck, and with the real bottleneck being userspace crypto on one vCPU
(which back-pressures via the TUN read rather than by growing a deep UDP egress
queue), no standing queue forms — so no bufferbloat manifests. The P0 fixture as
built therefore **cannot exercise bufferbloat/pacing**; that is a harness gap,
not an all-clear.

**Verdict — the bonding scheduler must still pace egress / bound in-flight bytes
per path, but this claim is NOT yet demonstrated in-fixture and rests on three
grounds:**

1. **Real-link buffer depth (external evidence).** Starlink and consumer 5G/LTE
   uplinks are well-documented bufferbloat sources — carrier and CPE buffers
   hold hundreds of ms under load. The fixture's delay-only emulation omits
   exactly this. The real-link manual checklist (`docs/manual-checklist.md` →
   `## P0`) must measure loaded RTT on the actual uplinks; that is where the
   inflation will appear.
2. **Head-of-line blocking when bonding a fast + slow path.** Independent of
   single-path bufferbloat, an unbounded slow path accumulates a queue and
   stalls the resequencer (section 6 — the reorder buffer waits on the slow
   path's backlog). Bounding in-flight bytes per path caps this stall.
3. **Correctness of "socket accepted the write" ≠ "path has capacity."** The
   scheduler cannot use write-acceptance as a capacity signal; it needs a
   per-path congestion/BDP estimate to place packets, which is the same state a
   pacer needs.

**Action for P1/P2 (blocks a real pacing decision):** add a rate-limited fixture
variant — netem `rate`, or a `tbf`/`htb` bottleneck qdisc on the edge veth — so
`TestP0Baseline` (or a P2 successor) can actually build a standing queue and
measure the idle→loaded inflation, then size the per-path in-flight bound from
the measured BDP. Until that exists, the pacing requirement is a design
commitment justified by grounds 1–3, not by an in-fixture measurement. This is
recorded for the T10 checkpoint as a REVISED assumption (the fixture must gain a
bandwidth bottleneck before P2 aggregation/pacing can be tuned).

---

## See also — manual real-link verification

The emulated numbers above are the netns/netem counterpart of the manual
real-link P0 checks. Run the real-link baseline per the checklist:
`docs/manual-checklist.md` → section **## P0 — spike / baseline** (tunnel comes
up, ping + TCP pass through, record single-path baseline throughput per uplink
with iperf3). The real-link run is also where the section-7 bufferbloat gap gets
resolved: unlike the delay-only fixture, real Starlink/5G uplinks have deep
buffers, so record loaded RTT there to see the inflation the fixture cannot
produce.
