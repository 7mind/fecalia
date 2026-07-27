# wanbond

**wanbond** bonds two (or more) unreliable, heterogeneous WAN uplinks — e.g. a
low-latency-but-jittery Starlink link and a stable-but-metered 4G/5G link — into
a single resilient, DPI-resistant WireGuard tunnel for general IP traffic, with
adaptive forward error correction (FEC) that masks per-path loss without
duplicating every packet.

It is a single self-contained Go binary that runs on both ends of the tunnel:

- **edge** — a Linux box (behind a router) that bonds the local WAN uplinks;
- **concentrator** — a small public-IP VPS that terminates the tunnel and NATs
  traffic onward. Supports multiple edges (multi-peer mode); with more than
  one edge, each authenticates with its own per-peer PSK (a single edge uses
  the top-level PSK).

The same binary serves both roles; the role is chosen from the config file.

## What it gives you

In priority order (earlier properties never regress for later ones):

1. **Transparent failover** — a TCP flow survives a WAN dying mid-session, with
   no reset (WireGuard's roaming + our per-path liveness/failover).
2. **Data-thrift** — a metered link stays ~idle until it is actually needed.
3. **On-demand aggregation** — under load, traffic stripes across both links.
4. **FEC loss-masking** — Reed-Solomon parity reconstructs lost frames instead
   of retransmitting.
5. **Adaptive FEC** — redundancy tracks measured loss on the path(s) that
   **actually carry data**. For one stable active-backup carrier, authenticated
   receiver feedback counts native, parity-reconstructed, and final missing
   DATA exactly once, so clean priority probes cannot mask ordinary DATA loss;
   the controller conservatively combines that pre-recovery signal with the
   carrier's probe loss. Weighted striping retains its weight-weighted probe
   mix, and a lossy but data-idle standby never inflates parity. The controller
   targets a residual loss SLA rather than fixed overhead: parity engages as
   soon as measured loss would miss the SLA, while one estimator-quantum blip
   or an early probe spike measured over too few samples stays at zero overhead.
6. **DPI resistance** — the outer wire is unidentifiable high-entropy UDP: no
   WireGuard fingerprint, no magic bytes (nDPI/Suricata do not classify it as
   VPN/WireGuard).

## How it works, in one paragraph

wanbond embeds a locally patched [amneziawg-go](https://github.com/amnezia-vpn/amneziawg-go)
WireGuard engine (TUN, Noise handshake, AEAD, rekey, roaming, keepalive) and puts
**all** bonding logic — multipath scheduling, an obfuscated outer frame codec,
Reed-Solomon FEC, a receive resequencer, and per-path telemetry — into a custom
`conn.Bind` that lives *beneath* the engine and operates only on opaque, already-
encrypted WireGuard datagrams. The engine sees one stable virtual endpoint per
peer; the Bind privately fans traffic out across the real per-path UDP sockets
and, on a concentrator with multiple edges, demuxes inbound traffic to the
owning peer from PROBE frames authenticated under that peer's own PSK. For the
full picture and the exact list of what we built on top of amneziawg-go, read
**[docs/design.md](docs/design.md)**.

## Quick start

Requires the dev shell (`nix develop`) which puts Go 1.26, Node.js 24/npm,
golangci-lint, and the netem/DPI test tooling on `PATH`.

```sh
just build          # frontend typecheck/test/build + embedded UI + go build ./...
just test           # frontend typecheck/test + unprivileged Go tests
just lint           # go vet + golangci-lint (incl. -tags e2e / -tags realhosts)
just release        # web-build + static linux amd64+arm64 binaries into dist/
```

Deploying the tunnel (build → install → config → systemd → firewall → metrics) is
covered per-topic in **[docs/install.md](docs/install.md)**; to provision a fresh
edge + concentrator (+ standby) from scratch, follow the operator-facing
**[pre-pilot rollout runbook](docs/runbook.md)**. The short version:

1. `just release`, then `install -m 0755 dist/wanbond-linux-<arch> /usr/local/bin/wanbond`.
2. Write `/etc/wanbond/config.toml` (mode **0600** — the daemon refuses looser
   permissions). Minimal shape:

   ```toml
   role = "edge"                    # or "concentrator"

   [[paths]]
   name        = "starlink"
   source_addr = "192.0.2.10"       # local IP this path's socket binds to
   [[paths]]
   name        = "cellular"
   source_addr = "192.0.2.20"

   [wireguard]
   private_key = "…"
   [[wireguard.peers]]
   public_key  = "…"
   endpoint    = "concentrator.example:51820"   # edge only; concentrator learns edges
   allowed_ips = ["10.10.0.0/24"]

   psk = "…"                        # outer control/probe PSK (not the WG PSK)

   # optional: [amnezia] (obfuscation, all-or-nothing), [fec], [scheduler], [dns], [liveness], [metrics], [monitor], [log]
   ```

3. Install the systemd unit for the role
   (`packaging/systemd/wanbond-{edge,concentrator}.service`), `daemon-reload`,
   `enable --now`.
4. On the **concentrator**, allow the tunnel interface through the firewall
   *ahead of* any default REJECT, and persist it across reboots (see install.md;
   `just realhosts-provision` automates the standing-testbed case).

## Operating it

- **Live reload**: `systemctl reload wanbond-…` (SIGHUP) re-reads the config and
  adds/removes paths without tearing the tunnel down. With pacing+FEC enabled,
  path-service changes use an authenticated peer recovery contract: DATA and
  inner control wait behind a 250 ms bounded transition while PROBEs remain
  live, then fast recovery resumes only after an exact ACK; legacy peers fall
  back conservatively without resetting OuterSeq or FEC GroupID. Same-name
  pacing/FEC scalar changes remain restart-required: reload warns and leaves the
  running service unchanged. A daemon restart establishes a new authenticated
  SessionID; any engine-driven Close/Open within one process advertises a new
  ContractID while preserving that process's sequence spaces.
  On a stable active-backup FEC receive path, that exact ACK also authorizes a
  shorter head-of-line recovery window
  `W=min(250ms, A+clamp(4*max(SRTT),10ms,250ms))`. The receiver uses it only
  while the ACK's composite path/source and the current DATA carrier's
  authenticated RTT sample remain fresh for at least 250 ms; missing, stale,
  changed, weighted, or otherwise uncertain evidence keeps the 250 ms fallback.
  Contract, session, membership, source-roam, rebaseline, resequencer-replacement,
  and teardown transitions advance one receiver/topology generation before
  clearing evidence, so a delayed ACK completion or recovery-window publication
  from an older generation cannot restore the shorter hold. The authority
  carries that generation with its exact transition time and wakes the receive
  drainer through a coalescing notification. A conservative gap therefore
  remains bounded by `transitionAt+250ms`, including when the later explicit
  publication stalls; observation after that bound expires it immediately.
  An ordinary/bootstrap probe without a recovery ACK revokes any admitted or
  acknowledged recovery evidence once. When no such evidence exists, repeating
  those probes is idempotent and cannot move an already armed 250 ms deadline.
  Within one unchanged
  topology, ACK-venue and authenticated RTT/liveness changes receive ordered
  publication revisions only after their exact inputs are revalidated; an older
  refresh cannot erase a newer venue or restore a smaller RTT headroom. Each gap
  keeps the evidence snapshot and deadline it armed with, so same-topology
  updates affect only future gaps.
- **Metrics**: set `[metrics].listen = "127.0.0.1:9090"` (loopback only — a
  non-loopback bind is refused) and scrape `/metrics` for per-path loss, FEC
  recovery, throughput, probed RTT/liveness,
  `wanbond_path_probe_send_errors_total` (unexpected locally-originated ordinary
  and PMTU PROBE socket write failures; expected PMTU `EMSGSIZE` too-large
  verdicts are excluded; PMTU failures return to discovery, while ordinary
  failures are counted then discarded so other paths continue, D96), exact-byte shaper
  queue/backpressure, byte, priority-debt/bound, configuration, and asynchronous
  write-error series (`wanbond_path_shaper_*`) plus underlying
  `wanbond_path_socket_write_errors_total` (shaped/direct DATA, PARITY, and
  inner-control socket failures; generated outer PROBE and reflected-echo
  failures are excluded), active-backup closed-loop target/actual wire-rate,
  delivered-capacity, base-RTT/queue-delay, authenticated-loss freshness,
  installed-rate acknowledgment, retarget count/pending state, and
  carrier-epoch series (`wanbond_path_congestion_*`), exact Linux TUN-AQM
  rate/queue/GSO target/readback/freshness/epoch, live backlog/drop, and
  non-dropping shrink-deferral series
  (`wanbond_tun_aqm_*`), engine-side TUN/send batch histograms, outbound
  queue/active-send gauges, and exact-byte admission limit/retention/wait
  counters (`wanbond_engine_*`), WG-session establishment
  (`wanbond_session_established`, plus a per-peer
  `wanbond_peer_session_established{peer}` in multi-peer mode, T256; every peer,
  including the first configured one, carries its configured `peer` label, while
  a true single-peer exposition omits the label), receive-resequencer head-of-line holds vs
  single-path immediate releases (`wanbond_resequencer_hol_hold_seconds_total` /
  `wanbond_resequencer_immediate_releases_total`, D93), and — under
  `scheduler.policy = "weighted"`
  — a static `wanbond_weighted_capacity_sane` gauge that flags an unverifiable
  `link_bandwidth` declaration (see [docs/install.md
  §6b](docs/install.md#6b-weighted-policy-capacity-sanity-check-t144)) plus the
  aggregation-gate quartet `wanbond_aggregation_engaged` /
  `wanbond_offered_load_fps` / `wanbond_aggregation_{engage,disengage}_threshold_fps`
  (per-peer gauges showing whether data-thrift striping is engaged, the smoothed
  offered load driving it, and the static engage/disengage thresholds; absent
  under `active-backup`). Independent of policy, a static
  `wanbond_liveness_budget_sane` gauge flags a `[liveness]` `down_after` /
  per-path `ride_through` widened past the 3s transparent-failover recovery
  deadline (WARN-and-allow — it never blocks startup). When `[fec] enabled =
  true, adaptive = true`, four per-peer gauges expose the adaptive controller's
  live decision — `wanbond_fec_adaptive_parity` (current target parity M),
  `wanbond_fec_smoothed_loss` (its EWMA loss estimate),
  `wanbond_fec_eligible_path_loss` (the selected controller input — the maximum
  of fresh authenticated pre-recovery DATA loss and probe loss for one stable
  active-backup carrier, or the weight-weighted probe mix under striping), and
  `wanbond_fec_eligible_paths` (the count of those data paths; 0 while learned
  DATA feedback is stale or identity-mismatched and the controller holds) —
  absent entirely for a fixed-ratio or FEC-off peer.
- **Monitoring UI**: set `[monitor].listen = "127.0.0.1:9101"` for a
  live-updating dashboard (per-peer throughput/loss/FEC sparklines, pushed over
  a `/ws` WebSocket every 1s) — read-only except the loopback-only `POST
  /api/exit` control (which 403s on any non-loopback bind), exposed in the
  dashboard itself as an exit-switch `<select>` populated from the daemon's
  authoritative configured exit-capable peer set — loopback-only by default
  like `[metrics]`, but
  it MAY bind non-loopback if you also set `[monitor].token` (otherwise
  refused at config load). Every request, including the WebSocket upgrade, is
  Host/Origin-validated (DNS-rebinding/CSRF defense); a configured token is
  presented once as `?token=…` and then carried by a `SameSite=Strict`
  `HttpOnly` cookie. Reach it via `ssh -L 9101:127.0.0.1:9101 …` for the
  loopback case, or a token + non-loopback bind on a trusted LAN — the monitor
  has no TLS in v1, so a non-loopback bind trades in an explicitly accepted
  cleartext-token risk (see [docs/design.md §Security
  model](docs/design.md#security-model) and [docs/install.md
  §6c](docs/install.md#6c-monitoring-ui-monitor)). Beyond per-peer traffic/
  quality, the dashboard shows: the daemon's effective **role** (edge/
  concentrator), **version**, and process **uptime**; per-path **bind mode**
  (`source`/`device`/`auto`) plus the resolved **bound device**, and the
  operator-declared **link bandwidth/RTT**; the truncated WireGuard
  public-key **fingerprint** (never the full key — read-only identity
  disambiguation only); and, on any binding, an ordered **hub-endpoint
  failover list** with the active entry highlighted against its standbys.
  Per-path **source/remote addressing** (the bound local address and the
  current wire remote — on the concentrator role, a connected edge's observed
  source) and the endpoint list's **addresses** are the one REDACTABLE
  surface: they are shown in full when the monitor is ACTUALLY bound to
  loopback (verified against the kernel-bound listener address, not the
  configured `listen` string) OR the operator sets the default-off
  `reveal_addressing` opt-in; on any non-loopback binding by default —
  including a token-authorized one — they are redacted server-side and the
  dashboard renders an "addressing hidden on non-loopback binding" placeholder
  instead (unless `reveal_addressing` is explicitly set).
- **Logs**: structured, to stderr → `journalctl -u wanbond-…`; watch for the
  one-shot `"scheduler aggregation change"` record on every engage/disengage
  flip. With pacing enabled, encoded DATA and FEC parity now backpressure in a
  bounded exact-byte shaper instead of producing scheduler-shedding records.
- **Pacing on/off is a real tradeoff, not just a knob**: pacing applies bounded
  sender backpressure at a live outer target. Under
  active-backup, Linux owns a small `wanbond0` HTB+bounded-`fq` qdisc and adapts
  both the path shaper and TUN ingress rate from actual outer delivery, probe queue delay, measured
  encapsulation expansion, and fresh authenticated DATA loss; this prevents the
  embedded engine's 1,024-container peer queue from hiding congestion from AQM.
  `link_bandwidth` supplies the measured seed and optional
  `link_bandwidth_limit` supplies an operator ceiling.
  After any rate change, another controller decision waits for exact kernel
  readback and a one-second-or-one-SRTT settling interval. Mutable `fq`
  parameters change in place, preserving the live queue and counters; a limit
  shrink waits while the current packet count exceeds the new limit, GSO
  shrink waits for an empty TUN backlog, and engine admission shrink waits
  until every peer's retained bytes fit. Repeated exact readbacks of
  the unchanged target retain the first acknowledgment time, so reconciliation
  cannot perpetually restart that settling interval. The fair-queue limit admits
  one configured per-peer BDP plus one complete GSO batch at the 20-byte
  minimum legal inner-packet size. The TUN ptr ring holds that same logical
  service window plus the larger of a full device batch and one GSO-sized
  direct/dequeue burst; the HTB burst equals that GSO byte bound and is read
  back exactly. This prevents `FULL_RING` while the engine reader stalls for
  the bounded B+C ownership window. A longer arbitrary reader stall lies
  outside that invariant and may drop at the TUN driver; overload beyond the
  bounded `fq` window may tail-drop there and remains observable. Shaped FEC
  retains the same engine admission reservation until
  its owner batch terminally emits or fails, so ownership admission cannot
  duplicate that backlog behind the gate. Ring occupancy uses a non-consuming
  zero-time fd poll that does not contend with the engine's blocking TUN read,
  so an idle concentrator can complete startup reconciliation. Admission
  growth installs and reads back downstream ring/`fq` capacity before exposing
  the larger engine window. Admission shrink changes the engine first; if
  retained bytes defer it, the installed downstream target remains held until
  the engine change succeeds.
  Weighted scheduling retains fixed per-path shaping because no single
  authenticated carrier record represents simultaneous striping. Leaving pacing off maximizes
  offered throughput but risks bufferbloat-driven liveness flaps under
  sustained overload. The published throughput/RTT numbers predate T299's
  exact-byte shaper and must be rerun on real hardware — see
  [docs/design.md §Send-side scheduler](docs/design.md) for that historical
  evidence, the three-tier `ClassControl`/`frame.KindProbe`/`ClassData`
  priority model, why inner-tunnel traffic (e.g. inner ICMP) can never get its
  own priority lane, and the full operability runbook tying every signal
  above together.

## Testing

### Recovery contract and observability notation

The recovery path uses one notation across config, metrics, and the monitor:
`D=250ms`, dispatch grace `G=10ms`, lease lifetime `F=1200ms`, memory terms
`B/C/P/Fgroup/Lio/Mtotal`, rate terms `R/Rp/I`, sender service `Sdevice`,
receiver headroom `H=clamp(4*max(SRTT),10ms,D)` over qualified fresh DATA
carriers only, hold `W=min(D,A+H)`, and
completion bound `Ecompletion`. `SessionID` identifies a process epoch,
`ContractID` rotates a service offer within that epoch, and `OuterSeq` remains
continuous across same-process rotation. An exact authenticated `ACK` enables
the shorter hold only when `A+H<D`; absence, staleness, saturation, a
transition, or incompatible peers reports `W=D` with a bounded conservative
fallback reason. FEC geometry comes from authenticated contract/config
state—there is no zero-parity inference. A pacing-off single-owner FEC socket
enforces the same absolute `A=I=10ms` recovery cut directly and can therefore
offer finite service; shared multi-peer sockets remain disabled.

Prometheus and the monitor expose staging, decisions/deadlines, bounded
contract event/status/reason signals independently for outbound sender and
inbound receiver directions, recovery-window inputs, recovery cuts,
retained-memory high-water marks, and resequencer arm/wake/fill counters.
Prometheus uses the bounded `direction={sender,receiver}` label; monitor JSON
nests the same values under `recovery.sender` and `recovery.receiver`.

Three tiers (see [docs/design.md §Testing](docs/design.md) and
[docs/manual-checklist.md](docs/manual-checklist.md)):

| Tier | Command | What it covers |
|------|---------|----------------|
| frontend + unit / property | `just test` (TypeScript typecheck/Vitest + root and patched `amneziawg-go/device` tests) | monitor wire/UI contract, codec, FEC math, adaptive control law, anti-replay, schedulers, config |
| netns e2e | `just e2e` (`sudo -E go test -tags e2e ./test/e2e/...`) | two-netns tunnel bring-up, bonding, failover, FEC recovery, DPI audit (P0–P5) |
| netns e2e (device) | `sudo -E go test -tags e2e ./internal/device/` | the permanent D96 adaptive-FEC regressions against the real daemon under netem loss: single-path parity ramp (T266) + two-path active/standby anti-phase (T275); self-isolating (each re-execs into a fresh netns) |
| real-host e2e | `just realhosts` (`-tags realhosts`) | two real machines over the internet (NAT edge + public concentrator); report-only |

> **Important fixture limitation:** the netns fixture is CPU/PPS-bound, so it
> validates *functional* bonding/FEC/failover/DPI but **cannot** measure
> real-link throughput aggregation or bufferbloat. Those must be measured on real
> uplinks (manual-checklist §P0) before a production rollout.

## Repository layout

```
cmd/wanbond/            entry point; role selection; SIGHUP reload
internal/bind/          the custom conn.Bind — multipath fan-out/coalesce, the amnezia boundary
internal/frame/         outer bonding frame codec (obfuscation + optional HMAC)
internal/sched/         send-side scheduler (active-backup, weighted, pacing)
internal/shaper/        serialized per-(peer,path) exact-byte shaping and recovery cuts
internal/telemetry/     per-path PROBE/liveness, RTT/loss/jitter
internal/reseq/         receive resequencer (bounded-window reorder)
internal/fec/           Reed-Solomon FEC encoder/decoder
internal/adaptivefec/   closed-loop parity controller (target-residual SLA)
internal/config/        TOML load + fail-fast validation
internal/dnsresolve/    DNS resolution seam (Resolver interface, system + DoH + DoT impls, test fake)
internal/device/        tunnel lifecycle (Up/Down/Reload), metrics wiring
internal/metrics/       loopback Prometheus /metrics
internal/monitor/       monitoring-UI endpoint, read-only except the loopback-only POST /api/exit control (auth + /ws push + embedded frontend)
internal/wireaudit/     requirement-6 DPI wire-format audit tooling
internal/log/           structured logging wrapper
web/                    monitoring-UI frontend (Vite + TypeScript), built into internal/monitor/dist
third_party/amneziawg-go local v1.0.4 patches: per-Device protocol state (#155), engine queue/batch observability, test vet fix (#157)
test/e2e/               -tags e2e netns fixture (P0–P5)
test/realhosts/         -tags realhosts real-machine tier
docs/                   design, install, findings, manual checklist
```

## Status & limitations

The P0–P5 build is functionally complete, reviewed, and hardened. Known,
deliberate boundaries you must plan around:

- **Receiver DATA-loss feedback covers exactly one stable active-backup
  carrier.** It rides a separate feedback-only authenticated PROBE, is bound to
  the peer session, recovery ContractID, carrier path/generation, and monotonic
  report ID, and expires after two probe intervals. Recovery OFFERs and ACKs
  remain top-level legacy `WBRC` payloads, so a peer that parses `WBRC` directly
  can still negotiate recovery in either direction with a feedback-capable
  peer. When both probes share a cadence, feedback carries the lower probe
  sequence and `WBRC` the higher one, preserving the legacy peer's recovery
  evidence under ordered or reordered delivery. Stale, replayed, wrong-peer, or
  path-transition evidence cannot drive parity: only an accepted native DATA
  frame starts a carrier epoch, and final/recovered outcomes from before that
  sequence do not enter the new carrier's interval.
  A lower-loss same-epoch report cannot erase a still-fresh higher-loss report;
  the retained maximum keeps its original expiry, so cadence phase cannot hide
  loss or extend evidence indefinitely. While a lease renewal remains pending,
  both the still-valid acknowledged ContractID and pending renewal ContractID
  can authenticate feedback; old-lease expiry removes the former. After capability adoption,
  missing current evidence holds the existing controller state. Peers that never
  send DATA feedback retain probe-loss adaptation.
  Weighted multi-carrier scheduling deliberately retains its existing weighted
  probe-loss signal because one carrier record cannot represent simultaneous
  distribution shares.
- **Pacing is off by default (opt-in)** — enable it under `[scheduler]`
  (`pacing_enabled = true`) and supply a measured per-link
  `link_bandwidth` seed plus `link_rtt` (bandwidth-delay product). Pre-T299 pacing was
  measured on the report-only real-link tier; those measurements do not validate
  T299's replacement shaper, and the CPU/PPS-bound netns fixture cannot establish
  absolute throughput or bufferbloat. Under active-backup, the outer exact-byte
  shaper and early TUN AQM start conservatively, raise their targets above the
  measured seed while clean loaded samples support growth, and reduce promptly
  on queue delay or authenticated loss. `link_bandwidth_limit` is the distinct
  optional operator safety ceiling; omitting it leaves discovery uncapped.
  Legacy active-backup frame-rate knobs likewise seed the controller from
  `R=per_path_capacity_fps*1500` and infer RTT as `B/R`; they no longer impose
  a permanent rate cap.
  Weighted scheduling keeps `link_bandwidth` as its fixed shaping rate and does
  not accept `link_bandwidth_limit`.
  Pacing is **policy-independent** (defect D65): `pacing_enabled`,
  `link_bandwidth`, and `link_rtt` are meaningful — and configured with the
  SAME keys — under the **default `active-backup` policy** too, not only under
  `policy = "weighted"`; see [docs/design.md §Send-side
  scheduler](docs/design.md) for the per-path-vs-bottleneck sizing distinction.
  Config load derives the initial exact-byte shaper envelope: wire seed `Rseed`,
  maximum encoded datagram `Lmax`, DATA/PARITY budget `B>=Lmax`, inner-control
  reserve `C=Lmax`, retained generated-priority reserve `P=Pburst`, one owned
  FEC-group bound `Fgroup`, one writer-in-flight `Lio=Lmax`, and
  `Mtotal=B+C+P+Fgroup+Lio`. It also derives generated-priority rate `Rp`, finite
  receiver-observable recovery-cut bound `A=I`, and completion overrun
  `Ecompletion` at the controller's minimum possible service rate. Active-backup
  retargeting changes only future serialization/admission, recomputes
  `B=ceil(Rtarget*link_rtt)`, and leaves already-admitted deadlines immutable;
  a B shrink waits until retained DATA fits. It rejects
  an envelope whose maximum probe+echo rate
  consumes the whole link. Non-finite inputs and byte budgets that cannot fit
  the platform's integer byte-count domain are rejected before conversion; `Q`
  and `Mtotal` must fit `int`, and `Ecompletion` must fit `time.Duration`.
  `A=I=10ms` must remain below the conservative 250 ms receiver fallback. A
  sender-side queue prefix is excluded from `A`: it drains before the cut's
  first DATA can make a receiver gap observable. At runtime each
  `(peer,path)` owns one shaper. One engine `Send` selects one path. With FEC
  off, each input is classified, framed, and admitted in order. With FEC on, a
  per-peer owner stages one group until its size or exact deadline decision,
  then frames its DATA/PARITY; no open-group DATA is writer-visible. Each
  original `Send` publishes one owner command through the bounded mailbox,
  irrespective of its offload-frame count. With pacing enabled, or on an
  unshaped socket protected by an active exclusive direct-recovery contract,
  `Send` returns once the owner has copied/admitted every caller buffer. Thus
  successive serial sub-*K* engine calls can fill one group without waiting for
  its later decision or wire service; the owner retains a separate terminal
  completion for lifecycle and accounting. Uncontracted direct sends retain
  synchronous terminal completion. The Bind advertises and accepts the vendored engine's 128-buffer
  ideal batch, so a TUN-offloaded send reaches this path without an artificial
  single-buffer interface limit.
  Compatible shaped frames from one
  decided group that naturally uses one exclusive path transfers ownership to
  that path shaper as one recovery tranche. Mixed-path/shared-socket groups keep
  the conservative receiver fallback. An exact successfully written ACK for
  that service can shorten a stable active-backup receiver gap to `A` plus
  four times the fresh active DATA-carrier SRTT (10 ms floor, 250 ms cap); evidence
  uncertainty, path/source change, rebaseline, or weighted scheduling uses the
  full 250 ms. A buffered gap's deadline starts when its first successor becomes
  receiver-observable, even if an earlier gap still blocks it; a successor first
  observed later retains its own remaining recovery time. Encoded DATA and
  every decided FEC parity datagram
  consume their exact byte length. A recovery cut orders the retained
  lower-OuterSeq prefix and already-admitted priority, then the complete group
  DATA+parity tranche, before later priority/groups. One absolute
  `cutStart+10ms` socket deadline covers an already-blocked predecessor and
  every tranche syscall. Install, clear, or writer failure terminates without
  retry, assigns its originating cause to every already-accepted retired byte,
  disables admission immediately, and retires that exact socket
  generation from its peer path, scheduler view, and remote; a stale failure
  cannot match a replacement generation. A causally interrupted in-flight
  shaped syscall reports that published cause to the owner's terminal
  completion while socket/error counters retain the actual syscall failure
  class; a shaped caller already acknowledged at owned admission is not
  retroactively failed. The shaper retains
  at most `Mtotal`; saturation backpressures the sender
  without discarding ordinary traffic. Per-path accepted, emitted,
  shaper-error, and socket-error counters expose every terminal prefix. Live
  queue gauges prove `DATA<=B`, `control<=C`, `priority<=P`, one
  `Fgroup`, and total retained `<=Mtotal`; admission-wait
  counters distinguish bounded backpressure from cancellation. Accepted bytes
  linearize when queue capacity is reserved (including a pending-copy
  placeholder), so accepted, emitted, generic-error, `EMSGSIZE`, queue, and
  in-flight bytes reconcile at every live snapshot; both DATA/PARITY and inner
  control contribute. The shaper series are absent when pacing is off and
  restart from zero with each fresh shaper/socket generation.
  `wanbond_path_tx_bytes_total`, FEC DATA/PARITY emitted counters, and the
  peer's last-write timestamp advance only after the eventual socket write
  succeeds, never at the earlier caller-ownership acknowledgement.
  Runtime remove/rollback and Close atomically stop that generation's admission,
  retire queued work, close its socket to interrupt blocked kernel I/O, then join
  shaped and direct UDP writers without holding the bind lock. Re-add or Close/Open creates a fresh empty
  shaper/socket generation; no old writer can cross into it. Per-peer
  resequencer/FEC teardown cancels and joins the old FEC owner but retains the
  shaper while the underlying path socket remains live. Inner
  WireGuard handshake/cookie/keepalive frames use the `C=Lmax` reserve one
  buffer at a time, but retain selected-path FIFO, outer sequencing, and FEC;
  they never overtake lower DATA, and DATA cannot borrow `C`. Authenticated
  outer PROBE/echo frames reserve `P` and use the same serialized writer.
  Ordinary probes coalesce one cadence when `P` is full, PMTU admission waits
  cancellably, and reactive echoes fail non-blockingly with a distinct overflow
  counter, so none can bypass a recovery cut or block inbound dispatch.
  A local padded PMTU probe substitutes in the next eligible local probe slot
  instead of adding another producer at that cadence; the following slot is
  always ordinary liveness before another PMTU attempt, while a peer-requested
  reactive echo attempts non-blocking `P` admission immediately. PMTU reserves
  `P` before sequence allocation, timestamping, and echo registration in the
  selected slot, so admission and cadence waits do not inflate path RTT. Each
  search candidate remains an outer-IP MTU; the generated
  UDP payload subtracts the validated path family's header cost (28 bytes for IPv4,
  48 for IPv6), matching that path's `Lmax` and exact debit. The configured
  `Pburst=2*Lmax` therefore covers one maximum local probe (ordinary or PMTU) plus
  one maximum echo. An unexpected PMTU writer failure preserves its error and
  increments the probe-send-error counter; `EMSGSIZE` remains the expected
  too-large verdict. Distinct
  `probe_priority_coalesced`, `pmtu_admission_canceled`, and
  `echo_priority_overflow` counters expose bounded-admission outcomes.
  For existing priority debt `P0`, a coincident `Pburst`, and sustained
  generated priority bounded by `Rp<R`, admission is bounded by
  `Dp=(P0+Pburst)/(R-Rp)`, not `P0/R`; after admission, local egress adds at
  most `Q/R+Lmax/R`, so call-to-receiver delivery is bounded by
  `Dp+Q/R+Lmax/R` plus the active resequencer hold.
  Priority arrivals in the half-open interval `[call, call+Dp)` update the
  registered waiter's deadline under the shaper lock, even if that waiter does
  not run until the former deadline; once the deadline matures, an exact-boundary
  debit changes only future admission and cannot revoke the waiting call's
  eligibility.
  Sustained authenticated generated outer-priority traffic beyond the declared
  `Rp`/`Pburst` model constitutes overload; no live CONTROL protocol exists.
- **Throughput aggregation and bufferbloat are not measured by the netns fixture**
  (it is CPU-bound) — the report-only real-link tier (`just p0-baseline`) measures
  them instead; validate on your own uplinks before a production rollout.
- **No live CONTROL protocol** — the frame type and its anti-replay guard exist,
  but inbound CONTROL is currently dropped (reserved for future signalling).
- **Multi-concentrator hub-failover: built and validated** — an
  edge peer may declare an ORDERED `endpoints` list (active concentrator + ordered
  standbys); the single `endpoint` form still works unchanged (its one-element
  case, which takes no failover action). On HUB LOSS (every path to the active
  concentrator down at once) the edge advances to the next endpoint, repoints the
  bond, and re-handshakes a fresh session (round-robin/wrap at end of list). The
  switch is covered by unit/component tests, the netns hub-failover e2e (T62), and
  the real-link mid-transfer WAN-kill tier (T63). Endpoints may be IP:port
  literals (default) or hostnames with per-peer opt-in `dns = true`; the
  `[dns]` resolver block is OPTIONAL — an absent block defaults to the system
  resolver — and only selects the transport (system/DoH/DoT) that opt-in
  uses; see
  [docs/design.md §DNS endpoints and resolver privacy trade-offs](docs/design.md).
- **Multi-concentrator edge: N warm bonds brought up concurrently (G28)** — the
  edge may declare **several concentrator peers** and bond to all of them over the
  same uplinks. Each `mode = "default-route"` peer is an exit-capable alternate for
  the full-tunnel egress (the first in config order is the boot-default; the
  selection does not persist). Config load enforces the multi-exit invariants —
  matching default-route sets across exit peers, a mandatory non-default inner
  `/32` per exit peer, non-overlapping non-default allowed_ips, distinct
  per-peer endpoints, per-peer name/psk, one shared scheduler/FEC/reseq policy
  block, and an `N × U` probe-fan-out budget (≤ 32 probers). `device.Up` brings
  **all N peers up warm concurrently** (T251): each peer's configured endpoint is
  routed to ITS concentrator (distinct per-peer virtual endpoint + per-path
  remotes), every peer gets a persistent keepalive and a first-path-up handshake
  so all sessions stay warm, and the concentrator-role dead-peer reclaim never
  tears down a healthy edge warm-standby. **Per-peer hub-failover / DNS
  re-resolution is per-concentrator (T253):** every eligible peer gets its OWN
  controller over its OWN prober set and repoints only its OWN remote through the
  T252 per-peer seam, so one exit's failover cannot disturb another's (defect D100
  fixed). Each per-peer controller also raises an endpoint-list-exhaustion signal
  for the cross-concentrator exit selector (T269). **The active-exit selector
  (T254)** owns WHICH exit-capable peer carries the default route: the first
  `mode = "default-route"` peer boots owning the wg-quick `/1`+`/1` split while the
  other exit peers boot as **warm standbys** carrying only their inner `/32`;
  switching the active exit repoints the split onto the target peer in the engine's
  allowed-ips trie (WireGuard's steal-on-insert moves ownership atomically per
  prefix, no re-handshake — the standby session is already warm) with kernel routes
  untouched. **Auto-promotion (T269)** moves egress off a FULLY-failed active exit
  (its endpoint list exhausted — every endpoint tried and down, distinct from
  within-concentrator failover) onto the first healthy warm standby, logged with
  `reason=auto-promotion`; a manual switch always wins and there is no auto-failback
  onto a recovered exit. Per-concentrator stats are grouped per-peer on the
  monitor dashboard, and on-the-fly exit switching is exposed there through a
  loopback-only exit-switch widget (T259/T260, G28/M107; see
  [docs/design.md §Security model](docs/design.md)).
  See [docs/install.md §Multi-concentrator edge](docs/install.md).
- **UDP only** — obfuscation defeats DPI *classification*, not a wholesale UDP
  block; there is no TCP/TLS fallback.
- **DATA/PARITY frames are unauthenticated by design** (inner WireGuard
  authenticates the real payload; a DoS-grade forgery risk is accepted). PROBE
  and CONTROL are PSK-HMAC authenticated.
- **Per-path `mtu` is declared but not yet sized into the tunnel** — an
  optional per-path `mtu` config key lets an operator declare that path's real
  outer underlay MTU (e.g. a cellular APN capped below 1500); it is validated
  at config load (1280..9000, derived inner MTU `>= 576`) but not yet consumed
  when sizing the TUN's inner MTU (see [docs/install.md §7](docs/install.md)).

See [docs/design.md §Security model](docs/design.md) and
[docs/p0-findings.md](docs/p0-findings.md) for the reasoning behind each.

## Documentation

- **[docs/design.md](docs/design.md)** — architecture and exactly what we built on
  top of amneziawg-go.
- **[docs/install.md](docs/install.md)** — full setup and operation (per-topic
  reference); §3z is the exhaustive all-keys config reference.
- **[wanbond.example.toml](wanbond.example.toml)** — copy-pasteable annotated
  example config with every key, its default, and its constraints.
- **[docs/runbook.md](docs/runbook.md)** — pre-pilot rollout runbook: provision a
  fresh edge + concentrator (+ standby) from scratch, end to end.
- **[docs/manual-checklist.md](docs/manual-checklist.md)** — manual per-phase and
  real-link verification checklist.
- **[docs/p0-findings.md](docs/p0-findings.md)** / **[docs/p0-checkpoint.md](docs/p0-checkpoint.md)**
  — the P0 spike findings that fixed several load-bearing design decisions
  (single virtual endpoint, resequencing, why the fixture is CPU-bound).
- **[AGENTS.md](AGENTS.md)** — instructions for AI agents working in this repo
  (including the rule to keep these docs in sync with the code).
