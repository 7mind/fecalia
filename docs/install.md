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

`just release` (and `just build`) first runs `just web-build`, which builds the
monitoring-UI frontend (Vite + TypeScript, under `web/`) and embeds the bundle
into the binary via `//go:embed all:dist` in `internal/monitor` — this needs
node/npm, which the dev shell provides. A committed `dist/.gitkeep` keeps the
embed compilable even without running `web-build` first (so a tagless `go
build`/`just lint` never fails on a missing directory); `web-build` restores it
after Vite empties the directory, so the working tree stays clean.

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
endpoint = "203.0.113.7:51820"     # required on the edge; an ip:port literal
                                   # (default) OR a hostname:port behind the
                                   # opt-in `dns = true` below — see §Optional
                                   # [dns] resolver block.
# dns = true                        # OPTIONAL, default false. Enables hostname
# resolution for the endpoint above (e.g. endpoint = "hub.example.com:51820").
# The [dns] block is itself OPTIONAL: absent, hostnames resolve through the
# OS system resolver; see §Optional [dns] resolver block below.
# endpoints = ["203.0.113.7:51820", "198.51.100.7:51820"]  # ordered hub-failover
# form (Q18): first entry is the active/primary concentrator, the rest are
# ordered standbys tried in order when the active one is lost. Mutually
# exclusive with `endpoint` above — `endpoint` is just its one-element form,
# so a single-concentrator deployment keeps using it unchanged. Edge-only.
# Each entry may likewise be an ip:port literal or, with `dns = true`, a
# hostname:port. Hub failover (all-paths-down detection + switch/re-handshake
# to the next endpoint) is implemented: T54 config surface + T57 switch.
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

### Multi-peer concentrator (G4) — supporting multiple edges

A concentrator can bond traffic from **multiple edges** (e.g., a branch-office
edge, a mobile edge, a cloud gateway). Plural `[[wireguard.peers]]` blocks are
supported; with more than one peer, each edge authenticates with its own psk
for authenticated source-based demux:

```toml
role = "concentrator"
psk = "<base64 32-byte PSK>"       # REQUIRED by validation; authenticates no
                                   # peer once a second peer is configured
                                   # (see note below)

[[paths]]
name = "wan0"
source_addr = "10.0.0.5"

[wireguard]
private_key = "<base64 concentrator private key>"
listen_port = 51820

# Edge 1: office, its own PSK
[[wireguard.peers]]
public_key = "<base64 office-edge public key>"
allowed_ips = ["10.77.1.0/24"]
psk = "<base64 32-byte PSK for office-edge>"   # REQUIRED in multi-peer mode
name = "office"                                 # REQUIRED in multi-peer mode

# Edge 2: mobile, its own PSK
[[wireguard.peers]]
public_key = "<base64 mobile-edge public key>"
allowed_ips = ["10.77.2.0/24"]
psk = "<base64 32-byte PSK for mobile-edge>"   # Must differ from office's psk
name = "mobile"                                 # Must differ from office's name

[metrics]
listen = "127.0.0.1:9090"
```

**Single-peer back-compatibility:** when a concentrator has only one peer
(`[[wireguard.peers]]`), a per-peer `psk` is **rejected** at config load — leave
it unset; the top-level `psk` remains the sole authenticator, so existing
single-peer configs parse and run unchanged. A per-peer `name` is optional and
has no effect on metrics for a single bound peer (the `peer` label is omitted
entirely, not emitted empty).

**Multi-peer requirements and authentication:** with more than one peer, each
peer **must** have both a unique `psk` (config load rejects a duplicate) and a
unique `name`. Every peer authenticates its OWN PROBE frames with its own psk;
the concentrator learns each source address's owning peer only from a PROBE
that MAC-verifies under that peer's psk, and subsequent DATA/PARITY frames from
that source route accordingly without re-authentication. **The top-level `psk`
stays required by config validation in every configuration, but on a
multi-peer concentrator it authenticates no peer** — an existing single-peer
edge does NOT keep authenticating via the top-level psk once a second peer is
added; it must be given its own per-peer psk at that point.

**Metrics `peer` label:** every bound peer — including the first-configured
(primary) one — is exposed under its configured `name` as the metrics `peer`
label once a second peer is configured; `peer=""` appears only on a true
single-peer edge/hub/concentrator (D58).

**Edges see no change:** each edge always points to the concentrator's public
address (its public IP + port); it does not know or care that multiple edges
are connected to the same concentrator. The concentrator's internal demux is
entirely transparent to the edge.

### Multi-concentrator edge (G28) — multiple exit-capable concentrators

The **edge** may declare **several concentrator peers** and bond to all of them
concurrently over the **same set of uplinks** (G28/Q74). Each peer is a separate
tunnel to a separate concentrator; the sockets are shared (one UDP socket per
uplink, fanned out to every peer), while the per-peer scheduler / FEC /
resequencer state stays independent. Every peer marked `mode = "default-route"`
is an **exit-capable** alternate for the full-tunnel egress; the **first**
default-route peer in config order is the **boot-default exit** (the selection is
not persisted — the daemon always boots to the config default, Q74). On-the-fly
switching of the active exit and per-concentrator statistics are surfaced by the
monitor dashboard's web UI (T259/T260, G28/M107; see
[docs/design.md §Security model](design.md)); this section covers only the
**config surface**.

```toml
role = "edge"
psk = "<base64 32-byte outer-control PSK, same on all ends>"

# The SAME uplinks carry every concentrator bond.
[[paths]]
name = "starlink"
source_addr = "192.168.1.10"

[[paths]]
name = "5g"
source_addr = "192.168.2.10"

[wireguard]
private_key = "<base64 edge private key>"

# Concentrator A — the boot-default exit (first default-route peer).
[[wireguard.peers]]
public_key = "<base64 concentrator-A public key>"
name = "hub-a"                        # REQUIRED with more than one peer
psk = "<base64 32-byte PSK for hub-a>" # REQUIRED and pairwise-distinct
endpoint = "203.0.113.7:51820"        # each peer keeps its OWN endpoint /
                                      # endpoints failover list (Q72)
allowed_ips = ["0.0.0.0/0", "10.77.0.1/32"]  # full default route + its inner /32
mode = "default-route"

# Concentrator B — a warm-standby exit alternate.
[[wireguard.peers]]
public_key = "<base64 concentrator-B public key>"
name = "hub-b"
psk = "<base64 32-byte PSK for hub-b>"
endpoint = "198.51.100.7:51820"
allowed_ips = ["0.0.0.0/0", "10.77.0.2/32"]  # SAME default route + its OWN /32
mode = "default-route"

[metrics]
listen = "127.0.0.1:9090"
```

**Validation rules (enforced at config load, fail-fast):**

- A `0.0.0.0/0` / `::/0` entry is permitted **only** on a `mode = "default-route"`
  peer. With more than one exit-capable peer, they are alternates for the same
  egress role, so **every** exit-capable peer must list the **same** default-route
  entry set.
- **Each exit-capable peer must also carry at least one non-default allowed_ip**
  (its concentrator's inner address, e.g. its inner `/32`). A peer listing
  **solely** `0.0.0.0/0` is rejected, naming the offending peer — a standby exit
  renders only its non-default routes, so a `/0`-only peer would render an empty
  allowed_ips at boot. (The single-peer legacy full-tunnel form
  `allowed_ips = ["0.0.0.0/0"]` is unaffected — it still loads unchanged.)
- **Non-default** allowed_ips must **not overlap** across peers (each
  concentrator's inner address stays unambiguous). The shared `/0` default route
  is exempt — it is intentionally identical across the exit-capable peers.
- Every peer needs its **own** `endpoint` / `endpoints` (the per-concentrator
  T57 failover list is retained per peer); an **exact duplicate literal
  endpoint** `address:port` across two peers is rejected.
- With more than one peer the usual multi-peer rules apply on the edge exactly as
  on the concentrator: each peer's `name` and `psk` are **required and
  pairwise-distinct**.
- There is **one shared** `[scheduler]` / `[fec]` / `[reseq]` policy block that
  applies uniformly to every peer bond (Q71) — there are no per-peer policy
  fields (a per-peer `policy` key or `[wireguard.peers.scheduler]` sub-table is
  rejected as an unknown key).

**Probe fan-out budget (N × U):** the edge runs one liveness/RTT prober per
`(concentrator peer, uplink)` pair, so the probe fan-out is **N concentrator
peers × U uplinks**. The uplink **sockets** do not multiply by N (all peers share
each uplink's socket), but the **probers** do: each emits one PROBE per fixed
200 ms interval plus its reflected echo, and those frames bypass the pacer
(charged-but-never-shed), drawing on each uplink's token bucket. Config load
rejects a config whose computed fan-out exceeds the budget of **32 probers**,
with a message stating the computed value vs. the available budget. Worked
example: the 2-concentrator edge above over its 2 uplinks is `2 × 2 = 4` probers,
well within budget; an 8-concentrator edge over 4 uplinks is `8 × 4 = 32`, at the
ceiling; adding a 5th uplink (`8 × 5 = 40`) is rejected — reduce the concentrator
or uplink count.

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
failover); an optional `[scheduler]` block can instead select the
**weighted-aggregation** policy. Independently of that choice, `[scheduler]`
also turns on, off by default, per-path send-**pacing** (token buckets that
bound bufferbloat under sustained load) — pacing is **policy-independent**
(D65/T152/T153): it is available, and configured the same way, under either
`policy = "active-backup"` (the default) or `policy = "weighted"`.

```toml
[scheduler]
# policy = "weighted"      # optional; omitted/"active-backup" is the default
pacing_enabled = true       # OFF by default; when on, size the pace from the links below
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

The two policies size the resulting pace **differently**, because they egress
differently:

- **Weighted** stripes every path simultaneously, so ONE reference capacity
  applies to every path's token bucket: a heterogeneous link set is sized to
  the **slowest declared link** (the bottleneck) — pacing any path faster than
  that would let it build the very standing queue pacing exists to prevent.
- **Active-backup** egresses on exactly ONE path at a time, so each path is
  paced from **its own** BDP into a PER-PATH capacity/burst — NOT min-reduced
  to the bottleneck. A fast active primary paces at its own drain rate even
  when a much slower path is configured as its backup; only the currently
  ACTIVE path's bucket is drained.

Common rules, either policy:

- The declaration is **operator-declared, not auto-tuned**: the value is fixed at
  load; wanbond does not adjust it live. Measure it once per link.
- It is **all-or-nothing**: declare `link_bandwidth` (and `link_rtt`) on *every*
  path or none — a partial declaration is rejected.
- A declared bandwidth with `pacing_enabled = false` (the default) is **inert** —
  the synthetic default pace is kept and the tunnel behaves as before.
- `link_bandwidth` is **mutually exclusive** with the raw `per_path_capacity_fps` /
  `pacing_burst_frames` knobs: declare the link bandwidth *or* set the frame-slot
  knobs, not both. A non-positive or unparseable bandwidth/RTT is rejected at load.
- Under active-backup, pacing enabled with **neither** a declared
  `link_bandwidth` **nor** the explicit `per_path_capacity_fps` +
  `pacing_burst_frames` pair fails config load fast — active-backup never
  falls back to the weighted synthetic default pace, since a nominally-on but
  unbound pace would silently reproduce the D65 bufferbloat this feature
  fixes. (Weighted keeps its pre-existing synthetic-default fallback.)

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
  you must declare it on all (under the weighted policy wanbond sizes the shared
  pace to the slowest link, the bottleneck; under active-backup, the default,
  each path is sized from its own declared link instead). Partial declarations
  are rejected at load.
- **Round conservatively.** Round down if unsure (e.g., measure 49.8 Mbit/s →
  declare `49Mbit` rather than `50Mbit`). Under-sized pacing is safe; over-sized
  pacing may not bind and bufferbloat may occur.
- **Use the `link_rtt` from Step 1**, not the loaded RTT from Step 2. The idle RTT
  is the baseline delay the BDP calculation assumes; the loaded RTT tells you how
  much bufferbloat exists.

#### Step 4: Enable pacing and deploy

Enable pacing in the edge config; this works under either scheduler policy
(pacing is policy-independent — see above). Under the default active-backup
policy:

```toml
[scheduler]
# policy defaults to "active-backup"; no need to set it
pacing_enabled = true
```

or, under weighted aggregation:

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

### 3b. Policy-routing edge topologies: source-IP pinning with `bind = "source"`

**Symptom:** On a VLAN-per-WAN edge with `ip rule from <source_addr>` policy routing,
the tunnel silently fails (ENETUNREACH on all packets), even though `ping -I <source_ip>`
proves the WAN interface works.

**Root cause:** wanbond's default `bind` mode is `"auto"`, which selects `SO_BINDTODEVICE`
(device bind, wildcard source) on one-address interfaces — the exact case of a VLAN-per-WAN
edge. A wildcard-source socket never matches `ip rule from <source_addr>`, so the route
lookup falls through to the main table, finds no route to the concentrator via that VLAN,
and returns ENETUNREACH. The tunnel's UDP packets are silently dropped at the IP layer; no
error appears in the daemon's logs. This failure was observed in production (D38).

**Workaround 1: Policy rule with output interface (production recipe):**

Add an `oif <dev>` (output interface) clause to your policy rule **instead of** a
`from <source_ip>` clause. `oif` and `from` are ANDed together, not ORed — and a
wildcard-source device-bind socket, which is exactly the root cause above, never
matches `from <source_ip>`. The `from` clause would therefore make the rule just as
dead as the `ip rule from <source_addr>` rule that caused the symptom. Match on
output interface alone instead; that stays correct regardless of which source
address the socket ends up using:

```sh
# Replace <dev> and <N> with your values.
# <dev>: the WAN interface name (e.g., eth0.231)
# <N>: the routing table number (e.g., 10)

ip rule add oif <dev> table <N> prio 100
ip route add default dev <dev> via <gateway-ip> table <N>
```

This is the recipe actually used to work around D38 in production. **It is not
reboot-persistent** — `ip rule`/`ip route` are runtime-only kernel state and do not
survive a reboot or an interface recreate, regardless of `ip rule` ordering. Persist
it the same way this document persists other runtime-only `ip`/`nft`/`iptables` state
elsewhere: fold the `ip rule add`/`ip route add` calls into the role's re-apply script
under the `wanbond-addressing@.service` oneshot (§4, "Persistence recipe for
non-networkd hosts"), or into whatever equivalent boot-time mechanism replays your
policy routing (cf. the `netfilter-persistent` pattern in §5 for the analogous
firewall-rule case).

**Workaround 2: Per-path `bind = "source"` toggle (recommended):**

Add `bind = "source"` to each `[[paths]]` block in the config. This forces the path socket
to bind to the source IP (pre-T16 behavior) instead of the device, ensuring the
wildcard-source collision never occurs. The socket's source IP will match your
`ip rule from` clauses:

```toml
[[paths]]
name = "starlink"
source_addr = "192.168.1.10"
bind = "source"

[[paths]]
name = "5g"
source_addr = "192.168.2.10"
bind = "source"
```

If every path in a VLAN-per-WAN topology needs source binding, set the top-level
default instead of repeating `bind = "source"` on each `[[paths]]` block — a path
only needs its own `bind` when it deviates from the default:

```toml
bind = "source"

[[paths]]
name = "starlink"
source_addr = "192.168.1.10"

[[paths]]
name = "5g"
source_addr = "192.168.2.10"
```

Accepted `bind` values (top-level default, and per-path override) are:

- `"source"` — force source-IP binding (defeats device-bind roam tolerance, safe for
  policy-routed topologies)
- `"device"` — force device binding (requires manual rule workaround for policy routing)
- `"auto"` (default) — automatic heuristic: device-bind only when the source resolves to
  a non-loopback interface, that interface carries exactly one address of the source's
  family, AND exactly one configured path resolves to that interface (two paths sharing
  one device fall back to source binding, since two wildcard+device sockets on the same
  port would collide `EADDRINUSE`); every other case source-binds.

Restart the daemon after changing the config:

```sh
systemctl restart wanbond-edge
```

### 3z. Full configuration reference (all keys)

This is the exhaustive key list — every configuration key wanbond reads, in one
place. The example below is **edge-oriented**; concentrator-only keys are shown
and marked `CONCENTRATOR-ONLY`. Required keys are shown live; optional/defaulted
keys are shown **commented-out with their default value**. Uncommenting a key
and leaving it at the shown value is a no-op. The per-section notes after the
example capture the cross-field constraints a single example cannot express
inline. All of these are enforced at config load (`config.Load`), which fails
fast on the first violation — except the loopback check and the `allowed_ips`
non-empty check, noted below.

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
# bind = "auto"                    # OPTIONAL, DEFAULT "auto". Top-level default
                                   #   bind mode applied to every [[paths]] block
                                   #   that omits its own `bind`. "source" |
                                   #   "device" | "auto" — see §3b for the
                                   #   VLAN-per-WAN policy-routing case this exists
                                   #   to fix, and the per-path `bind` note below.

# ── paths: one [[paths]] block per WAN uplink; at least one is REQUIRED ───────
[[paths]]
name        = "starlink"           # REQUIRED. Stable, unique identifier.
source_addr = "192.168.1.10"       # REQUIRED. Bare local source IP the path's
                                   #   UDP socket binds to. Must be unique across
                                   #   paths (a shared source collides EADDRINUSE
                                   #   at the second bind). No default.
# bind = "auto"                    # OPTIONAL, DEFAULT the top-level default
                                   #   above (itself DEFAULT "auto"). "source"
                                   #   forces the pre-T16 source-IP pin
                                   #   unconditionally; "device" forces
                                   #   SO_BINDTODEVICE unconditionally; "auto"
                                   #   reproduces today's heuristic (device-bind
                                   #   only when provably equivalent to the
                                   #   source-IP pin, source-bind otherwise). See
                                   #   §3b for the policy-routing recipe.
# dest_addr = "203.0.113.7:51820"  # OPTIONAL, edge-only meaning. Per-path
                                   #   concentrator endpoint (ip:port). Omit when
                                   #   one public IP fronts all uplinks (the
                                   #   peer's endpoint is reused). No default.
                                   #   Inert on the concentrator (it learns edge
                                   #   endpoints from traffic).
# link_bandwidth = "50Mbit"        # OPTIONAL. Operator-declared bottleneck
                                   #   bandwidth. SI bit/s: k/M/G = 1e3/1e6/1e9
                                   #   ("bit" may be written "bps"). Must be > 0.
                                   #   Used ONLY with scheduler.pacing_enabled
                                   #   (either policy — sized to the shared
                                   #   bottleneck under weighted, per-path under
                                   #   active-backup); inert otherwise. No
                                   #   default (undeclared).
# link_rtt = "45ms"                # OPTIONAL. Operator-declared baseline RTT
                                   #   (Go duration). REQUIRED (> 0) when
                                   #   link_bandwidth is set with pacing enabled
                                   #   (either policy); ignored otherwise. No
                                   #   default.
# mtu = 1500                       # OPTIONAL, DEFAULT 0 (unset = AUTO-DISCOVER:
                                   #   the daemon PMTU-probes the path and sizes
                                   #   wanbond0 to fit, re-probing on roam). Set
                                   #   it to PIN the path (skip discovery);
                                   #   must be 1280..9000 and leave a derived
                                   #   inner (TUN) MTU >= 576 after the fixed
                                   #   ~100-byte outer overhead (IP/UDP + outer
                                   #   DATA frame + WireGuard transport). Pin on
                                   #   any uplink whose real path MTU is below
                                   #   1500 (e.g. some cellular APNs).
# ride_through = "5s"              # OPTIONAL, DEFAULT 0. This path's liveness
                                   #   ride-through duration (Go duration
                                   #   string; D86). Config-surface-only this
                                   #   pass (T203) — parses and validates
                                   #   (>= 0) but is not yet wired into
                                   #   failover behavior.

# A second uplink (edge). Repeat the block per path.
[[paths]]
name        = "5g"
source_addr = "192.168.2.10"
mtu         = 1400                 # This carrier's cellular APN caps the path
                                   #   MTU below 1500 — declare it so the tunnel
                                   #   never IP-fragments on this leg.

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
                                   #   ip:port literal (default) OR hostname
                                   #   with opt-in `dns = true` (§Optional
                                   #   [dns] resolver block). CONCENTRATOR:
                                   #   must be ABSENT (rejected) — it roams
                                   #   the edge dynamically. Mutually
                                   #   exclusive with `endpoints`.
# endpoints = ["203.0.113.7:51820", "198.51.100.7:51820"]
                                   # EDGE-ONLY ordered hub-failover list: [0] is
                                   #   the active concentrator, the rest ordered
                                   #   standbys. Each entry may be an ip:port
                                   #   literal or, with `dns = true`, a
                                   #   hostname; duplicates rejected within
                                   #   each form. Mutually exclusive with
                                   #   `endpoint` (which is its one-element
                                   #   form). Rejected on the concentrator.
allowed_ips = ["10.77.0.1/32"]     # REQUIRED: >= 1 CIDR routed to this peer
                                   #   (enforced when the WG UAPI is built). A
                                   #   literal 0.0.0.0/0 or ::/0 is always split
                                   #   into the equivalent /1+/1 pair at UAPI
                                   #   render.
# mode = "default-route"           # OPTIONAL, edge-only. Marks this peer as an
                                   #   exit-capable full-tunnel concentrator:
                                   #   the daemon installs the active exit
                                   #   peer's allowed_ips split (wg-quick /1+/1
                                   #   for 0.0.0.0/0) as routes via wanbond0
                                   #   once the interface is up, and removes
                                   #   them on stop. Routes ONLY — no policy
                                   #   routing/SNAT/forwarding, which stay
                                   #   operator-owned (§4). Rejected on the
                                   #   concentrator. SEVERAL edge peers may
                                   #   carry the mode (T250 multi-exit; first in
                                   #   config order is the boot-default exit) —
                                   #   see §Multi-concentrator edge (G28).

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
                                   #   MTU: with this block set, wanbond reserves
                                   #   max(s1, s2) bytes of junk-prefix headroom in
                                   #   the derived TUN MTU and per-path PMTU
                                   #   discovery (D85) so full-size obfuscated DATA
                                   #   still fits the path MTU; larger s1/s2 => a
                                   #   smaller inner (wanbond0) MTU.
# h1 = 1                           # magic header: initiation. Default 1..4 when
# h2 = 2                           #   the block is configured but NO header is
# h3 = 3                           #   given. When any is set, h1..h4 must be a
# h4 = 4                           #   distinct set (values <= 4 mean "standard
                                   #   message type").

# ── scheduler: OPTIONAL. Omitted => active-backup, pacing off ─────────────────
# [scheduler]
# policy = "active-backup"         # "active-backup" (DEFAULT) | "weighted".
                                   #   pacing_enabled/per_path_capacity_fps/
                                   #   pacing_burst_frames below apply under
                                   #   EITHER policy (policy-independent
                                   #   pacing, D65/T152/T153); the
                                   #   engage_fraction..weight_loss_floor knobs
                                   #   apply ONLY to "weighted" and stay
                                   #   inert/unset under active-backup.
# per_path_capacity_fps = 10000.0  # DEFAULT 10000. Weighted: aggregation-gate
                                   #   denominator and shared pacing refill
                                   #   rate. Active-backup: explicit per-path
                                   #   pacing refill rate (replicated across
                                   #   paths) when no link_bandwidth is
                                   #   declared. > 0 when set.
# engage_fraction    = 0.9         # DEFAULT 0.9. Weighted only. Engage
                                   #   aggregation above engage_fraction *
                                   #   capacity. In (0, 1].
# disengage_fraction = 0.5         # DEFAULT 0.5. Weighted only. Collapse below
                                   #   disengage_fraction * capacity. Must be in
                                   #   [0, engage_fraction) — the hysteresis band.
# collapse_dwell = "2s"            # DEFAULT 2s. Weighted only. Sustained-low
                                   #   dwell before collapsing to primary-only.
                                   #   >= 0.
# load_tau = "200ms"               # DEFAULT 200ms. Weighted only. Offered-load
                                   #   rate estimator time constant. > 0.
# pacing_enabled = false           # DEFAULT false. Turn on per-path send-pacing
                                   #   (token buckets) under EITHER policy. Off
                                   #   => buckets bypassed.
# pacing_burst_frames = 64.0       # DEFAULT 64. Token-bucket burst (frames),
                                   #   either policy. > 0 when pacing_enabled.
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

# ── liveness: OPTIONAL. Omitted => 1200ms down_after (today's fixed value) ────
# [liveness]
# down_after = "1200ms"            # DEFAULT 1200ms. Silence duration that
                                   #   marks an UP path DOWN (Go duration
                                   #   string; D86). FLOOR: must be >= 400ms
                                   #   (2x the fixed, non-configurable 200ms
                                   #   probe interval) — a lower value makes
                                   #   the liveness detector's silence check
                                   #   outrun the echo cadence and permanently
                                   #   flap every path DOWN; rejected at load.
                                   #   No upper bound is enforced.

# ── metrics: OPTIONAL. Omit the block => no /metrics endpoint is served ───────
[metrics]
listen = "127.0.0.1:9090"          # No default. LOOPBACK-ONLY (127.0.0.0/8,
                                   #   ::1, or a hostname resolving only to
                                   #   loopback); any other address is REFUSED
                                   #   when the endpoint binds (not at load).

# ── monitor: OPTIONAL. Omit the block (or leave listen empty) => no monitoring-
#    UI endpoint is served. Read-only live-stats dashboard; see §6c. ──────────
# [monitor]
# listen = "127.0.0.1:9101"        # No default. LOOPBACK-ONLY unless token
                                   #   (below) is set, in which case a
                                   #   non-loopback bind is ALLOWED. An
                                   #   unauthenticated non-loopback bind is
                                   #   REFUSED AT CONFIG LOAD (fail-fast, not at
                                   #   bind time — stricter than metrics above).
# token = "..."                    # OPTIONAL when listen is loopback; REQUIRED
                                   #   when listen is non-loopback. See §6c for
                                   #   the token-cookie flow and the accepted
                                   #   cleartext-on-LAN residual risk.
# reveal_addressing = false        # OPTIONAL, DEFAULT false. Opt-in that
                                   #   discloses per-path source IPs, hub
                                   #   endpoint addresses, and connected-edge
                                   #   sources to token holders on a
                                   #   non-loopback bind. Does NOT bypass the
                                   #   token requirement above; see §6c.

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
  concentrator must set neither. Each entry is an `ip:port` literal (default)
  or, with the peer's `dns = true` opt-in, a hostname resolved at runtime
  (never at config load); duplicates are rejected within each form
  (literal-vs-literal, hostname-vs-hostname) — see §Optional `[dns]` resolver
  block.
- **Distinct `source_addr`.** Each `[[paths]]` needs a unique, valid
  `source_addr` (compared unmapped, so `192.0.2.10` and `::ffff:192.0.2.10`
  collide). `name` must also be unique.
- **`allowed_ips`.** At least one CIDR per peer; empty is rejected when the WG
  configuration is assembled (not by `config.validate`). Every entry's CIDR
  syntax IS parsed and validated by `config.validate` (naming the peer and the
  offending entry on a malformed prefix, e.g. an out-of-range `/33` or a typo),
  so a bad entry fails fast at load instead of surfacing later as an opaque
  UAPI `allowed_ip=` rejection at daemon start. A literal `0.0.0.0/0` or `::/0`
  (full tunnel) is ALWAYS split into the equivalent `/1`+`/1` pair at UAPI
  render — the engine never receives the literal `/0`, which wedges the
  handshake. A duplicate `0.0.0.0/0`/`::/0` entry within ONE peer's
  `allowed_ips` is rejected on either role. The CROSS-peer default-route rules
  are role-split (T250):
  - **Concentrator:** a `0.0.0.0/0`/`::/0` entry may appear on at most ONE
    peer per address family across the whole config — WireGuard cryptokey
    routing makes overlapping `allowed_ips` last-writer-wins, a silent
    misconfig (D59, unchanged).
  - **Edge:** a `0.0.0.0/0`/`::/0` entry is permitted ONLY on a
    `mode = "default-route"` (exit-capable) peer. With more than one
    exit-capable peer, every exit-capable peer must carry the SAME
    default-route entry set (they are alternates for the same egress role)
    AND at least one NON-default allowed_ip (its concentrator's inner
    address, e.g. its inner `/32`); non-default `allowed_ips` must not
    overlap across peers. See §Multi-concentrator edge (G28) above.
- **`mode` is edge-only.** Peer `mode = "default-route"` marks a peer as an
  exit-capable full-tunnel concentrator (an opt-in alongside a
  `0.0.0.0/0`/`::/0` `allowed_ips` entry); rejected on the concentrator,
  mirroring the `endpoint`/`dns` edge-only rules. Multiple edge peers may
  carry the mode (T250 — each is an exit-capable alternate; the FIRST
  default-route peer in config order is the boot-default exit). When set, the
  daemon installs the ACTIVE exit peer's `allowed_ips` split (the wg-quick
  `/1`+`/1` pair for `0.0.0.0/0`) as scope-link routes via `wanbond0` once the
  interface is up, and withdraws them on stop — the daemon's ONLY route
  programming. It is deliberately minimal: routes ONLY, **no** client-LAN
  policy routing, SNAT, or concentrator `ip_forward`/`MASQUERADE`/`FORWARD` —
  those stay operator-owned recipes (see §4). Without the mode, no route is
  ever installed.
- **amnezia all-or-nothing.** An unconfigured (all-zero) block is plain
  WireGuard. Once *any* of `jc/jmin/jmax/s1/s2/h1..h4` is set, the full
  `jc,jmin,jmax,s1,s2` set must be `> 0`, `jmin <= jmax`, the init/response
  lengths must not collide (`148+s1 != 92+s2`), and `h1..h4` must be a distinct
  set (omitted headers default to `1..4`). The same profile must be set on both
  ends. One obfuscation engine per process.
- **`link_bandwidth` + `link_rtt` are a pair.** Both-or-neither, and
  **all-or-nothing across every path**: declare on every `[[paths]]` block or
  none (under weighted the shared pace is sized to the slowest declared link;
  under active-backup each path is sized from its own — either way undefined
  under a partial declaration). They take effect **only** with
  `pacing_enabled = true`, under **either** `scheduler.policy` (pacing is
  policy-independent, D65/T152/T153); otherwise a declared bandwidth is inert.
  When active they are **mutually exclusive** with the raw
  `scheduler.per_path_capacity_fps` / `pacing_burst_frames` knobs — declare
  link bandwidth *or* set the frame-slot knobs, not both.
- **`link_bandwidth` vs. the aggregation engage threshold (weighted only).** When
  `scheduler.policy = "weighted"`, every path that declares `link_bandwidth` is
  checked against the (effective, post-default) engage threshold: `Load` FAILS
  FAST if `scheduler.engage_fraction * scheduler.per_path_capacity_fps` exceeds
  the bandwidth-implied capacity (`link_bandwidth / (8 * 1500 bytes)`,
  frames/s) — i.e. aggregation could mathematically never engage on that path
  at line rate. With `pacing_enabled = true` and a declared bandwidth this can
  never fire (§3a auto-derives `per_path_capacity_fps` from the same
  bandwidth); it chiefly bites with pacing **disabled** (the synthetic default
  `per_path_capacity_fps = 10000` left standing against a much slower declared
  link) or with the raw knobs set explicitly. Fix by lowering
  `per_path_capacity_fps`, enabling `pacing_enabled` to auto-derive it (see
  §3a), or correcting `link_bandwidth`.
- **scheduler off-unless-present.** No `[scheduler]` block ⇒ `active-backup` and
  every weighted knob ignored. Under `weighted`, omitted knobs take the defaults
  shown; the hysteresis band requires `disengage_fraction < engage_fraction`.
- **fec off-unless-present.** No `[fec]` block (or `enabled = false`) ⇒ no
  parity on the wire. `adaptive = true` requires `enabled = true`. In adaptive
  mode set **either** `target_residual` (recommended, `(0,1)`) **or**
  `safety_factor` (`>= 1`), never both; both must stay 0 (unset) in fixed mode.
- **liveness off-unless-present (D86, T203).** No `[liveness]` block ⇒
  `down_after` defaults to `1200ms`, byte-identical to before this key
  existed. `down_after` must be `>= 400ms` (2x the fixed, non-configurable
  200ms probe interval — a lower value makes the liveness detector's Tick
  silence check outrun the echo cadence and permanently flap every path DOWN)
  or `Load` fails fast; no upper bound is enforced yet (a large `down_after`
  loads fine — the WARN-and-allow budget check is a later task). Per-path
  `ride_through` (`>= 0`, DEFAULT 0) is a **config-surface-only** addition
  this pass: it parses and validates but is not yet consumed by the running
  scheduler.
- **metrics loopback-only.** `[metrics] listen` must be a loopback address (or a
  hostname resolving entirely to loopback); a non-loopback bind is refused when
  the endpoint starts. Omit the block to serve no metrics at all.
- **`bind` (top-level default + per-path override).** `"source"`, `"device"`, or
  `"auto"` (DEFAULT). A `[[paths]]` block with no `bind` uses the top-level
  default; the top-level default is itself `"auto"` when also omitted, so a
  config with no `bind` anywhere keeps exactly today's per-path bind behavior.
  See §3b for the VLAN-per-WAN policy-routing case `bind = "source"` exists to
  fix.
- **`mtu` (per-path, optional).** `0` (DEFAULT, unset) means auto. When set,
  must be `1280..9000` and leave a derived inner MTU `>= 576` after the fixed
  ~100-byte outer overhead (IP/UDP + outer DATA frame + WireGuard transport).
  Declares the path's OUTER underlay MTU only — see §7 for what wanbond does
  (and does not yet do) with the declared value.

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
- Device-bind paths (`bind = "auto"` or `"device"`, `SO_BINDTODEVICE`) need no
  further capability on Linux >=5.7: since kernel commit c427bfec18f21 ("net:
  core: enable SO_BINDTODEVICE for non-root users"), binding a not-yet-bound
  socket to a device requires no capability at all — empirically confirmed
  (D40) with `CapabilityBoundingSet=CAP_NET_ADMIN` unchanged. Pre-5.7 kernels
  need `CAP_NET_RAW`, which these units deliberately don't grant; on such a
  kernel device-bind fails with EPERM and the daemon transparently falls back
  to source-IP binding (see [install.md §3b](#3b-policy-routing-edge-topologies-source-ip-pinning-with-bind--source)
  for when that fallback matters). All kernels this project supports
  (Debian bookworm 6.1+, Ubuntu 22.04 5.15+) are already >=5.7. For a
  `bind = "device"` path this fallback logs a WARN naming the path and the
  interface (D53) — it silently loses the roam-survival property `"device"`
  exists for, so it is worth watching for in the daemon's logs on a pre-5.7
  host or an interface that has gone away. The WARN fires only once a
  source-IP-pinned socket has actually bound AND been installed as a live
  path in the running bond. If the interface is unresolvable AND the
  source-IP fallback itself cannot bind either (the path stays deferred, not
  falling back to anything), a distinct "still deferred" WARN is logged
  instead, once per unresolvable spell rather than once per second of the T55
  background reconcile retry. If the fallback bind succeeds but installing it
  into the running bond then fails (a wiring defect), that reconcile tick logs
  neither WARN — the socket is closed and the path stays deferred for a clean
  retry next tick — rather than claiming a fallback that did not actually
  take.

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
but otherwise owns ONLY the tunnel engine: **it never assigns addresses**, and
**installs no routes** — with ONE narrow exception: a peer marked
`mode = "default-route"` (see §3) makes the daemon install that peer's
`allowed_ips` split (the wg-quick `/1`+`/1` pair for `0.0.0.0/0`) as scope-link
routes via `wanbond0`, and remove them on stop. That exception is routes ONLY —
no policy routing, SNAT, or concentrator forwarding, which remain operator-owned
(put them in the addressing script below). Addressing is always operator-owned (wg-quick
style, no privileged shell-outs); assign it with a **systemd-networkd `.network`
file**, using the inner addresses from `allowed_ips`:

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

### Concentrator: NAT/forwarding prerequisites for routed traffic (C6)

Required **only** when the concentrator routes traffic on beyond itself — any
full-tunnel / client-LAN recipe (§9). Skip this subsection for a plain
point-to-point tunnel that never carries traffic addressed past the tunnel
endpoints themselves. All three items are **operator-owned** — the daemon
programs none of `ip_forward`, NAT, or firewall rules — and match the
production Pi/o3 deploy (`wanbond-fixes.md` §C3/C6).

1. **`ip_forward` — REQUIRED.** Off by default on most distros; without it the
   kernel never routes tunnel packets on to the WAN, full stop:

   ```sh
   sudo sysctl -w net.ipv4.ip_forward=1
   echo 'net.ipv4.ip_forward = 1' | sudo tee /etc/sysctl.d/99-wanbond-forward.conf
   ```

2. **`MASQUERADE` the tunnel network out the WAN interface — REQUIRED.**
   `<tunnel-net>` is the inner WireGuard subnet (e.g. `10.77.0.0/24`);
   `<wan>` is the concentrator's actual internet-facing NIC — **not**
   `wanbond0` (on o3 this is the private `enp0s6` that OCI itself NATs to the
   public IP; substitute your own WAN-facing interface):

   ```sh
   sudo iptables -t nat -A POSTROUTING -s <tunnel-net> -o <wan> -j MASQUERADE
   ```

3. **`FORWARD`: accept both directions — REQUIRED, easy to miss.** The
   "Concentrator: tunnel-interface ACCEPT" rule above (`-i wanbond0 ACCEPT`)
   is an **`INPUT`** rule; it only covers traffic *terminating on* the
   concentrator and says nothing about `FORWARD`, which is what packets pass
   through when the concentrator merely routes them on to the WAN and back.
   The default `FORWARD` policy is `REJECT`/`DROP` on the same distros/clouds
   called out above (OCI included), so it silently drops one direction unless
   both of these are present:

   ```sh
   # outbound leg: client-subnet traffic entering FORWARD via wanbond0
   sudo iptables -I FORWARD -i wanbond0 -j ACCEPT
   # return leg: the outbound-leg ACCEPT above covers only that ONE
   # direction — without this conntrack-ESTABLISHED accept too, the default
   # FORWARD REJECT/DROP silently drops all RETURN traffic and the tunnel
   # looks one-way-dead from the client's perspective
   sudo iptables -I FORWARD -o wanbond0 -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
   ```

**Persistence.** All of the above (the `sysctl.d` drop-in aside) are
runtime-only mutations, same as the tunnel-interface ACCEPT above — persist
them with the identical `netfilter-persistent` flow already covered in
"Concentrator: persist the rules across reboots" (this section):

```sh
sudo netfilter-persistent save
```

`net.ipv4.ip_forward` written to `/etc/sysctl.d/` (step 1) is already
persistent across reboots without further action (`sysctl --system` re-reads
`/etc/sysctl.d/` on boot).

### Edge

Outbound UDP to the concentrator's `endpoint` (and any per-path `dest_addr`)
must be open on every uplink; no inbound rules are needed — the edge initiates.

## 6. Observability

Each daemon serves Prometheus metrics on the loopback-only `[metrics] listen`
address (`curl -s http://127.0.0.1:9090/metrics`) and, optionally, a read-only
monitoring-UI dashboard on `[monitor] listen` (§6c). Logs go to stderr →
`journalctl -u wanbond-<role>`.

### 6a. Tunnel restart guidance and convergence checking (interim, until D36 is fixed)

**This section documents interim behavior** — restarting tunnel endpoints leaves
the session wedged for minutes under specific conditions. Once defect D36
(stale-session peer does not promptly re-handshake) is fixed, the reconvergence
timing will improve; until then, use the guidance below.

**The operational check:** the tunnel is up and carrying traffic when the
**`wanbond_session_established` metric reads `1`** (or the log shows `session
established`). These signal the same event: the WireGuard peer has completed a
handshake within the session-validity window (RejectAfterTime, ~180 s / ~3
minutes).

- **`wanbond_session_established` Prometheus gauge:** a binary (0 or 1) metric
  on each daemon's `/metrics` endpoint. Query it to confirm the tunnel is
  converged:
  ```sh
  curl -s http://127.0.0.1:9090/metrics | grep wanbond_session_established
  ```
  Expected output when the tunnel is up: `wanbond_session_established 1`.
  When converging or wedged: `wanbond_session_established 0`.

- **`session established` log line:** emitted once per session-establish event
  at INFO level. Watch the daemon's logs:
  ```sh
  journalctl -u wanbond-edge -f | grep "session established"
  ```
  When the tunnel completes its initial handshake or re-establishes after a
  restart, this line appears exactly once, carrying the last handshake age in
  milliseconds (`last_handshake_age_ms`).

**Restarting one endpoint leaves the tunnel down for minutes (D36):**

When you restart **only one end** (edge or concentrator) while the other remains
live, the restarted end initiates a new handshake promptly. However, the
**stale-peer (still running) does not detect the new handshake or re-handshake
promptly** — it keeps the old session cached and does not proactively re-initiate.
Result: the tunnel is down at the restarted end (new session not yet up) and at
the stale end (old session cached, but its peer no longer matches — no traffic
flows). The stale end does not recover until WireGuard's own rekey timers force
it: RekeyAfterTime (120 s) is when a healthy sender attempts a proactive rekey,
and RejectAfterTime (180 s) is the hard ceiling past which the stale session is
discarded and a fresh handshake is required. The outage is therefore
**minutes-scale** (bounded by these timers, up to ~3 minutes) — not hours.

**Fast path: restart both ends together (~25 s observed):**

Restarting **both ends within a short window** (e.g., within the same minute)
sidesteps the stale-session hang. Both re-handshake together and reconverge
within seconds once the faster end detects the other is live.

Procedure:
1. On the concentrator (or hub): `systemctl restart wanbond-concentrator`
2. Immediately (within 30 s) on the edge: `systemctl restart wanbond-edge`
3. Watch for convergence on one end:
   ```sh
   journalctl -u wanbond-edge -f | grep "session established"
   ```
   Typical reconvergence time: 10–25 seconds from the second restart.

**Distinguishing converging from wedged:** after a coordinated both-end
restart, expect the `session established` log line (and the gauge flipping to
`1`) within ~25 s. If `wanbond_session_established` still reads `0` well
beyond that — or beyond the ~3-minute rekey window following a one-sided
restart — that indicates the D36 wedge rather than ordinary convergence.

**Stale-end caveat:** for up to ~180 s (~3 min) after the *peer* restarts, the
end that did **not** restart can still read `wanbond_session_established 1` —
its own last handshake is still inside the 180 s validity window — even though
the tunnel carries no traffic (the peer on the other side no longer matches
keys). A single end reading `1` is therefore not, by itself, a guarantee of
live traffic during the wedge window; check both ends, or wait out the
~3-minute window, before concluding the tunnel is healthy.

**Avoid:** restarting a single end for maintenance. This is a D36 issue with
the **inner WG tunnel session** (the whole encrypted tunnel), distinct from a
single outer path/uplink flapping — that failure mode is already handled by
the D12 fix and self-heals in seconds. A one-sided restart instead leaves the
**entire tunnel** down for minutes (see above), not just one uplink. If you
must restart one end, budget minutes of full-tunnel downtime; prefer the
coordinated both-ends restart above (~25 s total) whenever possible. Until
D36 is fixed, coordinate endpoint restarts — both together is far faster than
one at a time.

### 6b. Weighted-policy capacity-sanity check (T144)

Under `scheduler.policy = "weighted"`, at startup the daemon checks whether
every `[[paths]]` block declares `link_bandwidth` — the precondition for the
aggregate capacity accounting (`per_path_capacity_fps` / the engage threshold)
to be provably sane. This is a **soft** check — it never blocks startup —
distinct from the **hard** `link_bandwidth`-vs-`engage_fraction` guard
(config load refuses outright if a *declared* bandwidth cannot sustain the
engage threshold; see [§3a](#3a-tuning-per-link-bandwidth-and-pacing)). This
check instead covers the paths that guard cannot see: any path that leaves
`link_bandwidth` undeclared.

- **`wanbond_weighted_capacity_sane` Prometheus gauge:** an unlabeled 0/1
  metric. Its value is config-derived (not re-read at scrape time); it is
  seeded at startup and **re-set on a `reload`** whose path set changed the
  verdict (a divergence is logged — see below).
  ```sh
  curl -s http://127.0.0.1:9090/metrics | grep wanbond_weighted_capacity_sane
  ```
  - `1` — **SANE-VERIFIED**: every path declares `link_bandwidth`.
  - `0` — **UNVERIFIABLE**: at least one path does not declare
    `link_bandwidth` — either none of them do, or only some do (a PARTIAL
    declaration, reachable whenever `scheduler.pacing_enabled` is left at its
    default `false`).
  - **Absent entirely** under `scheduler.policy = "active-backup"` (the
    default) — the check does not apply outside the weighted policy.

- **Startup WARN log line:** emitted once, at daemon start, exactly when the
  gauge above reads `0`:
  ```sh
  journalctl -u wanbond-edge | grep "weighted policy: link_bandwidth capacity is unverifiable"
  ```
  The record's `declared_paths`/`total_paths` fields name how many paths
  declared a bandwidth out of the total. **Remedy:** declare `link_bandwidth`
  on **all** paths so capacity can be checked, or verify
  `scheduler.per_path_capacity_fps` against the BDP sizing recipe in
  [§3a](#3a-tuning-per-link-bandwidth-and-pacing) instead.

A path that DOES declare `link_bandwidth` but contradicts the engage
threshold still hard-fails config load exactly as before (§3a) — this WARN is
about the paths that declare nothing at all, which the hard guard cannot
evaluate.

#### Failover-budget sanity (`[liveness]` `down_after` / `ride_through`)

Independent of scheduler policy, wanbond also checks that the per-path
liveness timing still permits transparent failover within the 3s recovery
deadline. The **analytical per-direction failover budget** is
`down_after` + the largest path `ride_through` + `2 × 200ms` (the fixed probe
interval); at the defaults (1200ms `down_after`, 0 `ride_through`) it is 1.6s,
comfortably inside 3s. Widening `[liveness].down_after` or a path's
`ride_through` far enough pushes that budget past the deadline — the tunnel
still comes up (this is **WARN-and-allow**, unlike the too-SMALL `down_after`
that is rejected outright), but failover will be slower than the P1 target.

- **`wanbond_liveness_budget_sane` Prometheus gauge:** an unlabeled 0/1 metric,
  present for **every** config (not policy-gated). Config-derived, seeded at
  startup and **re-set on a `reload`** whose applied path change moved the
  worst-case `ride_through`.
  ```sh
  curl -s http://127.0.0.1:9090/metrics | grep wanbond_liveness_budget_sane
  ```
  - `1` — within budget: the failover budget fits the 3s recovery deadline.
  - `0` — over budget: `down_after` + worst-case `ride_through` + `2×200ms`
    exceeds 3s.
- **Startup WARN log line:** emitted once, at daemon start, exactly when the
  gauge reads `0`:
  ```sh
  journalctl -u wanbond-edge | grep "liveness failover budget exceeds"
  ```
  The record names `down_after`, `worst_path`, `max_ride_through`,
  `failover_budget`, and `recovery_budget`. **Remedy:** reduce
  `[liveness].down_after` and/or the largest path `ride_through` so the sum
  fits 3s, or accept slower-than-P1 failover for this deployment.

### 6c. Monitoring UI (`[monitor]`)

An OPTIONAL dashboard — live per-peer throughput/loss/FEC sparklines —
complementing `/metrics`. It is read-only except for one loopback-only control
(`POST /api/exit`, see **Scope** below). Omit the block, or leave `listen`
empty, and no monitoring-UI endpoint is served (the daemon behaves exactly as
without this section):

```toml
[monitor]
listen = "127.0.0.1:9101"
# token = "..."           # required only if listen is non-loopback
# reveal_addressing = false # optional, default false; enables addressing
                             # disclosure to token holders on non-loopback
                             # binds (per-path sources, hub endpoints,
                             # concentrator connected-edge sources)
```

- **Bind rule**: `listen` follows the SAME loopback-only default as `[metrics]`,
  with one difference — a non-loopback `listen` is *allowed* here, but only as
  an explicit opt-in: it **requires `token` to be set**, and an unauthenticated
  non-loopback `listen` is **refused at config load** (fail-fast, before the
  daemon even attempts to bind), not merely at bind time. See [docs/design.md
  §Security model](design.md) for the full invariant and the accepted residual
  risk of running non-loopback.
- **Auth model**: every request — including the `/ws` WebSocket upgrade — is
  validated against the request's `Host` and `Origin` headers regardless of
  whether a token is configured (DNS-rebinding/CSRF defense, no secret
  needed). When `token` IS set, the browser presents it once as
  `?token=<value>` in the URL; the server verifies it, sets a
  `wanbond_monitor_token` cookie (`SameSite=Strict`, `HttpOnly`), and
  redirects (302) to the same URL with `?token=` stripped, so the token does
  not persist in the address bar or browser history. Subsequent requests
  (including the `Authorization: Bearer <token>` form, if you prefer curling
  it) authenticate off that credential.
- **How to view it**:
  - **Loopback (recommended, default)**: leave `listen` on its loopback
    default and reach it through an SSH tunnel — no LAN exposure at all:
    ```sh
    ssh -L 9101:127.0.0.1:9101 user@wanbond-host
    ```
    then open `http://127.0.0.1:9101/` locally.
  - **LAN (opt-in, requires a token)**: set `listen` to a non-loopback
    address and `token` to a secret, then browse to
    `http://<host>:9101/?token=<value>` once — the redirect leaves you at a
    plain `http://<host>:9101/` afterward. **Do this only on a LAN you trust**:
    the monitor speaks plain HTTP (no TLS in v1), so the token and all
    dashboard traffic are visible in CLEARTEXT to anyone on-path; see
    [docs/design.md §Security model](design.md) for the accepted risk this
    trades off.
- **Scope (v1)**: read-only EXCEPT one loopback-only control. The dashboard
  shows live stats only, with a single mutating action: `POST /api/exit`
  (`{"peer": "<name>"}`) switches the active exit-capable peer on a multi-exit
  edge, returning `200 {"activeExit": "<name>"}`. In the dashboard itself
  (T260) this is a `<select>` listing every exit-capable peer with the current
  `activeExit` marked, disabled while a switch is in flight, and showing a
  visible error notice (reverting the display) on a non-2xx or network
  failure — no manual `curl`/token handling needed in the browser. This
  control is **LOOPBACK-ONLY**: it is refused with **403 on any non-loopback
  bind, regardless of a valid token**, so a token'd LAN-exposed monitor stays
  strictly read-only — you can watch the exits from off-host but can only
  *switch* them from a loopback-bound session (e.g. over the SSH tunnel
  above); the dashboard hides the `<select>` entirely on a token'd
  non-loopback bind (mirroring this gate) and on a single-peer edge (nothing
  to switch to). The usual auth applies (cross-origin → 403, missing/invalid
  token → 401); a non-POST method is 405 and an unknown/non-exit-capable peer
  is 400. See [docs/design.md §Security model](design.md) for the full
  posture.
- **What you see**: beyond per-peer throughput/loss/FEC, the dashboard shows
  the daemon's role/version/uptime, each path's bind mode + bound device and
  declared link bandwidth/RTT, the truncated WireGuard public-key
  fingerprint, and the ordered hub-endpoint failover list with the active
  entry highlighted — all of these are shown **on ANY binding**, loopback or
  token'd non-loopback alike.
  - **On a loopback binding**, you ALSO see full per-path addressing (each
    path's bound source address and its current remote) and every endpoint's
    address in the failover list; on the concentrator role, each connected
    edge's observed source address is shown too.
  - **On a token'd non-loopback binding**, by default addressing is redacted
    server-side before it ever leaves the daemon: per-path cards show an
    "addressing hidden on non-loopback binding" placeholder instead of the
    source/remote, and the endpoint list shows only the active/standby
    state with the address blanked. This holds even though you supplied a
    valid token — the token authorizes the non-loopback bind itself, not
    disclosure of addressing. However, if the operator sets the default-off
    `reveal_addressing` opt-in in `[monitor]`, per-path source addresses,
    hub endpoint addresses, and (on the concentrator) connected-edge source
    addresses become visible to anyone holding the token; the token gate is
    still enforced and unaffected. Everything else (role/version/uptime/
    bind-mode/link-params/fingerprint) is shown in full regardless of
    binding or the addressing flag.
- **Build step**: the dashboard ships as an embedded frontend bundle built by
  `just web-build` (Vite + TypeScript), which `just build`/`just release` run
  automatically — see [§1](#1-build-the-release-binaries).

## 7. MTU

The daemon sets the TUN MTU itself from the bonded-overhead budget (see
`docs/p1-mtu.md`); do not override it. If an on-path MTU below the default
1500 is in play, see the TCP MSS-clamp guidance in that document.

**TCP MSS clamping is split by ownership (T208, D85).** TCP that the **edge
host itself originates** over `wanbond0` is clamped by the **daemon**: on the
edge role it installs a mangle/`OUTPUT`-chain `TCPMSS --clamp-mss-to-pmtu` rule
(IPv4 and IPv6) at bring-up and withdraws it on shutdown — the operator installs
nothing for this case. **Forwarded** (routed-LAN) TCP traverses the `FORWARD`
chain, which the daemon does NOT touch; the operator installs the matching
`FORWARD`-chain clamp on each forwarding node — see the C6 forwarding recipe
below and `docs/p1-mtu.md`. The two chains are disjoint (a SYN is either locally
originated or forwarded), so the daemon-owned OUTPUT clamp and the operator-owned
FORWARD clamp are complementary. **`iptables` is OPTIONAL** (T232/T233, D92):
when `iptables`/`ip6tables` are absent — an nft-only host such as Raspberry Pi
OS or a modern Debian/Ubuntu — the daemon programs the same OUTPUT clamp via the
`nft` CLI (a daemon-owned `table inet wanbond_mssclamp`), and if NEITHER front-end
is installed it logs a single WARN and continues. A missing MSS-clamp front-end
NEVER fails bring-up: the tunnel comes up regardless, and forwarded/routed-LAN
traffic relies on the operator `FORWARD` clamp (below) in any case.

A per-path `mtu` config key (§3z) lets an operator DECLARE that path's real
outer underlay MTU (e.g. a cellular APN capped below 1500) — it is validated
at config load (1280..9000, and the derived inner MTU must stay >= 576) but,
as of this writing, is not yet consumed when sizing the TUN's inner MTU; that
wiring is tracked separately. Declaring it today only documents and validates
the operator's intent for a future release.

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

## 9. Full-tunnel / client-LAN recipe (C3)

wanbond's primary use case: route an entire client LAN's traffic through the
bonded tunnel and out the concentrator's public IP. This is end-to-end
validated on the production Pi/o3 deploy (`wanbond-fixes.md`): edge = a
Raspberry Pi with two WAN uplinks arriving as VLAN sub-interfaces (one per
WAN, each pinned by `source_addr`) and a client LAN on a third VLAN;
concentrator = a public host reached over NAT. Substitute your own
addresses/interfaces throughout.

**A config-level literal `allowed_ips = ["0.0.0.0/0"]` is safe to write** —
the daemon unconditionally splits it into the wg-quick `/1`+`/1` pair at UAPI
render (§3's `splitDefaultRoute`), so the *engine* itself never receives the
literal `/0` that would otherwise wedge the WG handshake permanently (D35,
open: the underlying engine defect this daemon-side split works around).
Both routes below reach the same result — an edge that can route to the
whole internet through the tunnel — by relying on that daemon-guaranteed
split, never a raw `/0` at the engine boundary:

**Operator warning — a literal `0.0.0.0/0` default route installed OUTSIDE
wanbond bypasses the split above and recurses the underlay.** The daemon's
`splitDefaultRoute` (`internal/device/device.go:1324-1333`) only rewrites
`allowed_ips` entries the daemon itself renders (the engine's UAPI config,
and — under `mode = "default-route"`, §9.1 — the routes it installs into
`wanbond0`). It has no reach into routes an operator installs by other
means: a wg-quick `PostUp = ip route add default dev wanbond0`, a
NetworkManager connection profile, or a bare `ip route add default dev
wanbond0`. Any of those install the literal, unsplit `/0` as the box's
default route, which then also captures the encrypted underlay UDP packets
wanbond itself sends to the concentrator's `endpoint` — routing them back
INTO the tunnel they are trying to leave through. The underlay recurses and
the handshake never completes: this is the observed D35 production
symptom (receiver `rx` floods, `tx≈0`, no handshake). **Use `mode =
"default-route"` (§9.1)** — it installs the safe `/1`+`/1` split
automatically and never overrides the physical route to the concentrator's
underlay endpoint. If you must hand-roll the default route instead, either
exclude the concentrator's endpoint with a more-specific host route ahead
of the default, or install the `/1`+`/1` split yourself (§9.1's manual
`ip route add` fallback) rather than a literal `/0`.

### 9.1 Edge: install the default-route split (daemon-automated, §3)

Set `mode = "default-route"` on the concentrator peer alongside a
`0.0.0.0/0` (and, for IPv6, `::/0`) `allowed_ips` entry:

```toml
[[wireguard.peers]]
public_key = "<base64 concentrator public key>"
endpoint = "203.0.113.7:51820"
allowed_ips = ["0.0.0.0/0"]
mode = "default-route"             # see §3: edge-only, full-tunnel opt-in
```

The daemon splits `0.0.0.0/0` into the wg-quick `/1`+`/1` pair
(`0.0.0.0/1` + `128.0.0.0/1`) at UAPI render regardless of `mode` — so the
engine itself never sees the literal `/0` that wedges D35 — and, because
`mode = "default-route"` is set, ALSO installs that same split as scope-link
routes via `wanbond0` once the interface comes up, withdrawing them on stop.
This is the daemon's **only** route programming anywhere in this recipe;
everything below (9.2–9.3) is entirely operator-owned, same boundary as §3/§4.

If you would rather not opt into `mode` at all, install the equivalent
routes yourself instead (e.g. from the addressing oneshot, §4):

```sh
sudo ip route add 0.0.0.0/1 dev wanbond0
sudo ip route add 128.0.0.0/1 dev wanbond0
```

### 9.2 Edge: route the client LAN onto wanbond0 (operator-owned)

Policy-route the client subnet to `wanbond0` and **SNAT to the edge's own
tunnel address**, so the concentrator's per-edge `allowed_ips =
<edge-tunnel-ip>/32` (the ordinary point-to-point form, §3) still matches
the source address of forwarded traffic — no widening needed on the
concentrator side:

```sh
sudo sysctl -w net.ipv4.ip_forward=1
sudo ip rule add from 192.168.223.0/24 lookup 223
sudo ip route add default dev wanbond0 table 223
sudo iptables -t nat -A POSTROUTING -s 192.168.223.0/24 -o wanbond0 -j SNAT --to-source 10.77.0.2
```

(`192.168.223.0/24` / table `223` / `10.77.0.2` are the production Pi's
client-LAN VLAN, routing table, and tunnel address, per `wanbond-fixes.md`
§C3 — substitute your own client subnet, an unused table number, and the
edge's own tunnel address from its interface `Address=` in §4. **Not** the
`allowed_ips` in the edge's own peer config (§3/§9.1) — in the point-to-point
form (§3) that holds the *concentrator's* tunnel address as seen from the edge
(e.g. `10.77.0.1/32`); in this full-tunnel recipe (§9.1) it instead holds
`0.0.0.0/0`. In neither case is it the edge's own address.)

**Alternative — widen the concentrator's `allowed_ips` instead of SNAT-ing on
the edge:** the SNAT recipe above is the recommended, validated path. This
alternative trades the edge-side SNAT for **three** concentrator-side
operator-owned changes (not one) and has **not** been validated on the
production Pi/o3 deploy — read on only if you have a specific reason to avoid
SNAT-ing on the edge.

Without the edge-side SNAT, forwarded packets keep their original
client-subnet source (`192.168.223.0/24`) all the way to the concentrator.
That requires all three of:

1. **Widen the concentrator's peer `allowed_ips`** for this edge to
   `allowed_ips = ["10.77.0.2/32", "192.168.223.0/24"]`, so WireGuard's
   cryptokey routing accepts traffic sourced from the client subnet on this
   peer (skip the edge-side `iptables -t nat` step above).
2. **Widen the concentrator's `MASQUERADE -s`** (§9.3 / §5 C6) to also cover
   the client subnet, or replace it with a supernet covering both — the
   `-s <tunnel-net>` scoping (e.g. `10.77.0.0/24`) does **not** match the
   client-subnet source, so without this the client-outbound leg of forwarded
   traffic leaves the WAN NIC un-NATed with an RFC1918 source and is dropped
   as non-routable before it reaches the internet.
3. **Add a kernel route for the client subnet on the concentrator, back
   toward `wanbond0`:**
   ```sh
   sudo ip route add 192.168.223.0/24 dev wanbond0
   ```
   This step has **no daemon-programmed equivalent** on the concentrator: the
   daemon installs routes only for a peer with `mode = "default-route"`
   (`internal/device/device.go`'s `defaultRoutePrefixes`/`installRoutes`), and
   that mode is rejected outright on the concentrator role
   (`internal/config/config.go`: "mode ... is not meaningful for the
   concentrator role"). Widening `allowed_ips` in step 1 does not substitute
   for this route — WireGuard cryptokey routing only *selects a peer* for a
   packet the kernel has *already* routed into `wanbond0`; it installs no
   kernel route itself. Without step 3, a reply the concentrator receives on
   its WAN NIC gets conntrack de-NATed (by step 2) to a `192.168.223.x`
   destination, but the concentrator's kernel — which per §4's networkd file
   has only the on-link tunnel `/24`, no route for the client subnet — sends
   it out the concentrator's default route toward the WAN instead of into
   `wanbond0`, so it never reaches the edge and the return path dies
   regardless of steps 1–2.

Persist step 3 the same interface-keyed way §9.4 persists the edge's policy
routes: add the `ip route add` above to the concentrator's own addressing
oneshot (`wanbond-addressing@concentrator.service`, §4) alongside its other
route/address state, so it survives `wanbond0` being torn down and recreated
on daemon restart (unless `tun_persist = true`, §4 I7).

Put the `ip_forward` toggle, the `ip rule`/`ip route`, and (if used) the
`iptables -t nat` SNAT rule into the edge's addressing oneshot
(`wanbond-addressing@edge.service`, §4) — `wanbond0` and everything routed
through it are torn down and recreated on every daemon restart unless
`tun_persist = true` (§4, I7), so these must be re-applied the same way the
existing persistence recipe re-applies addressing.

**MSS-clamp forwarded TCP — REQUIRED, on both forwarding nodes.** Splitting
`0.0.0.0/1`+`128.0.0.0/1` (§9.1) narrows the *tunnel* MTU, but TCP endpoints
behind either forwarding node — the client LAN behind the edge, or the
concentrator's own downstream (§9.3) — negotiate their MSS off their own link
MTU and happily emit oversized segments once wrapped. Clamp the MSS of
**forwarded** SYNs on **both** forwarding nodes — the edge (this section) and
the concentrator (§9.3) — to the tunnel's inner MTU; see [docs/p1-mtu.md §MSS
clamping](p1-mtu.md) for the inner-MTU-minus-headers arithmetic (IPv4:
`1400 − 40 = 1360` bytes at the default 1500-byte path MTU; IPv6:
`1380 − 60 = 1320` bytes):

```sh
sudo iptables  -t mangle -A FORWARD -o wanbond0 -p tcp --tcp-flags SYN,RST SYN \
               -j TCPMSS --clamp-mss-to-pmtu
sudo ip6tables -t mangle -A FORWARD -o wanbond0 -p tcp --tcp-flags SYN,RST SYN \
               -j TCPMSS --clamp-mss-to-pmtu
```

`--clamp-mss-to-pmtu` is preferred over a fixed `--set-mss <n>`: it derives
the MSS from the tunnel interface's live MTU, so it tracks any inner-MTU
retuning (amnezia junk prefixes, a lower real path MTU) automatically instead
of going stale. **Without this rule, forwarded TCP connections emit segments
the tunnel cannot carry whole — they either IP-fragment (the loss-amplification
risk in p1-mtu.md) or, more commonly, hit a PMTUD black hole and stall
silently** — the D65 compounding fault this recipe closes. Like the rest of
§9.2–9.3, this is an **operator** step: the daemon owns only the tunnel engine
(`internal/device`) and installs no firewall/mangle rules of its own.

Persist each node's clamp rule the same way this recipe already persists that
node's other rules (§9.4): fold the edge's `iptables -t mangle` insert above
into `wanbond-addressing@edge.service` alongside the policy-route/SNAT rules;
persist the concentrator's equivalent with `netfilter-persistent save`,
alongside its other `iptables`/`ip6tables` rules (§9.3/§5).

### 9.3 Concentrator: NAT and forward the tunnel traffic out the WAN (operator-owned)

See §5 "Concentrator: NAT/forwarding prerequisites for routed traffic (C6)"
— `ip_forward`, `MASQUERADE`, and the `FORWARD` established/related accept
are all required for this recipe and, like 9.2, are entirely
operator-owned: the daemon programs none of them (§3's `mode` boundary
covers routes only, never NAT/forwarding/policy-routing). Apply the concentrator
half of §9.2's MSS-clamp rules here too — the same `iptables`/`ip6tables -t
mangle -A FORWARD -o wanbond0 ... -j TCPMSS --clamp-mss-to-pmtu` pair, applied
on this node.

### 9.4 Persistence

Both the edge's policy-route/SNAT rules (9.2) and the concentrator's
NAT/forward rules (§5 C6) reference `wanbond0` or its addresses and so must
survive both reboots and daemon restarts. Use the existing §4
`wanbond-addressing@<role>.service` oneshot for anything keyed to the
interface (9.2's `ip rule`/`ip route`, and the `iptables -t nat` SNAT rule
if you took the SNAT branch above), and `netfilter-persistent save` (§5) for
the concentrator's `iptables`/`ip6tables` rules, which persist independently
of the interface. `net.ipv4.ip_forward` persists on its own once written to
`/etc/sysctl.d/` (§5 C6). The MSS-clamp rules (9.2) follow the same split:
the edge's into its addressing oneshot, the concentrator's into
`netfilter-persistent save`.
