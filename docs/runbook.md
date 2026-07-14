# wanbond — pre-pilot rollout runbook

This runbook provisions a fresh wanbond deployment **from scratch**: one
**edge** (the box bonding the WAN uplinks), one **concentrator** (the public-IP
VPS terminating the tunnel), and — optionally — a **standby concentrator** for
hub failover. Follow it top to bottom; each step is a concrete command.

It does **not** restate the config-key catalogue or the pacing-tuning
procedure. Those live in [docs/install.md](install.md):

- the annotated **Edge config** / **Concentrator config** blocks and the
  optional `[fec]` / `[scheduler]` blocks — [install.md §3 *Write the config
  file*](install.md);
- **§3a *Tuning per-link bandwidth and pacing*** — the full measure-and-enter
  procedure for `link_bandwidth` / `link_rtt` — [install.md §3a](install.md);
- **§4 systemd units**, **§5 Firewall**, **§6 Observability** — [install.md
  §4–6](install.md).

The concentrator hub-failover design (why a standby works with no state
handoff) is [docs/design.md §Concentrator hub failover](design.md).

## 0. Prerequisites

- The `wanbond` binary is installed on **both** hosts (`/usr/local/bin/wanbond`)
  and `wanbond version` prints a build — see [install.md §1–2](install.md).
- `wg` (the `wireguard-tools` package) is available on the machine where you
  generate keys — it need not be the deployment host.
- The concentrator has a reachable public IP and a UDP port you control
  (`listen_port`; `51820` below, but prefer a non-registered high port on
  hostile networks — see [install.md §8](install.md)).
- You can edit `/etc/wanbond/*.toml` as root on each host.

Inner tunnel addressing used throughout this runbook (operator-owned; assign it
with systemd-networkd per [install.md §4](install.md)):

| Host              | Inner address  |
|-------------------|----------------|
| concentrator      | `10.77.0.1/24` |
| edge              | `10.77.0.2/24` |

## 1. Generate the keypairs and the PSK

wanbond uses **two** independent secrets:

- a **WireGuard static keypair per host** — the inner Noise identity, consumed
  by the embedded amneziawg-go engine;
- one **outer-control PSK, shared by both ends** — a 32-byte base64 secret that
  keys the **PROBE/CONTROL HMAC** on the bonding frames (it is *not* the
  WireGuard peer PSK). Both ends must carry the byte-identical value.

Generate a WireGuard keypair on each host (private key stays local; the public
key goes into the *other* end's peer block):

```sh
# Run once per host. tee writes the private key; wg pubkey derives the public.
wg genkey | tee privatekey | wg pubkey > publickey
cat privatekey   # -> [wireguard].private_key on THIS host
cat publickey    # -> [[wireguard.peers]].public_key on the OTHER host
```

Generate the shared PSK **once** and copy the same value to both configs:

```sh
head -c 32 /dev/urandom | base64   # -> psk on BOTH the edge and the concentrator
```

**0600 is mandatory.** The config holds the WireGuard private key and the PSK,
so `config.Load` refuses any file whose mode is not exactly `0600` (`insecure
permissions` at startup). Create each file locked down before writing secrets:

```sh
mkdir -p /etc/wanbond
touch /etc/wanbond/edge.toml            # concentrator.toml on the VPS
chown root:root /etc/wanbond/edge.toml
chmod 0600 /etc/wanbond/edge.toml
```

## 2. Write both ends — a minimal working pair

Fill the keys generated in step 1 into the two files below. These are the
smallest configs that stand up a single-concentrator tunnel; the full key list
(every optional field, defaults, validation rules) is the exhaustive annotated
reference in [install.md §3z *Full configuration reference (all keys)*](install.md).

### Edge — `/etc/wanbond/edge.toml`

```toml
role = "edge"
psk  = "<base64 32-byte outer-control PSK — SAME on both ends>"

# One [[paths]] block per WAN uplink. source_addr pins this path's UDP socket
# to the local source IP the upstream router routes out the intended WAN.
[[paths]]
name        = "starlink"
source_addr = "192.168.1.10"

[[paths]]
name        = "5g"
source_addr = "192.168.2.10"

[wireguard]
private_key = "<base64 EDGE private key>"

[[wireguard.peers]]
public_key  = "<base64 CONCENTRATOR public key>"
endpoint    = "203.0.113.7:51820"   # required on the edge; concentrator's IP:port
allowed_ips = ["10.77.0.1/32"]      # the concentrator's inner tunnel address

[metrics]
listen = "127.0.0.1:9090"           # loopback only; a non-loopback bind is refused

[log]
level = "info"
```

### Concentrator — `/etc/wanbond/concentrator.toml`

```toml
role = "concentrator"
psk  = "<same PSK as the edge>"

# The concentrator learns the edge's real per-path endpoints from authenticated
# traffic; it only needs its own bind address.
[[paths]]
name        = "wan0"
source_addr = "10.0.0.5"            # the VPS's primary interface IP

[wireguard]
private_key = "<base64 CONCENTRATOR private key>"
listen_port = 51820                 # required on the concentrator

[[wireguard.peers]]
public_key  = "<base64 EDGE public key>"
allowed_ips = ["10.77.0.2/32"]      # the edge's inner tunnel address; no endpoint —
                                    # the concentrator roams the edge dynamically

[metrics]
listen = "127.0.0.1:9090"

[log]
level = "info"
```

Then install the systemd unit for each role, assign the inner addresses with a
systemd-networkd `.network` file, and `enable --now` — the exact steps are
[install.md §4](install.md). Bring up the concentrator first, then the edge.

> **Optional planes**, all off unless their block is present, all documented in
> [install.md §3](install.md): `[amnezia]` (DPI obfuscation, all-or-nothing),
> `[fec]` (Reed-Solomon forward error correction), `[scheduler]` (weighted
> aggregation + pacing — see step 5).

## 3. Add a standby concentrator (hub failover)

Hub failover moves the edge to a **standby concentrator** when the *active*
concentrator becomes unreachable on **every** uplink at once (HUB LOSS) — a
distinct event from per-path failover, which just shifts uplinks while the WG
session is untouched. Full rationale: [design.md §Concentrator hub
failover](design.md).

Two configuration facts make it work:

1. **The edge declares an ORDERED endpoint list.** Replace the single
   `endpoint` on the edge's peer block with `endpoints` — index 0 is the
   active/primary hub, the rest are ordered standbys tried in order:

   ```toml
   [[wireguard.peers]]
   public_key  = "<base64 CONCENTRATOR public key>"
   endpoints   = ["203.0.113.7:51820", "198.51.100.7:51820"]  # [active, standby, ...]
   allowed_ips = ["10.77.0.1/32"]
   ```

   `endpoints` is **mutually exclusive** with `endpoint`; the single `endpoint`
   form is just its one-element case and takes **no** failover action, so a
   one-concentrator deployment stays exactly as in step 2. Each entry may be
   an **IP:port literal** (as above) or a **hostname:port** behind the peer's
   `dns = true` opt-in — see [design.md §DNS endpoints and resolver privacy
   trade-offs](design.md).

2. **The standby shares the active's WireGuard static key.** All hubs in the
   list present the **same** WireGuard identity, so the edge re-handshakes to
   whichever hub is active against the single `public_key` it configured. In
   practice the standby's `concentrator.toml` is a **clone** of the active's —
   *same* `[wireguard].private_key`, same `listen_port`, same edge peer block —
   deployed on the standby's public IP:

   ```sh
   # On the standby VPS: copy the ACTIVE concentrator's config verbatim
   # (same private_key => same public key the edge already trusts), then
   # re-lock it. Do NOT generate a new keypair for the standby.
   install -m 0600 -o root -g root concentrator.toml /etc/wanbond/concentrator.toml
   ```

On HUB LOSS the edge advances to the next endpoint, repoints every path's
remote at it, and re-handshakes a **fresh** session — there is **no hub-to-hub
state handoff**; the standby starts clean and the edge re-baselines its receive
resequencer to the standby's first frame. End-of-list policy is **wrap**
(round-robin), so a hub that recovers earlier in the list is retried and
settled on within one cycle.

> Status: built and validated — the switch is covered by unit/component tests,
> the netns hub-failover e2e (T62), and the real-link mid-transfer WAN-kill tier
> (`TestRealMidTransferWANKill`, T63, reachable via `just p0-baseline`).

## 4. Concentrator firewall — open the port and PERSIST it

The concentrator needs two INPUT rules, and **both must survive a reboot**. On
OCI Ubuntu the `netfilter-persistent` boot service restores
`/etc/iptables/rules.v4` and silently discards any runtime-only rule (defect
D7/D8) — so a rule added with `iptables -I` alone reverts to the platform
default REJECT on the next reboot, with no signal until the tunnel is dead. The
full reasoning and the OCI default-REJECT caveat are [install.md §5](install.md);
the operational procedure is:

```sh
# 1. Open the WireGuard/listen UDP port from the WAN.
sudo iptables -I INPUT -p udp --dport 51820 -j ACCEPT

# 2. ACCEPT traffic on the tunnel interface, AHEAD of any default REJECT.
sudo iptables -I INPUT -i wanbond0 -j ACCEPT

# 3. Persist BOTH across reboot (Debian/Ubuntu; `service iptables save` on EL).
sudo apt-get install -y iptables-persistent   # provides netfilter-persistent
sudo netfilter-persistent save                # snapshots runtime chain -> rules.v4

# 4. Verify the tunnel ACCEPT is in the boot rules file and ordered first.
sudo grep -- '-A INPUT -i wanbond0 -j ACCEPT' /etc/iptables/rules.v4
sudo iptables -S INPUT                         # ACCEPTs must precede any -j REJECT
```

**Dedup discipline.** Both `iptables -I` and `netfilter-persistent save` are
non-idempotent by construction: re-running the insert stacks a duplicate ACCEPT
each time, and each `save` snapshots whatever duplicates exist. Guard the insert
with an `iptables -C` existence check so re-provisioning is idempotent:

```sh
sudo iptables -C INPUT -i wanbond0 -j ACCEPT 2>/dev/null \
  || sudo iptables -I INPUT -i wanbond0 -j ACCEPT
```

The repo automates and asserts exactly these steps for the standing testbed:
`test/realhosts/provision.go` (`Provision` / `InspectTunnelFirewall`, run via
`just realhosts-provision`) installs `iptables-persistent`, inserts the rule
**guarded by `iptables -C`** (so the tunnel ACCEPT count stays 1), runs
`netfilter-persistent save`, and re-inspects that the rule is present in
`/etc/iptables/rules.v4` and precedes the REJECT. Use it as the executable
reference for this section.

## 5. Pacing (optional — ships DISABLED, opt-in)

Per-path send-pacing bounds bufferbloat under sustained load by sizing each
uplink's pace from its bandwidth-delay product. It is **off by default**;
enabling it is a deliberate opt-in and requires **operator-measured** link
figures (wanbond does not auto-tune them).

Do **not** re-derive the procedure here — follow
[install.md §3a *Tuning per-link bandwidth and pacing*](install.md) end to end.
In summary:

- Measure each uplink's idle RTT and usable bandwidth (§3a Steps 1–2).
- Enter `link_bandwidth` / `link_rtt` on **every** `[[paths]]` block (all-or-none;
  a partial declaration is rejected at load) — §3a Step 3.
- Turn it on under `[scheduler]` and reload — §3a Step 4:

  ```toml
  [scheduler]
  policy         = "weighted"
  pacing_enabled = true          # OFF by default; sizes the pace from the links above
  ```

- Verify the loaded RTT stays close to the idle RTT under sustained load — §3a
  Step 5. (A declared bandwidth with `pacing_enabled = false` is inert.)

## 6. Monitoring and health checks

Each daemon serves Prometheus metrics on the **loopback-only** `[metrics].listen`
address ([install.md §6](install.md)); a non-loopback bind is refused. Scrape it
on-host:

```sh
curl -s http://127.0.0.1:9090/metrics
```

### Series to watch

Exact exported series (labels/help verbatim from `internal/metrics/metrics.go`).
Every per-path series carries a `path="<name>"` label matching the `[[paths]]`
`name`:

| Series                                          | Meaning / what to watch |
|-------------------------------------------------|-------------------------|
| `wanbond_path_up{path}`                         | Per-path liveness, `1`=up `0`=down. **Primary health signal.** |
| `wanbond_path_throughput_bits_per_second{path}` | Current per-path throughput; confirms traffic is striping. |
| `wanbond_path_loss_ratio{path}`                 | Probe loss fraction `[0,1]`; rising loss precedes a path drop. |
| `wanbond_path_rtt_seconds{path}`                | Smoothed RTT; a jump signals congestion/bufferbloat. |
| `wanbond_path_jitter_seconds{path}`             | Smoothed RTT deviation. |
| `wanbond_path_tx_bytes_total{path}` / `wanbond_path_rx_bytes_total{path}` | Per-path byte counters. |
| `wanbond_fec_recovered_packets_total`           | DATA packets reconstructed by FEC (masked loss). |
| `wanbond_fec_unrecoverable_packets_total`       | DATA lost beyond FEC repair — **should stay near-flat**. |
| `wanbond_fec_residual_loss_ratio`               | Post-recovery connection loss `[0,1]`; compare to your `target_residual`. |
| `wanbond_fec_repair_packets_total` / `wanbond_fec_data_packets_total` | Parity vs data counts — the overhead ratio (FEC only). |
| `wanbond_resequencer_released_frames_total`     | Frames released for delivery by the receive resequencer. |
| `wanbond_resequencer_dropped_duplicate_frames_total` / `wanbond_resequencer_dropped_stale_frames_total` / `wanbond_resequencer_dropped_suspect_frames_total` | Frames dropped as duplicate / already-past-release-point / not-yet-corroborated. |
| `wanbond_resequencer_skipped_seqs_total`        | Sequence numbers skipped (lost) by window-advance or timeout. |
| `wanbond_resequencer_resyncs_total` / `wanbond_resequencer_rebaselines_total` | Release-point re-pins after a corroborated discontinuity / forced re-baselines (e.g. hub failover). |
| `wanbond_session_established`                    | WG session liveness, `1`=a handshake has completed and is still fresh, `0`=still converging **or** wedged. **Distinguishes "converging" from "wedged".** |
| `wanbond_session_last_handshake_seconds`         | Age of the peer's most recent completed WG handshake (`0` when none has completed). |

> **Multi-peer concentrator (G4).** A concentrator bound to 2+ edges additionally
> labels every path/resequencer/FEC series above with `peer="<name>"` (the
> configured `[[wireguard.peers]]` `name`, or `""` for the concentrator's
> first-configured peer — see `internal/metrics/metrics.go`'s package doc for the
> back-compat rule). A single-peer edge/hub/concentrator carries **no** `peer` label
> at all — the exposition above is unchanged from pre-multi-peer wanbond.

> **Session vs paths.** `wanbond_session_established` is the WG-session signal the
> per-path gauges cannot give you: a path can be `up` (probes reflect) while the
> Noise handshake has not yet completed (**converging**) or has aged out
> (**wedged**). Read it together with `wanbond_path_up` — a healthy tunnel is
> `wanbond_path_up=1` **and** `wanbond_session_established=1`; a path up with
> `wanbond_session_established=0` past the initial convergence window is the wedged
> case. The daemon also logs one INFO `session established` record on each `0→1`
> transition. `wanbond_session_last_handshake_seconds` grows between rekeys and
> resets on each completed handshake; a healthy tunnel keeps it well under the
> 180s validity window (rekey + edge keepalive refresh it), so a value that climbs
> toward 180s and drops `wanbond_session_established` to `0` is the wedged signal.
>
> The FEC series are emitted only when an `[fec]` block is configured.

### Basic health check

A pilot is healthy when the tunnel is up (**at least one** path up) and,
ideally, **every** configured path is up. Since "tunnel up" is the max of the
per-path liveness gauges, both conditions reduce to `wanbond_path_up`:

```sh
# Exit 0 iff every configured path reports up (=1). Prints each path's state.
curl -s http://127.0.0.1:9090/metrics \
  | awk '/^wanbond_path_up\{/ { print; if ($NF != 1) bad=1 }
         END { exit bad }' \
  && echo "OK: all paths up" || echo "DEGRADED: a path is down"
```

For the operator's end-to-end sanity check, also confirm inner reachability and
that traffic is actually flowing on more than one path:

```sh
ping -c 3 10.77.0.1                         # from the edge: inner tunnel up
# then confirm >1 path is carrying bytes (aggregation engaged under load):
curl -s http://127.0.0.1:9090/metrics | grep '^wanbond_path_tx_bytes_total'
```

## 7. Pilot exit criterion (non-blocking)

The gate for proceeding to a **supervised pilot** is deliberately **non-blocking**
on any long soak (Q19). Two measurements are **sufficient** to enter the pilot:

1. **Capped-fixture aggregation + bufferbloat (netns, W2).** The bandwidth-capped
   netns fixture builds a real standing queue and measures aggregation and
   bufferbloat under it — `go test -tags e2e -run TestFixtureImpairment ./test/e2e`
   (see [install.md §3a Option A](install.md#3a-tuning-per-link-bandwidth-and-pacing)).
   It runs in the privileged netns tier.
2. **Report-only real-link smoke / baseline (W4).** `just p0-baseline` brings the
   tunnel up over the real internet between the two standing hosts and records the
   aggregation ratio, loaded-vs-idle RTT, and link/hub-failover recovery gaps — see
   [manual-checklist.md §P0 automated real-link baseline](manual-checklist.md#p0--automated-real-link-baseline-realhosts-tier).

Together these two are **enough to proceed to a supervised pilot.** The real-link
numbers are **INFORMATIONAL (report-only)** — no Mbit/s or millisecond threshold is
a hard pass/fail gate; a human reads them and makes the go/no-go call. A non-zero
exit from `just p0-baseline` means the run itself could not complete (a host was
unreachable or the tunnel never came up), **not** that a performance number missed a
target.

The **longer soak runs DURING the supervised pilot, not as a pre-gate.** The
reference short soak (`TestRealSoakShort`, ~2.5 min across a WG rekey — see the
appendix) only confirms the tunnel survives a rekey; sustained multi-hour/multi-day
soak is an **in-pilot** observation, gated on the live `wanbond_path_up` /
`wanbond_fec_unrecoverable_packets_total` health checks in [§6](#6-monitoring-and-health-checks),
never a blocker for entering the pilot.

## Appendix — reference figures measured on the test hosts

These numbers were **measured on this project's validation hosts** (edge
`llm-ubuntu-0`, amd64, NAT'd behind a home router ↔ public aarch64
concentrator `o3.7mind.io`) during pilot-readiness validation. They are
**illustrative, not guarantees** — your links will differ. Measure your own per
§5 (pacing) and the §6 health checks above.

| Metric (measured on test hosts)          | Value      |
|------------------------------------------|------------|
| Idle tunnel RTT                          | ~29 ms     |
| Loaded RTT (pacing on)                   | ~50 ms     |
| Bufferbloat Δ (loaded − idle, pacing on) | ~21 ms     |
| Per-path (LINK) failover — traffic resume| ~1.4 s     |
| Hub (concentrator) failover — resume via standby | ~2.1 s |
| Soak across a WG rekey                   | 2.5 min, tunnel stayed up |

Read these as "what a real cross-network deployment looked like once", not as a
target the code guarantees.
