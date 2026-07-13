# wanbond — install and operations

wanbond ships as a single static binary. One binary serves both roles — the
**edge** (the mobile Linux box bonding the WAN uplinks) and the **concentrator**
(the public-IP VPS terminating the tunnel); the role is selected by the `role`
key in the config file, never by which binary is invoked.

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

Toward the end of the output, you will see a `bdp` sub-test log:

```
path capped BDP: idle RTT=5.0ms loaded RTT=...ms (bufferbloat Δ=...ms) | 
  achieved throughput=47.3 Mbit/s | BDP=... bytes (...frames @ 1517B/frame) | 
  SizePacingFromBDP -> capacityFPS=... burstFrames=...
```

The **achieved throughput** (47.3 Mbit/s in this example) is a measured point;
the fixture builds a sustained queue by running iperf3 under a controlled bandwidth
cap, so it measures the true link-limited throughput (not CPU-bound).

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

### Interface addressing and routing (operator-owned)

The daemon creates the TUN interface (`wanbond0`) and owns ONLY the tunnel
engine — **it never assigns addresses or routes** (wg-quick style, no
privileged shell-outs). Assign them with a **systemd-networkd `.network`
file**, using the inner addresses from `allowed_ips`:

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
