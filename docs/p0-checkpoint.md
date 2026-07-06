# P0 findings checkpoint — gate on P1-P5 (Q8)

This is the explicit P0→P1 gate mandated by Q8. It reviews `docs/p0-findings.md`
(the seven conn.Bind pitfall areas, measured and source-cited during the P0
spike) against every planned P1-P5 task and records, for each design assumption,
a **confirmed** or **revised** verdict. Per the T10 acceptance, no P1 task starts
before this note exists.

Inputs: `docs/p0-findings.md` (T9), the P1-P5 task DAG (T11-T30), the hardware
runs on `ubuntu@o3.7mind.io` (TestP0PassThrough + TestP0Baseline green), and the
amnezia defects filed during P0 (D1, D2).

## Assumption ledger

| # | Design assumption | Findings § | Verdict | Tasks it gates |
| - | ----------------- | ---------- | ------- | -------------- |
| A1 | **Virtual-endpoint identity** — N real paths hide behind ONE stable `conn.Endpoint` the engine sees per peer; the Bind stripes/fails-over internally and the engine never sees per-packet endpoint churn | §3 | **CONFIRMED** | T12, T16, T30 |
| A2 | **Batched I/O + GSO/GRO** — `BatchSize()` may rise toward `IdealBatchSize=128` in the multipath Bind; GSO/GRO is best-effort per-path with the engine's runtime-disable discipline | §1, §2 | **CONFIRMED** | T12 |
| A3 | **Anti-replay vs reorder** — WG's inner anti-replay window is finite (8128 msgs); the bonding layer carries its OWN outer-seq and resequences BEFORE WG, never reusing/perturbing the inner counter | §6 | **CONFIRMED** | T11, T18 |
| A4 | **Junk-at-Bind is transport-opaque** — amnezia junk/magic-header packets reach the Bind as ordinary datagrams; the Bind passes them opaquely and the outer codec routes by its own framing, never sniffing WG type | §4 | **CONFIRMED (source analysis)** — P0 e2e ran plain WireGuard (amnezia UAPI keys emitted only when configured; unexercised at P0 per T8/D1), so no junk traversed the Bind operationally; opacity holds by construction (`Passthrough` never inspects the payload); operational soak deferred to T19. Added T19 scope — see below | T11, T19 |
| A5 | **Fork isolation** — the `conn.Bind`/`Endpoint`/`ReceiveFunc` contract is byte-identical to upstream wireguard-go and isolated behind one file's type aliases | §5 | **CONFIRMED** | (cross-cutting) |
| A6 | **MTU accounting** — inner MTU = path MTU − (outer bonding header + WG overhead); no fragmentation / ICMP black holes | — (not measured at P0) | **CARRIED FORWARD (design requirement)** — P0's pass-through Bind adds no outer header, so nothing was measured; the design requirement is not contradicted by any P0 finding and is verified by T12's own acceptance (inner-MTU arithmetic asserted against a fixture capture) | T12 |
| A7 | **Congestion / bufferbloat & pacing** — the scheduler must pace egress / bound in-flight bytes per path | §7 | **REVISED** | T21, T23 |

## The one REVISED assumption (A7) and its impact

The P0 bufferbloat measurement (`TestP0Baseline`) returned a **negligible**
idle→loaded RTT delta (starlink +1.8 ms, cellular −0.1 ms). This is NOT evidence
that pacing is unnecessary; it is a **fixture limitation**: the netns/netem
fixture (`test/e2e/netns.go`) applies delay / jitter / loss but **no bandwidth
cap**, and tunnel throughput is **CPU-bound** (single userspace-WG core on one
vCPU, one syscall per datagram), so no standing queue forms and no bufferbloat
manifests. The "must pace" verdict therefore stands on real-link buffer depth,
fast/slow head-of-line blocking, and write-acceptance≠capacity — but is **not yet
demonstrable in-fixture**.

Impact on the P2 tasks:

- **T21 (weighted aggregation + data-thrift + send-pacing).** Its acceptance is
  prefixed *"Unit tests:"* — the pacing clause ("per-path egress rate does not
  exceed the configured/derived pace and no unbounded send backlog accumulates
  under sustained overload") is a SYNTHETIC unit test (offered-load and pace
  driven in-memory) and **stands as-is today** — it does not need the netns
  fixture. What the fixture gap defers for T21 is only the **empirical sizing**
  of the per-path pace / in-flight bound from a *measured* BDP (findings §7
  action): that measurement needs a link-bound (not CPU-bound) path, i.e. the
  capped fixture. So T21's unit acceptance is NOT sequenced behind the fixture;
  only its real-BDP tuning is.
- **T23 (P2 e2e: bonded throughput ≥ 85% of the sum of per-path throughputs).**
  This is the HARD-gated clause: T23's acceptance is an in-fixture e2e, and with
  per-path throughput CPU-bound rather than link-bound, "the sum of the two
  paths' individual throughputs" is dominated by the shared single-core crypto
  bottleneck, not the emulated links, so bonding may not sum cleanly and the 85%
  assertion is not meaningfully measurable without a bandwidth-capped fixture.
  (T23's sibling data-thrift clause — "5G bytes < 1% while Starlink healthy" — is
  routing policy, measurable today, and is NOT affected.)

**Neither task is invalidated** — both remain correctly scoped, and T21's
unit-level work proceeds immediately. The gap is a **harness prerequisite** for
T23's e2e (and T21's empirical pace-sizing): P2 must gain a **bandwidth-capped
fixture variant** (a
`netem rate` or a `tbf`/`htb` bottleneck qdisc on the edge veth, ideally with a
per-path cap and enough CPU headroom that the link — not the crypto — is the
bottleneck) so that (a) each path is link-bound and aggregation is measurable,
and (b) a standing queue can form so pacing has something to bound.

**This prerequisite is now planned as T35** (M10 additive follow-up):
`pathSpec` gains an OPTIONAL per-path bandwidth cap (`netem rate`) and a
config-time controlled-loss knob (`netem loss`), both defaulting to zero so the
DefaultPaths topology stays uncapped/lossless and every existing P0/P1 e2e test
runs unchanged; the cap is sized well below the ~150 Mbit/s CPU bound (default
50 Mbit/s) so the link is the bottleneck and a standing queue forms. T35's
`TestFixtureImpairment` self-test proves both knobs operationally. The drafted
follow-up below is **superseded by, and unified into, T35** — there is no
duplicate/parallel fixture-cap knob; T23's aggregation e2e and T21's empirical
pace-sizing run against T35's capped fixture.

## Added scope from P0 defects (not gating P1)

- **D1** (partial-amnezia config validation gap) and **D2** (amnezia
  magic-header package-level globals — one engine per process) are `root-caused`
  and linked to **T19**. T19 (amnezia end-to-end) must therefore also: validate
  a configured amnezia block for internal consistency (D1), and document/assert
  the single-engine-per-process constraint (D2). These refine A4; they do not
  gate P1.
- **D3** (e2e iperf3 readiness uses fixed sleeps) is `root-caused`, out-of-scope
  test-hardening, tracked for a future pass. Does not gate any phase.

## Verdict

**GO-AHEAD for P1 (milestone M5).** Every P1 design assumption is confirmed by the
P0 spike (A1-A5) or carried forward unrevised and uncontradicted (A6 — unmeasured
at P0, verified by T12's own MTU acceptance); the P1 task DAG (T11 frame codec → T12 multipath Bind
→ T13 probes → T15 active-backup scheduler → T16 re-roaming → T20 failover e2e →
T22 packaging, plus T30 runtime path set) is sound as planned. T11 may start.

**GO-AHEAD for P3/P4/P5 (M7/M8/M9)** as planned — no assumption revised; the FEC,
adaptive-FEC, and DPI phases are unaffected by the A7 finding (they assert
recovery/entropy/classification, not pacing or aggregate bandwidth).

**GO-AHEAD-WITH-PREREQUISITE for P2 (milestone M6).** T17 (/metrics) and T18
(resequencer) are unaffected and may proceed. T21's unit-level acceptance
(including the synthetic pacing/backlog test) also proceeds now; only **T23's
in-fixture aggregation e2e** — and T21's *empirical* per-path pace-sizing from a
measured BDP — must be preceded by the bandwidth-capped fixture variant (A7).

**Actioned as T35** (M10). The drafted follow-up below was the original
placeholder; it is now **SUPERSEDED by and MERGED into T35** (extend the netns
fixture with a per-path bandwidth cap and controlled-loss knobs). No separate
`/cq:plan:follow-up` and no parallel fixture-cap task should be created — T35 is
the single owner of this harness prerequisite. Retained verbatim below for
provenance only:

> _(superseded by T35)_ `/cq:plan:follow-up G1` — P0 checkpoint (T10) revised
> assumption A7: the
> netns/netem fixture emulates delay/jitter/loss but no bandwidth cap, and P0
> tunnel throughput is CPU-bound, so T23's bonded-vs-sum aggregation e2e and the
> empirical BDP-based sizing of T21's per-path pace cannot be measured in it
> (T21's synthetic unit-level pacing test is unaffected). Add a P2 harness task: a
> bandwidth-capped fixture variant (per-path `netem rate` or `tbf`/`htb`
> bottleneck on the edge veth, sized so the link is the bottleneck with CPU
> headroom to spare), and make T23's aggregation e2e — plus a new
> T21 empirical-pace-sizing clause/check that derives the per-path pace from a
> measured BDP — run against it. Sequence it before T23 within M6. T21's unit
> acceptance is NOT blocked and proceeds independently.

This checkpoint does not re-plan the DAG; P1 (and P3-P5) proceed unchanged, and
P2's single prerequisite is captured as the follow-up above to be actioned when
P2 begins.
