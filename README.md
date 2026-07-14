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
5. **Adaptive FEC** — redundancy tracks measured per-path loss (target a residual
   loss SLA, not a fixed overhead).
6. **DPI resistance** — the outer wire is unidentifiable high-entropy UDP: no
   WireGuard fingerprint, no magic bytes (nDPI/Suricata do not classify it as
   VPN/WireGuard).

## How it works, in one paragraph

wanbond embeds the **unmodified** [amneziawg-go](https://github.com/amnezia-vpn/amneziawg-go)
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

Requires the dev shell (`nix develop`) which puts Go 1.26, golangci-lint, and the
netem/DPI test tooling on `PATH`.

```sh
just build          # go build ./...
just test           # unprivileged unit/property tests
just lint           # go vet + golangci-lint (incl. -tags e2e / -tags realhosts)
just release        # static linux amd64+arm64 binaries into dist/
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

   # optional: [amnezia] (obfuscation, all-or-nothing), [fec], [scheduler], [dns], [metrics], [log]
   ```

3. Install the systemd unit for the role
   (`packaging/systemd/wanbond-{edge,concentrator}.service`), `daemon-reload`,
   `enable --now`.
4. On the **concentrator**, allow the tunnel interface through the firewall
   *ahead of* any default REJECT, and persist it across reboots (see install.md;
   `just realhosts-provision` automates the standing-testbed case).

## Operating it

- **Live reload**: `systemctl reload wanbond-…` (SIGHUP) re-reads the config and
  adds/removes paths without tearing the tunnel down.
- **Metrics**: set `[metrics].listen = "127.0.0.1:9090"` (loopback only — a
  non-loopback bind is refused) and scrape `/metrics` for per-path loss, FEC
  recovery, throughput, probed RTT/liveness, WG-session establishment
  (`wanbond_session_established`), and — under `scheduler.policy = "weighted"`
  — a static `wanbond_weighted_capacity_sane` gauge that flags an unverifiable
  `link_bandwidth` declaration (see [docs/install.md
  §6b](docs/install.md#6b-weighted-policy-capacity-sanity-check-t144)) plus the
  aggregation-gate quartet `wanbond_aggregation_engaged` /
  `wanbond_offered_load_fps` / `wanbond_aggregation_{engage,disengage}_threshold_fps`
  (per-peer gauges showing whether data-thrift striping is engaged, the smoothed
  offered load driving it, and the static engage/disengage thresholds; absent
  under `active-backup`).
- **Logs**: structured, to stderr → `journalctl -u wanbond-…`.

## Testing

Three tiers (see [docs/design.md §Testing](docs/design.md) and
[docs/manual-checklist.md](docs/manual-checklist.md)):

| Tier | Command | What it covers |
|------|---------|----------------|
| unit / property | `just test` (`go test ./...`) | codec, FEC math, adaptive control law, anti-replay, schedulers, config |
| netns e2e | `just e2e` (`sudo -E go test -tags e2e ./test/e2e/...`) | two-netns tunnel bring-up, bonding, failover, FEC recovery, DPI audit (P0–P5) |
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
internal/telemetry/     per-path PROBE/liveness, RTT/loss/jitter
internal/reseq/         receive resequencer (bounded-window reorder)
internal/fec/           Reed-Solomon FEC encoder/decoder
internal/adaptivefec/   closed-loop parity controller (target-residual SLA)
internal/config/        TOML load + fail-fast validation
internal/dnsresolve/    DNS resolution seam (Resolver interface, system + DoH + DoT impls, test fake)
internal/device/        tunnel lifecycle (Up/Down/Reload), metrics wiring
internal/metrics/       loopback Prometheus /metrics
internal/wireaudit/     requirement-6 DPI wire-format audit tooling
internal/log/           structured logging wrapper
test/e2e/               -tags e2e netns fixture (P0–P5)
test/realhosts/         -tags realhosts real-machine tier
docs/                   design, install, findings, manual checklist
```

## Status & limitations

The P0–P5 build is functionally complete, reviewed, and hardened. Known,
deliberate boundaries you must plan around:

- **Pacing is off by default (opt-in)** — enable it under `[scheduler]`
  (`pacing_enabled = true`) and size it from operator-declared per-link
  `link_bandwidth`/`link_rtt` (bandwidth-delay product); it was measured to
  eliminate bufferbloat on the bandwidth-capped netns fixture and the real-link
  tier. wanbond fixes the pace at config load and does not auto-tune it live (Q20).
  Pacing is **policy-independent** (defect D65): `pacing_enabled`,
  `link_bandwidth`, and `link_rtt` are meaningful — and configured with the
  SAME keys — under the **default `active-backup` policy** too, not only under
  `policy = "weighted"`; see [docs/design.md §Send-side
  scheduler](docs/design.md) for the per-path-vs-bottleneck sizing distinction.
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
- **UDP only** — obfuscation defeats DPI *classification*, not a wholesale UDP
  block; there is no TCP/TLS fallback.
- **DATA/PARITY frames are unauthenticated by design** (inner WireGuard
  authenticates the real payload; a DoS-grade forgery risk is accepted). PROBE
  and CONTROL are PSK-HMAC authenticated.

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
