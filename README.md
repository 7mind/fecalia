# wanbond

**wanbond** bonds two (or more) unreliable, heterogeneous WAN uplinks — e.g. a
low-latency-but-jittery Starlink link and a stable 4G/5G link — into a single
DPI-resistant WireGuard tunnel for general IP traffic. It uses every uplink
under load, repairs loss by bounded retransmission, and keeps small real-time
datagrams ahead of bulk.

It is a single self-contained Go binary that runs on both ends of the tunnel:

- **edge** — a Linux box (behind a router) that bonds the local WAN uplinks;
- **concentrator** — a small public-IP VPS that terminates the tunnel and NATs
  traffic onward. Supports multiple edges (multi-peer mode); with more than
  one edge, each authenticates with its own per-peer PSK (a single edge uses
  the top-level PSK).

The same binary serves both roles; the role is chosen from the config file.

## What it gives you

1. **Transparent failover** — a TCP flow survives a WAN dying mid-session, with
   no reset: the WireGuard engine sees one stable endpoint per peer, and the
   transport stops using a lane whose acknowledgements stall or whose
   authenticated hello lapses, and repairs its outstanding datagrams over
   another lane.
2. **Aggregation** — under load, traffic is sent over every usable lane, each
   paced at the capacity the transport measured for it. No bandwidth figures
   are configured.
3. **Bounded loss repair** — a datagram not confirmed within the repair timer
   is sent again, preferably over another lane, at most four times and within
   250 ms; nothing is retransmitted after that.
4. **Priority for small datagrams** — encrypted datagrams of at most 384 bytes
   are served ahead of bulk; those that are not TCP (voice, DNS) go first, to
   the lane with the lowest latency, and may be copied onto a second lane
   within a rate budget.
5. **DPI resistance** — the outer wire is unidentifiable high-entropy UDP: no
   WireGuard fingerprint, no magic bytes (nDPI/Suricata do not classify it as
   VPN/WireGuard).

## How it works, in one paragraph

wanbond embeds a locally patched [amneziawg-go](https://github.com/amnezia-vpn/amneziawg-go)
WireGuard engine (TUN, Noise handshake, AEAD, rekey, roaming, keepalive) and puts
**all** bonding logic — the multipath transport, an obfuscated and authenticated
outer frame codec, a receive resequencer, and per-path telemetry — into a custom
`conn.Bind` that lives *beneath* the engine and operates only on opaque, already-
encrypted WireGuard datagrams. The engine sees one stable virtual endpoint per
peer; the Bind privately fans traffic out across the real per-path UDP sockets
and, on a concentrator with multiple edges, demuxes inbound traffic to the
owning peer from PROBE frames authenticated under that peer's own PSK. For the
full picture and the exact list of what we built on top of amneziawg-go, read
**[docs/design.md](docs/design.md)**.

### The transport

There is one transport (`internal/bond`), and both ends must run it; there is
nothing to select. `[scheduler] policy = "adaptive"` is accepted and is the
default. A build that still runs a removed policy (`active-backup`, `weighted`)
cannot exchange tunnel data with this one — see the
[upgrade note](docs/install.md#upgrading-from-a-build-with-the-removed-transports).

The transport learns each authenticated return path, including multiple edge
WANs behind one concentrator socket. It measures delivery and forward queue
delay, paces each direction independently, and uses every available path under
load. Idle feedback, including lost empty keepalives, does not lower the pacing
target. Loaded feedback uses a minimum
over each control interval, with a jitter allowance learned while the lane is
idle, to separate propagation variation from sustained queueing. The queue
allowance excludes return-path jitter; the window uses unloaded RTT history.
The in-flight window and repair timer also account for RTT variation. Loss
is measured from the receiver's byte counts, not from timeouts, so late or
reordered datagrams are not taken for lost ones. Delay that persists on a link
holding below its capacity first pauses bulk on it briefly and measures the
link's latency floor again, so a latency that moved to a higher level is not
taken for a queue. Loss
response caps pacing just below measured delivery, since a path that drops
instead of queueing delivers its capacity while it drops the rest. A link that
serves nothing for a moment and then delivers what it held is taken for
stalled, not congested: its delay ends no discovery and lowers no estimate,
and repairs are not sent into its silence. Metrics
distinguish the pacing target from actual send and delivery rates. The repair
timer also measures full delivery-confirmation time so reordered packets do not
trigger premature retries.
Small-packet duplication follows learned capacity, capped at 20% of the aggregate
pacing target and 64 kB/s; the second lane reserves capacity for the copies. Bulk receive batching reduces return ACK traffic and
flushes immediately for interactive packets.

Lost datagrams receive bounded cross-path retries. Bulk repair lasts at most
250 ms from first transmission; small packets retain a 250 ms limit from enqueue.
Retries do not extend either deadline. Small encrypted datagrams
(up to 384 bytes, including WireGuard overhead) receive priority and budgeted
replication; they bypass bulk receive ordering. Small datagrams that are not
TCP (voice, DNS) are served first and get the lowest-latency link, small TCP
datagrams (ACKs) next, bulk last; each lower class keeps a minimum share. This
is a size and protocol heuristic, not application identification. Within a
class each IP flow gets one turn at a time. Between capacity probes each link's
target holds just below the capacity it demonstrated, so the queue forms in
the tunnel, where voice has priority, and not in the modem. Unsent redundant pure TCP
ACKs can be coalesced while retaining the latest advertised window and preserving
window-only updates and other control information. Flow metadata is
local to the sender and does not change the wire protocol. Bulk datagrams remain resequenced. See the
[transport design](docs/design.md#adaptive-transport--internalbond) and
[autonomous VM lab](test/vm/README.md).

Throughput and continuity targets are not all met: the recorded measurements
and failures are in the [lab report](test/vm/README.md), the open items in the
[improvement plan](docs/drafts/20261001-0820-wanbond-improvement-plan.md).

On Linux the daemon needs `tc` (iproute2) at startup: it inspects the queue
discipline of `wanbond0` and removes the HTB/`bfifo` rate cap an older build
may have left on a persistent interface.

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
   psk  = "…"                       # outer control/probe PSK (not the WG PSK)

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
   endpoint    = "203.0.113.7:51820"   # edge only; concentrator learns edges
   allowed_ips = ["10.10.0.0/24"]

   # optional: [amnezia] (obfuscation, all-or-nothing), [dns], [liveness], [metrics], [monitor], [log]
   ```

3. Install the systemd unit for the role
   (`packaging/systemd/wanbond-{edge,concentrator}.service`), `daemon-reload`,
   `enable --now`.
4. On the **concentrator**, allow the tunnel interface through the firewall
   *ahead of* any default REJECT, and persist it across reboots (see install.md;
   `just realhosts-provision` automates the standing-testbed case).

## Operating it

- **Live reload**: `systemctl reload wanbond-…` (SIGHUP) re-reads the config and
  adds/removes paths without tearing the tunnel down, and rebinds the
  `[metrics]`/`[monitor]` endpoints. Every other change (keys, PSK, `[amnezia]`,
  `[dns]`, `[liveness]`, `tun_persist`, bind mode, a changed address on an
  existing path) is logged as ignored until restart; the running configuration
  is kept.
- **Metrics**: set `[metrics].listen = "127.0.0.1:9090"` (loopback only — a
  non-loopback bind is refused) and scrape `/metrics` for:
  per-path probed liveness, RTT, jitter and loss, byte counters, throughput and
  discovered MTU (`wanbond_path_*`), including
  `wanbond_path_probe_send_errors_total` (unexpected write failures of locally
  originated PROBE frames; an expected PMTU `EMSGSIZE` verdict is excluded) and
  `wanbond_path_socket_write_errors_total` (the transport's datagram writes the
  socket refused); per-lane transport state — pacing target, send and delivery
  rate, round trips, queue delay, window, repairs, discovery and eligibility —
  and per-peer queue drops and expired repairs (`wanbond_adaptive_*`);
  receive-resequencer releases, drops, skips and head-of-line holds
  (`wanbond_resequencer_*`); engine-side TUN/send batch histograms and outbound
  queue gauges (`wanbond_engine_*`); WG-session establishment
  (`wanbond_session_established`, plus `wanbond_peer_session_established`,
  labelled `peer` once two or more peers are bound; a single-peer exposition
  omits the label); the current TUN MTU (`wanbond_tun_mtu`); and a static
  `wanbond_liveness_budget_sane` gauge that flags a `[liveness]` `down_after` /
  per-path `ride_through` widened past the 3 s recovery deadline
  (WARN-and-allow — it never blocks startup).
- **Monitoring UI**: set `[monitor].listen = "127.0.0.1:9101"` for a
  live-updating dashboard (per-peer throughput/loss sparklines, pushed over
  a `/ws` WebSocket every 1s). A lanes panel shows what the transport is
  doing on each path: target, sent, the capacity it has demonstrated, queue
  delay against the lane's threshold, and what its control concluded. The compact dashboard automatically follows the
  system light/dark theme, shows overall WG-session status in the top bar,
  and places each peer's session and handshake age beside its name.
  The exit-selection control offers `auto` and
  each configured exit, and shows the active exit separately. `auto` is the
  edge config default: it selects the healthy exit with the lowest RTT on any
  up uplink, with a five-minute cooldown between RTT-driven switches. The
  control is available on loopback or a token-authenticated non-loopback bind — loopback-only by default
  like `[metrics]`, but
  it MAY bind non-loopback if you also set `[monitor].token` (otherwise
  refused at config load). Every request, including the WebSocket upgrade, is
  Host/Origin-validated (DNS-rebinding/CSRF defense); a configured token is
  presented once as `?token=…` and then carried by a `SameSite=Strict`
  `HttpOnly` cookie. URL-encode the token in the query, especially `+` and `/`.
  When using a DNS name with a wildcard `listen`, add that name to
  `[monitor].allowed_hosts`; unlisted Host and Origin values are rejected.
  The Nix package embeds the built dashboard assets. Reach it via
  `ssh -L 9101:127.0.0.1:9101 …` for the
  loopback case, or a token + non-loopback bind on a trusted LAN — the monitor
  has no TLS in v1, so a non-loopback bind trades in an explicitly accepted
  cleartext-token risk (see [docs/design.md §Security
  model](docs/design.md#security-model) and [docs/install.md
  §6c](docs/install.md#6c-monitoring-ui-monitor)). Beyond per-peer traffic/
  quality, the dashboard shows: the daemon's effective **role** (edge/
  concentrator), **version**, and process **uptime**; per-path **bind mode**
  (`source`/`device`/`auto`) plus the resolved **bound device**; the truncated WireGuard
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
- **Terminal monitor**: `sudo wanbond monitor` shows the same live snapshot on
  edge and concentrator hosts when `[monitor].listen` is enabled. It discovers
  the rendered `/run/wanbond/{edge,concentrator}.toml` config, reads its token,
  and connects locally. Terminal colors highlight headings and connection states
  when `TERM` supports them; `--no-color` or `NO_COLOR` disables colors.
  `sudo wanbond monitor --once` prints one snapshot (plain when redirected);
  use `--config PATH` for a different config location. This view is read-only.
  `wanbond --help` lists all subcommands.
- **Exit switching from the shell**: `sudo wanbond set-exit <exit-peer>` pins
  the default route to one exit peer on a running multi-exit edge;
  `sudo wanbond set-exit auto` returns to RTT-driven selection. It discovers
  the config like `wanbond monitor` and issues the same authenticated
  `POST /api/exit` as the dashboard's exit selector, so it needs
  `[monitor].listen` enabled. The override is runtime-only; a restart restores
  the config's top-level `exit`.
- **Logs**: structured, to stderr → `journalctl -u wanbond-…`.

## Testing

The [KVM lab](test/vm/README.md) runs actual edge/concentrator daemons and independent
HTB/netem WANs, with capacity calibration, bidirectional TCP, voice-like UDP,
capacity changes and temporary outages. Guest disks and per-candidate results
are retained for comparison without production deployments.

Three tiers (see [docs/manual-checklist.md](docs/manual-checklist.md)):

| Tier | Command | What it covers |
|------|---------|----------------|
| frontend + unit / property | `just test` (TypeScript typecheck/Vitest + root and patched `amneziawg-go/device` tests) | monitor wire/UI contract, codec, transport control law and delivery contract, anti-replay, resequencer, config |
| netns e2e | `just e2e` (`sudo -E go test -tags e2e ./test/e2e/...`) | two-netns tunnel bring-up, bonding, failover, DPI audit |
| netns e2e (device) | `sudo -E go test -tags e2e ./internal/device/` | privileged device-layer checks against a real kernel, e.g. the startup removal of an obsolete TUN shaper |
| real-host e2e | `just realhosts` (`-tags realhosts`) | two real machines over the internet (NAT edge + public concentrator); report-only |

> **Important fixture limitation:** the netns fixture is CPU/PPS-bound, so it
> validates *functional* bonding/failover/DPI but **cannot** measure
> real-link throughput aggregation or bufferbloat. Those are measured in the
> KVM lab and on real uplinks before a production rollout.

## Repository layout

```
cmd/wanbond/            entry point; role selection; SIGHUP reload
internal/bind/          the custom conn.Bind — per-path sockets, peer demux, probes, the amnezia boundary
internal/bond/          the transport: lanes, capacity discovery, pacing, traffic classes, repair, ACKs
internal/frame/         outer frame codec (obfuscation + HMAC authentication)
internal/telemetry/     per-path PROBE/liveness, RTT/loss/jitter, PMTU discovery
internal/reseq/         receive resequencer (bounded-window reorder)
internal/config/        TOML load + fail-fast validation
internal/dnsresolve/    DNS resolution seam (Resolver interface, system + DoH + DoT impls, test fake)
internal/device/        tunnel lifecycle (Up/Down/Reload), metrics wiring
internal/metrics/       loopback Prometheus /metrics
internal/monitor/       monitoring-UI endpoint, read-only except authenticated POST /api/exit (auth + /ws push + embedded frontend)
internal/wireaudit/     requirement-6 DPI wire-format audit tooling
internal/log/           structured logging wrapper
web/                    monitoring-UI frontend (Vite + TypeScript), built into internal/monitor/dist
third_party/amneziawg-go AmneziaWG engine v3.1.20260828 + local patches: flow metadata, outbound observability, S4 read rebase (#169), test vet fix (#157), 131008-message anti-replay window
test/e2e/               -tags e2e netns fixture
test/realhosts/         -tags realhosts real-machine tier
test/vm/                KVM lab: real daemons over shaped WANs
docs/                   design, install, findings, manual checklist
```

## Status & limitations

Known, deliberate boundaries you must plan around:

- **Both ends must run this transport.** Its data and acknowledgements travel
  in authenticated CONTROL frames after a hello exchange; a build running a
  removed policy sends frame kinds this build rejects, so the two cannot carry
  tunnel data, although their probes may still report the paths up. Upgrade
  both ends together and remove the retired configuration keys first
  ([docs/install.md, upgrade note](docs/install.md#upgrading-from-a-build-with-the-removed-transports)).
- **No metered-link conservation.** The transport has no notion of a preferred
  or metered uplink: under load it sends on every usable lane, it copies some
  small traffic onto a second lane, and every lane carries probes and
  keepalives while idle.
- **No forward error correction.** Loss is repaired by bounded retransmission
  and, for small real-time datagrams, by replication; a datagram that is not
  repaired within 250 ms is left to the inner protocol.
- **Tested envelope.** Local simulation and the VM lab validate recorded
  profiles, not every RF link or packet rate.
- **Throughput aggregation and bufferbloat are not measured by the netns fixture**
  (it is CPU-bound) — the report-only real-link tier (`just p0-baseline`) measures
  them instead; validate on your own uplinks before a production rollout.
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
  per-peer endpoints, per-peer name/psk, and an `N × U` probe-fan-out budget (≤ 32 probers). `device.Up` brings
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
  `reason=auto-promotion`. If the standby session becomes healthy only after
  exhaustion, promotion retries at the probe cadence while the active exit stays
  exhausted; selecting a fixed exit suspends RTT-driven switches, while selecting
  `auto` resumes them. Per-concentrator stats are grouped per-peer on the
  monitor dashboard, and on-the-fly exit switching is exposed there through a
  token-authenticated exit-switch widget and `wanbond set-exit` (T259/T260, G28/M107; see
  [docs/design.md §Security model](docs/design.md)).
  See [docs/install.md §Multi-concentrator edge](docs/install.md).
- **UDP only** — obfuscation defeats DPI *classification*, not a wholesale UDP
  block; there is no TCP/TLS fallback.
- **The outer wire is authenticated, not encrypted for confidentiality.** Every
  outer frame (PROBE and CONTROL, which carries the transport's data and
  acknowledgements) is PSK-HMAC authenticated; the payload's confidentiality
  and integrity come from the inner WireGuard session.
- **Per-path MTU** — a path that omits `mtu` is PMTU-discovered and `wanbond0`
  is resized to the smallest inner MTU across the up paths; an explicit `mtu`
  (1280..9000, derived inner MTU `>= 576`) pins the path. See
  [docs/p1-mtu.md](docs/p1-mtu.md).

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
