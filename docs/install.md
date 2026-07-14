# wanbond — install and operations

wanbond ships as a single static binary. One binary serves both roles — the
**edge** (the mobile Linux box bonding the WAN uplinks) and the **concentrator**
(the public-IP VPS terminating the tunnel); the role is selected by the `role`
key in the config file, never by which binary is invoked.

> **Provisioning a fresh deployment end-to-end?** Follow the
> **[pre-pilot rollout runbook](runbook.md)** — a top-to-bottom operator
> procedure (key/PSK generation, both-ends config, standby-concentrator hub
> failover, firewall persistence, pacing, and health checks) that ties the
> sections below together. This document is the per-topic reference the runbook
> points back into.

## 1. Build the release binaries

From the repo root, inside the dev shell:

```sh
nix develop -c just release
```

This cross-compiles `cmd/wanbond` with `CGO_ENABLED=0` (fully static, no libc
dependency) for both deployment architectures into `dist/`:

- `dist/wanbond-linux-amd64` — x86_64 (typical OCI/AWS VPS concentrator)
- `dist/wanbond-linux-arm64` — aarch64 (ARM edge boxes, Ampere VPSes)

Verify with `file dist/*`: both must report `statically linked`. Alternatively
`nix build` produces the host-architecture binary via the flake package.

## 2. Install the binary

On each host (pick the artifact matching `uname -m`):

```sh
install -m 0755 wanbond-linux-<arch> /usr/local/bin/wanbond
wanbond version   # prints the stamped build version
```

## 3. Write the config file — 0600 REQUIRED

The daemon takes exactly one flag: `--config <path>`. The systemd units use
`/etc/wanbond/edge.toml` and `/etc/wanbond/concentrator.toml`.

The file holds the WireGuard private key and the outer-control PSK, so
`config.Load` **refuses any file whose permission bits are not exactly `0600`**
(`insecure permissions` error at startup). Create it as:

```sh
mkdir -p /etc/wanbond
touch /etc/wanbond/edge.toml
chown root:root /etc/wanbond/edge.toml
chmod 0600 /etc/wanbond/edge.toml
```

### Edge config (`/etc/wanbond/edge.toml`)

```toml
role = "edge"
psk = "<base64 32-byte outer-control PSK, same on both ends>"

# One [[paths]] block per WAN uplink. source_addr pins the path's UDP socket
# to the local source IP the upstream router routes out the intended WAN.
[[paths]]
name = "starlink"
source_addr = "192.168.1.10"

[[paths]]
name = "5g"
source_addr = "192.168.2.10"
# dest_addr = "203.0.113.7:51820"  # optional per-path concentrator endpoint;
                                   # omit when one public IP fronts all uplinks

[wireguard]
private_key = "<base64 edge private key>"

[[wireguard.peers]]
public_key = "<base64 concentrator public key>"
endpoint = "203.0.113.7:51820"     # required on the edge
# endpoints = ["203.0.113.7:51820", "198.51.100.7:51820"]  # ordered hub-failover
# form (Q18): first entry is the active/primary concentrator, the rest are
# ordered standbys tried in order when the active one is lost. Mutually
# exclusive with `endpoint` above — `endpoint` is just its one-element form,
# so a single-concentrator deployment keeps using it unchanged. Edge-only.
# Hub failover (all-paths-down detection + switch/re-handshake to the next
# endpoint) is implemented: T54 config surface + T57 switch.
allowed_ips = ["10.77.0.1/32"]     # concentrator's inner tunnel address

[metrics]
listen = "127.0.0.1:9090"          # loopback only; the daemon rejects anything else

[log]
level = "info"
```

### Concentrator config (`/etc/wanbond/concentrator.toml`)

```toml
role = "concentrator"
psk = "<same PSK as the edge>"

# The concentrator learns the edge's real per-path endpoints from
# authenticated traffic; it only needs its own bind address.
[[paths]]
name = "wan0"
source_addr = "10.0.0.5"           # the VPS's primary (private) interface IP

[wireguard]
private_key = "<base64 concentrator private key>"
listen_port = 51820                # required on the concentrator

[[wireguard.peers]]
public_key = "<base64 edge public key>"
allowed_ips = ["10.77.0.2/32"]     # edge's inner tunnel address; no endpoint —
                                   # the concentrator roams the edge dynamically

[metrics]
listen = "127.0.0.1:9090"

[log]
level = "info"
```

Optional on both ends: an `[amnezia]` obfuscation block (all-or-nothing — when
used, `jc`, `jmin`, `jmax`, `s1`, `s2` must all be set, identically on both
ends). Omit it entirely for plain WireGuard framing.

Generate keys with standard WireGuard tooling (`wg genkey | tee k | wg pubkey`)
and the PSK with `head -c 32 /dev/urandom | base64`.

### Optional `[fec]` forward-error-correction plane

FEC is **off** unless an `[fec]` block is present. A fixed-ratio block protects
each group of `data_shards` (K) inner datagrams with `parity_shards` (M) parity
frames at a constant M/K overhead:

```toml
[fec]
enabled = true
data_shards = 10
parity_shards = 6        # in adaptive mode this is the parity CEILING
# adaptive = true        # opt into the closed-loop controller (below)
# target_residual = 0.005  # residual-loss SLA — the recommended adaptive surface
# safety_factor = 4.0    # legacy headroom multiplier (mutually exclusive with target_residual)
```

With `adaptive = true` the send side runs the loss-tracking controller: the
per-group parity floats in `[0, parity_shards]` to match measured path loss, so
a clean path spends near-zero overhead. Two mutually-exclusive ways size that
parity — **set exactly one**:

- **`target_residual`** (recommended, the primary surface): the target
  **post-recovery residual-loss** fraction in `(0,1)`. The controller derives the
  minimum parity M whose modeled binomial residual `E[max(0,D-M)]/K`
  (`D ~ Bin(K, smoothed loss)`) is at/below this target for the current loss and
  K, capped at the `parity_shards` ceiling. It maps an operator's loss budget
  directly to redundancy: e.g. `target_residual = 0.005` holds the post-recovery
  loss at/below 0.5% (the P4 bound) as long as the ceiling allows. Raising the
  ceiling (`parity_shards`) lets a tighter target be met under heavier loss.
- **`safety_factor`** (legacy): a bare headroom multiplier ≥ 1 sizing M so
  `M/(K+M) ≥ safety_factor × loss`. It does **not** map to a residual bound — an
  operator must hand-tune it per (loss, K) to clear a given SLA (the reason
  `target_residual` supersedes it). Defaults to 1.5 when adaptive and neither
  field is set; note 1.5 sizes M=1 at 5% loss with K=10 (~1% residual), so a
  sub-1% SLA needs a higher factor **or**, preferably, `target_residual`.

Setting both `target_residual` and `safety_factor` is rejected at config load.

### Optional `[scheduler]` weighted aggregation + pacing

The send scheduler defaults to **active-backup** (one active path, instant
failover). An optional `[scheduler]` block selects the **weighted-aggregation**
policy and, off by default, per-path send-**pacing** (token buckets that bound
bufferbloat under sustained load):

```toml
[scheduler]
policy = "weighted"
pacing_enabled = true     # OFF by default; when on, size the pace from the links below
```

When pacing is enabled you may declare each uplink's bandwidth and baseline RTT
directly on its `[[paths]]` block; the daemon then sizes the per-path pace from the
bandwidth-delay product at config load, instead of the synthetic default:

```toml
[[paths]]
name = "starlink"
source_addr = "192.168.1.10"
link_bandwidth = "50Mbit"  # SI bit/s: k/M/G = 1e3/1e6/1e9 (e.g. "10Mbit", "1Gbit")
link_rtt = "45ms"          # baseline RTT — the delay term of the pacing burst
```

- The declaration is **operator-declared, not auto-tuned**: the value is fixed at
  load; wanbond does not adjust it live. Measure it once per link.
- It is **all-or-nothing**: declare `link_bandwidth` (and `link_rtt`) on *every*
  path or none. The shared per-path pace is sized to the **slowest declared link**
  (the bottleneck), so a partial declaration is rejected.
- A declared bandwidth with `pacing_enabled = false` (the default) is **inert** —
  the synthetic default pace is kept and the tunnel behaves as before.
- `link_bandwidth` is **mutually exclusive** with the raw `per_path_capacity_fps` /
  `pacing_burst_frames` knobs: declare the link bandwidth *or* set the frame-slot
  knobs, not both. A non-positive or unparseable bandwidth/RTT is rejected at load.

### Optional `[dns]` resolver block

Hostname peer endpoints (opt-in per peer with `dns = true`, see the endpoint
list note above) are resolved through the OS **system resolver by default** —
an absent `[dns]` block is inert. To route that resolution through a private
DNS-over-HTTPS or DNS-over-TLS resolver instead, add an explicit `[dns]`
block:

```toml
[dns]
resolver = "doh"                                # "system" (default) | "doh" | "dot"
doh_url = "https://198.51.100.1/dns-query"       # required iff resolver = "doh"
# dot_server = "198.51.100.1"                    # required iff resolver = "dot"
# poll_interval = "30s"                          # re-resolution cadence; must be > 0
# timeout = "5s"                                 # per-lookup bound; must be > 0
```

- `[dns]` only **selects the transport** the per-peer `dns = true` opt-in uses;
  it never turns hostname resolution on by itself.
- **BOOTSTRAP-IP invariant**: `doh_url`/`dot_server`'s host must itself be
  reachable *without* a DNS lookup (otherwise resolving your private
  resolver's own name would need the very system resolver you configured it
  to avoid). Give it as an IP literal, or set `bootstrap_ip` explicitly when
  it is a hostname — config load fails fast otherwise. `bootstrap_ip` must
  stay absent when the host is already an IP literal; a non-empty value
  there is rejected at load as a mode mismatch.
- `dot_server` dials the fixed IANA-assigned DoT port (853); an explicit
  `host:port` form must use that exact port.
- `doh_url`/`dot_server` are mode-specific: setting the wrong one for the
  selected `resolver` (or either one under `resolver = "system"`) is
  rejected at load.
- **Tolerant boot**: a hostname endpoint that cannot be resolved at startup
  (resolver down, DNS outage) never fails bring-up — the tunnel comes up
  *without* that peer endpoint and the background re-resolution loop installs
  it and initiates the handshake on the first successful lookup. Steady-state
  re-resolution then repoints the bond whenever the record changes.

### 3a. Tuning per-link bandwidth and pacing

**Pacing ships DISABLED by default.** When enabled with `pacing_enabled = true`
under the `[scheduler]` block, wanbond sizes the per-path send-pace from the
bandwidth-delay product (BDP) — the product of each uplink's usable bandwidth and
round-trip latency — to bound bufferbloat (excessive queueing) under sustained load.
The declared bandwidth is **operator-measured, not auto-tuned**: you measure it once
per link and enter it in the config.

This section describes how to measure the required values (`link_bandwidth` and
`link_rtt`), where to enter them, and how to verify pacing is effective.

#### Step 1: Measure baseline (idle) round-trip time

Measure the idle RTT on each path — the latency with light traffic:

```sh
# From the edge, ping the concentrator's tunnel address (e.g., 10.77.0.1).
# Use -c 10 for a quick sample; ignore the first ping (ARP/cold cache).
ping -c 10 10.77.0.1
```

Example output:
```
round-trip min/avg/max/stddev = 20.1/21.5/23.2/1.0 ms
```

Record the **average** RTT (here: 21.5 ms). If RTTs are highly variable, the path
is jittery; take a longer sample (e.g. `-c 30`). Each path's idle RTT becomes the
`link_rtt` value in the config. If paths have different RTTs, declare each path's
own value separately.

#### Step 2: Measure usable bandwidth per uplink

You have two options:

##### Option A: Measurement via the capped-fixture test (T52, netns)

If you have access to a lab setup (or are developing/testing wanbond), the test
suite includes a deterministic bandwidth-measurement sub-test. From the repo root:

```sh
# This runs the entire fixture-impairment suite, including the BDP sub-test:
go test -tags e2e -run TestFixtureImpairment -v ./test/e2e
```

Toward the end of the output, you will see a `bdp` sub-test log (the numbers below
are **illustrative placeholders** — the sub-test is report-only and its measured
values vary run to run; read the actual figures from *your* run's log):

```
path capped BDP: idle RTT=<e.g. ~5>ms loaded RTT=...ms (bufferbloat Δ=...ms) | 
  achieved throughput=<e.g. ~35-56> Mbit/s | BDP=... bytes (...frames @ ≈1540B/frame) | 
  SizePacingFromBDP -> capacityFPS=... burstFrames=...
```

The **achieved throughput** (whatever your run reports) is a measured point;
the fixture builds a sustained queue by running iperf3 under a controlled bandwidth
cap, so it measures the true link-limited throughput (not CPU-bound). The per-frame
size (`≈1540B`) is the full path MTU (1500) plus the outer-frame DATA overhead
(40) that `SizePacingFromBDP` uses to convert bandwidth to a frame rate.

##### Option B: Measurement on the real deployment (manual)

Measure each uplink independently with iperf3, one at a time:

```sh
# On the concentrator, start the iperf3 server:
iperf3 -s -B 10.77.0.1

# On the edge, run a sustained transfer to build a standing queue (8–10 seconds):
# This measures the throughput the link will sustain under sustained load
# and allows RTT to stabilize under queue pressure.
iperf3 -c 10.77.0.1 -t 10
```

Example output:
```
Bitrate         Jitter  Lost/Total Datagrams
...
  50.2 Mbit/s  ...     ...
```

If using TCP (default), read the throughput from the final summary line.

**Important:** Repeat this measurement for each uplink **separately** — bring up
only one path at a time, or isolate it at the router layer. The measurement must
reflect each link's independent capacity, not the bonded throughput.

Also measure the RTT under load (as a sanity check on bufferbloat):

```sh
# While iperf3 is running (in another terminal on the edge):
ping -i 0.2 10.77.0.1
```

Record the average RTT under load. If it is much higher than the idle RTT
(e.g. idle 20 ms → loaded 200 ms), the path has severe bufferbloat; pacing will
help control this (that is the whole point).

#### Step 3: Enter the measured values in the config

For each path, add `link_bandwidth` and `link_rtt` to the `[[paths]]` block:

```toml
[[paths]]
name = "starlink"
source_addr = "192.168.1.10"
link_bandwidth = "50Mbit"    # from Step 2 measurement: 50.2 Mbit/s → round to 50Mbit
link_rtt = "21ms"            # from Step 1: 21.5 ms idle RTT → round to 21ms

[[paths]]
name = "5g"
source_addr = "192.168.2.10"
link_bandwidth = "10Mbit"    # measured as slower
link_rtt = "45ms"            # higher baseline latency
```

**Rules:**
- **Declare on every path or none.** If you declare `link_bandwidth` on one path,
  you must declare it on all (wanbond sizes pacing to the slowest link, the
  bottleneck). Partial declarations are rejected at load.
- **Round conservatively.** Round down if unsure (e.g., measure 49.8 Mbit/s →
  declare `49Mbit` rather than `50Mbit`). Under-sized pacing is safe; over-sized
  pacing may not bind and bufferbloat may occur.
- **Use the `link_rtt` from Step 1**, not the loaded RTT from Step 2. The idle RTT
  is the baseline delay the BDP calculation assumes; the loaded RTT tells you how
  much bufferbloat exists.

#### Step 4: Enable pacing and deploy

Ensure the edge config has the weighted scheduler and pacing enabled:

```toml
[scheduler]
policy = "weighted"
pacing_enabled = true
```

Reload or restart the daemon:

```sh
systemctl reload wanbond-edge   # SIGHUP: re-reads config, applies path changes
# or
systemctl restart wanbond-edge  # full restart if config is invalid
```

Verify there are no errors in the journal:

```sh
journalctl -u wanbond-edge -n 20
```

A successful load will log `config loaded` (or, on reload, `config reloaded`);
if the daemon rejects the config (e.g. inconsistent bandwidth declarations,
unparseable values), the error message will say why.

#### Step 5: Verify bufferbloat is controlled

While running a sustained load (iperf3 from edge to concentrator), measure the
RTT under pacing:

```sh
# Edge, run iperf3 traffic:
iperf3 -c 10.77.0.1 -t 30

# Edge, another terminal, sample RTT under load:
ping -i 0.2 10.77.0.1
```

If pacing is working, the RTT under sustained load should be **close to the idle
RTT** (ideally within 5–10 ms, depending on the link's buffering). Before pacing,
you may have seen loaded RTT inflate to 100+ ms on a bufferbloated link; pacing
bounds the queue so the inflation is minimal.

**Note:** the test suite's netns fixture is CPU-bound and does not build the
standing queues needed to validate pacing against real links. Real-link
verification (this step) is essential: measure on your actual uplinks to confirm
pacing meets your bufferbloat target.

### 3z. Full configuration reference (all keys)

This is the exhaustive key list — every configuration key wanbond reads, in one
place. The example below is **edge-oriented**; concentrator-only keys are shown
and marked `CONCENTRATOR-ONLY`. Required keys are shown live; optional/defaulted
keys are shown **commented-out with their default value**. Uncommenting a key
and leaving it at the shown value is a no-op. The per-section notes after the
example capture the cross-field constraints a single example cannot express
inline. All of these are enforced at config load (`config.Load`), which fails
fast on the first violation — except the loopback and `allowed_ips` checks,
noted below.

> The same content ships as a copy-pasteable file at the repo root,
> [`wanbond.example.toml`](../wanbond.example.toml): copy it, fill the
> `<placeholders>`, and delete what you do not need.

```toml
# ── top level ────────────────────────────────────────────────────────────────
role = "edge"                      # REQUIRED. "edge" | "concentrator". Never
                                   #   inferred from other keys.
psk  = "<base64 32-byte PSK>"      # REQUIRED. 32 raw bytes, base64. Same value
                                   #   on both ends; keys the PSK-HMAC that
                                   #   authenticates outer PROBE/CONTROL frames.
# tun_persist = false              # OPTIONAL, DEFAULT false. false => wanbond0 is
                                   #   destroyed on daemon stop (its addresses/
                                   #   routes/rules drop on every restart). true =>
                                   #   TUNSETPERSIST on start: wanbond0 survives a
                                   #   restart with the SAME ifindex, so operator-
                                   #   owned addressing persists. See "Interface
                                   #   addressing" below for the NM (D39) caveat.

# ── paths: one [[paths]] block per WAN uplink; at least one is REQUIRED ───────
[[paths]]
name        = "starlink"           # REQUIRED. Stable, unique identifier.
source_addr = "192.168.1.10"       # REQUIRED. Bare local source IP the path's
                                   #   UDP socket binds to. Must be unique across
                                   #   paths (a shared source collides EADDRINUSE
                                   #   at the second bind). No default.
# dest_addr = "203.0.113.7:51820"  # OPTIONAL, edge-only meaning. Per-path
                                   #   concentrator endpoint (ip:port). Omit when
                                   #   one public IP fronts all uplinks (the
                                   #   peer's endpoint is reused). No default.
                                   #   Inert on the concentrator (it learns edge
                                   #   endpoints from traffic).
# link_bandwidth = "50Mbit"        # OPTIONAL. Operator-declared bottleneck
                                   #   bandwidth. SI bit/s: k/M/G = 1e3/1e6/1e9
                                   #   ("bit" may be written "bps"). Must be > 0.
                                   #   Used ONLY under weighted policy + pacing;
                                   #   inert otherwise. No default (undeclared).
# link_rtt = "45ms"                # OPTIONAL. Operator-declared baseline RTT
                                   #   (Go duration). REQUIRED (> 0) when
                                   #   link_bandwidth is set under weighted
                                   #   pacing; ignored otherwise. No default.

# A second uplink (edge). Repeat the block per path.
[[paths]]
name        = "5g"
source_addr = "192.168.2.10"

# ── wireguard: inner tunnel key material ─────────────────────────────────────
[wireguard]
private_key = "<base64 32-byte private key>"  # REQUIRED (both roles).
# listen_port = 51820              # CONCENTRATOR-ONLY: REQUIRED (> 0) there;
                                   #   omit / leave 0 on the edge. uint16
                                   #   (< 1024 needs CAP_NET_BIND_SERVICE).

# ── wireguard peers: at least one [[wireguard.peers]] is REQUIRED ─────────────
[[wireguard.peers]]
public_key = "<base64 peer public key>"  # REQUIRED. base64 32-byte key.
endpoint = "203.0.113.7:51820"     # EDGE: REQUIRED (this OR `endpoints`).
                                   #   ip:port literal only, no DNS.
                                   #   CONCENTRATOR: must be ABSENT (rejected) —
                                   #   it roams the edge dynamically. Mutually
                                   #   exclusive with `endpoints`.
# endpoints = ["203.0.113.7:51820", "198.51.100.7:51820"]
                                   # EDGE-ONLY ordered hub-failover list: [0] is
                                   #   the active concentrator, the rest ordered
                                   #   standbys. ip:port literals only, no DNS;
                                   #   duplicates rejected. Mutually exclusive
                                   #   with `endpoint` (which is its one-element
                                   #   form). Rejected on the concentrator.
allowed_ips = ["10.77.0.1/32"]     # REQUIRED: >= 1 CIDR routed to this peer
                                   #   (enforced when the WG UAPI is built). A
                                   #   literal 0.0.0.0/0 or ::/0 is always split
                                   #   into the equivalent /1+/1 pair at UAPI
                                   #   render.
# mode = "default-route"           # OPTIONAL, edge-only. Marks this peer as
                                   #   the edge's full-tunnel concentrator.
                                   #   Rejected on the concentrator.

# ── amnezia obfuscation: OPTIONAL, OFF by default (plain WireGuard) ───────────
# ALL-OR-NOTHING: either omit the whole block, or set the entire
# jc/jmin/jmax/s1/s2 set — IDENTICALLY on both ends. One engine per process.
# [amnezia]
# jc   = 4                         # junk packet count. > 0 when configured.
# jmin = 40                        # min junk packet size. > 0; jmin <= jmax.
# jmax = 70                        # max junk packet size. > 0.
# s1   = 30                        # init-packet junk prefix size. > 0.
# s2   = 40                        # response-packet junk prefix size. > 0.
                                   #   Constraint: (148 + s1) != (92 + s2) — the
                                   #   obfuscated init/response lengths must
                                   #   differ. (no defaults: required when block
                                   #   present.)
# h1 = 1                           # magic header: initiation. Default 1..4 when
# h2 = 2                           #   the block is configured but NO header is
# h3 = 3                           #   given. When any is set, h1..h4 must be a
# h4 = 4                           #   distinct set (values <= 4 mean "standard
                                   #   message type").

# ── scheduler: OPTIONAL. Omitted => active-backup, all knobs below ignored ────
# [scheduler]
# policy = "active-backup"         # "active-backup" (DEFAULT) | "weighted".
                                   #   Every knob below applies ONLY to
                                   #   "weighted"; under active-backup they are
                                   #   inert and left unset.
# per_path_capacity_fps = 10000.0  # DEFAULT 10000. Reference per-path capacity
                                   #   (frame slots/s): aggregation-gate
                                   #   denominator and pacing refill rate. > 0.
# engage_fraction    = 0.9         # DEFAULT 0.9. Engage aggregation above
                                   #   engage_fraction * capacity. In (0, 1].
# disengage_fraction = 0.5         # DEFAULT 0.5. Collapse below
                                   #   disengage_fraction * capacity. Must be in
                                   #   [0, engage_fraction) — the hysteresis band.
# collapse_dwell = "2s"            # DEFAULT 2s. Sustained-low dwell before
                                   #   collapsing to primary-only. >= 0.
# load_tau = "200ms"               # DEFAULT 200ms. Offered-load rate estimator
                                   #   time constant. > 0.
# pacing_enabled = false           # DEFAULT false. Turn on per-path send-pacing
                                   #   (token buckets). Off => buckets bypassed.
# pacing_burst_frames = 64.0       # DEFAULT 64. Token-bucket burst (frames).
                                   #   > 0 when pacing_enabled.
# weight_rtt_floor = "1ms"         # DEFAULT 1ms. RTT floor in the weight
                                   #   formula. > 0.
# weight_loss_floor = 0.001        # DEFAULT 1e-3. Loss floor under the sqrt in
                                   #   the weight formula. > 0.

# ── fec: OPTIONAL, OFF by default (no parity on the wire) ─────────────────────
# [fec]
# enabled = true                   # DEFAULT false. Turns the FEC plane on; when
                                   #   false every field below is ignored.
# data_shards = 10                 # K: inner datagrams per coding group. >= 1
                                   #   when enabled. No default (required).
# parity_shards = 6                # M: parity frames per group (and the adaptive
                                   #   parity CEILING). >= 1 when enabled;
                                   #   K + M <= 256. No default (required).
# deadline = "5ms"                 # DEFAULT 5ms. Partial-group flush deadline.
                                   #   > 0 and <= 125ms.
# adaptive = false                 # DEFAULT false. Closed-loop parity controller
                                   #   (parity tracks measured loss up to the
                                   #   ceiling). Requires enabled = true.
# target_residual = 0.005          # ADAPTIVE-ONLY, primary sizing surface:
                                   #   post-recovery residual-loss SLA in (0, 1).
                                   #   No default. Mutually exclusive with
                                   #   safety_factor.
# safety_factor = 1.5              # ADAPTIVE-ONLY, legacy headroom multiplier
                                   #   >= 1. DEFAULTS to 1.5 when adaptive and
                                   #   NEITHER field is set. Must stay 0 in fixed
                                   #   mode. Mutually exclusive with
                                   #   target_residual.

# ── metrics: OPTIONAL. Omit the block => no /metrics endpoint is served ───────
[metrics]
listen = "127.0.0.1:9090"          # No default. LOOPBACK-ONLY (127.0.0.0/8,
                                   #   ::1, or a hostname resolving only to
                                   #   loopback); any other address is REFUSED
                                   #   when the endpoint binds (not at load).

# ── log: OPTIONAL ────────────────────────────────────────────────────────────
[log]
level = "info"                     # DEFAULT "info" (empty => info). One of
                                   #   debug | info | warn | error ("warning" is
                                   #   accepted for warn). Unknown => fail-fast.
```

**Cross-field constraints and role applicability (not expressible inline):**

- **Roles.** `role` is required and never inferred. Edge-only:
  per-peer `endpoint`/`endpoints` (required on the edge, rejected on the
  concentrator) and the pacing declarations (`dest_addr` is edge-only in effect,
  but is not *rejected* on the concentrator — merely inert). Concentrator-only:
  `wireguard.listen_port` (required there, unused on the edge).
- **`endpoint` vs `endpoints`.** Mutually exclusive; `endpoint` is the
  one-element form of `endpoints`. On the edge exactly one must be present; the
  concentrator must set neither. Entries are `ip:port` literals only (no DNS),
  de-duplicated.
- **Distinct `source_addr`.** Each `[[paths]]` needs a unique, valid
  `source_addr` (compared unmapped, so `192.0.2.10` and `::ffff:192.0.2.10`
  collide). `name` must also be unique.
- **`allowed_ips`.** At least one CIDR per peer; empty is rejected when the WG
  configuration is assembled (not by `config.validate`). A literal `0.0.0.0/0`
  or `::/0` (full tunnel) is ALWAYS split into the equivalent `/1`+`/1` pair at
  UAPI render — the engine never receives the literal `/0`, which wedges the
  handshake.
- **`mode` is edge-only.** Peer `mode = "default-route"` marks a peer as the
  edge's full-tunnel concentrator (an opt-in alongside a `0.0.0.0/0`/`::/0`
  `allowed_ips` entry); rejected on the concentrator, mirroring the
  `endpoint`/`dns` edge-only rules. It is a config-surface marker only today —
  it does not install any OS-level default route.
- **amnezia all-or-nothing.** An unconfigured (all-zero) block is plain
  WireGuard. Once *any* of `jc/jmin/jmax/s1/s2/h1..h4` is set, the full
  `jc,jmin,jmax,s1,s2` set must be `> 0`, `jmin <= jmax`, the init/response
  lengths must not collide (`148+s1 != 92+s2`), and `h1..h4` must be a distinct
  set (omitted headers default to `1..4`). The same profile must be set on both
  ends. One obfuscation engine per process.
- **`link_bandwidth` + `link_rtt` are a pair.** Both-or-neither, and
  **all-or-nothing across every path**: declare on every `[[paths]]` block or
  none (the shared pace is sized to the slowest declared link, undefined under a
  partial declaration). They take effect **only** under
  `scheduler.policy = "weighted"` with `pacing_enabled = true`; otherwise a
  declared bandwidth is inert. When active they are **mutually exclusive** with
  the raw `scheduler.per_path_capacity_fps` / `pacing_burst_frames` knobs —
  declare link bandwidth *or* set the frame-slot knobs, not both.
- **scheduler off-unless-present.** No `[scheduler]` block ⇒ `active-backup` and
  every weighted knob ignored. Under `weighted`, omitted knobs take the defaults
  shown; the hysteresis band requires `disengage_fraction < engage_fraction`.
- **fec off-unless-present.** No `[fec]` block (or `enabled = false`) ⇒ no
  parity on the wire. `adaptive = true` requires `enabled = true`. In adaptive
  mode set **either** `target_residual` (recommended, `(0,1)`) **or**
  `safety_factor` (`>= 1`), never both; both must stay 0 (unset) in fixed mode.
- **metrics loopback-only.** `[metrics] listen` must be a loopback address (or a
  hostname resolving entirely to loopback); a non-loopback bind is refused when
  the endpoint starts. Omit the block to serve no metrics at all.

## 4. systemd units

Unit files live in `packaging/systemd/`:

```sh
cp packaging/systemd/wanbond-edge.service /etc/systemd/system/          # edge box
cp packaging/systemd/wanbond-concentrator.service /etc/systemd/system/  # VPS
systemctl daemon-reload
systemctl enable --now wanbond-edge      # or wanbond-concentrator
```

- `systemctl reload wanbond-<role>` sends SIGHUP: the daemon re-reads the
  config and applies the **path diff** (add/remove uplinks) without tearing
  down the live tunnel. A bad reload is logged and the running config kept.
- `Restart=on-failure` restarts the daemon if the tunnel engine tears down
  unexpectedly (the process exits non-zero in that case by design).
- The units run with `CapabilityBoundingSet=CAP_NET_ADMIN` (TUN creation) plus
  standard hardening. If you set `wireguard.listen_port` below 1024, add
  `CAP_NET_BIND_SERVICE` to the bounding set.

### NetworkManager unmanaged-devices drop-in

On systems running **NetworkManager** (the default on RPi OS, Debian, and Ubuntu
desktop), NetworkManager monitors the `wanbond0` interface for link changes and
automatically flushes any configured IP addresses on link-up, destroying the
tunnel routing without operator action. This failure mode (D39, D5) is prevented
by marking `wanbond0` as an unmanaged device in NetworkManager's configuration.

Deploy the shipped drop-in:

```sh
cp packaging/networkmanager/99-wanbond-unmanaged.conf /etc/NetworkManager/conf.d/
```

Then reload NetworkManager to apply the change:

```sh
nmcli general reload
# or
sudo systemctl reload NetworkManager
```

Verify the interface is marked unmanaged:

```sh
nmcli device show wanbond0 | grep STATE
```

The output should show `STATE: unmanaged`.

If your edge box runs **systemd-networkd** instead, and NetworkManager is **not running**,
you do not need this drop-in — skip this subsection and proceed to "Interface
addressing and routing" below. (If NetworkManager is active alongside systemd-networkd,
the drop-in is still required to prevent the address flush.)

### Interface addressing and routing (operator-owned)

The daemon creates the TUN interface (`wanbond0`) and brings it
administratively **UP** itself (`SIOCSIFFLAGS`/`IFF_UP`) right after creation —
so a write to it never silently fails with `EIO` for want of a link-up step —
but owns ONLY the tunnel engine: **it never assigns addresses or routes**
(wg-quick style, no privileged shell-outs). Assign them with a
**systemd-networkd `.network` file**, using the inner addresses from
`allowed_ips`:

If something ELSE takes the link back down after boot (NetworkManager without
the unmanaged-devices drop-in below is the common case, D39), a `TUN write`
`EIO` no longer surfaces only as the engine's raw `input/output error`: the
daemon inspects the interface's link state and MTU and logs an actionable
ERROR naming the cause (e.g. `wanbond0 is DOWN — address & bring it up
(install.md §4)`) alongside the raw errno, rate-limited to one diagnostic per
30s so a write storm does not flood the log.

```ini
# /etc/systemd/network/10-wanbond0.network  (edge; concentrator: 10.77.0.1/24)
[Match]
Name=wanbond0

[Network]
Address=10.77.0.2/24
```

```sh
systemctl enable --now systemd-networkd
```

Do **not** use a `[Service] ExecStartPost=ip address add … dev wanbond0`
drop-in: the units are `Type=exec`, so systemd considers the service started
the instant `execve()` returns — *before* the daemon has created `wanbond0` —
and the `ExecStartPost` would run against a not-yet-existent interface, fail
with "Cannot find device", and (being an un-prefixed `ExecStartPost`) fail the
unit and trip the `Restart=on-failure` crash-loop. networkd is race-free: it
watches for the interface and applies the address the moment `wanbond0`
appears, whenever that is. (The daemon exposes no `sd_notify` readiness, so
there is no ordering guarantee to hang an `ExecStartPost` on.)

#### Persisting `wanbond0` across daemon restarts (`tun_persist`, I7)

By default `wanbond0` is a **non-persistent** TUN: the kernel destroys it when
the daemon's last file descriptor closes on stop, so every restart re-creates
it from scratch and the operator-owned addresses/routes/rules attached to it are
dropped. networkd re-applies its `.network` address on the fresh interface, but
any manual `ip` state and the interface's `ifindex` do not survive.

Set the top-level `tun_persist = true` to opt into persistence: the daemon
issues `TUNSETPERSIST` on start, so the kernel keeps `wanbond0` across a daemon
stop/start with the **same `ifindex`**, and the next start re-adopts the same
device by name rather than recreating it. Addresses, routes, and rules
referencing `wanbond0` then survive a restart untouched. Reverting to
`tun_persist = false` and restarting clears the flag, so the device returns to
being torn down on stop.

> **NetworkManager (D39):** a *persistent* `wanbond0` still needs the
> NetworkManager `unmanaged-devices` drop-in on NM-managed hosts. Persistence
> keeps the link alive across restarts; it does **not** exempt the interface
> from NetworkManager, which would otherwise flush the addresses/routes the
> operator assigned. Keep the unmanaged-devices drop-in in place regardless of
> `tun_persist`.

#### Persistence recipe for non-networkd hosts (`wanbond-addressing@.service`)

The `systemd-networkd` `.network` recipe above needs `systemd-networkd`
itself watching `wanbond0`. On a host that addresses the interface another
way — NetworkManager with the unmanaged-devices drop-in above but no
networkd `.network` file, or neither daemon running — use the shipped
templated oneshot instead, `packaging/systemd/wanbond-addressing@.service`
(instance = role: `edge` or `concentrator`):

```sh
cp packaging/systemd/wanbond-addressing@.service /etc/systemd/system/
# Write your own re-apply script (address + link-up + policy rules +
# per-table routes + nft SNAT — whatever the role needs) and, optionally, an
# environment file it can read:
$EDITOR /etc/wanbond/addressing-edge.sh    # or addressing-concentrator.sh
chmod +x /etc/wanbond/addressing-edge.sh
$EDITOR /etc/wanbond/addressing-edge.env   # optional EnvironmentFile
systemctl daemon-reload
systemctl enable --now wanbond-addressing@edge.service   # instance = role
```

`wanbond-addressing@<role>.service` is `PartOf=`/`After=wanbond-<role>.service`
(stops with the daemon, only ever starts after it) and has
`ConditionPathExists=/etc/wanbond/addressing-<role>.sh` — leave that script
unwritten and the unit is skipped, not failed. It is a `Type=oneshot` with
`RemainAfterExit=yes` that runs `ExecStart=/etc/wanbond/addressing-<role>.sh`,
optionally fed by `EnvironmentFile=-/etc/wanbond/addressing-<role>.env`.

**Do not replace its `ExecStartPre` wait loop with a plain `ExecStartPost=` on
`wanbond-<role>.service` itself.** That is the exact race R27 found and fixed:
`wanbond-edge.service`/`wanbond-concentrator.service` are `Type=exec`, so
systemd considers them started the instant `execve()` returns — **before**
the daemon has created `wanbond0` — so an `ExecStartPost` there runs against a
not-yet-existent interface, fails with "Cannot find device", and (being
un-prefixed) fails the unit and trips the `Restart=on-failure` crash-loop
(the same failure mode as the raw `ip address add` `ExecStartPost` warned
against above). `wanbond-addressing@.service` avoids this by ordering itself
`After=` the daemon unit **and** actively polling `/sys/class/net/wanbond0` in
its own `ExecStartPre` until the interface exists, so it never races tun
creation regardless of how long the daemon takes to create it.

Once `tun_persist = true` (above) is enabled, `wanbond0` and the
addresses/routes/rules attached to it survive a daemon restart on their own,
so re-running this oneshot on every restart becomes **optional but
harmless** — keep it enabled anyway as a safety net for what `tun_persist`
does not cover (first boot, a manual `ip link delete wanbond0`, a host that
cannot enable `tun_persist`).

## 5. Firewall

### Concentrator: UDP listen port

Allow the WireGuard UDP port in from the WAN (and, on OCI, in the subnet's
security list / NSG as well — both layers must pass):

```sh
iptables -I INPUT -p udp --dport 51820 -j ACCEPT
```

### Concentrator: tunnel-interface ACCEPT — REQUIRED (OCI default-REJECT caveat)

The concentrator **must ACCEPT traffic arriving on the wanbond tunnel
interface, ordered ahead of any default REJECT rule**:

```sh
iptables -I INPUT -i wanbond0 -j ACCEPT
```

Why this is required: Oracle Cloud (OCI) images ship an INPUT chain ending in

```
-A INPUT -j REJECT --reject-with icmp-host-prohibited
```

which silently applies to the tunnel interface too. ICMP echo is allowed by an
earlier rule, so **ping through the tunnel works while any TCP connection
through the tunnel fails with a confusing `No route to host`** (the REJECT's
ICMP host-prohibited answer). This exact failure was hit during P0 real-host
testing. `iptables -I` (insert at head) places the ACCEPT ahead of the appended
(`-A`) REJECT; verify the ordering with `iptables -S INPUT` — the tunnel ACCEPT
must appear before the `-j REJECT` line. The same applies to any distro or
provider whose default firewall ends INPUT with REJECT/DROP (e.g. firewalld
zones): treat "tunnel ACCEPT ahead of the default reject" as a required
concentrator deployment step.

### Concentrator: persist the rules across reboots — REQUIRED

`iptables -I` mutates only the **runtime** chain. On OCI Ubuntu the
`netfilter-persistent` boot service **restores `/etc/iptables/rules.v4`**, so a
reboot silently discards any runtime-only rule and the tunnel ACCEPT (and the
UDP-port ACCEPT) revert to the default REJECT with no signal until
re-provisioned (defect D7). The rules **must** therefore be written to the boot
rules file. On Debian/Ubuntu:

```sh
sudo apt-get install -y iptables-persistent   # provides netfilter-persistent
sudo netfilter-persistent save                # snapshots the runtime chain to /etc/iptables/rules.v4
```

(`service iptables save`, or a firewalld permanent rule, on EL.) Verify the
tunnel ACCEPT survives by confirming it appears in the saved file:

```sh
sudo grep -- '-A INPUT -i wanbond0 -j ACCEPT' /etc/iptables/rules.v4
```

The rule references the tunnel interface by name, so it only matches while
`wanbond0` exists; restoring it at boot (before the daemon starts) is harmless.

This save is **idempotent** — re-running it against an unchanged chain is a
no-op — and the real-host provisioning (`test/realhosts`,
`Provision`/`TestRealProvision`, run via `just realhosts-provision`) performs
and asserts exactly these steps automatically: it installs
`iptables-persistent`, inserts the runtime rule, runs `netfilter-persistent
save`, and re-inspects that the rule is present in `/etc/iptables/rules.v4`.

### Edge

Outbound UDP to the concentrator's `endpoint` (and any per-path `dest_addr`)
must be open on every uplink; no inbound rules are needed — the edge initiates.

## 6. Observability

Each daemon serves Prometheus metrics on the loopback-only `[metrics] listen`
address (`curl -s http://127.0.0.1:9090/metrics`). Logs go to stderr →
`journalctl -u wanbond-<role>`.

## 7. MTU

The daemon sets the TUN MTU itself from the bonded-overhead budget (see
`docs/p1-mtu.md`); do not override it. If an on-path MTU below the default
1500 is in play, see the TCP MSS-clamp guidance in that document.

## 8. Limitations

### UDP-blocking networks defeat wanbond (no TCP/TLS fallback — by design)

wanbond carries every path over **UDP** (WireGuard's transport). Its
DPI-resistance goal is that a network which *inspects* traffic cannot
distinguish the flow from ordinary UDP: the outer frame codec (amnezia
obfuscation + the FEC-framed Bind) removes the WireGuard/VPN fingerprint, so a
network doing protocol classification does not identify and block it. This is
verified by the `TestP5DPI` / `TestWireFormatAudit` e2e checks (requirement 6):
neither nDPI nor Suricata classifies the obfuscated flow as WireGuard or any
identified VPN.

Obfuscation does **not** help against a network that blocks UDP **wholesale** —
dropping all UDP (or all UDP except DNS/QUIC to specific resolvers), regardless
of payload. Against such a network wanbond cannot connect, and there is **no
in-scope mitigation**:

- There is **no TCP or TLS-tunnelled fallback transport**, and adding one is an
  **explicit non-goal** for this project. wanbond's value is WAN *bonding* with
  adaptive FEC over multiple real uplinks; a single TCP-over-TLS obfuscation
  transport (the domain of tools like obfs4/shadowsocks/`udp2raw`) is a
  different problem and is deliberately out of scope.
- Wholesale-UDP-block is distinct from **DPI classification**: obfuscation
  answers the latter (proven), not the former. A hostile network that lets *no*
  UDP through cannot be defeated by making the UDP look innocuous.

Operationally: if an uplink blocks UDP entirely, treat that uplink as
unavailable for wanbond. If **every** uplink blocks UDP, wanbond is not usable
on that site; use a different access network or an out-of-scope UDP-encapsulation
tool upstream of wanbond. The manual P5 checklist (`docs/manual-checklist.md`)
includes a step to confirm this failure mode is understood and, where a test
network permits, observed.

### DPI port-guessing on WireGuard's registered port 51820 (deployment note)

nDPI (and DPI engines generally) will label **any** UDP flow on WireGuard's
IANA-registered port **51820** as `WireGuard` / category `VPN` **by a port
guess alone — regardless of payload** (nDPI reports this as `Confidence: Match
by port`). This is a classification of the *port*, not the *wire format*: a
random-payload UDP flow to `:51820` is labelled WireGuard, while the identical
payload to a non-registered port is `Unknown`. wanbond's payload is verified
indistinguishable from random by `TestWireFormatAudit` (T26) and by
`TestP5DPI`, which reads nDPI's per-flow `Confidence` and only treats a
**payload/content** match (`Confidence: DPI`) as a classification — a port
guess is disregarded.

Deployment consequence: `wireguard.listen_port` is operator-configurable, so on
a hostile network that classifies by port, **prefer a non-registered UDP port**
(any high, unassigned port) for the concentrator's `listen_port` and the edge's
`endpoint`. This avoids the trivial port-based "VPN" label. It is a deployment
consideration, not a payload weakness — the obfuscated payload itself does not
identify the tunnel as WireGuard/VPN.
