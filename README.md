# wanbond

**wanbond** bonds two (or more) unreliable, heterogeneous WAN uplinks — e.g. a
low-latency-but-jittery Starlink link and a stable-but-metered 4G/5G link — into
a single resilient, DPI-resistant WireGuard tunnel for general IP traffic, with
adaptive forward error correction (FEC) that masks per-path loss without
duplicating every packet.

It is a single self-contained Go binary that runs on both ends of the tunnel:

- **edge** — a Linux box (behind a router) that bonds the local WAN uplinks;
- **concentrator** — a small public-IP VPS that terminates the tunnel and NATs
  traffic onward.

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
encrypted WireGuard datagrams. The engine sees one stable virtual endpoint; the
Bind privately fans traffic out across the real per-path UDP sockets. For the
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
covered end-to-end in **[docs/install.md](docs/install.md)**. The short version:

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

   # optional: [amnezia] (obfuscation, all-or-nothing), [fec], [scheduler], [metrics], [log]
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
  recovery, throughput, and probed RTT/liveness.
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

- **Pacing is off by default and not empirically sized** — enable + tune from a
  measured bandwidth-delay product if your links have deep buffers (Starlink/5G
  do).
- **Throughput aggregation and bufferbloat are unmeasured in-fixture** — validate
  on real uplinks first.
- **No live CONTROL protocol** — the frame type and its anti-replay guard exist,
  but inbound CONTROL is currently dropped (reserved for future signalling).
- **Single concentrator** — no tunnel-termination failover; edge multipathing is
  the redundancy.
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
- **[docs/install.md](docs/install.md)** — full setup and operation.
- **[docs/manual-checklist.md](docs/manual-checklist.md)** — manual per-phase and
  real-link verification checklist.
- **[docs/p0-findings.md](docs/p0-findings.md)** / **[docs/p0-checkpoint.md](docs/p0-checkpoint.md)**
  — the P0 spike findings that fixed several load-bearing design decisions
  (single virtual endpoint, resequencing, why the fixture is CPU-bound).
- **[AGENTS.md](AGENTS.md)** — instructions for AI agents working in this repo
  (including the rule to keep these docs in sync with the code).
